import { createHash } from "node:crypto";
import { defineServerAction, type GuestCtx, type ServerHandler } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_ACTIONS,
  RefreshReviewReadingsInputSchema,
  ReviewReadingsResultSchema,
  reviewReadingProviderRevision,
  type ReviewReading,
} from "../contract.ts";
import { answersOf } from "./screen/screener.ts";
import { jevPolicyRevision, type JevAnswerStore, type JevServices } from "./server/credential.ts";
import { cachedJudge, requestFor, requestKey } from "./server/judge.ts";
import { positionOf } from "./tally/position.ts";

type ReadingDeps = Pick<GuestCtx, "actions" | "host"> & {
  readonly services: Pick<JevServices, "listInstances">;
  readonly answers?: Pick<JevAnswerStore, "get">;
};

/** No invocation handle: refresh cannot buy a judgement, including on a miss or a dry account. */
export async function refreshReviewReadings(deps: ReadingDeps, ids: readonly string[]) {
  const absent = {
    records: [],
    accepted: 0,
    reason: "no current cached advice; ordinary draw unchanged",
    funding: "unknown" as const,
  };
  try {
    const providerRevision = reviewReadingProviderRevision(await deps.host.roster());
    const policyRevision = await jevPolicyRevision(deps.services);
    if (providerRevision === null || policyRevision === null) return absent;
    const offered = ReviewReadingsResultSchema.parse(
      await deps.actions.call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.reviewReadings,
        input: { records: ids },
      }),
    );
    const readings: ReviewReading[] = [];
    for (const record of offered.records) {
      const request = requestFor(record.kind, record.text);
      const answer = cachedJudge(request, policyRevision, deps.answers);
      if (answer === undefined) continue;
      const position = positionOf(
        {
          id: record.recordId,
          revision: record.revision,
          kind: record.kind,
          title: "",
          text: record.text,
        },
        answersOf(answer),
      );
      readings.push({
        recordId: record.recordId,
        revision: record.revision,
        kind: record.kind,
        textDigest: createHash("sha256").update(record.text).digest("hex"),
        requestKey: requestKey(request),
        bankVersion: request.bankVersion,
        documentVersion: request.documentVersion,
        standing: position.standing,
        tally: position.tally,
        heard: position.heard,
        roster: position.roster,
      });
    }
    if (
      (await jevPolicyRevision(deps.services)) !== policyRevision ||
      reviewReadingProviderRevision(await deps.host.roster()) !== providerRevision
    )
      return absent;
    return ReviewReadingsResultSchema.parse(
      await deps.actions.call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.reviewReadings,
        input: { publish: { providerRevision, policyRevision, records: ids, readings } },
      }),
    );
  } catch {
    // Missing metadata, an older baseline or a refused optional handoff never blocks a sweep.
    return absent;
  }
}

export const refreshReviewReadingsAction = defineServerAction({
  name: JEV_ACTIONS.refreshReviewReadings,
  title: "Refresh ephemeral review advice from cached current judgements without spending",
  caps: ["containers:read"],
  delegates: ["services:invoke"],
  input: RefreshReviewReadingsInputSchema,
  result: ReviewReadingsResultSchema,
});
export const REVIEW_READING_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.refreshReviewReadings]: async (ctx, args: unknown) =>
    await refreshReviewReadings(
      {
        host: ctx.host,
        actions: ctx.actions,
        services: { listInstances: (input) => ctx.services.listInstances(input) },
      },
      RefreshReviewReadingsInputSchema.parse(args).records,
    ),
};
