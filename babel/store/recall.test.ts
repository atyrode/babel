import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import {
  BABEL_PLUGIN_ID,
  RECALL_MAX_SERVED_BYTES,
  RECALL_SERVICE_ID,
  RECALL_UNTRUSTED_BEGIN,
  RECALL_UNTRUSTED_END,
  RecallRequestSchema,
  RecallResultSchema,
  RecallTargetSchema,
  RecallTraceRequestSchema,
  SESSION_RECORD_COORDINATES,
  type RecallReply,
} from "../contract.ts";
import { ownsRecallPreview, readRecallRequest, recordRecallOutcome, startRecall } from "./recall.ts";
import { SCHEMA_V1 } from "./schema.ts";
import { openStore } from "./store.ts";

const directories: string[] = [];
afterEach(() => {
  for (const directory of directories.splice(0)) rmSync(directory, { recursive: true, force: true });
});

async function fixture() {
  const directory = mkdtempSync(join(tmpdir(), "babel-recall-ledger-"));
  directories.push(directory);
  const db = openPluginDatabase({ dataDir: directory, pluginId: BABEL_PLUGIN_ID });
  for (const statement of SCHEMA_V1) await db.run(statement);
  const store = openStore(db, () => Date.UTC(2026, 8, 21));
  const target = RecallTargetSchema.parse({
    kind: "service", machineId: "archive-machine", serviceId: RECALL_SERVICE_ID, operationId: "local",
  });
  const locator = {
    coordinates: SESSION_RECORD_COORDINATES, host: "synthetic", harness: "omp", session: "omp/project/session",
    snapshot: "a".repeat(64), path: "/synthetic/session.jsonl", captureDigest: `sha256:${"b".repeat(64)}`,
    sourceDigest: `sha256:${"c".repeat(64)}`,
    record: { line: 1, byteOffset: 0, byteLength: 8, digest: `sha256:${"d".repeat(64)}`, time: null },
  };
  const request = RecallRequestSchema.parse({ kind: "preview", locator });
  const requestId = await startRecall(store, "reader", target, "revision-1", request);
  const previewId = crypto.randomUUID();
  const result = RecallResultSchema.parse({
    operation: "preview", observedAt: "2026-09-21T00:00:00Z", newestSnapshotAt: "2026-09-20T00:00:00Z",
    previewByteLimit: RECALL_MAX_SERVED_BYTES,
    cost: { fetchedFiles: 1, fetchedBytes: 8, cacheHits: 0, indexedFiles: 1, listedSnapshots: 1, listedEntries: 1, replayedBytes: 8 },
    coverage: { eligible: 1, indexed: 1, complete: true, overBound: 0 },
    matches: null, omitted: 0, omittedSubjects: 0, refusedSubjects: [], refusal: null, hits: [],
    preview: { previewId, sourceBytes: 8, servedBytes: 8, records: 1, sourceDigest: locator.sourceDigest },
  });
  const reply: RecallReply = { requestId, state: "complete", result };
  return { store, target, locator, requestId, previewId, reply };
}

test("a completed preview belongs to the recorded principal, class and service revision", async () => {
  const f = await fixture();
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(false);
  await recordRecallOutcome(f.store, f.reply);
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(true);
  expect(await ownsRecallPreview(f.store, "another-reader", f.target, "revision-1", f.previewId)).toBe(false);
  expect(await ownsRecallPreview(f.store, "reader", { ...f.target, operationId: "external" }, "revision-1", f.previewId)).toBe(false);
  expect(await ownsRecallPreview(f.store, "reader", { ...f.target, machineId: "other-machine" }, "revision-1", f.previewId)).toBe(false);
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-2", f.previewId)).toBe(false);
  expect(await readRecallRequest(f.store, "another-reader", f.requestId)).toBeNull();
  expect(await readRecallRequest(f.store, "reader", f.requestId)).toEqual({
    target: f.target, revision: "revision-1", operation: "preview",
  });
});

test("durable requests redact likely secrets and retain only a digest of a widening handle", async () => {
  const f = await fixture();
  await recordRecallOutcome(f.store, f.reply);
  const wideningId = await startRecall(f.store, "reader", f.target, "revision-1", RecallRequestSchema.parse({
    kind: "session", previewId: f.previewId, offset: 0, maxBytes: 8,
  }));
  const likelySecret = ["synthetic", "private", "material", "0123456789"].join("-");
  await startRecall(f.store, "reader", f.target, "revision-1", RecallRequestSchema.parse({
    kind: "search", query: `Authorization: Bearer ${likelySecret}`,
  }));
  const requests = await f.store.db.query("SELECT id, redacted_request FROM recall_requests ORDER BY seq");
  const outcomes = await f.store.db.query("SELECT summary, preview_digest FROM recall_outcomes ORDER BY seq");
  const persisted = JSON.stringify({ requests, outcomes });
  expect(persisted).not.toContain(f.previewId);
  expect(persisted).not.toContain(likelySecret);
  const widening = requests.find(row => row["id"] === wideningId)!;
  const intent = RecallTraceRequestSchema.parse(JSON.parse(String(widening["redacted_request"])));
  expect(intent.kind).toBe("session");
  if (intent.kind !== "session") throw new Error("Expected a widening intent");
  expect(intent.previewDigest).toBe(outcomes[0]!["preview_digest"]);
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(true);
});

test("concurrent repeated outcomes append once without retaining excerpt bodies", async () => {
  const f = await fixture();
  const body = "private archived evidence must not enter the derived ledger";
  const result = RecallResultSchema.parse({
    ...f.reply.result,
    hits: [{
      locator: f.locator, snapshotAt: "2026-09-20T00:00:00Z", title: null, workspace: null,
      repository: null, metadataOrigin: "archive",
      excerpt: {
        trust: "archived-untrusted", begin: RECALL_UNTRUSTED_BEGIN, text: body,
        end: RECALL_UNTRUSTED_END, maxBytes: 8192, bytes: body.length,
        truncated: false, firstRecord: 1, lastRecord: 1,
      },
    }],
  });
  const reply: RecallReply = { ...f.reply, result };
  await Promise.all([recordRecallOutcome(f.store, reply), recordRecallOutcome(f.store, reply)]);
  const rows = await f.store.db.query("SELECT summary FROM recall_outcomes WHERE request_id = ?", [f.requestId]);
  expect(rows).toHaveLength(1);
  expect(String(rows[0]!["summary"])).not.toContain(body);
  await expect(f.store.db.run("DELETE FROM recall_outcomes WHERE request_id = ?", [f.requestId])).rejects.toThrow();
});
