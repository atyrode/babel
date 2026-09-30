import { defineServerAction, type ServerHandler } from "@manifold/plugin-kit/server";
import {
  DuplicatePlanInputSchema,
  DuplicateSweepInputSchema,
  DuplicateSweepPlanSchema,
  DuplicateSweepReportSchema,
  JEV_ACTIONS,
} from "../../contract.ts";
import { duplicates, duplicatesPlan } from "./pass.ts";

export const duplicatesPlanAction = defineServerAction({
  name: JEV_ACTIONS.duplicatesPlan,
  title: "Plan a bounded duplicate-record judgement without spending",
  caps: ["containers:read"],
  input: DuplicatePlanInputSchema,
  result: DuplicateSweepPlanSchema,
});

export const duplicatesAction = defineServerAction({
  name: JEV_ACTIONS.duplicates,
  title: "Judge planned duplicate candidates and preview advisory cluster suggestions",
  caps: ["containers:read"],
  input: DuplicateSweepInputSchema,
  result: DuplicateSweepReportSchema,
});

export const DUPLICATE_ACTIONS = [duplicatesPlanAction, duplicatesAction] as const;

export const DUPLICATE_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.duplicatesPlan]: async (ctx, args: unknown) =>
    await duplicatesPlan(
      { actions: ctx.actions, services: ctx.services },
      DuplicatePlanInputSchema.parse(args),
    ),
  [JEV_ACTIONS.duplicates]: async (ctx, args: unknown) =>
    await duplicates(
      { actions: ctx.actions, services: ctx.services },
      DuplicateSweepInputSchema.parse(args),
    ),
};
