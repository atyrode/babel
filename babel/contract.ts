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
/**
 * The optional judgement part. It is named here because every id of the family is, and because
 * `test/contract.test.ts` pins its manifest to this name — not because anything of the baseline
 * calls it: no door, no panel and no cycle of Babel's names this plugin, which is what makes the
 * part removable.
 */
export const JEV_PLUGIN_ID = "atyrode.babel.jev";

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

/**
 * THE CLOSED VOCABULARY OF NEXT ACTIONS A RUN MAY PROPOSE (#340), and the two answers the
 * operator may give one.
 *
 * A run reads a corpus and can see what should happen next; until now it had nowhere to say so,
 * and `machine/results.ts` left the field out on purpose because a field nothing could land is
 * worse than its absence. The vocabulary is CLOSED because an open one turns a proposal into
 * prose: five known destinations a model chooses between can be routed, counted and acted on,
 * and a sixth one it invented can only be read.
 *
 * The words are the retired product's own (`v0.4.0:internal/disposition`, `Kinds()`), unchanged,
 * so the rows the crossing left stranded import as themselves rather than through a translation
 * nobody could check afterwards.
 *
 * `RULINGS` above and these are different vocabularies about different things, and conflating
 * them is exactly the ambiguity the Go package split itself in two to avoid: a ruling is a
 * verdict on the RECORD — is this claim any good — and a decision here is an answer about the
 * ACTION — should this be done. "Accepted" would otherwise mean two things in one corpus.
 */
export const NEXT_ACTIONS = [
  /** Render the record as a GitHub issue draft. Babel publishes nothing; the operator does. */
  "draft-issue",
  /** Route the record into the ledger's proposed-until-authorized facts (§4.8). */
  "propose-reality-fact",
  /** Keep it as an operator-specific memory. */
  "store-memory",
  /** Put a question to the operator. */
  "ask-question",
  /** Spend another exploration pass on the record. */
  "develop-further",
] as const;
export const NextActionSchema = z.enum(NEXT_ACTIONS);
export type NextAction = z.infer<typeof NextActionSchema>;

/**
 * The operator's answer to a proposed action. There are exactly two: every action is a proposal
 * until a person authorizes it, and a third value would be a way of half-authorizing one.
 */
export const NEXT_ACTION_DECISIONS = ["accepted", "declined"] as const;
export const NextActionDecisionSchema = z.enum(NEXT_ACTION_DECISIONS);
export type NextActionDecision = z.infer<typeof NextActionDecisionSchema>;

/** What a proposed action is standing at, derived from its ledger and never stored. */
export const NextActionStandingSchema = z.enum(["proposed", "accepted", "declined"]);

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

/**
 * HOW A RECORD CAME TO NAME A REPOSITORY, which is two claims rather than one (#183).
 *
 * `observed` is Babel's own: the scan probed the workspace a cited session worked in and git
 * answered, so the repository is a fact this deployment established (`machine/repository.ts`,
 * `sessions.repository_remote`). `named` is the evidence's: a run read a repository in a
 * transcript and nothing of Babel's ever stood in that checkout. Folding the two together would
 * let a repository a conversation merely mentioned read as one Babel saw, which is the quiet
 * kind of error 42.7% of the corpus not naming its codebase at all is the loud kind of.
 */
export const REPOSITORY_PROVENANCES = ["observed", "named"] as const;
export const RepositoryProvenanceSchema = z.enum(REPOSITORY_PROVENANCES);

/**
 * States a git remote URL as host/owner/repo, and returns "" for a URL it cannot read that
 * way.
 *
 * The normalization is what makes one repository one topic. git's own URL grammar writes the
 * same GitHub repository as git@github.com:atyrode/manifold.git,
 * https://github.com/atyrode/manifold, https://token@github.com/atyrode/manifold.git/ and
 * ssh://git@github.com/atyrode/manifold — four strings, one project — so the scheme, the
 * credentials, the ".git" suffix and the trailing slash are removed and the ssh short form's
 * colon becomes the separator it means.
 *
 * A local path remote ("/srv/git/thing", "../other") normalizes to nothing: it names a
 * directory on one machine, which is a locator and not an identity, and the common directory
 * is already the better answer for it.
 *
 * It is spelled here, beside the names, rather than beside the git probe that first needed it:
 * three consumers share it now — the probe, the answer contract a run states a repository in,
 * and the reader that decides whether those two agree — and a remote canonicalized two ways is
 * one repository read as two.
 */
export function normalizeRemote(url: string): string {
  let remote = url.trim();
  if (remote === "") return "";
  const scheme = remote.indexOf("://");
  if (scheme >= 0) {
    remote = remote.slice(scheme + 3);
  } else {
    const colon = remote.indexOf(":");
    // The scp-like short form, [user@]host:owner/repo. Its colon is a separator rather than a
    // port, which is why it is rewritten here and not for a URL that carried a scheme.
    if (colon >= 0 && !remote.slice(0, colon).includes("/")) {
      remote = remote.slice(0, colon) + "/" + remote.slice(colon + 1);
    }
  }
  // Credentials in a URL that had a scheme: user[:password]@host.
  const at = remote.indexOf("@");
  if (at >= 0) remote = remote.slice(at + 1);
  remote = remote.replace(/^\/+|\/+$/gu, "");
  if (remote === "" || remote.startsWith(".")) return "";
  const parts: string[] = [];
  for (const part of remote.split("/")) {
    if (part === "" || part === ".") continue;
    parts.push(part);
  }
  if (parts.length < 2) return "";
  const last = parts.length - 1;
  const tail = parts[last];
  if (tail === undefined) return "";
  parts[last] = tail.endsWith(".git") ? tail.slice(0, -".git".length) : tail;
  if (parts[last] === "") return "";
  // A host element carries a dot or is localhost; anything else is a path, and a path remote
  // is a locator rather than a repository identity.
  const first = parts[0] ?? "";
  const host = first.includes(":") ? first.slice(0, first.indexOf(":")) : first;
  if (!host.includes(".") && host !== "localhost") return "";
  parts[0] = host;
  return parts.join("/");
}

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
  /**
   * RETRIEVING OVER THE CORPUS BY WHAT A RECORD SAYS (#337).
   *
   * It is a reading door beside `feed` rather than a mode of it, because the two answer
   * different questions with different guarantees: `feed` enumerates and filters structured
   * columns and is exhaustive by construction, and this one RANKS by relevance and is only as
   * complete as the indexes behind it. Folding them together would make one result shape carry
   * "everything that matched these filters" and "the best guesses about these words" under one
   * name, and a caller could not tell which it had.
   *
   * Its answer says which of the two indexes found each record and how much of the corpus each
   * index holds, because a partially built index that answered silently would be indistinguishable
   * from a corpus that holds nothing about the question.
   */
  search: "search",
  // the operator's acts
  rule: "rule",
  /**
   * ANSWERING A NEXT ACTION A RUN PROPOSED (#340): accepted, or declined, with the operator's
   * own words. It is a second door beside `rule` rather than a ruling with a wider vocabulary,
   * because the two answer different questions — `rule` judges the claim, this judges the
   * action — and one door with a mode is how "accepted" comes to mean two things at once.
   */
  decide: "decide",
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
   * THE ONE WRITE A DEPENDENT PLUGIN GETS (#410): a typed suggestion about one record revision,
   * attributed to the plugin the operator allow-listed and never to him. It writes a
   * `next_actions` row and can reach no other table, so the frontier keeps its two writer
   * classes; `suggestions` is its reading half, and what a retroactive sweep sizes itself from.
   */
  suggest: "suggest",
  suggestions: "suggestions",
  /**
   * RENDERING A RECORD FOR A DESTINATION (§4.6): a sanitized issue draft, an agent brief, or an
   * operator note. The answer is a filename and its text — a file the operator takes. There is
   * no `publish` beside it and there will not be one: Babel opens no issue, writes into no
   * repository and launches no agent at a destination, and this door holds no authority that
   * would let it.
   */
  export: "export",
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
   * VERIFYING THE ARCHIVE, AND RESTORING OUT OF IT (#338).
   *
   * It posts `atyrode.babel.verify` on the machine that holds the repository: `restic check`,
   * structurally or over the stored bytes, and optionally ONE catalogued session restored from
   * a named snapshot and proved byte-exact against the digest `scan` recorded. The verdict
   * arrives as the run's receipt, which the `run` door already serves.
   *
   * There is no companion act for forgetting, pruning or unlocking, and there will not be one
   * by accident: the machine half admits a closed set of restic verbs and none of those three
   * is in it.
   */
  verify: "verify",
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
  /**
   * THE HOST SERVICES THIS BUNDLE'S OPERATIONS BIND, composed and installed (#400). Owner only,
   * for the same reason the crossing is: `engine.services` admits a configuration read or write
   * only from the hub's owner holding `services:configure` on that machine, and no capability in
   * Manifold's vocabulary means "the owner".
   *
   * `previewServices` composes the policy each `services` binding in the manifest declares,
   * reports what is installed on that machine and whether its credential came up, and digests
   * the whole of it. `installServices` echoes that digest and applies the policies as a
   * compare-and-swap on the configuration revision the preview was read against, so a policy
   * that moved underneath is refused rather than overwritten by a screen nobody re-read.
   *
   * NEITHER CARRIES A CREDENTIAL VALUE, and there is nowhere in their schemas one could be
   * written: a policy names a credential by reference and the machine's owner resolves it. The
   * preview says which reference and which file on that machine, which is the whole of what
   * Babel may know about it.
   */
  previewServices: "previewServices",
  installServices: "installServices",
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

/**
 * THE THREE SURFACES a post is routed to, by who acts on it next (#351).
 *
 * Routing precedes ranking: one ordering of everything puts a record that needs the operator's
 * judgement in the same list as a record an agent could execute unattended and a record worth
 * keeping but not worth showing, so the first is buried under the volume of the third. The
 * route is read off the record's kind and its standing, both of which are already columns —
 * there is no judgement model here and none is wanted.
 */
export const POST_SURFACES = ["desk", "queue", "shelf"] as const;
export const PostSurfaceSchema = z.enum(POST_SURFACES);
export type PostSurface = z.infer<typeof PostSurfaceSchema>;

/** The surfaces a reader may ask for: the three, plus the one list they were cut out of. */
export const FEED_SURFACES = [...POST_SURFACES, "all"] as const;
export const FeedSurfaceSchema = z.enum(FEED_SURFACES);
export type FeedSurface = z.infer<typeof FeedSurfaceSchema>;

/**
 * HOW WELL ESTABLISHED a post is (#354) — the second axis, and the one the front page had no
 * word for.
 *
 * What a record is ABOUT and how well established it is are different questions, and until now
 * only the first was on a row: the topics carried the subject, and the status was spread across
 * a standing nothing rendered, a score in the gutter and a sentence of prose. So "important and
 * shaky" and "trivial and certain" read alike, which is the distinction that decides what to do
 * about either.
 *
 * The vocabulary is a fold of two columns this store already keeps and invents no third:
 *
 *   - `settled` — a ruling was made (`dispositions`), or the question reached a state that
 *     awaits nobody. The operator's act decides it, even where his reviewers disagreed: Babel
 *     votes and the operator rules.
 *   - `contested` — his reviewers are on both sides inside one role (`assessments`), which is
 *     the shakiest a record with evidence gets.
 *   - `reviewed` — assessed and not split, and still undecided.
 *   - `unsettled` — no ruling and no vote: nothing has judged it at all.
 */
export const ESTABLISHED = ["unsettled", "contested", "reviewed", "settled"] as const;
export const EstablishedSchema = z.enum(ESTABLISHED);
export type Established = z.infer<typeof EstablishedSchema>;

/**
 * THE KEY THE DESK IS GROUPED BY (#352), so one concept occupies it once.
 *
 * A concept observed forty times took forty slots and spent the operator's attention on the
 * repetition rather than on the concept. Both keys are existing columns — the topic a record is
 * filed under, and the recipe that produced it — so this is a grouping over data Babel already
 * has. Grouping by MEANING is a different thing, needs a judgement model, and is not this.
 *
 * The study built this grouping to check it and reported it as QUALIFIED: the grouping method
 * is itself the thing to validate, not only what it produces. `none` is therefore the door's
 * default and the grouping is always something a reader turned on.
 */
export const FEED_GROUPINGS = ["none", "topic", "recipe"] as const;
export const FeedGroupingSchema = z.enum(FEED_GROUPINGS);
export type FeedGrouping = z.infer<typeof FeedGroupingSchema>;

// ---------------------------------------------------------------------------- the feed

export const FeedQuerySchema = z.strictObject({
  sort: FeedSortSchema.default("next"),
  window: FeedWindowSchema.default("day"),
  kinds: z.array(PostKindSchema).max(POST_KINDS.length).default([]),
  /**
   * Which surface the list is. The desk is the default because it is the one that awaits him;
   * the shelf is reached by asking for it, which is the whole of "never shown unprompted".
   */
  surface: FeedSurfaceSchema.default("desk"),
  /**
   * The status axis, filtered independently of the subject one: `topic` narrows what a post is
   * about, this narrows how well established it is, and the two compose.
   */
  established: z.array(EstablishedSchema).max(ESTABLISHED.length).default([]),
  /** Which existing key the list is grouped under, or `none` for a row per record. */
  group: FeedGroupingSchema.default("none"),
  /** A topic by entity id or name; `unfiled` is the records under nothing. */
  topic: z.string().max(200).optional(),
  limit: z.number().int().min(1).max(100).default(25),
  offset: z.number().int().min(0).default(0),
});
export type FeedQuery = z.infer<typeof FeedQuerySchema>;

/** One reviewer's vote as the row's strip shows it. */
export const FeedVoteSchema = z.strictObject({
  role: RoleSchema.or(z.literal("")),
  vote: VoteSchema,
});

export const FeedPostSchema = z.strictObject({
  id: z.string(),
  kind: PostKindSchema,
  /** Which of the three surfaces this post is routed to, from its kind and its standing. */
  surface: PostSurfaceSchema,
  title: z.string(),
  standing: z.string(),
  /** How well established it is: the fold of its standing and its reception (#354). */
  established: EstablishedSchema,
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

/**
 * One group of the list: what holds it together, and which of its records this page carries.
 *
 * `keyKind` is on the group rather than implied by the query because a record belonging to no
 * group is still in the list — as a group of one, keyed `none` — and a reader has to be able
 * to tell "these forty are one topic" from "this one is by itself". `records` is the group's
 * true size and `posts` is what came with this page, so a group larger than the page says so
 * rather than looking complete.
 */
export const FeedGroupSchema = z.strictObject({
  key: z.string(),
  keyKind: z.enum([...FEED_GROUPINGS]),
  label: z.string(),
  records: z.number().int(),
  posts: z.array(z.string()),
});
export type FeedGroup = z.infer<typeof FeedGroupSchema>;

export const FeedResultSchema = z.strictObject({
  posts: z.array(FeedPostSchema),
  /**
   * The size of the eligible set in the unit the query asked for: records, or groups when it
   * asked for a grouping. It is what the page is cut out of, so it has to be counted in the
   * same unit the page is.
   */
  total: z.number().int(),
  /**
   * How many posts are on the desk, whatever surface this query asked for. It travels on every
   * answer because "is this a plausible amount of work" is a question a reader has while
   * looking at the queue or the shelf, and a count he has to change surface to see is a count
   * he does not have.
   */
  desk: z.number().int(),
  builtAt: z.string(),
  notice: z.string(),
  /** Empty when nothing was grouped; otherwise one entry per group this page carries. */
  groups: z.array(FeedGroupSchema),
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
      session: z
        .strictObject({ selector: z.string(), title: z.string(), href: z.string() })
        .nullable(),
      note: z.string(),
      line: z.number().int().nullable(),
      /**
       * WHAT BECAME OF THE EXCERPT WHEN BABEL LOOKED FOR IT (#348), one of
       * {@link CITATION_OUTCOMES} — and EMPTY for a record written before anything looked,
       * which is the whole imported corpus. Empty is not "clean": a page that rendered an
       * unchecked citation as verified would be making the claim the check exists to stop.
       */
      verification: z.string(),
    }),
  ),
  /**
   * HOW MANY RUNS THIS RECORD RESTS ON, beside how many supports it has.
   *
   * Three observations under one finding read as corroboration; three observations from ONE run
   * are one reading restated. The surface said nothing about the difference, and in this
   * deployment's own corpus 175 of 207 findings rest on a single run while every one of 116
   * proposals shares its finding's run — so "corroborated" was a word the page implied and the
   * data did not support. It is computed at read time from `records.run_id` and the typed edges,
   * and a record with no supports answers zero rather than being absent.
   */
  corroboration: z.strictObject({
    supports: z.number().int().min(0),
    distinctRuns: z.number().int().min(0),
  }),
  /**
   * WHICH CODEBASE THIS RECORD CONCERNS, and on whose word (#183).
   *
   * 42.7% of this deployment's records cannot say which codebase they are about, measured over
   * 974 sampled of 6,038 and replicated within four points on a disjoint sample — the largest
   * single defect the corpus has, and one no ranking, routing or retrieval change touches. The
   * join was always there and nothing walked it: a record rests on observations, an observation
   * cites a session, and the catalog holds the repository that session's workspace was in.
   *
   * `remote` is `host/owner/repo` and is never empty: a checkout that declares no origin names
   * no repository a reader of another machine can act on, and a directory on one host is a
   * locator rather than an identity, so it is absent instead of guessed. `commit` and
   * `reference` are what the EVIDENCE recorded, never a probe of a checkout as it stands now —
   * the commit a run read a repository at is a fact about the past, and the working tree's
   * current HEAD is not evidence of it. Both are empty when the evidence recorded neither.
   *
   * An empty list is a record with no repository, which renders as nothing at all.
   */
  repository: z.array(
    z.strictObject({
      remote: z.string().min(1),
      commit: z.string(),
      reference: z.string(),
      provenance: RepositoryProvenanceSchema,
    }),
  ),
  reception: z.strictObject({
    byRole: z.array(
      z.strictObject({
        role: RoleSchema,
        support: z.number().int(),
        oppose: z.number().int(),
        unsure: z.number().int(),
        opposingRationales: z.array(z.string()),
      }),
    ),
    contested: z.boolean(),
    operatorHistory: z.array(
      z.strictObject({ stance: z.string(), reason: z.string(), at: z.string() }),
    ),
  }),
  machinery: z.record(z.string(), z.string()),
  related: z.array(
    z.strictObject({
      relation: z.string(),
      id: z.string(),
      kind: RecordKindSchema,
      title: z.string(),
    }),
  ),
  plan: z
    .strictObject({ kind: z.enum(["topic", "backlog"]), operation: z.string(), state: z.string() })
    .nullable(),
  /**
   * WHAT A RUN PROPOSED BE DONE ABOUT THIS RECORD, and what the operator answered (#340).
   *
   * It rides on the peel rather than on the listing row because it is a decision, and §8.6
   * gives a row one line of claim and at most three facts — a proposed action read off a list
   * is one accepted without reading the record it is about. `standing` is derived from
   * `history`, never stored, so a status can never disagree with the entries behind it; an
   * empty `history` is `proposed`, which is different from having been declined.
   *
   * An empty list renders as nothing at all, which is the honest shape for the whole imported
   * corpus: nothing proposed an action on any of it.
   */
  nextActions: z.array(
    z.strictObject({
      id: z.string(),
      kind: NextActionSchema,
      summary: z.string(),
      rationale: z.string(),
      /** The run that proposed it, which is what makes an acceptance rate readable by lens. */
      proposedBy: z.string(),
      at: z.string(),
      standing: NextActionStandingSchema,
      history: z.array(
        z.strictObject({
          decision: NextActionDecisionSchema,
          note: z.string(),
          by: z.string(),
          at: z.string(),
        }),
      ),
    }),
  ),
});
export type RecordPeel = z.infer<typeof RecordPeelSchema>;

// ---------------------------------------------------------------------------- searching (#337)

/**
 * WHAT A SEARCH IS ASKED, and it is one line of prose plus two bounds.
 *
 * There is no field for an operator, a filter grammar or a page: the corpus is one hub's and the
 * feed is where structured filtering already lives. What this door adds is the one thing the feed
 * cannot do, which is answer "about what".
 *
 * The query is bounded at 512 characters because it is a question and not a document — the door
 * that carries a document is `launch`. A longer one is refused rather than truncated: a search
 * whose last third was silently dropped returns a confident answer to something nobody asked.
 */
export const SearchQuerySchema = z.strictObject({
  query: bounded(512),
  limit: z.number().int().min(1).max(100).default(20),
  /** Record kinds to answer with; an empty list means every kind. */
  kinds: z.array(RecordKindSchema).max(RECORD_KINDS.length).default([]),
});
export type SearchQuery = z.infer<typeof SearchQuerySchema>;

/**
 * HOW MUCH OF THE CORPUS EACH INDEX HOLDS, on every answer.
 *
 * A partially built index that answered silently is indistinguishable from a corpus that holds
 * nothing about the question, and the two want opposite responses from a reader: one is "ask
 * again later", the other is "nobody has looked at this". So the counts travel with the hits
 * rather than living behind a second door somebody would have to know to open.
 *
 * `model` is the embedding model the vectors read were produced by, and it is empty when none
 * were read. A vector whose model nobody recorded is a vector nobody can tell is stale, and
 * `stale` counts the rows some earlier model made — they are not compared and not deleted, they
 * are the backfill's remaining work.
 */
export const SearchCoverageSchema = z.strictObject({
  records: z.number().int().min(0),
  keyworded: z.number().int().min(0),
  embedded: z.number().int().min(0),
  /** Records with no text to embed; they are covered, because they can never be more. */
  empty: z.number().int().min(0),
  stale: z.number().int().min(0),
  model: z.string(),
});

/**
 * One record a search matched, and which index matched it.
 *
 * `score` is the fused rank and is comparable only within one answer: the two indexes score on
 * incommensurable scales — bm25 is unbounded and negative, cosine is bounded in [-1, 1] — so
 * their RANKS are fused and the underlying numbers travel beside the result for a reader who
 * wants to see why. `keyword` and `meaning` are null where that index did not match the record,
 * which is a different fact from matching it badly.
 */
export const SearchHitSchema = z.strictObject({
  id: RecordIdSchema,
  title: z.string(),
  via: z.enum(["keyword", "meaning", "both"]),
  score: z.number(),
  keyword: z.number().nullable(),
  meaning: z.number().nullable(),
});

/**
 * A SEARCH'S ANSWER, AND ITS OWN ACCOUNT OF ITSELF.
 *
 * `meaning` is `absent`, `partial` or `full`, and `meaningAbsent` is the sentence saying why when
 * it is absent — no service installed, the service did not answer, or nothing has been embedded
 * yet. All three are ordinary states of a deployment rather than errors, and a door that raised
 * for them would make a keyword search fail because an account ran dry.
 *
 * `scanned`, `rescored` and `approximate` are what let a caller tell a miss from a near miss.
 * The meaning index is scanned through a one-bit-per-dimension sketch and only the best
 * `rescored` of `scanned` are scored exactly, so an answer where the two differ was drawn from a
 * prefilter that may have cut a better match. `scanned` of 0 means there was nothing to compare
 * against at all. A search that silently missed would be worse than one that says it is
 * approximate.
 */
export const SearchResultSchema = z.strictObject({
  hits: z.array(SearchHitSchema),
  coverage: SearchCoverageSchema,
  meaning: z.enum(["absent", "partial", "full"]),
  meaningAbsent: z.string(),
  scanned: z.number().int().min(0),
  rescored: z.number().int().min(0),
  approximate: z.boolean(),
});
export type SearchResult = z.infer<typeof SearchResultSchema>;

// ---------------------------------------------------------------------------- the thread

/** `thread` takes the record whose conversation is wanted; the same identifier `record` takes. */
export const ThreadQuerySchema = z.strictObject({ id: RecordIdSchema });

export const CommentSchema: z.ZodType<Comment> = z.lazy(() =>
  z.strictObject({
    id: z.string(),
    kind: z.enum([
      "comment",
      "question",
      "contribution",
      "refinement",
      "answer",
      "reconsideration",
    ]),
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
    .strictObject({
      kind: z.string(),
      identity: z.string(),
      remote: z.string(),
      paths: z.array(z.string()),
    })
    .nullable(),
  posts: z.number().int(),
  awaiting: z.number().int(),
  latestAt: z.string(),
  interest: z.strictObject({
    state: InterestStateSchema.or(z.literal("")),
    reason: z.string(),
    at: z.string(),
    by: z.string(),
  }),
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
  /**
   * WHICH LENSES HAVE LOOKED AT THIS TOPIC, one row per recipe the policy holds — and the rows
   * at zero are the point. Nothing could say "this method has produced nothing about this
   * subject", so nothing could propose the pair, and a coverage gap that is only an absence is
   * one nobody notices. A hub whose policy names no recipe answers an empty array.
   */
  coverage: z.array(
    z.strictObject({
      recipeId: z.string(),
      title: z.string(),
      records: z.number().int().min(0),
      /**
       * Whether a launch of this lens would be accepted: the policy enables it and gives it a
       * body, which is what `server.ts`'s `cookbook()` admits. The topic page offers a run of a
       * never-looked lens (#330), and an offer the door could only refuse is a control the
       * operator discovers by pressing it.
       */
      runnable: z.boolean(),
    }),
  ),
  feed: FeedResultSchema,
});

// -------------------------------------------- refinement (§4.7) and output projections (§4.6)

/*
  TWO THINGS A RECORD CAN HAVE DONE TO IT ONCE IT EXISTS, and they are here together because
  both are the same shape of act: the operator decides, and Babel writes nothing outward.

  A REFINEMENT (§4.7) is a separately reviewable proposal that names the exact revision and JSON
  Pointer it would change and the replacement it proposes. It is written by a review
  (`server/engine/review.ts`) and applied — as a supersession, never an edit — by the operator's
  acceptance of it (`store/acts.ts`). The pointer is into the PROJECTION a review was shown, not
  into the payload column: `/title` and `/payload/problem` are the two shapes it takes, because
  `server/conductor.ts`'s `project()` is what a reviewer reads and what its pointer indexes.

  AN OUTPUT PROJECTION (§4.6) renders a proposal for a destination. Exactly one of the three
  destinations leaves this deployment, and §4.6 is the thing that says which: the issue draft is
  the one it calls SANITIZED, and the agent brief is the one it requires to carry "evidence
  locators an agent can open rather than excerpts it must trust". So a classification governs the
  issue draft and governs nothing else — and there is no `publish` here, in any spelling. Babel
  renders a file; the operator takes it.
*/

/**
 * The payload key a proposal carries its refinement under. Spelled once because two files write
 * and read it — the review that proposes and the act that applies — and a key one of them
 * misspelled would be a refinement nothing could ever find.
 */
export const REFINEMENT_KEY = "refinement";

/** One refinement, as the proposal that carries it spells it under `payload.refinement`. */
export const RefinementSchema = z.strictObject({
  /** The record the refinement rewords, which is the revision's own root-line identity. */
  targetRecordId: RecordIdSchema,
  /** The exact immutable revision the reviewer read. A newer one is a different wording. */
  targetRevisionId: RecordIdSchema,
  /** How many refinements deep this one is, against the policy's bound. */
  depth: z.number().int().min(0),
  /**
   * The JSON Pointer into the reviewed projection. Never empty: §4.7 requires the EXACT pointer,
   * and a refinement naming the record as a whole names no wording that could be replaced.
   */
  targetPath: z.string().min(1).startsWith("/"),
  reason: z.string().min(1),
  replacement: z.string().min(1),
  sourceRole: RoleSchema,
});
export type Refinement = z.infer<typeof RefinementSchema>;

/** What accepting a refinement did: the revision it wrote, or the refusal that stopped it. */
export const RefinementOutcomeSchema = z.strictObject({
  targetRecordId: RecordIdSchema,
  targetPath: z.string(),
  /** The superseding revision the acceptance wrote, and "" when it wrote none. */
  revisionId: z.string(),
  applied: z.boolean(),
  /** Why nothing was written, in the store's own sentence. */
  error: z.string().optional(),
});

/** §4.6's three destinations. A fourth is a destination, never a publishing capability. */
export const PROJECTIONS = ["issue-draft", "agent-brief", "operator-note"] as const;
export const ProjectionSchema = z.enum(PROJECTIONS);
export type Projection = z.infer<typeof ProjectionSchema>;

export const ExportInputSchema = z.strictObject({
  id: RecordIdSchema,
  projection: ProjectionSchema,
});

export const ExportResultSchema = z.strictObject({
  recordId: RecordIdSchema,
  projection: ProjectionSchema,
  /** The classification the redaction was decided from; "" when the record states none. */
  classification: z.string(),
  /** A name for the file the operator saves. Babel writes no file and sends nothing. */
  filename: z.string(),
  contentType: z.literal("text/markdown"),
  /**
   * What the classification kept out, named. A reader has to be able to tell a projection that
   * carries everything from one that was cut down, or a redacted draft reads as the whole record.
   */
  withheld: z.array(z.string()),
  text: z.string(),
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
  /**
   * What the ruling did to the refinement the proposal carries. It is a sibling of `plan` rather
   * than a variant of it because a refinement is not a plan row: `plans.kind` is a closed
   * `('topic','backlog','answer')` and SQLite cannot widen a CHECK, so the refinement travels in
   * the proposal's own payload and is applied from there.
   */
  refinement: RefinementOutcomeSchema.nullable(),
});

/**
 * THE OPERATOR'S ANSWER TO ONE PROPOSED ACTION (#340). It names the proposal, never the record:
 * a record may carry several, and "accept the next action on this finding" is ambiguous the
 * moment a second run proposes a second one.
 */
export const DecideInputSchema = z.strictObject({
  nextActionId: z.string().regex(/^nxt_[0-9a-f]{8,64}$/),
  decision: NextActionDecisionSchema,
  note: z.string().max(4000).default(""),
});

export const DecideResultSchema = z.strictObject({
  id: z.string(),
  /** The record it was proposed on, so the surface knows what to re-read. */
  recordId: RecordIdSchema,
  standing: NextActionStandingSchema,
  /** The entry's place in this proposal's ledger; a reconsideration is a higher one. */
  seq: z.number().int(),
  at: z.string(),
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

// ------------------------------------------------------- what an allowed plugin may suggest

/*
  ONE NARROW DOOR, AND IT WRITES A SUGGESTION (#410, decided on #360).

  Babel has exactly two writer classes and neither of them is a dependent plugin: the operator is
  AUTHENTICATED and his acts arrive under his own principal, and a run is MEDIATED — its output
  never touches a door, the conductor ingests it against a schema the baseline owns. A plugin that
  judges records holds neither, so admitting it as a third writer on the frontier would put
  `records`, `edges`, `assessments`, `dispositions` and `status_events` behind a rule somebody has
  to remember at every one of them, for ever.

  So it writes a `next_actions` row and nothing else. That table already exists, its `kind` is
  already a CLOSED vocabulary, and `proposed_by_kind` already has an author slot that is neither
  the operator nor a run — `engine`. A suggestion therefore renders where the record is read, as
  one more proposed action, and the operator accepts or declines it through `decide` like any
  other. There is no second review surface and no second acceptance vocabulary.

  WHO IS SUGGESTING IS NEVER AN ARGUMENT, and it is not the plugin id either, because the host
  does not supply one: `IsolateDispatchCtxSchema` (manifold `protocol/src/isolate.ts`) carries the
  trace, the PRINCIPAL, its caps, its root flag, its container scope and the clock, and
  `GuestCtx.pluginId` (manifold `plugin-kit/src/server.ts`) is this plugin's OWN manifest id. The
  caller's plugin id is known host-side (`plugin-host.ts`'s `actionCalls`) and reaches the trace
  ledger and the cycle bound, never the handler. What the host DOES authenticate is
  `ctx.principal`, so the suggester is resolved from the principal through the operator's own
  allow-list below, and the input document has nowhere to name an author: these are strict
  objects, so a field trying to would be refused unread.
*/

/**
 * ONE ALLOWED SUGGESTER, as the policy document carries it: the principal the host authenticates,
 * and the plugin whose name its suggestions are written under.
 *
 * It is a LIST OF NAMES rather than "any plugin that declares a dependency on Babel", and the
 * difference is authorising one plugin versus authorising a category — the only thing selecting a
 * caller otherwise is a dependency edge the caller declares about ITSELF, so the category version
 * grants every future Babel-dependent plugin write access to the queue by default.
 */
export const SuggesterSchema = z.strictObject({
  /** The principal a suggestion arrives under; `ctx.principal.id`, which no caller chooses. */
  principalId: bounded(200),
  /** The plugin its suggestions are attributed to, written into `next_actions.proposed_by_id`. */
  pluginId: z.string().regex(/^[a-z0-9][a-z0-9.-]{1,79}$/),
  /** Why the operator allowed it. Read by nobody; kept because a grant with no reason ages badly. */
  note: z.string().max(400).default(""),
});
export type Suggester = z.infer<typeof SuggesterSchema>;

/**
 * WHAT A SUGGESTION SAYS. `revision` is the `records.seq` the suggester judged, and it is
 * required: a record is immutable and a refinement is a NEW revision, so a suggestion naming only
 * a record id would silently re-attach to whatever the live wording becomes. Carrying it means a
 * suggestion about a wording that has since been superseded is refused rather than quietly
 * inherited.
 */
export const SuggestInputSchema = z.strictObject({
  recordId: RecordIdSchema,
  revision: z.number().int().min(0),
  kind: NextActionSchema,
  summary: bounded(400),
  rationale: z.string().max(2000).default(""),
});

export const SuggestedSchema = z.strictObject({
  id: z.string(),
  recordId: RecordIdSchema,
  revision: z.number().int(),
  kind: NextActionSchema,
  /** The plugin it is attributed to, resolved from the principal and never from the input. */
  suggester: z.string(),
  /** The suggestion this one replaced, or empty: one live suggestion per revision and kind. */
  supersedes: z.string(),
  at: z.string(),
  /** How many of this suggester's live suggestions the operator has not answered yet. */
  outstanding: z.number().int().nonnegative(),
});
export type Suggested = z.infer<typeof SuggestedSchema>;

/**
 * WHAT ONE SUGGESTER'S QUEUE LOOKS LIKE, so a sweep can state its size before it runs.
 *
 * #360 requires retroactive application over the whole imported corpus, filtered hard by default.
 * A sweep that cannot say how many suggestions it would add is how the queue becomes the one
 * undifferentiated list the desk/queue/shelf split was built to end, one level down — so the
 * numbers are a door rather than something a caller counts by writing.
 */
export const SuggestionsResultSchema = z.strictObject({
  suggester: z.string(),
  /** Live suggestions with no answer from the operator. */
  outstanding: z.number().int().nonnegative(),
  /** Live suggestions he has accepted or declined. */
  answered: z.number().int().nonnegative(),
  /** Record revisions this suggester has already judged: the durable "do not judge twice" mark. */
  judged: z.number().int().nonnegative(),
  /** Live record revisions it has not judged: exactly what one more sweep would add. */
  unjudged: z.number().int().nonnegative(),
});
export type SuggestionsResult = z.infer<typeof SuggestionsResultSchema>;

// ------------------------------------------------------------------- what a draw answers

/*
  THE COORDINATOR'S TWO REASON VOCABULARIES, spelled here rather than beside the draw that
  produces them, because a door now answers with them (#328). A reason that crossed the wire as
  a free string would be matched as prose by every consumer — the panel's label table, a future
  filter, whatever reads the pulse next — and the first misspelling would read as "no such
  reason" rather than fail. `store/coordinator.ts` builds the words; this is where they are
  named, and `z.enum` over them is what makes the door refuse a word nobody has spelled.

  The two sets are DISJOINT, which is what lets one map keyed by reason hold both without
  ambiguity (`CycleTally` in `server/conductor.ts` relies on it).
*/

export const GAP_REASONS = [
  "excluded",
  /**
   * The TOPIC the record is filed under is retired, so work filed under it is withheld. The
   * subject is in the word because {@link GAP_REASONS} is read as a counted list of bare words,
   * where an unqualified `retired` beside `record-replaced` reads as the same fact twice (#382).
   */
  "topic-retired",
  /** The RECORD's own lifecycle is superseded or retired, so no review of it is outstanding. */
  "record-replaced",
  "capped",
  "claimed",
  "exhausted",
  "cooling",
  "settled",
  "unsupported",
  "empty",
] as const;
export type GapReason = (typeof GAP_REASONS)[number];
export const GapReasonSchema = z.enum(GAP_REASONS);

export const STOP_REASONS = [
  "invalid-policy",
  "disabled",
  /**
   * The batch is full of work somebody else holds: every slot this deployment allows is claimed
   * and none of those claims finished, so the cycle dispatched nothing. It is a wedged loop —
   * stale claims read exactly like a busy one until they expire — and is why the filled batch
   * below carries its own word (#382).
   */
  "batch",
  /** The cycle filled the batch itself: it dispatched everything one cycle is allowed. */
  "batch-filled",
  "per-cycle",
  "daily",
  "no-candidates",
  "no-lane",
  /**
   * Evaluation is enabled but the installed policy names no Code profile and destination. It
   * is the cycle's reason and never a draw's — an unrouted loop must not reserve a claim it
   * cannot dispatch.
   */
  "unrouted",
  /** A route existed, but its projection, prompt, engine post or claim binding was refused. */
  "dispatch-refused",
] as const;
export type StopReason = (typeof STOP_REASONS)[number];
export const StopReasonSchema = z.enum(STOP_REASONS);

/**
 * WHY THE LOOP PARKED ITSELF, which is the conductor's own verdict and never a draw's (#265).
 *
 * A park is a run of settlements that produced nothing, and THE WORD SAYS WHAT THEY COST,
 * because the remedy differs and the 2026-09-13 drain could tell the two apart in neither
 * direction (post-mortem F16, F8):
 *
 *   - `barren`: no model was ever reached. A machine whose engine will not launch, a role with
 *     no recipe, a credential that has lapsed — the lane is broken, and the deployment charged
 *     a reservation for each attempt and learned nothing.
 *   - `spent`: a model answered every time and the contract refused every answer. That is
 *     money out of the day's allowance (§6.5, a refused submission is spend), and the remedy
 *     is the recipe or the contract rather than the machine.
 *
 * Both park, because a fourth draw buys the same nothing either way; neither is permanent —
 * one answered review, an hour of quiet or a new policy version lifts it. It is a word rather
 * than the sentence the park used to carry so that a reader can label it and count by it, and
 * it is a THIRD vocabulary beside {@link GAP_REASONS} and {@link STOP_REASONS} rather than an
 * extension of either: a park is the loop's verdict about the runs that already happened, not
 * a reason a draw declined.
 */
export const PARK_REASONS = ["barren", "spent"] as const;
export type ParkReason = (typeof PARK_REASONS)[number];
export const ParkReasonSchema = z.enum(PARK_REASONS);

// ---------------------------------------------------------------------------- the pulse

/** What the STORE can answer about the pulse: today's counts, off its own tables. */
export const PulseTodaySchema = z.strictObject({
  since: z.string(),
  today: z.strictObject({
    sessionsRead: z.number().int(),
    records: z.number().int(),
    votes: z.number().int(),
    proposals: z.number().int(),
    topicProposals: z.number().int(),
    ruled: z.number().int(),
  }),
  reviewing: z.array(
    z.strictObject({ id: z.string(), kind: z.string(), title: z.string(), since: z.string() }),
  ),
});

/**
 * WHY THE LAST CYCLE DID WHAT IT DID (#328).
 *
 * The loop's own verdict was readable in the hub's log and nowhere else: a cycle that drew
 * nothing left a `console.warn` and no door reported it, so an operator watching a deployment
 * where nothing happens could not tell "no candidate is eligible" from "the policy names no
 * route" from "the day's ceiling is spent" — and a cycle that produced nothing and said nothing
 * is indistinguishable from a broken one.
 *
 * THE GAPS ARE COUNTED, NOT LISTED. One busy cycle declines hundreds of candidates for the same
 * two or three reasons; shipping every one of them to a panel that can only render "many"
 * is a second defect wearing the first one's clothes. So it is one row per reason — at most
 * {@link GAP_REASONS}`.length` of them — carrying how many, and the first instance's record and
 * sentence, because "forty of kind `claimed`" says how much and "…starting with hyp_7f3a" says
 * where to look.
 */
export const CycleReportSchema = z.strictObject({
  /** When the cycle ran, as an instant a panel can age against its own clock. */
  at: z.string(),
  /** Why drawing stopped, and the sentence it stopped with; null when the cycle never drew. */
  stop: z.strictObject({ reason: StopReasonSchema, detail: z.string().max(400) }).nullable(),
  gaps: z
    .array(
      z.strictObject({
        reason: GapReasonSchema,
        count: z.number().int().min(1),
        /** The first candidate declined for this reason; empty for a gap about a whole lane. */
        recordId: z.string(),
        detail: z.string().max(400),
      }),
    )
    .max(GAP_REASONS.length),
});

/**
 * The pulse as a reader asks for it: what Babel did today, and what its last cycle did. The
 * cycle is null until one has run — a store enabled a minute ago has a pulse and no verdict.
 */
export const PulseResultSchema = PulseTodaySchema.extend({
  cycle: CycleReportSchema.nullable(),
});

/**
 * Where the conductor leaves {@link CycleReportSchema} for the `pulse` door to find it.
 *
 * It is a key rather than a return value because the cycle runs AFTER the door has answered
 * (`server.ts`): the door reports the cycle BEFORE it, which is the one every operator is
 * asking about — the cycle that has already failed to do anything.
 */
export const CONDUCTOR_CYCLE_KEY = "conductor:cycle";

/**
 * EVERY WORD A CYCLE MAY COUNT ITSELF UNDER (#265): the two draw vocabularies above, and
 * nothing besides them.
 *
 * The conductor's tally is one map keyed by reason holding the gaps a draw declined and the
 * stop that ended the cycle at once — that {@link GAP_REASONS} and {@link STOP_REASONS} are
 * disjoint is what allows it. Keyed by a bare string it held whatever a caller composed, and
 * the day's half of it is READ BACK out of {@link CONDUCTOR_TALLY_KEY}, which some other build
 * wrote: a word nobody ever spelled would reach a reader as a reason he cannot act on, and a
 * misspelling of a real one would read as a second kind of gap beside it. This enum is the key
 * type of the counter AND the parse of the kept day, so neither half can carry a reason that
 * is not one of these.
 */
export const TALLY_REASONS = [...GAP_REASONS, ...STOP_REASONS] as const;
export type TallyReason = (typeof TALLY_REASONS)[number];
export const TallyReasonSchema = z.enum(TALLY_REASONS);

/**
 * Where the conductor keeps the day's counts between wakes, beside {@link CONDUCTOR_CYCLE_KEY}.
 *
 * A cycle is a fresh conductor built over whatever wake caused it, so a running total cannot
 * live in the loop: it is written here and read back by the next tick. That round trip through
 * JSON is the only way a reason word ever arrives from outside this build at all.
 */
export const CONDUCTOR_TALLY_KEY = "conductor:tally";

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
  /** The archive's reading half (#338): `restic check`, and one session restored from a named
   *  snapshot and proved byte-exact. It deletes nothing and cannot — `machine/restic.ts`
   *  admits a closed set of verbs that holds no `forget`, `prune`, `repair` or `unlock`. */
  verify: `${BABEL_PLUGIN_ID}.verify`,
} as const;

/**
 * EVERY OPERATION BABEL NAMES, which is not the same list (#279).
 *
 * `explore`, `evaluate` and `title` are NAMED and not DECLARED. A Babel run is a Code session:
 * the operator parametrizes it through a saved Code profile or Code's generator, and Code's
 * `runSession` door posts it to omp (atyrode/code#170, reached through atyrode/manifold#575).
 * Babel neither composes the session nor launches omp, so none of them is a machine operation
 * of this bundle any more — but each is still what a run is CALLED: the node a launch asks
 * authority at, the `kind` a run row and a receipt record, and the lane a preset names. The two
 * tables are therefore two different questions, and the day they answered as one is the day
 * Babel had a launcher of its own.
 */
export const OPERATIONS = {
  ...MACHINE_OPERATIONS,
  explore: `${BABEL_PLUGIN_ID}.explore`,
  evaluate: `${BABEL_PLUGIN_ID}.evaluate`,
  /** Naming the sessions whose own logs carry no title (#342); never a preset, never declared. */
  title: `${BABEL_PLUGIN_ID}.title`,
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
  rows: z
    .array(z.record(z.string(), z.union([z.string(), z.number(), z.null()])))
    .min(1)
    .max(500),
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
 * WHAT BECAME OF ONE CITATION'S QUOTED TEXT (#348), in the five words a record may carry.
 *
 * A citation names a location and quotes what is there. Checking that the location was SERVED
 * and checking that the QUOTE is at it are two different questions, and only the first was ever
 * asked: of 300 digest-verified citations in the imported corpus, 86 carried a quote of twelve
 * characters or more, 53 matched the cited line, 57 matched somewhere else in the right file and
 * 29 matched nowhere in it at all (`docs/jev-case-study-audit.md`). The plugin's own rate is
 * unmeasured, which is the reason these five words exist rather than a refusal: the outcome is
 * RECORDED, on the record, and a deployment can count its own before anyone argues from a
 * number measured somewhere else.
 *
 * `verified` — the quoted text is at the line the citation names.
 * `moved`    — it is in that session at another line. The claim is about real bytes and the
 *              locator does not reach them, which is a different defect from an invention.
 * `absent`   — it is nowhere in the session the citation names.
 * `unquoted` — the citation quoted nothing, so there was nothing to check. It is a word rather
 *              than an absence because "not checked" and "checked and clean" must not look alike.
 * `unchecked`— Babel could not read the bytes: the material is past the bound the hub reads back,
 *              the preparation's own lease is gone, or the quote is too short to mean anything.
 */
export const CITATION_OUTCOMES = {
  verified: "verified",
  moved: "moved",
  absent: "absent",
  unquoted: "unquoted",
  unchecked: "unchecked",
} as const;
export type CitationOutcome = (typeof CITATION_OUTCOMES)[keyof typeof CITATION_OUTCOMES];

/**
 * The field a citation carries its quoted text in, and the most of it one citation may carry.
 *
 * The bound is the Go tree's own served-excerpt bound (`v0.4.0:internal/explore/retrieval.go`,
 * `maxServedExcerptBytes`): an excerpt longer than this is a copy of the record rather than the
 * span that supports the claim, and a payload that grows with the corpus is the thing every
 * bound in this file exists to stop.
 */
export const MAX_CITATION_QUOTE = 2048;

/**
 * The shortest quote worth checking, and the study's own threshold (`11-the-bench.md`): below
 * twelve characters a span matches somewhere in almost any session, so a verdict either way
 * would be noise presented as a finding. A shorter quote is {@link CITATION_OUTCOMES.unchecked}.
 */
export const MIN_CITATION_QUOTE = 12;

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
export const PRESETS = [
  "read-whats-new",
  "explore-topic",
  "review-backlog",
  "file-and-tidy",
  "keep-going",
] as const;
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
  minutes: z
    .number()
    .int()
    .min(5)
    .max(24 * 60)
    .optional(),
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
export type LaunchRequest = z.infer<typeof LaunchRequestSchema>;

/**
 * THE DOCUMENT A PRESS POSTS, ASSEMBLED ONCE FOR EVERY SURFACE THAT POSTS ONE.
 *
 * Two of them ask for a run — Watch's Start form, and a never-looked cell on a topic page
 * (#330) — and they must not be two disciplines. A cell is a shorter way to ASK for a run and
 * must not be a shorter way to get one, so the only thing either surface supplies is the
 * request's own fields: the machine, the preset, its knobs and the Code profile with the
 * revision the operator was shown.
 *
 * THE NODE IS DERIVED AND NEVER SUPPLIED. It is made of the two fields above it, and a caller
 * that assembled it itself and named the wrong operation for its preset would be refused
 * `invalid authority target` before the handler ran — a refusal that reads like a missing
 * grant. Deriving it here is the same reason {@link PRESET_OPERATIONS} is in this file at all.
 */
export function asLaunchRequest(input: z.input<typeof LaunchInputSchema>): LaunchRequest {
  return LaunchRequestSchema.parse({
    ...input,
    operation: {
      kind: "operation",
      machineId: input.machineId,
      operationId: PRESET_OPERATIONS[input.preset],
    },
  });
}

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
 * WHAT A VERIFICATION IS ASKED (#338): which machine reads the repository, how deep, and which
 * catalogued session — if any — to restore out of it and prove byte-exact.
 *
 * `readData` is ONE field rather than a flag with a subset beside it: `false` checks the
 * repository's structure, `true` reads every stored byte, and a string is restic's own subset
 * spelling (`n/t`, `x%`, a size) for a repository too large to read whole on a cadence. Two
 * fields could contradict each other and this one cannot.
 *
 * A RESTORE IS ASKED FOR BY SELECTOR, never by path. The door reads the session's catalogued
 * snapshot and digest out of `sessions` and the machine resolves the path from the SNAPSHOT
 * itself, so a session whose log is no longer on the disk — the case an archive exists for —
 * is still restorable, and the operator never types a filesystem path for a machine he is not
 * standing on.
 */
export const VerifyInputSchema = z.strictObject({
  machineId: bounded(120),
  readData: z.union([z.boolean(), z.string().trim().max(16)]).default(false),
  session: z
    .strictObject({
      selector: bounded(400),
      /** The snapshot to read; empty uses the one the catalog recorded for this session. */
      snapshotId: z
        .union([z.literal(""), z.string().regex(/^(latest|[0-9a-f]{8,64})$/)])
        .default(""),
      /** Where the restored files are KEPT on the machine; empty proves and keeps nothing. */
      target: z.string().trim().max(4096).default(""),
    })
    .optional(),
});
export type VerifyInput = z.infer<typeof VerifyInputSchema>;

/** The request as the door takes it: the above plus the OPERATION NODE it is authorized at,
 *  for the reason {@link LaunchRequestSchema} carries one. */
export const VerifyRequestSchema = VerifyInputSchema.extend({ operation: OperationRefSchema });

/**
 * What a verification answers: the run it became, on which machine, and what it was told to
 * read. The VERDICT is the receipt of that run — `counts.checkErrors`, `counts.restored`,
 * `counts.restoredBytes` and a `reason` when something failed — read back through the `run`
 * door, because a job that takes an hour cannot answer at the press.
 */
export const VerifyResultSchema = z.strictObject({
  runId: z.string(),
  jobId: z.string(),
  machineId: z.string(),
  /** The snapshot the restore will read, or "" when this verification restores nothing. */
  snapshotId: z.string(),
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
  /**
   * WHAT THE OPERATOR TOLD BABEL, newest first, bounded.
   *
   * `tell` has written `steering` rows since it shipped and nothing read one back: his own words
   * went into a table no surface opened. A box that accepts a sentence and shows it nowhere
   * reads as a sentence that was heard. `about` names the record it concerns, or is empty for a
   * standing remark. This same projection is what a run's prompt quotes (`carriedSteering`, in
   * `server/engine/prompts.ts`), so the panel and the prompt read one answer and not two.
   */
  steering: z.array(
    z.strictObject({
      id: z.string(),
      text: z.string(),
      about: z.string(),
      at: z.string(),
    }),
  ),
  payload: z.record(z.string(), z.unknown()),
});

// ---------------------------------------------------------------- the secret preflight (#339)

/*
  WHAT A PREPARATION'S SECRET SCAN PUT ON ITS RECEIPT (SPEC §3 step 4, §6.4).

  `machine/preflight.ts` holds the rules and `machine/prepare.ts` runs them inside the pass that
  seals the material. This is the part a reviewer reads afterwards, and it exists because the
  alternative — a preparation whose receipt says nothing about secrets — is indistinguishable from
  one nobody scanned.

  THE REPORT NAMES CLASSES AND POSITIONS AND NEVER A VALUE. `sites` carries the locator of each
  redaction: the session, the record, and the range inside it. Resolving that locator back into
  bytes needs the machine that holds the session (`resolveRedaction`), so the receipt travels to
  the hub carrying no credential and no commitment to one.
*/

/** The shape of a preflight report, recorded in it so a reader never guesses which layout it has. */
export const PREFLIGHT_SCHEMA = "babel.preflight/1";

/**
 * WHAT A PREPARATION DOES ABOUT A LIKELY SECRET, chosen per preparation.
 *
 * `redact` is the default and the answer for a corpus of years of transcripts: the span is
 * replaced, the rest of the record is still evidence, and the run proceeds. `refuse` is for a
 * scope that must not risk a disclosure at all — the whole preparation fails and seals no index,
 * so no material is ever bound. `off` prepares the raw stream and is recorded as such: it is the
 * operator's to choose and a reviewer's to see, which is the only reason it is nameable.
 */
export const PreflightModeSchema = z.enum(["redact", "refuse", "off"]);
export type PreflightMode = z.infer<typeof PreflightModeSchema>;

/**
 * WHERE ONE REDACTED VALUE WAS. The class, the session, the record's 1-based ordinal in that
 * session's normalized stream — the same line number the material's own file has — and the range
 * inside that record before it was redacted. The length is evidence; the bytes are not here.
 */
export const PreflightSiteSchema = z.strictObject({
  class: z.string().min(1),
  selector: z.string().min(1),
  line: z.number().int().positive(),
  offset: z.number().int().nonnegative(),
  length: z.number().int().positive(),
});

export const PreflightReportSchema = z.strictObject({
  schema: z.literal(PREFLIGHT_SCHEMA),
  /** Which rule set ran (`PREFLIGHT_DETECTORS`), so "scanned" stays a claim about something. */
  detectors: z.string().min(1),
  mode: PreflightModeSchema,
  /** Records read by the scan; 0 under `off`, which is how a reader tells the two apart. */
  records: z.number().int().nonnegative(),
  redactions: z.number().int().nonnegative(),
  /** Complete, one row per class that fired, sorted by class. */
  classes: z.array(
    z.strictObject({ class: z.string().min(1), redactions: z.number().int().positive() }),
  ),
  /** A bounded sample of the sites, so a receipt cannot grow with the corpus; the counts above
   *  are complete, and the material's own markers hold every locator. */
  sites: z.array(PreflightSiteSchema),
  sitesOmitted: z.number().int().nonnegative(),
});
export type PreflightReport = z.infer<typeof PreflightReportSchema>;

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
  nextActions: "next-actions.json",
  sessions: "sessions.json",
  receipt: "receipt.json",
} as const;

/**
 * EVERY TABLE A RUN'S OWN OUTPUT MAY REACH, and the whole of the list.
 *
 * A run writes rows; the conductor's `INGEST` map is the only thing that turns them into SQL,
 * and this is the closed set that map may name (`server/conductor.ts`: `TableIngest.table` is
 * typed as {@link IngestibleTable}, so an entry pointing anywhere else does not compile).
 *
 * IT IS HERE RATHER THAN THERE BECAUSE IT IS THE BOUNDARY OF WHAT A RUN MAY DO, not a detail of
 * how ingestion works. Two tables are deliberately absent and must stay absent: `dispositions`
 * is the operator's ruling on a record (§4.7), and `next_action_rulings` is his answer to a
 * proposed action (#340). A run may propose either subject and may answer neither — a Babel
 * that could write its own acceptance is an agent that agrees with itself, and the acceptance
 * rate stops being evidence about anything. Stating the boundary as a type rather than as a
 * convention is what makes "a run cannot rule" checkable instead of remembered.
 */
export const INGESTIBLE_TABLES = [
  "sessions",
  "records",
  "edges",
  "status_events",
  "assessments",
  "filings",
  "questions",
  "plans",
  "steering",
  "next_actions",
] as const;
export type IngestibleTable = (typeof INGESTIBLE_TABLES)[number];

/**
 * WHAT THE OPERATOR'S STANDING MEMORY PUT INTO ONE RUN'S PROMPT (#331).
 *
 * A remark reaches a run as quoted evidence, bounded — so a receipt has to say which remarks,
 * identifiably enough to find the rows again, and how many the bound left out. "This run was
 * told three things" is misleading when there were nine and six did not fit, and reading a
 * claim against what the run was told is exactly the question the distinction answers. The
 * words are here as well as the identifiers because a remark is short and a receipt that needs
 * a second query to be legible is read once and never again.
 */
export const CarriedSteeringSchema = z.strictObject({
  id: z.string(),
  text: z.string(),
  /** `record:<id>` for a remark about one record, empty for a standing one. */
  about: z.string(),
  at: z.string(),
});
export type CarriedSteering = z.infer<typeof CarriedSteeringSchema>;

/**
 * THE PAYLOAD KEY A SETTLEMENT WRITES ITS CORROBORATION DETERMINATION UNDER.
 *
 * A finding that rests on three observations out of one run is one reading restated, and the
 * number was computable only by joining `edges` to `records` at read time (`store/store.ts`,
 * `corroborationOf`) — so nothing could rank, filter or route on it. The writer therefore states
 * the same fact in the record's own payload, where a `json_extract` reaches it: `true` when every
 * record it rests on came out of one run, `false` when they came out of two or more, and ABSENT
 * when the settlement could not decide, which is also every imported record and every record that
 * rests on nothing. Absent means "this payload does not answer; ask `corroborationOf`", and it
 * means only that.
 *
 * AT CREATION is part of the name because `records` is immutable by trigger: the value can never
 * be corrected in place, so it may not promise a current number. It is the determination made
 * when the row was written, and the live count stays `corroborationOf`'s.
 */
export const RECORD_RESTS_ON_ONE_RUN = "restsOnOneRunAtCreation";
/** The receipt every run writes last (§7): what it was asked, read, produced and cost. */
export const ReceiptSchema = z.strictObject({
  runId: z.string(),
  kind: z.enum(["scan", "archive", "prepare", "verify", "explore", "evaluate", "title"]),
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
  /**
   * WHETHER THIS PREPARATION WAS SCANNED FOR SECRETS, AND WHAT THE SCAN FOUND (#339).
   *
   * Present on every `prepare` receipt this machine half writes, including one whose mode was
   * `off` and one the scan refused — because ABSENT MUST NOT READ AS CLEAN. An absent field says
   * one thing only: no scan ran, either because the operation reaches no material at all or
   * because the machine half that wrote it predates the preflight. A reviewer deciding whether a
   * corpus was checked before a provider read it needs those two states not to look alike.
   */
  preflight: PreflightReportSchema.optional(),
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
  /**
   * WHAT THE OPERATOR'S MEMORY PUT INTO THIS RUN'S PROMPT (#331).
   *
   * `carried` is the remarks the run was quoted, in the order the prompt quoted them, and
   * `omitted` is how many eligible remarks the prompt's bound left out. Absent for a run that
   * reaches no model and for a review, whose prompt is composed from the record under review;
   * present and empty on an exploration nobody has told anything, which is a different fact
   * from "this run was not told what he said".
   */
  steering: z
    .strictObject({
      carried: z.array(CarriedSteeringSchema),
      omitted: z.number().int().nonnegative(),
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
  /**
   * WHAT THIS RUN'S CITATIONS WERE FOUND TO BE (#348), one count per {@link CITATION_OUTCOMES}.
   *
   * It is the measurement the issue asks for before anyone acts on the imported corpus's own
   * numbers: those were measured on one deployment, over Go-era output, at one date, and the
   * plugin's intake path has never been measured at all. Every one of the five keys is present
   * on an exploration's receipt, so a run whose citations were all checked and all sound is
   * distinguishable from one nothing looked at; absent on every other kind of run, which cites
   * nothing.
   *
   * A count here is not a refusal. `absent` and `moved` are recorded and the records stand —
   * the verdict travels on the record's own evidence, where the reader of the claim is.
   */
  citations: z.record(z.string(), z.number().int().nonnegative()).optional(),
});
export type Receipt = z.infer<typeof ReceiptSchema>;

// ------------------------------------------------------------- a run's replayable trace (#349)

/*
  WHAT A RUN'S CALLS WERE, AS SOMETHING A CLAIM CAN BE RECHECKED AGAINST.

  The receipt above is SPEND ACCOUNTING: one total per run. It cannot answer "how did Babel
  judge this", only "what did judging it cost", and the difference matters because the remedy
  for a doubted conclusion is otherwise to run it again — which is a different event. The Jev
  bench could be audited precisely because it kept the traffic beside the totals.

  THE HUB EXPOSES NO PER-CALL TRAFFIC TO A PLUGIN, and these shapes are honest about it rather
  than shaped around a source that does not exist. The hub's `inference_call` frame is metering
  by its own protocol — the model, the tokens, the price, never a prompt or a byte of the answer
  — and Babel is served none of them anyway: no operation this bundle declares binds a model
  service (`server/plan.ts`), and a run that reaches a model is a Code session whose job belongs
  to `atyrode.omp`, which `ctx.jobs` may neither follow nor journal. What a settlement holds is
  omp's receipt through `atyrode.code.readSession` — a session id, the transcript's path, the
  model, the agent's last message, ONE usage total summed over every turn, and an exit code.

  SO A CALL'S BODY LIVES IN THE SESSION'S OWN TRANSCRIPT and the trace holds its LOCATOR, on the
  discipline `PreflightSiteSchema` above already states: the class and the position travel, the
  bytes stay on the machine that holds the log. `response.digest` is what lets two answers be
  compared without either being copied.
*/

/** One call of a run: what it cost, how it ended, and where the bytes of it are. */
export const RunCallSchema = z.strictObject({
  runId: z.string(),
  /** 1-based ordinal within the run. A posted session is omp's one-shot, so it is 1 today. */
  seq: z.number().int().positive(),
  /** When this deployment recorded the call; a call carries no instant of its own. */
  recordedAt: z.string(),
  /** The model that answered, as the receipt named it; empty when no transcript was sealed. */
  model: z.string(),
  /** The engine's own counts, summed over the turns inside the call, as omp reports them. */
  inputTokens: z.number().int().nonnegative(),
  outputTokens: z.number().int().nonnegative(),
  cacheReadTokens: z.number().int().nonnegative(),
  cacheWriteTokens: z.number().int().nonnegative(),
  /** Integer micro-dollars: the unit the hub meters in, so two runs subtract exactly. */
  costMicros: z.number().int().nonnegative(),
  /** The session's exit code, or null where Code sealed no transcript to read one from. */
  exitCode: z.number().int().nullable(),
  closure: z.enum(["completed", "failed", "stopped", "skipped"]),
  /** The contract's refusal code for what came back, empty when the answer stood. */
  refusal: z.string(),
  /**
   * SHA-256 OF THE FINAL MESSAGE, AND ITS SIZE — never the message. Two runs that answered
   * byte-identically share a digest, which is the only way to observe that repeating a request
   * repeated its answer without keeping either copy. Empty digest means no transcript was
   * sealed, which is a different fact from an answer that was empty.
   */
  response: z.strictObject({
    digest: z.string(),
    bytes: z.number().int().nonnegative(),
  }),
  /**
   * WHERE THE BYTES ARE. The machine that ran the session, the engine's session id, and the
   * log's path ON THAT MACHINE — one JSONL record per message, the request among them.
   * Resolving it needs that machine, which is the whole point: `machine/adapters/omp.ts` is
   * what names the log and `machine/prepare.ts` what numbers its records, and neither runs here.
   */
  transcript: z.strictObject({
    host: z.string(),
    sessionId: z.string(),
    path: z.string(),
  }),
});
export type RunCall = z.infer<typeof RunCallSchema>;

/**
 * ONE RUN AS TWO HALVES: what it was asked, and the calls it made answering.
 *
 * The request half is read off the run row and its receipt rather than copied into a second
 * place, for the reason the trace keeps no bodies: a duplicate is a thing that can disagree.
 */
export const RunTraceSchema = z.strictObject({
  runId: z.string(),
  /** The operation, as the run row spells it. */
  kind: z.string(),
  machineId: z.string(),
  recipeId: z.string(),
  /** The `prepare` job whose sealed material this run read; empty for a run that read none. */
  material: z.string(),
  /** `<provider>/<identityKey>`, empty where the receipt named no account. */
  account: z.string(),
  /** The model the receipt named, which is what was ASKED for; a call says what answered. */
  model: z.string(),
  /** The operator's remarks the prompt quoted, by id, in the order it quoted them (#331). */
  steering: z.array(z.string()),
  calls: z.array(RunCallSchema),
});
export type RunTrace = z.infer<typeof RunTraceSchema>;

/**
 * EVERYTHING TWO RUNS CAN BE SAID TO DIFFER IN, and nothing else — a closed vocabulary because
 * "what changed" is only an answer if the reader knows what was looked at.
 *
 * THE TRANSCRIPT LOCATOR IS DELIBERATELY NOT IN IT. Two runs never share a log, so reporting
 * that their transcripts differ is noise that would appear in every comparison ever made; both
 * locators are on the traces for a reader who wants to go and read them.
 */
export const RUN_DIFF_FIELDS = [
  "kind",
  "machineId",
  "recipe",
  "material",
  "account",
  "model",
  "steering",
  "calls",
  "closure",
  "refusal",
  "response",
  "responseBytes",
  "inputTokens",
  "outputTokens",
  "cacheReadTokens",
  "cacheWriteTokens",
  "costMicros",
] as const;
export type RunDiffField = (typeof RUN_DIFF_FIELDS)[number];

/**
 * WHICH HALF EACH FIELD BELONGS TO, spelled once, because the verdict below is exactly the
 * question "did the same request answer the same way" and that needs the halves named.
 */
export const RUN_DIFF_SIDES: Readonly<Record<RunDiffField, "request" | "answer">> = {
  kind: "request",
  machineId: "request",
  recipe: "request",
  material: "request",
  account: "request",
  model: "request",
  steering: "request",
  calls: "answer",
  closure: "answer",
  refusal: "answer",
  response: "answer",
  responseBytes: "answer",
  inputTokens: "answer",
  outputTokens: "answer",
  cacheReadTokens: "answer",
  cacheWriteTokens: "answer",
  costMicros: "answer",
};

export const RunFieldDiffSchema = z.strictObject({
  field: z.enum(RUN_DIFF_FIELDS),
  side: z.enum(["request", "answer"]),
  a: z.string(),
  b: z.string(),
});
export type RunFieldDiff = z.infer<typeof RunFieldDiffSchema>;

/**
 * THE FOUR THINGS A COMPARISON OF TWO RUNS CAN CONCLUDE.
 *
 * `different-request` comes first because it disqualifies the others: two runs asked different
 * things and the answers were never comparable. `unanswered` is both runs sealing no transcript
 * — nothing answered, so nothing agreed. The remaining pair is the measurement the Jev bench
 * made, which is why this vocabulary exists: the same request, put twice, answering with the
 * same bytes or with different ones.
 */
export const RUN_DIFF_VERDICTS = [
  "different-request",
  "unanswered",
  "same-answer",
  "different-answer",
] as const;
export type RunDiffVerdict = (typeof RUN_DIFF_VERDICTS)[number];

export const RunDiffSchema = z.strictObject({
  a: z.string(),
  b: z.string(),
  verdict: z.enum(RUN_DIFF_VERDICTS),
  /** Every field that differed, with both values; ordered as {@link RUN_DIFF_FIELDS} is. */
  differed: z.array(RunFieldDiffSchema),
  /** Every field that matched. A diff that named only differences could not be read as
   *  "these two runs agreed about everything except the model" without a second lookup. */
  same: z.array(z.enum(RUN_DIFF_FIELDS)),
});
export type RunDiff = z.infer<typeof RunDiffSchema>;

/**
 * A RUN'S TRACE FLATTENED TO ONE STRING PER FIELD, which is what makes a diff one shape rather
 * than a union of seventeen.
 *
 * A run's calls are folded rather than compared pairwise: counts and money sum, and the words
 * join in call order. An absent refusal and an absent digest are written `-` so that two runs
 * with different numbers of calls cannot collide on a run of empty strings.
 */
function foldTrace(trace: RunTrace): Readonly<Record<RunDiffField, string>> {
  const sum = (pick: (call: RunCall) => number): string =>
    String(trace.calls.reduce((total, call) => total + pick(call), 0));
  const join = (pick: (call: RunCall) => string): string =>
    trace.calls.map((call) => pick(call) || "-").join(" ");
  return {
    kind: trace.kind,
    machineId: trace.machineId,
    recipe: trace.recipeId,
    material: trace.material,
    account: trace.account,
    model: trace.model,
    steering: trace.steering.join(" "),
    calls: String(trace.calls.length),
    closure: join((call) => call.closure),
    refusal: join((call) => call.refusal),
    response: join((call) => call.response.digest),
    responseBytes: sum((call) => call.response.bytes),
    inputTokens: sum((call) => call.inputTokens),
    outputTokens: sum((call) => call.outputTokens),
    cacheReadTokens: sum((call) => call.cacheReadTokens),
    cacheWriteTokens: sum((call) => call.cacheWriteTokens),
    costMicros: sum((call) => call.costMicros),
  };
}

/** A run answered when every call it made left a digest to compare. */
function answered(trace: RunTrace): boolean {
  return trace.calls.length > 0 && trace.calls.every((call) => call.response.digest !== "");
}

/**
 * WHAT ACTUALLY DIFFERED BETWEEN TWO RUNS.
 *
 * It is a pure function of two traces and reaches no store, so the same comparison serves a
 * door, a test and an operator holding two run ids, and none of them can be shown a different
 * answer than the others.
 */
export function diffRunTraces(a: RunTrace, b: RunTrace): RunDiff {
  const left = foldTrace(a);
  const right = foldTrace(b);
  const differed: RunFieldDiff[] = [];
  const same: RunDiffField[] = [];
  for (const field of RUN_DIFF_FIELDS) {
    if (left[field] === right[field]) {
      same.push(field);
      continue;
    }
    differed.push({ field, side: RUN_DIFF_SIDES[field], a: left[field], b: right[field] });
  }
  const askedDifferently = differed.some((entry) => entry.side === "request");
  const verdict: RunDiffVerdict = askedDifferently
    ? "different-request"
    : !answered(a) && !answered(b)
      ? "unanswered"
      : answered(a) && left.response === right.response
        ? "same-answer"
        : "different-answer";
  return { a: a.runId, b: b.runId, verdict, differed, same };
}

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
  /**
   * THE NAME OF THE STORE'S TOKEN, AND THE FILE THE MACHINE'S AGENT READS IT OUT OF — never the
   * token. A policy carries `credential.ref`; the owner resolves it against a source it holds
   * and writes the value into the outbound request, so nothing in this family ever holds the
   * other half. `JEV_SERVICE` names its own the same way, for the same reason.
   *
   * `credentialFile` is the ONE thing the protocol will not tell anybody: native bootstrap
   * "advertises references and allowed origins, never source paths or values"
   * (manifold `packages/protocol/src/services.ts`), so a hub can say a reference is missing and
   * can never say where to put it. It is this deployment's own convention — the path
   * `docs/building.md`'s nix block writes — stated here so a panel can tell the operator where
   * to write the file rather than leaving him the same silence "out of credit" produces.
   */
  credentialRef: "babel-restic",
  credentialFile: "/run/credentials/babel-restic-token",
} as const;
/** Where the engine binds that file inside the sandbox: one job's own, read-only. */
export const RESTIC_CREDENTIAL_FILE = `/inputs/${RESTIC_SERVICE.inputFile}`;

/**
 * THE EMBEDDING SERVICE the corpus index's meaning half is computed through (#337).
 *
 * BABEL BINDS NO MODEL SERVICE AND HOLDS NO CREDENTIAL, so the question "how does an embedding
 * get computed" has four possible answers in this tree and only one of them works. `preflight`
 * is deterministic and local, which means no model and therefore no meaning. `restic` is a
 * runtime tool on the MACHINE half — a sandboxed job posted to an enrolled machine and read back
 * a wake later — and a search door has to embed THE QUERY at the latency of a door, so a local
 * model there would serve the backfill and could not serve one search; two mechanisms for one
 * capability is how the halves come to disagree about what a vector means. A Code session, which
 * is how every other model call Babel makes reaches a model, returns prose: right for a session
 * title, and no way to obtain a vector anybody should trust. What is left is `JEV_SERVICE`'s
 * shape, and it is the correct one: the plugin names the service and the operation, the host
 * resolves the credential by reference and writes it into the outbound request, and the key is
 * never in this bundle's address space — not by discipline, by construction.
 *
 * WHAT THAT COSTS, because it is not free. The baseline's manifest declares `services:invoke`
 * for this and held no such authority before: Babel's server half could previously reach nothing
 * at all, and after this it can reach one origin an operator installed. The property that
 * replaces "it cannot" is "it does not unless the operator installed something saying it may" —
 * `server/embed.ts` reads the roster first and every absence is one branch, so a deployment that
 * never installs a policy makes no outbound call and is indistinguishable from this shape not
 * existing. `docs/sandbox-threat-model.md` carries the same change one layer up.
 *
 * WHAT LEAVES, exactly: the record's own prose — its title and the claim fields the peel shows at
 * depths one and two — capped, and nothing else. No identifier, no run, no session byte, no
 * locator, no timestamp. It is the expression `recordTextSql` selects rather than a caller's
 * discipline, and `store/corpus.test.ts` asserts it against the serialized request.
 *
 * `modelField` IS NOT OPTIONAL. The decision this implements requires the producing model to be
 * named in what it produced, and a policy whose projection omits it leaves no way to tell a
 * stale vector from a current one — so an answer without it is refused rather than stored under
 * a guess.
 */
export const EMBEDDING_SERVICE = {
  serviceId: `${BABEL_PLUGIN_ID}.embeddings`,
  revision: "1",
  /** The operations the baseline may name. A policy may declare more; this calls this one. */
  operations: { embed: "embed" },
  /** The one input leaf this bundle fills: the text to be embedded. */
  textField: "text",
  /** The two leaves the operator's response projection must name. */
  vectorField: "embedding",
  modelField: "model",
  /** The name of the key, which is the only half of a credential a repository may hold. */
  credentialRef: "babel-embeddings",
  credentialFile: "/run/credentials/babel-embeddings-token",
} as const;
/** An operation id this bundle may ask for; a typo should not compile. */
export type EmbeddingOperationId =
  (typeof EMBEDDING_SERVICE.operations)[keyof typeof EMBEDDING_SERVICE.operations];

// -------------------------------------------------------------- composing a service policy (#400)

/*
  INSTALLING A SERVICE POLICY WAS A PROCEDURE IN A DOCUMENT, and a policy whose install lives
  only in a runbook is a policy nobody can verify they installed correctly. These two doors are
  the same act as a screen: compose what the manifest already declares, show it, then swap it in
  against the revision it was read at.

  WHAT THEY DO NOT DO is take a key. Manifold has no path anywhere for a person to supply a
  credential VALUE — the machine's agent opens `serviceCredentials[<ref>].source` off its own
  disk, and the protocol states the boundary outright (atyrode/manifold#768 is the upstream
  gap). A field here would mean the value transiting Babel's server half, which is exactly what
  naming a credential by reference removed. So the preview NAMES the reference and the file, and
  the operator writes it there himself.
*/

/** What is installed on a machine under one service id, as a preview reports it. */
export const SERVICE_STANDINGS = ["absent", "different", "installed"] as const;
export const ServiceStandingSchema = z.enum(SERVICE_STANDINGS);
export type ServiceStanding = z.infer<typeof ServiceStandingSchema>;

/**
 * ONE DECLARED SERVICE, composed and weighed against the machine.
 *
 * `reason` is the one sentence an operator can act on, and its whole point is that it tells
 * *not configured* from *configured and refused*: today both produce identical silence at the
 * job, which is the state this preview exists to end. Empty means a job binding this service
 * would admit.
 */
export const ServicePreviewSchema = z.strictObject({
  serviceId: z.string(),
  revision: z.string(),
  /** The endpoint the policy admits; empty when nobody has named one and none is installed. */
  origin: z.string(),
  /** The operation ids the manifest's binding names — the policy declares these and no others. */
  operations: z.array(z.string()),
  credential: z.strictObject({
    /** The name the policy carries. There is no field here for the value, by construction. */
    ref: z.string(),
    /** Where the agent on that machine reads it from, so the operator has somewhere to write. */
    file: z.string(),
    /** Whether that machine advertises a source under the name at all. */
    advertised: z.boolean(),
    /** Whether it advertises one it can read, allowed for this origin. */
    readable: z.boolean(),
  }),
  standing: ServiceStandingSchema,
  reason: z.string(),
});
export type ServicePreview = z.infer<typeof ServicePreviewSchema>;

/**
 * The endpoint a service's policy may reach, which is the one part of a policy this bundle
 * cannot know: it is where THIS deployment's store lives, not a fact about Babel. A service the
 * request names no origin for keeps the origin of the policy already installed, so re-checking a
 * configured machine needs nothing typed.
 */
export const ServiceOriginSchema = z.strictObject({
  serviceId: bounded(120),
  origin: z.url().max(4096),
});

export const PreviewServicesInputSchema = z.strictObject({
  machineId: bounded(120),
  origins: z.array(ServiceOriginSchema).max(16).default([]),
});

export const ServicesPreviewSchema = z.strictObject({
  machineId: z.string(),
  /** Whether the hub can reach that machine's owner; nothing below is known while it cannot. */
  connected: z.boolean(),
  /** The configuration revision this was composed against, `null` for a machine with none. */
  expectedRevision: z.string().nullable(),
  services: z.array(ServicePreviewSchema),
  /** What an install must carry back. A preview nobody re-read cannot be the one applied. */
  previewDigest: z.string(),
  /** Whether installing would change anything at all. */
  current: z.boolean(),
});
export type ServicesPreview = z.infer<typeof ServicesPreviewSchema>;

export const InstallServicesInputSchema = z.strictObject({
  ...PreviewServicesInputSchema.shape,
  expectedRevision: z.string().nullable(),
  previewDigest: z.string().min(1).max(64),
});

export const ServicesInstalledSchema = z.strictObject({
  machineId: z.string(),
  /** The revision the hub minted for the configuration that now stands. */
  revision: z.string().nullable(),
  services: z.array(z.strictObject({ serviceId: z.string(), revision: z.string() })),
});

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
  minutes: z
    .number()
    .int()
    .min(5)
    .max(24 * 60)
    .optional(),
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

// ------------------------------------------------------------- what a drain leaves behind (#270)

/*
  A DRAIN LEAVES A RECORD OF ITSELF, AND IT IS A FRONTIER RECORD RATHER THAN A LOG.

  On 2026-09-13 the questions the operator asked afterwards — did we hit a cap, is the pipeline
  optimised, what did it cost, how much erroring, how much value came out — were answered by hand,
  hours later, out of `run_receipt.payload`, `/proc`, fan logs and attempt rows. Nothing Babel
  produced could have told Babel that: per-job stage and spend exist while a drain runs and a
  panel shows them, but when it stops the only durable trace is one receipt per job with nothing
  relating them to the drain, to the account, or to the value produced.

  So the controller writes ONE record when a drain reaches its ending, and the whole of this
  schema is the rule that it must answer those questions FROM ITSELF: a reader holding the payload
  and nothing else can say what was spent, on whose account, against which duties, with what
  erroring, for how much output. A field whose number would have to be looked up elsewhere does
  not belong here, and a question this deployment genuinely cannot observe is named in
  {@link DrainReportSchema}'s `unobserved` rather than carried as a column of nulls.

  IT IS A RECORD BECAUSE BABEL IMPROVES BABEL BY READING ITS OWN WORK. A drain is a session of
  Babel's own, and a finding is the kind the frontier reaches: it is a feed post, it is drawable
  for review, and a proposal ADDRESSES a finding — which is exactly the shape of "what the next
  drain should change". A log in a column would be none of those.
*/

/** The payload version a drain report declares inside itself, as every record payload does. */
export const DRAIN_REPORT_SCHEMA = "babel.drain-report/1";
/**
 * THE PROVENANCE, SPELLED IN THE PAYLOAD AND NOT INFERRED. `records.actor_kind` admits `run`,
 * `operator` and `engine`, and a drain report is the `engine`'s — the controller wrote it, no
 * model was asked and no person typed it. But `engine` is also what a machine half is, so the
 * word that says WHICH engine act this was is here, and a reader filtering the corpus for
 * drains matches on it rather than on the shape of an identifier.
 */
export const DRAIN_REPORT_PROVENANCE = "drain";
/** The record kind a drain report is written as; see the note above for why it is this one. */
export const DRAIN_REPORT_KIND = "finding";

/**
 * TOKENS AS A DRAIN COUNTS THEM: the hub's own meter, never the engine's word about itself.
 *
 * `cacheReadTokens` is `usage.inference`'s `cachedInputTokens`, which is the whole of what the
 * meter reports about the cache. There is no cache-WRITE figure: the `inference_call` frame
 * carries none and the settle path keeps none, so a drain cannot answer that half and says so in
 * `unobserved` instead of reporting a zero that reads like a measurement.
 */
export const DrainTokensSchema = z.strictObject({
  calls: z.number().int(),
  inputTokens: z.number().int(),
  outputTokens: z.number().int(),
  cacheReadTokens: z.number().int(),
  costMicros: z.number().int(),
});
export type DrainTokens = z.infer<typeof DrainTokensSchema>;

/** One duty or one account, with the runs that carried it and what they metered. */
export const DrainLaneSchema = z.strictObject({
  name: z.string(),
  runs: z.number().int(),
  tokens: DrainTokensSchema,
});
export type DrainLane = z.infer<typeof DrainLaneSchema>;

/** One reason some of this drain's launched work produced nothing, and how many jobs it took. */
export const DrainGapSchema = z.strictObject({
  reason: z.string(),
  jobs: z.number().int(),
  detail: z.string(),
});

/**
 * WHAT THE CONTROLLER SAW AND COULD NOT ACT ON, and what it did beside the launching. Each is
 * something a tick reported and nothing durable would otherwise hold: an admission refusal leaves
 * no run row at all, a stall is read off `run_progress` which is deleted the instant a run
 * settles, and an adopted job is a write this controller lost and took back.
 *
 * `index` is the corpus index's own slice of a tick (#337). The index's ROWS say what the corpus
 * has reached; they cannot say that this drain's tick is what paid for twenty-four of them, and a
 * drain that leaves a report of what it cost has to be able to.
 */
export const DRAIN_NOTE_KINDS = [
  "stall",
  "admission",
  "orphan",
  "adopted",
  "cancel",
  "index",
  "error",
] as const;
const DrainNoteKindSchema = z.enum(DRAIN_NOTE_KINDS);
export type DrainNoteKind = (typeof DRAIN_NOTE_KINDS)[number];

export const DrainNoteSchema = z.strictObject({
  at: z.string(),
  kind: DrainNoteKindSchema,
  detail: z.string(),
});

/** The payload of the one record a drain leaves; every number in it is the drain's own. */
export const DrainReportSchema = z.strictObject({
  schema: z.literal(DRAIN_REPORT_SCHEMA),
  provenance: z.literal(DRAIN_REPORT_PROVENANCE),
  drainId: z.string(),
  machineId: z.string(),
  preset: DrainPresetSchema,
  ending: z.string(),
  reason: z.string(),
  startedBy: z.string(),
  startedAt: z.string(),
  finishedAt: z.string(),
  wallMs: z.number().int(),
  concurrent: z.number().int(),
  target: DrainTargetSchema,
  /** Whose window this spent and what answered, as Code reported both when the drain started. */
  account: z.string(),
  model: z.string(),
  thinking: z.string(),
  /**
   * ALLOCATION AS NAMED AND AS SPENT. `named` is the recipe list the operator started the drain
   * with — the duties, in the operator's own words — and `ran` is what the runs actually carried.
   * `shared` is true when any one run carried more than one recipe, which is the ordinary case
   * for an exploration: one session performs every named method, so the per-duty figures OVERLAP
   * and do not sum to `tokens`. Saying so is the difference between a measurement and a total
   * that quietly double-counts.
   */
  allocation: z.strictObject({
    named: z.array(z.string()),
    ran: z.array(DrainLaneSchema),
    shared: z.boolean(),
  }),
  /** Per account, from the account each run RECORDED, falling back to the drain's own ledger. */
  accounts: z.array(DrainLaneSchema),
  tokens: DrainTokensSchema,
  jobs: z.strictObject({
    launched: z.number().int(),
    /** Reached a model: the hub metered at least one call against it. */
    reachedModel: z.number().int(),
    settled: z.number().int(),
    /** Still running when the drain ended, so their receipts are not in these figures. */
    unsettled: z.number().int(),
    /** Launched with no run row to show for it: the row write did not land. */
    withoutRunRow: z.number().int(),
  }),
  /** How each settled job closed, by closure. */
  closures: z.record(z.string(), z.number().int()),
  /** Paid work with no result, by the code `machine/results.ts` names (#265). */
  refusals: z.record(z.string(), z.number().int()),
  /** Launches the hub or the launch path refused, by code: work that never became a job. */
  launchRefusals: z.record(z.string(), z.number().int()),
  /** What came out, and what a million tokens of this drain bought. */
  produced: z.strictObject({
    records: z.number().int(),
    assessments: z.number().int(),
    recordsPerMillionTokens: z.number(),
    assessmentsPerMillionTokens: z.number(),
  }),
  /**
   * THE LOAD BABEL CAN ACTUALLY SEE: its own. `heldMs` and `atModelMs` are the jobs held and the
   * jobs at the model integrated over the drain's life, one rectangle per tick, so
   * `atModelFraction` is time-at-the-model as a fraction of the fan's whole capacity. The
   * machine's CPU and memory are NOT here — see `unobserved`.
   */
  load: z.strictObject({
    heldMs: z.number().int(),
    atModelMs: z.number().int(),
    atModelFraction: z.number(),
    peakHeld: z.number().int(),
    peakAtModel: z.number().int(),
  }),
  /**
   * WHERE THE WALL TIME WENT, which is the "is the pipeline optimised" question: a drain whose
   * jobs spend most of their life sealing material rather than at a model is the 2026-09-13
   * shape, where engines were present for 13 of 134 minutes.
   */
  pipeline: z.strictObject({
    prepareRuns: z.number().int(),
    prepareWallMs: z.number().int(),
    sessionRuns: z.number().int(),
    sessionWallMs: z.number().int(),
  }),
  gaps: z.array(DrainGapSchema),
  notes: z.array(DrainNoteSchema),
  /** Notes the row's bound dropped, so a short list never reads as a quiet drain. */
  notesDropped: z.number().int(),
  /** What this drain could not observe, and why — in place of a field that is always empty. */
  unobserved: z.array(z.string()),
});
export type DrainReportPayload = z.infer<typeof DrainReportSchema>;

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
  /**
   * THE REPORT THIS DRAIN LEFT (#270), on the newest ended drain and null on every other row.
   *
   * It is carried on the status rather than fetched separately because the panel's question is
   * "what did the last drain do", and a second door would make that two requests that can
   * disagree about which drain is last. It is one row's worth and not six: the payload is the
   * whole account of a drain, and six of them on a five-second poll is a listing paying for a
   * page nobody opened.
   */
  report: DrainReportSchema.nullable(),
});
export type DrainStatus = z.infer<typeof DrainStatusSchema>;

export const DrainStatusResultSchema = z.strictObject({
  drains: z.array(DrainStatusSchema),
});
