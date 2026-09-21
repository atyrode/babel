import { createHash } from "node:crypto";
import {
  RecallResultSchema,
  RecallTargetSchema,
  RecallTraceRequestSchema,
  RecallTraceSchema,
  type RecallReply,
  type RecallRequest,
  type RecallTarget,
  type RecallTrace,
} from "../contract.ts";
import { secretScan } from "../machine/preflight.ts";
import type { BabelStore } from "./store.ts";

/** Record intent before native I/O; an interrupted dispatch remains an inspectable attempt. */
export async function startRecall(
  store: BabelStore,
  principalId: string,
  target: RecallTarget,
  revision: string,
  request: RecallRequest,
): Promise<string> {
  const id = crypto.randomUUID();
  const recorded =
    request.kind === "session"
      ? {
          kind: request.kind,
          previewDigest: previewDigest(request.previewId),
          offset: request.offset,
          maxBytes: request.maxBytes,
        }
      : request;
  const redactedRequest = secretScan().redact(
    JSON.stringify(RecallTraceRequestSchema.parse(recorded)),
    1,
  );
  await store.db.run(
    `INSERT INTO recall_requests(id, principal_id, operation, target, service_revision, redacted_request, created_at)
     VALUES(?, ?, ?, ?, ?, ?, ?)`,
    [
      id,
      principalId,
      request.kind,
      JSON.stringify(target),
      revision,
      redactedRequest,
      new Date(store.now()).toISOString(),
    ],
  );
  return id;
}

export async function readRecallRequest(store: BabelStore, principalId: string, id: string) {
  const rows = await store.db.query(
    `SELECT target, service_revision, operation FROM recall_requests WHERE id = ? AND principal_id = ?`,
    [id, principalId],
  );
  const row = rows[0];
  if (row === undefined) return null;
  return {
    target: RecallTargetSchema.parse(JSON.parse(String(row["target"]))),
    revision: String(row["service_revision"]),
    operation: RecallResultSchema.shape.operation.parse(row["operation"]),
  };
}

const previewDigest = (id: string): string => createHash("sha256").update(id).digest("hex");

/** A random token is not authority: the same authenticated principal, class and revision own it. */
export async function ownsRecallPreview(
  store: BabelStore,
  principalId: string,
  target: RecallTarget,
  revision: string,
  token: string,
): Promise<boolean> {
  const rows = await store.db.query(
    `SELECT 1 FROM recall_outcomes AS outcome
     JOIN recall_requests AS request ON request.id = outcome.request_id
     WHERE outcome.preview_digest = ? AND request.principal_id = ? AND request.target = ?
       AND request.service_revision = ? AND request.operation = 'preview' LIMIT 1`,
    [previewDigest(token), principalId, JSON.stringify(target), revision],
  );
  return rows.length !== 0;
}

/** Neither excerpts nor session page bodies enter the hub's derived ledger. */
export async function recordRecallOutcome(store: BabelStore, reply: RecallReply): Promise<void> {
  const result = reply.result;
  const summary: RecallTrace = { state: reply.state };
  if (result !== undefined) {
    const { hits, preview, ...metadata } = result;
    summary.result = {
      ...metadata,
      locators: hits.map((hit) => hit.locator),
      preview:
        preview === undefined
          ? undefined
          : {
              sourceBytes: preview.sourceBytes,
              servedBytes: preview.servedBytes,
              records: preview.records,
              sourceDigest: preview.sourceDigest,
            },
    };
  }
  const redacted = secretScan().redact(JSON.stringify(RecallTraceSchema.parse(summary)), 1);
  // One SQL statement makes concurrent identical polls idempotent without a mutable ledger row.
  await store.db.run(
    `INSERT INTO recall_outcomes(request_id, state, summary, preview_digest, created_at)
     SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS(
       SELECT 1 FROM recall_outcomes WHERE request_id = ? AND state = ? AND summary = ?
     )`,
    [
      reply.requestId,
      reply.state,
      redacted,
      result?.preview === undefined ? null : previewDigest(result.preview.previewId),
      new Date(store.now()).toISOString(),
      reply.requestId,
      reply.state,
      redacted,
    ],
  );
}
