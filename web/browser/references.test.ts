// Browser acceptance for issue #113's citation section, driven against the
// synthetic mock so no Go server, archive, model, or network is needed.
//
// What only a browser can prove is here, and all of it is about what a reader
// is allowed to believe about a link.
//
// That both directions render as two labelled halves with their edge-kind chips,
// so "what is this built on" and "what rests on this" are distinguishable at a
// glance rather than merged into one list of arrows.
//
// That an endpoint this host cannot open renders as identified text with the
// server's own reason beside it, and carries no href at all — the case that
// matters most, because #113 makes an edge's shape readable on a machine that
// holds neither the record nor the key to it, so a correct citation list
// routinely names records that are not here.
//
// That a session endpoint links on the local selector while the edge keeps the
// deployment-scoped durable key it was recorded under, which is the one place
// the two identities of one record are both on screen.
//
// That an edge note reaches the page as inert text: hostile markup renders as
// characters, executes nothing, and never becomes a link destination.
//
// That the disposition inbox carries the same counts the record page's panel
// does, and that a launch with no reference graph shows no section and no chip
// rather than an error over a panel it never had.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_HTML } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Typed reference graph gate",
  covers: "issue #113's citation section on the record surfaces, in a browser",
  unverified: [
    "that a record's citations render as two labelled directions with their edge-kind chips",
    "that an endpoint this host cannot open renders as inert identified text with the reason, and carries no link",
    "that a session citation links on the local selector while the edge keeps its durable key",
    "that an edge note reaches the page as inert text and never as markup or a destination",
    "that the disposition inbox reports the same citation counts the record page shows",
    "that a launch with no reference graph renders no citation section at all",
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

// citationsRendered waits for a row rather than for a phrase. The direction
// headings are uppercased by CSS and Chrome's innerText reports them that way,
// so a text wait on "Cited by" would hang on a panel that rendered correctly;
// a row's own class is the thing the assertions below then read.
function citationsRendered(): Promise<unknown> {
  return page.waitForSelector(".references-card .citation-entry", { timeout: 15_000 });
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

test.skipIf(!chrome)("a record's citations render as two directions with edge-kind chips", async () => {
  await open("hypotheses/hyp_unverified-closures");
  await visible("Citations");

  const state = await page.evaluate(() => {
    const panel = document.querySelector(".references-card");
    const directions = Array.from(panel?.querySelectorAll(".citation-direction") ?? []);
    return {
      framing: (panel as HTMLElement | null)?.innerText ?? "",
      headings: directions.map((section) => section.querySelector("h3")?.textContent ?? ""),
      chips: directions.map((section) =>
        Array.from(section.querySelectorAll(".citation-chips .badge")).map(
          (chip) => (chip as HTMLElement).innerText,
        ),
      ),
      rows: directions.map((section) => section.querySelectorAll(".citation-entry").length),
      // The count-label is the whole degree of the record: the panel is read
      // for "how connected is this", so the two directions have to add up in
      // the header rather than only in the rows.
      total: panel?.querySelector(".count-label")?.textContent ?? "",
    };
  });

  expect(state.headings).toEqual(["Cites", "Cited by"]);
  expect(state.rows).toEqual([5, 2]);
  expect(state.total).toBe("7");
  // The chips are the direction's whole vocabulary with its counts, newest
  // kind first.
  expect(state.chips[0]).toEqual(["evidence 2", "inspired_by 1", "duplicates 1", "addresses 1"]);
  expect(state.chips[1]).toEqual(["refines 1", "evidence 1"]);
  // Epistemic framing, not decoration: a citation is an assertion somebody
  // made, and the panel says so before any row is read.
  expect(state.framing).toContain("Append-only citations recorded beside the record");
  expect(state.framing).toContain("none is evidence on its own");
  // A direction is phrased from the reader's side, so the same edge kind reads
  // correctly in both halves rather than printing the raw token twice.
  expect(state.framing).toContain("rests on");
  expect(state.framing).toContain("is refined by");
  // Attribution is on every row: who asserted the link, never just when.
  expect(state.framing).toContain("asserted by run");
  expect(state.framing).toContain("asserted by operator");

  await shoot(".references-card", "record-citations.png");
});

test.skipIf(!chrome)(
  "an endpoint this host cannot open is inert, states why, and is not a link",
  async () => {
    await open("hypotheses/hyp_unverified-closures");
    await citationsRendered();

    const targets = await page.evaluate(() =>
      Array.from(document.querySelectorAll(".references-card .citation-entry")).map((entry) => {
        const target = entry.querySelector(".citation-target");
        return {
          edge: entry.getAttribute("data-citation") ?? "",
          inert: target?.classList.contains("inert") ?? false,
          href: target?.getAttribute("href"),
          tag: target?.tagName ?? "",
          reason: entry.querySelector(".unopened-note")?.textContent ?? "",
        };
      }),
    );

    const byEdge: Record<string, (typeof targets)[number] | undefined> = Object.fromEntries(
      targets.map((target) => [target.edge, target]),
    );

    // A record this host does not hold. The row stays — that is the whole
    // point of a plaintext-eligible endpoint — and it is text, not a link.
    const absent = byEdge["rle_duplicates-absent-hyp"];
    expect(absent?.inert).toBe(true);
    expect(absent?.tag).toBe("SPAN");
    expect(absent?.href).toBeNull();
    expect(absent?.reason).toContain("holds no hypothesis with that identifier");

    // A namespace with no page in this build. The reason names the namespace,
    // so a missing page is distinguishable from a missing record.
    const unknown = byEdge["rle_addresses-complaint"];
    expect(unknown?.inert).toBe(true);
    expect(unknown?.href).toBeNull();
    expect(unknown?.reason).toContain('opens no page for the "complaint" namespace');

    // A session on another machine, and a backlink from a record that is not
    // here: the direction does not change what is knowable.
    expect(byEdge["rle_evidence-session-absent"]?.reason).toContain(
      "holds no session with that durable key",
    );
    expect(byEdge["rle_evidence-absent-obs"]?.reason).toContain(
      "holds no observation with that identifier",
    );

    // Nothing on the panel points anywhere but this app. A citation's
    // destination is derived from a namespace and an identifier, so no absolute
    // URL and no scriptable scheme can appear even though a note contains one.
    const hrefs = await page.evaluate(() =>
      Array.from(document.querySelectorAll(".references-card a")).map(
        (link) => link.getAttribute("href") ?? "",
      ),
    );
    expect(hrefs.length).toBeGreaterThan(0);
    for (const href of hrefs) {
      expect(href.startsWith("#/")).toBe(true);
    }

    await shoot(".references-card", "record-citations-inert.png");
  },
);

test.skipIf(!chrome)(
  "a session citation links on the local selector and keeps its durable key",
  async () => {
    await open("hypotheses/hyp_unverified-closures");
    await citationsRendered();

    const session = await page.evaluate(() => {
      const entry = document.querySelector("[data-citation='rle_evidence-session-a']");
      const target = entry?.querySelector(".citation-target");
      return {
        href: target?.getAttribute("href") ?? "",
        text: (target as HTMLElement | null)?.innerText ?? "",
        inert: target?.classList.contains("inert") ?? false,
      };
    });

    // The link is the selector the session page has always routed on, encoded
    // so its slash stays inside one route parameter.
    expect(session.inert).toBe(false);
    expect(session.href).toBe("#/sessions/claude%2Funverified-closures-a");
    expect(session.text).toContain("claude/unverified-closures-a");

    // Clicking it lands on the session, which is what makes the derived route a
    // route rather than a plausible string.
    await page.click("[data-citation='rle_evidence-session-a'] .citation-target");
    await page.waitForFunction(
      () => window.location.hash.startsWith("#/sessions/"),
      { timeout: 15_000 },
    );
    expect(page.url()).toContain("/sessions/claude%2Funverified-closures-a");
  },
);

test.skipIf(!chrome)("an edge note renders as inert text, never as markup", async () => {
  await open("hypotheses/hyp_hostile-content");
  await visible("Citations");

  const note = await page.evaluate(() => {
    const element = document.querySelector(".references-card .citation-note");
    return {
      text: (element as HTMLElement | null)?.innerText ?? "",
      // The hostile fixture is markup. If any of it were parsed, the note
      // would contain child elements and the document would contain the tags
      // it names; both are measured rather than assumed.
      children: element?.children.length ?? -1,
      injected: document.querySelectorAll(".references-card script, .references-card img").length,
      untrusted: element?.classList.contains("untrusted-inline") ?? false,
    };
  });

  expect(note.untrusted).toBe(true);
  expect(note.children).toBe(0);
  expect(note.injected).toBe(0);
  // The note's own characters survive: a citation says why the link exists, and
  // a surface that dropped the sentence because it contained a bracket would be
  // editing the corpus rather than neutralizing it.
  expect(note.text).toContain(HOSTILE_HTML.slice(0, 12));

  await shoot(".references-card", "record-citations-hostile-note.png");
});

test.skipIf(!chrome)("a record with no citations says so in both directions", async () => {
  await open("hypotheses/hyp_promoted-pattern");
  await visible("Citations");

  const state = await page.evaluate(() => {
    const panel = document.querySelector(".references-card");
    return {
      text: (panel as HTMLElement | null)?.innerText ?? "",
      rows: panel?.querySelectorAll(".citation-entry").length ?? -1,
      total: panel?.querySelector(".count-label")?.textContent ?? "",
    };
  });

  expect(state.rows).toBe(0);
  expect(state.total).toBe("0");
  expect(state.text).toContain("This record cites nothing.");
  expect(state.text).toContain("Nothing cites this record.");
});

test.skipIf(!chrome)("the disposition inbox reports each record's citation counts", async () => {
  await open("review?status=all");
  await visible("hyp_unverified-closures");

  const counts = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".frontier-table tbody tr"));
    return rows.map((row) => ({
      record: row.querySelector(".statement-cell .mono")?.textContent ?? "",
      citations: row.querySelector(".citation-count")?.getAttribute("data-citations") ?? "",
      text: (row.querySelector(".citation-count") as HTMLElement | null)?.innerText ?? "",
    }));
  });

  const cited = counts.find((row) => row.record === "hyp_unverified-closures");
  // The same five out and two in the record page's panel showed: one graph,
  // two surfaces, no second count.
  expect(cited?.citations).toBe("5/2");
  expect(cited?.text).toContain("5");
  expect(cited?.text).toContain("2");

  // A counted zero is stated as one rather than left blank, because on a wired
  // build "nothing cites this" is a measurement. The proposal is used rather
  // than a decided candidate because the inbox's default filter is records
  // awaiting a first decision, and a row that is not on the page cannot be
  // asserted about.
  const isolated = counts.find((row) => row.record === "prp_criteria-template");
  expect(isolated?.text).toBe("no citations");

  await shoot(".table-card", "inbox-citation-counts.png");
});

test.skipIf(!chrome)("a launch with no reference graph renders no citation section", async () => {
  const unwired = await startMock({ MOCK_UNWIRED: "frontier" });
  const bare = await browser!.newPage();
  try {
    await bare.setViewport({ width: 1440, height: 900 });
    await bare.goto(`${unwired.base}/#/review?status=all`, { waitUntil: "networkidle2" });
    await bare.reload({ waitUntil: "networkidle2" });
    const state = await bare.evaluate(() => ({
      // The section is absent, not empty: a build with no graph has one fewer
      // panel rather than a panel reporting a feature it does not have.
      panels: document.querySelectorAll(".references-card").length,
      chips: document.querySelectorAll(".citation-count").length,
      // And no error banner, which is the reason the route answers 200 with
      // `available: false` instead of refusing.
      banner: document.querySelectorAll(".error-banner").length,
      text: document.body.innerText,
    }));
    expect(state.panels).toBe(0);
    expect(state.chips).toBe(0);
    expect(state.banner).toBe(0);
    expect(state.text).not.toContain("Citations");
  } finally {
    await bare.close();
    unwired.process.kill();
  }
});
