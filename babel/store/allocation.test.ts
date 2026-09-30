import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import {
  ACTIONS,
  ALLOCATION_LOOKBACK_MS,
  ALLOCATION_MAX_PLAN_BYTES,
  ALLOCATION_MAX_POLICY_BYTES,
  ALLOCATION_MAX_PROVENANCE,
  ALLOCATION_MAX_SAVED_BYTES,
  ALLOCATION_MIN_SAMPLE,
  BABEL_PLUGIN_ID,
  OPERATIONS,
  type Activity,
  type AllocationPreview,
  type AllocationVersion,
  type Ruling,
} from "../contract.ts";
import { allocationDoors } from "../doors/allocation.ts";
import { stamp, type ActsStore } from "./acts.ts";
import {
  allocationVersion,
  previewAllocation,
  replayAllocation,
  saveAllocation,
} from "./allocation.ts";
import { DEFAULT_POLICY } from "./coordinator.ts";
import { SCHEMA_V1 } from "./schema.ts";

const NOW = Date.UTC(2026, 8, 29, 12);
const cleanup: string[] = [];
afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

async function fixture(
  weights = { review: 0.4, explore: 0.2, challenge: 0.4, synthesize: 0, map: 0 },
  enabled = true,
  configured = true,
): Promise<ActsStore> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-allocation-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  for (const sql of SCHEMA_V1) await db.run(sql);
  if (configured)
    await db.run(
      "INSERT INTO policies(version,seq,actor_id,reason,payload,recorded_at) VALUES('baseline',1,'owner','fixture',?,?)",
      [
        JSON.stringify({
          ...DEFAULT_POLICY,
          version: "baseline",
          enabled,
          activityWeights: weights,
        }),
        stamp(NOW),
      ],
    );
  return {
    db,
    now: () => NOW,
    touch: () => {
      throw new Error("allocation must not invalidate the work/feed state");
    },
  };
}

async function proposal(
  store: ActsStore,
  id: string,
  activity: Activity | null = "challenge",
  options: { root?: string; seq?: number; payload?: object; operatorAuthored?: boolean } = {},
): Promise<void> {
  const runId = activity === null ? null : `run_${id}`;
  if (runId) {
    const preparation =
      activity === "review" ? { review: { role: "reception" } } : { analysis: { stage: activity } };
    await store.db.run(
      "INSERT INTO runs(id,kind,started_at,payload,preparation) VALUES(?,?,?,'{}',?)",
      [
        runId,
        activity === "review" ? OPERATIONS.evaluate : OPERATIONS.explore,
        stamp(NOW - 1000),
        JSON.stringify(preparation),
      ],
    );
  }
  await store.db.run(
    "INSERT INTO records(id,kind,root_id,seq,run_id,actor_kind,actor_id,title,created_at,payload) VALUES(?,'proposal',?,?,?,?,?,'synthetic improvement',?,?)",
    [
      id,
      options.root ?? id,
      options.seq ?? 0,
      runId,
      options.operatorAuthored ? "operator" : "run",
      options.operatorAuthored ? "owner" : (runId ?? "missing"),
      stamp(NOW - 1000),
      JSON.stringify(options.payload ?? {}),
    ],
  );
}

async function ruling(
  store: ActsStore,
  id: string,
  decision: Ruling,
  at = stamp(NOW),
  seq = 1,
  actor = "owner",
): Promise<void> {
  await store.db.run(
    "INSERT INTO dispositions(id,record_id,seq,disposition,actor_id,recorded_at) VALUES(?,?,?,?,?,?)",
    [`${id}_${String(seq)}`, id, seq, decision, actor, at],
  );
}

function slice(plan: AllocationPreview, activity: Activity) {
  return plan.slices.find((entry) => entry.activity === activity)!;
}

async function sample(
  store: ActsStore,
  activity: Activity,
  decision: Ruling,
  n = ALLOCATION_MIN_SAMPLE,
): Promise<void> {
  for (let i = 0; i < n; i += 1) {
    const id = `${activity}_${String(i)}`;
    await proposal(store, id, activity);
    await ruling(store, id, decision);
  }
}

const preview = (store: ActsStore) => previewAllocation(store, { edits: [] });

test("a quiet window preserves the configured baseline and reports unknown eligibility rather than zero", async () => {
  const store = await fixture();
  await proposal(store, "old");
  await ruling(store, "old", "accept", stamp(NOW - ALLOCATION_LOOKBACK_MS - 1));
  const plan = await preview(store);
  expect(plan.slices.map((entry) => entry.proposed)).toEqual([0.4, 0.2, 0.4, 0, 0]);
  expect(plan.slices.every((entry) => entry.sample === 0 && entry.rate === null)).toBe(true);
  expect(plan.snapshot.inventories.find((row) => row.key === "eligible-work")?.count).toBeNull();
  expect(plan.snapshot.feedback).toEqual([]);
  expect(plan.snapshot.coverage).toMatchObject({
    historicalRulings: 1,
    windowRulings: 0,
    candidateRoots: 0,
  });
  expect(plan.snapshot.cutoff).toBe(new Date(NOW - ALLOCATION_LOOKBACK_MS).toISOString());
  expect(replayAllocation(plan.snapshot)).toEqual(plan);
});

test("the exact seven-day boundary is inclusive and one nanosecond beyond either endpoint is excluded", async () => {
  const store = await fixture();
  const cutoff = NOW - ALLOCATION_LOOKBACK_MS;
  const offset = (at: number, fraction: string) =>
    `${new Date(at + 2 * 60 * 60 * 1000).toISOString().slice(0, 19)}.${fraction}+02:00`;
  const negativeOffset = (at: number, fraction: string) =>
    `${new Date(at - 5 * 60 * 60 * 1000).toISOString().slice(0, 19)}.${fraction}-05:00`;
  for (const [id, at] of [
    ["boundary", stamp(cutoff)],
    ["before", stamp(cutoff - 1000).replace("000000000Z", "999999999Z")],
    ["now", stamp(NOW)],
    ["future", stamp(NOW).replace("000000000Z", "000000001Z")],
    ["offset-boundary", offset(cutoff, "000000000")],
    ["offset-before", offset(cutoff - 1000, "999999999")],
    ["offset-now", offset(NOW, "000000000")],
    ["offset-future", offset(NOW, "000000001")],
    ["negative-boundary", negativeOffset(cutoff, "000000000")],
    ["negative-future", negativeOffset(NOW, "000000001")],
    ["offset-short", offset(NOW - 1000, "1")],
  ]) {
    await proposal(store, id!);
    await ruling(store, id!, "accept", at!);
  }
  const plan = await preview(store);
  expect(slice(plan, "challenge").sample).toBe(6);
  expect(
    plan.snapshot.feedback.filter((row) => row.excluded === "counted").map((row) => row.rootId),
  ).toEqual([
    "boundary",
    "negative-boundary",
    "now",
    "offset-boundary",
    "offset-now",
    "offset-short",
  ]);
  expect(plan.snapshot.coverage.historicalRulings).toBe(2);
  expect(plan.snapshot.coverage.futureRulings).toBe(3);
});

test("repeated answers and revised records count once without renewing an expired answer", async () => {
  const store = await fixture();
  await proposal(store, "root");
  await ruling(store, "root", "accept", stamp(NOW - ALLOCATION_LOOKBACK_MS - 1));
  await proposal(store, "revision", "review", { root: "root", seq: 1 });
  await ruling(store, "revision", "accept");
  expect(slice(await preview(store), "challenge").sample).toBe(0);
  expect(slice(await preview(store), "review").sample).toBe(0);
  await ruling(store, "revision", "reject", stamp(NOW), 2);
  const plan = await preview(store);
  expect(slice(plan, "review")).toMatchObject({ accepted: 0, rejected: 1, sample: 1 });
  expect(plan.snapshot.feedback).toMatchObject([{ recordId: "revision", supersededDecisions: 2 }]);
  await ruling(store, "revision", "defer", stamp(NOW), 3);
  expect(slice(await preview(store), "review").sample).toBe(0);
});

test("administrative rulings, nondecisions, missing attribution and operator-authored records earn no credit", async () => {
  const store = await fixture();
  await proposal(store, "topic", "review", { payload: { topic: { operation: "create" } } });
  await ruling(store, "topic", "accept");
  await proposal(store, "linked-plan");
  await store.db.run(
    "INSERT INTO plans(id,kind,subject_kind,subject_id,operation,payload,proposed_by_kind,proposed_by_id,created_at) VALUES('admin','topic','proposal','linked-plan','merge','{}','run','run_linked-plan',?)",
    [stamp(NOW)],
  );
  await ruling(store, "linked-plan", "reject");
  await proposal(store, "duplicate");
  await ruling(store, "duplicate", "duplicate");
  await proposal(store, "deferred");
  await ruling(store, "deferred", "defer");
  await proposal(store, "pending");
  await proposal(store, "no-source", null);
  await ruling(store, "no-source", "accept");
  await proposal(store, "no-operator");
  await ruling(store, "no-operator", "reject", stamp(NOW), 1, "");
  await proposal(store, "operator", "challenge", { operatorAuthored: true });
  await ruling(store, "operator", "accept");
  for (const [id, preparation] of [
    ["legacy-empty", "{}"],
    ["legacy-null", null],
    ["legacy-null-stage", '{"analysis":{"stage":null}}'],
  ] as const) {
    await proposal(store, id, "explore");
    await store.db.run("UPDATE runs SET preparation = ? WHERE id = ?", [preparation, `run_${id}`]);
    await ruling(store, id, "accept");
  }
  const plan = await preview(store);
  expect(
    plan.slices.every((entry) => entry.sample === 0 && entry.proposed === entry.baseline),
  ).toBe(true);
  expect(plan.snapshot.feedback.map((entry) => [entry.rootId, entry.excluded])).toEqual([
    ["deferred", "not-decided"],
    ["duplicate", "not-decided"],
    ["legacy-empty", "no-producing-activity"],
    ["legacy-null", "no-producing-activity"],
    ["legacy-null-stage", "no-producing-activity"],
    ["linked-plan", "administrative"],
    ["no-operator", "no-operator"],
    ["no-source", "no-producing-activity"],
    ["operator", "no-producing-activity"],
    ["topic", "administrative"],
  ]);
});

test("sparse evidence cannot displace another duty; sufficiently sampled outcomes damp only the discretionary remainder", async () => {
  const store = await fixture();
  await sample(store, "review", "accept");
  await sample(store, "challenge", "reject", ALLOCATION_MIN_SAMPLE - 1);
  const sparse = await preview(store);
  expect(sparse.slices.map((entry) => entry.proposed)).toEqual([0.4, 0.2, 0.4, 0, 0]);
  expect(slice(sparse, "challenge").evidence).toBe("sparse");
  await proposal(store, "last", "challenge");
  await ruling(store, "last", "reject");
  await sample(store, "synthesize", "accept");
  const plan = await preview(store);
  expect(slice(plan, "review").proposed).toBeGreaterThan(0.4);
  expect(slice(plan, "review").damping).toBeCloseTo(0.05, 12);
  expect(slice(plan, "challenge").proposed).toBeLessThan(0.4);
  expect(slice(plan, "explore").proposed).toBe(0.2);
  expect(slice(plan, "synthesize").proposed).toBe(0);
  expect(plan.slices.reduce((sum, entry) => sum + entry.proposed, 0)).toBeCloseTo(1, 12);
  expect(plan.slices.every((entry) => entry.proposed >= entry.protected)).toBe(true);
  expect(replayAllocation(plan.snapshot)).toEqual(plan);
});

test("self-generated inventory growth changes the visible basis, not rewards or the arithmetic", async () => {
  const store = await fixture();
  await sample(store, "review", "accept");
  await sample(store, "challenge", "reject");
  const before = await preview(store);
  for (let i = 0; i < 20; i += 1) await proposal(store, `unreviewed_${String(i)}`);
  const after = await preview(store);
  expect(after.slices).toEqual(before.slices);
  expect(before.snapshot.inventories.find((row) => row.key === "proposal")?.count).toBe(16);
  expect(after.snapshot.inventories.find((row) => row.key === "proposal")?.count).toBe(36);
  expect(after.basis.digest).not.toBe(before.basis.digest);
});

test("edits renormalize the permitted remainder, protect exploration and never enable a zero duty", async () => {
  const store = await fixture();
  const initial = await preview(store);
  const edited = await previewAllocation(store, {
    basis: initial.basis,
    edits: [{ activity: "challenge", fraction: 0.2 }],
  });
  expect(slice(edited, "challenge").proposed).toBe(0.2);
  expect(slice(edited, "explore").proposed).toBe(0.2);
  expect(slice(edited, "review").proposed).toBeCloseTo(0.6, 12);
  expect(edited.slices.reduce((sum, entry) => sum + entry.proposed, 0)).toBeCloseTo(1, 12);
  await expect(
    previewAllocation(store, {
      basis: initial.basis,
      edits: [{ activity: "explore", fraction: 0.1 }],
    }),
  ).rejects.toThrow(/protected/);
  await expect(
    previewAllocation(store, {
      basis: initial.basis,
      edits: [{ activity: "synthesize", fraction: 0.1 }],
    }),
  ).rejects.toThrow(/disabled/);
  await expect(
    previewAllocation(store, {
      basis: initial.basis,
      edits: [{ activity: "challenge", fraction: 0.9 }],
    }),
  ).rejects.toThrow(/remainder/);
  const zero = await fixture({ review: 0, explore: 0, challenge: 0, synthesize: 0, map: 0 });
  const unallocated = await preview(zero);
  expect(unallocated.unallocatedFraction).toBe(1);
  expect(unallocated.slices.map((entry) => entry.proposed)).toEqual([0, 0, 0, 0, 0]);
});

async function authority(store: ActsStore) {
  return await store.db.query(`SELECT
    (SELECT json_group_array(json_object('version',version,'seq',seq,'payload',payload)) FROM policies) AS policies,
    (SELECT count(*) FROM budgets) AS budgets, (SELECT count(*) FROM claims) AS claims,
    (SELECT count(*) FROM runs) AS runs, (SELECT count(*) FROM drains) AS drains,
    (SELECT count(*) FROM facts) AS facts, (SELECT count(*) FROM steering) AS steering`);
}

test("owner preview/edit/save and version read spend nothing and preserve policy, caps and all admission ledgers", async () => {
  const store = await fixture();
  const doors = allocationDoors(store);
  const ctx = {
    auth: { isRoot: true },
    principal: { id: "authenticated-owner" },
  } as unknown as GuestCtx;
  const call = async (name: string, input: unknown) => {
    const door = doors.find((candidate) => candidate.action.name === name)!;
    const result = await door.handler(ctx, door.action.input.parse(input) as never);
    return door.action.result.parse(result);
  };
  const before = await authority(store);
  const plan = (await call(ACTIONS.previewAllocation, {})) as AllocationPreview;
  expect(await store.db.query("SELECT version FROM allocation_plans")).toEqual([]);
  const edited = (await call(ACTIONS.previewAllocation, {
    basis: plan.basis,
    edits: [{ activity: "challenge", fraction: 0.2 }],
  })) as AllocationPreview;
  const saved = (await call(ACTIONS.saveAllocation, {
    name: "week-40",
    reason: "Prefer reviewed improvements",
    basis: edited.basis,
    edits: edited.edits,
  })) as AllocationVersion;
  expect(saved.actorId).toBe("authenticated-owner");
  expect(saved.plan).toEqual(edited);
  expect(await authority(store)).toEqual(before);
  expect(await call(ACTIONS.allocationVersion, {})).toEqual(saved);
  expect(await call(ACTIONS.allocationVersion, { version: "week-40" })).toEqual(saved);
  expect(await call(ACTIONS.allocationVersion, { version: "missing" })).toBeNull();
  expect(replayAllocation(saved.plan.snapshot, saved.plan.edits)).toEqual(saved.plan);
  await expect(store.db.run("UPDATE allocation_plans SET reason='rewrite'")).rejects.toThrow(
    /never edited/,
  );
  await expect(store.db.run("DELETE FROM allocation_plans")).rejects.toThrow(/never deleted/);
});

test("stale evidence, policy and a competing named save are refused without replacing the saved version", async () => {
  const store = await fixture();
  const plan = await preview(store);
  await proposal(store, "arrived");
  await ruling(store, "arrived", "accept");
  const request = { name: "saved", reason: "explicit intention", basis: plan.basis, edits: [] };
  await expect(saveAllocation(store, request, "owner")).rejects.toThrow(/stale/);
  const fresh = await preview(store);
  const saved = await saveAllocation(store, { ...request, basis: fresh.basis }, "owner");
  await expect(
    saveAllocation(store, { ...request, basis: fresh.basis, name: "competing" }, "owner"),
  ).rejects.toThrow(/stale/);
  const beforePolicy = await preview(store);
  await store.db.run(
    "INSERT INTO policies(version,seq,actor_id,payload,recorded_at) VALUES('changed',2,'owner',?,?)",
    [JSON.stringify({ ...DEFAULT_POLICY, version: "changed" }), stamp(NOW)],
  );
  await expect(
    saveAllocation(store, { ...request, name: "new-policy", basis: beforePolicy.basis }, "owner"),
  ).rejects.toThrow(/stale/);
  expect(await allocationVersion(store, "saved")).toEqual(saved);
  const current = await preview(store);
  await expect(
    saveAllocation(store, { ...request, basis: current.basis }, "owner"),
  ).rejects.toThrow(/already exists/);
});

test("nonowners cannot preview sensitive basis, edit, save or read saved versions", async () => {
  const store = await fixture();
  const plan = await preview(store);
  const doors = allocationDoors(store);
  const ctx = { auth: { isRoot: false }, principal: { id: "other" } } as unknown as GuestCtx;
  for (const door of doors) {
    const input =
      door.action.name === ACTIONS.saveAllocation
        ? { name: "denied", reason: "not authorized", basis: plan.basis }
        : {};
    const result = await door.handler(ctx, door.action.input.parse(input) as never);
    expect(result).toMatchObject({ refused: expect.stringContaining("owner") });
  }
  expect(await allocationVersion(store)).toBeNull();
});

test("disabled and never-configured policies authorize no allocation despite positive default weights", async () => {
  for (const configured of [true, false]) {
    const store = await fixture(undefined, false, configured);
    const before = await authority(store);
    const plan = await preview(store);
    expect(
      plan.slices.map((entry) => [
        entry.enabled,
        entry.baseline,
        entry.protected,
        entry.proposed,
        entry.evidence,
      ]),
    ).toEqual(Array.from({ length: 5 }, () => [false, 0, 0, 0, "disabled"]));
    expect(plan.unallocatedFraction).toBe(1);
    expect(plan.discretionaryFraction).toBe(0);
    await expect(
      previewAllocation(store, { basis: plan.basis, edits: [{ activity: "review", fraction: 1 }] }),
    ).rejects.toThrow(/disabled/);
    const saved = await saveAllocation(
      store,
      { name: "unallocated", reason: "inspect disabled authority", basis: plan.basis, edits: [] },
      "owner",
    );
    expect(saved.plan.unallocatedFraction).toBe(1);
    expect(await allocationVersion(store)).toEqual(saved);
    expect(await authority(store)).toEqual(before);
  }
});

test("large old history stays summarized rather than copied into a quiet saved version", async () => {
  const store = await fixture();
  await store.db.run(
    `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1200)
    INSERT INTO records(id,kind,root_id,actor_kind,actor_id,title,created_at,payload)
    SELECT 'old_'||i,'proposal','old_'||i,'run','missing','old',?,'{}' FROM n`,
    [stamp(NOW - ALLOCATION_LOOKBACK_MS - 1)],
  );
  await store.db.run(
    `INSERT INTO dispositions(id,record_id,seq,disposition,actor_id,recorded_at)
    SELECT id||'_ruling',id,1,'accept',?,? FROM records`,
    ["operator".repeat(600), stamp(NOW - ALLOCATION_LOOKBACK_MS - 1)],
  );
  const plan = await preview(store);
  expect(plan.snapshot.feedback).toEqual([]);
  expect(plan.snapshot.coverage).toMatchObject({
    historicalRulings: 1200,
    candidateRoots: 0,
    provenanceOmitted: 0,
  });
  expect(plan.slices.map((entry) => entry.proposed)).toEqual([0.4, 0.2, 0.4, 0, 0]);
  expect(Buffer.byteLength(JSON.stringify(plan))).toBeLessThan(16 * 1024);
  const saved = await saveAllocation(
    store,
    { name: "quiet", reason: "bounded history", basis: plan.basis, edits: [] },
    "owner",
  );
  expect(replayAllocation(saved.plan.snapshot)).toEqual(plan);
});

test("complete window totals drive replay even when provenance examples are bounded or oversized", async () => {
  const store = await fixture();
  await sample(store, "review", "accept", 80);
  await sample(store, "challenge", "reject", 80);
  await proposal(store, "oversized-actor", "review");
  await ruling(store, "oversized-actor", "accept", stamp(NOW), 1, "owner".repeat(1000));
  const plan = await preview(store);
  expect(slice(plan, "review")).toMatchObject({ accepted: 81, rejected: 0, sample: 81 });
  expect(slice(plan, "challenge")).toMatchObject({ accepted: 0, rejected: 80, sample: 80 });
  expect(plan.snapshot.coverage).toMatchObject({
    complete: true,
    candidateRoots: 161,
    windowRulings: 161,
    provenanceReturned: ALLOCATION_MAX_PROVENANCE,
    provenanceOmitted: 161 - ALLOCATION_MAX_PROVENANCE,
    exclusions: [{ reason: "counted", roots: 161 }],
  });
  expect(plan.snapshot.feedback).toHaveLength(ALLOCATION_MAX_PROVENANCE);
  expect(slice(plan, "review").proposed).toBeGreaterThan(0.4);
  expect(Buffer.byteLength(JSON.stringify(plan))).toBeLessThan(ALLOCATION_MAX_PLAN_BYTES);
  const saved = await saveAllocation(
    store,
    {
      name: "populated",
      reason: "complete arithmetic, bounded citations",
      basis: plan.basis,
      edits: [],
    },
    "owner",
  );
  expect(replayAllocation(saved.plan.snapshot)).toEqual(plan);
  expect((await allocationVersion(store))?.plan.slices).toEqual(plan.slices);
});

test("mutated run attribution invalidates a bounded basis without carrying a producer inventory", async () => {
  const store = await fixture();
  await proposal(store, "attributed", "challenge");
  await ruling(store, "attributed", "accept");
  const plan = await preview(store);
  await store.db.run("UPDATE runs SET preparation=? WHERE id='run_attributed'", [
    JSON.stringify({ analysis: { stage: "synthesize" } }),
  ]);
  await expect(
    saveAllocation(
      store,
      { name: "stale", reason: "must refuse moved source", basis: plan.basis, edits: [] },
      "owner",
    ),
  ).rejects.toThrow(/stale/);
  expect(slice(await preview(store), "synthesize").accepted).toBe(1);
  expect(await allocationVersion(store)).toBeNull();
});

test("oversized policy snapshots and full saved-version capacity refuse before appending", async () => {
  const store = await fixture();
  await store.db.run("UPDATE policies SET payload=?", [
    JSON.stringify({ ...DEFAULT_POLICY, version: "x".repeat(ALLOCATION_MAX_POLICY_BYTES) }),
  ]);
  await expect(preview(store)).rejects.toThrow(/bounded allocation snapshot/);
  const bounded = await fixture();
  await bounded.db.run(
    `INSERT INTO allocation_plans(version,seq,actor_id,reason,policy_version,payload,recorded_at)
    VALUES('full',1,'owner','synthetic capacity fixture','baseline',CAST(zeroblob(?) AS TEXT),?)`,
    [ALLOCATION_MAX_SAVED_BYTES, stamp(NOW)],
  );
  const plan = await preview(bounded);
  await expect(
    saveAllocation(
      bounded,
      {
        name: "overflow",
        reason: "must not grow past reserved capacity",
        basis: plan.basis,
        edits: [],
      },
      "owner",
    ),
  ).rejects.toThrow(/capacity is full/);
  expect(await bounded.db.query("SELECT version FROM allocation_plans ORDER BY seq")).toEqual([
    { version: "full" },
  ]);
});
