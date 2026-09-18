import type { HostServices } from "@manifold/plugin";
import { formatManifoldUri } from "@manifold/protocol";
import { GENERATOR_PLUGIN_ID, LAUNCHER_PANEL } from "@atyrode/manifold-code";
import type { z } from "zod";
import type {
  CycleReportSchema,
  DrainStatusSchema,
  PolicyResultSchema,
  PresetSchema,
  ProfileRowSchema,
  ProfilesResultSchema,
  PulseResultSchema,
  RecipeRowSchema,
  RunProgressSchema,
  RunRowSchema,
  RunsResultSchema,
  TopicRowSchema,
  TopicsResultSchema,
} from "../contract.ts";
import {
  DRAIN_CONCURRENT_MAX,
  DrainStartRequestSchema,
  DrainStopInputSchema,
  OPERATIONS,
  PRESETS,
  PRESET_OPERATIONS,
  PRESET_START,
  RUN_STAGES,
  StopInputSchema,
  asLaunchRequest,
  door,
  type ActionName,
  type DrainPreset,
  type GapReason,
  type LaunchRequest,
  type ProfileAccount,
  type StopReason,
} from "../contract.ts";

/** One of the five requests, as the contract spells them. */
export type Preset = z.infer<typeof PresetSchema>;

/** The wire rows this panel renders; the contract spells the schemas and not the types. */
export type RunRow = z.infer<typeof RunRowSchema>;
export type RunsResult = z.infer<typeof RunsResultSchema>;
export type TopicRow = z.infer<typeof TopicRowSchema>;
export type TopicsResult = z.infer<typeof TopicsResultSchema>;
export type RecipeRow = z.infer<typeof RecipeRowSchema>;
export type PolicyResult = z.infer<typeof PolicyResultSchema>;
export type ProfileRow = z.infer<typeof ProfileRowSchema>;
export type ProfilesResult = z.infer<typeof ProfilesResultSchema>;

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

export type DoorRead<T> =
  { readonly ok: true; readonly value: T } | { readonly ok: false; readonly message: string };

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
  throw new Error(
    `${action} answered a shape Watch does not know: ${where} ${first?.message ?? ""}`.trim(),
  );
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
 * THE KNOB A PRESET OWNS. One apiece, because a form offering every dial is the CLI with
 * labels on it — the flags-with-labels form this panel replaces, where five dials were offered
 * to an operator who had to know which three the chosen kind would refuse.
 */
export type Knob = "days" | "minutes" | "topic";

export interface PresetCard {
  readonly title: string;
  /** One line: what asking for this actually does. */
  readonly does: string;
  readonly knob: Knob;
  readonly knobLabel: string;
  /** Whether the cookbook selection applies; an empty selection runs the enabled default set. */
  readonly takesRecipes: boolean;
}

/**
 * The three direct requests this panel posts. `review-backlog` and `file-and-tidy` remain
 * policy-managed draws: the conductor selects them under one shared budget and dispatches them
 * with a blinded projection, so this form does not offer an on-demand bypass around that lane.
 */
export const LAUNCH_PRESETS: readonly Preset[] = PRESETS.filter(
  (preset) => PRESET_START[preset] !== "draw",
);

export const LAUNCH_CARDS: Record<string, PresetCard> = {
  "read-whats-new": {
    title: "Read what's new",
    does: "Reads the sessions since you last looked and writes up what it found.",
    knob: "days",
    knobLabel: "Days back",
    takesRecipes: true,
  },
  "explore-topic": {
    title: "Explore a topic",
    does: "Runs the cookbook over one topic's own sessions, and files what it writes under it.",
    knob: "topic",
    knobLabel: "Topic",
    takesRecipes: true,
  },
  "keep-going": {
    title: "Keep going",
    does: "Lets Babel run its own loop under the ceilings until the time is up. It reaches no model.",
    knob: "minutes",
    knobLabel: "Minutes",
    takesRecipes: false,
  },
};

/** The knob's bounds, read off the contract's own schema so a spinner cannot offer a refusal. */
export const KNOB_BOUNDS: Record<
  Knob,
  { readonly min: number; readonly max: number; readonly step: number }
> = {
  days: { min: 1, max: 365, step: 1 },
  minutes: { min: 5, max: 24 * 60, step: 5 },
  topic: { min: 0, max: 0, step: 0 },
};

/**
 * WHAT THE START FORM HOLDS (#279).
 *
 * There is no model, no thinking level and no account among these fields, and their absence is
 * the whole shape of the change: those three belong to a CODE PROFILE, which is a configured
 * Code workspace. The operator picks a saved one — `containerId` — or opens Code's generator
 * and parametrizes one there; Babel carries the container and the revision it was shown and
 * chooses none of the three.
 */
export interface LaunchDraft {
  readonly preset: Preset;
  readonly machineId: string;
  /** The Code workspace whose profile answers this run; empty until one is picked. */
  readonly containerId: string;
  /** The topic for `explore-topic`; empty until one is picked. */
  readonly entityId: string;
  readonly sinceDays: number;
  readonly minutes: number;
  readonly recipes: readonly string[];
}

export const INITIAL_LAUNCH: LaunchDraft = {
  preset: "read-whats-new",
  machineId: "",
  containerId: "",
  entityId: "",
  sinceDays: 1,
  minutes: 60,
  recipes: [],
};

/**
 * The profile the draft names, out of what the `profiles` door answered — or null, which is
 * every state before one is chosen and the state after Code's list moved under a choice that
 * is no longer in it. The second one matters: pressing with a stale container is what
 * `code_stale_preferences` refuses, and the panel would rather say it before the press.
 */
export function chosenProfile(
  draft: { readonly containerId: string },
  profiles: readonly ProfileRow[],
): ProfileRow | null {
  return profiles.find((profile) => profile.containerId === draft.containerId) ?? null;
}

/**
 * THE PANEL CODE PARAMETRIZES A RUN IN, named from Code's own published constants rather than
 * spelled here: a string of ours would be the copy nobody checked the day Code moved it.
 */
export const GENERATOR_PANEL = `${GENERATOR_PLUGIN_ID}.${LAUNCHER_PANEL}`;

/**
 * WHERE THAT PANEL IS: the workspace itself, which is what a Code profile IS.
 *
 * The generator is seated in the container, so the address that reaches it is the container's
 * own — a plugin may only `openPanel` its OWN panels, and arranging another plugin's presence
 * is the principal's through the shell. The RETURN PATH is not a callback either: the operator
 * changes the model, the thinking level or the account in Code and comes back, and this panel
 * re-reads `profiles` — the revision it then shows is the one the launch carries.
 */
export function generatorUri(containerId: string): string {
  return formatManifoldUri({ kind: "container", containerId });
}

/**
 * The draft as the `launch` door takes it: exactly the knobs the chosen preset owns, assembled
 * into the request by the contract's own {@link asLaunchRequest} — so this form and the topic
 * page's coverage cell post one document and not two.
 *
 * A preset carries no flag it cannot use — `LaunchInputSchema` is strict, so `minutes` on a
 * `read-whats-new` would be refused whole — and the PROFILE travels only for a preset that
 * reaches a model: `keep-going` is a scan of Babel's own, and a profile on it would be a field
 * nothing reads.
 */
export function launchRequest(draft: LaunchDraft, profile: ProfileRow | null): LaunchRequest {
  const card = LAUNCH_CARDS[draft.preset];
  const reaches = PRESET_START[draft.preset] === "explore";
  return asLaunchRequest({
    machineId: draft.machineId,
    preset: draft.preset,
    recipes: card?.takesRecipes === true ? [...draft.recipes] : [],
    ...(card?.knob === "topic" && draft.entityId !== "" ? { entityId: draft.entityId } : {}),
    ...(card?.knob === "days" ? { sinceDays: draft.sinceDays } : {}),
    ...(card?.knob === "minutes" ? { minutes: draft.minutes } : {}),
    ...(reaches && profile !== null
      ? { profile: { containerId: profile.containerId, expectedRevision: profile.revision } }
      : {}),
  });
}

/**
 * Why a launch cannot be started yet, in one clause and in the order an operator would fix it,
 * or empty when it can. The profile clause is the panel's half of `startExplore`'s own
 * `profile_required`: a launch posted without one is refused by name, and the operator would
 * read that refusal after the press rather than before it.
 */
export function launchUnready(draft: LaunchDraft, profile: ProfileRow | null): string {
  if (draft.machineId === "") return "Pick a machine to run on.";
  if (LAUNCH_CARDS[draft.preset]?.knob === "topic" && draft.entityId === "") {
    return "Pick a topic to explore.";
  }
  if (PRESET_START[draft.preset] !== "explore") return "";
  if (profile === null) {
    return draft.containerId === ""
      ? "Pick the Code profile this run is posted on — the model, the thinking level and the account are its."
      : `Code no longer lists ${draft.containerId}; pick one of the profiles above.`;
  }
  return "";
}

/**
 * WHAT A PROFILE'S ACCOUNTS READ AS (#267, #279).
 *
 * Three states and three sentences, because an operator acts differently on each: Code named
 * them, Code says there are none, or Code was never asked because the build it is running
 * does not report them. A blank would collapse the three into the one the 2026-09-13 drain
 * could not answer — "which account is this burning".
 */
export function accountsClause(profile: {
  readonly accounts: readonly ProfileAccount[];
  readonly resolved: boolean;
}): string {
  if (profile.accounts.length > 0) {
    const named = profile.accounts.map(
      (account) => account.label || account.identityKey || account.provider,
    );
    return `Code reports ${named.join(", ")}`;
  }
  return profile.resolved
    ? "Code reports no account for this profile"
    : "Code did not report an account — open it in the generator";
}

/**
 * What `stop` takes for one row: the run, and the JOB NODE the engine holds `jobs:cancel` at.
 * The row already carries every part of it — the machine, the operation it ran as its `kind`,
 * and the job — so the panel posts the node rather than asking the door to look one up, because
 * the requirement is discharged against these arguments before the door is entered.
 *
 * A RUN STILL PREPARING NAMES ITS PREPARATION (#592). A run is started in two wakes and the
 * first posts only `atyrode.babel.prepare`, so until Code's session id lands there is exactly
 * one job to stop and it is that one — at its own operation, because a node is an operation on
 * a machine and the explore node has no job behind it yet.
 */
export function stopInput(run: RunRow, reason = ""): z.infer<typeof StopInputSchema> {
  const preparing = run.jobId === "" && run.prepareJobId !== "";
  return StopInputSchema.parse({
    runId: run.id,
    job: {
      kind: "job",
      machineId: run.machineId,
      operationId: preparing ? OPERATIONS.prepare : run.kind,
      jobId: preparing ? run.prepareJobId : run.jobId,
    },
    reason,
  });
}

// ------------------------------------------------------ who a drain spends (#258, #267, #279)

/*
  A DRAIN NAMES A CODE PROFILE, AND NOTHING ELSE ABOUT WHAT IT SPENDS.

  What stood here was five typed fields — a provider, a credential id, an identity key, a
  model and a thinking level — and a `sessionChoice` that assembled them into the contract's
  `SessionChoice`. All five were Babel deciding what a run is, which is not Babel's (#279).
  The drain picks a profile from the same `profiles` door Start reads, and what it RECORDS
  about the model and the account is Code's own report, copied once at the press and labelled
  as Code's ({@link accountsClause}, `DrainProfileSchema`).
*/

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
 * refuses a token target on. The {@link Knob} vocabulary is the launch form's own: the two
 * forms offer the same three requests, so two words for one dial would be two answers to it.
 */

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

export interface DrainDraft {
  readonly machineId: string;
  /** The Code workspace this fan is posted on; empty until one is picked. */
  readonly containerId: string;
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
  containerId: "",
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
  profile: ProfileRow,
  now: number,
): z.infer<typeof DrainStartRequestSchema> {
  const card = DRAIN_CARDS[draft.preset];
  return DrainStartRequestSchema.parse({
    machineId: draft.machineId,
    preset: draft.preset,
    concurrent: draft.concurrent,
    reason: draft.reason,
    profile: { containerId: profile.containerId, expectedRevision: profile.revision },
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
export function drainUnready(draft: DrainDraft, profile: ProfileRow | null): string {
  if (draft.machineId === "") return "Pick a machine to drain on.";
  if (DRAIN_CARDS[draft.preset].knob === "topic" && draft.entityId === "") {
    return "Pick a topic to explore.";
  }
  if (profile === null) {
    return draft.containerId === ""
      ? "Pick the Code profile this drain spends — the model and the account are its."
      : `Code no longer lists ${draft.containerId}; pick one of the profiles above.`;
  }
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
  const left =
    deadline === null || !Number.isFinite(deadline)
      ? ""
      : ` · deadline ${until(drain.target.deadline ?? "", now)}`;
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

// ------------------------------------------------------ why a cycle did nothing (#328)

/** The pulse door's whole answer; this panel reads the last cycle out of it. */
export type PulseResult = z.infer<typeof PulseResultSchema>;

/** The last cycle's own verdict, as the pulse door answers it. */
export type CycleReport = z.infer<typeof CycleReportSchema>;

/**
 * THE STOP THAT IS THE LOOP WORKING. A cycle that dispatched everything one batch allows has
 * stopped for the best possible reason, and a panel that announced it would be a notice on
 * every healthy cycle — which is how a section meant to explain silence becomes the noise it
 * was built to replace. It is the one word this panel says nothing about.
 *
 * It is `batch-filled` and not `batch`, because those are opposite states of health: the
 * filled batch is this loop's own dispatches, and a bare `batch` is every slot held by claims
 * that are not finishing. Staying silent for both is how a wedged deployment looked like a busy
 * one (#382).
 */
export const HEALTHY_STOP: StopReason = "batch-filled";

/*
  WHY DRAWING STOPPED, AND WHY A CANDIDATE WAS DECLINED, in the operator's words.

  Both tables are keyed by the contract's own vocabularies rather than by `string`, so a word
  added to `STOP_REASONS` or `GAP_REASONS` and not spelled here is a type error at the build
  rather than a blank line on the panel. That is the whole reason the two lists moved into
  `contract.ts`: the loop's reasons are a vocabulary two halves share, not prose a panel
  matches on.

  A STOP IS A SENTENCE AND A GAP IS A CLAUSE, because of where each is read: the stop is the
  answer to "why did nothing run" and stands alone; a gap is one row of a counted list and is
  read after its own figure. The coordinator's own detail is shown beside each, with the
  numbers and the names these cannot carry.
*/

export const STOP_NOTE: Record<StopReason, string> = {
  "invalid-policy": "The policy in force is not usable, so nothing may be drawn against it.",
  disabled: "Babel is switched off: the policy in force does not authorize evaluation.",
  batch: "Every review slot is already claimed and none of those reviews has finished.",
  "batch-filled": "It dispatched everything one cycle is allowed.",
  "per-cycle": "The cycle's own spend ceiling is reached.",
  daily: "The day's spend ceiling is reached.",
  "no-candidates": "Nothing is eligible for review.",
  "no-lane": "No lane could be satisfied from what is eligible.",
  unrouted: "The policy names no Code profile to run a review on.",
  "dispatch-refused": "A review was drawn and the dispatch was refused.",
};

export const GAP_NOTE: Record<GapReason, string> = {
  excluded: "filed under a topic you excluded",
  "topic-retired": "filed under a topic that is retired",
  "record-replaced": "the record itself is superseded or retired",
  capped: "already reviewed as many times as the policy allows",
  claimed: "another worker holds the claim",
  exhausted: "skipped or failed too often to keep drawing",
  cooling: "reviewed too recently to review again",
  settled: "a pass already ran and its proposal is waiting on a ruling",
  empty: "the lane had nothing to draw",
  unsupported: "not the kind of record that lane works",
};
