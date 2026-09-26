import { expect, test } from "bun:test";
import { ageClause, elapsedClock, stopInput, usd, usdRate } from "../api.ts";
import { OPERATIONS, TRANSCRIPT_MAP_SESSION_OPERATION } from "../../contract.ts";
import { runRow } from "./host.ts";

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

test("a run still preparing is stopped at its preparation, not at a job it does not have", () => {
  /*
    A run is started in two wakes (#592): the press posts only `atyrode.babel.prepare` and the
    session arrives one wake later under Code's own id. The node the panel names has to be a
    job that exists, or the operator's Stop is refused and the posting wake spends the account
    after he pressed it.
  */
  const preparing = stopInput(
    runRow({
      id: "run_1",
      state: "running",
      startedAt: "2026-09-14T12:00:00.000Z",
      lastWord: "2026-09-14T12:00:00.000Z",
      jobId: "",
      prepareJobId: "job_1_material",
    }),
  );
  expect(preparing.job).toEqual({
    kind: "job",
    machineId: "m-dev-01",
    operationId: OPERATIONS.prepare,
    jobId: "job_1_material",
  });
  const mapping = stopInput(
    runRow({
      id: "map_run",
      state: "running",
      startedAt: "2026-09-14T12:00:00.000Z",
      lastWord: "2026-09-14T12:00:00.000Z",
      kind: TRANSCRIPT_MAP_SESSION_OPERATION,
      jobId: "",
      prepareJobId: "map_material",
    }),
  );
  expect(mapping.job).toEqual({
    kind: "job",
    machineId: "m-dev-01",
    operationId: OPERATIONS.mapPrepare,
    jobId: "map_material",
  });

  // Once the session is posted the run has its own job, and that is the node again.
  const posted = stopInput(
    runRow({
      id: "run_1",
      state: "running",
      startedAt: "2026-09-14T12:00:00.000Z",
      lastWord: "2026-09-14T12:00:00.000Z",
      jobId: "omp_1",
      prepareJobId: "job_1_material",
    }),
  );
  expect(posted.job).toEqual({
    kind: "job",
    machineId: "m-dev-01",
    operationId: OPERATIONS.explore,
    jobId: "omp_1",
  });
});
