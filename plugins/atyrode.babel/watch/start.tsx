import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, PRESETS, PRESET_REACHES_MODEL, door } from "../contract.ts";
import {
  KNOB_BOUNDS,
  PRESET_CARDS,
  SESSION_POLICY_LABEL,
  THINKING_CHOICES,
  accountLabel,
  unready,
  usd,
  type AccountsResult,
  type LaunchAnswer,
  type LaunchDraft,
  type Preset,
  type RecipeRow,
  type SessionPick,
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
  before its button — states what will actually run: which machine, which account and model,
  what the owner meters that model at, and the two ceilings the run is bounded by. None of the
  last three is this panel's opinion: the price and the ceiling come back from `launchPreview`
  out of the OWNER's installed service policy, so the sentence above the button and the figure
  the owner enforces come from the same place (#251, #279).

  WHO ANSWERS IS NOW CHOSEN HERE. Until #279 the model, the thinking level and the account were
  behind a Code profile reference the machine resolved; Babel's own job launches the engine now,
  so the request names them and `launch` refuses a preset that reaches a model and names no
  session. The picker is therefore not a convenience — without it the button posts a launch the
  door answers `session_required`.
*/

export interface StartProps {
  readonly draft: LaunchDraft;
  readonly machines: readonly MachineSummary[];
  readonly topics: readonly TopicRow[];
  readonly recipes: readonly RecipeRow[];
  /** What the machine's broker has observed, or the reason nobody could be asked (#279). */
  readonly accounts: AccountsResult;
  /** The session the picker has made, or why it is not one yet; null when none is needed. */
  readonly session: SessionPick | null;
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
 * WHO ANSWERS THE RUN: an account, a model, and how hard it thinks (#279).
 *
 * The account is OFFERED rather than typed wherever it can be. The `accounts` door reads the
 * machine's own broker through Babel's binding to it, and an identity key is not something an
 * operator should have to go and find in another plugin's UI. When the broker cannot be asked —
 * not installed on this hub, or this caller not admitted to read it — the reason is shown and
 * the three fields a pool slot NAMES are taken typed instead, because an operator who knows his
 * credential's row number must still be able to spend it: a select with nothing in it and no
 * explanation is where a drain stops.
 *
 * The model is a TEXT FIELD and not a list, on purpose. Nothing on this side knows which models
 * the owner's policy prices — that is `launchPreview`'s answer, and it reads `unpriced` the
 * moment this field names something the policy has no price for — so a closed list here would
 * be the panel guessing at another machine's configuration and hiding the models it guessed
 * wrong about.
 */
function SessionPicker({
  draft,
  accounts,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly accounts: AccountsResult;
  readonly onDraft: (draft: LaunchDraft) => void;
}) {
  const session = draft.session;
  const typed = accounts.unavailable !== "";
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__session">
      <Cluster gap="var(--babel-space-3)">
        {typed ? (
          <>
            <label className="plugin-atyrode_babel_watch__knob">
              <span className="plugin-atyrode_babel_watch__knob-label">Provider</span>
              <input
                type="text"
                className="plugin-atyrode_babel_watch__session-input"
                placeholder="anthropic"
                value={session.provider}
                onInput={(event) =>
                  onDraft({ ...draft, session: { ...session, provider: event.currentTarget.value } })
                }
              />
            </label>
            <label className="plugin-atyrode_babel_watch__knob">
              <span className="plugin-atyrode_babel_watch__knob-label">Credential</span>
              <input
                type="text"
                className="plugin-atyrode_babel_watch__session-input"
                placeholder="7"
                value={session.credentialId}
                onInput={(event) =>
                  onDraft({ ...draft, session: { ...session, credentialId: event.currentTarget.value } })
                }
              />
            </label>
            <label className="plugin-atyrode_babel_watch__knob">
              <span className="plugin-atyrode_babel_watch__knob-label">Identity key</span>
              <input
                type="text"
                className="plugin-atyrode_babel_watch__session-input"
                placeholder="empty for an api key"
                value={session.identityKey}
                onInput={(event) =>
                  onDraft({ ...draft, session: { ...session, identityKey: event.currentTarget.value } })
                }
              />
            </label>
          </>
        ) : (
          <label className="plugin-atyrode_babel_watch__knob">
            <span className="plugin-atyrode_babel_watch__knob-label">Account</span>
            <select
              className="plugin-atyrode_babel_watch__picker"
              value={session.credentialId}
              onChange={(event) => {
                /*
                  The whole row is copied into the draft rather than its id kept as a key into a
                  list: the same three fields are what the operator types when the broker cannot
                  be read, and an identity left behind from a previous choice would name one
                  account with another's credential.
                */
                const row = accounts.accounts.find((entry) => entry.credentialId === event.target.value);
                onDraft({
                  ...draft,
                  session: {
                    ...session,
                    provider: row?.provider ?? "",
                    credentialId: row?.credentialId ?? "",
                    identityKey: row?.identityKey ?? "",
                  },
                });
              }}
            >
              <option value="">
                {accounts.accounts.length === 0 ? "No account offered yet…" : "Pick an account…"}
              </option>
              {accounts.accounts.map((row) => (
                <option key={row.credentialId} value={row.credentialId} disabled={row.disabled}>
                  {accountLabel(row)}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="plugin-atyrode_babel_watch__knob">
          <span className="plugin-atyrode_babel_watch__knob-label">Model</span>
          <input
            type="text"
            className="plugin-atyrode_babel_watch__session-input"
            placeholder="provider/model"
            value={session.model}
            onInput={(event) => onDraft({ ...draft, session: { ...session, model: event.currentTarget.value } })}
          />
        </label>
        <label className="plugin-atyrode_babel_watch__knob">
          <span className="plugin-atyrode_babel_watch__knob-label">Thinking</span>
          <select
            className="plugin-atyrode_babel_watch__picker"
            value={session.thinking}
            onChange={(event) => onDraft({ ...draft, session: { ...session, thinking: event.target.value } })}
          >
            {THINKING_CHOICES.map((choice) => (
              <option key={choice.value} value={choice.value}>
                {choice.label}
              </option>
            ))}
          </select>
        </label>
      </Cluster>
      {typed ? (
        <p className="plugin-atyrode_babel_watch__session-note plugin-atyrode_babel_watch__muted">
          {accounts.unavailable} Name the account yourself: its provider, the credential's own row
          number in the broker, and the identity it belongs to — empty for an api key, which has
          none.
        </p>
      ) : null}
    </Stack>
  );
}

/**
 * WHAT WILL RUN, before the button.
 *
 * Three things the operator chooses and the door cannot infer — the machine, the account and
 * the model — then the door's own answer about that choice: what the owner meters the model at,
 * the ceiling the request will carry, the state of the policy behind it, and what the machine's
 * last completed run actually ran under. When the door has not answered, the line says so: a
 * blank where a model should be is the failure this section exists to prevent.
 */
function WillRun({
  draft,
  machines,
  accounts,
  preview,
  previewNote,
  onDraft,
}: {
  readonly draft: LaunchDraft;
  readonly machines: readonly MachineSummary[];
  readonly accounts: AccountsResult;
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
            onChange={(event) =>
              /*
                A machine change CLEARS the account. The rows come from that machine's own
                broker, so a credential id kept across the change would name a row in another
                machine's broker — a launch the gateway refuses `account_unavailable` after the
                job is posted. The model and the thinking level are the operator's own words and
                survive.
              */
              onDraft({
                ...draft,
                machineId: event.target.value,
                session: { ...draft.session, provider: "", credentialId: "", identityKey: "" },
              })
            }
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
      {/*
        The picker waits for the machine, because the accounts are that machine's own: an empty
        select nobody can fill is worse than no select at all. A preset that reaches no model
        never shows one.
      */}
      {PRESET_REACHES_MODEL[draft.preset] && draft.machineId !== "" ? (
        <SessionPicker draft={draft} accounts={accounts} onDraft={onDraft} />
      ) : null}
      {preview === null ? (
        <p className="plugin-atyrode_babel_watch__willrun-line plugin-atyrode_babel_watch__muted">
          {previewNote === "" ? "Asking the machine what will run…" : previewNote}
        </p>
      ) : (
        /*
          WHAT WILL RUN AND WHAT IT WILL BE METERED AT, in the owner's own numbers (#279).

          `session` is always present and always says something an operator acts on: the policy
          is missing, the model is unpriced, the price and the ceiling are facts, or the machine's
          configuration could not be read from here. `profile` is the different sentence — what
          the machine's LAST completed run actually ran under — and a machine that has run
          nothing says so rather than leaving a blank where a model should be (#251).
        */
        <p className="plugin-atyrode_babel_watch__willrun-line">
          Will run <strong>{preview.kind}</strong>
          {preview.session.model === "" ? (
            <> · choose a model and an account</>
          ) : (
            <>
              {" "}
              as <strong>{preview.session.model}</strong>
              {preview.session.account === "" ? null : (
                <>
                  {" "}
                  on{" "}
                  <span className="plugin-atyrode_babel_watch__mono">
                    {preview.session.account}
                  </span>
                </>
              )}
            </>
          )}{" "}
          · {preview.session.note} · {ceiling}
          {preview.profile === null ? null : (
            <>
              {" "}
              · last run here: {preview.profile.model || "unknown"}
              {preview.profile.thinking === "" ? null : <> at {preview.profile.thinking}</>}
              {preview.profile.account === "" ? null : (
                <>
                  {" "}
                  on{" "}
                  <span className="plugin-atyrode_babel_watch__mono">{preview.profile.account}</span>
                </>
              )}
            </>
          )}
        </p>
      )}
      {preview === null ? null : (
        /*
          THE SESSION AS THE DOOR SEES IT, beside the price the line above states: the account
          and the model the request would carry, and the POLICY's state as one word. The word is
          not the note repeated — it is what the four notes are notes about, and it is the thing
          an operator acts differently on: `no policy` is `setupInference`, `unpriced` is another
          model, `policy unread` is nobody's fault and blocks nothing.
        */
        <Cluster gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__session-state">
          <span
            className="plugin-atyrode_babel_watch__session-policy"
            data-policy={preview.session.policy}
          >
            {SESSION_POLICY_LABEL[preview.session.policy] ?? preview.session.policy}
          </span>
          <span className="plugin-atyrode_babel_watch__muted">
            account{" "}
            <span className="plugin-atyrode_babel_watch__mono">
              {preview.session.account === "" ? "none chosen" : preview.session.account}
            </span>{" "}
            · model{" "}
            <span className="plugin-atyrode_babel_watch__mono">
              {preview.session.model === "" ? "none chosen" : preview.session.model}
            </span>
          </span>
          {preview.session.unreadable === "" ? null : (
            <span className="plugin-atyrode_babel_watch__muted">{preview.session.unreadable}</span>
          )}
        </Cluster>
      )}
    </Stack>
  );
}

export function Start({
  draft,
  machines,
  topics,
  recipes,
  accounts,
  session,
  preview,
  previewNote,
  starting,
  note,
  onDraft,
  onStart,
}: StartProps) {
  const card = PRESET_CARDS[draft.preset];
  /*
    WHY THE BUTTON IS NOT PRESSABLE, in one clause and in the order the operator would fix
    them: what the draft itself lacks first, then the session. The session's reason is the
    panel's half of the door's `session_required` — a launch posted without one is refused by
    name, and the operator would read that refusal after the press rather than before it.
  */
  const blocked = unready(draft) || (session !== null && !session.ok ? session.reason : "");
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
          accounts={accounts}
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
