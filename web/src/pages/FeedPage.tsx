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
  type TopicsResponse,
} from "../feedapi";
import { errorMessage, formatTime } from "../format";
import { TopicList, useWideViewport } from "../shell";
import { VoteArrows, type VoteTally } from "../vote";
import "../feed.css";

// The front page (§8.7).
//
// Every record Babel has produced is a post: one line of claim, its kind as
// flair, the topics it belongs to, its age, its score with the operator's
// arrows, and its comment count. There is one list, one sort bar over the
// whole deployment, and the kinds are a filter on it rather than five places
// to go — which is what §8.6 already said they were and what the four listing
// pages this replaces never quite believed.
//
// Nothing here computes an ordering. The five sorts are internal/web/feed.go's
// and are tested there, because a ranking implemented twice is a ranking that
// disagrees with itself the first time either side is touched; this page names
// each one and says when it was computed, which is what §8.5 requires of any
// list that claims to be ranked.
//
// Every control writes the URL, for the ordinary reason: a sorted, filtered
// feed is a thing an operator reloads, shares in an issue, and walks back out
// of with the browser's own Back button. What is *not* in the URL is how far
// down he has read — "show 25 more" appends rather than paging, and a Back
// press should leave the filter he was reading under, not step back through
// three appends of the same list.

const PAGE_SIZE = 25;

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
  hot: "The score against how long ago the post arrived.",
  new: "Newest first, and nothing else.",
  top: "The highest score inside the window.",
  controversial: "Support and opposition together, strongest where they are most evenly split.",
  rising: "Votes and comments in the last twelve hours, against the age of the post.",
};

const SORT_LABEL: Record<FeedSort, string> = {
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

// The standing is said only when there is one. "new" is the absence of a
// ruling, and a word on every row that means "nothing has happened" is a word
// a reader learns to skip — along with the ones beside it.
function standingWord(standing: string): string {
  if (!standing || standing === "new") return "";
  return standing.replaceAll("-", " ");
}

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.closest("input, textarea, select, [contenteditable='true']") !== null;
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
  const askedSort = params.get("sort") ?? "";
  const sort: FeedSort = (FEED_SORTS as string[]).includes(askedSort)
    ? (askedSort as FeedSort)
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

  const [answer, setAnswer] = useState<FeedResponse | null>(null);
  const [posts, setPosts] = useState<FeedPost[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [appending, setAppending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [focus, setFocus] = useState(-1);
  const rows = useRef(new Map<string, HTMLLIElement>());

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getFeed({ sort, t, topic, kind: kindKey ? kindKey.split(",") : [], limit: PAGE_SIZE })
      .then((next) => {
        setAnswer(next);
        setPosts(next.posts ?? []);
        setFocus(-1);
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [kindKey, sort, t, topic]);

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

  // A recorded vote moves the number on the row it was cast on and nothing
  // else: the feed is not re-read and not re-ordered under the operator's
  // hands, because a row that jumps out from under a click is how a stance
  // lands on the wrong record.
  function voted(id: string, next: VoteTally) {
    setPosts((current) =>
      (current ?? []).map((post) =>
        post.id === id
          ? {
              ...post,
              score: next.score,
              support: next.support,
              oppose: next.oppose,
              unsure: next.unsure,
              you: next.you as FeedPost["you"],
            }
          : post,
      ),
    );
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

  const shown = posts ?? [];
  const total = answer?.total ?? 0;
  const focused = focus >= 0 ? shown[focus] : undefined;
  const focusID = focused?.id ?? null;

  // The ring is a real DOM focus, so the browser scrolls the row into view and
  // a screen reader follows it. It is not taken back from a control inside the
  // row that already holds it — an operator whose pointer is on the arrows
  // must be able to press them.
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
        case "a":
        case "d": {
          if (!focused) return;
          event.preventDefault();
          // The keys press the arrow rather than posting a stance of their
          // own. One control records a vote — including the withdrawal a lit
          // arrow performs — and a keyboard path that posted directly would
          // be a second implementation of it, with its own idea of what
          // pressing "a" twice means.
          const stance = event.key === "a" ? "agree" : "disagree";
          rows.current
            .get(focused.id)
            ?.querySelector<HTMLButtonElement>(`[data-stance="${stance}"]`)
            ?.click();
          return;
        }
        default:
          return;
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [focused, navigate, shown.length]);

  const built = formatTime(answer?.built_at);
  const filtered = kinds.length > 0 || topic !== "" || (windowed(sort) && t !== "all");
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
                  onClick={() => select("sort", name === "hot" ? "" : name)}
                >
                  {SORT_LABEL[name]}
                </button>
              ))}
            </div>
            {/* The window is a control for the two sorts that read it and is
                absent for the three that do not: a period selector beside
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
            {/* The kinds, on the same row as the ordering because they are
                the same gesture — what to read and in what order — and two
                rows of controls above a list is the header growing into the
                page it is supposed to be a handle for. */}
            <div className="feed-kinds" role="group" aria-label="Kind">
              <button
                type="button"
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
            <span className="feed-keys">j/k move · ↵ open · a/d vote</span>
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
            (filtered ? (
              <div className="surface state-note empty-state">
                <span className="empty-icon" aria-hidden="true">◇</span>
                <strong>Nothing matches this view</strong>
                <span>
                  That is a statement about the filters, not about what Babel has produced.{" "}
                  <button
                    type="button"
                    className="link-button"
                    onClick={() => navigate("/")}
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
                  <Link to="/">Read the feed hot</Link> instead.
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
                  onFocus={() => setFocus(index)}
                  onVoted={(next) => voted(post.id, next)}
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
// The arrows, the claim, and the facts a reader decides with: what kind of
// thing it is, where it came from, how old it is and how much has been said
// about it. A fact this record does not have is absent rather than empty —
// an unattributed record carries no "by", an unfiled one carries no topic,
// and neither absence is rendered as a dash on a row that has thirty
// neighbours.
function FeedRow({
  post,
  focused,
  onFocus,
  onVoted,
  register,
}: {
  post: FeedPost;
  focused: boolean;
  onFocus: () => void;
  onVoted: (next: VoteTally) => void;
  register: (element: HTMLLIElement | null) => void;
}) {
  const created = formatTime(post.created_at);
  const standing = standingWord(post.standing);
  const [first, second, ...rest] = post.topics;
  return (
    <li
      className="feed-row"
      data-focused={focused ? "" : undefined}
      data-post={post.id}
      tabIndex={-1}
      ref={register}
      onFocus={onFocus}
      aria-label={`${kindLabel(post.kind)}: ${post.title}`}
    >
      <VoteArrows
        id={post.id}
        score={post.score}
        support={post.support}
        oppose={post.oppose}
        unsure={post.unsure}
        you={post.you}
        onVoted={onVoted}
      />
      <div className="feed-body">
        <Link className="feed-claim untrusted-inline" to={post.href}>
          {post.title || "a record with no title recorded"}
        </Link>
        <span className="feed-facts">
          <Badge label={kindLabel(post.kind)} tone={KIND_TONES[post.kind]} />
          {standing && (
            <span className="feed-standing" data-standing={post.standing}>
              {standing}
            </span>
          )}
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
      </div>
    </li>
  );
}

// Every topic, which is where the rail's "all topics" goes.
//
// It is a list of communities and their sizes, not a page about storage: a
// topic is a name with a count (§8.7), and this deployment resolving it to a
// directory is an implementation detail the surface must not assume, let
// alone print.
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
          <strong>No record's evidence has been traced to a topic yet</strong>
          <span>
            A topic is where a record's evidence came from, so they appear as sessions are
            archived and cited. <Link to="/">The feed</Link> holds every post either way.
          </span>
        </div>
      )}

      {topics.length > 0 && (
        <ul className="topic-index">
          {topics.map((row) => {
            const latest = formatTime(row.latest_at);
            return (
              <li key={row.name}>
                <Link to={`/t/${encodeURIComponent(row.name)}`}>t/{row.name}</Link>
                <span className="topic-facts">
                  {row.posts.toLocaleString()} {row.posts === 1 ? "post" : "posts"}
                  {latest && <> · newest {latest.relative}</>}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {answer && answer.unfiled > 0 && (
        <p className="topic-note">
          <Link to={`/?topic=${UNFILED}`}>{answer.unfiled.toLocaleString()} posts</Link> cite no
          session this deployment can resolve to a topic. They are in the feed, without one.
        </p>
      )}
    </section>
  );
}

export default FeedPage;
