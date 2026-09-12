// ⌘K: one input over everything this deployment can name.
//
// The operator's direction is one line — "⌘K: search records, sessions,
// entities; jump; act" — and the shape follows from the word "jump". This is a
// locator, not a search page: it answers with destinations, it is reached from
// anywhere without a click, and activating a row ends it. Nothing here
// paginates, filters or sorts, because every one of those is a reason to stay
// in a box the operator opened in order to leave.
//
// Results and commands share one list on purpose. A palette that made the
// operator choose between "find a thing" and "do a thing" before typing would
// be the navigation §8.6 replaced, in miniature: the question he has is
// "verif" or "watch", and which of the two it turns out to be is Babel's
// problem.
//
// Results come from GET /api/search/names, which matches the one line each row
// renders. That is what lets this file rank nothing and hide nothing: the
// server decided what matched and in what order, so a row on screen always
// contains what was typed, and the client's whole job is to group, move and
// open.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { dismissAPIError, request } from "./api";
import "./palette.css";

// NameHit is one row of GET /api/search/names: something this deployment can
// name, and the page that opens it. The href is the server's, for the reason
// stated there — five of the seven kinds route differently and the mapping is
// the server's knowledge, not this file's.
export interface NameHit {
  kind: string;
  id: string;
  title: string;
  href: string;
  meta?: string;
}

interface NameSearchResponse {
  hits: NameHit[];
}

// openPalette opens the mounted palette from anywhere — the shell's search
// control uses it. It is a no-op when no palette is mounted rather than an
// error: a header button that threw because a sibling component was absent
// would be a worse failure than a button that does nothing.
const openers = new Set<() => void>();

export function openPalette(): void {
  for (const open of openers) open();
}

// DEBOUNCE_MS is how long a keystroke waits before the corpus is scanned. The
// lookup reads the frontier's head revisions live rather than an index, so a
// request per keystroke would put four store scans behind a word typed at
// speed; a tenth of a second collapses them into one without the input ever
// feeling detached from it.
const DEBOUNCE_MS = 110;

// HIT_LIMIT is a palette's worth of rows. The server bounds each kind's share
// of it, so this is what the operator sees rather than what matched.
const HIT_LIMIT = 20;

// The order an operator wants the kinds in: what asks for a decision, then
// what Babel concluded, then what it is still guessing at, then the evidence
// and the conversations under all of it, then the ledger's own subjects. It is
// §8.6's peel applied to a list of records rather than to one.
//
// A kind this build has no rank for sorts last rather than first, so a server
// that learns to name something new is a new group at the bottom of the list
// instead of a row above the proposals.
const KIND_RANK: Record<string, number> = {
  proposal: 0,
  finding: 1,
  hypothesis: 2,
  observation: 3,
  session: 4,
  entity: 5,
  question: 6,
};
const UNRANKED_KIND = 99;

const KIND_LABEL: Record<string, string> = {
  proposal: "Proposals",
  finding: "Findings",
  hypothesis: "Candidates",
  observation: "Observations",
  session: "Sessions",
  entity: "Subjects",
  question: "Open questions",
};

// The destinations the shell's nav names, in its own words, so the palette
// and the header cannot come to describe the same place differently. Search
// is absent because it is this list: a row that opened the palette from
// inside the palette is not a destination.
const DESTINATIONS: { path: string; label: string; note: string }[] = [
  { path: "/", label: "Home", note: "the feed — everything Babel has produced" },
  { path: "/queue", label: "Mod queue", note: "what awaits a ruling from you" },
  { path: "/watch", label: "Watch", note: "what it is doing, and what it cost" },
  { path: "/settings", label: "Settings", note: "the archive, the ceilings, and what Babel is" },
];

const GROUP_GO = "Go";
const GROUP_DO = "Do";

// Row is one line of the list: a destination, or something to do. Both are one
// type because the list is one list — the keyboard moves through it without
// caring which kind of thing it is about to activate.
interface Row {
  key: string;
  group: string;
  title: string;
  meta?: string;
  // href is the hash route this row opens, present on every row that is a
  // destination. It is a real link as well as a keyboard target: hovering
  // shows where it goes, middle-click opens it in a tab, and ⌘-click does what
  // it does everywhere else. Issue #234 measured what a list of onclick
  // handlers costs a reader.
  href?: string;
  // act is what the row does when it is not a destination.
  act?: () => void;
}

// commandRows are the things to do that are not a record: the five
// destinations the shell's nav names, the run this machine can start, and the
// density the shell renders at.
function commandRows(query: string): Row[] {
  const rows: Row[] = DESTINATIONS.map((destination) => ({
    key: `go:${destination.path}`,
    group: GROUP_GO,
    title: `Go to ${destination.label}`,
    meta: destination.note,
    href: `#${destination.path}`,
  }));
  rows.push({
    key: "do:start",
    group: GROUP_DO,
    title: "Start a run",
    meta: "on this machine, under its ceilings",
    // The control room owns starting things, and #start is the section of it
    // that does: a palette that posted a launch itself would be a second place
    // the ceilings have to be checked.
    href: "#/watch#start",
  });
  rows.push({
    key: "do:density",
    group: GROUP_DO,
    title: "Toggle density",
    // The shell owns the setting and puts it on the document element, so this
    // reads the current value rather than keeping a second copy of it.
    meta: `now ${document.documentElement.dataset.density === "compact" ? "compact" : "comfortable"}`,
    // The shell listens for this. A palette that wrote the attribute itself
    // would be a second author of one piece of state, and the two would
    // disagree the first time either changed.
    act: () => document.dispatchEvent(new CustomEvent("babel:density")),
  });
  if (query === "") return rows;
  // A command matches on what it says, both halves of it: "cost" reaches Watch
  // through the sentence under it. Matching hidden text would put a row on
  // screen that does not contain what was typed, which is the one thing the
  // server's own matching refuses to do.
  const want = query.toLocaleLowerCase();
  return rows.filter((row) =>
    `${row.title} ${row.meta ?? ""}`.toLocaleLowerCase().includes(want),
  );
}

// resultRows groups what the lookup found without reordering within a kind:
// the sort is stable, so the server's ranking survives inside each group and
// only the groups themselves are arranged.
function resultRows(hits: NameHit[]): Row[] {
  const ordered = [...hits].sort(
    (a, b) => (KIND_RANK[a.kind] ?? UNRANKED_KIND) - (KIND_RANK[b.kind] ?? UNRANKED_KIND),
  );
  return ordered.map((hit) => ({
    key: `${hit.kind}:${hit.id}`,
    group: KIND_LABEL[hit.kind] ?? hit.kind,
    title: hit.title,
    meta: hit.meta,
    href: hit.href,
  }));
}

// group collapses the ordered rows into the runs the list renders under one
// heading. It is a run rather than a bucket, so the heading order is the row
// order and a kind cannot appear twice.
function group(rows: Row[]): { label: string; rows: Row[] }[] {
  const groups: { label: string; rows: Row[] }[] = [];
  for (const row of rows) {
    const last = groups[groups.length - 1];
    if (last && last.label === row.group) {
      last.rows.push(row);
      continue;
    }
    groups.push({ label: row.group, rows: [row] });
  }
  return groups;
}

export function Palette() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<NameHit[]>([]);
  const [searching, setSearching] = useState(false);
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const opener = () => setOpen(true);
    openers.add(opener);
    return () => {
      openers.delete(opener);
    };
  }, []);

  // ⌘K and Ctrl+K, from anywhere including a text field. Contract K keeps
  // single-letter shortcuts out of inputs because "a" is a letter somebody is
  // typing; a chord is not, and a palette that stopped working because the
  // caret happened to be in a filter box would be a palette the operator stops
  // reaching for.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (!event.metaKey && !event.ctrlKey) return;
      if (event.key.toLocaleLowerCase() !== "k") return;
      event.preventDefault();
      setOpen((was) => !was);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // The input keeps what was typed last and selects it on open, so ⌘K twice
  // repeats the last lookup and typing replaces it.
  useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [open]);

  useEffect(() => {
    const wanted = query.trim();
    if (!open || wanted === "") {
      setHits([]);
      setSearching(false);
      setFailed(false);
      return;
    }
    setSearching(true);
    let live = true;
    const timer = window.setTimeout(async () => {
      try {
        const answer = await request<NameSearchResponse>(
          `/api/search/names?q=${encodeURIComponent(wanted)}&limit=${HIT_LIMIT}`,
        );
        if (!live) return;
        setHits(answer.hits);
        setFailed(false);
      } catch {
        if (!live) return;
        setHits([]);
        setFailed(true);
        // The shell's banner reports the failure of the page the operator is
        // looking at (see api.ts). This request was not that page's, and the
        // line below the input says so where it happened, so the banner is
        // released rather than left accusing whatever is behind the palette.
        dismissAPIError();
      } finally {
        if (live) setSearching(false);
      }
    }, DEBOUNCE_MS);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [open, query]);

  // Commands lead. There are seven of them and thousands of records, so a
  // matching command is a near-certain intent — typing "watch" is somebody on
  // his way to the control room — while a record match is a search. Measured
  // the other way round on the live corpus, "watch" put "Go to Watch" under
  // seventeen observations, which is seventeen keystrokes to reach the row the
  // operator meant. A command that matches nothing is not rendered at all, so
  // this costs a record search nothing.
  const rows = useMemo(() => [...commandRows(query.trim()), ...resultRows(hits)], [hits, query]);

  // The active row is an index into a list that changes under it, so it is
  // clamped at render rather than trusted. Every new answer starts at the top,
  // which is the likeliest row: a matching command, or the server's best
  // match.
  const at = rows.length === 0 ? 0 : Math.min(active, rows.length - 1);
  useEffect(() => setActive(0), [hits, query]);

  useEffect(() => {
    if (!open) return;
    listRef.current
      ?.querySelector<HTMLElement>('[data-active="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [open, at, rows.length]);

  const activate = useCallback(
    (row: Row) => {
      setOpen(false);
      if (row.act) {
        row.act();
        return;
      }
      // Every href this surface emits is a hash link, so the fragment marker
      // is the only part of it that is not the route — including on a link
      // like #/watch#start, whose second fragment is a section of the page it
      // opens.
      if (row.href) navigate(row.href.replace(/^#/u, ""));
    },
    [navigate],
  );

  if (!open) return null;

  const groups = group(rows);
  const activeRow = rows[at];
  const nothing = query.trim() !== "" && rows.length === 0 && !searching;

  function onInputKey(event: React.KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "Escape":
        event.preventDefault();
        setOpen(false);
        return;
      case "ArrowDown":
        event.preventDefault();
        if (rows.length > 0) setActive((was) => (Math.min(was, rows.length - 1) + 1) % rows.length);
        return;
      case "ArrowUp":
        event.preventDefault();
        if (rows.length > 0) {
          setActive((was) => (Math.min(was, rows.length - 1) + rows.length - 1) % rows.length);
        }
        return;
      case "Enter":
        event.preventDefault();
        if (activeRow) activate(activeRow);
        return;
      case "Tab":
        // Focus stays in the input while the palette is open: everything it
        // offers is reachable with the arrows, and Esc is how it is left.
        event.preventDefault();
        return;
      default:
    }
  }

  return (
    <div className="palette-scrim" onMouseDown={() => setOpen(false)} role="presentation">
      <div
        className="palette-panel"
        role="dialog"
        aria-modal="true"
        aria-label="Find anything"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="palette-field">
          {/* Drawn rather than typed: U+2315 is absent from most system font
              stacks, and a tofu box in the one place the operator looks first
              is worse than no mark at all. */}
          <svg className="palette-prompt" viewBox="0 0 16 16" aria-hidden="true">
            <circle cx="6.6" cy="6.6" r="4.4" fill="none" stroke="currentColor" strokeWidth="1.6" />
            <path d="M10 10 L14 14" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          </svg>
          <input
            ref={inputRef}
            className="palette-input"
            type="text"
            value={query}
            placeholder="Find a record, a session, a subject — or type a command"
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-list"
            aria-activedescendant={activeRow ? `palette-row-${activeRow.key}` : undefined}
            aria-label="Find anything"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onInputKey}
          />
          <kbd className="kbd">esc</kbd>
        </div>

        <div className="palette-list" id="palette-list" role="listbox" ref={listRef}>
          {groups.map((section) => (
            <div
              className="palette-group"
              role="group"
              aria-label={section.label}
              key={section.label}
            >
              <div className="palette-group-label">
                <span>{section.label}</span>
                <span className="palette-group-count">{section.rows.length}</span>
              </div>
              {section.rows.map((row) => {
                const index = rows.indexOf(row);
                const selected = index === at;
                const body = (
                  <>
                    <span className="palette-title">{row.title}</span>
                    {row.meta && <span className="palette-meta">{row.meta}</span>}
                    {selected && <kbd className="kbd palette-enter">↵</kbd>}
                  </>
                );
                const shared = {
                  id: `palette-row-${row.key}`,
                  className: "palette-row",
                  role: "option",
                  "aria-selected": selected,
                  "data-active": selected,
                  onMouseEnter: () => setActive(index),
                };
                return row.href ? (
                  <a
                    {...shared}
                    key={row.key}
                    href={row.href}
                    onClick={(event) => {
                      // A modified click is the operator asking the browser
                      // for a tab, not asking the palette for a jump.
                      if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                      event.preventDefault();
                      activate(row);
                    }}
                  >
                    {body}
                  </a>
                ) : (
                  <button {...shared} key={row.key} type="button" onClick={() => activate(row)}>
                    {body}
                  </button>
                );
              })}
            </div>
          ))}
          {nothing && (
            <p className="palette-empty">
              {failed
                ? "The lookup did not answer; this session may hold no searchable store."
                : "Nothing here is called that."}
            </p>
          )}
        </div>

        <div className="palette-footer">
          <span>
            <kbd className="kbd">↑</kbd>
            <kbd className="kbd">↓</kbd> move
            <span className="palette-sep">·</span>
            <kbd className="kbd">↵</kbd> open
            <span className="palette-sep">·</span>
            <kbd className="kbd">⌘K</kbd> close
          </span>
          <span className="palette-count">
            {searching
              ? "searching…"
              : query.trim() === ""
                ? `${rows.length} commands`
                : `${hits.length} found`}
          </span>
        </div>
      </div>
    </div>
  );
}

export default Palette;
