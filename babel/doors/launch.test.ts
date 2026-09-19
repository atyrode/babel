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
  MATERIAL_OUTPUT,
  OPERATIONS,
  OUTPUT_BINDING,
  PRESET_OPERATIONS,
  type ProfileRow,
} from "../contract.ts";
import type { JobLaunch, JobRef, JobRunState, MachineReadiness } from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import {
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
import {
  DRAW_MANAGED,
  MAX_MATERIAL_BYTES,
  launchDoors,
  launchMachinery,
  type LaunchDeps,
  type LaunchMachinery,
} from "./launch.ts";

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
  saved: ProfileRow[] = [
    {
      containerId: "ctr_workbench",
      revision: 7,
      model: "anthropic/claude-opus-4-1",
      thinking: "high",
      lastMachineId: MACHINE,
      accounts: [{ provider: "anthropic", identityKey: "victorballu", label: "" }],
      resolved: true,
    },
  ];
  unavailable = "";

  async profiles(): Promise<EngineAnswer<readonly ProfileRow[]>> {
    if (this.unavailable !== "") {
      return await Promise.resolve(refusedByCode("engine_unavailable", this.unavailable));
    }
    return await Promise.resolve({ ok: true, value: this.saved });
  }

  /**
   * WHAT A SETTLE WAKE POSTS, and nothing else. A job input binds a SETTLED job's output, and
   * `prepare` is running the instant the press posts it, so the session belongs to
   * `postPrepared` — a `launch` that called this is a `launch` that would bind a job still in
   * flight, and the unset hook says so rather than quietly answering a job id.
   */
  posting: ((request: SessionRequest) => EngineAnswer<CodeJob>) | null = null;

  async runSession(request: SessionRequest): Promise<EngineAnswer<CodeJob>> {
    if (this.posting === null) {
      throw new Error("the press must not post a session: the preparation is still running");
    }
    return await Promise.resolve(this.posting(request));
  }

  /** What a Stop reaches for on a Code session; the launch tests never press one. */
  cancelled: { containerId: string; jobId: string }[] = [];

  async cancelSession(args: {
    containerId: string;
    jobId: string;
  }): Promise<EngineAnswer<CodeJob>> {
    this.cancelled.push(args);
    return await Promise.resolve({
      ok: true,
      value: {
        jobId: args.jobId,
        machineId: MACHINE,
        operationId: "atyrode.omp.session",
        pluginId: "atyrode.omp",
        state: "cancelled",
      },
    });
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
  if (!parsed.success)
    return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
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
    selector: "omp/s1",
    host: MACHINE,
    harness: "omp",
    source_id: "s1",
    title: "yesterday",
    content_digest: "d1",
    snapshot_id: "snap-1",
    seen_at: stamp(NOW - 2 * HOUR),
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
  machinery = launchMachinery(store, deps);
});

afterEach(() => {
  harness.close();
});

test("the roster is profiles, launch, verify and stop, and none is governed at a node that is gone", () => {
  expect(doors.map((entry) => entry.action.name)).toEqual([
    ACTIONS.profiles,
    ACTIONS.launch,
    ACTIONS.verify,
    ACTIONS.stop,
  ]);
  const [profiles, launch, verify, stop] = doors as readonly Door[];

  // Reading Code's saved profiles is a read of containers and nothing else.
  expect(profiles?.action.caps).toEqual(["containers:read"]);
  expect(profiles?.action.requirements).toBeUndefined();

  // A launch posts Babel's OWN `prepare` job and asks Code to post the session, so it keeps
  // the delegates that posting needs — reading the job back, the locations the sealed leases are
  // cut from, and the machine read `ready` describes with before anything is posted — and names
  // no governed node, because the operations a requirement would name (`explore`, `evaluate`)
  // are declared by nobody.
  expect(launch?.action.caps).toEqual(["containers:read"]);
  expect(launch?.action.requirements).toBeUndefined();
  expect(launch?.action.delegates).toEqual([
    "jobs:read",
    "locations:read",
    "locations:write",
    "machines:read",
  ]);

  // Verifying the archive posts one of Babel's OWN jobs and writes its run row, so it carries
  // a write of this plugin's rows and the delegates a posting needs — the same ones, because
  // it is the same posting path.
  expect(verify?.action.caps).toEqual(["containers:write"]);
  expect(verify?.action.requirements).toBeUndefined();
  expect(verify?.action.delegates).toEqual([
    "jobs:read",
    "locations:read",
    "locations:write",
    "machines:read",
  ]);

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
      // The accounts are CODE'S, passed through unchanged: Babel has no broker and infers
      // none, and `resolved` is what says whether Code could name them at all.
      accounts: [{ provider: "anthropic", identityKey: "victorballu", label: "" }],
      resolved: true,
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

test("an explore seals its material and records the intent; the session waits for the settle", async () => {
  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  /*
    THE PRESS ENDS AT THE PREPARATION (#592). A job-inputs binding names a SETTLED job's
    output, so the session cannot be posted while its own `prepare` is still running — the
    answer is the preparation's job, and `postPrepared` turns the run into a Code session on
    the wake that preparation's settlement causes.
  */
  expect(answer["refused"]).toBeUndefined();
  expect(answer["kind"]).toBe("explore");
  expect(answer["jobId"]).toBe(
    `job_${answer["runId"] as string}`.replace("job_run_", "job_") + "_material",
  );

  // THE MATERIAL IS REAL WORK, POSTED: one `atyrode.babel.prepare` job with TWO sealed leases,
  // the ordinary outputs and the material a session will read. Code was not called at all.
  expect(fleet.executed).toHaveLength(1);
  const sealed = fleet.executed[0]!;
  expect(sealed.operationId).toBe(OPERATIONS.prepare);
  expect(sealed.outputs.map((output) => output.name)).toEqual([OUTPUT_BINDING, MATERIAL_OUTPUT]);
  expect(JSON.parse(String(sealed.input["input"]))["selectors"]).toEqual(["omp/s1"]);

  // TWO ROWS: the preparation's, and the explore's own — open, in the Code lane by its
  // container, holding no job yet, and carrying the intent the settle wake composes from.
  const runs = await harness.db.query<{
    id: string;
    kind: string;
    job_id: string | null;
    container_id: string | null;
    prepare_job_id: string | null;
    preparation: string;
  }>(`SELECT id, kind, job_id, container_id, prepare_job_id, preparation FROM runs ORDER BY kind`);
  expect(runs.map((row) => row.kind)).toEqual([OPERATIONS.explore, OPERATIONS.prepare]);
  const explore = runs[0]!;
  expect(explore.job_id).toBeNull();
  expect(explore.container_id).toBe("ctr_workbench");
  expect(explore.prepare_job_id).toBe(sealed.jobId);
  expect(JSON.parse(explore.preparation)["recipes"]).toEqual([{ id: "code-health", version: 3 }]);
});

/*
  THE OFFER BEHIND A BLANK COVERAGE CELL POSTS THIS (#330): one lens, scoped to one topic. The
  panel's own test pins the document it sends; this pins what the door then DOES with it, which
  is the half that could quietly select nothing or run the default set instead of the lens asked
  for — a cell reporting "never looked" that started a different look would be worse than the
  cell that could not be acted on at all.
*/
test("explore-topic prepares the sessions the topic's records cite, under the one lens asked for", async () => {
  const entityId = "ent_0000beef";
  const { db } = harness;
  // A session on this machine that nothing filed cites: it is in the window and out of scope.
  await insert(db, "sessions", {
    selector: "omp/elsewhere",
    host: MACHINE,
    harness: "omp",
    source_id: "elsewhere",
    title: "another subject",
    content_digest: "d2",
    seen_at: stamp(NOW - HOUR),
  });
  await insert(db, "entities", {
    id: entityId,
    kind: "repository",
    name: "babel",
    canonical_id: entityId,
    created_by: "operator",
    created_at: stamp(NOW - 4 * HOUR),
  });
  await insert(db, "records", {
    id: RECORD,
    kind: "finding",
    root_id: RECORD,
    seq: 1,
    run_id: "run-old",
    actor_kind: "run",
    actor_id: "run-old",
    title: "the tests were adjusted to the code",
    created_at: stamp(NOW - 3 * HOUR),
    payload: JSON.stringify({ schema: 1 }),
  });
  await insert(db, "edges", {
    id: "edg_0330",
    kind: "cites",
    from_kind: "finding",
    from_id: RECORD,
    to_kind: "session",
    to_id: "omp/s1",
    actor_kind: "run",
    actor_id: "run-old",
    created_at: stamp(NOW - 3 * HOUR),
  });
  await insert(db, "filings", {
    id: "fil_0330",
    record_id: RECORD,
    entity_id: entityId,
    rationale: "it is about this repository",
    author_kind: "run",
    author_id: "run-old",
    created_at: stamp(NOW - 3 * HOUR),
  });
  cookbook = {
    ...RECIPES,
    "time-and-spend": {
      id: "time-and-spend",
      version: 2,
      title: "Time sinks and token spend",
      body: "Look for where the hours went.",
    },
  };

  const answer = await start({
    preset: "explore-topic",
    entityId,
    recipes: ["time-and-spend"],
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  expect(answer["refused"]).toBeUndefined();
  expect(answer["kind"]).toBe("explore");
  // THE SCOPE IS THE TOPIC'S OWN EVIDENCE: the cited session and not the machine's window.
  const sealed = fleet.executed[0]!;
  expect(JSON.parse(String(sealed.input["input"]))["selectors"]).toEqual(["omp/s1"]);
  // …and the method is the one lens the cell offered, not the enabled default set.
  const runs = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE kind = ?`,
    [OPERATIONS.explore],
  );
  const intent = JSON.parse(String(runs[0]?.preparation)) as Record<string, unknown>;
  expect(intent["recipes"]).toEqual([{ id: "time-and-spend", version: 2 }]);
  expect(intent["entityId"]).toBe(entityId);
});

test("a topic whose cited sessions are not on the chosen machine is refused by name", async () => {
  const answer = await start({
    preset: "explore-topic",
    entityId: "ent_0000beef",
    recipes: ["code-health"],
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  // Nothing is filed under it at all here, which is the same shape as a topic whose evidence
  // lives on another machine: the operator reads why rather than watching a run find nothing.
  expect(String(answer["refused"])).toContain("is cited by anything filed under ent_0000beef");
  expect(fleet.executed).toEqual([]);
});

test("the selection stops at the bytes one preparation may seal, and says how many it left", async () => {
  // Three quarters of the bound apiece: the newest fits, the next does not, and the third
  // does not either. `scan`'s own `size` is the only figure the hub has before the job runs.
  const big = Math.floor(MAX_MATERIAL_BYTES * 0.75);
  for (const [n, at] of [
    ["big1", NOW - 1000],
    ["big2", NOW - 2000],
    ["big3", NOW - 3000],
  ] as const) {
    await insert(harness.db, "sessions", {
      selector: `omp/${n}`,
      host: MACHINE,
      harness: "omp",
      source_id: n,
      title: n,
      content_digest: `d-${n}`,
      size: big,
      seen_at: stamp(at),
    });
  }

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  // The selection was admitted and holds the newest big log plus the one small seeded
  // session, not all four: the bound is on the bytes, not on the count.
  expect(answer["refused"]).toBeUndefined();
  const sealed = fleet.executed[0]!;
  const selectors = JSON.parse(String(sealed.input["input"]))["selectors"] as string[];
  expect(selectors).toEqual(["omp/big1", "omp/s1"]);
  const runs = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE kind = ?`,
    [OPERATIONS.prepare],
  );
  const preparation = JSON.parse(String(runs[0]?.preparation)) as Record<string, number>;
  expect(preparation["overBound"]).toBe(2);
  expect(preparation["bytes"]).toBeLessThanOrEqual(MAX_MATERIAL_BYTES);
});

test("a window offering nothing the lease can hold is refused by name, not as an empty window", async () => {
  // One session larger than the whole bound. The machine would discover this after reading
  // every byte of it; the door says it before a job exists.
  await harness.db.run(`DELETE FROM sessions`);
  await insert(harness.db, "sessions", {
    selector: "omp/huge",
    host: MACHINE,
    harness: "omp",
    source_id: "huge",
    title: "huge",
    content_digest: "d-huge",
    size: MAX_MATERIAL_BYTES + 1,
    seen_at: stamp(NOW - 1000),
  });

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  const refused = String(answer["refused"]);
  expect(refused).toStartWith("material_too_large:");
  expect(refused).toContain("448 MiB");
  expect(refused).not.toContain("has catalogued no session");
  expect(fleet.executed).toEqual([]);
});

test("a drawn preset names the policy-managed lane and posts nothing itself", async () => {
  for (const preset of ["review-backlog", "file-and-tidy"] as const) {
    const answer = await start({ preset, draws: 1 });
    expect(answer["refused"]).toBe(DRAW_MANAGED);
    expect(String(answer["refused"])).toContain("conductor");
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
    id: "run_live",
    kind: OPERATIONS.evaluate,
    machine_id: MACHINE,
    job_id: "job_live",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  await insert(db, "claims", {
    id: "asg_live",
    record_id: RECORD,
    role: "reception",
    lane: "coverage",
    policy_version: "p1",
    job_id: "job_live",
    run_id: "cyc_1",
    fence: 1,
    reserved_cost: 0.0625,
    granted_at: stamp(NOW - HOUR),
    expires_at: stamp(NOW + HOUR),
  });

  const answer = await halt(
    "run_live",
    { operationId: OPERATIONS.evaluate, jobId: "job_live" },
    "it is arguing with itself",
  );

  expect(answer).toEqual({
    runId: "run_live",
    jobId: "job_live",
    machineId: MACHINE,
    closure: "stopped",
  });
  expect(fleet.cancelled).toEqual([
    { kind: "job", machineId: MACHINE, operationId: OPERATIONS.evaluate, jobId: "job_live" },
  ]);
  const run = await store.run("run_live");
  expect(run.run).toMatchObject({ state: "stopped", freshness: "ended" });
  expect(run.receipt).toMatchObject({
    closure: "stopped",
    stoppedBy: "operator",
    reason: "it is arguing with itself",
  });
  // The reservation is released at what it actually spent, so the day's allowance is not held
  // by a worker the operator has just sent home.
  const claim = await db.query<{ outcome: string; actual_cost: number; finished_at: string }>(
    `SELECT outcome, actual_cost, finished_at FROM claims WHERE id = 'asg_live'`,
  );
  expect(claim[0]).toMatchObject({ outcome: "skipped", actual_cost: 0 });
});

test("stop refuses a run that has already ended, and one nobody started", async () => {
  await insert(harness.db, "runs", {
    id: "run_done",
    kind: OPERATIONS.scan,
    machine_id: MACHINE,
    job_id: "job_done",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW),
    closure: "completed",
    records: 0,
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
    id: "run_live",
    kind: OPERATIONS.scan,
    machine_id: MACHINE,
    job_id: "job_live",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  fleet.refusal = "job_not_cancellable";

  const answer = await halt("run_live", { operationId: OPERATIONS.scan, jobId: "job_live" });

  expect(answer["refused"]).toContain("job_not_cancellable");
  expect((await harness.store.run("run_live")).run).toMatchObject({ state: "running" });
});

test("a stop authorized at one job and aimed at another reaches nothing", async () => {
  await insert(harness.db, "runs", {
    id: "run_live",
    kind: OPERATIONS.scan,
    machine_id: MACHINE,
    job_id: "job_live",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
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

test("stopping a Code session cancels it through Code, never through the hub's own jobs verb", async () => {
  await insert(harness.db, "runs", {
    id: "run_session",
    kind: OPERATIONS.explore,
    machine_id: MACHINE,
    job_id: "job_code_1",
    container_id: "ctr_workbench",
    prepare_job_id: "job_code_1_material",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });

  const answer = await halt("run_session", {
    operationId: OPERATIONS.explore,
    jobId: "job_code_1",
  });

  expect(answer).toEqual({
    runId: "run_session",
    jobId: "job_code_1",
    machineId: MACHINE,
    closure: "stopped",
  });
  /*
    THE JOB IS `atyrode.omp`'S AND `ctx.jobs.cancel` IS BOUND TO THE CALLING PLUGIN'S ID, so
    reaching for the hub's verb here would refuse every run of the lane that reaches a model.
    The container on the row is what says which lane this is.
  */
  expect(code.cancelled).toEqual([{ containerId: "ctr_workbench", jobId: "job_code_1" }]);
  expect(fleet.cancelled).toEqual([]);
  expect((await harness.store.run("run_session")).run).toMatchObject({ state: "stopped" });
});

test("stopping a run that is still preparing cancels the preparation and closes the row", async () => {
  /*
    A RUN IS STARTED IN TWO WAKES (#592), so there is a window where the operator's Stop finds
    no session: `job_id` is NULL, the preparation is in flight, and the id the panel could
    name is not a job anywhere. Refusing there — "no job on a machine to stop" — left the
    posting wake free to post the session afterwards, which is the operator pressing stop and
    the account spending anyway.

    So the preparation is cancelled, with the HUB's verb because that job is Babel's own, and
    the row is closed — the row being what `postPrepared` reads.
  */
  await insert(harness.db, "runs", {
    id: "run_preparing",
    kind: OPERATIONS.explore,
    machine_id: MACHINE,
    container_id: "ctr_workbench",
    prepare_job_id: "job_x_material",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });

  // The panel asks at the PREPARATION's node, which is the only job this run has yet.
  const answer = await halt("run_preparing", {
    operationId: OPERATIONS.prepare,
    jobId: "job_x_material",
  });

  expect(answer).toEqual({
    runId: "run_preparing",
    jobId: "",
    machineId: MACHINE,
    closure: "stopped",
  });
  expect(fleet.cancelled).toEqual([
    {
      kind: "job",
      machineId: MACHINE,
      operationId: OPERATIONS.prepare,
      jobId: "job_x_material",
    },
  ]);
  // Nothing was said to Code: there is no session to say it about.
  expect(code.cancelled).toEqual([]);
  expect((await harness.store.run("run_preparing")).run).toMatchObject({ state: "stopped" });
});

/** A verification as the operator posts one: the request plus the node it is authorized at. */
async function check(args: Record<string, unknown> = {}): Promise<Record<string, unknown>> {
  const machineId = typeof args["machineId"] === "string" ? args["machineId"] : MACHINE;
  return await dispatch(ACTIONS.verify, {
    ...args,
    machineId,
    operation: { kind: "operation", machineId, operationId: MACHINE_OPERATIONS.verify },
  });
}

test("a verification posts Babel's own job and asks for the depth the operator chose", async () => {
  const answer = await check({ readData: "10%" });

  const runId = String(answer["runId"]);
  const jobId = String(answer["jobId"]);
  expect(runId).toMatch(/^run_/);
  expect(answer).toEqual({
    runId,
    jobId,
    machineId: MACHINE,
    // Nothing was asked to be restored, so the answer names no snapshot rather than a default.
    snapshotId: "",
  });
  const [posted] = fleet.executed;
  expect(posted?.operationId).toBe(MACHINE_OPERATIONS.verify);
  expect(posted?.machineId).toBe(MACHINE);
  expect(posted?.outputs).toEqual([
    { name: OUTPUT_BINDING, locationId: "atyrode.babel.outputs", components: [jobId] },
  ]);
  expect(JSON.parse(String(posted?.input?.["input"] ?? "null"))).toEqual({
    runId,
    machineId: MACHINE,
    readData: "10%",
  });
  // The run row is what the conductor settles the receipt onto, so the verdict has somewhere
  // to land before the job is posted anywhere.
  expect((await harness.store.run(runId)).run).toMatchObject({
    id: runId,
    kind: MACHINE_OPERATIONS.verify,
    state: "running",
  });
});

test("a restore is built from the catalogued snapshot and digest, never from the request", async () => {
  const snapshot = "c".repeat(64);
  const digest = `sha256:${"a".repeat(64)}`;
  await insert(harness.db, "sessions", {
    selector: "omp/s2",
    host: MACHINE,
    harness: "omp",
    source_id: "s2",
    content_digest: digest,
    snapshot_id: snapshot,
    seen_at: stamp(NOW - HOUR),
  });

  const answer = await check({ session: { selector: "omp/s2" } });

  expect(answer).toMatchObject({ snapshotId: snapshot });
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toEqual({
    runId: answer["runId"],
    machineId: MACHINE,
    readData: false,
    // The machine is told what the HUB recorded: a digest the asker supplied would be a
    // comparison against whatever he believed, which proves nothing about the archive.
    restore: { snapshotId: snapshot, selector: "omp/s2", digest, target: "" },
  });
});

test("a session whose catalogued snapshot is not a restic id is refused, not posted", async () => {
  // `omp/s1` is the seeded row of an imported corpus: `snap-1` is the Go deployment's own
  // spelling, and a job carrying it would fail parsing its input on a machine nobody watches.
  const answer = await check({ session: { selector: "omp/s1" } });

  expect(answer["refused"]).toContain('"snap-1"');
  expect(answer["refused"]).toContain("not a restic snapshot id");
  expect(fleet.executed).toEqual([]);

  // Naming the snapshot to read is the remedy the refusal offers, and it works.
  const named = await check({ session: { selector: "omp/s1", snapshotId: "latest" } });
  expect(named).toMatchObject({ snapshotId: "latest" });
  // The digest column of that row is the Go spelling too, so the catalog is dropped from the
  // comparison rather than the restore being refused: the machine still compares the restored
  // bytes against the snapshot's own.
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toMatchObject({
    restore: { snapshotId: "latest", selector: "omp/s1", digest: "" },
  });
});

test("a verification refuses a session this machine does not hold, and one nobody catalogued", async () => {
  await insert(harness.db, "sessions", {
    selector: "omp/elsewhere",
    host: "m-other-02",
    harness: "omp",
    source_id: "elsewhere",
    snapshot_id: "d".repeat(64),
    seen_at: stamp(NOW - HOUR),
  });

  const wrong = await check({ session: { selector: "omp/elsewhere" } });
  expect(wrong["refused"]).toContain("m-other-02");

  const unknown = await check({ session: { selector: "omp/never-seen" } });
  expect(unknown["refused"]).toBe("no catalogued session omp/never-seen");

  expect(fleet.executed).toEqual([]);
});

test("a verification asked at the wrong node is refused before anything is posted", async () => {
  const answer = await dispatch(ACTIONS.verify, {
    machineId: MACHINE,
    operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.archive },
  });

  expect(answer["refused"]).toContain(MACHINE_OPERATIONS.verify);
  expect(answer["refused"]).toContain(OPERATIONS.archive);
  expect(fleet.executed).toEqual([]);
});

// ------------------------------------- naming the sessions whose logs carry no title (#342)

/**
 * The policy an autonomous lane needs: enabled, and naming the Code profile and machine that
 * will spend. It is the SAME route a review is dispatched over, because it is the only model
 * authorization the operator has given — a titling lane that reached for a second one would
 * be a second spend nobody metered.
 */
const ROUTED = {
  enabled: true,
  perCycleCost: 0.25,
  dailyCost: 2,
  batchSize: 4,
  review: {
    machineId: MACHINE,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
    roleRecipes: {
      reception: "code-health",
      evidence: "code-health",
      challenge: "code-health",
      comparison: "code-health",
      outcome: "code-health",
      relevance: "code-health",
      filing: "code-health",
      backlog: "code-health",
    },
    recipes: [{ id: "code-health", version: 3, body: "Look for the thing that keeps failing." }],
  },
};

/** Installs the routed policy over the `beforeEach` one, which names no route. */
async function route(): Promise<void> {
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "naming the box that spends",
    payload: JSON.stringify(ROUTED),
    recorded_at: stamp(NOW - HOUR),
  });
}

/** One catalogued session with no title of its own, on the machine the route names. */
async function nameless(sourceId: string, over: Record<string, unknown> = {}): Promise<void> {
  await insert(harness.db, "sessions", {
    selector: `codex/${sourceId}`,
    host: MACHINE,
    harness: "codex",
    source_id: sourceId,
    title: null,
    live: 0,
    kind: "operator",
    size: 4096,
    modified_at: stamp(NOW - HOUR),
    seen_at: stamp(NOW - HOUR),
    ...over,
  });
}

/** The launch path the cycle drives, over the same deps the doors were built with. */
let machinery: LaunchMachinery;

test("the untitled sessions are prepared once, as one bounded batch charged to the cycle", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  // Not candidates: a log still being appended, one of Babel's own runs' transcripts, and a
  // session that already has a title. None of the three is work this lane may pay for.
  await nameless("moving", { live: 1 });
  await nameless("babels-own", { kind: "agent" });

  const posted = await machinery.inferTitles(fleet, "cyc_1");

  // ONE `prepare`, over exactly the two, and nothing posted to a model yet: a job input binds
  // a SETTLED output, so the session belongs to the wake this preparation's settlement causes.
  expect(fleet.executed).toHaveLength(1);
  expect(fleet.executed[0]?.operationId).toBe(OPERATIONS.prepare);
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toMatchObject({
    machineId: MACHINE,
    selectors: ["codex/a", "codex/b"],
  });

  const run = (
    await harness.db.query<{ id: string; kind: string; preparation: string; container_id: string }>(
      `SELECT id, kind, preparation, container_id FROM runs WHERE kind = ?`,
      [OPERATIONS.title],
    )
  )[0]!;
  expect(posted).toEqual({ runId: run.id, jobId: fleet.executed[0]?.jobId ?? "" });
  expect(run.container_id).toBe("ctr_workbench");
  expect(JSON.parse(run.preparation)).toMatchObject({
    titles: { selectors: ["codex/a", "codex/b"], reserved: 0.0625 },
  });

  // AND ONE AT A TIME. A second wake finds the batch still in flight and posts nothing, so a
  // cycle that fires every few seconds cannot fan the corpus out across the whole fleet.
  expect(await machinery.inferTitles(fleet, "cyc_2")).toBeNull();
  expect(fleet.executed).toHaveLength(1);
});

test("a deployment at its ceiling names nothing", async () => {
  await route();
  await nameless("a");
  // The day's allowance, already committed by reviews. The ledger is the claims table and this
  // lane reads the same one a draw is admitted against: what the reviews spent is what the
  // titler has left, and 2.0 of 2.0 leaves nothing.
  await insert(harness.db, "claims", {
    id: "clm_spent",
    record_id: RECORD,
    role: "reception",
    lane: "coverage",
    policy_version: "p2",
    run_id: "cyc_earlier",
    fence: 1,
    reserved_cost: 2,
    granted_at: stamp(NOW - HOUR),
    expires_at: stamp(NOW + HOUR),
  });

  const refused = await machinery.inferTitles(fleet, "cyc_1");

  expect(refused).toMatchObject({ refused: expect.stringContaining("daily ceiling 2.0000") });
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
});

test("a cycle that has already committed its own allowance to reviews names nothing", async () => {
  await route();
  await nameless("a");
  // Four reviews at 0.0625 apiece is one cycle's 0.25: the two lanes compete for one
  // allowance rather than each having a private one.
  for (const slot of [0, 1, 2, 3]) {
    await insert(harness.db, "claims", {
      id: `clm_${String(slot)}`,
      record_id: RECORD,
      role: "reception",
      lane: "coverage",
      policy_version: "p2",
      run_id: "cyc_1",
      fence: 1,
      reserved_cost: 0.0625,
      granted_at: stamp(NOW - 60_000),
      expires_at: stamp(NOW + HOUR),
    });
  }

  expect(await machinery.inferTitles(fleet, "cyc_1")).toMatchObject({
    refused: expect.stringContaining("per-cycle ceiling 0.2500"),
  });
  expect(fleet.executed).toEqual([]);

  // The NEXT cycle has its own allowance under a daily ceiling that still has room, so the
  // lane is deferred rather than closed.
  expect(await machinery.inferTitles(fleet, "cyc_2")).toMatchObject({
    jobId: expect.stringContaining("_material"),
  });
});

test("a session already answered is never offered again, and a policy with no route names nothing", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  // `a` was named by an earlier run and `b` was declined by one. Both are answered, and the
  // second half of "inferred once" is that a decline is an answer too.
  await insert(harness.db, "session_titles", {
    selector: "codex/a",
    title: "Retention on dev-01",
    reason: "",
    run_id: "run_title_earlier",
    inferred_at: stamp(NOW - HOUR),
  });
  await insert(harness.db, "session_titles", {
    selector: "codex/b",
    title: "",
    reason: "the log holds one aborted turn",
    run_id: "run_title_earlier",
    inferred_at: stamp(NOW - HOUR),
  });

  expect(await machinery.inferTitles(fleet, "cyc_1")).toBeNull();
  expect(fleet.executed).toEqual([]);

  // AND A DEPLOYMENT THAT NAMED NO PROFILE NAMES NO SESSION. There is one road to a model and
  // the operator has not opened it; a lane that found a second one would be Babel's first
  // credential.
  await nameless("c");
  await insert(harness.db, "policies", {
    version: "p3",
    seq: 3,
    actor_id: "operator",
    reason: "the route is withdrawn",
    payload: JSON.stringify({ enabled: true, perCycleCost: 0.25, dailyCost: 2, batchSize: 4 }),
    recorded_at: stamp(NOW - 60_000),
  });
  expect(await machinery.inferTitles(fleet, "cyc_2")).toBeNull();
  expect(fleet.executed).toEqual([]);
});

/**
 * The preparation a titling run waited on, settled, with the index it sealed — and the run row
 * `inferTitles` left behind, as `postPrepared` finds the pair.
 */
async function preparedTitles(selectors: readonly string[], closure = "completed"): Promise<void> {
  await insert(harness.db, "runs", {
    id: "run_prep_title",
    kind: OPERATIONS.prepare,
    machine_id: MACHINE,
    job_id: "job_title_material",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW - 60_000),
    closure,
    records: 0,
    payload: JSON.stringify({
      runId: "run_prep_title",
      kind: "prepare",
      closure,
      material: {
        schema: "babel.material/1",
        preparationId: "prep-title",
        preparedAt: stamp(NOW - 60_000),
        machineId: MACHINE,
        sessions: selectors.map((selector, index) => ({
          selector,
          harness: "codex",
          sourceId: selector.slice(selector.indexOf("/") + 1),
          captureDigest: "c".repeat(64),
          sourceDigest: "d".repeat(64),
          file: `000${String(index + 1)}-${selector.replace("/", "-")}.jsonl`,
          records: 7,
          bytes: 2048,
        })),
      },
    }),
  });
  await insert(harness.db, "runs", {
    id: "run_title_1",
    kind: OPERATIONS.title,
    machine_id: MACHINE,
    container_id: "ctr_workbench",
    prepare_job_id: "job_title_material",
    profile: JSON.stringify({ containerId: "ctr_workbench", expectedRevision: 7 }),
    preparation: JSON.stringify({ titles: { selectors, reserved: 0.0625 } }),
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null, preparing: "job_title_material" }),
  });
}

test("a settled titling preparation posts a session asking for a title per sealed file", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  await preparedTitles(["codex/a", "codex/b"]);
  const posted: { prompt: string; prepareJobId?: string | undefined }[] = [];
  code.posting = (request) => {
    posted.push({ prompt: request.prompt, prepareJobId: request.prepareJobId });
    return {
      ok: true,
      value: {
        jobId: "job_code_title",
        machineId: MACHINE,
        operationId: "atyrode.omp.session",
        pluginId: "atyrode.omp",
        state: "started",
      },
    };
  };

  const answers = await machinery.postPrepared(fleet, code, {
    metered: {},
    limits: { timeoutMs: 1, memoryBytes: 1, processes: 1, outputBytes: 1 },
  });

  expect(answers).toEqual([{ runId: "run_title_1", jobId: "job_code_title" }]);
  const prompt = posted[0]?.prompt ?? "";
  // The session is bound to the material its own preparation sealed, and the prompt names
  // each selector against the file the index actually wrote.
  expect(posted[0]?.prepareJobId).toBe("job_title_material");
  expect(prompt).toContain("codex/a — `sessions/0001-codex-a.jsonl`");
  expect(prompt).toContain("codex/b — `sessions/0002-codex-b.jsonl`");
  // It asks for titles and nothing else: no cookbook recipe, no evidence contract, no record
  // vocabulary — none of which a title needs and every one of which would invite an answer
  // this lane refuses to write.
  expect(prompt).toContain("# Babel session titles");
  expect(prompt).not.toContain(RECIPES["code-health"]?.body ?? "unreachable");
  expect(prompt).not.toContain("candidates");

  // The job id lands on the row, which is what the conductor reconciles the session through.
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = 'run_title_1'`)).toEqual([
    { job_id: "job_code_title" },
  ]);
});

test("a titling preparation that failed answers its sessions rather than leaving them for the next cycle", async () => {
  await route();
  await nameless("a");
  await preparedTitles(["codex/a"], "failed");

  const answers = await machinery.postPrepared(fleet, code, {
    metered: {},
    limits: { timeoutMs: 1, memoryBytes: 1, processes: 1, outputBytes: 1 },
  });

  expect(answers).toEqual([
    { runId: "run_title_1", refused: "the preparation job_title_material closed as failed" },
  ]);
  expect(await harness.db.query(`SELECT selector, title, reason FROM session_titles`)).toEqual([
    {
      selector: "codex/a",
      title: "",
      reason: "the preparation job_title_material closed as failed",
    },
  ]);
  // AND SO THE NEXT CYCLE ASKS FOR NOTHING. Without the row above this lane would post another
  // preparation over the same session on every wake, for ever.
  expect(await machinery.inferTitles(fleet, "cyc_2")).toBeNull();
  expect(fleet.executed).toEqual([]);
});
