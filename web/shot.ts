// Throwaway screenshot driver for the craft round. Not part of the app.
//
//   bun shot.ts <outdir> <route> [<name>] [--w=1440] [--h=900] [--full]
//
// Routes are hash routes without the leading "#/".
import { existsSync, mkdirSync, readdirSync } from "node:fs";
import puppeteer, { type Page } from "puppeteer-core";
const BASE = process.env.SHOT_BASE ?? "http://127.0.0.1:8873";

function chrome(): string {
  const fromEnv = process.env.BABEL_TEST_CHROME;
  if (fromEnv && existsSync(fromEnv)) return fromEnv;
  const cache = `${process.env.HOME}/.cache/puppeteer`;
  if (existsSync(cache)) {
    for (const dir of readdirSync(cache)) {
      const base = `${cache}/${dir}`;
      for (const rev of readdirSync(base)) {
        for (const candidate of [
          `${base}/${rev}/chrome-linux64/chrome`,
          `${base}/${rev}/chrome-headless-shell-linux64/chrome-headless-shell`,
        ]) {
          if (existsSync(candidate)) return candidate;
        }
      }
    }
  }
  for (const candidate of [
    "/run/current-system/sw/bin/chromium",
    "/etc/profiles/per-user/alex/bin/chromium",
  ]) {
    if (existsSync(candidate)) return candidate;
  }
  throw new Error("no chrome found");
}

const args = process.argv.slice(2);
const positional = args.filter((a) => !a.startsWith("--"));
const flags = new Map(
  args.filter((a) => a.startsWith("--")).map((a) => {
    const [k, v] = a.replace(/^--/u, "").split("=");
    return [k, v ?? "1"];
  }),
);
const [outdir, route, name] = positional;
const width = Number(flags.get("w") ?? 1440);
const height = Number(flags.get("h") ?? 900);
const full = flags.has("full");

mkdirSync(outdir, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: chrome(),
  headless: true,
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--force-device-scale-factor=1"],
});
const page = await browser.newPage();
await page.setViewport({ width, height });
await page.goto(`${BASE}/#/${route}`, { waitUntil: "networkidle2" });
await page.reload({ waitUntil: "networkidle2" });
await page
  .waitForFunction(
    () =>
      document.querySelector("main .page") !== null &&
      document.querySelector(".state-note .spinner") === null &&
      document.querySelector(".feed-skeleton") === null,
    { timeout: 15_000 },
  )
  .catch(() => undefined);

// Anything the caller wants done before the shot. `--script=<file.ts>` loads a
// module whose default export gets the page; the rest are one-liners.
const clicked = flags.get("click");
if (clicked) await page.click(clicked);
const hovered = flags.get("hover");
if (hovered) await page.hover(hovered);
const into = flags.get("into");
if (into) {
  await page.evaluate((selector: string) => {
    document.querySelector(selector)?.scrollIntoView({ block: "center" });
  }, into);
}
const typed = flags.get("type");
if (typed) await page.keyboard.press(typed as Parameters<Page["keyboard"]["press"]>[0]);
const scriptFile = flags.get("script");
if (scriptFile) {
  const module_ = (await import(`${process.cwd()}/${scriptFile}`)) as {
    default: (page: Page) => Promise<void>;
  };
  await module_.default(page);
}

const sleep = Number(flags.get("wait") ?? 350);
const timer = Promise.withResolvers<void>();
setTimeout(() => timer.resolve(), sleep);
await timer.promise;

const metrics = await page.evaluate(() => ({
  height: document.documentElement.scrollHeight,
  width: document.documentElement.scrollWidth,
  inner: window.innerWidth,
}));

const file = `${outdir}/${name ?? (route || "home").replace(/[^a-z0-9]+/giu, "-")}-${width}.png`;
await page.screenshot({ path: file, fullPage: full });
console.log(JSON.stringify({ file, exists: existsSync(file), ...metrics }));
await browser.close();
