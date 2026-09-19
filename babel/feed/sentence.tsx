import { useEffect, useRef, type ReactElement, type ReactNode } from "react";
import {
  ESTABLISHED,
  FEED_GROUPINGS,
  FEED_SORTS,
  FEED_SURFACES,
  FEED_WINDOWS,
  POST_KINDS,
  type Established,
  type FeedGrouping,
  type FeedSort,
  type FeedSurface,
  type FeedWindow,
  type PostKind,
} from "../contract.ts";
import { since, type FeedQuery } from "./api.ts";

/*
  THE CONTROLS, AS ONE SENTENCE THE READER EDITS.

  "Showing your desk · sorted by next · all kinds · 128 · ranked 2m ago". They were a
  segmented control of six orderings, a second of six periods, six chips and a paragraph
  explaining all of it — four rows of chrome above a list of fifteen one-line posts. Each word
  he can change is the control that changes it, and the keys `s`, `c` and `m` are the panel's
  because the reader is looking at the LIST when he decides the list is wrong.

  A menu and not a `<select>`: two of the three are not one choice — the kinds are a set and
  the order carries a period beside it — and a native select holds neither. So everything a
  select gives for free is stated here: the button says it opens a menu and whether it is
  open, the arrows walk the items, Enter takes the one under the keyboard, Escape closes and
  hands the keyboard back, and a press outside closes.
*/

/** Which segment is open. One at a time, held by the panel, because the keys are the panel's. */
export type PickName = "surface" | "sort" | "kinds" | "established" | "group";

/**
 * How the list is grouped, and what each key means (#352). The words name the KEY and never a
 * concept: records sharing a topic is a fact the store holds, and "the same idea" is not.
 */
const GROUP_WORD: Record<FeedGrouping, string> = {
  none: "one by one",
  topic: "grouped by topic",
  recipe: "grouped by lens",
};

const GROUP_LABEL: Record<FeedGrouping, string> = {
  none: "One row per record",
  topic: "Group by topic",
  recipe: "Group by lens",
};

const GROUP_NOTE: Record<FeedGrouping, string> = {
  none: "every record competes for its own slot",
  topic: "records filed under one entity share a slot, best first",
  recipe: "records one recipe produced share a slot, best first",
};

/** The surface, as the sentence says it and as the menu explains it (#351). */
const SURFACE_WORD: Record<FeedSurface, string> = {
  desk: "your desk",
  queue: "the agent queue",
  shelf: "the shelf",
  all: "everything",
};

const SURFACE_LABEL: Record<FeedSurface, string> = {
  desk: "Your desk",
  queue: "The agent queue",
  shelf: "The shelf",
  all: "Everything",
};

/** What each surface is FOR, which is who acts on what is in it. */
const SURFACE_NOTE: Record<FeedSurface, string> = {
  desk: "a ruling or an answer is waiting on you",
  queue: "an agent could do it without you: accepted, or sent back for refinement",
  shelf:
    "kept rather than shown — candidates Babel is still developing, and what you have already decided",
  all: "every post Babel has produced, in one list",
};

/**
 * The order each surface arrives under. The desk is a queue to drain, so it arrives by what is
 * waiting longest; the other three are a corpus to read, so they arrive hot.
 */
const SURFACE_SORT: Record<FeedSurface, FeedSort> = {
  desk: "next",
  queue: "next",
  shelf: "hot",
  all: "hot",
};

/** What each ordering is computed from, in the reader's terms. The formula is the store's. */
const SORT_BASIS: Record<FeedSort, string> = {
  next: "What is waiting on you: the most urgent first, and at equal urgency a proposal before a finding before a candidate, oldest first.",
  hot: "The score against how long ago the post arrived.",
  new: "Newest first, and nothing else.",
  top: "The highest score inside the window.",
  controversial:
    "Only the records Babel's reviewers split on — both sides inside one question — most evenly split first.",
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

/** The same six words inside the sentence, where they are read as part of it. */
const SORT_WORD: Record<FeedSort, string> = {
  next: "next",
  hot: "hot",
  new: "newest",
  top: "top",
  controversial: "controversial",
  rising: "rising",
};

const WINDOW_LABEL: Record<FeedWindow, string> = {
  hour: "this hour",
  day: "today",
  week: "this week",
  month: "this month",
  year: "this year",
  all: "all time",
};

const KIND_PLURAL: Record<PostKind, string> = {
  proposal: "proposals",
  finding: "findings",
  hypothesis: "hypotheses",
  question: "questions",
};

const KIND_LABEL: Record<PostKind, string> = {
  proposal: "Proposals",
  finding: "Findings",
  hypothesis: "Hypotheses",
  question: "Questions",
};

/** The status axis in the sentence's own voice, and what each value is read off. */
const ESTABLISHED_LABEL: Record<Established, string> = {
  unsettled: "Unjudged",
  contested: "Shaky",
  reviewed: "Reviewed",
  settled: "Settled",
};

const ESTABLISHED_NOTE: Record<Established, string> = {
  unsettled: "no ruling and no vote: nothing has judged it",
  contested: "Babel's reviewers are on both sides inside one role",
  reviewed: "assessed and not split, and still undecided",
  settled: "you ruled on it, or the question reached an end",
};

const ESTABLISHED_WORD: Record<Established, string> = {
  unsettled: "unjudged",
  contested: "shaky",
  reviewed: "reviewed",
  settled: "settled",
};

/**
 * What the status segment says. The subject axis is the topic and is chosen elsewhere; this is
 * the other one, and naming the chosen values rather than counting them is what makes the
 * sentence readable at two of four.
 */
function establishedWord(chosen: readonly Established[]): string {
  const [first, second] = chosen;
  if (first === undefined) return "any standing";
  if (second === undefined) return ESTABLISHED_WORD[first];
  if (chosen.length === 2) return `${ESTABLISHED_WORD[first]} and ${ESTABLISHED_WORD[second]}`;
  return `${String(chosen.length)} standings`;
}

/** The two orders computed over a period. The other four read no window and are offered none. */
const WINDOWED: Record<FeedSort, boolean> = {
  next: false,
  hot: false,
  new: false,
  top: true,
  controversial: true,
  rising: false,
};

/**
 * What the kinds segment says. One kind is named, two are named, and past that the sentence
 * counts them: "proposals, findings and hypotheses" is longer than the sentence it is inside.
 */
function kindsWord(chosen: readonly PostKind[]): string {
  const [first, second] = chosen;
  if (first === undefined) return "all kinds";
  if (second === undefined) return KIND_PLURAL[first];
  if (chosen.length === 2) return `${KIND_PLURAL[first]} and ${KIND_PLURAL[second]}`;
  return `${chosen.length} kinds`;
}

function Menu({
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
  wide?: boolean;
  open: boolean;
  setOpen: (next: PickName | null) => void;
  children: ReactNode;
}): ReactElement {
  const surface = useRef<HTMLSpanElement | null>(null);
  const opener = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!open) return undefined;
    const box = surface.current;
    box?.querySelector<HTMLElement>("[role^='menuitem']")?.focus();
    function onPointerDown(event: PointerEvent): void {
      if (box === null || !box.contains(event.target as Node)) setOpen(null);
    }
    function onKey(event: KeyboardEvent): void {
      if (event.key === "Escape") {
        event.preventDefault();
        setOpen(null);
        opener.current?.focus();
        return;
      }
      if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
      const items = [...(box?.querySelectorAll<HTMLElement>("[role^='menuitem']") ?? [])];
      if (items.length === 0) return;
      event.preventDefault();
      const at = items.indexOf(document.activeElement as HTMLElement);
      const step = event.key === "ArrowDown" ? 1 : -1;
      // A wrap rather than a stop: the list is five items long and the reader holding the key
      // down is looking for one of them, not for the end.
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
    <span className="babel-pick" ref={surface}>
      <button
        type="button"
        ref={opener}
        data-pick={name}
        className="babel-pick-button"
        aria-haspopup="menu"
        aria-expanded={open}
        title={title}
        onClick={() => setOpen(open ? null : name)}
      >
        {label}
        <span className="babel-pick-caret" aria-hidden="true">
          ▾
        </span>
      </button>
      {open && (
        <div
          className={wide ? "babel-menu babel-menu-wide" : "babel-menu"}
          role="menu"
          aria-label={title}
        >
          {children}
        </div>
      )}
    </span>
  );
}

export function Sentence({
  query,
  onQuery,
  pick,
  setPick,
  total,
  desk,
  builtAt,
  ruledToday,
  counted,
  now,
  heading,
}: {
  query: FeedQuery;
  onQuery: (next: FeedQuery) => void;
  pick: PickName | null;
  setPick: (next: PickName | null) => void;
  /** Absent until the first read answers: a count the panel has not been told is not a nought. */
  total: number | null;
  /** How many posts are on the desk, whatever surface is being read. Absent until answered. */
  desk: number | null;
  builtAt: string;
  ruledToday: number;
  counted: boolean;
  now: number;
  /** On a topic the sentence sits under a header and is a control again, not the heading. */
  heading?: boolean;
}): ReactElement {
  const ranked = since(builtAt, now);
  return (
    // A div and not a `<p>`: each editable word carries a real menu, and a browser closes a
    // paragraph the moment a div opens inside it — which would put the list's own rows
    // outside the sentence they belong to.
    <div className="babel-sentence" data-heading={heading === true ? "" : undefined}>
      Showing{" "}
      <Menu
        name="surface"
        label={SURFACE_WORD[query.surface]}
        title="Which surface the list is: what needs you, what an agent could do, or what is kept (m)"
        open={pick === "surface"}
        setOpen={setPick}
      >
        {FEED_SURFACES.map((name) => (
          <button
            type="button"
            key={name}
            role="menuitemradio"
            data-surface={name}
            aria-checked={query.surface === name}
            onClick={() => {
              setPick(null);
              // Changing surface changes what the list is FOR, so it brings the order that
              // surface is read under: a queue to drain arrives by what has waited longest, a
              // corpus to read arrives hot.
              onQuery({ ...query, surface: name, sort: SURFACE_SORT[name], offset: 0 });
            }}
          >
            <span>{SURFACE_LABEL[name]}</span>
            <span className="babel-menu-note">{SURFACE_NOTE[name]}</span>
          </button>
        ))}
      </Menu>
      {" · "}
      <Menu
        name="group"
        label={GROUP_WORD[query.group]}
        title="Whether one concept occupies one slot, and by which existing key"
        open={pick === "group"}
        setOpen={setPick}
      >
        {FEED_GROUPINGS.map((name) => (
          <button
            type="button"
            key={name}
            role="menuitemradio"
            data-group={name}
            aria-checked={query.group === name}
            onClick={() => {
              setPick(null);
              onQuery({ ...query, group: name, offset: 0 });
            }}
          >
            <span>{GROUP_LABEL[name]}</span>
            <span className="babel-menu-note">{GROUP_NOTE[name]}</span>
          </button>
        ))}
      </Menu>
      {" · sorted by "}
      <Menu
        name="sort"
        wide
        label={
          WINDOWED[query.sort]
            ? `${SORT_WORD[query.sort]} · ${WINDOW_LABEL[query.window]}`
            : SORT_WORD[query.sort]
        }
        title={SORT_BASIS[query.sort]}
        open={pick === "sort"}
        setOpen={setPick}
      >
        <div className="babel-menu-column" role="group" aria-label="Order">
          {FEED_SORTS.map((name) => (
            <button
              type="button"
              key={name}
              role="menuitemradio"
              data-sort={name}
              aria-checked={query.sort === name}
              title={SORT_BASIS[name]}
              onClick={() => {
                // An order computed over a period needs the period, so choosing one of those
                // two opens the column that names it rather than asking for a second press.
                if (!WINDOWED[name]) setPick(null);
                onQuery({ ...query, sort: name, offset: 0 });
              }}
            >
              {SORT_LABEL[name]}
            </button>
          ))}
        </div>
        {WINDOWED[query.sort] && (
          <div className="babel-menu-column" role="group" aria-label="Period">
            <span className="babel-menu-head">Over</span>
            {FEED_WINDOWS.map((name) => (
              <button
                type="button"
                key={name}
                role="menuitemradio"
                data-window={name}
                aria-checked={query.window === name}
                onClick={() => {
                  setPick(null);
                  onQuery({ ...query, window: name, offset: 0 });
                }}
              >
                {WINDOW_LABEL[name]}
              </button>
            ))}
          </div>
        )}
      </Menu>
      {" · "}
      <Menu
        name="kinds"
        label={kindsWord(query.kinds)}
        title="Which kinds of post are in the list (c)"
        open={pick === "kinds"}
        setOpen={setPick}
      >
        <button
          type="button"
          role="menuitemradio"
          data-kind="all"
          aria-checked={query.kinds.length === 0}
          onClick={() => {
            setPick(null);
            onQuery({ ...query, kinds: [], offset: 0 });
          }}
        >
          <span className="babel-menu-tick" aria-hidden="true">
            {query.kinds.length === 0 ? "✓" : ""}
          </span>
          <span>All kinds</span>
        </button>
        {/* A set rather than a choice, so the menu stays open while it is built: pressing a
            second kind widens the list, and a menu that closed would make that four presses. */}
        {POST_KINDS.map((kind) => (
          <button
            type="button"
            key={kind}
            role="menuitemcheckbox"
            data-kind={kind}
            aria-checked={query.kinds.includes(kind)}
            onClick={() =>
              onQuery({
                ...query,
                kinds: query.kinds.includes(kind)
                  ? query.kinds.filter((name) => name !== kind)
                  : [...query.kinds, kind],
                offset: 0,
              })
            }
          >
            <span className="babel-menu-tick" aria-hidden="true">
              {query.kinds.includes(kind) ? "✓" : ""}
            </span>
            <span>{KIND_LABEL[kind]}</span>
          </button>
        ))}
      </Menu>
      {" · "}
      <Menu
        name="established"
        label={establishedWord(query.established)}
        title="How well established a post is: the other axis, narrowed on its own"
        open={pick === "established"}
        setOpen={setPick}
      >
        <button
          type="button"
          role="menuitemradio"
          data-established="any"
          aria-checked={query.established.length === 0}
          onClick={() => {
            setPick(null);
            onQuery({ ...query, established: [], offset: 0 });
          }}
        >
          <span className="babel-menu-tick" aria-hidden="true">
            {query.established.length === 0 ? "✓" : ""}
          </span>
          <span>Any standing</span>
        </button>
        {/* A set, like the kinds: "shaky or unjudged" is one question about one axis, and a
            menu that closed on the first press would make it two. */}
        {ESTABLISHED.map((value) => (
          <button
            type="button"
            key={value}
            role="menuitemcheckbox"
            data-established={value}
            aria-checked={query.established.includes(value)}
            title={ESTABLISHED_NOTE[value]}
            onClick={() =>
              onQuery({
                ...query,
                established: query.established.includes(value)
                  ? query.established.filter((name) => name !== value)
                  : [...query.established, value],
                offset: 0,
              })
            }
          >
            <span className="babel-menu-tick" aria-hidden="true">
              {query.established.includes(value) ? "✓" : ""}
            </span>
            <span>{ESTABLISHED_LABEL[value]}</span>
          </button>
        ))}
      </Menu>
      {total !== null && (
        <span className="babel-count" data-ticked={counted ? "" : undefined}>
          {" "}
          · {total.toLocaleString()}
        </span>
      )}
      {/* The desk's size, when the count beside it is not already it. "Is this a plausible
          amount of work" is a question he has while reading the shelf, and a number he has to
          change surface to see is a number he does not have. */}
      {desk !== null && total !== desk && (
        <span className="babel-desk" title="Posts waiting on you, whatever this list is showing">
          {" "}
          · {desk.toLocaleString()} on your desk
        </span>
      )}
      {ranked !== "" && <span className="babel-ranked"> · ranked {ranked}</span>}
      {ruledToday > 0 && (
        <span className="babel-ruled" title="Rulings you have recorded in this reading">
          {" "}
          · ruled today {ruledToday.toLocaleString()}
        </span>
      )}
    </div>
  );
}
