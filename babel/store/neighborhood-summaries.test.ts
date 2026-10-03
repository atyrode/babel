import { afterEach, beforeEach, expect, test } from "bun:test";
import {
  NeighborhoodQuerySchema,
  NeighborhoodResultSchema,
  NeighborhoodSourceQuerySchema,
  NavigationMapPrepareInputSchema,
  TranscriptMapPolicySchema,
  type TranscriptMapModelResult,
  type TranscriptMapWork,
} from "../contract.ts";
import { mapPrepare } from "../machine/transcript-map-jobs.ts";
import { transcriptMaps } from "./transcript-maps.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = "2026-09-30T10:00:00.000Z";
const ROOT = "ent_00005060";
const query = NeighborhoodQuerySchema.parse({ entityId: ROOT });
const policy = TranscriptMapPolicySchema.parse({
  sourceMachineId: "synthetic-owner",
  executorMachineId: "synthetic-executor",
  profile: { containerId: "synthetic-profile", expectedRevision: 3 },
  dailyCost: 1,
  generateRecipe: "navigation",
  reviewRecipe: "navigation-review",
  recipes: [
    {
      id: "navigation",
      version: 1,
      body: "Navigate supplied material; preserve disputes and gaps.",
    },
    {
      id: "navigation-review",
      version: 1,
      body: "Check supplied navigation against exact inputs.",
    },
  ],
  neighborhoods: [query],
  segmentation: { leafBytes: 8192, directBytes: 4096 },
});
let db: TestStore;
beforeEach(async () => {
  db = await openTestStore(Date.parse(NOW));
  await insert(db.db, "entities", {
    id: ROOT,
    kind: "project",
    name: "Synthetic navigation",
    canonical_id: ROOT,
    created_by: "synthetic-operator",
    created_at: NOW,
  });
  await insert(db.db, "facts", {
    id: "fact-navigation",
    entity_id: ROOT,
    predicate: "owner",
    value: "Synthetic owner",
    valid_from: NOW,
    observed_at: NOW,
    authority_kind: "operator",
    authority_id: "synthetic-operator",
    recorded_at: NOW,
  });
  await insert(db.db, "fact_status", {
    id: "disputed-navigation",
    fact_id: "fact-navigation",
    seq: 1,
    status: "disputed",
    actor_id: "synthetic-reviewer",
    reason: "Two owners disagree",
    recorded_at: NOW,
  });
});
afterEach(() => db.close());
const maps = () => transcriptMaps(db.store);

async function finish(work: TranscriptMapWork, result: TranscriptMapModelResult) {
  const store = maps();
  const runId = `run-${work.id}-${work.attempt}`;
  expect(await store.startWork(work.id, { id: `claim-${work.id}`, runId, fence: 1 }, NOW)).toBe(
    true,
  );
  const details = (await store.work(work.id))!;
  const sealed = await mapPrepare(
    NavigationMapPrepareInputSchema.parse({
      runId,
      sourceMachineId: policy.sourceMachineId,
      executorMachineId: policy.executorMachineId,
      source: details.plan.source,
      nodeId: details.node.id,
      node: details.node,
      text: await store.materialText(details),
      segmentation: details.plan.segmentation,
      mode: work.mode,
      children: work.children.map(({ nodeId: _id, ...child }) => child),
      ...(details.baseSummary
        ? { baseSummary: { id: details.baseSummary.id, text: details.baseSummary.text } }
        : {}),
      ...(details.feedback ? { feedback: details.feedback } : {}),
    }),
    {
      write: async () => {
        throw new Error("A summary must not create records.");
      },
      receipt: async () => {},
    },
    {
      session: async () => {
        throw new Error("A neighborhood must not retrieve sessions.");
      },
      index: async () => {
        throw new Error("A neighborhood has no raw archive index.");
      },
      document: async (_file, text) => {
        const material = JSON.parse(text);
        expect(material.source.kind).toBe("neighborhood");
        if (material.text !== null) {
          const rows = material.text
            .trimEnd()
            .split("\n")
            .map((line: string) => JSON.parse(line));
          expect(
            rows.find((row: { id: string }) => row.id === "fact-navigation").value.status.state,
          ).toBe("disputed");
          expect(rows.some((row: { kind: string }) => row.kind === "coverage")).toBe(true);
        }
      },
    },
  );
  expect(sealed.mapping).toMatchObject({ kind: "material", context: null, access: null });
  const settlement = await store.settlementStatements({
    details,
    result,
    runId,
    now: NOW,
    guard: { sql: "1", params: [] },
  });
  await db.db.batch(settlement.statements);
  return settlement.summaryId;
}
async function generate(
  text = "The recorded owner is disputed; consult the attributed fact before acting.",
) {
  const store = maps();
  await store.refreshWork(policy, NOW);
  const work = (await store.offers(policy, NOW)).find((work) => work.mode === "generate")!;
  expect(work).toBeDefined();
  await finish(work, { kind: "summary", text });
  return NeighborhoodResultSchema.parse(await db.store.neighborhood(query));
}

test("reads keep the baseline usable without buying generation, then serve generated hub-record inference with precise sources", async () => {
  const missing = await db.store.neighborhood(query);
  expect(missing.summary.state).toBe("missing");
  expect(missing.facts[0]?.status?.state).toBe("disputed");
  expect(await db.db.query("SELECT id FROM transcript_map_work")).toEqual([]);
  expect(await db.db.query("SELECT id FROM transcript_map_captures")).toEqual([]);
  await maps().refreshWork({ ...policy, neighborhoods: [] }, NOW);
  expect(await maps().offers(policy, NOW)).toEqual([]);
  const result = await generate();
  expect(result.summary).toMatchObject({
    inference: true,
    state: "available",
    source: { kind: "neighborhood", disputed: 1 },
    producer: { executorMachineId: policy.executorMachineId, profile: policy.profile },
    coverage: { partial: false, stale: false, gapBytes: 0, unmappedBytes: 0 },
  });
  expect(result.summary.views[0]?.summary.text).toContain("owner is disputed");
  const sourceId = result.summary.source!.id;
  const exact = await maps().neighborhoodSource(NeighborhoodSourceQuerySchema.parse({ sourceId }));
  const fact = exact.rows.find((row) => row.source.kind === "facts")!;
  expect(fact.source).toMatchObject({
    id: "fact-navigation",
    value: { value: "Synthetic owner", status: { state: "disputed" } },
  });
  expect(fact.source.revision).toMatch(/^sha256:[a-f0-9]{64}$/);
  expect(await maps().candidates(policy.sourceMachineId, { captureId: sourceId })).toEqual([]);
  expect(
    await maps().reference(
      policy.sourceMachineId,
      result.summary.versionId!,
      result.summary.views[0]!.node.id,
    ),
  ).toBeNull();
  expect((await db.db.query("SELECT id FROM entities")).map((row) => row.id)).toEqual([ROOT]);
  expect((await db.db.query("SELECT id FROM facts")).map((row) => row.id)).toEqual([
    "fact-navigation",
  ]);
  expect(await db.db.query("SELECT id FROM records")).toEqual([]);
});

test("changed source status invalidates currentness without hiding old exact inputs or baseline records", async () => {
  const original = await generate();
  await insert(db.db, "fact_status", {
    id: "stale-navigation",
    fact_id: "fact-navigation",
    seq: 2,
    status: "stale",
    actor_id: "synthetic-reviewer",
    reason: "Ownership changed",
    recorded_at: NOW,
  });
  const changed = await db.store.neighborhood(query);
  expect(changed.summary.state).toBe("stale");
  expect(changed.summary.coverage).toMatchObject({ stale: true, partial: true });
  expect(changed.facts[0]?.status?.state).toBe("stale");
  expect(changed.summary.source?.revision).toBe(original.summary.source?.revision);
  const exact = await maps().neighborhoodSource(
    NeighborhoodSourceQuerySchema.parse({ sourceId: original.summary.source!.id }),
  );
  expect(exact.rows.find((row) => row.source.kind === "facts")?.source.value.status).toMatchObject({
    state: "disputed",
  });
  await maps().refreshWork(policy, NOW);
  expect(
    (await maps().offers(policy, NOW)).some(
      (work) => work.mode === "generate" && work.versionId !== original.summary.versionId,
    ),
  ).toBe(true);
});

test("served summaries alone enter bounded review and a rejection hides prose without hiding its records", async () => {
  await generate();
  await maps().refreshWork(policy, NOW);
  const review = (await maps().offers(policy, NOW)).find((work) => work.mode === "review")!;
  await finish(review, {
    kind: "review",
    verdict: "reject",
    reason: "The inferred owner wording must be corrected.",
  });
  const result = await db.store.neighborhood(query);
  expect(result.summary.views).toEqual([]);
  expect(result.summary.coverage).toMatchObject({ partial: true, stale: true });
  expect(result.facts[0]?.value).toBe("Synthetic owner");
  await maps().refreshWork(policy, NOW);
  const correction = (await maps().offers(policy, NOW)).find((work) => work.mode === "correct")!;
  expect(correction.baseSummaryId).toBe(review.baseSummaryId);
  await finish(correction, {
    kind: "summary",
    text: "A disputed stored owner claim exists, not a verified owner.",
  });
  const corrected = await db.store.neighborhood(query);
  expect(corrected.summary.state).toBe("available");
  expect(corrected.summary.views[0]?.summary).toMatchObject({
    supersedes: review.baseSummaryId,
    correctionDepth: 1,
  });
});

test("source changes between settlement preparation and atomic publication cannot publish a current summary", async () => {
  const store = maps();
  await store.refreshWork(policy, NOW);
  const work = (await store.offers(policy, NOW))[0]!;
  await store.startWork(work.id, { id: "held-claim", runId: "held-run", fence: 1 }, NOW);
  const details = (await store.work(work.id))!;
  const pending = await store.settlementStatements({
    details,
    result: { kind: "summary", text: "A disputed stored owner claim." },
    runId: "held-run",
    now: NOW,
    guard: { sql: "1", params: [] },
  });
  await insert(db.db, "fact_status", {
    id: "changed-while-posting",
    fact_id: "fact-navigation",
    seq: 2,
    status: "stale",
    actor_id: "synthetic-reviewer",
    reason: "Changed before publication",
    recorded_at: NOW,
  });
  await db.db.batch(pending.statements);
  expect(await db.db.query("SELECT id FROM transcript_map_summaries")).toEqual([]);
  expect(await store.sourceCurrent(details)).toBe(false);
  expect((await db.store.neighborhood(query)).facts[0]?.status?.state).toBe("stale");
});

test("unrelated source changes do not invalidate a still-exact neighborhood revision", async () => {
  const store = maps();
  await store.refreshWork(policy, NOW);
  const work = (await store.offers(policy, NOW))[0]!;
  await insert(db.db, "entities", {
    id: "ent_00009999",
    kind: "project",
    name: "Unrelated",
    canonical_id: "ent_00009999",
    created_by: "operator",
    created_at: NOW,
  });
  await finish(work, { kind: "summary", text: "The stored owner is disputed." });
  expect((await db.store.neighborhood(query)).summary.state).toBe("available");
});

test("generated-source drilldown retains mandatory redaction instead of disclosing source secrets", async () => {
  const synthetic = "ghp_SYNTHETICabcdefghijklmnopqrstuv";
  await insert(db.db, "facts", {
    id: "fact-secret",
    entity_id: ROOT,
    predicate: "note",
    value: `Authorization: Bearer ${synthetic}`,
    valid_from: NOW,
    observed_at: NOW,
    authority_kind: "operator",
    authority_id: "operator",
    recorded_at: NOW,
  });
  const result = await generate();
  expect(result.summary.source!.redactions).toBeGreaterThan(0);
  const source = await maps().neighborhoodSource(
    NeighborhoodSourceQuerySchema.parse({ sourceId: result.summary.source!.id }),
  );
  expect(JSON.stringify(source.rows)).not.toContain(synthetic);
  expect(source.rows.find((row) => row.source.id === "fact-secret")?.source.revision).toMatch(
    /^sha256:[0-9a-f]{64}$/,
  );
});

test("summary and exact-source pages respect byte bounds and missing/unavailable summaries do not displace stored facts", async () => {
  const tight = NeighborhoodQuerySchema.parse({ entityId: ROOT, maxBytes: 4096 });
  await maps().refreshWork({ ...policy, neighborhoods: [tight] }, NOW);
  const work = (await maps().offers({ ...policy, neighborhoods: [tight] }, NOW))[0]!;
  await finish(work, { kind: "summary", text: "Navigation inference. ".repeat(20) });
  const bounded = await db.store.neighborhood(tight);
  expect(bounded.summary.state).toBe("bounded");
  expect(bounded.facts[0]?.value).toBe("Synthetic owner");
  expect(Buffer.byteLength(JSON.stringify(bounded))).toBeLessThanOrEqual(4096);
  expect(bounded.coverage.resultBytes).toBe(Buffer.byteLength(JSON.stringify(bounded)));
  expect(await db.db.query("SELECT summary_id FROM transcript_map_served")).toEqual([]);
  const details = (await maps().work(work.id))!;
  const page = await maps().neighborhoodSource(
    NeighborhoodSourceQuerySchema.parse({
      sourceId: details.plan.source.id,
      maxRecords: 1,
      maxBytes: 4096,
    }),
  );
  expect(page.rows[0]?.record).toBe(1);
  expect(page.nextOffset).toBe(1);
  expect(Buffer.byteLength(JSON.stringify(page))).toBeLessThanOrEqual(4096);
  const absent = await maps().neighborhoodSource(
    NeighborhoodSourceQuerySchema.parse({ sourceId: `tmcap_${"0".repeat(64)}` }),
  );
  expect(absent.source).toBeNull();
  // Lose optional summary routing, not the retained source provenance privacy still needs.
  await db.db.run(
    "ALTER TABLE transcript_map_neighborhood_inputs RENAME COLUMN query_key TO unavailable_query_key",
  );
  const unavailable = await db.store.neighborhood(tight);
  expect(unavailable.summary.state).toBe("unavailable");
  expect(unavailable.facts[0]?.status?.state).toBe("disputed");
});

test("bounded containment coverage, not other relations, is the generated source scope", async () => {
  await insert(db.db, "entities", {
    id: "ent_00005061",
    kind: "project",
    name: "Child",
    canonical_id: "ent_00005061",
    created_by: "operator",
    created_at: NOW,
  });
  await insert(db.db, "edges", {
    id: "part-of-child",
    kind: "part-of",
    from_kind: "entity",
    from_id: "ent_00005061",
    to_kind: "entity",
    to_id: ROOT,
    actor_kind: "operator",
    actor_id: "operator",
    created_at: NOW,
  });
  const limited = NeighborhoodQuerySchema.parse({ entityId: ROOT, depth: 0 });
  const route = { ...policy, neighborhoods: [limited] };
  await maps().refreshWork(route, NOW);
  await finish((await maps().offers(route, NOW))[0]!, {
    kind: "summary",
    text: "Disputed owner; child omitted at the containment depth boundary.",
  });
  const result = await db.store.neighborhood(limited);
  expect(result.summary.source?.coverage).toMatchObject({
    recordsComplete: false,
    reasons: ["depth"],
    omittedNodesAtLeast: 1,
  });
  expect(result.summary.coverage?.partial).toBe(true);
  expect(result.nodes.map((node) => node.id)).toEqual([ROOT]);
  expect(result.links[0]).toMatchObject({
    parentId: ROOT,
    childId: "ent_00005061",
    kind: "part-of",
  });
});
