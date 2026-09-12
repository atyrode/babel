import type { MachineHalf, PluginManifest } from "@manifold/protocol";
import type { GuestSettledJobs } from "@manifold/plugin-kit/server";
import { BABEL_PLUGIN_ID, OPERATIONS, type OperationName } from "../contract.ts";
import type { Policy } from "../store/coordinator.ts";
import type {
  Awaitable,
  JobLaunch,
  JobLimits,
  JobRef,
  JobRunState,
  JobsSlice,
  MachineReadiness,
  OutputRef,
  Recipe,
  RunPlan,
  ScheduleRow,
} from "./conductor.ts";

/*
  WHAT A RUN RUNS UNDER, and the slice it is asked for through.

  `RunPlan` is everything a drawn review needs that is neither store state nor a coordinator
  decision (conductor.ts): the engine to drive, the profile, the caps, the recipe each role
  performs, the containment demand and the job's limits. The loop takes it rather than reading
  it, so this file is the ONE place the plugin decides what it spends and what it runs.

  Three of those come from somewhere other than a constant here, and each says where:

  - THE ENGINE'S PATH IS THE MANIFEST'S. A machine operation names its runtime tools, and the
    owner binds each one at `/runtime/bin/<alias>` (agent/src/job-linux.ts). So the binary the
    explore and evaluate documents carry is `/runtime/bin/code` and nothing else, and it is
    ASSERTED against the manifest's own machine block: a manifest that stops declaring the tool
    fails here, at wiring, rather than as a job that cannot exec on a machine.
  - THE LIMITS ARE THE OPERATION'S OWN. `engine.jobs.execute` takes `args.limits ?? op.limits`
    and refuses any key above the operation's declared ceiling with `limit_exceeded`
    (packages/server/src/job-service.ts), so the plan carries the declared limits of the
    operation the loop launches and a machine block that changes them changes this with it.
  - THE PER-RUN CEILING IS THE POLICY'S. One review may spend what one claim reserves — the
    per-cycle allowance divided by the batch — because that is the number the coordinator
    actually holds against the day. A cap invented here would be a second spending limit beside
    the one an operator recorded.

  WHAT IS NOT HERE, and why. The cookbook: a recipe's BODY is what a run performs, and this
  deployment has none on the hub. `cookbook/recipes` in this repository is 273 KB of markdown
  against the 65,536-byte ceiling on a job's whole input record (`JobRequestSchema.input`), so
  the bodies cannot cross as an input field at all, and neither the store (no table) nor the
  policy (`PolicySchema` is strict) carries them. The plan therefore takes the cookbook it is
  given, and the door and the loop each refuse BY NAME when a role or a preset has no method to
  run — which is the honest state of a hub that holds no cookbook, rather than a run performing
  a method nobody wrote.
*/

// ---------------------------------------------------------------------------- the engine

/** Where the owner binds a runtime tool inside the job (`agent/src/job-linux.ts`). */
export const RUNTIME_BIN = "/runtime/bin";
/** The runtime tool that IS the engine: Code, driven over its own RPC by the machine half. */
export const ENGINE_TOOL = "code";
/** The engine's guest path, as the explore and evaluate input documents carry it. */
export const ENGINE_BINARY = `${RUNTIME_BIN}/${ENGINE_TOOL}`;

/**
 * The Code profile every Babel run asks for. It is a REFERENCE and never a model name: what is
 * behind `analysis@3` — the model, the disclosure class, the price per 1k — is Code's to resolve
 * and to report back, and Babel records what ran rather than what it asked for (plan §5).
 */
export const PROFILE = { id: "analysis", revision: 3 } as const;

/**
 * The caps that are not money. Measured on this deployment rather than chosen: 40 tool calls is
 * above every completed review here (the largest made 31); two minutes of silence is four
 * missed heartbeats, the same bound the runs strip calls `recent`; and thirty seconds is what a
 * cold engine took to answer its handshake on dev-01 under a warm page cache.
 */
export const TOOL_CALLS = 40;
export const IDLE_MS = 120_000;
export const HANDSHAKE_MS = 30_000;

/**
 * The limits a job runs under when the manifest declares no machine half at all — a bundle
 * packed without one, which the engine refuses to run an operation from anyway. They are the
 * evaluate operation's own numbers, so a plan built against a manifest in that state asks for
 * nothing larger than the one it would have got.
 */
export const DEFAULT_LIMITS: JobLimits = {
  timeoutMs: 3_600_000,
  memoryBytes: 2_147_483_648,
  processes: 64,
  outputBytes: 67_108_864,
};

// ---------------------------------------------------------------------------- the machine block

/**
 * The declared limits of one operation. An operation the manifest does not declare cannot run
 * on any machine, so the fallback is never a launch that succeeds under invented bounds: it is
 * the number a refused request would have been compared against.
 */
export function operationLimits(
  machine: MachineHalf | null,
  operationId: OperationName | string,
): JobLimits {
  return machine?.operations[operationId]?.limits ?? DEFAULT_LIMITS;
}

/**
 * Asserts the engine is on the machine this plugin ships. `runtimeTools` is what the owner
 * binds; an operation that drives Code and does not require it would exec a path that is not
 * there, and the job would fail on the machine with a message about a missing file rather than
 * here with one about the manifest.
 */
export function engineBinary(machine: MachineHalf | null): string {
  if (machine === null) return ENGINE_BINARY;
  const drives = [OPERATIONS.explore, OPERATIONS.evaluate];
  for (const operationId of drives) {
    const operation = machine.operations[operationId];
    if (operation === undefined) continue;
    if (!operation.runtimeTools.includes(ENGINE_TOOL)) {
      throw new Error(
        `${BABEL_PLUGIN_ID}: the ${operationId} operation does not require the ${ENGINE_TOOL} ` +
          `runtime tool, so ${ENGINE_BINARY} is not bound in its job`,
      );
    }
  }
  return ENGINE_BINARY;
}

// ---------------------------------------------------------------------------- the plan

export interface PlanRequest {
  readonly manifest: PluginManifest;
  /** The policy in force; its per-cycle allowance and batch are what one run may spend. */
  readonly policy: Policy;
  /** The cookbook this hub holds, by recipe id. Empty until one is installed. */
  readonly cookbook?: Readonly<Record<string, Recipe>> | undefined;
  /** Which recipe each review role performs, by role. A role named here needs a recipe above. */
  readonly roles?: Readonly<Record<string, string>> | undefined;
  /** The operation the plan's limits are for; the loop's draws are evaluate jobs. */
  readonly operationId?: OperationName | undefined;
}

/** What one review may spend: the per-cycle allowance divided by the batch it is granted in. */
export function perRunUsd(policy: Policy): number {
  return policy.batchSize <= 0 ? policy.perCycleCost : policy.perCycleCost / policy.batchSize;
}

export function runPlan(request: PlanRequest): RunPlan {
  const machine = request.manifest.machine ?? null;
  const cookbook = request.cookbook ?? {};
  const recipes: Record<string, Recipe> = {};
  for (const [role, recipeId] of Object.entries(request.roles ?? {})) {
    const recipe = cookbook[recipeId];
    // A role pointed at a recipe the cookbook does not hold is left UNSET rather than given an
    // empty body: the loop refuses such a role by name, and a body of nothing would be a run
    // asking a model to perform a method that says nothing.
    if (recipe !== undefined) recipes[role] = recipe;
  }
  return {
    engine: { binary: engineBinary(machine), args: [] },
    profile: { id: PROFILE.id, revision: PROFILE.revision },
    caps: {
      perRunUsd: perRunUsd(request.policy),
      toolCalls: TOOL_CALLS,
      idleMs: IDLE_MS,
      handshakeMs: HANDSHAKE_MS,
    },
    recipes,
    // Every Babel run is contained. The operator relaxes it per run and never by default: an
    // engine that reports no sandbox is refused by the machine half, which is the check.
    requireContainment: true,
    limits: operationLimits(machine, request.operationId ?? OPERATIONS.evaluate),
  };
}

// ---------------------------------------------------------------------------- the jobs slice

/**
 * WHAT A HARDENED CONTEXT CANNOT DO, stated once.
 *
 * The loop's slice has eight verbs; `ctx.jobs` across the isolate boundary serves five of them
 * and the three schedule verbs do not exist there at all. `ISOLATE_CTX_METHODS`
 * (packages/protocol/src/isolate.ts) lists `jobs.describe`, `execute`, `status`, `listRuns`,
 * `input`, `cancel`, `output`, `outputs`, `journal`, `follow`, `ack` and `unfollow` — and no
 * `jobs.schedule`, `jobs.schedules` or `jobs.disableSchedule` — while `GuestJobs`
 * (packages/plugin-kit/src/server.ts) declares members for exactly that list. In-realm
 * `PluginJobContext` has all three (packages/plugin/src/runtime.ts) and `docs/PLUGINS.md`
 * calls the hardened handles "their asynchronous bridge counterparts", so the absence is a
 * hole in the bridge rather than a decision about plugins.
 *
 * So a hardened half registers no schedule, and this says so rather than pretending:
 * `schedules()` answers the truth — this plugin has no schedule the host will admit to — and
 * `schedule`/`disableSchedule` refuse by name. The loop records that refusal as a note and
 * carries on ingesting and drawing, because a beat it cannot register is one wake it does not
 * get, not a reason to stop doing the work a dispatch woke it for.
 */
export const SCHEDULE_UNAVAILABLE =
  "jobs.schedule does not cross the isolate boundary: ISOLATE_CTX_METHODS serves no schedule verb, " +
  "so a hardened server half cannot register its beat";

/**
 * The loop's eight verbs and the one only the operator's own stop needs. `cancel` is not the
 * loop's business — a cycle never stops a job it started — so it is here rather than in
 * `JobsSlice`, and a fake that drives the loop does not have to implement it.
 */
export interface BabelJobs extends JobsSlice {
  cancel(node: JobRef): Awaitable<void>;
}

/**
 * `ctx.jobs`, narrowed to the verbs this plugin uses. Everything the boundary serves is passed
 * straight through — the protocol's own shapes already satisfy the loop's, which is why this is
 * a narrowing and not a translation.
 */
export function jobsSlice(jobs: GuestSettledJobs): BabelJobs {
  return {
    describe: async (args): Promise<MachineReadiness> =>
      await jobs.describe({ machineId: args.machineId, pluginId: args.pluginId }),
    // The host's request type owns its arrays; the loop's is readonly. One copy per job keeps
    // both honest rather than casting the promise away.
    execute: async (args: JobLaunch): Promise<JobRunState> =>
      await jobs.execute({
        ...args,
        outputs: args.outputs.map((output) => ({
          name: output.name,
          locationId: output.locationId,
          components: [...output.components],
        })),
      }),
    status: async (node: JobRef): Promise<JobRunState> => await jobs.status(node),
    listRuns: async (args) => await jobs.listRuns(args),
    output: async (args: { node: OutputRef; offset: number; maxBytes: number }) =>
      await jobs.output(args),
    cancel: async (node: JobRef): Promise<void> => {
      await jobs.cancel(node);
    },
    schedules: (): readonly ScheduleRow[] => [],
    schedule: () => {
      throw new Error(SCHEDULE_UNAVAILABLE);
    },
    disableSchedule: () => {
      throw new Error(SCHEDULE_UNAVAILABLE);
    },
  };
}

/**
 * WHAT AN ENABLE HOOK HAS INSTEAD OF JOBS.
 *
 * `GuestLifecycleCtx` carries a plugin's storage, its tables and `emit`, and no job slice at
 * all — in-realm `LifecycleCtx` carries none either, and `onJobSettled` is the only hook whose
 * context is widened with one (`JobSettledCtx`). So the cycle an enable runs has no machine it
 * may reach, and this is what it reaches through: every verb refuses with the same sentence,
 * which the loop records as a note and works around. It does the store's half — reconciling
 * what the policy says, and reporting the machines it cannot ask — and asks nothing of a host
 * that has not offered.
 */
export const ENABLE_WITHOUT_JOBS =
  "the enable hook is served no job slice: GuestLifecycleCtx carries storage and the database, " +
  "and only onJobSettled is widened with jobs";

export function unauthorized(reason: string): BabelJobs {
  const refuse = (): never => {
    throw new Error(reason);
  };
  return {
    describe: refuse,
    execute: refuse,
    status: refuse,
    listRuns: refuse,
    output: refuse,
    cancel: refuse,
    schedules: refuse,
    schedule: refuse,
    disableSchedule: refuse,
  };
}
