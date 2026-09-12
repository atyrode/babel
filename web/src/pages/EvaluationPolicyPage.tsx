import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  getEvaluationPolicy,
  saveEvaluationPolicy,
  type EvaluationPolicy,
  type EvaluationPolicyResponse,
  type EvaluationStatus,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, type Tone } from "../analysis";
import { CoverageSummary } from "../evaluation";

// The review policy and its budget (SPEC.md §5.8, §8.5).
//
// Two requirements from §8.5 are the whole design of this page. The first is
// that the operator can see whether authorized evaluation work is running,
// awaiting its next scheduled draw, paused, or unavailable — four states, and
// the page must be able to tell "Babel is not reviewing" apart from "this
// session cannot say". The second is that saving a policy is not permission to
// launch compute, which is why the save button is a save button, the
// consequence sentence is the server's own and is shown before the save as
// well as after it, and there is no start control anywhere on this surface.
//
// Every number here bounds spend. None of them causes it: the schedule the
// deployment already runs is what draws work, and a budget raised on a paused
// policy still draws nothing.

const STATUS_LABELS: Record<EvaluationStatus, string> = {
  running: "Running",
  scheduled: "Awaiting its next scheduled draw",
  paused: "Paused",
  unavailable: "Unavailable",
};

const STATUS_TONES: Record<EvaluationStatus, Tone> = {
  running: "green",
  scheduled: "cyan",
  paused: "amber",
  unavailable: "red",
};

// The policy fields that are numbers. `enabled` is a checkbox and `version`
// is the service's own, so neither is editable as a knob — and typing the
// table this way is what stops one being added to it by accident, which
// would render a version string as a number input.
type NumericPolicyKey = {
  [Key in keyof EvaluationPolicy]: EvaluationPolicy[Key] extends number ? Key : never;
}[keyof EvaluationPolicy];

// One editable knob. `help` is what the number actually bounds, because a
// field called `cooldown_seconds` with no sentence beside it is a number the
// operator sets by guessing.
interface Knob {
  key: NumericPolicyKey;
  label: string;
  help: string;
  step?: number;
  unit?: string;
}

const SELECTION_KNOBS: Knob[] = [
  {
    key: "cadence_seconds",
    label: "Coverage check cadence",
    help: "How often a coverage sweep looks for new, unfinished and overdue work. The sweep finding work is not the same as work being done about it.",
    unit: "seconds",
  },
  {
    key: "overdue_seconds",
    label: "Initial review is overdue after",
    help: "How long an eligible artifact may go without its first review before the reserved allocation prioritises it.",
    unit: "seconds",
  },
  {
    key: "initial_reviews",
    label: "Initial reviews per artifact",
    help: "How many independent assessments an artifact needs before reception is treated as established.",
  },
  {
    key: "max_item_reviews",
    label: "Maximum reviews per artifact",
    help: "The ceiling on repeated attention to one revision, so a settled opinion cannot absorb the budget.",
  },
  {
    key: "cooldown_seconds",
    label: "Cooldown at stable reception",
    help: "How long an artifact with a stable, unanimous reception rests before it is eligible again.",
    unit: "seconds",
  },
];

const SHARE_KNOBS: Knob[] = [
  {
    key: "coverage_share",
    label: "Reserved for oldest-due coverage",
    help: "The share of each draw reserved for initial reviews that are overdue, so unpopular work still gets read.",
    step: 0.01,
  },
  {
    key: "exploration_share",
    label: "Reserved for exploration",
    help: "A positive share that deliberately does not follow the weighting, so the selection cannot collapse onto what it already prefers.",
    step: 0.01,
  },
  {
    key: "discovery_share",
    label: "Protected discovery",
    help: "The share protected for newly discovered artifacts, kept apart from the weighted allocation.",
    step: 0.01,
  },
];

const BUDGET_KNOBS: Knob[] = [
  {
    key: "per_cycle_cost",
    label: "Ceiling per cycle",
    help: "The most one draw may reserve. It is a ceiling, not an allocation: nothing is spent because it is available.",
    step: 0.01,
    unit: "USD",
  },
  {
    key: "daily_cost",
    label: "Ceiling per day",
    help: "The deployment-wide daily ceiling across every participating worker, accounted once rather than per machine.",
    step: 0.01,
    unit: "USD",
  },
  {
    key: "batch_size",
    label: "Assignments per draw",
    help: "How many assignments one draw may hand out at once.",
  },
  {
    key: "lease_seconds",
    label: "Assignment lease",
    help: "How long a claimed assignment stays claimed before another worker may take it. An expired lease does not make its spend free.",
    unit: "seconds",
  },
];

function EvaluationPolicyPage() {
  const [data, setData] = useState<EvaluationPolicyResponse | null>(null);
  const [draft, setDraft] = useState<EvaluationPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getEvaluationPolicy()
      .then((value) => {
        setData(value);
        // The form starts from what is stored rather than from defaults of
        // its own, and a knob this build does not render still travels back
        // on the save: a partial write would silently zero it.
        setDraft(value.policy);
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft) return;
    setSaving(true);
    setFailure(null);
    try {
      const result = await saveEvaluationPolicy(draft);
      setData(result);
      setDraft(result.policy);
      // The server's consequence sentence is announced as it is served —
      // "Policy eval-policy-2 stored. the policy is stored." was two claims
      // about the same write, one of them mid-sentence.
      setAnnouncement(`Policy ${result.policy.version}: ${result.saving}`);
    } catch (reason) {
      setFailure(errorMessage(reason));
    } finally {
      setSaving(false);
    }
  }

  function edit(key: NumericPolicyKey, raw: string) {
    // The cast is the computed-key widening and nothing else: NumericPolicyKey
    // is exactly the set of number-valued fields, so the result is a policy.
    setDraft((current) => (current ? ({ ...current, [key]: Number(raw) } as EvaluationPolicy) : current));
  }

  const nextDraw = formatTime(data?.next_draw);
  const status = data?.status ?? "unavailable";

  return (
    // A section of Settings rather than a page of its own: this is what
    // authorized evaluation work is allowed to cost. Saving it starts
    // nothing, which is the server's own sentence and is rendered verbatim
    // beside the form.
    <div className="page-section policy-section">

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {loading && !data && (
        <div className="surface state-note"><span className="spinner" /> Reading the review policy…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>The review policy could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}

      {data && (
        <div className="surface policy-status" data-status={status}>
          <div className="evaluation-row-marks">
            <Badge label={STATUS_LABELS[status]} tone={STATUS_TONES[status]} />
            <span className="secondary mono">policy {data.policy.version || "unversioned"}</span>
          </div>
          {/* The server's sentence, not a paraphrase. Whether work is
              running is observed from claimed assignments rather than
              inferred from the policy being enabled, and only the surface
              that made that observation can say so honestly. */}
          <p className="untrusted-inline">{data.detail}</p>
          {nextDraw && (
            <p className="secondary">
              Next scheduled draw <span title={nextDraw.absolute}>{nextDraw.relative}</span>.
            </p>
          )}
          {!nextDraw && status === "scheduled" && (
            <p className="secondary">
              No coverage check has been recorded yet, so when the next draw falls is not known
              from this session. It is not "now".
            </p>
          )}
        </div>
      )}

      {data && <CoverageSummary coverage={data.coverage} />}

      {draft && data && (
        <form className="surface evaluation-policy-form" onSubmit={save}>
          <h2>What review work may cost</h2>

          <label className="evaluation-enabled">
            <input
              type="checkbox"
              checked={draft.enabled}
              onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })}
            />
            <span>
              <strong>Authorize evaluation work</strong>
              <span className="secondary">
                Off means no draw happens at all. On means the schedule may draw within the
                ceilings below — it does not mean a run starts when you save.
              </span>
            </span>
          </label>

          <fieldset>
            <legend>Selection</legend>
            <p className="muted">
              How attention is chosen. Reserved coverage goes to the oldest initial reviews still
              owed, so an unpopular record is read; the weighted remainder favours lightly
              reviewed revisions and backs away from settled opinions at both extremes.
            </p>
            <KnobFields knobs={SELECTION_KNOBS} policy={draft} onEdit={edit} />
          </fieldset>

          <fieldset>
            <legend>Shares</legend>
            <p className="muted">
              Fractions of one draw. They are named separately because each protects something
              the weighting would otherwise starve; a positive exploration share is deliberate
              rather than slack.
            </p>
            <KnobFields knobs={SHARE_KNOBS} policy={draft} onEdit={edit} />
          </fieldset>

          <fieldset>
            <legend>Budget</legend>
            <p className="muted">
              One allowance for the whole deployment, not one per machine: concurrent workers
              account against the same ceiling, and an expired reservation does not become
              unspent.
            </p>
            <KnobFields knobs={BUDGET_KNOBS} policy={draft} onEdit={edit} />
          </fieldset>

          {/* Stated before the save, not only after it. The operator is
              about to press a button on a form with a dollar figure in it,
              and the honest moment to say what it does not do is the moment
              before he presses it. */}
          <p className="muted evaluation-saving">{data.saving}</p>

          <button type="submit" className="primary-button" disabled={saving}>
            {saving && <span className="spinner small" />}
            {saving ? "Saving…" : "Save policy"}
          </button>
          {failure && <p className="inline-error" role="alert">{failure}</p>}
        </form>
      )}

      {data?.record && (
        <div className="surface">
          <h2>Recorded</h2>
          <p className="secondary">
            Your change is an attributed record, not a settings blob:{" "}
            <span className="mono">{data.record.id}</span>, by{" "}
            <span className="mono">{data.record.actor_id || "unattributed"}</span>.
          </p>
        </div>
      )}
    </div>
  );
}

function KnobFields({
  knobs,
  policy,
  onEdit,
}: {
  knobs: Knob[];
  policy: EvaluationPolicy;
  onEdit: (key: NumericPolicyKey, value: string) => void;
}) {
  return (
    <div className="evaluation-knobs">
      {knobs.map((knob) => (
        <label key={knob.key}>
          <span>
            {knob.label}
            {knob.unit && <span className="secondary"> ({knob.unit})</span>}
          </span>
          <input
            type="number"
            data-knob={knob.key}
            min={0}
            step={knob.step ?? 1}
            value={String(policy[knob.key])}
            onChange={(event) => onEdit(knob.key, event.target.value)}
          />
          <span className="secondary">{knob.help}</span>
        </label>
      ))}
    </div>
  );
}

export default EvaluationPolicyPage;
