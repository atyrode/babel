import {
  DRAIN_SPENDING_PRESETS,
  OPERATIONS,
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
  foldJournal,
  noteDrain,
  reconcileLive,
  readDrain,
  recordLaunch,
  saveFold,
  targetMet,
  writeDrainReport,
  type DrainNote,
  type DrainRow,
  type DrainsStore,
  type LiveJob,
  type Reconciled,
} from "../store/drains.ts";
import {
  backfillVectors,
  ensureTerms,
  BACKFILL_BATCH,
  type CorpusStore,
  type Embedder,
} from "../store/corpus.ts";
import type { BabelStore } from "../store/store.ts";
import { materialJobId, type LaunchIdentity, type Started } from "../doors/launch.ts";
import type { RunPlan } from "./conductor.ts";
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
    jobs: BabelJobs,
    engine: CodeEngine,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  startBeat(
    identity: LaunchIdentity,
    jobs: BabelJobs,
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
  /**
   * THE EMBEDDING AUTHORITY THIS WAKE HOLDS, or `null` because it holds none (#337).
   *
   * It is `null` on every background wake and that is the host's construction rather than a
   * choice: `GuestServices` is served to a DISPATCH, and the settled-job hook's context
   * (`GuestJobSettledCtx`) carries jobs and actions and no services at all. So the corpus
   * backfill rides the ticks a dispatch woke — the operator watching his drain, which is
   * precisely when he is paying attention to what it is spending — and a settlement's tick does
   * the launching and none of the embedding. Absent is one branch: the keyword half of the index
   * is still brought current, because that half needs nobody's account.
   */
  readonly embed?: Embedder | null;
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
    ...(row.knobs.inferenceLimits === undefined
      ? {}
      : { inferenceLimits: row.knobs.inferenceLimits }),
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
  /** The snapshot and reconciliation that won the fold, refreshed after a competing write. */
  readonly row: DrainRow;
  readonly seen: Reconciled;
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
  if (row.state !== "running" && row.state !== "closing") {
    return { row, seen, spent: row.spent, notes: [] };
  }
  const notes: string[] = [];
  const journaled: DrainNote[] = [];
  for (const gone of seen.missing) {
    const note = `${gone.jobId} was launched with no run row to show for it, so its slot is released`;
    notes.push(note);
    journaled.push({ at, kind: "orphan", detail: note });
  }
  /*
    A STALL IS JOURNALED WHERE IT IS SEEN, because nowhere else keeps it (#270). `run_progress`
    is what says a job has been at the model with nothing metered for ninety seconds, and that
    row is deleted the instant the job settles — so a drain that recorded only totals could
    never afterwards say it had spent an hour stalled. The note is the tick's count rather than
    one per job: the same two jobs stalling across six ticks is one fact observed six times, and
    naming them each time would be the bound's whole budget spent on one stall.
  */
  if (seen.stalled > 0) {
    journaled.push({
      at,
      kind: "stall",
      detail:
        `${String(seen.stalled)} of ${String(seen.holding.length)} job(s) are at the model with ` +
        `nothing metered for 90s`,
    });
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
  const saved = await saveFold(store, row, {
    live: seen.holding,
    spent: settledSpend,
    closures,
    refusals,
    journal: foldJournal(
      row.journal,
      at,
      spent,
      { held: seen.holding.length, atModel: seen.atModel },
      journaled,
    ),
    settledNow: seen.settled.length,
  });
  if (!saved) {
    const current = await readDrain(store, row.id);
    if (current === null) throw new Error(`drain ${row.id} disappeared during its fold`);
    return await foldDrain(store, current, await reconcileLive(store, current.live), at);
  }
  return { row, seen, spent, notes };
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
 * WHICH LANE A RUN IS IN, AND THE ID THAT LANE RETAINED — the three columns that decide which
 * verb stops it.
 *
 * A DRAIN'S `LiveJob.jobId` IS NOT A JOB for the lane that spends. It is the run's derived
 * identity, and what gets posted under it is the preparation (`${jobId}_material`) and, one
 * wake later, CODE's session under an id only Code minted. So a stop that reached for
 * `job.jobId` would ask Code for a session id that never existed and hear
 * `code_session_unknown`, while the preparation it could have cancelled ran on and
 * `postPrepared` posted the session after the drain had ended. The row is the only place the
 * real ids are.
 *
 * Three shapes, and each has its own verb:
 * - no container: a job of Babel's own (the beat), cancelled with `ctx.jobs.cancel`;
 * - a container and a `job_id`: a CODE SESSION, cancelled with `code.cancelSession`, because
 *   its job belongs to `atyrode.omp` and the hub's verb is bound to the caller's plugin id;
 * - a container and no `job_id`: INTENT — the preparation is in flight and no session exists.
 *   That one is Babel's own job at `atyrode.babel.prepare`.
 */
interface RunLane {
  readonly container: string;
  readonly jobId: string;
  readonly prepareJobId: string;
  readonly posting: boolean;
}

async function laneOf(store: DrainDeps["store"], runId: string): Promise<RunLane> {
  const rows = await store.db.query<{
    container_id: string | null;
    job_id: string | null;
    prepare_job_id: string | null;
    posting: number | bigint | null;
  }>(
    `SELECT container_id, job_id, prepare_job_id, json_extract(payload, '$.posting') AS posting
       FROM runs WHERE id = ?`,
    [runId],
  );
  const row = rows[0];
  return {
    container: row?.container_id ?? "",
    jobId: row?.job_id ?? "",
    prepareJobId: row?.prepare_job_id ?? "",
    posting: Number(row?.posting) === 1,
  };
}

/**
 * CLOSES A RUN THAT NEVER REACHED A SESSION, so no later wake posts one for it.
 *
 * `postPrepared` walks every row whose material has sealed and whose `job_id` is still NULL;
 * an intent row left open outlives the drain that made it, and the session it would post
 * would spend an account after the operator stopped spending. `AND job_id IS NULL` is the
 * fence against the opposite race — a wake that posted the session between the read and this
 * write owns the row, and that run is cancelled through Code on the next tick.
 */
async function closeIntent(deps: DrainDeps, runId: string, reason: string): Promise<void> {
  const at = new Date(deps.now()).toISOString();
  const closed = await deps.store.db.run(
    `UPDATE runs SET closure = 'stopped', finished_at = ?, payload = ?
      WHERE id = ? AND closure IS NULL AND job_id IS NULL
        AND COALESCE(json_extract(payload, '$.posting'), 0) = 0`,
    [at, JSON.stringify({ closure: "stopped", reason, stoppedAt: at }), runId],
  );
  if (closed.changes === 0)
    throw new Error(`${runId} changed during cancellation; its session may be live`);
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
  const journaled: DrainNote[] = [];
  const at = deps.now();
  const operationId = drainOperation(row.preset);
  let cancelled = 0;
  for (const job of live) {
    const lane = await laneOf(deps.store, job.runId);
    try {
      if (lane.container === "") {
        // BABEL'S OWN JOB (the beat): posted under this plugin's id at the drain's operation,
        // so the hub's own verb is the one that stops it.
        await deps.jobs.cancel({
          kind: "job",
          machineId: row.machineId,
          operationId,
          jobId: job.jobId,
        });
      } else if (lane.jobId === "") {
        if (lane.posting)
          throw new Error(`${job.runId} has an unresolved Code posting; its session may be live`);
        /*
          INTENT: the preparation is in flight and no session exists yet. Cancelling the
          PREPARATION is only half of it — `postPrepared` walks every open row whose material
          has sealed, so a preparation that settles anyway (a cancel is a request, and one
          that races the seal loses) would have its session posted by the next wake, after
          this drain ended. Closing the row is what makes that impossible, and it is the row
          that the wake's own `WHERE r.closure IS NULL` reads.
        */
        if (lane.prepareJobId !== "") {
          await deps.jobs.cancel({
            kind: "job",
            machineId: row.machineId,
            operationId: OPERATIONS.prepare,
            jobId: lane.prepareJobId,
          });
        }
        await closeIntent(deps, job.runId, reason);
      } else {
        /*
          A CODE SESSION IS CANCELLED THROUGH CODE (#279). Its job belongs to `atyrode.omp`
          and `ctx.jobs.cancel` is bound to the calling plugin's id, so a drain that reached
          for the hub's verb would refuse every job of the lane it exists to stop. The id is
          the ROW's, because Code minted it: `job.jobId` is the run's derived identity and
          naming it here is how a stop asks Code about a session that never existed.
          `cancelSession` is idempotent on a job that has already settled.
        */
        const answered = await deps.engine.cancelSession({
          containerId: lane.container,
          jobId: lane.jobId,
        });
        if (!answered.ok) {
          notes.push(`${lane.jobId} was not cancelled: ${answered.refused}`);
          journaled.push({ at, kind: "cancel", detail: `${lane.jobId}: ${answered.refused}` });
          continue;
        }
      }
      cancelled += 1;
    } catch (error) {
      notes.push(`${job.jobId} was not cancelled: ${message(error)}`);
      journaled.push({ at, kind: "cancel", detail: `${job.jobId}: ${message(error)}` });
    }
  }
  const closed = await closeDrain(deps.store, row.id, ending, reason);
  if (closed === "already") {
    notes.push(`drain ${row.id} had already ended when this tick closed it`);
  }
  if (closed === "closing") {
    notes.push(
      `${String(live.length)} job(s) of this drain are still running: it ends as ${ending} when ` +
        `their receipts have landed`,
    );
  }
  await noteDrain(deps.store, row.id, journaled);
  // The report is written from the row AFTER the close, so the ending, the reason and the
  // finishing instant it carries are the ones the store holds rather than the ones this call
  // intended. A drain that went to `closing` is not finished and leaves none yet.
  const left = closed === "ended" ? await leaveReport(deps, row.id) : [];
  deps.store.touch();
  return {
    state: closed === "closing" ? "closing" : ending,
    cancelled,
    notes: [...notes, ...left],
  };
}

/**
 * THE RECORD A FINISHED DRAIN LEAVES (#270), written once, from the row as it now stands.
 *
 * A REPORT THAT COULD NOT BE WRITTEN IS A NOTE AND NEVER A FAILURE OF THE ENDING. The drain has
 * stopped — the jobs are cancelled, the row is closed, the window is safe — and throwing here
 * would make a reporting bug look like a controller that could not stop spending. It is
 * idempotent on a derived identifier, so a later close of the same drain writes no second.
 */
async function leaveReport(deps: DrainDeps, id: string): Promise<readonly string[]> {
  try {
    const row = await readDrain(deps.store, id);
    if (row === null || row.state === "running" || row.state === "closing") return [];
    await writeDrainReport(deps.store, row);
    return [];
  } catch (error) {
    return [`this drain ended and its report was not written: ${message(error)}`];
  }
}

/**
 * THE CORPUS INDEX AS A DRAIN DUTY (#337): what one tick of one drain spends on it.
 *
 * WHY THE DRAIN PAYS FOR IT. Embedding 6,038 records is the exact shape of work this mechanism
 * exists for — bounded, resumable, worth doing while capacity is free and not worth blocking on
 * when it is not — and the drain is the one thing in Babel that spends surplus deliberately,
 * meters it, and leaves a report of what it cost (#270). It also answers the only real objection
 * to embeddings, which was never the money but the STANDING cost: a re-embed when the model
 * changes is a drain, and a drain is a thing Babel already knows how to run and account for.
 *
 * BOUNDED. {@link BACKFILL_BATCH} records a tick, one service call each. The bound is here
 * rather than in the index because it is a statement about a TICK: a tick holds the wake that
 * called it, and a pass that embedded a whole corpus would hold a dispatch open for minutes.
 *
 * RESUMABLE, AND WITH NO CURSOR. The pending set is a query over `records` and `record_vectors`
 * (`store/corpus.ts`, `pendingVectors`), so a tick that died, a hub that restarted and a drain
 * that was stopped all leave the same state: the rows that were written, and the rest still
 * pending. Nothing has to be reconciled on the way back up.
 *
 * THE KEYWORD HALF RUNS WHATEVER THE ACCOUNT SAYS. `ensureTerms` needs no model, no credential
 * and no egress, so it is ahead of the embedding and outside its condition: a deployment that
 * installed nothing still gets its index brought current by every tick, which is the half that
 * makes a search work at all.
 *
 * IT CANNOT FAIL A DRAIN. Everything here is inside one `catch`: a drain is an operator's
 * decision to spend a window that is about to reset, and an index that could not be written is
 * not a reason to stop spending it. The sentence is journaled as an `error` note, which is where
 * every other thing a tick could not do already goes.
 */
async function indexDuty(
  deps: DrainDeps,
  at: number,
): Promise<{ readonly journal: readonly DrainNote[]; readonly notes: readonly string[] }> {
  const journal: DrainNote[] = [];
  const notes: string[] = [];
  const corpus: CorpusStore = { db: deps.store.db };
  try {
    const terms = await ensureTerms(corpus);
    if (terms > 0) {
      const detail = `the keyword index was rebuilt over ${String(terms)} records`;
      journal.push({ at, kind: "index", detail });
      notes.push(detail);
    }
    const embed = deps.embed ?? null;
    if (embed === null) return { journal, notes };
    const report = await backfillVectors(corpus, embed, new Date(at).toISOString(), BACKFILL_BATCH);
    if (report.model === "") {
      // No policy installed, or the service did not answer. Neither is an error and neither is
      // journaled: a note per tick for a deployment that has installed nothing would fill the
      // drain's own report with the absence of a feature.
      return { journal, notes };
    }
    if (report.embedded === 0 && report.empty === 0 && report.unanswered === 0) {
      return { journal, notes };
    }
    const detail =
      `${String(report.embedded)} records embedded by ${report.model}` +
      `${report.empty === 0 ? "" : `, ${String(report.empty)} with no text`}` +
      `${report.unanswered === 0 ? "" : `, ${String(report.unanswered)} unanswered`}` +
      `, ${String(report.remaining)} left`;
    journal.push({ at, kind: "index", detail });
    notes.push(detail);
  } catch (error) {
    const detail = `the corpus index could not be advanced: ${message(error)}`;
    journal.push({ at, kind: "error", detail });
    notes.push(detail);
  }
  return { journal, notes };
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
  const folded = await foldDrain(deps.store, row, await reconcileLive(deps.store, row.live), at);
  row = folded.row;
  const seen = folded.seen;
  const notes: string[] = [...folded.notes];
  const spent = folded.spent;
  if (row.state !== "running" && row.state !== "closing") {
    return {
      drainId: row.id,
      launched: 0,
      settled: 0,
      live: row.live.length,
      state: row.state,
      reason: row.reason,
      notes,
    };
  }

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
    // The last receipt of a closing drain has landed, so this tick is where its account becomes
    // final and where its report is written (#270) — never at the close, which happened while
    // up to (N−1) of its jobs were still at a model.
    const left = finished ? await leaveReport(deps, row.id) : [];
    if (finished) deps.store.touch();
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: 0,
      state: ending,
      reason: row.reason,
      notes: [...notes, ...left],
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
  // Exhausting admissions stops refills, not the jobs already admitted. Their receipts still
  // belong to this drain, including refusals and jobs that spent nothing.
  if (row.knobs.maxJobs !== undefined && row.jobsLaunched >= row.knobs.maxJobs) {
    const why = `the admission bound of ${String(row.knobs.maxJobs)} jobs is exhausted`;
    if (seen.holding.length > 0) {
      return {
        drainId: row.id,
        launched: 0,
        settled: seen.settled.length,
        live: seen.holding.length,
        state: "running",
        reason: why,
        notes,
      };
    }
    const ended = await endDrain(deps, row, "target", why, []);
    return {
      drainId: row.id,
      launched: 0,
      settled: seen.settled.length,
      live: 0,
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
  // What this round could not do. It is journaled AFTER the round rather than folded with it,
  // because a fold advances the drain's own load integrals and this write must not advance them
  // a second time in the same instant.
  const journaled: DrainNote[] = [];
  let launched = 0;
  let refused = "";
  for (let slot = holding.length; slot < row.concurrent; slot += 1) {
    if (row.knobs.maxJobs !== undefined && row.jobsLaunched + launched >= row.knobs.maxJobs) break;
    const identity = drainIdentity(row, row.jobsLaunched + launched);
    const started = SPENDING.includes(row.preset)
      ? await deps.launch.startExplore(identity, deps.jobs, deps.engine, input, plan)
      : await deps.launch.startBeat(identity, deps.jobs, input, plan);
    /*
      WHAT IS RECORDED AS LIVE IS THE JOB THAT WAS POSTED, which for the lane that spends is
      the PREPARATION and not the run's derived identity: `startExplore` posts
      `materialJobId(identity.jobId)` and the session comes one wake later under an id Code
      mints (#592). `drain.start`'s own first fan records `started.jobId`, which is the same
      string, and the two paths writing different things into one column is how a stop ends
      up naming an id nothing holds. The verb that stops a run still reads the ROW, not this.
    */
    const postedJobId = SPENDING.includes(row.preset)
      ? materialJobId(identity.jobId)
      : identity.jobId;
    const job: LiveJob = { runId: identity.runId, jobId: postedJobId, launchedAt: at };
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

        IT IS THE HUB'S WORD THAT DECIDES, not the sentence: `refused` is written for the
        operator and this branch is a behaviour, so it turns on the code `post` carried out of
        the error the hub threw (#288). A code is present only when the hub refused, so a
        sentence of Babel's own can never reach here.
      */
      if (started.code === "job_digest_conflict") {
        const adopted = `${identity.jobId} was already posted by an earlier tick, and is taken back`;
        notes.push(adopted);
        journaled.push({ at, kind: "adopted", detail: adopted });
        const recorded = await recordLaunch(deps.store, row.id, job, row.jobsLaunched + launched);
        if (!recorded) {
          const current = await readDrain(deps.store, row.id);
          holding.splice(0, holding.length, ...(current?.live ?? []));
          break;
        }
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
      /*
        AN ADMISSION REFUSAL IS DURABLE NOWHERE ELSE (#270). `startExplore` writes its run row
        only after the hub has taken the job, so a refused launch leaves no row, no receipt and
        no closure — it is work that never became a job, and on 2026-09-13 the only trace of
        twenty of them was a shell's scrollback. The code the hub carried out of its own error
        leads the sentence, which is what makes the report's `launchRefusals` countable.
      */
      journaled.push({
        at,
        kind: "admission",
        detail:
          started.code === undefined || started.code === ""
            ? started.refused
            : `${started.code}: ${started.refused}`,
      });
      break;
    }
    // The persisted admission cursor decides whether this wake, or another one, recorded it.
    const recorded = await recordLaunch(deps.store, row.id, job, row.jobsLaunched + launched);
    if (!recorded) {
      const current = await readDrain(deps.store, row.id);
      holding.splice(0, holding.length, ...(current?.live ?? []));
      break;
    }
    holding.push(job);
    launched += 1;
  }
  // THE CORPUS INDEX'S OWN SLICE OF THIS TICK (#337), after the launching and before the
  // journal, so the note it leaves rides the one write that already happens here.
  const indexed = await indexDuty(deps, at);
  journaled.push(...indexed.journal);
  notes.push(...indexed.notes);
  await noteDrain(deps.store, row.id, journaled);

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
      const detail = `this drain could not be moved on: ${message(error)}`;
      /*
        A TICK THAT THREW IS THE ONE EVENT NOTHING ELSE RECORDS. The report goes to the caller
        and the caller is a wake; the row is untouched, so a drain that failed every tick for
        two hours would end looking like a drain that simply produced nothing. The note is
        written on its own and a failure to write it is swallowed, because this handler exists
        so that one unreadable drain does not stop the others (#270).
      */
      await noteDrain(deps.store, row.id, [{ at: deps.now(), kind: "error", detail }]).catch(
        () => undefined,
      );
      reports.push({
        drainId: row.id,
        launched: 0,
        settled: 0,
        live: row.live.length,
        state: row.state,
        reason: "",
        notes: [detail],
      });
    }
  }
  return reports;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
