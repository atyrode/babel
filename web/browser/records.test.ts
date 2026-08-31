// Browser acceptance for issue #87's record actions, driven against the
// synthetic mock so no Go server, archive, or network is needed.
//
// What only a browser can prove is here. That the revision history renders as a
// history — every wording, its author, and why it replaced the one before it.
// That authorizing a proposed action is a deliberate act that reads back, and
// that the interface says in as many words that nothing was published. That the
// "process further" button records a nudge and shows it queued, with nowhere to
// type an instruction. That reviving a resting candidate refuses to proceed
// without a reason. And that a record revised while the page was open refuses
// the click and explains itself instead of recording a decision about words
// nobody read.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type Page } from "puppeteer-core";
import { HOSTILE_HTML } from "../mock/phaseb";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Record actions gate",
  covers: "issue #87's record actions -- revision history, dispositions, process-further and revive -- in a browser",
  unverified: [
    "that a record's revision chain renders with its authors and its reasons, and that a superseded wording is still reachable",
    "that authorizing a proposed action records an attributed ruling and states that Babel published nothing",
    "that a draft-issue's rendered draft stays closed until a reader opens it, and is never opened or filed by the interface",
    "that 'process further' records an instruction-free invitation and shows it queued",
    "that reviving a resting candidate refuses without a stated reason",
    "that a record revised after the page was rendered refuses the mutation and explains why",
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

function visible(text: string): Promise<unknown> {
  return page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 15_000 },
    text,
  );
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
  // Every mutation on this surface confirms itself through window.confirm, so
  // the dialog is accepted by default; the one test about the refusal path
  // dismisses it explicitly.
  page.on("dialog", (dialog) => void dialog.accept());
});

afterAll(async () => {
  await browser?.close();
  mock?.process.kill();
});

test.skipIf(!chrome)("a record's revision chain renders with its authors and reasons", async () => {
  await open("hypotheses/hyp_unverified-closures");
  await visible("Revision history");
  const state = await page.evaluate(() => {
    const entries = Array.from(document.querySelectorAll(".revision-timeline .timeline-entry"));
    return {
      count: entries.length,
      text: entries.map((entry) => (entry as HTMLElement).innerText),
      framing: document.body.innerText.includes("supersedes its predecessor"),
    };
  });
  // Two wordings: the run's original and the operator's revision of it.
  expect(state.count).toBe(2);
  expect(state.text[0]).toContain("run · revision 1");
  // An original supersedes nothing, so it states no reason, and the interface
  // says that rather than leaving an empty line.
  expect(state.text[0]).toContain("supersedes nothing");
  expect(state.text[1]).toContain("operator · revision 2");
  expect(state.text[1]).toContain("narrower claim");
  expect(state.text[1]).toContain("current");
  expect(state.framing).toBe(true);
});

test.skipIf(!chrome)("authorizing a proposed action records a ruling and publishes nothing", async () => {
  await open("hypotheses/hyp_unverified-closures");
  await visible("Dispositions");
  // The epistemic frame is on the block itself, not on a separate page.
  const framing = await page.evaluate(() => document.body.innerText);
  expect(framing).toContain("authorizes the action rather than performing it");

  await page.click("[data-disposition-accept='dsp_001']");
  await visible("Babel published nothing");
  const state = await page.evaluate(() => {
    const entry = document.querySelector("[data-disposition-accept='dsp_001']")?.closest(".disposition-entry");
    return {
      text: (entry as HTMLElement | null)?.innerText ?? "",
      status: document.body.innerText.includes("accepted"),
    };
  });
  expect(state.text).toContain("accepted");
  expect(state.text).toContain("operator");
  expect(state.status).toBe(true);

  // The ruling survives a reload, because it is a durable record rather than
  // page state, and the declined action beside it is still readable.
  await open("hypotheses/hyp_unverified-closures");
  await visible("Dispositions");
  const reread = await page.evaluate(() => document.body.innerText);
  expect(reread).toContain("accepted");
  expect(reread).toContain("declined");
  expect(reread).toContain("stays readable");
});

test.skipIf(!chrome)("a draft-issue's draft is text, closed until a reader opens it", async () => {
  await open("hypotheses/hyp_lens-overlap");
  await visible("draft-issue");
  const closed = await page.evaluate(() => {
    const details = document.querySelector(".draft-disclosure") as HTMLDetailsElement | null;
    return { present: details !== null, open: details?.open ?? true };
  });
  expect(closed.present).toBe(true);
  // Never auto-opened: reading the text an operator would paste into a public
  // issue has to be the reader's own act.
  expect(closed.open).toBe(false);

  await page.click(".draft-disclosure summary");
  await visible("published nothing");
  const state = await page.evaluate(
    (needle: string) => ({
      pwned: String(Reflect.get(globalThis, "__babel_pwned")),
      injectedImage: document.querySelector(".draft-disclosure img") !== null,
      scriptURL: Array.from(document.querySelectorAll(".dispositions-card a"))
        .some((anchor) => (anchor as HTMLAnchorElement).href.startsWith("javascript:")),
      literal: (document.querySelector(".draft-disclosure pre") as HTMLElement | null)?.innerText.includes(needle),
      anchored: document.body.innerText.includes("git@github.com:atyrode/synthetic-preview"),
    }),
    HOSTILE_HTML,
  );
  // The draft is inert: hostile bytes inside it render as the text they are.
  expect(state.pwned).toBe("undefined");
  expect(state.injectedImage).toBe(false);
  expect(state.scriptURL).toBe(false);
  expect(state.literal).toBe(true);
  // #88's anchor travels with the draft, so a reader can see which repository
  // it binds to before authorizing anything.
  expect(state.anchored).toBe(true);
});

test.skipIf(!chrome)("process further records an instruction-free invitation and shows it queued", async () => {
  await open("hypotheses/hyp_unverified-closures");
  await visible("Process further");
  const before = await page.evaluate(() => ({
    queued: document.querySelector("[data-invite-queued]") !== null,
    // The one thing this card must not have: a place to write a brief.
    fields: document.querySelectorAll(".invite-card textarea, .invite-card input").length,
    framing: document.body.innerText.includes("nowhere here to write an instruction"),
  }));
  expect(before.queued).toBe(false);
  expect(before.fields).toBe(0);
  expect(before.framing).toBe(true);
  await page.click("[data-invite='hyp_unverified-closures']");
  await visible("the next run's judgement");
  // The queued marker appears when the reload lands, which is after the
  // outcome line: the invitation is read back from the server rather than
  // asserted by the page that sent it.
  await page.waitForFunction(
    () => document.querySelector("[data-invite-queued]")?.getAttribute("data-invite-queued") === "1",
    { timeout: 15_000 },
  );
  const after = await page.evaluate(
    () => (document.querySelector("[data-invite-queued]") as HTMLElement | null)?.innerText ?? "",
  );
  expect(after).toContain("queued");
  // A second ask is a second invitation: the operator asked twice, and the
  // queue says so rather than deduplicating the second click away.
  await page.click("[data-invite='hyp_unverified-closures']");
  await page.waitForFunction(
    () => document.querySelector("[data-invite-queued]")?.getAttribute("data-invite-queued") === "2",
    { timeout: 15_000 },
  );
});

test.skipIf(!chrome)("reviving a resting candidate requires a stated reason", async () => {
  // A promoted candidate: #87 makes even that a resting place rather than an
  // ending, which is the case most likely to read as closed.
  await open("hypotheses/hyp_promoted-pattern");
  await visible("Revive");
  const framing = await page.evaluate(() => ({
    text: document.body.innerText,
    reason: document.querySelector("[data-revive-reason]") !== null,
  }));
  expect(framing.text).toContain("resting place, not an ending");
  expect(framing.reason).toBe(true);

  // Clicking with an empty reason refuses locally and records nothing.
  await page.click("[data-revive='hyp_promoted-pattern']");
  await visible("states why the candidate deserves to move again");
  const refused = await page.evaluate(() => ({
    alert: document.querySelector(".revive-card .inline-error")?.textContent ?? "",
    status: document.querySelector(".heading-badges .badge")?.textContent ?? "",
  }));
  expect(refused.alert).toContain("states why");
  expect(refused.status).toBe("promoted");

  await page.type("[data-revive-reason]", "A newer session contradicts the promoted wording.");
  await page.click("[data-revive='hyp_promoted-pattern']");
  await visible("Revived.");
  const revived = await page.evaluate(() => document.body.innerText);
  expect(revived).toContain("queued");
  // The rejection it left is still in the history: reviving appends, and the
  // status history keeps every state the candidate has held.
  expect(revived).toContain("promoted");
});

test.skipIf(!chrome)("a record revised after the page was rendered refuses the click", async () => {
  // hyp_many-observations is the raced fixture: the mock hands back the wording
  // that was current when the page read it and then a synthetic run revises it,
  // which is exactly the state the confirmation contract exists for.
  await open("hypotheses/hyp_many-observations");
  await visible("Process further");
  await page.click("[data-invite='hyp_many-observations']");
  await visible("revised after the page was rendered");
  const state = await page.evaluate(() => {
    const alert = document.querySelector(".invite-card .inline-error") as HTMLElement | null;
    return {
      text: alert?.innerText ?? "",
      role: alert?.getAttribute("role"),
      queued: document.querySelector("[data-invite-queued]") !== null,
    };
  });
  // The refusal explains itself, names the wording that replaced the one shown,
  // and tells the operator what to do about it.
  expect(state.text).toContain("revised after the page was rendered");
  expect(state.text).toContain("hyp_many-observations@r");
  expect(state.text).toContain("Reload");
  expect(state.role).toBe("alert");
  // And nothing was queued: the refusal happened instead of the write.
  expect(state.queued).toBe(false);
});

test.skipIf(!chrome)("the dashboard counts proposed actions and says why each run happened", async () => {
  await open("");
  await page.waitForSelector(".panel--review", { timeout: 15_000 });
  const state = await page.evaluate(() => {
    const review = document.querySelector(".panel--review") as HTMLElement | null;
    const receipts = Array.from(document.querySelectorAll(".receipt-authority")) as HTMLElement[];
    return {
      review: review?.innerText ?? "",
      authorities: receipts.map((mark) => mark.innerText),
    };
  });
  expect(state.review.toLowerCase()).toContain("proposed actions");
  // Both renderings: an authority the receipt recorded, and the honest absence
  // on one written before receipts carried the field.
  expect(state.authorities.join(" ")).toContain("policy");
  expect(state.authorities.join(" ")).toContain("recorded before authority");
});
