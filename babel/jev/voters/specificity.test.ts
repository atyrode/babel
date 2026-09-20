import { expect, test } from "bun:test";
import { RECORD_KINDS } from "../../contract.ts";
import { advisoriesFor, bankFor } from "../bank/bank.ts";
import { screenRecord } from "../screen/pass.ts";
import type { ScreenedRecord } from "../screen/screener.ts";
import { SCREENERS } from "../screen/screeners.ts";
import { SPECIFICITY, VAGUE_QUESTION } from "./specificity.ts";

/*
  THE GATE THAT MUST NOT BE A GATE.

  The one thing this could do wrong that nothing downstream would notice is refuse, hide or
  demote a record. It cannot, because the only value it can return is a proposed next action —
  so what is tested here is the reading rather than the refusing: the line is the bank's and it
  is a `<= 1` over a scale that ASCENDS in concreteness, which is the coding a flip would
  silently invert into a gate that fires on the most specific quarter of the corpus instead of
  the vaguest half.

  The other risk is the one the study names: a question answered on somebody else's scale. A
  projection returning 0.5 for a perfectly specific record is below every plausible cut, so an
  entire corpus would be proposed for at once. An answer that is not an index into the described
  levels is no reading at all.
*/

const RECORD: ScreenedRecord = {
  id: "fnd_00000001",
  revision: 4,
  kind: "finding",
  title: "a finding",
  text: "credential handling here is designed per occasion",
};

function screen(answers: Readonly<Record<string, number | string>>) {
  return screenRecord(RECORD, answers, [SPECIFICITY]);
}

test("a record that names a concern and no move is proposed for another pass", () => {
  const { suggestions, failed } = screen({ vague: 1 });
  expect(failed).toEqual([]);
  expect(suggestions).toHaveLength(1);
  expect(suggestions[0]?.kind).toBe("develop-further");
  expect(suggestions[0]?.summary).toBe(
    "too vague to act on: An area of concern, with no move named.",
  );
  // The line, the wording it was fitted under and the share it fires on, so he can disagree
  // with the line rather than only with the record.
  expect(suggestions[0]?.rationale).toContain("level 1 or below");
  expect(suggestions[0]?.rationale).toContain("54.9%");
});

test("the scale ascends in concreteness, so the specific end is never proposed for", () => {
  // A flip of the coding would turn this gate onto the most specific quarter of the corpus,
  // and every assertion above would still pass. This is the one that would not.
  expect(screen({ vague: 0 }).suggestions).toHaveLength(1);
  expect(screen({ vague: 2 }).suggestions).toEqual([]);
  expect(screen({ vague: 3 }).suggestions).toEqual([]);
  const criteria = bankFor("finding").questions.find(
    (question) => question.id === VAGUE_QUESTION,
  )?.criteria;
  expect(criteria?.at(0)).toBe("No action is implied at all.");
  expect(criteria?.at(-1)).toBe("A specific action an agent could start on.");
});

test("an answer on another question's scale is no reading at all", () => {
  for (const answered of [0.5, 1.5, 4, -1, "1"] as const) {
    expect(screen({ vague: answered }).suggestions).toEqual([]);
  }
  expect(screen({}).suggestions).toEqual([]);
});

test("every kind carries the line, and the whole roster speaks about one vague record", () => {
  for (const kind of RECORD_KINDS) {
    expect(
      advisoriesFor(kind).filter((advisory) => advisory.question === VAGUE_QUESTION),
    ).toHaveLength(1);
  }
  // Three voters, one record, three independent proposals: nothing here ranks or resolves, so
  // two voters with something to say produce two suggestions and not one verdict.
  const { suggestions, failed } = screenRecord(
    RECORD,
    { vague: 1, overclaims: 3, settleable: "needs_live_system" },
    SCREENERS,
  );
  expect(failed).toEqual([]);
  expect(suggestions.map((suggestion) => suggestion.screener)).toEqual([
    "overreach",
    "settleable",
    "specificity",
  ]);
  expect(suggestions.map((suggestion) => suggestion.kind)).toEqual([
    "develop-further",
    "ask-question",
    "develop-further",
  ]);
});
