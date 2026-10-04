import type { GuestDatabase, GuestSqlParam } from "@manifold/plugin-kit";
import {
  HARNESSES,
  MAX_SESSION_EXCLUSIONS,
  PRIVACY_PROJECTION_BUILDING,
  RECORD_KINDS,
  type NavigationMapSource,
} from "../contract.ts";
import { sessionIsExcluded } from "./exclusions.ts";
import {
  backfillSourceDependencies,
  SOURCE_DEPENDENCY_KINDS as KINDS,
  SOURCE_DEPENDENCY_READY,
  sourceDependenciesReady,
} from "./source-dependencies.ts";

type Database = Pick<GuestDatabase, "query">;
const PAGE = 2000;

// The projection contains input identities, not taint. Resolve catalog-dependent references
// against the current rows and read the immutable exclusion ledger in this same statement.
const RECORDS = RECORD_KINDS.map((kind) => `'${kind}'`).join(",");
const INPUT_KIND = `CASE
  WHEN d.input_kind IN (${KINDS}) THEN d.input_kind
  WHEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id AND r.kind=d.input_kind) THEN 'record'
  WHEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id) THEN 'capture'
  WHEN EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_versions v WHERE v.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_plans p WHERE p.id=d.input_id) THEN 'map'
  ELSE d.input_kind END`;
const ROW_GUARD = `(d.consumer_record_kind IS NULL OR EXISTS (
    SELECT 1 FROM records r WHERE r.id=d.consumer_id AND r.kind=d.consumer_record_kind))
  AND CASE d.input_catalog
    WHEN 'record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id
      AND (d.input_record_kind IS NULL OR r.kind=d.input_record_kind))
    WHEN 'entity' THEN EXISTS (SELECT 1 FROM entities e WHERE e.id=d.input_id)
    WHEN 'question' THEN EXISTS (SELECT 1 FROM questions q WHERE q.id=d.input_id)
    WHEN 'capture' THEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id)
    WHEN 'map_node_summary' THEN
      EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END`;
const BARE_SESSION = `NOT EXISTS (SELECT 1 FROM sessions exact WHERE exact.selector=d.input_id)
  AND substr(d.input_id,1,instr(d.input_id,'/')-1)
    NOT IN (${HARNESSES.map((harness) => `'${harness}'`).join(",")})`;

// Separate recursive arms retain index lookups instead of normalizing/materializing the
// entire graph at admission. UNION bounds cycles by finite persisted (kind,id) identities.
const TAINT = `WITH RECURSIVE
projection_state(ready) AS MATERIALIZED (SELECT ${SOURCE_DEPENDENCY_READY}),
additional(id) AS MATERIALIZED (SELECT value FROM json_each(?)),
active_taint(kind,id) AS (
  SELECT 'session',selector FROM session_exclusions WHERE (SELECT ready FROM projection_state)=1
  UNION
  SELECT 'session',id FROM additional WHERE (SELECT ready FROM projection_state)=1
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency'
      AND d.input_kind=t.kind AND d.input_id=t.id
    WHERE d.input_kind IN (${KINDS}) AND ${ROW_GUARD}
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_generic ON d.relation='dependency' AND d.input_id=t.id
    WHERE t.kind IN ('record','capture','map','plan')
      AND (t.kind<>'record' OR d.input_kind IN (${RECORDS}))
      AND (t.kind<>'plan' OR d.input_kind='plan')
      AND (d.input_kind IS NULL OR d.input_kind NOT IN (${KINDS}))
      AND t.kind=${INPUT_KIND} AND ${ROW_GUARD}
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency'
      AND d.input_kind='session' AND s.source_id=d.input_id
    WHERE ${BARE_SESSION} AND ${ROW_GUARD}
  UNION
  -- A prepare producer's payload was also a document of each consuming run. The join stays
  -- live so later job assignment, preparation binding and producer updates cannot go stale.
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency'
      AND d.input_kind=t.kind AND d.input_id=t.id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND d.input_kind IN (${KINDS})
  UNION
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency'
      AND d.input_kind='session' AND s.source_id=d.input_id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND ${BARE_SESSION}
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector
    WHERE d.detail=title.title
  UNION
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND d.detail=title.title
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN sessions s ON s.selector=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='current_title' AND d.input_id=title.selector
    WHERE s.title_provenance='inferred' AND s.title=title.title AND ${ROW_GUARD}
  UNION
  SELECT 'run',consumer.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id
    CROSS JOIN runs consumer ON consumer.id=d.consumer_id
    WHERE consumer.started_at>=title.inferred_at
  UNION
  SELECT 'run',consumer.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs consumer ON consumer.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND consumer.started_at>=title.inferred_at
  UNION
  SELECT 'capture',p.capture_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='binding_capture' AND t.kind='map' AND d.input_id=t.id
    CROSS JOIN transcript_map_versions v ON v.id=d.consumer_id
    CROSS JOIN transcript_map_plans p ON p.id=v.plan_id
  UNION
  -- Applied output identities remain joined to the current catalog. Accepting model wording
  -- attributes the act to the operator without cutting its proposal/proposing-run lineage.
  SELECT 'entity',e.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN entities e ON e.id=d.input_id
  UNION
  SELECT 'entity',f.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN facts f ON f.id=d.input_id
  UNION
  SELECT 'entity',m.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN resolution_members m ON m.resolution_id=d.input_id
  UNION
  SELECT 'record',f.record_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN filings f ON f.id=d.input_id
  UNION
  SELECT 'entity',f.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN filings f ON f.id=d.input_id
  UNION
  SELECT 'record',r.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN records r ON r.id=d.input_id
  UNION
  SELECT 'question',q.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN questions q ON q.id=d.input_id
),
conservative_state AS MATERIALIZED (
  SELECT 1 FROM projection_state WHERE ready=0
    AND (EXISTS (SELECT 1 FROM session_exclusions) OR EXISTS (SELECT 1 FROM additional))
),
conservative(kind,id) AS (
  SELECT 'session',selector FROM conservative_state CROSS JOIN session_exclusions
  UNION SELECT 'session',id FROM conservative_state CROSS JOIN additional
  UNION SELECT 'session',selector FROM conservative_state CROSS JOIN sessions
  UNION SELECT 'record',id FROM conservative_state CROSS JOIN records
  UNION SELECT 'root',root_id FROM conservative_state CROSS JOIN records
  UNION SELECT 'run',id FROM conservative_state CROSS JOIN runs
  UNION SELECT 'capture',id FROM conservative_state CROSS JOIN transcript_map_captures
  UNION SELECT 'entity',id FROM conservative_state CROSS JOIN entities
  UNION SELECT 'question',id FROM conservative_state CROSS JOIN questions
  UNION SELECT 'plan',id FROM conservative_state CROSS JOIN plans
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_plans
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_nodes
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_versions
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_summaries
),
tainted(kind,id) AS (
  SELECT kind,id FROM active_taint UNION ALL SELECT kind,id FROM conservative
)`;

/** One shared closure for compound, fixed-alias readers of the retained ledger. */
export function sourcePrivacyCTE(additionalSelectors: readonly string[] = []): {
  readonly sql: string;
  readonly params: readonly GuestSqlParam[];
} {
  return { sql: TAINT, params: [JSON.stringify(additionalSelectors)] };
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
  const ready = await sourceDependenciesReady(db);
  if (!ready && additionalSelectors.length !== 0) throw new Error(PRIVACY_PROJECTION_BUILDING);
  let cursor = "";
  for (;;) {
    const rows = await db.query<{ id: string }>(
      ready
        ? `${TAINT} SELECT id FROM tainted WHERE kind = ? AND id > ? ORDER BY id LIMIT ?`
        : `SELECT id FROM ${kind === "record" ? "records" : kind === "run" ? "runs" : "transcript_map_captures"}
          WHERE id > ? ORDER BY id LIMIT ?`,
      ready ? [JSON.stringify(additionalSelectors), kind, cursor, PAGE] : [cursor, PAGE],
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
    // SQLite evaluates only the selected CASE arm. Read the immutable ledger in this same
    // statement: the first ban immediately enables the full fence, without a cached bypass.
    sql: `CASE WHEN NOT EXISTS (SELECT 1 FROM session_exclusions) THEN 1
      WHEN NOT (${SOURCE_DEPENDENCY_READY}) THEN 0
      ELSE ${idSql} NOT IN (${TAINT} SELECT id FROM tainted WHERE kind = ?) END`,
    params: ["[]", kind],
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
      WHEN NOT (${SOURCE_DEPENDENCY_READY}) THEN 0
      ELSE NOT EXISTS (${TAINT} SELECT 1 FROM tainted
      WHERE (kind = 'session' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'record' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'run' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'capture' AND id IN (SELECT value FROM json_each(?)))) END`,
    params: [
      "[]",
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
  if (!(await backfillSourceDependencies(db))) return false;
  const privacy = sourcePrivacyCondition("record", "record_terms.record_id");
  const titlePrivacy = sourcePrivacyCondition("run", "title.run_id");
  await db.batch([
    {
      sql: `INSERT INTO session_exclusions(selector,actor_id,recorded_at)
      SELECT ?,?,? WHERE (${SOURCE_DEPENDENCY_READY}) AND NOT EXISTS (
        ${TAINT} SELECT 1 FROM tainted t JOIN runs r ON r.id = t.id
          WHERE t.kind = 'run' AND r.closure IS NULL)
      AND coalesce((SELECT json_extract(payload,'$.enabled') FROM policies ORDER BY seq DESC LIMIT 1),0) != 1
      AND NOT EXISTS (SELECT 1 FROM drains WHERE state IN ('running','closing'))
      AND (SELECT count(*) FROM session_exclusions) < ?
      ON CONFLICT(selector) DO NOTHING`,
      params: [selector, actorId, recordedAt, JSON.stringify([selector]), MAX_SESSION_EXCLUSIONS],
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
