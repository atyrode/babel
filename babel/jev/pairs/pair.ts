import type { NextAction } from "../../contract.ts";
import type { ScreenedRecord } from "../screen/screener.ts";

/*
  TWO RECORDS AT A TIME (#357, #358): WHAT A PAIR IS, AND WHY IT IS A DIFFERENT UNIT.

  Every voter this part ships judges ONE record: `overreach`, `settleable` and `specificity` are
  pure functions of a record and the answers already paid for, and the intake screen pays once per
  record and asks all of them. These two relations do not fit that shape, because neither is a
  property of a record. A contradiction is a property of a PAIR — two claims that cannot both be
  true — and so is a supersession, which is one record describing a later state of what another
  describes. A voter that took one record could not see either.

  THE COST STRUCTURE IS THE WHOLE OF THE DESIGN PROBLEM AND IT IS ANSWERED NEXT DOOR. The imported
  corpus is 6,038 records, which is 18,225,703 unordered pairs; a pass that enumerated them would
  not be slow, it would be impossible, and that is exactly why nothing in Babel has ever looked
  for either relation. What changed is that the corpus now has an index (#337), so a candidate
  pair can be RETRIEVED rather than enumerated. `pairs/propose.ts` owns that and states its own
  bound; nothing in this file proposes anything.

  A PAIR CARRIES ITS OWN INSTANTS, which is the one field {@link PairRecord} adds to what a voter
  sees. #358 asks for the stronger signal over the self-report Babel already has, and what makes
  it stronger is naming what superseded what AND WHEN: an operator asked to fold a record is owed
  the two dates without a second read. It is one field rather than the whole peel for
  `screen/screener.ts`'s own reason — a pair judgement that reached the store would turn one read
  per record into one read per record per relation.

  SYMMETRIC AND DIRECTED ARE TWO TYPES, NOT ONE TYPE WITH A FLAG, and this is the load-bearing
  decision of the slice. {@link ContradictionDetection} carries a `records` tuple in a canonical
  order and has NO field naming one of the two, so nothing downstream can read a favourite out of
  it — #357's point is that one of the pair may well be the correct one and Jev is not the thing
  that decides which. {@link SupersessionDetection} carries `stale` and `fresh` and has no
  symmetric field, so a consumer must say which side it means. The two are therefore impossible
  to confuse in a signature, and each one's behaviour under swapping the pair is the proof of
  which it is: a contradiction is INVARIANT under the swap and a supersession INVERTS. Getting a
  supersession backwards would put a stale record in front of a fresh one, which is worse than
  missing it entirely, so the direction is held in three places rather than one — in the type, in
  the value's sensitivity to the order it was asked in, and in which record the suggestion is
  delivered beside (`pairs/detect.ts`).

  NEITHER RELATION HAS AN ADMITTED BANK ROW, AND THAT IS AN HONEST STATE RATHER THAN A GAP.
  Two separate reasons, and both of them are reasons not to write a number:

    - THE BANK HAS NOWHERE TO PUT ONE. `BankSchema` holds exactly one document per RECORD kind and
      a pair is not a record kind — the questions here are asked of two records that may be of
      two different kinds. Extending the bank to pairwise documents is a schema change with its
      own version bump and its own review, and it is named in this slice's report rather than
      done inside it.
    - THE STUDY'S DENOMINATOR IS NOT THE BANK'S. `admits()` retires a side firing on under 2% or
      over 95% of its KIND, and the argument is that a question answering the same way for every
      record is broken rather than calibrated. It does not transfer to a pair: the study measured
      40 contradicting and 36 superseding pairs out of 2,000 CANDIDATE PAIRS, and a relation that
      holds between 2% of candidate pairs is a rare signal, which is the entire point of looking
      for it. Passing a pair rate through a record-rate band would retire both relations for
      being rare.

  So a cut is the CALLER's to state, there is no default for one anywhere in this directory, and a
  detector whose question has no stated cut is REPORTED as uncalibrated rather than quietly
  producing nothing. The study's own reporting cuts and the counts measured at them travel as
  {@link PairObservation} data beside each detector, as measurements rather than as defaults —
  every one of them taken on one operator's instance, over his own imported Go-era output, at one
  date, under one recipe and model set, and on a LEXICALLY BLOCKED sample, which #357 notes makes
  the rate a floor rather than an estimate. Read `docs/jev-case-study-audit.md` §0 before quoting
  one as if it described Babel.

  JEV COMPUTES AND PAYS; A CALLER DELIVERS. `screen/pass.ts` argues why nothing here may write:
  the `suggest` door declares `containers:write`, a cross-plugin call is graded against the
  CALLER's ceiling, and the allow-list is keyed on a principal the host does not expose
  (atyrode/manifold#770). What the part DOES hold is `containers:read` (#404) and
  `services:invoke`, so the reading and the judgement are its own: `pairs/pass.ts` retrieves
  through `babel.search`, reads through `babel.record` and buys one answer per ordered pair.

  NOTHING IN THIS FILE OR THE TWO DETECTORS DOES ANY OF THAT. {@link PairSubject} contains no
  handle on anything and {@link PairDetector} is synchronous, which is the same rule
  `screen/screener.ts` holds a voter to and for the same two reasons: a detector that could
  reach the store would be untestable without a database, and one that read per pair would turn
  one read per record into one read per record per relation. The loop reaches; the relations
  are pure functions of what it brought back.
*/

/**
 * ONE RECORD OF A PAIR: the identity and prose a relation reads, plus its own instant.
 *
 * The base fields are a named `Pick` from {@link ScreenedRecord} rather than a parallel spelling,
 * and they are deliberately the subset both relations use. A per-record voter may also need the
 * claim's cited evidence; a pair relation compares the two claims themselves and carrying every
 * citation twice would change its cost without changing either closed question.
 *
 * `writtenAt` is `records.created_at` as the peel reports it, an ISO-8601 instant. It is here for
 * supersession — a fold the operator is asked to make names two dates — and it is deliberately
 * NOT a gate on the direction; `pairs/supersession.ts` says why at length.
 */
export type PairRecord = Pick<ScreenedRecord, "id" | "revision" | "kind" | "title" | "text"> & {
  readonly writtenAt: string;
};

/**
 * THE TWO RECORDS, IN THE ORDER THEY WERE ASKED ABOUT.
 *
 * The order is part of the question and not decoration: a directed question — does `b` describe a
 * later state of what `a` describes — has a different answer about `(b, a)` than about `(a, b)`,
 * and the only thing that binds an answer to a direction is which record was presented as which.
 * {@link chronological} is how a proposer chooses that order; a detector never assumes it did.
 */
export interface RecordPair {
  readonly a: PairRecord;
  readonly b: PairRecord;
}

/**
 * A MEASUREMENT OF A RELATION AT A CUT, and never a threshold.
 *
 * `of` is a count of CANDIDATE PAIRS, not of records, which is the whole reason these do not go
 * through the bank's admission band — see the head. A row of these says "at this line, this many
 * of that many pairs came back"; it does not say what line to draw, and nothing in this directory
 * reads one to decide anything.
 */
export interface PairObservation {
  /** The confidence the study reported at. Nouls are bounded 0 to 1, as the bank's own are. */
  readonly cut: number;
  /** Pairs at or above the cut. */
  readonly pairs: number;
  /** Candidate pairs judged. */
  readonly of: number;
}

/** A measurement as a suggestion's rationale states it, so the operator can disagree with it. */
export function observedShare(observation: PairObservation): string {
  const share = (observation.pairs / observation.of) * 100;
  return (
    `${String(observation.pairs)} of ${String(observation.of)} candidate pairs ` +
    `(${share.toFixed(1)}%) at >= ${String(observation.cut)}`
  );
}

/**
 * ONE PAIR AS A DETECTOR SEES IT: the two records, the answers already paid for, and the lines
 * the CALLER has stated.
 *
 * `cuts` is keyed by question id and holds nothing by default. An empty map is not "use the
 * study's numbers" and it is not "detect nothing quietly": `detectPair` reports every detector
 * whose question has no line as uncalibrated, so a deployment that has not drawn one can tell
 * that from a deployment that drew one and found nothing.
 */
export interface PairSubject {
  readonly pair: RecordPair;
  readonly answers: Readonly<Record<string, number | string>>;
  readonly cuts: Readonly<Record<string, number>>;
}

/**
 * TWO CLAIMS THAT CANNOT BOTH BE TRUE (#357), AND NO OPINION ABOUT WHICH.
 *
 * `records` is the pair in a canonical order — by record id, ascending — so the value does not
 * depend on which of the two was the anchor of the retrieval that proposed it. There is no
 * `wrong`, no `correct`, no `stale` and no first-class first element: the type has nowhere to put
 * a favourite, which is #357's requirement held as a shape rather than as a rule somebody
 * remembers. What reaches the operator is the pair and the question.
 */
export interface ContradictionDetection {
  readonly relation: "contradiction";
  readonly records: readonly [string, string];
  readonly confidence: number;
  readonly summary: string;
  readonly rationale: string;
}

/**
 * Whether the pair's own instants agree with the direction the judgement claimed.
 *
 * `silent` is the honest third value: two records written in the same instant, or an instant
 * nothing can parse, say nothing about which describes the later state.
 */
export type PairClock = "agrees" | "disagrees" | "silent";

/**
 * ONE RECORD DESCRIBING A LATER STATE OF WHAT ANOTHER DESCRIBES (#358), WITH ITS DIRECTION.
 *
 * `fresh` describes the later state and `stale` the earlier one, and the two names are the
 * direction: there is no symmetric field, so a consumer cannot read this without saying which
 * side it means. `clock` reports whether the records' own instants agree — evidence stated beside
 * the finding, never a veto over it (`pairs/supersession.ts`).
 */
export interface SupersessionDetection {
  readonly relation: "supersession";
  readonly stale: string;
  readonly fresh: string;
  readonly staleWrittenAt: string;
  readonly freshWrittenAt: string;
  readonly clock: PairClock;
  readonly confidence: number;
  readonly summary: string;
  readonly rationale: string;
}

/** What a detector may conclude. The two members are distinguishable by `relation` alone. */
export type PairDetection = ContradictionDetection | SupersessionDetection;

/**
 * ONE DETECTOR. Pure, synchronous, and `null` when it has nothing to say, which is the ordinary
 * case — the same contract a voter has, for the same reasons.
 *
 * `question` is the bank question id its answer is read from, declared here so `detectPair` can
 * tell "no line was stated for this" from "the line was not reached" WITHOUT asking the detector
 * to answer in two channels. A detector declares no record kinds, unlike a voter: a kind list is
 * a claim about which distributions calibrated it, there is no pairwise document to hold one, and
 * a pair may span two kinds anyway.
 */
export interface PairDetector {
  readonly id: string;
  readonly question: string;
  detect(subject: PairSubject): PairDetection | null;
}

/**
 * ONE SUGGESTION A CALLER MAY DELIVER, in `SuggestInputSchema`'s own fields plus the counterpart.
 *
 * A suggestion names ONE record, because the door does: `next_actions` sits beside a record and a
 * revision. So a symmetric relation is delivered as two of these and a directed one as a single
 * suggestion beside the record that needs the work — which is the asymmetry falling out of the
 * relation rather than being decided again.
 *
 * `counterpart` is the other record of the pair, and since #432 it is a door field: it travels
 * as `SuggestInputSchema.subject` and joins the live-uniqueness key, which is what lets the two
 * pairs a record belongs to be two suggestions instead of one overwriting the other. The id is
 * also in the summary the operator reads, because a field is for a caller and a sentence is for
 * him.
 */
export interface PairSuggestion {
  readonly recordId: string;
  readonly revision: number;
  readonly kind: NextAction;
  readonly summary: string;
  readonly rationale: string;
  readonly counterpart: string;
  readonly detector: string;
}

/**
 * A NOUL AS A DETECTOR MAY READ IT: a bounded degree of yes, or nothing.
 *
 * This is `say.ts`'s `levelOf` argument applied to the other question shape, and it is worth
 * writing for the same reason: `screen/screener.ts` admits any finite number, which is the right
 * coercion and not enough on its own. Every noul in the bank is bounded 0 to 1 and the study's
 * confidences are on that scale, so an answer of 7 — or of -1, or of a projection that returned a
 * percentage — is an answer to some other question, and the honest reading of another question's
 * answer is no reading at all. A detector that clamped it would report a contradiction at full
 * confidence for every pair in the corpus.
 */
export function degreeOf(answered: number | string | undefined): number | null {
  if (typeof answered !== "number") return null;
  if (!Number.isFinite(answered)) return null;
  if (answered < 0 || answered > 1) return null;
  return answered;
}

/**
 * The line the caller stated for this question, or `null` because it stated none.
 *
 * A cut outside a noul's own range is no line rather than a line nobody can reach: a caller that
 * passed 70 for 0.7 would otherwise silently detect nothing forever, which is the one failure
 * mode this directory is required not to have.
 */
export function statedCut(cuts: Readonly<Record<string, number>>, question: string): number | null {
  const cut = cuts[question];
  if (typeof cut !== "number" || !Number.isFinite(cut)) return null;
  if (cut < 0 || cut > 1) return null;
  return cut;
}

/**
 * THE UNORDERED IDENTITY OF A PAIR, which is what makes a proposal propose each pair once.
 *
 * Retrieval reaches the same pair from both of its ends — `a` is among `b`'s neighbours exactly
 * when `b` is among `a`'s — so without this a proposal of n anchors would carry every pair twice
 * and every judgement would be paid for twice.
 */
export function pairKey(a: PairRecord, b: PairRecord): string {
  return a.id <= b.id ? `${a.id}\u0000${b.id}` : `${b.id}\u0000${a.id}`;
}

/**
 * THE PAIR IN THE ORDER THE CLOCK PUTS IT IN: the earlier-written record as `a`.
 *
 * A directed question has to be asked in some order and a proposer has to choose one. The
 * record's own instant is the only choice that does not make the answer depend on which end the
 * retrieval started from, and it asks the likelier direction first — but it is a PRESENTATION and
 * nothing more. No detector assumes a pair arrived through here, and `pairs/supersession.ts`
 * refuses to let the clock decide the answer, because a record written later can describe an
 * earlier state and the study never measured how often it does.
 */
export function chronological(a: PairRecord, b: PairRecord): RecordPair {
  if (a.writtenAt < b.writtenAt) return { a, b };
  if (b.writtenAt < a.writtenAt) return { a: b, b: a };
  return a.id <= b.id ? { a, b } : { a: b, b: a };
}
