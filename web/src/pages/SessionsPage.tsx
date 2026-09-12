import { useCallback, useMemo, useRef, useState, useEffect } from "react";
import { useNavigate } from "react-router-dom";
import {
  getScan,
  getSessions,
  refreshSessions,
  type ScanState,
  type SessionSummary,
  type SessionsResponse,
} from "../api";
import { errorMessage, formatBytes, formatDuration, formatTime } from "../format";

type SortColumn = "harness" | "title" | "workspace" | "size" | "modified" | "continuation";
type SortDirection = "asc" | "desc";

// The server returns cached rows immediately and describes stale sessions on a
// background scan, so the page polls the cheap scan endpoint frequently and
// re-reads the row set less often to show partial results as they land.
const SCAN_POLL_MS = 750;
const ROW_REFRESH_MS = 3_000;
const ELAPSED_TICK_MS = 1_000;

// The corpus is one body of work and this page names no machine for it. Which
// computer's disk a transcript happens to sit on answers no question a reader
// has of a session, so there is no host column, no host chip, no scope
// selector and no sort by where a file landed: a session is read by its time
// and its own attributes. A snapshot is the one place a machine is a
// legitimate subject, because a snapshot is a backup *of* one, and that is the
// Archive page's.

// Three different kinds of claim look identical on this page: a title the
// harness wrote into its own log, one babel computed offline from the session's
// records, and one a model was paid to write. Most of the corpus is the middle
// kind — codex records no title at all, so 349 of its 640 rollouts are titled
// by derivation — and a reader who cannot tell them apart is being shown
// babel's arithmetic as if it were the session's own name.
//
// This is a mark on the title rather than a seventh column. The table already
// carries six, the value is one short word, and it is a property of the title
// and not of the session — a column would put it as far from the thing it
// qualifies as the layout allows, and cost width on every row to do it.
//
// A recorded title carries no mark on purpose. It is what a reader already
// assumes a title is, so marking every row would make the mark decoration and
// decoration is ignored; the mark exists to flag the departure from that
// assumption. The absence is unambiguous because a session with no provenance
// has no title either, and renders as "Untitled session" instead.
const TITLE_ORIGIN: Record<string, { label: string; tone: string; hint: string }> = {
  derived: {
    label: "derived",
    tone: "tone-cyan",
    hint:
      "This harness records no title. Babel derived one offline from the session's " +
      "own records — no model, no network, free and reproducible from the same bytes.",
  },
  inferred: {
    label: "inferred",
    tone: "tone-violet",
    hint:
      "A model wrote this title, and session material was sent to a provider for it. " +
      "It happened because you ran `babel sessions title infer --confirm`.",
  },
};

function TitleOrigin({ provenance, hasTitle }: { provenance: string | null; hasTitle: boolean }) {
  if (!hasTitle) return null;
  if (provenance === "recorded") return null;
  const known = provenance ? TITLE_ORIGIN[provenance] : undefined;
  if (!known) {
    // A title whose origin nothing recorded. It should not occur in a catalog
    // rebuilt with the provenance column, and saying so is better than
    // silently letting it pass for a harness's own record.
    return (
      <span className="badge title-origin" title="Nothing recorded where this title came from.">
        origin unknown
      </span>
    );
  }
  return (
    <span className={`badge title-origin ${known.tone}`} title={known.hint}>
      {known.label}
    </span>
  );
}

function SessionsPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<SessionsResponse | null>(null);
  const [scan, setScan] = useState<ScanState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [scanErrorDismissed, setScanErrorDismissed] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [starting, setStarting] = useState(false);
  const [clock, setClock] = useState(() => Date.now());
  const [search, setSearch] = useState("");
  const [harness, setHarness] = useState<string | null>(null);
  const [sortColumn, setSortColumn] = useState<SortColumn>("modified");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");
  const scanWasRunning = useRef(false);

  const loadSessions = useCallback((mode: "blocking" | "quiet") => {
    if (mode === "blocking") setLoading(true);
    return getSessions()
      .then((value) => {
        setData(value);
        setScan(value.scan);
        setError(null);
        return value;
      })
      .catch((reason) => {
        setError(errorMessage(reason));
        return null;
      })
      .finally(() => {
        if (mode === "blocking") setLoading(false);
      });
  }, []);

  useEffect(() => {
    void loadSessions("blocking");
  }, [loadSessions]);

  const running = scan?.running ?? false;

  useEffect(() => {
    if (!running) return;
    let live = true;
    const progressTimer = window.setInterval(() => {
      getScan()
        .then((value) => {
          if (live) setScan(value);
        })
        .catch(() => undefined);
    }, SCAN_POLL_MS);
    const rowTimer = window.setInterval(() => {
      if (live) void loadSessions("quiet");
    }, ROW_REFRESH_MS);
    const clockTimer = window.setInterval(() => {
      if (live) setClock(Date.now());
    }, ELAPSED_TICK_MS);
    return () => {
      live = false;
      window.clearInterval(progressTimer);
      window.clearInterval(rowTimer);
      window.clearInterval(clockTimer);
    };
  }, [running, loadSessions]);

  useEffect(() => {
    if (!scan) return;
    if (scan.running) {
      scanWasRunning.current = true;
      return;
    }
    if (!scanWasRunning.current) return;
    scanWasRunning.current = false;
    const { described, failed } = scan;
    void loadSessions("quiet").then((value) => {
      const rows = value?.sessions.length ?? 0;
      const failures = failed > 0 ? ` ${failed} could not be described.` : "";
      setAnnouncement(`Scan complete. Described ${described} sessions.${failures} ${rows} sessions in the catalog.`);
    });
  }, [scan, loadSessions]);

  const startScan = useCallback(async () => {
    setStarting(true);
    setScanErrorDismissed(false);
    setAnnouncement("");
    try {
      setScan(await refreshSessions());
    } catch (reason) {
      setError(errorMessage(reason));
    } finally {
      setStarting(false);
    }
  }, []);

  const harnesses = useMemo(
    () => Array.from(new Set(data?.sessions.map((session) => session.harness) ?? [])).sort(),
    [data],
  );

  const sessions = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    const filtered = (data?.sessions ?? []).filter((session) => {
      if (harness && session.harness !== harness) return false;
      if (!needle) return true;
      // Provenance is in the haystack so "derived" narrows the list to the
      // sessions babel named itself, which is the question the mark on each
      // title makes an operator want to ask of the whole corpus.
      return [session.title, session.title_provenance, session.workspace, session.selector]
        .some((value) => value?.toLocaleLowerCase().includes(needle));
    });
    const direction = sortDirection === "asc" ? 1 : -1;
    return filtered.sort((left, right) => {
      let comparison = 0;
      if (sortColumn === "size") comparison = left.size - right.size;
      else if (sortColumn === "modified") {
        comparison = new Date(left.modified ?? 0).getTime() - new Date(right.modified ?? 0).getTime();
      } else if (sortColumn === "continuation") {
        comparison = Number(left.continuation_grade) - Number(right.continuation_grade);
      } else {
        const leftValue = (left[sortColumn] ?? "").toLocaleLowerCase();
        const rightValue = (right[sortColumn] ?? "").toLocaleLowerCase();
        comparison = leftValue.localeCompare(rightValue);
      }
      if (comparison === 0) comparison = left.selector.localeCompare(right.selector);
      return comparison * direction;
    });
  }, [data, harness, search, sortColumn, sortDirection]);

  function changeSort(column: SortColumn) {
    if (sortColumn === column) setSortDirection((current) => current === "asc" ? "desc" : "asc");
    else {
      setSortColumn(column);
      setSortDirection("asc");
    }
  }

  function sortLabel(column: SortColumn): string {
    if (sortColumn !== column) return "Sort";
    return sortDirection === "asc" ? "Sorted ascending" : "Sorted descending";
  }

  function openSession(session: SessionSummary) {
    navigate(`/sessions/${encodeURIComponent(session.selector)}`);
  }

  const rowCount = data?.sessions.length ?? 0;
  const scanError = scan?.error && !scanErrorDismissed ? scan.error : null;
  const showEmptyState = data !== null && rowCount === 0 && !running && !scanError;

  return (
    <section className="page sessions-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Corpus</p>
          <h1>Sessions</h1>
          <p className="subtitle">
            Every session Babel found, across every harness. This list is not scoped to the
            folder <code>babel web</code> was launched from — the workspace column is a property
            of each session, not a filter on the list.
          </p>
        </div>
        <div className="heading-meta">
          <span className="count-label">{rowCount} cached {rowCount === 1 ? "session" : "sessions"}</span>
          {data && <span className="refresh-time">Refreshed {formatTime(data.refreshed_at)?.relative ?? data.refreshed_at}</span>}
        </div>
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {running && scan && <ScanProgress scan={scan} rowCount={rowCount} clock={clock} />}

      {scanError && (
        <div className="surface state-note error-state" role="alert">
          <strong>The session scan failed.</strong>
          <span>{scanError}</span>
          <div className="scan-error-actions">
            <button type="button" onClick={startScan} disabled={starting}>Scan again</button>
            <button type="button" className="chip" onClick={() => setScanErrorDismissed(true)}>Dismiss</button>
          </div>
        </div>
      )}

      <div className="toolbar surface">
        <label className="search-field">
          <span className="sr-only">Filter sessions</span>
          <input
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Filter title, workspace, or selector…"
            autoComplete="off"
          />
        </label>
        <div className="filter-chips" aria-label="Filter by harness">
          <button type="button" className={!harness ? "chip active" : "chip"} onClick={() => setHarness(null)}>All</button>
          {harnesses.map((name) => (
            <button type="button" className={harness === name ? "chip active" : "chip"} onClick={() => setHarness(name)} key={name}>
              {name}
            </button>
          ))}
        </div>
        <button type="button" onClick={startScan} disabled={running || starting}>
          {running ? "Scanning…" : "Refresh"}
        </button>
      </div>

      {loading && !data && <div className="surface state-note"><span className="spinner" /> Reading the cached catalog…</div>}
      {error && !data && (
        <div className="surface state-note error-state">
          <strong>Sessions could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => loadSessions("blocking")}>Try again</button>
        </div>
      )}
      {showEmptyState && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>No sessions cached</strong>
          <span>Babel has not described any harness sessions yet. Start a scan to read the session files it can reach.</span>
          <button type="button" onClick={startScan} disabled={starting}>Scan now</button>
        </div>
      )}
      {rowCount > 0 && sessions.length === 0 && (
        <div className="surface state-note empty-state">
          <strong>No matching sessions</strong>
          <span>Clear the search or choose another harness.</span>
        </div>
      )}
      {sessions.length > 0 && (
        <div className="surface flush">
          <div className="table-scroll">
            <table className="sessions-table">
              <thead>
                <tr>
                  <th><button type="button" onClick={() => changeSort("harness")} aria-label={`${sortLabel("harness")} by harness`}>Harness <SortMark column="harness" active={sortColumn} direction={sortDirection} /></button></th>
                  <th><button type="button" onClick={() => changeSort("title")} aria-label={`${sortLabel("title")} by title`}>Session <SortMark column="title" active={sortColumn} direction={sortDirection} /></button></th>
                  <th title="The workspace path recorded inside the session. It is not a filter on this list.">
                    <button type="button" onClick={() => changeSort("workspace")} aria-label={`${sortLabel("workspace")} by recorded workspace`}>Recorded workspace <SortMark column="workspace" active={sortColumn} direction={sortDirection} /></button>
                  </th>
                  <th className="numeric"><button type="button" onClick={() => changeSort("size")} aria-label={`${sortLabel("size")} by size`}>Size <SortMark column="size" active={sortColumn} direction={sortDirection} /></button></th>
                  <th><button type="button" onClick={() => changeSort("modified")} aria-label={`${sortLabel("modified")} by modified time`}>Modified <SortMark column="modified" active={sortColumn} direction={sortDirection} /></button></th>
                  <th className="grade-column"><button type="button" onClick={() => changeSort("continuation")} aria-label={`${sortLabel("continuation")} by continuation grade`}>Grade <SortMark column="continuation" active={sortColumn} direction={sortDirection} /></button></th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((session) => {
                  const modified = formatTime(session.modified);
                  return (
                    <tr
                      key={session.selector}
                      tabIndex={0}
                      role="link"
                      onClick={() => openSession(session)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openSession(session);
                      }}
                    >
                      <td><span className="harness-badge">{session.harness}</span></td>
                      <td>
                        <span className="session-title">
                          <strong>{session.title || "Untitled session"}</strong>
                          <TitleOrigin
                            provenance={session.title_provenance}
                            hasTitle={Boolean(session.title)}
                          />
                        </span>
                        <span className="secondary mono">{session.selector}</span>
                      </td>
                      <td>{session.workspace || <span className="muted">—</span>}</td>
                      <td className="numeric mono">{formatBytes(session.size)}</td>
                      <td>
                        {modified ? <><span>{modified.relative}</span><span className="secondary" title={modified.absolute}>{modified.absolute}</span></> : <span className="muted">—</span>}
                      </td>
                      <td className="grade-column"><span className={session.continuation_grade ? "grade-dot good" : "grade-dot partial"} title={session.continuation_grade ? "Continuation-ready" : "Partial continuation metadata"} /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </section>
  );
}

interface ScanProgressProps {
  scan: ScanState;
  rowCount: number;
  clock: number;
}

function ScanProgress({ scan, rowCount, clock }: ScanProgressProps) {
  const ceiling = Math.max(scan.total, scan.described, 1);
  const percent = Math.min(100, Math.round((scan.described / ceiling) * 100));
  const startedAt = scan.started_at ? new Date(scan.started_at).getTime() : Number.NaN;
  const elapsed = Number.isNaN(startedAt) ? "—" : formatDuration(clock - startedAt);

  return (
    <article className="surface scan-progress">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Catalog scan running</p>
          <h2>Describing sessions</h2>
        </div>
        <span className="scan-counter mono">
          {scan.described} / {scan.total} ({percent}%)
        </span>
      </div>
      <div
        className="scan-bar"
        role="progressbar"
        aria-label="Session scan progress"
        aria-valuemin={0}
        aria-valuemax={ceiling}
        aria-valuenow={scan.described}
        aria-valuetext={`${scan.described} of ${scan.total} sessions described`}
      >
        <span className="scan-bar-fill" style={{ width: `${percent}%` }} />
      </div>
      <div className="scan-facts">
        <span>Harness <strong>{scan.harness || "—"}</strong></span>
        <span>Elapsed <strong>{elapsed}</strong></span>
        <span>Rows cached <strong>{rowCount}</strong></span>
        {scan.failed > 0 && <span>Failed <strong>{scan.failed}</strong></span>}
      </div>
      <p className="scan-note">
        The first scan reads every session file once, so it can take a couple of minutes on a large
        corpus. Described sessions are cached, so every later load stays fast. Rows appear in the
        table below as they are described — you can start browsing right away.
      </p>
    </article>
  );
}

interface SortMarkProps<T extends string> {
  column: T;
  active: T;
  direction: SortDirection;
}

function SortMark<T extends string>({ column, active, direction }: SortMarkProps<T>) {
  return <span className={active === column ? "sort-mark active" : "sort-mark"} aria-hidden="true">{active === column ? (direction === "asc" ? "↑" : "↓") : "↕"}</span>;
}

export default SessionsPage;
