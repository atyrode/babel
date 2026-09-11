import { useCallback, useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  getEvaluationList,
  type EvaluationItem,
  type EvaluationListResponse,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import {
  coverageLabel,
  coverageTone,
  kindLabel,
  laneLabel,
  laneTone,
  Reception,
  sortBasis,
  sortLabel,
  StaleNotice,
  SubjectLink,
} from "../evaluation";

// The evaluation inbox (SPEC.md §8.5): Babel's own output, ordered for the
// operator's next useful decision, with the alternative orders beside it.
//
// This page is deliberately not the review queue. The review queue is what has
// been enrolled for a decision, in enrolment order; this is the whole eligible
// set of reviewable output, ordered by a recorded policy whose basis, inputs
// and freshness are stated with the order. Both exist, and the difference is
// the reason the sort control names what each ordering is computed from
// instead of offering five bare words.
//
// Every control writes the URL, and that is load-bearing rather than tidy. A
// sort, a lane, a coverage state and a page are what an operator shares,
// reloads and walks back through with the browser's own Back button; a page
// that kept them in component state would lose all four on reload and make
// Back leave the surface entirely.
//
// The snapshot is pinned in the URL for a stronger reason. §8.5 requires
// pagination to be consistent while publication continues, so the first
// response's snapshot is written into the query and sent back on every page
// after it: without that, page two is cut from a differently ordered set and
// rows silently move between pages. The server answers from the current
// snapshot with `stale` set when a pinned one has been pruned, so a pin can
// never wedge the page.

const PAGE_SIZE = 25;

function EvaluationPage() {
  const [params, setParams] = useSearchParams();
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

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getEvaluationList({ sort, lane, kind, coverage, role, snapshot, limit: PAGE_SIZE, offset })
      .then(setData)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [sort, lane, kind, coverage, role, snapshot, offset]);

  useEffect(load, [load]);

  // The first answer pins the ordering the rest of the paging walks. It is a
  // replacing navigation: pinning is not something the operator did, so it
  // must not cost him a Back press to undo.
  useEffect(() => {
    if (!data || snapshot || !data.snapshot) return;
    const next = new URLSearchParams(params);
    next.set("snapshot", data.snapshot);
    setParams(next, { replace: true });
  }, [data, snapshot, params, setParams]);

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
  const updated = formatTime(data?.updated_at);

  return (
    <section className="page evaluation-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Evaluation</p>
          <h1>Backlog</h1>
          <p className="subtitle">
            What Babel has produced, ordered for your next useful decision. Reception is who said
            what — it is not evidence strength, and it is never an operator ruling. Nothing on this
            page votes.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {total.toLocaleString()} {total === 1 ? "record" : "records"}
            </span>
          )}
          <Link className="back-link" to="/evaluation/coverage">Coverage inventory →</Link>
          <Link className="back-link" to="/evaluation/policy">Review policy →</Link>
        </div>
      </div>

      {vocabulary && (
        <div className="toolbar card evaluation-toolbar">
          <div className="filter-chips" aria-label="Order">
            {vocabulary.sorts.map((name) => (
              <button
                type="button"
                key={name}
                className={shownSort === name ? "chip active" : "chip"}
                data-sort={name}
                onClick={() => select("sort", name === "recommended" ? "" : name)}
                title={sortBasis(name)}
                aria-pressed={shownSort === name}
              >
                {sortLabel(name)}
              </button>
            ))}
          </div>
          {/* The basis is stated rather than implied. §8.5 requires every
              ordering to name what it is computed from, and a row of five
              words with no explanation is the ranking an operator cannot
              argue with. */}
          {basis && <p className="muted evaluation-basis">{basis}</p>}

          <div className="evaluation-filters">
            <label>
              <span>Lifecycle</span>
              <select value={lane} onChange={(event) => select("lane", event.target.value)}>
                <option value="">Every lane</option>
                {vocabulary.lanes.map((name) => (
                  <option value={name} key={name}>{laneLabel(name)}</option>
                ))}
              </select>
            </label>
            <label>
              <span>Kind</span>
              <select value={kind} onChange={(event) => select("kind", event.target.value)}>
                <option value="">Every kind</option>
                {vocabulary.kinds.map((name) => (
                  <option value={name} key={name}>{kindLabel(name)}</option>
                ))}
              </select>
            </label>
            <label>
              <span>Coverage</span>
              <select value={coverage} onChange={(event) => select("coverage", event.target.value)}>
                <option value="">Any coverage</option>
                {vocabulary.coverage.map((name) => (
                  <option value={name} key={name}>{coverageLabel(name)}</option>
                ))}
              </select>
            </label>
            <label>
              <span>Review role</span>
              <select value={role} onChange={(event) => select("role", event.target.value)}>
                <option value="">Every role</option>
                {vocabulary.roles.map((name) => (
                  <option value={name} key={name}>{name}</option>
                ))}
              </select>
            </label>
          </div>
        </div>
      )}

      {data && <StaleNotice stale={data.stale} unavailable={data.unavailable} />}

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the evaluation projection…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The evaluation backlog could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing matches this view</strong>
          <span>
            No record in the projection is in this lane, kind and coverage state. That is a
            statement about the filter, not about what has been reviewed — the coverage inventory
            reports what is still owed.
          </span>
          <Link to="/evaluation/coverage">Open the coverage inventory</Link>
        </div>
      )}

      {items.length > 0 && (
        <ol className="evaluation-list">
          {items.map((item) => (
            <EvaluationRow item={item} key={item.artifact.subject.id} />
          ))}
        </ol>
      )}

      {data && (total > PAGE_SIZE || offset > 0) && (
        <div className="pager card">
          <button type="button" disabled={offset === 0} onClick={() => page(Math.max(0, offset - PAGE_SIZE))}>
            ← Previous
          </button>
          <span className="muted">
            {(offset + 1).toLocaleString()}–{Math.min(offset + items.length, total).toLocaleString()} of{" "}
            {total.toLocaleString()}
            {data.snapshot && (
              <span className="secondary mono" title="The ranked set these pages are cut from. Paging stays inside one ordering.">
                {" "}snapshot {data.snapshot}
              </span>
            )}
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

      {updated && (
        <p className="muted evaluation-freshness">
          Ordering computed from the projection built{" "}
          <span title={updated.absolute}>{updated.relative}</span>. These are this deployment's
          committed evaluation records as this instance has read them.
        </p>
      )}
    </section>
  );
}

// One row. It carries the four things §8.5 requires an item to show — the
// revision it is about, its reception, its coverage and its lifecycle lane —
// and one line of why it sits where it does.
//
// The score is deliberately not rendered as a number. A decimal beside a vote
// count reads as a measurement of the idea; what it actually is is a position
// in one ordering under one policy, which the reasons below it state in words.
function EvaluationRow({ item }: { item: EvaluationItem }) {
  const created = formatTime(item.artifact.created_at);
  // A vote binds to the exact wording that was read. When the record has been
  // revised since, saying so is the whole point: an endorsement of the old
  // wording must not read as an endorsement of the current one.
  const superseded =
    item.artifact.head_id !== "" && item.artifact.head_id !== item.artifact.subject.id;
  return (
    <li className="card evaluation-row" data-item={item.artifact.subject.id}>
      <div className="evaluation-row-head">
        <SubjectLink subject={item.artifact.subject} title={item.artifact.title} />
        <div className="evaluation-row-marks">
          <Badge label={kindLabel(item.artifact.subject.kind)} tone="neutral" />
          <Badge label={laneLabel(item.lane)} tone={laneTone(item.lane)} />
          <Badge label={coverageLabel(item.coverage)} tone={coverageTone(item.coverage)} />
          {item.reconsider && <Badge label="Reconsider" tone="violet" />}
        </div>
      </div>

      <div className="evaluation-row-meta">
        <Reception reception={item.reception} />
        {created && (
          <span className="secondary" title={created.absolute}>revision {created.relative}</span>
        )}
        {superseded && (
          <span className="not-observed" title="Reception below is about this wording, not the newer one.">
            a newer revision exists
          </span>
        )}
        {item.group && (
          <span className="secondary" title="Read together with the other remedies for this problem. Grouping is a reading projection, not a merge.">
            grouped: {item.group}
          </span>
        )}
      </div>

      {item.coverage_reason && (
        <p className="muted untrusted-inline">{item.coverage_reason}</p>
      )}

      {/* One line of why, and only what was recorded. A row with nothing
          recorded says nothing here rather than acquiring a generated
          sentence. */}
      {item.reasons && item.reasons.length > 0 && (
        <p className="evaluation-row-why untrusted-inline">{item.reasons[0]}</p>
      )}
      {item.objections && item.objections.length > 0 && (
        <p className="evaluation-row-objection untrusted-inline">
          <span className="counter-heading">Against: </span>
          {item.objections[0]}
        </p>
      )}
    </li>
  );
}

export default EvaluationPage;
