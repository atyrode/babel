import { Cluster, Stack } from "@manifold/ui";
import { figure, since, type RecipeRow } from "./api.ts";

/*
  THE RECIPES, BY NAME.

  The cookbook is what Babel is looking for, and before this panel the only way to read it was
  the repository: a run's recipe id appeared in a receipt and nowhere else, so "which of these
  is actually running, and when did this one last run?" was a question answered by `ls
  cookbook/recipes`. The list states each one's name, the line saying what it looks for, whether
  the policy has it enabled, when it last ran and how many runs it has to its name.

  An empty title is rendered as the id rather than as a blank: the title and the line come from
  the policy payload's recipe map, which a store imported before that map existed does not
  carry, and a nameless row is still a recipe that ran nine times last week.
*/

export interface RecipesProps {
  readonly recipes: readonly RecipeRow[];
  readonly now: number;
  /** A failed read, in the door's own words. */
  readonly note: string;
}

export function Recipes({ recipes, now, note }: RecipesProps) {
  const enabled = recipes.filter((recipe) => recipe.enabled).length;
  return (
    <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__section">
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Recipes</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          {recipes.length === 0
            ? "The policy in force names no recipes."
            : `${enabled} of ${recipes.length} enabled — what Babel is looking for, and when it last looked.`}
        </p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      <ul className="plugin-atyrode_babel_watch__recipes">
        {recipes.map((recipe) => (
          <li key={recipe.id} className="plugin-atyrode_babel_watch__recipe">
            <Stack gap="var(--babel-space-1)">
              <Cluster gap="var(--babel-space-2)" justify="space-between">
                <Cluster gap="var(--babel-space-2)">
                  <span className="plugin-atyrode_babel_watch__recipe-name">
                    {recipe.title === "" ? recipe.id : recipe.title}
                  </span>
                  {recipe.enabled ? null : <span className="plugin-atyrode_babel_watch__off">off</span>}
                </Cluster>
                <span className="plugin-atyrode_babel_watch__mono plugin-atyrode_babel_watch__muted">
                  {recipe.lastRanAt === "" ? "never run" : `ran ${since(recipe.lastRanAt, now)}`} ·{" "}
                  {figure(recipe.runs)} {recipe.runs === 1 ? "run" : "runs"}
                </span>
              </Cluster>
              <p className="plugin-atyrode_babel_watch__recipe-looks">
                {recipe.looksFor === "" ? "The policy carries no description for this one." : recipe.looksFor}
              </p>
            </Stack>
          </li>
        ))}
      </ul>
    </Stack>
  );
}
