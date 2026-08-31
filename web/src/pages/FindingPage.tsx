import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getFinding, type FindingDetail, type Proposal } from "../api";
import { errorMessage, formatTime } from "../format";
import {
  Badge,
  CounterEvidence,
  EvidenceItems,
  FallibilityNote,
  Quoted,
  reviewTone,
} from "../analysis";
import { ObservationCard } from "./HypothesisPage";
import { RecordActions } from "../records";

function FindingPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<FindingDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getFinding(id)
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
        <Link className="back-link" to="/findings">← Findings</Link>
        <div className="state-card error-state">
          <strong>Finding could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading finding…</div>
      </section>
    );
  }

  const { finding, observations, proposals } = detail;
  const payload = finding.payload;
  const created = formatTime(finding.created_at);

  return (
    <section className="page detail-page frontier-detail">
      <Link className="back-link" to="/findings">← Findings</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label="finding" tone="cyan" />
          </div>
          <h1>Finding</h1>
          <p className="subtitle mono">{finding.id}</p>
        </div>
      </div>

      <article className="card statement-card">
        <Quoted
          label="Consolidated pattern, in the model's wording — untrusted"
          text={`${payload.title}\n\n${payload.pattern}`}
        />
        <FallibilityNote />
        <dl className="metadata-list compact">
          {payload.significance && (
            <div>
              <dt>Why it matters</dt>
              <dd className="untrusted-inline">{payload.significance}</dd>
            </div>
          )}
          {payload.scope && payload.scope.length > 0 && (
            <div>
              <dt>Affected scope</dt>
              <dd className="untrusted-inline">{payload.scope.join(" · ")}</dd>
            </div>
          )}
          <div>
            <dt>Recurrence</dt>
            <dd>
              {payload.recurrence
                ? `${payload.recurrence} occurrences`
                : "not applicable to this finding"}
            </dd>
          </div>
          {payload.temporal_status && (
            <div>
              <dt>Temporal status</dt>
              <dd>{payload.temporal_status}</dd>
            </div>
          )}
          <div>
            <dt>Consolidating run</dt>
            <dd className="mono">{finding.run_id}</dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{created ? `${created.relative} · ${created.absolute}` : finding.created_at}</dd>
          </div>
          <div>
            <dt>Develops</dt>
            <dd>
              {finding.hypothesis_ids.map((hid, index) => (
                <span key={hid}>
                  {index > 0 && " · "}
                  <Link className="mono" to={`/hypotheses/${encodeURIComponent(hid)}`}>{hid}</Link>
                </span>
              ))}
            </dd>
          </div>
        </dl>
        <CounterEvidence items={payload.counter_evidence} absent={payload.counter_evidence_absent} />
      </article>

      <div className="detail-grid">
        <div className="detail-main">
          <article className="card observations-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Supporting claims</p>
                <h2>Observations</h2>
              </div>
              <span className="count-label">{observations.length}</span>
            </div>
            <p className="muted">
              A finding is only its observations, consolidated. Each keeps its own evidence
              locators and counter-evidence.
            </p>
            <div className="observation-list">
              {observations.map((observation) => (
                <ObservationCard key={observation.id} observation={observation} />
              ))}
            </div>
          </article>
          <RecordActions record={{ type: "finding", id: finding.id }} />
        </div>

        <aside className="detail-side">
          <article className="card proposals-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Suggested improvements</p>
                <h2>Proposals</h2>
              </div>
              <span className="count-label">{proposals.length}</span>
            </div>
            {proposals.length === 0 ? (
              <p className="muted">No proposals were generated from this finding.</p>
            ) : (
              <div className="proposal-list">
                {proposals.map((proposal) => (
                  <ProposalCard key={proposal.id} proposal={proposal} />
                ))}
              </div>
            )}
          </article>

          <article className="card review-shortcut-card">
            <p className="eyebrow">Review</p>
            <h2>Decide on this record</h2>
            <p className="muted">
              Dispositions are append-only events recorded beside the finding — deciding never
              edits or deletes it.
            </p>
            <Link className="review-link" to={`/review/finding/${encodeURIComponent(finding.id)}`}>
              Open review history →
            </Link>
          </article>
        </aside>
      </div>
    </section>
  );
}

function ProposalCard({ proposal }: { proposal: Proposal }) {
  const payload = proposal.payload;
  return (
    <article className="proposal-entry">
      <div className="observation-heading">
        <Badge label={proposal.review_status} tone={reviewTone(proposal.review_status)} />
        <span className="mono event-index">{proposal.id}</span>
      </div>
      <Quoted
        label="Proposal — a suggestion for review, with no external effect"
        text={`${payload.title}\n\nProblem: ${payload.problem}\n\nProposed outcome: ${payload.outcome}`}
      />
      {payload.uncertainty && (
        <p className="uncertainty-note">
          <strong>Uncertainty:</strong> <span className="untrusted-inline">{payload.uncertainty}</span>
        </p>
      )}
      <p className="grading-line">
        <span>impact <strong>{payload.impact}</strong></span>
        {payload.estimated_scope && <span>scope <strong>{payload.estimated_scope}</strong></span>}
        <span>classification <strong>{payload.classification}</strong></span>
        <span className="muted">model-graded, not verified</span>
      </p>
      {payload.supporting && payload.supporting.length > 0 && (
        <div className="evidence-block">
          <h4 className="evidence-heading">Supporting material</h4>
          <EvidenceItems items={payload.supporting} kind="supporting" />
        </div>
      )}
      {payload.conflicting && payload.conflicting.length > 0 && (
        <div className="counter-evidence">
          <h4 className="counter-heading">Conflicting material</h4>
          <EvidenceItems items={payload.conflicting} kind="counter" />
        </div>
      )}
      {payload.targets && payload.targets.length > 0 && (
        <div className="target-list">
          <h4 className="evidence-heading">Suggested targets — suggestions, never facts</h4>
          <ul>
            {payload.targets.map((target) => (
              <li key={target.system}>
                <span className="mono untrusted-inline">{target.system}</span>
                <span className="muted"> · confidence {target.confidence}</span>
                {target.rationale && <span className="untrusted-inline"> — {target.rationale}</span>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {(payload.risks?.length || payload.open_questions?.length) ? (
        <details className="json-disclosure">
          <summary>Risks and open questions</summary>
          <pre>
            {[
              ...(payload.risks ?? []).map((risk) => `Risk: ${risk}`),
              ...(payload.open_questions ?? []).map((question) => `Open question: ${question}`),
            ].join("\n")}
          </pre>
        </details>
      ) : null}
      <p className="observation-provenance secondary">
        <Link className="review-link" to={`/review/proposal/${encodeURIComponent(proposal.id)}`}>
          Review this proposal →
        </Link>
      </p>
    </article>
  );
}

export default FindingPage;
