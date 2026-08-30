import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  getAnalysisState,
  searchCorpus,
  type AnalysisState,
  type SearchHit,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted } from "../analysis";

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
                      <th>Commit state</th>
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
                          <td>
                            <Badge
                              label={run.sync}
                              tone={run.sync === "committed" ? "green" : "amber"}
                            />
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

export default ExplorePage;
