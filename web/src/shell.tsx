import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { getTopics, UNFILED, type TopicsResponse } from "./feedapi";
import { openPalette } from "./palette";

// The chrome's own instruments, kept out of App.tsx so that file stays a
// router: what is running, how dense the interface is, and what the reader can
// press. None of them is a page, and none of them may fail loudly — a header
// that reports on the system must never be the reason the system looks broken.

// The header reads Contract W's presence endpoint and uses three of its
// fields. The rest of the payload belongs to Watch, which renders it properly;
// typing only what is consumed here keeps the shell independent of that page's
// client.
interface LiveRun {
  run_id: string;
  kind: string;
  spend_usd: number | null;
  // How old the last word from this run is, in internal/presence's own
  // vocabulary: fresh, stale, lost, finished. Absent for a child this server
  // launched a moment ago that has not announced itself yet.
  freshness?: string;
}

interface LiveResponse {
  runs?: LiveRun[] | null;
}

// Whether a row is something the header may call live. `freshness` grades the
// age of the evidence, never the health of a process, so this is the one
// question the shell is allowed to ask of it: was the run heard from recently
// enough that saying "live" is a report rather than a guess.
//
// A row that has not announced at all is live: it is a run this server started
// seconds ago, and its silence is its age, not a doubt. "stale" and "lost" are
// both excluded — presence is explicit that a lost row may be working, blocked
// or gone and that this host cannot tell, and a header that counted those
// would be asserting liveness nobody observed. On the walked deployment that
// was the whole defect: 33 rows on the endpoint, 16 with a fresh heartbeat,
// and a pill that said "33 runs".
function heardFromRecently(run: LiveRun): boolean {
  return run.freshness === undefined || run.freshness === "" ||
    run.freshness === "fresh" || run.freshness === "recent";
}

const LIVE_POLL_MS = 15_000;
// A poll that overtakes the bootstrap exchange is refused, and on a first load
// that is every request the shell makes. Retrying soon after a 401 costs one
// request and removes a fifteen-second hole where a live run is invisible.
const LIVE_RETRY_MS = 2_000;

// LiveIndicator is the header's mark that something is in flight, deployment
// wide, and a link to the page that can do something about it.
//
// It fetches directly rather than through ./api on purpose. api.ts publishes
// every failure to the error banner, and a background poll for a decoration is
// the one request in this application that must never accuse a page of being
// broken: the reader did not ask for it, and its failure costs them nothing.
export function LiveIndicator() {
  const [runs, setRuns] = useState<LiveRun[]>([]);

  useEffect(() => {
    let live = true;
    let timer = 0;
    // One quick retry after a refusal covers the first-load race with the
    // bootstrap; after that a refused page is a page with no session — a
    // spent launch link, a locked server — and it must not knock every two
    // seconds for as long as it stays open.
    let refusals = 0;

    async function poll(): Promise<void> {
      let next = LIVE_POLL_MS;
      try {
        const response = await fetch("/api/watch/live", {
          cache: "no-store",
          credentials: "same-origin",
        });
        // A build without the endpoint is not a failure and will not grow one
        // while this page is open, so the loop stops asking rather than
        // knocking on a missing door four times a minute.
        if (response.status === 404) return;
        if (response.status === 401) {
          refusals += 1;
          if (refusals === 1) next = LIVE_RETRY_MS;
        } else if (response.ok) {
          refusals = 0;
          const body = (await response.json()) as LiveResponse;
          if (!live) return;
          setRuns(Array.isArray(body.runs) ? body.runs : []);
        }
      } catch {
        // Offline, aborted, or malformed: the mark simply keeps its last state
        // and tries again on the next tick.
      }
      if (live) timer = window.setTimeout(poll, next);
    }

    void poll();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, []);

  // Every figure below is about the rows that were heard from recently, and
  // the ones that were not are neither counted nor thrown away: they are the
  // second half of the tooltip, which is where "17 rows still claim to be
  // running" belongs — it is news about presence, not about what is in flight.
  const inFlight = runs.filter(heardFromRecently);
  const doubted = runs.length - inFlight.length;
  if (inFlight.length === 0) return null;

  // Spend is summed over the in-flight runs that reported one. A run whose
  // receipt has no cost yet contributes nothing and is not counted as zero, so
  // the figure is "what is known to have been spent", never a claim about the
  // rest.
  let spend: number | null = null;
  for (const run of inFlight) {
    if (typeof run.spend_usd === "number") spend = (spend ?? 0) + run.spend_usd;
  }

  const kinds = [...new Set(inFlight.map((run) => run.kind).filter(Boolean))].join(", ");
  const title = [
    kinds ? `In flight: ${kinds}` : "Runs in flight",
    doubted > 0
      ? `${doubted} more ${doubted === 1 ? "row claims" : "rows claim"} to be running ` +
        "but have not been heard from"
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <Link className="live-indicator" to="/watch" title={title}>
      <span className="live-dot" aria-hidden="true" />
      {/* "live" rather than "runs": the number is how many runs were heard
          from recently, and the word has to say which set it counts. */}
      <span>{inFlight.length} live</span>
      {spend !== null && <span className="live-spend">${spend.toFixed(2)}</span>}
    </Link>
  );
}

export type Density = "comfortable" | "compact";

const DENSITY_KEY = "babel.density";

// The density attribute lives on <html> rather than in React state alone,
// because the six space tokens it retunes are read by every stylesheet
// including the ones React does not own.
export function useDensity() {
  const [density, setDensity] = useState<Density>(() => {
    try {
      return window.localStorage.getItem(DENSITY_KEY) === "compact" ? "compact" : "comfortable";
    } catch {
      // A browser that refuses storage still gets a working interface.
      return "comfortable";
    }
  });

  useEffect(() => {
    document.documentElement.dataset.density = density;
    try {
      window.localStorage.setItem(DENSITY_KEY, density);
    } catch {
      // Same: the preference is lost on reload, the interface is not.
    }
  }, [density]);

  // The command palette can flip density too, so the two controls do not each
  // own half the truth: it dispatches `babel:density` on the document and the
  // switch that actually holds the state is here. A detail.mode is honoured if
  // one is sent; a bare event is a toggle.
  useEffect(() => {
    function onDensity(event: Event) {
      const mode = (event as CustomEvent<{ mode?: Density } | undefined>).detail?.mode;
      setDensity((current) => {
        if (mode === "compact" || mode === "comfortable") return mode;
        return current === "compact" ? "comfortable" : "compact";
      });
    }
    document.addEventListener("babel:density", onDensity);
    return () => document.removeEventListener("babel:density", onDensity);
  }, []);

  return { density, setDensity };
}

// The two words the density switch can be in, and the glyph that has always
// stood for each. The glyph alone was the whole control, and an unlabelled
// ▦ in a header is a button an operator never presses: the word is now beside
// it, and the label says what pressing it does rather than what it is.
const DENSITY_GLYPH: Record<Density, string> = { comfortable: "▦", compact: "▤" };
const DENSITY_WORD: Record<Density, string> = { comfortable: "Comfortable", compact: "Compact" };

function densityLabel(density: Density): string {
  const other = density === "compact" ? "comfortable" : "compact";
  return `${DENSITY_WORD[density]} spacing — switch to ${other}`;
}

// The width at which the header folds. It is the same number as the media
// query in styles.css that stacks the instruments under the navigation, and
// the two must stay equal: this decides *what* is in the header, the
// stylesheet decides *where*, and a header that folded its controls at one
// width and its rows at another would be neither layout.
const NARROW_HEADER = "(max-width: 640px)";

function useNarrowHeader(): boolean {
  const [narrow, setNarrow] = useState(() => window.matchMedia(NARROW_HEADER).matches);
  useEffect(() => {
    // matchMedia hands out a new list object per call, so the list is made
    // here rather than during render: one subscription for the life of the
    // component instead of one per render.
    const query = window.matchMedia(NARROW_HEADER);
    // Re-read on mount as well as on change: a resize between the first
    // render and this effect would otherwise leave the wrong set of controls
    // in the header until the next one.
    setNarrow(query.matches);
    function onChange(event: MediaQueryListEvent) {
      setNarrow(event.matches);
    }
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

// The width at which the topics stand beside the feed rather than folded
// above it. It is the same number as feed.css's rail query and as the
// stylesheet's own one-row header breakpoint: the page decides whether the
// rail is mounted at all, because a list rendered twice and hidden once is
// two lists to every reader who is not looking at pixels.
const WIDE_RAIL = "(min-width: 1024px)";

export function useWideViewport(): boolean {
  const [wide, setWide] = useState(() => window.matchMedia(WIDE_RAIL).matches);
  useEffect(() => {
    const query = window.matchMedia(WIDE_RAIL);
    setWide(query.matches);
    function onChange(event: MediaQueryListEvent) {
      setWide(event.matches);
    }
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return wide;
}

// How many topics the rail names before it stops. Twelve is what stands
// beside a feed without becoming the page's second list; the rest are one
// link away, and the link says so rather than the list trailing off.
const RAIL_TOPICS = 12;

// The topics, with their counts. §8.7's sixth destination that is not a page:
// the same list is the rail on a wide viewport and the fold above the feed on
// a narrow one, so it lives here with the shell's other instruments rather
// than inside the page that happens to mount it.
//
// A topic is a name and a count. Nothing here assumes it is a directory — it
// is the workspace a cited session came from today and could be a mailbox or
// a tracker tomorrow — so the list neither prints a path nor offers to open
// one.
export function TopicList({ current }: { current: string }) {
  const [answer, setAnswer] = useState<TopicsResponse | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    getTopics()
      .then((next) => {
        if (live) setAnswer(next);
      })
      .catch(() => {
        if (live) setFailed(true);
      });
    return () => {
      live = false;
    };
  }, []);

  // A rail that could not be read says so in one line and takes no more room
  // than that: the feed beside it is fine, and a failed decoration must not
  // read as a failed page.
  if (failed) return <p className="topic-note">The topics could not be read.</p>;
  if (!answer) return <p className="topic-note">Reading the topics…</p>;

  const topics = answer.topics ?? [];
  return (
    <>
      <ul className="topic-list">
        <li>
          <Link to="/" aria-current={current === "" ? "page" : undefined}>
            All posts
          </Link>
        </li>
        {topics.slice(0, RAIL_TOPICS).map((topic) => (
          <li key={topic.name}>
            <Link
              to={`/t/${encodeURIComponent(topic.name)}`}
              aria-current={current === topic.name ? "page" : undefined}
              title={`${topic.posts.toLocaleString()} posts filed under evidence from ${topic.name}`}
            >
              <span>t/{topic.name}</span>
              <span className="topic-count">{topic.posts.toLocaleString()}</span>
            </Link>
          </li>
        ))}
        {/* The posts whose origin this deployment could not resolve. They are
            in the feed rather than hidden (§8.7) and this is the filter that
            selects exactly them; a deployment that has none says nothing. */}
        {answer.unfiled > 0 && (
          <li>
            <Link
              to={`/?topic=${UNFILED}`}
              aria-current={current === UNFILED ? "page" : undefined}
              title="Posts whose evidence cites no session this deployment can resolve"
            >
              <span>no topic</span>
              <span className="topic-count">{answer.unfiled.toLocaleString()}</span>
            </Link>
          </li>
        )}
      </ul>
      {topics.length > RAIL_TOPICS && (
        <Link className="topic-all" to="/t">
          all {topics.length.toLocaleString()} topics →
        </Link>
      )}
    </>
  );
}

// ShellControls is the three instruments between the live mark and the stop:
// search, density, keys. Nothing about them changes with the viewport except
// how many buttons they occupy — on a phone the header had five controls and
// the navigation on three rows, which pushed the page's own title off the
// screen, so below NARROW_HEADER the three fold into one … menu and the two
// controls that must never be a click away — what is running, and how to stop
// it — stay where they are.
export function ShellControls({
  density,
  setDensity,
  onKeyHints,
}: {
  density: Density;
  setDensity: (density: Density) => void;
  onKeyHints: () => void;
}) {
  const narrow = useNarrowHeader();
  const [open, setOpen] = useState(false);
  const host = useRef<HTMLDivElement | null>(null);

  // A menu that is only rendered in the folded header must not still be open
  // when the window grows and then shrinks again.
  useEffect(() => setOpen(false), [narrow]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: PointerEvent) {
      if (!host.current?.contains(event.target as Node)) setOpen(false);
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const other: Density = density === "compact" ? "comfortable" : "compact";
  const label = densityLabel(density);

  if (!narrow) {
    return (
      <>
        {/* The search control says "Search" rather than wearing a magnifier
            glyph: U+2315 is missing from most Linux font stacks and renders
            as a tofu box, and a control the operator cannot name is a control
            they do not press. The key is on the button beside the word, which
            is also how they learn it. */}
        <button
          type="button"
          className="shell-toggle shell-search"
          onClick={openPalette}
          title="Search records, sessions, entities and questions"
        >
          Search
          <kbd className="kbd">⌘K</kbd>
        </button>
        <button
          type="button"
          className="shell-toggle shell-density"
          onClick={() => setDensity(other)}
          aria-pressed={density === "compact"}
          title={label}
          aria-label={label}
        >
          <span aria-hidden="true">{DENSITY_GLYPH[density]}</span>
          <span className="shell-density-word">{DENSITY_WORD[density]}</span>
        </button>
        <button
          type="button"
          className="shell-toggle"
          onClick={onKeyHints}
          title="Keyboard shortcuts (?)"
          aria-label="Keyboard shortcuts"
        >
          ?
        </button>
      </>
    );
  }

  return (
    <div className="shell-menu-host" ref={host}>
      <button
        type="button"
        className="shell-toggle shell-menu-button"
        onClick={() => setOpen((current) => !current)}
        aria-expanded={open}
        aria-haspopup="menu"
        title="Search, density and keyboard shortcuts"
        aria-label="More controls"
      >
        <span aria-hidden="true">…</span>
      </button>
      {open && (
        <div className="surface shell-menu" role="menu" aria-label="More controls">
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              openPalette();
            }}
          >
            <span>Search</span>
            <kbd className="kbd">⌘K</kbd>
          </button>
          {/* The item states the density it is in and the one it goes to, so
              the reader does not have to press it to find out which is which.
              That is also why it carries no pressed state: `aria-pressed` is
              not a property of a menuitem, and the two words are a better
              answer to "which am I in" than a checkmark. */}
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              setDensity(other);
            }}
          >
            {/* Two words, because the menu is 390px wide minus a thumb: the
                density it is in, and the one it goes to. */}
            <span>
              <span aria-hidden="true">{DENSITY_GLYPH[density]}</span> {DENSITY_WORD[density]}
            </span>
            <span className="shell-menu-meta">→ {other}</span>
          </button>
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              onKeyHints();
            }}
          >
            <span>Keyboard shortcuts</span>
            <kbd className="kbd">?</kbd>
          </button>
        </div>
      )}
    </div>
  );
}

// The keys Babel answers to. This list is the contract, not a description of
// it: every entry here is implemented by the shell, the listings or the record
// page, and an entry that stops being true is a bug in this file.
const KEY_HINTS: { group: string; keys: { press: string[]; does: string }[] }[] = [
  {
    group: "Anywhere",
    keys: [
      { press: ["⌘K", "Ctrl+K"], does: "Search records, sessions, entities and open questions" },
      { press: ["?"], does: "These key hints" },
      { press: ["Esc"], does: "Close whatever is open" },
    ],
  },
  {
    group: "The feed",
    keys: [
      { press: ["j", "k"], does: "Move down and up the posts" },
      { press: ["Enter"], does: "Open the focused post" },
      { press: ["a", "d"], does: "Agree or disagree; pressing the lit arrow again withdraws it" },
    ],
  },
  {
    group: "The mod queue",
    keys: [
      { press: ["j", "k"], does: "Move down and up the list" },
      { press: ["Enter"], does: "Open the focused record" },
      { press: ["a", "d", "u"], does: "Agree, disagree or unsure on the focused record" },
      { press: ["r"], does: "Open the rule bar for the focused record" },
    ],
  },
  {
    group: "A record",
    keys: [
      { press: ["a", "d", "u"], does: "Agree, disagree or unsure" },
      { press: ["r"], does: "Open the rule bar" },
      { press: ["1", "…", "5"], does: "Open or close a depth" },
    ],
  },
];

// KeyHints is a dialog over the page the reader is already on: the question
// "what can I press here" is asked in place and answered in place.
export function KeyHints({ onClose }: { onClose: () => void }) {
  return (
    <div
      className="keyhints"
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      onClick={onClose}
    >
      <div className="surface keyhints-panel" onClick={(event) => event.stopPropagation()}>
        <h2>Keys</h2>
        <p className="muted">
          Every list and every record can be worked without the mouse. Keys are ignored while
          you are typing in a field.
        </p>
        {KEY_HINTS.map((section) => (
          <section className="keyhints-group" key={section.group}>
            <h3>{section.group}</h3>
            <ul className="keyhints-list">
              {section.keys.map((hint) => (
                <li key={hint.does}>
                  <span>
                    {hint.press.map((key) => (
                      <kbd className="kbd" key={key}>
                        {key}
                      </kbd>
                    ))}
                  </span>
                  <span>{hint.does}</span>
                </li>
              ))}
            </ul>
          </section>
        ))}
        <button type="button" className="keyhints-close" onClick={onClose}>
          Close
        </button>
      </div>
    </div>
  );
}

// ShellFooter carries the §1 frame — once, for the whole application. It used
// to be a dashed box beside every analytical panel, which on a record page
// meant four copies of the same caveat on one screen (see analysis.tsx).
export function ShellFooter({ version }: { version: string }) {
  return (
    <footer className="app-footer">
      <p>
        Babel's analytical output is fallible interpretation, not established fact: it is
        creative, incomplete, and recorded for human review. Follow the evidence locators before
        believing a claim.
      </p>
      <p className="footer-keys">
        <span className="mono">{version}</span>
        <span aria-hidden="true">·</span>
        <span>
          press <kbd className="kbd">?</kbd> for keys
        </span>
      </p>
    </footer>
  );
}
