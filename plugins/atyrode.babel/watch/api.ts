import type { HostServices } from "@manifold/plugin";
import { z } from "zod";
import {
  AccountRowSchema,
  AccountsResultSchema,
  LaunchInputSchema,
  LaunchRequestSchema,
  LaunchResultSchema,
  MODEL_REFERENCE,
  OPERATIONS,
  PRESETS,
  PRESET_OPERATIONS,
  PRESET_REACHES_MODEL,
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
  type LaunchInput,
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

/**
 * WHO ANSWERS THE RUN, as the picker holds it (#279).
 *
 * Five strings rather than the contract's `SessionChoice`, because a picker is a half-made
 * choice for as long as the operator is making it: a model typed but no account yet is a state
 * `SessionChoiceSchema` has no shape for, and a draft that could only hold valid sessions could
 * not hold what the operator is looking at. {@link sessionChoice} is the one place the two meet.
 *
 * `provider`, `credentialId` and `identityKey` are copied off the chosen {@link AccountRow}
 * rather than looked up on the way out: the same three fields are what the operator TYPES when
 * the broker cannot be read, so the draft holds the choice itself and not a key into a list that
 * may not exist. The scope is not among them — it names the observation a pool was frozen from
 * and is the broker's to state, so {@link sessionChoice} takes it off the row.
 */
export interface SessionDraft {
  /** `provider/model`, the reference omp routes by; prefilled from the machine's last run. */
  readonly model: string;
  /** One of `THINKING_LEVELS`, or "" for whatever the model does by default. */
  readonly thinking: string;
  readonly provider: string;
  readonly credentialId: string;
  /** The OAuth identity, or "" for an api-key credential, which has none. */
  readonly identityKey: string;
}

export const INITIAL_SESSION: SessionDraft = {
  model: "",
  thinking: "",
  provider: "",
  credentialId: "",
  identityKey: "",
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
  /** The model, the thinking level and the account; ignored by a preset that reaches no model. */
  readonly session: SessionDraft;
}

export const INITIAL_DRAFT: LaunchDraft = {
  preset: PRESETS[0],
  machineId: "",
  entityId: "",
  sinceDays: 1,
  draws: 5,
  minutes: 60,
  recipes: [],
  session: INITIAL_SESSION,
};

/**
 * The draft as the `launch` door takes it — the contract's own shape, parsed by the contract's
 * own schema, with exactly the knobs the chosen preset owns.
 *
 * A preset carries no flag it cannot use: sending `minutes` with `read-whats-new` would be an
 * argument the door has no business for, and `LaunchInputSchema` being strict means the hub
 * would refuse the whole launch for it. So the knobs are filtered here, once, by the same table
 * the card renders from.
 *
 * The SESSION is the same rule applied to the choice the picker makes (#279): it travels only
 * for a preset that reaches a model — `PRESET_REACHES_MODEL`, the table `launch` refuses by —
 * and it is absent rather than half-made while the operator is still choosing, because
 * `launchPreview` is polled throughout that and must answer without one.
 */
export function launchInput(draft: LaunchDraft, session: SessionChoice | null = null): LaunchInput {
  const card = PRESET_CARDS[draft.preset];
  return LaunchInputSchema.parse({
    machineId: draft.machineId,
    preset: draft.preset,
    recipes: card.takesRecipes ? [...draft.recipes] : [],
    ...(card.knob === "topic" && draft.entityId !== "" ? { entityId: draft.entityId } : {}),
    ...(card.knob === "days" ? { sinceDays: draft.sinceDays } : {}),
    ...(card.knob === "draws" ? { draws: draft.draws } : {}),
    ...(card.knob === "minutes" ? { minutes: draft.minutes } : {}),
    ...(session !== null && PRESET_REACHES_MODEL[draft.preset] ? { session } : {}),
  });
}

/**
 * The same draft as the `launch` door takes it: the request above plus the OPERATION NODE.
 *
 * `launch` holds `machines:run` at a node rather than over the workspace, and the host reads
 * that node out of the arguments this function builds — so a panel that posted only a machine id
 * would be refused `invalid authority target` before the door ran. The node is the machine the
 * operator picked and the operation his preset becomes, which is `PRESET_OPERATIONS`, the same
 * table the door plans from.
 */
export function launchRequest(
  draft: LaunchDraft,
  session: SessionChoice | null = null,
): z.infer<typeof LaunchRequestSchema> {
  const input = launchInput(draft, session);
  return LaunchRequestSchema.parse({
    ...input,
    operation: {
      kind: "operation",
      machineId: draft.machineId,
      operationId: PRESET_OPERATIONS[draft.preset],
    },
  });
}

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

/** Why a draft cannot be started yet, in one clause, or empty when it can. */
export function unready(draft: LaunchDraft): string {
  if (draft.machineId === "") return "Pick a machine to run on.";
  if (PRESET_CARDS[draft.preset].knob === "topic" && draft.entityId === "") return "Pick a topic to explore.";
  return "";
}

// ------------------------------------------------------------------ who answers the run (#279)

/*
  THE PICKER'S HALF OF THE SESSION.

  Until #279 the model, the thinking level and the account were behind a Code profile reference
  the machine resolved, so the panel had nothing to choose and nothing to show: the operator
  read a model off the LAST run and hoped the next one would agree. Code's engine is gone, the
  choice travels with the request, and `launch` refuses a preset that reaches a model and names
  no session — which is exactly why the button must be able to say what is missing, in the
  operator's own words, before he presses it.

  Three things are chosen: an account out of what the machine's broker has observed (the
  `accounts` door), the model reference omp routes by, and how hard it thinks. The FIRST is the
  one that can be unavailable: the broker is another plugin's Instance Service, and a hub where
  it is not installed — or a caller not admitted to read it — answers a reason rather than a
  list. A picker that showed an empty select there would be a dead end; this one takes the three
  fields typed, because an operator who knows his credential's row number must still be able to
  spend it.
*/

export type AccountRow = z.infer<typeof AccountRowSchema>;
export type AccountsResult = z.infer<typeof AccountsResultSchema>;

/** No machine picked yet: no accounts, and no reason either — nobody has been asked. */
export const NO_ACCOUNTS: AccountsResult = { accounts: [], unavailable: "" };

/** What the thinking picker offers; the empty one is the level nobody chose (`THINKING_LEVELS`). */
export const THINKING_CHOICES: readonly { readonly value: string; readonly label: string }[] = [
  { value: "", label: "none — the model's own default" },
  ...THINKING_LEVELS.map((level) => ({ value: level, label: level })),
];

/**
 * The policy state as the one word beside the price, per `SESSION_POLICY_STATES`. A state this
 * build does not know is shown as the word the door sent rather than translated into a guess,
 * which is why the map is keyed loosely.
 */
export const SESSION_POLICY_LABEL: Record<string, string> = {
  missing: "no policy",
  unpriced: "unpriced",
  priced: "priced",
  unreadable: "policy unread",
  /** The hub refused the owner's policy for a meter kind it does not know (manifold#570). */
  unsupported: "hub too old",
};

/** One account as the picker offers it: who it is, whose provider, and whether it is spendable. */
export function accountLabel(row: AccountRow): string {
  const name =
    row.identityKey !== "" ? row.identityKey : row.label !== "" ? row.label : `credential ${row.credentialId}`;
  return `${name} · ${row.provider}${row.disabled ? " · blocked" : ""}`;
}

/** A session the door would take, or the named reason it is not one yet. */
export type SessionPick =
  | { readonly ok: true; readonly session: SessionChoice }
  | { readonly ok: false; readonly reason: string };

function incomplete(clause: string): SessionPick {
  return { ok: false, reason: `session_incomplete: ${clause}` };
}

/**
 * THE PICKER'S STATE AS THE CONTRACT'S SESSION, or why it is not one.
 *
 * The reason is what disables the button, so it is a clause an operator can act on and it is
 * NAMED — `session_incomplete` is the panel's half of the door's `session_required`, and
 * `account_blocked` is the one refusal the broker can hand us about a choice already made: an
 * account marked blocked between the poll that offered it and the press would fail on the
 * machine with `account_unavailable`, after the job was posted and a claim taken.
 *
 * The SCOPE is read off the offered row rather than held in the draft, because it names the
 * broker observation the account was seen in and is the broker's statement, not the operator's
 * choice. A TYPED account was seen in none, and carries a tag naming where it did come from:
 * the gateway worker checks only that every slot of one pool agrees on its scope and compares
 * it against no canonical value (`doors/inference.ts`), a pool Babel builds holds exactly one
 * slot, so the tag is both admissible and honest — this panel, on this machine, rather than an
 * observation that never happened.
 */
export function sessionChoice(draft: LaunchDraft, accounts: AccountsResult): SessionPick {
  const session = draft.session;
  const model = session.model.trim();
  if (model === "") return incomplete("name the model this run asks for, as provider/model");
  if (!MODEL_REFERENCE.test(model)) {
    return incomplete(
      `${model} is not a model reference — omp routes by provider/model, and a bare model id ` +
        `misses the route and the price at once`,
    );
  }
  if (session.provider === "" || session.credentialId === "") {
    return incomplete("choose the account this run spends");
  }
  const row = accounts.accounts.find((entry) => entry.credentialId === session.credentialId);
  if (row?.disabled === true) {
    return {
      ok: false,
      reason: `account_blocked: the broker reports ${accountLabel(row)}, so this run would be refused on the machine`,
    };
  }
  const parsed = SessionChoiceSchema.safeParse({
    model,
    ...(session.thinking === "" ? {} : { thinking: session.thinking }),
    account: {
      provider: session.provider,
      scope: row?.scope ?? `atyrode.babel.watch/typed/${draft.machineId}`,
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
