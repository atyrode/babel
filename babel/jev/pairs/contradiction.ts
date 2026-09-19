import { PAIR_QUESTIONS, type NextAction } from "../../contract.ts";
import { clip, RATIONALE_CHARS, SUMMARY_CHARS } from "../voters/say.ts";
import {
  degreeOf,
  observedShare,
  statedCut,
  type ContradictionDetection,
  type PairDetector,
  type PairObservation,
  type PairSubject,
} from "./pair.ts";

/*
  CLAIMS IN TWO RECORDS THAT CANNOT BOTH BE TRUE (#357).

  WHAT IT IS FOR. Six thousand records written by different runs over weeks contain claims that
  disagree, and until the corpus had an index nothing could find them. The study asked the closed
  question on 2,000 lexically blocked candidate pairs — do `a` and `b` make claims that cannot
  both be true about the same system? — and got 40 pairs at 0.7 and 11 at 0.85, including a
  dispute about whether the corpus's secret hygiene works and another about whether a checkout was
  dirty during one `atyrode infra apply` session. Those are not corpus-quality questions; one of
  them is a security question that has been sitting unresolved because nothing looked.

  IT REPORTS THE PAIR AND IT DOES NOT PICK A SIDE. This is #357's own requirement and the reason
  the relation is symmetric: the interesting case is precisely that one of the two IS the correct
  record, and deciding which is a ruling. Jev proposes and never rules (#360), so there is nowhere
  in {@link ContradictionDetection} to put a favourite — no `wrong`, no `correct`, no privileged
  first element — the pair travels in a canonical order by record id, and the two suggestions it
  is delivered as carry THE SAME SENTENCE beside each record. An operator reading either one
  reads the same words about the same pair. A version of this that named the "likely wrong" record
  would be ruling on wording alone, against a corpus whose measured bias is already that hedged
  prose is punished for hedging (#336).

  TWO SPECIES, AND ONLY ONE OF THEM IS THIS. The study separates explicit self-corrections —
  records whose text opens `CONTRADICTS hyp_…` or `CORRECTION of…` — from genuine factual
  disputes. The first species is a structural repair and is already done elsewhere: #347 shipped
  `server/engine/records.ts`, which writes the edge at creation, and `tools/link-corrections.ts`,
  which swept the ones the import stranded. What this detector adds is the rest: a correction
  nobody marked, and a dispute nobody framed as one.

  THE LINE IS THE CALLER'S AND THERE IS NO DEFAULT. {@link CONTRADICTION_OBSERVED} is what the
  study measured at its own two reporting cuts, and it is data rather than a threshold: the
  denominator is CANDIDATE PAIRS, so it cannot go through the bank's admission band, and the bank
  has no pairwise document to hold a row in. `pairs/pair.ts` argues both halves of that. Every
  number here was measured on one operator's instance, over his own imported Go-era output, at one
  date, under one recipe and model set, by a lexical block that #357 notes undercounts — so the
  rate is a floor and a reason to look, never a target. `docs/jev-case-study-audit.md` §0 is the
  authority on that and it applies to every sentence above.
*/

/** The question this detector reads, asked of the pair as a whole. Symmetric by its wording.
 *  The literal is the family's (`PAIR_QUESTIONS`), so the leaf a policy projects, the leaf the
 *  door's input names a cut for and the leaf read here cannot drift apart. */
export const CONTRADICTS_QUESTION = PAIR_QUESTIONS.contradicts;

/**
 * WHAT THE STUDY MEASURED, at the two cuts it reported and on the sample it reported them for.
 * Read as "at this line, this many of that many candidate pairs came back" — not as a line to
 * draw. See the head, and `pairs/pair.ts` on why these are not bank rows.
 */
export const CONTRADICTION_OBSERVED: readonly PairObservation[] = [
  { cut: 0.7, pairs: 40, of: 2000 },
  { cut: 0.85, pairs: 11, of: 2000 },
];

/**
 * What a contradiction proposes, read by `pairs/detect.ts` when it delivers one. `ask-question`
 * is the honest word in the door's closed vocabulary: the work this creates is a question for the
 * operator — which of these two is right — and every other word in `NEXT_ACTIONS` presumes an
 * answer to it.
 */
export const CONTRADICTION_PROPOSES: NextAction = "ask-question";

function detect(subject: PairSubject): ContradictionDetection | null {
  const { a, b } = subject.pair;
  // A record does not contradict itself. Retrieval matches an anchor against its own prose better
  // than against anything else, so this is the pair a proposer must drop and a detector must not
  // depend on it having dropped.
  if (a.id === b.id) return null;
  const cut = statedCut(subject.cuts, CONTRADICTS_QUESTION);
  if (cut === null) return null;
  const confidence = degreeOf(subject.answers[CONTRADICTS_QUESTION]);
  if (confidence === null) return null;
  if (confidence < cut) return null;
  // CANONICAL ORDER, so the value does not depend on which end of the pair the retrieval started
  // from: the same two records reached from either anchor produce the same detection, byte for
  // byte, which is what "neither side is favoured" means when it is a property rather than a
  // promise.
  const first = a.id <= b.id ? a : b;
  const second = a.id <= b.id ? b : a;
  const records: readonly [string, string] = a.id <= b.id ? [a.id, b.id] : [b.id, a.id];
  const summary = clip(
    `${records[0]} and ${records[1]} cannot both be true; which of them is correct is the ` +
      `operator's call and jev takes none`,
    SUMMARY_CHARS,
  );
  const rationale = clip(
    `jev answered ${confidence.toFixed(2)} on ${CONTRADICTS_QUESTION} — do these two records ` +
      `make claims that cannot both be true about the same system — against a line of ` +
      `${String(cut)} this deployment stated. the pair is reported and nothing is ruled: one of ` +
      `the two may well be the correct record, and saying which is a ruling jev does not make. ` +
      `titles: "${first.title}" and "${second.title}". for scale rather than as a target, the ` +
      `case study ` +
      `measured ${CONTRADICTION_OBSERVED.map(observedShare).join(", ")} on ONE deployment's ` +
      `imported corpus, at one date, under one recipe and model set, over pairs drawn by ` +
      `lexical blocking — which misses a contradiction phrased in different words, so that rate ` +
      `is a floor. there is no calibrated bank row for this relation on this hub yet.`,
    RATIONALE_CHARS,
  );
  return { relation: "contradiction", records, confidence, summary, rationale };
}

/** The contradiction detector as a pair pass sees it. */
export const CONTRADICTION: PairDetector = {
  id: "contradiction",
  question: CONTRADICTS_QUESTION,
  detect,
};
