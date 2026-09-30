/*
  THE DRAIN, HELD TO WHAT IT DOES TO THE WORLD (#258).

  Every test dispatches the way the kit does — parse the arguments against the action's own input,
  run the handler, parse what it produced against the action's own result — and then asks the
  STORE and the FLEET what happened, because that is what a drain is: N jobs posted to one
  machine, and one row that remembers them until the last receipt lands. The fleet is fake and
  the store is real, which is the right way round: a job request is a shape this file can pin
  exactly, and the drain row is SQL under every CHECK the schema declares.

  The launch path is the REAL `launchMachinery` over the same store, so a job this drain posts is
  the job the operator's own button posts — if the two ever diverged these tests would still pass
  against a fake and prove nothing.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import { PluginManifestSchema } from "@manifold/protocol";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  DrainReportSchema,
  MATERIAL_HEADROOM_BYTES,
  MATERIAL_SCHEMA,
  OPERATIONS,
  PRESET_OPERATIONS,
  RECALL_SERVICE_ID,
  TRANSCRIPT_MAP_SERVICE_OPERATION,
  PrepareInputSchema,
  type ProfileRow,
} from "../contract.ts";
import { actionSchemas, type ActionInput } from "@atyrode/manifold-code";
import type {
  JobLaunch,
  JobRef,
  JobRunState,
  JobsSlice,
  MachineReadiness,
  RunPlan,
} from "../server/conductor.ts";
import { drainTick, type DrainDeps, type DrainLaunch } from "../server/drain.ts";
import { liveDrainCapacity } from "../server/drain-admission.ts";
import { runPlan, type BabelJobs } from "../server/plan.ts";
import { coordinator } from "../store/coordinator.ts";
import { drainReportId, readDrain, readDrainReport } from "../store/drains.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import manifestJson from "../manifest.json";
import type { Door } from "./door.ts";
import { drainDoors } from "./drain.ts";
import {
  hubRefusal,
  launchMachinery,
  principalChain,
  type LaunchIdentity,
  type Started,
} from "./launch.ts";
import {
  PROMPT_LIMIT,
  materialInput,
  promptBytes,
  type CodeEngine,
  type EngineAnswer,
} from "../server/engine/session.ts";

const NOW = Date.UTC(2026, 8, 14, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";

const LIMITS = {
  timeoutMs: 3_600_000,
  memoryBytes: 2_147_483_648,
  processes: 64,
  outputBytes: 16_777_216,
};

/** The Code profile a drain names, and what Code reports about it (#267, #279). */
const PROFILE = { containerId: "ctr_workbench", expectedRevision: 7 };
const LISTED: ProfileRow = {
  containerId: "ctr_workbench",
  revision: 7,
  model: "anthropic/claude-sonnet-4-5",
  thinking: "high",
  lastMachineId: "m-dev-01",
  accounts: [{ provider: "anthropic", identityKey: "the-drain-account", label: "" }],
  resolved: true,
};
/**
 * CODE, as a drain reaches it.
 *
 * `runSession` parses what it was handed with CODE'S OWN published input schema, which is the
 * point of driving a fake at all here: a request Babel built that Code's schema refuses is
 * Babel's bug, and a fake that accepted anything would hide it. `posted` keeps the parsed
 * requests so a test can read the material binding off the one the settle wake sent.
 */
const code: CodeEngine & {
  listed: ProfileRow;
  posted: ActionInput<"runSession">[];
  cancelled: string[];
  cancelRefusal: string;
} = {
  listed: LISTED,
  posted: [],
  cancelled: [],
  cancelRefusal: "",
  profiles: async () => await Promise.resolve({ ok: true, value: [code.listed] }),
  checkProfile: async () => await Promise.resolve({ ok: true, value: null }),
  runSession: async (request) => {
    const parsed = actionSchemas.runSession.input.safeParse({
      containerId: request.profile.containerId,
      machineId: request.machineId,
      expectedRevision: request.profile.expectedRevision,
      prompt: request.prompt,
      ...(request.prepareJobId === undefined ? {} : materialInput(request.prepareJobId)),
      ...(request.inferenceLimits === undefined
        ? {}
        : { inferenceLimits: request.inferenceLimits }),
    });
    if (!parsed.success) {
      throw new Error(`Babel built a request Code refuses: ${parsed.error.message}`);
    }
    code.posted.push(parsed.data);
    return await Promise.resolve({
      ok: true,
      value: {
        jobId: `omp_${String(code.posted.length)}`,
        machineId: request.machineId,
        operationId: "atyrode.omp.session",
        pluginId: "atyrode.omp",
        state: "queued",
      },
    });
  },
  cancelSession: async (args) => {
    if (code.cancelRefusal !== "") {
      return await Promise.resolve({
        ok: false,
        code: "engine_forbidden",
        refused: code.cancelRefusal,
      } as EngineAnswer<never>);
    }
    code.cancelled.push(args.jobId);
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
  },
  readSession: async () => {
    throw new Error("a drain never reads a session back");
  },
};

const PLAN: RunPlan = { metered: { [OPERATIONS.explore]: true }, limits: LIMITS };

const READY: MachineReadiness = {
  connected: true,
  operations: {
    [PRESET_OPERATIONS["keep-going"]]: { ready: true, reason: null },
    [OPERATIONS.explore]: { ready: true, reason: null },
    [OPERATIONS.evaluate]: { ready: true, reason: null },
  },
  installation: { revision: "rev-7", artifactSha256: "a".repeat(64), enabled: true, ready: true },
};

class Fleet implements BabelJobs {
  readonly executed: JobLaunch[] = [];
  readonly cancelled: JobRef[] = [];
  refusal = "";
  /**
   * THE HUB'S OWN WORD FOR THE REFUSAL, when it is not the whole of what the hub said. It is
   * raised as a field on the error rather than inside the message because that is the only
   * way a test can hold the plugin to reading the word and not the wording (#288).
   */
  refusalWord = "";
  /** Set to refuse cancellation the way a credential without `jobs:cancel` does. */
  cancelRefusal = "";
  /**
   * What happens between the hub taking a job and the caller recording it: the window a launch
   * really has, and where an operator's stop lands in the test below.
   */
  duringExecute: ((args: JobLaunch) => Promise<void>) | null = null;

  describe(): MachineReadiness {
    return READY;
  }

  async execute(args: JobLaunch): Promise<JobRunState> {
    if (this.refusal !== "") {
      throw this.refusalWord === ""
        ? new Error(this.refusal)
        : Object.assign(new Error(this.refusal), { refusal: this.refusalWord });
    }
    const existing = this.executed.find((job) => job.jobId === args.jobId);
    if (existing !== undefined) {
      return {
        jobId: args.jobId,
        machineId: args.machineId,
        operationId: args.operationId,
        state: "queued",
        result: null,
      };
    }
    this.executed.push(args);
    if (this.duringExecute !== null) await this.duringExecute(args);
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

  status(node: JobRef): JobRunState {
    const held = this.executed.find((job) => job.jobId === node.jobId);
    if (held === undefined) throw new Error("jobs.status: job_not_started");
    return {
      jobId: held.jobId,
      machineId: held.machineId,
      operationId: held.operationId,
      state: "queued",
      result: null,
    };
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
let physicalCores: number | undefined;
let operationCeiling: number | null;
let inventory: Pick<GuestCtx["machines"], "inventory"> | undefined;

const ctx = {
  principal: { id: "operator" },
  auth: { isRoot: true },
  emit: () => {},
} as unknown as GuestCtx;
/** The operator's account chain (#470): his press, and the wakes his drain's settlements cause. */
const WAKE = principalChain("operator");

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
    profile: PROFILE,
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
  if (!Array.isArray(drains) || drains[0] === undefined)
    throw new Error(`no status for ${drainId}`);
  return drains[0] as Record<string, unknown>;
}

/** A job of this drain settled, the way the conductor writes one: the meter's own totals. */
async function settleJob(
  runId: string,
  over: {
    readonly costMicros?: number;
    readonly outputTokens?: number;
    readonly reason?: string;
  } = {},
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

/**
 * THE RECEIPT A SETTLED PREPARATION LEAVES: its index, which is what the posting wake reads
 * the prompt's file names and digests out of. One shape, used for both of a fan's two
 * preparations, because a difference between them would be a difference this test did not
 * mean.
 */
function sealedMaterial(): string {
  return JSON.stringify({
    closure: "completed",
    material: {
      schema: MATERIAL_SCHEMA,
      preparationId: "prep-1",
      preparedAt: stamp(NOW),
      machineId: MACHINE,
      sessions: [
        {
          selector: "omp/s1",
          harness: "omp",
          sourceId: "s1",
          captureDigest: "c".repeat(64),
          sourceDigest: "a".repeat(64),
          file: "0001-omp-s1.jsonl",
          records: 4,
          bytes: 1024,
        },
      ],
    },
  });
}

/**
 * WHETHER THIS FAKE'S LAUNCH REACHES THE SECOND WAKE (#592). A real run is started twice: the
 * press seals the material and records INTENT — `job_id` NULL, the preparation in flight —
 * and `postPrepared` posts the session one wake later and writes CODE'S OWN job id. Both
 * states are stoppable, and they are stopped with different verbs, so both are launchable
 * here. Reset per test by `beforeEach`.
 */
let reachesCode = true;

/** What Code's job id looks like: its own, never the identity Babel derived. */
function codeJobOf(identity: LaunchIdentity): string {
  return identity.jobId.replace(/^job_/, "omp_");
}

/**
 * A LAUNCH PATH THAT POSTS, which the real one no longer is in one wake (#279, #592).
 *
 * The CONTROLLER's contract is with the `DrainLaunch` interface — keep a fan of N filled, fold
 * what settles, stop at the target — and none of that is about who posts, so the controller is
 * exercised against a path that does. What the real one answers is its own test below, and
 * `drain.start` inherits it.
 *
 * It posts ONE job as a stand-in for the two the real path posts, and then writes THE ROW THE
 * REAL PATH LEAVES, which is the part that matters here: `container_id` says which lane the
 * run is in, `prepare_job_id` is the job Babel posted, and `job_id` is CODE'S — a different
 * id, because a fake that wrote the identity the controller derived into `job_id` would make
 * a stop that reached for `LiveJob.jobId` look correct against a world where it is not.
 */
function posting(store: TestStore["store"], jobs: () => BabelJobs): DrainLaunch {
  const post = async (
    identity: LaunchIdentity,
    _slice: JobsSlice,
    ..._rest: unknown[]
  ): Promise<Started> => {
    const input = _rest.find(
      (
        entry,
      ): entry is {
        machineId: string;
        preset: keyof typeof PRESET_OPERATIONS;
        recipes: readonly string[];
      } => typeof entry === "object" && entry !== null && "preset" in entry,
    )!;
    const operationId = PRESET_OPERATIONS[input.preset];
    const beat = operationId === PRESET_OPERATIONS["keep-going"];
    try {
      await jobs().execute({
        jobId: identity.jobId,
        machineId: input.machineId,
        operationId,
        input: { input: JSON.stringify({ runId: identity.runId }) },
        outputs: [],
      });
    } catch (error) {
      // THE PAIR THE REAL `post` ANSWERS, through the real extraction: the sentence the fleet
      // said, and the hub's own word for what it refused.
      return {
        refused: error instanceof Error ? error.message : String(error),
        code: hubRefusal(error),
      };
    }
    const prepareJobId = `${identity.jobId}_material`;
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id, recipe_id,
                        profile, authority_kind, authority_id, preparation, started_at, records,
                        payload)
       VALUES (?, ?, ?, ?, ?, ?, '', ?, 'operator', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        identity.runId,
        operationId,
        input.machineId,
        beat ? identity.jobId : reachesCode ? codeJobOf(identity) : null,
        beat ? null : "ctr_workbench",
        beat ? null : prepareJobId,
        // BABEL'S OWN LAUNCH REPORT, the two columns the real path writes and the drain's own
        // report reads back (#270): which methods this run was asked to perform, and the
        // container it was posted through. There is no `account` here and the real path writes
        // none either — `drainInput` carries no session — so a report's per-account figures come
        // from the drain's recorded ledger, which is the thing under test.
        JSON.stringify({ containerId: "ctr_workbench", expectedRevision: 7 }),
        identity.authorityId,
        JSON.stringify({
          preset: input.preset,
          recipes: input.recipes.map((id) => ({ id, version: 1 })),
        }),
        new Date(store.now()).toISOString(),
        JSON.stringify({ closure: null, requestedAt: store.now() }),
      ],
    );
    store.touch();
    return { runId: identity.runId, jobId: beat ? identity.jobId : prepareJobId };
  };
  return { startExplore: post, startBeat: post };
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  physicalCores = 16;
  operationCeiling = 16;
  inventory = {
    inventory: async () => ({
      ok: true,
      value: {
        machines: [
          {
            id: MACHINE,
            name: MACHINE,
            online: true,
            revoked: false,
            draining: false,
            terminalExecution: null,
            lastRefusal: null,
            ...(physicalCores === undefined ? {} : { physicalCoreCount: physicalCores }),
          },
        ],
      },
    }),
  };
  code.posted.length = 0;
  fleet = new Fleet();
  // The Code fake is one object across the file, so its record of what it was asked is reset
  // here: a count that leaked between tests would pass for the wrong reason.
  code.listed = LISTED;
  code.cancelled.length = 0;
  code.cancelRefusal = "";
  reachesCode = true;
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
  // Archived captures, which is all a preparation reads (#453).
  for (const n of [1, 2, 3, 4, 5]) {
    await insert(db, "sessions", {
      selector: `omp/s${String(n)}`,
      host: MACHINE,
      harness: "omp",
      source_id: `s${String(n)}`,
      title: `session ${String(n)}`,
      archive_label: "dev-01",
      archive_path: `/home/alex/.omp/agent/sessions/s${String(n)}.jsonl`,
      snapshot_id: "5".repeat(64),
      archived_at: new Date(NOW - HOUR).toISOString(),
      size: 1000,
      modified_at: new Date(NOW - 2 * HOUR).toISOString(),
      seen_at: stamp(NOW - 2 * HOUR),
    });
  }
  const coordinated = coordinator(store, () => store.now(), 16);
  deps = {
    store,
    admission: async (machineId) => await liveDrainCapacity(inventory, machineId, operationCeiling),
    coordinator: coordinated,
    launch: posting(store, () => fleet),
    jobs: fleet,
    engine: code,
    chain: WAKE,
    plan: () => PLAN,
    now: () => store.now(),
  };
  doors = drainDoors(store, {
    coordinator: coordinated,
    deps: () => deps,
    startOrdinary: async () => ({ ok: true, notes: [] }),
    stopOrdinary: async () => {},
    now: () => store.now(),
  });
});

afterEach(() => {
  harness.close();
});

test("the roster is two starts, a dry read and a stop; only the mapping start names nodes", () => {
  expect(doors.map((entry) => entry.action.name)).toEqual([
    ACTIONS.drainStart,
    ACTIONS.mapDrainStart,
    ACTIONS.drainStatus,
    ACTIONS.drainStop,
  ]);
  const [, mapBegin, status, stop] = doors as readonly Door[];

  // THE MAPPING START IS ADMITTED WHERE ITS FIRST FAN IS POSTED: the executor's `map-prepare`
  // node and the source owner's private mapping target, as `startMapCatalog` is. A read wake is
  // attenuated below native posting, so this press is the one place paid mapping can begin.
  // It ALSO discharges writing in the route's Code workspace and lends the broker read, because
  // the drain's sessions are posted later by its own wakes, graded against what this press held
  // (#469): without them Code and OMP refuse every session the drain paid to prepare.
  expect(mapBegin?.action.caps).toEqual([
    "machines:run",
    "operations:invoke",
    "services:invoke",
    "network:host",
    "containers:read",
    "containers:write",
  ]);
  expect(mapBegin?.action.requirements).toEqual([
    { cap: "machines:run", target: ["operation"] },
    { cap: "operations:invoke", target: ["operation"] },
    { cap: "network:host", target: ["operation"] },
    { cap: "services:invoke", target: ["source"] },
    { cap: "containers:read", target: ["profile"] },
    { cap: "containers:write", target: ["profile"] },
  ]);
  expect(mapBegin?.action.delegates).toEqual([
    "machines:read",
    "jobs:read",
    "locations:write",
    "services:read",
  ]);

  expect(status?.action.caps).toEqual(["containers:read"]);

  // A stop closes this plugin's own row and reaches its jobs through its OWN ceiling. It
  // asked `jobs:cancel` at the operation they share, and that operation is one no
  // installation declares any more (#279) — so the host refused the dispatch before the
  // handler ran and a drain v0.3.0 left running could not be stopped at all. The cancel is a
  // DELEGATE now; the hub still checks consent at the effect and a refused cancel is
  // reported by name rather than assumed.
  expect(stop?.action.caps).toEqual(["containers:write"]);
  expect(stop?.action.requirements).toBeUndefined();
  expect(stop?.action.delegates).toEqual(["jobs:cancel"]);
});

test("a start posts the whole fan, moves no policy number, and names the account it spends", async () => {
  const answer = await start({ concurrent: 3 });
  expect(answer["launched"]).toBe(3);
  expect(answer["account"]).toBe("ctr_workbench: the-drain-account (as Code reported at start)");
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

  // A DRAIN MOVES NO GOVERNED NUMBER AT ALL (#260, and the review of #285). Its jobs are launched
  // directly and take no claim, so no admission bound ever counts one: an overlay raising
  // `concurrentPerMachine` bounded nothing of the drain's and raised the CONDUCTOR's review fan
  // on every online machine instead. Neither table is written.
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);
  expect(await harness.db.query(`SELECT version FROM policies`)).toHaveLength(1);

  // The row remembers what it must relaunch with: the account, the fan, and the preset's knobs.
  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("running");
  expect(row?.live).toHaveLength(3);
  expect(row?.profile.accounts[0]?.identityKey).toBe("the-drain-account");

  // EVERY DRAIN CARRIES A DEADLINE. This one named none, so it stops two hours out whatever it
  // has spent: a drain that could outlive the window it exists to spend is the failure this
  // operation was written against.
  expect(answer["deadline"]).toBe(new Date(NOW + 2 * HOUR).toISOString());
  expect(row?.target.deadline).toBe(new Date(NOW + 2 * HOUR).toISOString());
  expect(String(answer["note"])).toMatch(/names no deadline, so it stops at/);
});

test("a fan the standing daily ceiling would have refused is admitted: a drain consults none", async () => {
  /*
    THE CEILING THAT WAS NOT THE DRAIN'S (the review of #285, finding 3). The overlay was judged
    by `validateNewPolicy`, which refuses a policy whose `dailyCost` is below its `perCycleCost`;
    with `perCycleCost = perRunUsd(standing) * concurrent` any fan past `dailyCost / perRunUsd`
    was refused at the door. On this fixture — 0.25 a cycle over a batch of one, 2 a day — a fan
    of nine answered "daily cost 2 is below the per-cycle cost 2.25" and posted nothing, naming
    two numbers a drain's directly-launched jobs never consult.
  */
  const answer = await start({ concurrent: 9 });
  expect(answer["refused"]).toBeUndefined();
  expect(answer["launched"]).toBe(9);
  expect(fleet.executed).toHaveLength(9);
  const daily = await harness.db.query<{ payload: string }>(`SELECT payload FROM policies`);
  expect(JSON.parse(daily[0]?.payload ?? "{}")["dailyCost"]).toBe(2);
});

test("a profile Code reported no account for is named as that rather than as a blank", async () => {
  // #267: the panel printed `Draining  as drn_…` when nothing could name the account. Code
  // publishes no accounts on a profile at this pin, so the honest answer is which silence it
  // is — and the drain says the container it is spending through.
  code.listed = { ...LISTED, accounts: [], resolved: true };
  const answer = await start({});
  expect(answer["account"]).toBe("ctr_workbench (Code reported no account)");
  expect((await statusOf(String(answer["drainId"])))["account"]).toBe(
    "ctr_workbench (Code reported no account)",
  );
});

test("a drain without a target is refused, and so is a second drain on the same machine", async () => {
  expect(String((await start({ target: {} }))["refused"])).toMatch(/a drain needs a target/);
  // A deadline that has already gone is not a deadline.
  expect(
    String((await start({ target: { deadline: new Date(NOW - 1000).toISOString() } }))["refused"]),
  ).toMatch(/already passed/);
  // `keep-going` reaches no model, so a cost target on it could never be met.
  expect(String((await start({ preset: "keep-going" }))["refused"])).toMatch(
    /give this drain a deadline/,
  );

  expect((await start())["launched"]).toBe(2);
  expect(String((await start())["refused"])).toMatch(/is already draining under drn_/);
});

test("a fan above the machine's ceiling is refused by name rather than posted and rejected", async () => {
  // `concurrentJobs` is the manifest's `limits.concurrentJobs` for the operation this preset
  // posts: the hub refuses every posting past it at `execute`, so the door refuses above it by
  // name rather than spending the drain's first round on refusals.
  operationCeiling = 4;
  expect(await start({ concurrent: 8 })).toHaveProperty("refused");
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
});

test("start refuses a fan above live physical cores without rewriting policy", async () => {
  const policy = await harness.db.query(`SELECT payload FROM policies`);
  physicalCores = 1;
  expect(await start({ concurrent: 2 })).toHaveProperty("refused");
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
  expect(await harness.db.query(`SELECT payload FROM policies`)).toEqual(policy);
});

test.each([undefined, 0, -1, 1.5, Number.NaN, Infinity])(
  "unknown or invalid physical core capacity %s cannot start a drain",
  async (cores) => {
    physicalCores = cores;
    expect(await start()).toHaveProperty("refused");
    expect(fleet.executed).toEqual([]);
    expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
  },
);

test.each(["absent", "refused", "disconnect", "offline", "revoked"] as const)(
  "%s inventory cannot authorize a drain",
  async (condition) => {
    const source = inventory!;
    inventory =
      condition === "absent"
        ? undefined
        : {
            inventory: async () => {
              if (condition === "disconnect") throw new Error("disconnected");
              if (condition === "refused")
                return { ok: false, code: "not_authorized", message: "credential withdrawn" };
              const answer = await source.inventory();
              if (!answer.ok) return answer;
              return {
                ok: true,
                value: {
                  machines: answer.value.machines.map((machine) => ({
                    ...machine,
                    online: condition !== "offline",
                    revoked: condition === "revoked",
                  })),
                },
              };
            },
          };
    expect(await start()).toHaveProperty("refused");
    expect(fleet.executed).toEqual([]);
    expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
  },
);

test("capacity lost after the first reservation refuses an empty drain and releases its slot", async () => {
  const source = inventory!;
  inventory = {
    inventory: async () => {
      if ((await harness.db.query(`SELECT run_id FROM drain_launches`)).length > 0)
        physicalCores = undefined;
      return await source.inventory();
    },
  };
  expect(await start({ concurrent: 1 })).toHaveProperty("refused");
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT state, live FROM drains`)).toEqual([
    { state: "failed", live: "[]" },
  ]);
  expect(await harness.db.query(`SELECT state FROM drain_launches`)).toEqual([
    { state: "refused" },
  ]);
});

test("a shrink between reservations stops the fan and retains paid work and receipts", async () => {
  physicalCores = 3;
  const execute = fleet.execute.bind(fleet);
  fleet.execute = (job) => {
    const answer = execute(job);
    physicalCores = 1;
    return answer;
  };
  const started = await start({ concurrent: 3, target: { costMicros: 9_000_000 } });
  expect(started).toMatchObject({ concurrent: 3, launched: 1 });
  const id = String(started["drainId"]);
  expect((await readDrain(harness.store, id))?.live.map((job) => job.runId)).toEqual([
    `run_${id}_0`,
  ]);
  inventory = undefined;
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 1, state: "running" });
  expect(code.cancelled).toEqual([]);
  await settleJob(`run_${id}_0`, { costMicros: 250_000 });
  await drainTick(deps);
  expect((await readDrain(harness.store, id))?.spent.costMicros).toBe(250_000);
  expect(fleet.executed).toHaveLength(1);
});

test("pending native recovery and delayed Code posting require current physical capacity", async () => {
  const machinery = realLaunch();
  const id = String((await start({ concurrent: 2, maxJobs: 2 }))["drainId"]);
  await harness.db.run(`UPDATE drain_launches SET state='reserved' WHERE drain_id=?`, [id]);
  physicalCores = 1;
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 2 });
  expect(fleet.executed).toHaveLength(2);
  await sealDrainJob(id, 0);
  await sealDrainJob(id, 1);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted).toEqual([]);
  expect((await readDrain(harness.store, id))?.live).toHaveLength(2);
  physicalCores = 2;
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted).toHaveLength(2);
  expect(code.cancelled).toEqual([]);
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
  expect(row?.live.map((job) => job.jobId)).toEqual([
    `job_${drainId}_1_material`,
    `job_${drainId}_2_material`,
  ]);
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

test("maxJobs bounds the first fan even when it is smaller than concurrent", async () => {
  const answer = await start({ concurrent: 3, maxJobs: 1, target: {} });
  const drainId = String(answer["drainId"]);
  expect(answer["launched"]).toBe(1);
  expect(fleet.executed.map((job) => job.jobId)).toEqual([`job_${drainId}_0`]);
  expect((await drainTick(deps))[0]?.state).toBe("running");
  expect(code.cancelled).toEqual([]);
  await settleJob(`run_${drainId}_0`, { costMicros: 0, outputTokens: 0 });
  expect((await drainTick(deps))[0]?.state).toBe("target");
  expect(fleet.executed).toHaveLength(1);
});

test("an exhausted admission bound waits for refused and zero-meter jobs without cancelling", async () => {
  const drainId = String((await start({ concurrent: 2, maxJobs: 2 }))["drainId"]);
  await harness.db.run(
    `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
    [
      stamp(NOW),
      JSON.stringify({ closure: "failed", reason: "Code refused admission" }),
      `run_${drainId}_0`,
    ],
  );
  const [waiting] = await drainTick(deps);
  expect(waiting).toMatchObject({ launched: 0, settled: 1, live: 1, state: "running" });
  expect(code.cancelled).toEqual([]);
  expect(fleet.cancelled).toEqual([]);
  expect((await readDrain(harness.store, drainId))?.finishedAt).toBe("");
  expect(await readDrainReport(harness.store, drainId)).toBeNull();

  await settleJob(`run_${drainId}_1`, { costMicros: 240_000, outputTokens: 700 });
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 0, state: "target" });
  expect(fleet.executed).toHaveLength(2);
  const ended = await readDrain(harness.store, drainId);
  expect(ended).toMatchObject({
    jobsLaunched: 2,
    jobsSettled: 2,
    spent: { costMicros: 240_000, outputTokens: 700 },
    closures: { failed: 1, completed: 1 },
  });
  expect((await readDrainReport(harness.store, drainId))?.ending).toBe("target");
  expect(await drainTick(deps)).toEqual([]);
  expect(fleet.executed).toHaveLength(2);
});

test("the target stops the drain and it closes on the job it cancelled, ending when that settles", async () => {
  const answer = await start({ concurrent: 2, target: { costMicros: 500_000 } });
  const drainId = String(answer["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 600_000 });

  const [report] = await drainTick(deps);
  expect(report?.reason).toMatch(/target of 500000 micro-dollars is met at 600000/);
  // NOTHING IS LAUNCHED BY THE TICK THAT ENDS IT: the fold happens before the decision, so the
  // job that met the target ends the drain instead of making room for one more.
  expect(report?.launched).toBe(0);
  expect(fleet.executed).toHaveLength(2);

  // The in-flight job is cancelled THROUGH CODE, because it is a Code session: its job is
  // `atyrode.omp`'s and `ctx.jobs.cancel` is bound to the calling plugin's id.
  expect(code.cancelled).toEqual([`omp_${drainId}_1`]);
  expect(fleet.cancelled).toEqual([]);
  // …and the drain is CLOSING on it, not finished: a cancel is a request and a receipt is what
  // answers it, so what that job metered is still owed to this drain's total.
  expect(report?.state).toBe("closing");
  const closing = await readDrain(harness.store, drainId);
  expect(closing?.state).toBe("closing");
  expect(closing?.ending).toBe("target");
  expect(closing?.finishedAt).toBe("");
  expect(closing?.live.map((job) => job.jobId)).toEqual([`job_${drainId}_1_material`]);
  // No policy number was moved, so there is none to unwind (#260, and the review of #285).
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);

  // The cancelled job writes its receipt, and the tick behind it folds that spend and records
  // the end. A drain that dropped it would report 600000 of the 1100000 it actually spent.
  await settleJob(`run_${drainId}_1`, { costMicros: 500_000, outputTokens: 400 });
  const [after] = await drainTick(deps);
  expect(after?.state).toBe("target");
  expect(after?.launched).toBe(0);
  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("target");
  expect(row?.finishedAt).not.toBe("");
  expect(row?.live).toEqual([]);
  expect(row?.spent.costMicros).toBe(1_100_000);
  expect(row?.jobsSettled).toBe(2);
  expect(row?.reason).toMatch(/target of 500000 micro-dollars is met at 600000/);
});

test("a deadline that has passed stops the drain even while it is under its cost target", async () => {
  const drainId = String(
    (
      await start({
        concurrent: 1,
        target: { costMicros: 1_000_000_000, deadline: new Date(NOW + 1000).toISOString() },
      })
    )["drainId"],
  );
  harness.at(NOW + 2000);
  const [report] = await drainTick(deps);
  expect(report?.launched).toBe(0);
  expect(report?.reason).toMatch(/deadline .* has passed/);
  // It stops launching at once and closes on the job it cancelled; the ending is recorded now
  // and taken when that job's receipt lands.
  expect(report?.state).toBe("closing");
  expect((await readDrain(harness.store, drainId))?.ending).toBe("deadline");
  await settleJob(`run_${drainId}_0`);
  expect((await drainTick(deps))[0]?.state).toBe("deadline");
  expect((await readDrain(harness.store, drainId))?.state).toBe("deadline");
});

test("a stop leaves nothing in flight, takes no claim to release, and relaunches nothing", async () => {
  // THE ACCEPTANCE LINE OF #258, as it actually holds: the drain's three presets are launched
  // directly and never `claim`, so "zero open claims after a stop" is true by construction — and
  // the assertion that matters is that the drain took none in the first place. What a stop must
  // leave is nothing in flight and nothing to relaunch.
  const answer = await start({ concurrent: 2 });
  const drainId = String(answer["drainId"]);
  expect(await harness.db.query(`SELECT id FROM claims`)).toEqual([]);

  const halted = await halt(drainId, "the operator stopped it");
  expect(halted["cancelled"]).toBe(2);
  expect(code.cancelled).toEqual([`omp_${drainId}_0`, `omp_${drainId}_1`]);
  // Both cancels landed, so the drain is closing on two receipts rather than finished.
  expect(halted["state"]).toBe("closing");
  const row = await readDrain(harness.store, drainId);
  expect(row?.state).toBe("closing");
  expect(row?.ending).toBe("stopped");
  expect(row?.reason).toBe("the operator stopped it");
  expect(await harness.db.query(`SELECT id FROM claims`)).toEqual([]);

  // A STOPPED DRAIN IS NOT A PAUSED ONE: the tick behind it folds and never launches, and this is
  // what five rounds of kill-and-restart could not achieve on the day.
  const [report] = await drainTick(deps);
  expect(report?.launched).toBe(0);
  expect(report?.state).toBe("closing");
  expect(fleet.executed).toHaveLength(2);

  await settleJob(`run_${drainId}_0`);
  await settleJob(`run_${drainId}_1`);
  const [ended] = await drainTick(deps);
  expect(ended?.state).toBe("stopped");
  expect((await readDrain(harness.store, drainId))?.state).toBe("stopped");
  expect(await drainTick(deps)).toEqual([]);
  expect(String((await halt(drainId))["refused"])).toMatch(/already ended as stopped/);
});

test("a stop before the session is posted cancels the preparation and closes the intent row", async () => {
  /*
    THE OTHER HALF OF A CODE-SESSION STOP (#592). A run is started in two wakes: the press
    seals the material and records INTENT, and only the second wake posts the session and
    writes Code's job id. A stop that lands between them has no session to cancel — and the
    id it would have named, the run's derived identity, is not a job anywhere. What it must
    do instead is cancel BABEL'S OWN preparation and close the row, and the row is the half
    that matters: `postPrepared` posts a session for every open row whose material sealed, so
    an intent row left open outlives the drain and spends the account afterwards.
  */
  reachesCode = false;
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);

  const halted = await halt(drainId, "the operator stopped it");
  expect(halted["cancelled"]).toBe(1);
  // Not one word was said to Code: there is no session yet to say it about.
  expect(code.cancelled).toEqual([]);
  expect(fleet.cancelled).toEqual([
    {
      kind: "job",
      machineId: MACHINE,
      operationId: OPERATIONS.prepare,
      jobId: `job_${drainId}_0_material`,
    },
  ]);
  // The row is closed, which is what the posting wake reads.
  const rows = await harness.db.query<{ closure: string | null; job_id: string | null }>(
    `SELECT closure, job_id FROM runs WHERE id = ?`,
    [`run_${drainId}_0`],
  );
  expect(rows[0]).toEqual({ closure: "stopped", job_id: null });
});

test("a stop the hub will not honour keeps the job, and its later receipt still lands in spent", async () => {
  /*
    THE SPEND OF A JOB NOBODY COULD CANCEL (the review of #285, finding 4). A target or a deadline
    is met on a settle-woken tick whose credential holds no `jobs:cancel`, so the cancels are
    refused BY DESIGN and up to N−1 jobs keep running. A row that emptied `live` at the close made
    them nobody's: their receipts never reached `spent`, `closures` or `jobsSettled`, and the
    panel's final total — §11.5's "final totals from usage.inference" — was short by their spend.
  */
  const drainId = String(
    (await start({ concurrent: 2, target: { costMicros: 500_000 } }))["drainId"],
  );
  code.cancelRefusal = "jobs:cancel capability required at target";
  await settleJob(`run_${drainId}_0`, { costMicros: 600_000 });

  const [report] = await drainTick(deps);
  expect(report?.state).toBe("closing");
  expect(report?.notes.join(" ")).toMatch(/was not cancelled: jobs:cancel capability required/);
  expect((await readDrain(harness.store, drainId))?.live).toHaveLength(1);
  // The panel reads the whole spend while it closes: the settled receipt, and the job still out.
  expect((await statusOf(drainId))["state"]).toBe("closing");

  // The uncancelled job finishes on its own an hour later; its receipt is still this drain's.
  harness.at(NOW + HOUR);
  await settleJob(`run_${drainId}_1`, { costMicros: 900_000, outputTokens: 700 });
  const [after] = await drainTick(deps);
  expect(after?.state).toBe("target");
  const row = await readDrain(harness.store, drainId);
  expect(row?.spent.costMicros).toBe(1_500_000);
  expect(row?.jobsSettled).toBe(2);
  expect(row?.closures).toEqual({ completed: 2 });
  expect(row?.finishedAt).not.toBe("");
  const status = await statusOf(drainId);
  expect(status["spent"]).toMatchObject({ costMicros: 1_500_000 });
  expect(status["jobsSettled"]).toBe(2);
});

test("an operator's stop cancels the stragglers of a drain that already closed itself", async () => {
  const drainId = String(
    (await start({ concurrent: 2, target: { costMicros: 500_000 } }))["drainId"],
  );
  code.cancelRefusal = "jobs:cancel capability required at target";
  await settleJob(`run_${drainId}_0`, { costMicros: 600_000 });
  await drainTick(deps);
  expect((await readDrain(harness.store, drainId))?.state).toBe("closing");

  // The stop door is the one caller that holds `jobs:cancel` at the operation, and a closing
  // drain is exactly the one whose jobs a tick could not stop. Why it ended does not change.
  code.cancelRefusal = "";
  const halted = await halt(drainId, "kill the stragglers");
  expect(halted["cancelled"]).toBe(1);
  expect(halted["state"]).toBe("closing");
  const row = await readDrain(harness.store, drainId);
  expect(row?.ending).toBe("target");
  expect(row?.reason).toMatch(/target of 500000/);
  expect(code.cancelled).toEqual([`omp_${drainId}_1`]);
});

test("a stop folds the receipt that landed since the last tick instead of closing over it", async () => {
  /*
    THE RECEIPT IN THE WINDOW (the re-review of #285, finding 1). A job's closure is written by
    the conductor and folded by the drain's own tick, and between those two writes — one cycle's
    window, or a settle hook cut off by its two-second lease — the operator can press stop. A door
    that closed the row on what was still RUNNING wrote `live` without that job in it, so no later
    tick could ever reach it: the drain reported 900000 of the 1500000 it had metered.
  */
  const drainId = String((await start({ concurrent: 2 }))["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 600_000, outputTokens: 500 });

  const halted = await halt(drainId, "the window is about to reset");
  expect(halted["state"]).toBe("closing");
  // The settled job is folded, not cancelled: only the one still running is asked to stop.
  expect(code.cancelled).toEqual([`omp_${drainId}_1`]);
  const closing = await readDrain(harness.store, drainId);
  expect(closing?.spent.costMicros).toBe(600_000);
  expect(closing?.jobsSettled).toBe(1);
  expect(closing?.closures).toEqual({ completed: 1 });
  expect(closing?.live.map((job) => job.jobId)).toEqual([`job_${drainId}_1_material`]);

  // …and the straggler's own receipt lands on top of it, so the final total is the whole spend.
  harness.at(NOW + 5 * 60_000);
  await settleJob(`run_${drainId}_1`, { costMicros: 900_000, outputTokens: 700 });
  const [after] = await drainTick(deps);
  expect(after?.state).toBe("stopped");
  const row = await readDrain(harness.store, drainId);
  expect(row?.spent.costMicros).toBe(1_500_000);
  expect(row?.spent.outputTokens).toBe(1_200);
  expect(row?.jobsSettled).toBe(2);
  expect((await statusOf(drainId))["spent"]).toMatchObject({ costMicros: 1_500_000 });
});

test("a stop that lands mid-launch keeps the job the hub already took, and ends behind it", async () => {
  /*
    THE JOB POSTED INTO A CLOSING DRAIN (the re-review of #285, finding 2). A launch is two writes
    — `jobs.execute`, then the row that records it — and a stop can land between them. A
    `recordLaunch` that only wrote under `running` made that job nobody's: it ran on the hub with
    a run row and off `live`, the stop answered for one job while two were out, and the drain took
    its ending on the straggler it knew about while the other was still at the model.
  */
  const drainId = String((await start({ concurrent: 2 }))["drainId"]);
  await settleJob(`run_${drainId}_0`, { costMicros: 200_000 });
  fleet.duringExecute = async (args) => {
    if (args.jobId !== `job_${drainId}_2`) return;
    fleet.duringExecute = null;
    await halt(drainId, "the operator stopped it");
  };
  await drainTick(deps);

  expect(fleet.executed.map((job) => job.jobId)).toEqual([
    `job_${drainId}_0`,
    `job_${drainId}_1`,
    `job_${drainId}_2`,
  ]);
  const closing = await readDrain(harness.store, drainId);
  expect(closing?.state).toBe("closing");
  // BOTH running jobs are on the row: the one the stop cancelled, and the one it could not know
  // about because the hub had only just taken it.
  expect(closing?.live.map((job) => job.jobId)).toEqual([
    `job_${drainId}_1_material`,
    `job_${drainId}_2_material`,
  ]);
  expect(closing?.jobsLaunched).toBe(3);

  // The cancelled job's receipt does not end the drain: it still holds the other one.
  await settleJob(`run_${drainId}_1`, { costMicros: 300_000 });
  const [held] = await drainTick(deps);
  expect(held?.state).toBe("closing");
  expect(held?.launched).toBe(0);
  expect((await readDrain(harness.store, drainId))?.live.map((job) => job.jobId)).toEqual([
    `job_${drainId}_2_material`,
  ]);

  await settleJob(`run_${drainId}_2`, { costMicros: 400_000 });
  const [ended] = await drainTick(deps);
  expect(ended?.state).toBe("stopped");
  const row = await readDrain(harness.store, drainId);
  expect(row?.live).toEqual([]);
  expect(row?.spent.costMicros).toBe(900_000);
  expect(row?.jobsSettled).toBe(3);
});

test("the status folds the live spend, the rate over the last three minutes, and the ETA", async () => {
  const drainId = String(
    (await start({ concurrent: 2, target: { costMicros: 4_000_000 } }))["drainId"],
  );

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
  const drainId = String(
    (await start({ concurrent: 1, target: { costMicros: 9_000_000 } }))["drainId"],
  );
  await settleJob(`run_${drainId}_0`, {
    costMicros: 300_000,
    reason: "schema: no outcome on the claim",
  });
  await drainTick(deps);
  const row = await readDrain(harness.store, drainId);
  expect(row?.refusals).toEqual({ schema: 1 });
  expect(row?.spent.costMicros).toBe(300_000);
  expect((await statusOf(drainId))["refusals"]).toEqual({ schema: 1 });
});

test("a start that can launch nothing refuses, and leaves a failed drain that says why", async () => {
  fleet.refusal = "dev-01 refused the job: concurrency_limit";
  const refused = String((await start())["refused"]);
  expect(refused).toMatch(/launched nothing/);
  expect(refused).toMatch(/concurrency_limit/);
  // The row is written before the first post and closed when none lands, so what survives is a
  // `failed` drain that says why rather than a `running` one holding nothing. It holds no job, so
  // it ends outright rather than closing on one.
  const rows = await harness.db.query<{ state: string; reason: string; finished_at: string }>(
    `SELECT state, reason, finished_at FROM drains`,
  );
  expect(rows[0]?.state).toBe("failed");
  expect(rows[0]?.reason).toMatch(/concurrency_limit/);
  expect(rows[0]?.finished_at).not.toBeNull();
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);
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

function realLaunch() {
  const machinery = launchMachinery(harness.store, {
    coordinator: deps.coordinator,
    drainAdmission: (machineId, operationId) => deps.admission(machineId, operationId),
    jobs: () => fleet,
    engine: () => deps.engine,
    cookbook: async () =>
      await Promise.resolve({
        "code-health": { id: "code-health", version: 3, body: "look for what keeps breaking" },
      }),
    plan: () => PLAN,
    now: () => harness.store.now(),
  });
  deps = { ...deps, launch: machinery };
  return machinery;
}

test("an ordinary preparation still occupies the machine after Stop and a fresh drain start", async () => {
  const machinery = realLaunch();
  physicalCores = 1;
  const policy = await harness.db.query(`SELECT payload FROM policies`);
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);
  expect(fleet.executed.map((job) => job.jobId)).toEqual([`job_${drainId}_0_material`]);
  await halt(drainId, "stop before Code");
  await drainTick(deps);
  expect(await start({ concurrent: 1 })).toHaveProperty("refused");
  expect(fleet.executed.map((job) => job.jobId)).toEqual([`job_${drainId}_0_material`]);
  expect(
    await harness.db.query(`SELECT closure FROM runs WHERE job_id=?`, [
      `job_${drainId}_0_material`,
    ]),
  ).toEqual([{ closure: null }]);
  expect(code.posted).toEqual([]);
  await sealDrainJob(drainId, 0);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted).toEqual([]); // A late seal never revives stopped result authority.
  const restart = await start({ concurrent: 1 });
  expect(restart).toMatchObject({ launched: 1 });
  expect(fleet.executed.map((job) => job.jobId)).toEqual([
    `job_${drainId}_0_material`,
    `job_${String(restart["drainId"])}_0_material`,
  ]);
  expect(await harness.db.query(`SELECT payload FROM policies`)).toEqual(policy);
});

async function sealDrainJob(drainId: string, ordinal: number): Promise<void> {
  await harness.db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ?, payload = ? WHERE job_id = ?`,
    [stamp(NOW), sealedMaterial(), `job_${drainId}_${String(ordinal)}_material`],
  );
}

test("a drain bounds each material to its fan's share of the machine's measured scratch", async () => {
  // The newest receipt measured 5000 bytes past headroom. Raw and sealed material coexist:
  // a fan of two has room for one 1000-byte capture per preparation, not two.
  await insert(harness.db, "runs", {
    id: "run_catalog_earlier",
    kind: PRESET_OPERATIONS["keep-going"],
    machine_id: MACHINE,
    job_id: "job_catalog_earlier",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW - HOUR),
    closure: "completed",
    records: 0,
    payload: JSON.stringify({
      closure: "completed",
      outputCapacity: { bytes: MATERIAL_HEADROOM_BYTES + 5000, free: 0 },
    }),
  });
  realLaunch();
  const handed = (job: JobLaunch | undefined): number =>
    PrepareInputSchema.parse(JSON.parse(String(job?.input["input"]))).captures.flatMap(
      (group) => group.sessions,
    ).length;

  // The door's own first fan…
  const drainId = String((await start({ concurrent: 2 }))["drainId"]);
  expect(fleet.executed.map(handed)).toEqual([1, 1]);
  // …and the fan a later tick refills.
  await settleJob(`run_${drainId}_0`, { costMicros: 0 });
  await settleJob(`run_${drainId}_1`, { costMicros: 0 });
  await drainTick(deps);
  expect(fleet.executed.map(handed)).toEqual([1, 1, 1, 1]);
});

test("a bounded fan recovers its interrupted native acknowledgement without buying a third Code job", async () => {
  const machinery = realLaunch();
  const inferenceLimits = { calls: 2, costMicros: 50_000 };
  const drainId = String((await start({ concurrent: 2, maxJobs: 2, inferenceLimits }))["drainId"]);
  // Slot and ordinal were committed before posting; only the acknowledgement was lost.
  await harness.db.run(`UPDATE drain_launches SET state = 'reserved' WHERE run_id = ?`, [
    `run_${drainId}_1`,
  ]);
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 1, live: 2 });
  await sealDrainJob(drainId, 0);
  await sealDrainJob(drainId, 1);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted.map((request) => request.inferenceLimits)).toEqual([
    inferenceLimits,
    inferenceLimits,
  ]);
  await settleJob(`run_${drainId}_0`, { costMicros: 0, outputTokens: 0 });
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 1, state: "running" });
  expect(code.cancelled).toEqual([]);
  await settleJob(`run_${drainId}_1`, { costMicros: 0, outputTokens: 0 });
  expect((await drainTick(deps))[0]?.state).toBe("target");
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted).toHaveLength(2);
  expect(fleet.executed).toHaveLength(2);
  expect((await readDrain(harness.store, drainId))?.jobsSettled).toBe(2);
});

test("a later wake replays reviewed limits and stops refilling at the cumulative bound", async () => {
  let machinery = realLaunch();
  const inferenceLimits = { calls: 1, outputTokens: 1000, costMicros: 75_000 };
  const drainId = String((await start({ concurrent: 1, maxJobs: 2, inferenceLimits }))["drainId"]);
  await sealDrainJob(drainId, 0);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  await settleJob(`run_${drainId}_0`, { costMicros: 0, outputTokens: 0 });

  machinery = realLaunch();
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 1, live: 1 });
  await sealDrainJob(drainId, 1);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  expect(code.posted.map((request) => request.inferenceLimits)).toEqual([
    inferenceLimits,
    inferenceLimits,
  ]);
  await settleJob(`run_${drainId}_1`, { costMicros: 30_000, outputTokens: 300 });
  expect((await drainTick(deps))[0]?.state).toBe("target");
  expect((await readDrain(harness.store, drainId))?.spent.costMicros).toBe(30_000);
  expect(code.posted).toHaveLength(2);
  expect(fleet.executed).toHaveLength(2);
  expect(code.cancelled).toEqual([]);
});

test("a drain's first slot and its relaunch post their preparations within prepare's own ceiling", async () => {
  // The drain plans each round for the operation its press posts (#449), and the hub judges that
  // posting against the MANIFEST's declaration — so the plan here is `runPlan` over the shipped
  // manifest, the way `server.ts` plans a real drain, rather than this file's one fixed answer.
  const manifest = PluginManifestSchema.parse(manifestJson);
  let machinery = realLaunch();
  const planned: DrainDeps["plan"] = (policy, operationId) =>
    runPlan({ manifest, policy, operationId });
  deps = { ...deps, plan: planned };
  const drainId = String((await start({ concurrent: 1, maxJobs: 2 }))["drainId"]);
  await sealDrainJob(drainId, 0);
  await machinery.postPrepared(fleet, code, PLAN, WAKE);
  await settleJob(`run_${drainId}_0`, { costMicros: 0, outputTokens: 0 });

  machinery = realLaunch();
  deps = { ...deps, plan: planned };
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 1 });

  const ceiling = manifest.machine?.operations[OPERATIONS.prepare]?.limits;
  expect(ceiling).toBeDefined();
  expect(fleet.executed.map((job) => job.operationId)).toEqual([
    OPERATIONS.prepare,
    OPERATIONS.prepare,
  ]);
  for (const job of fleet.executed) {
    expect(job.limits?.timeoutMs ?? Infinity).toBeLessThanOrEqual(ceiling?.timeoutMs ?? 0);
    expect(job.limits?.memoryBytes ?? Infinity).toBeLessThanOrEqual(ceiling?.memoryBytes ?? 0);
    expect(job.limits?.outputBytes ?? Infinity).toBeLessThanOrEqual(ceiling?.outputBytes ?? 0);
  }
});

test("an unresolved ordinary Code admission remains held across wakes and drain Stop", async () => {
  const machinery = realLaunch();
  const drainId = String((await start({ concurrent: 2, maxJobs: 2 }))["drainId"]);
  await sealDrainJob(drainId, 0);
  await sealDrainJob(drainId, 1);
  const asked: (string | undefined)[] = [];
  const uncertain: CodeEngine = {
    ...code,
    runSession: async (request) => {
      asked.push(request.postingKey);
      throw new Error("Code posting response lost");
    },
  };
  await machinery.postPrepared(fleet, uncertain, PLAN, WAKE);
  // The same account's next wake asks each run's key again (#470); nothing else asks at all.
  await machinery.postPrepared(fleet, uncertain, PLAN, "enable:other");
  await machinery.postPrepared(fleet, uncertain, PLAN, WAKE);
  expect(asked.toSorted()).toEqual([
    `run_${drainId}_0`,
    `run_${drainId}_0`,
    `run_${drainId}_1`,
    `run_${drainId}_1`,
  ]);
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 2, state: "running" });
  await halt(drainId);
  expect(code.cancelled).toEqual([]);
  expect(fleet.cancelled).toEqual([]);
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 2, state: "closing" });
  expect(
    await harness.db.query(`SELECT closure FROM runs WHERE kind = ?`, [OPERATIONS.explore]),
  ).toEqual([{ closure: null }, { closure: null }]);
  expect(await readDrainReport(harness.store, drainId)).toBeNull();
  expect(fleet.executed).toHaveLength(2);
});

test("a lost posting whose drain was stopped is retired and never posted, and the drain then ends", async () => {
  const machinery = realLaunch();
  const drainId = String((await start({ concurrent: 1, maxJobs: 1 }))["drainId"]);
  await sealDrainJob(drainId, 0);
  // The first post throws before Code created anything, and the operator stops the drain.
  const lost: CodeEngine = {
    ...code,
    runSession: async () => await Promise.reject(new Error("Code posting response lost")),
  };
  await machinery.postPrepared(fleet, lost, PLAN, WAKE);
  await halt(drainId);
  // The posting account's own next wake may no longer buy the session: it only retires the key.
  const asked: boolean[] = [];
  const spy: CodeEngine = {
    ...code,
    runSession: async (request) => {
      asked.push(request.adoptOnly === true);
      if (request.adoptOnly === true)
        return {
          ok: false,
          code: "engine_posting_unknown",
          refused: "engine_posting_unknown: nothing was posted under this key",
        };
      return await code.runSession(request);
    },
  };
  await machinery.postPrepared(fleet, spy, PLAN, WAKE);
  expect(asked).toEqual([true]);
  expect(code.posted).toEqual([]);
  expect(
    await harness.db.query(`SELECT closure FROM runs WHERE id = ?`, [`run_${drainId}_0`]),
  ).toEqual([{ closure: "failed" }]);
  // With its only run released, the closing drain ends.
  expect((await drainTick(deps))[0]).toMatchObject({ drainId, live: 0 });
  expect((await readDrain(harness.store, drainId))?.state).not.toBe("closing");
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
  expect(report?.reason).toMatch(/was disabled/);
  expect(report?.launched).toBe(0);
  // Its one job was cancelled, so it closes on that receipt and ends when it lands.
  expect(report?.state).toBe("closing");
  expect(code.cancelled).toEqual([`omp_${drainId}_0`]);
  await settleJob(`run_${drainId}_0`);
  const [after] = await drainTick(deps);
  expect(after?.state).toBe("stopped");
  expect((await readDrain(harness.store, drainId))?.state).toBe("stopped");
});

test("over the real launch path a drain's fan seals material, and the settle wake posts it", async () => {
  /*
    THE DRAIN AND THE BUTTON GO THROUGH ONE PATH (#279), which is why there is one answer and
    not two: whatever the operator's button does, the fan does. The controller above is
    exercised against a fake that posts, because keeping a fan filled is not a claim about who
    posts; this is the claim about who posts.

    And the press no longer reaches Code at all (#592): it seals the material and records the
    intent, and the SESSION is posted by `postPrepared` on the wake that preparation's own
    settlement causes. So this drain STARTS, and what Code says is said there.
  */
  const machinery = launchMachinery(harness.store, {
    coordinator: deps.coordinator,
    drainAdmission: (machineId, operationId) => deps.admission(machineId, operationId),
    jobs: () => fleet,
    engine: () => deps.engine,
    cookbook: async () =>
      await Promise.resolve({
        "code-health": { id: "code-health", version: 3, body: "look for what keeps breaking" },
      }),
    plan: () => PLAN,
    now: () => harness.store.now(),
  });
  deps = { ...deps, launch: machinery };

  const answer = await start({
    concurrent: 2,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });

  // TWO PREPARATIONS POSTED, and nothing asked of Code yet.
  expect(answer["launched"]).toBe(2);
  expect(fleet.executed.map((job) => job.operationId)).toEqual([
    OPERATIONS.prepare,
    OPERATIONS.prepare,
  ]);
  const drainId = String(answer["drainId"]);
  const waiting = await harness.db.query<{ id: string; job_id: string | null }>(
    `SELECT id, job_id FROM runs WHERE kind = ? ORDER BY id`,
    [OPERATIONS.explore],
  );
  expect(waiting.map((row) => row.id)).toEqual([`run_${drainId}_0`, `run_${drainId}_1`]);
  expect(waiting.every((row) => row.job_id === null)).toBe(true);

  // THE SETTLE WAKE IS WHERE THE MATERIAL BINDING IS MADE. A settled preparation with no
  // material in its receipt closes its run for that reason; one that sealed an index reaches
  // `runSession` with the binding on the request.
  await harness.db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ?, payload = ? WHERE job_id = ?`,
    [stamp(NOW), sealedMaterial(), `job_${drainId}_0_material`],
  );

  const posted = await machinery.postPrepared(fleet, deps.engine, PLAN, WAKE);

  // THE SESSION IS POSTED, and the run takes CODE'S job id: that pair — the container and
  // this job — is the whole of how the conductor reconciles a job `ctx.jobs` cannot read.
  expect(posted).toEqual([{ runId: `run_${drainId}_0`, jobId: "omp_1" }]);
  const row = await harness.db.query<{ job_id: string | null; container_id: string | null }>(
    `SELECT job_id, container_id FROM runs WHERE id = ?`,
    [`run_${drainId}_0`],
  );
  expect(row[0]).toEqual({ job_id: "omp_1", container_id: "ctr_workbench" });

  /*
    AND THE REQUEST CARRIED THE MATERIAL (ADR 0044). This is the assertion the whole lane
    exists for: the model reads `/inputs/material`, and what puts bytes there is this binding
    naming the SETTLED `prepare` job's own sealed output. A session posted without it would
    send a model to an empty directory and have Babel record the answer as evidence-backed.

    The fake parses what it was handed with CODE'S OWN published input schema, so a request
    Babel built that Code would refuse fails here rather than on a machine.
  */
  expect(code.posted).toHaveLength(1);
  expect(code.posted[0]?.inputs).toEqual([
    { name: "material", from: { jobId: `job_${drainId}_0_material`, output: "material" } },
  ]);
  /*
    The prompt names the file the MATERIAL ACTUALLY HOLDS — the index's own `file`, not a name
    the press derived from a selector before `prepare` had run — and the preparation it cites
    is the sealed one's content-addressed id. The DIGEST is not in the prompt on purpose: it
    is in `index.json`, which the prompt sends the model to read, because that is the one
    place a citation's digest is written and a second copy is a second answer to it.
  */
  expect(code.posted[0]?.prompt).toContain("sessions/0001-omp-s1.jsonl");
  expect(code.posted[0]?.prompt).toContain("prep-1");
  expect(code.posted[0]?.prompt).toContain('"sourceDigest"');
  // …and it fits Code's byte bound with room to spare, which is what `PROMPT_LIMIT` guards.
  expect(promptBytes(code.posted[0]?.prompt ?? "")).toBeLessThan(PROMPT_LIMIT);

  /*
    AND A RUN WHOSE ROW IS CLOSED IS NOT POSTED, however well its material sealed. A stop
    that lands while a run is still preparing cancels the preparation and closes the row —
    but a cancel is a request, and one that races the seal loses, so the preparation settles
    anyway. The closed row is what makes this wake pass it by; without it the drain the
    operator stopped posts a session, and the account spends after the stop.
  */
  await harness.db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ?, payload = ? WHERE job_id = ?`,
    [stamp(NOW), sealedMaterial(), `job_${drainId}_1_material`],
  );
  await harness.db.run(`UPDATE runs SET closure = 'stopped', finished_at = ? WHERE id = ?`, [
    stamp(NOW),
    `run_${drainId}_1`,
  ]);
  expect(await machinery.postPrepared(fleet, deps.engine, PLAN, WAKE)).toEqual([]);
  expect(code.posted).toHaveLength(1);
});

test("the session a wake posts quotes what the operator told Babel, and the run records which", async () => {
  /*
    THE MEMORY HALF OF `tell` (#331). The remarks were written by a door, read back by the
    `policy` door, and reached no run: the operator could say "stop proposing work on the
    staging queue" and the next run proposed it again. This is the whole path — a row he
    wrote, the prompt Code is handed, and the run row the receipt is built from.
  */
  const machinery = launchMachinery(harness.store, {
    coordinator: deps.coordinator,
    drainAdmission: (machineId, operationId) => deps.admission(machineId, operationId),
    jobs: () => fleet,
    engine: () => deps.engine,
    cookbook: async () =>
      await Promise.resolve({
        "code-health": { id: "code-health", version: 3, body: "look for what keeps breaking" },
      }),
    plan: () => PLAN,
    now: () => harness.store.now(),
  });
  deps = { ...deps, launch: machinery };
  for (const [id, text, at] of [
    ["stg_0001", "stop proposing work on the staging queue, it is going away", NOW - HOUR],
    ["stg_0002", "the restic lane is mine, do not file issues about it", NOW - 2 * HOUR],
  ] as const) {
    await insert(harness.db, "steering", {
      id,
      root_id: id,
      reply_to_id: null,
      seq: 1,
      actor_kind: "operator",
      actor_id: "operator",
      target_kind: null,
      target_id: null,
      text,
      recorded_at: stamp(at),
    });
  }

  const answer = await start({
    concurrent: 1,
    profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  });
  const drainId = String(answer["drainId"]);
  await harness.db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ?, payload = ? WHERE job_id = ?`,
    [stamp(NOW), sealedMaterial(), `job_${drainId}_0_material`],
  );
  await machinery.postPrepared(fleet, deps.engine, PLAN, WAKE);

  // HIS WORDS ARE IN THE PROMPT, quoted and attributed, newest first.
  const prompt = code.posted[0]?.prompt ?? "";
  expect(prompt).toContain("## What the operator has told Babel");
  expect(prompt).toContain("- stg_0001 (");
  expect(prompt).toContain('"stop proposing work on the staging queue, it is going away"');
  expect(prompt.indexOf("stg_0001")).toBeLessThan(prompt.indexOf("stg_0002"));

  // AND THE RUN ROW SAYS WHAT IT WAS TOLD, which is what the receipt is built from: a claim
  // this run makes can be read against the remarks that were in front of it.
  const row = await harness.db.query<{ preparation: string }>(
    `SELECT preparation FROM runs WHERE id = ?`,
    [`run_${drainId}_0`],
  );
  const asked = JSON.parse(String(row[0]?.preparation)) as {
    preset: string;
    steering: { carried: { id: string; text: string }[]; omitted: number };
  };
  expect(asked.preset).toBe("read-whats-new");
  expect(asked.steering.carried.map((remark) => remark.id)).toEqual(["stg_0001", "stg_0002"]);
  expect(asked.steering.omitted).toBe(0);
});

/*
  WHAT A DRAIN LEAVES BEHIND (#270).

  The 2026-09-13 drain was reconstructed by hand, hours later, out of `run_receipt.payload`,
  `/proc`, fan logs and attempt rows: 25.3M tokens for 70 runs, load 42 on 12 cores, 20 runs
  paid and refused, engines present for 13 of 134 minutes. Nothing Babel produced could have
  told Babel that. These two tests are the whole of the fix: a drain driven to its stop leaves
  ONE record, its payload answers the operator's questions from itself, and the corpus reaches
  it like any other frontier record.
*/

test("a drain driven to its stop leaves one record that answers what it cost, on whose account, against which duties, how much erroring and how much came out", async () => {
  const drainId = String(
    (await start({ concurrent: 2, recipes: ["code-health", "time-and-spend"] }))["drainId"],
  );

  /*
    ONE TICK WHILE IT RUNS, so the drain has a life to report rather than only two endpoints.
    Both jobs are at the model and one of them has said nothing for ninety seconds, which is
    the reading `run_progress` holds and DROPS the instant a run settles — a stall nothing else
    in the store will remember.
  */
  for (const ordinal of [0, 1]) {
    await harness.db.run(
      `INSERT INTO run_progress(run_id, job_id, stage, since, calls, input_tokens, output_tokens,
                                cache_tokens, cost_usd, last_model, stalled, updated_at)
       VALUES (?, ?, 'at the model', ?, 1, 5000, 100, 0, 0.1, 'claude-sonnet-4-5', ?, ?)`,
      [
        `run_${drainId}_${String(ordinal)}`,
        `job_${drainId}_${String(ordinal)}`,
        stamp(NOW),
        ordinal,
        stamp(NOW),
      ],
    );
  }
  harness.at(NOW + 60_000);
  await drainTick(deps);

  /*
    AND THE PREPARATION EACH LAUNCH REALLY POSTS. `startExplore` posts an
    `atyrode.babel.prepare` job at `<runId>_material` before the session exists, and that is
    where the 2026-09-13 drain's wall time actually went — engines present for 13 of 134
    minutes. A report that counted those rows as sessions would report the fan as busy and
    every preparation as a job that answered nothing.
  */
  for (const ordinal of [0, 1]) {
    await harness.db.run(
      `INSERT INTO runs(id, kind, machine_id, authority_kind, authority_id, started_at,
                        finished_at, closure, records, payload)
       VALUES (?, ?, ?, 'operator', 'operator', ?, ?, 'completed', 0, '{"closure":"completed"}')`,
      [
        `run_${drainId}_${String(ordinal)}_material`,
        OPERATIONS.prepare,
        MACHINE,
        stamp(NOW),
        stamp(NOW + 90_000),
      ],
    );
  }

  // One job settles with a result; the other is PAID WORK WITH NO RESULT, which is the
  // distinction the operator's "how much erroring" is actually about (#265).
  await settleJob(`run_${drainId}_0`, { costMicros: 400_000, outputTokens: 1_000 });
  await settleJob(`run_${drainId}_1`, {
    costMicros: 300_000,
    outputTokens: 200,
    reason: "unknown-reference: the claim cites a session the material never served",
  });
  // What the first job put in the corpus: the value half of "how much value came out".
  await harness.db.run(
    `INSERT INTO records(id, kind, root_id, seq, run_id, actor_kind, actor_id, title, created_at,
                         payload)
     VALUES ('fnd_0000000a', 'finding', 'fnd_0000000a', 0, ?, 'run', ?, 'a finding', ?, '{}')`,
    [`run_${drainId}_0`, `run_${drainId}_0`, stamp(NOW + 60_000)],
  );

  harness.at(NOW + 120_000);
  const stopped = await halt(drainId, "the window resets");
  expect(stopped["state"]).toBe("stopped");

  const rows = await harness.db.query<{
    id: string;
    kind: string;
    actor_kind: string;
    actor_id: string;
    run_id: string | null;
    payload: string;
  }>(`SELECT id, kind, actor_kind, actor_id, run_id, payload FROM records WHERE actor_id = ?`, [
    drainId,
  ]);
  expect(rows).toHaveLength(1);
  const record = rows[0]!;
  // A FRONTIER RECORD WITH AN EXPLICIT DRAIN PROVENANCE. `engine` is the actor because the
  // controller wrote it — no model was asked — and `run_id` is null because the report belongs
  // to the drain rather than to whichever of its jobs happened to settle last.
  expect(record.kind).toBe("finding");
  expect(record.actor_kind).toBe("engine");
  expect(record.run_id).toBeNull();
  const report = DrainReportSchema.parse(JSON.parse(record.payload));
  expect(report.provenance).toBe("drain");

  // WHAT IT COST, from the hub's own meter and not from the engine's word about itself.
  expect(report.tokens).toEqual({
    calls: 6,
    inputTokens: 40_000,
    outputTokens: 1_200,
    cacheReadTokens: 0,
    costMicros: 700_000,
  });

  // ON WHOSE ACCOUNT, as Code reported it when the drain started — the question nothing on the
  // machine could answer on 2026-09-13.
  expect(report.account).toBe("ctr_workbench: the-drain-account (as Code reported at start)");
  expect(report.accounts).toEqual([{ name: report.account, runs: 2, tokens: report.tokens }]);

  // AGAINST WHICH DUTIES, as named and as spent. Both runs carried both recipes, so the
  // per-duty figures overlap and the report says so rather than letting them read as a split.
  expect(report.allocation.named).toEqual(["code-health", "time-and-spend"]);
  expect(report.allocation.shared).toBe(true);
  expect(report.allocation.ran.map((lane) => [lane.name, lane.runs])).toEqual([
    ["code-health", 2],
    ["time-and-spend", 2],
  ]);

  // HOW MUCH ERRORING, by code and by reason, with the launch's own lane kept separate from
  // the submission's: a job that was never posted is not paid work with no result.
  expect(report.jobs).toEqual({
    launched: 2,
    reachedModel: 2,
    settled: 2,
    unsettled: 0,
    withoutRunRow: 0,
  });
  expect(report.refusals).toEqual({ "unknown-reference": 1 });
  expect(report.closures).toEqual({ completed: 2 });
  // The gap is the refused job and only that one: the other completed AND put a record in the
  // store, so it is not a gap at all. A report that counted every job it could not praise
  // would be the "errors: 20" nobody can act on.
  expect(report.gaps).toEqual([
    {
      reason: "refused:unknown-reference",
      jobs: 1,
      detail: "unknown-reference: the claim cites a session the material never served",
    },
  ]);

  // HOW MUCH VALUE CAME OUT, per million tokens, which is the figure that compares one drain
  // with the next.
  expect(report.produced.records).toBe(1);
  expect(report.produced.assessments).toBe(0);
  expect(report.produced.recordsPerMillionTokens).toBeCloseTo(1_000_000 / 41_200, 6);

  // THE LOAD BABEL CAN SEE, integrated over the drain's life, and the stall it would otherwise
  // have forgotten.
  expect(report.load.peakAtModel).toBe(2);
  expect(report.load.atModelMs).toBeGreaterThan(0);
  expect(report.load.atModelFraction).toBeGreaterThan(0);
  expect(report.notes.map((note) => note.kind)).toContain("stall");

  // WHERE THE WALL TIME WENT: the preparations are their own lane, never sessions that
  // answered nothing. This is the 13-of-134-minutes reading, from the store.
  expect(report.pipeline).toEqual({
    prepareRuns: 2,
    prepareWallMs: 180_000,
    sessionRuns: 2,
    sessionWallMs: 120_000,
  });

  // AND WHAT IT CANNOT ANSWER, said in the record rather than carried as a column of nulls.
  expect(report.unobserved.join(" ")).toContain("CPU load and memory");
  expect(report.unobserved.join(" ")).toContain("cache-write");

  // Written once: a second close of the same drain finds the record already there.
  await halt(drainId, "again");
  expect(
    await harness.db.query(`SELECT id FROM records WHERE actor_id = ?`, [drainId]),
  ).toHaveLength(1);

  // AND THE PANEL SHOWS IT beside the drain that left it (#270).
  const status = await statusOf(drainId);
  expect((status["report"] as { drainId: string } | null)?.drainId).toBe(drainId);
});

test("the report a drain leaves is a record the corpus reaches, so an explore can be given it", async () => {
  /*
    ELIGIBLE INPUT, SPELLED AS WHAT THIS DEPLOYMENT ACTUALLY DOES WITH A RECORD. A run is given
    a record through the projection every reader uses — the peel `store.record` builds, which is
    what the feed opens, what a review is composed from, and what a "Babel improves Babel"
    exploration is pointed at. A finding is one of the POST kinds (`store/feedindex.ts`), so the
    report is rankable, drawable and answerable by a proposal that ADDRESSES it, which is the
    shape of "what the next drain should change". A log in a column would be none of that.
  */
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);
  await settleJob(`run_${drainId}_0`);
  harness.at(NOW + 60_000);
  await halt(drainId, "that is enough");

  const id = drainReportId(drainId);
  const peel = await harness.store.record(id);
  expect(peel).not.toBeNull();
  expect(peel?.post.kind).toBe("finding");
  expect(peel?.post.id).toBe(id);
  // The one line a listing shows: what the drain was and what it came to.
  expect(peel?.claim.statement).toContain(drainId);

  // …and this drain's one job completed and put nothing in the store, which the report names
  // as its own reason rather than folding into the failures: a run that answered and produced
  // nothing is a different thing to look at from one that was refused.
  const report = await readDrainReport(harness.store, drainId);
  expect(report?.gaps).toEqual([
    {
      reason: "completed-empty",
      jobs: 1,
      detail: "the run completed and put no record and no assessment in the store",
    },
  ]);

  // A run may not edit it and may not delete it, which is what makes it evidence rather than a
  // log entry: the table's own triggers, the same ones every record is under.
  await expect(
    harness.db.run(`UPDATE records SET title = 'rewritten' WHERE id = ?`, [id]),
  ).rejects.toThrow(/never edited/u);
});

/*
  THE CORPUS INDEX AS A DUTY OF THE TICK (#337).

  Two properties, and both are about what a drain does BESIDE launching. The keyword half must
  advance on a wake that holds no service authority at all, because that half needs nobody's
  account and is what makes a search work on a deployment that installed nothing. The meaning half
  must be bounded per tick and resumable across ticks, because it is 6,038 service calls and a
  pass that tried to finish would hold the dispatch that woke it.
*/

async function claim(id: string, title: string, pattern: string): Promise<void> {
  await harness.db.run(
    `INSERT INTO records(id, kind, root_id, seq, run_id, actor_kind, actor_id, title, created_at,
                         payload)
     VALUES (?, 'finding', ?, 0, NULL, 'run', 'run_x', ?, ?, ?)`,
    [id, id, title, stamp(NOW), JSON.stringify({ pattern })],
  );
}

test("a tick with no service authority still brings the keyword index current", async () => {
  await claim("fnd_0000001a", "The drain stalls at zero", "the fan never drains");
  await claim("fnd_0000001b", "A window is unspent", "the account resets with the window full");
  // A store that reached this shape by addition holds the records and none of the terms.
  await harness.db.run("DELETE FROM record_terms");
  await start({ concurrent: 1 });

  const [report] = await drainTick(deps);
  expect(report?.notes.join(" ")).toMatch(/keyword index was rebuilt over 2 records/u);
  const terms = await harness.db.query<{ n: number }>("SELECT COUNT(*) AS n FROM record_terms");
  expect(Number(terms[0]?.n)).toBe(2);
  // Nothing was embedded, because this tick held no service authority — which is every background
  // wake — and no note claims otherwise.
  expect(await harness.db.query("SELECT record_id FROM record_vectors")).toEqual([]);
  expect(report?.notes.join(" ")).not.toMatch(/embedded/u);

  // And it does not rebuild again: the gap between the two counts is the whole condition.
  const [second] = await drainTick(deps);
  expect(second?.notes.join(" ")).not.toMatch(/keyword index/u);
});

test("the backfill rides the drain: bounded per tick, resumable across ticks, and journaled", async () => {
  for (const n of [1, 2, 3]) {
    await claim(
      `fnd_0000002${String(n)}`,
      `A window is unspent ${String(n)}`,
      "the drain runs dry",
    );
  }
  const asked: string[] = [];
  deps = {
    ...deps,
    embed: async (text: string) => {
      asked.push(text);
      return { model: "stub-embed-v1", values: [1, -1] };
    },
  };
  const drainId = String((await start({ concurrent: 1 }))["drainId"]);

  const [first] = await drainTick(deps);
  expect(first?.notes.join(" ")).toMatch(/3 records embedded by stub-embed-v1, 0 left/u);
  expect(asked).toHaveLength(3);
  const rows = await harness.db.query<{ n: number }>("SELECT COUNT(*) AS n FROM record_vectors");
  expect(Number(rows[0]?.n)).toBe(3);

  // A LATER TICK PAYS FOR NOTHING. The pending set is a query over the rows themselves, so there
  // is no cursor to be wrong and a drain that ticks for two hours does not re-embed a corpus.
  const before = asked.length;
  const [second] = await drainTick(deps);
  expect(asked).toHaveLength(before);
  expect(second?.notes.join(" ")).not.toMatch(/embedded/u);

  // WHAT IT COST IS ON THE DRAIN'S OWN ROW, which is what makes the report (#270) able to say it:
  // the index's rows say what the corpus reached and can never say which tick paid for them.
  const row = await readDrain(harness.store, drainId);
  expect(row?.journal.notes.map((note) => note.kind)).toContain("index");
});

test.each([
  {},
  { "read-whats-new": 0 },
  { "read-whats-new": -1 },
  { "keep-going": 1 },
  { "read-whats-new": Infinity },
])("a mixed drain rejects absent, nonpositive and nonspending weights: %j", async (allocation) => {
  const result = await start({ preset: undefined, allocation });
  expect(result["drainId"]).toBeUndefined();
  expect(fleet.executed).toEqual([]);
});

test("whole-item deficits follow uneven cost weights rather than the number of jobs or recipes", async () => {
  const answer = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "explore-topic": 3 },
    inferenceLimits: { costMicros: 300_000 },
    recipes: ["one", "two"],
    concurrent: 3,
    maxJobs: 40,
    target: { deadline: new Date(NOW + HOUR).toISOString() },
  });
  const id = String(answer["drainId"]);
  for (let wake = 0; wake < 40; wake += 1) {
    const row = (await readDrain(harness.store, id))!;
    if (row.state !== "running") break;
    for (const job of row.live) {
      const [run] = await harness.db.query<{ preset: string }>(
        `SELECT json_extract(preparation, '$.preset') AS preset FROM runs WHERE id = ?`,
        [job.runId],
      );
      const preset = run!.preset;
      await settleJob(job.runId, { costMicros: preset === "read-whats-new" ? 300_000 : 100_000 });
    }
    await drainTick(deps);
  }
  const row = (await readDrain(harness.store, id))!;
  expect(row).toMatchObject({ state: "target", jobsLaunched: 40, jobsSettled: 40, live: [] });
  const status = await statusOf(id);
  const allocation = status["allocation"] as {
    preset: string;
    incurredCostMicros: number;
    reservedCostMicros: number;
    share: number;
  }[];
  const total = allocation.reduce((sum, lane) => sum + lane.incurredCostMicros, 0);
  expect(total).toBe(row.spent.costMicros);
  expect(Math.abs(allocation[0]!.incurredCostMicros - total / 4)).toBeLessThanOrEqual(300_000);
  expect(allocation.map((lane) => lane.reservedCostMicros)).toEqual([0, 0]);
  const counts = await harness.db.query<{ preset: string; n: number }>(
    `SELECT preset, COUNT(*) AS n FROM drain_launches WHERE drain_id = ? GROUP BY preset ORDER BY preset`,
    [id],
  );
  expect(Number(counts.find((entry) => entry.preset === "explore-topic")?.n)).toBeGreaterThan(30);
  expect(await harness.db.query(`SELECT id FROM claims`)).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);
});

test("an empty high-weight preset yields its slot and reports its gap while eligible work runs", async () => {
  realLaunch();
  const answer = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "explore-topic": 9 },
    inferenceLimits: { costMicros: 100_000 },
    concurrent: 2,
    target: { costMicros: 9_000_000 },
  });
  const id = String(answer["drainId"]);
  const row = (await readDrain(harness.store, id))!;
  expect(row.live).toHaveLength(1);
  const status = await statusOf(id);
  expect(status["allocation"]).toMatchObject([
    { preset: "read-whats-new", unpricedJobs: 1, gap: "missing-price" },
    { preset: "explore-topic", incurredCostMicros: 0, gap: "no-eligible" },
  ]);
  await settleJob(row.live[0]!.runId, { costMicros: 100_000 });
  await drainTick(deps);
  expect((await readDrain(harness.store, id))?.live).toHaveLength(2);
  const started = await harness.db.query<{ preset: string }>(
    `SELECT preset FROM drain_launches WHERE drain_id = ? AND state = 'posted'`,
    [id],
  );
  expect(started.every((entry) => entry.preset === "read-whats-new")).toBe(true);
});

test("unpriced mixed work does not relaunch as free work and still stops at its deadline", async () => {
  const answer = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "explore-topic": 1 },
    inferenceLimits: { costMicros: 100_000 },
    target: { deadline: new Date(NOW + 60_000).toISOString() },
  });
  const id = String(answer["drainId"]);
  const row = (await readDrain(harness.store, id))!;
  await settleJob(row.live[0]!.runId, { costMicros: 0 });
  await harness.db.run(`UPDATE runs SET closure = 'completed', payload = '{}' WHERE id = ?`, [
    row.live[1]!.runId,
  ]);
  await drainTick(deps);
  expect(fleet.executed).toHaveLength(2);
  expect((await statusOf(id))["allocation"]).toMatchObject([
    { gap: "zero-price", unpricedJobs: 1 },
    { gap: "missing-price", unpricedJobs: 1 },
  ]);
  harness.at(NOW + 60_000);
  expect((await drainTick(deps))[0]?.state).toBe("deadline");
  expect(fleet.executed).toHaveLength(2);
});

test("simultaneous starts and settlement ticks cannot overcommit machine slots", async () => {
  const starts = await Promise.all([
    start({ concurrent: 2, maxJobs: 4 }),
    start({ concurrent: 2, maxJobs: 4 }),
  ]);
  const accepted = starts.filter((answer) => answer["drainId"] !== undefined);
  expect(accepted).toHaveLength(1);
  const id = String(accepted[0]!["drainId"]);
  await settleJob(`run_${id}_0`, { costMicros: 100_000 });
  await settleJob(`run_${id}_1`, { costMicros: 100_000 });
  await Promise.all([drainTick(deps), drainTick(deps)]);
  const row = (await readDrain(harness.store, id))!;
  expect(row).toMatchObject({ jobsLaunched: 4, jobsSettled: 2, spent: { costMicros: 200_000 } });
  expect(row.live.map((job) => job.runId)).toEqual([`run_${id}_2`, `run_${id}_3`]);
  expect(new Set(fleet.executed.map((job) => job.jobId)).size).toBe(4);
  await halt(id);
  await settleJob(`run_${id}_2`, { costMicros: 100_000 });
  await settleJob(`run_${id}_3`, { costMicros: 100_000 });
  await Promise.all([drainTick(deps), drainTick(deps)]);
  expect((await readDrain(harness.store, id))?.spent.costMicros).toBe(400_000);
  expect(fleet.executed).toHaveLength(4);
});

test("mixed admission refuses before launching when no durable initial cost reservation is named", async () => {
  const result = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "explore-topic": 1 },
  });
  expect(result["drainId"]).toBeUndefined();
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
});

test("a review allocation is refused unless the installed review route matches the drain", async () => {
  const result = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "review-backlog": 1 },
    inferenceLimits: { costMicros: 100_000 },
  });
  expect(String(result["refused"])).toMatch(/review route/);
  expect(fleet.executed).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
  expect(await harness.db.query(`SELECT version FROM policies`)).toHaveLength(1);
});

async function installReviewRoute(
  options: {
    recipeBody?: string;
    recordPayload?: string;
    batchSize?: number;
    concurrentPerMachine?: number;
  } = {},
) {
  const inForce = (await deps.coordinator.policy()).policy;
  const recipe = {
    id: "installed-review",
    version: 1,
    body: options.recipeBody ?? "Judge the supplied immutable record.",
  };
  await insert(harness.db, "policies", {
    version: "review-enabled",
    seq: 2,
    actor_id: "operator",
    reason: "review route",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...inForce,
      version: "review-enabled",
      activityWeights: { ...inForce.activityWeights, review: 1 },
      batchSize: options.batchSize ?? inForce.batchSize,
      concurrentPerMachine: options.concurrentPerMachine ?? inForce.concurrentPerMachine,
      review: {
        machineId: MACHINE,
        profile: PROFILE,
        recipes: [recipe],
        roleRecipes: {
          reception: recipe.id,
          evidence: recipe.id,
          challenge: recipe.id,
          comparison: recipe.id,
          outcome: recipe.id,
          relevance: recipe.id,
          filing: recipe.id,
          backlog: recipe.id,
        },
        stageRecipes: {},
      },
    }),
  });
  await insert(harness.db, "records", {
    id: "hyp_00000001",
    root_id: "hyp_00000001",
    seq: 0,
    kind: "hypothesis",
    actor_kind: "run",
    actor_id: "run_seed",
    title: "Synthetic mixed-drain subject",
    created_at: stamp(NOW - HOUR),
    payload: options.recordPayload ?? "{}",
  });
}

test("one mixed start admits coordinator review and explore without changing either standing budget", async () => {
  await installReviewRoute();
  const before = await harness.db.query(`SELECT version,payload FROM policies`);
  const result = await start({
    preset: undefined,
    allocation: { "read-whats-new": 1, "review-backlog": 3 },
    inferenceLimits: { costMicros: 100_000 },
    concurrent: 2,
    maxJobs: 2,
  });
  expect(result["drainId"]).toBeDefined();
  const row = (await readDrain(harness.store, String(result["drainId"])))!;
  expect(row.live).toHaveLength(2);
  expect(
    await harness.db.query(`SELECT preset FROM drain_launches WHERE drain_id=? ORDER BY ordinal`, [
      row.id,
    ]),
  ).toEqual([{ preset: "review-backlog" }, { preset: "read-whats-new" }]);
  expect(code.posted).toHaveLength(1);
  expect(code.posted[0]!.prompt).toContain("Judge the supplied immutable record.");
  expect(fleet.executed.map((job) => job.operationId)).toEqual([OPERATIONS.explore]);
  expect(await harness.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toHaveLength(1);
  expect(await harness.db.query(`SELECT version,payload FROM policies`)).toEqual(before);
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);
});

test.each(["recipe", "projection"])(
  "an oversized review %s refuses its unused slot and lets the other preset progress",
  async (oversized) => {
    await installReviewRoute(
      oversized === "recipe"
        ? { recipeBody: "x".repeat(PROMPT_LIMIT) }
        : { recordPayload: JSON.stringify({ statement: "x".repeat(PROMPT_LIMIT) }) },
    );
    const result = await start({
      preset: undefined,
      allocation: { "read-whats-new": 1, "review-backlog": 3 },
      inferenceLimits: { costMicros: 100_000 },
      concurrent: 1,
    });
    expect(result["drainId"]).toBeDefined();
    const row = (await readDrain(harness.store, String(result["drainId"])))!;
    expect(row.state).toBe("running");
    expect(row.live).toHaveLength(1);
    expect(
      await harness.db.query(
        `SELECT preset,state FROM drain_launches WHERE drain_id=? ORDER BY ordinal`,
        [row.id],
      ),
    ).toEqual([
      { preset: "review-backlog", state: "refused" },
      { preset: "read-whats-new", state: "posted" },
    ]);
    expect(await harness.db.query(`SELECT id FROM claims`)).toEqual([]);
    expect(code.posted).toEqual([]);
    expect(fleet.executed.map((job) => job.operationId)).toEqual([OPERATIONS.explore]);
    const restarted = {
      ...deps,
      coordinator: coordinator(harness.store, () => harness.store.now(), 64),
    };
    await Promise.all([drainTick(restarted), drainTick(restarted)]);
    expect(fleet.executed.map((job) => job.operationId)).toEqual([OPERATIONS.explore]);
    expect(await harness.db.query(`SELECT id FROM claims`)).toEqual([]);
  },
);

test("review refill and restart keep one durable cycle ceiling with a one-item batch and four machine slots", async () => {
  await installReviewRoute({ batchSize: 1, concurrentPerMachine: 4 });
  const policy = (await deps.coordinator.policy()).policy;
  const before = await harness.db.query(`SELECT version,payload FROM policies`);
  const result = await start({
    preset: undefined,
    allocation: { "review-backlog": 1 },
    inferenceLimits: { costMicros: 100_000 },
    concurrent: 4,
  });
  expect(result["drainId"]).toBeDefined();
  const id = String(result["drainId"]);
  const row = (await readDrain(harness.store, id))!;
  expect(code.posted).toHaveLength(1);
  expect(row.live).toHaveLength(1);
  expect((await deps.coordinator.spend()).byRun[id]).toBe(policy.perCycleCost);
  const parent = row.live[0]!;
  expect(parent.runId).not.toBe(id);
  await harness.db.run(`UPDATE runs SET closure='completed',payload=? WHERE id=?`, [
    JSON.stringify({ inference: { costMicros: policy.perCycleCost * 1_000_000 } }),
    parent.runId,
  ]);
  const restarted = {
    ...deps,
    coordinator: coordinator(harness.store, () => harness.store.now(), 64),
  };
  await Promise.all([drainTick(restarted), drainTick(deps)]);
  await drainTick({
    ...deps,
    coordinator: coordinator(harness.store, () => harness.store.now(), 64),
  });
  expect(code.posted).toHaveLength(1);
  expect((await readDrain(harness.store, id))!.live).toEqual([]);
  expect(await harness.db.query(`SELECT id FROM claims WHERE finished_at IS NULL`)).toEqual([]);
  expect((await restarted.coordinator.spend()).byRun[id]).toBe(policy.perCycleCost);
  expect(await harness.db.query(`SELECT version,payload FROM policies`)).toEqual(before);
  expect(await harness.db.query(`SELECT id FROM budgets`)).toEqual([]);
});

test("mapping start shares physical admission without entering its separately governed lane", async () => {
  await installReviewRoute();
  await harness.db.run(`UPDATE policies SET payload=json_set(payload, '$.mapping', json(?))`, [
    JSON.stringify({
      sourceMachineId: "source-machine",
      executorMachineId: MACHINE,
      profile: PROFILE,
      dailyCost: 1,
      generateRecipe: "installed-review",
      reviewRecipe: "installed-review",
    }),
  ]);
  let entered = false;
  doors = drainDoors(harness.store, {
    coordinator: deps.coordinator,
    deps: () => deps,
    startOrdinary: async () => ({ ok: true, notes: [] }),
    stopOrdinary: async () => {},
    startMapping: async () => {
      entered = true;
      return { launched: 1, notes: [] };
    },
    now: () => harness.store.now(),
  });
  physicalCores = 1;
  const request = {
    operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.mapPrepare },
    source: {
      kind: "service",
      machineId: "source-machine",
      serviceId: RECALL_SERVICE_ID,
      operationId: TRANSCRIPT_MAP_SERVICE_OPERATION,
    },
    profile: { kind: "container", containerId: PROFILE.containerId },
    concurrent: 2,
    target: { costMicros: 1_000_000 },
    reason: "bounded mapping window",
  };
  expect(await dispatch(ACTIONS.mapDrainStart, request)).toHaveProperty("refused");
  expect(entered).toBe(false);
  expect(await harness.db.query(`SELECT id FROM drains`)).toEqual([]);
  expect(await dispatch(ACTIONS.mapDrainStart, { ...request, concurrent: 1 })).toMatchObject({
    concurrent: 1,
    launched: 1,
  });
  expect(entered).toBe(true);
});

test("a later core shrink retains a paid fan and folds it down before admitting more", async () => {
  physicalCores = 3;
  const id = String(
    (
      await start({
        concurrent: 3,
        target: { costMicros: 9_000_000 },
      })
    )["drainId"],
  );
  physicalCores = 1;
  await settleJob(`run_${id}_0`, { costMicros: 100_000 });
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 2, state: "running" });
  await settleJob(`run_${id}_1`, { costMicros: 100_000 });
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 0, live: 1, state: "running" });
  await settleJob(`run_${id}_2`, { costMicros: 100_000 });
  expect((await drainTick(deps))[0]).toMatchObject({ launched: 1, live: 1, state: "running" });
  expect(fleet.executed).toHaveLength(4);
  expect(code.cancelled).toEqual([]);
  expect((await readDrain(harness.store, id))?.spent.costMicros).toBe(300_000);
});
