import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

// The chrome's own instruments, kept out of App.tsx so that file stays a
// router: what is running, how dense the interface is, and what the reader can
// press. None of them is a page, and none of them may fail loudly — a header
// that reports on the system must never be the reason the system looks broken.

// The header reads Contract W's presence endpoint and uses two of its fields.
// The rest of the payload belongs to Watch, which renders it properly; typing
// only what is consumed here keeps the shell independent of that page's client.
interface LiveRun {
  run_id: string;
  kind: string;
  spend_usd: number | null;
}

interface LiveResponse {
  runs?: LiveRun[] | null;
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
          next = LIVE_RETRY_MS;
        } else if (response.ok) {
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

  if (runs.length === 0) return null;

  // Spend is summed over the runs that reported one. A run whose receipt has
  // no cost yet contributes nothing and is not counted as zero, so the figure
  // is "what is known to have been spent", never a claim about the rest.
  let spend: number | null = null;
  for (const run of runs) {
    if (typeof run.spend_usd === "number") spend = (spend ?? 0) + run.spend_usd;
  }

  const kinds = [...new Set(runs.map((run) => run.kind).filter(Boolean))].join(", ");
  return (
    <Link
      className="live-indicator"
      to="/watch"
      title={kinds ? `In flight: ${kinds}` : "Runs in flight"}
    >
      <span className="live-dot" aria-hidden="true" />
      <span>
        {runs.length} {runs.length === 1 ? "run" : "runs"}
      </span>
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
    group: "Decide and Read",
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
