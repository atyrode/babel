import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack, Switcher } from "@manifold/ui";
import { ACTIONS, DRAIN_PRESETS, door, type DrainPreset } from "../contract.ts";
import {
  DRAIN_BOUNDS,
  DRAIN_CARDS,
  DRAIN_STATE_NOTE,
  drainUnready,
  etaClause,
  figure,
  micros,
  perMinute,
  since,
  usd,
  type AccountsResult,
  type DrainDraft,
  type DrainStatus,
  type SessionPick,
  type TopicRow,
} from "./api.ts";
import { SessionPicker } from "./start.tsx";

/*
  DRAINING A WINDOW — the panel that makes a drain something an operator watches rather than
  something they hope is happening (#258).

  EVERY FIGURE HERE IS ONE THE RUNBOOK NAMES (§11.4), and each has exactly one thing it must do:
  jobs at the model must be non-zero within ninety seconds of the start; tokens a minute must be
  non-zero once a call has been metered; the spend must rise toward the target; the ETA must stay
  before the deadline. On 2026-09-13 none of them existed, so the operator was told "draining" at
  10:48 and again at 12:17, asked "is anything running?" at 12:04 — when no engine had existed
  for seventy-five minutes — and was given a percentage at 12:49 that came from a home-made
  script with a lookup bug. A process count, a socket count and a provider percentage are none of
  these numbers, and this panel shows none of them.

  THE ACCOUNT IS NAMED BEFORE THE BUTTON (#267). It is not a detail of the form: a drain exists
  to spend one account's window, and on the day nothing on the machine could say which account a
  running fan was burning. So the account is chosen through the SAME picker the Start form uses
  — the machine's own broker rows, the model, the thinking level — and one line the row shows
  back afterwards, and a drain cannot be started without it.

  IT IS A READING AND TWO ACTS. Starting and stopping are `drain.start` and `drain.stop`, each
  governed at the operation node; everything else on the screen is either something a job said or
  something folded from what it said.
*/

export interface DrainProps {
  readonly draft: DrainDraft;
  readonly drains: readonly DrainStatus[];
  readonly machines: readonly MachineSummary[];
  readonly topics: readonly TopicRow[];
  /** What this machine's broker has observed, or the reason nobody could be asked (#279). */
  readonly accounts: AccountsResult;
  /** The session the picker has made, or why it is not one yet: the drain always needs one. */
  readonly session: SessionPick;
  /** The panel's clock, ticked once a second while a drain is running. */
  readonly now: number;
  readonly starting: boolean;
  /** The drain whose stop is in flight, or empty. */
  readonly stopping: string;
  /** A failed read, a refused start or a refused stop, in the door's own words. */
  readonly note: string;
  readonly onDraft: (draft: DrainDraft) => void;
  readonly onStart: () => void;
  readonly onStop: (drain: DrainStatus) => void;
}

function Figure({
  label,
  value,
  note,
}: {
  readonly label: string;
  readonly value: string;
  readonly note: string;
}) {
  return (
    <Stack gap="var(--babel-space-1)" className="plugin-atyrode_babel_watch__stat">
      <span className="plugin-atyrode_babel_watch__stat-label">{label}</span>
      <span className="plugin-atyrode_babel_watch__stat-value">{value}</span>
      <span className="plugin-atyrode_babel_watch__muted">{note}</span>
    </Stack>
  );
}

/** A whole-number knob, clamped to the contract's own bounds so a spinner cannot be refused. */
function Spinner({
  label,
  unit,
  value,
  bounds,
  onValue,
}: {
  readonly label: string;
  readonly unit: string;
  readonly value: number;
  readonly bounds: { readonly min: number; readonly max: number; readonly step: number };
  readonly onValue: (value: number) => void;
}) {
  return (
    <label className="plugin-atyrode_babel_watch__knob">
      <span className="plugin-atyrode_babel_watch__knob-label">{label}</span>
      <input
        type="number"
        className="plugin-atyrode_babel_watch__knob-input"
        min={bounds.min}
        max={bounds.max}
        step={bounds.step}
        value={value}
        onInput={(event) => {
          const typed = Number(event.currentTarget.value);
          onValue(
            Number.isFinite(typed)
              ? Math.min(bounds.max, Math.max(bounds.min, Math.round(typed)))
              : bounds.min,
          );
        }}
      />
      <span className="plugin-atyrode_babel_watch__knob-unit">{unit}</span>
    </label>
  );
}

function Field({
  label,
  value,
  placeholder,
  onValue,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder: string;
  readonly onValue: (value: string) => void;
}) {
  return (
    <label className="plugin-atyrode_babel_watch__knob">
      <span className="plugin-atyrode_babel_watch__knob-label">{label}</span>
      <input
        type="text"
        className="plugin-atyrode_babel_watch__field"
        value={value}
        placeholder={placeholder}
        onInput={(event) => onValue(event.currentTarget.value)}
      />
    </label>
  );
}

/**
 * ONE RUNNING DRAIN, IN THE SIX NUMBERS §11.4 NAMES, and the account it is spending.
 *
 * The spend is shown against the target rather than alone, because "$1.83" answers nothing and
 * "$1.83 of $5.00" answers the question that was asked. The ETA carries the deadline beside it
 * for the same reason: the fact an operator acts on is whether one is before the other.
 */
function Running({
  drain,
  now,
  stopping,
  onStop,
}: {
  readonly drain: DrainStatus;
  readonly now: number;
  readonly stopping: string;
  readonly onStop: (drain: DrainStatus) => void;
}) {
  const refusals = Object.entries(drain.refusals);
  const closures = Object.entries(drain.closures);
  const target =
    drain.target.costMicros === undefined
      ? drain.target.outputTokens === undefined
        ? "no spend target"
        : `${figure(drain.spent.outputTokens)} of ${figure(drain.target.outputTokens)} out`
      : `${micros(drain.spent.costMicros)} of ${micros(drain.target.costMicros)}`;
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__drain">
      <Cluster gap="var(--babel-space-3)">
        <span className="plugin-atyrode_babel_watch__mono">{drain.drainId}</span>
        <span className="plugin-atyrode_babel_watch__stat-label" title={DRAIN_STATE_NOTE[drain.state] ?? ""}>
          {drain.state}
        </span>
        <span className="plugin-atyrode_babel_watch__muted">
          {DRAIN_CARDS[drain.preset].title} on {drain.machineId} · started {since(drain.startedAt, now)}
        </span>
      </Cluster>
      {/*
        THE ACCOUNT AND THE MODEL, on the row and not only on the form (#267): an operator
        reading a drain that is already running is asking which account it is burning, and the
        form they filled in half an hour ago is not on the screen any more.
      */}
      <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__drain-account">
        <span className="plugin-atyrode_babel_watch__lane">
          account <span className="plugin-atyrode_babel_watch__mono">{drain.account}</span>
        </span>
        <span className="plugin-atyrode_babel_watch__lane">
          model <span className="plugin-atyrode_babel_watch__mono">{drain.model}</span>
        </span>
        {drain.budgetId === "" ? (
          <span className="plugin-atyrode_babel_watch__muted">no overlay: the standing bound admits this fan</span>
        ) : (
          <span className="plugin-atyrode_babel_watch__lane">
            overlay <span className="plugin-atyrode_babel_watch__mono">{drain.budgetId}</span>
          </span>
        )}
      </Cluster>
      <Switcher threshold="18rem" gap="var(--babel-space-4)">
        <Figure
          label="Jobs live"
          value={`${figure(drain.jobsLive)} of ${figure(drain.concurrent)}`}
          note={`${figure(drain.jobsLaunched)} launched, ${figure(drain.jobsSettled)} settled.`}
        />
        <Figure
          label="At the model"
          value={figure(drain.jobsAtModel)}
          note={
            drain.jobsStalled > 0
              ? `${figure(drain.jobsStalled)} stalled: at the model, nothing metered for 90s.`
              : "Must be non-zero within 90 seconds of the start."
          }
        />
        <Figure
          label="Output tokens"
          value={perMinute(drain.outputTokensPerMinute)}
          note="Flat for three minutes while jobs are at the model is a stop."
        />
        <Figure
          label="Spend"
          value={target}
          note={`${micros(drain.settled.costMicros)} settled · ${micros(drain.spent.costMicros - drain.settled.costMicros)} in flight.`}
        />
        <Figure
          label="ETA"
          value={etaClause(drain, now)}
          note={`${micros(drain.costMicrosPerMinute)} a minute, metered.`}
        />
        <Figure
          label="Calls"
          value={figure(drain.spent.calls)}
          note={`${figure(drain.spent.inputTokens)} in / ${figure(drain.spent.outputTokens)} out.`}
        />
      </Switcher>
      {refusals.length === 0 && closures.length === 0 ? null : (
        <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__lanes">
          {closures.map(([closure, n]) => (
            <span key={`closure:${closure}`} className="plugin-atyrode_babel_watch__lane">
              {closure} <span className="plugin-atyrode_babel_watch__mono">{figure(n)}</span>
            </span>
          ))}
          {/*
            REFUSALS ARE PAID WORK WITH NO RESULT (#265), so they are their own row rather than a
            kind of failure: a model answered, the submission did not stand, and the deployment
            was charged for it.
          */}
          {refusals.map(([code, n]) => (
            <span key={`refusal:${code}`} className="plugin-atyrode_babel_watch__stalled">
              refused {code} <span className="plugin-atyrode_babel_watch__mono">{figure(n)}</span>
            </span>
          ))}
        </Cluster>
      )}
      {drain.state === "running" ? (
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__quiet"
            data-action={door(ACTIONS.drainStop)}
            disabled={stopping === drain.drainId}
            onClick={() => onStop(drain)}
          >
            {stopping === drain.drainId ? "Stopping…" : "Stop this drain"}
          </button>
          <span className="plugin-atyrode_babel_watch__muted">
            Cancels what is in flight and clears the overlay. Never kill a job by hand.
          </span>
        </Cluster>
      ) : (
        <span className="plugin-atyrode_babel_watch__muted">
          {drain.reason === "" ? (DRAIN_STATE_NOTE[drain.state] ?? "") : drain.reason} · ended{" "}
          {drain.finishedAt === "" ? "at an instant nobody recorded" : since(drain.finishedAt, now)}
        </span>
      )}
    </Stack>
  );
}

export function Drain({
  draft,
  drains,
  machines,
  topics,
  accounts,
  session,
  now,
  starting,
  stopping,
  note,
  onDraft,
  onStart,
  onStop,
}: DrainProps) {
  const card = DRAIN_CARDS[draft.preset];
  const blocked = drainUnready(draft, session);
  return (
    // The section carries a class of its own because two sections on this screen offer a
    // "Machine" picker: `watch/test/drain.test.ts` scopes its reads to this one, and a test that
    // could not would drive the Start form while asserting about the drain.
    <Stack
      gap="var(--babel-space-3)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__drain-section"
    >
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Drain a window</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          Spend one account's remaining usage before it resets. Babel keeps the fan you ask for in
          flight, measures what the hub metered, and stops itself at the target or the deadline.
        </p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      {drains.length === 0 ? (
        <p className="plugin-atyrode_babel_watch__muted">Nothing has been drained on this hub yet.</p>
      ) : (
        drains.map((drain) => (
          <Running key={drain.drainId} drain={drain} now={now} stopping={stopping} onStop={onStop} />
        ))
      )}
      <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__open">
        {/*
          The cards carry `__drain-preset` rather than the Start section's `__preset`, and the
          difference is a contract rather than a style: `watch/test/start.test.tsx` asserts that
          the five preset cards on the screen are the five Watch offers, in order, and a drain
          offering three of them under the same class would make that assertion about both
          sections at once.
        */}
        <Cluster gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__presets">
          {DRAIN_PRESETS.map((preset: DrainPreset) => (
            <button
              key={preset}
              type="button"
              className="plugin-atyrode_babel_watch__drain-preset"
              aria-pressed={preset === draft.preset}
              onClick={() => onDraft({ ...draft, preset })}
            >
              <span className="plugin-atyrode_babel_watch__preset-title">{DRAIN_CARDS[preset].title}</span>
              <span className="plugin-atyrode_babel_watch__preset-does">{DRAIN_CARDS[preset].does}</span>
            </button>
          ))}
        </Cluster>
        <Cluster gap="var(--babel-space-4)" className="plugin-atyrode_babel_watch__knobs">
          <label className="plugin-atyrode_babel_watch__knob">
            <span className="plugin-atyrode_babel_watch__knob-label">Machine</span>
            <select
              className="plugin-atyrode_babel_watch__picker"
              value={draft.machineId}
              onChange={(event) =>
                // A machine change CLEARS the account, as it does in the Start form: the rows
                // are that machine's broker's own, and a credential kept across the change
                // would name a row in another one — refused `account_unavailable` on the
                // machine, after the jobs were posted and the overlay set.
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
          {card.knob === "topic" ? (
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
          ) : card.knob === "days" ? (
            <Spinner
              label="Sessions from"
              unit="days back"
              value={draft.sinceDays}
              bounds={{ min: 1, max: 365, step: 1 }}
              onValue={(sinceDays) => onDraft({ ...draft, sinceDays })}
            />
          ) : (
            <Spinner
              label="Each scan runs"
              unit="minutes"
              value={draft.minutes}
              bounds={{ min: 5, max: 24 * 60, step: 5 }}
              onValue={(minutes) => onDraft({ ...draft, minutes })}
            />
          )}
          <Spinner
            label="Jobs at once"
            unit="in flight"
            value={draft.concurrent}
            bounds={DRAIN_BOUNDS.concurrent}
            onValue={(concurrent) => onDraft({ ...draft, concurrent })}
          />
          <Spinner
            label="Stop after"
            unit="minutes"
            value={draft.minutesToDeadline}
            bounds={DRAIN_BOUNDS.minutesToDeadline}
            onValue={(minutesToDeadline) => onDraft({ ...draft, minutesToDeadline })}
          />
          <Spinner
            label="Or at"
            unit={draft.targetUsd > 0 ? `${usd(draft.targetUsd)} metered` : "dollars (0 = no cost target)"}
            value={draft.targetUsd}
            bounds={{ min: 0, max: 10_000, step: 1 }}
            onValue={(targetUsd) => onDraft({ ...draft, targetUsd })}
          />
        </Cluster>
        {/*
          THE ACCOUNT THIS DRAIN SPENDS, through the Start form's own picker (#279, #267): the
          machine's broker rows where they can be read, the three fields typed where they cannot,
          and the model beside them. The picker waits for the machine because the rows are that
          machine's own, and a drain of `keep-going` waits for it too — that preset reaches no
          model, but a drain of it is the rehearsal of one that does, so the account it would
          spend is named before the fan rather than discovered when the real drain is started.
        */}
        {draft.machineId === "" ? null : (
          <SessionPicker
            session={draft.session}
            accounts={accounts}
            onSession={(session) => onDraft({ ...draft, session })}
          />
        )}
        <Cluster gap="var(--babel-space-4)" className="plugin-atyrode_babel_watch__knobs">
          <Field
            label="Why"
            value={draft.reason}
            placeholder="the 7-day window resets at 13:00Z"
            onValue={(reason) => onDraft({ ...draft, reason })}
          />
        </Cluster>
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__primary"
            data-action={door(ACTIONS.drainStart)}
            disabled={starting || blocked !== ""}
            onClick={onStart}
          >
            {starting ? "Starting…" : `Drain with ${card.title.toLowerCase()}`}
          </button>
          {blocked === "" ? null : <span className="plugin-atyrode_babel_watch__muted">{blocked}</span>}
        </Cluster>
      </Stack>
    </Stack>
  );
}
