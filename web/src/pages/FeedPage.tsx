import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Badge, type Tone } from "../analysis";
import { kindLabel } from "../evaluation";
import {
  FEED_KINDS,
  FEED_SORTS,
  FEED_WINDOWS,
  getFeed,
  getTopics,
  UNFILED,
  windowed,
  type FeedKind,
  type FeedPost,
  type FeedResponse,
  type FeedSort,
  type FeedWindow,
  type TopicRow,
  type TopicsResponse,
} from "../feedapi";
import { errorMessage, formatTime } from "../format";
import { ACT_DONE, ROW_ACTS, RULE_KEYS, RuleActs, type RuleAct } from "../ruling";
import type { RecordKind } from "../recordapi";
import { TopicList, useWideViewport } from "../shell";
import "../feed.css";

// The front page (§8.7), and there is one list.
//
// Every record Babel has produced is a post: one line of claim, its kind as
// flair, the topics it belongs to, its age, Babel's score, its comment count,
// and — when it awaits the operator — why it is next in five words. The mod
// queue was this list twice: the same records, read a second way, under a
// second ordering, with a second set of controls. It is a filter and a sort
// now, which is what §8.7 says a distinction the reader applies should be.
//
// Babel votes and the operator rules. The score is the reviewers' assessments
// and only theirs, read-only wherever it appears; the acts a row offers are
// the rulings themselves — accept, reject, defer, refine — and ask, which is a
// question Babel's next review of the record must answer. The arrows are gone,
// and with them the operator's stance: "me voting is a subpar concept, since I
// would rather just triage the idea at this point" (operator direction
// 2026-09-12).
//
// Nothing here computes an ordering. The six sorts are internal/web/feed.go's
// and are tested there, `next` included — it was this client's until this wave
// and a ranking implemented twice is a ranking that disagrees with itself the
// first time either side is touched. This page names each one and says when it
// was computed, which is what §8.5 requires of any list that claims to be
// ranked.
//
// Every control writes the URL, for the ordinary reason: a sorted, filtered
// feed is a thing an operator reloads, shares in an issue, and walks back out
// of with the browser's own Back button. What is *not* in the URL is how far
// down he has read — "show 25 more" appends rather than paging, and a Back
// press should leave the filter he was reading under, not step back through
// three appends of the same list.

// How many rows one read brings, which is §8.6's density ceiling expressed in
// rows: measured at 1440×900, a row waiting on the operator is 130px with its
// reason and its acts, and fifteen of them leave the page inside the 2,880px
// the contract allows. The rest are one press away, which is the pagination
// the same rule asks for rather than a shorter list.
const PAGE_SIZE = 15;

// The filter's two states, as the URL spells them. `me` is what the operator
// arrives under and `all` is the whole corpus; the word is in the URL rather
// than implied by its absence because both are places he shares and walks back
// to, and a filter that vanished from the address bar when it was turned off
// would be a filter Back could not restore.
const NEEDS_ME = "me";
const NEEDS_ALL = "all";

// The flair colours. A kind is a name and not a judgement, so the tones are
// the quiet half of the palette; `question` is amber because it is the one
// kind that is addressed to the reader rather than produced for him.
const KIND_TONES: Record<FeedKind, Tone> = {
  proposal: "blue",
  finding: "green",
  hypothesis: "violet",
  observation: "neutral",
  question: "amber",
};

// What each ordering is computed from, in one sentence, in the reader's terms
// rather than as the formula. The formula is in feed.go; this is what it is
// for.
const SORT_BASIS: Record<FeedSort, string> = {
  next: "What is waiting on you: the most urgent first, and at equal urgency a proposal before a finding before a candidate, oldest first.",
  hot: "The score against how long ago the post arrived.",
  new: "Newest first, and nothing else.",
  top: "The highest score inside the window.",
  controversial: "Support and opposition together, strongest where they are most evenly split.",
  rising: "Votes and comments in the last twelve hours, against the age of the post.",
};

const SORT_LABEL: Record<FeedSort, string> = {
  next: "Next",
  hot: "Hot",
  new: "New",
  top: "Top",
  controversial: "Controversial",
  rising: "Rising",
};

const WINDOW_LABEL: Record<FeedWindow, string> = {
  hour: "Hour",
  day: "Day",
  week: "Week",
  month: "Month",
  year: "Year",
  all: "All",
};

// A question is answered where answers are written and carries no review
// disposition, so a row for one offers no rulings. The kinds that do are the
// record kinds, and a kind this build has no word for is a row with no
// controls rather than a cast that lies.
const RECORD_KINDS: Record<string, RecordKind> = {
  proposal: "proposal",
  finding: "finding",
  hypothesis: "hypothesis",
  observation: "observation",
};

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.closest("input, textarea, select, [contenteditable='true']") !== null;
}

// One act this browser recorded on one post, and when. It stands in the row's
// controls until the next read, because a permanent act that left the list
// looking exactly as it did is an act the operator performs twice.
interface Acted {
  act: RuleAct;
  at: number;
}

function FeedPage() {
  const [params, setParams] = useSearchParams();
  const routed = useParams();
  const navigate = useNavigate();

  // The topic is the path on /t/:topic and a query parameter on the front
  // page, and both mean the same read. The query form exists because the
  // reserved `unfiled` is a filter over the whole feed rather than a
  // community with a page of its own.
  const topic = (routed.topic ?? params.get("topic") ?? "").trim();

  // What awaits him is what he arrives to, on the front page. A topic is the
  // same feed narrowed to one community and arrives whole: the reader who
  // opened a project is reading the project, not triaging it.
  const askedNeeds = params.get("needs") ?? "";
  const needsMe = askedNeeds === "" ? topic === "" : askedNeeds === NEEDS_ME;

  // The ordering follows the filter until the reader names one. Under "what
  // needs me" the useful order is §8.5's; over the whole corpus it is hot,
  // which is the front page of a feed nobody is triaging.
  const askedSort = params.get("sort") ?? "";
  const sort: FeedSort = (FEED_SORTS as string[]).includes(askedSort)
    ? (askedSort as FeedSort)
    : needsMe
      ? "next"
      : "hot";
  const askedWindow = params.get("t") ?? "";
  const t: FeedWindow = (FEED_WINDOWS as string[]).includes(askedWindow)
    ? (askedWindow as FeedWindow)
    : "day";
  // An unknown kind in the URL selects everything rather than nothing: a
  // reader who typed a word into the address bar asked for a feed, not for an
  // error.
  const kindKey = (params.get("kind") ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name) => (FEED_KINDS as string[]).includes(name))
    .join(",");
  const kinds = kindKey ? (kindKey.split(",") as FeedKind[]) : [];
  const needs = needsMe ? NEEDS_ME : "";

  const [answer, setAnswer] = useState<FeedResponse | null>(null);
  const [posts, setPosts] = useState<FeedPost[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [appending, setAppending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [focus, setFocus] = useState(-1);
  const [acted, setActed] = useState<Record<string, Acted>>({});
  const [announcement, setAnnouncement] = useState("");
  const rows = useRef(new Map<string, HTMLLIElement>());

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getFeed({ sort, t, topic, needs, kind: kindKey ? kindKey.split(",") : [], limit: PAGE_SIZE })
      .then((next) => {
        setAnswer(next);
        setPosts(next.posts ?? []);
        setFocus(-1);
        // The receipts a ruling left on the rows belong to the list they were
        // recorded in. A read carries the new standing, so keeping them would
        // be showing the act twice.
        setActed({});
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [kindKey, needs, sort, t, topic]);

  useEffect(load, [load]);

  // More of the same order, appended. The offset is how many rows are already
  // on screen rather than a page number, so a post published while the
  // operator was reading cannot make the next batch start inside the last one
  // — and the ids already drawn are skipped if it does anyway.
  function more() {
    const drawn = posts ?? [];
    setAppending(true);
    getFeed({
      sort,
      t,
      topic,
      needs,
      kind: kindKey ? kindKey.split(",") : [],
      limit: PAGE_SIZE,
      offset: drawn.length,
    })
      .then((next) => {
        setAnswer(next);
        setPosts((current) => {
          const already = new Set((current ?? []).map((post) => post.id));
          return [...(current ?? []), ...(next.posts ?? []).filter((post) => !already.has(post.id))];
        });
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setAppending(false));
  }

  // One control, one parameter, and the first page of the new order. Nothing
  // carries the scroll depth across a filter change: it belonged to a list
  // that no longer exists.
  function select(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next);
  }

  function toggleKind(kind: FeedKind) {
    const next = kinds.includes(kind) ? kinds.filter((name) => name !== kind) : [...kinds, kind];
    select("kind", next.join(","));
  }

  // One gesture, and the ordering follows it: turning the filter off drops a
  // sort the reader never asked for, so "everything" arrives hot rather than
  // in the order the queue was in. A sort he did name is his and stays.
  function toggleNeeds() {
    const next = new URLSearchParams(params);
    next.set("needs", needsMe ? NEEDS_ALL : NEEDS_ME);
    if (!(FEED_SORTS as string[]).includes(askedSort)) next.delete("sort");
    setParams(next);
  }

  const shown = posts ?? [];
  const total = answer?.total ?? 0;
  const focused = focus >= 0 ? shown[focus] : undefined;
  const focusID = focused?.id ?? null;

  // The ring is a real DOM focus, so the browser scrolls the row into view and
  // a screen reader follows it. It is not taken back from a control inside the
  // row that already holds it — an operator whose pointer is in the row's
  // confirmation must be able to answer it.
  useEffect(() => {
    if (!focusID) return;
    const element = rows.current.get(focusID);
    if (!element || element.contains(document.activeElement)) return;
    element.focus();
  }, [focusID]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (isTyping(event.target)) return;
      if (shown.length === 0) return;
      switch (event.key) {
        case "j":
          event.preventDefault();
          setFocus((current) => Math.min(shown.length - 1, current + 1));
          return;
        case "k":
          event.preventDefault();
          setFocus((current) => (current <= 0 ? 0 : current - 1));
          return;
        case "Enter": {
          // A link or a button under the cursor has its own meaning for
          // Enter, and this must not double it.
          const inControl =
            event.target instanceof HTMLElement &&
            event.target.closest("a, button, summary") !== null;
          if (inControl || !focused) return;
          event.preventDefault();
          navigate(focused.href);
          return;
        }
        default: {
          const act = RULE_KEYS[event.key];
          if (!act || !focused) return;
          const control = rows.current
            .get(focused.id)
            ?.querySelector<HTMLButtonElement>(`[data-ruling="${act}"]`);
          if (!control) return;
          event.preventDefault();
          // The key presses the row's own control rather than posting. A
          // ruling is confirmed in one sentence before it is recorded, and a
          // keyboard path that wrote directly would be a second authority
          // over one append-only ledger.
          control.click();
          return;
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [focused, navigate, shown.length]);

  const built = formatTime(answer?.built_at);
  const filtered =
    kinds.length > 0 || topic !== "" || needsMe || (windowed(sort) && t !== "all");
  const where = topic === UNFILED ? "No topic" : topic ? `t/${topic}` : "Home";
  // Where the topics go. Above 1024px they are a rail beside the feed and
  // below it they are a fold above it — one list either way, mounted once,
  // because two copies of it is two copies for a screen reader that reads
  // neither of them as decoration.
  const wide = useWideViewport();

  return (
    <section className="page feed-page">
      <div className="feed-layout">
        <div className="feed-column">
          <div className="page-heading">
            <div>
              <p className="eyebrow">{topic ? "Topic" : "The feed"}</p>
              <h1>{where}</h1>
            </div>
            {answer && (
              <p className="feed-count">
                {total.toLocaleString()} {total === 1 ? "post" : "posts"}
              </p>
            )}
          </div>

          {!wide && (
            <details className="peel topics-peel">
              <summary>Topics</summary>
              <div className="peel-body">
                <TopicList current={topic} />
              </div>
            </details>
          )}

          <div className="feed-controls">
            <div className="rule-bar" role="group" aria-label="Sort">
              {FEED_SORTS.map((name) => (
                <button
                  type="button"
                  key={name}
                  data-sort={name}
                  aria-pressed={sort === name}
                  title={SORT_BASIS[name]}
                  onClick={() => select("sort", name)}
                >
                  {SORT_LABEL[name]}
                </button>
              ))}
            </div>
            {/* The window is a control for the two sorts that read it and is
                absent for the four that do not: a period selector beside
                "new" is a control that does nothing and does not say so. */}
            {windowed(sort) && (
              <div className="rule-bar" role="group" aria-label="Period">
                {FEED_WINDOWS.map((name) => (
                  <button
                    type="button"
                    key={name}
                    data-window={name}
                    aria-pressed={t === name}
                    onClick={() => select("t", name === "day" ? "" : name)}
                  >
                    {WINDOW_LABEL[name]}
                  </button>
                ))}
              </div>
            )}
            {/* The kinds and the one filter that is not a kind, on the same
                row as the ordering because they are the same gesture — what
                to read and in what order — and two rows of controls above a
                list is the header growing into the page it is a handle
                for. */}
            <div className="feed-kinds" role="group" aria-label="Kind">
              <button
                type="button"
                data-chip="needs-me"
                className={needsMe ? "chip active feed-needs" : "chip feed-needs"}
                aria-pressed={needsMe}
                title="Only the posts waiting on you: a record awaiting a ruling, or a question awaiting your answer."
                onClick={toggleNeeds}
              >
                Needs me
              </button>
              <button
                type="button"
                data-chip="all"
                className={kinds.length === 0 ? "chip active" : "chip"}
                aria-pressed={kinds.length === 0}
                onClick={() => select("kind", "")}
              >
                Everything
              </button>
              {FEED_KINDS.map((kind) => (
                <button
                  type="button"
                  key={kind}
                  data-chip={`kind-${kind}`}
                  className={kinds.includes(kind) ? "chip active" : "chip"}
                  aria-pressed={kinds.includes(kind)}
                  onClick={() => toggleKind(kind)}
                >
                  {kindLabel(kind)}
                </button>
              ))}
            </div>
          </div>

          <p className="feed-basis">
            {SORT_BASIS[sort]}
            {built && <> Ranked {built.relative}.</>}{" "}
            <span className="feed-keys">
              j/k move · ↵ open · y/n/d accept, reject, defer · f refine · q ask
            </span>
          </p>

          {/* Every act on a row happens in place, so the page says what it
              did — above the list, where it is on screen whichever row the
              reader is standing on. */}
          <p className="feed-said" role="status" aria-live="polite">
            {announcement}
          </p>

          {answer?.notice && (
            <p className="feed-notice" role="status">
              {answer.notice}
            </p>
          )}

          {loading && !posts && (
            <div className="surface state-note">
              <span className="spinner" /> Reading the feed…
            </div>
          )}

          {error && (
            <div className="surface state-note error-state">
              <strong>The feed could not be read.</strong>
              <span>{error}</span>
              <button type="button" onClick={load}>
                Try again
              </button>
            </div>
          )}

          {!loading &&
            !error &&
            shown.length === 0 &&
            (needsMe && kinds.length === 0 && topic === "" ? (
              <div className="surface state-note empty-state">
                <span className="empty-icon" aria-hidden="true">◇</span>
                <strong>Nothing is waiting on you</strong>
                <span>
                  Records arrive here when exploration develops them far enough to be worth a
                  ruling.{" "}
                  <button type="button" className="link-button" onClick={toggleNeeds}>
                    Read everything
                  </button>{" "}
                  in the meantime.
                </span>
              </div>
            ) : filtered ? (
              <div className="surface state-note empty-state">
                <span className="empty-icon" aria-hidden="true">◇</span>
                <strong>Nothing matches this view</strong>
                <span>
                  That is a statement about the filters, not about what Babel has produced.{" "}
                  <button
                    type="button"
                    className="link-button"
                    onClick={() => navigate(`/?needs=${NEEDS_ALL}`)}
                  >
                    Clear them
                  </button>
                  .
                </span>
              </div>
            ) : sort === "rising" ? (
              <div className="surface state-note empty-state">
                <span className="empty-icon" aria-hidden="true">◇</span>
                <strong>Nothing has been voted on or commented in the last twelve hours</strong>
                <span>
                  Rising is activity against age, so an unvisited deployment has none.{" "}
                  <Link to={`/?needs=${NEEDS_ALL}`}>Read the feed hot</Link> instead.
                </span>
              </div>
            ) : (
              <div className="surface state-note empty-state">
                <span className="empty-icon" aria-hidden="true">◇</span>
                <strong>Babel has not posted anything yet</strong>
                <span>
                  Every record it produces appears here. Nothing is running until a run is
                  started under <Link to="/watch">Watch</Link>.
                </span>
              </div>
            ))}

          {shown.length > 0 && (
            <ol className="feed-list">
              {shown.map((post, index) => (
                <FeedRow
                  key={post.id}
                  post={post}
                  focused={index === focus}
                  acted={acted[post.id]}
                  onFocus={() => setFocus(index)}
                  onActed={(act, message) => {
                    setActed((current) => ({
                      ...current,
                      [post.id]: { act, at: Date.now() },
                    }));
                    setAnnouncement(message);
                    // A question is a comment, and the count beside the claim
                    // is the one number on the row that moves the moment it is
                    // recorded: the thread is read live, while the feed's own
                    // projection is rebuilt on its own schedule.
                    if (act === "ask") {
                      setPosts((current) =>
                        (current ?? []).map((row) =>
                          row.id === post.id ? { ...row, comments: row.comments + 1 } : row,
                        ),
                      );
                    }
                  }}
                  register={(element) => {
                    if (element) rows.current.set(post.id, element);
                    else rows.current.delete(post.id);
                  }}
                />
              ))}
            </ol>
          )}

          {shown.length > 0 && shown.length < total && (
            <div className="feed-more">
              <button type="button" onClick={more} disabled={appending}>
                {appending && <span className="spinner small" />}
                {appending ? "Reading…" : `Show ${Math.min(PAGE_SIZE, total - shown.length)} more`}
              </button>
              <span className="muted">
                {shown.length.toLocaleString()} of {total.toLocaleString()}
              </span>
            </div>
          )}

          {shown.length > 0 && shown.length >= total && total > PAGE_SIZE && (
            <p className="feed-end">That is all {total.toLocaleString()} of them.</p>
          )}
        </div>

        {wide && (
          <aside className="feed-rail" aria-label="Topics">
            <h2>Topics</h2>
            <TopicList current={topic} />
          </aside>
        )}
      </div>
    </section>
  );
}

// One post.
//
// The score, the claim, the facts a reader decides with, why it is next when
// it is, and the acts it invites when it is waiting on him. A fact this record
// does not have is absent rather than empty — an unattributed record carries
// no "by", an unfiled one carries no topic, an unassessed one carries no score
// — and no absence is rendered as a dash on a row that has twenty-five
// neighbours.
//
// The controls are on the awaiting rows only. A row that offered a ruling on a
// record already ruled on would be offering to overwrite an append-only
// decision, and one that offered it on a question would be offering to rule on
// something answered somewhere else.
function FeedRow({
  post,
  focused,
  acted,
  onFocus,
  onActed,
  register,
}: {
  post: FeedPost;
  focused: boolean;
  acted: Acted | undefined;
  onFocus: () => void;
  onActed: (act: RuleAct, message: string) => void;
  register: (element: HTMLLIElement | null) => void;
}) {
  const created = formatTime(post.created_at);
  const [first, second, ...rest] = post.topics;
  const kind = RECORD_KINDS[post.kind];
  // What Babel's reviewers said, for the one gesture that explains the
  // number. A record no reviewer has assessed says so rather than reading as
  // nought support and nought opposition, which §8.5 refuses: an unreviewed
  // record rendered as a zero reads as one nobody objected to.
  const voted = post.support + post.oppose + post.unsure > 0;
  const breakdown = voted
    ? `Babel's reviewers: ${post.support} support, ${post.oppose} oppose, ${post.unsure} unsure`
    : "Babel's reviewers have not assessed this yet.";
  const recorded = acted ? formatTime(new Date(acted.at).toISOString()) : null;
  return (
    <li
      className="feed-row"
      data-focused={focused ? "" : undefined}
      data-post={post.id}
      data-awaiting={post.awaiting ? "" : undefined}
      tabIndex={-1}
      ref={register}
      onFocus={onFocus}
      aria-label={`${kindLabel(post.kind)}: ${post.title}`}
    >
      {/* Babel's score, read-only, with what it is made of one gesture away.
          It is a figure and is set as one; it is not a control, because the
          operator does not vote. */}
      <span className="feed-score" title={breakdown} aria-label={breakdown}>
        {voted ? post.score : "—"}
      </span>
      <div className="feed-body">
        <Link className="feed-claim untrusted-inline" to={post.href}>
          {post.title || "a record with no title recorded"}
        </Link>
        <span className="feed-facts">
          <Badge label={kindLabel(post.kind)} tone={KIND_TONES[post.kind]} />
          {first && (
            <Link className="feed-topic" to={`/t/${encodeURIComponent(first)}`}>
              t/{first}
            </Link>
          )}
          {second && (
            <Link className="feed-topic" to={`/t/${encodeURIComponent(second)}`}>
              t/{second}
            </Link>
          )}
          {rest.length > 0 && (
            <span className="feed-topic" title={rest.map((name) => `t/${name}`).join(" · ")}>
              +{rest.length}
            </span>
          )}
          {post.author && (
            <Link className="feed-author" to={post.author.href}>
              by {post.author.run_id}
            </Link>
          )}
          {created && (
            <time className="feed-age" dateTime={post.created_at} title={created.absolute}>
              {created.relative}
            </time>
          )}
          {post.comments > 0 && (
            <Link className="feed-comments" to={`${post.href}#comments`}>
              {post.comments.toLocaleString()} {post.comments === 1 ? "comment" : "comments"}
            </Link>
          )}
        </span>
        {/* Why it is next, from the fields the post's own store returned. It
            is the one fact on the row a reader cannot reconstruct for
            himself, and it is absent rather than empty for a post nobody is
            waiting on. */}
        {post.awaiting && post.why && <span className="feed-why">{post.why}</span>}
        {/* What he did, in place of what he could do. It stays until the next
            read: the standing the read carries is the store's answer, and
            this is the receipt for the moment in between. */}
        {acted && (
          <span className="feed-acted" data-act={acted.act}>
            {ACT_DONE[acted.act]}
            {recorded && ` · ${recorded.relative}`}
          </span>
        )}
        {!acted && post.awaiting && kind && (
          <div className="feed-acts">
            <RuleActs id={post.id} kind={kind} acts={ROW_ACTS} onActed={onActed} />
          </div>
        )}
      </div>
    </li>
  );
}

// Every topic, which is where the rail's "all topics" goes.
//
// It is a list of communities and their sizes, not a page about storage: a
// topic is a name bound to something real, with a reason (§4.13), and this
// deployment binding it to a repository's identity is a fact about the binding
// rather than a path the reading path may print.
export function TopicsIndex() {
  const [answer, setAnswer] = useState<TopicsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getTopics()
      .then(setAnswer)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  const topics = answer?.topics ?? [];

  return (
    <section className="page feed-page topics-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">The feed</p>
          <h1>Topics</h1>
        </div>
        {answer && (
          <p className="feed-count">
            {topics.length.toLocaleString()} {topics.length === 1 ? "topic" : "topics"}
          </p>
        )}
      </div>

      {loading && !answer && (
        <div className="surface state-note">
          <span className="spinner" /> Reading the topics…
        </div>
      )}

      {error && (
        <div className="surface state-note error-state">
          <strong>The topics could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>
            Try again
          </button>
        </div>
      )}

      {!loading && !error && topics.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing is filed under a topic yet</strong>
          <span>
            A topic is what a record is about, bound to something real — today, the repository
            the work happened in. <Link to="/">The feed</Link> holds every post either way.
          </span>
        </div>
      )}

      {topics.length > 0 && (
        <ul className="topic-index">
          {topics.map((row) => {
            const latest = formatTime(row.latest_at);
            // What the name is bound to, for the one gesture that asks. It is
            // a title rather than a line of the list because §4.13 is
            // explicit that a binding is evidence about a topic and not the
            // topic: the name is what the reader reads, and the identity
            // behind it is what he can check.
            const bound = row.binding
              ? `${row.binding.kind}: ${row.binding.identity} — ${
                  row.heuristic
                    ? "seeded from repository identity and not yet reviewed"
                    : "filed with a reason"
                }`
              : undefined;
            return (
              <li key={row.name}>
                <Link to={`/t/${encodeURIComponent(row.name)}`} title={bound}>
                  t/{row.name}
                </Link>
                <span className="topic-facts">
                  {row.posts.toLocaleString()} {row.posts === 1 ? "post" : "posts"}
                  {latest && <> · newest {latest.relative}</>}
                  {row.heuristic && <> · seeded, not yet reviewed</>}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {answer && answer.unfiled > 0 && (
        <p className="topic-note">
          <Link to={`/?topic=${UNFILED}&needs=${NEEDS_ALL}`}>
            {answer.unfiled.toLocaleString()} posts
          </Link>{" "}
          are about nothing this deployment can name yet. They are in the feed, unfiled, which is
          the triage backlog and not a bin.
        </p>
      )}
    </section>
  );
}


export default FeedPage;
