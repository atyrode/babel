// Every browser suite in this directory has the same three-part problem: it
// needs a Chrome to drive, a developer legitimately may not have one, and a
// suite that skips for that reason reports success while testing nothing. The
// third part is the dangerous one -- a skip whose only trace is bun's "skip"
// tally reads exactly like a pass, so the run goes green and the operator
// learns nothing about what was never checked.
//
// resolveChrome answers all three parts at once: it finds the browser, it hard
// fails under CI where a skip would silently retire a gate, and locally it
// prints a notice naming each guarantee the skip withdrew. Resolution and
// notice are deliberately one call. A caller cannot obtain the executable path
// without the absence being reported, so the failure mode this file exists to
// remove cannot be reintroduced by adopting half of it.
//
// The gate description is a parameter rather than shared text because a banner
// that names the wrong guarantees is worse than no banner: it tells the
// operator something was covered when it was not. Each suite states its own.
//
// leak.test.ts predates this file and still carries its own copy of the same
// logic and its own banner; it is the only suite covering the SPEC.md §548
// browser channels and was fixed first, under separate file ownership. Its
// behaviour is identical to what this file produces, so folding it in here is
// a mechanical follow-up, not a behaviour change.

import { existsSync, readdirSync } from "node:fs";
import { join } from "node:path";

export interface Gate {
  // Short noun phrase for what a skip retires, e.g. "lock/stop gate". Used
  // verbatim in the CI failure and, upper-cased, as the banner heading.
  gate: string;
  // What this suite's tests exercise, completing "Nothing in this file tested
  // ...", e.g. "the SPEC.md §2 lock and stop control".
  covers: string;
  // One entry per guarantee the skip withdrew, written as a plain clause with
  // no trailing punctuation. Naming them individually is the whole point: the
  // operator needs to know which properties went unchecked, not merely that
  // something was skipped.
  unverified: string[];
}

// findChrome locates a browser to drive. BABEL_TEST_CHROME wins so a machine
// with an unusual install, or CI with a pinned download, can say where it is;
// otherwise PATH and puppeteer's own download cache are searched.
function findChrome(): string | null {
  const explicit = process.env.BABEL_TEST_CHROME;
  if (explicit) return explicit;

  for (const name of ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"]) {
    const found = Bun.which(name);
    if (found) return found;
  }

  const cache = join(process.env.HOME ?? "", ".cache", "puppeteer", "chrome");
  if (existsSync(cache)) {
    for (const dir of readdirSync(cache)) {
      const candidate = join(cache, dir, "chrome-linux64", "chrome");
      if (existsSync(candidate)) return candidate;
    }
  }
  return null;
}

// The banner is a fixed 80-column block so it stays solid in a default
// terminal, which is what makes it hard to scroll past among test results.
const WIDTH = 80;
const RULE = "=".repeat(WIDTH);

// wrap lays out one clause as a paragraph with a hanging indent. The clauses
// are written as plain sentences at the callsite so that adding or rewording a
// guarantee is a one-line edit; hand-wrapped literals would rot on the first
// rewording and the block would stop looking deliberate.
function wrap(text: string, first: string, hang: string): string[] {
  const lines: string[] = [];
  let line = first;
  let empty = true;
  for (const word of text.split(/\s+/u).filter(Boolean)) {
    if (!empty && `${line} ${word}`.length > WIDTH) {
      lines.push(line);
      line = hang;
      empty = true;
    }
    line = empty ? line + word : `${line} ${word}`;
    empty = false;
  }
  if (!empty) lines.push(line);
  return lines;
}

function notice(gate: Gate): string {
  return [
    "",
    RULE,
    `  ${gate.gate.toUpperCase()} DID NOT RUN -- no Chrome or Chromium found.`,
    "",
    ...wrap(
      `Nothing in this file tested ${gate.covers}. Every one of these is UNVERIFIED by this run:`,
      "  ",
      "  ",
    ),
    ...gate.unverified.flatMap((item, index) =>
      wrap(`${item}${index === gate.unverified.length - 1 ? "." : ","}`, "    - ", "      "),
    ),
    "",
    ...wrap(
      "A green result for this file means the gate was skipped, not that it passed. Install Chrome or Chromium, or point BABEL_TEST_CHROME at one, and re-run:",
      "  ",
      "  ",
    ),
    "      BABEL_TEST_CHROME=/path/to/chrome bun run test:browser",
    RULE,
    "",
  ].join("\n");
}

// resolveChrome returns the browser to drive, or null when the caller should
// skip. A developer without Chrome skips, which mirrors how the Go suite
// treats a missing restic; the notice is what keeps that skip from passing for
// a result. In CI the same absence is a hard failure, because there a skip
// retires the gate for everyone.
//
// The notice goes to stderr at import time, so it lands ahead of every result
// the run will report rather than being buried under them.
export function resolveChrome(gate: Gate): string | null {
  const chrome = findChrome();
  if (chrome) return chrome;

  if (process.env.CI) {
    throw new Error(
      `no Chrome found in CI; set BABEL_TEST_CHROME. Skipping here would retire the ${gate.gate}.`,
    );
  }
  console.error(notice(gate));
  return null;
}
