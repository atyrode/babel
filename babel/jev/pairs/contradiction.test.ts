import { expect, test } from "bun:test";
import { CONTRADICTION, CONTRADICTS_QUESTION } from "./contradiction.ts";
import { deliveries, detectPair } from "./detect.ts";
import type { PairRecord, RecordPair } from "./pair.ts";

/*
  THE SYMMETRIC RELATION, as the consumer observes it rather than by inspecting its source.

  A contradiction report has two properties worth keeping. Swapping the records cannot change
  the detection — a symmetric relation that changed under a swap would have smuggled a favourite
  in through input order — and delivery must put the SAME account beside both records. The latter
  is where #357's "neither side favoured" requirement reaches what an operator sees: one
  suggestion would privilege the record it happened to be attached to even if the value itself
  held a neutral tuple.
*/

const WRITTEN = "2026-09-19T12:00:00.000Z";

function pairRecord(id: string, revision: number, title: string, text: string): PairRecord {
  return { id, revision, kind: "finding", title, text, writtenAt: WRITTEN };
}

function pair(a: PairRecord, b: PairRecord): RecordPair {
  return { a, b };
}

test("a contradicting pair is invariant under reversal and carries no favoured side", () => {
  const secretSink = pairRecord(
    "fnd_00000009",
    2,
    "The archive is a credential sink",
    "an environment dump exposed four live credential values",
  );
  const secretDiscipline = pairRecord(
    "fnd_00000003",
    5,
    "Secret hygiene is consistently enforced",
    "agents enumerate credential environment variables by name only and never value",
  );
  const answers = { [CONTRADICTS_QUESTION]: 0.91 };
  const cuts = { [CONTRADICTS_QUESTION]: 0.7 };
  const forward = CONTRADICTION.detect({
    pair: pair(secretSink, secretDiscipline),
    answers,
    cuts,
  });
  const reversed = CONTRADICTION.detect({
    pair: pair(secretDiscipline, secretSink),
    answers,
    cuts,
  });
  if (forward === null || forward.relation !== "contradiction") {
    throw new Error("the contradicting pair was not reported");
  }
  expect(reversed).toEqual(forward);
  expect(forward.relation).toBe("contradiction");
  expect(forward.records).toEqual(["fnd_00000003", "fnd_00000009"]);
  // There is nowhere in the result for a detector to name one side right, wrong, stale or fresh.
  expect(Object.keys(forward ?? {}).sort()).toEqual([
    "confidence",
    "rationale",
    "records",
    "relation",
    "summary",
  ]);
});

test("a contradiction is delivered beside both records in the same words, so neither side is privileged", () => {
  const dirty = pairRecord(
    "fnd_00000001",
    7,
    "The checkout was dirty",
    "git status showed a modified file during apply",
  );
  const clean = pairRecord(
    "fnd_00000002",
    4,
    "The checkout was clean",
    "git status showed no changes during the same apply",
  );
  const result = detectPair(
    pair(dirty, clean),
    { [CONTRADICTS_QUESTION]: 0.93 },
    { [CONTRADICTS_QUESTION]: 0.7 },
    [CONTRADICTION],
  );
  expect(result.failed).toEqual([]);
  expect(result.uncalibrated).toEqual([]);
  expect(result.detections).toHaveLength(1);
  const detection = result.detections[0];
  if (detection === undefined) throw new Error("the contradicting pair was not reported");
  const suggestions = deliveries(detection, pair(dirty, clean));
  expect(suggestions).toHaveLength(2);
  expect(suggestions.map((suggestion) => suggestion.recordId).sort()).toEqual(
    [dirty.id, clean.id].sort(),
  );
  expect(suggestions[0]?.summary).toBe(suggestions[1]?.summary);
  expect(suggestions[0]?.rationale).toBe(suggestions[1]?.rationale);
  expect(suggestions[0]?.counterpart).toBe(clean.id);
  expect(suggestions[1]?.counterpart).toBe(dirty.id);
  expect(suggestions.every((suggestion) => suggestion.kind === "ask-question")).toBe(true);
});

test("a stated line distinguishes no contradiction from no calibration", () => {
  const first = pairRecord("fnd_a", 0, "First", "claim one");
  const second = pairRecord("fnd_b", 0, "Second", "claim two");
  const below = detectPair(
    pair(first, second),
    { [CONTRADICTS_QUESTION]: 0.69 },
    { [CONTRADICTS_QUESTION]: 0.7 },
    [CONTRADICTION],
  );
  expect(below.detections).toEqual([]);
  expect(below.uncalibrated).toEqual([]);

  const missing = detectPair(pair(first, second), { [CONTRADICTS_QUESTION]: 0.99 }, {}, [
    CONTRADICTION,
  ]);
  expect(missing.detections).toEqual([]);
  expect(missing.uncalibrated).toEqual([
    { detector: "contradiction", question: CONTRADICTS_QUESTION },
  ]);
});
