import { afterEach, beforeEach, expect, test } from "bun:test";
import type { SqlParam } from "@manifold/plugin";
import { OPERATIONS } from "../contract.ts";
import { readAttention } from "./attention.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 20);
const DAY = 86_400_000;
const ago = (days: number): string => new Date(NOW - days * DAY).toISOString();
let fixture: TestStore;
let serial = 0;

beforeEach(async () => {
  fixture = await openTestStore(NOW);
  serial = 0;
});
afterEach(() => fixture.close());

async function record(id: string, over: Record<string, SqlParam> = {}): Promise<void> {
  await insert(fixture.db, "records", {
    id,
    root_id: id,
    kind: "hypothesis",
    seq: 0,
    run_id: `run-${id}`,
    actor_kind: "run",
    actor_id: `run-${id}`,
    title: id,
    created_at: ago(30),
    payload: "{}",
    ...over,
  });
}
async function session(selector: string, over: Record<string, SqlParam> = {}): Promise<void> {
  await insert(fixture.db, "sessions", {
    selector,
    host: "synthetic",
    harness: "omp",
    source_id: selector,
    modified_at: ago(100),
    seen_at: ago(0),
    ...over,
  });
}
async function edge(
  from: string,
  to: string,
  at: string,
  over: Record<string, SqlParam> = {},
): Promise<void> {
  await insert(fixture.db, "edges", {
    id: `edge-${++serial}`,
    kind: "cites",
    from_kind: "hypothesis",
    from_id: from,
    to_kind: "session",
    to_id: to,
    actor_kind: "run",
    actor_id: `run-${from}`,
    created_at: at,
    ...over,
  });
}

test("same source across runs, digests and revisions keeps its first durable citation; an independent source renews", async () => {
  await record("claim");
  await session("old-host/source", { source_id: "source", content_digest: "old" });
  await session("new-host/source", { source_id: "source", content_digest: "new" });
  await record("first", { kind: "observation", parent_id: "claim", created_at: ago(20) });
  await edge("first", "old-host/source", ago(20), { from_kind: "observation" });
  await record("rerun", { kind: "observation", parent_id: "claim", created_at: ago(2) });
  await edge("rerun", "new-host/source", ago(2), { from_kind: "observation" });
  await record("revision", {
    root_id: "claim",
    supersedes_id: "claim",
    seq: 1,
    created_at: ago(1),
  });
  await edge("revision", "new-host/source", ago(1));
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(20),
    basis: "evidence",
  });
  await fixture.db.run("UPDATE sessions SET modified_at = ?, seen_at = ?, content_digest = ?", [
    ago(0),
    ago(0),
    "changed-again",
  ]);
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(20),
    basis: "evidence",
  });
  await session("independent");
  await edge("revision", "independent", ago(3));
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(3),
    basis: "evidence",
  });
});

test("support introduction dates the claim, with earliest alternate paths and only three relationship levels", async () => {
  await session("source");
  await record("observation", { kind: "observation", created_at: ago(25) });
  await edge("observation", "source", ago(25), { from_kind: "observation" });
  await record("finding", { kind: "finding" });
  await edge("finding", "observation", ago(10), {
    kind: "consolidates",
    from_kind: "finding",
    to_kind: "observation",
  });
  await record("proposal", { kind: "proposal" });
  await edge("proposal", "finding", ago(8), {
    kind: "addresses",
    from_kind: "proposal",
    to_kind: "finding",
  });
  await edge("proposal", "observation", ago(12), {
    kind: "consolidates",
    from_kind: "proposal",
    to_kind: "observation",
  });
  await record("third");
  await edge("third", "proposal", ago(6), { kind: "addresses", to_kind: "proposal" });
  await record("fourth");
  await edge("fourth", "third", ago(4), { kind: "addresses", to_kind: "hypothesis" });
  await record("fifth");
  await edge("fifth", "fourth", ago(2), { kind: "addresses", to_kind: "hypothesis" });
  const index = await readAttention(fixture.db, NOW);
  expect(index.records.get("finding")).toEqual({ at: ago(10), basis: "evidence" });
  expect(index.records.get("proposal")).toEqual({ at: ago(12), basis: "evidence" });
  expect(index.records.get("fourth")).toEqual({ at: ago(4), basis: "evidence" });
  expect(index.records.get("fifth")).toEqual({ at: null, basis: null });
});

test("missing, invalid and future first citation histories cannot be repaired by newer copies", async () => {
  await session("source");
  for (const [id, at] of [
    ["missing", ""],
    ["invalid", "not-a-date"],
    ["future", ago(-1)],
  ] as const) {
    await record(id);
    await edge(id, "source", at);
    await edge(id, "source", ago(1));
  }
  await record("unresolved");
  await edge("unresolved", "legacy-uid-without-catalog", ago(2));
  await record("empty");
  await record("agent-citation");
  await session("agent", { kind: "agent" });
  await edge("agent-citation", "agent", ago(1));
  const index = await readAttention(fixture.db, NOW);
  for (const id of ["missing", "invalid", "future", "unresolved", "empty", "agent-citation"]) {
    expect(index.records.get(id)).toEqual({ at: null, basis: null });
  }
});

test("copied operator correction edges keep old evidence while the correction is explicit attention", async () => {
  await record("claim");
  await session("source");
  await edge("claim", "source", ago(20));
  await record("corrected", {
    root_id: "claim",
    seq: 1,
    supersedes_id: "claim",
    actor_kind: "operator",
    actor_id: "opaque-principal",
    created_at: ago(2),
  });
  await edge("corrected", "source", ago(2), {
    actor_kind: "operator",
    actor_id: "opaque-principal",
  });
  await session("new-source");
  await edge("corrected", "new-source", ago(2));
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(2),
    basis: "operator",
  });
  await insert(fixture.db, "assessments", {
    id: "review",
    record_id: "corrected",
    revision_id: "corrected",
    run_id: "new-review",
    role: "critic",
    vote: "support",
    payload: "{}",
    recorded_at: ago(0),
  });
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(2),
    basis: "operator",
  });
});

test("repairing a self-declared correction keeps the original record chronology", async () => {
  await record("target");
  await record("correction", { created_at: ago(20), title: "CORRECTS target" });
  await session("source");
  await edge("correction", "source", ago(20));
  await edge("correction", "target", ago(1), {
    kind: "corrects",
    to_kind: "hypothesis",
    actor_kind: "engine",
    actor_id: "link-corrections",
  });
  expect((await readAttention(fixture.db, NOW)).records.get("target")).toEqual({
    at: ago(20),
    basis: "evidence",
  });
});

test("refining a correction renews that record, not the target of its copied relation", async () => {
  await record("target");
  await record("correction", { created_at: ago(20) });
  await session("source");
  await edge("correction", "source", ago(20));
  await edge("correction", "target", ago(20), { kind: "corrects", to_kind: "hypothesis" });
  await record("revision", {
    root_id: "correction",
    seq: 1,
    supersedes_id: "correction",
    actor_kind: "operator",
    actor_id: "opaque",
    created_at: ago(1),
  });
  await edge("revision", "source", ago(1), { actor_kind: "operator", actor_id: "opaque" });
  await edge("revision", "target", ago(1), {
    kind: "corrects",
    to_kind: "hypothesis",
    actor_kind: "operator",
    actor_id: "opaque",
  });
  const index = await readAttention(fixture.db, NOW);
  expect(index.records.get("correction")).toEqual({ at: ago(1), basis: "operator" });
  expect(index.records.get("target")).toEqual({ at: ago(20), basis: "evidence" });
});

test("opaque operator ledger authors count, while model filing and steering do not", async () => {
  await record("ruled");
  await insert(fixture.db, "dispositions", {
    id: "ruling",
    record_id: "ruled",
    seq: 0,
    disposition: "defer",
    actor_id: "opaque",
    recorded_at: ago(5),
  });
  await record("feedback");
  await insert(fixture.db, "feedback", {
    id: "comment",
    record_id: "feedback",
    actor_id: "another-opaque",
    reason: "Revisit this",
    recorded_at: ago(4),
  });
  await record("filed");
  await insert(fixture.db, "filings", {
    id: "filing",
    record_id: "filed",
    entity_id: "",
    rationale: "explicit",
    author_kind: "operator",
    author_id: "opaque",
    created_at: ago(3),
  });
  await record("steered");
  await insert(fixture.db, "steering", {
    id: "steering",
    root_id: "steering",
    seq: 0,
    actor_kind: "operator",
    actor_id: "opaque",
    target_kind: "record",
    target_id: "steered",
    text: "Reconsider",
    recorded_at: ago(2),
  });
  await record("decided");
  await insert(fixture.db, "next_actions", {
    id: "action",
    record_id: "decided",
    kind: "develop-further",
    proposed_by_kind: "run",
    proposed_by_id: "model",
    summary: "Investigate",
    created_at: ago(10),
    payload: "{}",
  });
  await insert(fixture.db, "next_action_rulings", {
    id: "decision",
    next_action_id: "action",
    seq: 0,
    decision: "declined",
    operator_id: "opaque",
    recorded_at: ago(1),
  });
  await record("model-only");
  await insert(fixture.db, "filings", {
    id: "model-filing",
    record_id: "model-only",
    entity_id: "",
    rationale: "model",
    author_kind: "run",
    author_id: "model",
    created_at: ago(0),
  });
  await insert(fixture.db, "steering", {
    id: "model-steering",
    root_id: "model-steering",
    seq: 0,
    actor_kind: "run",
    actor_id: "model",
    target_kind: "record",
    target_id: "model-only",
    text: "Model reply",
    recorded_at: ago(0),
  });
  const index = await readAttention(fixture.db, NOW);
  for (const [id, days] of [
    ["ruled", 5],
    ["feedback", 4],
    ["filed", 3],
    ["steered", 2],
    ["decided", 1],
  ] as const) {
    expect(index.records.get(id)).toEqual({ at: ago(days), basis: "operator" });
  }
  expect(index.records.get("model-only")).toEqual({ at: null, basis: null });
});

test("questions date creation and explicit answers, not unattributed lifecycle activity", async () => {
  for (const [id, at] of [
    ["question", ago(20)],
    ["unknown", ""],
  ] as const) {
    await insert(fixture.db, "questions", {
      id,
      kind: "reality",
      class: "blocking",
      text: "Which?",
      why: "Ambiguous",
      raised_by_kind: "run",
      raised_by_id: "model",
      payload: "{}",
      created_at: at,
    });
  }
  await insert(fixture.db, "question_events", {
    id: "automatic",
    question_id: "question",
    seq: 0,
    state: "open",
    recorded_at: ago(0),
  });
  expect((await readAttention(fixture.db, NOW)).questions.get("question")).toEqual({
    at: ago(20),
    basis: "question",
  });
  await insert(fixture.db, "answers", {
    id: "answer",
    question_id: "question",
    actor_id: "opaque",
    outcome: "answered",
    text: "This one",
    recorded_at: ago(2),
  });
  const index = await readAttention(fixture.db, NOW);
  expect(index.questions.get("question")).toEqual({ at: ago(2), basis: "operator" });
  expect(index.questions.get("unknown")).toEqual({ at: null, basis: null });
  await insert(fixture.db, "question_events", {
    id: "operator-event",
    question_id: "question",
    seq: 1,
    state: "open",
    actor_id: "operator",
    recorded_at: ago(1),
  });
  expect((await readAttention(fixture.db, NOW)).questions.get("question")).toEqual({
    at: ago(1),
    basis: "operator",
  });
});

test("grounded incoming objections use actual held relations; cycles and unrelated observations cannot renew", async () => {
  await record("claim");
  await record("objection", { kind: "observation", created_at: ago(10) });
  await record("unrelated", { kind: "observation", created_at: ago(1) });
  await session("source");
  await session("unrelated-source");
  await edge("objection", "source", ago(10), { from_kind: "observation" });
  await edge("unrelated", "unrelated-source", ago(1), { from_kind: "observation" });
  await edge("objection", "claim", ago(8), {
    kind: "challenges",
    from_kind: "observation",
    to_kind: "hypothesis",
    note: "evidence",
  });
  await edge("objection", "claim", ago(2), {
    kind: "consolidates",
    from_kind: "observation",
    to_kind: "hypothesis",
  });
  await edge("claim", "claim", ago(1), { kind: "addresses", to_kind: "hypothesis" });
  await edge("unrelated", "claim", ago(1), {
    kind: "challenges",
    from_kind: "observation",
    to_kind: "hypothesis",
    actor_id: "not-the-producing-run",
  });
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(8),
    basis: "evidence",
  });
});

test("retained material can resolve a missing catalog selector but selections alone are not citations", async () => {
  await record("claim", { run_id: "consumer" });
  await record("uncited", { run_id: "consumer" });
  await record("lost-history", { run_id: null });
  await edge("lost-history", "missing-selector", "");
  await record("repeated", {
    root_id: "lost-history",
    seq: 1,
    run_id: "consumer",
    created_at: ago(1),
  });
  await edge("repeated", "missing-selector", ago(1));
  await insert(fixture.db, "runs", {
    id: "consumer",
    kind: "analysis",
    prepare_job_id: "prepare-job",
    started_at: ago(1),
    payload: "{}",
  });
  await insert(fixture.db, "runs", {
    id: "prepare",
    kind: OPERATIONS.prepare,
    job_id: "prepare-job",
    closure: "completed",
    started_at: ago(1),
    payload: JSON.stringify({
      material: {
        sessions: [
          {
            selector: "missing-selector",
            harness: "omp",
            sourceId: "actual-source",
            sourceDigest: "changing-digest",
          },
        ],
      },
    }),
  });
  await edge("claim", "missing-selector", ago(15));
  await session("other-host", { source_id: "actual-source" });
  await edge("claim", "other-host", ago(2));
  const index = await readAttention(fixture.db, NOW);
  expect(index.records.get("claim")).toEqual({ at: ago(15), basis: "evidence" });
  expect(index.records.get("uncited")).toEqual({ at: null, basis: null });
  expect(index.records.get("lost-history")).toEqual({ at: null, basis: null });
});

test("conflicting retained identities stay unknown after a later unambiguous citation", async () => {
  await record("claim", { run_id: "ambiguous" });
  await record("revision", { root_id: "claim", seq: 1, run_id: "clear" });
  for (const [run, sources] of [
    ["ambiguous", ["source-a", "source-b"]],
    ["clear", ["source-a"]],
  ] as const) {
    await insert(fixture.db, "runs", {
      id: run,
      kind: "analysis",
      prepare_job_id: `job-${run}`,
      started_at: ago(1),
      payload: "{}",
    });
    await insert(fixture.db, "runs", {
      id: `prepare-${run}`,
      kind: OPERATIONS.prepare,
      job_id: `job-${run}`,
      closure: "completed",
      started_at: ago(1),
      payload: JSON.stringify({
        material: {
          sessions: sources.map((sourceId) => ({
            selector: "missing-selector",
            harness: "omp",
            sourceId,
          })),
        },
      }),
    });
  }
  await edge("claim", "missing-selector", ago(20));
  await edge("revision", "missing-selector", ago(1));
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: null,
    basis: null,
  });
});

test("citation history beyond the first SQL page is not truncated into freshness", async () => {
  await record("claim");
  await session("source");
  const copies = Array.from({ length: 260 }, (_, n) => ({
    sql: `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
      VALUES(?, 'cites', 'hypothesis', 'claim', 'session', 'source', 'run', 'model', ?)`,
    params: [`copy-${n}`, ago(1)],
  }));
  for (let offset = 0; offset < copies.length; offset += 256) {
    await fixture.db.batch(copies.slice(offset, offset + 256));
  }
  await edge("claim", "source", ago(30));
  expect((await readAttention(fixture.db, NOW)).records.get("claim")).toEqual({
    at: ago(30),
    basis: "evidence",
  });
});
