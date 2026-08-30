import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  acceptPlan,
  answerQuestion,
  getRealityInbox,
  type PlanAcceptResult,
  type PlanView,
  type QuestionSummary,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, type Tone } from "../analysis";

// The §4.8 action kinds that mutate reality and therefore apply only on the
// operator's explicit plan acceptance. Everything else is a non-authoritative
// descendant retained immediately.
const AUTHORITATIVE_KINDS: Record<string, true> = {
  "assert-fact": true,
  "supersede-fact": true,
  "dispute-fact": true,
  "merge-entities": true,
  "split-entity": true,
  "change-focus-policy": true,
};

function classTone(value: string): Tone {
  if (value === "blocking") return "amber";
  if (value === "curiosity") return "cyan";
  return "neutral";
}

function questionStateTone(state: string): Tone {
  switch (state) {
    case "open":
      return "violet";
    case "answered-uninterpreted":
    case "interpreting":
      return "amber";
    case "plan-ready":
      return "cyan";
    case "answered":
      return "green";
    default:
      return "neutral";
  }
}

function RealityPage() {
  const [items, setItems] = useState<QuestionSummary[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback((mode: "blocking" | "quiet") => {
    if (mode === "blocking") {
      setLoading(true);
      setError(null);
    }
    getRealityInbox()
      .then((value) => setItems(value.items))
      .catch((reason) => {
        if (mode === "blocking") setError(errorMessage(reason));
      })
      .finally(() => {
        if (mode === "blocking") setLoading(false);
      });
  }, []);

  useEffect(() => load("blocking"), [load]);

  return (
    <section className="page reality-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Reality Ledger</p>
          <h1>Reality</h1>
          <p className="subtitle">
            Prioritized questions about the operator's world. Answers are kept verbatim;
            interpreted plans change nothing until explicitly accepted.
          </p>
        </div>
        {items && (
          <div className="heading-meta">
            <span className="count-label">
              {items.length} {items.length === 1 ? "question" : "questions"}
            </span>
          </div>
        )}
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {loading && !items && (
        <div className="state-card"><span className="spinner" /> Reading the inbox…</div>
      )}
      {error && !items && (
        <div className="state-card error-state">
          <strong>The Reality inbox could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load("blocking")}>Try again</button>
        </div>
      )}
      {items && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>No open questions</strong>
          <span>
            Analysis has nothing it needs to ask. Questions appear here when exploration hits
            missing, stale, or conflicting knowledge about your systems.
          </span>
        </div>
      )}

      {items && items.length > 0 && (
        <div className="question-list">
          {items.map((question) => (
            <QuestionCard
              key={question.id}
              question={question}
              onChanged={(message) => {
                setAnnouncement(message);
                load("quiet");
              }}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function QuestionCard({
  question,
  onChanged,
}: {
  question: QuestionSummary;
  onChanged: (message: string) => void;
}) {
  const created = formatTime(question.created_at);
  const answerable = ["open", "snoozed"].includes(question.state);
  const terms = Object.entries(question.terms).filter(([, value]) => value !== 0);

  return (
    <article className="card question-card">
      <div className="question-heading">
        <Badge label={question.class} tone={classTone(question.class)} />
        <Badge label={question.state} tone={questionStateTone(question.state)} />
        <span className="kind-label">{question.kind}</span>
        {question.sensitivity !== "routine" && (
          <Badge label={question.sensitivity} tone="red" />
        )}
        <details className="rank-disclosure">
          <summary>rank score {question.score}</summary>
          <div className="rank-terms">
            <p className="muted">
              Attention ranking only — the factors are shown so the ordering can be argued with.
            </p>
            {terms.map(([factor, value]) => (
              <span className="tag" key={factor}>{factor} {value > 0 ? `+${value}` : value}</span>
            ))}
          </div>
        </details>
        {created && (
          <time dateTime={question.created_at} title={created.absolute}>{created.relative}</time>
        )}
      </div>

      <Quoted label="Question — generated from analysis, untrusted" text={question.prompt} />
      <p className="why-asked muted">Why asked: <span className="untrusted-inline">{question.why_asked}</span></p>

      {question.target_entity_ids.length > 0 && (
        <p className="question-targets">
          About:{" "}
          {question.target_entity_ids.map((entityID, index) => (
            <span key={entityID}>
              {index > 0 && ", "}
              <Link className="mono" to={`/reality/entities/${encodeURIComponent(entityID)}`}>
                {entityID}
              </Link>
            </span>
          ))}
        </p>
      )}

      {question.answers.length > 0 && (
        <div className="answer-list">
          {question.answers.map((answer) => (
            <Quoted
              key={answer.id}
              label={`Operator answer — ${answer.author}, kept verbatim · ${answer.outcome}`}
              text={answer.text}
            />
          ))}
        </div>
      )}

      {question.state === "answered-uninterpreted" && (
        <p className="inline-state muted">
          The answer is retained verbatim and awaits the Answer Interpreter. Nothing becomes a
          fact until a plan is shown here and explicitly accepted.
        </p>
      )}

      {question.plans.length === 0 && question.answers.length > 0 && question.state !== "answered-uninterpreted" && (
        <p className="inline-state muted">No interpretation yet.</p>
      )}

      {question.plans.map((plan) => (
        <PlanCard key={plan.id} plan={plan} onChanged={onChanged} />
      ))}

      {answerable && <AnswerForm question={question} onChanged={onChanged} />}
    </article>
  );
}

function AnswerForm({
  question,
  onChanged,
}: {
  question: QuestionSummary;
  onChanged: (message: string) => void;
}) {
  const [text, setText] = useState("");
  const [outcome, setOutcome] = useState("answered");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!text.trim() && outcome === "answered") return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const result = await answerQuestion(question.id, text, outcome);
      onChanged(`Answer recorded. The question is now ${result.state}.`);
      setText("");
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="answer-form" onSubmit={submit}>
      <label>
        Your answer
        <textarea
          value={text}
          onChange={(event) => setText(event.target.value)}
          rows={3}
          placeholder="Answered text is retained verbatim and attributed to you."
        />
      </label>
      <div className="answer-actions">
        <label className="outcome-select">
          Outcome
          <select value={outcome} onChange={(event) => setOutcome(event.target.value)}>
            <option value="answered">answered — send to the interpreter</option>
            <option value="unknown">unknown — I don't know</option>
            <option value="declined">declined — stop asking this</option>
          </select>
        </label>
        <button
          type="submit"
          className="primary-button"
          disabled={submitting || (outcome === "answered" && !text.trim())}
        >
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : "Record answer"}
        </button>
      </div>
      {submitError && <p className="inline-error" role="alert">{submitError}</p>}
    </form>
  );
}

function PlanCard({
  plan,
  onChanged,
}: {
  plan: PlanView;
  onChanged: (message: string) => void;
}) {
  const [accepting, setAccepting] = useState(false);
  const [acceptError, setAcceptError] = useState<string | null>(null);
  const [acceptResult, setAcceptResult] = useState<PlanAcceptResult | null>(null);

  const mutating = plan.actions.filter((action) => AUTHORITATIVE_KINDS[action.kind]);
  const retained = plan.actions.filter((action) => !AUTHORITATIVE_KINDS[action.kind]);
  const proposed = plan.state === "proposed";

  async function accept() {
    const summary = mutating.map((action) => action.kind).join(", ") || "no mutations";
    const prompt =
      `Accept this plan?\n\nThis applies ${mutating.length} reality ` +
      `${mutating.length === 1 ? "mutation" : "mutations"} (${summary}) atomically with the `
      + "question's disposition. Acceptance is recorded and cannot be un-recorded.";
    if (!window.confirm(prompt)) return;
    setAccepting(true);
    setAcceptError(null);
    try {
      const result = await acceptPlan(plan.id);
      setAcceptResult(result);
      onChanged(`Plan accepted. Applied ${result.applied.length} changes; question is now ${result.state}.`);
    } catch (reason) {
      setAcceptError(errorMessage(reason));
    } finally {
      setAccepting(false);
    }
  }

  return (
    <div className={proposed ? "plan-card proposed" : "plan-card"}>
      <div className="question-heading">
        <Badge
          label={proposed ? "proposed — nothing applied yet" : plan.state}
          tone={proposed ? "amber" : plan.state === "accepted" ? "green" : plan.state === "rejected" ? "red" : "neutral"}
        />
        <span className="kind-label">interpreter v{plan.interpreter_version}</span>
        <span className="mono event-index">{plan.id}</span>
      </div>
      <Quoted label="Interpreter summary — model text, untrusted" text={plan.summary} />

      <ol className="action-list">
        {plan.actions.map((action) => {
          const authoritative = AUTHORITATIVE_KINDS[action.kind] ?? false;
          const applied = formatTime(action.applied_at);
          const { rationale, ...detail } = action.payload;
          const options = Object.entries(detail).filter(([, value]) => value != null);
          return (
            <li className="action-entry" key={action.id}>
              <div className="action-heading">
                <Badge label={action.kind} tone={authoritative ? "amber" : "neutral"} />
                {action.state === "applied" ? (
                  <Badge label="applied" tone="green" />
                ) : authoritative ? (
                  <span className="action-state amber-text">applies only on acceptance</span>
                ) : (
                  <span className="action-state muted">
                    {action.state === "retained" ? "retained immediately" : action.state}
                  </span>
                )}
                {applied && action.state === "applied" && (
                  <span className="secondary" title={applied.absolute}>{applied.relative}</span>
                )}
                {action.result_id && <span className="mono secondary">{action.result_id}</span>}
              </div>
              <p className="action-rationale untrusted-inline">{rationale}</p>
              {options.length > 0 && (
                <details className="json-disclosure">
                  <summary>Proposed change</summary>
                  <pre>{JSON.stringify(Object.fromEntries(options), null, 2)}</pre>
                </details>
              )}
            </li>
          );
        })}
      </ol>

      {proposed && !acceptResult && (
        <div className="accept-panel">
          <div>
            <strong>Acceptance is one explicit act.</strong>
            <p className="muted">
              {mutating.length > 0
                ? `${mutating.length} ${mutating.length === 1 ? "mutation applies" : "mutations apply"} atomically on acceptance; `
                : "This plan proposes no reality mutations; "}
              {retained.length > 0
                ? `${retained.length} non-authoritative ${retained.length === 1 ? "descendant is" : "descendants are"} retained regardless.`
                : "and it retains no descendants."}
            </p>
          </div>
          <button type="button" className="primary-button" onClick={accept} disabled={accepting}>
            {accepting && <span className="spinner small" />}
            {accepting ? "Applying…" : "Accept plan"}
          </button>
        </div>
      )}
      {acceptError && <p className="inline-error" role="alert">Acceptance failed: {acceptError}</p>}
      {acceptResult && (
        <div className="result-panel success-panel" role="status">
          <strong>Plan accepted and applied atomically</strong>
          <dl>
            {acceptResult.applied.map((ref) => (
              <div key={`${ref.kind}-${ref.id}`}>
                <dt>{ref.kind}</dt>
                <dd className="mono">{ref.id}</dd>
              </div>
            ))}
            <div>
              <dt>Question</dt>
              <dd>{acceptResult.state}</dd>
            </div>
          </dl>
        </div>
      )}
    </div>
  );
}

export default RealityPage;
