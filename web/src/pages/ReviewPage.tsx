import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getReviewQueue, type QueueItem, type ReviewQueueResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import {
  Badge,
  FleetNotice,
  HostChips,
  HostLabel,
  LOCAL_SCOPE,
  SyncBadge,
  SyncDegradedNotice,
  UnopenedNote,
  inHostScope,
  reviewTone,
  syncRowClass,
  useFleetHosts,
  type HostScope,
} from "../analysis";
import { CitationCount } from "../references";
import { SteeringSection } from "../steering";

const TYPES = ["hypothesis", "finding", "proposal"];
const STATUSES = ["accepted", "rejected", "deferred", "duplicate", "refine-requested"];

function ReviewPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<ReviewQueueResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [type, setType] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  // The host scope is the fleet chip row's selection, separate from the type
  // and status filters because it narrows a different thing: which machine
  // produced the record rather than what the record is or where it stands.
  const [scope, setScope] = useState<HostScope>(LOCAL_SCOPE);
  const fleet = useFleetHosts();

  const load = useCallback(
    (typeFilter: string | null, statusFilter: string | null, hostScope: HostScope) => {
      setLoading(true);
      setError(null);
      getReviewQueue({
        type: typeFilter ?? undefined,
        status: statusFilter ?? undefined,
        fleet: hostScope.fleet,
      })
        .then((value) => setData(value))
        .catch((reason) => setError(errorMessage(reason)))
        .finally(() => setLoading(false));
    },
    [],
  );

  useEffect(() => load(type, status, scope), [load, type, status, scope]);

  function openItem(item: QueueItem) {
    // A decision is recorded against a record in this machine's durable store,
    // and another host's record is not in it. Such a row is not a link: a
    // control that led nowhere -- or worse, invited a decision this machine
    // cannot append -- would be this page claiming an authority it lacks.
    if (item.local_host === false) return;
    navigate(`/review/${encodeURIComponent(item.subject.type)}/${encodeURIComponent(item.subject.id)}`);
  }

  // Narrowed client-side over the merged list, so selecting a host hides rows
  // the browser already holds rather than costing a round trip.
  const items = (data?.items ?? []).filter((item) => inHostScope(item, scope));

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

      {/* #115's capture box rides the review surface by operator decision
          (2026-08-31), and it sits above the queue rather than below it: the
          operator who came to review is the operator with something to say,
          and the box must not be buried under the rows it steers. */}
      <SteeringSection />

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
        <HostChips
          hosts={fleet.hosts}
          scope={scope}
          localHost={fleet.localHost}
          onSelect={setScope}
        />
      </div>

      {fleet.configured === false && <FleetNotice />}
      {data?.sync_degraded && <SyncDegradedNotice detail={data.sync_detail} />}

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the review queue…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The review queue could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load(type, status, scope)}>Try again</button>
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
                  <th>Host</th>
                  <th>Sync</th>
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
                      className={syncRowClass(item)}
                      tabIndex={item.local_host === false ? undefined : 0}
                      role={item.local_host === false ? undefined : "link"}
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
                        ) : item.local_host === false ? (
                          <span className="muted no-summary">
                            no summary for this {item.subject.type} on this machine
                          </span>
                        ) : (
                          <strong className="untrusted-inline">Untitled record</strong>
                        )}
                        <span className="secondary mono">{item.subject.id}</span>
                        <UnopenedNote reason={item.unopened} />
                        {/* #113's compact form of the record's citations: how
                            many typed references leave it and arrive at it,
                            which is what makes an isolated candidate
                            distinguishable from one four observations rest on
                            before it is opened. Absent for a record nobody
                            counted, never rendered as a zero. */}
                        <CitationCount citations={item.citations} />
                      </td>
                      <td><Badge label={item.subject.type} tone="neutral" /></td>
                      {/* The review status, the decision count, the last
                          decision and the refinement count are derived from the
                          owning host's append-only history. This machine holds
                          none of it for another host's record, so it says
                          nothing rather than reporting a decided-nothing. */}
                      <td>
                        {item.local_host === false
                          ? <span className="muted">—</span>
                          : <Badge label={item.status} tone={reviewTone(item.status)} />}
                      </td>
                      <td><HostLabel mark={item} /></td>
                      <td><SyncBadge sync={item.sync} /></td>
                      <td className="numeric mono">
                        {item.local_host === false ? <span className="muted">—</span> : item.decisions}
                      </td>
                      <td>
                        {item.local_host === false ? (
                          <span className="muted">—</span>
                        ) : lastDecided ? (
                          <span title={lastDecided.absolute}>{lastDecided.relative}</span>
                        ) : (
                          <span className="muted">never</span>
                        )}
                      </td>
                      <td className="numeric mono">
                        {item.local_host === false ? <span className="muted">—</span> : item.refinements}
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
