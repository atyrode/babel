import type { NextAction, RecordKind } from "../../contract.ts";
import type { JevAnswer } from "../server/credential.ts";

/*
  WHAT A VOTER IS, AND WHY IT IS THIS SMALL (#360).

  The intake screen reads a record through Babel's doors, pays for ONE judgement of it, and asks
  every voter what that judgement means. A voter is therefore a pure function of two things and
  has no other reach: the record as the loop read it, and the answers the loop already paid for.

  THE SUBJECT IS THE WHOLE INPUT, BY CONSTRUCTION. A voter cannot reach the store, the record's
  edges, its corroboration, its filings or a second door, and that is a feature twice over: a
  voter that could would be untestable without a database, and a voter that read per record would
  turn one read per record into one read per record PER VOTER. If a voter needs something beyond
  the text and the answers — how many distinct runs a finding rests on is the obvious next ask —
  the answer is not to reach for it. It is to say so, and {@link ScreenedRecord} is widened ONCE,
  for everyone, out of the single peel the loop already reads.

  IT PAYS NOTHING AND IT KNOWS NOTHING ABOUT PAYING. `judge()` is called by the loop, once per
  record for the whole document, and every voter sees the same answers — the study's own
  economics made structural, because the cost is the state and not the question. A voter holding
  its own call would have turned three voters into three bills for one record. Every absence is
  therefore resolved before a voter is consulted: no part, no binding, no credit, a record too
  large to send — all of them are one `null` out of `judge()`, no answers, and no voter call at
  all. The pass records that record as NOT JUDGED YET. It is never "judged and found wanting",
  and no voter is in a position to make that mistake because none of them ran.

  IT ANNOTATES AND NEVER DROPS. The only thing a voter may return is a next action to be proposed
  BESIDE the record, from the closed vocabulary the operator already answers — which the `suggest`
  door writes into `next_actions` and he accepts or declines like any other. There is no return
  value that hides a record, removes it, or moves it down a list, because there is nowhere in this
  type to put one. That is the load-bearing half of #360 held as a shape rather than as a rule.
*/

/**
 * ONE RECORD, AS THE LOOP READ IT THROUGH THE DOORS.
 *
 * `revision` is `records.seq` as the peel's fifth depth reports it, and it travels because the
 * `suggest` door requires it: a suggestion names the wording it judged, so one made against a
 * revision that has since been replaced is refused rather than silently re-attached. A voter
 * never fills it in — see {@link ScreenSuggestion}.
 */
export interface ScreenedRecord {
  readonly id: string;
  readonly revision: number;
  readonly kind: RecordKind;
  readonly title: string;
  /** The record's own words, exactly as they were sent to Jev, and the whole of what was sent. */
  readonly text: string;
}

/**
 * ONE JUDGEMENT, AS A VOTER SEES IT: the record, and the answers already paid for.
 *
 * `answers` is keyed by the bank's own question ids and is COERCED rather than transformed — a
 * leaf is admitted as a `string`, or as a finite `number`, and anything else (null, a boolean, a
 * nested document, `NaN`) is ABSENT rather than present and wrong. Nothing here rounds, clamps or
 * re-scales: a projection that answered `0.5` arrives as `0.5`, because a coercion that guessed
 * which scale an answer was on is how a `<= 1` cut comes to fire on an entire corpus. A voter
 * checks the value it reads is the shape its own question asks for.
 */
export interface ScreenSubject {
  readonly record: ScreenedRecord;
  readonly answers: Readonly<Record<string, number | string>>;
}

/**
 * WHAT A VOTER CONCLUDED, and it is a proposal about the record rather than a verdict on it.
 *
 * `recordId` and `revision` are deliberately absent: they are the loop's, taken from the peel it
 * actually read, so a voter cannot be right about the record and wrong about the wording.
 */
export interface ScreenSuggestion {
  readonly kind: NextAction;
  readonly summary: string;
  readonly rationale: string;
}

/**
 * ONE VOTER. `kinds` is which record kinds it speaks for; `screen` is pure, synchronous, and
 * returns `null` when it has nothing to say, which is the ordinary case.
 *
 * A voter that throws is a bug, and the pass isolates it rather than letting one bad record end a
 * sweep — but it is counted and reported as `failed`, because a voter throwing on everything and
 * a voter with nothing to say must never look alike.
 */
export interface Screener {
  readonly id: string;
  readonly kinds: readonly RecordKind[];
  screen(subject: ScreenSubject): ScreenSuggestion | null;
}

/**
 * Jev's answer as a voter reads it: the scalar leaves, and nothing else.
 *
 * The operator's policy projects the reply, so what arrives is whatever his projection named. A
 * leaf that is not a string or a finite number is dropped rather than carried as `undefined`,
 * which is what lets a voter write `answers[id] === undefined` for "unanswered" and be right.
 */
export function answersOf(answer: JevAnswer): Readonly<Record<string, number | string>> {
  const out: Record<string, number | string> = {};
  for (const [key, value] of Object.entries(answer)) {
    if (typeof value === "string") out[key] = value;
    else if (typeof value === "number" && Number.isFinite(value)) out[key] = value;
  }
  return out;
}
