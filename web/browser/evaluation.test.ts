// Browser acceptance for the ranked reading surface (issue #219; SPEC.md
// §4.12, §5.8, §8.5), driven against the synthetic mock so no Go server,
// archive, or network is needed.
//
// This file used to cover four destinations — the backlog, the coverage
// inventory, one record's evaluation and the review queue's reopen. #235
// collapsed the first two into Read and the last two into the record page.
// What is here is what Read owns.
//
// What only a browser can prove:
//
// That every ordering names what it is computed from, on the page, and that
// choosing one actually changes the answer. A ranking an operator cannot argue
// with is a ranking he has to take on faith, which is the opposite of what
// §8.5 asks for.
//
// That the sort, the filters and the page live in the URL, survive a reload,
// and walk back with the browser's own Back button — and that paging stays
// inside one ranked set while publication continues, which is the whole reason
// the snapshot is pinned.
//
// That a record nobody has reviewed is findable through the coverage filter
// and renders as an absence of review rather than as three zeroes, which read
// as unanimous absence of opposition.
//
// That a stale projection still answers and says so, rather than refusing or
// presenting itself as current.
//
// And that no control on this surface casts a vote: the browser holds no run
// identity and no claim, and §4.12's separation is kept by there being nothing
// here to press.
//
// The record page's own gate covers what one record shows. The corpus is
// synthetic and disposable; nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Ranked reading gate",
  covers: "issue #219's ranked output listing — ordering, filters, paging and coverage — on Read, in a browser",
  unverified: [
    "that each ordering names its basis on the page and actually reorders the listing",
    "that filters, sorts and pages live in the URL and survive reload and Back",
    "that paging stays inside one ranked snapshot",
    "that a never-reviewed record is findable and never renders as unopposed",
    "that a stale projection still answers and says it is not current",
    "that no control on this surface casts a vote",
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

async function open(route: string): Promise<void> {
  await page.goto(`${mock?.base}/#/${route}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
}

// visible waits for text to be on the page AND for the reader to have settled.
// Both halves are needed: a heading survives from the page being navigated
// away from, so a bare text match would read the previous view.
function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => {
      const body = document.body.innerText;
      return body.includes(needle) && !body.includes("Reading the output…");
    },
    { timeout: 15_000 },
    text,
  );
}

function ids(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("[data-item]"))
      .map((row) => row.getAttribute("data-item") ?? ""));
}

// order chooses one of the served orderings through the control an operator
// uses — the ordering menu, which names what each order is computed from —
// then waits for the listing to be the answer to that choice rather than the
// one still on screen.
async function order(sort: string): Promise<void> {
  const before = await ids();
  await page.click(".read-order > summary");
  await page.click(`[data-order='${sort}']`);
  await page.waitForFunction(
    (first: string) =>
      (document.querySelector("[data-item]")?.getAttribute("data-item") ?? "") !== first,
    { timeout: 15_000 },
    before[0] ?? "",
  );
}

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

test.skipIf(!chrome)("every ordering names its basis and reorders the listing", async () => {
  await open("read");
  await visible("What has Babel found?");
  // §8.5: the ordering says what it is computed from, on the page, not in a
  // document nobody reading it has and not in a tooltip nobody hovers.
  expect(await page.evaluate(() => document.body.innerText))
    .toContain("recorded priority, current work and pain");
  const recommended = await ids();

  await order("recent");
  await visible("Newest revisions first");
  const recent = await ids();
  expect(recent).not.toEqual(recommended);

  // Recently strengthened is not "new": it ranks by substantive contribution,
  // and the page has to say so, because another bare vote must not move an
  // item up it.
  await order("strengthened");
  await visible("Another bare vote does not move an item up this order");
  const strengthened = await ids();
  expect(strengthened).not.toEqual(recent);
  expect(strengthened[0]).toBe("pro_group-cache");

  await order("contested");
  await visible("Unresolved disagreement first");
  expect((await ids())[0]).toBe("fnd_no-evaluator");

  await order("unreviewed");
  await visible("how little has been looked at, not how little it was liked");
  expect(await ids()).toContain("hyp_never-reviewed");
});

test.skipIf(!chrome)("sorts, filters and pages live in the URL and survive reload and Back", async () => {
  await open("read");
  await visible("What has Babel found?");
  await order("contested");
  expect(page.url()).toContain("sort=contested");
  // The first answer pins the ranked set so paging stays inside one ordering.
  await page.waitForFunction(() => window.location.hash.includes("snapshot="));

  await page.click("[data-chip='lane-accepted']");
  await page.waitForFunction(() => window.location.hash.includes("lane=accepted"));

  // A reload re-reads the same view rather than dropping to the default.
  await page.reload({ waitUntil: "networkidle2" });
  await visible("What has Babel found?");
  expect(page.url()).toContain("sort=contested");
  expect(page.url()).toContain("lane=accepted");

  // Back undoes the operator's own last choice, not the whole surface.
  await page.goBack({ waitUntil: "networkidle2" });
  await page.waitForFunction(() => !window.location.hash.includes("lane=accepted"));
  expect(page.url()).toContain("sort=contested");
});

test.skipIf(!chrome)("paging stays inside one ranked snapshot and reports its window", async () => {
  await open("read");
  await visible("What has Babel found?");
  await page.waitForFunction(() => window.location.hash.includes("snapshot="));
  const firstPage = await ids();
  const snapshot = await page.evaluate(() =>
    new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("snapshot"));
  expect(snapshot).toBe("snap-2026-09-11T09-00-00Z");
  expect(firstPage.length).toBe(25);

  await page.click(".pager button:last-child");
  await page.waitForFunction(() => window.location.hash.includes("offset=25"));
  // The hash moves before the fetch resolves, so wait for the rendered set to
  // be the second window rather than reading the first one again.
  await page.waitForFunction(
    (first: string) =>
      (document.querySelector("[data-item]")?.getAttribute("data-item") ?? "") !== first,
    {},
    firstPage[0],
  );
  const secondPage = await ids();
  // No row appears on both pages: the ordering was cut once, not re-ranked per
  // page.
  expect(secondPage.some((id) => firstPage.includes(id))).toBe(false);
  expect(page.url()).toContain(`snapshot=${snapshot}`);

  await page.click(".pager button:first-child");
  await page.waitForFunction(() => !window.location.hash.includes("offset="));
  await page.waitForFunction(
    (first: string) =>
      (document.querySelector("[data-item]")?.getAttribute("data-item") ?? "") === first,
    {},
    firstPage[0],
  );
  expect(await ids()).toEqual(firstPage);
});

test.skipIf(!chrome)("a never-reviewed record is found and is not rendered as unopposed", async () => {
  // The coverage inventory was a destination of its own; it is a filter on the
  // one list now, and the question it answers — what has nobody read — is the
  // same question.
  await open("read?coverage=unreviewed");
  await visible("What has Babel found?");
  const listing = await ids();
  expect(listing).toContain("hyp_never-reviewed");

  // The absence is stated as an absence. Three zeroes would read as a record
  // nobody objected to.
  const row = await page.evaluate(() =>
    (document.querySelector("[data-item='hyp_never-reviewed']") as HTMLElement | null)?.innerText ?? "");
  expect(row).toContain("no reviews yet");
  expect(row).not.toContain("+0");

  // And the inventory behind the peel still reports what is owed, role by
  // role, because coverage is role-specific: a reception vote discharges no
  // evidence check.
  await page.click("details.peel > summary");
  await visible("Never reviewed");
  const roles = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".role-totals tbody tr")).map(
      (row_) => (row_ as HTMLElement).innerText));
  expect(roles.some((entry) => entry.startsWith("Evidence check"))).toBe(true);
  expect(roles.some((entry) => entry.startsWith("Outcome verification"))).toBe(true);
});

test.skipIf(!chrome)("a stale projection still answers and says it is not current", async () => {
  const degraded = await startMock({ MOCK_EVALUATION: "degraded" });
  const bare = await browser!.newPage();
  try {
    await bare.setViewport({ width: 1440, height: 900 });
    await bare.goto(`${degraded.base}/#/read`, { waitUntil: "networkidle2" });
    await bare.reload({ waitUntil: "networkidle2" });
    await bare.waitForSelector(".read-row", { timeout: 15_000 });
    const state = await bare.evaluate(() => ({
      text: document.body.innerText,
      rows: document.querySelectorAll(".read-row").length,
      banner: document.querySelectorAll(".error-banner").length,
    }));
    // It answered: the rows are there. It is labelled: the reader is told the
    // ordering is not current. And it is not an error, because a projection
    // that has not been rebuilt is a fact about the deployment.
    expect(state.rows).toBeGreaterThan(0);
    expect(state.text).toContain("not current");
    expect(state.banner).toBe(0);
  } finally {
    await bare.close();
    degraded.process.kill();
  }
});

test.skipIf(!chrome)("no control on this surface casts a vote", async () => {
  for (const route of ["read", "read?coverage=unreviewed", "read?kind=proposal"]) {
    await open(route);
    await page.waitForSelector(".page");
    const controls = await page.evaluate(() =>
      Array.from(document.querySelectorAll("button, input[type='submit'], select"))
        .map((control) => ((control as HTMLElement).innerText || (control as HTMLInputElement).value || "").toLowerCase()));
    for (const label of controls) {
      expect(label).not.toContain("upvote");
      expect(label).not.toContain("downvote");
      expect(label).not.toMatch(/\bvote\b/u);
    }
  }
});
