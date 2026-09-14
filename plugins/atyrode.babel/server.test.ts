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

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx, GuestDatabase, GuestHookJobs } from "@manifold/plugin-kit/server";
import type { SettledJob } from "@manifold/protocol";
import { ACTIONS, BABEL_PLUGIN_ID, OPERATIONS, RUN_STAGES } from "./contract.ts";
import { WAKES, plugin } from "./server.ts";
import { stamp } from "./store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "./store/testdb.ts";

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const MACHINE = "m-dev-01";
const RECORD = "fnd_00000001";

class Jobs {
  described = 0;
  statuses = 0;
  listed = 0;
  followed = 0;
  /** The cadences this fake has been asked to register, newest last. */
  readonly scheduled: { scheduleId: string; revision: string; machineId: string }[] = [];

  describe(): unknown {
    this.described += 1;
    return {
      connected: true,
      operations: { [OPERATIONS.scan]: { ready: true, reason: null } },
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
        operationId: OPERATIONS.explore,
        state: "running",
        result: null,
      };
    }
    return {
      jobId: "job_live",
      machineId: MACHINE,
      operationId: OPERATIONS.evaluate,
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

  execute(): unknown {
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

  schedule(args: { scheduleId: string; revision: string; machineId: string }): unknown {
    this.scheduled.push({
      scheduleId: args.scheduleId,
      revision: args.revision,
      machineId: args.machineId,
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
    // Only a DISPATCH is served one (`serveCtxCall`), and this plugin asks it one question:
    // what a folder a scan catalogued is. Nothing here catalogues one, so nothing asks.
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
 * `ctx.jobs` AS THE HOST SERVES IT TO ONE DOOR'S DISPATCH: attenuated to what that action
 * declared (`plugin-host.ts` intersects the door's caps and delegates with the native set, and
 * `authorizedJob` refuses every job read without `jobs:read` in them). It is derived from the
 * plugin's OWN declaration, so a door added to `WAKES` without the delegate is served a slice
 * that cannot read a job — which is a cycle that settles nothing and folds nothing, and is the
 * whole reason the delegate is on the door rather than assumed.
 */
function served(slice: Jobs, name: string): Jobs {
  const action = plugin.actions.find((entry) => entry.name === name);
  if (action === undefined) throw new Error(`no action ${name}`);
  const reach = [...(action.caps ?? []), ...(action.delegates ?? [])];
  if (reach.includes("jobs:read")) return slice;
  const refuse = (): never => {
    throw new Error("jobs:read capability required");
  };
  return new Proxy(slice, {
    get(target, key, receiver) {
      if (key === "status" || key === "follow" || key === "listRuns") return refuse;
      return Reflect.get(target, key, receiver);
    },
  });
}

/** A run of this plugin's whose job is still RUNNING: the only case progress can be folded for. */
async function watched(): Promise<void> {
  await insert(harness.db, "runs", {
    id: "run_watched", kind: OPERATIONS.explore, machine_id: MACHINE, job_id: "job_running",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
}

function settled(over: Partial<SettledJob> = {}): SettledJob {
  return {
    jobId: "job_live",
    machineId: MACHINE,
    operationId: OPERATIONS.evaluate,
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
    id: "run_live", kind: OPERATIONS.evaluate, machine_id: MACHINE, job_id: "job_live",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
  });
  await insert(db, "claims", {
    id: "asg_live", record_id: RECORD, role: "reception", lane: "coverage", policy_version: "p1",
    job_id: "job_live", run_id: "cyc_1", fence: 1, reserved_cost: 0.0625,
    granted_at: stamp(NOW - HOUR), expires_at: stamp(NOW + HOUR),
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
  await insert(harness.db, "policies", {
    version: "p1", seq: 1, actor_id: "operator", reason: "on",
    payload: JSON.stringify({ enabled: true, perCycleCost: 0.25, batchSize: 4, dailyCost: 2 }),
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

  await plugin.handlers[ACTIONS.feed]?.(ctx, { sort: "new", window: "day", limit: 5, offset: 0 } as never);
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
    id: "run_next", kind: OPERATIONS.evaluate, machine_id: MACHINE, job_id: "job_next",
    started_at: stamp(NOW - HOUR), records: 0, payload: JSON.stringify({ closure: null }),
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
  // so if the enable did not add them here nothing ever would — and every session row a `scan`
  // wrote would name a column the table has not got.
  await db.run(`ALTER TABLE sessions DROP COLUMN live`);
  await db.run(`ALTER TABLE sessions DROP COLUMN kind`);
  await insert(db, "sessions", {
    selector: "omp/older", host: MACHINE, harness: "omp", source_id: "older",
    title: "catalogued before the columns existed", seen_at: stamp(NOW - HOUR),
  });

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  // The row it already held is the operator's own and settled, which is what a default says.
  const older = await db.query<{ live: number; kind: string }>(
    `SELECT live, kind FROM sessions WHERE selector = 'omp/older'`,
  );
  expect(older[0]?.kind).toBe("operator");
  expect(Number(older[0]?.live)).toBe(0);

  // And a row in the shape `scan` writes now lands, which is the whole point of the column.
  await insert(db, "sessions", {
    selector: "omp/run-7/explore", host: MACHINE, harness: "omp", source_id: "run-7/explore",
    title: "babel explore pass", live: 1, kind: "agent", seen_at: stamp(NOW),
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
