import { createHash } from "node:crypto";
import { MACHINE_REPOSITORY_REASONS } from "@manifold/protocol";
import type { PluginDatabase, SqlParam, SqlStatement } from "@manifold/plugin";
import { z } from "zod";
import type { CycleReportSchema, IngestibleTable } from "../contract.ts";
import {
  BABEL_PLUGIN_ID,
  CONDUCTOR_CYCLE_KEY,
  INPUT_FIELD,
  JOB_OUTPUT_FILES,
  MATERIAL_OUTPUT,
  MaterialIndexSchema,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  RUN_STAGES,
  ReceiptSchema,
  type GapReason,
  type MaterialEntry,
  type MaterialIndex,
  type Receipt,
  type RunCall,
  type RunTrace,
} from "../contract.ts";
import type { Assignment, Coordinator, Fence, Gap, Policy, Stop } from "../store/coordinator.ts";
import { refuseRow, type RowRefusal } from "../store/acts.ts";
import { REFUSALS, refusalCode, refusalReason, type RefusalCode } from "../machine/results.ts";
import type { BabelStore } from "../store/store.ts";
import {
  PROMPT_LIMIT,
  promptBytes,
  type CodeEngine,
  type SessionReceipt,
  type SessionRead,
} from "./engine/session.ts";
import { readExploreAnswer, type Recipe } from "./engine/prompts.ts";
import {
  admitCitation,
  checkCitations,
  citationNotes,
  citationTally,
  citedEvidence,
  materialLines,
  unservedCitation,
  type SessionLines,
} from "./engine/citations.ts";
import { exploreRows, markerReferences } from "./engine/records.ts";
import {
  blindedLeak,
  composeReviewPrompt,
  reviewPreparation,
  reviewRows,
  reviewVerdict,
  REVIEW_BLINDING_POLICY_VERSION,
  REVIEW_JOB_VERSION,
  REVIEW_PROMPT_VERSION,
  blinded,
  rejectedSubmission,
  type RefusedContribution,
  type ReviewPreparation,
  type ReviewProjection,
} from "./engine/review.ts";
import {
  declinedTitles,
  offeredSelectors,
  readTitleAnswer,
  titleStatements,
  type InferredTitle,
} from "./engine/titles.ts";

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
    readonly usage?: { readonly inference?: InferenceUsage | undefined } | null | undefined;
  } | null;
  /** WHY A POSTING WAS REFUSED, which is never in `result`: a job refused at admission never
   *  ran and has no result at all. The hub's own word for it — `concurrency_limit` when the
   *  operation's declared `limits.concurrentJobs` is full — is on the authority decision
   *  (`PublicJobSchema.authority`), which the kit's own `GuestJobStatus` does not restate. */
  readonly authority?:
    { readonly decision?: { readonly refusal: string | null } | null | undefined } | undefined;
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
 * the {@link Stop} that ended the cycle (`disabled`, `batch`, `batch-filled`, `per-cycle`,
 * `daily`, `no-candidates`, …) and every candidate {@link Gap} it declined on the way
 * (`claimed`, `cooling`, `capped`, …). The two vocabularies are disjoint word sets, so one map
 * by reason is unambiguous, and a surface that wants the records rather than the counts reads
 * `gaps` on the report itself.
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
/** `MAX_SQL_PARAMS`: one statement binds at most this many, so a long `IN` list is asked in pieces. */
const MAX_SQL_PARAMS = 999;
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
 * How much of a stop's or a gap's sentence the door is given. A dispatch refusal carries
 * whatever the engine said, and an operator reading a panel needs the first clause of it far
 * more than he needs the stack that followed; the whole sentence is in the hub's log.
 */
const DETAIL_KEPT = 400;

/**
 * The stop the loop makes on its own behalf. A disabled policy is the coordinator's own first
 * stop reason and the cycle never gets far enough to be told it — so the loop says it, in the
 * coordinator's word, rather than leaving the commonest "why is nothing running" as a silence.
 */
const DISABLED_STOP: Stop = {
  reason: "disabled",
  detail: "the policy in force is not enabled, so the loop draws nothing",
};

/**
 * How many folders one tick asks the fleet about; the rest wait for the next tick. A machine
 * holds tens of workspaces and a first scan of a new fleet may present hundreds at once, and a
 * cycle is something a dispatch is waiting behind: 64 probes is a bounded second of it.
 */
const WORKSPACES_PER_TICK = 64;
/**
 * How many statements one settlement's `batch` carries. The engine takes 256 a call, and an
 * answer that claimed more rows than that has to land as several transactions rather than not
 * at all; the chunk is under the bound so a caller adding a guard statement cannot breach it.
 */
const STATEMENTS_PER_BATCH = 250;

/** One `?` per word of the hub's closed reason vocabulary, for the marker predicate below. */
const HUB_REASON_HOLES = MACHINE_REPOSITORY_REASONS.map(() => "?").join(", ");

// ---------------------------------------------------------------------------- ingestion tables

interface TableIngest {
  /**
   * The table these rows land in, from `contract.ts`'s closed `INGESTIBLE_TABLES`. The type is
   * the boundary of what a run may write: `dispositions` and `next_action_rulings` are the
   * operator's ledgers and are not in that list, so an entry naming one does not compile.
   */
  readonly table: IngestibleTable;
  readonly columns: readonly string[];
  /**
   * `ignore` is every table that records an act: a row is written once under its own id and a
   * second delivery of it is the same row. `upsert` is the two projections — a session's
   * catalog entry and a run — where a later observation completes an earlier one.
   */
  readonly conflict: "ignore" | "upsert";
  readonly key?: string;
  /**
   * COLUMNS AN OBSERVATION THAT SAW NOTHING MAY NOT ERASE, on an `upsert`.
   *
   * It is the same rule as "never mention the column", reached the other way. A scan that
   * observed no snapshot leaves `snapshot_id` out of its row entirely and the archive's answer
   * survives; a scan that observed no TITLE cannot do that, because the row it writes is the
   * whole catalog shape and a title is one of its columns — so NULL arrives meaning "this
   * reader found none", and the upsert reads it as "there is none".
   *
   * That was harmless while every title had a free source. It is not now: a model-inferred
   * title (#342) is the one value here a rescan cannot recover, and wiping it would both lose
   * what was paid for and put the session back in the queue to be paid for again on the next
   * wake. `COALESCE(excluded.x, x)` is the whole fix — a scan that READ a title still wins,
   * because its value is not null.
   */
  readonly observed?: readonly string[];
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
    observed: ["title", "title_provenance"],
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
  [JOB_OUTPUT_FILES.nextActions]: {
    table: "next_actions",
    columns: [
      "id",
      "record_id",
      "kind",
      "proposed_by_kind",
      "proposed_by_id",
      "summary",
      "created_at",
      "payload",
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
  // After `records`, because a proposed action references the record it is about.
  JOB_OUTPUT_FILES.nextActions,
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
  for (let offset = 0; offset + 512 <= bytes.length;) {
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
interface SqlCondition {
  readonly sql: string;
  readonly params: readonly SqlParam[];
}

function rowStatement(
  ingest: TableIngest,
  row: Record<string, unknown>,
  condition?: SqlCondition,
): SqlStatement | null {
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
  const values =
    condition === undefined ? `VALUES (${holes})` : `SELECT ${holes} WHERE ${condition.sql}`;
  const bound = condition === undefined ? params : [...params, ...condition.params];
  if (ingest.conflict === "ignore") {
    return { sql: `INSERT OR IGNORE INTO ${ingest.table}(${names}) ${values}`, params: bound };
  }
  const key = ingest.key ?? "id";
  const observed = new Set(ingest.observed ?? []);
  const updates = columns
    .filter((column) => column !== key)
    .map((column) =>
      observed.has(column)
        ? `${column} = COALESCE(excluded.${column}, ${column})`
        : `${column} = excluded.${column}`,
    )
    .join(", ");
  return {
    sql:
      `INSERT INTO ${ingest.table}(${names}) ${values} ON CONFLICT(${key}) ` +
      (updates === "" ? "DO NOTHING" : `DO UPDATE SET ${updates}`),
    params: bound,
  };
}

/** The run row a finished job leaves behind: the receipt as the machine wrote it, or its absence. */
function runStatement(
  runId: string,
  target: IngestTarget,
  receipt: Receipt | null,
  rows: Readonly<Record<string, number>>,
  condition?: SqlCondition,
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
  const holes = columns.map(() => "?").join(", ");
  const valuesSql =
    condition === undefined ? `VALUES (${holes})` : `SELECT ${holes} WHERE ${condition.sql}`;
  return {
    sql:
      `INSERT INTO runs(${columns.join(", ")}) ${valuesSql} ` +
      `ON CONFLICT(id) DO UPDATE SET ${updates.join(", ")}` +
      (condition === undefined ? "" : " RETURNING id"),
    params: [
      ...columns.map((column) => values[column] ?? null),
      ...(condition === undefined ? [] : condition.params),
    ],
  };
}

// ------------------------------------------------------------- a run's replayable trace (#349)

/** Every column of a call row, in the one order the statement below writes them. */
const CALL_COLUMNS = [
  "run_id",
  "seq",
  "recorded_at",
  "model",
  "input_tokens",
  "output_tokens",
  "cache_read_tokens",
  "cache_write_tokens",
  "cost_micros",
  "exit_code",
  "closure",
  "refusal",
  "response_digest",
  "response_bytes",
  "transcript_host",
  "transcript_session",
  "transcript_path",
] as const;

/**
 * WHAT A REFUSED ANSWER'S CALL ROW SAYS WHEN THE REASON CARRIES NO CODE THIS BUILD KNOWS.
 *
 * Every reason either settlement writes is `<code>: <sentence>` built out of {@link REFUSALS},
 * so this is unreachable today. It exists because the alternative — an empty `refusal` — reads
 * as "the answer stood", which is the one thing it certainly did not do. The word is
 * deliberately outside the refusal vocabulary so a tally cannot count it as one of them.
 */
const UNCLASSIFIED_REFUSAL = "refused";

/**
 * ONE SETTLED SESSION AS A CALL ROW: what it cost, how it ended, and where its bytes are.
 *
 * Both settlement lanes reach this, because "a run's calls can be rechecked" cannot be true of
 * explorations and false of reviews.
 *
 * THE DIGEST IS TAKEN OVER THE FINAL MESSAGE EXACTLY AS CODE'S RECEIPT CARRIED IT, empty string
 * included: a session that sealed a transcript and said nothing is a different event from one
 * that sealed none, and only the second leaves the digest empty. Nothing else about the message
 * is kept — the message itself, and the request that drew it, are records in the transcript the
 * three `transcript_*` columns locate, on the machine that ran the session.
 */
function sessionCall(input: {
  readonly runId: string;
  readonly at: number;
  readonly machineId: string;
  readonly session: SessionReceipt | null;
  readonly closure: Receipt["closure"];
  readonly reason: string;
}): RunCall {
  const session = input.session;
  const usage = session?.usage ?? null;
  const message = session?.finalMessage ?? "";
  return {
    runId: input.runId,
    // ONE CALL PER SETTLEMENT, for the reason `settleSession` states about `inference`: a posted
    // session is omp's one-shot and the turns inside it are summed before Babel is told of any.
    seq: 1,
    recordedAt: new Date(input.at).toISOString(),
    model: session?.model ?? "",
    inputTokens: usage?.input ?? 0,
    outputTokens: usage?.output ?? 0,
    cacheReadTokens: usage?.cacheRead ?? 0,
    cacheWriteTokens: usage?.cacheWrite ?? 0,
    costMicros: Math.round((usage?.cost ?? 0) * 1_000_000),
    exitCode: session?.exitCode ?? null,
    closure: input.closure,
    refusal: input.reason === "" ? "" : (refusalCode(input.reason) ?? UNCLASSIFIED_REFUSAL),
    response:
      session === null
        ? { digest: "", bytes: 0 }
        : {
            digest: createHash("sha256").update(message).digest("hex"),
            bytes: new TextEncoder().encode(message).byteLength,
          },
    transcript:
      session === null
        ? { host: "", sessionId: "", path: "" }
        : { host: input.machineId, sessionId: session.sessionId, path: session.sessionPath },
  };
}

/**
 * The call row, written once. `OR IGNORE` on `(run_id, seq)` is what makes a settlement replayed
 * after a crash a no-op here, the way every other row a settlement writes is one.
 */
function callStatement(call: RunCall, condition?: SqlCondition): SqlStatement {
  const values: readonly SqlParam[] = [
    call.runId,
    call.seq,
    call.recordedAt,
    call.model,
    call.inputTokens,
    call.outputTokens,
    call.cacheReadTokens,
    call.cacheWriteTokens,
    call.costMicros,
    call.exitCode,
    call.closure,
    call.refusal,
    call.response.digest,
    call.response.bytes,
    call.transcript.host,
    call.transcript.sessionId,
    call.transcript.path,
  ];
  const holes = CALL_COLUMNS.map(() => "?").join(", ");
  return {
    sql:
      `INSERT OR IGNORE INTO run_calls(${CALL_COLUMNS.join(", ")}) ` +
      (condition === undefined ? `VALUES (${holes})` : `SELECT ${holes} WHERE ${condition.sql}`),
    params: [...values, ...(condition === undefined ? [] : condition.params)],
  };
}

/** A run row as a trace's request half is read off it. */
type TraceRow = {
  readonly kind: string;
  readonly machine_id: string | null;
  readonly recipe_id: string | null;
  readonly prepare_job_id: string | null;
  readonly payload: string;
};

/** A call row as SQLite answers it: every INTEGER column arrives as a BIGINT. */
type CallRow = {
  readonly run_id: string;
  readonly seq: number | bigint;
  readonly recorded_at: string;
  readonly model: string;
  readonly input_tokens: number | bigint;
  readonly output_tokens: number | bigint;
  readonly cache_read_tokens: number | bigint;
  readonly cache_write_tokens: number | bigint;
  readonly cost_micros: number | bigint;
  readonly exit_code: number | bigint | null;
  readonly closure: RunCall["closure"];
  readonly refusal: string;
  readonly response_digest: string;
  readonly response_bytes: number | bigint;
  readonly transcript_host: string;
  readonly transcript_session: string;
  readonly transcript_path: string;
};

/**
 * THE FOUR THINGS A TRACE READS OUT OF A RUN'S PAYLOAD, and why they are not read with
 * {@link ReceiptSchema}.
 *
 * `runs.payload` is the receipt AND the hub's own meter beside it ({@link runStatement} writes
 * `inference` into the same document), so a strict parse of it fails by construction — and
 * failing would blank the request half of every metered run's trace. A reader that wants four
 * fields asks for four fields; the receipt itself stays the strict shape a producer writes.
 */
const TracedRequestSchema = z.object({
  recipeId: z.string().optional(),
  model: z.string().optional(),
  account: z.object({ provider: z.string(), identityKey: z.string() }).optional(),
  steering: z.object({ carried: z.array(z.object({ id: z.string() })) }).optional(),
});

/**
 * ONE RUN'S TRACE: what it was asked, and the calls it made answering. Null for a run this hub
 * has no row for.
 *
 * The request half is READ off the run row and its receipt rather than copied into `run_calls`,
 * for the same reason the calls keep no bodies — a second copy of a fact is a thing that can
 * come to disagree with the first. A receipt this build cannot parse (`'{}'` on a run the hub
 * has not settled) leaves the request half empty, which is the truth about it: nobody recorded
 * what that run was asked.
 */
export async function readRunTrace(
  db: Pick<PluginDatabase, "query">,
  runId: string,
): Promise<RunTrace | null> {
  const row = (
    await db.query<TraceRow>(
      `SELECT kind, machine_id, recipe_id, prepare_job_id, payload FROM runs WHERE id = ?`,
      [runId],
    )
  )[0];
  if (row === undefined) return null;
  let held: unknown;
  try {
    held = JSON.parse(row.payload);
  } catch {
    held = null;
  }
  const parsed = TracedRequestSchema.safeParse(held);
  const receipt = parsed.success ? parsed.data : null;
  const account = receipt?.account;
  const calls = await db.query<CallRow>(
    `SELECT ${CALL_COLUMNS.join(", ")} FROM run_calls WHERE run_id = ? ORDER BY seq`,
    [runId],
  );
  return {
    runId,
    kind: row.kind,
    machineId: row.machine_id ?? "",
    recipeId: row.recipe_id ?? receipt?.recipeId ?? "",
    material: row.prepare_job_id ?? "",
    account: account === undefined ? "" : `${account.provider}/${account.identityKey}`,
    model: receipt?.model ?? "",
    steering: (receipt?.steering?.carried ?? []).map((remark) => remark.id),
    calls: calls.map((call) => ({
      runId: call.run_id,
      seq: Number(call.seq),
      recordedAt: call.recorded_at,
      model: call.model,
      inputTokens: Number(call.input_tokens),
      outputTokens: Number(call.output_tokens),
      cacheReadTokens: Number(call.cache_read_tokens),
      cacheWriteTokens: Number(call.cache_write_tokens),
      costMicros: Number(call.cost_micros),
      exitCode: call.exit_code === null ? null : Number(call.exit_code),
      closure: call.closure,
      refusal: call.refusal,
      response: { digest: call.response_digest, bytes: Number(call.response_bytes) },
      transcript: {
        host: call.transcript_host,
        sessionId: call.transcript_session,
        path: call.transcript_path,
      },
    })),
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
      const shaped =
        typeof row === "object" && row !== null && !Array.isArray(row)
          ? (row as Record<string, unknown>)
          : null;
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
type Existing = { id: string };
/** One distinct `sessions.host` value; a machine id only when an id is what was catalogued. */
type HostRow = { host: string };
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

/**
 * THE MACHINE AS THE HUB DESCRIBES IT, or the sentence saying why it cannot run this operation.
 *
 * `machineId` IS AN ID. `describe` is keyed on the machine id and a host NAME is not one, so a
 * caller holding a name has nothing to ask with: the hub refuses the identifier rather than
 * reporting a machine as offline (atyrode/manifold#725), and the first sentence below is what
 * comes back. Nothing here turns a name into an id, because nothing a plugin is served could —
 * `ctx.machines` is `isOnline`, `getTerminalExecution` and `repository`, and all three are
 * keyed on the id as well.
 *
 * EVERY REFUSAL NAMES ITS CAUSE, in the shape the hub's own `job_owner_mismatch` and
 * `installation_absent` arrive in. A caller told only "no" has to guess, and a loop told only
 * "no" left the cadence unregistered for a day without saying so.
 */
export async function describeHost(
  jobs: JobsSlice,
  machineId: string,
  operationId: string,
): Promise<{ readiness: MachineReadiness } | { refused: string }> {
  let described: MachineReadiness;
  try {
    described = await jobs.describe({ machineId, pluginId: BABEL_PLUGIN_ID });
  } catch (error) {
    return { refused: `${machineId} cannot be described: ${message(error)}` };
  }
  if (!described.connected) return { refused: `${machineId} is offline` };
  const installed = described.installation;
  if (installed === null) return { refused: `${machineId} has no Babel installed` };
  if (!installed.enabled || !installed.ready) {
    return { refused: `Babel on ${machineId} is installed but not ready to run` };
  }
  const operation = described.operations?.[operationId];
  if (operation?.ready === false) {
    return {
      refused: `${operationId} is not ready on ${machineId}${
        operation.reason === null ? "" : `: ${operation.reason}`
      }`,
    };
  }
  return { readiness: described };
}

/**
 * THE MACHINES A CYCLE MAY NAME TO THE HUB, and every one of them is an ID.
 *
 * `SELECT DISTINCT host FROM sessions` used to be this list, and it is the whole of why the
 * cadence never registered. `sessions.host` is written by `scan` as the machine it ran on, but
 * an IMPORTED corpus carries the operator's own host name there instead (`tools/import.ts
 * --host`, the Go deployment's `storage.json` `host_id`), so all 588 rows of that store read
 * `dev-01`. A name is not an id: `describe` answered `connected: false` for it, nothing was
 * ever usable, and the schedule was reported `absent` without a note. `runs.machine_id` is no
 * better a source, because the same import writes the same `--host` into it.
 *
 * What is left is the two places an id can only have come from the operator or from the hub:
 *
 *   - THE MACHINE THE POLICY ROUTES ITS WORK TO (`review.machineId`). It is the one machine id
 *     an operator RECORDED rather than one a column implied, and `dispatchReviews` already
 *     posts every review of this cycle to it — so a beat registered anywhere else would wake a
 *     machine the policy never authorised to spend.
 *   - THE MACHINES BABEL'S OWN SCHEDULES NAME, which the hub itself answered `jobs.schedules()`
 *     with. A beat still arriving under a policy version that routed elsewhere is therefore
 *     folded by `reconcileRuns` rather than left orphaned on a machine nothing asks about.
 *
 * The policy's machine comes first, because it is the one this cycle would post to.
 */
function beatMachines(policy: Policy, registered: readonly ScheduleRow[]): readonly string[] {
  const ids: string[] = [];
  const routed = policy.review?.machineId ?? "";
  if (routed !== "") ids.push(routed);
  for (const row of registered) {
    if (row.machineId !== "" && !ids.includes(row.machineId)) ids.push(row.machineId);
  }
  return ids;
}

/**
 * What reconciling the cadence settled: the state the report carries, and the machine ids this
 * cycle may name to the hub. They are one answer because they come from one look at
 * `jobs.schedules()` beside one policy, and a second look would be a second answer to "which
 * machines is this loop entitled to ask about".
 */
interface ScheduleReconciliation {
  readonly state: ScheduleState;
  readonly machines: readonly string[];
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
    if (next !== held)
      await store.db.run(`UPDATE runs SET unreadable = ? WHERE id = ?`, [next, runId]);
    return next;
  }

  /**
   * The one schedule the loop keeps: the policy's cadence, on the machine the policy names.
   *
   * A HOST THAT WILL NOT REGISTER IT IS NOT A REASON TO STOP. The three schedule verbs are the
   * only ones a cycle can be refused for structurally rather than for this policy's sake — a
   * hardened server half reaches none of them (`ISOLATE_CTX_METHODS` serves no `jobs.schedule`),
   * and an authority that has lapsed refuses the other two — so the refusal is recorded as a
   * note and the cycle carries on. The beat is one WAKE; ingesting what has already finished
   * and drawing what the policy allows are the work, and they do not need it.
   *
   * IT IS NOT A REASON TO SAY NOTHING EITHER, and that half is why the bug survived a day.
   * `absent` returned in silence is a cycle with nothing wrong with it beside an empty
   * `job_schedules`: no note named the machine that was tried, so no report could show that the
   * loop had been asking the hub about a host NAME. A refusal names its cause here exactly as
   * it does at a door ({@link describeHost}), and the machine it names is an id.
   */
  async function reconcileSchedule(
    policy: Policy,
    at: number,
    notes: string[],
  ): Promise<ScheduleReconciliation> {
    let listed: readonly ScheduleRow[];
    try {
      listed = await jobs.schedules();
    } catch (error) {
      notes.push(`the beat's schedule cannot be read: ${message(error)}`);
      return { state: "absent", machines: beatMachines(policy, []) };
    }
    const registered = listed.filter((row) => row.scheduleId === CONDUCTOR_SCHEDULE_ID);
    const machines = beatMachines(policy, registered);
    if (!policy.enabled) {
      try {
        for (const row of registered) {
          await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
        }
      } catch (error) {
        notes.push(`the beat cannot be unregistered: ${message(error)}`);
        return { state: "kept", machines };
      }
      return { state: registered.length === 0 ? "absent" : "unregistered", machines };
    }
    const intervalMs = Math.max(1, policy.cadenceSeconds) * 1000;
    const current = registered.find(
      (row) =>
        row.revision === policy.version &&
        row.intervalMs === intervalMs &&
        row.expiresAt - at > intervalMs,
    );
    if (current !== undefined) return { state: "kept", machines };
    // What a cycle that could not register reports: the rows it already holds still stand, so a
    // live cadence is never reported away because this one tick could not renew it.
    const unregistered: ScheduleReconciliation = {
      state: registered.length === 0 ? "absent" : "kept",
      machines,
    };
    const routed = policy.review?.machineId ?? "";
    if (routed === "") {
      notes.push(
        `the beat cannot be registered: policy ${policy.version} names no machine for its work, ` +
          `so there is no machine id to register the cadence on`,
      );
      return unregistered;
    }
    const described = await describeHost(jobs, routed, BEAT_OPERATION);
    if ("refused" in described) {
      notes.push(`the beat cannot be registered: ${described.refused}`);
      return unregistered;
    }
    try {
      for (const row of registered) {
        await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
      }
    } catch (error) {
      notes.push(`the beat cannot be re-registered: ${message(error)}`);
      return { state: "kept", machines };
    }
    const installation = described.readiness.installation;
    try {
      await jobs.schedule({
        jobId: `${CONDUCTOR_SCHEDULE_ID}.${policy.version}`,
        machineId: routed,
        operationId: BEAT_OPERATION,
        // The beat's input is fixed at registration, so it carries no run id: the machine half
        // mints one per occurrence and the receipt is what names it.
        input: {
          [INPUT_FIELD]: JSON.stringify({
            runId: "",
            machineId: routed,
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
      return { state: "absent", machines };
    }
    return { state: "registered", machines };
  }

  /**
   * One claim released because the job behind it is gone, as the cycle's own report row.
   *
   * Every path that discovers a dead job comes through here — a settlement with no receipt, a
   * posting the machine refused, the reaper — so "a claim dies with its job" is one sentence of
   * accounting written once. A refusal is reported rather than thrown: the claim moved on under
   * a later fence, which is somebody else's live work and not this cycle's to close.
   */
  async function release(
    claim: { id: string; fence: Fence },
    reason: string,
  ): Promise<SettledClaim> {
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

  /**
   * THE BYTES OF THE SESSIONS THIS ANSWER QUOTED, read back out of the preparation's own sealed
   * material — the one read in this lane that opens a corpus rather than an index (#348).
   *
   * IT IS NOT A SECOND INDEX. {@link materialOf} answers "was this path served, at these
   * bytes" out of a row, and that is what admits a citation at all. This answers "is the quoted
   * text there", which no digest can: the digest covers the whole file, and a fabricated span
   * inside a file whose digest is right is exactly the defect the study measured. Only the
   * bytes can say.
   *
   * THREE THINGS BOUND WHAT IT COSTS. It is called only when the answer carries a quote worth
   * checking; it reads the material ONCE for the whole answer, however many citations there
   * are; and it decodes only the members a citation actually named. What is left is one
   * re-read of a sealed output per quoting answer, against the read the hub already performs
   * over every output of every settled job ({@link ingestOutputs}) — the same archive, the
   * same chunked transport, the same {@link MAX_OUTPUT_BYTES} ceiling.
   *
   * IT NEVER FAILS THE ANSWER. A preparation whose lease is gone, a machine that no longer
   * answers, a material past the ceiling: each returns null for that file, the citation is
   * recorded `unchecked` with the reason, and the run settles. A hub that refused a claim
   * because it could not reach the bytes would be punishing the model for the hub's own reach.
   */
  async function quotedSessions(
    run: PendingRun,
    wanted: ReadonlySet<string>,
  ): Promise<SessionLines> {
    const held = new Map<string, readonly string[]>();
    const prepareJobId = run.prepare_job_id;
    if (prepareJobId !== null && prepareJobId !== "" && wanted.size > 0) {
      try {
        const state = await jobs.status({
          kind: "job",
          machineId: run.machine_id,
          operationId: OPERATIONS.prepare,
          jobId: prepareJobId,
        });
        const sealed = (state.result?.outputs ?? []).find(
          (output) => output.name === MATERIAL_OUTPUT,
        );
        if (sealed !== undefined) {
          const bytes = await readOutput(
            jobs,
            {
              kind: "output",
              machineId: run.machine_id,
              operationId: OPERATIONS.prepare,
              jobId: prepareJobId,
              outputId: sealed.outputId,
            },
            sealed.bytes,
          );
          for (const member of tarMembers(bytes)) {
            if (!wanted.has(member.name)) continue;
            held.set(member.name, materialLines(TEXT.decode(member.body)));
          }
        }
      } catch {
        // The reason a reader needs is on the citation, not here: an empty map answers null
        // for every file, and `checkCitations` writes `unchecked` against each one.
        held.clear();
      }
    }
    return (entry) => held.get(entry.file) ?? null;
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

  function jsonRecord(value: string | null): Record<string, unknown> | undefined {
    if (value === null || value === "") return undefined;
    try {
      const parsed: unknown = JSON.parse(value);
      return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)
        ? (parsed as Record<string, unknown>)
        : undefined;
    } catch {
      return undefined;
    }
  }

  /** The immutable, blinded record revision a review session is shown. */
  async function project(recordId: string): Promise<ReviewProjection | null> {
    const records = await store.db.query<{
      id: string;
      kind: string;
      root_id: string;
      parent_id: string | null;
      title: string;
      created_at: string;
      payload: string;
    }>(
      `SELECT id, kind, root_id, parent_id, title, created_at, payload
         FROM records WHERE id = ? LIMIT 1`,
      [recordId],
    );
    const record = records[0];
    if (record === undefined) return null;
    let payload: unknown = {};
    try {
      // Withheld review state is removed here, not merely detected downstream: an imported
      // record's own payload carries the keys the reviewer must not see.
      payload = blinded(JSON.parse(record.payload));
    } catch {
      payload = {};
    }
    const sources = await store.db.query<{
      selector: string;
      harness: string;
      title: string;
      workspace: string;
      repository_remote: string | null;
      content_digest: string;
    }>(
      `SELECT s.selector, s.harness, s.title, s.workspace, s.repository_remote,
              s.content_digest
         FROM edges e JOIN sessions s ON s.selector = e.to_id
        WHERE e.from_id = ? AND e.kind = 'cites' AND e.to_kind = 'session'
        ORDER BY e.position, s.selector`,
      [recordId],
    );
    return {
      target: {
        id: record.id,
        kind: record.kind,
        root_id: record.root_id,
        parent_id: record.parent_id,
        title: record.title,
        created_at: record.created_at,
        payload,
      },
      sources,
    };
  }

  async function settleReviewSession(
    at: number,
    run: PendingRun,
    read: SessionRead,
    reportedClosure: Receipt["closure"],
    preparation: ReviewPreparation,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
    refusals: Counter,
  ): Promise<void> {
    const session = read.session;
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
    let reason = "";
    let skippedResult = false;
    let refusedContributions: RefusedContribution[] = [];
    let submittedPayload: unknown = null;
    let acceptedRows: Readonly<Record<string, readonly Record<string, string | number | null>[]>> =
      {};
    const projection = await project(preparation.recordId);
    if (session === null) {
      reason =
        `${REFUSALS.empty}: the review session closed as ${read.job.state} and sealed no transcript, ` +
        "so it submitted no result";
    } else if (session.exitCode !== 0) {
      reason = `${REFUSALS.schema}: the review session exited ${String(session.exitCode)} and submitted no result`;
    } else if (projection === null) {
      reason = `${REFUSALS.unknownReference}: record ${preparation.recordId} is no longer readable`;
    } else {
      const verdict = reviewVerdict(preparation, session.finalMessage, projection.target);
      reason = verdict.reason;
      refusedContributions = [...verdict.refused];
      submittedPayload = verdict.submitted;
      if (verdict.result !== null) {
        skippedResult = verdict.result.skip !== "";
        acceptedRows = reviewRows(preparation, verdict.result, run.id, new Date(at).toISOString());
      }
    }

    const authorityParams: readonly SqlParam[] = [
      preparation.assignmentId,
      preparation.fence,
      run.job_id,
    ];
    const liveAuthority: SqlCondition = {
      sql:
        `EXISTS (SELECT 1 FROM claims WHERE id = ? AND fence = ? AND job_id = ? ` +
        `AND finished_at IS NULL)`,
      params: authorityParams,
    };
    const staleAuthority: SqlCondition = {
      sql:
        `NOT EXISTS (SELECT 1 FROM claims WHERE id = ? AND fence = ? AND job_id = ? ` +
        `AND finished_at IS NULL)`,
      params: authorityParams,
    };
    const counts: Record<string, number> = {};
    const statements: SqlStatement[] = [
      {
        // A no-op write makes the authority check and every guarded output below one SQLite
        // write transaction. A takeover before this statement wins and suppresses the review;
        // one after it waits until the accepted rows and terminal receipt are durable.
        sql:
          `UPDATE claims SET job_id = job_id ` +
          `WHERE id = ? AND fence = ? AND job_id = ? AND finished_at IS NULL ` +
          `RETURNING run_id`,
        params: authorityParams,
      },
    ];
    if (reason === "") {
      for (const file of INGEST_ORDER) {
        const ingest = INGEST[file];
        const rows = acceptedRows[file] ?? [];
        if (ingest === undefined || rows.length === 0) continue;
        for (const row of rows) {
          const refused = refuseRow(ingest.table, row);
          const statement = refused === null ? rowStatement(ingest, row, liveAuthority) : null;
          if (refused !== null || statement === null) {
            reason = `${refused?.code ?? REFUSALS.schema}: ${refused?.message ?? `${file} contains a row outside the store schema`}`;
            break;
          }
          statements.push(statement);
          counts[file] = (counts[file] ?? 0) + 1;
        }
        if (reason !== "") break;
      }
    }
    if (reason !== "") {
      statements.splice(1);
      for (const key of Object.keys(counts)) delete counts[key];
    }
    const receiptClosure: Receipt["closure"] =
      reason === ""
        ? skippedResult
          ? "skipped"
          : "completed"
        : session === null
          ? reportedClosure
          : "failed";
    const base: Receipt = {
      runId: run.id,
      kind: "evaluate",
      machineId: run.machine_id,
      recipeId: preparation.recipe.id,
      role: preparation.role,
      ...(jsonRecord(run.profile) === undefined ? {} : { profile: jsonRecord(run.profile) }),
      ...(session === null ? {} : { model: session.model, models: [session.model] }),
      preparation: {
        review: preparation,
        jobVersion: REVIEW_JOB_VERSION,
        promptVersion: REVIEW_PROMPT_VERSION,
        blindingPolicyVersion: REVIEW_BLINDING_POLICY_VERSION,
      },
      startedAt: run.started_at,
      finishedAt: new Date(at).toISOString(),
      closure: receiptClosure,
      ...(reason === "" ? {} : { reason }),
      costUsd,
      tokens: usage === null ? 0 : usage.input + usage.output,
      counts,
    };
    // `counts` itself stays the per-file row count the ingest reports; the refused contributions
    // are counted onto the RECEIPT's copy of it, where "how did this review's spend land" is read.
    const withRefusals: Receipt =
      refusedContributions.length === 0
        ? base
        : {
            ...base,
            refusedContributions,
            counts: { ...counts, contributionsRefused: refusedContributions.length },
          };
    // A REFUSED REVIEW KEEPS WHAT IT SUBMITTED. The sentence alone made "did this class of
    // refusal fall?" and "did the judgement change under refusal?" unanswerable from the store
    // (#311); the payload is the model's own answer, unedited, because an edited one is not
    // evidence. Too large to keep is reported as its size rather than truncated into something
    // nobody submitted.
    const receipt: Receipt =
      reason === "" || submittedPayload === null
        ? withRefusals
        : { ...withRefusals, rejectedSubmission: rejectedSubmission(submittedPayload) };
    const target: IngestTarget = {
      runId: run.id,
      jobId: run.job_id,
      machineId: run.machine_id,
      operationId: run.kind,
      outputs: [],
      closure: receipt.closure,
      inference,
    };
    const staleReason =
      `${REFUSALS.authority}: assignment ${preparation.assignmentId} fence ` +
      `${String(preparation.fence)} is no longer held by job ${run.job_id}`;
    const staleReceipt: Receipt = {
      // FROM `base`, NOT FROM `receipt`: a takeover suppressed this submission entirely, so the
      // refused contributions are not this epoch's account of anything either.
      ...base,
      closure: "failed",
      reason: staleReason,
      counts: {},
    };
    const callBase = {
      runId: run.id,
      at,
      machineId: run.machine_id,
      session,
      closure: receipt.closure,
      reason,
    } as const;
    statements.push(
      runStatement(run.id, target, receipt, counts, liveAuthority),
      runStatement(run.id, { ...target, closure: "failed" }, staleReceipt, {}, staleAuthority),
      // THE CALL IS RECORDED UNDER WHICHEVER EPOCH WON, on the same two guards and after the
      // run row so the reference has something to point at. A takeover suppressed the
      // SUBMISSION, never the call: the model answered and the deployment paid, and a trace
      // that dropped the row would make a spent review look like one that never happened.
      callStatement(sessionCall(callBase), liveAuthority),
      callStatement(
        sessionCall({ ...callBase, closure: "failed", reason: staleReason }),
        staleAuthority,
      ),
    );
    const results = await store.db.batch(statements);
    const claimRunId = results[0]?.[0]?.["run_id"];
    const authorized = typeof claimRunId === "string";
    const finalReason = authorized ? reason : staleReason;
    const finalReceipt = authorized ? receipt : staleReceipt;
    const finalCounts = authorized ? counts : {};
    const refusedCode = finalReason === "" ? null : refusalCode(finalReason);
    if (refusedCode !== null) count(refusals, refusedCode);
    await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
    store.touch();
    if (finalReason !== "") notes.push(`run ${run.id}: ${finalReason}`);
    else if (authorized && refusedContributions.length > 0) {
      // A REVIEW THAT STOOD STILL SAYS WHAT THE CONTRACT REFUSED. The tally is where "how much
      // did the contract refuse today, and under which code" is answered, and a contribution
      // dropped out of a recorded review would otherwise be invisible — which is the half of
      // #305 that strictness alone was never going to answer. A review refused WHOLE is counted
      // once, above, by the sentence it failed on: the two are never counted for the same defect.
      for (const dropped of refusedContributions) {
        const code = refusalCode(dropped.reason);
        if (code !== null) count(refusals, code);
      }
      notes.push(
        `run ${run.id}: recorded the review and refused ` +
          `${String(refusedContributions.length)} of its contributions: ` +
          refusedContributions.map((dropped) => dropped.reason).join("; "),
      );
    }
    ingested.push({
      runId: run.id,
      jobId: run.job_id,
      closure: finalReceipt.closure,
      costUsd,
      rows: finalCounts,
      skipped: 0,
    });
    if (!authorized) return;
    const outcome =
      reason === ""
        ? skippedResult
          ? "skipped"
          : "completed"
        : session === null
          ? "skipped"
          : "failed";
    const finished = await coordinator.finish({
      id: preparation.assignmentId,
      runId: claimRunId,
      fence: preparation.fence,
      cost: costUsd,
      outcome,
    });
    settled.push(
      finished.outcome === "finished"
        ? {
            claimId: preparation.assignmentId,
            outcome,
            cost: finished.cost,
            overrun: finished.overrun,
            refused: null,
            reason: null,
          }
        : {
            claimId: preparation.assignmentId,
            outcome,
            cost: costUsd,
            overrun: false,
            refused: finished.refusal.reason,
            reason: null,
          },
    );
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
   * ONE FINISHED TITLING SESSION, TURNED INTO ONE ANSWER PER SESSION IT WAS OFFERED (#342).
   *
   * EVERY OFFERED SELECTOR IS ANSWERED, whatever happened. A title where the model wrote one
   * and the reason where it did not — the session it declined, the log its preparation never
   * sealed, the one it simply left out, and, when the whole submission was refused, all of
   * them at once. A selector with no row is one the next cycle offers again, so an unanswered
   * session is not a gap in a report: it is an unbounded loop of preparations and prompts
   * over the same batch.
   *
   * A REFUSED SUBMISSION IS SPEND here exactly as it is for an exploration: the receipt
   * carries the cost and the refusal's own code, and the tally counts it. What is different
   * is that the refusal still writes rows — they are the record of what was paid for and what
   * it bought, which for a refused batch is nothing but the knowledge not to ask again.
   *
   * NO CLAIM SETTLES HERE. A claim is one reviewer's grant on one record in one role and a
   * titling batch is none of those; what bounded this run was `inferTitles`, against the same
   * two ceilings and the same ledger, before the preparation was ever posted.
   */
  async function settleTitleSession(
    at: number,
    run: PendingRun,
    read: SessionRead,
    closure: Receipt["closure"],
    offered: readonly string[],
    ingested: IngestedRun[],
    notes: string[],
    refusals: Counter,
  ): Promise<void> {
    const session = read.session;
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

    // The sessions the preparation actually sealed. An offered selector missing from the index
    // is a log that went away between the catalog and the machine: the model was never shown
    // it, so the honest answer for it is that and not "the model said nothing".
    const material = await materialOf(run.prepare_job_id);
    const sealed = new Set((material?.sessions ?? []).map((entry) => entry.selector));
    const unsealed = offered.filter((selector) => !sealed.has(selector));
    const shown = offered.filter((selector) => sealed.has(selector));

    let reason = "";
    let answers: readonly InferredTitle[] = [];
    if (session === null) {
      reason =
        `${REFUSALS.empty}: the session closed as ${read.job.state} and sealed no transcript, ` +
        `so it submitted no result`;
    } else if (session.exitCode !== 0) {
      reason = `${REFUSALS.schema}: the session exited ${String(session.exitCode)} and submitted no result`;
    } else {
      const answer = readTitleAnswer(session.finalMessage, shown);
      if ("refused" in answer) reason = answer.refused;
      else answers = answer.titles;
    }
    const refusedCode = reason === "" ? null : refusalCode(reason);
    if (refusedCode !== null) count(refusals, refusedCode);
    const written = [
      ...(reason === "" ? answers : declinedTitles(shown, reason)),
      ...declinedTitles(
        unsealed,
        `the preparation ${run.prepare_job_id ?? ""} sealed no log for this session`,
      ),
    ];
    const named = written.filter((answer) => answer.title !== "").length;
    const counts: Record<string, number> = {
      offered: offered.length,
      named,
      unnamed: written.length - named,
    };

    const receipt: Receipt = {
      runId: run.id,
      kind: "title",
      machineId: run.machine_id,
      ...(namedAccount(run.profile) === undefined ? {} : { account: namedAccount(run.profile) }),
      ...(session === null ? {} : { model: session.model }),
      ...(preparationOf(run.preparation) === undefined
        ? {}
        : { preparation: preparationOf(run.preparation) }),
      startedAt: run.started_at,
      finishedAt: new Date(at).toISOString(),
      closure: reason === "" ? "completed" : session === null ? closure : "failed",
      ...(reason === "" ? {} : { reason }),
      costUsd,
      tokens: usage === null ? 0 : usage.input + usage.output,
      ...(session === null ? {} : { models: [session.model] }),
      counts,
    };

    // THE ANSWERS FIRST AND THE RUN ROW LAST, which is the same crash contract the exploration
    // path is arranged by: a settlement that died between the two leaves the run unsettled,
    // the reaper reads the same session again, and every statement above is a no-op on the
    // rows that already landed.
    const produced = titleStatements({
      runId: run.id,
      at: new Date(at).toISOString(),
      titles: written,
    });
    for (let from = 0; from < produced.length; from += STATEMENTS_PER_BATCH) {
      await store.db.batch(produced.slice(from, from + STATEMENTS_PER_BATCH));
    }
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
        counts,
      ),
      callStatement(
        sessionCall({
          runId: run.id,
          at,
          machineId: run.machine_id,
          session,
          closure: receipt.closure,
          reason,
        }),
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
      rows: { [JOB_OUTPUT_FILES.sessions]: named },
      skipped: 0,
    });
  }

  /**
   * ONE FINISHED CODE SESSION, TURNED INTO A RECEIPT AND A SETTLED CLAIM (#279).
   *
   * The transcript is Code's; what Babel owns is the CONTRACT the prompt stated, and this is
   * where it is enforced: the answer is the last fenced block of the final message
   * ({@link readExploreAnswer}), and every locator it cites must name a file the material's
   * index served at the digest the index recorded ({@link unservedCitation}).
   *
   * THE TWO CITATION CHECKS REFUSE DIFFERENTLY, AND `engine/citations.ts` HOLDS THE REASON. A
   * path outside the served selection refuses the whole answer, because it is a claim about
   * bytes nobody served and nothing later can check it. A quote that is not at the line it
   * names is RECORDED on the record's own evidence and refuses nothing, because it is a claim
   * about real bytes that is wrong about where they are, and discarding a paid run over one is
   * the all-or-nothing waste #231 and #311 measured.
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
    const preparedReview = reviewPreparation(preparationOf(run.preparation));
    if (preparedReview !== null) {
      await settleReviewSession(
        at,
        run,
        read,
        closure,
        preparedReview,
        ingested,
        settled,
        notes,
        refusals,
      );
      return;
    }
    const offered = offeredSelectors(preparationOf(run.preparation));
    if (offered.length > 0) {
      await settleTitleSession(at, run, read, closure, offered, ingested, notes, refusals);
      return;
    }
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

    // WHAT THE ANSWER WAS WORTH, AND THE ROWS IT CLAIMED. A session that never sealed a
    // transcript submitted nothing, and the reason says which of the job's own endings that was
    // rather than inventing a schema refusal about a message that was never written.
    //
    // A REFUSAL WRITES NOTHING AND STILL COSTS. The rows are built only once the answer parsed,
    // every locator resolved against the material, and the material itself is readable — and
    // they are built BEFORE the receipt, because a row the store's own schema refuses is itself
    // a refusal of the answer and has to reach the receipt's `reason` like any other.
    let reason = "";
    const produced: SqlStatement[] = [];
    const counts: Record<string, number> = {};
    // WHAT THE CITATIONS TURNED OUT TO BE, for every answer that got as far as being checked.
    // It is undefined rather than empty on a run that never submitted one, because "this run
    // wrote no citations" and "nobody looked" are different facts about a receipt (#348).
    let citations: Record<string, number> | undefined;
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
        const unserved = unservedCitation(answer.result, served);
        if (unserved !== "") {
          reason = `${REFUSALS.unknownReference}: ${unserved}`;
        } else if (material === null) {
          // The selection is how a claim is checkable at all: a result admitted against a
          // material nobody can read is an unverifiable claim recorded as a verified one.
          reason =
            `${REFUSALS.unknownReference}: the material of prepare job ` +
            `${run.prepare_job_id ?? "(none)"} is not on any settled run of this hub, so this ` +
            `run's citations cannot be checked against what it was served`;
        } else {
          // WHICH OF THE RECORDS THIS ANSWER'S MARKERS NAME THIS HUB ACTUALLY HOLDS (#347). A
          // record whose text opens `CONTRADICTS hyp_…` gets the edge, and a marker naming
          // something nobody holds is dropped with a note rather than pointing an edge at
          // nothing. The rows are built synchronously and the store is not, so the answer is
          // asked for once, here, instead of the writer reaching for a database.
          const named = markerReferences(answer.result);
          const holds = new Set<string>();
          for (let from = 0; from < named.length; from += MAX_SQL_PARAMS) {
            const asked = named.slice(from, from + MAX_SQL_PARAMS);
            const held = await store.db.query<{ id: string }>(
              `SELECT id FROM records WHERE id IN (${asked.map(() => "?").join(", ")})`,
              asked,
            );
            for (const row of held) holds.add(row.id);
          }
          // WHAT THE ANSWER'S QUOTES ACTUALLY SAY (#348). The bytes are read once, for the
          // sessions a quote names and no others, and a citation nobody could read comes back
          // `unchecked` rather than accused. The verdicts travel into the payload beside the
          // citations they belong to; none of them refuses anything.
          // The file is taken from the ADMITTED ENTRY and never from the cited string: the
          // path a model wrote selects an index entry or nothing at all, and no byte is ever
          // opened by a name it chose (`engine/citations.ts`).
          const quoted = new Set<string>();
          for (const evidence of citedEvidence(answer.result)) {
            if (evidence.locator.quote.trim() === "") continue;
            const entry = admitCitation(evidence.locator.path, served);
            if (entry !== null) quoted.add(entry.file);
          }
          const checks = checkCitations(answer.result, served, await quotedSessions(run, quoted));
          citations = citationTally(checks);
          for (const note of citationNotes(checks)) notes.push(`run ${run.id}: ${note}`);
          const written = exploreRows(answer.result, {
            runId: run.id,
            at: new Date(at).toISOString(),
            sessions: served,
            holds,
            checks,
          });
          if ("refusal" in written) {
            reason = refusalReason(written.refusal);
          } else {
            // THE SAME INGEST A SEALED OUTPUT GOES THROUGH: one output file per table, each row
            // in the table's own shape, `INSERT OR IGNORE` keyed by the row's own identifier.
            // Nothing between the answer and the table reinterprets a row, and a row carrying a
            // column this build's schema does not have is not written at all.
            for (const file of INGEST_ORDER) {
              const ingest = INGEST[file];
              const rows = written.rows[file] ?? [];
              if (ingest === undefined || rows.length === 0) continue;
              for (const row of rows) {
                const refused = refuseRow(ingest.table, row);
                const statement = refused === null ? rowStatement(ingest, row) : null;
                if (refused !== null || statement === null) {
                  reason =
                    `${refused?.code ?? REFUSALS.schema}: ` +
                    `${refused?.message ?? `${file} contains a row outside the store schema`}`;
                  break;
                }
                produced.push(statement);
                counts[file] = (counts[file] ?? 0) + 1;
              }
              if (reason !== "") break;
            }
            for (const note of written.notes) notes.push(`run ${run.id}: ${note}`);
          }
        }
      }
    }
    if (reason !== "") {
      produced.length = 0;
      for (const key of Object.keys(counts)) delete counts[key];
    }
    const refusedCode = reason === "" ? null : refusalCode(reason);
    if (refusedCode !== null) count(refusals, refusedCode);
    // WHAT THE PROMPT QUOTED THE OPERATOR AS SAYING (#331), written onto the run row when the
    // session was posted. It is on the receipt so a claim can be read against what the run was
    // told — which remarks, and how many the bound left out.
    const told = ReceiptSchema.shape.steering.safeParse(
      preparationOf(run.preparation)?.["steering"],
    );

    const receipt: Receipt = {
      runId: run.id,
      kind: "explore",
      machineId: run.machine_id,
      ...(namedAccount(run.profile) === undefined ? {} : { account: namedAccount(run.profile) }),
      ...(session === null ? {} : { model: session.model }),
      ...(preparationOf(run.preparation) === undefined
        ? {}
        : { preparation: preparationOf(run.preparation) }),
      ...(told.success && told.data !== undefined ? { steering: told.data } : {}),
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
      counts,
      ...(citations === undefined ? {} : { citations }),
    };
    /*
      THE ROWS FIRST, THE RUN ROW LAST, and the order is the crash contract.

      A `batch` is the transaction and there is no open handle, so an answer larger than one
      batch cannot be one: the engine takes 256 statements a call. Chunking is therefore how a
      large answer lands at all, and the ordering is what makes a crash between two chunks
      repairable — the run stays unsettled, the reaper reads the same session again, and every
      identifier is minted from the run and the model's own handle, so the replay's inserts are
      `INSERT OR IGNORE` no-ops on the rows that already landed. The reverse order would settle
      the run against records that were never written and nothing would ever go back for them.

      The run row is written through the SAME statement an ingested job's is: one shape for what
      a finished run looks like, whoever posted the job. `outputs` is empty because the rows a
      result becomes are not in a sealed lease here — the transcript is Code's job's `session`
      output, read on demand — and `inference` is what the meter said.
    */
    for (let from = 0; from < produced.length; from += STATEMENTS_PER_BATCH) {
      await store.db.batch(produced.slice(from, from + STATEMENTS_PER_BATCH));
    }
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
        counts,
      ),
      // IN THE RUN ROW'S OWN TRANSACTION, and after it, so the call cannot reference a run that
      // is not there and cannot survive a settlement that rolled back. It is written whether or
      // not the answer stood: a refused submission is paid work, and its refusal code is on the
      // row beside what it cost.
      callStatement(
        sessionCall({
          runId: run.id,
          at,
          machineId: run.machine_id,
          session,
          closure: receipt.closure,
          reason,
        }),
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
      rows: counts,
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
          (frame.data.stage === RUN_STAGES.atModel &&
            (frame.data.message ?? "") !== folded.message);
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
    machineIds: readonly string[],
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
    // THE BEAT'S OWN RUNS, asked of the machines this cycle is entitled to ask about — ids, as
    // {@link beatMachines} explains. It used to be every distinct `sessions.host`, which on an
    // imported corpus is a host name the hub cannot resolve: one note per cycle per name, about
    // a machine that was never the one being described.
    for (const machineId of machineIds) {
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
   *
   * AND ONLY A MACHINE ID IS EVER ASKED. `sessions.host` is the machine a `scan` ran on, but an
   * imported corpus holds the operator's host NAME there ({@link beatMachines}), and the hub
   * resolves no names: asking about one buys a refusal about a machine that does not exist,
   * every cycle, for every folder of every row the importer wrote. Those rows are left alone
   * and NAMED IN A NOTE instead — silence here would be the same silence that hid an
   * unregistered cadence for a day, and the cure is to re-catalogue the corpus under the
   * machine's id rather than to guess which id a name meant.
   */
  async function identifyFolders(machineIds: readonly string[], notes: string[]): Promise<void> {
    const asked = machineIds.map(() => "?").join(", ");
    const unasked = await store.db.query<HostRow>(
      `SELECT DISTINCT host FROM sessions
        WHERE host <> '' AND workspace LIKE '/%'
          AND (repository_reason IS NULL OR repository_reason NOT IN (${HUB_REASON_HOLES}))
          ${machineIds.length === 0 ? "" : `AND host NOT IN (${asked})`}
        ORDER BY host`,
      [...MACHINE_REPOSITORY_REASONS, ...machineIds],
    );
    if (unasked.length > 0) {
      notes.push(
        `the folders catalogued under ${unasked.map((row) => row.host).join(", ")} are not asked ` +
          `about: sessions.host holds a host name there rather than a machine id, and the hub ` +
          `resolves no names`,
      );
    }
    if (machineIds.length === 0) return;
    const unidentified = await store.db.query<UnidentifiedFolder>(
      `SELECT DISTINCT host AS machineId, workspace FROM sessions
        WHERE host IN (${asked}) AND workspace LIKE '/%'
          AND (repository_reason IS NULL OR repository_reason NOT IN (${HUB_REASON_HOLES}))
        ORDER BY host, workspace
        LIMIT ?`,
      [...machineIds, ...MACHINE_REPOSITORY_REASONS, WORKSPACES_PER_TICK],
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

  async function dispatchReviews(
    policy: Policy,
    at: number,
    cycleRunId: string,
    requested: RequestedJob[],
    settled: SettledClaim[],
    refused: RefusedDraw[],
  ): Promise<{ readonly stop: Stop | null; readonly gaps: readonly Gap[] }> {
    const route = policy.review;
    if (route === undefined) {
      return {
        stop: {
          reason: "unrouted",
          detail:
            `policy ${policy.version} enables evaluation but names no Code profile and machine; ` +
            "install a new policy version with a review route",
        },
        gaps: [],
      };
    }
    const gaps: Gap[] = [];
    const machineId = route.machineId;
    const bound = policy.concurrentPerMachine ?? policy.batchSize;
    while (requested.length < policy.batchSize) {
      const open = await coordinator.open(at);
      if ((open.byMachine[machineId] ?? 0) >= bound) {
        return {
          stop: {
            reason: "batch",
            detail: `${machineId} already holds ${String(open.byMachine[machineId] ?? 0)} of ${String(bound)} review slots`,
          },
          gaps,
        };
      }
      const drawn = await coordinator.draw({ runId: cycleRunId, machines: [machineId], now: at });
      gaps.push(...drawn.gaps);
      if (drawn.outcome === "gap") return { stop: drawn.gap, gaps };
      const assignment: Assignment = drawn.assignment;
      const recipeId = route.roleRecipes[assignment.role];
      const recipe = route.recipes.find((candidate) => candidate.id === recipeId) as
        Recipe | undefined;
      if (recipe === undefined) {
        const detail = `the ${assignment.role} role names recipe ${JSON.stringify(recipeId)}, which policy ${policy.version} does not carry`;
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: "recipe",
          detail,
        });
        return { stop: { reason: "dispatch-refused", detail }, gaps };
      }
      const projection = await project(assignment.recordId);
      const leak = projection === null ? "" : blindedLeak(projection.target);
      if (projection === null || leak !== "") {
        const detail =
          projection === null
            ? `record ${assignment.recordId} is not readable`
            : `the blinded projection leaks withheld review state at ${leak}`;
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: "projection",
          detail,
        });
        return { stop: { reason: "dispatch-refused", detail }, gaps };
      }
      const payload = projection.target["payload"];
      const refinement =
        typeof payload === "object" && payload !== null
          ? (payload as Record<string, unknown>)["refinement"]
          : undefined;
      const heldDepth =
        typeof refinement === "object" && refinement !== null
          ? (refinement as Record<string, unknown>)["depth"]
          : undefined;
      const refinementDepth =
        typeof heldDepth === "number" && Number.isInteger(heldDepth) && heldDepth >= 0
          ? heldDepth
          : 0;
      const claimed = await coordinator.claim({ assignment, runId: cycleRunId, now: at });
      if (claimed.outcome === "refused") {
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: claimed.refusal.reason,
          detail: claimed.refusal.detail,
        });
        return { stop: { reason: "dispatch-refused", detail: claimed.refusal.detail }, gaps };
      }
      const runId = `run_${assignment.id}_${String(claimed.claim.fence)}`;
      const preparation: ReviewPreparation = {
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        revisionId: assignment.recordId,
        rootId: assignment.rootId,
        kind: assignment.kind,
        role: assignment.role,
        lane: assignment.lane,
        policyVersion: assignment.policyVersion,
        fence: claimed.claim.fence,
        ordinal: assignment.ordinal,
        seed: assignment.seed,
        inputDigest: assignment.inputDigest,
        refinementDepth,
        maxRefinementDepth: route.maxRefinementDepth ?? 2,
        blinded: true,
        recipe: { id: recipe.id, version: recipe.version },
      };
      const prompt = composeReviewPrompt({ assignment, preparation, recipe, projection });
      const bytes = promptBytes(prompt);
      if (bytes > PROMPT_LIMIT) {
        const detail =
          `the ${assignment.role} review prompt is ${String(bytes)} bytes and Code accepts ` +
          `${String(PROMPT_LIMIT)}; shorten recipe ${recipe.id} rather than dropping the record contract`;
        settled.push(await release(claimed.claim, detail));
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: "prompt-too-large",
          detail,
        });
        return { stop: { reason: "dispatch-refused", detail }, gaps };
      }
      const answered = await engine.runSession({
        profile: route.profile,
        machineId,
        prompt,
      });
      if (!answered.ok) {
        settled.push(await release(claimed.claim, answered.refused));
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: answered.code,
          detail: answered.refused,
        });
        return { stop: { reason: "dispatch-refused", detail: answered.refused }, gaps };
      }
      const boundClaim = await coordinator.bind({
        id: claimed.claim.id,
        runId: cycleRunId,
        fence: claimed.claim.fence,
        jobId: answered.value.jobId,
      });
      if (boundClaim.outcome === "refused") {
        await engine.cancelSession({
          containerId: route.profile.containerId,
          jobId: answered.value.jobId,
        });
        const detail = `Code posted ${answered.value.jobId}, but its claim could not be bound: ${boundClaim.refusal.detail}`;
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: boundClaim.refusal.reason,
          detail,
        });
        return { stop: { reason: "dispatch-refused", detail }, gaps };
      }
      try {
        await store.db.run(
          `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id,
                            recipe_id, profile, authority_kind, authority_id, preparation,
                            started_at, records, payload)
           VALUES (?, ?, ?, ?, ?, NULL, ?, ?, 'conductor', ?, ?, ?, 0, ?)`,
          [
            runId,
            OPERATIONS.evaluate,
            machineId,
            answered.value.jobId,
            route.profile.containerId,
            recipe.id,
            JSON.stringify(route.profile),
            cycleRunId,
            JSON.stringify({ review: preparation }),
            new Date(at).toISOString(),
            JSON.stringify({ closure: null, requestedAt: at }),
          ],
        );
      } catch (error) {
        await engine.cancelSession({
          containerId: route.profile.containerId,
          jobId: answered.value.jobId,
        });
        const detail = `Code posted ${answered.value.jobId}, but Babel could not retain its run: ${message(error)}`;
        settled.push(await release(boundClaim.claim, detail));
        refused.push({
          assignmentId: assignment.id,
          recordId: assignment.recordId,
          reason: "retention",
          detail,
        });
        return { stop: { reason: "dispatch-refused", detail }, gaps };
      }
      store.touch();
      requested.push({
        runId,
        jobId: answered.value.jobId,
        machineId,
        claimId: assignment.id,
        recordId: assignment.recordId,
        role: assignment.role,
        lane: assignment.lane,
      });
    }
    // THE CYCLE FILLED THE BATCH ITSELF, which is the one stop that means the loop worked. It
    // is a different word from the `batch` above, where every slot is held by somebody else and
    // nothing is finishing: a panel given one word for both cannot tell a wedged deployment
    // from a busy one, and stays silent for the wedge (#382).
    return {
      stop: {
        reason: "batch-filled",
        detail: `cycle ${cycleRunId} dispatched its ${String(policy.batchSize)} reviews`,
      },
      gaps,
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

  /**
   * THE CYCLE'S OWN VERDICT, left where the `pulse` door can find it (#328).
   *
   * Only the newest one is kept: "why did nothing run" is a question about the cycle that just
   * ended, and a history of them is the hub's log, which already has every gap in full. The
   * day's counts are {@link rollUp}'s and are a different reading — this one is a single cycle,
   * and the two would answer the operator's question differently on the first tick after
   * midnight if they were one key.
   *
   * THE GAPS ARE FOLDED BY REASON HERE, at the only place that holds all of them: a cycle
   * contending with a second conductor declines every candidate it looks at, and a door that
   * shipped four hundred rows to a panel would have replaced an invisible loop with an
   * unreadable one. The first instance of each reason survives the fold, because a count says
   * how much and a record id says where to look.
   *
   * A key the host refuses is a note and never a raised cycle: the loop's work is done by the
   * time this runs, and losing the explanation must not lose the tick.
   */
  async function keepCycle(
    at: number,
    stop: Stop | null,
    gaps: readonly Gap[],
    notes: string[],
  ): Promise<void> {
    const counted = new Map<GapReason, { count: number; recordId: string; detail: string }>();
    for (const gap of gaps) {
      const held = counted.get(gap.reason);
      if (held === undefined) {
        counted.set(gap.reason, {
          count: 1,
          recordId: gap.recordId,
          detail: gap.detail.slice(0, DETAIL_KEPT),
        });
      } else held.count += 1;
    }
    const report: z.infer<typeof CycleReportSchema> = {
      at: new Date(at).toISOString(),
      stop:
        stop === null ? null : { reason: stop.reason, detail: stop.detail.slice(0, DETAIL_KEPT) },
      // Most declined first, and ties by the reason's own word, so two cycles of the same shape
      // read the same way down the page.
      gaps: [...counted]
        .sort(([left, a], [right, b]) => b.count - a.count || left.localeCompare(right))
        .map(([reason, seen]) => ({ reason, ...seen })),
    };
    try {
      await keys.set(CONDUCTOR_CYCLE_KEY, JSON.stringify(report));
    } catch (error) {
      notes.push(`the cycle's own verdict cannot be kept: ${message(error)}`);
    }
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
        await keepCycle(at, DISABLED_STOP, [], notes);
        return {
          at,
          cycleRunId,
          policyVersion: policy.version,
          enabled: false,
          schedule: schedule.state,
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

      const reconciled = await reconcileRuns(
        at,
        schedule.machines,
        ingested,
        settled,
        notes,
        refusals,
      );
      // …and the claims no settlement can reach are released before this cycle asks the
      // coordinator what may be drawn, so a batch held by dead workers is a batch of free slots
      // by the time it answers rather than one cycle later.
      await reapClaims(at, policy.leaseSeconds, settled, notes);
      // What a scan just catalogued is folders; what they ARE is the host's to say, and it is
      // asked here, after the rows exist and before this cycle spends anything.
      await identifyFolders(schedule.machines, notes);

      // WHETHER THE LOOP IS PARKED is still asked, and still recorded, because it is read off
      // the spend ledger of the runs that did happen — a review that was paid for and refused
      // is in it too, and the park heuristic is what keeps that from reading as a broken lane.
      const parked = await parkState(policy, at);
      if (parked !== null) notes.push(`the loop is parked: ${parked.reason}`);

      const dispatched =
        parked === null
          ? await dispatchReviews(policy, at, cycleRunId, requested, settled, refused)
          : { stop: null, gaps: [] as readonly Gap[] };
      const stop = dispatched.stop;
      const gaps = dispatched.gaps;
      for (const gap of gaps) count(gapsByReason, gap.reason);
      if (stop !== null) count(gapsByReason, stop.reason);
      const tick = { gaps: tallied(gapsByReason), refusals: tallied(refusals) };
      await keepCycle(at, stop, gaps, notes);

      return {
        at,
        cycleRunId,
        policyVersion: policy.version,
        enabled: true,
        schedule: schedule.state,
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
