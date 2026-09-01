// Browser acceptance for issue #115's steering surfaces — the capture box and
// complaint listing on /review, and a complaint's own record page — driven
// against the synthetic mock, so no Go server, archive, model, store, or
// network is needed.
//
// What only a browser can prove is here, and nearly all of it is about the two
// promises #115 makes to an operator annoyed enough to start typing.
//
// That capture works end to end from a box a person can actually reach: typed
// words become a stored complaint with an identifier, the answer quotes the
// words back verbatim, names what Babel already holds touching them, and says
// in one fixed sentence that pressing the button opened nothing, assigned
// nothing to anybody and scheduled no work. Then the listing below the box
// holds the complaint that was just told, because a capture that answered and
// vanished would leave the operator unable to tell whether Babel kept it.
//
// That neither surface offers a status control. This is the guarantee most
// likely to be lost by accident rather than by argument: a complaint listing
// looks exactly like a ticket queue, and the first person to add a "resolved"
// filter turns steering pressure into a backlog with an unread column. So the
// rendered pages are searched for any control — button, select or input —
// whose accessible text reads like closure, assignment or priority, and for a
// Status label anywhere in either section. "Was this addressed?" has to stay
// the Cited by direction of the citation panel and nothing else, which is why
// the same test insists Cited by and Revision history are still on the page:
// the charter is that there is no status, not that there is nothing.
//
// That the body is verbatim and inert. The operator's paragraphs survive as
// paragraphs, and the markup he pasted in from the output he is complaining
// about renders as characters: it injects no element, gains the document no
// script, and never becomes a link destination (§2.7). This field is the one
// on these surfaces that is entirely his, which makes it the likeliest to
// carry an injection and the least acceptable to sanitize into silence.
//
// That amending appends. Both wordings of a chain stay readable at their own
// identifiers, the current one is marked current, and opening a superseded
// wording renders it as superseded while still reporting the chain's head —
// never as an error, and never redirected to the head, because what the
// operator said at the time is not a mistake to be corrected.
//
// That both citation directions render on a complaint, including an endpoint
// this host cannot open. That is how a complaint told on a laptop reads once
// another machine's runs have cited it, and it is the case that carries the
// whole "was this addressed?" answer (#113).
//
// That day one — an operator who has told Babel nothing — is an empty state
// beside a usable box, not an error.
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
  gate: "Operator steering gate",
  covers: "issue #115's complaint capture box and complaint record pages, in a browser",
  unverified: [
    "that telling Babel something from the web captures it, quotes it back verbatim, and reports what Babel already holds touching it",
    "that a capture answers with the fixed sentence saying nothing was opened, assigned or scheduled",
    "that the captured complaint then appears in the listing below the box",
    "that neither the steering section nor a complaint's record page offers a status, closure, assignment or priority control",
    "that a complaint's body renders verbatim and inert, newlines kept and pasted markup executed nowhere",
    "that a revision chain shows every wording oldest first, marks the current one, and keeps an earlier wording readable at its own id",
    "that both citation directions render on a complaint, the followable endpoint as a link into this app and the unopenable one as identified text with its reason",
    "that a launch with nothing told renders the empty state rather than an error",
  ],
});

// SHOTS is where the run leaves its evidence. A screenshot is not an assertion
// and nothing here passes or fails on one; it exists because a layout claim in a
// pull request should be checkable by looking, and BABEL_TEST_SHOTS lets CI or an
// operator put it somewhere durable instead of the temp directory.
const SHOTS = process.env.BABEL_TEST_SHOTS ?? join(tmpdir(), "babel-complaints-shots");

// The sentence the operator types. It carries the word "rules" so the fixtures
// have something to be adjacent to, and enough of one candidate's wording that
// the adjacency panel has a candidate to name as well as a complaint — the
// panel is only readable as a prompt to compare when it can name more than one
// kind of thing.
const TOLD =
  "My repository rules are ignored and sessions still end with an agent claiming success " +
  "with no verification step.";

// The fixed sentence a capture answers with, written out here rather than
// imported from the mock. It is the one string on this surface that exists to
// stop a reader from believing Babel did something, so the test carries the
// contract's own wording: a test that read the sentence from the server it is
// testing could not notice either side quietly rewording it.
const STEERING =
  "nothing; a complaint is steering pressure, and Babel opened, assigned and scheduled none of it";

// The vocabulary of a ticket. Any control on either surface whose accessible
// text matches this is the defect #115 exists to prevent: a complaint has no
// closure, so a button offering one would be offering an operation Babel does
// not have, and a filter offering one would invent a state nothing stores.
const TICKET_CONTROL = "resolv|close|closed|reopen|assign|status|done|priority|due";

// The kinds capture-time adjacency may report — frontier.OutputKind, and
// nothing else. A row of some sixth kind would mean the pass reached a store
// whose records have no page to open.
const ADJACENT_KINDS = ["hypothesis", "observation", "finding", "review-answer", "complaint"];

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

function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 15_000 },
    text,
  );
}

// complaintRendered waits for the record page's own body card rather than for a
// phrase, for the reason references.test.ts waits on a row: the eyebrows and
// the citation headings are uppercased by CSS and Chrome's innerText reports
// them that way, so a text wait would hang on a page that rendered correctly.
function complaintRendered(): Promise<unknown> {
  return page.waitForSelector(".complaint-page .statement-card .quoted-text", { timeout: 15_000 });
}

// shoot photographs one element rather than the viewport. A panel that sits
// below the fold of a record page is not in a viewport screenshot at all, so a
// full-page capture or a scroll would be the alternative; framing the element
// is what makes the file usable as evidence of the panel's layout.
//
// The scroll leaves a gap above the element instead of using scrollIntoView,
// because the app's top bar is sticky: an element aligned to the viewport top
// is photographed with the nav painted over its first rows, which reads as a
// clipped panel in the very file that exists to show the panel is not clipped.
const STICKY_HEADER_CLEARANCE = 140;

async function shoot(selector: string, name: string): Promise<void> {
  const element = await page.waitForSelector(selector, { timeout: 15_000 });
  if (!element) throw new Error(`no element to photograph: ${selector}`);
  // The top bar is sticky and taller than the clearance a scroll can buy: a
  // capture that reaches past the viewport re-paints it at the top of the
  // clipped region, over the panel's own heading. Hiding it with `visibility`
  // rather than `display` keeps the layout identical, so the panel in the file
  // is the panel the page laid out.
  await page.evaluate(
    (target: string, clearance: number) => {
      const bar = document.querySelector(".topbar") as HTMLElement | null;
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
    const bar = document.querySelector(".topbar") as HTMLElement | null;
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

test.skipIf(!chrome)("telling Babel something captures it, answers, and lists it", async () => {
  await open("review");
  await page.waitForSelector(".steering-section .capture-card .capture-input", { timeout: 15_000 });

  // The button is dead until there are words. A capture box that submitted an
  // empty complaint would store a record saying nothing, which the store
  // refuses anyway — so the refusal has to be visible as a disabled control
  // rather than reached as an error.
  expect(await page.$eval(".capture-submit", (button) => (button as HTMLButtonElement).disabled))
    .toBe(true);
  await page.type(".capture-input", TOLD);
  expect(await page.$eval(".capture-submit", (button) => (button as HTMLButtonElement).disabled))
    .toBe(false);

  await page.click(".capture-submit");
  await page.waitForSelector(".capture-result", { timeout: 15_000 });

  const captured = await page.evaluate((kinds: string[]) => {
    const result = document.querySelector(".capture-result");
    const rows = Array.from(result?.querySelectorAll(".capture-adjacent li") ?? []);
    return {
      recorded: result?.querySelector(".capture-recorded")?.textContent ?? "",
      id: result?.querySelector(".capture-recorded .mono")?.textContent ?? "",
      // textContent rather than innerText: the body is a <pre> and the
      // assertion is about the operator's exact bytes.
      quoted: result?.querySelector(".quoted-text")?.textContent ?? "",
      heading: result?.querySelector("h3")?.textContent ?? "",
      adjacent: rows.length,
      badges: rows.map((row) => row.querySelector(".badge")?.textContent ?? ""),
      unknownKinds: rows
        .map((row) => row.querySelector(".badge")?.textContent ?? "")
        .filter((badge) => !kinds.includes(badge)),
      // Every row's text, so the "no score, ever" rule is measured on the
      // rendered row rather than trusted from the wire shape.
      rows: rows.map((row) => (row as HTMLElement).innerText),
      linked: rows.filter((row) => row.querySelector("a[href^='#/']") !== null).length,
      steering: result?.querySelector(".capture-steering")?.textContent ?? "",
    };
  }, ADJACENT_KINDS);

  expect(captured.recorded).toContain("Captured as");
  expect(captured.id).toMatch(/^cmp_/u);
  // The words come back exactly as typed. This is the operator's own text, and
  // a surface that trimmed, reflowed or re-cased it would be quoting him
  // wrongly on the page where he can see it.
  expect(captured.quoted).toBe(TOLD);

  expect(captured.heading).toBe("What Babel already has touching this");
  expect(captured.adjacent).toBeGreaterThan(0);
  expect(captured.unknownKinds).toEqual([]);
  // At least one row is a link, because adjacency is only useful if the thing
  // it names can be opened and compared.
  expect(captured.linked).toBeGreaterThan(0);
  // No score, no percentage, no "match" number: adjacency is a prompt to
  // compare, and a number beside it would read as Babel having judged that
  // these two records are the same complaint.
  for (const row of captured.rows) {
    expect(row).not.toMatch(/\d+(\.\d+)?\s*%|\bscore\b|\bsimilarity\b/iu);
  }
  expect(captured.steering).toBe(STEERING);

  // And the listing below now holds it. Newest first, so it is the first row:
  // the operator has just told Babel this, and a capture that landed at the
  // bottom of a page of older complaints would read as having been filed away.
  await page.waitForFunction(
    (id: string) =>
      (document.querySelector(".steering-list-card tbody tr .mono")?.textContent ?? "") === id,
    { timeout: 15_000 },
    captured.id,
  );
  const listed = await page.evaluate(() => {
    const row = document.querySelector(".steering-list-card tbody tr");
    return {
      summary: row?.querySelector(".untrusted-inline")?.textContent ?? "",
      role: row?.getAttribute("role") ?? "",
    };
  });
  expect(listed.summary).toBe(TOLD);
  expect(listed.role).toBe("link");

  await shoot(".steering-section", "steering-capture.png");
});

test.skipIf(!chrome)("neither steering surface offers a way to close a complaint", async () => {
  // The measurement is the same on both surfaces, so it is written once and
  // run twice: a status control is as damaging on the listing as on the record
  // page, and a guard that only watched one would be half a guard.
  const audit = (scope: string, forbidden: string) =>
    page.evaluate(
      (selector: string, pattern: string) => {
        const section = document.querySelector(selector);
        const ticket = new RegExp(pattern, "iu");
        const controls = Array.from(section?.querySelectorAll("button, select, input") ?? []);
        const labels = Array.from(
          section?.querySelectorAll("th, label, dt, .field-label, .eyebrow, .count-label") ?? [],
        );
        return {
          present: section !== null,
          // Accessible text, not just the label: a control whose text is an
          // icon still announces itself through aria-label or title, and that
          // is the name a screen reader and this assertion both read.
          offenders: controls
            .map((control) => {
              const element = control as HTMLElement;
              return [
                element.getAttribute("aria-label") ?? "",
                element.getAttribute("title") ?? "",
                element.getAttribute("value") ?? "",
                element.innerText,
              ].join(" ");
            })
            .filter((text) => ticket.test(text)),
          statusLabels: labels
            .map((label) => (label.textContent ?? "").trim())
            .filter((text) => text.toLocaleLowerCase() === "status"),
          // textContent rather than innerText, because CSS uppercases the
          // eyebrows: the charter sentences say "no status" in lowercase prose
          // on purpose, and a capital-S Status in the source is a field label.
          statusLabelInText: /\bStatus\b/u.test(section?.textContent ?? ""),
          headers: Array.from(section?.querySelectorAll("thead th") ?? []).map(
            (cell) => cell.textContent ?? "",
          ),
        };
      },
      scope,
      forbidden,
    );

  await open("review");
  await page.waitForSelector(".steering-section .steering-table", { timeout: 15_000 });
  const listing = await audit(".steering-section", TICKET_CONTROL);
  expect(listing.present).toBe(true);
  expect(listing.offenders).toEqual([]);
  expect(listing.statusLabels).toEqual([]);
  expect(listing.statusLabelInText).toBe(false);
  // The whole column vocabulary, so a Status column cannot arrive beside these
  // and the "Addressed by" column cannot quietly become one.
  expect(listing.headers).toEqual(["Complaint", "Told by", "Host", "Told", "Addressed by"]);

  await open("complaints/cmp_rules");
  await complaintRendered();
  const record = await audit(".complaint-page", TICKET_CONTROL);
  expect(record.present).toBe(true);
  expect(record.offenders).toEqual([]);
  expect(record.statusLabels).toEqual([]);
  expect(record.statusLabelInText).toBe(false);

  // And the two things that must still be there. The charter is that a
  // complaint has no status, not that the page says nothing about whether it
  // was addressed: that answer is the citation panel, and the chain is how the
  // operator's own rewordings stay readable.
  const kept = await page.evaluate(() => ({
    directions: Array.from(document.querySelectorAll(".complaint-page .citation-direction h3")).map(
      (heading) => heading.textContent ?? "",
    ),
    revisions: document.querySelector(".complaint-page .revisions-card h2")?.textContent ?? "",
    noStatus: document.querySelector(".complaint-no-status")?.textContent ?? "",
  }));
  expect(kept.directions).toEqual(["Cites", "Cited by"]);
  expect(kept.revisions).toBe("Revision history");
  expect(kept.noStatus).toContain('"Was this addressed?"');
});

test.skipIf(!chrome)("a complaint's body is verbatim and inert", async () => {
  // A page from the same app with no hostile fixture on it, to count the
  // scripts the application itself loads. Comparing against that count is what
  // makes "the document gained nothing" a measurement rather than a hope.
  await open("complaints/cmp_quiet");
  await complaintRendered();
  const baseline = await page.evaluate(() => document.querySelectorAll("script").length);

  await open("complaints/cmp_hostile");
  await complaintRendered();

  const body = await page.evaluate(() => {
    const quoted = document.querySelector(".complaint-page .statement-card .quoted-text");
    const element = quoted as HTMLElement | null;
    return {
      tag: quoted?.tagName ?? "",
      // The exact bytes, which is what "verbatim" means. innerText is read
      // separately and only for the newline, because that is a claim about
      // what was rendered rather than about what was received.
      text: quoted?.textContent ?? "",
      rendered: element?.innerText ?? "",
      // If any of the fixture's markup were parsed, the body would have child
      // elements and the document would hold the tags it names.
      children: quoted?.children.length ?? -1,
      injected: document.querySelectorAll(".complaint-page script, .complaint-page img").length,
      scripts: document.querySelectorAll("script").length,
      href: quoted?.getAttribute("href"),
      anchors: quoted?.querySelectorAll("a").length ?? -1,
      pwned: String(Reflect.get(globalThis, "__babel_pwned")),
    };
  });

  expect(body.tag).toBe("PRE");
  // The operator pasted the model's output into his complaint. Every byte of
  // it is on the page, and none of it did anything.
  expect(body.text).toContain(HOSTILE_HTML);
  expect(body.children).toBe(0);
  expect(body.injected).toBe(0);
  expect(body.scripts).toBe(baseline);
  expect(body.pwned).toBe("undefined");
  // Never a link destination: the body is the operator's words, and a surface
  // that turned part of them into an href would let a complaint navigate the
  // person reading it.
  expect(body.href).toBeNull();
  expect(body.anchors).toBe(0);
  // His paragraphs survive as paragraphs. The fixture has a blank line in it,
  // and a body that reflowed the text would read as one run-on complaint.
  expect(body.rendered).toContain("\n");
});

test.skipIf(!chrome)("amending appends, and every wording stays readable", async () => {
  await open("complaints/cmp_rules");
  await complaintRendered();

  const head = await page.evaluate(() => {
    const section = document.querySelector(".complaint-page");
    const entries = Array.from(section?.querySelectorAll(".revision-timeline .timeline-entry") ?? []);
    return {
      badges: Array.from(section?.querySelectorAll(".badge") ?? []).map(
        (badge) => badge.textContent ?? "",
      ),
      counts: Array.from(section?.querySelectorAll(".count-label") ?? []).map(
        (count) => count.textContent ?? "",
      ),
      entryBadges: entries.map((entry) => entry.querySelector(".badge")?.textContent ?? ""),
      entryText: entries.map((entry) => (entry as HTMLElement).innerText),
      entryLinks: entries.map((entry) => entry.querySelector("a")?.getAttribute("href") ?? ""),
    };
  });

  // Oldest first: a chain is read as a history, and a history that opened with
  // its own ending would make the operator's first wording look like a reply.
  expect(head.entryBadges).toEqual(["revision 1", "revision 2"]);
  expect(head.entryLinks).toEqual(["#/complaints/cmp_rules_1", "#/complaints/cmp_rules"]);
  expect(head.badges).toContain("current wording");
  expect(head.badges).not.toContain("superseded wording");
  expect(head.counts).toContain("revision 2");
  // The wording on screen is marked as the one on screen, and the current one
  // as current. On the head's page they are the same entry, which is exactly
  // the state a reader must be able to confirm rather than assume.
  expect(head.entryText[1]).toContain("the wording shown above");
  expect(head.entryText[1]).toContain("current");
  expect(head.entryText[0]).not.toContain("the wording shown above");

  await shoot(".complaint-page", "complaint-record.png");

  // The superseded wording at its own identifier. Nothing here ends a chain,
  // so this page is not an error and not a redirect: it is what the operator
  // said before he said it better, and it still points at what replaced it.
  await open("complaints/cmp_rules_1");
  await complaintRendered();

  const superseded = await page.evaluate(() => {
    const section = document.querySelector(".complaint-page");
    const entries = Array.from(section?.querySelectorAll(".revision-timeline .timeline-entry") ?? []);
    return {
      badges: Array.from(section?.querySelectorAll(".badge") ?? []).map(
        (badge) => badge.textContent ?? "",
      ),
      counts: Array.from(section?.querySelectorAll(".count-label") ?? []).map(
        (count) => count.textContent ?? "",
      ),
      entries: entries.length,
      entryText: entries.map((entry) => (entry as HTMLElement).innerText),
      headLink: entries[entries.length - 1]?.querySelector("a")?.getAttribute("href") ?? "",
      body: section?.querySelector(".statement-card .quoted-text")?.textContent ?? "",
    };
  });

  expect(superseded.badges).toContain("superseded wording");
  expect(superseded.badges).not.toContain("current wording");
  expect(superseded.counts).toContain("revision 1");
  // The whole chain is still on the page, and the head is still named as the
  // current one — the earlier wording reports the chain rather than replacing
  // it, which is what stops an old link from misleading whoever follows it.
  expect(superseded.entries).toBe(2);
  expect(superseded.entryText[0]).toContain("the wording shown above");
  expect(superseded.entryText[1]).toContain("current");
  expect(superseded.headLink).toBe("#/complaints/cmp_rules");
  // And it is this wording's own words on the page, not the head's.
  expect(superseded.body).toContain("I have said this three times");
});

test.skipIf(!chrome)("a complaint's citations render in both directions", async () => {
  await open("complaints/cmp_rules");
  await page.waitForSelector(".complaint-page .references-card .citation-entry", { timeout: 15_000 });

  const citations = await page.evaluate(() => {
    const panel = document.querySelector(".complaint-page .references-card");
    const directions = Array.from(panel?.querySelectorAll(".citation-direction") ?? []);
    const target = (edge: string) => {
      const entry = panel?.querySelector(`[data-citation='${edge}']`);
      const element = entry?.querySelector(".citation-target");
      return {
        tag: element?.tagName ?? "",
        href: element?.getAttribute("href") ?? null,
        inert: element?.classList.contains("inert") ?? false,
        text: (element as HTMLElement | null)?.innerText ?? "",
        reason: entry?.querySelector(".unopened-note")?.textContent ?? "",
      };
    };
    return {
      headings: directions.map((section) => section.querySelector("h3")?.textContent ?? ""),
      rows: directions.map((section) => section.querySelectorAll(".citation-entry").length),
      // The operator's own aim, recorded as an outgoing edge: "this is what I
      // am complaining about".
      aim: target("rle_rules-addresses-hyp"),
      // The answer to "was this addressed?", in the only direction that can
      // answer it.
      addressed: target("rle_hyp-addresses-rules"),
      elsewhere: target("rle_absent-finding-addresses-rules"),
    };
  });

  expect(citations.headings).toEqual(["Cites", "Cited by"]);
  // Both directions carry rows. A complaint whose incoming half were empty
  // would be indistinguishable from one nobody has answered, and this fixture
  // exists so the difference is visible.
  expect(citations.rows[0]).toBeGreaterThan(0);
  expect(citations.rows[1]).toBeGreaterThan(0);

  expect(citations.aim.href).toBe("#/hypotheses/hyp_promoted-pattern");
  // A record this host holds is a link into this app's own route for it, so
  // the reader can walk from the complaint to the work that claims to answer
  // it in one click.
  expect(citations.addressed.inert).toBe(false);
  expect(citations.addressed.href).toBe("#/hypotheses/hyp_unverified-closures");
  expect(citations.addressed.text).toContain("hyp_unverified-closures");

  // And a record this host never held stays on the page as identified text
  // with the server's own reason. Dropping the row would under-report what
  // addressed this complaint, and the operator would read that absence as
  // nobody having answered him.
  expect(citations.elsewhere.inert).toBe(true);
  expect(citations.elsewhere.tag).toBe("SPAN");
  expect(citations.elsewhere.href).toBeNull();
  expect(citations.elsewhere.text).toContain("fnd_absent-on-this-host");
  expect(citations.elsewhere.reason).toContain("holds no finding with that identifier");

  await page.click("[data-citation='rle_hyp-addresses-rules'] .citation-target");
  await page.waitForFunction(
    () => window.location.hash.startsWith("#/hypotheses/"),
    { timeout: 15_000 },
  );
  expect(page.url()).toContain("/hypotheses/hyp_unverified-closures");
});

test.skipIf(!chrome)("day one shows the box and an empty state, not an error", async () => {
  const dayOne = await startMock({ MOCK_PHASEB: "empty" });
  const bare = await browser!.newPage();
  try {
    await bare.setViewport({ width: 1440, height: 900 });
    await bare.goto(`${dayOne.base}/#/review`, { waitUntil: "networkidle2" });
    await bare.reload({ waitUntil: "networkidle2" });
    await bare.waitForSelector(".steering-section .capture-input", { timeout: 15_000 });

    const state = await bare.evaluate(() => {
      const section = document.querySelector(".steering-section");
      return {
        // The box is there on day one. It is the day the operator has the most
        // to say about the tool he used before this one.
        box: section?.querySelectorAll(".capture-input").length ?? -1,
        empty: section?.querySelector(".empty-state strong")?.textContent ?? "",
        rows: section?.querySelectorAll(".steering-table tbody tr").length ?? -1,
        // Nothing told is not a failure to load anything, so no banner and no
        // error state: the route answers an empty list, and the page says so.
        banners: document.querySelectorAll(".error-banner").length,
        errors: section?.querySelectorAll(".inline-error").length ?? -1,
      };
    });

    expect(state.box).toBe(1);
    expect(state.empty).toBe("Nothing has been told yet");
    expect(state.rows).toBe(0);
    expect(state.banners).toBe(0);
    expect(state.errors).toBe(0);
  } finally {
    await bare.close();
    dayOne.process.kill();
  }
});
