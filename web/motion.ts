// Throwaway motion verification for the craft round. Not part of the app.
//
//   bun motion.ts <outdir>
//
// Proves three things and writes the frames that show them:
//   1. a sort change slides the rows that stayed (two frames 100ms apart, with
//      the running animations read out of the DOM),
//   2. a route change runs a view transition rather than swapping,
//   3. the live poll re-reads the feed on its own and the page survives it.
import { existsSync, mkdirSync } from "node:fs";
import puppeteer from "puppeteer-core";

const BASE = process.env.SHOT_BASE ?? "http://127.0.0.1:8873";
const CHROME = process.env.BABEL_TEST_CHROME ?? "/home/alex/.nix-profile/bin/chromium";
const out = process.argv[2] ?? "/tmp/craft2-motion";
mkdirSync(out, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: true,
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--force-device-scale-factor=1"],
});
const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 900 });
const reads: string[] = [];
page.on("request", (request) => {
  if (request.url().includes("/api/feed")) reads.push(request.url());
});

async function settle(): Promise<void> {
  await page.waitForSelector("ol.feed-list li.feed-row", { timeout: 15_000 });
}

await page.goto(`${BASE}/#/?needs=all&sort=new`, { waitUntil: "networkidle2" });
await page.reload({ waitUntil: "networkidle2" });
await settle();

// The order the rows are in before the change, so the slide can be checked
// against a list that really moved.
const before = await page.$$eval("ol.feed-list li.feed-row", (rows) =>
  rows.map((row) => row.getAttribute("data-post") ?? ""));

// Change the order: open the sentence's sort menu and take `top`, which ranks
// by what Babel's reviewers said rather than by age.
await page.click("[data-pick='sort']");
await page.waitForSelector("[data-sort='top']", { timeout: 5_000 });
await page.click("[data-sort='top']");
// Two frames, 100ms apart, while the rows are moving — plus what the browser
// says is actually animating at that moment.
// The sort menu stays open after a period-bearing order is chosen, and it
// covers the first rows: the frames below are of the list, not of a popover.
await page.keyboard.press("Escape");
const frames: Array<{ file: string; animations: number; shifted: number }> = [];
for (const [index, delay] of [60, 160].entries()) {
  await new Promise((resolve) => setTimeout(resolve, index === 0 ? delay : 100));
  const state = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll("ol.feed-list li.feed-row"));
    const running = rows.flatMap((row) => row.getAnimations());
    const shifted = rows.filter((row) => {
      const transform = getComputedStyle(row).transform;
      return transform !== "none" && transform !== "matrix(1, 0, 0, 1, 0, 0)";
    }).length;
    return { animations: running.length, shifted };
  });
  const file = `${out}/home-resort-frame${index + 1}-1440.png`;
  await page.screenshot({ path: file });
  frames.push({ file, ...state });
}

await settle();
const after = await page.$$eval("ol.feed-list li.feed-row", (rows) =>
  rows.map((row) => row.getAttribute("data-post") ?? ""));

// A route change: does the arriving page animate, and does the page survive?
await page.click("ol.feed-list li.feed-row a.feed-claim");
await page.waitForFunction(() => window.location.hash.startsWith("#/r/"), { timeout: 15_000 });
const transition = await page.evaluate(() => {
  const body = document.querySelector("main > .page");
  const running = body?.getAnimations() ?? [];
  return {
    animating: running.length,
    name: running.map((animation) => (animation as CSSAnimation).animationName ?? "").join(","),
  };
});
const landed = await page.evaluate(() => ({
  hash: window.location.hash,
  page: document.querySelector("main .page") !== null,
}));

// The live poll: one read the reader did not ask for, inside sixteen seconds.
await page.goto(`${BASE}/#/?needs=all`, { waitUntil: "networkidle2" });
await settle();
const asked = reads.length;
await new Promise((resolve) => setTimeout(resolve, 16_500));
const polled = reads.length - asked;
const alive = await page.$$eval("ol.feed-list li.feed-row", (rows) => rows.length);

console.log(
  JSON.stringify(
    {
      frames: frames.map((frame) => ({ ...frame, exists: existsSync(frame.file) })),
      orderChanged: before.join(",") !== after.join(","),
      stayed: before.filter((id) => after.includes(id)).length,
      transition,
      landed,
      polled,
      alive,
    },
    null,
    2,
  ),
);
await browser.close();
