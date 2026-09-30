import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  DuplicateAppliedSchema,
  DuplicateApplyInputSchema,
  DuplicatePlanInputSchema,
  DuplicatePlanSchema,
  DuplicatePreviewInputSchema,
  DuplicatePreviewSchema,
  EVENTS,
} from "../contract.ts";
import { ActRefused, type ActsStore } from "../store/acts.ts";
import { duplicateApply, duplicatePlan, duplicatePreview } from "../store/duplicates.ts";
import { acted } from "./acts.ts";
import { defineDoor, type Door } from "./door.ts";

const planAction = defineServerAction({
  name: ACTIONS.duplicatePlan,
  title: "Read a bounded duplicate-candidate plan without judgement or writes",
  caps: ["containers:read"],
  input: DuplicatePlanInputSchema,
  result: DuplicatePlanSchema,
});
const previewAction = defineServerAction({
  name: ACTIONS.duplicatePreview,
  title: "Preview exact duplicate corroboration links (owner only)",
  // Read-only, but intentionally outside a read-only adviser's authority ceiling.
  caps: ["containers:write"],
  input: DuplicatePreviewInputSchema,
  result: DuplicatePreviewSchema,
});
const applyAction = defineServerAction({
  name: ACTIONS.duplicateApply,
  title: "Explicitly apply previewed duplicate corroboration links (owner only)",
  caps: ["containers:write"],
  input: DuplicateApplyInputSchema,
  result: DuplicateAppliedSchema,
});

export function duplicateDoors(store: ActsStore): readonly Door[] {
  return [
    defineDoor(planAction, async (_ctx, args) => acted(async () => duplicatePlan(store, args))),
    defineDoor(previewAction, async (ctx, args) =>
      acted(async () => {
        if (!ctx.auth.isRoot) throw new ActRefused("duplicate preview is the owner's act");
        return await duplicatePreview(store, args.nextActionId);
      }),
    ),
    defineDoor(applyAction, async (ctx, args) =>
      acted(async () => {
        if (!ctx.auth.isRoot) throw new ActRefused("duplicate application is the owner's act");
        const result = await duplicateApply(store, args, ctx.principal.id);
        ctx.emit({ kind: "plugin", pluginId: BABEL_PLUGIN_ID }, EVENTS.recordWritten, {
          id: result.nextActionId,
          recordId: result.recordId,
        });
        return result;
      }),
    ),
  ];
}
