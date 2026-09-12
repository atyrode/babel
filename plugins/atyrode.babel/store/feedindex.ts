/*
  THE FEED INDEX: the whole deployment's posts, projected once and ranked whole (SPEC §8.7,
  ported from `internal/web/feed.go`).

  Three decisions shape this module, and they are the Go tree's, kept.

  The ranking is over the whole deployment, before paging. §8.5 is explicit: ordering uses the
  complete eligible set the projection represents, and a page ranked independently would make
  "hot" mean "hot among the twenty-five rows this request happened to read". So the index holds
  every post, the sort runs over it, and the window is cut afterwards.

  The index is a projection with a stated freshness, not a query. Assembling it is nine grouped
  reads — the ledger's entities and their facts, the live filings, the head of every post-kind
  record, the newest ruling on each, the questions, the reception, the operator's prose and the
  claims in flight — and doing that per request would make the front page the most expensive
  thing this process does. It is rebuilt on demand, at most once a minute, and `touch()` drops
  it so an act the operator has just performed is visible in the next read rather than a minute
  later.

  The grouping is SQL wherever the schema's indexes make it cheap: the head of a chain is a
  `MAX(seq)` over `records_by_root`, the newest ruling a `MAX(seq)` over `dispositions_by_record`,
  a question's state a correlated read of its own event log, and the prose under an assessment a
  `json_each` over its payload rather than the payload itself crossing the boundary. What is
  folded here is what SQL cannot say in one pass: §4.12's one-vote-per-run-per-role dedup, which
  needs the superseded set complete before the surviving votes can be counted.

  The index holds only what ranking needs. The peel, the thread and the run pages read the store
  directly, because they are about one row and this is about the order of all of them.
*/

import type { PluginDatabase, SqlParam, SqlRow } from "@manifold/plugin";
import {
  POST_KINDS,
  ROLES,
  type FeedPost,
  type FeedQuery,
  type FeedSort,
  type FeedWindow,
  type PostKind,
} from "../contract.ts";
import { standingOf } from "./acts.ts";
import {
  ageWord,
  feedWhy,
  risingRank,
  URGENCY,
  WINDOW_MS,
  type Ranked,
} from "./rank.ts";

/** The reserved topic naming the posts nothing has said anything about (§4.13). */
export const TOPIC_UNFILED = "unfiled";

/** The review roles §4.12 authorizes, as a membership test over what the store happens to hold. */
const REVIEW_ROLES: readonly string[] = ROLES;

/**
 * The standing the feed shows for a record whose newest ruling was lifted. It is derived on top
 * of `standingOf` rather than inside it: a ruling of `reopen` leaves a record undecided, which
 * is what the rule door enforces, and a record nobody has ever ruled on is a different object to
 * decide about, which is what a reader scanning a queue needs to see.
 */
export const STANDING_REOPENED = "reopened";

/**
 * Rows one paged scan reads at a time, and the ceiling it stops at.
 *
 * The page is well inside the database's ten-thousand-row and four-megabyte per-call bounds
 * (ADR 0034), and the cap is where a deployment has outgrown an in-memory front page — a
 * different problem from an unbounded request, and one this module should stop at rather than
 * discover.
 */
const SCAN_PAGE = 5_000;
const SCAN_CAP = 400_000;

/** The record kinds that are posts; observations are evidence and never rows (§4.13). */
const POST_KIND_LIST = "'hypothesis','finding','proposal'";

/** The ledger predicates a topic's binding and the operator's stance are read from (§4.8). */
const TOPIC_PREDICATES = "'lifecycle','analysis-policy','repository-remote','local-path'";

/** The question states that await the operator, and the half-sentence each one earns (§4.8). */
const QUESTION_WAITS: Record<string, string> = {
  "answered-uninterpreted": "no plan yet",
  "plan-ready": "plan ready",
};

/** What an open question's class costs the operator to leave alone. */
const QUESTION_CLASS_WAITS: Record<string, string> = {
  blocking: "blocks a run",
  maintenance: "upkeep",
};

/** One topic as the index counts it; the operator's stance is attached per request. */
export interface IndexTopic {
  readonly id: string;
  readonly name: string;
  readonly kind: string;
  readonly binding: { kind: string; identity: string; remote: string; paths: string[] } | null;
  posts: number;
  awaiting: number;
  latestAt: string;
}

/** One topic a record is filed under, as both the wire and the `?topic=` filter read it. */
export interface TopicMembership {
  readonly id: string;
  readonly name: string;
}

/** One post as the index holds it: the wire row plus the few facts the ranks need. */
export interface IndexEntry {
  readonly post: FeedPost;
  /** The parsed instants the ranks and the windows use; the wire carries their text. */
  readonly createdAt: number;
  readonly activity: number[];
  urgency: number;
  /**
   * The live topics this post is filed under. Both halves travel because `?topic=` takes
   * either: a reader following a sidebar row has the id, one who typed the name has the name.
   */
  readonly topics: readonly TopicMembership[];
}

export interface FeedIndex {
  readonly builtAt: number;
  readonly posts: readonly IndexEntry[];
  readonly topics: readonly IndexTopic[];
  /** The posts no live filing names a topic for — §4.13's honest state and the triage backlog. */
  readonly unfiled: number;
  /** One record a reviewer is holding right now, by record id, oldest claim first. */
  readonly reviewing: ReadonlyMap<string, number>;
}

/** One subject's reception as the feed counts it (`internal/evaluation`'s Tally). */
interface Tally {
  support: number;
  oppose: number;
  unsure: number;
  contested: boolean;
  comments: number;
  lastActivity: number;
  activity: number[];
  votes: FeedPost["votes"];
}

// ---------------------------------------------------------------------------- reading

/** A column as text; a null, an absent column and a number all answer honestly. */
function text(value: SqlParam | undefined): string {
  if (typeof value === "string") return value;
  if (value === null || value === undefined || value instanceof Uint8Array) return "";
  return String(value);
}

/** A counted column as a number; SQLite hands `COUNT` back as a number or a bigint. */
function count(value: SqlParam | undefined): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  return 0;
}

/** A stored instant in milliseconds; an unparseable or absent one is zero. */
export function instant(value: SqlParam | undefined): number {
  const parsed = Date.parse(text(value));
  return Number.isNaN(parsed) ? 0 : parsed;
}

/**
 * An instant as the store writes them: ISO-8601 UTC with nine fractional digits, fixed width.
 *
 * The width is what makes a text comparison a time comparison — `expires_at > ?` and
 * `created_at >= ?` are both range reads over stored text — and it is the shape the Go tree
 * wrote, so a row minted today compares byte for byte against the sixty-five thousand imported
 * beside it.
 */
export function stamp(milliseconds: number): string {
  return `${new Date(milliseconds).toISOString().slice(0, -1)}000000Z`;
}

/**
 * Reads one statement to exhaustion in pages, so a projection over a corpus larger than the
 * database's per-call row bound is a loop rather than a refusal. The statement must carry its
 * own total order, because the paging is `OFFSET` and an unordered page is a different page
 * each time it is read.
 */
async function scan<Row extends SqlRow>(
  db: PluginDatabase,
  sql: string,
  params: readonly SqlParam[],
  receive: (row: Row) => void,
): Promise<void> {
  for (let offset = 0; offset < SCAN_CAP; offset += SCAN_PAGE) {
    const page = await db.query<Row>(
      `${sql} LIMIT ${String(SCAN_PAGE)} OFFSET ${String(offset)}`,
      params,
    );
    for (const row of page) receive(row);
    if (page.length < SCAN_PAGE) return;
  }
}

// ---------------------------------------------------------------------------- the build

/**
 * Assembles the whole deployment's posts at one instant.
 *
 * `nowMs` is the build's own clock and every age in the index is measured from it — "waiting
 * 3d", "asked 2h" — rather than from the instant a request arrives. The two differ by at most
 * the index's freshness, and reading the clock per row would let two rows of one page disagree
 * about now.
 */
export async function buildFeedIndex(db: PluginDatabase, nowMs: number): Promise<FeedIndex> {
  const topics = await readTopics(db);
  const membership = await readFilings(db, topics);
  const standings = await readStandings(db);
  const tallies = await readTallies(db);
  const reviewing = await readOpenClaims(db, nowMs);

  const posts: IndexEntry[] = [];
  await scan<SqlRow>(
    db,
    `SELECT r.id AS id, r.kind AS kind, r.run_id AS run_id, r.title AS title,
            r.created_at AS created_at
       FROM records r
       JOIN (SELECT root_id, MAX(seq) AS head_seq FROM records GROUP BY root_id) h
         ON h.root_id = r.root_id AND h.head_seq = r.seq
      WHERE r.kind IN (${POST_KIND_LIST})
      ORDER BY r.id`,
    [],
    (row) => {
      const entry = recordEntry(row, membership, standings, tallies, reviewing, nowMs);
      // A row whose line this build could not read is an identifier in a list, which is the
      // surface §8.6 replaced.
      if (entry !== null) posts.push(entry);
    },
  );
  await scan<SqlRow>(
    db,
    `SELECT q.id AS id, q.class AS class, q.text AS text, q.created_at AS created_at,
            COALESCE((SELECT e.state FROM question_events e
                       WHERE e.question_id = q.id ORDER BY e.seq DESC LIMIT 1), 'open') AS state,
            (SELECT COUNT(*) FROM answers a WHERE a.question_id = q.id) AS answers
       FROM questions q
      ORDER BY q.id`,
    [],
    (row) => {
      const entry = questionEntry(row, reviewing, nowMs);
      if (entry !== null) posts.push(entry);
    },
  );

  const unfiled = countTopics(posts, topics);
  return { builtAt: nowMs, posts, topics, unfiled, reviewing };
}

/**
 * The topics the operator has created (§4.13): every live ledger entity, with what it is bound
 * to.
 *
 * Every entity is a topic, not only the ones something is filed under — §4.13 makes a topic a
 * ledger entity and nothing else, so a project the operator named and Babel has written nothing
 * about yet is an empty topic rather than an absent one, which is what makes it possible to file
 * the first record under it. A merged-away identity is skipped because the entity that speaks
 * for it is already in the list, and a retired one because retiring re-queues its filings: it is
 * no longer a place records live.
 */
async function readTopics(db: PluginDatabase): Promise<IndexTopic[]> {
  const facts = await readEntityFacts(db);
  const topics: IndexTopic[] = [];
  await scan<SqlRow>(
    db,
    `SELECT id, kind, name, canonical_id FROM entities ORDER BY id`,
    [],
    (row) => {
      const id = text(row["id"]);
      const canonical = text(row["canonical_id"]);
      if (canonical !== "" && canonical !== id) return;
      const held = facts.get(id);
      if (held !== undefined && held.lifecycle === "retired") return;
      const kind = text(row["kind"]);
      const remote = held?.remote ?? "";
      const paths = held?.paths ?? [];
      const identity = remote === "" ? (paths[0] ?? "") : remote;
      topics.push({
        id,
        name: text(row["name"]),
        kind,
        binding: identity === "" ? null : { kind, identity, remote, paths },
        posts: 0,
        awaiting: 0,
        latestAt: "",
      });
    },
  );
  return topics;
}

/** One entity's live binding and stance facts, folded out of the ledger's append-only rows. */
export interface EntityFacts {
  lifecycle: string;
  analysisPolicy: string;
  remote: string;
  paths: string[];
  /** The deciding fact's own attribution, so a stance can say whose act it was. */
  reason: string;
  by: string;
  at: string;
}

/**
 * Reads the ledger facts a topic is described by, newest live one per predicate.
 *
 * A fact is live when nothing supersedes it and its own newest status does not say otherwise; a
 * fact with no status row at all is live, which is what an import that carried the facts and not
 * their statuses leaves behind. The local paths are the exception and accumulate: a repository
 * is checked out in as many places as the operator checked it out, and the newest of them is not
 * the only one.
 */
async function readEntityFacts(db: PluginDatabase): Promise<Map<string, EntityFacts>> {
  const rows: SqlRow[] = [];
  const superseded = new Set<string>();
  await scan<SqlRow>(
    db,
    `SELECT f.id AS id, f.entity_id AS entity_id, f.predicate AS predicate, f.value AS value,
            f.note AS note, f.authority_id AS authority_id, f.recorded_at AS recorded_at,
            f.supersedes_id AS supersedes_id,
            COALESCE((SELECT s.status FROM fact_status s
                       WHERE s.fact_id = f.id ORDER BY s.seq DESC LIMIT 1), 'active') AS status
       FROM facts f
      WHERE f.predicate IN (${TOPIC_PREDICATES})
      ORDER BY f.entity_id, f.predicate, f.recorded_at, f.id`,
    [],
    (row) => {
      const replaces = text(row["supersedes_id"]);
      if (replaces !== "") superseded.add(replaces);
      rows.push(row);
    },
  );
  const out = new Map<string, EntityFacts>();
  for (const row of rows) {
    if (superseded.has(text(row["id"])) || text(row["status"]) === "superseded") continue;
    const entityId = text(row["entity_id"]);
    let held = out.get(entityId);
    if (held === undefined) {
      held = { lifecycle: "", analysisPolicy: "", remote: "", paths: [], reason: "", by: "", at: "" };
      out.set(entityId, held);
    }
    const value = text(row["value"]);
    switch (text(row["predicate"])) {
      case "lifecycle":
        held.lifecycle = value;
        held.reason = text(row["note"]);
        held.by = text(row["authority_id"]);
        held.at = text(row["recorded_at"]);
        break;
      case "analysis-policy":
        held.analysisPolicy = value;
        if (value === "excluded") {
          held.reason = text(row["note"]);
          held.by = text(row["authority_id"]);
          held.at = text(row["recorded_at"]);
        }
        break;
      case "repository-remote":
        held.remote = value;
        break;
      case "local-path":
        held.paths.push(value);
        break;
      default:
        break;
    }
  }
  for (const held of out.values()) held.paths.sort();
  return out;
}

/**
 * The operator's stance toward one topic, read off the same facts (§4.13).
 *
 * It is a reading of the ledger rather than a stored state: an `excluded` analysis policy is
 * the refusal whatever the lifecycle says, and a lifecycle this vocabulary does not spell —
 * retired, or anything a later build adds — reads as nothing said, which is a different answer
 * from "not now" and is shown as one.
 */
export function interestOf(facts: EntityFacts | undefined): {
  state: "" | "working" | "watching" | "not-now" | "excluded";
  reason: string;
  at: string;
  by: string;
} {
  if (facts === undefined) return { state: "", reason: "", at: "", by: "" };
  const attribution = { reason: facts.reason, at: facts.at, by: facts.by };
  if (facts.analysisPolicy === "excluded") return { state: "excluded", ...attribution };
  switch (facts.lifecycle) {
    case "active":
      return { state: "working", ...attribution };
    case "maintenance-only":
      return { state: "watching", ...attribution };
    case "dormant":
      return { state: "not-now", ...attribution };
    default:
      return { state: "", reason: "", at: "", by: "" };
  }
}

/** Reads the entity facts for the topics door, which attaches a stance per request. */
export async function readInterests(db: PluginDatabase): Promise<Map<string, EntityFacts>> {
  return await readEntityFacts(db);
}

/**
 * What each record is filed under right now: the newest filing per record and entity wins, a
 * withdrawal kills it, and a filing naming nothing in particular files nothing.
 *
 * A filing under an entity this deployment no longer lists as a topic contributes nothing — a
 * merged-away or retired entity is not a place records live, so the record reads unfiled, which
 * is the honest answer rather than a topic nobody can open.
 *
 * The order breaks its tie on `rowid` rather than on the identifier, and that is a correctness
 * rule rather than a preference: ids are random hex, a file and the unfile that follows it land
 * in the same millisecond inside one batch, and nothing deletes a filing — so the row written
 * last is the row with the greatest rowid, and ordering by id would resolve a re-filing against
 * an arbitrary one of the two.
 */
async function readFilings(
  db: PluginDatabase,
  topics: readonly IndexTopic[],
): Promise<Map<string, TopicMembership[]>> {
  const names = new Map<string, string>();
  for (const topic of topics) names.set(topic.id, topic.name);
  // The scan pages in write order, for `readTallies`' measured reason, and the "which filing
  // holds" question is then answered by comparing the pair each row would replace: newest
  // `created_at`, and the later write when two share an instant.
  const byRecord = new Map<string, Map<string, { at: string; written: number; filed: boolean }>>();
  await scan<SqlRow>(
    db,
    `SELECT record_id, entity_id, withdrawn, created_at, rowid AS written FROM filings
      ORDER BY rowid`,
    [],
    (row) => {
      const entityId = text(row["entity_id"]);
      if (entityId === "" || !names.has(entityId)) return;
      const recordId = text(row["record_id"]);
      let held = byRecord.get(recordId);
      if (held === undefined) {
        held = new Map<string, { at: string; written: number; filed: boolean }>();
        byRecord.set(recordId, held);
      }
      const at = text(row["created_at"]);
      const written = count(row["written"]);
      const standing = held.get(entityId);
      if (standing !== undefined && (standing.at > at || (standing.at === at && standing.written > written))) {
        return;
      }
      held.set(entityId, { at, written, filed: count(row["withdrawn"]) === 0 });
    },
  );
  const out = new Map<string, TopicMembership[]>();
  for (const [recordId, entities] of byRecord) {
    const membership: TopicMembership[] = [];
    for (const [entityId, standing] of entities) {
      const name = names.get(entityId);
      if (!standing.filed || name === undefined) continue;
      membership.push({ id: entityId, name });
    }
    if (membership.length > 0) out.set(recordId, membership);
  }
  return out;
}

/** The newest ruling on every record that has ever been ruled on (§4.7). */
async function readStandings(db: PluginDatabase): Promise<Map<string, string>> {
  const out = new Map<string, string>();
  await scan<SqlRow>(
    db,
    `SELECT d.record_id AS record_id, d.disposition AS disposition
       FROM dispositions d
       JOIN (SELECT record_id, MAX(seq) AS head_seq FROM dispositions GROUP BY record_id) n
         ON n.record_id = d.record_id AND n.head_seq = d.seq
      ORDER BY d.record_id`,
    [],
    (row) => {
      out.set(text(row["record_id"]), text(row["disposition"]));
    },
  );
  return out;
}

/**
 * The deployment's reception, grouped by record in one pass (`internal/evaluation`'s Tallies).
 *
 * The dedup rule is §4.12's own: one vote per run per role. A correction supersedes the
 * statement it names, so a superseded assessment is dropped entirely; of what remains the newest
 * per (record, run, role) is the one that counts, which is what makes a re-granted review a
 * changed vote rather than a second one.
 *
 * The votes are Babel's reviewers and only theirs. The operator's prose is read for its words,
 * never for a position (§8.7: "the score is Babel's reception and only Babel's"), so a bare
 * stance folds into nothing — not a column, not a comment and not activity — and there is no
 * arithmetic in which his click could become a model's observation.
 *
 * The paging order is the table's own write order and the commit order is restored once, here.
 * That is measured rather than stylistic: `recorded_at` carries no index, so an `ORDER BY` on it
 * makes every `OFFSET` page a fresh sort of the whole table — twenty pages of forty thousand
 * rows cost twenty sorts where one costs one. `rowid` also breaks the tie an instant cannot,
 * which matters for the same reason it matters on a filing: two assessments written inside one
 * batch share a millisecond, and the later one is the one that counts.
 */
async function readTallies(db: PluginDatabase): Promise<Map<string, Tally>> {
  const rows: SqlRow[] = [];
  const superseded = new Set<string>();
  await scan<SqlRow>(
    db,
    `SELECT a.id AS id, a.record_id AS record_id, a.run_id AS run_id, a.role AS role,
            a.vote AS vote, a.supersedes_id AS supersedes_id, a.recorded_at AS recorded_at,
            a.rowid AS written,
            CASE WHEN json_valid(a.payload)
                 THEN (SELECT COUNT(*) FROM json_each(a.payload, '$.contributions') c
                        WHERE TRIM(COALESCE(json_extract(c.value, '$.text'), '')) <> '')
                 ELSE 0 END AS prose
       FROM assessments a
      ORDER BY a.rowid`,
    [],
    (row) => {
      const replaces = text(row["supersedes_id"]);
      if (replaces !== "") superseded.add(replaces);
      rows.push(row);
    },
  );
  rows.sort((left, right) => {
    const leftAt = text(left["recorded_at"]);
    const rightAt = text(right["recorded_at"]);
    if (leftAt !== rightAt) return leftAt < rightAt ? -1 : 1;
    return count(left["written"]) - count(right["written"]);
  });

  const out = new Map<string, Tally>();
  const ensure = (recordId: string): Tally => {
    let held = out.get(recordId);
    if (held === undefined) {
      held = {
        support: 0, oppose: 0, unsure: 0, contested: false, comments: 0,
        lastActivity: 0, activity: [], votes: [],
      };
      out.set(recordId, held);
    }
    return held;
  };

  // The newest surviving vote per record, run and role. The rows arrive in commit order, so a
  // later one simply replaces an earlier one under the same key.
  type SurvivingVote = { record: string } & FeedPost["votes"][number];
  const votes = new Map<string, SurvivingVote>();
  for (const row of rows) {
    if (superseded.has(text(row["id"]))) continue;
    const recordId = text(row["record_id"]);
    const tally = ensure(recordId);
    const at = instant(row["recorded_at"]);
    const vote = text(row["vote"]);
    if (vote === "support" || vote === "oppose" || vote === "unsure") {
      // A grant whose role this build cannot name is a vote in the columns and outside the
      // by-role split, exactly as a grant that carried no role at all is: crediting it to a role
      // nobody authorized it for is how a bare vote comes to read as a satisfied evidence check.
      const stored = text(row["role"]);
      const role = REVIEW_ROLES.includes(stored) ? (stored as FeedPost["votes"][number]["role"]) : "";
      votes.set(`${recordId}\u0000${text(row["run_id"])}\u0000${stored}`, { record: recordId, role, vote });
      tally.activity.push(at);
    }
    for (let said = count(row["prose"]); said > 0; said--) {
      tally.comments++;
      tally.activity.push(at);
    }
    if (at > tally.lastActivity) tally.lastActivity = at;
  }

  // The columns and the by-role split are folded out of the same surviving votes, so a row's
  // score and its contested mark cannot disagree about which votes counted. A vote whose grant
  // carried no role is in the columns and outside the split: crediting it to a role nobody
  // authorized it for is how a bare vote comes to read as a satisfied evidence check.
  const sides = new Map<string, { support: boolean; oppose: boolean }>();
  for (const held of votes.values()) {
    const tally = ensure(held.record);
    if (held.vote === "support") tally.support++;
    else if (held.vote === "oppose") tally.oppose++;
    else tally.unsure++;
    tally.votes.push({ role: held.role, vote: held.vote });
    if (held.role === "") continue;
    const key = `${held.record}\u0000${held.role}`;
    let split = sides.get(key);
    if (split === undefined) {
      split = { support: false, oppose: false };
      sides.set(key, split);
    }
    if (held.vote === "support") split.support = true;
    if (held.vote === "oppose") split.oppose = true;
  }
  for (const [key, split] of sides) {
    if (!split.support || !split.oppose) continue;
    const recordId = key.slice(0, key.indexOf("\u0000"));
    ensure(recordId).contested = true;
  }
  for (const tally of out.values()) {
    tally.votes.sort((left, right) =>
      left.role === right.role
        ? left.vote < right.vote ? -1 : left.vote > right.vote ? 1 : 0
        : left.role < right.role ? -1 : 1,
    );
  }

  // The operator's own words under a record are comments and activity; his stance is neither.
  await scan<SqlRow>(
    db,
    `SELECT record_id, recorded_at FROM feedback
      WHERE TRIM(reason) <> ''
      ORDER BY rowid`,
    [],
    (row) => {
      const tally = ensure(text(row["record_id"]));
      const at = instant(row["recorded_at"]);
      tally.comments++;
      tally.activity.push(at);
      if (at > tally.lastActivity) tally.lastActivity = at;
    },
  );
  return out;
}

/**
 * Every record a reviewer is holding right now, with the instant the oldest claim on it was
 * granted.
 *
 * Open is three conditions and all three are the coordinator's own: the claim has not been
 * finished, its lease has not lapsed, and it names a record. An expired lease is not open — the
 * next claimer may take it at any instant, which is precisely what the fence exists for — and a
 * finished one is work that has already been said rather than work in flight. Neither is
 * deleted: the schema refuses that, so both are still here and both are excluded by what they
 * say about themselves rather than by their absence.
 */
async function readOpenClaims(db: PluginDatabase, nowMs: number): Promise<Map<string, number>> {
  const out = new Map<string, number>();
  const now = stamp(nowMs);
  await scan<SqlRow>(
    db,
    `SELECT record_id, granted_at FROM claims
      WHERE (finished_at IS NULL OR finished_at = '') AND expires_at > ? AND record_id <> ''
      ORDER BY record_id, granted_at, id`,
    [now],
    (row) => {
      const recordId = text(row["record_id"]);
      const at = instant(row["granted_at"]);
      const held = out.get(recordId);
      if (held === undefined || at < held) out.set(recordId, at);
    },
  );
  return out;
}

// ---------------------------------------------------------------------------- projection

/** Projects one head record into a post, or nothing when it carries no line to show. */
function recordEntry(
  row: SqlRow,
  membership: ReadonlyMap<string, TopicMembership[]>,
  standings: ReadonlyMap<string, string>,
  tallies: ReadonlyMap<string, Tally>,
  reviewing: ReadonlyMap<string, number>,
  nowMs: number,
): IndexEntry | null {
  const id = text(row["id"]);
  const title = text(row["title"]);
  if (id === "" || title === "") return null;
  // The statement restricts `kind` to the three post kinds, which the column's CHECK also
  // constrains; the compiler cannot read either.
  const kind = text(row["kind"]) as PostKind;
  const ruling = standings.get(id);
  const standing = ruling === "reopen" ? STANDING_REOPENED : standingOf(ruling ?? null);
  const createdAtText = text(row["created_at"]);
  const createdAt = instant(createdAtText);
  const runId = text(row["run_id"]);
  const filed = membership.get(id);
  const tally = tallies.get(id);

  const post: FeedPost = {
    id,
    kind,
    title,
    standing,
    createdAt: createdAtText,
    author: runId === "" ? null : { runId },
    topics: filed ?? [],
    score: (tally?.support ?? 0) - (tally?.oppose ?? 0),
    support: tally?.support ?? 0,
    oppose: tally?.oppose ?? 0,
    unsure: tally?.unsure ?? 0,
    votes: tally?.votes ?? [],
    contested: tally?.contested ?? false,
    reviewing: reviewing.has(id),
    comments: tally?.comments ?? 0,
    awaiting: false,
    why: "",
    lastActivityAt: stamp(Math.max(tally?.lastActivity ?? 0, createdAt)),
  };
  const entry: IndexEntry = {
    post,
    createdAt,
    activity: tally?.activity ?? [],
    urgency: URGENCY.none,
    topics: filed ?? [],
  };

  // Two standings await a ruling and they are the record page's own two: `new`, which nobody
  // has decided, and `reopened`, whose ruling an operator deliberately lifted. Those are exactly
  // the two the peel offers "Rule on this" against, so the feed and the record cannot disagree
  // about what needs him. Every other standing is a ruling that was made, and a deferral is a
  // decision rather than the postponement of one.
  if (standing === "new") {
    entry.urgency = URGENCY.unruled;
    post.awaiting = true;
    post.why = feedWhy("never ruled on", `waiting ${ageWord(nowMs - createdAt)}`);
  } else if (standing === STANDING_REOPENED) {
    entry.urgency = URGENCY.reopened;
    post.awaiting = true;
    post.why = feedWhy("reopened", `waiting ${ageWord(nowMs - createdAt)}`);
  }
  return entry;
}

/**
 * Projects one of the ledger's questions, which §8.7 puts in the feed beside the records.
 *
 * A question carries no run author: the ledger records what was asked and why, and the run that
 * provoked it is not part of the question. Its standing is its own state. Three of §4.8's states
 * await the operator and the rest do not — `open` is the ordinary one, `answered-uninterpreted`
 * is his answer sitting with no plan drawn from it, and `plan-ready` is an interpretation
 * waiting for the single acceptance §4.8 requires of him — and the urgency is the question's
 * CLASS rather than its state, because what a blocking question costs is a run that has stopped.
 */
function questionEntry(
  row: SqlRow,
  reviewing: ReadonlyMap<string, number>,
  nowMs: number,
): IndexEntry | null {
  const id = text(row["id"]);
  const title = text(row["text"]);
  if (id === "" || title === "") return null;
  const createdAtText = text(row["created_at"]);
  const createdAt = instant(createdAtText);
  const state = text(row["state"]);
  const questionClass = text(row["class"]);
  const post: FeedPost = {
    id,
    kind: "question",
    title,
    standing: state,
    createdAt: createdAtText,
    author: null,
    topics: [],
    score: 0,
    support: 0,
    oppose: 0,
    unsure: 0,
    votes: [],
    contested: false,
    reviewing: reviewing.has(id),
    comments: count(row["answers"]),
    awaiting: false,
    why: "",
    lastActivityAt: stamp(createdAt),
  };
  const entry: IndexEntry = {
    post,
    createdAt,
    activity: [],
    urgency: URGENCY.none,
    topics: [],
  };
  const head =
    state === "open"
      ? (QUESTION_CLASS_WAITS[questionClass] ?? "curiosity")
      : QUESTION_WAITS[state];
  if (head === undefined) return entry;
  post.awaiting = true;
  post.why = feedWhy(head, `asked ${ageWord(nowMs - createdAt)}`);
  entry.urgency = questionClass === "blocking" ? URGENCY.blocked : URGENCY.asked;
  return entry;
}

/**
 * Counts the posts under each topic from the posts themselves, so a topic's count and the feed
 * it opens cannot disagree, and reports how many are filed under nothing.
 *
 * Awaiting is counted beside the total because §4.13 puts the operator's attention on the topic
 * page: a topic with forty posts and nothing waiting is a different thing to open from one with
 * three that all need a ruling.
 */
function countTopics(posts: readonly IndexEntry[], topics: readonly IndexTopic[]): number {
  const filed = new Map<string, number>();
  const awaiting = new Map<string, number>();
  const latest = new Map<string, number>();
  let unfiled = 0;
  for (const entry of posts) {
    if (entry.topics.length === 0) {
      unfiled++;
      continue;
    }
    for (const { id } of entry.topics) {
      filed.set(id, (filed.get(id) ?? 0) + 1);
      if (entry.post.awaiting) awaiting.set(id, (awaiting.get(id) ?? 0) + 1);
      if (entry.createdAt > (latest.get(id) ?? 0)) latest.set(id, entry.createdAt);
    }
  }
  for (const topic of topics) {
    topic.posts = filed.get(topic.id) ?? 0;
    topic.awaiting = awaiting.get(topic.id) ?? 0;
    const at = latest.get(topic.id) ?? 0;
    topic.latestAt = at === 0 ? "" : stamp(at);
  }
  return unfiled;
}

// ---------------------------------------------------------------------------- narrowing

/**
 * Narrows the index to the eligible set, which is what the sort then orders whole.
 *
 * The window applies to `top` and `controversial` and to nothing else, because those are the two
 * sorts that are ABOUT a period: "hot" over a day and "hot" over all time would be the same list
 * with the older half deleted, and a rising post is by definition recent.
 *
 * `needs` is the queue, and it is a filter rather than a second list: a post awaiting the
 * operator is a fact the feed already carries, so asking for only those narrows one order
 * instead of opening another that could disagree with it about what is waiting.
 */
export function filterFeed(
  posts: readonly IndexEntry[],
  query: Pick<FeedQuery, "sort" | "window" | "kinds" | "needs"> & { topic: string },
  nowMs: number,
): IndexEntry[] {
  const windowed: FeedSort[] = ["top", "controversial"];
  const width = windowed.includes(query.sort) ? WINDOW_MS[query.window as FeedWindow] : 0;
  const since = width > 0 ? nowMs - width : 0;
  const needs = query.needs === "me";
  const out: IndexEntry[] = [];
  for (const entry of posts) {
    if (query.kinds.length > 0 && !query.kinds.includes(entry.post.kind)) continue;
    if (needs && !entry.post.awaiting) continue;
    if (query.topic !== "" && !inTopic(entry, query.topic)) continue;
    if (since > 0 && entry.createdAt < since) continue;
    if (query.sort === "rising" && risingRank(entry.activity, entry.createdAt, nowMs) === 0) continue;
    out.push(entry);
  }
  return out;
}

/**
 * Whether one post belongs to the topic a reader asked for, by the topic's name or by its id.
 *
 * Both are accepted because both are what a reader has: a sidebar row carries the entity id and
 * is unambiguous, a name is what an operator types. A name two entities answer to opens both,
 * which is the honest answer to an ambiguous question.
 */
export function inTopic(entry: IndexEntry, topic: string): boolean {
  if (topic === TOPIC_UNFILED) return entry.topics.length === 0;
  return entry.topics.some((filed) => filed.name === topic || filed.id === topic);
}

/** The post kinds the feed admits, as the door's refusal names them. */
export const FEED_KINDS: readonly PostKind[] = POST_KINDS;

/** Everything `sortFeed` needs of an index entry, restated so the type is checkable here. */
export type RankedEntry = IndexEntry & Ranked;
