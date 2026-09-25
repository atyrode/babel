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
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  OPERATIONS,
  PRESET_OPERATIONS,
  RUN_STAGES,
  SessionRowSchema,
} from "./contract.ts";
import { WAKES, plugin } from "./server.ts";
import { stamp } from "./store/feedindex.ts";
import { upsertSessionRows } from "./store/sessions.ts";
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
      operations: { [PRESET_OPERATIONS["keep-going"]]: { ready: true, reason: null } },
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
 * What the bridge asks of one job verb before it is served, as `job-service.ts` asks it: the
 * reads a cycle ingests with, the machine read a cadence is registered from, and the run a
 * posting or a schedule is discharged against (#448).
 */
const VERB_CAPS: Record<string, string> = {
  status: "jobs:read",
  follow: "jobs:read",
  listRuns: "jobs:read",
  describe: "machines:read",
  execute: "machines:run",
  schedule: "machines:run",
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
      const cap = typeof key === "string" ? VERB_CAPS[key] : undefined;
      if (cap !== undefined && !reach.includes(cap)) {
        return (): never => {
          throw new Error(`job_capability_absent:${cap}`);
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
    kind: OPERATIONS.explore,
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

test("the cycle behind a read describes a machine, so the loop keeps its own cadence", async () => {
  /*
    THE BEAT THAT WAS NEVER REGISTERED (atyrode/manifold#739, #740). The loop has no clock: its
    cadence is one `engine.jobs.schedule` of the beat, on the machine the policy routes its work
    to, and it is registered only once that machine has said it can run it — one
    `engine.jobs.describe`, a read that moved off `machines:run` and onto `machines:read`
    (atyrode/manifold#736). The bridge a dispatch is served is the door's own caps plus its
    delegates, and `machines:read` could not be delegated at all until #740, so every describe
    behind a read was refused `job_capability_absent:machines:read` however privileged the
    caller: `reconcileSchedule` noted that the beat could not be registered and registered
    nothing, and Babel beat for exactly as long as somebody kept pressing something. The
    schedule itself is then discharged against `machines:run`, which no wake carried either
    (#448): on the integrated preview every cycle logged `the beat cannot be registered:
    job_capability_absent:machines:run`.
  */
  await pending();
  const ctx = context(harness.db as unknown as GuestDatabase, served(jobs, ACTIONS.pulse));

  await plugin.handlers[ACTIONS.pulse]?.(ctx, {} as never);

  expect(jobs.described).toBeGreaterThan(0);
  expect(jobs.scheduled).toMatchObject([
    { scheduleId: `${BABEL_PLUGIN_ID}.conductor`, machineId: MACHINE },
  ]);
});

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
    the native service candidate and rechecks it on installation. Nothing else asks a machine
    anything: reading a feed, ruling on a record and stopping a run stay inside this plugin's
    own tables and job nodes.
  */
  const asks: Record<string, true> = {
    ...WAKES,
    [ACTIONS.drainStart]: true,
    [ACTIONS.verify]: true,
    [ACTIONS.importLedger]: true,
    [ACTIONS.rehostSessions]: true,
    [ACTIONS.previewRecall]: true,
    [ACTIONS.installRecall]: true,
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

test("the doors whose wake or press starts Babel's own jobs are lent machines:run, and no others are", () => {
  /*
    WHO POSTS, AND THEREFORE WHO IS LENT IT (#448). `engine.jobs.execute` and `schedule` discharge
    `machines:run` against the dispatch's attenuated bridge, so a door that starts work without it
    is refused `authority_or_consent_refused` however privileged its caller — on the integrated
    preview the beat never registered and no explicit explore, drain slot or analysis stage was
    ever admitted. The cycle behind every wake registers the beat, posts analysis preparations and
    relaunches a drain's settled slot; `launch`, `drainStart` and `verify` post on their own
    account. Nothing else starts anything, and it is a delegate everywhere, never a cap the
    caller is asked to hold.
  */
  const posts: Record<string, true> = {
    ...WAKES,
    [ACTIONS.drainStart]: true,
    [ACTIONS.verify]: true,
  };
  for (const action of plugin.actions) {
    expect({ door: action.name, runs: (action.delegates ?? []).includes("machines:run") }).toEqual({
      door: action.name,
      runs: Object.hasOwn(posts, action.name),
    });
    expect(action.caps).not.toContain("machines:run");
  }
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
    id: "run_next",
    kind: OPERATIONS.evaluate,
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

test("enabling a store made before the history indexes adds every one of them", async () => {
  const { db } = harness;
  // The preview's store is exactly this case: made by earlier enables, so `SCHEMA_V1`'s copy of
  // these indexes never reached it, and only the addition can. An index missing from that path
  // is invisible in a fresh install and is a cycle that scans its whole history per question
  // on every store already in the field (atyrode/manifold#841).
  const history = [
    "assessments_by_supersedes",
    "claims_by_job",
    "facts_by_supersedes",
    "records_by_supersedes",
    "runs_by_job",
    "runs_by_prepare_job",
  ];
  for (const name of history) await db.run(`DROP INDEX ${name}`);

  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);
  // A second enable is the ordinary case and must find nothing missing.
  await plugin.lifecycle?.onEnable?.(context(db as unknown as GuestDatabase, jobs) as never);

  expect(
    await db.query(
      `SELECT name FROM sqlite_master WHERE type = 'index' AND name IN (${history.map(() => "?").join(", ")})
        ORDER BY name`,
      history,
    ),
  ).toEqual(history.map((name) => ({ name })));
});
