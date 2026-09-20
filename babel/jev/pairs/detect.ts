import { CONTRADICTION, CONTRADICTION_PROPOSES } from "./contradiction.ts";
import {
  statedCut,
  type PairDetection,
  type PairDetector,
  type PairRecord,
  type PairSubject,
  type PairSuggestion,
  type RecordPair,
} from "./pair.ts";
import { SUPERSESSION, SUPERSESSION_PROPOSES } from "./supersession.ts";

/*
  THE PAIR ROSTER, WHAT ONE PAIR PRODUCES, AND WHAT A CALLER DELIVERS (#357, #358).

  THIS IS `screen/pass.ts` ONE UNIT UP, AND DELIBERATELY THE SAME SHAPE. One judgement per pair,
  every detector asked, nothing dropped: the two relations ride the SAME candidate pairs and the
  SAME judgement, which is what made #358 cost nothing on top of #357 in the study and is the
  study's own economics — you pay for the state, not for the question. Two detectors holding their
  own calls would be two bills for one pair over two thousand of them.

  ORDER IS NOT PRECEDENCE, and here it could not be: the two relations are not alternatives. Two
  records can contradict each other AND one of them supersede the other — a correction that
  disagrees with what it corrects is exactly that — and both detections are produced, because
  either one hidden by the other would be a ruling about which reading of the pair matters.

  A DETECTOR THAT THROWS IS ISOLATED AND COUNTED, for `screen/pass.ts`'s reason: one odd pair must
  not end a pass over two thousand, and a guard that swallowed the throw would make a detector
  broken on everything look like a detector with nothing to say.

  AN UNSTATED LINE IS REPORTED, NEVER SILENTLY OBEYED. There is no default cut anywhere in this
  directory, so a deployment that has stated none detects nothing — and the one thing that must
  not happen is for that to look like a clean pass over a corpus with no contradictions in it.
  {@link PairResult.uncalibrated} names every detector whose question has no line, so "we have not
  drawn one yet" and "we drew one and nothing reached it" are different answers.

  NOTHING HERE WRITES, AND THE DELIVERY SHAPE IS WHERE THAT BITES. `deliveries` returns values; a
  caller hands them to `babel.suggest`. `screen/pass.ts` argues why the part cannot make that call
  itself. Two consequences of the door's own schema are worth knowing before reading one:

    - A SUGGESTION NAMES ONE RECORD, because `next_actions` sits beside a record and a revision.
      So a symmetric relation is delivered TWICE, once beside each record, with the same sentence
      on both — which is what keeps "neither side is favoured" true in what the operator actually
      sees — and a directed one is delivered ONCE, beside the record that needs the work. The
      counterpart's id travels in the summary and in `counterpart`.
    - THE DOOR KEEPS ONE LIVE SUGGESTION PER REVISION, KIND AND SUBJECT, and the third column is
      #432, added for this shape. Without it a record in two contradicting pairs would carry
      whichever was written second and the first counterpart would vanish — the door's
      deduplication doing its job on a shape it was not designed for. `counterpart` is what
      travels as `subject`, so two findings about one record are two suggestions and a second
      finding about the SAME counterpart still supersedes rather than duplicating.
*/

/**
 * The detectors this bundle ships, in the order they are consulted, which is not an order of
 * precedence — see the head.
 */
export const DETECTORS: readonly PairDetector[] = [CONTRADICTION, SUPERSESSION];

/** A detector that threw, with the pair it threw on: two different defects to tell apart. */
export interface PairFailure {
  readonly detector: string;
  readonly records: readonly [string, string];
  readonly reason: string;
}

/** A detector that was never consulted because this deployment has stated no line for it. */
export interface PairUncalibrated {
  readonly detector: string;
  readonly question: string;
}

/** What judging one pair produced, with what could not be judged kept beside what was. */
export interface PairResult {
  readonly detections: readonly PairDetection[];
  readonly uncalibrated: readonly PairUncalibrated[];
  readonly failed: readonly PairFailure[];
}

/**
 * EVERY DETECTOR'S VIEW OF ONE JUDGED PAIR. Pure, synchronous and total: it returns for every
 * input, because the only way a detector can end this function is by being the last one.
 */
export function detectPair(
  pair: RecordPair,
  answers: Readonly<Record<string, number | string>>,
  cuts: Readonly<Record<string, number>>,
  detectors: readonly PairDetector[] = DETECTORS,
): PairResult {
  const records: readonly [string, string] =
    pair.a.id <= pair.b.id ? [pair.a.id, pair.b.id] : [pair.b.id, pair.a.id];
  const detections: PairDetection[] = [];
  const uncalibrated: PairUncalibrated[] = [];
  const failed: PairFailure[] = [];
  const subject: PairSubject = { pair, answers, cuts };
  for (const detector of detectors) {
    if (statedCut(cuts, detector.question) === null) {
      uncalibrated.push({ detector: detector.id, question: detector.question });
      continue;
    }
    let detected: PairDetection | null;
    try {
      detected = detector.detect(subject);
    } catch (thrown) {
      failed.push({
        detector: detector.id,
        records,
        reason: thrown instanceof Error ? thrown.message : String(thrown),
      });
      continue;
    }
    if (detected === null) continue;
    detections.push(detected);
  }
  return { detections, uncalibrated, failed };
}

function memberOf(pair: RecordPair, id: string): PairRecord {
  if (pair.a.id === id) return pair.a;
  if (pair.b.id === id) return pair.b;
  throw new Error(`${id} is not a record of the pair ${pair.a.id}/${pair.b.id}`);
}

/**
 * THE SUGGESTIONS ONE DETECTION IS DELIVERED AS: two for a contradiction, one for a supersession.
 *
 * The revision comes from the pair the LOOP read and never from a detector, which is
 * `screen/screener.ts`'s rule and the door's requirement both: a suggestion names the wording it
 * judged, so one made against a revision since replaced is refused rather than re-attached.
 */
export function deliveries(detection: PairDetection, pair: RecordPair): readonly PairSuggestion[] {
  if (detection.relation === "contradiction") {
    const first = memberOf(pair, detection.records[0]);
    const second = memberOf(pair, detection.records[1]);
    // Same summary, same rationale, both records: the symmetry is in what he reads, not only in
    // the value. Neither suggestion says anything the other does not.
    return [
      {
        recordId: first.id,
        revision: first.revision,
        kind: CONTRADICTION_PROPOSES,
        summary: detection.summary,
        rationale: detection.rationale,
        counterpart: second.id,
        detector: CONTRADICTION.id,
      },
      {
        recordId: second.id,
        revision: second.revision,
        kind: CONTRADICTION_PROPOSES,
        summary: detection.summary,
        rationale: detection.rationale,
        counterpart: first.id,
        detector: CONTRADICTION.id,
      },
    ];
  }
  // ONE SUGGESTION, BESIDE THE STALE RECORD. The fresh one needs no work, and putting the
  // proposal where the work is makes a reversed direction visible to the operator rather than
  // legible only to whoever reads the field.
  const stale = memberOf(pair, detection.stale);
  return [
    {
      recordId: stale.id,
      revision: stale.revision,
      kind: SUPERSESSION_PROPOSES,
      summary: detection.summary,
      rationale: detection.rationale,
      counterpart: detection.fresh,
      detector: SUPERSESSION.id,
    },
  ];
}
