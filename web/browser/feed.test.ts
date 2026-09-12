// Browser acceptance for the front page (SPEC.md §8.6-8.7), driven against the
// synthetic mock so no Go server, archive, or network is needed (§10's fixture
// rule).
//
// There is one list. Every record Babel has produced is a post, the kinds are
// a filter on it, and what used to be a second surface — the mod queue — is
// the `needs me` filter and the `next` ordering over the same rows. What only
// a browser can prove about that is here: that the front page arrives showing
// what needs the operator and that one gesture widens it to everything; that
// the chips narrow the one list and land in the URL a reader can share and
// walk back out of; that the six sorts visibly disagree, that `next` puts the
// more urgent record above the calmer one, and that the period control belongs
// to the two sorts that are about a period; that j and k move and ↵ opens what
// is focused; that a ruling from a row is confirmed before it is recorded and
// that the row then says what was done; that a question asked from a row lands
// in the record's thread as one; that a topic is the same feed narrowed to one
// community; and that a comment written under a post appears in its thread.
//
// Nothing in the client computes an order — the sorts are
// internal/web/feed.go's and are tested there — so each computed sort is
// checked twice over: that the page renders the order the server sent, and
// that the order has the property the named sort promises, read from the same
// wire the page read. A second implementation of "hot" in a test would be a
// second thing to disagree with the first. `next` is read as the property it
// promises — urgency before kind before age — rather than as a formula,
// because it is a grouping and has none.
//
// One thing this file deliberately does not assert, for a stated reason: the
// comment count on a feed row after a comment is posted. The feed is a
// projection with a stated freshness, rebuilt at most once a minute
// (internal/web/feed.go's feedFreshness), so a row's count is not expected to
// move on the next read and the only way to watch it move is to wait out a
// real minute. What the row does show immediately is the question it just
// recorded, which is asserted; the thread's own count is live and is asserted
// where it is.
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
  covers:
    "§8.7's front page -- one list, its needs-me filter, its sorts, its chips, its rulings and a topic -- in a browser",
  unverified: [
    "that the front page arrives narrowed to what needs the operator, in next order, and that one gesture widens it to everything hot",
    "that the front page is one list of every kind, that a kind chip narrows it and lands in the URL, and that Back restores the filter it replaced",
    "that the computed sorts produce visibly different orders, that next puts the more urgent record above the calmer one, and that the period control appears for exactly the two sorts that read it",
    "that j and k move and ↵ opens what the focused row points at",
    "that a ruling from a row is confirmed before it is recorded, is recorded once, and leaves the row saying what was done",
    "that y and n open the confirmation for the focused row rather than ruling on it",
    "that a question asked from a row is recorded as one and reads as \"you asked\" in the record's own thread",
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
const PAGE_SIZE = 15;

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
  // Whether the row says it is waiting on the operator, and the five words it
  // gives for why. Both are read off the row rather than off the wire,
  // because the row is what the reader acts on.
  awaiting: boolean;
  why: string;
  // What the row offers to do about it, in the order it offers them.
  acts: string[];
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
      score: row.querySelector(".feed-score")?.textContent ?? "",
      awaiting: row.hasAttribute("data-awaiting"),
      why: row.querySelector(".feed-why")?.textContent ?? "",
      acts: Array.from(row.querySelectorAll("[data-ruling]")).map(
        (button) => button.getAttribute("data-ruling") ?? "",
      ),
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
  // Read with the operator's own filter off, because what this test is about
  // is the one list: the totals have to add up to every kind, and "what needs
  // me" is a filter over that list rather than the list itself.
  await open("?needs=all");
  const everything = await counted();
  expect(everything).toBeGreaterThan(0);
  // Nothing is selected and the chip that says so is pressed: "Everything" is a
  // state of the filter rather than a sixth kind.
  expect(await page.$eval("[data-chip='all']", (chip) => chip.getAttribute("aria-pressed")))
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
  await page.click("[data-chip='all']");
  await page.waitForSelector("[data-chip='kind-finding'][aria-pressed='false']", { timeout: 15_000 });
  await page.waitForFunction(() => !window.location.hash.includes("kind="), { timeout: 15_000 });
  await page.goBack();
  await page.waitForFunction(() => window.location.hash.includes("kind=finding"), { timeout: 15_000 });
  const restored = await listed();
  expect(restored.length).toBeGreaterThan(0);
  for (const row of restored) expect(row.kind).toBe("Finding");
});

test.skipIf(!chrome)("the computed sorts order the same list differently", async () => {
  await open("?needs=all");
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
  // the four that do not: a period selector beside "new" is a control that
  // does nothing and does not say so.
  const windowed: Array<[string, boolean]> = [
    ["next", false], ["hot", false], ["new", false], ["rising", false],
    ["top", true], ["controversial", true],
  ];
  for (const [sort, offered] of windowed) {
    await page.click(`[data-sort='${sort}']`);
    await page.waitForSelector(`[data-sort='${sort}'][aria-pressed='true']`, { timeout: 15_000 });
    const period = await page.$("[role='group'][aria-label='Period']");
    expect(`${sort}:${period !== null}`).toBe(`${sort}:${offered}`);
  }
});

test.skipIf(!chrome)("the front page arrives showing what needs the operator", async () => {
  await open("");

  // Two controls are in force without the reader having pressed anything: the
  // filter that says "what needs me" and the ordering that says which of it
  // is next. That is the mod queue, and it is this list.
  await page.waitForSelector("[data-chip='needs-me'][aria-pressed='true']", { timeout: 15_000 });
  await page.waitForSelector("[data-sort='next'][aria-pressed='true']", { timeout: 15_000 });
  const waiting = await listed();
  expect(waiting.length).toBeGreaterThan(1);
  const mine = await counted();

  // Every row on it is waiting on him, says why in a few words, and offers the
  // acts §8.7 gives a row. A question is answered where answers are written
  // and carries no disposition, so it offers the one act that is not a ruling.
  for (const row of waiting) {
    expect(`${row.id}:${row.awaiting}`).toBe(`${row.id}:true`);
    expect(`${row.id}:${row.why.length > 0}`).toBe(`${row.id}:true`);
    // Five words at most (§8.7). The dot between the reason and the age is
    // punctuation rather than one of them.
    const words = row.why.split(/\s+/u).filter((word) => word !== "·");
    expect(`${row.id}:${words.length <= 5}`).toBe(`${row.id}:true`);
    // A question is answered where answers are written — it is not a record
    // in the corpus, carries no disposition and has no thread of its own — so
    // its row offers no acts and its claim opens the page that takes the
    // answer. Every record that is waiting offers the four rulings and the
    // question.
    const expected = row.kind === "Question"
      ? []
      : ["accept", "reject", "defer", "refine", "ask"];
    expect(`${row.id}:${row.acts.join(",")}`).toBe(`${row.id}:${expected.join(",")}`);
  }

  // One gesture widens it to everything, and the ordering follows: a reader
  // who is no longer triaging is reading a feed, and the front page of a feed
  // is hot. Both land in the URL, because both are places he shares and walks
  // back out of.
  await page.click("[data-chip='needs-me']");
  await page.waitForSelector("[data-chip='needs-me'][aria-pressed='false']", { timeout: 15_000 });
  await page.waitForSelector("[data-sort='hot'][aria-pressed='true']", { timeout: 15_000 });
  expect(await page.evaluate(() => window.location.hash)).toContain("needs=all");
  await page.waitForFunction(
    (narrower: number) =>
      Number((document.querySelector(".feed-count")?.textContent ?? "").replace(/[^0-9]/gu, "")) >
      narrower,
    { timeout: 15_000 },
    mine,
  );

  // And the wider list holds rows nobody is waiting on, which is what makes
  // the filter a filter: a row that is not waiting offers no acts and gives no
  // reason for being where it is.
  const all = await listed();
  const calm = all.filter((row) => !row.awaiting);
  expect(calm.length).toBeGreaterThan(0);
  for (const row of calm) {
    expect(`${row.id}:${row.why}`).toBe(`${row.id}:`);
    expect(`${row.id}:${row.acts.length}`).toBe(`${row.id}:0`);
  }

  // Back restores the filter it replaced, exactly as a kind chip does.
  await page.goBack();
  await page.waitForSelector("[data-chip='needs-me'][aria-pressed='true']", { timeout: 15_000 });
});

test.skipIf(!chrome)("next puts the more urgent record above the calmer one", async () => {
  await open("");
  const rows = await listed();

  // The fixture pins the pair this is read against: a finding something has
  // changed about, and a proposal nobody has ruled on yet. Urgency decides
  // before kind does — §8.5's order — so the finding is above the proposal
  // even though a proposal outranks a finding at equal urgency.
  const urgent = rows.findIndex((row) => row.id === "fnd_conflicting-evidence");
  const calm = rows.findIndex((row) => row.id === "pro_criteria-template");
  expect(`urgent:${urgent >= 0}`).toBe("urgent:true");
  expect(`calm:${calm >= 0}`).toBe("calm:true");
  expect(urgent).toBeLessThan(calm);

  // And the page renders the order the server sent rather than one of its
  // own: the client computes no ordering, here least of all, because this one
  // used to live in it.
  const answered = (await served("sort=next&needs=me")).slice(0, PAGE_SIZE).map((post) => post.id);
  expect(rows.map((row) => row.id)).toEqual(answered);
});

test.skipIf(!chrome)("a ruling from a row is confirmed, recorded once, and shown", async () => {
  await open("");
  const row = `li.feed-row[data-post='pro_criteria-template']`;
  await page.waitForSelector(row, { timeout: 15_000 });

  // Every /api/review/decide this page makes, so "confirmed before it is
  // recorded" is measured rather than inferred from what is on screen.
  const decides: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.url().includes("/api/review/decide")) decides.push(request.method());
  };
  page.on("request", watch);
  try {
    // Pressing the act asks. It does not rule: a disposition is an appended,
    // attributed event, so the row opens one sentence saying what it does.
    await page.click(`${row} [data-ruling='accept']`);
    await page.waitForSelector(`${row} .record-confirm`, { timeout: 15_000 });
    expect(decides).toEqual([]);
    const sentence = await page.$eval(`${row} .record-confirm p`, (line) => line.textContent ?? "");
    expect(sentence).toContain("appended permanently");

    await page.click(`${row} .record-confirm button[type='submit']`);
    // The row says what was done, in place of what could be done: a permanent
    // act that left the list looking the same is an act performed twice.
    await page.waitForFunction(
      (selector: string) =>
        (document.querySelector(`${selector} .feed-acted`)?.textContent ?? "").includes("accepted"),
      { timeout: 15_000 },
      row,
    );
    expect(decides).toEqual(["POST"]);
    expect(await page.$(`${row} [data-ruling='accept']`)).toBeNull();
  } finally {
    page.off("request", watch);
  }

  // And it is a record rather than a rendering: the record's own page reads
  // the standing back out of the store.
  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-post .heading-badges", { timeout: 15_000 });
  const badges = await page.$eval(
    ".record-post .heading-badges",
    (strip) => (strip as HTMLElement).innerText,
  );
  expect(badges.toLowerCase()).toContain("accepted");
});

test.skipIf(!chrome)("y and n open the confirmation for the focused row", async () => {
  await open("");
  const rows = await listed();
  expect(rows.length).toBeGreaterThan(0);
  const index = rows.findIndex((row) => row.acts.includes("accept"));
  expect(index).toBeGreaterThanOrEqual(0);
  const ruled = rows[index].id;
  const selector = `li.feed-row[data-post='${ruled}']`;

  const decides: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.url().includes("/api/review/decide")) decides.push(request.method());
  };
  page.on("request", watch);
  try {
    // j moves to the row the keys act on, and the key presses that row's own
    // control: a ruling recorded by key goes through the same confirmation as
    // one recorded by click, because a permanent act is never one keystroke
    // away.
    for (let press = 0; press <= index; press += 1) await page.keyboard.press("j");
    await page.waitForFunction(
      (want: string) => document.activeElement?.getAttribute("data-post") === want,
      { timeout: 15_000 },
      ruled,
    );

    await page.keyboard.press("y");
    await page.waitForSelector(`${selector} .record-confirm`, { timeout: 15_000 });
    expect(await page.$eval(`${selector} .record-confirm button[type='submit']`,
      (button) => (button as HTMLElement).innerText)).toContain("accept");
    await page.click(`${selector} .record-confirm button[type='button']`);
    await page.waitForFunction(
      (want: string) => document.querySelector(`${want} .record-confirm`) === null,
      { timeout: 15_000 },
      selector,
    );

    // The same for rejection, which is the other half of the pair a triaging
    // operator presses without looking.
    await page.keyboard.press("n");
    await page.waitForSelector(`${selector} .record-confirm`, { timeout: 15_000 });
    expect(await page.$eval(`${selector} .record-confirm button[type='submit']`,
      (button) => (button as HTMLElement).innerText)).toContain("reject");
    expect(decides).toEqual([]);
  } finally {
    page.off("request", watch);
  }
});

test.skipIf(!chrome)("a question asked from a row reads as one in the thread", async () => {
  await open("");
  // The proposal the fixture pins as awaiting, so the row that is asked about
  // is on the first page whatever the dice did — and it is a record this
  // deployment holds, so the thread it lands in is readable.
  const row = "li.feed-row[data-post='pro_criteria-template']";
  await page.waitForSelector(row, { timeout: 15_000 });
  const before = await page.$eval(
    row,
    (entry) => Number(
      (entry.querySelector(".feed-comments")?.textContent ?? "0").replace(/[^0-9]/gu, ""),
    ),
  );

  const asked = `Synthetic operator question ${Date.now()}`;
  await page.click(`${row} [data-ruling='ask']`);
  await page.waitForSelector(`${row} .record-ask input`, { timeout: 15_000 });
  await page.type(`${row} .record-ask input`, asked);
  await page.click(`${row} .record-ask button[type='submit']`);

  // The row says it asked, and the count beside the claim ticks up: a question
  // is a comment, and this is the one number on the row that moves the moment
  // it is recorded rather than when the projection is next rebuilt.
  await page.waitForFunction(
    (selector: string) =>
      (document.querySelector(`${selector} .feed-acted`)?.textContent ?? "").includes("asked"),
    { timeout: 15_000 },
    row,
  );
  const after = await page.$eval(
    row,
    (entry) => Number(
      (entry.querySelector(".feed-comments")?.textContent ?? "0").replace(/[^0-9]/gu, ""),
    ),
  );
  expect(after).toBe(before + 1);

  // And it is in the record's own thread as the act it is — "you asked" —
  // rather than as an opinion about the record.
  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-thread-list .record-comment", { timeout: 15_000 });
  const thread = await page.$eval(
    ".record-thread-list",
    (list) => (list as HTMLElement).innerText,
  );
  expect(thread).toContain("you asked");
  expect(thread).toContain(asked);
});

test.skipIf(!chrome)("j and k move and ↵ opens the focused row", async () => {
  // Observations are evidence rather than review subjects, so none of them is
  // ever waiting on the operator: this list is read with the filter off.
  await open("?kind=observation&needs=all");
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
