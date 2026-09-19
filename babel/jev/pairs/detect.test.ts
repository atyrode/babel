import { expect, test } from "bun:test";
import { CONTRADICTION, CONTRADICTS_QUESTION } from "./contradiction.ts";
import { detectPair } from "./detect.ts";
import type { PairDetector, PairRecord, RecordPair } from "./pair.ts";
import { SUPERSEDES_QUESTION, SUPERSESSION } from "./supersession.ts";

/*
  THE TWO RELATIONS RIDING ONE JUDGEMENT, plus the one failure boundary a corpus pass needs.

  Contradiction and supersession are not alternatives: a correction can disagree with what it
  corrects and be its later state at once. Dropping one when the other fires would turn roster
  order into precedence and lose information. And one malformed answer or detector bug must not
  end the remaining 1,999 pairs, but it must be visible — isolated and counted rather than
  swallowed as "nothing found".
*/

function pairRecord(id: string, minute: number): PairRecord {
  return {
    id,
    revision: 0,
    kind: "finding",
    title: id,
    text: id,
    writtenAt: new Date(Date.UTC(2026, 8, 19, 12, minute)).toISOString(),
  };
}

function pair(a: PairRecord, b: PairRecord): RecordPair {
  return { a, b };
}

test("contradiction and supersession both survive one pair judgement", () => {
  const first = pairRecord("fnd_old", 0);
  const second = pairRecord("fnd_fresh", 5);
  const result = detectPair(
    pair(first, second),
    { [CONTRADICTS_QUESTION]: 0.92, [SUPERSEDES_QUESTION]: 0.88 },
    { [CONTRADICTS_QUESTION]: 0.7, [SUPERSEDES_QUESTION]: 0.7 },
    [CONTRADICTION, SUPERSESSION],
  );
  expect(result.failed).toEqual([]);
  expect(result.uncalibrated).toEqual([]);
  expect(result.detections.map((detection) => detection.relation)).toEqual([
    "contradiction",
    "supersession",
  ]);
});

test("a throwing detector is reported with its pair and does not suppress the next detector", () => {
  const first = pairRecord("fnd_a", 0);
  const second = pairRecord("fnd_b", 5);
  const broken: PairDetector = {
    id: "broken",
    question: "broken",
    detect: () => {
      throw new Error("projection was not a document");
    },
  };
  const result = detectPair(
    pair(first, second),
    { broken: 1, [CONTRADICTS_QUESTION]: 0.9 },
    { broken: 0.7, [CONTRADICTS_QUESTION]: 0.7 },
    [broken, CONTRADICTION],
  );
  expect(result.failed).toEqual([
    {
      detector: "broken",
      records: ["fnd_a", "fnd_b"],
      reason: "projection was not a document",
    },
  ]);
  expect(result.detections.map((detection) => detection.relation)).toEqual(["contradiction"]);
});
