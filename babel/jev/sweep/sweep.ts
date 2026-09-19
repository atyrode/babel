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
  jevPolicyRevision,
  withinCallCap,
  type JevAnswerStore,
  type JevServices,
} from "../server/credential.ts";

/*
  A bounded, caller-driven sweep over existing live records, including observations and records
  the operator has ruled on. It reads through baseline doors and returns positions plus eligible
  suggestions; it never writes the frontier or the suggestion queue.

  The pending basis includes the bank, kind document and service policy revisions. Submitted
  suggestions are the durable marks. Silent readings have no such mark: the caller's transient
  continuation walks past them within one run, and losing it can repeat work but cannot certify
  unfinished work. The bounded answer memo avoids another payment only while it retains an answer.

  An unreadable or over-cap record is skipped as unjudged. A missing service answer stops the
  pass, including after earlier success: a refusal does not prove that no money was spent.
  Policy revision checks keep answers from being attributed to a replaced configuration.
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
  const policyRevision = await jevPolicyRevision(deps.services);
  if (policyRevision === null) {
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
