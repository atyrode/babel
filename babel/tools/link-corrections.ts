#!/usr/bin/env bun
/*
  THE ONE-TIME SWEEP FOR CORRECTIONS THE CORPUS ALREADY STATED (#347).

  `server/engine/records.ts` writes the edge at creation from now on. This is the other half:
  the records already in the store said what they corrected in their own first words and no
  edge says it, so the graph a reader walks disagrees with the prose a reader reads.

  It is a dev-time Bun CLI in the shape of `tools/import.ts` and never enters a packed
  artifact. It reads and writes the plugin's own database through ADR 0034's `PluginDatabase`
  contract, and every edge identifier is a digest of the relation it stands for, so a second
  run writes nothing and reports zero.

  WHAT THE SWEEP CAN AND CANNOT RECOVER, which is the finding rather than a shortfall. A marker
  naming a DURABLE identifier is resolvable forever. A marker naming a RUN-LOCAL HANDLE — `o2`,
  `o12`, the handles a result's own `ref` fields carry — is resolvable only while the
  settlement that minted the rows exists, and nothing ever wrote that mapping down. On the
  operator's 6,038-record corpus eleven records carry a marker, two name durable identifiers
  and nine do not: six name a handle and three name only prose. So this sweep repairs two edges
  and reports nine as permanently unrecoverable, and it prints that sentence itself rather than
  leaving it in a pull request nobody re-reads. Guessing which record `o12` meant by
  re-deriving handles from a settled run is exactly the invention the crossing refuses.
*/

import { isAbsolute, join, resolve, sep } from "node:path";
import type { PluginDatabase, SqlParam } from "@manifold/plugin";
import { openPluginDatabase, pluginDatabasePath } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID } from "../contract.ts";
import { correctionMarker } from "../server/engine/records.ts";
import { mintId } from "../server/engine/rows.ts";

/** ADR 0034's caps, restated so a chunk is sized against them rather than against a guess. */
const MAX_SQL_PARAMS = 999;
const MAX_SQL_BATCH_STATEMENTS = 256;
/**
 * The byte cap `tools/import.ts` opens the operator's store with; the default is 256 MiB and
 * the store crosses that on edges alone, so a sweep that took the default would be refused.
 */
const DATABASE_MAX_BYTES = 1024 * 1024 * 1024;

/**
 * Who the swept edges are attributed to. Not `run`: no run wrote them, and an edge claiming a
 * run's authorship would make a repair indistinguishable from the settlement's own work.
 */
const ACTOR_KIND = "engine";
const ACTOR_ID = "link-corrections";

const FAMILIES: Readonly<Record<string, string>> = {
  hyp: "hypothesis",
  obs: "observation",
  fnd: "finding",
  pro: "proposal",
};

/** Why a marker produced no edge. The counts are the report, so the reasons are the buckets. */
type Drop =
  | "names a run-local handle whose run is settled, so the mapping no longer exists"
  | "names a record this store does not hold"
  | "names prose where an identifier belongs";

/** One edge the sweep would write, or did. */
interface Repair {
  readonly id: string;
  readonly kind: string;
  readonly fromKind: string;
  readonly fromId: string;
  readonly toKind: string;
  readonly toId: string;
  readonly position: number;
  readonly note: string;
}

export interface SweepPlan {
  readonly repairs: readonly Repair[];
  /** One line per dropped reference, in the order the records were read. */
  readonly dropped: readonly {
    readonly recordId: string;
    readonly reference: string;
    readonly why: Drop;
  }[];
  /** How many records carried a marker at all, which is the denominator of everything above. */
  readonly marked: number;
}

/**
 * The record's own words, as the marker grammar reads them.
 *
 * The `title` column is bounded at 200 characters and a marker sitting across that boundary
 * would read as prose, so the payload's own headline field is preferred; the column is the
 * fallback for a payload shape this build does not know.
 */
function headline(title: string, payload: string): string {
  let held: unknown;
  try {
    held = JSON.parse(payload);
  } catch {
    return title;
  }
  if (typeof held !== "object" || held === null) return title;
  const fields = held as Record<string, unknown>;
  for (const key of ["statement", "claim", "title"]) {
    const value = fields[key];
    if (typeof value === "string" && value !== "") return value;
  }
  return title;
}

type RecordRow = Record<string, SqlParam> & {
  id: string;
  kind: string;
  title: string;
  payload: string;
};

/**
 * Every record in the store, a page at a time.
 *
 * ADR 0034 caps one result at 4 MiB and the operator's corpus is 6,038 records whose payloads
 * are the whole of each claim: a single `SELECT … payload FROM records` is refused outright,
 * which is a thing only the real corpus says. The page is small because the cap is on BYTES
 * and a record's payload has no bound of its own.
 */
async function* pages(
  db: PluginDatabase,
  columns: string,
  size: number,
): AsyncGenerator<readonly RecordRow[]> {
  for (let offset = 0; ; offset += size) {
    const page = await db.query<RecordRow>(
      `SELECT ${columns} FROM records ORDER BY id LIMIT ? OFFSET ?`,
      [size, offset],
    );
    if (page.length === 0) return;
    yield page;
    if (page.length < size) return;
  }
}

/** Every edge the corpus's own markers ask for, and every marker that asks for nothing. */
export async function planSweep(db: PluginDatabase): Promise<SweepPlan> {
  // TWO PASSES, because a marker may name a record that sorts after the one naming it. The
  // first carries two short columns and the second carries the payloads.
  const held: Record<string, string> = {};
  for await (const page of pages(db, "id, kind, '' AS title, '' AS payload", 2000)) {
    for (const record of page) held[record.id] = record.kind;
  }

  const repairs: Repair[] = [];
  const dropped: { recordId: string; reference: string; why: Drop }[] = [];
  const seen = new Set<string>();
  let marked = 0;
  for await (const page of pages(db, "id, kind, title, payload", 100)) {
    for (const record of page) {
      const marker = correctionMarker(headline(record.title, record.payload));
      if (marker === null) continue;
      marked += 1;
      if (marker.references.length === 0) {
        dropped.push({
          recordId: record.id,
          reference: "",
          why: "names prose where an identifier belongs",
        });
        continue;
      }
      for (const [position, reference] of marker.references.entries()) {
        const kind = held[reference];
        if (kind === undefined) {
          dropped.push({
            recordId: record.id,
            reference,
            why:
              FAMILIES[reference.slice(0, reference.indexOf("_"))] === undefined
                ? "names a run-local handle whose run is settled, so the mapping no longer exists"
                : "names a record this store does not hold",
          });
          continue;
        }
        if (reference === record.id) continue;
        // The identifier is a digest of the relation, so the second sweep mints what the first
        // one did and `INSERT OR IGNORE` makes the write a no-op. `ACTOR_ID` is inside the digest
        // because a repair and a run's own edge over the same pair are two different assertions.
        const id = mintId("edg", ACTOR_ID, `${marker.relation}|${record.id}|${reference}`);
        if (seen.has(id)) continue;
        seen.add(id);
        repairs.push({
          id,
          kind: marker.relation,
          fromKind: record.kind,
          fromId: record.id,
          toKind: kind,
          toId: reference,
          position,
          note: `the record's own text opens ${marker.token}`,
        });
      }
    }
  }
  return { repairs, dropped, marked };
}

/** Which of the planned edges the store does not already hold. */
export async function unwritten(
  db: PluginDatabase,
  repairs: readonly Repair[],
): Promise<readonly Repair[]> {
  const present = new Set<string>();
  for (let from = 0; from < repairs.length; from += MAX_SQL_PARAMS) {
    const asked = repairs.slice(from, from + MAX_SQL_PARAMS).map((repair) => repair.id);
    if (asked.length === 0) continue;
    const rows = await db.query<{ id: string }>(
      `SELECT id FROM edges WHERE id IN (${asked.map(() => "?").join(", ")})`,
      asked,
    );
    for (const row of rows) present.add(row.id);
  }
  return repairs.filter((repair) => !present.has(repair.id));
}

/** Writes the repairs, idempotent on the edge's own identifier. Answers how many landed. */
export async function writeRepairs(
  db: PluginDatabase,
  repairs: readonly Repair[],
  at: string,
): Promise<number> {
  const pending = await unwritten(db, repairs);
  const statements = pending.map((repair) => ({
    sql: `INSERT OR IGNORE INTO edges (id, kind, from_kind, from_id, to_kind, to_id, position,
            note, actor_kind, actor_id, created_at)
          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    params: [
      repair.id,
      repair.kind,
      repair.fromKind,
      repair.fromId,
      repair.toKind,
      repair.toId,
      repair.position,
      repair.note,
      ACTOR_KIND,
      ACTOR_ID,
      at,
    ] satisfies SqlParam[],
  }));
  for (let from = 0; from < statements.length; from += MAX_SQL_BATCH_STATEMENTS) {
    await db.batch(statements.slice(from, from + MAX_SQL_BATCH_STATEMENTS));
  }
  return pending.length;
}

// ---------------------------------------------------------------------------- the command

/** `--into` names the hub's data directory, or the `data.db` the engine would open inside it. */
function resolveTarget(into: string): { dataDir: string; path: string } {
  const absolute = isAbsolute(into) ? into : resolve(into);
  const suffix = `${sep}${join("plugins", BABEL_PLUGIN_ID, "data.db")}`;
  if (absolute.endsWith(suffix)) {
    return { dataDir: absolute.slice(0, absolute.length - suffix.length), path: absolute };
  }
  if (absolute.endsWith(".db")) {
    throw new Error(
      `--into ${into} is not a path the engine would open: a plugin's database is ` +
        `<dataDir>/plugins/${BABEL_PLUGIN_ID}/data.db (ADR 0034). Pass the data directory, or that path.`,
    );
  }
  return { dataDir: absolute, path: pluginDatabasePath(absolute, BABEL_PLUGIN_ID) };
}

const USAGE = `bun babel/tools/link-corrections.ts --into <data.db> [--dry-run]

  --into <path>        the hub data directory, or <dataDir>/plugins/${BABEL_PLUGIN_ID}/data.db
  --dry-run            plan everything and print the counts; write nothing
`;

/** The report, as one block of lines, so a test reads what an operator reads. */
export function report(plan: SweepPlan, written: number): string {
  const lines = [
    `${String(plan.marked)} records open with a correction marker`,
    `${String(written)} edges written`,
    `${String(plan.dropped.length)} references dropped for naming a record this store cannot resolve`,
  ];
  const reasons: Record<string, number> = {};
  for (const drop of plan.dropped) reasons[drop.why] = (reasons[drop.why] ?? 0) + 1;
  for (const [why, count] of Object.entries(reasons)) lines.push(`  ${String(count)}  ${why}`);
  for (const drop of plan.dropped) {
    lines.push(
      `  - ${drop.recordId} ${drop.reference === "" ? "(no identifier)" : drop.reference}`,
    );
  }
  return `${lines.join("\n")}\n`;
}

export async function main(argv: readonly string[]): Promise<number> {
  const flags = new Set(argv.filter((token) => token.startsWith("--")));
  if (flags.has("--help")) {
    process.stdout.write(USAGE);
    return 0;
  }
  const at = argv.indexOf("--into");
  const into = at === -1 ? undefined : argv[at + 1];
  if (into === undefined || into.startsWith("--")) {
    process.stderr.write(`--into <data.db> is required\n\n${USAGE}`);
    return 2;
  }
  const target = resolveTarget(into);
  const db = openPluginDatabase({
    dataDir: target.dataDir,
    pluginId: BABEL_PLUGIN_ID,
    maxBytes: DATABASE_MAX_BYTES,
  });
  try {
    // A STORE WITH NO `records` TABLE IS NOT A BABEL STORE. The enable hook creates the schema
    // and this sweep never does: a tool that created tables could be pointed at the wrong data
    // directory and leave a plausible empty one behind.
    const existing = await db.query<{ name: string }>(
      `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'records'`,
    );
    if (existing.length === 0) {
      process.stderr.write(
        `${target.path} holds no records table, so it is not a Babel store this plugin has ever enabled\n`,
      );
      return 2;
    }
    const plan = await planSweep(db);
    const dryRun = flags.has("--dry-run");
    const written = dryRun
      ? (await unwritten(db, plan.repairs)).length
      : await writeRepairs(db, plan.repairs, new Date().toISOString());
    process.stdout.write(`${target.path}${dryRun ? " (dry run, nothing written)" : ""}\n`);
    process.stdout.write(report(plan, written));
  } finally {
    db.close();
  }
  return 0;
}

if (import.meta.main) {
  process.exitCode = await main(process.argv.slice(2));
}
