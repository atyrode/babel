import { expect, test } from "bun:test";
import { deliveries } from "./detect.ts";
import type { PairRecord, RecordPair, SupersessionDetection } from "./pair.ts";
import { SUPERSEDES_QUESTION, SUPERSESSION } from "./supersession.ts";

/*
  THE DIRECTED RELATION, with direction proved at the boundary that uses it.

  A field called `direction` is not proof that direction matters: a consumer may ignore it and put
  the suggestion beside whichever record arrived first. These tests reverse the pair and assert
  both the detection AND the delivery reverse — stale/fresh change places and the suggestion moves
  to the newly stale record. That is the failure #358 calls worse than a miss: getting it wrong
  would displace a fresh record with stale state.
*/

function pairRecord(id: string, revision: number, writtenAt: string, title: string): PairRecord {
  return { id, revision, kind: "finding", title, text: title, writtenAt };
}

function pair(a: PairRecord, b: PairRecord): RecordPair {
  return { a, b };
}

function supersession(subject: RecordPair): SupersessionDetection {
  const detected = SUPERSESSION.detect({
    pair: subject,
    answers: { [SUPERSEDES_QUESTION]: 0.88 },
    cuts: { [SUPERSEDES_QUESTION]: 0.7 },
  });
  if (detected === null || detected.relation !== "supersession") {
    throw new Error("the superseding pair was not reported");
  }
  return detected;
}

test("a superseding pair carries its direction and reversing the input reverses the answer", () => {
  const old = pairRecord(
    "fnd_old",
    3,
    "2026-09-15T12:00:00.000Z",
    "The service still uses the retired endpoint",
  );
  const fresh = pairRecord(
    "fnd_fresh",
    8,
    "2026-09-19T12:00:00.000Z",
    "The service now uses the replacement endpoint",
  );
  const forward = supersession(pair(old, fresh));
  const reversed = supersession(pair(fresh, old));

  expect(forward.stale).toBe(old.id);
  expect(forward.fresh).toBe(fresh.id);
  expect(forward.clock).toBe("agrees");
  expect(reversed.stale).toBe(fresh.id);
  expect(reversed.fresh).toBe(old.id);
  expect(reversed.clock).toBe("disagrees");
  expect(reversed).not.toEqual(forward);
});

test("direction decides which record receives the suggestion, so it cannot be incidental", () => {
  const old = pairRecord(
    "fnd_old",
    3,
    "2026-09-15T12:00:00.000Z",
    "The service still uses the retired endpoint",
  );
  const fresh = pairRecord(
    "fnd_fresh",
    8,
    "2026-09-19T12:00:00.000Z",
    "The service now uses the replacement endpoint",
  );
  const forwardPair = pair(old, fresh);
  const reversePair = pair(fresh, old);
  const forward = deliveries(supersession(forwardPair), forwardPair);
  const reversed = deliveries(supersession(reversePair), reversePair);

  expect(forward).toHaveLength(1);
  expect(forward[0]?.recordId).toBe(old.id);
  expect(forward[0]?.revision).toBe(old.revision);
  expect(forward[0]?.counterpart).toBe(fresh.id);
  expect(reversed).toHaveLength(1);
  expect(reversed[0]?.recordId).toBe(fresh.id);
  expect(reversed[0]?.revision).toBe(fresh.revision);
  expect(reversed[0]?.counterpart).toBe(old.id);
});

test("the clock is reported as evidence and does not silently overrule a retrospective record", () => {
  const writtenLaterAboutOldState = pairRecord(
    "fnd_retrospective",
    1,
    "2026-09-19T12:00:00.000Z",
    "A later import describes the retired endpoint",
  );
  const writtenEarlierAboutNewState = pairRecord(
    "fnd_contemporary",
    1,
    "2026-09-15T12:00:00.000Z",
    "An earlier run describes the replacement endpoint",
  );
  const detected = supersession(pair(writtenLaterAboutOldState, writtenEarlierAboutNewState));
  expect(detected.stale).toBe(writtenLaterAboutOldState.id);
  expect(detected.fresh).toBe(writtenEarlierAboutNewState.id);
  expect(detected.clock).toBe("disagrees");
  expect(detected.rationale).toContain("DISAGREE");
});

test("a confidence outside the noul scale is no answer rather than a full-confidence supersession", () => {
  const first = pairRecord("fnd_a", 0, "2026-09-15T12:00:00.000Z", "First");
  const second = pairRecord("fnd_b", 0, "2026-09-19T12:00:00.000Z", "Second");
  const detected = SUPERSESSION.detect({
    pair: pair(first, second),
    answers: { [SUPERSEDES_QUESTION]: 88 },
    cuts: { [SUPERSEDES_QUESTION]: 0.7 },
  });
  expect(detected).toBeNull();
});
