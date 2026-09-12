import type { HostServices } from "@manifold/plugin";
import { z } from "zod";
import {
  LaunchInputSchema,
  LaunchResultSchema,
  PRESETS,
  PolicyResultSchema,
  PresetSchema,
  RecipeRowSchema,
  RunRowSchema,
  RunsResultSchema,
  TopicRowSchema,
  TopicsResultSchema,
  door,
  type ActionName,
  type LaunchInput,
} from "../contract.ts";

/** The wire rows this panel renders; the contract spells the schemas and not the types. */
export type RunRow = z.infer<typeof RunRowSchema>;
export type RunsResult = z.infer<typeof RunsResultSchema>;
export type TopicRow = z.infer<typeof TopicRowSchema>;
export type TopicsResult = z.infer<typeof TopicsResultSchema>;
export type RecipeRow = z.infer<typeof RecipeRowSchema>;
export type PolicyResult = z.infer<typeof PolicyResultSchema>;
export type Preset = z.infer<typeof PresetSchema>;
/** What the `launch` door answers, dry or wet: the profile, the model, the cost, the ceiling. */
export type LaunchAnswer = z.infer<typeof LaunchResultSchema>;

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
 * One knob, named in the operator's words rather than the flag's.
 *
 * A preset takes exactly one number or one topic — that is what makes it a preset instead of
 * the flags-with-labels form this panel replaces, where five dials of the CLI were offered to
 * an operator who had to know which three of them the chosen kind would refuse.
 */
export type Knob = "days" | "draws" | "minutes" | "topic";

export interface PresetCard {
  /** What the card is called. */
  readonly title: string;
  /** One line: what asking for this actually does. */
  readonly does: string;
  readonly knob: Knob;
  readonly knobLabel: string;
  /** Whether the cookbook selection applies; an empty selection runs the enabled default set. */
  readonly takesRecipes: boolean;
}

/**
 * THE FIVE REQUESTS, in the order an operator reaches for them: the daily read, one topic on
 * purpose, the backlog, Babel's own housekeeping, and letting it run.
 *
 * The wording is the whole point of this panel. "explore --preparation p_3f2a --recipe
 * code-health-comprehensibility --develop 3" is a sentence about Babel's internals; "read
 * what's new · the last 1 day of sessions" is a sentence about the operator's intent, and the
 * receipt records the same run either way.
 */
export const PRESET_CARDS: Record<Preset, PresetCard> = {
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
  "review-backlog": {
    title: "Review the backlog",
    does: "Draws candidates nobody came back to and has Babel's reviewers vote on them.",
    knob: "draws",
    knobLabel: "Draws",
    takesRecipes: false,
  },
  "file-and-tidy": {
    title: "File and tidy",
    does: "Files what is unfiled and proposes the consolidations the backlog has earned.",
    knob: "draws",
    knobLabel: "Draws",
    takesRecipes: false,
  },
  "keep-going": {
    title: "Keep going",
    does: "Lets Babel run its own loop under the ceilings until the time is up.",
    knob: "minutes",
    knobLabel: "Minutes",
    takesRecipes: false,
  },
};

/** The knob's bounds, read off the contract's own schema so a spinner cannot offer a refusal. */
export const KNOB_BOUNDS: Record<Knob, { readonly min: number; readonly max: number; readonly step: number }> = {
  days: { min: 1, max: 365, step: 1 },
  draws: { min: 1, max: 50, step: 1 },
  minutes: { min: 5, max: 24 * 60, step: 5 },
  topic: { min: 0, max: 0, step: 0 },
};

export interface LaunchDraft {
  readonly preset: Preset;
  readonly machineId: string;
  /** The topic for `explore-topic`; empty until one is picked. */
  readonly entityId: string;
  readonly sinceDays: number;
  readonly draws: number;
  readonly minutes: number;
  readonly recipes: readonly string[];
}

export const INITIAL_DRAFT: LaunchDraft = {
  preset: PRESETS[0],
  machineId: "",
  entityId: "",
  sinceDays: 1,
  draws: 5,
  minutes: 60,
  recipes: [],
};

/**
 * The draft as the `launch` door takes it — the contract's own shape, parsed by the contract's
 * own schema, with exactly the knobs the chosen preset owns.
 *
 * A preset carries no flag it cannot use: sending `minutes` with `read-whats-new` would be an
 * argument the door has no business for, and `LaunchInputSchema` being strict means the hub
 * would refuse the whole launch for it. So the knobs are filtered here, once, by the same table
 * the card renders from.
 */
export function launchInput(draft: LaunchDraft): LaunchInput {
  const card = PRESET_CARDS[draft.preset];
  return LaunchInputSchema.parse({
    machineId: draft.machineId,
    preset: draft.preset,
    recipes: card.takesRecipes ? [...draft.recipes] : [],
    ...(card.knob === "topic" && draft.entityId !== "" ? { entityId: draft.entityId } : {}),
    ...(card.knob === "days" ? { sinceDays: draft.sinceDays } : {}),
    ...(card.knob === "draws" ? { draws: draft.draws } : {}),
    ...(card.knob === "minutes" ? { minutes: draft.minutes } : {}),
  });
}

/** Why a draft cannot be started yet, in one clause, or empty when it can. */
export function unready(draft: LaunchDraft): string {
  if (draft.machineId === "") return "Pick a machine to run on.";
  if (PRESET_CARDS[draft.preset].knob === "topic" && draft.entityId === "") return "Pick a topic to explore.";
  return "";
}

// ---------------------------------------------------------------------------- figures and clocks

export const FRESHNESS_NOTE: Record<string, string> = {
  fresh: "Heard from seconds ago.",
  recent: "Heard from a minute or two ago.",
  lost: "Nothing heard for a long time. That is not the same as dead.",
  ended: "Finished: the receipt is written.",
};

/** The kinds a run row carries, in the two words a receipt records. */
export const RUN_KIND_LABELS: Record<string, string> = {
  explore: "Exploration",
  evaluate: "Review",
  conductor: "Loop",
  prepare: "Preparation",
  scan: "Scan",
  archive: "Archive",
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
