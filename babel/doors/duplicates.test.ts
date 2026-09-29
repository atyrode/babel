import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  type DuplicateApplied,
  type DuplicateIntent,
  type DuplicatePreview,
} from "../contract.ts";
import { DEFAULT_POLICY, setPolicy, stamp, type ActsStore } from "../store/acts.ts";
import { duplicatePlan } from "../store/duplicates.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { actDoors } from "./acts.ts";
import { duplicateDoors } from "./duplicates.ts";
import type { Door } from "./door.ts";
import { suggestDoors } from "./suggest.ts";

const cleanup: string[] = [];
const AT = Date.UTC(2026, 8, 29);
const A = "fnd_00000001";
const B = "fnd_00000002";
afterEach(() => {
  for (const path of cleanup.splice(0)) rmSync(path, { recursive: true, force: true });
});

async function fixture(): Promise<{
  store: ActsStore;
  doors: readonly Door[];
  intent: DuplicateIntent;
}> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-duplicate-doors-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  for (const sql of SCHEMA_V1) await db.run(sql);
  const store = { db, now: () => AT, touch: () => {} };
  await setPolicy(
    store,
    {
      ...DEFAULT_POLICY,
      suggesters: [{ principalId: "jev", pluginId: "atyrode.babel.jev", note: "allowed" }],
    },
    "",
    "owner",
    16,
  );
  for (const id of [A, B])
    await db.run(
      `INSERT INTO records(id, kind, root_id, seq, actor_kind, actor_id, title, created_at, payload)
    VALUES(?, 'finding', ?, 0, 'run', 'author', 'same finding', ?, '{"pattern":"same claim"}')`,
      [id, id, stamp(AT)],
    );
  const plan = await duplicatePlan(store, { after: "", limit: 24, ids: [] });
  const intent: DuplicateIntent = {
    kind: "merge-duplicate-records",
    representative: A,
    members: plan.candidates.map(
      ({ recordId, revision, fingerprint, kind, runId, sourceIds, unresolvedSources }) => ({
        recordId,
        revision,
        fingerprint,
        kind,
        runId,
        sourceIds,
        unresolvedSources,
      }),
    ),
    pairs: [{ a: A, b: B, evidence: "same claim and evidence" }],
    audit: { records: 2, distinctRuns: 0, distinctSources: 0, missingRuns: 2, missingSources: 2 },
  };
  return {
    store,
    intent,
    doors: [
      ...duplicateDoors(store),
      ...suggestDoors(store),
      ...actDoors(store, 16, (() => ({ describe: async () => ({}) })) as never),
    ],
  };
}

async function call(
  doors: readonly Door[],
  name: string,
  input: unknown,
  root: boolean,
  principal = "owner",
): Promise<unknown> {
  const door = doors.find((held) => held.action.name === name);
  if (door === undefined) throw new Error(`missing ${name}`);
  const ctx = {
    principal: { id: principal },
    auth: { isRoot: root, caps: ["containers:read", "containers:write"] },
    emit: () => {},
  } as unknown as GuestCtx;
  const result: unknown = await door.handler(ctx, door.action.input.parse(input) as never);
  if (typeof result === "object" && result !== null && "refused" in result) return result;
  return door.action.result.parse(result);
}

async function suggestCluster(doors: readonly Door[], intent: DuplicateIntent): Promise<string> {
  const result = (await call(
    doors,
    ACTIONS.suggest,
    {
      recordId: A,
      revision: 0,
      kind: "ask-question",
      aspect: "duplicate-cluster",
      summary: "link duplicates",
      rationale: "same claim",
      intent,
    },
    false,
    "jev",
  )) as { id: string };
  return result.id;
}

test("a write-capable allowed adviser can suggest but cannot preview or apply an operator action", async () => {
  const { store, doors, intent } = await fixture();
  const id = await suggestCluster(doors, intent);
  expect(await call(doors, ACTIONS.duplicatePreview, { nextActionId: id }, false, "jev")).toEqual({
    refused: "duplicate preview is the owner's act",
  });
  expect(
    await call(
      doors,
      ACTIONS.duplicateApply,
      { nextActionId: id, fingerprint: "0".repeat(64), confirm: true },
      false,
      "jev",
    ),
  ).toEqual({ refused: "duplicate application is the owner's act" });
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
});

test("root preview is read-only, generic decide stays inert, explicit apply attributes only the authenticated principal", async () => {
  const { store, doors, intent } = await fixture();
  const id = await suggestCluster(doors, intent);
  const preview = (await call(
    doors,
    ACTIONS.duplicatePreview,
    { nextActionId: id },
    true,
  )) as DuplicatePreview;
  expect(preview.state).toBe("ready");
  await call(
    doors,
    ACTIONS.decide,
    { nextActionId: id, decision: "accepted", note: "not an apply" },
    true,
  );
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
  const applied = (await call(
    doors,
    ACTIONS.duplicateApply,
    { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
    true,
    "authenticated-owner",
  )) as DuplicateApplied;
  expect(applied.operatorId).toBe("authenticated-owner");
  expect(applied.links).toEqual([{ fromId: A, toId: B, kind: "corroborates" }]);
  const door = doors.find((held) => held.action.name === ACTIONS.duplicateApply);
  expect(
    door?.action.input.safeParse({
      nextActionId: id,
      fingerprint: preview.fingerprint,
      confirm: true,
      operatorId: "forged",
    }).success,
  ).toBe(false);
  expect(
    door?.action.input.safeParse({
      nextActionId: id,
      fingerprint: preview.fingerprint,
      confirm: false,
    }).success,
  ).toBe(false);
  expect(
    await call(
      doors,
      ACTIONS.duplicateApply,
      { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
      true,
      "second-owner",
    ),
  ).toEqual(applied);
});
