import type { RecordPosition } from "../../contract.ts";
import { votesFor } from "../bank/bank.ts";
import { panel, readable, tally, type Vote } from "../bank/schema.ts";
import type { JevAnswerStore, JevServices } from "../server/credential.ts";
import { judge, requestFor } from "../server/judge.ts";
import { answersOf, type ScreenedRecord } from "../screen/screener.ts";

/*
  WHERE ONE RECORD STANDS WITH THE PANEL (#355), AND WHY THAT IS NOT A SCORE.

  A single absolute judgement cannot order a corpus: the study's best single question, rounded,
  put 72.9% of records in one bucket and produced four tiers, while the same questions read as
  independent up/down/abstain voters and summed produced a largest bucket of 20.3% and fourteen
  usable tiers. `tally()` in `bank/schema.ts` is that sum and it already existed. What did not
  exist is the thing a consumer reads: a POSITION on one record, which is the sum plus everything
  the sum on its own destroys.

  THE SUM ON ITS OWN DESTROYS THREE DISTINCTIONS, and each of them is a record an operator would
  treat differently:

  1. ONE VOTER BACKING IT AND THREE VOTERS SPLIT 2-1 BOTH SUM TO +1. That is #406's measured
     failure mode one level down — a sort over a net number ranks agreement above disagreement,
     so the records the panel argued about sink to exactly where the records nobody found
     interesting sit. `standing` is therefore a WORD and not a number: `contested` is its own
     answer, and `backed` and `objected` are the unanimous ones. A consumer tells the three apart
     without reading the voters, and `backed`/`objected` name who was on each side when it wants
     to.
  2. NOBODY JUDGED IT AND EVERY VOTER ABSTAINED BOTH SUM TO 0. Jev is optional — absent,
     disabled, out of credit, or handed a record too large to send, `judge()` answers one `null`
     and no voter runs at all. `tally` is therefore NULLABLE and is null in exactly the two
     states where no opinion was heard: `unjudged`, nothing was asked, and `unheard`, a judgement
     came back and not one of the panel's questions was answered in it. A zero means the panel
     looked and shrugged. Missing data has no number, so nothing downstream can sort, threshold
     or average it as though it had one.
  3. A PANEL OF TWO AND A PANEL OF THREE WITH ONE SILENT BOTH SUM THE SAME. So the position
     carries its own denominator: `roster` is the panel the bank admits for this kind, `heard` is
     how many of them the reply put in a position to speak, and `silent` names the rest. A voter
     whose question came back in the wrong shape — a magnitude cut handed the word `"high"` — is
     SILENT and not abstaining, which is `readable()`'s whole reason to exist beside `fires()`.
     A row that cannot be read cannot vote either, which is why the sum here is taken over the
     readable rows rather than over all of them.

  A FAILED VOTER IS NEVER AGREEMENT. `failed` carries the advisory voters that threw on this
  record, by id, and it is the same list the pass reports — a position over a panel where one
  voter crashed is a position with a hole in it, and an operator reading `backed` has to be able
  to see that the roster was not whole. Nothing in this file can turn a failure into a vote: the
  only things that reach the sum are bank rows, and a throw produces no row.

  UP AND DOWN ARE COUNTED IN VOTERS, NOT IN ROWS. A two-sided voter writes one row per side and a
  `choice` voter one row per option it cares about, so a row count would report a panel of
  nineteen for a bank of thirteen voters. `panel()` is that reduction and `roster` uses it too,
  so the numerator and the denominator are counted in the same unit. A voter whose rows somehow
  fire in both directions appears in BOTH lists and nets to nothing: the disagreement inside one
  voter is visible rather than quietly cancelled.

  THE POSITION IS DERIVED AND IS NEVER STORED, and #337's rule is the precedent rather than the
  counter-example. The corpus backfill has no cursor because the pending set IS the gap, and a
  stored cursor would be a second authority that can disagree with the rows; a stored position is
  the same mistake with a different name. It is a pure function of three things that are already
  durable elsewhere — the record's own revision, the bank at the version it ships, and the
  judgement `JevAnswers` memoises — so a stored copy could only ever be the same answer again or
  a stale one nothing can tell from a current one. Worse, it would be a plugin's opinion sitting
  in a table beside the operator's rulings, which is the third writer class #360 refused: Jev
  writes a `next_actions` suggestion through `babel.suggest` and nothing else, and it holds no
  store of its own to write a position into even if it were allowed to. Records and claims are
  append-only and derived state may be replaced — so a position, being derived, is recomputed
  from the rows and can never contradict them.

  THE READ PATH IS A CALL, NOT A COLUMN. {@link positionFor} is the whole of it: a caller hands
  in one record, one judgement is paid for and memoised, and a position comes back. A caller that
  cannot reach Jev at all does not have to special-case anything — `positionOf(record, null)` is
  the same value `positionFor` returns against a hub with no service binding, so "Jev is absent"
  and "Jev was never installed" are one answer and not two code paths.
*/

/**
 * THE POSITION, DERIVED. Pure, total, and the only place a standing is decided.
 *
 * `answers` is `null` for every absence there is, because `judge()` collapses them all into one
 * `null` before any voter is consulted. `votes` defaults to the admitted rows of the record's own
 * kind; a caller passing its own is choosing a panel, which is what the tally has always required
 * of a caller.
 */
export function positionOf(
  record: ScreenedRecord,
  answers: Readonly<Record<string, number | string>> | null,
  options: {
    readonly votes?: readonly Vote[];
    readonly failed?: readonly string[];
  } = {},
): RecordPosition {
  const votes = options.votes ?? votesFor(record.kind);
  const roster = panel(votes);
  const failed = options.failed ?? [];
  const about = {
    recordId: record.id,
    revision: record.revision,
    roster: roster.length,
    failed,
  };
  if (answers === null) {
    // NOT JUDGED YET, and never judged and found wanting. No voter ran, so none is silent either:
    // silence is a voter that was asked, and nothing was asked here.
    return {
      ...about,
      standing: "unjudged",
      tally: null,
      up: 0,
      down: 0,
      backed: [],
      objected: [],
      silent: [],
      heard: 0,
    };
  }
  // A ROW THAT CANNOT BE READ CANNOT VOTE. Taking the sum over the readable rows is what makes
  // "fired" imply "heard" by construction rather than by a comment, and it closes the one way
  // `fires()` can cast from a malformed answer: `is-not` is satisfied by a number, and a number
  // is not an answer to a question asked in named choices.
  const heard = votes.filter((vote) => readable(vote.when, answers[vote.question]));
  const spoke = panel(heard);
  const counted = tally(heard, answers);
  // ONE VOTER IS ONE OPINION, and both lists are read back in the panel's own order rather than
  // in the order the rows happened to fire: `spoke` holds each heard voter once, so filtering it
  // is the deduplication and the ordering at the same time.
  const backed = spoke.filter((voter) => counted.backed.includes(voter));
  const objected = spoke.filter((voter) => counted.objected.includes(voter));
  return {
    ...about,
    standing:
      backed.length > 0 && objected.length > 0
        ? "contested"
        : backed.length > 0
          ? "backed"
          : objected.length > 0
            ? "objected"
            : spoke.length === 0
              ? "unheard"
              : "unremarked",
    tally: spoke.length === 0 ? null : backed.length - objected.length,
    up: backed.length,
    down: objected.length,
    backed,
    objected,
    silent: roster.filter((voter) => !spoke.includes(voter)),
    heard: spoke.length,
  };
}

/**
 * THE READ PATH FOR ONE RECORD: judge it once, and derive where it stands.
 *
 * One call, memoised by `requestKey`, so asking twice about the same wording under the same bank
 * costs once — the same economics the pass rests on, and the reason a caller may ask for a
 * position wherever it reads a record rather than having to batch.
 *
 * With Jev absent, disabled or dry this returns the `unjudged` position and makes no call, which
 * is the same value a caller with no Jev at all derives from `positionOf(record, null)`.
 */
export async function positionFor(
  services: JevServices,
  record: ScreenedRecord,
  options: {
    readonly answers?: JevAnswerStore;
    readonly votes?: readonly Vote[];
  } = {},
): Promise<RecordPosition> {
  const answer = await judge(services, requestFor(record.kind, record.text), options.answers);
  // The memo and the panel travel in one bag: `positionOf` reads the panel out of it and ignores
  // the rest, which is what keeps an absent `votes` absent rather than present and undefined.
  return positionOf(record, answer === null ? null : answersOf(answer), options);
}
