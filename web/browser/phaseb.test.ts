// Browser acceptance for the Phase B areas: Explore, Hypotheses (with
// Findings), Reality, and Review, driven against the synthetic mock server so
// no Go server, archive, or network is needed (SPEC.md §10's fixture rule).
//
// What only a browser can prove is covered here: that the areas actually
// render, that every control is reachable by keyboard, that narrow and wide
// viewports lay out without overflow, that the hostile HTML/Markdown/URL/
// control fixtures render inert — no script executes and no markup is
// injected — and that record content never enters a request URL or the
// location hash. The real server's leak channels stay covered by
// leak.test.ts; internal/web owns the HTTP-side contracts.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_HTML, UNBROKEN_TOKEN } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

// A developer without Chrome skips this suite. The notice resolveChrome prints
// in that case is what keeps the skip from reading as a pass: nothing else
// drives the Phase B areas in a browser, so a green run that never launched one
// would report the whole surface as acceptable while no page was ever rendered.
// In CI the same absence is a hard failure.
const chrome = resolveChrome({
  gate: "Phase B web gate",
  covers: "the Phase B areas -- Explore, Hypotheses, Findings, Reality and Review -- in a browser",
  unverified: [
    "that every Phase B area renders against the mock at all, and that an empty frontier reads as a state rather than as a bug",
    "that the hostile HTML, Markdown, URL and control fixtures render inert: no script runs, no markup is injected, and the literal markup stays visible as escaped text",
    "that every control is reachable by keyboard, and that no route overflows at either 390px or 1440px",
    "that recording a disposition, accepting a plan and answering a question are explicit acts that persist and read back",
    "that no record content reaches a request URL or the location hash",
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
let browser: Browser | null = null;
let page: Page;

// Every request the page makes, so the "no record content in URLs" channel
// can be asserted over the whole browsing session rather than one route.
const requestURLs: string[] = [];

const WIDE = { width: 1440, height: 900 };
const NARROW = { width: 390, height: 844 };

// Every Phase B route, including the awkward detail fixtures. The overflow
// audit walks all of them at both widths.
const ROUTES = [
  "explore",
  "hypotheses",
  "hypotheses/hyp_unverified-closures",
  "hypotheses/hyp_hostile-content",
  "hypotheses/hyp_dense-token",
  "hypotheses/hyp_many-observations",
  "findings",
  "findings/fnd_conflicting-evidence",
  "reality",
  "reality/entities/ent_atlas",
  "reality/entities/ent_longname",
  "review",
  "review/hypothesis/hyp_lens-overlap",
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

beforeAll(async () => {
  if (!chrome) return;

  // The mock serves web/dist, so the bundle under test is built from the
  // sources in this checkout rather than whatever was last committed.
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);

  [rich, emptyMock] = await Promise.all([startMock({}), startMock({ MOCK_PHASEB: "empty" })]);

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
});

test.skipIf(!chrome)("every Phase B area renders against the mock", async () => {
  await open("explore");
  await visible("Exploration is not startable here");
  await visible("Outcome integrity and unresolved state");
  await visible("pending-sync");

  await open("hypotheses");
  await page.waitForFunction(
    () => document.querySelectorAll(".frontier-table tbody tr").length === 6,
    { timeout: 15_000 },
  );

  await open("findings");
  await visible("Stated acceptance criteria correlate with verified closes");

  await open("reality");
  await page.waitForFunction(
    () => document.querySelectorAll(".question-card").length === 3,
    { timeout: 15_000 },
  );

  await open("review");
  await page.waitForFunction(
    () => document.querySelectorAll(".frontier-table tbody tr").length >= 4,
    { timeout: 15_000 },
  );
});

test.skipIf(!chrome)("the empty frontier reads as a state, not a bug", async () => {
  await open("explore", emptyMock?.base);
  await visible("Exploration is not startable here");
  await visible("No exploration runs are recorded");

  await open("hypotheses", emptyMock?.base);
  await visible("The frontier is empty");

  await open("reality", emptyMock?.base);
  await visible("No open questions");

  await open("review", emptyMock?.base);
  await visible("Nothing awaits a decision");
});

test.skipIf(!chrome)("a rejected hypothesis stays reachable and visibly rejected", async () => {
  await open("hypotheses");
  // The "rejected" status chip (All + statuses in §4.2 order).
  await page.waitForSelector(".filter-chips button", { timeout: 15_000 });
  const chips = await page.$$(".filter-chips button");
  await chips[5].click();
  await page.waitForFunction(
    () => document.querySelectorAll(".frontier-table tbody tr").length === 1
      && document.querySelector(".frontier-table tbody .badge")?.textContent === "rejected",
    { timeout: 15_000 },
  );

  await open("hypotheses/hyp_lens-overlap");
  await visible("rejected, and kept");
  const history = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".timeline-entry .badge")).map((badge) => badge.textContent));
  expect(history).toEqual(["untriaged", "investigating", "rejected"]);
});

test.skipIf(!chrome)("fifty observations render with their evidence locators", async () => {
  await open("hypotheses/hyp_many-observations");
  await page.waitForFunction(
    () => document.querySelectorAll(".observation-entry").length === 50,
    { timeout: 15_000 },
  );
  const locators = await page.evaluate(
    () => document.querySelectorAll(".observation-entry .evidence-locator").length,
  );
  expect(locators).toBe(50);
});

test.skipIf(!chrome)("counter-evidence renders where the claim is", async () => {
  await open("findings/fnd_conflicting-evidence");
  await visible("Counter-evidence");
  const state = await page.evaluate(() => ({
    counterItems: document.querySelectorAll(".evidence-list.counter li").length,
    fallibility: document.querySelectorAll(".fallibility-note").length,
    uncertainty: document.body.innerText.includes("confounded by task size"),
  }));
  // The finding's two conflicting locators plus the proposal's conflicting
  // material and the observation-level counter-evidence.
  expect(state.counterItems).toBeGreaterThanOrEqual(3);
  expect(state.fallibility).toBeGreaterThanOrEqual(1);
  expect(state.uncertainty).toBe(true);
});

test.skipIf(!chrome)("hostile fixtures render inert everywhere they appear", async () => {
  for (const route of ["hypotheses", "hypotheses/hyp_hostile-content", "reality", "reality/entities/ent_atlas"]) {
    await open(route);
    await page.waitForFunction(
      () => document.querySelector(".frontier-table, .frontier-detail, .question-card, .entity-page") !== null,
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
  await open("hypotheses/hyp_hostile-content");
  await visible("Model suggests");
  const literal = await page.evaluate(
    (needle: string) => document.body.innerText.includes(needle),
    HOSTILE_HTML,
  );
  expect(literal).toBe(true);

  // The search hit carrying hostile transcript bytes is likewise inert.
  await open("explore");
  await page.type("input[type=search]", "hostile");
  await page.click("button[type=submit]");
  await page.waitForSelector(".hit-entry", { timeout: 15_000 });
  const hit = await page.evaluate(() => ({
    pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    injected: document.querySelector(".hit-entry img, .hit-entry script") !== null,
    ordinals: Array.from(document.querySelectorAll(".hit-entry")).some((entry) =>
      /^\s*#?\d+[.)]/u.test(entry.textContent ?? "")),
  }));
  expect(hit.pwned).toBe("undefined");
  expect(hit.injected).toBe(false);
  // Rank is not strength: result rows carry no ordinal or score decoration.
  expect(hit.ordinals).toBe(false);
});

test.skipIf(!chrome)("keyboard navigation reaches every control", async () => {
  // List page: chips and rows are tabbable, Enter opens the focused row.
  await open("hypotheses");
  await page.waitForSelector(".frontier-table tbody tr", { timeout: 15_000 });
  const walk: string[] = [];
  for (let step = 0; step < 20 && !walk.includes("TR"); step += 1) {
    await page.keyboard.press("Tab");
    walk.push(await page.evaluate(() => document.activeElement?.tagName ?? ""));
  }
  expect(walk).toContain("BUTTON");
  expect(walk).toContain("TR");
  await page.keyboard.press("Enter");
  await page.waitForFunction(
    () => window.location.hash.startsWith("#/hypotheses/hyp_"),
    { timeout: 15_000 },
  );

  // Decision form: dispositions, both textareas, submit, and export controls
  // are all reachable without a pointer.
  await open("review/hypothesis/hyp_unverified-closures");
  await page.waitForSelector(".decide-card", { timeout: 15_000 });
  const reached = new Set<string>();
  for (let step = 0; step < 40; step += 1) {
    await page.keyboard.press("Tab");
    reached.add(await page.evaluate(() => {
      const active = document.activeElement;
      if (!active) return "";
      const value = active.getAttribute("value");
      return `${active.tagName}${value ? `:${value}` : ""}${active.textContent && active.tagName === "BUTTON" ? `:${active.textContent.trim().slice(0, 16)}` : ""}`;
    }));
  }
  expect([...reached].some((entry) => entry.startsWith("INPUT:accept"))).toBe(true);
  expect([...reached].filter((entry) => entry === "TEXTAREA").length).toBeGreaterThanOrEqual(1);
  expect([...reached].some((entry) => entry.includes("Record"))).toBe(true);
  expect([...reached].some((entry) => entry.includes("Show JSON"))).toBe(true);
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
          && document.querySelector(".state-card .spinner") === null,
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

test.skipIf(!chrome)("a disposition appends through the API and reads back", async () => {
  await open("review/hypothesis/hyp_unverified-closures");
  await page.waitForSelector(".decide-card", { timeout: 15_000 });
  await page.click("input[value=reject]");
  const areas = await page.$$(".decide-field textarea");
  await areas[0].type("Synthetic reviewer note");
  await areas[1].type("Synthetic attributed guidance");
  await page.click(".decide-card button[type=submit]");
  await page.waitForFunction(
    () => document.querySelectorAll(".timeline-entry").length === 1,
    { timeout: 15_000 },
  );
  const state = await page.evaluate(() => ({
    status: document.querySelector(".heading-badges .badge:nth-child(2)")?.textContent,
    guidance: document.querySelector(".context-note")?.textContent ?? "",
  }));
  expect(state.status).toBe("rejected");
  expect(state.guidance).toContain("Synthetic attributed guidance");
  expect(state.guidance).toContain("never evidence");
});

test.skipIf(!chrome)("the export shows its fallibility notice as escaped text", async () => {
  await open("review/hypothesis/hyp_lens-overlap");
  await page.waitForSelector(".export-card", { timeout: 15_000 });
  const buttons = await page.$$(".export-card .verify-actions button");
  await buttons[1].click();
  await page.waitForSelector(".export-card pre", { timeout: 15_000 });
  const exported = await page.evaluate(() => ({
    text: document.querySelector(".export-card pre")?.textContent ?? "",
    injected: document.querySelector(".export-card pre a, .export-card pre img") !== null,
  }));
  expect(exported.text).toContain("not an audit and not a finding of fact");
  expect(exported.injected).toBe(false);
});

test.skipIf(!chrome)("plan acceptance is explicit and flips proposed to applied", async () => {
  await open("reality");
  await page.waitForSelector(".accept-panel button", { timeout: 15_000 });
  const before = await page.evaluate(() => ({
    badge: document.querySelector(".plan-card .badge")?.textContent,
    pending: Array.from(document.querySelectorAll(".action-state")).map((state) => state.textContent),
  }));
  expect(before.badge).toBe("proposed — nothing applied yet");
  expect(before.pending).toContain("applies only on acceptance");

  await page.click(".accept-panel button");
  await visible("Plan accepted and applied atomically");
  const after = await page.evaluate(() => ({
    badge: document.querySelector(".plan-card .badge")?.textContent,
    applied: Array.from(document.querySelectorAll(".action-heading .badge"))
      .map((badge) => badge.textContent)
      .filter((label) => label === "applied").length,
  }));
  expect(after.badge).toBe("accepted");
  expect(after.applied).toBe(2);

  // The accepted assertion is now a fact on the entity, visibly active —
  // and the plan-proposed fixture fact remains visibly "proposed".
  await open("reality/entities/ent_atlas");
  await visible("Applied by accepting the synthetic interpreter plan");
});

test.skipIf(!chrome)("an answer records verbatim and moves the question state", async () => {
  await open("reality");
  await page.waitForSelector(".answer-form textarea", { timeout: 15_000 });
  await page.type(".answer-form textarea", "Yes, they are the same synthetic service.");
  await page.click(".answer-form button[type=submit]");
  await page.waitForFunction(
    () => document.body.innerText.includes("Yes, they are the same synthetic service."),
    { timeout: 15_000 },
  );
  const state = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".question-card"))
      .find((card) => card.textContent?.includes("nightly reconciliation"))
      ?.querySelector(".badge:nth-child(2)")?.textContent);
  expect(state).toBe("answered-uninterpreted");
});

test.skipIf(!chrome)("record content never enters a request URL or the location hash", async () => {
  // By this point the suite has browsed every area, searched, decided, and
  // accepted. The only things allowed in URLs are routes, identifiers, and
  // the operator's own explicit search query.
  const forbidden = [
    "regressions discovered",
    "Synthetic reviewer note",
    "Synthetic attributed guidance",
    "Atlas is active again",
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
