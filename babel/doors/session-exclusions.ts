import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  RECALL_SERVICE_ID,
  PRIVACY_PROJECTION_BUILDING,
  ExcludeSessionInputSchema,
  ExcludeSessionResultSchema,
  SessionExclusionsResultSchema,
  SessionExclusionsSchema,
} from "../contract.ts";
import {
  readSessionExclusions,
  recallEnforcesSessionExclusions,
  sessionIsExcluded,
} from "../store/exclusions.ts";
import { activeDrains } from "../store/drains.ts";
import { excludeSessionWhenQuiescent, readExcludedRunIds } from "../store/source-privacy.ts";
import { backfillSourceDependencies } from "../store/source-dependencies.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";
import {
  enforceSessionExclusions,
  pauseRecallDisclosure,
  withRecallPolicyCutover,
} from "./recall-services.ts";

const OWNER = {
  caps: [],
  trace: "opaque",
} as const;

async function status(ctx: GuestCtx, store: BabelStore) {
  const rows = await store.db.query<{ selector: string; recorded_at: string }>(
    "SELECT selector, recorded_at FROM session_exclusions ORDER BY recorded_at, selector",
  );
  let recallEnforced = false;
  try {
    const description = await ctx.services.describeInstance({ serviceId: RECALL_SERVICE_ID });
    recallEnforced = await recallEnforcesSessionExclusions(store.db, description);
  } catch {
    // A missing observation is not evidence of an enforced native policy.
  }
  return {
    exclusions: rows.map((row) => ({ selector: row.selector, recordedAt: row.recorded_at })),
    recallEnforced,
    reason: recallEnforced ? "" : "Recall is unavailable until its exclusion policy is ready.",
  };
}

export function sessionExclusionDoors(store: BabelStore): readonly Door[] {
  return [
    defineDoor(
      defineServerAction({
        ...OWNER,
        name: ACTIONS.sessionExclusions,
        title: "Read owner-controlled conversation exclusions",
        input: ExcludeSessionInputSchema.pick({}).strict(),
        result: SessionExclusionsResultSchema,
      }),
      async (ctx) => {
        if (!ctx.auth.isRoot) return { refused: "Conversation exclusions require the owner." };
        return await status(ctx, store);
      },
    ),
    defineDoor(
      defineServerAction({
        ...OWNER,
        delegates: ["services:configure", "machines:read"],
        name: ACTIONS.excludeSession,
        title: "Exclude a conversation from all Babel model and Recall use, retaining its backup",
        input: ExcludeSessionInputSchema,
        result: ExcludeSessionResultSchema,
      }),
      async (ctx, { selector }) => {
        if (!ctx.auth.isRoot) return { refused: "Conversation exclusions require the owner." };
        if (!(await backfillSourceDependencies(store.db)))
          return { refused: PRIVACY_PROJECTION_BUILDING };
        return await withRecallPolicyCutover(store, async () => {
          try {
            if (!(await sessionIsExcluded(store.db, selector))) {
              const known = await store.db.query(
                "SELECT 1 FROM sessions WHERE selector = ? LIMIT 1",
                [selector],
              );
              if (known.length === 0)
                return { refused: "No catalogued conversation matches this selector." };
              const { payload } = await store.policy();
              if (payload.enabled === true || (await activeDrains(store)).length !== 0)
                return {
                  refused: "Pause Babel and stop its drains before excluding a conversation.",
                };
              SessionExclusionsSchema.parse([...(await readSessionExclusions(store.db)), selector]);
              const affected = await readExcludedRunIds(store.db, [selector]);
              const unsettled = await store.db.query<{ id: string }>(
                "SELECT id FROM runs WHERE closure IS NULL",
              );
              if (unsettled.some((run) => affected.has(run.id)))
                return {
                  refused: "Finish or cancel this source's in-flight work before excluding it.",
                };
              // The shared cutover keeps owner installs and other exclusions from reopening
              // disclosure between this revision revocation and the ledger/enforcement receipt.
              await pauseRecallDisclosure(ctx);
              const recorded = await excludeSessionWhenQuiescent(
                store.db,
                selector,
                ctx.auth.principal.id,
                new Date(store.now()).toISOString(),
              );
              if (!recorded) {
                await enforceSessionExclusions(ctx, store);
                return {
                  refused:
                    "Conversation work changed; keep Babel paused and retry after it settles.",
                };
              }
              store.touch();
            }
            const before = await status(ctx, store);
            if (!before.recallEnforced) await enforceSessionExclusions(ctx, store);
          } catch {
            // A durable ban is never rolled back because the native owner is unavailable. Reader
            // doors fail closed until the exact privacy policy is installed and ready.
            if (!(await sessionIsExcluded(store.db, selector)))
              return {
                refused: "Conversation exclusion could not be recorded; keep Babel paused.",
              };
          }
          const observed = await status(ctx, store);
          const exclusion = observed.exclusions.find((item) => item.selector === selector);
          if (exclusion === undefined)
            return { refused: "Conversation exclusion could not be verified." };
          return {
            ...exclusion,
            excluded: true as const,
            recallEnforced: observed.recallEnforced,
            reason: observed.reason,
          };
        });
      },
    ),
  ];
}
