import {
  DRAIN_SPENDING_PRESETS,
  PRESET_OPERATIONS,
  type DrainEnding,
  type DrainPreset,
  type DrainSpend,
  type LaunchInput,
  type OperationName,
  type DrainProfile,
} from "../contract.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import {
  activeDrains,
  addSpend,
  closeDrain,
  deadlineOf,
  finishDrain,
  reconcileLive,
  recordLaunch,
  sample,
  saveFold,
  targetMet,
  type DrainRow,
  type DrainsStore,
  type LiveJob,
  type Reconciled,
} from "../store/drains.ts";
import type { BabelStore } from "../store/store.ts";
import type { LaunchIdentity, Started } from "../doors/launch.ts";
import type { JobsSlice, RunPlan } from "./conductor.ts";
import type { CodeEngine } from "./engine/session.ts";
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
  is never touched and there is no overlay to set — `doors/drain.ts` says the whole of it: an
  overlay moves admission numbers, and a drain's jobs, launched directly, consult none of them.
  THE FAN IS BOUNDED WHERE IT IS REAL, twice: the door refuses a `concurrent` above the
  manifest's `concurrentJobs` for the operation the preset posts, and this controller launches
  only into the slots its own live jobs leave free. Nothing here edits a lease, a share or a
  version — the three things a draw is replayable against, and the five rewrites of them on
  2026-09-13 are why assignment ids kept changing under jobs in flight.

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

  AND WHAT IT CANNOT CANCEL, IT KEEPS. A drain that stopped launching with jobs still out goes to
  `closing` holding them: they were paid for, their receipts are part of what this drain spent,
  and every later tick folds them until none is left and the recorded ending is taken. Dropping
  them at the close — which is what emptying `live` did — under-reported the drain's own total by
  up to (N−1) runs, on the ordinary path rather than in an edge case: the tick that meets a
  target is usually a settlement's, and a settlement's tick cannot cancel anything.
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
    engine: CodeEngine,
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
   * Babel's side of Code's doors, over this wake's own authority (#279). A drain posts a Code
   * session exactly as the button does, so the engine travels with the launch path rather than
   * being rebuilt here: two objects reaching Code under two authorities is how one of them ends
   * up spending a principal nobody graded.
   */
  readonly engine: CodeEngine;
  /**
   * What a run of this operation runs under. The third parameter is the CODE PROFILE the drain
   * was started on, with Babel's ledger of what Code said it runs as (#279): the drain hands
   * its own stored profile to every job it launches, never a default re-read later.
   */
  plan(policy: Policy, operationId: OperationName, profile?: DrainProfile | undefined): RunPlan;
  now(): number;
}

const SPENDING: readonly string[] = DRAIN_SPENDING_PRESETS;

/**
 * The launch input one of this drain's jobs is posted with: the preset, the machine, the Code
 * profile and the knobs the operator named, every time, unchanged. A controller that rebuilt the
 * request from defaults would widen or narrow the scope between the first job and the ninetieth
 * — and, since #279, would post the ninetieth against a different profile than the first.
 *
 * THE SESSION TRAVELS TOO, and it is never posted to Code: it is the NAME of the account this
 * drain exists to spend, recorded on every run row the fan writes, so "which window did that
 * fan burn" is answered by summing the runs that named it (#267) rather than by trusting that
 * the profile still points where it did at the start.
 */
export function drainInput(row: DrainRow): LaunchInput {
  return {
    machineId: row.machineId,
    preset: row.preset,
    recipes: [...row.knobs.recipes],
    profile: row.profile.profile,
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

/** What one fold wrote, and what the read of this drain's jobs could not account for. */
export interface Folded {
  /** Everything this drain has metered: the receipts that landed, plus what its live jobs report. */
  readonly spent: DrainSpend;
  /** Jobs held with no run row, named: their slots were released rather than held for ever. */
  readonly notes: readonly string[];
}

/**
 * WHAT THIS DRAIN'S JOBS HAVE DONE, folded onto its row: the receipts that landed since the last
 * fold added to `spent`, their closures and refusals tallied, the jobs still running kept, and
 * one observation taken so a rate has two to work from.
 *
 * IT IS WHAT EVERY READER OF {@link reconcileLive} DOES NEXT, controller or door, and it is the
 * same fold in both because the row is one row. A caller that closed the drain on what was still
 * running WITHOUT folding first wrote `live` without the settled jobs in it, and no later tick
 * could find them again: the receipt of a job that closed between the last tick and an
 * operator's stop — one cycle's own window, or a settle hook cut off by its two-second lease —
 * was simply missing from the total §11.5 asks the operator to read (the review of #285).
 *
 * Idempotent on the same rule as the rest of the controller: a settled job leaves `live`, so a
 * second fold over the same receipt has nothing left to add.
 */
export async function foldDrain(
  store: DrainsStore,
  row: DrainRow,
  seen: Reconciled,
  at: number,
): Promise<Folded> {
  const notes: string[] = [];
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
  await saveFold(store, row.id, {
    live: seen.holding,
    spent: settledSpend,
    closures,
    refusals,
    samples: sample(row.samples, at, spent),
    settledNow: seen.settled.length,
  });
  return { spent, notes };
}

/** What ending a drain did: the state it is in now, what was cancelled, and what it could not do. */
export interface Ended {
  /** `closing` while it still holds a job whose receipt is owed, otherwise the ending itself. */
  readonly state: DrainRow["state"];
  readonly cancelled: number;
  readonly notes: readonly string[];
}

/**
 * Ends a drain: the jobs it still holds asked to stop, and the row closed on them.
 *
 * The cancels are asked for and not required: a tick woken by a settlement does not hold
 * `jobs:cancel`, and a drain that could not stop its last two jobs has still stopped launching,
 * which is what its target asked for. What could not be cancelled is named.
 *
 * WHAT IT HOLDS IT KEEPS HOLDING, cancelled or not. A cancel is a request and a receipt is what
 * answers it: the job settles later either way, and what it metered is this drain's spend. So the
 * row goes to `closing` with every job it held — {@link finishDrain} takes the ending when the
 * last of them has settled — and only a drain holding nothing ends here and now.
 *
 * There is NO OVERLAY TO CLEAR. A drain's jobs are launched directly and consult no ceiling of
 * the standing policy, so it never moved one (`doors/drain.ts` says the whole of it): what used
 * to be unwound here was a number nothing read.
 */

/**
 * THE CODE WORKSPACE A RUN WAS POSTED ON, or empty for a job of Babel's own.
 *
 * It is the one column that says which of the two lanes a job is in, and therefore which verb
 * stops it: `ctx.jobs.cancel` for a job this plugin posted, `code.cancelSession` for a session
 * `atyrode.code` posted under `atyrode.omp`'s operation.
 */
async function containerOf(store: DrainDeps["store"], runId: string): Promise<string> {
  const rows = await store.db.query<{ container_id: string | null }>(
    `SELECT container_id FROM runs WHERE id = ?`,
    [runId],
  );
  return rows[0]?.container_id ?? "";
}
/**
 * WHAT A DRAIN'S END DOES TO WHAT IT IS HOLDING: asks for each live job to be cancelled, in
 * the lane that job belongs to, and closes the row.
 */
export async function endDrain(
  deps: DrainDeps,
  row: DrainRow,
  ending: DrainEnding,
  reason: string,
  live: readonly LiveJob[],
): Promise<Ended> {
  const notes: string[] = [];
  const operationId = drainOperation(row.preset);
  let cancelled = 0;
  for (const job of live) {
    /*
      A CODE SESSION IS CANCELLED THROUGH CODE (#279). Its job belongs to `atyrode.omp` and
      `ctx.jobs.cancel` is bound to the calling plugin's id, so a drain that reached for the
      hub's verb would refuse every job of the lane it exists to stop. `container_id` on the
      run row is what says which lane a job is in, and `cancelSession` is idempotent on a job
      that has already settled — a stop that raced a settlement answers the job.
    */
    const container = await containerOf(deps.store, job.runId);
    try {
      if (container === "") {
        await deps.jobs.cancel({
          kind: "job",
          machineId: row.machineId,
          operationId,
          jobId: job.jobId,
        });
      } else {
        const answered = await deps.engine.cancelSession({
          containerId: container,
          jobId: job.jobId,
        });
        if (!answered.ok) {
          notes.push(`${job.jobId} was not cancelled: ${answered.refused}`);
          continue;
        }
      }
      cancelled += 1;
    } catch (error) {
      notes.push(`${job.jobId} was not cancelled: ${message(error)}`);
    }
  }
  const closed = await closeDrain(deps.store, row.id, ending, reason, live);
  if (closed === "already") {
    notes.push(`drain ${row.id} had already ended when this tick closed it`);
  }
  if (closed === "closing") {
    notes.push(
      `${String(live.length)} job(s) of this drain are still running: it ends as ${ending} when ` +
        `their receipts have landed`,
    );
  }
  deps.store.touch();
  return { state: closed === "closing" ? "closing" : ending, cancelled, notes };
}

/**
 * One drain, moved on by one tick.
 *
 * The order is the whole of it: fold what closed BEFORE deciding, so a target met by the job
 * that just settled ends the drain instead of launching one more; then decide; then launch, so a
 * drain that is already over never posts again.
 *
 * A CLOSING DRAIN IS FOLDED AND NOTHING ELSE. Its ending is already recorded and its targets are
 * already answered; what is left is the receipts of the jobs it was holding when it closed, and
 * the tick that folds the last of them is the one that records the end.
 */
async function tickDrain(deps: DrainDeps, row: DrainRow): Promise<DrainReport> {
  const at = deps.now();
  const seen = await reconcileLive(deps.store, row.live);
  const folded = await foldDrain(deps.store, row, seen, at);
  const notes: string[] = [...folded.notes];
  const spent = folded.spent;

  if (row.state === "closing") {
    const ending = row.ending === "" ? "stopped" : row.ending;
    if (seen.holding.length > 0) {
      return {
        drainId: row.id,
        launched: 0,
        settled: seen.settled.length,
        live: seen.holding.length,
        state: "closing",
        reason: row.reason,
        notes,
      };
    }
    const finished = await finishDrain(deps.store, row.id);
    if (finished) deps.store.touch();
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: 0,
      state: ending,
      reason: row.reason,
      notes,
    };
  }

  const met = targetMet(row.target, spent);
  if (met !== "") {
    const ended = await endDrain(deps, row, "target", met, seen.holding);
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: seen.holding.length,
      state: ended.state,
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
      live: seen.holding.length,
      state: ended.state,
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
      live: seen.holding.length,
      state: ended.state,
      reason: why,
      notes: [...notes, ...ended.notes],
    };
  }

  const operationId = drainOperation(row.preset);
  // The session is the drain's own, every time: the model and the account the operator named
  // when they started it, not whatever a later default would be (#267, #279).
  const plan = deps.plan(inForce.policy, operationId, row.profile);
  const input = drainInput(row);
  const holding = [...seen.holding];
  let launched = 0;
  let refused = "";
  for (let slot = holding.length; slot < row.concurrent; slot += 1) {
    const identity = drainIdentity(row, row.jobsLaunched + launched);
    const started = SPENDING.includes(row.preset)
      ? await deps.launch.startExplore(identity, deps.jobs, deps.engine, input, plan)
      : await deps.launch.startBeat(identity, deps.jobs, input, plan);
    const job: LiveJob = { runId: identity.runId, jobId: identity.jobId, launchedAt: at };
    if ("refused" in started) {
      /*
        A JOB THIS DRAIN ALREADY POSTED IS ADOPTED RATHER THAN RE-POSTED. `job_digest_conflict`
        is the hub saying it holds this id under a different request (`job-store.ts`: the digest
        covers the input, and an explore's selection window moves between ticks), which for a
        DERIVED id can only mean a tick of this drain posted it and lost the write that recorded
        it — the 2-second hook lease closing between `execute` and the row. Without this the
        ordinal never advances, every later tick re-posts the same conflicting id, the slot is
        dead for the rest of the drain and the orphan's spend is never folded. The run row is
        already there (it is written by the same call that posted the job), so taking the job
        back onto `live` is enough: it settles like any other and its receipt lands in `spent`.
        If the row is NOT there, the next tick's `missing` releases the slot.
      */
      if (started.refused.includes("job_digest_conflict")) {
        notes.push(`${identity.jobId} was already posted by an earlier tick, and is taken back`);
        await recordLaunch(deps.store, row.id, job, holding);
        holding.push(job);
        launched += 1;
        continue;
      }
      // ONE REFUSAL ENDS THE ROUND, not the drain. The next slot would ask the same thing of the
      // same machine with the same window and hear the same sentence, and a tick that asked
      // sixteen times would report one fact sixteen times. The drain stays running: the reason
      // may be a machine reconnecting or a concurrency ceiling that frees up on the next settle.
      refused = started.refused;
      notes.push(`no further job was launched: ${started.refused}`);
      break;
    }
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
    drains = await activeDrains(deps.store);
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
