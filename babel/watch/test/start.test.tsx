import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { GENERATOR_PLUGIN_ID, LAUNCHER_PANEL } from "@atyrode/manifold-code";
import { act } from "react";
import { ACTIONS, door } from "../../contract.ts";
import { Watch } from "../web.tsx";
import type { PROFILES } from "./host.ts";
import { MACHINES, TOPICS, fakeHost, runsResult, watchDoors, type FakeHost } from "./host.ts";
import { click, mount, settle, type, unmountAll } from "./render.tsx";

/*
  START SOMETHING, as the operator meets it (#279).

  WHAT IS NOT ON THE FORM is half of what these tests are for. A run's model, thinking level and
  account belong to a Code profile, and #284 put all three on this screen as fields of Babel's
  own; the revert took them out and they must not come back, so their ABSENCE is asserted rather
  than left to be noticed when somebody re-adds one.

  The other half is that what IS there posts the right thing: the profile the operator chose,
  at the revision he was shown it at, which is the whole of how `code_stale_preferences` can
  mean anything.
*/

async function open(profiles?: {
  readonly profiles: readonly (typeof PROFILES)[number][];
  readonly unavailable: string;
}): Promise<{ readonly root: HTMLElement; readonly fake: FakeHost }> {
  const fake = fakeHost(
    watchDoors({
      runs: () => runsResult([]),
      ...(profiles === undefined ? {} : { profiles: () => profiles }),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return { root, fake };
}

/** The Start section, scoped: two forms on this screen offer a machine picker. */
function section(root: HTMLElement): HTMLElement {
  const found = root.querySelector(".plugin-atyrode_babel_watch__start-section");
  if (found === null) throw new Error("the Start section is not on the screen");
  return found as HTMLElement;
}

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

test("the section renders the profiles Code answered, and offers no model, thinking or account field", async () => {
  const { root } = await open();
  const start = section(root);

  // The list is Code's, and each row says what Code will run it as and where Code last posted.
  const rows = [...start.querySelectorAll("[data-field='profile']")];
  expect(rows.map((row) => row.getAttribute("data-container"))).toEqual([
    "ctr_workbench",
    "ctr_spare",
  ]);
  expect(start.textContent).toContain("anthropic/claude-opus-4-1");
  expect(start.textContent).toContain("thinking high");
  // A profile whose saved selection no longer reviews is said rather than hidden.
  expect(start.textContent).toContain("no selection Code can review");

  /*
    THE THREE FIELDS THAT MUST NOT EXIST. Babel chooses no model, no thinking level and no
    account: it names a Code profile and Code decides all three. A field here would be Babel
    deciding again what a model run is, which is exactly what #290 reverted.
  */
  expect(start.querySelector("[data-field='model']")).toBeNull();
  expect(start.querySelector("[data-field='thinking']")).toBeNull();
  expect(start.querySelector("[data-field='account']")).toBeNull();
  expect(start.querySelector("[data-field='identityKey']")).toBeNull();
  expect(start.textContent).not.toContain("Thinking");
  expect(start.textContent).not.toContain("Account");
});

test("the generator link targets the chosen workspace, and names Code's launcher panel", async () => {
  const { root } = await open();
  const start = section(root);

  const link = start.querySelector(`a[data-panel='${GENERATOR_PLUGIN_ID}.${LAUNCHER_PANEL}']`);
  expect(link).not.toBeNull();
  // Before a choice it offers the first profile; after one it follows the choice, because the
  // operator opens the generator to parametrize THE workspace he is about to run on.
  expect(link?.getAttribute("href")).toBe("manifold://container/ctr_workbench");

  (start.querySelector("[data-container='ctr_spare']") as HTMLElement).click();
  await settle();
  expect(
    section(root)
      .querySelector(`a[data-panel='${GENERATOR_PLUGIN_ID}.${LAUNCHER_PANEL}']`)
      ?.getAttribute("href"),
  ).toBe("manifold://container/ctr_spare");
});

test("the button posts the launch carrying the chosen profile and the revision it was shown at", async () => {
  const { root, fake } = await open();
  const start = section(root);

  const button = start.querySelector(
    `[data-action='${door(ACTIONS.launch)}']`,
  ) as HTMLButtonElement | null;
  expect(button).not.toBeNull();
  // Nothing can post before a machine and a profile: the clause says which is missing.
  expect(button?.disabled).toBe(true);
  expect(start.textContent).toContain("Pick a machine to run on.");

  const machine = start.querySelector("[data-field='machine']") as HTMLSelectElement;
  machine.value = "m-dev-01";
  machine.dispatchEvent(new Event("change", { bubbles: true }));
  await settle();
  (section(root).querySelector("[data-container='ctr_workbench']") as HTMLElement).click();
  await settle();

  (
    section(root).querySelector(`[data-action='${door(ACTIONS.launch)}']`) as HTMLButtonElement
  ).click();
  await settle();

  const posted = fake.callsTo(ACTIONS.launch);
  expect(posted).toHaveLength(1);
  const args = posted[0]?.args as Record<string, unknown>;
  expect(args["profile"]).toEqual({ containerId: "ctr_workbench", expectedRevision: 7 });
  expect(args["machineId"]).toBe("m-dev-01");
  // …and nothing about a model, a thinking level or an account travels with it.
  expect(args["session"]).toBeUndefined();
});

test("minutes preserve typed digits and normalize bounds only when leaving the field", async () => {
  const { root } = await open();
  const start = section(root);
  const keepGoing = [
    ...start.querySelectorAll<HTMLButtonElement>(".plugin-atyrode_babel_watch__preset"),
  ].find((button) => button.textContent?.startsWith("Keep going"));
  if (keepGoing === undefined) throw new Error("keep-going preset absent");
  await click(keepGoing);
  const minutes = start.querySelector<HTMLInputElement>("input[type='number']")!;
  await type(minutes, "1");
  expect(minutes.value).toBe("1");
  await type(minutes, `${minutes.value}2`);
  expect(minutes.value).toBe("12");
  await act(async () => {
    minutes.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
  expect(minutes.value).toBe("12");
  await type(minutes, "2");
  await act(async () => {
    minutes.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  });
  expect(minutes.value).toBe("5");
});

test("an empty entity remains an explicit exploration target with an honest zero count", async () => {
  const fake = fakeHost(
    watchDoors({
      runs: () => runsResult([]),
      topics: () => ({
        ...TOPICS,
        topics: [
          {
            ...TOPICS.topics[0]!,
            posts: 0,
            awaiting: 0,
            latestAt: "",
            recentActivity: {
              since: "2026-09-06T00:00:00Z",
              days: [0, 0, 0, 0, 0, 0, 0],
              unknownDates: 0,
            },
          },
        ],
      }),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  const start = section(root);
  const explore = [
    ...start.querySelectorAll<HTMLButtonElement>(".plugin-atyrode_babel_watch__preset"),
  ].find((button) => button.textContent?.startsWith("Explore a topic"));
  if (!explore) throw new Error("exploration preset absent");
  explore.click();
  await settle();
  const machine = start.querySelector<HTMLSelectElement>("[data-field='machine']")!;
  machine.value = "m-dev-01";
  machine.dispatchEvent(new Event("change", { bubbles: true }));
  start.querySelector<HTMLElement>("[data-container='ctr_workbench']")!.click();
  await settle();
  const launch = start.querySelector<HTMLButtonElement>(`[data-action='${door(ACTIONS.launch)}']`)!;
  expect(launch.disabled).toBe(true);
  const topic = start.querySelector<HTMLSelectElement>("[data-field='topic']")!;
  expect(topic.options[1]!.textContent).toBe("babel · 0 posts");
  topic.value = TOPICS.topics[0]!.id;
  topic.dispatchEvent(new Event("change", { bubbles: true }));
  await settle();
  expect(topic.value).toBe(TOPICS.topics[0]!.id);
  expect(launch.disabled).toBe(false);
  expect(fake.callsTo(ACTIONS.launch)).toEqual([]);
});

test("a Code that could not be asked is the sentence it refused with, and no button that can post", async () => {
  const { root } = await open({
    profiles: [],
    unavailable: "engine_unavailable: atyrode.babel -> atyrode.code",
  });
  const start = section(root);

  expect(start.querySelector("[data-field='profiles-unavailable']")?.textContent).toContain(
    "atyrode.babel -> atyrode.code",
  );
  expect(start.querySelector("[data-field='profile']")).toBeNull();
  const button = start.querySelector(
    `[data-action='${door(ACTIONS.launch)}']`,
  ) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
});
