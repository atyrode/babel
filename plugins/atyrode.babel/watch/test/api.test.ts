import { expect, test } from "bun:test";
import { ageClause, elapsedClock, usd, usdRate } from "../api.ts";

/*
  WHAT THE PANEL COMPUTES OUT OF WHAT A RUN SAID, and nothing about starting one.

  The launch draft, its per-preset knob filter and its readiness clause went with the Start
  form (#279): a Babel run is a Code session, so there is no launch input for this side to
  build. What is left here is the arithmetic every section shares, where a wrong unit is a
  number the operator acts on — a spend read at a rate's precision overstates a cent by a
  third, and "no word yet" and "0s ago" are different facts about a run.
*/

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
