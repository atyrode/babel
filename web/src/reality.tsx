import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  acceptPlan,
  answerQuestion,
  type AnswerView,
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
// entity rather than printing its identifier, because "contains the
// repository Babel" is the claim and "contains ent_7f3a" is a lookup task.
// The name travels with the value from the server; an object the ledger can
// no longer name still renders, as its identifier, because a claim must stay
// readable when something it points at has gone.
export function FactValue({ fact }: { fact: FactView }) {
  if (fact.value.object_id) {
    return (
      <span className="fact-value">
        <EntityName
          entity={{ id: fact.value.object_id, display_name: fact.value.object_name }}
        />
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

// AnswerEntry is one recorded answer, wherever it is read.
//
// An answer with no text is not an empty quotation. `unknown` and `declined`
// are answers in themselves — §4.8 stores them as outcomes precisely so that
// the ledger stops asking — and the operator who gives one owes no prose. The
// quoted frame is for words that were typed; when none were, the outcome is
// stated as a sentence instead of framing nothing.
export function AnswerEntry({ answer }: { answer: AnswerView }) {
  const at = formatTime(answer.at);
  const spoken = answer.text.trim().length > 0;
  return (
    <div>
      {spoken ? (
        <Quoted
          label={`Operator answer — ${answer.author}, kept verbatim · ${answer.outcome}`}
          text={answer.text}
        />
      ) : (
        <p className="answer-bare">
          {answer.author}{" "}
          {answer.outcome === "declined"
            ? "declined this question"
            : "answered that they do not know"}
          , and left no note.
        </p>
      )}
      {at && <p className="secondary" title={at.absolute}>answered {at.relative}</p>}
    </div>
  );
}

// OUTCOMES are the three things an answer can be (§4.8), each with the one
// sentence that says what recording it does next.
//
// They were a `<select>` whose first option read "answered — send to the
// interpreter", which hid two of the three behind a click and made the
// consequence of each — interpretation, closure, suppression — a phrase the
// reader had to open a menu to find. They are three segments of one bar now,
// because they are one decision with three answers, and the sentence for the
// segment under the cursor or the keyboard is printed under it.
const OUTCOMES: { value: string; label: string; note: string; verb: string; busy: string }[] = [
  {
    value: "answered",
    label: "Answer it",
    note:
      "Kept verbatim and attributed to you, then read by the Answer Interpreter. What it proposes " +
      "changes nothing until you accept the plan here.",
    verb: "Record answer",
    busy: "Recording…",
  },
  {
    value: "unknown",
    label: "I don't know",
    note:
      "Closes the question with nothing to interpret, and stops Babel asking it again until " +
      "materially new evidence turns up.",
    verb: "Record that you don't know",
    busy: "Recording…",
  },
  {
    value: "declined",
    label: "Stop asking",
    note:
      "Refuses the question. It stays on the record, visibly declined, and is suppressed until " +
      "materially new evidence justifies asking again.",
    verb: "Decline the question",
    busy: "Declining…",
  },
];

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
  // What the reader is pointing at, which is not what they have chosen. The
  // note under the bar follows the pointer or the focus ring and falls back
  // to the chosen segment, so reading what an outcome would do never costs
  // the choice already made.
  const [previewed, setPreviewed] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const chosen = OUTCOMES.find((entry) => entry.value === outcome) ?? OUTCOMES[0];
  const shown = OUTCOMES.find((entry) => entry.value === previewed) ?? chosen;
  const substantive = outcome === "answered";

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!text.trim() && substantive) return;
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
      <div className="answer-outcome">
        <span className="answer-outcome-label" id={`outcome-${questionId}`}>
          What your answer is
        </span>
        <div className="rule-bar" role="group" aria-labelledby={`outcome-${questionId}`}>
          {OUTCOMES.map((entry) => (
            <button
              type="button"
              key={entry.value}
              aria-pressed={entry.value === outcome}
              onClick={() => setOutcome(entry.value)}
              onMouseEnter={() => setPreviewed(entry.value)}
              onMouseLeave={() => setPreviewed(null)}
              onFocus={() => setPreviewed(entry.value)}
              onBlur={() => setPreviewed(null)}
            >
              {entry.label}
            </button>
          ))}
        </div>
        <p className="answer-outcome-note">{shown.note}</p>
      </div>
      <label>
        {substantive ? "Your answer" : "Why, if you want to say (optional)"}
        <textarea
          value={text}
          onChange={(event) => setText(event.target.value)}
          rows={3}
          placeholder={
            substantive
              ? "Answered text is retained verbatim and attributed to you."
              : "Kept verbatim beside the outcome, for whoever reads this question next."
          }
        />
      </label>
      <div className="answer-actions">
        <button
          type="submit"
          className="primary-button"
          disabled={submitting || (substantive && !text.trim())}
        >
          {submitting && <span className="spinner small" />}
          {submitting ? chosen.busy : chosen.verb}
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
