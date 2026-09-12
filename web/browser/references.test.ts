// Browser acceptance for a record's citations, driven against the synthetic
// mock so no Go server, archive, model, or network is needed.
//
// The two-column citation panel is gone with the pages that carried it (#235).
// A record's citations are now part of the record: what it rests on is depth 3,
// in the words of whoever said it and one click from the transcript line it
// came from, and the typed edges — the relation, which way it points, the
// record at the far end — are depth 5 with the rest of the machinery. Nothing
// about what a reader is allowed to believe about a link changed, which is what
// this file measures.
//
// What only a browser can prove is here.
//
// That depth 3 says which side of the claim each citation is on before quoting
// anything, so a reader skimming cannot take conflicting material for support.
//
// That a citation Babel can locate opens the transcript at the cited event, and
// that the derived route is a route rather than a plausible string — the click
// lands on the session.
//
// That a citation Babel cannot locate stays on the page as evidence a reader
// cannot check, saying so, and carries no link at all. Dropping the row would
// under-report what a record rests on.
//
// That depth 5 names each edge's relation and its direction, so "what is this
// built on" and "what rests on this" are distinguishable, and that every
// destination is derived into this app's own route: no absolute URL and no
// scriptable scheme, whatever a record says.
//
// That a note reaches the page as inert text: hostile markup renders as
// characters, executes nothing, and never becomes a link destination (§2.7).
//
// That a record with no citations has no evidence depth at all — absent, not an
// empty heading over nothing.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_MARKDOWN } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Typed reference graph gate",
  covers: "a record's citations — its evidence at depth 3 and its typed edges at depth 5 — in a browser",
  unverified: [
    "that each citation states which side of the claim it is on before it is read",
    "that a locatable citation opens the transcript at the cited event, and that the route resolves",
    "that a citation this deployment cannot locate stays on the page, says so, and carries no link",
    "that every edge at depth 5 names its relation and its direction and opens inside this app",
    "that a citation's note reaches the page as inert text and never as markup or a destination",
    "that a record with no citations renders no evidence depth at all",
  ],
});

// SHOTS is where the run leaves its evidence. A screenshot is not an assertion
// and nothing here passes or fails on one; it exists because a layout claim in a
// pull request should be checkable by looking, and BABEL_TEST_SHOTS lets CI or an
// operator put it somewhere durable instead of the temp directory.
const SHOTS = process.env.BABEL_TEST_SHOTS ?? join(tmpdir(), "babel-references-shots");

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

function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 15_000 },
    text,
  );
}

// dig opens one depth through its own control and waits for the body, because
// the peel's open state is the page's rather than the <details> element's:
// Contract K lets `1`-`5` toggle a depth from anywhere, so a test that set the
// attribute would be opening something the page does not think is open.
async function dig(title: string): Promise<void> {
  await page.evaluate((needle: string) => {
    const peel = Array.from(document.querySelectorAll("details.peel")).find((entry) =>
      (entry.querySelector("summary")?.textContent ?? "").startsWith(needle),
    );
    if (!(peel as HTMLDetailsElement | undefined)?.open) {
      (peel?.querySelector("summary") as HTMLElement | undefined)?.click();
    }
  }, title);
  await page.waitForFunction(
    (needle: string) =>
      Array.from(document.querySelectorAll("details.peel")).some(
        (entry) =>
          (entry.querySelector("summary")?.textContent ?? "").startsWith(needle) &&
          (entry as HTMLDetailsElement).open,
      ),
    { timeout: 15_000 },
    title,
  );
}

function peelTitles(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("details.peel summary")).map(
      (summary) => (summary as HTMLElement).innerText,
    ),
  );
}

// shoot photographs one element rather than the viewport. A panel that sits
// below the fold of a record page is not in a viewport screenshot at all, so a
// full-page capture or a scroll would be the alternative; framing the element
// is what makes the file usable as evidence of the panel's layout.
//
// The scroll leaves a gap above the element instead of using scrollIntoView,
// because the app's top bar is sticky: an element aligned to the viewport top
// is photographed with the nav painted over its first rows, which reads as a
// clipped panel in the very file that exists to show the panel is not clipped.
const STICKY_HEADER_CLEARANCE = 140;

async function shoot(selector: string, name: string): Promise<void> {
  const element = await page.waitForSelector(selector, { timeout: 15_000 });
  if (!element) throw new Error(`no element to photograph: ${selector}`);
  // The top bar is sticky and taller than the clearance a scroll can buy: a
  // capture that reaches past the viewport re-paints it at the top of the
  // clipped region, over the panel's own heading. Hiding it with `visibility`
  // rather than `display` keeps the layout identical, so the panel in the file
  // is the panel the page laid out.
  await page.evaluate(
    (target: string, clearance: number) => {
      const bar = document.querySelector(".topbar") as HTMLElement | null;
      if (bar) bar.style.visibility = "hidden";
      const found = document.querySelector(target);
      if (!found) return;
      const top = found.getBoundingClientRect().top + window.scrollY - clearance;
      window.scrollTo({ top: Math.max(top, 0), behavior: "instant" });
    },
    selector,
    STICKY_HEADER_CLEARANCE,
  );
  await element.screenshot({ path: join(SHOTS, name) });
  await page.evaluate(() => {
    const bar = document.querySelector(".topbar") as HTMLElement | null;
    if (bar) bar.style.visibility = "";
  });
}

beforeAll(async () => {
  if (!chrome) return;
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);
  mkdirSync(SHOTS, { recursive: true });
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

test.skipIf(!chrome)("a citation says which side of the claim it is on, and opens the transcript", async () => {
  await open("r/pro_criteria-template");
  await visible("The evidence");
  await dig("The evidence");

  const cited = await page.evaluate(async () => {
    const record = (await fetch("/api/record/pro_criteria-template").then((response) =>
      response.json(),
    )) as { evidence?: Array<{ kind?: string; quote?: string; href?: string }> };
    const rows = Array.from(document.querySelectorAll(".record-evidence > li"));
    return {
      served: record.evidence ?? [],
      sides: rows.map((row) => (row.querySelector(".record-side") as HTMLElement | null)?.innerText ?? ""),
      notes: rows.map((row) => (row.querySelector(".record-note") as HTMLElement | null)?.innerText ?? ""),
      // The row against the claim is marked as well as worded: §4.5 makes a
      // proposal state what conflicts with it, and the tint is the second way
      // of saying it rather than the only one.
      counter: rows.map((row) => row.classList.contains("record-counter")),
      links: rows.map((row) => (row.querySelector(".peel-cite a") as HTMLAnchorElement | null)?.getAttribute("href") ?? null),
    };
  });

  // One row per citation the server sent, in its order.
  expect(cited.sides).toHaveLength(cited.served.length);
  expect(cited.served.length).toBeGreaterThan(1);
  // Each row names its side before anything else in it, and the two sides of
  // this proposal read as two different claims about the record.
  expect(cited.sides.every((side) => side.length > 0)).toBe(true);
  expect(new Set(cited.sides).size).toBe(2);
  const supporting = cited.served.findIndex((item) => item.kind === "supporting");
  const conflicting = cited.served.findIndex((item) => item.kind === "conflicting");
  expect(cited.sides[supporting].toLowerCase()).toContain("support");
  expect(cited.sides[conflicting].toLowerCase()).toContain("conflict");
  expect(cited.counter[supporting]).toBe(false);
  expect(cited.counter[conflicting]).toBe(true);
  // What the model said about the citation is on the row, as the record's own
  // words rather than as a summary of them.
  for (const [index, item] of cited.served.entries()) {
    if (item.quote) expect(cited.notes[index]).toContain(item.quote);
    // The destination is the server's, and it names the event rather than a
    // byte offset the reader would have to resolve himself.
    expect(cited.links[index]).toBe(item.href ?? null);
    expect(cited.links[index]).toMatch(/^#\/sessions\/.+\?event=\d+$/u);
  }

  await shoot("details.peel.peel-open:has(.record-evidence)", "record-evidence.png");

  // And the route resolves: following a citation lands on the session it was
  // taken from, at the event it cites.
  const target = cited.links[0] ?? "";
  await page.click(".record-evidence > li .peel-cite a");
  await page.waitForFunction(() => window.location.hash.startsWith("#/sessions/"), {
    timeout: 15_000,
  });
  expect(decodeURIComponent(page.url())).toContain(decodeURIComponent(target.slice(1)));
});

test.skipIf(!chrome)("a citation this deployment cannot locate stays, says so, and is not a link", async () => {
  await open("r/obs_claim-no-verify");
  await visible("The evidence");
  await dig("The evidence");

  const rows = await page.evaluate(async () => {
    const record = (await fetch("/api/record/obs_claim-no-verify").then((response) =>
      response.json(),
    )) as { evidence?: Array<{ href?: string }> };
    return {
      served: record.evidence ?? [],
      cites: Array.from(document.querySelectorAll(".record-evidence > li")).map((row) => ({
        text: (row.querySelector(".peel-cite") as HTMLElement | null)?.innerText ?? "",
        href: (row.querySelector(".peel-cite a") as HTMLAnchorElement | null)?.getAttribute("href") ?? null,
      })),
    };
  });

  // The fixture is the pair that matters: one citation this deployment can
  // resolve to a session and one it cannot.
  const unlocatable = rows.served.findIndex((item) => !item.href);
  expect(unlocatable).toBeGreaterThanOrEqual(0);
  expect(rows.served.some((item) => item.href)).toBe(true);

  // The row is still there — evidence a reader cannot check is still evidence
  // — and it says which of the two it is instead of offering a dead link.
  expect(rows.cites).toHaveLength(rows.served.length);
  expect(rows.cites[unlocatable].href).toBeNull();
  expect(rows.cites[unlocatable].text.toLowerCase()).toContain("not locatable");
});

test.skipIf(!chrome)("depth 5 names each edge's relation and direction, and opens inside this app", async () => {
  await open("r/hyp_unverified-closures");
  await visible("The machinery");
  await dig("The machinery");

  const edges = await page.evaluate(async () => {
    const record = (await fetch("/api/record/hyp_unverified-closures").then((response) =>
      response.json(),
    )) as { machinery?: { links?: Array<{ kind: string; direction: string; id: string; title?: string }> } };
    const panel = Array.from(document.querySelectorAll(".peel-body .panel")).find(
      (section) => section.querySelector("h3")?.textContent === "Links",
    );
    const rows = Array.from(panel?.querySelectorAll("li") ?? []);
    return {
      served: record.machinery?.links ?? [],
      text: rows.map((row) => (row as HTMLElement).innerText),
      hrefs: rows.map((row) => (row.querySelector("a") as HTMLAnchorElement | null)?.getAttribute("href") ?? ""),
    };
  });

  // Both directions are on the page, which is the whole reason an edge carries
  // one: "what is this built on" and "what rests on this" are two questions.
  expect(edges.served.some((edge) => edge.direction === "to")).toBe(true);
  expect(edges.served.some((edge) => edge.direction === "from")).toBe(true);
  expect(edges.text).toHaveLength(edges.served.length);

  for (const [index, edge] of edges.served.entries()) {
    const row = edges.text[index];
    // The relation is named rather than drawn, and the direction is stated in
    // words: an arrow the reader has to decode is a direction he can invert.
    expect(row).toContain(edge.kind);
    expect(row).toContain(edge.direction === "from" ? "inbound" : "outbound");
    // The far end is identified. Where the server sent the other record's own
    // words they are shown, and the identifier is beside them rather than
    // instead of them.
    expect(row).toContain(edge.id);
    if (edge.title) expect(row).toContain(edge.title);
  }

  // Every destination is derived into this app's own route. A record says
  // nothing about where it opens, so no absolute URL and no scriptable scheme
  // can appear here however hostile the corpus is.
  expect(edges.hrefs.length).toBe(edges.served.length);
  for (const href of edges.hrefs) expect(href.startsWith("#/r/")).toBe(true);

  await shoot("details.peel.peel-open:has(.peel-rows)", "record-machinery-links.png");
});

test.skipIf(!chrome)("a citation's note renders as inert text, never as markup", async () => {
  await open("r/obs_hostile");
  await visible("The evidence");
  await dig("The evidence");

  const note = await page.evaluate(() => {
    const element = document.querySelector(".record-evidence .record-note");
    return {
      text: (element as HTMLElement | null)?.innerText ?? "",
      // The hostile fixture is markup. If any of it were parsed, the note
      // would contain child elements and the document would contain the tags
      // it names; both are measured rather than assumed.
      children: element?.children.length ?? -1,
      injected: document.querySelectorAll(".record-evidence script, .record-evidence img").length,
      untrusted: element?.classList.contains("untrusted-inline") ?? false,
      // And nothing in it became a destination: a note that could navigate the
      // reader is the corpus steering the interface.
      anchors: Array.from(document.querySelectorAll(".record-evidence a")).map(
        (anchor) => anchor.getAttribute("href") ?? "",
      ),
      pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    };
  });

  expect(note.untrusted).toBe(true);
  expect(note.children).toBe(0);
  expect(note.injected).toBe(0);
  expect(note.pwned).toBe("undefined");
  // The note's own characters survive: a citation says why the link exists, and
  // a surface that dropped the sentence because it contained a bracket would be
  // editing the corpus rather than neutralizing it.
  expect(note.text).toContain(HOSTILE_MARKDOWN);
  // Including the scheme it carries, as characters. Nothing on the panel
  // points anywhere but this app: a destination is derived from the server's
  // own href, never assembled out of something a record said.
  expect(note.text).toContain("javascript:");
  expect(note.anchors.length).toBeGreaterThan(0);
  for (const href of note.anchors) expect(href.startsWith("#/")).toBe(true);

  await shoot("details.peel.peel-open:has(.record-evidence)", "record-evidence-hostile-note.png");
});

// The two-column citation panel — "Cites" and "Cited by" with their edge-kind
// chips, their counts and their per-row attribution — is not on a record page
// any more, so the assertions about it are gone from this file rather than
// re-pointed at markup that no longer renders here. The panel itself is alive
// on the two surfaces that still mount it, a complaint and a session, where
// browser/complaints.test.ts measures both directions, the inert endpoint and
// the server's reason for it.

test.skipIf(!chrome)("a record with no citations has no evidence depth at all", async () => {
  await open("r/hyp_promoted-pattern");
  await visible("The claim");
  await page.waitForSelector("details.peel", { timeout: 15_000 });

  const state = await page.evaluate(async () => {
    const record = (await fetch("/api/record/hyp_promoted-pattern").then((response) =>
      response.json(),
    )) as { evidence?: unknown[]; machinery?: { links?: unknown[] } };
    return {
      evidence: record.evidence ?? [],
      links: record.machinery?.links ?? [],
      rows: document.querySelectorAll(".record-evidence > li").length,
      body: document.body.innerText,
    };
  });

  // The fixture is the record that cites nothing and that nothing cites.
  expect(state.evidence).toHaveLength(0);
  expect(state.links).toHaveLength(0);

  // Absent, not empty: no depth, no heading, no zero. A heading reading "The
  // evidence 0" claims the record was checked for evidence and found to have
  // none, which is a different fact from a record that rests on nothing it
  // recorded.
  const titles = await peelTitles();
  expect(titles.some((title) => title.startsWith("The evidence"))).toBe(false);
  expect(state.rows).toBe(0);
  expect(state.body).not.toContain("Not locatable");

  // The machinery is still there — every record has one — and it has no links
  // section rather than an empty one.
  expect(titles.some((title) => title.startsWith("The machinery"))).toBe(true);
  await dig("The machinery");
  const sections = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".peel-body .panel h3")).map(
      (heading) => heading.textContent ?? "",
    ),
  );
  expect(sections).not.toContain("Links");
});
