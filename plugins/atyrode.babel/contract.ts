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
  launch: "launch",
  stop: "stop",
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

/** The operations the baseline declares on a machine (plan §4); each is one job. */
export const OPERATIONS = {
  scan: "scan",
  archive: "archive",
  prepare: "prepare",
  explore: "explore",
  evaluate: "evaluate",
} as const;
export type OperationName = (typeof OPERATIONS)[keyof typeof OPERATIONS];

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

// ---------------------------------------------------------------------------- runs and launches

/** What Watch offers instead of flags: a preset is a named request the operator understands. */
export const PRESETS = ["read-whats-new", "explore-topic", "review-backlog", "file-and-tidy", "keep-going"] as const;
export const PresetSchema = z.enum(PRESETS);

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
});
export type LaunchInput = z.infer<typeof LaunchInputSchema>;

export const LaunchResultSchema = z.strictObject({
  runId: z.string(),
  jobId: z.string(),
  machineId: z.string(),
  kind: z.enum(["explore", "evaluate", "conductor", "prepare"]),
  /** What will run, from the machine's `code engine --describe`, before the first byte. */
  profile: z.strictObject({ id: z.string(), revision: z.number().int(), model: z.string(), disclosure: z.string(), costPer1k: z.strictObject({ input: z.number(), output: z.number() }) }).nullable(),
  ceiling: z.strictObject({ perRunUsd: z.number(), perDayUsd: z.number() }),
});

export const RunRowSchema = z.strictObject({
  id: z.string(),
  kind: z.string(),
  machineId: z.string(),
  jobId: z.string(),
  recipe: z.string(),
  state: z.enum(["queued", "running", "finished", "failed", "stopped"]),
  startedAt: z.string(),
  finishedAt: z.string(),
  costUsd: z.number().nullable(),
  records: z.number().int(),
  freshness: z.enum(["fresh", "recent", "lost", "ended"]),
  lastWord: z.string(),
});

export const RunsResultSchema = z.strictObject({
  runs: z.array(RunRowSchema),
  total: z.number().int(),
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
  preparation: z.record(z.string(), z.unknown()).optional(),
  startedAt: z.string(),
  finishedAt: z.string(),
  closure: z.enum(["completed", "failed", "stopped", "skipped"]),
  reason: z.string().optional(),
  costUsd: z.number().optional(),
  tokens: z.number().int().optional(),
  counts: z.record(z.string(), z.number().int()),
});
export type Receipt = z.infer<typeof ReceiptSchema>;
