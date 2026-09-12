import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityEntity, type EntityDetail } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, TimelineEntry } from "../analysis";
import { EntityName, FactEntry } from "../reality";

// One subject's current reality: what it is called, what it is attached to,
// what Babel believes about it, and how its identity has been resolved.
//
// The page shows every fact status, superseded revisions and proposals
// included, because reviewing what was proposed is a real need and a chain that
// showed only its head would hide how reality was corrected. Each revision
// links to its own page, which is where the chain is readable end to end.
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
        <Link className="back-link" to="/ask/entities">← Subjects</Link>
        <div className="surface state-note error-state">
          <strong>Entity could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="surface state-note"><span className="spinner" /> Loading entity…</div>
      </section>
    );
  }

  const { entity, aliases, relationships, facts, resolutions } = detail;
  const merged = entity.canonical_id !== entity.id;

  return (
    <section className="page detail-page entity-page">
      <Link className="back-link" to="/ask/entities">← Subjects</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={entity.kind} tone="cyan" />
            {merged && <Badge label="merged away" tone="amber" />}
          </div>
          <h1 className="untrusted-inline entity-name">{entity.display_name}</h1>
          <p className="subtitle mono">{entity.id}</p>
        </div>
        {/* The way to "stop spending on this" from the record it is about.
            The subject travels as the entity id rather than as its display
            name: an id is an identifier this build minted, while a name is
            operator vocabulary that may mean two things, and the focus page
            has to be able to report that ambiguity rather than be handed a
            guess. */}
        <div className="heading-meta">
          <Link className="secondary-button" to={`/settings?section=ceilings&subject=${encodeURIComponent(entity.id)}`}>
            Analysis policy
          </Link>
        </div>
      </div>

      {merged && (
        <div className="surface state-note">
          <strong>This entity was folded into another.</strong>
          <span>
            Its canonical identity is now{" "}
            <Link className="mono" to={`/ask/entities/${encodeURIComponent(entity.canonical_id)}`}>
              {entity.canonical_id}
            </Link>
            . Merges are append-only history, so this record and its facts remain readable.
          </span>
        </div>
      )}

      {entity.notes && (
        <article className="surface">
          <p className="eyebrow">Notes</p>
          <p className="untrusted-inline">{entity.notes}</p>
        </article>
      )}

      <article className="surface">
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

      <article className="surface">
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
                  <EntityName entity={relationship.from} current={entity.id} />
                  {" → "}
                  <EntityName entity={relationship.to} current={entity.id} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </article>

      <article className="surface">
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
          Each one opens its own page, where what it replaced and what replaced it are shown.
        </p>
        {facts.length === 0 ? (
          <p className="muted">
            Babel believes nothing about this subject yet. Facts arrive when a question about it
            is answered and the interpretation accepted.
          </p>
        ) : (
          <div className="fact-list">
            {facts.map((fact) => <FactEntry key={fact.id} fact={fact} />)}
          </div>
        )}
      </article>

      {/* §8.2 names alias merge/split history as part of what Reality shows,
          and §4.8 keeps a mistaken resolution reversible — which is only worth
          something if the operator can see that a merge happened, who decided
          it, and what reason they gave. */}
      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only</p>
            <h2>Identity history</h2>
          </div>
          <span className="count-label">{resolutions.length}</span>
        </div>
        {resolutions.length === 0 ? (
          <p className="muted">
            This identity has never been merged or split. It has meant one thing since it was
            recognized.
          </p>
        ) : (
          <ol className="timeline">
            {resolutions.map((resolution) => (
              <TimelineEntry
                key={resolution.id}
                badge={resolution.kind}
                tone={resolution.kind === "undo" ? "amber" : "violet"}
                at={resolution.recorded_at}
              >
                <span className="secondary">decided by {resolution.actor}</span>
                {resolution.reason && <span className="untrusted-inline">{resolution.reason}</span>}
                <span className="secondary">
                  {resolution.sources.map((source, index) => (
                    <span key={source.id}>
                      {index > 0 && ", "}
                      <EntityName entity={source} current={entity.id} />
                    </span>
                  ))}
                  {resolution.results.length > 0 && " → "}
                  {resolution.results.map((result, index) => (
                    <span key={result.id}>
                      {index > 0 && ", "}
                      <EntityName entity={result} current={entity.id} />
                    </span>
                  ))}
                </span>
              </TimelineEntry>
            ))}
          </ol>
        )}
      </article>
    </section>
  );
}

export default RealityEntityPage;
