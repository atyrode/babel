import { createHash } from "node:crypto";
import type { GuestActions } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_SWEEP_BATCH,
  RECORD_KINDS,
  RecordPeelSchema,
  RecordKindSchema,
  SuggestionsResultSchema,
  type RecordKind,
  type RecordPeel,
  type SuggestionsResult,
  type SweepPlan,
  type Swept,
  type UnjudgedRecord,
} from "../../contract.ts";
import { BANK, bankFor } from "../bank/bank.ts";
import { screenPass, sweepSize, type PassSuggestion } from "../screen/pass.ts";
import type { ScreenedRecord, Screener } from "../screen/screener.ts";
import {
  JEV_SERVICE,
  withinCallCap,
  type JevAnswerStore,
  type JevServices,
} from "../server/credential.ts";

/*
  THE RETROACTIVE SWEEP (#356): THE CORPUS THAT WAS ALREADY THERE GETS JUDGED TOO.

  6,038 records were imported from the retired product and not one of them has ever been
  screened. A judgement layer that only ever sees the next arrival is a layer whose answer to
  "what should I look at" is "nothing yet, come back after the next run", which is #360's
  RETROACTIVE requirement and the reason this file exists. The study's own run over that corpus
  was 96,608 judgements in 72.9 seconds for $0.538 at 24-way concurrency — and that number is one
  deployment's, measured once, on one operator's imported Go-era output under one recipe and model
  set. It is the reason to look, never a target and never a promise about anybody else's corpus.

  IT WRITES NOTHING, AND IT CANNOT. A pass reads through Babel's reading doors and hands what it
  computed back to whoever asked; the part declares no `containers:write`, makes no `babel.suggest`
  call and holds no store, so there is no path from here to a record, a claim, an edge, a ranking
  or a `next_actions` row. That is not a rule this file remembers — a voter computes and a caller
  delivers, and the caller is the principal that knocked. `test/jev-sweep.test.ts` takes a census
  of every table the migration creates, before and after a pass, and none of them moves.

  BOUNDED, AND THE BOUND IS A STATEMENT ABOUT ONE DISPATCH. {@link JEV_SWEEP_BATCH} records a
  pass, one judgement each, exactly as `BACKFILL_BATCH` bounds one drain tick's embeddings
  (`store/corpus.ts`, `server/drain.ts`'s index duty): a pass holds the dispatch that called it,
  and 6,038 judgements would hold one open for minutes and spend a corpus's budget on a knock
  nobody could take back. The maximum is the default as well: a caller cannot turn the bounded
  duty into a corpus job by filling an optional field.

  RESUMABLE, AND THE PENDING SET IS THE GAP ITSELF. What is left to do is a QUERY — the live,
  unruled records this suggester has no mark on under the bank it ships (`store/acts.ts`'s `gap`,
  reached through the `suggestions` door) — and never a position this part wrote down. A pass that
  died, a hub that restarted and a driver that stopped asking all leave the same state: the rows
  that were written, and the rest still pending. A stored cursor would be a second authority about
  what has been judged, and the failure it causes is the quiet one — a corpus reported as screened
  that is not. `after` is the one thing that looks like a cursor and is not: it is the record the
  last pass ENDED on, answered to the caller and stored by nobody, so a driver can walk the gap in
  order within one authorised sequence of passes. Losing it costs an ordering and never a wrong
  answer, and the rows remain the only thing that says what has been judged.

  A MOVED BANK MEANS THERE IS WORK AGAIN, AND THAT IS THE DECISION THIS FILE IS MOST ABOUT.

  The bank is one document per record kind, each with its own version, and a changed threshold
  moves the document that carries it. Three answers were available:

  1. RE-SCREEN EVERYTHING ON EVERY EDIT. Unaffordable and undirected: an edit to one line of the
     finding document would re-buy judgements of every observation, hypothesis and proposal in the
     corpus, and the operator would learn what it cost afterwards.
  2. NEVER RE-SCREEN. Then the version on a suggestion is a lie — a row says it was judged under
     bank 2 when it was judged under bank 1's wording — and the one thing the bank's versioning is
     for is being able to read a judgement back against the question that produced it.
  3. WHAT THIS DOES. Every suggestion carries its BASIS — the bank version and the version of the
     document that spoke, {@link basisFor} — and the gap is computed against it. So a moved
     document puts the records of ITS kind back in the gap and leaves the other three alone, and
     `judged` stops counting them without forgetting the rows that are there. Nothing re-screens
     by itself: the gap grew, and a pass still reads at most a batch, still only when somebody
     knocks, and {@link sweepPlan} states the size before anything is spent. A re-judgement
     SUPERSEDES the suggestion it replaces rather than adding a second — `babel.suggest`'s
     uniqueness is per (suggester, revision, kind) — so re-screening is replacement work rather
     than duplication, and the operator's queue does not double because a threshold moved.

  It is `pendingVectors`' own argument in the other half of the family: "a row written under a
  model the operator has since changed is pending rather than present. That is the re-embed, and
  it needs no second mechanism." Here the model is the bank.

  WHAT THE GAP CANNOT MARK, said plainly because it decides how a pass is driven. The durable mark
  is a `next_actions` row, and a voter with nothing to say writes none — so a record that was
  screened in silence is still in the gap afterwards. Within one sequence of passes `after` walks
  past it; across a restart the gap offers it again, free while the process's own memo holds the
  answer (`server/judge.ts`) and a repeat call after that. Closing that hole needs a row that says
  "looked, said nothing", which is an `assessments` row with an actor, and no door writes one:
  #360 withheld the third writer class and the mechanism that would admit one narrow write is open
  upstream as atyrode/manifold#770. This file does not invent a private one — a part with a
  bookkeeping table of its own is the store it is defined as not having.

  ABSENT, DISABLED OR DRY IS A NO-OP THAT SAYS SO. Every one of those is one `null` out of
  `judge()` by construction, and a pass that is handed one before it has judged anything stops
  there: nothing screened, nothing spent, one sentence in `stopped`. It is neither an error (a
  deployment that installed no judgement service is a supported deployment) nor a silent success
  (a sweep that reported 24 records read and nothing found would be indistinguishable from a
  corpus Jev liked). A record that comes back unjudged AFTER one was judged is about that record —
  too large to send, one unreadable reply — and the pass carries on, for the same reason
  `screenRecord` isolates a voter that throws: one odd record may not end a sweep over six
  thousand.

  OBSERVATIONS ARE PART OF THE SWEEP WITHOUT BECOMING POSTS. The record door's peel used to carry
  a `FeedPost` at its top and refused an observation by name, because calling evidence a
  hypothesis to fit that shape would have been a lie. The peel now admits an observation's own
  kind while the feed's schema still does not, and `store.record` already knows how to render its
  claim, case and citations. That narrow widening is what makes {@link SWEPT_KINDS} the literal
  four-kind corpus rather than the three kinds that occupy the front page.
*/

/** Every durable record kind: the sweep is over the corpus, not over the feed. */
export const SWEPT_KINDS: readonly RecordKind[] = RECORD_KINDS;

/**
 * WHAT A JUDGEMENT WAS MADE UNDER, as the mark on the row spells it.
 *
 * Both versions, for `server/judge.ts`'s own reason: `bank/versions.json` carries them as
 * independent numbers, a reworded question moves its own document, and a mark keyed on the bank
 * alone would report a record as judged under wording it never saw. It is read from the bundle
 * rather than passed in, so no caller can claim a basis this part does not ship.
 */
export function basisFor(kind: RecordKind, policyRevision: string): string {
  return createHash("sha256")
    .update(JSON.stringify([BANK.version, kind, bankFor(kind).version, policyRevision]))
    .digest("hex");
}

/** What the part needs to run a pass: the judgement service, and the doors it reads through. */
export interface SweepDeps {
  readonly services: JevServices;
  readonly actions: GuestActions;
  /** The roster, overridable so a test can put one voter in front of the pass. */
  readonly screeners?: readonly Screener[];
  /** The memo the judgements are held in; left out, it is the server half's own. */
  readonly answers?: JevAnswerStore;
}

/** How a pass was asked to be bounded, in the door's own words. */
export interface SweepAsk {
  readonly limit: number;
  readonly kinds: readonly RecordKind[];
  readonly after: string;
}

/** One `suggestions` answer, as the reading door gives it. */
async function askSuggestions(
  actions: GuestActions,
  ask: { basis: string; kinds: readonly RecordKind[]; pending: number; after: string },
): Promise<SuggestionsResult> {
  return SuggestionsResultSchema.parse(
    await actions.call({
      plugin: BABEL_PLUGIN_ID,
      action: ACTIONS.suggestions,
      input: { basis: ask.basis, kinds: [...ask.kinds], pending: ask.pending, after: ask.after },
    }),
  );
}

/**
 * WHAT A SWEEP WOULD READ, BEFORE A RECORD IS JUDGED.
 *
 * It is the whole of "sizing before spending": one reading call per kind plus {@link sweepSize}'s,
 * no judgement call at all, and the breakdown is per kind because that is the granularity a moved
 * document has. `silent` carries the one case where there is nothing to say — the part is not
 * allow-listed, or the baseline is not there — because a plan is a question and "nothing, and
 * here is why" is an answer to it.
 */
export async function sweepPlan(
  actions: GuestActions,
  options: {
    readonly limit?: number;
    readonly kinds?: readonly RecordKind[];
    readonly policyRevision: string;
  },
): Promise<SweepPlan> {
  const asked = options.kinds?.length ? options.kinds : SWEPT_KINDS;
  const kinds = SWEPT_KINDS.filter((kind) => asked.includes(kind));
  const limit = options.limit ?? JEV_SWEEP_BATCH;
  const empty: SweepPlan = {
    kinds: [],
    unjudged: 0,
    outstanding: 0,
    unreadable: 0,
    batch: 0,
    silent: "",
  };
  try {
    // The corpus-wide half, and the only call this part made into Babel before this file: what
    // the operator has not answered yet, which is the queue a sweep is about to add to.
    const size = await sweepSize(actions);
    const rows: SweepPlan["kinds"] = await Promise.all(
      kinds.map(async (kind) => {
        const basis = basisFor(kind, options.policyRevision);
        const answer = await askSuggestions(actions, {
          basis,
          kinds: [kind],
          pending: 0,
          after: "",
        });
        return { kind, basis, judged: answer.judged, unjudged: answer.unjudged };
      }),
    );
    const unjudged = rows.reduce((total, row) => total + row.unjudged, 0);
    return {
      kinds: rows,
      unjudged,
      outstanding: size.outstanding,
      unreadable: 0,
      batch: Math.min(limit, unjudged),
      silent: "",
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return { ...empty, silent: `nothing can be swept: ${message}` };
  }
}


/**
 * One record of the gap, as the loop reads it: the door's own id, revision and kind, and the
 * words off the peel. `null` is a record the doors would not serve, which is counted and skipped
 * rather than judged — a judgement of words nobody could read is a judgement of nothing.
 */
async function readRecord(
  actions: GuestActions,
  pending: UnjudgedRecord,
): Promise<ScreenedRecord | null> {
  let peel: RecordPeel;
  try {
    peel = RecordPeelSchema.parse(
      await actions.call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.record,
        input: { id: pending.recordId },
      }),
    );
  } catch {
    return null;
  }
  if (peel.claim.statement === "") return null;
  return {
    id: pending.recordId,
    revision: pending.revision,
    kind: pending.kind,
    title: peel.post.title,
    text: peel.claim.statement,
  };
}

/**
 * ONE BOUNDED PASS OVER THE GAP.
 *
 * The order is the whole of it: read the gap, then read each record, then judge, then screen —
 * and stop the moment the deployment says it cannot judge, before anything has been read that
 * cannot be paid for. Every suggestion comes back to the caller with the basis it was judged
 * under, so delivering one through `babel.suggest` is handing on a row rather than composing one.
 */
export async function sweep(deps: SweepDeps, ask: SweepAsk): Promise<Swept> {
  const asked = ask.kinds.length === 0 ? SWEPT_KINDS : ask.kinds;
  const kinds = SWEPT_KINDS.filter((kind) => asked.includes(kind));
  const nothing: Swept = {
    read: 0,
    judged: 0,
    unjudged: 0,
    positions: [],
    suggestions: [],
    failed: [],
    continuation: "",
    stopped: "",
  };
  if (kinds.length === 0) {
    return { ...nothing, stopped: "no kind this part can read was asked for" };
  }
  const roster = await deps.services.listInstances({}).catch(() => null);
  const policyRevision = roster?.services.find(
    (service) => service.serviceId === JEV_SERVICE.serviceId && service.state === "ready",
  )?.configuration?.revision;
  if (policyRevision === undefined) {
    return { ...nothing, stopped: "the judgement service is not available" };
  }
  // ONE KIND PER PASS, because the basis and continuation are per document. A continuation is
  // `kind/id`: kinds before it are already walked, its own gap resumes after the id, and a later
  // kind begins at its first row. It is carried by the caller only; the rows still decide what is
  // pending and losing it can repeat work without ever skipping work as complete.
  const pending: UnjudgedRecord[] = [];
  const bases = new Map<RecordKind, string>();
  const slash = ask.after.indexOf("/");
  const parsedKind = RecordKindSchema.safeParse(slash < 0 ? "" : ask.after.slice(0, slash));
  const resumeKind = parsedKind.success ? parsedKind.data : null;
  const resumeAt = resumeKind === null ? "" : ask.after.slice(slash + 1);
  let started = resumeKind === null || !kinds.includes(resumeKind);
  for (const kind of kinds) {
    if (!started) {
      if (kind !== resumeKind) continue;
      started = true;
    }
    const basis = basisFor(kind, policyRevision);
    bases.set(kind, basis);
    let answer: SuggestionsResult;
    try {
      answer = await askSuggestions(deps.actions, {
        basis,
        kinds: [kind],
        pending: ask.limit,
        after: kind === resumeKind ? resumeAt : "",
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return { ...nothing, stopped: `the gap could not be read: ${message}` };
    }
    pending.push(...answer.pending);
    if (pending.length > 0) break;
  }
  if (pending.length === 0) {
    return {
      ...nothing,
      stopped: "no pending records remain after this continuation",
    };
  }
  const cursorFor = (row: UnjudgedRecord): string => `${row.kind}/${row.recordId}`;
  const suggestions: Swept["suggestions"] = [];
  const failed: Swept["failed"] = [];
  const positions: Swept["positions"] = [];
  let read = 0;
  let judged = 0;
  let unjudged = 0;
  let continuation = "";
  let stopped = "";
  for (const row of pending) {
    read += 1;
    const record = await readRecord(deps.actions, row);
    if (record === null) {
      // The doors would not serve this record's words. It is accounted for — a pass that stopped
      // here would stop for ever on the same row — and no voter saw it, so it is not judged.
      unjudged += 1;
      continuation = cursorFor(row);
      continue;
    }
    // A record beyond the per-call cap is one record's absence, knowable before the roster is
    // read. Advance past it rather than mistaking it for an absent deployment and stopping the
    // whole pass; this is the transport's own measure and input shape, not a second limit.
    if (!withinCallCap({ [JEV_SERVICE.stateField]: record.text })) {
      unjudged += 1;
      continuation = cursorFor(row);
      continue;
    }
    const collected: PassSuggestion[] = [];
    const report = await screenPass(
      deps.services,
      [record],
      async (suggestion) => {
        if (row.suggestible) collected.push(suggestion);
        await Promise.resolve();
      },
      {
        ...(deps.screeners ? { screeners: deps.screeners } : {}),
        ...(deps.answers ? { answers: deps.answers } : {}),
        onPosition: (position) => positions.push(position),
        expectedRevision: policyRevision,
      },
    );
    failed.push(...report.failed);
    if (report.judged === 0) {
      unjudged += 1;
      // Absence can follow a paid refusal or unreadable answer. Stop rather than spend the
      // rest of the batch; never claim a null answer proves that nothing was charged.
      stopped = `jev did not return a judgement for ${row.recordId}; the sweep stopped`;
      break;
    }
    judged += 1;
    continuation = cursorFor(row);
    const basis = bases.get(row.kind) ?? basisFor(row.kind, policyRevision);
    for (const suggestion of collected) {
      suggestions.push({
        recordId: suggestion.recordId,
        revision: suggestion.revision,
        kind: suggestion.kind,
        summary: suggestion.summary,
        rationale: suggestion.rationale,
        screener: suggestion.screener,
        basis,
      });
    }
  }
  return { read, judged, unjudged, suggestions, positions, failed, continuation, stopped };
}
