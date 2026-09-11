import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getRealityInbox, type QuestionSummary } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted } from "../analysis";
import {
  AnswerForm,
  PlanCard,
  answerableStates,
  classTone,
  questionStateTone,
} from "../reality";

// The §4.8 question inbox: what the ledger is asking that only the operator can
// answer, ranked by §4.8's five factors.
//
// It is deliberately not the list of every question. A snoozed question was
// deferred, a declined one was refused, and an answered one is done; putting
// them here would make the inbox the list of everything rather than the list of
// what to do. They are read on Questions instead, which is the sibling page
// §8.4 required: a record that leaves this page must still be reachable.
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
          <h1>Questions</h1>
          <p className="subtitle">
            What Babel needs you to tell it, most useful first. Answers are kept verbatim;
            interpreted plans change nothing until explicitly accepted.
          </p>
        </div>
        {items && (
          <div className="heading-meta">
            <span className="count-label">
              {items.length} waiting on you
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
          <strong>Nothing is waiting on you</strong>
          <span>
            Analysis has nothing it needs to ask. Questions appear here when exploration hits
            missing, stale, or conflicting knowledge about your systems — and every question
            ever asked, answered or not, stays readable under{" "}
            <Link to="/reality/questions">Asked</Link>.
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
  const answerable = answerableStates.includes(question.state);
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
        {/* The way to this question's own page, where its whole history, the
            facts that prompted it and every answer it has ever had are read.
            The card is the inbox's working view; the page is the record. */}
        <Link className="mono event-index" to={`/reality/questions/${encodeURIComponent(question.id)}`}>
          {question.id}
        </Link>
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

      {answerable && <AnswerForm questionId={question.id} onChanged={onChanged} />}
    </article>
  );
}

export default RealityPage;
