/*
  THE DRAIN'S ROWS AND THE TWO FIGURES DERIVED FROM THEM (#258).

  The table is real SQLite under every CHECK the schema declares, so a state this vocabulary does
  not admit fails at the insert rather than at an assertion, and a row that claimed to be running
  with an end recorded fails the same way. The two derived figures — the burn rate and the ETA —
  are pure and are tested as such, because they are the numbers `docs/runbook.md` §11.4 makes an
  operator act on and a mistake in either reads as a drain that is fine when it is not.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import {
  MAX_SAMPLES,
  NO_JOURNAL,
  RATE_WINDOW_MS,
  accountName,
  activeDrains,
  allocationEstimate,
  allocationStatus,
  burnRate,
  closeDrain,
  deadlineOf,
  directDrainAdmission,
  drainOnMachine,
  finishDrain,
  finishDirectLaunch,
  insertDrain,
  readDrain,
  reconcileLive,
  reserveDirectLaunch,
  recentDrains,
  recordLaunch,
  sample,
  saveFold,
  targetEta,
  targetMet,
  type DrainSample,
  type LiveJob,
} from "./drains.ts";
import type { DrainProfile } from "../contract.ts";
import { openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 14, 12, 0, 0);
const MINUTE = 60_000;

/** What a drain records about what it spends: the profile, and Code's own report of it. */
const LEDGER: DrainProfile = {
  profile: { containerId: "ctr_workbench", expectedRevision: 7 },
  model: "anthropic/claude-sonnet-4-5",
  thinking: "high",
  accounts: [{ provider: "anthropic", identityKey: "the-drain-account", label: "" }],
  resolved: true,
};

let harness: TestStore;

beforeEach(async () => {
  harness = await openTestStore(NOW);
});

afterEach(() => {
  harness.close();
});

async function open(id: string, over: Record<string, unknown> = {}): Promise<void> {
  await insertDrain(harness.store, {
    id,
    machineId: "m-dev-01",
    preset: "read-whats-new",
    profile: LEDGER,
    knobs: { recipes: ["code-health"], sinceDays: 3 },
    concurrent: 2,
    target: { costMicros: 1_000_000 },
    startedBy: "operator",
    ...over,
  } as Parameters<typeof insertDrain>[1]);
}

test("a drain row keeps what a relaunch needs and reads back as it was written", async () => {
  await open("drn_one");
  const row = await readDrain(harness.store, "drn_one");
  expect(row?.state).toBe("running");
  expect(row?.finishedAt).toBe("");
  // The knobs are what a relaunch three settlements later asks for: a controller that remembered
  // only the preset would quietly widen or narrow the scope between the first job and the last.
  expect(row?.knobs).toEqual({ recipes: ["code-health"], sinceDays: 3 });
  expect(row?.profile.accounts[0]?.identityKey).toBe("the-drain-account");
  expect(row?.ending).toBe("");
  expect(row?.spent).toEqual({ calls: 0, inputTokens: 0, outputTokens: 0, costMicros: 0 });

  expect((await activeDrains(harness.store)).map((entry) => entry.id)).toEqual(["drn_one"]);
  expect((await drainOnMachine(harness.store, "m-dev-01"))?.id).toBe("drn_one");
  expect(await drainOnMachine(harness.store, "m-other")).toBeNull();
});

test.each([
  { recipes: [], maxJobs: "2", inferenceLimits: { calls: 1 } },
  { recipes: [], maxJobs: 2, inferenceLimits: { calls: "one" } },
  null,
])("persisted safety bounds cannot silently become an unbounded replay: %j", async (knobs) => {
  await open("drn_one");
  await harness.db.run(`UPDATE drains SET knobs = ? WHERE id = ?`, [
    JSON.stringify(knobs),
    "drn_one",
  ]);
  await expect(readDrain(harness.store, "drn_one")).rejects.toThrow();
});

test("a launch is counted once however many times the same job is recorded", async () => {
  await open("drn_one");
  const job = { runId: "run_a", jobId: "job_a", launchedAt: NOW };
  await Promise.all([
    recordLaunch(harness.store, "drn_one", job, 0),
    recordLaunch(harness.store, "drn_one", job, 0),
  ]);
  const row = await readDrain(harness.store, "drn_one");
  expect(row?.live).toEqual([job]);
  expect(row?.jobsLaunched).toBe(1);

  // A CLOSING DRAIN HOLDS WHAT THE HUB ALREADY TOOK. A launch is `jobs.execute` and then this
  // write, and a stop can land between them: a write that only landed under `running` left that
  // job running off the row, with nothing to fold its receipt onto (the review of #285).
  expect(await closeDrain(harness.store, "drn_one", "stopped", "stopped mid-launch")).toBe(
    "closing",
  );
  const late = { runId: "run_b", jobId: "job_b", launchedAt: NOW };
  await recordLaunch(harness.store, "drn_one", late, 1);
  const closing = await readDrain(harness.store, "drn_one");
  expect(closing?.live).toEqual([job, late]);
  expect(closing?.jobsLaunched).toBe(2);
});

test("a stale fold cannot drop a newer admission or count the same receipt twice", async () => {
  await open("drn_one");
  const first = { runId: "run_a", jobId: "job_a", launchedAt: NOW };
  const second = { runId: "run_b", jobId: "job_b", launchedAt: NOW };
  await recordLaunch(harness.store, "drn_one", first, 0);
  const stale = (await readDrain(harness.store, "drn_one"))!;
  await recordLaunch(harness.store, "drn_one", second, 1);
  await harness.db.run(`UPDATE drains SET live = ? WHERE id = 'drn_one'`, [
    JSON.stringify([first, second], null, 2),
  ]);
  const folded = {
    live: [],
    spent: { calls: 1, inputTokens: 10, outputTokens: 20, costMicros: 30 },
    closures: { completed: 1 },
    refusals: {},
    journal: NO_JOURNAL,
    settledNow: 1,
  };
  expect(await saveFold(harness.store, stale, folded)).toBe(false);
  const current = (await readDrain(harness.store, "drn_one"))!;
  expect(current.live).toEqual([first, second]);
  expect(current.jobsSettled).toBe(0);
  expect(await saveFold(harness.store, current, { ...folded, live: [second] })).toBe(true);
  expect(await saveFold(harness.store, current, { ...folded, live: [second] })).toBe(false);

  // An older wake can return from launch after its job has already settled. The durable
  // ordinal, not membership in today's live set, keeps it from consuming another slot.
  expect(await recordLaunch(harness.store, "drn_one", first, 0)).toBe(false);
  const held = (await readDrain(harness.store, "drn_one"))!;
  expect(held.live).toEqual([second]);
  expect(held.jobsLaunched).toBe(2);
  expect(held.jobsSettled).toBe(1);
  expect(held.spent).toEqual(folded.spent);
  expect(await closeDrain(harness.store, "drn_one", "stopped", "stop after the fold")).toBe(
    "closing",
  );
  expect((await readDrain(harness.store, "drn_one"))?.live).toEqual([second]);
});

test("a drain holding nothing ends at once, and ends only once", async () => {
  await open("drn_one");
  expect(await closeDrain(harness.store, "drn_one", "target", "the target was met")).toBe("ended");
  expect(await closeDrain(harness.store, "drn_one", "stopped", "and again")).toBe("already");
  const row = await readDrain(harness.store, "drn_one");
  expect(row?.state).toBe("target");
  expect(row?.reason).toBe("the target was met");
  expect(row?.finishedAt).not.toBe("");
  expect(row?.live).toEqual([]);
  expect(await activeDrains(harness.store)).toEqual([]);
  // …and the fold of a drain that has ended writes nothing: the guard is the WHERE clause.
  await saveFold(harness.store, row!, {
    live: [{ runId: "run_z", jobId: "job_z", launchedAt: NOW }],
    spent: { calls: 9, inputTokens: 9, outputTokens: 9, costMicros: 9 },
    closures: {},
    refusals: {},
    journal: NO_JOURNAL,
    settledNow: 1,
  });
  expect((await readDrain(harness.store, "drn_one"))?.live).toEqual([]);
  expect((await readDrain(harness.store, "drn_one"))?.spent.costMicros).toBe(0);
  await recordLaunch(
    harness.store,
    "drn_one",
    { runId: "run_z", jobId: "job_z", launchedAt: NOW },
    0,
  );
  expect((await readDrain(harness.store, "drn_one"))?.live).toEqual([]);
});

test("a drain that still holds a job closes onto it: the fold goes on until the last receipt", async () => {
  // THE SPEND OF A JOB NOBODY COULD CANCEL. A target is met on a tick that holds no
  // `jobs:cancel`, so the jobs keep running; a row that emptied `live` at the close dropped
  // their receipts out of its own total, which is the figure §11.5 asks an operator to read.
  await open("drn_one");
  const held: LiveJob[] = [
    { runId: "run_a", jobId: "job_a", launchedAt: NOW },
    { runId: "run_b", jobId: "job_b", launchedAt: NOW },
  ];
  for (const [ordinal, job] of held.entries()) {
    await recordLaunch(harness.store, "drn_one", job, ordinal);
  }
  expect(await closeDrain(harness.store, "drn_one", "target", "the target was met")).toBe(
    "closing",
  );
  const closing = await readDrain(harness.store, "drn_one");
  expect(closing?.state).toBe("closing");
  expect(closing?.ending).toBe("target");
  expect(closing?.finishedAt).toBe("");
  expect(closing?.live.map((job) => job.jobId)).toEqual(["job_a", "job_b"]);
  // A closing drain is one a tick still has work for: its receipts are owed to its total.
  expect((await activeDrains(harness.store)).map((entry) => entry.id)).toEqual(["drn_one"]);
  // It cannot finish while it holds one…
  await saveFold(harness.store, closing!, {
    live: [held[1] as LiveJob],
    spent: { calls: 3, inputTokens: 10, outputTokens: 900, costMicros: 600_000 },
    closures: { completed: 1 },
    refusals: {},
    journal: NO_JOURNAL,
    settledNow: 1,
  });
  expect(await finishDrain(harness.store, "drn_one")).toBe(false);
  expect((await readDrain(harness.store, "drn_one"))?.spent.costMicros).toBe(600_000);

  // …and when the last one has settled it takes the ending it was closed with, with the reason
  // and the spend written while it was closing.
  await saveFold(harness.store, (await readDrain(harness.store, "drn_one"))!, {
    live: [],
    spent: { calls: 6, inputTokens: 20, outputTokens: 1_800, costMicros: 1_500_000 },
    closures: { completed: 2 },
    refusals: {},
    journal: NO_JOURNAL,
    settledNow: 1,
  });
  harness.at(NOW + MINUTE);
  expect(await finishDrain(harness.store, "drn_one")).toBe(true);
  const ended = await readDrain(harness.store, "drn_one");
  expect(ended?.state).toBe("target");
  expect(ended?.reason).toBe("the target was met");
  expect(ended?.finishedAt).not.toBe("");
  expect(ended?.spent.costMicros).toBe(1_500_000);
  expect(ended?.jobsSettled).toBe(2);
  expect(await finishDrain(harness.store, "drn_one")).toBe(false);
  expect(await activeDrains(harness.store)).toEqual([]);
  // A closing drain does not hold its machine: a straggler must not stop the next drain.
  await open("drn_two");
  expect((await drainOnMachine(harness.store, "m-dev-01"))?.id).toBe("drn_two");
});

test("a store holding several drains lists the newest first", async () => {
  await open("drn_old");
  harness.at(NOW + MINUTE);
  await open("drn_new", { machineId: "m-other" });
  expect((await recentDrains(harness.store, 10)).map((entry) => entry.id)).toEqual([
    "drn_new",
    "drn_old",
  ]);
  expect((await recentDrains(harness.store, 1)).map((entry) => entry.id)).toEqual(["drn_new"]);
});

test("the rate is zero until something has been observed twice, then it is the observed rate", async () => {
  expect(burnRate([], NOW)).toEqual({ outputTokensPerMinute: 0, costMicrosPerMinute: 0 });
  const one: DrainSample = { at: NOW, outputTokens: 500, costMicros: 200_000, held: 1, atModel: 1 };
  // ONE SAMPLE IS NOT A RATE, and zero is the honest answer: the runbook's rule is about a rate
  // that has STOPPED moving, and one that has never been observed twice has not moved or stalled.
  expect(burnRate([one], NOW)).toEqual({ outputTokensPerMinute: 0, costMicrosPerMinute: 0 });
  // Two samples a minute apart: exactly the difference, per minute.
  const two: DrainSample = {
    at: NOW + MINUTE,
    outputTokens: 1_700,
    costMicros: 500_000,
    held: 1,
    atModel: 1,
  };
  expect(burnRate([one, two], NOW + MINUTE)).toEqual({
    outputTokensPerMinute: 1_200,
    costMicrosPerMinute: 300_000,
  });
  // Half a minute apart is double the per-minute rate: the window is a clock, not a count.
  expect(
    burnRate([one, { ...two, at: NOW + MINUTE / 2 }], NOW + MINUTE / 2).outputTokensPerMinute,
  ).toBe(2_400);
  // A total that somehow went backwards is a reading to discard, never a negative burn.
  expect(
    burnRate(
      [
        { ...two, at: NOW },
        { ...one, at: NOW + MINUTE },
      ],
      NOW + MINUTE,
    ).outputTokensPerMinute,
  ).toBe(0);
});

test("the samples keep an anchor for the window and never grow without bound", async () => {
  let held: readonly DrainSample[] = [];
  for (let n = 0; n <= 40; n += 1) {
    held = sample(held, NOW + n * MINUTE, {
      calls: n,
      inputTokens: n,
      outputTokens: n * 100,
      costMicros: n * 1_000,
    });
  }
  expect(held.length).toBeLessThanOrEqual(MAX_SAMPLES);
  // THE ANCHOR IS WHAT MAKES A RATE POSSIBLE AT ALL. Pruning to the window alone would leave the
  // newest sample on its own every time the others aged out together, and the panel would read
  // "0/min" while the drain was running — which is the one no-go the runbook names.
  const at = NOW + 40 * MINUTE;
  expect(held.filter((entry) => entry.at < at - RATE_WINDOW_MS).length).toBeGreaterThan(0);
  expect(burnRate(held, at).outputTokensPerMinute).toBeCloseTo(100, 6);
});

test("a rate is read over the anchor, so ticks further apart than the window still read one", async () => {
  /*
    THE READING THE NO-GO RULE ACTS ON. A tick happens on a settlement or on the 30-second floor
    behind a wake, so a drain nobody is watching — or one whose explores take longer than the
    three-minute window to settle — samples further apart than the window is wide. Measuring from
    the oldest sample INSIDE the window made the newest sample its own left edge in that case and
    answered `0/min` for the whole drain, which is exactly what §11.4 tells an operator to stop on.
  */
  const first: DrainSample = {
    at: NOW,
    outputTokens: 500,
    costMicros: 200_000,
    held: 2,
    atModel: 2,
  };
  const second: DrainSample = {
    at: NOW + 5 * MINUTE,
    outputTokens: 1_700,
    costMicros: 500_000,
    held: 2,
    atModel: 1,
  };
  const at = NOW + 5 * MINUTE;
  expect(at - first.at).toBeGreaterThan(RATE_WINDOW_MS);
  expect(burnRate([first, second], at)).toEqual({
    outputTokensPerMinute: 240,
    costMicrosPerMinute: 60_000,
  });

  // The same over a whole drain's worth of four-minute ticks at a steady hundred tokens a minute:
  // every read is that rate, never zero.
  let held: readonly DrainSample[] = [];
  for (let minute = 0; minute <= 40; minute += 4) {
    held = sample(held, NOW + minute * MINUTE, {
      calls: minute,
      inputTokens: minute,
      outputTokens: minute * 100,
      costMicros: minute * 1_000,
    });
    if (minute === 0) continue;
    expect(burnRate(held, NOW + minute * MINUTE).outputTokensPerMinute).toBeCloseTo(100, 6);
  }
});

test("a target is met by whichever figure reaches it, and the ETA is the nearest of them", () => {
  const spent = { calls: 3, inputTokens: 10, outputTokens: 900, costMicros: 400_000 };
  expect(targetMet({ costMicros: 500_000 }, spent)).toBe("");
  expect(targetMet({ costMicros: 400_000 }, spent)).toMatch(/is met at 400000/);
  expect(targetMet({ outputTokens: 900 }, spent)).toMatch(/900 output tokens is met at 900/);
  // A deadline is not a spend target: it ends the drain, it is never "met" by a number.
  expect(targetMet({ deadline: new Date(NOW).toISOString() }, spent)).toBe("");

  const rate = { outputTokensPerMinute: 100, costMicrosPerMinute: 100_000 };
  // 100_000 micro-dollars left at 100_000 a minute is one minute; 1_100 tokens left at 100 a
  // minute is eleven. The first target to be met ends the drain, so the ETA is the nearer one.
  const eta = targetEta({ costMicros: 500_000, outputTokens: 2_000 }, spent, rate, NOW);
  expect(Date.parse(eta)).toBe(NOW + MINUTE);
  // No rate is no ETA, and that is the reading the go/no-go rule acts on rather than a number
  // invented to fill the column.
  expect(
    targetEta(
      { costMicros: 500_000 },
      spent,
      { outputTokensPerMinute: 0, costMicrosPerMinute: 0 },
      NOW,
    ),
  ).toBe("");
  // A target with nothing to reach has no ETA either, and a deadline-only drain is that case.
  expect(targetEta({ deadline: "2026-09-14T14:00:00.000Z" }, spent, rate, NOW)).toBe("");
  // A target already met is now.
  expect(Date.parse(targetEta({ costMicros: 1 }, spent, rate, NOW))).toBe(NOW);
});

test("a deadline is an instant or it is nothing, and an unreadable one is nothing", () => {
  expect(deadlineOf({})).toBeNull();
  expect(deadlineOf({ deadline: "not an instant" })).toBeNull();
  expect(deadlineOf({ deadline: "2026-09-14T13:00:00.000Z" })).toBe(Date.UTC(2026, 8, 14, 13));
});

test("a state the vocabulary does not admit is refused by the store rather than stored", async () => {
  await open("drn_one");
  expect(
    harness.db.run(`UPDATE drains SET state = 'draining' WHERE id = ?`, ["drn_one"]),
  ).rejects.toThrow();
  // A row cannot be running and ended at once, nor ended and still running: the CHECK is the
  // whole of that guarantee, so no reader has to reconcile the two columns.
  expect(
    harness.db.run(`UPDATE drains SET finished_at = ? WHERE id = ?`, [
      "2026-09-14T12:30:00.000Z",
      "drn_one",
    ]),
  ).rejects.toThrow();
  expect(
    harness.db.run(`UPDATE drains SET state = 'stopped' WHERE id = ?`, ["drn_one"]),
  ).rejects.toThrow();
  // A closing drain has no end recorded and names the ending it is heading for: a row that was
  // closing towards nothing would be one nothing could finish.
  expect(
    harness.db.run(`UPDATE drains SET state = 'closing' WHERE id = ?`, ["drn_one"]),
  ).rejects.toThrow();
  expect(
    harness.db.run(`UPDATE drains SET ending = 'draining' WHERE id = ?`, ["drn_one"]),
  ).rejects.toThrow();
});

test("a profile Code reported no account for says so rather than leaving a blank", () => {
  // #267: "which account did that fan burn" has to be answerable, and at this pin Code
  // publishes no accounts on a profile at all — so the reading says which of the two
  // silences it is instead of printing nothing.
  expect(accountName(LEDGER)).toBe("ctr_workbench: the-drain-account (as Code reported at start)");
  expect(accountName({ ...LEDGER, accounts: [] })).toBe("ctr_workbench (Code reported no account)");
});

test("direct admission atomically owns a slot and ordinal across stale concurrent wakes", async () => {
  await open("drn_reserved", { concurrent: 1 });
  const row = (await readDrain(harness.store, "drn_reserved"))!;
  const job = { runId: "run_reserved_0", jobId: "job_reserved_0", launchedAt: NOW, reserved: true };
  const won = await Promise.all([
    reserveDirectLaunch(harness.store, row, job, "read-whats-new", 300_000, 1),
    reserveDirectLaunch(harness.store, row, job, "read-whats-new", 300_000, 1),
  ]);
  expect(won.sort()).toEqual([false, true]);
  const held = (await readDrain(harness.store, row.id))!;
  expect(held.jobsLaunched).toBe(1);
  expect((await reconcileLive(harness.store, held.live)).holding).toEqual([job]);
  expect(
    await reserveDirectLaunch(
      harness.store,
      held,
      { ...job, runId: "run_reserved_1", jobId: "job_reserved_1" },
      "explore-topic",
      1,
      1,
    ),
  ).toBe(false);
  await closeDrain(harness.store, row.id, "stopped", "operator stop");
  const guard = directDrainAdmission(row.id, job.runId, NOW);
  expect(await harness.db.query(`SELECT 1 AS admitted WHERE ${guard.sql}`, guard.params)).toEqual(
    [],
  );
  await finishDirectLaunch(harness.store, job.runId, "stopped before preparation");
  expect((await reconcileLive(harness.store, held.live)).missing).toEqual([job]);
});

test("allocation uses each whole meter once despite overlapping recipes and spends remaining reserve only", async () => {
  await open("drn_weighted", {
    knobs: { recipes: ["one", "two"], allocation: { "read-whats-new": 3, "explore-topic": 1 } },
  });
  let row = (await readDrain(harness.store, "drn_weighted"))!;
  const first = {
    runId: "run_weighted_0",
    jobId: "job_weighted_0",
    launchedAt: NOW,
    reserved: true,
  };
  expect(await reserveDirectLaunch(harness.store, row, first, "read-whats-new", 400_000, 2)).toBe(
    true,
  );
  await harness.db.run(
    `INSERT INTO runs(id, kind, machine_id, started_at, closure, preparation, payload)
      VALUES (?, 'atyrode.babel.explore', 'm-dev-01', ?, 'completed', ?, ?)`,
    [
      first.runId,
      new Date(NOW).toISOString(),
      JSON.stringify({ recipes: [{ id: "one" }, { id: "two" }] }),
      JSON.stringify({ inference: { calls: 2, costMicros: 300_000 } }),
    ],
  );
  await finishDirectLaunch(harness.store, first.runId, null);
  row = (await readDrain(harness.store, row.id))!;
  const second = { ...first, runId: "run_weighted_1", jobId: "job_weighted_1" };
  expect(await reserveDirectLaunch(harness.store, row, second, "explore-topic", 200_000, 2)).toBe(
    true,
  );
  await harness.db.run(
    `INSERT INTO runs(id, kind, machine_id, started_at, payload)
      VALUES (?, 'atyrode.babel.explore', 'm-dev-01', ?, '{}')`,
    [second.runId, new Date(NOW).toISOString()],
  );
  await harness.db.run(
    `INSERT INTO run_progress(run_id, job_id, stage, since, calls, cost_usd, updated_at)
      VALUES (?, ?, 'at the model', ?, 1, 0.05, ?)`,
    [second.runId, second.jobId, new Date(NOW).toISOString(), new Date(NOW).toISOString()],
  );
  const allocation = await allocationStatus(harness.store, row);
  expect(allocation).toMatchObject([
    {
      preset: "read-whats-new",
      share: 0.75,
      incurredCostMicros: 300_000,
      reservedCostMicros: 0,
      deficitCostMicros: 75_000,
      unpricedJobs: 0,
    },
    {
      preset: "explore-topic",
      share: 0.25,
      incurredCostMicros: 50_000,
      reservedCostMicros: 150_000,
      deficitCostMicros: -75_000,
      unpricedJobs: 0,
    },
  ]);
  expect(await allocationEstimate(harness.store, row, "read-whats-new")).toEqual({
    costMicros: 300_000,
    eligible: true,
  });
});

test.each([
  { payload: {}, gap: "missing-price" },
  { payload: { inference: { calls: 3, costMicros: 0 } }, gap: "zero-price" },
])(
  "unpriced whole work keeps uncertainty visible and cannot drive mixed refills: $gap",
  async ({ payload, gap }) => {
    await open("drn_unpriced", {
      knobs: {
        recipes: [],
        inferenceLimits: { costMicros: 100_000 },
        allocation: { "read-whats-new": 1, "explore-topic": 2 },
      },
    });
    const row = (await readDrain(harness.store, "drn_unpriced"))!;
    expect(await allocationEstimate(harness.store, row, "read-whats-new")).toEqual({
      costMicros: 100_000,
      eligible: true,
    });
    const job = { runId: "run_unpriced", jobId: "job_unpriced", launchedAt: NOW, reserved: true };
    expect(await reserveDirectLaunch(harness.store, row, job, "read-whats-new", null, 2)).toBe(
      false,
    );
    expect(await reserveDirectLaunch(harness.store, row, job, "read-whats-new", 100_000, 2)).toBe(
      true,
    );
    await harness.db.run(
      `INSERT INTO runs(id, kind, machine_id, started_at, closure, cost_usd, payload)
      VALUES (?, 'atyrode.babel.explore', 'm-dev-01', ?, 'completed', 7, ?)`,
      [job.runId, new Date(NOW).toISOString(), JSON.stringify(payload)],
    );
    await finishDirectLaunch(harness.store, job.runId, null);
    expect((await allocationStatus(harness.store, row))[0]).toMatchObject({
      incurredCostMicros: 0,
      unpricedJobs: 1,
      gap,
    });
    expect((await allocationEstimate(harness.store, row, "read-whats-new")).eligible).toBe(false);
    expect((await allocationEstimate(harness.store, row, "explore-topic")).eligible).toBe(true);
  },
);

test("the reservation fence reads pending receipts, deadlines and other machine work", async () => {
  await open("drn_fenced", { concurrent: 1, knobs: { recipes: [] } });
  const row = (await readDrain(harness.store, "drn_fenced"))!;
  const job = { runId: "run_fenced", jobId: "job_fenced", launchedAt: NOW, reserved: true };
  await harness.db.run(
    `INSERT INTO runs(id, kind, machine_id, started_at, payload)
      VALUES ('other-work', 'atyrode.babel.explore', 'm-dev-01', ?, '{}')`,
    [new Date(NOW).toISOString()],
  );
  expect(await reserveDirectLaunch(harness.store, row, job, "read-whats-new", 1, 1)).toBe(false);
  await harness.db.run(`UPDATE runs SET closure = 'completed' WHERE id = 'other-work'`);
  expect(await reserveDirectLaunch(harness.store, row, job, "read-whats-new", 1, 1)).toBe(true);
  await harness.db.run(
    `INSERT INTO runs(id, kind, machine_id, started_at, closure, payload)
      VALUES (?, 'atyrode.babel.explore', 'm-dev-01', ?, 'completed', ?)`,
    [
      job.runId,
      new Date(NOW).toISOString(),
      JSON.stringify({ inference: { costMicros: 1_000_000 } }),
    ],
  );
  const guard = directDrainAdmission(row.id, job.runId, NOW);
  expect(await harness.db.query(`SELECT 1 AS admitted WHERE ${guard.sql}`, guard.params)).toEqual(
    [],
  );
  await harness.db.run(`UPDATE runs SET payload = '{}' WHERE id = ?`, [job.runId]);
  await harness.db.run(`UPDATE drains SET target = ? WHERE id = ?`, [
    JSON.stringify({ deadline: new Date(NOW).toISOString() }),
    row.id,
  ]);
  expect(await harness.db.query(`SELECT 1 AS admitted WHERE ${guard.sql}`, guard.params)).toEqual(
    [],
  );
});

test("live capacity serializes competing machine reservations and a later shrink", async () => {
  await open("drn_capacity_a", { concurrent: 3 });
  await open("drn_capacity_b", { concurrent: 3 });
  const a = (await readDrain(harness.store, "drn_capacity_a"))!;
  const b = (await readDrain(harness.store, "drn_capacity_b"))!;
  const job = { runId: "run_capacity_a", jobId: "job_capacity_a", launchedAt: NOW, reserved: true };
  const other = { ...job, runId: "run_capacity_b", jobId: "job_capacity_b" };
  const won = await Promise.all([
    reserveDirectLaunch(harness.store, a, job, "read-whats-new", 1, 1),
    reserveDirectLaunch(harness.store, b, other, "read-whats-new", 1, 1),
  ]);
  expect(won.filter(Boolean)).toHaveLength(1);
  const held = (await readDrain(harness.store, won[0] ? a.id : b.id))!;
  const next = { ...job, runId: "run_capacity_next", jobId: "job_capacity_next" };
  expect(await reserveDirectLaunch(harness.store, held, next, "read-whats-new", 1, 0)).toBe(false);
  expect(await reserveDirectLaunch(harness.store, held, next, "read-whats-new", 1, 1)).toBe(false);
  expect((await readDrain(harness.store, held.id))?.jobsLaunched).toBe(1);
  expect((await readDrain(harness.store, held.id))?.live).toEqual(held.live);
  expect(await reserveDirectLaunch(harness.store, held, next, "read-whats-new", 1, 2)).toBe(true);
});
