import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS } from "../../contract.ts";
import { Watch } from "../web.tsx";
import { MACHINES, fakeHost, runsResult, watchDoors, type FakeHost } from "./host.ts";
import { mount, settle, unmountAll } from "./render.tsx";

/*
  START SOMETHING, as the operator meets it: a sentence, and no way to post (#279).

  What stood here was five preset cards, a machine picker, a session picker and a button, and
  every assertion in this file was about what reached the `launch` door. None of it is Babel's
  to decide any more: a run's model, thinking level and account belong to a Code profile, and
  Code's `runSession` door is what posts the omp job. So the cases that pinned the form are
  deleted rather than re-pinned, and what stands in their place is the property that matters
  while the door is missing — the panel offers nothing that can post, and says why.
*/

async function open(): Promise<{ readonly root: HTMLElement; readonly fake: FakeHost }> {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]) }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return { root, fake };
}

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

test("the Start section is the refusal, and it names both issues the operator waits on", async () => {
  const { root } = await open();
  const text = root.textContent ?? "";

  expect(text).toContain("Babel runs are Code sessions");
  expect(text).toContain("runSession");
  // The two issues are what a reader schedules against, so they are on the screen and not only
  // in a door's answer: manifold#575 is the missing in-process door call, code#170 is the door.
  expect(root.querySelector("a[href='https://github.com/atyrode/manifold/issues/575']")).not.toBeNull();
  expect(root.querySelector("a[href='https://github.com/atyrode/code/issues/170']")).not.toBeNull();
  expect(text).toContain("engine_pending");
});

test("there is no button that can post, and nothing polls a launch", async () => {
  const { root, fake } = await open();

  // A form whose button always refused would make the operator discover the refusal by
  // pressing it; there is no button, no preset card and no session picker on this section.
  expect(root.querySelector("[data-action='atyrode.babel.launch']")).toBeNull();
  expect(root.querySelector(".plugin-atyrode_babel_watch__preset")).toBeNull();
  expect(fake.callsTo(ACTIONS.launch)).toHaveLength(0);
});

test("the rest of Watch is untouched: the runs feed is still read", async () => {
  // The revert removed a way to START work, not the panel that watches it. A screen that lost
  // its runs feed with its launch form would be an operator unable to see what is already
  // going on, which is the failure the whole panel exists against.
  const { fake } = await open();
  expect(fake.callsTo(ACTIONS.runs).length).toBeGreaterThan(0);
  expect(fake.callsTo(ACTIONS.drainStatus).length).toBeGreaterThan(0);
});
