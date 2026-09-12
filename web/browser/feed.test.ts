// Browser acceptance for the front page (SPEC.md §8.7), driven against the
// synthetic mock so no Go server, archive, or network is needed (§10's fixture
// rule).
//
// Every record Babel has produced is a post, there is one list, and the kinds
// are a filter on it rather than five places to go. What only a browser can
// prove about that is here: that the chips narrow the one list and land in the
// URL a reader can share and walk back out of; that the five sorts visibly
// disagree, and that the period control belongs to the two of them that are
// about a period; that j and k move and ↵ opens what is focused; that a topic
// is the same feed narrowed to one community; and that a comment written under
// a post appears in its thread.
//
// Nothing in the client computes an order — the five sorts are
// internal/web/feed.go's and are tested there — so each sort is checked twice
// over: that the page renders the order the server sent, and that the order has
// the property the named sort promises, read from the same wire the page read.
// A second implementation of "hot" in a test would be a second thing to
// disagree with the first.
//
// Two things this file deliberately does not assert, each for a stated reason:
//
//   - The comment count on a feed row after a comment is posted. The feed is a
//     projection with a stated freshness, rebuilt at most once a minute
//     (internal/web/feed.go's feedFreshness), so a row's count is not expected
//     to move on the next read and the only way to watch it move is to wait out
//     a real minute. The thread's own count is live and is asserted where it is.
//
//   - What a row's own controls record. The operator's arrows are being
//     replaced by the rulings §8.5 keeps in a queue today, with the score
//     becoming Babel's reviewers' alone, so anything asserted about them now
//     would be asserted about a control on its way out.
//
// The hostile-fixture case for this surface — a post title and a reviewer's
// comment carrying markup a model wrote — is in phaseb.test.ts, which owns
// "hostile fixtures render inert everywhere they appear" and now walks the feed
// and the thread as two of those places. A second copy here would be a second
// thing to forget to update.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Feed web gate",
  covers: "§8.7's front page -- the feed, its sorts, its chips and a topic -- in a browser",
  unverified: [
    "that the front page is one list of every kind, that a kind chip narrows it and lands in the URL, and that Back restores the filter it replaced",
    "that the five sorts produce visibly different orders and that the period control appears for exactly the two sorts that read it",
    "that j and k move and ↵ opens what the focused row points at",
    "that a topic page is the feed narrowed to one community with the rail marking it, and that an unknown topic says so instead of reading as day one",
    "that a comment written under a post appears at the head of its thread, and that an empty one cannot be posted",
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

// The rail is a rail above 1024px and a fold below it, and everything here is
// read at the width an operator reads the feed at.
const WIDE = { width: 1440, height: 900 };

// How many rows one read of the feed brings: §8.6's density contract expressed
// in rows, and the page's own PAGE_SIZE.
const PAGE_SIZE = 25;

// The kinds, with the words the chips carry. They are written out rather than
// imported because what is checked is what a reader sees: a label imported from
// the source it renders would agree with itself whatever it said.
const KINDS: Array<[string, string]> = [
  ["proposal", "Proposal"],
  ["finding", "Finding"],
  ["hypothesis", "Hypothesis"],
  ["observation", "Observation"],
  ["question", "Question"],
];

// The window `rising` counts activity over (§8.7, internal/web/feed.go).
const RISING_WINDOW_MS = 12 * 60 * 60 * 1000;

interface Row {
  id: string;
  kind: string;
  created: string;
  score: string;
}

interface ServedPost {
  id: string;
  score: number;
  support: number;
  oppose: number;
  created_at: string;
  last_activity_at: string;
}

async function open(route: string): Promise<void> {
  // A hash-only change is a same-document navigation, so a full reload keeps
  // every test starting from a freshly booted application.
  await page.goto(`${mock?.base}/#/${route}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
}

// listed waits for the feed to have painted rows and reports what is on
// screen, in the order it is on screen: one entry per row, with the facts a
// reader can see on it.
async function listed(): Promise<Row[]> {
  await page.waitForSelector("ol.feed-list > li.feed-row", { timeout: 15_000 });
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("ol.feed-list > li.feed-row")).map((row) => ({
      id: row.getAttribute("data-post") ?? "",
      kind: row.querySelector(".badge")?.textContent ?? "",
      created: row.querySelector("time.feed-age")?.getAttribute("datetime") ?? "",
      score: row.querySelector(".vote-score")?.textContent ?? "",
    })));
}

// served reads the same feed the page read, from the page itself, for the facts
// a row does not print: what a post's votes are made of, and when something
// last happened to it. Comparing the rendering against the server's own answer
// rather than against a copy of the fixture is what stops this file from
// passing after the page stops reading the server.
function served(query: string): Promise<ServedPost[]> {
  return page.evaluate(async (search: string) => {
    const answer = await fetch(`/api/feed?limit=100&${search}`).then((response) => response.json());
    return (answer.posts ?? []) as Array<{
      id: string;
      score: number;
      support: number;
      oppose: number;
      created_at: string;
      last_activity_at: string;
    }>;
  }, query);
}

// counted is the total the heading states, which is the size of the eligible
// set rather than of the page: the control under the list exists because the
// two differ.
function counted(): Promise<number> {
  return page.evaluate(() =>
    Number((document.querySelector(".feed-count")?.textContent ?? "").replace(/[^0-9]/gu, "")));
}

// order presses one ordering, waits for the rows on screen to be the ones the
// server answered that ordering with, and reports them.
//
// The wait is on that agreement rather than on a duration because the previous
// rows stay up while the next read is in flight — a sleep here would be a race
// — and because the agreement is itself the client's whole job: a page that
// re-ranked what it was sent would never satisfy it.
async function order(sort: string, period?: string): Promise<Row[]> {
  await page.click(`[data-sort='${sort}']`);
  if (period) {
    await page.waitForSelector("[role='group'][aria-label='Period']", { timeout: 15_000 });
    await page.click(`[data-window='${period}']`);
  }
  const query = `sort=${sort}${period ? `&t=${period}` : ""}`;
  const answered = (await served(query)).slice(0, PAGE_SIZE).map((post) => post.id).join(",");
  await page.waitForFunction(
    (want: string) =>
      Array.from(document.querySelectorAll("ol.feed-list > li.feed-row"))
        .map((row) => row.getAttribute("data-post"))
        .join(",") === want,
    { timeout: 15_000 },
    answered,
  );
  return listed();
}

beforeAll(async () => {
  if (!chrome) return;
  // The mock serves web/dist, so the bundle under test is built from the
  // sources in this checkout rather than whatever was last committed.
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);
  mock = await startMock({});
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
  mock?.process.kill();
});

test.skipIf(!chrome)("the front page is one list, and a kind is a filter on it", async () => {
  await open("");
  const everything = await counted();
  expect(everything).toBeGreaterThan(0);
  // Nothing is selected and the chip that says so is pressed: "Everything" is a
  // state of the filter rather than a sixth kind.
  expect(await page.$eval(".feed-kinds button", (chip) => chip.getAttribute("aria-pressed")))
    .toBe("true");

  // Each kind narrows the one list to itself, says so in the URL, and holds
  // some of the corpus. The totals then have to add up to the unfiltered one,
  // which is what makes Everything every kind and nothing else — and unlike a
  // count of the rows on screen it does not depend on what the ranking put on
  // the first page.
  let accounted = 0;
  for (const [kind, label] of KINDS) {
    await page.click(`[data-chip='kind-${kind}']`);
    await page.waitForFunction(
      (want: string) => window.location.hash.includes(`kind=${want}`),
      { timeout: 15_000 },
      kind,
    );
    // The chip's own pressed state, and not only the URL: the control says
    // what is in force, and waiting on it is also what keeps the next press
    // from being computed against the filter this one replaced.
    await page.waitForSelector(`[data-chip='kind-${kind}'][aria-pressed='true']`, { timeout: 15_000 });
    await page.waitForFunction(
      (want: string) => {
        const rows = Array.from(document.querySelectorAll("ol.feed-list > li.feed-row"));
        return rows.length > 0
          && rows.every((row) => row.querySelector(".badge")?.textContent === want);
      },
      { timeout: 15_000 },
      label,
    );
    const narrowed = await counted();
    expect(`${kind}:${narrowed > 0 && narrowed < everything}`).toBe(`${kind}:true`);
    accounted += narrowed;
    // Off again, so the next kind is read on its own: the chips are a set, and
    // pressing a second one widens rather than replaces.
    await page.click(`[data-chip='kind-${kind}']`);
    await page.waitForSelector(`[data-chip='kind-${kind}'][aria-pressed='false']`, { timeout: 15_000 });
    await page.waitForFunction(() => !window.location.hash.includes("kind="), { timeout: 15_000 });
  }
  expect(accounted).toBe(everything);

  // The filter is a place, so Back goes back to it. A reader who narrows to
  // findings, clears the filter and presses Back is asking for his findings
  // again, not for whatever he was reading before the feed.
  await page.click("[data-chip='kind-finding']");
  await page.waitForSelector("[data-chip='kind-finding'][aria-pressed='true']", { timeout: 15_000 });
  await page.waitForFunction(() => window.location.hash.includes("kind=finding"), { timeout: 15_000 });
  await page.click(".feed-kinds button");
  await page.waitForSelector("[data-chip='kind-finding'][aria-pressed='false']", { timeout: 15_000 });
  await page.waitForFunction(() => !window.location.hash.includes("kind="), { timeout: 15_000 });
  await page.goBack();
  await page.waitForFunction(() => window.location.hash.includes("kind=finding"), { timeout: 15_000 });
  const restored = await listed();
  expect(restored.length).toBeGreaterThan(0);
  for (const row of restored) expect(row.kind).toBe("Finding");
});

test.skipIf(!chrome)("the five sorts order the same list differently", async () => {
  await open("");
  const hot = await listed();
  expect(hot.length).toBeGreaterThan(1);

  // Top, over the whole corpus: the highest score in the eligible set leads,
  // not merely the highest of the rows this read happened to bring back.
  const top = await order("top", "all");
  expect(await page.evaluate(() => window.location.hash)).toContain("sort=top");
  expect(await page.evaluate(() => window.location.hash)).toContain("t=all");
  const scored = await served("sort=top&t=all");
  expect(scored.length).toBeGreaterThan(top.length);
  const best = Math.max(...scored.map((post) => post.score));
  expect(scored.find((post) => post.id === top[0].id)?.score).toBe(best);
  expect(top.map((row) => row.id)).not.toEqual(hot.map((row) => row.id));

  // New: newest first, the whole way down. A list that is only right about its
  // first row is not sorted.
  const fresh = await order("new");
  const times = fresh.map((row) => Date.parse(row.created));
  expect(times.every((at) => Number.isFinite(at))).toBe(true);
  for (let index = 1; index < times.length; index += 1) {
    expect(`${index}:${times[index - 1] >= times[index]}`).toBe(`${index}:true`);
  }

  // Controversial needs both support and opposition (§8.7), so a record nine
  // reviewers agreed with is not slightly controversial — it is agreed on, and
  // every genuinely split record sorts above it.
  //
  // The boundary is read over the whole ordering rather than over the page:
  // the split records outnumber one page of rows, so a one-sided one is simply
  // not on the first screen, which is the ordering being right rather than a
  // reason not to check it. What the page is checked for is that it renders
  // that ordering — order() waited for exactly that — and that the row it
  // leads with is a disputed one.
  const split = await order("controversial", "all");
  const ranked = await served("sort=controversial&t=all");
  const disputed = ranked.map((post) => post.support > 0 && post.oppose > 0);
  expect(disputed).toContain(true);
  expect(disputed).toContain(false);
  expect(disputed.lastIndexOf(true)).toBeLessThan(disputed.indexOf(false));
  const leading = ranked.find((post) => post.id === split[0].id);
  expect(`${split[0].id}:${(leading?.support ?? 0) > 0 && (leading?.oppose ?? 0) > 0}`)
    .toBe(`${split[0].id}:true`);

  // Rising is recent activity against age, so a post nothing has happened to is
  // absent rather than ranked last behind the ones something has. Both halves
  // are asserted: what is shown has been touched, and the corpus holds
  // something untouched that is therefore not there.
  const rising = await order("rising");
  const whole = await served("sort=new");
  const now = Date.now();
  const active = new Set(
    whole
      .filter((post) => now - Date.parse(post.last_activity_at) < RISING_WINDOW_MS)
      .map((post) => post.id),
  );
  const silent = whole.filter((post) => !active.has(post.id)).map((post) => post.id);
  expect(rising.length).toBeGreaterThan(0);
  expect(silent.length).toBeGreaterThan(0);
  for (const row of rising) expect(`${row.id}:${active.has(row.id)}`).toBe(`${row.id}:true`);
  for (const id of silent) expect(`${id}:${rising.some((row) => row.id === id)}`).toBe(`${id}:false`);

  // The period is a control for the two sorts that read it and is absent for
  // the three that do not: a period selector beside "new" is a control that
  // does nothing and does not say so.
  const windowed: Array<[string, boolean]> = [
    ["hot", false], ["new", false], ["rising", false], ["top", true], ["controversial", true],
  ];
  for (const [sort, offered] of windowed) {
    await page.click(`[data-sort='${sort}']`);
    await page.waitForSelector(`[data-sort='${sort}'][aria-pressed='true']`, { timeout: 15_000 });
    const period = await page.$("[role='group'][aria-label='Period']");
    expect(`${sort}:${period !== null}`).toBe(`${sort}:${offered}`);
  }
});

// The operator's own arrows on a feed row are not measured here, and that is a
// deliberate gap rather than an oversight: the surface they belong to is being
// reworked — the rulings move onto the rows and the score becomes Babel's own,
// read-only — so a test written against the arrows now would be written against
// a control that is on its way out. What a row's controls record, and where it
// is read back, is asserted once that surface settles.

test.skipIf(!chrome)("j and k move and ↵ opens the focused row", async () => {
  await open("?kind=observation");
  const rows = await listed();
  expect(rows.length).toBeGreaterThan(2);
  const first = `li.feed-row[data-post='${rows[0].id}']`;

  // The ring is a real DOM focus, so the browser scrolls the row into view and
  // a screen reader follows it: a highlight drawn with a class would move the
  // eye and nothing else.
  await page.keyboard.press("j");
  await page.waitForFunction(
    (want: string) => document.activeElement?.getAttribute("data-post") === want,
    { timeout: 15_000 },
    rows[0].id,
  );
  await page.keyboard.press("j");
  await page.waitForFunction(
    (want: string) => document.activeElement?.getAttribute("data-post") === want,
    { timeout: 15_000 },
    rows[1].id,
  );
  await page.keyboard.press("k");
  await page.waitForFunction(
    (want: string) => document.activeElement?.getAttribute("data-post") === want,
    { timeout: 15_000 },
    rows[0].id,
  );

  // ↵ opens exactly what the focused row points at. The feed carries the
  // questions Babel asks beside the records it produced and each is read where
  // it is answered, so the destination is the row's own href and not "a record".
  const href = await page.$eval(`${first} a.feed-claim`, (claim) => claim.getAttribute("href") ?? "");
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    (want: string) => window.location.hash === want,
    { timeout: 15_000 },
    href,
  );
});

test.skipIf(!chrome)("a topic is the same feed narrowed to one community", async () => {
  await open("t/atlas");
  const rows = await listed();
  expect(rows.length).toBeGreaterThan(0);
  // Every row carries the topic it was filed under, so the narrowing is visible
  // on the rows rather than only in the heading above them.
  const filed = await page.evaluate(() =>
    Array.from(document.querySelectorAll("ol.feed-list > li.feed-row")).map((row) =>
      Array.from(row.querySelectorAll("a.feed-topic")).map((link) => link.textContent ?? "")));
  for (const topics of filed) expect(topics).toContain("t/atlas");
  expect(await page.$eval("h1", (heading) => heading.textContent)).toBe("t/atlas");

  // The rail says where the reader is, and says it with aria-current: the mark
  // is part of the list rather than a colour a screen reader cannot read.
  expect(await page.$eval(".feed-rail [aria-current='page']",
    (link) => (link as HTMLElement).innerText)).toContain("t/atlas");

  // A topic nothing is filed under is a statement about the filter, not about
  // the deployment: day one's sentence here would tell a reader Babel has
  // produced nothing while the whole corpus sits one click away.
  await open("t/nothing-cites-this");
  await page.waitForSelector(".empty-state", { timeout: 15_000 });
  expect(await page.$eval("h1", (heading) => heading.textContent)).toBe("t/nothing-cites-this");
  const empty = await page.evaluate(() => document.body.innerText);
  expect(empty).toContain("Nothing matches this view");
  expect(empty).not.toContain("Babel has not posted anything yet");
});

test.skipIf(!chrome)("a comment lands at the head of the thread it was written in", async () => {
  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-thread-list .record-comment", { timeout: 15_000 });
  const heading = () => page.$eval(".record-thread h2", (title) => title.textContent ?? "");
  const before = Number((await heading()).replace(/[^0-9]/gu, ""));
  expect(before).toBeGreaterThan(0);

  // An empty box is refused where the operator is writing rather than by the
  // store: the control that would record nothing cannot be pressed, and
  // whitespace is nothing.
  await page.type(".record-comment-form textarea", "   ");
  expect(await page.$eval(".record-comment-form button[type='submit']",
    (button) => (button as HTMLButtonElement).disabled)).toBe(true);
  expect(Number((await heading()).replace(/[^0-9]/gu, ""))).toBe(before);

  // His own words, put where the thread's own order puts them: newest first,
  // which is the top.
  const written = `Synthetic operator comment ${Date.now()}`;
  await page.type(".record-comment-form textarea", written);
  await page.click(".record-comment-form button[type='submit']");
  await page.waitForFunction(
    (needle: string) =>
      (document.querySelector(".record-thread-list > li")?.textContent ?? "").includes(needle),
    { timeout: 15_000 },
    written,
  );
  expect(Number((await heading()).replace(/[^0-9]/gu, ""))).toBe(before + 1);

  // And it is a record rather than a rendering: a reload reads it back out of
  // the store, in the same place.
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForSelector(".record-thread-list .record-comment", { timeout: 15_000 });
  expect(await page.$eval(".record-thread-list > li", (entry) => entry.textContent ?? ""))
    .toContain(written);
});
