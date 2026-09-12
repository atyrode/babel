import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS, LaunchRequestSchema, LaunchInputSchema, OPERATIONS } from "../../contract.ts";
import { Watch } from "../web.tsx";
import { MACHINES, fakeHost, runsResult, watchDoors, type FakeHost } from "./host.ts";
import { choose, click, mount, settle, type, unmountAll } from "./render.tsx";

/*
  START SOMETHING, as the operator meets it: five named requests, the one knob each owns, and the
  sentence saying what will run before the button is pressable. The assertions are about what
  reaches the door, because that is the contract: a preset that posted a flag its kind refuses
  would be refused by the hub, and a preset that posted the wrong number would run the wrong job.
*/

const PRESET = ".plugin-atyrode_babel_watch__preset";
const KNOB_LABEL = ".plugin-atyrode_babel_watch__knob-label";
const WILLRUN = ".plugin-atyrode_babel_watch__willrun-line";
const LAUNCH_BUTTON = "[data-action='atyrode.babel.launch']";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

/** The select under the label with this word; two pickers can be on screen at once. */
function picker(root: HTMLElement, label: string): HTMLSelectElement {
  for (const field of root.querySelectorAll("label")) {
    if (field.querySelector(KNOB_LABEL)?.textContent === label) {
      const select = field.querySelector("select");
      if (select !== null) return select;
    }
  }
  throw new Error(`no picker labelled ${label}`);
}

function knobInput(root: HTMLElement): HTMLInputElement {
  const input = root.querySelector<HTMLInputElement>(".plugin-atyrode_babel_watch__knob-input");
  if (input === null) throw new Error("no knob on screen");
  return input;
}

async function open(): Promise<{ readonly root: HTMLElement; readonly fake: FakeHost }> {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]) }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return { root, fake };
}

test("every preset says what it does; the first one is open with its own knob", async () => {
  const { root } = await open();
  const cards = [...root.querySelectorAll(PRESET)];
  expect(cards.map((card) => card.querySelector(".plugin-atyrode_babel_watch__preset-title")?.textContent)).toEqual([
    "Read what's new",
    "Explore a topic",
    "Review the backlog",
    "File and tidy",
    "Keep going",
  ]);
  expect(cards[0]?.querySelector(".plugin-atyrode_babel_watch__preset-does")?.textContent).toBe(
    "Reads the sessions since you last looked and writes up what it found.",
  );
  expect(cards[0]?.getAttribute("aria-pressed")).toBe("true");
  expect(root.querySelector(KNOB_LABEL)?.textContent).toBe("Days back");
  expect(knobInput(root).value).toBe("1");
});

test("with no machine there is no launch and no preview: the card says why", async () => {
  const { root, fake } = await open();
  expect(root.textContent).toContain("Pick a machine to run on.");
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(true);
  expect(root.querySelector(WILLRUN)?.textContent).toContain("Pick a machine to run on.");
  expect(fake.callsTo(ACTIONS.launchPreview)).toHaveLength(0);
  expect(fake.callsTo(ACTIONS.launch)).toHaveLength(0);
});

test("picking a machine states what will run — profile, model, cost per 1k, both ceilings", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  // The dry read is its own door, and it carries no node: a preview asks nothing of a machine,
  // so it must not need the operator's version-bound consent to say what a run would cost.
  const dry = fake.callsTo(ACTIONS.launchPreview);
  expect(dry).toHaveLength(1);
  expect(dry[0]?.args).toEqual({
    machineId: "m-dev-01",
    preset: "read-whats-new",
    sinceDays: 1,
    recipes: [],
  });
  expect(fake.callsTo(ACTIONS.launch)).toHaveLength(0);

  const line = root.querySelector(WILLRUN)?.textContent ?? "";
  expect(line).toContain("claude-opus-4");
  expect(line).toContain("babel-explore/4");
  expect(line).toContain("$0.015 in / $0.075 out per 1k");
  expect(line).toContain("stops at $2.00 this run, $20.00 today");
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(false);
});

test("the button posts the contract's launch request, with the node it is authorized at", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  const calls = fake.callsTo(ACTIONS.launch);
  expect(calls.at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "read-whats-new",
      sinceDays: 1,
      operation: {
        kind: "operation",
        machineId: "m-dev-01",
        operationId: OPERATIONS.explore,
      },
    }),
  );
  expect(root.textContent).toContain("Started explore as run_new");
});

test("a knob's value is what reaches the door", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await type(knobInput(root), "9");
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toMatchObject(
    LaunchInputSchema.parse({ machineId: "m-dev-01", preset: "read-whats-new", sinceDays: 9 }),
  );
});

test("switching preset switches the knob, and the old preset's knob is not sent", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await click([...root.querySelectorAll(PRESET)][4] ?? null);
  await settle();

  expect(root.querySelector(KNOB_LABEL)?.textContent).toBe("Minutes");
  expect(knobInput(root).value).toBe("60");
  expect(fake.callsTo(ACTIONS.launchPreview).at(-1)?.args).toEqual({
    machineId: "m-dev-01",
    preset: "keep-going",
    minutes: 60,
    recipes: [],
  });
});

test("explore a topic offers the topics door's own rows and will not start without one", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await click([...root.querySelectorAll(PRESET)][1] ?? null);
  await settle();

  expect(root.textContent).toContain("Pick a topic to explore.");
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(true);
  const topics = picker(root, "Topic");
  expect([...topics.options].map((option) => option.textContent)).toEqual([
    "Pick a topic…",
    "babel · 128 posts",
    "manifold · 61 posts",
  ]);

  await choose(topics, "ent_1a2b3c4d");
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "explore-topic",
      entityId: "ent_1a2b3c4d",
      operation: { kind: "operation", machineId: "m-dev-01", operationId: OPERATIONS.explore },
    }),
  );
});

test("a chosen recipe replaces the default set in the launch input", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  const chips = [...root.querySelectorAll(".plugin-atyrode_babel_watch__chip")];
  expect(chips.map((chip) => chip.textContent)).toEqual(["Code health: comprehensibility", "babel-tunes-itself"]);

  await click(chips[0] ?? null);
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toMatchObject(
    LaunchInputSchema.parse({
      machineId: "m-dev-01",
      preset: "read-whats-new",
      sinceDays: 1,
      recipes: ["code-health-comprehensibility"],
    }),
  );
});

test("a refused launch shows the hub's own sentence and starts nothing", async () => {
  const refuse = () => {
    throw new Error("dev-01 has no code engine configured");
  };
  const fake = fakeHost(
    watchDoors({ runs: () => runsResult([]), launchPreview: refuse, launch: refuse }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  expect(root.querySelector(WILLRUN)?.textContent).toContain("dev-01 has no code engine configured");
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();
  expect(root.textContent).toContain("dev-01 has no code engine configured");
});
