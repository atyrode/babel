import type { GuestDatabase, GuestSqlParam } from "@manifold/plugin-kit";
import { HARNESSES, RECORD_KINDS } from "../contract.ts";
import {
  SOURCE_DEPENDENCY_KINDS as KINDS,
  SOURCE_DEPENDENCY_READY,
} from "./source-dependencies.ts";

const RECORDS = RECORD_KINDS.map((kind) => `'${kind}'`).join(",");
const INPUT_KIND = `CASE WHEN d.input_kind IN (${KINDS}) THEN d.input_kind
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
    WHEN 'map_node_summary' THEN EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END`;
const UNQUALIFIED_SESSION = `substr(d.input_id,1,instr(d.input_id,'/')-1)
    NOT IN (${HARNESSES.map((harness) => `'${harness}'`).join(",")})`;
const BARE_SESSION = `NOT EXISTS (SELECT 1 FROM sessions exact WHERE exact.selector=d.input_id)
  AND ${UNQUALIFIED_SESSION}`;

interface DependencyArm {
  readonly kind: string;
  readonly id: string;
  readonly joins: string;
  readonly where: string;
}
// One definition of every graph edge, shared by full rebuilds and incremental propagation.
const ARMS: readonly DependencyArm[] = [
  {
    kind: "d.consumer_kind",
    id: "d.consumer_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency' AND d.input_kind=t.kind AND d.input_id=t.id`,
    where: `d.input_kind IN (${KINDS}) AND ${ROW_GUARD}`,
  },
  {
    kind: "d.consumer_kind",
    id: "d.consumer_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_generic ON d.relation='dependency' AND d.input_id=t.id`,
    where: `t.kind IN ('record','capture','map','plan') AND (t.kind<>'record' OR d.input_kind IN (${RECORDS})) AND (t.kind<>'plan' OR d.input_kind='plan') AND (d.input_kind IS NULL OR d.input_kind NOT IN (${KINDS})) AND t.kind=${INPUT_KIND} AND ${ROW_GUARD}`,
  },
  {
    kind: "d.consumer_kind",
    id: "d.consumer_id",
    joins: `CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency' AND d.input_kind='session' AND s.source_id=d.input_id`,
    where: `${BARE_SESSION} AND ${ROW_GUARD}`,
  },
  {
    kind: "'run'",
    id: "r.id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency' AND d.input_kind=t.kind AND d.input_id=t.id CROSS JOIN runs p ON p.id=d.producer_run_id CROSS JOIN runs r ON r.prepare_job_id=p.job_id`,
    where: `d.producer_run_id IS NOT NULL AND d.input_kind IN (${KINDS})`,
  },
  {
    kind: "'run'",
    id: "r.id",
    joins: `CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency' AND d.input_kind='session' AND s.source_id=d.input_id CROSS JOIN runs p ON p.id=d.producer_run_id CROSS JOIN runs r ON r.prepare_job_id=p.job_id`,
    where: `d.producer_run_id IS NOT NULL AND ${BARE_SESSION}`,
  },
  {
    kind: "d.consumer_kind",
    id: "d.consumer_id",
    joins: `CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector`,
    where: `d.detail=title.title`,
  },
  {
    kind: "'run'",
    id: "r.id",
    joins: `CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector CROSS JOIN runs p ON p.id=d.producer_run_id CROSS JOIN runs r ON r.prepare_job_id=p.job_id`,
    where: `d.producer_run_id IS NOT NULL AND d.detail=title.title`,
  },
  {
    kind: "d.consumer_kind",
    id: "d.consumer_id",
    joins: `CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id CROSS JOIN sessions s ON s.selector=title.selector CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='current_title' AND d.input_id=title.selector`,
    where: `s.title_provenance='inferred' AND s.title=title.title AND ${ROW_GUARD}`,
  },
  {
    kind: "'run'",
    id: "consumer.id",
    joins: `CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id CROSS JOIN runs consumer ON consumer.id=d.consumer_id`,
    where: `consumer.started_at>=title.inferred_at`,
  },
  {
    kind: "'run'",
    id: "consumer.id",
    joins: `CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id CROSS JOIN runs p ON p.id=d.producer_run_id CROSS JOIN runs consumer ON consumer.prepare_job_id=p.job_id`,
    where: `d.producer_run_id IS NOT NULL AND consumer.started_at>=title.inferred_at`,
  },
  {
    kind: "'capture'",
    id: "p.capture_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='binding_capture' AND t.kind='map' AND d.input_id=t.id CROSS JOIN transcript_map_versions v ON v.id=d.consumer_id CROSS JOIN transcript_map_plans p ON p.id=v.plan_id`,
    where: "1",
  },
  {
    kind: "'entity'",
    id: "e.id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN entities e ON e.id=d.input_id`,
    where: "1",
  },
  {
    kind: "'entity'",
    id: "f.entity_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN facts f ON f.id=d.input_id`,
    where: "1",
  },
  {
    kind: "'entity'",
    id: "m.entity_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN resolution_members m ON m.resolution_id=d.input_id`,
    where: "1",
  },
  {
    kind: "'record'",
    id: "f.record_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN filings f ON f.id=d.input_id`,
    where: "1",
  },
  {
    kind: "'entity'",
    id: "f.entity_id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN filings f ON f.id=d.input_id`,
    where: "1",
  },
  {
    kind: "'record'",
    id: "r.id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN records r ON r.id=d.input_id`,
    where: "1",
  },
  {
    kind: "'question'",
    id: "q.id",
    joins: `CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id CROSS JOIN questions q ON q.id=d.input_id`,
    where: "1",
  },
];
function closure(seeds: string, incremental = false): string {
  return `WITH RECURSIVE active_taint(kind,id) AS (${seeds} UNION ${ARMS.map(
    (arm) =>
      `SELECT ${arm.kind},${arm.id} FROM active_taint t ${arm.joins} WHERE ${arm.where}${incremental ? ` AND NOT EXISTS (SELECT 1 FROM source_taint known WHERE known.kind=${arm.kind} AND known.id=${arm.id})` : ""}`,
  ).join(" UNION ")})`;
}
export const SOURCE_TAINT_READY = `(${SOURCE_DEPENDENCY_READY}) AND coalesce((SELECT ready=1 AND dirty=0 FROM source_taint_state WHERE id=1),0)`;
const LEDGER_SEEDS = "SELECT 'session',selector FROM session_exclusions";
const FULL_CLOSURE = closure(LEDGER_SEEDS);
const INCREMENT_EDGE_SEEDS = `SELECT t.kind,t.id FROM source_taint t WHERE t.kind IN (${KINDS},'plan') AND t.id=NEW.input_id
  UNION SELECT t.kind,t.id FROM sessions s JOIN source_taint t ON t.kind='session' AND t.id=s.selector WHERE s.source_id=NEW.input_id
  UNION SELECT t.kind,t.id FROM session_titles title JOIN source_taint t ON t.kind='run' AND t.id=title.run_id WHERE title.selector=NEW.input_id
  UNION SELECT t.kind,t.id FROM edges e JOIN session_titles title ON title.selector=e.to_id JOIN source_taint t ON t.kind='run' AND t.id=title.run_id WHERE e.to_kind='session' AND e.from_id=NEW.input_id AND NEW.relation='legacy_review'
  UNION SELECT 'plan',NEW.consumer_id WHERE NEW.relation='applied_result' AND EXISTS (SELECT 1 FROM source_taint WHERE kind='plan' AND id=NEW.consumer_id)`;
// Keep recursion in one trigger: ordinary base writes and ALTER must not reparse a copy per catalog.
const PROPAGATE = `UPDATE source_taint_state SET propagation=propagation+1 WHERE id=1 AND (${SOURCE_TAINT_READY}) AND EXISTS (SELECT 1 FROM session_exclusions);`;
const INSERT_EDGE_TAINT = `INSERT OR IGNORE INTO source_taint_seeds(kind,id) ${INCREMENT_EDGE_SEEDS}; ${PROPAGATE}`;
const REBUILD = `DELETE FROM source_taint_seeds; DELETE FROM source_taint; INSERT OR IGNORE INTO source_taint(kind,id) ${FULL_CLOSURE} SELECT kind,id FROM active_taint; UPDATE source_taint_state SET ready=1,dirty=0,generation=generation+1 WHERE id=1;`;
const PROJECTION_CHANGED = `UPDATE source_taint_state SET ready=0 WHERE id=1 AND NOT (${SOURCE_DEPENDENCY_READY});
  UPDATE source_taint_state SET dirty=1 WHERE id=1 AND ready=0 AND (${SOURCE_DEPENDENCY_READY});`;
export const SOURCE_TAINT_BEGIN = "UPDATE source_taint_state SET depth=depth+1 WHERE id=1;";
export const SOURCE_TAINT_END = "UPDATE source_taint_state SET depth=depth-1 WHERE id=1;";

const CATALOG_KEYS: Readonly<Record<string, string>> = {
  runs: "id",
  records: "id",
  entities: "id",
  facts: "id",
  filings: "id",
  questions: "id",
  plans: "id",
  transcript_map_captures: "id",
  transcript_map_plans: "id",
  transcript_map_nodes: "id",
  transcript_map_versions: "id",
  transcript_map_summaries: "id",
  sessions: "selector",
  session_titles: "selector",
  resolution_members: "resolution_id",
  edges: "from_id",
};
const CATALOG_COLUMNS: Readonly<Record<string, readonly string[]>> = {
  runs: ["job_id", "prepare_job_id", "started_at"],
  records: ["kind"],
  facts: ["entity_id"],
  filings: ["record_id", "entity_id"],
  sessions: ["source_id", "title", "title_provenance"],
  session_titles: ["run_id", "title", "inferred_at"],
  resolution_members: ["entity_id"],
  edges: ["to_kind", "to_id"],
  transcript_map_plans: ["capture_id"],
  transcript_map_versions: ["plan_id"],
};
function affectedCatalog(table: string): string {
  const key = CATALOG_KEYS[table]!;
  const candidates = [`(kind IN (${KINDS},'plan') AND id=OLD.${key})`];
  if (table === "facts" || table === "filings" || table === "resolution_members")
    candidates.push(`(kind='entity' AND id=OLD.entity_id)`);
  if (table === "filings") candidates.push("(kind='record' AND id=OLD.record_id)");
  if (table === "session_titles") candidates.push("(kind='run' AND id=OLD.run_id)");
  if (table === "sessions")
    candidates.push(
      "(kind='run' AND id IN (SELECT run_id FROM session_titles WHERE selector=OLD.selector AND title=OLD.title))",
    );
  if (table === "edges")
    candidates.push(
      "(kind='run' AND id IN (SELECT run_id FROM session_titles WHERE selector=OLD.to_id AND OLD.to_kind='session'))",
    );
  if (table === "runs")
    candidates.push("(kind='run' AND id IN (SELECT id FROM runs WHERE prepare_job_id=OLD.job_id))");
  return `EXISTS (SELECT 1 FROM source_taint WHERE ${candidates.join(" OR ")})
    OR EXISTS (SELECT 1 FROM source_dependency_edges d JOIN source_taint t
      ON t.kind=d.consumer_kind AND t.id=d.consumer_id
      WHERE d.relation IN ('dependency','title','current_title','legacy_review','binding_capture','applied_result') AND d.input_id=OLD.${key})`;
}
function catalogChanged(table: string): string {
  return [CATALOG_KEYS[table]!, ...(CATALOG_COLUMNS[table] ?? [])]
    .map((column) => `OLD.${column} IS NOT NEW.${column}`)
    .join(" OR ");
}

export function sourceTaintCatalogBefore(table: string, event: "update" | "delete"): string {
  const key = CATALOG_KEYS[table];
  if (key === undefined) return "";
  const changed = event === "delete" ? "1" : catalogChanged(table);
  return `UPDATE source_taint_state SET dirty=1 WHERE id=1 AND ready=1
    AND EXISTS (SELECT 1 FROM session_exclusions) AND (${changed}) AND (${affectedCatalog(table)});`;
}
export function sourceTaintCatalogAfter(table: string): string {
  const key = CATALOG_KEYS[table];
  if (key === undefined) return "";
  const candidates = [{ condition: `d.input_id=NEW.${key}`, index: "source_dependency_edges_id" }];
  if (table === "records")
    candidates.push({
      condition:
        "d.consumer_kind='record' AND d.consumer_id=NEW.id AND d.consumer_record_kind=NEW.kind",
      index: "source_taint_guarded_consumer",
    });
  if (table === "runs")
    candidates.push(
      {
        condition: "d.relation='legacy_review' AND d.consumer_id=NEW.id",
        index: "source_taint_legacy_consumer",
      },
      { condition: "d.producer_run_id=NEW.id", index: "source_taint_producer" },
      {
        condition: "d.producer_run_id IN (SELECT id FROM runs WHERE job_id=NEW.prepare_job_id)",
        index: "source_taint_producer",
      },
    );
  if (table === "transcript_map_versions")
    candidates.push({
      condition: "d.relation='binding_capture' AND d.consumer_id=NEW.id",
      index: "source_taint_binding",
    });
  if (table === "transcript_map_plans")
    candidates.push({
      condition:
        "d.relation='binding_capture' AND d.consumer_id IN (SELECT id FROM transcript_map_versions WHERE plan_id=NEW.id)",
      index: "source_taint_binding",
    });
  const parentSeeds = `SELECT t.kind,t.id FROM source_taint t WHERE t.kind IN (${KINDS},'plan') AND t.id=NEW.${key}
    UNION SELECT t.kind,t.id FROM candidate_edges d JOIN source_taint t ON t.kind IN (${KINDS},'plan') AND t.id=d.input_id
    UNION SELECT t.kind,t.id FROM candidate_edges d JOIN sessions s ON s.source_id=d.input_id JOIN source_taint t ON t.kind='session' AND t.id=s.selector
    UNION SELECT t.kind,t.id FROM candidate_edges d JOIN session_titles title ON title.selector=d.input_id JOIN source_taint t ON t.kind='run' AND t.id=title.run_id
    UNION SELECT t.kind,t.id FROM candidate_edges d JOIN edges e ON e.from_id=d.input_id AND e.to_kind='session' JOIN session_titles title ON title.selector=e.to_id JOIN source_taint t ON t.kind='run' AND t.id=title.run_id WHERE d.relation='legacy_review'
    UNION SELECT t.kind,t.id FROM candidate_edges d JOIN source_taint t ON t.kind='plan' AND t.id=d.consumer_id WHERE d.relation='applied_result'
    ${table === "session_titles" ? "UNION SELECT t.kind,t.id FROM source_taint t WHERE t.kind='run' AND t.id=NEW.run_id" : ""}
    ${table === "sessions" ? "UNION SELECT t.kind,t.id FROM session_titles title JOIN source_taint t ON t.kind='run' AND t.id=title.run_id WHERE title.selector=NEW.selector" : ""}
    ${table === "edges" ? "UNION SELECT t.kind,t.id FROM session_titles title JOIN source_taint t ON t.kind='run' AND t.id=title.run_id WHERE NEW.to_kind='session' AND title.selector=NEW.to_id" : ""}`;
  const candidateSql = `candidate_edges AS MATERIALIZED (${candidates
    .map(
      ({ condition, index }) =>
        `SELECT d.* FROM source_dependency_edges d INDEXED BY ${index}
      WHERE d.relation IN ('dependency','title','current_title','legacy_review','binding_capture','applied_result') AND ${condition}`,
    )
    .join(" UNION ")})`;
  const shadow =
    table === "records"
      ? `d.input_kind=NEW.kind`
      : table === "sessions"
        ? `d.input_kind='session' AND ${UNQUALIFIED_SESSION}`
        : [
              "transcript_map_captures",
              "transcript_map_nodes",
              "transcript_map_summaries",
              "transcript_map_versions",
              "transcript_map_plans",
            ].includes(table)
          ? `(d.input_kind IS NULL OR d.input_kind NOT IN (${KINDS}))`
          : "0";
  return `UPDATE source_taint_state SET dirty=1 WHERE id=1 AND ready=1 AND EXISTS (SELECT 1 FROM session_exclusions)
    AND EXISTS (SELECT 1 FROM source_dependency_edges d JOIN source_taint t ON t.kind=d.consumer_kind AND t.id=d.consumer_id
      WHERE d.relation='dependency' AND d.input_id=NEW.${key} AND ${shadow});
    INSERT OR IGNORE INTO source_taint_seeds(kind,id) WITH ${candidateSql}
      SELECT kind,id FROM (${parentSeeds}) WHERE (${SOURCE_TAINT_READY}) AND EXISTS (SELECT 1 FROM session_exclusions);
    ${PROPAGATE}`;
}

const EXTRA_CATALOGS = ["sessions", "session_titles", "resolution_members"];

export const SOURCE_TAINT_SCHEMA: readonly string[] = [
  `CREATE TABLE source_taint(kind ANY NOT NULL,id ANY NOT NULL,PRIMARY KEY(kind,id)) STRICT, WITHOUT ROWID`,
  `CREATE TABLE source_taint_seeds(kind ANY NOT NULL,id ANY NOT NULL,PRIMARY KEY(kind,id)) STRICT, WITHOUT ROWID`,
  `CREATE TABLE source_taint_state(id INTEGER PRIMARY KEY CHECK(id=1),ready INTEGER NOT NULL DEFAULT 0,dirty INTEGER NOT NULL DEFAULT 0,depth INTEGER NOT NULL DEFAULT 0 CHECK(depth>=0),generation INTEGER NOT NULL DEFAULT 0,propagation INTEGER NOT NULL DEFAULT 0) STRICT`,
  `CREATE INDEX source_taint_sessions_source ON sessions(source_id,selector)`,
  `CREATE INDEX source_taint_legacy_titles ON edges(from_id,to_id) WHERE to_kind='session'`,
  `CREATE INDEX source_taint_producer ON source_dependency_edges(producer_run_id,edge_id) WHERE producer_run_id IS NOT NULL`,
  `CREATE INDEX source_taint_binding ON source_dependency_edges(consumer_id,input_id) WHERE relation='binding_capture'`,
  `CREATE INDEX source_taint_guarded_consumer ON source_dependency_edges(consumer_id,consumer_record_kind,input_id) WHERE consumer_record_kind IS NOT NULL`,
  `CREATE INDEX source_taint_legacy_consumer ON source_dependency_edges(consumer_id,input_id,producer_run_id) WHERE relation='legacy_review'`,
  `CREATE TRIGGER source_taint_propagate AFTER UPDATE OF propagation ON source_taint_state WHEN (${SOURCE_TAINT_READY}) AND EXISTS (SELECT 1 FROM session_exclusions) BEGIN
    INSERT OR IGNORE INTO source_taint(kind,id) ${closure("SELECT kind,id FROM source_taint_seeds", true)} SELECT kind,id FROM active_taint;
    UPDATE source_taint_state SET generation=generation+1 WHERE id=1 AND changes()>0;
    DELETE FROM source_taint_seeds; END`,
  `CREATE TRIGGER source_taint_flush AFTER UPDATE OF depth,dirty ON source_taint_state WHEN NEW.depth=0 AND NEW.dirty=1 AND (${SOURCE_DEPENDENCY_READY}) BEGIN ${REBUILD} END`,
  `CREATE TRIGGER source_taint_projection_progress AFTER UPDATE OF complete,origin ON source_dependency_progress
    WHEN NEW.complete IS NOT OLD.complete OR NEW.origin IS NOT OLD.origin BEGIN ${PROJECTION_CHANGED} END`,
  ...["INSERT", "DELETE"].map(
    (event) =>
      `CREATE TRIGGER source_taint_projection_${event.toLowerCase()} AFTER ${event} ON source_dependency_progress BEGIN ${PROJECTION_CHANGED} END`,
  ),
  `CREATE TRIGGER source_taint_exclusion AFTER INSERT ON session_exclusions WHEN (${SOURCE_TAINT_READY}) BEGIN
    INSERT OR IGNORE INTO source_taint_seeds(kind,id) VALUES('session',NEW.selector); ${PROPAGATE} END`,
  `CREATE TRIGGER source_taint_edge_insert AFTER INSERT ON source_dependency_edges WHEN (${SOURCE_TAINT_READY}) AND EXISTS (SELECT 1 FROM session_exclusions) BEGIN ${INSERT_EDGE_TAINT} END`,
  `CREATE TRIGGER source_taint_edge_delete AFTER DELETE ON source_dependency_edges WHEN EXISTS (SELECT 1 FROM session_exclusions) AND EXISTS (SELECT 1 FROM source_taint WHERE (kind=OLD.consumer_kind AND id=OLD.consumer_id) OR (kind IN (${KINDS},'plan') AND id=OLD.input_id)) BEGIN UPDATE source_taint_state SET dirty=1 WHERE id=1; END`,
  `CREATE TRIGGER source_taint_edge_update AFTER UPDATE OF consumer_kind,consumer_id,input_kind,input_id,consumer_record_kind,input_catalog,input_record_kind,relation,detail,producer_run_id ON source_dependency_edges
    WHEN EXISTS (SELECT 1 FROM session_exclusions) BEGIN
    ${SOURCE_TAINT_BEGIN}
    UPDATE source_taint_state SET dirty=1 WHERE id=1 AND EXISTS (
      SELECT 1 FROM source_taint WHERE (kind=OLD.consumer_kind AND id=OLD.consumer_id)
        OR (kind IN (${KINDS},'plan') AND id=OLD.input_id));
    ${INSERT_EDGE_TAINT}
    ${SOURCE_TAINT_END} END`,
  ...EXTRA_CATALOGS.flatMap((table) => {
    const columns = [CATALOG_KEYS[table]!, ...(CATALOG_COLUMNS[table] ?? [])];
    return [
      `CREATE TRIGGER source_taint_${table}_insert AFTER INSERT ON ${table} BEGIN ${SOURCE_TAINT_BEGIN} ${sourceTaintCatalogAfter(table)} ${SOURCE_TAINT_END} END`,
      `CREATE TRIGGER source_taint_${table}_update AFTER UPDATE OF ${columns.join(",")} ON ${table}
        WHEN ${catalogChanged(table)} BEGIN ${SOURCE_TAINT_BEGIN} ${sourceTaintCatalogBefore(table, "update")} ${sourceTaintCatalogAfter(table)} ${SOURCE_TAINT_END} END`,
      `CREATE TRIGGER source_taint_${table}_delete AFTER DELETE ON ${table} BEGIN ${SOURCE_TAINT_BEGIN} ${sourceTaintCatalogBefore(table, "delete")} ${SOURCE_TAINT_END} END`,
    ];
  }),
];
export const SOURCE_TAINT_FRESH: readonly string[] = [
  "INSERT INTO source_taint_state(id,ready) VALUES(1,1)",
];

/** Rebuild the authoritative taint set before publishing a newly upgraded projection. */
export async function backfillSourceTaint(
  db: Pick<GuestDatabase, "query" | "batch">,
): Promise<boolean> {
  if ((await db.query(`SELECT 1 WHERE ${SOURCE_TAINT_READY}`)).length !== 0) return true;
  await db.batch([
    { sql: "INSERT OR IGNORE INTO source_taint_state(id) VALUES(1)" },
    {
      sql: `UPDATE source_taint_state SET dirty=1 WHERE id=1 AND ready=0 AND (${SOURCE_DEPENDENCY_READY})`,
    },
  ]);
  return (await db.query(`SELECT 1 WHERE ${SOURCE_TAINT_READY}`)).length !== 0;
}

/** Recursive reference for prospective exclusions and exact maintenance proof. */
export function sourcePrivacyClosure(additionalSelectors: readonly string[] = []): {
  readonly sql: string;
  readonly params: readonly GuestSqlParam[];
} {
  return {
    sql: `${closure(`${LEDGER_SEEDS} UNION SELECT 'session',value FROM json_each(?)`)},tainted(kind,id) AS (SELECT kind,id FROM active_taint)`,
    params: [JSON.stringify(additionalSelectors)],
  };
}
