// Browser acceptance for issue #118's Fleet view, driven against the synthetic
// mock so no Go server, PostgreSQL, model, or network is needed.
//
// What only a browser can prove is here, and all of it is about what a reader
// is allowed to believe about a row.
//
// That a run which has gone quiet says so in words. This is the gate the whole
// page exists for: presence answers "what is running where", and the honest
// answer past the staleness threshold is that this host cannot tell. A rendering
// that resolved that into a colour would be asserting something about a process
// on another machine that nothing here observed, so the disclaimer has to be
// text, it has to be on exactly the doubtful rows, and it has to be absent from
// the rows that have nothing to disclaim.
//
// That the page classifies nothing itself: the badge beside each age is the
// server's own word, and the thresholds the legend quotes are the server's own
// numbers, so a future client-side constant cannot drift away from the badge.
//
// That this machine's own runs appear among the others, marked but not
// privileged — the empty state promises exactly that, and an operator who could
// not see his own host would read an idle fleet as a broken page.
//
// That hostile content from another machine's model reaches the recipe and
// authority cells as characters: presence rows are written by hosts this one
// does not control, which makes them the least trusted strings on the page.
//
// That a deployment with no shared catalog, and a configured one whose catalog
// could not be read, render as two different stated facts rather than as one
// generic failure or an error banner over a page that worked.
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
  gate: "Fleet presence gate",
  covers: "issue #118's Fleet view — what runs where, and what this host cannot tell — in a browser",
  unverified: [
    "that a run past the staleness threshold renders the words 'running or dead, this host cannot tell'",
    "that the disclaimer appears on exactly the doubtful rows and on no fresh or finished row",
    "that no row renders a liveness colour, and the freshness badge is the server's own classification",
    "that this machine's own runs appear among the fleet's, marked as this host",
    "that a remote recipe and authority carrying hostile markup render as inert characters",
    "that local mode and an unreachable catalog render as two different stated facts, not as an error",
  ],
});

// SHOTS is where the run leaves its evidence. A screenshot is not an assertion
// and nothing here passes or fails on one; it exists because a layout claim in a
// pull request should be checkable by looking, and BABEL_TEST_SHOTS lets CI or an
// operator put it somewhere durable instead of the temp directory.
const SHOTS = process.env.BABEL_TEST_SHOTS ?? join(tmpdir(), "babel-fleet-shots");

// The sentence the page is not allowed to lose. It is written out here rather
// than imported so that a rewording in the component is a failure here and has
// to be made deliberately in both places.
const DISCLAIMER = "running or dead, this host cannot tell";

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

// The rows are waited on by selector rather than by phrase. The page's own
// headings are uppercased by CSS and Chrome reports them that way, so a text
// wait would hang on a table that rendered perfectly.
function rowsRendered(): Promise<unknown> {
  return page.waitForSelector(".presence-table .presence-row", { timeout: 15_000 });
}

// readRows lifts the whole table out of the page as data, so the assertions
// below read columns rather than search the page's text for substrings. A
// substring search would pass on a page that printed the disclaimer once in a
// legend and never on a row.
function readRows(): Promise<
  Array<{ id: string; freshness: string; state: string; text: string; doubt: string; host: string }>
> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".presence-host-card")).flatMap((card) => {
      const host = card.querySelector<HTMLElement>("h2")?.innerText ?? "";
      return Array.from(card.querySelectorAll<HTMLElement>("tbody tr")).map((row) => ({
        id: row.className,
        freshness: row.querySelector<HTMLElement>(".presence-freshness .badge")?.innerText ?? "",
        state:
          row.querySelectorAll<HTMLElement>("td")[4]?.querySelector<HTMLElement>(".badge")?.innerText ?? "",
        text: row.innerText,
        doubt: row.querySelector<HTMLElement>(".presence-doubt")?.innerText ?? "",
        host,
      }));
    }),
  );
}

const STICKY_HEADER_CLEARANCE = 140;

async function shoot(selector: string, name: string): Promise<void> {
  const element = await page.waitForSelector(selector, { timeout: 15_000 });
  if (!element) throw new Error(`no element to photograph: ${selector}`);
  await page.evaluate(
    (target: string, clearance: number) => {
      const bar = document.querySelector<HTMLElement>(".topbar");
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
    const bar = document.querySelector<HTMLElement>(".topbar");
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

test.skipIf(!chrome)("a quiet run says in words that this host cannot tell", async () => {
  await open("fleet");
  await rowsRendered();
  const rows = await readRows();

  // The disclaimer is on exactly the doubtful rows, and it is the same sentence
  // on each. Counting is the assertion: one stray disclaimer on a fresh row
  // would teach an operator to distrust a heartbeat seconds old, and a missing
  // one on a lost row is the failure this page exists to prevent.
  const doubtful = rows.filter((row) => row.freshness === "stale" || row.freshness === "lost");
  const disclaiming = rows.filter((row) => row.doubt !== "");
  expect(doubtful.length).toBe(2);
  expect(disclaiming.map((row) => row.freshness).sort()).toEqual(["lost", "stale"]);
  for (const row of disclaiming) expect(row.doubt).toBe(DISCLAIMER);

  // And no fresh or finished row carries it anywhere in its text, not merely
  // outside the disclaimer element.
  for (const row of rows.filter((r) => r.freshness === "fresh" || r.freshness === "finished")) {
    expect(row.text).not.toContain(DISCLAIMER);
  }

  await shoot(".fleet-presence-page", "fleet-presence.png");
});

test.skipIf(!chrome)("the classification and its thresholds are the server's, not the page's", async () => {
  await open("fleet");
  await rowsRendered();

  const state = await page.evaluate(() => ({
    legend: document.querySelector<HTMLElement>(".presence-legend")?.innerText ?? "",
    badges: Array.from(document.querySelectorAll<HTMLElement>(".presence-freshness .badge")).map(
      (badge) => badge.innerText,
    ),
    // A liveness dot is the thing that must never appear. The dashboard's own
    // vocabulary for "this is happening now" is .pulse-dot, and its presence
    // on this page would be an observation nobody made.
    pulses: document.querySelectorAll(".fleet-presence-page .pulse-dot").length,
  }));

  // Every row's badge is one of internal/presence's four words and nothing else.
  expect(state.badges.length).toBeGreaterThan(0);
  for (const badge of state.badges) {
    expect(["fresh", "stale", "lost", "finished"]).toContain(badge);
  }
  expect(state.pulses).toBe(0);
  // The legend quotes the thresholds the server classified by. The mock sends
  // 120s and 900s, so a page carrying its own copy of "two minutes" would have
  // to be edited to keep this passing — which is the point.
  expect(state.legend).toContain("2m");
  expect(state.legend).toContain("15m");
  expect(state.legend).toContain("A heartbeat is evidence, not a pulse");
});

test.skipIf(!chrome)("this machine's own runs appear among the fleet's, marked as this host", async () => {
  await open("fleet");
  await rowsRendered();

  const hosts = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".presence-host-card")).map((card) => ({
      heading: card.querySelector<HTMLElement>("h2")?.innerText ?? "",
      local: card.classList.contains("local-host-card"),
      rows: card.querySelectorAll("tbody tr").length,
    })),
  );

  // Two machines, and exactly one of them is this one.
  expect(hosts.length).toBe(2);
  expect(hosts.filter((host) => host.local).length).toBe(1);
  const local = hosts.find((host) => host.local);
  expect(local?.heading).toContain("demo-laptop");
  expect(local?.heading.toLowerCase()).toContain("this host");
  // A conductor cycle and the run inside it announce separately under one run
  // id, so this machine shows more rows than it has runs.
  expect(local?.rows).toBeGreaterThan(1);
});

test.skipIf(!chrome)("a remote recipe and authority render as inert characters", async () => {
  await open("fleet");
  await rowsRendered();

  const hostile = await page.evaluate((markup: string) => {
    const surface = document.querySelector<HTMLElement>(".fleet-presence-page");
    return {
      // The exact bytes reached the page as text.
      text: surface?.innerText.includes(markup) ?? false,
      // And became no element and no destination.
      images: surface?.querySelectorAll("img").length ?? 0,
      scripts: surface?.querySelectorAll("script").length ?? 0,
      pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    };
  }, HOSTILE_HTML);

  expect(hostile.text).toBe(true);
  expect(hostile.images).toBe(0);
  expect(hostile.scripts).toBe(0);
  expect(hostile.pwned).toBe("undefined");
});

test.skipIf(!chrome)("local mode and an unreachable catalog are two different stated facts", async () => {
  const local = await startMock({ MOCK_FLEET: "unconfigured" });
  const degraded = await startMock({ MOCK_FLEET: "degraded" });
  const said: string[] = [];
  try {
    const cases: Array<[MockServer, string]> = [
      [local, "no shared backend configured"],
      [degraded, "cannot see what the fleet is running"],
    ];
    for (const [server, expected] of cases) {
      await page.goto(`${server.base}/#/fleet`, { waitUntil: "networkidle2" });
      await page.reload({ waitUntil: "networkidle2" });
      await page.waitForSelector(".presence-notice", { timeout: 15_000 });
      const state = await page.evaluate(() => ({
        notice: document.querySelector<HTMLElement>(".presence-notice")?.innerText ?? "",
        // Neither case is an error: an error banner over a machine that is
        // working exactly as configured is the falsehood this asserts against.
        banner: document.querySelectorAll(".error-banner").length,
        errorState: document.querySelectorAll(".error-state").length,
        rows: document.querySelectorAll(".presence-row").length,
      }));
      expect(state.notice).toContain(expected);
      expect(state.banner).toBe(0);
      expect(state.errorState).toBe(0);
      expect(state.rows).toBe(0);
      said.push(state.notice);
    }

    // The two sentences differ, which is the whole reason the envelope carries
    // `configured` beside `available`: one is fixed by configuring shared mode
    // and the other by looking at a catalog that is already configured.
    expect(said[0]).not.toBe(said[1]);
  } finally {
    local.process.kill();
    degraded.process.kill();
  }
});
