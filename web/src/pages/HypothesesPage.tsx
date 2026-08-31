import { useCallback, useEffect, useState } from "react";
import { NavLink, useNavigate } from "react-router-dom";
import { getHypotheses, type HypothesesResponse, type HypothesisSummary } from "../api";
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
  statusTone,
  syncRowClass,
  useFleetHosts,
  type HostScope,
} from "../analysis";

const STATUSES = ["untriaged", "queued", "investigating", "deferred", "rejected", "promoted"];

// FrontierToggle links the two frontier lists. It is shared chrome for the
// Hypotheses area: candidates and their consolidations are one frontier, not
// two applications.
export function FrontierToggle() {
  return (
    <nav className="view-toggle" aria-label="Frontier views">
      <NavLink to="/hypotheses" end className={({ isActive }) => (isActive ? "chip active" : "chip")}>
        Hypotheses
      </NavLink>
      <NavLink to="/findings" end className={({ isActive }) => (isActive ? "chip active" : "chip")}>
        Findings
      </NavLink>
    </nav>
  );
}

function HypothesesPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<HypothesesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  // The host scope is the fleet chip row's selection. It is separate from the
  // status filter because the two narrow different things: a status is a fact
  // about a candidate, a host is a fact about which machine produced it.
  const [scope, setScope] = useState<HostScope>(LOCAL_SCOPE);
  const fleet = useFleetHosts();

  const load = useCallback((filter: string | null, hostScope: HostScope) => {
    setLoading(true);
    setError(null);
    getHypotheses({ ...(filter ? { status: filter } : {}), fleet: hostScope.fleet })
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => load(status, scope), [load, status, scope]);

  function openHypothesis(item: HypothesisSummary) {
    // Only this machine's candidates open: the detail route reads the local
    // durable store, and another host's record is not in it. A row that
    // navigated to a 404 would be this page offering a control that cannot
    // work.
    if (item.local_host === false) return;
    navigate(`/hypotheses/${encodeURIComponent(item.id)}`);
  }

  // Narrowed client-side over the merged list, so selecting a host hides rows
  // the browser already holds rather than costing a round trip.
  const items = (data?.items ?? []).filter((item) => inHostScope(item, scope));

  return (
    <section className="page hypotheses-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Hypothesis frontier</p>
          <h1>Hypotheses</h1>
          <p className="subtitle">
            Every candidate idea, in the model's own wording. Nothing leaves the frontier:
            rejected and deferred candidates stay listed, visibly so.
          </p>
        </div>
        <div className="heading-meta">
          {/* The count is this host's frontier, and stays that way when the
              fleet block is shown: "the frontier" is this machine's, and the
              other hosts' candidates are stated separately rather than folded
              into a number that would then mean two things. */}
          {data && <span className="count-label">{data.total} in this host's frontier</span>}
          {scope.fleet && (
            <span className="count-label fleet-count">
              {items.length - items.filter((item) => item.local_host !== false).length} from other hosts
            </span>
          )}
          <FrontierToggle />
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
        <div className="state-card"><span className="spinner" /> Reading the frontier…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>Hypotheses could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={() => load(status, scope)}>Try again</button>
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
                  <th>Host</th>
                  <th>Sync</th>
                  <th className="numeric">Observations</th>
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
                      onClick={() => openHypothesis(item)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") openHypothesis(item);
                      }}
                    >
                      <td className="statement-cell">
                        <strong className="untrusted-inline">{item.statement}</strong>
                        <span className="secondary mono">{item.id}</span>
                        <UnopenedNote reason={item.unopened} />
                        {item.provisional_labels && item.provisional_labels.length > 0 && (
                          <span className="tag-list">
                            {item.provisional_labels.map((label) => (
                              <span className="tag" key={label}>{label}</span>
                            ))}
                          </span>
                        )}
                      </td>
                      <td><Badge label={item.status} tone={statusTone(item.status)} /></td>
                      <td><HostLabel mark={item} /></td>
                      <td><SyncBadge sync={item.sync} /></td>
                      {/* A candidate another host committed carries no
                          observation count: the evidence is in that host's
                          durable store, and a zero would say it rests on
                          none. */}
                      <td className="numeric mono">
                        {item.local_host === false ? <span className="muted">—</span> : item.observations}
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
