// Throwaway: does the filing fold read the topics when opened cold?
import puppeteer from "puppeteer-core";

const browser = await puppeteer.launch({
  executablePath: process.env.BABEL_TEST_CHROME ?? "/home/alex/.nix-profile/bin/chromium",
  headless: true,
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});
const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 900 });
const calls: string[] = [];
page.on("requestfinished", (request) => {
  if (request.url().includes("/api/topics")) calls.push("topics");
});
await page.goto("http://127.0.0.1:8873/#/r/fnd_conflicting-evidence", { waitUntil: "networkidle2" });
await page.reload({ waitUntil: "networkidle2" });
await page.waitForSelector("details.record-filing", { timeout: 15_000 });
await page.click("details.record-filing > summary");
await new Promise((resolve) => setTimeout(resolve, 900));
const first = await page.evaluate(() => ({
  open: (document.querySelector("details.record-filing") as HTMLDetailsElement).open,
  options: document.querySelectorAll(".record-filing [data-filing='topic'] option").length,
}));
// Close and open again: a second toggle is a second chance for the handler.
await page.click("details.record-filing > summary");
await page.click("details.record-filing > summary");
await new Promise((resolve) => setTimeout(resolve, 900));
const second = await page.evaluate(() => ({
  open: (document.querySelector("details.record-filing") as HTMLDetailsElement).open,
  options: document.querySelectorAll(".record-filing [data-filing='topic'] option").length,
}));
console.log(JSON.stringify({ first, second, calls }, null, 2));
await browser.close();
