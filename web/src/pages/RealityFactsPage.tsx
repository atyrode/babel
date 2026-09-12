import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { getRealityFacts, type FactsResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import { EntityName, FactValue, factTone } from "../reality";

// What Babel currently believes, newest first (SPEC.md §4.8, §8.4).
//
// This is the one listing on the Reality surface that is deliberately not
// exhaustive, and the reason is in the ledger's own shape: facts are indexed by
// the subject they are about, because that is how analysis asks. A reader who
// already knows the subject should be on that subject's page, where every
// revision about it is held. This page is for the reader who does not — the
// head of the ledger, so that "what has Babel concluded lately" is a question
// the interface can answer.
function RealityFactsPage() {
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const [data, setData] = useState<FactsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getRealityFacts(status || undefined)
      .then(setData)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [status]);

  useEffect(load, [load]);

  const items = data?.items ?? [];
  const statuses = data?.statuses ?? [];
  const total = statuses.reduce((sum, entry) => sum + entry.count, 0);

  return (
    <section className="page facts-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Reality Ledger</p>
          <h1>What it believes</h1>
          <p className="subtitle">
            The most recent revisions Babel has recorded, whatever they are about. A fact is never
            edited: a correction is a new revision, and the one it replaced stays readable.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {status
                ? `${items.length.toLocaleString()} of ${total.toLocaleString()} revisions`
                : `${total.toLocaleString()} recent ${total === 1 ? "revision" : "revisions"}`}
            </span>
          )}
        </div>
      </div>

      {statuses.length > 0 && (
        <div className="toolbar surface">
          <div className="filter-chips" aria-label="Filter by status">
            <button
              type="button"
              className={status === "" ? "chip active" : "chip"}
              onClick={() => setParams({})}
            >
              All {total}
            </button>
            {statuses.map((entry) => (
              <button
                type="button"
                key={entry.status}
                className={status === entry.status ? "chip active" : "chip"}
                onClick={() => setParams({ status: entry.status })}
              >
                {entry.status} {entry.count}
              </button>
            ))}
          </div>
        </div>
      )}

      {loading && !data && (
        <div className="surface state-note"><span className="spinner" /> Reading the ledger…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>The ledger could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>{status ? `Nothing is ${status}` : "Babel believes nothing yet"}</strong>
          <span>
            {status
              ? "No recent revision holds that status."
              : "A fact enters the ledger when you answer a question and accept the plan that " +
                "interprets it, or when a trusted source is imported. Nothing has been recorded " +
                "yet — the questions page is where it starts."}
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="surface flush">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Subject</th>
                  <th>Says</th>
                  <th>Standing</th>
                  <th>Authority</th>
                  <th>Recorded</th>
                </tr>
              </thead>
              <tbody>
                {items.map((row) => {
                  const recorded = formatTime(row.fact.recorded_at);
                  return (
                    <tr key={row.fact.id}>
                      <td className="statement-cell">
                        <EntityName entity={row.subject} />
                        {row.subject.kind && <span className="secondary">{row.subject.kind}</span>}
                      </td>
                      <td className="statement-cell">
                        {/* The predicate is the link to the revision's own
                            page, which is where the chain it sits in — what
                            it replaced and what replaced it — is readable. */}
                        <Link to={`/ask/facts/${encodeURIComponent(row.fact.id)}`}>
                          <span className="mono">{row.fact.predicate}</span>{" "}
                          <FactValue fact={row.fact} />
                        </Link>
                      </td>
                      <td><Badge label={row.fact.status} tone={factTone(row.fact.status)} /></td>
                      <td>
                        {/* The authority's kind, not its identifier: "an
                            operator said so" and "analysis proposed it" are
                            the two things that matter here, and the exact
                            actor is on the revision's own page. */}
                        <span className="secondary">{row.fact.authority.kind}</span>
                      </td>
                      <td>
                        {recorded
                          ? <span title={recorded.absolute}>{recorded.relative}</span>
                          : <span className="muted">—</span>}
                      </td>
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

export default RealityFactsPage;
