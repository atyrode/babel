import { expect, test } from "bun:test";
import { LaunchInputSchema } from "../../contract.ts";
import { INITIAL_DRAFT, ageClause, elapsedClock, launchInput, unready, usd, usdRate } from "../api.ts";

/*
  THE LAUNCH INPUT IS A CONTRACT, and this is where it is held to it.

  `LaunchInputSchema` is strict: one knob the chosen preset has no business for and the hub
  refuses the whole launch. The panel therefore filters the draft by the preset's own table, and
  the filter is the thing most worth a test — a preset switch that left `minutes` behind would
  produce a refusal the operator cannot explain from anything on the screen.
*/

test("each preset posts exactly its own knob", () => {
  expect(launchInput({ ...INITIAL_DRAFT, machineId: "m1", preset: "read-whats-new", sinceDays: 3 })).toEqual({
    machineId: "m1",
    preset: "read-whats-new",
    sinceDays: 3,
    recipes: [],
  });
  expect(
    launchInput({ ...INITIAL_DRAFT, machineId: "m1", preset: "keep-going", minutes: 90, sinceDays: 3, draws: 9 }),
  ).toEqual({ machineId: "m1", preset: "keep-going", minutes: 90, recipes: [] });
  expect(launchInput({ ...INITIAL_DRAFT, machineId: "m1", preset: "review-backlog", draws: 9 })).toEqual({
    machineId: "m1",
    preset: "review-backlog",
    draws: 9,
    recipes: [],
  });
  expect(
    launchInput({ ...INITIAL_DRAFT, machineId: "m1", preset: "explore-topic", entityId: "ent_0f1e2d3c" }),
  ).toEqual({ machineId: "m1", preset: "explore-topic", entityId: "ent_0f1e2d3c", recipes: [] });
});

test("a preset that takes no recipes drops the selection rather than sending it", () => {
  const draft = { ...INITIAL_DRAFT, machineId: "m1", recipes: ["code-health-comprehensibility"] };
  expect(launchInput({ ...draft, preset: "read-whats-new" }).recipes).toEqual(["code-health-comprehensibility"]);
  expect(launchInput({ ...draft, preset: "file-and-tidy" }).recipes).toEqual([]);
});

test("every preset's input is one the contract itself accepts", () => {
  for (const preset of ["read-whats-new", "explore-topic", "review-backlog", "file-and-tidy", "keep-going"] as const) {
    const input = launchInput({ ...INITIAL_DRAFT, machineId: "m1", preset, entityId: "ent_0f1e2d3c" });
    expect(LaunchInputSchema.safeParse(input).success).toBe(true);
  }
});

test("what is missing is named before the door is knocked on", () => {
  expect(unready(INITIAL_DRAFT)).toBe("Pick a machine to run on.");
  expect(unready({ ...INITIAL_DRAFT, machineId: "m1" })).toBe("");
  expect(unready({ ...INITIAL_DRAFT, machineId: "m1", preset: "explore-topic" })).toBe("Pick a topic to explore.");
  expect(unready({ ...INITIAL_DRAFT, machineId: "m1", preset: "explore-topic", entityId: "ent_0f1e2d3c" })).toBe("");
});

test("the clock reads in whole seconds, then minutes, then hours", () => {
  expect(elapsedClock(0)).toBe("0s");
  expect(elapsedClock(59.4)).toBe("59s");
  expect(elapsedClock(60)).toBe("1m 00s");
  expect(elapsedClock(3_599)).toBe("59m 59s");
  expect(elapsedClock(3_600)).toBe("1h 00m");
});

test("a run that has announced nothing has said nothing, not something stale", () => {
  const now = Date.parse("2026-09-12T12:00:00.000Z");
  expect(ageClause("", now)).toBe("no word yet");
  expect(ageClause("not an instant", now)).toBe("no word yet");
  expect(ageClause("2026-09-12T11:59:48.000Z", now)).toBe("last word 12s ago");
  expect(ageClause("2026-09-12T12:00:00.500Z", now)).toBe("last word just now");
});

test("a spend reads in two places; a rate per 1k reads in three", () => {
  expect(usd(1.875)).toBe("$1.88");
  expect(usd(0.04)).toBe("$0.04");
  expect(usd(20)).toBe("$20.00");
  expect(usdRate(0.015)).toBe("$0.015");
  expect(usdRate(0.0005)).toBe("$0.001");
});
