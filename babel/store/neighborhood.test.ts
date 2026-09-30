import { afterEach, beforeEach, expect, test } from "bun:test";
import type { SqlParam } from "@manifold/plugin";
import {
  NeighborhoodQuerySchema,
  NeighborhoodResultSchema,
  type NeighborhoodQuery,
} from "../contract.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 29, 12);
const AT = new Date(NOW).toISOString();
const ROOT = "ent_00000001";
const CHILD = "ent_00000002";
const OTHER = "ent_00000003";
const SHARED = `ent_${"a".repeat(64)}`;
const RECORD = "fnd_00000001";
let harness: TestStore;

beforeEach(async () => {
  harness = await openTestStore(NOW);
  await entity(ROOT);
});
afterEach(() => harness.close());

async function entity(id: string): Promise<void> {
  await insert(harness.db, "entities", {
    id,
    kind: "project",
    name: `Synthetic ${id}`,
    canonical_id: id,
    created_by: "fixture-operator",
    created_at: AT,
  });
}

async function edge(
  id: string,
  from: string,
  to: string,
  kind = "contains",
  fromKind = "entity",
  toKind = "entity",
): Promise<void> {
  await insert(harness.db, "edges", {
    id,
    kind,
    from_kind: fromKind,
    from_id: from,
    to_kind: toKind,
    to_id: to,
    actor_kind: "operator",
    actor_id: "fixture-operator",
    created_at: AT,
    note: "stored link",
  });
}

async function fact(
  id: string,
  entityId = ROOT,
  extra: Record<string, SqlParam> = {},
): Promise<void> {
  await insert(harness.db, "facts", {
    id,
    entity_id: entityId,
    predicate: "owner",
    value: "Synthetic owner",
    valid_from: AT,
    observed_at: AT,
    authority_kind: "operator",
    authority_id: "fixture-operator",
    recorded_at: AT,
    ...extra,
  });
}

async function record(
  id = RECORD,
  entityId = ROOT,
  extra: Record<string, SqlParam> = {},
): Promise<void> {
  await insert(harness.db, "records", {
    id,
    kind: "finding",
    root_id: id,
    actor_kind: "run",
    actor_id: "fixture-run",
    run_id: "fixture-run",
    recipe_id: "fixture-recipe",
    recipe_version: 3,
    title: "A reviewable conclusion",
    created_at: AT,
    payload: '{"claim":"stored wording"}',
    ...extra,
  });
  await insert(harness.db, "filings", {
    id: `fil_${id}`,
    record_id: id,
    entity_id: entityId,
    rationale: "Explicit stored association",
    author_kind: "run",
    author_id: "fixture-run",
    created_at: AT,
  });
}

async function read(overrides: Partial<NeighborhoodQuery> = {}) {
  return NeighborhoodResultSchema.parse(
    await harness.store.neighborhood(
      NeighborhoodQuerySchema.parse({ entityId: ROOT, ...overrides }),
    ),
  );
}

test("containment walks downward in both encodings, deduplicates cycles and shared descendants, and leaves other links disjoint", async () => {
  await entity(CHILD);
  await entity(OTHER);
  await entity(SHARED);
  await entity("ent_00000004");
  await edge("root-child", ROOT, CHILD);
  await edge("other-root", OTHER, ROOT, "part-of");
  await edge("child-shared", CHILD, SHARED);
  await edge("shared-other", SHARED, OTHER, "part-of");
  await edge("cycle", SHARED, ROOT);
  await edge("label-only", ROOT, "ent_00000004", "depends-on");
  await fact("outside-fact", "ent_00000004");

  const result = await read({ depth: 8 });
  expect(result.nodes.map(({ id, depth }) => [id, depth])).toEqual([
    [ROOT, 0],
    [CHILD, 1],
    [OTHER, 1],
    [SHARED, 2],
  ]);
  expect(result.facts).toEqual([]);
  expect(result.links.find(({ id }) => id === "shared-other")).toMatchObject({
    kind: "part-of",
    fromId: SHARED,
    toId: OTHER,
    parentId: OTHER,
    childId: SHARED,
  });
  expect(result.links.find(({ id }) => id === "label-only")).toMatchObject({
    kind: "depends-on",
    parentId: null,
    childId: null,
  });
  expect(result.coverage).toMatchObject({
    traversalComplete: true,
    recordsComplete: true,
    truncated: false,
  });
  const fromChild = await read({ entityId: OTHER, depth: 0 });
  expect(fromChild.nodes.map(({ id }) => id)).toEqual([OTHER]);
  expect(fromChild.coverage.omittedNodesAtLeast).toBe(1);
  expect(await read({ depth: 8 })).toEqual(result);
  await insert(harness.db, "fact_status", {
    id: "outside-status",
    fact_id: "outside-fact",
    seq: 1,
    status: "disputed",
    recorded_at: AT,
  });
  expect(await read({ depth: 8 })).toEqual(result);
});

test("depth, node and item ceilings report only the covered frontier and do not imply an exhaustive project census", async () => {
  await entity(CHILD);
  await entity(OTHER);
  await edge("a", ROOT, CHILD);
  await edge("b", CHILD, OTHER);
  await fact("fact-root");
  await fact("fact-child", CHILD);
  const shallow = await read({ depth: 0 });
  expect(shallow.nodes.map(({ id }) => id)).toEqual([ROOT]);
  expect(shallow.facts.map(({ id }) => id)).toEqual(["fact-root"]);
  expect(shallow.coverage).toMatchObject({
    traversalComplete: false,
    recordsComplete: false,
    omittedNodesAtLeast: 1,
    reasons: ["depth"],
  });
  const limited = await read({ depth: 8, maxNodes: 2 });
  expect(limited.nodes.map(({ id }) => id)).toEqual([ROOT, CHILD]);
  expect(limited.coverage).toMatchObject({ omittedNodesAtLeast: 1, reasons: ["nodes"] });
  const oneItem = await read({ depth: 8, maxItems: 1 });
  expect(oneItem.facts.map(({ id }) => id)).toEqual(["fact-root"]);
  expect(oneItem.coverage).toMatchObject({
    returnedItems: 1,
    omittedItems: 3,
    reasons: ["items"],
    truncated: true,
  });
});

test("fresh reads keep disputed/stale facts and conclusions with exact attribution and immutable revision links", async () => {
  await fact("old-fact");
  await fact("new-fact", ROOT, { value: "Revised owner", supersedes_id: "old-fact" });
  await insert(harness.db, "fact_status", {
    id: "fact-status",
    fact_id: "old-fact",
    seq: 1,
    status: "disputed",
    actor_id: "fixture-operator",
    reason: "Two incompatible statements",
    recorded_at: AT,
  });
  await record();
  await record("fnd_00000002", ROOT, { root_id: RECORD, supersedes_id: RECORD, seq: 2 });
  await insert(harness.db, "status_events", {
    id: "stale-event",
    record_id: RECORD,
    seq: 1,
    status: "stale",
    run_id: "review-run",
    actor_kind: "run",
    actor_id: "review-run",
    reason: "Changed source",
    recorded_at: AT,
  });
  await edge("disagreement", "new-fact", "old-fact", "contradicts", "fact", "fact");
  await edge("record-conflict", RECORD, "fnd_00000002", "contradicts", "finding", "finding");
  const result = await read();
  expect(result.facts.find(({ id }) => id === "old-fact")).toMatchObject({
    value: "Synthetic owner",
    authorityKind: "operator",
    authorityId: "fixture-operator",
    replacedBy: "new-fact",
    status: { state: "disputed", reason: "Two incompatible statements" },
  });
  expect(result.facts.find(({ id }) => id === "new-fact")?.status).toBeNull();
  expect(result.records.find(({ id }) => id === RECORD)).toMatchObject({
    payloadJson: '{"claim":"stored wording"}',
    replacedBy: "fnd_00000002",
    runId: "fixture-run",
    recipeVersion: 3,
    status: { state: "stale", actorKind: "run", actorId: "review-run", runId: "review-run" },
  });
  expect(result.links.map(({ kind }) => kind)).toEqual(["contradicts", "contradicts"]);
  await insert(harness.db, "fact_status", {
    id: "later-status",
    fact_id: "old-fact",
    seq: 2,
    status: "stale",
    recorded_at: AT,
    reason: "Source changed again",
  });
  const changed = await read();
  expect(changed.facts.find(({ id }) => id === "old-fact")?.status).toMatchObject({
    state: "stale",
    reason: "Source changed again",
  });
});

test("questions resolve only explicit stored targets, subjects or work, answers stay verbatim, and withdrawals remove membership", async () => {
  await entity(OTHER);
  await record();
  await insert(harness.db, "aliases", {
    id: "alias",
    entity_id: ROOT,
    kind: "name",
    value: "synthetic-alias",
    value_key: "synthetic-alias",
    created_at: AT,
  });
  const targets = [
    { entities: [ROOT] },
    { subjects: ["synthetic-alias"] },
    { work: [{ kind: "finding", id: RECORD }] },
    { entities: [OTHER] },
    { subjects: ["an unresolvable subject"] },
  ];
  for (let i = 0; i < targets.length; i++) {
    await insert(harness.db, "questions", {
      id: `qst_0000000${i}`,
      kind: "acquire-context",
      class: "curiosity",
      text: `Question ${i}`,
      why: "Explicit uncertainty",
      raised_by_kind: "run",
      raised_by_id: "fixture-run",
      payload: JSON.stringify(targets[i]),
      created_at: AT,
    });
  }
  await insert(harness.db, "answers", {
    id: "answer",
    question_id: "qst_00000000",
    actor_id: "fixture-operator",
    outcome: "answered",
    text: "Exact operator answer; not an authorized fact",
    recorded_at: AT,
  });
  const result = await read();
  expect(result.questions.map(({ id }) => id)).toEqual([
    "qst_00000000",
    "qst_00000001",
    "qst_00000002",
  ]);
  expect(result.questions[0]).toMatchObject({ effectiveState: "open", status: null });
  expect(result.answers).toMatchObject([
    {
      questionId: "qst_00000000",
      actorId: "fixture-operator",
      text: "Exact operator answer; not an authorized fact",
    },
  ]);
  expect(result.facts).toEqual([]);
  await insert(harness.db, "filings", {
    id: "withdrawn",
    record_id: RECORD,
    entity_id: ROOT,
    rationale: "Wrong subject",
    author_kind: "operator",
    author_id: "fixture-operator",
    withdrawn: 1,
    created_at: AT,
  });
  const withdrawn = await read();
  expect(withdrawn.records).toEqual([]);
  expect(withdrawn.filings).toEqual([]);
  expect(withdrawn.questions.map(({ id }) => id)).toEqual(["qst_00000000", "qst_00000001"]);
});

test("archive references are current catalog metadata, never inferred historical captures or archive-access claims, and reads never write", async () => {
  await record();
  await edge("citation", RECORD, "omp/synthetic", "cites", "finding", "session");
  await edge("unavailable-citation", RECORD, "omp/unavailable", "cites", "finding", "session");
  await insert(harness.db, "sessions", {
    selector: "omp/synthetic",
    host: "synthetic-host",
    harness: "omp",
    source_id: "synthetic",
    seen_at: AT,
    snapshot_id: "current-synthetic-snapshot",
    archive_path: "synthetic/session.jsonl",
    content_digest: "current-digest",
  });
  await edge("dangling", ROOT, "ent_00000099");
  const before = await harness.db.query("SELECT total_changes() AS writes");
  const result = await read();
  const after = await harness.db.query("SELECT total_changes() AS writes");
  expect(after).toEqual(before);
  expect(result.sources).toMatchObject([
    {
      selector: "omp/synthetic",
      snapshotId: "current-synthetic-snapshot",
      authority: "current-catalog",
      reviewState: "unknown",
    },
  ]);
  expect(result.links.find(({ id }) => id === "unavailable-citation")?.toId).toBe(
    "omp/unavailable",
  );
  expect(result.coverage).toMatchObject({
    inaccessibleMaterial: null,
    unreviewedMaterial: null,
    unavailableEntities: 1,
    traversalComplete: false,
    truncated: true,
    reasons: ["unavailable"],
  });
});

test("byte ceilings count UTF-8 wire bytes, omit whole oversized rows and distinguish missing or empty entities", async () => {
  const empty = await read();
  expect(empty).toMatchObject({
    state: "found",
    facts: [],
    records: [],
    coverage: { recordsComplete: true, truncated: false },
  });
  const missing = await read({ entityId: "ent_00000099" });
  expect(missing).toMatchObject({
    state: "missing",
    nodes: [],
    coverage: { unavailableEntities: 1 },
  });
  await fact("large", ROOT, { value: "漢字".repeat(3000) });
  await fact("small", ROOT, { value: "Fits without clipping" });
  const result = await read({ maxBytes: 4096 });
  expect(result.facts.map(({ id }) => id)).toEqual(["small"]);
  expect(result.coverage).toMatchObject({
    omittedItems: 1,
    recordsComplete: false,
    reasons: ["bytes"],
  });
  expect(new TextEncoder().encode(JSON.stringify(result)).length).toBe(result.coverage.resultBytes);
  expect(result.coverage.resultBytes).toBeLessThanOrEqual(4096);
});
