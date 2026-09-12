import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS } from "../../contract.ts";
import { Watch } from "../web.tsx";
import { MACHINES, fakeHost, runRow, runsResult, watchDoors } from "./host.ts";
import { click, mount, settle, unmountAll } from "./render.tsx";

/*
  THE RUNS, THE RECIPES AND THE CEILINGS as the operator reads them.

  The tick is the assertion that matters here: a run's elapsed figure has to advance between
  polls, because a control room whose clock only moves when the server answers is a control room
  that looks frozen for five seconds at a time. So the test reads the cell, waits past one second
  WITHOUT the runs door being asked again, and requires the figure to have moved.
*/

const ELAPSED = ".plugin-atyrode_babel_watch__elapsed";
const LAST_WORD = ".plugin-atyrode_babel_watch__live-row td:nth-child(5)";
const DOT = ".plugin-atyrode_babel_watch__dot";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

/** The whole seconds a clock cell is showing, as a number, so a tick is comparable. */
function clockSeconds(root: HTMLElement, selector: string): number {
  const text = root.querySelector(selector)?.textContent ?? "";
  const match = /(\d+)s/.exec(text);
  if (match?.[1] === undefined) throw new Error(`no seconds in ${JSON.stringify(text)}`);
  return Number(match[1]);
}

test("a run in flight ticks its own clock between polls, and wears the live mark", async () => {
  const base = Date.now();
  const fake = fakeHost(
    watchDoors({
      runs: () =>
        runsResult([
          runRow({
            id: "run_live",
            state: "running",
            startedAt: new Date(base - 5_000).toISOString(),
            lastWord: new Date(base - 2_000).toISOString(),
            records: 7,
          }),
        ]),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  expect(root.textContent).toContain("1 in flight");
  expect(root.querySelector(DOT)).not.toBeNull();
  const elapsed = clockSeconds(root, ELAPSED);
  expect(elapsed).toBeGreaterThanOrEqual(5);
  const word = clockSeconds(root, LAST_WORD);
  expect(word).toBeGreaterThanOrEqual(2);
  expect(root.querySelector(LAST_WORD)?.textContent).toContain("last word");
  const reads = fake.callsTo(ACTIONS.runs).length;

  await settle(1_150);

  expect(clockSeconds(root, ELAPSED)).toBeGreaterThan(elapsed);
  expect(clockSeconds(root, LAST_WORD)).toBeGreaterThan(word);
  expect(fake.callsTo(ACTIONS.runs).length).toBe(reads);
});

test("stop asks the door for that run and says what stopping means", async () => {
  const base = Date.now();
  const fake = fakeHost(
    watchDoors({
      runs: () =>
        runsResult([
          runRow({
            id: "run_live",
            state: "running",
            startedAt: new Date(base - 30_000).toISOString(),
            lastWord: new Date(base - 1_000).toISOString(),
          }),
        ]),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  await click(root.querySelector("[data-action='atyrode.babel.stop']"));
  await settle();

  expect(fake.callsTo(ACTIONS.stop).at(-1)?.args).toEqual({ runId: "run_live", reason: "" });
  expect(root.textContent).toContain("Asked run_live to stop; it stops at its next safe point.");
});

test("a run nothing has been heard from keeps its row and loses only the mark", async () => {
  const base = Date.now();
  const fake = fakeHost(
    watchDoors({
      runs: () =>
        runsResult([
          runRow({
            id: "run_quiet",
            state: "running",
            freshness: "lost",
            startedAt: new Date(base - 3_600_000).toISOString(),
            lastWord: new Date(base - 1_800_000).toISOString(),
          }),
        ]),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  expect(root.textContent).toContain("run_quiet");
  expect(root.textContent).toContain("1 in flight, 0 heard from lately");
  expect(root.querySelector(DOT)).toBeNull();
  expect(root.querySelector(LAST_WORD)?.getAttribute("title")).toBe(
    "Nothing heard for a long time. That is not the same as dead.",
  );
});

test("an ended run is a receipt: what it took, wrote, spent and how it closed", async () => {
  const base = Date.now();
  const fake = fakeHost(
    watchDoors({
      runs: () =>
        runsResult(
          [
            runRow({
              id: "run_done",
              state: "finished",
              freshness: "ended",
              startedAt: new Date(base - 600_000).toISOString(),
              finishedAt: new Date(base - 540_000).toISOString(),
              lastWord: new Date(base - 540_000).toISOString(),
              records: 40,
              costUsd: 1.25,
            }),
          ],
          9,
        ),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  expect(root.textContent).toContain("Nothing running. Every row below is a receipt.");
  expect(root.querySelector(ELAPSED)).toBeNull();
  const row = root.querySelector("tbody tr")?.textContent ?? "";
  expect(row).toContain("run_done");
  expect(row).toContain("1m 00s");
  expect(row).toContain("$1.25");
  expect(row).toContain("finished");
  expect(root.textContent).toContain("8 older runs in the store.");
});

test("reading more asks the door for a longer page, not for a second one", async () => {
  const fake = fakeHost(
    watchDoors({
      runs: () =>
        runsResult(
          [
            runRow({
              id: "run_done",
              state: "finished",
              freshness: "ended",
              startedAt: "2026-09-12T08:00:00.000Z",
              finishedAt: "2026-09-12T08:05:00.000Z",
              lastWord: "2026-09-12T08:05:00.000Z",
            }),
          ],
          60,
        ),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  expect(fake.callsTo(ACTIONS.runs)[0]?.args).toEqual({ limit: 25, offset: 0 });

  await click(root.querySelector(".plugin-atyrode_babel_watch__quiet"));
  await settle();

  /*
    One page that grows, never a window that slides: the runs above the fold are the live ones,
    and a second page would drop them out of the table the moment the operator asked for older
    receipts.
  */
  expect(fake.callsTo(ACTIONS.runs).at(-1)?.args).toEqual({ limit: 50, offset: 0 });
});

test("the recipes read as names with what each looks for and when it last ran", async () => {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]) }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  const rows = [...root.querySelectorAll(".plugin-atyrode_babel_watch__recipe")].map((row) => row.textContent ?? "");
  expect(root.textContent).toContain("1 of 2 enabled");
  expect(rows[0]).toContain("Code health: comprehensibility");
  expect(rows[0]).toContain("Where the code is hard to read, and what that cost.");
  expect(rows[0]).toContain("42 runs");
  // No title in the policy payload: the id is the name, never a blank cell.
  expect(rows[1]).toContain("babel-tunes-itself");
  expect(rows[1]).toContain("off");
  expect(rows[1]).toContain("never run");
});

test("the ceilings section states both ceilings, the concurrency and today's spend", async () => {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]) }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  const stats = [...root.querySelectorAll(".plugin-atyrode_babel_watch__stat")].map((stat) => stat.textContent ?? "");
  expect(stats[0]).toContain("$2.00");
  expect(stats[1]).toContain("$20.00");
  expect(stats[1]).toContain("$4.50 spent since midnight UTC.");
  expect(stats[2]).toContain("3");
  expect(root.querySelector("[role='meter']")?.getAttribute("aria-valuenow")).toBe("4.5");
  expect(root.textContent).toContain("40% coverage · evidence-checker");
  expect(root.textContent).toContain("Policy pol_7 recorded");
});

test("a door that refuses a read leaves the section standing with the hub's sentence", async () => {
  const fake = fakeHost(
    watchDoors({
      runs: () => {
        throw new Error("the store is not open yet");
      },
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();

  expect(root.textContent).toContain("the store is not open yet");
  expect(root.textContent).toContain("Start something");
  expect(root.textContent).toContain("Ceilings");
});
