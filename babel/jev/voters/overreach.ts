import { RECORD_KINDS } from "../../contract.ts";
import { bankFor } from "../bank/bank.ts";
import type { Screener, ScreenSubject, ScreenSuggestion } from "../screen/screener.ts";
import { calibration, clip, firing, levelOf, RATIONALE_CHARS, SUMMARY_CHARS } from "./say.ts";

/*
  DOES THE RECORD'S CONFIDENCE OUTRUN ITS EVIDENCE (#336)?

  THIS VOTER EXISTS BECAUSE OF A MEASURED BIAS IN BABEL'S OWN READING, AND NOT BECAUSE
  OVERCLAIMING IS DISTASTEFUL. Forty findings were rewritten twice under instruction to preserve
  every fact, number, identifier and causal claim and to change only voice. Hedging one whose
  facts were VERIFIABLY unchanged cost it 0.48 on worth and 0.68 on evidence strength — 30 times
  and 43 times the model's own repeat-noise floor of 0.016 — and 12 of the 37 whose facts the
  control confirmed unchanged were re-routed on tone alone, desk membership moving 22 plain to
  28 assertive to 17 hedged. A record that hedges honestly is currently punished for honesty, and
  since Babel's records are written by the same system that asks for them to be judged, that is a
  bias in the AUTHOR's prose habits rather than in the finding.

  WHICH IS WHY IT PROPOSES AND CANNOT RANK. The correction for a bias that moves standing must
  not itself be able to move standing, or it is the same bias again with a better name on it. So
  this is an advisory and not a vote: `Advisory` has no `casts` and no `voter`, `tally()` cannot
  be handed one, and the only thing reaching the operator is a proposed action beside the record
  which he accepts or declines through `decide`.

  IT IS A SCORE AND THE YES/NO FORM IS WHY. Asked as a bounded yes, this question answered the
  same way for 92.4% of the corpus, which is no answer at all — the study's own list of three
  broken questions has it beside `checkable` at 92.8% and `overreach` at 96.7%. Over four
  described levels it discriminates: the corpus sits at mean 2.227 with sd 0.444 and 31.7% at the
  top level. Reworded back into a degree by an operator, it would answer a different question
  than the one the lines below were fitted on, so {@link OVERREACH} says nothing about a document
  whose `overclaims` has stopped being a score.

  IT IS THE MOST INDEPENDENT QUESTION IN THE BANK. Its closest neighbour correlates at 0.154
  against 0.535 for the most redundant genuine pair, so it measures something no other voter
  measures — which is the argument for adding it rather than reweighting what is already asked.

  Read `docs/jev-case-study-audit.md` §0 and §4 before quoting any share here as Babel's: it is
  one deployment's imported Go-era corpus, at one date, under one recipe and model set.
*/

/**
 * THE QUESTION THIS VOTER READS, and the study's own name for it (`docs/jev-case-study-audit.md`
 * §4). Higher is worse: the score is how far the language runs AHEAD of the material, so the
 * top level is a record whose central claim is asserted rather than shown.
 */
export const OVERCLAIMS_QUESTION = "overclaims";

function screen(subject: ScreenSubject): ScreenSuggestion | null {
  const question = bankFor(subject.record.kind).questions.find(
    (candidate) => candidate.id === OVERCLAIMS_QUESTION,
  );
  // `documentOf` refuses a document that asks a question it never defines, so an advisory with
  // no wording cannot be seeded — but it can be hand-written into the seed, and a voter that
  // threw here would take the whole pass down with it.
  if (question === undefined) return null;
  // THE SHAPE IS PART OF THE CALIBRATION: see the head. A degree is not a level.
  if (question.type !== "score") return null;
  const level = levelOf(question, subject.answers[OVERCLAIMS_QUESTION]);
  if (level === null) return null;
  const advisory = firing(subject.record.kind, OVERCLAIMS_QUESTION, subject.answers);
  if (advisory === undefined) return null;
  // A ROW THAT ADVISES NOTHING IS AN ANSWER. The bank's lower line records where the corpus sits
  // when its wording is matched by its material — 5.9% at or below level 1, none at all at level
  // 0 — and proposes nothing for it, because "this record does not overclaim" is a measurement
  // and not a piece of work. Writing anything here would invent the word the operator removed.
  if (advisory.suggests === null) return null;
  const described = question.criteria[level];
  if (described === undefined) return null;
  const summary = clip(`says more than it shows: ${described}`, SUMMARY_CHARS);
  const rationale = clip(
    `jev answered ${String(level)} on ${question.id}, a score over ` +
      `${String(question.criteria.length)} described levels: "${described}". the question is ` +
      `"${question.asks}". ${calibration(subject.record.kind, advisory)} tone is worth asking ` +
      `about separately because it already moves this corpus: hedging a record whose facts were ` +
      `unchanged cost it 0.48 on worth and 0.68 on evidence strength, 30 and 43 times the ` +
      `assessor's own noise floor.`,
    RATIONALE_CHARS,
  );
  return { kind: advisory.suggests, summary, rationale };
}

/**
 * THE TONE-AGAINST-EVIDENCE VOTER AS THE PASS SEES IT.
 *
 * `kinds` is every record kind on purpose: which kinds this speaks for is a fact about the
 * bank's measured distributions, and `firing` is where it is read. A list in code would be a
 * second, quieter copy of that fact which could disagree with the documents.
 */
export const OVERREACH: Screener = {
  id: "overreach",
  kinds: RECORD_KINDS,
  screen,
};
