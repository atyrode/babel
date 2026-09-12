import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  acceptPlan,
  answerQuestion,
  type EntityRef,
  type FactView,
  type PlanAcceptResult,
  type PlanView,
} from "./api";
import { errorMessage, formatTime } from "./format";
import { Badge, Quoted, type Tone } from "./analysis";

// Shared vocabulary for the Reality Ledger's pages (SPEC.md §4.8, §8.4).
//
// It lives here rather than on any one page because §8.4 made the ledger
// reachable from several directions at once: a question is read from the inbox
// and from the listing of every question ever asked, a fact is read from an
// entity's page and from its own, and the decisions each admits must be offered
// identically wherever it is read. Two copies of the answer form would be two
// wordings of the same act, and the second one to change would be a lie.
//
// Nothing here writes a fact. The two controls are the two acts §4.8 gives an
// operator over a model's proposals — retaining an answer, and the one explicit
// acceptance that lets an interpretation touch reality — and both call the
// routes that already existed.

// AUTHORITATIVE_KINDS are the §4.8 action kinds that mutate reality and
// therefore apply only on the operator's explicit acceptance. Everything else
// is a non-authoritative descendant retained immediately.
export const AUTHORITATIVE_KINDS: Record<string, true> = {
  "assert-fact": true,
  "supersede-fact": true,
  "dispute-fact": true,
  "merge-entities": true,
  "split-entity": true,
  "change-focus-policy": true,
};

export function classTone(value: string): Tone {
  if (value === "blocking") return "amber";
  if (value === "curiosity") return "cyan";
  return "neutral";
}

export function questionStateTone(state: string): Tone {
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
    case "declined":
    case "obsolete":
    case "superseded":
      return "red";
    default:
      return "neutral";
  }
}

// factTone colours a revision's status. Superseded and stale are neutral
// rather than red: a corrected fact and an expired one are the append-only
// ledger working, not a problem. Disputed is red because nobody has decided.
export function factTone(status: string): Tone {
  switch (status) {
    case "active":
      return "green";
    case "proposed":
      return "amber";
    case "disputed":
      return "red";
    case "stale":
      return "violet";
    default:
      return "neutral";
  }
}

// answerableStates are the states in which the answer form is offered. They
// are the states §4.8's machine accepts an answer from: an open question, and
// one the operator deferred and has come back to.
export const answerableStates = ["open", "snoozed"];

// EntityName renders a reference to an entity as the name a reader knows it
// by, linking to the page that holds it. An identifier the ledger could not
// name still renders — as the identifier — because a record must stay readable
// when something it points at has gone.
export function EntityName({ entity, current }: { entity: EntityRef; current?: string }) {
  const label = entity.display_name || entity.id;
  if (entity.id === current) return <span className="untrusted-inline">{label}</span>;
  return (
    <Link className="untrusted-inline" to={`/ask/entities/${encodeURIComponent(entity.id)}`}>
      {label}
    </Link>
  );
}

// FactValue renders what a fact asserts. An entity-valued fact points at the
// entity rather than printing its identifier, because "contains the repository
// Babel" is the claim and "contains ent_7f3a" is a lookup task.
export function FactValue({ fact }: { fact: FactView }) {
  if (fact.value.object_id) {
    return (
      <span className="fact-value">
        <EntityName entity={{ id: fact.value.object_id }} />
      </span>
    );
  }
  const text = fact.value.enum ?? fact.value.text;
  if (!text) return <span className="fact-value muted">—</span>;
  return <span className="fact-value untrusted-inline">{text}</span>;
}

// FactEntry is one revision as it appears in a list. The heading is a link to
// the revision's own page, which is where the chain it sits in is readable:
// §8.4 asks for the record to be reachable, and a fact's ancestors are part of
// the record.
export function FactEntry({ fact }: { fact: FactView }) {
  const observed = formatTime(fact.observed_at);
  const expires = formatTime(fact.expires_at);
  return (
    <div className={`fact-entry status-${fact.status}`}>
      <div className="fact-heading">
        <Badge label={fact.status} tone={factTone(fact.status)} />
        <Link className="fact-predicate mono" to={`/ask/facts/${encodeURIComponent(fact.id)}`}>
          {fact.predicate}
        </Link>
        <FactValue fact={fact} />
      </div>
      <p className="fact-meta secondary">
        authority {fact.authority.kind}
        {fact.authority.id && <span className="mono"> {fact.authority.id}</span>}
        {" · confidence "}{fact.confidence}
        {observed && <span title={observed.absolute}> · observed {observed.relative}</span>}
        {expires && <span title={expires.absolute}> · freshness expires {expires.relative}</span>}
        {fact.supersedes && (
          <>
            {" · supersedes "}
            <Link className="mono" to={`/ask/facts/${encodeURIComponent(fact.supersedes)}`}>
              {fact.supersedes}
            </Link>
          </>
        )}
      </p>
      {fact.note && <p className="fact-note untrusted-inline">{fact.note}</p>}
    </div>
  );
}

// AnswerForm is §4.8's answer, offered where the question is read. It takes an
// identifier rather than a record so that the inbox card and the question's own
// page offer the same control over the same act.
export function AnswerForm({
  questionId,
  onChanged,
}: {
  questionId: string;
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
      const result = await answerQuestion(questionId, text, outcome);
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

// PlanCard shows an interpretation and, while it is still proposed, offers the
// single explicit acceptance that applies it.
export function PlanCard({
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
    <div className={proposed ? "surface plan-inset proposed" : "surface plan-inset"}>
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
