import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  JEV_PLUGIN_ID,
  ReviewReadingsInputSchema,
  ReviewReadingsResultSchema,
} from "../contract.ts";
import type { ReviewReadings } from "../server/review-readings.ts";
import { defineDoor } from "./door.ts";

/**
 * Ephemeral advisory publication, deliberately not a durable write or a scheduling capability.
 * The caller needs only containers:read. Existing service delegates lend metadata visibility,
 * not a new grant; verified immediate plugin attribution admits the only provider.
 */
export function reviewReadingsDoor(readings: ReviewReadings) {
  return defineDoor(
    defineServerAction({
      name: ACTIONS.reviewReadings,
      title: "Publish optional cached review advice into the ephemeral inbox",
      caps: ["containers:read"],
      delegates: ["services:read", "services:invoke"],
      trace: "opaque",
      input: ReviewReadingsInputSchema,
      result: ReviewReadingsResultSchema,
    }),
    async (ctx, args) => {
      try {
        if (ctx.callerPlugin !== JEV_PLUGIN_ID)
          return { refused: "only the authenticated Jev part may hand off review readings" };
      } catch {
        return { refused: "the host did not authenticate the reading provider" };
      }
      if ("records" in args)
        return {
          records: await readings.records(args.records),
          accepted: 0,
          reason: "current record text for cache-only lookup",
          funding: "unknown" as const,
        };
      const accepted = await readings.publish(
        {
          host: ctx.host,
          services: { listInstances: (args) => ctx.services.listInstances(args) },
        },
        args.publish,
      );
      return {
        records: [],
        accepted,
        reason:
          accepted > 0
            ? "current cached advice held ephemerally"
            : "no current cached advice accepted; ordinary draw unchanged",
        funding: "unknown" as const,
      };
    },
  );
}
