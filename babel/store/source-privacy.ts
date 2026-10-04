import type { GuestDatabase, GuestSqlParam } from "@manifold/plugin-kit";
import {
  MAX_SESSION_EXCLUSIONS,
  PRIVACY_PROJECTION_BUILDING,
  type NavigationMapSource,
} from "../contract.ts";
import { sessionIsExcluded } from "./exclusions.ts";
import { backfillSourceDependencies } from "./source-dependencies.ts";
import { backfillSourceTaint, SOURCE_TAINT_READY, sourcePrivacyClosure } from "./source-taint.ts";

type Database = Pick<GuestDatabase, "query">;
const PAGE = 2000;

// Compound readers use the same persisted fence. Incomplete upgrades remain conservative;
// only prospective owner choices and transaction-maintenance writes traverse the graph.
const TAINT = `WITH conservative_state AS MATERIALIZED (
  SELECT 1 WHERE NOT (${SOURCE_TAINT_READY}) AND EXISTS (SELECT 1 FROM session_exclusions)
),tainted(kind,id) AS (
  SELECT kind,id FROM source_taint WHERE (${SOURCE_TAINT_READY})
  UNION ALL SELECT 'session',selector FROM conservative_state CROSS JOIN session_exclusions
  UNION ALL SELECT 'session',selector FROM conservative_state CROSS JOIN sessions
  UNION ALL SELECT 'record',id FROM conservative_state CROSS JOIN records
  UNION ALL SELECT 'root',root_id FROM conservative_state CROSS JOIN records
  UNION ALL SELECT 'run',id FROM conservative_state CROSS JOIN runs
  UNION ALL SELECT 'capture',id FROM conservative_state CROSS JOIN transcript_map_captures
  UNION ALL SELECT 'entity',id FROM conservative_state CROSS JOIN entities
  UNION ALL SELECT 'question',id FROM conservative_state CROSS JOIN questions
  UNION ALL SELECT 'plan',id FROM conservative_state CROSS JOIN plans
  UNION ALL SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_plans
  UNION ALL SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_nodes
  UNION ALL SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_versions
  UNION ALL SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_summaries
)`;

/** Shared persisted fence for compound readers; prospective owner choices traverse once. */
export function sourcePrivacyCTE(additionalSelectors: readonly string[] = []): {
  readonly sql: string;
  readonly params: readonly GuestSqlParam[];
} {
  return additionalSelectors.length === 0
    ? { sql: TAINT, params: [] }
    : sourcePrivacyClosure(additionalSelectors);
}

async function readExcludedIds(
  db: Database,
  kind: "record" | "run" | "capture",
  additionalSelectors: readonly string[],
): Promise<ReadonlySet<string>> {
  const ids = new Set<string>();
  if (
    additionalSelectors.length === 0 &&
    (await db.query("SELECT 1 FROM session_exclusions LIMIT 1")).length === 0
  )
    return ids;
  const ready = (await db.query(`SELECT 1 WHERE ${SOURCE_TAINT_READY}`)).length !== 0;
  if (!ready && additionalSelectors.length !== 0) throw new Error(PRIVACY_PROJECTION_BUILDING);
  const privacy = sourcePrivacyCTE(additionalSelectors);
  let cursor = "";
  for (;;) {
    const rows = await db.query<{ id: string }>(
      ready
        ? `${privacy.sql} SELECT id FROM tainted WHERE kind = ? AND id > ? ORDER BY id LIMIT ?`
        : `SELECT id FROM ${kind === "record" ? "records" : kind === "run" ? "runs" : "transcript_map_captures"}
          WHERE id > ? ORDER BY id LIMIT ?`,
      ready ? [...privacy.params, kind, cursor, PAGE] : [cursor, PAGE],
    );
    for (const row of rows) ids.add(row.id);
    if (rows.length < PAGE) return ids;
    cursor = rows[rows.length - 1]!.id;
  }
}

/** Whole derived revisions, including uncited material served to their original source run. */
export async function readExcludedRecordIds(
  db: Database,
  additionalSelectors: readonly string[] = [],
): Promise<ReadonlySet<string>> {
  return await readExcludedIds(db, "record", additionalSelectors);
}

/** Also supports checking an owner choice before recording its immutable exclusion. */
export async function readExcludedRunIds(
  db: Database,
  additionalSelectors: readonly string[] = [],
): Promise<ReadonlySet<string>> {
  return await readExcludedIds(db, "run", additionalSelectors);
}

/** Whole saved navigation sources, including summaries reused from contaminated captures. */
export async function readExcludedCaptureIds(
  db: Database,
  additionalSelectors: readonly string[] = [],
): Promise<ReadonlySet<string>> {
  return await readExcludedIds(db, "capture", additionalSelectors);
}

/** A fixed, caller-authored alias expression; the source closure stays uncorrelated. */
export function sourcePrivacyCondition(
  kind: "record" | "run" | "capture" | "entity" | "question",
  idSql: string,
): { readonly sql: string; readonly params: readonly GuestSqlParam[] } {
  return {
    // Read the immutable ledger and durable build fence here, then seek the compact primary key.
    // Bind caller-authored expressions once, before our kind parameters. Preserve NOT IN's
    // nullable-subject behavior as well as bare `id` aliases in the caller's scope.
    sql: `CASE WHEN NOT EXISTS (SELECT 1 FROM session_exclusions) THEN 1
      WHEN NOT (${SOURCE_TAINT_READY}) THEN 0
      ELSE (WITH source_privacy_value AS (SELECT ${idSql} AS privacy_id)
        SELECT CASE WHEN privacy_id IS NULL THEN CASE
          WHEN EXISTS (SELECT 1 FROM source_taint t WHERE t.kind=?) THEN NULL ELSE 1 END
        ELSE NOT EXISTS (SELECT 1 FROM source_taint t WHERE t.kind=? AND t.id=privacy_id) END
        FROM source_privacy_value) END`,
    params: [kind, kind],
  };
}

/** Reused inside the transaction retaining model intent, not a stale in-memory taint snapshot. */
export function modelPrivacyGuard(
  selectors: readonly string[] = [],
  recordIds: readonly string[] = [],
  runIds: readonly string[] = [],
  captureIds: readonly string[] = [],
): { readonly sql: string; readonly params: readonly GuestSqlParam[] } {
  return {
    sql: `CASE WHEN NOT EXISTS (SELECT 1 FROM session_exclusions) THEN 1
      WHEN NOT (${SOURCE_TAINT_READY}) THEN 0
      ELSE NOT EXISTS (SELECT 1 FROM json_each(?) j JOIN source_taint t ON t.kind='session' AND t.id=j.value)
        AND NOT EXISTS (SELECT 1 FROM json_each(?) j JOIN source_taint t ON t.kind='record' AND t.id=j.value)
        AND NOT EXISTS (SELECT 1 FROM json_each(?) j JOIN source_taint t ON t.kind='run' AND t.id=j.value)
        AND NOT EXISTS (SELECT 1 FROM json_each(?) j JOIN source_taint t ON t.kind='capture' AND t.id=j.value) END`,
    params: [
      JSON.stringify(selectors),
      JSON.stringify(recordIds),
      JSON.stringify(runIds),
      JSON.stringify(captureIds),
    ],
  };
}

/** A concurrent source-bearing reservation wins first or the immutable owner ban does. */
export async function excludeSessionWhenQuiescent(
  db: Pick<GuestDatabase, "query" | "batch">,
  selector: string,
  actorId: string,
  recordedAt: string,
): Promise<boolean> {
  if (!(await backfillSourceDependencies(db)) || !(await backfillSourceTaint(db))) return false;
  const prospective = sourcePrivacyClosure([selector]);
  const privacy = sourcePrivacyCondition("record", "record_terms.record_id");
  const titlePrivacy = sourcePrivacyCondition("run", "title.run_id");
  await db.batch([
    {
      sql: `INSERT INTO session_exclusions(selector,actor_id,recorded_at)
      SELECT ?,?,? WHERE (${SOURCE_TAINT_READY}) AND NOT EXISTS (
        ${prospective.sql} SELECT 1 FROM tainted t JOIN runs r ON r.id = t.id
          WHERE t.kind = 'run' AND r.closure IS NULL)
      AND coalesce((SELECT json_extract(payload,'$.enabled') FROM policies ORDER BY seq DESC LIMIT 1),0) != 1
      AND NOT EXISTS (SELECT 1 FROM drains WHERE state IN ('running','closing'))
      AND (SELECT count(*) FROM session_exclusions) < ?
      ON CONFLICT(selector) DO NOTHING`,
      params: [selector, actorId, recordedAt, ...prospective.params, MAX_SESSION_EXCLUSIONS],
    },
    {
      sql: `UPDATE sessions SET title=NULL,title_provenance=NULL
      WHERE title_provenance='inferred' AND selector IN (
        SELECT title.selector FROM session_titles title WHERE NOT (${titlePrivacy.sql}))`,
      params: titlePrivacy.params,
    },
    {
      // Keyword corpus statistics must stop depending on quarantined text in the same commit.
      sql: `DELETE FROM record_terms WHERE NOT (${privacy.sql})`,
      params: privacy.params,
    },
  ]);
  return await sessionIsExcluded(db, selector);
}

/** Saved neighborhood input is indivisible: summaries may have consumed any of its rows. */
export async function mappingSourceIsExcluded(
  db: Database,
  source: NavigationMapSource,
): Promise<boolean> {
  const guard = modelPrivacyGuard("kind" in source ? [] : [source.session], [], [], [source.id]);
  return (await db.query(`SELECT 1 WHERE NOT (${guard.sql})`, guard.params)).length !== 0;
}
