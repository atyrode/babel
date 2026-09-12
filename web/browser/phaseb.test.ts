// Browser acceptance for the reading surface: Decide, Read, Watch, Ask and the
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
// evaluation and per-kind detail pages redirect there. The assertions that went
// with those pages went with them rather than being re-pinned to new wording —
// what is asserted below is what a reader can see and do. The redirect table
// itself is enumerated once, in shell.test.ts, against App.tsx's routes; a
// second partial copy here only made a dropped entry easier to miss.
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
  covers: "the reading surface -- Decide, Read, Watch, Ask and the record page -- in a browser",
  unverified: [
    "that every area renders against the mock at all, and that an empty deployment reads as a state rather than as a bug",
    "that one record peels to five depths in place, that an absent section is absent rather than empty, and that no identifier appears above the machinery",
    "that the hostile HTML, Markdown, URL and control fixtures render inert: no script runs, no markup is injected, and the literal markup stays visible as escaped text",
    "that every control is reachable by keyboard — every depth, the rule bar's stances and rulings, the confirmation they open, and the palette — and that no route overflows at either 390px or 1440px",
    "that an operator's stance records and reverses, that recording a disposition persists and reads back, and that accepting a plan and answering a question are explicit acts",
    "that no record content reaches a request URL or the location hash",
    "that a listing whose catalog read came back partial says the list may be incomplete, in terms of the list",
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
  "read",
  "read?kind=hypothesis",
  "watch",
  "ask",
  "ask/questions",
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
  // Decide: the queue, mixed kinds, with the figures above it.
  await open("");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.decide-queue > li.decide-row").length >= 4,
    { timeout: 15_000 },
  );
  // Three at minimum — what is waiting, what is asked, what changed. The
  // figures about this operator's own visit are conditional, so the count is
  // a floor rather than an equality.
  const figures = await page.evaluate(() => document.querySelectorAll(".decide-stat").length);
  expect(figures).toBeGreaterThanOrEqual(3);

  // Read: one listing of output, filtered by kind rather than split into four
  // pages. More than this machine's own six hypotheses, because a listing
  // reads the deployment and not one computer's share of it: the rows the
  // catalog merged are in the same list as the rows held here.
  await open("read?kind=hypothesis");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.read-list > li.read-row").length >= 9,
    { timeout: 15_000 },
  );

  // Choosing a kind narrows the one list rather than opening another page, so
  // what has to hold is that every row it leaves is of that kind. Naming one
  // record here would pin the fixture's ranking instead.
  await open("read?kind=finding");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.read-list > li.read-row").length > 0,
    { timeout: 15_000 },
  );
  const listed = await page.evaluate(() =>
    Array.from(document.querySelectorAll("ol.read-list > li.read-row"))
      .map((row) => (row as HTMLElement).innerText));
  for (const row of listed) expect(row).toContain("Finding");

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

  // Ask: what Babel needs from the operator.
  await open("ask");
  await visible("nightly reconciliation");
});

test.skipIf(!chrome)("an empty deployment reads as a state, not a bug", async () => {
  // Each area answers for itself, and each says the thing that is true of it
  // rather than the same shrug four times. What must never appear on a
  // deployment with nothing in it is a filter's excuse: "nothing matches" is
  // a statement about controls the operator has not touched.
  await open("", emptyMock?.base);
  await visible("Nothing awaits a decision");

  await open("read", emptyMock?.base);
  await visible("Babel has not found anything yet");
  expect(await page.evaluate(() => document.body.innerText)).not.toContain(
    "a statement about the filters",
  );

  await open("ask", emptyMock?.base);
  await visible("Nothing is waiting on you");

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
  // rather than the schema's field names. The last line is the general form of
  // that: an identifier from the wire — verification_criteria, open_questions,
  // impact_scope — would carry an underscore, and nothing a reader is meant to
  // read does.
  const substance = await page.evaluate(() => {
    const peel = Array.from(document.querySelectorAll("details.peel")).find((depth) =>
      (depth.querySelector("summary")?.textContent ?? "").startsWith("The case"));
    return (peel as HTMLElement).innerText;
  });
  expect(substance).toContain("The problem");
  expect(substance).toContain("What it proposes");
  expect(substance).toContain("What is still unanswered");
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

  // The fifth depth exists as soon as there is a reception to hold, and the
  // operator's own stance is one: this record has no reviewers, so stating a
  // stance is what brings depth 4 into being — which is the case a reader is
  // most likely to meet, and the one where an interface can most easily lose
  // the act it just took.
  await page.click(".rule-bar button[data-stance='agree']");
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll("details.peel > summary"))
      .some((summary) => (summary.textContent ?? "").startsWith("The reception")),
    { timeout: 15_000 },
  );
  const five = await peelTitles();
  expect(five).toHaveLength(5);
  expect(five[0]).toBe("The claim");
  expect(five[1]).toBe("The case");
  expect(five[2].startsWith("The evidence")).toBe(true);
  expect(five[3].startsWith("The reception")).toBe(true);
  expect(five[4]).toBe("The machinery");

  // No identifier above depth 5, with every depth but the machinery open.
  await openPeel("The reception");
  const above = await page.evaluate(() => {
    const clone = document.querySelector("main")?.cloneNode(true) as HTMLElement;
    const machinery = Array.from(clone.querySelectorAll("details.peel")).find((peel) =>
      (peel.querySelector("summary")?.textContent ?? "").startsWith("The machinery"));
    machinery?.remove();
    return clone.innerText;
  });
  expect(above).not.toMatch(/\b(?:hyp|obs|fnd|pro|run|rev|rcp)[_-][0-9a-z]{4,}/u);

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

// The operator's own voice, at the point of reading: cheap, attributed,
// reversible, and deciding nothing. The last part is what the interface has to
// say out loud, because the control sits beside the one that does decide.
//
// What it must also do is show him the act. His stance lands at depth four,
// which is folded, so a record Babel has no reviewers for used to acknowledge
// an agreement with nothing but a pressed button — and a projection a second
// behind the write took even that back on the next read.
test.skipIf(!chrome)("an operator's stance records, reverses, and keeps what it replaced", async () => {
  await open("r/pro_stdin-credential");
  await page.waitForSelector(".rule-bar button[data-stance]", { timeout: 15_000 });
  // The sentence beside the control, in whatever case the shell sets it in:
  // what §4.12 requires here is that the page says a reception decides
  // nothing, not that it says it in small capitals.
  await page.waitForFunction(
    () => /your take · decides nothing/iu.test(document.body.innerText),
    { timeout: 15_000 },
  );

  const click = (stance: string) => page.evaluate((value: string) => {
    document.querySelector<HTMLButtonElement>(`[data-stance="${value}"]`)?.click();
  }, stance);

  await click("agree");
  await page.waitForFunction(
    () => document.querySelector("[data-stance='agree']")?.getAttribute("aria-pressed") === "true",
    { timeout: 15_000 },
  );

  // The act is on screen without being looked for: the depth it landed in
  // opens itself, and its summary carries the stance for a reader who folds
  // it again.
  await visible("You: agree");
  const summary = await peelTitles();
  expect(summary.some((title) => title.includes("you: agree"))).toBe(true);
  await visible("A reception is attributed, reversible and decides nothing");

  // Reversing it appends: §4.12 is append-only, so the earlier stance stays
  // readable rather than being replaced by the later one.
  await click("unsure");
  await page.waitForFunction(
    () => document.querySelector("[data-stance='unsure']")?.getAttribute("aria-pressed") === "true",
    { timeout: 15_000 },
  );
  await open("r/pro_stdin-credential");
  const reception = await openPeel("The reception");
  expect(reception).toContain("You: unsure");
  expect(reception).toContain("Earlier you said");
  expect(reception).toContain("agree");
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
  // `watch` is in the list because a search hit renders archive bytes: the
  // least trusted string on any page here, authored by whatever wrote the
  // transcript.
  for (const route of ["read", "r/hyp_hostile-content", "r/obs_hostile", "ask", "watch"]) {
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

  // The search hit carrying hostile transcript bytes is likewise inert. The
  // corpus search is behind a fold on Watch — it is the one thing on that page
  // the operator asks for rather than reads — so it is opened the way he opens
  // it before anything is typed into it.
  await open("watch");
  await page.waitForSelector("input[type=search]", { timeout: 15_000 });
  await page.evaluate(() => {
    const fold = document.querySelector("input[type=search]")?.closest("details");
    if (fold && !(fold as HTMLDetailsElement).open) {
      fold.querySelector("summary")?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    }
  });
  await page.click("input[type=search]");
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
  // Read: tabbing from the top of the document reaches the facets and then the
  // rows, and Enter on a row opens that record. The walk is bounded by the row
  // it is looking for rather than by a step count, because the number of
  // controls above the list is a layout decision and not this test's business.
  await open("read");
  await page.waitForSelector("ol.read-list a.read-claim", { timeout: 15_000 });
  const walk: string[] = [];
  let onRow = false;
  for (let step = 0; step < 120 && !onRow; step += 1) {
    await page.keyboard.press("Tab");
    const [tag, chip, row] = await page.evaluate(() => {
      const active = document.activeElement;
      return [
        active?.tagName ?? "",
        active?.getAttribute("data-chip") ?? "",
        active?.classList.contains("read-claim") ?? false,
      ] as [string, string, boolean];
    });
    walk.push(chip ? `chip:${chip}` : tag);
    onRow = row;
  }
  expect(onRow, `tabbing never reached a row: ${walk.join(" ")}`).toBe(true);
  // The facets are reachable on the way, so the list can be filtered without a
  // pointer as well as read.
  expect(walk.some((entry) => entry.startsWith("chip:kind-"))).toBe(true);
  expect(walk.some((entry) => entry.startsWith("chip:lane-"))).toBe(true);
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    () => window.location.hash.startsWith("#/r/"),
    { timeout: 15_000 },
  );

  // The record page: every depth is a focusable disclosure, and the rule bar —
  // which carries both of the operator's voices, the cheap stance and the
  // permanent ruling — is reachable in full without a pointer.
  await open("r/pro_criteria-template");
  await page.waitForSelector(".rule-bar button[data-stance]", { timeout: 15_000 });
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
  for (const stance of ["agree", "disagree", "unsure"]) {
    expect(reached, `the ${stance} control is outside the tab order`).toContain(`BAR:stance=${stance}`);
  }
  for (const ruling of ["accept", "reject", "defer", "duplicate", "reopen"]) {
    expect(reached, `the ${ruling} control is outside the tab order`).toContain(`BAR:ruling=${ruling}`);
  }

  // Pressing a ruling by keyboard opens the confirmation, and what it asks for
  // is reachable the same way: a ruling that could be started without a
  // pointer and not finished would be worse than one that could not be started.
  await page.focus("[data-ruling=defer]");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".record-confirm textarea", { timeout: 15_000 });
  const confirming = await tabThrough(30);
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
async function tabThrough(steps: number): Promise<string[]> {
  const reached: string[] = [];
  for (let step = 0; step < steps; step += 1) {
    await page.keyboard.press("Tab");
    reached.push(await page.evaluate(() => {
      const active = document.activeElement;
      if (!active) return "";
      const stance = active.getAttribute("data-stance");
      const ruling = active.getAttribute("data-ruling");
      if (active.closest(".rule-bar")) {
        if (stance) return `BAR:stance=${stance}`;
        if (ruling) return `BAR:ruling=${ruling}`;
      }
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
  await open("ask");
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
  await open("ask");
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
  // A launch whose review and ledger services could not be opened still serves
  // the ranked set, so Read works while Decide refuses. What must not happen
  // is the refusal following him: the banner reports the failure of a request,
  // and once he has navigated to a page that loaded perfectly, a banner still
  // accusing a service that page never called is telling him something false
  // about what he is looking at.
  //
  // Navigation here is a click rather than open(), deliberately. open()
  // reloads, which rebuilds the module holding the error, so a reload would
  // hide exactly the defect this test exists to catch.
  const base = unwiredMock?.base;
  if (!base) throw new Error("the unwired mock is not running");

  await page.goto(`${base}/#/`, { waitUntil: "networkidle2" });
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
      const text = document.body.innerText;
      if (
        text.includes("What has Babel found?")
        && text.includes("is not available in this session")
      ) {
        Reflect.set(globalThis, "__babel_both_frames", Number(Reflect.get(globalThis, "__babel_both_frames")) + 1);
      }
      requestAnimationFrame(watch);
    };
    requestAnimationFrame(watch);
  });
  await page.click('nav[aria-label="Primary navigation"] a[href="#/read"]');
  await page.waitForFunction(
    () => document.querySelector("ol.read-list > li.read-row") !== null,
    { timeout: 15_000 },
  );

  // Read rendered its rows, so any banner still on screen belongs to a route
  // the operator has left.
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toContain("is not available in this session");
  expect(await page.evaluate(() => Reflect.get(globalThis, "__babel_both_frames"))).toBe(0);

  // And the refusal is still reported where it is true, so clearing on
  // navigation has not simply silenced it.
  await page.click('nav[aria-label="Primary navigation"] a[href="#/"]');
  await visible("is not available in this session");
});

// A listing that could not resolve the whole catalog is the one state a reader
// cannot check for himself: the rows look complete because rows always look
// complete. What must be on screen is that the list may be short -- and what
// must not be on screen, here least of all, is an explanation in terms of
// machines and publication state. The catalog is one body of work; which
// computer holds what is not a question this interface asks or answers.
test.skipIf(!chrome)("a partial catalog read says so, and names no machine", async () => {
  // Each listing states the shortfall in its own terms — the queue's read was
  // partial, the ranked set's projection is stale — so what is asserted is
  // that each one says it at all, and that neither explains it in terms of
  // machines.
  //
  // The words are looked for in the notice and the rows rather than in the
  // whole document: Decide's spend figure is honestly about this computer's
  // own receipts, and a whole-body search would read that as provenance
  // vocabulary and fail for the wrong reason.
  const machines = ["this machine", "this host", "All hosts", "pending-sync", "unattributed", "demo-laptop", "build-server"];
  const listingText = () => page.evaluate(() =>
    Array.from(document.querySelectorAll(".page > .state-note, .page > ol > li"))
      .map((node) => (node as HTMLElement).innerText)
      .join("\n"));

  await open("read", degradedMock?.base);
  await visible("This ordering is not current");
  const rows = await page.evaluate(() =>
    document.querySelectorAll("ol.read-list > li.read-row").length);
  // The records still render in full: a partial read costs the rows it could
  // not reach and nothing else.
  expect(rows).toBeGreaterThan(0);
  const listing = await listingText();
  expect(listing).toMatch(/could not be opened from the shared catalog/u);
  for (const word of machines) expect(listing).not.toContain(word);

  await open("", degradedMock?.base);
  await visible("This list may be incomplete");
  const queue = await listingText();
  expect(queue).toMatch(/Part of the catalog did not answer/u);
  for (const word of machines) expect(queue).not.toContain(word);
});
