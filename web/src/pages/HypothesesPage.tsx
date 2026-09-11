import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getHypotheses, type HypothesesResponse, type HypothesisSummary } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, PartialListNotice, statusTone } from "../analysis";

const STATUSES = ["untriaged", "queued", "investigating", "deferred", "rejected", "promoted"];

function HypothesesPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<HypothesesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  const load = useCallback((filter: string | null) => {
    setLoading(true);
    setError(null);
    getHypotheses(filter ? { status: filter } : {})
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => load(status), [load, status]);

  function openHypothesis(item: HypothesisSummary) {
    navigate(`/hypotheses/${encodeURIComponent(item.id)}`);
  }

  const items = data?.items ?? [];

  return (
    <section className="page hypotheses-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Raw material</p>
          <h1>Hypotheses</h1>
          <p className="subtitle">
            Every candidate idea, in the model's own wording. Nothing leaves the frontier:
            rejected and deferred candidates stay listed, visibly so.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {items.length < data.total
                ? `${items.length.toLocaleString()} of ${data.total.toLocaleString()} candidates`
                : `${items.length.toLocaleString()} ${items.length === 1 ? "candidate" : "candidates"}`}
            </span>
          )}
        </div>
      </div>

      <div className="toolbar card">
        <div className="filter-chips" aria-label="Filter by status">
          <button type="button" className={!status ? "chip active" : "chip"} onClick={() => setStatus(null)}>
            All
          </button>
          {STATUSES.map((name) => (
            <button
              type="button"
              className={status === name ? "chip active" : "chip"}
              onClick={() => setStatus(name)}
              key={name}
            >
              {name}
            </button>
          ))}
        </div>
      </div>

      {data?.sync_degraded && <PartialListNotice />}

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the frontier…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>Hypotheses could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load(status)}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>{status ? `No ${status} hypotheses` : "The frontier is empty"}</strong>
          <span>
            {status
              ? "No candidate currently has this status. The frontier keeps every candidate, so try another filter."
              : "No exploration has recorded candidates yet. Candidates appear here the moment a run persists them — before any sorting."}
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Candidate</th>
                  <th>Status</th>
                  <th className="numeric">Observations</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const created = formatTime(item.created_at);
                  // A merged row carries the statement and no observation
                  // count: the evidence is derived beside the record and does
                  // not travel with it, and a zero would say the candidate
                  // rests on none.
                  const derived = item.local_host !== false;
                  return (
                    <tr
                      key={item.id}
                      tabIndex={0}
                      role="link"
                      onClick={() => openHypothesis(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openHypothesis(item);
                      }}
                    >
                      <td className="statement-cell">
                        {item.statement ? (
                          <strong className="untrusted-inline">{item.statement}</strong>
                        ) : (
                          <span className="muted no-summary">no statement recorded</span>
                        )}
                        <span className="secondary mono">{item.id}</span>
                        {item.provisional_labels && item.provisional_labels.length > 0 && (
                          <span className="tag-list">
                            {item.provisional_labels.map((label) => (
                              <span className="tag" key={label}>{label}</span>
                            ))}
                          </span>
                        )}
                      </td>
                      <td><Badge label={item.status} tone={statusTone(item.status)} /></td>
                      <td className="numeric mono">
                        {derived ? item.observations : <span className="muted">—</span>}
                      </td>
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

export default HypothesesPage;
