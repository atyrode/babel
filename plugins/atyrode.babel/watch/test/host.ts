import type { HostServices } from "@manifold/plugin";
import type { ActionOutcome, MachineSummary } from "@manifold/protocol";
import { ACTIONS, OPERATIONS, door, type ActionName } from "../../contract.ts";
import type {
  AccountsResult,
  DrainStatus,
  LaunchAnswer,
  PolicyResult,
  RunProgress,
  RunRow,
  RunsResult,
  TopicsResult,
} from "../api.ts";

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
    kind: OPERATIONS.explore,
    machineId: "m-dev-01",
    jobId: "job_1",
    recipe: "code-health-comprehensibility",
    finishedAt: "",
    costUsd: null,
    tokens: null,
    calls: null,
    records: 0,
    freshness: "fresh",
    progress: null,
    ...row,
  };
}

/** What the conductor folded out of a running job's replay ring, as a row carries it. */
export function runProgress(over: Partial<RunProgress> & Pick<RunProgress, "stage" | "since">): RunProgress {
  return {
    message: "",
    fraction: null,
    calls: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheTokens: 0,
    costUsd: 0,
    lastModel: "",
    stalled: false,
    updatedAt: over.since,
    ...over,
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
  overlay: null,
  payload: { batchSize: 3 },
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

/**
 * WHAT THE MACHINE'S BROKER HAS OBSERVED, as the `accounts` door projects it: one account to
 * spend and one the broker reports blocked, because "offered" and "spendable" are two facts and
 * the picker shows both.
 */
export const BROKER_SCOPE = "atyrode.omp.accounts.broker@rev_4/m-dev-01";

export const ACCOUNTS: AccountsResult = {
  accounts: [
    {
      provider: "anthropic",
      scope: BROKER_SCOPE,
      credentialId: "7",
      identityKey: "victorballu",
      label: "victorballu@gmail.com",
      disabled: false,
    },
    {
      provider: "anthropic",
      scope: BROKER_SCOPE,
      credentialId: "9",
      identityKey: "helena",
      label: "helena@example.com",
      disabled: true,
    },
  ],
  unavailable: "",
};

/** What the machine's own runtime report becomes on the way back through `launch`. */
export function launchAnswer(overrides: Partial<LaunchAnswer> = {}): LaunchAnswer {
  return {
    runId: "run_new",
    jobId: "job_new",
    machineId: "m-dev-01",
    kind: "explore",
    profile: { model: "anthropic/claude-opus-5", thinking: "high", account: "victorballu" },
    ceiling: { perRunUsd: 2, perDayUsd: 20 },
    session: {
      serviceId: "atyrode.babel.inference",
      account: "victorballu",
      model: "anthropic/claude-opus-5",
      priced: true,
      price: { inputPerMillion: 5_000_000, outputPerMillion: 25_000_000 },
      ceilingMicros: 2_000_000,
      policy: "priced",
      unreadable: "",
      note:
        "anthropic/claude-opus-5 is metered at $5.0000 per million input tokens and $25.0000 " +
        "per million output, on victorballu, under a ceiling of $2.0000 for this run",
    },
    ...overrides,
  };
}

/**
 * One drain as the status door answers for it (#258). The defaults are a drain that has just
 * started and said nothing yet, so a test names only the figures it is about.
 */
export function drainStatus(over: Partial<DrainStatus> = {}): DrainStatus {
  return {
    drainId: "drn_live",
    machineId: "m-dev-01",
    preset: "read-whats-new",
    state: "running",
    reason: "",
    startedAt: new Date(Date.now() - 60_000).toISOString(),
    startedBy: "operator",
    finishedAt: "",
    concurrent: 2,
    target: { costMicros: 5_000_000 },
    account: "the-drain-account",
    model: "anthropic/claude-sonnet-4-5",
    jobsLaunched: 2,
    jobsSettled: 0,
    jobsLive: 2,
    jobsAtModel: 0,
    jobsStalled: 0,
    spent: { calls: 0, inputTokens: 0, outputTokens: 0, costMicros: 0 },
    settled: { calls: 0, inputTokens: 0, outputTokens: 0, costMicros: 0 },
    outputTokensPerMinute: 0,
    costMicrosPerMinute: 0,
    etaAt: "",
    refusals: {},
    closures: {},
    ...over,
  };
}

/** The door table a Watch test mounts against; override one door to make it refuse. */
export function watchDoors(answers: {
  readonly runs: () => RunsResult;
  readonly policy?: () => PolicyResult;
  readonly topics?: () => TopicsResult;
  /** What the machine's broker has seen, or the reason nobody could be asked (#279). */
  readonly accounts?: (args: unknown) => AccountsResult;
  /** The dry read the card polls; `launch` below is only ever the button. */
  readonly launchPreview?: (args: unknown) => LaunchAnswer;
  readonly launch?: (args: unknown) => LaunchAnswer;
  readonly stop?: (args: unknown) => unknown;
  /** What is draining; the panel polls this one every five seconds like the runs feed. */
  readonly drainStatus?: (args: unknown) => { readonly drains: readonly DrainStatus[] };
  readonly drainStart?: (args: unknown) => unknown;
  readonly drainStop?: (args: unknown) => unknown;
}): Record<string, Doorman> {
  return {
    [door(ACTIONS.runs)]: () => answers.runs(),
    [door(ACTIONS.policy)]: () => (answers.policy ?? (() => POLICY))(),
    [door(ACTIONS.topics)]: () => (answers.topics ?? (() => TOPICS))(),
    [door(ACTIONS.accounts)]: (args) => (answers.accounts ?? (() => ACCOUNTS))(args),
    [door(ACTIONS.launchPreview)]: (args) =>
      (answers.launchPreview ?? (() => launchAnswer({ runId: "", jobId: "" })))(args),
    [door(ACTIONS.launch)]: (args) => (answers.launch ?? (() => launchAnswer()))(args),
    [door(ACTIONS.stop)]: (args) => (answers.stop ?? (() => ({ asked: true })))(args),
    [door(ACTIONS.drainStatus)]: (args) =>
      (answers.drainStatus ?? (() => ({ drains: [] })))(args),
    [door(ACTIONS.drainStart)]: (args) =>
      (
        answers.drainStart ??
        (() => ({
          drainId: "drn_started",
          machineId: "m-dev-01",
          preset: "read-whats-new" as const,
          concurrent: 4,
          launched: 4,
          deadline: new Date(Date.now() + 2 * 60 * 60_000).toISOString(),
          account: "the-drain-account",
          model: "anthropic/claude-sonnet-4-5",
          note: "",
        }))
      )(args),
    // The stop's own result shape: a panel that could not read `cancelled` and `note` would have
    // to assume what happened, which is what #285's review found it saying.
    [door(ACTIONS.drainStop)]: (args) =>
      (
        answers.drainStop ??
        (() => ({ drainId: "drn_live", state: "stopped" as const, cancelled: 2, note: "" }))
      )(args),
  };
}
