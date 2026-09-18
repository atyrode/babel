import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { Watch } from "../web.tsx";
import { MACHINES, POLICY, fakeHost, runsResult, watchDoors } from "./host.ts";
import { mount, settle, unmountAll } from "./render.tsx";

/*
  THE CEILINGS, AND THE BOUNDED EXCEPTION OVER THEM (#260).

  What the panel has to get right is the difference between the two. On 2026-09-13 the drain's
  batch of 256 and its ceiling of $100 were installed as THE policy, and nothing on any screen
  said they were temporary or what they had replaced — so they outlived the drain by ninety
  minutes and the operator learnt it from a runbook step. The overlay strip is that sentence:
  what moved, from what to what, and for how much longer, beside the standing figures rather
  than instead of them.
*/

const OVERLAY = ".plugin-atyrode_babel_watch__overlay";
const FIGURE = ".plugin-atyrode_babel_watch__stat-value";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

async function open(policy: typeof POLICY): Promise<HTMLElement> {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]), policy: () => policy }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return root;
}

test("with nothing overlaid the panel shows the standing ceilings and no overlay strip", async () => {
  const root = await open(POLICY);
  expect([...root.querySelectorAll(FIGURE)].map((figure) => figure.textContent)).toEqual([
    "$2.00",
    "$20.00",
    "3",
  ]);
  expect(root.querySelector(OVERLAY)).toBeNull();
});

test("an overlay in force says what it moved, from what, and how much longer it lasts", async () => {
  const root = await open({
    ...POLICY,
    overlay: {
      id: "bdg_drain",
      createdAt: "2026-09-13T10:40:00.000Z",
      // Forty-two minutes and a second ahead of the panel's own clock, which is the wall clock.
      expiresAt: new Date(Date.now() + 42 * 60_000 + 1_000).toISOString(),
      reason: "draining victorballu before the 13:00Z reset",
      changes: [
        { field: "concurrentPerMachine", standing: 3, overlaid: 16 },
        { field: "dailyCost", standing: 20, overlaid: 40 },
      ],
    },
  });

  const strip = root.querySelector(OVERLAY);
  if (strip === null) throw new Error("an overlay in force is not on the screen");
  expect(strip.querySelector(".plugin-atyrode_babel_watch__stat-label")?.textContent).toBe(
    "Overlay for 42m",
  );
  expect(
    [...strip.querySelectorAll(".plugin-atyrode_babel_watch__lane")].map((row) => row.textContent),
  ).toEqual(["At once, per machine 3 → 16", "Per day $20.00 → $40.00"]);
  expect(strip.querySelector(".plugin-atyrode_babel_watch__muted")?.textContent).toBe(
    "draining victorballu before the 13:00Z reset",
  );

  // AND THE STANDING FIGURES ARE UNTOUCHED: the day's ceiling still reads what the policy says,
  // so "what Babel does every day" and "what a drain is doing for the next 42 minutes" are two
  // readings and never one.
  expect([...root.querySelectorAll(FIGURE)].map((figure) => figure.textContent)).toEqual([
    "$2.00",
    "$20.00",
    "3",
  ]);
});

test("an overlay whose expiry has passed is shown as expired rather than as time remaining", async () => {
  const root = await open({
    ...POLICY,
    overlay: {
      id: "bdg_stale",
      createdAt: "2026-09-13T10:40:00.000Z",
      expiresAt: new Date(Date.now() - 1_000).toISOString(),
      reason: "",
      changes: [{ field: "concurrentPerMachine", standing: 3, overlaid: 8 }],
    },
  });

  // The panel holds the answer a poll gave it; an overlay that lapsed between two polls must
  // say so rather than keep counting down, because a stale read is what "0% → 4%" was made of.
  const strip = root.querySelector(OVERLAY);
  expect(strip?.querySelector(".plugin-atyrode_babel_watch__stat-label")?.textContent).toBe(
    "Overlay expired",
  );
  expect(strip?.querySelector(".plugin-atyrode_babel_watch__lane")?.textContent).toBe(
    "At once, per machine 3 → 8",
  );
  expect(strip?.querySelector(".plugin-atyrode_babel_watch__muted")?.textContent).toBe(
    "No reason recorded.",
  );
});
