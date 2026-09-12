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
// what is asserted below is what a reader can see and do.
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
    "that every control is reachable by keyboard, and that no route overflows at either 390px or 1440px",
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
  "watch?view=fleet",
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
    startMock({ MOCK_PHASEB: "empty" }),
    startMock({ MOCK_UNWIRED: "frontier,review,reality,search" }),
    startMock({ MOCK_FLEET: "degraded" }),
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
  // Decide: the queue, mixed kinds, with the three numbers above it.
  await open("");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.queue > li.queue-row").length >= 4,
    { timeout: 15_000 },
  );
  const tally = await page.evaluate(() => document.querySelectorAll(".tally .tally-item").length);
  expect(tally).toBe(3);

  // Read: one listing of output, filtered by kind rather than split into four
  // pages. Nine hypotheses — six this machine holds and the three the catalog
  // merged — because a listing reads the deployment and not one computer's
  // share of it.
  await open("read?kind=hypothesis");
  await page.waitForFunction(
    () => document.querySelectorAll("ol.output-list > li.output-row").length === 9,
    { timeout: 15_000 },
  );

  await open("read?kind=finding");
  await visible("Stated acceptance criteria correlate with verified closes");

  // Watch: what Babel is doing and what it cost. The receipt strip carries no
  // publication state and names no machine: a receipt is read for what the run
  // did, and where its records replicated to is plumbing the reading path
  // dropped.
  await open("watch");
  await visible("Outcome integrity and unresolved state");
  const watching = await page.evaluate(() => document.body.innerText);
  for (const word of ["pending-sync", "committed", "demo-laptop"]) {
    expect(watching).not.toContain(word);
  }

  // Ask: what Babel needs from the operator.
  await open("ask");
  await visible("nightly reconciliation");
});

test.skipIf(!chrome)("an empty deployment reads as a state, not a bug", async () => {
  await open("", emptyMock?.base);
  await visible("Nothing awaits a decision");

  await open("read", emptyMock?.base);
  await visible("Nothing has been recorded yet");

  await open("ask", emptyMock?.base);
  await visible("Nothing is waiting on you");

  await open("watch", emptyMock?.base);
  await visible("No exploration runs are recorded");
});

// §5.2: sorting never deletes a record, so a rejected one stays reachable and
// says plainly that it was rejected. The record page is where that is visible
// now, because the standing travels with the record rather than with a listing
// the reader has to filter.
test.skipIf(!chrome)("a rejected record stays reachable and visibly rejected", async () => {
  await open("r/hyp_lens-overlap");
  await page.waitForSelector("details.peel", { timeout: 15_000 });
  const standing = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".heading-badges .badge")).map((badge) => badge.textContent));
  expect(standing).toContain("rejected");
  await visible("Rejected. The record is kept, visibly.");

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
  // The count is on the collapsed sections, so a reader knows what opening
  // one will cost before he opens it.
  expect(titles.some((title) => title.startsWith("The evidence 2"))).toBe(true);
  expect(titles.at(-1)).toBe("The machinery");

  const openAtFirst = await page.evaluate(() =>
    Array.from(document.querySelectorAll("details.peel")).map((peel) =>
      `${(peel.querySelector("summary")?.textContent ?? "").slice(0, 12)}:${(peel as HTMLDetailsElement).open}`));
  expect(openAtFirst).toContain("The claim:true");
  expect(openAtFirst).toContain("The case:true");
  expect(openAtFirst).toContain("The evidence:false");
  expect(openAtFirst).toContain("The machinery:false");

  // Depth 2 is the argument in prose, under the questions a reader is asking
  // rather than the schema's field names.
  const substance = await page.evaluate(() => document.body.innerText);
  expect(substance).toContain("The problem");
  expect(substance).toContain("How you would know it worked");
  expect(substance).not.toContain("verification_criteria");

  // Depth 3 says which side of the claim each excerpt is on before quoting it:
  // §4.5 requires a proposal to state its conflicting material, and an
  // interface that rendered it like support would invert the record.
  const evidence = await openPeel("The evidence");
  expect(evidence).toContain("Supports the claim");
  expect(evidence).toContain("Conflicts with the claim");
  const marked = await page.evaluate(() => document.querySelectorAll(".peel-list .peel-counter").length);
  expect(marked).toBe(1);

  // The citation opens the transcript at the cited line, and the link is the
  // only place the session it came from is named.
  const citation = await page.evaluate(() =>
    document.querySelector(".peel-cite a")?.getAttribute("href"));
  expect(citation).toMatch(/^#\/sessions\/.+\?event=\d+$/u);

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
  expect(reception).toContain("never counting your stance");
  expect(reception).toContain("reviewers do not agree");
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
  expect(evidence).toContain("Counter-evidence");
  const state = await page.evaluate(() => ({
    counter: document.querySelectorAll(".peel-list .peel-counter").length,
    fallibility: document.querySelectorAll(".fallibility-note").length,
  }));
  // Both of this finding's citations are counter-evidence, and both are
  // marked: the finding's own conflicting material is the reason to read it.
  expect(state.counter).toBe(2);
  // §1's frame sits beside the claim, not on an about page.
  expect(state.fallibility).toBe(1);

  await open("r/pro_criteria-template");
  await visible("confounded by task size");
});

// The operator's own voice, at the point of reading: cheap, attributed,
// reversible, and deciding nothing. The last part is what the interface has to
// say out loud, because the control sits beside the one that does decide.
test.skipIf(!chrome)("an operator's stance records, reverses, and keeps what it replaced", async () => {
  await open("r/pro_stdin-credential");
  await page.waitForSelector(".peel-stance", { timeout: 15_000 });
  await visible("Your take. Decides nothing.");

  const click = (label: string) => page.evaluate((needle: string) => {
    Array.from(document.querySelectorAll<HTMLButtonElement>(".peel-stance"))
      .find((button) => button.textContent?.trim() === needle)
      ?.click();
  }, label);

  await page.evaluate(() => {
    const field = document.querySelector<HTMLTextAreaElement>(".peel-voice textarea");
    if (field) {
      field.value = "Worth trying before the release.";
      field.dispatchEvent(new Event("input", { bubbles: true }));
    }
  });
  await click("Agree");
  await page.waitForFunction(
    () => document.querySelector(".peel-stance[aria-pressed='true']")?.textContent?.trim() === "Agree",
    { timeout: 15_000 },
  );

  // It reads back as his, with his words, and the page says what it is.
  let reception = await openPeel("The reception");
  expect(reception).toContain("You said agree");
  expect(reception).toContain("Worth trying before the release.");
  expect(reception).toContain("A reception decides nothing");

  // Reversing it appends: §4.12 is append-only, so the earlier stance stays
  // readable rather than being replaced by the later one.
  await click("Unsure");
  await page.waitForFunction(
    () => document.querySelector(".peel-stance[aria-pressed='true']")?.textContent?.trim() === "Unsure",
    { timeout: 15_000 },
  );
  await open("r/pro_stdin-credential");
  reception = await openPeel("The reception");
  expect(reception).toContain("You said unsure");
  expect(reception).toContain("Earlier you said");
  expect(reception).toContain("Worth trying before the release.");
});

test.skipIf(!chrome)("a disposition appends through the API and reads back", async () => {
  await open("r/hyp_hostile-content");
  await page.waitForSelector("details.peel", { timeout: 15_000 });

  // The ruling control is a fold named by the act the record wants, so depth 1
  // stays the claim until the operator says he is ruling.
  await openPeel("Rule on this");
  await page.waitForSelector(".decide-form input[value=reject]", { timeout: 15_000 });
  await page.click(".decide-form input[value=reject]");
  const areas = await page.$$(".decide-form .decide-field textarea");
  await areas[0].type("Synthetic reviewer note");
  await areas[1].type("Synthetic attributed guidance");
  await page.click(".decide-form button[type=submit]");

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

  // The search hit carrying hostile transcript bytes is likewise inert.
  await open("watch");
  await page.type("input[type=search]", "hostile");
  await page.click("button[type=submit]");
  await page.waitForFunction(
    () => document.body.innerText.includes("hostile"),
    { timeout: 15_000 },
  );
  const hit = await page.evaluate(() => ({
    pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    injected: document.querySelector("main img, main script") !== null,
  }));
  expect(hit.pwned).toBe("undefined");
  expect(hit.injected).toBe(false);
});

test.skipIf(!chrome)("keyboard navigation reaches every control", async () => {
  // Read: the filters and the rows are tabbable, Enter opens the focused row
  // on the record page.
  await open("read");
  await page.waitForSelector("ol.output-list a.output-claim", { timeout: 15_000 });
  const walk: string[] = [];
  for (let step = 0; step < 40 && !walk.includes("A"); step += 1) {
    await page.keyboard.press("Tab");
    walk.push(await page.evaluate(() => document.activeElement?.tagName ?? ""));
  }
  expect(walk).toContain("SELECT");
  expect(walk).toContain("A");
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    () => window.location.hash.startsWith("#/r/"),
    { timeout: 15_000 },
  );

  // The record page: every depth is a focusable disclosure, the stance buttons
  // are reachable, and so is the whole ruling form once its fold is open.
  await open("r/pro_criteria-template");
  await openPeel("Rule on this");
  await page.evaluate(() => {
    const first = document.querySelector<HTMLElement>("main .page");
    first?.focus();
  });
  const reached = new Set<string>();
  for (let step = 0; step < 60; step += 1) {
    await page.keyboard.press("Tab");
    reached.add(await page.evaluate(() => {
      const active = document.activeElement;
      if (!active) return "";
      const value = active.getAttribute("value");
      const label = active.tagName === "BUTTON" || active.tagName === "SUMMARY"
        ? `:${(active.textContent ?? "").trim().slice(0, 16)}`
        : "";
      return `${active.tagName}${value ? `:${value}` : ""}${label}`;
    }));
  }
  // Every depth is announced and openable without a pointer.
  expect([...reached].some((entry) => entry.startsWith("SUMMARY:The claim"))).toBe(true);
  expect([...reached].some((entry) => entry.startsWith("SUMMARY:The machinery"))).toBe(true);
  // The operator's two voices: the cheap one and the permanent one.
  expect([...reached].some((entry) => entry.includes("Agree"))).toBe(true);
  expect([...reached].some((entry) => entry.startsWith("INPUT:accept"))).toBe(true);
  expect([...reached].filter((entry) => entry === "TEXTAREA").length).toBeGreaterThanOrEqual(2);
  expect([...reached].some((entry) => entry.includes("Record"))).toBe(true);
});

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

// Every old path still resolves. A citation, a bookmark or a link in a note
// written before the cutover lands on the record rather than on a dead route,
// and the kind segment the old paths carried is not needed to get there.
test.skipIf(!chrome)("the routes this surface replaced still lead somewhere", async () => {
  const redirects: Array<[string, string]> = [
    ["review", "#/"],
    ["findings", "#/read?kind=finding"],
    ["proposals", "#/read?kind=proposal"],
    ["hypotheses", "#/read?kind=hypothesis"],
    ["evaluation", "#/read"],
    ["explore", "#/watch"],
    ["fleet", "#/watch?view=fleet"],
    ["hypotheses/hyp_unverified-closures", "#/r/hyp_unverified-closures"],
    ["findings/fnd_conflicting-evidence", "#/r/fnd_conflicting-evidence"],
    ["proposals/pro_criteria-template", "#/r/pro_criteria-template"],
    ["review/hypothesis/hyp_lens-overlap", "#/r/hyp_lens-overlap"],
    ["evaluation/proposal/pro_bare-vote", "#/r/pro_bare-vote"],
  ];
  for (const [from, to] of redirects) {
    await open(from);
    await page.waitForFunction(
      (expected: string) => window.location.hash === expected,
      { timeout: 15_000 },
      to,
    );
    expect(`${from} -> ${await page.evaluate(() => window.location.hash)}`).toBe(`${from} -> ${to}`);
  }
});

test.skipIf(!chrome)("plan acceptance is explicit and flips proposed to applied", async () => {
  await open("ask");
  await page.waitForSelector(".accept-panel button", { timeout: 15_000 });
  await visible("proposed — nothing applied yet");
  await visible("applies only on acceptance");

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
  // A launch that could not open its durable store still serves Phase A, so
  // the operator's Sessions page works while the reading surface refuses. What
  // must not happen is the refusal following him: the banner reports the
  // failure of a request, and once he has navigated to a page that loaded
  // perfectly, a banner still accusing the frontier is telling him something
  // false about what he is looking at.
  //
  // Navigation here is a click rather than open(), deliberately. open()
  // reloads, which rebuilds the module holding the error, so a reload would
  // hide exactly the defect this test exists to catch.
  const base = unwiredMock?.base;
  if (!base) throw new Error("the unwired mock is not running");

  await page.goto(`${base}/#/read`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
  await visible("the hypothesis frontier is not available in this session");

  // Every frame from the click onwards is inspected, not just the state after
  // the navigation settled. Reading once afterwards makes this a race: clearing
  // the banner in an effect keyed on the path let the Sessions page paint one
  // frame carrying the frontier's refusal, and a single read caught it only
  // when something else on the page happened to be slow. One frame of a page
  // accusing another page of failing is the falsehood, so no frame may hold it.
  await page.evaluate(() => {
    Reflect.set(globalThis, "__babel_both_frames", 0);
    const watch = () => {
      const text = document.body.innerText;
      if (
        text.includes("Every session Babel found, across every harness")
        && text.includes("is not available in this session")
      ) {
        Reflect.set(globalThis, "__babel_both_frames", Number(Reflect.get(globalThis, "__babel_both_frames")) + 1);
      }
      requestAnimationFrame(watch);
    };
    requestAnimationFrame(watch);
  });
  await page.click('a[href="#/sessions"]');
  await page.waitForFunction(
    () => document.body.innerText.includes("Every session Babel found, across every harness"),
    { timeout: 15_000 },
  );

  // The Sessions page rendered, so any banner still on screen belongs to a
  // route the operator has left.
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toContain("is not available in this session");
  expect(await page.evaluate(() => Reflect.get(globalThis, "__babel_both_frames"))).toBe(0);

  // And the refusal is still reported where it is true, so clearing on
  // navigation has not simply silenced it.
  await page.click('a[href="#/"]');
  await visible("the review service is not available in this session");
});

// A listing that could not resolve the whole catalog is the one state a reader
// cannot check for himself: the rows look complete because rows always look
// complete. What must be on screen is that the list may be short -- and what
// must not be on screen, here least of all, is an explanation in terms of
// machines and publication state. The catalog is one body of work; which
// computer holds what is not a question this interface asks or answers.
test.skipIf(!chrome)("a partial catalog read says the list may be incomplete, and names no machine", async () => {
  await open("read", degradedMock?.base);
  await visible("This list may be incomplete");
  const state = await page.evaluate(() => ({
    rows: document.querySelectorAll("ol.output-list > li.output-row").length,
    text: document.body.innerText,
  }));
  // The records still render in full: a partial read costs the rows it could
  // not reach and nothing else.
  expect(state.rows).toBeGreaterThan(0);
  for (const word of ["this machine", "this host", "All hosts", "pending-sync", "unattributed", "demo-laptop", "build-server"]) {
    expect(state.text).not.toContain(word);
  }

  await open("", degradedMock?.base);
  await visible("This list may be incomplete");
});
