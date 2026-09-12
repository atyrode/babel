// Browser acceptance for the reading surface: the feed, Watch, Ask and the
// record page, driven against the synthetic mock server so no Go server,
// archive, or network is needed (SPEC.md §10's fixture rule).
//
// What only a browser can prove is covered here: that the areas actually
// render, that one record peels to five depths in place, that every control is
// reachable by keyboard, that narrow and wide viewports lay out without
// overflow, that the hostile HTML/Markdown/URL/control fixtures render inert —
// no script executes and no markup is injected — and that record content never
// enters a request URL or the location hash. The real server's leak channels
// stay covered by leak.test.ts; internal/web owns the HTTP-side contracts.
//
// The eleven concept-named routes this suite used to walk are gone: a record is
// read at /r/<id> whatever its kind, the four listings are one, and the review,
// evaluation and per-kind detail pages redirect there. §8.7 then made that one
// listing the front page, so the mod queue is the feed under `needs=me` and
// Read is the feed's own chips — what the feed itself does is
// browser/feed.test.ts's subject, and this file walks it only as one of the
// areas. /queue is a redirect and nothing here drives it: the assertions that
// went with the removed pages went with them rather than being re-pinned to
// new wording, and what is asserted below is what a reader can see and do. The
// redirect table itself is enumerated once, in shell.test.ts, against App.tsx's
// routes; a second partial copy here only made a dropped entry easier to miss.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_HTML, UNBROKEN_TOKEN } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

// A developer without Chrome skips this suite. The notice resolveChrome prints
// in that case is what keeps the skip from reading as a pass: nothing else
// drives these areas in a browser, so a green run that never launched one
// would report the whole surface as acceptable while no page was ever rendered.
// In CI the same absence is a hard failure.
const chrome = resolveChrome({
  gate: "Reading surface web gate",
  covers: "the reading surface -- the feed, Watch, Ask and the record page -- in a browser",
  unverified: [
    "that every area renders against the mock at all, and that an empty deployment reads as a state rather than as a bug",
    "that one record peels to five depths in place, that an absent section is absent rather than empty, and that no identifier appears above the machinery",
    "that the hostile HTML, Markdown, URL and control fixtures render inert: no script runs, no markup is injected, and the literal markup stays visible as escaped text",
    "that every control is reachable by keyboard — every depth, the rule bar's rulings, the confirmation they open, and the palette — and that no route overflows at either 390px or 1440px",
    "that the operator's one voice on a record is the ruling, that the stances he recorded before the arrows were retired stay readable, that recording a disposition persists and reads back, and that accepting a plan and answering a question are explicit acts",
    "that no record content reaches a request URL or the location hash",
    "that the feed whose catalog read came back partial says so in the catalog's own terms and names no machine",
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
let emptyMock: MockServer | null = null;
// A launch whose durable store could not be opened. Separate from emptyMock
// because an empty frontier and an absent one are different facts: the first
// is an answer, the second is a refusal, and the operator must be able to
// tell which one is on screen.
let unwiredMock: MockServer | null = null;
// A launch whose shared catalog did not answer. Separate from every other
// variant because it is the state a renderer is most likely to get wrong: the
// local records are all still readable, and the temptation is to call their
// sync state "local", which would promise that nothing is carrying them
// anywhere -- a claim nothing observed.
let degradedMock: MockServer | null = null;
let browser: Browser | null = null;
let page: Page;

// Every request the page makes, so the "no record content in URLs" channel
// can be asserted over the whole browsing session rather than one route.
const requestURLs: string[] = [];

const WIDE = { width: 1440, height: 900 };
const NARROW = { width: 390, height: 844 };

// Every route of the reading surface, including the awkward record fixtures:
// a record whose every field is populated, one that is a bare claim, one
// carrying hostile markup, and one whose claim is a thousand characters with
// no break in them. The overflow audit walks all of them at both widths.
//
// Settings' help section is deliberately absent: it is reference text and
// exceeds three screens by design, which is the one place the density rule
// does not apply.
const ROUTES = [
  "",
  // The mod queue is the feed's own filter now (§8.7), so the route that used
  // to be a second listing is the front page under `needs=me`.
  "?needs=me",
  "watch",
  "ask/questions",
  "ask/questions/qst_focus-policy",
  "ask/entities",
  "ask/entities/ent_atlas",
  "ask/entities/ent_longname",
  "ask/facts",
  "r/pro_criteria-template",
  "r/hyp_unverified-closures",
  "r/hyp_hostile-content",
  "r/hyp_dense-token",
  "r/fnd_conflicting-evidence",
  "sessions",
  "settings",
];

async function open(route: string, base = rich?.base): Promise<void> {
  // A hash-only change is a same-document navigation, so a full reload keeps
  // every test starting from a freshly booted application.
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

// peelTitles is what the record page offers to open, in order. A section the
// record has nothing for is absent from this list rather than present and
// empty, which is the difference the depth model turns on.
function peelTitles(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("details.peel > summary")).map((summary) =>
      (summary.textContent ?? "").replace(/\s+/gu, " ").trim()));
}

// openPeel clicks a depth open by its own summary, the way a reader does, and
// answers with the text it revealed. Clicking rather than setting the `open`
// attribute is the point: the disclosure has to work as a control.
async function openPeel(title: string): Promise<string> {
  const revealed = await page.evaluate((needle: string) => {
    const found = Array.from(document.querySelectorAll("details.peel")).find((peel) =>
      (peel.querySelector("summary")?.textContent ?? "").startsWith(needle));
    if (!found) return null;
    if (!(found as HTMLDetailsElement).open) found.querySelector("summary")?.dispatchEvent(
      new MouseEvent("click", { bubbles: true }));
    return (found as HTMLElement).innerText;
  }, title);
  if (revealed === null) throw new Error(`no peel titled ${title}`);
  return revealed;
}

beforeAll(async () => {
  if (!chrome) return;

  // The mock serves web/dist, so the bundle under test is built from the
  // sources in this checkout rather than whatever was last committed.
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);

  [rich, emptyMock, unwiredMock, degradedMock] = await Promise.all([
    startMock({}),
    // Day one is empty in every store the reading surface reads, and the
    // surface reads four of them: the frontier behind a record, the ranked
    // set behind Read and Decide's backlog, and what is in flight behind
    // Watch. Emptying one and leaving the others rich is a deployment that
    // does not exist, and it would let three of the four empty states go
    // unrendered while the test still claimed to have checked day one.
    startMock({ MOCK_PHASEB: "empty", MOCK_EVALUATION: "empty", MOCK_WATCH: "idle" }),
    startMock({ MOCK_UNWIRED: "frontier,review,reality,search" }),
    // Both catalogs are short for the same reason, and each listing states it
    // in its own terms: the queue's read is partial, the ranked set's
    // projection is stale. A mock that degraded only one would leave the
    // other's notice unrendered.
    startMock({ MOCK_FLEET: "degraded", MOCK_EVALUATION: "degraded" }),
  ]);

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
  await page.setViewport(WIDE);
  page.on("request", (request) => {
    requestURLs.push(request.url());
  });
  // The consequential controls confirm through a native dialog; accepting is
  // this suite's default because the flows under test are the confirmed ones.
  page.on("dialog", (dialog) => void dialog.accept());
});

afterAll(async () => {
  await browser?.close();
  rich?.process.kill();
  emptyMock?.process.kill();
  unwiredMock?.process.kill();
  degradedMock?.process.kill();
});

test.skipIf(!chrome)("every area of the reading surface renders against the mock", async () => {
  // Home: the feed, every kind in one list. What the feed does with that list
  // is browser/feed.test.ts's subject; what is asserted here is that the front
  // page is it.
  await open("");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.feed-list > li.feed-row").length >= 4,
    { timeout: 15_000 },
  );

  // The mod queue, which is the same list narrowed to what awaits a ruling and
  // ordered by what is next. Every row on it says why it is there and offers
  // the acts §8.7 gives a row — which is the whole of what the second surface
  // used to be.
  await open("?needs=me");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.feed-list > li.feed-row[data-awaiting]").length >= 4,
    { timeout: 15_000 },
  );
  const waiting = await page.evaluate(() => ({
    reasons: document.querySelectorAll("ol.feed-list .feed-why").length,
    rulings: document.querySelectorAll("ol.feed-list [data-ruling='accept']").length,
  }));
  expect(waiting.reasons).toBeGreaterThanOrEqual(4);
  expect(waiting.rulings).toBeGreaterThan(0);

  // Watch: what Babel is doing and what it cost. The receipt strip carries no
  // publication state and names no machine: a receipt is read for what the run
  // did, and where its records replicated to is plumbing the reading path
  // dropped.
  await open("watch");
  await page.waitForFunction(
    () => document.querySelectorAll("table.runs-table tbody tr").length > 0,
    { timeout: 15_000 },
  );
  const watching = await page.evaluate(() => document.body.innerText);
  for (const word of ["pending-sync", "committed", "demo-laptop"]) {
    expect(watching).not.toContain(word);
  }

  // Ask: what Babel needs from the operator. The inbox that used to lead this
  // section is the feed filtered to the questions, so the ledger's own listing
  // is where the questions are read as a body of work.
  await open("ask/questions");
  await visible("nightly reconciliation");
});

test.skipIf(!chrome)("an empty deployment reads as a state, not a bug", async () => {
  // Each area answers for itself, and each says the thing that is true of it
  // rather than the same shrug four times. What must never appear on a
  // deployment with nothing in it is a filter's excuse: "nothing matches" is
  // a statement about controls the operator has not touched.
  // Arriving is arriving under the operator's own filter, so day one says
  // what is true of it — nothing is waiting — and the sentence about the
  // whole corpus is one gesture away, where it is also true.
  await open("", emptyMock?.base);
  await visible("Nothing is waiting on you");
  expect(await page.evaluate(() => document.body.innerText)).not.toContain(
    "a statement about the filters",
  );

  await open("?needs=all", emptyMock?.base);
  await visible("Babel has not posted anything yet");

  await open("ask/questions", emptyMock?.base);
  await visible("Babel has asked nothing yet");

  await open("watch", emptyMock?.base);
  await visible("Nothing is running");
});

// §5.2: sorting never deletes a record, so one that was ruled against stays
// reachable and says plainly what was decided. The record page is where that
// is visible now, because the standing travels with the record rather than
// with a listing the reader has to filter.
//
// This record was rejected and then given a refinement, which is the harder
// case: the standing it wears is the composite one, and the rejection has to
// still be legible in it rather than being rounded off to "in progress".
test.skipIf(!chrome)("a record ruled against stays reachable and says so", async () => {
  await open("r/hyp_lens-overlap");
  await page.waitForSelector("details.peel", { timeout: 15_000 });
  const standing = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".heading-badges .badge")).map((badge) => badge.textContent));
  expect(standing).toContain("refine-requested");
  const claim = await page.evaluate(() => document.body.innerText);
  expect(claim).toMatch(/rejected/iu);

  // The ruling that rejected it, with the reviewer's own words, at depth 4.
  const reception = await openPeel("The reception");
  expect(reception).toContain("reject");
  expect(reception).toContain("The overlap is real but retiring a draft lens is premature.");
});

// The centrepiece: one record, five depths, opened in place. Nothing here
// navigates to see more of the same object, and nothing below depth 5 shows an
// identifier — a person deciding whether a suggestion is right has no use for
// its digest.
test.skipIf(!chrome)("a record peels to five depths without leaving the page", async () => {
  await open("r/pro_criteria-template");
  await page.waitForSelector("details.peel", { timeout: 15_000 });

  const titles = await peelTitles();
  expect(titles[0]).toBe("The claim");
  expect(titles).toContain("The case");
  // The count rides the collapsed summary, so a reader knows what opening one
  // will cost before he opens it. It is a separate element beside the title,
  // which is why this matches the pair rather than a sentence.
  expect(titles.some((title) => /^The evidence\s*\d+$/u.test(title))).toBe(true);
  expect(titles.at(-1)).toBe("The machinery");

  // A depth's own title is the prefix of its summary; the count that follows
  // it is a separate element, so the comparison is on the prefix and not on a
  // fixed slice of the concatenation.
  const foldedAtFirst = await page.evaluate(() =>
    Array.from(document.querySelectorAll("details.peel")).map((peel) => ({
      title: (peel.querySelector("summary")?.textContent ?? "").replace(/\s+/gu, " ").trim(),
      open: (peel as HTMLDetailsElement).open,
    })));
  const openness = (prefix: string) =>
    foldedAtFirst.find((peel) => peel.title.startsWith(prefix))?.open;
  // What the reader came for is already open; what costs him attention is not.
  expect(openness("The claim")).toBe(true);
  expect(openness("The case")).toBe(true);
  expect(openness("The evidence")).toBe(false);
  expect(openness("The machinery")).toBe(false);

  // Depth 2 is the argument in prose, under the questions a reader is asking
  // rather than the schema's field names. The eyebrows are matched in
  // whatever case the stylesheet sets them in — it sets them in small
  // capitals, and what this asserts is the question rather than the type
  // case. The last line is the general form of the rule: an identifier from
  // the wire — verification_criteria, open_questions, impact_scope — would
  // carry an underscore, and nothing a reader is meant to read does.
  const substance = await page.evaluate(() => {
    const peel = Array.from(document.querySelectorAll("details.peel")).find((depth) =>
      (depth.querySelector("summary")?.textContent ?? "").startsWith("The case"));
    return (peel as HTMLElement).innerText;
  });
  expect(substance).toMatch(/the problem/iu);
  expect(substance).toMatch(/what it proposes/iu);
  expect(substance).toMatch(/what is still unanswered/iu);
  expect(substance).not.toMatch(/[a-z]+_[a-z]+/u);

  // Depth 3 says which side of the claim each excerpt is on before quoting it:
  // §4.5 requires a proposal to state its conflicting material, and an
  // interface that rendered it like support would invert the record.
  const evidence = await openPeel("The evidence");
  expect(evidence).toMatch(/supports the claim/iu);
  expect(evidence).toMatch(/conflicts with the claim/iu);
  // Counter-evidence is marked on the item itself, so a reader scanning the
  // list sees which way an excerpt cuts without reading the label.
  const marked = await page.evaluate(() =>
    document.querySelectorAll(".record-evidence .record-counter").length);
  expect(marked).toBe(1);

  // The citation opens the transcript at the cited line, and the link is the
  // only place the session it came from is named.
  const citation = await page.evaluate(() =>
    document.querySelector(".peel-cite a")?.getAttribute("href"));
  expect(citation).toMatch(/^#\/sessions\/.+\?event=\d+$/u);

  // All five depths are present because this record holds something for each
  // of them, the reception included: the operator's own stance is in it,
  // read-only, which is what §4.12's append means when a write is retired —
  // §8.7 took the arrows away and what they recorded stays readable.
  const five = await peelTitles();
  expect(five).toHaveLength(5);
  expect(five[0]).toBe("The claim");
  expect(five[1]).toBe("The case");
  expect(five[2].startsWith("The evidence")).toBe(true);
  expect(five[3].startsWith("The reception")).toBe(true);
  expect(five[4]).toBe("The machinery");

  // No identifier above depth 5, with every depth but the machinery open.
  //
  // The scope is the depths, which is what §8.6's rule is about. The post
  // header and the thread are §8.7's surfaces and name the run that wrote
  // what they show — "a run is the author of what it wrote: its name on a
  // post or a vote reaches its run page" — so they are removed here and
  // asserted where they belong: the header's author link below, and the
  // thread's in its own suite.
  await openPeel("The reception");
  const above = await page.evaluate(() => {
    const clone = document.querySelector("main")?.cloneNode(true) as HTMLElement;
    const machinery = Array.from(clone.querySelectorAll("details.peel")).find((peel) =>
      (peel.querySelector("summary")?.textContent ?? "").startsWith("The machinery"));
    machinery?.remove();
    clone.querySelector(".record-post")?.remove();
    clone.querySelector(".record-thread")?.remove();
    return clone.innerText;
  });
  expect(above).not.toMatch(/\b(?:hyp|obs|fnd|pro|run|rev|rcp)[_-][0-9a-z]{4,}/u);

  // The post names its author, and the name is a link to that run's page
  // rather than an identifier a reader has to carry somewhere himself.
  const author = await page.evaluate(() => {
    const link = document.querySelector<HTMLAnchorElement>(".record-post .record-post-run");
    return { text: link?.textContent ?? "", href: link?.getAttribute("href") ?? "" };
  });
  expect(author.text).toBe("run_challenge-08");
  expect(author.href).toBe("#/watch/runs/run_challenge-08");

  // And depth 5 is where they all are.
  const machinery = await openPeel("The machinery");
  expect(machinery).toContain("pro_criteria-template");
  expect(machinery).toContain("run_challenge-08");
});

// A bare claim is a real record. The peel shows what the record holds and
// invents nothing: a hypothesis has no case and cites nothing of its own, so
// those depths are absent rather than present and empty.
test.skipIf(!chrome)("a record with no case shows no case, and no zero", async () => {
  await open("r/hyp_unverified-closures");
  await page.waitForSelector("details.peel", { timeout: 15_000 });

  const titles = await peelTitles();
  expect(titles).not.toContain("The case");
  expect(titles.some((title) => title.startsWith("The evidence"))).toBe(false);
  const body = await page.evaluate(() => document.body.innerText);
  expect(body).not.toContain("The problem");
  expect(body).not.toContain("0 excerpts");

  // §4.12 separation by attribution: the reviewers' tally is the runs' own and
  // says so, the operator's stance is a different block, and nothing adds one
  // to the other.
  const reception = await openPeel("The reception");
  expect(reception).toContain("Reviewer 1");
  // The tally is Babel's own runs, said in those words, and the disagreement
  // inside it is named rather than averaged into one verdict.
  expect(reception).toContain("never counting your stance");
  expect(reception).toMatch(/\d+ support, \d+ oppose, \d+ unsure/u);
  expect(reception).toMatch(/contested/iu);
  // A run is named by what it was asked and how it answered, not by its id:
  // there is no honest display name for a run, so the id stays at depth 5.
  expect(reception).not.toMatch(/run[_-][0-9a-z]{4,}/u);
  const machinery = await openPeel("The machinery");
  expect(machinery).toContain("Reviewer 1");
  expect(machinery).toMatch(/run[_-][0-9a-z]/u);
});

test.skipIf(!chrome)("counter-evidence renders where the claim is", async () => {
  await open("r/fnd_conflicting-evidence");
  await page.waitForSelector("details.peel", { timeout: 15_000 });
  const evidence = await openPeel("The evidence");
  expect(evidence).toMatch(/counter-evidence/iu);
  // Both of this finding's citations are counter-evidence, and both are marked
  // on the item rather than only in a sentence: the finding's own conflicting
  // material is the reason to read it, so it has to survive a reader who is
  // scanning the list instead of reading it.
  const counter = await page.evaluate(() => ({
    marked: document.querySelectorAll(".record-evidence .record-counter").length,
    items: document.querySelectorAll(".record-evidence > li").length,
  }));
  expect(counter.marked).toBe(2);
  expect(counter.marked).toBe(counter.items);

  await open("r/pro_criteria-template");
  await visible("confounded by task size");
});

// The operator's voice on the reading surface, at the point of reading.
//
// §8.7 settled what it is: "Babel votes; the operator rules" — "me voting is
// a subpar concept, since I would rather just triage the idea at this point"
// — so the arrows are gone from the row and from the post, and what he can do
// to a record is the append-only ruling he already had. Two things have to be
// true at once, and this is the surface where they meet: no control offers him
// a vote anywhere, and the stances he recorded before the write was retired
// are still readable, because §4.12 appends and retiring a write does not
// delete what it wrote.
//
// The ruling itself is driven from a feed row, which is where the operator
// triages: what is asserted here is that the reading surface has one voice
// for him and that it is confirmed before it lands. The feed's own suite owns
// the row's mechanics; this owns "no vote survives anywhere on the surface".
test.skipIf(!chrome)("the operator rules rather than votes, and his retired stances stay readable", async () => {
  await open("");
  const row = "li.feed-row[data-post='pro_criteria-template']";
  await page.waitForSelector(`${row} [data-ruling='accept']`, { timeout: 15_000 });

  // Nothing on the row is a vote: no arrow, no stance, and the score beside
  // the claim is a figure rather than a control.
  const votes = await page.evaluate((selector: string) => {
    const item = document.querySelector(selector) as HTMLElement;
    return {
      stances: item.querySelectorAll("[data-stance], .vote-up, .vote-down").length,
      score: item.querySelector(".feed-score")?.tagName ?? "",
      breakdown: item.querySelector(".feed-score")?.getAttribute("title") ?? "",
    };
  }, row);
  expect(votes.stances).toBe(0);
  expect(votes.score).toBe("SPAN");
  expect(votes.breakdown).toMatch(/Babel's reviewers/u);

  // The ruling is confirmed before it is recorded — it is an appended,
  // attributed event that cannot be edited — and the row then says what was
  // done in place of what could be done.
  const decides: string[] = [];
  const watch = (request: { url: () => string; method: () => string }) => {
    if (request.method() === "POST" && request.url().includes("/api/review/decide")) {
      decides.push(request.url());
    }
  };
  page.on("request", watch);
  try {
    await page.click(`${row} [data-ruling='accept']`);
    await page.waitForSelector(`${row} .record-confirm`, { timeout: 15_000 });
    expect(decides).toEqual([]);
    await page.click(`${row} .record-confirm button[type='submit']`);
    await page.waitForFunction(
      (selector: string) =>
        (document.querySelector(`${selector} .feed-acted`)?.textContent ?? "").includes("accepted"),
      { timeout: 15_000 },
      row,
    );
    expect(decides).toHaveLength(1);
  } finally {
    page.off("request", watch);
  }

  // On the record itself: the same absence, and the stance he recorded while
  // the surface took stances, rendered read-only at depth four with what it
  // replaced still under it.
  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-post .record-score", { timeout: 15_000 });
  expect(await page.$(".record-post [data-stance], .record-post .vote-up")).toBeNull();
  expect(await page.$eval(".record-post .record-post-note",
    (note) => (note as HTMLElement).innerText)).toMatch(/Babel's reviewers/iu);
  const reception = await openPeel("The reception");
  expect(reception).toContain("What you recorded earlier");
  expect(reception).toContain("agree");
  expect(reception).toContain("Before that:");
  expect(reception).toContain("A stance decided nothing and is no longer recorded");
  // And it is read-only: the depth that shows it offers no control that would
  // record another one.
  expect(await page.evaluate(() =>
    document.querySelectorAll("details.peel [data-stance]").length)).toBe(0);
});

test.skipIf(!chrome)("a disposition appends through the API and reads back", async () => {
  await open("r/hyp_hostile-content");
  await page.waitForSelector("details.peel", { timeout: 15_000 });

  // The ruling is five buttons and, on the one he presses, one sentence: the
  // reader has already decided by the time he reaches the bar, and the
  // confirmation's job is to take the decision rather than present the
  // options again.
  await page.waitForSelector("[data-ruling=reject]", { timeout: 15_000 });
  await page.click("[data-ruling=reject]");
  await page.waitForSelector(".record-confirm textarea", { timeout: 15_000 });
  const areas = await page.$$(".record-confirm textarea");
  await areas[0].type("Synthetic reviewer note");
  // The guidance is folded, because it is the one field that is not about
  // this decision. A reader who wants it opens it, so the test does too.
  await page.click(".record-confirm details summary");
  await areas[1].type("Synthetic attributed guidance");
  await page.click(".record-confirm button[type=submit]");

  // The standing is part of the record, so recording a ruling moves it on the
  // page that recorded it rather than on a listing the operator has to revisit.
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".heading-badges .badge"))
      .some((badge) => badge.textContent === "rejected"),
    { timeout: 15_000 },
  );
  const reception = await openPeel("The reception");
  expect(reception).toContain("reject");
  expect(reception).toContain("Synthetic reviewer note");
});

test.skipIf(!chrome)("hostile fixtures render inert everywhere they appear", async () => {
  // The feed is in this list because §8.7 put every record on the front page:
  // a post's title is a line a model wrote, so the least trusted string in the
  // corpus now renders on the first screen a reader sees. It is opened
  // narrowed to the findings so the hostile row is certainly on the page
  // rather than wherever the ranking put it. r/pro_criteria-template is here
  // for the same reason one depth down: a reviewer's comment is prose a model
  // wrote, and the thread is where it is read. `watch` is in the list because
  // a search hit renders archive bytes: the least trusted string of all,
  // authored by whatever wrote the transcript.
  const hostileRoutes = [
    "?kind=finding",
    "r/hyp_hostile-content",
    "r/obs_hostile",
    "r/pro_criteria-template",
    "watch",
  ];
  for (const route of hostileRoutes) {
    await open(route);
    await page.waitForFunction(
      () => document.querySelector("main .page") !== null
        && document.querySelector(".state-note .spinner") === null,
      { timeout: 15_000 },
    );
    const state = await page.evaluate(() => ({
      pwned: String(Reflect.get(globalThis, "__babel_pwned")),
      injectedImage: document.querySelector("img[src='x']") !== null,
      scriptURL: Array.from(document.querySelectorAll("a"))
        .some((anchor) => anchor.href.startsWith("javascript:")),
    }));
    expect(state.pwned).toBe("undefined");
    expect(state.injectedImage).toBe(false);
    expect(state.scriptURL).toBe(false);
  }

  // The literal markup is visible as text inside its quoted frame: escaped,
  // not swallowed — a reader sees exactly what the model emitted.
  await open("r/hyp_hostile-content");
  await visible("Model suggests");
  const literal = await page.evaluate(
    (needle: string) => document.body.innerText.includes(needle),
    HOSTILE_HTML,
  );
  expect(literal).toBe(true);

  // Non-vacuity for the two surfaces §8.7 added: the markup is on screen as
  // text, in the row and in the thread, rather than being swallowed by the
  // escaping that makes it safe.
  await open("?kind=finding");
  await page.waitForSelector("ol.feed-list a.feed-claim", { timeout: 15_000 });
  const inRow = await page.evaluate((needle: string) =>
    Array.from(document.querySelectorAll("a.feed-claim"))
      .some((claim) => (claim as HTMLElement).innerText.includes(needle)),
    HOSTILE_HTML);
  expect(inRow, "no feed row carries the hostile title fixture").toBe(true);

  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-thread-list .record-comment-text", { timeout: 15_000 });
  const inThread = await page.evaluate((needle: string) =>
    Array.from(document.querySelectorAll(".record-comment-text"))
      .some((line) => (line as HTMLElement).innerText.includes(needle)),
    HOSTILE_HTML);
  expect(inThread, "no comment in the thread carries the hostile fixture").toBe(true);

  // The search hit carrying hostile transcript bytes is likewise inert. The
  // corpus search is behind a fold on Watch — it is the one thing on that page
  // the operator asks for rather than reads — so it is opened the way he opens
  // it before anything is typed into it.
  await open("watch");
  await page.waitForSelector("input[type=search]", { timeout: 15_000 });
  await page.evaluate(() => {
    // Open the fold by property rather than by a synthetic click on its
    // summary: the peel animates open over 220ms, and a pointer click on the
    // input during that wipe can land outside it on a slower machine.
    const fold = document.querySelector("input[type=search]")?.closest("details");
    if (fold) (fold as HTMLDetailsElement).open = true;
  });
  await page.waitForSelector("input[type=search]", { visible: true, timeout: 15_000 });
  await page.$eval("input[type=search]", (input) => (input as HTMLInputElement).focus());
  await page.keyboard.type("hostile");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".hit-list .hit-entry", { timeout: 15_000 });
  const hit = await page.evaluate(() => ({
    pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    injected: document.querySelector("main img, main script") !== null,
    // Non-vacuity: the untrusted bytes really are on screen, escaped.
    quoted: document.body.innerText.includes("window.__babel_pwned=1"),
  }));
  expect(hit.pwned).toBe("undefined");
  expect(hit.injected).toBe(false);
  expect(hit.quoted).toBe(true);
});

test.skipIf(!chrome)("keyboard navigation reaches every control", async () => {
  // The feed: tabbing from the top of the document reaches the controls and
  // then the rows, and Enter on a row opens what that row points at. The walk
  // is bounded by the row it is looking for rather than by a step count,
  // because the number of controls above the list is a layout decision and not
  // this test's business.
  await open("");
  await page.waitForSelector("ol.feed-list a.feed-claim", { timeout: 15_000 });
  const walk: string[] = [];
  let onRow = false;
  for (let step = 0; step < 120 && !onRow; step += 1) {
    await page.keyboard.press("Tab");
    const [tag, chip, sort, row] = await page.evaluate(() => {
      const active = document.activeElement;
      return [
        active?.tagName ?? "",
        active?.getAttribute("data-chip") ?? "",
        active?.getAttribute("data-sort") ?? "",
        active?.classList.contains("feed-claim") ?? false,
      ] as [string, string, string, boolean];
    });
    walk.push(chip ? `chip:${chip}` : sort ? `sort:${sort}` : tag);
    onRow = row;
  }
  expect(onRow, `tabbing never reached a row: ${walk.join(" ")}`).toBe(true);
  // The ordering and the kinds are reachable on the way, so the one list can
  // be sorted and filtered without a pointer as well as read.
  expect(walk.some((entry) => entry.startsWith("chip:kind-"))).toBe(true);
  expect(walk.some((entry) => entry.startsWith("sort:"))).toBe(true);
  // Enter opens exactly what the focused row points at rather than "a record":
  // the feed carries the questions Babel asks beside the records it produced,
  // and those are answered on their own page.
  const opening = await page.evaluate(() =>
    (document.activeElement as HTMLAnchorElement).getAttribute("href") ?? "");
  expect(opening.length).toBeGreaterThan(2);
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    (want: string) => window.location.hash === want,
    { timeout: 15_000 },
    opening,
  );

  // The record page: every depth is a focusable disclosure, and the
  // operator's one voice — the permanent ruling in the rule bar — is
  // reachable in full without a pointer. There is no second voice to reach:
  // §8.7 retired the arrows, and the score beside the claim is a figure whose
  // breakdown is on the element itself for a screen reader to read in place.
  await open("r/pro_criteria-template");
  await page.waitForSelector(".record-post .record-score", { timeout: 15_000 });
  // Whichever depths this record has — a record holds only the ones it has
  // something for — every one of them must be openable without a pointer.
  const depths = await peelTitles();
  expect(depths.length).toBeGreaterThan(2);
  const reached = await tabThrough(60);
  for (const depth of depths) {
    expect(
      reached.includes(`SUMMARY:${depth}`),
      `${depth} is not reachable by keyboard`,
    ).toBe(true);
  }
  expect(
    await page.$eval(".record-post .record-score", (score) => score.getAttribute("aria-label") ?? ""),
    "the score carries no breakdown for a reader who cannot hover",
  ).toMatch(/Babel's reviewers/u);
  for (const ruling of ["accept", "reject", "defer", "duplicate", "reopen"]) {
    expect(reached, `the ${ruling} control is outside the tab order`).toContain(`BAR:ruling=${ruling}`);
  }

  // Pressing a ruling by keyboard opens the confirmation, and what it asks for
  // is reachable the same way: a ruling that could be started without a
  // pointer and not finished would be worse than one that could not be
  // started. The walk wraps the whole ring, which the thread's own box, the
  // arrows and every comment's author link made longer than it was.
  await page.focus("[data-ruling=defer]");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".record-confirm textarea", { timeout: 15_000 });
  const confirming = await tabThrough(120);
  expect(confirming).toContain("TEXTAREA");
  expect(confirming.some((entry) => entry.startsWith("SUBMIT"))).toBe(true);

  // The palette is keyboard-only by design: ⌘K opens it anywhere, it takes the
  // caret, and Escape gives the page back.
  await open("r/pro_criteria-template");
  await page.keyboard.down("Meta");
  await page.keyboard.press("KeyK");
  await page.keyboard.up("Meta");
  await page.waitForSelector("[role=dialog]", { timeout: 15_000 });
  expect(await page.evaluate(() => document.activeElement?.tagName)).toBe("INPUT");
  await page.keyboard.type("watch");
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => window.location.hash === "#/watch", { timeout: 15_000 });
  expect(await page.$("[role=dialog]")).toBeNull();
});

// tabThrough walks the tab ring and names what it landed on, in the terms the
// assertions are written in: which depth a summary opens, and which control of
// the rule bar a button is. The ring wraps, so a fixed number of presses covers
// a page whose control count is not this test's business.
//
// There is no vote token, because there is no vote: §8.7 retired the arrows
// and the operator's acts on a record are the rulings in the bar.
async function tabThrough(steps: number): Promise<string[]> {
  const reached: string[] = [];
  for (let step = 0; step < steps; step += 1) {
    await page.keyboard.press("Tab");
    reached.push(await page.evaluate(() => {
      const active = document.activeElement;
      if (!active) return "";
      const ruling = active.getAttribute("data-ruling");
      if (ruling && active.closest(".rule-bar")) return `BAR:ruling=${ruling}`;
      if (active.tagName === "SUMMARY") {
        return `SUMMARY:${(active.textContent ?? "").replace(/\s+/gu, " ").trim()}`;
      }
      if (active.getAttribute("type") === "submit") return `SUBMIT:${(active.textContent ?? "").trim()}`;
      return active.tagName;
    }));
  }
  return reached;
}

test.skipIf(!chrome)("narrow and wide viewports lay out without overflow", async () => {
  for (const viewport of [WIDE, NARROW]) {
    await page.setViewport(viewport);
    for (const route of ROUTES) {
      await open(route);
      // Rendering settles asynchronously; the loaded page is signalled by
      // its blocking spinner leaving the DOM, not by a guessed delay.
      await page.waitForFunction(
        () => document.querySelector("main .page") !== null
          && document.querySelector(".state-note .spinner") === null,
        { timeout: 15_000 },
      );
      const width = await page.evaluate(() => ({
        document: document.documentElement.scrollWidth,
        body: document.body.scrollWidth,
        inner: window.innerWidth,
      }));
      expect(`${route}@${viewport.width}:${width.document <= width.inner + 1}`)
        .toBe(`${route}@${viewport.width}:true`);
      expect(width.body).toBeLessThanOrEqual(width.inner + 1);
    }
  }
  await page.setViewport(WIDE);
});

test.skipIf(!chrome)("plan acceptance is explicit and flips proposed to applied", async () => {
  // The plan is read where the question is, now that the ledger's inbox is the
  // feed: a plan-ready question carries the interpreter's plan on its own page.
  await open("ask/questions/qst_focus-policy");
  await page.waitForSelector(".accept-panel button", { timeout: 15_000 });
  await visible("proposed — nothing applied yet");
  // Beside the control, before it is pressed: that pressing it is the whole
  // act, and that what it will change has not been changed yet. The sentence
  // is read off the panel rather than the page, because a caveat about an
  // irreversible control is worth nothing anywhere else.
  const offered = await page.evaluate(() =>
    (document.querySelector(".accept-panel") as HTMLElement).innerText);
  expect(offered).toMatch(/explicit act/iu);
  expect(offered).toMatch(/on acceptance/iu);

  await page.click(".accept-panel button");
  await visible("Plan accepted and applied atomically");
  // The confirmation message and the plan's own standing are separate renders,
  // so waiting for the message does not order the flip: await the flip this
  // test is about. One that never happens still fails, by timeout naming the
  // condition, rather than by being read mid-render.
  await page.waitForFunction(
    () => !document.body.innerText.includes("proposed — nothing applied yet"),
    { timeout: 15_000 },
  );
  const applied = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".badge")).filter((badge) => badge.textContent === "applied").length);
  expect(applied).toBe(2);

  // The accepted assertion is now a fact on the entity, visibly active —
  // and the plan-proposed fixture fact remains visibly "proposed".
  await open("ask/entities/ent_atlas");
  await visible("Applied by accepting the synthetic interpreter plan");
});

test.skipIf(!chrome)("an answer records verbatim and moves the question state", async () => {
  await open("ask/questions/qst_long-entity");
  await page.waitForSelector(".answer-form textarea", { timeout: 15_000 });
  await page.type(".answer-form textarea", "Yes, they are the same synthetic service.");
  await page.click(".answer-form button[type=submit]");
  await page.waitForFunction(
    () => document.body.innerText.includes("Yes, they are the same synthetic service."),
    { timeout: 15_000 },
  );
  await visible("answered-uninterpreted");
});

test.skipIf(!chrome)("record content never enters a request URL or the location hash", async () => {
  // By this point the suite has browsed every area, read records at every
  // depth, searched, stated a stance, ruled, accepted and answered. The only
  // things allowed in URLs are routes, identifiers, and the operator's own
  // explicit search query.
  const forbidden = [
    "regressions discovered",
    "Synthetic reviewer note",
    "Synthetic attributed guidance",
    "Worth trying before the release.",
    "the same synthetic service",
    HOSTILE_HTML,
    UNBROKEN_TOKEN.slice(0, 64),
  ];
  expect(requestURLs.length).toBeGreaterThan(0);
  for (const url of requestURLs) {
    const decoded = decodeURIComponent(url);
    for (const fragment of forbidden) {
      expect(decoded).not.toContain(fragment);
    }
  }
  const hash = await page.evaluate(() => window.location.hash);
  for (const fragment of forbidden) {
    expect(decodeURIComponent(hash)).not.toContain(fragment);
  }
});

test.skipIf(!chrome)("a refusal banner is scoped to the route that earned it", async () => {
  // A launch whose ledger could not be opened still serves the feed, so the
  // front page works while the ledger's own destinations refuse. What must not
  // happen is the refusal following him: the banner reports the failure of a
  // request, and once he has navigated to a page that loaded perfectly, a
  // banner still accusing a service that page never called is telling him
  // something false about what he is looking at.
  //
  // The refusing route is the ledger's questions rather than the mod queue:
  // /queue is a redirect to the front page now (§8.7 made the queue the feed's
  // own filter), so a page that refuses has to be a page that actually calls
  // the unwired service.
  //
  // Navigation here is a click or a hash change rather than open(),
  // deliberately. open() reloads, which rebuilds the module holding the error,
  // so a reload would hide exactly the defect this test exists to catch.
  const base = unwiredMock?.base;
  if (!base) throw new Error("the unwired mock is not running");
  const refusing = "#/ask/questions";

  await page.goto(`${base}/${refusing}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
  await visible("is not available in this session");

  // Every frame from the click onwards is inspected, not just the state after
  // the navigation settled. Reading once afterwards makes this a race: clearing
  // the banner in an effect keyed on the path let the destination paint one
  // frame carrying the previous route's refusal, and a single read caught it
  // only when something else on the page happened to be slow. One frame of a
  // page accusing another page of failing is the falsehood, so no frame may
  // hold it.
  await page.evaluate(() => {
    Reflect.set(globalThis, "__babel_both_frames", 0);
    const watch = () => {
      if (
        document.querySelector("ol.feed-list") !== null
        && document.body.innerText.includes("is not available in this session")
      ) {
        Reflect.set(globalThis, "__babel_both_frames", Number(Reflect.get(globalThis, "__babel_both_frames")) + 1);
      }
      requestAnimationFrame(watch);
    };
    requestAnimationFrame(watch);
  });
  await page.click('nav[aria-label="Primary navigation"] a[href="#/"]');
  await page.waitForFunction(
    () => document.querySelector("ol.feed-list > li.feed-row") !== null,
    { timeout: 15_000 },
  );

  // The feed rendered its rows, so any banner still on screen belongs to a
  // route the operator has left.
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toContain("is not available in this session");
  expect(await page.evaluate(() => Reflect.get(globalThis, "__babel_both_frames"))).toBe(0);

  // And the refusal is still reported where it is true, so clearing on
  // navigation has not simply silenced it. The hash is set rather than
  // reloaded, for the reason above: this is a same-document navigation, which
  // is what the banner's own lifetime is about.
  await page.evaluate((route: string) => {
    window.location.hash = route;
  }, refusing);
  await visible("is not available in this session");
});

// A list that could not resolve the whole catalog is the one state a reader
// cannot check for himself: the rows look complete because rows always look
// complete. What must be on screen is that the list may be short -- and what
// must not be on screen, here least of all, is an explanation in terms of
// machines and publication state. The catalog is one body of work; which
// computer holds what is not a question this interface asks or answers.
//
// The list is the feed. The mod queue that used to carry this notice is the
// feed's own filter (§8.7), so the sentence is the one the feed serves on
// every read — internal/web/feed.go puts the catalog's own wording in the
// response's `notice`, and the front page renders it above the rows rather
// than in place of them.
test.skipIf(!chrome)("a partial catalog read says so, and names no machine", async () => {
  const machines = ["this machine", "this host", "All hosts", "pending-sync", "unattributed", "demo-laptop", "build-server"];

  await open("?needs=all", degradedMock?.base);
  await page.waitForSelector(".feed-page .feed-notice", { timeout: 15_000 });
  const notice = await page.$eval(".feed-page .feed-notice",
    (node) => (node as HTMLElement).innerText);
  // The catalog is what it names, because the catalog is what an operator can
  // check; the failure and the machine behind it are not his business.
  expect(notice).toMatch(/shared catalog could not be reached/u);
  for (const word of machines) expect(notice).not.toContain(word);

  // The records still render in full: a partial read costs the rows it could
  // not reach and nothing else.
  const rows = await page.evaluate(() =>
    document.querySelectorAll("ol.feed-list > li.feed-row").length);
  expect(rows).toBeGreaterThan(0);

  // And the rows themselves explain nothing in terms of machines either: the
  // notice is the whole of what this state adds to the page.
  const listed_ = await page.evaluate(() =>
    Array.from(document.querySelectorAll("ol.feed-list > li.feed-row"))
      .map((row) => (row as HTMLElement).innerText)
      .join("\n"));
  for (const word of machines) expect(listed_).not.toContain(word);
});
