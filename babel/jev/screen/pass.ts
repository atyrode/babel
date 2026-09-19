import type { GuestActions } from "@manifold/plugin-kit/server";
import { ACTIONS, BABEL_PLUGIN_ID } from "../../contract.ts";
import type { JevAnswerStore, JevServices } from "../server/credential.ts";
import { judge, requestFor } from "../server/judge.ts";
import { positionOf } from "../tally/position.ts";
import type { RecordPosition, Standing } from "../../contract.ts";
import {
  answersOf,
  type ScreenedRecord,
  type Screener,
  type ScreenSuggestion,
} from "./screener.ts";
import { SCREENERS } from "./screeners.ts";

/*
  THE INTAKE SCREEN (#360): ONE JUDGEMENT PER RECORD, EVERY VOTER ASKED, NOTHING DROPPED.

  WHAT THE PASS IS FOR. A record is read through Babel's doors, judged once, and every voter is
  asked what that judgement means about it. What comes back is a SUGGESTION beside the record,
  which the `suggest` door (#412) turns into one `next_actions` row attributed to the part and
  never to the operator. Nothing here refuses a record, hides one, removes one or moves one down
  a list, and the reason it cannot is structural rather than a rule somebody remembers:
  {@link ScreenSuggestion} has nowhere to put a verdict, and this file never touches the record
  it screened — the record it was handed comes back out of the pass byte for byte.

  ONE BILL PER RECORD FOR THE WHOLE DOCUMENT. `judge()` is called here, once, and the same
  answers are handed to every voter — the study's own economics made structural, because the cost
  is the STATE and not the question. Three voters holding their own calls would have been three
  bills for one record, over a corpus of six thousand.

  EVERY ABSENCE IS ONE `null`, AND IT IS RESOLVED BEFORE A VOTER IS CONSULTED. No part, no service
  binding, no credit, an unreadable reply, a record too large to send: all of them come back from
  `judge()` as `null`, and such a record is counted UNJUDGED and screened by nobody. It is never
  "judged and found wanting", and no voter is in a position to make that mistake because none of
  them ran. That is the fallback #360 makes the acceptance criterion, held as one branch.

  A VOTER THAT THROWS IS ISOLATED AND COUNTED. One bad record must not end a sweep over six
  thousand of them, so the call is guarded — but a guard that swallowed the throw would make a
  voter broken on everything and a voter with nothing to say look identical, which is the worse
  failure of the two. So every throw is a {@link ScreenFailure} row in the report, naming the
  voter, the record and the reason, and the pass carries on.

  SIZE THE SWEEP BEFORE RUNNING IT. #360 requires the imported corpus to be swept and the queue
  to be filtered hard by default; at 6,038 records an unfiltered sweep is the one-list problem
  the desk/queue/shelf split was built to end, one level down. {@link sweepSize} asks the door
  how many revisions this suggester has already judged and how many it has not, which is exactly
  what one more pass would add, and it asks BEFORE anything is written. It is a reading call and
  inside the part's own ceiling.

  THE PASS HAS A READING HALF, AND IT IS THE ONLY OUTPUT THAT NEEDS NO DOOR (#355). Screening one
  record produces a {@link RecordPosition} beside its suggestions: the panel's tally over the one
  judgement already paid for, with the voters that backed it, the voters that objected, and the
  voters the reply left nothing readable for. It is DERIVED and written nowhere — `tally/position.ts`
  argues that at length — so it costs the pass one object per judged record and costs the store
  nothing, and a report over six thousand records carries the counted standings rather than a
  mean, because an average over the corpus hides exactly the forty records the panel argued about.

  THE PASS DOES NOT DELIVER, AND TODAY NOTHING IN THIS BUNDLE DOES. `deliver` is the caller's
  function and the pass hands each suggestion to it unchanged; it does not catch what that
  function throws, because a voter throwing is a bug in a pure function and the pass's to
  absorb, while a door refusing is an ANSWER — already judged, wording superseded, record ruled
  on — and whether that is expected is the caller's question. There is deliberately no call to
  `babel.suggest` anywhere in this part: the door exists (#412) and its allow-list is keyed on
  PRINCIPAL, but the door declares `containers:write` and a cross-plugin call is graded against
  the CALLER's own manifest ceiling for every engine capability the callee declares. Declaring
  `containers:write` here to reach one door would open `rule`, `decide`, `comment`, `file` and
  every other act at the same stroke, which is the authority #360's decision exists to withhold.
  The mechanism that would let the host admit this one write without the rest is open upstream
  as atyrode/manifold#770. Until it closes, Jev computes advisories and delivers nothing, and
  that is the intended state rather than a gap.
*/

/** What one voter concluded, with the record and wording the LOOP read rather than the voter. */
export interface PassSuggestion extends ScreenSuggestion {
  readonly recordId: string;
  readonly revision: number;
  /** Which voter proposed it, so a report can be read per voter as well as per record. */
  readonly screener: string;
}

/**
 * A voter that threw. It is a bug in a pure function of two arguments, so it is reported with
 * both the voter and the record: a voter that throws on one odd record and a voter that throws
 * on everything are different defects and the report has to tell them apart.
 */
export interface ScreenFailure {
  readonly screener: string;
  readonly recordId: string;
  readonly reason: string;
}

/**
 * What screening one record produced: where the panel stands on it, the suggestions its advisory
 * voters proposed, and the voters that failed kept beside what succeeded.
 *
 * The position is the reading half (#355) and the suggestions are the writing half (#410). They
 * are two fields rather than one because they answer to different authorities: a position is
 * derived from the bank and the judgement and is never written anywhere, while a suggestion
 * becomes a `next_actions` row the operator answers.
 */
export interface ScreenResult {
  readonly position: RecordPosition;
  readonly suggestions: readonly PassSuggestion[];
  readonly failed: readonly ScreenFailure[];
}

/**
 * WHAT A PASS DID, in the four numbers an owner needs and the standings they resolve into.
 *
 * `judged + unjudged === read` always, which is what makes "Jev was off" and "Jev found nothing"
 * impossible to confuse: a pass with the part removed reports every record unjudged and nothing
 * suggested, and a pass that ran and liked the corpus reports every record judged and nothing
 * suggested.
 *
 * `standings` is the same fact one level finer, and it is a COUNT PER WORD rather than an average
 * or a top slice: a sweep can say how many records the panel argued about before anything is
 * written, and a mean over a corpus of six thousand would hide exactly the forty records that
 * reached a tally of six or more. `standings.unjudged === unjudged` always, so the two halves of
 * the report cannot disagree about what was checked.
 */
export interface PassReport {
  readonly read: number;
  readonly judged: number;
  readonly unjudged: number;
  readonly suggested: number;
  readonly failed: readonly ScreenFailure[];
  readonly standings: Readonly<Record<Standing, number>>;
}

/** What a sweep would add, as the reading half of `babel.suggest` answers it. */
export interface SweepSize {
  /** Live suggestions the operator has not answered yet. */
  readonly outstanding: number;
  /** Record revisions this suggester has already judged: the durable "do not judge twice" mark. */
  readonly judged: number;
  /** Live revisions it has not judged, which is exactly what one more pass would look at. */
  readonly unjudged: number;
}

/**
 * EVERY VOTER'S VIEW OF ONE JUDGED RECORD. Pure, synchronous, and total: it returns for every
 * input, because the only way a voter can end this function is by being the last one.
 *
 * A voter is asked only about the kinds it declares. That is not an optimisation — a voter reads
 * its own line out of the bank document for the record's kind, and a kind it does not speak for
 * is a kind it has no calibrated line on.
 */
export function screenRecord(
  record: ScreenedRecord,
  answers: Readonly<Record<string, number | string>>,
  screeners: readonly Screener[] = SCREENERS,
): ScreenResult {
  const suggestions: PassSuggestion[] = [];
  const failed: ScreenFailure[] = [];
  for (const screener of screeners) {
    if (!screener.kinds.includes(record.kind)) continue;
    let proposed: ScreenSuggestion | null;
    try {
      proposed = screener.screen({ record, answers });
    } catch (thrown) {
      failed.push({
        screener: screener.id,
        recordId: record.id,
        reason: thrown instanceof Error ? thrown.message : String(thrown),
      });
      continue;
    }
    if (proposed === null) continue;
    // The record and the wording are the LOOP's, taken from the peel it actually read, so a
    // voter cannot be right about the record and wrong about the revision it judged.
    suggestions.push({
      ...proposed,
      recordId: record.id,
      revision: record.revision,
      screener: screener.id,
    });
  }
  // WHERE THE PANEL STANDS, derived from the same one judgement the voters read and stored
  // nowhere. The failures travel into it by id: a position over a panel where a voter crashed is
  // a position with a hole in it, and `backed` must never be read as though the roster were
  // whole. See `tally/position.ts`.
  const position = positionOf(record, answers, {
    failed: failed.map((failure) => failure.screener),
  });
  return { position, suggestions, failed };
}

/**
 * ONE PASS OVER THE RECORDS IT WAS GIVEN.
 *
 * `deliver` is the caller's, and every suggestion goes through it in the order the roster
 * produced them. The pass never batches, dedupes or reorders: `babel.suggest` refuses a
 * suggestion it has already written for a revision, so the door is the deduplicator and a second
 * one here would be a second answer to the question of what has been judged.
 *
 * `answers` is the memo the judgements are held in; left out, it is the server half's own.
 */
export async function screenPass(
  services: JevServices,
  records: readonly ScreenedRecord[],
  deliver: (suggestion: PassSuggestion) => Promise<void>,
  options: {
    readonly screeners?: readonly Screener[];
    readonly answers?: JevAnswerStore;
    readonly onPosition?: (position: RecordPosition) => void;
  } = {},
): Promise<PassReport> {
  const failed: ScreenFailure[] = [];
  // Every word, at zero. A map built from the vocabulary would need a cast; written out, the
  // typechecker is what refuses a report that has stopped counting one of the standings.
  const standings: Record<Standing, number> = {
    unjudged: 0,
    unheard: 0,
    unremarked: 0,
    backed: 0,
    objected: 0,
    contested: 0,
  };
  let judged = 0;
  let suggested = 0;
  for (const record of records) {
    const answer = await judge(services, requestFor(record.kind, record.text), options.answers);
    // NOT JUDGED YET, and never judged and found wanting: no part, no binding, no credit, a
    // record too large to send. No voter is consulted, so none of them can be wrong about it —
    // and this is the one standing the pass counts without building a position, because
    // `positionOf(record, null)` would say exactly this for every one of six thousand records.
    if (answer === null) {
      standings.unjudged += 1;
      continue;
    }
    judged += 1;
    const result = screenRecord(record, answersOf(answer), options.screeners);
    options.onPosition?.(result.position);
    standings[result.position.standing] += 1;
    failed.push(...result.failed);
    for (const suggestion of result.suggestions) {
      await deliver(suggestion);
      suggested += 1;
    }
  }
  return {
    read: records.length,
    judged,
    unjudged: records.length - judged,
    suggested,
    failed,
    standings,
  };
}

/**
 * HOW BIG ONE MORE PASS WOULD BE, asked of the door before anything is written — and it is the
 * only call this part makes into Babel, because `suggestions` declares `containers:read` and
 * the write beside it does not. See the head.
 */
export async function sweepSize(actions: GuestActions): Promise<SweepSize> {
  const answered = (await actions.call({
    plugin: BABEL_PLUGIN_ID,
    action: ACTIONS.suggestions,
    input: {},
  })) as SweepSize;
  return {
    outstanding: answered.outstanding,
    judged: answered.judged,
    unjudged: answered.unjudged,
  };
}
