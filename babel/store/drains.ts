import type { PluginDatabase, SqlParam } from "@manifold/plugin";
import {
  CodeProfileSchema,
  DRAIN_ENDINGS,
  DRAIN_NOTE_KINDS,
  DRAIN_PRESETS,
  DRAIN_REPORT_KIND,
  DRAIN_REPORT_PROVENANCE,
  DRAIN_REPORT_SCHEMA,
  DRAIN_STATES,
  DrainReportSchema,
  DrainSpendSchema,
  DrainProfileSchema,
  DrainStartInputSchema,
  MACHINE_OPERATIONS,
  RUN_STAGES,
  type DrainEnding,
  type DrainLane,
  type DrainNoteKind,
  type DrainPreset,
  type DrainReportPayload,
  type DrainSpend,
  type DrainState,
  type DrainStatus,
  type DrainTarget,
  type DrainTokens,
  type DrainProfile,
  type LaunchInput,
} from "../contract.ts";
import { createHash } from "node:crypto";
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
 * One observation of the drain's whole spend AND of the fan carrying it, taken once per tick.
 *
 * A RATE NEEDS TWO OBSERVATIONS and nothing else in the store keeps a series: `run_progress` is
 * rewritten in place and a settled run keeps a total. Without these, "tokens per minute" could
 * only be a total over an elapsed time, which never reads flat — and reading flat for three
 * minutes while jobs say `at the model` is the one no-go `docs/runbook.md` §11.4 names.
 *
 * `held` and `atModel` are here for the report's sake (#270): they are the peaks a drain reached,
 * and they are read off `run_progress` too, so a drain that kept only totals could never say
 * afterwards how much of its fan was ever actually at a model.
 */
export interface DrainSample {
  readonly at: number;
  readonly outputTokens: number;
  readonly costMicros: number;
  readonly held: number;
  readonly atModel: number;
}

/**
 * THE CONTROLLER'S OWN NOTES, KEPT (#270). A tick reports what it could not do and the report is
 * discarded with the wake; none of it is durable anywhere else, and three of the six kinds are
 * durable nowhere at all — an admission refusal writes no run row, a stall is read off
 * `run_progress` which is dropped the instant a run settles, and an adopted job is a write this
 * controller lost. So they are journaled on the row beside the samples.
 */
export interface DrainNote {
  readonly at: number;
  readonly kind: DrainNoteKind;
  readonly detail: string;
}

/**
 * WHAT THE `samples` COLUMN HOLDS: the series, the notes, and two integrals.
 *
 * It is one JSON document in one column because the drains table is created once by the enable
 * hook and `SCHEMA_ADDITIONS` cannot widen a row that a store already in the field has
 * (`store/schema.ts` says why at length). The column is read and written HERE and nowhere else,
 * so its shape is this module's to choose; a row written before the journal existed is a bare
 * array and still parses, which is what {@link journalOf} is for.
 *
 * `heldMs` and `atModelMs` are RECTANGLES, one per tick: at each fold the jobs held and the jobs
 * at the model are multiplied by the time since the previous fold and added. Ticks are sparse —
 * a settlement, or one 30-second floor behind a wake — so it is an approximation, and it is the
 * only account of "how much of this fan was ever at a model" that survives the drain, because
 * `run_progress` does not.
 */
export interface DrainJournal {
  readonly samples: readonly DrainSample[];
  readonly notes: readonly DrainNote[];
  /** Notes the bound dropped, so a short list never reads as a quiet drain. */
  readonly notesDropped: number;
  readonly heldMs: number;
  readonly atModelMs: number;
  /** When the integrals were last advanced; 0 before the first fold. */
  readonly observedAt: number;
}

export interface DrainRow {
  readonly id: string;
  readonly machineId: string;
  readonly preset: DrainPreset;
  /** The Code profile this fan is posted on, and Babel's ledger of what Code said it runs as. */
  readonly profile: DrainProfile;
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
  readonly journal: DrainJournal;
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
  readonly maxJobs?: number | undefined;
  readonly inferenceLimits?: LaunchInput["inferenceLimits"];
  /**
   * THE CODE PROFILE EVERY JOB OF THIS DRAIN IS POSTED ON (#279). It is a knob rather than a
   * column for the reason the others are: it is the launch input's own field, kept verbatim so
   * the ninetieth job of a drain asks for what the first one did. A drain that names none
   * refuses `profile_required` at the seam, which is the honest answer — a Code session needs a
   * Code profile, and Babel has no model of its own to fall back on.
   */
  readonly profile?:
    { readonly containerId: string; readonly expectedRevision: number } | undefined;
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

/**
 * How many of the controller's notes a row keeps. Sixty-four because a drain's report is meant
 * to be readable in one sitting, and because the row is the controller's memory rather than a
 * log: what a drain with two hundred admission refusals needs to say is the count, which
 * `launchRefusals` carries whole, and the sentences are the evidence for it. What the bound drops
 * is counted, so a short list never reads as a quiet drain.
 */
const MAX_NOTES = 64;

export const NO_JOURNAL: DrainJournal = {
  samples: [],
  notes: [],
  notesDropped: 0,
  heldMs: 0,
  atModelMs: 0,
  observedAt: 0,
};

const NOTE_KINDS: readonly string[] = DRAIN_NOTE_KINDS;

const PRESETS: readonly string[] = DRAIN_PRESETS;
const STATES: readonly string[] = DRAIN_STATES;
const ENDINGS: readonly string[] = DRAIN_ENDINGS;

/** Every column, in one place, so a read and a write cannot come to disagree about the shape. */
const COLUMNS =
  `id, machine_id, preset, profile, knobs, concurrent, target, started_at, started_by, ` +
  `finished_at, state, ending, reason, spent, live, samples, closures, refusals, ` +
  `jobs_launched, jobs_settled`;

/** A stored row as SQLite hands it back; a type alias, so the query generic accepts it. */
type DrainDbRow = {
  id: string;
  machine_id: string;
  preset: string;
  profile: string;
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

function samplesOf(held: unknown): DrainSample[] {
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
      held: count(row["held"] as number | undefined),
      atModel: count(row["atModel"] as number | undefined),
    });
  }
  return samples.sort((left, right) => left.at - right.at);
}

function notesOf(held: unknown): DrainNote[] {
  if (!Array.isArray(held)) return [];
  const notes: DrainNote[] = [];
  for (const entry of held) {
    if (entry === null || typeof entry !== "object") continue;
    const row = entry as Record<string, unknown>;
    const kind = row["kind"];
    const detail = row["detail"];
    if (typeof kind !== "string" || !NOTE_KINDS.includes(kind)) continue;
    if (typeof detail !== "string") continue;
    notes.push({ at: count(row["at"] as number | undefined), kind: kind as DrainNoteKind, detail });
  }
  return notes;
}

/**
 * The `samples` column, as the journal this module writes OR as the bare array a row written
 * before the journal existed holds. A store already in the field carries the old shape and no
 * migration can reach it — the enable hook's additions are per-object, not per-column — so the
 * old shape is read as a journal with no notes and no integrals, which is exactly what it is.
 */
function journalOf(text: string): DrainJournal {
  const held = parsed(text);
  if (Array.isArray(held)) return { ...NO_JOURNAL, samples: samplesOf(held) };
  if (held === null || typeof held !== "object") return NO_JOURNAL;
  const row = held as Record<string, unknown>;
  return {
    samples: samplesOf(row["samples"]),
    notes: notesOf(row["notes"]),
    notesDropped: count(row["notesDropped"] as number | undefined),
    heldMs: count(row["heldMs"] as number | undefined),
    atModelMs: count(row["atModelMs"] as number | undefined),
    observedAt: count(row["observedAt"] as number | undefined),
  };
}

/**
 * The preset's stored knobs. A knob whose stored value is not the type the launch input takes is
 * left absent rather than coerced: the launch path then asks for its own default, which is the
 * documented request, instead of reading a `"3"` as three days somewhere downstream.
 */
function knobsOf(text: string): DrainKnobs {
  const held: unknown = JSON.parse(text);
  if (held === null || typeof held !== "object" || Array.isArray(held))
    throw new Error("the drain's stored knobs are not an object");
  const row = held as Record<string, unknown>;
  const recipes = Array.isArray(row["recipes"])
    ? row["recipes"].filter((entry): entry is string => typeof entry === "string")
    : [];
  // The profile is read back through the contract's own schema rather than field by field: a
  // container id and a revision are what `runSession` is pinned by, and a half-read pair would
  // post a session against a revision nobody was shown.
  const profile = CodeProfileSchema.safeParse(row["profile"]);
  // A malformed safety bound must never become an unbounded replay.
  const maxJobs = DrainStartInputSchema.shape.maxJobs.parse(row["maxJobs"]);
  const inferenceLimits = DrainStartInputSchema.shape.inferenceLimits.parse(
    row["inferenceLimits"],
  );
  return {
    recipes,
    ...(maxJobs === undefined ? {} : { maxJobs }),
    ...(inferenceLimits === undefined ? {} : { inferenceLimits }),
    ...(typeof row["sinceDays"] === "number" ? { sinceDays: row["sinceDays"] } : {}),
    ...(typeof row["entityId"] === "string" ? { entityId: row["entityId"] } : {}),
    ...(typeof row["minutes"] === "number" ? { minutes: row["minutes"] } : {}),
    ...(typeof row["agentSessions"] === "boolean" ? { agentSessions: row["agentSessions"] } : {}),
    ...(profile.success ? { profile: profile.data } : {}),
  };
}

/**
 * One stored row as the controller reads it. A `preset` or a `state` outside the vocabulary is
 * the store holding a value its own CHECK forbids, so it is refused rather than guessed at: a
 * drain whose preset nobody can name is one nothing may relaunch.
 */
function rowOf(row: DrainDbRow): DrainRow {
  if (!PRESETS.includes(row.preset)) {
    throw new Error(
      `drain ${row.id} names the preset ${row.preset}, which is not one a drain runs`,
    );
  }
  if (!STATES.includes(row.state)) {
    throw new Error(`drain ${row.id} is in the state ${row.state}, which is not one a drain has`);
  }
  if (row.ending !== "" && !ENDINGS.includes(row.ending)) {
    throw new Error(`drain ${row.id} is closing towards ${row.ending}, which is not an ending`);
  }
  const profile = DrainProfileSchema.safeParse(parsed(row.profile));
  if (!profile.success) {
    throw new Error(`drain ${row.id} names no Code profile a run could be posted on`);
  }
  const target = parsed(row.target);
  return {
    id: row.id,
    machineId: row.machine_id,
    preset: row.preset as DrainPreset,
    profile: profile.data,
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
    journal: journalOf(row.samples),
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
export async function recentDrains(
  store: DrainsStore,
  limit: number,
): Promise<readonly DrainRow[]> {
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
  readonly profile: DrainProfile;
  readonly knobs: DrainKnobs;
  readonly concurrent: number;
  readonly target: DrainTarget;
  readonly startedBy: string;
}

/** The row a `drain.start` leaves behind, before its first job is posted. */
export async function insertDrain(store: DrainsStore, drain: NewDrain): Promise<void> {
  await store.db.run(
    `INSERT INTO drains(id, machine_id, preset, profile, knobs, concurrent, target, started_at,
                        started_by, state, ending, reason, spent, live, samples, closures,
                        refusals, jobs_launched, jobs_settled)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', '', '', ?, '[]', '[]', '{}', '{}', 0, 0)`,
    [
      drain.id,
      drain.machineId,
      drain.preset,
      JSON.stringify(drain.profile),
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
 * The ordinal is the durable admission cursor, not the caller's stale `live` array. Appending
 * and advancing it happen in one statement against the persisted row. A second wake adopting
 * the same ordinal does nothing, even if that job has already settled and left `live`.
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
  ordinal: number,
): Promise<boolean> {
  const rows = await store.db.query<{ id: string }>(
    `UPDATE drains SET live = json_insert(live, '$[#]', json(?)),
                       jobs_launched = jobs_launched + 1
      WHERE id = ? AND state IN ('running', 'closing') AND jobs_launched = ?
        AND NOT EXISTS (SELECT 1 FROM json_each(drains.live)
                         WHERE json_extract(value, '$.jobId') = ?)
      RETURNING id`,
    [JSON.stringify(job), id, ordinal, job.jobId],
  );
  return rows.length > 0;
}

/** What one tick folded: the jobs still held, the settled totals, the tallies and the journal. */
export interface DrainFold {
  readonly live: readonly LiveJob[];
  readonly spent: DrainSpend;
  readonly closures: Readonly<Record<string, number>>;
  readonly refusals: Readonly<Record<string, number>>;
  readonly journal: DrainJournal;
  readonly settledNow: number;
}

/**
 * The projections of one tick, compared against the snapshot it reconciled. A competing fold
 * may already have consumed the receipts; a launch may have appended a job. Neither may be
 * overwritten by this older view. The caller re-reads and reconciles when the comparison loses.
 */
export async function saveFold(
  store: DrainsStore,
  row: DrainRow,
  fold: DrainFold,
): Promise<boolean> {
  const rows = await store.db.query<{ id: string }>(
    `UPDATE drains SET live = ?, spent = ?, closures = ?, refusals = ?, samples = ?,
                       jobs_settled = jobs_settled + ?
      WHERE id = ? AND state = ? AND state IN ('running', 'closing')
        AND live = ? AND jobs_launched = ? AND jobs_settled = ?
      RETURNING id`,
    [
      JSON.stringify(fold.live),
      JSON.stringify(fold.spent),
      JSON.stringify(fold.closures),
      JSON.stringify(fold.refusals),
      JSON.stringify(fold.journal),
      fold.settledNow,
      row.id,
      row.state,
      JSON.stringify(row.live),
      row.jobsLaunched,
      row.jobsSettled,
    ],
  );
  return rows.length > 0;
}

/**
 * The journal with one tick's observation folded in: the sample appended, the two integrals
 * advanced, and whatever the tick could not do written down.
 *
 * THE RECTANGLE IS THE PREVIOUS OBSERVATION'S, NOT THIS ONE'S, and the difference is the whole
 * measurement. A tick sees a fan; that fan is what was running until the NEXT tick sees
 * something else. Multiplying the current reading by the interval just ended would credit the
 * interval to whatever happened to be true at its end — and since a drain's last tick is the one
 * where everything has settled, every drain would report zero time at the model however long its
 * jobs spent there.
 *
 * `observedAt` anchors the interval rather than the newest sample's instant, because the samples
 * are PRUNED to the rate window and the integrals are for the drain's whole life. A first fold
 * advances nothing: one instant is not an interval.
 */
export function foldJournal(
  journal: DrainJournal,
  at: number,
  spent: DrainSpend,
  fan: { readonly held: number; readonly atModel: number },
  notes: readonly DrainNote[],
): DrainJournal {
  const elapsed = journal.observedAt === 0 ? 0 : Math.max(0, at - journal.observedAt);
  const before = journal.samples[journal.samples.length - 1];
  return {
    samples: sample(journal.samples, at, spent, fan),
    ...noted(journal, notes),
    heldMs: journal.heldMs + (before?.held ?? 0) * elapsed,
    atModelMs: journal.atModelMs + (before?.atModel ?? 0) * elapsed,
    observedAt: at,
  };
}

/** The journal's notes with these appended, oldest dropped past {@link MAX_NOTES} and counted. */
function noted(
  journal: DrainJournal,
  notes: readonly DrainNote[],
): { readonly notes: readonly DrainNote[]; readonly notesDropped: number } {
  if (notes.length === 0) {
    return { notes: journal.notes, notesDropped: journal.notesDropped };
  }
  const all = [...journal.notes, ...notes];
  const over = Math.max(0, all.length - MAX_NOTES);
  return { notes: all.slice(over), notesDropped: journal.notesDropped + over };
}

/**
 * WHAT A TICK COULD NOT DO, WRITTEN DOWN WITHOUT A FOLD. The launch round happens after the
 * fold — a target met by the job that just settled must end the drain before one more is posted
 * — so an admission refusal is known one statement too late to travel with it. It is its own
 * write rather than a second fold because a fold advances the integrals, and advancing them
 * twice in one instant would count the same rectangle twice.
 */
export async function noteDrain(
  store: DrainsStore,
  id: string,
  notes: readonly DrainNote[],
): Promise<void> {
  if (notes.length === 0) return;
  const rows = await store.db.query<{ samples: string }>(
    `SELECT samples FROM drains WHERE id = ?`,
    [id],
  );
  const row = rows[0];
  if (row === undefined) return;
  const journal = journalOf(row.samples);
  await store.db.run(`UPDATE drains SET samples = ? WHERE id = ?`, [
    JSON.stringify({ ...journal, ...noted(journal, notes) }),
    id,
  ]);
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
): Promise<Closed> {
  const rows = await store.db.query<{ state: string }>(
    `UPDATE drains
        SET state = CASE WHEN live = '[]' THEN ? ELSE 'closing' END,
            ending = ?, reason = ?,
            finished_at = CASE WHEN live = '[]' THEN ? ELSE NULL END
      WHERE id = ? AND state IN ('running', 'closing')
      RETURNING state`,
    [ending, ending, reason, new Date(store.now()).toISOString(), id],
  );
  const row = rows[0];
  return row === undefined ? "already" : row.state === "closing" ? "closing" : "ended";
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
  const receipt =
    held === null || typeof held !== "object" ? {} : (held as Record<string, unknown>);
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
  fan: { readonly held: number; readonly atModel: number } = { held: 0, atModel: 0 },
): readonly DrainSample[] {
  const next = [
    ...held,
    {
      at,
      outputTokens: spend.outputTokens,
      costMicros: spend.costMicros,
      held: fan.held,
      atModel: fan.atModel,
    },
  ];
  const anchor = at - RATE_WINDOW_MS;
  const inWindow = next.filter((entry) => entry.at >= anchor);
  const older = next.filter((entry) => entry.at < anchor);
  const kept =
    older.length === 0 ? inWindow : [older[older.length - 1] as DrainSample, ...inWindow];
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
 * WHOSE WINDOW THIS DRAIN IS SPENDING, as CODE reported it when the drain started (#267).
 *
 * Babel has no broker and chooses no account: the account belongs to the Code profile, and
 * the only honest source for it is Code's own list, read once at the start and recorded as a
 * ledger entry. So this reads what was recorded and NEVER infers: a profile Code reported no
 * account for says that, in as many words, rather than leaving the blank that made "which
 * account did that fan burn" unanswerable on 2026-09-13.
 */
export function accountName(profile: DrainProfile): string {
  const named = profile.accounts
    .map((account) => account.label || account.identityKey || account.provider)
    .filter((name) => name !== "");
  const container = profile.profile.containerId;
  if (named.length > 0) return `${container}: ${named.join(", ")} (as Code reported at start)`;
  // `resolved: false` with an empty list means ASK AGAIN, never "spends nothing": Code stores
  // its account choices as exclusions, so without a live observation there is no list to give.
  return profile.resolved
    ? `${container} (Code reported no account)`
    : `${container} (Code could not resolve an account)`;
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
export async function drainStatus(
  store: DrainsStore,
  row: DrainRow,
  report: DrainReportPayload | null = null,
): Promise<DrainStatus> {
  const at = store.now();
  const seen = await reconcileLive(store, row.live);
  const settled = seen.settled.reduce((total, run) => addSpend(total, run.spend), row.spent);
  const spent = addSpend(settled, seen.inFlight);
  const rate = burnRate(sample(row.journal.samples, at, spent), at);
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
    account: accountName(row.profile),
    model: row.profile.model,
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
    report,
  };
}

// ---------------------------------------------------------------------------- the drain's report

/*
  WHAT A DRAIN LEAVES BEHIND (#270), AND WHERE EVERY NUMBER IN IT COMES FROM.

  THE RELATION NOTHING HAD. A drain's receipts were one per job with nothing tying them to the
  drain: `live` holds a job only while it runs, so by the time a drain ended the row named none
  of them. The tie is the identity the controller DERIVES — `run_<drain>_<ordinal>`, and for the
  lane that spends a preparation at `<runId>_material` — so the runs of a drain are recomputable
  from the drain id and its launch count, with no column and no join table. That is the whole
  reason this can be built at the end rather than accumulated in a shape nobody could widen.

  WHAT IS READ FROM WHERE:
    the row          the allocation as NAMED, the target, the fan, the account ledger, the
                     closures and refusal codes its folds tallied, and the journal (notes, the
                     samples' peaks, and the two integrals `load` is);
    `runs`           per job: its kind, its closure, its wall clock, the recipes its preparation
                     carried, the account its launch report named, and `payload.inference` —
                     THE HUB'S OWN METER, which is what every token and every micro-dollar here
                     is. A run with no `inference` block reached no metered service, and its own
                     `cost_usd`/`tokens` stand in with no call count claimed, exactly as
                     {@link settledOf} does for a live fold;
    `records`        what this drain put into the corpus, by `run_id`;
    `assessments`    the judgements it produced, by `run_id`.
*/

/** The record identifier a drain's report takes: derived, so a second close writes no second. */
export function drainReportId(drainId: string): string {
  const digest = createHash("sha256").update(`drain-report\u0000${drainId}`).digest("hex");
  return `fnd_${digest.slice(0, 32)}`;
}

/**
 * THE RUNS OF ONE DRAIN, derived. Two per launch on a spending preset — the session's run and
 * the `prepare` that sealed its material — and one on the beat, which posts no preparation. The
 * ids that name nothing are simply not there when the query comes back, which is also how a
 * launch whose row write never landed is counted.
 */
function drainRunIds(row: DrainRow): readonly string[] {
  const ids: string[] = [];
  for (let ordinal = 0; ordinal < row.jobsLaunched; ordinal += 1) {
    const runId = `run_${row.id}_${String(ordinal)}`;
    ids.push(runId, `${runId}_material`);
  }
  return ids;
}

type ReportRunRow = {
  id: string;
  kind: string | null;
  closure: string | null;
  cost_usd: number | null;
  tokens: number | bigint | null;
  started_at: string | null;
  finished_at: string | null;
  preparation: string | null;
  profile: string | null;
  payload: string;
};

/** How many ids one read asks for. Well under the engine's row cap, and a drain of a hundred
 *  jobs is two reads rather than one query nobody can plan. */
const REPORT_PAGE = 400;

const NO_TOKENS: DrainTokens = {
  calls: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  costMicros: 0,
};

function addTokens(left: DrainTokens, right: DrainTokens): DrainTokens {
  return {
    calls: left.calls + right.calls,
    inputTokens: left.inputTokens + right.inputTokens,
    outputTokens: left.outputTokens + right.outputTokens,
    cacheReadTokens: left.cacheReadTokens + right.cacheReadTokens,
    costMicros: left.costMicros + right.costMicros,
  };
}

/** What the hub metered for one run, or what the run said about itself when nothing metered it. */
function tokensOf(row: ReportRunRow): { readonly tokens: DrainTokens; readonly metered: boolean } {
  const held = parsed(row.payload);
  const receipt =
    held === null || typeof held !== "object" ? {} : (held as Record<string, unknown>);
  const metered = receipt["inference"];
  if (metered !== null && typeof metered === "object") {
    const block = metered as Record<string, unknown>;
    return {
      tokens: {
        calls: count(block["calls"] as number | undefined),
        inputTokens: count(block["inputTokens"] as number | undefined),
        outputTokens: count(block["outputTokens"] as number | undefined),
        cacheReadTokens: count(block["cachedInputTokens"] as number | undefined),
        costMicros: count(block["costMicros"] as number | undefined),
      },
      metered: true,
    };
  }
  return {
    tokens: {
      ...NO_TOKENS,
      outputTokens: count(row.tokens),
      costMicros: Math.round(count(row.cost_usd) * 1_000_000),
    },
    metered: false,
  };
}

/** The recipes one run carried, from the preparation its launch recorded. */
function recipesOf(row: ReportRunRow): readonly string[] {
  const held = parsed(row.preparation ?? "");
  if (held === null || typeof held !== "object") return [];
  const listed = (held as Record<string, unknown>)["recipes"];
  if (!Array.isArray(listed)) return [];
  const ids: string[] = [];
  for (const entry of listed) {
    if (entry === null || typeof entry !== "object") continue;
    const id = (entry as Record<string, unknown>)["id"];
    if (typeof id === "string" && id !== "") ids.push(id);
  }
  return ids;
}

/** The account one run's launch report named, or empty: the drain's own ledger stands in. */
function accountOf(row: ReportRunRow): string {
  const held = parsed(row.profile ?? "");
  if (held === null || typeof held !== "object") return "";
  const account = (held as Record<string, unknown>)["account"];
  if (account === null || typeof account !== "object") return "";
  const named = account as Record<string, unknown>;
  const key = typeof named["identityKey"] === "string" ? named["identityKey"] : "";
  const provider = typeof named["provider"] === "string" ? named["provider"] : "";
  return key === "" ? provider : `${provider}:${key}`;
}

/** Milliseconds between two recorded instants, or 0 when either is missing or unreadable. */
function spanMs(from: string, to: string): number {
  const start = Date.parse(from);
  const end = Date.parse(to);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return 0;
  return end - start;
}

/**
 * The code a LAUNCH refusal carries. It is not {@link refusalCode}'s vocabulary — that one is
 * closed over what a SUBMISSION may be refused for — and the two must not be folded together: a
 * job that was never posted is a different fact from paid work with no result. A refusal the hub
 * named carries its code first (`concurrency_limit: …`, `profile_required: …`); one written as a
 * plain sentence is `unnamed`, which is honest and countable.
 */
function launchCode(detail: string): string {
  const at = detail.indexOf(":");
  if (at <= 0) return "unnamed";
  const head = detail.slice(0, at);
  return /^[a-z][a-z0-9_]*$/u.test(head) ? head : "unnamed";
}

function bumped(tally: Record<string, number>, key: string): void {
  tally[key] = (tally[key] ?? 0) + 1;
}

function lanes(held: Map<string, { runs: number; tokens: DrainTokens }>): DrainLane[] {
  return [...held.entries()]
    .map(([name, lane]) => ({ name, runs: lane.runs, tokens: lane.tokens }))
    .sort(
      (left, right) =>
        right.tokens.costMicros - left.tokens.costMicros || (left.name < right.name ? -1 : 1),
    );
}

function perMillion(produced: number, tokens: DrainTokens): number {
  const total = tokens.inputTokens + tokens.outputTokens;
  return total === 0 ? 0 : (produced * 1_000_000) / total;
}

/** What this deployment cannot observe about a drain, said once rather than carried as nulls. */
const UNOBSERVED: readonly string[] = [
  "the machine's CPU load and memory over the drain's life: a plugin's server half reads no " +
    "/proc and the hub reports neither, so `load` is Babel's own fan and nothing about the host",
  "cache-write tokens: the hub's `inference_call` frame carries `cachedInputTokens` and nothing " +
    "else about the cache, so only cache READS are countable",
  "the account's usage window at the start and at the end: Code owns the account and reports no " +
    "window reading to Babel (atyrode/code#165), so `account` is the name and never a percentage",
];

/**
 * THE WHOLE ACCOUNT OF ONE DRAIN, built from its row and the runs its own identities name.
 *
 * It is built at the end rather than accumulated because everything it needs is durable by then:
 * a settled run keeps its receipt and the hub's meter beside it, and the row keeps the tallies,
 * the journal and the launch count. What is NOT durable — a stall, an admission refusal, a job
 * this controller adopted — is journaled as it happens, which is what {@link DrainJournal}'s
 * notes are.
 */
async function buildDrainReport(store: DrainsStore, row: DrainRow): Promise<DrainReportPayload> {
  const ids = drainRunIds(row);
  const runs: ReportRunRow[] = [];
  for (let from = 0; from < ids.length; from += REPORT_PAGE) {
    const page = ids.slice(from, from + REPORT_PAGE);
    const holes = page.map(() => "?").join(", ");
    runs.push(
      ...(await store.db.query<ReportRunRow>(
        `SELECT id, kind, closure, cost_usd, tokens, started_at, finished_at, preparation,
                profile, payload
           FROM runs WHERE id IN (${holes})`,
        page,
      )),
    );
  }

  const ledger = accountName(row.profile);
  const duties = new Map<string, { runs: number; tokens: DrainTokens }>();
  const accounts = new Map<string, { runs: number; tokens: DrainTokens }>();
  let tokens = NO_TOKENS;
  let reachedModel = 0;
  let unsettled = 0;
  let shared = false;
  let prepareRuns = 0;
  let prepareWallMs = 0;
  let sessionRuns = 0;
  let sessionWallMs = 0;
  const sessions = new Map<string, ReportRunRow>();

  for (const run of runs) {
    const wall = spanMs(run.started_at ?? "", run.finished_at ?? "");
    // `runs.kind` is the OPERATION a job ran as, and those are namespaced on the machine half:
    // the bare word `prepare` matches nothing in this table.
    if (run.kind === MACHINE_OPERATIONS.prepare) {
      prepareRuns += 1;
      prepareWallMs += wall;
      continue;
    }
    sessions.set(run.id, run);
    sessionRuns += 1;
    sessionWallMs += wall;
    if (run.closure === null) {
      unsettled += 1;
      continue;
    }
    const metered = tokensOf(run);
    if (metered.metered) reachedModel += 1;
    tokens = addTokens(tokens, metered.tokens);
    const carried = recipesOf(run);
    if (carried.length > 1) shared = true;
    for (const recipe of carried) {
      const lane = duties.get(recipe) ?? { runs: 0, tokens: NO_TOKENS };
      duties.set(recipe, { runs: lane.runs + 1, tokens: addTokens(lane.tokens, metered.tokens) });
    }
    const account = accountOf(run) || ledger;
    const held = accounts.get(account) ?? { runs: 0, tokens: NO_TOKENS };
    accounts.set(account, { runs: held.runs + 1, tokens: addTokens(held.tokens, metered.tokens) });
  }

  const settledIds = [...sessions.keys()];
  const produced = await producedBy(store, settledIds);

  // A launch with no run row at all: the hub took the job and the row write did not land, which
  // is the one case a drain can neither fold nor settle. It is counted from the ids rather than
  // from the notes, because a note is only written when a tick happened to observe it.
  const withoutRunRow = Math.max(0, row.jobsLaunched - sessionRuns);

  const launchRefusals: Record<string, number> = {};
  for (const note of row.journal.notes) {
    if (note.kind === "admission") bumped(launchRefusals, launchCode(note.detail));
  }

  const wallMs = spanMs(row.startedAt, row.finishedAt === "" ? row.startedAt : row.finishedAt);
  const capacity = wallMs * Math.max(1, row.concurrent);
  let peakHeld = 0;
  let peakAtModel = 0;
  for (const entry of row.journal.samples) {
    peakHeld = Math.max(peakHeld, entry.held);
    peakAtModel = Math.max(peakAtModel, entry.atModel);
  }

  return {
    schema: DRAIN_REPORT_SCHEMA,
    provenance: DRAIN_REPORT_PROVENANCE,
    drainId: row.id,
    machineId: row.machineId,
    preset: row.preset,
    ending: row.state,
    reason: row.reason,
    startedBy: row.startedBy,
    startedAt: row.startedAt,
    finishedAt: row.finishedAt,
    wallMs,
    concurrent: row.concurrent,
    target: row.target,
    account: ledger,
    model: row.profile.model,
    thinking: row.profile.thinking,
    allocation: { named: [...row.knobs.recipes], ran: lanes(duties), shared },
    accounts: lanes(accounts),
    tokens,
    jobs: {
      launched: row.jobsLaunched,
      reachedModel,
      settled: row.jobsSettled,
      unsettled,
      withoutRunRow,
    },
    closures: { ...row.closures },
    refusals: { ...row.refusals },
    launchRefusals,
    produced: {
      records: produced.records,
      assessments: produced.assessments,
      recordsPerMillionTokens: perMillion(produced.records, tokens),
      assessmentsPerMillionTokens: perMillion(produced.assessments, tokens),
    },
    load: {
      heldMs: Math.round(row.journal.heldMs),
      atModelMs: Math.round(row.journal.atModelMs),
      atModelFraction: capacity === 0 ? 0 : row.journal.atModelMs / capacity,
      peakHeld,
      peakAtModel,
    },
    pipeline: { prepareRuns, prepareWallMs, sessionRuns, sessionWallMs },
    gaps: gapsOf([...sessions.values()], produced.byRun, withoutRunRow),
    notes: row.journal.notes.map((note) => ({
      at: new Date(note.at).toISOString(),
      kind: note.kind,
      detail: note.detail,
    })),
    notesDropped: row.journal.notesDropped,
    unobserved: [...UNOBSERVED],
  };
}

interface Produced {
  readonly records: number;
  readonly assessments: number;
  /** Which runs produced something, so a gap can name the ones that produced nothing. */
  readonly byRun: ReadonlySet<string>;
}

type ProducedRow = { run_id: string; records: number | bigint; judgements: number | bigint };

async function producedBy(store: DrainsStore, runIds: readonly string[]): Promise<Produced> {
  const byRun = new Set<string>();
  let records = 0;
  let assessments = 0;
  for (let from = 0; from < runIds.length; from += REPORT_PAGE) {
    const page = runIds.slice(from, from + REPORT_PAGE);
    const holes = page.map(() => "?").join(", ");
    const rows = await store.db.query<ProducedRow>(
      `SELECT run_id, SUM(records) AS records, SUM(judgements) AS judgements FROM (
         SELECT run_id, COUNT(*) AS records, 0 AS judgements FROM records
          WHERE run_id IN (${holes}) GROUP BY run_id
         UNION ALL
         SELECT run_id, 0 AS records, COUNT(*) AS judgements FROM assessments
          WHERE run_id IN (${holes}) GROUP BY run_id
       ) GROUP BY run_id`,
      [...page, ...page],
    );
    for (const row of rows) {
      records += count(row.records);
      assessments += count(row.judgements);
      byRun.add(row.run_id);
    }
  }
  return { records, assessments, byRun };
}

/**
 * WHY SOME OF THIS DRAIN'S WORK PRODUCED NOTHING, one reason per line with a count.
 *
 * It is the question "how much erroring" asked so that the answer is actionable: a refusal, a
 * failure, a job still running when the drain ended and a run that completed and wrote nothing
 * are four different things to do next, and a single "errors: 20" is none of them.
 */
function gapsOf(
  sessions: readonly ReportRunRow[],
  produced: ReadonlySet<string>,
  withoutRunRow: number,
): { reason: string; jobs: number; detail: string }[] {
  const counted = new Map<string, { jobs: number; detail: string }>();
  const add = (reason: string, detail: string): void => {
    const held = counted.get(reason);
    counted.set(reason, { jobs: (held?.jobs ?? 0) + 1, detail: held?.detail ?? detail });
  };
  if (withoutRunRow > 0) {
    counted.set("no-run-row", {
      jobs: withoutRunRow,
      detail: "the hub took the job and the row write that would have recorded it did not land",
    });
  }
  for (const run of sessions) {
    if (run.closure === null) {
      add(
        "unsettled",
        "still running when the drain ended, so its receipt is not in these figures",
      );
      continue;
    }
    const held = parsed(run.payload);
    const receipt =
      held === null || typeof held !== "object" ? {} : (held as Record<string, unknown>);
    const reason = typeof receipt["reason"] === "string" ? receipt["reason"] : "";
    const refusal = reason === "" ? null : refusalCode(reason);
    if (refusal !== null) {
      add(`refused:${refusal}`, reason);
      continue;
    }
    if (run.closure !== "completed") {
      add(`closed:${run.closure}`, reason === "" ? `the run closed as ${run.closure}` : reason);
      continue;
    }
    if (!produced.has(run.id)) {
      add("completed-empty", "the run completed and put no record and no assessment in the store");
    }
  }
  return [...counted.entries()]
    .map(([reason, gap]) => ({ reason, jobs: gap.jobs, detail: gap.detail }))
    .sort((left, right) => right.jobs - left.jobs || (left.reason < right.reason ? -1 : 1));
}

/** One line an operator can read in a listing: what this drain was, and what it came to. */
function reportTitle(report: DrainReportPayload): string {
  const dollars = (report.tokens.costMicros / 1_000_000).toFixed(4);
  const line =
    `drain ${report.drainId} on ${report.machineId} ended as ${report.ending}: ` +
    `${String(report.jobs.launched)} jobs, ${String(report.tokens.outputTokens)} output tokens, ` +
    `$${dollars}, ${String(report.produced.records)} records`;
  return line.length <= 200 ? line : `${line.slice(0, 199)}…`;
}

/**
 * THE REPORT, WRITTEN AS A FRONTIER RECORD. Idempotent on the derived identifier: a drain closed
 * twice — an operator's stop landing on a drain a tick had already ended — writes one record.
 *
 * `actor_kind` is `engine` because the controller wrote it: no model was asked and no person
 * typed it, and `run` would attribute a drain's whole account to whichever job happened to be
 * last. `run_id` is NULL for the same reason — this record belongs to the drain and not to any
 * one of its runs — and `payload.provenance` is the word that says which engine act it was.
 */
export async function writeDrainReport(
  store: DrainsStore,
  row: DrainRow,
): Promise<DrainReportPayload> {
  const report = await buildDrainReport(store, row);
  await store.db.run(
    `INSERT OR IGNORE INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id,
                                   recipe_id, recipe_version, actor_kind, actor_id, title,
                                   created_at, payload)
     VALUES (?, ?, ?, NULL, 0, NULL, NULL, NULL, NULL, 'engine', ?, ?, ?, ?)`,
    [
      drainReportId(row.id),
      DRAIN_REPORT_KIND,
      drainReportId(row.id),
      row.id,
      reportTitle(report),
      row.finishedAt === "" ? new Date(store.now()).toISOString() : row.finishedAt,
      JSON.stringify(report),
    ],
  );
  return report;
}

/** The report a drain left, or null: it has not ended, or its record predates this shape. */
export async function readDrainReport(
  store: DrainsStore,
  drainId: string,
): Promise<DrainReportPayload | null> {
  const rows = await store.db.query<{ payload: string }>(
    `SELECT payload FROM records WHERE id = ?`,
    [drainReportId(drainId)],
  );
  const row = rows[0];
  if (row === undefined) return null;
  const held = DrainReportSchema.safeParse(parsed(row.payload));
  return held.success ? held.data : null;
}
