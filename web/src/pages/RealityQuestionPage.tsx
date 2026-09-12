import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityQuestion, type QuestionDetail } from "../api";
import { Badge, TimelineEntry, unescapeWhitespace } from "../analysis";
import RenderBoundary from "../boundary";
import { getTopics, type TopicRow } from "../feedapi";
import { errorMessage, formatTime } from "../format";
import {
  AnswerEntry,
  AnswerForm,
  FactEntry,
  PlanCard,
  answerableStates,
  questionStateTone,
} from "../reality";
import { LONG_CLAIM, Peel, Prose } from "../record";
import { Identifiers } from "./RealityData";
import "../record.css";

// A question is a post (§8.7), so it is read on the page every other post is
// read on.
//
// This was a surface of its own: three tabs above it ("What you said / Who and
// what / What it believes"), a boxed quotation for the question, and five
// stacked panels — why, facts, answers, plans, history — each with an eyebrow
// and a heading of its own. The operator's account of the difference was that
// a question Babel asks is the same kind of thing as a proposal Babel makes:
// something he reads and acts on, in one list, on one shape of page. So the
// shape here is the record's: the question's own words as the serif headline,
// then the depths.
//
//   depth 1  the claim      why it was asked, what it is about, what class of
//                           question it is — and the acts: answer it, say you
//                           don't know, stop being asked.
//   depth 3  the evidence   the records it was asked on the strength of, and
//                           the facts it suspects of drift.
//   depth 5  the machinery  the identifiers, and every state it has been in.
//
// Depths 2 and 4 are absent because a question has neither: it makes no case
// and receives no reviewer assessment. An absent depth is absent rather than
// an empty heading, which is the same rule the record page keeps.
//
// The answers and the plans are the thread under the post. That is what they
// are: what was said in reply, verbatim and attributed, and the interpretation
// of it that changes nothing until it is accepted.

// The three depths this page has, as the keys that open them. It is the
// record's `1`-`5` with the two a question does not carry left out, rather
// than renumbered to `1`-`3`: the depth number is what the reader learned on
// the record page, and a question whose evidence was at `2` would be teaching
// him a second numbering for the same idea.
const DEPTH_KEYS: Record<string, number> = { "1": 0, "3": 1, "5": 2 };

function RealityQuestionPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<QuestionDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  // The topics, so that "about ent_atlas" can say "about t/atlas" and land on
  // the topic's own page. A subject that is not a topic keeps its entity page,
  // which is the same page with no filings under it.
  const [topics, setTopics] = useState<TopicRow[]>([]);
  // Depths 1 and 3 open, the machinery folded: the same choice the record
  // makes, for the same reason — the claim and what it rests on are what the
  // reader came for, and digging into identifiers is a decision.
  const [open, setOpen] = useState<boolean[]>([true, true, false]);

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

  useEffect(() => {
    let live = true;
    getTopics()
      .then((answer) => {
        if (live) setTopics(answer.topics ?? []);
      })
      // The rail's read failing is not this page's failure: the subject then
      // reads as the entity it is, which is where it always linked.
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);

  // Contract K's depth keys, for the depths this page has.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable='true']")) return;
      const depth = DEPTH_KEYS[event.key];
      if (depth === undefined) return;
      event.preventDefault();
      setOpen((current) => current.map((value, index) => (index === depth ? !value : value)));
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

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
  const long = question.prompt.length > LONG_CLAIM;
  const evidence = detail.material_evidence.length + detail.existing_facts.length +
    detail.conflict_facts.length;
  const onChanged = (message: string) => {
    setAnnouncement(message);
    load("quiet");
  };
  const setDepth = (depth: number, value: boolean) =>
    setOpen((current) => current.map((entry, index) => (index === depth ? value : entry)));

  return (
    <section className="page">
      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {/* The post. The question's own words are the headline — they are the
          record here, not a name for one — so they wear the quoted frame and
          the editorial face, exactly as a hypothesis's statement does. */}
      <header className="surface record-post question-post">
        <div className="record-post-body">
          <div className="heading-badges">
            <Badge label="Question" tone="neutral" />
            <Badge label={question.state} tone={questionStateTone(question.state)} />
            {/* Sensitivity is the one thing on this page that changes how the
                words may be handled, so it stays a badge; the class is text at
                depth 1, where the reader is deciding. */}
            {question.sensitivity !== "routine" && <Badge label={question.sensitivity} tone="red" />}
          </div>
          <h1 className={`quote untrusted-inline record-claim${long ? " long" : ""}`}>
            {unescapeWhitespace(question.prompt)}
          </h1>
          <p className="record-post-meta">
            {question.pending && <span className="question-waiting">waiting on you</span>}
            {created && (
              <time dateTime={question.created_at} title={created.absolute}>
                asked {created.relative}
              </time>
            )}
          </p>
        </div>
      </header>

      <RenderBoundary key={id}>
        <div className="surface">
          <Peel title="The claim" open={open[0]} onToggle={(value) => setDepth(0, value)}>
            <Prose label="Why it was asked" text={question.why_asked} />
            <About
              ids={question.target_entity_ids}
              names={question.about_name}
              topics={topics}
            />
            {/* What kind of question it is, as words. It was two coloured
                badges — the class and the ledger's own noun for it — which
                made "blocking" and "acquire-context" the loudest things on a
                page whose subject is a sentence. */}
            <p className="question-class">
              {question.class} · {question.kind}
            </p>

            {answerable ? (
              <AnswerForm questionId={question.id} onChanged={onChanged} />
            ) : (
              <p className="record-standing">
                This question's state accepts no answer. §4.8's state machine decides that, not
                this page: a refused question stays refused until materially new evidence
                justifies asking again.
              </p>
            )}
          </Peel>

          {evidence > 0 && (
            <Peel
              title="The evidence"
              count={evidence}
              open={open[1]}
              onToggle={(value) => setDepth(1, value)}
            >
              {detail.material_evidence.length > 0 && (
                <div className="record-field">
                  <h3 className="eyebrow">Asked on the strength of</h3>
                  {/* The records themselves, as links. §4.8 lifts the
                      suppression of a declined question only when materially
                      new evidence exists, and this is the set that is measured
                      against — so a reader deciding whether to re-ask has to
                      be able to open what the last ask already knew. */}
                  <ul className="record-field-list question-strength">
                    {detail.material_evidence.map((item) => (
                      <li key={item}>
                        <Link className="mono" to={`/r/${encodeURIComponent(item)}`}>
                          {item}
                        </Link>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {detail.existing_facts.length > 0 && (
                <div className="record-field">
                  <h3 className="eyebrow">What the ledger already held</h3>
                  <p className="muted">Suspected of drift, or in need of refreshing.</p>
                  <div className="fact-list">
                    {detail.existing_facts.map((fact) => <FactEntry key={fact.id} fact={fact} />)}
                  </div>
                </div>
              )}
              {detail.conflict_facts.length > 0 && (
                <div className="record-field">
                  <h3 className="eyebrow">Revisions that contradict one another</h3>
                  <p className="muted">
                    §4.8 records the disagreement rather than letting the newest write win, which
                    is why an operator is being asked at all.
                  </p>
                  <div className="fact-list">
                    {detail.conflict_facts.map((fact) => <FactEntry key={fact.id} fact={fact} />)}
                  </div>
                </div>
              )}
            </Peel>
          )}

          <Peel
            title="The machinery"
            open={open[2]}
            onToggle={(value) => setDepth(2, value)}
          >
            <Identifiers
              rows={[
                ["Question", question.id],
                ...detail.targets.map((target): [string, string] => [
                  target.display_name || "Subject",
                  target.id,
                ]),
              ]}
            />
            {detail.predicates.length > 0 && (
              <p className="secondary">
                Predicates:{" "}
                {detail.predicates.map((predicate) => (
                  <span className="tag mono" key={predicate}>{predicate}</span>
                ))}
              </p>
            )}
            <div className="record-field">
              <h3 className="eyebrow">Every state it has been in</h3>
              <p className="muted">
                Oldest first. Nothing here is ever rewritten, so a deferral and the decision that
                reversed it are both still readable.
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
            </div>
          </Peel>

          <p className="record-keys">
            <span>
              <kbd className="kbd">1</kbd>
              <kbd className="kbd">3</kbd>
              <kbd className="kbd">5</kbd> depth
            </span>
          </p>
        </div>

        {/* The thread: what was said in reply, and what an interpreter made of
            it. Under the post, like every other thread (§8.7). */}
        <section className="surface record-thread" id="answers">
          <h2>
            {detail.answers.length === 1 ? "1 answer" : `${detail.answers.length} answers`}
          </h2>
          {detail.answers.length === 0 ? (
            <p className="muted">
              Nobody has answered yet. An answer is kept exactly as it is typed and attributed to
              whoever gave it; nothing reads it except the Answer Interpreter.
            </p>
          ) : (
            <div className="answer-list">
              {detail.answers.map((answer) => <AnswerEntry key={answer.id} answer={answer} />)}
            </div>
          )}

          {detail.plans.length > 0 && (
            <>
              <h2 className="question-plans-heading">
                {detail.plans.length === 1 ? "1 interpretation" : `${detail.plans.length} interpretations`}
              </h2>
              {detail.plans.map((plan) => (
                <PlanCard key={plan.id} plan={plan} onChanged={onChanged} />
              ))}
            </>
          )}
        </section>
      </RenderBoundary>
    </section>
  );
}

// About says what the question is about, and links it where the reader can act
// on it: a subject something has been filed under is a topic and reaches
// /t/<name>; a subject nothing has been filed under is still an entity and
// reaches its own page, which renders through the same component.
function About({
  ids,
  names,
  topics,
}: {
  ids: string[];
  names?: string[];
  topics: TopicRow[];
}) {
  if (ids.length === 0) return null;
  return (
    <p className="question-about">
      about{" "}
      {ids.map((id, index) => {
        const topic = topics.find((row) => row.id === id);
        return (
          <span key={id}>
            {index > 0 && <span aria-hidden="true"> · </span>}
            {topic ? (
              <Link className="mono" to={`/t/${encodeURIComponent(topic.name)}`}>
                t/{topic.name}
              </Link>
            ) : (
              <Link
                className={names?.[index] ? "untrusted-inline" : "mono"}
                to={`/ask/entities/${encodeURIComponent(id)}`}
              >
                {names?.[index] || id}
              </Link>
            )}
          </span>
        );
      })}
    </p>
  );
}

export default RealityQuestionPage;
