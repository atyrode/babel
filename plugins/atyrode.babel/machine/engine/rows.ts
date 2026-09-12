/*
  THE ROWS a run writes. A machine operation's output files are lists of rows in the store's OWN
  shapes — schema.ts's column names, verbatim — so ingestion is a `batch` and nothing is
  reinterpreted on the way in (plan §4). This module is the one place those shapes are built, so
  `explore` and `evaluate` cannot disagree about what a record or an edge looks like.

  Identifiers are DERIVED, not random. A run's record id is a digest of the run id and the
  worker's own ref for the item, which buys two things the Go product got from a resume ledger: a
  re-delivered output set ingests once, because the hub keys on (table, id) and the second copy is
  the same row; and a resumed attempt at the same run recognizes the record it already wrote
  instead of writing a second copy of it. The prefix keeps `RecordIdSchema`'s vocabulary, so
  nothing downstream has to learn a new id shape.

  Edge kinds are the rewrite's: `cites` (a record to the session it read), `derived_from`,
  `consolidates`, `addresses`, `contradicts`. A record filed under an entity is a `filings` row
  and never an edge — the feed's topic counts and the topics door read `filings`, and a second
  answer beside it could disagree.
*/

import { createHash } from "node:crypto";

/** The record families. The prefix is the id's, so a reader knows the kind before it joins. */
const RECORD_PREFIX: Record<"hypothesis" | "observation" | "finding" | "proposal", string> = {
  hypothesis: "hyp",
  observation: "obs",
  finding: "fnd",
  proposal: "pro",
};

export type RecordKind = keyof typeof RECORD_PREFIX;

/** The edge kinds a run writes. */
export const EDGE = {
  cites: "cites",
  derivedFrom: "derived_from",
  consolidates: "consolidates",
  addresses: "addresses",
  contradicts: "contradicts",
  supersedes: "supersedes",
} as const;

/** §4.2's statuses, as the append-only history records them. */
export const STATUS = {
  untriaged: "untriaged",
  deferred: "deferred",
  rejected: "rejected",
  promoted: "promoted",
  superseded: "superseded",
  retired: "retired",
} as const;

/** A column value as SQLite holds it and as the hub binds it. */
export type Cell = string | number | null;
export type Row = Record<string, Cell>;

/**
 * One derived identifier: `<prefix>_<32 hex>` over the run and the reference the worker used. The
 * digest is truncated to 32 hex characters because that is 128 bits of the namespace and
 * `RecordIdSchema` admits 8–64 — long enough that two refs in one run cannot collide, short
 * enough to read in a URL.
 */
export function mintId(prefix: string, runId: string, ref: string): string {
  const digest = createHash("sha256").update(`${prefix}\u0000${runId}\u0000${ref}`).digest("hex");
  return `${prefix}_${digest.slice(0, 32)}`;
}

/** The id of a record this run emits under `ref`. */
export function mintRecordId(kind: RecordKind, runId: string, ref: string): string {
  return mintId(RECORD_PREFIX[kind], runId, ref);
}

/** The one-line title a surface shows: the first line of the claim, bounded. */
export function titleOf(text: string): string {
  const line = text.trim().split("\n", 1)[0] ?? "";
  return line.length <= 200 ? line : `${line.slice(0, 199)}…`;
}

/** What every row a run writes carries: who wrote it and when. */
export interface Authorship {
  runId: string;
  at: string;
}

/** One record revision. A run's records are always heads: nothing it writes supersedes a row. */
export interface RecordRowInput {
  id: string;
  kind: RecordKind;
  /** An observation's hypothesis (§4.3: an observation hangs off exactly one). */
  parentId?: string;
  recipeId?: string;
  recipeVersion?: number;
  title: string;
  payload: unknown;
}

export function recordRow(input: RecordRowInput, by: Authorship): Row {
  return {
    id: input.id,
    kind: input.kind,
    root_id: input.id,
    supersedes_id: null,
    seq: 0,
    parent_id: input.parentId ?? null,
    run_id: by.runId,
    recipe_id: input.recipeId ?? null,
    recipe_version: input.recipeVersion ?? null,
    actor_kind: "run",
    actor_id: by.runId,
    title: titleOf(input.title),
    created_at: by.at,
    payload: JSON.stringify(input.payload),
  };
}

/** One relation. `to_kind` names the namespace of the target: a record kind, `session`, `entity`. */
export interface EdgeRowInput {
  kind: (typeof EDGE)[keyof typeof EDGE];
  fromKind: string;
  fromId: string;
  toKind: string;
  toId: string;
  position?: number;
  note?: string;
}

export function edgeRow(input: EdgeRowInput, by: Authorship): Row {
  const position = input.position ?? 0;
  return {
    id: mintId("edg", by.runId, `${input.kind}|${input.fromId}|${input.toId}|${position}`),
    kind: input.kind,
    from_kind: input.fromKind,
    from_id: input.fromId,
    to_kind: input.toKind,
    to_id: input.toId,
    position,
    note: input.note ?? null,
    actor_kind: "run",
    actor_id: by.runId,
    created_at: by.at,
  };
}

/**
 * One appended status. `seq` is this run's own count for a record it created; a record it did not
 * create gets 0, which the hub reads as "append after the newest" — a machine cannot know the
 * sequence a row it never read is at, and guessing would collide with the table's own
 * UNIQUE (record_id, seq).
 */
export function statusRow(
  recordId: string,
  seq: number,
  status: (typeof STATUS)[keyof typeof STATUS],
  reason: string,
  by: Authorship,
): Row {
  return {
    id: mintId("ste", by.runId, `${recordId}|${status}|${seq}`),
    record_id: recordId,
    seq,
    status,
    run_id: by.runId,
    actor_kind: "run",
    actor_id: by.runId,
    reason: reason === "" ? null : reason,
    recorded_at: by.at,
  };
}

/**
 * One question a run raised. §4.8's kinds are why it was asked; a run reading a corpus is
 * acquiring context, and the class is blocking exactly when the question holds up a candidate
 * this run emitted — which is the distinction §4.8 ranks on and the one thing the draft states.
 */
export interface QuestionRowInput {
  ref: string;
  subjects: readonly string[];
  predicates: readonly string[];
  /** The record whose development this question blocks, resolved to its durable id. */
  blocks: string;
  prompt: string;
  why: string;
}

export function questionRow(input: QuestionRowInput, by: Authorship): Row {
  const dedupe = createHash("sha256")
    .update([...input.subjects].sort().join(","))
    .update("\u0000")
    .update([...input.predicates].sort().join(","))
    .update("\u0000")
    .update(input.prompt.trim().toLowerCase())
    .digest("hex")
    .slice(0, 32);
  return {
    id: mintId("que", by.runId, input.ref),
    kind: "acquire-context",
    class: input.blocks === "" ? "curiosity" : "blocking",
    text: input.prompt,
    why: input.why,
    dedupe_key: dedupe,
    raised_by_kind: "run",
    raised_by_id: by.runId,
    payload: JSON.stringify({
      subjects: input.subjects,
      predicates: input.predicates,
      blocks: input.blocks,
    }),
    created_at: by.at,
  };
}

/** One run's assessment of one exact revision, in one role. A correction supersedes it. */
export interface AssessmentRowInput {
  recordId: string;
  revisionId: string;
  role: string;
  /** Null for every role but reception, and for a reception review that only contributed. */
  vote: string | null;
  lane: string;
  claimId: string;
  payload: unknown;
}

export function assessmentRow(input: AssessmentRowInput, by: Authorship): Row {
  return {
    id: mintId("asm", by.runId, `${input.revisionId}|${input.role}`),
    record_id: input.recordId,
    revision_id: input.revisionId,
    run_id: by.runId,
    role: input.role,
    vote: input.vote,
    lane: input.lane,
    claim_id: input.claimId,
    supersedes_id: null,
    payload: JSON.stringify(input.payload),
    recorded_at: by.at,
  };
}

/**
 * One filing: a record under an entity, with the rationale and who filed it. `entity_id = ''`
 * with the reason in the rationale is §4.13's "about nothing in particular" — an answer, not an
 * absence, which is why it is a row rather than a missing one.
 */
export function filingRow(recordId: string, entityId: string, rationale: string, by: Authorship): Row {
  return {
    id: mintId("fil", by.runId, `${recordId}|${entityId}`),
    record_id: recordId,
    entity_id: entityId,
    rationale,
    author_kind: "run",
    author_id: by.runId,
    heuristic: 0,
    withdrawn: 0,
    supersedes_id: null,
    created_at: by.at,
  };
}

/**
 * One plan: what accepting a proposal would apply. A topic change and a backlog act are both
 * keyed on the proposal record that carries them, because the operator's act on each is identical
 * — he reads a proposal, and accepting it is what applies it.
 */
export interface PlanRowInput {
  kind: "topic" | "backlog" | "answer";
  /** The proposal record that carries the plan. */
  subjectId: string;
  operation: string;
  dedupeKey?: string;
  payload: unknown;
}

export function planRow(input: PlanRowInput, by: Authorship): Row {
  return {
    id: mintId("pln", by.runId, `${input.kind}|${input.subjectId}|${input.operation}`),
    kind: input.kind,
    subject_kind: "proposal",
    subject_id: input.subjectId,
    operation: input.operation,
    dedupe_key: input.dedupeKey ?? null,
    payload: JSON.stringify(input.payload),
    proposed_by_kind: "run",
    proposed_by_id: by.runId,
    state: "open",
    ruled_by: null,
    ruled_at: null,
    ruling_reason: null,
    result: null,
    created_at: by.at,
  };
}

/**
 * One reply a run records against something the operator said. §4.13 has the operator ask Babel
 * and Babel answer — with a proposal when it agrees, and with a reasoned no when it does not —
 * and a reply threaded under his own entry is where he reads it.
 */
export function steeringReplyRow(rootId: string, replyToId: string, text: string, by: Authorship): Row {
  return {
    id: mintId("str", by.runId, `${replyToId}|${text.length}`),
    root_id: rootId,
    reply_to_id: replyToId,
    seq: 0,
    actor_kind: "run",
    actor_id: by.runId,
    target_kind: null,
    target_id: null,
    text,
    recorded_at: by.at,
  };
}
