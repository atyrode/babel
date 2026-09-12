// Browser acceptance for what this deployment says it is running, driven
// against the synthetic mock so no Go server, PostgreSQL, model, or network is
// needed.
//
// The Fleet page is gone (#235). The machine stopped being a dimension of the
// reading path — a run in flight is a run in flight, whichever host's process
// table holds it — so what "what runs where" answered is now the live strip on
// Watch, deployment-wide and ungrouped. The one place a machine is still a
// legitimate subject is a backup *of* a machine, which is Settings › Archive.
//
// What the fleet view was built for survives intact, and that is what this file
// measures.
//
// That a run this host has stopped hearing from is neither counted as running
// nor reported as ended. Nothing observed a death: a host that stopped
// announcing looks exactly like one that finished, so the rows are kept, said
// to be out of contact, and excluded from the headline that counts what is in
// flight.
//
// That the page classifies nothing itself. Which runs are in flight, which are
// out of contact, and which have been heard from recently are the server's own
// word on each row, so a client-side threshold cannot drift away from it.
//
// That nothing renders a liveness colour over an unobserved process: the live
// mark is on exactly the rows the server says were heard from.
//
// That no host appears in the reading path at all — not on the feed, not on a
// record, not on Watch — while Settings › Archive still names machines,
// because a repository's coverage is a fact about machines.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Live run presence gate",
  covers: "Watch's live strip — what is running, and what this host cannot tell — in a browser",
  unverified: [
    "that a run out of contact is neither counted as running nor reported as ended",
    "that the runs out of contact are exactly the ones the server classified that way",
    "that the live mark appears on exactly the rows the server says were heard from",
    "that every row's age is the figure the server sent, and a run that has said nothing says so",
    "that no host is named anywhere in the reading path, while Settings › Archive still names machines",
  ],
});

// SHOTS is where the run leaves its evidence. A screenshot is not an assertion
// and nothing here passes or fails on one; it exists because a layout claim in a
// pull request should be checkable by looking, and BABEL_TEST_SHOTS lets CI or an
// operator put it somewhere durable instead of the temp directory.
const SHOTS = process.env.BABEL_TEST_SHOTS ?? join(tmpdir(), "babel-fleet-shots");

// The sentence the page is not allowed to lose. It is written out here rather
// than imported so that a rewording in the component is a failure here and has
// to be made deliberately in both places: everything else on these rows is a
// figure, and this is the one clause that stops a reader from taking silence
// for death.
const DISCLAIMER = "not the same as dead";

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

interface ServedRun {
  run_id: string;
  freshness?: string;
  heartbeat_age_s?: number | null;
}

// live reads the answer the strip was drawn from, out of the page itself. Every
// assertion below compares the rendering against that answer rather than
// against a copy of the fixtures: a test carrying its own list of runs would
// keep passing after the page stopped reading the server's.
function live(): Promise<ServedRun[]> {
  return page.evaluate(async () => {
    const answer = (await fetch("/api/watch/live").then((response) => response.json())) as {
      runs?: ServedRun[];
    };
    return answer.runs ?? [];
  });
}

async function strip(): Promise<unknown> {
  const found = await page.waitForSelector(".live-card, .live-table", { timeout: 15_000 });
  // Both in-flight tables page ten rows at a time behind a "show more"
  // control, which is the pagination §8.6 allows; the tests below reason
  // about every run the server sent, so they ask for all of them first and
  // wait for each click to have added rows before asking again.
  for (let round = 0; round < 20; round += 1) {
    const selector = ".live-surface .runs-more button, .live-lost .runs-more button";
    const more = await page.$(selector);
    if (!more) break;
    const before = await page.$$eval(".live-table tbody tr", (rows) => rows.length);
    // A DOM click, because the lost group's control sits inside a folded
    // <details> and is not a pointer target until the reader opens it.
    await page.$eval(selector, (button) => (button as HTMLButtonElement).click());
    await page.waitForFunction(
      (count: number) => document.querySelectorAll(".live-table tbody tr").length > count,
      { timeout: 5_000 },
      before,
    );
  }
  return found;
}

// shoot photographs one element rather than the viewport, so a panel below the
// fold is in the file at all. The sticky top bar is hidden with `visibility`
// rather than `display` first: it re-paints over the top of a clipped capture,
// which reads as a cropped panel in the very file that exists to show the panel
// is not cropped.
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

test.skipIf(!chrome)("a run out of contact is not counted as running and not reported as ended", async () => {
  await open("watch");
  await strip();
  const runs = await live();
  const lost = runs.filter((run) => run.freshness === "lost");
  const flying = runs.filter((run) => run.freshness !== "lost");
  // The fixture is a deployment with both, which is what makes the arithmetic
  // below a measurement rather than a tautology.
  expect(lost.length).toBeGreaterThan(0);
  expect(flying.length).toBeGreaterThan(0);

  const shown = await page.evaluate(() => {
    const peel = document.querySelector(".live-lost");
    return {
      headline: document.querySelector(".page-heading h1")?.textContent ?? "",
      // The runs out of contact are peeled rather than dropped: a run still
      // spending money while its announcements go missing is exactly what an
      // operator has to be able to find.
      peeled: peel !== null,
      summary: (peel?.querySelector("summary") as HTMLElement | null)?.innerText ?? "",
      rows: peel?.querySelectorAll("tbody tr").length ?? -1,
      body: (peel as HTMLDetailsElement | null)?.innerText ?? "",
    };
  });

  // The headline counts what is in flight, and the lost runs are not in it.
  expect(shown.headline).toContain(String(flying.length));
  expect(shown.headline).not.toContain(String(runs.length));
  expect(shown.peeled).toBe(true);
  expect(shown.rows).toBe(lost.length);
  expect(shown.summary).toContain(String(lost.length));
  // And they are not reported as ended. Nothing observed them stop, and the
  // page says exactly that rather than resolving the doubt into a state.
  expect(shown.body).toContain(DISCLAIMER);

  await shoot(".live-lost", "watch-lost-contact.png");
});

test.skipIf(!chrome)("freshness is the server's word, and no row paints a colour over it", async () => {
  await open("watch");
  await strip();
  const runs = await live();
  // The out-of-contact rows are behind a fold, so they are opened first: a
  // closed <details> renders no text, and a test reading through one would be
  // measuring the rows nobody can see.
  await page.evaluate(() => {
    const peel = document.querySelector<HTMLDetailsElement>(".live-lost");
    if (peel && !peel.open) peel.querySelector<HTMLElement>("summary")?.click();
  });
  await page.waitForFunction(
    () => document.querySelector<HTMLDetailsElement>(".live-lost")?.open === true,
    { timeout: 15_000 },
  );

  const rendered = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".live-table tbody tr"));
    const cards = Array.from(document.querySelectorAll(".live-card"));
    const lost = document.querySelector(".live-lost");
    return {
      // A run is identified on a card by the receipt it links to and in a row
      // by its own id cell, which is the only place either names it.
      cards: cards.map((card) => ({
        run: (card.querySelector(".live-card-open") as HTMLAnchorElement | null)?.getAttribute("href") ?? "",
        word: (card.querySelector(".live-card-word") as HTMLElement | null)?.innerText ?? "",
        dot: card.querySelector(".live-dot") !== null,
      })),
      rows: rows.map((row) => ({
        run: (row.querySelector(".live-row-run a, .live-row-run .live-row-kind") as HTMLElement | null)?.innerText.trim() ?? "",
        word: (row.querySelector(".live-row-word") as HTMLElement | null)?.innerText ?? "",
        dot: row.querySelector(".live-dot") !== null,
        inLostGroup: lost !== null && lost.contains(row),
      })),
    };
  });

  const byID: Record<string, ServedRun> = {};
  for (const run of runs) if (run.run_id) byID[run.run_id] = run;

  // Every run the server sent is on the page once, as a card or as a row.
  const placed = [
    ...rendered.cards.map((card) => decodeURIComponent(card.run.replace("#/watch/runs/", ""))),
    ...rendered.rows.map((row) => row.run),
  ].filter((id) => id in byID);
  expect(new Set(placed).size).toBe(Object.keys(byID).length);

  // Which rows are in the out-of-contact group is the server's classification
  // and nothing else: a page that recomputed it from an age would drift away
  // from the badge the moment either threshold moved.
  for (const row of rendered.rows) {
    const served = byID[row.run];
    if (!served) continue;
    expect(row.inLostGroup).toBe(served.freshness === "lost");
    // No liveness mark on a run nothing has been heard from. A dot there
    // would be an observation nobody made.
    if (served.freshness === "lost" || served.freshness === "stale") expect(row.dot).toBe(false);
  }

  // Every row and card says how old its evidence is, from the figure the
  // server sent — and a run that has announced nothing at all says that
  // instead, because "no word yet" and "last word 0s ago" are different facts.
  for (const row of rendered.rows) {
    const served = byID[row.run];
    if (!served) continue;
    expect(row.word.length).toBeGreaterThan(0);
    if (served.heartbeat_age_s == null) {
      expect(row.word).not.toMatch(/\d/u);
    } else {
      expect(row.word).toMatch(/\d/u);
    }
  }
  for (const card of rendered.cards) {
    expect(card.word.length).toBeGreaterThan(0);
  }
});

// Two of this file's tests went with the page they measured, and neither
// guarantee is quietly lost.
//
// The presence rows were the only strings on this surface written by another
// machine's model — a remote recipe and a remote authority ref — so they
// carried the §2.7 inertness case for this page. /api/watch/live is this
// deployment's own answer about its own children and carries no such field,
// so there is nothing here to render hostile bytes into; the record surfaces
// still carry hostile fixtures and still assert it.
//
// "No shared backend configured" and "cannot see what the fleet is running"
// were the two states of a catalog read that no page in the reading path
// makes any more. The distinction still matters where a machine is the
// subject, which is Settings › Archive.

test.skipIf(!chrome)("no host is named in the reading path, and Settings › Archive still names machines", async () => {
  await open("watch");
  await strip();
  // The deployment's own host vocabulary, read from the machine that has one.
  // Asserting against the served names rather than a literal is what keeps
  // this from passing on a build that renamed its fixtures.
  const hosts = await page.evaluate(async () => {
    const answer = (await fetch("/api/fleet/hosts").then((response) => response.json())) as {
      hosts?: Array<{ host: string }>;
    };
    return (answer.hosts ?? []).map((entry) => entry.host).filter(Boolean);
  });
  expect(hosts.length).toBeGreaterThan(0);

  // Every surface a reader passes through to answer "what has Babel produced",
  // "what is it doing" and "what is this record".
  for (const route of ["", "watch", "r/hyp_unverified-closures"]) {
    await open(route);
    await page.waitForSelector(".page", { timeout: 15_000 });
    const state = await page.evaluate(() => ({
      text: document.body.innerText,
      // No host tabs, no host chips, no scope switch: the machine is not a
      // dimension the reader filters by.
      controls: Array.from(document.querySelectorAll("button, select, [role='tab']")).map(
        (control) => (control as HTMLElement).innerText,
      ),
    }));
    for (const host of hosts) expect(state.text).not.toContain(host);
    for (const label of state.controls) {
      expect(label.toLowerCase()).not.toContain("this machine");
      expect(label.toLowerCase()).not.toContain("every machine");
    }
  }

  // And the word is not gone from the app: a backup is a backup of a machine,
  // so the archive names which machines have pushed into the repository.
  await open("settings?section=archive");
  await page.waitForFunction(() => document.body.innerText.includes("Snapshots by host"), {
    timeout: 15_000,
  });
  const archive = await page.evaluate(() => document.body.innerText);
  expect(hosts.some((host) => archive.includes(host))).toBe(true);
});
