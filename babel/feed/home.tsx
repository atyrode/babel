import { useEffect, useRef, useState, type ReactElement, type ReactNode } from "react";
import type { HostServices, PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, ScrollRegion, Sidebar, Stack } from "@manifold/ui";
import { ACTIONS, FEED_PLUGIN_ID, type FeedGrouping, type FeedSurface } from "../contract.ts";
import {
  BABEL_NODE,
  NO_SEAT,
  ask,
  look,
  openRecord,
  openTopic,
  refusal,
  useSelection,
  type FeedPost,
  type FeedQuery,
  type FeedResult,
} from "./api.ts";
import { Pulse, TopicRail } from "./rail.tsx";
import { FeedRow, RULE_KEYS, type Acted, type ActedKind } from "./rows.tsx";
import { Sentence, type PickName } from "./sentence.tsx";

/*
  HOME (§8.7), and there are three surfaces over one order.

  Every record Babel has produced is a post, and which of them a reader is shown is a ROUTE
  rather than a rank: the desk is what needs his judgement, the agent queue is what a run does
  next unattended, and the shelf is what is kept without being shown. All three narrow the one
  index the store ranks whole, so they cannot disagree about what is waiting, and none of them
  deletes anything — the shelf is reached by asking for it. Nothing here computes an ordering
  or a route; both are the store's and are tested there. And nothing here votes: Babel votes,
  the operator rules.

  THE LIST IS LIVE. It re-reads itself while the reader is on it, because the corpus does not
  stop when he opens the panel: a run publishes, a reviewer votes, a question he answered in
  another tile stops waiting on him. What the last read changed says so — a row that arrived
  wears a halo for four seconds, a score that moved ticks, a row that stopped waiting folds —
  and three things hold the read back, each of them a reader mid-gesture: a menu is open, a
  confirmation is on screen, or the browser tab is hidden (the shared feed's own rule).

  WHAT A RULING DOES TO THE LIST. On the desk the list is what is left to do, so an
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

/** What an empty surface says: a fact about the surface, never about the corpus. */
const NOTHING_HERE: Record<FeedSurface, string> = {
  desk: "Nothing is waiting on you",
  queue: "Nothing is queued for an agent",
  shelf: "Nothing is on the shelf",
  all: "Babel has not posted anything yet",
};

/** Why the two surfaces nobody is blocked on are empty, in their own terms. */
const SURFACE_MEANS: Record<FeedSurface, string> = {
  desk: "",
  queue: "A record reaches the queue when you accept it or send it back for refinement.",
  shelf:
    "The shelf is what Babel keeps without showing: candidates it is developing on its own, and records you have already decided.",
  all: "",
};

/**
 * Why the records under a heading are together, said on the heading. It names the KEY rather
 * than asserting a concept: these records share a topic or a recipe, which is a fact, where
 * "these are the same idea" would be a claim no existing column supports.
 */
const GROUP_WHY: Record<FeedGrouping, string> = {
  topic: "all filed under",
  recipe: "all found by the lens",
  none: "",
};

export const EMPTY_QUERY: FeedQuery = {
  sort: "next",
  window: "day",
  kinds: [],
  surface: "desk",
  established: [],
  // The desk arrives grouped, because a concept observed forty times occupying forty of its
  // fifteen slots is the defect; the topic is the key because it is what a record is about.
  group: "topic",
  limit: PAGE,
  offset: 0,
};

/**
 * A note at the foot of the list: what happened, and the way back when the act it announces
 * has one. A ruling does; a panel that had nowhere to open does not.
 */
interface Note {
  readonly said: string;
  /** The record `reopen` appends to, or "" when the note announces no ruling. */
  readonly reopens: string;
}

/** A note, and how long it stays: long enough to change your mind, short enough to go. */
function useNote(): readonly [Note | null, (note: Note | null) => void] {
  const [note, setNote] = useState<Note | null>(null);
  useEffect(() => {
    if (note === null) return undefined;
    const timer = window.setTimeout(() => setNote(null), TOAST_MS);
    return () => window.clearTimeout(timer);
  }, [note]);
  return [note, setNote];
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
  said,
}: {
  host: HostServices;
  query: FeedQuery;
  onQuery: (next: FeedQuery) => void;
  heading?: ReactNode;
  /**
   * A note the surface AROUND the list pushed at it — Home's rail, when a topic had nowhere
   * to open. One toast at the foot of the page, wherever the gesture came from.
   */
  said?: string;
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
  const [toast, setToast] = useNote();
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
    surface: query.surface,
    established: query.established,
    group: query.group,
    limit: query.limit,
    offset: query.offset,
    ...(query.topic === undefined || query.topic === "" ? {} : { topic: query.topic }),
  };
  const asked = JSON.stringify(wire);
  const feed = usePolledResource<FeedResult | null>(
    async () => ask(host, ACTIONS.feed, wire),
    LIVE_MS,
    {
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
    },
  );

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

  /** Opens a record: a seat of its own, and the selection points at it either way. */
  function openRecordHere(id: string): void {
    if (openRecord(host, id) === "no_tile") setToast({ said: NO_SEAT, reopens: "" });
  }

  /** Opens a topic: the same gesture from a row's chip as from the rail. */
  function openTopicHere(id: string): void {
    if (openTopic(host, id) === "no_tile") setToast({ said: NO_SEAT, reopens: "" });
  }

  /** What a row's act did, and what the list does about it. */
  function recorded(post: FeedPost, act: ActedKind, done: string, message: string): void {
    setAnnouncement(message);
    if (act === "ask") {
      feed.setValue((current) =>
        current === null
          ? current
          : {
              ...current,
              posts: current.posts.map((row) =>
                row.id === post.id ? { ...row, comments: row.comments + 1 } : row,
              ),
            },
      );
    }
    if (act === "accept" || act === "reject") {
      setRuledToday((count) => count + 1);
      setToast({ said: done, reopens: post.id });
      if (query.surface === "desk") {
        const index = posts.findIndex((row) => row.id === post.id);
        setLeaving([{ post, index: index < 0 ? 0 : index }]);
        window.setTimeout(() => setLeaving([]), FOLD_MS);
        setCounted(true);
        window.setTimeout(() => setCounted(false), TICK_MS);
        feed.setValue((current) =>
          current === null
            ? current
            : {
                ...current,
                posts: current.posts.filter((row) => row.id !== post.id),
                total: Math.max(0, current.total - 1),
                desk: Math.max(0, current.desk - 1),
              },
        );
        return;
      }
    }
    setActed((current) => ({ ...current, [post.id]: { act, done, at: Date.now() } }));
  }

  /** The way back from a ruling, which is a ruling: the log is append-only, so reopen appends. */
  async function reopen(entry: Note): Promise<void> {
    setToast(null);
    try {
      await ask(host, ACTIONS.rule, {
        id: entry.reopens,
        ruling: "reopen",
        note: "reopened from the feed",
      });
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
          query.surface === "desk"
            ? {
                ...query,
                surface: "all",
                sort: query.sort === "next" ? "hot" : query.sort,
                offset: 0,
              }
            : { ...query, surface: "desk", sort: "next", offset: 0 },
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
        // reading rather than a way of aiming. It POINTS and never opens: a seat per row the
        // reader scrolled past is a workspace nobody asked for.
        const post = posts[next];
        if (post !== undefined && selection.recordId !== "") look({ recordId: post.id });
        return;
      }
      if (focused === undefined) return;
      if (event.key === "Enter") {
        const inControl =
          event.target instanceof HTMLElement &&
          event.target.closest("a, button, summary") !== null;
        if (inControl) return;
        event.preventDefault();
        openRecordHere(focused.id);
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
  // A narrowing is the reader's own filters, and it is a different emptiness from a surface
  // with nothing on it: one is a statement about the query, the other about the corpus.
  const narrowed =
    query.kinds.length > 0 || query.established.length > 0 || (query.topic ?? "") !== "";
  // The controversial order is a narrowing as well as an order — it lists only the records
  // Babel's reviewers took both sides on inside one role — so an empty one is a fact about the
  // argument rather than about the corpus. It is also the one emptiness on this page a reader
  // could mistake for a finding: "nothing is contested" reads as "everything agrees" when it
  // may instead mean the reviewers were asked one question several ways, which the deployment
  // has not measured. The page says both halves rather than implying the first.
  const unsplit = query.sort === "controversial";
  // ONE toast at the foot of the page, whatever raised it: a ruling this list recorded, or a
  // note the surface around it pushed in. A refusal carries no way back, so it offers none.
  const note = toast ?? (said === undefined || said === "" ? null : { said, reopens: "" });

  // WHAT THE LIST IS MADE OF, once. A row is drawn the same way grouped or not, so the focus
  // ring, the peek and `j`/`k` walk one order whichever shape the page took: the index is the
  // post's place in `shown`, and the groups only decide where the rows sit on the screen.
  const placed: Record<string, number> = {};
  for (const [at, post] of shown.entries()) placed[post.id] = at;
  const row = (post: FeedPost): ReactElement => {
    const index = placed[post.id] ?? -1;
    return (
      <FeedRow
        key={post.id}
        host={host}
        post={post}
        focused={index === focus}
        selected={selection.recordId === post.id}
        acted={acted[post.id]}
        ticked={ticked.includes(post.id)}
        arrived={arrived.includes(post.id)}
        leaving={leaving.some((gone) => gone.post.id === post.id)}
        now={now}
        onFocus={() => setFocus(index)}
        onOpen={() => openRecordHere(post.id)}
        onTopic={openTopicHere}
        onActed={(act, done, message) => recorded(post, act, done, message)}
        register={(element) => {
          if (element === null) rows.current.delete(post.id);
          else rows.current.set(post.id, element);
        }}
      />
    );
  };

  // The groups, with every shown post landing in exactly one block: a row the store did not
  // put in a group — one folding out after a ruling, say — keeps its place in a block of its
  // own rather than vanishing because the grouping had no slot for it.
  const groups = answer?.groups ?? [];
  const blocks: { id: string; group: FeedResult["groups"][number] | null; posts: FeedPost[] }[] =
    [];
  if (groups.length > 0) {
    const claimed: Record<string, true> = {};
    for (const group of groups) {
      const held = group.posts
        .map((id) => shown.find((post) => post.id === id))
        .filter((post): post is FeedPost => post !== undefined);
      for (const post of held) claimed[post.id] = true;
      if (held.length === 0) continue;
      blocks.push({ id: `${group.keyKind}:${group.key}:${held[0]?.id ?? ""}`, group, posts: held });
    }
    const loose = shown.filter((post) => claimed[post.id] !== true);
    if (loose.length > 0) blocks.push({ id: "loose", group: null, posts: loose });
  }
  // What the page is cut out of, in the unit the answer counted: groups when it grouped.
  const listed = groups.length > 0 ? groups.length : shown.length;

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
        desk={answer?.desk ?? null}
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
            {unsplit
              ? "Nothing here is split"
              : narrowed
                ? "Nothing matches this view"
                : NOTHING_HERE[query.surface]}
          </strong>
          <span>
            {unsplit ? (
              "This order lists only the records Babel's reviewers took both sides on inside one question. That none did may mean they agree, or that they were asked one question several ways; Babel has not measured which."
            ) : narrowed ? (
              "That is a statement about the filters, not about what Babel has produced."
            ) : query.surface === "desk" ? (
              <>
                Records arrive here when exploration develops them far enough to be worth a ruling.{" "}
                <button
                  type="button"
                  className="babel-link"
                  onClick={() => onQuery({ ...query, surface: "all", sort: "hot", offset: 0 })}
                >
                  Read everything
                </button>{" "}
                in the meantime.
              </>
            ) : (
              SURFACE_MEANS[query.surface]
            )}
          </span>
        </div>
      )}
      {shown.length > 0 && blocks.length === 0 && <ol className="babel-list">{shown.map(row)}</ol>}
      {shown.length > 0 && blocks.length > 0 && (
        <ol className="babel-groups">
          {blocks.map((block) => (
            <li className="babel-group" key={block.id}>
              {/* The key is STATED, so a reader knows why these records are together rather
                  than inferring it from the rows. A group of one is a record that nothing
                  groups, or the only one under its key, and it needs no heading to explain
                  itself. */}
              {block.group !== null && block.group.records > 1 && (
                <p className="babel-group-head">
                  <span className="babel-group-key">
                    {GROUP_WHY[block.group.keyKind]} {block.group.label}
                  </span>
                  <span className="babel-note">
                    {block.group.records.toLocaleString()} records
                    {block.group.records > block.posts.length &&
                      `, ${(block.group.records - block.posts.length).toLocaleString()} more under it`}
                  </span>
                </p>
              )}
              <ol className="babel-list">{block.posts.map(row)}</ol>
            </li>
          ))}
        </ol>
      )}
      {total !== null && listed > 0 && listed < total && (
        <Cluster className="babel-more" gap="var(--babel-space-3)" justify="center">
          <button type="button" onClick={() => onQuery({ ...query, limit: query.limit + PAGE })}>
            Show {Math.min(PAGE, total - listed)} more
          </button>
          <span className="babel-note">
            {listed.toLocaleString()} of {total.toLocaleString()}
          </span>
        </Cluster>
      )}
      {total !== null && listed > 0 && listed >= total && total > PAGE && (
        <p className="babel-note">That is all {total.toLocaleString()} of them.</p>
      )}
      {note !== null && (
        <div className="babel-toast" role="status">
          <span>{note.said}</span>
          {note.reopens !== "" && (
            <button type="button" onClick={() => void reopen(note)}>
              reopen
            </button>
          )}
        </div>
      )}
    </Stack>
  );
}

export function HomePanel({ host }: PanelProps): ReactElement {
  const [query, setQuery] = useState<FeedQuery>(EMPTY_QUERY);
  const [railNote, setRailNote] = useNote();
  const selection = useSelection();
  return (
    <ScrollRegion className={`plugin-${FEED_PLUGIN_ID.replaceAll(".", "_")}`} aria-label="Home">
      <Stack className="babel-panel" gap="var(--babel-space-4)">
        <Pulse host={host} />
        <Sidebar side="end" sideWidth="15rem" contentMin="60%" gap="var(--babel-space-6)">
          <FeedListing host={host} query={query} onQuery={setQuery} said={railNote?.said ?? ""} />
          <TopicRail
            host={host}
            current={selection.topic}
            onTopic={(topic) =>
              setRailNote(
                openTopic(host, topic) === "no_tile" ? { said: NO_SEAT, reopens: "" } : null,
              )
            }
            onUnfiled={() =>
              setQuery({ ...query, topic: "unfiled", surface: "all", sort: "new", offset: 0 })
            }
          />
        </Sidebar>
      </Stack>
    </ScrollRegion>
  );
}
