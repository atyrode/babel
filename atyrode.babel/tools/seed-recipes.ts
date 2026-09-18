#!/usr/bin/env bun
/*
  THE COOKBOOK, AS PLUGIN DATA.

  A recipe body is the whole of what Babel looks for: `server/engine/prompts.ts` writes it into
  the prompt VERBATIM, and a hub whose policy names no recipe with a body cannot run an explore
  at all (`doors/launch.ts`: "an explore has no method to run"). The bodies therefore have to
  live somewhere the repository keeps them, and the policy's own `review.recipes` block is where
  a hub reads them from (`server.ts`, `cookbook()`; `store/coordinator.ts`,
  `PolicyRecipeSchema`).

  Two jobs and no third:

    bun tools/seed-recipes.ts import <dir>   reads a cookbook-shaped directory and writes
                                             `store/recipes.seed.json`
    bun tools/seed-recipes.ts policy         prints the seed's `review.recipes` block, which is
                                             what `setPolicy` takes

  A cookbook-shaped directory is `versions.json`, `preamble.md` and `recipes/*.md`, each recipe a
  YAML frontmatter block, a level-1 heading for its title, and a `## Question` section whose first
  paragraph is the one line saying what it looks for. `versions.json` is the authority on a
  recipe's VERSION — a claim cites `id@version`, so a body seeded under the wrong number would
  make every citation of it wrong — and a frontmatter version that disagrees with it is a refusal
  rather than a preference.

  It is a dev-time Bun CLI and never enters a packed artifact, for the reason `tools/import.ts`
  is one: nothing a hub runs reads a repository file.

  IT INSTALLS NOTHING. `policy` prints; the operator installs. A tool that wrote a hub's policy
  would be choosing the ceilings, the route and the enablement that document also carries.
*/

import { createHash } from "node:crypto";
import { basename, join, resolve } from "node:path";

/** One recipe as the policy's `review.recipes` carries it (`PolicyRecipeSchema`). */
interface PolicyRecipe {
  readonly id: string;
  readonly version: number;
  readonly title: string;
  readonly looksFor: string;
  readonly enabled: boolean;
  readonly body: string;
}

/** The seed document: the recipes, and the standing statement they are edited against. */
interface Seed {
  /**
   * What the bodies are, in one sentence, so a reader of the file knows what it is for without
   * the commit that added it.
   */
  readonly about: string;
  readonly preamble: { readonly version: number; readonly body: string };
  readonly recipes: readonly PolicyRecipe[];
}

const ABOUT =
  "The cookbook: every method Babel performs, as the policy's review.recipes block carries them. " +
  "A body is written into an explore's prompt verbatim and a claim cites its id and version.";

/** The recipes' home in the repository, relative to this file. */
const SEED_PATH = resolve(import.meta.dir, "../store/recipes.seed.json");

// ---------------------------------------------------------------------------- reading a recipe

/** The frontmatter block and the prose under it, or a refusal naming the file. */
function split(file: string, text: string): { front: string; body: string } {
  if (!text.startsWith("---\n")) {
    throw new Error(`${file}: no frontmatter block, so the recipe declares no id or version`);
  }
  const close = text.indexOf("\n---\n", 3);
  if (close === -1) throw new Error(`${file}: the frontmatter block is never closed`);
  return { front: text.slice(4, close + 1), body: text.slice(close + 5).trim() };
}

/** One scalar field of a frontmatter block. Lists and nested maps are nobody's business here. */
function field(front: string, key: string): string {
  for (const line of front.split("\n")) {
    if (!line.startsWith(`${key}:`)) continue;
    return line.slice(key.length + 1).trim();
  }
  return "";
}

/**
 * The recipe's own name: its level-1 heading.
 *
 * The heading rather than the id, because the id is a slug and the heading is the sentence the
 * operator reads in Watch's Recipes section. A recipe with no heading keeps its id, which is
 * what the panel already falls back to.
 */
function title(body: string): string {
  for (const line of body.split("\n")) {
    if (line.startsWith("# ")) return line.slice(2).trim();
  }
  return "";
}

/**
 * THE ONE LINE SAYING WHAT THIS RECIPE LOOKS FOR: the first paragraph under `## Question`.
 *
 * It is taken from the document rather than written beside it because a summary maintained apart
 * from the method it summarizes drifts from it, and the panel's line would then describe a lens
 * the runs are not performing. Every recipe in the cookbook opens its question with one
 * interrogative paragraph, which is exactly the length this field wants.
 */
function looksFor(body: string): string {
  const at = body.indexOf("\n## Question\n");
  if (at === -1) return "";
  const rest = body.slice(at + "\n## Question\n".length).trimStart();
  const end = rest.indexOf("\n\n");
  const paragraph = end === -1 ? rest : rest.slice(0, end);
  return paragraph.replace(/\s+/gu, " ").trim();
}

/** Reads one recipe document into the shape a policy carries, or refuses it by name. */
function recipeOf(file: string, text: string, versions: Map<string, number>): PolicyRecipe {
  const { front, body } = split(file, text);
  const id = field(front, "id");
  if (id === "") throw new Error(`${file}: the frontmatter names no id`);
  if (id !== basename(file, ".md")) {
    throw new Error(
      `${file}: the frontmatter calls it ${id}, so the file name and the id disagree`,
    );
  }
  const declared = Number(field(front, "version"));
  const recorded = versions.get(id);
  if (recorded === undefined) throw new Error(`${file}: versions.json holds no record of ${id}`);
  // A CLAIM CITES `id@version`. A body seeded under a number the manifest does not record would
  // make every citation of it name a method nobody can read back.
  if (declared !== recorded) {
    throw new Error(
      `${file}: the document declares version ${String(declared)} and versions.json records ` +
        `${String(recorded)}; change the document and its record together`,
    );
  }
  if (body === "") throw new Error(`${file}: the recipe has no body, so it is not an instruction`);
  return {
    id,
    version: recorded,
    title: title(body) || id,
    looksFor: looksFor(body),
    // WHETHER A RUN PERFORMS IT BY DEFAULT is the recipe's own `default`, which is what the Go
    // tree's selection read and what an empty selection in `launch` still means: the enabled
    // default set. A meta recipe that reads Babel itself is off unless asked for by name.
    enabled: field(front, "default") === "true",
    body,
  };
}

// ---------------------------------------------------------------------------- the two jobs

async function read(path: string): Promise<string> {
  const file = Bun.file(path);
  if (!(await file.exists())) throw new Error(`${path} does not exist`);
  return await file.text();
}

interface Manifest {
  readonly preamble?: { readonly version?: number };
  readonly recipes?: readonly { readonly id?: string; readonly version?: number }[];
}

/** Reads a cookbook-shaped directory into the seed document. */
async function imported(dir: string): Promise<Seed> {
  const manifest = JSON.parse(await read(join(dir, "versions.json"))) as Manifest;
  const versions = new Map<string, number>();
  for (const entry of manifest.recipes ?? []) {
    if (typeof entry.id === "string" && typeof entry.version === "number") {
      versions.set(entry.id, entry.version);
    }
  }
  if (versions.size === 0) throw new Error(`${dir}/versions.json records no recipe`);
  const preamble = await read(join(dir, "preamble.md"));
  const recipes: PolicyRecipe[] = [];
  const seen = new Set<string>();
  const files = [...new Bun.Glob("*.md").scanSync({ cwd: join(dir, "recipes") })].sort();
  for (const name of files) {
    const recipe = recipeOf(name, await read(join(dir, "recipes", name)), versions);
    recipes.push(recipe);
    seen.add(recipe.id);
  }
  // EVERY RECORDED RECIPE HAS TO BE PRESENT. A manifest naming a document the directory no
  // longer holds is a method the hub would think it has and no run could perform.
  const missing = [...versions.keys()].filter((id) => !seen.has(id)).sort();
  if (missing.length > 0) {
    throw new Error(`versions.json records recipes with no document: ${missing.join(", ")}`);
  }
  return {
    about: ABOUT,
    preamble: {
      version: manifest.preamble?.version ?? 0,
      body: split("preamble.md", preamble).body,
    },
    recipes,
  };
}

function digestOf(body: string): string {
  return createHash("sha256").update(body).digest("hex").slice(0, 12);
}

async function main(argv: readonly string[]): Promise<number> {
  const [job, ...rest] = argv;
  if (job === "import") {
    const dir = rest[0];
    if (dir === undefined) {
      process.stderr.write("usage: seed-recipes.ts import <cookbook-dir>\n");
      return 2;
    }
    const seed = await imported(resolve(dir));
    await Bun.write(SEED_PATH, `${JSON.stringify(seed, null, 2)}\n`);
    process.stdout.write(
      `${String(seed.recipes.length)} recipes and the preamble (version ` +
        `${String(seed.preamble.version)}) written to ${SEED_PATH}\n`,
    );
    for (const recipe of seed.recipes) {
      process.stdout.write(
        `  ${recipe.id}@${String(recipe.version)} ${recipe.enabled ? "on " : "off"} ` +
          `${String(recipe.body.length)}b ${digestOf(recipe.body)} ${recipe.title}\n`,
      );
    }
    return 0;
  }
  if (job === "policy") {
    const seed = JSON.parse(await read(SEED_PATH)) as Seed;
    // The recipes ALONE. The rest of a policy — the route, the shares, the ceilings, whether
    // evaluation is on at all — is the operator's authorization and not a file's to supply.
    process.stdout.write(`${JSON.stringify(seed.recipes, null, 2)}\n`);
    return 0;
  }
  process.stderr.write(
    "usage: seed-recipes.ts import <cookbook-dir> | seed-recipes.ts policy\n" +
      "  import  reads versions.json, preamble.md and recipes/*.md into store/recipes.seed.json\n" +
      "  policy  prints the seed's recipes as a policy's review.recipes block\n",
  );
  return 2;
}

if (import.meta.main) {
  try {
    process.exit(await main(process.argv.slice(2)));
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exit(1);
  }
}

export { recipeOf };
