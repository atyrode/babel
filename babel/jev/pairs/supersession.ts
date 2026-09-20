import { PAIR_QUESTIONS, type NextAction } from "../../contract.ts";
import { clip, RATIONALE_CHARS, SUMMARY_CHARS } from "../voters/say.ts";
import {
  degreeOf,
  observedShare,
  statedCut,
  type PairClock,
  type PairDetector,
  type PairObservation,
  type PairSubject,
  type SupersessionDetection,
} from "./pair.ts";

/*
  ONE RECORD DESCRIBING A LATER STATE OF WHAT ANOTHER DESCRIBES (#358).

  WHY IT BEATS THE SELF-REPORT BABEL ALREADY HAS. The bank asks every record whether what it
  describes has since changed, and 2,138 of the imported 6,038 — 35.4% — say it has. That is a
  third of the corpus raising its hand to say "I may be stale", which is nearly useless on its
  own: it names no successor, carries no date, and cannot be acted on without reading the record
  and hunting for whatever replaced it. A pairwise supersession names WHAT superseded it and WHEN,
  so the older record can be folded with an audit trail instead of merely flagged. Asked on the
  same 2,000 candidate pairs as the contradiction question, at no extra retrieval cost, it
  returned 36 pairs at 0.7.

  THE DIRECTION IS THE WHOLE VALUE AND GETTING IT BACKWARDS IS WORSE THAN MISSING IT. A missed
  supersession leaves the corpus exactly as it is today. A reversed one folds the FRESH record
  behind the stale one, so the operator is shown the superseded state as current and the
  correction disappears — the failure is silent, durable and in the direction of confidence. So
  the direction is load-bearing in four places rather than carried as a label in one:

    1. IN THE TYPE. {@link SupersessionDetection} has `stale` and `fresh` and no symmetric field
       at all, so a consumer cannot read this relation without naming which side it means. There
       is no `records` tuple to reach for by accident, which is the field the contradiction
       detection has and this one deliberately does not.
    2. IN THE VALUE'S SENSITIVITY TO THE ORDER IT WAS ASKED IN. The question is directed — does
       `b` describe a later state of what `a` describes — and it is asked of the pair AS
       PRESENTED. So `detect` reads the direction out of the pair it was handed: the same answer
       about the reversed pair yields the reversed detection, never the same one. A detector that
       derived the direction from anything other than the question's own subject would answer
       identically for both orders, and that is exactly the bug this cannot have.
    3. IN WHERE THE SUGGESTION LANDS. `pairs/detect.ts` delivers ONE suggestion, beside the stale
       record, naming the fresher one — the asymmetry falls out of the relation, because the fresh
       record needs no work. A reversed direction therefore puts the proposal on the wrong record,
       where it is visible to the operator and to a test, rather than hiding in a field nobody
       reads.
    4. IN THE CLOCK, WHICH IS EVIDENCE AND NOT A VETO. Both instants travel on the detection and
       {@link PairClock} says whether they agree with the claimed direction.

  WHY THE CLOCK IS NOT A GATE, WHICH WAS THE DESIGN DECISION OF THIS FILE. Refusing any detection
  whose direction the instants contradict is tempting and it is wrong here. `records.created_at`
  is when a RUN WROTE the record, not when the state it describes held: a run reading an older
  session writes a record today about a state that was already superseded, and the later-written
  record is then the stale one. That happens in this corpus by construction — the whole of it was
  imported from Go-era output over weeks of sessions — and the study never measured how often, so
  a clock gate would refuse true supersessions at a rate nobody knows. Inventing that rule would
  be exactly the invention `docs/jev-case-study-audit.md` §0 forbids. So a disagreement is
  REPORTED, in the rationale the operator reads and in a field a caller can filter on: either the
  judgement is wrong or an instant is, and both are worth his eye. The cheap failure is his to
  make with the fact in front of him; the silent one is the one this file refuses.

  THE LINE IS THE CALLER'S AND THERE IS NO DEFAULT, by the argument `pairs/pair.ts` makes at
  length: the bank holds one document per RECORD kind and none for a pair, and the study's
  denominator is candidate pairs rather than records of a kind, so `admits()` cannot be applied to
  {@link SUPERSESSION_OBSERVED} without passing a pair rate through a record-rate band. An
  uncalibrated relation waiting for its first measurement on this deployment's own intake is an
  honest state; a number somebody typed would not be.
*/

/** The question this detector reads. Directed by its own wording: `b` later than `a`. The
 *  literal is the family's (`PAIR_QUESTIONS`), for `contradiction.ts`'s reason. */
export const SUPERSEDES_QUESTION = PAIR_QUESTIONS.supersedes;

/**
 * WHAT THE STUDY MEASURED, at the one cut it reported, over candidate pairs and not records. Data
 * rather than a threshold — see the head and `pairs/pair.ts`.
 */
export const SUPERSESSION_OBSERVED: readonly PairObservation[] = [
  { cut: 0.7, pairs: 36, of: 2000 },
];

/**
 * THE SELF-REPORT THIS RELATION IS MEASURED AGAINST, kept beside it because the comparison is the
 * argument for the feature: a third of the corpus says it may be stale and names nothing.
 */
export const SELF_REPORTED_STALE = { records: 2138, of: 6038 } as const;

/**
 * What a supersession proposes. `ask-question` for the same reason a contradiction does: folding
 * a record is a ruling, the door's vocabulary has no word for "link these two", and the one thing
 * jev may do is put the question to the operator.
 */
export const SUPERSESSION_PROPOSES: NextAction = "ask-question";

/** The clock's own reading, in the sentence the operator gets rather than as a field name. */
const CLOCK_READS: Record<PairClock, string> = {
  agrees: "agree with that direction",
  disagrees: "DISAGREE with that direction: the record called fresher was written first",
  silent: "say nothing about that direction",
};

function clockOn(staleWrittenAt: string, freshWrittenAt: string): PairClock {
  const stale = Date.parse(staleWrittenAt);
  const fresh = Date.parse(freshWrittenAt);
  // An unparseable instant says nothing, and neither do two identical ones. `silent` rather than
  // `agrees` for both: a clock that answered "agrees" when it could not read itself would make
  // the one corroboration on this detection worthless precisely where it is missing.
  if (!Number.isFinite(stale) || !Number.isFinite(fresh)) return "silent";
  if (fresh > stale) return "agrees";
  if (fresh < stale) return "disagrees";
  return "silent";
}

function detect(subject: PairSubject): SupersessionDetection | null {
  const { a, b } = subject.pair;
  if (a.id === b.id) return null;
  const cut = statedCut(subject.cuts, SUPERSEDES_QUESTION);
  if (cut === null) return null;
  const confidence = degreeOf(subject.answers[SUPERSEDES_QUESTION]);
  if (confidence === null) return null;
  if (confidence < cut) return null;
  // THE DIRECTION IS THE QUESTION'S SUBJECT AND NOTHING ELSE. The question asks whether `b`
  // describes a later state of what `a` describes, so a firing answer makes `b` the fresh record
  // and `a` the stale one, for the pair as it was presented. Nothing here consults the clock, the
  // ids or the kinds to decide it: those would answer the same way for both orders.
  const clock = clockOn(a.writtenAt, b.writtenAt);
  const summary = clip(
    `${a.id} appears superseded by ${b.id}, which describes a later state of the same thing` +
      (clock === "disagrees"
        ? `, though ${b.id} was written first — the instants disagree with the judgement`
        : ""),
    SUMMARY_CHARS,
  );
  const rationale = clip(
    `jev answered ${confidence.toFixed(2)} on ${SUPERSEDES_QUESTION} — does the second record ` +
      `describe a later state of what the first describes — against a line of ${String(cut)} ` +
      `this deployment stated. stale: ${a.id} "${a.title}", written ${a.writtenAt}. fresh: ` +
      `${b.id} "${b.title}", written ${b.writtenAt}. the two instants ${CLOCK_READS[clock]}; ` +
      `they are reported and never used to overrule it, because a record written ` +
      `later can describe an earlier state and nothing has measured how often it does. this is ` +
      `a proposal to fold ${a.id} behind ${b.id}, not a ruling: jev proposes and the operator ` +
      `rules. for scale rather than as a target, the case study measured ` +
      `${SUPERSESSION_OBSERVED.map(observedShare).join(", ")} on ONE deployment's imported ` +
      `corpus, at one date, under one recipe and model set, beside ` +
      `${String(SELF_REPORTED_STALE.records)} of ${String(SELF_REPORTED_STALE.of)} records that ` +
      `self-report as describing something since changed. there is no calibrated bank row for ` +
      `this relation on this hub yet.`,
    RATIONALE_CHARS,
  );
  return {
    relation: "supersession",
    stale: a.id,
    fresh: b.id,
    staleWrittenAt: a.writtenAt,
    freshWrittenAt: b.writtenAt,
    clock,
    confidence,
    summary,
    rationale,
  };
}

/** The supersession detector as a pair pass sees it. */
export const SUPERSESSION: PairDetector = {
  id: "supersession",
  question: SUPERSEDES_QUESTION,
  detect,
};
