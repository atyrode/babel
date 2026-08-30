import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getReviewQueue, type QueueItem, type ReviewQueueResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, reviewTone } from "../analysis";

const TYPES = ["hypothesis", "finding", "proposal"];
const STATUSES = ["accepted", "rejected", "deferred", "duplicate", "refine-requested"];

function ReviewPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<ReviewQueueResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [type, setType] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  const load = useCallback((typeFilter: string | null, statusFilter: string | null) => {
    setLoading(true);
    setError(null);
    getReviewQueue({
      type: typeFilter ?? undefined,
      status: statusFilter ?? undefined,
    })
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => load(type, status), [load, type, status]);

  function openItem(item: QueueItem) {
    navigate(`/review/${encodeURIComponent(item.subject.type)}/${encodeURIComponent(item.subject.id)}`);
  }

  const items = data?.items ?? [];

  return (
    <section className="page review-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Append-only decisions</p>
          <h1>Review</h1>
          <p className="subtitle">
            Hypotheses, findings, and proposals awaiting a human decision. Every disposition is
            an appended event: nothing here is edited, and nothing is deleted.
          </p>
        </div>
        {data && (
          <div className="heading-meta">
            <span className="count-label">
              {items.length} {items.length === 1 ? "record" : "records"}
            </span>
          </div>
        )}
      </div>

      <div className="toolbar card review-toolbar">
        <div className="filter-chips" aria-label="Filter by record type">
          <button type="button" className={!type ? "chip active" : "chip"} onClick={() => setType(null)}>
            All types
          </button>
          {TYPES.map((name) => (
            <button
              type="button"
              className={type === name ? "chip active" : "chip"}
              onClick={() => setType(name)}
              key={name}
            >
              {name}
            </button>
          ))}
        </div>
        <div className="filter-chips" aria-label="Filter by review status">
          <button type="button" className={!status ? "chip active" : "chip"} onClick={() => setStatus(null)}>
            Awaiting decision
          </button>
          <button
            type="button"
            className={status === "all" ? "chip active" : "chip"}
            onClick={() => setStatus("all")}
          >
            Everything
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

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the review queue…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The review queue could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load(type, status)}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>{status ? "No records match these filters" : "Nothing awaits a decision"}</strong>
          <span>
            {status
              ? "Widen the filters — decided records are under \u201cEverything\u201d or their own status."
              : "Records enter this queue when exploration develops them far enough for review."}
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Record</th>
                  <th>Type</th>
                  <th>Status</th>
                  <th className="numeric">Decisions</th>
                  <th>Last decided</th>
                  <th className="numeric">Refinements</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const lastDecided = formatTime(item.last_decided_at);
                  return (
                    <tr
                      key={`${item.subject.type}-${item.subject.id}`}
                      tabIndex={0}
                      role="link"
                      onClick={() => openItem(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openItem(item);
                      }}
                    >
                      <td className="statement-cell">
                        <strong className="untrusted-inline">{item.excerpt || "Untitled record"}</strong>
                        <span className="secondary mono">{item.subject.id}</span>
                      </td>
                      <td><Badge label={item.subject.type} tone="neutral" /></td>
                      <td><Badge label={item.status} tone={reviewTone(item.status)} /></td>
                      <td className="numeric mono">{item.decisions}</td>
                      <td>
                        {lastDecided
                          ? <span title={lastDecided.absolute}>{lastDecided.relative}</span>
                          : <span className="muted">never</span>}
                      </td>
                      <td className="numeric mono">{item.refinements}</td>
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

export default ReviewPage;
