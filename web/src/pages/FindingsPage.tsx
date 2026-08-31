import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getFindings, type FindingsResponse, type FindingSummary } from "../api";
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
import { FrontierToggle } from "./HypothesesPage";

function FindingsPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<FindingsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [scope, setScope] = useState<HostScope>(LOCAL_SCOPE);
  const fleet = useFleetHosts();

  const load = useCallback((hostScope: HostScope) => {
    setLoading(true);
    setError(null);
    getFindings({ fleet: hostScope.fleet })
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => load(scope), [load, scope]);

  function openFinding(item: FindingSummary) {
    // The detail route reads this machine's durable store, which does not hold
    // another host's record.
    if (item.local_host === false) return;
    navigate(`/findings/${encodeURIComponent(item.id)}`);
  }

  // Narrowed client-side over the merged list.
  const items = (data?.items ?? []).filter((item) => inHostScope(item, scope));

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
          {/* This host's count, kept that way when the fleet block is shown. */}
          {data && <span className="count-label">{data.total} on this host</span>}
          <FrontierToggle />
        </div>
      </div>

      <div className="toolbar card">
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
        <div className="state-card"><span className="spinner" /> Reading findings…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>Findings could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load(scope)}>Try again</button>
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
                  <th>Host</th>
                  <th>Sync</th>
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
                      className={syncRowClass(item)}
                      tabIndex={item.local_host === false ? undefined : 0}
                      role={item.local_host === false ? undefined : "link"}
                      onClick={() => openFinding(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openFinding(item);
                      }}
                    >
                      <td className="statement-cell">
                        <strong className="untrusted-inline">{item.title}</strong>
                        <span className="secondary mono">{item.id}</span>
                        <UnopenedNote reason={item.unopened} />
                      </td>
                      {/* The review status and the evidence counts are the
                          owning host's derivations, so a row from elsewhere
                          reports an absence rather than a zero. */}
                      <td>
                        {item.local_host === false
                          ? <span className="muted">—</span>
                          : <Badge label={item.review_status} tone={reviewTone(item.review_status)} />}
                      </td>
                      <td><HostLabel mark={item} /></td>
                      <td><SyncBadge sync={item.sync} /></td>
                      <td className="numeric mono">
                        {item.local_host === false ? <span className="muted">—</span> : item.observations}
                      </td>
                      <td className="numeric mono">
                        {item.local_host === false ? <span className="muted">—</span> : item.hypotheses}
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

export default FindingsPage;
