/*
  THE PLUGIN AS THE HOST DRIVES IT: a dispatch, an enable, and a job that settled.

  `defineServerPlugin` is inert when the module is not an isolate's entry, so the definition
  this file imports is exactly the one a hub loads — the same doors, the same lifecycle, the
  same store facade over `ctx.database`. What it asserts is the wiring and nothing the slices
  already own: that a cycle follows the doors an operator watches and no others, that the one
  wake a background half gets is honoured for this plugin's jobs and ignored for anybody else's,
  and that the cycle behind either of them reaches the store through the handle that call was
  given.

  A settled job with no sealed output is enough to prove ingestion ran: the run row moves from
  open to closed and the claim it held is released, which is the whole of what settlement does
  to the store. What an archive holds once it is read is `server/conductor.test.ts`'s subject,
  at the depth it deserves.
*/

import { createHash } from "node:crypto";
import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx, GuestDatabase, GuestHookJobs } from "@manifold/plugin-kit/server";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import type { SqlParam, SqlStatement } from "@manifold/plugin";
import type { SettledJob } from "@manifold/protocol";
import {
  ACTIONS,
  asLaunchRequest,
  BABEL_PLUGIN_ID,
  MACHINE_OPERATIONS,
  MAP_DRAIN_PRESET,
  OPERATIONS,
  PRESET_OPERATIONS,
  RECALL_SERVICE_ID,
  RUN_STAGES,
  SessionRowSchema,
  TRANSCRIPT_MAP_SERVICE_OPERATION,
  TRANSCRIPT_MAP_SESSION_OPERATION,
} from "./contract.ts";
import { WAKES, plugin } from "./server.ts";
import { stamp } from "./store/feedindex.ts";
import { upsertSessionRows } from "./store/sessions.ts";
import { insert, openTestStore, type TestStore } from "./store/testdb.ts";
import { mappingPolicy, PolicySchema } from "./store/coordinator.ts";
import { transcriptMaps } from "./store/transcript-maps.ts";
import { buildTranscriptMap } from "./machine/transcript-map-tree.ts";
import { transcriptMapCaptureId } from "./transcript-map-identity.ts";
import { insertDrain } from "./store/drains.ts";
import { CONDUCTOR_SCHEDULE_ID, type JobLaunch, type ScheduleTiming } from "./server/conductor.ts";

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";
const RECORD = "fnd_00000001";

class Jobs {
  described = 0;
  statuses = 0;
  listed = 0;
  /** Native bridge refusals, retained only so a test can name the attenuated capability. */
  readonly refused: string[] = [];
  followed = 0;
  /** The cadences this fake has been asked to register, newest last, as `schedules()` lists them. */
  readonly scheduled: {
    scheduleId: string;
    revision: string;
    machineId: string;
    intervalMs?: number;
    expiresAt?: number;
  }[] = [];

  describe(): unknown {
    this.described += 1;
    return {
      connected: true,
      operations: {
        [PRESET_OPERATIONS["keep-going"]]: { ready: true, reason: null },
        [MACHINE_OPERATIONS.verify]: { ready: true, reason: null },
      },
      installation: {
        revision: "rev-7",
        artifactSha256: "a".repeat(64),
        enabled: true,
        ready: true,
      },
    };
  }

  /**
   * The job a run row is waiting on. `job_live` exited cleanly with nothing sealed; `job_running`
   * is still going, which is the only case a cycle can fold progress for.
   */
  status(node: { jobId: string }): unknown {
    this.statuses += 1;
    if (node.jobId === "job_running") {
      return {
        jobId: "job_running",
        machineId: MACHINE,
        operationId: OPERATIONS.prepare,
        state: "running",
        result: null,
      };
    }
    return {
      jobId: "job_live",
      machineId: MACHINE,
      operationId: OPERATIONS.prepare,
      state: "exited",
      result: { state: "exited", exitCode: 0, reason: null, outputs: [] },
    };
  }

  /**
   * The replay ring of a running job, as `follow` answers with it: where the job says it is, and
   * every call the owner metered. A fold takes the snapshot and closes the subscription.
   */
  follow(_node: unknown, _receive: () => void): unknown {
    this.followed += 1;
    return {
      snapshot: {
        events: [
          { seq: 1, event: { type: "job_progress", stage: RUN_STAGES.atModel, at: NOW } },
          {
            seq: 2,
            event: {
              type: "inference_call",
              model: "claude-sonnet-4-5",
              inputTokens: 4_000,
              outputTokens: 120,
              cachedInputTokens: 0,
              costMicros: 90_000,
            },
          },
        ],
        firstSeq: 1,
        unavailable: null,
      },
      close: () => {},
    };
  }

  listRuns(): unknown {
    this.listed += 1;
    return { runs: [], nextCursor: null };
  }

  execute(_launch: JobLaunch): unknown {
    throw new Error("this test starts no job");
  }

  output(): unknown {
    throw new Error("this job sealed no output");
  }

  cancel(): void {
    throw new Error("this test cancels nothing");
  }

  /*
    The three verbs #534 serves a hardened half. The loop reads the list, finds no cadence of
    its own, and registers one — which is what an enable under the installer's credential is
    for, and what this fake makes observable.
  */
  schedules(): unknown {
    return [...this.scheduled];
  }

  schedule(args: {
    scheduleId: string;
    revision: string;
    machineId: string;
    intervalMs?: number;
    expiresAt?: number;
  }): unknown {
    this.scheduled.push({
      scheduleId: args.scheduleId,
      revision: args.revision,
      machineId: args.machineId,
      ...(args.intervalMs === undefined ? {} : { intervalMs: args.intervalMs }),
      ...(args.expiresAt === undefined ? {} : { expiresAt: args.expiresAt }),
    });
    return {};
  }

  disableSchedule(args: { scheduleId: string; revision: string }): unknown {
    const at = this.scheduled.findIndex(
      (row) => row.scheduleId === args.scheduleId && row.revision === args.revision,
    );
    if (at >= 0) this.scheduled.splice(at, 1);
    return {};
  }
}

let harness: TestStore;
let jobs: Jobs;
/** The host's clock, a fresh hour per dispatch: the module remembers when it last woke. */
let clock = NOW;
/** Every key the plugin has written, as the host would hold them: one store for the process. */
const held: Record<string, string> = {};

/**
 * One dispatch's context. `now` is the host's clock for that call, and it matters: the floor
 * under a dispatch-driven cycle is measured on it, so two dispatches inside the same half
 * minute are one wake. Each context takes the next hour unless a test is about the floor
 * itself, which keeps the tests independent of the order they run in.
 */
function context(
  database: GuestDatabase | undefined,
  slice: Jobs,
  now: number = (clock += HOUR),
): GuestCtx {
  return {
    pluginId: BABEL_PLUGIN_ID,
    principal: { id: "operator" },
    database,
    jobs: slice as unknown as GuestHookJobs,
    // Only a DISPATCH is served one (`serveCtxCall`), and this plugin asks it one question: what
    // a folder a catalogued session worked in is. Nothing here catalogues one, so nothing asks.
    machines: {
      repository: async () =>
        await Promise.resolve({ ok: false, reason: "this test enrolls no machine" }),
    },
    // The keys a call is served: the schema marker the enable writes, and the day's tally the
    // loop keeps. One map for the process, because the plugin's keys outlive a dispatch.
    storage: {
      get: async (key: string) => await Promise.resolve(held[key] ?? null),
      set: async (key: string, value: string) => {
        held[key] = value;
        await Promise.resolve();
      },
    },
    newId: async () => await Promise.resolve("000001"),
    now: () => now,
  } as unknown as GuestCtx;
}

/**
 * The native bridge checks every declared requirement when executing or scheduling a job.
 * Read those requirements from the posted operation, so this boundary cannot silently admit
 * a drain or verification that the real host refuses for its locations, service or network.
 */
function postingRequires(operationId: string): readonly string[] {
  const operation = plugin.manifest.machine?.operations[operationId];
  if (operation === undefined) throw new Error(`the manifest declares no ${operationId}`);
  return [
    "machines:run",
    ...operation.locations.map((location) => `locations:${location.access}`),
    ...(operation.services ?? []).flatMap((binding) =>
      binding.operationIds.map(() => "services:invoke"),
    ),
    ...(operation.network === "host" ? ["network:host"] : []),
  ];
}

/** Requirements of verbs that do not post an operation. */
const VERB_CAPS: Record<string, readonly string[]> = {
  status: ["jobs:read"],
  follow: ["jobs:read"],
  listRuns: ["jobs:read"],
  describe: ["machines:read"],
};

/**
 * `ctx.jobs` AS THE HOST SERVES IT TO ONE DOOR'S DISPATCH: attenuated to what that action
 * declared (`plugin-host.ts` intersects the door's caps and delegates with the native set, and
 * `job-service.ts` refuses every verb whose capability is outside it by name). It is derived
 * from the plugin's OWN declaration, so a door added to `WAKES` without the delegates is served
 * a slice that cannot read a job or describe a machine — which is a cycle that settles nothing,
 * folds nothing and keeps no cadence, and is the whole reason the delegates are on the door
 * rather than assumed.
 */
function served(slice: Jobs, name: string): Jobs {
  const action = plugin.actions.find((entry) => entry.name === name);
  if (action === undefined) throw new Error(`no action ${name}`);
  const reach: readonly string[] = [...(action.caps ?? []), ...(action.delegates ?? [])];
  return new Proxy(slice, {
    get(target, key, receiver) {
      if (key === "execute" || key === "schedule") {
        const verb = Reflect.get(target, key, receiver) as (args: unknown) => unknown;
        return (args: { operationId: string }): unknown => {
          const missing = postingRequires(args.operationId).find((cap) => !reach.includes(cap));
          if (missing !== undefined) {
            target.refused.push(`job_capability_absent:${missing}`);
            throw new Error(`job_capability_absent:${missing}`);
          }
          return verb.call(target, args);
        };
      }
      const caps = typeof key === "string" ? VERB_CAPS[key] : undefined;
      const missing = caps?.find((cap) => !reach.includes(cap));
      if (missing !== undefined) {
        target.refused.push(`job_capability_absent:${missing}`);
        return (): never => {
          throw new Error(`job_capability_absent:${missing}`);
        };
      }
      return Reflect.get(target, key, receiver);
    },
  });
}

/** A run of this plugin's whose job is still RUNNING: the only case progress can be folded for. */
async function watched(): Promise<void> {
  await insert(harness.db, "runs", {
    id: "run_watched",
    kind: OPERATIONS.prepare,
    machine_id: MACHINE,
    job_id: "job_running",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
}

function settled(over: Partial<SettledJob> = {}): SettledJob {
  return {
    jobId: "job_live",
    machineId: MACHINE,
    operationId: OPERATIONS.prepare,
    pluginId: BABEL_PLUGIN_ID,
    state: "exited",
    exitCode: 0,
    reason: null,
    finishedAt: NOW,
    outputs: [],
    ...over,
  };
}

/** One run the hub is waiting on, and the claim that authorized it. */
async function pending(): Promise<void> {
  const { db } = harness;
  await insert(db, "runs", {
    id: "run_live",
    kind: OPERATIONS.prepare,
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
}

async function closure(): Promise<string | null> {
  const rows = await harness.db.query<{ closure: string | null }>(
    `SELECT closure FROM runs WHERE id = 'run_live'`,
  );
  return rows[0]?.closure ?? null;
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  jobs = new Jobs();
  // THE ROUTE IS PART OF THE POLICY, and the cadence is registered on the machine it names:
  // naming the box that will spend is what makes an enabled policy a complete authorization,
  // and `sessions.host` — a host NAME on an imported corpus — is not an identifier the hub can
  // be asked about.
  await insert(harness.db, "policies", {
    version: "p1",
    seq: 1,
    actor_id: "operator",
    reason: "on",
    payload: JSON.stringify({
      enabled: true,
      perCycleCost: 0.25,
      batchSize: 4,
      dailyCost: 2,
      review: {
        machineId: MACHINE,
        profile: { containerId: "ctr_workbench", expectedRevision: 1 },
        roleRecipes: {
          reception: "triage",
          evidence: "triage",
          challenge: "triage",
          comparison: "triage",
          outcome: "triage",
          relevance: "triage",
          filing: "triage",
          backlog: "triage",
        },
        recipes: [{ id: "triage", version: 1, body: "Assess the assigned record." }],
      },
    }),
    recorded_at: stamp(NOW - HOUR),
  });
});

afterEach(() => {
  harness.close();
});

test("the roster carries every door, and declares the settled-job hook", () => {
  const names = plugin.actions.map((action) => action.name);
  expect(names).toContain(ACTIONS.launch);
  expect(names).toContain(ACTIONS.stop);
  expect(new Set(names).size).toBe(names.length);
  for (const name of names) expect(Object.hasOwn(plugin.handlers, name)).toBe(true);
  expect(plugin.lifecycle?.onJobSettled).toBeDefined();
});

test("a settled job of this plugin's ingests what finished, through its own handle", async () => {
  await pending();

  await plugin.lifecycle?.onJobSettled?.(
    context(harness.db as unknown as GuestDatabase, jobs) as never,
    settled(),
  );

  // The cycle read the job back, closed the run and released what the claim reserved — and it
  // registered the beat on the way, because the hook's slice serves the schedule verbs the
  // settled job's own credential carries (#534).
  expect(jobs.described).toBeGreaterThan(0);
  expect(jobs.statuses).toBe(1);
  expect(jobs.scheduled).toMatchObject([{ scheduleId: `${BABEL_PLUGIN_ID}.conductor` }]);
  expect(await closure()).toBe("completed");
  const claim = await harness.db.query<{ outcome: string; actual_cost: number }>(
    `SELECT outcome, actual_cost FROM claims WHERE id = 'asg_live'`,
  );
  expect(claim[0]).toMatchObject({ outcome: "failed", actual_cost: 0 });
});

test("a settled job of another plugin's is not this one's to ingest", async () => {
  await pending();

  await plugin.lifecycle?.onJobSettled?.(
    context(harness.db as unknown as GuestDatabase, jobs) as never,
    settled({ pluginId: "core.terminals", jobId: "job_theirs" }),
  );

  expect(jobs.statuses).toBe(0);
  expect(jobs.described).toBe(0);
  expect(await closure()).toBeNull();
});

test("a settled job served without the plugin's tables fails by name rather than silently", async () => {
  await expect(
    plugin.lifecycle?.onJobSettled?.(context(undefined, jobs) as never, settled()),
  ).rejects.toThrow(/without the plugin's tables/);
});

test("the doors an operator watches run a cycle; the ones he reads with do not", async () => {
  await pending();
  const ctx = context(harness.db as unknown as GuestDatabase, jobs);

  await plugin.handlers[ACTIONS.feed]?.(ctx, {
    sort: "new",
    window: "day",
    limit: 5,
    offset: 0,
  } as never);
  expect(jobs.statuses).toBe(0);
  expect(await closure()).toBeNull();

  const pulse = await plugin.handlers[ACTIONS.pulse]?.(ctx, {} as never);

  // The answer is the door's own; the cycle behind it is what closed the finished run.
  expect(pulse).toMatchObject({ since: expect.any(String) });
  expect(jobs.statuses).toBe(1);
  expect(await closure()).toBe("completed");
});

test("every door a cycle follows can read a job, and the drain's own read folds a running one", async () => {
  /*
    THE WAKE THAT WAS BLIND (the review of #285, finding 1). `drainStatus` is in `WAKES` so the
    panel's poll folds where the running jobs are — that is the whole reason it is there, since a
    settlement's hook is served no `follow` and cannot fold a job that has not finished. But the
    slice a dispatch is served is attenuated to what its door declared, and a door without
    `jobs:read` is served one that refuses every job read: the cycle behind it settled nothing and
    folded nothing, and since `woke` is one floor shared by the `runs` and `drainStatus` pollers,
    roughly every other period's cycle was the blind one.
  */
  expect(Object.keys(WAKES)).toContain(ACTIONS.drainStatus);
  for (const name of Object.keys(WAKES)) {
    const action = plugin.actions.find((entry) => entry.name === name);
    expect([...(action?.caps ?? []), ...(action?.delegates ?? [])]).toContain("jobs:read");
  }

  await pending();
  await watched();
  const ctx = context(harness.db as unknown as GuestDatabase, served(jobs, ACTIONS.drainStatus));

  const answer = await plugin.handlers[ACTIONS.drainStatus]?.(ctx, { limit: 10 } as never);

  // The door's own answer is unchanged — it reads this plugin's tables — and the cycle behind it
  // did the two things a dispatch-woken cycle is for: it settled what had finished…
  expect(answer).toMatchObject({ drains: [] });
  expect(await closure()).toBe("completed");
  // …and it folded where the job still running is, which is what makes tokens-a-minute move at
  // all while an operator watches a drain.
  expect(jobs.followed).toBeGreaterThan(0);
  const progress = await harness.db.query<{
    stage: string;
    calls: number | bigint;
    output_tokens: number | bigint;
    cost_usd: number;
  }>(`SELECT stage, calls, output_tokens, cost_usd FROM run_progress WHERE run_id = 'run_watched'`);
  expect(progress[0]?.stage).toBe(RUN_STAGES.atModel);
  expect(Number(progress[0]?.calls)).toBe(1);
  expect(Number(progress[0]?.output_tokens)).toBe(120);
  expect(progress[0]?.cost_usd).toBeCloseTo(0.09, 6);
});

test.each([ACTIONS.pulse, ACTIONS.runs, ACTIONS.drainStatus])(
  "the cycle behind %s registers the beat under its declared authority",
  async (name) => {
    // A schedule checks locations, service invocation and host network as well as machine
    // execution. The door's own bridge must carry them before the cadence can be registered.
    await pending();
    const ctx = context(harness.db as unknown as GuestDatabase, served(jobs, name));
    const action = plugin.actions.find((entry) => entry.name === name)!;

    await plugin.handlers[name]?.(ctx, action.input.parse({}) as never);

    expect(jobs.described).toBeGreaterThan(0);
    expect(jobs.refused).toEqual([]);
    expect(jobs.scheduled).toMatchObject([
      { scheduleId: `${BABEL_PLUGIN_ID}.conductor`, machineId: MACHINE },
    ]);
  },
);

test("the doors that ask a machine what it can run are lent that read, and no others are", () => {
  /*
    WHO ASKS, AND THEREFORE WHO IS LENT IT. Every door a cycle follows asks: the conductor
    describes a machine to register the beat on it. `drainStart` asks on its own account too — it
    posts the fan's first slot through `launchMachinery`, and `ready` describes before it posts.
    `verify` asks for the same reason: it posts one of Babel's own jobs (#338) through the same
    path, and a verification aimed at a machine with no Babel on it should be refused at the
    press rather than by a job that never starts. The crossing's two owner-only doors ask for a
    different reason: `importLedger` and `rehostSessions` write a session's machine column, and a
    column carrying a name the hub does not know is provenance nothing can read back, so each
    checks the id against the hub before writing it (#312). Recall's owner setup describes
    the native service candidate and rechecks it on installation, and the two mapping starts
    describe the route's hosts before admitting it. Nothing else asks a machine anything: reading
    a feed, ruling on a record and stopping a run stay inside this plugin's own tables and job
    nodes.
  */
  const asks: Record<string, true> = {
    ...WAKES,
    [ACTIONS.drainStart]: true,
    [ACTIONS.verify]: true,
    [ACTIONS.importLedger]: true,
    [ACTIONS.rehostSessions]: true,
    [ACTIONS.previewRecall]: true,
    [ACTIONS.installRecall]: true,
    [ACTIONS.startMapCatalog]: true,
    [ACTIONS.mapDrainStart]: true,
  };
  for (const action of plugin.actions) {
    const reach = [...(action.caps ?? []), ...(action.delegates ?? [])];
    expect({ door: action.name, describes: reach.includes("machines:read") }).toEqual({
      door: action.name,
      describes: Object.hasOwn(asks, action.name),
    });
  }
  // And it is a DELEGATE everywhere it appears: a caller is never asked to hold a machine
  // capability to be told whether the machine Babel was deployed to is ready.
  for (const action of plugin.actions) {
    expect(action.caps).not.toContain("machines:read");
  }
});

test.each([
  {
    name: ACTIONS.launch,
    operationId: PRESET_OPERATIONS["keep-going"],
    args: { machineId: MACHINE, preset: "keep-going", minutes: 5 },
  },
  {
    name: ACTIONS.verify,
    operationId: MACHINE_OPERATIONS.verify,
    args: {
      machineId: MACHINE,
      operation: { kind: "operation", machineId: MACHINE, operationId: MACHINE_OPERATIONS.verify },
    },
  },
])(
  "$name posts and records its job under the door's own ceiling",
  async ({ name, operationId, args }) => {
    jobs.execute = (launch: JobLaunch): unknown => ({
      ...launch,
      state: "queued",
      result: null,
    });
    const ctx = context(harness.db as unknown as GuestDatabase, served(jobs, name));
    const action = plugin.actions.find((entry) => entry.name === name)!;
    const answer = await plugin.handlers[name]?.(ctx, action.input.parse(args) as never);

    expect(answer).toMatchObject({ runId: "run_000001", jobId: "job_000001", machineId: MACHINE });
    expect(jobs.refused).toEqual([]);
    expect(await harness.db.query(`SELECT kind, machine_id, job_id FROM runs`)).toEqual([
      { kind: operationId, machine_id: MACHINE, job_id: "job_000001" },
    ]);
  },
);

test("a second dispatch inside the floor is the same wake, not another cycle", async () => {
  await pending();
  const at = (clock += HOUR);
  const first = context(harness.db as unknown as GuestDatabase, jobs, at);

  await plugin.handlers[ACTIONS.runs]?.(first, { limit: 25, offset: 0 } as never);
  expect(jobs.statuses).toBe(1);

  // Watch polls `runs` every five seconds; a cycle behind each of them would be an alternate
  // scheduler built out of somebody else's poll.
  const soon = context(harness.db as unknown as GuestDatabase, jobs, at + 5_000);
  await plugin.handlers[ACTIONS.runs]?.(soon, { limit: 25, offset: 0 } as never);
  expect(jobs.statuses).toBe(1);

  // A fresh run for the cycle past the floor: the first one it closed, so what proves the
  // third dispatch woke the loop is the second run it read back.
  await insert(harness.db, "runs", {
    id: "run_next",
    kind: OPERATIONS.prepare,
    machine_id: MACHINE,
    job_id: "job_next",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null }),
  });
  const later = context(harness.db as unknown as GuestDatabase, jobs, at + 30_000);
  await plugin.handlers[ACTIONS.runs]?.(later, { limit: 25, offset: 0 } as never);
  expect(jobs.statuses).toBe(2);
});

test("a cycle that stumbles never fails the door it followed", async () => {
  await pending();
  const broken = new Jobs();
  broken.status = () => {
    throw new Error("the machine went away mid-answer");
  };
  const ctx = context(harness.db as unknown as GuestDatabase, broken);

  const runs = await plugin.handlers[ACTIONS.runs]?.(ctx, { limit: 25, offset: 0 } as never);

  expect(runs).toMatchObject({ total: 1 });
  // The loop noted what it could not read and left the run open for the next cycle.
  expect(await closure()).toBeNull();
});

test("enabling registers the beat with the slice the installer's credential restored", async () => {
  await pending();

  await plugin.lifecycle?.onEnable?.(
    context(harness.db as unknown as GuestDatabase, jobs) as never,
  );

  // An enabled policy owns a cadence, and #534 is what lets the enable itself register it
  // rather than waiting for a dispatch or a settlement to notice there is none. The rest of
  // the cycle ran under the same authority: the run the hub was waiting on is closed.
  expect(jobs.scheduled).toMatchObject([
    { scheduleId: `${BABEL_PLUGIN_ID}.conductor`, revision: "p1", machineId: MACHINE },
  ]);
  expect(jobs.statuses).toBe(1);
  expect(await closure()).toBe("completed");
});

test("an enable whose installer is gone is served no jobs, and still does the store's half", async () => {
  await pending();
  const ctx = context(harness.db as unknown as GuestDatabase, jobs);
  // `GuestLifecycleCtx.jobs` is absent exactly when the host could not restore the installer's
  // credential, and the hook is handed the context without it rather than a refusing handle.
  const orphaned = { ...ctx, jobs: undefined };

  await plugin.lifecycle?.onEnable?.(orphaned as never);

  // Nothing was asked of a machine — no beat, no job read back — and the run the hub is
  // waiting on is still waiting: what the cycle could do without a slice, it did.
  expect(jobs.scheduled).toEqual([]);
  expect(jobs.statuses).toBe(0);
  expect(await closure()).toBeNull();
});

test("enabling a store made before the catalog's two columns adds them and keeps its rows", async () => {
  const { db } = harness;
  // A store exactly as the first shape (`2026-09-12-store-v1`) left it: the tables are there,
  // and `sessions` has neither column. `planDataMigration` runs no chain for a MINOR version,
  // so if the enable did not add them here nothing ever would — and every session row naming
  // them would name a column the table has not got.
  await db.run(`ALTER TABLE sessions DROP COLUMN live`);
  await db.run(`ALTER TABLE sessions DROP COLUMN kind`);
  await insert(db, "sessions", {
    selector: "omp/older",
    host: MACHINE,
    harness: "omp",
    source_id: "older",
    title: "catalogued before the columns existed",
    seen_at: stamp(NOW - HOUR),
  });

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  // The row it already held is the operator's own and settled, which is what a default says.
  const older = await db.query<{ live: number; kind: string }>(
    `SELECT live, kind FROM sessions WHERE selector = 'omp/older'`,
  );
  expect(older[0]?.kind).toBe("operator");
  expect(Number(older[0]?.live)).toBe(0);

  // And a row that names both columns now lands, which is the whole point of the column.
  await insert(db, "sessions", {
    selector: "omp/run-7/explore",
    host: MACHINE,
    harness: "omp",
    source_id: "run-7/explore",
    title: "babel explore pass",
    live: 1,
    kind: "agent",
    seen_at: stamp(NOW),
  });

  // A second enable is the ordinary case — it runs on every one — and must do nothing.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  const kinds = await db.query<{ selector: string; kind: string }>(
    `SELECT selector, kind FROM sessions ORDER BY selector`,
  );
  expect(kinds.map((row) => [row.selector, row.kind])).toEqual([
    ["omp/older", "operator"],
    ["omp/run-7/explore", "agent"],
  ]);
});

test("enabling a store made before the trace adds run_calls, triggers and all", async () => {
  const { db } = harness;
  /*
    A store exactly as the shape before #349 left it. A WHOLE-OBJECT ADDITION is the only
    additive move the enable hook can make, and its name is DERIVED from the statement — so two
    things can go wrong and neither shows up in a fresh install: a name derived wrong runs the
    addition on every enable and fails on the second, and an addition naming only the table
    leaves an append-only ledger that appends by convention, which is the mistake `drains`
    documents having made with its index.
  */
  await db.run(`DROP TABLE run_calls`);

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  await insert(db, "runs", {
    id: "run_traced",
    kind: "atyrode.babel.explore",
    machine_id: MACHINE,
    started_at: stamp(NOW - HOUR),
    payload: "{}",
  });
  await insert(db, "run_calls", {
    run_id: "run_traced",
    seq: 1,
    recorded_at: stamp(NOW),
    closure: "completed",
  });
  expect(
    db.run(`UPDATE run_calls SET cost_micros = 1 WHERE run_id = 'run_traced'`),
  ).rejects.toThrow(/never edited/);
  expect(db.run(`DELETE FROM run_calls WHERE run_id = 'run_traced'`)).rejects.toThrow(
    /never deleted/,
  );

  // A second enable is the ordinary case — it runs on every one — and must leave the row alone
  // rather than fail on a table it has already made.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  expect(await db.query(`SELECT seq FROM run_calls WHERE run_id = 'run_traced'`)).toEqual([
    { seq: 1n },
  ]);
});

test.each(["missing tables", "older columns"] as const)(
  "enable upgrades %s atomically and keeps records through another enable",
  async (shape) => {
    const { db } = harness;
    if (shape === "missing tables") {
      await db.run(`DROP TABLE drains`);
      await db.run(`DROP TABLE run_progress`);
      await db.run(`DROP TABLE transcript_map_captures`);
    } else {
      await db.run(`ALTER TABLE drains DROP COLUMN profile`);
      await db.run(`ALTER TABLE run_progress DROP COLUMN models`);
      await db.run(`ALTER TABLE transcript_map_captures DROP COLUMN source_machine_id`);
    }
    await insert(db, "sessions", {
      selector: "omp/retained",
      host: MACHINE,
      harness: "omp",
      source_id: "retained",
      title: "retained across an additive upgrade",
      seen_at: stamp(NOW),
    });
    async function historicalRows(): Promise<void> {
      await insert(db, "drains", {
        id: "drn_retained",
        machine_id: MACHINE,
        preset: "keep-going",
        concurrent: 1,
        target: "{}",
        started_at: stamp(NOW - HOUR),
        started_by: "operator",
        finished_at: stamp(NOW),
        state: "stopped",
        jobs_settled: 7,
      });
      await insert(db, "run_progress", {
        run_id: "run_retained",
        job_id: "job_retained",
        since: stamp(NOW),
        updated_at: stamp(NOW),
        calls: 12,
      });
      await insert(db, "transcript_map_captures", {
        id: "capture_retained",
        host: MACHINE,
        harness: "omp",
        session: "retained",
        captured_at: stamp(NOW),
        payload: '{"historical":true}',
      });
    }
    if (shape === "older columns") await historicalRows();
    await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
    if (shape === "missing tables") await historicalRows();
    const profile = JSON.stringify({ containerId: "ctr_workbench", expectedRevision: 1 });
    const models = JSON.stringify(["synthetic/model"]);
    await db.run(`UPDATE drains SET profile = ? WHERE id = 'drn_retained'`, [profile]);
    await db.run(`UPDATE run_progress SET models = ? WHERE run_id = 'run_retained'`, [models]);
    await insert(db, "transcript_map_captures", {
      id: "capture_owned",
      host: MACHINE,
      harness: "omp",
      session: "owned",
      captured_at: stamp(NOW),
      payload: "{}",
      source_machine_id: MACHINE,
    });
    await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
    expect(
      await db.query(`SELECT profile, jobs_settled FROM drains WHERE id = 'drn_retained'`),
    ).toEqual([{ profile, jobs_settled: 7n }]);
    expect(
      await db.query(`SELECT models, calls FROM run_progress WHERE run_id = 'run_retained'`),
    ).toEqual([{ models, calls: 12n }]);
    expect(
      await db.query(
        `SELECT source_machine_id, payload FROM transcript_map_captures WHERE id = 'capture_retained'`,
      ),
    ).toEqual([{ source_machine_id: "", payload: '{"historical":true}' }]);
    expect(
      await db.query(
        `SELECT source_machine_id FROM transcript_map_captures WHERE id = 'capture_owned'`,
      ),
    ).toEqual([{ source_machine_id: MACHINE }]);
    expect(await db.query(`SELECT title FROM sessions WHERE selector = 'omp/retained'`)).toEqual([
      { title: "retained across an additive upgrade" },
    ]);
  },
);

test("enabling a store made before drains named a profile drops the session column, and a drain starts", async () => {
  const { db } = harness;
  /*
    A store exactly as the shape before #279 left `drains`: the account a drain spent was a
    `session` column, NOT NULL with no default, and there was no `profile`. #279 added `profile`
    and a writer that names only it, so on such a store every drain start was refused by a
    column nothing writes any more — which a fresh store, never having had it, could not show.
  */
  await db.run(`DROP TABLE drains`);
  await db.run(`CREATE TABLE drains(
     id TEXT PRIMARY KEY,
     machine_id TEXT NOT NULL,
     preset TEXT NOT NULL,
     session TEXT NOT NULL,
     knobs TEXT NOT NULL DEFAULT '{}',
     concurrent INTEGER NOT NULL CHECK (concurrent >= 1),
     target TEXT NOT NULL,
     started_at TEXT NOT NULL,
     started_by TEXT NOT NULL,
     finished_at TEXT,
     state TEXT NOT NULL DEFAULT 'running'
       CHECK (state IN ('running','closing','stopped','target','deadline','failed')),
     ending TEXT NOT NULL DEFAULT ''
       CHECK (ending IN ('','stopped','target','deadline','failed')),
     reason TEXT NOT NULL DEFAULT '',
     spent TEXT NOT NULL DEFAULT '{}',
     live TEXT NOT NULL DEFAULT '[]',
     samples TEXT NOT NULL DEFAULT '[]',
     closures TEXT NOT NULL DEFAULT '{}',
     refusals TEXT NOT NULL DEFAULT '{}',
     jobs_launched INTEGER NOT NULL DEFAULT 0,
     jobs_settled INTEGER NOT NULL DEFAULT 0,
     CHECK ((state IN ('running','closing')) = (finished_at IS NULL)),
     CHECK (state != 'closing' OR ending != '')
   ) STRICT`);
  const older = {
    id: "drn_older",
    machine_id: MACHINE,
    preset: "keep-going",
    knobs: JSON.stringify({ recipes: ["triage"] }),
    concurrent: 2n,
    target: JSON.stringify({ costMicros: 250_000 }),
    started_at: stamp(NOW - 2 * HOUR),
    started_by: "operator",
    finished_at: stamp(NOW - HOUR),
    state: "target",
    ending: "",
    reason: "the target was met",
    spent: JSON.stringify({
      calls: 3,
      inputTokens: 12_000,
      outputTokens: 400,
      costMicros: 270_000,
    }),
    live: "[]",
    samples: "[]",
    closures: JSON.stringify({ completed: 3 }),
    refusals: "{}",
    jobs_launched: 3n,
    jobs_settled: 3n,
  };
  const session = { model: "claude-sonnet-4-5", account: "synthetic-account" };
  await insert(db, "drains", { ...older, session: JSON.stringify(session) });
  // These legacy columns were plain TEXT, without JSON constraints. Preserve opaque values
  // too rather than silently dropping the only record of an older drain's choice.
  await insert(db, "drains", {
    ...older,
    id: "drn_opaque",
    knobs: "{legacy-not-json",
    session: "legacy-unstructured-choice",
  });

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  // A second enable is the ordinary case and must find nothing left to add or take away.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  // Start through the real door and its attenuated native bridge, not just the SQL writer.
  jobs.execute = (launch: JobLaunch): unknown => ({
    ...launch,
    state: "queued",
    result: null,
  });
  const ctx = {
    ...context(db as unknown as GuestDatabase, served(jobs, ACTIONS.drainStart)),
    actions: {
      call: async ({ action }: { action: string }) => {
        if (action !== "listProfiles") throw new Error(`unexpected Code action ${action}`);
        return await Promise.resolve({
          profiles: [
            {
              containerId: "ctr_workbench",
              revision: 1,
              selected: {
                model: "anthropic/claude-sonnet-4-5",
                thinking: "high",
                capability: 4,
                advisor: "review",
              },
              machineId: MACHINE,
              accounts: [{ provider: "anthropic", identityKey: "synthetic-account" }],
              resolved: true,
            },
          ],
        });
      },
    },
    emit: () => {},
  } as unknown as GuestCtx;
  const action = plugin.actions.find((entry) => entry.name === ACTIONS.drainStart)!;
  const request = action.input.parse({
    machineId: MACHINE,
    preset: "keep-going",
    profile: { containerId: "ctr_workbench", expectedRevision: 1 },
    concurrent: 1,
    maxJobs: 1,
    minutes: 5,
    target: {},
    reason: "a synthetic legacy-store start",
    operation: {
      kind: "operation",
      machineId: MACHINE,
      operationId: PRESET_OPERATIONS["keep-going"],
    },
  });
  const answer = await plugin.handlers[ACTIONS.drainStart]?.(ctx, request as never);
  expect(answer).toMatchObject({ machineId: MACHINE, preset: "keep-going", launched: 1 });
  expect(jobs.refused).toEqual([]);
  expect(await db.query(`SELECT state, jobs_launched FROM drains WHERE state = 'running'`)).toEqual(
    [{ state: "running", jobs_launched: 1n }],
  );

  // The old account choice remains historical evidence, not an invented Code profile.
  expect(await db.query(`SELECT * FROM drains WHERE id = 'drn_older'`)).toEqual([
    { ...older, profile: "{}", knobs: JSON.stringify({ recipes: ["triage"], session }) },
  ]);
  const opaque = await db.query<{ knobs: string }>(
    `SELECT knobs FROM drains WHERE id = 'drn_opaque'`,
  );
  expect(JSON.parse(opaque[0]!.knobs)).toEqual({
    legacyKnobs: "{legacy-not-json",
    session: "legacy-unstructured-choice",
  });
});

test("mapping-only methods are unavailable to ordinary exploration", async () => {
  const stored = await harness.db.query<{ payload: string }>(
    `SELECT payload FROM policies ORDER BY seq DESC LIMIT 1`,
  );
  const policy = PolicySchema.parse(JSON.parse(stored[0]!.payload));
  if (policy.review === undefined) throw new Error("the fixture has no review route");
  const mapping = {
    sourceMachineId: MACHINE,
    executorMachineId: MACHINE,
    profile: policy.review.profile,
    dailyCost: 1,
    generateRecipe: "map-generation-only",
    reviewRecipe: "map-review-only",
  };
  const methods = [mapping.generateRecipe, mapping.reviewRecipe];
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "separate navigation methods",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...policy,
      mapping,
      review: {
        ...policy.review,
        recipes: [
          ...policy.review.recipes,
          ...methods.map((id) => ({ id, version: 1, enabled: true, body: "Map this input." })),
        ],
      },
    }),
  });
  const ctx = context(harness.db as unknown as GuestDatabase, jobs);
  for (const id of methods) {
    const result = await plugin.handlers[ACTIONS.launch]?.(
      ctx,
      asLaunchRequest({
        machineId: MACHINE,
        preset: "read-whats-new",
        profile: policy.review.profile,
        recipes: [id],
      }) as never,
    );
    // The unavailable method is named, rather than proceeding to transcript selection.
    expect(result).toMatchObject({ refused: expect.stringContaining(id) });
  }
  const ordinary = await plugin.handlers[ACTIONS.launch]?.(
    ctx,
    asLaunchRequest({
      machineId: MACHINE,
      preset: "read-whats-new",
      profile: policy.review.profile,
      recipes: ["triage"],
    }) as never,
  );
  // An ordinary method is not refused for its method: it reaches transcript selection, which
  // this fixture's empty archive refuses by its own sentence.
  expect(ordinary).toMatchObject({ refused: expect.any(String) });
  expect(String((ordinary as { refused: string }).refused)).not.toContain("triage");
});

test("catalog admission refuses mismatched targets without waking ordinary or paid work", async () => {
  const action = plugin.actions.find((entry) => entry.name === ACTIONS.startMapCatalog)!;
  const handler = plugin.handlers[ACTIONS.startMapCatalog]!;
  const request = {
    operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.mapCatalog },
    target: {
      kind: "service",
      machineId: MACHINE,
      serviceId: RECALL_SERVICE_ID,
      operationId: TRANSCRIPT_MAP_SERVICE_OPERATION,
    },
  };
  const ctx = context(harness.db as unknown as GuestDatabase, jobs);
  await pending();
  const wrongMachine = await handler(
    ctx,
    action.input.parse({
      ...request,
      target: { ...request.target, machineId: "another-machine" },
    }) as never,
  );
  expect(wrongMachine).toHaveProperty("refused");
  // This machine has no mapping route. An admitted node is not permission to choose one.
  expect(await handler(ctx, action.input.parse(request) as never)).toHaveProperty("refused");
  expect(jobs.statuses).toBe(0);
  expect(jobs.scheduled).toEqual([]);
  expect(await closure()).toBeNull();
  expect(
    action.input.safeParse({
      ...request,
      operation: { ...request.operation, operationId: OPERATIONS.mapPrepare },
    }).success,
  ).toBe(false);
  expect(
    action.input.safeParse({ ...request, target: { ...request.target, operationId: "private" } })
      .success,
  ).toBe(false);
});

test("a refused launch does not become a conductor wake", async () => {
  await pending();
  const request = asLaunchRequest({ machineId: MACHINE, preset: "keep-going" });
  const refused = await plugin.handlers[ACTIONS.launch]!(
    context(harness.db as unknown as GuestDatabase, jobs),
    { ...request, operation: { ...request.operation, machineId: "another-machine" } } as never,
  );
  expect(refused).toHaveProperty("refused");
  expect(jobs.statuses).toBe(0);
  expect(jobs.scheduled).toEqual([]);
  expect(await closure()).toBeNull();
});

test("explicit catalog admission posts free work without settling or launching paid work", async () => {
  const rows = await harness.db.query<{ payload: string }>(
    `SELECT payload FROM policies ORDER BY seq DESC LIMIT 1`,
  );
  const policy = PolicySchema.parse(JSON.parse(rows[0]!.payload));
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "free catalog",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...policy,
      mapping: {
        sourceMachineId: "source-machine",
        executorMachineId: MACHINE,
        profile: { containerId: "ctr_workbench", expectedRevision: 1 },
        dailyCost: 0,
        generateRecipe: "triage",
        reviewRecipe: "triage",
      },
    }),
  });
  await pending();
  const executed: JobLaunch[] = [];
  const scheduled: (JobLaunch & ScheduleTiming)[] = [];
  const disabled: string[] = [];
  const native = {
    describe: () => ({
      connected: true,
      operations: {
        [OPERATIONS.mapCatalog]: {
          ready: true,
          reason: null,
          resourceBindingDigest: "b".repeat(64),
          serviceBindings: {
            [RECALL_SERVICE_ID]: {
              machineId: "source-machine",
              serviceId: RECALL_SERVICE_ID,
              revision: "source-revision-1",
              policySha256: "c".repeat(64),
            },
          },
        },
      },
      installation: {
        revision: "rev-7",
        artifactSha256: "a".repeat(64),
        enabled: true,
        ready: true,
      },
    }),
    execute: (request: JobLaunch) => {
      executed.push(request);
      return { ...request, state: "queued", result: null };
    },
    status: ({ jobId }: { jobId: string }) => {
      const request = executed.find((job) => job.jobId === jobId);
      if (!request) throw new Error("Unexpected job outside the catalog lane.");
      return { ...request, state: "queued", result: null };
    },
    schedules: () => scheduled.filter((row) => !disabled.includes(row.revision)),
    schedule: (request: JobLaunch & ScheduleTiming) => {
      scheduled.push(request);
      return {};
    },
    disableSchedule: ({ revision }: { revision: string }) => {
      disabled.push(revision);
      return {};
    },
  };
  const ctx = context(harness.db as unknown as GuestDatabase, jobs);
  const action = plugin.actions.find((entry) => entry.name === ACTIONS.startMapCatalog)!;
  for (const [executor, source] of [
    [MACHINE, MACHINE],
    ["wrong-executor", "source-machine"],
  ]) {
    expect(
      await plugin.handlers[ACTIONS.startMapCatalog]!(
        { ...ctx, jobs: native as unknown as GuestCtx["jobs"] },
        action.input.parse({
          operation: { kind: "operation", machineId: executor, operationId: OPERATIONS.mapCatalog },
          target: {
            kind: "service",
            machineId: source,
            serviceId: RECALL_SERVICE_ID,
            operationId: TRANSCRIPT_MAP_SERVICE_OPERATION,
          },
        }) as never,
      ),
    ).toHaveProperty("refused");
  }
  expect(executed).toEqual([]);
  let paidCalls = 0;
  const result = await plugin.handlers[ACTIONS.startMapCatalog]!(
    {
      ...ctx,
      jobs: native as unknown as GuestCtx["jobs"],
      actions: {
        call: () => {
          paidCalls += 1;
          throw new Error("A free catalog admission must not call Code.");
        },
      } as unknown as GuestCtx["actions"],
    },
    action.input.parse({
      operation: { kind: "operation", machineId: MACHINE, operationId: OPERATIONS.mapCatalog },
      target: {
        kind: "service",
        machineId: "source-machine",
        serviceId: RECALL_SERVICE_ID,
        operationId: TRANSCRIPT_MAP_SERVICE_OPERATION,
      },
    }) as never,
  );
  expect(result).not.toHaveProperty("refused");
  expect(executed.map((job) => job.operationId)).toEqual([OPERATIONS.mapCatalog]);
  expect(await closure()).toBeNull();
  expect(paidCalls).toBe(0);
  const intent = await harness.db.query<{ request: string; closure: string | null }>(
    `SELECT json_extract(preparation,'$.input.request.kind') AS request, closure
       FROM runs WHERE kind = ?`,
    [OPERATIONS.mapCatalog],
  );
  expect(intent).toEqual([{ request: "map-inventory", closure: null }]);
  expect(scheduled.map((job) => JSON.parse(String(job.input["input"])))).toEqual([
    { kind: "catalog-wake", sourceMachineId: "source-machine", executorMachineId: MACHINE },
  ]);
  expect(scheduled[0]!.offlinePolicy).toBe("coalesce-one");

  // A native wake neither registers a duplicate template nor posts duplicate work in flight.
  await plugin.lifecycle?.onJobSettled?.(
    { ...ctx, jobs: native } as never,
    settled({ machineId: MACHINE, operationId: OPERATIONS.mapCatalog }),
  );
  expect(scheduled).toHaveLength(1);
  expect(executed).toHaveLength(1);

  // A replacement source on the same executor also needs its own explicit admission.
  await insert(harness.db, "policies", {
    version: "p3",
    seq: 3,
    actor_id: "operator",
    reason: "replace source owner",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...policy,
      mapping: {
        sourceMachineId: "replacement-source",
        executorMachineId: MACHINE,
        profile: { containerId: "ctr_workbench", expectedRevision: 1 },
        dailyCost: 0,
        generateRecipe: "triage",
        reviewRecipe: "triage",
      },
    }),
  });
  await plugin.lifecycle?.onJobSettled?.(
    { ...ctx, jobs: native } as never,
    settled({ machineId: MACHINE, operationId: OPERATIONS.mapCatalog }),
  );
  expect(disabled).toEqual([scheduled[0]!.revision]);
  expect(executed).toHaveLength(1);
  expect(scheduled).toHaveLength(1);

  // Moving the policy cannot spend the old machine's continuation on the new machine.
  await insert(harness.db, "policies", {
    version: "p4",
    seq: 4,
    actor_id: "operator",
    reason: "move the mapping route",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...policy,
      mapping: {
        sourceMachineId: "source-machine",
        executorMachineId: "another-machine",
        profile: { containerId: "ctr_workbench", expectedRevision: 1 },
        dailyCost: 0,
        generateRecipe: "triage",
        reviewRecipe: "triage",
      },
    }),
  });
  await plugin.lifecycle?.onJobSettled?.(
    { ...ctx, jobs: native } as never,
    settled({ machineId: MACHINE, operationId: OPERATIONS.mapCatalog }),
  );
  expect(disabled).toEqual([scheduled[0]!.revision]);
  expect(executed.map((job) => job.machineId)).toEqual([MACHINE]);
  expect(scheduled.map((job) => job.machineId)).toEqual([MACHINE]);
});

/**
 * One wake of a running mapping drain's own job, against a hub that refuses a schedule expiring
 * past the registering credential's ceiling, `credential` ms from now (`job-schedules.ts`,
 * `schedule-expiry-ceiling`). The drain's admission deadline is a minute away and one of its
 * sessions is still at the model. The plugin's store reads the host's clock, so everything here
 * is measured from the moments around the wake. `settledJobId` is the preparation that settled:
 * the drain's own by default.
 */
async function drainCadence(
  credential: number,
  settledJobId = "job_live",
): Promise<{
  readonly expiries: readonly number[];
  readonly deadline: number;
  readonly before: number;
  readonly after: number;
}> {
  const rows = await harness.db.query<{ payload: string }>(
    `SELECT payload FROM policies ORDER BY seq DESC LIMIT 1`,
  );
  const policy = PolicySchema.parse(JSON.parse(rows[0]!.payload));
  const profile = { containerId: "ctr_workbench", expectedRevision: 1 };
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "paid mapping route",
    recorded_at: stamp(NOW),
    payload: JSON.stringify({
      ...policy,
      activityWeights: Object.fromEntries(
        Object.keys(policy.activityWeights).map((name) => [name, 0]),
      ),
      mapping: {
        sourceMachineId: "source-machine",
        executorMachineId: MACHINE,
        profile,
        dailyCost: 1,
        generateRecipe: "triage",
        reviewRecipe: "triage",
      },
    }),
  });
  const before = Date.now();
  const deadline = before + 60_000;
  const ceiling = before + credential;
  await insertDrain({ db: harness.db, now: () => NOW } as never, {
    id: "drn_map",
    machineId: MACHINE,
    preset: MAP_DRAIN_PRESET,
    profile: { profile, model: "synthetic", thinking: "low", accounts: [], resolved: true },
    knobs: { recipes: [] },
    concurrent: 1,
    target: { deadline: new Date(deadline).toISOString() },
    startedBy: "operator",
  });
  // A session of this drain is still at the model, so the drain holds rather than ending; its
  // preparation, `job_live`, is the job whose settlement wakes Babel.
  await insert(harness.db, "runs", {
    id: "run_at_model",
    kind: TRANSCRIPT_MAP_SESSION_OPERATION,
    machine_id: MACHINE,
    prepare_job_id: "job_live",
    started_at: stamp(NOW),
    records: 0,
    payload: "{}",
  });
  await harness.db.run(`UPDATE drains SET live=?, jobs_launched=1 WHERE id='drn_map'`, [
    JSON.stringify([{ runId: "run_at_model", jobId: "job_at_model", launchedAt: NOW }]),
  ]);
  const scheduled: (JobLaunch & ScheduleTiming)[] = [];
  const native = {
    describe: () => ({
      connected: true,
      operations: {
        [OPERATIONS.mapPrepare]: {
          ready: true,
          reason: null,
          resourceBindingDigest: "b".repeat(64),
          serviceBindings: {
            [RECALL_SERVICE_ID]: {
              machineId: "source-machine",
              serviceId: RECALL_SERVICE_ID,
              revision: "source-revision-1",
              policySha256: "c".repeat(64),
            },
          },
        },
      },
      installation: {
        revision: "rev-7",
        artifactSha256: "a".repeat(64),
        enabled: true,
        ready: true,
      },
    }),
    status: () => {
      throw new Error("this drain holds no job");
    },
    listRuns: () => ({ runs: [], nextCursor: null }),
    schedules: () => [...scheduled],
    schedule: (request: JobLaunch & ScheduleTiming) => {
      if (request.expiresAt > ceiling) throw new Error("schedule-expiry-ceiling");
      scheduled.push(request);
      return {};
    },
    disableSchedule: () => ({}),
  };
  // A wake of the drain's own job carries its authority and renews its cadence.
  await plugin.lifecycle?.onJobSettled?.(
    { ...context(harness.db as unknown as GuestDatabase, jobs, NOW), jobs: native } as never,
    settled({ jobId: settledJobId, machineId: MACHINE, operationId: OPERATIONS.mapPrepare }),
  );
  const after = Date.now();
  return {
    deadline,
    before,
    after,
    expiries: scheduled
      .filter((job) => JSON.parse(String(job.input["input"])).kind === "drain-wake")
      .map((job) => job.expiresAt),
  };
}

test("a mapping drain's own cadence outlives its admission deadline", async () => {
  // Settlement, not admission, decides when the cadence may stop.
  const { expiries, deadline } = await drainCadence(Number.MAX_SAFE_INTEGER / 2);
  expect(expiries).toHaveLength(1);
  expect(expiries[0]!).toBeGreaterThan(deadline);
});

test("a drain's cadence takes the longest life its credential allows past the deadline", async () => {
  // A credential ending in two days cannot hold a thirty-day or a seven-day cadence.
  const day = 24 * 60 * 60 * 1000;
  const { expiries, before, after } = await drainCadence(2 * day);
  expect(expiries).toHaveLength(1);
  expect(expiries[0]!).toBeGreaterThanOrEqual(before + day);
  expect(expiries[0]!).toBeLessThanOrEqual(after + day);
});

test("a wake of another drain's job never re-registers this drain's cadence", async () => {
  // A registration carries the credential that made it: another principal's wake must not
  // become the one this drain's sessions are later settled under (#470).
  const { expiries } = await drainCadence(Number.MAX_SAFE_INTEGER / 2, "job_of_another_drain");
  expect(expiries).toEqual([]);
});

test("a drain's cadence is never registered to end before its deadline is settled past", async () => {
  // Every rung the credential admits ends before the deadline has been passed by two intervals.
  const { expiries } = await drainCadence(90_000);
  expect(expiries).toEqual([]);
});

test("a settled map-prepare job wakes its drain's lane and never another lane's run", async () => {
  // A settled-job hook keeps its tables only for the host's lifecycle bound, so a preparation's
  // wake is not a whole cycle: the review run it would otherwise poll first stays unread, and
  // no beat is listed or registered on it.
  await pending();

  await plugin.lifecycle?.onJobSettled?.(
    context(harness.db as unknown as GuestDatabase, jobs) as never,
    settled({ jobId: "job_map_prepared", operationId: OPERATIONS.mapPrepare }),
  );

  expect(jobs.statuses).toBe(0);
  expect(jobs.listed).toBe(0);
  expect(jobs.scheduled).toEqual([]);
  expect(await closure()).toBeNull();
});

test("a map-prepare settlement spends only for the lane that posted it, never for an orphan", async () => {
  // Mapping is weighted and work is queued, so a wake that spent for the standing lane would
  // reach for the executor. An ended drain's late preparation belongs to no lane: its settlement
  // carries that drain's press credential, and must not spend it on the standing lane's work.
  const rows = await harness.db.query<{ payload: string }>(
    `SELECT payload FROM policies ORDER BY seq DESC LIMIT 1`,
  );
  const policy = PolicySchema.parse({
    ...PolicySchema.parse(JSON.parse(rows[0]!.payload)),
    version: "p2",
    activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0, map: 1 },
    mapping: {
      sourceMachineId: "source-machine",
      executorMachineId: MACHINE,
      profile: { containerId: "ctr_workbench", expectedRevision: 1 },
      dailyCost: 1,
      generateRecipe: "triage",
      reviewRecipe: "triage",
      segmentation: { leafBytes: 1024, directBytes: 0 },
    },
  });
  await insert(harness.db, "policies", {
    version: "p2",
    seq: 2,
    actor_id: "operator",
    reason: "standing mapping",
    recorded_at: stamp(NOW),
    payload: JSON.stringify(policy),
  });
  const route = mappingPolicy(policy)!;
  const now = new Date(NOW).toISOString();
  const digest = `sha256:${"a".repeat(64)}`;
  const scope = {
    digest,
    policyDigest: digest,
    classId: "private",
    ceiling: 2,
    eligibleCaptures: 1,
    observedAt: now,
  };
  const identity = {
    host: "synthetic",
    harness: "omp" as const,
    session: "synthetic-0",
    snapshot: "1".repeat(64),
    path: "/synthetic/0.jsonl",
    capturedAt: now,
  };
  const capture = { id: transcriptMapCaptureId(identity), ...identity };
  const access = { captureId: capture.id, contextDigest: digest, sensitivity: 2 };
  const bytes = Buffer.from(`${JSON.stringify({ role: "user", text: "a".repeat(700) })}\n`);
  const sha = (data: Uint8Array) => `sha256:${createHash("sha256").update(data).digest("hex")}`;
  const tree = await buildTranscriptMap({
    capture,
    captureDigest: sha(bytes),
    sourceDigest: sha(bytes),
    segmentation: route.segmentation,
    async replay(sink) {
      sink.write(bytes);
      await sink.close();
    },
    rangeDigest: async (offset, length) => sha(bytes.subarray(offset, offset + length)),
  });
  const maps = transcriptMaps(harness.store);
  const machineId = route.sourceMachineId;
  await maps.recordCatalog({
    machineId,
    context: scope,
    entries: [{ capture, access }],
    nextCursor: null,
    now,
  });
  await maps.recordPlan({
    machineId,
    context: scope,
    access,
    plan: tree.header,
    nodes: tree.nodes,
    offset: 0,
    nextOffset: null,
    now,
  });
  await maps.ensureVersion(tree.header.id, route, now);
  // The standing lane's own preparation, long settled, recorded under the chain that posted it.
  await insert(harness.db, "runs", {
    id: "run_standing",
    kind: TRANSCRIPT_MAP_SESSION_OPERATION,
    machine_id: MACHINE,
    job_id: "job_standing_session",
    prepare_job_id: "job_standing",
    closure: "completed",
    chain: "enable:standing",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ standing: true }),
  });
  // An ended drain's run: the same account chain, but no lane's. Only the lane decides.
  await insert(harness.db, "runs", {
    id: "run_of_an_ended_drain",
    kind: TRANSCRIPT_MAP_SESSION_OPERATION,
    machine_id: MACHINE,
    job_id: "job_ended_session",
    prepare_job_id: "job_of_an_ended_drain",
    closure: "stopped",
    chain: "enable:standing",
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: "{}",
  });
  const described: string[] = [];
  const native = {
    describe: (args: { machineId: string }) => {
      described.push(args.machineId);
      return { connected: false, operations: {}, installation: null };
    },
    status: () => {
      throw new Error("this test holds no open job");
    },
    listRuns: () => ({ runs: [], nextCursor: null }),
    schedules: () => [],
    schedule: () => ({}),
    disableSchedule: () => ({}),
  };
  const wake = async (jobId: string) =>
    await plugin.lifecycle?.onJobSettled?.(
      { ...context(harness.db as unknown as GuestDatabase, jobs, NOW), jobs: native } as never,
      settled({ jobId, machineId: MACHINE, operationId: OPERATIONS.mapPrepare }),
    );

  await wake("job_of_an_ended_drain");
  expect(described).toEqual([]);
  await wake("job_standing");
  expect(described).toEqual([MACHINE]);
});

test("enabling a store made before archive captures adds their columns, the label map and the recency index", async () => {
  const { db } = harness;
  // A store exactly as the shape before #453 left it, holding one session the crossing hosted
  // at a host NAME. Every row an earlier shape wrote must read as naming no capture yet.
  await db.run(`DROP INDEX sessions_by_modified`);
  await db.run(`DROP TABLE archive_labels`);
  await db.run(`ALTER TABLE sessions DROP COLUMN archive_label`);
  await db.run(`ALTER TABLE sessions DROP COLUMN archive_path`);
  await insert(db, "sessions", {
    selector: "omp/older",
    host: "dev-01",
    harness: "omp",
    source_id: "older",
    seen_at: stamp(NOW - HOUR),
  });

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  // A second enable is the ordinary case and must find nothing missing.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  expect(
    await db.query(
      `SELECT name FROM sqlite_master WHERE name IN ('archive_labels', 'sessions_by_modified')
        ORDER BY name`,
    ),
  ).toEqual([{ name: "archive_labels" }, { name: "sessions_by_modified" }]);
  // And what the columns are for works on the row the store already held: a mapped label
  // hosts its first capture at the machine, where the crossing had left a name.
  await db.run(`INSERT INTO archive_labels(label, machine_id, mapped_at) VALUES('dev-01', ?, ?)`, [
    MACHINE,
    stamp(NOW),
  ]);
  const row = SessionRowSchema.parse({
    selector: "omp/older",
    harness: "omp",
    source_id: "older",
    kind: "operator",
    archive_label: "dev-01",
    archive_path: "/home/operator/.omp/agent/sessions/older.jsonl",
    snapshot_id: "a".repeat(64),
    archived_at: "2026-09-12T11:00:00.000Z",
    size: 10,
    modified_at: "2026-09-12T10:00:00.000Z",
  });
  expect(
    await upsertSessionRows({ db, touch: () => undefined }, [row], new Date(NOW).toISOString()),
  ).toMatchObject({ moved: 1 });
  expect(await db.query(`SELECT host, archive_label, archive_path FROM sessions`)).toEqual([
    { host: MACHINE, archive_label: "dev-01", archive_path: row.archive_path },
  ]);
});

/*
  WHICH ACCOUNT A WAKE ACTS FOR (#470). Code and omp derive a keyed session's job id from the
  posting key AND the calling principal, so a posting whose answer was lost is recovered only
  by an ask made for the same account. A door knows its principal; an enable and a settled-job
  hook do not, so each wake's chain is derived here: a door's from its principal, an enable's
  fresh, a settlement's from the job that settled. These drive the plugin as the host does and
  watch what Code is asked.
*/

/** A session as Code answers one it posted, in the shape its own result schema takes. */
function codeJob(jobId: string): unknown {
  return {
    jobId,
    machineId: MACHINE,
    operationId: "atyrode.omp.session",
    pluginId: "atyrode.omp",
    installationRevision: "1",
    artifactSha256: "a".repeat(64),
    inputDigest: "b".repeat(64),
    resourceBindingDigest: "c".repeat(64),
    inputs: [],
    state: "queued",
    nextInputSeq: null,
    result: null,
    authority: {
      origin: { kind: "action", traceId: "t1", door: null },
      requester: "operator",
      executor: null,
      decision: null,
    },
  };
}

/**
 * CODE, AS `ctx.actions` REACHES IT. Like Code and omp, a keyed ask returns the session its key
 * already posted and buys nothing, and an adopt-only ask never posts. `bought` counts sessions
 * paid for; `asked` is every posting ask, keyed or not; `onBought` runs once a session is bought
 * and before Code's answer is returned, which is where a test closes the hook's lease.
 */
function code(): {
  readonly actions: GuestCtx["actions"];
  readonly asked: { postingKey?: string; adoptOnly?: boolean }[];
  bought: number;
  onBought: () => void;
} {
  const keyed = new Map<string, unknown>();
  const fake = {
    asked: [] as { postingKey?: string; adoptOnly?: boolean }[],
    bought: 0,
    onBought: (): void => undefined,
    actions: {
      call: async ({ action, input }: { action: string; input: unknown }): Promise<unknown> => {
        await Promise.resolve();
        if (action === "listProfiles")
          return {
            profiles: [
              {
                containerId: "ctr_workbench",
                revision: 1,
                selected: null,
                machineId: null,
                accounts: [{ provider: "anthropic", identityKey: "operator" }],
                resolved: true,
              },
            ],
          };
        if (action !== "runSession") throw new Error(`this test asks Code nothing but posting`);
        const ask = input as { postingKey?: string; adoptOnly?: boolean };
        fake.asked.push({
          ...(ask.postingKey === undefined ? {} : { postingKey: ask.postingKey }),
          ...(ask.adoptOnly === true ? { adoptOnly: true } : {}),
        });
        const found = ask.postingKey === undefined ? undefined : keyed.get(ask.postingKey);
        if (found !== undefined) return found;
        if (ask.adoptOnly === true)
          throw new ActionCallError(
            "refused: atyrode.babel -> atyrode.code.runSession (code_omp_posting_unknown)",
          );
        fake.bought += 1;
        const job = codeJob(`omp_${String(fake.bought)}`);
        if (ask.postingKey !== undefined) keyed.set(ask.postingKey, job);
        fake.onBought();
        return job;
      },
    } as unknown as GuestCtx["actions"],
  };
  return fake;
}

/**
 * THE TABLES A SETTLED-JOB HOOK IS SERVED, whose lease the host closes at its lifecycle bound
 * whether the hook has returned or not (#470): after that, nothing the hook writes lands.
 */
function leased(lease: { closed: boolean }): GuestDatabase {
  const open = (): void => {
    if (lease.closed) throw new Error("the settled hook's data lease is closed");
  };
  const { db } = harness;
  return {
    pluginId: BABEL_PLUGIN_ID,
    query: async (sql: string, params?: readonly SqlParam[]) => {
      open();
      return await db.query(sql, params);
    },
    run: async (sql: string, params?: readonly SqlParam[]) => {
      open();
      return await db.run(sql, params);
    },
    batch: async (statements: readonly SqlStatement[]) => {
      open();
      return await db.batch(statements);
    },
  } as unknown as GuestDatabase;
}

/** One wake's context, acting for `principal` where a door would, reaching Code through `actions`. */
function wake(
  database: GuestDatabase,
  actions: GuestCtx["actions"],
  principal = "operator",
): GuestCtx {
  return { ...context(database, jobs), principal: { id: principal }, actions } as GuestCtx;
}

/** The index a settled preparation sealed over one catalogued session, as its receipt carries it. */
const SEALED = {
  closure: "completed",
  material: {
    schema: "babel.material/1",
    preparationId: "prep-470",
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
};

test("a door-launched explore's settlement recovers its own lost posting, and no other account's wake does", async () => {
  // The operator's press posts the preparation; it is still running when his own cycle looks.
  await insert(harness.db, "sessions", {
    selector: "omp/s1",
    host: MACHINE,
    harness: "omp",
    source_id: "s1",
    title: "a session that named itself",
    kind: "operator",
    live: 0,
    archive_label: "dev-01",
    archive_path: "/home/operator/.omp/agent/sessions/s1.jsonl",
    snapshot_id: "5".repeat(64),
    archived_at: new Date(Date.now() - HOUR).toISOString(),
    modified_at: new Date(Date.now() - HOUR).toISOString(),
    size: 1000,
    seen_at: stamp(NOW),
  });
  const executed: string[] = [];
  jobs.execute = ((args: { jobId: string }): unknown => {
    executed.push(args.jobId);
    return {
      jobId: args.jobId,
      machineId: MACHINE,
      operationId: OPERATIONS.prepare,
      state: "queued",
    };
  }) as Jobs["execute"];
  const status = jobs.status.bind(jobs);
  jobs.status = (node: { jobId: string }): unknown =>
    executed.includes(node.jobId)
      ? { jobId: node.jobId, machineId: MACHINE, operationId: OPERATIONS.prepare, state: "running" }
      : status(node);
  const fake = code();
  const launched = (await plugin.handlers[ACTIONS.launch]!(
    wake(harness.db as unknown as GuestDatabase, fake.actions),
    asLaunchRequest({
      machineId: MACHINE,
      preset: "read-whats-new",
      profile: { containerId: "ctr_workbench", expectedRevision: 1 },
    }) as never,
  )) as { runId: string; jobId: string };
  expect(executed).toEqual([launched.jobId]);
  expect(fake.asked).toEqual([]);
  // The preparation settles; its receipt is ingested, and the hook its settlement causes posts
  // the session — and overruns its lease once Code has bought it, before the job id is written.
  await harness.db.run(`UPDATE runs SET closure = 'completed', payload = ? WHERE job_id = ?`, [
    JSON.stringify(SEALED),
    launched.jobId,
  ]);
  const lease = { closed: false };
  fake.onBought = () => {
    lease.closed = true;
  };
  const prepared = settled({ jobId: launched.jobId, operationId: OPERATIONS.prepare });
  await Promise.resolve(
    plugin.lifecycle?.onJobSettled?.(wake(leased(lease), fake.actions) as never, prepared),
  ).catch(() => undefined);
  fake.onBought = () => undefined;
  expect(fake.bought).toBe(1);
  expect(fake.asked).toEqual([{ postingKey: launched.runId }]);

  // Another principal's door, a settlement nothing recorded a chain for, and a new enable each
  // act for another account: none of them asks about this posting.
  await plugin.handlers[ACTIONS.runs]!(
    wake(harness.db as unknown as GuestDatabase, fake.actions, "someone-else"),
    { limit: 25, offset: 0 } as never,
  );
  await plugin.lifecycle?.onJobSettled?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
    settled({ jobId: "job_nobody_recorded" }),
  );
  await plugin.lifecycle?.onEnable?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
  );
  expect(fake.asked).toHaveLength(1);
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = ?`, [launched.runId])).toEqual([
    { job_id: null },
  ]);

  // The same settlement, delivered again, acts for the operator whose press posted the job: it
  // asks under the same key and binds the session the lost post bought, buying nothing more.
  await plugin.lifecycle?.onJobSettled?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
    prepared,
  );
  expect(fake.asked).toEqual([{ postingKey: launched.runId }, { postingKey: launched.runId }]);
  expect(fake.bought).toBe(1);
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = ?`, [launched.runId])).toEqual([
    { job_id: "omp_1" },
  ]);
});

/** A preparation that settled and was ingested, and its explore waiting for the next wake to post. */
async function preparedExplore(): Promise<void> {
  await insert(harness.db, "runs", {
    id: "run_prep_material",
    kind: OPERATIONS.prepare,
    machine_id: MACHINE,
    job_id: "job_prep_material",
    started_at: stamp(NOW - HOUR),
    finished_at: stamp(NOW),
    closure: "completed",
    records: 0,
    payload: JSON.stringify(SEALED),
  });
  await insert(harness.db, "runs", {
    id: "run_prep",
    kind: OPERATIONS.explore,
    machine_id: MACHINE,
    container_id: "ctr_workbench",
    prepare_job_id: "job_prep_material",
    profile: JSON.stringify({ containerId: "ctr_workbench", expectedRevision: 1 }),
    preparation: JSON.stringify({
      preset: "read-whats-new",
      recipes: [{ id: "triage", version: 1 }],
    }),
    started_at: stamp(NOW - HOUR),
    records: 0,
    payload: JSON.stringify({ closure: null, preparing: "job_prep_material" }),
  });
}

test("an operator's own next door wake recovers a posting his last one lost, and another's does not", async () => {
  const fake = code();
  await preparedExplore();
  const lease = { closed: false };
  fake.onBought = () => {
    lease.closed = true;
  };
  // The dispatch's tables die with it once Code has bought the session.
  await plugin.handlers[ACTIONS.runs]!(wake(leased(lease), fake.actions), {
    limit: 25,
    offset: 0,
  } as never).catch(() => undefined);
  fake.onBought = () => undefined;
  expect(fake.asked).toEqual([{ postingKey: "run_prep" }]);
  const runs = async (principal: string) =>
    await plugin.handlers[ACTIONS.runs]!(
      wake(harness.db as unknown as GuestDatabase, fake.actions, principal),
      { limit: 25, offset: 0 } as never,
    );
  await runs("someone-else");
  expect(fake.asked).toHaveLength(1);
  await runs("operator");
  expect(fake.asked).toEqual([{ postingKey: "run_prep" }, { postingKey: "run_prep" }]);
  expect(fake.bought).toBe(1);
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = 'run_prep'`)).toEqual([
    { job_id: "omp_1" },
  ]);
});

test("the next beat recovers a posting the previous beat's wake lost, and only a beat does", async () => {
  const fake = code();
  // The enable registers the beat: its occurrences carry the installer's credential, a chain of
  // their own that no door and no later enable shares.
  await plugin.lifecycle?.onEnable?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
  );
  expect(jobs.scheduled).toMatchObject([{ scheduleId: CONDUCTOR_SCHEDULE_ID, revision: "p1" }]);
  await preparedExplore();
  const beat = (jobId: string, revision = "p1"): SettledJob =>
    settled({
      jobId,
      operationId: PRESET_OPERATIONS["keep-going"],
      scheduleId: CONDUCTOR_SCHEDULE_ID,
      revision,
    });
  // The first beat's wake posts the session and loses its lease before the job id is written.
  const lease = { closed: false };
  fake.onBought = () => {
    lease.closed = true;
  };
  await Promise.resolve(
    plugin.lifecycle?.onJobSettled?.(wake(leased(lease), fake.actions) as never, beat("beat_1")),
  ).catch(() => undefined);
  fake.onBought = () => undefined;
  expect(fake.asked).toEqual([{ postingKey: "run_prep" }]);

  // The operator's own door, a second enable, and an occurrence of a registration nobody named
  // do not act for the beat's account.
  await plugin.handlers[ACTIONS.runs]!(wake(harness.db as unknown as GuestDatabase, fake.actions), {
    limit: 25,
    offset: 0,
  } as never);
  await plugin.lifecycle?.onEnable?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
  );
  await plugin.lifecycle?.onJobSettled?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
    beat("beat_old", "p0"),
  );
  expect(fake.asked).toHaveLength(1);

  // The next beat does.
  await plugin.lifecycle?.onJobSettled?.(
    wake(harness.db as unknown as GuestDatabase, fake.actions) as never,
    beat("beat_2"),
  );
  expect(fake.asked).toEqual([{ postingKey: "run_prep" }, { postingKey: "run_prep" }]);
  expect(fake.bought).toBe(1);
  expect(await harness.db.query(`SELECT job_id FROM runs WHERE id = 'run_prep'`)).toEqual([
    { job_id: "omp_1" },
  ]);
});

test("enabling a store made before run chains adds the column, and every earlier run names none", async () => {
  const { db } = harness;
  await db.run(`ALTER TABLE runs DROP COLUMN chain`);
  await pending();

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  expect(await db.query(`SELECT chain FROM runs WHERE id = 'run_live'`)).toEqual([{ chain: null }]);
});

test("enabling a store made before the history indexes adds every one of them", async () => {
  const { db } = harness;
  const indexes = [
    "assessments_by_supersedes",
    "claims_by_job",
    "facts_by_supersedes",
    "records_by_supersedes",
    "runs_by_job",
    "runs_by_prepare_job",
  ];
  for (const name of indexes) await db.run(`DROP INDEX ${name}`);

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  // A second enable is the ordinary case and must find nothing missing.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  expect(
    await db.query(
      `SELECT name FROM sqlite_master WHERE type = 'index' AND name IN (${indexes
        .map(() => "?")
        .join(", ")}) ORDER BY name`,
      indexes,
    ),
  ).toEqual(indexes.map((name) => ({ name })));
});
