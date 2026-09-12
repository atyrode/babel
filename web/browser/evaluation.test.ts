// Browser acceptance for what is left of issue #219's ranked reading surface:
// the review policy, which is a section of Settings.
//
// This file used to cover a destination that no longer exists. It drove
// /read — the ranked listing with its ordering menu, its coverage filter, its
// pinned snapshot and its per-row reception spark — and §8.7 made that listing
// the front page: the orderings are the feed's own sort bar, the facets are
// its chips, and the row's reception is Babel's score. /read is a redirect
// now, so every test that opened it was driving a page this build does not
// serve.
//
// The seven tests that did are deleted rather than re-pinned, one line each:
//
//   - "every ordering names its basis and reorders the listing" — the
//     orderings are the feed's sort bar, and browser/feed.test.ts asserts both
//     halves of it: that each sort names what it is computed from, and that
//     the order the server sent is the order rendered.
//   - "sorts, filters and pages live in the URL and survive reload and Back" —
//     the feed's own controls write the URL and feed.test.ts walks Back
//     through them.
//   - "paging stays inside one ranked snapshot and reports its window" — the
//     feed appends rather than paging and pins no snapshot; there is no
//     snapshot parameter left to assert.
//   - "a never-reviewed record is found and is not rendered as unopposed" —
//     the coverage filter was that page's; the same rule is now a property of
//     the score, and feed.test.ts asserts the em dash on a row no reviewer has
//     assessed.
//   - "a row's reception is drawn where there is one and nowhere else" — same
//     rule, same place: the spark and the facts strip it was read from belong
//     to the deleted listing.
//   - "a stale projection still answers and says it is not current" — the
//     notice it read belonged to /read's projection; the feed carries its own
//     `notice` field, and phaseb.test.ts owns the degraded-read walk.
//   - "no control on this surface casts a vote" — the surface is gone, and the
//     stronger version of the rule is now structural: the operator has no vote
//     anywhere (§8.7), which records.test.ts asserts on the record page and
//     feed.test.ts on a row.
//
// What survives is the one test that was never about the listing: that what
// authorized evaluation work may spend is configuration rather than a
// destination, that its old path still opens it, that the page states the
// server's own sentences rather than paraphrasing them, and that saving a
// ceiling offers nothing that starts work.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Review policy gate",
  covers: "the review policy — what authorized evaluation work may spend — as a section of Settings, in a browser",
  unverified: [
    "that the review policy is a section of Settings reached by its old path",
    "that the page states the server's own running and consequence sentences rather than a paraphrase",
    "that nothing on it offers to start work",
  ],
});

interface MockServer {
  process: Bun.Subprocess<"ignore", "pipe", "pipe">;
  base: string;
}

async function startMock(env: Record<string, string>): Promise<MockServer> {
  const process_ = Bun.spawn(["bun", "mock/serve.ts"], {
    env: { ...process.env, PORT: "0", MOCK_SCAN: "idle", ...env },
    stdout: "pipe",
    stderr: "pipe",
  });
  const deadline = Date.now() + 15_000;
  const reader = process_.stdout.getReader();
  const decoder = new TextDecoder();
  let banner = "";
  let base = "";
  while (!base && Date.now() < deadline) {
    const { value, done } = await reader.read();
    if (done) break;
    banner += decoder.decode(value, { stream: true });
    const match = banner.match(/Babel mock: (http:\/\/127\.0\.0\.1:\d+)\//u);
    if (match) base = match[1];
  }
  reader.releaseLock();
  if (!base) throw new Error(`mock printed no base URL: ${banner}`);
  return { process: process_, base };
}

let mock: MockServer | null = null;
let browser: Browser | null = null;
let page: Page;

beforeAll(async () => {
  if (!chrome) return;
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);
  mock = await startMock({});
  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 900 });
});

afterAll(async () => {
  await browser?.close();
  mock?.process.kill();
});

test.skipIf(!chrome)("the review policy is a section of Settings, and saving it starts nothing", async () => {
  // What evaluation may spend is configuration rather than a destination, so
  // it is a drawer in Settings — and the path it used to have still opens it,
  // because an operator's bookmark outlives a navigation redesign.
  await page.goto(`${mock?.base}/#/evaluation/policy`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForFunction(() => window.location.hash.startsWith("#/settings"), {
    timeout: 15_000,
  });
  expect(page.url()).toContain("section=policy");
  await page.waitForSelector(".policy-section .evaluation-policy-form", { timeout: 15_000 });

  const section = await page.evaluate(async () => {
    const served = (await fetch("/api/evaluation/policy").then((response) => response.json())) as {
      saving?: string;
      detail?: string;
    };
    return {
      served,
      open: document.querySelector(".section-nav button[aria-pressed='true']")?.textContent ?? "",
      saving: document.querySelector(".evaluation-saving")?.textContent ?? "",
      detail: document.querySelector(".policy-status .untrusted-inline")?.textContent ?? "",
      controls: Array.from(
        document.querySelectorAll(".policy-section button, .policy-section input[type='submit']"),
      ).map((control) => ((control as HTMLElement).innerText || "").toLowerCase()),
    };
  });

  expect(section.open).toBe("Review policy");
  // What is running, and what saving does, are the server's own sentences
  // rendered verbatim: whether authorized work is drawing is observed from
  // claimed assignments, and only the surface that observed it can say so.
  expect(section.saving).toBe(section.served.saving ?? "");
  expect(section.saving.length).toBeGreaterThan(0);
  expect(section.detail).toBe(section.served.detail ?? "");

  // And the consequence is stated before the press, not after it: the operator
  // is about to press a button on a form with a dollar figure in it. Saving a
  // ceiling is not permission to spend it, so there is no control here that
  // starts work.
  expect(section.controls.length).toBeGreaterThan(0);
  for (const label of section.controls) {
    expect(label).not.toMatch(/\b(start|launch|draw now|run)\b/u);
  }
});
