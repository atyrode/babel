import { z } from "zod";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import { INTEREST_STATES, type Ruling } from "../contract.ts";
import { SCHEMA_V1 } from "./schema.ts";
import {
  PolicySchema,
  validateNewPolicy,
  type Policy,
} from "./coordinator.ts";
export { DEFAULT_POLICY, leaseFloor, PolicySchema, validateNewPolicy, type Policy } from "./coordinator.ts";

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
  per file (internal/reality/store_topic.go's ApplyTopicPlan says so at length). Here the plugin
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
export type Standing = "new" | "accepted" | "rejected" | "deferred" | "duplicate" | "refine-requested";

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
 * A stance as the ledger records it (§4.13, ported from internal/reality/interest.go): not a
 * preference column but §4.8's lifecycle and analysis-policy facts, so a paused project is
 * paused everywhere Babel looks. An empty lifecycle means "leave it alone", which is what
 * `excluded` does — a repository can be withheld from analysis and still be actively worked on,
 * and writing a lifecycle value would make the exclusion claim something it does not know.
 */
export type InterestState = (typeof INTEREST_STATES)[number];

export const INTEREST_FACTS: Record<InterestState, { readonly lifecycle: string; readonly policy: string }> = {
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
});
export type Ruled = z.infer<typeof RuledSchema>;

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

export const ImportedSchema = z.strictObject({
  source: z.string(),
  table: z.string(),
  inserted: z.number().int(),
  skipped: z.number().int(),
});
export type Imported = z.infer<typeof ImportedSchema>;

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
  const byId = await first<{ canonical_id: string }>(store, `SELECT canonical_id FROM entities WHERE id = ?`, [
    reference,
  ]);
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

interface FilingRow extends SqlRow {
  id: string;
  rationale: string;
  heuristic: number;
  withdrawn: number;
}

/**
 * The row that currently answers for one record and one topic, filing or withdrawal alike.
 *
 * The tie-break is `rowid` and not the identifier: two filings written in the same millisecond
 * are ordered by the order they were inserted, which is what "the newest" means, whereas a
 * random identifier would make re-filing a withdrawal a coin flip. Time still leads, so an
 * imported history reads in its own order however it arrived.
 */
async function newestFiling(store: ActsStore, recordId: string, entityId: string): Promise<FilingRow | null> {
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
      params: [factId, entityId, predicate, value, at, at, operator, confidence, note, prior?.id ?? null, at],
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
  const applied: Application = { entityId: "", resolutionId: "", factId: "", filed: [], settled: [] };
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
const TOPIC_OPERATIONS: Record<string, true> = { create: true, split: true, merge: true, retire: true };

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
        throw new ActRefused(`the identity this plan would bind is already bound by entity ${bound.entity_id}`);
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
    if (identity !== "") aliases.set(`identifier:${aliasKey(identity)}`, { kind: "identifier", value: identity });
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
          params: [factId, entityId, fact.predicate, fact.value, at, at, operator, fact.note ?? null, at],
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
  if (settles === undefined) throw new ActRefused(`backlog operation ${JSON.stringify(plan.operation)}`);
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
  if (reason.trim() === "") throw new ActRefused("a declined plan keeps the operator's reason, and this one is empty");
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
 * lands. The one thing that is reported rather than raised is a plan the ledger has moved past:
 * the ruling is the operator's answer to the proposal and stands on its own, so it is written
 * and the plan's refusal travels back beside it.
 */
export async function rule(store: ActsStore, args: RuleArgs, operator: string): Promise<Ruled> {
  if (operator === "") throw new ActRefused("a ruling has no operator");
  const kind = await recordKind(store, args.id);
  if (args.ruling === "reopen") {
    if (args.note.trim() === "") throw new ActRefused("a reopen states no reason for reopening");
    if (args.duplicateOf !== undefined) throw new ActRefused("a reopen names no original");
  }
  if (args.ruling === "duplicate") {
    if (args.duplicateOf === undefined) throw new ActRefused("a duplicate ruling names no original");
    const original = await recordKind(store, args.duplicateOf);
    if (original !== kind) {
      throw new ActRefused(`record ${args.id} is a ${kind} and the original it duplicates is a ${original}`);
    }
  } else if (args.ruling !== "reopen" && args.duplicateOf !== undefined) {
    throw new ActRefused(`a ${args.ruling} names no original`);
  }
  const standing = standingOf(
    (await first<{ disposition: string }>(
      store,
      `SELECT disposition FROM dispositions WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
      [args.id],
    ))?.disposition ?? null,
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
  const results = await store.db.batch(statements);
  const seq = Number(results[0]?.[0]?.["seq"] ?? 0);
  store.touch();
  return { id: args.id, standing: STANDING_OF[args.ruling], seq, plan: outcome };
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
export async function comment(store: ActsStore, args: CommentArgs, operator: string): Promise<Commented> {
  if (operator === "") throw new ActRefused("a comment has no author");
  if (args.text.trim() === "") throw new ActRefused("a comment exists to carry words, and this one has none");
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
export async function answer(store: ActsStore, args: AnswerArgs, operator: string): Promise<Answered> {
  if (operator === "") throw new ActRefused("an answer has no author");
  const question = await first<{ id: string }>(store, `SELECT id FROM questions WHERE id = ?`, [args.id]);
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
export async function interest(store: ActsStore, args: InterestArgs, operator: string): Promise<Interested> {
  if (operator === "") throw new ActRefused("an interest has no operator");
  // Indexed through the contract's own vocabulary: a fifth stance added there fails to compile
  // here until this table says what facts record it, and a stance the store never heard of is
  // still refused at runtime rather than silently writing nothing.
  const mapped = INTEREST_FACTS[args.state as InterestState] as
    | { readonly lifecycle: string; readonly policy: string }
    | undefined;
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
  if (current === null || current.withdrawn === 1) {
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
    heuristic: current.heuristic === 1,
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
  if (args.text.trim() === "") throw new ActRefused("steering exists to carry words, and this one has none");
  const id = newId("stg");
  let rootId = id;
  if (args.replyTo !== undefined) {
    const parent = await first<{ root_id: string }>(store, `SELECT root_id FROM steering WHERE id = ?`, [
      args.replyTo,
    ]);
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
): Promise<PolicySet> {
  if (operator === "") throw new ActRefused("a policy has no operator");
  const refusal = validateNewPolicy(policy);
  if (refusal !== null) throw new ActRefused(refusal);
  const held = await first<{ version: string }>(store, `SELECT version FROM policies WHERE version = ?`, [
    policy.version,
  ]);
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
    tables[name] = columnNames(statement.slice(head[0].length));
  }
  importable = tables;
  return tables;
}

/** The column names of one `CREATE TABLE` body: the leading identifier of each top-level part. */
function columnNames(body: string): readonly string[] {
  const columns: string[] = [];
  let depth = 0;
  let quoted = false;
  let part = "";
  const take = (): void => {
    const leading = /^\s*([a-z_][a-z_0-9]*)/i.exec(part);
    const word = leading?.[1];
    if (word !== undefined && !CONSTRAINT_WORDS[word.toUpperCase()]) columns.push(word);
    part = "";
  };
  for (const character of body) {
    if (quoted) {
      quoted = character !== "'";
      part += character;
      continue;
    }
    if (character === "'") {
      quoted = true;
      part += character;
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
 */
export async function importLedger(store: ActsStore, chunk: ImportChunk): Promise<Imported> {
  const columns = importableTables()[chunk.table];
  if (columns === undefined) throw new ActRefused(`the store holds no table named ${JSON.stringify(chunk.table)}`);
  const known: Record<string, true> = {};
  for (const column of columns) known[column] = true;
  const statements: SqlStatement[] = [];
  for (const row of chunk.rows) {
    const keys = Object.keys(row);
    if (keys.length === 0) throw new ActRefused(`a row for ${chunk.table} carries no columns`);
    for (const key of keys) {
      if (!known[key]) throw new ActRefused(`${chunk.table} has no column ${JSON.stringify(key)}`);
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
  return { source: chunk.source, table: chunk.table, inserted, skipped: chunk.rows.length - inserted };
}
