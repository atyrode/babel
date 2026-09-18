import { z } from "zod";

/*
  THE VOCABULARY OF THE atyrode.babel PLUGIN FAMILY, spelled once. Every id, door name, event
  kind, panel id and machine operation the halves use is a constant here; a manifest is JSON and
  repeats its own id as data, and `test/contract.test.ts` pins each manifest to these constants
  so the two can never disagree. The kit inlines this module into every bundle that imports it.

  The family:
  - `atyrode.babel` is the baseline: the store (ADR 0034's plugin database), every door, the
    machine half's declared operations and their schedules, the feed index. It serves no panel.
  - `atyrode.babel.feed` is the reading surface: Home, a record, a topic — SPEC §8.3 and §4.13.
  - `atyrode.babel.watch` is the control room: runs in flight, presets, recipes, ceilings — §8.4.
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
export const RecordIdSchema = z.string().regex(/^(hyp|obs|fnd|pro|qst)_[0-9a-f]{8,64}$/);
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
  /**
   * STARTING A RUN, which is posting a Code session (#279). Babel's runs are Code sessions: the
   * operator picks a saved Code profile or parametrizes one in Code's own generator, and Babel
   * posts the run through `atyrode.code.runSession` — reached with `ctx.actions.call` on the
   * declared dependency (ADR 0041). Babel composes the prompt and nothing else about the
   * session: no model, no thinking level, no account. There is no dry preview beside it: what a
   * run would cost is Code's to say, out of the profile the operator chose.
   */
  launch: "launch",
  stop: "stop",
  /**
   * THE SAVED CODE PROFILES, as Watch's Start section offers them. It is Babel's own door and
   * not a client-side call into Code, for one reason: the panel must read exactly the list the
   * SERVER will post against, and a browser that asked Code directly would show a revision the
   * server never saw — which is `code_stale_preferences` after the press instead of before it.
   */
  profiles: "profiles",
  /** The three acts of #258: start a drain, read one, end one. */
  drainStart: "drainStart",
  drainStatus: "drainStatus",
  drainStop: "drainStop",
  // the crossing (owner only)
  importLedger: "importLedger",
  /**
   * RE-HOSTING A CATALOGUED CORPUS, which is the crossing's own repair (#310).
   *
   * `sessions.host` is a HUB MACHINE ID and the crossing wrote a Go host NAME into it, so every
   * readiness check, run listing and folder question about those rows asks about a machine that
   * does not exist. Nothing can look a name up — the hub resolves none, and no door a plugin is
   * served lists machines — so the operator supplies the mapping and the HUB verifies the
   * destination: this act rewrites one `host` value to one machine id it has just described.
   */
  rehostSessions: "rehostSessions",
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
 * The operations THIS BUNDLE'S MACHINE HALF RUNS (plan §4); each is one job, and each is a verb
 * of the `babel-machine` binary and an entry of `manifest.json`'s `machine.operations`.
 *
 * THE IDS ARE NAMESPACED because the engine requires it: `engine.jobs.install` refuses a machine
 * half whose operation or location keys are not prefixed with the plugin's own id
 * (`unqualified_declaration`), so a bare `scan` is a declaration no hub would ever install.
 */
export const MACHINE_OPERATIONS = {
  scan: `${BABEL_PLUGIN_ID}.scan`,
  archive: `${BABEL_PLUGIN_ID}.archive`,
  prepare: `${BABEL_PLUGIN_ID}.prepare`,
} as const;

/**
 * EVERY OPERATION BABEL NAMES, which is not the same list (#279).
 *
 * `explore` and `evaluate` are NAMED and not DECLARED. A Babel run is a Code session: the
 * operator parametrizes it through a saved Code profile or Code's generator, and Code's
 * `runSession` door posts it to omp (atyrode/code#170, reached through atyrode/manifold#575).
 * Babel neither composes the session nor launches omp, so neither is a machine operation of
 * this bundle any more — but both are still what a run is CALLED: the node a launch asks
 * authority at, the `kind` a run row and a receipt record, and the lane a preset names. The two
 * tables are therefore two different questions, and the day they answered as one is the day
 * Babel had a launcher of its own.
 */
export const OPERATIONS = {
  ...MACHINE_OPERATIONS,
  explore: `${BABEL_PLUGIN_ID}.explore`,
  evaluate: `${BABEL_PLUGIN_ID}.evaluate`,
} as const;
export type OperationName = (typeof OPERATIONS)[keyof typeof OPERATIONS];

/**
 * The word the machine half's CLI takes and the receipt records — the KEY of the declared table.
 * A binary's verb is `scan`, not `atyrode.babel.scan`: the namespace exists so a hub can tell
 * two plugins' operations apart, and there is only ever one plugin inside that binary.
 */
export type OperationWord = keyof typeof MACHINE_OPERATIONS;

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

/**
 * The five kinds this plugin ORIGINATES. An event id is claimed across the whole assembly,
 * and Manifold `476a586c` gave `run_changed` to `core.access` — the hub's own word for a
 * terminal run moving — so Babel's is prefixed. Nothing else is: the other four are Babel's
 * nouns and nobody else's.
 */
export const EVENTS = {
  recordWritten: "record_written",
  ruled: "ruled",
  assessed: "assessed",
  planApplied: "plan_applied",
  runChanged: "babel_run_changed",
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

/**
 * The crossing's repair: one `sessions.host` value, replaced by one machine id (#310).
 *
 * `from` is whatever is in the store — a Go host name, which is why the rows are unusable. `to`
 * is a hub machine id, and the door describes it before it writes: a second unusable value would
 * be the same defect with a different string in it. Both are required and neither is guessed.
 */
export const RehostSessionsInputSchema = z.strictObject({
  from: bounded(200),
  to: bounded(200),
});

// ------------------------------------------------------------------------- the model session

/*
  WHO ANSWERS A RUN — which is never Babel, and the vocabulary a drain still holds.

  Babel does not choose a model, a thinking level or an account. Code does: `atyrode.babel`
  depends on `atyrode.code`, which depends on `atyrode.omp`, and the profiles — model, thinking,
  account — are Code's to save, to generate and to resolve when its `runSession` door posts the
  omp job. Babel's own picker, its own `atyrode.babel.inference` policy and its own price table
  were #284's interim and are gone. What Babel names is a PROFILE: one configured Code
  workspace, at the revision the operator was shown.

  What survives beside it is the SHAPE a drain records: a drain exists to spend one named
  account's window before it resets (#258, #267), so its row has to say which account and which
  model it was started for, and its panel has to say it back. That shape is a drain's own
  accounting and is never posted to Code — `runSession` takes a container, a destination and a
  prompt, and reads the rest off the profile.

  The model reference is FULLY QUALIFIED — `anthropic/claude-sonnet-4-5`, provider and all —
  because that is the string Code's composition and omp's gateway both key their model map by.
*/

/**
 * The thinking levels Babel offers. omp's own enum is wider (`minimal` … `max`); these four are
 * the ones an analysis run is worth asking at, and the one that is absent is the honest shape of
 * "whatever the model does by default" rather than a level nobody chose.
 */
export const THINKING_LEVELS = ["low", "medium", "high", "xhigh"] as const;
export const ThinkingSchema = z.enum(THINKING_LEVELS);
export type Thinking = z.infer<typeof ThinkingSchema>;

/**
 * The fully qualified model reference a composition routes by: `<provider>/<model>`, matching
 * manifold-omp's own `modelReference` (`plugins/api/index.ts`), because the string a drain
 * records has to be one Code's composition accepts unchanged.
 */
export const MODEL_REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._-]*\/[A-Za-z0-9][A-Za-z0-9._/:-]{0,255}$/;

/**
 * WHAT A DRAIN IS SPENDING: one model, one thinking level, one account (#258, #267).
 *
 * It is the drain's own record of what it was started for, not a session Babel composes — Babel
 * composes none. `account` carries the fields manifold-omp's broker verifies a pool against, and
 * none of them is a secret: a credential id and an identity key NAME a credential the machine's
 * broker holds, which is why a row may carry them and never a bearer.
 *
 * TWO FIELDS ARE SPELLED AS STRINGS HERE AND ARE NOT STRINGS ON THE WIRE, deliberately. In a
 * pool, `credentialId` is a positive INTEGER and `identityKey` is `string | null`. This is a
 * DOOR surface: it is posted from a form whose every value is a string, so it takes the decimal
 * digits and refuses at the door what could never be a credential row. An EMPTY `identityKey` is
 * the api-key case, where the broker's own reference is the credential row and there is no OAuth
 * identity.
 *
 * A drain records it; nothing posts it. The run itself is parametrized by the Code profile the
 * operator picked, and `runSession` reads the model, the level and the account off that.
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

// ---------------------------------------------------------------- the profile a run is posted on

/**
 * A SAVED CODE PROFILE, as a launch names one: the configured Code WORKSPACE, at the revision
 * the operator was shown it at.
 *
 * A profile IS a configured workspace (Code's `ProfileSchema`): its catalog, its selection and
 * its account choices belong to the container, and `machineId` on Code's own row is only where
 * Code last posted for it — never a saved pin. So a launch carries the container and the
 * destination separately, and the destination is Babel's `machineId` as it always was.
 *
 * `expectedRevision` is the optimistic pin `runSession` refuses `code_stale_preferences`
 * against. It is carried from the panel rather than re-read here on purpose: it is the whole of
 * how an operator learns that the profile moved between reading the list and pressing the
 * button, which is a thing he must be told rather than have silently accommodated.
 */
export const CodeProfileSchema = z.strictObject({
  containerId: bounded(128),
  expectedRevision: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
});
export type CodeProfile = z.infer<typeof CodeProfileSchema>;

/**
 * WHAT WATCH'S START SECTION OFFERS, read through Babel's own `profiles` door: every saved Code
 * profile, and — when Code could not be asked — the sentence saying so.
 *
 * The two halves are both answers. A hub with Code disabled, a Babel whose install grant does
 * not reach Code's doors, a Code too old to publish `listProfiles`: each is a refusal the
 * operator acts on differently, and each is `unavailable` with the engine's own word in it
 * rather than an empty list that reads as "you have saved none".
 */
/**
 * ONE ACCOUNT A PROFILE WOULD SPEND, as Code reports it.
 *
 * It is CODE'S FACT and not Babel's: the account belongs to the profile, Code resolves it and
 * omp holds it, and Babel has no broker to ask. Babel records what Code said, labelled as
 * that, because "which window did that fan burn" has to be answerable afterwards (#267) and
 * the only honest source for it is the plugin that chose it.
 */
export const ProfileAccountSchema = z.strictObject({
  provider: z.string(),
  /** The OAuth identity; empty for an api-key credential, which has none. */
  identityKey: z.string(),
  /** What Code shows a reader for it, when it shows anything. */
  label: z.string().default(""),
});
export type ProfileAccount = z.infer<typeof ProfileAccountSchema>;

export const ProfileRowSchema = z.strictObject({
  containerId: z.string(),
  revision: z.number().int(),
  /** The model leading the default role and the depth it thinks at; empty when the saved
   *  selection no longer reviews against its catalog, which is a profile to open in Code. */
  model: z.string(),
  thinking: z.string(),
  /** Where Code last posted a session for this workspace; empty when it never has. */
  lastMachineId: z.string(),
  /**
   * The accounts Code says this profile would spend. EMPTY IS NOT "none": a Code too old to
   * report them answers nothing here, and the panel says which of the two it is rather than
   * printing a blank where the answer to "whose window" belongs.
   */
  accounts: z.array(ProfileAccountSchema).max(64).default([]),
  /** Whether Code could resolve the profile's selection against its catalog at all. */
  resolved: z.boolean().default(false),
});
export type ProfileRow = z.infer<typeof ProfileRowSchema>;

/** The `profiles` door takes nothing: the list is every saved profile the hub's Code holds. */
export const ProfilesQuerySchema = z.strictObject({});

export const ProfilesResultSchema = z.strictObject({
  profiles: z.array(ProfileRowSchema).max(4096),
  /** Empty when Code answered; otherwise Babel's engine refusal, verbatim. */
  unavailable: z.string(),
});

/**
 * WHAT A DRAIN RECORDS ABOUT WHAT IT IS SPENDING (#267, #279).
 *
 * A drain names a Code PROFILE, and everything else here is Babel's own ledger entry of what
 * Code said that profile would run as, COPIED ONCE at the start and never re-read. Copied,
 * because a controller that asked again between the first job and the ninetieth would report
 * whatever the profile had become rather than what the operator started; a ledger entry,
 * because Babel chooses none of it and must not present it as its own decision. The panel
 * says so in as many words.
 */
export const DrainProfileSchema = z.strictObject({
  profile: CodeProfileSchema,
  model: z.string(),
  thinking: z.string(),
  accounts: z.array(ProfileAccountSchema).max(64),
  /** Whether Code had resolved the selection when the drain was started. */
  resolved: z.boolean(),
});
export type DrainProfile = z.infer<typeof DrainProfileSchema>;

// ------------------------------------------------------------------- the material a run reads

/*
  THE MATERIAL: the evidence a run reads, sealed by Babel and bound into the session's sandbox.

  Babel holds no host tools in the session any more — #284's `babel_search`/`babel_fetch`/
  `babel_submit` were tools of a driver Babel no longer runs. A Code session is omp's own job
  with omp's own tools, so the way Babel serves evidence is the way a job serves any input: a
  sealed OUTPUT of Babel's own `prepare`, bound into the consumer's sandbox as a read-only
  directory. The model reads it with the tools it already has.

  The layout is fixed here because two things far apart depend on it being the same: the machine
  half writes it (`machine/prepare.ts`) and the prompt describes it (`server/engine/prompts.ts`).
*/

/** The second sealed output of `atyrode.babel.prepare`: the material, as its own lease. */
export const MATERIAL_OUTPUT = "material";
/** Where the consumer's sandbox sees it, read-only: `/inputs/<name>` is the job's own namespace. */
export const MATERIAL_ROOT = `/inputs/${MATERIAL_OUTPUT}`;
/** What names the selection: the preparation's identity and one entry per session. */
export const MATERIAL_INDEX = "index.json";
/** The directory the per-session files live in, one file per session in the index. */
export const MATERIAL_SESSIONS = "sessions";
/** The shape of `index.json`, recorded in it so a reader never guesses which layout it has. */
export const MATERIAL_SCHEMA = "babel.material/1";

/**
 * THE NAME ONE SESSION'S FILE HAS INSIDE THE MATERIAL, and why it is not the selector.
 *
 * A selector is `harness/source-id` and a source id is whatever the harness chose: slashes,
 * spaces, colons, and on one harness a whole path. A file name has to be one path component, so
 * every run of characters outside `[A-Za-z0-9._-]` becomes a dash — and the ORDINAL goes in
 * front, because two sessions whose ids differ only in a character that was replaced would
 * otherwise be one file, and a material where one session silently overwrote another is worse
 * than no material at all. The index maps the name back to the selector, which is why the name
 * itself need only be unique and readable.
 *
 * It is HERE rather than in either half because both halves need the same answer and neither may
 * import the other: `machine/prepare.ts` writes the file and `doors/launch.ts` tells the model
 * which file to open, and a second copy of this rule is a prompt pointing at a path that is not
 * there.
 */
export function materialFile(ordinal: number, selector: string): string {
  const safe = selector
    .replace(/[^A-Za-z0-9._-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 96);
  return `${String(ordinal + 1).padStart(4, "0")}-${safe === "" ? "session" : safe}.jsonl`;
}

/**
 * ONE SESSION AS THE MATERIAL CARRIES IT: how the prompt names it, where its records are, and
 * the two digests that say which bytes those are (SPEC §7).
 */
export const MaterialEntrySchema = z.strictObject({
  selector: z.string(),
  harness: z.string(),
  sourceId: z.string(),
  captureDigest: z.string(),
  sourceDigest: z.string(),
  /** The file inside {@link MATERIAL_SESSIONS}, relative to the material's root. */
  file: z.string(),
  records: z.number().int(),
  bytes: z.number().int(),
});
export type MaterialEntry = z.infer<typeof MaterialEntrySchema>;

export const MaterialIndexSchema = z.strictObject({
  schema: z.literal(MATERIAL_SCHEMA),
  preparationId: z.string(),
  preparedAt: z.string(),
  machineId: z.string(),
  sessions: z.array(MaterialEntrySchema),
});
export type MaterialIndex = z.infer<typeof MaterialIndexSchema>;

/**
 * WHERE THE MATERIAL IS DECLARED, so another plugin's job may bind it (ADR 0044, #592).
 *
 * `atyrode.babel.prepare` writes the material into its own second sealed output and DECLARES
 * it exportable; a session Code posts under `atyrode.omp`'s operation then names it as an
 * input, and the hub extracts it read-only at {@link MATERIAL_ROOT}. A same-plugin binding
 * needs no export — this one is cross-plugin, and admission refuses
 * `input_not_exported:material` without the declaration.
 *
 * It is one string because the manifest, the request and `test/contract.test.ts` all name it
 * and three spellings of one output name is how a binding silently binds nothing.
 */
export const MATERIAL_EXPORT = MATERIAL_OUTPUT;

/**
 * THE FOUR NAMES BABEL GIVES AN ENGINE REFUSAL, because the operator acts differently on each.
 *
 * A call into Code refuses in two shapes and they arrive by different roads (ADR 0041): the
 * HOST's own refusal is a rejection whose sentence starts with its class
 * (`undeclared_dependency`, `dependency_unavailable`, `unknown_action`, `caller_ceiling`,
 * `capability`, `refused`, `dispatch_cycle`, `dispatch_depth`), and CODE's own refusal is a
 * resolved `{ refused: "code_…" }` value. Both are folded onto these four:
 *
 *   `engine_unavailable`  — there is no Code to ask: not declared, not installed, not enabled,
 *                           or too old to publish the door. The operator installs or upgrades.
 *   `engine_forbidden`    — Code's door demands authority this caller or this install does not
 *                           hold. The operator consents, or reinstalls Babel with the grant.
 *   `engine_stale_profile`— the profile moved between the read and the press. Re-read the list
 *                           and press again; the panel does exactly that.
 *   `engine_refused`      — Code said no, in its own word, which rides the detail.
 */
export const ENGINE_REFUSALS = {
  unavailable: "engine_unavailable",
  forbidden: "engine_forbidden",
  staleProfile: "engine_stale_profile",
  refused: "engine_refused",
} as const;
export type EngineRefusalCode = (typeof ENGINE_REFUSALS)[keyof typeof ENGINE_REFUSALS];

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
 * HOW A PRESET IS STARTED, which is the other half of the question above and the one the panel
 * could only answer by guessing (#279).
 *
 *   `explore` — a Code session over sealed material. It reaches a model, so it needs a CODE
 *               PROFILE: the operator picks a saved one or parametrizes a workspace in Code's
 *               generator, and the launch carries its container and the revision he was shown.
 *   `beat`    — one `atyrode.babel.scan`, Babel's own job. It reaches no model and takes no
 *               profile; a form that demanded one would be asking for a field nothing reads.
 *   `draw`    — a review the COORDINATOR picks, claims under a fence and dispatches through
 *               Code with a blinded projection of the record. The conductor owns that shared
 *               policy lane, so `launch` answers `draw_managed` rather than selecting work
 *               outside its cadence, reservations and budget.
 *
 * `doors/launch.ts` plans from this table and Watch's Start section renders from it, so the
 * two cannot disagree about which press posts what.
 */
export const PRESET_STARTS = ["explore", "draw", "beat"] as const;
export type PresetStart = (typeof PRESET_STARTS)[number];
export const PRESET_START: Record<(typeof PRESETS)[number], PresetStart> = {
  "read-whats-new": "explore",
  "explore-topic": "explore",
  "review-backlog": "draw",
  "file-and-tidy": "draw",
  "keep-going": "beat",
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
   * WHICH MODEL, AT WHICH THINKING LEVEL, ON WHOSE ACCOUNT — the drain's own record (#258).
   *
   * Babel chooses none of the three: a run's model, thinking level and account are Code's, and
   * the operator sets them on a Code profile or in Code's generator. What a drain needs is the
   * NAME of the account whose window it exists to spend, so it carries this and its panel says
   * it back. It is never posted to Code, which reads all three off the profile below.
   */
  session: SessionChoiceSchema.optional(),
  /**
   * THE CODE PROFILE THE RUN IS POSTED ON (#279), for a preset that reaches a model.
   *
   * Optional on the schema and required by the presets that need one, because `keep-going` is a
   * `scan` of Babel's own and reaches no model at all: a field the schema demanded would make
   * the beat carry a profile nothing would read. `startExplore` refuses by name when a model
   * preset names none, so the requirement is stated where it is true.
   */
  profile: CodeProfileSchema.optional(),
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
 * There is no dry preview beside it: what a run would cost is a composition's, and a
 * composition is Code's to make.
 */
export const LaunchRequestSchema = LaunchInputSchema.extend({ operation: OperationRefSchema });

/**
 * WHAT A LAUNCH ANSWERS when it started something: the run row's own id, the CODE job the
 * session runs as, the machine it runs on and what kind of run it is. `jobId` is Code's — the
 * job is posted by `atyrode.code.runSession` under `atyrode.omp`'s own operation — which is
 * why a run row keeps it beside the container it belongs to (#279).
 */
export const LaunchResultSchema = z.strictObject({
  runId: z.string(),
  jobId: z.string(),
  machineId: z.string(),
  kind: z.enum(["explore", "evaluate", "conductor", "prepare"]),
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
  /**
   * The `atyrode.babel.prepare` job whose sealed material this run reads, empty when it has
   * none. A run is started in two wakes (#592) and the first posts only that job, so while
   * `jobId` is empty this is the node a Stop is authorized at.
   */
  prepareJobId: z.string(),
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
  /**
   * THE MATERIAL THIS `prepare` SEALED, as its own index — the same document it wrote into the
   * `material` lease (#279).
   *
   * It is in the receipt as well as in the lease because the two readers are different. The
   * lease is bound into a SESSION's sandbox and read by the model; the receipt is ingested into
   * this hub's `runs` row, and the hub is what checks a submitted claim's locators against the
   * selection they were served from. Verifying out of the row costs one query; verifying out of
   * the lease would mean pulling a sealed archive of every session's records back through the
   * hub to read the twenty lines at the front of it.
   */
  material: MaterialIndexSchema.optional(),
  startedAt: z.string(),
  finishedAt: z.string(),
  closure: z.enum(["completed", "failed", "stopped", "skipped"]),
  reason: z.string().optional(),
  /**
   * WHAT THE CONTRACT REFUSED WHILE THE REST OF THE REVIEW STOOD (#305).
   *
   * `reason` above says why NOTHING was recorded; this says what was dropped from something that
   * was. A rule whose whole subject is one contribution refuses that contribution and the review
   * goes on without it, so a review with one bad contribution out of four stops discarding the
   * three good ones it had already paid for — and the model's compliance stops being invisible,
   * which is the half of #305 the strictness itself was never the answer to.
   *
   * `reason` is `<code>: <sentence>` here too, so the same `refusalCode` reads it back and a
   * refusal is countable whether it cost the review or only a contribution. It is absent, never
   * empty, on a review that had nothing refused; `counts.contributionsRefused` is how many.
   */
  refusedContributions: z
    .array(z.strictObject({ contribution: z.number().int().positive(), reason: z.string().min(1) }))
    .optional(),
  /**
   * THE SUBMISSION A REFUSED REVIEW ACTUALLY SENT (#311).
   *
   * `reason` says why nothing was recorded; this is what was refused, as the model wrote it.
   * Without it a refused run left a sentence and no evidence, so neither "did that class of
   * refusal fall after the contract changed?" nor "did the judgement change between attempts?"
   * was answerable from the store at all — and a measurement taken on faith is how a shape rule
   * gets loosened for the wrong reason.
   *
   * Absent when the review was recorded, and when the session submitted nothing this could
   * parse — there is no payload for a run that ended with no final message. `withheld` is the
   * honest answer for a submission too large to keep: the size is still evidence, the bytes are
   * not worth an unbounded row.
   */
  rejectedSubmission: z
    .strictObject({
      bytes: z.number().int().nonnegative(),
      payload: z.unknown().optional(),
      withheld: z.literal("too-large").optional(),
    })
    .optional(),
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
 * THE RESOURCE NAMES AN OPERATION MAY ASK A MACHINE FOR, and a machine answers by NAME: dev-01
 * advertises the `development` and `system` tool resources and nothing else, so a half that
 * asked for `git` by name was a half no machine in the fleet could satisfy — and
 * `engine.jobs.reviewDeployment` refused the whole native installation for it (#303). One
 * decision per tool, and each is a different answer:
 *
 * - `bun` is THIS BUNDLE'S, and the only tool it pins: it is the interpreter the machine half
 *   is written for, chosen here and moved here, so it ships as an artifact-managed
 *   `machine.tools` entry (url, digest and extracted-entry digest measured by
 *   `scripts/measure-runtime-tools.ts`). `jobResourceRequirements` drops a tool the
 *   installation's own declaration pins, which is what makes the operations satisfiable.
 * - `development` is the OWNER'S toolset, advertised by the fleet, and `git` lives inside its
 *   closure. So scan and prepare name the toolset rather than the binary; `machine/repository.ts`
 *   still resolves git at `RUNTIME_TOOL_BIN` first and on PATH second.
 * - `system` is the owner's reviewed, digest-promoted native closure. A pinned bun is
 *   dynamically linked (runtime-tools.json records the measured interpreter and DT_NEEDED list)
 *   and a job sandbox carries no libc, so every operation that runs it names this too.
 * - `restic` stays the owner's, by name, and only `archive` asks for it: upstream's whole Linux
 *   distribution is bare bzip2 and `MachineArtifactSchema` takes `raw`, `zip` or `tar.gz`, so
 *   there is nothing honest to pin. A machine that binds no restic disables that ONE operation
 *   (`jobResourceRequirements` is per-operation) and scan and prepare still reach `ready`.
 */
export const RUNTIME_TOOLS = ["bun", "development", "restic", "system"] as const;
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
 * credential an `s3:` locator needs therefore arrive together, never in halves, and no secret
 * reaches argv, the environment, the job request or the hub's journal.
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

// ---------------------------------------------------------------------------- the drain (#258)

/**
 * WHICH PRESETS A DRAIN MAY FAN OUT, and why it is these three and not all five.
 *
 * A drain keeps N jobs in flight and launches the next one itself. The two DRAWN presets
 * (`review-backlog`, `file-and-tidy`) do not work that way: the coordinator decides what is
 * reviewed, claims it under a fence and the conductor dispatches it, and a controller that
 * fanned those out would be a second implementation of the one thing the coordinator exists to
 * arbitrate — the lane, the fence, the reservation and the day's allowance (`doors/launch.ts`
 * says this about its own drawn branch). Running that loop faster is an operator's own budget
 * overlay (`setBudget`, #260), which raises the bound admission reads; a drain sets none,
 * because nothing it launches consults one (`server/drain.ts`).
 *
 * So a drain fans out exactly the presets that are launched DIRECTLY: two explores and the
 * beat. `keep-going` is in the list because it is the one lane that spends no model at all,
 * which makes it the honest rehearsal of the controller — the fan, the relaunch on settle and
 * the self-stop, proven without spending a cent of the window the drain exists to protect.
 */
export const DRAIN_PRESETS = ["read-whats-new", "explore-topic", "keep-going"] as const;
export const DrainPresetSchema = z.enum(DRAIN_PRESETS);
export type DrainPreset = (typeof DRAIN_PRESETS)[number];

/** The presets a drain fans out that reach a model; the rest spend nothing (see above). */
export const DRAIN_SPENDING_PRESETS: readonly DrainPreset[] = ["read-whats-new", "explore-topic"];

/**
 * The most jobs one machine may hold for a drain. It is the manifest's own
 * `limits.concurrentJobs` on the explore and evaluate operations: the hub refuses every posting
 * past that number at `execute` (atyrode/manifold#551), so a drain that asked for more would
 * spend its reservations on refusals. The door refuses above it by name rather than discovering
 * it one refused job at a time.
 */
export const DRAIN_CONCURRENT_MAX = 16;

/**
 * The deadline a drain gets when its target names none. Two hours is the 2026-09-13 drain's own
 * length, and a drain that could outlive the window it exists to spend is the failure the
 * operation was written against: the row carries the instant, so what stops it is one of its own
 * targets rather than somebody remembering.
 */
export const DRAIN_DEFAULT_TTL_MS = 2 * 60 * 60 * 1000;

/**
 * WHERE A DRAIN STOPS. At least one of the three is required, and the door refuses a target
 * that names none: "a drain without a target and a deadline is not a drain; it is a loop"
 * (`docs/runbook.md` §11.1). The two spend targets are measured in what the HUB metered on the
 * named account — `usage.inference` at settle, `inference_call` while running — and never in
 * the provider's percentage, which lags by minutes and moves in whole points.
 */
export const DrainTargetSchema = z.strictObject({
  /** Micro-dollars the hub has metered for this drain's jobs. */
  costMicros: z.number().int().positive().max(1_000_000_000).optional(),
  /** Output tokens, which is what a provider's window is mostly priced on. */
  outputTokens: z.number().int().positive().max(10_000_000_000).optional(),
  /** An ISO-8601 instant after which the drain launches nothing more. */
  deadline: z.string().min(1).max(64).optional(),
});
export type DrainTarget = z.infer<typeof DrainTargetSchema>;

/**
 * THE FOUR ENDINGS A DRAIN REACHES. `target` and `deadline` are the controller stopping itself,
 * which is the whole point of the operation; `stopped` is the operator's own act; `failed` is the
 * controller unable to continue — the machine gone, every launch refused — recorded rather than
 * retried for ever.
 */
export const DRAIN_ENDINGS = ["stopped", "target", "deadline", "failed"] as const;
export type DrainEnding = (typeof DRAIN_ENDINGS)[number];

/**
 * THE SIX STATES A DRAIN IS IN: running, the four endings above, and `closing`.
 *
 * `closing` IS THE ENDING WITH RECEIPTS STILL OUT. A drain stops launching the moment its target
 * or its deadline is reached, but the jobs it holds were paid for and keep going — a tick woken
 * by a settlement holds no `jobs:cancel`, so the cancels it asks for are refused by design — and
 * what those jobs metered is part of what this drain spent. So the row keeps them until each one
 * settles, folds their receipts as they land, and only then records the ending it was closed
 * with. A drain that dropped them would under-report its own spend by up to (N−1) runs, which is
 * precisely the figure `docs/runbook.md` §11.5 tells an operator to read.
 */
export const DRAIN_STATES = ["running", "closing", ...DRAIN_ENDINGS] as const;
export const DrainStateSchema = z.enum(DRAIN_STATES);
export type DrainState = (typeof DRAIN_STATES)[number];

/** What a drain has spent, in the meter's own units: micro-dollars, never a rounded dollar. */
export const DrainSpendSchema = z.strictObject({
  calls: z.number().int(),
  inputTokens: z.number().int(),
  outputTokens: z.number().int(),
  costMicros: z.number().int(),
});
export type DrainSpend = z.infer<typeof DrainSpendSchema>;

/**
 * What `drain.start` takes. The preset's own knobs travel with it, exactly as
 * `LaunchInputSchema` carries them, because a drain is that preset launched many times rather
 * than a different request.
 *
 * `session` is REQUIRED here and optional on a launch: a drain names the account it spends
 * before the button (#267), and "which account did that fan burn" is the question nothing on
 * the machine could answer on 2026-09-13. `reason` is required for the same reason `setBudget`
 * requires one — "why is the batch sixty-four today" is what nobody could answer either.
 */
export const DrainStartInputSchema = z.strictObject({
  machineId: bounded(120),
  preset: DrainPresetSchema,
  /**
   * THE CODE PROFILE EVERY JOB OF THIS FAN IS POSTED ON (#279), named before the button.
   *
   * It replaces the typed model/thinking/account a drain used to carry: Babel chooses none of
   * the three, and a field for them was Babel deciding what a run is. What the drain RECORDS
   * about them is copied from Code's own list at the start ({@link DrainProfileSchema}).
   */
  profile: CodeProfileSchema,
  concurrent: z.number().int().min(1).max(DRAIN_CONCURRENT_MAX),
  target: DrainTargetSchema,
  reason: z.string().trim().min(1).max(2000),
  /** For `explore-topic`: the entity whose cited sessions become the preparation. */
  entityId: EntityIdSchema.optional(),
  /** For `read-whats-new`: how far back, in days. */
  sinceDays: z.number().int().min(1).max(365).optional(),
  /** For `keep-going`: how long one beat runs, in minutes. */
  minutes: z.number().int().min(5).max(24 * 60).optional(),
  recipes: z.array(bounded(80)).max(16).default([]),
  /** Whether a preparation may hold Babel's own transcripts (#262); absent unless asked. */
  agentSessions: z.boolean().optional(),
});
/**
 * What the `drain.start` door takes: the request above plus the OPERATION NODE it is authorized
 * at, for the reason `LaunchRequestSchema` carries one — `machines:run` is granted at a node and
 * the host walks the declared target through the raw arguments before the handler is entered.
 */
export const DrainStartRequestSchema = DrainStartInputSchema.extend({
  operation: OperationRefSchema,
});

export const DrainStartResultSchema = z.strictObject({
  drainId: z.string(),
  machineId: z.string(),
  preset: DrainPresetSchema,
  concurrent: z.number().int(),
  /** How many jobs the start actually posted; fewer than `concurrent` is reported, not hidden. */
  launched: z.number().int(),
  /** The instant this drain launches nothing past, whether the target named one or not. */
  deadline: z.string(),
  /** The account this drain spends, as the operator reads it back. */
  account: z.string(),
  model: z.string(),
  /** Why fewer jobs than asked were posted, or empty. */
  note: z.string(),
});

/**
 * What `drain.stop` takes. The node is the OPERATION's rather than one job's: a drain holds
 * several jobs of one operation, a requirement resolves to exactly one node
 * (`plugin-host.ts`: one `ManifoldRef` per declared target), and the hub reads a job's consent
 * at its operation anyway (`job-service.ts` `consentFor`) — so consent at the operation is what
 * cancelling every job of this drain actually needs, and asking for it by name is honest about
 * the breadth.
 */
export const DrainStopInputSchema = z.strictObject({
  drainId: z.string().min(1).max(200),
  operation: OperationRefSchema,
  reason: z.string().max(2000).default(""),
});

export const DrainStopResultSchema = z.strictObject({
  drainId: z.string(),
  state: DrainStateSchema,
  /** How many in-flight jobs were cancelled. */
  cancelled: z.number().int(),
  /** A job the hub would not cancel, and the overlay it could not clear, in its own words. */
  note: z.string(),
});

/**
 * `drain.status` takes one drain or none. None is what a panel opening cold has: it does not
 * know a drain id until it has read one, and two doors — "list them" and "read this one" — would
 * be two answers to "what is draining right now".
 */
export const DrainQuerySchema = z.strictObject({
  drainId: z.string().min(1).max(200).optional(),
  limit: z.number().int().min(1).max(50).default(10),
});

/**
 * ONE DRAIN, AS THE PANEL WATCHES IT: the row, and the live fold over its jobs.
 *
 * Every field here is one of the numbers `docs/runbook.md` §11.4 says the panel must show, and
 * each has one thing it must do: `jobsAtModel` must be non-zero within 90 seconds of the first
 * launch; `outputTokensPerMinute` must be non-zero once a call has been metered; `spent` must
 * rise toward the target; `etaAt` must stay before `deadline`. A process count, a socket count
 * or a percentage from a home-made script is none of them (post-mortem O1, O7).
 *
 * `spent` is the drain's WHOLE spend — what its settled jobs metered plus what its live ones
 * have metered so far — because that is the figure a target is judged against. `settled` is the
 * durable part of it, so a reader can tell a receipt from a fold in progress.
 */
export const DrainStatusSchema = z.strictObject({
  drainId: z.string(),
  machineId: z.string(),
  preset: DrainPresetSchema,
  state: DrainStateSchema,
  reason: z.string(),
  startedAt: z.string(),
  startedBy: z.string(),
  finishedAt: z.string(),
  concurrent: z.number().int(),
  target: DrainTargetSchema,
  /** The account and model this drain spends (#267), named before the button and after it. */
  account: z.string(),
  model: z.string(),
  jobsLaunched: z.number().int(),
  jobsSettled: z.number().int(),
  jobsLive: z.number().int(),
  jobsAtModel: z.number().int(),
  jobsStalled: z.number().int(),
  spent: DrainSpendSchema,
  settled: DrainSpendSchema,
  /** Output tokens a minute over the last three minutes of samples; 0 before two of them. */
  outputTokensPerMinute: z.number(),
  /** Micro-dollars a minute over the same window, which is what a cost target closes against. */
  costMicrosPerMinute: z.number(),
  /** When this rate reaches the target, or empty: no rate, or no spend target to reach. */
  etaAt: z.string(),
  /** Refused submissions by the code `machine/results.ts` names; paid work, no result. */
  refusals: z.record(z.string(), z.number().int()),
  /** How each of this drain's jobs closed, by closure. */
  closures: z.record(z.string(), z.number().int()),
});
export type DrainStatus = z.infer<typeof DrainStatusSchema>;

export const DrainStatusResultSchema = z.strictObject({
  drains: z.array(DrainStatusSchema),
});
