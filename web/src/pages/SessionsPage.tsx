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
      return [session.title, session.workspace, session.selector]
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
          <p className="eyebrow">Local catalog</p>
          <h1>Sessions</h1>
          <p className="subtitle">Browse sessions discovered across local harnesses.</p>
        </div>
        <div className="heading-meta">
          <span className="count-label">{rowCount} cached {rowCount === 1 ? "session" : "sessions"}</span>
          {data && <span className="refresh-time">Refreshed {formatTime(data.refreshed_at)?.relative ?? data.refreshed_at}</span>}
        </div>
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {running && scan && <ScanProgress scan={scan} rowCount={rowCount} clock={clock} />}

      {scanError && (
        <div className="state-card error-state" role="alert">
          <strong>The session scan failed.</strong>
          <span>{scanError}</span>
          <div className="scan-error-actions">
            <button type="button" onClick={startScan} disabled={starting}>Scan again</button>
            <button type="button" className="chip" onClick={() => setScanErrorDismissed(true)}>Dismiss</button>
          </div>
        </div>
      )}

      <div className="toolbar card">
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

      {loading && !data && <div className="state-card"><span className="spinner" /> Reading the cached catalog…</div>}
      {error && !data && (
        <div className="state-card error-state">
          <strong>Sessions could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => loadSessions("blocking")}>Try again</button>
        </div>
      )}
      {showEmptyState && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>No sessions cached</strong>
          <span>Babel has not described any local harness sessions yet. Start a scan to read the session files on this host.</span>
          <button type="button" onClick={startScan} disabled={starting}>Scan now</button>
        </div>
      )}
      {rowCount > 0 && sessions.length === 0 && (
        <div className="state-card empty-state">
          <strong>No matching sessions</strong>
          <span>Clear the search or choose another harness.</span>
        </div>
      )}
      {sessions.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="sessions-table">
              <thead>
                <tr>
                  <th><button type="button" onClick={() => changeSort("harness")} aria-label={`${sortLabel("harness")} by harness`}>Harness <SortMark column="harness" active={sortColumn} direction={sortDirection} /></button></th>
                  <th><button type="button" onClick={() => changeSort("title")} aria-label={`${sortLabel("title")} by title`}>Session <SortMark column="title" active={sortColumn} direction={sortDirection} /></button></th>
                  <th><button type="button" onClick={() => changeSort("workspace")} aria-label={`${sortLabel("workspace")} by workspace`}>Workspace <SortMark column="workspace" active={sortColumn} direction={sortDirection} /></button></th>
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
                        <strong>{session.title || "Untitled session"}</strong>
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
    <article className="card scan-card">
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
        The first scan reads every session file on this host once, so it can take a couple of
        minutes on a large corpus. Described sessions are cached, so every later load stays fast.
        Rows appear in the table below as they are described — you can start browsing right away.
      </p>
    </article>
  );
}

interface SortMarkProps {
  column: SortColumn;
  active: SortColumn;
  direction: SortDirection;
}

function SortMark({ column, active, direction }: SortMarkProps) {
  return <span className={active === column ? "sort-mark active" : "sort-mark"} aria-hidden="true">{active === column ? (direction === "asc" ? "↑" : "↓") : "↕"}</span>;
}

export default SessionsPage;
