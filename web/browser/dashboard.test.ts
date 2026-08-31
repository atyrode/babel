// Browser acceptance for the landing page and the Help guide, driven against
// the synthetic mock so no Go server, archive, or network is needed (SPEC.md
// §10's fixture rule).
//
// What only a browser can prove is covered here: that the dashboard actually
// renders from one aggregate read, that every panel's jump link lands on the
// page or record that owns its rows, that a panel whose service is missing
// explains itself instead of taking the page down, that the first-visit pointer
// to Help is dismissible and stays dismissed, that Help is reachable from
// everywhere including a page that could not load, and that a hostile candidate
// statement renders inert in a panel.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_HTML } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

// A developer without Chrome skips this suite. The notice resolveChrome prints
// is what keeps the skip from reading as a pass: nothing else drives the
// landing page in a browser, so a green run that never launched one would
// report the operator's first screen as acceptable while no page was rendered.
// In CI the same absence is a hard failure.
const chrome = resolveChrome({
  gate: "Dashboard and Help web gate",
  covers: "the dashboard landing page and the Help guide in a browser",
  unverified: [
    "that the dashboard renders every panel from one /api/overview read, and that its totals agree with the pages that own them",
    "that every panel's jump link lands on the owning page or record",
    "that a panel whose service is unavailable explains itself while the rest of the page still renders",
    "that the first-visit pointer to Help dismisses and stays dismissed across a reload",
    "that Help is reachable from every page, including one whose data could not load",
    "that a hostile candidate statement renders inert inside a panel",
    "that neither route overflows at 390px or 1440px",
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

let rich: MockServer | null = null;
// A launch with neither storage configured nor an analysis store: the state a
// first run is in, and the one the landing page most has to render as an
// explanation rather than as a failure.
let bare: MockServer | null = null;
let browser: Browser | null = null;
let page: Page;

const WIDE = { width: 1440, height: 1100 };
const MID = { width: 900, height: 1200 };
const NARROW = { width: 390, height: 844 };

async function open(route: string, base = rich?.base): Promise<void> {
  await page.goto(`${base}/#/${route}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
}

function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 15_000 },
    text,
  );
}

// The banner is a first-visit state, so a test about anything else has to be
// able to start from a known one rather than inheriting the previous test's.
async function resetGuide(base = rich?.base): Promise<void> {
  await page.goto(`${base}/#/`, { waitUntil: "networkidle2" });
  await page.evaluate(() => window.localStorage.clear());
}

beforeAll(async () => {
  if (!chrome) return;

  // The mock serves web/dist, so the bundle under test is built from the
  // sources in this checkout rather than whatever was last committed.
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);

  [rich, bare] = await Promise.all([
    startMock({}),
    startMock({ MOCK_OVERVIEW: "degraded", MOCK_UNWIRED: "frontier,review,reality,search" }),
  ]);

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
  await page.setViewport(WIDE);
});

afterAll(async () => {
  await browser?.close();
  rich?.process.kill();
  bare?.process.kill();
});

test.skipIf(!chrome)("the launch URL lands on the dashboard with every panel answered", async () => {
  // No hash at all is what the token bootstrap leaves behind, so this is the
  // route a real launch actually arrives on.
  await page.goto(`${rich?.base}/`, { waitUntil: "networkidle2" });
  await page.waitForSelector(".panel-grid", { timeout: 15_000 });

  const state = await page.evaluate(() => ({
    hash: window.location.hash,
    panels: Array.from(document.querySelectorAll(".panel h2")).map((h) => h.textContent),
    notes: document.querySelectorAll(".panel-note").length,
    hosts: document.querySelectorAll(".host-list li").length,
    queue: document.querySelectorAll(".queue-list li").length,
    questions: document.querySelectorAll(".question-rows li").length,
    receipts: document.querySelectorAll(".receipt-list li").length,
    feed: document.querySelectorAll(".feed-list li").length,
    corpusSegments: document.querySelectorAll(".panel--corpus .dist-seg").length,
    frontierSegments: document.querySelectorAll(".panel--frontier .dist-seg").length,
    frontierRows: document.querySelectorAll(".panel--frontier .panel-rows li").length,
    spotlight: document.querySelectorAll(".panel--frontier .spotlight .panel-row-link").length,
  }));
  // The dashboard is home: the empty hash resolves here rather than redirecting.
  expect(state.hash === "" || state.hash === "#/").toBe(true);
  expect(state.panels).toEqual([
    "Archive health",
    "Corpus",
    "Hypothesis frontier",
    "Review inbox",
    "Recent runs",
    "Recent activity",
  ]);
  // Every service answered, so no panel is explaining an absence.
  expect(state.notes).toBe(0);
  // Non-vacuity, list by list: the panels are showing records, not empty
  // frames, and each list is the shape its panel owns.
  expect(state.hosts).toBe(2);
  expect(state.queue).toBe(4);
  expect(state.questions).toBe(3);
  expect(state.receipts).toBe(2);
  expect(state.feed).toBe(5);
  // The frontier is a distribution plus one spotlight, never a listing: the
  // records a human must act on render in the review queue, and the full
  // frontier belongs to the Hypotheses page. Repeating the same statements in
  // two adjacent panels was hierarchy-free duplication.
  expect(state.frontierRows).toBe(0);
  expect(state.spotlight).toBe(1);
  // The proportions render as filled segments — one per populated harness,
  // one per populated status.
  expect(state.corpusSegments).toBe(3);
  expect(state.frontierSegments).toBe(6);

  // The at-a-glance numbers are the owning services' own, rendered at hero
  // size with their labels beneath — facts that lead, not decoration.
  const heroes = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".stat-hero")).map((entry) => ({
      label: entry.querySelector(".stat-label")?.textContent ?? "",
      value: entry.querySelector(".stat-value")?.textContent ?? "",
    })));
  expect(heroes).toContainEqual({ label: "snapshots", value: "12" });
  expect(heroes).toContainEqual({ label: "sessions", value: "18" });
  expect(heroes).toContainEqual({ label: "candidates", value: "6" });
  expect(heroes).toContainEqual({ label: "awaiting a decision", value: "4" });

  // The whole exploration lifecycle is described, zeros included, so a
  // frontier with nothing rejected still shows that rejection is a state.
  const lifecycle = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".panel-chip .badge")).map((badge) => badge.textContent));
  expect(lifecycle).toEqual([
    "untriaged", "queued", "investigating", "deferred", "rejected", "promoted",
  ]);

  // The framing survives on the landing page: candidates are interpretations,
  // and ordering signals are not evidence.
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).toContain("ordering estimates only, never evidence");
  expect(text).toContain("Fallible interpretation, not established fact");
});

test.skipIf(!chrome)("the frontier panel's total agrees with the page that owns it", async () => {
  // A panel that disagreed with its own listing would be a second source of
  // truth, which is the one thing an aggregate read must not become.
  await open("");
  await page.waitForSelector(".panel--frontier .stat-value", { timeout: 15_000 });
  const panelTotal = await page.evaluate(() =>
    Number(document.querySelector(".panel--frontier .stat-value")?.textContent ?? "-1"));

  await open("hypotheses");
  await page.waitForSelector(".frontier-table tbody tr", { timeout: 15_000 });
  const listed = await page.evaluate(() => document.querySelectorAll(".frontier-table tbody tr").length);
  expect(panelTotal).toBe(listed);
});

test.skipIf(!chrome)("every panel jump link lands where it says", async () => {
  // The panel headings' links, in the order the grid renders them.
  const destinations = ["#/archive", "#/sessions", "#/hypotheses", "#/review", "#/explore", "#/sessions"];
  for (const [index, want] of destinations.entries()) {
    await open("");
    await page.waitForSelector(".panel-link", { timeout: 15_000 });
    const links = await page.$$(".panel-grid > .panel > .section-heading .panel-link");
    await links[index].click();
    await page.waitForFunction(
      (expected: string) => window.location.hash === expected,
      { timeout: 15_000 },
      want,
    );
    // The destination rendered, so the link is a working route rather than a
    // hash the router silently discarded.
    await page.waitForFunction(
      () => document.querySelector("main .page") !== null
        && document.querySelector(".state-card .spinner") === null,
      { timeout: 15_000 },
    );
  }

  // The ledger's own link is inside the review panel rather than in its
  // heading, because the question inbox is a section of its own.
  await open("");
  await page.waitForSelector(".panel-subsection .panel-link", { timeout: 15_000 });
  await page.click(".panel-subsection .panel-link");
  await page.waitForFunction(() => window.location.hash === "#/reality", { timeout: 15_000 });
});

test.skipIf(!chrome)("panel rows deep link to the records they show", async () => {
  for (const [selector, prefix] of [
    [".panel--frontier .spotlight .panel-row-link", "#/hypotheses/hyp_"],
    [".panel--review .queue-list .panel-row-link", "#/review/"],
    [".panel--activity .feed-list .panel-row-link", "#/sessions/"],
  ] as const) {
    await open("");
    await page.waitForSelector(selector, { timeout: 15_000 });
    await page.click(selector);
    await page.waitForFunction(
      (want: string) => window.location.hash.startsWith(want),
      { timeout: 15_000 },
      prefix,
    );
    // The record page rendered rather than reporting a missing identifier, so
    // the panel and the detail route agree about what an identifier is.
    const text = await page.evaluate(() => document.body.innerText);
    expect(text).not.toMatch(/no record with that identifier/i);
  }
});

test.skipIf(!chrome)("a launch with no services explains itself panel by panel", async () => {
  await open("", bare?.base);
  await page.waitForSelector(".panel-grid", { timeout: 15_000 });
  const state = await page.evaluate(() => ({
    panels: document.querySelectorAll(".panel").length,
    notes: Array.from(document.querySelectorAll(".panel-note")).map((note) => note.textContent),
    rows: document.querySelectorAll(".panel-rows li").length,
    // Nothing claims a number it could not read, so no panel renders a
    // glance row at all.
    facts: document.querySelectorAll(".panel-facts div").length,
    heroes: document.querySelectorAll(".stat-hero").length,
  }));
  // Six panels and seven notes: the review tile carries two sections, because
  // the review log and the Reality ledger are different stores.
  expect(state.panels).toBe(6);
  expect(state.notes).toHaveLength(7);
  expect(state.rows).toBe(0);
  expect(state.heroes).toBe(0);
  // The notes are the server's own wording, naming what to do about it.
  expect(state.notes.join(" ")).toContain("babel storage configure");
  expect(state.notes.join(" ")).toContain("not available in this session");
  expect(state.facts).toBe(0);

  // Help is still reachable from a page that could not load its data — which
  // is exactly when an operator needs it.
  await page.click('a[href="#/help"]');
  await visible("Creative, fallible, incomplete");
});

test.skipIf(!chrome)("the question inbox survives an unavailable review log", async () => {
  // Independent degradation is the property: the two halves of the review tile
  // come from different stores, so one missing must not hide the other.
  const partial = await startMock({ MOCK_UNWIRED: "review" });
  try {
    await open("", partial.base);
    await page.waitForSelector(".panel-subsection .panel-rows li", { timeout: 15_000 });
    const state = await page.evaluate(() => ({
      note: document.querySelector(".panel-grid > .panel:nth-child(4) > .panel-note")?.textContent ?? "",
      questions: document.querySelectorAll(".panel-subsection .panel-rows li").length,
    }));
    expect(state.note).toContain("review service is not available");
    expect(state.questions).toBe(3);
  } finally {
    partial.process.kill();
  }
});

test.skipIf(!chrome)("the Help guide explains the lifecycle and maps the commands", async () => {
  await open("help");
  await page.waitForSelector(".help-page", { timeout: 15_000 });
  const text = await page.evaluate(() => document.body.innerText);
  // The frame first: what Babel is, and what it is not.
  expect(text).toContain("not an audit");
  expect(text).toContain("Ordering is not evidence");
  expect(text).toContain("Nothing is deleted");
  // The lifecycle, in order.
  for (const stage of ["Archive", "Catalog", "Prepare", "Explore", "Hypotheses", "Review"]) {
    expect(text).toContain(stage);
  }
  // The vocabulary, including both status vocabularies a reader meets in the
  // interface.
  for (const term of ["Preparation", "Recipe", "Receipt", "Provenance"]) {
    expect(text).toContain(term);
  }
  const state = await page.evaluate(() => ({
    commands: Array.from(document.querySelectorAll(".help-page tbody tr td:first-child"))
      .map((cell) => cell.textContent),
    badges: Array.from(document.querySelectorAll(".help-badge-list .badge")).map((b) => b.textContent),
    // Every destination named in the map is a real route in this build.
    links: Array.from(document.querySelectorAll(".help-page a")).map((a) => a.getAttribute("href")),
  }));
  expect(state.commands).toContain("babel explore");
  expect(state.commands).toContain("babel review decide");
  expect(state.badges).toContain("rejected");
  expect(state.badges).toContain("refine-requested");
  for (const href of state.links) {
    expect(href).toMatch(/^#\/(|help|sessions|archive|explore|hypotheses|findings|reality|review)$/u);
  }

  // The guide is a page, not a request: nothing on it reads the API, so it
  // renders on a machine where nothing else does.
  const requests: string[] = [];
  const listener = (request: { url(): string }) => requests.push(request.url());
  page.on("request", listener);
  await open("help");
  await page.waitForSelector(".help-page", { timeout: 15_000 });
  page.off("request", listener);
  expect(requests.filter((url) => url.includes("/api/overview"))).toHaveLength(0);
});

test.skipIf(!chrome)("the first-visit pointer to Help dismisses and stays dismissed", async () => {
  await resetGuide();
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForSelector(".guide-banner", { timeout: 15_000 });

  // It points at Help and nowhere else, and it is one line rather than a tour.
  const banner = await page.evaluate(() => ({
    href: document.querySelector(".guide-banner a")?.getAttribute("href"),
    buttons: document.querySelectorAll(".guide-banner button").length,
  }));
  expect(banner.href).toBe("#/help");
  expect(banner.buttons).toBe(1);

  await page.click(".guide-banner button");
  await page.waitForFunction(() => document.querySelector(".guide-banner") === null, { timeout: 15_000 });

  // A reload is the test: a banner that came back would be an interface
  // arguing with someone who already read it.
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForSelector(".panel-grid", { timeout: 15_000 });
  const after = await page.evaluate(() => ({
    banner: document.querySelector(".guide-banner") !== null,
    flag: window.localStorage.getItem("babel.web.guide-dismissed"),
  }));
  expect(after.banner).toBe(false);
  expect(after.flag).toBe("1");

  // Clearing the flag brings it back, so the absence above is the flag's work
  // and not a banner that never rendered twice.
  await page.evaluate(() => window.localStorage.clear());
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForSelector(".guide-banner", { timeout: 15_000 });
  await page.click(".guide-banner button");
});

test.skipIf(!chrome)("a hostile candidate statement renders inert in a panel", async () => {
  await open("");
  await page.waitForSelector(".panel-rows li", { timeout: 15_000 });
  const state = await page.evaluate((needle: string) => ({
    pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    injectedImage: document.querySelector(".panel img[src='x']") !== null,
    scriptURL: Array.from(document.querySelectorAll<HTMLAnchorElement>(".panel a"))
      .some((anchor) => anchor.href.startsWith("javascript:")),
    // The markup is visible as text: escaped, not swallowed.
    literal: document.body.innerText.includes(needle),
  }), HOSTILE_HTML);
  expect(state.pwned).toBe("undefined");
  expect(state.injectedImage).toBe(false);
  expect(state.scriptURL).toBe(false);
  expect(state.literal).toBe(true);
});

test.skipIf(!chrome)("a full candidate statement renders once, in the queue that owns it", async () => {
  // The frontier and the review inbox used to list the same statements side
  // by side. The hierarchy now: the review queue is the one panel that lists
  // analytical records in full, and the frontier renders the distribution
  // plus its newest candidate. The hostile candidate awaits review, so its
  // statement must appear exactly once on the page — in the queue.
  await open("");
  await page.waitForSelector(".queue-list li", { timeout: 15_000 });
  const text = await page.evaluate(() => document.body.innerText);
  const first = text.indexOf(HOSTILE_HTML);
  expect(first).toBeGreaterThan(-1);
  expect(text.indexOf(HOSTILE_HTML, first + 1)).toBe(-1);
});

test.skipIf(!chrome)("a queue row disclosure reveals the identifier without navigating", async () => {
  // The glance shows two clamped lines and no identifier; the chevron
  // discloses the rest in place. Disclosure must never hijack the click that
  // means "open this record" — the title link does that.
  await open("");
  await page.waitForSelector(".queue-list li .disclose-button", { timeout: 15_000 });
  const before = await page.evaluate(() => ({
    hash: window.location.hash,
    ids: document.querySelectorAll(".queue-list li .secondary.mono").length,
  }));
  expect(before.ids).toBe(0);

  await page.click(".queue-list li:first-child .disclose-button");
  await page.waitForSelector(".queue-list li.expanded", { timeout: 15_000 });
  const after = await page.evaluate(() => ({
    hash: window.location.hash,
    expanded: document.querySelectorAll(".queue-list li.expanded").length,
    id: document.querySelector(".queue-list li.expanded .secondary.mono")?.textContent ?? "",
    aria: document.querySelector(".queue-list li:first-child .disclose-button")?.getAttribute("aria-expanded"),
  }));
  expect(after.hash).toBe(before.hash);
  expect(after.expanded).toBe(1);
  expect(after.id).toMatch(/^hyp_/u);
  expect(after.aria).toBe("true");

  // Collapse restores the glance row.
  await page.click(".queue-list li:first-child .disclose-button");
  await page.waitForFunction(
    () => document.querySelectorAll(".queue-list li.expanded").length === 0,
    { timeout: 15_000 },
  );
});

test.skipIf(!chrome)("the receipt strip is one row, and stubs that do not fit fold into Explore", async () => {
  // The strip slices the listing to the stub widths that actually fit, so a
  // receipt is never marooned on a ragged second line beside a void and never
  // stretched to a banner because it wrapped alone. What the strip cannot
  // show it counts, as a link to the page that owns the full listing.
  await page.setViewport(NARROW);
  await open("");
  await page.waitForSelector(".receipt-list li", { timeout: 15_000 });
  const narrow = await page.evaluate(() => ({
    stubs: document.querySelectorAll(".receipt-list li").length,
    more: document.querySelector(".receipt-more")?.textContent ?? "",
    href: document.querySelector(".receipt-more")?.getAttribute("href") ?? "",
  }));
  // One stub width fits at 390px, so the mock's second receipt is folded and
  // said out loud rather than clipped or wrapped.
  expect(narrow.stubs).toBe(1);
  expect(narrow.more).toContain("+1 more in Explore");
  expect(narrow.href).toBe("#/explore");

  await page.setViewport(WIDE);
  await open("");
  await page.waitForSelector(".receipt-list li", { timeout: 15_000 });
  const wide = await page.evaluate(() => ({
    stubs: document.querySelectorAll(".receipt-list li").length,
    rows: new Set(Array.from(document.querySelectorAll(".receipt-list li"))
      .map((li) => Math.round(li.getBoundingClientRect().top))).size,
    more: document.querySelectorAll(".receipt-more").length,
  }));
  // At full width every receipt fits: both stubs share one line and no link
  // claims a remainder that does not exist.
  expect(wide.stubs).toBe(2);
  expect(wide.rows).toBe(1);
  expect(wide.more).toBe(0);
});

test.skipIf(!chrome)("neither route overflows at 390px, 900px, or 1440px", async () => {
  for (const viewport of [WIDE, MID, NARROW]) {
    await page.setViewport(viewport);
    for (const route of ["", "help"]) {
      await open(route);
      await page.waitForFunction(
        () => document.querySelector("main .page") !== null
          && document.querySelector(".state-card .spinner") === null,
        { timeout: 15_000 },
      );
      const width = await page.evaluate(() => ({
        document: document.documentElement.scrollWidth,
        body: document.body.scrollWidth,
        inner: window.innerWidth,
      }));
      expect(`${route || "dashboard"}@${viewport.width}:${width.document <= width.inner + 1}`)
        .toBe(`${route || "dashboard"}@${viewport.width}:true`);
      expect(width.body).toBeLessThanOrEqual(width.inner + 1);
    }
  }
  await page.setViewport(WIDE);
});

test.skipIf(!chrome)("the dashboard reads once and keeps record content out of URLs", async () => {
  // One aggregate endpoint, not one per panel: six panels sharing a snapshot
  // is the whole reason /api/overview exists, and a landing page that fanned
  // out would pay six round trips before showing anything.
  //
  // The measurement is the set of endpoints rather than the number of
  // requests: this shell mounts under StrictMode, so every page in the
  // application issues each of its reads twice, and counting requests would
  // assert a fact about React instead of about this page.
  await open("");
  await page.waitForSelector(".panel-grid", { timeout: 15_000 });
  const requests: string[] = [];
  const listener = (request: { url(): string }) => requests.push(request.url());
  page.on("request", listener);
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForSelector(".panel-grid", { timeout: 15_000 });
  page.off("request", listener);

  const endpoints = [...new Set(requests
    .filter((url) => url.includes("/api/"))
    .map((url) => new URL(url).pathname))].sort();
  // The version banner belongs to the shell, not to a panel.
  expect(endpoints).toEqual(["/api/overview", "/api/version"]);
  for (const url of requests) {
    expect(decodeURIComponent(url)).not.toContain(HOSTILE_HTML);
    expect(decodeURIComponent(url)).not.toContain("regressions discovered");
  }
});
