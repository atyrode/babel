import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "bun:test";
import { testRuleViolations } from "./check-test-rules.ts";

/*
  The same argument as the documentation checker's test: a gate nobody has watched fail is not a
  gate. The fixtures are written to a temporary directory, so this proves the rules rather than
  the current state of the suites — which is clean, and would make every assertion here vacuous.
*/

let dir = "";

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "babel-test-rules-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

async function fixture(name: string, body: string): Promise<string> {
  const file = join(dir, name);
  await writeFile(file, body);
  return file;
}

test("a test that reads a document is refused, whichever way it opens it", async () => {
  const file = await fixture(
    "prose.test.ts",
    [
      'import { readFile } from "node:fs/promises";',
      'test("the spec says so", async () => {',
      '  const spec = await readFile("SPEC.md", "utf8");',
      '  expect(spec).toContain("storage is the product");',
      "});",
      'const other = await Bun.file("docs/runbook.md").text();',
      'const third = await Bun.file(new URL("../README.md", import.meta.url)).text();',
      "",
    ].join("\n"),
  );
  const violations = await testRuleViolations([file]);
  expect(violations.map((violation) => violation.line)).toEqual([3, 6, 7]);
  expect(new Set(violations.map((violation) => violation.rule))).toEqual(new Set(["markdown"]));
});

test("a test that skips itself on the environment is refused", async () => {
  const file = await fixture(
    "absent.test.ts",
    [
      'test.skipIf(process.env["BABEL_TEST_POSTGRES"] === undefined)("needs a server", () => {});',
      'if (process.env["CI"] === undefined) return;',
      "",
    ].join("\n"),
  );
  const violations = await testRuleViolations([file]);
  expect(violations.map((violation) => violation.rule)).toEqual(["skip", "skip"]);
});

test("an ordinary test is not a violation", async () => {
  const file = await fixture(
    "ordinary.test.ts",
    [
      'test("a refused answer writes nothing", async () => {',
      "  expect(await records()).toEqual([]);",
      "});",
      "",
    ].join("\n"),
  );
  expect(await testRuleViolations([file])).toEqual([]);
});

test("the suites this repository ships obey both rules", async () => {
  // The rules exist to keep it that way, so the current state is worth asserting once: a
  // violation arriving with a new suite is what this gate is for.
  const git = Bun.spawn(["git", "ls-files", "*.test.ts", "*.test.tsx"], { stdout: "pipe" });
  const files = (await new Response(git.stdout).text()).trim().split("\n").filter(Boolean);
  expect(files.length).toBeGreaterThan(20);
  expect(await testRuleViolations(files)).toEqual([]);
});
