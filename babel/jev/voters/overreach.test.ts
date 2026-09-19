import { expect, test } from "bun:test";
import { RECORD_KINDS } from "../../contract.ts";
import { advisoriesFor, bankFor } from "../bank/bank.ts";
import { screenRecord } from "../screen/pass.ts";
import type { ScreenedRecord } from "../screen/screener.ts";
import { OVERCLAIMS_QUESTION, OVERREACH } from "./overreach.ts";

/*
  WHAT THIS VOTER HAS TO GET RIGHT, AND IT IS MOSTLY WHAT IT REFUSES TO SAY.

  The voter's value is that it proposes a second look at a record whose language runs ahead of
  its material. Its RISK is that it proposes one for everything, which is what the yes/no form of
  this question did at 92.4% and what a misread answer would do again — so most of what is tested
  here is the set of answers it declines to speak about:

  - THE LINE IS THE BANK'S. A record at the top level is proposed for; one below the line is not.
  - A ROW THAT ADVISES NOTHING PROPOSES NOTHING. The bank's lower line FIRES on a well-matched
    record and carries `none`, so an admitted, firing row still yields no suggestion. That is the
    property `suggests: null` exists for and the one a plausible refactor would lose.
  - AN ANSWER ON THE WRONG SCALE IS NOT AN ANSWER. `0.5`, `7`, `"high"` and an unanswered
    question are each no reading at all, because a coercion that guessed the scale is how a cut
    comes to fire on an entire corpus.
  - IT PROPOSES AND NEVER RULES. What reaches the operator is a next action out of the closed
    vocabulary, carrying the wording and the measured share behind it.
*/

const RECORD: ScreenedRecord = {
  id: "fnd_00000001",
  revision: 4,
  kind: "finding",
  title: "a finding",
  text: "the contract check asserts the strings the controller emits",
};

/** The one voter under test, driven exactly as the pass drives it. */
function screen(answers: Readonly<Record<string, number | string>>) {
  return screenRecord(RECORD, answers, [OVERREACH]);
}

test("a record whose wording runs well ahead of its material is proposed for another pass", () => {
  const { suggestions, failed } = screen({ overclaims: 3 });
  expect(failed).toEqual([]);
  expect(suggestions).toHaveLength(1);
  const suggestion = suggestions[0];
  expect(suggestion?.kind).toBe("develop-further");
  expect(suggestion?.recordId).toBe("fnd_00000001");
  expect(suggestion?.revision).toBe(4);
  // The operator is answering a proposal, so it names the level, the wording he can read back
  // and the share the line was measured to fire on — the three facts that let him disagree with
  // the LINE rather than only with the record.
  expect(suggestion?.summary).toContain("says more than it shows");
  expect(suggestion?.rationale).toContain("finding@2");
  expect(suggestion?.rationale).toContain("31.7%");
});

test("an advisory that advises nothing produces nothing, though it fires", () => {
  // The bank's lower line is admitted and fires here — this is not "no row matched".
  const fired = advisoriesFor("finding").filter(
    (advisory) => advisory.question === OVERCLAIMS_QUESTION && advisory.suggests === null,
  );
  expect(fired).toHaveLength(1);
  expect(fired[0]?.when).toEqual({ op: "at-most", value: 1 });
  expect(screen({ overclaims: 1 }).suggestions).toEqual([]);
  expect(screen({ overclaims: 0 }).suggestions).toEqual([]);
});

test("a record between the two lines is spoken about by neither", () => {
  expect(screen({ overclaims: 2 }).suggestions).toEqual([]);
});

test("an answer that is not one of the described levels is no reading at all", () => {
  // A projection on a 0-to-1 scale, an index past the last level, a word, and no answer.
  for (const answered of [0.5, 7, -1, "high"] as const) {
    expect(screen({ overclaims: answered }).suggestions).toEqual([]);
  }
  expect(screen({}).suggestions).toEqual([]);
});

test("every kind the bank calibrated it on can be spoken about", () => {
  // The voter declares every record kind and reads which ones it may speak for off the bank, so
  // a document whose row was retired silently would show up here as a kind that says nothing.
  for (const kind of RECORD_KINDS) {
    const rows = bankFor(kind).advisories.filter(
      (advisory) => advisory.question === OVERCLAIMS_QUESTION,
    );
    expect(rows).toHaveLength(2);
    expect(rows.every((advisory) => advisory.admitted)).toBe(true);
  }
});
