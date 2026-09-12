import {
  useCallback,
  useMemo,
  useRef,
  useState,
  useEffect,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  getScan,
  getSessions,
  refreshSessions,
  type ScanState,
  type SessionSummary,
  type SessionsResponse,
} from "../api";
import { errorMessage, formatBytes, formatDuration, formatTime } from "../format";
import "../sessions.css";

// The corpus is a body of measured work, so this page is a data table: every
// number the catalog holds about a session is a column, every column sorts,
// and the header states the totals of whatever the filter currently selects.
// What a session cost, how many tokens it spent, how many turns it took and
// how many of its tool calls failed were all recorded by the harness and
// summed at describe time; until now they reached `sessions list --json` and
// stopped there.
type SortColumn =
  | "harness"
  | "title"
  | "workspace"
  | "size"
  | "modified"
  | "continuation"
  | "cost"
  | "tokens"
  | "turns"
  | "tool_errors";
type SortDirection = "asc" | "desc";

// The server returns cached rows immediately and describes stale sessions on a
// background scan, so the page polls the cheap scan endpoint frequently and
// re-reads the row set less often to show partial results as they land.
const SCAN_POLL_MS = 750;
const ROW_REFRESH_MS = 3_000;
const ELAPSED_TICK_MS = 1_000;

// The corpus runs to hundreds of sessions and this table is read a page at a
// time, because a list that renders all of them is twenty-five screens tall
// and answers no question a reader had: the sort is what finds a session, and
// a sort is only useful if its first rows are visible without scrolling.
//
// "All" stays available. It is the honest escape hatch for a reader who wants
// the browser's own find-in-page over the whole corpus, and the cost of it is
// the reader's own choice rather than the default.
const PAGE_SIZES = [25, 50, 100, 0] as const;
const DEFAULT_PAGE_SIZE = 50;

// Babel's own analysis passes are a harness in the catalog like any other,
// and unlike any other in the reading: they are runs Babel made over the
// corpus, not conversations the operator had with a coding agent. There are
// hundreds of them, they are titled from the run that wrote them, and on the
// page an operator opens to find one of his own sessions they outnumbered
// his. So the default view is his harnesses and Babel's own are one chip
// away — hidden, said to be hidden, and counted.
const SELF_HARNESS = "babel";

// The URL value for "including Babel's own". The default is the absence of
// the parameter, because the view an operator arrives at is the one whose
// address has nothing in it.
const EVERY_HARNESS = "all";

// Three different kinds of claim look identical on this page: a title the
// harness wrote into its own log, one babel computed offline from the session's
// records, and one a model was paid to write. Most of the corpus is the middle
// kind — codex records no title at all, so 349 of its 640 rollouts are titled
// by derivation — and a reader who cannot tell them apart is being shown
// babel's arithmetic as if it were the session's own name.
//
// This is a mark on the title rather than a column of its own. The table
// already carries nine, the value is one short word, and it is a property of
// the title and not of the session — a column would put it as far from the
// thing it qualifies as the layout allows, and cost width on every row to do
// it.
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

// formatUSD states a recorded cost at the precision that cost has. A cent is
// the unit an operator reasons in above a dollar, and below a cent the figure
// is still real money summed over a corpus, so it keeps its digits rather than
// rounding to "$0.00" — which reads as free.
export function formatUSD(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (value === 0) return "$0";
  if (Math.abs(value) < 0.01) return `$${value.toFixed(4)}`;
  if (Math.abs(value) < 1_000) return `$${value.toFixed(2)}`;
  return `$${Math.round(value).toLocaleString()}`;
}

// formatCount abbreviates a token or turn count. A corpus-wide token total
// runs to ten figures, and a figure that long is not compared, it is
// deciphered; the exact number rides the cell's own tooltip or the stat's
// note for the reader who wants it.
export function formatCount(value: number): string {
  if (!Number.isFinite(value)) return "—";
  const magnitude = Math.abs(value);
  for (const [unit, scale] of [["B", 1e9], ["M", 1e6], ["k", 1e3]] as const) {
    if (magnitude < scale) continue;
    const scaled = value / scale;
    // One decimal below ten, none above: "1.4M" and "297k" are both four
    // characters wide, which is what keeps a numeric column a column.
    return `${Math.abs(scaled) < 10 ? scaled.toFixed(1) : Math.round(scaled)}${unit}`;
  }
  return String(value);
}

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

// The numeric columns and how each one is read off a row. A null here means
// nobody measured it, which is why these return null rather than zero: the
// comparator and every cell treat the two differently.
const NUMERIC: Partial<Record<SortColumn, (session: SessionSummary) => number | null>> = {
  size: (session) => session.size,
  modified: (session) => (session.modified ? new Date(session.modified).getTime() : null),
  continuation: (session) => Number(session.continuation_grade),
  cost: (session) => session.cost_usd,
  tokens: (session) => session.total_tokens,
  turns: (session) => session.turns,
  tool_errors: (session) => session.tool_errors,
};

function SessionsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [data, setData] = useState<SessionsResponse | null>(null);
  const [scan, setScan] = useState<ScanState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [scanErrorDismissed, setScanErrorDismissed] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [starting, setStarting] = useState(false);
  const [clock, setClock] = useState(() => Date.now());
  const [search, setSearch] = useState("");
  // Which harness the list is narrowed to: "" is the operator's own, "all"
  // is every one including Babel's, and anything else is that harness alone.
  // It lives in the URL because a narrowed catalog is a thing an operator
  // reloads, shares and walks back out of with the browser's own Back
  // button — and because the view he lands on has to survive a refresh.
  const chosen = params.get("harness") ?? "";
  const [sortColumn, setSortColumn] = useState<SortColumn>("modified");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);
  const [page, setPage] = useState(0);
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

  // What the search matches, before the harness narrows it. It is a step of
  // its own because the page has to say how many rows the harness choice is
  // hiding, and hidden has to mean hidden by that choice rather than by the
  // words in the search box.
  const searched = useMemo(() => {
    const needle = search.trim().toLocaleLowerCase();
    const rows = data?.sessions ?? [];
    if (!needle) return rows;
    // Provenance is in the haystack so "derived" narrows the list to the
    // sessions babel named itself, which is the question the mark on each
    // title makes an operator want to ask of the whole corpus.
    return rows.filter((session) =>
      [session.title, session.title_provenance, session.workspace, session.selector]
        .some((value) => value?.toLocaleLowerCase().includes(needle)));
  }, [data, search]);

  const sessions = useMemo(() => {
    const filtered = searched.filter((session) => {
      if (chosen === EVERY_HARNESS) return true;
      if (chosen) return session.harness === chosen;
      return session.harness !== SELF_HARNESS;
    });
    const direction = sortDirection === "asc" ? 1 : -1;
    const read = NUMERIC[sortColumn];
    return filtered.sort((left, right) => {
      let comparison = 0;
      if (read) {
        const leftValue = read(left);
        const rightValue = read(right);
        // A session nobody measured sorts last in both directions. It is not
        // the cheapest session and it is not the most expensive one: ranking
        // an absent measurement against a number would invent the number.
        if (leftValue === null || rightValue === null) {
          if (leftValue !== rightValue) return leftValue === null ? 1 : -1;
        } else {
          comparison = leftValue - rightValue;
        }
      } else {
        const leftText = (left[sortColumn as "harness" | "title" | "workspace"] ?? "").toLocaleLowerCase();
        const rightText = (right[sortColumn as "harness" | "title" | "workspace"] ?? "").toLocaleLowerCase();
        comparison = leftText.localeCompare(rightText);
      }
      if (comparison === 0) return left.selector.localeCompare(right.selector);
      return comparison * direction;
    });
  }, [searched, chosen, sortColumn, sortDirection]);

  // How many rows the harness choice is holding back. It is never inferred
  // from a total: it is the difference between what the search matched and
  // what is on the page, so it counts exactly what the chip above hid.
  const hidden = searched.length - sessions.length;

  // The totals describe the filter, not the page: an operator who narrows to
  // one harness is asking what that harness cost, and a figure that answered
  // for the fifty rows currently visible would answer a question about
  // pagination instead.
  const totals = useMemo(() => {
    let cost = 0;
    let tokens = 0;
    let priced = 0;
    let costliest = 0;
    for (const session of sessions) {
      if (session.cost_usd !== null) {
        cost += session.cost_usd;
        priced += 1;
        costliest = Math.max(costliest, session.cost_usd);
      }
      if (session.total_tokens !== null) tokens += session.total_tokens;
    }
    return { cost, tokens, priced, costliest };
  }, [sessions]);

  const pages = pageSize === 0 ? 1 : Math.max(1, Math.ceil(sessions.length / pageSize));
  const currentPage = Math.min(page, pages - 1);
  const pageStart = pageSize === 0 ? 0 : currentPage * pageSize;
  const pageRows = pageSize === 0 ? sessions : sessions.slice(pageStart, pageStart + pageSize);

  // Every control that changes which rows exist returns to the first page,
  // because page four of a list that just became one page long is an empty
  // table and looks like a failure.
  function changeSort(column: SortColumn) {
    setPage(0);
    if (sortColumn === column) setSortDirection((current) => (current === "asc" ? "desc" : "asc"));
    else {
      setSortColumn(column);
      // Text reads naturally from A, and a measurement is asked for from the
      // top: "sort by cost" means the expensive sessions, every time.
      setSortDirection(NUMERIC[column] ? "desc" : "asc");
    }
  }

  // The harness chip writes the address bar and returns to the first page,
  // for the same reason the sort does.
  function chooseHarness(next: string) {
    const query = new URLSearchParams(params);
    if (next) query.set("harness", next);
    else query.delete("harness");
    setParams(query);
    setPage(0);
  }

  function sortLabel(column: SortColumn): string {
    if (sortColumn !== column) return "Sort";
    return sortDirection === "asc" ? "Sorted ascending" : "Sorted descending";
  }

  function openSession(session: SessionSummary, event: ReactMouseEvent | ReactKeyboardEvent) {
    // The title is a real link, so a click that landed on it has already been
    // handled — following the row too would navigate twice and break
    // middle-click and modified clicks, which are the whole reason the link
    // exists.
    if (event.target instanceof Element && event.target.closest("a")) return;
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
            Every session Babel found, across every harness, with what each one cost. The
            workspace column is a property of the session, not a filter on this list.
          </p>
        </div>
        {/* The totals row is its own container rather than the shared
            heading-meta, because heading-meta stacks one short line per fact
            and these are figures with labels. */}
        <div className="sessions-totals">
          <div className="stat">
            <span className="stat-label">Sessions</span>
            <strong className="stat-value">{sessions.length.toLocaleString()}</strong>
            <span className="stat-note">
              {sessions.length === rowCount ? "all cached" : `of ${rowCount.toLocaleString()} cached`}
            </span>
          </div>
          <div className="stat">
            <span className="stat-label">Recorded spend</span>
            <strong className="stat-value">{totals.priced === 0 ? "—" : formatUSD(totals.cost)}</strong>
            {/* The denominator is the point. Most harnesses record no usage,
                so a total without the count of what it was summed over would
                read as the corpus's cost rather than as the measured part of
                it. */}
            <span className="stat-note">
              {totals.priced === 0
                ? "nothing here recorded usage"
                : `${totals.priced.toLocaleString()} of ${sessions.length.toLocaleString()} priced`}
            </span>
          </div>
          <div className="stat">
            <span className="stat-label">Tokens</span>
            <strong className="stat-value">{totals.tokens === 0 ? "—" : formatCount(totals.tokens)}</strong>
            <span className="stat-note">{totals.tokens === 0 ? "unmeasured" : totals.tokens.toLocaleString()}</span>
          </div>
          {data && (
            <span className="refresh-time">
              Refreshed {formatTime(data.refreshed_at)?.relative ?? data.refreshed_at}
            </span>
          )}
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
            onChange={(event) => {
              setSearch(event.target.value);
              setPage(0);
            }}
            placeholder="Filter title, workspace, or selector…"
            autoComplete="off"
          />
        </label>
        {/* Two views and then the harnesses themselves. "Yours" is the
            default and is not the same claim as "Everything": one is the
            operator's own conversations, the other adds the passes Babel
            ran over them. Every harness in the catalog keeps a chip of its
            own, Babel's included, so nothing here is unreachable. */}
        <div className="filter-chips" aria-label="Filter by harness">
          <button
            type="button"
            className={chosen === "" ? "chip active" : "chip"}
            aria-pressed={chosen === ""}
            title="Every harness except babel: the sessions you drove yourself."
            onClick={() => chooseHarness("")}
          >
            Yours
          </button>
          <button
            type="button"
            className={chosen === EVERY_HARNESS ? "chip active" : "chip"}
            aria-pressed={chosen === EVERY_HARNESS}
            title="Every harness in the catalog, including Babel's own analysis passes."
            onClick={() => chooseHarness(EVERY_HARNESS)}
          >
            Everything
          </button>
          {harnesses.map((name) => (
            <button
              type="button"
              className={chosen === name ? "chip active" : "chip"}
              aria-pressed={chosen === name}
              title={
                name === SELF_HARNESS
                  ? "Babel's own analysis passes over the corpus."
                  : `Sessions recorded by ${name}.`
              }
              onClick={() => chooseHarness(name)}
              key={name}
            >
              {name}
            </button>
          ))}
        </div>
        <button type="button" onClick={startScan} disabled={running || starting}>
          {running ? "Scanning…" : "Refresh"}
        </button>
      </div>

      {/* What the chip above is holding back, in one line, with the figure
          that reveals it. A list that silently omits a third of the catalog
          is a list that lies about the corpus; a list that says what it
          omitted and how much is a list with a default. */}
      {hidden > 0 && (
        <p className="sessions-hidden">
          {sessions.length.toLocaleString()} shown ·{" "}
          <button
            type="button"
            className="link-button"
            onClick={() => chooseHarness(EVERY_HARNESS)}
            title="Show every harness in the catalog."
          >
            {hidden.toLocaleString()}
            {chosen === "" ? " of Babel's own passes" : " in other harnesses"}
          </button>{" "}
          hidden
        </p>
      )}

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
                  <th className="session-cell"><button type="button" onClick={() => changeSort("title")} aria-label={`${sortLabel("title")} by title`}>Session <SortMark column="title" active={sortColumn} direction={sortDirection} /></button></th>
                  <th className="workspace-cell" title="The workspace path recorded inside the session. It is not a filter on this list.">
                    <button type="button" onClick={() => changeSort("workspace")} aria-label={`${sortLabel("workspace")} by recorded workspace`}>Workspace <SortMark column="workspace" active={sortColumn} direction={sortDirection} /></button>
                  </th>
                  <th><button type="button" onClick={() => changeSort("modified")} aria-label={`${sortLabel("modified")} by modified time`}>Modified <SortMark column="modified" active={sortColumn} direction={sortDirection} /></button></th>
                  <th
                    className="numeric"
                    title={
                      totals.costliest > 0
                        ? `What the harness recorded this session's model work cost. The bar is drawn against ${formatUSD(totals.costliest)}, the highest in this filter.`
                        : "What the harness recorded this session's model work cost."
                    }
                  >
                    <button type="button" onClick={() => changeSort("cost")} aria-label={`${sortLabel("cost")} by recorded cost`}>Cost <SortMark column="cost" active={sortColumn} direction={sortDirection} /></button>
                  </th>
                  <th className="numeric"><button type="button" onClick={() => changeSort("tokens")} aria-label={`${sortLabel("tokens")} by token count`}>Tokens <SortMark column="tokens" active={sortColumn} direction={sortDirection} /></button></th>
                  <th className="numeric" title="Assistant turns the harness recorded.">
                    <button type="button" onClick={() => changeSort("turns")} aria-label={`${sortLabel("turns")} by turns`}>Turns <SortMark column="turns" active={sortColumn} direction={sortDirection} /></button>
                  </th>
                  <th className="numeric" title="Tool results the harness marked as failures.">
                    <button type="button" onClick={() => changeSort("tool_errors")} aria-label={`${sortLabel("tool_errors")} by tool errors`}>Tool err <SortMark column="tool_errors" active={sortColumn} direction={sortDirection} /></button>
                  </th>
                  <th className="numeric"><button type="button" onClick={() => changeSort("size")} aria-label={`${sortLabel("size")} by size`}>Size <SortMark column="size" active={sortColumn} direction={sortDirection} /></button></th>
                  <th className="grade-column"><button type="button" onClick={() => changeSort("continuation")} aria-label={`${sortLabel("continuation")} by continuation grade`}>Grade <SortMark column="continuation" active={sortColumn} direction={sortDirection} /></button></th>
                </tr>
              </thead>
              <tbody>
                {pageRows.map((session) => {
                  const modified = formatTime(session.modified);
                  return (
                    <tr
                      key={session.selector}
                      tabIndex={0}
                      role="link"
                      onClick={(event) => openSession(session, event)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openSession(session, event);
                      }}
                    >
                      <td><span className="harness-badge">{session.harness}</span></td>
                      <td className="session-cell" title={session.title ?? undefined}>
                        <span className="session-title">
                          {/* A row is a link with a URL the browser can show,
                              open in a new tab and middle-click, which an
                              onclick handler is not. */}
                          <Link to={`/sessions/${encodeURIComponent(session.selector)}`}>{session.title || "Untitled session"}</Link>
                          <TitleOrigin
                            provenance={session.title_provenance}
                            hasTitle={Boolean(session.title)}
                          />
                        </span>
                        <span className="secondary mono">{session.selector}</span>
                      </td>
                      <td className="workspace-cell" title={session.workspace ?? undefined}>{session.workspace || <Absent />}</td>
                      {/* One line per row: the absolute time is a tooltip
                          rather than a second line, because a table's rows
                          are only comparable at a glance if they are the
                          same height. */}
                      <td className="time-cell">
                        {modified ? <span title={modified.absolute}>{modified.relative}</span> : <Absent />}
                      </td>
                      <td className="numeric mono cost-cell">
                        {session.cost_usd === null ? <Absent /> : (
                          <>
                            <span>{formatUSD(session.cost_usd)}</span>
                            <CostBar value={session.cost_usd} ceiling={totals.costliest} />
                          </>
                        )}
                      </td>
                      <td className="numeric mono" title={session.total_tokens?.toLocaleString()}>
                        {session.total_tokens === null ? <Absent /> : formatCount(session.total_tokens)}
                      </td>
                      <td className="numeric mono">
                        {session.turns === null ? <Absent /> : session.turns.toLocaleString()}
                      </td>
                      <td className="numeric mono">
                        {session.tool_errors === null
                          ? <Absent />
                          : session.tool_errors === 0
                            ? <span className="zero">0</span>
                            : <span className="errors">{session.tool_errors.toLocaleString()}</span>}
                      </td>
                      <td className="numeric mono">{formatBytes(session.size)}</td>
                      <td className="grade-column"><span className={session.continuation_grade ? "grade-dot good" : "grade-dot partial"} title={session.continuation_grade ? "Continuation-ready" : "Partial continuation metadata"} /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <Pager
            total={sessions.length}
            page={currentPage}
            pages={pages}
            pageSize={pageSize}
            from={pageStart + 1}
            to={pageStart + pageRows.length}
            onPage={setPage}
            onPageSize={(size) => {
              setPageSize(size);
              setPage(0);
            }}
          />
        </div>
      )}
    </section>
  );
}

// Absent renders a measurement nobody took. It is a dash with a reason, not a
// zero: the difference is the whole point of the nullable columns.
function Absent() {
  return <span className="absent" title="Not recorded. This is an absent measurement, not a zero.">—</span>;
}

// CostBar draws one row's cost against the most expensive session in the
// current filter, so a column of figures becomes a shape a reader can scan.
// Inline SVG rather than a styled div because a bar is geometry, and geometry
// scales with the cell instead of being pinned to a pixel width.
function CostBar({ value, ceiling }: { value: number; ceiling: number }) {
  if (ceiling <= 0) return null;
  // A cost far below the ceiling still gets a visible mark: a bar that rounds
  // to nothing says "no cost" when the truth is "a small one".
  const width = Math.max(1.5, Math.min(100, (value / ceiling) * 100));
  return (
    <span className="spark cost-bar" style={{ "--spark-height": "4px" } as CSSProperties} aria-hidden="true">
      <svg viewBox="0 0 100 4" preserveAspectRatio="none">
        {/* The track is what makes the bar a comparison rather than a mark:
            a $1 bar beside a $2,000 one is a sliver, and a sliver with no
            scale behind it reads as a stray rule. */}
        <rect x="0" y="0" width="100" height="4" fill="currentColor" fillOpacity="0.16" />
        <rect x="0" y="0" width={width} height="4" fill="currentColor" />
      </svg>
    </span>
  );
}

interface PagerProps {
  total: number;
  page: number;
  pages: number;
  pageSize: number;
  from: number;
  to: number;
  onPage: (page: number) => void;
  onPageSize: (size: number) => void;
}

function Pager({ total, page, pages, pageSize, from, to, onPage, onPageSize }: PagerProps) {
  return (
    <div className="sessions-pager">
      <div className="rule-bar" role="group" aria-label="Rows per page">
        {PAGE_SIZES.map((size) => (
          <button
            type="button"
            key={size}
            className={pageSize === size ? "active" : undefined}
            aria-pressed={pageSize === size}
            onClick={() => onPageSize(size)}
          >
            {size === 0 ? "All" : size}
          </button>
        ))}
      </div>
      <span className="pager-range mono">
        {total === 0 ? "no rows" : `rows ${from.toLocaleString()}–${to.toLocaleString()} of ${total.toLocaleString()}`}
      </span>
      <div className="rule-bar" role="group" aria-label="Pages">
        <button type="button" onClick={() => onPage(page - 1)} disabled={page === 0}>← Previous</button>
        <button type="button" onClick={() => onPage(page + 1)} disabled={page >= pages - 1}>Next →</button>
      </div>
      <span className="pager-page mono">page {page + 1} of {pages}</span>
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
