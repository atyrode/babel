import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS, DrainStartRequestSchema, OPERATIONS } from "../../contract.ts";
import { Watch } from "../web.tsx";
import { MACHINES, drainStatus, fakeHost, runsResult, watchDoors, type FakeHost } from "./host.ts";
import { choose, click, mount, settle, type, unmountAll } from "./render.tsx";

/*
  DRAINING A WINDOW, as the operator meets it (#258).

  The assertions are about the two things that were missing on 2026-09-13: what reaches the door
  when the button is pressed, and what the screen says while a drain is running. The first is the
  contract — a drain that posted the wrong account would spend the wrong window, and the whole
  point of #267 is that the account is named before the button rather than discovered after. The
  second is the go/no-go rule: the panel has to make "jobs at the model" and "tokens a minute"
  readable at a glance, because an operator who cannot read them asks "is anything running?" two
  hours in.
*/

const SECTION = ".plugin-atyrode_babel_watch__drain-section";
const DRAIN_PRESET = ".plugin-atyrode_babel_watch__drain-preset";
const DRAIN = ".plugin-atyrode_babel_watch__drain";
const START = "[data-action='atyrode.babel.drainStart']";
const STOP = "[data-action='atyrode.babel.drainStop']";
const KNOB_LABEL = ".plugin-atyrode_babel_watch__knob-label";
const STAT = ".plugin-atyrode_babel_watch__stat";
const ALLOCATION_MODE = "[data-field='drain-allocation-mode']";
const ALLOCATION = "[data-field='drain-allocation']";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

/**
 * The drain section itself. Every read below is scoped to it: the Start form on the same screen
 * has a "Machine" picker of its own, and a document-wide query would drive that one instead.
 */
function section(root: HTMLElement): HTMLElement {
  const found = root.querySelector<HTMLElement>(SECTION);
  if (found === null) throw new Error("the drain section is not on the screen");
  return found;
}

/** The field or select under the label with this word; the panel shows a dozen at once. */
function field(root: HTMLElement, label: string): HTMLElement {
  for (const wrapper of section(root).querySelectorAll("label")) {
    if (wrapper.querySelector(KNOB_LABEL)?.textContent !== label) continue;
    const control = wrapper.querySelector("input, select");
    if (control !== null) return control as HTMLElement;
  }
  throw new Error(`no field labelled ${label}`);
}

function preset(root: HTMLElement, title: string): HTMLElement {
  const card = [...section(root).querySelectorAll<HTMLElement>(DRAIN_PRESET)].find(
    (entry) =>
      entry.querySelector(".plugin-atyrode_babel_watch__preset-title")?.textContent === title,
  );
  if (card === undefined) throw new Error(`no drain preset ${title}`);
  return card;
}

/** One figure of the running-drain strip, by its label. */
function stat(root: HTMLElement, label: string): string {
  for (const box of section(root).querySelectorAll(STAT)) {
    if (box.querySelector(".plugin-atyrode_babel_watch__stat-label")?.textContent !== label)
      continue;
    return box.querySelector(".plugin-atyrode_babel_watch__stat-value")?.textContent ?? "";
  }
  throw new Error(`no figure labelled ${label}`);
}

async function open(
  answers: Parameters<typeof watchDoors>[0] = { runs: () => runsResult([]) },
): Promise<{ readonly root: HTMLElement; readonly fake: FakeHost }> {
  const fake = fakeHost(watchDoors(answers), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return { root, fake };
}
/**
 * Fills in a startable drain: the machine, the CODE PROFILE it spends, and why.
 *
 * There is no provider, credential, identity key, model or thinking field to fill any more
 * (#279): all five belong to the Code profile, and a drain names the profile. What it records
 * about the model and the account is Code's own report, copied at the press.
 */
async function compose(root: HTMLElement): Promise<void> {
  await choose(field(root, "Machine"), "m-dev-01");
  await settle();
  (section(root).querySelector("[data-container='ctr_workbench']") as HTMLElement).click();
  await settle();
  await type(field(root, "Why"), "the 7-day window resets at 13:00Z");
}

test("review and exploration allocation keeps only entered weights, the shared topic and the canonical operation", async () => {
  const { root, fake } = await open();
  await compose(root);
  // A previously selected mapping route must not redirect a mixed drain to its door.
  await click(preset(root, "Map transcripts"));
  await click(root.querySelector(ALLOCATION_MODE));
  expect((field(root, "Read what's new weight") as HTMLInputElement).value).toBe("");
  expect((field(root, "Explore a topic weight") as HTMLInputElement).value).toBe("");
  expect((field(root, "Review backlog weight") as HTMLInputElement).value).toBe("");
  expect((field(root, "Per-job cost threshold") as HTMLInputElement).value).toBe("");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);

  await type(field(root, "Review backlog weight"), "2.5");
  await type(field(root, "Explore a topic weight"), "1.5");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  await choose(field(root, "Topic"), "ent_1a2b3c4d");
  await type(field(root, "Jobs at once"), "4");
  await type(field(root, "Or at"), "5");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  await type(field(root, "Per-job cost threshold"), "0.125");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(false);
  await click(root.querySelector(START));

  expect(fake.callsTo(ACTIONS.drainStart)).toHaveLength(1);
  const posted = DrainStartRequestSchema.parse(fake.callsTo(ACTIONS.drainStart)[0]?.args);
  expect(posted.allocation).toEqual({ "explore-topic": 1.5, "review-backlog": 2.5 });
  expect(posted.preset).toBeUndefined();
  expect(posted.entityId).toBe("ent_1a2b3c4d");
  expect(posted.sinceDays).toBeUndefined();
  expect(posted.concurrent).toBe(4);
  expect(posted.target.costMicros).toBe(5_000_000);
  expect(posted.inferenceLimits).toEqual({ costMicros: 125_000 });
  expect(posted.profile).toEqual({ containerId: "ctr_workbench", expectedRevision: 7 });
  expect(posted.operation).toEqual({
    kind: "operation",
    machineId: "m-dev-01",
    operationId: OPERATIONS.explore,
  });
  expect(fake.callsTo(ACTIONS.mapDrainStart)).toHaveLength(0);
});

test("blank weights omit a preset and nonpositive weights cannot start a drain", async () => {
  const { root, fake } = await open();
  await compose(root);
  await click(root.querySelector(ALLOCATION_MODE));
  await type(field(root, "Per-job cost threshold"), "0.5");
  for (const weight of ["0", "-1", ""]) {
    await type(field(root, "Review backlog weight"), weight);
    expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
    await click(root.querySelector(START));
  }
  expect(fake.callsTo(ACTIONS.drainStart)).toHaveLength(0);
  await type(field(root, "Review backlog weight"), "0.5");
  // A preset removed from the allocation must no longer require a topic.
  await type(field(root, "Explore a topic weight"), "1");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  for (const weight of ["0", "-1"]) {
    await type(field(root, "Explore a topic weight"), weight);
    expect(section(root).querySelectorAll("select")).toHaveLength(1);
    expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  }
  await type(field(root, "Explore a topic weight"), "");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(false);
  await click(root.querySelector(START));
  const posted = DrainStartRequestSchema.parse(fake.callsTo(ACTIONS.drainStart)[0]?.args);
  expect(posted.allocation).toEqual({ "review-backlog": 0.5 });
  expect(posted.preset).toBeUndefined();
  expect(posted.entityId).toBeUndefined();
  expect(posted.sinceDays).toBeUndefined();
  expect(posted.operation.operationId).toBe(OPERATIONS.explore);
});

test("allocation refuses missing or nonpositive per-job thresholds instead of choosing one", async () => {
  const { root, fake } = await open();
  await compose(root);
  await click(root.querySelector(ALLOCATION_MODE));
  await type(field(root, "Review backlog weight"), "1");
  for (const cost of ["", "0", "-1", "0.0000001", "1e309"]) {
    await type(field(root, "Per-job cost threshold"), cost);
    expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
    await click(root.querySelector(START));
  }
  expect(fake.callsTo(ACTIONS.drainStart)).toHaveLength(0);
  await type(field(root, "Per-job cost threshold"), "0.000001");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(false);
  await click(root.querySelector(START));
  const posted = DrainStartRequestSchema.parse(fake.callsTo(ACTIONS.drainStart)[0]?.args);
  expect(posted.inferenceLimits).toEqual({ costMicros: 1 });
});

test("choosing the free rehearsal after allocation sends one catalog preset, not weights", async () => {
  const { root, fake } = await open();
  await compose(root);
  await click(root.querySelector(ALLOCATION_MODE));
  await type(field(root, "Review backlog weight"), "3");
  await type(field(root, "Per-job cost threshold"), "0.5");
  await click(preset(root, "Keep going"));
  await type(field(root, "Each catalog runs"), "15");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(false);
  await click(root.querySelector(START));
  const posted = DrainStartRequestSchema.parse(fake.callsTo(ACTIONS.drainStart)[0]?.args);
  expect(posted.preset).toBe("keep-going");
  expect(posted.allocation).toBeUndefined();
  expect(posted.inferenceLimits).toBeUndefined();
  expect(posted.minutes).toBe(15);
  expect(posted.operation.operationId).toBe(OPERATIONS.catalog);
});

test("single review keeps its evaluate operation without exploration knobs or mixed limits", async () => {
  const { root, fake } = await open();
  await compose(root);
  await click(root.querySelector(ALLOCATION_MODE));
  await type(field(root, "Explore a topic weight"), "1");
  await choose(field(root, "Topic"), "ent_1a2b3c4d");
  await type(field(root, "Per-job cost threshold"), "0.5");
  await click(preset(root, "Review backlog"));
  expect(section(root).querySelectorAll("select")).toHaveLength(1);
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(false);
  await click(root.querySelector(START));

  const posted = DrainStartRequestSchema.parse(fake.callsTo(ACTIONS.drainStart)[0]?.args);
  expect(posted.preset).toBe("review-backlog");
  expect(posted.allocation).toBeUndefined();
  expect(posted.inferenceLimits).toBeUndefined();
  expect(posted.entityId).toBeUndefined();
  expect(posted.sinceDays).toBeUndefined();
  expect(posted.operation.operationId).toBe(OPERATIONS.evaluate);
  expect(posted.profile).toEqual({ containerId: "ctr_workbench", expectedRevision: 7 });
  expect(fake.callsTo(ACTIONS.mapDrainStart)).toHaveLength(0);
});

test("nothing can be started until a Code profile is named, and no model or account field exists", async () => {
  const { root, fake } = await open();
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  expect(section(root).textContent).toContain("Pick a machine to drain on.");

  await choose(field(root, "Machine"), "m-dev-01");
  await settle();
  expect(section(root).textContent).toContain("Pick the Code profile this drain spends");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);

  /*
    THE FIVE FIELDS THAT MUST NOT EXIST. A provider, a credential id, an identity key, a model
    and a thinking level were all Babel deciding what a run is; Code owns every one of them.
    Their absence is asserted so they cannot drift back the next time somebody wants the drain
    to "just name an account".
  */
  for (const label of ["Provider", "Credential", "Identity key", "Model", "Thinking"]) {
    expect(section(root).textContent).not.toContain(label);
  }

  (section(root).querySelector("[data-container='ctr_workbench']") as HTMLElement).click();
  await settle();
  // …and the row says whose window Code reports it would spend, before the button.
  expect(section(root).textContent).toContain("Code reports victorballu@gmail.com");
  expect(section(root).textContent).toContain("Say why: the reason is recorded on the overlay.");
  expect(root.querySelector<HTMLButtonElement>(START)?.disabled).toBe(true);
  expect(fake.callsTo(ACTIONS.drainStart)).toHaveLength(0);
});

test("the button posts the profile, the fan, the deadline and the operation node", async () => {
  const { root, fake } = await open();
  await compose(root);
  await type(field(root, "Jobs at once"), "4");
  await type(field(root, "Stop after"), "90");
  await type(field(root, "Or at"), "5");
  await settle();

  const button = root.querySelector<HTMLButtonElement>(START);
  expect(button?.disabled).toBe(false);
  const before = Date.now();
  await click(button);
  await settle();

  const calls = fake.callsTo(ACTIONS.drainStart);
  expect(calls).toHaveLength(1);
  const posted = DrainStartRequestSchema.parse(calls[0]?.args);
  expect(posted.preset).toBe("read-whats-new");
  expect(posted.allocation).toBeUndefined();
  // THE PROFILE, AT THE REVISION THE OPERATOR WAS SHOWN IT AT: a profile that moved between
  // the read and the press is refused `code_stale_preferences` by Code, which is the whole
  // reason the revision travels rather than being re-read on the server.
  expect(posted.profile).toEqual({ containerId: "ctr_workbench", expectedRevision: 7 });
  expect(posted.concurrent).toBe(4);
  expect(posted.target.costMicros).toBe(5_000_000);
  // THE DEADLINE IS COMPUTED AT THE PRESS, from the minutes the operator set: a form left open
  // for ten minutes must not post a deadline ten minutes in the past.
  const deadline = Date.parse(posted.target.deadline ?? "");
  expect(deadline).toBeGreaterThanOrEqual(before + 90 * 60_000);
  expect(deadline).toBeLessThan(before + 91 * 60_000);
  // The node is what `machines:run` is discharged at; a request carrying only a machine id is
  // refused `invalid authority target` before the door is entered.
  expect(posted.operation).toEqual({
    kind: "operation",
    machineId: "m-dev-01",
    operationId: OPERATIONS.explore,
  });
  // The refusal or the receipt is the sentence beside the button, and it names the account.
  expect(root.textContent).toContain("Draining the-drain-account as drn_started");
});

test("a running drain shows the six figures the runbook names, and the account it is spending", async () => {
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_live",
          jobsLive: 3,
          jobsAtModel: 2,
          jobsStalled: 1,
          concurrent: 4,
          jobsLaunched: 7,
          jobsSettled: 4,
          outputTokensPerMinute: 1_240,
          costMicrosPerMinute: 310_000,
          spent: { calls: 18, inputTokens: 420_000, outputTokens: 9_100, costMicros: 2_400_000 },
          settled: { calls: 12, inputTokens: 300_000, outputTokens: 6_000, costMicros: 1_800_000 },
          target: {
            costMicros: 5_000_000,
            deadline: new Date(Date.now() + 40 * 60_000).toISOString(),
          },
          etaAt: new Date(Date.now() + 8 * 60_000).toISOString(),
          refusals: { schema: 2 },
          closures: { completed: 3, failed: 1 },
        }),
      ],
    }),
  });
  const strip = root.querySelector(DRAIN);
  if (strip === null) throw new Error("a running drain is not on the screen");

  expect(stat(root, "Jobs live")).toBe("3 of 4");
  expect(stat(root, "At the model")).toBe("2");
  expect(stat(root, "Output tokens")).toBe("1,240/min");
  expect(stat(root, "Spend")).toBe("$2.4000 of $5.0000");
  expect(stat(root, "Calls")).toBe("18");
  // THE ETA AGAINST THE DEADLINE, which is the comparison an operator acts on: eight minutes to
  // the target and forty to the deadline is a drain that will make it.
  expect(stat(root, "ETA")).toContain("target for 8m");
  expect(stat(root, "ETA")).toContain("deadline for 40m");
  expect(stat(root, "ETA")).not.toContain("after the deadline");

  // A stall is a silence and is said as one, beside the figure it is about.
  expect(strip.textContent).toContain("1 stalled");
  // The account and the model are on the row, not only on the form (#267).
  expect(strip.textContent).toContain("the-drain-account");
  expect(strip.textContent).toContain("anthropic/claude-sonnet-4-5");
  // A refused submission is paid work with no result, so it is its own row rather than a failure.
  expect(strip.textContent).toContain("refused schema");
  expect(strip.textContent).toContain("completed");
});

test("weighted status distinguishes metered cost, held estimates and unpriced or ineligible work", async () => {
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          allocation: [
            {
              preset: "review-backlog",
              weight: 3,
              share: 0.75,
              incurredCostMicros: 0,
              reservedCostMicros: 300_000,
              deficitCostMicros: 200_000,
              unpricedJobs: 2,
              gap: "missing-price: 2 jobs have no positive metered cost",
            },
            {
              preset: "explore-topic",
              weight: 1,
              share: 0.25,
              incurredCostMicros: 400_000,
              reservedCostMicros: 100_000,
              deficitCostMicros: -200_000,
              unpricedJobs: 0,
              gap: "no-eligible-work: the topic has no sessions",
            },
          ],
        }),
      ],
    }),
  });
  const allocation = root.querySelector(ALLOCATION);
  const review = allocation?.querySelector("[data-preset='review-backlog']")?.textContent;
  const explore = allocation?.querySelector("[data-preset='explore-topic']")?.textContent;
  expect(review).toContain("Review backlog");
  expect(review).toContain("weight 3 · share 75.0%");
  expect(review).toContain("incurred $0.0000");
  expect(review).toContain("reserved $0.3000");
  expect(review).toContain("deficit $0.2000");
  expect(review).toContain("2 unpriced job(s)");
  expect(review).toContain("missing-price: 2 jobs have no positive metered cost");
  expect(explore).toContain("weight 1 · share 25.0%");
  expect(explore).toContain("incurred $0.4000");
  expect(explore).toContain("reserved $0.1000");
  expect(explore).toContain("deficit $-0.2000");
  expect(explore).toContain("0 unpriced job(s)");
  expect(explore).toContain("no-eligible-work: the topic has no sessions");
  expect(allocation?.textContent).toContain("not hard spend ceilings");
});

test("an ETA past the deadline says so, because that is the operator's cue to act", async () => {
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_slow",
          target: {
            costMicros: 5_000_000,
            deadline: new Date(Date.now() + 10 * 60_000).toISOString(),
          },
          etaAt: new Date(Date.now() + 90 * 60_000).toISOString(),
        }),
      ],
    }),
  });
  expect(stat(root, "ETA")).toContain("after the deadline");
});

test("a review-only allocation displays review while stopping at the canonical drain operation", async () => {
  const { root, fake } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_live",
          preset: "read-whats-new",
          allocation: [
            {
              preset: "review-backlog",
              weight: 1,
              share: 1,
              incurredCostMicros: 50_000,
              reservedCostMicros: 100_000,
              deficitCostMicros: 0,
              unpricedJobs: 0,
              gap: "",
            },
          ],
        }),
      ],
    }),
  });
  expect(root.querySelector(DRAIN)?.textContent).toContain("Review backlog on m-dev-01");
  expect(root.querySelector(DRAIN)?.textContent).not.toContain("Read what's new");
  await click(root.querySelector(STOP));
  await settle();
  const calls = fake.callsTo(ACTIONS.drainStop);
  expect(calls).toHaveLength(1);
  expect(calls[0]?.args).toEqual({
    drainId: "drn_live",
    reason: "",
    operation: { kind: "operation", machineId: "m-dev-01", operationId: OPERATIONS.explore },
  });
  // The door's own `cancelled` count, not an assumption: two of the two jobs it was holding.
  expect(root.textContent).toContain("2 of 2 in-flight job(s) cancelled");
});

test("a stop the hub refused says so, with the job it could not cancel and the state it reached", async () => {
  /*
    A PROGRESS CLAIM CARRIES ITS NUMBER (runbook §11.6, rule 2). The door answers `cancelled` and
    a `note` precisely because a stop can be refused — a settlement's tick holds no `jobs:cancel`,
    and the hub says so by name — and the panel that used to discard both told the operator "its
    jobs are cancelled" over a drain whose jobs were all still running.
  */
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({ drains: [drainStatus({ drainId: "drn_live", jobsLive: 2 })] }),
    drainStop: () => ({
      drainId: "drn_live",
      state: "closing" as const,
      cancelled: 0,
      note: "job_drn_live_1 was not cancelled: jobs:cancel capability required at target",
    }),
  });
  await click(root.querySelector(STOP));
  await settle();
  expect(root.textContent).toContain("0 of 2 in-flight job(s) cancelled");
  expect(root.textContent).toContain("it ends when their receipts land");
  expect(root.textContent).toContain("job_drn_live_1 was not cancelled");
  expect(root.textContent).not.toContain("its jobs are cancelled");
});

test("a closing drain is the one whose last jobs an operator can still cancel", async () => {
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_closing",
          state: "closing",
          jobsLive: 1,
          reason: "the target of 5000000 micro-dollars is met at 5100000",
          etaAt: "",
        }),
      ],
    }),
  });
  const strip = root.querySelector(DRAIN);
  expect(strip?.textContent).toContain("closing");
  expect(strip?.textContent).toContain("folding what its last 1 job(s) spend");
  // It has stopped launching, so the button is about its stragglers rather than about the drain.
  expect(root.querySelector(STOP)?.textContent).toBe("Cancel its last jobs");
});

test("an ended drain says how it ended and is not offered a stop", async () => {
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_done",
          state: "target",
          reason: "the target of 5000000 micro-dollars is met at 5100000",
          finishedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
          etaAt: "",
        }),
      ],
    }),
  });
  expect(root.querySelector(STOP)).toBeNull();
  expect(root.querySelector(DRAIN)?.textContent).toContain("the target of 5000000 micro-dollars");
  // A drain that has ended has no ETA: what it spent is what it spent.
  expect(stat(root, "ETA")).toBe("—");
});

test("the last drain's report is on the screen beside the drain that left it", async () => {
  /*
    WHAT THE PANEL COULD NOT SAY ON 2026-09-13 (#270). While a drain ran the screen answered the
    go/no-go rule; the moment it stopped it answered nothing, and the operator's questions —
    what did it cost, on whose account, against which duties, how much erroring, how much came
    out — were reconstructed by hand from receipts and `/proc` hours later. The door carries the
    record the drain left on the newest ended drain; this is that record, rendered.
  */
  const { root } = await open({
    runs: () => runsResult([]),
    drainStatus: () => ({
      drains: [
        drainStatus({
          drainId: "drn_done",
          state: "target",
          reason: "the target of 5000000 micro-dollars is met at 5100000",
          finishedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
          etaAt: "",
          report: {
            schema: "babel.drain-report/1",
            provenance: "drain",
            drainId: "drn_done",
            machineId: "m-dev-01",
            preset: "read-whats-new",
            ending: "target",
            reason: "the target of 5000000 micro-dollars is met at 5100000",
            startedBy: "operator",
            startedAt: new Date(Date.now() - 134 * 60_000).toISOString(),
            finishedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
            wallMs: 129 * 60_000,
            concurrent: 12,
            target: { costMicros: 5_000_000 },
            account: "ctr_workbench: the-drain-account (as Code reported at start)",
            model: "anthropic/claude-sonnet-4-5",
            thinking: "high",
            presetAllocation: [
              {
                preset: "review-backlog",
                weight: 3,
                share: 0.75,
                incurredCostMicros: 3_900_000,
                reservedCostMicros: 0,
                deficitCostMicros: -75_000,
                unpricedJobs: 0,
                gap: "",
              },
              {
                preset: "explore-topic",
                weight: 1,
                share: 0.25,
                incurredCostMicros: 1_200_000,
                reservedCostMicros: 0,
                deficitCostMicros: 75_000,
                unpricedJobs: 0,
                gap: "",
              },
            ],
            allocation: {
              named: ["code-health", "time-and-spend", "performance"],
              ran: [
                {
                  name: "code-health",
                  runs: 70,
                  tokens: {
                    calls: 210,
                    inputTokens: 5_700_000,
                    outputTokens: 402_000,
                    cacheReadTokens: 19_200_000,
                    costMicros: 5_100_000,
                  },
                },
                {
                  name: "time-and-spend",
                  runs: 70,
                  tokens: {
                    calls: 210,
                    inputTokens: 5_700_000,
                    outputTokens: 402_000,
                    cacheReadTokens: 19_200_000,
                    costMicros: 5_100_000,
                  },
                },
              ],
              shared: true,
            },
            accounts: [
              {
                name: "ctr_workbench: the-drain-account (as Code reported at start)",
                runs: 70,
                tokens: {
                  calls: 210,
                  inputTokens: 5_700_000,
                  outputTokens: 402_000,
                  cacheReadTokens: 19_200_000,
                  costMicros: 5_100_000,
                },
              },
            ],
            tokens: {
              calls: 210,
              inputTokens: 5_700_000,
              outputTokens: 402_000,
              cacheReadTokens: 19_200_000,
              costMicros: 5_100_000,
            },
            jobs: {
              launched: 70,
              reachedModel: 70,
              settled: 68,
              unsettled: 2,
              withoutRunRow: 0,
            },
            closures: { completed: 48, failed: 20 },
            refusals: { "unknown-reference": 20 },
            launchRefusals: { concurrency_limit: 6 },
            produced: {
              records: 50,
              assessments: 50,
              recordsPerMillionTokens: 8.2,
              assessmentsPerMillionTokens: 8.2,
            },
            load: {
              heldMs: 90 * 60_000,
              atModelMs: 13 * 60_000,
              atModelFraction: 0.097,
              peakHeld: 12,
              peakAtModel: 6,
            },
            pipeline: {
              prepareRuns: 70,
              prepareWallMs: 118 * 60_000,
              sessionRuns: 70,
              sessionWallMs: 13 * 60_000,
            },
            gaps: [
              {
                reason: "refused:unknown-reference",
                jobs: 20,
                detail: "unknown-reference: a citation the material never served",
              },
            ],
            notes: [
              {
                at: new Date(Date.now() - 60 * 60_000).toISOString(),
                kind: "stall",
                detail: "6 of 12 job(s) are at the model with nothing metered for 90s",
              },
            ],
            notesDropped: 3,
            unobserved: ["the machine's CPU load and memory over the drain's life: not readable"],
          },
        }),
      ],
    }),
  });
  const strip = root.querySelector(DRAIN);
  if (strip === null) throw new Error("the ended drain is not on the screen");
  const text = strip.textContent ?? "";

  // What it cost, and on whose account.
  expect(text).toContain("402,000 out");
  expect(text).toContain("19,200,000 cache read");
  expect(text).toContain("$5.1000 on ctr_workbench: the-drain-account");
  // How much erroring, in the two lanes that are different questions: paid work with no result,
  // and work that never became a job at all.
  expect(text).toContain("refused:unknown-reference");
  expect(text).toContain("never launched concurrency_limit");
  // How much value came out, per million tokens.
  expect(text).toContain("50 records and 50 assessments");
  expect(text).toContain("8.2 records");
  // Where the wall time went: the 13-of-134-minutes reading, from the panel rather than /proc.
  expect(text).toContain("at the model 9.7%");
  expect(text).toContain("preparing 1h 58m against 13m 00s in session");
  // A duty the operator named and no run carried: the allocation gap, said as one.
  expect(text).toContain("never ran");
  // The controller's own notes, and what it could not see at all.
  expect(text).toContain("1 note(s) the controller made, 3 dropped");
  expect(text).toContain("what this report cannot answer");
  const report = strip.querySelector(".plugin-atyrode_babel_watch__drain-report");
  const allocation = report?.querySelector(ALLOCATION);
  const reviewAllocation = allocation?.querySelector("[data-preset='review-backlog']");
  const exploreAllocation = allocation?.querySelector("[data-preset='explore-topic']");
  expect(reviewAllocation?.textContent).toContain("incurred $3.9000");
  expect(exploreAllocation?.textContent).toContain("incurred $1.2000");
  const recipes = report?.querySelector("[data-field='drain-recipe-participation']");
  expect(recipes?.textContent).toContain("code-health 70 runs · $5.1000");
  expect(recipes?.textContent).toContain("time-and-spend 70 runs · $5.1000");
  expect(recipes?.textContent).toContain("Do not add them together");
  expect(allocation?.textContent).not.toContain("code-health");
  expect(text).not.toContain("$10.2000");
});
