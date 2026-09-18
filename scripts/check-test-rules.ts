#!/usr/bin/env bun
/*
  TWO RULES ABOUT TESTS, ENFORCED.

  **A TEST MAY NOT READ A MARKDOWN FILE.** Prose is not a contract. A test that asserts a document
  says something pins the wording rather than the behaviour: it breaks when the sentence is
  improved, passes when the sentence is right and the code is wrong, and teaches whoever hits it
  to edit the test. The one mechanical thing worth checking about a document is whether the paths
  it names exist, and `check-doc-paths.ts` does that without pretending prose is a consumer.

  **A TEST MAY NOT SKIP ITSELF ON AN ENVIRONMENT VARIABLE.** A lane that cannot run here must be
  zero tests, not a green run full of silent skips. The retired product had suites that skipped
  without restic and without PostgreSQL, and the effect was a passing local run that had proven
  nothing anybody could name — which is the exact failure mode the engineering contract calls a
  local skip that is not a pass. Where a capability genuinely needs a machine, the honest shapes
  are a test that fails with the prerequisite named, or a check that runs on a hub and says so.

  Both rules are about the same thing: a suite whose green means something. A violation is either
  fixed or listed below with a reason — never left implicit, because an exemption nobody can see
  is how the rule dies.
*/

import { readFile } from "node:fs/promises";

/** Reading a document from a test. Allowed only for a file the check itself is about. */
const READS_MARKDOWN = /(?:readFile|Bun\.file|readFileSync)\([^)]*\.md["'`]/;
/** `import.meta.dir`-relative or URL-built document reads, which the above misses. */
const READS_MARKDOWN_URL = /new URL\([^)]*\.md["'`]/;
/**
 * A skip decided by the environment. `test.skipIf`, `describe.skipIf`, a bare `return` guarded by
 * `process.env`, and `it.todo` gated the same way all reduce to "this did not run and nobody
 * said so".
 */
const SKIPS_ON_ENV = [
  /\.skipIf\s*\(/,
  /\.skip\s*\(\s*(?:!!)?process\.env/,
  /process\.env\[[^\]]*\][^\n]*\?\s*test\.skip/,
  /if\s*\([^)]*process\.env[^)]*\)\s*(?:\{\s*)?return\s*;/,
];

/**
 * Violations that are accepted, each with the reason. A file here is a decision, not a backlog:
 * the reason has to say why the rule does not apply rather than why nobody got to it.
 */
const ACCEPTED: readonly { readonly file: string; readonly rule: string; readonly why: string }[] =
  [
    {
      file: "scripts/check-test-rules.test.ts",
      rule: "markdown",
      why: "its fixtures are violations by construction: the file proves the rule fires, in strings written to a temporary directory",
    },
    {
      file: "scripts/check-test-rules.test.ts",
      rule: "skip",
      why: "the same reason; a gate nobody has watched fail is not a gate, and watching it fail means writing the thing it refuses",
    },
  ];

interface Violation {
  readonly file: string;
  readonly line: number;
  readonly rule: "markdown" | "skip";
  readonly text: string;
}

async function testFiles(): Promise<readonly string[]> {
  const git = Bun.spawn(["git", "ls-files", "*.test.ts", "*.test.tsx"], { stdout: "pipe" });
  const [status, out] = await Promise.all([git.exited, new Response(git.stdout).text()]);
  if (status !== 0) throw new Error("git ls-files failed");
  return out.trim().split("\n").filter(Boolean);
}

export async function testRuleViolations(files: readonly string[]): Promise<readonly Violation[]> {
  const found: Violation[] = [];
  for (const file of files) {
    const lines = (await readFile(file, "utf8")).split("\n");
    for (const [index, line] of lines.entries()) {
      const rule: Violation["rule"] | null =
        READS_MARKDOWN.test(line) || READS_MARKDOWN_URL.test(line)
          ? "markdown"
          : SKIPS_ON_ENV.some((pattern) => pattern.test(line))
            ? "skip"
            : null;
      if (rule === null) continue;
      if (ACCEPTED.some((entry) => entry.file === file && entry.rule === rule)) continue;
      found.push({ file, line: index + 1, rule, text: line.trim() });
    }
  }
  return found;
}

const SENTENCE: Record<Violation["rule"], string> = {
  markdown: "a test may not read a Markdown file: prose is not a contract",
  skip: "a test may not skip itself on an environment variable: a lane that cannot run is zero tests",
};

if (import.meta.main) {
  const violations = await testRuleViolations(await testFiles());
  for (const violation of violations) {
    process.stdout.write(
      `${violation.file}:${String(violation.line)}  ${SENTENCE[violation.rule]}\n` +
        `    ${violation.text.slice(0, 120)}\n`,
    );
  }
  if (violations.length > 0) {
    process.stdout.write(
      `\n${String(violations.length)} test rule violation(s). Fix them, or add the file to ` +
        "ACCEPTED in this script with the reason the rule does not apply.\n",
    );
    process.exit(1);
  }
  process.stdout.write("every test asserts behaviour and none of them skips itself\n");
}
