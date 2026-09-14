import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS, LaunchRequestSchema, LaunchInputSchema, OPERATIONS } from "../../contract.ts";
import { Watch } from "../web.tsx";
import { BROKER_SCOPE, MACHINES, fakeHost, launchAnswer, runsResult, watchDoors, type FakeHost } from "./host.ts";
import { choose, click, mount, settle, type, unmountAll } from "./render.tsx";

/*
  START SOMETHING, as the operator meets it: five named requests, the one knob each owns, the
  session the run will be answered by, and the sentence saying what will run before the button is
  pressable. The assertions are about what reaches the door, because that is the contract: a
  preset that posted a flag its kind refuses would be refused by the hub, a preset that posted
  the wrong number would run the wrong job, and a launch that named no session is refused
  `session_required` after the press instead of being explained before it (#279).
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

/** The text field under the label with this word: the model, and the typed account's three. */
function field(root: HTMLElement, label: string): HTMLInputElement {
  for (const entry of root.querySelectorAll("label")) {
    if (entry.querySelector(KNOB_LABEL)?.textContent === label) {
      const input = entry.querySelector<HTMLInputElement>("input");
      if (input !== null) return input;
    }
  }
  throw new Error(`no field labelled ${label}`);
}

/**
 * THE SESSION THE FIXTURES MAKE: the first account the broker offers, at the model the machine's
 * last completed run recorded — which is what the field is prefilled with, so the account is the
 * only thing an operator must do to make the button pressable.
 */
const SESSION = {
  model: "anthropic/claude-opus-5",
  account: {
    provider: "anthropic",
    scope: BROKER_SCOPE,
    credentialId: "7",
    identityKey: "victorballu",
  },
};

/** Choosing the offered account: the half of the session no preset that reaches a model runs without. */
async function pickAccount(root: HTMLElement, credentialId = "7"): Promise<void> {
  await choose(picker(root, "Account"), credentialId);
  await settle();
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
  // Nor is there an account to offer: the rows are one machine's own, so the picker waits for
  // it rather than showing a select nobody can fill.
  expect(fake.callsTo(ACTIONS.accounts)).toHaveLength(0);
  expect(() => picker(root, "Account")).toThrow();
});

test("picking a machine states what will run — the session, what it is metered at, both ceilings, and the last run", async () => {
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
  // WHAT THE RUN WILL BE: the session the request would carry, and the OWNER's own price and
  // ceiling for it — never this panel's arithmetic (ADR 0038, #279).
  expect(line).toContain("Will run explore as anthropic/claude-opus-5 on victorballu");
  expect(line).toContain("$5.0000 per million input tokens and $25.0000 per million output");
  expect(line).toContain("under a ceiling of $2.0000 for this run");
  expect(line).toContain("stops at $2.00 this run, $20.00 today");
  // AND WHAT ACTUALLY RAN LAST, from the receipt that recorded it: all three of the launch
  // profile, because a model without its thinking level and its account does not say which
  // window was spent (#251).
  expect(line).toContain("last run here: anthropic/claude-opus-5 at high on victorballu");
  // AND THE SESSION AS THE DOOR SEES IT, beside that price: the account, the model and the
  // policy's state as the word an operator acts on.
  const state = root.querySelector(".plugin-atyrode_babel_watch__session-state")?.textContent ?? "";
  expect(state).toContain("priced");
  expect(state).toContain("victorballu");
  expect(state).toContain("anthropic/claude-opus-5");
  // The button is NOT pressable yet. A preset that reaches a model must name the account it
  // spends, and the panel says which half is missing rather than letting the door say
  // `session_required` after the press.
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(true);
  expect(root.textContent).toContain("session_incomplete: choose the account this run spends");
});

test("a machine whose policy is not installed says so instead of a price", async () => {
  // The state an operator acts on differently from every other: there is no lane to a model at
  // all, and `setupInference` is what makes one. A panel that showed a ceiling here would be
  // stating a bound on calls that cannot happen.
  const fake = fakeHost(
    watchDoors({
      runs: () => runsResult([]),
      launchPreview: () =>
        launchAnswer({
          runId: "",
          jobId: "",
          profile: null,
          session: {
            serviceId: "atyrode.babel.inference",
            account: "",
            model: "",
            priced: false,
            policy: "missing",
            unreadable: "",
            note:
              "no atyrode.babel.inference policy is installed on this machine, so a run has no " +
              "lane to a model; setupInference installs one",
          },
        }),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  const line = root.querySelector(WILLRUN)?.textContent ?? "";
  expect(line).toContain("choose a model and an account");
  expect(line).toContain("no atyrode.babel.inference policy is installed on this machine");
  expect(line).not.toContain("last run here");
  expect(root.querySelector("[data-policy='missing']")?.textContent).toBe("no policy");
});

test("the button posts the contract's launch request, with the node it is authorized at", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await pickAccount(root);
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  const calls = fake.callsTo(ACTIONS.launch);
  expect(calls.at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "read-whats-new",
      sinceDays: 1,
      // WHO ANSWERS IT, exactly as the picker made it: no thinking level, because the level
      // nobody chose is the model's own default and is absent rather than empty (#279).
      session: SESSION,
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
  await pickAccount(root);
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
  await pickAccount(root);
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
  await pickAccount(root);
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "explore-topic",
      entityId: "ent_1a2b3c4d",
      session: SESSION,
      operation: { kind: "operation", machineId: "m-dev-01", operationId: OPERATIONS.explore },
    }),
  );
});

test("a chosen recipe replaces the default set in the launch input", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await pickAccount(root);
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
      session: SESSION,
    }),
  );
});

test("a refused launch shows the hub's own sentence and starts nothing", async () => {
  const refuse = () => {
    throw new Error("m-dev-01 is not enrolled for atyrode.babel.explore");
  };
  const fake = fakeHost(
    watchDoors({ runs: () => runsResult([]), launchPreview: refuse, launch: refuse }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  expect(root.querySelector(WILLRUN)?.textContent).toContain("not enrolled for atyrode.babel.explore");
  // The preview never answered, so nothing prefilled the model: the session is made by hand and
  // the refusal under the button is then the LAUNCH's own, not a preview note left over.
  await type(field(root, "Model"), "anthropic/claude-opus-5");
  await settle();
  await pickAccount(root);
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();
  expect(fake.callsTo(ACTIONS.launch)).toHaveLength(1);
  expect(root.querySelector(".plugin-atyrode_babel_watch__note")?.textContent).toContain(
    "not enrolled for atyrode.babel.explore",
  );
});

test("the picker offers the machine's own accounts, and refuses the one the broker blocked", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  // ONE MACHINE'S BROKER. The rows are that host's enrolled credentials, so the door is asked
  // about the machine the operator picked and about nothing else.
  expect(fake.callsTo(ACTIONS.accounts).at(-1)?.args).toEqual({ machineId: "m-dev-01" });
  const accounts = picker(root, "Account");
  expect([...accounts.options].map((option) => option.textContent)).toEqual([
    "Pick an account…",
    "victorballu · anthropic",
    "helena · anthropic · blocked",
  ]);
  expect([...accounts.options].map((option) => option.disabled)).toEqual([false, false, true]);

  // A blocked account is OFFERED and MARKED, because "this exists and you may not spend it" is
  // an answer; choosing it names the refusal here rather than after the job is posted and a
  // claim taken, which is what the machine's `account_unavailable` would cost.
  await pickAccount(root, "9");
  expect(root.textContent).toContain("account_blocked");
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(true);
});

test("an account and a model make the session: the preview asks again, now naming it", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  // Prefilled from the machine's LAST COMPLETED RUN, which is a recorded model that has
  // actually answered there — never a guess at what the owner's policy prices.
  expect(field(root, "Model").value).toBe("anthropic/claude-opus-5");
  expect(fake.callsTo(ACTIONS.launchPreview)).toHaveLength(1);

  await pickAccount(root);
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(false);
  // The dry read is keyed on the request, and the session is part of it: the price and the
  // ceiling are facts about THAT model under THAT policy, so a new choice is a new question.
  const dry = fake.callsTo(ACTIONS.launchPreview);
  expect(dry).toHaveLength(2);
  expect(dry.at(-1)?.args).toMatchObject({ session: SESSION });
});

test("the thinking level is part of the session the button posts", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await pickAccount(root);
  await choose(picker(root, "Thinking"), "xhigh");
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "read-whats-new",
      sinceDays: 1,
      session: { ...SESSION, thinking: "xhigh" },
      operation: { kind: "operation", machineId: "m-dev-01", operationId: OPERATIONS.explore },
    }),
  );
});

test("a model that is not provider/model is named as such, and starts nothing", async () => {
  // omp routes by the model's provider and the owner's policy prices that same string, so a
  // bare model id misses the route and the price at once. It is refused here rather than by a
  // gateway answering `gateway_unavailable` and saying no more.
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await pickAccount(root);
  await type(field(root, "Model"), "claude-opus-5");
  await settle();

  expect(root.textContent).toContain("session_incomplete: claude-opus-5 is not a model reference");
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(true);
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();
  expect(fake.callsTo(ACTIONS.launch)).toHaveLength(0);
});

test("a broker nobody can read is named, and the account is typed instead", async () => {
  // `unavailable` is not an empty list: a hub where the accounts service is not installed, or a
  // caller not admitted to read it, is a picker that says so and takes the three fields a pool
  // slot NAMES — otherwise an operator who knows his credential's row number cannot spend it.
  const unavailable =
    "atyrode.omp.accounts.broker is not installed on this hub, so no account can be offered; " +
    "enrol one through the omp accounts plugin";
  const fake = fakeHost(
    watchDoors({ runs: () => runsResult([]), accounts: () => ({ accounts: [], unavailable }) }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();

  expect(root.textContent).toContain(unavailable);
  expect(() => picker(root, "Account")).toThrow();

  await type(field(root, "Provider"), "anthropic");
  await settle();
  await type(field(root, "Credential"), "11");
  await settle();
  await type(field(root, "Identity key"), "victorballu");
  await settle();
  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();

  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toMatchObject({
    session: {
      model: "anthropic/claude-opus-5",
      account: {
        provider: "anthropic",
        // Seen in no broker observation, so the scope names where it did come from. A pool's
        // slots need only agree with each other, and Babel's pool holds exactly one.
        scope: "atyrode.babel.watch/typed/m-dev-01",
        credentialId: "11",
        identityKey: "victorballu",
      },
    },
  });
});

test("the beat reaches no model: no picker, and the launch names no session", async () => {
  const { root, fake } = await open();
  await choose(picker(root, "Machine"), "m-dev-01");
  await settle();
  await pickAccount(root);
  await click([...root.querySelectorAll(PRESET)][4] ?? null);
  await settle();

  expect(() => picker(root, "Account")).toThrow();
  expect(() => field(root, "Model")).toThrow();
  expect(root.querySelector<HTMLButtonElement>(LAUNCH_BUTTON)?.disabled).toBe(false);

  await click(root.querySelector(LAUNCH_BUTTON));
  await settle();
  // The session the operator chose a moment ago does NOT travel: a preset posts exactly its own
  // knobs, and a beat that named a model would be naming one it never spends.
  expect(fake.callsTo(ACTIONS.launch).at(-1)?.args).toEqual(
    LaunchRequestSchema.parse({
      machineId: "m-dev-01",
      preset: "keep-going",
      minutes: 60,
      operation: { kind: "operation", machineId: "m-dev-01", operationId: OPERATIONS.scan },
    }),
  );
});
