/*
  The read model against a real plugin database.

  Everything here is driven through `openStore` over the engine's own SQLite file with
  `SCHEMA_V1` applied, because the properties that matter are about the assembly rather than the
  arithmetic: what a topic is, what a post is, what awaits the operator, what "contested" means.
  A fake handed hand-shaped rows would assert the fake.
*/

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { insert, openTestStore, type TestStore } from "./testdb.ts";
import { stamp } from "./feedindex.ts";

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;
/** Noon UTC, so "today" is half over and yesterday is unambiguously outside it. */
const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const MIDNIGHT = Date.UTC(2026, 8, 12, 0, 0, 0);

const REPOSITORY = "ent_00000001";
const PROJECT = "ent_00000002";
const MERGED = "ent_00000003";
const RETIRED = "ent_00000004";

const CANDIDATE = "hyp_00000001";
const FINDING = "fnd_00000002";
const ARGUED = "pro_00000003";
const AGREED = "pro_00000004";
const OBSERVATION = "obs_00000005";
const UNDER_REVIEW = "hyp_00000006";
const BLOCKING = "qst_00000001";
const ANSWERED = "qst_00000002";

const SESSION = "dev-01/omp/2026-09-01T10-00-00Z_abc";
const SOURCE = "-code/2026-09-01T10-00-00Z_abc";
const CITED_PATH = `/home/alex/.omp/agent/sessions/${SOURCE}.jsonl`;

let harness: TestStore;

/**
 * One deployment, written as the store's own rows.
 *
 * The fixture carries one of everything the feed has an opinion about: a record nobody ruled on,
 * one whose ruling was lifted, one already decided, one argued over inside a role, one agreed
 * about across two roles, an observation that is evidence rather than a post, a record under an
 * open claim, a question that blocks a run and one that is finished.
 */
async function seed(store: TestStore): Promise<void> {
  const { db } = store;

  await insert(db, "entities", {
    id: REPOSITORY,
    kind: "repository",
    name: "tyrode-infra",
    canonical_id: REPOSITORY,
    created_by: "operator",
    created_at: stamp(NOW - 30 * DAY),
  });
  await insert(db, "entities", {
    id: PROJECT,
    kind: "project",
    name: "babel",
    canonical_id: PROJECT,
    created_by: "operator",
    created_at: stamp(NOW - 30 * DAY),
  });
  // Merged away: the entity that speaks for it is already in the list.
  await insert(db, "entities", {
    id: MERGED,
    kind: "repository",
    name: "tyrode-infra-old",
    canonical_id: REPOSITORY,
    created_by: "operator",
    created_at: stamp(NOW - 30 * DAY),
  });
  // Retired: retiring re-queues its filings, so it is no longer a place records live.
  await insert(db, "entities", {
    id: RETIRED,
    kind: "project",
    name: "an abandoned thing",
    canonical_id: RETIRED,
    created_by: "operator",
    created_at: stamp(NOW - 30 * DAY),
  });
  const fact = (id: string, entity: string, predicate: string, value: string, note: string) =>
    insert(db, "facts", {
      id,
      entity_id: entity,
      predicate,
      value,
      valid_from: stamp(NOW - 30 * DAY),
      observed_at: stamp(NOW - 30 * DAY),
      authority_kind: "operator",
      authority_id: "operator",
      note,
      recorded_at: stamp(NOW - 30 * DAY),
    });
  await fact("fct_0001", REPOSITORY, "lifecycle", "active", "this is what I am on");
  await fact("fct_0002", REPOSITORY, "repository-remote", "github.com/tyrode/tyrode-infra", "");
  await fact("fct_0003", REPOSITORY, "local-path", "/home/alex/infra", "");
  await fact("fct_0004", RETIRED, "lifecycle", "retired", "finished with it");

  const record = (
    id: string,
    kind: string,
    title: string,
    createdAt: number,
    runId: string,
    payload: Record<string, unknown>,
    parentId: string | null = null,
  ) =>
    insert(db, "records", {
      id,
      kind,
      root_id: id,
      seq: 1,
      parent_id: parentId,
      run_id: runId,
      recipe_id: "outcome-integrity",
      recipe_version: 3,
      actor_kind: "run",
      actor_id: runId,
      title,
      created_at: stamp(createdAt),
      payload: JSON.stringify(payload),
    });

  await record(CANDIDATE, "hypothesis", "a candidate nobody has ruled on", NOW - 3 * DAY, "run-a", {
    schema: 1,
    statement: "the agent adjusts tests to the code rather than the code to the tests",
  });
  await record(FINDING, "finding", "a finding whose ruling was lifted", NOW - 2 * DAY, "run-a", {
    schema: 1,
    title: "a finding whose ruling was lifted",
    pattern: "it recurs across checkouts",
    significance: "it costs a review every time",
    scope: ["infra", "babel"],
  });
  await record(ARGUED, "proposal", "a proposal Babel argued over", NOW - DAY, "run-b", {
    schema: 1,
    title: "a proposal Babel argued over",
    problem: "the guard runs on every push and nobody reads it",
    outcome: "run the guard on the merge queue instead",
    impact: "high",
    estimated_scope: "one workflow file",
    classification: "internal",
    uncertainty: "the queue may not carry the same context",
    risks: ["a slower queue"],
    open_questions: ["does the queue see forks"],
    prerequisites: ["the queue is enabled"],
    verification_criteria: ["the guard no longer runs on push"],
    targets: [
      { system: "tyrode-infra", confidence: "likely", rationale: "the workflow lives there" },
    ],
    supporting: [
      {
        locator: { path: CITED_PATH, line: 2073, byte_offset: 7300451, digest: "a2d7" },
        note: "the operator says so in his own words",
      },
    ],
    conflicting: [
      {
        locator: { path: "/nowhere/unheld.jsonl", line: 4, byte_offset: 9, digest: "ffff" },
        note: "one run disagreed",
      },
    ],
  });
  await record(
    AGREED,
    "proposal",
    "a proposal two roles answered differently",
    NOW - 6 * HOUR,
    "run-b",
    {
      schema: 1,
      title: "a proposal two roles answered differently",
      problem: "p",
      outcome: "o",
    },
  );
  await record(
    OBSERVATION,
    "observation",
    "an observation, which is evidence",
    NOW - 5 * HOUR,
    "run-a",
    {
      schema: 1,
      claim: "an observation, which is evidence",
      category: "process",
      confidence: "stated",
      impact: "medium",
      evidence: [
        {
          locator: { path: CITED_PATH, line: 2093, byte_offset: 7321776, digest: "5b38" },
          note: "what the человек said",
        },
      ],
    },
    CANDIDATE,
  );
  await record(
    UNDER_REVIEW,
    "hypothesis",
    "a candidate under review right now",
    NOW - 4 * HOUR,
    "run-c",
    {
      schema: 1,
      statement: "a candidate under review right now",
    },
  );

  await insert(db, "sessions", {
    selector: SESSION,
    host: "dev-01",
    harness: "omp",
    source_id: SOURCE,
    title: "the conversation it came from",
    seen_at: stamp(NOW - 10 * DAY),
  });
  await insert(db, "edges", {
    id: "edg_0001",
    kind: "cites",
    from_kind: "observation",
    from_id: OBSERVATION,
    to_kind: "session",
    to_id: SESSION,
    position: 0,
    note: "what the operator said",
    actor_kind: "run",
    actor_id: "run-a",
    created_at: stamp(NOW - 5 * HOUR),
  });
  await insert(db, "edges", {
    id: "edg_0002",
    kind: "consolidates",
    from_kind: "finding",
    from_id: FINDING,
    to_kind: "observation",
    to_id: OBSERVATION,
    position: 0,
    note: null,
    actor_kind: "run",
    actor_id: "run-a",
    created_at: stamp(NOW - 2 * DAY),
  });

  // Filings: the candidate and the finding are about the repository, the argued proposal about
  // the project, and one filing was withdrawn so the record reads unfiled again.
  const filing = (id: string, recordId: string, entity: string, withdrawn: number, at: number) =>
    insert(db, "filings", {
      id,
      record_id: recordId,
      entity_id: entity,
      rationale: "it is about this",
      author_kind: "operator",
      author_id: "operator",
      heuristic: 0,
      withdrawn,
      created_at: stamp(at),
    });
  await filing("fil_0001", CANDIDATE, REPOSITORY, 0, NOW - 3 * DAY);
  await filing("fil_0002", FINDING, REPOSITORY, 0, NOW - 2 * DAY);
  await filing("fil_0003", ARGUED, PROJECT, 0, NOW - DAY);
  await filing("fil_0004", AGREED, PROJECT, 0, NOW - 6 * HOUR);
  await filing("fil_0005", AGREED, PROJECT, 1, NOW - 5 * HOUR);
  // A filing under a retired entity is not a topic anybody can open.
  await filing("fil_0006", UNDER_REVIEW, RETIRED, 0, NOW - 4 * HOUR);

  // Rulings: the finding was accepted and then reopened today; the argued proposal was accepted
  // yesterday and stays decided.
  await insert(db, "dispositions", {
    id: "dsp_0001",
    record_id: FINDING,
    seq: 1,
    disposition: "accept",
    note: "worth keeping",
    actor_id: "operator",
    recorded_at: stamp(NOW - 2 * DAY),
  });
  await insert(db, "dispositions", {
    id: "dsp_0002",
    record_id: FINDING,
    seq: 2,
    disposition: "reopen",
    note: "on reflection",
    actor_id: "operator",
    recorded_at: stamp(MIDNIGHT + 9 * HOUR),
  });
  await insert(db, "dispositions", {
    id: "dsp_0003",
    record_id: ARGUED,
    seq: 1,
    disposition: "accept",
    note: "do it",
    actor_id: "operator",
    recorded_at: stamp(NOW - 20 * HOUR),
  });

  const assessment = (
    id: string,
    recordId: string,
    runId: string,
    role: string,
    vote: string | null,
    at: number,
    contributions: { kind: string; text: string }[] = [],
    supersedes: string | null = null,
  ) =>
    insert(db, "assessments", {
      id,
      record_id: recordId,
      revision_id: recordId,
      run_id: runId,
      role,
      vote,
      lane: role === "filing" ? "filing" : "coverage",
      supersedes_id: supersedes,
      payload: JSON.stringify({ vote, contributions }),
      recorded_at: stamp(at),
    });
  // Inside one role, both sides: this is the disagreement a listing can render.
  await assessment("asm_0001", ARGUED, "run-x", "reception", "support", NOW - 20 * HOUR, [
    { kind: "note", text: "the queue is the right place" },
  ]);
  await assessment("asm_0002", ARGUED, "run-y", "reception", "oppose", NOW - 19 * HOUR, [
    { kind: "note", text: "the queue does not see forks" },
  ]);
  await assessment("asm_0003", ARGUED, "run-z", "evidence", "support", NOW - 18 * HOUR);
  // Across two roles, both sides: two reviewers agreeing about different things.
  await assessment("asm_0004", AGREED, "run-x", "reception", "support", MIDNIGHT + 7 * HOUR);
  await assessment("asm_0005", AGREED, "run-y", "evidence", "oppose", MIDNIGHT + 7 * HOUR);
  // A correction supersedes the statement it names and counts once, as the newer vote.
  await assessment("asm_0006", CANDIDATE, "run-x", "reception", "oppose", NOW - 2 * DAY);
  await assessment(
    "asm_0007",
    CANDIDATE,
    "run-x",
    "reception",
    "support",
    NOW - 2 * DAY + HOUR,
    [],
    "asm_0006",
  );

  await insert(db, "feedback", {
    id: "fbk_0001",
    record_id: ARGUED,
    actor_id: "operator",
    stance: "agree",
    reason: "this is the one I want",
    question: 0,
    recorded_at: stamp(NOW - 17 * HOUR),
  });
  await insert(db, "feedback", {
    id: "fbk_0002",
    record_id: ARGUED,
    actor_id: "operator",
    stance: null,
    reason: "does this cover forks?",
    question: 1,
    related_id: "fbk_0001",
    recorded_at: stamp(NOW - 16 * HOUR),
  });
  // A bare stance is neither reception nor something said, and folds into nothing.
  await insert(db, "feedback", {
    id: "fbk_0003",
    record_id: AGREED,
    actor_id: "operator",
    stance: "unsure",
    reason: "",
    question: 0,
    recorded_at: stamp(NOW - 3 * HOUR),
  });

  const claim = (
    id: string,
    recordId: string,
    granted: number,
    expires: number,
    finished: string | null,
    cost: number | null,
  ) =>
    insert(db, "claims", {
      id,
      record_id: recordId,
      role: "reception",
      lane: "coverage",
      policy_version: "pol-3",
      run_id: "run-c",
      fence: 1,
      reserved_cost: 0.1,
      actual_cost: cost,
      granted_at: stamp(granted),
      expires_at: stamp(expires),
      finished_at: finished,
      outcome: finished === null ? null : "recorded",
    });
  await claim("clm_0001", UNDER_REVIEW, NOW - 3 * HOUR, NOW + HOUR, null, null);
  // An expired lease is not open: the next claimer may take it at any instant.
  await claim("clm_0002", FINDING, NOW - 5 * HOUR, NOW - HOUR, null, null);
  // A finished claim is work already said rather than work in flight.
  await claim("clm_0003", ARGUED, NOW - 22 * HOUR, NOW - 20 * HOUR, stamp(NOW - 21 * HOUR), 0.42);
  // Settled today, so its cost is what the day has spent against the ceiling.
  await claim(
    "clm_0005",
    AGREED,
    MIDNIGHT + 6 * HOUR,
    MIDNIGHT + 8 * HOUR,
    stamp(MIDNIGHT + 7 * HOUR),
    0.25,
  );

  await insert(db, "questions", {
    id: BLOCKING,
    kind: "clarification",
    class: "blocking",
    text: "which checkout is the one you actually work in?",
    why: "two remotes answer to one name",
    raised_by_kind: "run",
    raised_by_id: "run-b",
    payload: "{}",
    created_at: stamp(NOW - 2 * HOUR),
  });
  await insert(db, "questions", {
    id: ANSWERED,
    kind: "clarification",
    class: "curiosity",
    text: "is the old mirror still read?",
    why: "it has not moved in a year",
    raised_by_kind: "run",
    raised_by_id: "run-b",
    payload: "{}",
    created_at: stamp(NOW - 8 * DAY),
  });
  await insert(db, "question_events", {
    id: "qev_0001",
    question_id: ANSWERED,
    seq: 1,
    state: "answered",
    actor_id: "operator",
    recorded_at: stamp(NOW - 7 * DAY),
  });
  await insert(db, "answers", {
    id: "ans_0001",
    question_id: ANSWERED,
    actor_id: "operator",
    outcome: "answered",
    text: "no, it has been dead since the migration",
    recorded_at: stamp(NOW - 7 * DAY),
  });

  await insert(db, "plans", {
    id: "pln_0001",
    kind: "topic",
    subject_kind: "proposal",
    subject_id: AGREED,
    operation: "create",
    payload: JSON.stringify({
      reasoning: "32 sessions in 3 checkouts cite it",
      name: "the queue",
      entityKind: "project",
      targets: [PROJECT],
      records: [{ id: ARGUED }, { id: "pro_elsewhere" }],
    }),
    proposed_by_kind: "run",
    proposed_by_id: "run-b",
    state: "open",
    created_at: stamp(NOW - 6 * HOUR),
  });

  await insert(db, "policies", {
    version: "pol-3",
    seq: 3,
    actor_id: "operator",
    reason: "raise the ceiling",
    payload: JSON.stringify({
      per_cycle_cost: 1.5,
      daily_cost: 12,
      batch_size: 4,
      coverage_share: 0.5,
      exploration_share: 0.3,
      filing_share: 0.2,
      // Two lenses in force and only one of them has ever been performed: the fixture carries
      // the deployment #344 is about, where the roster read off `runs` alone showed one row.
      recipes: [
        {
          id: "outcome-integrity",
          title: "Outcome integrity",
          looksFor: "claims that do not match what happened",
          enabled: true,
        },
        {
          id: "security-boundaries",
          title: "Security, privacy, and trust boundaries",
          looksFor: "authority crossing a boundary nobody drew",
          enabled: false,
        },
      ],
    }),
    recorded_at: stamp(NOW - 4 * DAY),
  });

  const run = (id: string, started: number, finished: string | null, selection: unknown[]) =>
    insert(db, "runs", {
      id,
      kind: "explore",
      machine_id: "dev-01",
      job_id: `job-${id}`,
      recipe_id: "outcome-integrity",
      preparation: JSON.stringify({ selection }),
      started_at: stamp(started),
      finished_at: finished,
      closure: finished === null ? null : "completed",
      cost_usd: 0.31,
      tokens: 40_000,
      records: 4,
      payload: JSON.stringify({ runId: id, counts: {} }),
    });
  await run("run-a", NOW - 3 * DAY, stamp(NOW - 3 * DAY + HOUR), [
    { host: "dev-01", harness: "omp", sourceId: "yesterday-only" },
  ]);
  await run("run-b", MIDNIGHT + 6 * HOUR, stamp(MIDNIGHT + 7 * HOUR), [
    { host: "dev-01", harness: "omp", sourceId: SOURCE },
    { host: "dev-01", harness: "omp", sourceId: SOURCE },
    { host: "dev-01", harness: "codex", sourceId: "another-one" },
  ]);
  // Three minutes since its last word: past the first threshold and nowhere near the second.
  await run("run-c", NOW - 3 * 60_000, null, []);
  // Thirty seconds: a run the hub heard from a moment ago.
  await run("run-d", NOW - 30_000, null, []);
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  await seed(harness);
});

afterEach(() => {
  harness.close();
});

const feed = async (query: Partial<Parameters<TestStore["store"]["feed"]>[0]> = {}) =>
  await harness.store.feed({
    sort: "next",
    window: "day",
    kinds: [],
    surface: "desk",
    established: [],
    group: "none",
    limit: 25,
    offset: 0,
    ...query,
  });

describe("the feed", () => {
  test("is every kind of record that is a post, and never an observation", async () => {
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const kinds = new Set(all.posts.map((post) => post.kind));
    expect([...kinds].sort()).toEqual(["finding", "hypothesis", "proposal", "question"]);
    expect(all.posts.map((post) => post.id)).not.toContain(OBSERVATION);
    // Five records that are posts, two questions; the observation is evidence and is neither.
    expect(all.total).toBe(7);
  });

  // The desk is the default, and it is a narrowing of the one order rather than a second list:
  // a record the operator has already decided is not on it, an unanswered question is, and a
  // candidate Babel is still developing on its own is not — it is a question Babel asked itself.
  test("the desk is what awaits his ruling, and a candidate is not on it", async () => {
    const desk = await feed({ window: "all", limit: 100 });
    const ids = desk.posts.map((post) => post.id);
    expect(ids).toContain(BLOCKING);
    // Accepted yesterday: a ruling that was made, and a deferral would be one too.
    expect(ids).not.toContain(ARGUED);
    // Answered: §4.8's finished states await nobody.
    expect(ids).not.toContain(ANSWERED);
    // Reopened is undecided again, and is a different wait from one nobody ever ruled on.
    expect(ids).toContain(FINDING);
    // Unruled, and still a candidate: it awaits him in the sense that nobody has ruled, and it
    // is not addressed to him, which is the whole of the routing.
    expect(ids).not.toContain(CANDIDATE);
    const reopened = desk.posts.find((post) => post.id === FINDING);
    expect(reopened?.standing).toBe("reopened");
    for (const post of desk.posts) expect(post.awaiting).toBe(true);
  });

  // Nothing is deleted by routing: every post the one list held is on exactly one of the three
  // surfaces, and the shelf is where the volume goes.
  test("the three surfaces partition the corpus, and the shelf is asked for", async () => {
    const whole = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const desk = await feed({ sort: "new", window: "all", surface: "desk", limit: 100 });
    const queue = await feed({ sort: "new", window: "all", surface: "queue", limit: 100 });
    const shelf = await feed({ sort: "new", window: "all", surface: "shelf", limit: 100 });
    expect(desk.total + queue.total + shelf.total).toBe(whole.total);
    // Accepted: what remains is the work, and no ruling is waiting.
    expect(queue.posts.map((post) => post.id)).toEqual([ARGUED]);
    // The candidate nobody ruled on and the finished question are kept rather than shown.
    const shelved = shelf.posts.map((post) => post.id);
    expect(shelved).toContain(CANDIDATE);
    expect(shelved).toContain(ANSWERED);
    const unruled = shelf.posts.find((post) => post.id === CANDIDATE);
    expect(unruled?.standing).toBe("new");
    // Never unprompted, still reachable: the same narrowings work over the shelf.
    const byTopic = await feed({
      sort: "new",
      window: "all",
      surface: "shelf",
      topic: "tyrode-infra",
      limit: 100,
    });
    expect(byTopic.posts.map((post) => post.id)).toEqual([CANDIDATE]);
  });

  // "Is this a plausible amount of work" is a question he has while reading the shelf, so the
  // desk's size travels on every answer rather than only on the desk's own.
  test("the desk's size is reported whatever surface was asked for", async () => {
    const desk = await feed({ window: "all", limit: 100 });
    const shelf = await feed({ sort: "new", window: "all", surface: "shelf", limit: 100 });
    expect(desk.desk).toBe(desk.total);
    expect(shelf.desk).toBe(desk.total);
    // A narrowing of the desk narrows the page and not the desk.
    const narrowed = await feed({ window: "all", kinds: ["question"], limit: 100 });
    expect(narrowed.total).toBeLessThan(narrowed.desk);
  });

  test("a retired shelf sort resolves to all-time reception before filtering and grouped pagination", async () => {
    const page = { surface: "shelf", group: "recipe", offset: 1, limit: 1 } as const;
    const allTime = await feed({ ...page, sort: "top", window: "all" });
    const legacy = await feed({ ...page, sort: "rising", window: "hour" });
    expect(legacy).toEqual(allTime);
  });

  // Next is a complete order over the corpus and not a filter wearing a sort's name: turning the
  // queue off keeps the same rows on top and puts the rest underneath.
  test("next puts the blocking question first and what awaits nothing last", async () => {
    const all = await feed({ surface: "all", window: "all", limit: 100 });
    expect(all.posts[0]?.id).toBe(BLOCKING);
    const awaiting = all.posts.filter((post) => post.awaiting).length;
    for (let at = 0; at < awaiting; at++) expect(all.posts[at]?.awaiting).toBe(true);
    expect(all.posts[all.posts.length - 1]?.awaiting).toBe(false);
  });

  test("the score is the reviewers' and the operator's prose is a comment", async () => {
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const argued = all.posts.find((post) => post.id === ARGUED);
    expect(argued?.support).toBe(2);
    expect(argued?.oppose).toBe(1);
    expect(argued?.score).toBe(1);
    // Two comments: his statement and his question. His stance is in no column.
    expect(argued?.comments).toBe(4);
    expect(argued?.votes).toEqual([
      { role: "evidence", vote: "support" },
      { role: "reception", vote: "oppose" },
      { role: "reception", vote: "support" },
    ]);
  });

  // A correction supersedes the statement it names: one vote per run per role, newest wins.
  test("a superseded assessment is dropped rather than counted twice", async () => {
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const candidate = all.posts.find((post) => post.id === CANDIDATE);
    expect(candidate?.support).toBe(1);
    expect(candidate?.oppose).toBe(0);
  });

  // A re-granted review inside one instant is a CHANGED vote and not a second one, and the
  // instants cannot tell the two rows apart — only the order they were written in can.
  test("one run voting twice in one instant under one role counts once, as the later vote", async () => {
    const at = stamp(NOW - 10 * 60_000);
    // The identifiers deliberately sort against the write order: ordering on the id would end
    // on the support, which is the earlier of the two.
    for (const [id, vote] of [
      ["asm_1z01", "support"],
      ["asm_1a02", "oppose"],
    ] as const) {
      await insert(harness.db, "assessments", {
        id,
        record_id: AGREED,
        revision_id: AGREED,
        run_id: "run-w",
        role: "challenge",
        vote,
        lane: "coverage",
        payload: JSON.stringify({ vote, contributions: [] }),
        recorded_at: at,
      });
    }
    harness.store.touch();
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const agreed = all.posts.find((post) => post.id === AGREED);
    expect(agreed?.support).toBe(1);
    expect(agreed?.oppose).toBe(2);
    expect(agreed?.votes).toContainEqual({ role: "challenge", vote: "oppose" });
    expect(agreed?.votes).not.toContainEqual({ role: "challenge", vote: "support" });
  });

  test("contested is disagreement inside one role and never across two", async () => {
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    expect(all.posts.find((post) => post.id === ARGUED)?.contested).toBe(true);
    // Support on whether it matters beside opposition on whether the evidence holds is two
    // reviewers agreeing about different things.
    const agreed = all.posts.find((post) => post.id === AGREED);
    expect(agreed?.support).toBe(1);
    expect(agreed?.oppose).toBe(1);
    expect(agreed?.contested).toBe(false);
  });

  // The order that exists to find the argument reads it the same way the row's badge does:
  // inside one role. ARGUED is two runs contradicting each other on whether the proposal is
  // wanted; AGREED is one reviewer on whether it matters and another on whether the evidence
  // holds, which is two answers to two questions. Summed into a support and an oppose column
  // AGREED is the perfectly balanced one and leads the list, and the page then labels the row
  // it led with `reviewed` — the list saying one thing and its own rows another.
  test("controversial is the records split inside one role, and only those", async () => {
    const argued = await feed({ sort: "controversial", window: "all", surface: "all", limit: 100 });
    const ids = argued.posts.map((post) => post.id);
    expect(ids).toContain(ARGUED);
    expect(ids).not.toContain(AGREED);
    // A list of the whole corpus ordered by a number that is zero for most of it is a list of
    // the whole corpus, so the count is the answer to "how much of this is contested".
    expect(argued.total).toBe(1);
    expect(argued.posts[0]?.contested).toBe(true);
    // The same query under any other order carries both, so the narrowing is this sort's and
    // not a filter the feed grew.
    const whole = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    expect(whole.posts.map((post) => post.id)).toContain(AGREED);
  });

  test("reviewing flips with an open claim and not with a finished or lapsed one", async () => {
    const all = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    expect(all.posts.find((post) => post.id === UNDER_REVIEW)?.reviewing).toBe(true);
    expect(all.posts.find((post) => post.id === FINDING)?.reviewing).toBe(false);
    expect(all.posts.find((post) => post.id === ARGUED)?.reviewing).toBe(false);
  });

  // The index is rebuilt on demand and `touch` is the whole of its invalidation: an act the
  // operator has just performed must not wait out a minute of staleness.
  test("touch is what makes a just-recorded claim visible", async () => {
    expect(
      (await feed({ sort: "new", window: "all", surface: "all", limit: 100 })).posts.find(
        (post) => post.id === ARGUED,
      )?.reviewing,
    ).toBe(false);
    await insert(harness.db, "claims", {
      id: "clm_0004",
      record_id: ARGUED,
      role: "evidence",
      lane: "coverage",
      policy_version: "pol-3",
      run_id: "run-c",
      fence: 1,
      reserved_cost: 0.1,
      granted_at: stamp(NOW),
      expires_at: stamp(NOW + HOUR),
    });
    expect(
      (await feed({ sort: "new", window: "all", surface: "all", limit: 100 })).posts.find(
        (post) => post.id === ARGUED,
      )?.reviewing,
    ).toBe(false);
    harness.store.touch();
    expect(
      (await feed({ sort: "new", window: "all", surface: "all", limit: 100 })).posts.find(
        (post) => post.id === ARGUED,
      )?.reviewing,
    ).toBe(true);
  });

  // The window applies to top and controversial and to nothing else: "hot" over a day and "hot"
  // over all time would be the same list with the older half deleted.
  test("the window narrows top and leaves hot alone", async () => {
    const topDay = await feed({ sort: "top", window: "day", surface: "all", limit: 100 });
    const topAll = await feed({ sort: "top", window: "all", surface: "all", limit: 100 });
    expect(topDay.posts.map((post) => post.id)).not.toContain(CANDIDATE);
    expect(topAll.posts.map((post) => post.id)).toContain(CANDIDATE);
    const hotDay = await feed({ sort: "hot", window: "day", surface: "all", limit: 100 });
    expect(hotDay.total).toBe(topAll.total);
  });

  // §8.5: the ranking is over the whole deployment, before paging. A page ranked independently
  // would make "new" mean "newest among the rows this request happened to read".
  test("ranks the whole deployment before it pages", async () => {
    const whole = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    const first = await feed({ sort: "new", window: "all", surface: "all", limit: 2, offset: 0 });
    const second = await feed({ sort: "new", window: "all", surface: "all", limit: 2, offset: 2 });
    expect(first.total).toBe(whole.total);
    expect(second.total).toBe(whole.total);
    expect([...first.posts, ...second.posts].map((post) => post.id)).toEqual(
      whole.posts.slice(0, 4).map((post) => post.id),
    );
  });

  test("a topic narrows by name and by id, and `unfiled` is the records under nothing", async () => {
    const byName = await feed({ surface: "all", window: "all", topic: "tyrode-infra", limit: 100 });
    expect(byName.posts.map((post) => post.id).sort()).toEqual([CANDIDATE, FINDING].sort());
    const byId = await feed({ surface: "all", window: "all", topic: REPOSITORY, limit: 100 });
    expect(byId.posts.map((post) => post.id).sort()).toEqual([CANDIDATE, FINDING].sort());
    const unfiled = await feed({ surface: "all", window: "all", topic: "unfiled", limit: 100 });
    const ids = unfiled.posts.map((post) => post.id);
    // Withdrawn, so it is about nothing again; filed under a retired entity, so likewise.
    expect(ids).toContain(AGREED);
    expect(ids).toContain(UNDER_REVIEW);
    // An observation is never a row here, filed or not.
    expect(ids).not.toContain(OBSERVATION);
  });

  // The two axes are what a reader separates "important and shaky" from "trivial and certain"
  // with, so each has to narrow without the other: the subject is the filing, the status is the
  // fold of the standing and the reception, and asking for both is the intersection.
  test("the subject and the status narrow independently and compose", async () => {
    // A reopened finding its reviewers are split on inside one role: undecided and shaky.
    for (const [id, runId, vote] of [
      ["asm_0101", "run-p", "support"],
      ["asm_0102", "run-q", "oppose"],
    ] as const) {
      await insert(harness.db, "assessments", {
        id,
        record_id: FINDING,
        revision_id: FINDING,
        run_id: runId,
        role: "reception",
        vote,
        lane: "coverage",
        payload: JSON.stringify({ vote, contributions: [] }),
        recorded_at: stamp(NOW - HOUR),
      });
    }
    harness.store.touch();

    const shaky = await feed({ surface: "all", window: "all", established: ["contested"] });
    expect(shaky.posts.map((post) => post.id)).toEqual([FINDING]);
    // Ruled and split is settled, not shaky: Babel votes and the operator rules.
    const settled = await feed({ surface: "all", window: "all", established: ["settled"] });
    expect(settled.posts.map((post) => post.id)).toContain(ARGUED);
    expect(settled.posts.map((post) => post.id)).not.toContain(FINDING);

    // One subject, both statuses; one status, one of the two subjects. Neither axis is the
    // other, which is the whole of the issue.
    const subject = await feed({ surface: "all", window: "all", topic: "tyrode-infra" });
    expect(subject.posts.map((post) => post.id).sort()).toEqual([CANDIDATE, FINDING].sort());
    const both = await feed({
      surface: "all",
      window: "all",
      topic: "tyrode-infra",
      established: ["contested"],
    });
    expect(both.posts.map((post) => post.id)).toEqual([FINDING]);
    const elsewhere = await feed({
      surface: "all",
      window: "all",
      topic: "babel",
      established: ["contested"],
    });
    expect(elsewhere.posts).toEqual([]);
  });

  // A concept observed many times took a slot each time, so the page is cut out of the GROUPS:
  // the two records filed under one topic share one, and the page's unit is what `total` counts.
  test("grouping gives a concept one slot and says what holds it together", async () => {
    const flat = await feed({ sort: "new", surface: "all", window: "all", limit: 100 });
    const byTopic = await feed({
      sort: "new",
      surface: "all",
      window: "all",
      group: "topic",
      limit: 100,
    });
    // Same records, fewer slots: seven posts, six groups, because two share a topic.
    expect(byTopic.posts).toHaveLength(flat.posts.length);
    expect(byTopic.total).toBe(flat.total - 1);

    const filed = byTopic.groups.find((group) => group.key === REPOSITORY);
    expect(filed).toEqual({
      key: REPOSITORY,
      keyKind: "topic",
      // The key is stated, so a reader knows why these two are together.
      label: "t/tyrode-infra",
      records: 2,
      posts: expect.arrayContaining([CANDIDATE, FINDING]),
    });

    // A record no topic files is not hidden by the grouping: it keeps a slot of its own.
    const alone = byTopic.groups.find((group) => group.posts.includes(AGREED));
    expect(alone).toEqual({ key: "", keyKind: "none", label: "", records: 1, posts: [AGREED] });

    // Asking for no grouping is the list it always was.
    expect(flat.groups).toEqual([]);
  });

  // The recipe is the other existing key, and a group bigger than the page says how much of
  // itself it is carrying rather than looking complete.
  test("grouping by lens states the group's true size", async () => {
    await insert(harness.db, "records", {
      id: "hyp_00000007",
      kind: "hypothesis",
      root_id: "hyp_00000007",
      seq: 1,
      run_id: "run-a",
      recipe_id: "outcome-integrity",
      recipe_version: 3,
      actor_kind: "run",
      actor_id: "run-a",
      title: "a sixth record under the same lens",
      created_at: stamp(NOW - HOUR),
      payload: JSON.stringify({ schema: 1, statement: "one more" }),
    });
    harness.store.touch();
    const byLens = await feed({
      sort: "new",
      surface: "all",
      window: "all",
      group: "recipe",
      limit: 100,
    });
    const lens = byLens.groups.find((group) => group.key === "outcome-integrity");
    expect(lens?.keyKind).toBe("recipe");
    expect(lens?.records).toBe(6);
    // Five travel with the page; the sixth is counted rather than silently dropped.
    expect(lens?.posts).toHaveLength(5);
    // A question no recipe produced is its own slot beside the lens, not inside it.
    const asked = byLens.groups.find((group) => group.posts.includes(BLOCKING));
    expect(asked?.keyKind).toBe("none");
  });
});

describe("topics", () => {
  test("counts posts and what awaits from the live filings", async () => {
    const answer = await harness.store.topics();
    const repository = answer.topics.find((row) => row.id === REPOSITORY);
    expect(repository?.posts).toBe(2);
    expect(repository?.awaiting).toBe(2);
    expect(repository?.binding).toEqual({
      kind: "repository",
      identity: "github.com/tyrode/tyrode-infra",
      remote: "github.com/tyrode/tyrode-infra",
      paths: ["/home/alex/infra"],
    });
    // The withdrawn filing leaves the project with one post, not two.
    const project = answer.topics.find((row) => row.id === PROJECT);
    expect(project?.posts).toBe(1);
    expect(project?.awaiting).toBe(0);
    expect(project?.binding).toBeNull();
    // A merged-away identity and a retired one are not places records live.
    expect(answer.topics.map((row) => row.id)).not.toContain(MERGED);
    expect(answer.topics.map((row) => row.id)).not.toContain(RETIRED);
  });

  test("unfiled counts the posts under nothing and never the observations", async () => {
    const answer = await harness.store.topics();
    // The agreed proposal whose filing was withdrawn, the record filed under a retired entity,
    // and both questions. The observation is not a post and is counted nowhere here.
    expect(answer.unfiled).toBe(4);
  });

  // An unfile right after a file lands in the same millisecond — inside one batch they always
  // do — and the identifiers are random hex, so "the filing that holds" cannot be answered by
  // ordering on the id. The row written last is the one with the greatest rowid.
  test("a file, an unfile and a re-file inside one instant resolve to the last written", async () => {
    const at = stamp(NOW - 30 * 60_000);
    // The identifiers deliberately sort against the write order: ordering on the id would end
    // on the withdrawal, and the record would read unfiled after a re-filing that did happen.
    for (const [id, withdrawn] of [
      ["fil_1a01", 0],
      ["fil_1z02", 1],
      ["fil_1m03", 0],
    ] as const) {
      await insert(harness.db, "filings", {
        id,
        record_id: AGREED,
        entity_id: REPOSITORY,
        rationale: "it is about this",
        author_kind: "operator",
        author_id: "operator",
        heuristic: 0,
        withdrawn,
        created_at: at,
      });
    }
    harness.store.touch();
    const answer = await harness.store.topics();
    expect(answer.topics.find((row) => row.id === REPOSITORY)?.posts).toBe(3);
    expect(answer.unfiled).toBe(3);
  });

  // Silence is not a refusal: an unset stance sorts above "not now" and reads as unset.
  test("the operator's stance is the ledger's own facts, and orders the list", async () => {
    const answer = await harness.store.topics();
    expect(answer.topics[0]?.id).toBe(REPOSITORY);
    expect(answer.topics[0]?.interest).toEqual({
      state: "working",
      reason: "this is what I am on",
      at: stamp(NOW - 30 * DAY),
      by: "operator",
    });
    expect(answer.topics[1]?.interest.state).toBe("");
  });

  test("a proposed topic is a separate list joined to the proposal that carries it", async () => {
    const answer = await harness.store.topics();
    expect(answer.proposed).toHaveLength(1);
    const [proposal] = answer.proposed;
    expect(proposal?.proposalId).toBe(AGREED);
    expect(proposal?.title).toBe("a proposal two roles answered differently");
    expect(proposal?.name).toBe("the queue");
    expect(proposal?.operation).toBe("create");
    expect(proposal?.why).toBe("32 sessions in 3 checkouts cite it");
    // Over the records this deployment holds, not the ids the plan names.
    expect(proposal?.posts).toBe(1);
    expect(proposal?.targets).toEqual([{ id: PROJECT, name: "babel" }]);
  });

  test("a proposal whose record id nothing can name is dropped from the rail, not raised", async () => {
    // A row of the vintage #426 was reported from: written before the frontier's guard existed,
    // undeletable below the doors, and carrying an id `TopicProposalSchema` refuses. The rail
    // is a shortcut into the feed and nothing could open this one, so listing it would only
    // fail the `topics` door's own result and take the nameable proposal with it.
    await insert(harness.db, "records", {
      id: "rec_seed_002",
      kind: "proposal",
      root_id: "rec_seed_002",
      seq: 1,
      actor_kind: "run",
      actor_id: "run-b",
      title: "an imported proposal",
      created_at: stamp(NOW - 5 * HOUR),
      payload: JSON.stringify({ schema: 1, outcome: "file the imports somewhere" }),
    });
    await insert(harness.db, "plans", {
      id: "pln_0002",
      kind: "topic",
      subject_kind: "proposal",
      subject_id: "rec_seed_002",
      operation: "create",
      payload: JSON.stringify({ reasoning: "the imports cite it", name: "the imports" }),
      proposed_by_kind: "run",
      proposed_by_id: "run-b",
      state: "open",
      created_at: stamp(NOW - 5 * HOUR),
    });
    harness.store.touch();
    const answer = await harness.store.topics();
    expect(answer.proposed.map((row) => row.proposalId)).toEqual([AGREED]);
  });

  test("one topic answers with its row, its proposals and its own feed", async () => {
    const answer = await harness.store.topic("tyrode-infra");
    expect(answer.topic?.id).toBe(REPOSITORY);
    expect(answer.feed.posts.map((post) => post.id).sort()).toEqual([CANDIDATE, FINDING].sort());
    const unknown = await harness.store.topic("a word nobody used");
    expect(unknown.topic).toBeNull();
    expect(unknown.feed.posts).toHaveLength(0);
  });
});

describe("the peel", () => {
  test("its first three depths carry no identifier of Babel's own", async () => {
    const peeled = await harness.store.record(ARGUED);
    expect(peeled).not.toBeNull();
    const shallow = JSON.stringify({
      claim: peeled?.claim,
      case: peeled?.case,
      evidence: peeled?.evidence?.map((row) => ({
        // The session a citation names is the destination that makes evidence checkable (§4.3)
        // and is the contract's own field; what must not appear is Babel's vocabulary.
        excerpt: row.excerpt,
        speaker: row.speaker,
        note: row.note,
        line: row.line,
      })),
    });
    for (const identifier of [
      /hyp_/,
      /obs_/,
      /fnd_/,
      /pro_/,
      /qst_/,
      /ent_/,
      /asm_/,
      /run-[abc]/,
    ]) {
      expect(shallow).not.toMatch(identifier);
    }
  });

  test("the claim is the proposal's outcome and the case is the argument beneath it", async () => {
    const peeled = await harness.store.record(ARGUED);
    expect(peeled?.claim).toEqual({
      statement: "run the guard on the merge queue instead",
      standing: "accepted",
      // A decided record is offered no ruling control; the two that are, are new and reopened.
      act: "",
    });
    expect(peeled?.case).toEqual({
      problem: "the guard runs on every push and nobody reads it",
      outcome: "run the guard on the merge queue instead",
      impact: "high",
      scope: "one workflow file",
      classification: "internal",
      uncertainty: "the queue may not carry the same context",
      verification: ["the guard no longer runs on push"],
      risks: ["a slower queue"],
      openQuestions: ["does the queue see forks"],
      prerequisites: ["the queue is enabled"],
      targets: ["tyrode-infra · likely · the workflow lives there"],
    });
  });

  test("a record nobody has ruled on is offered the ruling, and a candidate has no case", async () => {
    const peeled = await harness.store.record(CANDIDATE);
    expect(peeled?.claim.standing).toBe("new");
    expect(peeled?.claim.act).toBe("Rule on this");
    expect(peeled?.case).toEqual({});
    expect(peeled?.evidence).toEqual([]);
  });

  test("evidence carries the note, the line and the session where this hub holds it", async () => {
    const peeled = await harness.store.record(ARGUED);
    expect(peeled?.evidence).toHaveLength(2);
    const [supporting, conflicting] = peeled?.evidence ?? [];
    expect(supporting?.note).toBe("the operator says so in his own words");
    expect(supporting?.line).toBe(2073);
    expect(supporting?.session).toEqual({
      selector: SESSION,
      title: "the conversation it came from",
      href: `#/sessions/${encodeURIComponent(SESSION)}?event=2072`,
    });
    // A citation whose session this hub does not hold travels anyway: the locator is what makes
    // the claim evidence, and the link is what it loses.
    expect(conflicting?.session).toBeNull();
    // Conflicting material must never render as supporting.
    expect(conflicting?.note).toStartWith("counter-evidence · ");
  });

  test("reception groups by role, carries the arguments against, and marks the split", async () => {
    const peeled = await harness.store.record(ARGUED);
    expect(peeled?.reception.contested).toBe(true);
    expect(peeled?.reception.byRole).toEqual([
      {
        role: "reception",
        support: 1,
        oppose: 1,
        unsure: 0,
        opposingRationales: ["the queue does not see forks"],
      },
      { role: "evidence", support: 1, oppose: 0, unsure: 0, opposingRationales: [] },
    ]);
    expect(peeled?.reception.operatorHistory).toEqual([
      { stance: "agree", reason: "this is the one I want", at: stamp(NOW - 17 * HOUR) },
    ]);
  });

  test("depth five is the machinery and depth one never is", async () => {
    const peeled = await harness.store.record(ARGUED);
    expect(peeled?.machinery["runId"]).toBe("run-b");
    expect(peeled?.machinery["recipe"]).toBe("outcome-integrity@3");
    expect(peeled?.machinery["revision"]).toBe(ARGUED);
    expect(peeled?.machinery["schema"]).toBe("1");
    expect(peeled?.machinery["reviews"]).toBe("1");
    expect(peeled?.machinery["policyVersion"]).toBe("pol-3");
  });

  test("related is the relations somebody asserted, and never the record itself", async () => {
    const peeled = await harness.store.record(FINDING);
    const relations = peeled?.related ?? [];
    expect(relations.map((row) => row.id)).not.toContain(FINDING);
    expect(relations).toContainEqual({
      relation: "consolidates",
      id: OBSERVATION,
      kind: "observation",
      title: "an observation, which is evidence",
    });
    // The rest of what its run wrote.
    expect(relations.some((row) => row.relation === "sibling" && row.id === CANDIDATE)).toBe(true);
  });

  test("a proposal carrying a plan says so, and one that carries none says null", async () => {
    expect(await harness.store.record(AGREED).then((peeled) => peeled?.plan)).toEqual({
      kind: "topic",
      operation: "create",
      state: "open",
    });
    expect(await harness.store.record(ARGUED).then((peeled) => peeled?.plan)).toBeNull();
  });

  test("an observation peels in its own kind without becoming a feed post", async () => {
    const peeled = await harness.store.record(OBSERVATION);
    expect(peeled?.post).toMatchObject({
      id: OBSERVATION,
      kind: "observation",
      title: "an observation, which is evidence",
    });
    expect(peeled?.claim.statement).toBe("an observation, which is evidence");
  });

  test("a record this deployment does not hold is nothing rather than an empty document", async () => {
    expect(await harness.store.record("pro_0000dead")).toBeNull();
  });
});

describe("the thread", () => {
  test("keeps rulings out of the conversation and nests a reply under what it answers", async () => {
    const answer = await harness.store.thread(ARGUED);
    expect(answer.acts.map((act) => act.act)).toEqual(["accept"]);
    expect(answer.acts[0]?.reason).toBe("do it");
    // Two contributions and the operator's two lines, his question nested under his statement.
    expect(answer.total).toBe(4);
    const roots = answer.comments.map((comment) => comment.id);
    expect(roots).toContain("fbk_0001");
    expect(roots).not.toContain("fbk_0002");
    const statement = answer.comments.find((comment) => comment.id === "fbk_0001");
    expect(statement?.kind).toBe("comment");
    expect(statement?.replies.map((reply) => reply.id)).toEqual(["fbk_0002"]);
    expect(statement?.replies[0]?.kind).toBe("question");
    // A reviewer's prose is a contribution attributed to the run that wrote it.
    const contribution = answer.comments.find((comment) => comment.id === "asm_0002#0");
    expect(contribution?.author).toEqual({ kind: "run", id: "run-y" });
    expect(contribution?.role).toBe("reception");
    expect(contribution?.text).toBe("the queue does not see forks");
  });

  test("a bare vote says nothing and is not a row in the conversation", async () => {
    const answer = await harness.store.thread(AGREED);
    expect(answer.comments).toHaveLength(0);
    expect(answer.total).toBe(0);
  });

  test("an answer under a question is a comment on the same route", async () => {
    const answer = await harness.store.thread(ANSWERED);
    expect(answer.comments).toHaveLength(1);
    expect(answer.comments[0]?.kind).toBe("answer");
    expect(answer.comments[0]?.text).toBe("no, it has been dead since the migration");
  });
});

describe("the pulse", () => {
  test("counts today and nothing before it", async () => {
    const answer = await harness.store.pulse();
    expect(answer.since).toBe(stamp(MIDNIGHT));
    // Written today: the agreed proposal, the observation and the record under review.
    expect(answer.today.records).toBe(3);
    expect(answer.today.proposals).toBe(1);
    // Recorded today: the two on the agreed proposal. The three from yesterday are not counted.
    expect(answer.today.votes).toBe(2);
    // Per record rather than per ruling: the finding's reopen is today, the rest are not.
    expect(answer.today.ruled).toBe(1);
    expect(answer.today.topicProposals).toBe(1);
    // Distinct sessions today's runs read: one conversation read twice counts once.
    expect(answer.today.sessionsRead).toBe(2);
  });

  test("names what is under review now, oldest claim first", async () => {
    const answer = await harness.store.pulse();
    expect(answer.reviewing).toEqual([
      {
        id: UNDER_REVIEW,
        kind: "hypothesis",
        title: "a candidate under review right now",
        since: stamp(NOW - 3 * HOUR),
      },
    ]);
  });

  // The whole of the day boundary: a clock moved past midnight counts nothing from before it.
  test("tomorrow counts none of today", async () => {
    harness.at(NOW + DAY);
    harness.store.touch();
    const answer = await harness.store.pulse();
    expect(answer.today).toEqual({
      sessionsRead: 0,
      records: 0,
      votes: 0,
      proposals: 0,
      topicProposals: 0,
      ruled: 0,
    });
  });
});

describe("runs and the policy", () => {
  test("a run's state and freshness come from its own closure and its last word", async () => {
    const answer = await harness.store.runs({ limit: 25, offset: 0 });
    expect(answer.total).toBe(4);
    // Neither word says a process is dead: nothing here observed one.
    expect(answer.runs.find((row) => row.id === "run-d")?.freshness).toBe("fresh");
    const live = answer.runs.find((row) => row.id === "run-c");
    expect(live?.state).toBe("running");
    expect(live?.freshness).toBe("recent");
    const done = answer.runs.find((row) => row.id === "run-a");
    expect(done?.state).toBe("finished");
    expect(done?.freshness).toBe("ended");
    // The last word is the newest thing the hub heard from it — here the observation it wrote
    // hours after its own finish instant, which is why the run row cannot answer from `finished_at`.
    expect(done?.lastWord).toBe(stamp(NOW - 5 * HOUR));
  });

  test("the state filter narrows the total as well as the page", async () => {
    const running = await harness.store.runs({ limit: 25, offset: 0, state: "running" });
    expect(running.total).toBe(2);
    expect(running.runs.map((row) => row.id)).toEqual(["run-d", "run-c"]);
  });

  test("one run answers with its receipt verbatim", async () => {
    const answer = await harness.store.run("run-b");
    expect(answer.run?.machineId).toBe("dev-01");
    expect(answer.receipt).toEqual({ runId: "run-b", counts: {} });
    expect((await harness.store.run("run-nowhere")).run).toBeNull();
  });

  test("a settled run's metered calls are read off the hub's own block in the receipt", async () => {
    // What the conductor writes beside the receipt when the owner metered the job: the whole of
    // `usage.inference`, because the in-flight row is dropped when a run ends and two columns
    // cannot hold five numbers.
    await harness.db.run(`UPDATE runs SET payload = ? WHERE id = 'run-b'`, [
      JSON.stringify({
        runId: "run-b",
        counts: {},
        inference: {
          calls: 7,
          inputTokens: 20_000,
          outputTokens: 1_500,
          cachedInputTokens: 400,
          costMicros: 410_000,
        },
      }),
    ]);
    harness.store.touch();
    const answer = await harness.store.runs({ limit: 25, offset: 0 });
    expect(answer.runs.find((row) => row.id === "run-b")?.calls).toBe(7);
    // A run nothing metered is not a run that made no call: the engine's own lane makes them
    // and nobody counts them, so the column is empty rather than zero.
    expect(answer.runs.find((row) => row.id === "run-a")?.calls).toBeNull();
  });

  test("the policy lists every recipe it declares, including one nothing has ever run", async () => {
    const answer = await harness.store.policy();
    expect(answer.version).toBe("pol-3");
    expect(answer.ceilings).toEqual({ perRunUsd: 1.5, perDayUsd: 12, concurrent: 4 });
    expect(answer.lanes).toEqual([
      { lane: "coverage", role: "reception", share: 0.5 },
      { lane: "exploration", role: "evidence", share: 0.3 },
      { lane: "filing", role: "filing", share: 0.2 },
    ]);
    // The roster is the declared list, so the lens in force that nothing has performed is a
    // row rather than an absence: a panel built off `runs` alone could not say it existed.
    expect(answer.recipes.map((recipe) => recipe.id)).toEqual([
      "outcome-integrity",
      "security-boundaries",
    ]);
    expect(answer.recipes[1]).toEqual({
      id: "security-boundaries",
      title: "Security, privacy, and trust boundaries",
      looksFor: "authority crossing a boundary nobody drew",
      enabled: false,
      lastRanAt: "",
      lastRunId: "",
      runs: 0,
    });
    // And the one that has run still carries its true count and its newest run, which is what
    // makes the zero above a reading rather than a default everything fell back to.
    expect(answer.recipes[0]).toEqual({
      id: "outcome-integrity",
      title: "Outcome integrity",
      looksFor: "claims that do not match what happened",
      enabled: true,
      lastRanAt: stamp(NOW - 30_000),
      lastRunId: "run-d",
      runs: 4,
    });
    // Only what settled today is spent today; yesterday's finished claim is not.
    expect(answer.spentTodayUsd).toBe(0.25);
  });

  test("explicit zero review shares take precedence over legacy shares independently of activity weights", async () => {
    const activityWeights = { review: 0, explore: 0.2, challenge: 0.6, synthesize: 0.3 };
    await insert(harness.db, "policies", {
      version: "pol-4",
      seq: 4,
      actor_id: "operator",
      reason: "analysis allocation",
      payload: JSON.stringify({
        activityWeights,
        explorationShare: 0,
        exploration_share: 0.8,
        coverageShare: 0.4,
      }),
      recorded_at: stamp(NOW),
    });
    const policy = await harness.store.policy();
    expect(policy.lanes.map(({ lane, share }) => ({ lane, share }))).toEqual([
      { lane: "coverage", share: 0.4 },
    ]);
  });

  test("the overlay in force is reported against the bound admission reads, never a second one", async () => {
    // `pol-3` is a Go-era row: it spells its numbers with underscores and names no per-machine
    // bound, so the bound in force is the batch it was written with — four. The strip's "from"
    // has to be that same four (#281/2): a panel reading one number where the coordinator reads
    // another is how `Batch 4 → 16` came to mean no extra draw at all.
    await insert(harness.db, "budgets", {
      id: "bdg_drain",
      created_at: stamp(NOW - HOUR),
      expires_at: stamp(NOW + HOUR),
      per_cycle_cost: null,
      daily_cost: 24,
      concurrent_per_machine: 16,
      reason: "draining victorballu before the 13:00Z reset",
      cleared_at: null,
      cleared_reason: null,
    });

    const answer = await harness.store.policy();
    // The standing figures are untouched by it.
    expect(answer.ceilings).toEqual({ perRunUsd: 1.5, perDayUsd: 12, concurrent: 4 });
    expect(answer.overlay).toEqual({
      id: "bdg_drain",
      createdAt: stamp(NOW - HOUR),
      expiresAt: stamp(NOW + HOUR),
      reason: "draining victorballu before the 13:00Z reset",
      changes: [
        { field: "dailyCost", standing: 12, overlaid: 24 },
        { field: "concurrentPerMachine", standing: 4, overlaid: 16 },
      ],
    });

    // A cleared overlay is not in force, and nothing unwinds it: the row simply stops answering.
    await harness.db.run(
      `UPDATE budgets SET cleared_at = ?, cleared_reason = ? WHERE id = 'bdg_drain'`,
      [stamp(NOW), "the window reset early"],
    );
    expect((await harness.store.policy()).overlay).toBeNull();
  });
});

// ------------------------------------------- what a record rests on, and what has looked here

describe("corroboration", () => {
  /*
    THE NUMBER THAT WAS MISSING. Three supports read as corroboration; three supports from one
    run are one reading restated, and the peel said only the count. In this deployment's own
    corpus 175 of 207 findings rest on a single run and every one of 116 proposals shares its
    finding's run, so the word the page implied was one the data did not support.
  */
  test("a record's supports are counted, and so are the runs behind them", async () => {
    const seed = async (id: string, runId: string, edge: string) => {
      await insert(harness.db, "records", {
        id,
        kind: "observation",
        root_id: id,
        seq: 0,
        parent_id: CANDIDATE,
        run_id: runId,
        recipe_id: "outcome-integrity",
        recipe_version: 3,
        actor_kind: "run",
        actor_id: runId,
        title: `an observation from ${runId}`,
        created_at: stamp(NOW - HOUR),
        payload: JSON.stringify({ schema: 1, claim: "it happened", confidence: "high" }),
      });
      await insert(harness.db, "edges", {
        id: edge,
        kind: "consolidates",
        from_kind: "finding",
        from_id: FINDING,
        to_kind: "observation",
        to_id: id,
        position: 0,
        note: null,
        actor_kind: "run",
        actor_id: runId,
        created_at: stamp(NOW - HOUR),
      });
    };
    // The fixture already consolidates one observation from run-a; two more, one of them from a
    // second run, make the two numbers differ.
    await seed("obs_00000101", "run-a", "edg_0101");
    await seed("obs_00000102", "run-c", "edg_0102");

    const peel = await harness.store.record(FINDING);
    expect(peel?.corroboration).toEqual({ supports: 3, distinctRuns: 2 });
  });

  test("supports that all came from one run say so, which is the common case", async () => {
    // The seeded finding consolidates exactly one observation, from run-a.
    const peel = await harness.store.record(FINDING);
    expect(peel?.corroboration).toEqual({ supports: 1, distinctRuns: 1 });
  });

  test("a record resting on nothing answers zero rather than being absent", async () => {
    // A candidate is not consolidated from anything: the shape is one shape for every peel, so a
    // panel never has to ask whether the field is there.
    expect((await harness.store.record(CANDIDATE))?.corroboration).toEqual({
      supports: 0,
      distinctRuns: 0,
    });
  });

  test("a cites edge is not a support: a transcript corroborates nothing on its own", async () => {
    // The fixture cites a session from the observation. Counting it would make every record that
    // quoted anything look corroborated.
    const peel = await harness.store.record(FINDING);
    expect(peel?.corroboration.supports).toBe(1);
  });
});

describe("grounded analysis challenges", () => {
  const objection = async (id: string, runId: string, ground = "missing-check") => {
    await insert(harness.db, "records", {
      id,
      kind: "hypothesis",
      root_id: id,
      seq: 1,
      run_id: runId,
      actor_kind: "run",
      actor_id: runId,
      title: `An unchecked boundary from ${runId}`,
      created_at: stamp(NOW - HOUR),
      payload: JSON.stringify({ statement: "The boundary was never checked." }),
    });
    await edge(`edg_${id}`, id, runId, ground);
  };
  const edge = async (
    id: string,
    source: string,
    actor: string,
    ground: string,
    target = CANDIDATE,
    kind = "hypothesis",
  ) => {
    await insert(harness.db, "edges", {
      id,
      kind: "challenges",
      from_kind: kind,
      from_id: source,
      to_kind: "hypothesis",
      to_id: target,
      position: 0,
      note: ground,
      actor_kind: "run",
      actor_id: actor,
      created_at: stamp(NOW - HOUR),
    });
  };

  test("counts distinct objections and their actual source runs without changing support or standing", async () => {
    await objection("hyp_00000201", "challenger-a");
    await objection("hyp_00000202", "challenger-a", "consequence");
    await objection("hyp_00000203", "challenger-b", "alternative");
    await edge("edg_duplicate", "hyp_00000201", "challenger-a", "missing-check");
    const expected = { objections: 3, distinctRuns: 2 };
    const listed = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    expect(listed.posts.find((post) => post.id === CANDIDATE)?.challenges).toEqual(expected);
    const opened = await harness.store.record(CANDIDATE);
    expect(opened?.post.challenges).toEqual(expected);
    expect(opened?.challenges).toEqual([
      {
        id: "hyp_00000203",
        kind: "hypothesis",
        runId: "challenger-b",
        grounds: "alternative",
        summary: "An unchecked boundary from challenger-b",
      },
      {
        id: "hyp_00000202",
        kind: "hypothesis",
        runId: "challenger-a",
        grounds: "consequence",
        summary: "An unchecked boundary from challenger-a",
      },
      {
        id: "hyp_00000201",
        kind: "hypothesis",
        runId: "challenger-a",
        grounds: "missing-check",
        summary: "An unchecked boundary from challenger-a",
      },
    ]);
    expect(opened?.corroboration).toEqual({ supports: 0, distinctRuns: 0 });
    expect(opened?.claim.standing).toBe("new");
  });

  test("imported self-challenge edges count in neither the feed nor the opened record", async () => {
    await edge("edg_self_challenge", CANDIDATE, "run-a", "consequence");
    const listed = await feed({ sort: "new", window: "all", surface: "all", limit: 100 });
    expect(listed.posts.find((post) => post.id === CANDIDATE)?.challenges).toEqual({
      objections: 0,
      distinctRuns: 0,
    });
    const opened = await harness.store.record(CANDIDATE);
    expect(opened?.post.challenges).toEqual({ objections: 0, distinctRuns: 0 });
    expect(opened?.challenges).toEqual([]);
  });

  test("ignores review-role opposition and malformed or falsely attributed challenge edges", async () => {
    await edge("edg_no_source", "hyp_ffffffff", "run-a", "alternative");
    await edge("edg_bad_ground", CANDIDATE, "run-a", "oppose");
    await edge("edg_bad_actor", CANDIDATE, "another-run", "alternative");
    await edge("edg_bad_kind", OBSERVATION, "run-a", "evidence");
    await edge("edg_no_evidence", CANDIDATE, "run-a", "evidence");
    await objection("old-hypothesis", "run-a");
    await insert(harness.db, "assessments", {
      id: "asm_challenge_vote",
      record_id: CANDIDATE,
      revision_id: CANDIDATE,
      run_id: "reviewer",
      role: "challenge",
      vote: "oppose",
      recorded_at: stamp(NOW),
      payload: "{}",
    });
    const opened = await harness.store.record(CANDIDATE);
    expect(opened?.post.challenges).toEqual({ objections: 0, distinctRuns: 0 });
    expect(opened?.challenges).toEqual([]);
    expect(opened?.post.oppose).toBe(1);
    // A real evidence-bearing objection is still valid under a non-evidence ground.
    await edge("edg_evidence", OBSERVATION, "run-a", "consequence", CANDIDATE, "observation");
    expect((await harness.store.record(CANDIDATE))?.challenges).toEqual([
      expect.objectContaining({ id: OBSERVATION, runId: "run-a", grounds: "consequence" }),
    ]);
  });

  test("bounds the detail list but not the objection or independent run totals", async () => {
    for (let index = 0; index < 22; index++) {
      await objection(`hyp_${(0x300 + index).toString(16).padStart(8, "0")}`, `source-${index}`);
    }
    const opened = await harness.store.record(CANDIDATE);
    expect(opened?.post.challenges).toEqual({ objections: 22, distinctRuns: 22 });
    expect(opened?.challenges).toHaveLength(20);
    expect(opened?.challenges[0]?.id).toBe("hyp_00000315");
    expect(opened?.challenges.at(-1)?.id).toBe("hyp_00000302");
  });
});

describe("the repository a record concerns", () => {
  /*
    42.7% OF THE CORPUS CANNOT NAME ITS CODEBASE, which is the largest measured defect in it and
    the one no sorter touches. The join was there the whole time: the candidate's observation
    cites a session, and the catalog holds the repository the machine half probed in that
    session's own workspace. What has to be true is that a repository Babel saw and a repository
    a transcript merely mentioned never read as the same claim.
  */

  /** The catalog learning what the cited session's workspace was, as the conductor writes it. */
  const probed = async (remote: string) =>
    harness.db.run(`UPDATE sessions SET repository_remote = ? WHERE selector = ?`, [
      remote,
      SESSION,
    ]);

  /** A candidate and the observation under it, which is where a repository claim is stated. */
  const claiming = async (repository: unknown) => {
    await insert(harness.db, "records", {
      id: "hyp_00000201",
      kind: "hypothesis",
      root_id: "hyp_00000201",
      seq: 0,
      run_id: "run-d",
      actor_kind: "run",
      actor_id: "run-d",
      title: "a candidate about a project nobody stood in",
      created_at: stamp(NOW - HOUR),
      payload: JSON.stringify({ schema: 1, statement: "the publisher drops on the second retry" }),
    });
    await insert(harness.db, "records", {
      id: "obs_00000202",
      kind: "observation",
      root_id: "obs_00000202",
      seq: 0,
      parent_id: "hyp_00000201",
      run_id: "run-d",
      recipe_id: "outcome-integrity",
      recipe_version: 3,
      actor_kind: "run",
      actor_id: "run-d",
      title: "what the conversation said about it",
      created_at: stamp(NOW - HOUR),
      payload: JSON.stringify({
        schema: 1,
        claim: "the operator says the publisher drops",
        evidence: [{ locator: { path: CITED_PATH, line: 9, digest: "aa" }, note: "he says so" }],
        repository,
      }),
    });
    // The edge a settlement writes beside the payload: `cites` carries the session an
    // observation reached for, and it is what the catalog is joined through.
    await insert(harness.db, "edges", {
      id: "edg_0201",
      kind: "cites",
      from_kind: "observation",
      from_id: "obs_00000202",
      to_kind: "session",
      to_id: SESSION,
      position: 0,
      note: "he says so",
      actor_kind: "run",
      actor_id: "run-d",
      created_at: stamp(NOW - HOUR),
    });
  };

  test("a record whose cited session was probed carries what git answered there", async () => {
    await probed("github.com/atyrode/babel");
    // The candidate's own payload cites nothing: its observation holds the citation, which is
    // why the walk has to descend before it can answer at all.
    expect((await harness.store.record(CANDIDATE))?.repository).toEqual([
      { remote: "github.com/atyrode/babel", commit: "", reference: "", provenance: "observed" },
    ]);
    // And the finding reaches the same session through the observation it consolidates.
    expect((await harness.store.record(FINDING))?.repository).toEqual([
      { remote: "github.com/atyrode/babel", commit: "", reference: "", provenance: "observed" },
    ]);
    // And a proposal two hops out: it addresses the finding, which consolidates the
    // observation, which cites the session. A proposal never cites anything itself, so if the
    // walk stopped at one step every proposal in the corpus would read as being about nothing.
    await insert(harness.db, "edges", {
      id: "edg_0203",
      kind: "addresses",
      from_kind: "proposal",
      from_id: ARGUED,
      to_kind: "finding",
      to_id: FINDING,
      position: 0,
      note: null,
      actor_kind: "run",
      actor_id: "run-b",
      created_at: stamp(NOW - DAY),
    });
    expect((await harness.store.record(ARGUED))?.repository).toEqual([
      { remote: "github.com/atyrode/babel", commit: "", reference: "", provenance: "observed" },
    ]);
  });

  test("a repository only the transcript named is marked as named, not as observed", async () => {
    await claiming({
      remote: "github.com/tyrode/tyrode-infra",
      commit: "1a8ff65ab",
      reference: "https://github.com/tyrode/tyrode-infra/issues/41",
    });
    // Nothing of Babel's ever stood in that checkout — the cited session has no repository at
    // all — so the only authority for it is a conversation, and the peel says so.
    expect((await harness.store.record("hyp_00000201"))?.repository).toEqual([
      {
        remote: "github.com/tyrode/tyrode-infra",
        commit: "1a8ff65ab",
        reference: "https://github.com/tyrode/tyrode-infra/issues/41",
        provenance: "named",
      },
    ]);
  });

  test("the same repository named and probed is one entry, observed, at the commit named", async () => {
    await probed("github.com/atyrode/babel");
    // Stated in the spelling git prints rather than the one the catalog holds: two strings for
    // one repository, and comparing them raw would file a repository Babel saw as hearsay.
    await claiming({ remote: "git@github.com:atyrode/babel.git", commit: "9c44aaf" });
    expect((await harness.store.record("hyp_00000201"))?.repository).toEqual([
      {
        remote: "github.com/atyrode/babel",
        commit: "9c44aaf",
        reference: "",
        provenance: "observed",
      },
    ]);
  });

  test("a reference naming another project is dropped rather than linked", async () => {
    // The one part of the claim nothing can check against the catalog, so the check that remains
    // is that the link goes where the record says it is about. A reader who followed this one
    // would be reading some other repository's issue 41.
    await claiming({
      remote: "github.com/atyrode/babel",
      reference: "https://github.com/someone/else/issues/41",
    });
    expect((await harness.store.record("hyp_00000201"))?.repository[0]?.reference).toBe("");
  });

  test("a record with no repository at all carries none, rather than an empty one", async () => {
    // The cited session was never probed and no payload names a project: the honest answer is
    // that this record cannot say which codebase it is about, which is 42.7% of the corpus.
    expect((await harness.store.record(CANDIDATE))?.repository).toEqual([]);
    expect((await harness.store.record(FINDING))?.repository).toEqual([]);
  });

  test("the commit and the link live at depth five, where the identifiers are", async () => {
    await probed("github.com/atyrode/babel");
    await claiming({
      remote: "github.com/atyrode/babel",
      commit: "9c44aaf1ab3c",
      reference: "https://github.com/atyrode/babel/pull/377",
    });
    const peel = await harness.store.record("hyp_00000201");
    expect(peel?.machinery["repositoryCommit"]).toBe("github.com/atyrode/babel@9c44aaf1ab3c");
    expect(peel?.machinery["repositoryReference"]).toBe(
      "https://github.com/atyrode/babel/pull/377",
    );
  });
});

describe("lens coverage", () => {
  /*
    THE ZEROS ARE THE FEATURE. Nothing could say "this method has produced nothing about this
    subject", so nothing could propose the pair. A grid that grouped `records.recipe_id` directly
    would have reported zero for every lens that ever produced a finding, because only an
    observation carries a recipe — which is a false zero and worse than no grid.
  */
  /**
   * A later policy naming three lenses, only one of which this deployment has ever run — and
   * only one of which a run could be started for: the second is declared with no body and the
   * third is turned off, which are the two ways `server.ts`'s `cookbook()` holds no recipe.
   */
  const threeLenses = async () =>
    insert(harness.db, "policies", {
      version: "pol-4",
      seq: 4,
      actor_id: "operator",
      reason: "two more lenses",
      payload: JSON.stringify({
        per_cycle_cost: 1.5,
        daily_cost: 12,
        batch_size: 4,
        recipes: [
          {
            id: "outcome-integrity",
            title: "Outcome integrity",
            enabled: true,
            body: "# Outcome integrity\n\nWhat was left unresolved?",
          },
          { id: "test-economics", title: "Test economics", enabled: true },
          {
            id: "time-and-spend",
            title: "Time sinks and token spend",
            enabled: false,
            body: "# Time sinks\n\nWhere did the hours go?",
          },
        ],
      }),
      recorded_at: stamp(NOW - HOUR),
    });

  test("a topic reports every recipe the policy holds, including the ones at zero", async () => {
    await threeLenses();
    const result = await harness.store.topic(REPOSITORY);
    const looked = result.coverage.filter((row) => row.records > 0);
    const never = result.coverage.filter((row) => row.records === 0);
    expect(looked.length).toBeGreaterThan(0);
    // An array that omitted the zeros would be the feature failing: the recipes the hub holds and
    // has never run here are the rows worth reading.
    expect(never.length).toBeGreaterThan(0);
    // THE DECLARED LIST IS THE AXIS, not the list that has run: every lens the policy holds is
    // a row, the looked-at ones first and the never-looked ones after them.
    expect(result.coverage.map((row) => row.recipeId)).toEqual([
      "outcome-integrity",
      "test-economics",
      "time-and-spend",
    ]);
  });

  test("a finding is attributed through the observation it consolidates, not by its own column", async () => {
    await threeLenses();
    // The finding filed under the repository carries a recipe of its own in this fixture, and the
    // observation beneath it carries one too; the walk must not count the record twice.
    const result = await harness.store.topic(REPOSITORY);
    const row = result.coverage.find((entry) => entry.recipeId === "outcome-integrity");
    const filed = 2; // the candidate and the finding
    expect(row?.records).toBeLessThanOrEqual(filed);
    expect(row?.records).toBeGreaterThan(0);
  });

  test("a zero says whether it can be acted on: the lens the launch door would accept", async () => {
    await threeLenses();
    const result = await harness.store.topic(RETIRED);
    // A blank cell is only an offer when an explore of that lens could start: the hub's cookbook
    // holds the recipes the policy both enables and gives a body, and a launch naming any other
    // is refused by name. A row that claimed otherwise would be a control that can only fail.
    expect(result.coverage.map((row) => [row.recipeId, row.runnable])).toEqual([
      ["outcome-integrity", true],
      ["test-economics", false],
      ["time-and-spend", false],
    ]);
  });

  test("a topic with no filings still reports every lens, all at zero", async () => {
    await threeLenses();
    const result = await harness.store.topic(RETIRED);
    expect(result.coverage.every((row) => row.records === 0)).toBe(true);
    expect(result.coverage.length).toBeGreaterThan(0);
  });
});

describe("the operator's steering, read back", () => {
  test("what he told Babel comes back newest first, with what it was about", async () => {
    const told = async (id: string, text: string, at: number, about: string | null) =>
      insert(harness.db, "steering", {
        id,
        root_id: id,
        reply_to_id: null,
        seq: 0,
        actor_kind: "operator",
        actor_id: "operator",
        target_kind: about === null ? null : "record",
        target_id: about,
        text,
        recorded_at: stamp(at),
      });
    await told("str_0001", "stop proposing work on the staging queue", NOW - 2 * DAY, null);
    await told("str_0002", "this one is about the drain", NOW - HOUR, FINDING);

    const policy = await harness.store.policy();
    expect(policy.steering.map((remark) => remark.id)).toEqual(["str_0002", "str_0001"]);
    expect(policy.steering[0]?.about).toBe(`record:${FINDING}`);
    expect(policy.steering[1]?.about).toBe("");
  });

  test("a run's own steering reply is not the operator's words", async () => {
    // The table carries a run's replies too. Reading them back here would put a model's sentence
    // under a heading that says the operator wrote it.
    await insert(harness.db, "steering", {
      id: "str_0003",
      root_id: "str_0003",
      reply_to_id: null,
      seq: 0,
      actor_kind: "run",
      actor_id: "run-a",
      target_kind: null,
      target_id: null,
      text: "a run answering a question",
      recorded_at: stamp(NOW),
    });
    expect((await harness.store.policy()).steering).toEqual([]);
  });
});
