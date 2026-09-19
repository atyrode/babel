import { z } from "zod";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import type { INTEREST_STATES } from "../contract.ts";
import {
  NextActionStandingSchema,
  RECORD_KINDS,
  RecordIdSchema,
  RecordKindSchema,
  REFINEMENT_KEY,
  RefinementOutcomeSchema,
  RefinementSchema,
  RoleSchema,
  SuggesterSchema,
  type NextAction,
  type NextActionDecision,
  type Refinement,
  type Ruling,
  type Suggested,
  type Suggester,
  type SuggestionsQuery,
  type SuggestionsResult,
  type UnjudgedRecord,
} from "../contract.ts";
import {
  acceptReviewResult,
  REFUSALS,
  ResultRefusal,
  type RefusalCode,
} from "../machine/results.ts";
import { nameableRecordSql, SCHEMA_V1 } from "./schema.ts";
import {
  budgetChanges,
  coordinator,
  PolicySchema,
  validateBudget,
  validateNewPolicy,
  type Budget,
  type Policy,
} from "./coordinator.ts";
export {
  DEFAULT_POLICY,
  leaseFloor,
  perMachineBound,
  PolicySchema,
  validateNewPolicy,
  type Policy,
} from "./coordinator.ts";

/*
  THE OPERATOR'S ACTS, as writes. Everything in this file appends: a ruling, a comment, an
  answer, a stance, a filing, a word to Babel, a policy. Nothing edits and nothing deletes —
  the store's triggers refuse both — so "he changed his mind" is a later row that supersedes an
  earlier one and says why, which is the whole of SPEC §4.7's append-only review and §4.13's
  readable filing history.

  Three things are load-bearing and are stated here rather than assumed.

  A ruling is one act even when it moves the ledger. Accepting a proposal that carries a topic
  plan creates an entity, binds it with facts and files records; accepting one that carries a
  backlog plan settles candidates. The Go tree had to split that across two transactions because
  the frontier and the Reality Ledger were two handles on one SQLite file and its write lock is
  per file (v0.4.0:internal/reality/store_topic.go's ApplyTopicPlan says so at length). Here the plugin
  database is one handle and `batch` is one immediate transaction, so the ruling and what it
  applies commit together or not at all. What survives from the Go is the *refusal* order: a plan
  the ledger has moved past, or a rejection with no reason, is refused before anything is
  appended, and a plan whose application is refused leaves the ruling standing with the reason
  reported — §4.7 does not un-append a ruling.

  An identifier is minted here, not read off a clock. Every id is `<family>_<16 hex>`, which is
  the shape the frontier minted and the shape `contract.ts`'s `EntityIdSchema` enforces, so an
  entity this file creates is addressable by the same regex the importer's rows satisfy.

  Every instant is stored as the Go stored it: ISO-8601 UTC with nine fractional digits, fixed
  width, so text order is time order and a row written today compares byte-for-byte against the
  sixty-five thousand the importer brought across.
*/

// ---------------------------------------------------------------------------- the store handle

/**
 * What an act needs of the store: the database, the clock and the index invalidation. It is
 * stated as its own shape rather than as the whole `BabelStore` because a write does not read
 * the feed, and because the derivations below (`standingOf`) are what the read side imports
 * from here — a module that also imported the read side would be a cycle.
 */
export interface ActsStore {
  readonly db: PluginDatabase;
  /** Milliseconds since the epoch, from the store's injected clock. */
  now(): number;
  /** Drops the cached feed index; every act that appends a row calls it once, at the end. */
  touch(): void;
}

/**
 * A refused act: the caller asked for something the store will not do — an unknown record, a
 * ruling that says nothing new, a policy that cannot be honoured. It is separate from a failure
 * because a door answers the two differently: a refusal is `{ refused }` and the dispatch is
 * denied by rule, while a failure raises and is logged as one.
 */
export class ActRefused extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ActRefused";
  }
}

// ---------------------------------------------------------------------------- what a job wrote

/** Why the store will not write a row a machine half produced. */
export interface RowRefusal {
  /** The producer's own refusal vocabulary (`schema`, `support`, `empty`, …). */
  readonly code: RefusalCode;
  readonly message: string;
}

/**
 * The store's acceptance of one row a finished job wrote, or null when it accepts it.
 *
 * An `assessments` row carries a review submission, and the store validates it with THE SAME
 * validator the engine's per-role JSON Schema is generated from (`machine/results.ts`).
 * That identity is the point: the Go tree stated the environment/outcome/criterion-results rule
 * three times — in the review contract, in the store's acceptance and in what it counted as
 * empty — and on 2026-09-13 an evidence review was paid for and then refused at submit because
 * the three disagreed (docs/postmortem-2026-09-13-drain.md, F8). A payload the model's schema
 * admits is a payload this accepts, under the same code, because it is one function.
 *
 * A refusal here is the row's, not the run's: the claim is settled from the receipt the machine
 * wrote, which carries the closure and the cost whether the submission stood or not.
 */
export function refuseRow(
  table: string,
  row: Readonly<Record<string, unknown>>,
): RowRefusal | null {
  if (table !== "assessments") return null;
  const role = RoleSchema.safeParse(row["role"]);
  if (!role.success) {
    return {
      code: REFUSALS.schema,
      message: `${JSON.stringify(row["role"])} is not a review role this build knows`,
    };
  }
  const payload = row["payload"];
  let decoded: unknown;
  try {
    decoded = typeof payload === "string" ? JSON.parse(payload) : payload;
  } catch {
    return { code: REFUSALS.schema, message: "the assessment's payload is not JSON" };
  }
  try {
    acceptReviewResult(role.data, decoded);
    return null;
  } catch (error) {
    if (error instanceof ResultRefusal) return { code: error.refusal, message: error.message };
    throw error;
  }
}

// ---------------------------------------------------------------------------- ids and instants

/** `<family>_<16 hex>`: the identifier shape every table in this store holds. */
export function newId(prefix: string): string {
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  let tail = "";
  for (const byte of bytes) tail += byte.toString(16).padStart(2, "0");
  return `${prefix}_${tail}`;
}

/**
 * One instant as the store holds it: `2026-09-12T17:43:05.123000000Z`. The nine fractional
 * digits are the Go tree's `timestampLayout`, kept so that a `WHERE expires_at > ?` or a day
 * boundary compares as text against both new and imported rows.
 */
export function stamp(millis: number): string {
  return `${new Date(millis).toISOString().slice(0, -1)}000000Z`;
}

// ---------------------------------------------------------------------------- rulings

/** §4.5's review status, derived from the rulings and never stored. */
export type Standing =
  "new" | "accepted" | "rejected" | "deferred" | "duplicate" | "refine-requested";

/**
 * What each ruling makes of a record. `reopen` deriving `new` is the point of the value: the
 * ruling it lifts keeps its row and its place in the history while the record goes back to
 * undecided. `refine` derives `refine-requested` on its own, which is the one thing the rewrite
 * collapses — the Go needed a rejection plus a refinement-request row to mean it, and a ruling
 * that can only be spelled with two rows is a ruling the vocabulary was missing.
 */
export const STANDING_OF: Record<Ruling, Standing> = {
  accept: "accepted",
  reject: "rejected",
  defer: "deferred",
  duplicate: "duplicate",
  reopen: "new",
  refine: "refine-requested",
};

/** The standing a record with this newest ruling has; nothing ruled, or unknown, is `new`. */
export function standingOf(ruling: string | null): Standing {
  const standing = STANDING_OF[ruling as Ruling] as Standing | undefined;
  return standing ?? "new";
}

/**
 * The two standings that refuse any further ruling, with the reason each refuses. Both say the
 * decision belongs to another record: a duplicate is decided at the original, and a refinement
 * request is decided at the descendant it authorized. Appending here would put two answers where
 * the vocabulary allows one.
 */
const TERMINAL_STANDING: Record<string, string> = {
  duplicate: "it is decided at the record it duplicates",
  "refine-requested": "it is decided at the descendant the rejection authorized",
};

/**
 * Why this ruling may not be appended to a record standing that way, or null when it may.
 * Repeating the standing ruling is refused because the history is the audit record of how the
 * operator's position moved, and an event that moved nothing makes it read as though he
 * reconsidered when he did not.
 */
function rulingRefusal(standing: Standing, ruling: Ruling): string | null {
  const terminal = TERMINAL_STANDING[standing];
  if (terminal !== undefined) return `this record is ${standing}: ${terminal}`;
  if (ruling === "reopen") {
    return standing === "new" ? "nothing has been decided here to reopen" : null;
  }
  return STANDING_OF[ruling] === standing ? `this record is already ${standing}` : null;
}

// ---------------------------------------------------------------------------- interest

/**
 * A stance as the ledger records it (§4.13, ported from v0.4.0:internal/reality/interest.go): not a
 * preference column but §4.8's lifecycle and analysis-policy facts, so a paused project is
 * paused everywhere Babel looks. An empty lifecycle means "leave it alone", which is what
 * `excluded` does — a repository can be withheld from analysis and still be actively worked on,
 * and writing a lifecycle value would make the exclusion claim something it does not know.
 */
export type InterestState = (typeof INTEREST_STATES)[number];

export const INTEREST_FACTS: Record<
  InterestState,
  { readonly lifecycle: string; readonly policy: string }
> = {
  working: { lifecycle: "active", policy: "normal" },
  watching: { lifecycle: "maintenance-only", policy: "learn-only" },
  "not-now": { lifecycle: "dormant", policy: "learn-only" },
  excluded: { lifecycle: "", policy: "excluded" },
};

export const PREDICATE_LIFECYCLE = "lifecycle";
export const PREDICATE_ANALYSIS_POLICY = "analysis-policy";
export const LIFECYCLE_RETIRED = "retired";

// ---------------------------------------------------------------------------- filing

/**
 * The topic word that means "about nothing in particular" — the same one `FeedQuerySchema`
 * reserves for the records under nothing. Filing under it is §4.13's other honest answer and is
 * stored as a filing with an empty `entity_id`, because "somebody considered this and answered"
 * and "nobody has looked" are different states and the triage lane has to tell them apart.
 */
export const NO_TOPIC = "unfiled";

/** Folds the incidental differences between two spellings of one name: case and surrounding space. */
function aliasKey(value: string): string {
  return value.trim().toLowerCase();
}

// ---------------------------------------------------------------------------- the policy

/**
 * What the `setPolicy` door takes. The reason travels with the policy because a bound an
 * operator changed without saying why is a number nobody can argue with later. The policy's
 * shape, its defaults and its refusals are the coordinator's: one definition, read and written
 * through the same schema.
 */
export const SetPolicyInputSchema = z.strictObject({
  policy: PolicySchema,
  reason: z.string().max(2000).default(""),
});

// ---------------------------------------------------------------------------- plans

const AliasDraftSchema = z.strictObject({
  kind: z.string().min(1).max(40),
  value: z.string().min(1).max(400),
});

const FactDraftSchema = z.object({
  predicate: z.string().min(1).max(60),
  value: z.string().min(1).max(400),
  note: z.string().max(2000).optional(),
});

/**
 * What a topic proposal would do to the ledger, as the plan row's payload carries it. The row's
 * own columns hold the operation, the subject it hangs off and the ruling; everything an
 * application needs to perform the act is here, so applying a plan reads one row and nothing
 * else the proposer knew is lost.
 */
export const TopicPlanPayloadSchema = z.object({
  reasoning: z.string().default(""),
  /** The identity a create or a split binds — a normalized remote, a common directory, a slug. */
  identity: z.string().optional(),
  name: z.string().optional(),
  entityKind: z.string().optional(),
  aliases: z.array(AliasDraftSchema).default([]),
  binding: z.array(FactDraftSchema).default([]),
  /** A merge's `[source, target]`; a split's and a retirement's `[subject]`. */
  targets: z.array(z.string()).default([]),
  records: z.array(z.object({ id: z.string(), rationale: z.string().optional() })).default([]),
  considered: z.array(z.object({ id: z.string(), name: z.string(), why: z.string() })).default([]),
  sessions: z.number().int().optional(),
});
export type TopicPlanPayload = z.infer<typeof TopicPlanPayloadSchema>;

/** What a backlog proposal would do: §4.13's consolidate, supersede, retire and promote. */
export const BacklogPlanPayloadSchema = z.object({
  reasoning: z.string().default(""),
  /** The deferred candidates the act settles. */
  hypotheses: z.array(z.string()).default([]),
  /** The newer candidate that says it better; a supersession's and nothing else's. */
  supersededBy: z.string().optional(),
  /** The finding a consolidation folds them into; the run minted it before the ruling. */
  finding: z.string().optional(),
  observation: z.string().optional(),
  fact: z
    .object({
      entityId: z.string(),
      predicate: z.string().min(1).max(60),
      value: z.string().min(1).max(400),
      note: z.string().max(2000).optional(),
    })
    .optional(),
  evidence: z.number().int().optional(),
});
export type BacklogPlanPayload = z.infer<typeof BacklogPlanPayloadSchema>;

/** What a ruling did to the plan the record carried. */
export interface PlanOutcome {
  kind: "topic" | "backlog";
  operation: string;
  applied: boolean;
  declined: boolean;
  entityId?: string;
  error?: string;
}

/** What a ruling did to the refinement the record carried (§4.7). */
export interface RefinementOutcome {
  targetRecordId: string;
  targetPath: string;
  revisionId: string;
  applied: boolean;
  error?: string;
}

// ---------------------------------------------------------------------------- results

export const RuledSchema = z.strictObject({
  id: z.string(),
  standing: z.string(),
  seq: z.number().int(),
  plan: z
    .strictObject({
      kind: z.enum(["topic", "backlog"]),
      operation: z.string(),
      applied: z.boolean(),
      declined: z.boolean(),
      entityId: z.string().optional(),
      error: z.string().optional(),
    })
    .nullable(),
  refinement: RefinementOutcomeSchema.nullable(),
});
export type Ruled = z.infer<typeof RuledSchema>;

export const DecidedSchema = z.strictObject({
  id: z.string(),
  recordId: z.string(),
  standing: NextActionStandingSchema,
  seq: z.number().int(),
  at: z.string(),
});
export type Decided = z.infer<typeof DecidedSchema>;

export const CommentedSchema = z.strictObject({
  id: z.string(),
  recordId: z.string(),
  question: z.boolean(),
  at: z.string(),
});
export type Commented = z.infer<typeof CommentedSchema>;

export const AnsweredSchema = z.strictObject({
  id: z.string(),
  questionId: z.string(),
  state: z.string(),
  at: z.string(),
});
export type Answered = z.infer<typeof AnsweredSchema>;

export const InterestedSchema = z.strictObject({
  entityId: z.string(),
  state: z.string(),
  facts: z.array(z.string()),
  at: z.string(),
});
export type Interested = z.infer<typeof InterestedSchema>;

export const FiledSchema = z.strictObject({
  id: z.string(),
  recordId: z.string(),
  entityId: z.string(),
  withdrawn: z.boolean(),
  supersedes: z.string(),
  at: z.string(),
});
export type Filed = z.infer<typeof FiledSchema>;

export const ToldSchema = z.strictObject({
  id: z.string(),
  rootId: z.string(),
  seq: z.number().int(),
  at: z.string(),
});
export type Told = z.infer<typeof ToldSchema>;

export const PolicySetSchema = z.strictObject({
  version: z.string(),
  seq: z.number().int(),
  at: z.string(),
});
export type PolicySet = z.infer<typeof PolicySetSchema>;

/**
 * WHAT AN OVERLAY IS SET WITH (#260). `expiresAt` is an ISO-8601 instant and is required: an
 * overlay with no end is an edit of the standing policy wearing another name, and the whole
 * point of the row is that nobody has to remember to unwind it. Every number is optional and
 * an overlay carries only what it moves; the reason is required, because "why is the batch
 * sixty-four today" is the question nobody could answer on 2026-09-13.
 *
 * There is no `batchSize`. Admission bounds a MACHINE (`perMachineBound`), so an overlay that
 * named the batch beside a standing per-machine bound moved a number nothing reads: the
 * operator would raise it for a drain, the panel would report the change, and no further draw
 * would be admitted. `concurrentPerMachine` is the one knob, and the batch follows it.
 */
export const SetBudgetInputSchema = z.strictObject({
  expiresAt: z.string().min(1).max(64),
  perCycleCost: z.number().positive().max(1_000_000).optional(),
  dailyCost: z.number().positive().max(1_000_000).optional(),
  concurrentPerMachine: z.number().int().min(1).max(4096).optional(),
  reason: z.string().trim().min(1).max(2000),
});

/** Ending an overlay early is an act like setting one, so it carries its own reason and the
 *  row keeps it: `cleared_reason` beside `cleared_at` says why a drain stopped. */
export const ClearBudgetInputSchema = z.strictObject({
  id: z.string().min(1).max(200),
  reason: z.string().max(2000).default(""),
});

export const BudgetSetSchema = z.strictObject({
  id: z.string(),
  expiresAt: z.string(),
  at: z.string(),
  /** What it moves, so the answer says the change rather than the row. */
  changes: z.array(
    z.strictObject({ field: z.string(), standing: z.number(), overlaid: z.number() }),
  ),
});
export type BudgetSet = z.infer<typeof BudgetSetSchema>;

export const BudgetClearedSchema = z.strictObject({
  id: z.string(),
  at: z.string(),
});
export type BudgetCleared = z.infer<typeof BudgetClearedSchema>;

export const ImportedSchema = z.strictObject({
  source: z.string(),
  table: z.string(),
  inserted: z.number().int(),
  skipped: z.number().int(),
});
export type Imported = z.infer<typeof ImportedSchema>;

export const SessionsRehostedSchema = z.strictObject({
  from: z.string(),
  to: z.string(),
  /** Catalogued sessions now reachable: the rows this act moved. */
  sessions: z.number().int().nonnegative(),
});
export type SessionsRehosted = z.infer<typeof SessionsRehostedSchema>;

// ---------------------------------------------------------------------------- reads the acts need

async function first<Row extends SqlRow>(
  store: ActsStore,
  sql: string,
  params: readonly SqlParam[],
): Promise<Row | null> {
  const rows = await store.db.query<Row>(sql, params);
  return rows[0] ?? null;
}

/** The kind of a record this store holds, refusing one it does not. */
async function recordKind(store: ActsStore, id: string): Promise<string> {
  const row = await first<{ kind: string }>(store, `SELECT kind FROM records WHERE id = ?`, [id]);
  if (row === null) throw new ActRefused(`no record ${id}`);
  return row.kind;
}

/**
 * The entity an act names, resolved through the merge history so a stance cannot be recorded
 * against a name a merge has folded away. A reference is either an identifier the ledger minted
 * or a spelling one of its aliases holds.
 */
async function resolveEntity(store: ActsStore, reference: string): Promise<string> {
  const byId = await first<{ canonical_id: string }>(
    store,
    `SELECT canonical_id FROM entities WHERE id = ?`,
    [reference],
  );
  if (byId !== null) return byId.canonical_id;
  const byAlias = await first<{ canonical_id: string }>(
    store,
    `SELECT e.canonical_id FROM aliases a JOIN entities e ON e.id = a.entity_id
     WHERE a.value_key = ? AND a.retired_at IS NULL ORDER BY a.created_at LIMIT 1`,
    [aliasKey(reference)],
  );
  if (byAlias === null) throw new ActRefused(`no topic ${JSON.stringify(reference)} in the ledger`);
  return byAlias.canonical_id;
}

/** Whether the lifecycle fact in force retires this subject. */
async function entityRetired(store: ActsStore, entityId: string): Promise<boolean> {
  const fact = await factInForce(store, entityId, PREDICATE_LIFECYCLE, stamp(store.now()));
  return fact?.value === LIFECYCLE_RETIRED;
}

interface FactRow extends SqlRow {
  id: string;
  value: string;
}

/**
 * The revision of one predicate that currently holds: not proposed, not superseded, its valid
 * time covering now, newest observation first. It is derived rather than flagged for §4.8's
 * reason — a stored "current" bit would need an UPDATE this store has none of, and a bit that
 * can disagree with the rows makes the history unreadable exactly when somebody needs it.
 */
async function factInForce(
  store: ActsStore,
  entityId: string,
  predicate: string,
  at: string,
): Promise<FactRow | null> {
  return await first<FactRow>(
    store,
    `SELECT f.id, f.value FROM facts f
     WHERE f.entity_id = ? AND f.predicate = ?
       AND COALESCE((SELECT s.status FROM fact_status s WHERE s.fact_id = f.id ORDER BY s.seq DESC LIMIT 1),
                    'active') NOT IN ('proposed', 'superseded')
       AND (f.valid_until IS NULL OR f.valid_until > ?)
     ORDER BY f.observed_at DESC, f.recorded_at DESC, f.rowid DESC LIMIT 1`,
    [entityId, predicate, at],
  );
}

/**
 * One filing row. `heuristic` and `withdrawn` are INTEGER columns and the engine opens this
 * plugin's file with `safeIntegers`, so they answer as BIGINTs: `withdrawn === 1` is false for a
 * withdrawn row, which is how a second withdrawal stopped being refused. They are carried in the
 * shape the database hands them over and read through `Number()` at the two places that ask.
 */
interface FilingRow extends SqlRow {
  id: string;
  rationale: string;
  heuristic: number | bigint;
  withdrawn: number | bigint;
}

/**
 * The row that currently answers for one record and one topic, filing or withdrawal alike.
 *
 * The tie-break is `rowid` and not the identifier: two filings written in the same millisecond
 * are ordered by the order they were inserted, which is what "the newest" means, whereas a
 * random identifier would make re-filing a withdrawal a coin flip. Time still leads, so an
 * imported history reads in its own order however it arrived.
 */
async function newestFiling(
  store: ActsStore,
  recordId: string,
  entityId: string,
): Promise<FilingRow | null> {
  return await first<FilingRow>(
    store,
    `SELECT id, rationale, heuristic, withdrawn FROM filings
     WHERE record_id = ? AND entity_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
    [recordId, entityId],
  );
}

/** The newest status a candidate took, or `untriaged` for one nothing has moved. */
async function recordStatus(store: ActsStore, recordId: string): Promise<string> {
  const row = await first<{ status: string }>(
    store,
    `SELECT status FROM status_events WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
    [recordId],
  );
  return row?.status ?? "untriaged";
}

// ---------------------------------------------------------------------------- statement builders

/**
 * A fact that states one operator-intent enum: a supersession when the ledger holds a revision
 * in force, an assertion when it does not. Restating the same value is a supersession too — the
 * operator said it again, possibly for a different reason, and an append-only ledger records
 * that rather than deciding it was redundant.
 */
async function stateFactStatements(
  store: ActsStore,
  entityId: string,
  predicate: string,
  value: string,
  operator: string,
  note: string,
  at: string,
  confidence = "stated",
): Promise<{ statements: SqlStatement[]; factId: string }> {
  const prior = await factInForce(store, entityId, predicate, at);
  const factId = newId("fct");
  const statements: SqlStatement[] = [
    {
      sql: `INSERT INTO facts(id, entity_id, predicate, value, object_id, valid_from, valid_until,
              observed_at, authority_kind, authority_id, confidence, note, supersedes_id, recorded_at)
            VALUES(?, ?, ?, ?, NULL, ?, NULL, ?, 'operator', ?, ?, ?, ?, ?)`,
      params: [
        factId,
        entityId,
        predicate,
        value,
        at,
        at,
        operator,
        confidence,
        note,
        prior?.id ?? null,
        at,
      ],
    },
    {
      sql: `INSERT INTO fact_status(id, fact_id, seq, status, actor_id, reason, recorded_at)
            VALUES(?, ?, 1, 'active', ?, NULL, ?)`,
      params: [newId("fst"), factId, operator, at],
    },
  ];
  if (prior !== null) {
    statements.push({
      sql: `INSERT INTO fact_status(id, fact_id, seq, status, actor_id, reason, recorded_at)
            SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, 'superseded', ?, 'superseded by a later revision', ?
            FROM fact_status WHERE fact_id = ?`,
      params: [newId("fst"), prior.id, operator, at, prior.id],
    });
  }
  return { statements, factId };
}

/** One status event on a candidate, sequenced against the candidate's own history. */
function statusEventStatement(
  recordId: string,
  status: string,
  operator: string,
  reason: string,
  at: string,
): SqlStatement {
  return {
    sql: `INSERT INTO status_events(id, record_id, seq, status, run_id, actor_kind, actor_id, reason, recorded_at)
          SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, NULL, 'operator', ?, ?, ?
          FROM status_events WHERE record_id = ?`,
    params: [newId("sev"), recordId, status, operator, reason, at, recordId],
  };
}

function filingStatement(args: {
  id: string;
  recordId: string;
  entityId: string;
  rationale: string;
  authorKind: string;
  authorId: string;
  heuristic: boolean;
  withdrawn: boolean;
  supersedes: string | null;
  at: string;
}): SqlStatement {
  return {
    sql: `INSERT INTO filings(id, record_id, entity_id, rationale, author_kind, author_id, heuristic,
            withdrawn, supersedes_id, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    params: [
      args.id,
      args.recordId,
      args.entityId,
      args.rationale,
      args.authorKind,
      args.authorId,
      args.heuristic ? 1 : 0,
      args.withdrawn ? 1 : 0,
      args.supersedes,
      args.at,
    ],
  };
}

function resolutionStatements(
  kind: "merge" | "split",
  operator: string,
  reason: string,
  at: string,
  members: readonly (readonly [string, number, string])[],
): { statements: SqlStatement[]; resolutionId: string } {
  const resolutionId = newId("res");
  const statements: SqlStatement[] = [
    {
      sql: `INSERT INTO resolutions(id, kind, reverses_id, actor_id, reason, recorded_at)
            VALUES(?, ?, NULL, ?, ?, ?)`,
      params: [resolutionId, kind, operator, reason, at],
    },
  ];
  for (const [role, position, entityId] of members) {
    statements.push({
      sql: `INSERT INTO resolution_members(resolution_id, role, position, entity_id) VALUES(?, ?, ?, ?)`,
      params: [resolutionId, role, position, entityId],
    });
  }
  return { statements, resolutionId };
}

// ---------------------------------------------------------------------------- the plan application

interface PlanRow extends SqlRow {
  id: string;
  kind: string;
  subject_id: string;
  operation: string;
  payload: string;
  proposed_by_kind: string;
  proposed_by_id: string;
  state: string;
}

/** The open topic or backlog plan a proposal carries; answer plans are ruled on their question. */
async function openPlans(store: ActsStore, recordId: string): Promise<readonly PlanRow[]> {
  return await store.db.query<PlanRow>(
    `SELECT id, kind, subject_id, operation, payload, proposed_by_kind, proposed_by_id, state
     FROM plans WHERE subject_id = ? AND kind IN ('topic', 'backlog') AND state = 'open'
     ORDER BY created_at, id`,
    [recordId],
  );
}

/**
 * The plan a ruling answers, refusing the proposal that carries two. One ruling cannot apply two
 * different changes to the ledger, and a run that wanted both should have published two
 * proposals — so this is Babel's mistake stated as such rather than a half-applied acceptance.
 */
async function planFor(store: ActsStore, recordId: string): Promise<PlanRow | null> {
  const plans = await openPlans(store, recordId);
  const head = plans[0];
  if (head !== undefined && plans.some((plan) => plan.kind !== head.kind)) {
    throw new ActRefused(
      "this proposal carries both a topic plan and a backlog plan; one ruling cannot apply two " +
        "different changes, and Babel should have published them as two proposals",
    );
  }
  return head ?? null;
}

function planRuledStatement(
  planId: string,
  state: string,
  operator: string,
  reason: string,
  at: string,
  result: unknown,
): SqlStatement {
  return {
    sql: `UPDATE plans SET state = ?, ruled_by = ?, ruled_at = ?, ruling_reason = ?, result = ?
          WHERE id = ? AND state = 'open'`,
    params: [state, operator, at, reason, JSON.stringify(result), planId],
  };
}

/**
 * What an application produced: the plan row keeps it as its `result`, so "what did accepting
 * this actually do" is answerable from the plan alone rather than by re-deriving it from the
 * rows the acceptance happened to write.
 */
interface Application {
  entityId: string;
  resolutionId: string;
  factId: string;
  filed: string[];
  settled: string[];
}

/**
 * Everything applying this plan writes, with what it produced. The applicability checks run
 * first and refuse: a plan is Babel's reading of the ledger at the moment it ran, the operator
 * may rule on it days later, and between the two the topic it names can have been merged into
 * another or retired. Refusing names the state, which is what tells him the answer is to let
 * Babel look again.
 */
async function applyStatements(
  store: ActsStore,
  plan: PlanRow,
  operator: string,
  reason: string,
  at: string,
): Promise<{ statements: SqlStatement[]; outcome: PlanOutcome }> {
  const applied: Application = {
    entityId: "",
    resolutionId: "",
    factId: "",
    filed: [],
    settled: [],
  };
  const statements: SqlStatement[] =
    plan.kind === "topic"
      ? await topicStatements(store, plan, operator, at, applied)
      : await backlogStatements(store, plan, operator, at, applied);
  statements.push(planRuledStatement(plan.id, "applied", operator, reason, at, applied));
  const outcome: PlanOutcome = {
    kind: plan.kind === "backlog" ? "backlog" : "topic",
    operation: plan.operation,
    applied: true,
    declined: false,
  };
  if (applied.entityId !== "") outcome.entityId = applied.entityId;
  return { statements, outcome };
}

/**
 * §4.13's four acts on a topic, which are one output kind rather than four features: a new
 * topic, a split, a merge and a retirement are all "Babel says the naming is wrong and here is
 * what it should be", and the operator's ruling on the proposal is what applies whichever it is.
 */
const TOPIC_OPERATIONS: Record<string, true> = {
  create: true,
  split: true,
  merge: true,
  retire: true,
};

async function topicStatements(
  store: ActsStore,
  plan: PlanRow,
  operator: string,
  at: string,
  applied: Application,
): Promise<SqlStatement[]> {
  const payload = TopicPlanPayloadSchema.parse(JSON.parse(plan.payload));
  if (!TOPIC_OPERATIONS[plan.operation]) {
    throw new ActRefused(`topic operation ${JSON.stringify(plan.operation)}`);
  }
  const statements: SqlStatement[] = [];
  const targets: string[] = [];
  for (const target of payload.targets) {
    const canonical = await resolveEntity(store, target);
    if (canonical !== target) {
      throw new ActRefused(`topic ${target} was merged into ${canonical} after this was proposed`);
    }
    if (await entityRetired(store, target)) {
      throw new ActRefused(`topic ${target} was retired after this was proposed`);
    }
    targets.push(target);
  }
  const creates = plan.operation === "create" || plan.operation === "split";
  if (creates) {
    const identity = payload.identity ?? "";
    if (identity !== "") {
      const bound = await first<{ entity_id: string }>(
        store,
        `SELECT a.entity_id FROM aliases a JOIN entities e ON e.id = a.entity_id
         WHERE a.value_key = ? AND a.retired_at IS NULL AND e.canonical_id = e.id LIMIT 1`,
        [aliasKey(identity)],
      );
      if (bound !== null && !(await entityRetired(store, bound.entity_id))) {
        throw new ActRefused(
          `the identity this plan would bind is already bound by entity ${bound.entity_id}`,
        );
      }
    }
    const name = (payload.name ?? "").trim();
    if (name === "") throw new ActRefused("a topic this plan would create has no name");
    const entityId = newId("ent");
    applied.entityId = entityId;
    statements.push({
      sql: `INSERT INTO entities(id, kind, name, canonical_id, created_by, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
      params: [entityId, payload.entityKind ?? "subject", name, entityId, operator, at],
    });
    // The identity becomes an identifier alias, and that is what makes the binding enforceable
    // rather than documentary: the next plan for the same repository resolves it through this
    // index and is refused as bound.
    const aliases = new Map<string, { kind: string; value: string }>();
    if (identity !== "")
      aliases.set(`identifier:${aliasKey(identity)}`, { kind: "identifier", value: identity });
    aliases.set(`name:${aliasKey(name)}`, { kind: "name", value: name });
    for (const alias of payload.aliases) {
      aliases.set(`${alias.kind}:${aliasKey(alias.value)}`, alias);
    }
    for (const alias of aliases.values()) {
      statements.push({
        sql: `INSERT INTO aliases(id, entity_id, kind, value, value_key, retired_at, created_at)
              VALUES(?, ?, ?, ?, ?, NULL, ?)`,
        params: [newId("als"), entityId, alias.kind, alias.value, aliasKey(alias.value), at],
      });
    }
    for (const fact of payload.binding) {
      const factId = newId("fct");
      statements.push(
        {
          sql: `INSERT INTO facts(id, entity_id, predicate, value, object_id, valid_from, valid_until,
                  observed_at, authority_kind, authority_id, confidence, note, supersedes_id, recorded_at)
                VALUES(?, ?, ?, ?, NULL, ?, NULL, ?, 'operator', ?, 'stated', ?, NULL, ?)`,
          params: [
            factId,
            entityId,
            fact.predicate,
            fact.value,
            at,
            at,
            operator,
            fact.note ?? null,
            at,
          ],
        },
        {
          sql: `INSERT INTO fact_status(id, fact_id, seq, status, actor_id, reason, recorded_at)
                VALUES(?, ?, 1, 'active', ?, NULL, ?)`,
          params: [newId("fst"), factId, operator, at],
        },
      );
    }
    // The author of a filing an application performs is the plan's provenance and not the
    // accepting operator: a run judged that each record is about this topic, so the filing is
    // the run's; a plan with no run behind it judged nothing, so its filings are heuristic and
    // the triage lane knows to revisit them. What he accepted is the topic, not each membership.
    const heuristic = plan.proposed_by_kind !== "run" || plan.proposed_by_id === "";
    const filed: string[] = [];
    for (const record of payload.records) {
      // Checked here rather than left to the foreign key: a plan naming a record this store does
      // not hold is Babel's mistake, and the whole application is refused so the operator is
      // told to let it look again. The Go tree filed after its own commit and left such records
      // unfiled beside a created topic — it had two database handles and no choice; one
      // transaction has one, and a half-applied acceptance is the worse half.
      await recordKind(store, record.id);
      const id = newId("fil");
      filed.push(id);
      statements.push(
        filingStatement({
          id,
          recordId: record.id,
          entityId,
          rationale: record.rationale ?? payload.reasoning,
          authorKind: heuristic ? "heuristic" : "run",
          authorId: heuristic ? "" : plan.proposed_by_id,
          heuristic,
          withdrawn: false,
          supersedes: (await newestFiling(store, record.id, entityId))?.id ?? null,
          at,
        }),
      );
    }
    applied.filed = filed;
    if (plan.operation === "split") {
      const parent = targets[0];
      if (parent === undefined) throw new ActRefused("a split names the topic it carves out of");
      // §4.8's split is recorded as a resolution naming the subject it came out of and the part
      // it produced. The parent keeps its facts, its filings and its history — they were
      // asserted about the identity as it was then understood — and the records the plan named
      // move by being filed under the part.
      const resolution = resolutionStatements("split", operator, payload.reasoning, at, [
        ["parent", 0, parent],
        ["part", 0, entityId],
      ]);
      statements.push(...resolution.statements);
      applied.resolutionId = resolution.resolutionId;
    }
    return statements;
  }
  if (plan.operation === "merge") {
    const source = targets[0];
    const target = targets[1];
    if (source === undefined || target === undefined) {
      throw new ActRefused("a merge names the topic to fold and the one to fold it into");
    }
    if (source === target) throw new ActRefused("a merge names one topic twice");
    // Two things of different kinds are not one thing said twice. A wrong merge is the failure
    // §4.8 is most concerned with, and a repository folded into a project is the shape it takes.
    const kinds: Record<string, string> = {};
    for (const row of await store.db.query<{ id: string; kind: string }>(
      `SELECT id, kind FROM entities WHERE id IN (?, ?)`,
      [source, target],
    )) {
      kinds[row.id] = row.kind;
    }
    const sourceKind = kinds[source];
    const targetKind = kinds[target];
    if (sourceKind !== undefined && targetKind !== undefined && sourceKind !== targetKind) {
      throw new ActRefused(`topic ${source} is a ${sourceKind} and ${target} is a ${targetKind}`);
    }
    const resolution = resolutionStatements("merge", operator, payload.reasoning, at, [
      ["source", 0, source],
      ["target", 0, target],
    ]);
    statements.push(...resolution.statements, {
      // The folded identity keeps its row, its aliases and its facts; what changes is who speaks
      // for it, which is the one thing a reader of a merged name has to learn.
      sql: `UPDATE entities SET canonical_id = ? WHERE canonical_id = ?`,
      params: [target, source],
    });
    applied.entityId = target;
    applied.resolutionId = resolution.resolutionId;
    return statements;
  }
  const subject = targets[0];
  if (subject === undefined) throw new ActRefused("a retirement names the topic it retires");
  const retirement = await stateFactStatements(
    store,
    subject,
    PREDICATE_LIFECYCLE,
    LIFECYCLE_RETIRED,
    operator,
    payload.reasoning,
    at,
  );
  applied.entityId = subject;
  applied.factId = retirement.factId;
  statements.push(...retirement.statements);
  return statements;
}

/** What a backlog status change is called for each operation (§4.13's last paragraph). */
const BACKLOG_SETTLES: Record<string, string> = {
  consolidate: "promoted",
  supersede: "superseded",
  retire: "retired",
  promote: "promoted",
};

async function backlogStatements(
  store: ActsStore,
  plan: PlanRow,
  operator: string,
  at: string,
  applied: Application,
): Promise<SqlStatement[]> {
  const payload = BacklogPlanPayloadSchema.parse(JSON.parse(plan.payload));
  const settles = BACKLOG_SETTLES[plan.operation];
  if (settles === undefined)
    throw new ActRefused(`backlog operation ${JSON.stringify(plan.operation)}`);
  if (payload.hypotheses.length === 0) throw new ActRefused("a backlog plan names no candidate");
  const statements: SqlStatement[] = [];
  // A candidate can have been revived, promoted by another accepted plan or superseded by a
  // different one between the proposal and the ruling; settling it again would append a second
  // ending nobody argued for.
  for (const candidate of payload.hypotheses) {
    await recordKind(store, candidate);
    const status = await recordStatus(store, candidate);
    if (status !== "deferred") {
      throw new ActRefused(`hypothesis ${candidate} is ${status}, not deferred`);
    }
  }
  if (payload.supersededBy !== undefined && payload.supersededBy !== "") {
    const status = await recordStatus(store, payload.supersededBy);
    if (status === "superseded" || status === "retired") {
      throw new ActRefused(`hypothesis ${payload.supersededBy} was already ${status}`);
    }
    statements.push({
      sql: `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note,
              actor_kind, actor_id, created_at)
            VALUES(?, 'supersedes', 'hypothesis', ?, 'hypothesis', ?, NULL, ?, 'operator', ?, ?)`,
      params: [
        newId("edg"),
        payload.supersededBy,
        payload.hypotheses[0] ?? "",
        payload.reasoning,
        operator,
        at,
      ],
    });
  }
  if (plan.operation === "promote") {
    const fact = payload.fact;
    if (fact === undefined) throw new ActRefused("a promotion carries the fact it would record");
    const entityId = await resolveEntity(store, fact.entityId);
    // §4.8 gives the accepting operator the authority for every fact a proposal carries, and
    // the confidence is the Go's: a promoted observation is not a guess.
    const asserted = await stateFactStatements(
      store,
      entityId,
      fact.predicate,
      fact.value,
      operator,
      fact.note ?? payload.reasoning,
      at,
      "high",
    );
    statements.push(...asserted.statements);
    applied.entityId = entityId;
    applied.factId = asserted.factId;
  }
  for (const candidate of payload.hypotheses) {
    statements.push(statusEventStatement(candidate, settles, operator, payload.reasoning, at));
    applied.settled.push(candidate);
  }
  return statements;
}

// ------------------------------------------------------------------------ the refinement's
// application

/*
  ACCEPTING A REFINEMENT WRITES THE SUPERSEDING REVISION (§4.7, #341).

  A review can propose a refinement — the exact revision, the exact JSON Pointer, and the
  replacement wording — and until now the operator's acceptance of one wrote a disposition and
  nothing else: the reworded record was never written, so the whole lane produced proposals whose
  acceptance did nothing.

  IT IS NOT A PLAN ROW, and that is a schema fact rather than a preference. `plans.kind` is
  `CHECK (kind IN ('topic','backlog','answer'))`; SQLite has no statement that widens a CHECK,
  and this store's additions are applied only where the object they name is absent, so a store
  already in the field could never reach a widened one. The refinement therefore travels in the
  proposal's own payload, under `REFINEMENT_KEY`, and is applied from there.

  A CORRECTION IS A SUPERSESSION. `records` refuses an UPDATE by trigger, so applying a
  refinement appends a new revision of the same root: `root_id` carried over, `supersedes_id`
  naming the wording it replaces, `seq` one past the root's highest. `store/coordinator.ts`'s
  `heads()` picks the newest revision per root by `seq`, so the new row becomes what is reviewed
  and the old one stops being drawn without anything being marked.

  AND NOTHING WRITES A STATUS EVENT. It would be the obvious second lineage marker and it would
  be a bug: `status_events` is read PER ROOT (`statuses()`), so a `superseded` row on the old
  revision would gap out every review of the new head as `record-replaced` — the supersession
  would silence the very wording it installed.
*/

/** The `records.id` family for each kind, as `RecordIdSchema` spells the four of them. */
const RECORD_PREFIX: Record<string, string> = {
  hypothesis: "hyp",
  observation: "obs",
  finding: "fnd",
  proposal: "pro",
};

interface RevisionRow extends SqlRow {
  id: string;
  kind: string;
  root_id: string;
  seq: number;
  title: string;
  payload: string;
}

/**
 * The refinement a proposal carries, or null when it carries none.
 *
 * A payload whose `refinement` block does not parse is a REFUSAL rather than an absence: the
 * operator pressed accept on a proposal whose whole content is that block, and reading it as
 * "this proposal proposes nothing" would report his acceptance as having succeeded at nothing.
 */
async function refinementFor(store: ActsStore, proposalId: string): Promise<Refinement | null> {
  const row = await first<{ kind: string; payload: string }>(
    store,
    `SELECT kind, payload FROM records WHERE id = ?`,
    [proposalId],
  );
  if (row === null || row.kind !== "proposal") return null;
  let payload: unknown;
  try {
    payload = JSON.parse(row.payload);
  } catch {
    return null;
  }
  if (typeof payload !== "object" || payload === null) return null;
  const held = (payload as Record<string, unknown>)[REFINEMENT_KEY];
  if (held === undefined) return null;
  const parsed = RefinementSchema.safeParse(held);
  if (!parsed.success) {
    throw new ActRefused(
      `the refinement ${proposalId} carries is not one this build can apply: ` +
        parsed.error.issues.map((issue) => `${issue.path.join(".")} ${issue.message}`).join("; "),
    );
  }
  return parsed.data;
}

/**
 * The reworded `title` and `payload`, with the replacement written at the pointer.
 *
 * THE POINTER INDEXES THE PROJECTION A REVIEWER READ, not the payload column: a review is shown
 * `{ id, kind, root_id, parent_id, title, created_at, payload }` (`server/conductor.ts`'s
 * `project()`), which is why `/title` and `/payload/problem` are the two shapes a refinement
 * names. Everything else in that projection is the record's IDENTITY — its id, its kind, its
 * root, its instant — and a refinement is a rewording, so a pointer at one of those is refused
 * rather than honoured.
 *
 * The replacement is text, so only text may be replaced. A pointer resting on an object or a
 * number would otherwise have its shape swapped for a sentence, and the payload a later reader
 * parses would no longer be the kind's own shape.
 */
function reworded(
  revision: RevisionRow,
  refinement: Refinement,
): { title: string; payload: string } {
  const path = refinement.targetPath;
  if (path === "/title") return { title: refinement.replacement, payload: revision.payload };
  if (!path.startsWith("/payload/")) {
    throw new ActRefused(
      `this refinement would change ${path}, which is the record's identity rather than its ` +
        "wording; a refinement replaces the text under /title or under /payload",
    );
  }
  let payload: unknown;
  try {
    payload = JSON.parse(revision.payload);
  } catch {
    throw new ActRefused(`revision ${revision.id} holds no readable payload to refine`);
  }
  const keys = path
    .slice("/payload/".length)
    .split("/")
    .map((part) => part.replace(/~1/g, "/").replace(/~0/g, "~"));
  let holder: unknown = payload;
  for (const key of keys.slice(0, -1)) holder = step(holder, key, path);
  const last = keys[keys.length - 1] ?? "";
  const target = step(holder, last, path);
  if (typeof target !== "string") {
    throw new ActRefused(
      `${path} holds ${target === null ? "null" : typeof target}, and a refinement replaces ` +
        "text; this one would change the record's shape rather than its wording",
    );
  }
  if (Array.isArray(holder)) holder[Number(last)] = refinement.replacement;
  else (holder as Record<string, unknown>)[last] = refinement.replacement;
  return { title: revision.title, payload: JSON.stringify(payload) };
}

/** One step of a JSON Pointer into a payload, refusing a key the revision does not carry. */
function step(holder: unknown, key: string, path: string): unknown {
  if (Array.isArray(holder)) {
    if (!/^(0|[1-9]\d*)$/.test(key) || Number(key) >= holder.length) {
      throw new ActRefused(`${path} names no part of this revision`);
    }
    return holder[Number(key)];
  }
  if (typeof holder !== "object" || holder === null || !Object.hasOwn(holder, key)) {
    throw new ActRefused(`${path} names no part of this revision`);
  }
  return (holder as Record<string, unknown>)[key];
}

/**
 * Everything applying this refinement writes: the superseding revision, the `supersedes` edge
 * that states the lineage as a relation, and the superseded revision's own outgoing edges
 * carried onto it.
 *
 * THE EDGES ARE CARRIED BECAUSE A REWORDING IS THE SAME CLAIM. A record's supports, its cited
 * sessions and its topic binding are `edges` rows whose `from_id` is the revision, and the reads
 * over them are per row: `store/store.ts`'s `corroborationOf` counts a record's `consolidates`
 * and `addresses` edges, and `server/conductor.ts`'s `project()` shows a reviewer the sessions a
 * revision `cites`. A revision that inherited none of them would read as a finding that rests on
 * nothing and would be reviewed with its evidence missing — the refinement would have destroyed
 * the record it improved. Filings are NOT carried: they are a separate table read per root
 * (`store/coordinator.ts`'s `filings()`), so they survive on their own, and a copy would be a
 * second filing nobody made.
 *
 * The edge ids are minted in SQL rather than by {@link newId} because the number of edges is not
 * known before the statement runs; the expression produces exactly `newId`'s shape, and one
 * statement can carry any number of rows without approaching the engine's per-batch bound.
 */
async function refinementStatements(
  store: ActsStore,
  proposalId: string,
  refinement: Refinement,
  operator: string,
  at: string,
): Promise<{ statements: SqlStatement[]; revisionId: string }> {
  const revision = await first<RevisionRow>(
    store,
    `SELECT id, kind, root_id, seq, title, payload FROM records WHERE id = ?`,
    [refinement.targetRevisionId],
  );
  if (revision === null) {
    throw new ActRefused(
      `the revision ${refinement.targetRevisionId} this refinement names is not in this store`,
    );
  }
  // A REFINEMENT WRITTEN AGAINST AN OLDER WORDING IS A REFINEMENT OF SOMETHING ELSE. The
  // reviewer read one immutable revision and proposed a replacement for a sentence in it; if
  // that revision has since been superseded, the sentence it names may no longer be there, may
  // have been corrected already, or may have been the thing the newer wording exists to fix.
  // Applying it anyway would fork the root — two revisions superseding one — and silently
  // discard whichever lost.
  const newer = await first<{ id: string }>(
    store,
    `SELECT id FROM records WHERE supersedes_id = ? LIMIT 1`,
    [refinement.targetRevisionId],
  );
  if (newer !== null) {
    throw new ActRefused(
      `revision ${refinement.targetRevisionId} was superseded by ${newer.id} after this ` +
        "refinement was written, so it no longer names the record's current wording; refine " +
        "the newest revision instead",
    );
  }
  const rewritten = reworded(revision, refinement);
  const prefix = RECORD_PREFIX[revision.kind];
  if (prefix === undefined) throw new ActRefused(`record kind ${revision.kind}`);
  const revisionId = newId(prefix);
  const statements: SqlStatement[] = [
    {
      // The row's own columns carry across — kind, root, parent, run, recipe — because a
      // rewording is the same claim from the same run under the same method. What changes is the
      // wording and who wrote it, and the actor is the operator: he is the authority the
      // acceptance rests on, not the run that suggested it.
      sql: `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id,
              recipe_id, recipe_version, actor_kind, actor_id, title, created_at, payload)
            SELECT ?, r.kind, r.root_id, r.id,
                   (SELECT COALESCE(MAX(seq), 0) + 1 FROM records WHERE root_id = r.root_id),
                   r.parent_id, r.run_id, r.recipe_id, r.recipe_version, 'operator', ?, ?, ?, ?
              FROM records r
             WHERE r.id = ?
               AND NOT EXISTS (SELECT 1 FROM records l WHERE l.supersedes_id = r.id)`,
      params: [
        revisionId,
        operator,
        rewritten.title,
        at,
        rewritten.payload,
        refinement.targetRevisionId,
      ],
    },
    {
      // Every row below is conditioned on the revision above having landed, so a target that
      // went stale inside the transaction leaves no edge pointing at a record that is not there.
      sql: `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note,
              actor_kind, actor_id, created_at)
            SELECT ?, 'supersedes', r.kind, r.id, r.kind, ?, NULL, ?, 'operator', ?, ?
              FROM records r WHERE r.id = ?`,
      params: [
        newId("edg"),
        refinement.targetRevisionId,
        `${refinement.reason} (${proposalId}, ${refinement.targetPath})`,
        operator,
        at,
        revisionId,
      ],
    },
    {
      sql: `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note,
              actor_kind, actor_id, created_at)
            SELECT 'edg_' || lower(hex(randomblob(8))), e.kind, e.from_kind, ?, e.to_kind,
                   e.to_id, e.position, e.note, 'operator', ?, ?
              FROM edges e
             WHERE e.from_id = ? AND e.kind <> 'supersedes'
               AND EXISTS (SELECT 1 FROM records r WHERE r.id = ?)`,
      params: [revisionId, operator, at, refinement.targetRevisionId, revisionId],
    },
  ];
  return { statements, revisionId };
}

/**
 * Applies the plan the proposal carries, on the operator's authority. It is the body a ruling of
 * `accept` runs, exposed on its own because the conductor applies a plan it has already ruled on
 * and because a failed application is retried rather than re-ruled.
 */
export async function applyPlan(
  store: ActsStore,
  proposalId: string,
  operator: string,
  reason: string,
): Promise<PlanOutcome> {
  if (operator === "") throw new ActRefused("an acceptance has no operator");
  const plan = await planFor(store, proposalId);
  if (plan === null) throw new ActRefused(`proposal ${proposalId} carries no open plan`);
  const at = stamp(store.now());
  const { statements, outcome } = await applyStatements(store, plan, operator, reason, at);
  await store.db.batch(statements);
  store.touch();
  return outcome;
}

/**
 * Declines the plan, keeping the operator's reason verbatim. The reason is required: §4.13 has
 * the triage lane read why a plan was declined as the evidence it weighs before proposing the
 * same thing again, and a refusal with no reason teaches Babel nothing.
 */
export async function declinePlan(
  store: ActsStore,
  proposalId: string,
  operator: string,
  reason: string,
): Promise<PlanOutcome> {
  if (operator === "") throw new ActRefused("a decline has no operator");
  if (reason.trim() === "")
    throw new ActRefused("a declined plan keeps the operator's reason, and this one is empty");
  const plan = await planFor(store, proposalId);
  if (plan === null) throw new ActRefused(`proposal ${proposalId} carries no open plan`);
  const at = stamp(store.now());
  const statement = planRuledStatement(plan.id, "declined", operator, reason, at, {});
  await store.db.run(statement.sql, statement.params ?? []);
  store.touch();
  return {
    kind: plan.kind === "backlog" ? "backlog" : "topic",
    operation: plan.operation,
    applied: false,
    declined: true,
  };
}

// ---------------------------------------------------------------------------- the acts

export interface RuleArgs {
  id: string;
  ruling: Ruling;
  note: string;
  duplicateOf?: string | undefined;
}

/**
 * The operator's ruling on a record (§4.7), and what it applies.
 *
 * The order of the refusals is the design. Everything that can refuse — the record, the
 * original a duplicate names, the transition the standing allows, the reason a plan's rejection
 * keeps, the state the ledger has moved to — is checked while the record is still undecided,
 * because a ruling appended first would leave the record ruled and the plan unanswerable. Then
 * the ruling and everything it applies go into ONE transaction, so an acceptance never half
 * lands. The two things that are reported rather than raised are a plan the ledger has moved
 * past and a refinement whose revision has been superseded: the ruling is the operator's answer
 * to the proposal and stands on its own, so it is written and the refusal travels back beside it.
 */
export async function rule(store: ActsStore, args: RuleArgs, operator: string): Promise<Ruled> {
  if (operator === "") throw new ActRefused("a ruling has no operator");
  const kind = await recordKind(store, args.id);
  if (args.ruling === "reopen") {
    if (args.note.trim() === "") throw new ActRefused("a reopen states no reason for reopening");
    if (args.duplicateOf !== undefined) throw new ActRefused("a reopen names no original");
  }
  if (args.ruling === "duplicate") {
    if (args.duplicateOf === undefined)
      throw new ActRefused("a duplicate ruling names no original");
    const original = await recordKind(store, args.duplicateOf);
    if (original !== kind) {
      throw new ActRefused(
        `record ${args.id} is a ${kind} and the original it duplicates is a ${original}`,
      );
    }
  } else if (args.ruling !== "reopen" && args.duplicateOf !== undefined) {
    throw new ActRefused(`a ${args.ruling} names no original`);
  }
  const standing = standingOf(
    (
      await first<{ disposition: string }>(
        store,
        `SELECT disposition FROM dispositions WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
        [args.id],
      )
    )?.disposition ?? null,
  );
  const refusal = rulingRefusal(standing, args.ruling);
  if (refusal !== null) throw new ActRefused(refusal);

  const plan = await planFor(store, args.id);
  if (plan !== null && args.ruling === "reject" && args.note.trim() === "") {
    throw new ActRefused(
      "rejecting a proposal that carries a plan keeps the reason verbatim, and suppresses the same " +
        "plan until something materially new turns up; this one gives none",
    );
  }
  const at = stamp(store.now());
  const statements: SqlStatement[] = [
    {
      sql: `INSERT INTO dispositions(id, record_id, seq, disposition, duplicate_of_id, note, context_id,
              actor_id, recorded_at)
            SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, NULL, ?, ?
            FROM dispositions WHERE record_id = ?
            RETURNING seq`,
      params: [
        newId("dsp"),
        args.id,
        args.ruling,
        args.duplicateOf ?? null,
        args.note,
        operator,
        at,
        args.id,
      ],
    },
  ];
  let outcome: PlanOutcome | null = null;
  if (plan !== null) {
    outcome = {
      kind: plan.kind === "backlog" ? "backlog" : "topic",
      operation: plan.operation,
      applied: false,
      declined: false,
    };
    if (args.ruling === "accept") {
      try {
        const applied = await applyStatements(store, plan, operator, args.note, at);
        statements.push(...applied.statements);
        outcome = applied.outcome;
      } catch (error) {
        if (!(error instanceof ActRefused)) throw error;
        outcome.error = error.message;
      }
    } else if (args.ruling === "reject") {
      statements.push(planRuledStatement(plan.id, "declined", operator, args.note, at, {}));
      outcome.declined = true;
    }
    // Every other ruling — defer, duplicate, refine, reopen — leaves the plan open, which is
    // what a deferral means.
  }
  // THE REFINEMENT IS ANSWERED ONLY BY AN ACCEPTANCE, and the asymmetry with a plan is real: a
  // plan is a `plans` row whose `state` some ruling has to answer, so a rejection declines it,
  // while a refinement is a block in the proposal's own payload with no state of its own.
  // Deferring or rejecting a refinement proposal does exactly what it does to any proposal, and
  // there is nothing to report about it.
  let refined: RefinementOutcome | null = null;
  if (args.ruling === "accept") {
    try {
      const refinement = await refinementFor(store, args.id);
      if (refinement !== null) {
        refined = {
          targetRecordId: refinement.targetRecordId,
          targetPath: refinement.targetPath,
          revisionId: "",
          applied: false,
        };
        const written = await refinementStatements(store, args.id, refinement, operator, at);
        statements.push(...written.statements);
        refined.revisionId = written.revisionId;
        refined.applied = true;
      }
    } catch (error) {
      if (!(error instanceof ActRefused)) throw error;
      // A refinement the store will not apply leaves the acceptance standing and says why:
      // §4.7 does not un-append a ruling, and the operator's answer to the proposal was still
      // his answer.
      refined = {
        targetRecordId: refined?.targetRecordId ?? args.id,
        targetPath: refined?.targetPath ?? "",
        revisionId: "",
        applied: false,
        error: error.message,
      };
    }
  }
  const results = await store.db.batch(statements);
  const seq = Number(results[0]?.[0]?.["seq"] ?? 0);
  store.touch();
  return {
    id: args.id,
    standing: STANDING_OF[args.ruling],
    seq,
    plan: outcome,
    refinement: refined,
  };
}

export interface DecideArgs {
  nextActionId: string;
  decision: NextActionDecision;
  note: string;
}

/**
 * THE OPERATOR'S ANSWER TO A NEXT ACTION A RUN PROPOSED (#340), appended.
 *
 * It is a separate act from {@link rule} because it answers a separate question. `rule` judges
 * the CLAIM — is this finding any good — and writes `dispositions`. This judges the ACTION — is
 * this worth doing — and writes `next_action_rulings`. One act with a wider vocabulary would
 * make "accepted" mean two things in the same corpus, which is the ambiguity the retired
 * product split its own packages to avoid, and it is exactly where an acceptance rate is meant
 * to be evidence about output quality.
 *
 * NOTHING IS APPLIED. §4.6 keeps publishing, applying and writing to a source repository
 * outside Babel, so accepting a `draft-issue` opens no issue and accepting a `store-memory`
 * writes no memory: the row is the durable, attributable fact that a person accepted it.
 *
 * RECONSIDERING IS A LATER ROW. The ledger is append-only by trigger and the standing is its
 * newest entry, so an operator who accepts and then declines leaves both readable — which is
 * what makes the ledger evidence rather than a status column. Answering the same way twice is
 * refused instead: it says nothing new and would put two indistinguishable rows in the history
 * a later reader has to interpret.
 *
 * The note is kept verbatim and is not required. `declinePlan` demands a reason because §4.13's
 * triage lane reads it before proposing the same plan again; nothing reads this one yet, and a
 * reason nothing reads is friction on a choice that is meant to be one press.
 */
export async function decide(
  store: ActsStore,
  args: DecideArgs,
  operator: string,
): Promise<Decided> {
  if (operator === "") throw new ActRefused("a decision has no operator");
  const proposal = await first<{ record_id: string; kind: string }>(
    store,
    `SELECT record_id, kind FROM next_actions WHERE id = ?`,
    [args.nextActionId],
  );
  if (proposal === null) {
    throw new ActRefused(`no proposed action ${args.nextActionId}`);
  }
  const standing = await first<{ decision: string }>(
    store,
    `SELECT decision FROM next_action_rulings WHERE next_action_id = ? ORDER BY seq DESC LIMIT 1`,
    [args.nextActionId],
  );
  if (standing?.decision === args.decision) {
    throw new ActRefused(
      `this ${proposal.kind} was already ${args.decision}; a decision that says nothing new is not a reconsideration`,
    );
  }
  const at = stamp(store.now());
  const [appended] = await store.db.batch([
    {
      sql: `INSERT INTO next_action_rulings(id, next_action_id, seq, decision, operator_id, note,
              recorded_at)
            SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?
            FROM next_action_rulings WHERE next_action_id = ?
            RETURNING seq`,
      params: [
        newId("nxr"),
        args.nextActionId,
        args.decision,
        operator,
        args.note,
        at,
        args.nextActionId,
      ],
    },
  ]);
  store.touch();
  return {
    id: args.nextActionId,
    recordId: proposal.record_id,
    standing: args.decision,
    seq: Number(appended?.[0]?.["seq"] ?? 0),
    at,
  };
}

// ------------------------------------------------- what an allowed plugin may suggest (#410)

/*
  A SUGGESTION IS A `next_actions` ROW AND CAN BE NOTHING ELSE.

  Every statement below names `next_actions` or reads `policies`, `records`, `dispositions` and
  `next_action_rulings`, and that is the whole reachable set: no `records`, no `edges`, no
  `assessments`, no `dispositions`, no `status_events`, no delete and no edit. The frontier's two
  writer classes — the operator, authenticated; a run, mediated by the conductor against a schema
  the baseline owns — are unchanged, which is what keeps "a run may propose and may never rule"
  a property of the store rather than a rule somebody remembers.

  THE SUGGESTER IS RESOLVED FROM THE PRINCIPAL, never from the input. The host authenticates
  `ctx.principal` and supplies no caller plugin id at all, so the operator's allow-list maps the
  one to the other; an unlisted principal is refused by name and told what would allow it. The
  row's `proposed_by_kind` is the literal `'engine'` in the SQL below — there is no argument, and
  no code path, that could make it `'operator'`.
*/

/** What the policy allows to suggest, as the newest `policies` row carries it. */
async function allowedSuggesters(store: ActsStore): Promise<readonly Suggester[]> {
  const row = await first<{ suggesters: string | null }>(
    store,
    `SELECT json_extract(payload, '$.suggesters') AS suggesters FROM policies ORDER BY seq DESC LIMIT 1`,
    [],
  );
  // No policy, or one installed before the field existed: nobody may suggest, which is the
  // right default for an authority the operator has not granted yet.
  const carried = row?.suggesters;
  if (carried === null || carried === undefined || carried === "") return [];
  const parsed = z.array(SuggesterSchema).safeParse(JSON.parse(carried));
  if (!parsed.success) {
    throw new Error(`the policy's suggesters are not an allow-list: ${parsed.error.message}`);
  }
  return parsed.data;
}

/**
 * The plugin this principal writes as, or the refusal saying what would allow it.
 *
 * The sentence names the act the operator would perform, because a refusal a caller cannot act
 * on produces a support question rather than a policy change.
 */
export async function suggesterFor(store: ActsStore, principalId: string): Promise<string> {
  if (principalId === "") throw new ActRefused("a suggestion has no caller");
  const allowed = await allowedSuggesters(store);
  const match = allowed.find((suggester) => suggester.principalId === principalId);
  if (match === undefined) {
    throw new ActRefused(
      `${principalId} is not allowed to suggest: add ` +
        `{"principalId":${JSON.stringify(principalId)},"pluginId":"<the plugin>"} to the ` +
        `policy's "suggesters" and install it with the setPolicy door`,
    );
  }
  return match.pluginId;
}

/** One revision, and the revision that has replaced it if any has. */
interface JudgedRow extends SqlRow {
  seq: number | bigint;
  head_id: string;
  head_seq: number | bigint;
}

export interface SuggestArgs {
  recordId: string;
  revision: number;
  kind: NextAction;
  /**
   * The other record this suggestion is about, or empty because there is not one. See
   * `SuggestInputSchema` for why a pair finding needs it; here it is the fifth column of the
   * live-uniqueness key and nothing else.
   */
  subject: string;
  summary: string;
  rationale: string;
  /** What the suggester judged under; kept on the row and compared by equality, never parsed. */
  basis: string;
}

/**
 * ONE TYPED SUGGESTION ABOUT ONE RECORD REVISION, attributed to the plugin the operator
 * allow-listed.
 *
 * The refusals are in the order that keeps each one meaningful: who is asking, then what they
 * named, then whether the argument is still open, then whether they have already said it. A
 * suggestion about a wording that has been superseded is refused rather than inherited, because
 * a record is immutable and a refinement is a new revision — a suggestion carrying only a record
 * id would silently re-attach to whatever the live revision becomes.
 *
 * ONE LIVE SUGGESTION PER (SUGGESTER, REVISION, KIND, SUBJECT), and the second supersedes the
 * first. The subject is in the key because a record can be half of two pairs — two records it
 * contradicts, one it supersedes — and without it the second finding would supersede the first
 * and the operator would only ever see one counterpart. It defaults to empty, which is every
 * per-record suggester and every row already written, so for them the key is the one it was.
 *
 * The same predicate is the durable "already judged" mark: `suggestionsOf` counts the revisions
 * this suggester has ever named — over any subject, because a record judged in one pair has been
 * judged — so a retroactive sweep over the whole corpus can state how many rows it would add
 * before it adds one. One constraint, two jobs, and no second bookkeeping table to disagree with
 * the rows.
 */
export async function suggest(
  store: ActsStore,
  args: SuggestArgs,
  principalId: string,
): Promise<Suggested> {
  const suggester = await suggesterFor(store, principalId);
  if (args.summary.trim() === "") throw new ActRefused("a suggestion says what to do, in one line");
  const judged = await first<JudgedRow>(
    store,
    `SELECT r.seq AS seq,
            (SELECT h.id FROM records h WHERE h.root_id = r.root_id
              ORDER BY h.seq DESC, h.rowid DESC LIMIT 1) AS head_id,
            (SELECT h.seq FROM records h WHERE h.root_id = r.root_id
              ORDER BY h.seq DESC, h.rowid DESC LIMIT 1) AS head_seq
       FROM records r WHERE r.id = ?`,
    [args.recordId],
  );
  // `next_actions.record_id` REFERENCES `records(id)`, so the store would refuse this anyway —
  // as a foreign-key violation nobody can read. Saying it here keeps the refusal legible.
  if (judged === null) throw new ActRefused(`no record ${args.recordId}`);
  if (Number(judged.seq) !== args.revision) {
    throw new ActRefused(
      `${args.recordId} is revision ${String(Number(judged.seq))} and this suggestion was made ` +
        `against revision ${String(args.revision)}`,
    );
  }
  if (judged.head_id !== args.recordId) {
    throw new ActRefused(
      `${args.recordId} has been superseded by ${judged.head_id} (revision ` +
        `${String(Number(judged.head_seq))}): a suggestion is about the revision it read`,
    );
  }
  // THE COUNTERPART HAS TO BE A RECORD. `next_actions` has no foreign key for it — the column
  // is one leaf of a JSON payload — so nothing but this refuses an id that resolves to nothing,
  // and a dangling counterpart is a pair finding the operator cannot read the other half of.
  if (args.subject !== "") {
    if (args.subject === args.recordId) {
      throw new ActRefused(`${args.recordId} cannot be its own counterpart`);
    }
    const counterpart = await first<{ id: string }>(store, `SELECT id FROM records WHERE id = ?`, [
      args.subject,
    ]);
    if (counterpart === null) throw new ActRefused(`no counterpart record ${args.subject}`);
  }
  const standing = standingOf(
    (
      await first<{ disposition: string }>(
        store,
        `SELECT disposition FROM dispositions WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
        [args.recordId],
      )
    )?.disposition ?? null,
  );
  // A RULING ENDS THE ARGUMENT. Reopening is the operator's own act and returns the standing to
  // `new`, so a suggestion is admitted again exactly when he has re-opened the question.
  if (standing !== "new") {
    throw new ActRefused(`${args.recordId} is ${standing}: the operator has ruled on it`);
  }
  const live = await first<{ id: string }>(
    store,
    `SELECT n.id AS id FROM next_actions n
      WHERE n.record_id = ? AND n.kind = ? AND n.proposed_by_kind = 'engine'
        AND n.proposed_by_id = ? AND json_extract(n.payload, '$.revision') = ?
        AND COALESCE(json_extract(n.payload, '$.subject'), '') = ?
        AND NOT EXISTS (SELECT 1 FROM next_actions s WHERE s.record_id = n.record_id
                          AND json_extract(s.payload, '$.supersedes') = n.id)
      ORDER BY n.created_at DESC, n.id DESC LIMIT 1`,
    [args.recordId, args.kind, suggester, args.revision, args.subject],
  );
  const supersedes = live?.id ?? "";
  const id = newId("nxt");
  const at = stamp(store.now());
  const payload = JSON.stringify({
    rationale: args.rationale,
    revision: args.revision,
    suggester,
    supersedes,
    // The counterpart, empty for a per-record suggestion. A row written before this field
    // existed carries no leaf at all, which `COALESCE` reads as the same empty: the old rows and
    // the new per-record ones share one uniqueness key, as they must.
    subject: args.subject,
    // The mark's own date: `suggestionsOf` reads it back as an equality, which is what lets a
    // suggester whose rules moved find its corpus unjudged again without forgetting this row.
    basis: args.basis,
  });
  // THE UNIQUENESS IS IN THE STATEMENT and not only in the read above, so two calls that raced
  // past the same read cannot both land: the second finds a live sibling it is not superseding
  // and inserts nothing.
  const [inserted] = await store.db.batch([
    {
      sql: `INSERT INTO next_actions(id, record_id, kind, proposed_by_kind, proposed_by_id,
              summary, created_at, payload)
            SELECT ?, ?, ?, 'engine', ?, ?, ?, ?
             WHERE NOT EXISTS (
               SELECT 1 FROM next_actions n
                WHERE n.record_id = ? AND n.kind = ? AND n.proposed_by_kind = 'engine'
                  AND n.proposed_by_id = ? AND json_extract(n.payload, '$.revision') = ?
                  AND COALESCE(json_extract(n.payload, '$.subject'), '') = ?
                  AND n.id <> ?
                  AND NOT EXISTS (SELECT 1 FROM next_actions s WHERE s.record_id = n.record_id
                                    AND json_extract(s.payload, '$.supersedes') = n.id))
            RETURNING id`,
      params: [
        id,
        args.recordId,
        args.kind,
        suggester,
        args.summary,
        at,
        payload,
        args.recordId,
        args.kind,
        suggester,
        args.revision,
        args.subject,
        supersedes,
      ],
    },
  ]);
  if ((inserted?.length ?? 0) === 0) {
    throw new ActRefused(
      `${suggester} already has a live ${args.kind} suggestion on ${args.recordId} revision ` +
        `${String(args.revision)}` +
        (args.subject === "" ? "" : ` about ${args.subject}`),
    );
  }
  store.touch();
  const counted = await suggestionsOf(store, suggester);
  return {
    id,
    recordId: args.recordId,
    revision: args.revision,
    kind: args.kind,
    subject: args.subject,
    suggester,
    supersedes,
    at,
    outstanding: counted.outstanding,
  };
}

/** The one asked when nobody asked: the counts, every kind, any basis, from the start. */
const EVERY_SUGGESTION: SuggestionsQuery = { pending: 0, basis: "", kinds: [], after: "" };

/**
 * THE GAP, AS ONE PREDICATE: the live records of these kinds that this suggester has no mark on
 * under this basis.
 *
 * It is built once and used by both the count and the page, because a sweep that was told a
 * number by one query and handed rows by another could be told the two disagree — and the number
 * is what an operator authorises spending against.
 *
 * A RULED RECORD IS STILL UNSCREENED. The gap is what a sweep has not LOOKED at, and #360 asks
 * for the whole imported corpus: a record the operator accepted or declined has a position a
 * screener can still hold about it, and excluding it would leave the larger half of a long-lived
 * hub permanently unread. What a ruling governs is what may be OFFERED — a caller suppresses the
 * action it would deliver on a ruled record — and that is a decision about delivery rather than
 * a reason to never read the row. Only the record's live head is here: a superseded revision is
 * a wording nobody can act on.
 *
 * A row whose `basis` is not the one asked for counts as UNJUDGED, which is the whole mechanism
 * behind a moved rule set: it is `pendingVectors`' join on the model (`store/corpus.ts`) in the
 * other half of the family, and for the same reason — a derived fact computed under something
 * that has since changed is pending rather than present, and needs no second bookkeeping.
 *
 * A ROW WHOSE ID NOTHING CAN NAME IS NOT IN THE GAP AT ALL (#426). `suggest` refuses such an id
 * on the way in and `UnjudgedRecordSchema` refuses it on the way out, so a sweep offered one
 * could only fail on it for ever — and because the exclusion is in the predicate rather than in
 * the caller, the count and the page still answer about the same set: filtering the page after
 * its LIMIT would make `unjudged` promise work no pass can deliver and could hand back an empty
 * page whose continuation never moves.
 */
function gap(
  suggester: string,
  ask: SuggestionsQuery,
): { readonly sql: string; readonly params: readonly SqlParam[] } {
  const kinds = ask.kinds.length === 0 ? RECORD_KINDS : ask.kinds;
  const holes = (values: readonly unknown[]): string => values.map(() => "?").join(", ");
  return {
    sql: `FROM records r
           WHERE r.kind IN (${holes(kinds)})
             AND ${nameableRecordSql("r.id")}
             AND NOT EXISTS (SELECT 1 FROM records h WHERE h.supersedes_id = r.id)
             AND NOT EXISTS (SELECT 1 FROM next_actions n
                              WHERE n.record_id = r.id AND n.proposed_by_kind = 'engine'
                                AND n.proposed_by_id = ?
                                AND (? = '' OR
                                     COALESCE(json_extract(n.payload, '$.basis'), '') = ?))`,
    params: [...kinds, suggester, ask.basis, ask.basis],
  };
}

/**
 * WHAT ONE SUGGESTER'S QUEUE LOOKS LIKE, and what one more sweep would cost.
 *
 * `judged` counts every record this suggester has marked under the basis asked for, superseded
 * rows included: the mark exists so a sweep does not re-judge what it has judged, and a
 * supersession is a second opinion rather than a reason to forget the first. `unjudged` is
 * {@link gap} counted — the LIVE records this suggester has not screened under this basis,
 * ruled ones included, since a ruling decides what may be offered on a record and not whether a
 * screener has read it — and `pending` is the same gap's first rows, so the number a pass is
 * authorised against and the records it would read are one query's two answers.
 *
 * `after` walks the gap in `rowid` order, oldest record first. An `after` naming a record this
 * deployment does not hold starts from the beginning rather than answering nothing: the rows are
 * the authority on what has been judged, a continuation is only how one sequence of passes walks
 * them, and a lost one must cost an ordering rather than stall a sweep for ever.
 */
export async function suggestionsOf(
  store: ActsStore,
  suggester: string,
  ask: SuggestionsQuery = EVERY_SUGGESTION,
): Promise<SuggestionsResult> {
  const live = await first<{ live: number | bigint; outstanding: number | bigint }>(
    store,
    `SELECT COUNT(*) AS live,
            SUM(CASE WHEN (SELECT r.decision FROM next_action_rulings r
                            WHERE r.next_action_id = n.id ORDER BY r.seq DESC LIMIT 1) IS NULL
                     THEN 1 ELSE 0 END) AS outstanding
       FROM next_actions n
      WHERE n.proposed_by_kind = 'engine' AND n.proposed_by_id = ?
        AND NOT EXISTS (SELECT 1 FROM next_actions s WHERE s.record_id = n.record_id
                          AND json_extract(s.payload, '$.supersedes') = n.id)`,
    [suggester],
  );
  const unjudged = gap(suggester, ask);
  const kinds = ask.kinds.length === 0 ? RECORD_KINDS : ask.kinds;
  const marks = await first<{ judged: number | bigint }>(
    store,
    `SELECT COUNT(DISTINCT n.record_id) AS judged
       FROM next_actions n JOIN records r ON r.id = n.record_id
      WHERE n.proposed_by_kind = 'engine' AND n.proposed_by_id = ?
        AND r.kind IN (${kinds.map(() => "?").join(", ")})
        AND (? = '' OR COALESCE(json_extract(n.payload, '$.basis'), '') = ?)`,
    [suggester, ...kinds, ask.basis, ask.basis],
  );
  const counted = await first<{ unjudged: number | bigint }>(
    store,
    `SELECT COUNT(*) AS unjudged ${unjudged.sql}`,
    [...unjudged.params],
  );
  const pending: UnjudgedRecord[] = [];
  if (ask.pending > 0) {
    const rows = await store.db.query<{
      id: string;
      seq: number | bigint;
      kind: string;
      disposition: string | null;
    }>(
      `SELECT r.id AS id, r.seq AS seq, r.kind AS kind,
              (SELECT d.disposition FROM dispositions d
                WHERE d.record_id = r.id ORDER BY d.seq DESC LIMIT 1) AS disposition
         ${unjudged.sql}
         AND r.rowid > COALESCE((SELECT a.rowid FROM records a WHERE a.id = ?), 0)
        ORDER BY r.rowid LIMIT ?`,
      [...unjudged.params, ask.after, ask.pending],
    );
    for (const row of rows) {
      pending.push({
        recordId: String(row.id),
        revision: Number(row.seq),
        kind: RecordKindSchema.parse(row.kind),
        // The newest ruling decides it, read exactly as `suggest` reads it on the way in: the
        // gap carries ruled rows so a screener can still read them, and this is what says the
        // door would refuse the suggestion. The row is the live head already.
        suggestible: standingOf(row.disposition) === "new",
      });
    }
  }
  const outstanding = Number(live?.outstanding ?? 0);
  return {
    suggester,
    outstanding,
    answered: Number(live?.live ?? 0) - outstanding,
    judged: Number(marks?.judged ?? 0),
    unjudged: Number(counted?.unjudged ?? 0),
    pending,
  };
}

export interface CommentArgs {
  id: string;
  text: string;
  kind: "comment" | "question";
  relatedId?: string | undefined;
}

/**
 * The operator's own words on a record: a comment, or a question the record's next review must
 * answer. A question is the words it asks — there is no marker without them — so the text is
 * required for both and `question = 1` is what the next review reads as an obligation.
 */
export async function comment(
  store: ActsStore,
  args: CommentArgs,
  operator: string,
): Promise<Commented> {
  if (operator === "") throw new ActRefused("a comment has no author");
  if (args.text.trim() === "")
    throw new ActRefused("a comment exists to carry words, and this one has none");
  await recordKind(store, args.id);
  const id = newId("fbk");
  const at = stamp(store.now());
  const question = args.kind === "question";
  await store.db.run(
    `INSERT INTO feedback(id, record_id, actor_id, stance, reason, question, related_id, recorded_at)
     VALUES(?, ?, ?, NULL, ?, ?, ?, ?)`,
    [id, args.id, operator, args.text, question ? 1 : 0, args.relatedId ?? null, at],
  );
  store.touch();
  return { id, recordId: args.id, question, at };
}

/** What state an answer with this outcome moves its question to (§4.8's machine). */
const ANSWER_STATE: Record<string, string> = {
  answered: "answered-uninterpreted",
  unknown: "answered",
  declined: "declined",
};

/**
 * §4.8's question state machine. Three edges carry their own reason: `plan-ready` back to
 * `answered-uninterpreted` is what a rejected interpretation does, since the raw answer is still
 * good; `declined` has no edge back to `open`, because a refusal is lifted by asking a new
 * question backed by new evidence and not by silently reopening the one he refused; and
 * `answered` is not terminal, because a fact an answer established can later be superseded.
 */
const QUESTION_TRANSITIONS: Record<string, readonly string[]> = {
  open: ["answered-uninterpreted", "answered", "snoozed", "declined", "obsolete", "superseded"],
  "answered-uninterpreted": ["interpreting", "snoozed", "obsolete", "superseded"],
  interpreting: ["plan-ready", "answered-uninterpreted", "obsolete", "superseded"],
  "plan-ready": ["answered", "answered-uninterpreted", "obsolete", "superseded"],
  answered: ["obsolete", "superseded"],
  snoozed: ["open", "answered-uninterpreted", "obsolete", "superseded"],
  declined: ["obsolete", "superseded"],
  obsolete: [],
  superseded: [],
};

export interface AnswerArgs {
  id: string;
  outcome: "answered" | "unknown" | "declined";
  text: string;
}

/**
 * The operator's answer to a question Babel raised, kept verbatim, with the state event it
 * produces. The text is deliberately not checked for anything but emptiness on a substantive
 * answer: §4.8 requires it retained as he wrote it, and refusing an operator's own words would
 * lose the answer entirely.
 */
export async function answer(
  store: ActsStore,
  args: AnswerArgs,
  operator: string,
): Promise<Answered> {
  if (operator === "") throw new ActRefused("an answer has no author");
  const question = await first<{ id: string }>(store, `SELECT id FROM questions WHERE id = ?`, [
    args.id,
  ]);
  if (question === null) throw new ActRefused(`no question ${args.id}`);
  if (args.outcome === "answered" && args.text.trim() === "") {
    throw new ActRefused("a substantive answer has no text");
  }
  const next = ANSWER_STATE[args.outcome];
  if (next === undefined) throw new ActRefused(`answer outcome ${JSON.stringify(args.outcome)}`);
  const current =
    (
      await first<{ state: string }>(
        store,
        `SELECT state FROM question_events WHERE question_id = ? ORDER BY seq DESC LIMIT 1`,
        [args.id],
      )
    )?.state ?? "open";
  if (!(QUESTION_TRANSITIONS[current] ?? []).includes(next)) {
    throw new ActRefused(`a question that is ${current} cannot become ${next}`);
  }
  const id = newId("ans");
  const at = stamp(store.now());
  await store.db.batch([
    {
      sql: `INSERT INTO answers(id, question_id, actor_id, outcome, text, recorded_at) VALUES(?, ?, ?, ?, ?, ?)`,
      params: [id, args.id, operator, args.outcome, args.text, at],
    },
    {
      sql: `INSERT INTO question_events(id, question_id, seq, state, actor_id, reason, recorded_at)
            SELECT ?, ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ?
            FROM question_events WHERE question_id = ?`,
      params: [newId("qev"), args.id, next, operator, `answer ${id}`, at, args.id],
    },
  ]);
  store.touch();
  return { id, questionId: args.id, state: next, at };
}

export interface InterestArgs {
  entityId: string;
  state: string;
  reason: string;
}

/**
 * The operator's stance toward a topic, as the facts that record it. The two predicates are one
 * transaction here — the Go tree could not do that and said so — so a stance is never half
 * stated, and restating the same stance supersedes rather than being discarded as redundant.
 */
export async function interest(
  store: ActsStore,
  args: InterestArgs,
  operator: string,
): Promise<Interested> {
  if (operator === "") throw new ActRefused("an interest has no operator");
  // Indexed through the contract's own vocabulary: a fifth stance added there fails to compile
  // here until this table says what facts record it, and a stance the store never heard of is
  // still refused at runtime rather than silently writing nothing.
  const mapped = INTEREST_FACTS[args.state as InterestState] as
    { readonly lifecycle: string; readonly policy: string } | undefined;
  if (mapped === undefined) throw new ActRefused(`interest state ${JSON.stringify(args.state)}`);
  const entityId = await resolveEntity(store, args.entityId);
  const at = stamp(store.now());
  const statements: SqlStatement[] = [];
  const facts: string[] = [];
  if (mapped.lifecycle !== "") {
    const lifecycle = await stateFactStatements(
      store,
      entityId,
      PREDICATE_LIFECYCLE,
      mapped.lifecycle,
      operator,
      args.reason,
      at,
    );
    statements.push(...lifecycle.statements);
    facts.push(lifecycle.factId);
  }
  const policy = await stateFactStatements(
    store,
    entityId,
    PREDICATE_ANALYSIS_POLICY,
    mapped.policy,
    operator,
    args.reason,
    at,
  );
  statements.push(...policy.statements);
  facts.push(policy.factId);
  await store.db.batch(statements);
  store.touch();
  return { entityId, state: args.state, facts, at };
}

export interface FileArgs {
  id: string;
  entity: string;
  rationale: string;
}

/**
 * Files a record under a topic, or answers that it is about nothing in particular. A live filing
 * of the same record and topic is superseded rather than duplicated: re-filing with a better
 * rationale is a correction, and two rows both claiming to be the current filing would make
 * "why is this here" a question with two answers.
 */
export async function file(store: ActsStore, args: FileArgs, operator: string): Promise<Filed> {
  if (operator === "") throw new ActRefused("a filing has no author");
  if (args.rationale.trim() === "") {
    throw new ActRefused("a filing says why the record belongs to the topic");
  }
  await recordKind(store, args.id);
  const entityId = args.entity === NO_TOPIC ? "" : await resolveEntity(store, args.entity);
  const current = await newestFiling(store, args.id, entityId);
  const id = newId("fil");
  const at = stamp(store.now());
  const statement = filingStatement({
    id,
    recordId: args.id,
    entityId,
    rationale: args.rationale,
    authorKind: "operator",
    authorId: operator,
    heuristic: false,
    withdrawn: false,
    supersedes: current?.id ?? null,
    at,
  });
  await store.db.run(statement.sql, statement.params ?? []);
  store.touch();
  return { id, recordId: args.id, entityId, withdrawn: false, supersedes: current?.id ?? "", at };
}

export interface UnfileArgs {
  id: string;
  entity: string;
  reason: string;
}

/**
 * Withdraws a filing, with the reason kept verbatim. It appends rather than removing, so the
 * record's history says it was filed here, by whom, and why that was undone. A record that is
 * not filed under the topic is refused: withdrawing a filing that does not exist would write a
 * history nobody made.
 *
 * The withdrawal's own prose is the reason it was withdrawn. The store keeps one prose column
 * per filing and the row it supersedes still carries what was claimed, so the pair reads as the
 * claim and its end rather than the claim twice.
 */
export async function unfile(store: ActsStore, args: UnfileArgs, operator: string): Promise<Filed> {
  if (operator === "") throw new ActRefused("an unfiling has no author");
  if (args.reason.trim() === "") throw new ActRefused("unfiling a record needs its reason");
  const entityId = args.entity === NO_TOPIC ? "" : await resolveEntity(store, args.entity);
  const current = await newestFiling(store, args.id, entityId);
  if (current === null || Number(current.withdrawn) === 1) {
    throw new ActRefused(
      `record ${args.id} is not filed under ${args.entity === NO_TOPIC ? NO_TOPIC : entityId}`,
    );
  }
  const id = newId("fil");
  const at = stamp(store.now());
  const statement = filingStatement({
    id,
    recordId: args.id,
    entityId,
    rationale: args.reason,
    authorKind: "operator",
    authorId: operator,
    heuristic: Number(current.heuristic) === 1,
    withdrawn: true,
    supersedes: current.id,
    at,
  });
  await store.db.run(statement.sql, statement.params ?? []);
  store.touch();
  return { id, recordId: args.id, entityId, withdrawn: true, supersedes: current.id, at };
}

export interface TellArgs {
  text: string;
  target?: { kind: "record" | "entity" | "run"; id: string } | undefined;
  replyTo?: string | undefined;
}

/**
 * What the operator told Babel, threaded. A reply joins the thread of the row it answers, so a
 * conversation is one root and its sequence rather than a pile of rows sharing a subject; an
 * original is its own root, which is what makes the first thing he said addressable.
 */
export async function tell(store: ActsStore, args: TellArgs, operator: string): Promise<Told> {
  if (operator === "") throw new ActRefused("steering has no author");
  if (args.text.trim() === "")
    throw new ActRefused("steering exists to carry words, and this one has none");
  const id = newId("stg");
  let rootId = id;
  if (args.replyTo !== undefined) {
    const parent = await first<{ root_id: string }>(
      store,
      `SELECT root_id FROM steering WHERE id = ?`,
      [args.replyTo],
    );
    if (parent === null) throw new ActRefused(`no steering ${args.replyTo} to reply to`);
    rootId = parent.root_id;
  }
  const at = stamp(store.now());
  const rows = await store.db.query<{ seq: number }>(
    `INSERT INTO steering(id, root_id, reply_to_id, seq, actor_kind, actor_id, target_kind, target_id,
       text, recorded_at)
     SELECT ?, ?, ?, COALESCE(MAX(seq), 0) + 1, 'operator', ?, ?, ?, ?, ?
     FROM steering WHERE root_id = ?
     RETURNING seq`,
    [
      id,
      rootId,
      args.replyTo ?? null,
      operator,
      args.target?.kind ?? null,
      args.target?.id ?? null,
      args.text,
      at,
      rootId,
    ],
  );
  store.touch();
  return { id, rootId, seq: Number(rows[0]?.seq ?? 1), at };
}

/**
 * Installs an evaluation policy. It is a record and not a scheduler command: nothing here starts
 * compute, raises a budget or launches anything, and the newest row is simply the one in force.
 * A version is written once — re-installing the same version would make a draw taken under it
 * unreplayable — so a change is a new version with the operator's reason beside it.
 */
export async function setPolicy(
  store: ActsStore,
  policy: Policy,
  reason: string,
  operator: string,
  concurrentJobs: number | null,
): Promise<PolicySet> {
  if (operator === "") throw new ActRefused("a policy has no operator");
  const refusal = validateNewPolicy(policy, concurrentJobs);
  if (refusal !== null) throw new ActRefused(refusal);
  const held = await first<{ version: string }>(
    store,
    `SELECT version FROM policies WHERE version = ?`,
    [policy.version],
  );
  if (held !== null) {
    throw new ActRefused(
      `policy version ${JSON.stringify(policy.version)} is already stored; a change is a new version`,
    );
  }
  const at = stamp(store.now());
  const rows = await store.db.query<{ seq: number }>(
    `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
     SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ?, ? FROM policies
     RETURNING seq`,
    [policy.version, operator, reason, JSON.stringify(policy), at],
  );
  store.touch();
  return { version: policy.version, seq: Number(rows[0]?.seq ?? 1), at };
}

/**
 * Sets a BUDGET OVERLAY: the standing policy's batch and ceilings, moved for a stated while and
 * a stated reason, and nothing else about the policy touched (#260).
 *
 * This is the act a drain performs instead of `setPolicy`. It writes no `policies` row, so the
 * version every in-flight assignment id digests is exactly where it was, and it ends by itself:
 * `expires_at` passing is the whole of the unwind. What it refuses is what
 * `validateNewPolicy` refuses about the policy the overlay would produce — a lease that cannot
 * cover the batch above all, which is the refusal the drain talked itself out of five times by
 * raising the lease instead.
 *
 * An overlay does not supersede the live one by editing it: the newest unexpired row is simply
 * the one in force, so setting a second is a second row and clearing it exposes the first again
 * for whatever is left of its own TTL.
 */
export async function setBudget(
  store: ActsStore,
  args: z.infer<typeof SetBudgetInputSchema>,
  operator: string,
  concurrentJobs: number | null,
): Promise<BudgetSet> {
  if (operator === "") throw new ActRefused("an overlay has no operator");
  const expires = Date.parse(args.expiresAt);
  if (!Number.isFinite(expires)) {
    throw new ActRefused(
      `${JSON.stringify(args.expiresAt)} is not an instant an overlay can expire at`,
    );
  }
  const created = store.now();
  const standing = (await coordinator({ db: store.db }, store.now, concurrentJobs).policy(created))
    .standing;
  const overlay: Budget = {
    id: newId("bdg"),
    createdAt: created,
    expiresAt: expires,
    perCycleCost: args.perCycleCost ?? null,
    dailyCost: args.dailyCost ?? null,
    concurrentPerMachine: args.concurrentPerMachine ?? null,
    reason: args.reason,
  };
  const refusal = validateBudget(standing, overlay, concurrentJobs);
  if (refusal !== null) throw new ActRefused(refusal);
  const at = stamp(created);
  await store.db.run(
    `INSERT INTO budgets(id, created_at, expires_at, per_cycle_cost, daily_cost,
                         concurrent_per_machine, reason, cleared_at, cleared_reason)
     VALUES(?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
    [
      overlay.id,
      at,
      stamp(expires),
      overlay.perCycleCost,
      overlay.dailyCost,
      overlay.concurrentPerMachine,
      overlay.reason,
    ],
  );
  store.touch();
  return {
    id: overlay.id,
    expiresAt: stamp(expires),
    at,
    changes: budgetChanges(standing, overlay).map((change) => ({ ...change })),
  };
}

/**
 * Ends an overlay before its expiry, with the reason it was ended. `cleared_at` is written
 * once, from NULL — the guard is the WHERE clause, so clearing twice refuses rather than
 * rewriting when it ended — and an overlay that has already expired is refused for the same
 * reason a finished claim is: there is nothing left to end. The reason is written in the same
 * statement: "the drain finished early" and "the box fell over" are different endings, and a
 * field the door accepted and dropped would make them the same row.
 */
export async function clearBudget(
  store: ActsStore,
  args: z.infer<typeof ClearBudgetInputSchema>,
  operator: string,
): Promise<BudgetCleared> {
  if (operator === "") throw new ActRefused("clearing an overlay has no operator");
  const at = stamp(store.now());
  const rows = await store.db.query<{ id: string }>(
    `UPDATE budgets SET cleared_at = ?, cleared_reason = ?
      WHERE id = ? AND cleared_at IS NULL AND expires_at > ?
      RETURNING id`,
    [at, args.reason, args.id, at],
  );
  const cleared = rows[0];
  if (cleared === undefined) {
    const held = await first<{ cleared_at: string | null; expires_at: string }>(
      store,
      `SELECT cleared_at, expires_at FROM budgets WHERE id = ?`,
      [args.id],
    );
    if (held === null) throw new ActRefused(`no budget overlay ${args.id}`);
    throw new ActRefused(
      held.cleared_at === null
        ? `budget overlay ${args.id} expired at ${held.expires_at}, so the standing policy is already in force`
        : `budget overlay ${args.id} was cleared at ${held.cleared_at}`,
    );
  }
  store.touch();
  return { id: cleared.id, at };
}

// ---------------------------------------------------------------------------- the crossing

/**
 * Every table the crossing may write into, with its columns, derived from the migration itself.
 * Deriving rather than listing is the point: a column list written out here would be a second
 * copy of `SCHEMA_V1` that could come to disagree with it, and the importer would then refuse a
 * column the store actually has. It is computed once and cached.
 */
let importable: Record<string, readonly string[]> | null = null;

export function importableTables(): Record<string, readonly string[]> {
  if (importable !== null) return importable;
  const tables: Record<string, readonly string[]> = {};
  for (const statement of SCHEMA_V1) {
    const head = /^\s*CREATE TABLE\s+(\w+)\s*\(/.exec(statement);
    if (head === null) continue;
    const name = head[1];
    if (name === undefined) continue;
    tables[name] = columnParts(statement.slice(head[0].length)).map((column) => column.name);
  }
  importable = tables;
  return tables;
}

/** One column of a `CREATE TABLE` body: its name, and the whole of what was declared about it. */
interface ColumnPart {
  readonly name: string;
  readonly text: string;
}

/**
 * The columns of one `CREATE TABLE` body: the leading identifier of each top-level part, and the
 * part itself. Callers that want only the names throw the text away; `recordColumns` reads it,
 * because a column's REFERENCES clause is the one place the migration says what a value MEANS.
 *
 * A COMMENT IS SKIPPED RATHER THAN SCANNED, because a comment is prose and prose carries
 * apostrophes. The note inside `runs` — "where this run's job is" — opened a string literal that
 * nothing closed, so the remainder of that table, `unreadable` and `payload`, was read as one
 * quoted run and never became a column. The crossing then refused every `runs` chunk for having
 * no column "payload", which is the column every receipt carries, and the run log could not
 * cross at all.
 *
 * THERE IS ONE PARSER because there was nearly a second: the shape below is fiddly enough that a
 * sibling written to read REFERENCES clauses would have had to repeat the comment and literal
 * handling, and a repeat of that handling is how the apostrophe bug happens twice.
 */
function columnParts(body: string): readonly ColumnPart[] {
  const columns: ColumnPart[] = [];
  let depth = 0;
  let part = "";
  const take = (): void => {
    const leading = /^\s*([a-z_][a-z_0-9]*)/i.exec(part);
    const word = leading?.[1];
    if (word !== undefined && !CONSTRAINT_WORDS[word.toUpperCase()])
      columns.push({ name: word, text: part });
    part = "";
  };
  for (let at = 0; at < body.length; at += 1) {
    const character = body[at];
    if (character === "-" && body[at + 1] === "-") {
      const end = body.indexOf("\n", at + 2);
      if (end === -1) break;
      at = end;
      continue;
    }
    if (character === "/" && body[at + 1] === "*") {
      const end = body.indexOf("*/", at + 2);
      if (end === -1) break;
      at = end + 1;
      continue;
    }
    if (character === "'") {
      // A literal — a CHECK's allowed values, a DEFAULT — is data, not structure: it rides into
      // the part whole so a comma or a bracket inside it cannot end a column.
      const close = body.indexOf("'", at + 1);
      const end = close === -1 ? body.length - 1 : close;
      part += body.slice(at, end + 1);
      at = end;
      continue;
    }
    if (character === "(") depth += 1;
    if (character === ")") {
      if (depth === 0) break;
      depth -= 1;
    }
    if (character === "," && depth === 0) {
      take();
      continue;
    }
    part += character;
  }
  take();
  return columns;
}

/** The words that begin a table constraint rather than a column. */
const CONSTRAINT_WORDS: Record<string, true> = {
  PRIMARY: true,
  UNIQUE: true,
  CHECK: true,
  FOREIGN: true,
  CONSTRAINT: true,
};

/**
 * Which columns of each table hold a HUB MACHINE ID, derived from the migration for the reason
 * `importableTables` is: a list written out here would be a second copy of `SCHEMA_V1`, and the
 * way a second copy comes to disagree is by being SHORT. The crossing's machine check is worth
 * having only if it is exhaustive — a guard over two of the three columns reads as a statement
 * that the third is fine, which is how `runs.machine_id` came to have no test at all and, behind
 * it, a derivation bug that made its arm unreachable for months (#378, #379).
 *
 * The shape is the name: `machine_id`, or a column whose name ends in `host`. Both spellings are
 * already in the store and both are handed to `describe` — `sessions.host` predates the hub's
 * vocabulary, and `run_calls.transcript_host` is the machine holding a transcript, written from
 * the run's own `machineId`. `policies.concurrent_per_machine` is a count of jobs and matches
 * neither, which is the rule earning its shape rather than naming its tables.
 *
 * A COLUMN ADDED TO THE MIGRATION IS GUARDED WITHOUT ANYONE REMEMBERING TO GUARD IT, and one
 * that takes the shape while holding something else — a `repository_host` — is over-guarded
 * rather than under-: it refuses an import instead of admitting a row nothing can read back.
 * Either way the derived map changes, and `store/acts.test.ts` pins it, so the change is a
 * failing test rather than a quiet pass.
 */
let machines: Record<string, readonly string[]> | null = null;

export function machineColumns(): Record<string, readonly string[]> {
  if (machines !== null) return machines;
  const holding: Record<string, readonly string[]> = {};
  for (const [table, columns] of Object.entries(importableTables())) {
    const named = columns.filter((column) => MACHINE_COLUMN.test(column));
    if (named.length > 0) holding[table] = named;
  }
  machines = holding;
  return holding;
}

/** The name of a column holding a hub machine id: `machine_id`, or something's `host`. */
const MACHINE_COLUMN = /^(machine_id|([a-z_]+_)?host)$/;

/**
 * Which columns of each table hold a RECORD IDENTIFIER, derived from the migration for the reason
 * `machineColumns` is: a list written out here would be a second copy of `SCHEMA_V1`, and the way
 * a second copy comes to disagree is by being SHORT.
 *
 * THE DEFECT THIS EXISTS FOR (#414): `records.id` carries no CHECK, and the crossing validated
 * the table name and the column names against the migration and then inserted whatever values it
 * was handed. Every reading door takes `RecordIdSchema`, so a row whose id did not match it was
 * listed by the feed — title, kind, age, five acts offered — and refused by the `record` door
 * when opened. `records_kept` refuses DELETE below the doors, so such a row is PERMANENT: there
 * is no repair after the fact, which is what makes this a guard on the way in rather than a
 * message on the way out.
 *
 * A column holds a record id if the migration says so with `REFERENCES records(id)`, or if its
 * name is one the frontier only ever spells with one — ten tables, checked against the migration
 * rather than assumed. `filings.supersedes_id` and `facts.supersedes_id` reference their OWN
 * tables and are correctly absent: the name family is the columns whose meaning is fixed, and
 * the REFERENCES clause carries the rest.
 */
let records: Record<string, readonly string[]> | null = null;

export function recordColumns(): Record<string, readonly string[]> {
  if (records !== null) return records;
  const holding: Record<string, readonly string[]> = {};
  for (const statement of SCHEMA_V1) {
    const head = /^\s*CREATE TABLE\s+(\w+)\s*\(/.exec(statement);
    const table = head?.[1];
    if (head === null || table === undefined) continue;
    const named = columnParts(statement.slice(head[0].length))
      .filter(
        (column) =>
          RECORD_COLUMN.test(column.name) ||
          RECORD_REFERENCE.test(column.text) ||
          (table === "records" && column.name === "id"),
      )
      .map((column) => column.name);
    if (named.length > 0) holding[table] = named;
  }
  records = holding;
  return holding;
}

/** The name of a column the frontier only ever writes a record identifier into. */
const RECORD_COLUMN = /^(record_id|root_id|parent_id|revision_id|duplicate_of_id)$/;

/** The migration saying outright that a column holds one. */
const RECORD_REFERENCE = /REFERENCES\s+records\s*\(\s*id\s*\)/i;

/**
 * `edges` is the one table whose identifier columns are POLYMORPHIC: `from_id` holds a record or
 * an entity, and the row's own `from_kind` says which. Guarding them by name would refuse every
 * entity edge; not guarding them would admit an edge pointing at a record that cannot exist. So
 * the check reads the kind beside the id, which is exact rather than over- or under-guarded.
 */
const EDGE_ENDS: readonly (readonly [kind: string, id: string])[] = [
  ["from_kind", "from_id"],
  ["to_kind", "to_id"],
];

/** How many row statements ride in one batch, under the engine's 256-statement bound. */
const IMPORT_BATCH = 200;

export interface ImportChunk {
  source: string;
  table: string;
  rows: readonly Readonly<Record<string, string | number | null>>[];
}

/**
 * The one-off crossing's write path: rows in the store's own shapes, straight into the table
 * they name, idempotent by primary key so a chunk that was already delivered inserts nothing and
 * reports it. Identifiers in the statement come from the migration's own column list and never
 * from the payload, which is what makes a table name in a request data rather than SQL.
 *
 * `INSERT OR IGNORE` is the whole of the idempotence, and it is also why nothing here fights the
 * append-only triggers: a row already held is skipped rather than updated.
 *
 * A RECORD IDENTIFIER IS CHECKED BEFORE ANY OF IT IS WRITTEN (#414), against the same schema
 * every reading door takes. The chunk is refused whole rather than row by row: a partial import
 * of an append-only table cannot be undone, and `records_kept` refuses the DELETE that would be
 * the repair, so "some of it went in" is a worse answer than "none of it did".
 */
export async function importLedger(store: ActsStore, chunk: ImportChunk): Promise<Imported> {
  const columns = importableTables()[chunk.table];
  if (columns === undefined)
    throw new ActRefused(`the store holds no table named ${JSON.stringify(chunk.table)}`);
  const known: Record<string, true> = {};
  for (const column of columns) known[column] = true;
  const statements: SqlStatement[] = [];
  for (const row of chunk.rows) {
    const keys = Object.keys(row);
    if (keys.length === 0) throw new ActRefused(`a row for ${chunk.table} carries no columns`);
    for (const key of keys) {
      if (!known[key]) throw new ActRefused(`${chunk.table} has no column ${JSON.stringify(key)}`);
    }
    for (const column of recordColumns()[chunk.table] ?? []) {
      const value = row[column];
      if (value === undefined || value === null) continue;
      if (!RecordIdSchema.safeParse(value).success) {
        throw new ActRefused(
          `${chunk.table}.${column} holds ${JSON.stringify(value)}, which no record can be ` +
            `named: a record id is a family and a hex tail, and every door that reads one ` +
            `refuses this. Nothing was imported from this chunk.`,
        );
      }
    }
    if (chunk.table === "edges") {
      for (const [kind, id] of EDGE_ENDS) {
        const value = row[id];
        if (row[kind] !== "record" || value === undefined || value === null) continue;
        if (!RecordIdSchema.safeParse(value).success) {
          throw new ActRefused(
            `edges.${id} holds ${JSON.stringify(value)} with ${kind} "record", which no record ` +
              `can be named. Nothing was imported from this chunk.`,
          );
        }
      }
    }
    statements.push({
      sql: `INSERT OR IGNORE INTO ${chunk.table}(${keys.join(", ")})
            VALUES(${keys.map(() => "?").join(", ")}) RETURNING rowid`,
      params: keys.map((key) => row[key] ?? null),
    });
  }
  let inserted = 0;
  for (let at = 0; at < statements.length; at += IMPORT_BATCH) {
    const results = await store.db.batch(statements.slice(at, at + IMPORT_BATCH));
    for (const result of results) inserted += result.length;
  }
  await store.db.run(
    `INSERT INTO imports(id, source, table_name, rows, imported_at) VALUES(?, ?, ?, ?, ?)`,
    [newId("imp"), chunk.source, chunk.table, inserted, stamp(store.now())],
  );
  store.touch();
  return {
    source: chunk.source,
    table: chunk.table,
    inserted,
    skipped: chunk.rows.length - inserted,
  };
}

/**
 * Re-host a catalogued corpus: every session row carrying one `host` value takes another (#310).
 *
 * THE VALUE IT LEAVES IS A MACHINE ID THE HUB HAS JUST DESCRIBED — the door checks that before
 * calling here, because the defect being repaired is a `host` no machine answers to, and writing
 * a second one would be the same defect spelled differently. Nothing is inferred: a name cannot
 * be resolved (the hub resolves none, and no door a plugin is served lists machines), so the
 * operator states the mapping and this writes exactly that.
 *
 * A no-op is reported rather than refused. `sessions: 0` is the truthful answer for a `from`
 * nothing was catalogued under, and it is what makes the act idempotent: running it twice moves
 * the rows once and says so the second time.
 */
export async function rehostSessions(
  store: ActsStore,
  move: { from: string; to: string },
): Promise<SessionsRehosted> {
  if (move.from === move.to) {
    throw new ActRefused(
      "a re-host needs two different hosts; this one names the same value twice",
    );
  }
  const [counted] = await store.db.query<{ sessions: number | bigint }>(
    `SELECT COUNT(*) AS sessions FROM sessions WHERE host = ?`,
    [move.from],
  );
  const sessions = Number(counted?.sessions ?? 0);
  if (sessions > 0) {
    await store.db.run(`UPDATE sessions SET host = ? WHERE host = ?`, [move.to, move.from]);
    store.touch();
  }
  return { from: move.from, to: move.to, sessions };
}
