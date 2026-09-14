import { jobLimits, type MachineHalf, type PluginManifest } from "@manifold/protocol";
import type { GuestCtx, GuestHookJobs } from "@manifold/plugin-kit/server";
import { BABEL_PLUGIN_ID, OPERATIONS, type OperationName } from "../contract.ts";
import { DEFAULT_POLICY, type Policy } from "../store/coordinator.ts";
import type {
  Awaitable,
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
 * The declared limits of one operation, as a REQUEST may carry them. An operation the manifest
 * does not declare cannot run on any machine, so the fallback is never a launch that succeeds
 * under invented bounds: it is the number a refused request would have been compared against.
 *
 * `jobLimits` drops the machine-only half — `concurrentJobs`, which bounds how many of this
 * operation's jobs one host runs at once. That ceiling belongs to the manifest and never to a
 * request (`packages/protocol/src/jobs.ts`), and `JobExecuteArgsSchema.limits` is strict: a
 * plan that passed the declaration through verbatim would have every posting refused for an
 * unrecognised key rather than run under it.
 */
export function operationLimits(
  machine: MachineHalf | null,
  operationId: OperationName | string,
): JobLimits {
  const declared = machine?.operations[operationId]?.limits;
  return declared === undefined ? DEFAULT_LIMITS : jobLimits(declared);
}

/**
 * THE CEILING THE MACHINE HALF WILL ACTUALLY RUN: `limits.concurrentJobs` on the two operations
 * this plugin launches. The hub enforces it at `execute` and refuses the rest
 * `concurrency_limit` (`packages/server/src/job-service.ts`), so it is the hard bound the
 * coordinator governs inside — a per-machine bound above it would admit draws whose postings
 * the hub refuses, and a refused posting costs its reservation and produces no review.
 *
 * The LOWER of the two, because one number governs both lanes and a bound honoured by explore
 * but not by evaluate is not a bound. A manifest that declares neither is one no operation runs
 * from at all; the batch a policy is written with stands in, as `DEFAULT_LIMITS` does above.
 */
export function jobCeiling(manifest: PluginManifest): number {
  const operations = manifest.machine?.operations;
  let ceiling: number | null = null;
  for (const operationId of [OPERATIONS.explore, OPERATIONS.evaluate]) {
    const declared = operations?.[operationId]?.limits.concurrentJobs;
    if (declared === undefined) continue;
    ceiling = ceiling === null ? declared : Math.min(ceiling, declared);
  }
  return ceiling ?? DEFAULT_POLICY.batchSize;
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
 * artifact as well — more than the loop reads, never less. `execute` and `status` answer a
 * whole `PublicJob`, whose `authority.decision.refusal` the kit's `GuestJobStatus` does not
 * restate; `JobRunState` declares it optional and the loop reads it to name why a posting was
 * refused.
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
    // The kit's `journal` takes its two bounds as plain optionals, so an absent one is left out
    // rather than passed as `undefined` (`exactOptionalPropertyTypes`).
    journal: async (args) =>
      await jobs.journal({
        node: args.node,
        ...(args.after === undefined ? {} : { after: args.after }),
        ...(args.limit === undefined ? {} : { limit: args.limit }),
      }),
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
    journal: refuse,
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
