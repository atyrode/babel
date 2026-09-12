import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityQuestion, type QuestionDetail } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, TimelineEntry } from "../analysis";
import {
  AnswerForm,
  FactEntry,
  PlanCard,
  answerableStates,
  classTone,
  questionStateTone,
} from "../reality";
import { Identifiers, Subjects } from "./RealityData";

// One Reality Question read whole, with the two decisions it admits offered
// beside it (SPEC.md §4.8, §8.4).
//
// §8.4's second requirement is the one this page exists for: a record shown
// without the decision it invites sends the operator to a command line. A
// question admits exactly two — an answer, kept verbatim and attributed, and
// the single explicit acceptance of an interpretation — and both are here,
// against the record they concern rather than on a separate screen.
//
// Everything else on the page is why the question exists: the facts that
// prompted it, the entities it is about, and the append-only history of every
// state it has passed through, including the refusals.
function RealityQuestionPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<QuestionDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(
    (mode: "blocking" | "quiet") => {
      if (mode === "blocking") {
        setDetail(null);
        setError(null);
      }
      getRealityQuestion(id)
        .then(setDetail)
        .catch((reason) => {
          if (mode === "blocking") setError(errorMessage(reason));
        });
    },
    [id],
  );

  useEffect(() => load("blocking"), [load]);

  if (error && !detail) {
    return (
      <section className="page">
        <Link className="back-link" to="/ask/questions">← Asked</Link>
        <div className="surface state-note error-state">
          <strong>This question could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="surface state-note"><span className="spinner" /> Loading question…</div>
      </section>
    );
  }

  const question = detail.question;
  const created = formatTime(question.created_at);
  const answerable = answerableStates.includes(question.state);
  const onChanged = (message: string) => {
    setAnnouncement(message);
    load("quiet");
  };

  return (
    <section className="page detail-page question-detail-page">
      <Link className="back-link" to="/ask/questions">← Asked</Link>
      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={question.state} tone={questionStateTone(question.state)} />
            <Badge label={question.class} tone={classTone(question.class)} />
            <span className="kind-label">{question.kind}</span>
            {question.sensitivity !== "routine" && (
              <Badge label={question.sensitivity} tone="red" />
            )}
          </div>
          <Quoted label="Question — generated from analysis, untrusted" text={question.prompt} />
          {/* The identifier used to be the subtitle under the prompt. It is
              under a disclosure at the foot of the page now, with the rest
              of the machinery: it is what a link resolves and what a
              command takes, and it has never been what the question says. */}
        </div>
        <div className="heading-meta">
          {question.pending && <span className="count-label">waiting on you</span>}
          {created && <span className="secondary" title={created.absolute}>asked {created.relative}</span>}
        </div>
      </div>

      <article className="surface">
        <p className="eyebrow">Why it was asked</p>
        <p className="untrusted-inline">{question.why_asked}</p>
        <Subjects
          ids={question.target_entity_ids}
          names={question.about_name}
        />
        {detail.predicates.length > 0 && (
          <p className="secondary">
            Predicates: {detail.predicates.map((predicate) => (
              <span className="tag mono" key={predicate}>{predicate}</span>
            ))}
          </p>
        )}
        {detail.material_evidence.length > 0 && (
          <p className="secondary">
            {/* §4.8 lifts the suppression of a declined question only when
                materially new evidence exists, and this is the set that is
                measured against. It is shown because a reader deciding
                whether to re-ask needs to know what the last ask already
                knew. */}
            Asked on the strength of:{" "}
            {detail.material_evidence.map((item) => (
              <span className="tag mono untrusted-inline" key={item}>{item}</span>
            ))}
          </p>
        )}
      </article>

      {(detail.existing_facts.length > 0 || detail.conflict_facts.length > 0) && (
        <article className="surface">
          <div className="section-heading">
            <div>
              <p className="eyebrow">What prompted it</p>
              <h2>Facts behind the question</h2>
            </div>
          </div>
          {detail.existing_facts.length > 0 && (
            <>
              <p className="muted">
                What the ledger already held — suspected of drift, or in need of refreshing.
              </p>
              <div className="fact-list">
                {detail.existing_facts.map((fact) => <FactEntry key={fact.id} fact={fact} />)}
              </div>
            </>
          )}
          {detail.conflict_facts.length > 0 && (
            <>
              <p className="muted">
                Revisions that contradict one another. §4.8 records the disagreement rather than
                letting the newest write win, which is why an operator is being asked at all.
              </p>
              <div className="fact-list">
                {detail.conflict_facts.map((fact) => <FactEntry key={fact.id} fact={fact} />)}
              </div>
            </>
          )}
        </article>
      )}

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Provenance</p>
            <h2>Answers</h2>
          </div>
          <span className="count-label">{detail.answers.length}</span>
        </div>
        {detail.answers.length === 0 ? (
          <p className="muted">
            Nobody has answered yet. An answer is kept exactly as it is typed and attributed to
            whoever gave it; nothing reads it except the Answer Interpreter.
          </p>
        ) : (
          <div className="answer-list">
            {detail.answers.map((answer) => {
              const at = formatTime(answer.at);
              return (
                <div key={answer.id}>
                  <Quoted
                    label={`Operator answer — ${answer.author}, kept verbatim · ${answer.outcome}`}
                    text={answer.text}
                  />
                  {at && <p className="secondary" title={at.absolute}>answered {at.relative}</p>}
                </div>
              );
            })}
          </div>
        )}
        {answerable && <AnswerForm questionId={question.id} onChanged={onChanged} />}
        {!answerable && detail.answers.length === 0 && (
          <p className="inline-state muted">
            This question's state accepts no answer. §4.8's state machine decides that, not this
            page: a refused question stays refused until materially new evidence justifies asking
            again.
          </p>
        )}
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Interpretation</p>
            <h2>Plans</h2>
          </div>
          <span className="count-label">{detail.plans.length}</span>
        </div>
        {detail.plans.length === 0 ? (
          <p className="muted">
            No interpretation has been recorded. An answer becomes a plan through the versioned
            Answer Interpreter, and the plan changes nothing until it is accepted here.
          </p>
        ) : (
          detail.plans.map((plan) => <PlanCard key={plan.id} plan={plan} onChanged={onChanged} />)
        )}
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only</p>
            <h2>History</h2>
          </div>
          <span className="count-label">{detail.history.length}</span>
        </div>
        <p className="muted">
          Every state this question has been in, oldest first. Nothing here is ever rewritten, so
          a deferral and the decision that reversed it are both still readable.
        </p>
        <ol className="timeline">
          {detail.history.map((event) => (
            <TimelineEntry
              key={event.id}
              badge={event.state}
              tone={questionStateTone(event.state)}
              at={event.recorded_at}
            >
              <span className="secondary">{event.actor}</span>
              {event.note && <span className="untrusted-inline">{event.note}</span>}
            </TimelineEntry>
          ))}
        </ol>
      </article>

      <Identifiers
        rows={[
          ["Question", question.id],
          ...detail.targets.map((target): [string, string] => [
            target.display_name || "Subject",
            target.id,
          ]),
        ]}
      />
    </section>
  );
}

export default RealityQuestionPage;
