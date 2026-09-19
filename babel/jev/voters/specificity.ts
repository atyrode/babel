import { RECORD_KINDS } from "../../contract.ts";
import { bankFor } from "../bank/bank.ts";
import type { Screener, ScreenSubject, ScreenSuggestion } from "../screen/screener.ts";
import { calibration, clip, firing, levelOf, RATIONALE_CHARS, SUMMARY_CHARS } from "./say.ts";

/*
  THE SPECIFICITY GATE: "THIS IS TOO VAGUE TO ACT ON" SAID BEFORE ANYBODY SPENDS ATTENTION ON IT.

  This is the one place the study says typed judgement clearly earns its keep, and the reason is
  not that the question is clever. It is that specificity is the corpus's binding defect and
  three unrelated questions found it independently: 22.2% of records were too vague to start
  (24.0% on a disjoint 400-record sample), 50.1% named an area of concern without naming a move,
  and specific hypotheses reached a proposal at 67.3% against 28.3% for broad ones. Read
  `docs/jev-case-study-audit.md` §0 and §5 before quoting any of those as Babel's numbers: they
  are one deployment's imported Go-era corpus, at one date, under one recipe and model set, and
  §5's own measurement is that wording moves an aggregate share ten to twenty points.

  THE SCALE RUNS THE OTHER WAY FROM THE QUESTION'S NAME, AND IT IS THE STUDY'S OWN CODING. The
  question is `vague`; its levels ASCEND in concreteness, from "no action is implied at all" to
  "a specific action an agent could start on", with the corpus at mean 1.581 and sd 0.876 over
  buckets of 4.8%, 50.1%, 20.5% and 24.5%. So the line is `<= 1` and not `>= n`, and the reason
  to keep the study's coding rather than flip it is that every number in the document's cell
  then appears verbatim in its source; a re-coded mean is a number nobody can look up.

  IT FIRES ON MORE THAN HALF THE CORPUS AND THAT IS THE MEASUREMENT, NOT A BUG. 54.9% of records
  sit at or below the line, because half the corpus names a worry without naming a move. It is
  also the cost an owner should read twice: a retroactive sweep proposes work beside one record
  in two. That is what `sweepSize` is for — state how many a pass would add before adding them —
  and what #360's "filtered hard by default" is for. Neither is this voter's to decide: moving
  the line is an edit to a reviewable document, which is the whole reason the line is there.

  WHAT IT PRODUCES IS A SUGGESTION AND IT COULD NOT BE ANYTHING ELSE. A vague record is written,
  filed, read and ranked exactly as it would be without this part: nothing here refuses a record,
  drops one, marks one or moves one down a list. The `ScreenSuggestion` this returns becomes one
  `next_actions` row through `babel.suggest` — a proposed action beside the record, attributed to
  the part and never to the operator, which he accepts or declines like any other. A plugin may
  propose and may never rule, and a gate that could stop an intake would be ruling.

  THE ACTION IS `develop-further` AND IT COMES FROM THE BANK, not from this file.
  `Advisory.suggests` is a word from `NEXT_ACTIONS`, so what to do about a vague record is a
  calibration the operator can edit in a reviewable document, the same as the line itself.

  A SCORE OVER FOUR DESCRIBED LEVELS, WHICH IS THE WHOLE OF WHY IT WORKS. The study's
  neighbouring questions asked this as a bounded yes and answered the same way for almost
  everything — `checkable` at 92.8%, `overclaims` at 92.4%, `overreach` at 96.7%. A question that
  answers the same way for everything is broken rather than calibrated, and the levels are what
  repaired it: the four this reads are the study's own, and they are the wording the
  reverse-coded control mirrored at r = -0.779, which is why this question survived its control
  when four of eleven ideas did not.

  NOTHING HERE IS A THRESHOLD. The line is `bank/questions/<kind>.md`'s advisory row and the
  share it fired on is beside it; a kind whose row is not admitted — under 2% or over 95%, the
  degeneracy band — is a kind this says nothing about, because `advisoriesFor` returns the
  admitted rows alone. That is also why `kinds` below is every record kind: which kinds this
  speaks for is a fact about the measurement, and a list in code would be a second, quieter copy
  of it that could disagree.

  THIS FILE IS PURE, SYNCHRONOUS AND THROWS NOTHING. The judgement is paid for once per record by
  `screen/pass.ts` for the whole document and handed to every screener, so a voter that called
  out on its own would be a second bill for one state. Every absence — no part, no binding, no
  credit, a record too large to send — reaches here as an answer that is simply not in
  `subject.answers`, and lands in the same `null` as an answer this cannot read.
*/

/**
 * THE QUESTION THIS VOTER READS, and the only string tying the three halves together: the
 * advisory row that carries the line, the `### vague` block that carries the wording, and the
 * leaf of Jev's answer. It is spelled once because a typo in any of the three would be a voter
 * that silently never fires.
 */
export const VAGUE_QUESTION = "vague";

function screen(subject: ScreenSubject): ScreenSuggestion | null {
  const question = bankFor(subject.record.kind).questions.find(
    (candidate) => candidate.id === VAGUE_QUESTION,
  );
  // `documentOf` refuses a document that asks a question it never defines, so an advisory with
  // no wording cannot be seeded. It can still be hand-written into the seed, and a screener that
  // threw there would take the whole pass down with it.
  if (question === undefined) return null;
  // THE SHAPE IS PART OF THE CALIBRATION. Reworded as a bounded yes this is the question that
  // answered the same way for 92% of records, and its answer would arrive as a degree rather
  // than as a level — so a document reworded that way is one this says nothing about until
  // somebody has measured the new shape.
  if (question.type !== "score") return null;
  const level = levelOf(question, subject.answers[VAGUE_QUESTION]);
  if (level === null) return null;
  const advisory = firing(subject.record.kind, VAGUE_QUESTION, subject.answers);
  // NO ADMITTED LINE, NO OPINION, AND NO INVENTED CUT.
  if (advisory === undefined) return null;
  // A row that advises nothing is a measurement the operator kept and an action he withdrew
  // (`suggests` reads `none`). Writing anything would invent the word he removed.
  if (advisory.suggests === null) return null;
  const described = question.criteria[level];
  if (described === undefined) return null;
  const summary = clip(`too vague to act on: ${described}`, SUMMARY_CHARS);
  const rationale = clip(
    `jev answered ${String(level)} on ${question.id}, a score over ` +
      `${String(question.criteria.length)} described levels of concreteness, lowest first: ` +
      `"${described}". the question is "${question.asks}". ` +
      `${calibration(subject.record.kind, advisory)} specificity is the corpus's binding ` +
      `defect on three independent measurements: 22.2% of records were too vague to start, ` +
      `50.1% named a concern with no move, and specific hypotheses reached a proposal at 67.3% ` +
      `against 28.3% for broad ones.`,
    RATIONALE_CHARS,
  );
  return { kind: advisory.suggests, summary, rationale };
}

/**
 * THE SPECIFICITY GATE AS THE PASS SEES IT.
 *
 * `kinds` is every record kind on purpose: see the head — which kinds this speaks for is a fact
 * about the bank's measured distributions, and `firing` is where it is read.
 */
export const SPECIFICITY: Screener = {
  id: "specificity",
  kinds: RECORD_KINDS,
  screen,
};
