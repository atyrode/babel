import { createHash } from "node:crypto";
import {
  TranscriptMapNativeRequestSchema, TranscriptMapReadTraceSchema, TranscriptMapTargetSchema,
  type TranscriptMapNativeRequest, type TranscriptMapReadTrace, type TranscriptMapTarget,
} from "../contract.ts";
import type { TranscriptMapStore } from "./transcript-maps.ts";

export interface MapReadRequest {
  id: string;
  operation: "read" | "source" | "regenerate";
  target: TranscriptMapTarget;
  revision: string;
  digest: string;
}
const digest = (input: unknown) => createHash("sha256").update(JSON.stringify(input)).digest("hex");
const now = () => new Date().toISOString();

export function transcriptMapReads(store: TranscriptMapStore) {
  async function locate(principal: string, reference: { requestId?: string | undefined; traceId?: number | undefined }): Promise<MapReadRequest | null> {
    const rows = await store.db.query<{
      id: string; operation: MapReadRequest["operation"]; target: string; service_revision: string; request_digest: string;
    }>(`SELECT id,operation,target,service_revision,request_digest FROM transcript_map_requests
      WHERE principal_id=? AND ${reference.requestId === undefined ? "trace_id=?" : "id=?"}`,
    [principal, reference.requestId ?? reference.traceId ?? -1]);
    const row = rows[0];
    return row ? { id: row.id, operation: row.operation, target: TranscriptMapTargetSchema.parse(JSON.parse(row.target)),
      revision: row.service_revision, digest: row.request_digest } : null;
  }
  async function begin(input: { principal: string; traceId: number; target: TranscriptMapTarget; revision: string;
    operation: MapReadRequest["operation"]; request: unknown; requestId?: string }): Promise<MapReadRequest> {
    const id = input.requestId ?? crypto.randomUUID();
    const target = TranscriptMapTargetSchema.parse(input.target);
    const requestDigest = digest(input.request);
    await store.db.run(`INSERT OR IGNORE INTO transcript_map_requests
      (id,principal_id,trace_id,operation,target,service_revision,request_digest,created_at) VALUES(?,?,?,?,?,?,?,?)`,
    [id, input.principal, input.traceId, input.operation, JSON.stringify(target), input.revision, requestDigest, now()]);
    const held = await locate(input.principal, input.requestId === undefined ? { traceId: input.traceId } : { requestId: id });
    if (!held || held.operation !== input.operation || held.digest !== requestDigest ||
      JSON.stringify(held.target) !== JSON.stringify(target)) throw new Error("Map request identity mismatch");
    return held;
  }
  async function round(requestId: string): Promise<number> {
    const rows = await store.db.query<{ n: number }>(`SELECT coalesce(max(round)+1,0) n FROM transcript_map_read_outcomes
      WHERE request_id=? AND json_extract(outcome,'$.state')='complete'`, [requestId]);
    return Number(rows[0]?.n ?? 0);
  }
  async function native(requestId: string, round: number, stage: string, request: TranscriptMapNativeRequest) {
    const nativeId = crypto.randomUUID();
    // Only native structural metadata is kept here. Search text and regeneration reasons never enter it.
    const parsed = TranscriptMapNativeRequestSchema.parse(request);
    await store.db.run(`INSERT OR IGNORE INTO transcript_map_native_requests
      (request_id,round,stage,native_id,request,created_at) VALUES(?,?,?,?,?,?)`,
    [requestId, round, stage, nativeId, JSON.stringify(parsed), now()]);
    const rows = await store.db.query<{ native_id: string; request: string }>(`SELECT native_id,request
      FROM transcript_map_native_requests WHERE request_id=? AND round=? AND stage=?`, [requestId, round, stage]);
    const row = rows[0];
    if (!row) throw new Error("Map native intent unavailable");
    return { id: row.native_id, fresh: row.native_id === nativeId,
      request: TranscriptMapNativeRequestSchema.parse(JSON.parse(row.request)) };
  }
  async function outcome(requestId: string, round: number, input: TranscriptMapReadTrace): Promise<void> {
    const trace = TranscriptMapReadTraceSchema.parse(input);
    const summaryIds = trace.state === "complete" && trace.source === undefined
      ? [...new Set(trace.summaries.flatMap((item) => item.summaryId === null ? [] : [item.summaryId]))] : [];
    const at = now();
    // One transaction: failed outcome persistence cannot leave review eligibility behind.
    await store.db.batch([
      { sql: `INSERT OR IGNORE INTO transcript_map_read_outcomes(request_id,round,outcome,created_at) VALUES(?,?,?,?)`,
        params: [requestId, round, JSON.stringify(trace), at] },
      { sql: `INSERT OR IGNORE INTO transcript_map_served(read_id,summary_id,created_at)
          SELECT ?,s.id,? FROM transcript_map_summaries s JOIN json_each(?) ids ON ids.value=s.id
          WHERE EXISTS (SELECT 1 FROM transcript_map_requests r WHERE r.id=? AND r.operation='read')`,
        params: [requestId, at, JSON.stringify(summaryIds), requestId] },
    ]);
    store.touch?.();
  }
  return { locate, begin, round, native, outcome };
}
