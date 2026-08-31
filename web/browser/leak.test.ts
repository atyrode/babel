// SPEC.md §548 requires proof that no transcript or credential sentinel reaches
// URLs, browser history, server logs, or cached responses. internal/cli's
// leak test covers the server's HTTP surface; the channels only a browser can
// observe -- what the address bar and history actually hold, what a reload and
// a back/forward navigation re-serve, and whether the fragment bootstrap really
// authenticates -- are covered here.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import puppeteer, { type Browser, type BrowserContext, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const TRANSCRIPT_SENTINEL = "TRANSCRIPTSENTINELa1b2c3d4e5f60718";
const CREDENTIAL_SENTINEL = "CREDENTIALSENTINEL90817263544f3e2";
const SESSION_TITLE = "Synthetic verification session";
const STEM = "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001";

// This suite is the only cover for the browser-observable SPEC.md §548
// channels, so a skip whose only trace is bun's "skip" tally reads exactly
// like a pass. resolveChrome resolves the browser and reports its absence in
// one call, so the executable cannot be obtained without the absence being
// named: locally it prints the guarantees this run did not check, and in CI it
// fails hard rather than retiring the gate for everyone.
const chrome = resolveChrome({
  gate: "§548 leak gate",
  covers: "the SPEC.md §548 browser channels",
  unverified: [
    "that the launch nonce is exchanged for a session cookie the page cannot read, leaves the address bar, and is absent from every reachable history entry",
    "that the launch link authenticates exactly one browser session and a second use of it is refused",
    "that no transcript or credential sentinel reaches a URL",
    "that no /api response is served from the browser cache",
    "that a browser context with no session is refused",
  ],
});

// One launch of `babel web`: the process, the printed URL, and the two halves
// of that URL a test needs separately.
//
// It is a helper rather than beforeAll's local state because the launch nonce
// is single-use, and two of the properties below are about exactly that: a
// suite that shared one launch could not prove a second use is refused, nor
// walk the history of a page that bootstrapped successfully. Each launch is a
// process against the same fixture, which costs milliseconds.
interface Launch {
  process: Bun.Subprocess;
  url: string;
  base: string;
  nonce: string;
}

let root = "";
let browser: Browser | null = null;
let page: Page;
let primary: Launch;
let binaryPath = "";
// session is the established credential, read out of the browser's cookie
// store by the first test. Every later leak assertion checks it as well as the
// nonce: the value the page cannot read must also never appear in a URL.
let session = "";
let env: Record<string, string> = {};
let selection: string[] = [];

// Every launch this suite starts, killed together at the end.
const launched: Bun.Subprocess[] = [];

async function launch(): Promise<Launch> {
  // The local binding keeps the piped-stdout type; reading through a widened
  // handle loses it.
  const process_ = Bun.spawn([binaryPath, "web", "--port", "0", ...selection], {
    env,
    stdout: "pipe",
    stderr: "pipe",
  });
  launched.push(process_);

  const deadline = Date.now() + 30_000;
  const reader = process_.stdout.getReader();
  const decoder = new TextDecoder();
  let banner = "";
  let url = "";
  while (!url && Date.now() < deadline) {
    const { value, done } = await reader.read();
    if (done) break;
    banner += decoder.decode(value, { stream: true });
    const match = banner.match(/(http:\/\/127\.0\.0\.1:\d+\/#nonce=[0-9a-f]{64})/);
    if (match) url = match[1];
  }
  reader.releaseLock();
  if (!url) throw new Error(`server printed no launch URL: ${banner}`);
  const [base, nonce] = url.split("/#nonce=");
  return { process: process_, url, base, nonce };
}

// sessionCookie reads the credential the page itself cannot: an HttpOnly cookie
// is invisible to `document.cookie` and visible to the browser's own cookie
// store, which is the whole property being asserted.
async function sessionCookie(context: BrowserContext) {
  const cookies = await context.cookies();
  return cookies.find((cookie) => cookie.name === "babel_session");
}

// visited records every URL the browser actually held, which is the set browser
// history is built from. Every assertion about the history channel reads this.
const visited: string[] = [];

async function show(target: Page): Promise<void> {
  visited.push(target.url());
}

beforeAll(async () => {
  if (!chrome) return;

  root = mkdtempSync(join(tmpdir(), "babel-browser-"));
  const home = join(root, "home");
  const sessions = join(home, ".omp", "agent", "sessions", "synthetic-project");
  mkdirSync(sessions, { recursive: true });
  for (const sub of ["data", "cache", "config", "repo"]) mkdirSync(join(root, sub));

  const log = [
    { type: "title", v: 1, title: SESSION_TITLE, source: "auto", updatedAt: "2026-01-02T04:00:00.000Z" },
    {
      type: "session",
      version: 3,
      id: "00000000-0000-4000-8000-000000000001",
      timestamp: "2026-01-02T03:04:05.678Z",
      cwd: "/synthetic/workspace/one",
      titleSource: "auto",
    },
    {
      type: "message",
      id: "f0000001",
      timestamp: "2026-01-02T03:10:00.000Z",
      message: {
        role: "user",
        content: [{ type: "text", text: `synthetic message carrying ${TRANSCRIPT_SENTINEL}` }],
      },
    },
  ];
  writeFileSync(join(sessions, `${STEM}.jsonl`), log.map((l) => `${JSON.stringify(l)}\n`).join(""));

  const passwordFile = join(root, "password");
  writeFileSync(passwordFile, `${CREDENTIAL_SENTINEL}\n`, { mode: 0o600 });

  const repo = join(root, "repo");
  const binary = join(root, "babel");

  // babel runs against the synthetic home; the Go toolchain must not. Pointing
  // HOME at the fixture would move the module cache with it and make the build
  // re-download the world.
  env = {
    ...process.env,
    HOME: home,
    XDG_DATA_HOME: join(root, "data"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    BABEL_HOST_ID: "browserhost",
  } as Record<string, string>;

  const prebuilt = process.env.BABEL_TEST_BINARY;
  if (prebuilt) {
    if (!existsSync(prebuilt)) throw new Error(`BABEL_TEST_BINARY does not exist: ${prebuilt}`);
    binaryPath = prebuilt;
  } else {
    const build = Bun.spawnSync(["go", "build", "-o", binary, "./cmd/babel"], { cwd: ".." });
    if (!build.success) throw new Error(`go build failed: ${build.stderr.toString()}`);
    binaryPath = binary;
  }

  // The archive endpoints must be live, so the credential is genuinely in use
  // rather than reported as an unconfigured 409.
  //
  // The repository is created explicitly: `archive push` deliberately refuses
  // to create one, because concurrent creation corrupts a restic repository and
  // silent creation would hide a mistyped locator (SPEC.md §6.1).
  selection = ["--repo", repo, "--password-file", passwordFile];
  const init = Bun.spawnSync([binaryPath, "archive", "init", ...selection], { env });
  if (!init.success) throw new Error(`archive init failed: ${init.stderr.toString()}`);
  const push = Bun.spawnSync([binaryPath, "archive", "push", "--json", ...selection], { env });
  if (!push.success) throw new Error(`archive push failed: ${push.stderr.toString()}`);

  primary = await launch();

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
});

afterAll(async () => {
  await browser?.close();
  for (const process_ of launched) process_.kill();
  if (!root) return;
  // restic writes its repository read-only, so the tree cannot be removed
  // until it is writable again. A cleanup failure must not mask a result.
  try {
    Bun.spawnSync(["chmod", "-R", "u+w", root]);
    rmSync(root, { recursive: true, force: true });
  } catch {
    // The fixture lives under the system temp directory; leaving it is harmless.
  }
});

test.skipIf(!chrome)("the fragment nonce becomes a cookie the page cannot read", async () => {
  await page.goto(primary.url, { waitUntil: "networkidle2" });
  // The scrubbed launch URL carries no hash, so it lands on the dashboard.
  // Waiting for its own authorized read is the non-vacuity check: the panel
  // heading below only appears once GET /api/overview answered 200, which it
  // cannot do without the session this test is about.
  await page.waitForFunction(
    () => document.body.innerText.includes("Archive health"),
    { timeout: 30_000 },
  );
  await show(page);

  expect(page.url()).not.toContain(primary.nonce);

  // The live credential is a cookie with the flags §2.7 requires, and it is
  // read here out of the browser's own cookie store rather than out of the
  // page: `document.cookie` cannot see it, which is the entire point of the
  // exchange. Nothing is in web storage any more, because a cookie the page
  // cannot read needs no page-side copy.
  const cookie = await sessionCookie(browser!.defaultBrowserContext());
  expect(cookie, "the bootstrap set no session cookie").toBeDefined();
  expect(cookie!.httpOnly).toBe(true);
  expect(cookie!.sameSite).toBe("Strict");
  expect(cookie!.session).toBe(true);
  expect(cookie!.value).not.toBe(primary.nonce);
  session = cookie!.value;

  expect(await page.evaluate(() => document.cookie)).not.toContain("babel_session");
  expect(await page.evaluate(() => document.cookie)).not.toContain(session);
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toHaveLength(0);
  expect(await page.evaluate(() => Object.keys(localStorage))).not.toContain("babel.web.token");

  // The rest of this suite browses the session and its transcript, which is
  // where the §548 channels are, so it starts from the listing. The hash
  // navigation is a real history entry, exactly as clicking the nav is.
  await page.evaluate(() => {
    window.location.hash = "#/sessions";
  });
  await page.waitForFunction(
    (title: string) => document.body.innerText.includes(title),
    { timeout: 30_000 },
    SESSION_TITLE,
  );
  await show(page);
});

test.skipIf(!chrome)("a session's transcript renders without its content entering the URL", async () => {
  const row = await page.waitForSelector(`text/${SESSION_TITLE}`, { timeout: 30_000 });
  await row?.click();
  await page.waitForFunction(() => location.hash.startsWith("#/sessions/"), { timeout: 30_000 });
  await page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 30_000 },
    TRANSCRIPT_SENTINEL,
  );
  await show(page);

  // Non-vacuity: the sentinel is on screen, so this is a real transcript view
  // rather than an empty page that trivially leaks nothing.
  expect(await page.evaluate(() => document.body.innerText)).toContain(TRANSCRIPT_SENTINEL);
  expect(page.url()).toContain("#/sessions/");
  expect(page.url()).not.toContain(TRANSCRIPT_SENTINEL);
  expect(page.url()).not.toContain(primary.nonce);
  expect(page.url()).not.toContain(session);
});

test.skipIf(!chrome)("a reload stays authenticated with no credential in the URL", async () => {
  await page.reload({ waitUntil: "networkidle2" });
  await page.waitForFunction(
    (needle: string) => document.body.innerText.includes(needle),
    { timeout: 30_000 },
    TRANSCRIPT_SENTINEL,
  );
  await show(page);

  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toMatch(/unauthorized/i);
  expect(page.url()).not.toContain(primary.nonce);
  expect(page.url()).not.toContain(session);
});

// Back and forward change the hash immediately, but the view behind it renders
// asynchronously, so waiting on `location.hash` alone would let the content
// assertions read a half-rendered page. Each step therefore waits for the
// destination's own content to settle. Waiting for "expected content OR an
// authorization failure" keeps the credential assertion fast and precise: if
// back/forward ever dropped authentication, this reports that rather than
// timing out after 30s with no explanation.
test.skipIf(!chrome)("back and forward keep working and stay credential-free", async () => {
  await page.evaluate(() => history.back());
  await page.waitForFunction(() => location.hash === "#/sessions", { timeout: 30_000 });
  await page.waitForFunction(
    (title: string) => {
      const text = document.body.innerText;
      return text.includes(title) || /unauthorized/i.test(text);
    },
    { timeout: 30_000 },
    SESSION_TITLE,
  );
  await show(page);
  expect(await page.evaluate(() => document.body.innerText)).not.toMatch(/unauthorized/i);

  await page.evaluate(() => history.forward());
  await page.waitForFunction(() => location.hash.startsWith("#/sessions/"), { timeout: 30_000 });
  await page.waitForFunction(
    (needle: string) => {
      const text = document.body.innerText;
      return text.includes(needle) || /unauthorized/i.test(text);
    },
    { timeout: 30_000 },
    TRANSCRIPT_SENTINEL,
  );
  await show(page);
  expect(await page.evaluate(() => document.body.innerText)).not.toMatch(/unauthorized/i);
  expect(await page.evaluate(() => document.body.innerText)).toContain(TRANSCRIPT_SENTINEL);
});

test.skipIf(!chrome)("no history entry or request URL carries a sentinel or a credential", async () => {
  // Guard against the whole suite passing because nothing was ever visited.
  expect(visited.length).toBeGreaterThanOrEqual(5);
  expect(await page.evaluate(() => history.length)).toBeGreaterThan(1);

  for (const url of visited) {
    expect(url).not.toContain(primary.nonce);
    expect(url).not.toContain(session);
    expect(url).not.toContain(TRANSCRIPT_SENTINEL);
    expect(url).not.toContain(CREDENTIAL_SENTINEL);
  }

  const requests = await page.evaluate(() =>
    performance.getEntriesByType("resource").map((entry) => entry.name),
  );
  expect(requests.length).toBeGreaterThan(0);
  for (const url of requests) {
    expect(url).not.toContain(primary.nonce);
    expect(url).not.toContain(session);
    expect(url).not.toContain(TRANSCRIPT_SENTINEL);
    expect(url).not.toContain(CREDENTIAL_SENTINEL);
  }
});

test.skipIf(!chrome)("no /api response is served from the browser cache", async () => {
  const api = await page.evaluate(() =>
    performance
      .getEntriesByType("resource")
      .filter((entry) => entry.name.includes("/api/"))
      .map((entry) => {
        const timing = entry as PerformanceResourceTiming;
        return {
          name: timing.name,
          transferSize: timing.transferSize,
          decodedBodySize: timing.decodedBodySize,
        };
      }),
  );

  // The reload and the back/forward navigations above already re-requested the
  // API, so an empty set here would mean this is measuring nothing.
  expect(api.length).toBeGreaterThan(0);
  for (const entry of api) {
    // A cache hit reports a body it did not transfer. Cache-Control: no-store
    // must prevent that for every response that can carry session content.
    const servedFromCache = entry.transferSize === 0 && entry.decodedBodySize > 0;
    expect(servedFromCache, `${entry.name} was served from cache`).toBe(false);
  }
});

test.skipIf(!chrome)("a context with no session is refused", async () => {
  const isolated = await browser!.createBrowserContext();
  try {
    const fresh = await isolated.newPage();
    await fresh.goto(`${primary.base}/#/sessions`, { waitUntil: "networkidle2" });
    // Waiting for a character count would race the shell against the 401: the
    // app frame can exceed any threshold before the refusal arrives. Waiting
    // for either outcome keeps the failure fast and named — if an unauthorized
    // context were ever served real content, this resolves on the title and the
    // assertion below says so.
    await fresh.waitForFunction(
      (title: string) => {
        const text = document.body.innerText;
        return /unauthorized/i.test(text) || text.includes(title);
      },
      { timeout: 30_000 },
      SESSION_TITLE,
    );

    const text = await fresh.evaluate(() => document.body.innerText);
    expect(text).toMatch(/unauthorized/i);
    expect(text).not.toContain(SESSION_TITLE);
    expect(text).not.toContain(TRANSCRIPT_SENTINEL);
    expect(await fresh.evaluate(() => Object.keys(sessionStorage))).toHaveLength(0);
    // A context reached without a launch link holds no session either: the
    // static shell is served to anyone on loopback and authenticates nothing.
    expect(await sessionCookie(isolated)).toBeUndefined();
  } finally {
    await isolated.close();
  }
});

// The §2.7 property this file exists for after #72: the launch link is spent by
// the browser that uses it. Two real contexts against one launch, because that
// is the shape of the mistake it prevents — a URL left in a terminal, pasted
// again later, or read by someone else on the machine — and a second exchange
// is the only way the nonce could be worth anything to them.
//
// This launch is its own, since the suite's primary launch was spent by the
// first test and a single-use credential cannot be proven twice.
test.skipIf(!chrome)("the launch link authenticates once and is then refused", async () => {
  const single = await launch();
  const first = await browser!.createBrowserContext();
  const second = await browser!.createBrowserContext();
  try {
    const opened = await first.newPage();
    await opened.goto(single.url, { waitUntil: "networkidle2" });
    await opened.waitForFunction(
      () => document.body.innerText.includes("Archive health"),
      { timeout: 30_000 },
    );
    const established = await sessionCookie(first);
    expect(established, "the first use established no session").toBeDefined();

    // The same URL again, in a browser that holds none of the first one's
    // state. The server has already consumed the nonce, so the page reports the
    // refusal it was given rather than a bare "unauthorized" the operator
    // cannot act on.
    const replayed = await second.newPage();
    await replayed.goto(single.url, { waitUntil: "networkidle2" });
    await replayed.waitForFunction(
      () => /already used|unauthorized/i.test(document.body.innerText)
        || document.body.innerText.includes("Archive health"),
      { timeout: 30_000 },
    );
    const refused = await replayed.evaluate(() => document.body.innerText);
    expect(refused).toMatch(/already used/i);
    expect(refused).toMatch(/babel web/i);
    expect(refused).not.toContain("Archive health");
    expect(await sessionCookie(second)).toBeUndefined();

    // And the legitimate page is undisturbed by the replay: a reload still
    // authenticates with the cookie it holds, which is what makes the refusal
    // above a property of the nonce rather than of a server that broke.
    await opened.reload({ waitUntil: "networkidle2" });
    await opened.waitForFunction(
      () => document.body.innerText.includes("Archive health"),
      { timeout: 30_000 },
    );
    expect((await sessionCookie(first))!.value).toBe(established!.value);
  } finally {
    await first.close();
    await second.close();
    single.process.kill();
  }
});

// The property that matters is not "we call replaceState" but "no history entry
// the operator can navigate to holds the launch nonce". Two mechanisms
// independently satisfy it today — the bootstrap's scrub, and App.tsx's
// catch-all <Navigate to="/" replace />, which the unmatched "#nonce=…"
// fragment falls through to — so removing either alone is not observable here.
// Removing both is: the walk fails and names the retained entry.
//
// The walk is bounded by the stack the browser reports, and completion is
// proven by landing on the context's initial blank entry rather than by the
// loop finishing. A fixed bound, or trusting goBack's return value, would let
// part of the stack go unexamined while the test still claimed every reachable
// entry was checked.
//
// It gets its own launch, because the walk has to start from a page that
// actually bootstrapped and the nonce is spent by whoever does that first.
test.skipIf(!chrome)("no reachable history entry retains the launch nonce", async () => {
  const walked = await launch();
  const walker = await browser!.createBrowserContext();
  try {
    const trail = await walker.newPage();
    await trail.goto(walked.url, { waitUntil: "networkidle2" });
    await trail.waitForFunction(
      () => document.body.innerText.includes("Archive health"),
      { timeout: 30_000 },
    );

    // Visit two more routes so the stack has somewhere to walk back from, and
    // so the walk crosses the landing entry the nonce arrived on.
    await trail.evaluate(() => {
      window.location.hash = "#/sessions";
    });
    const row = await trail.waitForSelector(`text/${SESSION_TITLE}`, { timeout: 30_000 });
    await row?.click();
    await trail.waitForFunction(() => location.hash.startsWith("#/sessions/"), { timeout: 30_000 });

    const depth = await trail.evaluate(() => history.length);
    const landed: string[] = [trail.url()];
    for (let step = 0; step <= depth + 1; step += 1) {
      const before = trail.url();
      // goBack resolves null for same-document (hash) navigations, so its
      // return value says nothing about whether the stack moved. It can also
      // resolve before the URL settles, because waitUntil describes document
      // loads and a hash change is not one. Progress is therefore awaited
      // explicitly; the catches cover a back-navigation that destroys the
      // document, where the URL has already changed.
      await trail.goBack({ waitUntil: "domcontentloaded" }).catch(() => null);
      await trail
        .waitForFunction((previous: string) => location.href !== previous, { timeout: 5_000 }, before)
        .catch(() => null);
      const after = trail.url();
      if (after === before) break;
      landed.push(after);
      // The blank entry is the bottom of a fresh context's stack, so stopping
      // here is the normal exit. Without it the next iteration has nowhere to
      // go and pays the full progress timeout to discover that.
      if (after === "about:blank") break;
    }

    // Reaching the context's initial blank entry is what proves the whole stack
    // was traversed. URL-progress alone could stop early if two adjacent
    // entries happened to share a URL, and the launch entry is the first one,
    // so an early stop is precisely the failure that would hide a nonce. It
    // also stops early when a retained credential entry redirects away on
    // arrival, which is why the trail is reported: that is what a reviewer
    // needs to see. The nonce is redacted, since a test about credential
    // hygiene should not print one into CI output.
    const trailText = landed.map((url) => url.replace(walked.nonce, "<NONCE>")).join(" -> ");
    expect(landed.at(-1), `the walk stopped before the bottom of the stack: ${trailText}`).toBe(
      "about:blank",
    );
    expect(landed.length).toBeGreaterThan(1);
    for (const url of landed) {
      // Compared in redacted form deliberately: asserting on the raw URL would
      // make Bun print the received string, and the credential with it.
      const redacted = url.replace(walked.nonce, "<NONCE>");
      expect(redacted, `a history entry retains the launch nonce: ${trailText}`).not.toContain(
        "<NONCE>",
      );
    }
  } finally {
    await walker.close();
    walked.process.kill();
  }
});
