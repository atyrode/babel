import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getFindings, type FindingsResponse, type FindingSummary } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, reviewTone } from "../analysis";
import { FrontierToggle } from "./HypothesesPage";

function FindingsPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<FindingsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getFindings()
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  function openFinding(item: FindingSummary) {
    navigate(`/findings/${encodeURIComponent(item.id)}`);
  }

  const items = data?.items ?? [];

  return (
    <section className="page findings-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Hypothesis frontier</p>
          <h1>Findings</h1>
          <p className="subtitle">
            Consolidated observations — still interpretations for review, not verified facts.
            Every finding keeps its supporting and conflicting evidence attached.
          </p>
        </div>
        <div className="heading-meta">
          {data && <span className="count-label">{data.total} findings</span>}
          <FrontierToggle />
        </div>
      </div>

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading findings…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>Findings could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>No findings yet</strong>
          <span>
            A finding is created only from developed observations. None have been consolidated —
            the hypotheses list shows what exploration is still working through.
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Finding</th>
                  <th>Review</th>
                  <th className="numeric">Observations</th>
                  <th className="numeric">Hypotheses</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const created = formatTime(item.created_at);
                  return (
                    <tr
                      key={item.id}
                      tabIndex={0}
                      role="link"
                      onClick={() => openFinding(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openFinding(item);
                      }}
                    >
                      <td className="statement-cell">
                        <strong className="untrusted-inline">{item.title}</strong>
                        <span className="secondary mono">{item.id}</span>
                      </td>
                      <td><Badge label={item.review_status} tone={reviewTone(item.review_status)} /></td>
                      <td className="numeric mono">{item.observations}</td>
                      <td className="numeric mono">{item.hypotheses}</td>
                      <td>
                        {created
                          ? <span title={created.absolute}>{created.relative}</span>
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

export default FindingsPage;
