import type { RecordKind } from "../../contract.ts";
import { advisoriesFor, BANK, bankFor } from "../bank/bank.ts";
import { advises, type Advisory, type Condition, type Question } from "../bank/schema.ts";

/*
  WHAT EVERY VOTER SHARES, AND IT IS DELIBERATELY ONLY THE SAYING.

  A voter is a pure function of a record and an answer, and the three this bundle ships differ
  in exactly the way they should: which question they read, what a level or an option means, and
  what they propose about it. What they must NOT differ in is the arithmetic around that — which
  row of the bank applies, whether it fires, how a number is admitted as a level, and how the
  calibration behind a suggestion is stated to the operator. A fourth hand-written copy of a
  four-armed comparison is the one that is wrong, and a comparison that is wrong here produces a
  confident proposal about a record nobody judged.

  THERE IS NO SHARED `screen` HERE, and that is on purpose. A base class or a "generic voter"
  parameterised by question id would make the three files look identical and hide the only part
  that matters: each of them decides for itself what it is willing to read, and two of the three
  refuse a question whose SHAPE has been reworded out from under them. That refusal is the
  calibration, not boilerplate.
*/

/**
 * THE DOOR'S OWN BOUNDS. `SuggestInputSchema` (`babel/contract.ts`) bounds `summary` at 400
 * characters and `rationale` at 2,000 and refuses anything longer, and both strings a voter
 * builds are made partly out of bank text an operator edits. A suggestion refused for length is
 * a suggestion lost, so the clipping happens where the text is built rather than at the door.
 */
export const SUMMARY_CHARS = 400;
export const RATIONALE_CHARS = 2000;

export function clip(text: string, limit: number): string {
  return text.length <= limit ? text : `${text.slice(0, limit - 1).trimEnd()}…`;
}

/**
 * THE ROW THAT APPLIES, or nothing at all.
 *
 * Admitted rows only, because `advisoriesFor` has already dropped the ones whose measured share
 * put them outside the degeneracy band — a kind this voter cannot speak for arrives here as no
 * row rather than as an invented cut. And the row has to FIRE: several rows may name one
 * question, one per option a `choice` cares about or one per side of a score, so "the row for
 * this question" is not a well-formed request and only "the row this answer fires" is.
 *
 * A question Jev did not answer fires nothing. That is the tally's own rule for the same reason:
 * an absent answer is not a judgement, and the part answers `null` for every absence there is.
 */
export function firing(
  kind: RecordKind,
  question: string,
  answers: Readonly<Record<string, number | string>>,
): Advisory | undefined {
  return advisoriesFor(kind).find(
    (advisory) => advisory.question === question && advises(advisory, answers),
  );
}

/**
 * The level Jev answered, or `null` because the answer is not one of the levels the bank
 * describes.
 *
 * THIS IS THE ONE GUARD WORTH WRITING TWICE. `screen/pass.ts` admits a finite number and drops
 * everything else, which is the right coercion and not enough on its own: a policy whose
 * projection returned a score on a 0-to-1 scale would answer 0.5 for a record at the top of its
 * range, and 0.5 is below every plausible cut, so an entire corpus would be suggested at once.
 * An answer that is not an index into the described levels is a projection of some other
 * question, and the honest reading of another question's answer is no reading at all.
 */
export function levelOf(question: Question, answered: number | string | undefined): number | null {
  if (typeof answered !== "number") return null;
  if (!Number.isInteger(answered)) return null;
  if (answered < 0 || answered >= question.criteria.length) return null;
  return answered;
}

/** The bank's own line, in the operator's reading voice rather than as an operator and a number. */
function reads(when: Condition): string {
  switch (when.op) {
    case "at-least":
      return `level ${String(when.value)} or above`;
    case "at-most":
      return `level ${String(when.value)} or below`;
    case "is":
      return `an answer of "${when.value}"`;
    case "is-not":
      return `any answer but "${when.value}"`;
  }
}

/**
 * WHERE THE LINE CAME FROM, in the sentence every suggestion ends with.
 *
 * The operator is being asked to answer a proposal, and the first question he is entitled to ask
 * of one is who decided and on what. So a suggestion names the wording it was made under, the
 * cut, and the share of the corpus that cut was measured to fire on — the three facts that make
 * it possible to disagree with the line rather than only with the record.
 */
export function calibration(kind: RecordKind, advisory: Advisory): string {
  const document = bankFor(kind);
  return (
    `the bank's ${kind}@${String(document.version)} advisory (bank ${String(BANK.version)}) ` +
    `proposes this at ${reads(advisory.when)}, a line measured to fire on ` +
    `${String(advisory.observed.fires)}% of the ${String(advisory.observed.n)} records it was ` +
    `fitted to. it proposes work beside the record; nothing about the record's standing ` +
    `follows from it.`
  );
}
