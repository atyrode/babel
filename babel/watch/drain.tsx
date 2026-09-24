import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Stack, Switcher } from "@manifold/ui";
import {
  ACTIONS,
  DRAIN_PRESETS,
  door,
  type DrainPreset,
  type DrainReportPayload,
} from "../contract.ts";
import {
  DRAIN_BOUNDS,
  DRAIN_CARDS,
  DRAIN_STATE_NOTE,
  accountsClause,
  chosenProfile,
  drainUnready,
  elapsedClock,
  etaClause,
  figure,
  micros,
  perMinute,
  since,
  usd,
  type DrainDraft,
  type DrainStatus,
  type ProfileRow,
  type ProfilesResult,
  type TopicRow,
} from "./api.ts";

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
  running fan was burning. It is TYPED here, not offered: what a run is composed from is a Code
  profile and account choice is Code's (#279), so Babel reads no broker of its own. The row
  shows it back afterwards, and a drain cannot be started without it.

  IT IS A READING AND TWO ACTS. Starting and stopping are `drain.start` and `drain.stop`, each
  governed at the operation node; everything else on the screen is either something a job said or
  something folded from what it said.
*/

export interface DrainProps {
  readonly draft: DrainDraft;
  readonly drains: readonly DrainStatus[];
  readonly machines: readonly MachineSummary[];
  readonly topics: readonly TopicRow[];
  /** Code's saved profiles, or the sentence saying why Code could not be asked. */
  readonly profiles: ProfilesResult;
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

/**
 * WHICH CODE PROFILE THIS DRAIN SPENDS (#267, #279).
 *
 * It was three typed fields — a provider, a credential id and an identity key — plus a model
 * and a thinking level, and all five were Babel deciding what a run is. They are gone. A
 * drain names a CODE PROFILE, exactly as Watch's Start section does and from the same
 * `profiles` door, and what it records about the model and the account is CODE'S OWN REPORT,
 * copied once when the button is pressed.
 *
 * A profile whose accounts Code does not report says so rather than showing a blank: at the
 * pin this plugin builds against Code publishes no `accounts` on a profile at all, and the
 * difference between "Code says none" and "Code was not asked" is the difference between a
 * drain an operator can account for and the one he could not on 2026-09-13.
 */
function ProfileFields({
  draft,
  profiles,
  onDraft,
}: {
  readonly draft: DrainDraft;
  readonly profiles: ProfilesResult;
  readonly onDraft: (draft: DrainDraft) => void;
}) {
  if (profiles.unavailable !== "") {
    return (
      <p className="plugin-atyrode_babel_watch__note" data-field="drain-profiles-unavailable">
        {profiles.unavailable}
      </p>
    );
  }
  return (
    <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__profiles">
      <span className="plugin-atyrode_babel_watch__knob-label">Code profile</span>
      {profiles.profiles.length === 0 ? (
        <span className="plugin-atyrode_babel_watch__muted">
          Code holds no saved profile yet; a drain is posted on one.
        </span>
      ) : null}
      {profiles.profiles.map((profile: ProfileRow) => (
        <button
          key={profile.containerId}
          type="button"
          className="plugin-atyrode_babel_watch__profile"
          data-field="drain-profile"
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
          <span className="plugin-atyrode_babel_watch__muted">{accountsClause(profile)}</span>
        </button>
      ))}
    </Stack>
  );
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
 * THE REPORT THE LAST DRAIN LEFT (#270), beside the drain it belongs to.
 *
 * On 2026-09-13 every one of these numbers was recovered by hand, hours later, out of receipts
 * and `/proc`. They are here because the operator's questions afterwards were always the same
 * five — what did it cost, on whose account, against which duties, how much erroring, how much
 * came out — and a panel that showed a drain while it ran and nothing once it stopped answered
 * none of them.
 *
 * It says what it CANNOT see as plainly as what it can. `unobserved` is the record's own list,
 * rendered rather than summarised, because a reader who does not know the machine's load is
 * missing will read its absence as "the load was fine".
 */
function Report({ report }: { readonly report: DrainReportPayload }) {
  const spent = report.tokens;
  const duties = report.allocation.ran;
  const unnamed = report.allocation.named.filter(
    (name) => !duties.some((lane) => lane.name === name),
  );
  const admissions = Object.entries(report.launchRefusals).sort(
    (left, right) => right[1] - left[1],
  );
  return (
    <Stack gap="var(--babel-space-1)" className="plugin-atyrode_babel_watch__drain-report">
      <Cluster gap="var(--babel-space-3)">
        <span className="plugin-atyrode_babel_watch__stat-label">what this drain came to</span>
        <span className="plugin-atyrode_babel_watch__muted">
          {elapsedClock(report.wallMs / 1000)} at {figure(report.concurrent)} jobs · a record the
          frontier can read
        </span>
      </Cluster>
      <p className="plugin-atyrode_babel_watch__muted">
        {figure(spent.inputTokens)} in / {figure(spent.outputTokens)} out /{" "}
        {figure(spent.cacheReadTokens)} cache read over {figure(spent.calls)} calls ·{" "}
        {micros(spent.costMicros)} on {report.account}
      </p>
      <p className="plugin-atyrode_babel_watch__muted">
        {figure(report.jobs.launched)} launched · {figure(report.jobs.reachedModel)} reached a model
        · {figure(report.jobs.settled)} settled
        {report.jobs.unsettled === 0 ? "" : ` · ${figure(report.jobs.unsettled)} never settled`}
        {report.jobs.withoutRunRow === 0
          ? ""
          : ` · ${figure(report.jobs.withoutRunRow)} left no run row`}
      </p>
      <p className="plugin-atyrode_babel_watch__muted">
        {figure(report.produced.records)} records and {figure(report.produced.assessments)}{" "}
        assessments · {report.produced.recordsPerMillionTokens.toFixed(1)} records and{" "}
        {report.produced.assessmentsPerMillionTokens.toFixed(1)} assessments a million tokens
      </p>
      <p className="plugin-atyrode_babel_watch__muted">
        at the model {(report.load.atModelFraction * 100).toFixed(1)}% of the fan's time, peak{" "}
        {figure(report.load.peakAtModel)} of {figure(report.concurrent)} · preparing{" "}
        {elapsedClock(report.pipeline.prepareWallMs / 1000)} against{" "}
        {elapsedClock(report.pipeline.sessionWallMs / 1000)} in session
      </p>
      {duties.length === 0 ? null : (
        <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__lanes">
          {duties.map((lane) => (
            <span key={`duty:${lane.name}`} className="plugin-atyrode_babel_watch__lane">
              {lane.name}{" "}
              <span className="plugin-atyrode_babel_watch__mono">
                {figure(lane.runs)} runs · {micros(lane.tokens.costMicros)}
              </span>
            </span>
          ))}
          {/*
            A DUTY THE OPERATOR NAMED AND NO RUN CARRIED is the allocation question answered in
            the direction that matters: "as named" and "as spent" are two lists, and the gap
            between them is the finding.
          */}
          {unnamed.map((name) => (
            <span key={`unrun:${name}`} className="plugin-atyrode_babel_watch__stalled">
              {name} <span className="plugin-atyrode_babel_watch__mono">never ran</span>
            </span>
          ))}
        </Cluster>
      )}
      {report.gaps.length === 0 ? null : (
        <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__lanes">
          {report.gaps.map((gap) => (
            <span
              key={`gap:${gap.reason}`}
              className="plugin-atyrode_babel_watch__stalled"
              title={gap.detail}
            >
              {gap.reason}{" "}
              <span className="plugin-atyrode_babel_watch__mono">{figure(gap.jobs)}</span>
            </span>
          ))}
        </Cluster>
      )}
      {admissions.length === 0 ? null : (
        <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__lanes">
          {admissions.map(([code, n]) => (
            <span key={`admission:${code}`} className="plugin-atyrode_babel_watch__stalled">
              never launched {code}{" "}
              <span className="plugin-atyrode_babel_watch__mono">{figure(n)}</span>
            </span>
          ))}
        </Cluster>
      )}
      {report.notes.length === 0 ? null : (
        <details className="plugin-atyrode_babel_watch__drain-notes">
          <summary className="plugin-atyrode_babel_watch__muted">
            {figure(report.notes.length)} note(s) the controller made
            {report.notesDropped === 0 ? "" : `, ${figure(report.notesDropped)} dropped`}
          </summary>
          <Stack gap="var(--babel-space-1)">
            {report.notes.map((note) => (
              <span
                key={`${note.at}:${note.kind}:${note.detail}`}
                className="plugin-atyrode_babel_watch__muted"
              >
                {note.kind} · {note.detail}
              </span>
            ))}
          </Stack>
        </details>
      )}
      <details className="plugin-atyrode_babel_watch__drain-unobserved">
        <summary className="plugin-atyrode_babel_watch__muted">
          what this report cannot answer
        </summary>
        <Stack gap="var(--babel-space-1)">
          {report.unobserved.map((missing) => (
            <span key={missing} className="plugin-atyrode_babel_watch__muted">
              {missing}
            </span>
          ))}
        </Stack>
      </details>
    </Stack>
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
        <span
          className="plugin-atyrode_babel_watch__stat-label"
          title={DRAIN_STATE_NOTE[drain.state] ?? ""}
        >
          {drain.state}
        </span>
        <span className="plugin-atyrode_babel_watch__muted">
          {DRAIN_CARDS[drain.preset].title} on {drain.machineId} · started{" "}
          {since(drain.startedAt, now)}
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
        {drain.state === "closing" ? (
          <span className="plugin-atyrode_babel_watch__muted">
            launching nothing more: folding what its last {figure(drain.jobsLive)} job(s) spend
          </span>
        ) : null}
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
      {drain.state === "running" || drain.state === "closing" ? (
        <Cluster gap="var(--babel-space-3)">
          <button
            type="button"
            className="plugin-atyrode_babel_watch__quiet"
            data-action={door(ACTIONS.drainStop)}
            disabled={stopping === drain.drainId}
            onClick={() => onStop(drain)}
          >
            {stopping === drain.drainId
              ? "Stopping…"
              : drain.state === "closing"
                ? "Cancel its last jobs"
                : "Stop this drain"}
          </button>
          {/*
            A CLOSING DRAIN IS THE ONE THE OPERATOR CAN STILL HELP. It stopped launching on its
            own target, and the tick that ended it held no `jobs:cancel` — this press does, at
            their shared operation. Either way the drain is over when their receipts land.
          */}
          <span className="plugin-atyrode_babel_watch__muted">
            {drain.state === "closing"
              ? "It has stopped launching; this cancels the jobs it could not. Never kill a job by hand."
              : "Cancels what is in flight; it ends when their receipts land. Never kill a job by hand."}
          </span>
        </Cluster>
      ) : (
        <span className="plugin-atyrode_babel_watch__muted">
          {drain.reason === "" ? (DRAIN_STATE_NOTE[drain.state] ?? "") : drain.reason} · ended{" "}
          {drain.finishedAt === "" ? "at an instant nobody recorded" : since(drain.finishedAt, now)}
        </span>
      )}
      {/*
        THE REPORT IS SHOWN WHERE THE DRAIN IS (#270), under the ended row it belongs to. The
        door carries it on the newest ended drain and nowhere else, so this is the last drain's
        report and there is never a second one to mistake it for.
      */}
      {drain.report === null ? null : <Report report={drain.report} />}
    </Stack>
  );
}

export function Drain({
  draft,
  drains,
  machines,
  topics,
  profiles,
  now,
  starting,
  stopping,
  note,
  onDraft,
  onStart,
  onStop,
}: DrainProps) {
  const card = DRAIN_CARDS[draft.preset];
  const profile = chosenProfile(draft, profiles.profiles);
  const blocked = drainUnready(draft, profile);
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
        <p className="plugin-atyrode_babel_watch__muted">
          Nothing has been drained on this hub yet.
        </p>
      ) : (
        drains.map((drain) => (
          <Running
            key={drain.drainId}
            drain={drain}
            now={now}
            stopping={stopping}
            onStop={onStop}
          />
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
              <span className="plugin-atyrode_babel_watch__preset-title">
                {DRAIN_CARDS[preset].title}
              </span>
              <span className="plugin-atyrode_babel_watch__preset-does">
                {DRAIN_CARDS[preset].does}
              </span>
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
                // A machine change KEEPS the profile: a Code workspace is not a machine's and
                // the destination is the caller's choice on every post (`TargetSchema`). What
                // this used to clear — a credential id belonging to one machine's broker — is
                // not a field of this form any more.
                onDraft({ ...draft, machineId: event.target.value })
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
              label="Each catalog runs"
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
            unit={
              draft.targetUsd > 0
                ? `${usd(draft.targetUsd)} metered`
                : "dollars (0 = no cost target)"
            }
            value={draft.targetUsd}
            bounds={{ min: 0, max: 10_000, step: 1 }}
            onValue={(targetUsd) => onDraft({ ...draft, targetUsd })}
          />
        </Cluster>
        {/*
          WHICH CODE PROFILE THIS DRAIN SPENDS (#267, #279), named before the fan rather than
          discovered while it runs. The model and the account are the profile's and Code's;
          what the drain records is Code's own report of them, copied at the press. A drain of
          `keep-going` names one too — that preset reaches no model, but a drain of it is the
          rehearsal of one that does, and rehearsing without naming the profile would rehearse
          a different operation.
        */}
        <ProfileFields draft={draft} profiles={profiles} onDraft={onDraft} />
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
          {blocked === "" ? null : (
            <span className="plugin-atyrode_babel_watch__muted">{blocked}</span>
          )}
        </Cluster>
      </Stack>
    </Stack>
  );
}
