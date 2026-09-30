import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  AllocationPreviewInputSchema,
  AllocationPreviewSchema,
  AllocationVersionInputSchema,
  AllocationVersionResultSchema,
  AllocationVersionSchema,
  SaveAllocationInputSchema,
} from "../contract.ts";
import type { ActsStore } from "../store/acts.ts";
import { allocationVersion, previewAllocation, saveAllocation } from "../store/allocation.ts";
import { acted } from "./acts.ts";
import { defineDoor, type Door } from "./door.ts";

/** No delegated job, service, model or scheduler authority. All three doors are the owner's. */
export function allocationDoors(store: ActsStore): readonly Door[] {
  return [
    defineDoor(
      defineServerAction({
        name: ACTIONS.previewAllocation,
        title: "Preview or edit spend allocation without starting work",
        caps: [],
        input: AllocationPreviewInputSchema,
        result: AllocationPreviewSchema,
      }),
      async (ctx, input) => {
        if (!ctx.auth.isRoot) return { refused: "allocation planning is the owner's act" };
        return await acted(() => previewAllocation(store, input));
      },
    ),
    defineDoor(
      defineServerAction({
        name: ACTIONS.saveAllocation,
        title: "Save a named allocation intention, not execution authority",
        caps: [],
        input: SaveAllocationInputSchema,
        result: AllocationVersionSchema,
      }),
      async (ctx, input) => {
        if (!ctx.auth.isRoot) return { refused: "saving an allocation version is the owner's act" };
        return await acted(() => saveAllocation(store, input, ctx.principal.id));
      },
    ),
    defineDoor(
      defineServerAction({
        name: ACTIONS.allocationVersion,
        title: "Read a saved allocation version and its replayable basis",
        caps: [],
        input: AllocationVersionInputSchema,
        result: AllocationVersionResultSchema,
      }),
      async (ctx, input) => {
        if (!ctx.auth.isRoot) return { refused: "allocation versions are the owner's records" };
        return await allocationVersion(store, input.version);
      },
    ),
  ];
}
