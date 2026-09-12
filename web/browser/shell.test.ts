// Browser acceptance for the navigation shell and Settings (issue #235),
// driven against the synthetic mock so no Go server, archive, or network is
// needed (SPEC.md §10's fixture rule).
//
// This file replaces the dashboard gate. The dashboard it defended was six
// panels summarizing five other pages and is gone; what survived is here.
//
// What only a browser can prove is covered:
//
// That the primary row is four questions and a drawer, and that none of the
// five entries names a record kind or a place Babel keeps bytes. Eleven
// concept-named entries were the first of #234's four structural faults: the
// reader had to know the data model before he could pick one.
//
// That every path this build no longer serves redirects instead of 404ing. A
// navigation redesign that broke an operator's bookmarks, an issue's links and
// a terminal's printed URLs would read as data loss, and the redirect table is
// long enough that a dropped entry is easy to miss and invisible until someone
// follows an old link.
//
// That the four pages fit. #234 measured /sessions at 15,311px and /review at
// 5,563px; the rule that came out of it is that a page states one thing and no
// page exceeds about three screens without pagination.
//
// That the orientation text and the review policy are still reachable now that
// they are sections of Settings rather than destinations — including that the
// orientation text still reads the API not at all, so it renders on a machine
// where nothing else does.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Navigation shell and Settings gate",
  covers: "issue #235's four-entry navigation, its redirects, and the Settings sections, in a browser",
  unverified: [
    "that the primary row is Decide, Read, Watch, Ask and Settings, and names no record kind",
    "that every path the cutover removed redirects to its successor rather than 404ing",
    "that Decide, Read, Watch and Ask each fit about three screens and overflow no viewport",
    "that the orientation text is reachable under Settings and still reads no API",
    "that the review policy states what is running and that saving it starts nothing",
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

const WIDE = { width: 1440, height: 900 };
const MID = { width: 900, height: 1200 };
const NARROW = { width: 390, height: 844 };

async function open(route: string): Promise<void> {
  await page.goto(`${mock?.base}/#/${route}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
}

function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 15_000 },
    text,
  );
}

// landed waits for the router to actually answer, then reports where it did.
//
// The wait is on the destination rather than on a duration, for two reasons
// that are both real races. A redirect is a render-time <Navigate replace>,
// so the hash the browser reports immediately after a hash-only goto is still
// the one that was asked for. And Read pins the ranked set's snapshot into
// the query from an effect after its first answer lands, so a hash read one
// tick too early on the way past Read reads the listing's URL instead of the
// destination's.
//
// A timeout is swallowed on purpose: the caller's assertion then prints the
// hash the router really produced, which is the thing worth reading when a
// redirect has been dropped.
async function landed(prefix: string): Promise<string> {
  try {
    await page.waitForFunction(
      (want: string) => window.location.hash.startsWith(want),
      { timeout: 10_000 },
      prefix,
    );
  } catch {
    // Fall through to report the actual hash.
  }
  return page.evaluate(() => window.location.hash);
}

// rendered waits for a page to have painted its content rather than its
// loading state, for the measurements that do not care which route answered.
function rendered(): Promise<unknown> {
  return page.waitForFunction(
    () => document.querySelector("main .page") !== null
      && document.querySelector(".state-note .spinner") === null,
    { timeout: 15_000 },
  );
}

beforeAll(async () => {
  if (!chrome) return;
  // The mock serves web/dist, so the bundle under test is built from the
  // working tree rather than from whatever was committed.
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);
  mock = await startMock({});
  browser = await puppeteer.launch({
    executablePath: chrome,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
  await page.setViewport(WIDE);
});

afterAll(async () => {
  await browser?.close();
  mock?.process.kill();
});

test.skipIf(!chrome)("the primary row is four questions and a drawer", async () => {
  await open("");
  const entries = await page.evaluate(() =>
    Array.from(document.querySelectorAll("nav[aria-label='Primary navigation'] a")).map((link) => ({
      label: (link as HTMLElement).innerText.trim(),
      href: link.getAttribute("href") ?? "",
      // The question each entry answers rides its title rather than a line of
      // prose under the row.
      title: link.getAttribute("title") ?? "",
    })),
  );
  expect(entries.map((entry) => entry.label)).toEqual(["Decide", "Read", "Watch", "Ask", "Settings"]);
  expect(entries.map((entry) => entry.href)).toEqual(["#/", "#/read", "#/watch", "#/ask", "#/settings"]);
  for (const entry of entries) expect(entry.title.length).toBeGreaterThan(0);

  // #234's first fault, stated as a test: no entry names a record kind or a
  // store. A reader who does not know Babel's data model must still be able to
  // pick one.
  const forbidden = [
    "finding", "proposal", "hypothes", "evaluation", "review", "reality",
    "session", "archive", "explore", "fleet", "focus",
  ];
  for (const entry of entries) {
    for (const word of forbidden) {
      expect(entry.label.toLowerCase()).not.toContain(word);
    }
  }
});

test.skipIf(!chrome)("every path the cutover removed redirects rather than 404s", async () => {
  // The left column is what an operator may still have written down; the right
  // is what this build answers with. A redirect that lands on the catch-all
  // would show "#/" here and fail, which is the failure that matters: it reads
  // to the operator as the record having been deleted.
  const moved: Array<[string, string]> = [
    ["review", "#/"],
    ["review/proposal/pro_bare-vote", "#/r/pro_bare-vote"],
    ["findings", "#/read?kind=finding"],
    ["findings/fnd_conflicting-evidence", "#/r/fnd_conflicting-evidence"],
    ["proposals", "#/read?kind=proposal"],
    ["proposals/pro_bare-vote", "#/r/pro_bare-vote"],
    ["hypotheses", "#/read?kind=hypothesis"],
    ["hypotheses/hyp_unverified-closures", "#/r/hyp_unverified-closures"],
    ["evaluation", "#/read"],
    ["evaluation/coverage", "#/read?coverage=unreviewed"],
    ["evaluation/policy", "#/settings?section=policy"],
    ["evaluation/proposal/pro_bare-vote", "#/r/pro_bare-vote"],
    ["explore", "#/watch"],
    ["fleet", "#/watch?view=fleet"],
    ["archive", "#/settings?section=archive"],
    ["help", "#/settings?section=help"],
    ["reality", "#/ask"],
    ["reality/questions", "#/ask/questions"],
    ["reality/entities/ent_atlas", "#/ask/entities/ent_atlas"],
    ["reality/facts", "#/ask/facts"],
    ["reality/focus", "#/settings?section=ceilings"],
  ];
  for (const [from, to] of moved) {
    await page.goto(`${mock?.base}/#/${from}`, { waitUntil: "networkidle2" });
    const hash = await landed(to);
    // startsWith rather than equality: Read pins the ranked set's snapshot into
    // the query on its first answer, which is its own contract and not this
    // one's business.
    expect(`${from} -> ${hash}`).toBe(`${from} -> ${hash.startsWith(to) ? hash : to}`);
  }
});

test.skipIf(!chrome)("a record opens by identity, whatever kind it is", async () => {
  // One record is one page. The kind used to be in the route, which is how the
  // same proposal came to have four of them.
  for (const id of ["hyp_unverified-closures", "fnd_conflicting-evidence", "pro_bare-vote"]) {
    await page.goto(`${mock?.base}/#/r/${id}`, { waitUntil: "networkidle2" });
    const hash = await landed(`#/r/${id}`);
    expect(hash).toBe(`#/r/${id}`);
  }
});

test.skipIf(!chrome)("no page of the four overflows a viewport, and none runs long", async () => {
  // Three screens of the widest viewport. Settings' orientation section is
  // excluded by intention: it is reference text, read once and reached
  // deliberately, which is the one kind of page the rule does not govern.
  const CEILING = WIDE.height * 3;
  for (const viewport of [WIDE, MID, NARROW]) {
    await page.setViewport(viewport);
    for (const route of ["", "read", "watch", "watch?view=fleet", "ask", "settings"]) {
      await open(route);
      await rendered();
      const size = await page.evaluate(() => ({
        document: document.documentElement.scrollWidth,
        body: document.body.scrollWidth,
        inner: window.innerWidth,
        height: document.documentElement.scrollHeight,
      }));
      const name = `${route || "decide"}@${viewport.width}`;
      expect(`${name}:${size.document <= size.inner + 1}`).toBe(`${name}:true`);
      expect(size.body).toBeLessThanOrEqual(size.inner + 1);
      if (viewport === WIDE) {
        expect(`${name}:${size.height <= CEILING}`).toBe(`${name}:true`);
      }
    }
  }
  await page.setViewport(WIDE);
});

test.skipIf(!chrome)("Decide's header is three numbers and its rows are one line each", async () => {
  await open("");
  await page.waitForSelector(".queue-row", { timeout: 15_000 });
  const state = await page.evaluate(() => {
    const tallies = Array.from(document.querySelectorAll(".tally-item")).map((item) => ({
      count: item.querySelector(".tally-count")?.textContent ?? "",
      sentence: item.querySelector(".tally-sentence")?.textContent ?? "",
    }));
    const rows = Array.from(document.querySelectorAll(".queue-row")).map((row) => ({
      // One line: the claim is clamped to a single line, so its rendered box is
      // one line-height tall however long the model's wording is.
      lines: Math.round(
        (row.querySelector(".queue-claim") as HTMLElement).getBoundingClientRect().height /
          parseFloat(getComputedStyle(row.querySelector(".queue-claim") as HTMLElement).lineHeight),
      ),
      facts: row.querySelectorAll(".queue-facts > *").length,
      href: row.querySelector("a")?.getAttribute("href") ?? "",
    }));
    return { tallies, rows };
  });
  expect(state.tallies).toHaveLength(3);
  for (const tally of state.tallies) {
    expect(tally.count.length).toBeGreaterThan(0);
    // One sentence each, and a sentence rather than a label.
    expect(tally.sentence.length).toBeGreaterThan(20);
  }
  expect(state.rows.length).toBeGreaterThan(0);
  for (const row of state.rows) {
    expect(row.lines).toBe(1);
    // The density rule, measured: a claim and at most three facts.
    expect(row.facts).toBeLessThanOrEqual(3);
    expect(row.href).toMatch(/^#\/(r|ask)\//u);
  }
});

test.skipIf(!chrome)("Read is one list with the filters the four listings had", async () => {
  await open("read");
  await page.waitForSelector(".output-row", { timeout: 15_000 });
  const state = await page.evaluate(() => ({
    // Kind, standing, order, coverage and review role: every facet the four
    // separate listings offered, as controls on one list.
    filters: Array.from(document.querySelectorAll(".read-filters label > span")).map(
      (label) => label.textContent ?? "",
    ),
    kinds: Array.from(
      document.querySelectorAll<HTMLSelectElement>(".read-filters select"),
    )[0].options.length,
    rows: Array.from(document.querySelectorAll(".output-row")).map((row) => ({
      facts: row.querySelectorAll(".output-facts > *").length,
      href: row.querySelector("a")?.getAttribute("href") ?? "",
    })),
    paged: document.querySelector(".pager") !== null,
  }));
  expect(state.filters).toEqual(["Kind", "Standing", "Order", "Reviewed", "By role"]);
  // Every kind plus the "every kind" default: the listing pages this replaced
  // were one destination per kind.
  expect(state.kinds).toBeGreaterThan(3);
  expect(state.rows.length).toBeGreaterThan(0);
  for (const row of state.rows) {
    expect(row.facts).toBeLessThanOrEqual(3);
    expect(row.href).toMatch(/^#\/r\//u);
  }
  // Server-side paging survives: the fixture holds more than one page and the
  // page does not fetch them all.
  expect(state.paged).toBe(true);
});

test.skipIf(!chrome)("the orientation text is a Settings section and reads no API", async () => {
  await open("settings?section=help");
  await page.waitForSelector(".help-section", { timeout: 15_000 });
  const text = await page.evaluate(() => document.body.innerText);
  // The frame first: what Babel is, and what it is not.
  expect(text).toContain("not an audit");
  expect(text).toContain("Ordering is not evidence");
  expect(text).toContain("Nothing is deleted");
  for (const stage of ["Archive", "Catalog", "Prepare", "Explore", "Hypotheses", "Review"]) {
    expect(text).toContain(stage);
  }
  for (const term of ["Preparation", "Recipe", "Receipt", "Provenance"]) {
    expect(text).toContain(term);
  }

  const state = await page.evaluate(() => ({
    commands: Array.from(document.querySelectorAll(".help-section tbody tr td:first-child"))
      .map((cell) => cell.textContent),
    badges: Array.from(document.querySelectorAll(".help-badge-list .badge")).map((b) => b.textContent),
    links: Array.from(document.querySelectorAll(".help-section a")).map((a) => a.getAttribute("href")),
  }));
  expect(state.commands).toContain("babel explore");
  expect(state.commands).toContain("babel review decide");
  expect(state.badges).toContain("rejected");
  expect(state.badges).toContain("refine-requested");
  // Every destination the guide names is a route this build serves. A guide
  // that pointed at a page the redesign removed would be the one document an
  // operator trusts sending him nowhere.
  for (const href of state.links) {
    expect(href).toMatch(/^#\/(|read|watch|ask|sessions|settings)(\?[a-z=&_-]+)?$/u);
  }

  // The orientation text is a page, not a request: the section itself reads
  // nothing, so it renders on a machine where nothing else does. The shell's
  // own two reads — the bootstrap exchange and the version in the wordmark —
  // are the shell's and happen on every route including this one.
  const requests: string[] = [];
  const listener = (request: { url(): string }) => requests.push(request.url());
  page.on("request", listener);
  await open("settings?section=help");
  await page.waitForSelector(".help-section", { timeout: 15_000 });
  page.off("request", listener);
  const reads = requests.filter(
    (url) => url.includes("/api/") && !url.includes("/api/version") && !url.includes("/api/bootstrap"),
  );
  expect(reads).toEqual([]);
});

test.skipIf(!chrome)("the runtime model says when Babel runs and where AI touches", async () => {
  await open("settings?section=help");
  await page.waitForSelector("#runtime-model", { timeout: 15_000 });
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).toContain("When does Babel run?");
  expect(text).toContain("Am I talking to an AI?");
  expect(text).toContain("Between runs, no agent exists.");

  // The diagrams are labelled images, not decoration: role="img" with an
  // aria-labelledby that resolves to a non-empty title and description.
  const diagrams = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".runtime-loops svg")).map((svg) => ({
      role: svg.getAttribute("role"),
      labels: (svg.getAttribute("aria-labelledby") ?? "")
        .split(/\s+/u)
        .map((id) => document.getElementById(id)?.textContent?.trim().length ?? 0),
    })));
  expect(diagrams).toHaveLength(2);
  for (const diagram of diagrams) {
    expect(diagram.role).toBe("img");
    expect(diagram.labels).toHaveLength(2);
    for (const length of diagram.labels) expect(length).toBeGreaterThan(0);
  }

  // The table answers per surface, and no answer is "yes": Babel has no
  // conversational surface.
  const table = await page.evaluate(() => ({
    surfaces: Array.from(document.querySelectorAll(".runtime-ai-table tbody td:first-child"))
      .map((cell) => cell.textContent ?? ""),
    answers: Array.from(document.querySelectorAll(".runtime-ai-table tbody .badge"))
      .map((badge) => badge.textContent ?? ""),
  }));
  expect(table.surfaces.length).toBeGreaterThanOrEqual(6);
  expect(table.surfaces.join("\n")).toContain("babel explore");
  expect(table.surfaces.join("\n")).toContain("babel prepare");
  expect([...new Set(table.answers)].sort()).toEqual(["never", "only during a run"]);
});

test.skipIf(!chrome)("the policy states what is running, and saving it starts nothing", async () => {
  await open("settings?section=policy");
  await visible("What review work may cost");
  // A paused deployment says so, and says it differently from "unavailable".
  await page.waitForSelector("[data-status='paused']");
  let text = await page.evaluate(() => document.body.innerText);
  expect(text).toContain("authorized evaluation work is paused");
  expect(text).toContain("starts no run, launches no compute");

  // Enabling the policy does not make anything run: with nothing claimed the
  // honest state is "awaiting its next scheduled draw".
  await page.click(".evaluation-enabled input[type='checkbox']");
  await page.click(".evaluation-policy-form button[type='submit']");
  await page.waitForSelector("[data-status='scheduled']");
  text = await page.evaluate(() => document.body.innerText);
  expect(text).toContain("enabled with nothing in flight");
  expect(text).not.toContain("Running");
  // The change is a record, not a settings blob.
  expect(text).toContain("attributed record");

  // A budget edit is stored and read back from the service rather than echoed
  // from the form, which is what makes the version bump beside it meaningful.
  const before = await page.$eval("[data-knob='daily_cost']", (input) => (input as HTMLInputElement).value);
  await page.click("[data-knob='daily_cost']", { count: 3 });
  await page.keyboard.type("9");
  await page.click(".evaluation-policy-form button[type='submit']");
  await page.waitForFunction(() => document.body.innerText.includes("eval-policy-3"));
  const after = await page.$eval("[data-knob='daily_cost']", (input) => (input as HTMLInputElement).value);
  expect(after).toBe("9");
  expect(after).not.toBe(before);
  // Storing a larger ceiling still started nothing.
  expect(await page.$("[data-status='scheduled']")).not.toBeNull();
  expect(await page.evaluate(() => document.body.innerText)).toContain("starts no run, launches no compute");
});
