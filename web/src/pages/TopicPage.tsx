import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, Navigate, useParams } from "react-router-dom";
import { Badge } from "../analysis";
import {
  getComplaints,
  getRealityEntity,
  tellComplaint,
  type ComplaintSummary,
  type EntityDetail,
} from "../api";
import {
  getTopics,
  INTEREST_LABEL,
  INTEREST_MEANS,
  INTEREST_STATES,
  setTopicInterest,
  type InterestState,
  type TopicRow,
  type TopicsResponse,
} from "../feedapi";
import { errorMessage, formatTime } from "../format";
import FeedPage from "./FeedPage";
import "../topic.css";

// One topic: what it is, where the operator stands toward it, and the feed
// narrowed to it (§4.13).
//
// The page is the feed with a header, not a second listing. Everything below
// the header is FeedPage — the same sorts, the same chips, the same rows and
// the same pagination, narrowed by the same route parameter — because a topic
// is *the same feed narrowed to one community* and a page that re-implemented
// the list would be the second surface §8.7 spent this wave deleting.
//
// Two things live here and nowhere else:
//
//   - The stance. §4.13 records the operator's interest as attributed
//     lifecycle and analysis-policy facts on the entity, offered "on the
//     topic's own page", and this is that page. It is the one act on a topic
//     that is still the operator's own: stating where he stands is a fact
//     about the world rather than a change to Babel's.
//   - The asks. Retiring, splitting and merging go *through* Babel by
//     operator direction (2026-09-12): the form records what he wants in his
//     own words, Babel's next filing run answers with a proposal, and he
//     rules on that proposal like any other. There is no button here that
//     rewrites the ledger, because a topic change is a judgement Babel has to
//     make its case for.
//
// No path is printed. A binding's identity can be the common directory every
// worktree of a repository shares, and §4.13 is explicit that a locator is
// evidence about a topic and never the topic — so the row carries the remote
// and a count of checkouts, and the paths themselves are in the fold.

// It also serves /ask/entities/:id, because an entity and a topic are one
// thing (§4.13: a topic *is* a Reality Ledger entity). A subject something has
// been filed under, or the operator has said where he stands on, is a topic
// and redirects to the name it is read under; a subject neither is true of is
// rendered here as what it is — its kind, its binding, the stance control, and
// a feed with nothing in it yet. There is no second page about a subject: the
// one that existed was a fact timeline nobody reached from the reading path,
// and what the ledger believes is read under Ask's beliefs, which the identity
// fold links to.
export default function TopicPage() {
  const routed = useParams();
  // Which route this is. `/t/:topic` names a topic and `/ask/entities/:id`
  // names an entity; they resolve to the same row and differ only in what the
  // page does when the row turns out to be a topic with filings.
  const entityID = (routed.id ?? "").trim();
  const [answer, setAnswer] = useState<TopicsResponse | null>(null);
  const [failed, setFailed] = useState(false);
  // What the ledger calls an entity nothing is filed under. It is read only on
  // the entity route, and only when the topics do not already hold the row:
  // the name and the kind are the two things the header needs and the topics
  // read does not carry for an unfiled subject.
  const [subject, setSubject] = useState<EntityDetail | null>(null);

  const load = useCallback(() => {
    getTopics()
      .then((next) => {
        setAnswer(next);
        setFailed(false);
      })
      .catch(() => setFailed(true));
  }, []);

  useEffect(load, [load]);

  useEffect(() => {
    if (!entityID) return;
    let live = true;
    getRealityEntity(entityID)
      .then((value) => {
        if (live) setSubject(value);
      })
      // An entity the ledger cannot open is not a page-level failure: the
      // header says nothing answers to the name, which is what is true.
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [entityID]);

  // The row is found by name because the name is what the reader typed and
  // what every link carries; the id is accepted too, so a link built from an
  // entity id opens the page it names.
  const asked = entityID || (routed.topic ?? "").trim();
  const row = (answer?.topics ?? []).find((entry) => entry.name === asked || entry.id === asked);

  // A subject with filings under it, or a stance stated on it, is a topic and
  // is read under its name: the entity id is an identifier, and a reader who
  // followed one from a question should land where the topic lives rather than
  // on a second URL for it.
  if (entityID && row && (row.posts > 0 || row.interest.state !== "")) {
    return <Navigate to={`/t/${encodeURIComponent(row.name)}`} replace />;
  }

  // An entity the topics read does not hold, rendered as the topic it is not
  // yet: a name, a kind, no binding, no filings, and the stance control —
  // which applies to any entity, because §4.13 records the stance as facts on
  // the entity rather than on a topic-shaped thing.
  const entity = subject?.entity;
  const topic: TopicRow | undefined = row ??
    (entity
      ? {
          id: entity.id,
          name: entity.display_name || entity.id,
          kind: entity.kind,
          binding: null,
          posts: 0,
          awaiting: 0,
          latest_at: entity.created_at,
          interest: { state: "", reason: "", at: "", by: "" },
        }
      : undefined);
  const name = topic?.name ?? asked;

  return (
    <FeedPage
      // The feed is narrowed by the topic's own name, or — on the entity route
      // for a subject nothing is filed under — by its identifier, which
      // matches nothing and says so. Neither ever reaches the address bar: a
      // display name can be a thousand characters of model text, and a URL is
      // not where that belongs.
      topic={entityID && !row ? entityID : name}
      heading={
        <TopicHeader
          subject={entityID !== "" && row === undefined}
          name={name}
          topic={topic}
          // A read that has not answered yet is not an absence. Saying "no
          // topic answers to this name" before the list has arrived would
          // accuse the deployment of something it has not been asked.
          read={answer !== null && (!entityID || subject !== null)}
          failed={failed}
          onStated={(interest) => {
            if (!topic) return;
            setAnswer((current) =>
              current === null
                ? current
                : {
                    ...current,
                    topics: (current.topics ?? []).map((entry) =>
                      entry.id === topic.id ? { ...entry, interest } : entry,
                    ),
                  },
            );
            // Stating a stance on a subject nothing is filed under is what
            // makes it a topic, so the topics are read again: the row arrives
            // with the stance on it and the entity route moves to the name it
            // is now read under.
            if (entityID) load();
          }}
        />
      }
    />
  );
}

// The header: the name as the headline, what it is, how much is in it, what it
// is bound to, where the operator stands, and the fold that asks Babel to
// change its identity.
function TopicHeader({
  name,
  topic,
  subject,
  read,
  failed,
  onStated,
}: {
  name: string;
  topic: TopicRow | undefined;
  // Whether this is a subject the ledger holds and nothing has been filed
  // under. It is not a topic yet — nobody has said a record is about it and
  // the operator has not said where he stands — so it is not named as one.
  subject: boolean;
  read: boolean;
  failed: boolean;
  onStated: (interest: TopicRow["interest"]) => void;
}) {
  return (
    <header className="surface topic-header">
      <div className="page-heading">
        <div>
          <p className="eyebrow">{subject ? "Subject" : "Topic"}</p>
          <h1 className={subject ? "topic-subject-name" : undefined}>
            {subject ? name : `t/${name}`}
          </h1>
        </div>
        {topic && (
          <p className="topic-posts">
            {topic.posts.toLocaleString()} {topic.posts === 1 ? "post" : "posts"}
            {topic.awaiting > 0 && <> · {topic.awaiting.toLocaleString()} awaiting you</>}
          </p>
        )}
      </div>

      {topic && (
        <p className="topic-flair">
          <Badge label={topic.kind} tone="neutral" />
          <BindingLine topic={topic} />
        </p>
      )}

      {/* A name no entity answers to. It is a real state — the feed still
          narrows by the name, and nothing is filed under it — and it is said
          rather than rendered as an empty topic page. */}
      {!topic && read && !failed && (
        <p className="topic-honest">
          No topic in this deployment answers to that name. What follows is the feed narrowed to
          it, which is why it is empty: only you create a topic, and Babel proposes the identity.
        </p>
      )}
      {failed && <p className="topic-honest">The topics could not be read, so this page shows the feed alone.</p>}

      {/* An accepted topic with nothing filed under it reads as what it is:
          an identity the operator created and the corpus has not reached. A
          subject reads as one step further back: the ledger knows it, and
          nothing has claimed to be about it. */}
      {topic && topic.posts === 0 && (
        <p className="topic-honest">
          {subject
            ? "The ledger holds this subject and nothing is filed under it, so it is not a " +
              "topic yet. Saying where you stand makes it one."
            : "Nothing is filed under this topic yet. It exists, and no record has been said " +
              "to be about it."}
        </p>
      )}

      {topic && <InterestControl topic={topic} onStated={onStated} />}
      {topic && <IdentityPeel topic={topic} />}
    </header>
  );
}

// What the topic is bound to, in one muted line: the remote as a name, and the
// checkouts as a count with the list in the title.
//
// A binding with no remote prints no identity at all, because the identity is
// then the common directory the worktrees share — a path, and a path in the
// reading path is what §4.13 forbids. The count is still true and still
// useful, and the fold below carries the rest.
function BindingLine({ topic }: { topic: TopicRow }) {
  if (!topic.binding) return <span className="topic-binding">no binding recorded</span>;
  const paths = topic.binding.paths ?? [];
  const seen = paths.length;
  return (
    <span className="topic-binding" title={bindingDetail(topic)}>
      {topic.binding.remote || topic.binding.kind}
      {seen > 0 && <> · {seen.toLocaleString()} {seen === 1 ? "checkout" : "checkouts"}</>}
    </span>
  );
}

function bindingDetail(topic: TopicRow): string {
  if (!topic.binding) return "";
  const paths = topic.binding.paths ?? [];
  return [`${topic.binding.kind}: ${topic.binding.identity}`, ...paths].join(" · ");
}

// The stance, as §4.13 spells it: four words, the current one pressed, and a
// reason kept verbatim.
//
// Changing it opens the reason box rather than posting immediately, because
// the reason is the part the triage recipe reads. The box does not refuse an
// empty reason for any of the four: internal/web/topics_routes.go requires
// none — "an operator who says *not now* has said something attributable" —
// and a client that refused the act for want of prose would lose a lawful
// stance to gain nothing.
function InterestControl({
  topic,
  onStated,
}: {
  topic: TopicRow;
  onStated: (interest: TopicRow["interest"]) => void;
}) {
  const [chosen, setChosen] = useState<InterestState | "">("");
  const [reason, setReason] = useState("");
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState("");
  const current = topic.interest.state;
  const stated = formatTime(topic.interest.at);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (chosen === "") return;
    setSaving(true);
    setFailure("");
    try {
      const result = await setTopicInterest(topic.id, chosen, reason.trim());
      onStated(result.interest);
      setChosen("");
      setReason("");
    } catch (reason_) {
      setFailure(errorMessage(reason_));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="topic-interest">
      <div className="rule-bar" role="group" aria-label="Interest">
        {INTEREST_STATES.map((state) => (
          <button
            type="button"
            key={state}
            data-interest={state}
            aria-pressed={chosen === "" ? current === state : chosen === state}
            title={INTEREST_MEANS[state]}
            onClick={() => {
              setChosen(state === chosen ? "" : state);
              setReason("");
              setFailure("");
            }}
          >
            {INTEREST_LABEL[state]}
          </button>
        ))}
      </div>
      <span className="topic-interest-label">
        Your interest · Babel spends where you say
      </span>

      {chosen !== "" && (
        <form className="topic-reason" onSubmit={submit}>
          <label>
            Why {INTEREST_LABEL[chosen].toLowerCase()}? Kept verbatim, and optional.
            <input
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              autoFocus
            />
          </label>
          <div className="topic-reason-acts">
            <button type="submit" className="primary-button" disabled={saving}>
              {saving && <span className="spinner small" />}
              {saving ? "Recording…" : `Record ${INTEREST_LABEL[chosen].toLowerCase()}`}
            </button>
            <button type="button" onClick={() => setChosen("")} disabled={saving}>
              Cancel
            </button>
          </div>
          {failure && (
            <p className="inline-error" role="alert">
              {failure}
            </p>
          )}
        </form>
      )}

      {/* What is recorded, with the attribution of the act that recorded it.
          An empty state says nobody has said anything, which is a different
          answer from any of the four and is shown as one. */}
      {current ? (
        <p className="topic-stance">
          {INTEREST_LABEL[current as InterestState] ?? current}
          {topic.interest.reason && (
            <>
              {" · "}
              <span className="quote untrusted-inline">{topic.interest.reason}</span>
            </>
          )}
          {topic.interest.by && <> · {topic.interest.by}</>}
          {stated && (
            <>
              {" · "}
              <time dateTime={topic.interest.at} title={stated.absolute}>
                {stated.relative}
              </time>
            </>
          )}
        </p>
      ) : (
        <p className="topic-stance muted">You have not said where you stand on this.</p>
      )}
    </div>
  );
}

// The three acts on a topic's identity, folded, and each of them an ask.
//
// Retiring, splitting and merging are not offered as buttons that write,
// because the operator ruled that everything about a topic goes through Babel
// (2026-09-12): what the form records is what he wants and why, in his own
// words, and Babel's next filing run answers with a proposal he rules on like
// any other. That is also why nothing here picks the records a split would
// move — which records belong to which part is the judgement Babel has to make
// its case for, and a checkbox list would be the operator doing Babel's work
// and calling it his decision.
//
// It writes through the same capture the header's Tell Babel uses, with the
// topic named in the text, so there is one store of steering pressure rather
// than a second one for topics.
const ASKS: Array<{ value: string; label: string; asks: string }> = [
  { value: "retire", label: "Retire this topic", asks: "retire this" },
  { value: "split", label: "Split this topic", asks: "split this" },
  { value: "merge", label: "Merge this topic into…", asks: "merge this" },
];

function IdentityPeel({ topic }: { topic: TopicRow }) {
  const [act, setAct] = useState("retire");
  const [into, setInto] = useState("");
  const [reason, setReason] = useState("");
  const [others, setOthers] = useState<TopicRow[]>([]);
  const [asks, setAsks] = useState<ComplaintSummary[]>([]);
  const [sending, setSending] = useState(false);
  const [failure, setFailure] = useState("");

  const prefix = `topic t/${topic.name}:`;

  const readAsks = useCallback(() => {
    getComplaints({ limit: 50 })
      .then((listing) =>
        setAsks(listing.items.filter((item) => item.summary.startsWith(prefix))))
      .catch(() => setAsks([]));
  }, [prefix]);

  useEffect(() => {
    readAsks();
    // The merge target is picked by name from the topics that exist, because
    // a merge names two identities the ledger already holds.
    getTopics()
      .then((next) => setOthers((next.topics ?? []).filter((row) => row.id !== topic.id)))
      .catch(() => setOthers([]));
  }, [readAsks, topic.id]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const words = reason.trim();
    if (words === "" || sending) return;
    setSending(true);
    setFailure("");
    try {
      const what = act === "merge" ? `merge into t/${into}` : act;
      await tellComplaint(`${prefix} ${what} — ${words}`);
      setReason("");
      readAsks();
    } catch (reason_) {
      setFailure(errorMessage(reason_));
    } finally {
      setSending(false);
    }
  }

  return (
    <details className="peel topic-identity">
      <summary>Identity</summary>
      <div className="peel-body">
        <p className="muted">
          A topic that names two things is split, two that name one are merged, and one that
          should never have existed is retired. You say which and why; Babel proposes the change
          and you rule on the proposal.
        </p>
        <form className="topic-ask" onSubmit={submit}>
          <label>
            What should change
            <select value={act} onChange={(event) => setAct(event.target.value)}>
              {ASKS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>
          {act === "merge" && (
            <label>
              Into which topic
              <select
                value={into}
                onChange={(event) => setInto(event.target.value)}
                required
                data-ask="into"
              >
                <option value="">Pick a topic…</option>
                {others.map((row) => (
                  <option key={row.id} value={row.name}>
                    t/{row.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            Why (required, kept verbatim)
            <textarea
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              rows={2}
              required
              placeholder="What makes this two things, one thing, or nothing?"
            />
          </label>
          <button
            type="submit"
            className="primary-button"
            disabled={sending || reason.trim() === "" || (act === "merge" && into === "")}
          >
            {sending && <span className="spinner small" />}
            {sending ? "Recording…" : "Ask Babel"}
          </button>
        </form>
        {failure && (
          <p className="inline-error" role="alert">
            {failure}
          </p>
        )}

        {asks.length > 0 && (
          <>
            <ul className="topic-asks">
              {asks.map((ask) => {
                const at = formatTime(ask.at);
                return (
                  <li key={ask.id}>
                    <Link to={`/complaints/${encodeURIComponent(ask.id)}`}>
                      You asked Babel to {askPhrase(ask.summary, prefix)}
                    </Link>
                    {at && (
                      <>
                        {" · "}
                        <time dateTime={ask.at} title={at.absolute}>
                          {at.relative}
                        </time>
                      </>
                    )}
                  </li>
                );
              })}
            </ul>
            {/* No status, deliberately. #115's capture opens nothing,
                assigns nothing and schedules nothing, and a "pending" here
                would be this page promising work the store never took on.
                What is true is what happens next. */}
            <p className="muted">
              Asking schedules nothing. Babel's next filing run answers with a proposal, and the
              proposal is what you rule on.
            </p>
          </>
        )}
      </div>
    </details>
  );
}

// What one ask asked for, read back out of the operator's own words.
//
// The summary is the wording as stored, so the act is the clause between the
// topic and the reason. A wording this page cannot parse — the operator
// amended it, or wrote the line from `babel tell` — is shown as itself rather
// than as a guess about which act he meant.
function askPhrase(summary: string, prefix: string): string {
  const rest = summary.slice(prefix.length).trim();
  const act = rest.split("—")[0].trim();
  if (act.startsWith("merge into ")) return `merge this into ${act.slice("merge into ".length)}`;
  const known = ASKS.find((option) => option.value === act);
  return known ? known.asks : rest;
}
