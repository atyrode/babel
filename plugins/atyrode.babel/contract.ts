import { z } from "zod";

/*
  THE VOCABULARY OF THE atyrode.babel PLUGIN FAMILY, spelled once. Every id, door name, event
  kind, panel id and machine operation the halves use is a constant here; a manifest is JSON and
  repeats its own id as data, and `test/contract.test.ts` pins each manifest to these constants
  so the two can never disagree. The kit inlines this module into every bundle that imports it.

  The family, per `docs/manifold-plan.md` §2:
  - `atyrode.babel` is the baseline: the store (ADR 0034's plugin database), every door, the
    machine half's declared operations and their schedules, the feed index. It serves no panel.
  - `atyrode.babel.feed` is the reading surface: Home, a record, a topic — SPEC §8.7 and §4.13.
  - `atyrode.babel.watch` is the control room: runs in flight, presets, recipes, ceilings — §8.3.
  A sub-plugin depends on the baseline (`dependencies` in its manifest) and reads only through
  the baseline's doors; it holds no storage of its own.
 */

// ---------------------------------------------------------------------------- plugin ids

export const BABEL_PLUGIN_ID = "atyrode.babel";
export const FEED_PLUGIN_ID = "atyrode.babel.feed";
export const WATCH_PLUGIN_ID = "atyrode.babel.watch";

// ---------------------------------------------------------------------------- shared vocabulary

/** The record kinds that are posts (§4.13: observations are evidence, never rows). */
export const POST_KINDS = ["hypothesis", "finding", "proposal", "question"] as const;
export const PostKindSchema = z.enum(POST_KINDS);
export type PostKind = z.infer<typeof PostKindSchema>;

export const RECORD_KINDS = ["hypothesis", "observation", "finding", "proposal"] as const;
export const RecordKindSchema = z.enum(RECORD_KINDS);
export type RecordKind = z.infer<typeof RecordKindSchema>;

/** §8.7's sorts. `next` is what needs the operator, most urgent first. */
export const FEED_SORTS = ["next", "hot", "new", "top", "controversial", "rising"] as const;
export const FeedSortSchema = z.enum(FEED_SORTS);
export type FeedSort = z.infer<typeof FeedSortSchema>;

export const FEED_WINDOWS = ["hour", "day", "week", "month", "year", "all"] as const;
export const FeedWindowSchema = z.enum(FEED_WINDOWS);
export type FeedWindow = z.infer<typeof FeedWindowSchema>;

/** The operator's rulings (§4.7) — every one appends, none edits. */
export const RULINGS = ["accept", "reject", "defer", "duplicate", "reopen", "refine"] as const;
export const RulingSchema = z.enum(RULINGS);
export type Ruling = z.infer<typeof RulingSchema>;

/** A reviewer's vote (§4.12). */
export const VOTES = ["support", "oppose", "unsure"] as const;
export const VoteSchema = z.enum(VOTES);

/** The review roles a run may take; `filing` and `backlog` are §4.13's own lanes. */
export const ROLES = [
  "reception",
  "evidence",
  "challenge",
  "comparison",
  "outcome",
  "relevance",
  "filing",
  "backlog",
] as const;
export const RoleSchema = z.enum(ROLES);

/** The operator's stance toward a topic (§4.13): a fact about him, the one direct act. */
export const INTEREST_STATES = ["working", "watching", "not-now", "excluded"] as const;
export const InterestStateSchema = z.enum(INTEREST_STATES);

/** A record identifier as the frontier mints them: a three-letter family and a hex tail. */
export const RecordIdSchema = z.string().regex(/^(hyp|obs|fnd|pro|que)_[0-9a-f]{8,64}$/);
export const EntityIdSchema = z.string().regex(/^ent_[0-9a-f]{8,64}$/);

const bounded = (max: number) => z.string().trim().min(1).max(max);

// ---------------------------------------------------------------------------- doors (baseline)

/** LOCAL action names, as `defineServerAction` takes them; the roster prefixes the plugin id. */
export const ACTIONS = {
  // reading
  feed: "feed",
  record: "record",
  thread: "thread",
  topics: "topics",
  topic: "topic",
  pulse: "pulse",
  runs: "runs",
  run: "run",
  policy: "policy",
  // the operator's acts
  rule: "rule",
  comment: "comment",
  answer: "answer",
  interest: "interest",
  file: "file",
  unfile: "unfile",
  tell: "tell",
  setPolicy: "setPolicy",
  /** The two acts of #260: a bounded exception to the standing policy, and its early end. */
  setBudget: "setBudget",
  clearBudget: "clearBudget",
  launch: "launch",
  launchPreview: "launchPreview",
  stop: "stop",
  /**
   * The two doors #279 adds. `accounts` is a dry read of the accounts the machine's broker has
   * observed, so the Start panel can offer the operator one to spend rather than ask him to
   * type an identity key; `setupInference` installs or refreshes the `atyrode.babel.inference`
   * policy on a machine, so the owner is not asked to hand-write the JSON of a service whose
   * shape is Babel's own.
   */
  accounts: "accounts",
  setupInference: "setupInference",
  // the crossing (owner only)
  importLedger: "importLedger",
} as const;
export type ActionName = (typeof ACTIONS)[keyof typeof ACTIONS];

/** FULL door names, as `host.action` and a button's `action` spell them. */
export function door(action: ActionName): `${typeof BABEL_PLUGIN_ID}.${ActionName}` {
  return `${BABEL_PLUGIN_ID}.${action}`;
}

// ---------------------------------------------------------------------------- the feed

export const FeedQuerySchema = z.strictObject({
  sort: FeedSortSchema.default("next"),
  window: FeedWindowSchema.default("day"),
  kinds: z.array(PostKindSchema).max(POST_KINDS.length).default([]),
  /** `me` narrows to what awaits the operator: a ruling or an answer. */
  needs: z.enum(["me", "all"]).default("me"),
  /** A topic by entity id or name; `unfiled` is the records under nothing. */
  topic: z.string().max(200).optional(),
  limit: z.number().int().min(1).max(100).default(25),
  offset: z.number().int().min(0).default(0),
});
export type FeedQuery = z.infer<typeof FeedQuerySchema>;

/** One reviewer's vote as the row's strip shows it. */
export const FeedVoteSchema = z.strictObject({ role: RoleSchema.or(z.literal("")), vote: VoteSchema });

export const FeedPostSchema = z.strictObject({
  id: z.string(),
  kind: PostKindSchema,
  title: z.string(),
  standing: z.string(),
  createdAt: z.string(),
  author: z.strictObject({ runId: z.string() }).nullable(),
  topics: z.array(z.strictObject({ id: EntityIdSchema, name: z.string() })),
  score: z.number().int(),
  support: z.number().int(),
  oppose: z.number().int(),
  unsure: z.number().int(),
  votes: z.array(FeedVoteSchema),
  contested: z.boolean(),
  reviewing: z.boolean(),
  comments: z.number().int(),
  awaiting: z.boolean(),
  why: z.string(),
  lastActivityAt: z.string(),
});
export type FeedPost = z.infer<typeof FeedPostSchema>;

export const FeedResultSchema = z.strictObject({
  posts: z.array(FeedPostSchema),
  total: z.number().int(),
  builtAt: z.string(),
  notice: z.string(),
});
export type FeedResult = z.infer<typeof FeedResultSchema>;

// ---------------------------------------------------------------------------- the record

export const RecordQuerySchema = z.strictObject({ id: RecordIdSchema });

/** The peel (§8.6): five depths, the first three free of identifiers. */
export const RecordPeelSchema = z.strictObject({
  post: FeedPostSchema,
  claim: z.strictObject({ statement: z.string(), standing: z.string(), act: z.string() }),
  case: z.record(z.string(), z.union([z.string(), z.array(z.string())])),
  evidence: z.array(
    z.strictObject({
      excerpt: z.string(),
      speaker: z.string(),
      session: z.strictObject({ selector: z.string(), title: z.string(), href: z.string() }).nullable(),
      note: z.string(),
      line: z.number().int().nullable(),
    }),
  ),
  reception: z.strictObject({
    byRole: z.array(z.strictObject({ role: RoleSchema, support: z.number().int(), oppose: z.number().int(), unsure: z.number().int(), opposingRationales: z.array(z.string()) })),
    contested: z.boolean(),
    operatorHistory: z.array(z.strictObject({ stance: z.string(), reason: z.string(), at: z.string() })),
  }),
  machinery: z.record(z.string(), z.string()),
  related: z.array(z.strictObject({ relation: z.string(), id: z.string(), kind: RecordKindSchema, title: z.string() })),
  plan: z.strictObject({ kind: z.enum(["topic", "backlog"]), operation: z.string(), state: z.string() }).nullable(),
});
export type RecordPeel = z.infer<typeof RecordPeelSchema>;

// ---------------------------------------------------------------------------- the thread

/** `thread` takes the record whose conversation is wanted; the same identifier `record` takes. */
export const ThreadQuerySchema = z.strictObject({ id: RecordIdSchema });

export const CommentSchema: z.ZodType<Comment> = z.lazy(() =>
  z.strictObject({
    id: z.string(),
    kind: z.enum(["comment", "question", "contribution", "refinement", "answer", "reconsideration"]),
    author: z.strictObject({ kind: z.enum(["run", "operator"]), id: z.string() }),
    role: z.string(),
    text: z.string(),
    at: z.string(),
    relatedId: z.string(),
    replies: z.array(CommentSchema),
  }),
);
export interface Comment {
  id: string;
  kind: "comment" | "question" | "contribution" | "refinement" | "answer" | "reconsideration";
  author: { kind: "run" | "operator"; id: string };
  role: string;
  text: string;
  at: string;
  relatedId: string;
  replies: Comment[];
}

export const ActSchema = z.strictObject({
  id: z.string(),
  act: RulingSchema,
  by: z.string(),
  at: z.string(),
  reason: z.string(),
});

export const ThreadResultSchema = z.strictObject({
  comments: z.array(CommentSchema),
  acts: z.array(ActSchema),
  total: z.number().int(),
});

// ---------------------------------------------------------------------------- topics

/** `topic` takes an entity id, a topic name, or the reserved `unfiled`. */
export const TopicQuerySchema = z.strictObject({ topic: z.string().trim().min(1).max(200) });

export const TopicRowSchema = z.strictObject({
  id: EntityIdSchema,
  name: z.string(),
  kind: z.string(),
  binding: z
    .strictObject({ kind: z.string(), identity: z.string(), remote: z.string(), paths: z.array(z.string()) })
    .nullable(),
  posts: z.number().int(),
  awaiting: z.number().int(),
  latestAt: z.string(),
  interest: z.strictObject({ state: InterestStateSchema.or(z.literal("")), reason: z.string(), at: z.string(), by: z.string() }),
});

export const TopicProposalSchema = z.strictObject({
  proposalId: RecordIdSchema,
  title: z.string(),
  name: z.string(),
  kind: z.string(),
  operation: z.enum(["create", "split", "merge", "retire"]),
  targets: z.array(z.strictObject({ id: EntityIdSchema, name: z.string() })),
  runId: z.string(),
  posts: z.number().int(),
  why: z.string(),
});

export const TopicsResultSchema = z.strictObject({
  topics: z.array(TopicRowSchema),
  proposed: z.array(TopicProposalSchema),
  unfiled: z.number().int(),
});

/**
 * One topic, its open proposals and its own feed, in one answer, because they are one decision:
 * a reader on a topic page is choosing between what is filed under it, what Babel proposes to do
 * to it, and where he stands toward it, and a page that had to ask three times would let the
 * three disagree about what exists.
 */
export const TopicResultSchema = z.strictObject({
  topic: TopicRowSchema.nullable(),
  proposed: z.array(TopicProposalSchema),
  feed: FeedResultSchema,
});

// ---------------------------------------------------------------------------- the operator's acts

export const RuleInputSchema = z.strictObject({
  id: RecordIdSchema,
  ruling: RulingSchema,
  note: z.string().max(4000).default(""),
  duplicateOf: RecordIdSchema.optional(),
});

export const RuleResultSchema = z.strictObject({
  id: RecordIdSchema,
  standing: z.string(),
  seq: z.number().int(),
  /** What the ruling applied on the ledger, when the proposal carried a plan. */
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

export const CommentInputSchema = z.strictObject({
  id: RecordIdSchema,
  text: bounded(8000),
  kind: z.enum(["comment", "question"]).default("comment"),
  relatedId: z.string().optional(),
});

export const AnswerInputSchema = z.strictObject({
  id: RecordIdSchema,
  outcome: z.enum(["answered", "unknown", "declined"]),
  text: z.string().max(8000).default(""),
});

export const InterestInputSchema = z.strictObject({
  entityId: EntityIdSchema,
  state: InterestStateSchema,
  reason: z.string().max(2000).default(""),
});

export const FileInputSchema = z.strictObject({
  id: RecordIdSchema,
  entity: bounded(200),
  rationale: bounded(2000),
});

export const UnfileInputSchema = z.strictObject({
  id: RecordIdSchema,
  entity: bounded(200),
  reason: bounded(2000),
});

export const TellInputSchema = z.strictObject({
  text: bounded(8000),
  target: z.strictObject({ kind: z.enum(["record", "entity", "run"]), id: z.string() }).optional(),
  replyTo: z.string().optional(),
});

// ---------------------------------------------------------------------------- the pulse

export const PulseResultSchema = z.strictObject({
  since: z.string(),
  today: z.strictObject({
    sessionsRead: z.number().int(),
    records: z.number().int(),
    votes: z.number().int(),
    proposals: z.number().int(),
    topicProposals: z.number().int(),
    ruled: z.number().int(),
  }),
  reviewing: z.array(z.strictObject({ id: z.string(), kind: z.string(), title: z.string(), since: z.string() })),
});

// ---------------------------------------------------------------------------- machine operations

/**
 * The operations the baseline declares on a machine (plan §4); each is one job.
 *
 * THE IDS ARE NAMESPACED because the engine requires it: `engine.jobs.install` refuses a machine
 * half whose operation or location keys are not prefixed with the plugin's own id
 * (`unqualified_declaration`), so a bare `scan` is a declaration no hub would ever install.
 */
export const OPERATIONS = {
  scan: `${BABEL_PLUGIN_ID}.scan`,
  archive: `${BABEL_PLUGIN_ID}.archive`,
  prepare: `${BABEL_PLUGIN_ID}.prepare`,
  explore: `${BABEL_PLUGIN_ID}.explore`,
  evaluate: `${BABEL_PLUGIN_ID}.evaluate`,
} as const;
export type OperationName = (typeof OPERATIONS)[keyof typeof OPERATIONS];

/**
 * The word the machine half's CLI takes and the receipt records — the KEY of the table above.
 * A binary's verb is `scan`, not `atyrode.babel.scan`: the namespace exists so a hub can tell
 * two plugins' operations apart, and there is only ever one plugin inside that binary.
 */
export type OperationWord = keyof typeof OPERATIONS;

/**
 * A NODE THE ENGINE ADDRESSES, as a door's caller posts it (ADR 0035).
 *
 * A governed capability — `machines:run`, `jobs:cancel` — is never held over a workspace: it is
 * held at one node, and the door declares `requirements: [{cap, target}]` naming where in its
 * OWN ARGUMENTS the node is. The host walks that path through the raw arguments before the
 * handler runs, so the reference has to travel as a structured `ManifoldRef` — a machine id and
 * an operation name in two separate fields is a pair the evaluator cannot ask a question about.
 *
 * These two are `ManifoldRefSchema`'s `operation` and `job` members, restated here rather than
 * imported, because a plugin's contract may not depend on the engine's protocol package: the
 * shape is a wire shape, and this file is where Babel's wire shapes are spelled.
 */
const refId = z.string().min(1).max(128);
export const OperationRefSchema = z.strictObject({
  kind: z.literal("operation"),
  machineId: refId,
  operationId: refId,
});
export type OperationRef = z.infer<typeof OperationRefSchema>;

export const JobRefSchema = z.strictObject({
  kind: z.literal("job"),
  machineId: refId,
  operationId: refId,
  jobId: refId,
});
export type JobRef = z.infer<typeof JobRefSchema>;

// ---------------------------------------------------------------------------- events

export const EVENTS = {
  recordWritten: "record_written",
  ruled: "ruled",
  assessed: "assessed",
  planApplied: "plan_applied",
  runChanged: "run_changed",
} as const;

// ---------------------------------------------------------------------------- panels

export const PANELS = {
  home: "home",
  record: "record",
  topic: "topic",
  watch: "watch",
} as const;

// ---------------------------------------------------------------------------- the crossing

/** The one-off import's door: owner-only, chunked, idempotent by (table, id). */
export const ImportChunkSchema = z.strictObject({
  source: bounded(200),
  table: bounded(64),
  rows: z.array(z.record(z.string(), z.union([z.string(), z.number(), z.null()]))).min(1).max(500),
});

// ------------------------------------------------------------------------- the model session

/*
  WHO ANSWERS A RUN, AND WHAT IT COSTS — the vocabulary #279 replaced a Code profile reference
  with.

  Until 2026-09-13 an `explore` or an `evaluate` named `analysis@3`, and what was behind it —
  the model, the account, the price — was `code engine`'s to resolve and to report back. Code's
  engine no longer exists (atyrode/code#153), so Babel's own job launches `omp --mode rpc` and
  reaches a model ONLY through the `atyrode.babel.inference` service binding, whose runtime is
  omp's own gateway. Three consequences shape everything below:

  - The choice is the OPERATOR's and travels with the request. There is no profile to resolve,
    so a run states its model, its thinking level and the account it spends, and the job carries
    them as `models`/`config`/`accountPool` for the owner to materialize.
  - The price is the OWNER's. `prices.models` in the installed policy is what a call is metered
    at, in integer micro-dollars per million tokens, and Babel restates it here rather than
    inventing one: a ceiling in money without a price is not a ceiling.
  - The model reference is FULLY QUALIFIED — `anthropic/claude-sonnet-4-5`, provider and all —
    because that is the string omp's gateway keys its model map by, the `modelId` the metered
    proxy reads off the request body, and therefore the key a price is looked up under. A bare
    model id misses in all three places at once.
*/

/**
 * A model's price as the owner's policy states it: integer micro-dollars per million tokens, so
 * $3.00 per million input tokens is `3000000`. Restated rather than imported from the hub's
 * protocol because Watch reads it out of `launchPreview` rather than out of a policy, and a
 * machine half compiles without the hub's package at all.
 */
export const ModelPriceSchema = z.strictObject({
  inputPerMillion: z.number().int().min(0),
  outputPerMillion: z.number().int().min(0),
  cachedInputPerMillion: z.number().int().min(0).optional(),
});
export type ModelPrice = z.infer<typeof ModelPriceSchema>;

/**
 * The thinking levels Babel offers. omp's own enum is wider (`minimal` … `max`); these four are
 * the ones an analysis run is worth asking at, and the one that is absent is the honest shape of
 * "whatever the model does by default" rather than a level nobody chose.
 */
export const THINKING_LEVELS = ["low", "medium", "high", "xhigh"] as const;
export const ThinkingSchema = z.enum(THINKING_LEVELS);
export type Thinking = z.infer<typeof ThinkingSchema>;

/**
 * The fully qualified model reference omp routes by: `<provider>/<model>`, matching
 * manifold-omp's own `modelReference` (`plugins/api/index.ts`), because the string Babel writes
 * into `models.yml` has to be one omp accepts unchanged.
 */
export const MODEL_REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._-]*\/[A-Za-z0-9][A-Za-z0-9._/:-]{0,255}$/;

/**
 * WHAT A RUN IS ASKED TO BE: one model, one thinking level, one account to spend.
 *
 * `account` is one slot of manifold-omp's `RuntimeAccountPool` (`plugins/api/contracts.ts`) and
 * carries every field its broker verifies a pool against — the provider it belongs to, the
 * observation scope it was seen in, the credential row and the identity. None of them is a
 * secret: a credential id and an identity key NAME a credential the machine's broker holds and
 * resolves, which is exactly why a job may carry them and never a bearer.
 *
 * TWO FIELDS ARE SPELLED AS STRINGS HERE AND ARE NOT STRINGS ON THE WIRE, deliberately. In the
 * pool, `credentialId` is a positive INTEGER and `identityKey` is `string | null`
 * (`RuntimeAccountPoolSchema`). This is a DOOR surface: it is posted by a `<select>` whose every
 * value is a string and carried through a job input record, so it takes the decimal digits and
 * `server/plan.ts` `sessionInputs` converts once, at the one place the pool is built. The regex
 * is what makes that conversion total — a credential id that cannot be a positive integer is
 * refused by the door rather than by a gateway that answers `gateway_unavailable` and says no
 * more. An EMPTY `identityKey` is the api-key case, where the broker's own reference is the
 * credential row and there is no OAuth identity; it becomes `null` in the pool.
 */
export const SessionChoiceSchema = z.strictObject({
  model: z.string().trim().min(1).max(256).regex(MODEL_REFERENCE),
  thinking: ThinkingSchema.optional(),
  account: z.strictObject({
    provider: bounded(128),
    scope: z.string().trim().min(1).max(1024),
    credentialId: z.string().regex(/^[1-9][0-9]{0,14}$/),
    identityKey: z.string().trim().max(1024),
  }),
});
export type SessionChoice = z.infer<typeof SessionChoiceSchema>;

/**
 * THE FOUR STATES AN OPERATOR ACTS DIFFERENTLY ON, plus the one that is not a state at all.
 *
 * `missing`: the machine's owner has installed no `atyrode.babel.inference` policy, so there is
 * no lane to a model and the button does nothing but refuse. `unpriced`: the policy is there and
 * prices no such model, so a cost ceiling refuses the run `service_price_unknown` before its
 * first call. `priced`: the price and the ceiling are both facts and the preview states them.
 *
 * `unsupported`: the owner DID try and THIS HUB refused the policy, because it does not know
 * the `pi-native-usage` meter kind omp's wire needs (manifold#570, landing as #572). It is a
 * fourth state and not a shade of `missing` because the act is different: nobody installs
 * anything until the hub moves, and the same hub also refuses to deploy Babel's machine half at
 * all (`service_definition_changed` over every operation's bindings, hub-side) — so the
 * sentence an operator needs is "this hub is older than this plugin", not "install a policy".
 *
 * `unreadable` is the answer that is deliberately not a state:
 * `services.readConfiguration` is admitted only to a root caller holding `services:configure` at
 * the machine, so an ordinary operator's dispatch is refused the read. Telling him to install a
 * policy that is already installed, and disabling the button over it, is worse than saying
 * nothing — so it says what it could not see.
 */
export const SESSION_POLICY_STATES = [
  "missing",
  "unpriced",
  "priced",
  "unreadable",
  "unsupported",
] as const;
export const SessionPolicyStateSchema = z.enum(SESSION_POLICY_STATES);

/** What `launchPreview` answers about the session: the account, the model, the price, the ceiling. */
export const SessionPreviewSchema = z.strictObject({
  serviceId: z.string(),
  /** The identity key of the account this run would spend, or "" when none was chosen yet. */
  account: z.string(),
  /** The model reference this run would ask for, or "" when none was chosen yet. */
  model: z.string(),
  priced: z.boolean(),
  price: ModelPriceSchema.optional(),
  /** The ceiling the job request will carry as `limits.inference.costMicros`; absent when none. */
  ceilingMicros: z.number().int().min(0).optional(),
  policy: SessionPolicyStateSchema,
  /** Why the configuration could not be read, or "". Never an absent policy. */
  unreadable: z.string(),
  /** One sentence for the operator: what will be metered, and what will refuse the run. */
  note: z.string(),
  /**
   * WHAT THE HUB ANSWERED the last time an owner installed this policy on this machine,
   * verbatim; absent when it never refused one. It is what turns `unsupported` from a claim
   * into evidence: the sentence is the hub's, so an operator can tell a version gap from a
   * refusal Babel misread.
   */
  setupRefusal: z.string().optional(),
});
export type SessionPreview = z.infer<typeof SessionPreviewSchema>;

// ---------------------------------------------------------------------------- runs and launches

/** What Watch offers instead of flags: a preset is a named request the operator understands. */
export const PRESETS = ["read-whats-new", "explore-topic", "review-backlog", "file-and-tidy", "keep-going"] as const;
export const PresetSchema = z.enum(PRESETS);

/** The five states a run passes through; `stopped` is the only one an operator can cause. */
export const RUN_STATES = ["queued", "running", "finished", "failed", "stopped"] as const;
export const RunStateSchema = z.enum(RUN_STATES);

/**
 * WHICH OPERATION A PRESET BECOMES. It is here rather than beside the door's plan table because
 * the panel needs it too: `launch` declares `machines:run` at the operation node its arguments
 * name, so the caller has to build that node — the machine it picked and the operation its
 * preset runs — before it can knock. Two tables would be two answers to the same question, and
 * the one the panel held would be the one nobody checked.
 */
export const PRESET_OPERATIONS: Record<(typeof PRESETS)[number], OperationName> = {
  "read-whats-new": OPERATIONS.explore,
  "explore-topic": OPERATIONS.explore,
  "review-backlog": OPERATIONS.evaluate,
  "file-and-tidy": OPERATIONS.evaluate,
  "keep-going": OPERATIONS.scan,
};

/**
 * WHICH PRESETS REACH A MODEL, and therefore must name a session (#279).
 *
 * It sits beside {@link PRESET_OPERATIONS} for the same reason that table does: `launch`
 * refuses a session-less request for every preset in it (`session_required`), and the panel
 * hides its session picker for the one that reaches none. The beat asks a model nothing — it
 * ticks the conductor, which claims and dispatches jobs of its own — so a request for it is not
 * refused for lacking a session it would never spend.
 */
export const PRESET_REACHES_MODEL: Record<(typeof PRESETS)[number], boolean> = {
  "read-whats-new": true,
  "explore-topic": true,
  "review-backlog": true,
  "file-and-tidy": true,
  "keep-going": false,
};

export const LaunchInputSchema = z.strictObject({
  machineId: bounded(120),
  preset: PresetSchema,
  /** For `explore-topic`: the entity to run on; its sessions become the preparation. */
  entityId: EntityIdSchema.optional(),
  /** For `read-whats-new`: how far back, in days. */
  sinceDays: z.number().int().min(1).max(365).optional(),
  /** For `review-backlog`: how many draws. */
  draws: z.number().int().min(1).max(50).optional(),
  /** For `keep-going`: how long, in minutes. */
  minutes: z.number().int().min(5).max(24 * 60).optional(),
  /** Cookbook recipe ids to run; empty runs the enabled default set. */
  recipes: z.array(bounded(80)).max(16).default([]),
  /**
   * Whether the preparation may hold sessions of BABEL'S OWN runs (`sessions.kind` `agent`).
   *
   * Absent unless asked: a preset reads the operator's work, and a corpus that quietly included
   * Babel's own transcripts would have Babel reading itself by accident (#262). A preset whose
   * subject IS Babel (#270) sets it, and it is the only one that posts the knob — the panel's
   * rule is that a preset posts exactly its own knobs. Sessions still being written are never
   * selectable and have no flag: a preparation's identity is its selection's content.
   */
  agentSessions: z.boolean().optional(),
  /**
   * WHICH MODEL, AT WHICH THINKING LEVEL, ON WHOSE ACCOUNT (#279).
   *
   * Babel's explore and evaluate jobs launch `omp --mode rpc` themselves and reach a model only
   * through the `atyrode.babel.inference` binding, so the three things that used to be behind a
   * Code profile reference are now the operator's own choice and travel with the request. It is
   * OPTIONAL on the schema and REQUIRED in effect: `launchPreview` is polled while the operator
   * is still choosing and must answer without one, and `launch` refuses `session_required` for
   * any preset that reaches a model. A preset that reaches none — the beat — needs no session
   * and is not refused for lacking one.
   */
  session: SessionChoiceSchema.optional(),
});
export type LaunchInput = z.infer<typeof LaunchInputSchema>;

/**
 * What the `launch` door takes: the request above, plus the OPERATION NODE it is asked at.
 *
 * `launch` declares `machines:run`, which is governed: the engine grants it at a node and never
 * at a workspace, and the door's `requirements` name `operation` as the argument path the node
 * is read from — the host walks it through the RAW arguments and refuses `invalid authority
 * target` before the handler is entered. So the node is a field of the request rather than
 * something the door assembles: a reference the handler built would be a reference nobody
 * authorized the caller to name.
 *
 * The dry read is not on this door. `launchPreview` answers it under `containers:read`, because
 * a preview asks nothing of a machine and requiring version-bound consent to READ what a run
 * would cost is the panel unable to say what it is about to ask for.
 */
export const LaunchRequestSchema = LaunchInputSchema.extend({ operation: OperationRefSchema });

export const LaunchResultSchema = z.strictObject({
  runId: z.string(),
  jobId: z.string(),
  machineId: z.string(),
  kind: z.enum(["explore", "evaluate", "conductor", "prepare"]),
  /**
   * WHAT THE MACHINE'S LAST COMPLETED RUN ACTUALLY RAN UNDER, from the receipt it wrote: the
   * model that was asked for, the thinking level it was asked at, and the account it spent.
   *
   * It is a RECORDED figure and not a restatement of the request, which is the whole of its
   * value: a fallback or a retry moves the model mid-run (#261), and a machine that has run
   * nothing answers `null` rather than echoing what would be asked of it.
   */
  profile: z
    .strictObject({ model: z.string(), thinking: z.string(), account: z.string() })
    .nullable(),
  ceiling: z.strictObject({ perRunUsd: z.number(), perDayUsd: z.number() }),
  /**
   * WHAT THE OWNER WILL METER THIS REQUEST AT (ADR 0038). The price is the owner's policy's and
   * the ceiling is the one the job request will carry, so the sentence above the button and the
   * number the owner enforces come from the same place.
   */
  session: SessionPreviewSchema,
});

/**
 * What `stop` takes: the run to end, the JOB NODE the engine holds `jobs:cancel` at, and why —
 * the reason is recorded, never required. The node travels for the same reason `launch`'s does:
 * the requirement is discharged against the raw arguments, so the run row's `job_id` and
 * `machine_id` have to be posted, not looked up.
 */
export const StopInputSchema = z.strictObject({
  runId: z.string().min(1).max(200),
  job: JobRefSchema,
  reason: z.string().max(2000).default(""),
});

export const StopResultSchema = z.strictObject({
  runId: z.string(),
  jobId: z.string(),
  machineId: z.string(),
  closure: z.literal("stopped"),
});

/**
 * WHERE A RUN IS, in the three words the machine half reports and a row shows (#261).
 *
 * A stage is the WORKLOAD's own account of its phase, written to the private owner channel and
 * folded by the owner into one `job_progress` event per five seconds (manifold
 * `packages/protocol/src/worker.ts`). Three words and no more, because the point is a row an
 * operator reads at a glance: `preparing` is everything before the engine is asked anything,
 * `at the model` is from the instant the prompt leaves Babel until the turn ends, and
 * `submitting` is the results being written into the output lease. On 2026-09-13 a run printed
 * `preparing N/M` and then nothing for the rest of its life, and seventy-five minutes passed
 * with no engine on the machine and nothing saying so (post-mortem F12, O2).
 *
 * The words obey `JobProgressEventSchema.stage`: 1–64 characters of lowercase
 * `[a-z0-9 ._-]` with no leading or trailing space. A stage the owner refuses is not a bad
 * label, it is a failed channel and a cancelled job, so they are spelled once, here.
 */
export const RUN_STAGES = {
  preparing: "preparing",
  atModel: "at the model",
  submitting: "submitting",
} as const;
export type RunStage = (typeof RUN_STAGES)[keyof typeof RUN_STAGES];

/** What the owner's channel accepts, restated so the machine half can refuse its own frame. */
export const STAGE_PATTERN = /^[a-z0-9](?:[a-z0-9 ._-]{0,62}[a-z0-9])?$/;
export const STAGE_MESSAGE_MAX = 256;

/**
 * WHAT A RUNNING JOB IS DOING AND WHAT IT HAS SPENT, as the conductor folds it out of the job's
 * replay ring each cycle: the newest `job_progress` for the stage and every `inference_call`
 * since the last fold for the spend (manifold#554).
 *
 * `stalled` is the one judgement in it: a run the hub METERS that said `at the model` and has
 * had no metered call for ninety seconds. A run nothing meters is never judged — where no call
 * is ever counted, that silence is the ordinary state and not a symptom. It is a flag on a row
 * and never a closure — nothing here observed a dead process — and the next call clears it.
 */
export const RunProgressSchema = z.strictObject({
  stage: z.string(),
  message: z.string(),
  /** How far through the stage, where the loop reporting it counts; null where it cannot. */
  fraction: z.number().nullable(),
  /** When this stage was first observed: the "since" of `at the model since T`. */
  since: z.string(),
  calls: z.number().int(),
  inputTokens: z.number().int(),
  outputTokens: z.number().int(),
  cacheTokens: z.number().int(),
  costUsd: z.number(),
  /** The model that answered the newest call; empty until one has. */
  lastModel: z.string(),
  stalled: z.boolean(),
  updatedAt: z.string(),
});
export type RunProgress = z.infer<typeof RunProgressSchema>;

export const RunRowSchema = z.strictObject({
  id: z.string(),
  kind: z.string(),
  machineId: z.string(),
  jobId: z.string(),
  recipe: z.string(),
  state: RunStateSchema,
  startedAt: z.string(),
  finishedAt: z.string(),
  costUsd: z.number().nullable(),
  records: z.number().int(),
  freshness: z.enum(["fresh", "recent", "lost", "ended"]),
  lastWord: z.string(),
  /** What the run has spent, from the hub's own meter when it has one; null before it has. */
  tokens: z.number().int().nullable(),
  /**
   * How many calls the hub metered, kept with the receipt when the run settled; null for a run
   * nothing metered — which is every run of the local lane, not a run that made no call.
   */
  calls: z.number().int().nullable(),
  /** Where it is and what it has spent so far; null for a run nothing is folding. */
  progress: RunProgressSchema.nullable(),
});

/** `runs` serves all five narrowings; Watch sends the first three. */
export const RunsQuerySchema = z.strictObject({
  limit: z.number().int().min(1).max(100).default(25),
  offset: z.number().int().min(0).default(0),
  state: RunStateSchema.optional(),
  machineId: z.string().max(120).optional(),
  kind: z.string().max(40).optional(),
});

export const RunsResultSchema = z.strictObject({
  runs: z.array(RunRowSchema),
  total: z.number().int(),
});

export const RunQuerySchema = z.strictObject({ id: z.string().min(1).max(200) });

/**
 * One run and the receipt it wrote. The receipt travels as the document the machine half
 * produced rather than as a projection of it: §7 makes the receipt the run's own account of what
 * it was asked, read, produced and cost, and a surface that re-stated it in its own fields would
 * be a second answer to a question the run already answered.
 */
export const RunResultSchema = z.strictObject({
  run: RunRowSchema.nullable(),
  receipt: z.record(z.string(), z.unknown()).nullable(),
});

// ---------------------------------------------------------------------------- the policy

export const RecipeRowSchema = z.strictObject({
  id: z.string(),
  /** Empty when the policy payload carries no recipe map; the list then shows the id. */
  title: z.string(),
  /** One line of what this recipe looks for, from the policy payload. */
  looksFor: z.string(),
  enabled: z.boolean(),
  /** ISO instant of the newest run under this recipe, or empty: never run. */
  lastRanAt: z.string(),
  lastRunId: z.string(),
  runs: z.number().int(),
});

/**
 * THE OVERLAY IN FORCE, as the ceilings panel shows it beside the standing numbers (#260): what
 * it moves, until when, and why. `changes` carries both values because the operator's question
 * is never "what may a machine hold" but "what did the drain change it from".
 *
 * The fields are the policy's own camelCase names — `perCycleCost`, `dailyCost` and
 * `concurrentPerMachine`, the one admission knob — so a reader of the panel and a reader of
 * `setBudget` see one vocabulary.
 */
export const BudgetOverlaySchema = z.strictObject({
  id: z.string(),
  createdAt: z.string(),
  expiresAt: z.string(),
  reason: z.string(),
  changes: z.array(
    z.strictObject({ field: z.string(), standing: z.number(), overlaid: z.number() }),
  ),
});
export type BudgetOverlay = z.infer<typeof BudgetOverlaySchema>;

/**
 * The evaluation policy in force, as Watch reads it: the ceilings and the lanes projected out of
 * the stored document, what has been spent against them today, the recipes joined to what has
 * actually run under them, the bounded exception in force over it — and the document itself, so
 * the projection above can be checked against the row it came from rather than believed.
 *
 * `ceilings` are the STANDING numbers throughout. An overlay is reported as itself rather than
 * folded into them, because a panel that showed 64 with no other word would be the interface
 * that let the drain's batch outlive the drain by ninety minutes unnoticed.
 */
export const PolicyResultSchema = z.strictObject({
  version: z.string(),
  seq: z.number().int(),
  actorId: z.string(),
  reason: z.string(),
  recordedAt: z.string(),
  ceilings: z.strictObject({
    perRunUsd: z.number(),
    perDayUsd: z.number(),
    concurrent: z.number(),
  }),
  spentTodayUsd: z.number(),
  lanes: z.array(z.strictObject({ lane: z.string(), role: z.string(), share: z.number() })),
  recipes: z.array(RecipeRowSchema),
  /** Null when nothing is overlaid: the standing numbers are the numbers. */
  overlay: BudgetOverlaySchema.nullable(),
  payload: z.record(z.string(), z.unknown()),
});

// ---------------------------------------------------------------------------- job outputs

/**
 * What a machine operation writes as its output files and the hub ingests (plan §4): one
 * JSON document per file, each a list of rows in the store's own shapes, so ingestion is a
 * `batch` and nothing is reinterpreted on the way in.
 */
export const JOB_OUTPUT_FILES = {
  records: "records.json",
  edges: "edges.json",
  statusEvents: "status-events.json",
  assessments: "assessments.json",
  filings: "filings.json",
  plans: "plans.json",
  questions: "questions.json",
  steeringReplies: "steering-replies.json",
  sessions: "sessions.json",
  receipt: "receipt.json",
} as const;

/** The receipt every run writes last (§7): what it was asked, read, produced and cost. */
export const ReceiptSchema = z.strictObject({
  runId: z.string(),
  kind: z.enum(["scan", "archive", "prepare", "explore", "evaluate"]),
  machineId: z.string(),
  recipeId: z.string().optional(),
  role: RoleSchema.optional(),
  profile: z.record(z.string(), z.unknown()).optional(),
  /**
   * WHOSE WINDOW THIS RUN SPENT, and WHICH MODEL IT ASKED FOR (#267, #279).
   *
   * The `profile` block above is Babel's own launch report and says the same two things in the
   * launcher's words; these two fields are the flat pair every reader wants — the run row, the
   * drain's fold, the receipt page — and they are the ones a drain is measured by, because
   * "drain THIS account" is answered by summing the runs that named it and by nothing else.
   * Absent for a run that reaches no model at all (`scan`, `archive`, `prepare`).
   */
  account: z.strictObject({ provider: z.string(), identityKey: z.string() }).optional(),
  model: z.string().optional(),
  preparation: z.record(z.string(), z.unknown()).optional(),
  startedAt: z.string(),
  finishedAt: z.string(),
  closure: z.enum(["completed", "failed", "stopped", "skipped"]),
  reason: z.string().optional(),
  costUsd: z.number().optional(),
  tokens: z.number().int().optional(),
  /**
   * THE MODELS THAT ANSWERED, in the order the run first heard from each (#261).
   *
   * The profile block above says which model the run was LAUNCHED under, which is not the same
   * sentence: a fallback, an auto-retry or an operator's steer moves it mid-run, and on
   * 2026-09-13 nothing anywhere said what had actually answered. Absent for a run that reached
   * no engine; empty for one whose engine never named a model.
   */
  models: z.array(z.string()).optional(),
  counts: z.record(z.string(), z.number().int()),
});
export type Receipt = z.infer<typeof ReceiptSchema>;

// ---------------------------------------------------------------------------- job bindings

/**
 * The runtime tools an operation may name. Two kinds, and the difference matters:
 *
 * `bun`, `git`, `restic`, `ca-certificates` and `system` are the machine OWNER's, bound WITH
 * their closures (`execution.runtimeToolClosures`, manifold docs/SELF-HOST.md) because a
 * Manifold job sandbox carries no libc and a bare binary cannot exec in one. `bun` runs
 * machine.js; `git` reads repository identity for scan and prepare; `restic` owns the archive's
 * repository format; `ca-certificates` is the CA bundle `SSL_CERT_FILE` names, and `system` is
 * the reviewed libc closure a dynamically linked binary needs.
 *
 * `omp` is the one tool this manifest PINS as an artifact, by url and digest, from
 * manifold-omp's own `runtime-artifacts.json` (SDK 18.1.14). It is pinned rather than delegated
 * because it is the thing being driven: a run's answers come from that exact build, and an
 * owner-bound `omp` would let one machine's engine differ from another's without anything in
 * the record saying so. Babel's own launch report names the version it got.
 */
export const RUNTIME_TOOLS = [
  "bun",
  "omp",
  "ca-certificates",
  "system",
  "git",
  "restic",
] as const;
/** Where a runtime tool is bound inside the sandbox: `<RUNTIME_TOOL_BIN>/<alias>`. */
export const RUNTIME_TOOL_BIN = "/runtime/bin";

/**
 * Every Babel operation writes its files flat into ONE output directory and binds it under one
 * name, so an operation that had nothing to say about a file simply writes no file, rather than
 * leaving a promised binding unfilled. The operation declarations in server.ts and the loop that
 * reads the outputs back agree through these three strings and nothing else.
 */
export const OUTPUT_BINDING = "outputs";
/** The LOCATION is a machine-half declaration, so it carries the plugin's own prefix; the
 *  binding name above is a name inside one job and does not. */
export const OUTPUT_LOCATION = `${BABEL_PLUGIN_ID}.outputs`;
/** The operation's single input binding: one JSON document, as the machine half parses it. */
export const INPUT_FIELD = "input";

/**
 * THE STORAGE SERVICE the `archive` operation is bound to (issue #244).
 *
 * An operation's `environment` is fixed reviewed values in a committed manifest, which is not
 * where the repository password goes — and not where this deployment's repository locator can
 * go either, since a manifest is code and the locator is provisioning. Both arrive through ONE
 * service the operator installs, under this id: the engine materializes that binding's loopback
 * endpoint and a capability minted for this job alone into `RESTIC_CREDENTIAL_FILE`, and the
 * operation asks the service for the storage document. The locator and the object-store
 * credential an `s3:` locator needs therefore arrive together, never in halves (SPEC decision
 * 50), and no secret reaches argv, the environment, the job request or the hub's journal.
 *
 * `operationId` is the policy key the binding names and `path` is the route that policy
 * declares; the manifest spells both and so does machine/restic.ts, which is why they are
 * stated here once.
 */
export const RESTIC_SERVICE = {
  serviceId: `${BABEL_PLUGIN_ID}.restic`,
  revision: "1",
  operationId: "storage",
  path: "/storage",
  /** The input file the binding is materialized into, as `{url, bearer}`. */
  inputFile: "restic",
} as const;
/** Where the engine binds that file inside the sandbox: one job's own, read-only. */
export const RESTIC_CREDENTIAL_FILE = `/inputs/${RESTIC_SERVICE.inputFile}`;

// ------------------------------------------------------------- the inference service (#279)

/**
 * THE INFERENCE SERVICE `explore` and `evaluate` ARE BOUND TO (ADR 0038, babel#256's hub half).
 *
 * A job that drives a model never holds the model's credential, and after atyrode/code#153 there
 * is no Code process to hold one on its behalf either. So Babel's job launches `omp --mode rpc`
 * itself, and the only route out of that sandbox to a provider is this binding, whose RUNTIME is
 * `atyrode.omp.gateway`'s own `serve` operation: a job-scoped gateway the machine's owner starts
 * with the account pool THIS job named, and whose `stream` operation the owner meters per call.
 *
 * Why a runtime and not an origin. An `origin` policy points at a provider and needs a credential
 * the owner holds; a `runtime` policy points at another plugin's machine operation, so there is
 * no credential in the policy at all — the gateway resolves one from the machine's broker for the
 * pool it was handed, and the job receives a loopback url and a bearer minted for it alone. The
 * bearer is spliced by the OWNER into `models.yml`'s `providers.*.apiKey` (see
 * {@link OMP_INPUT_FILES}), so it never passes through Babel's code, argv, environment or logs.
 *
 * `operationIds` are exactly omp's gateway's two: listing models, and one streaming call. The
 * metered one is `stream`; `plugins/README.md` carries the policy the owner installs and
 * `doors/inference.ts` is what installs it, so nobody hand-writes it.
 */
export const INFERENCE_SERVICE = {
  serviceId: `${BABEL_PLUGIN_ID}.inference`,
  revision: "1",
  /** Every operation the binding names, in the manifest's own order. */
  operationIds: ["models", "stream"] as const,
  /** omp's gateway, which provides the service: the plugin and the operation that serves it. */
  gatewayPluginId: "atyrode.omp.gateway",
  gatewayOperationId: "atyrode.omp.gateway.serve",
  /**
   * The meter kind the owner reads usage with. omp's wire is pi-native (`modelId`,
   * `context.messages`, `usage.input`/`.output`/`.cacheRead`), not OpenAI's, so `openai-usage`
   * would refuse every call rather than silently miss; manifold#570 adds this kind.
   */
  meterKind: "pi-native-usage",
  /** The gateway's own route, which the policy's `stream` operation proxies. */
  streamPath: "/v1/pi/stream",
  modelsPath: "/v1/models",
} as const;

/**
 * THE ACCOUNTS BROKER Babel READS, and never writes (#267).
 *
 * manifold-omp's accounts plugin owns one Instance Service holding the machine's enrolled
 * credentials, and it projects a secret-free subset of the broker's snapshot through its
 * `metadata` operation — ids, providers, identity keys, credential type and email, and the
 * blocks in force (`plugins/atyrode.omp/service-policies.ts` `buildSharedBrokerPolicy`). That
 * projection is exactly what a picker needs and nothing more, which is why Babel reads it
 * instead of asking the operator to type an identity key he would have to find elsewhere.
 *
 * It is an INSTANCE service, so the read is `ctx.services.readInstance` under `services:read` —
 * the same call manifold-omp's own `accountObservation` makes. There is no plugin-to-plugin door
 * call in Manifold and none is needed: an Instance Service is the seam.
 */
export const ACCOUNTS_SERVICE = {
  serviceId: "atyrode.omp.accounts.broker",
  /** The projected, secret-free snapshot: what accounts exist, and which are blocked. */
  metadataOperationId: "metadata",
  /** The projected usage windows a drain reads to know what is left (#258, #267). */
  usageOperationId: "usage",
} as const;

// ------------------------------------------------------------------- launching omp (#279)

/** The pinned engine's alias and the path it is bound at inside a job. */
export const OMP_TOOL = "omp";
export const OMP_BINARY = `${RUNTIME_TOOL_BIN}/${OMP_TOOL}`;

/**
 * WHERE THE JOB'S PRIVATE HOME IS, and the two files the OWNER materializes into it.
 *
 * A Manifold job's home is a tmpfs the sandbox creates and `HOME` names it
 * (`agent/src/job-linux.ts`); an `inputFiles` declaration with a `homePath` is bound read-only
 * underneath it (`agent/src/job-inputs.ts`). omp discovers `~/.omp/agent/models.yml` for its
 * providers and takes `--config` for the rest, which is exactly the pair `atyrode.omp.launch`
 * declares — so Babel declares the same two files with the same two paths, and the credential
 * splice (`jsonValues` into `providers.*.baseUrl` and `providers.*.apiKey`) is the owner's.
 *
 * Babel's machine half therefore WRITES NEITHER FILE. It reads that both exist and refuses by
 * name when one does not, which is the only honest check available to a process that must never
 * be able to see the bearer inside them.
 */
export const OMP_HOME = "/home/job";
export const OMP_INPUT_FILES = {
  models: { name: "models", path: `${OMP_HOME}/.omp/agent/models.yml` },
  config: { name: "config", path: `${OMP_HOME}/.omp/agent/config.yml` },
} as const;

/**
 * THE JOB INPUT FIELDS an explore or an evaluate carries beyond its launch document.
 *
 * `accountPool` is a `RuntimeAccountPool` JSON document — `{[provider]: [{scope, credentialId,
 * identityKey}]}` — and is NOT read by Babel's machine half at all: the service policy's runtime
 * maps it into the gateway job (`ServiceRuntime.input`, exactly as manifold-omp's own
 * `configureGateway` does), which is how a job says which of several enrolled accounts it spends
 * without any new primitive (#267, and why manifold#549 is unnecessary here). `models` and
 * `config` are the two YAML documents above, derived the way manifold-omp's `execution.ts`
 * `nativeModelConfiguration`/`effectiveOverlay` derive them.
 */
export const SESSION_INPUTS = {
  accountPool: "accountPool",
  models: OMP_INPUT_FILES.models.name,
  config: OMP_INPUT_FILES.config.name,
} as const;

// --------------------------------------------------------------- accounts and setup (#279)

/** One account the machine's broker has observed, as the Start panel offers it. */
export const AccountRowSchema = z.strictObject({
  provider: z.string(),
  scope: z.string(),
  credentialId: z.string(),
  identityKey: z.string(),
  /** What the broker's projected snapshot calls it, when it says anything; "" otherwise. */
  label: z.string(),
  /** True when the broker reports the credential blocked or disabled: offered, and marked. */
  disabled: z.boolean(),
});
export type AccountRow = z.infer<typeof AccountRowSchema>;

export const AccountsQuerySchema = z.strictObject({ machineId: bounded(120) });

/**
 * WHAT THE BROKER HAS SEEN, or the reason nobody could be asked.
 *
 * `unavailable` is not an empty list: Babel reads the accounts through its own binding to
 * `atyrode.omp.accounts.broker`'s projected `metadata` operation, and a hub where that service is
 * not installed, or a caller not admitted to read it, is a picker that says so and accepts a
 * typed identity key instead. An empty list with no reason is the broker answering "none
 * enrolled", which is a different instruction to the operator.
 */
export const AccountsResultSchema = z.strictObject({
  accounts: z.array(AccountRowSchema),
  unavailable: z.string(),
});

/**
 * WHAT `setupInference` TAKES. It is the owner's act — `services:configure` at the machine — and
 * it is compare-and-set on the machine's whole service configuration, so the revision the caller
 * last read travels with it: two operators installing two policies at once must not silently
 * overwrite one another.
 *
 * `apply: false` is the preview. It answers exactly what the write would do without doing it,
 * which is how Watch can state "this would install a policy pricing 4 models" before the button.
 */
export const SetupInferenceInputSchema = z.strictObject({
  machineId: bounded(120),
  /** What `readConfiguration` last reported, or null for a machine with no configuration yet. */
  expectedServiceRevision: z.string().max(200).nullable().default(null),
  apply: z.boolean().default(false),
});

export const SetupInferenceResultSchema = z.strictObject({
  serviceId: z.string(),
  /** The configuration revision after the write, or null for a preview. */
  revision: z.string().nullable(),
  state: z.enum(["previewed", "installed", "refreshed", "unchanged"]),
  /** Every model the policy prices, in the order the policy states them. */
  models: z.array(z.string()),
  /** One sentence naming what the owner now has, or what stopped it. */
  note: z.string(),
});
