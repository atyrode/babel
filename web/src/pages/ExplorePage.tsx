import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  getAnalysisState,
  getFleetRecords,
  searchCorpus,
  type AnalysisState,
  type FleetRecordsResponse,
  type SearchHit,
} from "../api";
import { errorMessage, formatTime } from "../format";
import {
  AuthorityMark,
  Badge,
  FleetNotice,
  HostChips,
  HostLabel,
  SyncBadge,
  SyncDegradedNotice,
  UnopenedNote,
  Quoted,
  inHostScope,
  syncRowClass,
  useFleetHosts,
  type HostScope,
} from "../analysis";

const HARNESSES = ["omp", "codex", "claude-code"];

function ExplorePage() {
  const [analysis, setAnalysis] = useState<AnalysisState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [searchText, setSearchText] = useState("");
  const [harness, setHarness] = useState<string | null>(null);
  const [hits, setHits] = useState<SearchHit[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);

  const loadState = useCallback(() => {
    setLoading(true);
    setError(null);
    getAnalysisState()
      .then((value) => setAnalysis(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(loadState, [loadState]);

  async function submitSearch(event: FormEvent) {
    event.preventDefault();
    const q = searchText.trim();
    if (!q) return;
    setSearching(true);
    setSearchError(null);
    try {
      const result = await searchCorpus({ q, harness: harness ?? undefined });
      setHits(result.hits);
    } catch (reason) {
      setSearchError(errorMessage(reason));
    } finally {
      setSearching(false);
    }
  }

  const runs = analysis?.runs ?? [];
  const cookbook = analysis?.cookbook ?? [];

  return (
    <section className="page explore-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Analysis control plane</p>
          <h1>Explore</h1>
          <p className="subtitle">
            Recipes, run receipts, and provenance-preserving corpus retrieval.
          </p>
        </div>
        {analysis && (
          <div className="heading-meta">
            <span className="count-label">
              {runs.length} recorded {runs.length === 1 ? "run" : "runs"}
            </span>
          </div>
        )}
      </div>

      {loading && !analysis && (
        <div className="state-card"><span className="spinner" /> Reading analysis state…</div>
      )}
      {error && !analysis && (
        <div className="state-card error-state">
          <strong>Analysis state could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={loadState}>Try again</button>
        </div>
      )}

      {analysis && !analysis.configured && (
        <div className="state-card empty-state configure-empty">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Analysis state not configured</strong>
          <span>Run <code>babel storage configure</code> to connect durable analysis storage.</span>
        </div>
      )}

      {analysis?.configured && (
        <>
          {/* Today's normal case: Code has not implemented the worker
              protocol, and the web surface never starts a run regardless
              (§2.6 consent, §2.7 session lifetime). Said plainly, as a
              statement about the deployment rather than an empty state that
              reads like a bug. The prose comes from the server verbatim. */}
          {!analysis.worker.available && (
            <article className="card worker-card">
              <div>
                <p className="eyebrow">Worker</p>
                <h2>Exploration is not startable here</h2>
              </div>
              <p>{analysis.worker.detail}</p>
              <p className="muted">
                Everything already recorded stays browseable below; new runs start from a terminal
                with <code>babel explore</code> once an analysis worker is available.
              </p>
            </article>
          )}

          {analysis.sync_degraded && <SyncDegradedNotice detail={analysis.sync_detail} />}

          <article className="card runs-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Receipts</p>
                <h2>Recent runs</h2>
              </div>
              <span className="count-label">{runs.length}</span>
            </div>
            {runs.length === 0 ? (
              <p className="muted">
                No exploration runs are recorded. A run's receipt appears here as soon as
                <code> babel explore</code> records one — including failed runs, which keep their
                receipts too.
              </p>
            ) : (
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>Receipt</th>
                      <th>Run</th>
                      <th>Recorded</th>
                      <th>Host</th>
                      <th>Commit state</th>
                      <th>Authority</th>
                      <th>Counts</th>
                    </tr>
                  </thead>
                  <tbody>
                    {runs.map((run) => {
                      const recorded = formatTime(run.recorded_at);
                      return (
                        <tr key={run.receipt_id}>
                          <td className="mono" title={run.receipt_id}>
                            {run.receipt_id.slice(0, 14)}
                            {run.revision > 1 && <span className="secondary">rev {run.revision}</span>}
                          </td>
                          <td className="mono">{run.run_id}</td>
                          <td>
                            {recorded
                              ? <span title={recorded.absolute}>{recorded.relative}</span>
                              : <span className="muted">—</span>}
                          </td>
                          {/* The host is the shared catalog's, not the
                              receipt's: a run the catalog cannot attribute
                              renders as unattributed rather than as this
                              machine. */}
                          <td><HostLabel mark={{ host: run.host, host_attributed: run.host_attributed }} /></td>
                          <td><SyncBadge sync={run.sync} /></td>
                          <td>
                            <AuthorityMark authority={run.authority} />
                          </td>
                          <td className="counts-cell">
                            <span>{run.counts.retrieval} retrievals</span>
                            <span>{run.counts.deferred} deferred</span>
                            <span>{run.counts.failures} failures</span>
                            {run.counts.redactions > 0 && (
                              <span className="redaction-alert" title="Credential-shaped values were removed while building this receipt.">
                                {run.counts.redactions} redactions
                              </span>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </article>

          <FleetRecordsCard />

          <article className="card cookbook-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Cookbook</p>
                <h2>Recipes</h2>
              </div>
              <span className="count-label">{cookbook.length}</span>
            </div>
            <p className="muted">
              Versioned, reviewable investigation guidance. Recipes structure exploration without
              constraining what discovery may propose; a draft is simply not enabled by default.
            </p>
            {cookbook.length === 0 ? (
              <p className="muted">No cookbook assets are loaded.</p>
            ) : (
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>Recipe</th>
                      <th>Kind</th>
                      <th>Enabled</th>
                      <th>Scope</th>
                      <th>Stages</th>
                    </tr>
                  </thead>
                  <tbody>
                    {cookbook.map((recipe) => (
                      <tr key={recipe.id}>
                        <td>
                          <strong>{recipe.title}</strong>
                          <span className="secondary mono">{recipe.id} · v{recipe.version}</span>
                        </td>
                        <td><Badge label={recipe.kind} tone={recipe.kind === "lens" ? "cyan" : "neutral"} /></td>
                        <td>
                          {recipe.default
                            ? <Badge label="default" tone="green" />
                            : <Badge label="draft" tone="neutral" />}
                        </td>
                        <td>{recipe.scope.join(", ")}</td>
                        <td>{recipe.stages.join(", ")}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </article>
        </>
      )}

      <article className="card search-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Retrieval</p>
            <h2>Search the corpus</h2>
          </div>
        </div>
        <p className="muted">
          Full-text matches over normalized archive events. Results are matches, not rankings:
          the API carries no relevance score, and position in this list says nothing about
          evidence strength.
        </p>
        <form className="search-form" onSubmit={submitSearch}>
          <label className="search-field">
            <span className="sr-only">Search the corpus</span>
            <input
              type="search"
              value={searchText}
              onChange={(event) => setSearchText(event.target.value)}
              placeholder="Search normalized events…"
              autoComplete="off"
            />
          </label>
          <div className="filter-chips" aria-label="Filter by harness">
            <button
              type="button"
              className={!harness ? "chip active" : "chip"}
              onClick={() => setHarness(null)}
            >
              All
            </button>
            {HARNESSES.map((name) => (
              <button
                type="button"
                className={harness === name ? "chip active" : "chip"}
                onClick={() => setHarness(name)}
                key={name}
              >
                {name}
              </button>
            ))}
          </div>
          <button type="submit" disabled={searching || !searchText.trim()}>
            {searching && <span className="spinner small" />}
            {searching ? "Searching…" : "Search"}
          </button>
        </form>

        {searchError && <p className="inline-error" role="alert">Search failed: {searchError}</p>}
        {hits !== null && !searchError && (
          hits.length === 0 ? (
            <p className="muted">No events matched.</p>
          ) : (
            <ul className="hit-list">
              {hits.map((hit) => {
                const time = formatTime(hit.time);
                return (
                  <li className="hit-entry" key={`${hit.selector}-${hit.index}`}>
                    <div className="hit-heading">
                      <span className="harness-badge">{hit.harness}</span>
                      <span className="kind-label">{hit.kind}</span>
                      {hit.role && <span className="kind-label">{hit.role}</span>}
                      {hit.partial && <Badge label="torn record" tone="amber" />}
                      {time && <time dateTime={hit.time} title={time.absolute}>{time.relative}</time>}
                    </div>
                    <Quoted label="Archive excerpt — untrusted, bounded" text={hit.text}>
                      <div className="hit-footing">
                        <span className="evidence-locator mono">
                          {hit.locator.path}
                          {hit.locator.line > 0 ? `:${hit.locator.line}` : ""}
                        </span>
                        <Link className="evidence-open" to={`/sessions/${encodeURIComponent(hit.selector)}`}>
                          Open session →
                        </Link>
                      </div>
                    </Quoted>
                  </li>
                );
              })}
            </ul>
          )
        )}
      </article>
    </section>
  );
}

// FleetRecordsCard is what the deployment has committed, as this machine can
// read it (issue #109 item 4).
//
// It lives on Explore rather than on a page of its own because it answers the
// question the receipt strip beside it answers, one scope wider: the strip is
// what this machine ran, and this is what every machine produced. A separate
// page would have split one question in two.
//
// Its default scope is every host, unlike the frontier and the inbox. Those are
// working surfaces whose default must stay this machine's own backlog; this card
// exists only to show the fleet, and a fleet card defaulting to one host would
// have nothing to say.
function FleetRecordsCard() {
  const fleet = useFleetHosts();
  const [scope, setScope] = useState<HostScope>({ fleet: true, host: null });
  const [data, setData] = useState<FleetRecordsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    // Staged output is asked for on purpose: SPEC.md §6.5 requires it to be
    // visible, and a card that read committed records only would make a host
    // mid-outage look idle rather than behind.
    getFleetRecords({ pending: true })
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  // Narrowed client-side, so a chip hides rows the browser already holds.
  const items = (data?.items ?? []).filter((item) => inHostScope(item, scope));

  if (data && !data.configured) {
    return (
      <article className="card fleet-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Fleet</p>
            <h2>Committed across the deployment</h2>
          </div>
        </div>
        <FleetNotice />
      </article>
    );
  }

  return (
    <article className="card fleet-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Fleet</p>
          <h2>Committed across the deployment</h2>
        </div>
        {data && (
          <span className="count-label">
            {data.items.length} shown
            {data.pending > 0 && <span className="chip-pending">{data.pending} staged</span>}
          </span>
        )}
      </div>
      <p className="muted">
        Every host's Phase B records as this machine can read them: decrypted here, with this
        machine's own keys, and never written into its durable store. A record sealed under a key
        this instance does not hold is listed with the reason rather than hidden.
      </p>

      <HostChips
        hosts={data?.hosts ?? fleet.hosts}
        scope={scope}
        localHost={fleet.localHost}
        onSelect={setScope}
      />

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the fleet…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The fleet's records could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <p className="muted">No host has committed records matching this filter.</p>
      )}

      {items.length > 0 && (
        <div className="table-scroll">
          <table className="frontier-table fleet-table">
            <thead>
              <tr>
                <th>Record</th>
                <th>Kind</th>
                <th>Host</th>
                <th>Sync</th>
                <th>Actor</th>
                <th>Committed</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const committed = formatTime(item.committed_at);
                return (
                  <tr key={item.record_id} className={syncRowClass(item)}>
                    <td className="statement-cell">
                      {/* A kind the retrieval surface does not hold -- a
                          proposal, a link, a receipt -- has no summary by
                          construction, and that absence is stated rather than
                          left as a blank cell. */}
                      {item.summary ? (
                        <strong className="untrusted-inline">{item.summary}</strong>
                      ) : (
                        <span className="muted no-summary">no summary for a {item.kind} record</span>
                      )}
                      <span className="secondary mono">{item.record_id}</span>
                      <UnopenedNote reason={item.unopened} />
                    </td>
                    <td><Badge label={item.kind} tone="neutral" /></td>
                    <td><HostLabel mark={item} /></td>
                    <td><SyncBadge sync={item.sync} /></td>
                    <td className="mono untrusted-inline">{item.actor}</td>
                    <td>
                      {committed
                        ? <span title={committed.absolute}>{committed.relative}</span>
                        : <span className="muted">—</span>}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </article>
  );
}

export default ExplorePage;
