import type { HostServices } from "@manifold/plugin";
import { useEffect, useState } from "react";
import {
  JEV_ACTIONS,
  JEV_PLUGIN_ID,
  RECIPE_READING_BATCH,
  RecipeStandingSchema,
  type RecipeStanding,
} from "../contract.ts";
import { Cluster, Stack } from "@manifold/ui";
import { figure, since, type RecipeRow } from "./api.ts";

/*
  THE RECIPES, BY NAME.

  The cookbook is what Babel is looking for, and before this panel the only way to read it was
  the repository: a run's recipe id appeared in a receipt and nowhere else, so "which of these
  is actually running, and when did this one last run?" was a question answered by `ls
  cookbook/recipes`. The list states each one's name, the line saying what it looks for, whether
  the policy has it enabled, when it last ran and how many runs it has to its name.

  NEVER RUN IS A STATE, NOT A ZERO (#344). The roster used to be assembled from the runs table,
  so a recipe in force that nothing had performed was not on the screen at all. Now it is, and a
  row saying `0` among rows saying `42` reads as a rounding rather than as the thing worth
  acting on — so it is marked beside the name, with the same badge that marks one switched off,
  and the lede counts them. The unpointed lens is the one to point.

  An empty title is rendered as the id rather than as a blank: the title and the line come from
  the policy payload's recipe map, which a store imported before that map existed does not
  carry, and a nameless row is still a recipe that ran nine times last week.
*/

export interface RecipesProps {
  readonly host: HostServices;
  readonly recipes: readonly RecipeRow[];
  readonly now: number;
  /** A failed read, in the door's own words. */
  readonly note: string;
}

export function Recipes({ host, recipes, now, note }: RecipesProps) {
  const [held, setHeld] = useState<{
    client: HostServices["client"];
    reading: RecipeStanding;
  } | null>(null);
  useEffect(() => {
    const client = host.client;
    let active = true;
    let timer: number | undefined;
    async function refresh(): Promise<void> {
      let reading: RecipeStanding = null;
      try {
        const reply = await client.action(`${JEV_PLUGIN_ID}.${JEV_ACTIONS.recipeStanding}`, {});
        const parsed = reply.ok ? RecipeStandingSchema.safeParse(reply.result) : null;
        if (parsed?.success) reading = parsed.data;
      } catch {
        // Optional means no error/empty section, including after a previously warm reading.
      }
      if (!active) return;
      setHeld({ client, reading });
      timer = window.setTimeout(() => void refresh(), 15_000);
    }
    void refresh();
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [host.client]);
  const reading = held?.client === host.client ? held.reading : null;
  const byRecipe = new Map(reading?.recipes.map((row) => [row.recipeId, row]));
  const enabled = recipes.filter((recipe) => recipe.enabled).length;
  const never = recipes.filter((recipe) => recipe.runs === 0).length;
  const tally =
    never === 0
      ? `${enabled} of ${recipes.length} enabled`
      : `${enabled} of ${recipes.length} enabled, ${never} never run`;
  return (
    <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__section">
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Recipes</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          {recipes.length === 0
            ? "The policy in force names no recipes."
            : `${tally} — what Babel is looking for, and when it last looked.`}
        </p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      {reading === null ? null : (
        <p className="plugin-atyrode_babel_watch__note" data-recipe-standing="partial-cache">
          Partial cached-current Jev readings — funding unknown; no provider calls. Bank{" "}
          {reading.bankVersion}, service policy {reading.policyRevision}, observed{" "}
          {reading.observedAt}. Complete eligibility census: {figure(reading.eligible)}{" "}
          exactly-one-recipe current records of {figure(reading.total)} retained;{" "}
          {figure(reading.multiRecipe)} multi-recipe; {figure(reading.excluded)} otherwise excluded
          (non-current, unreadable identifier or unattributed). Only the newest{" "}
          {RECIPE_READING_BATCH} eligible records can be inspected:{" "}
          {figure(reading.counts.knownCached)} known cached,{" "}
          {figure(reading.counts.missingOrEvicted)} missing-or-evicted,{" "}
          {figure(reading.counts.notInspected)} not inspected (older or over the text bound).
          Never-judged count unknown: a missing answer is not proof it was never judged. Bands below
          describe only the known cached sample, not the corpus.
        </p>
      )}
      <ul className="plugin-atyrode_babel_watch__recipes">
        {recipes.map((recipe) => (
          <li key={recipe.id} className="plugin-atyrode_babel_watch__recipe">
            <Stack gap="var(--babel-space-1)">
              <Cluster gap="var(--babel-space-2)" justify="space-between">
                <Cluster gap="var(--babel-space-2)">
                  <span className="plugin-atyrode_babel_watch__recipe-name">
                    {recipe.title === "" ? recipe.id : recipe.title}
                  </span>
                  {recipe.enabled ? null : (
                    <span className="plugin-atyrode_babel_watch__off">off</span>
                  )}
                  {recipe.runs === 0 ? (
                    <span className="plugin-atyrode_babel_watch__never">never run</span>
                  ) : null}
                </Cluster>
                {recipe.runs === 0 ? null : (
                  <span className="plugin-atyrode_babel_watch__mono plugin-atyrode_babel_watch__muted">
                    ran {since(recipe.lastRanAt, now)} · {figure(recipe.runs)}{" "}
                    {recipe.runs === 1 ? "run" : "runs"}
                  </span>
                )}
              </Cluster>
              <p className="plugin-atyrode_babel_watch__recipe-looks">
                {recipe.looksFor === ""
                  ? "The policy carries no description for this one."
                  : recipe.looksFor}
              </p>
              <RecipeReading row={byRecipe.get(recipe.id)} />
            </Stack>
          </li>
        ))}
        {reading?.recipes
          .filter((row) => !recipes.some((recipe) => recipe.id === row.recipeId))
          .map((row) => (
            <li key={row.recipeId} className="plugin-atyrode_babel_watch__recipe">
              <span className="plugin-atyrode_babel_watch__recipe-name">{row.recipeId}</span>
              <RecipeReading row={row} />
            </li>
          ))}
      </ul>
    </Stack>
  );
}

function RecipeReading({
  row,
}: {
  row: NonNullable<RecipeStanding>["recipes"][number] | undefined;
}) {
  if (row === undefined) return null;
  return (
    <p className="plugin-atyrode_babel_watch__recipe-looks" data-recipe-reading={row.recipeId}>
      {figure(row.eligible)} eligible{row.eligible === 0 ? " — zero eligible output" : ""};{" "}
      {figure(row.knownCached)} known cached; {figure(row.missingOrEvicted)} missing-or-evicted;{" "}
      {figure(row.notInspected)} not inspected; never-judged unknown.
      {row.knownCached === 0 ? null : (
        <>
          {" "}
          Cached sample bands:{" "}
          {Object.entries(row.bands)
            .filter(([band]) => band !== "unjudged")
            .map(
              ([band, count]) =>
                `${band} ${figure(count)} (${String(Math.round((100 * count) / row.knownCached))}%)`,
            )
            .join(" · ")}
          .
        </>
      )}
    </p>
  );
}
