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
  RATE_WINDOW_MS,
  burnRate,
  closeDrain,
  deadlineOf,
  drainOnMachine,
  insertDrain,
  readDrain,
  recentDrains,
  recordLaunch,
  runningDrains,
  sample,
  saveFold,
  targetEta,
  targetMet,
  type DrainSample,
} from "./drains.ts";
import { openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 14, 12, 0, 0);
const MINUTE = 60_000;

const SESSION = {
  model: "anthropic/claude-sonnet-4-5",
  account: {
    provider: "anthropic",
    scope: "subscription",
    credentialId: "41",
    identityKey: "the-drain-account",
  },
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
    session: SESSION,
    knobs: { recipes: ["code-health"], sinceDays: 3 },
    concurrent: 2,
    target: { costMicros: 1_000_000 },
    startedBy: "operator",
    budgetId: "bdg_one",
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
  expect(row?.session.account.credentialId).toBe("41");
  expect(row?.budgetId).toBe("bdg_one");
  expect(row?.spent).toEqual({ calls: 0, inputTokens: 0, outputTokens: 0, costMicros: 0 });

  expect((await runningDrains(harness.store)).map((entry) => entry.id)).toEqual(["drn_one"]);
  expect((await drainOnMachine(harness.store, "m-dev-01"))?.id).toBe("drn_one");
  expect(await drainOnMachine(harness.store, "m-other")).toBeNull();
});

test("a launch is counted once however many times the same job is recorded", async () => {
  await open("drn_one");
  const job = { runId: "run_a", jobId: "job_a", launchedAt: NOW };
  await recordLaunch(harness.store, "drn_one", job, []);
  // A RETRIED TICK POSTS THE SAME JOB, because its ids are derived rather than minted: recording
  // it twice must not make the drain believe it holds two.
  await recordLaunch(harness.store, "drn_one", job, [job]);
  const row = await readDrain(harness.store, "drn_one");
  expect(row?.live).toEqual([job]);
  expect(row?.jobsLaunched).toBe(1);
});

test("a drain ends once: the second close finds nothing to close and the row keeps the first end", async () => {
  await open("drn_one");
  expect(await closeDrain(harness.store, "drn_one", "target", "the target was met")).toBe(true);
  expect(await closeDrain(harness.store, "drn_one", "stopped", "and again")).toBe(false);
  const row = await readDrain(harness.store, "drn_one");
  expect(row?.state).toBe("target");
  expect(row?.reason).toBe("the target was met");
  expect(row?.finishedAt).not.toBe("");
  // A closed drain holds nothing, whether or not its cancels landed: a later tick has no job to
  // relaunch against.
  expect(row?.live).toEqual([]);
  expect(await runningDrains(harness.store)).toEqual([]);
  // …and the fold of a drain that has ended writes nothing: the guard is the WHERE clause.
  await saveFold(harness.store, "drn_one", {
    live: [{ runId: "run_z", jobId: "job_z", launchedAt: NOW }],
    spent: { calls: 9, inputTokens: 9, outputTokens: 9, costMicros: 9 },
    closures: {},
    refusals: {},
    samples: [],
    settledNow: 1,
  });
  expect((await readDrain(harness.store, "drn_one"))?.live).toEqual([]);
  expect((await readDrain(harness.store, "drn_one"))?.spent.costMicros).toBe(0);
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
  const one: DrainSample = { at: NOW, outputTokens: 500, costMicros: 200_000 };
  // ONE SAMPLE IS NOT A RATE, and zero is the honest answer: the runbook's rule is about a rate
  // that has STOPPED moving, and one that has never been observed twice has not moved or stalled.
  expect(burnRate([one], NOW)).toEqual({ outputTokensPerMinute: 0, costMicrosPerMinute: 0 });
  // Two samples a minute apart: exactly the difference, per minute.
  const two: DrainSample = { at: NOW + MINUTE, outputTokens: 1_700, costMicros: 500_000 };
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
    burnRate([{ ...two, at: NOW }, { ...one, at: NOW + MINUTE }], NOW + MINUTE)
      .outputTokensPerMinute,
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
  expect(targetEta({ costMicros: 500_000 }, spent, { outputTokensPerMinute: 0, costMicrosPerMinute: 0 }, NOW)).toBe("");
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
    harness.db.run(`UPDATE drains SET finished_at = ? WHERE id = ?`, ["2026-09-14T12:30:00.000Z", "drn_one"]),
  ).rejects.toThrow();
  expect(
    harness.db.run(`UPDATE drains SET state = 'stopped' WHERE id = ?`, ["drn_one"]),
  ).rejects.toThrow();
});
