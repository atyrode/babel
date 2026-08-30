import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityEntity, type EntityDetail, type FactView } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, type Tone } from "../analysis";

function factTone(status: string): Tone {
  switch (status) {
    case "active":
      return "green";
    case "proposed":
    case "stale":
    case "expired":
      return "amber";
    case "disputed":
      return "red";
    default:
      return "neutral"; // superseded and anything newer than this build
  }
}

function RealityEntityPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<EntityDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getRealityEntity(id)
      .then((value) => {
        if (live) setDetail(value);
      })
      .catch((reason) => {
        if (live) setError(errorMessage(reason));
      });
    return () => {
      live = false;
    };
  }, [id]);

  if (error && !detail) {
    return (
      <section className="page">
        <Link className="back-link" to="/reality">← Reality</Link>
        <div className="state-card error-state">
          <strong>Entity could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading entity…</div>
      </section>
    );
  }

  const { entity, aliases, relationships, facts } = detail;
  const merged = entity.canonical_id !== entity.id;

  return (
    <section className="page detail-page entity-page">
      <Link className="back-link" to="/reality">← Reality</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={entity.kind} tone="cyan" />
            {merged && <Badge label="merged away" tone="amber" />}
          </div>
          <h1 className="untrusted-inline entity-name">{entity.display_name}</h1>
          <p className="subtitle mono">{entity.id}</p>
        </div>
      </div>

      {merged && (
        <div className="state-card">
          <strong>This entity was folded into another.</strong>
          <span>
            Its canonical identity is now{" "}
            <Link className="mono" to={`/reality/entities/${encodeURIComponent(entity.canonical_id)}`}>
              {entity.canonical_id}
            </Link>
            . Merges are append-only history, so this record and its facts remain readable.
          </span>
        </div>
      )}

      {entity.notes && (
        <article className="card">
          <p className="eyebrow">Notes</p>
          <p className="untrusted-inline">{entity.notes}</p>
        </article>
      )}

      <article className="card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Identity</p>
            <h2>Aliases</h2>
          </div>
          <span className="count-label">{aliases.length}</span>
        </div>
        {aliases.length === 0 ? (
          <p className="muted">No aliases recorded.</p>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr><th>Kind</th><th>Value</th><th>State</th><th>Recorded</th></tr>
              </thead>
              <tbody>
                {aliases.map((alias) => {
                  const created = formatTime(alias.created_at);
                  return (
                    <tr key={alias.id}>
                      <td>{alias.kind}</td>
                      <td className="mono untrusted-inline alias-value">{alias.value}</td>
                      <td><Badge label={alias.state} tone={alias.state === "asserted" ? "green" : "neutral"} /></td>
                      <td>{created ? <span title={created.absolute}>{created.relative}</span> : "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </article>

      <article className="card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Structure</p>
            <h2>Relationships</h2>
          </div>
          <span className="count-label">{relationships.length}</span>
        </div>
        {relationships.length === 0 ? (
          <p className="muted">No relationships recorded.</p>
        ) : (
          <ul className="link-list">
            {relationships.map((relationship) => (
              <li key={relationship.id}>
                <Badge label={relationship.kind} tone="neutral" />
                <span className="link-target">
                  <EntityRef id={relationship.from.id} name={relationship.from.display_name} current={entity.id} />
                  {" → "}
                  <EntityRef id={relationship.to.id} name={relationship.to.display_name} current={entity.id} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </article>

      <article className="card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Temporal record</p>
            <h2>Facts</h2>
          </div>
          <span className="count-label">{facts.length}</span>
        </div>
        <p className="muted">
          Immutable revisions with explicit authority and freshness. A proposed fact asserts
          nothing yet; a superseded or disputed fact stays readable rather than disappearing.
        </p>
        {facts.length === 0 ? (
          <p className="muted">No facts recorded about this entity.</p>
        ) : (
          <div className="fact-list">
            {facts.map((fact) => <FactRow key={fact.id} fact={fact} />)}
          </div>
        )}
      </article>
    </section>
  );
}

function EntityRef({ id, name, current }: { id: string; name: string; current: string }) {
  if (id === current) return <span className="untrusted-inline">{name}</span>;
  return (
    <Link className="untrusted-inline" to={`/reality/entities/${encodeURIComponent(id)}`}>
      {name}
    </Link>
  );
}

function FactRow({ fact }: { fact: FactView }) {
  const observed = formatTime(fact.observed_at);
  const expires = formatTime(fact.expires_at);
  return (
    <div className={`fact-entry status-${fact.status}`}>
      <div className="fact-heading">
        <Badge label={fact.status} tone={factTone(fact.status)} />
        <span className="fact-predicate mono">{fact.predicate}</span>
        <span className="fact-value untrusted-inline">
          {fact.value.enum ?? fact.value.text ?? (fact.value.object_id ? (
            <Link className="mono" to={`/reality/entities/${encodeURIComponent(fact.value.object_id)}`}>
              {fact.value.object_id}
            </Link>
          ) : "—")}
        </span>
      </div>
      <p className="fact-meta secondary">
        authority {fact.authority.kind}
        {fact.authority.id && <span className="mono"> {fact.authority.id}</span>}
        {" · confidence "}{fact.confidence}
        {observed && <span title={observed.absolute}> · observed {observed.relative}</span>}
        {expires && <span title={expires.absolute}> · freshness expires {expires.relative}</span>}
        {fact.supersedes && <span className="mono"> · supersedes {fact.supersedes}</span>}
      </p>
      {fact.note && <p className="fact-note untrusted-inline">{fact.note}</p>}
    </div>
  );
}

export default RealityEntityPage;
