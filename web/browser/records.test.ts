// Browser acceptance for the acts a record carries, driven against the
// synthetic mock so no Go server, archive, or network is needed.
//
// One record has one page now (#235), so everything below happens on /r/:id:
// the ruling that used to be a five-radio ballot on /review/:type/:id is the
// rule bar and its one-sentence confirmation, and the chain of wordings that
// used to be a card of its own is at depth 5 with the rest of the machinery.
// The acts themselves are unchanged, and they are what this file measures.
//
// What only a browser can prove is here. That a ruling is a deliberate act:
// pressing a disposition asks rather than records, cancelling records nothing,
// and only the confirmation writes. That what it writes is appended and
// attributed — the earlier ruling stays readable beside the later one — and
// that the record's standing moves with it and survives a reload. That a
// reopen refuses to proceed without a stated reason, because undoing a
// decision that has already been made is the one ruling that needs one. That a
// record nothing can be ruled on carries no ruling control at all rather than
// one the service would refuse. That the chain of wordings a record has had is
// readable, in the server's own order. And that a receipt says why its run
// happened, on Watch and on the run's own page.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import puppeteer, { type Browser, type HTTPRequest, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const chrome = resolveChrome({
  gate: "Record actions gate",
  covers: "the ruling bar, its confirmation, a record's chain and a run's authority, in a browser",
  unverified: [
    "that pressing a disposition asks for a confirmation and records nothing until it is confirmed",
    "that a recorded ruling is attributed, appended beside the earlier ones, and moves the record's standing durably",
    "that a reopen refuses without a stated reason and leaves the ruling it would undo in place",
    "that a record carrying no review decision offers no ruling control at all",
    "that the chain of wordings a record has had renders at depth 5 in the order the server sent",
    "that a receipt says why its run happened, on Watch and on the run's own page",
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

// dig opens one depth by pressing its own summary, which is what a reader
// does. The peel's open state belongs to the page rather than to the <details>
// — Contract K lets `1`-`5` toggle a depth from anywhere — so the click has to
// land on the control and not on the element's `open` attribute.
async function dig(title: string): Promise<string> {
  await page.evaluate((needle: string) => {
    const peel = Array.from(document.querySelectorAll("details.peel")).find((entry) =>
      (entry.querySelector("summary")?.textContent ?? "").startsWith(needle),
    );
    if (!(peel as HTMLDetailsElement | undefined)?.open) {
      (peel?.querySelector("summary") as HTMLElement | undefined)?.click();
    }
  }, title);
  const body = await page.waitForFunction(
    (needle: string) => {
      const peel = Array.from(document.querySelectorAll("details.peel")).find((entry) =>
        (entry.querySelector("summary")?.textContent ?? "").startsWith(needle),
      );
      if (!peel || !(peel as HTMLDetailsElement).open) return null;
      return (peel.querySelector(".peel-body") as HTMLElement | null)?.innerText ?? "";
    },
    { timeout: 15_000 },
    title,
  );
  return (await body.jsonValue()) as string;
}

// served reads the same record the page read, from the page itself. The
// assertions below compare what is rendered against what the server sent
// rather than against a literal copied out of the fixtures: a test that
// carried its own list of revisions would keep passing after the page stopped
// reading the server's.
function served(id: string): Promise<Record<string, unknown>> {
  return page.evaluate(
    async (record: string) =>
      (await fetch(`/api/record/${encodeURIComponent(record)}`).then((response) =>
        response.json(),
      )) as Record<string, unknown>,
    id,
  );
}

interface Ruling {
  disposition: string;
  by?: string;
  note?: string;
}

// rulings is the §4.7 ledger as the server holds it: the list the page reads
// back at depth 4, which is what makes "recorded" and "not recorded" a fact
// about the store rather than about the DOM.
async function rulings(id: string): Promise<Ruling[]> {
  const record = (await served(id)) as { reception?: { decisions?: Ruling[] } };
  return record.reception?.decisions ?? [];
}

function standing(): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll(".heading-badges .badge")).map(
      (badge) => badge.textContent ?? "",
    ),
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
});

afterAll(async () => {
  await browser?.close();
  mock?.process.kill();
});

test.skipIf(!chrome)("a record's chain of wordings is readable at depth 5", async () => {
  await open("r/hyp_unverified-closures");
  await visible("The machinery");
  const machinery = await dig("The machinery");
  const record = (await served("hyp_unverified-closures")) as {
    machinery?: { revision?: string; revisions?: Array<{ id: string; at?: string }> };
  };
  const chain = record.machinery?.revisions ?? [];
  // The fixture is a record that has been reworded, which is the case the
  // chain exists for: a single-wording record could not tell a list that
  // renders its history from one that renders only its head.
  expect(chain.length).toBeGreaterThan(1);

  const rendered = await page.evaluate(() => {
    const panel = Array.from(document.querySelectorAll(".peel-body .panel")).find(
      (section) => section.querySelector("h3")?.textContent === "Revisions",
    );
    return Array.from(panel?.querySelectorAll("li") ?? []).map(
      (entry) => (entry as HTMLElement).innerText,
    );
  });

  // Every wording, in the order the server sent them, each with when it was
  // recorded. A page that rendered only the head would be reporting the
  // record's current text as the whole of its history.
  expect(rendered).toHaveLength(chain.length);
  for (const [index, revision] of chain.entries()) {
    expect(rendered[index]).toContain(revision.id);
  }
  expect(rendered.every((entry) => /\d{4}/u.test(entry))).toBe(true);
  // And the wording on screen is named: the head is the revision this page is
  // showing, and it is stated beside the chain rather than inferred from it.
  expect(machinery).toContain(record.machinery?.revision ?? "");
});

test.skipIf(!chrome)("a ruling is confirmed before it is recorded, and it is appended", async () => {
  const id = "hyp_unverified-closures";
  await open(`r/${id}`);
  await visible("The claim");

  const bar = await page.evaluate(() => ({
    dispositions: Array.from(document.querySelectorAll("[data-ruling]")).map((button) =>
      button.getAttribute("data-ruling"),
    ),
    group: document
      .querySelector(".rule-bar[aria-label^='Rule on']")
      ?.getAttribute("aria-label") ?? "",
    // The ballot is gone: the reader has already decided, and the bar takes
    // the decision rather than presenting the options as a form to fill in.
    radios: document.querySelectorAll("input[type='radio']").length,
    // Every field on the page that is not the thread's comment box. §8.7's box
    // is the operator's own words about the record and belongs to nothing
    // being confirmed; the ruling's note is what must not exist yet.
    fields: Array.from(document.querySelectorAll("textarea"))
      .filter((field) => !field.closest(".record-comment-form")).length,
  }));
  expect(bar.dispositions).toContain("accept");
  expect(bar.dispositions).toContain("defer");
  expect(bar.dispositions).toContain("reopen");
  expect(bar.group).toContain("hypothesis");
  expect(bar.radios).toBe(0);
  // Nothing to type into until a disposition is chosen: the note belongs to
  // the ruling being confirmed, not to the page.
  expect(bar.fields).toBe(0);

  // Pressing a disposition asks. It does not rule.
  await page.click("[data-ruling='accept']");
  await page.waitForSelector(".record-confirm", { timeout: 15_000 });
  expect(await rulings(id)).toHaveLength(0);
  expect(await standing()).toContain("new");

  // And cancelling is a way out that records nothing, which is the whole
  // reason the confirmation exists.
  await page.click(".record-confirm button:not([type='submit'])");
  await page.waitForFunction(() => document.querySelector(".record-confirm") === null, {
    timeout: 15_000,
  });
  expect(await rulings(id)).toHaveLength(0);

  await page.click("[data-ruling='accept']");
  await page.waitForSelector(".record-confirm", { timeout: 15_000 });
  await page.click(".record-confirm button[type='submit']");

  // The standing moves with the ruling: the page re-reads the record rather
  // than showing the old standing beside the button that changed it.
  await page.waitForFunction(
    () =>
      Array.from(document.querySelectorAll(".heading-badges .badge")).some(
        (badge) => badge.textContent === "accepted",
      ),
    { timeout: 15_000 },
  );

  const recorded = await rulings(id);
  expect(recorded).toHaveLength(1);
  expect(recorded[0].disposition).toBe("accept");
  // Attributed: a ruling is somebody's act, and the store names who.
  expect(recorded[0].by).toBeTruthy();

  const acted = await page.evaluate(() => {
    const reception = Array.from(document.querySelectorAll("details.peel")).find((peel) =>
      (peel.querySelector("summary")?.textContent ?? "").startsWith("The reception"),
    );
    return {
      // An act the reader performs is an act he has to be able to see: the
      // depth his ruling landed in opens itself.
      open: (reception as HTMLDetailsElement | undefined)?.open ?? false,
      rulings: (reception?.querySelector(".peel-body") as HTMLElement | null)?.innerText ?? "",
      announced: document.querySelector("[role='status']")?.textContent ?? "",
    };
  });
  expect(acted.open).toBe(true);
  expect(acted.rulings).toContain("accept");
  expect(acted.rulings).toContain(recorded[0].by ?? "");
  expect(acted.announced).toContain("accept");

  // Durable rather than page state: the ruling is still there after a reload,
  // and it is still the record's standing.
  await open(`r/${id}`);
  await visible("The claim");
  expect(await standing()).toContain("accepted");
  const reread = await dig("The reception");
  expect(reread).toContain("accept");
});

test.skipIf(!chrome)("reopening refuses without a stated reason and appends to the ruling it undoes", async () => {
  // Reopening is the one disposition that undoes a decision, so the test makes
  // the decision it undoes: a record nothing has been ruled on has nothing to
  // reopen, and the service says so. Doing it here rather than picking a
  // pre-decided fixture keeps the test independent of what the tests above
  // recorded.
  const id = "pro_criteria-template";
  await open(`r/${id}`);
  await visible("The claim");
  expect(await rulings(id)).toHaveLength(0);

  await page.click("[data-ruling='defer']");
  await page.waitForSelector(".record-confirm", { timeout: 15_000 });
  await page.click(".record-confirm button[type='submit']");
  await page.waitForFunction(
    () =>
      Array.from(document.querySelectorAll(".heading-badges .badge")).some(
        (badge) => badge.textContent === "deferred",
      ),
    { timeout: 15_000 },
  );

  // Count the writes rather than waiting to see whether one lands: a refusal
  // proved by a sleep is a refusal proved by nothing under load.
  const decides: string[] = [];
  const watch = (request: HTTPRequest) => {
    if (request.url().includes("/api/review/decide")) decides.push(request.method());
  };
  page.on("request", watch);

  await page.click("[data-ruling='reopen']");
  await page.waitForSelector(".record-confirm", { timeout: 15_000 });
  const asked = await page.evaluate(() => {
    const field = document.querySelector(".record-confirm textarea") as HTMLTextAreaElement | null;
    return {
      required: field?.required ?? false,
      valid: field?.checkValidity() ?? true,
      label: field?.closest("label")?.textContent ?? "",
    };
  });
  expect(asked.required).toBe(true);
  expect(asked.valid).toBe(false);
  // The field asks a question rather than naming a column: what stopped
  // holding, not "note".
  expect(asked.label.toLowerCase()).toContain("why");

  // Confirming with nothing typed sends nothing and records nothing.
  await page.click(".record-confirm button[type='submit']");
  expect(decides).toEqual([]);
  expect(await rulings(id)).toHaveLength(1);
  expect(await page.$(".record-confirm")).not.toBeNull();

  const reason = "The template has since been tried on a real corpus, so the deferral is spent.";
  await page.type(".record-confirm textarea", reason);
  await page.click(".record-confirm button[type='submit']");
  await page.waitForFunction(() => document.querySelector(".record-confirm") === null, {
    timeout: 15_000,
  });
  page.off("request", watch);
  // One write for the whole act: the refused press sent nothing, and the
  // confirmed one is not sent twice.
  expect(decides).toEqual(["POST"]);

  const after = await rulings(id);
  // Appended, never edited: the deferral it undoes keeps its place, and the
  // reason travels with the reopen.
  expect(after).toHaveLength(2);
  expect(after[0].disposition).toBe("defer");
  expect(after[1].disposition).toBe("reopen");
  expect(after[1].note).toContain("real corpus");

  const shown = await dig("The reception");
  expect(shown).toContain("defer");
  expect(shown).toContain("reopen");
  expect(shown).toContain(reason);
});

// Four acts this file used to measure are not on any surface now, and are not
// asserted anywhere else either. They are named here rather than dropped
// without a trace, because a guarantee that disappears silently is
// indistinguishable from one that was never made:
//
//   - the §4.7 proposed actions a record carries — develop-further,
//     draft-issue, store-memory, ask-operator-question — with the framing that
//     authorizing one performs none of it and publishes nothing, and the
//     rendered draft that stayed closed until a reader opened it;
//   - "process further", the invitation with nowhere to write an instruction;
//   - reviving a resting candidate onto the frontier with a stated reason;
//   - and the refusal that guarded all three: every one of those mutations
//     sent the chain head the page was rendered against, and a record revised
//     since was refused with an explanation instead of a recorded decision.
//
// web/src/records.tsx still implements all four, and nothing imports it. The
// two writes this surface does make — a stance and a ruling — send no head, so
// the raced fixture in web/mock/phaseb.ts is unreachable from the browser.

test.skipIf(!chrome)("a record that carries no review decision offers no ruling", async () => {
  // §6.7 makes an observation evidence rather than a review subject, and
  // internal/review refuses a disposition about one. The control is absent
  // rather than present and refused — but the reader's own position is not a
  // ruling, so his vote stays.
  await open("r/obs_claim-no-verify");
  await visible("The claim");
  const bar = await page.evaluate(() => ({
    rulings: document.querySelectorAll("[data-ruling]").length,
    stances: Array.from(document.querySelectorAll("[data-stance]")).map((button) =>
      button.getAttribute("data-stance"),
    ),
  }));
  expect(bar.rulings).toBe(0);
  // Two arrows and no third button: §8.7 makes unsure what pressing a lit
  // arrow again records, so the withdrawal is a gesture rather than a control.
  expect(bar.stances).toEqual(["agree", "disagree"]);

  // And a record that does carry one has it, so the absence above is about
  // this record rather than about the page having lost the control.
  await open("r/pro_criteria-template");
  await visible("The claim");
  expect(await page.evaluate(() => document.querySelectorAll("[data-ruling]").length))
    .toBeGreaterThan(0);
});

test.skipIf(!chrome)("a receipt says why its run happened", async () => {
  // The dashboard that used to carry this beside a count of proposed actions
  // is gone (#235): it was six panels summarizing five other pages. The
  // authority mark itself is what mattered and it rides the receipts on Watch,
  // which is where "what did it cost, and why did it run" is answered now.
  await open("watch");
  await page.waitForSelector(".runs-table .receipt-authority", { timeout: 15_000 });

  const table = await page.evaluate(async () => {
    const rows = Array.from(document.querySelectorAll(".runs-table tbody tr"));
    // The same read the page makes, so the comparison is against what the
    // server said about these runs rather than against a copy of the fixtures.
    const listing = (await fetch("/api/watch/runs?limit=20").then((response) =>
      response.json(),
    )) as { runs?: Array<{ run_id: string; authority?: { kind?: string; ref?: string } }> };
    const served: Record<string, { kind?: string; ref?: string }> = {};
    for (const row of listing.runs ?? []) served[row.run_id] = row.authority ?? {};
    return {
      shown: rows.map((row) => ({
        run: (row.querySelector(".runs-open .mono") as HTMLElement | null)?.innerText ?? "",
        mark: (row.querySelector(".receipt-authority") as HTMLElement | null)?.innerText ?? "",
      })),
      served,
    };
  });

  // Every receipt says why. The mark is the authority the receipt recorded —
  // its kind and its reference — and never a word this page chose: authority
  // is why Babel spent the tokens, and inventing one would be this interface
  // manufacturing provenance.
  expect(table.shown.length).toBeGreaterThan(0);
  for (const row of table.shown) {
    const authority = table.served[row.run];
    expect(authority).toBeDefined();
    expect(row.mark).toContain(authority.kind ?? "");
    if (authority.ref) expect(row.mark).toContain(authority.ref);
  }

  // And the run's own page answers the same question in its own words, which
  // is where an operator arriving from a link reads it.
  await page.click(".runs-table tbody tr .runs-open");
  await page.waitForFunction(() => window.location.hash.includes("/watch/runs/"), {
    timeout: 15_000,
  });
  await visible("What it was asked to do");
  const receipt = await page.evaluate(async () => {
    const id = decodeURIComponent(window.location.hash.split("/watch/runs/")[1] ?? "");
    const detail = (await fetch(`/api/watch/runs/${encodeURIComponent(id)}`).then((response) =>
      response.json(),
    )) as { authority?: { kind?: string; ref?: string } };
    const rows = Array.from(document.querySelectorAll(".run-facts div"));
    const authority = rows.find((row) => row.querySelector("dt")?.textContent === "Authority");
    return {
      shown: (authority?.querySelector("dd") as HTMLElement | null)?.innerText ?? "",
      served: detail.authority ?? {},
    };
  });
  expect(receipt.shown).toContain(receipt.served.kind ?? "");
  expect(receipt.shown).toContain(receipt.served.ref ?? "");
});
