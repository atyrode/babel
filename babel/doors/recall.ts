import {
  defineServerAction,
  type GuestCtx,
  type ServerActionDef,
} from "@manifold/plugin-kit/server";
import { z } from "zod";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  RECALL_MAX_REQUEST_BODY_BYTES,
  RECALL_RESULT_PROJECTION,
  RECALL_SERVICE_ID,
  RECALL_SKILL_PROJECTION,
  RECALL_SKILL_VERSION,
  RecallPollInputSchema,
  RecallPreviewInputSchema,
  RecallReplySchema,
  RecallSearchInputSchema,
  RecallServiceBodySchema,
  RecallSessionInputSchema,
  RecallShowInputSchema,
  RecallSkillSchema,
  type RecallReply,
  type RecallRequest,
  type RecallServiceRequest,
  type RecallTarget,
} from "../contract.ts";
import recallSkillBody from "../recall-skill.md" with { type: "text" };
import {
  ownsRecallPreview,
  readRecallRequest,
  recordRecallOutcome,
  startRecall,
} from "../store/recall.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

const READ = {
  caps: [],
  delegates: ["services:invoke"],
  requirements: [{ cap: "services:invoke", target: ["target"] }],
  trace: "opaque",
  result: RecallReplySchema,
  resultProjection: RECALL_RESULT_PROJECTION,
} satisfies Pick<
  ServerActionDef,
  "caps" | "delegates" | "requirements" | "trace" | "result" | "resultProjection"
>;

async function currentRevision(ctx: GuestCtx, target: RecallTarget): Promise<string | null> {
  if (!(await ctx.auth.allows("services:invoke", target))) return null;
  const description = await ctx.services.describeInstance({ serviceId: RECALL_SERVICE_ID });
  if (
    description.owner?.machineId !== target.machineId ||
    description.configuration?.pluginId !== BABEL_PLUGIN_ID ||
    !description.configuration.enabled
  )
    return null;
  return description.configuration.revision;
}

async function invoke(
  ctx: GuestCtx,
  store: BabelStore,
  target: RecallTarget,
  revision: string,
  frame: RecallServiceRequest,
  operation: RecallRequest["kind"],
): Promise<RecallReply> {
  let reply: RecallReply;
  try {
    const body = RecallServiceBodySchema.parse({ request: JSON.stringify(frame) });
    if (new TextEncoder().encode(JSON.stringify(body)).byteLength > RECALL_MAX_REQUEST_BODY_BYTES)
      throw new Error("Recall request exceeds its bound.");
    const called = await ctx.services.invokeInstance({
      serviceId: RECALL_SERVICE_ID,
      expectedRevision: revision,
      operationId: target.operationId,
      input: body,
    });
    if (!called.ok) {
      reply = { requestId: frame.requestId, state: "unavailable" };
    } else {
      const parsed = RecallReplySchema.safeParse(called.result);
      reply =
        parsed.success &&
        parsed.data.requestId === frame.requestId &&
        (parsed.data.result === undefined || parsed.data.result.operation === operation)
          ? parsed.data
          : { requestId: frame.requestId, state: "failed" };
    }
  } catch {
    // An interrupted native call may have started. Preserve its id for polling, never retry it
    // under another id or pretend the archive incurred zero cost.
    reply = { requestId: frame.requestId, state: "unavailable" };
  }
  try {
    await recordRecallOutcome(store, reply);
  } catch {
    // The intent is durable already. No data leaves this door before its outcome is ledgered.
    return { requestId: frame.requestId, state: "unavailable" };
  }
  return reply;
}

async function begin(
  ctx: GuestCtx,
  store: BabelStore,
  target: RecallTarget,
  request: RecallRequest,
): Promise<RecallReply | { refused: string }> {
  try {
    const revision = await currentRevision(ctx, target);
    if (revision === null)
      return { refused: "Recall requires an authorized owner-configured disclosure class." };
    if (
      request.kind === "session" &&
      !(await ownsRecallPreview(store, ctx.auth.principal.id, target, revision, request.previewId))
    )
      return { refused: "Whole-session widening requires this caller's completed size preview." };
    const requestId = await startRecall(store, ctx.auth.principal.id, target, revision, request);
    return await invoke(ctx, store, target, revision, { requestId, request }, request.kind);
  } catch {
    return { refused: "Recall could not record or authorize this request." };
  }
}

export function recallDoors(store: BabelStore): readonly Door[] {
  return [
    defineDoor(
      defineServerAction({
        name: ACTIONS.recallSkill,
        title: "Read the versioned archived Recall skill",
        caps: [],
        trace: "opaque",
        input: z.strictObject({}),
        result: RecallSkillSchema,
        resultProjection: RECALL_SKILL_PROJECTION,
      }),
      async () => ({ version: RECALL_SKILL_VERSION, body: recallSkillBody }),
    ),
    defineDoor(
      defineServerAction({
        ...READ,
        name: ACTIONS.recallSearch,
        title: "Search authorized archived conversations",
        input: RecallSearchInputSchema,
      }),
      async (ctx, { target, ...request }) =>
        begin(ctx, store, target, { kind: "search", ...request }),
    ),
    defineDoor(
      defineServerAction({
        ...READ,
        name: ACTIONS.recallShow,
        title: "Show bounded archived evidence at a locator",
        input: RecallShowInputSchema,
      }),
      async (ctx, { target, ...request }) =>
        begin(ctx, store, target, { kind: "show", ...request }),
    ),
    defineDoor(
      defineServerAction({
        ...READ,
        name: ACTIONS.recallPreview,
        title: "Preview redacted whole-session size without its content",
        input: RecallPreviewInputSchema,
      }),
      async (ctx, { target, locator }) => begin(ctx, store, target, { kind: "preview", locator }),
    ),
    defineDoor(
      defineServerAction({
        ...READ,
        name: ACTIONS.recallSession,
        title: "Explicitly widen a previewed archived session",
        input: RecallSessionInputSchema,
      }),
      async (ctx, { target, ...request }) =>
        begin(ctx, store, target, { kind: "session", ...request }),
    ),
    defineDoor(
      defineServerAction({
        ...READ,
        name: ACTIONS.recallPoll,
        title: "Poll this caller's bounded Recall request",
        input: RecallPollInputSchema,
      }),
      async (ctx, { target, requestId }) => {
        try {
          const owned = await readRecallRequest(store, ctx.auth.principal.id, requestId);
          if (owned === null || JSON.stringify(owned.target) !== JSON.stringify(target))
            return { refused: "Unknown Recall request for this caller and disclosure class." };
          const revision = await currentRevision(ctx, target);
          if (revision === null) return { refused: "Recall disclosure authority is unavailable." };
          if (revision !== owned.revision) {
            const reply: RecallReply = { requestId, state: "expired" };
            await recordRecallOutcome(store, reply);
            return reply;
          }
          return await invoke(
            ctx,
            store,
            target,
            revision,
            { requestId, request: { kind: "poll" } },
            owned.operation,
          );
        } catch {
          return { refused: "Recall could not read or authorize this request." };
        }
      },
    ),
  ];
}
