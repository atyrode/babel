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
    "§8.7's front page and §4.13's topics -- one list, its needs-me filter, its sorts, its chips, its rulings, the topics rail, a topic's own page and a record's filings -- in a browser",
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
    "that the rail groups the topics by the operator's interest, folds what he parked with its count, prints no filesystem path, and counts the unfiled backlog the feed's own filter answers with",
    "that accepting Babel's topic proposal from the rail is the ordinary review ruling, creates the topic and files the records it named",
    "that declining one asks for the reason before recording it",
    "that a topic's page states the recorded stance with its attribution, records a new one with a reason, and renders the binding as a name and a count rather than as paths",
    "that the identity fold asks Babel for a retirement, a split or a merge instead of calling a ledger route, and that the ask reads back in the operator's own words",
    "that a record's chips are its filings, that filing asks why and unfiling asks why not, and that a page opened cold says it does not know the filings rather than reporting none",
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
//
// An observation is not among them: by operator decision (2026-09-12) it is
// evidence at depth 3 of the hypothesis that cites it rather than a row, so
// the filter is gone and `?kind=observation` is refused like any other unknown
// kind.
const KINDS: Array<[string, string]> = [
  ["proposal", "Proposal"],
  ["finding", "Finding"],
  ["hypothesis", "Hypothesis"],
  ["question", "Question"],
];

// The same four kinds as the control sentence says them. They are written out
// rather than imported for the same reason the labels are: what is checked is
// what a reader sees.
const KIND_WORDS: Record<string, string> = {
  proposal: "proposals",
  finding: "findings",
  hypothesis: "hypotheses",
  question: "questions",
};

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
  // What the row offers to do about it, in the order it offers them: the
  // rulings for a record, and the three answers for a question — a question is
  // answered rather than ruled on, and §8.4 puts the decision where the record
  // is read.
  acts: string[];
  answers: string[];
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
  await page.waitForSelector("ol.feed-list > li.feed-row:not(.feed-skeleton)", { timeout: 15_000 });
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("ol.feed-list > li.feed-row")).map((row) => ({
      id: row.getAttribute("data-post") ?? "",
      kind: row.querySelector(".feed-kind")?.textContent ?? "",
      created: row.querySelector("time.feed-age")?.getAttribute("datetime") ?? "",
      score: row.querySelector(".feed-score")?.textContent ?? "",
      awaiting: row.hasAttribute("data-awaiting"),
      why: row.querySelector(".feed-why")?.textContent ?? "",
      acts: Array.from(row.querySelectorAll("[data-ruling]")).map(
        (button) => button.getAttribute("data-ruling") ?? "",
      ),
      answers: Array.from(row.querySelectorAll("[data-answer]")).map(
        (button) => button.getAttribute("data-answer") ?? "",
      ),
    })));
}

// The controls are one sentence with three words the reader can change, each
// of which opens a menu. Everything below drives them the way he does: press
// the word, choose from what opens.
type Pick = "needs" | "sort" | "kinds";

async function openPick(name: Pick): Promise<void> {
  await page.click(`[data-pick='${name}']`);
  await page.waitForSelector(`[data-pick='${name}'][aria-expanded='true']`, { timeout: 15_000 });
}

// What the sentence says. It is read as text rather than as attributes
// because it is prose the operator reads: "Showing what needs me · sorted by
// next · all kinds".
function sentence(): Promise<string> {
  return page.$eval(".feed-sentence", (line) =>
    (line as HTMLElement).innerText.replace(/\s+/gu, " ").trim());
}

// Turning one kind on or off. The menu is a set, so it stays open between
// presses and is closed here explicitly.
async function toggleKind(kind: string, want: boolean): Promise<void> {
  await openPick("kinds");
  await page.click(`[data-kind='${kind}']`);
  await page.waitForSelector(`[data-kind='${kind}'][aria-checked='${want}']`, { timeout: 15_000 });
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => document.querySelector(".feed-menu") === null, { timeout: 15_000 });
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

// counted is the total the sentence ends with, which is the size of the
// eligible set rather than of the page: the control under the list exists
// because the two differ.
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
  await openPick("sort");
  await page.click(`[data-sort='${sort}']`);
  if (period) {
    // A windowed order keeps the menu open on the column that names the
    // period, which is where the second press lands.
    await page.waitForSelector("[role='group'][aria-label='Period']", { timeout: 15_000 });
    await page.click(`[data-window='${period}']`);
  }
  await page.waitForFunction(() => document.querySelector(".feed-menu") === null, { timeout: 15_000 });
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
  // The sentence says what is in the list, and with nothing narrowed it says
  // every kind: "all kinds" is a state of the filter rather than a fifth kind.
  expect(await sentence()).toContain("all kinds");
  await openPick("kinds");
  expect(await page.$eval("[data-kind='all']", (item) => item.getAttribute("aria-checked")))
    .toBe("true");
  await page.keyboard.press("Escape");

  // Each kind narrows the one list to itself, says so in the URL, and holds
  // some of the corpus. The totals then have to add up to the unfiltered one,
  // which is what makes "all kinds" every kind and nothing else — and unlike a
  // count of the rows on screen it does not depend on what the ranking put on
  // the first page.
  let accounted = 0;
  for (const [kind, label] of KINDS) {
    await toggleKind(kind, true);
    await page.waitForFunction(
      (want: string) => window.location.hash.includes(`kind=${want}`),
      { timeout: 15_000 },
      kind,
    );
    await page.waitForFunction(
      (want: string) => {
        const rows = Array.from(document.querySelectorAll("ol.feed-list > li.feed-row"));
        return rows.length > 0
          && rows.every((row) => row.querySelector(".feed-kind")?.textContent === want);
      },
      { timeout: 15_000 },
      label,
    );
    // The sentence names the one kind in force, in the plural it would be read
    // in: the control states what is happening as well as doing it.
    expect(await sentence()).toContain(KIND_WORDS[kind]);
    const narrowed = await counted();
    expect(`${kind}:${narrowed > 0 && narrowed < everything}`).toBe(`${kind}:true`);
    accounted += narrowed;
    // Off again, so the next kind is read on its own: the kinds are a set, and
    // pressing a second one widens rather than replaces.
    await toggleKind(kind, false);
    await page.waitForFunction(() => !window.location.hash.includes("kind="), { timeout: 15_000 });
  }
  expect(accounted).toBe(everything);

  // The set widens rather than replaces, and the sentence says both kinds
  // rather than the last one pressed.
  await openPick("kinds");
  await page.click("[data-kind='proposal']");
  await page.waitForSelector("[data-kind='proposal'][aria-checked='true']", { timeout: 15_000 });
  await page.click("[data-kind='finding']");
  await page.waitForSelector("[data-kind='finding'][aria-checked='true']", { timeout: 15_000 });
  await page.keyboard.press("Escape");
  await page.waitForFunction(
    // The comma the client joins the set with is percent-encoded in a query
    // string, so the URL is read decoded rather than matched against its
    // wire form.
    () => decodeURIComponent(window.location.hash).includes("kind=proposal,finding"),
    { timeout: 15_000 },
  );
  expect(await sentence()).toContain("proposals and findings");

  // The filter is a place, so Back goes back to it. A reader who narrows to
  // findings, clears the filter and presses Back is asking for his findings
  // again, not for whatever he was reading before the feed.
  await open("?needs=all");
  await toggleKind("finding", true);
  await page.waitForFunction(() => window.location.hash.includes("kind=finding"), { timeout: 15_000 });
  await openPick("kinds");
  await page.click("[data-kind='all']");
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

  // The period is a column of the order's own menu for the two orders that
  // read it and is absent for the four that do not: a period selector beside
  // "newest" is a control that does nothing and does not say so. It is also
  // why choosing one of those two leaves the menu open — the second column is
  // where the next press goes.
  const windowed: Array<[string, boolean]> = [
    ["next", false], ["hot", false], ["new", false], ["rising", false],
    ["top", true], ["controversial", true],
  ];
  for (const [sort, offered] of windowed) {
    await openPick("sort");
    await page.click(`[data-sort='${sort}']`);
    await page.waitForFunction(
      (want: string) => window.location.hash.includes(`sort=${want}`),
      { timeout: 15_000 },
      sort,
    );
    // The column is waited for rather than sampled: the URL is written by the
    // press and the column is rendered from the URL, so the two are one
    // commit apart and a read between them is a race rather than a result.
    // An order that reads no period closes the menu instead, which is the
    // same wait read the other way round.
    await page.waitForFunction(
      (want: boolean) => {
        const menu = document.querySelector(".feed-menu");
        if (!want) return menu === null;
        return menu?.querySelector("[role='group'][aria-label='Period']") != null;
      },
      { timeout: 15_000 },
      offered,
    );
    const period = await page.$("[role='group'][aria-label='Period']");
    expect(`${sort}:${period !== null}`).toBe(`${sort}:${offered}`);
    await page.keyboard.press("Escape");
    await page.waitForFunction(
      () => document.querySelector(".feed-menu") === null,
      { timeout: 15_000 },
    );
  }
});

test.skipIf(!chrome)("the front page arrives showing what needs the operator", async () => {
  await open("");

  // Two controls are in force without the reader having pressed anything, and
  // the sentence above the list says both: the filter that says "what needs
  // me" and the ordering that says which of it is next. That is the mod queue,
  // and it is this list.
  await page.waitForSelector(".feed-sentence", { timeout: 15_000 });
  expect(await sentence()).toContain("Showing what needs me");
  expect(await sentence()).toContain("sorted by next");
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
    // A question carries no review disposition — it is answered, not ruled on
    // — so its row offers §4.8's three outcomes rather than the four rulings.
    // Every record that is waiting offers the rulings and the question.
    const rulings = row.kind === "Question"
      ? []
      : ["accept", "reject", "defer", "refine", "ask"];
    const answers = row.kind === "Question" ? ["answered", "unknown", "declined"] : [];
    expect(`${row.id}:${row.acts.join(",")}`).toBe(`${row.id}:${rulings.join(",")}`);
    expect(`${row.id}:${row.answers.join(",")}`).toBe(`${row.id}:${answers.join(",")}`);
  }

  // One gesture widens it to everything, and the ordering follows: a reader
  // who is no longer triaging is reading a feed, and the front page of a feed
  // is hot. Both land in the URL, because both are places he shares and walks
  // back out of.
  await openPick("needs");
  await page.click("[data-needs='all']");
  await page.waitForFunction(
    () => window.location.hash.includes("needs=all"),
    { timeout: 15_000 },
  );
  await page.waitForFunction(
    () => {
      const line = (document.querySelector(".feed-sentence") as HTMLElement | null)?.innerText ?? "";
      return line.includes("Showing everything") && line.includes("sorted by hot");
    },
    { timeout: 15_000 },
  );
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

  // Back restores the filter it replaced, exactly as a kind does.
  await page.goBack();
  await page.waitForFunction(
    () =>
      ((document.querySelector(".feed-sentence") as HTMLElement | null)?.innerText ?? "")
        .includes("Showing what needs me"),
    { timeout: 15_000 },
  );

  // `m` is the same gesture from the keyboard, which is where the operator's
  // hands are while he reads the list.
  await page.keyboard.press("m");
  await page.waitForFunction(
    () => window.location.hash.includes("needs=all"),
    { timeout: 15_000 },
  );
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

  // The acts are in the row's markup and out of sight until the reader is on
  // it. That is the whole of what hover-revealed means, and it is asserted
  // rather than assumed: a `display: none` that never lifted would be five
  // controls an operator cannot reach.
  const reach = (selector: string) => {
    const control = document.querySelector(`${selector} [data-ruling='accept']`);
    return control === null ? "absent" : (control as HTMLElement).offsetParent === null
      ? "hidden"
      : "shown";
  };
  expect(await page.evaluate(reach, row)).toBe("hidden");
  await page.hover(`${row} .feed-claim`);
  await page.waitForFunction(
    (selector: string) => {
      const control = document.querySelector(`${selector} [data-ruling='accept']`);
      return control !== null && (control as HTMLElement).offsetParent !== null;
    },
    { timeout: 15_000 },
    row,
  );

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
    const asked = await page.$eval(`${row} .record-confirm p`, (line) => line.textContent ?? "");
    expect(asked).toContain("appended permanently");

    await page.click(`${row} .record-confirm button[type='submit']`);
    // The row leaves the list it was waiting in: under "what needs me" the
    // list is what is left to do, and a row sitting in it with "accepted" on
    // it is a line the reader skips past for the rest of the session. What
    // stands in its place is the receipt, bottom-left, with the one act that
    // undoes a permanent ruling — reopening it.
    await page.waitForFunction(
      (selector: string) => document.querySelector(selector) === null,
      { timeout: 15_000 },
      row,
    );
    const receipt = await page.$eval(".feed-toast", (note) => (note as HTMLElement).innerText);
    expect(receipt).toContain("accepted");
    expect(receipt).toContain("reopen");
    // And the count at the end of the sentence is one shorter, because the
    // list it counts is.
    expect(await page.$eval(".feed-ruled", (note) => (note as HTMLElement).innerText))
      .toContain("ruled today 1");
    expect(decides).toEqual(["POST"]);
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
  // The acts arrive with the pointer, so the pointer goes to the row first.
  await page.hover(`${row} .feed-claim`);
  await page.waitForSelector(`${row} [data-ruling='ask']`, { visible: true, timeout: 15_000 });
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
  // Read with the operator's filter off, so the rows the keys move over are
  // the whole list rather than the queue: what is being checked is the
  // movement, and a row that offers rulings is a row whose controls the keys
  // also reach (asserted in its own test above).
  await open("?kind=hypothesis&needs=all");
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

  // A name no entity answers to is a real state and says so: the feed still
  // narrows by the name, nothing is filed under it, and nothing on the page
  // offers to act on a topic that does not exist. Day one's sentence here
  // would tell a reader Babel has produced nothing while the whole corpus
  // sits one click away.
  await open("t/nothing-cites-this");
  await page.waitForSelector(".empty-state", { timeout: 15_000 });
  expect(await page.$eval("h1", (heading) => heading.textContent)).toBe("t/nothing-cites-this");
  const empty = await page.evaluate(() => document.body.innerText);
  expect(empty).toContain("Nothing matches this view");
  expect(empty).not.toContain("Babel has not posted anything yet");
  expect(empty).toContain("No topic in this deployment answers to that name");
  expect(await page.$(".topic-header [data-interest]")).toBeNull();
  expect(await page.$(".topic-identity")).toBeNull();
});

// §4.13's rail: the topics the operator accepted, grouped by where he stands
// toward them, with what he has parked folded rather than gone — "not
// interested is a signal, not a deletion" — and what Babel has proposed under
// them.
//
// The binding is the part a rail must not print. A topic's identity can be the
// common directory every worktree of a repository shares, and §4.13 is
// explicit that a locator is evidence about a topic and never the topic, so a
// path in a row would be the surface teaching the reader to read a topic as a
// folder.
test.skipIf(!chrome)("the rail groups topics by interest, folds what is parked, and prints no path", async () => {
  await open("");
  await page.waitForSelector(".feed-rail .topic-group-label", { timeout: 15_000 });
  const rail = await page.evaluate(() => {
    const aside = document.querySelector(".feed-rail") as HTMLElement;
    return {
      labels: Array.from(aside.querySelectorAll(".topic-group-label")).map((node) =>
        (node.textContent ?? "").trim()),
      folds: Array.from(aside.querySelectorAll("details.topic-fold")).map((fold) => ({
        summary: (fold.querySelector("summary")?.textContent ?? "").replace(/\s+/gu, " ").trim(),
        open: (fold as HTMLDetailsElement).open,
        rows: fold.querySelectorAll("li").length,
      })),
      text: aside.innerText,
      // How tall each row of the rail is. A topic row is one line — the
      // density contract — and the measurement is the rendered box rather
      // than the text, because the name and its count are laid out as a row
      // and read as two lines in the DOM either way.
      //
      // A proposal's row is deliberately not in this list: it carries what
      // the change would do, why, who wrote it and two rulings, so it is a
      // decision rather than a destination and is measured as a whole below.
      heights: Array.from(aside.querySelectorAll(".topic-list > li:not(.topic-proposal) > a")).map(
        (link) => (link as HTMLElement).getBoundingClientRect().height),
      proposals: Array.from(aside.querySelectorAll(".topic-proposal")).map((row) =>
        (row as HTMLElement).getBoundingClientRect().height),
    };
  });
  expect(rail.labels).toContain("Working on it");
  expect(rail.labels).toContain("Keep an eye");
  // "Babel proposes" carries the count of decisions under it, because a reader
  // deciding whether to look wants the number.
  expect(rail.labels.some((label: string) => /^Babel proposes\s*\d+$/u.test(label))).toBe(true);
  // Parked and excluded are folded, closed, and say how many they hold.
  expect(rail.folds.length).toBe(2);
  for (const fold of rail.folds) {
    expect(fold.open).toBe(false);
    expect(fold.rows).toBeGreaterThan(0);
    expect(fold.summary).toMatch(/^(?:Not now|Excluded)\s*\d+$/u);
  }
  // No path anywhere in the rail, and no row of it is two lines tall.
  expect(rail.text).not.toMatch(/\/home\/|\.git/u);
  expect(rail.heights.length).toBeGreaterThan(2);
  for (const height of rail.heights) expect(height).toBeLessThan(32);
  // A proposal's row stays inside the rail's own budget: it is three short
  // lines and a pair of buttons, not a card.
  expect(rail.proposals.length).toBeGreaterThan(0);
  for (const height of rail.proposals) expect(height).toBeLessThan(140);

  // The parked topic is reachable by opening the fold: a stance moves a topic
  // out of the way and never out of the surface.
  await page.click("details.topic-fold > summary");
  await page.waitForFunction(
    () => (document.querySelector("details.topic-fold") as HTMLDetailsElement).open,
    { timeout: 15_000 },
  );
  const parked = await page.$eval("details.topic-fold", (fold) => (fold as HTMLElement).innerText);
  expect(parked).toMatch(/t\//u);

  // The posts nothing has filed are a filter over the feed rather than a bin,
  // and the rail's count is the number that filter answers with.
  const unfiled = await page.evaluate(async () => {
    const rail_ = document.querySelector(".feed-rail") as HTMLElement;
    const row = Array.from(rail_.querySelectorAll(".topic-list > li > a")).find((link) =>
      (link.textContent ?? "").startsWith("unfiled")) as HTMLAnchorElement | undefined;
    const answer = await fetch("/api/feed?topic=unfiled&limit=1").then((r) => r.json());
    return {
      href: row?.getAttribute("href") ?? "",
      shown: Number((row?.textContent ?? "").replace(/[^0-9]/gu, "")),
      total: answer.total as number,
    };
  });
  expect(unfiled.href).toContain("topic=unfiled");
  expect(unfiled.shown).toBe(unfiled.total);
});

// A topic change is an ordinary proposal (operator direction, 2026-09-12), so
// the rail's Accept is a shortcut to the same ruling the record page makes and
// the answer says what the ledger then did.
test.skipIf(!chrome)("accepting Babel's proposal from the rail creates the topic and files its records", async () => {
  await open("");
  const row = ".feed-rail .topic-proposal";
  await page.waitForSelector(`${row} [data-topic-act='accept']`, { timeout: 15_000 });

  // What the row says before it is ruled on: what it would do, how much it
  // touches, why, and which run wrote it.
  const offered = await page.$eval(row, (item) => (item as HTMLElement).innerText);
  expect(offered).toMatch(/New topic t\/manifold/u);
  expect(offered).toMatch(/by run_/u);
  // The row is the proposal's own record, one click away.
  expect(await page.$eval(`${row} a`, (link) => link.getAttribute("href") ?? ""))
    .toContain("#/r/pro_topic-manifold");

  const before = await page.evaluate(async () => {
    const answer = await fetch("/api/topics").then((r) => r.json());
    return {
      names: (answer.topics ?? []).map((topic: { name: string }) => topic.name) as string[],
      unfiled: answer.unfiled as number,
    };
  });
  expect(before.names).not.toContain("manifold");

  // It is the review route that records it, not a topic route of its own.
  const decides: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.method() === "POST") decides.push(new URL(request.url()).pathname);
  };
  page.on("request", watch);
  try {
    await page.click(`${row} [data-topic-act='accept']`);
    // The receipt says what was ruled and what the ledger did with it.
    await page.waitForSelector(".feed-rail [data-topic-ruled='pro_topic-manifold']", {
      timeout: 15_000,
    });
    const said = await page.$eval(".feed-rail [data-topic-ruled='pro_topic-manifold']",
      (line) => (line as HTMLElement).innerText);
    expect(said).toContain("Accepted");
    expect(said).toMatch(/\d+ filed/u);
    expect(decides).toContain("/api/review/decide");
  } finally {
    page.off("request", watch);
  }

  // And the topic is in the accepted list, with the records the proposal
  // named now filed under it: the count the row promised is the count the
  // topic arrives with, and the unfiled backlog is that much shorter.
  await page.waitForFunction(
    () => (document.querySelector(".feed-rail") as HTMLElement).innerText.includes("t/manifold"),
    { timeout: 15_000 },
  );
  const after = await page.evaluate(async () => {
    const answer = await fetch("/api/topics").then((r) => r.json());
    const created = (answer.topics ?? []).find((topic: { name: string }) => topic.name === "manifold");
    return { posts: created?.posts as number, unfiled: answer.unfiled as number };
  });
  expect(after.posts).toBeGreaterThan(0);
  expect(after.unfiled).toBeLessThan(before.unfiled);
});

// Declining keeps the operator's words: the ruling refuses a rejection with no
// reason, so the row asks for one where he is rather than letting the server
// say no after the click.
test.skipIf(!chrome)("declining a proposal from the rail asks for the reason first", async () => {
  await open("");
  const row = ".feed-rail .topic-proposal:last-of-type";
  await page.waitForSelector(`${row} [data-topic-act='decline']`, { timeout: 15_000 });
  const id = await page.$eval(`${row} a`, (link) =>
    (link.getAttribute("href") ?? "").split("/r/")[1] ?? "");
  expect(id.length).toBeGreaterThan(0);

  await page.click(`${row} [data-topic-act='decline']`);
  await page.waitForSelector(`${row} .topic-reason input`, { timeout: 15_000 });
  // Nothing is recorded by opening the box, and the empty box cannot record:
  // the control is required rather than rejected afterwards.
  expect(await page.$eval(`${row} .topic-reason input`,
    (input) => (input as HTMLInputElement).required)).toBe(true);

  const reason = `Not a topic: synthetic decline ${Date.now()}`;
  await page.type(`${row} .topic-reason input`, reason);
  await page.click(`${row} .topic-reason button[type='submit']`);
  await page.waitForSelector(`.feed-rail [data-topic-ruled='${id}']`, { timeout: 15_000 });
  expect(await page.$eval(`.feed-rail [data-topic-ruled='${id}']`,
    (line) => (line as HTMLElement).innerText)).toContain("Declined");

  // The proposal is gone from what is offered, because it has been answered.
  const offered = await page.evaluate(async () => {
    const answer = await fetch("/api/topics").then((r) => r.json());
    return (answer.proposed ?? []).map((row_: { proposal_id: string }) => row_.proposal_id) as string[];
  });
  expect(offered).not.toContain(id);
});

// The topic's own page: what it is, what is in it, and the one act on a topic
// that is still the operator's own (§4.13).
test.skipIf(!chrome)("a topic page states the stance, records a new one, and prints no path", async () => {
  await open("t/kepler");
  await page.waitForSelector(".topic-header [data-interest]", { timeout: 15_000 });

  const header = await page.$eval(".topic-header", (node) => (node as HTMLElement).innerText);
  // The figures, and the binding as a name with a count rather than as paths.
  expect(header).toMatch(/\d+ posts?/u);
  expect(header).toContain("example.invalid/synthetic/kepler");
  expect(header).toMatch(/\d+ checkout/u);
  expect(header).not.toMatch(/\/home\/|\.git/u);
  // The paths are readable, once, where a locator belongs: in the title.
  expect(await page.$eval(".topic-binding", (node) => node.getAttribute("title") ?? ""))
    .toContain("/home/demo/projects/kepler");
  // The stance in force is pressed, and the recorded reason and attribution
  // are under it.
  expect(await page.$eval("[data-interest='watching']",
    (button) => button.getAttribute("aria-pressed"))).toBe("true");
  expect(await page.$eval(".topic-stance", (node) => (node as HTMLElement).innerText))
    .toContain("Keep an eye");

  // Changing it opens the reason box and records nothing until it is
  // submitted: the reason is what the triage recipe reads.
  const posts: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.method() === "POST") posts.push(new URL(request.url()).pathname);
  };
  page.on("request", watch);
  try {
    await page.click("[data-interest='not-now']");
    await page.waitForSelector(".topic-header .topic-reason input", { timeout: 15_000 });
    expect(posts).toEqual([]);
    const reason = `Parked while the import work lands ${Date.now()}`;
    await page.type(".topic-header .topic-reason input", reason);
    await page.click(".topic-header .topic-reason button[type='submit']");
    await page.waitForFunction(
      (want: string) =>
        (document.querySelector(".topic-stance") as HTMLElement | null)?.innerText.includes(want)
          ?? false,
      { timeout: 15_000 },
      reason,
    );
    expect(posts).toContain("/api/topics/ent_kepler/interest");
  } finally {
    page.off("request", watch);
  }

  // What is recorded is what the ledger now says: the stance, the words
  // verbatim, and who said it.
  const stance = await page.$eval(".topic-stance", (node) => (node as HTMLElement).innerText);
  expect(stance).toContain("Not now");
  expect(stance).toMatch(/opr_/u);
  expect(await page.$eval("[data-interest='not-now']",
    (button) => button.getAttribute("aria-pressed"))).toBe("true");

  // And it is a fact rather than a rendering: the ledger answers with it.
  const recorded = await page.evaluate(async () => {
    const answer = await fetch("/api/topics").then((r) => r.json());
    const row = (answer.topics ?? []).find((topic: { name: string }) => topic.name === "kepler");
    return row?.interest as { state: string; reason: string };
  });
  expect(recorded.state).toBe("not-now");
  expect(recorded.reason).toContain("Parked while the import work lands");
});

// Retiring, splitting and merging go through Babel (operator direction,
// 2026-09-12): the page records what the operator wants and why, and Babel
// answers with a proposal he rules on. What must be true is that nothing here
// writes to the ledger — no retire, no merge, no split route is called — and
// that the ask is readable afterwards.
test.skipIf(!chrome)("the identity fold asks Babel rather than rewriting the ledger", async () => {
  await open("t/atlas");
  await page.waitForSelector(".topic-identity > summary", { timeout: 15_000 });
  await page.click(".topic-identity > summary");
  await page.waitForSelector(".topic-ask textarea", { timeout: 15_000 });

  // The three acts are offered as one ask rather than as three buttons that
  // write.
  const offered = await page.$$eval(".topic-ask select:first-of-type option",
    (options) => options.map((option) => (option as HTMLOptionElement).value));
  expect(offered).toEqual(["retire", "split", "merge"]);

  const wrote: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.method() === "POST") wrote.push(new URL(request.url()).pathname);
  };
  page.on("request", watch);
  try {
    // A merge names the other topic by name, from the topics that exist.
    await page.select(".topic-ask select:first-of-type", "merge");
    await page.waitForSelector("[data-ask='into']", { timeout: 15_000 });
    await page.select("[data-ask='into']", "kepler");
    const reason = `Both cover one import pipeline ${Date.now()}`;
    await page.type(".topic-ask textarea", reason);
    await page.click(".topic-ask button[type='submit']");
    // The ask is listed under the form, in the operator's own words, with no
    // status attached to it: capture opens nothing and schedules nothing.
    await page.waitForSelector(".topic-asks li", { timeout: 15_000 });
    const listed_ = await page.$eval(".topic-asks", (list) => (list as HTMLElement).innerText);
    expect(listed_).toContain("You asked Babel to merge this into t/kepler");
    expect(wrote).toContain("/api/complaint/tell");
    for (const path of wrote) {
      expect(path).not.toMatch(/\/api\/topics\/(?:merge|split)|\/retire$/u);
    }
  } finally {
    page.off("request", watch);
  }

  // The wording is the record: it is stored verbatim under the topic's name,
  // which is what lets the triage recipe read why the operator said it.
  const told = await page.evaluate(async () => {
    const answer = await fetch("/api/complaints?limit=50").then((r) => r.json());
    return (answer.items ?? []).map((item: { summary: string }) => item.summary) as string[];
  });
  expect(told.some((summary) => summary.startsWith("topic t/atlas: merge into t/kepler"))).toBe(true);
});

// A record's chips are its filings, and both acts on them keep a reason
// (§4.13: filing is a link with a rationale, and a withdrawal is a row rather
// than an absence).
test.skipIf(!chrome)("a record's chips are its filings, and the operator can file and unfile one", async () => {
  await open("t/atlas");
  const claim = "li.feed-row[data-post='fnd_conflicting-evidence'] a.feed-claim";
  await page.waitForSelector(claim, { timeout: 15_000 });
  await page.click(claim);
  await page.waitForSelector(".record-post .record-topic", { timeout: 15_000 });

  // The chips are the topics the row carried, which are the entities the
  // record is filed under — not the workspace it was produced in.
  const chips = await page.$$eval(".record-post .record-topic a",
    (links) => links.map((link) => link.textContent ?? ""));
  expect(chips).toContain("t/atlas");
  expect(await page.$eval(".record-post-meta", (node) => (node as HTMLElement).innerText))
    .not.toMatch(/\/home\//u);

  // Filing asks which topic and why, and the topic is picked from the ones
  // that exist: filing does not create one.
  await page.click("details.record-filing > summary");
  await page.waitForSelector(".record-filing [data-filing='topic']", { timeout: 15_000 });
  await page.waitForFunction(
    () => document.querySelectorAll(".record-filing [data-filing='topic'] option").length > 1,
    { timeout: 15_000 },
  );
  expect(await page.$eval(".record-filing textarea",
    (box) => (box as HTMLTextAreaElement).required)).toBe(true);
  const target = await page.$$eval(".record-filing [data-filing='topic'] option",
    (options) =>
      options
        .map((option) => (option as HTMLOptionElement).value)
        .filter((value) => value !== "")[0]);
  await page.select(".record-filing [data-filing='topic']", target);
  await page.type(".record-filing textarea", "The same import pipeline is the subject here.");
  await page.click(".record-filing button[type='submit']");
  await page.waitForFunction(
    (want: string) =>
      Array.from(document.querySelectorAll(".record-post .record-topic a")).some(
        (link) => link.textContent === `t/${want}`),
    { timeout: 15_000 },
    target,
  );

  // The filing is a record rather than a rendering: the feed narrowed to that
  // topic now holds this post.
  const filedUnder = await page.evaluate(async (name: string) => {
    const answer = await fetch(`/api/feed?needs=all&limit=100&topic=${name}`).then((r) => r.json());
    return (answer.posts ?? []).map((post: { id: string }) => post.id) as string[];
  }, target);
  expect(filedUnder).toContain("fnd_conflicting-evidence");

  // Withdrawing one asks why, and keeps the filing readable: the × opens a
  // box rather than deleting an edge.
  await page.click(`.record-post [data-unfile='${target}']`);
  await page.waitForSelector(".record-filing-open input", { timeout: 15_000 });
  await page.type(".record-filing-open input", "Filed by hand in a test; withdrawing it.");
  await page.click(".record-filing-open button[type='submit']");
  await page.waitForFunction(
    (want: string) =>
      !Array.from(document.querySelectorAll(".record-post .record-topic a")).some(
        (link) => link.textContent === `t/${want}`),
    { timeout: 15_000 },
    target,
  );
  const afterUnfile = await page.evaluate(async (name: string) => {
    const answer = await fetch(`/api/feed?needs=all&limit=100&topic=${name}`).then((r) => r.json());
    return (answer.posts ?? []).map((post: { id: string }) => post.id) as string[];
  }, target);
  expect(afterUnfile).not.toContain("fnd_conflicting-evidence");

  // A record page opened cold knows nothing about the filings rather than
  // reporting the record as unfiled, and says which of the two it is.
  await open("r/fnd_conflicting-evidence");
  await page.waitForSelector("details.record-filing", { timeout: 15_000 });
  expect(await page.$(".record-post .record-topic")).toBeNull();
  await page.click("details.record-filing > summary");
  expect(await page.$eval("details.record-filing", (fold) => (fold as HTMLElement).innerText))
    .toContain("is not on this page");
});

// The index behind the rail's twelve: the same three lists, with the figures a
// page can afford.
test.skipIf(!chrome)("the topics index reads the same three lists with counts", async () => {
  await open("t");
  await page.waitForSelector("ul.topic-index", { timeout: 15_000 });
  const page_ = await page.$eval(".topics-page", (node) => (node as HTMLElement).innerText);
  expect(page_).toMatch(/\d+ posts? · \d+ waiting on you/u);
  expect(page_).toContain("Working on it");
  // The heading is set in small capitals by the stylesheet, so what innerText
  // answers with is the transformed text: the words are what is asserted, not
  // the case the shell sets them in.
  expect(page_).toMatch(/babel proposes/iu);
  expect(page_).toContain("the triage backlog");
  // No path on the index either, and the binding is still one gesture away.
  expect(page_).not.toMatch(/\/home\/|\.git/u);
  const titles = await page.$$eval("ul.topic-index a",
    (links) => links.map((link) => link.getAttribute("title") ?? ""));
  expect(titles.some((title) => title.includes("repository: example.invalid"))).toBe(true);
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
