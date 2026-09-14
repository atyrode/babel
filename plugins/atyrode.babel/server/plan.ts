import { jobLimits, type MachineHalf, type PluginManifest } from "@manifold/protocol";
import type { GuestCtx, GuestHookJobs, GuestJobs } from "@manifold/plugin-kit/server";
import {
  BABEL_PLUGIN_ID,
  INFERENCE_SERVICE,
  OMP_BINARY,
  OMP_TOOL,
  OPERATIONS,
  RUNTIME_TOOL_BIN,
  SESSION_INPUTS,
  type ModelPrice,
  type OperationName,
  type SessionChoice,
  type SessionPreview,
} from "../contract.ts";
import { DEFAULT_POLICY, type Policy } from "../store/coordinator.ts";
import { UNSUPPORTED_METER, type ServiceSetup } from "./inference.ts";
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

/** Where a runtime tool is bound inside the job (`agent/src/job-linux.ts`). */
export const RUNTIME_BIN = RUNTIME_TOOL_BIN;
/** The runtime tool that IS the engine: omp, driven over its own RPC by the machine half. */
export const ENGINE_TOOL = OMP_TOOL;
/** The engine's guest path, as the explore and evaluate input documents carry it. */
export const ENGINE_BINARY = OMP_BINARY;

/**
 * THE SESSION AN AUTONOMOUS DRAW RUNS UNDER, when this deployment has named one.
 *
 * It replaces the Code profile reference `analysis@3`. A profile was a name Code resolved a
 * model, an account and a price behind; there is no Code, so the three are the deployment's own
 * choice and one of them — the account — cannot be a constant in a repository: a credential id
 * and an identity key name rows in one machine's broker.
 *
 * So it is `null` here, and that is a statement rather than a stub, exactly as the empty
 * `COOKBOOK` beside it in `server.ts` is: a drawn review dispatched with no session is refused
 * BY NAME (`no-session` in the cycle's report) instead of being launched with an invented model
 * on nobody's account. An operator's own launch always carries one — `launch` takes it from the
 * request — so the surface that spends money is the one that names what it spends.
 */
export const STANDING_SESSION: SessionChoice | null = null;

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
  if (declared === undefined) return DEFAULT_LIMITS;
  const limits = jobLimits(declared);
  const costMicros = limits.inference?.costMicros;
  return {
    timeoutMs: limits.timeoutMs,
    memoryBytes: limits.memoryBytes,
    processes: limits.processes,
    outputBytes: limits.outputBytes,
    // Babel sets exactly one inference ceiling and reads exactly one back: a cost. A manifest
    // that declared a call or token ceiling would be declaring a bound this plugin does not
    // govern by, and carrying it through unread would be the plan claiming to honour it.
    ...(costMicros === undefined ? {} : { inference: { costMicros } }),
  };
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
 * Asserts the engine is on the machine this plugin ships. `runtimeTools` is what the job gets
 * bound; an operation that drives omp and does not require it would exec a path that is not
 * there, and the job would fail on the machine with a message about a missing file rather than
 * here with one about the manifest. Unlike the other aliases this one is the manifest's OWN
 * pinned artifact (`machine.tools.omp`), so the assertion is over the operation's declaration
 * and the pin is checked by `test/contract.test.ts`.
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

/**
 * THE OPERATOR'S CEILING, IN THE UNITS THE OWNER ENFORCES IT IN (ADR 0038).
 *
 * The policy states an allowance in dollars; `limits.inference.costMicros` is integer
 * micro-dollars, and the machine owner refuses the call that would pass it. Rounding is UP so a
 * ceiling is never quietly tightened by a fraction of a micro-dollar, and a non-positive
 * allowance yields NO ceiling rather than a zero one — a zero would refuse the first call, and
 * "the operator set no ceiling" is a different statement from "the operator allowed nothing".
 */
export function inferenceCeiling(allowanceUsd: number): number | null {
  if (!Number.isFinite(allowanceUsd) || allowanceUsd <= 0) return null;
  return Math.ceil(allowanceUsd * 1_000_000);
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
  /**
   * The session a run of this plan asks for, or null. An operator's launch overrides it with
   * the one the request named; see {@link STANDING_SESSION} for why the default is null.
   */
  readonly session?: SessionChoice | null | undefined;
}

/** What one review may spend: the per-cycle allowance divided by the batch it is granted in. */
export function perRunUsd(policy: Policy): number {
  return policy.batchSize <= 0 ? policy.perCycleCost : policy.perCycleCost / policy.batchSize;
}

/**
 * WHICH OPERATIONS THE OWNER MAY METER, by operation id ({@link RunPlan.metered}).
 *
 * A job's inference is metered by the OWNER, and only when its operation binds a service whose
 * installed policy meters one of the bound operations (`agent/src/job-owner.ts`
 * `prepareServiceProxies`: a binding, a policy, and `bound.meter !== undefined`). The policy is
 * the operator's and no server half can read it, so the manifest answers the half it knows:
 * an operation that binds NOTHING can never be metered, whatever the operator installed. That
 * is the half the loop needs — it is what keeps an unmetered run from being called stalled —
 * and a call the hub actually counted is what proves the other half.
 */
function meterableOperations(machine: MachineHalf | null): Record<string, boolean> {
  const metered: Record<string, boolean> = {};
  for (const [operationId, operation] of Object.entries(machine?.operations ?? {})) {
    metered[operationId] = (operation.services ?? []).length > 0;
  }
  return metered;
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
  const operationId = request.operationId ?? OPERATIONS.evaluate;
  const allowance = perRunUsd(request.policy);
  const ceiling =
    METERED_OPERATIONS[operationId] === true ? inferenceCeiling(allowance) : null;
  return {
    engine: { binary: engineBinary(machine), args: [] },
    session: request.session ?? STANDING_SESSION,
    caps: {
      perRunUsd: allowance,
      toolCalls: TOOL_CALLS,
      idleMs: IDLE_MS,
      handshakeMs: HANDSHAKE_MS,
    },
    recipes,
    metered: meterableOperations(machine),
    // Every Babel run is contained. The operator relaxes it per run and never by default: a
    // launch that cannot state the sandbox it established is refused by the machine half.
    requireContainment: true,
    limits: {
      ...operationLimits(machine, operationId),
      // THE CEILING LEAVES THE HUB HERE and nowhere else: the owner enforces it per call and
      // refuses the one that would pass it (HTTP 429 `service_ceiling_exceeded`), never
      // mid-stream. A run whose model the policy does not price is refused before its first
      // call, because a ceiling in money without a price is not a ceiling.
      ...(ceiling === null ? {} : { inference: { costMicros: ceiling } }),
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
 * THE NINTH VERB IS THE CALLER'S TO PASS. `follow` is the one thing `GuestHookJobs` omits, and
 * it is the only read the hub serves for a job that is still running (`journal` refuses that
 * job `job_unfinished`), so a dispatch hands its own and a hook hands none — which is what
 * decides whether that cycle can fold where its in-flight runs are (#261). It is taken here
 * rather than reached for, because the slice is built from a handle whose type says a hook's
 * has no such member.
 *
 * Everything is passed straight through: the protocol's own shapes already satisfy the loop's,
 * which is why this is a narrowing and not a translation. `schedules()` answers
 * `PublicJobSchedule` rows, which are `ScheduleRow`s carrying the plugin id and the pinned
 * artifact as well — more than the loop reads, never less. `execute` and `status` answer a
 * whole `PublicJob`, whose `authority.decision.refusal` the kit's `GuestJobStatus` does not
 * restate; `JobRunState` declares it optional and the loop reads it to name why a posting was
 * refused.
 */
export function jobsSlice(jobs: GuestHookJobs, follow?: GuestJobs["follow"]): BabelJobs {
  return {
    describe: async (args): Promise<MachineReadiness> =>
      await jobs.describe({ machineId: args.machineId, pluginId: args.pluginId }),
    execute: async (args: JobLaunch): Promise<JobRunState> =>
      await jobs.execute({ ...args, outputs: owned(args.outputs) }),
    status: async (node: JobRef): Promise<JobRunState> => await jobs.status(node),
    listRuns: async (args) => await jobs.listRuns(args),
    output: async (args: { node: OutputRef; offset: number; maxBytes: number }) =>
      await jobs.output(args),
    // Absent, not `undefined`: the loop reads `follow === undefined` as "this cycle cannot see
    // a running job", and an own property holding undefined would satisfy neither
    // `exactOptionalPropertyTypes` nor a reader of the object.
    ...(follow === undefined
      ? {}
      : { follow: async (node: JobRef, receive: () => void) => await follow(node, receive) }),
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

// ---------------------------------------------------------------------------- the session

/**
 * THE THREE JOB INPUTS A SESSION BECOMES (#279).
 *
 * Babel's machine half launches `omp --mode rpc` and hands it nothing about a provider: the two
 * YAML documents below are materialized into the job's private home by the OWNER
 * (`inputFiles.models`/`inputFiles.config` with a `homePath`), and the url and bearer of the
 * inference binding are spliced into `models.yml`'s `providers.*.baseUrl` and
 * `providers.*.apiKey` by the owner too (`jsonValues`). The credential therefore never passes
 * through this file, the job request, the hub's journal or the sandbox's argv.
 *
 * The shapes are manifold-omp's own, copied rather than invented so one omp build reads both:
 * `models` is `nativeModelConfiguration(pool).models` and `config` is that function's `config`
 * spread under the run's overlay (`plugins/atyrode.omp/execution.ts`
 * `nativeModelConfiguration`, and `sessionPreparation`'s `{...overlay, ...native.config}`).
 * `transport: "pi-native"` with `discovery: {type: "proxy"}` is what makes omp speak the
 * gateway's `/v1/pi/stream` instead of a provider's own API.
 *
 * `accountPool` is NOT a file. It is a `RuntimeAccountPool` the service policy's runtime maps
 * into the gateway job (`ServiceRuntime.input: {accountPool: {input: "accountPool"}}`, exactly
 * as manifold-omp's `configureGateway` installs it), which is how one job says which of several
 * enrolled accounts it spends — the whole of #267, with no new primitive.
 *
 * `disabledProviders` is deliberately absent where manifold-omp sets it: omp disables the
 * providers its bundled catalogue holds and Babel does not have that catalogue. Nothing is lost
 * — a provider with no `baseUrl` and no `apiKey` in `models.yml`, in a job whose environment
 * carries no provider variable at all (`machine/engine/launch.ts` inherits a fixed list), has
 * no route to anything.
 */
export function sessionInputs(session: SessionChoice): Record<string, string> {
  const provider = session.account.provider;
  const pool = {
    [provider]: [
      {
        scope: session.account.scope,
        // The two conversions the door's string surface owes the wire; `SessionChoiceSchema`'s
        // regex is what makes the first total, and an empty key is the api-key case.
        credentialId: Number(session.account.credentialId),
        identityKey: session.account.identityKey === "" ? null : session.account.identityKey,
      },
    ],
  };
  const models = {
    providers: {
      [provider]: {
        baseUrl: "",
        apiKey: "",
        transport: "pi-native",
        discovery: { type: "proxy" },
      },
    },
  };
  const config = {
    modelRoles: { default: session.model },
    ...(session.thinking === undefined ? {} : { defaultThinkingLevel: session.thinking }),
    extensions: [],
    extendedContext: true,
    startup: { setupWizard: false },
  };
  return {
    [SESSION_INPUTS.accountPool]: JSON.stringify(pool),
    [SESSION_INPUTS.models]: JSON.stringify(models),
    [SESSION_INPUTS.config]: JSON.stringify(config),
  };
}

/** The refusal for a session whose model and account disagree about the provider, or "". */
export function sessionShortfall(session: SessionChoice): string {
  const provider = session.model.slice(0, session.model.indexOf("/"));
  if (provider === session.account.provider) return "";
  return (
    `session_provider_mismatch: the model ${JSON.stringify(session.model)} is routed to ` +
    `${JSON.stringify(provider)} and the account named belongs to ` +
    `${JSON.stringify(session.account.provider)}`
  );
}

/**
 * BABEL'S OWN LAUNCH PROFILE: the three things a run was asked to be, as the `runs` row records
 * them before the machine answers and as the receipt restates them afterwards. It replaces the
 * profile block of Code's runtime-info sidecar, which named a Code profile id and revision that
 * no longer resolve to anything.
 */
export function launchProfile(session: SessionChoice | null): {
  model: string;
  thinking: string;
  account: string;
} {
  return {
    model: session?.model ?? "",
    thinking: session?.thinking ?? "",
    account: session?.account.identityKey ?? "",
  };
}

// ---------------------------------------------------------------------------- the services slice

/**
 * ONE OWNER-INSTALLED SERVICE POLICY, narrowed to what a preview reads (ADR 0038): the prices
 * the owner will meter a model call at. Everything else in a policy — the runtime, the routes,
 * the concurrency — is the owner's business and none of a preview's.
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
 * holding `services:configure` at the machine — and a preview that reported "not installed"
 * because it was not allowed to look would be telling the operator to install a policy that is
 * already there.
 *
 * `setup` is what makes the SECOND answer two answers (#284). A policy the hub refused and a
 * policy nobody installed read identically here, and the difference is the whole of what the
 * operator does next, so the last thing the hub answered an owner is carried beside the read.
 */
export type ServicePolicyOutcome =
  | {
      readonly ok: true;
      readonly policy: { readonly prices?: ServicePrices | undefined } | null;
      readonly setup?: ServiceSetup | null | undefined;
    }
  | { readonly ok: false; readonly reason: string };

/** The one service question this plugin asks of a machine: what the owner installed under an id. */
export interface ServicesSlice {
  policy(machineId: string, serviceId: string): Awaitable<ServicePolicyOutcome>;
}

/** What the hub last answered an owner about one service here; see `server/inference.ts`. */
export type SetupReader = (
  machineId: string,
  serviceId: string,
) => Awaitable<ServiceSetup | null>;

/**
 * `ctx.services`, narrowed to that question. The refusal is KEPT rather than thrown because it
 * is an ordinary answer here: a dispatch under the operator's own credential is not entitled to
 * read a machine's service configuration, and the preview says what it could not see instead of
 * failing the door the operator opened to look at something else.
 *
 * `setup` is read ONLY when the configuration was readable and holds no such policy: that is
 * the one case where Babel's own record of the hub's answer changes what the preview says.
 */
export function servicesSlice(services: GuestCtx["services"], setup?: SetupReader): ServicesSlice {
  return {
    policy: async (machineId: string, serviceId: string): Promise<ServicePolicyOutcome> => {
      try {
        const read = await services.readConfiguration({ machineId });
        const found = read.configuration.policies.find((entry) => entry.serviceId === serviceId);
        if (found !== undefined) return { ok: true, policy: { prices: found.prices } };
        return {
          ok: true,
          policy: null,
          setup: setup === undefined ? null : await setup(machineId, serviceId),
        };
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

function dollars(micros: number): string {
  return `$${(micros / 1_000_000).toFixed(4)}`;
}

/**
 * WHAT `launchPreview` SAYS ABOUT THE SESSION, in the four states of
 * {@link SESSION_POLICY_STATES}. It is assembled from the OWNER's numbers and the operator's
 * choice and never from an estimate of either: the price is the installed policy's, the ceiling
 * is the one the job request will carry, and a machine whose configuration this caller may not
 * read is reported as unread rather than as unconfigured.
 */
export function sessionPreview(args: {
  readonly session: SessionChoice | null;
  readonly outcome: ServicePolicyOutcome;
  readonly ceilingMicros: number | null;
}): SessionPreview {
  const model = args.session?.model ?? "";
  const account = args.session?.account.identityKey ?? "";
  const ceiling = args.ceilingMicros === null ? {} : { ceilingMicros: args.ceilingMicros };
  const bound =
    args.ceilingMicros === null
      ? "no cost ceiling"
      : `a ceiling of ${dollars(args.ceilingMicros)} for this run`;
  if (!args.outcome.ok) {
    return {
      serviceId: INFERENCE_SERVICE.serviceId,
      account,
      model,
      priced: false,
      ...ceiling,
      policy: "unreadable",
      unreadable: args.outcome.reason,
      note:
        `this hub may not read the machine's service configuration, so whether ` +
        `${INFERENCE_SERVICE.serviceId} is installed is unknown here; the launch runs under ${bound}`,
    };
  }
  if (args.outcome.policy === null) {
    // THE HUB'S OWN ANSWER DECIDES WHICH SENTENCE THIS IS (#284). An absent policy the hub
    // refused over the meter kind is a hub older than this plugin: nobody can install anything
    // until its SDK carries `pi-native-usage` (manifold#570/#572), and the same hub refuses to
    // deploy Babel's machine half at all, so "run setupInference" would be advice that cannot
    // work. Any other refusal is reported as itself, with the hub's sentence beside it.
    const refusal = args.outcome.setup?.state === "refused" ? args.outcome.setup.detail : "";
    const unsupported = refusal !== "" && UNSUPPORTED_METER.test(refusal);
    return {
      serviceId: INFERENCE_SERVICE.serviceId,
      account,
      model,
      priced: false,
      ...ceiling,
      policy: unsupported ? "unsupported" : "missing",
      unreadable: "",
      ...(refusal === "" ? {} : { setupRefusal: refusal }),
      note: unsupported
        ? `this hub refused the ${INFERENCE_SERVICE.serviceId} policy because it does not know ` +
          `the ${INFERENCE_SERVICE.meterKind} meter kind (manifold#570), so no metered lane can ` +
          `be installed here until the hub carries it; nothing an operator does on this machine ` +
          `changes that`
        : refusal === ""
          ? `no ${INFERENCE_SERVICE.serviceId} policy is installed on this machine, so a run has ` +
            `no lane to a model; setupInference installs one`
          : `no ${INFERENCE_SERVICE.serviceId} policy is installed: the last attempt was refused ` +
            `by this hub (${refusal.slice(0, 200)})`,
    };
  }
  const prices = args.outcome.policy.prices;
  const price = model === "" ? undefined : (prices?.models[model] ?? prices?.default);
  if (price === undefined) {
    return {
      serviceId: INFERENCE_SERVICE.serviceId,
      account,
      model,
      priced: false,
      ...ceiling,
      policy: "unpriced",
      unreadable: "",
      note:
        model === ""
          ? `${INFERENCE_SERVICE.serviceId} is installed; choose a model to see what it is priced at`
          : `${INFERENCE_SERVICE.serviceId} prices no ${model}, so a run with a cost ceiling is ` +
            `refused service_price_unknown before its first call`,
    };
  }
  return {
    serviceId: INFERENCE_SERVICE.serviceId,
    account,
    model,
    priced: true,
    price,
    ...ceiling,
    policy: "priced",
    unreadable: "",
    note:
      `${model} is metered at ${dollars(price.inputPerMillion)} per million input tokens and ` +
      `${dollars(price.outputPerMillion)} per million output, on ${account === "" ? "no account yet" : account}, under ${bound}`,
  };
}
