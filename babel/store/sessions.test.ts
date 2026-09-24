import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase } from "@manifold/plugin";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID, SessionRowSchema, type SessionRow } from "../contract.ts";
import { SCHEMA_V1 } from "./schema.ts";
import { upsertSessionRows, type SessionsStore } from "./sessions.ts";

/*
  Against a real plugin database migrated with `SCHEMA_V1`, because every rule under test is a
  SQL condition: which of the three guarded statements applies is SQLite's answer, not this
  module's, and a fake handle would agree with whatever the module assumed.

  Every row is synthetic: made-up selectors, paths and snapshot ids. Nothing here reads a
  transcript or an archive.
*/

const cleanup: string[] = [];

afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

async function openStore(): Promise<SessionsStore & { db: PluginDatabase; touched: number }> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-sessions-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  for (const statement of SCHEMA_V1) await db.run(statement);
  const store = {
    db,
    touched: 0,
    touch: () => {
      store.touched += 1;
    },
  };
  return store;
}

const SNAPSHOT = { a: "a".repeat(64), b: "b".repeat(64), c: "c".repeat(64) } as const;
const SEEN = "2026-09-24T22:30:00.000Z";
const LATER = "2026-09-24T23:30:00.000Z";
const DIGEST = `sha256:${"d".repeat(64)}`;

/** One catalogued capture of a synthetic OMP session, as `catalog` would write it. */
function captured(id: string, overrides: Partial<SessionRow> = {}): SessionRow {
  return SessionRowSchema.parse({
    selector: `omp/-work-project/${id}`,
    harness: "omp",
    source_id: `-work-project/${id}`,
    kind: "operator",
    archive_label: "dev-01",
    archive_path: `/home/operator/.omp/agent/sessions/-work-project/${id}.jsonl`,
    snapshot_id: SNAPSHOT.a,
    archived_at: "2026-09-24T21:00:00.000Z",
    size: 1000,
    modified_at: "2026-09-24T20:59:00.000Z",
    ...overrides,
  });
}

async function map(store: SessionsStore, label: string, machineId: string): Promise<void> {
  await store.db.run(
    `INSERT INTO archive_labels(label, machine_id, mapped_at) VALUES(?, ?, '2026-09-24T00:00:00.000000000Z')`,
    [label, machineId],
  );
}

/** The columns a rule decides, with SQLite's integers read back as numbers. */
async function session(store: SessionsStore, id: string): Promise<Record<string, unknown>> {
  const [row] = await store.db.query(
    `SELECT host, live, archive_label, archive_path, snapshot_id, archived_at, size, modified_at,
            content_digest, title, title_provenance, workspace, repository_identity, cost_usd,
            seen_at
       FROM sessions WHERE selector = ?`,
    [`omp/-work-project/${id}`],
  );
  if (row === undefined) throw new Error(`no session ${id}`);
  return Object.fromEntries(
    Object.entries(row).map(([key, value]) => [
      key,
      typeof value === "bigint" ? Number(value) : value,
    ]),
  );
}

test("a first capture is inserted and hosted where its label is mapped, and nowhere otherwise", async () => {
  const store = await openStore();
  await map(store, "dev-01", "m-dev-01");

  const upserted = await upsertSessionRows(
    store,
    [captured("s1"), captured("s2", { archive_label: "workstation-linux" })],
    SEEN,
  );

  expect(upserted).toEqual({ inserted: 2, moved: 0, kept: 0, ignored: 0, unmapped: 1 });
  expect(await session(store, "s1")).toMatchObject({
    host: "m-dev-01",
    live: 0,
    archive_label: "dev-01",
    snapshot_id: SNAPSHOT.a,
    content_digest: null,
    seen_at: SEEN,
  });
  // A label nobody mapped hosts nothing, and `''` is what the hub already reads as that.
  expect(await session(store, "s2")).toMatchObject({
    host: "",
    archive_label: "workstation-linux",
  });
  expect(store.touched).toBe(1);
});

test("the same observation keeps the capture and digest it already names while a reading's facts land", async () => {
  const store = await openStore();
  await upsertSessionRows(store, [captured("s1")], SEEN);

  // The preparation that read snapshot A reports what it found in those bytes.
  const read = captured("s1", {
    content_digest: DIGEST,
    title: "Port the catalog",
    title_provenance: "recorded",
    workspace: "/work/project",
    cost_usd: 1.25,
  });
  expect(await upsertSessionRows(store, [read], SEEN)).toMatchObject({ kept: 1 });

  // A later snapshot holds the very same file: the row keeps naming A, so the digest and every
  // reading keyed on A stay valid, and only `seen_at` says it was catalogued again.
  const again = captured("s1", {
    snapshot_id: SNAPSHOT.b,
    archived_at: "2026-09-24T22:00:00.000Z",
  });
  expect(await upsertSessionRows(store, [again], LATER)).toEqual({
    inserted: 0,
    moved: 0,
    kept: 1,
    ignored: 0,
    unmapped: 1,
  });
  expect(await session(store, "s1")).toMatchObject({
    snapshot_id: SNAPSHOT.a,
    archived_at: "2026-09-24T21:00:00.000Z",
    content_digest: DIGEST,
    title: "Port the catalog",
    title_provenance: "recorded",
    workspace: "/work/project",
    cost_usd: 1.25,
    seen_at: LATER,
  });
});

test("a newer observation moves the row onto its capture and clears the digest; an older one changes nothing", async () => {
  const store = await openStore();
  await map(store, "dev-01", "m-dev-01");
  await upsertSessionRows(
    store,
    [
      captured("s1", {
        content_digest: DIGEST,
        title: "Port the catalog",
        title_provenance: "recorded",
        workspace: "/work/project",
        cost_usd: 1.25,
      }),
    ],
    SEEN,
  );
  const before = await session(store, "s1");

  // A backfill of an older snapshot, and a preparation of a capture the catalog has since moved
  // past: both describe other bytes, and neither may overwrite the capture the row names.
  const older = captured("s1", {
    snapshot_id: SNAPSHOT.c,
    archived_at: "2026-09-23T21:00:00.000Z",
    size: 900,
    modified_at: "2026-09-23T20:59:00.000Z",
  });
  const superseded = { ...older, content_digest: `sha256:${"e".repeat(64)}`, title: "Old name" };
  const ignored = await upsertSessionRows(
    store,
    [older, SessionRowSchema.parse({ ...superseded, title_provenance: "recorded" })],
    LATER,
  );
  expect(ignored).toMatchObject({ inserted: 0, moved: 0, kept: 0, ignored: 2 });
  expect(await session(store, "s1")).toEqual(before);

  // The session grew and a newer snapshot holds it: the row moves, and the digest of the old
  // bytes goes with them. What the old reading said stays until the new capture is read.
  const grown = captured("s1", {
    snapshot_id: SNAPSHOT.b,
    archived_at: "2026-09-24T23:00:00.000Z",
    size: 1200,
    modified_at: "2026-09-24T22:59:00.000Z",
  });
  expect(await upsertSessionRows(store, [grown], LATER)).toMatchObject({ moved: 1 });
  expect(await session(store, "s1")).toMatchObject({
    host: "m-dev-01",
    snapshot_id: SNAPSHOT.b,
    archived_at: "2026-09-24T23:00:00.000Z",
    size: 1200,
    modified_at: "2026-09-24T22:59:00.000Z",
    content_digest: null,
    title: "Port the catalog",
    workspace: "/work/project",
    cost_usd: 1.25,
    seen_at: LATER,
  });
});

test("a title a model inferred survives a reading that found none, and a recorded one replaces it", async () => {
  const store = await openStore();
  await upsertSessionRows(store, [captured("s1")], SEEN);
  await store.db.run(
    `UPDATE sessions SET title = 'Named by a model', title_provenance = 'inferred'
      WHERE selector = 'omp/-work-project/s1'`,
  );

  await upsertSessionRows(store, [captured("s1", { content_digest: DIGEST, title: null })], SEEN);
  expect(await session(store, "s1")).toMatchObject({
    title: "Named by a model",
    title_provenance: "inferred",
  });

  await upsertSessionRows(
    store,
    [captured("s1", { title: "What the harness called it", title_provenance: "recorded" })],
    SEEN,
  );
  expect(await session(store, "s1")).toMatchObject({
    title: "What the harness called it",
    title_provenance: "recorded",
  });
});

test("an imported session gets its first capture: its old digest goes, its host and what it said stay", async () => {
  const store = await openStore();
  // A Go-era row as the crossing left it: hosted at a host NAME, with a digest of bytes no
  // capture addresses, and an `archive` snapshot recorded in restic's own spelling of the time —
  // later than the capture below, which must not matter to a row that names no capture path.
  await store.db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, title, title_provenance, workspace,
       repository_identity, cost_usd, content_digest, snapshot_id, archived_at, seen_at)
     VALUES('omp/-work-project/s1', 'dev-01', 'omp', '-work-project/s1', 'Begin issue #91',
       'recorded', '/work/project', 'github.com/example/project', 162.19, ?, 'legacy', ?, ?)`,
    [DIGEST, "2026-09-25T10:00:00.123456789+02:00", "2026-09-01T00:00:00Z"],
  );

  expect(await upsertSessionRows(store, [captured("s1")], SEEN)).toEqual({
    inserted: 0,
    moved: 1,
    kept: 0,
    ignored: 0,
    unmapped: 1,
  });
  expect(await session(store, "s1")).toMatchObject({
    // Unmapped: the row keeps the host it had until `rehostSessions` moves it.
    host: "dev-01",
    archive_label: "dev-01",
    snapshot_id: SNAPSHOT.a,
    content_digest: null,
    title: "Begin issue #91",
    title_provenance: "recorded",
    workspace: "/work/project",
    repository_identity: "github.com/example/project",
    cost_usd: 162.19,
  });
});

test("a catalog larger than one batch lands whole, and replaying it writes nothing new", async () => {
  const store = await openStore();
  const rows = Array.from({ length: 205 }, (_, at) => captured(`s${String(at)}`));

  expect(await upsertSessionRows(store, rows, SEEN)).toEqual({
    inserted: 205,
    moved: 0,
    kept: 0,
    ignored: 0,
    unmapped: 205,
  });
  expect(await upsertSessionRows(store, rows, LATER)).toEqual({
    inserted: 0,
    moved: 0,
    kept: 205,
    ignored: 0,
    unmapped: 205,
  });
  const [counted] = await store.db.query<{ n: number | bigint }>(
    `SELECT COUNT(*) AS n FROM sessions WHERE seen_at = ? AND snapshot_id = ?`,
    [LATER, SNAPSHOT.a],
  );
  expect(Number(counted?.n)).toBe(205);
});
