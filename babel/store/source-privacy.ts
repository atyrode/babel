import type { GuestDatabase, GuestSqlParam } from "@manifold/plugin-kit";
import { INPUT_FIELD, MAX_SESSION_EXCLUSIONS, type NavigationMapSource } from "../contract.ts";
import { sessionIsExcluded } from "./exclusions.ts";

type Database = Pick<GuestDatabase, "query">;
const PAGE = 2000;

// Dependencies point from a consumer to its inputs. UNION in the recursive walk bounds cycles
// by finite persisted identities; no payload or transcript is copied out of SQLite.
const TAINT = `WITH RECURSIVE
  documents(run_id, document) AS (
    SELECT id, CASE WHEN json_valid(preparation) THEN preparation ELSE '{}' END FROM runs
    UNION ALL
    SELECT id, CASE WHEN json_valid(payload) THEN payload ELSE '{}' END FROM runs
    UNION ALL
    SELECT id, json_extract(payload, '$.preparation') FROM runs
      WHERE json_valid(payload) AND json_type(payload, '$.preparation') = 'object'
    UNION ALL
    SELECT id, json_extract(preparation, '$.nativeRequest.input.${INPUT_FIELD}') FROM runs
      WHERE json_valid(preparation)
        AND json_valid(json_extract(preparation, '$.nativeRequest.input.${INPUT_FIELD}'))
    UNION ALL
    SELECT r.id, CASE WHEN json_valid(p.payload) THEN p.payload ELSE '{}' END
      FROM runs r JOIN runs p ON p.job_id = r.prepare_job_id
  ),
  session_inputs(run_id, selector) AS (
    SELECT d.run_id, j.value FROM documents d, json_each(d.document, '$.selectors') j
    UNION ALL
    SELECT d.run_id, j.value FROM documents d, json_each(d.document, '$.titles.selectors') j
    UNION ALL
    SELECT d.run_id, j.value FROM documents d, json_each(d.document, '$.analysis.selectors') j
    UNION ALL
    SELECT d.run_id, coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.harness') || '/' || json_extract(j.value, '$.sourceId'))
      FROM documents d, json_each(d.document, '$.material.sessions') j
    UNION ALL
    SELECT d.run_id, coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.harness') || '/' ||
        coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
      FROM documents d, json_each(d.document, '$.selection') j WHERE j.type = 'object'
    UNION ALL
    SELECT d.run_id, json_extract(j.value, '$.harness') || '/' || json_extract(j.value, '$.sourceId')
      FROM documents d, json_each(d.document, '$.captures') c, json_each(c.value, '$.sessions') j
    UNION ALL
    SELECT d.run_id, json_extract(d.document, '$.mapping.details.plan.source.session') FROM documents d
  ),
  record_inputs(run_id, record_id) AS (
    SELECT d.run_id, json_extract(j.value, '$.id')
      FROM documents d, json_each(d.document, '$.analysis.brief') j
    UNION ALL
    SELECT d.run_id, json_extract(d.document, '$.review.recordId') FROM documents d
    UNION ALL
    SELECT d.run_id, json_extract(d.document, '$.review.revisionId') FROM documents d
    UNION ALL
    SELECT d.run_id, json_extract(d.document, '$.embedding.recordId') FROM documents d
  ),
  neighborhood_rows(capture_id, document) AS (
    SELECT n.capture_id, j.value FROM transcript_map_neighborhood_inputs n,
      json_each('[' || rtrim(replace(n.text, char(10), ','), ',') || ']') j
  ),
  dependencies(consumer_kind, consumer_id, input_kind, input_id) AS (
    SELECT 'run', run_id, 'session', selector FROM session_inputs WHERE selector IS NOT NULL
    UNION ALL
    SELECT 'run', run_id, 'record', record_id FROM record_inputs WHERE record_id IS NOT NULL
    UNION ALL
    SELECT 'record', r.id, 'run', r.run_id FROM records r WHERE r.run_id IS NOT NULL
    UNION ALL
    SELECT 'run', r.run_id, 'record', r.id FROM records r WHERE r.run_id IS NOT NULL
    UNION ALL
    SELECT 'record', id, 'run', actor_id FROM records WHERE actor_kind = 'run'
    UNION ALL
    SELECT 'run', actor_id, 'record', id FROM records WHERE actor_kind = 'run'
    UNION ALL
    SELECT 'record', r.id, 'session', coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.sessionRef'),
      CASE WHEN json_extract(j.value, '$.kind') = 'session' THEN json_extract(j.value, '$.id') END,
      json_extract(j.value, '$.harness') || '/' ||
      coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
      FROM records r, json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.type = 'object'
    UNION ALL
    SELECT 'record', r.id, 'session', j.value FROM records r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('session', 'selector', 'sessionRef')
    UNION ALL
    SELECT 'record', record_id, 'run', run_id FROM assessments
    UNION ALL
    SELECT 'record', revision_id, 'run', run_id FROM assessments WHERE revision_id <> record_id
    UNION ALL
    SELECT 'record', record_id, 'run', run_id FROM status_events WHERE run_id IS NOT NULL
    UNION ALL
    SELECT 'record', record_id, 'run', actor_id FROM status_events WHERE actor_kind = 'run'
    UNION ALL
    SELECT 'record', record_id, 'run', proposed_by_id FROM next_actions WHERE proposed_by_kind = 'run'
    UNION ALL
    SELECT 'record', r.id, 'run', e.actor_id FROM edges e
      JOIN records r ON (r.id = e.from_id AND r.kind = e.from_kind)
        OR (r.id = e.to_id AND r.kind = e.to_kind) WHERE e.actor_kind = 'run'
    UNION ALL
    SELECT 'entity', e.from_id, 'run', e.actor_id FROM edges e
      WHERE e.actor_kind = 'run' AND e.from_kind = 'entity'
    UNION ALL
    SELECT 'entity', e.to_id, 'run', e.actor_id FROM edges e
      WHERE e.actor_kind = 'run' AND e.to_kind = 'entity'
    UNION ALL
    SELECT 'record', r.id, 'record', r.parent_id FROM records r WHERE r.parent_id IS NOT NULL
    UNION ALL
    SELECT 'record', id, 'root', root_id FROM records
    UNION ALL
    SELECT 'root', root_id, 'record', id FROM records
    UNION ALL
    SELECT 'record', r.id, 'session', e.to_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
      WHERE e.to_kind = 'session'
    UNION ALL
    SELECT 'record', r.id, 'record', target.id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
      JOIN records target ON target.id = e.to_id AND target.kind = e.to_kind
    UNION ALL
    SELECT 'record', r.id, 'run', title.run_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
      JOIN session_titles title ON title.selector = e.to_id WHERE e.to_kind = 'session'
    UNION ALL
    SELECT 'entity', id, 'run', created_by FROM entities
    UNION ALL
    SELECT 'entity', id, 'record', created_by FROM entities
    UNION ALL
    SELECT 'entity', id, 'entity', canonical_id FROM entities
    UNION ALL
    SELECT 'entity', canonical_id, 'entity', id FROM entities
    UNION ALL
    SELECT 'entity', entity_id, 'run', authority_id FROM facts WHERE authority_kind = 'run'
    UNION ALL
    SELECT 'entity', entity_id, 'entity', object_id FROM facts WHERE object_id IS NOT NULL
    UNION ALL
    SELECT 'record', r.id, 'entity', e.to_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind WHERE e.to_kind = 'entity'
    UNION ALL
    SELECT 'record', record_id, 'entity', entity_id FROM filings
    UNION ALL
    SELECT 'record', record_id, 'run', author_id FROM filings WHERE author_kind = 'run'
    UNION ALL
    SELECT 'entity', entity_id, 'run', author_id FROM filings WHERE author_kind = 'run'
    UNION ALL
    SELECT 'question', id, 'run', raised_by_id FROM questions WHERE raised_by_kind = 'run'
    UNION ALL
    SELECT 'question', q.id, 'session', j.value FROM questions q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('session', 'selector', 'sessionRef')
    UNION ALL
    SELECT 'question', q.id, 'session', coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.sessionRef'),
      CASE WHEN json_extract(j.value, '$.kind') = 'session' THEN json_extract(j.value, '$.id') END,
      json_extract(j.value, '$.harness') || '/' ||
      coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
      FROM questions q, json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'object'
    UNION ALL
    SELECT 'question', q.id, 'session', j.value FROM questions q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j
      WHERE j.type = 'text'
    UNION ALL
    SELECT 'question', q.id, 'record', r.id FROM questions q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j
      JOIN records r ON r.id = j.value WHERE j.type = 'text'
    UNION ALL
    SELECT 'question', q.id, 'entity', e.id FROM questions q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j
      JOIN entities e ON e.id = j.value WHERE j.type = 'text'
    UNION ALL
    SELECT 'question', q.id, 'record', r.id FROM questions q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      JOIN records r ON r.id = j.value
      WHERE j.type = 'text' AND j.key IN ('id', 'record', 'recordId', 'record_id', 'fromId', 'toId')
    UNION ALL
    SELECT 'question', q.id, 'entity', e.id FROM questions q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      JOIN entities e ON e.id = j.value
      WHERE j.type = 'text' AND j.key IN ('id', 'entityId', 'entity_id', 'objectId', 'subjectId')
    UNION ALL
    SELECT 'record', r.id, 'question', e.to_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind WHERE e.to_kind = 'question'
    UNION ALL
    SELECT 'capture', id, 'session', session FROM transcript_map_captures
    UNION ALL
    SELECT 'map', id, 'capture', capture_id FROM transcript_map_plans
    UNION ALL
    SELECT 'map', id, 'map', plan_id FROM transcript_map_nodes
    UNION ALL
    SELECT 'map', id, 'map', plan_id FROM transcript_map_versions
    UNION ALL
    SELECT 'map', id, 'map', version_id FROM transcript_map_summaries
    UNION ALL
    SELECT 'map', id, 'run', json_extract(payload, '$.runId') FROM transcript_map_summaries
    UNION ALL
    SELECT 'map', version_id, 'map', summary_id FROM transcript_map_bindings
    UNION ALL
    SELECT 'capture', p.capture_id, 'map', b.summary_id FROM transcript_map_bindings b
      JOIN transcript_map_versions v ON v.id = b.version_id
      JOIN transcript_map_plans p ON p.id = v.plan_id
    UNION ALL
    SELECT 'record', r.id, 'map', e.to_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
      WHERE EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id = e.to_id)
         OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id = e.to_id)
    UNION ALL
    SELECT 'record', r.id, 'capture', e.to_id FROM edges e
      JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
      JOIN transcript_map_captures c ON c.id = e.to_id
    UNION ALL
    SELECT 'record', r.id, 'map', j.value FROM records r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.key IN ('nodeId','versionId','summaryId') AND j.type = 'text'
    UNION ALL
    SELECT 'record', r.id, 'capture', j.value FROM records r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.key = 'captureId' AND j.type = 'text'
    UNION ALL
    SELECT 'run', d.run_id, 'map', json_extract(j.value, '$.summaryId')
      FROM documents d, json_each(d.document, '$.mapping.details.work.children') j
    UNION ALL
    SELECT 'run', d.run_id, 'map', json_extract(d.document, '$.mapping.details.work.baseSummaryId')
      FROM documents d
    UNION ALL
    SELECT 'run', run_id, 'capture', json_extract(document, '$.mapping.details.plan.source.id')
      FROM documents WHERE json_type(document, '$.mapping.details.plan.source.id') = 'text'
    UNION ALL
    SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.selector')
      FROM neighborhood_rows WHERE json_extract(document, '$.kind') = 'sources'
    UNION ALL
    SELECT 'capture', capture_id, 'record', json_extract(document, '$.id')
      FROM neighborhood_rows WHERE json_extract(document, '$.kind') = 'records'
    UNION ALL
    SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.recordId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.fromId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.toId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.fromId')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.fromKind') = 'session'
    UNION ALL
    SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.toId')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.toKind') = 'session'
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.authorId')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.authorKind') = 'run'
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.runId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.actorId')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.actorKind') = 'run'
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.authorityId')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.authorityKind') = 'run'
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.raisedById')
      FROM neighborhood_rows WHERE json_extract(document, '$.value.raisedByKind') = 'run'
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.status.runId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.ruling.runId') FROM neighborhood_rows
    UNION ALL
    SELECT 'capture', n.capture_id, 'run', title.run_id FROM neighborhood_rows n
      JOIN session_titles title ON title.selector = json_extract(n.document, '$.value.selector')
      WHERE json_extract(n.document, '$.kind') = 'sources'
    UNION ALL
    SELECT 'capture', n.capture_id, 'entity', e.id FROM neighborhood_rows n,
      json_tree(n.document, '$.value') j JOIN entities e ON e.id = j.value
      WHERE j.type = 'text' AND j.key IN ('id', 'entityId', 'entity_id', 'fromId', 'toId', 'objectId')
    UNION ALL
    SELECT 'capture', n.capture_id, 'question', q.id FROM neighborhood_rows n,
      json_tree(n.document, '$.value') j JOIN questions q ON q.id = j.value
      WHERE j.type = 'text' AND j.key IN ('id', 'questionId', 'question_id', 'fromId', 'toId')
  ),
  tainted(kind, id) AS (
    SELECT 'session', selector FROM session_exclusions
    UNION
    SELECT 'session', value FROM json_each(?)
    UNION
    SELECT d.consumer_kind, d.consumer_id FROM dependencies d
      JOIN tainted t ON t.kind = d.input_kind AND t.id = d.input_id
  )`;

/** One shared closure for compound, fixed-alias readers of the retained ledger. */
export function sourcePrivacyCTE(
  additionalSelectors: readonly string[] = [],
): { readonly sql: string; readonly params: readonly GuestSqlParam[] } {
  return { sql: TAINT, params: [JSON.stringify(additionalSelectors)] };
}

async function readExcludedIds(
  db: Database,
  kind: "record" | "run" | "capture",
  additionalSelectors: readonly string[],
): Promise<ReadonlySet<string>> {
  const ids = new Set<string>();
  if (additionalSelectors.length === 0 &&
      (await db.query("SELECT 1 FROM session_exclusions LIMIT 1")).length === 0) return ids;
  let cursor = "";
  for (;;) {
    const rows = await db.query<{ id: string }>(
      `${TAINT} SELECT id FROM tainted WHERE kind = ? AND id > ? ORDER BY id LIMIT ?`,
      [JSON.stringify(additionalSelectors), kind, cursor, PAGE],
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
    sql: `${idSql} NOT IN (${TAINT} SELECT id FROM tainted WHERE kind = ?)`,
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
    sql: `NOT EXISTS (${TAINT} SELECT 1 FROM tainted
      WHERE (kind = 'session' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'record' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'run' AND id IN (SELECT value FROM json_each(?)))
         OR (kind = 'capture' AND id IN (SELECT value FROM json_each(?))))`,
    params: ["[]", JSON.stringify(selectors), JSON.stringify(recordIds),
      JSON.stringify(runIds), JSON.stringify(captureIds)],
  };
}

/** A concurrent source-bearing reservation wins first or the immutable owner ban does. */
export async function excludeSessionWhenQuiescent(
  db: Database,
  selector: string,
  actorId: string,
  recordedAt: string,
): Promise<boolean> {
  const inserted = await db.query(
    `INSERT INTO session_exclusions(selector,actor_id,recorded_at)
      SELECT ?,?,? WHERE NOT EXISTS (
        ${TAINT} SELECT 1 FROM tainted t JOIN runs r ON r.id = t.id
          WHERE t.kind = 'run' AND r.closure IS NULL)
      AND coalesce((SELECT json_extract(payload,'$.enabled') FROM policies ORDER BY seq DESC LIMIT 1),0) != 1
      AND NOT EXISTS (SELECT 1 FROM drains WHERE state IN ('running','closing'))
      AND (SELECT count(*) FROM session_exclusions) < ?
      ON CONFLICT(selector) DO NOTHING RETURNING selector`,
    [selector, actorId, recordedAt, JSON.stringify([selector]), MAX_SESSION_EXCLUSIONS],
  );
  return inserted.length !== 0 || await sessionIsExcluded(db, selector);
}

/** Saved neighborhood input is indivisible: summaries may have consumed any of its rows. */
export async function mappingSourceIsExcluded(
  db: Database,
  source: NavigationMapSource,
): Promise<boolean> {
  const guard = modelPrivacyGuard("kind" in source ? [] : [source.session], [], [], [source.id]);
  return (await db.query(`SELECT 1 WHERE NOT (${guard.sql})`, guard.params)).length !== 0;
}
