import { useEffect, useRef, useState, type ReactElement, type ReactNode } from "react";
import type { HostServices, PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, ScrollRegion, Sidebar, Stack } from "@manifold/ui";
import { ACTIONS, FEED_PLUGIN_ID } from "../contract.ts";
import {
  BABEL_NODE,
  ask,
  look,
  refusal,
  useSelection,
  type FeedPost,
  type FeedQuery,
  type FeedResult,
} from "./api.ts";
import { Pulse, TopicRail } from "./rail.tsx";
import { FeedRow, RULE_KEYS, type Acted, type RuleAct } from "./rows.tsx";
import { Sentence, type PickName } from "./sentence.tsx";

/*
  HOME (§8.7), and there is one list.

  Every record Babel has produced is a post; the queue is a filter and a sort, not a second
  list. Nothing here computes an ordering — the six sorts are the store's and are tested
  there — and nothing here votes: Babel votes, the operator rules.

  THE LIST IS LIVE. It re-reads itself while the reader is on it, because the corpus does not
  stop when he opens the panel: a run publishes, a reviewer votes, a question he answered in
  another tile stops waiting on him. What the last read changed says so — a row that arrived
  wears a halo for four seconds, a score that moved ticks, a row that stopped waiting folds —
  and three things hold the read back, each of them a reader mid-gesture: a menu is open, a
  confirmation is on screen, or the browser tab is hidden (the shared feed's own rule).

  WHAT A RULING DOES TO THE LIST. Under "what needs me" the list is what is left to do, so an
  accepted row folds out of it, the count ticks down, and the way back is offered for six
  seconds — because the act is permanent and append-only, and "reopen" is the ruling that
  undoes it rather than a deletion.
*/

/** How many rows one read brings, and how many more each press appends. */
const PAGE = 15;

/** How long a row that arrived wears its halo, and how long a fold takes. */
const HALO_MS = 4_000;
const FOLD_MS = 220;
const TICK_MS = 400;

/** How long a ruling's way back stays offered: long enough to change your mind, short enough to go. */
const TOAST_MS = 6_000;

/** The fallback cadence when no event has arrived. The rail and the pulse read on the same beat. */
const LIVE_MS = 15_000;

export const EMPTY_QUERY: FeedQuery = {
  sort: "next",
  window: "day",
  kinds: [],
  needs: "me",
  limit: PAGE,
  offset: 0,
};

/** A ruling this reading recorded, and what it takes to undo it. */
interface Ruled {
  readonly id: string;
  readonly done: string;
}

/** A row on its way out: its post, and where it was standing when it left. */
interface Leaving {
  readonly post: FeedPost;
  readonly index: number;
}

function isTyping(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    target.closest("input, textarea, select, [contenteditable='true']") !== null
  );
}

/**
 * THE LIST, with the sentence over it. Home mounts it whole and a topic mounts it under its
 * own header, narrowed by the same query — a topic is the same feed narrowed to one
 * community, and a second listing is the surface §8.7 exists to delete.
 */
export function FeedListing({
  host,
  query,
  onQuery,
  heading,
}: {
  host: HostServices;
  query: FeedQuery;
  onQuery: (next: FeedQuery) => void;
  heading?: ReactNode;
}): ReactElement {
  const selection = useSelection();
  const [pick, setPick] = useState<PickName | null>(null);
  const [focus, setFocus] = useState(-1);
  const [acted, setActed] = useState<Record<string, Acted>>({});
  const [announcement, setAnnouncement] = useState("");
  const [failure, setFailure] = useState("");
  const [arrived, setArrived] = useState<readonly string[]>([]);
  const [ticked, setTicked] = useState<readonly string[]>([]);
  const [leaving, setLeaving] = useState<readonly Leaving[]>([]);
  const [counted, setCounted] = useState(false);
  const [toast, setToast] = useState<Ruled | null>(null);
  const [ruledToday, setRuledToday] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  const rows = useRef(new Map<string, HTMLLIElement>());
  const seen = useRef<{ readonly asked: string; readonly posts: readonly FeedPost[] } | null>(null);
  const held = useRef(false);
  held.current = pick !== null || Object.keys(acted).length > 0;

  const wire = {
    sort: query.sort,
    window: query.window,
    kinds: query.kinds,
    needs: query.needs,
    limit: query.limit,
    offset: query.offset,
    ...(query.topic === undefined || query.topic === "" ? {} : { topic: query.topic }),
  };
  const asked = JSON.stringify(wire);
  const feed = usePolledResource<FeedResult | null>(async () => ask(host, ACTIONS.feed, wire), LIVE_MS, {
    key: "atyrode.babel.feed",
    restartKey: asked,
    initial: null,
    topics: [BABEL_NODE],
    events: host.client,
    // A confirmation on screen is a permanent act being written, and the row it belongs to
    // must still be there when it is recorded.
    hold: () => held.current,
    onError: (reason) => setFailure(refusal(reason)),
    onSuccess: () => setFailure(""),
  });

  const answer = feed.value;
  const posts = answer?.posts ?? [];
  const total = answer?.total ?? null;

  // What the LAST READ changed, which is only a question when the reader did not change the
  // question: a re-sort is a different list and every row in it is new by construction, so
  // haloing them would be the panel announcing the reader's own gesture back at him. The
  // motion below is for the world moving under him.

  useEffect(() => {
    if (answer === null) return undefined;
    const previous = seen.current;
    seen.current = { asked, posts: answer.posts };
    setNow(Date.now());
    if (previous === null || previous.asked !== asked) return undefined;
    const before = previous.posts;
    const fresh = answer.posts.filter((post) => !before.some((row) => row.id === post.id));
    const moved = answer.posts.filter((post) =>
      before.some((row) => row.id === post.id && row.score !== post.score),
    );
    const gone = before
      .map((post, index): Leaving => ({ post, index }))
      .filter((row) => !answer.posts.some((post) => post.id === row.post.id));
    if (fresh.length > 0) setArrived(fresh.map((post) => post.id));
    if (moved.length > 0) setTicked(moved.map((post) => post.id));
    if (gone.length > 0) setLeaving(gone);
    const timers = [
      window.setTimeout(() => setArrived([]), HALO_MS),
      window.setTimeout(() => setTicked([]), TICK_MS),
      window.setTimeout(() => setLeaving([]), FOLD_MS),
    ];
    return () => {
      for (const timer of timers) window.clearTimeout(timer);
    };
  }, [answer, asked]);

  useEffect(() => {
    if (toast === null) return undefined;
    const timer = window.setTimeout(() => setToast(null), TOAST_MS);
    return () => window.clearTimeout(timer);
  }, [toast]);

  // The ring is a real DOM focus, so the region scrolls the row into view and a screen reader
  // follows it. It is never taken back from a control inside the row that already holds it.
  const focused = focus >= 0 ? posts[focus] : undefined;
  const focusedId = focused?.id ?? "";
  useEffect(() => {
    if (focusedId === "") return;
    const element = rows.current.get(focusedId);
    if (element === undefined || element.contains(document.activeElement)) return;
    element.focus();
  }, [focusedId]);

  /** What a row's act did, and what the list does about it. */
  function recorded(post: FeedPost, act: RuleAct | "answer", done: string, message: string): void {
    setAnnouncement(message);
    if (act === "ask") {
      feed.setValue((current) =>
        current === null
          ? current
          : { ...current, posts: current.posts.map((row) => (row.id === post.id ? { ...row, comments: row.comments + 1 } : row)) },
      );
    }
    if (act === "accept" || act === "reject") {
      setRuledToday((count) => count + 1);
      setToast({ id: post.id, done });
      if (query.needs === "me") {
        const index = posts.findIndex((row) => row.id === post.id);
        setLeaving([{ post, index: index < 0 ? 0 : index }]);
        window.setTimeout(() => setLeaving([]), FOLD_MS);
        setCounted(true);
        window.setTimeout(() => setCounted(false), TICK_MS);
        feed.setValue((current) =>
          current === null
            ? current
            : { ...current, posts: current.posts.filter((row) => row.id !== post.id), total: Math.max(0, current.total - 1) },
        );
        return;
      }
    }
    setActed((current) => ({ ...current, [post.id]: { act, done, at: Date.now() } }));
  }

  /** The way back from a ruling, which is a ruling: the log is append-only, so reopen appends. */
  async function reopen(entry: Ruled): Promise<void> {
    setToast(null);
    try {
      await ask(host, ACTIONS.rule, { id: entry.id, ruling: "reopen", note: "reopened from the feed" });
      setAnnouncement("Reopened. It is waiting on you again.");
      setRuledToday((count) => Math.max(0, count - 1));
      feed.refresh();
    } catch (reason) {
      setAnnouncement(refusal(reason));
    }
  }

  useEffect(() => {
    function onKey(event: KeyboardEvent): void {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (isTyping(event.target)) return;
      // While a segment of the sentence is open it owns the keyboard: the arrows walk its
      // items and Escape closes it.
      if (pick !== null) return;
      if (event.key === "s" || event.key === "c") {
        event.preventDefault();
        setPick(event.key === "s" ? "sort" : "kinds");
        return;
      }
      if (event.key === "m") {
        event.preventDefault();
        onQuery(
          query.needs === "me"
            ? { ...query, needs: "all", sort: query.sort === "next" ? "hot" : query.sort, offset: 0 }
            : { ...query, needs: "me", sort: "next", offset: 0 },
        );
        return;
      }
      if (posts.length === 0) return;
      if (event.key === "j" || event.key === "k") {
        event.preventDefault();
        const next =
          event.key === "j" ? Math.min(posts.length - 1, focus + 1) : focus <= 0 ? 0 : focus - 1;
        setFocus(next);
        // The peek walks with the list once it is open, which is what makes `j`/`k` a way of
        // reading rather than a way of aiming.
        const post = posts[next];
        if (post !== undefined && selection.recordId !== "") look({ recordId: post.id });
        return;
      }
      if (focused === undefined) return;
      if (event.key === "Enter") {
        const inControl =
          event.target instanceof HTMLElement && event.target.closest("a, button, summary") !== null;
        if (inControl) return;
        event.preventDefault();
        look({ recordId: focused.id });
        return;
      }
      // `a` answers, and the rulings press the row's own control rather than posting: a
      // permanent, attributed event is never one keystroke away.
      const selector =
        event.key === "a"
          ? focused.kind === "question"
            ? '[data-answer="answered"]'
            : ""
          : RULE_KEYS[event.key] === undefined
            ? ""
            : `[data-ruling="${RULE_KEYS[event.key] ?? ""}"]`;
      if (selector === "") return;
      const control = rows.current.get(focused.id)?.querySelector<HTMLButtonElement>(selector);
      if (control === null || control === undefined) return;
      event.preventDefault();
      control.click();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [focus, focused, onQuery, pick, posts, query, selection.recordId]);

  const shown = [...posts];
  for (const gone of leaving) {
    if (shown.some((post) => post.id === gone.post.id)) continue;
    shown.splice(Math.min(gone.index, shown.length), 0, gone.post);
  }
  const filtered = query.kinds.length > 0 || query.needs === "me" || (query.topic ?? "") !== "";

  return (
    <Stack className="babel-listing" gap="var(--babel-space-3)">
      {heading}
      <Sentence
        query={query}
        onQuery={(next) => {
          setFocus(-1);
          setActed({});
          onQuery(next);
        }}
        pick={pick}
        setPick={setPick}
        total={total}
        builtAt={answer?.builtAt ?? ""}
        ruledToday={ruledToday}
        counted={counted}
        now={now}
        heading={heading !== undefined}
      />
      <p className="babel-said" role="status" aria-live="polite">
        {announcement}
      </p>
      {answer !== null && answer.notice !== "" && (
        <p className="babel-notice" role="status">
          {answer.notice}
        </p>
      )}
      {failure !== "" && (
        <div className="babel-state" role="alert">
          <strong>The feed could not be read.</strong>
          <span>{failure}</span>
          <button type="button" onClick={() => feed.refresh()}>
            Try again
          </button>
        </div>
      )}
      {answer === null && failure === "" && (
        <ol className="babel-waiting" aria-hidden="true">
          {[0, 1, 2, 3, 4, 5].map((row) => (
            <li className="babel-row babel-skeleton" key={row}>
              <div className="babel-votes">
                <span className="babel-skeleton-block babel-skeleton-score">&nbsp;</span>
              </div>
              <div className="babel-row-body">
                <span className="babel-claim">
                  <span className="babel-skeleton-block">&nbsp;</span>
                </span>
                <span className="babel-facts">
                  <span className="babel-skeleton-block">&nbsp;</span>
                </span>
              </div>
            </li>
          ))}
        </ol>
      )}
      {answer !== null && shown.length === 0 && (
        <div className="babel-state">
          <strong>
            {query.needs === "me" && !filtered
              ? "Nothing is waiting on you"
              : filtered
                ? "Nothing matches this view"
                : "Babel has not posted anything yet"}
          </strong>
          <span>
            {query.needs === "me" ? (
              <>
                Records arrive here when exploration develops them far enough to be worth a ruling.{" "}
                <button
                  type="button"
                  className="babel-link"
                  onClick={() => onQuery({ ...query, needs: "all", sort: "hot", offset: 0 })}
                >
                  Read everything
                </button>{" "}
                in the meantime.
              </>
            ) : (
              "That is a statement about the filters, not about what Babel has produced."
            )}
          </span>
        </div>
      )}
      {shown.length > 0 && (
        <ol className="babel-list">
          {shown.map((post, index) => (
            <FeedRow
              key={post.id}
              host={host}
              post={post}
              focused={index === focus}
              selected={selection.recordId === post.id}
              acted={acted[post.id]}
              ticked={ticked.includes(post.id)}
              arrived={arrived.includes(post.id)}
              leaving={leaving.some((row) => row.post.id === post.id)}
              now={now}
              onFocus={() => setFocus(index)}
              onOpen={() => look({ recordId: post.id })}
              onActed={(act, done, message) => recorded(post, act, done, message)}
              register={(element) => {
                if (element === null) rows.current.delete(post.id);
                else rows.current.set(post.id, element);
              }}
            />
          ))}
        </ol>
      )}
      {total !== null && shown.length > 0 && shown.length < total && (
        <Cluster className="babel-more" gap="var(--babel-space-3)" justify="center">
          <button type="button" onClick={() => onQuery({ ...query, limit: query.limit + PAGE })}>
            Show {Math.min(PAGE, total - shown.length)} more
          </button>
          <span className="babel-note">
            {shown.length.toLocaleString()} of {total.toLocaleString()}
          </span>
        </Cluster>
      )}
      {total !== null && shown.length > 0 && shown.length >= total && total > PAGE && (
        <p className="babel-note">That is all {total.toLocaleString()} of them.</p>
      )}
      {toast !== null && (
        <div className="babel-toast" role="status">
          <span>{toast.done}</span>
          <button type="button" onClick={() => void reopen(toast)}>
            reopen
          </button>
        </div>
      )}
    </Stack>
  );
}

export function HomePanel({ host }: PanelProps): ReactElement {
  const [query, setQuery] = useState<FeedQuery>(EMPTY_QUERY);
  const selection = useSelection();
  return (
    <ScrollRegion className={`plugin-${FEED_PLUGIN_ID.replaceAll(".", "_")}`} aria-label="Babel">
      <Stack className="babel-panel" gap="var(--babel-space-4)">
        <Pulse host={host} />
        <Sidebar side="end" sideWidth="15rem" contentMin="60%" gap="var(--babel-space-6)">
          <FeedListing host={host} query={query} onQuery={setQuery} />
          <TopicRail
            host={host}
            current={selection.topic}
            onUnfiled={() => setQuery({ ...query, topic: "unfiled", needs: "all", sort: "new", offset: 0 })}
          />
        </Sidebar>
      </Stack>
    </ScrollRegion>
  );
}
