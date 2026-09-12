import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getRealityInbox, type QuestionSummary } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted } from "../analysis";
import {
  AnswerEntry,
  AnswerForm,
  PlanCard,
  answerableStates,
  classTone,
  questionStateTone,
} from "../reality";
import { Identifiers, ScoreBreakdown, Subjects } from "./RealityData";

// The §4.8 question inbox: what the ledger is asking that only the operator can
// answer, ranked by §4.8's five factors.
//
// It is deliberately not the list of every question. A snoozed question was
// deferred, a declined one was refused, and an answered one is done; putting
// them here would make the inbox the list of everything rather than the list of
// what to do. They are read on Questions instead, which is the sibling page
// §8.4 required: a record that leaves this page must still be reachable.
const INBOX_PAGE = 6;

function RealityPage() {
  const [items, setItems] = useState<QuestionSummary[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  // The inbox is answered one question at a time, and each card carries a
  // form; six of them is already a page. The rest stay one click away rather
  // than pushing the page past §8.6's ceiling.
  const [shown, setShown] = useState(INBOX_PAGE);

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
          <h1>What it needs</h1>
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
        <div className="surface state-note"><span className="spinner" /> Reading the inbox…</div>
      )}
      {error && !items && (
        <div className="surface state-note error-state">
          <strong>The Reality inbox could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load("blocking")}>Try again</button>
        </div>
      )}
      {items && items.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing is waiting on you</strong>
          <span>
            Analysis has nothing it needs to ask. Questions appear here when exploration hits
            missing, stale, or conflicting knowledge about your systems — and every question
            ever asked, answered or not, stays readable under{" "}
            <Link to="/ask/questions">Asked</Link>.
          </span>
        </div>
      )}

      {items && items.length > 0 && (
        <div className="question-list">
          {items.slice(0, shown).map((question) => (
            <QuestionCard
              key={question.id}
              question={question}
              onChanged={(message) => {
                setAnnouncement(message);
                load("quiet");
              }}
            />
          ))}
          {items.length > shown && (
            <p className="muted">
              {items.length - shown} more, most useful first.{" "}
              <button type="button" className="link-button" onClick={() => setShown((n) => n + INBOX_PAGE)}>
                Show the next {Math.min(INBOX_PAGE, items.length - shown)}
              </button>
            </p>
          )}
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

  return (
    <article className="surface">
      <div className="question-heading">
        <Badge label={question.class} tone={classTone(question.class)} />
        <Badge label={question.state} tone={questionStateTone(question.state)} />
        <span className="kind-label">{question.kind}</span>
        {question.sensitivity !== "routine" && (
          <Badge label={question.sensitivity} tone="red" />
        )}
        {created && (
          <time dateTime={question.created_at} title={created.absolute}>{created.relative}</time>
        )}
        {/* The way to this question's own page, where its whole history, the
            facts that prompted it and every answer it has ever had are read.
            The card is the inbox's working view; the page is the record. It
            is reached by a sentence rather than by the identifier it used to
            print, which was the only readable thing on the row and said
            nothing. */}
        <Link className="question-open" to={`/ask/questions/${encodeURIComponent(question.id)}`}>
          Read it whole
        </Link>
      </div>

      <Quoted label="Question — generated from analysis, untrusted" text={question.prompt} />
      <p className="why-asked muted">Why asked: <span className="untrusted-inline">{question.why_asked}</span></p>

      <Subjects ids={question.target_entity_ids} names={question.about_name} />

      {/* The arithmetic behind this card's position in the queue. It is a
          disclosure rather than a number in the heading because the score
          only matters when the order is being argued with, and that is the
          moment the factors have to be there. */}
      <ScoreBreakdown question={question} />

      {question.answers.length > 0 && (
        <div className="answer-list">
          {question.answers.map((answer) => (
            <AnswerEntry key={answer.id} answer={answer} />
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

      <Identifiers rows={[["Question", question.id]]} />
    </article>
  );
}

export default RealityPage;
