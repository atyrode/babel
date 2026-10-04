import type { GuestDatabase } from "@manifold/plugin-kit";
import { INPUT_FIELD, RECORD_KINDS } from "../contract.ts";

// Persist input identities, never a taint snapshot. ANY preserves JSON scalar types
// and SQLite equality; catalog joins remain live in source-privacy.ts.
interface Projection {
  readonly prefix: string;
  readonly rows: string;
}
const PROJECTIONS: Readonly<Record<string, Projection>> = {
  runs: {
    prefix: `  source_documents(id,source_rowid,preparation,payload) AS MATERIALIZED (
    SELECT id,source_rowid,
      jsonb(CASE WHEN json_valid(preparation) THEN preparation ELSE '{}' END),
      jsonb(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END) FROM source_rows
  ),
  documents(run_id, source_rowid, payload_document, document) AS MATERIALIZED (
    SELECT id, source_rowid, 0, preparation FROM source_documents
    UNION ALL
    SELECT id, source_rowid, 1, payload FROM source_documents
    UNION ALL
    SELECT id, source_rowid, 0, jsonb_extract(payload, '$.preparation') FROM source_documents
      WHERE json_type(payload, '$.preparation') = 'object'
    UNION ALL
    SELECT id, source_rowid, 0, jsonb(jsonb_extract(preparation, '$.nativeRequest.input.${INPUT_FIELD}'))
      FROM source_documents
      WHERE json_valid(jsonb_extract(preparation, '$.nativeRequest.input.${INPUT_FIELD}'),5)
  ),
  session_inputs(run_id, source_rowid, payload_document, selector) AS (
    SELECT d.run_id, d.source_rowid, d.payload_document, j.value FROM documents d, json_each(d.document, '$.selectors') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, j.value FROM documents d, json_each(d.document, '$.titles.selectors') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, j.value FROM documents d, json_each(d.document, '$.analysis.selectors') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.harness') || '/' || json_extract(j.value, '$.sourceId'))
      FROM documents d, json_each(d.document, '$.material.sessions') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.harness') || '/' ||
        coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
      FROM documents d, json_each(d.document, '$.selection') j WHERE j.type = 'object'
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(j.value, '$.harness') || '/' || json_extract(j.value, '$.sourceId')
      FROM documents d, json_each(d.document, '$.captures') c, json_each(c.value, '$.sessions') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(d.document, '$.mapping.details.plan.source.session') FROM documents d
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(j.value, '$.source.selector')
      FROM documents d, json_each(d.document, '$.tasks') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(j.value, '$.source.harness') || '/' ||
      json_extract(j.value, '$.source.sourceId')
      FROM documents d, json_each(d.document, '$.tasks') j
  ),
  record_inputs(run_id, source_rowid, payload_document, record_id) AS (
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(j.value, '$.id')
      FROM documents d, json_each(d.document, '$.analysis.brief') j
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(d.document, '$.review.recordId') FROM documents d
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(d.document, '$.review.revisionId') FROM documents d
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(d.document, '$.embedding.recordId') FROM documents d
    UNION ALL
    SELECT d.run_id, d.source_rowid, d.payload_document, json_extract(j.value, '$.recordId')
      FROM documents d, json_each(d.document, '$.tasks') j
  ),
`,
    rows: `SELECT 'run', run_id, 'session', selector, source_rowid, NULL, NULL, NULL, 'dependency', NULL, payload_document FROM session_inputs WHERE selector IS NOT NULL
    UNION ALL
SELECT 'run', run_id, 'record', record_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, payload_document FROM record_inputs WHERE record_id IS NOT NULL
    UNION ALL
SELECT 'run', d.run_id, 'run', j.value, d.source_rowid, NULL, NULL, NULL, 'dependency', NULL, d.payload_document FROM documents d,
      json_each(d.document, '$.review.titleRunIds') j
    UNION ALL
SELECT 'run',d.run_id,NULL,json_extract(d.document,'$.review.recordId'), d.source_rowid, NULL, NULL, NULL, 'legacy_review', NULL, d.payload_document FROM documents d WHERE json_type(d.document,'$.review.titleRunIds') IS NULL
    UNION ALL
SELECT 'run',d.run_id,NULL,coalesce(json_extract(j.value,'$.selector'),json_extract(j.value,'$.sessionRef')), d.source_rowid, NULL, NULL, NULL, 'title', json_extract(j.value,'$.title'), d.payload_document FROM documents d,jsonb_tree(d.document) j WHERE j.type='object'
    UNION ALL
SELECT 'run', d.run_id, 'map', json_extract(j.value, '$.summaryId')
     , d.source_rowid, NULL, NULL, NULL, 'dependency', NULL, d.payload_document FROM documents d, json_each(d.document, '$.mapping.details.work.children') j
    UNION ALL
SELECT 'run', d.run_id, 'map', json_extract(d.document, '$.mapping.details.work.baseSummaryId')
     , d.source_rowid, NULL, NULL, NULL, 'dependency', NULL, d.payload_document FROM documents d
    UNION ALL
SELECT 'run', run_id, 'capture', json_extract(document, '$.mapping.details.plan.source.id')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, payload_document FROM documents WHERE json_type(document, '$.mapping.details.plan.source.id') = 'text'`,
  },
  records: {
    prefix: ``,
    rows: `SELECT 'record', r.id, 'run', r.run_id, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r WHERE r.run_id IS NOT NULL
    UNION ALL
SELECT 'run', r.run_id, 'record', r.id, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r WHERE r.run_id IS NOT NULL
    UNION ALL
SELECT 'record', id, 'run', actor_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE actor_kind = 'run'
    UNION ALL
SELECT 'run', actor_id, 'record', id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE actor_kind = 'run'
    UNION ALL
SELECT 'record', r.id, 'session', coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.sessionRef'),
      CASE WHEN json_extract(j.value, '$.kind') = 'session' THEN json_extract(j.value, '$.id') END,
      json_extract(j.value, '$.harness') || '/' ||
      coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
     , r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r, json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.type = 'object'
    UNION ALL
SELECT 'record', r.id, 'session', j.value, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('session', 'selector', 'sessionRef')
    UNION ALL
SELECT 'record', r.id, 'record', r.parent_id, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r WHERE r.parent_id IS NOT NULL
    UNION ALL
SELECT 'record', id, 'root', root_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'root', root_id, 'record', id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'record', r.id, 'map', j.value, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.key IN ('nodeId','versionId','summaryId') AND j.type = 'text'
    UNION ALL
SELECT 'record', r.id, 'capture', j.value, r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r,
      json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.key = 'captureId' AND j.type = 'text'
    UNION ALL
SELECT 'record', r.id, json_extract(j.value, '$.kind'), json_extract(j.value, '$.id')
     , r.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows r, json_tree(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) j
      WHERE j.type = 'object'`,
  },
  citation_facts: {
    prefix: ``,
    rows: `SELECT 'record', record_id, 'session', json_extract(task, '$.source.selector')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'record', record_id, 'session', json_extract(task, '$.source.harness') || '/' ||
      json_extract(task, '$.source.sourceId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  assessments: {
    prefix: ``,
    rows: `SELECT 'record', record_id, 'run', run_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'record', revision_id, 'run', run_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE revision_id <> record_id`,
  },
  status_events: {
    prefix: ``,
    rows: `SELECT 'record', record_id, 'run', run_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE run_id IS NOT NULL
    UNION ALL
SELECT 'record', record_id, 'run', actor_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE actor_kind = 'run'`,
  },
  next_actions: {
    prefix: ``,
    rows: `SELECT 'record', record_id, 'run', proposed_by_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE proposed_by_kind = 'run'`,
  },
  edges: {
    prefix: ``,
    rows: `SELECT 'record',e.from_id,'run',e.actor_id, e.source_rowid, e.from_kind, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e WHERE e.actor_kind='run'
    UNION ALL
SELECT 'record',e.to_id,'run',e.actor_id, e.source_rowid, e.to_kind, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e WHERE e.actor_kind='run'
    UNION ALL
SELECT 'entity', e.from_id, 'run', e.actor_id, e.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e
      WHERE e.actor_kind = 'run' AND e.from_kind = 'entity'
    UNION ALL
SELECT 'entity', e.to_id, 'run', e.actor_id, e.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e
      WHERE e.actor_kind = 'run' AND e.to_kind = 'entity'
    UNION ALL
SELECT 'record',e.from_id,'session',e.to_id, e.source_rowid, e.from_kind, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e WHERE e.to_kind='session'
    UNION ALL
SELECT 'record',e.from_id,'record',e.to_id, e.source_rowid, e.from_kind, 'record', e.to_kind, 'dependency', NULL, 0 FROM source_rows e
    UNION ALL
SELECT 'record',e.from_id,NULL,e.to_id, e.source_rowid, e.from_kind, NULL, NULL, 'current_title', NULL, 0 FROM source_rows e WHERE e.to_kind='session'
    UNION ALL
SELECT 'record',e.from_id,'entity',e.to_id, e.source_rowid, e.from_kind, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e WHERE e.to_kind='entity'
    UNION ALL
SELECT 'record',e.from_id,'question',e.to_id, e.source_rowid, e.from_kind, NULL, NULL, 'dependency', NULL, 0 FROM source_rows e WHERE e.to_kind='question'
    UNION ALL
SELECT 'record',e.from_id,'map',e.to_id, e.source_rowid, e.from_kind, 'map_node_summary', NULL, 'dependency', NULL, 0 FROM source_rows e
    UNION ALL
SELECT 'record',e.from_id,'capture',e.to_id, e.source_rowid, e.from_kind, 'capture', NULL, 'dependency', NULL, 0 FROM source_rows e`,
  },
  entities: {
    prefix: ``,
    rows: `SELECT 'entity', id, 'run', created_by, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'entity', id, 'record', created_by, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'entity', id, 'entity', canonical_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'entity', canonical_id, 'entity', id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  facts: {
    prefix: ``,
    rows: `SELECT 'entity', entity_id, 'run', authority_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE authority_kind = 'run'
    UNION ALL
SELECT 'entity', entity_id, 'entity', object_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE object_id IS NOT NULL`,
  },
  filings: {
    prefix: ``,
    rows: `SELECT 'record', record_id, 'entity', entity_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'record', record_id, 'run', author_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE author_kind = 'run'
    UNION ALL
SELECT 'entity', entity_id, 'run', author_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE author_kind = 'run'`,
  },
  questions: {
    prefix: ``,
    rows: `SELECT 'question', id, 'run', raised_by_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows WHERE raised_by_kind = 'run'
    UNION ALL
SELECT 'question', q.id, 'session', j.value, q.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('session', 'selector', 'sessionRef')
    UNION ALL
SELECT 'question', q.id, 'session', coalesce(json_extract(j.value, '$.selector'),
      json_extract(j.value, '$.sessionRef'),
      CASE WHEN json_extract(j.value, '$.kind') = 'session' THEN json_extract(j.value, '$.id') END,
      json_extract(j.value, '$.harness') || '/' ||
      coalesce(json_extract(j.value, '$.sourceId'), json_extract(j.value, '$.source_id')))
     , q.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows q, json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'object'
    UNION ALL
SELECT 'question', q.id, 'session', j.value, q.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j
      WHERE j.type = 'text'
    UNION ALL
SELECT 'question', q.id, 'record', j.value, q.source_rowid, NULL, 'record', NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j WHERE j.type = 'text'
    UNION ALL
SELECT 'question', q.id, 'entity', j.value, q.source_rowid, NULL, 'entity', NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') j WHERE j.type = 'text'
    UNION ALL
SELECT 'question', q.id, 'record', j.value, q.source_rowid, NULL, 'record', NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('id', 'record', 'recordId', 'record_id', 'fromId', 'toId')
    UNION ALL
SELECT 'question', q.id, 'entity', j.value, q.source_rowid, NULL, 'entity', NULL, 'dependency', NULL, 0 FROM source_rows q,
      json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'text' AND j.key IN ('id', 'entityId', 'entity_id', 'objectId', 'subjectId')
    UNION ALL
SELECT 'question', q.id, json_extract(j.value, '$.kind'), json_extract(j.value, '$.id')
     , q.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows q, json_tree(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END) j
      WHERE j.type = 'object'`,
  },
  transcript_map_captures: {
    prefix: ``,
    rows: `SELECT 'capture', id, 'session', session, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  transcript_map_plans: {
    prefix: ``,
    rows: `SELECT 'map', id, 'capture', capture_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  transcript_map_nodes: {
    prefix: ``,
    rows: `SELECT 'map', id, 'map', plan_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  transcript_map_versions: {
    prefix: ``,
    rows: `SELECT 'map', id, 'map', plan_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  transcript_map_summaries: {
    prefix: ``,
    rows: `SELECT 'map', id, 'map', version_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'map', id, 'run', json_extract(payload, '$.runId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows`,
  },
  transcript_map_bindings: {
    prefix: ``,
    rows: `SELECT 'map', version_id, 'map', summary_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM source_rows
    UNION ALL
SELECT 'capture',b.version_id,NULL,b.summary_id, b.source_rowid, NULL, NULL, NULL, 'binding_capture', NULL, 0 FROM source_rows b`,
  },
  transcript_map_neighborhood_inputs: {
    prefix: `  neighborhood_rows(capture_id, source_rowid, document) AS MATERIALIZED (
    SELECT n.capture_id,n.source_rowid,j.value FROM source_rows n,
      json_each('[' || rtrim(replace(n.text, char(10), ','), ',') || ']') j
  ),
`,
    rows: `SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.selector')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.kind') = 'sources'
    UNION ALL
SELECT 'capture', capture_id, 'record', json_extract(document, '$.id')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.kind') = 'records'
    UNION ALL
SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.recordId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.fromId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, 'record', json_extract(document, '$.value.toId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.fromId')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.fromKind') = 'session'
    UNION ALL
SELECT 'capture', capture_id, 'session', json_extract(document, '$.value.toId')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.toKind') = 'session'
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.authorId')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.authorKind') = 'run'
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.runId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.actorId')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.actorKind') = 'run'
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.authorityId')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.authorityKind') = 'run'
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.raisedById')
     , source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows WHERE json_extract(document, '$.value.raisedByKind') = 'run'
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.status.runId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, 'run', json_extract(document, '$.value.ruling.runId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture',n.capture_id,NULL,json_extract(n.document,'$.value.selector'), n.source_rowid, NULL, NULL, NULL, 'title', json_extract(n.document,'$.value.title'), 0 FROM neighborhood_rows n WHERE json_extract(n.document,'$.kind')='sources'
    UNION ALL
SELECT 'capture', n.capture_id, 'entity', j.value, n.source_rowid, NULL, 'entity', NULL, 'dependency', NULL, 0 FROM neighborhood_rows n,
      json_tree(n.document, '$.value') j
      WHERE j.type = 'text' AND j.key IN ('id', 'entityId', 'entity_id', 'fromId', 'toId', 'objectId')
    UNION ALL
SELECT 'capture', n.capture_id, 'question', j.value, n.source_rowid, NULL, 'question', NULL, 'dependency', NULL, 0 FROM neighborhood_rows n,
      json_tree(n.document, '$.value') j
      WHERE j.type = 'text' AND j.key IN ('id', 'questionId', 'question_id', 'fromId', 'toId')
    UNION ALL
SELECT 'capture', capture_id, json_extract(document, '$.value.fromKind'),
      json_extract(document, '$.value.fromId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', capture_id, json_extract(document, '$.value.toKind'),
      json_extract(document, '$.value.toId'), source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows
    UNION ALL
SELECT 'capture', n.capture_id, json_extract(j.value, '$.kind'), json_extract(j.value, '$.id')
     , n.source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM neighborhood_rows n, json_tree(n.document, '$.value') j WHERE j.type = 'object'`,
  },
  plans: {
    prefix: `  applied_plans(id, kind, subject_kind, subject_id, proposed_by_kind, proposed_by_id,
    source_rowid, result, result_document, payload) AS MATERIALIZED (
    SELECT id, kind, subject_kind, subject_id, proposed_by_kind, proposed_by_id, source_rowid, result,
      CASE WHEN json_valid(result) THEN result ELSE '{}' END,
      CASE WHEN json_valid(payload) THEN payload ELSE '{}' END
      FROM source_rows WHERE state = 'applied'
  ),
  applied_results(plan_id, source_rowid, result_id) AS (
    SELECT id, source_rowid, json_extract(result_document, '$.entityId') FROM applied_plans
    UNION ALL
    SELECT id, source_rowid, json_extract(result_document, '$.resolutionId') FROM applied_plans
    UNION ALL
    SELECT id, source_rowid, json_extract(result_document, '$.factId') FROM applied_plans
    UNION ALL
    SELECT p.id, p.source_rowid, j.value FROM applied_plans p, json_each(p.result_document, '$.filed') j
      WHERE j.type = 'text'
    UNION ALL
    SELECT p.id, p.source_rowid, j.value FROM applied_plans p, json_each(p.result_document, '$.settled') j
      WHERE j.type = 'text'
    UNION ALL
    SELECT id, source_rowid, result FROM applied_plans WHERE kind = 'topic'
    UNION ALL
    SELECT p.id, p.source_rowid, json_extract(j.value, '$.result_id')
      FROM applied_plans p, json_each(p.payload, '$.actions') j
      WHERE p.kind = 'answer' AND j.type = 'object'
  ),
`,
    rows: `SELECT 'plan', id, subject_kind, subject_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM applied_plans
    UNION ALL
SELECT 'plan', id, 'run', proposed_by_id, source_rowid, NULL, NULL, NULL, 'dependency', NULL, 0 FROM applied_plans WHERE proposed_by_kind = 'run'
    UNION ALL
SELECT 'plan',plan_id,NULL,result_id, source_rowid, NULL, NULL, NULL, 'applied_result', NULL, 0 FROM applied_results`,
  },
};

const SOURCES = Object.keys(PROJECTIONS);
export const SOURCE_DEPENDENCY_READY = `(SELECT count(*) FROM source_dependency_progress WHERE complete=1)=${SOURCES.length}`;
const INITIALIZE_PROGRESS = `INSERT OR IGNORE INTO source_dependency_progress(origin) VALUES ${SOURCES.map((table) => `('${table}')`).join(",")}`;

// A fresh store has no history to backfill. Upgrades create only the schema objects and
// leave these markers incomplete until the bounded maintenance pass reaches every row.
export const SOURCE_DEPENDENCY_FRESH: readonly string[] = [
  `INSERT INTO source_dependency_progress(origin,complete) VALUES ${SOURCES.map((table) => `('${table}',1)`).join(",")}`,
];

const COLUMNS =
  "origin,origin_rowid,consumer_kind,consumer_id,input_kind,input_id,consumer_record_kind,input_catalog,input_record_kind,relation,detail,payload_document";
export const SOURCE_DEPENDENCY_KINDS =
  "'session','run','record','root','capture','map','entity','question'";
const RECORD_KIND_SQL = RECORD_KINDS.map((kind) => `'${kind}'`).join(",");

const EDGE_COLUMNS = [
  "consumer_kind",
  "consumer_id",
  "input_kind",
  "input_id",
  "consumer_record_kind",
  "input_catalog",
  "input_record_kind",
  "relation",
  "detail",
  "producer_run_id",
] as const;

const NEW_EDGE = EDGE_COLUMNS.map((column) =>
  column === "producer_run_id"
    ? "CASE WHEN NEW.origin='runs' AND NEW.payload_document=1 THEN NEW.consumer_id END"
    : `NEW.${column}`,
);

// JSONB preserves integer/text/null identity compactly. Explicit markers keep all real bits
// and arbitrary blobs distinct; concatenation removes JSON subtypes without truncating NULs.
// No serialized key is decoded or exposed outside this store.
function edgeKey(values: readonly string[]): string {
  return `jsonb_array(${values
    .map(
      (value) => `CASE typeof(${value})
        WHEN 'real' THEN jsonb_array(0,printf('%!.26g',${value}))
        WHEN 'blob' THEN jsonb_array(1,hex(${value}))
        WHEN 'text' THEN ${value}||'' ELSE ${value} END`,
    )
    .join(",")})`;
}

function project(table: string, where: string): string {
  const definition = PROJECTIONS[table]!;
  return `INSERT INTO source_dependency_rows(${COLUMNS})
    SELECT DISTINCT '${table}',origin_rowid,consumer_kind,consumer_id,normalized_input_kind,input_id,
      CASE WHEN known_consumer=1 THEN NULL ELSE consumer_record_kind END,
      CASE WHEN known_input=1 THEN NULL ELSE input_catalog END,
      CASE WHEN known_input=1 THEN NULL ELSE input_record_kind END,
      relation,detail,payload_document
    FROM (WITH source_rows AS MATERIALIZED (
      SELECT rowid AS source_rowid,* FROM ${table} WHERE ${where}
    ), ${definition.prefix} projection_rows(consumer_kind,consumer_id,input_kind,input_id,
      origin_rowid,consumer_record_kind,input_catalog,input_record_kind,relation,detail,payload_document) AS (
      ${definition.rows}
    ), resolved_rows AS MATERIALIZED (
      SELECT projection_rows.*,
        CASE WHEN input_kind IN (${SOURCE_DEPENDENCY_KINDS}) THEN input_kind
          WHEN input_kind IN (${RECORD_KIND_SQL}) AND EXISTS (
            SELECT 1 FROM records r WHERE r.id=input_id AND r.kind=input_kind) THEN 'record'
          ELSE input_kind END AS normalized_input_kind,
        CASE WHEN consumer_record_kind IS NULL THEN 0 ELSE EXISTS (
          SELECT 1 FROM records r WHERE r.id=consumer_id AND r.kind=consumer_record_kind) END AS known_consumer,
        CASE WHEN input_catalog='record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=input_id
          AND (input_record_kind IS NULL OR r.kind=input_record_kind)) ELSE 0 END AS known_input
      FROM projection_rows WHERE consumer_id IS NOT NULL AND input_id IS NOT NULL
        AND (consumer_record_kind IS NULL OR consumer_record_kind IN (${RECORD_KIND_SQL}))
        AND (input_record_kind IS NULL OR input_record_kind IN (${RECORD_KIND_SQL}))
    ) SELECT * FROM resolved_rows)`;
}

// Records cannot be edited or deleted. Resolve positive record catalog facts once above;
// unresolved references still carry their live guards, so later catalog arrival is exact.
// Coalescing equal edges avoids repeated work from many ledger rows citing the same input.
// Refcounts change only through source-row triggers in the original write transaction.

export const SOURCE_DEPENDENCY_SCHEMA: readonly string[] = [
  `CREATE TABLE source_dependencies(
    origin TEXT NOT NULL, origin_rowid INTEGER NOT NULL,
    edge_id INTEGER NOT NULL REFERENCES source_dependency_edges(edge_id)
  ) STRICT`,
  `CREATE TABLE source_dependency_edges(
    edge_id INTEGER PRIMARY KEY,edge_key BLOB NOT NULL UNIQUE,
    consumer_kind ANY NOT NULL,consumer_id ANY NOT NULL,input_kind ANY,input_id ANY NOT NULL,
    consumer_record_kind ANY,input_catalog TEXT,input_record_kind ANY,
    relation TEXT NOT NULL,detail ANY,producer_run_id ANY,
    refs INTEGER NOT NULL CHECK (refs>=0)
  ) STRICT`,
  `CREATE VIEW source_dependency_rows AS SELECT o.origin,o.origin_rowid,
    ${EDGE_COLUMNS.filter((column) => column !== "producer_run_id")
      .map((column) => `e.${column}`)
      .join(",")},
    CASE WHEN e.producer_run_id IS NULL THEN 0 ELSE 1 END AS payload_document
    FROM source_dependencies o JOIN source_dependency_edges e ON e.edge_id=o.edge_id`,
  `CREATE INDEX source_dependency_edges_id ON source_dependency_edges(
    relation,input_id,input_kind,consumer_kind,consumer_id,consumer_record_kind,input_catalog,input_record_kind,detail,producer_run_id)`,
  `CREATE INDEX source_dependency_edges_consumer ON source_dependency_edges(consumer_id,input_id)
    WHERE relation='applied_result'`,
  `CREATE INDEX source_dependency_edges_linked ON source_dependency_edges(input_kind,input_id,producer_run_id)
    WHERE relation='dependency' AND producer_run_id IS NOT NULL`,
  `CREATE INDEX source_dependency_edges_generic ON source_dependency_edges(
    input_id,input_kind,consumer_kind,consumer_id,consumer_record_kind,input_catalog,input_record_kind)
    WHERE relation='dependency' AND (input_kind IS NULL OR input_kind NOT IN (${SOURCE_DEPENDENCY_KINDS}))`,
  "CREATE INDEX source_dependencies_origin ON source_dependencies(origin,origin_rowid)",
  "CREATE INDEX source_dependencies_edge ON source_dependencies(edge_id)",
  "CREATE INDEX session_titles_by_run ON session_titles(run_id)",
  `CREATE TABLE source_dependency_progress(
    origin TEXT PRIMARY KEY,last_rowid TEXT,complete INTEGER NOT NULL DEFAULT 0
  ) STRICT`,
  `CREATE TRIGGER source_dependency_rows_insert INSTEAD OF INSERT ON source_dependency_rows BEGIN
    INSERT INTO source_dependency_edges(edge_key,${EDGE_COLUMNS.join(",")},refs)
      VALUES (${edgeKey(NEW_EDGE)},${NEW_EDGE.join(",")},0) ON CONFLICT(edge_key) DO NOTHING;
    INSERT INTO source_dependencies(origin,origin_rowid,edge_id)
      SELECT NEW.origin,NEW.origin_rowid,edge_id FROM source_dependency_edges
      WHERE edge_key=${edgeKey(NEW_EDGE)};
  END`,
  `CREATE TRIGGER source_dependency_edges_insert AFTER INSERT ON source_dependencies BEGIN
    UPDATE source_dependency_edges SET refs=refs+1 WHERE edge_id=NEW.edge_id;
  END`,
  `CREATE TRIGGER source_dependency_edges_delete AFTER DELETE ON source_dependencies BEGIN
    UPDATE source_dependency_edges SET refs=refs-1 WHERE edge_id=OLD.edge_id;
    DELETE FROM source_dependency_edges WHERE edge_id=OLD.edge_id AND refs=0;
  END`,
  ...SOURCES.flatMap((table) => [
    `CREATE TRIGGER source_dependencies_${table}_insert AFTER INSERT ON ${table} BEGIN
      ${project(table, "rowid=NEW.rowid")}; END`,
    `CREATE TRIGGER source_dependencies_${table}_update AFTER UPDATE ON ${table} BEGIN
      DELETE FROM source_dependencies WHERE origin='${table}' AND origin_rowid=OLD.rowid;
      ${project(table, "rowid=NEW.rowid")}; END`,
    `CREATE TRIGGER source_dependencies_${table}_delete AFTER DELETE ON ${table} BEGIN
      DELETE FROM source_dependencies WHERE origin='${table}' AND origin_rowid=OLD.rowid; END`,
  ]),
];

export async function sourceDependenciesReady(db: Pick<GuestDatabase, "query">): Promise<boolean> {
  return (await db.query(`SELECT 1 WHERE ${SOURCE_DEPENDENCY_READY}`)).length !== 0;
}

// One bounded maintenance turn, not an enable-time traversal of history. Five-row chunks
// limit host-thread blocking, and every await returns authority to the host between chunks.
// TEXT cursors preserve the whole SQLite rowid range; null includes negative/minimum rowids.
export async function backfillSourceDependencies(
  db: Pick<GuestDatabase, "query" | "batch">,
  budgetMs = 1000,
): Promise<boolean> {
  if (await sourceDependenciesReady(db)) return true;
  const started = performance.now();
  await db.batch([{ sql: INITIALIZE_PROGRESS }]);
  for (const table of SOURCES) {
    const progress = await db.query<{ last_rowid: string | null; complete: number }>(
      "SELECT last_rowid,complete FROM source_dependency_progress WHERE origin=?",
      [table],
    );
    if (Number(progress[0]?.complete) === 1) continue;
    let cursor = progress[0]?.last_rowid ?? null;
    for (;;) {
      if (performance.now() - started >= budgetMs) return false;
      const lower = cursor === null ? "" : "WHERE rowid>CAST(? AS INTEGER)";
      const rows = await db.query<{ row_id: string }>(
        `SELECT CAST(rowid AS TEXT) AS row_id FROM ${table} ${lower} ORDER BY rowid LIMIT 5`,
        cursor === null ? [] : [cursor],
      );
      if (rows.length === 0) {
        await db.batch([
          {
            sql: "UPDATE source_dependency_progress SET complete=1 WHERE origin=?",
            params: [table],
          },
        ]);
        break;
      }
      const last = rows[rows.length - 1]!.row_id;
      const ids = rows.map((row) => row.row_id);
      const selected = ids.map(() => "CAST(? AS INTEGER)").join(",");
      await db.batch([
        {
          sql: `DELETE FROM source_dependencies WHERE origin=? AND origin_rowid IN (${selected})`,
          params: [table, ...ids],
        },
        {
          sql: project(table, `rowid IN (${selected})`),
          params: ids,
        },
        {
          sql: "UPDATE source_dependency_progress SET last_rowid=? WHERE origin=?",
          params: [last, table],
        },
      ]);
      cursor = last;
    }
  }
  return true;
}
