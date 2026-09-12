import type { HostServices } from "@manifold/plugin";
import type { ActionOutcome, MachineSummary } from "@manifold/protocol";
import { ACTIONS, door, type ActionName } from "../../contract.ts";
import type { LaunchAnswer, PolicyResult, RunRow, RunsResult, TopicsResult } from "../api.ts";

/*
  THE FAKE HOST.

  A panel's whole world is `HostServices`: the doors it knocks on and the machines the hub knows.
  This is that world made of two closures — a door table and a machine list — plus a log of every
  call, which is what lets a test assert WHAT was posted rather than merely that something was.
  A doorman that throws is a refused dispatch, because that is how the real door answers: a
  denial is data on the wire, and the panel must show its sentence rather than break.

  Only the two members this panel uses are real. The rest of `HostServices` is a workspace shell's
  business (viewports, authoring, placement, terminals), and a fake that implemented them would be
  a second engine nobody reads.
*/

export interface DoorCall {
  readonly name: string;
  readonly args: unknown;
}

/** One door's answer. Returning a value is `ok`; throwing is the host's refusal sentence. */
export type Doorman = (args: unknown) => unknown;

export interface FakeHost {
  readonly host: HostServices;
  readonly calls: readonly DoorCall[];
  /** Every call to one door, in order, so a test can read the last request's arguments. */
  callsTo(action: ActionName): readonly DoorCall[];
}

export function fakeHost(doors: Readonly<Record<string, Doorman>>, machines: readonly MachineSummary[]): FakeHost {
  const calls: DoorCall[] = [];
  const client = {
    async action(name: string, args: unknown): Promise<ActionOutcome> {
      calls.push({ name, args });
      const doorman = doors[name];
      if (doorman === undefined) {
        return { ok: false, denial: { rule: "unknown_action", message: `${name} has no fake` } };
      }
      try {
        return { ok: true, result: doorman(args) };
      } catch (error) {
        return {
          ok: false,
          denial: { rule: "refused", message: error instanceof Error ? error.message : String(error) },
        };
      }
    },
    async machines(): Promise<readonly MachineSummary[]> {
      return machines;
    },
  };
  // Two of `HostServices`' members, which are the two this panel reaches for; see the note above.
  const host = { client, principal: { id: "operator" } } as unknown as HostServices;
  return {
    host,
    calls,
    callsTo: (action) => calls.filter((call) => call.name === door(action)),
  };
}

// ---------------------------------------------------------------------------- fixtures

export const MACHINES: readonly MachineSummary[] = [
  { id: "m-dev-01", name: "dev-01", online: true },
  { id: "m-old", name: "retired-box", online: false },
];

export function runRow(row: Partial<RunRow> & Pick<RunRow, "id" | "state" | "startedAt" | "lastWord">): RunRow {
  return {
    kind: "explore",
    machineId: "m-dev-01",
    jobId: "job_1",
    recipe: "code-health-comprehensibility",
    finishedAt: "",
    costUsd: null,
    records: 0,
    freshness: "fresh",
    ...row,
  };
}

export function runsResult(runs: readonly RunRow[], total?: number): RunsResult {
  return { runs: [...runs], total: total ?? runs.length };
}

export const POLICY: PolicyResult = {
  version: "pol_7",
  seq: 7,
  recordedAt: "2026-09-12T09:00:00.000Z",
  actorId: "operator",
  reason: "raised the day's ceiling",
  ceilings: { perRunUsd: 2, perDayUsd: 20, concurrent: 3 },
  spentTodayUsd: 4.5,
  lanes: [
    { lane: "coverage", role: "evidence-checker", share: 0.4 },
    { lane: "exploration", role: "", share: 0.35 },
  ],
  recipes: [
    {
      id: "code-health-comprehensibility",
      title: "Code health: comprehensibility",
      looksFor: "Where the code is hard to read, and what that cost.",
      enabled: true,
      lastRanAt: "2026-09-12T08:00:00.000Z",
      lastRunId: "run_9",
      runs: 42,
    },
    {
      id: "babel-tunes-itself",
      title: "",
      looksFor: "",
      enabled: false,
      lastRanAt: "",
      lastRunId: "",
      runs: 0,
    },
  ],
  payload: { batch_size: 3 },
};

export const TOPICS: TopicsResult = {
  topics: [
    {
      id: "ent_1a2b3c4d",
      name: "babel",
      kind: "repository",
      binding: null,
      posts: 128,
      awaiting: 4,
      latestAt: "2026-09-12T08:30:00.000Z",
      interest: { state: "working", reason: "", at: "2026-09-01T00:00:00.000Z", by: "operator" },
    },
    {
      id: "ent_5e6f7a8b",
      name: "manifold",
      kind: "repository",
      binding: null,
      posts: 61,
      awaiting: 0,
      latestAt: "2026-09-11T20:00:00.000Z",
      interest: { state: "watching", reason: "", at: "2026-09-01T00:00:00.000Z", by: "operator" },
    },
  ],
  proposed: [],
  unfiled: 12,
};

/** What the machine's own runtime report becomes on the way back through `launch`. */
export function launchAnswer(overrides: Partial<LaunchAnswer> = {}): LaunchAnswer {
  return {
    runId: "run_new",
    jobId: "job_new",
    machineId: "m-dev-01",
    kind: "explore",
    profile: {
      id: "babel-explore",
      revision: 4,
      model: "claude-opus-4",
      disclosure: "full",
      costPer1k: { input: 0.015, output: 0.075 },
    },
    ceiling: { perRunUsd: 2, perDayUsd: 20 },
    ...overrides,
  };
}

/** The door table a Watch test mounts against; override one door to make it refuse. */
export function watchDoors(answers: {
  readonly runs: () => RunsResult;
  readonly policy?: () => PolicyResult;
  readonly topics?: () => TopicsResult;
  readonly launch?: (args: unknown) => LaunchAnswer;
  readonly stop?: (args: unknown) => unknown;
}): Record<string, Doorman> {
  return {
    [door(ACTIONS.runs)]: () => answers.runs(),
    [door(ACTIONS.policy)]: () => (answers.policy ?? (() => POLICY))(),
    [door(ACTIONS.topics)]: () => (answers.topics ?? (() => TOPICS))(),
    [door(ACTIONS.launch)]: (args) => (answers.launch ?? (() => launchAnswer()))(args),
    [door(ACTIONS.stop)]: (args) => (answers.stop ?? (() => ({ asked: true })))(args),
  };
}
