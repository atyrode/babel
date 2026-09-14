/*
  THE DRAIN, HELD TO WHAT IT DOES TO THE WORLD (#258).

  Every test dispatches the way the kit does — parse the arguments against the action's own input,
  run the handler, parse what it produced against the action's own result — and then asks the
  STORE and the FLEET what happened, because that is what a drain is: N jobs posted to one
  machine, one row that remembers them, and an overlay with a TTL. The fleet is fake and the store
  is real, which is the right way round: a job request is a shape this file can pin exactly, and
  the drain row is SQL under every CHECK the schema declares.

  The launch path is the REAL `launchMachinery` over the same store, so a job this drain posts is
  the job the operator's own button posts — if the two ever diverged these tests would still pass
  against a fake and prove nothing.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, OPERATIONS, PRESET_OPERATIONS } from "../contract.ts";
import type {
  JobLaunch,
  JobRef,
  JobRunState,
  MachineReadiness,
  Recipe,
  RunPlan,
} from "../server/conductor.ts";
import { drainTick, type DrainDeps } from "../server/drain.ts";
import type { BabelJobs } from "../server/plan.ts";
import { coordinator } from "../store/coordinator.ts";
import { readDrain } from "../store/drains.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import type { Door } from "./door.ts";
import { drainDoors } from "./drain.ts";
import { launchMachinery } from "./launch.ts";

const NOW = Date.UTC(2026, 8, 14, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";

const RECIPE: Recipe = {
  id: "code-health-comprehensibility",
  version: 3,
  title: "Code health",
  body: "What in this code is harder to understand than it needs to be?",
};

const LIMITS = {
  timeoutMs: 3_600_000,
  memoryBytes: 2_147_483_648,
  processes: 64,
  outputBytes: 16_777_216,
};

/** The account a drain names, in the contract's own shape (#267). */
const SESSION = {
  model: "anthropic/claude-sonnet-4-5",
  account: {
    provider: "anthropic",
    scope: "subscription",
    credentialId: "41",
    identityKey: "the-drain-account",
  },
};

const PLAN: RunPlan = {
  engine: { binary: "/runtime/bin/omp", args: [] },
  session: SESSION,
  caps: { perRunUsd: 0.0625, toolCalls: 40, idleMs: 120_000, handshakeMs: 30_000 },
  recipes: {},
  metered: { [OPERATIONS.explore]: true },
  requireContainment: true,
  limits: LIMITS,
};

const READY: MachineReadiness = {
  connected: true,
  operations: {
    [OPERATIONS.scan]: { ready: true, reason: null },
    [OPERATIONS.explore]: { ready: true, reason: null },
    [OPERATIONS.evaluate]: { ready: true, reason: null },
  },
  installation: { revision: "rev-7", artifactSha256: "a".repeat(64), enabled: true, ready: true },
};

class Fleet implements BabelJobs {
  readonly executed: JobLaunch[] = [];
  readonly cancelled: JobRef[] = [];
  refusal = "";
  /** Set to refuse cancellation the way a credential without `jobs:cancel` does. */
  cancelRefusal = "";

  describe(): MachineReadiness {
    return READY;
  }

  execute(args: JobLaunch): JobRunState {
    if (this.refusal !== "") throw new Error(this.refusal);
    this.executed.push(args);
    return {
      jobId: args.jobId,
      machineId: args.machineId,
      operationId: args.operationId,
      state: "queued",
      result: null,
    };
  }

  cancel(node: JobRef): void {
    if (this.cancelRefusal !== "") throw new Error(this.cancelRefusal);
    this.cancelled.push(node);
  }

  status(): JobRunState {
    throw new Error("a drain never reads a job back: the conductor settles them");
  }

  listRuns(): { runs: readonly { job: JobRunState | null }[] } {
    throw new Error("a drain never lists runs");
  }

  follow(): never {
    throw new Error("a drain never follows a job");
  }

  output(): { data: string; eof: boolean } {
    throw new Error("a drain never reads an output");
  }

  schedules(): readonly [] {
    return [];
  }

  schedule(): never {
    throw new Error("a drain never schedules");
  }

  disableSchedule(): never {
    throw new Error("a drain never unschedules");
  }
}

let harness: TestStore;
let fleet: Fleet;
let doors: readonly Door[];
let deps: DrainDeps;

const ctx = { principal: { id: "operator" }, auth: { isRoot: true }, emit: () => {} } as unknown as GuestCtx;

async function dispatch(name: string, args: unknown): Promise<Record<string, unknown>> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success) {
    return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
  }
  const produced = await found.handler(ctx, parsed.data as never);
  if (typeof produced === "object" && produced !== null && "refused" in produced) {
    return produced as Record<string, unknown>;
  }
  const result = found.action.result.safeParse(produced);
  if (!result.success) {
    throw new Error(`${name} produced a result outside its schema: ${result.error.message}`);
  }
  return result.data as Record<string, unknown>;
}

/** A drain as the panel starts one: the request plus the OPERATION NODE it is authorized at. */
async function start(over: Record<string, unknown> = {}): Promise<Record<string, unknown>> {
  const preset = typeof over["preset"] === "string" ? over["preset"] : "read-whats-new";
  return await dispatch(ACTIONS.drainStart, {
    machineId: MACHINE,
    preset,
    session: SESSION,
    concurrent: 2,
    reason: "the 7-day window resets at 13:00Z",
    target: { costMicros: 1_000_000 },
    ...over,
    operation: {
      kind: "operation",
      machineId: MACHINE,
      operationId: PRESET_OPERATIONS[preset as keyof typeof PRESET_OPERATIONS],
    },
  });
}

/** A stop as the panel's own button posts one: the drain, and its jobs' shared operation node. */
async function halt(drainId: string, reason = ""): Promise<Record<string, unknown>> {
  return await dispatch(ACTIONS.drainStop, {
    drainId,
    reason,
    operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.explore },
  });
}

/** One drain's status, through the door the panel polls. */
async function statusOf(drainId: string): Promise<Record<string, unknown>> {
  const answer = await dispatch(ACTIONS.drainStatus, { drainId });
  const drains = answer["drains"];
  if (!Array.isArray(drains) || drains[0] === undefined) throw new Error(`no status for ${drainId}`);
  return drains[0] as Record<string, unknown>;
}

/** A job of this drain settled, the way the conductor writes one: the meter's own totals. */
async function settleJob(
  runId: string,
  over: { readonly costMicros?: number; readonly outputTokens?: number; readonly reason?: string } = {},
): Promise<void> {
  await harness.db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ?, cost_usd = ?, tokens = ?, payload = ?
      WHERE id = ?`,
    [
      stamp(NOW + 60_000),
      (over.costMicros ?? 400_000) / 1_000_000,
      over.outputTokens ?? 1_000,
      JSON.stringify({
        closure: "completed",
        ...(over.reason === undefined ? {} : { reason: over.reason }),
        inference: {
          calls: 3,
          inputTokens: 20_000,
          outputTokens: over.outputTokens ?? 1_000,
          cachedInputTokens: 0,
          costMicros: over.costMicros ?? 400_000,
        },
      }),
      runId,
    ],
  );
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  fleet = new Fleet();
  const { db, store } = harness;
  // An enabled policy with a lease that can cover a fan of four, as `setBudget` demands.
  await insert(db, "policies", {
    version: "p1",
    seq: 1,
    actor_id: "operator",
    reason: "turning it on",
    payload: JSON.stringify({
      enabled: true,
      perCycleCost: 0.25,
      batchSize: 1,
      dailyCost: 2,
      leaseSeconds: 900,
    }),
    recorded_at: stamp(NOW - HOUR),
  });
  for (const n of [1, 2, 3, 4, 5]) {
    await insert(db, "sessions", {
      selector: `omp/s${String(n)}`,
      host: MACHINE,
      harness: "omp",
      source_id: `s${String(n)}`,
      title: `session ${String(n)}`,
      content_digest: `d${String(n)}`,
      seen_at: stamp(NOW - 2 * HOUR),
    });
  }
  const coordinated = coordinator(store, () => store.now(), 16);
  const machinery = launchMachinery(store, {
    coordinator: coordinated,
    cookbook: { [RECIPE.id]: RECIPE },
    jobs: () => fleet,
    machines: () => ({ repository: () => ({ ok: false, reason: "this test enrolls no machine" }) }),
    // Only the preview reads a machine's service policy, and a drain previews nothing: a slice
    // that says so is more honest than a priced fake nothing in this file ever consults.
    services: () => ({
      policy: () => ({ ok: false as const, reason: "a drain reads no service configuration" }),
    }),
    plan: () => PLAN,
    cycle: () => ({ tick: () => Promise.reject(new Error("a drain never draws")) }),
    now: () => store.now(),
  });
  deps = {
    store,
    coordinator: coordinated,
    launch: machinery,
    jobs: fleet,
    plan: () => PLAN,
    now: () => store.now(),
  };
  doors = drainDoors(store, {
    coordinator: coordinated,
    deps: () => deps,
    concurrentJobs: 16,
    now: () => store.now(),
  });
});

afterEach(() => {
  harness.close();
});

test("the roster is a governed start, a dry read and a governed stop, each at a node", () => {
  expect(doors.map((entry) => entry.action.name)).toEqual([
    ACTIONS.drainStart,
    ACTIONS.drainStatus,
    ACTIONS.drainStop,
  ]);
  const [begin, read, stop] = doors as readonly Door[];

  // Posting jobs is `machines:run` at the operation node, paired with its requirement: a
  // governed cap with no requirement is refused outright by the dispatcher.
  expect(begin?.action.caps).toEqual(["machines:run"]);
  expect(begin?.action.requirements).toEqual([{ cap: "machines:run", target: ["operation"] }]);
  expect(begin?.action.delegates).toEqual(["jobs:read", "locations:read", "locations:write"]);

  // The dry read asks no machine anything, so it carries no governed capability and no target —
  // the panel polls it every five seconds while the operator watches.
  expect(read?.action.caps).toEqual(["containers:read"]);
  expect(read?.action.requirements).toBeUndefined();

  // A drain holds several jobs and a requirement resolves to exactly one node, so the stop asks
  // for `jobs:cancel` at the OPERATION they share rather than at one of them.
  expect(stop?.action.caps).toEqual(["jobs:cancel"]);
  expect(stop?.action.requirements).toEqual([{ cap: "jobs:cancel", target: ["operation"] }]);
});

test("a start posts the whole fan, sets the overlay to it, and names the account it spends", async () => {
  const answer = await start({ concurrent: 3 });
  expect(answer["launched"]).toBe(3);
  expect(answer["account"]).toBe("the-drain-account");
  expect(answer["model"]).toBe("anthropic/claude-sonnet-4-5");
  const drainId = String(answer["drainId"]);

  // THREE JOBS, of the preset's own operation, with ids derived from the drain so a retry cannot
  // double-post: the hub is idempotent on the job id and so is the run row.
  expect(fleet.executed).toHaveLength(3);
  expect(fleet.executed.map((job) => job.jobId)).toEqual([
    `job_${drainId}_0`,
    `job_${drainId}_1`,
    `job_${drainId}_2`,
  ]);
  expect(new Set(fleet.executed.map((job) => job.operationId))).toEqual(
    new Set([OPERATIONS.explore]),
  );

  // THE OVERLAY IS THE FAN, and the standing `policies` row is untouched (#260): the bound moves
  // to three and the cycle's allowance moves with it, so what ONE run may spend is unchanged.
  const overlay = await harness.db.query<{
    id: string;
    concurrent_per_machine: number | bigint;
    per_cycle_cost: number;
    expires_at: string;
    reason: string;
  }>(`SELECT id, concurrent_per_machine, per_cycle_cost, expires_at, reason FROM budgets`);
  expect(overlay).toHaveLength(1);
  expect(Number(overlay[0]?.concurrent_per_machine)).toBe(3);
  // THE INVARIANT, not a literal: the standing policy grants 0.25 a cycle over a batch of one,
  // so one run may spend 0.25; after the overlay the batch is three and the cycle's allowance is
  // three times as much, so one run may still spend exactly 0.25. A drain changes how many runs
  // happen at once and never what one run is allowed.
  expect(overlay[0]?.per_cycle_cost).toBeCloseTo(0.25 * 3, 6);
  expect((overlay[0]?.per_cycle_cost ?? 0) / 3).toBeCloseTo(0.25, 6);
  expect(overlay[0]?.reason).toBe("the 7-day window resets at 13:00Z");
  expect(answer["budgetId"]).toBe(overlay[0]?.id);
  expect(await harness.db.query(`SELECT version FROM policies`)).toHaveLength(1);

  // The row remembers what it must relaunch with: the account, the fan, and the preset's knobs.
  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("running");
  expect(row?.live).toHaveLength(3);
  expect(row?.jobsLaunched).toBe(3);
  expect(row?.session.account.identityKey).toBe("the-drain-account");
});

test("a drain without a target is refused, and so is a second drain on the same machine", async () => {
  expect(String((await start({ target: {} }))["refused"])).toMatch(/a drain needs a target/);
  // A deadline that has already gone is not a deadline.
  expect(
    String((await start({ target: { deadline: new Date(NOW - 1000).toISOString() } }))["refused"]),
  ).toMatch(/already passed/);
  // `keep-going` reaches no model, so a cost target on it could never be met.
  expect(String((await start({ preset: "keep-going" }))["refused"])).toMatch(/give this drain a deadline/);

  expect((await start())["launched"]).toBe(2);
  expect(String((await start())["refused"])).toMatch(/is already draining under drn_/);
});

test("a fan above the machine's ceiling is refused by name rather than posted and rejected", async () => {
  // `concurrentJobs` is 16 here, and a bound of 16 needs a lease of 320s; the policy grants 900,
  // so the refusal has to come from the manifest's ceiling rather than from the lease.
  doors = drainDoors(harness.store, {
    coordinator: coordinator(harness.store, () => harness.store.now(), 4),
    deps: () => deps,
    concurrentJobs: 4,
    now: () => harness.store.now(),
  });
  const refused = String((await start({ concurrent: 8 }))["refused"]);
  expect(refused).toMatch(/fan of 8 cannot be admitted/);
  expect(refused).toMatch(/4 jobs a machine runs at once/);
  expect(fleet.executed).toHaveLength(0);
  expect(await harness.db.query(`SELECT id FROM drains`)).toHaveLength(0);
});

test("a settlement relaunches: the fan is refilled and the spend is folded once", async () => {
  const drainId = String((await start({ concurrent: 2 }))["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 250_000, outputTokens: 900 });

  const [report] = await drainTick(deps);
  expect(report?.settled).toBe(1);
  expect(report?.launched).toBe(1);
  expect(report?.state).toBe("running");

  // The third job is the one the settlement made room for, and its id continues the sequence.
  expect(fleet.executed.map((job) => job.jobId)).toEqual([
    `job_${drainId}_0`,
    `job_${drainId}_1`,
    `job_${drainId}_2`,
  ]);
  const row = await readDrain(harness.store, drainId);
  expect(row?.live.map((job) => job.jobId)).toEqual([`job_${drainId}_1`, `job_${drainId}_2`]);
  expect(row?.spent.costMicros).toBe(250_000);
  expect(row?.spent.outputTokens).toBe(900);
  expect(row?.jobsSettled).toBe(1);
  expect(row?.closures).toEqual({ completed: 1 });

  // A SECOND TICK OVER THE SAME SETTLEMENT FOLDS NOTHING TWICE: the settled job is out of `live`,
  // so its spend is counted once however many wakes arrive.
  const [again] = await drainTick(deps);
  expect(again?.settled).toBe(0);
  expect((await readDrain(harness.store, drainId))?.spent.costMicros).toBe(250_000);
});

test("the target stops the drain, cancels what is in flight, and clears the overlay", async () => {
  const answer = await start({ concurrent: 2, target: { costMicros: 500_000 } });
  const drainId = String(answer["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 600_000 });

  const [report] = await drainTick(deps);
  expect(report?.state).toBe("target");
  expect(report?.reason).toMatch(/target of 500000 micro-dollars is met at 600000/);
  // NOTHING IS LAUNCHED BY THE TICK THAT ENDS IT: the fold happens before the decision, so the
  // job that met the target ends the drain instead of making room for one more.
  expect(report?.launched).toBe(0);
  expect(fleet.executed).toHaveLength(2);

  // The in-flight job is cancelled at its own job node…
  expect(fleet.cancelled).toEqual([
    { kind: "job", machineId: MACHINE, operationId: OPERATIONS.explore, jobId: `job_${drainId}_1` },
  ]);
  // …and the overlay is cleared, which is the part waiting could never undo (2026-09-13's
  // eval-policy-10 outlived its drain by ninety minutes).
  const cleared = await harness.db.query<{ cleared_at: string | null; cleared_reason: string }>(
    `SELECT cleared_at, cleared_reason FROM budgets WHERE id = ?`,
    [String(answer["budgetId"])],
  );
  expect(cleared[0]?.cleared_at).not.toBeNull();
  expect(cleared[0]?.cleared_reason).toMatch(/target of 500000/);

  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("target");
  expect(row?.finishedAt).not.toBe("");
  expect(row?.live).toEqual([]);
});

test("a deadline that has passed stops the drain even while it is under its cost target", async () => {
  const drainId = String(
    (await start({
      concurrent: 1,
      target: { costMicros: 1_000_000_000, deadline: new Date(NOW + 1000).toISOString() },
    }))["drainId"],
  );
  harness.at(NOW + 2000);
  const [report] = await drainTick(deps);
  expect(report?.state).toBe("deadline");
  expect(report?.launched).toBe(0);
  expect((await readDrain(harness.store, drainId))?.state).toBe("deadline");
});

test("a stop leaves nothing in flight and no claim open, within one tick", async () => {
  // THE ACCEPTANCE LINE OF #258. The drain's own jobs are explores and hold no claim, so the
  // claim this asserts about is one the loop took on the same machine: a stop must not leave it,
  // and a later tick must find nothing to relaunch.
  const answer = await start({ concurrent: 2 });
  const drainId = String(answer["drainId"]);

  const halted = await halt(drainId, "the operator stopped it");
  expect(halted["state"]).toBe("stopped");
  expect(halted["cancelled"]).toBe(2);
  expect(fleet.cancelled.map((node) => node.jobId)).toEqual([
    `job_${drainId}_0`,
    `job_${drainId}_1`,
  ]);

  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("stopped");
  expect(row?.reason).toBe("the operator stopped it");
  expect(row?.live).toEqual([]);
  expect(
    await harness.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`),
  ).toEqual([]);

  // One tick later it is still stopped and has launched nothing more: a stopped drain is not a
  // paused one, and this is what five rounds of kill-and-restart could not achieve on the day.
  const reports = await drainTick(deps);
  expect(reports).toEqual([]);
  expect(fleet.executed).toHaveLength(2);
  expect(String((await halt(drainId))["refused"])).toMatch(/already ended as stopped/);
});

test("a stop the hub will not honour still ends the drain, and says which job it could not cancel", async () => {
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);
  fleet.cancelRefusal = "jobs:cancel capability required at target";
  const halted = await halt(drainId);
  expect(halted["cancelled"]).toBe(0);
  expect(String(halted["note"])).toMatch(/was not cancelled: jobs:cancel capability required/);
  // The drain is over either way: it has stopped launching, which is what stopping means.
  expect((await readDrain(harness.store, drainId))?.state).toBe("stopped");
});

test("the status folds the live spend, the rate over the last three minutes, and the ETA", async () => {
  const drainId = String((await start({ concurrent: 2, target: { costMicros: 4_000_000 } }))["drainId"]);

  // What the conductor folds out of a running job's replay ring (#261) is where a live spend
  // comes from, so the rows are written the way it writes them.
  for (const [runId, cost] of [
    [`run_${drainId}_0`, 0.4],
    [`run_${drainId}_1`, 0.2],
  ] as const) {
    await harness.db.run(
      `INSERT INTO run_progress(run_id, job_id, stage, since, calls, input_tokens, output_tokens,
                                cache_tokens, cost_usd, last_model, updated_at)
       VALUES (?, ?, 'at the model', ?, 2, 10000, 500, 0, ?, 'claude-sonnet-4-5', ?)`,
      [runId, `job_${runId.slice(4)}`, stamp(NOW), cost, stamp(NOW)],
    );
  }
  const first = await statusOf(drainId);
  expect(first["jobsLive"]).toBe(2);
  expect(first["jobsAtModel"]).toBe(2);
  expect(first["spent"]).toEqual({
    calls: 4,
    inputTokens: 20_000,
    outputTokens: 1_000,
    costMicros: 600_000,
  });
  // Nothing has been observed twice yet, so the rate is zero rather than a total over an
  // elapsed time — and zero is the honest answer, not a dash.
  expect(first["outputTokensPerMinute"]).toBe(0);
  expect(first["etaAt"]).toBe("");

  // One tick takes the first sample; a minute later the second one makes a rate.
  await drainTick(deps);
  harness.at(NOW + 60_000);
  await harness.db.run(`UPDATE run_progress SET output_tokens = 1500, cost_usd = 0.9`, []);
  await drainTick(deps);

  const later = await statusOf(drainId);
  // 500 -> 1500 and 1500 -> 2000 output tokens over one minute across two rows.
  expect(Number(later["outputTokensPerMinute"])).toBeGreaterThan(0);
  expect(Number(later["costMicrosPerMinute"])).toBeGreaterThan(0);
  // …and the ETA is an instant ahead of now, because the target has not been reached.
  expect(Date.parse(String(later["etaAt"]))).toBeGreaterThan(NOW + 60_000);
});

test("a refused submission is counted as spend with a result, not as a free failure", async () => {
  const drainId = String((await start({ concurrent: 1, target: { costMicros: 9_000_000 } }))["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 300_000, reason: "schema: no outcome on the claim" });
  await drainTick(deps);
  const row = await readDrain(harness.store, drainId);
  expect(row?.refusals).toEqual({ schema: 1 });
  expect(row?.spent.costMicros).toBe(300_000);
  expect((await statusOf(drainId))["refusals"]).toEqual({ schema: 1 });
});

test("a start that can launch nothing refuses, and leaves no drain and no overlay behind", async () => {
  fleet.refusal = "dev-01 refused the job: concurrency_limit";
  const refused = String((await start())["refused"]);
  expect(refused).toMatch(/launched nothing/);
  expect(refused).toMatch(/concurrency_limit/);
  // The row is written before the first post and closed when none lands, so what survives is a
  // `failed` drain that says why rather than a `running` one holding nothing.
  const rows = await harness.db.query<{ state: string; reason: string }>(
    `SELECT state, reason FROM drains`,
  );
  expect(rows[0]?.state).toBe("failed");
  expect(rows[0]?.reason).toMatch(/concurrency_limit/);
  const overlay = await harness.db.query<{ cleared_at: string | null }>(
    `SELECT cleared_at FROM budgets`,
  );
  expect(overlay[0]?.cleared_at).not.toBeNull();
});

test("a launch whose run row never landed releases its slot instead of holding it for ever", async () => {
  const drainId = String((await start({ concurrent: 2 }))["drainId"]);
  // A row-write that did not land: the job is held, nothing will ever settle it, and on
  // 2026-09-13 exactly this shape held the top-ranked subjects for 86 minutes each.
  await harness.db.run(`DELETE FROM runs WHERE id = ?`, [`run_${drainId}_0`]);
  const [report] = await drainTick(deps);
  expect(report?.notes.join(" ")).toMatch(/no run row to show for it, so its slot is released/);
  expect(report?.launched).toBe(1);
  expect((await readDrain(harness.store, drainId))?.live).toHaveLength(2);
});

test("disabling the policy mid-drain ends it as an operator's act rather than as a failure", async () => {
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);
  expect((await readDrain(harness.store, drainId))?.state).toBe("running");
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "turning it off",
    payload: JSON.stringify({ enabled: false, perCycleCost: 0.25, batchSize: 1, dailyCost: 2 }),
    recorded_at: stamp(NOW + 1000),
  });
  const [report] = await drainTick(deps);
  expect(report?.state).toBe("stopped");
  expect(report?.reason).toMatch(/was disabled/);
  expect(report?.launched).toBe(0);
  expect((await readDrain(harness.store, drainId))?.state).toBe("stopped");
});
