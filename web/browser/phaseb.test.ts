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
    "that the host filter appears, narrows an already-merged list, and offers the unattributed group",
    "that a pending-sync row is visibly staged and an unknown sync state never reads as local",
    "that a machine with no shared backend says so instead of rendering an empty fleet as a bug",
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
  // Scoped to the status-history card: the page also renders #87's revision
  // chain as a timeline, and the two histories are different questions about
  // the record — where it stands, and what it has said.
  const history = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".history-card .timeline-entry .badge"))
      .map((badge) => badge.textContent));
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
  // `explore` is in the list because its fleet card renders another host's
  // record summary, which is the least trusted string on any page here: it was
  // authored by a model on a machine this one does not control and decrypted
  // locally.
  for (const route of ["hypotheses", "hypotheses/hyp_hostile-content", "reality", "reality/entities/ent_atlas", "explore"]) {
    await open(route);
    await page.waitForFunction(
      () => document.querySelector(".frontier-table, .frontier-detail, .question-card, .entity-page, .fleet-table") !== null,
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
  // The bound is generous because the toolbar now carries two chip rows: the
  // status filter and the host filter. What is under test is that a row is
  // reachable at all, not how many chips precede it.
  for (let step = 0; step < 40 && !walk.includes("TR"); step += 1) {
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
  // The confirmation message and the card's badge are separate renders, so
  // waiting for the message does not order the badge's update: on a loaded CI
  // runner the badge was still "proposed - nothing applied yet" when read here.
  // Await the flip this test is about. A badge that never flips still fails,
  // by timeout naming the condition, rather than by being read mid-render -
  // and the applied count below stays a real assertion, because it belongs to
  // the same render and a lag between them would be a product inconsistency.
  await page.waitForFunction(
    () => document.querySelector(".plan-card .badge")?.textContent === "accepted",
    { timeout: 15_000 },
  );
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

// ---------------------------------------------------------------------------
// The fleet surfaces (issue #109 item 4).
//
// What only a browser can prove here is that the attribution is legible: that
// the chips exist and narrow, that a staged row looks staged while scanning,
// that an absent host reads as an absence rather than as this machine, and that
// another host's record content is inert in the DOM it lands in.
// ---------------------------------------------------------------------------

// syncBadges reads the sync column's rendered labels, which is the vocabulary
// under test: four values, each meaning something different.
function syncBadges(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".frontier-table tbody .sync-mark .badge"))
      .map((badge) => badge.textContent ?? ""));
}

function rowCount(): Promise<number> {
  return page.evaluate(() => document.querySelectorAll(".frontier-table tbody tr").length);
}

test.skipIf(!chrome)("the host filter appears and narrows an already-merged list", async () => {
  await open("hypotheses");
  await page.waitForSelector(".host-chips button", { timeout: 15_000 });
  const chips = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".host-chips button")).map((chip) => chip.textContent ?? ""));
  // This host, All hosts, then one chip per machine that holds records --
  // including the group whose origin instances registered no host, without
  // which its records could not be reached at all.
  expect(chips[0]).toContain("This host");
  expect(chips[1]).toContain("All hosts");
  expect(chips.some((chip) => chip.includes("build-server"))).toBe(true);
  expect(chips.some((chip) => chip.includes("unattributed"))).toBe(true);

  // The default is this machine alone, which is what every other listing shows.
  const local = await rowCount();
  expect(local).toBe(6);

  // All hosts widens.
  const buttons = await page.$$(".host-chips button");
  await buttons[1].click();
  await page.waitForFunction(
    (before: number) => document.querySelectorAll(".frontier-table tbody tr").length > before,
    { timeout: 15_000 },
    local,
  );
  const everything = await rowCount();

  // And one host narrows the already-merged list, without losing the chips:
  // a filter that narrowed its own options would leave no way back.
  const remoteIndex = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".host-chips button"))
      .findIndex((chip) => (chip.textContent ?? "").includes("build-server")));
  expect(remoteIndex).toBeGreaterThan(1);
  const widened = await page.$$(".host-chips button");
  await widened[remoteIndex].click();
  await page.waitForFunction(
    (before: number) => document.querySelectorAll(".frontier-table tbody tr").length < before
      && document.querySelectorAll(".host-chips button").length >= 4,
    { timeout: 15_000 },
    everything,
  );
  const narrowed = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".frontier-table tbody .host-label")).map((label) => label.textContent ?? ""));
  expect(narrowed.length).toBeGreaterThan(0);
  for (const label of narrowed) expect(label).toContain("build-server");

  // Back to this host: the chip row is a filter, not a one-way door.
  const backButtons = await page.$$(".host-chips button");
  await backButtons[0].click();
  await page.waitForFunction(
    () => document.querySelectorAll(".frontier-table tbody tr").length === 6,
    { timeout: 15_000 },
  );
});

test.skipIf(!chrome)("a pending-sync row is visibly staged and an absent host reads as absent", async () => {
  await open("hypotheses");
  await page.waitForSelector(".frontier-table tbody tr", { timeout: 15_000 });

  // Staged output is marked on the row, not only in the badge's text: a
  // reviewer scanning twenty rows reads rows.
  const staged = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".frontier-table tbody tr.row-pending-sync"));
    return {
      count: rows.length,
      badge: rows[0]?.querySelector(".sync-mark .badge")?.textContent,
      marked: rows[0] ? getComputedStyle(rows[0]).boxShadow !== "none" : false,
    };
  });
  expect(staged.count).toBeGreaterThanOrEqual(1);
  expect(staged.badge).toBe("pending-sync");
  expect(staged.marked).toBe(true);

  // Three of the four sync states are reachable from this machine's own rows,
  // and none of them is blank.
  const badges = await syncBadges();
  expect(new Set(badges)).toEqual(new Set(["committed", "pending-sync", "local"]));

  // A record no host can be named for says so, and is never labelled with this
  // machine. The receipt strip carries one too.
  const chips = await page.$$(".host-chips button");
  await chips[1].click();
  await page.waitForFunction(
    () => Array.from(document.querySelectorAll(".frontier-table tbody .host-label"))
      .some((label) => (label.textContent ?? "").includes("unattributed")),
    { timeout: 15_000 },
  );
  const attribution = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".frontier-table tbody tr"));
    const unattributed = rows.find((row) =>
      (row.querySelector(".host-label")?.textContent ?? "").includes("unattributed"));
    return {
      muted: unattributed?.querySelector(".unattributed-host") !== null,
      // The absence must not carry this machine's name anywhere on the row.
      namesLocalHost: (unattributed?.textContent ?? "").includes("demo-laptop"),
      // A row from another host is not a link: the detail routes read this
      // machine's durable store, which does not hold it.
      remoteNotLinked: rows
        .filter((row) => row.classList.contains("row-remote-host"))
        .every((row) => row.getAttribute("role") === null && row.getAttribute("tabindex") === null),
    };
  });
  expect(attribution.muted).toBe(true);
  expect(attribution.namesLocalHost).toBe(false);
  expect(attribution.remoteNotLinked).toBe(true);
});

test.skipIf(!chrome)("the review inbox renders fleet-wide with its proposals intact", async () => {
  await open("review");
  await page.waitForSelector(".host-chips button", { timeout: 15_000 });
  const chips = await page.$$(".host-chips button");
  await chips[1].click();
  await page.waitForFunction(
    () => document.querySelectorAll(".frontier-table tbody tr.row-remote-host").length > 0,
    { timeout: 15_000 },
  );
  const inbox = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".frontier-table tbody tr.row-remote-host"));
    return {
      hosts: rows.map((row) => row.querySelector(".host-label")?.textContent ?? ""),
      // A proposal has no searchable summary at all. It must still be here --
      // an inbox that dropped proposals would withhold records from the
      // reviewer whose inbox it is -- and it must say why it has no excerpt
      // rather than claiming the record is untitled.
      proposals: rows.filter((row) => (row.textContent ?? "").includes("proposal")).length,
      noSummary: rows.some((row) => row.querySelector(".no-summary") !== null),
      untitled: rows.some((row) => (row.textContent ?? "").includes("Untitled record")),
      // The review status and the counts are the owning host's derivations, so
      // a remote row shows an absence rather than a decided-nothing.
      dashes: rows.every((row) => (row.textContent ?? "").includes("\u2014")),
    };
  });
  expect(inbox.proposals).toBeGreaterThanOrEqual(1);
  expect(inbox.noSummary).toBe(true);
  expect(inbox.untitled).toBe(false);
  expect(inbox.dashes).toBe(true);
  for (const host of inbox.hosts) expect(host).toContain("build-server");
});

test.skipIf(!chrome)("another host's record content is inert in the DOM it lands in", async () => {
  await open("explore");
  await page.waitForSelector(".fleet-table tbody tr", { timeout: 15_000 });
  const state = await page.evaluate(() => ({
    pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    injected: document.querySelector(".fleet-table img, .fleet-table script") !== null,
    scriptURL: Array.from(document.querySelectorAll(".fleet-table a"))
      .some((anchor) => (anchor as HTMLAnchorElement).href.startsWith("javascript:")),
    // The reason a record could not be opened is rendered rather than
    // swallowed: a blank row would send an operator looking in the wrong place.
    unopened: document.querySelector(".fleet-table .unopened-note")?.textContent ?? "",
    // An unattributed row and a staged row are both on this card.
    unattributed: document.querySelectorAll(".fleet-table .unattributed-host").length,
    staged: document.querySelectorAll(".fleet-table tbody tr.row-pending-sync").length,
  }));
  expect(state.pwned).toBe("undefined");
  expect(state.injected).toBe(false);
  expect(state.scriptURL).toBe(false);
  expect(state.unopened).toContain("does not hold");
  expect(state.unattributed).toBeGreaterThanOrEqual(1);
  expect(state.staged).toBeGreaterThanOrEqual(1);

  // The literal markup is visible as escaped text inside the row, so a reader
  // sees exactly what the other machine emitted -- neutralized, not dropped.
  const literal = await page.evaluate(
    (needle: string) => document.body.innerText.includes(needle),
    HOSTILE_HTML,
  );
  expect(literal).toBe(true);
});

test.skipIf(!chrome)("a machine with no shared backend says so", async () => {
  await open("hypotheses", emptyMock?.base);
  await visible("This machine has no shared backend configured");
  const state = await page.evaluate(() => ({
    notice: document.querySelectorAll(".fleet-notice").length,
    // No chips, because there is no vocabulary: there are no other hosts.
    chips: document.querySelectorAll(".host-chips button").length,
    // And no claim that these records are anywhere else.
    unattributedAsLocal: document.body.innerText.includes("demo-laptop"),
  }));
  expect(state.notice).toBe(1);
  expect(state.chips).toBe(0);
  expect(state.unattributedAsLocal).toBe(false);

  await open("explore", emptyMock?.base);
  await visible("This machine has no shared backend configured");
});

test.skipIf(!chrome)("an unreachable catalog reads as unknown, never as local", async () => {
  await open("hypotheses", degradedMock?.base);
  await visible("Global sync state is not known for these records");
  const badges = await syncBadges();
  expect(badges.length).toBeGreaterThan(0);
  // The whole point: not one row claims "local". A catalog that did not answer
  // did not observe that nothing is carrying these records anywhere.
  expect(new Set(badges)).toEqual(new Set(["unknown"]));
  const state = await page.evaluate(() => ({
    notice: document.querySelectorAll(".sync-degraded-notice").length,
    marked: document.querySelectorAll(".frontier-table tbody tr.row-sync-unknown").length,
    rows: document.querySelectorAll(".frontier-table tbody tr").length,
  }));
  expect(state.notice).toBe(1);
  // The rows are still this machine's own records and still render in full.
  expect(state.rows).toBe(6);
  expect(state.marked).toBe(6);

  await open("review", degradedMock?.base);
  await visible("Global sync state is not known for these records");
});

test.skipIf(!chrome)("the fleet-engaged listings lay out without overflow", async () => {
  // The overflow audit walks routes without interacting, so the widened lists
  // -- longer host labels, an extra two columns, a wrapped unopened reason --
  // need their own pass at both widths.
  for (const viewport of [WIDE, NARROW]) {
    await page.setViewport(viewport);
    for (const route of ["hypotheses", "findings", "review"]) {
      await open(route);
      await page.waitForSelector(".host-chips button", { timeout: 15_000 });
      const chips = await page.$$(".host-chips button");
      await chips[1].click();
      await page.waitForFunction(
        () => document.querySelector(".state-card .spinner") === null,
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

test.skipIf(!chrome)("a refusal banner is scoped to the route that earned it", async () => {
  // A launch that could not open its durable store still serves Phase A, so
  // the operator's Sessions and Archive pages work while the Phase B pages
  // refuse. What must not happen is the refusal following him: the banner
  // reports the failure of a request, and once he has navigated to a page
  // that loaded perfectly, a banner still accusing the frontier is telling
  // him something false about what he is looking at.
  //
  // Navigation here is a click rather than open(), deliberately. open()
  // reloads, which rebuilds the module holding the error, so a reload would
  // hide exactly the defect this test exists to catch.
  const base = unwiredMock?.base;
  if (!base) throw new Error("the unwired mock is not running");

  await page.goto(`${base}/#/hypotheses`, { waitUntil: "networkidle2" });
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
        text.includes("Every session Babel found on this machine")
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
    () => document.body.innerText.includes("Every session Babel found on this machine"),
    { timeout: 15_000 },
  );

  // The Sessions page rendered, so any banner still on screen belongs to a
  // route the operator has left.
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toContain("is not available in this session");
  expect(await page.evaluate(() => Reflect.get(globalThis, "__babel_both_frames"))).toBe(0);

  // And the refusal is still reported where it is true, so clearing on
  // navigation has not simply silenced it.
  await page.click('a[href="#/review"]');
  await visible("the review service is not available in this session");
});
