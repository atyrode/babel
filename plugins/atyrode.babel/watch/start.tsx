import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, PRESETS, door } from "../contract.ts";
import {
  KNOB_BOUNDS,
  PRESET_CARDS,
  unready,
  usd,
  usdRate,
  type LaunchAnswer,
  type LaunchDraft,
  type Preset,
  type RecipeRow,
  type TopicRow,
} from "./api.ts";

/*
  START SOMETHING — the half of Watch that replaces the launch form.

  What was here before was the CLI with labels on it: a kind picker, then eleven flags —
  `--preparation`, `--develop`, `--retrievals`, `--fetches`, `--challenge`, `--synthesize`,
  `--until`, `--concurrent`, `--consolidate`, `--evaluate`, `--once` — of which the chosen kind
  refused between four and seven, so the form's real content was knowledge of which
  combinations the server would reject. An operator who wanted the last day's sessions read had
  to know that this is `explore` over a `preparation` he must mint first.

  Five cards replace it. Each says what asking for it does, carries the one knob it owns, and —
  before its button — states what will actually run: which machine, which profile and model,
  what a thousand tokens cost there, and the two ceilings the run is bounded by. The last three
  are not this panel's opinion: they come back from the `launch` door itself on a dry read, from
  the machine's own `code engine --describe`, so the sentence above the button and the receipt
  written afterwards cannot disagree (#251).
*/

export interface StartProps {
  readonly draft: LaunchDraft;
  readonly machines: readonly MachineSummary[];
  readonly topics: readonly TopicRow[];
  readonly recipes: readonly RecipeRow[];
  /** The dry read of what will run; null until a machine is picked and the door has answered. */
  readonly preview: LaunchAnswer | null;
  /** Why there is no preview: the door's own sentence, or what is still missing. */
  readonly previewNote: string;
  readonly starting: boolean;
  /** What the last start said — the run it created, or the refusal. */
  readonly note: string;
  readonly onDraft: (draft: LaunchDraft) => void;
  readonly onStart: () => void;
}

/** The one knob a preset owns, as a number the operator can only set inside the contract's bounds. */
function Knob({ draft, onDraft }: { readonly draft: LaunchDraft; readonly onDraft: (draft: LaunchDraft) => void }) {
  const card = PRESET_CARDS[draft.preset];
  const bounds = KNOB_BOUNDS[card.knob];
  const value = card.knob === "days" ? draft.sinceDays : card.knob === "draws" ? draft.draws : draft.minutes;
  return (
    <label className="plugin-atyrode_babel_watch__knob">
      <span className="plugin-atyrode_babel_watch__knob-label">{card.knobLabel}</span>
      <input
        type="number"
        className="plugin-atyrode_babel_watch__knob-input"
        min={bounds.min}
        max={bounds.max}
        step={bounds.step}
        value={value}
        /*
          `onInput` rather than `onChange`: it is the native event a field fires on every
          keystroke, which is what a live figure wants, and it is the one React's own
          `onChange` is an alias for on a text field. Naming it is the smaller surprise.

          The value is clamped to the contract's own bounds here rather than trusted: a
          spinner can be typed into, and `LaunchInputSchema` would refuse the whole launch
          for a 0 or a 400 — a refusal the operator could not explain from the screen.
        */
        onInput={(event) => {
          const typed = Number(event.currentTarget.value);
          const next = Number.isFinite(typed)
            ? Math.min(bounds.max, Math.max(bounds.min, Math.round(typed)))
            : bounds.min;
          onDraft(
            card.knob === "days"
              ? { ...draft, sinceDays: next }
              : card.knob === "draws"
                ? { ...draft, draws: next }
                : { ...draft, minutes: next },
          );
        }}
      />
      <span className="plugin-atyrode_babel_watch__knob-unit">
        {card.knob === "days" ? "days of sessions" : card.knob === "draws" ? "candidates drawn" : "minutes of loop"}
      </span>
    </label>
  );
}

/**
 * The topic picker, which is the whole of `explore-topic`'s knob.
 *
 * Entities with nothing filed are not topics (#248), so the list is the `topics` door's own
 * rows — what Babel has actually written about — and each option carries how much is under it,
 * because "the topic with four posts" and "the topic with four hundred" are different requests.
 */
function TopicKnob({
  draft,
  topics,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly topics: readonly TopicRow[];
  readonly onDraft: (draft: LaunchDraft) => void;
}) {
  return (
    <label className="plugin-atyrode_babel_watch__knob">
      <span className="plugin-atyrode_babel_watch__knob-label">Topic</span>
      <select
        className="plugin-atyrode_babel_watch__picker"
        value={draft.entityId}
        onChange={(event) => onDraft({ ...draft, entityId: event.target.value })}
      >
        <option value="">Pick a topic…</option>
        {topics.map((topic) => (
          <option key={topic.id} value={topic.id}>
            {topic.name} · {topic.posts} posts
          </option>
        ))}
      </select>
    </label>
  );
}

/**
 * The cookbook selection: nothing chosen runs the enabled default set, which is what the
 * contract's empty array means, so the empty state is a statement rather than a blank.
 */
function RecipePicks({
  draft,
  recipes,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly recipes: readonly RecipeRow[];
  readonly onDraft: (draft: LaunchDraft) => void;
}) {
  if (recipes.length === 0) return null;
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__recipe-picks">
      <span className="plugin-atyrode_babel_watch__knob-label">
        {draft.recipes.length === 0 ? "Recipes — the enabled default set" : `Recipes — ${draft.recipes.length} chosen`}
      </span>
      <Cluster gap="var(--babel-space-1)">
        {recipes.map((recipe) => {
          const picked = draft.recipes.includes(recipe.id);
          return (
            <button
              key={recipe.id}
              type="button"
              className="plugin-atyrode_babel_watch__chip"
              aria-pressed={picked}
              onClick={() =>
                onDraft({
                  ...draft,
                  recipes: picked ? draft.recipes.filter((id) => id !== recipe.id) : [...draft.recipes, recipe.id],
                })
              }
            >
              {recipe.title === "" ? recipe.id : recipe.title}
            </button>
          );
        })}
      </Cluster>
    </Stack>
  );
}

/**
 * WHAT WILL RUN, before the button.
 *
 * The machine is a picker rather than a reading because it is the one thing the operator
 * chooses that the door cannot infer, and the rest of the line is the door's answer about that
 * choice: a profile, a model, a disclosure, a cost per thousand tokens and the two ceilings.
 * When the machine has not answered, the line says so — a blank where a model should be is the
 * failure this section exists to prevent.
 */
function WillRun({
  draft,
  machines,
  preview,
  previewNote,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly machines: readonly MachineSummary[];
  readonly preview: LaunchAnswer | null;
  readonly previewNote: string;
  readonly onDraft: (draft: LaunchDraft) => void;
}) {
  const ceiling =
    preview === null ? null : (
      <>
        stops at {usd(preview.ceiling.perRunUsd)} this run, {usd(preview.ceiling.perDayUsd)} today.
      </>
    );
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__willrun">
      <Cluster gap="var(--babel-space-2)">
        <label className="plugin-atyrode_babel_watch__knob">
          <span className="plugin-atyrode_babel_watch__knob-label">Machine</span>
          <select
            className="plugin-atyrode_babel_watch__picker"
            value={draft.machineId}
            onChange={(event) => onDraft({ ...draft, machineId: event.target.value })}
          >
            <option value="">Pick a machine…</option>
            {machines.map((machine) => (
              <option key={machine.id} value={machine.id} disabled={!machine.online}>
                {machine.name}
                {machine.online ? "" : " · offline"}
              </option>
            ))}
          </select>
        </label>
      </Cluster>
      {preview === null ? (
        <p className="plugin-atyrode_babel_watch__willrun-line plugin-atyrode_babel_watch__muted">
          {previewNote === "" ? "Asking the machine what will run…" : previewNote}
        </p>
      ) : preview.profile === null ? (
        /*
          The door answered and the machine named no profile: the ceilings are still facts, and
          the model being unknown is the sentence the operator needs rather than a blank where a
          name should be (#251 is exactly this complaint).
        */
        <p className="plugin-atyrode_babel_watch__willrun-line">
          Will run <strong>{preview.kind}</strong>, but the machine has not said under which profile, so the
          model is unknown · {ceiling}
        </p>
      ) : (
        <p className="plugin-atyrode_babel_watch__willrun-line">
          Will run <strong>{preview.kind}</strong> as <strong>{preview.profile.model}</strong> under profile{" "}
          <span className="plugin-atyrode_babel_watch__mono">
            {preview.profile.id}/{preview.profile.revision}
          </span>{" "}
          · {preview.profile.disclosure} · {usdRate(preview.profile.costPer1k.input)} in /{" "}
          {usdRate(preview.profile.costPer1k.output)} out per 1k tokens · {ceiling}
        </p>
      )}
    </Stack>
  );
}

export function Start({
  draft,
  machines,
  topics,
  recipes,
  preview,
  previewNote,
  starting,
  note,
  onDraft,
  onStart,
}: StartProps) {
  const card = PRESET_CARDS[draft.preset];
  const blocked = unready(draft);
  return (
    <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__section">
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Start something</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          Five requests in your own words. Babel records the run the same way whichever one you ask for.
        </p>
      </Stack>
      <Cluster gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__presets">
        {PRESETS.map((preset: Preset) => (
          <button
            key={preset}
            type="button"
            className="plugin-atyrode_babel_watch__preset"
            aria-pressed={preset === draft.preset}
            onClick={() => onDraft({ ...draft, preset })}
          >
            <span className="plugin-atyrode_babel_watch__preset-title">{PRESET_CARDS[preset].title}</span>
            <span className="plugin-atyrode_babel_watch__preset-does">{PRESET_CARDS[preset].does}</span>
          </button>
        ))}
      </Cluster>
      <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__open">
        <Cluster gap="var(--babel-space-4)" className="plugin-atyrode_babel_watch__knobs">
          {card.knob === "topic" ? (
            <TopicKnob draft={draft} topics={topics} onDraft={onDraft} />
          ) : (
            <Knob draft={draft} onDraft={onDraft} />
          )}
        </Cluster>
        {card.takesRecipes ? <RecipePicks draft={draft} recipes={recipes} onDraft={onDraft} /> : null}
        <WillRun
          draft={draft}
          machines={machines}
          preview={preview}
          previewNote={previewNote}
          onDraft={onDraft}
        />
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__primary"
            data-action={door(ACTIONS.launch)}
            disabled={starting || blocked !== ""}
            onClick={onStart}
          >
            {starting ? "Starting…" : card.title}
          </button>
          {blocked === "" ? null : <span className="plugin-atyrode_babel_watch__muted">{blocked}</span>}
          {note === "" ? null : <span className="plugin-atyrode_babel_watch__note">{note}</span>}
        </Cluster>
      </Stack>
    </Stack>
  );
}
