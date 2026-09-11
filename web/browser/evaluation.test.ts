// Browser acceptance for issue #219's evaluation surface (SPEC.md §4.12,
// §5.8, §8.5), driven against the synthetic mock so no Go server, archive, or
// network is needed.
//
// What only a browser can prove is here. That the whole lifecycle is reachable
// by moving through the navigation rather than by typing a URL somebody
// already knew. That every ordering names what it is computed from, and that
// choosing one actually changes the answer. That paging stays inside one
// ranked set, survives a reload, and walks back with the browser's own Back
// button. That a record nobody reviewed is found and is not rendered as
// unopposed; that a role with no evaluator reads as a gap rather than a pass;
// that a bare vote acquires no invented rationale; that a verification and the
// contradiction after it are both readable. That the operator's own controls
// record attributed statements, and that the one of them which does change a
// disposition — an explicit reopen — actually reopens the record while its
// opposite leaves the earlier decision standing, whatever the reason text
// says. That /review offers the same reopen and refuses it where nothing was
// decided. That saving a budget says it started no compute. And that there is
// no control anywhere on this surface that casts a vote.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Evaluation surface gate",
  covers: "issue #219's evaluation backlog, coverage inventory, record evaluation and review policy in a browser",
  unverified: [
    "that the evaluation surface is reachable from the navigation and from a record's own page",
    "that each ordering names its basis and actually reorders the listing",
    "that filters, sorts and pages live in the URL and survive reload and Back",
    "that paging stays inside one ranked snapshot",
    "that a never-reviewed record is found and never renders as unopposed",
    "that a role with no evaluator renders as a named gap rather than as a pass",
    "that a bare vote renders bare and acquires no generated rationale",
    "that a verified outcome and a later contradiction are both readable, with scope and date",
    "that grouped alternatives stay separately addressable and are not merged",
    "that a superseded revision says its reception is about the wording on the page",
    "that operator feedback and criteria record attributed statements and decide nothing",
    "that a reconsideration is an explicit reopen or retain, that hostile reason text cannot pick the act, and that a reopen actually reopens the record while a retain leaves it decided",
    "that the review surface reopens a decided record, refuses a reopen where nothing was decided, and keeps the reopened decision in the history",
    "that an unpriced attempt reads as a conservative reserved charge and unresolved criteria name no stand-in identity",
    "that saving a review policy states that it started no compute",
    "that no control on this surface casts a vote",
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

async function open(route: string): Promise<void> {
  await page.goto(`${mock?.base}/#/${route}`, { waitUntil: "networkidle2" });
  await page.reload({ waitUntil: "networkidle2" });
}

// visible waits for text to be on the page AND for the evaluation reader to
// have settled. Both halves are needed: a heading can survive from the page
// being navigated away from, so a bare text match would read the previous
// record's body while the next one is still loading.
function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => {
      const body = document.body.innerText;
      return body.includes(needle) && !body.includes("Reading the evaluation…");
    },
    { timeout: 15_000 },
    text,
  );
}
// follow clicks a link and waits for the navigation it causes.
//
// A click is retried when the element is replaced under it: every page here
// re-renders when its answer lands, which detaches the node Puppeteer just
// scrolled to, and a test that treated that as a failure would be reporting
// the harness rather than the surface. A click that never navigates is still
// a failure, which is what the loop's exit says.
async function follow(selector: string): Promise<void> {
  const before = page.url();
  for (let attempt = 0; attempt < 8; attempt++) {
    await page.waitForSelector(selector);
    try {
      await page.click(selector);
    } catch (error) {
      if (!String(error).includes("detached")) throw error;
    }
    try {
      await page.waitForFunction((url: string) => window.location.href !== url, { timeout: 2_000 }, before);
      return;
    } catch {
      continue;
    }
  }
  throw new Error(`clicking ${selector} never navigated away from ${before}`);
}

// openRow follows one listing row's link into the record it names.
function openRow(item: string): Promise<void> {
  return follow(`[data-item='${item}'] a`);
}

function bodyText(): Promise<string> {
  return page.evaluate(() => document.body.innerText);
}

function ids(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll("[data-item]"))
      .map((row) => row.getAttribute("data-item") ?? ""));
}

beforeAll(async () => {
  if (!chrome) return;
  const build = Bun.spawnSync(["bun", "run", "build"]);
  if (!build.success) throw new Error(`bun run build failed: ${build.stderr.toString()}`);
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

test.skipIf(!chrome)("the whole surface is reachable by navigation, without a guessed URL", async () => {
  await page.goto(`${mock?.base}/#/`, { waitUntil: "networkidle2" });
  await page.waitForSelector("nav");
  // From the primary navigation, not from an address bar.
  await follow("nav a[href='#/evaluation']");
  await visible("Backlog");

  await follow("a[href='#/evaluation/coverage']");
  await visible("Coverage");
  await visible("Never reviewed");

  await follow("a[href='#/evaluation/policy']");
  await visible("Review policy");

  // And back down into one record, from the listing rather than by id.
  await follow("a[href='#/evaluation']");
  await visible("Backlog");
  await openRow("prp_bare-vote");
  await visible("The revision under evaluation");
  expect(page.url()).toContain("/evaluation/proposal/prp_bare-vote");
});

test.skipIf(!chrome)("a record's own page links to its evaluation", async () => {
  // The hypothesis page is served by the Phase B fixtures and the evaluation
  // projection holds the same record, which is what a real deployment looks
  // like: the frontier holds the wording, the projection holds what was said
  // about it. §8.5's reachability is that an operator holding the record can
  // get to its reception without knowing a URL.
  await open("hypotheses/hyp_unverified-closures");
  await visible("Hypothesis");
  expect(await page.$("a[href='#/evaluation/hypothesis/hyp_unverified-closures']")).not.toBeNull();
  await follow("a[href='#/evaluation/hypothesis/hyp_unverified-closures']");
  await visible("The revision under evaluation");
  await visible("hyp_unverified-closures");
  // And the decision surface is reachable from there, rather than being
  // duplicated onto it.
  expect(await page.$("a[href='#/review/hypothesis/hyp_unverified-closures']")).not.toBeNull();
});

test.skipIf(!chrome)("every ordering names its basis and reorders the listing", async () => {
  await open("evaluation");
  await visible("Recommended");
  // §8.5: the ordering says what it is computed from, on the page, not in a
  // document nobody reading it has.
  expect(await bodyText()).toContain("recorded priority, current work and pain");
  const recommended = await ids();

  await page.click("[data-sort='recent']");
  await visible("Newest revisions first");
  const recent = await ids();
  expect(recent).not.toEqual(recommended);

  // Recently strengthened is not "new": it ranks by substantive contribution,
  // and the page has to say so, because another bare vote must not move an
  // item up it.
  await page.click("[data-sort='strengthened']");
  await visible("Another bare vote does not move an item up this order");
  const strengthened = await ids();
  expect(strengthened).not.toEqual(recent);
  // The comparison contribution is the newest substantive one in the fixture.
  expect(strengthened[0]).toBe("prp_group-cache");

  await page.click("[data-sort='contested']");
  await visible("Unresolved disagreement first");
  expect((await ids())[0]).toBe("fnd_no-evaluator");

  await page.click("[data-sort='unreviewed']");
  await visible("how little has been looked at, not how little it was liked");
  const underReviewed = await ids();
  expect(underReviewed).toContain("hyp_never-reviewed");
});

test.skipIf(!chrome)("sorts, filters and pages live in the URL and survive reload and Back", async () => {
  await open("evaluation");
  await page.click("[data-sort='contested']");
  await visible("Unresolved disagreement first");
  expect(page.url()).toContain("sort=contested");
  // The first answer pins the ranked set so paging stays inside one ordering.
  await page.waitForFunction(() => window.location.hash.includes("snapshot="));

  await page.select(".evaluation-filters select", "accepted");
  await page.waitForFunction(() => window.location.hash.includes("lane=accepted"));

  // A reload re-reads the same view rather than dropping to the default.
  await page.reload({ waitUntil: "networkidle2" });
  await visible("Backlog");
  expect(page.url()).toContain("sort=contested");
  expect(page.url()).toContain("lane=accepted");

  // Back undoes the operator's own last choice, not the whole surface.
  await page.goBack({ waitUntil: "networkidle2" });
  await page.waitForFunction(() => !window.location.hash.includes("lane=accepted"));
  expect(page.url()).toContain("sort=contested");
});

test.skipIf(!chrome)("paging stays inside one ranked snapshot and reports its window", async () => {
  await open("evaluation");
  await visible("Recommended");
  await page.waitForFunction(() => window.location.hash.includes("snapshot="));
  const firstPage = await ids();
  const snapshot = await page.evaluate(() =>
    new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("snapshot"));
  expect(snapshot).toBe("snap-2026-09-11T09-00-00Z");
  expect(firstPage.length).toBe(25);

  await page.click(".pager button:last-child");
  await page.waitForFunction(() => window.location.hash.includes("offset=25"));
  // The hash moves before the fetch resolves, so wait for the rendered set
  // to be the second window rather than reading the first one again.
  await page.waitForFunction(
    (first: string) =>
      (document.querySelector("[data-item]")?.getAttribute("data-item") ?? "") !== first,
    {},
    firstPage[0],
  );
  const secondPage = await ids();
  // No row appears on both pages: the ordering was cut once, not re-ranked
  // per page.
  expect(secondPage.some((id) => firstPage.includes(id))).toBe(false);
  // The same snapshot is still pinned, and the page says which window it is.
  expect(page.url()).toContain(`snapshot=${snapshot}`);
  expect(await bodyText()).toContain("snapshot snap-2026-09-11T09-00-00Z");

  await page.click(".pager button:first-child");
  await page.waitForFunction(() => !window.location.hash.includes("offset="));
  await page.waitForFunction(
    (first: string) =>
      (document.querySelector("[data-item]")?.getAttribute("data-item") ?? "") === first,
    {},
    firstPage[0],
  );
  expect(await ids()).toEqual(firstPage);
});

test.skipIf(!chrome)("a never-reviewed record is found and is not rendered as unopposed", async () => {
  await open("evaluation/coverage");
  await visible("Never reviewed");
  const listing = await bodyText();
  // Found by the exact inventory, regardless of score or enrolment.
  expect(listing).toContain("hyp_never-reviewed");

  // Navigated to, not typed: the inventory's rows are links to the record.
  await openRow("hyp_never-reviewed");
  await page.waitForFunction(() => window.location.hash.includes("hyp_never-reviewed"));
  // The record's own heading, not "Reception": the coverage page this click
  // left renders that word in its per-role table, so waiting for it can read
  // the page being navigated away from.
  await visible("The revision under evaluation");
  const text = await bodyText();
  // The absence is stated as an absence. Three zeroes would read as a record
  // nobody objected to.
  expect(text).toContain("no reviews yet");
  expect(text).not.toContain("+0");
  expect(text).toContain("absence of review, not an absence of opposition");
});

test.skipIf(!chrome)("a role with no evaluator reads as a gap, never as a pass", async () => {
  await open("evaluation/finding/fnd_no-evaluator");
  await visible("Coverage by role");
  const roles = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll(".evaluation-roles tbody tr"));
    return rows.map((row) => (row as HTMLElement).innerText);
  });
  const evidence = roles.find((row) => row.startsWith("Evidence check"));
  expect(evidence).toBeDefined();
  expect(evidence).toContain("No evaluator");
  expect(evidence).toContain("no evidence evaluator is registered");
  expect(evidence).not.toContain("Reviewed");

  // A supported role that is not yet required is visible and is not dressed
  // up as overdue work.
  const text = await bodyText();
  expect(text).toContain("supported without being required yet");
});

test.skipIf(!chrome)("a bare vote renders bare, and a superseded revision says so", async () => {
  await open("evaluation/proposal/prp_bare-vote");
  await visible("Evaluation history");
  const text = await bodyText();
  expect(text).toContain("A bare vote. No argument was offered, and none is invented here.");
  expect(text).not.toContain("no comment provided");

  await open("evaluation/proposal/prp_superseded-r1");
  await visible("The revision under evaluation");
  const superseded = await bodyText();
  expect(superseded).toContain("A newer revision of this record exists");
  expect(superseded).toContain("an endorsement does not move to the next revision");
  const forward = await page.$("a[href='#/evaluation/proposal/prp_superseded-r2']");
  expect(forward).not.toBeNull();
});

test.skipIf(!chrome)("a verification and the contradiction after it are both readable", async () => {
  await open("evaluation/proposal/prp_verified-then-contradicted");
  await visible("Observed outcomes");
  const outcomes = await page.evaluate(() =>
    Array.from(document.querySelectorAll(".evaluation-outcome")).map((entry) => (entry as HTMLElement).innerText));
  expect(outcomes.length).toBe(2);
  expect(outcomes[0]).toContain("verified");
  expect(outcomes[1]).toContain("contradicted");
  // Scope and date rather than a timeless badge, and the criterion version
  // each was judged against.
  expect(outcomes[0]).toContain("synthetic corpus, 40 sessions");
  expect(outcomes[1]).toContain("a second host with a larger corpus");
  expect(outcomes[0]).toContain("criteria evr_criteria-1");
  const text = await bodyText();
  expect(text).toContain("A later contradiction does not delete an earlier verification");
  // Criteria settled after acceptance stay identifiable as a later decision.
  expect(text).toContain("settled after acceptance");
});

test.skipIf(!chrome)("competing remedies read together and stay separately addressable", async () => {
  await open("evaluation/proposal/prp_group-cache");
  await visible("Read beside");
  const text = await bodyText();
  expect(text).toContain("nothing here is merged");
  expect(text).toContain("prp_group-skip");
  // The comparison's preference is labelled as being about the comparison,
  // not as a vote for either record.
  expect(text).toContain("preferred here: prp_group-cache");

  expect(await page.$("a[href='#/evaluation/proposal/prp_group-skip']")).not.toBeNull();
  await follow("a[href='#/evaluation/proposal/prp_group-skip']");
  await visible("The revision under evaluation");
  await page.waitForFunction(() =>
    (document.body.innerText ?? "").includes("prp_group-skip") &&
    !(document.body.innerText ?? "").includes("Reading the evaluation…"));
  // The other remedy kept its own decision: deferred, with its own page.
  expect(await bodyText()).toContain("Deferred");
});

test.skipIf(!chrome)("operator feedback is attributed, scoped, and decides nothing", async () => {
  await open("evaluation/proposal/prp_bare-vote");
  await visible("Tell Babel why");
  await page.type("[data-form='feedback'] input[type='text']", "not-now");
  await page.click("[data-form='feedback'] button[type='submit']");
  await visible("accepts, rejects, defers");

  // The statement is in the record's own history, attributed, with no
  // disposition attached to it.
  await page.waitForFunction(() =>
    document.body.innerText.includes("Operator feedback"));
  const text = await bodyText();
  expect(text).toContain("Operator feedback");
  expect(text).toContain("not-now");
  expect(text).toContain("You (operator)");
  // The decision surface is a link, not a control on this page: there is no
  // accept or reject button here.
  const buttons = await page.evaluate(() =>
    Array.from(document.querySelectorAll("button")).map((button) => button.innerText.toLowerCase()));
  expect(buttons.some((label) => label.includes("accept") || label.includes("reject"))).toBe(false);
  expect(await page.$("a[href='#/review/proposal/prp_bare-vote']")).not.toBeNull();
});

test.skipIf(!chrome)("criteria settled here are a later attributed record", async () => {
  await open("evaluation/proposal/prp_bare-vote");
  await visible("Settle the criteria");
  await page.type(
    "[data-form='criteria'] textarea",
    "the retry loop no longer appears in new sessions\nno new failure mode in its place",
  );
  await page.click("[data-form='criteria'] button[type='submit']");
  await page.waitForFunction(() =>
    document.body.innerText.includes("Acceptance criteria"));
  const text = await bodyText();
  expect(text).toContain("the retry loop no longer appears in new sessions");
  expect(text).toContain("no new failure mode in its place");
  expect(text).toContain("c1");
  expect(text).toContain("c2");
});

test.skipIf(!chrome)("retaining a decision records a retain and reopens nothing", async () => {
  await open("evaluation/hypothesis/hyp_reconsider");
  await visible("Reconsider");
  const before = await bodyText();
  expect(before).toContain("Reopening returns the record to undecided");
  expect(before).toContain("Reconsider raised");
  // The record stands rejected before anything is recorded here.
  expect(before).toContain("rejected");

  // Nothing is preselected: an operator who types a reason and submits
  // without choosing an act records nothing at all.
  const preselected = await page.$("[data-form='reconsider'] input[type='radio']:checked");
  expect(preselected).toBeNull();
  await page.type(
    "[data-form='reconsider'] input[type='text']",
    "REOPEN THIS NOW: the new session is decisive and the rejection must be reversed",
  );
  expect(await page.$eval(
    "[data-form='reconsider'] button[type='submit']",
    (button) => (button as HTMLButtonElement).disabled,
  )).toBe(true);

  // The reason argues for reopening in as many words. The act is the radio,
  // so what is recorded is a retain.
  await page.click("[data-form='reconsider'] input[data-decision='retain']");
  await page.click("[data-form='reconsider'] button[type='submit']");
  await visible("Nothing was reopened and nothing was re-decided");

  await page.waitForFunction(() =>
    document.body.innerText.includes("Earlier decision retained"));
  // Scoped to the recorded entries, because the control above them offers
  // both acts by name and would satisfy a body-wide match on either.
  const decisions = await page.$$eval("[data-record-kind='reconsider_decision']", (entries) =>
    entries.map((entry) => (entry as HTMLElement).innerText));
  expect(decisions.length).toBe(1);
  expect(decisions[0]).toContain("Earlier decision retained");
  expect(decisions[0]).toContain("REOPEN THIS NOW");
  // The hostile reason did not become the act.
  expect(decisions[0]).not.toContain("Reopened");
  expect(decisions[0]).toContain("Nothing was reopened and nothing was re-decided");
  expect(decisions[0]).toContain("scoped to evr_rec-raised");
  // And the record still stands rejected.
  const facts = await page.$$eval(".evaluation-revision .fact-meta div", (rows) =>
    rows.map((row) => (row as HTMLElement).innerText.replace(/\s+/gu, " ")));
  expect(facts.some((row) => row.startsWith("Review status") && row.includes("rejected"))).toBe(true);
});

test.skipIf(!chrome)("reopening reopens the record rather than only saying so", async () => {
  await open("evaluation/hypothesis/hyp_reconsider");
  await visible("Reconsider");
  await page.type(
    "[data-form='reconsider'] input[type='text']",
    "not a reversal on the merits, but the benchmark changed",
  );
  await page.click("[data-form='reconsider'] input[data-decision='reopen']");
  await page.click("[data-form='reconsider'] button[type='submit']");

  // The response says the record was reopened, and the reopened status is
  // what the page reads back from the service — an evaluation-only lane flip
  // with /review still saying rejected is the failure this pins.
  await visible("the record was reopened with it");
  await page.waitForFunction(() =>
    Array.from(document.querySelectorAll("[data-record-kind='reconsider_decision']"))
      .some((entry) => (entry as HTMLElement).innerText.includes("Reopened")));
  const decisions = await page.$$eval("[data-record-kind='reconsider_decision']", (entries) =>
    entries.map((entry) => (entry as HTMLElement).innerText));
  const reopened = decisions.filter((entry) => entry.includes("Reopened"));
  expect(reopened.length).toBe(1);
  expect(reopened[0]).toContain("the operator reopened this");
  // The retain recorded by the previous test is still there, unchanged: a
  // later act does not rewrite an earlier one.
  expect(decisions.some((entry) => entry.includes("Earlier decision retained"))).toBe(true);
  const facts = await page.$$eval(".evaluation-revision .fact-meta div", (rows) =>
    rows.map((row) => (row as HTMLElement).innerText.replace(/\s+/gu, " ")));
  expect(facts.some((row) => row.startsWith("Review status") && row.includes("new"))).toBe(true);
  expect(facts.some((row) => row.startsWith("Review status") && row.includes("rejected"))).toBe(false);
  // The reconsider item it answers is still readable beside the decision.
  expect(await bodyText()).toContain("Reconsider raised");
});

test.skipIf(!chrome)("an unpriced attempt is a reserved charge, not free work", async () => {
  await open("evaluation/finding/fnd_no-evaluator");
  await visible("Evaluation history");
  const attempts = await page.$$eval("[data-record-kind='attempt']", (entries) =>
    entries.map((entry) => (entry as HTMLElement).innerText));
  const unpriced = attempts.filter((entry) => entry.includes("the provider reported no cost"));
  expect(unpriced.length).toBe(1);
  // The reservation is what was charged, and the sentence says why it is not
  // an observation. A zero here would read as work that cost nothing.
  expect(unpriced[0]).toContain("charged 0.0500 as the reservation");
  expect(unpriced[0]).toContain("unmeasured work is not free work");
  // The skip that genuinely ran nothing still reads as a reported zero.
  const priced = attempts.filter((entry) => entry.includes("as the provider reported it"));
  expect(priced.length).toBe(1);
  expect(priced[0]).toContain("cost 0.0000");
});

test.skipIf(!chrome)("criteria with no operator record say so instead of naming a stand-in", async () => {
  // A proposal that states criteria about itself, with no operator criteria
  // record behind them.
  await open("evaluation/proposal/prp_bare-vote");
  await visible("What this is measured against");
  const panel = await page.$eval(".evaluation-accepted-criteria", (card) => ({
    resolved: card.getAttribute("data-criteria-resolved"),
    text: (card as HTMLElement).innerText,
  }));
  expect(panel.resolved).toBe("no");
  expect(panel.text).toContain("not linked to an operator criteria record");
  // Nothing plausible-looking is offered in place of the missing identity:
  // not the context version, not a digest, not an id of any kind.
  expect(panel.text).not.toContain("ctx-7");
  expect(panel.text).not.toMatch(/\bevr_/u);

  // The accepted, verified proposal names the record its criteria came from.
  await open("evaluation/proposal/prp_verified-then-contradicted");
  await visible("What this is measured against");
  const resolved = await page.$eval(".evaluation-accepted-criteria", (card) => ({
    resolved: card.getAttribute("data-criteria-resolved"),
    text: (card as HTMLElement).innerText,
  }));
  expect(resolved.resolved).toBe("yes");
  expect(resolved.text).toContain("evr_criteria-1");
  expect(resolved.text).not.toContain("not linked to an operator criteria record");
});

test.skipIf(!chrome)("the review surface reopens a decided record and keeps its history", async () => {
  await open("review/finding/fnd_conflicting-evidence");
  await visible("Record a decision");

  // Reopening something nobody decided is refused by the service, and the
  // page shows the refusal rather than a decision.
  await page.click(".decide-card input[value='reopen']");
  expect(await bodyText()).toContain("why the earlier decision stopped holding");
  await page.type(".decide-card textarea", "reopening what was never decided");
  page.once("dialog", (dialog) => dialog.accept());
  await page.click(".decide-card button[type='submit']");
  await visible("nothing has been decided here to reopen");

  // Defer it, then reopen it: the deferral stays in the history and the
  // status returns to new.
  await page.click(".decide-card input[value='defer']");
  // Cleared the way a person clears it, so React sees the change.
  await page.focus(".decide-card textarea");
  await page.keyboard.down("Control");
  await page.keyboard.press("KeyA");
  await page.keyboard.up("Control");
  await page.keyboard.press("Backspace");
  await page.type(".decide-card textarea", "not now; the next corpus run settles it");
  page.once("dialog", (dialog) => dialog.accept());
  await page.click(".decide-card button[type='submit']");
  await visible("status is now deferred");

  await page.click(".decide-card input[value='reopen']");
  await page.type(".decide-card textarea", "the deferral's reason no longer holds");
  page.once("dialog", (dialog) => dialog.accept());
  await page.click(".decide-card button[type='submit']");
  await visible("status is now new");
  const after = await bodyText();
  // Both events are in the history, each with its own reason: the reopen
  // rewrote nothing.
  expect(after).toContain("not now; the next corpus run settles it");
  expect(after).toContain("the deferral's reason no longer holds");
  const decisions = await page.$$eval(".timeline li", (entries) =>
    entries.map((entry) => (entry as HTMLElement).innerText));
  expect(decisions.some((entry) => entry.includes("defer"))).toBe(true);
  expect(decisions.some((entry) => entry.includes("reopen"))).toBe(true);
});

test.skipIf(!chrome)("the policy states what is running, and saving it starts nothing", async () => {
  await open("evaluation/policy");
  await visible("Review policy");
  // A paused deployment says so, and says it differently from "unavailable".
  await page.waitForSelector("[data-status='paused']");
  let text = await bodyText();
  expect(text).toContain("authorized evaluation work is paused");
  expect(text).toContain("starts no run, launches no compute");
  // The disclaimer is present before the save as well as after it.
  expect(await page.$("[data-status='paused']")).not.toBeNull();

  // Enabling the policy does not make anything run: with nothing claimed the
  // honest state is "awaiting its next scheduled draw".
  await page.click(".evaluation-enabled input[type='checkbox']");
  await page.click(".evaluation-policy-form button[type='submit']");
  await page.waitForSelector("[data-status='scheduled']");
  text = await bodyText();
  expect(text).toContain("enabled with nothing in flight");
  expect(text).not.toContain("Running");
  // The change is a record, not a settings blob.
  expect(text).toContain("attributed record");

  // A budget edit is stored and read back from the service rather than
  // echoed from the form, which is what makes the version bump beside it
  // meaningful.
  const before = await page.$eval("[data-knob='daily_cost']", (input) => (input as HTMLInputElement).value);
  await page.click("[data-knob='daily_cost']", { count: 3 });
  await page.keyboard.type("9");
  await page.click(".evaluation-policy-form button[type='submit']");
  await page.waitForFunction(() => document.body.innerText.includes("eval-policy-3"));
  const after = await page.$eval("[data-knob='daily_cost']", (input) => (input as HTMLInputElement).value);
  expect(after).toBe("9");
  expect(after).not.toBe(before);
  // Storing a larger ceiling still started nothing.
  expect(await page.$("[data-status='scheduled']")).not.toBeNull();
  expect(await bodyText()).toContain("starts no run, launches no compute");
});

test.skipIf(!chrome)("no control on this surface casts a vote", async () => {
  for (const route of [
    "evaluation",
    "evaluation/coverage",
    "evaluation/policy",
    "evaluation/proposal/prp_bare-vote",
    "evaluation/finding/fnd_no-evaluator",
  ]) {
    await open(route);
    await page.waitForSelector(".page");
    const controls = await page.evaluate(() =>
      Array.from(document.querySelectorAll("button, input[type='submit'], select"))
        .map((control) => ((control as HTMLElement).innerText || (control as HTMLInputElement).value || "").toLowerCase()));
    for (const label of controls) {
      expect(label).not.toContain("upvote");
      expect(label).not.toContain("downvote");
      expect(label).not.toMatch(/\bvote\b/u);
    }
  }
});

// The two states a reader must be able to tell apart, each on its own launch
// because each is a different server: a projection that answered late, and a
// build with no evaluation store at all. §8.5 requires the first to be
// readable and labelled rather than withheld, and the second to be a stated
// refusal rather than an empty page that looks like a covered corpus.
async function withLaunch(env: Record<string, string>, run: (visit: (route: string) => Promise<void>, read: () => Promise<string>) => Promise<void>) {
  const server = await startMock(env);
  const tab = await browser!.newPage();
  await tab.setViewport({ width: 1440, height: 900 });
  try {
    await run(
      async (route: string) => {
        await tab.goto(`${server.base}/#/${route}`, { waitUntil: "networkidle2" });
        await tab.reload({ waitUntil: "networkidle2" });
      },
      () => tab.evaluate(() => document.body.innerText),
    );
  } finally {
    await tab.close();
    server.process.kill();
  }
}

test.skipIf(!chrome)("a stale projection is labelled and still readable", async () => {
  await withLaunch({ MOCK_EVALUATION: "degraded" }, async (visit, read) => {
    await visit("evaluation");
    const text = await read();
    expect(text).toContain("This ordering is not current");
    // The reason is the server's own sentence, and the rows are still there:
    // a reader told the ordering is old can use it, a reader shown nothing
    // cannot.
    expect(text).toContain("could not be opened from the shared catalog");
    expect(text).toContain("not labelled as the deployment's current tally");
    expect(text).toContain("Synthetic");

    // The policy page cannot claim what is running from a degraded
    // inventory, and says that rather than reporting "paused".
    await visit("evaluation/policy");
    const policy = await read();
    expect(policy).toContain("Unavailable");
    expect(policy).toContain("cannot be stated from it");
    expect(policy).not.toContain("authorized evaluation work is paused");
  });
});

test.skipIf(!chrome)("a build with no evaluation store refuses and says so", async () => {
  await withLaunch({ MOCK_UNWIRED: "evaluation" }, async (visit, read) => {
    await visit("evaluation");
    const text = await read();
    expect(text).toContain("could not be loaded");
    expect(text).toContain("evaluation service is not available in this session");
    // Not an empty backlog: a refusal and "nothing is owed" are different
    // claims, and the empty state must not stand in for the refusal.
    expect(text).not.toContain("Nothing matches this view");

    // Every other page keeps working, which is the degradation the rest of
    // this surface already promises.
    await visit("hypotheses");
    expect(await read()).toContain("Hypotheses");
  });
});
