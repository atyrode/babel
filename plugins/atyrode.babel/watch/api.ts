import type { HostServices } from "@manifold/plugin";
import { z } from "zod";
import {
  DRAIN_CONCURRENT_MAX,
  DrainStartRequestSchema,
  DrainStatusSchema,
  DrainStopInputSchema,
  MODEL_REFERENCE,
  OPERATIONS,
  PRESET_OPERATIONS,
  PolicyResultSchema,
  PresetSchema,
  RecipeRowSchema,
  RUN_STAGES,
  RunProgressSchema,
  RunRowSchema,
  RunsResultSchema,
  SessionChoiceSchema,
  StopInputSchema,
  THINKING_LEVELS,
  TopicRowSchema,
  TopicsResultSchema,
  door,
  type ActionName,
  type DrainPreset,
  type SessionChoice,
} from "../contract.ts";

/** The wire rows this panel renders; the contract spells the schemas and not the types. */
export type RunRow = z.infer<typeof RunRowSchema>;
export type RunsResult = z.infer<typeof RunsResultSchema>;
export type TopicRow = z.infer<typeof TopicRowSchema>;
export type TopicsResult = z.infer<typeof TopicsResultSchema>;
export type RecipeRow = z.infer<typeof RecipeRowSchema>;
export type PolicyResult = z.infer<typeof PolicyResultSchema>;
export type Preset = z.infer<typeof PresetSchema>;

/*
  WHAT WATCH ASKS THE HUB, AND IN WHAT WORDS.

  Every door this panel knocks on is the baseline's, by its contract name, and every answer is
  parsed before a pixel is painted: a panel that renders an unvalidated shape is a panel that
  paints a lie the first time the server half changes. Every shape on the wire is the contract's
  own — `contract.ts` spells them once for both halves and this file imports them, so a door's
  input is never described a second time in a component.

  Nothing in this file knows about React. The launch input, the ceilings reading, the clock and
  the preset table are the panel's whole vocabulary, and they are testable without a DOM.
*/

// ---------------------------------------------------------------------------- knocking on a door

export type DoorRead<T> = { readonly ok: true; readonly value: T } | { readonly ok: false; readonly message: string };

/**
 * Reads a door and parses its answer, raising the host's own sentence.
 *
 * A denial is data on the wire and an exception here on purpose: every reading in this panel is
 * a polled resource, and a polled resource reports a failed read through `onError` — so the
 * refusal reaches the operator as the note under the section rather than as a shape the view
 * has to re-check. Acts use {@link act}, which turns the same throw back into data.
 */
export async function read<T>(
  host: HostServices,
  action: ActionName,
  args: unknown,
  schema: z.ZodType<T>,
): Promise<T> {
  const outcome = await host.client.action(door(action), args);
  if (!outcome.ok) throw new Error(outcome.denial.message);
  const parsed = schema.safeParse(outcome.result);
  if (parsed.success) return parsed.data;
  const first = parsed.error.issues[0];
  const where = first === undefined ? "(root)" : first.path.map(String).join(".") || "(root)";
  throw new Error(`${action} answered a shape Watch does not know: ${where} ${first?.message ?? ""}`.trim());
}

/** The operator's own acts: a refusal is the sentence beside the button, never an exception. */
export async function act<T>(
  host: HostServices,
  action: ActionName,
  args: unknown,
  schema: z.ZodType<T>,
): Promise<DoorRead<T>> {
  try {
    return { ok: true, value: await read(host, action, args, schema) };
  } catch (error) {
    return { ok: false, message: error instanceof Error ? error.message : String(error) };
  }
}

// ---------------------------------------------------------------------------- the presets

/**
 * THE THREE REQUESTS A DRAIN MAY FAN OUT are {@link DRAIN_CARDS} below; there is no launch form
 * any more (#279), so there are no launch cards either. A run that reaches a model is a Code
 * session, composed in Code and posted through Code's own door, and Watch's Start section says
 * exactly that (`watch/start.tsx`).
 */

/**
 * WHO A DRAIN SPENDS, as its form holds it (#258, #267).
 *
 * Five strings rather than the contract's `SessionChoice`, because a form is a half-made choice
 * for as long as the operator is making it: a model typed but no account yet is a state
 * `SessionChoiceSchema` has no shape for. {@link sessionChoice} is the one place the two meet.
 *
 * It is NOT a picker over anything Babel read. The model, the thinking level and the account
 * belong to the Code profile a run is composed from (#279); these fields are what the drain
 * RECORDS about the window it is spending, so the panel can say afterwards which account a fan
 * burned — the one question nothing could answer on 2026-09-13.
 */
export interface SessionDraft {
  /** `provider/model`, the reference a composition routes by. */
  readonly model: string;
  /** One of `THINKING_LEVELS`, or "" for whatever the model does by default. */
  readonly thinking: string;
  readonly provider: string;
  readonly credentialId: string;
  /** The OAuth identity, or "" for an api-key credential, which has none. */
  readonly identityKey: string;
}

/** What {@link sessionChoice} needs of a form: the half-made session, and the machine. */
export interface SessionHolder {
  readonly session: SessionDraft;
  readonly machineId: string;
}

export const INITIAL_SESSION: SessionDraft = {
  model: "",
  thinking: "",
  provider: "",
  credentialId: "",
  identityKey: "",
};

/**
 * What `stop` takes for one row: the run, and the JOB NODE the engine holds `jobs:cancel` at.
 * The row already carries every part of it — the machine, the operation it ran as its `kind`,
 * and the job — so the panel posts the node rather than asking the door to look one up, because
 * the requirement is discharged against these arguments before the door is entered.
 */
export function stopInput(run: RunRow, reason = ""): z.infer<typeof StopInputSchema> {
  return StopInputSchema.parse({
    runId: run.id,
    job: { kind: "job", machineId: run.machineId, operationId: run.kind, jobId: run.jobId },
    reason,
  });
}

// ------------------------------------------------------ who a drain spends (#258, #267, #279)

/*
  THE FORM'S HALF OF THE SESSION.

  Babel does not choose a model, a thinking level or an account: a run is composed from a Code
  profile and posted through Code's own door (#279). What is left on this side is the DRAIN's
  record of which account's window it exists to spend, which the operator names when he starts
  it and the panel says back while it runs. There is no `accounts` door and no broker read any
  more — the accounts a machine holds are omp's, reached through Code — so the fields are typed
  and the scope is the one honest tag available: this panel, on this machine.
*/

/** What the thinking picker offers; the empty one is the level nobody chose (`THINKING_LEVELS`). */
export const THINKING_CHOICES: readonly { readonly value: string; readonly label: string }[] = [
  { value: "", label: "none — the model's own default" },
  ...THINKING_LEVELS.map((level) => ({ value: level, label: level })),
];

/** A session the door would take, or the named reason it is not one yet. */
export type SessionPick =
  | { readonly ok: true; readonly session: SessionChoice }
  | { readonly ok: false; readonly reason: string };

function incomplete(clause: string): SessionPick {
  return { ok: false, reason: `session_incomplete: ${clause}` };
}

/**
 * THE FORM'S STATE AS THE CONTRACT'S SESSION, or why it is not one.
 *
 * The reason is what disables the button, so it is a clause an operator can act on and it is
 * NAMED: `session_incomplete` is a half-made choice, and the drain's own refusal for anything
 * the contract will not parse.
 *
 * The SCOPE is a tag naming where the account came from rather than a broker observation,
 * because no observation happened: an account named here was named by the operator. A pool
 * Babel records holds exactly one slot, so the tag is both admissible and honest.
 */
export function sessionChoice(draft: SessionHolder): SessionPick {
  const session = draft.session;
  const model = session.model.trim();
  if (model === "") return incomplete("name the model this run asks for, as provider/model");
  if (!MODEL_REFERENCE.test(model)) {
    return incomplete(
      `${model} is not a model reference — a run is routed by provider/model, and a bare model ` +
        `id misses the route and the price at once`,
    );
  }
  if (session.provider === "" || session.credentialId === "") {
    return incomplete("name the account this drain spends");
  }
  const parsed = SessionChoiceSchema.safeParse({
    model,
    ...(session.thinking === "" ? {} : { thinking: session.thinking }),
    account: {
      provider: session.provider,
      scope: `atyrode.babel.watch/typed/${draft.machineId}`,
      credentialId: session.credentialId,
      identityKey: session.identityKey,
    },
  });
  if (parsed.success) return { ok: true, session: parsed.data };
  const issue = parsed.error.issues[0];
  return incomplete(
    issue === undefined
      ? "this is not a session the contract accepts"
      : `${issue.path.join(".")} ${issue.message.toLowerCase()}`,
  );
}

// ---------------------------------------------------------------------------- figures and clocks

export const FRESHNESS_NOTE: Record<string, string> = {
  fresh: "Heard from seconds ago.",
  recent: "Heard from a minute or two ago.",
  lost: "Nothing heard for a long time. That is not the same as dead.",
  ended: "Finished: the receipt is written.",
};

/**
 * The kinds a run row carries, in the two words a receipt records. A row's `kind` is the
 * OPERATION ID the job ran as, and those are namespaced on the machine half, so the table is
 * keyed off the contract's own names rather than the short words it used to carry.
 */
export const RUN_KIND_LABELS: Record<string, string> = {
  [OPERATIONS.explore]: "Exploration",
  [OPERATIONS.evaluate]: "Review",
  [OPERATIONS.prepare]: "Preparation",
  [OPERATIONS.scan]: "Scan",
  [OPERATIONS.archive]: "Archive",
  conductor: "Loop",
};

/**
 * A ticking wall clock: whole seconds always, so the figure visibly advances between reads, and
 * never the sub-second precision a recorded duration keeps — a card that opened on "0ms
 * elapsed" was reporting a measurement at a precision nobody watches.
 */
export function elapsedClock(seconds: number): string {
  const whole = Math.max(0, Math.round(seconds));
  if (whole < 60) return `${whole}s`;
  const minutes = Math.floor(whole / 60);
  if (minutes < 60) return `${minutes}m ${String(whole % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

/**
 * How old the newest thing the hub heard from a run is, as the one clause a row says about it.
 *
 * `lastWord` on the wire is an instant, not a sentence (agreed with StoreDoors): the age is
 * computed here against the panel's own clock, which is what lets it tick between polls. A run
 * that has announced nothing has said nothing, which is a third state and not a stale one.
 */
export function ageClause(instant: string, now: number): string {
  const at = Date.parse(instant);
  if (!Number.isFinite(at)) return "no word yet";
  const seconds = (now - at) / 1000;
  if (seconds < 1) return "last word just now";
  return `last word ${elapsedClock(seconds)} ago`;
}

/** Seconds a run has been going, from its start instant against the panel's clock. */
export function elapsedSince(instant: string, now: number): number | null {
  const at = Date.parse(instant);
  if (!Number.isFinite(at)) return null;
  return (now - at) / 1000;
}

/** A SPEND or a CEILING: two places, because that is how a receipt and a limit are read. */
export function usd(amount: number): string {
  return `$${amount.toFixed(2)}`;
}

/**
 * A RATE — what a thousand tokens costs — which is a different figure from a spend and needs a
 * different precision: $0.015 in two places is "$0.02", which overstates it by a third, and the
 * price per 1k is exactly the number the operator said the interface never told him (#251).
 */
export function usdRate(amount: number): string {
  return `$${amount.toFixed(3)}`;
}

/** A count, grouped, so a column of them compares down the page. */
export function figure(value: number): string {
  return value.toLocaleString("en-US");
}

/** An instant as "3m ago" / "2d ago", for a last-ran column. */
export function since(instant: string, now: number): string {
  const at = Date.parse(instant);
  if (!Number.isFinite(at)) return "never";
  const seconds = Math.max(0, (now - at) / 1000);
  if (seconds < 90) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

/**
 * HOW MUCH OF AN OVERLAY IS LEFT (#260), as the clause beside it: "for 42m", and "expired" for
 * one the panel is still holding when its instant has passed.
 *
 * A TTL is the whole difference between a drain and an edit of the standing policy, so the
 * figure that matters is the remaining time and not the expiry instant — the operator who read
 * "batch 256" on 2026-09-13 had no way to see that it outlived the drain by ninety minutes.
 */
export function until(instant: string, now: number): string {
  const at = Date.parse(instant);
  if (!Number.isFinite(at)) return "for an unreadable while";
  const seconds = (at - now) / 1000;
  if (seconds <= 0) return "expired";
  if (seconds < 90) return `for ${String(Math.max(1, Math.round(seconds)))}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `for ${String(minutes)}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `for ${String(hours)}h`;
  return `for ${String(Math.floor(hours / 24))}d`;
}

/** The overlay's fields in the operator's words, and how each is written. There is no batch
 *  row: the bound per machine is the one number an overlay moves admission with. */
export const OVERLAY_FIELDS: Record<string, { readonly label: string; readonly money: boolean }> = {
  perCycleCost: { label: "Per run", money: true },
  dailyCost: { label: "Per day", money: true },
  concurrentPerMachine: { label: "At once, per machine", money: false },
};

// ------------------------------------------------------------------ where a run is (#261)

/** What the conductor folded out of a running job's replay ring, as the row carries it. */
export type RunProgress = z.infer<typeof RunProgressSchema>;

/**
 * What a stage means, for the row's own title. The three words are the machine half's
 * (`RUN_STAGES`); anything else came from a build this panel does not know and is shown as it
 * was reported rather than translated into a guess.
 */
export const STAGE_NOTE: Record<string, string> = {
  [RUN_STAGES.preparing]: "Reading its input. Nothing has been asked of a model yet.",
  [RUN_STAGES.atModel]: "The prompt is written and the engine is at the model.",
  [RUN_STAGES.submitting]: "Writing what it produced into the job's output.",
};

/** What `stalled` means, spelled where it is shown: a silence, never a death. */
export const STALLED_NOTE =
  "At the model, and nothing metered for over 90 seconds. That is a silence, not a death.";

/**
 * The tokens a running row shows: input, output and cache, in that order and grouped.
 *
 * Three figures rather than a total, because they are three different costs — output is the
 * expensive one, cache the cheap one — and a drain that reads only a sum cannot tell a run that
 * is thinking from one that is re-reading its own context (#261, post-mortem O1).
 */
export function tokenClause(progress: RunProgress): string {
  return `${figure(progress.inputTokens)} / ${figure(progress.outputTokens)} / ${figure(progress.cacheTokens)}`;
}

// ------------------------------------------------------------------ draining a window (#258)

/** One drain as the door answers for it; the contract spells the schema and not the type. */
export type DrainStatus = z.infer<typeof DrainStatusSchema>;

/**
 * What each drain preset is, in the operator's words, and what its one knob is: the days a
 * `read-whats-new` reads back over, the topic an `explore-topic` runs on, the minutes a
 * `keep-going` beat is given. It also says what a drain of it SPENDS, which is what the form
 * refuses a token target on.
 */
export type Knob = "days" | "minutes" | "topic";

export interface DrainCard {
  readonly title: string;
  readonly does: string;
  readonly knob: Knob;
  /** Whether jobs of this preset reach a model at all; `keep-going` does not. */
  readonly spends: boolean;
}

export const DRAIN_CARDS: Record<DrainPreset, DrainCard> = {
  "read-whats-new": {
    title: "Read what's new",
    does: "One exploration per job over the sessions this machine has catalogued lately.",
    knob: "days",
    spends: true,
  },
  "explore-topic": {
    title: "Explore a topic",
    does: "One exploration per job over the sessions this topic's own records cite.",
    knob: "topic",
    spends: true,
  },
  "keep-going": {
    title: "Keep going",
    does: "One scan per job. It reaches no model, so it spends nothing: this is the rehearsal.",
    knob: "minutes",
    spends: false,
  },
};

/** The bounds of the two knobs a drain owns, read off the contract's own schema. */
export const DRAIN_BOUNDS = {
  concurrent: { min: 1, max: DRAIN_CONCURRENT_MAX, step: 1 },
  minutesToDeadline: { min: 5, max: 24 * 60, step: 5 },
} as const;

export interface DrainDraft extends SessionHolder {
  readonly preset: DrainPreset;
  readonly entityId: string;
  readonly sinceDays: number;
  readonly minutes: number;
  readonly concurrent: number;
  /**
   * THE DEADLINE AS MINUTES FROM NOW, which is the figure an operator has: "the window resets at
   * 13:00 and it is 10:40, so ninety minutes". The instant is computed when the button is
   * pressed, so a form left open for ten minutes does not post a deadline ten minutes in the
   * past.
   */
  readonly minutesToDeadline: number;
  /** The cost target in whole dollars; 0 means "no cost target", which the deadline then bounds. */
  readonly targetUsd: number;
  readonly reason: string;
}

export const INITIAL_DRAIN: DrainDraft = {
  preset: "read-whats-new",
  machineId: "",
  entityId: "",
  sinceDays: 1,
  minutes: 60,
  concurrent: 2,
  minutesToDeadline: 120,
  targetUsd: 0,
  session: INITIAL_SESSION,
  reason: "",
};

/**
 * The draft as `drain.start` takes it, plus the OPERATION NODE the door is authorized at — the
 * same reason `launchRequest` carries one, and the same table it reads the operation from.
 *
 * The SESSION is passed in rather than read off the draft, exactly as `launchRequest` takes the
 * one the picker made: the scope belongs to the broker observation the account was seen in, so
 * only {@link sessionChoice} — which holds the offered rows — can state it.
 *
 * The deadline is turned into an instant HERE, at the moment of the press, from the minutes the
 * operator set. `targetUsd` becomes `costMicros` because the meter's unit is micro-dollars and
 * rounding a target to cents would make "stop at five dollars" stop at $4.99 or $5.01 depending
 * on the direction nobody chose.
 */
export function drainStartRequest(
  draft: DrainDraft,
  session: SessionChoice,
  now: number,
): z.infer<typeof DrainStartRequestSchema> {
  const card = DRAIN_CARDS[draft.preset];
  return DrainStartRequestSchema.parse({
    machineId: draft.machineId,
    preset: draft.preset,
    concurrent: draft.concurrent,
    reason: draft.reason,
    session,
    target: {
      deadline: new Date(now + draft.minutesToDeadline * 60_000).toISOString(),
      ...(draft.targetUsd > 0 ? { costMicros: Math.round(draft.targetUsd * 1_000_000) } : {}),
    },
    recipes: [],
    ...(card.knob === "topic" && draft.entityId !== "" ? { entityId: draft.entityId } : {}),
    ...(card.knob === "days" ? { sinceDays: draft.sinceDays } : {}),
    ...(card.knob === "minutes" ? { minutes: draft.minutes } : {}),
    operation: {
      kind: "operation",
      machineId: draft.machineId,
      operationId: PRESET_OPERATIONS[draft.preset],
    },
  });
}

/** What `drain.stop` takes: the drain, and the OPERATION NODE its jobs are cancelled at. */
export function drainStopInput(
  drain: DrainStatus,
  reason = "",
): z.infer<typeof DrainStopInputSchema> {
  return DrainStopInputSchema.parse({
    drainId: drain.drainId,
    operation: {
      kind: "operation",
      machineId: drain.machineId,
      operationId: PRESET_OPERATIONS[drain.preset],
    },
    reason,
  });
}

/**
 * Why a drain cannot be started yet, in one clause, or empty when it can — in the order the form
 * reads, so the sentence beside the button is about the field the operator is looking at.
 *
 * THE ACCOUNT IS REFUSED HERE IN THE SAME WORDS A LAUNCH WOULD USE, because a drain NAMES the
 * account it spends (#267): on 2026-09-13 nothing on the machine could say which account a
 * running fan was burning, and "start it and find out" is the failure this door exists to
 * remove. The clause is {@link sessionChoice}'s own — one picker, one refusal — and the drain
 * adds only what a launch has no equivalent of: the reason the overlay records.
 */
export function drainUnready(draft: DrainDraft, session: SessionPick): string {
  if (draft.machineId === "") return "Pick a machine to drain on.";
  if (DRAIN_CARDS[draft.preset].knob === "topic" && draft.entityId === "") {
    return "Pick a topic to explore.";
  }
  if (!session.ok) return session.reason;
  if (draft.reason.trim() === "") return "Say why: the reason is recorded on the overlay.";
  return "";
}

/** A micro-dollar figure as money. Four places, because a drain's target is set in cents. */
export function micros(value: number): string {
  return `$${(value / 1_000_000).toFixed(4)}`;
}

/**
 * A RATE PER MINUTE, and zero written as zero.
 *
 * "0/min" is the reading the go/no-go rule is about — tokens per minute flat for three minutes
 * while jobs say `at the model` is a stop — so it is shown as a number rather than as a dash,
 * which is what an unmeasured figure looks like. The two are different facts.
 */
export function perMinute(value: number): string {
  return `${figure(Math.round(value))}/min`;
}

/**
 * WHEN THIS DRAIN REACHES ITS TARGET, against the deadline it has to beat.
 *
 * The comparison is the whole point and it is made here rather than left to the reader: an ETA
 * after the deadline means the target will NOT be met, which is the operator's cue to raise the
 * fan or accept the shortfall, and it was the number nobody had on 2026-09-13.
 */
export function etaClause(drain: DrainStatus, now: number): string {
  if (drain.state !== "running") return "—";
  const deadline = drain.target.deadline === undefined ? null : Date.parse(drain.target.deadline);
  const left = deadline === null || !Number.isFinite(deadline) ? "" : ` · deadline ${until(drain.target.deadline ?? "", now)}`;
  if (drain.etaAt === "") {
    const spends = drain.target.costMicros !== undefined || drain.target.outputTokens !== undefined;
    return `${spends ? "no rate yet" : "no spend target"}${left}`;
  }
  const eta = Date.parse(drain.etaAt);
  if (!Number.isFinite(eta)) return `—${left}`;
  const clause = `target ${until(drain.etaAt, now)}`;
  if (deadline === null || !Number.isFinite(deadline)) return clause;
  return `${clause}${eta > deadline ? " — after the deadline" : ""}${left}`;
}

/** What a drain's state means, spelled where it is shown. */
export const DRAIN_STATE_NOTE: Record<string, string> = {
  running: "Launching jobs and folding what they spend.",
  closing: "It has stopped launching; its last jobs' receipts are still owed to its total.",
  target: "It stopped itself: the target was met.",
  deadline: "It stopped itself: the deadline passed.",
  stopped: "An operator stopped it.",
  failed: "It could not launch anything and stopped rather than pretending to run.",
};
