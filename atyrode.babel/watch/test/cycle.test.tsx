import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import type { CycleReport } from "../api.ts";
import { Watch } from "../web.tsx";
import { MACHINES, fakeHost, pulseResult, runsResult, watchDoors } from "./host.ts";
import { mount, settle, unmountAll } from "./render.tsx";

/*
  WHY NOTHING RAN, AND WHEN NOT TO SAY IT (#328).

  The conductor stopped for a reason and declined each candidate for another, and every word of
  it went to the hub's log: an operator looking at an empty runs table could not tell a loop
  that had nothing to do from one that could not dispatch. What this panel has to get right is
  both halves of that — the reason, and the silence when there is no reason. A section headed
  "no problems" on every healthy cycle is the same non-answer the log was, with a frame round it.
*/

const SECTION = ".plugin-atyrode_babel_watch__cycle";
const GAP = ".plugin-atyrode_babel_watch__gap";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

async function open(cycle: CycleReport | null): Promise<HTMLElement> {
  const fake = fakeHost(
    watchDoors({ runs: () => runsResult([]), pulse: () => pulseResult(cycle) }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return root;
}

test("a cycle that stopped says why, in the operator's words and with the loop's own sentence", async () => {
  const root = await open({
    at: "2026-09-12T08:59:30.000Z",
    stop: {
      reason: "unrouted",
      detail: "policy pol_7 enables evaluation but names no Code profile and machine",
    },
    gaps: [],
  });

  const section = root.querySelector(SECTION);
  if (section === null) throw new Error("a cycle that stopped is not on the screen");
  expect(section.querySelector(".plugin-atyrode_babel_watch__lede")?.textContent).toBe(
    "The policy names no Code profile to run a review on.",
  );
  // …and the coordinator's own sentence under it, which is where the policy version is: the
  // operator's remedy needs the name of the thing to change, not only the kind of fault.
  expect(section.querySelector(".plugin-atyrode_babel_watch__mono")?.textContent).toContain(
    "policy pol_7 enables evaluation but names no Code profile and machine",
  );
  expect(root.querySelectorAll(GAP)).toHaveLength(0);
});

test("the candidates a cycle declined are one row per reason, counted, with one example each", async () => {
  const root = await open({
    at: "2026-09-12T08:59:30.000Z",
    stop: { reason: "no-candidates", detail: "no eligible review remains" },
    gaps: [
      {
        reason: "claimed",
        count: 412,
        recordId: "hyp_00000009",
        detail: "claimed by another worker while this draw was reading its candidates",
      },
      { reason: "cooling", count: 2, recordId: "fnd_0000000a", detail: "reviewed 9m ago" },
    ],
  });

  const rows = [...root.querySelectorAll(GAP)];
  expect(rows).toHaveLength(2);

  // FOUR HUNDRED CONTENDED DRAWS ARE ONE ROW WITH A FIGURE ON IT. A panel given the list would
  // have replaced an invisible loop with an unreadable one.
  const contended = rows[0];
  if (contended === undefined) throw new Error("the counted gap is not on the screen");
  expect(contended.querySelector(".plugin-atyrode_babel_watch__mono")?.textContent).toBe(
    "412 × claimed",
  );
  expect(contended.textContent).toContain("another worker holds the claim");
  // The count says how much; the record says where to look.
  expect(contended.textContent).toContain("hyp_00000009");

  expect(rows[1]?.textContent).toContain("2 × cooling");
});

test("a cycle that spent normally renders nothing at all", async () => {
  // `batch` is the loop saying it dispatched everything one cycle allows. It is the commonest
  // stop on a working deployment, and a section that appeared for it would be on the screen
  // every thirty seconds saying that nothing is wrong.
  const root = await open({
    at: "2026-09-12T08:59:30.000Z",
    stop: { reason: "batch", detail: "dev-01 already holds 4 of 4 review slots" },
    gaps: [],
  });
  expect(root.querySelector(SECTION)).toBeNull();
});

test("a deployment whose loop has never run renders nothing rather than an empty heading", async () => {
  const root = await open(null);
  expect(root.querySelector(SECTION)).toBeNull();
});
