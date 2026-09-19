import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { ACTIONS, BABEL_PLUGIN_ID, ExportResultSchema } from "../contract.ts";
import { newId, stamp, type ActsStore } from "../store/acts.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import type { Door } from "./door.ts";
import { exportDoors } from "./export.ts";

/*
  THE EXPORT DOOR (§4.6, #341), against a real plugin database, because what is under test is
  the door's own business: the joins it makes to a record's cited sessions and its newest ruling,
  and the authority it does not hold.
*/

const cleanup: string[] = [];

afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

const AT = stamp(Date.UTC(2026, 8, 12, 12, 0, 0));
const DIGEST = "a".repeat(64);

async function open(): Promise<{ store: ActsStore; door: Door }> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-export-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  const store: ActsStore = { db, now: () => Date.UTC(2026, 8, 12, 12, 0, 0), touch: () => {} };
  for (const statement of SCHEMA_V1) await db.run(statement);
  const door = exportDoors(store).find((candidate) => candidate.action.name === ACTIONS.export);
  if (door === undefined) throw new Error("no export door");
  return { store, door };
}

/** One proposal with a citation, the session it cites, and a ruling on it. */
async function seed(store: ActsStore): Promise<void> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES('pro_00000001', 'proposal', 'pro_00000001', NULL, 0, NULL, 'run_9', NULL, NULL, 'run',
       'run_9', 'fence the drain write', ?, ?)`,
    [
      AT,
      JSON.stringify({
        title: "fence the drain write",
        problem: "a stale worker rewrites a committed batch",
        outcome: "carry the lease fence into the write",
        impact: "high",
        classification: "public-safe",
        verification_criteria: ["a stale worker's batch is refused"],
        supporting: [
          { locator: { path: "sessions/0001-omp-s1.jsonl", line: 412, digest: DIGEST }, note: "" },
        ],
      }),
    ],
  );
  await store.db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, title, workspace, content_digest,
       seen_at)
     VALUES('omp/s1', 'mach_1', 'omp', 'sessions/0001-omp-s1', 'the drain session', '/srv/babel',
       ?, ?)`,
    [DIGEST, AT],
  );
  await store.db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note, actor_kind,
       actor_id, created_at)
     VALUES(?, 'cites', 'proposal', 'pro_00000001', 'session', 'omp/s1', 0, NULL, 'run', 'run_9',
       ?)`,
    [newId("edg"), AT],
  );
  await store.db.run(
    `INSERT INTO dispositions(id, record_id, seq, disposition, duplicate_of_id, note, context_id,
       actor_id, recorded_at)
     VALUES(?, 'pro_00000001', 1, 'accept', NULL, 'do it', NULL, 'alex', ?)`,
    [newId("dsp"), AT],
  );
}

async function knock(door: Door, id: string, projection: string): Promise<unknown> {
  const args = door.action.input.parse({ id, projection });
  return await door.handler({} as unknown as GuestCtx, args as never);
}

test("the export door holds the authority to read and none to publish", () => {
  // THIS IS WHAT "NOTHING IS PUBLISHED" MEANS AT THE DOOR. §4.6 exists to refuse a publisher, so
  // the guarantee is a capability the door does not hold rather than a branch it does not take:
  // with `containers:read` alone and no delegate, it cannot post a job, cannot call another
  // plugin's door and cannot reach a network. A later `delegates` here fails this test.
  const doors = exportDoors({
    db: { query: async () => [] } as never,
    now: () => 0,
    touch: () => {},
  });
  expect(doors).toHaveLength(1);
  expect(doors[0]?.action.caps).toEqual(["containers:read"]);
  expect(doors[0]?.action.delegates).toBeUndefined();
});

test("a record exports with its evidence locators, the session they resolve to, and its standing", async () => {
  const { store, door } = await open();
  await seed(store);

  // The door declares this very schema as its result, so parsing with it is what the kit does.
  const brief = ExportResultSchema.parse(await knock(door, "pro_00000001", "agent-brief"));
  expect(brief).toMatchObject({
    recordId: "pro_00000001",
    projection: "agent-brief",
    classification: "public-safe",
    filename: "agent-brief-pro_00000001.md",
    contentType: "text/markdown",
    withheld: [],
  });
  expect(brief.text).toContain("`sessions/0001-omp-s1.jsonl:412`");
  // The catalog is what can say where the cited bytes are now: the join is the door's work.
  expect(brief.text).toContain("session `omp/s1`, in /srv/babel");

  const note = ExportResultSchema.parse(await knock(door, "pro_00000001", "operator-note"));
  expect(note.text).toContain("ruled accept.");
});

test("an export names a record it does not hold rather than answering with an empty file", async () => {
  const { door } = await open();
  expect(await knock(door, "pro_ffffffff", "operator-note")).toEqual({
    refused: "no record pro_ffffffff",
  });
});

test("a classification that will not travel refuses the draft, naming the classification", async () => {
  const { store, door } = await open();
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES('pro_00000002', 'proposal', 'pro_00000002', NULL, 0, NULL, 'run_9', NULL, NULL, 'run',
       'run_9', 'a private proposal', ?, '{"problem":"p","outcome":"o","classification":"private"}')`,
    [AT],
  );

  expect(await knock(door, "pro_00000002", "issue-draft")).toEqual({
    refused:
      "pro_00000002 is classified private, and an issue draft leaves this deployment; only a " +
      "public-safe record is rendered whole for one, and a redaction-required record is " +
      "rendered without its evidence",
  });
  // It is still the operator's own record, and he can read it out.
  expect(
    ExportResultSchema.parse(await knock(door, "pro_00000002", "operator-note")).text,
  ).toContain("- classification: private");
});
