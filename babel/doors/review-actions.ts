import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  JEV_PLUGIN_ID,
  REVIEW_ACTION_CAP,
  REVIEW_ACTION_RESULT_PROJECTION,
  ReviewActionReceiptSchema,
  ReviewJudgmentContextInputSchema,
  ReviewJudgmentContextSchema,
} from "../contract.ts";
import { ReviewActionInputSchema } from "../machine/results.ts";
import type { ActsStore } from "../store/acts.ts";
import { reviewAction, reviewJudgmentContext } from "../store/review-actions.ts";
import { acted } from "./acts.ts";
import { defineDoor, type Door } from "./door.ts";

export const reviewActionDefinition = defineServerAction({
  name: ACTIONS.reviewAction,
  title: "Submit a durable review assessment, refinement or completion marker",
  caps: [REVIEW_ACTION_CAP],
  input: ReviewActionInputSchema,
  result: ReviewActionReceiptSchema,
  resultProjection: REVIEW_ACTION_RESULT_PROJECTION,
});

/** Only host-authenticated Agent Run provenance may reach the authoritative intake. */
export function reviewActionDoors(store: ActsStore): readonly Door[] {
  return [
    defineDoor(reviewActionDefinition, async (ctx, input) => {
      if (ctx.callerPlugin === JEV_PLUGIN_ID)
        return { refused: "Jev supplies optional judgment, never durable review actions" };
      const actor = ctx.agentRun;
      if (actor === null || actor === undefined) {
        return {
          refused:
            "review actions require a governed Agent Run; operator authority is not review authority",
        };
      }
      return await acted(() => reviewAction(store, actor, input));
    }),
    defineDoor(
      defineServerAction({
        name: ACTIONS.reviewJudgmentContext,
        title: "Reserve or fence an optional judgment for the authenticated review Run",
        caps: [REVIEW_ACTION_CAP],
        trace: "opaque",
        input: ReviewJudgmentContextInputSchema,
        result: ReviewJudgmentContextSchema,
      }),
      async (ctx, input) => {
        if (ctx.callerPlugin !== JEV_PLUGIN_ID || !ctx.agentRun)
          return { refused: "only Jev serving an authenticated review Run may check its scope" };
        return await acted(() => reviewJudgmentContext(store, ctx.agentRun!, input));
      },
    ),
  ];
}
