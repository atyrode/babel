import { jobLimits, type MachineHalf, type PluginManifest } from "@manifold/protocol";
import type { GuestCtx, GuestHookJobs, GuestJobs } from "@manifold/plugin-kit/server";
import { MACHINE_OPERATIONS, type OperationName } from "../contract.ts";
import type { Policy } from "../store/coordinator.ts";
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
  RepositoryOutcome,
  RunPlan,
  ScheduleRow,
  ScheduleTiming,
} from "./conductor.ts";

/*
  WHAT A RUN RUNS UNDER, and the slice it is asked for through.

  `RunPlan` is what the loop needs that is neither store state nor a coordinator decision
  (conductor.ts): the limits a job it posts runs under, and which operations the owner may
  meter. It is small because Babel posts very little — the beat, and the catalog operations
  behind it.

  A RUN THAT REACHES A MODEL IS A CODE SESSION (#279). The operator picks a saved Code profile
  or parametrizes one in Code's generator, and Code's `runSession` door posts the omp job
  (atyrode/code#170, reached through atyrode/manifold#575). So the engine's path, the model,
  the thinking level, the account, the per-run caps and the containment demand are Code's, and
  none of them is here. What #284 put here instead was a launcher of Babel's own, and that is
  what this file lost.

  THE LIMITS ARE THE OPERATION'S OWN. `engine.jobs.execute` takes `args.limits ?? op.limits`
  and refuses any key above the operation's declared ceiling with `limit_exceeded`
  (packages/server/src/job-service.ts), so the plan carries the declared limits of the
  operation the loop launches and a machine block that changes them changes this with it.
*/

// ---------------------------------------------------------------------------- the job's bounds

/**
 * The limits a job runs under when the manifest declares no machine half at all — a bundle
 * packed without one, which the engine refuses to run an operation from anyway. They are the
 * archive operation's own numbers, so a plan built against a manifest in that state asks for
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
 * THE MOST JOBS THIS BUNDLE ASKS A MACHINE FOR AT ONCE: the lowest `limits.concurrentJobs` any
 * operation it DECLARES states, or NULL when none of them states one. The hub enforces the
 * declared number at `execute` and refuses the rest `concurrency_limit`
 * (`packages/server/src/job-service.ts`), so it is the hard bound a fan is held to at the door
 * rather than discovered one refused posting at a time.
 *
 * The LOWEST, because one number governs every lane and a bound honoured by one operation and
 * not another is not a bound.
 *
 * AND NULL IS NOT A NUMBER TO INVENT (#279). The two operations that declared a ceiling were
 * the two a launcher posted, and they are gone: the job a run becomes is one CODE posts, under
 * CODE's declaration. Standing in the policy's own default batch here would refuse an
 * operator's stored policy above four with a sentence citing a manifest that declares nothing
 * — a governor bounded by a number nobody wrote. So the absence travels, and every validator
 * that takes it skips the bound rather than judging against a fiction.
 */
export function jobCeiling(manifest: PluginManifest): number | null {
  let ceiling: number | null = null;
  for (const operation of Object.values(manifest.machine?.operations ?? {})) {
    const declared = operation.limits.concurrentJobs;
    if (declared === undefined) continue;
    ceiling = ceiling === null ? declared : Math.min(ceiling, declared);
  }
  return ceiling;
}

// ---------------------------------------------------------------------------- the plan

export interface PlanRequest {
  readonly manifest: PluginManifest;
  /** The policy in force; a disabled one is what stops a cycle before it asks for anything. */
  readonly policy: Policy;
  /** The operation the plan's limits are for; the beat's is `scan`. */
  readonly operationId?: OperationName | undefined;
}

/**
 * WHICH OPERATIONS THE OWNER MAY METER, by operation id ({@link RunPlan.metered}).
 *
 * A job's inference is metered by the OWNER, and only when its operation binds a service whose
 * installed policy meters one of the bound operations (`agent/src/job-owner.ts`
 * `prepareServiceProxies`: a binding, a policy, and `bound.meter !== undefined`). The policy is
 * the operator's and no server half can read it, so the manifest answers the half it knows:
 * an operation that binds NOTHING can never be metered, whatever the operator installed. That
 * is the half the fold needs — it is what keeps an unmetered run from being called stalled —
 * and a call the hub actually counted is what proves the other half.
 *
 * No operation this bundle declares binds a model service any more (#279): a Babel run is a
 * Code session, so whatever meters it is carried by the job CODE posts. The table is still
 * read off the manifest rather than written as `{}` here, because the fold's question is about
 * the operation a RECEIPT names, and receipts outlive a manifest.
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
  return {
    metered: meterableOperations(machine),
    limits: operationLimits(machine, request.operationId ?? MACHINE_OPERATIONS.scan),
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
    describe: async (args): Promise<MachineReadiness> => await jobs.describe(args),
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
