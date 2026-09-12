import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { APIError, dismissAPIError } from "../api";
import { errorMessage, formatTime } from "../format";
import {
  ABSENT,
  count,
  dollars,
  durationText,
  getWatchCeilings,
  saveWatchCeilings,
  type CeilingRequest,
  type Ceilings,
} from "../watchapi";
import "../watch.css";

// What Babel may spend on itself (SPEC.md §5.7, `babel conductor configure`).
//
// This section exists because the ceilings lived nowhere in the interface. The
// launch form on Watch refuses a conductor with the command's own sentence —
// "the conductor has no budget ceilings, so it will not run. babel conductor
// configure --per-cycle 0.50 --per-day 5.00" — and linked to Settings ›
// Ceilings, which held the *focus* policy: what analysis may spend on one
// subject. Two different things under one word, and the remedy the refusal
// named was not on the page it pointed at. Now it is, at the top of it.
//
// Three rules shape it.
//
// The command is the writer. Saving runs `babel conductor configure` on this
// machine with these flags, so the validation, the incremental semantics and
// the refusals are the command's and there is no second implementation of the
// two numbers that bound autonomy. A refusal is rendered verbatim: "--per-cycle
// 9.00 is above --per-day 5.00, which would refuse every cycle" names the
// number the machine objected to, and a paraphrase would not.
//
// Absent is not zero. A machine that has never stated ceilings has none — not
// ceilings of nothing — and the conductor refuses to run on either, so the two
// states are rendered differently: one is a blank form with the refusal it
// would meet, the other is two figures in force.
//
// A dial the operator never set still shows what the loop runs under. The
// serendipity floor of one in four is not a blank; it is the default the
// scheduler obeys, which is the figure that matters when reading what the
// machine will do tonight.

// The standing duties, in the words the command's own flags use. Each is an
// authorization rather than a schedule: turning one on grants no authority a
// cycle did not already have — same profile, same ceilings, same read-only
// corpus — and what it authorizes is that the work may be scheduled without
// the operator asking for it each time.
// A standing duty is keyed by the field the wire carries it under, which is
// the flag name the command takes: one name for the checkbox, the request and
// the stored document, so a duty cannot be rendered under one key and sent
// under another.
type DutyKey = "babel_improves_babel" | "babel_tunes_itself" | "babel_triages_the_queue";

const DUTIES: Array<{ key: DutyKey; label: string; help: string }> = [
  {
    key: "babel_improves_babel",
    label: "Babel improves Babel",
    help: "Cycles whose subject is this product may be drawn without being asked for. Their output is proposals like any other: suggestions, never side effects.",
  },
  {
    key: "babel_tunes_itself",
    label: "Babel tunes itself",
    help: "Cycles that re-read recorded facts against fresh archive state, which is how a stale answer about your own deployment gets noticed.",
  },
  {
    key: "babel_triages_the_queue",
    label: "Babel triages the queue",
    help: "Babel may form attributed judgements about records you have not ruled on. Without it, review work is refused — including a review started by hand from Watch.",
  },
];

// The editable dials, kept as text so an empty field is distinguishable from a
// zero. An empty field is not sent at all, and the machine's stored setting
// decides — which is the difference between "the operator asked for one cycle
// in three" and "the operator said nothing about the floor".
interface Draft {
  perCycle: string;
  perDay: string;
  floor: string;
  interval: string;
  duties: Record<DutyKey, boolean>;
}

function draftOf(settings: Ceilings): Draft {
  return {
    perCycle: settings.per_cycle == null ? "" : String(settings.per_cycle),
    perDay: settings.per_day == null ? "" : String(settings.per_day),
    floor: settings.serendipity_floor > 0 ? String(settings.serendipity_floor) : "",
    interval: durationText(settings.interval_seconds),
    duties: {
      babel_improves_babel: settings.babel_improves_babel,
      babel_tunes_itself: settings.babel_tunes_itself,
      babel_triages_the_queue: settings.babel_triages_the_queue,
    },
  };
}

function SpendCeilingsSection() {
  const [settings, setSettings] = useState<Ceilings | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getWatchCeilings()
      .then((value) => {
        setSettings(value);
        setDraft(draftOf(value));
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft || !settings) return;
    const body: CeilingRequest = {};
    // A field the operator filled in is a flag the command gets, at the value
    // in the box. A field he cleared names nothing, so what is stored stands:
    // `conductor configure` is incremental, and a form that posted zeros for
    // its blanks would withdraw dials nobody touched.
    const perCycle = Number(draft.perCycle.trim());
    if (draft.perCycle.trim() && Number.isFinite(perCycle)) body.per_cycle = perCycle;
    const perDay = Number(draft.perDay.trim());
    if (draft.perDay.trim() && Number.isFinite(perDay)) body.per_day = perDay;
    const floor = Number(draft.floor.trim());
    if (draft.floor.trim() && Number.isFinite(floor)) body.floor = floor;
    if (draft.interval.trim()) body.interval = draft.interval.trim();
    // A duty travels only when it changed, because the wire carries three
    // states and a checkbox carries two: sending "off" for a duty the
    // operator never looked at would withdraw an authorization he gave.
    for (const duty of DUTIES) {
      if (draft.duties[duty.key] !== settings[duty.key]) body[duty.key] = draft.duties[duty.key];
    }

    setSaving(true);
    setRefusal(null);
    try {
      const stored = await saveWatchCeilings(body);
      setSettings(stored);
      setDraft(draftOf(stored));
      setAnnouncement(
        stored.configured
          ? `Ceilings stored: ${dollars(stored.per_cycle)} a cycle, ${dollars(stored.per_day)} a day.`
          : "Stored. No ceilings are set, so the conductor will still refuse to run.",
      );
    } catch (reason) {
      // The command's own sentence about the configuration it refused: it
      // names the number that is wrong and is the whole message.
      setRefusal(errorMessage(reason));
      // The shell's banner reports a request that failed, and this one did
      // not fail: the command answered, correctly, that the configuration
      // being asked for is not one. Showing both puts the same sentence on
      // the screen twice — once beside the field that caused it, once as a
      // red bar about a broken page — so the form that can render it
      // properly takes ownership of it. The launch refusal on Watch does the
      // same with its own 409.
      if (reason instanceof APIError && (reason.status === 400 || reason.status === 409)) {
        dismissAPIError();
      }
    } finally {
      setSaving(false);
    }
  }

  const at = formatTime(settings?.configured_at);
  // What a day's ceiling buys at the per-cycle one. It is arithmetic on two
  // stated numbers rather than a new fact, and it is the sentence an operator
  // actually reasons in: "five dollars a day" means nothing until it means
  // "ten cycles".
  const cycles =
    settings?.per_cycle != null && settings.per_day != null && settings.per_cycle > 0
      ? Math.floor(settings.per_day / settings.per_cycle)
      : null;

  return (
    <div className="page-section spend-ceilings">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Autonomy</p>
          <h2>Spend ceilings</h2>
        </div>
        {settings?.configured && (
          <span className="count-label">
            {dollars(settings.per_cycle)} per cycle · {dollars(settings.per_day)} per day
          </span>
        )}
      </div>

      <p className="sr-only" role="status" aria-live="polite">
        {announcement}
      </p>

      <p className="muted spend-ceilings-blurb">
        What the loop may spend without asking. Autonomy here is budget-bounded rather than
        trust-bounded: neither ceiling has a default, and the conductor refuses to run until both
        are set. These are the same two numbers <code>babel conductor configure</code> stores, in
        the same file, and saving here runs that command on this machine.
      </p>

      {loading && !settings && (
        <div className="surface state-note">
          <span className="spinner" /> Reading the stored ceilings…
        </div>
      )}

      {error && !settings && (
        <div className="surface state-note error-state">
          <strong>The stored ceilings could not be read.</strong>
          <span>{error}</span>
          <span className="muted">
            <code>babel conductor status</code> reports the same document from a terminal.
          </span>
          <button type="button" onClick={load}>
            Try again
          </button>
        </div>
      )}

      {settings && !settings.configured && (
        <div className="surface state-note spend-ceilings-unset">
          <strong>No ceilings are set, so the conductor will not run.</strong>
          <span>
            That is the refusal <Link to="/watch">Watch</Link> shows when a loop is asked for here.
            Two amounts lift it: the most one cycle may cost, and the most a day of cycles may cost
            together. Nothing starts when they are saved.
          </span>
        </div>
      )}

      {settings?.configured && (
        <div className="surface spend-ceilings-figures">
          <div className="stat">
            <span className="stat-label">per cycle</span>
            <strong className="stat-value">{dollars(settings.per_cycle)}</strong>
            <span className="stat-note">the most one cycle may cost</span>
          </div>
          <div className="stat">
            <span className="stat-label">per day</span>
            <strong className="stat-value">{dollars(settings.per_day)}</strong>
            <span className="stat-note">
              {cycles == null
                ? "the most one UTC day of cycles may cost together"
                : `${count(cycles)} ${cycles === 1 ? "cycle" : "cycles"} at the per-cycle ceiling`}
            </span>
          </div>
          <div className="stat">
            <span className="stat-label">interval</span>
            <strong className="stat-value">{durationText(settings.interval_seconds) || ABSENT}</strong>
            <span className="stat-note">waited between cycles</span>
          </div>
          <div className="stat">
            <span className="stat-label">serendipity</span>
            <strong className="stat-value">1 in {count(settings.serendipity_floor)}</strong>
            <span className="stat-note">cycles drawn at random, whatever is queued</span>
          </div>
        </div>
      )}

      {draft && settings && (
        <form className="surface spend-ceilings-form" onSubmit={save}>
          <h3>What the loop may spend</h3>
          <div className="spend-ceilings-fields">
            <label className="spend-field">
              <span className="spend-label">Per cycle</span>
              <input
                type="number"
                min={0}
                step={0.01}
                inputMode="decimal"
                placeholder="0.50"
                autoComplete="off"
                value={draft.perCycle}
                onChange={(event) => setDraft({ ...draft, perCycle: event.target.value })}
              />
              <span className="spend-hint">
                {settings.currency || "USD"} — the most one cycle may cost. Mandatory; no default.
              </span>
            </label>
            <label className="spend-field">
              <span className="spend-label">Per day</span>
              <input
                type="number"
                min={0}
                step={0.01}
                inputMode="decimal"
                placeholder="5.00"
                autoComplete="off"
                value={draft.perDay}
                onChange={(event) => setDraft({ ...draft, perDay: event.target.value })}
              />
              <span className="spend-hint">
                {settings.currency || "USD"} — the most one UTC day of cycles may cost together.
              </span>
            </label>
            <label className="spend-field">
              <span className="spend-label">Serendipity floor</span>
              <input
                type="number"
                min={1}
                step={1}
                inputMode="numeric"
                placeholder="4"
                autoComplete="off"
                value={draft.floor}
                onChange={(event) => setDraft({ ...draft, floor: event.target.value })}
              />
              <span className="spend-hint">
                One cycle in N is drawn at random rather than from the queue, so a busy ladder
                cannot starve chance.
              </span>
            </label>
            <label className="spend-field">
              <span className="spend-label">Interval</span>
              <input
                type="text"
                placeholder="1h"
                autoComplete="off"
                value={draft.interval}
                onChange={(event) => setDraft({ ...draft, interval: event.target.value })}
              />
              <span className="spend-hint">
                How long the loop waits between cycles — <code>45m</code>, <code>1h30m</code>. The
                ceilings bound the spend; this bounds the pace.
              </span>
            </label>
          </div>

          <h3>Standing duties</h3>
          <p className="muted">
            Work Babel may schedule without being asked. Each is an authorization and not a
            schedule: a cycle it allows runs under the profile the analysis ceremony stored, inside
            the ceilings above, over the same read-only corpus.
          </p>
          <div className="spend-ceilings-duties">
            {DUTIES.map((duty) => (
              <label className="spend-duty" key={duty.key}>
                <input
                  type="checkbox"
                  checked={draft.duties[duty.key]}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      duties: { ...draft.duties, [duty.key]: event.target.checked },
                    })
                  }
                />
                <span>
                  <strong>{duty.label}</strong>
                  <span className="secondary">{duty.help}</span>
                </span>
              </label>
            ))}
          </div>

          <p className="muted spend-ceilings-saving">
            Saving stores the configuration and starts nothing. The loop runs when somebody runs
            it — <code>babel conductor run</code>, or the conductor form on Watch.
          </p>

          <button type="submit" className="primary-button" disabled={saving}>
            {saving && <span className="spinner small" />}
            {saving ? "Saving…" : "Save ceilings"}
          </button>
          {refusal && (
            <p className="inline-error spend-ceilings-refusal" role="alert">
              {refusal}
            </p>
          )}
        </form>
      )}

      {/* The rest of the document, peeled. These are dials `babel conductor
          configure` also holds and this form does not offer: they are set from
          a terminal, and hiding what they are set to would make the browser a
          partial view of a file the operator can read whole. */}
      {settings && (
        <details className="peel">
          <summary>
            Everything else this document holds
            <span className="peel-count">{settings.path ? "conductor.json" : "stored"}</span>
          </summary>
          <div className="peel-body">
            <dl className="peel-rows">
              <dt>Currency</dt>
              <dd className="mono">{settings.currency || ABSENT}</dd>
              <dt>Serendipity slice</dt>
              <dd>
                up to <span className="mono">{count(settings.slice_sessions)}</span>{" "}
                {settings.slice_sessions === 1 ? "session" : "sessions"} per random draw
              </dd>
              <dt>Consolidation</dt>
              <dd>
                {settings.consolidate_one_in > 0 ? (
                  <>
                    one cycle in <span className="mono">{count(settings.consolidate_one_in)}</span>, seeded
                    from up to <span className="mono">{count(settings.consolidate_roots)}</span> candidates
                  </>
                ) : (
                  <span className="muted">off — no cycle is reserved for turning candidates into findings</span>
                )}
              </dd>
              <dt>Evaluation</dt>
              <dd>
                {settings.evaluate_one_in > 0 ? (
                  <>
                    one cycle in <span className="mono">{count(settings.evaluate_one_in)}</span>
                  </>
                ) : (
                  <span className="muted">off — no cycle is reserved for reviewing what is already durable</span>
                )}
              </dd>
              <dt>Coverage sweep</dt>
              <dd className="mono">{settings.evaluate_cadence || ABSENT}</dd>
              <dt>Last configured</dt>
              <dd>
                {at ? (
                  <time dateTime={settings.configured_at} title={at.absolute}>
                    {at.relative}
                  </time>
                ) : (
                  <span className="muted">never</span>
                )}
              </dd>
              <dt>Stored in</dt>
              <dd className="mono">{settings.path || ABSENT}</dd>
            </dl>
            <p className="muted">
              Consolidation, the evaluation share, the coverage sweep and the serendipity slice are
              set with <code>babel conductor configure</code>; they are shown here so the browser is
              not a partial view of a file you can read whole.
            </p>
          </div>
        </details>
      )}
    </div>
  );
}

export default SpendCeilingsSection;
