import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getHypothesis, type HypothesisDetail, type Observation } from "../api";
import { errorMessage, formatTime } from "../format";
import { RecordActions } from "../records";
import {
  Badge,
  CounterEvidence,
  EvidenceItems,
  FallibilityNote,
  GradingLine,
  Quoted,
  statusTone,
  TimelineEntry,
} from "../analysis";
import { RecordLinks } from "../references";

function HypothesisPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<HypothesisDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getHypothesis(id)
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
        <Link className="back-link" to="/hypotheses">← Hypotheses</Link>
        <div className="state-card error-state">
          <strong>Hypothesis could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading hypothesis…</div>
      </section>
    );
  }

  const { hypothesis, statusHistory, observations, links, lineage } = detail;
  const payload = hypothesis.payload;
  const created = formatTime(hypothesis.created_at);
  const lineageEdges = [...lineage.ancestors, ...lineage.descendants];

  return (
    <section className="page detail-page frontier-detail">
      <Link className="back-link" to="/hypotheses">← Hypotheses</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={hypothesis.status} tone={statusTone(hypothesis.status)} />
            {hypothesis.status === "rejected" && (
              <span className="muted">rejected, and kept — nothing leaves the frontier</span>
            )}
          </div>
          <h1>Hypothesis</h1>
          <p className="subtitle mono">{hypothesis.id}</p>
        </div>
      </div>

      <article className="card statement-card">
        <Quoted label="Candidate, in the model's own wording — untrusted" text={payload.statement} />
        <FallibilityNote />
        <dl className="metadata-list compact">
          <div>
            <dt>Generating run</dt>
            <dd className="mono">{hypothesis.run_id || <span className="muted">—</span>}</dd>
          </div>
          {hypothesis.ancestor_id && (
            <div>
              <dt>Revises</dt>
              <dd className="mono">
                <Link to={`/hypotheses/${encodeURIComponent(hypothesis.ancestor_id)}`}>
                  {hypothesis.ancestor_id}
                </Link>
              </dd>
            </div>
          )}
          <div>
            <dt>Created</dt>
            <dd>{created ? `${created.relative} · ${created.absolute}` : hypothesis.created_at}</dd>
          </div>
          {payload.origin_cues && payload.origin_cues.length > 0 && (
            <div>
              <dt>Origin cues</dt>
              <dd className="untrusted-inline">{payload.origin_cues.join(" · ")}</dd>
            </div>
          )}
          {payload.provisional_labels && payload.provisional_labels.length > 0 && (
            <div>
              <dt>Provisional labels</dt>
              <dd>
                <span className="tag-list">
                  {payload.provisional_labels.map((label) => <span className="tag" key={label}>{label}</span>)}
                </span>
              </dd>
            </div>
          )}
          <div>
            <dt>Sorting signals</dt>
            <dd>
              novelty {payload.novelty.toFixed(2)} · priority {payload.priority.toFixed(2)}
              <span className="muted"> — ordering estimates only, never evidence</span>
            </dd>
          </div>
          {payload.notes && (
            <div>
              <dt>Investigator notes</dt>
              <dd className="untrusted-inline">{payload.notes}</dd>
            </div>
          )}
        </dl>
      </article>

      <div className="detail-grid">
        <div className="detail-main">
          <article className="card observations-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Development</p>
                <h2>Observations</h2>
              </div>
              <span className="count-label">{observations.length}</span>
            </div>
            {observations.length === 0 ? (
              <p className="muted">
                No observations yet. A candidate develops only through locator-backed
                observations; until then it is an idea, not a claim.
              </p>
            ) : (
              <div className="observation-list">
                {observations.map((observation) => (
                  <ObservationCard key={observation.id} observation={observation} />
                ))}
              </div>
            )}
          </article>
          <RecordActions
            record={{ type: "hypothesis", id: hypothesis.id }}
            lifecycle={hypothesis.status}
          />
        </div>

        <aside className="detail-side">
          <article className="card history-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Append-only</p>
                <h2>Status history</h2>
              </div>
            </div>
            <p className="muted">
              Every state this candidate has held, oldest first. History is appended, never
              rewritten.
            </p>
            <ol className="timeline">
              {statusHistory.map((event) => (
                <TimelineEntry
                  key={event.id}
                  badge={event.status}
                  tone={statusTone(event.status)}
                  at={event.recorded_at}
                >
                  {/* A transition belongs either to a run or to an operator,
                      and #87 made the second possible: an operator's revive
                      borrows no run identity, so the author is rendered from
                      the actor rather than from the run column. */}
                  {event.actor.kind === "operator" ? (
                    <span className="secondary">operator <span className="mono">{event.actor.id}</span></span>
                  ) : (
                    event.run_id && <span className="mono secondary">{event.run_id}</span>
                  )}
                  {event.note && <span className="untrusted-inline">{event.note}</span>}
                </TimelineEntry>
              ))}
            </ol>
          </article>

          <article className="card links-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Typed links</p>
                <h2>Related candidates</h2>
              </div>
            </div>
            {links.length === 0 ? (
              <p className="muted">No recorded relationships.</p>
            ) : (
              <ul className="link-list">
                {links.map((link) => {
                  const otherID = link.from_id === hypothesis.id ? link.to_id : link.from_id;
                  const direction = link.from_id === hypothesis.id ? "→" : "←";
                  return (
                    <li key={link.id}>
                      <Badge
                        label={`${link.type} ${direction}`}
                        tone={link.type === "contradicts" ? "red" : link.type === "corroborates" ? "green" : "neutral"}
                      />
                      <Link to={`/hypotheses/${encodeURIComponent(otherID)}`} className="link-target">
                        {link.other_statement
                          ? <span className="untrusted-inline">{link.other_statement}</span>
                          : <span className="mono">{otherID}</span>}
                      </Link>
                    </li>
                  );
                })}
              </ul>
            )}
          </article>

          {/* #113's citation section, beside #87's lineage rather than merged
              into it: lineage is the refinement chain internal/review derives,
              and these are append-only typed edges any record may assert about
              any other. One panel showing both would flatten two different
              claims into one list. */}
          <RecordLinks record={{ type: "hypothesis", id: hypothesis.id }} />

          <article className="card lineage-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Lineage</p>
                <h2>Refinement history</h2>
              </div>
            </div>
            {lineageEdges.length === 0 ? (
              <p className="muted">No ancestors or descendants recorded.</p>
            ) : (
              <ul className="link-list">
                {lineage.ancestors.map((edge) => (
                  <li key={edge.id}>
                    <Badge label={`${edge.relation} ↑${edge.generation}`} tone="neutral" />
                    <LineageLink kind={edge.to.kind} id={edge.to.id} />
                  </li>
                ))}
                {lineage.descendants.map((edge) => (
                  <li key={edge.id}>
                    <Badge label={`${edge.relation} ↓${edge.generation}`} tone="neutral" />
                    <LineageLink kind={edge.from.kind} id={edge.from.id} />
                  </li>
                ))}
              </ul>
            )}
          </article>

          <article className="card review-shortcut-card">
            <p className="eyebrow">Review</p>
            <h2>Decide on this record</h2>
            <p className="muted">
              Dispositions are append-only events recorded beside the candidate — deciding
              never edits or deletes it.
            </p>
            <Link className="review-link" to={`/review/hypothesis/${encodeURIComponent(hypothesis.id)}`}>
              Open review history →
            </Link>
          </article>
        </aside>
      </div>
    </section>
  );
}

// LineageLink routes a lineage node to the page that renders it; kinds
// without a page of their own stay as identified text rather than dead links.
function LineageLink({ kind, id }: { kind: string; id: string }) {
  if (kind === "hypothesis") {
    return <Link className="link-target mono" to={`/hypotheses/${encodeURIComponent(id)}`}>{id}</Link>;
  }
  if (kind === "finding") {
    return <Link className="link-target mono" to={`/findings/${encodeURIComponent(id)}`}>{id}</Link>;
  }
  return <span className="link-target mono">{kind} {id}</span>;
}

export function ObservationCard({ observation }: { observation: Observation }) {
  const payload = observation.payload;
  const created = formatTime(observation.created_at);
  return (
    <article className="observation-entry">
      <div className="observation-heading">
        <span className="kind-label">observation</span>
        <span className="mono event-index">{observation.id}</span>
        {payload.category && <Badge label={payload.category} tone="neutral" />}
        {created && (
          <time dateTime={observation.created_at} title={created.absolute}>{created.relative}</time>
        )}
      </div>
      <Quoted label="Model claim — untrusted" text={payload.claim} />
      <GradingLine
        confidence={payload.confidence}
        impact={payload.impact}
        temporal={payload.temporal_status}
      />
      <div className="evidence-block">
        <h4 className="evidence-heading">Evidence ({observation.evidence_count})</h4>
        <EvidenceItems items={payload.evidence} kind="supporting" />
      </div>
      <CounterEvidence items={payload.counter_evidence} absent={payload.counter_evidence_absent} />
      <p className="observation-provenance secondary">
        recipe <span className="mono">{observation.recipe_id || "ad hoc"}</span>
        {observation.recipe_version > 0 && <span className="mono"> v{observation.recipe_version}</span>}
        {" · run "}
        <span className="mono">{observation.run_id}</span>
      </p>
    </article>
  );
}

export default HypothesisPage;
