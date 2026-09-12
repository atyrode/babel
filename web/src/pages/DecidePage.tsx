import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  getEvaluationList,
  getRealityInbox,
  getReviewQueue,
  type EvaluationItem,
  type QuestionSummary,
  type QueueItem,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, PartialListNotice, reviewTone } from "../analysis";
import { kindLabel } from "../evaluation";
import { answerableStates } from "../reality";
import { SteeringSection } from "../steering";

// Decide answers one question: what needs me?
//
// It replaces four surfaces that each held part of the answer — the review
// queue, the dashboard's review inbox, the ledger's question inbox, and the
// reconsiderations buried in the evaluation backlog's `reconsider` lane. They
// were four destinations because they come from four stores, which is Babel's
// problem and was never the reader's: an operator with something to rule on
// does not know, and must not have to know, which store is holding it.
//
// So there is one queue of mixed kinds. Each row is one line of the record's
// own claim and at most three facts, because a row is for deciding whether to
// open the thing, not for deciding the thing. Everything else is a peel down
// on the record's own page.
//
// The header is three numbers. It is what is left of the dashboard, which was
// a six-panel grid summarizing five other pages: a page that reports on other
// pages is a page that has nothing of its own to say.

const PAGE_SIZE = 20;

// How many reconsiderations to draw. They are rare — a reconsideration is
// something changing about a record already decided — so one page of them is
// the whole set in practice, and drawing more would cost a request to render
// rows below the fold of a queue whose point is the top of it.
const RECONSIDER_LIMIT = 25;

// One row of the merged queue, flattened from whichever store produced it.
//
// The ordering rule lives in `rank`, and it is deliberately a small integer
// rather than a score: this queue is not ranked by a policy, it is grouped by
// how stuck Babel is without an answer, and a decimal would invite the reader
// to argue with a precision that does not exist.
interface Row {
  key: string;
  rank: number;
  claim: string;
  href: string;
  // At most three, enforced where they are built rather than where they are
  // rendered, so a row that grows a fourth fact fails review here.
  facts: Fact[];
  at: string;
}

interface Fact {
  label: string;
  tone?: "badge" | "text";
  badgeTone?: "neutral" | "green" | "amber" | "red" | "violet" | "blue" | "cyan";
  title?: string;
}

function DecidePage() {
  const [params, setParams] = useSearchParams();
  const [queue, setQueue] = useState<QueueItem[] | null>(null);
  const [queueTotal, setQueueTotal] = useState(0);
  const [degraded, setDegraded] = useState(false);
  const [questions, setQuestions] = useState<QuestionSummary[] | null>(null);
  const [reconsider, setReconsider] = useState<EvaluationItem[] | null>(null);
  const [reconsiderTotal, setReconsiderTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const page = Math.max(0, Number(params.get("page") ?? 0) || 0);

  // Three reads, three stores, and a failure in one must not blank the other
  // two: an operator whose ledger is unreachable still has records enrolled
  // for a ruling, and a page that refused to show them would be reporting the
  // ledger's outage as an empty inbox. Only a total failure is an error.
  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    const reviews = getReviewQueue({});
    const inbox = getRealityInbox();
    const changed = getEvaluationList({
      lane: "reconsider",
      sort: "reconsider",
      limit: RECONSIDER_LIMIT,
    });
    Promise.allSettled([reviews, inbox, changed])
      .then(([reviewed, asked, reopened]) => {
        if (reviewed.status === "fulfilled") {
          setQueue(reviewed.value.items ?? []);
          setQueueTotal(reviewed.value.total ?? reviewed.value.items?.length ?? 0);
          setDegraded(reviewed.value.sync_degraded === true);
        } else {
          setQueue(null);
        }
        if (asked.status === "fulfilled") {
          setQuestions(
            (asked.value.items ?? []).filter((item) => answerableStates.includes(item.state)),
          );
        } else {
          setQuestions(null);
        }
        if (reopened.status === "fulfilled") {
          setReconsider(reopened.value.items ?? []);
          setReconsiderTotal(reopened.value.total ?? 0);
        } else {
          setReconsider(null);
        }
        if (
          reviewed.status === "rejected" &&
          asked.status === "rejected" &&
          reopened.status === "rejected"
        ) {
          setError(errorMessage(reviewed.reason));
        }
      })
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  const rows = useMemo(
    () => merge(queue ?? [], questions ?? [], reconsider ?? []),
    [queue, questions, reconsider],
  );

  const shown = rows.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const pages = Math.ceil(rows.length / PAGE_SIZE);

  function turn(next: number) {
    const query = new URLSearchParams(params);
    if (next > 0) query.set("page", String(next));
    else query.delete("page");
    setParams(query);
  }

  return (
    <section className="page decide-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Your queue</p>
          <h1>What needs me?</h1>
        </div>
      </div>

      {/* Three numbers, one sentence each. The sentence says what the number
          is about, not how Babel derived it. */}
      <div className="tally">
        <Tally
          count={queue === null ? null : queueTotal}
          label="awaiting a ruling"
          sentence="Records Babel developed far enough to ask you about."
        />
        <Tally
          count={questions === null ? null : questions.length}
          label="questions for you"
          sentence="Things only you can answer, so Babel stopped guessing."
        />
        <Tally
          count={reconsider === null ? null : reconsiderTotal}
          label="worth reconsidering"
          sentence="Records you already decided that something has changed about."
        />
      </div>

      {/* #115's capture box rides this surface by operator decision
          (2026-08-31), above the queue rather than below it: the operator who
          came to decide is the operator with something to say. */}
      <SteeringSection />

      {degraded && <PartialListNotice />}

      {loading && rows.length === 0 && (
        <div className="surface state-note"><span className="spinner" /> Reading what is waiting…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>Nothing could be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && rows.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing awaits a decision</strong>
          <span>
            Records arrive here when exploration develops them far enough to be worth a ruling.{" "}
            <Link to="/read">Read what Babel has found</Link> in the meantime.
          </span>
        </div>
      )}

      {rows.length > 0 && (
        <>
          <p
            className="muted queue-order"
            title={
              "Questions Babel is blocked on come first, then decisions something has changed " +
              "about, then records enrolled for a ruling, oldest first. Curiosities are last."
            }
          >
            Blocked first, then what changed, then what has waited longest.
          </p>
          <ol className="queue">
            {shown.map((row) => (
              <QueueRow row={row} key={row.key} />
            ))}
          </ol>
        </>
      )}

      {pages > 1 && (
        <div className="pager surface">
          <button type="button" disabled={page === 0} onClick={() => turn(page - 1)}>
            ← Previous
          </button>
          <span className="muted">
            {(page * PAGE_SIZE + 1).toLocaleString()}–
            {Math.min(page * PAGE_SIZE + shown.length, rows.length).toLocaleString()} of{" "}
            {rows.length.toLocaleString()}
          </span>
          <button type="button" disabled={page + 1 >= pages} onClick={() => turn(page + 1)}>
            Next →
          </button>
        </div>
      )}
    </section>
  );
}

function Tally({
  count,
  label,
  sentence,
}: {
  count: number | null;
  label: string;
  sentence: string;
}) {
  return (
    <div className="tally-item">
      {/* A store that did not answer says so. A zero here would claim nothing
          is waiting, which is a different thing from not having looked. */}
      <span className="tally-count">
        {count === null ? <span className="not-observed" title={sentence}>unread</span> : count.toLocaleString()}
      </span>
      <span className="tally-label">{label}</span>
      <span className="tally-sentence">{sentence}</span>
    </div>
  );
}

function QueueRow({ row }: { row: Row }) {
  return (
    <li className="queue-row">
      <Link className="queue-claim untrusted-inline" to={row.href}>
        {row.claim}
      </Link>
      <span className="queue-facts">
        {row.facts.map((fact) =>
          fact.tone === "badge" ? (
            <Badge label={fact.label} tone={fact.badgeTone ?? "neutral"} key={fact.label} />
          ) : (
            <span className="queue-fact" title={fact.title} key={fact.label}>
              {fact.label}
            </span>
          ),
        )}
      </span>
    </li>
  );
}

// merge flattens three stores into one order.
//
// Rank 0 is a question Babel is blocked on: it has stopped rather than guessed,
// and every other row is work that is merely waiting. Rank 1 is a decision the
// operator already made that something has changed about — cheap to rule on,
// because he has read the record before. Rank 2 is the enrolled queue. Rank 3
// is a question that is not blocking anything, which is the only group here
// that is genuinely optional.
//
// Within a rank the oldest is first, on the ordinary grounds that a queue
// nobody drains from the bottom is a queue with a permanent bottom.
function merge(
  queue: QueueItem[],
  questions: QuestionSummary[],
  reconsider: EvaluationItem[],
): Row[] {
  const rows: Row[] = [];

  for (const item of questions) {
    const asked = formatTime(item.created_at);
    const blocking = item.class === "blocking";
    rows.push({
      key: `q-${item.id}`,
      rank: blocking ? 0 : 3,
      claim: item.prompt || "a question with no prompt recorded",
      href: `/ask/questions/${encodeURIComponent(item.id)}`,
      facts: [
        { label: "Question", tone: "badge", badgeTone: blocking ? "amber" : "cyan" },
        ...(asked
          ? [{ label: `asked ${asked.relative}`, tone: "text" as const, title: asked.absolute }]
          : []),
      ],
      at: item.created_at,
    });
  }

  for (const item of reconsider) {
    const revised = formatTime(item.artifact.created_at);
    rows.push({
      key: `x-${item.artifact.subject.kind}-${item.artifact.subject.id}`,
      rank: 1,
      claim: item.artifact.title || "a record with no title recorded",
      href: `/r/${encodeURIComponent(item.artifact.subject.id)}`,
      facts: [
        { label: kindLabel(item.artifact.subject.kind), tone: "badge" },
        { label: "something changed", tone: "text", title: item.reasons?.[0] },
        ...(revised
          ? [{ label: revised.relative, tone: "text" as const, title: revised.absolute }]
          : []),
      ],
      at: item.artifact.created_at,
    });
  }

  for (const item of queue) {
    const enrolled = formatTime(item.enrolled_at);
    // A merged row arrives without the append-only decision history, which is
    // derived beside the record and does not travel with it. Its standing is
    // absent rather than "new": a record decided on another host is not an
    // undecided one.
    const derived = item.local_host !== false;
    rows.push({
      key: `r-${item.subject.type}-${item.subject.id}`,
      rank: 2,
      claim: item.excerpt || `a ${item.subject.type} with no summary recorded`,
      href: `/r/${encodeURIComponent(item.subject.id)}`,
      facts: [
        { label: kindLabel(item.subject.type), tone: "badge" },
        ...(derived && item.status && item.status !== "new"
          ? [{
              label: item.status,
              tone: "badge" as const,
              badgeTone: reviewTone(item.status) as Fact["badgeTone"],
            }]
          : []),
        ...(enrolled
          ? [{
              label: `waiting ${enrolled.relative}`,
              tone: "text" as const,
              title: enrolled.absolute,
            }]
          : []),
      ],
      at: item.enrolled_at,
    });
  }

  rows.sort((left, right) => {
    if (left.rank !== right.rank) return left.rank - right.rank;
    return left.at.localeCompare(right.at);
  });
  return rows;
}

export default DecidePage;
