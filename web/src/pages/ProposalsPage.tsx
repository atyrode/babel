import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getProposals, type ProposalsResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, PartialListNotice, reviewTone } from "../analysis";

// The proposals listing: what Babel suggests doing about what it found.
//
// It is a listing rather than a section of the findings page because a proposal
// outlives the consolidation it came from — a run may propose against a claim it
// never consolidated (#114's candidate form) — and because this is the list an
// operator actually reads down. Impact and classification ride every row for
// exactly that reason: they are what makes a suggestion worth opening, and they
// are model gradings, so they render as the words the model chose and never as
// a score.

function ProposalsPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<ProposalsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getProposals()
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  const items = data?.items ?? [];

  return (
    <section className="page proposals-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Suggested improvements</p>
          <h1>Proposals</h1>
          <p className="subtitle">
            What Babel suggests doing about what it found. Every proposal is a suggestion for
            review with no external effect: nothing here is applied, opened, or acted on.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {items.length < data.total
                ? `${items.length.toLocaleString()} of ${data.total.toLocaleString()} proposals`
                : `${items.length.toLocaleString()} ${items.length === 1 ? "proposal" : "proposals"}`}
            </span>
          )}
        </div>
      </div>

      {data?.sync_degraded && <PartialListNotice />}

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading proposals…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>Proposals could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>No proposals yet</strong>
          <span>
            A proposal is written against a claim a run developed far enough to suggest something
            about. Findings and hypotheses show what exploration has produced so far.
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Proposal</th>
                  <th title="The model's own grading of how much this would matter. A grading, never a measurement.">
                    Impact
                  </th>
                  <th>Kind</th>
                  <th>Review</th>
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
                      onClick={() => navigate(`/proposals/${encodeURIComponent(item.id)}`)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") {
                          navigate(`/proposals/${encodeURIComponent(item.id)}`);
                        }
                      }}
                    >
                      <td className="statement-cell">
                        {item.title ? (
                          <strong className="untrusted-inline">{item.title}</strong>
                        ) : (
                          <span className="muted no-summary">no title recorded</span>
                        )}
                        {item.problem && (
                          <span className="secondary untrusted-inline">{item.problem}</span>
                        )}
                        <span className="secondary mono">{item.id}</span>
                        {item.advised && (
                          // Babel has read this one and written down what it
                          // thinks. The mark says only that, and it sits with
                          // the record's own text rather than in the Review
                          // column, because the Review column is where a
                          // ruling goes and advice is not one. The rows stay
                          // in the order the store returned them: nothing
                          // here sorts by what Babel thought.
                          <span
                            className="secondary advice-mark"
                            title="Babel read this proposal before you and left advice beside it — the case against acting on it, and any records worth comparing it with. Advice, not a ruling; open the proposal to read it."
                          >
                            Babel left advice on this one
                          </span>
                        )}
                      </td>
                      <td>
                        {item.impact
                          ? <span className="grading-word">{item.impact}</span>
                          : <span className="muted">—</span>}
                      </td>
                      <td>
                        {item.classification
                          ? <Badge label={item.classification} tone="neutral" />
                          : <span className="muted">—</span>}
                      </td>
                      <td>
                        {item.review_status
                          ? <Badge label={item.review_status} tone={reviewTone(item.review_status)} />
                          : <span className="muted">—</span>}
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

export default ProposalsPage;
