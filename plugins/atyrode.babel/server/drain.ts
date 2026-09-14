import {
  BABEL_PLUGIN_ID,
  DRAIN_SPENDING_PRESETS,
  PRESET_OPERATIONS,
  type DrainPreset,
  type LaunchInput,
  type OperationName,
  type SessionChoice,
} from "../contract.ts";
import { clearBudget, ActRefused } from "../store/acts.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import {
  addSpend,
  closeDrain,
  deadlineOf,
  reconcileLive,
  recordLaunch,
  runningDrains,
  sample,
  saveFold,
  targetMet,
  type DrainRow,
  type LiveJob,
} from "../store/drains.ts";
import type { BabelStore } from "../store/store.ts";
import type { LaunchIdentity, Started } from "../doors/launch.ts";
import type { JobsSlice, RunPlan } from "./conductor.ts";
import type { BabelJobs } from "./plan.ts";

/*
  THE DRAIN CONTROLLER (#258): keep N jobs in flight until a target, a deadline or a stop.

  WHAT IT IS FOR. On 2026-09-13 an operator asked for one thing — spend this account's remaining
  week before it resets — and got four generations of shell loop around a CLI: 10 then 24 then 26
  then 36 concurrent processes chosen by guess on twelve cores, no target, no burn rate, and a
  self-stop that never fired once ("zero `.stopped` files across ten fans; every fan ended by a
  kill"). Two hours and fourteen minutes later the window had not moved a percent. This is the
  operation that was missing.

  WHAT IT IS NOT. It is not a second launcher: every job it posts goes through
  `launchMachinery`'s own `startExplore`/`startBeat`, so the document, the ceiling, the pinned
  installation and the run row are the ones the operator's own button produces — there is no
  second answer to what a run IS. It is not a second governor either: the standing `policies` row
  is never touched, the drain's concurrency lives in a `budgets` overlay with a TTL (#260), and
  nothing here edits a lease, a share or a version — the three things a draw is replayable
  against, and the five rewrites of them on 2026-09-13 are why assignment ids kept changing under
  jobs in flight.

  IT HAS NO CLOCK, and cannot have one: a plugin may not poll as an alternate scheduler. A tick
  happens when something has already woken this half — the operator opening the panel, or one of
  this plugin's own jobs settling — and a settlement is the wake that matters, because a
  settlement is exactly when a slot opens. Every step is idempotent, so two ticks in the same
  second do the work of one: the launch ids are DERIVED from the drain and its launch ordinal, so
  a retried tick re-posts the same job rather than a second one.

  WHAT IT CAN AND CANNOT DO IN A TICK. It can post jobs: the conductor's own dispatch has always
  posted from a tick, under whatever authority woke it. It can only ASK to cancel one: `cancel`
  is discharged against the caller's credential at the job's node, and a settled job's hook
  carries the authority the launch delegated, which does not include `jobs:cancel`. So closing on
  a target STOPS LAUNCHING — which is the self-stop, and is what makes the fan drain to zero —
  and asks the hub to cancel what is still running, recording the refusal as a note when it
  cannot. A job already at the model has been paid for; letting it finish and write its receipt
  is worth more than killing it. The operator's own `drain.stop` holds `jobs:cancel` at the
  operation and is where cancellation really lands.
*/

/** What a drain's tick reports, per drain, so a caller can say what happened. */
export interface DrainReport {
  readonly drainId: string;
  /** Jobs posted by this tick. */
  readonly launched: number;
  /** Jobs of this drain that closed since the last tick. */
  readonly settled: number;
  /** Jobs still in flight after this tick. */
  readonly live: number;
  /** The state it is in after this tick: `running`, or the ending it reached. */
  readonly state: DrainRow["state"];
  /** Why it ended, or empty. */
  readonly reason: string;
  /** What the tick could not do, in the hub's own words. */
  readonly notes: readonly string[];
}

/** The launch path this controller posts through; `launchMachinery` satisfies it. */
export interface DrainLaunch {
  startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  startBeat(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
}

export interface DrainDeps {
  readonly store: BabelStore;
  readonly coordinator: Coordinator;
  readonly launch: DrainLaunch;
  /** This wake's own job authority: what posts a job, and what may be asked to cancel one. */
  readonly jobs: BabelJobs;
  /**
   * What a run of this operation runs under. The third parameter is the session the run is
   * launched with (#279): a plan carries the model and the account rather than a profile
   * reference, so the drain hands its own stored session to every job it launches.
   */
  plan(policy: Policy, operationId: OperationName, session?: SessionChoice | undefined): RunPlan;
  now(): number;
}

const SPENDING: readonly string[] = DRAIN_SPENDING_PRESETS;

/**
 * The launch input one of this drain's jobs is posted with: the preset, the machine, the account
 * and the knobs the operator named, every time, unchanged. A controller that rebuilt the request
 * from defaults would widen or narrow the scope between the first job and the ninetieth.
 */
export function drainInput(row: DrainRow): LaunchInput {
  return {
    machineId: row.machineId,
    preset: row.preset,
    recipes: [...row.knobs.recipes],
    ...(row.knobs.sinceDays === undefined ? {} : { sinceDays: row.knobs.sinceDays }),
    ...(row.knobs.entityId === undefined ? {} : { entityId: row.knobs.entityId }),
    ...(row.knobs.minutes === undefined ? {} : { minutes: row.knobs.minutes }),
    ...(row.knobs.agentSessions === undefined ? {} : { agentSessions: row.knobs.agentSessions }),
  };
}

/** The operation a drain's preset posts, which is also the node its jobs are cancelled at. */
export function drainOperation(preset: DrainPreset): OperationName {
  return PRESET_OPERATIONS[preset];
}

/**
 * WHO THE NEXT JOB IS. Derived rather than minted, for two reasons: a settled job's hook has no
 * `newId` to mint from, and a derived id makes a retried tick post the same job again instead of
 * a second one. The ordinal is the drain's own launch count, which only ever rises.
 */
export function drainIdentity(row: DrainRow, ordinal: number): LaunchIdentity {
  const tail = `${row.id}_${String(ordinal)}`;
  return { runId: `run_${tail}`, jobId: `job_${tail}`, authorityId: row.startedBy };
}

/**
 * Ends a drain: the overlay cleared, the row closed, and the jobs it still holds asked to stop.
 *
 * THE OVERLAY IS CLEARED FIRST, because that is the part an operator cannot undo by waiting: a
 * drain that ended while its bound stayed raised is 2026-09-13's eval-policy-10 outliving its
 * drain by ninety minutes. An overlay that has already expired refuses the clear, and that
 * refusal is the standing policy already being in force — a note, never a failure.
 *
 * The cancels are asked for and not required: a tick woken by a settlement does not hold
 * `jobs:cancel`, and a drain that could not stop its last two jobs has still stopped launching,
 * which is what its target asked for. What could not be cancelled is named.
 */
export async function endDrain(
  deps: DrainDeps,
  row: DrainRow,
  state: Exclude<DrainRow["state"], "running">,
  reason: string,
  live: readonly LiveJob[],
): Promise<{ readonly cancelled: number; readonly notes: readonly string[] }> {
  const notes: string[] = [];
  if (row.budgetId !== "") {
    try {
      await clearBudget(
        deps.store,
        { id: row.budgetId, reason: `drain ${row.id} ended: ${reason}` },
        // The act's actor is this plugin's own loop: a tick has no principal, and the drain's
        // own `started_by` is who asked for the drain rather than who ended it.
        BABEL_PLUGIN_ID,
      );
    } catch (error) {
      notes.push(
        error instanceof ActRefused
          ? `the overlay ${row.budgetId} was not cleared: ${error.message}`
          : `the overlay ${row.budgetId} could not be cleared: ${message(error)}`,
      );
    }
  }
  const operationId = drainOperation(row.preset);
  let cancelled = 0;
  for (const job of live) {
    try {
      await deps.jobs.cancel({
        kind: "job",
        machineId: row.machineId,
        operationId,
        jobId: job.jobId,
      });
      cancelled += 1;
    } catch (error) {
      notes.push(`${job.jobId} was not cancelled: ${message(error)}`);
    }
  }
  const closed = await closeDrain(deps.store, row.id, state, reason);
  if (!closed) notes.push(`drain ${row.id} had already ended when this tick closed it`);
  deps.store.touch();
  return { cancelled, notes };
}

/**
 * One drain, moved on by one tick.
 *
 * The order is the whole of it: fold what closed BEFORE deciding, so a target met by the job
 * that just settled ends the drain instead of launching one more; then decide; then launch, so a
 * drain that is already over never posts again.
 */
async function tickDrain(deps: DrainDeps, row: DrainRow): Promise<DrainReport> {
  const at = deps.now();
  const notes: string[] = [];
  const seen = await reconcileLive(deps.store, row.live);
  for (const gone of seen.missing) {
    notes.push(
      `${gone.jobId} was launched with no run row to show for it, so its slot is released`,
    );
  }
  const settledSpend = seen.settled.reduce((total, run) => addSpend(total, run.spend), row.spent);
  const spent = addSpend(settledSpend, seen.inFlight);
  const closures = { ...row.closures };
  const refusals = { ...row.refusals };
  for (const run of seen.settled) {
    const closure = run.closure === "" ? "unknown" : run.closure;
    closures[closure] = (closures[closure] ?? 0) + 1;
    // A refusal is PAID work with no result (#265): counted where the receipt is, because the
    // run it is on is one this drain will have forgotten by the time anybody asks.
    if (run.refusal !== null) refusals[run.refusal] = (refusals[run.refusal] ?? 0) + 1;
  }
  await saveFold(deps.store, row.id, {
    live: seen.holding,
    spent: settledSpend,
    closures,
    refusals,
    samples: sample(row.samples, at, spent),
    settledNow: seen.settled.length,
  });

  const met = targetMet(row.target, spent);
  if (met !== "") {
    const ended = await endDrain(deps, row, "target", met, seen.holding);
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: seen.holding.length - ended.cancelled,
      state: "target",
      reason: met,
      notes: [...notes, ...ended.notes],
    };
  }
  const deadline = deadlineOf(row.target);
  if (deadline !== null && at >= deadline) {
    const why = `the deadline ${row.target.deadline ?? ""} has passed`;
    const ended = await endDrain(deps, row, "deadline", why, seen.holding);
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: seen.holding.length - ended.cancelled,
      state: "deadline",
      reason: why,
      notes: [...notes, ...ended.notes],
    };
  }

  // THE POLICY BEING DISABLED ENDS THE DRAIN, and ends it as the operator's own act rather than
  // as a failure: `launch` refuses every start under a disabled policy, so a controller that
  // kept trying would post nothing and report nothing for as long as the drain had left.
  const inForce = await deps.coordinator.policy(at);
  if (!inForce.policy.enabled) {
    const why = `the evaluation policy in force (${inForce.version}) was disabled`;
    const ended = await endDrain(deps, row, "stopped", why, seen.holding);
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: seen.holding.length - ended.cancelled,
      state: "stopped",
      reason: why,
      notes: [...notes, ...ended.notes],
    };
  }

  const operationId = drainOperation(row.preset);
  // The session is the drain's own, every time: the model and the account the operator named
  // when they started it, not whatever a later default would be (#267, #279).
  const plan = deps.plan(inForce.policy, operationId, row.session);
  const input = drainInput(row);
  const holding = [...seen.holding];
  let launched = 0;
  let refused = "";
  for (let slot = holding.length; slot < row.concurrent; slot += 1) {
    const identity = drainIdentity(row, row.jobsLaunched + launched);
    const started = SPENDING.includes(row.preset)
      ? await deps.launch.startExplore(identity, deps.jobs, input, plan)
      : await deps.launch.startBeat(identity, deps.jobs, input, plan);
    if ("refused" in started) {
      // ONE REFUSAL ENDS THE ROUND, not the drain. The next slot would ask the same thing of the
      // same machine with the same window and hear the same sentence, and a tick that asked
      // sixteen times would report one fact sixteen times. The drain stays running: the reason
      // may be a machine reconnecting or a concurrency ceiling that frees up on the next settle.
      refused = started.refused;
      notes.push(`no further job was launched: ${started.refused}`);
      break;
    }
    const job: LiveJob = { runId: started.runId, jobId: started.jobId, launchedAt: at };
    // The row is written before the array grows, so what it stores is what this tick actually
    // holds: the write is `live` plus this job, counted once whatever a retry does.
    await recordLaunch(deps.store, row.id, job, holding);
    holding.push(job);
    launched += 1;
  }

  // A DRAIN THAT HOLDS NOTHING AND CANNOT LAUNCH HAS FAILED, and says so rather than sitting at
  // "running" for the rest of its deadline reporting zero of everything. Holding nothing is the
  // test: while even one job is in flight there is a settlement coming that will try again.
  if (holding.length === 0 && refused !== "") {
    const why = `nothing could be launched: ${refused}`;
    const ended = await endDrain(deps, row, "failed", why, []);
    return {
      drainId: row.id,
      launched,
      settled: seen.settled.length,
      live: 0,
      state: "failed",
      reason: why,
      notes: [...notes, ...ended.notes],
    };
  }
  if (launched > 0) deps.store.touch();
  return {
    drainId: row.id,
    launched,
    settled: seen.settled.length,
    live: holding.length,
    state: "running",
    reason: "",
    notes,
  };
}

/**
 * Every running drain, moved on by one tick. Called from the plugin's own cycle, after the
 * conductor's tick: the conductor is what settles a finished job and writes what it spent, and a
 * controller that read the runs table first would decide against last cycle's numbers.
 *
 * A drain that throws does not stop the others: they are separate operator decisions about
 * separate machines, and one unreadable row is not a reason to stop spending a window that is
 * about to reset.
 */
export async function drainTick(deps: DrainDeps): Promise<readonly DrainReport[]> {
  let drains: readonly DrainRow[];
  try {
    drains = await runningDrains(deps.store);
  } catch (error) {
    return [
      {
        drainId: "",
        launched: 0,
        settled: 0,
        live: 0,
        state: "running",
        reason: "",
        notes: [`the running drains could not be read: ${message(error)}`],
      },
    ];
  }
  const reports: DrainReport[] = [];
  for (const row of drains) {
    try {
      reports.push(await tickDrain(deps, row));
    } catch (error) {
      reports.push({
        drainId: row.id,
        launched: 0,
        settled: 0,
        live: row.live.length,
        state: row.state,
        reason: "",
        notes: [`this drain could not be moved on: ${message(error)}`],
      });
    }
  }
  return reports;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
