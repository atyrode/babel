import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { kindLabel } from "../evaluation";
import {
  FEED_KINDS,
  FEED_SORTS,
  FEED_WINDOWS,
  getFeed,
  getTopics,
  INTEREST_LABEL,
  UNFILED,
  windowed,
  type FeedKind,
  type FeedPost,
  type FeedResponse,
  type FeedSort,
  type FeedWindow,
  type InterestState,
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

// The kind's tone. A kind is a name and not a judgement, so the tones are the
// quiet half of the palette; `question` is amber because it is the one kind
// that is addressed to the reader rather than produced for him.
//
// It is worn as small-caps text in the tone's colour rather than as a filled
// badge. Fifteen boxed badges down a list of fifteen one-line claims made the
// kind the loudest thing on every row — a coloured rectangle beats a sentence
// every time — and the kind is the least surprising fact about a post.
const KIND_TONES: Record<FeedKind, string> = {
  proposal: "accent",
  finding: "good",
  hypothesis: "info",
  question: "warn",
};

// What each ordering is computed from, in one sentence, in the reader's terms
// rather than as the formula. The formula is in feed.go; this is what it is
// for. It used to be printed under the control bar on every read; it is on the
// control itself now, which is where a reader asks what it means.
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

// The same six words inside the sentence, where they are read as part of it
// rather than as the name of a control.
const SORT_WORD: Record<FeedSort, string> = {
  next: "next",
  hot: "hot",
  new: "newest",
  top: "top",
  controversial: "controversial",
  rising: "rising",
};

// The window as the reader would say it, because it is read inside a sentence:
// "sorted by top · this week", not "Top / Week".
const WINDOW_LABEL: Record<FeedWindow, string> = {
  hour: "this hour",
  day: "today",
  week: "this week",
  month: "this month",
  year: "this year",
  all: "all time",
};

// The kinds in the plural, for the same reason: the sentence says what is in
// the list, and a list holds proposals rather than Proposal.
const KIND_PLURAL: Record<FeedKind, string> = {
  proposal: "proposals",
  finding: "findings",
  hypothesis: "hypotheses",
  question: "questions",
};

// The two states of the filter as the sentence says them.
const NEEDS_WORD = "what needs me";
const EVERYTHING_WORD = "everything";

// What the kinds segment says. One kind is named, two are named, and past
// that the sentence counts them: "proposals, findings and hypotheses" is
// longer than the sentence it is inside, and the menu is one press away for
// anybody who needs to know which three.
function kindsWord(chosen: FeedKind[]): string {
  if (chosen.length === 0) return "all kinds";
  if (chosen.length === 1) return KIND_PLURAL[chosen[0]];
  if (chosen.length === 2) return `${KIND_PLURAL[chosen[0]]} and ${KIND_PLURAL[chosen[1]]}`;
  return `${chosen.length} kinds`;
}

// How many rows a cold load draws in place of the list. Six is what fills the
// first screen at 1440×900 without claiming a page length the answer has not
// arrived to confirm.
const SKELETON_ROWS = [0, 1, 2, 3, 4, 5];

// A question is answered where answers are written and carries no review
// disposition, so a row for one offers no rulings. The kinds that do are the
// record kinds, and a kind this build has no word for is a row with no
// controls rather than a cast that lies.
//
// An observation is not in the feed at all (operator decision, 2026-09-12):
// it is evidence at depth 3 of the hypothesis that cites it, and §6.7 makes
// it a review subject of nothing — so there was never a row for it to rule
// on.
const RECORD_KINDS: Record<string, RecordKind> = {
  proposal: "proposal",
  finding: "finding",
  hypothesis: "hypothesis",
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

// Which of the sentence's three segments is open. One at a time, held by the
// page rather than by each menu, because the keys that open them (`s`, `c`,
// `m`) are the page's and a second copy of "is this one open" is the first
// thing to disagree.
type PickName = "needs" | "sort" | "kinds";

// One editable segment of the sentence: a word the reader can press, and the
// menu of the words it could be instead.
//
// It is a menu and not a `<select>` because two of the three are not one
// choice — the kinds are a set, and the order carries a period beside it —
// and a native select cannot hold either. Everything a native select gives
// for free is therefore stated here: the button says it opens a menu and
// whether it is open, the arrows walk the items, Enter takes the one under
// the keyboard, Escape closes and hands the keyboard back, and a press
// outside closes.
function FeedMenu({
  name,
  label,
  title,
  wide,
  open,
  setOpen,
  children,
}: {
  name: PickName;
  label: string;
  title: string;
  // The order's menu carries a second column when the order reads a period.
  wide?: boolean;
  open: boolean;
  setOpen: (next: PickName | null) => void;
  children: ReactNode;
}) {
  const host = useRef<HTMLSpanElement | null>(null);
  const opener = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!open) return;
    const surface = host.current;
    surface?.querySelector<HTMLElement>("[role^='menuitem']")?.focus();
    function onPointerDown(event: PointerEvent) {
      if (!surface?.contains(event.target as Node)) setOpen(null);
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(null);
        opener.current?.focus();
        return;
      }
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
      const items = [...(surface?.querySelectorAll<HTMLElement>("[role^='menuitem']") ?? [])];
      if (items.length === 0) return;
      event.preventDefault();
      const at = items.indexOf(document.activeElement as HTMLElement);
      const step = event.key === "ArrowDown" ? 1 : -1;
      // A wrap rather than a stop: the list is five items long and the reader
      // who holds the key down is looking for one of them, not for the end.
      items[(at + step + items.length) % items.length]?.focus();
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, setOpen]);

  return (
    <span className="feed-pick" ref={host}>
      <button
        type="button"
        ref={opener}
        data-pick={name}
        className="feed-pick-button"
        aria-haspopup="menu"
        aria-expanded={open}
        title={title}
        onClick={() => setOpen(open ? null : name)}
      >
        {label}
        <span className="feed-pick-caret" aria-hidden="true">▾</span>
      </button>
      {open && (
        <div
          className={wide ? "surface feed-menu feed-menu-wide" : "surface feed-menu"}
          role="menu"
          aria-label={title}
        >
          {children}
        </div>
      )}
    </span>
  );
}

// `heading` is the page's own header, when the page is not the front page.
// The topic page passes its header — the name, the figures, the binding and
// the operator's stance — and everything below it is this feed, narrowed by
// the same route parameter: one list, one set of controls, one pagination,
// whichever heading stands above it.
function FeedPage({ heading }: { heading?: ReactNode } = {}) {
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
  const [pick, setPick] = useState<PickName | null>(null);
  const rows = useRef(new Map<string, HTMLLIElement>());

  // A read replaces the rows when it answers and not before. Changing the
  // order used to blank the list, paint a "Reading the feed…" note where
  // fifteen rows had been, and paint them back — three layouts for one
  // gesture, and the rail jumped twice on the way. The rows on screen are the
  // last true answer until there is a newer one; what says a newer one is
  // coming is the line above the list.
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
  function chooseNeeds(mine: boolean) {
    const next = new URLSearchParams(params);
    next.set("needs", mine ? NEEDS_ME : NEEDS_ALL);
    if (!(FEED_SORTS as string[]).includes(askedSort)) next.delete("sort");
    setParams(next);
  }

  function toggleNeeds() {
    chooseNeeds(!needsMe);
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
      // While a segment of the sentence is open it owns the keyboard: the
      // arrows walk its items and Escape closes it. A `j` that moved the row
      // focus behind an open menu would be two things listening to one press.
      if (pick !== null) return;
      switch (event.key) {
        // The three keys that edit the sentence. They are here rather than on
        // the controls because the reader is looking at the list when he
        // decides the list is wrong.
        case "s":
          event.preventDefault();
          setPick("sort");
          return;
        case "c":
          event.preventDefault();
          setPick("kinds");
          return;
        case "m":
          event.preventDefault();
          toggleNeeds();
          return;
        default:
          break;
      }
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
    // `toggleNeeds` closes over the current URL parameters, which is exactly
    // what `m` has to read, so the effect is re-bound when they change.
  }, [focused, navigate, pick, shown.length, needsMe, askedSort, params]);

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
          {heading ?? (
            <div className="page-heading">
              <div>
                <p className="eyebrow">The feed</p>
                <h1>{where}</h1>
              </div>
            </div>
          )}

          {!wide && (
            <details className="peel topics-peel">
              <summary>Topics</summary>
              <div className="peel-body">
                <TopicList current={topic} />
              </div>
            </details>
          )}

          {/* The controls, as one sentence the reader edits.

              They were a segmented control of six orderings, a second one of
              six periods, six pill chips, and a paragraph under all of it
              explaining the ordering and listing the keys — four rows of
              chrome above a list of fifteen one-line posts, and the operator
              could not tell from it what he was looking at. The sentence says
              exactly that in the words he would use, and each word he can
              change is the control that changes it. The count and the
              ordering's freshness end the sentence, because §8.5 asks a
              ranked list to say what it is ranked by and when, and the answer
              to the first is on the word "sorted by". */}
          <p className="feed-sentence">
            Showing{" "}
            <FeedMenu
              name="needs"
              label={needsMe ? NEEDS_WORD : EVERYTHING_WORD}
              title="Whether the list is only the posts waiting on you (m)"
              open={pick === "needs"}
              setOpen={setPick}
            >
              <button
                type="button"
                role="menuitemradio"
                data-needs="me"
                aria-checked={needsMe}
                onClick={() => {
                  setPick(null);
                  chooseNeeds(true);
                }}
              >
                <span>What needs me</span>
                <span className="feed-menu-note">a ruling or an answer is waiting</span>
              </button>
              <button
                type="button"
                role="menuitemradio"
                data-needs="all"
                aria-checked={!needsMe}
                onClick={() => {
                  setPick(null);
                  chooseNeeds(false);
                }}
              >
                <span>Everything</span>
                <span className="feed-menu-note">every post Babel has produced</span>
              </button>
            </FeedMenu>
            {" · sorted by "}
            <FeedMenu
              name="sort"
              wide
              label={windowed(sort) ? `${SORT_WORD[sort]} · ${WINDOW_LABEL[t]}` : SORT_WORD[sort]}
              title={SORT_BASIS[sort]}
              open={pick === "sort"}
              setOpen={setPick}
            >
              <div className="feed-menu-column" role="group" aria-label="Order">
                {FEED_SORTS.map((name) => (
                  <button
                    type="button"
                    key={name}
                    role="menuitemradio"
                    data-sort={name}
                    aria-checked={sort === name}
                    title={SORT_BASIS[name]}
                    onClick={() => {
                      // An order computed over a period needs the period, so
                      // choosing one of those two opens the column that names
                      // it rather than closing and asking for a second press.
                      if (!windowed(name)) setPick(null);
                      select("sort", name);
                    }}
                  >
                    {SORT_LABEL[name]}
                  </button>
                ))}
              </div>
              {/* The period belongs to the two orders that read it and is
                  absent for the four that do not: a period selector beside
                  "newest" is a control that does nothing and does not say
                  so. */}
              {windowed(sort) && (
                <div className="feed-menu-column" role="group" aria-label="Period">
                  <p className="feed-menu-head">Over</p>
                  {FEED_WINDOWS.map((name) => (
                    <button
                      type="button"
                      key={name}
                      role="menuitemradio"
                      data-window={name}
                      aria-checked={t === name}
                      onClick={() => {
                        setPick(null);
                        select("t", name === "day" ? "" : name);
                      }}
                    >
                      {WINDOW_LABEL[name]}
                    </button>
                  ))}
                </div>
              )}
            </FeedMenu>
            {" · "}
            <FeedMenu
              name="kinds"
              label={kindsWord(kinds)}
              title="Which kinds of post are in the list (c)"
              open={pick === "kinds"}
              setOpen={setPick}
            >
              <button
                type="button"
                role="menuitemradio"
                data-kind="all"
                aria-checked={kinds.length === 0}
                onClick={() => {
                  setPick(null);
                  select("kind", "");
                }}
              >
                <span className="feed-menu-tick" aria-hidden="true">
                  {kinds.length === 0 ? "✓" : ""}
                </span>
                <span>All kinds</span>
              </button>
              {/* A set rather than a choice, so the menu stays open while it
                  is being built: pressing a second kind widens the list, and
                  a menu that closed after the first would make widening a
                  four-press gesture. */}
              {FEED_KINDS.map((kind) => (
                <button
                  type="button"
                  key={kind}
                  role="menuitemcheckbox"
                  data-kind={kind}
                  aria-checked={kinds.includes(kind)}
                  onClick={() => toggleKind(kind)}
                >
                  <span className="feed-menu-tick" aria-hidden="true">
                    {kinds.includes(kind) ? "✓" : ""}
                  </span>
                  <span>{kindLabel(kind)}</span>
                </button>
              ))}
            </FeedMenu>
            {answer && <span className="feed-count"> · {total.toLocaleString()}</span>}
            {built && <span className="feed-ranked"> · ranked {built.relative}</span>}
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

          {/* The list, and the floor under it. The floor is what stops a
              narrower answer from shortening the page: without it, moving from
              fifteen rows to two pulled the footer up nine hundred pixels and
              took the sticky rail with it, so every filter change was a jump
              as well as a read. */}
          <div className="feed-results" aria-busy={loading || appending}>
            {/* A read is in flight. It is a line rather than a spinner
                because the rows under it are still true — a spinner over live
                content says the content is not there. */}
            {(loading || appending) && <span className="feed-progress" aria-hidden="true" />}

            {/* A cold load, drawn as the rows it is about to be. The blocks
                are inside the row's own elements, so their height is the real
                row's height by construction rather than by a number somebody
                has to keep in step.

                It is deliberately not a `.feed-list`: that class means "these
                are posts" to every reader and to every test that waits for
                one, and a placeholder wearing it would be fifteen rows of
                nothing answering to the name. */}
            {posts === null && loading && !error && (
              <ol className="feed-waiting" aria-hidden="true">
                {SKELETON_ROWS.map((row) => (
                  <li className="feed-row feed-skeleton" key={row}>
                    <span className="feed-claim">
                      <span className="feed-skeleton-block">&nbsp;</span>
                    </span>
                    <span className="feed-facts">
                      <span className="feed-skeleton-block">&nbsp;</span>
                    </span>
                  </li>
                ))}
              </ol>
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
// Two lines and no box: the claim as a link, then the facts a reader decides
// with — Babel's score when there is one, what kind of thing it is, where it
// is filed, who wrote it, how old it is, how much has been said about it. A
// fact this record does not have is absent rather than empty: an unattributed
// record carries no "by", an unfiled one carries no topic, and an unassessed
// one carries no score.
//
// The score used to hold a column of its own with an em dash in it wherever
// no reviewer had voted, which on the arriving front page was most rows: a
// 44px gutter of dashes down the left of the list, spending the reader's first
// glance on an absence. It is a figure at the head of the fact line now, and
// a record nobody has assessed simply has no figure.
//
// The acts are hidden until the row is under the pointer, holds the keyboard,
// or is the row `j`/`k` put the focus on. Five controls on every waiting row
// meant seventy-five buttons on the first screen — the operator asked whether
// a row needs its acts "at all time, or only on hover", and the answer a list
// of fifteen gives is on hover. They are still on the awaiting rows only: a
// row that offered a ruling on a record already ruled on would be offering to
// overwrite an append-only decision, and one that offered it on a question
// would be offering to rule on something answered somewhere else.
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
  // number. A record no reviewer has assessed carries no figure rather than a
  // zero, which §8.5 refuses: an unreviewed record rendered as a zero reads as
  // one nobody objected to.
  const voted = post.support + post.oppose + post.unsure > 0;
  const breakdown = `Babel's reviewers: ${post.support} support, ${post.oppose} oppose, ${post.unsure} unsure`;
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
      {/* The row's own filings travel with the click. There is no route
          that reads one record's filings — the peel carries none — so the
          topics a reader can see on the row are the topics the page he
          opens can show, and the alternative is a post that loses what it
          is about by being opened. */}
      <Link className="feed-claim untrusted-inline" to={post.href} state={{ topics: post.topics }}>
        {post.title || "a record with no title recorded"}
      </Link>
      <span className="feed-facts">
        {/* Babel's score, read-only, with what it is made of one gesture
            away. It is a figure and is set as one; it is not a control,
            because the operator does not vote. */}
        {voted && (
          <span className="feed-score" title={breakdown} aria-label={breakdown}>
            {post.score}
          </span>
        )}
        <span className="feed-kind" data-tone={KIND_TONES[post.kind]}>
          {kindLabel(post.kind)}
        </span>
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
          <RuleActs id={post.id} kind={kind} acts={ROW_ACTS} onActed={onActed} plain />
        </div>
      )}
    </li>
  );
}

// Every topic, which is where the rail's "all topics" goes.
//
// It is the same three lists the rail carries, read at a size that can afford
// the figures: what the operator has accepted with where he stands toward it,
// what Babel has proposed about a topic and nobody has ruled on, and how much
// is filed under neither. Nothing here is a page about storage — a topic is an
// entity bound to something real (§4.13) — and no row prints a path, because a
// binding's identity can be a checkout directory and a locator is evidence
// about a topic rather than the topic.
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
  const proposed = answer?.proposed ?? [];

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
          <strong>Nobody has created a topic yet</strong>
          <span>
            A topic is what a record is about, and only you create one: Babel proposes an
            identity and the proposal is what you rule on.{" "}
            <Link to="/">The feed</Link> holds every post either way.
          </span>
        </div>
      )}

      {topics.length > 0 && (
        <ul className="topic-index">
          {topics.map((row) => {
            const latest = formatTime(row.latest_at);
            const state = row.interest.state as InterestState;
            return (
              <li key={row.id || row.name}>
                {/* What the name is bound to stays in the title, for the one
                    gesture that asks: §4.13 is explicit that a binding is
                    evidence about a topic and not the topic, and its identity
                    may be a path. */}
                <Link to={`/t/${encodeURIComponent(row.name)}`} title={bindingTitle(row)}>
                  t/{row.name}
                </Link>
                <span className="topic-facts">
                  {row.posts.toLocaleString()} {row.posts === 1 ? "post" : "posts"}
                  {row.awaiting > 0 && <> · {row.awaiting.toLocaleString()} waiting on you</>}
                  {latest && <> · newest {latest.relative}</>}
                  {/* The stance, in the operator's own vocabulary. An empty
                      state says nothing rather than reading as one of the
                      four: silence is not a refusal. */}
                  {row.interest.state && (
                    <> · {INTEREST_LABEL[state] ?? row.interest.state}</>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {proposed.length > 0 && (
        <>
          <h2 className="topic-index-heading">Babel proposes</h2>
          <ul className="topic-index">
            {proposed.map((row) => (
              <li key={row.proposal_id}>
                <Link to={`/r/${encodeURIComponent(row.proposal_id)}`}>{row.title}</Link>
                <span className="topic-facts">
                  {row.posts.toLocaleString()} {row.posts === 1 ? "record" : "records"}
                  {row.why && <> · {row.why}</>}
                  {row.run_id && <> · by {row.run_id}</>}
                </span>
              </li>
            ))}
          </ul>
          <p className="topic-note">
            Each of these is an ordinary proposal: you rule on it where you read it, and Babel
            performs what it proposed.
          </p>
        </>
      )}

      {answer && answer.unfiled > 0 && (
        <p className="topic-note">
          <Link to={`/?topic=${UNFILED}&needs=${NEEDS_ALL}`}>
            {answer.unfiled.toLocaleString()} posts
          </Link>{" "}
          are filed under nothing. Unfiled is an honest state and the triage backlog, not a bin.
        </p>
      )}
    </section>
  );
}

// What a topic's binding says when the pointer rests on its name: the kind,
// the identity, and how many checkouts were seen. The paths themselves are
// the topic page's fold — they are locators, and a list of them in a title
// would be the reading path printing filesystem paths by another route.
function bindingTitle(row: TopicRow): string | undefined {
  if (!row.binding) return undefined;
  const seen = row.binding.paths?.length ?? 0;
  const where = seen > 0 ? ` · ${seen} ${seen === 1 ? "checkout" : "checkouts"}` : "";
  return `${row.binding.kind}: ${row.binding.remote || row.binding.identity}${where}`;
}


export default FeedPage;
