/*
  The two doors Watch posts to, held to what they do to the world.

  Every test dispatches the way the kit does — parse the arguments against the action's own
  input, run the handler, parse what it produced against the action's own result — and then
  asks the STORE and the FLEET what happened.

  WHAT A LAUNCH DOES TO THE WORLD IS NOTHING (#279). A Babel run is a Code session: the
  operator picks a saved Code profile or parametrizes one in Code's generator, and Code's
  `runSession` door posts it. So the cases that pinned Babel's own posting — the preparation
  window, the recipe selection, the machine description, the run row, the drawn cycle — are
  deleted rather than re-pinned, and what stands in their place is the refusal, by name, with
  both issues in it, and the proof that the fleet was never touched.

  `stop` is unchanged and still fully exercised: a run this deployment already started can be
  running when the plugin is upgraded, and ending it releases what it reserved.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, OPERATIONS, PRESET_OPERATIONS } from "../contract.ts";
import type { JobLaunch, JobRef, JobRunState, MachineReadiness } from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import { coordinator } from "../store/coordinator.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import type { Door } from "./door.ts";
import { launchDoors, type LaunchDeps } from "./launch.ts";

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";
const RECORD = "fnd_00000001";

/** The machine, as the engine describes one that can run Babel. */
const READY: MachineReadiness = {
  connected: true,
  operations: { [OPERATIONS.scan]: { ready: true, reason: null } },
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

  follow(): never {
    throw new Error("a launch never follows a job");
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

let harness: TestStore;
let fleet: Fleet;
let doors: readonly Door[];

const ctx = { principal: { id: "operator" } } as unknown as GuestCtx;

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

/**
 * A launch as the panel would post one: the request, plus the OPERATION NODE the door is
 * authorized at. Every test goes through this rather than hand-writing the node, because a
 * launch without one is not a request the hub would ever deliver — the host refuses `invalid
 * authority target` before the handler is entered.
 */
async function start(
  args: { readonly preset: keyof typeof PRESET_OPERATIONS } & Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const machineId = typeof args["machineId"] === "string" ? args["machineId"] : MACHINE;
  return await dispatch(ACTIONS.launch, {
    ...args,
    machineId,
    operation: { kind: "operation", machineId, operationId: PRESET_OPERATIONS[args.preset] },
  });
}

/** A stop as a run row's own Stop button posts one: the run, and that run's job node. */
async function halt(
  runId: string,
  job: { readonly operationId: string; readonly jobId: string; readonly machineId?: string },
  reason = "",
): Promise<Record<string, unknown>> {
  return await dispatch(ACTIONS.stop, {
    runId,
    reason,
    job: {
      kind: "job",
      machineId: job.machineId ?? MACHINE,
      operationId: job.operationId,
      jobId: job.jobId,
    },
  });
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  fleet = new Fleet();
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
  const deps: LaunchDeps = {
    coordinator: coordinator(store, () => store.now(), 16),
    jobs: () => fleet,
    now: () => store.now(),
  };
  doors = launchDoors(store, deps);
});

afterEach(() => {
  harness.close();
});

test("the roster declares a governed launch and a governed stop, and no dry read", () => {
  // The preview went with Babel's own inference policy: what a run costs is a fact about a
  // composition, and a composition is Code's to make.
  expect(doors.map((entry) => entry.action.name)).toEqual([ACTIONS.launch, ACTIONS.stop]);
  const [launch, stop] = doors as readonly Door[];

  // A governed cap without a requirement is refused outright by the dispatcher, and a
  // requirement whose cap is not declared is refused at assembly: the two lists pair exactly.
  expect(launch?.action.caps).toEqual(["machines:run"]);
  expect(launch?.action.requirements).toEqual([{ cap: "machines:run", target: ["operation"] }]);
  expect(stop?.action.caps).toEqual(["jobs:cancel"]);
  expect(stop?.action.requirements).toEqual([{ cap: "jobs:cancel", target: ["job"] }]);
});

test("every declared requirement resolves to a node in the arguments the panel posts", () => {
  // This is the host's own walk (`plugin-host.ts`: follow the target through the RAW arguments,
  // parse a `ManifoldRef`), and it is the whole reason the node travels in the request. A door
  // whose target named a field nobody posts is refused `invalid authority target` for every
  // caller, which is a denial no test of the handler would ever see.
  const posted: Record<string, Record<string, unknown>> = {
    [ACTIONS.launch]: {
      machineId: MACHINE,
      preset: "keep-going",
      operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.scan },
    },
    [ACTIONS.stop]: {
      runId: "run_live",
      job: { kind: "job", machineId: MACHINE, operationId: OPERATIONS.scan, jobId: "job_live" },
    },
  };
  for (const entry of doors) {
    for (const requirement of entry.action.requirements ?? []) {
      let value: unknown = posted[entry.action.name];
      for (const segment of requirement.target) {
        value =
          value !== null && typeof value === "object" && Object.hasOwn(value, segment)
            ? Reflect.get(value, segment)
            : undefined;
      }
      expect(value).toMatchObject({ kind: expect.any(String), machineId: MACHINE });
    }
  }
});

test("every preset answers engine_pending, naming manifold#575 and code#170, and posts nothing", async () => {
  /*
    THE ONE THING THIS DOOR DOES. The refusal has to carry both issues because they are what an
    operator schedules against: manifold#575 is the missing in-process door call and code#170 is
    the door Babel will call. A refusal that said only "not available" would send him looking.
  */
  for (const preset of Object.keys(PRESET_OPERATIONS) as (keyof typeof PRESET_OPERATIONS)[]) {
    const answer = await start({ preset, sinceDays: 1, draws: 1, minutes: 5 });
    const refused = String(answer["refused"]);
    expect(refused).toStartWith("engine_pending:");
    expect(refused).toContain("Babel runs are Code sessions");
    expect(refused).toContain("Code's runSession door is not yet available");
    expect(refused).toContain("atyrode/manifold#575");
    expect(refused).toContain("atyrode/code#170");
  }
  // Nothing was posted, nothing was described, and no run row was invented for a job that does
  // not exist: a row for a run nobody started is a row an operator waits on for ever.
  expect(fleet.executed).toEqual([]);
  expect(fleet.described).toBe(0);
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
});

test("a request authorized at one node and aimed at another is refused as itself", async () => {
  // The host discharged the caller's authority at the node in the ARGUMENTS, and a request whose
  // two halves disagree is a mistake the operator fixes — so it is answered as itself rather
  // than folded into the engine's absence, which he can do nothing about.
  const crossed = await dispatch(ACTIONS.launch, {
    machineId: MACHINE,
    preset: "keep-going",
    operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.explore },
  });
  expect(crossed["refused"]).toContain(OPERATIONS.explore);
  expect(crossed["refused"]).not.toContain("engine_pending");
  expect(fleet.executed).toEqual([]);
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

  const answer = await halt(
    "run_live",
    { operationId: OPERATIONS.evaluate, jobId: "job_live" },
    "it is arguing with itself",
  );

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

  expect(
    (await halt("run_done", { operationId: OPERATIONS.scan, jobId: "job_done" }))["refused"],
  ).toContain("already ended");
  expect(
    (await halt("run_nothing", { operationId: OPERATIONS.scan, jobId: "job_none" }))["refused"],
  ).toContain("no run run_nothing");
  expect(fleet.cancelled).toEqual([]);
});

test("a machine that refuses to stop leaves the run open rather than lying about it", async () => {
  await insert(harness.db, "runs", {
    id: "run_live", kind: OPERATIONS.scan, machine_id: MACHINE, job_id: "job_live",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
  fleet.refusal = "job_not_cancellable";

  const answer = await halt("run_live", { operationId: OPERATIONS.scan, jobId: "job_live" });

  expect(answer["refused"]).toContain("job_not_cancellable");
  expect((await harness.store.run("run_live")).run).toMatchObject({ state: "running" });
});

test("a stop authorized at one job and aimed at another reaches nothing", async () => {
  await insert(harness.db, "runs", {
    id: "run_live", kind: OPERATIONS.scan, machine_id: MACHINE, job_id: "job_live",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
  const elsewhere = await halt("run_live", {
    machineId: "m-other",
    operationId: OPERATIONS.scan,
    jobId: "job_live",
  });
  expect(elsewhere["refused"]).toContain("m-other");
  expect(fleet.cancelled).toEqual([]);
  expect((await harness.store.run("run_live")).run).toMatchObject({ state: "running" });
});
