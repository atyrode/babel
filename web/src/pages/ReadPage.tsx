import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import {
  getEvaluationList,
  type EvaluationCoverageCounts,
  type EvaluationItem,
  type EvaluationListResponse,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import {
  coverageLabel,
  CoverageSummary,
  kindLabel,
  laneLabel,
  laneTone,
  Reception,
  roleBasis,
  roleLabel,
  sortBasis,
  sortLabel,
  StaleNotice,
} from "../evaluation";

// Read answers one question: what has Babel found?
//
// It replaces four listings — findings, proposals, the hypothesis frontier and
// the evaluation backlog — that differed in which subset of one corpus they
// drew and in nothing else a reader cares about. Four destinations for one
// question is how the reader ended up needing to know the record kinds before
// he could look at the output; the kinds are a filter on one list now, which
// is what they always were.
//
// Every filter and every ordering those four pages offered survives as a
// control here. The frontier's exploration statuses — untriaged, queued,
// investigating, promoted — do not, and their absence is deliberate: they are
// the pipeline's own bookkeeping about a candidate, not a standing anybody
// rules on, and the one that matters to a reader rides each hypothesis row as
// a fact. What a reader filters by is the standing: open, accepted, rejected,
// deferred, duplicate, refine-requested, and the outcome lanes beyond them.
//
// Paging is the server's, and the snapshot is pinned in the URL, because §8.5
// requires pagination to stay consistent while publication continues. Without
// the pin, page two is cut from a differently ordered set and rows silently
// move between pages. Every control writes the URL for the ordinary reason:
// a filtered list is a thing an operator reloads, shares and walks back
// through with the browser's own Back button.

const PAGE_SIZE = 25;

function ReadPage() {
  const [params, setParams] = useSearchParams();
  const { pathname } = useLocation();
  const [data, setData] = useState<EvaluationListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const sort = params.get("sort") ?? "";
  const lane = params.get("lane") ?? "";
  const kind = params.get("kind") ?? "";
  const coverage = params.get("coverage") ?? "";
  const role = params.get("role") ?? "";
  const snapshot = params.get("snapshot") ?? "";
  const offset = Math.max(0, Number(params.get("offset") ?? 0) || 0);

  // fetched is the query the rows on screen came from. It exists because
  // pinning the snapshot below rewrites the URL, and without it that rewrite
  // would re-request the identical page: the server answered from a snapshot,
  // the client wrote that snapshot into the query, and the query changed. The
  // second request returned the same rows and replaced every one of them,
  // which cost a round trip and detached whatever the operator was about to
  // click.
  const fetched = useRef("");
  const requested = JSON.stringify({ sort, lane, kind, coverage, role, snapshot, offset });

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getEvaluationList({ sort, lane, kind, coverage, role, snapshot, limit: PAGE_SIZE, offset })
      .then((answer) => {
        fetched.current = JSON.stringify({
          sort, lane, kind, coverage, role, snapshot: answer.snapshot || snapshot, offset,
        });
        setData(answer);
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [sort, lane, kind, coverage, role, snapshot, offset]);

  useEffect(() => {
    if (fetched.current === requested) return;
    load();
  }, [load, requested]);

  // The first answer pins the ordering the rest of the paging walks. It is a
  // replacing navigation: pinning is not something the operator did, so it
  // must not cost him a Back press to undo.
  //
  // The path guard is load-bearing rather than defensive. The pin writes a URL
  // built from this page's own captured query, and `replace` overwrites
  // whatever entry is current — so if the answer lands in the same tick as a
  // click into a record, the pin replaces the record's URL with the listing's
  // and the reader is silently returned to the list he just left. Measured:
  // walking /findings/:id straight after /findings reproduced it every time.
  useEffect(() => {
    if (pathname !== "/read") return;
    if (!data || snapshot || !data.snapshot) return;
    const next = new URLSearchParams(params);
    next.set("snapshot", data.snapshot);
    setParams(next, { replace: true });
  }, [data, snapshot, params, setParams, pathname]);

  // select changes one facet and starts the ordering again from its first
  // page. The snapshot is dropped with it: a snapshot identifies one ranked
  // set, and carrying it onto a different filter would page through an
  // ordering that no longer exists.
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

  const items = data?.items ?? [];
  const vocabulary = data?.vocabulary;
  const total = data?.total ?? 0;
  const shownSort = data?.query.sort || "recommended";
  const basis = sortBasis(shownSort);

  return (
    <section className="page read-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Output</p>
          <h1>What has Babel found?</h1>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {total.toLocaleString()} {total === 1 ? "record" : "records"}
            </span>
          )}
        </div>
      </div>

      {vocabulary && (
        <div className="toolbar surface read-filters">
          <label>
            <span>Kind</span>
            <select
              data-filter="kind"
              value={kind}
              onChange={(event) => select("kind", event.target.value)}
            >
              <option value="">Every kind</option>
              {vocabulary.kinds.map((name) => (
                <option value={name} key={name}>{kindLabel(name)}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Standing</span>
            <select
              data-filter="lane"
              value={lane}
              onChange={(event) => select("lane", event.target.value)}
            >
              <option value="">Any standing</option>
              {vocabulary.lanes.map((name) => (
                <option value={name} key={name}>{laneLabel(name)}</option>
              ))}
            </select>
          </label>
          <label>
            <span>Order</span>
            <select
              data-filter="sort"
              value={shownSort === "recommended" ? "" : shownSort}
              onChange={(event) => select("sort", event.target.value)}
            >
              {vocabulary.sorts.map((name) => (
                <option value={name === "recommended" ? "" : name} key={name}>
                  {sortLabel(name)}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>Reviewed</span>
            <select
              data-filter="coverage"
              value={coverage}
              onChange={(event) => select("coverage", event.target.value)}
            >
              <option value="">Any coverage</option>
              {vocabulary.coverage.map((name) => (
                <option value={name} key={name}>{coverageLabel(name)}</option>
              ))}
            </select>
          </label>
          <label>
            <span>By role</span>
            <select
              data-filter="role"
              value={role}
              onChange={(event) => select("role", event.target.value)}
            >
              <option value="">Every role</option>
              {vocabulary.roles.map((name) => (
                <option value={name} key={name}>{roleLabel(name)}</option>
              ))}
            </select>
          </label>
        </div>
      )}

      {/* The basis is stated rather than implied, and it is one line. §8.5
          requires every ordering to name what it is computed from: a row of
          words with no explanation is the ranking an operator cannot argue
          with, and a tooltip is an explanation only for the reader who
          already suspected there was one. */}
      {basis && <p className="muted read-basis">{basis}</p>}

      {data && <StaleNotice stale={data.stale} unavailable={data.unavailable} />}

      {loading && !data && (
        <div className="surface state-note"><span className="spinner" /> Reading the output…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>The output could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing matches this view</strong>
          <span>
            That is a statement about the filters, not about what Babel has found.{" "}
            <button type="button" className="link-button" onClick={() => setParams(new URLSearchParams())}>
              Clear them
            </button>
            .
          </span>
        </div>
      )}

      {items.length > 0 && (
        <ol className="output-list">
          {items.map((item) => (
            <OutputRow item={item} key={`${item.artifact.subject.kind}-${item.artifact.subject.id}`} />
          ))}
        </ol>
      )}

      {data && (total > PAGE_SIZE || offset > 0) && (
        <div className="pager surface">
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

      {/* §8.5's coverage inventory. It was a page of its own and is a peel
          here, because "what has nobody read" is a question about this list
          rather than a destination beside it — and because the counts arrive
          with every page of the listing, so opening it costs no request. */}
      {data && (
        <details className="peel coverage-peel">
          <summary>
            What has nobody read yet?
            <span className="peel-count">
              {data.coverage.unreviewed.toLocaleString()} unreviewed
            </span>
          </summary>
          <div className="peel-body">
            <CoverageSummary coverage={data.coverage} />
            {vocabulary && (
              <RoleTotals roles={vocabulary.roles} byRole={data.coverage.by_role} />
            )}
            <p className="muted">
              What analysis is allowed to spend reviewing this is in{" "}
              <Link to="/settings?section=policy">Settings</Link>.
            </p>
          </div>
        </details>
      )}
    </section>
  );
}

// One row: the record's own claim, and three facts.
//
// The three are what decide whether to open it — what kind of thing it is,
// where it stands, and who has said anything about it. The score it was ranked
// by is deliberately not among them: a decimal beside a vote count reads as a
// measurement of the idea, when it is a position in one ordering under one
// recorded policy. Everything else this record holds is a peel down on its own
// page, which is one click away and never four.
function OutputRow({ item }: { item: EvaluationItem }) {
  const created = formatTime(item.artifact.created_at);
  return (
    <li className="output-row" data-item={item.artifact.subject.id}>
      <Link
        className="output-claim untrusted-inline"
        to={`/r/${encodeURIComponent(item.artifact.subject.id)}`}
        title={created ? `Recorded ${created.absolute}` : undefined}
      >
        {item.artifact.title || "a record with no title recorded"}
      </Link>
      <span className="output-facts">
        <Badge label={kindLabel(item.artifact.subject.kind)} tone="neutral" />
        <Badge label={laneLabel(item.lane)} tone={laneTone(item.lane)} />
        <Reception reception={item.reception} />
      </span>
    </li>
  );
}

// The deployment-wide inventory, role by role.
//
// Coverage is role-specific because §4.12 makes it so: a reception vote says
// how an idea was received and discharges no evidence check, and an outcome
// verification is owed only where something was accepted. One number for six
// different questions would report a satisfied obligation nobody met.
//
// A role the projection reported nothing about is unknown rather than zero.
// Printing zeroes for it would report an obligation nobody measured as one
// nobody owes.
function RoleTotals({
  roles,
  byRole,
}: {
  roles: string[];
  byRole: Record<string, EvaluationCoverageCounts> | null;
}) {
  return (
    <table className="role-totals">
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
        {roles.map((name) => {
          const counts = byRole?.[name];
          return (
            <tr key={name}>
              <td>
                <strong>{roleLabel(name)}</strong>
                <span className="secondary">{roleBasis(name)}</span>
              </td>
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
  );
}

export default ReadPage;
