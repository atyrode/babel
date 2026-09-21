import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import {
  SESSION_RECORD_COORDINATES,
  TranscriptMapNodeSchema,
  TranscriptMapPlanSchema,
  TranscriptMapPolicySchema,
  TranscriptMapSourceSchema,
  type TranscriptMapAccess,
  type TranscriptMapContext,
  type TranscriptMapModelResult,
  type TranscriptMapNode,
  type TranscriptMapPlan,
  type TranscriptMapWork,
} from "../contract.ts";
import { transcriptMapCaptureId, transcriptMapManifestDigest, transcriptMapNodeId, transcriptMapPlanId } from "../transcript-map-identity.ts";
import { transcriptMaps, type TranscriptMaps } from "./transcript-maps.ts";
import { openTestStore, type TestStore } from "./testdb.ts";

const NOW = "2026-09-20T10:00:00.000Z";
const LATER = "2026-09-20T10:02:00.000Z";
const hash = (text: string) => `sha256:${createHash("sha256").update(text).digest("hex")}`;
const policy = TranscriptMapPolicySchema.parse({
  machineId: "machine-map", profile: { containerId: "profile-map", expectedRevision: 1 }, dailyCost: 2,
  generateRecipe: "mapping-generate", reviewRecipe: "mapping-review",
  recipes: [{ id: "mapping-generate", version: 1, body: "Describe the supplied navigation span." },
    { id: "mapping-review", version: 1, body: "Review the supplied navigation summary." }],
  segmentation: { leafBytes: 1024, directBytes: 0, fanout: 64, maxDepth: 4 },
  maxAttempts: 2, maxReviews: 1, maxCorrections: 1,
});
const context: TranscriptMapContext = {
  digest: hash("inventory and classifications"), policyDigest: hash("classifications"),
  classId: "private", ceiling: 2, eligibleCaptures: 1, observedAt: NOW,
};
const scope = { machineId: policy.machineId, context };
const opened: TestStore[] = [];
afterEach(() => { for (const db of opened.splice(0)) db.close(); });
async function setup() {
  const db = await openTestStore(Date.parse(NOW));
  opened.push(db);
  return { ...db, maps: transcriptMaps(db.store) };
}
interface MapFixture {
  plan: TranscriptMapPlan;
  nodes: TranscriptMapNode[];
  access: TranscriptMapAccess;
}
function fixture(count = 2, snapshot = "a".repeat(64)): MapFixture {
  const records = Array.from({ length: count }, (_, index) => `${String(index).padStart(4, "0")} ${"x".repeat(1018)}\n`);
  const capture = { host: "archive-host", harness: "omp" as const, session: "map-session", snapshot, path: "sessions/map-session.jsonl", capturedAt: NOW };
  const source = TranscriptMapSourceSchema.parse({ ...capture, id: transcriptMapCaptureId(capture), coordinates: SESSION_RECORD_COORDINATES,
    captureDigest: hash(records.join("")), sourceDigest: hash(records.join("")), bytes: records.length * 1024, records: records.length });
  const planId = transcriptMapPlanId(source, policy.segmentation);
  function span(first: number, last: number) {
    return { firstRecord: first + 1, lastRecord: last + 1, byteOffset: first * 1024, byteLength: (last - first + 1) * 1024,
      digest: hash(records.slice(first, last + 1).join("")),
      anchor: { line: first + 1, byteOffset: first * 1024, byteLength: 1024, digest: hash(records[first] ?? ""), time: null } };
  }
  const nodes: TranscriptMapNode[] = records.map((_, index) => {
    const bounds = span(index, index);
    return TranscriptMapNodeSchema.parse({ id: transcriptMapNodeId(planId, 0, index, bounds, [], null), planId, parentId: null, level: 0, ordinal: index, span: bounds, children: [], gap: null });
  });
  let layer = [...nodes];
  let level = 1;
  while (layer.length > 1) {
    const parents: TranscriptMapNode[] = [];
    for (let start = 0; start < layer.length; start += 64) {
      const group = layer.slice(start, start + 64);
      const first = group[0];
      const last = group[group.length - 1];
      if (!first || !last) throw new Error("empty fixture group");
      const bounds = span(first.span.firstRecord - 1, last.span.lastRecord - 1);
      const ids = group.map((child) => child.id);
      const parent = TranscriptMapNodeSchema.parse({ id: transcriptMapNodeId(planId, level, parents.length, bounds, ids, null), planId, parentId: null, level, ordinal: parents.length, span: bounds, children: ids, gap: null });
      for (const child of group) child.parentId = parent.id;
      parents.push(parent);
    }
    nodes.push(...parents);
    layer = parents;
    level += 1;
  }
  const plan = TranscriptMapPlanSchema.parse({ id: planId, source, segmentation: policy.segmentation, rootId: layer[0]?.id ?? null,
    nodeCount: nodes.length, digest: transcriptMapManifestDigest(nodes), direct: count === 0, gapBytes: 0 });
  const access = { captureId: source.id, contextDigest: context.digest, sensitivity: 2 };
  return { plan, nodes, access };
}
async function publish(maps: TranscriptMaps, data: MapFixture) {
  await maps.recordPlan({ ...scope, ...data, offset: 0, nextOffset: null, now: NOW });
  return maps.ensureVersion(data.plan.id, policy, NOW);
}
async function settle(db: TestStore, maps: TranscriptMaps, item: TranscriptMapWork, result: TranscriptMapModelResult, now = NOW) {
  const runId = `run-${item.id}`;
  expect(await maps.startWork(item.id, { id: `claim-${item.id}`, runId, fence: 1 }, now)).toBe(true);
  const details = await maps.work(item.id);
  if (!details) throw new Error("missing fixture work");
  const settlement = await maps.settlementStatements({ details, result, runId, now, guard: { sql: "1", params: [] } });
  await db.db.batch(settlement.statements);
  return settlement.summaryId;
}
async function generate(db: TestStore, maps: TranscriptMaps) {
  // A hierarchy is bottom-up: new parents become eligible only after their children settle.
  for (let pass = 0; pass < 5; pass += 1) {
    await maps.refreshWork(policy, NOW, 128);
    for (const item of await maps.offers(policy, NOW, 64)) {
      if (item.mode === "generate") await settle(db, maps, item, { kind: "summary", text: `Navigation for span ${item.nodeId.slice(-6)}.` });
    }
  }
}

test("paged plans stay invisible until manifest verification and never authorize another class", async () => {
  const db = await setup();
  const data = fixture();
  expect(await db.maps.recordPlan({ ...scope, ...data, nodes: data.nodes.slice(0, 1), offset: 0, nextOffset: 1, now: NOW })).toEqual({ complete: false });
  expect((await db.maps.coverage(scope)).unmappedBytes).toBe(data.plan.source.bytes);
  expect((await db.maps.coverage(scope)).partial).toBe(true);
  expect(await db.maps.status(scope)).toEqual({ eligibleCaptures: 1, verifiedMappedCaptures: 0, observedAt: NOW, partial: true });
  await expect(db.maps.ensureVersion(data.plan.id, policy, NOW)).rejects.toThrow();
  expect(await db.maps.recordPlan({ ...scope, ...data, nodes: data.nodes.slice(1), offset: 1, nextOffset: null, now: NOW })).toEqual({ complete: true });
  const version = await db.maps.ensureVersion(data.plan.id, policy, NOW);
  const root = data.plan.rootId;
  if (!root) throw new Error("missing root");
  expect((await db.maps.node(scope, version.id, root))?.node.id).toBe(root);
  const denied = { ...scope, context: { ...context, classId: "public", ceiling: 0, eligibleCaptures: 0 } };
  expect(await db.maps.node(denied, version.id, root)).toBeNull();
  expect(await db.maps.children(denied, version.id, root)).toEqual([]);
  expect((await db.maps.coverage(denied)).sourceBytes).toBe(0);
  expect((await db.maps.status(denied)).verifiedMappedCaptures).toBe(0);
  const changed = { ...context, digest: hash("changed classifications"), observedAt: LATER };
  await db.maps.recordCatalog({ machineId: policy.machineId, context: changed, entries: [], nextCursor: null, now: LATER });
  expect(await db.maps.node(scope, version.id, root)).toBeNull();
  expect(await db.maps.status(scope)).toEqual({ eligibleCaptures: 1, verifiedMappedCaptures: 0, observedAt: NOW, partial: true });
});

test("a receipt larger than the engine batch limit publishes without truncating its graph", async () => {
  const db = await setup();
  const data = fixture(300);
  const version = await publish(db.maps, data);
  if (!data.plan.rootId) throw new Error("missing root");
  const parents = await db.maps.children(scope, version.id, data.plan.rootId);
  expect(parents.map((view) => view.node.span.byteLength)).toEqual([65536, 65536, 65536, 65536, 45056]);
  const coverage = await db.maps.coverage(scope);
  expect(coverage.unmappedBytes).toBe(307200);
  expect(coverage.tailBytes).toBe(307200);
  expect(coverage.partial).toBe(true);
});

test("a corrupt final manifest cannot publish previously ingested nodes", async () => {
  const db = await setup();
  const data = fixture();
  const corrupt = { ...data, plan: { ...data.plan, digest: hash("wrong manifest") } };
  await expect(db.maps.recordPlan({ ...scope, ...corrupt, offset: 0, nextOffset: null, now: NOW })).rejects.toThrow();
  await expect(db.maps.ensureVersion(data.plan.id, policy, NOW)).rejects.toThrow();
  expect((await db.maps.coverage(scope)).summarizedBytes).toBe(0);
  expect((await db.maps.coverage(scope)).partial).toBe(true);
});

test("identical captures and unchanged prefix spans bind original prose without buying another run", async () => {
  const db = await setup();
  const first = fixture();
  const v1 = await publish(db.maps, first);
  await generate(db, db.maps);
  expect(await db.maps.status(scope)).toEqual({ eligibleCaptures: 1, verifiedMappedCaptures: 1, observedAt: NOW, partial: false });
  const original = await db.maps.node(scope, v1.id, first.nodes[0]?.id ?? "");
  expect(original?.summary?.runId).toStartWith("run-");
  const second = fixture(2, "b".repeat(64));
  const v2 = await publish(db.maps, second);
  await db.maps.refreshWork(policy, NOW, 128);
  expect(await db.maps.offers(policy, NOW)).toEqual([]);
  const reused = await db.maps.node(scope, v2.id, second.nodes[0]?.id ?? "");
  expect(reused?.reused).toBe(true);
  expect(reused?.summary).toEqual(original?.summary);
  expect(reused?.coverage.stale).toBe(false);
  const extended = fixture(3, "c".repeat(64));
  const v3 = await publish(db.maps, extended);
  await db.maps.refreshWork(policy, NOW, 128);
  const offered = await db.maps.offers(policy, NOW);
  expect(offered.map((item) => item.nodeId)).toEqual([extended.nodes[2]?.id]);
  expect((await db.maps.node(scope, v3.id, extended.nodes[0]?.id ?? ""))?.summary?.id).toBe(original?.summary?.id);
  const coverage = await db.maps.coverage(scope, extended.plan.source.id);
  expect(coverage.summarizedBytes).toBe(2048);
  expect(coverage.tailBytes).toBe(1024);
  const limited = await db.maps.search(scope, "Navigation", 1);
  expect(limited.length).toBe(1);
  expect(limited[0]?.coverage.partial).toBe(true);
});

test("only actual serving buys a bounded review, including when artifacts have many bindings", async () => {
  const db = await setup();
  const first = fixture();
  const v1 = await publish(db.maps, first);
  await generate(db, db.maps);
  const view = await db.maps.node(scope, v1.id, first.nodes[0]?.id ?? "");
  const summaryId = view?.summary?.id;
  if (!summaryId) throw new Error("missing generated artifact");
  await db.maps.search(scope, "Navigation", 20);
  await db.maps.refreshWork(policy, NOW, 128);
  expect(await db.maps.offers(policy, NOW)).toEqual([]);
  await db.maps.noteServed({ readId: "read-one", summaryIds: [summaryId], now: NOW });
  await db.maps.noteServed({ readId: "read-one", summaryIds: [summaryId], now: NOW });
  const repeat = fixture(2, "b".repeat(64));
  const v2 = await publish(db.maps, repeat);
  await db.maps.refreshWork(policy, NOW, 128);
  const reviews = await db.maps.offers(policy, NOW);
  expect(reviews.map((item) => [item.mode, item.baseSummaryId])).toEqual([["review", summaryId]]);
  const review = reviews[0];
  if (!review) throw new Error("missing review");
  await settle(db, db.maps, review, { kind: "review", verdict: "correct", reason: "The span has a missing qualification." });
  await db.maps.refreshWork(policy, NOW, 128);
  const correction = (await db.maps.offers(policy, NOW)).find((item) => item.mode === "correct");
  if (!correction) throw new Error("missing correction");
  const correctedId = await settle(db, db.maps, correction, { kind: "summary", text: "Navigation with the qualification preserved." });
  await db.maps.refreshWork(policy, NOW, 128);
  const next = await db.maps.offers(policy, NOW);
  expect(next.some((item) => item.mode === "review" && item.baseSummaryId === correctedId)).toBe(false);
  expect((await db.maps.node(scope, v2.id, repeat.nodes[0]?.id ?? ""))?.summary?.id).toBe(correctedId);
  expect((await db.maps.node(scope, v1.id, first.plan.rootId ?? ""))?.coverage.stale).toBe(true);
  for (const item of next) if (item.mode === "correct") await settle(db, db.maps, item, { kind: "summary", text: "Updated parent navigation." });
  await db.maps.refreshWork(policy, NOW, 128);
  expect((await db.maps.node(scope, v2.id, repeat.plan.rootId ?? ""))?.coverage.stale).toBe(false);
  await db.maps.noteServed({ readId: "read-two", summaryIds: [summaryId], now: LATER });
  await db.maps.refreshWork(policy, LATER, 128);
  expect((await db.maps.offers(policy, LATER)).some((item) => item.baseSummaryId === summaryId && item.mode === "review")).toBe(false);
});

test("refused output is never stored, attempts back off, and false fences write no artifact", async () => {
  const db = await setup();
  const data = fixture(1);
  await publish(db.maps, data);
  await db.maps.refreshWork(policy, NOW);
  const item = (await db.maps.offers(policy, NOW))[0];
  if (!item) throw new Error("missing work");
  const runId = "run-map-attempt";
  await db.maps.startWork(item.id, { id: "claim-map", runId, fence: 1 }, NOW);
  const details = await db.maps.work(item.id);
  if (!details) throw new Error("missing work details");
  await expect(db.maps.settlementStatements({ details, runId, now: NOW, guard: { sql: "1", params: [] },
    result: { kind: "summary", text: `credential: ${"gh" + "p_"}${"a1B2c3D4".repeat(5)}` } })).rejects.toThrow();
  const fenced = await db.maps.settlementStatements({ details, runId, now: NOW, guard: { sql: "0", params: [] }, result: { kind: "summary", text: "Safe navigation." } });
  await db.db.batch(fenced.statements);
  expect((await db.maps.coverage(scope)).summarizedBytes).toBe(0);
  await db.db.batch(await db.maps.failureStatements({ workId: item.id, now: NOW, reason: "raw rejected provider output", guard: { sql: "1", params: [] } }));
  expect(await db.maps.offers(policy, NOW)).toEqual([]);
  const retry = (await db.maps.offers(policy, LATER))[0];
  expect(retry?.attempt).toBe(2);
  if (!retry) throw new Error("missing retry");
  await db.maps.startWork(item.id, { id: "claim-retry", runId: "run-map-retry", fence: 2 }, LATER);
  await db.db.batch(await db.maps.failureStatements({ workId: item.id, now: LATER, reason: "raw rejected provider output", guard: { sql: "1", params: [] } }));
  await db.maps.refreshWork(policy, LATER);
  expect(await db.maps.offers(policy, "2026-09-21T10:00:00.000Z")).toEqual([]);
  const rows = await db.db.query<{ payload: string; reason: string }>("SELECT payload,reason FROM transcript_map_work WHERE id=?", [item.id]);
  expect(rows[0]?.reason).toBe("attempt failed");
  expect(rows[0]?.payload.includes("raw rejected")).toBe(false);
  expect(await db.db.query("SELECT id FROM transcript_map_summaries")).toEqual([]);
});

test("explicit regeneration is idempotent and preserves the previous producing version", async () => {
  const db = await setup();
  const data = fixture(1);
  const original = await publish(db.maps, data);
  await generate(db, db.maps);
  const before = await db.maps.node(scope, original.id, data.plan.rootId ?? "");
  const request = { captureId: data.plan.source.id, requestId: "regenerate-once", reason: "Revisit this capture.", now: LATER };
  expect(await db.maps.regenerate(request)).toBe(1);
  expect(await db.maps.regenerate(request)).toBe(1);
  const replacement = await db.maps.ensureVersion(data.plan.id, policy, LATER);
  expect(replacement.supersedes).toBe(original.id);
  expect(replacement.generation).toBe(1);
  expect((await db.maps.node(scope, original.id, data.plan.rootId ?? ""))?.summary).toEqual(before?.summary);
  expect((await db.maps.node(scope, original.id, data.plan.rootId ?? ""))?.coverage.stale).toBe(true);
  await db.maps.refreshWork(policy, LATER);
  const pending = await db.maps.offers(policy, LATER);
  expect(pending.map((item) => item.versionId)).toEqual([replacement.id]);
  expect((await db.maps.node(scope, replacement.id, data.plan.rootId ?? ""))?.summary).toBeNull();
});

test("oversized records remain explicit gaps instead of becoming paid work or silent coverage", async () => {
  const db = await setup();
  const first = fixture(1);
  const bytes = `${"x".repeat(2047)}\n`;
  const source = { ...first.plan.source, bytes: 2048, sourceDigest: hash(bytes), captureDigest: hash(bytes) };
  const planId = transcriptMapPlanId(source, policy.segmentation);
  const span = { firstRecord: 1, lastRecord: 1, byteOffset: 0, byteLength: 2048, digest: hash(bytes),
    anchor: { line: 1, byteOffset: 0, byteLength: 2048, digest: hash(bytes), time: null } };
  const item = TranscriptMapNodeSchema.parse({ id: transcriptMapNodeId(planId, 0, 0, span, [], "record-too-large"),
    planId, parentId: null, level: 0, ordinal: 0, span, children: [], gap: "record-too-large" });
  const data: MapFixture = { plan: { ...first.plan, id: planId, source, rootId: item.id,
    gapBytes: 2048, digest: transcriptMapManifestDigest([item]) }, nodes: [item], access: first.access };
  const version = await publish(db.maps, data);
  await db.maps.refreshWork(policy, NOW);
  expect(await db.maps.offers(policy, NOW)).toEqual([]);
  const view = await db.maps.node(scope, version.id, item.id);
  expect(view?.node.gap).toBe("record-too-large");
  expect(view?.summary).toBeNull();
  expect(view?.coverage).toEqual({ sourceBytes: 2048, summarizedBytes: 0, directBytes: 0, unmappedBytes: 0,
    gapBytes: 2048, levels: [], partial: true, stale: false, tailBytes: 0 });
  expect((await db.maps.status(scope)).verifiedMappedCaptures).toBe(0);
});

test("read attestations preserve worker inventory progress and planning resumes the first missing page", async () => {
  const db = await setup();
  const data = fixture();
  const source = data.plan.source;
  const capture = { id: source.id, host: source.host, harness: source.harness, session: source.session,
    snapshot: source.snapshot, path: source.path, capturedAt: source.capturedAt };
  await db.maps.recordCatalog({ ...scope, entries: [{ capture, access: data.access }], nextCursor: "inventory-page-two", now: NOW });
  const reading = { ...context, observedAt: LATER, classId: "public", ceiling: 0, eligibleCaptures: 0 };
  await db.maps.recordAccess({ machineId: policy.machineId, context: reading, entries: [], now: LATER });
  await db.maps.recordAccess({ ...scope, context: { ...context, observedAt: LATER }, entries: [], now: LATER });
  expect(await db.maps.catalogState(policy.machineId)).toEqual({ context, nextCursor: "inventory-page-two", completedAt: null });
  expect(await db.maps.nextPlan(policy.machineId, policy.segmentation)).toEqual({ capture, offset: 0 });
  await db.maps.recordPlan({ ...scope, ...data, nodes: data.nodes.slice(0, 1), offset: 0, nextOffset: 1, now: LATER });
  expect(await db.maps.nextPlan(policy.machineId, policy.segmentation)).toEqual({ capture, offset: 1 });
  expect(await db.maps.catalogState(policy.machineId)).toEqual({ context, nextCursor: "inventory-page-two", completedAt: null });
  await db.maps.recordPlan({ ...scope, ...data, nodes: data.nodes.slice(1), offset: 1, nextOffset: null, now: LATER });
  expect(await db.maps.nextPlan(policy.machineId, policy.segmentation)).toBeNull();
  await db.maps.recordCatalog({ ...scope, entries: [], nextCursor: null, now: LATER });
  expect((await db.maps.catalogState(policy.machineId)).completedAt).toBe(LATER);
  await db.maps.refreshWork(policy, LATER);
  const offered = (await db.maps.offers(policy, LATER))[0];
  if (!offered) throw new Error("missing attested work");
  expect((await db.maps.work(offered.id))?.context).toEqual(context);
  await db.maps.recordAccess({ machineId: policy.machineId,
    context: { ...reading, digest: hash("new archive inventory"), observedAt: "2026-09-20T10:03:00.000Z" },
    entries: [], now: "2026-09-20T10:03:00.000Z" });
  expect(await db.maps.catalogState(policy.machineId)).toEqual({ context: null, nextCursor: null, completedAt: null });
  expect((await db.maps.work(offered.id))?.context).toBeNull();
  expect(await db.maps.offers(policy, LATER)).toEqual([]);
});
