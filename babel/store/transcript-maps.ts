import { createHash } from "node:crypto";
import type { PluginDatabase, SqlParam, SqlStatement } from "@manifold/plugin";
import {
  termsQuery,
  TRANSCRIPT_MAP_JOB_PAGE_NODES,
  TRANSCRIPT_MAP_MAX_CAPTURES,
  TranscriptMapAccessSchema,
  TranscriptMapCaptureSchema,
  TranscriptMapContextSchema,
  TranscriptMapModelResultSchema,
  TranscriptMapNodeSchema,
  TranscriptMapPlanSchema,
  TranscriptMapPolicySchema,
  TranscriptMapSegmentationSchema,
  TranscriptMapSummarySchema,
  TranscriptMapVersionSchema,
  TranscriptMapWorkSchema,
  type TranscriptMapAccess,
  type TranscriptMapCatalogEntry,
  type TranscriptMapCapture,
  type TranscriptMapChildSummary,
  type TranscriptMapContext,
  type TranscriptMapCoverage,
  type TranscriptMapModelResult,
  type TranscriptMapNode,
  type TranscriptMapPlan,
  type TranscriptMapPolicy,
  type TranscriptMapSegmentation,
  type TranscriptMapStatus,
  type TranscriptMapSource,
  type TranscriptMapSummary,
  type TranscriptMapVersion,
  type TranscriptMapView,
  type TranscriptMapWork,
} from "../contract.ts";
import { secretScan } from "../machine/preflight.ts";
import {
  transcriptMapCaptureId,
  transcriptMapNodeId,
  transcriptMapPlanId,
} from "../transcript-map-identity.ts";

/** A terminal native proof refusal, unlike an interrupted database projection. */
export class TranscriptMapProjectionRefusal extends Error {}

export interface TranscriptMapStore {
  readonly db: PluginDatabase;
  readonly touch?: () => void;
}
export interface TranscriptMapScope {
  readonly machineId: string;
  /** Obtained from the current native read context, never a caller-selected classification. */
  readonly context: TranscriptMapContext;
  /** A read door's freshly attested selection; never widen to older cached attestations. */
  readonly captureIds?: readonly string[];
}
export interface TranscriptMapCondition {
  readonly sql: string;
  readonly params: readonly SqlParam[];
}
export interface TranscriptMapWorkDetails {
  readonly work: TranscriptMapWork;
  readonly plan: TranscriptMapPlan;
  readonly node: TranscriptMapNode;
  readonly version: TranscriptMapVersion;
  readonly baseSummary: TranscriptMapSummary | null;
  readonly feedback: string | null;
  readonly context: TranscriptMapContext | null;
}
export interface TranscriptMapCatalogState {
  readonly context: TranscriptMapContext | null;
  readonly nextCursor: string | null;
  readonly completedAt: string | null;
}
export interface TranscriptMapNextPlan {
  readonly capture: TranscriptMapCapture;
  readonly offset: number;
}
export interface TranscriptMapAccessInput extends TranscriptMapScope {
  readonly entries: readonly TranscriptMapCatalogEntry[];
  readonly now: string;
  /** Checked at the context mutation boundary, so a late receipt replay cannot rewind progress. */
  readonly guard?: TranscriptMapCondition;
}
export interface TranscriptMapCatalogInput extends TranscriptMapScope {
  readonly entries: readonly TranscriptMapCatalogEntry[];
  readonly nextCursor: string | null;
  readonly now: string;
  readonly guard?: TranscriptMapCondition;
}
export interface TranscriptMapPlanInput extends TranscriptMapScope {
  readonly access: TranscriptMapAccess;
  readonly plan: TranscriptMapPlan;
  readonly nodes: readonly TranscriptMapNode[];
  readonly offset: number;
  readonly nextOffset: number | null;
  readonly now: string;
  readonly guard?: TranscriptMapCondition;
}
export interface TranscriptMapSettlementInput {
  readonly details: TranscriptMapWorkDetails;
  readonly result: TranscriptMapModelResult;
  readonly runId: string;
  readonly now: string;
  readonly guard: TranscriptMapCondition;
}
export interface TranscriptMaps {
  recordCatalog(input: TranscriptMapCatalogInput): Promise<void>;
  recordAccess(input: TranscriptMapAccessInput): Promise<void>;
  catalogState(machineId: string): Promise<TranscriptMapCatalogState>;
  nextPlan(
    machineId: string,
    segmentation: TranscriptMapSegmentation,
    afterCaptureId?: string | null,
  ): Promise<TranscriptMapNextPlan | null>;
  recordPlan(input: TranscriptMapPlanInput): Promise<{ complete: boolean }>;
  ensureVersion(
    planId: string,
    policy: TranscriptMapPolicy,
    now: string,
    generation?: number,
  ): Promise<TranscriptMapVersion>;
  refreshWork(policy: TranscriptMapPolicy, now: string, limit?: number): Promise<number>;
  offers(policy: TranscriptMapPolicy, now: string, limit?: number): Promise<TranscriptMapWork[]>;
  work(id: string): Promise<TranscriptMapWorkDetails | null>;
  startWork(
    id: string,
    claim: { id: string; runId: string; fence: number },
    now: string,
  ): Promise<boolean>;
  settlementStatements(
    input: TranscriptMapSettlementInput,
  ): Promise<{ statements: SqlStatement[]; summaryId: string | null }>;
  failureStatements(input: {
    workId: string;
    now: string;
    guard: TranscriptMapCondition;
    reason: string;
  }): Promise<SqlStatement[]>;
  candidates(
    machineId: string,
    input: { query?: string; captureId?: string; versionId?: string; nodeId?: string },
    limit?: number,
  ): Promise<TranscriptMapCapture[]>;
  reference(
    machineId: string,
    versionId: string,
    nodeId: string,
  ): Promise<{ source: TranscriptMapSource; node: TranscriptMapNode } | null>;
  search(scope: TranscriptMapScope, query: string, limit: number): Promise<TranscriptMapView[]>;
  node(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
  ): Promise<TranscriptMapView | null>;
  children(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
  ): Promise<TranscriptMapView[]>;
  ancestors(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
  ): Promise<TranscriptMapView[]>;
  coverage(scope: TranscriptMapScope, captureId?: string): Promise<TranscriptMapCoverage>;
  status(scope: TranscriptMapScope): Promise<TranscriptMapStatus>;
  noteServed(input: { readId: string; summaryIds: readonly string[]; now: string }): Promise<void>;
  regenerate(input: {
    captureId: string;
    requestId: string;
    reason: string;
    now: string;
  }): Promise<number>;
}

// Deliberately smaller than both the engine result and batch budgets. A node can carry 64 IDs;
// a version can carry two 64 KiB recipes. Never return a page of joined recipe payloads.
const PAGE = 32;
const READ_LIMIT = 64;
const MAX_SCAN = 128;
const json = JSON.stringify;
const digest = (value: unknown): string =>
  `sha256:${createHash("sha256").update(json(value)).digest("hex")}`;
const identity = (prefix: string, value: unknown): string => `${prefix}_${digest(value).slice(7)}`;
const bound = (limit: number, max = READ_LIMIT): number =>
  Math.max(1, Math.min(max, Math.floor(Number.isFinite(limit) ? limit : 1)));
function safeText(text: string): void {
  const scan = secretScan();
  scan.redact(JSON.stringify(text), 1);
  if (scan.report().redactions !== 0)
    throw new Error("transcript map prose refused by secret preflight");
}
function recipes(policy: TranscriptMapPolicy) {
  const generate = policy.recipes.find(
    (recipe) => recipe.id === policy.generateRecipe && recipe.enabled !== false,
  );
  const review = policy.recipes.find(
    (recipe) => recipe.id === policy.reviewRecipe && recipe.enabled !== false,
  );
  if (!generate || !review) throw new Error("transcript map recipes are not enabled");
  return { generate, review };
}
function contract(policy: TranscriptMapPolicy): string {
  const selected = recipes(policy);
  return digest([policy.profile, selected.generate, selected.review, policy.segmentation]);
}
function emptyCoverage(): TranscriptMapCoverage {
  return {
    sourceBytes: 0,
    summarizedBytes: 0,
    directBytes: 0,
    unmappedBytes: 0,
    gapBytes: 0,
    levels: [],
    partial: false,
    stale: false,
    tailBytes: 0,
  };
}

export function transcriptMaps(store: TranscriptMapStore): TranscriptMaps {
  const db = store.db;
  async function one<T>(table: string, id: string): Promise<T | null> {
    const rows = await db.query<{ payload: string }>(`SELECT payload FROM ${table} WHERE id=?`, [
      id,
    ]);
    return rows[0] ? (JSON.parse(rows[0].payload) as T) : null;
  }
  async function batch(statements: readonly SqlStatement[]): Promise<void> {
    for (let start = 0; start < statements.length; start += PAGE)
      await db.batch(statements.slice(start, start + PAGE));
    if (statements.length) store.touch?.();
  }
  function authorization(scope: TranscriptMapScope, captureColumn: string): TranscriptMapCondition {
    const context = TranscriptMapContextSchema.parse(scope.context);
    return {
      sql: `EXISTS (SELECT 1 FROM transcript_map_access a JOIN transcript_map_contexts c
        ON c.machine_id=a.machine_id AND c.digest=a.context_digest
        WHERE a.machine_id=? AND a.capture_id=${captureColumn} AND a.context_digest=? AND a.sensitivity<=?)
        AND NOT EXISTS (SELECT 1 FROM transcript_map_contexts c WHERE c.machine_id=?
          AND c.observed_at>? AND c.digest!=?)
        ${scope.captureIds === undefined ? "" : `AND ${captureColumn} IN (SELECT value FROM json_each(?))`}`,
      params: [
        scope.machineId,
        context.digest,
        context.ceiling,
        scope.machineId,
        context.observedAt,
        context.digest,
        ...(scope.captureIds === undefined ? [] : [json(scope.captureIds)]),
      ],
    };
  }
  async function rememberContext(
    scope: TranscriptMapScope,
    nextCursor: string | null,
    preserveCursor: boolean,
    mapping: boolean,
    now: string,
    guard?: TranscriptMapCondition,
  ): Promise<void> {
    const context = TranscriptMapContextSchema.parse(scope.context);
    const fresh = `NOT EXISTS (SELECT 1 FROM transcript_map_contexts newer
      WHERE newer.machine_id=? AND newer.observed_at>? AND newer.digest!=?)`;
    const freshness = [scope.machineId, context.observedAt, context.digest];
    const result = await db.batch([
      {
        sql: `DELETE FROM transcript_map_contexts WHERE machine_id=? AND digest!=? AND observed_at<=?
          AND (${guard?.sql ?? "1"}) AND ${fresh}`,
        params: [scope.machineId, context.digest, context.observedAt, ...(guard?.params ?? []), ...freshness],
      },
      {
        sql: `INSERT INTO transcript_map_contexts(machine_id,class_id,digest,ceiling,observed_at,payload,next_cursor,cataloged_at,completed_at,mapping,mapping_payload)
        SELECT ?,?,?,?,?,?,?,?,?,?,? WHERE (${guard?.sql ?? "1"}) AND ${fresh}
        ON CONFLICT(machine_id,class_id) DO UPDATE SET digest=excluded.digest,
        ceiling=excluded.ceiling,observed_at=max(observed_at,excluded.observed_at),
        payload=CASE WHEN excluded.observed_at>=observed_at THEN excluded.payload ELSE payload END,
        next_cursor=CASE WHEN ? THEN transcript_map_contexts.next_cursor ELSE excluded.next_cursor END,
        cataloged_at=coalesce(excluded.cataloged_at,transcript_map_contexts.cataloged_at),
        completed_at=CASE WHEN ? THEN transcript_map_contexts.completed_at ELSE excluded.completed_at END,
        mapping=max(transcript_map_contexts.mapping,excluded.mapping),
        mapping_payload=coalesce(excluded.mapping_payload,transcript_map_contexts.mapping_payload)
        WHERE excluded.observed_at>=transcript_map_contexts.observed_at OR excluded.digest=transcript_map_contexts.digest`,
        params: [
          scope.machineId,
          context.classId,
          context.digest,
          context.ceiling,
          context.observedAt,
          json(context),
          nextCursor,
          preserveCursor ? null : now,
          !preserveCursor && nextCursor === null ? now : null,
          mapping ? 1 : 0,
          mapping ? json(context) : null,
          ...(guard?.params ?? []),
          ...freshness,
          preserveCursor ? 1 : 0,
          preserveCursor ? 1 : 0,
        ],
      },
      {
        sql: `SELECT 1 AS refused WHERE (${guard?.sql ?? "1"}) AND NOT (${fresh})`,
        params: [...(guard?.params ?? []), ...freshness],
      },
    ]);
    if (result[2]?.length) throw new TranscriptMapProjectionRefusal("stale transcript map context");
  }
  async function recordCatalog(
    input: TranscriptMapCatalogInput,
    preserveCursor = false,
    mapping = true,
  ): Promise<void> {
    if (input.entries.length > TRANSCRIPT_MAP_MAX_CAPTURES)
      throw new TranscriptMapProjectionRefusal("transcript catalog page exceeds bound");
    await rememberContext(input, input.nextCursor, preserveCursor, mapping, input.now, input.guard);
    const statements: SqlStatement[] = [];
    for (const entry of input.entries) {
      const capture = TranscriptMapCaptureSchema.parse(entry.capture);
      const access = TranscriptMapAccessSchema.parse(entry.access);
      if (
        capture.id !== transcriptMapCaptureId(capture) ||
        access.captureId !== capture.id ||
        access.contextDigest !== input.context.digest ||
        access.sensitivity > input.context.ceiling
      )
        throw new TranscriptMapProjectionRefusal("invalid transcript map access attestation");
      const previous = await one("transcript_map_captures", capture.id);
      if (previous && json(previous) !== json(capture)) throw new TranscriptMapProjectionRefusal("capture identity changed");
      statements.push({
        sql: `INSERT OR IGNORE INTO transcript_map_captures(id,host,harness,session,captured_at,payload) VALUES(?,?,?,?,?,?)`,
        params: [
          capture.id,
          capture.host,
          capture.harness,
          capture.session,
          capture.capturedAt,
          json(capture),
        ],
      });
      statements.push({
        sql: `INSERT INTO transcript_map_access(machine_id,capture_id,context_digest,sensitivity) VALUES(?,?,?,?)
        ON CONFLICT(machine_id,capture_id,context_digest) DO UPDATE SET sensitivity=max(sensitivity,excluded.sensitivity)`,
        params: [input.machineId, capture.id, access.contextDigest, access.sensitivity],
      });
    }
    await batch(statements);
  }
  async function recordAccess(input: TranscriptMapAccessInput): Promise<void> {
    await recordCatalog({ ...input, nextCursor: null }, true, false);
  }
  async function catalogState(machineId: string): Promise<TranscriptMapCatalogState> {
    const rows = await db.query<{
      payload: string;
      next_cursor: string | null;
      completed_at: string | null;
    }>(
      `SELECT mapping_payload payload,next_cursor,completed_at FROM transcript_map_contexts WHERE machine_id=? AND mapping=1
       ORDER BY cataloged_at DESC,observed_at DESC,class_id LIMIT 1`,
      [machineId],
    );
    return {
      context: rows[0] ? TranscriptMapContextSchema.parse(JSON.parse(rows[0].payload)) : null,
      nextCursor: rows[0]?.next_cursor ?? null,
      completedAt: rows[0]?.completed_at ?? null,
    };
  }
  async function nextPlan(
    machineId: string,
    segmentation: TranscriptMapSegmentation,
    afterCaptureId?: string | null,
  ): Promise<TranscriptMapNextPlan | null> {
    const state = await catalogState(machineId);
    if (!state.context) return null;
    const structural = json(TranscriptMapSegmentationSchema.parse(segmentation));
    const auth = authorization({ machineId, context: state.context }, "c.id");
    const rows = await db.query<{ payload: string; next_offset: number }>(
      `SELECT c.payload,
      CASE WHEN EXISTS (SELECT 1 FROM transcript_map_nodes n0 WHERE n0.plan_id=p.id AND n0.position=0)
        THEN (SELECT min(n.position+1) FROM transcript_map_nodes n WHERE n.plan_id=p.id
          AND NOT EXISTS (SELECT 1 FROM transcript_map_nodes nn WHERE nn.plan_id=p.id AND nn.position=n.position+1))
        ELSE 0 END next_offset
      FROM transcript_map_captures c LEFT JOIN transcript_map_plans p ON p.rowid=
        (SELECT max(candidate.rowid) FROM transcript_map_plans candidate WHERE candidate.capture_id=c.id AND json_extract(candidate.payload,'$.segmentation')=json(?))
      WHERE ${auth.sql} AND NOT EXISTS (SELECT 1 FROM transcript_map_plans ready
        WHERE ready.capture_id=c.id AND ready.complete=1 AND json_extract(ready.payload,'$.segmentation')=json(?))
      AND (? IS NULL OR (julianday(c.captured_at),c.id) >
        (SELECT julianday(prior.captured_at),prior.id FROM transcript_map_captures prior WHERE prior.id=?))
      ORDER BY julianday(c.captured_at),c.id LIMIT 1`,
      [structural, ...auth.params, structural, afterCaptureId ?? null, afterCaptureId ?? null],
    );
    return rows[0]
      ? {
          capture: TranscriptMapCaptureSchema.parse(JSON.parse(rows[0].payload)),
          offset: Number(rows[0].next_offset),
        }
      : null;
  }
  async function recordPlan(input: TranscriptMapPlanInput): Promise<{ complete: boolean }> {
    const plan = TranscriptMapPlanSchema.parse(input.plan);
    if (plan.id !== transcriptMapPlanId(plan.source, plan.segmentation))
      throw new TranscriptMapProjectionRefusal("invalid transcript plan identity");
    const {
      coordinates: _coordinates,
      captureDigest: _captureDigest,
      sourceDigest: _sourceDigest,
      bytes: _bytes,
      records: _records,
      ...capture
    } = plan.source;
    await recordCatalog(
      { ...input, entries: [{ capture, access: input.access }], nextCursor: null },
      true,
    );
    const old = await one<TranscriptMapPlan>("transcript_map_plans", plan.id);
    if (old && json(old) !== json(plan)) throw new TranscriptMapProjectionRefusal("immutable transcript plan changed");
    if (
      !Number.isSafeInteger(input.offset) ||
      input.offset < 0 ||
      input.nodes.length > TRANSCRIPT_MAP_JOB_PAGE_NODES ||
      input.offset + input.nodes.length > plan.nodeCount ||
      (input.nextOffset === null
        ? input.offset + input.nodes.length !== plan.nodeCount
        : input.nextOffset !== input.offset + input.nodes.length ||
          input.nextOffset >= plan.nodeCount ||
          !input.nodes.length)
    )
      throw new TranscriptMapProjectionRefusal("invalid transcript plan page");
    await db.run(
      `INSERT OR IGNORE INTO transcript_map_plans(id,capture_id,payload,created_at) VALUES(?,?,?,?)`,
      [plan.id, plan.source.id, json(plan), input.now],
    );
    const statements: SqlStatement[] = [];
    for (let index = 0; index < input.nodes.length; index += 1) {
      const node = TranscriptMapNodeSchema.parse(input.nodes[index]);
      if (
        node.planId !== plan.id ||
        node.level >= plan.segmentation.maxDepth ||
        node.id !==
          transcriptMapNodeId(
            plan.id,
            node.level,
            node.ordinal,
            node.span,
            node.children,
            node.gap,
          ) ||
        node.span.byteOffset + node.span.byteLength > plan.source.bytes ||
        node.span.lastRecord > plan.source.records ||
        (node.gap !== null && node.children.length !== 0) ||
        (node.children.length === 0 &&
          node.gap === null &&
          node.span.byteLength > plan.segmentation.leafBytes) ||
        (node.gap === "record-too-large" &&
          (node.span.firstRecord !== node.span.lastRecord ||
            node.span.byteLength <= plan.segmentation.leafBytes)) ||
        node.children.length > plan.segmentation.fanout
      )
        throw new TranscriptMapProjectionRefusal("invalid transcript map node");
      statements.push({
        sql: `INSERT OR IGNORE INTO transcript_map_nodes(id,plan_id,position,parent_node_id,level,ordinal,byte_offset,byte_length,gap,payload) VALUES(?,?,?,?,?,?,?,?,?,?)`,
        params: [
          node.id,
          plan.id,
          input.offset + index,
          node.parentId,
          node.level,
          node.ordinal,
          node.span.byteOffset,
          node.span.byteLength,
          node.gap,
          json(node),
        ],
      });
    }
    await batch(statements);
    const count = await db.query<{ n: number }>(
      `SELECT count(*) n FROM transcript_map_nodes WHERE plan_id=?`,
      [plan.id],
    );
    if (Number(count[0]?.n) !== plan.nodeCount) return { complete: false };
    const published = await db.query<{ complete: number }>(
      `SELECT complete FROM transcript_map_plans WHERE id=?`,
      [plan.id],
    );
    if (Number(published[0]?.complete) === 1) return { complete: true };
    await verifyPlan(plan);
    await db.run(`UPDATE transcript_map_plans SET complete=1 WHERE id=?`, [plan.id]);
    store.touch?.();
    return { complete: true };
  }
  async function verifyPlan(plan: TranscriptMapPlan): Promise<void> {
    const hash = createHash("sha256").update("[");
    let position = 0;
    let roots = 0;
    let gaps = 0;
    let leafEnd = 0;
    let recordEnd = 0;
    let previousLevel = -1;
    let previousOrdinal = -1;
    while (position < plan.nodeCount) {
      const rows = await db.query<{ payload: string; position: number }>(
        `SELECT payload,position FROM transcript_map_nodes WHERE plan_id=? AND position>=? ORDER BY position LIMIT ?`,
        [plan.id, position, PAGE],
      );
      if (!rows.length) throw new TranscriptMapProjectionRefusal("missing transcript plan page");
      for (const row of rows) {
        const node = TranscriptMapNodeSchema.parse(JSON.parse(row.payload));
        if (
          Number(row.position) !== position ||
          node.level < previousLevel ||
          node.ordinal !== (node.level === previousLevel ? previousOrdinal + 1 : 0)
        )
          throw new TranscriptMapProjectionRefusal("unordered transcript manifest");
        previousLevel = node.level;
        previousOrdinal = node.ordinal;
        hash.update(position === 0 ? "" : ",").update(json(node));
        position += 1;
        if (node.parentId === null) {
          roots += 1;
          if (
            node.id !== plan.rootId ||
            node.span.byteOffset !== 0 ||
            node.span.byteLength !== plan.source.bytes ||
            node.span.firstRecord !== 1 ||
            node.span.lastRecord !== plan.source.records ||
            node.span.digest !== plan.source.sourceDigest
          )
            throw new TranscriptMapProjectionRefusal("invalid transcript root");
        } else {
          const parent = await one<TranscriptMapNode>("transcript_map_nodes", node.parentId);
          if (
            !parent ||
            parent.planId !== plan.id ||
            parent.level <= node.level ||
            !parent.children.includes(node.id)
          )
            throw new TranscriptMapProjectionRefusal("invalid transcript parent link");
        }
        if (!node.children.length) {
          // All terminal spans (including explicit depth gaps) are checked separately by offset below.
          gaps += node.gap === null ? 0 : node.span.byteLength;
        } else {
          let byteEnd = node.span.byteOffset;
          let lastRecord = node.span.firstRecord - 1;
          for (const id of node.children) {
            const child = await one<TranscriptMapNode>("transcript_map_nodes", id);
            if (
              !child ||
              child.parentId !== node.id ||
              child.planId !== plan.id ||
              child.level >= node.level ||
              child.span.byteOffset !== byteEnd ||
              child.span.firstRecord !== lastRecord + 1
            )
              throw new TranscriptMapProjectionRefusal("noncontiguous transcript child span");
            byteEnd += child.span.byteLength;
            lastRecord = child.span.lastRecord;
          }
          if (
            byteEnd !== node.span.byteOffset + node.span.byteLength ||
            lastRecord !== node.span.lastRecord
          )
            throw new TranscriptMapProjectionRefusal("transcript children do not cover parent");
        }
      }
    }
    let after = -1;
    for (;;) {
      const leaves = await db.query<{ payload: string; byte_offset: number }>(
        `SELECT payload,byte_offset FROM transcript_map_nodes WHERE plan_id=? AND json_array_length(json_extract(payload,'$.children'))=0 AND byte_offset>? ORDER BY byte_offset LIMIT ?`,
        [plan.id, after, PAGE],
      );
      for (const row of leaves) {
        const node = TranscriptMapNodeSchema.parse(JSON.parse(row.payload));
        if (node.span.byteOffset !== leafEnd || node.span.firstRecord !== recordEnd + 1)
          throw new TranscriptMapProjectionRefusal("transcript terminal spans overlap or omit records");
        leafEnd += node.span.byteLength;
        recordEnd = node.span.lastRecord;
        after = Number(row.byte_offset);
      }
      if (leaves.length < PAGE) break;
    }
    if (
      `sha256:${hash.update("]").digest("hex")}` !== plan.digest ||
      gaps !== plan.gapBytes ||
      roots !== (plan.source.bytes === 0 ? 0 : 1) ||
      leafEnd !== plan.source.bytes ||
      recordEnd !== plan.source.records ||
      (plan.rootId === null) !== (plan.source.bytes === 0) ||
      plan.direct !== plan.source.bytes <= plan.segmentation.directBytes
    )
      throw new TranscriptMapProjectionRefusal("transcript plan manifest verification failed");
  }
  async function ensureVersion(
    planId: string,
    raw: TranscriptMapPolicy,
    now: string,
    generation?: number,
  ): Promise<TranscriptMapVersion> {
    const policy = TranscriptMapPolicySchema.parse(raw);
    const plan = await one<TranscriptMapPlan>("transcript_map_plans", planId);
    if (!plan) throw new Error("unknown transcript map plan");
    const complete = await db.query<{ complete: number }>(
      `SELECT complete FROM transcript_map_plans WHERE id=?`,
      [planId],
    );
    if (
      Number(complete[0]?.complete) !== 1 ||
      json(plan.segmentation) !== json(policy.segmentation)
    )
      throw new Error("transcript plan is incomplete or uses a different segmentation");
    const requests = await db.query<{ generation: number }>(
      `SELECT coalesce(max(generation),0) generation FROM transcript_map_regenerations WHERE capture_id=?`,
      [plan.source.id],
    );
    const gen = generation ?? Number(requests[0]?.generation ?? 0);
    if (!Number.isSafeInteger(gen) || gen < 0 || gen > Number(requests[0]?.generation ?? 0))
      throw new Error("generation requires an explicit regeneration request");
    const key = contract(policy);
    const id = identity("tmver", [policy.machineId, planId, key, gen]);
    const held = await one<TranscriptMapVersion>("transcript_map_versions", id);
    if (held) {
      if (gen === Number(requests[0]?.generation ?? 0))
        await db.run(
          `INSERT INTO transcript_map_heads(machine_id,plan_id,version_id)
        VALUES(?,?,?) ON CONFLICT(machine_id,plan_id) DO UPDATE SET version_id=excluded.version_id`,
          [policy.machineId, planId, held.id],
        );
      return held;
    }
    if (gen !== Number(requests[0]?.generation ?? 0))
      throw new Error("cannot create an obsolete generation");
    const heads = await db.query<{ version_id: string }>(
      `SELECT version_id FROM transcript_map_heads WHERE machine_id=? AND plan_id=?`,
      [policy.machineId, planId],
    );
    const selected = recipes(policy);
    const version = TranscriptMapVersionSchema.parse({
      id,
      planId,
      contractDigest: key,
      profile: policy.profile,
      generateRecipe: selected.generate,
      reviewRecipe: selected.review,
      generation: gen,
      supersedes: heads[0]?.version_id ?? null,
      createdAt: now,
    });
    await db.batch([
      {
        sql: `INSERT OR IGNORE INTO transcript_map_versions(id,plan_id,machine_id,contract_digest,generation,payload,policy,created_at) VALUES(?,?,?,?,?,?,?,?)`,
        params: [id, planId, policy.machineId, key, gen, json(version), json(policy), now],
      },
      {
        sql: `INSERT INTO transcript_map_heads(machine_id,plan_id,version_id) VALUES(?,?,?) ON CONFLICT(machine_id,plan_id) DO UPDATE SET version_id=excluded.version_id`,
        params: [policy.machineId, planId, id],
      },
    ]);
    store.touch?.();
    return version;
  }

  async function boundSummary(
    versionId: string,
    nodeId: string,
    includeRejected = false,
  ): Promise<TranscriptMapSummary | null> {
    const rows = await db.query<{ payload: string }>(
      `SELECT s.payload FROM transcript_map_bindings b
      JOIN transcript_map_summaries s ON s.id=b.summary_id WHERE b.version_id=? AND b.node_id=?
      ${includeRejected ? "" : "AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=s.id AND r.verdict IN ('correct','reject'))"}`,
      [versionId, nodeId],
    );
    return rows[0] ? TranscriptMapSummarySchema.parse(JSON.parse(rows[0].payload)) : null;
  }
  async function childInputs(
    versionId: string,
    parent: TranscriptMapNode,
  ): Promise<TranscriptMapChildSummary[] | null> {
    const inputs: TranscriptMapChildSummary[] = [];
    for (const id of parent.children) {
      const child = await one<TranscriptMapNode>("transcript_map_nodes", id);
      if (!child) return null;
      const summary = child.gap === null ? await boundSummary(versionId, id) : null;
      if (child.gap === null && summary === null) return null;
      inputs.push({
        nodeId: id,
        summaryId: summary?.id ?? null,
        text: summary?.text ?? null,
        gap: child.gap,
      });
    }
    return inputs;
  }
  function inputKey(
    plan: TranscriptMapPlan,
    item: TranscriptMapNode,
    version: TranscriptMapVersion,
    inputs: readonly TranscriptMapChildSummary[],
  ): string {
    return digest([
      plan.source.host,
      plan.source.harness,
      plan.source.session,
      version.contractDigest,
      version.generation === 0 ? null : [plan.source.id, version.generation],
      item.level,
      item.span.digest,
      item.span.byteLength,
      item.span.lastRecord - item.span.firstRecord + 1,
      inputs.map((child) => [child.summaryId, child.gap]),
    ]);
  }
  const liveAccess = `EXISTS (SELECT 1 FROM transcript_map_access a JOIN transcript_map_contexts c
    ON c.machine_id=a.machine_id AND c.digest=a.context_digest
    WHERE a.capture_id=p.capture_id AND a.machine_id=v.machine_id AND a.sensitivity<=c.ceiling AND c.mapping=1
      AND NOT EXISTS (SELECT 1 FROM transcript_map_contexts newer WHERE newer.machine_id=c.machine_id
        AND newer.observed_at>c.observed_at AND newer.digest!=c.digest))`;
  async function work(id: string): Promise<TranscriptMapWorkDetails | null> {
    const queued = await one<TranscriptMapWork>("transcript_map_work", id);
    if (!queued) return null;
    const version = await one<TranscriptMapVersion>("transcript_map_versions", queued.versionId);
    const item = await one<TranscriptMapNode>("transcript_map_nodes", queued.nodeId);
    const plan = version
      ? await one<TranscriptMapPlan>("transcript_map_plans", version.planId)
      : null;
    if (!version || !item || !plan) return null;
    const baseSummary = queued.baseSummaryId
      ? await one<TranscriptMapSummary>("transcript_map_summaries", queued.baseSummaryId)
      : null;
    const feedback =
      queued.mode === "correct" && queued.baseSummaryId
        ? await db.query<{ reason: string }>(
            `SELECT reason FROM transcript_map_reviews WHERE summary_id=? AND verdict IN ('correct','reject') ORDER BY created_at DESC,work_id DESC LIMIT 1`,
            [queued.baseSummaryId],
          )
        : [];
    const contexts = await db.query<{ payload: string }>(
      `SELECT c.mapping_payload payload FROM transcript_map_contexts c
      JOIN transcript_map_access a ON a.machine_id=c.machine_id AND a.context_digest=c.digest
      JOIN transcript_map_versions v ON v.machine_id=c.machine_id
      WHERE v.id=? AND a.capture_id=? AND a.sensitivity<=c.ceiling AND c.mapping=1
      ORDER BY c.cataloged_at DESC,c.observed_at DESC,c.class_id LIMIT 1`,
      [version.id, plan.source.id],
    );
    return {
      work: TranscriptMapWorkSchema.parse(queued),
      plan,
      node: item,
      version,
      baseSummary,
      feedback: feedback[0]?.reason ?? null,
      context: contexts[0]
        ? TranscriptMapContextSchema.parse(JSON.parse(contexts[0].payload))
        : null,
    };
  }
  function readiness(details: TranscriptMapWorkDetails): TranscriptMapCondition {
    const item = details.work;
    const sql: string[] = [
      `EXISTS (SELECT 1 FROM transcript_map_versions v JOIN transcript_map_plans p ON p.id=v.plan_id
        JOIN transcript_map_heads h ON h.version_id=v.id AND h.machine_id=v.machine_id
        WHERE v.id=? AND p.complete=1 AND ${liveAccess}
        AND v.generation=(SELECT coalesce(max(generation),0) FROM transcript_map_regenerations WHERE capture_id=p.capture_id))`,
      item.baseSummaryId === null
        ? `NOT EXISTS (SELECT 1 FROM transcript_map_bindings WHERE version_id=? AND node_id=?)`
        : `EXISTS (SELECT 1 FROM transcript_map_bindings WHERE version_id=? AND node_id=? AND summary_id=?)`,
    ];
    const params: SqlParam[] = [item.versionId, item.versionId, details.node.id];
    if (item.baseSummaryId !== null) params.push(item.baseSummaryId);
    for (const child of item.children) {
      if (child.summaryId === null) continue;
      sql.push(`EXISTS (SELECT 1 FROM transcript_map_bindings b WHERE b.version_id=? AND b.node_id=? AND b.summary_id=?
        AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject')))`);
      params.push(item.versionId, child.nodeId, child.summaryId);
    }
    if (item.mode === "review") {
      sql.push(`EXISTS (SELECT 1 FROM transcript_map_served WHERE summary_id=?)`);
      params.push(item.baseSummaryId);
    }
    return { sql: sql.join(" AND "), params };
  }
  async function eligible(
    details: TranscriptMapWorkDetails,
    policy: TranscriptMapPolicy,
  ): Promise<boolean> {
    if (
      details.version.contractDigest !== contract(policy) ||
      details.node.gap !== null ||
      details.plan.direct ||
      details.work.attempt > policy.maxAttempts ||
      details.work.correctionDepth > policy.maxCorrections
    )
      return false;
    if (details.work.mode === "review") {
      const count = await db.query<{ n: number }>(
        `SELECT count(*) n FROM transcript_map_work
        WHERE mode='review' AND base_summary_id=? AND rowid<=(SELECT rowid FROM transcript_map_work WHERE id=?)`,
        [details.work.baseSummaryId, details.work.id],
      );
      if (Number(count[0]?.n ?? 0) > policy.maxReviews || policy.maxReviews === 0) return false;
    }
    const inputs = await childInputs(details.version.id, details.node);
    if (inputs === null || json(inputs) !== json(details.work.children)) return false;
    const check = readiness(details);
    const rows = await db.query<{ valid: number }>(`SELECT (${check.sql}) valid`, check.params);
    return Number(rows[0]?.valid) === 1;
  }
  async function queue(
    details: Pick<TranscriptMapWorkDetails, "plan" | "node" | "version">,
    policy: TranscriptMapPolicy,
    mode: TranscriptMapWork["mode"],
    base: TranscriptMapSummary | null,
    inputs: TranscriptMapChildSummary[],
    depth: number,
    key: string,
    servedSeq: number,
    now: string,
  ): Promise<number> {
    const id = identity("tmwork", [
      details.version.id,
      details.node.id,
      mode,
      base?.id ?? null,
      key,
      servedSeq,
    ]);
    const item = TranscriptMapWorkSchema.parse({
      id,
      versionId: details.version.id,
      nodeId: details.node.id,
      mode,
      baseSummaryId: base?.id ?? null,
      children: inputs,
      correctionDepth: depth,
      attempt: 1,
      createdAt: now,
    });
    let unique =
      mode === "generate"
        ? `NOT EXISTS (SELECT 1 FROM transcript_map_work WHERE input_key=? AND mode='generate'
          AND (state!='obsolete' OR attempt>1))`
        : `NOT EXISTS (SELECT 1 FROM transcript_map_work
          WHERE base_summary_id=? AND mode=? AND state IN ('queued','running'))`;
    const params: SqlParam[] = [
      id,
      item.versionId,
      item.nodeId,
      mode,
      item.baseSummaryId,
      now,
      now,
      json(item),
      key,
      servedSeq,
    ];
    if (mode === "generate") params.push(key);
    else params.push(item.baseSummaryId, mode);
    if (mode === "review") {
      unique += ` AND (SELECT count(*) FROM transcript_map_work WHERE mode='review' AND base_summary_id=?)<?
        AND (SELECT coalesce(max(served_seq),0) FROM transcript_map_work WHERE mode='review' AND base_summary_id=?)<?`;
      params.push(item.baseSummaryId, policy.maxReviews, item.baseSummaryId, servedSeq);
    }
    if (mode === "correct") {
      unique += ` AND (SELECT count(*) FROM transcript_map_work WHERE mode='correct' AND base_summary_id=?)<?
        AND NOT EXISTS (SELECT 1 FROM transcript_map_work WHERE mode='correct' AND base_summary_id=? AND input_key=?)`;
      params.push(item.baseSummaryId, policy.maxCorrections, item.baseSummaryId, key);
    }
    const result = await db.run(
      `INSERT OR IGNORE INTO transcript_map_work(id,version_id,node_id,mode,base_summary_id,
      attempt,state,ready_at,created_at,payload,input_key,served_seq) SELECT ?,?,?,?,?,1,'queued',?,?,?,?,? WHERE ${unique}`,
      params,
    );
    return result.changes;
  }
  async function refreshWork(raw: TranscriptMapPolicy, now: string, limit = 32): Promise<number> {
    const policy = TranscriptMapPolicySchema.parse(raw);
    const key = contract(policy);
    const cap = bound(limit, MAX_SCAN);
    const plans = await db.query<{ id: string }>(
      `SELECT p.id FROM transcript_map_plans p
      WHERE p.complete=1 AND EXISTS (SELECT 1 FROM transcript_map_access a WHERE a.capture_id=p.capture_id AND a.machine_id=?)
      AND json_extract(p.payload,'$.segmentation')=json(?)
      AND NOT EXISTS (SELECT 1 FROM transcript_map_heads h JOIN transcript_map_versions v ON v.id=h.version_id
        WHERE h.plan_id=p.id AND h.machine_id=? AND v.contract_digest=?
          AND v.generation=(SELECT coalesce(max(generation),0) FROM transcript_map_regenerations WHERE capture_id=p.capture_id))
      ORDER BY p.created_at,p.id LIMIT ?`,
      [policy.machineId, json(policy.segmentation), policy.machineId, key, Math.min(cap, 8)],
    );
    for (const row of plans) await ensureVersion(row.id, policy, now);
    const cursor = await db.query<{ version_cursor: number; node_cursor: number }>(
      `SELECT version_cursor,node_cursor FROM transcript_map_scan WHERE machine_id=? AND contract_digest=?`,
      [policy.machineId, key],
    );
    const rows = await db.query<{ version_id: string; node_id: string; vr: number; nr: number }>(
      `SELECT v.id version_id,n.id node_id,v.rowid vr,n.rowid nr FROM transcript_map_heads h
       JOIN transcript_map_versions v ON v.id=h.version_id JOIN transcript_map_plans p ON p.id=v.plan_id
       JOIN transcript_map_nodes n ON n.plan_id=p.id
       WHERE h.machine_id=? AND v.contract_digest=? AND p.complete=1 AND ${liveAccess}
       AND (v.rowid,n.rowid)>(?,?) ORDER BY v.rowid,n.rowid LIMIT ?`,
      [policy.machineId, key, cursor[0]?.version_cursor ?? 0, cursor[0]?.node_cursor ?? 0, cap],
    );
    let added = 0;
    for (const row of rows) {
      const version = await one<TranscriptMapVersion>("transcript_map_versions", row.version_id);
      const item = await one<TranscriptMapNode>("transcript_map_nodes", row.node_id);
      const plan = version
        ? await one<TranscriptMapPlan>("transcript_map_plans", version.planId)
        : null;
      if (!version || !item || !plan || plan.direct || item.gap !== null) continue;
      const inputs = await childInputs(version.id, item);
      if (inputs === null) continue;
      const reuseKey = inputKey(plan, item, version, inputs);
      let base = await boundSummary(version.id, item.id, true);
      const binding = await db.query<{ input_key: string }>(
        `SELECT input_key FROM transcript_map_bindings WHERE version_id=? AND node_id=?`,
        [version.id, item.id],
      );
      let boundKey = binding[0]?.input_key;
      const reusable = await db.query<{ id: string }>(
        `SELECT s.id FROM transcript_map_summaries s WHERE s.reuse_key=?
        AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=s.id AND r.verdict IN ('correct','reject'))
        ORDER BY CAST(json_extract(s.payload,'$.correctionDepth') AS INTEGER) DESC,s.created_at DESC,s.id DESC LIMIT 1`,
        [reuseKey],
      );
      if (reusable[0] && reusable[0].id !== base?.id) {
        await db.run(
          `INSERT INTO transcript_map_bindings(version_id,node_id,summary_id,input_key) VALUES(?,?,?,?)
          ON CONFLICT(version_id,node_id) DO UPDATE SET summary_id=excluded.summary_id,input_key=excluded.input_key`,
          [version.id, item.id, reusable[0].id, reuseKey],
        );
        base = await boundSummary(version.id, item.id, true);
        boundKey = reuseKey;
      }
      if (!base) {
        const depth = Math.max(
          0,
          ...(await Promise.all(
            inputs.map(async (child) =>
              child.summaryId
                ? ((await one<TranscriptMapSummary>("transcript_map_summaries", child.summaryId))
                    ?.correctionDepth ?? 0)
                : 0,
            ),
          )),
        );
        if (depth <= policy.maxCorrections)
          added += await queue(
            { version, node: item, plan },
            policy,
            "generate",
            null,
            inputs,
            depth,
            reuseKey,
            0,
            now,
          );
        continue;
      }
      const reviews = await db.query<{ verdict: string }>(
        `SELECT verdict FROM transcript_map_reviews WHERE summary_id=? ORDER BY created_at DESC,work_id DESC LIMIT 1`,
        [base.id],
      );
      if (
        boundKey !== reuseKey ||
        reviews[0]?.verdict === "correct" ||
        reviews[0]?.verdict === "reject"
      ) {
        if (base.correctionDepth < policy.maxCorrections)
          added += await queue(
            { version, node: item, plan },
            policy,
            "correct",
            base,
            inputs,
            base.correctionDepth + 1,
            reuseKey,
            0,
            now,
          );
        continue;
      }
      const served = await db.query<{ seq: number }>(
        `SELECT coalesce(max(rowid),0) seq FROM transcript_map_served WHERE summary_id=?`,
        [base.id],
      );
      if (Number(served[0]?.seq ?? 0) > 0 && policy.maxReviews > 0)
        added += await queue(
          { version, node: item, plan },
          policy,
          "review",
          base,
          inputs,
          base.correctionDepth,
          reuseKey,
          Number(served[0]?.seq),
          now,
        );
    }
    const last = rows.length === cap ? rows[rows.length - 1] : undefined;
    await db.run(
      `INSERT INTO transcript_map_scan(machine_id,contract_digest,version_cursor,node_cursor) VALUES(?,?,?,?)
      ON CONFLICT(machine_id,contract_digest) DO UPDATE SET version_cursor=excluded.version_cursor,node_cursor=excluded.node_cursor`,
      [policy.machineId, key, last?.vr ?? 0, last?.nr ?? 0],
    );
    if (added) store.touch?.();
    return added;
  }
  async function offers(
    raw: TranscriptMapPolicy,
    now: string,
    limit = 32,
  ): Promise<TranscriptMapWork[]> {
    const policy = TranscriptMapPolicySchema.parse(raw);
    const rows = await db.query<{ id: string }>(
      `SELECT w.id FROM transcript_map_work w JOIN transcript_map_versions v ON v.id=w.version_id
      JOIN transcript_map_heads h ON h.version_id=v.id JOIN transcript_map_plans p ON p.id=v.plan_id
      WHERE v.machine_id=? AND v.contract_digest=? AND w.state='queued' AND w.ready_at<=? AND w.attempt<=?
      AND p.complete=1 AND ${liveAccess}
      ORDER BY w.ready_at,w.created_at,w.id LIMIT ?`,
      [policy.machineId, contract(policy), now, policy.maxAttempts, MAX_SCAN],
    );
    const result: TranscriptMapWork[] = [];
    for (const row of rows) {
      const details = await work(row.id);
      if (details && (await eligible(details, policy))) result.push(details.work);
      else
        await db.run(
          `UPDATE transcript_map_work SET state='obsolete' WHERE id=? AND state='queued'`,
          [row.id],
        );
      if (result.length >= bound(limit)) break;
    }
    return result;
  }
  async function startWork(
    id: string,
    claim: { id: string; runId: string; fence: number },
    now: string,
  ): Promise<boolean> {
    const held = await db.query<{
      state: string;
      claim_id: string | null;
      run_id: string | null;
      fence: number | null;
    }>(`SELECT state,claim_id,run_id,fence FROM transcript_map_work WHERE id=?`, [id]);
    const running = held[0]?.state === "running";
    if (
      running &&
      (held[0]?.claim_id !== claim.id ||
        held[0]?.run_id !== claim.runId ||
        Number(held[0]?.fence) !== claim.fence)
    )
      return false;
    const details = await work(id);
    if (!details) return false;
    const rows = await db.query<{ policy: string }>(
      `SELECT policy FROM transcript_map_versions WHERE id=?`,
      [details.version.id],
    );
    if (
      !rows[0] ||
      !(await eligible(details, TranscriptMapPolicySchema.parse(JSON.parse(rows[0].policy))))
    )
      return false;
    if (running) return true;
    const check = readiness(details);
    const changed = await db.run(
      `UPDATE transcript_map_work SET state='running',claim_id=?,run_id=?,fence=?
      WHERE id=? AND state='queued' AND ready_at<=? AND ${check.sql}`,
      [claim.id, claim.runId, claim.fence, id, now, ...check.params],
    );
    if (changed.changes) store.touch?.();
    return changed.changes === 1;
  }
  async function settlementStatements(
    input: TranscriptMapSettlementInput,
  ): Promise<{ statements: SqlStatement[]; summaryId: string | null }> {
    const parsed = TranscriptMapModelResultSchema.safeParse(input.result);
    if (!parsed.success) throw new Error("invalid or overbound transcript map output");
    const result = parsed.data;
    safeText(result.kind === "summary" ? result.text : result.reason);
    const details = await work(input.details.work.id);
    if (
      !details ||
      json({ ...details, context: null }) !== json({ ...input.details, context: null })
    )
      throw new Error("transcript map work inputs changed");
    const { work: item, version, node: target, plan } = details;
    if ((item.mode === "review") !== (result.kind === "review"))
      throw new Error("transcript map result kind does not match work");
    const rows = await db.query<{ machine_id: string; policy: string }>(
      `SELECT machine_id,policy FROM transcript_map_versions WHERE id=?`,
      [version.id],
    );
    if (!rows[0]) throw new Error("missing transcript version policy");
    const policy = TranscriptMapPolicySchema.parse(JSON.parse(rows[0].policy));
    if (!(await eligible(details, policy)))
      throw new Error("transcript map work no longer eligible");
    const check = readiness(details);
    const guard: TranscriptMapCondition = {
      sql: `(${input.guard.sql}) AND EXISTS (SELECT 1 FROM transcript_map_work WHERE id=? AND state='running' AND run_id=?) AND (${check.sql})`,
      params: [...input.guard.params, item.id, input.runId, ...check.params],
    };
    const statements: SqlStatement[] = [];
    let summaryId: string | null = null;
    if (result.kind === "review") {
      if (!item.baseSummaryId) throw new Error("review has no original summary");
      statements.push({
        sql: `INSERT OR IGNORE INTO transcript_map_reviews(work_id,summary_id,verdict,reason,run_id,served_seq,created_at)
        SELECT ?,?,?,?,?,served_seq,? FROM transcript_map_work WHERE id=? AND ${guard.sql}`,
        params: [
          item.id,
          item.baseSummaryId,
          result.verdict,
          result.reason,
          input.runId,
          input.now,
          item.id,
          ...guard.params,
        ],
      });
    } else {
      const recipe = version.generateRecipe;
      summaryId = identity("tmsum", [item.id, input.runId]);
      const summary = TranscriptMapSummarySchema.parse({
        id: summaryId,
        versionId: version.id,
        nodeId: target.id,
        text: result.text,
        runId: input.runId,
        recipeId: recipe.id,
        recipeVersion: recipe.version,
        recipeDigest: digest(recipe),
        profile: version.profile,
        children: item.children,
        supersedes: item.baseSummaryId,
        correctionDepth: item.correctionDepth,
        createdAt: input.now,
      });
      const key = inputKey(plan, target, version, item.children);
      statements.push({
        sql: `INSERT OR IGNORE INTO transcript_map_summaries(id,version_id,node_id,reuse_key,payload,text,created_at)
        SELECT ?,?,?,?,?,?,? WHERE ${guard.sql}`,
        params: [
          summaryId,
          version.id,
          target.id,
          key,
          json(summary),
          summary.text,
          input.now,
          ...guard.params,
        ],
      });
      statements.push({
        sql: `INSERT INTO transcript_map_bindings(version_id,node_id,summary_id,input_key)
        SELECT ?,?,?,? WHERE ${guard.sql} ON CONFLICT(version_id,node_id) DO UPDATE SET summary_id=excluded.summary_id,input_key=excluded.input_key`,
        params: [version.id, target.id, summaryId, key, ...guard.params],
      });
      if (item.baseSummaryId)
        statements.push({
          sql: `UPDATE transcript_map_bindings SET summary_id=?
        WHERE summary_id=? AND input_key=? AND (${input.guard.sql}) AND EXISTS (SELECT 1 FROM transcript_map_work WHERE id=? AND state='running' AND run_id=?)
        AND EXISTS (SELECT 1 FROM transcript_map_summaries WHERE id=?)`,
          params: [
            summaryId,
            item.baseSummaryId,
            key,
            ...input.guard.params,
            item.id,
            input.runId,
            summaryId,
          ],
        });
    }
    // Readiness changes with the binding above. Completion instead requires the artifact itself.
    statements.push({
      sql: `UPDATE transcript_map_work SET state='complete' WHERE id=? AND state='running' AND run_id=? AND (${input.guard.sql})
      AND ${summaryId === null ? "EXISTS (SELECT 1 FROM transcript_map_reviews WHERE work_id=?)" : "EXISTS (SELECT 1 FROM transcript_map_summaries WHERE id=?)"}`,
      params: [item.id, input.runId, ...input.guard.params, summaryId ?? item.id],
    });
    return { statements, summaryId };
  }
  async function failureStatements(input: {
    workId: string;
    now: string;
    guard: TranscriptMapCondition;
    reason: string;
  }): Promise<SqlStatement[]> {
    const details = await work(input.workId);
    if (!details) return [];
    const rows = await db.query<{ policy: string }>(
      `SELECT policy FROM transcript_map_versions WHERE id=?`,
      [details.version.id],
    );
    if (!rows[0]) return [];
    const policy = TranscriptMapPolicySchema.parse(JSON.parse(rows[0].policy));
    const exhausted = details.work.attempt >= policy.maxAttempts;
    const ready = new Date(
      Date.parse(input.now) + 60_000 * 2 ** (details.work.attempt - 1),
    ).toISOString();
    // Never persist reason: callers may accidentally pass a rejected provider body as an error.
    return [
      {
        sql: `UPDATE transcript_map_work SET state=?,attempt=?,payload=json_set(payload,'$.attempt',?),
      ready_at=?,run_id=NULL,fence=NULL,claim_id=NULL,reason='attempt failed'
      WHERE id=? AND state='running' AND (${input.guard.sql})`,
        params: [
          exhausted ? "failed" : "queued",
          exhausted ? details.work.attempt : details.work.attempt + 1,
          exhausted ? details.work.attempt : details.work.attempt + 1,
          ready,
          input.workId,
          ...input.guard.params,
        ],
      },
    ];
  }

  async function versionCoverage(
    plan: TranscriptMapPlan,
    versionId: string | null,
  ): Promise<TranscriptMapCoverage> {
    const out = emptyCoverage();
    out.sourceBytes = plan.source.bytes;
    const state = await db.query<{ complete: number }>(
      `SELECT complete FROM transcript_map_plans WHERE id=?`,
      [plan.id],
    );
    if (Number(state[0]?.complete) !== 1)
      return { ...out, unmappedBytes: out.sourceBytes, partial: true, tailBytes: out.sourceBytes };
    if (plan.direct) return { ...out, directBytes: plan.source.bytes };
    out.gapBytes = plan.gapBytes;
    const accepted = `b.summary_id IS NOT NULL AND NOT EXISTS
      (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject'))`;
    const rows = await db.query<{ summarized: number; tail: number; missing: number }>(
      `SELECT
      coalesce(sum(CASE WHEN n.gap IS NULL AND ${accepted} AND json_array_length(json_extract(n.payload,'$.children'))=0 THEN n.byte_length ELSE 0 END),0) summarized,
      coalesce(max(CASE WHEN n.gap IS NOT NULL OR (${accepted} AND json_array_length(json_extract(n.payload,'$.children'))=0) THEN n.byte_offset+n.byte_length ELSE 0 END),0) tail,
      coalesce(sum(CASE WHEN n.gap IS NULL AND NOT (${accepted}) THEN 1 ELSE 0 END),0) missing
      FROM transcript_map_nodes n LEFT JOIN transcript_map_bindings b ON b.node_id=n.id AND b.version_id=?
      WHERE n.plan_id=?`,
      [versionId, plan.id],
    );
    out.summarizedBytes = Number(rows[0]?.summarized ?? 0);
    out.unmappedBytes = Math.max(0, out.sourceBytes - out.gapBytes - out.summarizedBytes);
    out.tailBytes = Math.max(0, out.sourceBytes - Number(rows[0]?.tail ?? 0));
    out.partial = out.gapBytes > 0 || out.unmappedBytes > 0 || Number(rows[0]?.missing ?? 0) > 0;
    const levels = await db.query<{ level: number }>(
      `SELECT DISTINCT n.level FROM transcript_map_nodes n
      JOIN transcript_map_bindings b ON b.node_id=n.id AND b.version_id=? WHERE n.plan_id=? AND ${accepted} ORDER BY n.level LIMIT 4`,
      [versionId, plan.id],
    );
    out.levels = levels.map((row) => Number(row.level));
    // A parent retains its actual historical inputs. Changed child bindings make that parent
    // stale until a bounded correction is published; they never silently rewrite its provenance.
    const stale = await db.query<{ n: number }>(
      `SELECT count(*) n FROM transcript_map_bindings b
      JOIN transcript_map_summaries s ON s.id=b.summary_id WHERE b.version_id=? AND EXISTS
      (SELECT 1 FROM json_each(json_extract(s.payload,'$.children')) child
       WHERE json_extract(child.value,'$.summaryId') IS NOT NULL AND NOT EXISTS
       (SELECT 1 FROM transcript_map_nodes parent JOIN transcript_map_bindings cb
          ON cb.node_id=json_extract(parent.payload,'$.children[' || child.key || ']') AND cb.version_id=b.version_id
        WHERE parent.id=b.node_id AND cb.summary_id=json_extract(child.value,'$.summaryId')
          AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=cb.summary_id AND r.verdict IN ('correct','reject'))))`,
      [versionId],
    );
    out.stale = Number(stale[0]?.n ?? 0) > 0;
    const pending = await db.query<{ n: number }>(
      `SELECT count(*) n FROM transcript_map_bindings b
      WHERE b.version_id=? AND EXISTS (SELECT 1 FROM transcript_map_reviews r
        WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject'))`,
      [versionId],
    );
    out.stale ||= Number(pending[0]?.n ?? 0) > 0;
    if (versionId) {
      const current = await db.query<{ n: number }>(
        `SELECT count(*) n FROM transcript_map_heads WHERE version_id=?`,
        [versionId],
      );
      out.stale ||= Number(current[0]?.n ?? 0) === 0;
      const generation = await db.query<{ n: number }>(
        `SELECT count(*) n FROM transcript_map_versions v
        WHERE v.id=? AND v.generation<(SELECT coalesce(max(generation),0) FROM transcript_map_regenerations WHERE capture_id=?)`,
        [versionId, plan.source.id],
      );
      out.stale ||= Number(generation[0]?.n ?? 0) > 0;
    }
    out.partial ||= out.stale;
    return out;
  }
  async function coverage(
    scope: TranscriptMapScope,
    captureId?: string,
  ): Promise<TranscriptMapCoverage> {
    const auth = authorization(scope, "c.id");
    // There is one selected plan per capture, not the sum of every historical segmentation.
    // Header bytes of incomplete plans remain explicitly unmapped.
    const rows = await db.query<{
      capture_id: string;
      plan_id: string | null;
      version_id: string | null;
    }>(
      `SELECT c.id capture_id,p.id plan_id,h.version_id FROM transcript_map_captures c
       LEFT JOIN transcript_map_plans p ON p.rowid=(SELECT max(p2.rowid) FROM transcript_map_plans p2 WHERE p2.capture_id=c.id)
       LEFT JOIN transcript_map_heads h ON h.plan_id=p.id AND h.machine_id=?
       WHERE ${auth.sql} ${captureId === undefined ? "" : "AND c.id=?"} ORDER BY c.id LIMIT ?`,
      [
        scope.machineId,
        ...auth.params,
        ...(captureId === undefined ? [] : [captureId]),
        MAX_SCAN + 1,
      ],
    );
    const out = emptyCoverage();
    const levels = new Set<number>();
    for (const row of rows.slice(0, MAX_SCAN)) {
      const plan = row.plan_id
        ? await one<TranscriptMapPlan>("transcript_map_plans", row.plan_id)
        : null;
      if (!plan) {
        out.partial = true;
        out.tailBytes = null;
        continue;
      }
      const part = await versionCoverage(plan, row.version_id);
      out.sourceBytes += part.sourceBytes;
      out.summarizedBytes += part.summarizedBytes;
      out.directBytes += part.directBytes;
      out.unmappedBytes += part.unmappedBytes;
      out.gapBytes += part.gapBytes;
      out.partial ||= part.partial;
      out.stale ||= part.stale;
      if (out.tailBytes !== null)
        out.tailBytes = part.tailBytes === null ? null : out.tailBytes + part.tailBytes;
      for (const level of part.levels) levels.add(level);
    }
    out.levels = [...levels].sort((a, b) => a - b);
    if (
      rows.length > MAX_SCAN ||
      (captureId === undefined && rows.length < scope.context.eligibleCaptures) ||
      (captureId !== undefined && rows.length === 0)
    ) {
      out.partial = true;
      out.tailBytes = null;
    }
    return out;
  }
  async function status(scope: TranscriptMapScope): Promise<TranscriptMapStatus> {
    const auth = authorization(scope, "p.capture_id");
    const rows = await db.query<{ n: number }>(
      `SELECT count(*) n FROM transcript_map_plans p
      LEFT JOIN transcript_map_heads h ON h.plan_id=p.id AND h.machine_id=?
      LEFT JOIN transcript_map_versions v ON v.id=h.version_id
      WHERE p.complete=1 AND p.rowid=(SELECT max(latest.rowid) FROM transcript_map_plans latest WHERE latest.capture_id=p.capture_id)
      AND ${auth.sql} AND json_extract(p.payload,'$.gapBytes')=0
      AND (json_extract(p.payload,'$.direct')=1 OR (v.id IS NOT NULL
        AND v.generation=(SELECT coalesce(max(generation),0) FROM transcript_map_regenerations WHERE capture_id=p.capture_id)
        AND NOT EXISTS (SELECT 1 FROM transcript_map_nodes n
          LEFT JOIN transcript_map_bindings b ON b.node_id=n.id AND b.version_id=v.id
          LEFT JOIN transcript_map_summaries s ON s.id=b.summary_id
          WHERE n.plan_id=p.id AND (b.summary_id IS NULL OR EXISTS
            (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject'))
            OR EXISTS (SELECT 1 FROM json_each(json_extract(s.payload,'$.children')) child
              WHERE json_extract(child.value,'$.summaryId') IS NOT NULL AND NOT EXISTS
                (SELECT 1 FROM transcript_map_bindings cb WHERE cb.version_id=v.id
                  AND cb.node_id=json_extract(n.payload,'$.children[' || child.key || ']')
                  AND cb.summary_id=json_extract(child.value,'$.summaryId')))))))`,
      [scope.machineId, ...auth.params],
    );
    const known = await db.query<{ n: number }>(
      `SELECT count(*) n FROM transcript_map_contexts WHERE machine_id=? AND digest=?`,
      [scope.machineId, scope.context.digest],
    );
    const verifiedMappedCaptures = Number(rows[0]?.n ?? 0);
    return {
      eligibleCaptures: scope.context.eligibleCaptures,
      verifiedMappedCaptures,
      observedAt: scope.context.observedAt,
      partial:
        Number(known[0]?.n ?? 0) === 0 || verifiedMappedCaptures !== scope.context.eligibleCaptures,
    };
  }

  async function node(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
    knownCoverage?: TranscriptMapCoverage,
  ): Promise<TranscriptMapView | null> {
    const auth = authorization(scope, "p.capture_id");
    const visible = await db.query<{ plan_id: string }>(
      `SELECT v.plan_id FROM transcript_map_versions v
      JOIN transcript_map_plans p ON p.id=v.plan_id JOIN transcript_map_nodes n ON n.plan_id=p.id
      WHERE v.id=? AND v.machine_id=? AND n.id=? AND p.complete=1 AND ${auth.sql}`,
      [versionId, scope.machineId, nodeId, ...auth.params],
    );
    if (!visible[0]) return null;
    const plan = await one<TranscriptMapPlan>("transcript_map_plans", visible[0].plan_id);
    const item = await one<TranscriptMapNode>("transcript_map_nodes", nodeId);
    if (!plan || !item) return null;
    const summary = await boundSummary(versionId, nodeId);
    const view =
      summary === null
        ? null
        : (() => {
            const { children: inputs, ...provenance } = summary;
            return {
              ...provenance,
              inputSummaryIds: inputs.flatMap((child) =>
                child.summaryId ? [child.summaryId] : [],
              ),
            };
          })();
    return {
      inference: true,
      versionId,
      source: plan.source,
      node: item,
      summary: view,
      reused: summary !== null && (summary.versionId !== versionId || summary.nodeId !== nodeId),
      coverage: knownCoverage ?? (await versionCoverage(plan, versionId)),
    };
  }
  async function children(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
  ): Promise<TranscriptMapView[]> {
    const parent = await node(scope, versionId, nodeId);
    if (!parent) return [];
    const result: TranscriptMapView[] = [];
    for (const id of parent.node.children) {
      const child = await node(scope, versionId, id, parent.coverage);
      if (child) result.push(child);
    }
    return result;
  }
  async function ancestors(
    scope: TranscriptMapScope,
    versionId: string,
    nodeId: string,
  ): Promise<TranscriptMapView[]> {
    let current = await node(scope, versionId, nodeId);
    const result: TranscriptMapView[] = [];
    while (current?.node.parentId && result.length < 3) {
      current = await node(scope, versionId, current.node.parentId, current.coverage);
      if (current) result.push(current);
    }
    return result;
  }
  // These selection aids return immutable metadata only. They are not authorization.
  async function candidates(
    machineId: string,
    input: { query?: string; captureId?: string; versionId?: string; nodeId?: string },
    limit = TRANSCRIPT_MAP_MAX_CAPTURES,
  ): Promise<TranscriptMapCapture[]> {
    if (
      input.captureId &&
      input.query === undefined &&
      input.versionId === undefined &&
      input.nodeId === undefined
    ) {
      const capture = await one<TranscriptMapCapture>("transcript_map_captures", input.captureId);
      return capture ? [capture] : [];
    }
    const match = input.query === undefined ? null : termsQuery(input.query);
    if (input.query !== undefined && !match) return [];
    const rows = await db.query<{ payload: string }>(
      `SELECT DISTINCT c.payload FROM transcript_map_captures c
      JOIN transcript_map_plans p ON p.capture_id=c.id JOIN transcript_map_versions v ON v.plan_id=p.id
      ${
        match
          ? `JOIN transcript_map_heads h ON h.version_id=v.id AND h.machine_id=v.machine_id
        JOIN transcript_map_bindings b ON b.version_id=v.id
        JOIN transcript_map_terms ON transcript_map_terms.summary_id=b.summary_id`
          : ""
      }
      WHERE v.machine_id=? AND p.complete=1
      ${
        match
          ? `AND transcript_map_terms MATCH ?
        AND p.rowid=(SELECT max(latest.rowid) FROM transcript_map_plans latest WHERE latest.capture_id=p.capture_id)
        AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject'))`
          : ""
      }
      ${input.captureId ? "AND c.id=?" : ""}
      ${input.versionId ? "AND v.id=?" : ""}
      ${input.nodeId ? "AND EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=? AND n.plan_id=p.id)" : ""}
      ORDER BY c.id LIMIT ?`,
      [
        machineId,
        ...(match ? [match] : []),
        ...(input.captureId ? [input.captureId] : []),
        ...(input.versionId ? [input.versionId] : []),
        ...(input.nodeId ? [input.nodeId] : []),
        bound(limit, TRANSCRIPT_MAP_MAX_CAPTURES),
      ],
    );
    return rows.map((row) => TranscriptMapCaptureSchema.parse(JSON.parse(row.payload)));
  }
  async function reference(
    machineId: string,
    versionId: string,
    nodeId: string,
  ): Promise<{ source: TranscriptMapSource; node: TranscriptMapNode } | null> {
    const rows = await db.query<{ plan: string; node: string }>(
      `SELECT p.payload plan,n.payload node
      FROM transcript_map_versions v JOIN transcript_map_plans p ON p.id=v.plan_id
      JOIN transcript_map_nodes n ON n.plan_id=p.id WHERE v.machine_id=? AND v.id=? AND n.id=? AND p.complete=1`,
      [machineId, versionId, nodeId],
    );
    return rows[0]
      ? {
          source: TranscriptMapPlanSchema.parse(JSON.parse(rows[0].plan)).source,
          node: TranscriptMapNodeSchema.parse(JSON.parse(rows[0].node)),
        }
      : null;
  }
  async function search(
    scope: TranscriptMapScope,
    query: string,
    limit: number,
  ): Promise<TranscriptMapView[]> {
    const match = termsQuery(query);
    if (!match) return [];
    const auth = authorization(scope, "p.capture_id");
    const cap = bound(limit);
    const rows = await db.query<{ version_id: string; node_id: string }>(
      `SELECT b.version_id,b.node_id
      FROM transcript_map_terms JOIN transcript_map_bindings b ON b.summary_id=transcript_map_terms.summary_id
      JOIN transcript_map_heads h ON h.version_id=b.version_id
      JOIN transcript_map_versions v ON v.id=h.version_id JOIN transcript_map_plans p ON p.id=v.plan_id
      WHERE transcript_map_terms MATCH ? AND h.machine_id=? AND p.complete=1 AND ${auth.sql}
      AND p.rowid=(SELECT max(latest.rowid) FROM transcript_map_plans latest WHERE latest.capture_id=p.capture_id)
      AND NOT EXISTS (SELECT 1 FROM transcript_map_reviews r WHERE r.summary_id=b.summary_id AND r.verdict IN ('correct','reject'))
      ORDER BY bm25(transcript_map_terms),b.version_id,b.node_id LIMIT ?`,
      [match, scope.machineId, ...auth.params, cap + 1],
    );
    const overall = await coverage(scope);
    const result: TranscriptMapView[] = [];
    for (const row of rows.slice(0, cap)) {
      const view = await node(scope, row.version_id, row.node_id);
      if (!view) continue;
      view.coverage.partial ||= overall.partial || rows.length > cap;
      if (overall.tailBytes === null) view.coverage.tailBytes = null;
      result.push(view);
    }
    return result;
  }
  async function noteServed(input: {
    readId: string;
    summaryIds: readonly string[];
    now: string;
  }): Promise<void> {
    if (!input.readId || input.readId.length > 512 || input.summaryIds.length > READ_LIMIT)
      throw new Error("invalid transcript map served event");
    await batch(
      [...new Set(input.summaryIds)].map((id) => ({
        sql: `INSERT OR IGNORE INTO transcript_map_served(read_id,summary_id,created_at)
        SELECT ?,id,? FROM transcript_map_summaries WHERE id=?`,
        params: [input.readId, input.now, id],
      })),
    );
  }
  async function regenerate(input: {
    captureId: string;
    requestId: string;
    reason: string;
    now: string;
  }): Promise<number> {
    if (
      !input.requestId ||
      input.requestId.length > 512 ||
      !input.reason.trim() ||
      input.reason.length > 2000
    )
      throw new Error("invalid transcript map regeneration request");
    safeText(input.reason);
    await db.run(
      `INSERT OR IGNORE INTO transcript_map_regenerations(capture_id,request_id,generation,reason,created_at)
      SELECT ?,?,coalesce(max(generation),0)+1,?,? FROM transcript_map_regenerations WHERE capture_id=?`,
      [input.captureId, input.requestId, input.reason, input.now, input.captureId],
    );
    const rows = await db.query<{ generation: number }>(
      `SELECT generation FROM transcript_map_regenerations WHERE capture_id=? AND request_id=?`,
      [input.captureId, input.requestId],
    );
    if (!rows[0]) throw new Error("transcript map regeneration was not recorded");
    store.touch?.();
    return Number(rows[0].generation);
  }

  return {
    recordCatalog,
    recordAccess,
    catalogState,
    nextPlan,
    recordPlan,
    ensureVersion,
    refreshWork,
    offers,
    work,
    startWork,
    settlementStatements,
    failureStatements,
    candidates,
    reference,
    search,
    node,
    children,
    ancestors,
    coverage,
    status,
    noteServed,
    regenerate,
  };
}
