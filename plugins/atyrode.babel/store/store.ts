/*
  THE READ MODEL: the one handle every slice reads Babel through.

  `openStore` binds a plugin database (ADR 0034) and a clock, and answers the nine questions the
  reading doors ask — the feed, one record peeled, the conversation under it, the topics, one
  topic, the pulse, the runs, one run, the policy in force. It runs no migration: the plugin's
  own migration ledger applies `SCHEMA_V1`, and a read model that also created tables would be a
  second place the schema lived.

  Two rules shape every query below.

  The grouping is SQL wherever the schema's indexes make it cheap. The head of a chain is a
  `MAX(seq)` over `records_by_root`; the newest ruling a `MAX(seq)` over `dispositions_by_record`;
  a run's last word four `MAX`es over `records_by_run` and `assessments_by_run`; the prose inside
  an assessment a `json_each` over its payload, so the payload itself never crosses the boundary.
  What is folded in TypeScript is what one statement cannot say: §4.12's one-vote-per-run-per-role
  dedup, and the thread's nesting.

  The feed index is the one cached thing, and `touch()` is the whole of its invalidation. It is
  called by the acts that change what the front page ranks rather than by every write, because a
  minute of staleness is affordable for a background ingestion and is not affordable for the
  ruling an operator has just recorded and is looking at.
*/

import type { PluginDatabase, SqlParam, SqlRow } from "@manifold/plugin";
import {
  FEED_SORTS,
  POST_KINDS,
  PulseResultSchema,
  ROLES,
  RULINGS,
  type Comment,
  type FeedPost,
  type FeedQuery,
  type FeedResult,
  type PostKind,
  type RecordPeel,
  type Ruling,
} from "../contract.ts";
import type { z } from "zod";
import { standingOf, type Standing } from "./acts.ts";
import {
  buildFeedIndex,
  filterFeed,
  instant,
  interestOf,
  readInterests,
  stamp,
  STANDING_REOPENED,
  TOPIC_UNFILED,
  type FeedIndex,
  type IndexEntry,
} from "./feedindex.ts";
import { FEED_FRESHNESS_MS, sortFeed } from "./rank.ts";

/** The pulse as the contract spells it; that module exports the schema and not the type. */
export type PulseResult = z.infer<typeof PulseResultSchema>;

type Role = (typeof ROLES)[number];

const ROLE_NAMES: readonly string[] = ROLES;
const RULING_NAMES: readonly string[] = RULINGS;

/** Whether a stored role is one §4.12 authorizes; a later vocabulary's is not credited here. */
function isRole(value: string): value is Role {
  return ROLE_NAMES.includes(value);
}

/** Whether a stored ruling is one §4.7 admits; the column's CHECK says so, the compiler cannot. */
function isRuling(value: string): value is Ruling {
  return RULING_NAMES.includes(value);
}

/**
 * A stored record kind as a post kind. An observation is refused before it reaches here — the
 * peel's top row is a `FeedPost` and cannot carry one — so a kind outside the four is a store
 * holding a value its own CHECK forbids, and saying so is better than picking a label.
 */
function postKind(value: string): PostKind {
  if (value === "hypothesis" || value === "finding" || value === "proposal" || value === "question") {
    return value;
  }
  throw new Error(`a record of kind ${value} is not a post`);
}

// ---------------------------------------------------------------------------- shapes

/** One topic as the topics door answers for it: the index's counts plus the operator's stance. */
export interface TopicRow {
  id: string;
  name: string;
  kind: string;
  binding: { kind: string; identity: string; remote: string; paths: string[] } | null;
  posts: number;
  awaiting: number;
  latestAt: string;
  interest: { state: "" | "working" | "watching" | "not-now" | "excluded"; reason: string; at: string; by: string };
}

/** One topic change Babel has published and nobody has ruled on. */
export interface TopicProposal {
  proposalId: string;
  title: string;
  name: string;
  kind: string;
  operation: "create" | "split" | "merge" | "retire";
  targets: { id: string; name: string }[];
  runId: string;
  posts: number;
  why: string;
}

export interface TopicsResult {
  topics: TopicRow[];
  proposed: TopicProposal[];
  unfiled: number;
}

export interface TopicResult {
  topic: TopicRow | null;
  proposed: TopicProposal[];
  feed: FeedResult;
}

export interface ThreadResult {
  comments: Comment[];
  acts: { id: string; act: "accept" | "reject" | "defer" | "duplicate" | "reopen" | "refine"; by: string; at: string; reason: string }[];
  total: number;
}

export interface RunRow {
  id: string;
  kind: string;
  machineId: string;
  jobId: string;
  recipe: string;
  state: "queued" | "running" | "finished" | "failed" | "stopped";
  startedAt: string;
  finishedAt: string;
  costUsd: number | null;
  records: number;
  freshness: "fresh" | "recent" | "lost" | "ended";
  lastWord: string;
}

export interface RunsQuery {
  limit: number;
  offset: number;
  /** Absent — or explicitly undefined, as a parsed request carries it — is every run. */
  state?: RunRow["state"] | undefined;
  machineId?: string | undefined;
  kind?: string | undefined;
}

export interface RunsResult {
  runs: RunRow[];
  total: number;
}

export interface RunResult {
  run: RunRow | null;
  /** The receipt the machine half wrote, verbatim; null for a run that has written none. */
  receipt: Record<string, unknown> | null;
}

export interface RecipeRow {
  id: string;
  title: string;
  looksFor: string;
  enabled: boolean;
  lastRanAt: string;
  lastRunId: string;
  runs: number;
}

export interface PolicyResult {
  version: string;
  seq: number;
  actorId: string;
  reason: string;
  recordedAt: string;
  ceilings: { perRunUsd: number; perDayUsd: number; concurrent: number };
  spentTodayUsd: number;
  lanes: { lane: string; role: string; share: number }[];
  recipes: RecipeRow[];
  /** The stored policy document, so nothing is lost in the projection above. */
  payload: Record<string, unknown>;
}

/** The one handle every slice reads through. */
export interface BabelStore {
  readonly db: PluginDatabase;
  /** Milliseconds since the Unix epoch, from the injected clock. */
  now(): number;
  /** Drops the built feed index so the next read rebuilds it. */
  touch(): void;
  /** The current projection, rebuilt when it has aged past its freshness. */
  index(): Promise<FeedIndex>;
  feed(query: FeedQuery): Promise<FeedResult>;
  record(id: string): Promise<RecordPeel | null>;
  thread(id: string): Promise<ThreadResult>;
  topics(): Promise<TopicsResult>;
  topic(name: string): Promise<TopicResult>;
  pulse(): Promise<PulseResult>;
  runs(query: RunsQuery): Promise<RunsResult>;
  run(id: string): Promise<RunResult>;
  policy(): Promise<PolicyResult>;
}

// ---------------------------------------------------------------------------- column readers

function text(value: SqlParam | undefined): string {
  if (typeof value === "string") return value;
  if (value === null || value === undefined || value instanceof Uint8Array) return "";
  return String(value);
}

function count(value: SqlParam | undefined): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  return 0;
}

/** A stored JSON document as an object; anything else — absent, null, malformed — is empty. */
function document(value: SqlParam | undefined): Record<string, unknown> {
  const raw = text(value);
  if (raw === "") return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
}

function stringField(payload: Record<string, unknown>, key: string): string {
  const value = payload[key];
  return typeof value === "string" ? value : "";
}

function numberField(payload: Record<string, unknown>, key: string): number {
  const value = payload[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function listField(payload: Record<string, unknown>, key: string): string[] {
  const value = payload[key];
  if (!Array.isArray(value)) return [];
  const out: string[] = [];
  for (const item of value) if (typeof item === "string" && item !== "") out.push(item);
  return out;
}

function objectsField(payload: Record<string, unknown>, key: string): Record<string, unknown>[] {
  const value = payload[key];
  if (!Array.isArray(value)) return [];
  const out: Record<string, unknown>[] = [];
  for (const item of value) {
    if (typeof item === "object" && item !== null && !Array.isArray(item)) {
      out.push(item as Record<string, unknown>);
    }
  }
  return out;
}

// ---------------------------------------------------------------------------- the peel

/** The record kinds §6.7 makes reviewable: an observation is evidence, not an artifact. */
const REVIEWABLE: Record<string, true> = { hypothesis: true, finding: true, proposal: true };

/**
 * The two rulings that leave a record decided for good. They are here because the peel's act
 * line reads them: a record at one of these is not offered a ruling control.
 */
const CASE_LABELS = [
  "problem",
  "outcome",
  "impact",
  "scope",
  "classification",
  "uncertainty",
] as const;

/** What the run that produced a record cited, in the order the record itself carries them. */
interface Citation {
  note: string;
  line: number;
  path: string;
  counter: boolean;
}

/**
 * Reads one record's citations out of its own payload, supporting material before conflicting.
 *
 * The locators live in the payload rather than on the `cites` edges, which carry the session a
 * record reached for and nothing about where inside it: a citation's line and its note are what
 * make a claim evidence (§4.3), and they travel whether or not this hub can open the
 * conversation they name.
 */
function citations(kind: string, payload: Record<string, unknown>): Citation[] {
  const out: Citation[] = [];
  const take = (key: string, counter: boolean): void => {
    for (const item of objectsField(payload, key)) {
      const locator = typeof item["locator"] === "object" && item["locator"] !== null
        ? (item["locator"] as Record<string, unknown>)
        : {};
      out.push({
        note: stringField(item, "note"),
        line: numberField(locator, "line"),
        path: stringField(locator, "path"),
        counter,
      });
    }
  };
  if (kind === "observation") {
    take("evidence", false);
    take("counter_evidence", true);
  } else if (kind === "finding") {
    take("counter_evidence", true);
  } else if (kind === "proposal") {
    take("supporting", false);
    take("conflicting", true);
  }
  return out;
}

/**
 * The record's own sentence at depth one, by kind.
 *
 * A finding's claim is what recurs rather than its headline, and a proposal's is the change it
 * asks for rather than the situation it describes: depth one is what a reader decides whether to
 * care about, and the problem is the first line of the case beneath it.
 */
function claimOf(kind: string, payload: Record<string, unknown>, title: string): string {
  switch (kind) {
    case "hypothesis":
      return stringField(payload, "statement") || title;
    case "observation":
      return stringField(payload, "claim") || title;
    case "finding":
      return stringField(payload, "pattern") || title;
    case "proposal":
      return stringField(payload, "outcome") || title;
    default:
      return title;
  }
}

/**
 * Depth two: the argument, in prose, with no identifier in it.
 *
 * A field a kind has no answer for is absent rather than empty — a panel of blank labels tells a
 * reader the record is thin, and what it means is that this kind of record has no case to make.
 * A candidate has none at all, which is the record rather than a gap: a page that manufactured a
 * case for one would be dressing a guess as an argument.
 */
function caseOf(kind: string, payload: Record<string, unknown>): Record<string, string | string[]> {
  const out: Record<string, string | string[]> = {};
  const put = (key: string, value: string | string[]): void => {
    if (typeof value === "string" ? value !== "" : value.length > 0) out[key] = value;
  };
  if (kind === "observation") {
    put(CASE_LABELS[2], stringField(payload, "impact"));
    put(CASE_LABELS[4], stringField(payload, "category"));
    return out;
  }
  if (kind === "finding") {
    put(CASE_LABELS[2], stringField(payload, "significance"));
    put(CASE_LABELS[3], listField(payload, "scope").join(", "));
    return out;
  }
  if (kind !== "proposal") return out;
  put(CASE_LABELS[0], stringField(payload, "problem"));
  put(CASE_LABELS[1], stringField(payload, "outcome"));
  put(CASE_LABELS[2], stringField(payload, "impact"));
  put(CASE_LABELS[3], stringField(payload, "estimated_scope"));
  put(CASE_LABELS[4], stringField(payload, "classification"));
  put(CASE_LABELS[5], stringField(payload, "uncertainty"));
  put("verification", listField(payload, "verification_criteria"));
  put("risks", listField(payload, "risks"));
  put("openQuestions", listField(payload, "open_questions"));
  put("prerequisites", listField(payload, "prerequisites"));
  const targets: string[] = [];
  for (const target of objectsField(payload, "targets")) {
    const system = stringField(target, "system");
    if (system === "") continue;
    const confidence = stringField(target, "confidence");
    const rationale = stringField(target, "rationale");
    targets.push([system, confidence, rationale].filter((part) => part !== "").join(" · "));
  }
  put("targets", targets);
  return out;
}

// ---------------------------------------------------------------------------- runs

const RUN_STATE_WHERE: Record<RunRow["state"], string> = {
  queued: `r.started_at = ''`,
  running: `r.started_at <> '' AND (r.closure IS NULL OR r.closure = '')
            AND (r.finished_at IS NULL OR r.finished_at = '')`,
  finished: `r.closure = 'completed'
             OR ((r.closure IS NULL OR r.closure = '') AND r.finished_at IS NOT NULL AND r.finished_at <> '')`,
  failed: `r.closure = 'failed'`,
  stopped: `r.closure IN ('stopped','skipped')`,
};

/** What a run's own closure says its state is; an unclosed row is judged by its instants. */
function runState(closure: string, startedAt: string, finishedAt: string): RunRow["state"] {
  switch (closure) {
    case "completed":
      return "finished";
    case "failed":
      return "failed";
    case "stopped":
    case "skipped":
      return "stopped";
    default:
      break;
  }
  if (startedAt === "") return "queued";
  return finishedAt === "" ? "running" : "finished";
}

/**
 * How old the last word from a run is, in `internal/presence`'s own words with the contract's
 * spelling of the middle one.
 *
 * Two minutes is four missed heartbeats — a run this quiet may still be fine, which is why the
 * classification says "doubt it" rather than "it is gone" — and fifteen is where a row is more
 * likely a process that died than a process that is quiet. Neither says a process is dead:
 * nothing here observed one.
 */
function runFreshness(state: RunRow["state"], lastWordMs: number, nowMs: number): RunRow["freshness"] {
  if (state !== "running" && state !== "queued") return "ended";
  const age = nowMs - lastWordMs;
  if (age >= 15 * 60_000) return "lost";
  if (age >= 2 * 60_000) return "recent";
  return "fresh";
}

function runRow(row: SqlRow, nowMs: number): RunRow {
  const closure = text(row["closure"]);
  const startedAt = text(row["started_at"]);
  const finishedAt = text(row["finished_at"]);
  const state = runState(closure, startedAt, finishedAt);
  const lastWord = text(row["last_word"]);
  const cost = row["cost_usd"];
  return {
    id: text(row["id"]),
    kind: text(row["kind"]),
    machineId: text(row["machine_id"]),
    jobId: text(row["job_id"]),
    recipe: text(row["recipe_id"]),
    state,
    startedAt,
    finishedAt,
    costUsd: typeof cost === "number" ? cost : null,
    records: count(row["records"]),
    freshness: runFreshness(state, instant(lastWord), nowMs),
    lastWord,
  };
}

const RUN_COLUMNS = `r.id AS id, r.kind AS kind, r.machine_id AS machine_id, r.job_id AS job_id,
  r.recipe_id AS recipe_id, r.started_at AS started_at, r.finished_at AS finished_at,
  r.closure AS closure, r.cost_usd AS cost_usd, r.records AS records,
  MAX(r.started_at, COALESCE(r.finished_at, ''),
      COALESCE((SELECT MAX(created_at) FROM records WHERE run_id = r.id), ''),
      COALESCE((SELECT MAX(recorded_at) FROM assessments WHERE run_id = r.id), '')) AS last_word`;

// ---------------------------------------------------------------------------- the store

export function openStore(db: PluginDatabase, now?: () => number): BabelStore {
  const clock = now ?? Date.now;
  let built: FeedIndex | null = null;

  /** The current projection, rebuilt when it has aged past its freshness or been touched. */
  const index = async (): Promise<FeedIndex> => {
    const at = clock();
    if (built !== null && at - built.builtAt < FEED_FRESHNESS_MS) return built;
    built = await buildFeedIndex(db, at);
    return built;
  };

  /** One row by id, or nothing. */
  const one = async (sql: string, params: readonly SqlParam[]): Promise<SqlRow | null> => {
    const rows = await db.query(sql, params);
    return rows[0] ?? null;
  };

  const topicRows = async (): Promise<TopicRow[]> => {
    const current = await index();
    const facts = await readInterests(db);
    const rows: TopicRow[] = current.topics.map((topic) => ({
      id: topic.id,
      name: topic.name,
      kind: topic.kind,
      binding: topic.binding,
      posts: topic.posts,
      awaiting: topic.awaiting,
      latestAt: topic.latestAt,
      interest: interestOf(facts.get(topic.id)),
    }));
    // The order is the operator's attention rather than the corpus's size: what he is working
    // on, what he is keeping an eye on, what he has said nothing about, what he has parked, what
    // he excluded — busiest first inside each group. Silence sorts above "not now" because
    // silence is not a refusal.
    const rank: Record<string, number> = { working: 0, watching: 1, "not-now": 3, excluded: 4 };
    rows.sort((left, right) => {
      const leftRank = rank[left.interest.state] ?? 2;
      const rightRank = rank[right.interest.state] ?? 2;
      if (leftRank !== rightRank) return leftRank - rightRank;
      if (left.posts !== right.posts) return right.posts - left.posts;
      return left.name < right.name ? -1 : left.name > right.name ? 1 : 0;
    });
    return rows;
  };

  /**
   * The topic changes Babel has published and nobody has ruled on, joined to the proposals that
   * carry them.
   *
   * The join is what makes the rail a view of the feed rather than a second list: the row's
   * title is the proposal record's own line, so the shortcut and the post a reader opens from it
   * say the same thing, and a plan whose proposal this deployment does not hold is dropped
   * rather than shown as a row with nothing behind it. The post count is over the records this
   * deployment actually holds, because that is what accepting it would file here.
   */
  const topicProposals = async (): Promise<TopicProposal[]> => {
    const current = await index();
    const held: Record<string, true> = {};
    const titles: Record<string, string> = {};
    for (const entry of current.posts) {
      held[entry.post.id] = true;
      titles[entry.post.id] = entry.post.title;
    }
    const names: Record<string, string> = {};
    for (const topic of current.topics) names[topic.id] = topic.name;
    const rows = await db.query(
      `SELECT p.subject_id AS subject_id, p.operation AS operation, p.payload AS payload,
              p.proposed_by_id AS proposed_by_id
         FROM plans p
        WHERE p.kind = 'topic' AND p.state = 'open' AND p.subject_kind = 'proposal'
        ORDER BY p.created_at, p.id
        LIMIT 200`,
      [],
    );
    const out: TopicProposal[] = [];
    for (const row of rows) {
      const proposalId = text(row["subject_id"]);
      const title = titles[proposalId];
      if (title === undefined) continue;
      const payload = document(row["payload"]);
      let posts = 0;
      for (const named of objectsField(payload, "records")) {
        if (held[stringField(named, "id")] === true) posts++;
      }
      for (const named of listField(payload, "records")) {
        if (held[named] === true) posts++;
      }
      const operation = text(row["operation"]);
      out.push({
        proposalId,
        title,
        name: stringField(payload, "name"),
        kind: stringField(payload, "entityKind"),
        operation:
          operation === "split" || operation === "merge" || operation === "retire"
            ? operation
            : "create",
        targets: listField(payload, "targets").map((id) => ({ id, name: names[id] ?? "" })),
        runId: text(row["proposed_by_id"]),
        posts,
        why: stringField(payload, "reasoning"),
      });
    }
    out.sort((left, right) =>
      left.posts !== right.posts
        ? right.posts - left.posts
        : left.title < right.title ? -1 : left.title > right.title ? 1 : 0,
    );
    return out;
  };

  const page = (entries: IndexEntry[], query: FeedQuery, current: FeedIndex): FeedResult => {
    const total = entries.length;
    const posts: FeedPost[] = [];
    for (let at = query.offset; at < Math.min(total, query.offset + query.limit); at++) {
      posts.push((entries[at] as IndexEntry).post);
    }
    return {
      posts,
      total,
      builtAt: stamp(current.builtAt),
      notice: "",
    };
  };

  const feed = async (query: FeedQuery): Promise<FeedResult> => {
    const current = await index();
    const eligible = filterFeed(current.posts, { ...query, topic: query.topic ?? "" }, current.builtAt);
    sortFeed(eligible, query.sort, current.builtAt);
    return page(eligible, query, current);
  };

  /**
   * One record at five depths, the first three free of identifiers (§8.6).
   *
   * The row at the top is the feed's own, taken from the index where the record is a post, so a
   * listing row and the page it opens cannot disagree about a score, a topic or a wait. A
   * superseded wording is not a post and is read on its own terms, which is the same rule the
   * pulse applies to a record under review that the front page does not carry.
   *
   * An observation is nothing here, and that is the contract rather than a judgement: the peel's
   * top row is a `FeedPost`, whose kind is one of the four POST kinds, so a peel of an
   * observation could only be served by calling it a hypothesis — a silent falsehood a panel
   * would render as a label. §4.13 makes an observation evidence, reached from the records that
   * cite it, and it is reachable there; the door refuses it by name and says why.
   */
  const record = async (id: string): Promise<RecordPeel | null> => {
    const row = await one(
      `SELECT id, kind, root_id, seq, supersedes_id, parent_id, run_id, recipe_id,
              recipe_version, actor_kind, actor_id, title, created_at, payload
         FROM records WHERE id = ?`,
      [id],
    );
    if (row === null) return await questionPeel(id);
    const kind = text(row["kind"]);
    if (kind === "observation") return null;
    const payload = document(row["payload"]);
    const current = await index();
    const post = current.posts.find((entry) => entry.post.id === id)?.post ?? (await soloPost(row, current));

    const replacedBy = await one(`SELECT id FROM records WHERE supersedes_id = ? LIMIT 1`, [id]);
    const ruling = await one(
      `SELECT disposition FROM dispositions WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
      [id],
    );
    const last = ruling === null ? null : text(ruling["disposition"]);
    const reviewable = REVIEWABLE[kind] === true;
    let standing: string = reviewable ? (last === "reopen" ? STANDING_REOPENED : standingOf(last)) : "";
    if (replacedBy !== null) standing = "superseded";
    const act = reviewable && (standing === "new" || standing === STANDING_REOPENED) ? "Rule on this" : "";

    return {
      post,
      claim: { statement: claimOf(kind, payload, text(row["title"])), standing, act },
      case: caseOf(kind, payload),
      evidence: await evidenceOf(kind, payload),
      reception: await receptionOf(id),
      machinery: await machineryOf(row, payload),
      related: await relatedOf(id, text(row["run_id"]), text(row["supersedes_id"])),
      plan: await planOf(id),
    };
  };

  /** A question opened as a record: the ledger asked it, and answering it is the act. */
  const questionPeel = async (id: string): Promise<RecordPeel | null> => {
    const current = await index();
    const entry = current.posts.find((post) => post.post.id === id);
    if (entry === undefined) return null;
    const row = await one(
      `SELECT text, why, class, kind, created_at FROM questions WHERE id = ?`,
      [id],
    );
    if (row === null) return null;
    const why = text(row["why"]);
    return {
      post: entry.post,
      claim: {
        statement: text(row["text"]),
        standing: entry.post.standing,
        act: entry.post.awaiting ? "Answer this" : "",
      },
      case: why === "" ? {} : { problem: why },
      evidence: [],
      reception: { byRole: [], contested: false, operatorHistory: [] },
      machinery: { class: text(row["class"]), kind: text(row["kind"]), createdAt: text(row["created_at"]) },
      related: [],
      plan: await planOf(id),
    };
  };

  /**
   * A record the front page does not carry — a wording a later revision replaced — as a row.
   *
   * It carries the record's own kind, never a default: the four post kinds are the only ones
   * that reach here, because an observation is refused above rather than relabelled.
   */
  const soloPost = async (row: SqlRow, current: FeedIndex): Promise<FeedPost> => {
    const id = text(row["id"]);
    const createdAt = text(row["created_at"]);
    const tally = await one(
      `SELECT COALESCE(SUM(vote = 'support'), 0) AS support,
              COALESCE(SUM(vote = 'oppose'), 0) AS oppose,
              COALESCE(SUM(vote = 'unsure'), 0) AS unsure
         FROM assessments a
        WHERE a.record_id = ?
          AND NOT EXISTS (SELECT 1 FROM assessments s WHERE s.supersedes_id = a.id)`,
      [id],
    );
    const support = count(tally?.["support"]);
    const oppose = count(tally?.["oppose"]);
    const runId = text(row["run_id"]);
    return {
      id,
      kind: postKind(text(row["kind"])),
      title: text(row["title"]),
      standing: "",
      createdAt,
      author: runId === "" ? null : { runId },
      topics: [],
      score: support - oppose,
      support,
      oppose,
      unsure: count(tally?.["unsure"]),
      votes: [],
      contested: false,
      reviewing: current.reviewing.has(id),
      comments: 0,
      awaiting: false,
      why: "",
      lastActivityAt: createdAt,
    };
  };

  /**
   * Depth three: what the record rests on, each citation with the note the citing record wrote
   * about it and the session it names where this hub still holds the row.
   *
   * The session is matched the way the record pages always have: the cited file's own name is
   * the lookup, and the catalog row's full source id checked against the path is the proof. The
   * excerpt is empty here and that is the hub being honest — the bytes live in a session log on
   * a machine, and a blank pull-quote reads as a person who said nothing, so nothing is
   * invented for it.
   */
  const evidenceOf = async (
    kind: string,
    payload: Record<string, unknown>,
  ): Promise<RecordPeel["evidence"]> => {
    const cited = citations(kind, payload);
    if (cited.length === 0) return [];
    const stems: string[] = [];
    for (const item of cited) {
      const stem = fileStem(item.path);
      if (stem !== "" && !stems.includes(stem)) stems.push(stem);
    }
    const sessions: Record<string, { selector: string; title: string; sourceId: string }> = {};
    if (stems.length > 0) {
      const holes = stems.map(() => "?").join(",");
      const likes = stems.map(() => `source_id LIKE ?`).join(" OR ");
      const rows = await db.query(
        `SELECT selector, source_id, title FROM sessions
          WHERE source_id IN (${holes}) OR ${likes}`,
        [...stems, ...stems.map((stem) => `%/${stem}`)],
      );
      for (const row of rows) {
        sessions[fileStem(text(row["source_id"]))] = {
          selector: text(row["selector"]),
          title: text(row["title"]),
          sourceId: text(row["source_id"]),
        };
      }
    }
    return cited.map((item) => {
      const held = sessions[fileStem(item.path)];
      const stripped = item.path.replace(/\.[^./]*$/, "");
      // The cited file's own name is the lookup and the catalog row's full source id checked
      // against the path is the proof, so a stem two sessions share cannot resolve to the wrong
      // conversation.
      const matched =
        held !== undefined && held.sourceId !== "" && stripped.endsWith(held.sourceId) ? held : null;
      const event = item.line > 0 ? item.line - 1 : 0;
      return {
        excerpt: "",
        speaker: "",
        session:
          matched === null
            ? null
            : {
                selector: matched.selector,
                title: matched.title,
                href: `#/sessions/${encodeURIComponent(matched.selector)}?event=${String(event)}`,
              },
        // §4.3 and §4.5 require a record to state its counter-evidence, and a surface that
        // rendered conflicting material as supporting would invert the record while showing
        // every one of its words. The contract's evidence row has no side of its own yet, so the
        // side is said in the note rather than dropped.
        note: item.counter ? `counter-evidence · ${item.note}` : item.note,
        line: item.line > 0 ? item.line : null,
      };
    });
  };

  /**
   * Depth four: who has said what, with the operator's own voice kept apart from Babel's
   * reviewers.
   *
   * The separation is structural rather than conventional. §4.12 keeps an operator's reception
   * and a run's assessment distinct acts, and nothing here sums them: the by-role tally is
   * run-authored votes and nothing else, and an operator's feedback carries no vote to add to
   * it. Contested is per role — support on whether the record matters beside opposition on
   * whether its evidence holds is two reviewers agreeing about different things.
   */
  const receptionOf = async (id: string): Promise<RecordPeel["reception"]> => {
    const rows = await db.query(
      `SELECT a.role AS role, a.vote AS vote, a.run_id AS run_id, a.recorded_at AS recorded_at,
              CASE WHEN json_valid(a.payload)
                   THEN (SELECT json_extract(c.value, '$.text')
                           FROM json_each(a.payload, '$.contributions') c
                          WHERE TRIM(COALESCE(json_extract(c.value, '$.text'), '')) <> ''
                          LIMIT 1)
                   ELSE NULL END AS rationale
         FROM assessments a
        WHERE a.record_id = ?
          AND NOT EXISTS (SELECT 1 FROM assessments s WHERE s.supersedes_id = a.id)
        ORDER BY a.recorded_at, a.id`,
      [id],
    );
    // One vote per run per role, newest wins: a re-granted review is a changed vote, not a
    // second one. The order above is commit order, so a later row replaces an earlier one.
    const votes = new Map<string, { role: string; vote: string; rationale: string }>();
    const order: string[] = [];
    for (const row of rows) {
      const vote = text(row["vote"]);
      if (vote === "") continue;
      const role = text(row["role"]);
      if (role !== "" && !order.includes(role)) order.push(role);
      votes.set(`${text(row["run_id"])}\u0000${role}`, {
        role,
        vote,
        rationale: text(row["rationale"]),
      });
    }
    const byRole = new Map<string, { support: number; oppose: number; unsure: number; opposingRationales: string[] }>();
    for (const held of votes.values()) {
      if (held.role === "") continue;
      let tally = byRole.get(held.role);
      if (tally === undefined) {
        tally = { support: 0, oppose: 0, unsure: 0, opposingRationales: [] };
        byRole.set(held.role, tally);
      }
      if (held.vote === "support") tally.support++;
      else if (held.vote === "oppose") tally.oppose++;
      else tally.unsure++;
      // A reader looking at a contested role needs the argument against; the arguments for are
      // already beside every reviewer's own line.
      if (held.vote === "oppose" && held.rationale !== "") tally.opposingRationales.push(held.rationale);
    }
    let contested = false;
    const roles: RecordPeel["reception"]["byRole"] = [];
    for (const role of order) {
      const tally = byRole.get(role);
      // A role this build does not know is a grant written under a later vocabulary. Its votes
      // are still in the columns; crediting them to a role nobody here can name is what would
      // let a reader read a satisfied check that was never authorized.
      if (tally === undefined || !isRole(role)) continue;
      if (tally.support > 0 && tally.oppose > 0) contested = true;
      roles.push({
        role,
        support: tally.support,
        oppose: tally.oppose,
        unsure: tally.unsure,
        opposingRationales: tally.opposingRationales,
      });
    }
    const stances = await db.query(
      `SELECT stance, reason, recorded_at FROM feedback
        WHERE record_id = ? AND stance IS NOT NULL AND stance <> ''
        ORDER BY recorded_at DESC, id DESC LIMIT 50`,
      [id],
    );
    return {
      byRole: roles,
      contested,
      operatorHistory: stances.map((row) => ({
        stance: text(row["stance"]),
        reason: text(row["reason"]),
        at: text(row["recorded_at"]),
      })),
    };
  };

  /** Depth five: everything a reader debugging Babel needs and a reader deciding never sees. */
  const machineryOf = async (
    row: SqlRow,
    payload: Record<string, unknown>,
  ): Promise<Record<string, string>> => {
    const id = text(row["id"]);
    const head = await one(
      `SELECT id, seq FROM records WHERE root_id = ? ORDER BY seq DESC, id DESC LIMIT 1`,
      [text(row["root_id"])],
    );
    const grant = await one(
      `SELECT policy_version, COUNT(*) AS claims, SUM(COALESCE(actual_cost, 0)) AS spent
         FROM claims WHERE record_id = ?`,
      [id],
    );
    const out: Record<string, string> = {
      createdAt: text(row["created_at"]),
      actor: `${text(row["actor_kind"])}:${text(row["actor_id"])}`,
      revision: head === null ? id : text(head["id"]),
      seq: String(count(row["seq"])),
      schema: String(numberField(payload, "schema")),
    };
    const runId = text(row["run_id"]);
    if (runId !== "") out["runId"] = runId;
    const recipe = text(row["recipe_id"]);
    if (recipe !== "") out["recipe"] = `${recipe}@${String(count(row["recipe_version"]))}`;
    const parent = text(row["parent_id"]);
    if (parent !== "") out["parent"] = parent;
    if (grant !== null && count(grant["claims"]) > 0) {
      out["policyVersion"] = text(grant["policy_version"]);
      out["reviews"] = String(count(grant["claims"]));
      out["reviewCostUsd"] = String(count(grant["spent"]));
    }
    if (runId !== "") {
      const run = await one(`SELECT cost_usd, tokens FROM runs WHERE id = ?`, [runId]);
      // A receipt whose usage block is all zeros is an engine that answered for nothing, not a
      // free run, so an absent cost says nobody here can price it.
      if (run !== null && typeof run["cost_usd"] === "number") {
        out["runCostUsd"] = String(run["cost_usd"]);
        out["runTokens"] = String(count(run["tokens"]));
      }
    }
    return out;
  };

  /**
   * Every connection a record has that its own words do not state.
   *
   * All of it is a relation somebody or something already asserted and none of it is computed
   * here: the typed edges in both directions, the revision chain's supersession, and the rest of
   * what this record's run wrote. A relation this build cannot resolve to a record it holds is
   * skipped rather than shown as a row with nothing behind it.
   */
  const relatedOf = async (
    id: string,
    runId: string,
    supersedesId: string,
  ): Promise<RecordPeel["related"]> => {
    const out: RecordPeel["related"] = [];
    const seen: Record<string, true> = { [id]: true };
    const add = (relation: string, otherId: string, kind: string, title: string): void => {
      if (otherId === "" || title === "" || seen[otherId] === true) return;
      if (
        kind !== "hypothesis" &&
        kind !== "observation" &&
        kind !== "finding" &&
        kind !== "proposal"
      ) {
        return;
      }
      seen[otherId] = true;
      out.push({ relation, id: otherId, kind, title });
    };
    if (supersedesId !== "") {
      const row = await one(`SELECT id, kind, title FROM records WHERE id = ?`, [supersedesId]);
      if (row !== null) add("supersedes", text(row["id"]), text(row["kind"]), text(row["title"]));
    }
    const replaced = await one(
      `SELECT id, kind, title FROM records WHERE supersedes_id = ? LIMIT 1`,
      [id],
    );
    if (replaced !== null) {
      add("superseded by", text(replaced["id"]), text(replaced["kind"]), text(replaced["title"]));
    }
    const edges = await db.query(
      `SELECT e.kind AS relation, r.id AS other_id, r.kind AS other_kind, r.title AS title
         FROM edges e JOIN records r ON r.id = e.to_id
        WHERE e.from_id = ? AND e.to_kind <> 'session' AND e.to_kind <> 'entity'
        UNION ALL
       SELECT e.kind, r.id, r.kind, r.title
         FROM edges e JOIN records r ON r.id = e.from_id
        WHERE e.to_id = ?
        ORDER BY relation, other_id
        LIMIT 200`,
      [id, id],
    );
    for (const row of edges) {
      add(text(row["relation"]), text(row["other_id"]), text(row["other_kind"]), text(row["title"]));
    }
    if (runId !== "") {
      const siblings = await db.query(
        `SELECT id, kind, title FROM records WHERE run_id = ? AND id <> ?
          ORDER BY created_at, id LIMIT 20`,
        [runId, id],
      );
      for (const row of siblings) {
        add("sibling", text(row["id"]), text(row["kind"]), text(row["title"]));
      }
    }
    return out;
  };

  /** The plan this record carries, if an interpreter proposed one and nobody has ruled yet. */
  const planOf = async (id: string): Promise<RecordPeel["plan"]> => {
    const row = await one(
      `SELECT kind, operation, state FROM plans
        WHERE subject_id = ? AND kind IN ('topic','backlog')
        ORDER BY created_at DESC, id DESC LIMIT 1`,
      [id],
    );
    if (row === null) return null;
    const kind = text(row["kind"]);
    return {
      kind: kind === "backlog" ? "backlog" : "topic",
      operation: text(row["operation"]),
      state: text(row["state"]),
    };
  };

  /**
   * The conversation under one record.
   *
   * Comments and acts are two lists rather than one stream, and that is §8.7's line rather than
   * a rendering preference: a ruling is the moderator's log and renders as the act it is,
   * attributed and dated, never as a comment. A reader who could not tell them apart would read
   * "reject" as somebody's opinion.
   *
   * A bare vote is not here either: a support with no prose is a reviewer's position, which the
   * score already carries, and rendering it as an empty comment would put a row in the
   * conversation that says nothing.
   */
  const thread = async (id: string): Promise<ThreadResult> => {
    const flat: Comment[] = [];
    const contributions = await db.query(
      `SELECT a.id AS id, a.run_id AS run_id, a.role AS role, a.recorded_at AS at,
              a.supersedes_id AS related_id, c.key AS position,
              json_extract(c.value, '$.text') AS text, json_extract(c.value, '$.kind') AS kind
         FROM (SELECT id, run_id, role, recorded_at, supersedes_id, payload FROM assessments
                WHERE record_id = ? AND json_valid(payload)) a,
              json_each(a.payload, '$.contributions') c
        WHERE TRIM(COALESCE(json_extract(c.value, '$.text'), '')) <> ''
        ORDER BY a.recorded_at, a.id, c.key
        LIMIT 500`,
      [id],
    );
    for (const row of contributions) {
      flat.push({
        // A contribution has no identity of its own in the store, and a thread whose rows shared
        // one id could not nest or be replied to.
        id: `${text(row["id"])}#${text(row["position"])}`,
        kind: text(row["kind"]) === "refinement" ? "refinement" : "contribution",
        author: { kind: "run", id: text(row["run_id"]) },
        role: text(row["role"]),
        text: text(row["text"]),
        at: text(row["at"]),
        relatedId: text(row["related_id"]),
        replies: [],
      });
    }
    const said = await db.query(
      `SELECT id, actor_id, reason, question, related_id, recorded_at FROM feedback
        WHERE record_id = ? AND TRIM(reason) <> ''
        ORDER BY recorded_at, id LIMIT 500`,
      [id],
    );
    for (const row of said) {
      flat.push({
        id: text(row["id"]),
        kind: count(row["question"]) === 1 ? "question" : "comment",
        author: { kind: "operator", id: text(row["actor_id"]) },
        role: "",
        text: text(row["reason"]),
        at: text(row["recorded_at"]),
        relatedId: text(row["related_id"]),
        replies: [],
      });
    }
    const answers = await db.query(
      `SELECT id, actor_id, outcome, text, recorded_at FROM answers
        WHERE question_id = ? ORDER BY recorded_at, id LIMIT 200`,
      [id],
    );
    for (const row of answers) {
      flat.push({
        id: text(row["id"]),
        kind: "answer",
        author: { kind: "operator", id: text(row["actor_id"]) },
        role: text(row["outcome"]),
        text: text(row["text"]),
        at: text(row["recorded_at"]),
        relatedId: "",
        replies: [],
      });
    }
    const acts = await db.query(
      `SELECT id, disposition, actor_id, note, recorded_at FROM dispositions
        WHERE record_id = ? ORDER BY seq DESC LIMIT 200`,
      [id],
    );
    const log: ThreadResult["acts"] = [];
    for (const row of acts) {
      const act = text(row["disposition"]);
      if (!isRuling(act)) continue;
      log.push({
        id: text(row["id"]),
        act,
        by: text(row["actor_id"]),
        at: text(row["recorded_at"]),
        reason: text(row["note"]),
      });
    }
    return { comments: nest(flat), acts: log, total: flat.length };
  };

  /**
   * What Babel did today, and what it is doing at this instant.
   *
   * Every number is read from its own source and none is derived from another: a count assembled
   * by summing two others is a number that goes wrong silently when either changes. One instant
   * decides the window and the claim expiry both, so a row cannot be inside today for one count
   * and outside it for another.
   */
  const pulse = async (): Promise<PulseResult> => {
    const at = clock();
    const since = stamp(Math.floor(at / 86_400_000) * 86_400_000);
    const written = await db.query(
      `SELECT kind, COUNT(*) AS n FROM records WHERE created_at >= ? GROUP BY kind`,
      [since],
    );
    let records = 0;
    let proposals = 0;
    for (const row of written) {
      records += count(row["n"]);
      if (text(row["kind"]) === "proposal") proposals += count(row["n"]);
    }
    const votes = await one(
      `SELECT COUNT(*) AS n FROM assessments WHERE recorded_at >= ? AND vote IS NOT NULL`,
      [since],
    );
    const ruled = await one(
      `SELECT COUNT(*) AS n FROM (
         SELECT d.record_id FROM dispositions d
          JOIN (SELECT record_id, MAX(seq) AS head_seq FROM dispositions GROUP BY record_id) n
            ON n.record_id = d.record_id AND n.head_seq = d.seq
         WHERE d.recorded_at >= ?)`,
      [since],
    );
    const topicPlans = await one(
      `SELECT COUNT(*) AS n FROM plans p JOIN records r ON r.id = p.subject_id
        WHERE p.kind = 'topic' AND p.state = 'open' AND r.created_at >= ?`,
      [since],
    );
    // The receipts rather than the citations of today's records, which is the honest half as
    // well as the cheap one: a record cites the sessions its evidence came from, which is the
    // material that survived into a claim rather than the material Babel read.
    const sessionsRead = await one(
      `SELECT COUNT(*) AS n FROM (
         SELECT DISTINCT json_extract(c.value, '$.host') AS host,
                         json_extract(c.value, '$.harness') AS harness,
                         json_extract(c.value, '$.sourceId') AS source
           FROM (SELECT preparation FROM runs
                  WHERE started_at >= ? AND preparation IS NOT NULL AND json_valid(preparation)) r,
                json_each(r.preparation, '$.selection') c)`,
      [since],
    );

    const current = await index();
    const titles: Record<string, { kind: string; title: string }> = {};
    for (const entry of current.posts) {
      titles[entry.post.id] = { kind: entry.post.kind, title: entry.post.title };
    }
    const reviewing: PulseResult["reviewing"] = [];
    const unnamed: string[] = [];
    for (const [recordId, grantedAt] of current.reviewing) {
      const held = titles[recordId];
      if (held === undefined) unnamed.push(recordId);
      reviewing.push({
        id: recordId,
        kind: held?.kind ?? "",
        title: held?.title ?? "",
        since: stamp(grantedAt),
      });
    }
    if (unnamed.length > 0) {
      // A record the front page does not carry is still under review: an observation is evidence
      // rather than a post, and a superseded wording is reached from its replacement. One read,
      // bounded by the open claims, is what it costs to name them.
      const rows = await db.query(
        `SELECT id, kind, title FROM records WHERE id IN (${unnamed.map(() => "?").join(",")})`,
        unnamed.slice(0, 500),
      );
      const named: Record<string, { kind: string; title: string }> = {};
      for (const row of rows) {
        named[text(row["id"])] = { kind: text(row["kind"]), title: text(row["title"]) };
      }
      for (const row of reviewing) {
        const held = named[row.id];
        if (held === undefined) continue;
        row.kind = held.kind;
        row.title = held.title;
      }
    }
    // Oldest first: the record held longest is the one a reader wonders about, and a list
    // ordered by identifier would reshuffle every poll. Ties resolve by identifier.
    reviewing.sort((left, right) =>
      left.since !== right.since
        ? left.since < right.since ? -1 : 1
        : left.id < right.id ? -1 : left.id > right.id ? 1 : 0,
    );

    return {
      since,
      today: {
        sessionsRead: count(sessionsRead?.["n"]),
        records,
        votes: count(votes?.["n"]),
        proposals,
        topicProposals: count(topicPlans?.["n"]),
        ruled: count(ruled?.["n"]),
      },
      reviewing,
    };
  };

  const runs = async (query: RunsQuery): Promise<RunsResult> => {
    const clauses: string[] = [];
    const params: SqlParam[] = [];
    if (query.state !== undefined) clauses.push(`(${RUN_STATE_WHERE[query.state]})`);
    if (query.machineId !== undefined) {
      clauses.push(`r.machine_id = ?`);
      params.push(query.machineId);
    }
    if (query.kind !== undefined) {
      clauses.push(`r.kind = ?`);
      params.push(query.kind);
    }
    const where = clauses.length === 0 ? "" : `WHERE ${clauses.join(" AND ")}`;
    const total = await one(`SELECT COUNT(*) AS n FROM runs r ${where}`, params);
    const rows = await db.query(
      `SELECT ${RUN_COLUMNS} FROM runs r ${where} ORDER BY r.started_at DESC, r.id DESC LIMIT ? OFFSET ?`,
      [...params, query.limit, query.offset],
    );
    const at = clock();
    return { runs: rows.map((row) => runRow(row, at)), total: count(total?.["n"]) };
  };

  const run = async (id: string): Promise<RunResult> => {
    const row = await one(`SELECT ${RUN_COLUMNS}, r.payload AS payload FROM runs r WHERE r.id = ?`, [id]);
    if (row === null) return { run: null, receipt: null };
    const receipt = document(row["payload"]);
    return {
      run: runRow(row, clock()),
      receipt: Object.keys(receipt).length === 0 ? null : receipt,
    };
  };

  /**
   * The evaluation policy in force: the newest row, with the ceilings and lanes read out of its
   * own document and the recipes joined to what has actually run under them.
   *
   * A recipe's title and what it looks for are the cookbook's, which is not in this store; when
   * the policy document does not carry them they are empty and the id is what a reader sees,
   * which is the honest answer rather than a label invented here.
   */
  const policy = async (): Promise<PolicyResult> => {
    const row = await one(
      `SELECT version, seq, actor_id, reason, payload, recorded_at FROM policies
        ORDER BY seq DESC LIMIT 1`,
      [],
    );
    const payload = document(row?.["payload"]);
    const at = clock();
    const since = stamp(Math.floor(at / 86_400_000) * 86_400_000);
    const spent = await one(
      `SELECT COALESCE(SUM(actual_cost), 0) AS spent FROM claims WHERE granted_at >= ?`,
      [since],
    );
    const ran = await db.query(
      `SELECT recipe_id AS id, COUNT(*) AS runs, MAX(started_at) AS last_ran_at,
              (SELECT id FROM runs inner_runs WHERE inner_runs.recipe_id = runs.recipe_id
                ORDER BY started_at DESC, id DESC LIMIT 1) AS last_run_id
         FROM runs WHERE recipe_id IS NOT NULL AND recipe_id <> ''
        GROUP BY recipe_id ORDER BY recipe_id LIMIT 200`,
      [],
    );
    const described: Record<string, Record<string, unknown>> = {};
    for (const entry of objectsField(payload, "recipes")) {
      described[stringField(entry, "id")] = entry;
    }
    const recipes: RecipeRow[] = ran.map((entry) => {
      const id = text(entry["id"]);
      const spelled = described[id] ?? {};
      const enabled = spelled["enabled"];
      return {
        id,
        title: stringField(spelled, "title"),
        looksFor: stringField(spelled, "looksFor"),
        enabled: typeof enabled === "boolean" ? enabled : true,
        lastRanAt: text(entry["last_ran_at"]),
        lastRunId: text(entry["last_run_id"]),
        runs: count(entry["runs"]),
      };
    });
    const lanes: PolicyResult["lanes"] = [];
    const shares: Record<string, string> = {
      coverage_share: "reception",
      exploration_share: "evidence",
      discovery_share: "challenge",
      filing_share: "filing",
      backlog_share: "backlog",
    };
    for (const [key, role] of Object.entries(shares)) {
      const share = numberField(payload, key);
      if (share > 0) lanes.push({ lane: key.replace("_share", ""), role, share });
    }
    return {
      version: text(row?.["version"]),
      seq: count(row?.["seq"]),
      actorId: text(row?.["actor_id"]),
      reason: text(row?.["reason"]),
      recordedAt: text(row?.["recorded_at"]),
      ceilings: {
        perRunUsd: numberField(payload, "per_cycle_cost"),
        perDayUsd: numberField(payload, "daily_cost"),
        concurrent: numberField(payload, "batch_size"),
      },
      spentTodayUsd: count(spent?.["spent"]),
      lanes,
      recipes,
      payload,
    };
  };

  const topic = async (name: string): Promise<TopicResult> => {
    const current = await index();
    const rows = await topicRows();
    const match = rows.find((row) => row.id === name) ?? rows.find((row) => row.name === name) ?? null;
    const proposed = (await topicProposals()).filter((proposal) =>
      match === null ? false : proposal.targets.some((target) => target.id === match.id),
    );
    const query: FeedQuery = {
      sort: "next",
      window: "all",
      kinds: [],
      needs: "all",
      topic: name,
      limit: 25,
      offset: 0,
    };
    const eligible = filterFeed(current.posts, { ...query, topic: name }, current.builtAt);
    sortFeed(eligible, "next", current.builtAt);
    return { topic: match, proposed, feed: page(eligible, query, current) };
  };

  return {
    db,
    now: clock,
    touch: () => {
      built = null;
    },
    index,
    feed,
    record,
    thread,
    topics: async () => ({
      topics: await topicRows(),
      proposed: await topicProposals(),
      unfiled: (await index()).unfiled,
    }),
    topic,
    pulse,
    runs,
    run,
    policy,
  };
}

// ---------------------------------------------------------------------------- helpers

/** A path's last element without its extension: the part that survives a session being moved. */
function fileStem(value: string): string {
  const name = value.slice(value.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  return dot <= 0 ? name : name.slice(0, dot);
}

/**
 * Nests replies under what they are about and orders every level newest first.
 *
 * A comment whose related record is not in this thread stays at the top level rather than being
 * dropped: it is still something somebody said about this record, and hiding it because its
 * parent lives elsewhere would lose prose to a relation the reader never asked about. The walk
 * carries a visited set rather than trusting the relations to be a forest — they come out of a
 * durable log this process did not write, and a reader that could be sent into unbounded
 * recursion by a cycle in it is a page that crashes the hub.
 */
function nest(flat: readonly Comment[]): Comment[] {
  if (flat.length === 0) return [];
  const position: Record<string, number> = {};
  for (let at = 0; at < flat.length; at++) {
    const comment = flat[at] as Comment;
    position[comment.id] = at;
    // A contribution's parent may be named by the assessment it belongs to rather than by the
    // contribution itself, which is the only identity a correction carries.
    const cut = comment.id.indexOf("#");
    if (cut > 0) {
      const base = comment.id.slice(0, cut);
      if (!Object.hasOwn(position, base)) position[base] = at;
    }
  }
  const children: Record<number, number[]> = {};
  const roots: number[] = [];
  for (let at = 0; at < flat.length; at++) {
    const comment = flat[at] as Comment;
    const parent = comment.relatedId === "" ? undefined : position[comment.relatedId];
    if (parent === undefined || parent === at) {
      roots.push(at);
      continue;
    }
    (children[parent] ??= []).push(at);
  }
  const newestFirst = (left: number, right: number): number => {
    const a = flat[left] as Comment;
    const b = flat[right] as Comment;
    if (a.at !== b.at) return a.at < b.at ? 1 : -1;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  };
  const walked = new Set<number>();
  const assemble = (at: number): Comment => {
    walked.add(at);
    const comment = { ...(flat[at] as Comment), replies: [] as Comment[] };
    const kids = children[at] ?? [];
    kids.sort(newestFirst);
    for (const child of kids) {
      if (walked.has(child)) continue;
      comment.replies.push(assemble(child));
    }
    return comment;
  };
  roots.sort(newestFirst);
  const out: Comment[] = [];
  for (const root of roots) {
    if (walked.has(root)) continue;
    out.push(assemble(root));
  }
  return out;
}

/** The vocabulary the feed door refuses by name rather than answering with an empty list. */
export const FEED_VOCABULARY = { sorts: FEED_SORTS, kinds: POST_KINDS, unfiled: TOPIC_UNFILED } as const;

export type { Standing };
