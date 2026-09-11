import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  getEvaluationCoverage,
  getEvaluationList,
  type EvaluationCoverageResponse,
  type EvaluationListResponse,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import {
  coverageLabel,
  coverageTone,
  CoverageSummary,
  kindLabel,
  roleBasis,
  roleLabel,
  StaleNotice,
  SubjectLink,
} from "../evaluation";

// The coverage inventory (SPEC.md §8.5): what has not been checked, by kind
// and by review role, whether or not anybody voted on it.
//
// It is a page of its own rather than a filter on the backlog, and the reason
// is in the section: "Under-reviewed is a broader sort and does not replace
// this exact inventory". The backlog's Under-reviewed order ranks by exposure
// and will happily put a much-reviewed record above a never-reviewed one if
// the policy says so. This answers a different question, exactly: what is
// owed, what is late, what is blocked, and what nothing can review at all.
//
// Three absences are visible here rather than implied. A never-reviewed record
// appears with no votes and is not sorted below records that have them. A role
// with no evaluator reads as a named gap rather than as a satisfied
// obligation. And the completion of the last sweep is stated beside the work
// still outstanding, because §8.5 makes "the check finished" and "everything
// is reviewed" separate facts.

const PAGE_SIZE = 25;

// The inventory opens on what has never been reviewed, because that is the
// question the page exists for. Every other state is one selection away.
const DEFAULT_STATE = "unreviewed";

function EvaluationCoveragePage() {
  const [params, setParams] = useSearchParams();
  const [summary, setSummary] = useState<EvaluationCoverageResponse | null>(null);
  const [listing, setListing] = useState<EvaluationListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const state = params.get("state") ?? DEFAULT_STATE;
  const kind = params.get("kind") ?? "";
  const role = params.get("role") ?? "";
  const snapshot = params.get("snapshot") ?? "";
  const offset = Math.max(0, Number(params.get("offset") ?? 0) || 0);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    // Both reads in one pass: the inventory's totals and the rows behind
    // them are one answer, and a page that resolved them separately could
    // render a count of nine above a list of four.
    Promise.all([
      getEvaluationCoverage(),
      getEvaluationList({
        coverage: state,
        kind,
        role,
        snapshot,
        // Oldest due first, which is the order the reserved allocation
        // works through. A coverage list ordered by recommendation would
        // hide the oldest never-reviewed record behind whatever is most
        // interesting today.
        sort: "overdue",
        limit: PAGE_SIZE,
        offset,
      }),
    ])
      .then(([coverage, page]) => {
        setSummary(coverage);
        setListing(page);
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [state, kind, role, snapshot, offset]);

  useEffect(load, [load]);

  useEffect(() => {
    if (!listing || snapshot || !listing.snapshot) return;
    const next = new URLSearchParams(params);
    next.set("snapshot", listing.snapshot);
    setParams(next, { replace: true });
  }, [listing, snapshot, params, setParams]);

  function select(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("offset");
    next.delete("snapshot");
    setParams(next);
  }

  function page(nextOffset: number) {
    const next = new URLSearchParams(params);
    if (nextOffset > 0) next.set("offset", String(nextOffset));
    else next.delete("offset");
    setParams(next);
  }

  const items = listing?.items ?? [];
  const total = listing?.total ?? 0;
  const byRole = summary?.coverage.by_role ?? null;

  return (
    <section className="page evaluation-coverage-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Evaluation</p>
          <h1>Coverage</h1>
          <p className="subtitle">
            What has not been checked, across every reviewable kind Babel produces. A record with
            no votes is found here whether or not anything ranked it, and a role nothing can
            review is a gap rather than a pass.
          </p>
        </div>
        <div className="heading-meta">
          <Link className="back-link" to="/evaluation">← Backlog</Link>
          <Link className="back-link" to="/evaluation/policy">Review policy →</Link>
        </div>
      </div>

      {loading && !summary && (
        <div className="state-card"><span className="spinner" /> Reading the coverage inventory…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The coverage inventory could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}

      {summary && <CoverageSummary coverage={summary.coverage} />}

      {summary && (
        <div className="card evaluation-role-card">
          <h2>By review role</h2>
          <p className="muted">
            Coverage is role-specific. A reception vote says how an idea was received and
            discharges no evidence check; an outcome verification is owed only where something was
            accepted. A role with no evaluator stays listed as a gap. These columns count role
            rows, including roles a record supports but does not yet owe — the row's own standing
            says which, and the totals above count records rather than roles.
          </p>
          <table className="evaluation-roles">
            <thead>
              <tr>
                <th>Role</th>
                <th className="numeric">Never reviewed</th>
                <th className="numeric">Due</th>
                <th className="numeric">Overdue</th>
                <th className="numeric">Blocked</th>
                <th className="numeric">No evaluator</th>
                <th className="numeric">Reviewed</th>
              </tr>
            </thead>
            <tbody>
              {summary.roles.map((name) => {
                const counts = byRole?.[name];
                return (
                  <tr key={name}>
                    <td>
                      <strong>{roleLabel(name)}</strong>
                      <span className="secondary">{roleBasis(name)}</span>
                    </td>
                    {/* A role the projection reported nothing about is
                        unknown, not zero. Printing zeroes for it would
                        report an obligation nobody measured as one nobody
                        owes. */}
                    {counts ? (
                      <>
                        <td className="numeric mono">{counts.unreviewed}</td>
                        <td className="numeric mono">{counts.due}</td>
                        <td className="numeric mono">{counts.overdue}</td>
                        <td className="numeric mono">{counts.blocked}</td>
                        <td className="numeric mono">{counts.unsupported}</td>
                        <td className="numeric mono">{counts.reviewed}</td>
                      </>
                    ) : (
                      <td className="muted" colSpan={6}>not reported by this projection</td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {summary && (
        <div className="toolbar card evaluation-toolbar">
          <div className="filter-chips" aria-label="Coverage state">
            {listing?.vocabulary.coverage.map((name) => (
              <button
                type="button"
                key={name}
                className={state === name ? "chip active" : "chip"}
                onClick={() => select("state", name)}
                aria-pressed={state === name}
              >
                {coverageLabel(name)}
              </button>
            ))}
          </div>
          <div className="evaluation-filters">
            <label>
              <span>Kind</span>
              <select value={kind} onChange={(event) => select("kind", event.target.value)}>
                <option value="">Every kind</option>
                {summary.kinds.map((name) => (
                  <option value={name} key={name}>{kindLabel(name)}</option>
                ))}
              </select>
            </label>
            <label>
              <span>Review role</span>
              <select value={role} onChange={(event) => select("role", event.target.value)}>
                <option value="">Every role</option>
                {summary.roles.map((name) => (
                  <option value={name} key={name}>{roleLabel(name)}</option>
                ))}
              </select>
            </label>
          </div>
        </div>
      )}

      {listing && <StaleNotice stale={listing.stale} unavailable={listing.unavailable} />}

      {listing && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">✓</span>
          <strong>Nothing is {coverageLabel(state).toLowerCase()} in this selection</strong>
          <span>
            This is what the last completed check found, not a claim that no further review is
            useful. The counts above say when that check finished.
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table evaluation-coverage-table">
              <thead>
                <tr>
                  <th>Record</th>
                  <th>Kind</th>
                  <th>Standing</th>
                  <th title="Roles with no completed review. A role can be supported without being required yet; the standing on each names what would make it due.">
                    Roles not yet reviewed
                  </th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const created = formatTime(item.artifact.created_at);
                  const outstanding = (item.review_coverage ?? []).filter(
                    (row) => row.state !== "reviewed" && row.state !== "not_applicable",
                  );
                  return (
                    <tr key={item.artifact.subject.id} data-item={item.artifact.subject.id}>
                      <td className="statement-cell">
                        <SubjectLink subject={item.artifact.subject} title={item.artifact.title} />
                        {item.coverage_reason && (
                          <span className="secondary untrusted-inline">{item.coverage_reason}</span>
                        )}
                      </td>
                      <td>{kindLabel(item.artifact.subject.kind)}</td>
                      <td>
                        <Badge label={coverageLabel(item.coverage)} tone={coverageTone(item.coverage)} />
                      </td>
                      <td>
                        {outstanding.length === 0 ? (
                          <span className="muted">—</span>
                        ) : (
                          <span className="tag-list">
                            {outstanding.map((row) => (
                              <span
                                className="tag"
                                key={row.role}
                                title={row.reason || roleBasis(row.role)}
                              >
                                {roleLabel(row.role)}: {coverageLabel(row.state)}
                              </span>
                            ))}
                          </span>
                        )}
                      </td>
                      <td>
                        {created ? (
                          <span title={created.absolute}>{created.relative}</span>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {listing && (total > PAGE_SIZE || offset > 0) && (
        <div className="pager card">
          <button type="button" disabled={offset === 0} onClick={() => page(Math.max(0, offset - PAGE_SIZE))}>
            ← Previous
          </button>
          <span className="muted">
            {(offset + 1).toLocaleString()}–{Math.min(offset + items.length, total).toLocaleString()} of{" "}
            {total.toLocaleString()}
          </span>
          <button
            type="button"
            disabled={offset + items.length >= total}
            onClick={() => page(offset + PAGE_SIZE)}
          >
            Next →
          </button>
        </div>
      )}
    </section>
  );
}

export default EvaluationCoveragePage;
