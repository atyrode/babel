import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "bun:test";
import { documentMisses } from "./check-doc-paths.ts";

/*
  A GATE HAS TO BE PROVEN TO FAIL. A checker that only ever passes is indistinguishable from one
  that returns the empty list, and this one runs on every pull request, so its false-negative is
  the whole repository's documentation going unchecked while the run stays green.

  The fixtures are written to a temporary directory rather than taken from the tree, for the same
  reason the test rules forbid reading a document: a test over `docs/runbook.md` would pin that
  document's current wording, and improving it would break this.
*/

let dir = "";
const scripts = new Set(["check", "test", "lint"]);

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "babel-doc-paths-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

async function document(name: string, body: string): Promise<string> {
  const file = join(dir, name);
  await writeFile(file, body);
  return file;
}

test("a path that does not exist is a miss, naming the file and the line", async () => {
  const file = await document(
    "broken.md",
    ["# A document", "", "The answer is in `docs/there-is-no-such-file.md`.", ""].join("\n"),
  );
  const misses = await documentMisses([file], scripts);
  expect(misses).toHaveLength(1);
  expect(misses[0]?.line).toBe(3);
  expect(misses[0]?.what).toBe("docs/there-is-no-such-file.md");
});

test("a script the manifest does not declare is a miss", async () => {
  const file = await document("script.md", "Run `bun run lint` and then `bun run nonesuch`.\n");
  const misses = await documentMisses([file], scripts);
  expect(misses.map((miss) => miss.what)).toEqual(["bun run nonesuch"]);
  expect(misses[0]?.why).toContain("no such script");
});

test("what is deliberately not a path stays unchecked, and each exemption is a rule", async () => {
  const file = await document(
    "exempt.md",
    [
      // A bare filename names a shape that recurs in several directories.
      "Every part declares a `manifest.json`.",
      // A runtime location on a machine, which a postmortem cites as evidence.
      "The pool is at `/run/code/account-pool.json`.",
      // Another repository's path; this checkout has no opinion on it.
      "See `packages/plugin-kit/src/pack.ts` and `scripts/gate.sh`.",
      // Written by the build and absent in a clean tree.
      "`pack.sh` writes `atyrode.babel/machine.js`.",
      "",
    ].join("\n"),
  );
  expect(await documentMisses([file], scripts)).toEqual([]);
});

test("a path resolves from the plugin root, which is the shorthand the documents use", async () => {
  // `store/schema.ts` means the baseline's, and every document in this repository writes it that
  // way. A checker that demanded the prefix would have failed on prose that was never wrong.
  const file = await document("shorthand.md", "The tables are in `store/schema.ts`.\n");
  expect(await documentMisses([file], scripts)).toEqual([]);
});

test("the changelog is a ledger and is not checked", async () => {
  // An entry naming a file a release held stays true after the file is deleted.
  const misses = await documentMisses(["CHANGELOG.md"], scripts);
  expect(misses).toEqual([]);
});
