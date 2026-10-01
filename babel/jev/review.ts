import { createHash } from "node:crypto";
import { defineServerAction, type GuestCtx, type ServerHandler } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_ACTIONS,
  JEV_ASK_CAP,
  JEV_REVIEW_INPUT_BYTES,
  JEV_REVIEW_RESULT_PROJECTION,
  JevReviewInputSchema,
  JevReviewResultSchema,
  ReviewJudgmentContextSchema,
  reviewReadingProviderRevision,
  type JevReviewResult,
} from "../contract.ts";
import { bankFor } from "./bank/bank.ts";
import { answersOf } from "./screen/screener.ts";
import {
  jevPolicyRevision,
  withinCallCap,
  JEV_SERVICE,
  type JevServices,
} from "./server/credential.ts";
import { judge, requestFor, requestKey } from "./server/judge.ts";

const ENCODER = new TextEncoder();
type ReviewContext = Pick<GuestCtx, "agentRun" | "actions" | "host"> & { services: JevServices };

/** Optional assistance only. The host binds the actor; Babel owns scope, attempts and fences. */
export async function reviewJudgment(ctx: ReviewContext, args: unknown): Promise<JevReviewResult> {
  const input = JevReviewInputSchema.parse(args);
  const absent = { key: input.key, status: "absent" as const };
  if (
    !ctx.agentRun ||
    input.state.trim() === "" ||
    ENCODER.encode(JSON.stringify(input)).byteLength > JEV_REVIEW_INPUT_BYTES ||
    !withinCallCap({ [JEV_SERVICE.stateField]: input.state })
  )
    return absent;
  try {
    const provider = reviewReadingProviderRevision(await ctx.host.roster());
    const policy = await jevPolicyRevision(ctx.services);
    if (provider === null || policy === null) return absent;
    const stateDigest = createHash("sha256").update(input.state).digest("hex");
    const context = async (phase: "reserve" | "check") =>
      ReviewJudgmentContextSchema.parse(
        await ctx.actions.call({
          plugin: BABEL_PLUGIN_ID,
          action: ACTIONS.reviewJudgmentContext,
          input: { phase, key: input.key, stateDigest },
        }),
      );
    const scope = await context("reserve");
    if (
      scope.agentId !== ctx.agentRun.agentId ||
      scope.agentRunId !== ctx.agentRun.runId ||
      scope.key !== input.key ||
      scope.stateDigest !== stateDigest
    )
      return absent;
    const request = requestFor(scope.kind, input.state);
    // Reuse the reviewed service, bank and memo. Ready is not a credit observation: a refused
    // invocation, including out of credit, is absence and cannot turn into a supporting vote.
    const answer = await judge(ctx.services, request, undefined, policy);
    if (answer === null) return absent;
    const leaves = answersOf(answer);
    const answers = bankFor(scope.kind).questions.flatMap(({ id }) => {
      const value = leaves[id];
      return value === undefined ? [] : [{ question: id, answer: value }];
    });
    const result = JevReviewResultSchema.safeParse({
      ...scope,
      status: "judged",
      requestKey: requestKey(request),
      bankVersion: request.bankVersion,
      documentVersion: request.documentVersion,
      answers,
    });
    if (
      !result.success ||
      ENCODER.encode(JSON.stringify(result.data)).byteLength >
        JEV_REVIEW_RESULT_PROJECTION.maxResultBytes
    )
      return absent;
    if (
      (await jevPolicyRevision(ctx.services)) !== policy ||
      reviewReadingProviderRevision(await ctx.host.roster()) !== provider ||
      JSON.stringify(await context("check")) !== JSON.stringify(scope)
    )
      return absent;
    return result.data;
  } catch {
    // No provider exception, private state or fabricated judgment crosses the optional edge.
    // Stop, expiry, reassignment and consumed attempt keys all leave ordinary review intact.
    return absent;
  }
}

export const reviewJudgmentAction = defineServerAction({
  name: JEV_ACTIONS.ask,
  title: "Optionally grade retrieved review state; never submit an assessment or ruling",
  caps: [JEV_ASK_CAP],
  delegates: ["services:invoke"],
  trace: "opaque",
  input: JevReviewInputSchema,
  result: JevReviewResultSchema,
  resultProjection: JEV_REVIEW_RESULT_PROJECTION,
});
export const REVIEW_JUDGMENT_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.ask]: async (ctx, args: unknown) => await reviewJudgment(ctx, args),
};
