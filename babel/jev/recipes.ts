import {
  defineServerAction,
  type GuestActions,
  type ServerHandler,
} from "@manifold/plugin-kit/server";
import { z } from "zod";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_ACTIONS,
  PolicyResultSchema,
  RECORD_KINDS,
  RecipeStandingSchema,
  type RecipeStanding,
} from "../contract.ts";
import { BANK } from "./bank/bank.ts";
import { answersOf } from "./screen/screener.ts";
import { jevPolicyRevision, type JevAnswerStore, type JevServices } from "./server/credential.ts";
import { cachedJudge, requestFor } from "./server/judge.ts";
import { basisFor } from "./sweep/sweep.ts";
import { positionOf } from "./tally/position.ts";

type Counts = NonNullable<RecipeStanding>["counts"];
function emptyCounts(eligible: number): Counts {
  return {
    knownCached: 0,
    missingOrEvicted: 0,
    notInspected: eligible,
    unjudged: null,
    bands: { unjudged: 0, unheard: 0, unremarked: 0, backed: 0, objected: 0, contested: 0 },
  };
}

/**
 * Complete eligibility counts; explicitly partial positions from the existing process memo.
 * Only the newest bounded current-record handoff is inspected. A cache miss cannot distinguish
 * never judged from evicted, and the free roster cannot distinguish ready from funded.
 * Nothing is stored, no sweep runs, and even a cold lookup has no route to an invocation.
 */
export async function recipeStanding(deps: {
  readonly actions: GuestActions;
  readonly services: Pick<JevServices, "listInstances">;
  readonly answers?: Pick<JevAnswerStore, "get">;
}): Promise<RecipeStanding> {
  const policyRevision = await jevPolicyRevision(deps.services);
  if (policyRevision === null) return null;
  try {
    const policy = PolicyResultSchema.parse(
      await deps.actions.call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.policy,
        input: { recipeRecords: true },
      }),
    );
    const census = policy.recipeRecords;
    if (census === undefined) return null;
    const rows = new Map<string, NonNullable<RecipeStanding>["recipes"][number]>();
    for (const recipe of policy.recipes) {
      rows.set(recipe.id, { recipeId: recipe.id, eligible: 0, ...emptyCounts(0) });
    }
    for (const row of census.recipes) {
      rows.set(row.recipeId, { ...row, ...emptyCounts(row.eligible) });
    }
    const counts = emptyCounts(census.eligible);
    for (const record of census.records) {
      const row = rows.get(record.recipeId);
      if (row === undefined || record.text === null) continue;
      const answer = cachedJudge(
        requestFor(record.kind, record.text),
        policyRevision,
        deps.answers,
      );
      row.notInspected -= 1;
      counts.notInspected -= 1;
      if (answer === undefined) {
        row.missingOrEvicted += 1;
        counts.missingOrEvicted += 1;
        continue;
      }
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
      row.knownCached += 1;
      counts.knownCached += 1;
      row.bands[position.standing] += 1;
      counts.bands[position.standing] += 1;
    }
    // Disabling/reconfiguring Jev while the baseline read was in flight invalidates the batch.
    // A ready roster still says nothing about funding, including after a cached hit.
    if ((await jevPolicyRevision(deps.services)) !== policyRevision || counts.knownCached === 0) {
      return null;
    }
    return {
      observedAt: new Date().toISOString(),
      bankVersion: BANK.version,
      policyRevision,
      basis: RECORD_KINDS.map((kind) => ({ kind, basis: basisFor(kind, policyRevision) })),
      funding: "unknown",
      coverage: "partial-cache",
      total: census.total,
      eligible: census.eligible,
      excluded: census.excluded,
      multiRecipe: census.multiRecipe,
      counts,
      recipes: [...rows.values()],
    };
  } catch {
    // An older/unavailable baseline or any optional-part failure preserves the existing surface.
    return null;
  }
}

export const recipeStandingAction = defineServerAction({
  name: JEV_ACTIONS.recipeStanding,
  title: "Read partial cached-current standings by recipe without spending",
  // The existing service grant permits its metadata to be listed; this reader cannot invoke it.
  caps: ["containers:read"],
  delegates: ["services:invoke"],
  input: z.strictObject({}),
  result: RecipeStandingSchema,
});
export const RECIPE_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.recipeStanding]: async (ctx) => await recipeStanding(ctx),
};
