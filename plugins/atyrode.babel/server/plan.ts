import type { MachineHalf, PluginManifest } from "@manifold/protocol";
import type { GuestCtx, GuestHookJobs } from "@manifold/plugin-kit/server";
import { BABEL_PLUGIN_ID, OPERATIONS, type ModelPrice, type OperationName } from "../contract.ts";
import type { Policy } from "../store/coordinator.ts";
import type {
  Awaitable,
  JobInferenceLimits,
  JobLaunch,
  JobLimits,
  JobRef,
  JobRunState,
  JobsSlice,
  MachineReadiness,
  MachinesSlice,
  OutputRef,
  Recipe,
  RepositoryOutcome,
  RunPlan,
  ScheduleRow,
  ScheduleTiming,
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

/**
 * THE OPERATOR'S CEILING, IN THE UNITS THE OWNER ENFORCES IT IN (ADR 0038).
 *
 * The policy states an allowance in dollars; `limits.inference.costMicros` is integer
 * micro-dollars, and the machine owner refuses the call that would pass it. Rounding is up so a
 * ceiling is never quietly tightened by a fraction of a micro-dollar, and a non-positive
 * allowance yields NO ceiling rather than a zero one — a zero would refuse the first call, and
 * "the operator set no ceiling" is a different statement from "the operator allowed nothing".
 */
export function inferenceCeiling(policy: Policy): JobInferenceLimits | null {
  const allowance = perRunUsd(policy);
  if (!Number.isFinite(allowance) || allowance <= 0) return null;
  return { costMicros: Math.ceil(allowance * 1_000_000) };
}

/**
 * WHICH OPERATIONS CARRY AN INFERENCE CEILING: the two that bind the inference service and
 * drive a model. A ceiling on `scan` would be a bound on calls it cannot make, and the honest
 * shape of that is no ceiling at all.
 */
const METERED_OPERATIONS: Record<string, true> = {
  [OPERATIONS.explore]: true,
  [OPERATIONS.evaluate]: true,
};

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
  const operationId = request.operationId ?? OPERATIONS.evaluate;
  const ceiling = METERED_OPERATIONS[operationId] === true ? inferenceCeiling(request.policy) : null;
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
    limits: {
      ...operationLimits(machine, operationId),
      ...(ceiling === null ? {} : { inference: ceiling }),
    },
  };
}

// ---------------------------------------------------------------------------- the jobs slice

/**
 * The loop's eight verbs and the one only the operator's own stop needs. `cancel` is not the
 * loop's business — a cycle never stops a job it started — so it is here rather than in
 * `JobsSlice`, and a fake that drives the loop does not have to implement it.
 */
export interface BabelJobs extends JobsSlice {
  cancel(node: JobRef): Awaitable<void>;
}

/**
 * The host's request owns its arrays; the loop's are readonly. One copy per job keeps both
 * honest rather than casting the promise away, and both verbs that send a request want it.
 */
function owned(outputs: JobLaunch["outputs"]): {
  name: string;
  locationId: string;
  components: string[];
}[] {
  return outputs.map((output) => ({
    name: output.name,
    locationId: output.locationId,
    components: [...output.components],
  }));
}

/**
 * `ctx.jobs`, narrowed to the verbs this plugin uses.
 *
 * ALL EIGHT CROSS THE BOUNDARY (#534). `ISOLATE_CTX_METHODS` serves `jobs.schedule`,
 * `jobs.schedules` and `jobs.disableSchedule` alongside the five a dispatch always had, and
 * `GuestHookJobs` — every job verb but the live subscription — declares them, so a hardened
 * server half registers its OWN beat instead of recording that it cannot. The hole this file
 * used to state is closed, and with it the sentence the loop carried a refusal in.
 *
 * Everything is passed straight through: the protocol's own shapes already satisfy the loop's,
 * which is why this is a narrowing and not a translation. `schedules()` answers
 * `PublicJobSchedule` rows, which are `ScheduleRow`s carrying the plugin id and the pinned
 * artifact as well — more than the loop reads, never less.
 */
export function jobsSlice(jobs: GuestHookJobs): BabelJobs {
  return {
    describe: async (args): Promise<MachineReadiness> =>
      await jobs.describe({ machineId: args.machineId, pluginId: args.pluginId }),
    execute: async (args: JobLaunch): Promise<JobRunState> =>
      await jobs.execute({ ...args, outputs: owned(args.outputs) }),
    status: async (node: JobRef): Promise<JobRunState> => await jobs.status(node),
    listRuns: async (args) => await jobs.listRuns(args),
    output: async (args: { node: OutputRef; offset: number; maxBytes: number }) =>
      await jobs.output(args),
    cancel: async (node: JobRef): Promise<void> => {
      await jobs.cancel(node);
    },
    schedule: async (args: JobLaunch & ScheduleTiming): Promise<unknown> =>
      await jobs.schedule({ ...args, outputs: owned(args.outputs) }),
    schedules: async (): Promise<readonly ScheduleRow[]> => await jobs.schedules(),
    disableSchedule: async (args: { scheduleId: string; revision: string }): Promise<unknown> =>
      await jobs.disableSchedule(args),
  };
}

/**
 * WHAT A HOOK WHOSE INSTALLER IS GONE HAS INSTEAD OF JOBS.
 *
 * `GuestLifecycleCtx.jobs` is present exactly when the `hook` frame said the host serves it —
 * the installer's credential restored (#514, #534) — so an enable that owns a cadence registers
 * it there, and one whose installer has been revoked sees `undefined` rather than a handle
 * whose every call refuses. This is what the cycle reaches through in that second case: every
 * verb refuses with the same sentence, which the loop records as a note and works around. It
 * does the store's half — reconciling what the policy says, and reporting the machines it
 * cannot ask — and asks nothing of a host that has not offered.
 */
export const ENABLE_WITHOUT_JOBS =
  "this hook is served no job slice: GuestLifecycleCtx carries jobs only while the installer's " +
  "credential can be restored, and this one's could not";

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

// ---------------------------------------------------------------------------- the machines slice

/**
 * What the in-realm host hands a bundle it imported: the machine gateway's own admission, whose
 * verb takes the machine and the path as two arguments (`MachineAdmission` in
 * `server/src/plugin-host.ts`).
 */
interface PositionalMachines {
  repository(machineId: string, path: string): Awaitable<RepositoryOutcome>;
}

/**
 * `ctx.machines`, narrowed to the one verb this plugin asks: what one folder on one enrolled
 * machine IS (#535). The answer is the host's own — an `ok: false` is the hub saying nobody
 * could be asked, never a fact about a disk — so this is a narrowing and not a translation.
 *
 * TWO HOSTS SERVE THAT VERB WITH TWO SIGNATURES, and a half that runs under both has to bridge
 * them. A HARDENED half is served the kit's handle, which takes the query as ONE OBJECT because
 * that is what crosses the ipc frame (`GuestCtx.machines.repository(query)`). A bundle the host
 * imported into its own realm — the DEFAULT for an installed server half (`plugin-host.ts`
 * `loadBundle`: a plain module import, and only the installer's consent selects a child) — is
 * handed the machine gateway's admission straight, and its verb is `(machineId, path)`.
 *
 * The declared arity is the difference and is what selects here, because the alternative is
 * silent: calling the gateway with a query object hands it an object where a machine id goes,
 * and every folder on every machine answers "not connected" for ever. A wrong answer nobody can
 * see is worse than either signature.
 */
export function machinesSlice(machines: GuestCtx["machines"]): MachinesSlice {
  if (machines.repository.length >= 2) {
    // The in-realm admission, which this module cannot name in its types: the kit types
    // `ctx.machines` as the hardened handle, and the host serves its own class to an import.
    const positional = machines as unknown as PositionalMachines;
    return {
      repository: async (machineId: string, path: string): Promise<RepositoryOutcome> =>
        await positional.repository(machineId, path),
    };
  }
  return {
    repository: async (machineId: string, path: string): Promise<RepositoryOutcome> =>
      await machines.repository({ machineId, path }),
  };
}

/**
 * WHAT A HOOK HAS INSTEAD OF MACHINES.
 *
 * A hook's context is not a dispatch's: `GuestLifecycleCtx` carries storage, the database, the
 * job slice its installer's credential restored — and no machines member at all. The isolate
 * proxy says the same thing from the other side (`serveCtxCall`: a `hook` or `settled` frame
 * serves `jobs.*` and answers `slice_unavailable` to everything else), so a cycle a settlement
 * woke cannot ask what a folder is. It reaches through this instead, which answers the one
 * refusal the outcome type already has a place for — the loop notes it once per machine and
 * asks again on the next cycle a dispatch wakes.
 */
export const HOOK_WITHOUT_MACHINES =
  "a lifecycle hook is served no machines slice: GuestLifecycleCtx carries storage, the " +
  "database and the installer's jobs, and machines.repository is a dispatch's to ask";

export function unaskable(reason: string): MachinesSlice {
  return { repository: (): RepositoryOutcome => ({ ok: false, reason }) };
}

// ---------------------------------------------------------------------------- the services slice

/**
 * ONE OWNER-INSTALLED SERVICE POLICY, narrowed to what a preview reads (ADR 0038): the prices
 * the owner will meter a model call at. Everything else in a policy — the origin, the
 * credential reference, the routes — is the owner's business and none of Babel's.
 */
export interface ServicePrices {
  readonly default?: ModelPrice | undefined;
  readonly models: Readonly<Record<string, ModelPrice>>;
}

/**
 * WHAT THE MACHINE'S SERVICE CONFIGURATION SAYS ABOUT ONE SERVICE. Three answers, because an
 * operator acts differently on each: the policy is installed (`policy`), the configuration was
 * readable and the policy is not in it (`policy: null`), or nobody could be asked (`ok: false`).
 * The third is NOT the second — `services.readConfiguration` is admitted only to a root caller
 * holding `services:configure` at the machine, so an ordinary dispatch is refused it — and a
 * preview that reported "not installed" because it was not allowed to look would be telling the
 * operator to install a policy that is already there.
 */
export type ServicePolicyOutcome =
  | { readonly ok: true; readonly policy: { readonly prices?: ServicePrices | undefined } | null }
  | { readonly ok: false; readonly reason: string };

/** The one service question this plugin asks: what the owner installed under an id. */
export interface ServicesSlice {
  policy(machineId: string, serviceId: string): Awaitable<ServicePolicyOutcome>;
}

/**
 * `ctx.services`, narrowed to that question. The refusal is kept rather than thrown because it
 * is an ordinary answer here: a dispatch under the operator's own credential is not entitled to
 * read a machine's service configuration, and the preview says what it could not see instead of
 * failing the door the operator opened to look at something else.
 */
export function servicesSlice(services: GuestCtx["services"]): ServicesSlice {
  return {
    policy: async (machineId: string, serviceId: string): Promise<ServicePolicyOutcome> => {
      try {
        const read = await services.readConfiguration({ machineId });
        const found = read.configuration.policies.find((entry) => entry.serviceId === serviceId);
        return { ok: true, policy: found === undefined ? null : { prices: found.prices } };
      } catch (error) {
        return { ok: false, reason: error instanceof Error ? error.message : String(error) };
      }
    },
  };
}

/** What a caller that cannot ask has instead; the same shape, and a reason instead of a fact. */
export function unreadable(reason: string): ServicesSlice {
  return { policy: (): ServicePolicyOutcome => ({ ok: false, reason }) };
}
