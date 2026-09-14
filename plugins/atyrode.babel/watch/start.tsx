import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, PRESET_START, door } from "../contract.ts";
import {
  GENERATOR_PANEL,
  KNOB_BOUNDS,
  LAUNCH_CARDS,
  LAUNCH_PRESETS,
  chosenProfile,
  generatorUri,
  launchUnready,
  type LaunchDraft,
  type Preset,
  type ProfileRow,
  type ProfilesResult,
  type RecipeRow,
  type TopicRow,
} from "./api.ts";

/*
  START SOMETHING — three requests, a Code profile, and a button (#279).

  WHAT IS NOT ON THIS FORM IS THE POINT. There is no model field, no thinking level and no
  account picker, and there will not be one: `atyrode.babel` depends on `atyrode.code`, which
  depends on `atyrode.omp`, and all three of those belong to a CODE PROFILE — a configured Code
  workspace. The operator picks a saved profile or opens Code's generator and parametrizes one
  there; Babel carries its container and the revision it was shown, and chooses none of the
  three. #284 put those dials here and the revert (#290) took them out; putting them back would
  be Babel deciding again what a model run is.

  THE LIST IS CODE'S AND SO IS ITS ABSENCE. The `profiles` door answers `{profiles, unavailable}`
  and both halves are answers: a hub with Code disabled, an install whose grant does not reach
  Code's doors, and an operator who has saved no profile are three situations with three
  remedies, and an empty select with no word beside it reads as the third when it is the first.
  So `unavailable` is rendered as the sentence Code refused with, and there is no button under
  it — a press that could only be refused is an interface asking to be discovered by pressing.

  THE GENERATOR LINK IS A NAVIGATION, NOT A CALLBACK. A plugin may only open its OWN panels, so
  the link addresses the WORKSPACE (`manifold://container/<id>`), where Code seats its generator
  panel. Nothing comes back through it: the operator parametrizes the run in Code and returns,
  and this section re-reads `profiles` — the revision it then shows is the one the launch
  carries, and a profile that moved in between is what `code_stale_preferences` refuses by name
  rather than silently accommodating.

  TWO OF THE FIVE PRESETS HAVE NO CARD. `review-backlog` and `file-and-tidy` are DRAWN: the
  coordinator picks the record, claims it under a fence, and the conductor dispatches it with a
  blinded projection. That dispatch is not on this build (#268), `launch` answers `draw_pending`
  for both, and the section says so once in prose instead of offering two buttons that refuse.
*/

export interface StartProps {
  readonly draft: LaunchDraft;
  readonly machines: readonly MachineSummary[];
  readonly topics: readonly TopicRow[];
  readonly recipes: readonly RecipeRow[];
  /** Code's saved profiles, or the sentence saying why Code could not be asked. */
  readonly profiles: ProfilesResult;
  readonly starting: boolean;
  /** What the last start said — the run it created, or the refusal. */
  readonly note: string;
  readonly onDraft: (draft: LaunchDraft) => void;
  readonly onStart: () => void;
  /** Where the shell is asked to go when the operator opens Code's generator. */
  readonly onOpen: (uri: string) => void;
}

/** The one numeric knob a preset owns, clamped to the contract's own bounds. */
function Knob({
  draft,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly onDraft: (draft: LaunchDraft) => void;
}) {
  const card = LAUNCH_CARDS[draft.preset];
  const knob = card?.knob ?? "days";
  const bounds = KNOB_BOUNDS[knob];
  const value = knob === "days" ? draft.sinceDays : draft.minutes;
  return (
    <label className="plugin-atyrode_babel_watch__knob">
      <span className="plugin-atyrode_babel_watch__knob-label">{card?.knobLabel ?? ""}</span>
      <input
        type="number"
        className="plugin-atyrode_babel_watch__knob-input"
        min={bounds.min}
        max={bounds.max}
        step={bounds.step}
        value={value}
        /*
          The value is clamped to the contract's own bounds here rather than trusted: a spinner
          can be typed into, and `LaunchInputSchema` would refuse the whole launch for a 0 or a
          400 — a refusal the operator could not explain from the screen.
        */
        onInput={(event) => {
          const typed = Number(event.currentTarget.value);
          const next = Number.isFinite(typed)
            ? Math.min(bounds.max, Math.max(bounds.min, Math.round(typed)))
            : bounds.min;
          onDraft(knob === "days" ? { ...draft, sinceDays: next } : { ...draft, minutes: next });
        }}
      />
      <span className="plugin-atyrode_babel_watch__knob-unit">
        {knob === "days" ? "days of sessions" : "minutes of loop"}
      </span>
    </label>
  );
}

/**
 * The topic picker, which is the whole of `explore-topic`'s knob. Entities with nothing filed
 * are not topics (#248), so the list is what Babel has actually written about, and each option
 * carries how much is under it: "the topic with four posts" and "the topic with four hundred"
 * are different requests.
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
        data-field="topic"
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

/** The cookbook selection: nothing chosen runs the enabled default set, which is a statement
 *  rather than a blank, so the label says which of the two the operator is looking at. */
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
        {draft.recipes.length === 0
          ? "Recipes — the enabled default set"
          : `Recipes — ${String(draft.recipes.length)} chosen`}
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
                  recipes: picked
                    ? draft.recipes.filter((id) => id !== recipe.id)
                    : [...draft.recipes, recipe.id],
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
 * WHICH CODE PROFILE ANSWERS THIS RUN: one row per saved workspace, naming the model leading
 * its default role and the depth that role thinks at — the two figures the operator would
 * otherwise have to open Code to read — and where Code last posted a session for it.
 *
 * A profile whose saved selection no longer reviews against its catalog answers `selected:
 * null`, which arrives here as two empty strings: it is a profile to OPEN IN CODE, said as
 * that rather than hidden, because hiding it would leave the operator hunting for a workspace
 * he knows he saved.
 */
function Profiles({
  draft,
  profiles,
  onDraft,
  onOpen,
}: {
  readonly draft: LaunchDraft;
  readonly profiles: ProfilesResult;
  readonly onDraft: (draft: LaunchDraft) => void;
  readonly onOpen: (uri: string) => void;
}) {
  if (profiles.unavailable !== "") {
    return (
      <p className="plugin-atyrode_babel_watch__note" data-field="profiles-unavailable">
        {profiles.unavailable}
      </p>
    );
  }
  if (profiles.profiles.length === 0) {
    return (
      <p className="plugin-atyrode_babel_watch__muted" data-field="profiles-empty">
        Code holds no saved profile yet. Open a workspace in Code, choose the model, the thinking
        level and the account there, and it appears here.
      </p>
    );
  }
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__profiles">
      <span className="plugin-atyrode_babel_watch__knob-label">Code profile</span>
      {profiles.profiles.map((profile: ProfileRow) => (
        <button
          key={profile.containerId}
          type="button"
          className="plugin-atyrode_babel_watch__profile"
          data-field="profile"
          data-container={profile.containerId}
          aria-pressed={profile.containerId === draft.containerId}
          onClick={() => onDraft({ ...draft, containerId: profile.containerId })}
        >
          <span className="plugin-atyrode_babel_watch__mono">{profile.containerId}</span>
          <span className="plugin-atyrode_babel_watch__muted">
            {profile.model === ""
              ? "no selection Code can review — open it in the generator"
              : `${profile.model}${profile.thinking === "" ? "" : ` · thinking ${profile.thinking}`}`}
          </span>
          <span className="plugin-atyrode_babel_watch__muted">
            {profile.lastMachineId === ""
              ? "Code has posted no session for it"
              : `Code last posted on ${profile.lastMachineId}`}
          </span>
        </button>
      ))}
      {/*
        THE WAY OUT AND BACK. It is a link rather than a form field because the parameters are
        not Babel's to hold: the operator sets them in Code, on the workspace, and this section
        re-reads `profiles` when he returns.
      */}
      <Cluster gap="var(--babel-space-2)">
        <a
          className="plugin-atyrode_babel_watch__link"
          data-panel={GENERATOR_PANEL}
          href={generatorUri(draft.containerId === "" ? (profiles.profiles[0]?.containerId ?? "") : draft.containerId)}
          onClick={(event) => {
            event.preventDefault();
            const containerId =
              draft.containerId === ""
                ? (profiles.profiles[0]?.containerId ?? "")
                : draft.containerId;
            if (containerId !== "") onOpen(generatorUri(containerId));
          }}
        >
          Parametrize it in Code&rsquo;s generator
        </a>
        <span className="plugin-atyrode_babel_watch__muted">
          The model, the thinking level and the account are set there. Come back and the list is
          re-read.
        </span>
      </Cluster>
    </Stack>
  );
}

export function Start({
  draft,
  machines,
  topics,
  recipes,
  profiles,
  starting,
  note,
  onDraft,
  onStart,
  onOpen,
}: StartProps) {
  const card = LAUNCH_CARDS[draft.preset];
  const profile = chosenProfile(draft, profiles.profiles);
  const reaches = PRESET_START[draft.preset] === "explore";
  const blocked = profiles.unavailable !== "" && reaches ? profiles.unavailable : launchUnready(draft, profile);
  return (
    // The section names itself, as the drain's does and for the same reason: two forms on this
    // screen offer a "Machine" picker, and `watch/test/start.test.tsx` scopes its reads to this
    // one rather than driving whichever came first in the document.
    <Stack
      gap="var(--babel-space-3)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__start-section"
    >
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Start something</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          Three requests in your own words, on a Code profile. A run that reaches a model is a
          Code session: Code owns the model, the thinking level and the account, and Babel posts
          the run through its <code>runSession</code> door.
        </p>
      </Stack>
      <Cluster gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__presets">
        {LAUNCH_PRESETS.map((preset: Preset) => (
          <button
            key={preset}
            type="button"
            className="plugin-atyrode_babel_watch__preset"
            aria-pressed={preset === draft.preset}
            onClick={() => onDraft({ ...draft, preset })}
          >
            <span className="plugin-atyrode_babel_watch__preset-title">
              {LAUNCH_CARDS[preset]?.title ?? preset}
            </span>
            <span className="plugin-atyrode_babel_watch__preset-does">
              {LAUNCH_CARDS[preset]?.does ?? ""}
            </span>
          </button>
        ))}
      </Cluster>
      <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__open">
        <Cluster gap="var(--babel-space-4)" className="plugin-atyrode_babel_watch__knobs">
          <label className="plugin-atyrode_babel_watch__knob">
            <span className="plugin-atyrode_babel_watch__knob-label">Machine</span>
            <select
              className="plugin-atyrode_babel_watch__picker"
              data-field="machine"
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
          {card?.knob === "topic" ? (
            <TopicKnob draft={draft} topics={topics} onDraft={onDraft} />
          ) : (
            <Knob draft={draft} onDraft={onDraft} />
          )}
        </Cluster>
        {card?.takesRecipes === true ? (
          <RecipePicks draft={draft} recipes={recipes} onDraft={onDraft} />
        ) : null}
        {/* `keep-going` is a scan of Babel's own and reaches no model, so it is offered without
            a profile rather than with one nothing would read. */}
        {reaches ? (
          <Profiles draft={draft} profiles={profiles} onDraft={onDraft} onOpen={onOpen} />
        ) : (
          <p className="plugin-atyrode_babel_watch__muted" data-field="no-profile-needed">
            Keep going is one <code>scan</code> of Babel&rsquo;s own. It reaches no model, so it
            needs no Code profile and spends nothing.
          </p>
        )}
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__primary"
            data-action={door(ACTIONS.launch)}
            disabled={starting || blocked !== ""}
            onClick={onStart}
          >
            {starting ? "Starting…" : (card?.title ?? "Start")}
          </button>
          {blocked === "" ? null : (
            <span className="plugin-atyrode_babel_watch__muted">{blocked}</span>
          )}
          {note === "" ? null : <span className="plugin-atyrode_babel_watch__note">{note}</span>}
        </Cluster>
      </Stack>
      <p className="plugin-atyrode_babel_watch__muted" data-field="drawn-pending">
        Reviewing the backlog and filing are <em>drawn</em>: the coordinator picks the record,
        claims it under a fence and dispatches it with a blinded projection of what is under
        review. That dispatch is not on this build, so neither is offered here — it returns with{" "}
        <a href="https://github.com/atyrode/babel/issues/268">babel#268</a>.
      </p>
    </Stack>
  );
}
