import { defineServerAction, type GuestCtx, type ServerActionDef } from "@manifold/plugin-kit/server";
import {
  ACTIONS, BABEL_PLUGIN_ID, RECALL_MAX_REQUEST_BODY_BYTES, RECALL_MAX_RESULT_BYTES, RECALL_SERVICE_ID,
  TRANSCRIPT_MAP_MAX_CAPTURES, TRANSCRIPT_MAP_LOCATE_RESULT_PROJECTION,
  TRANSCRIPT_MAP_READ_RESULT_PROJECTION, TRANSCRIPT_MAP_RESULT_PROJECTION,
  RecallServiceBodySchema, TranscriptMapCaptureSchema, TranscriptMapLocateInputSchema, TranscriptMapLocateReplySchema,
  TranscriptMapNativeReplySchema, TranscriptMapReadInputSchema, TranscriptMapReadReplySchema,
  TranscriptMapRegenerateInputSchema, TranscriptMapRegenerateReplySchema, TranscriptMapSourceInputSchema,
  TranscriptMapTargetSchema,
  type TranscriptMapCapture, type TranscriptMapNativeReply, type TranscriptMapNativeRequest, type TranscriptMapNativeResult,
  type TranscriptMapReadRequest, type TranscriptMapReadResult, type TranscriptMapReadTrace,
  type TranscriptMapTarget, type TranscriptMapView,
} from "../contract.ts";
import { transcriptMapReads, type MapReadRequest } from "../store/transcript-map-reads.ts";
import { transcriptMaps } from "../store/transcript-maps.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

const READ = {
  caps: ["services:invoke"], delegates: ["services:invoke"],
  requirements: [{ cap: "services:invoke", target: ["target"] }], trace: "opaque",
} satisfies Pick<ServerActionDef, "caps" | "delegates" | "requirements" | "trace">;
const REGENERATE_PROJECTION = { ...TRANSCRIPT_MAP_LOCATE_RESULT_PROJECTION, fields: [["requestId"], ["state"], ["generation"]] };

async function currentRevision(ctx: GuestCtx, target: TranscriptMapTarget): Promise<string | null> {
  if (!TranscriptMapTargetSchema.safeParse(target).success || !(await ctx.auth.allows("services:invoke", target))) return null;
  const description = await ctx.services.describeInstance({ serviceId: RECALL_SERVICE_ID });
  if (description.owner?.machineId !== target.machineId || description.configuration?.pluginId !== BABEL_PLUGIN_ID ||
    !description.configuration.enabled) return null;
  return description.configuration.revision;
}
class Interrupted extends Error {
  constructor(readonly state: Exclude<TranscriptMapNativeReply["state"], "complete">) { super(state); }
}
type Input = { target: TranscriptMapTarget; requestId?: string | undefined } & (
  { operation: "read"; request: TranscriptMapReadRequest } |
  { operation: "source"; versionId: string; nodeId: string; maxBytes: number } |
  { operation: "regenerate"; captureId: string; reason: string }
);

export function transcriptMapDoors(store: BabelStore): readonly Door[] {
  const maps = transcriptMaps(store);
  const traces = transcriptMapReads(store);
  async function execute(ctx: GuestCtx, input: Input) {
    let held: MapReadRequest | undefined;
    let round = 0;
    try {
      const revision = await currentRevision(ctx, input.target);
      if (revision === null || (input.operation === "regenerate" && !(await ctx.auth.allows("containers:write"))))
        return { refused: "Transcript maps require the exact native map class and, for regeneration, operator write authority." };
      const { target, requestId, ...request } = input;
      held = await traces.begin({ principal: ctx.auth.principal.id, traceId: ctx.traceId, target, revision,
        operation: input.operation, request, ...(requestId === undefined ? {} : { requestId }) });
      round = await traces.round(held.id);
      if (held.revision !== revision) throw new Interrupted("expired");
      const owned = held;
      async function native(stage: string, request: TranscriptMapNativeRequest, source = false): Promise<{ result: TranscriptMapNativeResult; request: TranscriptMapNativeRequest }> {
        const step = await traces.native(owned.id, source ? 0 : round, stage, request);
        let reply: TranscriptMapNativeReply;
        try {
          const body = RecallServiceBodySchema.parse({ request: JSON.stringify({ requestId: step.id,
            request: step.fresh ? step.request : { kind: "poll" } }) });
          if (Buffer.byteLength(JSON.stringify(body)) > RECALL_MAX_REQUEST_BODY_BYTES) throw new Interrupted("failed");
          const called = await ctx.services.invokeInstance({ serviceId: RECALL_SERVICE_ID, expectedRevision: revision!,
            operationId: target.operationId, input: body });
          if (!called.ok) throw new Interrupted("unavailable");
          reply = TranscriptMapNativeReplySchema.parse(called.result);
          if (reply.requestId !== step.id || (reply.result && reply.result.operation !== step.request.kind) ||
            Buffer.byteLength(JSON.stringify(reply)) > RECALL_MAX_RESULT_BYTES) throw new Interrupted("failed");
        } catch (error) {
          // Once its ID is recorded even an interrupted dispatch can only be polled, never reposted.
          throw error instanceof Interrupted ? error : new Interrupted("unavailable");
        }
        if (reply.state !== "complete") throw new Interrupted(reply.state);
        if (!reply.result || (!source && reply.result.refusal !== null) || !reply.result.context ||
          `map.${reply.result.context.classId}` !== target.operationId) throw new Interrupted("failed");
        return { result: reply.result, request: step.request };
      }
      const initial = await native("context", { kind: "map-context" });
      const referenceInput = input.operation === "source" ? input :
        input.operation === "read" && "versionId" in input.request ? input.request : null;
      const requestedReference = referenceInput
        ? await maps.reference(target.machineId, referenceInput.versionId, referenceInput.nodeId) : null;
      let captures: TranscriptMapCapture[];
      let inventoryPartial = false;
      if (input.operation === "read" && (input.request.kind === "status" || (input.request.kind === "coverage" && input.request.captureId === undefined))) {
        const inventory = await native("inventory", { kind: "map-inventory", maxCaptures: TRANSCRIPT_MAP_MAX_CAPTURES });
        captures = inventory.result.entries.map((entry) => entry.capture);
        inventoryPartial = inventory.result.nextCursor !== null;
      } else if (requestedReference) {
        captures = [TranscriptMapCaptureSchema.parse({ id: requestedReference.source.id, host: requestedReference.source.host,
          harness: requestedReference.source.harness, session: requestedReference.source.session,
          snapshot: requestedReference.source.snapshot, path: requestedReference.source.path, capturedAt: requestedReference.source.capturedAt })];
      } else {
        captures = await maps.candidates(target.machineId, input.operation === "regenerate" ? { captureId: input.captureId } :
          input.operation === "source" ? { versionId: input.versionId, nodeId: input.nodeId } : input.request.kind === "search" ? { query: input.request.query } :
            input.request.kind === "coverage" ? { ...(input.request.captureId === undefined ? {} : { captureId: input.request.captureId }) } :
              "versionId" in input.request ? { versionId: input.request.versionId, nodeId: input.request.nodeId } : {});
        inventoryPartial = input.operation === "read" && input.request.kind === "search";
      }
      while (captures.length > 0) {
        const body = { request: JSON.stringify({ requestId: owned.id, request: { kind: "map-authorize", captures } }) };
        if (RecallServiceBodySchema.safeParse(body).success && Buffer.byteLength(JSON.stringify(body)) <= RECALL_MAX_REQUEST_BODY_BYTES) break;
        captures.pop();
        inventoryPartial = true;
      }
      const authorized = await native("authorize", { kind: "map-authorize", captures });
      if (authorized.request.kind !== "map-authorize") throw new Interrupted("failed");
      // On resume the durable selection, not a newly changed FTS window, is the one authorized.
      captures = authorized.request.captures;
      const context = authorized.result.context!;
      if (context.digest !== initial.result.context!.digest) throw new Interrupted("expired");
      const entries = captures.flatMap((capture) => {
        const access = authorized.result.accesses.find((item) => item.captureId === capture.id &&
          item.contextDigest === context.digest && item.sensitivity <= context.ceiling);
        return access ? [{ capture, access }] : [];
      });
      let sourceResult: TranscriptMapNativeResult | undefined;
      if (input.operation === "source") {
        if (!requestedReference || !entries.some((entry) => entry.capture.id === requestedReference.source.id)) throw new Interrupted("failed");
        const span = await native("source", { kind: "map-span", source: requestedReference.source,
          span: requestedReference.node.span, maxBytes: input.maxBytes }, true);
        sourceResult = span.result;
        const returned = sourceResult.span;
        if (sourceResult.context!.digest !== context.digest) throw new Interrupted("expired");
        if (sourceResult.refusal === null) {
          if (!returned || JSON.stringify(returned.source) !== JSON.stringify(requestedReference.source) ||
            JSON.stringify(returned.span) !== JSON.stringify(requestedReference.node.span) ||
            returned.excerpt.bytes > input.maxBytes || returned.excerpt.bytes > returned.span.byteLength ||
            returned.excerpt.truncated !== (returned.excerpt.bytes < returned.span.byteLength) ||
            returned.excerpt.bytes !== Buffer.byteLength(returned.excerpt.text) || returned.excerpt.maxBytes !== input.maxBytes)
            throw new Interrupted("failed");
        } else if (returned !== undefined) throw new Interrupted("failed");
      }
      // A separate native observation after the access/source steps prevents old cached prose
      // from being returned just because an earlier round once authorized its capture.
      const fresh = await native("disclosure-context", { kind: "map-context" });
      const current = fresh.result.context!;
      if (current.digest !== context.digest || current.classId !== context.classId || current.ceiling !== context.ceiling ||
        await currentRevision(ctx, target) !== revision) throw new Interrupted("expired");
      const scope = { machineId: target.machineId, context: current, captureIds: entries.map((entry) => entry.capture.id) };
      await maps.recordAccess({ ...scope, entries, now: new Date().toISOString() });
      if (input.operation === "regenerate") {
        if (!entries.some((entry) => entry.capture.id === input.captureId) || !(await ctx.auth.allows("containers:write"))) throw new Interrupted("failed");
        // The durable intent binds the original reason digest. No potentially secret reason is stored.
        const generation = await maps.regenerate({ captureId: input.captureId, requestId: owned.id,
          reason: `Explicit map regeneration request ${owned.id}`, now: new Date().toISOString() });
        const reply = TranscriptMapRegenerateReplySchema.parse({ requestId: owned.id, state: "complete", generation });
        await traces.outcome(owned.id, round, { state: "complete", summaries: [], generation });
        return reply;
      }
      if (input.operation === "source") {
        const reply = TranscriptMapNativeReplySchema.parse({ requestId: owned.id, state: "complete", result: sourceResult });
        const span = sourceResult!.span;
        await traces.outcome(owned.id, round, { state: "complete", summaries: [],
          ...(span === undefined ? {} : { source: { captureId: span.source.id,
            span: span.span, servedBytes: span.excerpt.bytes, truncated: span.excerpt.truncated } }),
          ...(round === 0 ? { cost: sourceResult!.cost } : {}) });
        return reply;
      }
      const read = input.request;
      let views: TranscriptMapView[] = [];
      let childCount = 0;
      if (read.kind === "search") views = await maps.search(scope, read.query, read.limit);
      else if (read.kind === "node") {
        const view = await maps.node(scope, read.versionId, read.nodeId);
        if (view) views = [view];
      } else if (read.kind === "children") {
        const parent = await maps.node(scope, read.versionId, read.nodeId);
        childCount = parent?.node.children.length ?? 0;
        // Do not load or mark unreturned siblings or parent input summaries as served.
        for (const id of parent?.node.children.slice(read.offset, read.offset + read.limit) ?? []) {
          const view = await maps.node(scope, read.versionId, id);
          if (!view) throw new Interrupted("failed");
          views.push(view);
        }
      } else if (read.kind === "ancestors") views = await maps.ancestors(scope, read.versionId, read.nodeId);
      const historical = referenceInput
        ? views[0] ?? await maps.node(scope, referenceInput.versionId, referenceInput.nodeId) : null;
      const coverage = historical ? { ...historical.coverage } :
        await maps.coverage(scope, read.kind === "coverage" ? read.captureId : requestedReference?.source.id);
      // Historical version coverage must not be replaced by the current head's status.
      for (const view of views) { coverage.stale ||= view.coverage.stale; coverage.partial ||= view.coverage.partial; }
      if (inventoryPartial) { coverage.partial = true; coverage.tailBytes = null; }
      const result: TranscriptMapReadResult = {
        operation: read.kind, inference: true, views: views.map(({ inference: _inference, coverage: _coverage, node, summary, ...view }) => {
          const { planId: _planId, ...compact } = node;
          return { ...view, node: compact, ...(summary === null ? {} : { summary }) };
        }), coverage, status: await maps.status(scope),
        nextOffset: read.kind === "children" && read.offset + views.length < childCount ? read.offset + views.length : null,
      };
      result.status.partial ||= inventoryPartial;
      const reply = TranscriptMapReadReplySchema.parse({ requestId: owned.id, state: "complete", result });
      while (Buffer.byteLength(JSON.stringify(reply)) > RECALL_MAX_RESULT_BYTES && reply.result!.views.length > 0) {
        reply.result!.views.pop();
        reply.result!.coverage.partial = true;
        reply.result!.coverage.tailBytes = null;
        if (read.kind === "children") reply.result!.nextOffset = read.offset + reply.result!.views.length;
      }
      if (Buffer.byteLength(JSON.stringify(reply)) > RECALL_MAX_RESULT_BYTES || (views.length > 0 && reply.result!.views.length === 0))
        throw new Interrupted("failed");
      const final = TranscriptMapReadReplySchema.parse(reply);
      const outcome: TranscriptMapReadTrace = { state: "complete", summaries: final.result!.views.map((view) => ({
        versionId: view.versionId, nodeId: view.node.id, summaryId: view.summary?.id ?? null,
      })) };
      await traces.outcome(owned.id, round, outcome);
      return final;
    } catch (error) {
      if (!held) return { refused: "Transcript map request could not be recorded or authorized." };
      const state = error instanceof Interrupted ? error.state : "unavailable";
      try { await traces.outcome(held.id, round, { state, summaries: [] }); } catch { return { requestId: held.id, state: "unavailable" as const }; }
      return { requestId: held.id, state };
    }
  }
  return [
    defineDoor(defineServerAction({ ...READ, name: ACTIONS.mapRead, title: "Navigate authorized immutable transcript maps",
      input: TranscriptMapReadInputSchema, result: TranscriptMapReadReplySchema, resultProjection: TRANSCRIPT_MAP_READ_RESULT_PROJECTION }),
    async (ctx, input) => {
      const reply = await execute(ctx, { ...input, operation: "read" });
      return "refused" in reply && typeof reply.refused === "string" ? { refused: reply.refused } : TranscriptMapReadReplySchema.parse(reply);
    }),
    defineDoor(defineServerAction({ ...READ, name: ACTIONS.mapSource, title: "Read a map node's bounded canonical source",
      input: TranscriptMapSourceInputSchema, result: TranscriptMapNativeReplySchema, resultProjection: TRANSCRIPT_MAP_RESULT_PROJECTION }),
    async (ctx, input) => {
      const reply = await execute(ctx, { ...input, operation: "source" });
      return "refused" in reply && typeof reply.refused === "string" ? { refused: reply.refused } : TranscriptMapNativeReplySchema.parse(reply);
    }),
    defineDoor(defineServerAction({ ...READ, name: ACTIONS.mapLocate, title: "Locate this caller's durable map request",
      input: TranscriptMapLocateInputSchema, result: TranscriptMapLocateReplySchema, resultProjection: TRANSCRIPT_MAP_LOCATE_RESULT_PROJECTION }),
    async (ctx, { target, ...reference }) => {
      try {
        const owned = await traces.locate(ctx.auth.principal.id, reference);
        if (!owned || JSON.stringify(owned.target) !== JSON.stringify(target)) return { refused: "Unknown map request for this caller and class." };
        const revision = await currentRevision(ctx, target);
        if (revision === null) return { refused: "Transcript map disclosure authority is unavailable." };
        if (revision !== owned.revision) {
          await traces.outcome(owned.id, await traces.round(owned.id), { state: "expired", summaries: [] });
          return { refused: "Transcript map request expired with its configuration revision." };
        }
        return { requestId: owned.id, state: "located" as const };
      } catch { return { refused: "Transcript map request could not be located or authorized." }; }
    }),
    defineDoor(defineServerAction({ ...READ, caps: ["services:invoke", "containers:write"], delegates: ["services:invoke", "containers:write"],
      name: ACTIONS.regenerateMap, title: "Request bounded explicit transcript map regeneration",
      input: TranscriptMapRegenerateInputSchema, result: TranscriptMapRegenerateReplySchema, resultProjection: REGENERATE_PROJECTION }),
    async (ctx, input) => {
      const reply = await execute(ctx, { ...input, operation: "regenerate" });
      return "refused" in reply && typeof reply.refused === "string" ? { refused: reply.refused } : TranscriptMapRegenerateReplySchema.parse(reply);
    }),
  ];
}
