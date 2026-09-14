/*
  The three doors Watch posts to, held to what they do to the world.

  Every test dispatches the way the kit does — parse the arguments against the action's own
  input, run the handler, parse what it produced against the action's own result — and then
  asks the STORE and the FLEET what happened.

  WHAT A LAUNCH DOES IS FIVE STEPS AND STOPS AT THE FIFTH (#279). A Babel run is a Code
  session: the operator names a saved Code profile, Babel chooses the sessions, posts its OWN
  `prepare` job to seal them as the material, composes the prompt around `/inputs/material` —
  and then asks `atyrode.code.runSession` to post the session. That last call is what
  `MATERIAL_INPUT_PENDING` still refuses, because Manifold cannot yet bind one job's sealed
  output into another plugin's job. So the tests below pin the four steps that DO happen, the
  refusal that ends the fifth, and the fact that the preparation is real work left behind
  rather than a ghost.

  `stop` is unchanged and still fully exercised: a run this deployment already started can be
  running when the plugin is upgraded, and ending it releases what it reserved.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  MACHINE_OPERATIONS,
  MATERIAL_INPUT_PENDING_CODE,
  MATERIAL_OUTPUT,
  OPERATIONS,
  OUTPUT_BINDING,
  PRESET_OPERATIONS,
  type ProfileRow,
} from "../contract.ts";
import type { JobLaunch, JobRef, JobRunState, MachineReadiness } from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import {
  materialInput,
  type CodeEngine,
  type CodeJob,
  type EngineAnswer,
  type SessionRequest,
} from "../server/engine/session.ts";
import type { Recipe } from "../server/engine/prompts.ts";
import { coordinator } from "../store/coordinator.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import type { Door } from "./door.ts";
import { DRAW_PENDING, launchDoors, type LaunchDeps } from "./launch.ts";

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

/** A Code refusal, in the shape `codeEngine` folds every refusal onto. */
function refusedByCode<T>(code: string, detail: string): EngineAnswer<T> {
  return { ok: false, code: code as never, refused: `${code}: ${detail}` };
}

/**
 * CODE, as this door reaches it. `runSession` is the one verb that cannot be called yet:
 * `codeEngine` refuses it `material_input_pending` before the call is made, so a fake that
 * ACCEPTED it would be testing a door against a world that does not exist. This one throws if
 * it is ever reached, which is how the refusal's position in the sequence is pinned.
 */
class Code implements CodeEngine {
  saved: { containerId: string; revision: number; model: string; thinking: string; lastMachineId: string }[] =
    [
      {
        containerId: "ctr_workbench",
        revision: 7,
        model: "anthropic/claude-opus-4-1",
        thinking: "high",
        lastMachineId: MACHINE,
      },
    ];
  unavailable = "";

  async profiles(): Promise<EngineAnswer<readonly ProfileRow[]>> {
    if (this.unavailable !== "") {
      return await Promise.resolve(refusedByCode("engine_unavailable", this.unavailable));
    }
    return await Promise.resolve({ ok: true, value: this.saved });
  }

  /*
    THE REFUSAL IS THE REAL ONE. `materialInput` is production code and it is the single line
    that moves when Manifold's job-inputs primitive lands, so the fake asks it rather than
    inventing a sentence: the day that line returns a binding, this fake stops refusing and
    the test that pins the refusal fails, which is exactly the reminder that wants leaving.
  */
  async runSession(request: SessionRequest): Promise<EngineAnswer<CodeJob>> {
    const material = materialInput(request.prepareJobId);
    if ("refused" in material) {
      return await Promise.resolve({
        ok: false,
        code: MATERIAL_INPUT_PENDING_CODE,
        refused: material.refused,
      });
    }
    throw new Error("the material binds now: this fake has to post a session");
  }

  async readSession(): Promise<never> {
    throw new Error("a launch never reads a session back");
  }
}

/** One cookbook recipe, as the hub's policy document holds one and the prompt carries it. */
const RECIPES: Record<string, Recipe> = {
  "code-health": {
    id: "code-health",
    version: 3,
    title: "Code health",
    body: "Look for the thing that keeps going wrong.",
  },
};

let harness: TestStore;
let fleet: Fleet;
let code: Code;
let cookbook: Record<string, Recipe>;
let doors: readonly Door[];

let minted = 0;
const ctx = {
  principal: { id: "operator" },
  newId: () => `id${String((minted += 1))}`,
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
  code = new Code();
  cookbook = { ...RECIPES };
  const deps: LaunchDeps = {
    coordinator: coordinator(store, () => store.now(), 16),
    jobs: () => fleet,
    engine: () => code,
    cookbook: async () => await Promise.resolve(cookbook),
    plan: () => ({
      metered: {},
      limits: {
        timeoutMs: 3_600_000,
        memoryBytes: 1024 * 1024 * 1024,
        processes: 64,
        outputBytes: 64 * 1024 * 1024,
      },
    }),
    now: () => store.now(),
  };
  doors = launchDoors(store, deps);
});

afterEach(() => {
  harness.close();
});

test("the roster is profiles, launch and stop, and none of them is governed at a node that is gone", () => {
  expect(doors.map((entry) => entry.action.name)).toEqual([
    ACTIONS.profiles,
    ACTIONS.launch,
    ACTIONS.stop,
  ]);
  const [profiles, launch, stop] = doors as readonly Door[];

  // Reading Code's saved profiles is a read of containers and nothing else.
  expect(profiles?.action.caps).toEqual(["containers:read"]);
  expect(profiles?.action.requirements).toBeUndefined();

  // A launch posts Babel's OWN `prepare` job and asks Code to post the session, so it keeps
  // the delegates that posting needs — reading the job back, and the locations the sealed
  // leases are cut from — and names no governed node, because the operations a requirement
  // would name (`explore`, `evaluate`) are declared by nobody.
  expect(launch?.action.caps).toEqual(["containers:read"]);
  expect(launch?.action.requirements).toBeUndefined();
  expect(launch?.action.delegates).toEqual(["jobs:read", "locations:read", "locations:write"]);

  // A stop closes this plugin's own rows and reaches a job through its OWN ceiling: a delegate
  // rather than a cap the caller must hold at a node no installation declares any more.
  expect(stop?.action.caps).toEqual(["containers:write"]);
  expect(stop?.action.requirements).toBeUndefined();
  expect(stop?.action.delegates).toEqual(["jobs:cancel"]);
});

test("no door requires a node at an operation no installation declares", () => {
  /*
    THE DEFECT THIS PINS. The host discharges a door's `requirements` against the RAW arguments
    BEFORE the handler runs (`plugin-host.ts`: walk the target, parse a `ManifoldRef`, ask the
    authority waterfall, then admit against the operator's version-bound CONSENT at that node;
    `job-service.ts` refuses an operation the installation does not declare). `machines:run` at
    `atyrode.babel.explore` was exactly that, and this bundle stopped declaring the operation
    — so every model preset was refused "explicit version-bound consent required" at a node
    that cannot exist, and the door's own refusal was unreachable. A refusal the caller cannot
    reach is not a refusal, so no door may name a node while none is declared.
  */
  for (const entry of doors) {
    expect({ door: entry.action.name, requirements: entry.action.requirements }).toEqual({
      door: entry.action.name,
      requirements: undefined,
    });
  }
  // The two ids a requirement would have named are the two this manifest no longer declares.
  const declared: readonly string[] = Object.values(MACHINE_OPERATIONS);
  expect(declared).not.toContain(OPERATIONS.explore);
  expect(declared).not.toContain(OPERATIONS.evaluate);
});

test("the profiles door answers Code's saved list, and Code's silence as the sentence it refused with", async () => {
  const listed = await dispatch(ACTIONS.profiles, {});
  expect(listed["unavailable"]).toBe("");
  expect(listed["profiles"]).toEqual([
    {
      containerId: "ctr_workbench",
      revision: 7,
      model: "anthropic/claude-opus-4-1",
      thinking: "high",
      lastMachineId: MACHINE,
    },
  ]);

  // BOTH HALVES ARE ANSWERS. An empty list with no word beside it reads as "you have saved
  // none" when what happened is that Code could not be asked at all.
  code.unavailable = "atyrode.babel -> atyrode.code";
  const silent = await dispatch(ACTIONS.profiles, {});
  expect(silent["profiles"]).toEqual([]);
  expect(String(silent["unavailable"])).toStartWith("engine_unavailable:");
});

test("a model preset naming no Code profile is refused by name, and nothing is posted", async () => {
  const answer = await start({ preset: "read-whats-new", sinceDays: 1 });
  expect(String(answer["refused"])).toStartWith("profile_required:");
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
});

test("a hub holding no cookbook recipe refuses an explore rather than posting one with no method", async () => {
  cookbook = {};
  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  expect(answer["refused"]).toBe(
    "no cookbook recipe is installed on this hub, so an explore has no method to run",
  );
  expect(fleet.executed).toEqual([]);
});

test("an explore seals its material, then answers material_input_pending and leaves the preparation behind", async () => {
  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  /*
    THE REFUSAL IS THE FIFTH STEP, and it names the two lines that move when Manifold can bind
    one job's sealed output into another plugin's job. Everything before it happened.
  */
  const refused = String(answer["refused"]);
  expect(refused).toStartWith(`${MATERIAL_INPUT_PENDING_CODE}:`);
  expect(refused).toContain('exports: ["material"]');
  expect(refused).toContain("server/engine/session.ts");

  // THE MATERIAL IS REAL WORK, POSTED: one `atyrode.babel.prepare` job with TWO sealed leases,
  // the ordinary outputs and the material a session will read.
  expect(fleet.executed).toHaveLength(1);
  const sealed = fleet.executed[0]!;
  expect(sealed.operationId).toBe(OPERATIONS.prepare);
  expect(sealed.outputs.map((output) => output.name)).toEqual([OUTPUT_BINDING, MATERIAL_OUTPUT]);
  expect(JSON.parse(String(sealed.input["input"]))["selectors"]).toEqual(["omp/s1"]);

  // …and its run row stands, because the catalog work happened and a later cycle ingests it.
  // A session that was never posted leaves NO row of its own: a run an operator waits on for
  // ever is exactly the ghost this ordering exists to avoid.
  const runs = await harness.db.query<{ id: string; kind: string; container_id: string | null }>(
    `SELECT id, kind, container_id FROM runs ORDER BY id`,
  );
  expect(runs).toHaveLength(1);
  expect(runs[0]?.kind).toBe(OPERATIONS.prepare);
  expect(runs[0]?.container_id).toBeNull();
});

test("a drawn preset answers draw_pending, names where the lane returns, and posts nothing", async () => {
  for (const preset of ["review-backlog", "file-and-tidy"] as const) {
    const answer = await start({ preset, draws: 1 });
    expect(answer["refused"]).toBe(DRAW_PENDING);
    expect(String(answer["refused"])).toContain("#268");
  }
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
});

test("keep-going posts Babel's own beat, which reaches no model and needs no profile", async () => {
  const answer = await start({ preset: "keep-going", minutes: 30 });

  expect(answer["kind"]).toBe("conductor");
  expect(fleet.executed).toHaveLength(1);
  const beat = fleet.executed[0]!;
  expect(beat.operationId).toBe(OPERATIONS.scan);
  // The operator's own bound on the beat, under the operation's ceiling.
  expect(beat.limits?.timeoutMs).toBe(30 * 60_000);
  const runs = await harness.db.query<{ id: string; kind: string }>(`SELECT id, kind FROM runs`);
  expect(runs).toHaveLength(1);
  expect(runs[0]?.kind).toBe(OPERATIONS.scan);
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
