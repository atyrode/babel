/*
  The two doors that spend money, held to what they do to the world.

  Every test dispatches the way the kit does — parse the arguments against the action's own
  input, run the handler, parse what it produced against the action's own result — and then
  asks the STORE and the FLEET what happened, because that is what a launch is: one job posted
  to one machine and one row written about it. The fleet is fake and the store is real, which
  is the right way round: the job request is a shape this file can pin exactly, and the run row
  is SQL under every CHECK and trigger the schema declares.

  The coordinator is the real one over the same store, so the policy in force is a row an
  operator could have written and the claim a stop releases is a claim the ledger holds.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, INPUT_FIELD, OPERATIONS, OUTPUT_BINDING, OUTPUT_LOCATION } from "../contract.ts";
import type {
  Conductor,
  JobLaunch,
  JobRef,
  JobRunState,
  MachineReadiness,
  Recipe,
  RunPlan,
  TickReport,
} from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import { coordinator } from "../store/coordinator.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import type { Door } from "./door.ts";
import { launchDoors, type LaunchDeps } from "./launch.ts";

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";
const TOPIC = "ent_00000001";
const RECORD = "fnd_00000001";

const RECIPE: Recipe = {
  id: "code-health-comprehensibility",
  version: 3,
  title: "Code health",
  body: "What in this code is harder to understand than it needs to be?",
};

const LIMITS = { timeoutMs: 600_000, memoryBytes: 1_073_741_824, processes: 32, outputBytes: 1_048_576 };

const PLAN: RunPlan = {
  engine: { binary: "/runtime/bin/code", args: [] },
  profile: { id: "analysis", revision: 3 },
  caps: { perRunUsd: 0.0625, toolCalls: 40, idleMs: 120_000, handshakeMs: 30_000 },
  recipes: {},
  requireContainment: true,
  limits: LIMITS,
};

/** The machine, as the engine describes one that can run Babel. */
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
  described = 0;
  readiness: MachineReadiness = READY;
  refusal = "";

  describe(): MachineReadiness {
    this.described += 1;
    return this.readiness;
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
    if (this.refusal !== "") throw new Error(this.refusal);
    this.cancelled.push(node);
  }

  status(): JobRunState {
    throw new Error("a launch never reads a job back");
  }

  listRuns(): { runs: readonly { job: JobRunState | null }[] } {
    throw new Error("a launch never lists runs");
  }

  output(): { data: string; eof: boolean } {
    throw new Error("a launch never reads an output");
  }

  schedules(): readonly [] {
    return [];
  }

  schedule(): never {
    throw new Error("a launch never schedules");
  }

  disableSchedule(): never {
    throw new Error("a launch never unschedules");
  }
}

/** One cycle of the loop, as the drawn presets see it. */
class Cycle implements Conductor {
  ticks = 0;
  reports: TickReport[] = [];

  async tick(): Promise<TickReport> {
    const report = this.reports[Math.min(this.ticks, this.reports.length - 1)];
    this.ticks += 1;
    if (report === undefined) throw new Error("the cycle was asked for a report it has none of");
    return await Promise.resolve(report);
  }
}

function report(over: Partial<TickReport> = {}): TickReport {
  return {
    at: NOW,
    cycleRunId: "cyc_1",
    policyVersion: "p1",
    enabled: true,
    schedule: "absent",
    requested: [],
    ingested: [],
    settled: [],
    refused: [],
    stop: null,
    gaps: [],
    pending: 0,
    notes: [],
    ...over,
  };
}

let harness: TestStore;
let fleet: Fleet;
let cycle: Cycle;
let cookbook: Record<string, Recipe>;
let doors: readonly Door[];
let minted = 0;

const ctx = {
  principal: { id: "operator" },
  newId: async () => {
    minted += 1;
    return await Promise.resolve(`00000${String(minted)}`);
  },
} as unknown as GuestCtx;

async function dispatch(name: string, args: unknown): Promise<Record<string, unknown>> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success) return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
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

/** The document one job carries, read back out of the request the fleet was handed. */
function documentOf(launch: JobLaunch): Record<string, unknown> {
  const text = launch.input[INPUT_FIELD];
  if (typeof text !== "string") throw new Error("the job carries no input document");
  return JSON.parse(text) as Record<string, unknown>;
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  fleet = new Fleet();
  cycle = new Cycle();
  cookbook = {};
  minted = 0;
  const { db, store } = harness;
  // An enabled policy, as `setPolicy` writes one: the document is the coordinator's own shape.
  await insert(db, "policies", {
    version: "p1",
    seq: 1,
    actor_id: "operator",
    reason: "turning it on",
    payload: JSON.stringify({ enabled: true, perCycleCost: 0.25, batchSize: 4, dailyCost: 2 }),
    recorded_at: stamp(NOW - HOUR),
  });
  await insert(db, "sessions", {
    selector: "omp/s1", host: MACHINE, harness: "omp", source_id: "s1", title: "yesterday",
    content_digest: "d1", snapshot_id: "snap-1", seen_at: stamp(NOW - 2 * HOUR),
  });
  await insert(db, "sessions", {
    selector: "omp/s2", host: MACHINE, harness: "omp", source_id: "s2", title: "last month",
    content_digest: "d2", seen_at: stamp(NOW - 40 * 24 * HOUR),
  });
  const deps: LaunchDeps = {
    coordinator: coordinator(store, () => store.now()),
    get cookbook() {
      return cookbook;
    },
    jobs: () => fleet,
    plan: () => PLAN,
    cycle: () => cycle,
    now: () => store.now(),
  };
  doors = launchDoors(store, deps);
});

afterEach(() => {
  harness.close();
});

test("the roster declares the two doors as writes", () => {
  expect(doors.map((entry) => entry.action.name)).toEqual([ACTIONS.launch, ACTIONS.stop]);
  for (const entry of doors) {
    expect(entry.action.caps).toEqual(["containers:write"]);
    // A governed capability at a door needs a reference target (ADR 0035), and the panel posts
    // a machine ID rather than a machine node: declaring one here would refuse every dispatch.
    expect(entry.action.requirements).toBeUndefined();
  }
});

test("a preview states what will run, and runs nothing", async () => {
  await insert(harness.db, "runs", {
    id: "run-old", kind: "explore", machine_id: MACHINE, job_id: "job-old",
    started_at: stamp(NOW - 3 * HOUR), finished_at: stamp(NOW - 2 * HOUR), closure: "completed",
    records: 2,
    payload: JSON.stringify({
      runId: "run-old",
      closure: "completed",
      profile: {
        id: "analysis", revision: 3, model: "claude-sonnet-4-5", disclosure: "cloud",
        costPer1k: { input: 0.003, output: 0.015 },
      },
    }),
  });

  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "read-whats-new", sinceDays: 1, preview: true,
  });

  expect(answer).toEqual({
    runId: "",
    jobId: "",
    machineId: MACHINE,
    kind: "explore",
    // What actually ran last, from the receipt that recorded it — never the reference asked for.
    profile: {
      id: "analysis", revision: 3, model: "claude-sonnet-4-5", disclosure: "cloud",
      costPer1k: { input: 0.003, output: 0.015 },
    },
    // One claim's reservation is the per-cycle allowance over the batch; the day's is the day's.
    ceiling: { perRunUsd: 0.0625, perDayUsd: 2 },
  });
  expect(fleet.executed).toEqual([]);
  expect(fleet.described).toBe(0);
  expect((await harness.store.runs({ limit: 25, offset: 0 })).total).toBe(1);
});

test("a preview of a deployment that has run nothing states no profile", async () => {
  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "keep-going", minutes: 30, preview: true,
  });
  expect(answer["profile"]).toBeNull();
  expect(answer["kind"]).toBe("conductor");
});

test("keep going starts the beat, under the operator's own minutes, and records the run", async () => {
  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "keep-going", minutes: 5,
  });

  expect(fleet.executed).toHaveLength(1);
  const launch = fleet.executed[0]!;
  expect(launch).toMatchObject({
    jobId: "job_000001",
    machineId: MACHINE,
    operationId: OPERATIONS.scan,
    outputs: [{ name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: ["job_000001"] }],
    installationRevision: "rev-7",
    artifactSha256: "a".repeat(64),
  });
  // Five minutes is what he asked for; the operation's own ceiling is what it cannot pass.
  expect(launch.limits).toEqual({ ...LIMITS, timeoutMs: 300_000 });
  expect(documentOf(launch)).toEqual({
    runId: "run_000001", machineId: MACHINE, roots: [], harnesses: [],
  });
  expect(answer).toMatchObject({ runId: "run_000001", jobId: "job_000001", kind: "conductor" });

  const runs = await harness.store.runs({ limit: 25, offset: 0 });
  expect(runs.runs).toHaveLength(1);
  expect(runs.runs[0]).toMatchObject({
    id: "run_000001", kind: OPERATIONS.scan, machineId: MACHINE, jobId: "job_000001",
    state: "running", finishedAt: "",
  });
});

test("reading what is new carries the window's sessions and the recipe it was told to run", async () => {
  cookbook[RECIPE.id] = RECIPE;

  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "read-whats-new", sinceDays: 1, recipes: [RECIPE.id],
  });

  expect(answer).toMatchObject({ runId: "run_000001", jobId: "job_000001", kind: "explore" });
  const launch = fleet.executed[0]!;
  expect(launch.operationId).toBe(OPERATIONS.explore);
  const document = documentOf(launch);
  expect(document).toMatchObject({
    runId: "run_000001",
    machineId: MACHINE,
    engine: { binary: "/runtime/bin/code", args: [], cwd: "" },
    profile: { id: "analysis", revision: 3 },
    recipes: [RECIPE],
    stages: ["explore"],
    caps: { toolCalls: 40, minutes: 0, perRunUsd: 0.0625, idleMs: 120_000, handshakeMs: 30_000 },
    requireContainment: true,
  });
  // The window is the window: the session last seen forty days ago is not in it.
  expect(document["preparation"]).toEqual({
    id: "",
    selection: [
      { harness: "omp", sourceId: "s1", selector: "omp/s1", digest: "d1", snapshot: "snap-1" },
    ],
  });
  // One job's whole input record is bounded at 64 KiB, and this one is inside it.
  expect(new TextEncoder().encode(JSON.stringify(launch.input)).byteLength).toBeLessThan(65_536);

  const run = await harness.store.run("run_000001");
  expect(run.run).toMatchObject({ recipe: RECIPE.id, kind: OPERATIONS.explore });
});

test("exploring a topic reads the sessions its own records cite", async () => {
  cookbook[RECIPE.id] = RECIPE;
  const { db } = harness;
  await insert(db, "entities", {
    id: TOPIC, kind: "repository", name: "tyrode-infra", canonical_id: TOPIC,
    created_by: "operator", created_at: stamp(NOW - HOUR),
  });
  await insert(db, "records", {
    id: RECORD, kind: "finding", root_id: RECORD, seq: 1, actor_kind: "run", actor_id: "run-old",
    title: "a finding", created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1 }),
  });
  await insert(db, "filings", {
    id: "fil_0001", record_id: RECORD, entity_id: TOPIC, rationale: "about this",
    author_kind: "operator", author_id: "operator", created_at: stamp(NOW - HOUR),
  });
  // The cited session is the one last seen forty days ago: a topic's evidence is not a window.
  await insert(db, "edges", {
    id: "edg_0001", kind: "cites", from_kind: "finding", from_id: RECORD, to_kind: "session",
    to_id: "omp/s2", position: 0, actor_kind: "run", actor_id: "run-old",
    created_at: stamp(NOW - HOUR),
  });

  await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "explore-topic", entityId: TOPIC });

  expect(documentOf(fleet.executed[0]!)["preparation"]).toEqual({
    id: "",
    selection: [
      { harness: "omp", sourceId: "s2", selector: "omp/s2", digest: "d2", snapshot: "" },
    ],
  });
});

test("an explore with no method to run is refused by name, and starts nothing", async () => {
  const empty = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "read-whats-new", sinceDays: 1,
  });
  expect(empty["refused"]).toContain("no cookbook recipe is installed");

  cookbook[RECIPE.id] = RECIPE;
  const missing = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "read-whats-new", sinceDays: 1, recipes: ["time-and-spend"],
  });
  expect(missing["refused"]).toContain("time-and-spend");
  expect(fleet.executed).toEqual([]);
});

test("an explore over a window holding nothing says so rather than starting an empty run", async () => {
  cookbook[RECIPE.id] = RECIPE;
  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "read-whats-new", sinceDays: 1, recipes: [RECIPE.id],
  });
  expect(answer["runId"]).toBe("run_000001");

  const none = await dispatch(ACTIONS.launch, {
    machineId: "m-other", preset: "read-whats-new", sinceDays: 1, recipes: [RECIPE.id],
  });
  expect(none["refused"]).toContain("catalogued no session");
});

test("a machine that cannot run it refuses the launch and posts nothing", async () => {
  fleet.readiness = { ...READY, connected: false };
  expect((await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "keep-going" }))["refused"]).toContain(
    "offline",
  );

  fleet.readiness = {
    ...READY,
    operations: { [OPERATIONS.scan]: { ready: false, reason: "artifact_missing" } },
  };
  const notReady = await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "keep-going" });
  expect(notReady["refused"]).toContain("artifact_missing");
  expect(fleet.executed).toEqual([]);
  expect((await harness.store.runs({ limit: 25, offset: 0 })).total).toBe(0);
});

test("a job the machine refuses leaves no run row behind", async () => {
  fleet.refusal = "machine_offline";
  const answer = await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "keep-going" });
  expect(answer["refused"]).toContain("machine_offline");
  expect((await harness.store.runs({ limit: 25, offset: 0 })).total).toBe(0);
});

test("a disabled policy starts nothing, and says which policy", async () => {
  await insert(harness.db, "policies", {
    version: "p2", seq: 2, actor_id: "operator", reason: "pausing",
    payload: JSON.stringify({ enabled: false }), recorded_at: stamp(NOW),
  });
  const answer = await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "keep-going" });
  expect(answer["refused"]).toContain("p2");
  expect(fleet.executed).toEqual([]);
});

test("a drawn preset runs cycles of the loop and answers with the first job drawn", async () => {
  cycle.reports = [
    report({
      requested: [
        {
          runId: "run_asg1", jobId: "job_asg1", machineId: MACHINE, claimId: "asg1",
          recordId: RECORD, role: "reception", lane: "coverage",
        },
      ],
    }),
    report({ stop: { reason: "per-cycle", detail: "one cycle's allowance is spent" } }),
  ];

  const answer = await dispatch(ACTIONS.launch, {
    machineId: MACHINE, preset: "review-backlog", draws: 3,
  });

  expect(answer).toMatchObject({ runId: "run_asg1", jobId: "job_asg1", kind: "evaluate" });
  // The door posts nothing itself: the conductor claims and dispatches, so the ceiling and the
  // fence are the coordinator's in both paths.
  expect(fleet.executed).toEqual([]);
  expect(cycle.ticks).toBe(2);
});

test("a cycle that draws nothing answers with the reason nothing was drawn", async () => {
  cycle.reports = [report({ stop: { reason: "no-candidates", detail: "every candidate is resting" } })];
  const answer = await dispatch(ACTIONS.launch, { machineId: MACHINE, preset: "file-and-tidy", draws: 2 });
  expect(answer["refused"]).toContain("every candidate is resting");
  expect(cycle.ticks).toBe(1);
});

test("stop cancels the job, closes the run and releases what it reserved", async () => {
  const { db, store } = harness;
  await insert(db, "runs", {
    id: "run_live", kind: OPERATIONS.evaluate, machine_id: MACHINE, job_id: "job_live",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
  await insert(db, "claims", {
    id: "asg_live", record_id: RECORD, role: "reception", lane: "coverage", policy_version: "p1",
    job_id: "job_live", run_id: "cyc_1", fence: 1, reserved_cost: 0.0625,
    granted_at: stamp(NOW - HOUR), expires_at: stamp(NOW + HOUR),
  });

  const answer = await dispatch(ACTIONS.stop, { runId: "run_live", reason: "it is arguing with itself" });

  expect(answer).toEqual({
    runId: "run_live", jobId: "job_live", machineId: MACHINE, closure: "stopped",
  });
  expect(fleet.cancelled).toEqual([
    { kind: "job", machineId: MACHINE, operationId: OPERATIONS.evaluate, jobId: "job_live" },
  ]);
  const run = await store.run("run_live");
  expect(run.run).toMatchObject({ state: "stopped", freshness: "ended" });
  expect(run.receipt).toMatchObject({ closure: "stopped", stoppedBy: "operator", reason: "it is arguing with itself" });
  // The reservation is released at what it actually spent, so the day's allowance is not held
  // by a worker the operator has just sent home.
  const claim = await db.query<{ outcome: string; actual_cost: number; finished_at: string }>(
    `SELECT outcome, actual_cost, finished_at FROM claims WHERE id = 'asg_live'`,
  );
  expect(claim[0]).toMatchObject({ outcome: "skipped", actual_cost: 0 });
});

test("stop refuses a run that has already ended, and one nobody started", async () => {
  await insert(harness.db, "runs", {
    id: "run_done", kind: OPERATIONS.scan, machine_id: MACHINE, job_id: "job_done",
    started_at: stamp(NOW - HOUR), finished_at: stamp(NOW), closure: "completed", records: 0,
    payload: JSON.stringify({ closure: "completed" }),
  });

  expect((await dispatch(ACTIONS.stop, { runId: "run_done" }))["refused"]).toContain("already ended");
  expect((await dispatch(ACTIONS.stop, { runId: "run_nothing" }))["refused"]).toContain("no run run_nothing");
  expect(fleet.cancelled).toEqual([]);
});

test("a machine that refuses to stop leaves the run open rather than lying about it", async () => {
  await insert(harness.db, "runs", {
    id: "run_live", kind: OPERATIONS.scan, machine_id: MACHINE, job_id: "job_live",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
  fleet.refusal = "job_not_cancellable";

  const answer = await dispatch(ACTIONS.stop, { runId: "run_live" });

  expect(answer["refused"]).toContain("job_not_cancellable");
  expect((await harness.store.run("run_live")).run).toMatchObject({ state: "running" });
});
