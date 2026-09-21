import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  RECALL_MAX_SERVED_BYTES,
  RECALL_SERVICE_ID,
  RECALL_UNTRUSTED_BEGIN,
  RECALL_UNTRUSTED_END,
  RecallPollInputSchema,
  RecallPollReplySchema,
  RecallRequestSchema,
  RecallResultSchema,
  RecallServiceRequestSchema,
  RecallTargetSchema,
  RecallTraceRequestSchema,
  SESSION_RECORD_COORDINATES,
  type RecallReply,
  type RecallServiceRequest,
} from "../contract.ts";
import {
  ownsRecallPreview,
  readRecallRequest,
  recordRecallOutcome,
  startRecall,
} from "./recall.ts";
import { SCHEMA_V1 } from "./schema.ts";
import { openStore } from "./store.ts";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { recallDoors } from "../doors/recall.ts";

const directories: string[] = [];
afterEach(() => {
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true });
});

async function fixture() {
  const directory = mkdtempSync(join(tmpdir(), "babel-recall-ledger-"));
  directories.push(directory);
  const db = openPluginDatabase({ dataDir: directory, pluginId: BABEL_PLUGIN_ID });
  for (const statement of SCHEMA_V1) await db.run(statement);
  const store = openStore(db, () => Date.UTC(2026, 8, 21));
  const target = RecallTargetSchema.parse({
    kind: "service",
    machineId: "archive-machine",
    serviceId: RECALL_SERVICE_ID,
    operationId: "local",
  });
  const locator = {
    coordinates: SESSION_RECORD_COORDINATES,
    host: "synthetic",
    harness: "omp",
    session: "omp/project/session",
    snapshot: "a".repeat(64),
    path: "/synthetic/session.jsonl",
    captureDigest: `sha256:${"b".repeat(64)}`,
    sourceDigest: `sha256:${"c".repeat(64)}`,
    record: {
      line: 1,
      byteOffset: 0,
      byteLength: 8,
      digest: `sha256:${"d".repeat(64)}`,
      time: null,
    },
  };
  const request = RecallRequestSchema.parse({ kind: "preview", locator });
  const requestId = await startRecall(store, "reader", 1, target, "revision-1", request);
  const previewId = crypto.randomUUID();
  const result = RecallResultSchema.parse({
    operation: "preview",
    observedAt: "2026-09-21T00:00:00Z",
    newestSnapshotAt: "2026-09-20T00:00:00Z",
    previewByteLimit: RECALL_MAX_SERVED_BYTES,
    cost: {
      fetchedFiles: 1,
      fetchedBytes: 8,
      cacheHits: 0,
      indexedFiles: 1,
      listedSnapshots: 1,
      listedEntries: 1,
      replayedBytes: 8,
    },
    coverage: { eligible: 1, indexed: 1, complete: true, overBound: 0 },
    matches: null,
    omitted: 0,
    omittedSubjects: 0,
    refusedSubjects: [],
    refusal: null,
    hits: [],
    preview: {
      previewId,
      sourceBytes: 8,
      servedBytes: 8,
      records: 1,
      sourceDigest: locator.sourceDigest,
    },
  });
  const reply: RecallReply = { requestId, state: "complete", result };
  return { store, target, locator, requestId, previewId, reply };
}

test("a completed preview belongs to the recorded principal, class and service revision", async () => {
  const f = await fixture();
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(
    false,
  );
  await recordRecallOutcome(f.store, f.reply);
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(
    true,
  );
  expect(
    await ownsRecallPreview(f.store, "another-reader", f.target, "revision-1", f.previewId),
  ).toBe(false);
  expect(
    await ownsRecallPreview(
      f.store,
      "reader",
      { ...f.target, operationId: "external" },
      "revision-1",
      f.previewId,
    ),
  ).toBe(false);
  expect(
    await ownsRecallPreview(
      f.store,
      "reader",
      { ...f.target, machineId: "other-machine" },
      "revision-1",
      f.previewId,
    ),
  ).toBe(false);
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-2", f.previewId)).toBe(
    false,
  );
  expect(await readRecallRequest(f.store, "another-reader", { requestId: f.requestId })).toBeNull();
  expect(await readRecallRequest(f.store, "reader", { requestId: f.requestId })).toEqual({
    requestId: f.requestId,
    target: f.target,
    revision: "revision-1",
    operation: "preview",
  });
});

test("durable requests redact likely secrets and retain only a digest of a widening handle", async () => {
  const f = await fixture();
  await recordRecallOutcome(f.store, f.reply);
  const wideningId = await startRecall(
    f.store,
    "reader",
    2,
    f.target,
    "revision-1",
    RecallRequestSchema.parse({
      kind: "session",
      previewId: f.previewId,
      offset: 0,
      maxBytes: 8,
    }),
  );
  const likelySecret = ["synthetic", "private", "material", "0123456789"].join("-");
  await startRecall(
    f.store,
    "reader",
    3,
    f.target,
    "revision-1",
    RecallRequestSchema.parse({
      kind: "search",
      query: `Authorization: Bearer ${likelySecret}`,
    }),
  );
  const requests = await f.store.db.query(
    "SELECT id, redacted_request FROM recall_requests ORDER BY seq",
  );
  const outcomes = await f.store.db.query(
    "SELECT summary, preview_digest FROM recall_outcomes ORDER BY seq",
  );
  const persisted = JSON.stringify({ requests, outcomes });
  expect(persisted).not.toContain(f.previewId);
  expect(persisted).not.toContain(likelySecret);
  const widening = requests.find((row) => row["id"] === wideningId)!;
  const intent = RecallTraceRequestSchema.parse(JSON.parse(String(widening["redacted_request"])));
  expect(intent.kind).toBe("session");
  if (intent.kind !== "session") throw new Error("Expected a widening intent");
  expect(intent.previewDigest).toBe(String(outcomes[0]!["preview_digest"]));
  expect(await ownsRecallPreview(f.store, "reader", f.target, "revision-1", f.previewId)).toBe(
    true,
  );
});

test("concurrent repeated outcomes append once without retaining excerpt bodies", async () => {
  const f = await fixture();
  const body = "private archived evidence must not enter the derived ledger";
  const result = RecallResultSchema.parse({
    ...f.reply.result,
    hits: [
      {
        locator: f.locator,
        snapshotAt: "2026-09-20T00:00:00Z",
        title: null,
        workspace: null,
        repository: null,
        metadataOrigin: "archive",
        excerpt: {
          trust: "archived-untrusted",
          begin: RECALL_UNTRUSTED_BEGIN,
          text: body,
          end: RECALL_UNTRUSTED_END,
          maxBytes: 8192,
          bytes: body.length,
          truncated: false,
          firstRecord: 1,
          lastRecord: 1,
        },
      },
    ],
  });
  const reply: RecallReply = { ...f.reply, result };
  await Promise.all([recordRecallOutcome(f.store, reply), recordRecallOutcome(f.store, reply)]);
  const rows = await f.store.db.query("SELECT summary FROM recall_outcomes WHERE request_id = ?", [
    f.requestId,
  ]);
  expect(rows).toHaveLength(1);
  expect(String(rows[0]!["summary"])).not.toContain(body);
  await expect(
    f.store.db.run("DELETE FROM recall_outcomes WHERE request_id = ?", [f.requestId]),
  ).rejects.toThrow();
});

test("a lost start response is recovered by its owned trace without starting or publishing again", async () => {
  const f = await fixture();
  const nativeRequests: RecallServiceRequest[] = [];
  const control = { allowed: true, revision: "revision-1" };
  const context = (principalId = "reader"): GuestCtx =>
    ({
      traceId: 42,
      auth: { principal: { id: principalId }, allows: async () => control.allowed },
      services: {
        describeInstance: async () => ({
          owner: { machineId: f.target.machineId },
          configuration: {
            pluginId: BABEL_PLUGIN_ID,
            enabled: true,
            revision: control.revision,
          },
        }),
        invokeInstance: async (args: { input: { request: string } }) => {
          const frame = RecallServiceRequestSchema.parse(JSON.parse(args.input.request));
          nativeRequests.push(frame);
          if (frame.request.kind !== "poll") throw new Error("synthetic response lost after start");
          return { ok: true, result: { requestId: frame.requestId, state: "pending" } };
        },
      },
    }) as unknown as GuestCtx;
  const knock = async (name: string, ctx: GuestCtx, args: unknown): Promise<unknown> => {
    const door = recallDoors(openStore(f.store.db)).find((entry) => entry.action.name === name)!;
    return door.handler(ctx, door.action.input.parse(args) as never);
  };
  // Deliberately discard the start response, as a failed SDK projection does.
  await knock(ACTIONS.recallSearch, context(), { target: f.target, query: "archived" });
  const lookup = RecallPollInputSchema.parse({ target: f.target, traceId: 42 });
  const recovered = RecallPollReplySchema.parse(await knock(ACTIONS.recallPoll, context(), lookup));
  expect(recovered.state).toBe("located");
  expect(Object.keys(recovered).toSorted()).toEqual(["requestId", "state"]);
  expect(nativeRequests).toHaveLength(1);
  expect(recovered.requestId).toBe(nativeRequests[0]!.requestId);
  expect(await knock(ACTIONS.recallPoll, context("another-reader"), lookup)).toHaveProperty(
    "refused",
  );
  expect(
    await knock(ACTIONS.recallPoll, context(), {
      ...lookup,
      target: { ...f.target, operationId: "other-class" },
    }),
  ).toHaveProperty("refused");
  control.allowed = false;
  expect(await knock(ACTIONS.recallPoll, context(), lookup)).toHaveProperty("refused");
  control.allowed = true;
  expect(nativeRequests).toHaveLength(1);
  expect(
    await knock(ACTIONS.recallPoll, context(), {
      target: f.target,
      requestId: recovered.requestId,
    }),
  ).toEqual({ requestId: recovered.requestId, state: "pending" });
  expect(nativeRequests.map((frame) => frame.request.kind)).toEqual(["search", "poll"]);
  control.revision = "revision-2";
  expect(await knock(ACTIONS.recallPoll, context(), lookup)).toEqual({
    requestId: recovered.requestId,
    state: "expired",
  });
  expect(nativeRequests).toHaveLength(2);
});
