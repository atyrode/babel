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
import "./ask.css";

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

// ANSWER_OUTCOMES are the three things an answer can be (§4.8), each with the
// one sentence that says what recording it does next.
//
// They were a `<select>` whose first option read "answered — send to the
// interpreter", which hid two of the three behind a click and made the
// consequence of each — interpretation, closure, suppression — a phrase the
// reader had to open a menu to find. They are three acts now, and the sentence
// for the one he pressed is printed in the panel it unfolds.
//
// The table is exported because the feed row offers the same three (§8.4 asks
// for the decision where the record is read, and a question is read on the
// front page first). The row says `short` because a listing has no room for a
// verb phrase; everything that states a consequence — the note, the verb on
// the button, the past tense on the receipt — is written once here, because two
// wordings of one permanent act is one wording that is wrong.
export const ANSWER_OUTCOMES: {
  value: string;
  label: string;
  short: string;
  note: string;
  verb: string;
  busy: string;
  done: string;
}[] = [
  {
    value: "answered",
    label: "Answer it",
    short: "Answer",
    note:
      "Kept verbatim and attributed to you, then read by the Answer Interpreter. What it proposes " +
      "changes nothing until you accept the plan here.",
    verb: "Record answer",
    busy: "Recording…",
    done: "answered",
  },
  {
    value: "unknown",
    label: "I don't know",
    short: "I don't know",
    note:
      "Closes the question with nothing to interpret, and stops Babel asking it again until " +
      "materially new evidence turns up.",
    verb: "Record that you don't know",
    busy: "Recording…",
    done: "recorded that you don't know",
  },
  {
    value: "declined",
    label: "Stop asking",
    short: "Stop asking",
    note:
      "Refuses the question. It stays on the record, visibly declined, and is suppressed until " +
      "materially new evidence justifies asking again.",
    verb: "Decline the question",
    busy: "Declining…",
    done: "declined",
  },
];

// AnswerForm is §4.8's answer, offered where the question is read — and it is
// the question's rule bar, because a question is a post and its acts stand
// where a record's rulings do (§8.7).
//
// The three outcomes are the bar; the words go in the panel that unfolds under
// the one he pressed, through the same fold a ruling's confirmation uses. It
// used to be a bar with a textarea and a submit button permanently open under
// it, which put a 200-pixel form on every open question whether or not the
// reader had decided to answer one — and made "I don't know" look like a thing
// you type an answer into.
export function AnswerForm({
  questionId,
  onChanged,
}: {
  questionId: string;
  onChanged: (message: string) => void;
}) {
  const [text, setText] = useState("");
  // Which outcome's panel is open, and nothing is open until he presses one:
  // an answer is an attributed, append-only act, so the act begins with a
  // deliberate press rather than with a box that was already there.
  const [outcome, setOutcome] = useState<string>("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const chosen = ANSWER_OUTCOMES.find((entry) => entry.value === outcome);
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
      setOutcome("");
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <div className="record-acts">
        <div className="rule-bar" role="group" aria-label="Answer this question">
          {ANSWER_OUTCOMES.map((entry) => (
            <button
              type="button"
              key={entry.value}
              data-outcome={entry.value}
              className={entry.value === outcome ? "active" : undefined}
              aria-expanded={entry.value === outcome}
              title={entry.note}
              onClick={() => setOutcome(entry.value === outcome ? "" : entry.value)}
            >
              {entry.label}
            </button>
          ))}
        </div>
        <span className="record-acts-label">kept verbatim · attributed to you</span>
      </div>
      {chosen && (
        <div className="record-confirm-fold">
          <form className="answer-form record-confirm" onSubmit={submit}>
            <p>{chosen.note}</p>
            <label>
              {substantive ? "Your answer" : "Why, if you want to say (optional)"}
              <textarea
                value={text}
                onChange={(event) => setText(event.target.value)}
                rows={3}
                autoFocus
                placeholder={
                  substantive
                    ? "Answered text is retained verbatim and attributed to you."
                    : "Kept verbatim beside the outcome, for whoever reads this question next."
                }
              />
            </label>
            <div className="record-confirm-acts">
              <button
                type="submit"
                className="primary-button"
                disabled={submitting || (substantive && !text.trim())}
              >
                {submitting && <span className="spinner small" />}
                {submitting ? chosen.busy : chosen.verb}
              </button>
              <button type="button" onClick={() => setOutcome("")} disabled={submitting}>
                Cancel
              </button>
            </div>
            {submitError && <p className="inline-error" role="alert">{submitError}</p>}
          </form>
        </div>
      )}
    </>
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

      {/* The actions are the plan's machinery. The summary above and the
          acceptance below are what a reader decides on; the list of what
          would change is opened when he wants to check it, which keeps a
          plan-ready question from being a screen and a half on its own. */}
      <details className="peel plan-actions">
        <summary>
          What it would do
          <span className="peel-count">{plan.actions.length}</span>
          <span className="muted">
            {mutating.length} {mutating.length === 1 ? "applies" : "apply"} on acceptance
            {retained.length > 0 ? ` · ${retained.length} retained regardless` : ""}
          </span>
        </summary>
        <div className="peel-body">
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
        </div>
      </details>

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
