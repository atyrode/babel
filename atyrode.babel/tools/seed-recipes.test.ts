import { expect, test } from "bun:test";
import { ReviewDispatchSchema } from "../store/coordinator.ts";
import { recipeOf } from "./seed-recipes.ts";

/*
  WHAT THE SEED HAS TO BE FOR A HUB TO RUN AN EXPLORE AT ALL.

  `server.ts`'s `cookbook()` drops a policy recipe with no body, and `doors/launch.ts` refuses an
  explore whose selection resolves to none: a seed carrying titles and no instructions is a hub
  that can start nothing, and it would fail at the press rather than here. The committed seed is
  therefore checked against the policy's own schema, which is the shape a hub reads.

  The parsing rules are exercised through `recipeOf` on documents written inline. A test that
  read a recipe out of the tree would pin prose rather than behaviour, and the version rule — the
  one that matters, because a claim cites `id@version` — needs two documents that disagree, which
  no tracked file should ever be.
*/

const VERSIONS = new Map([["test-economics", 3]]);

const DOCUMENT = `---
id: test-economics
version: 3
kind: lens
default: true
---

# Test economics: what the tests defend

## Question

Did the tests written in these sessions earn their keep?

A test is a purchase: it costs writing time once and running time forever.

## Inclusion

Include a test written in a session.
`;

test("the committed seed is a policy's recipes block, and every recipe carries an instruction", async () => {
  const seed = (await Bun.file(new URL("../store/recipes.seed.json", import.meta.url)).json()) as {
    preamble: { version: number; body: string };
    recipes: { id: string; body: string; looksFor: string }[];
  };
  // The policy's own schema is the check: it bounds the count, the body's size and the fields a
  // hub will read, so a seed that parses here is one `setPolicy` will take.
  const recipes = ReviewDispatchSchema.shape.recipes.parse(seed.recipes);
  expect(recipes.length).toBeGreaterThan(0);
  for (const recipe of recipes) {
    expect(recipe.body.trim()).not.toBe("");
    expect(recipe.looksFor ?? "").not.toBe("");
  }
  // One document per id: two bodies under one id would make a citation of it ambiguous.
  expect(new Set(recipes.map((recipe) => recipe.id)).size).toBe(recipes.length);
  // The standing statement the recipes are edited against travels with them: `cookbook/` is
  // gone, and a directory's editorial half is not something a deletion may take with it.
  expect(seed.preamble.body.trim()).not.toBe("");
});

test("a recipe becomes its heading, the line its question asks, and whether it runs by default", () => {
  const recipe = recipeOf("test-economics.md", DOCUMENT, VERSIONS);
  expect(recipe.id).toBe("test-economics");
  expect(recipe.version).toBe(3);
  expect(recipe.title).toBe("Test economics: what the tests defend");
  // The FIRST paragraph of the question and nothing after it: the line is what a panel shows
  // beside the name, and a whole section there would be the method rather than its label.
  expect(recipe.looksFor).toBe("Did the tests written in these sessions earn their keep?");
  expect(recipe.enabled).toBe(true);
  expect(recipe.body.startsWith("# Test economics")).toBe(true);
});

test("a version the manifest does not record is refused rather than seeded under a guess", () => {
  const moved = DOCUMENT.replace("version: 3", "version: 4");
  // A CLAIM CITES `id@version`. Seeding the body under either number while the other stands
  // would make every citation of this method name something nobody can read back.
  expect(() => recipeOf("test-economics.md", moved, VERSIONS)).toThrow(/versions\.json records 3/);
  expect(() => recipeOf("other.md", DOCUMENT, VERSIONS)).toThrow(/file name and the id disagree/);
  expect(() => recipeOf("test-economics.md", DOCUMENT, new Map())).toThrow(/no record of/);
});

test("a document with no instruction under its heading is not a method", () => {
  const empty = `---\nid: test-economics\nversion: 3\n---\n`;
  expect(() => recipeOf("test-economics.md", empty, VERSIONS)).toThrow(/no body/);
  expect(() => recipeOf("test-economics.md", "# no frontmatter\n", VERSIONS)).toThrow(
    /no frontmatter/,
  );
});
