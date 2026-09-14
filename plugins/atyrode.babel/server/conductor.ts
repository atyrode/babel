import { MACHINE_REPOSITORY_REASONS } from "@manifold/protocol";
import type { SqlParam, SqlStatement } from "@manifold/plugin";
import { z } from "zod";
import {
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  JOB_OUTPUT_FILES,
  MaterialIndexSchema,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  RUN_STAGES,
  ReceiptSchema,
  type MaterialEntry,
  type MaterialIndex,
  type Receipt,
} from "../contract.ts";
import type { Coordinator, Fence, Gap, Policy, Stop } from "../store/coordinator.ts";
import { refuseRow, type RowRefusal } from "../store/acts.ts";
import { REFUSALS, refusalCode, refusalReason, type RefusalCode } from "../machine/results.ts";
import type { BabelStore } from "../store/store.ts";
import { DRAW_PENDING } from "../doors/launch.ts";
import { readExploreAnswer, unservedLocator } from "./engine/prompts.ts";
import type { CodeEngine, SessionRead } from "./engine/session.ts";

/*
  THE CONDUCTOR — Babel's loop, on the hub (plan §4: `babel conductor run` becomes a server-half
  loop over `engine.jobs`, and the loop's policy is store state rather than a process).

  One `tick()` is one cycle, and a cycle is four things in a fixed order:

    1. the policy in force decides whether the loop exists at all. `enabled` is the activation
       gate (§14): a disabled policy registers nothing, draws nothing, ingests nothing and
       spends nothing. Turning Babel off is one operator record, not a migration.
    2. the schedule is reconciled. `engine.jobs.schedule` is the only cadence primitive a plugin
       has, and it schedules a JOB on a machine — so the loop's beat is the cheapest useful job
       Babel owns, `scan`: no model spend, a fresh catalog every cadence, and a settlement that
       wakes the hub. Drawing stays here, where the coordinator's lanes and ceilings govern
       every model dollar; a fixed `explore` registered at schedule time would spend outside them.
    3. finished jobs are ingested — the ones the loop requested and the beat's own, which nobody
       requested. A run's records, edges, statuses, assessments, filings, plans, questions,
       steering replies and sessions arrive as ONE sealed output holding the files
       `JOB_OUTPUT_FILES` names, each a list of rows in the store's own shapes; ingestion is a
       `batch` of `INSERT OR IGNORE` (append-only tables are never rewritten) and an upsert for
       the two tables that are a projection rather than an act — `sessions` and `runs`. Every
       insert is keyed by the row's own identifier, so ingesting the same output twice writes
       the same rows and changes nothing.
    4. the receipt settles the claim. What a review cost is what the machine recorded, not what
       the draw reserved; a job that failed, was cancelled or wrote no receipt settles `failed`
       at the cost it did incur, so a lane's allowance is never held by a dead worker.

  What the loop does NOT own: a timer. A plugin may not poll as an alternate scheduler
  (`docs/PLUGINS.md`), and a server half has no clock of its own — so `tick()` is called by the
  plugin's own dispatch (any door) and by the job-settled lifecycle hook for this plugin's jobs.
  Every step above is idempotent, which is what makes that safe: two ticks in the same second
  do the work of one, and a cycle that is retried re-derives the same assignment ids, the same
  job ids and the same claim.
*/

// ---------------------------------------------------------------------------- the jobs slice

/** In-realm the engine answers directly, isolated it answers over a promise; the loop takes both. */
export type Awaitable<T> = T | Promise<T>;

/** The engine's own address of a job and of one of its sealed outputs (`ManifoldRef`). */
export interface JobRef {
  readonly kind: "job";
  readonly machineId: string;
  readonly operationId: string;
  readonly jobId: string;
}
export interface OutputRef {
  readonly kind: "output";
  readonly machineId: string;
  readonly operationId: string;
  readonly jobId: string;
  readonly outputId: string;
}

export type JobState =
  | "queued"
  | "admitted"
  | "start-committed"
  | "started"
  | "exited"
  | "interrupted"
  | "cancelled"
  | "refused";

/** One sealed output as a result names it; `bytes` is the archive's, not the files' sum. */
export interface JobOutput {
  readonly outputId: string;
  readonly name: string;
  readonly bytes: number;
  readonly files: number;
}

export interface JobRunState {
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly state: JobState;
  readonly result: {
    readonly state: JobState;
    readonly exitCode: number | null;
    readonly reason: string | null;
    readonly outputs: readonly JobOutput[];
    /**
     * What the OWNER metered for this job, when the hub has it: the brokered lane's own count
     * of calls, tokens and price (`JobInferenceUsageSchema`, manifold#554). It is what fills
     * the run row's `tokens` and `cost_usd` at settle, in preference to the receipt's own
     * numbers — the receipt is the engine's word about itself, this is the meter's (#261).
     */
    readonly usage?:
      | { readonly inference?: InferenceUsage | undefined }
      | null
      | undefined;
  } | null;
  /** WHY A POSTING WAS REFUSED, which is never in `result`: a job refused at admission never
   *  ran and has no result at all. The hub's own word for it — `concurrency_limit` when the
   *  operation's declared `limits.concurrentJobs` is full — is on the authority decision
   *  (`PublicJobSchema.authority`), which the kit's own `GuestJobStatus` does not restate. */
  readonly authority?:
    | { readonly decision?: { readonly refusal: string | null } | null | undefined }
    | undefined;
}

/** One job's metered inference, restated so the loop compiles against the slice. */
export interface InferenceUsage {
  readonly calls: number;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cachedInputTokens: number;
  readonly costMicros: number;
}

/**
 * WHAT A RUNNING JOB'S REPLAY RING HOLDS, restated the same way.
 *
 * `follow` is the only read the hub serves for a job that has NOT finished — `journal` refuses
 * one `job_unfinished`, because "watching one is what `follow` is for" — and what it answers
 * with is a snapshot of the ring the hub retains per job (`JobFollowSnapshotSchema`;
 * `packages/server/src/job-service.ts` `follow`). That ring is kept for every running job
 * whether or not anybody is watching (`retainJobEvent`), so a fold that opens a subscription,
 * takes the snapshot and closes it in the same turn sees everything a subscriber would have
 * been sent, and holds no stream between cycles.
 *
 * Unlike the journal's, this sequence is CONTIGUOUS: the ring carries byte frames too, so a
 * hole below `firstSeq` is retention that dropped frames rather than the journal's own
 * contract. Two of them are what this loop reads — `job_progress` (manifold#552) and
 * `inference_call` (#554) — and every other one is a `type` it steps over, which is why the
 * event is typed as its discriminant and parsed by a schema at the fold rather than asserted
 * into a shape here. No frame carries a clock of the hub's: the only instant in one is the
 * owner's, inside a `job_progress` body.
 */
export interface FollowEvent {
  readonly seq: number;
  readonly event: { readonly type: string } & Readonly<Record<string, unknown>>;
}

export interface FollowSnapshot {
  readonly events: readonly FollowEvent[];
  /** The oldest sequence still in the ring, or null while it holds nothing. */
  readonly firstSeq: number | null;
  /** What the hub says it cannot replay: a trimmed prefix, or a ring it threw away whole. */
  readonly unavailable: { readonly fromSeq: number; readonly toSeq: number } | null;
}

/** One look at a running job: the snapshot, and the subscription to close in the same turn. */
export interface FollowRead {
  readonly snapshot: FollowSnapshot;
  close(): Awaitable<void>;
}

/** What `describe` answers, narrowed to the four facts that decide where a job may run. */
export interface MachineReadiness {
  readonly connected: boolean;
  readonly operations?:
    | Readonly<Record<string, { readonly ready: boolean; readonly reason: string | null }>>
    | undefined;
  readonly installation: {
    readonly revision: string;
    readonly artifactSha256: string;
    readonly enabled: boolean;
    readonly ready: boolean;
  } | null;
}
export interface JobLimits {
  readonly timeoutMs: number;
  readonly memoryBytes: number;
  readonly processes: number;
  readonly outputBytes: number;
  /**
   * THE PER-CALL CEILING THE OWNER ENFORCES (ADR 0038), in integer micro-dollars and only on the
   * two operations that bind the inference service. The owner refuses the call that would pass
   * it — HTTP 429 `service_ceiling_exceeded`, never mid-stream — and a run whose model the
   * policy does not price is refused before its first call, because a ceiling in money without
   * a price is not a ceiling.
   */
  readonly inference?: { readonly costMicros: number } | undefined;
}

/** A job request as the loop makes one: the engine fills in authority, permit and digest. */
export interface JobLaunch {
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly input: Readonly<Record<string, string | number | boolean>>;
  readonly outputs: readonly {
    readonly name: string;
    readonly locationId: string;
    readonly components: readonly string[];
  }[];
  readonly limits?: JobLimits | undefined;
  readonly installationRevision?: string | undefined;
  readonly artifactSha256?: string | undefined;
}

/** `JobScheduleTiming`, restated so the loop compiles against the slice rather than the host. */
export interface ScheduleTiming {
  readonly scheduleId: string;
  readonly revision: string;
  readonly firstNominalAt: number;
  readonly intervalMs: number;
  readonly deadlineMs: number;
  readonly expiresAt: number;
  readonly offlinePolicy: "skip" | "coalesce-one";
}

export interface ScheduleRow extends ScheduleTiming {
  readonly machineId: string;
  readonly operationId: string;
}

/**
 * The nine verbs the loop uses, and no more: `engine.jobs` in-realm (`PluginJobContext`) and
 * the kit's `GuestJobs` with its schedule verbs both satisfy it, and a test satisfies it with a
 * fake. Everything returns `Awaitable` because in-realm the engine answers synchronously and
 * across the isolate boundary it answers with a promise (ADR 0016's one contract, both ends).
 *
 * `follow` IS THE ONE OPTIONAL MEMBER, and `journal` is not here at all. A running job's
 * journal cannot be read — the hub refuses it `job_unfinished` — so the only way to learn where
 * an in-flight run is is `follow`'s snapshot, taken and closed in one turn. It is a DISPATCH's
 * verb alone (`GuestHookJobs` is every job verb except the live subscription), so the slice a
 * settlement's hook is served carries none, and a cycle that hook woke folds nothing rather
 * than pretending to (#261).
 */
export interface JobsSlice {
  describe(args: { machineId: string; pluginId: string }): Awaitable<MachineReadiness>;
  execute(args: JobLaunch): Awaitable<JobRunState>;
  status(node: JobRef): Awaitable<JobRunState>;
  listRuns(args: {
    machineId: string;
    operationId?: string | undefined;
    limit?: number | undefined;
  }): Awaitable<{ runs: readonly { job: JobRunState | null }[] }>;
  output(args: {
    node: OutputRef;
    offset: number;
    maxBytes: number;
  }): Awaitable<{ data: string; eof: boolean }>;
  /**
   * A running job's replay ring, opened for its snapshot and closed in the same turn; absent on
   * a hook's slice. `receive` is never called: what the hub sends after the snapshot is in the
   * ring for the next cycle, and this half holds no stream between cycles.
   */
  follow?: (node: JobRef, receive: () => void) => Awaitable<FollowRead>;
  schedule(args: JobLaunch & ScheduleTiming): Awaitable<unknown>;
  schedules(): Awaitable<readonly ScheduleRow[]>;
  disableSchedule(args: { scheduleId: string; revision: string }): Awaitable<unknown>;
}

// ---------------------------------------------------------------------------- the machines slice

/**
 * `MachineRepositoryFact`, restated so the loop compiles against the slice rather than the
 * host. `identity` is the resolved git common directory — one per repository, however many
 * worktrees view it — `remote` is `origin` normalized to `host/owner/repo`, both null unless
 * `reason` is `repository`, and `observedAt` is the AGENT's clock at the probe.
 */
export interface RepositoryFact {
  readonly path: string;
  readonly identity: string | null;
  readonly remote: string | null;
  readonly reason: string;
  readonly observedAt: number;
}

/** The fact, or the hub's word that nobody could be asked — which is never a fact about a disk. */
export type RepositoryOutcome =
  | { readonly ok: true; readonly fact: RepositoryFact }
  | { readonly ok: false; readonly reason: string };

/**
 * The one verb of `ctx.machines` this loop uses: `engine.machines.repository` (#535), asked of
 * the agent standing on the host rather than of the sandbox a scan ran in.
 */
export interface MachinesSlice {
  repository(machineId: string, path: string): Awaitable<RepositoryOutcome>;
}

// ---------------------------------------------------------------------------- the keys slice

/**
 * The two verbs of `ctx.storage` this loop uses (ADR 0034: `ctx.storage` is where a plugin keeps
 * KEYS and the plugin database is where it keeps ROWS).
 *
 * The loop keeps exactly one key: the day's tally of the reasons it did not spend. It is a key
 * rather than a table because it is one small value rewritten in place that nobody reads as a
 * row — and it is kept outside this object because a cycle is a FRESH CONDUCTOR over the wake
 * that caused it (`server.ts`: a settlement, a door, the enable), so a counter living in this
 * closure would read zero for every tick of a real day.
 *
 * A key the host will not serve is a tally the report says nothing false about: the tick's own
 * counts stand, the day's are the tick's, and a note says the day could not be read.
 */
export interface KeysSlice {
  get(key: string): Awaitable<string | null>;
  set(key: string, value: string): Awaitable<void>;
}

// ---------------------------------------------------------------------------- what a run needs

/**
 * What the loop needs that is neither store state nor a coordinator decision. It is two facts
 * now, and the shrinkage is the point (#279): a run that reaches a model is a CODE session, so
 * the engine to drive, the model, the account, the caps and the containment demand are Code's
 * and this loop states none of them. What is left is what the loop still posts itself — the
 * beat — and what it still has to judge about a settled run.
 */
export interface RunPlan {
  /**
   * WHETHER A RUN OF THIS OPERATION IS ONE THE OWNER METERS, by operation id.
   *
   * It is what a stall may be judged of (`foldRun`), and it is derived from the manifest's own
   * service bindings: the owner attaches `usage.inference` to a job only when its operation
   * binds a service and the policy it installed meters one of the bound operations
   * (`agent/src/job-owner.ts` `prepareServiceProxies`). The second half of that is the
   * OPERATOR's, in a policy no server half can read, so `true` here is "the hub may meter this"
   * and never "the hub is metering this" — which is why the fold also accepts a call it has
   * already counted as proof. An operation that binds nothing can never be metered, and no
   * operation this bundle declares binds one: a receipt from a run Code posted is metered by
   * the policy CODE's job carries, and the fold reads that off the job it settles.
   */
  readonly metered: Readonly<Record<string, boolean>>;
  readonly limits: JobLimits;
}

export interface ConductorDeps {
  readonly store: BabelStore;
  readonly coordinator: Coordinator;
  readonly jobs: JobsSlice;
  readonly machines: MachinesSlice;
  /**
   * BABEL'S SIDE OF CODE'S DOORS, over this wake's own authority (#279).
   *
   * A run that reaches a model is a job of CODE's, posted by `atyrode.code.runSession` under
   * `atyrode.omp`'s operation. `ctx.jobs` verbs are bound to the calling plugin's id and
   * `onJobSettled` is delivered only to the plugin that STARTED the job, so such a job is never
   * Babel's to poll or to be woken by: the only way this loop learns what became of it is to
   * ask Code, through `code.readSession`, on a wake something else caused. That is the whole
   * reason this dependency is here, and it is why `reconcileRuns` has two halves.
   */
  readonly engine: CodeEngine;
  /** Where the day's tally is kept between wakes; see {@link KeysSlice}. */
  readonly keys: KeysSlice;
  readonly plan: RunPlan;
  readonly now: () => number;
}

// ---------------------------------------------------------------------------- the report


export type ScheduleState = "registered" | "kept" | "unregistered" | "absent";

export interface RequestedJob {
  readonly runId: string;
  readonly jobId: string;
  readonly machineId: string;
  readonly claimId: string;
  readonly recordId: string;
  readonly role: string;
  readonly lane: string;
}

export interface IngestedRun {
  readonly runId: string;
  readonly jobId: string;
  readonly closure: string;
  readonly costUsd: number;
  /** Rows written per output file, by the file's own name. */
  readonly rows: Readonly<Record<string, number>>;
  readonly skipped: number;
}

export interface SettledClaim {
  readonly claimId: string;
  /** `abandoned` is a claim released without a result: nobody is coming back to report one. */
  readonly outcome: "completed" | "failed" | "skipped" | "abandoned";
  readonly cost: number;
  readonly overrun: boolean;
  readonly refused: string | null;
  /** Why it was abandoned, in the cycle's own words; null for a claim a receipt settled. The
   *  claims row holds the outcome and the spend, and this holds the sentence. */
  readonly reason: string | null;
}

export interface RefusedDraw {
  readonly assignmentId: string;
  readonly recordId: string;
  readonly reason: string;
  readonly detail: string;
}

/**
 * WHY THE LOOP IS NOT DRAWING, when it is the loop's own verdict rather than the coordinator's.
 *
 * Three settlements in a row that reached no model and produced nothing is a lane that is
 * broken rather than a deployment that is satisfied — a machine whose engine will not launch, a
 * role with no recipe, a credential that has lapsed — and drawing a fourth spends another
 * reservation to learn the same thing. {@link Park} is that verdict, with the sentence an
 * operator acts on and the streak it was reached by.
 */
export interface Park {
  /** How many settled claims in a row spent nothing and produced nothing. */
  readonly barren: number;
  readonly reason: string;
}

/**
 * WHY A CYCLE DID NOT SPEND, counted rather than narrated (F11, G9).
 *
 * `gaps` counts the coordinator's own reason word for every draw that yielded no assignment:
 * the {@link Stop} that ended the cycle (`disabled`, `batch`, `per-cycle`, `daily`,
 * `no-candidates`, …) and every candidate {@link Gap} it declined on the way (`claimed`,
 * `cooling`, `capped`, …). The two vocabularies are disjoint word sets, so one map by reason is
 * unambiguous, and a surface that wants the records rather than the counts reads `gaps` on the
 * report itself.
 *
 * `refusals` counts the submissions the review contract refused, by the code
 * `machine/results.ts` names — the receipt's own `reason` for a run that was paid for and
 * refused at submit, and the store's verdict on a row it would not write. Those are not
 * failures of the loop: they are money spent on an answer that did not stand, which is what the
 * 2026-09-13 drain could not see (F8, F16).
 */
export interface CycleTally {
  readonly gaps: Readonly<Record<string, number>>;
  readonly refusals: Readonly<Record<string, number>>;
}

/**
 * WHAT IS IN FLIGHT AND WHERE IT IS, counted once a cycle from what the loop could read.
 *
 * Three numbers, because three is what the 2026-09-13 drain needed and did not have: how many
 * jobs are running, how many of them have reached the model, and how many said they had and
 * then went quiet. `running` and `atModel` being far apart for a whole cycle is the shape of
 * that day — twenty-six draws, every one of them still reading the corpus, no engine anywhere
 * (F12, O2) — and `stalled` is the shape of the other half of it.
 *
 * `atModel` and `stalled` are what the cycle just folded, or — for a cycle woken by a
 * settlement, which is served no `follow` — the rows exactly as the last dispatch-woken cycle
 * left them. Neither is ever a number about a run this half has not read.
 */
export interface RunsTally {
  readonly running: number;
  readonly atModel: number;
  readonly stalled: number;
}

export interface TickReport {
  readonly at: number;
  /** The cycle's own run id: what this tick's draws and their claims are accounted to. */
  readonly cycleRunId: string;
  readonly policyVersion: string;
  readonly enabled: boolean;
  readonly schedule: ScheduleState;
  readonly requested: readonly RequestedJob[];
  readonly ingested: readonly IngestedRun[];
  readonly settled: readonly SettledClaim[];
  readonly refused: readonly RefusedDraw[];
  /** Why drawing stopped; null when the cycle never drew (a disabled policy, or a park). */
  readonly stop: Stop | null;
  readonly gaps: readonly Gap[];
  /** The loop's own reason for drawing nothing, or null while it draws. */
  readonly parked: Park | null;
  /** The reasons this cycle did not spend, for the tick and cumulatively for the UTC day. */
  readonly pulse: { readonly tick: CycleTally; readonly today: CycleTally };
  /** Where this cycle's in-flight jobs are, from the replay rings it read. */
  readonly runs: RunsTally;
  /** Jobs still in flight when the cycle ended. */
  readonly pending: number;
  readonly notes: readonly string[];
}

export interface Conductor {
  tick(): Promise<TickReport>;
}

// ---------------------------------------------------------------------------- constants

/** The loop's one schedule. Its revision is the policy version, so a policy change re-registers. */
export const CONDUCTOR_SCHEDULE_ID = `${BABEL_PLUGIN_ID}.conductor`;
/** The beat: cheap, model-free, useful, and its settlement is what wakes the hub. */
export const BEAT_OPERATION: string = OPERATIONS.scan;
/** A schedule is registered for a bounded life and renewed by the loop that still wants it. */
export const SCHEDULE_LIFETIME_MS = 30 * 24 * 60 * 60 * 1000;
/** How far back a tick looks for beat jobs nobody has ingested yet. */
const BEAT_RUNS_SCANNED = 20;

/** The engine serves an output in chunks of at most this many bytes (`job-doors.ts`). */
const OUTPUT_CHUNK_BYTES = 65536;
/** An output larger than this is refused rather than buffered; the note says what was lost. */
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;
/** `MAX_SQL_BATCH_STATEMENTS`: a batch holds the write lock, so ingestion writes it in pieces. */
const MAX_BATCH_STATEMENTS = 256;
/** The states a job is done in; anything else is still in flight. */
const TERMINAL_STATES: Record<string, true> = {
  exited: true,
  interrupted: true,
  cancelled: true,
  refused: true,
};
/**
 * How many cycles in a row the hub must fail to report a job before the reaper treats its claim
 * as dead. One silent poll is a hiccup — a machine reconnecting, a hub restarting — and killing
 * a live review for it would be worse than the ghost; two in a row is a worker that is gone.
 */
const UNREPORTED_CYCLES = 2;

/**
 * WHEN A RUN AT THE MODEL IS CALLED STALLED, and why it is ninety seconds.
 *
 * A brokered call is metered when it ANSWERS, so the gap between saying `at the model` and the
 * first `inference_call` is one whole model turn — thinking included — and a long one is
 * ordinary. Ninety seconds is past the slowest ordinary turn and well short of the engine's own
 * idle bound, so the flag appears while an operator can still act on it and never on a run that
 * is merely thinking hard. It is a flag on a row and never a closure: nothing here observed a
 * dead process, and the next metered call clears it.
 *
 * IT IS SAID ONLY OF A RUN THE HUB METERS. Where nothing is metered, "nothing metered for over
 * ninety seconds" is true of every run that ever reached a model and says nothing at all; see
 * `foldRun`.
 */
const STALLED_AFTER_MS = 90_000;

/**
 * The two lifecycle frames the loop reads, parsed rather than asserted — they arrive from the
 * job's replay ring, which grows frames this build has never heard of, and a fold that trusted
 * a shape would write a NaN into a spend column the day one changed.
 */
const ProgressFrameSchema = z.object({
  type: z.literal("job_progress"),
  stage: z.string(),
  message: z.string().optional(),
  fraction: z.number().optional(),
  at: z.number(),
});
const InferenceFrameSchema = z.object({
  type: z.literal("inference_call"),
  model: z.string(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  cachedInputTokens: z.number(),
  costMicros: z.number(),
});

/**
 * THE PARK, in three numbers and the one rule that made 2026-09-13 worth a document.
 *
 * {@link PARK_AFTER} settlements in a row that reached no model and produced nothing park the
 * loop. WHAT COUNTS AS ONE IS THE WHOLE POINT: a review the model answered and the contract
 * then refused (`schema`, `support`, `empty`) is SPEND — the day's allowance went to it and the
 * remedy is the recipe, not the loop — while a job that died before the first call, or a claim
 * abandoned because its worker never came back, is the free failure a park exists to stop. The
 * Go conductor counted the first as the second, parked after three of them, and the evaluation
 * ladder sat unreviewed while the operator's window drained into nothing (F16, F8).
 *
 * {@link PARK_WINDOW_MS} is what lifts it without an operator: the streak is only a park while
 * its newest settlement is recent, so an hour of quiet lets a fixed machine be tried again and
 * a still-broken one re-parks after three more. A NEW POLICY VERSION clears it at once, because
 * the streak is read per `policy_version` — the operator's act of changing the governance is
 * also his way of saying "try again now".
 */
const PARK_AFTER = 3;
const PARK_WINDOW_MS = 60 * 60 * 1000;

/** Where the day's {@link CycleTally} is kept between wakes ({@link KeysSlice}). */
const TALLY_KEY = "conductor:tally";

/**
 * How many folders one tick asks the fleet about; the rest wait for the next tick. A machine
 * holds tens of workspaces and a first scan of a new fleet may present hundreds at once, and a
 * cycle is something a dispatch is waiting behind: 64 probes is a bounded second of it.
 */
const WORKSPACES_PER_TICK = 64;
/** One `?` per word of the hub's closed reason vocabulary, for the marker predicate below. */
const HUB_REASON_HOLES = MACHINE_REPOSITORY_REASONS.map(() => "?").join(", ");

// ---------------------------------------------------------------------------- ingestion tables

interface TableIngest {
  readonly table: string;
  readonly columns: readonly string[];
  /**
   * `ignore` is every table that records an act: a row is written once under its own id and a
   * second delivery of it is the same row. `upsert` is the two projections — a session's
   * catalog entry and a run — where a later observation completes an earlier one.
   */
  readonly conflict: "ignore" | "upsert";
  readonly key?: string;
}

const SESSION_COLUMNS = [
  "selector",
  "host",
  "harness",
  "source_id",
  "title",
  "title_provenance",
  "workspace",
  "repository_identity",
  "repository_remote",
  "repository_reason",
  "modified_at",
  "live",
  "kind",
  "size",
  "cost_usd",
  "total_tokens",
  "turns",
  "tool_errors",
  "content_digest",
  "snapshot_id",
  "archived_at",
  "seen_at",
] as const;

const RUN_COLUMNS = [
  "id",
  "kind",
  "machine_id",
  "job_id",
  "recipe_id",
  "profile",
  "authority_kind",
  "authority_id",
  "preparation",
  "started_at",
  "finished_at",
  "closure",
  "cost_usd",
  "tokens",
  "records",
  "payload",
] as const;

/** One output file, one table. */
const INGEST: Record<string, TableIngest> = {
  [JOB_OUTPUT_FILES.sessions]: {
    table: "sessions",
    columns: SESSION_COLUMNS,
    conflict: "upsert",
    key: "selector",
  },
  [JOB_OUTPUT_FILES.records]: {
    table: "records",
    columns: [
      "id",
      "kind",
      "root_id",
      "supersedes_id",
      "seq",
      "parent_id",
      "run_id",
      "recipe_id",
      "recipe_version",
      "actor_kind",
      "actor_id",
      "title",
      "created_at",
      "payload",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.edges]: {
    table: "edges",
    columns: [
      "id",
      "kind",
      "from_kind",
      "from_id",
      "to_kind",
      "to_id",
      "position",
      "note",
      "actor_kind",
      "actor_id",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.statusEvents]: {
    table: "status_events",
    columns: [
      "id",
      "record_id",
      "seq",
      "status",
      "run_id",
      "actor_kind",
      "actor_id",
      "reason",
      "recorded_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.assessments]: {
    table: "assessments",
    columns: [
      "id",
      "record_id",
      "revision_id",
      "run_id",
      "role",
      "vote",
      "lane",
      "claim_id",
      "supersedes_id",
      "payload",
      "recorded_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.filings]: {
    table: "filings",
    columns: [
      "id",
      "record_id",
      "entity_id",
      "rationale",
      "author_kind",
      "author_id",
      "heuristic",
      "withdrawn",
      "supersedes_id",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.questions]: {
    table: "questions",
    columns: [
      "id",
      "kind",
      "class",
      "text",
      "why",
      "dedupe_key",
      "raised_by_kind",
      "raised_by_id",
      "payload",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.plans]: {
    table: "plans",
    columns: [
      "id",
      "kind",
      "subject_kind",
      "subject_id",
      "operation",
      "dedupe_key",
      "payload",
      "proposed_by_kind",
      "proposed_by_id",
      "state",
      "ruled_by",
      "ruled_at",
      "ruling_reason",
      "result",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.steeringReplies]: {
    table: "steering",
    columns: [
      "id",
      "root_id",
      "reply_to_id",
      "seq",
      "actor_kind",
      "actor_id",
      "target_kind",
      "target_id",
      "text",
      "recorded_at",
    ],
    conflict: "ignore",
  },
};

/** Subjects before the rows that reference them; the receipt is read last, as it is written last. */
const INGEST_ORDER: readonly string[] = [
  JOB_OUTPUT_FILES.sessions,
  JOB_OUTPUT_FILES.records,
  JOB_OUTPUT_FILES.edges,
  JOB_OUTPUT_FILES.statusEvents,
  JOB_OUTPUT_FILES.assessments,
  JOB_OUTPUT_FILES.filings,
  JOB_OUTPUT_FILES.questions,
  JOB_OUTPUT_FILES.plans,
  JOB_OUTPUT_FILES.steeringReplies,
];

// ---------------------------------------------------------------------------- the tar reader

/**
 * The engine seals every bound output as a POSIX ustar archive (`agent/src/job-outputs.ts`:
 * regular files only, no extension records), so reading a run's files is reading its members.
 * Nothing here tolerates a path: a member is taken by its base name, which is the only thing
 * the file table is keyed by, and a member that is not a regular file is not a file.
 */
export interface TarMember {
  readonly name: string;
  readonly body: Uint8Array;
}

const TEXT = new TextDecoder();

function field(header: Uint8Array, at: number, length: number): string {
  let end = at;
  const limit = at + length;
  while (end < limit && header[end] !== 0) end += 1;
  return TEXT.decode(header.subarray(at, end)).trim();
}

export function tarMembers(bytes: Uint8Array): TarMember[] {
  const members: TarMember[] = [];
  for (let offset = 0; offset + 512 <= bytes.length; ) {
    const header = bytes.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const digits = field(header, 124, 12);
    const size = digits === "" ? 0 : Number.parseInt(digits, 8);
    if (!Number.isSafeInteger(size) || size < 0 || offset + 512 + size > bytes.length) {
      throw new Error(`sealed output is not a readable archive at byte ${String(offset)}`);
    }
    const name = field(header, 0, 100);
    const prefix = field(header, 345, 155);
    const type = header[156];
    offset += 512;
    if (type === 0x30 || type === 0x00) {
      const path = prefix === "" ? name : `${prefix}/${name}`;
      members.push({
        name: path.slice(path.lastIndexOf("/") + 1),
        body: bytes.subarray(offset, offset + size),
      });
    }
    offset += Math.ceil(size / 512) * 512;
  }
  return members;
}

/** Reads one sealed output whole, in the chunks the engine serves. */
async function readOutput(jobs: JobsSlice, node: OutputRef, bytes: number): Promise<Uint8Array> {
  if (bytes > MAX_OUTPUT_BYTES) {
    throw new Error(`output ${node.outputId} is ${String(bytes)} bytes, past what the hub ingests`);
  }
  const chunks: Buffer[] = [];
  let offset = 0;
  for (;;) {
    const chunk = await jobs.output({ node, offset, maxBytes: OUTPUT_CHUNK_BYTES });
    const decoded = Buffer.from(chunk.data, "base64");
    chunks.push(decoded);
    offset += decoded.byteLength;
    if (chunk.eof) break;
    if (decoded.byteLength === 0) {
      throw new Error(`output ${node.outputId} stopped short of its end`);
    }
    if (offset > MAX_OUTPUT_BYTES) {
      throw new Error(`output ${node.outputId} grew past the ingest limit`);
    }
  }
  return Buffer.concat(chunks);
}

// ---------------------------------------------------------------------------- row statements

/**
 * One row into one statement, naming only the columns the row actually carries — a scan that
 * observed no snapshot must not clobber the snapshot an archive recorded, and the way to
 * promise that is never to mention the column. A row carrying a column this table does not
 * have, or a value SQLite cannot hold, is not written at all: a machine half that changed shape
 * is a fault to see in the report, not a half-written row.
 */
function rowStatement(ingest: TableIngest, row: Record<string, unknown>): SqlStatement | null {
  const columns: string[] = [];
  const params: SqlParam[] = [];
  for (const column of ingest.columns) {
    if (!Object.hasOwn(row, column)) continue;
    const value = row[column];
    if (value === null || typeof value === "string" || typeof value === "number") {
      params.push(value);
    } else if (typeof value === "boolean") {
      params.push(value ? 1 : 0);
    } else {
      return null;
    }
    columns.push(column);
  }
  if (columns.length === 0) return null;
  for (const key of Object.keys(row)) if (!ingest.columns.includes(key)) return null;
  const names = columns.join(", ");
  const holes = columns.map(() => "?").join(", ");
  if (ingest.conflict === "ignore") {
    return { sql: `INSERT OR IGNORE INTO ${ingest.table}(${names}) VALUES (${holes})`, params };
  }
  const key = ingest.key ?? "id";
  const updates = columns
    .filter((column) => column !== key)
    .map((column) => `${column} = excluded.${column}`)
    .join(", ");
  return {
    sql:
      `INSERT INTO ${ingest.table}(${names}) VALUES (${holes}) ON CONFLICT(${key}) ` +
      (updates === "" ? "DO NOTHING" : `DO UPDATE SET ${updates}`),
    params,
  };
}

/** The run row a finished job leaves behind: the receipt as the machine wrote it, or its absence. */
function runStatement(
  runId: string,
  target: IngestTarget,
  receipt: Receipt | null,
  rows: Readonly<Record<string, number>>,
): SqlStatement {
  const closure = receipt?.closure ?? target.closure;
  const produced = receipt?.counts["records"] ?? rows[JOB_OUTPUT_FILES.records] ?? 0;
  const values: Record<string, SqlParam | undefined> = {
    id: runId,
    // THE OPERATION ID, never the receipt's word. `kind` is the operation the job ran as: it is
    // what `stop` builds a job node out of and what a surface labels a row by, and both of those
    // speak the engine's namespaced id. The receipt's `kind` is the machine half's own account
    // of which verb its binary ran, it stays inside the receipt payload below, and letting it
    // overwrite this column turned a finished `atyrode.babel.scan` into a bare `scan` — a node
    // no hub can address.
    kind: target.operationId,
    machine_id: target.machineId,
    job_id: target.jobId,
    recipe_id: receipt?.recipeId,
    profile: receipt?.profile === undefined ? undefined : JSON.stringify(receipt.profile),
    preparation:
      receipt?.preparation === undefined ? undefined : JSON.stringify(receipt.preparation),
    started_at: receipt?.startedAt ?? "",
    finished_at: receipt?.finishedAt ?? "",
    closure,
    // THE METER FIRST, the receipt second. `usage.inference` is what the owner's proxy counted
    // against the credential it holds; `receipt.costUsd` is what the engine said about its own
    // session. They answer the same question from two sides and the hub's own count is the one
    // a spend is reconciled against (#261) — a run in the local lane has no meter and keeps the
    // receipt's numbers, which is the only reason both are read here.
    //
    // A METERED RUN THAT SPENT NOTHING SETTLES AT ZERO, and that is the meter's word rather
    // than a hole: the owner attaches the block the moment a metered binding exists
    // (`agent/src/job-owner.ts` `prepareServiceProxies`), so a job whose engine reached a model
    // through some other credential records 0/0 here. What the hub counted against ITS
    // credential is the number a spend is reconciled against; the receipt still says what the
    // engine thinks it did.
    //
    // WHAT `tokens` MEANS IS THEREFORE THE LANE'S. Metered: the prompt and the answer the owner
    // counted, `inputTokens + outputTokens`, with cache reads excluded because they are priced
    // apart and summing them would overstate the turn. Local: the engine's own `total` for its
    // session, whatever it counted into that. The two are not comparable to the token, and the
    // block below is what lets a reader tell which one a row is — a row with `inference` is the
    // meter's, one without is the engine's.
    cost_usd:
      target.inference === null || target.inference === undefined
        ? (receipt?.costUsd ?? null)
        : target.inference.costMicros / 1_000_000,
    tokens:
      target.inference === null || target.inference === undefined
        ? (receipt?.tokens ?? null)
        : target.inference.inputTokens + target.inference.outputTokens,
    records: produced,
    // THE METER OUTLIVES THE FOLD. `run_progress` is dropped the moment a run settles, and with
    // it went the only record of how many calls were made and what the cache carried: two
    // columns cannot hold five numbers, and `usage.inference` is the hub's own account of the
    // whole run. So it is kept beside the machine's receipt, under a name of its own — the
    // receipt stays the run's word about itself, `inference` is the hub's word about it, and
    // neither is written into the other's fields. Which models answered is the receipt's
    // `models`: the meter's frames name one per call and `JobInferenceUsageSchema` keeps none.
    payload: JSON.stringify(
      target.inference === null || target.inference === undefined
        ? (receipt ?? { closure, reason: target.closure })
        : { ...(receipt ?? { closure, reason: target.closure }), inference: target.inference },
    ),
  };
  const columns = RUN_COLUMNS.filter((column) => values[column] !== undefined);
  const updates = columns.filter((column) => column !== "id").map((c) => `${c} = excluded.${c}`);
  return {
    sql:
      `INSERT INTO runs(${columns.join(", ")}) VALUES (${columns.map(() => "?").join(", ")}) ` +
      `ON CONFLICT(id) DO UPDATE SET ${updates.join(", ")}`,
    params: columns.map((column) => values[column] ?? null),
  };
}

// ---------------------------------------------------------------------------- ingestion

export interface IngestResult {
  readonly runId: string;
  readonly rows: Record<string, number>;
  readonly skipped: number;
  readonly receipt: Receipt | null;
  readonly notes: string[];
  /**
   * Every row the store would not write, with the producer's own refusal code. They are in the
   * notes too, as sentences; these are the same verdicts as data, because the loop counts them
   * by code and a surface groups them (#265), and reading a code back out of a sentence is how
   * a tally starts lying.
   */
  readonly refusals: RowRefusal[];
}

export interface IngestTarget {
  /**
   * The run row's id when the hub minted one (a requested job), null for a job nobody
   * requested — the scheduled beat, whose run id the machine half mints and puts in its
   * receipt, because a schedule's input is fixed at registration and cannot carry one.
   */
  readonly runId: string | null;
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly outputs: readonly JobOutput[];
  /** What the engine says became of the job, for a run whose receipt never arrived. */
  readonly closure: string;
  /**
   * What the OWNER metered for this job, or null when the hub has none — a machine whose lane
   * is not brokered, or a job that never reached a model. It is preferred over the receipt's
   * own numbers for the run row's `tokens` and `cost_usd`: the receipt is what the engine says
   * about itself, this is what the thing holding the credential counted (#261).
   */
  readonly inference?: InferenceUsage | null | undefined;
}

/**
 * Reads a finished job's sealed outputs and writes them into the store. Idempotent by
 * construction: every act is inserted under its own identifier and ignored when it is already
 * there, the catalog and the run are upserted to the same values, so a second ingestion of the
 * same outputs is a no-op the store cannot tell from the first.
 */
export async function ingestOutputs(
  store: BabelStore,
  jobs: JobsSlice,
  target: IngestTarget,
): Promise<IngestResult> {
  const files = new Map<string, unknown>();
  const notes: string[] = [];
  const refusals: RowRefusal[] = [];
  for (const output of target.outputs) {
    const node: OutputRef = {
      kind: "output",
      machineId: target.machineId,
      operationId: target.operationId,
      jobId: target.jobId,
      outputId: output.outputId,
    };
    const bytes = await readOutput(jobs, node, output.bytes);
    for (const member of tarMembers(bytes)) {
      if (INGEST[member.name] === undefined && member.name !== JOB_OUTPUT_FILES.receipt) {
        notes.push(`${output.name}/${member.name} is not a file this hub ingests`);
        continue;
      }
      try {
        files.set(member.name, JSON.parse(TEXT.decode(member.body)));
      } catch {
        notes.push(`${member.name} is not JSON`);
      }
    }
  }

  const rows: Record<string, number> = {};
  let skipped = 0;
  const statements: SqlStatement[] = [];
  for (const file of INGEST_ORDER) {
    const ingest = INGEST[file];
    const document = files.get(file);
    if (ingest === undefined || document === undefined) continue;
    if (!Array.isArray(document)) {
      notes.push(`${file} is not a list of rows`);
      continue;
    }
    let written = 0;
    for (const row of document) {
      const shaped = typeof row === "object" && row !== null && !Array.isArray(row) ? (row as Record<string, unknown>) : null;
      // What a machine half wrote is accepted under the contract it was prompted with: the
      // submission validator the engine's schema is generated from is the one that runs here, so
      // a producer and a store cannot disagree about one payload (#263, post-mortem F8). The
      // run's own closure and cost are the receipt's and settle the claim either way.
      const refused = shaped === null ? null : refuseRow(ingest.table, shaped);
      if (refused !== null) {
        notes.push(`${file}: ${refused.code}: ${refused.message}`);
        refusals.push(refused);
      }
      const statement = shaped === null || refused !== null ? null : rowStatement(ingest, shaped);
      if (statement === null) {
        skipped += 1;
        continue;
      }
      statements.push(statement);
      written += 1;
    }
    rows[file] = written;
  }

  const document = files.get(JOB_OUTPUT_FILES.receipt);
  const parsed = document === undefined ? null : ReceiptSchema.safeParse(document);
  if (parsed !== null && !parsed.success) {
    notes.push(`${JOB_OUTPUT_FILES.receipt} is not a receipt`);
  }
  const receipt = parsed !== null && parsed.success ? parsed.data : null;
  if (receipt !== null && target.runId !== null && receipt.runId !== target.runId) {
    notes.push(`the receipt calls this run ${receipt.runId}, the hub asked for ${target.runId}`);
  }
  const runId = target.runId ?? receipt?.runId ?? `run_${target.jobId}`;

  statements.push(runStatement(runId, target, receipt, rows));
  for (let at = 0; at < statements.length; at += MAX_BATCH_STATEMENTS) {
    await store.db.batch(statements.slice(at, at + MAX_BATCH_STATEMENTS));
  }
  store.touch();
  return { runId, rows, skipped, receipt, notes, refusals };
}

// ---------------------------------------------------------------------------- the loop

/**
 * One run the loop is waiting on. `container_id` is the fork in the road: null is a job of
 * Babel's own, polled through `ctx.jobs`; non-null is a CODE SESSION, whose job belongs to
 * another plugin and is reconciled through `code.readSession` (#279).
 */
type PendingRun = {
  id: string;
  job_id: string;
  machine_id: string;
  kind: string;
  container_id: string | null;
  prepare_job_id: string | null;
  started_at: string;
  profile: string | null;
  preparation: string | null;
  unreadable: number | bigint;
};
/**
 * The `run_progress` row the fold carries between cycles. Every INTEGER column arrives as a
 * bigint from the engine's database (#536), so each one is `Number(...)`-ed the moment it is
 * read and nothing here ever compares a raw column against a number.
 */
type RunProgressRow = {
  stage: string;
  message: string;
  fraction: number | null;
  since: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_tokens: number;
  cost_usd: number;
  last_model: string;
  last_call_at: string;
  seq: number;
};
/** A claim row as SQLite hands it back: `fence` is an INTEGER column and the engine's own
 *  database answers those as bigints, so it is carried as the coordinator's {@link Fence} and
 *  normalized there rather than compared against a number here. */
type OpenClaim = { id: string; run_id: string; fence: Fence; reserved_cost: number };
/** One open claim and what the runs table knows about the job behind it, for the reaper. */
type OrphanClaim = {
  id: string;
  fence: Fence;
  job_id: string | null;
  granted_at: string;
  /** Two `COUNT(*)` subqueries, so bigints from the engine's database, as {@link Fence} is. */
  runs: number | bigint;
  open_runs: number | bigint;
  /** The run row's own consecutive-silence count; NULL when no run row stands behind it. */
  silent: number | bigint | null;
};
type MachineCount = { machineId: string; cited: number };
type MachineRow = { machineId: string };
type Existing = { id: string };
/** One folder of one machine that no hub-side answer has been written for yet. */
type UnidentifiedFolder = { machineId: string; workspace: string };
/**
 * One settled claim and what the job behind it is known to have done, for the park heuristic.
 * The claim says how it closed; the run row says whether a model ever answered — its cost, its
 * tokens, or a receipt reason that carries a refusal code, which only a submission can earn.
 */
type ClosedClaim = {
  outcome: string | null;
  finished_at: string;
  cost: number | null;
  tokens: number | null;
  payload: string | null;
};

/** A tally under construction: one map per vocabulary, counted up as the cycle learns things. */
type Counter = Map<string, number>;

/** One more of whatever this is, whether the cycle has seen one before or not. */
function count(counter: Counter, key: string): void {
  counter.set(key, (counter.get(key) ?? 0) + 1);
}

/** The counted map as the report carries it, in reason order so two reports compare. */
function tallied(counter: Counter): Record<string, number> {
  const out: Record<string, number> = {};
  for (const key of [...counter.keys()].sort()) out[key] = counter.get(key) ?? 0;
  return out;
}

/**
 * The refusal code a run's receipt carries, or null when no submission was refused.
 *
 * The receipt's `reason` is written by `machine/evaluate.ts` as `refusalReason` spells it, and
 * read back here through the same module's `refusalCode`, which matches the closed vocabulary
 * — so an engine failure written in the same shape (`launch: …`) is not counted as a refusal.
 */
function receiptRefusal(payload: string | null): RefusalCode | null {
  if (payload === null) return null;
  let reason: unknown;
  try {
    reason = (JSON.parse(payload) as Record<string, unknown>)["reason"];
  } catch {
    return null;
  }
  return typeof reason === "string" ? refusalCode(reason) : null;
}

export function conductor(deps: ConductorDeps): Conductor {
  const { store, coordinator, jobs, machines, keys, plan, engine } = deps;
  let cycle = 0;
  /**
   * HOW MANY CYCLES IN A ROW NOBODY COULD SAY WHERE A RUN'S JOB IS, written on the run row.
   *
   * It was a `Map` in this closure, and the closure was the bug: `server.ts` builds a NEW
   * conductor for every wake, so a counter whose whole predicate is "and the cycle before
   * this one" was reset before it could ever be read a second time and the reaper's bound
   * could not fire. A run that answers is set back to zero, which is what makes the count
   * CONSECUTIVE rather than cumulative.
   */
  async function silence(runId: string, held: number, seen: boolean): Promise<number> {
    const next = seen ? 0 : held + 1;
    if (next !== held) await store.db.run(`UPDATE runs SET unreadable = ? WHERE id = ?`, [next, runId]);
    return next;
  }

  /** Machines are described once per tick: the answer is the same for every draw in it. */
  async function readiness(
    seen: Map<string, MachineReadiness | null>,
    machineId: string,
  ): Promise<MachineReadiness | null> {
    const known = seen.get(machineId);
    if (known !== undefined) return known;
    let described: MachineReadiness | null = null;
    try {
      described = await jobs.describe({ machineId, pluginId: BABEL_PLUGIN_ID });
    } catch {
      described = null;
    }
    seen.set(machineId, described);
    return described;
  }

  /** Every machine Babel has evidence of: one that holds sessions, or one that has run for it. */
  async function knownMachines(): Promise<string[]> {
    const rows = await store.db.query<MachineRow>(
      `SELECT DISTINCT host AS machineId FROM sessions WHERE host <> ''
       UNION
       SELECT DISTINCT machine_id AS machineId FROM runs
        WHERE machine_id IS NOT NULL AND machine_id <> ''
       ORDER BY machineId`,
    );
    return rows.map((row) => row.machineId);
  }

  /** Whether a described machine can run one operation right now: connected, this plugin
   *  installed, enabled and ready, and the operation itself not reported unready. */
  function usable(
    described: MachineReadiness | null,
    operationId: string,
  ): described is MachineReadiness {
    if (described === null || !described.connected) return false;
    const installed = described.installation;
    if (installed === null || !installed.enabled || !installed.ready) return false;
    return described.operations?.[operationId]?.ready !== false;
  }

  /**
   * Where the work belongs: the machine holding the sessions the record cites, because that is
   * where the evidence can be read, and the one that holds most of them first. Failing that, any
   * enrolled machine that is online with the operation ready — a review of a record whose
   * sessions sit on an offline machine is still a review Babel can perform, it just reads what
   * the hub already holds.
   *
   * `free` is the per-machine bound (#260), and it is applied HERE as well as at admission: a
   * coordinator that admits a draw because the fleet has a slot, dispatched by a loop that
   * always prefers the machine citing the evidence, would put every job of a two-machine
   * deployment on one host. The beat asks nothing of it and passes nothing.
   */
  async function machineFor(
    seen: Map<string, MachineReadiness | null>,
    operationId: string,
    recordId: string,
    free?: (machineId: string) => boolean,
  ): Promise<{ machineId: string; readiness: MachineReadiness } | null> {
    const order: string[] = [];
    if (recordId !== "") {
      const preferred = await store.db.query<MachineCount>(
        `SELECT s.host AS machineId, COUNT(*) AS cited
           FROM edges e JOIN sessions s ON s.selector = e.to_id
          WHERE e.from_id = ? AND e.kind = 'cites' AND e.to_kind = 'session'
          GROUP BY s.host
          ORDER BY cited DESC, s.host`,
        [recordId],
      );
      for (const row of preferred) order.push(row.machineId);
    }
    for (const machineId of await knownMachines()) {
      if (!order.includes(machineId)) order.push(machineId);
    }
    for (const machineId of order) {
      if (free !== undefined && !free(machineId)) continue;
      const described = await readiness(seen, machineId);
      if (!usable(described, operationId)) continue;
      return { machineId, readiness: described };
    }
    return null;
  }

  /**
   * The one schedule the loop keeps: the policy's cadence, on a machine that can run the beat.
   *
   * A HOST THAT WILL NOT REGISTER IT IS NOT A REASON TO STOP. The three schedule verbs are the
   * only ones a cycle can be refused for structurally rather than for this policy's sake — a
   * hardened server half reaches none of them (`ISOLATE_CTX_METHODS` serves no `jobs.schedule`),
   * and an authority that has lapsed refuses the other two — so the refusal is recorded as a
   * note and the cycle carries on. The beat is one WAKE; ingesting what has already finished
   * and drawing what the policy allows are the work, and they do not need it.
   */
  async function reconcileSchedule(
    policy: Policy,
    at: number,
    notes: string[],
  ): Promise<ScheduleState> {
    let listed: readonly ScheduleRow[];
    try {
      listed = await jobs.schedules();
    } catch (error) {
      notes.push(`the beat's schedule cannot be read: ${message(error)}`);
      return "absent";
    }
    const registered = listed.filter((row) => row.scheduleId === CONDUCTOR_SCHEDULE_ID);
    if (!policy.enabled) {
      try {
        for (const row of registered) {
          await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
        }
      } catch (error) {
        notes.push(`the beat cannot be unregistered: ${message(error)}`);
        return "kept";
      }
      return registered.length === 0 ? "absent" : "unregistered";
    }
    const intervalMs = Math.max(1, policy.cadenceSeconds) * 1000;
    const current = registered.find(
      (row) =>
        row.revision === policy.version &&
        row.intervalMs === intervalMs &&
        row.expiresAt - at > intervalMs,
    );
    if (current !== undefined) return "kept";
    const host = await machineFor(new Map(), BEAT_OPERATION, "");
    if (host === null) return registered.length === 0 ? "absent" : "kept";
    try {
      for (const row of registered) {
        await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
      }
    } catch (error) {
      notes.push(`the beat cannot be re-registered: ${message(error)}`);
      return "kept";
    }
    const installation = host.readiness.installation;
    try {
      await jobs.schedule({
        jobId: `${CONDUCTOR_SCHEDULE_ID}.${policy.version}`,
        machineId: host.machineId,
        operationId: BEAT_OPERATION,
        // The beat's input is fixed at registration, so it carries no run id: the machine half
        // mints one per occurrence and the receipt is what names it.
        input: {
          [INPUT_FIELD]: JSON.stringify({
            runId: "",
            machineId: host.machineId,
            roots: [],
            harnesses: [],
          }),
        },
        outputs: [
          { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [BEAT_OPERATION] },
        ],
        limits: plan.limits,
        ...(installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            }),
        scheduleId: CONDUCTOR_SCHEDULE_ID,
        revision: policy.version,
        firstNominalAt: at + intervalMs,
        intervalMs,
        deadlineMs: intervalMs,
        expiresAt: at + SCHEDULE_LIFETIME_MS,
        offlinePolicy: "coalesce-one",
      });
    } catch (error) {
      notes.push(`the beat cannot be registered: ${message(error)}`);
      return "absent";
    }
    return "registered";
  }

  /**
   * One claim released because the job behind it is gone, as the cycle's own report row.
   *
   * Every path that discovers a dead job comes through here — a settlement with no receipt, a
   * posting the machine refused, the reaper — so "a claim dies with its job" is one sentence of
   * accounting written once. A refusal is reported rather than thrown: the claim moved on under
   * a later fence, which is somebody else's live work and not this cycle's to close.
   */
  async function release(claim: { id: string; fence: Fence }, reason: string): Promise<SettledClaim> {
    const abandoned = await coordinator.abandon({ id: claim.id, fence: claim.fence, reason });
    return {
      claimId: claim.id,
      outcome: "abandoned",
      cost: abandoned.outcome === "abandoned" ? abandoned.cost : 0,
      overrun: false,
      refused: abandoned.outcome === "abandoned" ? null : abandoned.refusal.reason,
      reason,
    };
  }

  /**
   * Ingests one finished job, settles whatever claim authorized it, and counts what the
   * contract refused: the store's verdict on every row it would not write, and the receipt's
   * own reason when the model answered and the submission did not stand. A refusal is SPEND,
   * so it is counted here — where the receipt is — and never inferred later from a failure.
   */
  async function settle(
    at: number,
    target: IngestTarget,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
    refusals: Counter,
  ): Promise<void> {
    let result: IngestResult | null = null;
    try {
      result = await ingestOutputs(store, jobs, target);
    } catch (error) {
      // An output the hub cannot read closes its run as failed rather than being retried every
      // cycle for ever: the run row is the loop's memory of what it has already dealt with, and
      // a job nobody requested (the beat) has none until this writes one.
      notes.push(`job ${target.jobId} outputs were refused: ${message(error)}`);
      const failed = JSON.stringify({ closure: "failed", reason: message(error) });
      await store.db.run(
        `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure, records, payload)
         VALUES (?, ?, ?, ?, ?, ?, 'failed', 0, ?)
         ON CONFLICT(id) DO UPDATE SET closure = 'failed', finished_at = excluded.finished_at,
                                       payload = excluded.payload`,
        [
          target.runId ?? `run_${target.jobId}`,
          target.operationId,
          target.machineId,
          target.jobId,
          new Date(at).toISOString(),
          new Date(at).toISOString(),
          failed,
        ],
      );
    }
    for (const note of result?.notes ?? []) notes.push(`${target.jobId}: ${note}`);
    for (const refused of result?.refusals ?? []) count(refusals, refused.code);
    const receipt = result?.receipt ?? null;
    // A receipt whose reason names a refusal code is a model that answered and a submission
    // that did not stand: the code is the one `results.ts` names, and it is spend.
    const refusedSubmission = receipt?.reason === undefined ? null : refusalCode(receipt.reason);
    if (refusedSubmission !== null) count(refusals, refusedSubmission);
    ingested.push({
      runId: result?.runId ?? target.runId ?? `run_${target.jobId}`,
      jobId: target.jobId,
      closure: receipt?.closure ?? target.closure,
      costUsd: receipt?.costUsd ?? 0,
      rows: result?.rows ?? {},
      skipped: result?.skipped ?? 0,
    });

    // WHAT A CLAIM IS WORTH WHEN ITS JOB IS OVER, in two cases that look alike and are not.
    //
    // A job that ran to the end and exited cleanly told us what it spent, receipt or no
    // receipt: nothing, if it wrote none. Its claim is FINISHED at that cost, and a cycle that
    // produced nothing costs the deployment nothing.
    //
    // A job that was killed, interrupted, refused or failed mid-flight told us nothing at all.
    // It may have spent every cent of its reservation at the model before it died, and there is
    // no receipt to ask. Its claim is ABANDONED — released, because the worker is not coming
    // back and its batch slot belongs to the next draw; and charged at the reservation, because
    // releasing a crash at zero is how a crash loop spends the day's allowance many times over
    // (F3, and the 86-minute ghosts of 2026-09-13).
    const died = receipt === null && target.closure !== "completed";
    if (died) {
      const open = await store.db.query<OpenClaim>(
        `SELECT id, run_id, fence, reserved_cost FROM claims WHERE job_id = ? AND finished_at IS NULL`,
        [target.jobId],
      );
      for (const claim of open) {
        settled.push(
          await release(
            claim,
            `job ${target.jobId} closed as ${target.closure} and wrote no receipt`,
          ),
        );
      }
      return;
    }
    await settleClaims(
      target.jobId,
      receipt?.costUsd ?? 0,
      receipt?.closure === "completed"
        ? "completed"
        : receipt?.closure === "skipped"
          ? "skipped"
          : "failed",
      settled,
    );
  }

  /**
   * Closes the open claims of a run this loop settled without an `ingestOutputs` pass, at the
   * cost the receipt records. It is `settle`'s second half, lifted out because the Code lane
   * has no sealed output to read and the accounting is identical once the cost is known.
   */
  async function settleClaims(
    jobId: string,
    cost: number,
    outcome: "completed" | "failed" | "skipped",
    settled: SettledClaim[],
  ): Promise<void> {
    const open = await store.db.query<OpenClaim>(
      `SELECT id, run_id, fence, reserved_cost FROM claims WHERE job_id = ? AND finished_at IS NULL`,
      [jobId],
    );
    for (const claim of open) {
      const finished = await coordinator.finish({
        id: claim.id,
        runId: claim.run_id,
        fence: claim.fence,
        cost,
        outcome,
      });
      settled.push(
        finished.outcome === "finished"
          ? {
              claimId: claim.id,
              outcome,
              cost: finished.cost,
              overrun: finished.overrun,
              refused: null,
              reason: null,
            }
          : {
              claimId: claim.id,
              outcome,
              cost,
              overrun: false,
              refused: finished.refusal.reason,
              reason: null,
            },
      );
    }
  }

  /**
   * THE SELECTION A RUN WAS SERVED, read off the `prepare` job's own receipt.
   *
   * `prepare` writes its {@link MaterialIndex} into the sealed material AND into its receipt,
   * and the receipt is what this hub already ingested into the `runs` row. So verifying a
   * citation costs ONE query against a row, rather than pulling a sealed archive of every
   * selected session's records back through the hub to read the twenty lines at the front of
   * it — which is the whole economy this lane was rebuilt for (post-mortem F1).
   */
  async function materialOf(prepareJobId: string | null): Promise<MaterialIndex | null> {
    if (prepareJobId === null || prepareJobId === "") return null;
    const rows = await store.db.query<{ payload: string }>(
      `SELECT payload FROM runs WHERE job_id = ? AND closure IS NOT NULL LIMIT 1`,
      [prepareJobId],
    );
    const payload = rows[0]?.payload;
    if (payload === undefined) return null;
    let held: unknown;
    try {
      held = (JSON.parse(payload) as Record<string, unknown>)["material"];
    } catch {
      return null;
    }
    const parsed = MaterialIndexSchema.safeParse(held);
    return parsed.success ? parsed.data : null;
  }

  /** The account a launch NAMED, kept on the run row so a drain's total can be attributed. */
  function namedAccount(profile: string | null): Receipt["account"] {
    if (profile === null || profile === "") return undefined;
    let held: unknown;
    try {
      held = (JSON.parse(profile) as Record<string, unknown>)["account"];
    } catch {
      return undefined;
    }
    if (typeof held !== "object" || held === null) return undefined;
    const account = held as Record<string, unknown>;
    const provider = account["provider"];
    const identityKey = account["identityKey"];
    if (typeof provider !== "string" || typeof identityKey !== "string") return undefined;
    return { provider, identityKey };
  }

  /** The run row's own preparation blob, as the receipt carries it back unchanged. */
  function preparationOf(preparation: string | null): Receipt["preparation"] {
    if (preparation === null || preparation === "") return undefined;
    let held: unknown;
    try {
      held = JSON.parse(preparation);
    } catch {
      return undefined;
    }
    return typeof held === "object" && held !== null && !Array.isArray(held)
      ? (held as Record<string, unknown>)
      : undefined;
  }

  /**
   * ONE FINISHED CODE SESSION, TURNED INTO A RECEIPT AND A SETTLED CLAIM (#279).
   *
   * The transcript is Code's; what Babel owns is the CONTRACT the prompt stated, and this is
   * where it is enforced: the answer is the last fenced block of the final message
   * ({@link readExploreAnswer}), and every locator it cites must name a file the material's
   * index served at the digest the index recorded ({@link unservedLocator}).
   *
   * A REFUSED SUBMISSION IS SPEND, and that is the sentence the whole function is arranged
   * around. The model answered; the deployment paid for it; the answer did not stand. So the
   * receipt is written WITH the cost and the refusal's own code, the claim is FINISHED rather
   * than abandoned, and the tally counts the refusal by code — a drain that read a refusal as a
   * free failure would relaunch against a burn rate that never happened, and the park heuristic
   * would read a recipe's problem as a broken lane (post-mortem F8, F16).
   */
  async function settleSession(
    at: number,
    run: PendingRun,
    read: SessionRead,
    closure: Receipt["closure"],
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
    refusals: Counter,
  ): Promise<void> {
    /*
      A RECEIPT ONLY EXISTS FOR A JOB THAT EXITED 0 AND SEALED ITS TRANSCRIPT. Code answers
      `session: null` for a job it cancelled, interrupted or that exited non-zero, and that is
      a successful READ of a run that produced nothing to read — not a fault, and not a run
      whose spend is knowable. It settles at zero with the closure the job itself reported,
      because a receipt is the only thing that could have said what it cost.
    */
    const session = read.session;
    // ONE CALL, because a Code session posted by `runSession` is omp's one-shot: `usage` is the
    // whole run's, there is no per-call frame to count, and writing `calls: 0` beside real
    // tokens would make a metered run read as one that never reached a model.
    const usage = session?.usage ?? null;
    const inference: InferenceUsage | null =
      usage === null
        ? null
        : {
            calls: 1,
            inputTokens: usage.input,
            outputTokens: usage.output,
            cachedInputTokens: usage.cacheRead,
            costMicros: Math.round((usage.cost ?? 0) * 1_000_000),
          };
    const costUsd = usage?.cost ?? 0;

    // WHAT THE ANSWER WAS WORTH. A session that never sealed a transcript submitted nothing,
    // and the reason says which of the job's own endings that was rather than inventing a
    // schema refusal about a message that was never written.
    let reason = "";
    if (session === null) {
      reason =
        `${REFUSALS.empty}: the session closed as ${read.job.state} and sealed no transcript, ` +
        `so it submitted no result`;
    } else if (session.exitCode !== 0) {
      reason = `${REFUSALS.schema}: the session exited ${String(session.exitCode)} and submitted no result`;
    } else {
      const answer = readExploreAnswer("explore", session.finalMessage);
      if ("refusal" in answer) {
        reason = refusalReason(answer.refusal);
      } else {
        const material = await materialOf(run.prepare_job_id);
        const served: readonly MaterialEntry[] = material?.sessions ?? [];
        const unserved = unservedLocator(answer.result, served);
        if (unserved !== "") {
          reason = `${REFUSALS.unknownReference}: ${unserved}`;
        } else if (material === null) {
          // The selection is how a claim is checkable at all: a result admitted against a
          // material nobody can read is an unverifiable claim recorded as a verified one.
          reason =
            `${REFUSALS.unknownReference}: the material of prepare job ` +
            `${run.prepare_job_id ?? "(none)"} is not on any settled run of this hub, so this ` +
            `run's citations cannot be checked against what it was served`;
        }
      }
    }
    const refusedCode = reason === "" ? null : refusalCode(reason);
    if (refusedCode !== null) count(refusals, refusedCode);

    const receipt: Receipt = {
      runId: run.id,
      kind: "explore",
      machineId: run.machine_id,
      ...(namedAccount(run.profile) === undefined ? {} : { account: namedAccount(run.profile) }),
      ...(session === null ? {} : { model: session.model }),
      ...(preparationOf(run.preparation) === undefined
        ? {}
        : { preparation: preparationOf(run.preparation) }),
      startedAt: run.started_at,
      finishedAt: new Date(at).toISOString(),
      // A CLOSURE THE JOB REPORTED, not one inferred from the reason: a session an operator
      // cancelled is `stopped` and not `failed`, and a panel that called every unreadable run
      // a failure is how a stop looks like a fault.
      closure: reason === "" ? "completed" : session === null ? closure : "failed",
      ...(reason === "" ? {} : { reason }),
      costUsd,
      tokens: usage === null ? 0 : usage.input + usage.output,
      ...(session === null ? {} : { models: [session.model] }),
      counts: {},
    };
    // The run row is written through the SAME statement an ingested job's is: one shape for
    // what a finished run looks like, whoever posted the job. `outputs` is empty because the
    // rows a result becomes are not in a sealed lease here — the transcript is Code's job's
    // `session` output, read on demand — and `inference` is what the meter said.
    await store.db.batch([
      runStatement(
        run.id,
        {
          runId: run.id,
          jobId: run.job_id,
          machineId: run.machine_id,
          operationId: run.kind,
          outputs: [],
          closure: receipt.closure,
          inference,
        },
        receipt,
        {},
      ),
    ]);
    await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
    store.touch();
    if (reason !== "") notes.push(`run ${run.id}: ${reason}`);
    ingested.push({
      runId: run.id,
      jobId: run.job_id,
      closure: receipt.closure,
      costUsd,
      rows: {},
      skipped: 0,
    });
    /*
      WHAT THE CLAIM IS WORTH. A refused SUBMISSION is `failed` and costs what the model was
      paid; a session that sealed no transcript at all is `skipped` — nobody answered, nothing
      was submitted, and calling it a failure would feed the park heuristic a streak that is
      really an operator pressing Stop.
    */
    await settleClaims(
      run.job_id,
      costUsd,
      reason === "" ? "completed" : session === null ? "skipped" : "failed",
      settled,
    );
  }

  /**
   * WHERE ONE CODE SESSION IS, ASKED OF CODE, and what this cycle does about the answer.
   *
   * Three outcomes and no fourth: the job is still going and the run stays open; the job is
   * over and {@link settleSession} closes it; or CODE REFUSED THE READ — a profile that moved,
   * a consent that lapsed, a Code that is no longer installed.
   *
   * THE REFUSAL IS BOUNDED THE WAY THE REAPER BOUNDS AN UNREADABLE JOB, and by the same counter.
   * One refusal is a hiccup and the note says so; {@link UNREPORTED_CYCLES} in a row is a run
   * nobody will ever be able to read, and retrying it on every wake for ever is how a dead run
   * holds a batch slot and a panel row until someone notices. So the sentence is recorded ON THE
   * RUN as its note while it is still hoped for, and at the bound the run is closed as failed
   * with that sentence and its claim released.
   */
  async function reconcileSession(
    at: number,
    run: PendingRun,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
    refusals: Counter,
  ): Promise<{ readonly inFlight: boolean }> {
    const containerId = run.container_id ?? "";
    const answered = await engine.readSession({ containerId, jobId: run.job_id });
    if (!answered.ok) {
      const silent = await silence(run.id, Number(run.unreadable), false);
      const note = `session ${run.job_id} in ${containerId} cannot be read: ${answered.refused}`;
      notes.push(note);
      if (silent < UNREPORTED_CYCLES) {
        // Still hoped for: the sentence is on the row so a reader sees it without the journal,
        // and the run stays open for the next wake to ask again.
        await store.db.run(`UPDATE runs SET payload = ? WHERE id = ?`, [
          JSON.stringify({ closure: null, note }),
          run.id,
        ]);
        store.touch();
        return { inFlight: true };
      }
      await store.db.run(
        `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
        [new Date(at).toISOString(), JSON.stringify({ closure: "failed", reason: note }), run.id],
      );
      await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
      store.touch();
      for (const claim of await store.db.query<OpenClaim>(
        `SELECT id, run_id, fence, reserved_cost FROM claims WHERE job_id = ? AND finished_at IS NULL`,
        [run.job_id],
      )) {
        settled.push(await release(claim, note));
      }
      return { inFlight: false };
    }
    await silence(run.id, Number(run.unreadable), true);
    /*
      CODE ANSWERS FOR A JOB IN ANY STATE, and the vocabulary is the hub's own. A job that is
      not terminal is still going, and this loop folds no progress for it — the replay ring
      belongs to `atyrode.omp`'s job and `ctx.jobs.follow` on it is not Babel's to open.

      A TERMINAL ONE CARRIES ITS OWN ENDING, which is what the receipt records when Code
      sealed no transcript: `cancelled` is an operator's Stop and closes `stopped`, anything
      else is `failed`. Reading that off the job rather than off the absent receipt is the
      difference between a stop that looks like a stop and one that looks like a fault.
    */
    const job = answered.value.job;
    if (TERMINAL_STATES[job.state] !== true) return { inFlight: true };
    const closure: Receipt["closure"] = job.state === "cancelled" ? "stopped" : "failed";
    await settleSession(at, run, answered.value, closure, ingested, settled, notes, refusals);
    return { inFlight: false };
  }

  /**
   * WHAT ONE RUNNING JOB HAS BEEN DOING SINCE THE LAST CYCLE, folded into its `run_progress` row.
   *
   * THE READ IS `follow`, TAKEN AS A SNAPSHOT AND CLOSED IN THE SAME TURN. A running job's
   * journal cannot be read at all — the hub refuses it `job_unfinished`, because watching a
   * live job is what `follow` is for — and the snapshot `follow` answers with is the replay
   * ring the hub retains for every job whether or not anyone is watching. So this holds no live
   * stream per in-flight run, which was the whole objection to `follow`: what it opens, it
   * closes before it writes a row.
   *
   * A CYCLE A SETTLEMENT WOKE FOLDS NOTHING, because a hook is served no `follow`. It leaves
   * the row as the last dispatch-woken cycle wrote it, reports that row unchanged, and says
   * nothing: a stage nobody read is not a stage that moved.
   *
   * The ring is read from the sequence this row last folded, so a cycle counts what it has not
   * seen and nothing more. Two frames matter and every other one is stepped over: the newest
   * `job_progress` is where the job says it is, and every `inference_call` above that sequence
   * is added to the running spend.
   *
   * THE SPEND HERE IS A RUNNING BEST EFFORT, not the account. The ring holds at most
   * `MAX_JOB_FOLLOW_EVENTS` frames of EVERY kind, byte frames included, so a job that outran
   * the loop has a prefix nobody folded — which the note names once, in the hub's own terms.
   * `usage.inference` at settle is the meter's own total and overwrites this, so the number an
   * operator reads while a drain runs is honest about the direction and the receipt is honest
   * about the amount.
   */
  async function foldRun(
    at: number,
    run: PendingRun,
    notes: string[],
  ): Promise<{ stage: string; stalled: boolean }> {
    const held = (
      await store.db.query<RunProgressRow & { stalled: number | bigint }>(
        `SELECT stage, message, fraction, since, calls, input_tokens, output_tokens, cache_tokens,
                cost_usd, last_model, last_call_at, seq, stalled
           FROM run_progress WHERE run_id = ?`,
        [run.id],
      )
    )[0];
    /** The row as it stands: what a cycle that cannot read this job reports, unchanged. */
    const standing = { stage: held?.stage ?? "", stalled: Number(held?.stalled ?? 0) === 1 };
    // Bound, because the verb is read off the slice before it is called: the kit's slice and
    // the host's are objects of closures, a fake's is a class whose method wants its receiver,
    // and the loop's business is which of them HAS the verb rather than how it was written.
    const watch = jobs.follow?.bind(jobs);
    if (watch === undefined) return standing;
    const folded: RunProgressRow = {
      stage: held?.stage ?? "",
      message: held?.message ?? "",
      fraction: held?.fraction ?? null,
      since: held?.since ?? "",
      calls: Number(held?.calls ?? 0),
      input_tokens: Number(held?.input_tokens ?? 0),
      output_tokens: Number(held?.output_tokens ?? 0),
      cache_tokens: Number(held?.cache_tokens ?? 0),
      cost_usd: Number(held?.cost_usd ?? 0),
      last_model: held?.last_model ?? "",
      last_call_at: held?.last_call_at ?? "",
      seq: Number(held?.seq ?? 0),
    };

    let read: FollowSnapshot;
    try {
      const watching = await watch(
        { kind: "job", machineId: run.machine_id, operationId: run.kind, jobId: run.job_id },
        // Nothing is ever delivered here: the subscription exists for its snapshot and is gone
        // before the hub's next frame. What arrives after it is in the ring for the next cycle.
        () => {},
      );
      try {
        read = watching.snapshot;
      } finally {
        await watching.close();
      }
    } catch (error) {
      notes.push(`job ${run.job_id} cannot be followed: ${message(error)}`);
      return standing;
    }

    // WHAT THE RING NO LONGER HOLDS, in the hub's own two shapes: `unavailable` is the span it
    // says it cannot replay — a gap, a byte limit, a ring thrown away whole — and `firstSeq` is
    // the oldest frame still in it. Either one above the sequence this row folded is progress
    // and spend nobody will ever see. It is said once per fold, and it is a real loss rather
    // than the journal's contractual holes: this ring carries the byte frames too.
    const lostThrough = Math.max(read.unavailable?.toSeq ?? 0, (read.firstSeq ?? 0) - 1);
    if (lostThrough > folded.seq) {
      notes.push(
        `job ${run.job_id}: progress before seq ${String(lostThrough + 1)} was not retained; ` +
          `its spend so far is short by what those frames carried`,
      );
    }
    for (const entry of read.events) {
      if (entry.seq <= folded.seq) continue;
      folded.seq = entry.seq;
      if (entry.event.type === "job_progress") {
        const frame = ProgressFrameSchema.safeParse(entry.event);
        if (!frame.success) continue;
        // A stage that did not change keeps its `since`: the row says how long the job has been
        // WHERE IT IS, and restamping it on every repeat of the same word would turn "at the
        // model since 11 minutes" into "at the model since 4 seconds" for ever. At the model it
        // is the stage AND its message, because there the message is which prompt is out — the
        // owner coalesces newest-wins every five seconds and composing the next prompt is
        // sub-second, so the word alone would read one turn's clock across three of them.
        const moved =
          frame.data.stage !== folded.stage ||
          (frame.data.stage === RUN_STAGES.atModel && (frame.data.message ?? "") !== folded.message);
        if (moved) {
          folded.stage = frame.data.stage;
          folded.since = new Date(frame.data.at).toISOString();
        }
        folded.message = frame.data.message ?? "";
        folded.fraction = frame.data.fraction ?? null;
        continue;
      }
      if (entry.event.type !== "inference_call") continue;
      const frame = InferenceFrameSchema.safeParse(entry.event);
      if (!frame.success) continue;
      folded.calls += 1;
      folded.input_tokens += frame.data.inputTokens;
      folded.output_tokens += frame.data.outputTokens;
      folded.cache_tokens += frame.data.cachedInputTokens;
      folded.cost_usd += frame.data.costMicros / 1_000_000;
      folded.last_model = frame.data.model;
      // THIS CYCLE'S CLOCK, because a call has no instant of its own: `inference_call` carries
      // the tokens and the price and no time, and the ring stamps nothing. So the newest call
      // is dated when the loop SAW it, which is the same clock the stall is judged against —
      // late by at most one cycle, and never skewed against a machine's own.
      folded.last_call_at = new Date(at).toISOString();
    }

    // NOTHING HEARD IS NOT A ROW. `run_progress` exists to say where a job is and what it has
    // spent; a job that has reported no stage and had no call metered has told this deployment
    // neither, and a row of empty strings would render as a blank stage over a clock ticking
    // from the instant of the FOLD — which is what a queued job, a job inside the owner's
    // five-second coalescing window and an operation that reports no stage at all would each
    // have looked like. No row is what the store answers `null` for, and the panel says "no
    // word yet" (#261). `since` is written as it was reported for the same reason: this loop
    // never invents the instant a job reached a stage.
    if (folded.stage === "" && folded.calls === 0) return { stage: "", stalled: false };

    // WHOSE SILENCE IS A STALL: only a run the hub meters. One whose calls it has already
    // counted, or one whose operation binds a service the owner may meter ({@link
    // RunPlan.metered}). Everything else reaches the model through the engine's own credential
    // and journals no `inference_call` ever, so judging it would mark every run that thought
    // for ninety seconds "stalled" for the rest of its life and count it in the header — a
    // number about an adjacent thing, reported as the thing asked (post-mortem O7).
    //
    // The clock the flag is decided against is the CYCLE's, so every row of one report agrees
    // about what is stalled. A job that has been metered is judged from its newest call, which
    // this loop dated when it read it; one that has not, from the instant it said it was at the
    // model, which is the owner's own clock and the one figure here a machine's skew can shift.
    const metered = folded.calls > 0 || plan.metered[run.kind] === true;
    const waitingSince = instantOf(folded.last_call_at === "" ? folded.since : folded.last_call_at);
    const stalled =
      metered &&
      folded.stage === RUN_STAGES.atModel &&
      waitingSince !== null &&
      at - waitingSince >= STALLED_AFTER_MS;
    await store.db.run(
      `INSERT INTO run_progress(run_id, job_id, stage, message, fraction, since, calls,
                                input_tokens, output_tokens, cache_tokens, cost_usd, last_model,
                                last_call_at, seq, stalled, updated_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(run_id) DO UPDATE SET
         job_id = excluded.job_id, stage = excluded.stage, message = excluded.message,
         fraction = excluded.fraction, since = excluded.since, calls = excluded.calls,
         input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
         cache_tokens = excluded.cache_tokens, cost_usd = excluded.cost_usd,
         last_model = excluded.last_model, last_call_at = excluded.last_call_at,
         seq = excluded.seq, stalled = excluded.stalled, updated_at = excluded.updated_at`,
      [
        run.id,
        run.job_id,
        folded.stage,
        folded.message,
        folded.fraction,
        folded.since,
        folded.calls,
        folded.input_tokens,
        folded.output_tokens,
        folded.cache_tokens,
        folded.cost_usd,
        folded.last_model,
        folded.last_call_at,
        folded.seq,
        stalled ? 1 : 0,
        new Date(at).toISOString(),
      ],
    );
    return { stage: folded.stage, stalled };
  }

  /**
   * Every job the hub is waiting on, plus the beat's own, which nobody requested.
   *
   * A job that is still running has its replay ring folded into `run_progress` before the cycle
   * moves on, which is the only reason this loop asks the hub about a job it cannot settle:
   * before #261 a running job was polled purely to be counted, and the count was the whole of
   * what anyone could learn about it.
   */
  async function reconcileRuns(
    at: number,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
    refusals: Counter,
  ): Promise<{ inFlight: number; runs: RunsTally }> {
    const pending = await store.db.query<PendingRun>(
      `SELECT id, job_id, machine_id, kind, container_id, prepare_job_id, started_at,
              profile, preparation, unreadable
         FROM runs
        WHERE closure IS NULL AND job_id IS NOT NULL AND machine_id IS NOT NULL
        ORDER BY started_at`,
    );
    let inFlight = 0;
    let atModel = 0;
    let stalled = 0;
    // The count of silent cycles is on the row now, so nothing is pruned here: a run that is
    // no longer waited on is not selected, and one that answers is set back to zero in place.
    for (const run of pending) {
      // THE FORK: a run with a container is a CODE SESSION, and its job is not Babel's to poll
      // (#279). `ctx.jobs` verbs are bound to the calling plugin's id, so `jobs.status` on it
      // answers nothing useful at best; Code is asked instead, through the door that owns it.
      if (run.container_id !== null && run.container_id !== "") {
        const reconciled = await reconcileSession(at, run, ingested, settled, notes, refusals);
        if (reconciled.inFlight) {
          inFlight += 1;
          // A Code session is at the model from the moment it starts: Babel composes nothing
          // and the job's only work is the turn. There is no replay ring of Babel's to fold, so
          // the count is the honest one rather than a stage nobody read.
          atModel += 1;
        }
        continue;
      }
      let state: JobRunState | null = null;
      try {
        state = await jobs.status({
          kind: "job",
          machineId: run.machine_id,
          operationId: run.kind,
          jobId: run.job_id,
        });
      } catch (error) {
        notes.push(`job ${run.job_id} cannot be read: ${message(error)}`);
      }
      // A status the hub cannot answer — it threw, or it does not know this job — leaves the run
      // in flight for this cycle and is remembered: a machine that vanished would otherwise keep
      // its claims "running" for ever, and the reaper below counts the cycles.
      if (state === null) {
        await silence(run.id, Number(run.unreadable), false);
        inFlight += 1;
        continue;
      }
      await silence(run.id, Number(run.unreadable), true);
      if (TERMINAL_STATES[state.state] !== true) {
        inFlight += 1;
        const folded = await foldRun(at, run, notes);
        if (folded.stage === RUN_STAGES.atModel) atModel += 1;
        if (folded.stalled) stalled += 1;
        continue;
      }
      await settle(
        at,
        {
          runId: run.id,
          jobId: run.job_id,
          machineId: run.machine_id,
          operationId: run.kind,
          outputs: state.result?.outputs ?? [],
          closure: closureOf(state),
          inference: state.result?.usage?.inference ?? null,
        },
        ingested,
        settled,
        notes,
        refusals,
      );
      // The receipt is the record now. A settled run keeps no in-flight row: the panel reads a
      // finished run's spend off the run itself, and a `run_progress` row left behind would be
      // a second, staler answer to the same question.
      await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
    }



    for (const machineId of await knownMachines()) {
      let listed: readonly { job: JobRunState | null }[];
      try {
        listed = (
          await jobs.listRuns({
            machineId,
            operationId: BEAT_OPERATION,
            limit: BEAT_RUNS_SCANNED,
          })
        ).runs;
      } catch (error) {
        notes.push(`the beat's runs on ${machineId} cannot be listed: ${message(error)}`);
        continue;
      }
      for (const row of listed) {
        const job = row.job;
        if (job === null) continue;
        if (TERMINAL_STATES[job.state] !== true) {
          inFlight += 1;
          continue;
        }
        const recorded = await store.db.query<Existing>(`SELECT id FROM runs WHERE job_id = ?`, [
          job.jobId,
        ]);
        if (recorded.length > 0) continue;
        await settle(
          at,
          {
            runId: null,
            jobId: job.jobId,
            machineId,
            operationId: BEAT_OPERATION,
            outputs: job.result?.outputs ?? [],
            closure: closureOf(job),
            inference: job.result?.usage?.inference ?? null,
          },
          ingested,
          settled,
          notes,
          refusals,
        );
      }
    }
    return { inFlight, runs: { running: inFlight, atModel, stalled } };
  }

  /**
   * THE CLAIMS NO SETTLEMENT WILL EVER REACH, released once per cycle.
   *
   * `settle` closes the claim of a job the hub reported terminal, which covers every job that
   * has a run row the loop is waiting on. Three kinds of claim fall outside that, and on
   * 2026-09-13 they were what held the top-ranked subjects for 86 minutes each:
   *
   *   - a claim with NO JOB. `claim` reserves before `jobs.execute` is called, so a posting the
   *     machine refused, or a cycle that died between the two, leaves a grant nothing will ever
   *     match `WHERE job_id = ?`. `dispatch` releases the ones it sees; this releases the ones
   *     nobody saw, once the lease it was granted under has run out.
   *   - a claim whose JOB HAS NO OPEN RUN ROW: the run was closed by another path (an operator
   *     stop, an ingestion that could not write its claim) or never written at all. Nothing
   *     polls it, so nothing would ever settle it.
   *   - a claim whose job the HUB CANNOT REPORT, two cycles running. One silent poll is a
   *     hiccup; two is a machine that is not coming back, and `reconcileRuns` would otherwise
   *     count its jobs in flight for ever.
   *
   * One function and one query, because a second place that decides what a dead claim is would
   * be a second answer to it. The query asks the claims table what it is holding and the runs
   * table what stands behind each row; the counts are subqueries rather than a join so that a
   * job with two run rows is one answer rather than two.
   */
  async function reapClaims(
    at: number,
    leaseSeconds: number,
    settled: SettledClaim[],
    notes: string[],
  ): Promise<void> {
    const orphans = await store.db.query<OrphanClaim>(
      `SELECT c.id, c.fence, c.job_id, c.granted_at,
              (SELECT COUNT(*) FROM runs r WHERE r.job_id = c.job_id) AS runs,
              (SELECT COUNT(*) FROM runs r WHERE r.job_id = c.job_id AND r.closure IS NULL) AS open_runs,
              (SELECT MAX(r.unreadable) FROM runs r WHERE r.job_id = c.job_id) AS silent
         FROM claims c
        WHERE c.finished_at IS NULL
        ORDER BY c.granted_at`,
    );
    const stale = at - Math.max(leaseSeconds, 0) * 1000;
    for (const orphan of orphans) {
      const granted = Date.parse(orphan.granted_at);
      // An unparseable grant time is as old as it gets: it can never become fresh.
      const overdue = !Number.isFinite(granted) || granted <= stale;
      const jobId = orphan.job_id;
      const silent = Number(orphan.silent ?? 0);
      const reason =
        jobId === null
          ? overdue
            ? `granted at ${orphan.granted_at} and never posted to a machine`
            : null
          : Number(orphan.runs) === 0
            ? overdue
              ? `job ${jobId} has no run row and the lease it was granted under has run out`
              : null
            : Number(orphan.open_runs) === 0
              ? `job ${jobId} is closed and its claim was left open`
              : silent >= UNREPORTED_CYCLES
                ? `the hub has not been able to report job ${jobId} for ${String(silent)} cycles`
                : null;
      if (reason === null) continue;
      settled.push(await release(orphan, reason));
      notes.push(`claim ${orphan.id} abandoned: ${reason}`);
    }
  }

  /**
   * WHAT THE FOLDERS A SCAN CATALOGUED ARE, asked of the host rather than of the sandbox.
   *
   * A scan observes its sessions' workspaces from INSIDE the job, where the operator's
   * checkouts are not mounted: the rows it ships carry its own prose reason ("workspace absent
   * on this host") for folders that are ordinary repositories on the machine itself.
   * `engine.machines.repository` asks the agent standing on that host instead (#535), and its
   * answer is one word of a CLOSED vocabulary — which is also the marker that says who answered.
   * A row whose `repository_reason` is one of those words has been asked; every other row —
   * null because the scan found a repository, prose because the scan could not, empty because
   * the import carried none — has not, and is what this asks about.
   *
   * ONE QUESTION PER FOLDER, never per session: a machine holds tens of workspaces and
   * thousands of sessions, so the rows of one workspace are written by one answer. At most
   * {@link WORKSPACES_PER_TICK} of them per tick, because a cycle is something a dispatch is
   * waiting behind — the rest are asked on the next one, in the same order.
   *
   * A MACHINE THAT CANNOT ANSWER IS DROPPED FOR THE REST OF THE TICK rather than asked once per
   * folder. `ok: false` is offline, too old a transport, or silence — facts about the MACHINE,
   * not about the path — so the second question would buy the same refusal and another note.
   * Its rows are left exactly as they were, which is what makes the next tick ask again.
   */
  async function identifyFolders(notes: string[]): Promise<void> {
    const unidentified = await store.db.query<UnidentifiedFolder>(
      `SELECT DISTINCT host AS machineId, workspace FROM sessions
        WHERE host <> '' AND workspace LIKE '/%'
          AND (repository_reason IS NULL OR repository_reason NOT IN (${HUB_REASON_HOLES}))
        ORDER BY host, workspace
        LIMIT ?`,
      [...MACHINE_REPOSITORY_REASONS, WORKSPACES_PER_TICK],
    );
    const silent = new Set<string>();
    let identified = 0;
    for (const folder of unidentified) {
      if (silent.has(folder.machineId)) continue;
      let outcome: RepositoryOutcome;
      try {
        outcome = await machines.repository(folder.machineId, folder.workspace);
      } catch (error) {
        silent.add(folder.machineId);
        notes.push(`${folder.machineId} cannot be asked what a folder is: ${message(error)}`);
        continue;
      }
      if (!outcome.ok) {
        silent.add(folder.machineId);
        notes.push(
          `${folder.machineId} could not say what ${folder.workspace} is: ${outcome.reason}`,
        );
        continue;
      }
      await store.db.run(
        `UPDATE sessions SET repository_identity = ?, repository_remote = ?, repository_reason = ?
          WHERE host = ? AND workspace = ?`,
        [
          outcome.fact.identity,
          outcome.fact.remote,
          outcome.fact.reason,
          folder.machineId,
          folder.workspace,
        ],
      );
      identified += 1;
    }
    if (identified > 0) store.touch();
  }

  /**
   * WHETHER THE LOOP IS PARKED, read off the spend ledger rather than counted in a process.
   *
   * The window is the last {@link PARK_AFTER} claims to close under the policy in force, and
   * each one is judged by whether A MODEL EVER ANSWERED FOR IT:
   *
   *   - a claim that closed as `completed` produced the review it was drawn for. Not barren,
   *     whatever it cost.
   *   - a run that spent, or reported tokens, reached the model. Not barren.
   *   - a receipt whose reason carries a refusal code reached the model and had its submission
   *     refused: PAID WORK WITH NO RESULT, which is the recipe's problem and not the loop's.
   *     Not barren, and this one sentence is the whole of #265.
   *   - anything else — an `abandoned` claim whose job wrote no receipt, a job that failed
   *     before its first call, a claim whose posting was refused and has no run at all — is
   *     barren: the deployment paid a reservation and learned nothing.
   *
   * The claims table is the ledger for this because it is where a settlement is durable: the
   * conductor is rebuilt for every wake, so anything the loop "remembers" has to be something
   * the store can be asked. `finished_at` is written by `finish` and `abandon` in the store's
   * fixed-width instant, so text order is time order and the newest rows come first.
   */
  async function parkState(policy: Policy, at: number): Promise<Park | null> {
    const closed = await store.db.query<ClosedClaim>(
      `SELECT c.outcome AS outcome, c.finished_at AS finished_at,
              r.cost_usd AS cost, r.tokens AS tokens, r.payload AS payload
         FROM claims c LEFT JOIN runs r ON r.job_id = c.job_id
        WHERE c.finished_at IS NOT NULL AND c.policy_version = ?
        ORDER BY c.finished_at DESC
        LIMIT ?`,
      [policy.version, PARK_AFTER],
    );
    if (closed.length < PARK_AFTER) return null;
    for (const claim of closed) {
      if (claim.outcome === "completed") return null;
      if ((claim.cost ?? 0) > 0 || (claim.tokens ?? 0) > 0) return null;
      if (receiptRefusal(claim.payload) !== null) return null;
    }
    // An unreadable instant is as old as it gets, as it is for a claim's grant in `reapClaims`:
    // a streak nothing recent stands behind is not a park, and the next cycle draws.
    const newest = Date.parse(closed[0]?.finished_at ?? "");
    if (!Number.isFinite(newest) || newest <= at - PARK_WINDOW_MS) return null;
    return {
      barren: closed.length,
      reason:
        `the last ${String(closed.length)} reviews under policy ${policy.version} reached no ` +
        `model and produced nothing, the most recent at ${closed[0]?.finished_at ?? ""}; the ` +
        `loop draws nothing until one of them is answered, an hour has passed, or a new policy ` +
        `is installed`,
    };
  }

  /**
   * The day's tally: this tick's counts added to the ones the day already had.
   *
   * One key, read and rewritten, because a cycle is a fresh conductor over the wake that caused
   * it and a counter in this closure would report zero for every tick of a real day. A key the
   * host refuses, or a value that is not this shape, is not a reason to report nothing: the
   * tick's own counts stand as the day's and the note says the day could not be read.
   */
  async function rollUp(at: number, tick: CycleTally, notes: string[]): Promise<CycleTally> {
    const day = new Date(at).toISOString().slice(0, 10);
    const gaps = new Map(Object.entries(tick.gaps));
    const refusals = new Map(Object.entries(tick.refusals));
    let held: string | null = null;
    try {
      held = await keys.get(TALLY_KEY);
    } catch (error) {
      notes.push(`the day's tally cannot be read: ${message(error)}`);
      return tick;
    }
    if (held !== null) {
      let stored: unknown = null;
      try {
        stored = JSON.parse(held);
      } catch {
        notes.push("the day's tally was not readable and starts again from this cycle");
      }
      const kept = stored as Partial<{ day: string; gaps: unknown; refusals: unknown }> | null;
      // Yesterday's tally is not this day's: the key is rewritten, never accumulated across the
      // boundary the spend ledger itself is kept by.
      if (kept !== null && typeof kept === "object" && kept.day === day) {
        for (const [counter, source] of [
          [gaps, kept.gaps],
          [refusals, kept.refusals],
        ] as const) {
          if (typeof source !== "object" || source === null) continue;
          for (const [reason, seen] of Object.entries(source as Record<string, unknown>)) {
            if (typeof seen !== "number" || !Number.isFinite(seen)) continue;
            counter.set(reason, (counter.get(reason) ?? 0) + seen);
          }
        }
      }
    }
    const today = { gaps: tallied(gaps), refusals: tallied(refusals) };
    try {
      await keys.set(TALLY_KEY, JSON.stringify({ day, ...today }));
    } catch (error) {
      notes.push(`the day's tally cannot be kept: ${message(error)}`);
    }
    return today;
  }

  return {
    async tick(): Promise<TickReport> {
      const at = deps.now();
      cycle += 1;
      const cycleRunId = `cyc_${String(at)}_${String(cycle)}`;
      // THE POLICY IN FORCE IS THE STANDING ROW PLUS WHATEVER OVERLAY IS UNEXPIRED (#260).
      // Admission — the batch, the two ceilings — reads the overlaid numbers; the SCHEDULE
      // reads the standing version, because the beat's revision is the policy's version and an
      // overlay that re-registered the cadence would make a two-hour drain a permanent
      // rewrite of the loop's own clock.
      const inForce = await coordinator.policy(at);
      const policy = inForce.policy;
      const notes: string[] = [];
      const schedule = await reconcileSchedule(inForce.standing, at, notes);
      const requested: RequestedJob[] = [];
      const ingested: IngestedRun[] = [];
      const settled: SettledClaim[] = [];
      const refused: RefusedDraw[] = [];
      const refusals: Counter = new Map();
      const gapsByReason: Counter = new Map();

      if (!policy.enabled) {
        // A disabled policy is the coordinator's own first stop reason, and the cycle never gets
        // as far as being told it: the loop counts it, so "why did nothing happen today" is
        // answered by the tally rather than by the absence of one.
        count(gapsByReason, "disabled");
        const tick = { gaps: tallied(gapsByReason), refusals: tallied(refusals) };
        return {
          at,
          cycleRunId,
          policyVersion: policy.version,
          enabled: false,
          schedule,
          requested,
          ingested,
          settled,
          refused,
          stop: null,
          gaps: [],
          parked: null,
          pulse: { tick, today: await rollUp(at, tick, notes) },
          runs: { running: 0, atModel: 0, stalled: 0 },
          pending: 0,
          notes,
        };
      }

      const reconciled = await reconcileRuns(at, ingested, settled, notes, refusals);
      // …and the claims no settlement can reach are released before this cycle asks the
      // coordinator what may be drawn, so a batch held by dead workers is a batch of free slots
      // by the time it answers rather than one cycle later.
      await reapClaims(at, policy.leaseSeconds, settled, notes);
      // What a scan just catalogued is folders; what they ARE is the host's to say, and it is
      // asked here, after the rows exist and before this cycle spends anything.
      await identifyFolders(notes);

      // WHETHER THE LOOP IS PARKED is still asked, and still recorded, because it is read off
      // the spend ledger of the runs that did happen — a review that was paid for and refused
      // is in it too, and the park heuristic is what keeps that from reading as a broken lane.
      const parked = await parkState(policy, at);
      if (parked !== null) notes.push(`the loop is parked: ${parked.reason}`);

      /*
        AND THEN NOTHING IS DRAWN, and the reason is no longer the engine's (#279).

        A drawn review used to become an `atyrode.babel.evaluate` job this loop launched. The
        engine is Code now and this deployment can reach it — the `launch` door posts an explore
        through `atyrode.code.runSession` — but a DRAWN review is a different thing: the
        coordinator picks it, claims it under a fence, and the dispatch hands the model a BLINDED
        projection of the record under review. That dispatch and that projection went with
        Babel's own launcher in the revert (#290) and have not come back, so the cycle states the
        one true reason it spent nothing and asks the coordinator for no candidate at all.

        Drawing anyway would claim a record under a fence, hold a batch slot for a lease and then
        abandon it once the posting refused — the ghost-claim shape of 2026-09-13, for work
        nobody could have done.

        Everything above this line still runs: the settlements, the reaper, the beat, the
        folders, the pulse. The loop is intact and idle, not dismantled.
      */
      const stop: Stop = { reason: "draw-pending", detail: DRAW_PENDING };
      const gaps: readonly Gap[] = [];
      // The reason this cycle did not spend, counted once.
      count(gapsByReason, stop.reason);
      const tick = { gaps: tallied(gapsByReason), refusals: tallied(refusals) };

      return {
        at,
        cycleRunId,
        policyVersion: policy.version,
        enabled: true,
        schedule,
        requested,
        ingested,
        settled,
        refused,
        stop,
        gaps,
        parked,
        pulse: { tick, today: await rollUp(at, tick, notes) },
        runs: reconciled.runs,
        pending: reconciled.inFlight + requested.length,
        notes,
      };
    },
  };
}

/** What the engine's own verdict says a run closed as, before any receipt is read. */
function closureOf(state: JobRunState): string {
  if (state.state === "cancelled") return "stopped";
  if (state.state === "exited" && (state.result?.exitCode ?? 1) === 0) return "completed";
  return "failed";
}

/** An ISO instant as epoch milliseconds, or null when the column held nothing readable. */
function instantOf(value: string): number | null {
  if (value === "") return null;
  const at = Date.parse(value);
  return Number.isFinite(at) ? at : null;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
