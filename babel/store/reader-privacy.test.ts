import { afterEach, beforeEach, expect, test } from "bun:test";
import { join, resolve } from "node:path";
import type { SqlParam, SqlRow } from "@manifold/plugin";
import { NeighborhoodQuerySchema, type FeedQuery } from "../contract.ts";
import { stamp } from "./feedindex.ts";
import { excludeSessionWhenQuiescent } from "./source-privacy.ts";
import type { BabelStore } from "./store.ts";
import { openStore } from "./store.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

// Parent-runnable before/after route, without live access or modifying either source tree:
// BABEL_PRIVACY_REPRO_ROOT=/path/to/67a097f bun test /path/to/repaired/babel/store/reader-privacy.test.ts
// With no override the same consumer regressions exercise this checkout's reader implementation.
const root = process.env["BABEL_PRIVACY_REPRO_ROOT"];
// Only the optional baseline module is runtime-selected; normal tests use the static import.
const openReaderStore: typeof openStore = root
  ? (await import(join(resolve(root), "babel/store/store.ts"))).openStore
  : openStore;
const NOW = Date.UTC(2026, 9, 3, 12);
const HOUR = 3_600_000;
const PRIVATE_SOURCE = "omp/reader-private-synthetic";
const PUBLIC_SOURCE = "omp/reader-public-synthetic";
const PRIVATE_RUN = "run_reader_private";
const PUBLIC_RUN = "run_reader_public";
const PRIVATE_RECORD = "hyp_000000a1";
const PRIVATE_PROPOSAL = "pro_000000a2";
const PRIVATE_OBSERVATION = "obs_000000a3";
const PUBLIC_RECORD = "fnd_000000a4";
const PRIVATE_QUESTION = "qst_000000a1";
const PUBLIC_QUESTION = "qst_000000a2";
const PRIVATE_TOPIC = "ent_000000a1";
const PUBLIC_TOPIC = "ent_000000a2";
const RECIPE = "reader-synthetic-lens";
const WITHHELD = "WITHHELD_SYNTHETIC";
const QUERY: FeedQuery = {
  sort: "new",
  window: "all",
  kinds: [],
  surface: "all",
  established: [],
  group: "none",
  limit: 100,
  offset: 0,
};
let fixture: TestStore;
let store: BabelStore;

async function seed(): Promise<void> {
  const { db } = fixture;
  for (const [selector, title, label] of [
    [PRIVATE_SOURCE, `${WITHHELD} source title`, `${WITHHELD} archive label`],
    [PUBLIC_SOURCE, "Independent source title", "independent-archive"],
  ] as const) {
    await insert(db, "sessions", {
      selector,
      host: "synthetic-host",
      harness: "omp",
      source_id: selector.slice(4),
      title,
      title_provenance: "recorded",
      archive_label: label,
      seen_at: stamp(NOW),
    });
  }
  for (const [id, selector, offset] of [
    [PRIVATE_RUN, PRIVATE_SOURCE, 0],
    [PUBLIC_RUN, PUBLIC_SOURCE, HOUR],
  ] as const) {
    await insert(db, "runs", {
      id,
      kind: "explore",
      recipe_id: RECIPE,
      started_at: stamp(NOW - offset),
      finished_at: stamp(NOW),
      closure: "completed",
      preparation: JSON.stringify({
        selectors: [selector],
        selection: [{ host: "synthetic-host", harness: "omp", sourceId: selector.slice(4) }],
      }),
      payload: JSON.stringify({ summary: id === PRIVATE_RUN ? WITHHELD : "Independent receipt" }),
    });
  }
  await insert(db, "runs", {
    id: "run_reader_unknown",
    kind: "explore",
    started_at: stamp(NOW),
    payload: JSON.stringify({ posting: true }),
  });
  for (const [id, createdBy, name] of [
    [PRIVATE_TOPIC, PRIVATE_RUN, `${WITHHELD} topic`],
    [PUBLIC_TOPIC, "synthetic-owner", "independent-topic"],
  ] as const) {
    await insert(db, "entities", {
      id,
      kind: "project",
      name,
      canonical_id: id,
      created_by: createdBy,
      created_at: stamp(NOW - HOUR),
    });
  }
  for (const [id, kind, run, title] of [
    [PRIVATE_RECORD, "hypothesis", PRIVATE_RUN, `${WITHHELD} hypothesis`],
    [PRIVATE_PROPOSAL, "proposal", PRIVATE_RUN, `${WITHHELD} proposal`],
    [PRIVATE_OBSERVATION, "observation", PRIVATE_RUN, `${WITHHELD} observation`],
    [PUBLIC_RECORD, "finding", PUBLIC_RUN, "Independent finding"],
  ] as const) {
    await insert(db, "records", {
      id,
      kind,
      root_id: id,
      seq: 1,
      run_id: run,
      recipe_id: RECIPE,
      actor_kind: "run",
      actor_id: run,
      title,
      created_at: stamp(NOW - HOUR),
      payload: JSON.stringify({
        schema: 1,
        title,
        statement: title,
        problem: title,
        pattern: title,
        claim: title,
        evidence: [],
      }),
    });
    await insert(db, "filings", {
      id: `fil_${id}`,
      record_id: id,
      entity_id: PUBLIC_TOPIC,
      rationale: "Synthetic filing",
      author_kind: "operator",
      author_id: "synthetic-owner",
      created_at: stamp(NOW - HOUR),
    });
  }
  for (const [id, run, text] of [
    [PRIVATE_QUESTION, PRIVATE_RUN, `${WITHHELD} question`],
    [PUBLIC_QUESTION, "synthetic-owner", "Independent question"],
  ] as const) {
    await insert(db, "questions", {
      id,
      kind: "acquire-context",
      class: "blocking",
      text,
      why: text,
      raised_by_kind: run === PRIVATE_RUN ? "run" : "operator",
      raised_by_id: run,
      payload: "{}",
      created_at: stamp(NOW - HOUR),
    });
  }
  for (const [id, run] of [
    [PRIVATE_RECORD, PRIVATE_RUN],
    [PUBLIC_RECORD, PUBLIC_RUN],
  ] as const) {
    await insert(db, "assessments", {
      id: `asm_${id}`,
      record_id: id,
      revision_id: id,
      run_id: run,
      role: "reception",
      vote: "support",
      payload: JSON.stringify({
        contributions: [{ text: run === PRIVATE_RUN ? WITHHELD : "Independent review" }],
      }),
      recorded_at: stamp(NOW),
    });
    await insert(db, "dispositions", {
      id: `dsp_${id}`,
      record_id: id,
      seq: 1,
      disposition: "reopen",
      actor_id: "synthetic-owner",
      note: id === PRIVATE_RECORD ? WITHHELD : "Independent ruling",
      recorded_at: stamp(NOW),
    });
  }
  await insert(db, "plans", {
    id: "pln_reader_private",
    kind: "topic",
    subject_kind: "proposal",
    subject_id: PRIVATE_PROPOSAL,
    operation: "create",
    payload: JSON.stringify({ name: WITHHELD, reasoning: WITHHELD, records: [PRIVATE_RECORD] }),
    proposed_by_kind: "run",
    proposed_by_id: PRIVATE_RUN,
    created_at: stamp(NOW),
  });
  await insert(db, "feedback", {
    id: "fbk_reader_private",
    record_id: PRIVATE_RECORD,
    actor_id: "synthetic-owner",
    reason: WITHHELD,
    recorded_at: stamp(NOW),
  });
  await insert(db, "policies", {
    version: "pol_reader",
    seq: 1,
    actor_id: "synthetic-owner",
    payload: JSON.stringify({
      recipes: [{ id: RECIPE, title: "Independent lens", body: "Synthetic body" }],
    }),
    recorded_at: stamp(NOW),
  });
  for (const [id, record, run, cost, finished] of [
    ["clm_reader_private_receipt", PRIVATE_OBSERVATION, PRIVATE_RUN, 4, stamp(NOW)],
    ["clm_reader_public_receipt", PUBLIC_RECORD, PUBLIC_RUN, 2, stamp(NOW)],
    ["clm_reader_private_open", PRIVATE_OBSERVATION, null, null, null],
    ["clm_reader_public_open", PUBLIC_RECORD, null, null, null],
    ["clm_reader_unknown", "hyp_000000fe", "run_reader_unknown", 1, null],
    ["clm_reader_null_unknown", "hyp_000000ff", null, 0.5, null],
  ] as const) {
    await insert(db, "claims", {
      id,
      record_id: record,
      run_id: run,
      role: "reception",
      lane: "coverage",
      policy_version: "pol_reader",
      reserved_cost: 0.5,
      actual_cost: cost,
      granted_at: stamp(NOW - HOUR),
      expires_at: stamp(NOW + HOUR),
      finished_at: finished,
    });
  }
}

beforeEach(async () => {
  fixture = await openTestStore(NOW);
  store = openReaderStore(fixture.db, () => NOW);
  await seed();
});
afterEach(() => fixture.close());

async function exclude(touch = true): Promise<void> {
  expect(
    await excludeSessionWhenQuiescent(fixture.db, PRIVATE_SOURCE, "synthetic-owner", stamp(NOW)),
  ).toBe(true);
  if (touch) store.touch();
}

// Hold a real SQLite result while exclusion commits. No sleeps, fake rows or live bindings.
// The selected query is the deterministic barrier and can fire only once, including on retry.
function excludeAfterQuery(
  matches: (sql: string, params: readonly SqlParam[]) => boolean,
  touch = true,
): void {
  const query = fixture.db.query.bind(fixture.db);
  let armed = true;
  fixture.db.query = async <Row extends SqlRow>(sql: string, params: readonly SqlParam[] = []) => {
    const rows = await query<Row>(sql, params);
    if (armed && matches(sql, params)) {
      armed = false;
      await exclude(touch);
    }
    return rows;
  };
}

function noPrivate(result: unknown): void {
  const serialized = JSON.stringify(result);
  for (const value of [
    WITHHELD,
    PRIVATE_SOURCE,
    PRIVATE_RUN,
    PRIVATE_RECORD,
    PRIVATE_PROPOSAL,
    PRIVATE_OBSERVATION,
    PRIVATE_QUESTION,
    PRIVATE_TOPIC,
  ]) expect(serialized).not.toContain(value);
}

async function publicFeed(): Promise<void> {
  const result = await store.feed(QUERY);
  noPrivate(result);
  expect(result.posts.map(({ id }) => id).sort()).toEqual([PUBLIC_RECORD, PUBLIC_QUESTION].sort());
  expect(result.total).toBe(2);
  expect(result.desk).toBe(2);
  const topics = await store.topics();
  noPrivate(topics);
  expect(topics.topics.map(({ id, posts, awaiting }) => ({ id, posts, awaiting }))).toEqual([
    { id: PUBLIC_TOPIC, posts: 1, awaiting: 1 },
  ]);
  expect(topics.proposed).toEqual([]);
  expect(topics.unfiled).toBe(1);
}

test("a ban at the last record projection discards the title, payload and assembled context", async () => {
  excludeAfterQuery(
    (sql, params) => sql.includes("FROM next_actions") && params[0] === PRIVATE_RECORD,
  );
  expect(await store.record(PRIVATE_RECORD)).toBeNull();
  expect((await store.record(PUBLIC_RECORD))?.post.title).toBe("Independent finding");
  await publicFeed();
});

test("the question-peel fallback cannot disclose an indexed question after exclusion completes", async () => {
  excludeAfterQuery(
    (sql, params) => sql.startsWith("SELECT kind, operation,") && params[0] === PRIVATE_QUESTION,
  );
  expect(await store.record(PRIVATE_QUESTION)).toBeNull();
  expect((await store.record(PUBLIC_QUESTION))?.claim.statement).toBe("Independent question");
  await publicFeed();
});

test("a completed exclusion discards an assembled private thread and its count", async () => {
  excludeAfterQuery(
    (sql, params) => sql.includes("FROM dispositions") && params[0] === PRIVATE_RECORD,
  );
  expect(await store.thread(PRIVATE_RECORD)).toEqual({ comments: [], acts: [], total: 0 });
  const independent = await store.thread(PUBLIC_RECORD);
  expect(independent.comments[0]?.text).toBe("Independent review");
  expect(independent.total).toBe(1);
});

for (const touch of [true, false]) {
  test(`an in-flight feed build cannot republish banned posts or topics (${touch ? "touch" : "durable ledger"})`, async () => {
    excludeAfterQuery(
      (sql) => sql.startsWith("SELECT q.id AS id, q.class AS class, q.text AS text"),
      touch,
    );
    const index = await store.index();
    noPrivate(index.posts);
    noPrivate(index.topics);
    expect(index.posts.map(({ post }) => post.id).sort()).toEqual(
      [PUBLIC_RECORD, PUBLIC_QUESTION].sort(),
    );
    expect(index.topics.map(({ id, posts }) => ({ id, posts }))).toEqual([
      { id: PUBLIC_TOPIC, posts: 1 },
    ]);
    expect(index.desk).toBe(2);
    expect(index.unfiled).toBe(1);
    expect([...index.reviewing.keys()].sort()).toEqual(
      [PUBLIC_RECORD, "hyp_000000fe", "hyp_000000ff"].sort(),
    );
    // Normal unchanged reads still reuse the accepted projection, never the stale candidate.
    expect(await store.index()).toBe(index);
    await publicFeed();
    expect(await store.index()).toBe(index);
  });
}

test("a cached index notices an exclusion committed through another store handle without touch", async () => {
  const before = await store.index();
  expect(before.posts.map(({ post }) => post.id)).toContain(PRIVATE_RECORD);
  await exclude(false);
  await publicFeed();
  const after = await store.index();
  expect(after).not.toBe(before);
  expect(await store.index()).toBe(after);
});

test("topic lists discard pre-ban names and counts held across a later plan query", async () => {
  excludeAfterQuery((sql) =>
    sql.startsWith("SELECT p.subject_id AS subject_id, p.operation AS operation, p.payload AS payload"),
  );
  const result = await store.topics();
  noPrivate(result);
  expect(result.topics.map(({ id, posts }) => ({ id, posts }))).toEqual([
    { id: PUBLIC_TOPIC, posts: 1 },
  ]);
  expect(result.proposed).toEqual([]);
});

test("topic peels discard pre-ban feeds and privacy-tainted coverage counts", async () => {
  excludeAfterQuery((sql) => sql.includes("WITH filed AS"));
  const result = await store.topic(PUBLIC_TOPIC);
  noPrivate(result);
  expect(result.feed.posts.map(({ id }) => id)).toEqual([PUBLIC_RECORD]);
  expect(result.feed.total).toBe(1);
  expect(result.topic?.posts).toBe(1);
  expect(result.coverage).toEqual([
    { recipeId: RECIPE, title: "Independent lens", records: 1, runnable: true },
  ]);
});

test("fresh privacy-bearing pulse and coverage projections omit excluded context without falsifying receipt accounting", async () => {
  await exclude();
  const pulse = await store.pulse();
  noPrivate(pulse);
  expect(pulse.today).toEqual({
    sessionsRead: 2,
    records: 1,
    votes: 1,
    proposals: 0,
    topicProposals: 0,
    ruled: 1,
  });
  expect(
    pulse.reviewing
      .map(({ id, title }) => ({ id, title }))
      .sort((a, b) => a.id.localeCompare(b.id)),
  ).toEqual([
    { id: PUBLIC_RECORD, title: "Independent finding" },
    { id: "hyp_000000fe", title: "" },
    { id: "hyp_000000ff", title: "" },
  ]);
  expect(pulse.archive).toEqual({
    unmapped: [{ label: "independent-archive", sessions: 1 }],
    omitted: 0,
  });
  const topic = await store.topic(PUBLIC_TOPIC);
  expect(topic.coverage).toEqual([
    { recipeId: RECIPE, title: "Independent lens", records: 1, runnable: true },
  ]);
  const policy = await store.policy();
  noPrivate(policy);
  expect(policy.spentTodayUsd).toBe(7.5);
  expect(
    policy.recipes.map(({ id, runs, lastRunId, lastRanAt }) => ({ id, runs, lastRunId, lastRanAt })),
  ).toEqual([
    { id: RECIPE, runs: 1, lastRunId: PUBLIC_RUN, lastRanAt: stamp(NOW - HOUR) },
  ]);
  // Read filtering never releases protected unknown work or rewrites genuine historical spend.
  expect(
    await fixture.db.query(
      `SELECT closure, json_extract(payload,'$.posting') AS posting FROM runs
        WHERE id='run_reader_unknown'`,
    ),
  ).toEqual([{ closure: null, posting: 1n }]);
  expect(
    await fixture.db.query(
      `SELECT id,finished_at,actual_cost FROM claims
        WHERE id IN ('clm_reader_unknown','clm_reader_null_unknown') ORDER BY id`,
    ),
  ).toEqual([
    { id: "clm_reader_null_unknown", finished_at: null, actual_cost: 0.5 },
    { id: "clm_reader_unknown", finished_at: null, actual_cost: 1 },
  ]);
  expect(await fixture.db.query("SELECT title FROM records WHERE id = ?", [PRIVATE_RECORD])).toEqual([
    { title: `${WITHHELD} hypothesis` },
  ]);
});

test("pulse assembly retries when exclusion completes after its final catalog-label query", async () => {
  excludeAfterQuery((sql) => sql.includes("COUNT(*) OVER () AS labels"));
  const pulse = await store.pulse();
  noPrivate(pulse);
  expect(pulse.today.records).toBe(1);
  expect(pulse.today.votes).toBe(1);
  expect(pulse.today.sessionsRead).toBe(2);
  expect(pulse.archive).toEqual({
    unmapped: [{ label: "independent-archive", sessions: 1 }],
    omitted: 0,
  });
});

test("policy context assembled before a ban is discarded but historical spend remains authoritative", async () => {
  excludeAfterQuery(
    (sql) => sql.includes("FROM steering") && sql.includes("ORDER BY recorded_at DESC"),
  );
  const policy = await store.policy();
  noPrivate(policy);
  expect(policy.spentTodayUsd).toBe(7.5);
  expect(policy.recipes.map(({ runs, lastRunId }) => ({ runs, lastRunId }))).toEqual([
    { runs: 1, lastRunId: PUBLIC_RUN },
  ]);
});

test("run lists retry both the page and its total when exclusion completes after the page query", async () => {
  excludeAfterQuery((sql) => sql.includes("ORDER BY r.started_at DESC, r.id DESC LIMIT"));
  const result = await store.runs({ limit: 100, offset: 0 });
  noPrivate(result);
  expect(result.total).toBe(2);
  expect(result.runs.map(({ id }) => id).sort()).toEqual([PUBLIC_RUN, "run_reader_unknown"].sort());
});

test("a run receipt held at the database boundary cannot be disclosed after exclusion", async () => {
  excludeAfterQuery(
    (sql, params) => sql.includes("r.payload AS payload") && params[0] === PRIVATE_RUN,
  );
  expect(await store.run(PRIVATE_RUN)).toEqual({ run: null, receipt: null });
  expect((await store.run(PUBLIC_RUN)).receipt).toEqual({ summary: "Independent receipt" });
});

test("neighborhood assembly discards private graph rows held across the summary selection query", async () => {
  excludeAfterQuery((sql) => sql.includes("SELECT p.id plan_id,v.id version_id"));
  const result = await store.neighborhood(
    NeighborhoodQuerySchema.parse({ entityId: PUBLIC_TOPIC }),
  );
  noPrivate(result);
  expect(result.records.map(({ id }) => id)).toEqual([PUBLIC_RECORD]);
});

test("private corrective evidence cannot renew an independent post's attention after a ban", async () => {
  const id = "hyp_000000a5";
  await insert(fixture.db, "records", {
    id,
    kind: "hypothesis",
    root_id: id,
    seq: 1,
    run_id: PUBLIC_RUN,
    actor_kind: "run",
    actor_id: PUBLIC_RUN,
    title: "Independent attention target",
    created_at: stamp(NOW - 3 * HOUR),
    payload: "{}",
  });
  for (const [edge, from, fromKind, to, toKind, kind, at] of [
    ["edg_reader_public_citation", id, "hypothesis", PUBLIC_SOURCE, "session", "cites", NOW - 2 * HOUR],
    [
      "edg_reader_private_citation",
      PRIVATE_OBSERVATION,
      "observation",
      PRIVATE_SOURCE,
      "session",
      "cites",
      NOW - HOUR / 2,
    ],
    [
      "edg_reader_private_correction",
      PRIVATE_OBSERVATION,
      "observation",
      id,
      "hypothesis",
      "corrects",
      NOW - HOUR / 2,
    ],
  ] as const) {
    await insert(fixture.db, "edges", {
      id: edge,
      kind,
      from_id: from,
      from_kind: fromKind,
      to_id: to,
      to_kind: toKind,
      actor_kind: "operator",
      actor_id: "synthetic-owner",
      created_at: stamp(at),
    });
  }
  const before = await store.record(id);
  expect(before?.post.attention).toEqual({
    at: new Date(NOW - HOUR / 2).toISOString(),
    basis: "evidence",
  });
  await exclude();
  const after = await store.record(id);
  noPrivate(after);
  expect(after?.post.attention).toEqual({
    at: new Date(NOW - 2 * HOUR).toISOString(),
    basis: "evidence",
  });
  expect(after?.post.title).toBe("Independent attention target");
  const index = await store.index();
  expect(index.attention.records.has(PRIVATE_RECORD)).toBe(false);
  expect(index.attention.questions.has(PRIVATE_QUESTION)).toBe(false);
});
