import { useEffect, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { getProposal, type ProposalDetail } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, EvidenceItems, FallibilityNote, Quoted, reviewTone } from "../analysis";
import { RecordActions } from "../records";
import { RecordLinks } from "../references";
import { TriageAdvice } from "../triage";

// One proposal, whole.
//
// The record's own field names are not what a reader is asked to decode:
// `verification_criteria` is "how you would know it worked", and someone
// deciding whether to act on a suggestion needs the question, not the schema.
// Every block below renders only when the record holds one — a proposal that
// names no prerequisite is not a proposal with an empty prerequisite list, and
// an empty section would read as an unasked question.

function Points({
  label,
  note,
  items,
}: {
  label: string;
  note?: string;
  items: string[] | undefined;
}) {
  if (!items || items.length === 0) return null;
  return (
    <div className="proposal-points">
      <h3>{label}</h3>
      {note && <p className="muted">{note}</p>}
      <ul>
        {items.map((item, index) => (
          <li className="untrusted-inline" key={`${index}-${item}`}>{item}</li>
        ))}
      </ul>
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className="untrusted-inline">{children}</dd>
    </div>
  );
}

function ProposalPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<ProposalDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getProposal(id)
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
        <Link className="back-link" to="/proposals">← Proposals</Link>
        <div className="state-card error-state">
          <strong>Proposal could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading proposal…</div>
      </section>
    );
  }

  const payload = detail.payload;
  const created = formatTime(detail.created_at);
  const empty =
    !payload.prerequisites?.length &&
    !payload.verification_criteria?.length &&
    !payload.risks?.length &&
    !payload.open_questions?.length;

  return (
    <section className="page detail-page frontier-detail">
      <Link className="back-link" to="/proposals">← Proposals</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label="proposal" tone="violet" />
            {/* Consolidated or candidate: a remedy resting on a finding and
                one resting only on the claim it addresses are different
                claims, and the badge is the only thing that says which. */}
            {detail.form && <Badge label={detail.form} tone="neutral" />}
            {detail.review_status && (
              <Badge label={detail.review_status} tone={reviewTone(detail.review_status)} />
            )}
          </div>
          <h1 className="untrusted-inline">{payload.title || detail.title || "Untitled proposal"}</h1>
          <p className="subtitle mono">{detail.id}</p>
        </div>
      </div>

      <article className="card statement-card">
        <Quoted
          label="Proposal — a suggestion for review, with no external effect"
          text={`The problem:\n${payload.problem}\n\nProposed outcome:\n${payload.outcome}`}
        />
        <FallibilityNote />
        <p className="grading-line">
          <span>impact <strong>{payload.impact || "not graded"}</strong></span>
          {payload.estimated_scope && <span>scope <strong>{payload.estimated_scope}</strong></span>}
          <span>kind <strong>{payload.classification || "unclassified"}</strong></span>
          <span className="muted">model-graded, not verified</span>
        </p>
        {payload.uncertainty && (
          <p className="uncertainty-note">
            <strong>What the model is unsure of:</strong>{" "}
            <span className="untrusted-inline">{payload.uncertainty}</span>
          </p>
        )}
        <dl className="metadata-list compact">
          {payload.applicability && <Fact label="Where it applies">{payload.applicability}</Fact>}
          {payload.temporal_status && (
            <Fact label="Still current?">{payload.temporal_status}</Fact>
          )}
          {payload.destinations && payload.destinations.length > 0 && (
            <Fact label="Suggested destinations">{payload.destinations.join(" · ")}</Fact>
          )}
          {(detail.finding_ids ?? []).length > 0 && (
            <div>
              <dt>Addresses</dt>
              <dd>
                {(detail.finding_ids ?? []).map((fid, index) => (
                  <span key={fid}>
                    {index > 0 && " · "}
                    <Link className="mono" to={`/findings/${encodeURIComponent(fid)}`}>{fid}</Link>
                  </span>
                ))}
              </dd>
            </div>
          )}
          {(detail.hypothesis_ids ?? []).length > 0 && (
            <div>
              <dt>Rests on</dt>
              <dd>
                {(detail.hypothesis_ids ?? []).map((hid, index) => (
                  <span key={hid}>
                    {index > 0 && " · "}
                    <Link className="mono" to={`/hypotheses/${encodeURIComponent(hid)}`}>{hid}</Link>
                  </span>
                ))}
              </dd>
            </div>
          )}
          <div>
            <dt>Proposing run</dt>
            <dd className="mono">{detail.run_id}</dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{created ? `${created.relative} · ${created.absolute}` : detail.created_at}</dd>
          </div>
        </dl>
      </article>

      <div className="detail-grid">
        <div className="detail-main">
          <article className="card proposal-detail-card">
            <Points
              label="Before this can be done"
              note="What the proposal says has to be true first."
              items={payload.prerequisites}
            />
            <Points
              label="How you would know it worked"
              note="The proposal's own test. Babel checks none of it."
              items={payload.verification_criteria}
            />
            <Points label="What could go wrong" items={payload.risks} />
            <Points
              label="Still unanswered"
              note="Questions the proposal leaves open. Answering them is a human's work."
              items={payload.open_questions}
            />
            {empty && (
              <p className="muted">
                This proposal records no prerequisites, verification criteria, risks or open
                questions. Its whole claim is the problem and the outcome above.
              </p>
            )}
            {payload.targets && payload.targets.length > 0 && (
              <div className="target-list">
                <h3 className="evidence-heading">Suggested targets — suggestions, never facts</h3>
                <ul>
                  {payload.targets.map((target) => (
                    <li key={target.system}>
                      <span className="mono untrusted-inline">{target.system}</span>
                      <span className="muted"> · confidence {target.confidence}</span>
                      {target.rationale && (
                        <span className="untrusted-inline"> — {target.rationale}</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {payload.supporting && payload.supporting.length > 0 && (
              <div className="evidence-block">
                <h3 className="evidence-heading">Supporting material</h3>
                <EvidenceItems items={payload.supporting} kind="supporting" />
              </div>
            )}
            {payload.conflicting && payload.conflicting.length > 0 && (
              <div className="counter-evidence">
                <h3 className="counter-heading">Conflicting material</h3>
                <EvidenceItems items={payload.conflicting} kind="counter" />
              </div>
            )}
          </article>
          <TriageAdvice advice={detail.triage} subject={detail.id} />
          <RecordActions record={{ type: "proposal", id: detail.id }} />
        </div>

        <aside className="detail-side">
          <RecordLinks record={{ type: "proposal", id: detail.id }} />

          <article className="card review-shortcut-card">
            <p className="eyebrow">Review</p>
            <h2>Decide on this record</h2>
            <p className="muted">
              Dispositions are append-only events recorded beside the proposal — deciding never
              edits or deletes it, and Babel performs nothing it suggests.
            </p>
            <Link className="review-link" to={`/review/proposal/${encodeURIComponent(detail.id)}`}>
              Open review history →
            </Link>
          </article>
        </aside>
      </div>
    </section>
  );
}

export default ProposalPage;
