import { useCallback, useMemo, useRef, useState, useEffect } from "react";
import { useNavigate } from "react-router-dom";
import {
  fetchSession,
  getArchiveSessions,
  getArchiveStatus,
  getScan,
  getSessions,
  getState,
  refreshSessions,
  type ArchiveHost,
  type ArchiveSessionRow,
  type ArchiveSessionsResponse,
  type FetchResult,
  type ScanState,
  type SessionSummary,
  type SessionsResponse,
} from "../api";
import { errorMessage, formatBytes, formatDuration, formatTime } from "../format";

type SortColumn = "harness" | "title" | "workspace" | "size" | "modified" | "continuation";
type ArchiveSortColumn = "harness" | "selector" | "size" | "fetched";
type SortDirection = "asc" | "desc";

// The server returns cached rows immediately and describes stale sessions on a
// background scan, so the page polls the cheap scan endpoint frequently and
// re-reads the row set less often to show partial results as they land.
const SCAN_POLL_MS = 750;
const ROW_REFRESH_MS = 3_000;
const ELAPSED_TICK_MS = 1_000;

// Scope is what this page is currently showing. `null` is this machine's own
// sessions; a string is another host's archived ones. The two are separate
// states rather than a filter over one list because they come from different
// sources and describe different things — see ScopeNotice.
type Scope = string | null;

// Archive is what the page knows about the repository, and every branch is a
// different sentence. Collapsing "not configured" into "nothing there" is the
// specific failure this type exists to prevent: an operator who cannot tell
// them apart cannot tell whether he is missing sessions.
type Archive =
  | { kind: "loading" }
  | { kind: "unknown"; reason: string }
  | { kind: "unconfigured" }
  | { kind: "unreachable"; repository: string; reason: string }
  | { kind: "reachable"; repository: string; snapshots: number; hosts: ArchiveHost[] };

// A snapshot's file listing records a path and a size. Everything else about a
// session — its title, the workspace it ran in, when it was last written, and
// whether its closure is continuation-grade — is inside the transcript, which
// browsing an archive deliberately does not download. This is what an archive
// row shows in those columns: an explicit statement that nothing looked, never
// a dash that reads like a session with no title.
const NOT_IN_LISTING =
  "Reading it would mean downloading the transcript. Browsing another host's " +
  "archive reads only the snapshot's file listing, so nothing here observed it.";

function NotInListing() {
  return <span className="not-observed" title={NOT_IN_LISTING}>not in listing</span>;
}

// Three different kinds of claim look identical on this page: a title the
// harness wrote into its own log, one babel computed offline from the session's
// records, and one a model was paid to write. Most of this machine's corpus is
// the middle kind — codex records no title at all, so 349 of its 640 rollouts
// are titled by derivation — and a reader who cannot tell them apart is being
// shown babel's arithmetic as if it were the session's own name.
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
      "A model wrote this title, and session material left this machine for it. " +
      "It happened because you ran `babel sessions title infer --confirm`.",
  },
};

function TitleOrigin({ provenance, hasTitle }: { provenance: string | null; hasTitle: boolean }) {
  if (!hasTitle) return null;
  if (provenance === "recorded") return null;
  const known = provenance ? TITLE_ORIGIN[provenance] : undefined;
  if (!known) {
    // A title whose origin nothing recorded. It should not occur locally — the
    // catalog is rebuilt with the provenance column — and saying so is better
    // than silently letting it pass for a harness's own record.
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

  const [hostID, setHostID] = useState("");
  const [archive, setArchive] = useState<Archive>({ kind: "loading" });
  const [scope, setScope] = useState<Scope>(null);
  const [archiveRows, setArchiveRows] = useState<ArchiveSessionsResponse | null>(null);
  const [archiveLoading, setArchiveLoading] = useState(false);
  const [archiveError, setArchiveError] = useState<string | null>(null);
  const [archiveSort, setArchiveSort] = useState<ArchiveSortColumn>("selector");
  const [archiveDirection, setArchiveDirection] = useState<SortDirection>("asc");
  const [fetching, setFetching] = useState<string | null>(null);
  const [fetchOutcome, setFetchOutcome] = useState<{ selector: string; result: FetchResult } | null>(null);
  const [fetchError, setFetchError] = useState<string | null>(null);

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

  // The scope question is answered by two documents, and the order matters:
  // /api/state says whether a repository is configured at all, and only then is
  // /api/archive/status worth asking. A status failure against a configured
  // repository is a distinct answer from no repository, so it is caught here
  // instead of being allowed to look like emptiness.
  const loadArchive = useCallback(() => {
    setArchive({ kind: "loading" });
    return getState()
      .then(async (state) => {
        setHostID(state.host_id);
        if (!state.configured) {
          setArchive({ kind: "unconfigured" });
          return;
        }
        try {
          const status = await getArchiveStatus();
          setArchive({
            kind: "reachable",
            repository: state.repository,
            snapshots: status.snapshots,
            hosts: status.hosts,
          });
        } catch (reason) {
          setArchive({ kind: "unreachable", repository: state.repository, reason: errorMessage(reason) });
        }
      })
      .catch((reason) => setArchive({ kind: "unknown", reason: errorMessage(reason) }));
  }, []);

  const loadArchiveSessions = useCallback((host: string) => {
    setArchiveLoading(true);
    setArchiveError(null);
    return getArchiveSessions(host)
      .then((value) => {
        setArchiveRows(value);
        return value;
      })
      .catch((reason) => {
        setArchiveRows(null);
        setArchiveError(errorMessage(reason));
        return null;
      })
      .finally(() => setArchiveLoading(false));
  }, []);

  useEffect(() => {
    void loadSessions("blocking");
    void loadArchive();
  }, [loadSessions, loadArchive]);

  useEffect(() => {
    if (scope === null) return;
    setFetchOutcome(null);
    setFetchError(null);
    void loadArchiveSessions(scope);
  }, [scope, loadArchiveSessions]);

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

  const archiveHarnesses = useMemo(
    () => Array.from(new Set(archiveRows?.sessions.map((row) => row.harness) ?? [])).sort(),
    [archiveRows],
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

  // Archive rows sort by the four things they know and nothing else. There is
  // no "sort by title" here because there are no titles to sort.
  const archived = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    const filtered = (archiveRows?.sessions ?? []).filter((row) => {
      if (harness && row.harness !== harness) return false;
      if (!needle) return true;
      return row.selector.toLocaleLowerCase().includes(needle);
    });
    const direction = archiveDirection === "asc" ? 1 : -1;
    return filtered.slice().sort((left, right) => {
      let comparison = 0;
      if (archiveSort === "size") comparison = left.size - right.size;
      else if (archiveSort === "fetched") comparison = Number(left.fetched) - Number(right.fetched);
      else if (archiveSort === "harness") comparison = left.harness.localeCompare(right.harness);
      else comparison = left.selector.localeCompare(right.selector);
      if (comparison === 0) comparison = left.selector.localeCompare(right.selector);
      return comparison * direction;
    });
  }, [archiveRows, harness, search, archiveSort, archiveDirection]);

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

  function changeArchiveSort(column: ArchiveSortColumn) {
    if (archiveSort === column) setArchiveDirection((current) => current === "asc" ? "desc" : "asc");
    else {
      setArchiveSort(column);
      setArchiveDirection("asc");
    }
  }

  function openSession(session: SessionSummary) {
    navigate(`/sessions/${encodeURIComponent(session.selector)}`);
  }

  // Fetching leaves this machine holding a session it did not have, so nothing
  // here reports that from the fetch alone. The archive listing is re-read so
  // the "On this machine" column is the server's answer, and a catalog scan is
  // started so the local view is not left describing the machine as it was
  // before the recovery.
  async function fetchArchived(host: string, row: ArchiveSessionRow) {
    setFetching(row.selector);
    setFetchError(null);
    setFetchOutcome(null);
    try {
      const result = await fetchSession(row.selector, undefined, host);
      setFetchOutcome({ selector: row.selector, result });
      await loadArchiveSessions(host);
      await startScan();
    } catch (reason) {
      setFetchError(errorMessage(reason));
    } finally {
      setFetching(null);
    }
  }

  const rowCount = data?.sessions.length ?? 0;
  const scanError = scan?.error && !scanErrorDismissed ? scan.error : null;
  const showEmptyState = data !== null && rowCount === 0 && !running && !scanError;
  const hostLabel = hostID || "this machine";
  const otherHosts = archive.kind === "reachable"
    ? archive.hosts.filter((host) => host.host !== hostID)
    : [];
  const activeHost = scope === null ? null : otherHosts.find((host) => host.host === scope) ?? null;
  const filterHarnesses = scope === null ? harnesses : archiveHarnesses;

  return (
    <section className="page sessions-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">This host · <span className="mono">{hostLabel}</span></p>
          <h1>Sessions</h1>
          <p className="subtitle">
            Every session Babel found on this machine, across every harness. This list is not
            scoped to the folder <code>babel web</code> was launched from — the workspace column
            is a property of each session, not a filter on the list.
          </p>
        </div>
        <div className="heading-meta">
          <span className="count-label">{rowCount} cached {rowCount === 1 ? "session" : "sessions"} on {hostLabel}</span>
          {data && <span className="refresh-time">Refreshed {formatTime(data.refreshed_at)?.relative ?? data.refreshed_at}</span>}
        </div>
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      <ScopeNotice
        archive={archive}
        hostLabel={hostID}
        localCount={rowCount}
        otherHosts={otherHosts}
        onRetry={loadArchive}
      />

      {otherHosts.length > 0 && (
        <div className="scope-switch" role="group" aria-label="Which machine's sessions to show">
          <button
            type="button"
            className={scope === null ? "chip active" : "chip"}
            onClick={() => setScope(null)}
            aria-pressed={scope === null}
          >
            This host — {hostLabel} ({rowCount})
          </button>
          {otherHosts.map((host) => (
            <button
              type="button"
              key={host.host}
              className={scope === host.host ? "chip active" : "chip"}
              onClick={() => setScope(host.host)}
              aria-pressed={scope === host.host}
            >
              {host.host} — archive ({host.snapshots} {host.snapshots === 1 ? "snapshot" : "snapshots"})
            </button>
          ))}
        </div>
      )}

      {scope === null && running && scan && <ScanProgress scan={scan} rowCount={rowCount} clock={clock} />}

      {scope === null && scanError && (
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
            placeholder={scope === null ? "Filter title, workspace, or selector…" : "Filter selector…"}
            autoComplete="off"
          />
        </label>
        <div className="filter-chips" aria-label="Filter by harness">
          <button type="button" className={!harness ? "chip active" : "chip"} onClick={() => setHarness(null)}>All</button>
          {filterHarnesses.map((name) => (
            <button type="button" className={harness === name ? "chip active" : "chip"} onClick={() => setHarness(name)} key={name}>
              {name}
            </button>
          ))}
        </div>
        {scope === null ? (
          <button type="button" onClick={startScan} disabled={running || starting}>
            {running ? "Scanning…" : "Refresh"}
          </button>
        ) : (
          <button type="button" onClick={() => void loadArchiveSessions(scope)} disabled={archiveLoading}>
            {archiveLoading ? "Reading snapshot…" : "Re-read snapshot"}
          </button>
        )}
      </div>

      {scope === null && (
        <>
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
        </>
      )}

      {scope !== null && (
        <>
          <article className="card archive-scope-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Not on this machine</p>
                <h2>Archived by {scope}</h2>
              </div>
              {archiveRows && (
                <span className="count-label">
                  {archiveRows.sessions.length} archived {archiveRows.sessions.length === 1 ? "session" : "sessions"}
                </span>
              )}
            </div>
            <p className="scan-note">
              Read from {scope}
              {archiveRows?.snapshot ? <> snapshot <span className="mono">{archiveRows.snapshot}</span></> : "'s newest snapshot"}
              {activeHost?.latest_short_id && !archiveRows?.snapshot
                ? <> (<span className="mono">{activeHost.latest_short_id}</span>, {formatTime(activeHost.latest_time)?.relative ?? activeHost.latest_time})</>
                : null}
              . Only the snapshot's file listing was read — no transcript bytes were downloaded — so
              these rows carry the selector and the primary log's recorded size and nothing that
              would require opening a transcript. Fetch a session to bring its files here.
            </p>
            {fetchError && <p className="inline-error" role="alert">Fetch failed: {fetchError}</p>}
            {fetchOutcome && <FetchOutcome selector={fetchOutcome.selector} result={fetchOutcome.result} />}
          </article>

          {archiveLoading && !archiveRows && (
            <div className="state-card"><span className="spinner" /> Reading {scope}'s snapshot listing…</div>
          )}
          {archiveError && (
            <div className="state-card error-state">
              <strong>{scope}'s archived sessions could not be listed.</strong>
              <span>{archiveError}</span>
              <span>This says the repository could not be read, not that {scope} archived nothing.</span>
              <button type="button" onClick={() => void loadArchiveSessions(scope)}>Try again</button>
            </div>
          )}
          {archiveRows && archiveRows.sessions.length === 0 && (
            <div className="state-card empty-state">
              <strong>No sessions in that snapshot</strong>
              <span>{scope} has published snapshots, but the one read here holds no session Babel's adapters recognize.</span>
            </div>
          )}
          {archiveRows && archiveRows.sessions.length > 0 && archived.length === 0 && (
            <div className="state-card empty-state">
              <strong>No matching sessions</strong>
              <span>Clear the search or choose another harness.</span>
            </div>
          )}
          {archived.length > 0 && (
            <div className="table-card">
              <div className="table-scroll">
                <table className="sessions-table archive-table">
                  <thead>
                    <tr>
                      <th><button type="button" onClick={() => changeArchiveSort("harness")}>Harness <SortMark column="harness" active={archiveSort} direction={archiveDirection} /></button></th>
                      <th><button type="button" onClick={() => changeArchiveSort("selector")}>Session <SortMark column="selector" active={archiveSort} direction={archiveDirection} /></button></th>
                      <th title={NOT_IN_LISTING}>Recorded workspace</th>
                      <th className="numeric"><button type="button" onClick={() => changeArchiveSort("size")}>Size <SortMark column="size" active={archiveSort} direction={archiveDirection} /></button></th>
                      <th title={NOT_IN_LISTING}>Modified</th>
                      <th title={NOT_IN_LISTING}>Grade</th>
                      <th><button type="button" onClick={() => changeArchiveSort("fetched")}>On this machine <SortMark column="fetched" active={archiveSort} direction={archiveDirection} /></button></th>
                    </tr>
                  </thead>
                  <tbody>
                    {archived.map((row) => (
                      <tr key={row.selector}>
                        <td><span className="harness-badge">{row.harness}</span></td>
                        <td>
                          <span className="mono">{row.selector}</span>
                          <span className="secondary"><NotInListing /> — no title without reading the transcript</span>
                        </td>
                        <td><NotInListing /></td>
                        <td className="numeric mono">{formatBytes(row.size)}</td>
                        <td><NotInListing /></td>
                        <td><NotInListing /></td>
                        <td>
                          {row.fetched ? (
                            <span className="fetched-mark" title={row.fetched_path}>Fetched here</span>
                          ) : (
                            <button
                              type="button"
                              className="chip"
                              onClick={() => void fetchArchived(scope, row)}
                              disabled={fetching !== null}
                            >
                              {fetching === row.selector && <span className="spinner small" />}
                              {fetching === row.selector ? "Fetching…" : "Fetch"}
                            </button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}

interface ScopeNoticeProps {
  archive: Archive;
  hostLabel: string;
  localCount: number;
  otherHosts: ArchiveHost[];
  onRetry: () => void;
}

// ScopeNotice is the sentence the page was missing. It says what the list is,
// whose it is, and — separately for each way the archive can stand — whether
// anything else exists. The four configured/reachable/publisher branches read
// differently on purpose: an operator told "nothing more to see" when the
// repository merely failed to answer has been misinformed, and one told
// nothing at all draws his own conclusion, which is how a folder-scoped
// reading of this page happens.
function ScopeNotice({ archive, hostLabel, localCount, otherHosts, onRetry }: ScopeNoticeProps) {
  const local = `${localCount} ${localCount === 1 ? "session" : "sessions"}`;
  // The host is named when Babel knows its identity, and the sentence falls
  // back to "this machine" when it does not. It never claims a name it did not
  // read: an unnamed host is still unambiguously this one.
  const here = hostLabel
    ? <>These {local} are every session Babel found on <span className="mono">{hostLabel}</span> — this machine.</>
    : <>These {local} are every session Babel found on this machine.</>;

  if (archive.kind === "loading") {
    return (
      <div className="state-card scope-notice">
        <span className="spinner" /> Checking whether an archive holds more than this host.
      </div>
    );
  }

  if (archive.kind === "unknown") {
    return (
      <div className="state-card scope-notice error-state" role="alert">
        <strong>Babel could not read its own storage configuration</strong>
        <span>{here} Whether a repository is configured could not be determined, so this page makes no claim either way.</span>
        <span className="secondary">{archive.reason}</span>
        <button type="button" onClick={onRetry}>Try again</button>
      </div>
    );
  }

  if (archive.kind === "unconfigured") {
    return (
      <div className="state-card scope-notice">
        <strong>No archive is configured</strong>
        <span>
          {here} No restic repository is configured, so there is nothing else for Babel to show
          here and nothing it can tell you about an archive.
        </span>
        <span className="secondary">Run <code>babel storage configure</code> to connect one.</span>
      </div>
    );
  }

  if (archive.kind === "unreachable") {
    return (
      <div className="state-card scope-notice error-state" role="alert">
        <strong>The archive could not be read</strong>
        <span>
          {here} The configured repository <span className="mono">{archive.repository}</span> did
          not answer, so what it holds is unknown right now. That is an unanswered question, not an
          empty archive.
        </span>
        <span className="secondary">{archive.reason}</span>
        <button type="button" onClick={onRetry}>Try again</button>
      </div>
    );
  }

  if (otherHosts.length === 0) {
    return (
      <div className="state-card scope-notice">
        <strong>This host is the archive's only publisher</strong>
        <span>
          {here} <span className="mono">{hostLabel}</span> is the only machine that has published
          to <span className="mono">{archive.repository}</span> ({archive.snapshots}{" "}
          {archive.snapshots === 1 ? "snapshot" : "snapshots"}), so the archive holds nothing from
          anywhere else. You are not missing any sessions.
        </span>
      </div>
    );
  }

  const others = otherHosts.reduce((total, host) => total + host.snapshots, 0);
  return (
    <div className="state-card scope-notice more-elsewhere">
      <strong>
        The archive also holds sessions from {otherHosts.length} other{" "}
        {otherHosts.length === 1 ? "host" : "hosts"}
      </strong>
      <span>
        {here} The archive at <span className="mono">{archive.repository}</span> also holds {others}{" "}
        {others === 1 ? "snapshot" : "snapshots"} published by{" "}
        {otherHosts.map((host, index) => (
          <span key={host.host}>
            {index > 0 ? ", " : ""}
            <span className="mono">{host.host}</span>
          </span>
        ))}
        , whose sessions this machine has never had. Switch to that host below to browse them and
        fetch the ones you want here.
      </span>
    </div>
  );
}

function FetchOutcome({ selector, result }: { selector: string; result: FetchResult }) {
  return (
    <div className="result-panel success-panel" role="status">
      <strong>{result.already_present ? "Already on this machine" : "Fetched to this machine"}</strong>
      <span className="mono">{selector}</span>
      <dl>
        <div><dt>Snapshot</dt><dd className="mono">{result.snapshot_short_id || result.snapshot_id}</dd></div>
        <div><dt>Target</dt><dd className="mono">{result.target}</dd></div>
        <div><dt>Recovered</dt><dd>{result.files} files · {formatBytes(result.bytes)}</dd></div>
        {result.missing?.length ? (
          <div><dt>Missing from the snapshot</dt><dd>{result.missing.length} paths</dd></div>
        ) : null}
      </dl>
      <span className="secondary">
        Fetched sessions are a separate, rebuildable store that Babel never modifies and only
        <code> babel sessions prune --local</code> removes. They are not harness source files, so
        they stay out of this host's session list and are shown here instead.
      </span>
    </div>
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

interface SortMarkProps<T extends string> {
  column: T;
  active: T;
  direction: SortDirection;
}

function SortMark<T extends string>({ column, active, direction }: SortMarkProps<T>) {
  return <span className={active === column ? "sort-mark active" : "sort-mark"} aria-hidden="true">{active === column ? (direction === "asc" ? "↑" : "↓") : "↕"}</span>;
}

export default SessionsPage;
