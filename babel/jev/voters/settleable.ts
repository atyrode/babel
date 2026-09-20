import { RECORD_KINDS } from "../../contract.ts";
import { bankFor } from "../bank/bank.ts";
import type { Question } from "../bank/schema.ts";
import type { Screener, ScreenSubject, ScreenSuggestion } from "../screen/screener.ts";
import { calibration, clip, firing, RATIONALE_CHARS, SUMMARY_CHARS } from "./say.ts";

/*
  WHAT WOULD SETTLE THIS CLAIM (#335)?

  IT IS THE STUDY'S MOST ROBUST NUMBER AND THAT IS THE WHOLE REASON IT IS HERE. 37.5% of the
  corpus — 365 records of 974 — could be settled by querying tables Babel already holds. Every
  other aggregate share in the study moves ten to twenty points when the options are reordered;
  this one stayed at 38% under all three orderings tested, and its per-record answers agreed
  79.2% of the time across them. It is the one axis whose number can be leaned on.

  IT IS A CHOICE AND THE YES/NO FORM IS WHY. Asked as a bounded yes — "could this be settled by
  running something?" — it answered the same way 92.8% of the time, which is no answer at all.
  Reworded as a choice over WHAT KIND of check, it discriminates properly, and the six kinds
  below are the study's own buckets with the study's own shares beside them.

  WHAT IT CHANGES FOR THE OPERATOR. A record whose claim Babel could settle by reading its own
  tables is a record nobody should be asked to adjudicate, and the corpus shows what happens
  when nothing makes that routable: one rejection in 4,850 status events. Nothing is rejected
  because nothing is checked. So the classification reaches him as a proposal carrying the kind
  of check in its own words, and the ACTION differs by who can perform it — a check a run can
  make is another pass, a check only a running system answers is a question for him, and a claim
  nothing short of the work itself settles is an issue to draft.

  TWO OF THE SIX ROWS ARE RETIRED BY THEIR OWN MEASUREMENT and stay in the document anyway.
  `not_settleable` fired on 1.0% and `needs_new_work` on 0.1%, both under the 2% degeneracy
  floor, so `advisoriesFor` drops them and this voter says nothing about a record answered
  either way. Deleting the rows would lose the measurement that retired them.

  THE COUNT IS NOT HERE. Six is what the document says, not what this file asserts: a choice
  reworded to five kinds or seven is a different question, and the only thing that keeps this
  voter honest about it is that it reads the answer back against the bank's own option list and
  declines anything not on it.

  Read `docs/jev-case-study-audit.md` §0 and §5 before quoting any share here as Babel's.
*/

/** THE QUESTION THIS VOTER READS, spelled once for the row, the wording and the answer leaf. */
export const SETTLEABLE_QUESTION = "settleable";

/**
 * The bank's own description of the option Jev named, or `null` because it named none of them.
 *
 * A `choice` criterion is written `id — what it is; not for: what it is not`, and only the
 * middle part is the operator's: the counter-example exists to stop the assessor sliding two
 * neighbouring options together and is noise in a proposal he has to read. An answer that is not
 * one of the ids is a projection of some other question, and the honest reading of another
 * question's answer is no reading at all — the same guard `levelOf` makes for a score.
 */
function optionOf(question: Question, answered: number | string | undefined): string | null {
  if (typeof answered !== "string") return null;
  for (const criterion of question.criteria) {
    const dash = criterion.indexOf(" — ");
    if (dash === -1 || criterion.slice(0, dash) !== answered) continue;
    const described = criterion.slice(dash + 3);
    const aside = described.indexOf("; not for:");
    return aside === -1 ? described : described.slice(0, aside);
  }
  return null;
}

function screen(subject: ScreenSubject): ScreenSuggestion | null {
  const question = bankFor(subject.record.kind).questions.find(
    (candidate) => candidate.id === SETTLEABLE_QUESTION,
  );
  if (question === undefined) return null;
  // A document that has reworded this back into a bounded yes is one this says nothing about:
  // that shape answered the same way for 92.8% of the corpus, and the lines below were fitted
  // on the other one.
  if (question.type !== "choice") return null;
  const answered = subject.answers[SETTLEABLE_QUESTION];
  const described = optionOf(question, answered);
  if (described === null) return null;
  const advisory = firing(subject.record.kind, SETTLEABLE_QUESTION, subject.answers);
  // NO ADMITTED ROW, NO OPINION. The two kinds of check the corpus almost never names are
  // dropped by `advisoriesFor` before they reach here, which is how a measured retirement stays
  // a measurement instead of becoming a proposal nobody could avoid.
  if (advisory === undefined) return null;
  // A row that advises nothing is an action the operator withdrew and a measurement he kept.
  if (advisory.suggests === null) return null;
  const summary = clip(`what would settle this: ${described}`, SUMMARY_CHARS);
  const rationale = clip(
    `jev answered "${String(answered)}" on ${question.id}, a choice over ` +
      `${String(question.criteria.length)} kinds of check: "${described}". the question is ` +
      `"${question.asks}". ${calibration(subject.record.kind, advisory)} this is the study's ` +
      `most robust axis: its largest bucket held under all three option orderings tested and ` +
      `its per-record answers agreed 79.2% of the time across them, where every other ` +
      `aggregate share moved ten to twenty points.`,
    RATIONALE_CHARS,
  );
  return { kind: advisory.suggests, summary, rationale };
}

/**
 * THE SETTLEABILITY CLASSIFIER AS THE PASS SEES IT.
 *
 * `kinds` is every record kind on purpose: which kinds this speaks for is a fact about the
 * bank's measured distributions, and `firing` is where it is read.
 */
export const SETTLEABLE: Screener = {
  id: "settleable",
  kinds: RECORD_KINDS,
  screen,
};
