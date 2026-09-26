/*
  The three doors Watch posts to, held to what they do to the world.

  Every test dispatches the way the kit does — parse the arguments against the action's own
  input, run the handler, parse what it produced against the action's own result — and then
  asks the STORE and the FLEET what happened.

  A launch names a Code profile, selects sessions and posts a preparation to seal the material.
  `postPrepared` waits for that job to settle before asking Code to post a session with the
  material bound. These tests distinguish a refused launch, a preparation in flight and the
  later session posting; none may be recorded as another.

  Stop preserves terminal Code accounting and holds reservations until cancellation settles.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import { PluginManifestSchema } from "@manifold/protocol";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { HostCallError } from "@manifold/plugin-kit/errors";
import {
  ACTIONS,
  MACHINE_OPERATIONS,
  MATERIAL_HEADROOM_BYTES,
  MATERIAL_OUTPUT,
  OPERATIONS,
  MAX_MATERIAL_BYTES,
  OUTPUT_BINDING,
  PREPARE_INPUT_MAX_BYTES,
  PRESET_OPERATIONS,
  PrepareInputSchema,
  type AnalysisWork,
  type ProfileRow,
  ENGINE_REFUSALS,
} from "../contract.ts";
import type { JobLaunch, JobRef, JobRunState, MachineReadiness } from "../server/conductor.ts";
import { runPlan, type BabelJobs } from "../server/plan.ts";
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
import manifestJson from "../manifest.json";
import type { Door } from "./door.ts";
import {
  DRAW_MANAGED,
  launchDoors,
  launchMachinery,
  type LaunchDeps,
  type LaunchMachinery,
} from "./launch.ts";

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";
const RECORD = "fnd_00000001";
/** The beat: `keep-going`'s operation, whichever job the contract names for it. */
const BEAT = PRESET_OPERATIONS["keep-going"];

/** The machine, as the engine describes one that can run Babel. */
const READY: MachineReadiness = {
  connected: true,
  operations: { [BEAT]: { ready: true, reason: null } },
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
 * CODE, as this door reaches it. A launch must not post a session before preparation settles;
 * tests of `postPrepared` explicitly supply the posting response for that later transition.
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

  checkResult: EngineAnswer<null> = { ok: true, value: null };

  async checkProfile(): Promise<EngineAnswer<null>> {
    return await Promise.resolve(this.checkResult);
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

  /** What a Stop reaches for on a Code session. */
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
let deps: LaunchDeps;

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

/** A 64-hex restic snapshot id, as the catalog records one. */
const SNAPSHOT = "5".repeat(64);

/**
 * ONE CATALOGUED SESSION NAMING AN ARCHIVED CAPTURE (#453): the snapshot and path that hold it,
 * the label it was taken under and the observation the catalog recorded, in
 * `CaptureInstantSchema`'s one spelling. It is the only row a preparation can read; `over`
 * shapes the rest, including a `host` the selection never reads.
 */
async function archived(selector: string, over: Record<string, unknown> = {}): Promise<void> {
  const cut = selector.indexOf("/");
  const sourceId = selector.slice(cut + 1);
  await insert(harness.db, "sessions", {
    selector,
    host: MACHINE,
    harness: selector.slice(0, cut),
    source_id: sourceId,
    kind: "operator",
    live: 0,
    archive_label: "dev-01",
    archive_path: `/home/alex/.omp/agent/sessions/${sourceId}.jsonl`,
    snapshot_id: SNAPSHOT,
    archived_at: new Date(NOW - HOUR).toISOString(),
    modified_at: new Date(NOW - 2 * HOUR).toISOString(),
    size: 1000,
    seen_at: stamp(NOW - HOUR),
    ...over,
  });
}

/** The sessions one posted preparation was handed, parsed as the machine parses its input. */
function handed(job: JobLaunch | undefined): string[] {
  const input = PrepareInputSchema.parse(JSON.parse(String(job?.input["input"] ?? "null")));
  return input.captures.flatMap((group) =>
    group.sessions.map((session) => `${session.harness}/${session.sourceId}`),
  );
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
  await archived("omp/s1", { title: "yesterday", content_digest: `sha256:${"1".repeat(64)}` });
  code = new Code();
  cookbook = { ...RECIPES };
  deps = {
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

  // A launch posts Babel's OWN `prepare` or `catalog` job and asks Code to post the session, so
  // it keeps the delegates that posting needs — reading the job back, the locations the sealed
  // leases are cut from, the machine read `ready` describes with before anything is posted, and
  // the `machines:run` the posting itself is discharged against (#448) — and names no governed
  // node, because the operations a requirement would name (`explore`, `evaluate`) are declared
  // by nobody.
  expect(launch?.action.caps).toEqual(["containers:read"]);
  expect(launch?.action.requirements).toBeUndefined();
  expect(launch?.action.delegates).toEqual([
    "jobs:read",
    "locations:read",
    "locations:write",
    "machines:read",
    "machines:run",
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
    "machines:run",
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

/*
  NOTHING IS SEALED FOR A PROFILE THAT CANNOT PAY FOR THE SESSION (#255).

  The session this press leads to is posted two wakes later and is gated there too — every
  posting in this bundle goes through `codeEngine.runSession`. What this pins is the PRESS: an
  `atyrode.babel.prepare` job is half a gigabyte and thirty minutes of a real machine's time,
  and a deployment that never installed an account must not spend it to be told so afterwards.
  The assertion is therefore what the FLEET was asked to run, not what the door returned.
*/
test("an account-check refusal prevents preparation and creates no run", async () => {
  code.checkResult = refusedByCode("engine_no_account", "ctr_workbench has no resolved account");

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
  expect(String(answer["refused"])).toStartWith("engine_no_account:");
  expect(String(answer["refused"])).toContain("ctr_workbench");
});

test("a hub holding no cookbook recipe refuses an explore rather than posting one with no method", async () => {
  cookbook = {};
  code.checkProfile = () =>
    Promise.reject(new Error("local eligibility must be checked before querying Code"));
  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  expect(answer).toHaveProperty("refused");
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
  // It is handed the capture, by snapshot and path, and nothing it would have to discover.
  expect(handed(sealed)).toEqual(["omp/s1"]);
  expect(JSON.parse(String(sealed.input["input"]))["captures"]).toEqual([
    {
      snapshotId: SNAPSHOT,
      label: "dev-01",
      sessions: [
        {
          harness: "omp",
          sourceId: "s1",
          path: "/home/alex/.omp/agent/sessions/s1.jsonl",
          size: 1000,
          modifiedAt: NOW - 2 * HOUR,
        },
      ],
    },
  ]);

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

test("reviewed limits survive the preparation wake and still govern the posted session", async () => {
  const inferenceLimits = { calls: 2, inputTokens: 10_000, costMicros: 50_000 };
  const answer = await start({
    preset: "read-whats-new",
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
    inferenceLimits,
  });
  const runId = String(answer["runId"]);
  const prepareJobId = String(answer["jobId"]);
  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
  await sealStage("completed", prepareJobId);
  let posts = 0;
  code.posting = (request) => {
    posts += 1;
    if (JSON.stringify(request.inferenceLimits) !== JSON.stringify(inferenceLimits))
      return refusedByCode("engine_forbidden", "the reviewed inference bounds changed");
    return stageJob();
  };
  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([
    { runId, jobId: "job_stage_code" },
  ]);
  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
  expect(posts).toBe(1);
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = ?`, [runId])).toEqual([
    { job_id: "job_stage_code" },
  ]);
});

test("an explicit explore posts its preparation within prepare's own declared ceiling", async () => {
  /*
    THE LIMITS THE HUB JUDGES A POSTING AGAINST ARE THE MANIFEST'S (#449). This file's fixed plan
    answers every operation alike, which is how an explore press planned for the undeclared
    `atyrode.babel.explore` went unseen: the production plan fell back to `DEFAULT_LIMITS` for it
    — an hour and 2 GiB — and posted the preparation under them, above `prepare`'s declared
    thirty minutes and 1 GiB, so the hub refused every one `limit_exceeded`. So this press is
    planned by `runPlan` over the shipped manifest, the way `server.ts` plans a real one.
  */
  const manifest = PluginManifestSchema.parse(manifestJson);
  doors = launchDoors(harness.store, {
    ...deps,
    plan: (policy, operationId) => runPlan({ manifest, policy, operationId }),
  });
  const answer = await start({
    preset: "read-whats-new",
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  expect(answer["refused"]).toBeUndefined();
  const [preparation] = fleet.executed;
  expect(preparation?.operationId).toBe(OPERATIONS.prepare);
  const ceiling = manifest.machine?.operations[OPERATIONS.prepare]?.limits;
  expect(ceiling).toBeDefined();
  const limits = preparation?.limits;
  expect(limits?.timeoutMs ?? Infinity).toBeLessThanOrEqual(ceiling?.timeoutMs ?? 0);
  expect(limits?.memoryBytes ?? Infinity).toBeLessThanOrEqual(ceiling?.memoryBytes ?? 0);
  expect(limits?.processes ?? Infinity).toBeLessThanOrEqual(ceiling?.processes ?? 0);
  expect(limits?.outputBytes ?? Infinity).toBeLessThanOrEqual(ceiling?.outputBytes ?? 0);
});

test.each(["before-wake", "before-claim"] as const)(
  "%s disablement leaves an ordinary prepared session unposted until re-enabled",
  async (timing) => {
    const answer = await start({
      preset: "read-whats-new",
      profile: { containerId: "ctr_workbench", expectedRevision: 7 },
    });
    const runId = String(answer["runId"]);
    await sealStage("completed", String(answer["jobId"]));
    const activation = async (enabled: boolean, seq: number) =>
      await insert(harness.db, "policies", {
        version: `activation-${String(seq)}`,
        seq,
        actor_id: "operator",
        reason: "changing activation",
        payload: JSON.stringify({ enabled, perCycleCost: 0.25, batchSize: 4, dailyCost: 2 }),
        recorded_at: stamp(NOW),
      });
    let posts = 0;
    code.posting = () => {
      posts += 1;
      return stageJob();
    };
    const batch = harness.db.batch.bind(harness.db);
    let pauseAtWrite = timing === "before-claim";
    harness.db.batch = async (statements) => {
      if (pauseAtWrite) {
        pauseAtWrite = false;
        await activation(false, 2);
      }
      return await batch(statements);
    };
    try {
      if (timing === "before-wake") await activation(false, 2);
      expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
      expect(posts).toBe(0);
    } finally {
      harness.db.batch = batch;
    }
    await activation(true, 3);
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([
      { runId, jobId: "job_stage_code" },
    ]);
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
    expect(posts).toBe(1);
  },
);

test("a malformed persisted inference bound refuses before buying an unbounded session", async () => {
  const answer = await start({
    preset: "read-whats-new",
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
    inferenceLimits: { calls: 2 },
  });
  const runId = String(answer["runId"]);
  await sealStage("completed", String(answer["jobId"]));
  await harness.db.run(
    `UPDATE runs SET profile = json_set(profile, '$.inferenceLimits.calls', 'two') WHERE id = ?`,
    [runId],
  );
  let posts = 0;
  code.posting = () => {
    posts += 1;
    return stageJob();
  };
  expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
  expect(posts).toBe(0);
  expect(await harness.db.query(`SELECT closure FROM runs WHERE id = ?`, [runId])).toEqual([
    { closure: "failed" },
  ]);
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
  // An archived session that nothing filed cites: it is in the window and out of scope.
  await archived("omp/elsewhere", {
    title: "another subject",
    modified_at: new Date(NOW - HOUR).toISOString(),
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
  expect(handed(sealed)).toEqual(["omp/s1"]);
  // …and the method is the one lens the cell offered, not the enabled default set.
  const runs = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE kind = ?`,
    [OPERATIONS.explore],
  );
  const intent = JSON.parse(String(runs[0]?.preparation)) as Record<string, unknown>;
  expect(intent["recipes"]).toEqual([{ id: "time-and-spend", version: 2 }]);
  expect(intent["entityId"]).toBe(entityId);
});

test("a topic whose cited sessions name no archived capture is refused by name", async () => {
  // Cited, catalogued, and not in the archive yet: an imported row the catalog has not listed.
  await insert(harness.db, "sessions", {
    selector: "omp/imported",
    host: "dev-01",
    harness: "omp",
    source_id: "imported",
    seen_at: stamp(NOW - HOUR),
  });
  await insert(harness.db, "records", {
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
  await insert(harness.db, "edges", {
    id: "edg_imported",
    kind: "cites",
    from_kind: "finding",
    from_id: RECORD,
    to_kind: "session",
    to_id: "omp/imported",
    actor_kind: "run",
    actor_id: "run-old",
    created_at: stamp(NOW - 3 * HOUR),
  });
  await insert(harness.db, "filings", {
    id: "fil_imported",
    record_id: RECORD,
    entity_id: "ent_0000beef",
    rationale: "it is about this repository",
    author_kind: "run",
    author_id: "run-old",
    created_at: stamp(NOW - 3 * HOUR),
  });
  const answer = await start({
    preset: "explore-topic",
    entityId: "ent_0000beef",
    recipes: ["code-health"],
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  // The operator reads why — the one cited session is not archived — rather than watching a
  // run find nothing, or a preparation be handed a session it has no capture to read.
  expect(String(answer["refused"])).toContain(
    "no archived session is cited by anything filed under ent_0000beef (1 of 1 catalogued",
  );
  expect(fleet.executed).toEqual([]);
});

test("the selection reads archived captures from every label, newest written first", async () => {
  // Another machine's capture is selectable: any machine holding the archive binding reads it.
  await archived("omp/other-host", {
    host: "m-other-02",
    archive_label: "workstation-linux",
    snapshot_id: "6".repeat(64),
    modified_at: new Date(NOW - HOUR).toISOString(),
  });
  // Catalogued a minute ago, written a week ago: `seen_at` is the catalog's clock, not the
  // session's, so it is outside a one-day window.
  await archived("omp/old", {
    modified_at: new Date(NOW - 7 * 24 * HOUR).toISOString(),
    seen_at: stamp(NOW - 60_000),
  });
  // Recent, and naming no capture: catalogued and not something a preparation can read.
  await insert(harness.db, "sessions", {
    selector: "omp/imported",
    host: MACHINE,
    harness: "omp",
    source_id: "imported",
    modified_at: new Date(NOW - 30 * 60_000).toISOString(),
    seen_at: stamp(NOW - 60_000),
  });

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  expect(answer["refused"]).toBeUndefined();
  const sealed = fleet.executed[0]!;
  expect(handed(sealed)).toEqual(["omp/other-host", "omp/s1"]);
  // One group per snapshot, each with the label that took it.
  expect(
    PrepareInputSchema.parse(JSON.parse(String(sealed.input["input"]))).captures.map((group) => [
      group.label,
      group.snapshotId,
    ]),
  ).toEqual([
    ["workstation-linux", "6".repeat(64)],
    ["dev-01", SNAPSHOT],
  ]);
  const runs = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE kind = ?`,
    [OPERATIONS.explore],
  );
  expect(JSON.parse(String(runs[0]?.preparation))).toMatchObject({
    selected: 2,
    available: 3,
    excluded: 1,
  });
});

test("a long window stops at the prepare input bound and counts the rest", async () => {
  // Paths near the contract's own 4096-character ceiling: 120 of them cannot share one input.
  for (let n = 0; n < 120; n++) {
    const name = `long-${String(n).padStart(3, "0")}`;
    await archived(`omp/${name}`, {
      archive_path: `/home/alex/.omp/agent/sessions/${"x".repeat(3000)}/${name}.jsonl`,
      modified_at: new Date(NOW - HOUR - n * 1000).toISOString(),
    });
  }

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  expect(answer["refused"]).toBeUndefined();
  const sealed = fleet.executed[0]!;
  // Under the bound as the engine counts it — the whole encoded input record — and still a
  // document the machine parses.
  expect(new TextEncoder().encode(JSON.stringify(sealed.input)).byteLength).toBeLessThanOrEqual(
    PREPARE_INPUT_MAX_BYTES,
  );
  const taken = handed(sealed);
  expect(taken.length).toBeGreaterThan(0);
  expect(taken[0]).toBe("omp/long-000");
  const runs = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE kind = ?`,
    [OPERATIONS.prepare],
  );
  const preparation = JSON.parse(String(runs[0]?.preparation)) as Record<string, number>;
  // The window's 120 rows are the newest 120 of 121; every one not taken is counted.
  expect(preparation["selected"]).toBe(taken.length);
  expect(preparation["selected"]! + preparation["overBound"]!).toBe(120);
});

test("the material bound is the machine's measured scratch, shared by the lane's fan", async () => {
  // The newest receipt from this machine measured 64 MiB of headroom plus 10 MiB.
  await insert(harness.db, "runs", {
    id: "run_catalog_earlier",
    kind: BEAT,
    machine_id: MACHINE,
    job_id: "job_catalog_earlier",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW - HOUR),
    closure: "completed",
    records: 0,
    payload: JSON.stringify({
      closure: "completed",
      outputCapacity: { bytes: MATERIAL_HEADROOM_BYTES + 10 * 1024 * 1024, free: 0 },
    }),
  });
  await archived("omp/four", {
    size: 4 * 1024 * 1024,
    modified_at: new Date(NOW - HOUR).toISOString(),
  });
  const launch = async (runId: string, materials?: number) =>
    await machinery.startExplore(
      { runId, jobId: `job_${runId}`, authorityId: "operator" },
      fleet,
      code,
      {
        preset: "read-whats-new",
        machineId: MACHINE,
        sinceDays: 1,
        profile: { containerId: "ctr_workbench", expectedRevision: 7 },
        recipes: [],
      },
      { ...ANALYSIS_PLAN, ...(materials === undefined ? {} : { materials }) },
    );

  // One material alone may hold the 10 MiB: the 4 MiB capture and the small one both fit.
  expect(await launch("run_one")).toHaveProperty("jobId");
  expect(handed(fleet.executed[0])).toEqual(["omp/four", "omp/s1"]);
  // A fan of three shares it at ⌊10 MiB / 3⌋ each: the 4 MiB capture is left out and counted.
  expect(await launch("run_fan", 3)).toHaveProperty("jobId");
  expect(handed(fleet.executed[1])).toEqual(["omp/s1"]);
  const fan = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE id = 'run_fan'`,
  );
  expect(JSON.parse(String(fan[0]?.preparation))).toMatchObject({ selected: 1, overBound: 1 });
});

test("the selection stops at the bytes one preparation may seal, and says how many it left", async () => {
  // Three quarters of the bound apiece: the newest fits, the next does not, and the third
  // does not either. The catalogued `size` is the only figure the hub has before the job runs.
  const big = Math.floor(MAX_MATERIAL_BYTES * 0.75);
  for (const [n, at] of [
    ["big1", NOW - 1000],
    ["big2", NOW - 2000],
    ["big3", NOW - 3000],
  ] as const) {
    await archived(`omp/${n}`, { title: n, size: big, modified_at: new Date(at).toISOString() });
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
  expect(handed(sealed)).toEqual(["omp/big1", "omp/s1"]);
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
  await archived("omp/huge", {
    size: MAX_MATERIAL_BYTES + 1,
    modified_at: new Date(NOW - 1000).toISOString(),
  });

  const answer = await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  const refused = String(answer["refused"]);
  expect(refused).toStartWith("material_too_large:");
  expect(refused).toContain(`${String(MAX_MATERIAL_BYTES / (1024 * 1024))} MiB`);
  expect(refused).not.toContain("holds no session");
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
  expect(beat.operationId).toBe(BEAT);
  // The beat is told the machine and its run, and nothing it would have to be told again.
  expect(JSON.parse(String(beat.input["input"]))).toEqual({
    runId: answer["runId"],
    machineId: MACHINE,
  });
  // The operator's own bound on the beat, under the operation's ceiling.
  expect(beat.limits?.timeoutMs).toBe(30 * 60_000);
  const runs = await harness.db.query<{ id: string; kind: string }>(`SELECT id, kind FROM runs`);
  expect(runs).toHaveLength(1);
  expect(runs[0]?.kind).toBe(BEAT);
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
  await insert(db, "run_progress", {
    run_id: "run_live",
    job_id: "job_live",
    stage: "reading",
    message: "Still reading material",
    since: stamp(NOW - HOUR),
    updated_at: stamp(NOW),
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
  expect(run.run?.progress).toBeNull();
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
    kind: BEAT,
    machine_id: MACHINE,
    job_id: "job_done",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW),
    closure: "completed",
    records: 0,
    payload: JSON.stringify({ closure: "completed" }),
  });

  expect((await halt("run_done", { operationId: BEAT, jobId: "job_done" }))["refused"]).toContain(
    "already ended",
  );
  expect(
    (await halt("run_nothing", { operationId: BEAT, jobId: "job_none" }))["refused"],
  ).toContain("no run run_nothing");
  expect(fleet.cancelled).toEqual([]);
});

test("a machine that refuses to stop leaves the run open rather than lying about it", async () => {
  await insert(harness.db, "runs", {
    id: "run_live",
    kind: BEAT,
    machine_id: MACHINE,
    job_id: "job_live",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  fleet.refusal = "job_not_cancellable";

  const answer = await halt("run_live", { operationId: BEAT, jobId: "job_live" });

  expect(answer["refused"]).toContain("job_not_cancellable");
  expect((await harness.store.run("run_live")).run).toMatchObject({ state: "running" });
});

test("a stop authorized at one job and aimed at another reaches nothing", async () => {
  await insert(harness.db, "runs", {
    id: "run_live",
    kind: BEAT,
    machine_id: MACHINE,
    job_id: "job_live",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  const elsewhere = await halt("run_live", {
    machineId: "m-other",
    operationId: BEAT,
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

async function stoppableSession(): Promise<void> {
  await insert(harness.db, "runs", {
    id: "run_session",
    kind: OPERATIONS.explore,
    machine_id: MACHINE,
    job_id: "job_code_1",
    container_id: "ctr_workbench",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  await insert(harness.db, "claims", {
    id: "asg_session",
    record_id: RECORD,
    role: "reception",
    lane: "coverage",
    policy_version: "p1",
    job_id: "job_code_1",
    run_id: "cyc_1",
    fence: 1,
    reserved_cost: 0.0625,
    granted_at: stamp(NOW - HOUR),
    expires_at: stamp(NOW + HOUR),
  });
  await insert(harness.db, "run_progress", {
    run_id: "run_session",
    job_id: "job_code_1",
    stage: "reading",
    message: "Still reading material",
    since: stamp(NOW - HOUR),
    updated_at: stamp(NOW),
    cost_usd: 0.025,
  });
}

function chargedSession(): CodeJob {
  return {
    jobId: "job_code_1",
    machineId: MACHINE,
    operationId: "atyrode.omp.session",
    pluginId: "atyrode.omp",
    state: "cancelled",
    result: {
      jobId: "job_code_1",
      requestDigest: "d".repeat(64),
      ownerId: "owner",
      ownerGeneration: 1,
      state: "cancelled",
      exitCode: null,
      reason: null,
      startedAt: NOW - HOUR,
      finishedAt: NOW,
      usage: {
        elapsedMs: HOUR,
        memoryBytes: 0,
        processes: 1,
        outputBytes: 0,
        inference: {
          calls: 3,
          inputTokens: 20_000,
          outputTokens: 1_500,
          cachedInputTokens: 5_000,
          costMicros: 410_000,
        },
      },
      limits: { timeoutMs: HOUR, memoryBytes: 1024, processes: 1, outputBytes: 1024 },
      outputs: [],
    },
  };
}

test("Stop racing a charged terminal Code job keeps its meter and charges the full overrun", async () => {
  await stoppableSession();
  const terminal = chargedSession();
  code.cancelSession = async () => ({ ok: true, value: terminal });

  expect(
    await halt("run_session", { operationId: OPERATIONS.explore, jobId: "job_code_1" }),
  ).toMatchObject({ closure: "stopped" });

  const result = await harness.store.run("run_session");
  expect(result.run).toMatchObject({
    state: "stopped",
    costUsd: 0.41,
    tokens: 21_500,
    calls: 3,
    progress: null,
  });
  expect(result.receipt).toMatchObject({ inference: terminal.result?.usage?.inference });
  expect(
    await harness.db.query(`SELECT actual_cost, outcome FROM claims WHERE id = 'asg_session'`),
  ).toEqual([{ actual_cost: 0.41, outcome: "skipped" }]);
  expect(await coordinator(harness.store, () => NOW, 16).spend()).toMatchObject({ total: 0.41 });
});

test.each([0, null])(
  "Stop preserves a completed session with exit code %p for receipt ingestion",
  async (exitCode) => {
    await stoppableSession();
    const terminal = chargedSession();
    if (!terminal.result) throw new Error("charged fixture needs a terminal result");
    const completed: CodeJob = {
      ...terminal,
      state: "exited",
      result: { ...terminal.result, state: "exited", exitCode },
    };
    code.cancelSession = async () => ({ ok: true, value: completed });
    expect(
      await halt("run_session", { operationId: OPERATIONS.explore, jobId: "job_code_1" }),
    ).toHaveProperty("refused");
    expect((await harness.store.run("run_session")).run?.state).toBe("running");
    expect(
      await harness.db.query(
        `SELECT actual_cost, finished_at FROM claims WHERE id = 'asg_session'`,
      ),
    ).toEqual([{ actual_cost: null, finished_at: null }]);
  },
);

test("Stop without a terminal Code meter charges the reservation instead of claiming free work", async () => {
  await stoppableSession();

  expect(
    await halt("run_session", { operationId: OPERATIONS.explore, jobId: "job_code_1" }),
  ).toMatchObject({ closure: "stopped" });

  const result = await harness.store.run("run_session");
  expect(result.run).toMatchObject({ state: "stopped", costUsd: null, tokens: null, calls: null });
  expect(result.receipt).not.toHaveProperty("inference");
  expect(
    await harness.db.query(`SELECT actual_cost, outcome FROM claims WHERE id = 'asg_session'`),
  ).toEqual([{ actual_cost: 0.0625, outcome: "skipped" }]);
  expect(await coordinator(harness.store, () => NOW, 16).spend()).toMatchObject({ total: 0.0625 });
});

test("a live Code cancellation acknowledgement keeps the run and reservation reachable until settlement", async () => {
  await stoppableSession();
  const terminal = chargedSession();
  code.cancelSession = async () => ({
    ok: true,
    value: { ...terminal, state: "started", result: null },
  });

  expect(
    await halt("run_session", { operationId: OPERATIONS.explore, jobId: "job_code_1" }),
  ).toHaveProperty("refused");
  expect((await harness.store.run("run_session")).run).toMatchObject({
    state: "running",
    progress: { costUsd: 0.025 },
  });
  expect(
    await harness.db.query(`SELECT actual_cost, finished_at FROM claims WHERE id = 'asg_session'`),
  ).toEqual([{ actual_cost: null, finished_at: null }]);

  code.cancelSession = async () => ({ ok: true, value: terminal });
  await halt("run_session", { operationId: OPERATIONS.explore, jobId: "job_code_1" });
  expect((await harness.store.run("run_session")).run).toMatchObject({ costUsd: 0.41, calls: 3 });
  expect(await harness.db.query(`SELECT actual_cost FROM claims WHERE id = 'asg_session'`)).toEqual(
    [{ actual_cost: 0.41 }],
  );
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

test("a restore is built from the catalogued capture and digest, never from the request", async () => {
  const snapshot = "c".repeat(64);
  const digest = `sha256:${"a".repeat(64)}`;
  // Catalogued under another machine's label: any machine holding the binding reads it (#453).
  await archived("omp/s2", { host: "m-other-02", content_digest: digest, snapshot_id: snapshot });

  const answer = await check({ session: { selector: "omp/s2" } });

  expect(answer).toMatchObject({ snapshotId: snapshot });
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toEqual({
    runId: answer["runId"],
    machineId: MACHINE,
    readData: false,
    // The machine is told what the HUB recorded: a digest the asker supplied would be a
    // comparison against whatever he believed, which proves nothing about the archive. The
    // catalogued path lets it list that path rather than the whole snapshot.
    restore: {
      snapshotId: snapshot,
      selector: "omp/s2",
      digest,
      target: "",
      path: "/home/alex/.omp/agent/sessions/s2.jsonl",
    },
  });

  // A snapshot the operator names instead is listed whole: the catalogued path is another
  // capture's.
  await check({ session: { selector: "omp/s2", snapshotId: "latest" } });
  expect(JSON.parse(String(fleet.executed[1]?.input?.["input"] ?? "null"))["restore"]).toEqual({
    snapshotId: "latest",
    selector: "omp/s2",
    digest,
    target: "",
  });
});

test("a session whose catalogued snapshot is not a restic id is refused, not posted", async () => {
  // An imported row: `snap-1` is the Go deployment's own spelling, and a job carrying it would
  // fail parsing its input on a machine nobody watches.
  await insert(harness.db, "sessions", {
    selector: "omp/imported",
    host: "dev-01",
    harness: "omp",
    source_id: "imported",
    content_digest: "d1",
    snapshot_id: "snap-1",
    seen_at: stamp(NOW - HOUR),
  });
  const answer = await check({ session: { selector: "omp/imported" } });

  expect(answer["refused"]).toContain('"snap-1"');
  expect(answer["refused"]).toContain("not a restic snapshot id");
  expect(fleet.executed).toEqual([]);

  // Naming the snapshot to read is the remedy the refusal offers, and it works.
  const named = await check({ session: { selector: "omp/imported", snapshotId: "latest" } });
  expect(named).toMatchObject({ snapshotId: "latest" });
  // The digest column of that row is the Go spelling too, so the catalog is dropped from the
  // comparison rather than the restore being refused: the machine still compares the restored
  // bytes against the snapshot's own.
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toMatchObject({
    restore: { snapshotId: "latest", selector: "omp/imported", digest: "" },
  });
});

test("a verification refuses a session nobody catalogued", async () => {
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

/** One archived session with no title of its own. */
async function nameless(sourceId: string, over: Record<string, unknown> = {}): Promise<void> {
  await archived(`codex/${sourceId}`, {
    title: null,
    size: 4096,
    modified_at: new Date(NOW - HOUR).toISOString(),
    ...over,
  });
}

/** The launch path the cycle drives, over the same deps the doors were built with. */
let machinery: LaunchMachinery;

test("zero autonomous weights suppress titling without blocking an explicit exploration", async () => {
  await route();
  await nameless("manual-only");
  await insert(harness.db, "policies", {
    version: "manual-only",
    seq: 3,
    actor_id: "operator",
    reason: "explicit runs only",
    payload: JSON.stringify({
      ...ROUTED,
      activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0 },
    }),
    recorded_at: stamp(NOW),
  });

  expect(await machinery.inferTitles(fleet, code, "cyc_manual")).toBeNull();
  expect(fleet.executed).toEqual([]);

  await start({
    preset: "read-whats-new",
    sinceDays: 1,
    profile: ROUTED.review.profile,
  });
  expect(fleet.executed.map((job) => job.operationId)).toEqual([OPERATIONS.prepare]);
});

test("a refused titling profile leaves the batch unprepared and available after correction", async () => {
  await route();
  await nameless("retry");
  code.checkResult = refusedByCode("engine_no_account", "no account selected");

  const refused = await machinery.inferTitles(fleet, code, "cyc_1");
  expect(refused).toMatchObject({ refused: expect.stringMatching(/^engine_no_account:/u) });
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM runs WHERE kind = ?`, [OPERATIONS.title])).toEqual(
    [],
  );
  expect(await harness.db.query(`SELECT selector FROM session_titles`)).toEqual([]);

  code.checkResult = { ok: true, value: null };
  await machinery.inferTitles(fleet, code, "cyc_2");
  expect(fleet.executed).toHaveLength(1);
  expect(fleet.executed[0]?.operationId).toBe(OPERATIONS.prepare);
  expect(handed(fleet.executed[0])).toEqual(["codex/retry"]);
});

test("the untitled sessions are prepared once, as one bounded batch charged to the cycle", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  // Not candidates: a session the catalog has not listed from the archive, one of Babel's own
  // runs' transcripts, and a session that already has a title. None of the three is work this
  // lane may pay for.
  await nameless("unarchived", { archive_path: null, snapshot_id: null, archive_label: null });
  await nameless("babels-own", { kind: "agent" });
  await nameless("titled", { title: "Its own", title_provenance: "recorded" });

  const posted = await machinery.inferTitles(fleet, code, "cyc_1");

  // ONE `prepare`, over exactly the two, and nothing posted to a model yet: a job input binds
  // a SETTLED output, so the session belongs to the wake this preparation's settlement causes.
  expect(fleet.executed).toHaveLength(1);
  expect(fleet.executed[0]?.operationId).toBe(OPERATIONS.prepare);
  expect(JSON.parse(String(fleet.executed[0]?.input?.["input"] ?? "null"))).toMatchObject({
    machineId: MACHINE,
  });
  expect(handed(fleet.executed[0])).toEqual(["codex/a", "codex/b"]);

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
  expect(await machinery.inferTitles(fleet, code, "cyc_2")).toBeNull();
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

  const refused = await machinery.inferTitles(fleet, code, "cyc_1");

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

  expect(await machinery.inferTitles(fleet, code, "cyc_1")).toMatchObject({
    refused: expect.stringContaining("per-cycle ceiling 0.2500"),
  });
  expect(fleet.executed).toEqual([]);

  // The NEXT cycle has its own allowance under a daily ceiling that still has room, so the
  // lane is deferred rather than closed.
  expect(await machinery.inferTitles(fleet, code, "cyc_2")).toMatchObject({
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

  expect(await machinery.inferTitles(fleet, code, "cyc_1")).toBeNull();
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
  expect(await machinery.inferTitles(fleet, code, "cyc_2")).toBeNull();
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

test("a titling run asks the model only about sessions whose own logs recorded no title", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  await preparedTitles(["codex/a", "codex/b"]);
  // The preparation read `a`'s recorded title, and the hub ingested it before this wake.
  await harness.db.run(
    `UPDATE sessions SET title = 'Its own', title_provenance = 'recorded' WHERE selector = 'codex/a'`,
  );
  const prompts: string[] = [];
  code.posting = (request) => {
    prompts.push(request.prompt);
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

  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([
    { runId: "run_title_1", jobId: "job_code_title" },
  ]);
  expect(prompts[0]).toContain("codex/b — `sessions/0002-codex-b.jsonl`");
  expect(prompts[0]).not.toContain("codex/a");
  // The settlement answers exactly the session the model was asked about.
  const run = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE id = 'run_title_1'`,
  );
  expect(JSON.parse(String(run[0]?.preparation))).toMatchObject({
    titles: { selectors: ["codex/b"], reserved: 0.0625 },
  });
});

test("a titling run whose preparation found every title settles without a session, at no cost", async () => {
  await route();
  await nameless("a");
  await nameless("b");
  await preparedTitles(["codex/a", "codex/b"]);
  await harness.db.run(
    `UPDATE sessions SET title = 'Its own', title_provenance = 'recorded'
      WHERE selector IN ('codex/a', 'codex/b')`,
  );
  code.posting = () => {
    throw new Error("no session may be bought for titles the logs already recorded");
  };

  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([
    {
      runId: "run_title_1",
      settled: "every session this run sealed recorded its own title (2), so no model was asked",
    },
  ]);
  expect(
    await harness.db.query(`SELECT closure, cost_usd, job_id FROM runs WHERE id = 'run_title_1'`),
  ).toEqual([{ closure: "completed", cost_usd: 0, job_id: null }]);
  // Nothing needed an answer: the titles are the catalog's, and none is offered again.
  expect(await harness.db.query(`SELECT selector FROM session_titles`)).toEqual([]);
  expect(await machinery.inferTitles(fleet, code, "cyc_2")).toBeNull();
  expect(fleet.executed).toEqual([]);
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
  expect(await machinery.inferTitles(fleet, code, "cyc_2")).toBeNull();
  expect(fleet.executed).toEqual([]);
});

const ANALYSIS_PLAN = {
  metered: {},
  limits: {
    timeoutMs: 900000,
    memoryBytes: 1024 * 1024 * 1024,
    processes: 64,
    outputBytes: 512 * 1024 * 1024,
  },
};

async function stageLaunch(
  stage: "challenge" | "synthesize" = "challenge",
  extraBrief: AnalysisWork["brief"] = [],
) {
  await insert(harness.db, "policies", {
    version: "p_stage",
    seq: 3,
    actor_id: "operator",
    reason: "stage authorization",
    payload: JSON.stringify({
      ...ROUTED,
      activityWeights: { review: 0, explore: 0, challenge: 1, synthesize: 1 },
      review: {
        ...ROUTED.review,
        stageRecipes: { challenge: "code-health", synthesize: "code-health" },
      },
    }),
    recorded_at: stamp(NOW),
  });
  await insert(harness.db, "claims", {
    id: "asg_stage",
    record_id: "hyp_00000001",
    role: `analysis:${stage}`,
    lane: "exploration",
    policy_version: "p_stage",
    run_id: "cyc_stage",
    job_id: "job_stage_material",
    fence: 1,
    reserved_cost: 0.05,
    granted_at: stamp(NOW),
    expires_at: stamp(NOW + HOUR),
  });
  const analysis: AnalysisWork = {
    stage,
    selectors: ["omp/s1"],
    claim: { id: "asg_stage", runId: "cyc_stage", fence: 1 },
    brief: [
      {
        id: "hyp_00000001",
        kind: "hypothesis",
        runId: null,
        summary: "A prior claim",
        payload: { statement: "Preserve the full prior claim", limits: ["Still unverified"] },
        objectionTo: [],
      },
      ...extraBrief,
    ],
  };
  const start = () =>
    machinery.startExplore(
      {
        runId: "run_stage",
        jobId: "job_stage",
        materialJobId: "job_stage_material",
        authorityId: "cyc_stage",
      },
      fleet,
      code,
      {
        preset: "read-whats-new",
        machineId: MACHINE,
        profile: ROUTED.review.profile,
        recipes: ["code-health"],
      },
      ANALYSIS_PLAN,
      analysis,
    );
  return { analysis, start };
}

async function sealStage(closure = "completed", jobId = "job_stage_material") {
  await harness.db.run(
    `UPDATE runs SET closure = ?, finished_at = ?, payload = ? WHERE job_id = ?`,
    [
      closure,
      stamp(NOW),
      JSON.stringify({
        closure,
        material: {
          schema: "babel.material/1",
          preparationId: "prep-stage",
          preparedAt: stamp(NOW),
          machineId: MACHINE,
          sessions: [
            {
              selector: "omp/s1",
              harness: "omp",
              sourceId: "s1",
              captureDigest: "c".repeat(64),
              sourceDigest: "d".repeat(64),
              file: "0001-omp-s1.jsonl",
              records: 12,
              bytes: 1024,
            },
          ],
        },
      }),
      jobId,
    ],
  );
}

function stageJob(): EngineAnswer<CodeJob> {
  return {
    ok: true,
    value: {
      jobId: "job_stage_code",
      machineId: MACHINE,
      operationId: "atyrode.omp.session",
      pluginId: "atyrode.omp",
      state: "started",
    },
  };
}

test.each(["challenge", "synthesize"] as const)(
  "a %s retains its one claim and exact scope across preparation and Code",
  async (stage) => {
    const { start, analysis } = await stageLaunch(stage);
    await nameless("not-offered");
    await harness.db.run(`UPDATE sessions SET modified_at = ? WHERE selector = 'omp/s1'`, [
      new Date(NOW - 30 * 24 * HOUR).toISOString(),
    ]);
    expect(await start()).toEqual({ runId: "run_stage", jobId: "job_stage_material" });
    const document = fleet.executed[0]?.input["input"];
    if (typeof document !== "string") throw new Error("the preparation has no input document");
    expect(handed(fleet.executed[0])).toEqual(["omp/s1"]);
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
    await sealStage();
    const governor = coordinator(harness.store, () => NOW, 16);
    expect((await governor.open(NOW)).byMachine[MACHINE]).toBe(1);
    let prompt = "";
    code.posting = (request) => {
      prompt = request.prompt;
      return stageJob();
    };
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([
      { runId: "run_stage", jobId: "job_stage_code" },
    ]);
    expect((await governor.open(NOW)).byMachine[MACHINE]).toBe(1);
    expect(
      await harness.db.query(`SELECT job_id, finished_at FROM claims WHERE id = 'asg_stage'`),
    ).toEqual([{ job_id: "job_stage_code", finished_at: null }]);
    expect((await harness.store.run("run_stage")).run?.progress?.unheard).not.toBe(true);
    const rows = await harness.db.query<{ preparation: string; authority_kind: string }>(
      `SELECT preparation, authority_kind FROM runs WHERE id = 'run_stage'`,
    );
    expect(JSON.parse(rows[0]!.preparation)["analysis"]).toEqual(analysis);
    expect(rows[0]!.authority_kind).toBe("conductor");
    expect(prompt).toContain(`babel.stage = ${stage}`);
    expect(prompt).toContain("Preserve the full prior claim");
    expect(prompt).toContain("Still unverified");
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
  },
);

test.each(["disabled", "expired", "taken-over", "profile", "preparation", "malformed"] as const)(
  "%s analysis authority cannot post a paid continuation",
  async (boundary) => {
    const { start } = await stageLaunch();
    await start();
    await sealStage(boundary === "preparation" ? "failed" : "completed");
    if (boundary === "disabled")
      await harness.db.run(
        `UPDATE policies SET payload = json_set(payload, '$.enabled', json('false')) WHERE version = 'p_stage'`,
      );
    if (boundary === "expired")
      await harness.db.run(`UPDATE claims SET expires_at = ? WHERE id = 'asg_stage'`, [stamp(NOW)]);
    if (boundary === "taken-over")
      await harness.db.run(
        `UPDATE claims SET fence = 2, run_id = 'cyc_other' WHERE id = 'asg_stage'`,
      );
    if (boundary === "profile")
      code.checkResult = refusedByCode("engine_unavailable", "profile changed");
    if (boundary === "malformed")
      await harness.db.run(
        `UPDATE runs SET preparation = json_set(preparation, '$.analysis.stage', 'ruling') WHERE id = 'run_stage'`,
      );
    let posts = 0;
    code.posting = () => {
      posts += 1;
      return stageJob();
    };
    const result = await machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
    expect(result[0]).toHaveProperty("refused");
    expect(posts).toBe(0);
    expect(await harness.db.query(`SELECT closure FROM runs WHERE id = 'run_stage'`)).toEqual([
      { closure: "failed" },
    ]);
    const claim = (
      await harness.db.query<{ finished_at: string | null; fence: bigint }>(
        `SELECT finished_at, fence FROM claims WHERE id = 'asg_stage'`,
      )
    )[0]!;
    if (boundary === "taken-over") expect(claim.finished_at).toBeNull();
    else expect(claim.finished_at).not.toBeNull();
  },
);

test("a takeover during Code posting cancels the new session without finishing the newer fence", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  code.runSession = async () => {
    await harness.db.run(
      `UPDATE claims SET fence = 2, run_id = 'cyc_other' WHERE id = 'asg_stage'`,
    );
    return stageJob();
  };
  expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
  expect(code.cancelled).toEqual([{ containerId: "ctr_workbench", jobId: "job_stage_code" }]);
  expect(await harness.db.query(`SELECT finished_at FROM claims WHERE id = 'asg_stage'`)).toEqual([
    { finished_at: null },
  ]);
});

test("failure retaining the posted continuation cancels it and accounts its reservation", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  code.posting = stageJob;
  const run = harness.db.run.bind(harness.db);
  harness.db.run = async (sql, params) => {
    if (sql.startsWith("UPDATE runs SET job_id")) throw new Error("retention unavailable");
    return await run(sql, params);
  };
  try {
    expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
  } finally {
    harness.db.run = run;
  }
  expect(code.cancelled).toEqual([{ containerId: "ctr_workbench", jobId: "job_stage_code" }]);
  expect(
    await harness.db.query(`SELECT outcome, actual_cost FROM claims WHERE id = 'asg_stage'`),
  ).toEqual([{ outcome: "abandoned", actual_cost: 0.05 }]);
});

test("an unconfirmed Code cancellation keeps the parent pollable and the reservation occupied", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  code.posting = stageJob;
  code.cancelSession = async () => refusedByCode("engine_unavailable", "cancellation unavailable");
  const run = harness.db.run.bind(harness.db);
  harness.db.run = async (sql, params) => {
    if (sql.startsWith("UPDATE runs SET job_id")) throw new Error("retention unavailable");
    return await run(sql, params);
  };
  try {
    expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
  } finally {
    harness.db.run = run;
  }
  expect(await harness.db.query(`SELECT closure, job_id FROM runs WHERE id = 'run_stage'`)).toEqual(
    [{ closure: null, job_id: "job_stage_code" }],
  );
  expect(
    await harness.db.query(`SELECT finished_at, actual_cost FROM claims WHERE id = 'asg_stage'`),
  ).toEqual([{ finished_at: null, actual_cost: null }]);
  expect((await coordinator(harness.store, () => NOW, 16).open(NOW)).byMachine[MACHINE]).toBe(1);
  expect((await harness.store.run("run_stage")).run?.progress).toBeNull();
});

test("multiple brief ids carry record-scoped operator steering into the prompt and receipt intent", async () => {
  const { start, analysis } = await stageLaunch("challenge", [
    {
      id: "hyp_00000002",
      kind: "hypothesis",
      runId: null,
      summary: "Another prior claim",
      payload: { statement: "A distinct operator concern" },
      objectionTo: [],
    },
  ]);
  for (const [index, record] of analysis.brief.entries()) {
    await insert(harness.db, "steering", {
      id: `stg_brief_${index}`,
      root_id: `stg_brief_${index}`,
      seq: 0,
      actor_kind: "operator",
      actor_id: "operator",
      target_kind: "record",
      target_id: record.id,
      text: `Check the operator concern about ${record.id}`,
      recorded_at: stamp(NOW),
    });
  }
  await start();
  await sealStage();
  let prompt = "";
  code.posting = (request) => {
    prompt = request.prompt;
    return stageJob();
  };
  await machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
  const rows = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE id = 'run_stage'`,
  );
  const carried = JSON.parse(rows[0]!.preparation).steering.carried;
  expect(carried.map((remark: { id: string }) => remark.id).sort()).toEqual([
    "stg_brief_0",
    "stg_brief_1",
  ]);
  for (const record of analysis.brief)
    expect(prompt).toContain(`Check the operator concern about ${record.id}`);
});

test("overlapping preparation continuations post only one session and cannot close its bound winner", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  let entered!: () => void;
  const posting = new Promise<void>((resolve) => {
    entered = resolve;
  });
  let resume!: () => void;
  const held = new Promise<void>((resolve) => {
    resume = resolve;
  });
  let posts = 0;
  code.runSession = async () => {
    posts += 1;
    entered();
    if (posts === 1) await held;
    return stageJob();
  };
  const first = machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
  await posting;
  try {
    expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
  } finally {
    resume();
    await first;
  }
  expect(posts).toBe(1);
  expect(code.cancelled).toEqual([]);
  expect(await harness.db.query(`SELECT job_id, closure FROM runs WHERE id = 'run_stage'`)).toEqual(
    [{ job_id: "job_stage_code", closure: null }],
  );
  expect(
    await harness.db.query(`SELECT job_id, finished_at FROM claims WHERE id = 'asg_stage'`),
  ).toEqual([{ job_id: "job_stage_code", finished_at: null }]);
  expect((await coordinator(harness.store, () => NOW, 16).open(NOW)).byMachine[MACHINE]).toBe(1);
});

test.each(["finish-first", "parent-first"] as const)(
  "%s atomically decides whether an expired analysis claim ever authorizes native posting",
  async (order) => {
    const { start, analysis } = await stageLaunch();
    const governor = coordinator(harness.store, () => NOW, 16);
    const run = harness.db.run.bind(harness.db);
    let outcome = "";
    harness.db.run = async (sql, params) => {
      if (!sql.startsWith("INSERT INTO runs(id, kind, machine_id, container_id"))
        return await run(sql, params);
      const inserted = order === "parent-first" ? await run(sql, params) : null;
      await run(`UPDATE claims SET expires_at = ? WHERE id = 'asg_stage'`, [stamp(NOW)]);
      const finished = await governor.finish({
        ...analysis.claim,
        unpostedJobId: "job_stage_material",
        cost: 0,
        outcome: "failed",
        now: NOW,
      });
      outcome = finished.outcome;
      return inserted ?? (await run(sql, params));
    };
    try {
      await start();
    } finally {
      harness.db.run = run;
    }
    expect(outcome).toBe(order === "finish-first" ? "finished" : "refused");
    expect(fleet.executed.map((job) => job.jobId)).toEqual(
      order === "finish-first" ? [] : ["job_stage_material"],
    );
    expect(
      await harness.db.query(`SELECT outcome, actual_cost FROM claims WHERE id = 'asg_stage'`),
    ).toEqual(
      order === "finish-first"
        ? [{ outcome: "failed", actual_cost: 0 }]
        : [{ outcome: null, actual_cost: null }],
    );
  },
);

test.each(["throw", "refused"] as const)(
  "an unconfirmed Code posting (%s) remains visible and reserved without buying another session",
  async (failure) => {
    const { start } = await stageLaunch();
    await start();
    await sealStage();
    let posts = 0;
    code.runSession = async () => {
      posts += 1;
      if (failure === "throw") throw new Error("response transport interrupted");
      return refusedByCode(ENGINE_REFUSALS.unconfirmed, "response transport interrupted");
    };
    expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
    await machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
    expect(posts).toBe(1);
    expect(
      await harness.db.query(
        `SELECT closure, json_extract(payload, '$.posting') AS posting FROM runs WHERE id = 'run_stage'`,
      ),
    ).toEqual([{ closure: null, posting: 1n }]);
    expect(await harness.db.query(`SELECT finished_at FROM claims WHERE id = 'asg_stage'`)).toEqual(
      [{ finished_at: null }],
    );
    const visible = (await harness.store.run("run_stage")).run;
    expect(visible?.progress?.unheard).toBe(true);
    expect(visible?.progress?.message).toContain("response transport interrupted");
    expect((await coordinator(harness.store, () => NOW, 16).open(NOW)).byMachine[MACHINE]).toBe(1);
  },
);

test("a definite pre-dispatch refusal closes an analysis parent instead of claiming an unknown posting", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  let posts = 0;
  code.runSession = async () => {
    posts += 1;
    return refusedByCode(ENGINE_REFUSALS.refused, "the local request failed validation");
  };
  expect((await machinery.postPrepared(fleet, code, ANALYSIS_PLAN))[0]).toHaveProperty("refused");
  await machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
  expect(posts).toBe(1);
  expect(await harness.db.query(`SELECT closure FROM runs WHERE id = 'run_stage'`)).toEqual([
    { closure: "failed" },
  ]);
  expect((await harness.store.run("run_stage")).run?.progress?.unheard).not.toBe(true);
});

test("a native titling admission refusal leaves no retained material or parent to poll", async () => {
  await route();
  await nameless("refused");
  fleet.execute = () => {
    throw new HostCallError("jobs.execute", "installation_changed");
  };
  fleet.status = () => {
    throw new HostCallError("jobs.status", "job_not_started");
  };
  expect(await machinery.inferTitles(fleet, code, "cyc_title")).toHaveProperty("refused");
  expect(await harness.db.query(`SELECT id FROM runs`)).toEqual([]);
  expect(fleet.executed).toEqual([]);
});

test("an unresolved posting cannot be stopped or reported as closed on a later refusal", async () => {
  const { start } = await stageLaunch();
  await start();
  await sealStage();
  code.runSession = async () =>
    refusedByCode(ENGINE_REFUSALS.unconfirmed, "response transport interrupted");
  await machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
  expect(
    await halt("run_stage", { operationId: OPERATIONS.prepare, jobId: "job_stage_material" }),
  ).toHaveProperty("refused");
  expect(fleet.cancelled).toEqual([]);
  await harness.db.run(
    `UPDATE policies SET payload = json_set(payload, '$.enabled', json('false'))`,
  );
  expect(await machinery.postPrepared(fleet, code, ANALYSIS_PLAN)).toEqual([]);
  expect(await harness.db.query(`SELECT closure FROM runs WHERE id = 'run_stage'`)).toEqual([
    { closure: null },
  ]);
  expect((await harness.store.run("run_stage")).run?.progress?.unheard).toBe(true);
  expect(await harness.db.query(`SELECT finished_at FROM claims WHERE id = 'asg_stage'`)).toEqual([
    { finished_at: null },
  ]);
});

test.each(["posting", "bound"] as const)(
  "Stop's preparing snapshot cannot close a parent that becomes %s while cancellation is awaited",
  async (boundary) => {
    const { start, analysis } = await stageLaunch();
    await start();
    await sealStage();
    const cancelling = Promise.withResolvers<void>();
    const cancelled = Promise.withResolvers<void>();
    fleet.cancel = async () => {
      cancelling.resolve();
      await cancelled.promise;
    };
    const posting = Promise.withResolvers<void>();
    const posted = Promise.withResolvers<void>();
    code.runSession = async () => {
      posting.resolve();
      await posted.promise;
      return stageJob();
    };
    const stopping = halt("run_stage", {
      operationId: OPERATIONS.prepare,
      jobId: "job_stage_material",
    });
    await cancelling.promise;
    const continuation = machinery.postPrepared(fleet, code, ANALYSIS_PLAN);
    await posting.promise;
    try {
      if (boundary === "bound") {
        posted.resolve();
        await continuation;
      }
      cancelled.resolve();
      expect(await stopping).toHaveProperty("refused");
      expect(await harness.db.query(`SELECT closure FROM runs WHERE id = 'run_stage'`)).toEqual([
        { closure: null },
      ]);
    } finally {
      cancelled.resolve();
      posted.resolve();
      await stopping;
      await continuation;
    }
    expect(code.cancelled).toEqual([]);
    expect(
      await harness.db.query(`SELECT job_id, closure FROM runs WHERE id = 'run_stage'`),
    ).toEqual([{ job_id: "job_stage_code", closure: null }]);
    const governor = coordinator(harness.store, () => NOW, 16);
    expect(
      await governor.finish({
        ...analysis.claim,
        unpostedJobId: "job_stage_code",
        outcome: "failed",
        cost: 0,
        now: NOW + 2 * HOUR,
      }),
    ).toHaveProperty("outcome", "refused");
    expect((await governor.open(NOW)).byMachine[MACHINE]).toBe(1);
  },
);
