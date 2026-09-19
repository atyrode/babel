import { createHash } from "node:crypto";
import { RECORD_RESTS_ON_ONE_RUN } from "../../contract.ts";

/*
  THE SHAPES A SETTLEMENT WRITES, AND THE ONE PLACE AN IDENTIFIER IS MINTED.

  A settled Code session becomes rows in the store's own tables, ingested through the same
  `INGEST` map a machine half's sealed output goes through (`server/conductor.ts`): one file key
  per table, each a list of rows whose keys are that table's own column names. Nothing between
  the answer and the table reinterprets a row, so there is exactly one row shape here and the
  column names are the schema's.

  {@link mintId} is the whole of how a run's answer becomes durable identifiers, and it is a
  DIGEST OF THE RUN AND THE MODEL'S OWN HANDLE rather than a counter. That is what makes a
  settlement idempotent: the second settlement of one run mints the identifiers the first one
  did, every insert is `INSERT OR IGNORE` keyed by that identifier, and a retry after a crash
  between two batches neither duplicates a record nor loses one. A counter would produce a
  second corpus of the same claims on every replay.
*/

/** What SQLite holds in one column of a row the ingest writes. */
export type Cell = string | number | null;
/** One row, keyed by the column names of the table it is bound for. */
export type Row = Record<string, Cell>;

/**
 * The durable identifier for one thing a run claimed, from the run and the handle the run gave
 * it. Stable across attempts, unique across runs, and in `RecordIdSchema`'s shape for the four
 * record families (`contract.ts`: a three-letter family and a hex tail).
 */
export function mintId(prefix: string, runId: string, ref: string): string {
  const digest = createHash("sha256").update(`${prefix}\u0000${runId}\u0000${ref}`).digest("hex");
  return `${prefix}_${digest.slice(0, 32)}`;
}

/** A title as the `records` column holds it: one line, bounded, never silently dropped. */
export function titleCell(title: string): string {
  const line = title.replace(/\s+/gu, " ").trim();
  return line.length <= 200 ? line : `${line.slice(0, 199)}…`;
}

/** The four kinds `records.kind` admits (`store/schema.ts`'s CHECK). */
export type RecordKind = "hypothesis" | "observation" | "finding" | "proposal";

/** What a settlement knows about one record it is writing. */
export interface RecordDraft {
  readonly id: string;
  readonly kind: RecordKind;
  readonly runId: string;
  /** The instant the whole settlement is stamped with. */
  readonly at: string;
  readonly title: string;
  /** The record's own JSON, in the shape its kind declares. */
  readonly payload: Record<string, unknown>;
  /**
   * The records this one rests on: exactly what its `consolidates` and `addresses` edges will
   * point at, and nothing else. It is REQUIRED rather than optional so that a creation path
   * written later cannot produce a record whose corroboration was never determined — a payload
   * key that is absent because nobody thought about it says the same thing as one absent
   * because the answer was unknowable, and then neither can be read.
   */
  readonly supports: readonly string[];
  /**
   * Every record id this same settlement minted. It is how a support's run is known without
   * asking the store: a support this run wrote carries this run's `run_id`.
   */
  readonly ownRecords: ReadonlySet<string>;
  readonly parentId?: string | null;
  /**
   * ONLY AN OBSERVATION CARRIES A RECIPE, which is what the answer attributes and what the
   * crossing wrote (`tools/import.ts`: the recipe columns are an observation's alone). A
   * hypothesis, a finding and a proposal are reached THROUGH a method and are not products of
   * one, and naming the run's first recipe on them would be an attribution nobody made. A
   * review's proposal is the exception the review lane already made: it is the product of the
   * recipe the reviewer was given.
   */
  readonly recipe?: { readonly id: string; readonly version: number } | null;
}

/**
 * WHETHER A RECORD RESTS ON ONE RUN, decided from what the settlement can see by itself, or
 * `null` when it cannot be decided here at all.
 *
 * THE DEFINITION IS `store/store.ts`'s `corroborationOf` AND MUST STAY IT: the distinct
 * `records.run_id` of what this record's `consolidates` and `addresses` edges point AT, resting
 * on one run when that count is one. Two definitions of resting on one run that disagreed would
 * be worse than none, so where this half cannot reproduce that count it declines to answer
 * instead of guessing, and the store's own query remains the live authority.
 *
 * What it can see: a support this settlement minted carries this run's `run_id`, and a support
 * named by a durable identifier was written by an earlier one. So this run's own supports alone
 * are one run; this run's own beside an earlier record's are two; a single earlier record is one
 * run whoever wrote it. The one case left is several earlier records and none of this run's —
 * whether those share a run is a question only the store holds, and it is answered `null`.
 *
 * IT NEVER REFUSES, AND MAY NEVER BE MADE TO. 175 of the 207 findings in this deployment's
 * corpus rest on a single run and every one of its 116 proposals shares its finding's run, so a
 * rule that rejected a single-run finding would reject most of a corpus that predates the rule.
 * A finding resting on one run has weak independence; it is not invalid. Mark it, never refuse
 * it — the next reader of this function will be tempted to make it a validation.
 */
function restsOnOneRun(
  supports: readonly string[],
  ownRecords: ReadonlySet<string>,
): boolean | null {
  // One relation is one edge however many times the answer named it, so the count that has to
  // match `COUNT(*)` over `edges` is over distinct supports.
  const distinct = new Set(supports);
  if (distinct.size === 0) return null;
  let earlier = 0;
  for (const id of distinct) if (!ownRecords.has(id)) earlier += 1;
  if (earlier === 0) return true;
  if (earlier < distinct.size) return false;
  return distinct.size === 1 ? true : null;
}

/**
 * One `records` row, in the columns the table spells them, with the corroboration
 * determination folded into the payload ({@link restsOnOneRun}).
 *
 * Every creation path goes through here — an accepted exploration's claims (`records.ts`) and a
 * drawn review's proposals (`review.ts`) — because a record written one way and a record
 * written the other have to mean the same thing by the same key.
 *
 * THE VALUE IS AS OF CREATION AND CANNOT GO STALE. A record's own supporting edges are written
 * in the settlement that creates it and nowhere else: the other writers of those kinds are the
 * one-time crossing (`tools/import.ts`) and a review's own new proposal, whose edge points away
 * from itself, while the operator writes only `supersedes` (`store/acts.ts`). Nothing later adds
 * a support to a record that already exists, because a record is immutable by trigger and a
 * correction is a supersession — a new row, with its own determination.
 */
export function recordRow(draft: RecordDraft): Row {
  const rests = restsOnOneRun(draft.supports, draft.ownRecords);
  return {
    id: draft.id,
    kind: draft.kind,
    // A NEW RECORD IS ITS OWN ROOT AT SEQUENCE ZERO. A correction is a supersession — the table
    // refuses an update by trigger — so nothing a run writes ever revises a row.
    root_id: draft.id,
    supersedes_id: null,
    seq: 0,
    parent_id: draft.parentId ?? null,
    run_id: draft.runId,
    recipe_id: draft.recipe?.id ?? null,
    recipe_version: draft.recipe?.version ?? null,
    actor_kind: "run",
    actor_id: draft.runId,
    title: titleCell(draft.title),
    created_at: draft.at,
    payload: JSON.stringify(
      rests === null ? draft.payload : { ...draft.payload, [RECORD_RESTS_ON_ONE_RUN]: rests },
    ),
  };
}
