// SPEC.md §2 requires that the operator can "lock and stop server", which
// invalidates the launch session and terminates the listener (§12, decisions 34
// and 45). internal/web tests the route; the thing only a browser can show is
// that the control the operator actually sees does both -- and that the page
// says so instead of appearing to still work.
//
// This file runs its own server because it kills it. Sharing leak.test.ts's
// server would make every later test in that file depend on the order this one
// ran in.
//
// The corpus is synthetic and disposable. Nothing here reads a real session.

import { afterAll, beforeAll, expect, test } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import puppeteer, { type Browser, type Dialog, type Page } from "puppeteer-core";
import { resolveChrome } from "./chrome";

const SESSION_TITLE = "Synthetic lock session";
const STEM = "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000002";
const LOCK_BUTTON = "text/Lock & stop";

// A developer without Chrome skips this suite. The notice resolveChrome prints
// in that case is what keeps the skip from reading as a pass: this file is the
// only cover for the control the operator actually clicks, so a green run that
// never opened a browser would say the revocation path works when nothing
// checked it. In CI the same absence is a hard failure.
const chrome = resolveChrome({
  gate: "lock/stop gate",
  covers: "the SPEC.md §2 lock and stop control through the browser",
  unverified: [
    "that the control the operator actually sees asks for confirmation before it can end the session, and that declining leaves the server still serving",
    "that confirming revokes the session server-side and the server process really exits, rather than the page merely claiming it did",
    "that the stopped page is terminal -- no navigation left, no stale shell still polling -- and tells the operator how to start Babel again",
  ],
});

let root = "";
let server: Bun.Subprocess | null = null;
let browser: Browser | null = null;
let page: Page;
let launchURL = "";
let base = "";
let nonce = "";
// session is the credential the page holds after the bootstrap exchange, read
// out of the browser's own cookie store because the page cannot read it. It is
// what makes `call` below a report on the server rather than on the page.
let session = "";

// answerConfirm installs a one-shot handler for the control's confirmation
// dialog. Puppeteer dismisses dialogs when nothing is listening, so the
// handler is what distinguishes "the operator confirmed" from "the operator
// backed out", and removing it again keeps one test's answer out of the next.
async function answerConfirm(accept: boolean, act: () => Promise<void>): Promise<string> {
  let prompt = "";
  const handler = (dialog: Dialog) => {
    prompt = dialog.message();
    void (accept ? dialog.accept() : dialog.dismiss());
  };
  page.on("dialog", handler);
  try {
    await act();
  } finally {
    page.off("dialog", handler);
  }
  return prompt;
}

// call reaches the API the way the CLI would, from outside the browser, so it
// reports on the server rather than on the page's state. It presents the
// session the browser established, which is the only credential the server
// accepts: the launch nonce was spent by the page and authenticates nothing.
//
// One transport failure is not evidence: this process has already spoken to
// this port in beforeAll, and a pooled socket the server has since let go
// idle fails on reuse rather than being redialled. Observed only under CI
// (bun 1.4.0), where a declined confirmation - which stops nothing - was
// followed by an "unreachable" the browser contradicted by still rendering
// the page. A server that is genuinely gone refuses the second dial too, so
// the retry costs the negative case nothing and removes the false one.
async function call(path: string): Promise<number | "unreachable"> {
  for (let attempt = 0; ; attempt++) {
    try {
      const response = await fetch(`${base}${path}`, {
        headers: { Cookie: `babel_session=${session}` },
      });
      return response.status;
    } catch {
      // No delay: a socket the server has let go fails at once, and the
      // redial opens a new one. Waiting would only guess at a duration.
      if (attempt > 0) return "unreachable";
    }
  }
}

// adoptSession copies the page's own session out of the browser, and asserts on
// the way past that it is the cookie §2.7 requires. A test that read a
// JavaScript-readable credential here would be testing a different design.
async function adoptSession(): Promise<void> {
  const cookies = await browser!.defaultBrowserContext().cookies();
  const cookie = cookies.find((candidate) => candidate.name === "babel_session");
  expect(cookie, "the page established no session cookie").toBeDefined();
  expect(cookie!.httpOnly).toBe(true);
  expect(cookie!.sameSite).toBe("Strict");
  expect(cookie!.value).not.toBe(nonce);
  session = cookie!.value;
}

beforeAll(async () => {
  if (!chrome) return;

  root = mkdtempSync(join(tmpdir(), "babel-lock-"));
  const home = join(root, "home");
  const sessions = join(home, ".omp", "agent", "sessions", "synthetic-project");
  mkdirSync(sessions, { recursive: true });
  for (const sub of ["data", "cache", "config"]) mkdirSync(join(root, sub));

  const log = [
    { type: "title", v: 1, title: SESSION_TITLE, source: "auto", updatedAt: "2026-01-02T04:00:00.000Z" },
    {
      type: "session",
      version: 3,
      id: "00000000-0000-4000-8000-000000000002",
      timestamp: "2026-01-02T03:04:05.678Z",
      cwd: "/synthetic/workspace/two",
      titleSource: "auto",
    },
  ];
  writeFileSync(join(sessions, `${STEM}.jsonl`), log.map((l) => `${JSON.stringify(l)}\n`).join(""));

  // babel runs against the synthetic home; the Go toolchain must not. Pointing
  // HOME at the fixture would move the module cache with it and make the build
  // re-download the world.
  const env = {
    ...process.env,
    HOME: home,
    XDG_DATA_HOME: join(root, "data"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    BABEL_HOST_ID: "lockhost",
  };

  // No repository is configured: locking and stopping is a property of the
  // server, not of the archive, and building a restic repository here would buy
  // nothing this test asserts on.
  let binaryPath = process.env.BABEL_TEST_BINARY ?? "";
  if (binaryPath) {
    if (!existsSync(binaryPath)) throw new Error(`BABEL_TEST_BINARY does not exist: ${binaryPath}`);
  } else {
    binaryPath = join(root, "babel");
    const build = Bun.spawnSync(["go", "build", "-o", binaryPath, "./cmd/babel"], { cwd: ".." });
    if (!build.success) throw new Error(`go build failed: ${build.stderr.toString()}`);
  }

  // The local binding keeps the piped-stdout type; reading through the
  // module-level handle widens it back to number | ReadableStream.
  const process_ = Bun.spawn([binaryPath, "web", "--port", "0"], {
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
    const match = banner.match(/(http:\/\/127\.0\.0\.1:\d+\/#nonce=[0-9a-f]{64})/);
    if (match) launchURL = match[1];
  }
  reader.releaseLock();
  if (!launchURL) throw new Error(`server printed no launch URL: ${banner}`);
  [base, nonce] = launchURL.split("/#nonce=");

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  page = await browser.newPage();
});

afterAll(async () => {
  await browser?.close();
  // A no-op when the control did its job; the safety net for a failed run.
  server?.kill();
  if (!root) return;
  try {
    rmSync(root, { recursive: true, force: true });
  } catch {
    // The fixture lives under the system temp directory; leaving it is harmless.
  }
});

test.skipIf(!chrome)("the stop control is guarded by a confirmation the operator can decline", async () => {
  await page.goto(launchURL, { waitUntil: "networkidle2" });
  const control = await page.waitForSelector(LOCK_BUTTON, { timeout: 30_000 });
  expect(control).not.toBeNull();
  // The shell rendered, so the bootstrap exchange has run; its session is what
  // `call` uses to observe the server from outside the browser.
  await adoptSession();

  const prompt = await answerConfirm(false, async () => {
    await control?.click();
    // The click resolves as soon as the dialog is answered; what must be
    // observed is that nothing followed it. The one request that would follow
    // is the lock, so the server itself is the witness.
    await page.waitForFunction(() => !document.body.innerText.includes("Stopping…"), {
      timeout: 30_000,
    });
  });

  // A control that can end the session must say so before it does.
  expect(prompt).toMatch(/lock and stop/i);
  expect(prompt).toMatch(/revoked/i);

  expect(await call("/api/version")).toBe(200);
  const text = await page.evaluate(() => document.body.innerText);
  expect(text).not.toMatch(/server stopped/i);
});

test.skipIf(!chrome)("confirming it revokes the session, stops the server, and says so", async () => {
  const control = await page.waitForSelector(LOCK_BUTTON, { timeout: 30_000 });

  await answerConfirm(true, async () => {
    await control?.click();
    // The terminal state is the page's own report that the lock landed, so it
    // is the synchronization point for everything asserted below.
    await page.waitForFunction(() => /server stopped/i.test(document.body.innerText), {
      timeout: 30_000,
    });
  });

  // Terminal means terminal: no navigation left, and no page still polling
  // behind a stale shell.
  const stopped = await page.evaluate(() => document.body.innerText);
  expect(stopped).toMatch(/no longer reach Babel/i);
  expect(stopped).toContain("babel web");
  expect(stopped).not.toContain(SESSION_TITLE);
  expect(await page.$("nav")).toBeNull();

  // The process's own exit is the proof that the listener is gone, and its
  // status is the proof that a requested stop is not reported as a failure.
  // Awaited directly: the exit is a signal the process already emits, so a
  // deadline of our own would only turn a hang into a less informative
  // failure than the suite's own timeout.
  expect(await server!.exited).toBe(0);

  // The session was revoked before the listener closed, so neither the
  // credential nor the port is worth anything now. Revocation is asserted
  // separately from the exit in internal/web, where a server can be locked
  // without being stopped; here the two are one operator action.
  expect(await call("/api/version")).toBe("unreachable");
  expect(await call("/api/lock")).toBe("unreachable");
});
