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
import type { GuestCtx, GuestDatabase, GuestSettledJobs } from "@manifold/plugin-kit/server";
import type { SettledJob } from "@manifold/protocol";
import { ACTIONS, BABEL_PLUGIN_ID, OPERATIONS } from "./contract.ts";
import { plugin } from "./server.ts";
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

  /** The job the run row is waiting on: exited cleanly, with nothing sealed. */
  status(): unknown {
    this.statuses += 1;
    return {
      jobId: "job_live",
      machineId: MACHINE,
      operationId: OPERATIONS.evaluate,
      state: "exited",
      result: { state: "exited", exitCode: 0, reason: null, outputs: [] },
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
}

let harness: TestStore;
let jobs: Jobs;
/** The host's clock, a fresh hour per dispatch: the module remembers when it last woke. */
let clock = NOW;

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
    jobs: slice as unknown as GuestSettledJobs,
    storage: { set: async () => await Promise.resolve() },
    newId: async () => await Promise.resolve("000001"),
    now: () => now,
  } as unknown as GuestCtx;
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
  // did all of that AFTER the beat it cannot register refused it: the hardened slice has no
  // schedule verb, so the cycle described a machine, was refused, noted it and carried on.
  expect(jobs.described).toBeGreaterThan(0);
  expect(jobs.statuses).toBe(1);
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

test("enabling runs a cycle with no job authority at all, and still answers", async () => {
  await pending();

  await plugin.lifecycle?.onEnable?.(
    context(harness.db as unknown as GuestDatabase, jobs) as never,
  );

  // `GuestLifecycleCtx` carries no jobs, so nothing was asked of a machine and the run the hub
  // is waiting on is still waiting: what the cycle could do without one, it did.
  expect(jobs.statuses).toBe(0);
  expect(await closure()).toBeNull();
});
