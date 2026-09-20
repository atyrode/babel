import { expect, test } from "bun:test";
import { RECORD_KINDS } from "../../contract.ts";
import { advisoriesFor, bankFor } from "../bank/bank.ts";
import { screenRecord } from "../screen/pass.ts";
import type { ScreenedRecord } from "../screen/screener.ts";
import { SETTLEABLE, SETTLEABLE_QUESTION } from "./settleable.ts";

/*
  WHAT THE CLASSIFICATION HAS TO BE WORTH.

  A classification that proposed the same action for every kind of check would be a label
  wearing a suggestion's clothes, so what is tested here is that the ACTION follows from the
  kind — a check a run can make is another pass, a check only a running system answers is a
  question for the operator — and that the kinds the corpus almost never names produce nothing
  at all, because a row under the 2% floor proposes the same thing for everything.

  And the answer is read back against the bank's own option list. A choice question whose answer
  arrived as a number, or as a word nobody defined, is a projection of some other question, and
  a voter that proposed on one would be confidently wrong about a record nobody judged.
*/

const RECORD: ScreenedRecord = {
  id: "fnd_00000001",
  revision: 4,
  kind: "finding",
  title: "a finding",
  text: "every proposal in the corpus shares its finding's run",
};

function screen(answers: Readonly<Record<string, number | string>>) {
  return screenRecord(RECORD, answers, [SETTLEABLE]);
}

test("a claim Babel could settle from its own tables is proposed for a pass, not for his desk", () => {
  const { suggestions, failed } = screen({ settleable: "query_own_data" });
  expect(failed).toEqual([]);
  expect(suggestions).toHaveLength(1);
  expect(suggestions[0]?.kind).toBe("develop-further");
  // The kind of check is what he reads, in the bank's own words and without the assessor's
  // counter-example, which exists to separate neighbouring options and is noise in a proposal.
  expect(suggestions[0]?.summary).toBe(
    "what would settle this: A query against the records, runs, edges or events Babel already holds",
  );
  expect(suggestions[0]?.rationale).toContain("37.5%");
});

test("a check only a running system answers becomes a question for the operator", () => {
  const { suggestions } = screen({ settleable: "needs_live_system" });
  expect(suggestions).toHaveLength(1);
  // The action follows from who can perform the check: Babel cannot watch his running system.
  expect(suggestions[0]?.kind).toBe("ask-question");
});

test("the two kinds the corpus almost never names are retired by their own measurement", () => {
  // They are in the document — deleting them would lose the measurement — and out of the
  // admitted set, so the voter says nothing about a record answered either way.
  const rows = bankFor("finding").advisories.filter(
    (advisory) => advisory.question === SETTLEABLE_QUESTION,
  );
  expect(rows).toHaveLength(6);
  expect(rows.filter((advisory) => !advisory.admitted).map((advisory) => advisory.when)).toEqual([
    { op: "is", value: "not_settleable" },
    { op: "is", value: "needs_new_work" },
  ]);
  expect(screen({ settleable: "not_settleable" }).suggestions).toEqual([]);
  expect(screen({ settleable: "needs_new_work" }).suggestions).toEqual([]);
});

test("an answer the bank does not name is no reading at all", () => {
  for (const answered of ["running_it", "QUERY_OWN_DATA", "", 3] as const) {
    expect(screen({ settleable: answered }).suggestions).toEqual([]);
  }
  expect(screen({}).suggestions).toEqual([]);
});

test("every kind carries the same six rows, four of them admitted", () => {
  for (const kind of RECORD_KINDS) {
    expect(
      bankFor(kind).advisories.filter((advisory) => advisory.question === SETTLEABLE_QUESTION),
    ).toHaveLength(6);
    expect(
      advisoriesFor(kind).filter((advisory) => advisory.question === SETTLEABLE_QUESTION),
    ).toHaveLength(4);
  }
});
