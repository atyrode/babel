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
import puppeteer, { type Browser, type Page } from "puppeteer-core";
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
    "that the session token leaves the address bar and is absent from every reachable history entry",
    "that no transcript or credential sentinel reaches a URL",
    "that no /api response is served from the browser cache",
    "that a browser context without the token is refused",
  ],
});

let root = "";
let server: Bun.Subprocess | null = null;
let browser: Browser | null = null;
let page: Page;
let launchURL = "";
let token = "";
let binaryPath = "";

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
  const env = {
    ...process.env,
    HOME: home,
    XDG_DATA_HOME: join(root, "data"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    BABEL_HOST_ID: "browserhost",
  };

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
  const selection = ["--repo", repo, "--password-file", passwordFile];
  const init = Bun.spawnSync([binaryPath, "archive", "init", ...selection], { env });
  if (!init.success) throw new Error(`archive init failed: ${init.stderr.toString()}`);
  const push = Bun.spawnSync([binaryPath, "archive", "push", "--json", ...selection], { env });
  if (!push.success) throw new Error(`archive push failed: ${push.stderr.toString()}`);

  // The local binding keeps the piped-stdout type; reading through the
  // module-level handle widens it back to number | ReadableStream.
  const process_ = Bun.spawn([binaryPath, "web", "--port", "0", ...selection], {
    env,
    stdout: "pipe",
    stderr: "pipe",
  });
  server = process_;

  const deadline = Date.now() + 30_000;
  const reader = process_.stdout.getReader();
  const decoder = new TextDecoder();
  let banner = "";
  while (!launchURL && Date.now() < deadline) {
    const { value, done } = await reader.read();
    if (done) break;
    banner += decoder.decode(value, { stream: true });
    const match = banner.match(/(http:\/\/127\.0\.0\.1:\d+\/#token=[0-9a-f]{64})/);
    if (match) launchURL = match[1];
  }
  reader.releaseLock();
  if (!launchURL) throw new Error(`server printed no launch URL: ${banner}`);
  token = launchURL.split("#token=")[1];

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
});

afterAll(async () => {
  await browser?.close();
  server?.kill();
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

test.skipIf(!chrome)("the fragment token authenticates and leaves the address bar", async () => {
  await page.goto(launchURL, { waitUntil: "networkidle2" });
  await page.waitForFunction(
    (title: string) => document.body.innerText.includes(title),
    { timeout: 30_000 },
    SESSION_TITLE,
  );
  await show(page);

  expect(page.url()).not.toContain(token);
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toContain("babel.web.token");
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
  expect(page.url()).not.toContain(token);
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
  expect(page.url()).not.toContain(token);
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

test.skipIf(!chrome)("no history entry or request URL carries a sentinel or the token", async () => {
  // Guard against the whole suite passing because nothing was ever visited.
  expect(visited.length).toBeGreaterThanOrEqual(5);
  expect(await page.evaluate(() => history.length)).toBeGreaterThan(1);

  for (const url of visited) {
    expect(url).not.toContain(token);
    expect(url).not.toContain(TRANSCRIPT_SENTINEL);
    expect(url).not.toContain(CREDENTIAL_SENTINEL);
  }

  const requests = await page.evaluate(() =>
    performance.getEntriesByType("resource").map((entry) => entry.name),
  );
  expect(requests.length).toBeGreaterThan(0);
  for (const url of requests) {
    expect(url).not.toContain(token);
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

test.skipIf(!chrome)("a context without the token is refused", async () => {
  const isolated = await browser!.createBrowserContext();
  try {
    const fresh = await isolated.newPage();
    const base = launchURL.split("/#token=")[0];
    await fresh.goto(`${base}/#/sessions`, { waitUntil: "networkidle2" });
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
  } finally {
    await isolated.close();
  }
});

// The property that matters is not "we call replaceState" but "no history entry
// the operator can navigate to holds the token". Two mechanisms independently
// satisfy it today — the bootstrap's scrub, and App.tsx's catch-all
// <Navigate to="/sessions" replace />, which the unmatched "#token=…" fragment
// falls through to — so removing either alone is not observable here. Removing
// both is: the walk fails and names the retained entry.
//
// The walk is bounded by the stack the browser reports, and completion is
// proven by landing on the context's initial blank entry rather than by the
// loop finishing. A fixed bound, or trusting goBack's return value, would let
// part of the stack go unexamined while the test still claimed every reachable
// entry was checked.
test.skipIf(!chrome)("no reachable history entry retains the token", async () => {
  const walker = await browser!.createBrowserContext();
  try {
    const trail = await walker.newPage();
    await trail.goto(launchURL, { waitUntil: "networkidle2" });
    await trail.waitForFunction(
      (title: string) => document.body.innerText.includes(title),
      { timeout: 30_000 },
      SESSION_TITLE,
    );

    // Visit a second route so the stack has somewhere to walk back from.
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
    // so an early stop is precisely the failure that would hide a token. It
    // also stops early when a retained token entry redirects away on arrival,
    // which is why the trail is reported: that is what a reviewer needs to see.
    // The token is redacted, since a test about credential hygiene should not
    // print one into CI output.
    const trailText = landed.map((url) => url.replace(token, "<TOKEN>")).join(" -> ");
    expect(landed.at(-1), `the walk stopped before the bottom of the stack: ${trailText}`).toBe(
      "about:blank",
    );
    expect(landed.length).toBeGreaterThan(1);
    for (const url of landed) {
      // Compared in redacted form deliberately: asserting on the raw URL would
      // make Bun print the received string, and the token with it.
      const redacted = url.replace(token, "<TOKEN>");
      expect(redacted, `a history entry retains the launch token: ${trailText}`).not.toContain(
        "<TOKEN>",
      );
    }
  } finally {
    await walker.close();
  }
});
