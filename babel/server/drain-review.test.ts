import { afterEach, expect, test } from "bun:test";
import { ENGINE_REFUSALS, ROLES } from "../contract.ts";
import { coordinator, DEFAULT_POLICY, PolicySchema } from "../store/coordinator.ts";
import {
  allocationStatus,
  closeDrain,
  insertDrain,
  readDrain,
  reserveDirectLaunch,
} from "../store/drains.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import { drainIdentity } from "./drain.ts";
import { reconcileDrainReview, startDrainReview, stopDrainReview } from "./drain-review.ts";
import { settleReviewSession, type PendingRun } from "./conductor.ts";
import { reviewPreparation } from "./engine/review.ts";
import type { CodeEngine, CodeJob, SessionRead, SessionRequest } from "./engine/session.ts";

const NOW = Date.parse("2026-09-29T12:00:00Z");
const PROFILE = { containerId: "review-fixture", expectedRevision: 1 };
const CHAIN = "principal:fixture";
const opened: TestStore[] = [];
afterEach(() => {
  for (const store of opened.splice(0)) store.close();
});

async function fixture(options: { batchSize?: number; concurrent?: number } = {}) {
  const store = await openTestStore(NOW);
  opened.push(store);
  const policy = PolicySchema.parse({
    ...DEFAULT_POLICY,
    version: "review-fixture",
    enabled: true,
    activityWeights: { ...DEFAULT_POLICY.activityWeights, review: 0.05, explore: 1 },
    perCycleCost: 1,
    dailyCost: 10,
    batchSize: options.batchSize ?? 4,
    concurrentPerMachine: 4,
    review: {
      machineId: "machine-fixture",
      profile: PROFILE,
      roleRecipes: Object.fromEntries(ROLES.map((role) => [role, "review-fixture"])),
      stageRecipes: { explore: "review-fixture" },
      recipes: [{ id: "review-fixture", version: 1, body: "Judge the supplied record." }],
    },
  });
  await insert(store.db, "policies", {
    version: policy.version,
    seq: 1,
    actor_id: "fixture",
    reason: "synthetic review authority",
    payload: JSON.stringify(policy),
    recorded_at: new Date(NOW).toISOString(),
  });
  for (const id of ["hyp_00000001", "hyp_00000002"])
    await insert(store.db, "records", {
      id,
      kind: "hypothesis",
      root_id: id,
      seq: 0,
      actor_kind: "run",
      actor_id: "run_seed",
      title: "A synthetic review subject",
      created_at: new Date(NOW - 86_400_000).toISOString(),
      payload: JSON.stringify({ statement: "A synthetic claim", votes: { support: 900 } }),
    });
  await insertDrain(store.store, {
    id: "drain_review_fixture",
    machineId: "machine-fixture",
    preset: "read-whats-new",
    profile: {
      profile: PROFILE,
      model: "fixture/model",
      thinking: "off",
      accounts: [],
      resolved: true,
    },
    knobs: {
      recipes: [],
      allocation: { "review-backlog": 1 },
      inferenceLimits: { costMicros: 100_000 },
    },
    concurrent: options.concurrent ?? 2,
    target: { costMicros: 5_000_000, deadline: new Date(NOW + 3_600_000).toISOString() },
    startedBy: "fixture",
  });
  const initial = (await readDrain(store.store, "drain_review_fixture"))!;
  const identity = { ...drainIdentity(initial, 0, CHAIN), drainId: initial.id };
  expect(
    await reserveDirectLaunch(
      store.store,
      initial,
      { runId: identity.runId, jobId: identity.jobId, launchedAt: NOW, reserved: true },
      "review-backlog",
      100_000,
      4,
    ),
  ).toBe(true);
  const jobs = new Map<string, CodeJob>();
  const retired = new Set<string>();
  const requests: SessionRequest[] = [];
  const behaviour: {
    afterPost?: () => Promise<void>;
    beforePost?: () => Promise<void>;
    cancel: boolean;
    meter: boolean;
  } = { cancel: true, meter: true };
  const receipt = (job: CodeJob): SessionRead => ({
    job:
      job.state === "cancelled" && behaviour.meter
        ? {
            ...job,
            result: {
              jobId: job.jobId,
              requestDigest: "d".repeat(64),
              ownerId: "fixture",
              ownerGeneration: 1,
              state: job.state,
              exitCode: null,
              reason: "synthetic cancellation",
              startedAt: NOW,
              finishedAt: NOW + 1000,
              usage: {
                elapsedMs: 1000,
                memoryBytes: 0,
                processes: 1,
                outputBytes: 0,
                inference: {
                  calls: 1,
                  inputTokens: 100,
                  outputTokens: 50,
                  cachedInputTokens: 0,
                  costMicros: 200_000,
                },
              },
              limits: { timeoutMs: 60_000, memoryBytes: 1024, processes: 1, outputBytes: 1024 },
              outputs: [],
            },
          }
        : job,
    session: null,
    silence: job.state === "cancelled" ? "omp_session_cancelled" : "omp_session_running",
  });
  const engine: CodeEngine = {
    profiles: async () => ({ ok: true, value: [] }),
    checkProfile: async () => ({ ok: true, value: null }),
    runSession: async (request) => {
      requests.push(request);
      const key = request.postingKey!;
      const known = jobs.get(key);
      if (known !== undefined) return { ok: true, value: known };
      if (request.adoptOnly === true || retired.has(key)) {
        retired.add(key);
        return { ok: false, code: ENGINE_REFUSALS.postingUnknown, refused: "retired unused key" };
      }
      await behaviour.beforePost?.();
      const job: CodeJob = {
        jobId: `omp_${key}`,
        machineId: request.machineId,
        operationId: "atyrode.omp.session",
        pluginId: "atyrode.omp",
        state: "started",
      };
      jobs.set(key, job);
      await behaviour.afterPost?.();
      return { ok: true, value: job };
    },
    cancelSession: async ({ jobId }) => {
      const entry = [...jobs.entries()].find(([, job]) => job.jobId === jobId)!;
      if (!behaviour.cancel)
        return { ok: false, code: "engine_forbidden", refused: "synthetic cancellation denied" };
      const cancelled = { ...entry[1], state: "cancelled" as const };
      jobs.set(entry[0], cancelled);
      return { ok: true, value: cancelled };
    },
    readSession: async ({ jobId }) => ({
      ok: true,
      value: receipt([...jobs.values()].find((job) => job.jobId === jobId)!),
    }),
  };
  const deps = {
    store: store.store,
    admission: async () => ({ limit: 4, activeJobIds: [] }),
    coordinator: coordinator(store.store, () => store.store.now(), 16),
    engine,
    chain: CHAIN,
    now: () => store.store.now(),
  };
  return {
    store,
    row: (await readDrain(store.store, initial.id))!,
    identity,
    deps,
    jobs,
    requests,
    behaviour,
    policy,
    receipt,
  };
}

test("review-only draw and concurrent ordinal replay buy one blinded review and one shared reservation", async () => {
  const f = await fixture();
  const before = await f.store.db.query(`SELECT version,payload FROM policies`);
  await Promise.all([
    startDrainReview(f.deps, f.row, f.identity),
    startDrainReview(f.deps, f.row, f.identity),
  ]);
  const [claim] = await f.store.db.query<{ role: string; job_id: string; reserved_cost: number }>(
    `SELECT role,job_id,reserved_cost FROM claims WHERE finished_at IS NULL`,
  );
  expect(claim!.role.startsWith("analysis:")).toBe(false);
  expect(claim!.job_id).toBe(`omp_${f.identity.runId}`);
  expect(f.jobs.size).toBe(1);
  expect(f.requests[0]!.prompt).not.toContain('"support": 900');
  expect(f.requests[0]!.inferenceLimits?.costMicros).toBe(100_000);
  expect(await f.store.db.query(`SELECT version,payload FROM policies`)).toEqual(before);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toHaveLength(1);
  expect(
    (await allocationStatus(f.store.store, f.row))[0]!.reservedCostMicros,
  ).toBeGreaterThanOrEqual(Math.ceil(claim!.reserved_cost * 1_000_000));
});

test("claim publication rolls back with its parent, leaving the ordinal recoverable", async () => {
  const f = await fixture();
  await f.store.db.run(
    `CREATE TRIGGER refuse_review_parent BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'synthetic parent write interrupted'); END`,
  );
  await expect(startDrainReview(f.deps, f.row, f.identity)).rejects.toThrow(
    "synthetic parent write interrupted",
  );
  expect(await f.store.db.query(`SELECT id FROM claims`)).toEqual([]);
  expect(f.jobs.size).toBe(0);
  await f.store.db.run(`DROP TRIGGER refuse_review_parent`);
  await startDrainReview(f.deps, f.row, f.identity);
  expect(f.jobs.size).toBe(1);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toHaveLength(1);
});

test("lost acknowledgement survives restart and rejects a different account chain", async () => {
  const f = await fixture();
  f.behaviour.afterPost = async () => {
    throw new Error("synthetic lost acknowledgement");
  };
  expect(await startDrainReview(f.deps, f.row, f.identity)).toMatchObject({ pending: true });
  const original = f.requests[0];
  const count = f.requests.length;
  expect(
    await startDrainReview({ ...f.deps, chain: "principal:other" }, f.row, f.identity),
  ).toMatchObject({ pending: true });
  expect(f.requests.length).toBe(count);
  const restarted = {
    ...f.deps,
    coordinator: coordinator(f.store.store, () => f.store.store.now(), 16),
  };
  expect(await startDrainReview(restarted, f.row, f.identity)).toMatchObject({
    jobId: `omp_${f.identity.runId}`,
  });
  expect(f.requests.at(-1)).toEqual(original);
  expect(f.jobs.size).toBe(1);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toHaveLength(1);
});

test.each(["stop", "target", "deadline", "lease"])(
  "a late acknowledgement after %s is cancelled, charged once and grants no review result authority",
  async (ending) => {
    const f = await fixture();
    f.behaviour.afterPost = async () => {
      if (ending === "stop") await closeDrain(f.store.store, f.row.id, "stopped", "synthetic stop");
      else if (ending === "target")
        await f.store.db.run(
          `UPDATE drains SET spent=json_set(spent,'$.costMicros',5000000) WHERE id=?`,
          [f.row.id],
        );
      else f.store.at(NOW + (ending === "deadline" ? 3_600_001 : f.policy.leaseSeconds * 1000 + 1));
    };
    await startDrainReview(f.deps, f.row, f.identity);
    expect([...f.jobs.values()][0]!.state).toBe("cancelled");
    expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
    const [claim] = await f.store.db.query<{ actual_cost: number }>(
      `SELECT actual_cost FROM claims WHERE run_id=?`,
      [f.row.id],
    );
    expect(claim!.actual_cost).toBe(0.2);
    expect(await f.store.db.query(`SELECT id FROM assessments`)).toEqual([]);
    await stopDrainReview(f.deps, f.row, f.identity.runId, "retry");
    expect((await f.deps.coordinator.spend()).total).toBe(0.2);
  },
);

test("stop holds an unconfirmed cancellation, then releases the exact claim on terminal evidence", async () => {
  const f = await fixture();
  await startDrainReview(f.deps, f.row, f.identity);
  await closeDrain(f.store.store, f.row.id, "stopped", "synthetic stop");
  f.behaviour.cancel = false;
  expect((await stopDrainReview(f.deps, f.row, f.identity.runId, "stop")).cancelled).toBe(false);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toHaveLength(1);
  expect((await readDrain(f.store.store, f.row.id))!.live.map((job) => job.runId)).toEqual([
    f.identity.runId,
  ]);
  f.behaviour.cancel = true;
  expect((await stopDrainReview(f.deps, f.row, f.identity.runId, "stop")).cancelled).toBe(true);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
  expect((await f.deps.coordinator.spend()).total).toBe(0.2);
});

test("unused keyed intent retires at zero, but a terminal job with no meter keeps its reservation charge", async () => {
  const f = await fixture();
  f.behaviour.beforePost = async () => {
    throw new Error("synthetic transport failure before Code posting");
  };
  await startDrainReview(f.deps, f.row, f.identity);
  await closeDrain(f.store.store, f.row.id, "stopped", "stop");
  await stopDrainReview(f.deps, f.row, f.identity.runId, "stop");
  expect(f.jobs.size).toBe(0);
  expect((await f.deps.coordinator.spend()).total).toBe(0);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
  const g = await fixture();
  await startDrainReview(g.deps, g.row, g.identity);
  const reserved = (await g.deps.coordinator.spend()).total;
  g.behaviour.meter = false;
  await closeDrain(g.store.store, g.row.id, "stopped", "stop");
  await stopDrainReview(g.deps, g.row, g.identity.runId, "stop");
  expect((await g.deps.coordinator.spend()).total).toBe(reserved);
  expect((await allocationStatus(g.store.store, g.row))[0]!.gap).toBe("missing-price");
});

test("terminal receipt replay settles a claim after interruption without rebuying work", async () => {
  const f = await fixture();
  await startDrainReview(f.deps, f.row, f.identity);
  await f.store.db.run(`UPDATE runs SET closure='completed',payload=? WHERE id=?`, [
    JSON.stringify({ inference: { costMicros: 345_000 } }),
    f.identity.runId,
  ]);
  await Promise.all([
    reconcileDrainReview(f.deps, f.identity.runId),
    reconcileDrainReview(f.deps, f.identity.runId),
  ]);
  expect((await f.deps.coordinator.spend()).total).toBe(0.345);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
  expect(f.jobs.size).toBe(1);
});

test("two settlement wakes retain the first receipt and finish a shared review claim once", async () => {
  const f = await fixture();
  await startDrainReview(f.deps, f.row, f.identity);
  const [run] = await f.store.db.query<PendingRun>(`SELECT * FROM runs WHERE id=?`, [
    f.identity.runId,
  ]);
  const preparation = reviewPreparation(JSON.parse(run!.preparation!))!;
  const read: SessionRead = {
    job: { ...[...f.jobs.values()][0]!, state: "exited" },
    silence: null,
    session: {
      sessionId: "fixture-session",
      sessionPath: "/synthetic/session.jsonl",
      model: "fixture/model",
      finalMessage: '```json\n{"skip":"No additional evidence in this synthetic fixture"}\n```',
      usage: { input: 100, output: 50, cacheRead: 0, cacheWrite: 0, cost: 0.2 },
      exitCode: 0,
      failure: null,
    },
  };
  const settle = () =>
    settleReviewSession(f.deps, NOW, run!, read, "stopped", preparation, [], [], [], {
      paid: new Map(),
      free: new Map(),
    });
  await Promise.all([settle(), settle()]);
  const [closed] = await f.store.db.query<{ closure: string; payload: string }>(
    `SELECT closure,payload FROM runs WHERE id=?`,
    [f.identity.runId],
  );
  expect(closed!.closure).toBe("skipped");
  expect((await f.deps.coordinator.spend()).total).toBe(0.2);
  expect(await f.store.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
  await settle();
  expect(
    await f.store.db.query(`SELECT closure,payload FROM runs WHERE id=?`, [f.identity.runId]),
  ).toEqual([closed!]);
});

test("stop winning the claim-publication race leaves no ghost claim or purchased review", async () => {
  const f = await fixture();
  const original = f.deps.coordinator;
  const stopped = {
    ...f.deps,
    coordinator: {
      ...original,
      claim: async (request: Parameters<typeof original.claim>[0]) => {
        await closeDrain(f.store.store, f.row.id, "stopped", "stop won publication");
        return await original.claim(request);
      },
    },
  };
  expect(await startDrainReview(stopped, f.row, f.identity)).toHaveProperty("refused");
  expect(await f.store.db.query(`SELECT id FROM claims`)).toEqual([]);
  expect(await f.store.db.query(`SELECT id FROM runs`)).toEqual([]);
  expect(f.jobs.size).toBe(0);
});

test.each([1, 4])(
  "independent review grant transactions share the durable ceiling with batch size %s",
  async (batchSize) => {
    const f = await fixture({ batchSize, concurrent: 4 });
    const second = { ...drainIdentity(f.row, 1, CHAIN), drainId: f.row.id };
    expect(
      await reserveDirectLaunch(
        f.store.store,
        f.row,
        { runId: second.runId, jobId: second.jobId, launchedAt: NOW, reserved: true },
        "review-backlog",
        100_000,
        4,
      ),
    ).toBe(true);
    const identities = [f.identity, second];
    // Real draws reserve distinct candidate handouts, so this race tests the shared budget,
    // not merely the assignment's uniqueness constraint.
    const draws: Extract<
      Awaited<ReturnType<typeof f.deps.coordinator.draw>>,
      { outcome: "assignment" }
    >[] = [];
    for (const identity of identities) {
      const drawn = await f.deps.coordinator.draw({
        runId: identity.runId,
        machines: [f.row.machineId],
        only: "review",
        now: NOW,
      });
      if (drawn.outcome !== "assignment") throw new Error(JSON.stringify(drawn));
      draws.push(drawn);
    }
    expect(new Set(draws.map((drawn) => drawn.assignment.id)).size).toBe(2);
    let arrivals = 0;
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const gatedStore = {
      ...f.store.store,
      db: {
        ...f.store.db,
        batch: async (statements: Parameters<typeof f.store.db.batch>[0]) => {
          // Both coordinators have already read the empty spend ledger when they reach
          // their publication transaction. Only the atomic SQL ceiling can decide the race.
          arrivals += 1;
          if (arrivals === 2) release();
          await gate;
          return await f.store.db.batch(statements);
        },
      },
    };
    const results = await Promise.all(
      identities.map((identity, index) =>
        startDrainReview(
          {
            ...f.deps,
            coordinator: {
              ...coordinator(gatedStore, () => NOW, 16),
              draw: async () => draws[index]!,
            },
          },
          f.row,
          identity,
        ),
      ),
    );
    const admitted = batchSize === 1 ? 1 : 2;
    expect(results.filter((result) => !("refused" in result))).toHaveLength(admitted);
    expect(f.jobs.size).toBe(admitted);
    expect((await f.deps.coordinator.spend()).byRun[f.row.id]).toBe(admitted / batchSize);
    const held = await f.store.db.query<{ run_id: string; job_id: string }>(
      `SELECT run_id,job_id FROM claims WHERE finished_at IS NULL`,
    );
    expect(held).toHaveLength(admitted);
    expect(held.every((claim) => claim.run_id === f.row.id)).toBe(true);
    expect(new Set(held.map((claim) => claim.job_id)).size).toBe(admitted);
    const restarted = {
      ...f.deps,
      coordinator: coordinator(f.store.store, () => NOW, 16),
    };
    if (batchSize === 1) {
      const refusedIndex = results.findIndex((result) => "refused" in result);
      expect(await startDrainReview(restarted, f.row, identities[refusedIndex]!)).toMatchObject({
        code: "per-cycle",
      });
      expect(f.jobs.size).toBe(1);
    } else {
      // A shared budget identity must not merge parent/posting identities or let stopping
      // one ordinal retire a sibling's fenced claim.
      expect(new Set(f.requests.map((request) => request.postingKey))).toEqual(
        new Set(identities.map((identity) => identity.runId)),
      );
      expect(
        (await stopDrainReview(restarted, f.row, f.identity.runId, "stop one review")).cancelled,
      ).toBe(true);
      expect(await f.store.db.query(`SELECT job_id FROM claims WHERE finished_at IS NULL`)).toEqual(
        [{ job_id: `omp_${second.runId}` }],
      );
      expect((await restarted.coordinator.spend()).byRun[f.row.id]).toBe(0.45);
    }
  },
);

test("a newly excluded review source is not disclosed by retry or adopt-only retirement and its unknown reservation stays held", async () => {
  const f = await fixture();
  for (const id of ["hyp_00000001", "hyp_00000002"])
    await insert(f.store.db, "edges", {
      id: `edg_privacy_${id}`, kind: "cites", from_kind: "hypothesis", from_id: id,
      to_kind: "session", to_id: "omp/direct-review-private-synthetic",
      actor_kind: "run", actor_id: "run_seed", created_at: new Date(NOW).toISOString(),
    });
  f.behaviour.afterPost = async () => { throw new Error("synthetic lost acknowledgement"); };
  expect(await startDrainReview(f.deps, f.row, f.identity)).toMatchObject({ pending: true });
  expect(f.requests).toHaveLength(1);
  await insert(f.store.db, "session_exclusions", {
    selector: "omp/direct-review-private-synthetic", actor_id: "synthetic-owner",
    recorded_at: new Date(NOW).toISOString(),
  });
  expect(await startDrainReview(f.deps, f.row, f.identity)).toMatchObject({
    refused: expect.stringContaining("session_excluded"), pending: true,
  });
  expect((await stopDrainReview(f.deps, f.row, f.identity.runId, "synthetic stop")).cancelled).toBe(false);
  expect(f.requests).toHaveLength(1);
  expect(await f.store.db.query("SELECT closure,job_id FROM runs WHERE id=?", [f.identity.runId])).toEqual([
    { closure: null, job_id: null },
  ]);
  expect(await f.store.db.query("SELECT finished_at,actual_cost FROM claims WHERE finished_at IS NULL")).toEqual([
    { finished_at: null, actual_cost: null },
  ]);
});
