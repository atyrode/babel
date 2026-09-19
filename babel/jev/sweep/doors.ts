import { defineServerAction, type ServerHandler } from "@manifold/plugin-kit/server";
import { JEV_ACTIONS, SweepInputSchema, SweepPlanSchema, SweptSchema } from "../../contract.ts";
import { sweep, sweepPlan } from "./sweep.ts";

/*
  THE DRIVER OF THE SWEEP (#356), AND THE ONLY NEW SURFACE.

  The part has no cycle, no job and no lifecycle hook, and the baseline deliberately has no edge
  to it: a baseline duty that called its part would make removing the part break the duty, which
  is exactly what optional means must not happen. So the owner drives a sweep by knocking here.

  Two actions rather than a `dryRun` flag, because the distinction is authority and spend, not a
  rendering option. `sweepPlan` reads the gap and answers the size without invoking the judgement
  service once. `sweep` reads at most `JEV_SWEEP_BATCH` records and may spend one service call per
  one. Both declare `containers:read`; neither can write, and the suggestions the second returns
  are for its caller to deliver. That ceiling is load-bearing: adding `containers:write` to the
  manifest would open every ruling door in the baseline, not only `babel.suggest`.
*/

export const sweepPlanAction = defineServerAction({
  name: JEV_ACTIONS.sweepPlan,
  title: "Size the pending corpus judgement before spending",
  caps: ["containers:read"],
  input: SweepInputSchema,
  result: SweepPlanSchema,
});

export const sweepAction = defineServerAction({
  name: JEV_ACTIONS.sweep,
  title: "Judge one bounded batch of the pending corpus",
  caps: ["containers:read"],
  input: SweepInputSchema,
  result: SweptSchema,
});

export const SWEEP_ACTIONS = [sweepPlanAction, sweepAction] as const;

/**
 * The action handlers under the names the declarations publish, kept beside the declarations so
 * a registration cannot carry one without the other.
 */
export const SWEEP_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.sweepPlan]: async (ctx, args: unknown) => {
    const parsed = SweepInputSchema.parse(args);
    return await sweepPlan(ctx.actions, { limit: parsed.limit, kinds: parsed.kinds });
  },
  [JEV_ACTIONS.sweep]: async (ctx, args: unknown) => {
    const parsed = SweepInputSchema.parse(args);
    return await sweep(
      { actions: ctx.actions, services: ctx.services },
      { limit: parsed.limit, kinds: parsed.kinds, after: parsed.after },
    );
  },
};
