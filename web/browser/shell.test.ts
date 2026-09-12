// Browser acceptance for the navigation shell and Settings (issue #235,
// §8.7), driven against the synthetic mock so no Go server, archive, or
// network is needed (SPEC.md §10's fixture rule).
//
// This file replaces the dashboard gate. The dashboard it defended was six
// panels summarizing five other pages and is gone; what survived is here.
//
// What only a browser can prove is covered:
//
// That the primary row is a handful of destinations, that the first of them is
// home, and that none of them names a record kind or a place Babel keeps
// bytes. Eleven concept-named entries were the first of #234's four structural
// faults: the reader had to know the data model before he could pick one. The
// row's exact membership is read off the page rather than written down here —
// it has changed twice while this file stood, and a list of words in a test is
// a second place to change it without the property it defends moving at all.
//
// That every path this build no longer serves redirects instead of 404ing. A
// navigation redesign that broke an operator's bookmarks, an issue's links and
// a terminal's printed URLs would read as data loss, and the redirect table is
// long enough that a dropped entry is easy to miss and invisible until someone
// follows an old link. It is enumerated here once, against App.tsx's routes.
//
// That every page of the surface fits. #234 measured /sessions at 15,311px and
// /review at 5,563px; the rule that came out of it is that a page states one
// thing and no page exceeds about three screens without pagination. The feed,
// a topic, a record with its thread, Watch, Settings and Sessions are all
// measured, because the rule is about pages and not about nav entries.
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
  covers: "§8.7's navigation row, its redirects, and the Settings sections, in a browser",
  unverified: [
    "that the primary row is a handful of destinations led by home, and that none of them names a record kind or a store",
    "that every path the cutover removed redirects to its successor rather than 404ing",
    "that the feed, a topic, Watch, Settings, a record with its thread and Sessions each fit about three screens and overflow no viewport",
    "that the orientation text is reachable under Settings, reads no API, and links only to pages this build serves",
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

test.skipIf(!chrome)("the primary row is a few destinations, led by home", async () => {
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
  // #234's fault was eleven entries, not four or five: what has to hold is
  // that the row stays short, that it starts at home, and that every entry is
  // a top-level route with a sentence saying what it answers. Which
  // destinations those are is the design's business and is read off the page.
  expect(entries.length).toBeGreaterThan(1);
  expect(entries.length).toBeLessThanOrEqual(5);
  expect(entries[0].href).toBe("#/");
  for (const entry of entries) {
    expect(`${entry.href}:${entry.label.length > 0}`).toBe(`${entry.href}:true`);
    expect(`${entry.href}:${entry.title.length > 0}`).toBe(`${entry.href}:true`);
    expect(entry.href).toMatch(/^#\/[a-z-]*$/u);
  }

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
  // /queue, /review and /evaluation/coverage are here now: the mod queue was
  // a destination until §8.7 made it the feed's own filter, and each of those
  // three meant "what needs me", which is what the front page arrives
  // showing.
  const moved: Array<[string, string]> = [
    ["queue", "#/"],
    ["review", "#/"],
    ["evaluation/coverage", "#/"],
    // The per-record rows below name pro_criteria-template, which is the
    // proposal this fixture actually holds. pro_bare-vote stood here until
    // the cutover moved the record peel's fixture into mock/phaseb.ts and
    // left that id only in the evaluation fixture, where no /api/record/{id}
    // answers for it — so every row pointing at it was asserting a redirect
    // onto a page that reads "This record could not be read."
    ["review/proposal/pro_criteria-template", "#/r/pro_criteria-template"],
    // /read was a destination and is now the feed. The kind it filtered by is
    // the one thing that bookmark carried which the feed still answers, so it
    // survives as a chip; everything else that URL could say belonged to the
    // page this section replaces.
    ["read", "#/"],
    ["read?kind=finding", "#/?kind=finding"],
    ["read/backlog", "#/"],
    // A per-kind bookmark meant every record of that kind, so it lands with
    // the operator's own filter off: arriving under "needs me" would answer a
    // narrower question than the link asked.
    ["findings", "#/?kind=finding&needs=all"],
    ["findings/fnd_conflicting-evidence", "#/r/fnd_conflicting-evidence"],
    ["proposals", "#/?kind=proposal&needs=all"],
    ["proposals/pro_criteria-template", "#/r/pro_criteria-template"],
    ["hypotheses", "#/?kind=hypothesis&needs=all"],
    ["hypotheses/hyp_unverified-closures", "#/r/hyp_unverified-closures"],
    ["evaluation", "#/?needs=all"],
    ["evaluation/policy", "#/settings?section=policy"],
    ["evaluation/proposal/pro_criteria-template", "#/r/pro_criteria-template"],
    ["explore", "#/watch"],
    // The fleet page was about machines, and the machine is no longer a
    // dimension of the reading path: the bookmark lands on Watch rather than
    // on Watch carrying a query nothing answers.
    ["fleet", "#/watch"],
    ["archive", "#/settings?section=archive"],
    ["help", "#/settings?section=help"],
    // The ledger's inbox is the feed filtered to the questions Babel asks, so
    // /reality lands there rather than on a page of its own; the ledger's
    // subjects and beliefs keep their pages under /ask.
    ["reality", "#/?kind=question"],
    ["reality/questions", "#/ask/questions"],
    ["reality/entities/ent_atlas", "#/ask/entities/ent_atlas"],
    ["reality/facts", "#/ask/facts"],
    ["reality/focus", "#/settings?section=ceilings"],
    // The catch-all, which is load-bearing rather than cosmetic: the launch
    // URL's "#nonce=…" fragment matches no route and falls through to it, and
    // it is what keeps a path this build never served from reading as a 404.
    ["a-path-this-build-never-served", "#/"],
  ];
  for (const [from, to] of moved) {
    await page.goto(`${mock?.base}/#/${from}`, { waitUntil: "networkidle2" });
    const hash = await landed(to);
    expect(`${from} -> ${hash}`).toBe(`${from} -> ${hash.startsWith(to) ? hash : to}`);
  }
});

test.skipIf(!chrome)("a record opens by identity, whatever kind it is", async () => {
  // One record is one page. The kind used to be in the route, which is how the
  // same proposal came to have four of them.
  //
  // Landing is not opening, so both are read. This test asserted the hash
  // alone and named pro_bare-vote, an id no fixture serves since the peel's
  // state moved to mock/phaseb.ts: it passed while the page it measured was
  // the error state, which is exactly the failure a route-by-identity test
  // exists to catch. What the record page renders when it has a record is its
  // post header and its depths, and what it renders when it has none is the
  // server's own sentence — so the claim has to be on screen and that sentence
  // must not be.
  for (const id of ["hyp_unverified-closures", "fnd_conflicting-evidence", "pro_criteria-template"]) {
    await page.goto(`${mock?.base}/#/r/${id}`, { waitUntil: "networkidle2" });
    const hash = await landed(`#/r/${id}`);
    expect(hash).toBe(`#/r/${id}`);
    // The wait is on the page having settled either way — the claim, or the
    // sentence that says why there is none — so a record this deployment
    // cannot open is reported as the state it rendered rather than as a
    // selector timeout with nothing in it a reader can act on.
    await page.waitForFunction(
      () => document.querySelector(".record-post .record-claim") !== null
        || document.querySelector(".state-note.error-state") !== null,
      { timeout: 15_000 },
    );
    const opened = await page.evaluate(() => ({
      claim: (document.querySelector(".record-post .record-claim") as HTMLElement | null)
        ?.innerText.trim() ?? "",
      unreadable: (document.querySelector(".state-note.error-state") as HTMLElement | null)
        ?.innerText.trim() ?? "",
      depths: document.querySelectorAll("details.peel").length,
    }));
    expect(`${id}: ${opened.unreadable || "read"}`).toBe(`${id}: read`);
    expect(`${id}: claim ${opened.claim.length > 0}, depths ${opened.depths > 0}`)
      .toBe(`${id}: claim true, depths true`);
  }
});

test.skipIf(!chrome)("no page of the reading surface overflows a viewport, and none runs long", async () => {
  // §8.6: a page states one thing and does not run past roughly three screens
  // without pagination. #234 measured /sessions at 15,311px and /review at
  // 5,563px, and the rule is only a rule while something measures it. The bar
  // is 3.2 screens rather than a flat 3 so that a page landing on the line is
  // reported for its layout and not for a scrollbar's rounding.
  //
  // Settings' orientation section is excluded by intention: it is reference
  // text, read once and reached deliberately, which is the one kind of page
  // the rule does not govern.
  const CEILING = WIDE.height * 3.2;
  // Every destination the row offers; the feed narrowed to one topic, which is
  // the same page with a rail selection and is where a long list of rows would
  // show first; the record page a feed row opens, whose thread is part of the
  // page now (§8.7) and is measured with it; and Sessions, which has no nav
  // entry and is the page the rule was written about.
  const surface = [
    "",
    "t/atlas",
    "watch",
    "settings",
    "r/pro_criteria-template",
    "sessions",
  ];
  for (const viewport of [WIDE, NARROW]) {
    await page.setViewport(viewport);
    for (const route of surface) {
      await open(route);
      await rendered();
      // A record's conversation is read on the record, so the measurement
      // waits for the thread to have answered rather than for the claim
      // above it: a page measured before its comments arrive is a page
      // measured without the part most likely to run long.
      if (route.startsWith("r/")) {
        await page.waitForFunction(
          () => document.querySelector(".record-thread ol.record-thread-list") !== null
            || (document.querySelector(".record-thread")?.textContent ?? "").includes("No comments yet"),
          { timeout: 15_000 },
        );
      }
      const size = await page.evaluate(() => ({
        document: document.documentElement.scrollWidth,
        body: document.body.scrollWidth,
        inner: window.innerWidth,
        height: document.documentElement.scrollHeight,
      }));
      const name = `${route || "home"}@${viewport.width}`;
      expect(`${name}:${size.document <= size.inner + 1}`).toBe(`${name}:true`);
      expect(size.body).toBeLessThanOrEqual(size.inner + 1);
      if (viewport === WIDE) {
        expect(`${name}:${size.height}<=${CEILING}`).toBe(`${name}:${Math.min(size.height, CEILING)}<=${CEILING}`);
      }
    }
  }
  await page.setViewport(WIDE);
});

// Two tests stood here and neither is re-pinned, because both measured a page
// this section replaced rather than a guarantee that moved:
//
//   - "Decide leads with the queue, and every row says why it is next" was
//     about the landing page's order, and the landing page is the feed. The
//     queue of things awaiting a ruling is being folded into the rows of that
//     feed, so there is no second listing left to lead with anything.
//   - "Read is one list whose facets are chips, not a query form" was about
//     the ranked listing, which is the feed's own sort bar and chips now. What
//     it defended — a facet is a chip and never a select — belongs with those
//     controls rather than with a page this build no longer routes.

test.skipIf(!chrome)("the orientation text is a Settings section and reads no API", async () => {
  await open("settings?section=help");
  await page.waitForSelector(".help-section", { timeout: 15_000 });
  const text = await page.evaluate(() => document.body.innerText);
  // The frame first: what Babel is, and what it is not.
  expect(text).toContain("not an audit");
  expect(text).toContain("Ordering is not evidence");
  expect(text).toContain("Nothing is deleted");
  // The lifecycle is read out of its own list rather than out of the page's
  // text, because a word like "Archive" appears in the sidebar and in the
  // vocabulary too: what has to hold is that the guide still walks the whole
  // pipeline, from the bytes it starts with to the ruling it ends at.
  const lifecycle = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".help-lifecycle")[0]?.children ?? [])
      .map((item) => item.querySelector("strong")?.textContent ?? ""));
  expect(lifecycle[0]).toBe("Archive");
  expect(lifecycle.at(-1)).toBe("Decide");
  expect(lifecycle.length).toBeGreaterThanOrEqual(5);
  for (const stage of lifecycle) expect(stage.length).toBeGreaterThan(0);
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
  // Every destination the guide names is a page this build serves. A guide
  // that pointed at a page the redesign removed would be the one document an
  // operator trusts sending him nowhere — so each link is followed rather than
  // matched against a list of routes written down here, which would have to be
  // rewritten every time the surface moves and would say nothing about whether
  // the link works.
  const links = [...new Set(state.links.filter((href): href is string => Boolean(href)))];
  expect(links.length).toBeGreaterThan(3);
  for (const href of links) {
    await page.goto(`${mock?.base}/${href}`, { waitUntil: "networkidle2" });
    await rendered();
    const arrived = await page.evaluate(() => ({
      hash: window.location.hash,
      failed: document.querySelector("main .error-state") !== null,
    }));
    // A path this build does not serve falls through App.tsx's catch-all to
    // the feed, so a link that has gone stale arrives somewhere other than
    // where it pointed.
    expect(`${href} -> ${arrived.hash}`).toBe(`${href} -> ${arrived.hash.startsWith(href) ? arrived.hash : href}`);
    expect(`${href}:${arrived.failed}`).toBe(`${href}:false`);
  }

  // The orientation text is a page, not a request: the section itself reads
  // nothing, so it renders on a machine where nothing else does. The shell's
  // own reads — the bootstrap exchange, the version in the wordmark, and the
  // live mark in the instrument cluster — belong to the chrome and happen on
  // every route including this one.
  const shellReads = ["/api/version", "/api/bootstrap", "/api/watch/live"];
  const requests: string[] = [];
  const listener = (request: { url(): string }) => requests.push(request.url());
  page.on("request", listener);
  await open("settings?section=help");
  await page.waitForSelector(".help-section", { timeout: 15_000 });
  page.off("request", listener);
  const reads = requests.filter(
    (url) => url.includes("/api/") && !shellReads.some((shell) => url.includes(shell)),
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
