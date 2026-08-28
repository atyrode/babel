import { useCallback, useMemo, useState, useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { getSessions, type SessionSummary, type SessionsResponse } from "../api";
import { errorMessage, formatBytes, formatTime } from "../format";

type SortColumn = "harness" | "title" | "workspace" | "size" | "modified" | "continuation";
type SortDirection = "asc" | "desc";

function SessionsPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<SessionsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [harness, setHarness] = useState<string | null>(null);
  const [sortColumn, setSortColumn] = useState<SortColumn>("modified");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");

  const loadSessions = useCallback(() => {
    setLoading(true);
    setError(null);
    getSessions()
      .then(setData)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(loadSessions, [loadSessions]);

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

  return (
    <section className="page sessions-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Local catalog</p>
          <h1>Sessions</h1>
          <p className="subtitle">Browse sessions discovered across local harnesses.</p>
        </div>
        {data && <span className="refresh-time">Refreshed {formatTime(data.refreshed_at)?.relative ?? data.refreshed_at}</span>}
      </div>

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
      </div>

      {loading && !data && <div className="state-card"><span className="spinner" /> Loading sessions…</div>}
      {error && !data && (
        <div className="state-card error-state">
          <strong>Sessions could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={loadSessions}>Try again</button>
        </div>
      )}
      {data && data.sessions.length === 0 && (
        <div className="state-card empty-state">
          <strong>No sessions found</strong>
          <span>Babel has not discovered any local harness sessions yet.</span>
        </div>
      )}
      {data && data.sessions.length > 0 && sessions.length === 0 && (
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

interface SortMarkProps {
  column: SortColumn;
  active: SortColumn;
  direction: SortDirection;
}

function SortMark({ column, active, direction }: SortMarkProps) {
  return <span className={active === column ? "sort-mark active" : "sort-mark"} aria-hidden="true">{active === column ? (direction === "asc" ? "↑" : "↓") : "↕"}</span>;
}

export default SessionsPage;
