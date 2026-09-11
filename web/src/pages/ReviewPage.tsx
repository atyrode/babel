import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getReviewQueue, type QueueItem, type ReviewQueueResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, PartialListNotice, reviewTone } from "../analysis";
import { CitationCount } from "../references";
import { SteeringSection } from "../steering";

// The type chips lead with what a reader can decide in one sitting. A proposal
// says what to do about a claim and a finding says what the claim is; a
// hypothesis is still raw material, and thousands of them enrolled for triage
// bury the handful of records worth a verdict. Which is why the queue opens on
// proposals rather than on everything: an inbox whose first screen is the same
// deferred candidates the frontier already shows reads as an inbox with nothing
// in it.
const TYPES = ["proposal", "finding", "hypothesis"];
const STATUSES = ["accepted", "rejected", "deferred", "duplicate", "refine-requested"];

function ReviewPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<ReviewQueueResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [type, setType] = useState<string | null>("proposal");
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
              {data.total !== undefined && items.length < data.total
                ? `${items.length.toLocaleString()} of ${data.total.toLocaleString()} records`
                : `${items.length.toLocaleString()} ${items.length === 1 ? "record" : "records"}`}
            </span>
          </div>
        )}
      </div>

      {/* #115's capture box rides the review surface by operator decision
          (2026-08-31), and it sits above the queue rather than below it: the
          operator who came to review is the operator with something to say,
          and the box must not be buried under the rows it steers. */}
      <SteeringSection />

      <div className="toolbar card review-toolbar">
        <div className="filter-chips" aria-label="Filter by record type">
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
          <button type="button" className={!type ? "chip active" : "chip"} onClick={() => setType(null)}>
            All types
          </button>
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

      {data?.sync_degraded && <PartialListNotice />}

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
                  {/* The review disposition, which is a different vocabulary
                      from a candidate's exploration status: a record can be
                      "new" here and "deferred" on the frontier, and one column
                      named "status" for both read as a contradiction. */}
                  <th title="The last review decision recorded against this record.">Decision</th>
                  <th className="numeric">Decisions</th>
                  <th>Last decided</th>
                  <th className="numeric">Refinements</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const lastDecided = formatTime(item.last_decided_at);
                  // The status, the decision count, the last decision and the
                  // refinement count are derived from the record's append-only
                  // history. A merged row arrives without it, so those cells
                  // say nothing rather than reporting a decided-nothing.
                  const derived = item.local_host !== false;
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
                        {/* A kind with no searchable summary -- a proposal, a
                            link -- reaches this inbox with no excerpt, and it
                            says so rather than borrowing "Untitled record",
                            which would claim the record has no title. */}
                        {item.excerpt ? (
                          <strong className="untrusted-inline">{item.excerpt}</strong>
                        ) : (
                          <span className="muted no-summary">
                            no summary recorded for this {item.subject.type}
                          </span>
                        )}
                        <span className="secondary mono">{item.subject.id}</span>
                        {/* #113's compact form of the record's citations: how
                            many typed references leave it and arrive at it,
                            which is what makes an isolated candidate
                            distinguishable from one four observations rest on
                            before it is opened. Absent for a record nobody
                            counted, never rendered as a zero. */}
                        <CitationCount citations={item.citations} />
                      </td>
                      <td><Badge label={item.subject.type} tone="neutral" /></td>
                      <td>
                        {derived
                          ? <Badge label={item.status} tone={reviewTone(item.status)} />
                          : <span className="muted">—</span>}
                      </td>
                      <td className="numeric mono">
                        {derived ? item.decisions : <span className="muted">—</span>}
                      </td>
                      <td>
                        {!derived ? (
                          <span className="muted">—</span>
                        ) : lastDecided ? (
                          <span title={lastDecided.absolute}>{lastDecided.relative}</span>
                        ) : (
                          <span className="muted">never</span>
                        )}
                      </td>
                      <td className="numeric mono">
                        {derived ? item.refinements : <span className="muted">—</span>}
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

export default ReviewPage;
