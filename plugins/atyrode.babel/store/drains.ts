import type { PluginDatabase, SqlParam } from "@manifold/plugin";
import {
  DRAIN_ENDINGS,
  DRAIN_PRESETS,
  DRAIN_STATES,
  DrainSpendSchema,
  SessionChoiceSchema,
  RUN_STAGES,
  type DrainEnding,
  type DrainPreset,
  type DrainSpend,
  type DrainState,
  type DrainStatus,
  type DrainTarget,
  type SessionChoice,
} from "../contract.ts";
import { refusalCode } from "../machine/results.ts";

/*
  THE DRAIN'S OWN ROWS (#258): one table, read and written here and nowhere else.

  A drain is an operator decision that outlives every process carrying it out — spend this
  account's remaining window, on this preset, this many jobs at a time, until this target or this
  deadline — and the server half holds nothing between wakes. So the row IS the controller's
  memory, and this module is the only place that reads or writes it: `server/drain.ts` decides
  what to do next and `doors/drain.ts` answers the operator, both through the functions below.

  TWO SPENDS, AND THE DIFFERENCE MATTERS. `spent` on the row is what this drain's SETTLED jobs
  metered: folded once as each job closes, never recomputed, because the `run_progress` row a
  job's spend was folded through is deleted the instant it settles (`server/conductor.ts`: "the
  receipt is the record now"). What its LIVE jobs have metered so far is added when the status is
  read. The sum is what a target is judged against, and `settled` is reported beside it so a
  reader can tell a receipt from a fold in progress.

  EVERY INTEGER COLUMN ARRIVES AS A BIGINT from the engine's database (#536), so each one is
  `Number(...)`-ed where it is read and nothing here ever compares a raw column against a number.
  That bug voided a guarantee once already (`0n === 0` in the claim reaper).
*/

/** The handle this module reads and writes through; `BabelStore` satisfies it. */
export interface DrainsStore {
  readonly db: PluginDatabase;
  now(): number;
}

/** One job this drain is holding, as the row's `live` array carries it. */
export interface LiveJob {
  readonly runId: string;
  readonly jobId: string;
  /** Epoch milliseconds, so the panel's clock and this one are the same clock. */
  readonly launchedAt: number;
}

/**
 * One observation of the drain's whole spend, taken once per tick.
 *
 * A RATE NEEDS TWO OBSERVATIONS and nothing else in the store keeps a series: `run_progress` is
 * rewritten in place and a settled run keeps a total. Without these, "tokens per minute" could
 * only be a total over an elapsed time, which never reads flat — and reading flat for three
 * minutes while jobs say `at the model` is the one no-go `docs/runbook.md` §11.4 names.
 */
export interface DrainSample {
  readonly at: number;
  readonly outputTokens: number;
  readonly costMicros: number;
}

export interface DrainRow {
  readonly id: string;
  readonly machineId: string;
  readonly preset: DrainPreset;
  readonly session: SessionChoice;
  readonly knobs: DrainKnobs;
  readonly concurrent: number;
  readonly target: DrainTarget;
  readonly startedAt: string;
  readonly startedBy: string;
  readonly finishedAt: string;
  readonly state: DrainState;
  /** The ending a `closing` drain takes when its last receipt lands; empty otherwise. */
  readonly ending: DrainEnding | "";
  readonly reason: string;
  readonly spent: DrainSpend;
  readonly live: readonly LiveJob[];
  readonly samples: readonly DrainSample[];
  readonly closures: Readonly<Record<string, number>>;
  readonly refusals: Readonly<Record<string, number>>;
  readonly jobsLaunched: number;
  readonly jobsSettled: number;
}

/** One settled job of this drain: what it metered, how it closed, and what it had refused. */
export interface SettledRun {
  readonly runId: string;
  readonly closure: string;
  readonly spend: DrainSpend;
  /** The code a refused submission carries, or null: nothing was refused. */
  readonly refusal: string | null;
}

/**
 * WHERE THIS DRAIN'S JOBS ARE, from one read of the runs table and the conductor's live fold.
 *
 * One read rather than two because the three answers are one question — a job the drain is
 * holding is either still running, or it has closed and its spend belongs in the total, or its
 * row is not there at all — and a controller that asked twice could see a job in both halves.
 */
export interface Reconciled {
  /** Still running: what the next tick keeps holding. */
  readonly holding: readonly LiveJob[];
  /** Closed since this drain last folded: their spend and their verdicts. */
  readonly settled: readonly SettledRun[];
  /**
   * Held, with no run row at all. A launch writes the row only after the hub accepted the job,
   * so this is a row-write that did not land; the job is not something a settlement will ever
   * reach, and holding a slot for it for ever would starve the fan the way a ghost claim
   * starved the draws of 2026-09-13.
   */
  readonly missing: readonly LiveJob[];
  readonly atModel: number;
  readonly stalled: number;
  /** What the still-running jobs have metered so far, from `run_progress` (#261). */
  readonly inFlight: DrainSpend;
}

/**
 * WHAT THE PRESET WAS ASKED FOR, kept so a relaunch asks for the same thing. It is the launch
 * input's own knobs, minus the machine and the preset, which are columns of their own.
 */
export interface DrainKnobs {
  readonly recipes: readonly string[];
  readonly sinceDays?: number | undefined;
  readonly entityId?: string | undefined;
  readonly minutes?: number | undefined;
  readonly agentSessions?: boolean | undefined;
}

/**
 * HOW FAR BACK A RATE IS MEASURED. Three minutes because that is the window the runbook judges
 * against — "tokens per minute flat for three minutes while jobs read `at the model` is a no-go"
 * — so the figure shown and the figure the rule is about are one number.
 */
export const RATE_WINDOW_MS = 3 * 60 * 1000;

/**
 * How many samples a row keeps. Enough to cover the rate window several times over at any tick
 * cadence a wake produces, and bounded so a two-hour drain's row stays a row: the samples are a
 * working record of the last few minutes, not a history of the drain.
 */
export const MAX_SAMPLES = 24;

export const NO_SPEND: DrainSpend = { calls: 0, inputTokens: 0, outputTokens: 0, costMicros: 0 };

const PRESETS: readonly string[] = DRAIN_PRESETS;
const STATES: readonly string[] = DRAIN_STATES;
const ENDINGS: readonly string[] = DRAIN_ENDINGS;

/** Every column, in one place, so a read and a write cannot come to disagree about the shape. */
const COLUMNS =
  `id, machine_id, preset, session, knobs, concurrent, target, started_at, started_by, ` +
  `finished_at, state, ending, reason, spent, live, samples, closures, refusals, ` +
  `jobs_launched, jobs_settled`;

/** A stored row as SQLite hands it back; a type alias, so the query generic accepts it. */
type DrainDbRow = {
  id: string;
  machine_id: string;
  preset: string;
  session: string;
  knobs: string;
  concurrent: number | bigint;
  target: string;
  started_at: string;
  started_by: string;
  finished_at: string | null;
  state: string;
  ending: string;
  reason: string;
  spent: string;
  live: string;
  samples: string;
  closures: string;
  refusals: string;
  jobs_launched: number | bigint;
  jobs_settled: number | bigint;
};

function count(value: SqlParam | number | bigint | undefined | null): number {
  if (typeof value === "number") return Number.isFinite(value) ? value : 0;
  if (typeof value === "bigint") return Number(value);
  return 0;
}

function parsed(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

/** A stored `{word: count}` tally, with anything that is not a number dropped. */
function tally(text: string): Record<string, number> {
  const held = parsed(text);
  const out: Record<string, number> = {};
  if (held === null || typeof held !== "object") return out;
  for (const [key, value] of Object.entries(held as Record<string, unknown>)) {
    if (typeof value === "number" && Number.isFinite(value)) out[key] = value;
  }
  return out;
}

function spendOf(text: string): DrainSpend {
  const held = DrainSpendSchema.safeParse(parsed(text));
  return held.success ? held.data : NO_SPEND;
}

function liveOf(text: string): LiveJob[] {
  const held = parsed(text);
  if (!Array.isArray(held)) return [];
  const jobs: LiveJob[] = [];
  for (const entry of held) {
    if (entry === null || typeof entry !== "object") continue;
    const row = entry as Record<string, unknown>;
    const runId = typeof row["runId"] === "string" ? row["runId"] : "";
    const jobId = typeof row["jobId"] === "string" ? row["jobId"] : "";
    if (runId === "" || jobId === "") continue;
    jobs.push({ runId, jobId, launchedAt: count(row["launchedAt"] as number | undefined) });
  }
  return jobs;
}

function samplesOf(text: string): DrainSample[] {
  const held = parsed(text);
  if (!Array.isArray(held)) return [];
  const samples: DrainSample[] = [];
  for (const entry of held) {
    if (entry === null || typeof entry !== "object") continue;
    const row = entry as Record<string, unknown>;
    const at = count(row["at"] as number | undefined);
    if (at <= 0) continue;
    samples.push({
      at,
      outputTokens: count(row["outputTokens"] as number | undefined),
      costMicros: count(row["costMicros"] as number | undefined),
    });
  }
  return samples.sort((left, right) => left.at - right.at);
}

/**
 * The preset's stored knobs. A knob whose stored value is not the type the launch input takes is
 * left absent rather than coerced: the launch path then asks for its own default, which is the
 * documented request, instead of reading a `"3"` as three days somewhere downstream.
 */
function knobsOf(text: string): DrainKnobs {
  const held = parsed(text);
  if (held === null || typeof held !== "object") return { recipes: [] };
  const row = held as Record<string, unknown>;
  const recipes = Array.isArray(row["recipes"])
    ? row["recipes"].filter((entry): entry is string => typeof entry === "string")
    : [];
  return {
    recipes,
    ...(typeof row["sinceDays"] === "number" ? { sinceDays: row["sinceDays"] } : {}),
    ...(typeof row["entityId"] === "string" ? { entityId: row["entityId"] } : {}),
    ...(typeof row["minutes"] === "number" ? { minutes: row["minutes"] } : {}),
    ...(typeof row["agentSessions"] === "boolean" ? { agentSessions: row["agentSessions"] } : {}),
  };
}

/**
 * One stored row as the controller reads it. A `preset` or a `state` outside the vocabulary is
 * the store holding a value its own CHECK forbids, so it is refused rather than guessed at: a
 * drain whose preset nobody can name is one nothing may relaunch.
 */
function rowOf(row: DrainDbRow): DrainRow {
  if (!PRESETS.includes(row.preset)) {
    throw new Error(`drain ${row.id} names the preset ${row.preset}, which is not one a drain runs`);
  }
  if (!STATES.includes(row.state)) {
    throw new Error(`drain ${row.id} is in the state ${row.state}, which is not one a drain has`);
  }
  if (row.ending !== "" && !ENDINGS.includes(row.ending)) {
    throw new Error(`drain ${row.id} is closing towards ${row.ending}, which is not an ending`);
  }
  const session = SessionChoiceSchema.safeParse(parsed(row.session));
  if (!session.success) {
    throw new Error(`drain ${row.id} names no account and model a run could be launched under`);
  }
  const target = parsed(row.target);
  return {
    id: row.id,
    machineId: row.machine_id,
    preset: row.preset as DrainPreset,
    session: session.data,
    knobs: knobsOf(row.knobs),
    concurrent: count(row.concurrent),
    target: target === null || typeof target !== "object" ? {} : (target as DrainTarget),
    startedAt: row.started_at,
    startedBy: row.started_by,
    finishedAt: row.finished_at ?? "",
    state: row.state as DrainState,
    ending: row.ending === "" ? "" : (row.ending as DrainEnding),
    reason: row.reason,
    spent: spendOf(row.spent),
    live: liveOf(row.live),
    samples: samplesOf(row.samples),
    closures: tally(row.closures),
    refusals: tally(row.refusals),
    jobsLaunched: count(row.jobs_launched),
    jobsSettled: count(row.jobs_settled),
  };
}

// ---------------------------------------------------------------------------- reading

export async function readDrain(store: DrainsStore, id: string): Promise<DrainRow | null> {
  const rows = await store.db.query<DrainDbRow>(`SELECT ${COLUMNS} FROM drains WHERE id = ?`, [id]);
  const row = rows[0];
  return row === undefined ? null : rowOf(row);
}

/**
 * Every drain a tick still has work for, oldest first: the order the controller works through
 * them. A `closing` drain is one of them — it launches nothing more, but the receipts of the
 * jobs it still holds are owed to its own total, and the tick is what folds them.
 */
export async function activeDrains(store: DrainsStore): Promise<readonly DrainRow[]> {
  const rows = await store.db.query<DrainDbRow>(
    `SELECT ${COLUMNS} FROM drains WHERE state IN ('running', 'closing') ORDER BY started_at, id`,
  );
  return rows.map(rowOf);
}

/** The newest drains, running or ended: what a panel opening cold has to show. */
export async function recentDrains(store: DrainsStore, limit: number): Promise<readonly DrainRow[]> {
  const rows = await store.db.query<DrainDbRow>(
    `SELECT ${COLUMNS} FROM drains ORDER BY started_at DESC, id DESC LIMIT ?`,
    [limit],
  );
  return rows.map(rowOf);
}

/**
 * Whether this machine already has a drain running: one at a time, per machine (see the door).
 *
 * A `closing` drain is not one: it launches nothing more, and holding the machine until the last
 * receipt of a job that may run for another half hour would make a straggler the reason the next
 * drain cannot start.
 */
export async function drainOnMachine(
  store: DrainsStore,
  machineId: string,
): Promise<DrainRow | null> {
  const rows = await store.db.query<DrainDbRow>(
    `SELECT ${COLUMNS} FROM drains WHERE state = 'running' AND machine_id = ? ORDER BY started_at LIMIT 1`,
    [machineId],
  );
  const row = rows[0];
  return row === undefined ? null : rowOf(row);
}

// ---------------------------------------------------------------------------- writing

export interface NewDrain {
  readonly id: string;
  readonly machineId: string;
  readonly preset: DrainPreset;
  readonly session: SessionChoice;
  readonly knobs: DrainKnobs;
  readonly concurrent: number;
  readonly target: DrainTarget;
  readonly startedBy: string;
}

/** The row a `drain.start` leaves behind, before its first job is posted. */
export async function insertDrain(store: DrainsStore, drain: NewDrain): Promise<void> {
  await store.db.run(
    `INSERT INTO drains(id, machine_id, preset, session, knobs, concurrent, target, started_at,
                        started_by, state, ending, reason, spent, live, samples, closures,
                        refusals, jobs_launched, jobs_settled)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', '', '', ?, '[]', '[]', '{}', '{}', 0, 0)`,
    [
      drain.id,
      drain.machineId,
      drain.preset,
      JSON.stringify(drain.session),
      JSON.stringify(drain.knobs),
      drain.concurrent,
      JSON.stringify(drain.target),
      new Date(store.now()).toISOString(),
      drain.startedBy,
      JSON.stringify(NO_SPEND),
    ],
  );
}

/**
 * One job this drain has just posted, added to what it is holding.
 *
 * The write is the whole array rather than an append, because the row is the controller's only
 * memory and two ticks in the same second must do the work of one: `live` is read, the job added
 * if it is not already there, and the array written back with the launch counted once.
 *
 * A CLOSING DRAIN HOLDS IT TOO, because recording a launch is not launching: by the time this is
 * called the hub has ALREADY taken the job and it is running. A launch is two writes —
 * `jobs.execute`, then this one — and an operator's stop can land between them; a write that
 * only landed under `running` made that job nobody's, running off the row with its receipt
 * folding into nothing and the drain taking its ending while it was still at the model (the
 * review of #285). What decides that nothing more is launched is the controller's own read of the
 * state, not this statement; what this statement must never do is drop a job that exists.
 */
export async function recordLaunch(
  store: DrainsStore,
  id: string,
  job: LiveJob,
  live: readonly LiveJob[],
): Promise<void> {
  if (live.some((entry) => entry.jobId === job.jobId)) return;
  await store.db.run(
    `UPDATE drains SET live = ?, jobs_launched = jobs_launched + 1
      WHERE id = ? AND state IN ('running', 'closing')`,
    [JSON.stringify([...live, job]), id],
  );
}

/** What one tick folded: the jobs still held, the settled totals, the tallies and the samples. */
export interface DrainFold {
  readonly live: readonly LiveJob[];
  readonly spent: DrainSpend;
  readonly closures: Readonly<Record<string, number>>;
  readonly refusals: Readonly<Record<string, number>>;
  readonly samples: readonly DrainSample[];
  readonly settledNow: number;
}

/**
 * The projections of one tick, written in one statement. A `closing` drain is folded by the same
 * statement as a running one: what its last jobs metered is still its own spend, and the only
 * thing it may not do is launch.
 */
export async function saveFold(store: DrainsStore, id: string, fold: DrainFold): Promise<void> {
  await store.db.run(
    `UPDATE drains SET live = ?, spent = ?, closures = ?, refusals = ?, samples = ?,
                       jobs_settled = jobs_settled + ?
      WHERE id = ? AND state IN ('running', 'closing')`,
    [
      JSON.stringify(fold.live),
      JSON.stringify(fold.spent),
      JSON.stringify(fold.closures),
      JSON.stringify(fold.refusals),
      JSON.stringify(fold.samples),
      fold.settledNow,
      id,
    ],
  );
}

/** What a close did: the drain ended, it is closing on its last receipts, or it had already gone. */
export type Closed = "ended" | "closing" | "already";

/**
 * The drain's end, written once. The guard is the WHERE clause rather than a read before the
 * write: two wakes that both decide the target is met close it once, and the second finds
 * nothing to close.
 *
 * A DRAIN THAT STILL HOLDS JOBS DOES NOT END HERE; it goes to `closing` with them. Those jobs
 * were paid for — a tick woken by a settlement cannot cancel them, and one that could would
 * still be waiting for the receipt — so what they metered is owed to this drain's total. The
 * ending it will take is recorded now, in `ending`, and {@link finishDrain} takes it when the
 * last of them has settled. Emptying `live` here instead, which is what a closed row used to do,
 * dropped up to (N−1) receipts out of the figure §11.5 asks an operator to read.
 */
export async function closeDrain(
  store: DrainsStore,
  id: string,
  ending: DrainEnding,
  reason: string,
  live: readonly LiveJob[],
): Promise<Closed> {
  if (live.length > 0) {
    // `closing` → `closing` is the ordinary second close: an operator stopping a drain that had
    // already met its target cancels its stragglers and changes nothing about why it ended, so
    // the statement is idempotent rather than a refusal the caller would report as a failure.
    const closing = await store.db.query<{ id: string }>(
      `UPDATE drains SET state = 'closing', ending = ?, reason = ?, live = ?
        WHERE id = ? AND state IN ('running', 'closing')
        RETURNING id`,
      [ending, reason, JSON.stringify(live), id],
    );
    return closing.length > 0 ? "closing" : "already";
  }
  const rows = await store.db.query<{ id: string }>(
    `UPDATE drains SET state = ?, ending = ?, reason = ?, finished_at = ?, live = '[]'
      WHERE id = ? AND state IN ('running', 'closing')
      RETURNING id`,
    [ending, ending, reason, new Date(store.now()).toISOString(), id],
  );
  return rows.length > 0 ? "ended" : "already";
}

/**
 * The last receipt of a closing drain has landed: it takes the ending it was closed with.
 *
 * `live = '[]'` is a condition and not a hope — a closing drain whose jobs have not all settled
 * is not finished, whatever a tick thinks — and the ending comes from the row rather than from
 * the caller, so the reason and the state a reader sees are the ones written when it closed.
 */
export async function finishDrain(store: DrainsStore, id: string): Promise<boolean> {
  const rows = await store.db.query<{ id: string }>(
    `UPDATE drains SET state = ending, finished_at = ?
      WHERE id = ? AND state = 'closing' AND live = '[]'
      RETURNING id`,
    [new Date(store.now()).toISOString(), id],
  );
  return rows.length > 0;
}

// ---------------------------------------------------------------------------- the live fold

/** One run of this drain, as the runs table and the conductor's fold answer for it. */
type DrainRunRow = {
  id: string;
  closure: string | null;
  cost_usd: number | null;
  tokens: number | bigint | null;
  payload: string;
  stage: string | null;
  stalled: number | bigint | null;
  calls: number | bigint | null;
  input_tokens: number | bigint | null;
  output_tokens: number | bigint | null;
  progress_cost: number | null;
};

/**
 * WHAT ONE SETTLED JOB METERED, read the way `runStatement` wrote it: THE METER FIRST, THE
 * RECEIPT SECOND.
 *
 * `payload.inference` is what the owner's proxy counted against the credential it holds, and it
 * is the number a drain's target is judged against — a metered run that spent nothing settles at
 * zero, and that is the meter's word rather than a hole. A run with NO `inference` block reached
 * no metered service (every run of the local lane is one), so its receipt's own `costUsd` and
 * `tokens` are recorded instead and no call count is claimed: the engine's word about its own
 * session is a different fact from the hub's word about it, and the two are not comparable to
 * the token.
 */
function settledOf(row: DrainRunRow): SettledRun {
  const held = parsed(row.payload);
  const receipt = held === null || typeof held !== "object" ? {} : (held as Record<string, unknown>);
  const reason = typeof receipt["reason"] === "string" ? receipt["reason"] : null;
  const refusal = reason === null ? null : refusalCode(reason);
  const metered = receipt["inference"];
  if (metered !== null && typeof metered === "object") {
    const block = metered as Record<string, unknown>;
    return {
      runId: row.id,
      closure: row.closure ?? "",
      spend: {
        calls: count(block["calls"] as number | undefined),
        inputTokens: count(block["inputTokens"] as number | undefined),
        outputTokens: count(block["outputTokens"] as number | undefined),
        costMicros: count(block["costMicros"] as number | undefined),
      },
      refusal,
    };
  }
  return {
    runId: row.id,
    closure: row.closure ?? "",
    spend: {
      calls: 0,
      inputTokens: 0,
      outputTokens: count(row.tokens),
      costMicros: Math.round(count(row.cost_usd) * 1_000_000),
    },
    refusal,
  };
}

/**
 * Where every job this drain is holding stands, in one read.
 *
 * The join is to `run_progress`, which is the conductor's fold of each running job's replay ring
 * (#261) and the only live account of a job's spend there is. A running job with no row there has
 * said nothing yet — still queued, or inside the owner's five-second coalescing window — and
 * contributes nothing rather than a zero that reads like a measurement.
 */
export async function reconcileLive(
  store: DrainsStore,
  live: readonly LiveJob[],
): Promise<Reconciled> {
  if (live.length === 0) {
    return { holding: [], settled: [], missing: [], atModel: 0, stalled: 0, inFlight: NO_SPEND };
  }
  const holes = live.map(() => "?").join(", ");
  const rows = await store.db.query<DrainRunRow>(
    `SELECT r.id AS id, r.closure AS closure, r.cost_usd AS cost_usd, r.tokens AS tokens,
            r.payload AS payload, p.stage AS stage, p.stalled AS stalled, p.calls AS calls,
            p.input_tokens AS input_tokens, p.output_tokens AS output_tokens,
            p.cost_usd AS progress_cost
       FROM runs r LEFT JOIN run_progress p ON p.run_id = r.id
      WHERE r.id IN (${holes})`,
    live.map((entry) => entry.runId),
  );
  const byRun = new Map(rows.map((row) => [row.id, row]));
  const holding: LiveJob[] = [];
  const missing: LiveJob[] = [];
  const settled: SettledRun[] = [];
  const inFlight = { ...NO_SPEND };
  let atModel = 0;
  let stalled = 0;
  for (const entry of live) {
    const row = byRun.get(entry.runId);
    if (row === undefined) {
      missing.push(entry);
      continue;
    }
    if (row.closure !== null) {
      settled.push(settledOf(row));
      continue;
    }
    holding.push(entry);
    if (row.stage === RUN_STAGES.atModel) atModel += 1;
    if (count(row.stalled) === 1) stalled += 1;
    inFlight.calls += count(row.calls);
    inFlight.inputTokens += count(row.input_tokens);
    inFlight.outputTokens += count(row.output_tokens);
    // The fold keeps dollars and a target is micro-dollars: the conversion is here rather than
    // at the comparison, so the two spend figures a status reports are in one unit.
    inFlight.costMicros += Math.round(count(row.progress_cost) * 1_000_000);
  }
  return { holding, settled, missing, atModel, stalled, inFlight };
}

// ---------------------------------------------------------------------------- the rate, the ETA

export function addSpend(left: DrainSpend, right: DrainSpend): DrainSpend {
  return {
    calls: left.calls + right.calls,
    inputTokens: left.inputTokens + right.inputTokens,
    outputTokens: left.outputTokens + right.outputTokens,
    costMicros: left.costMicros + right.costMicros,
  };
}

/**
 * Appends one observation and drops what the rate window no longer needs, keeping the oldest
 * sample that still ANCHORS the window: a rate over three minutes is the newest sample minus the
 * one from three minutes ago, so pruning to the window alone would leave a single point and a
 * rate of zero every time the samples aged out together.
 */
export function sample(
  held: readonly DrainSample[],
  at: number,
  spend: DrainSpend,
): readonly DrainSample[] {
  const next = [...held, { at, outputTokens: spend.outputTokens, costMicros: spend.costMicros }];
  const anchor = at - RATE_WINDOW_MS;
  const inWindow = next.filter((entry) => entry.at >= anchor);
  const older = next.filter((entry) => entry.at < anchor);
  const kept = older.length === 0 ? inWindow : [older[older.length - 1] as DrainSample, ...inWindow];
  return kept.length > MAX_SAMPLES ? kept.slice(kept.length - MAX_SAMPLES) : kept;
}

/**
 * What the drain is producing a minute, over the last {@link RATE_WINDOW_MS} of samples.
 *
 * THE LEFT EDGE OF THE WINDOW IS THE ANCHOR — the newest sample OLDER than it — and only when
 * there is none is it the oldest sample inside. {@link sample} keeps that anchor for exactly
 * this reason, and a rate that looked for its left edge inside the window instead found the
 * newest sample every time the previous one had aged out: a drain whose ticks are more than
 * three minutes apart (they happen on settlements and on one 30-second floor behind a wake, so
 * an unwatched drain's are) read `0/min` for its whole life — the one reading the runbook's
 * no-go rule acts on (§11.4), inverted.
 *
 * Zero when there is nothing to measure — one sample, or two taken in the same instant — and
 * zero is then the honest answer rather than a division nobody can read: the runbook's rule is
 * about a rate that has STOPPED moving, and a rate that has not been observed twice yet has not
 * moved or stalled. It is never negative: the totals only rise, and a row that somehow went
 * backwards is a reading to discard rather than a negative burn to report.
 */
export function burnRate(
  samples: readonly DrainSample[],
  at: number,
): { readonly outputTokensPerMinute: number; readonly costMicrosPerMinute: number } {
  const newest = samples[samples.length - 1];
  if (newest === undefined) return { outputTokensPerMinute: 0, costMicrosPerMinute: 0 };
  const anchor = at - RATE_WINDOW_MS;
  const older = samples.filter((entry) => entry.at < anchor);
  const oldest = older[older.length - 1] ?? samples.find((entry) => entry.at >= anchor);
  if (oldest === undefined || oldest.at >= newest.at) {
    return { outputTokensPerMinute: 0, costMicrosPerMinute: 0 };
  }
  const minutes = (newest.at - oldest.at) / 60_000;
  return {
    outputTokensPerMinute: Math.max(0, (newest.outputTokens - oldest.outputTokens) / minutes),
    costMicrosPerMinute: Math.max(0, (newest.costMicros - oldest.costMicros) / minutes),
  };
}

/**
 * When this rate reaches the target, as an instant, or empty.
 *
 * Empty is three different honest answers and never a guess: there is no spend target to reach,
 * the target is already met, or nothing is being produced — and the panel shows the deadline
 * beside this, so "no ETA while jobs are at the model" is the reading the go/no-go rule acts on
 * (§11.3) rather than a number invented to fill the column.
 */
export function targetEta(
  target: DrainTarget,
  spent: DrainSpend,
  rate: { readonly outputTokensPerMinute: number; readonly costMicrosPerMinute: number },
  at: number,
): string {
  const legs: number[] = [];
  if (target.costMicros !== undefined) {
    const left = target.costMicros - spent.costMicros;
    if (left <= 0) return new Date(at).toISOString();
    if (rate.costMicrosPerMinute <= 0) return "";
    legs.push(left / rate.costMicrosPerMinute);
  }
  if (target.outputTokens !== undefined) {
    const left = target.outputTokens - spent.outputTokens;
    if (left <= 0) return new Date(at).toISOString();
    if (rate.outputTokensPerMinute <= 0) return "";
    legs.push(left / rate.outputTokensPerMinute);
  }
  // The first target to be met ends the drain, so the ETA is the nearest of them.
  const minutes = legs.length === 0 ? null : Math.min(...legs);
  if (minutes === null || !Number.isFinite(minutes)) return "";
  return new Date(at + minutes * 60_000).toISOString();
}

/** Whether a spend target has been reached, and which one; empty when none has. */
export function targetMet(target: DrainTarget, spent: DrainSpend): string {
  if (target.costMicros !== undefined && spent.costMicros >= target.costMicros) {
    return `the target of ${String(target.costMicros)} micro-dollars is met at ${String(spent.costMicros)}`;
  }
  if (target.outputTokens !== undefined && spent.outputTokens >= target.outputTokens) {
    return `the target of ${String(target.outputTokens)} output tokens is met at ${String(spent.outputTokens)}`;
  }
  return "";
}

/** The deadline as an instant, or null: a target that names none, or names an unreadable one. */
export function deadlineOf(target: DrainTarget): number | null {
  if (target.deadline === undefined) return null;
  const at = Date.parse(target.deadline);
  return Number.isFinite(at) ? at : null;
}

/**
 * THE ACCOUNT AS IT CAN BE NAMED (#267). The identity key is empty for an api-key credential,
 * where the broker's own row IS the account, so the credential is named rather than leaving a
 * blank where the answer to "which account is this burning" belongs — and it is one function
 * rather than two readings, because the drain the door answers for and the drain the panel polls
 * are the same drain: `Draining  as drn_…` is what two of them produced.
 */
export function accountName(session: SessionChoice): string {
  const account = session.account;
  return account.identityKey === ""
    ? `${account.provider}#${account.credentialId}`
    : account.identityKey;
}

// ---------------------------------------------------------------------------- the status

/**
 * One drain as the panel watches it: the row, plus one read of where its jobs are.
 *
 * `spent` is the WHOLE spend — what its settled jobs metered, plus what the ones that have
 * closed since the last tick metered, plus what the running ones have metered so far — because
 * that is the figure the target is judged against and the figure an operator is asking about.
 * `settled` is the durable part of it, reported beside it so a receipt and a fold in progress
 * can be told apart. NOTHING HERE WRITES: a read of a drain never advances it, so the sample
 * this rate is measured against is taken and discarded, and the controller's own tick is what
 * persists one.
 */
export async function drainStatus(store: DrainsStore, row: DrainRow): Promise<DrainStatus> {
  const at = store.now();
  const seen = await reconcileLive(store, row.live);
  const settled = seen.settled.reduce((total, run) => addSpend(total, run.spend), row.spent);
  const spent = addSpend(settled, seen.inFlight);
  const rate = burnRate(sample(row.samples, at, spent), at);
  return {
    drainId: row.id,
    machineId: row.machineId,
    preset: row.preset,
    state: row.state,
    reason: row.reason,
    startedAt: row.startedAt,
    startedBy: row.startedBy,
    finishedAt: row.finishedAt,
    concurrent: row.concurrent,
    target: row.target,
    account: accountName(row.session),
    model: row.session.model,
    jobsLaunched: row.jobsLaunched,
    jobsSettled: row.jobsSettled + seen.settled.length,
    jobsLive: seen.holding.length,
    jobsAtModel: seen.atModel,
    jobsStalled: seen.stalled,
    spent,
    settled,
    outputTokensPerMinute: rate.outputTokensPerMinute,
    costMicrosPerMinute: rate.costMicrosPerMinute,
    // A drain that has ended has no ETA: what it spent is what it spent.
    etaAt: row.state === "running" ? targetEta(row.target, spent, rate, at) : "",
    refusals: { ...row.refusals },
    closures: { ...row.closures },
  };
}
