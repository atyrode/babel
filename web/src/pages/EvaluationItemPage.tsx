import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  getEvaluationDetail,
  recordEvaluationOperator,
  type EvaluationArtifact,
  type EvaluationAssessment,
  type EvaluationAttempt,
  type EvaluationContext,
  type EvaluationDetailResponse,
  type EvaluationItem,
  type EvaluationRecord,
  type EvaluationSubject,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, EvidenceItems, FallibilityNote, Quoted, unescapeWhitespace } from "../analysis";
import {
  artifactRoute,
  coverageLabel,
  coverageTone,
  kindLabel,
  laneLabel,
  laneTone,
  Reception,
  reconsiderDecisionAction,
  reconsiderDecisionBasis,
  reconsiderDecisionLabel,
  reconsiderDecisionMeans,
  reconsiderDecisionTone,
  recordKindLabel,
  recordKindTone,
  RoleCoverageTable,
  SubjectLink,
  WhyHere,
} from "../evaluation";

// One record's whole evaluation (SPEC.md §8.5): the revision that was read,
// what was said about it, what is still owed, the alternatives it is read
// beside, and the three statements an operator may make here.
//
// The hard rules of §4.12 are properties of this page rather than sentences on
// it. A bare vote renders as a bare vote and acquires no prose. An outcome
// renders with its criterion, its scope and its date, and a later contrary
// assessment renders beside the earlier one rather than replacing it. A
// comparison's preference is labelled as being about the comparison and is
// never counted as a vote. And the accept/reject/defer/refine decision is a
// link to the review surface that owns it, not a control here: reception
// informs the operator's decision and never becomes one.

function EvaluationItemPage() {
  const { kind = "", id = "" } = useParams();
  const [data, setData] = useState<EvaluationDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(
    (mode: "blocking" | "quiet") => {
      if (mode === "blocking") {
        setLoading(true);
        setError(null);
      }
      getEvaluationDetail(kind, id)
        .then(setData)
        .catch((reason) => {
          if (mode === "blocking") setError(errorMessage(reason));
        })
        .finally(() => {
          if (mode === "blocking") setLoading(false);
        });
    },
    [kind, id],
  );

  useEffect(() => load("blocking"), [load]);

  const recorded = useCallback(
    (message: string) => {
      setAnnouncement(message);
      load("quiet");
    },
    [load],
  );

  if (loading && !data) {
    return (
      <section className="page evaluation-item-page">
        <div className="state-card"><span className="spinner" /> Reading the evaluation…</div>
      </section>
    );
  }
  if (error || !data) {
    return (
      <section className="page evaluation-item-page">
        <Link className="back-link" to="/evaluation">← Backlog</Link>
        <div className="state-card error-state">
          <strong>This evaluation could not be loaded.</strong>
          <span>{error ?? "no answer"}</span>
          <button type="button" onClick={() => load("blocking")}>Try again</button>
        </div>
      </section>
    );
  }

  const item = data.item;
  const artifact = item.artifact;
  const history = data.history ?? [];
  const source = artifactRoute(artifact.subject);
  const superseded = artifact.head_id !== "" && artifact.head_id !== artifact.subject.id;
  const created = formatTime(artifact.created_at);
  // Outcome assessments are pulled out of the history rather than summarized
  // into a badge, because §8.5 refuses a last-writer-wins success mark: a
  // contradiction recorded after a verification has to be readable beside it.
  const outcomes = history.filter((record) => record.assessment?.outcome);
  const reconsiders = history.filter((record) => record.kind === "reconsider");

  return (
    <section className="page evaluation-item-page">
      <Link className="back-link" to="/evaluation">← Backlog</Link>

      <div className="page-heading">
        <div>
          <p className="eyebrow">{kindLabel(artifact.subject.kind)} · evaluation</p>
          <h1 className="untrusted-inline">{artifact.title || "Untitled record"}</h1>
          <p className="mono secondary">{artifact.subject.id}</p>
        </div>
        <div className="heading-meta">
          <Badge label={laneLabel(item.lane)} tone={laneTone(item.lane)} />
          <Badge label={coverageLabel(item.coverage)} tone={coverageTone(item.coverage)} />
          {item.reconsider && <Badge label="Reconsider" tone="violet" />}
        </div>
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      <FallibilityNote />

      <div className="detail-grid">
        <div className="detail-main">
          {/* The revision that was read. It leads because every vote below
              is about this wording and no other: a reader who does not know
              which revision he is looking at cannot judge whether the
              reception still applies. */}
          <article className="card evaluation-revision">
            <h2>The revision under evaluation</h2>
            <dl className="fact-meta">
              <div>
                <dt>Revision</dt>
                <dd className="mono">{artifact.subject.id}</dd>
              </div>
              <div>
                <dt>Chain root</dt>
                <dd className="mono">{artifact.root_id || "—"}</dd>
              </div>
              <div>
                <dt>Current head</dt>
                <dd className="mono">{artifact.head_id || "—"}</dd>
              </div>
              <div>
                <dt>Recorded</dt>
                <dd>{created ? <span title={created.absolute}>{created.relative}</span> : "—"}</dd>
              </div>
              <div>
                <dt>Produced by run</dt>
                <dd className="mono">{artifact.run_id || "—"}</dd>
              </div>
              <div>
                <dt>Review status</dt>
                <dd>{artifact.review_status || artifact.status || "—"}</dd>
              </div>
            </dl>
            {superseded ? (
              <p className="inline-warning">
                A newer revision of this record exists. Everything below is about the wording on
                this page: an endorsement does not move to the next revision, and the newer
                wording is evaluated on its own merits.{" "}
                <Link to={`/evaluation/${encodeURIComponent(artifact.subject.kind)}/${encodeURIComponent(artifact.head_id)}`}>
                  Open the current revision
                </Link>
              </p>
            ) : (
              <p className="muted">This is the current wording of the record.</p>
            )}
            {source ? (
              <Link className="evidence-open" to={source}>Open the record itself →</Link>
            ) : (
              <p className="muted">
                This kind of record has no page of its own in this build; it is read inside the
                record that holds it.
              </p>
            )}
          </article>

          <article className="card evaluation-reception-card">
            <h2>Reception</h2>
            <Reception reception={item.reception} />
            <p className="muted">
              Who said what about this wording. It is not evidence strength, not independent
              corroboration, and not a probability that the idea is right — and it is not a
              decision: the operator's own ruling is recorded on the review surface.
            </p>
          </article>

          <article className="card evaluation-why-card">
            <h2>Why it sits here</h2>
            <WhyHere item={item} />
            {artifact.context.unknown && artifact.context.unknown.length > 0 && (
              <div className="counter-evidence">
                <h4 className="counter-heading">Unknown, and left unknown</h4>
                <ul>
                  {artifact.context.unknown.map((unknown) => (
                    <li className="untrusted-inline" key={unknown}>{unknown}</li>
                  ))}
                </ul>
              </div>
            )}
          </article>

          <ContextPanel context={artifact.context} />

          {artifact.evidence && artifact.evidence.length > 0 && (
            <article className="card evidence-block">
              <h2>What this record cites</h2>
              <EvidenceItems items={artifact.evidence} kind="supporting" />
            </article>
          )}

          <CriteriaPanel artifact={artifact} />

          {outcomes.length > 0 && (
            <article className="card evaluation-outcomes">
              <h2>Observed outcomes</h2>
              <p className="muted">
                Every outcome assessment, in the order recorded — not a single verdict. A later
                contradiction does not delete an earlier verification, and neither is the
                operator's acceptance.
              </p>
              {outcomes.map((record) => (
                <OutcomeEntry record={record} key={record.id} />
              ))}
            </article>
          )}

          {data.alternatives && data.alternatives.length > 0 && (
            <article className="card evaluation-alternatives">
              <h2>Read beside</h2>
              <p className="muted">
                Other remedies recorded against the same problem. This is a reading projection and
                nothing else: each record keeps its own votes, its own decisions and its own page,
                nothing here is merged, and no alternative is suppressed.
              </p>
              <ul className="evaluation-alternative-list">
                {data.alternatives.map((alternative) => (
                  <AlternativeRow item={alternative} key={alternative.artifact.subject.id} />
                ))}
              </ul>
            </article>
          )}

          <article className="card evaluation-history">
            <h2>Evaluation history</h2>
            <p className="muted">
              Append-only, oldest first. An assignment, an exposure, a completed assessment, a
              skip and a failure are separate entries: a retry does not become a second vote, and
              a skip is not a review.
            </p>
            {history.length === 0 ? (
              <p className="muted">
                Nothing has been recorded about this revision yet. That is an absence of review,
                not an absence of opposition.
              </p>
            ) : (
              <ol className="evaluation-history-list">
                {history.map((record) => (
                  <HistoryEntry record={record} key={record.id} />
                ))}
              </ol>
            )}
          </article>
        </div>

        <aside className="detail-side">
          <article className="card">
            <h2>Coverage by role</h2>
            <RoleCoverageTable rows={item.review_coverage} />
            {item.coverage_reason && (
              <p className="muted untrusted-inline">{item.coverage_reason}</p>
            )}
          </article>

          {data.decisions.type !== "" && (
            <article className="card evaluation-decision-link">
              <h2>Your decision</h2>
              <p className="muted">
                Accept, reject, defer, mark duplicate, reopen or request refinement on the review
                surface that owns those words. Reception does not decide, and the only decision
                this page records is your answer to a reconsideration below.
              </p>
              <Link
                className="primary-button"
                to={`/review/${encodeURIComponent(data.decisions.type)}/${encodeURIComponent(data.decisions.id)}`}
              >
                Open the decision surface
              </Link>
            </article>
          )}

          <FeedbackForm
            subject={artifact.subject}
            reasons={data.vocabulary.feedback_reasons}
            onRecorded={recorded}
          />

          <CriteriaForm subject={artifact.subject} onRecorded={recorded} />

          {reconsiders.length > 0 && (
            <ReconsiderForm
              subject={artifact.subject}
              items={reconsiders}
              decisions={data.vocabulary.reconsider_decisions}
              onRecorded={recorded}
            />
          )}

          {data.assignments && data.assignments.length > 0 && (
            <article className="card evaluation-assignments">
              <h2>Assignments</h2>
              <ul className="disposition-list">
                {data.assignments.map((assignment) => {
                  const expires = formatTime(assignment.expires_at);
                  return (
                    <li key={assignment.id}>
                      <span className="mono">{assignment.id}</span>
                      <span className="secondary">
                        {assignment.role} · {assignment.lane} · policy {assignment.policy_version}
                      </span>
                      {expires && (
                        <span className="secondary" title={expires.absolute}>
                          lease {expires.relative}
                        </span>
                      )}
                    </li>
                  );
                })}
              </ul>
            </article>
          )}
        </aside>
      </div>
    </section>
  );
}

// ContextPanel renders the recorded context a recommendation was computed
// against (§5.8, §4.8).
//
// It is on the page because §8.5 requires the operator to be able to correct a
// mistaken context assumption. He corrects it in the Reality ledger, not here:
// the allowance and the recorded work are facts with their own authority, and
// a control on this page that edited them would install a second decision
// vocabulary over the ledger's.
function ContextPanel({ context }: { context: EvaluationContext }) {
  return (
    <article className="card evaluation-context">
      <h2>Recorded context this was ranked against</h2>
      <dl className="fact-meta">
        <div>
          <dt>Context version</dt>
          <dd className="mono">{context.version || "none recorded"}</dd>
        </div>
        <div>
          <dt>Current work</dt>
          <dd>{context.current_work ? "yes" : "not recorded as current work"}</dd>
        </div>
        <div>
          <dt>Recorded pain</dt>
          <dd className="mono">{context.pain}</dd>
        </div>
        <div>
          <dt>Blocked</dt>
          <dd>{context.blocked ? "yes" : "no"}</dd>
        </div>
        <div>
          <dt>Analysis allowance</dt>
          <dd>{context.allowance || "none stated"}</dd>
        </div>
      </dl>
      {context.reasons && context.reasons.length > 0 && (
        <ul className="context-note">
          {context.reasons.map((reason) => (
            <li className="untrusted-inline" key={reason}>{reason}</li>
          ))}
        </ul>
      )}
      {context.evidence && context.evidence.length > 0 && (
        <EvidenceItems items={context.evidence} kind="supporting" />
      )}
      <p className="muted">
        The same votes can produce a different order when this changes.{" "}
        <Link to="/reality/focus">Correct what Babel may spend on this subject</Link> — the
        ordering follows the ledger rather than the other way round.
      </p>
    </article>
  );
}

// CriteriaPanel renders the acceptance criteria this revision is measured
// against, and names the record they came from.
//
// The identity is the operator's criteria record, or it is missing. Nothing
// substitutes for it: a context version, a captured-input digest and a
// snapshot name all look like identifiers, and "verified against ctx-7" is a
// verification against whatever Babel happened to be reading rather than
// against a target a person settled. So an unlinked set says it is unlinked,
// and says what that costs — which is the whole of §4.12's rule that Babel
// may not choose the target it then verifies itself against.
function CriteriaPanel({ artifact }: { artifact: EvaluationArtifact }) {
  const criteria = artifact.criteria ?? [];
  if (criteria.length === 0 && !artifact.criteria_id) return null;
  return (
    <article
      className="card evaluation-accepted-criteria"
      data-criteria-resolved={artifact.criteria_id ? "yes" : "no"}
    >
      <h2>What this is measured against</h2>
      {artifact.criteria_id ? (
        <p className="secondary">
          Criteria record <span className="mono">{artifact.criteria_id}</span>, settled by the
          operator. An outcome assessment names this version, so the target cannot be moved after
          the fact.
        </p>
      ) : (
        <p className="inline-warning">
          These criteria are not linked to an operator criteria record, so they have no version an
          outcome could name. Nothing stands in for that identity here — not the context version,
          not an input digest — and until one exists, an outcome recorded against this record
          cannot be checked against the set it claims to answer.
        </p>
      )}
      {criteria.length > 0 ? (
        <ul className="evaluation-criteria">
          {criteria.map((criterion) => (
            <li key={criterion.id}>
              <span className="mono">{criterion.id}</span>
              <span className="untrusted-inline">{criterion.description}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted">
          The criteria record is named but its criteria are not in this projection.
        </p>
      )}
    </article>
  );
}

function AlternativeRow({ item }: { item: EvaluationItem }) {
  return (
    <li className="evaluation-alternative">
      <SubjectLink subject={item.artifact.subject} title={item.artifact.title} />
      <span className="evaluation-row-marks">
        <Badge label={laneLabel(item.lane)} tone={laneTone(item.lane)} />
        <Reception reception={item.reception} />
      </span>
      {item.objections && item.objections.length > 0 && (
        <span className="secondary untrusted-inline">{item.objections[0]}</span>
      )}
    </li>
  );
}

// OutcomeEntry renders one outcome assessment with everything that bounds it:
// the criterion version it was judged against, the environment observed, when,
// and what the assessor was unsure about. §4.12 refuses a timeless success
// badge, so the scope and the date are part of the claim rather than metadata
// beside it.
function OutcomeEntry({ record }: { record: EvaluationRecord }) {
  const assessment = record.assessment;
  if (!assessment) return null;
  const observed = formatTime(assessment.as_of || record.created_at);
  return (
    <div className="evaluation-outcome">
      <div className="evaluation-row-marks">
        <Badge label={assessment.outcome} tone={outcomeTone(assessment.outcome)} />
        {assessment.criteria_id && (
          <span className="secondary mono" title="The criterion version this was judged against">
            criteria {assessment.criteria_id}
          </span>
        )}
        {observed && (
          <span className="secondary" title={observed.absolute}>observed {observed.relative}</span>
        )}
      </div>
      {assessment.environment && (
        <p className="secondary untrusted-inline">Environment: {assessment.environment}</p>
      )}
      {assessment.uncertainty && (
        <p className="counter-evidence untrusted-inline">
          Unresolved: {unescapeWhitespace(assessment.uncertainty)}
        </p>
      )}
      {assessment.results && assessment.results.length > 0 && (
        <ul className="evaluation-criteria-results">
          {assessment.results.map((result) => (
            <li key={result.criterion_id}>
              <Badge
                label={result.satisfied ? "met" : "not met"}
                tone={result.satisfied ? "green" : "amber"}
              />
              <span className="mono">{result.criterion_id}</span>
              {result.uncertainty && (
                <span className="secondary untrusted-inline">{result.uncertainty}</span>
              )}
              <EvidenceItems items={result.evidence} kind="supporting" />
            </li>
          ))}
        </ul>
      )}
      <p className="secondary">
        Recorded by {record.actor_kind || "an unnamed actor"}
        {record.actor_id ? ` ${record.actor_id}` : ""}. An observation is not the operator's
        acceptance, and a merge is not a deployment.
      </p>
    </div>
  );
}

function outcomeTone(outcome: string) {
  if (outcome === "verified" || outcome === "implemented") return "green" as const;
  if (outcome === "contradicted") return "red" as const;
  return "amber" as const;
}

// HistoryEntry renders one record of whatever kind. A bare vote renders as a
// vote and nothing else — no placeholder rationale, no "no comment provided",
// because both would be this page writing something the reviewer did not.
function HistoryEntry({ record }: { record: EvaluationRecord }) {
  const at = formatTime(record.created_at);
  const assessment = record.assessment;
  return (
    <li className="evaluation-history-entry" data-record-kind={record.kind}>
      <div className="evaluation-row-marks">
        <Badge label={recordKindLabel(record.kind)} tone={recordKindTone(record.kind)} />
        {assessment?.vote && <Badge label={assessment.vote} tone={voteTone(assessment.vote)} />}
        {/* The act, from the record's own field. A decision with no recorded
            polarity is marked as one rather than guessed at from the reason
            below it: an older record written before the field existed states
            no act, and reading one into it would invent the operator's. */}
        {record.kind === "reconsider_decision" && (
          record.decision ? (
            <Badge
              label={reconsiderDecisionLabel(record.decision)}
              tone={reconsiderDecisionTone(record.decision)}
            />
          ) : (
            <span className="not-observed" title="This decision carries no recorded act. What it did is not stated, and the reason below is not read as one.">
              no act recorded
            </span>
          )
        )}
        {at && <span className="secondary" title={at.absolute}>{at.relative}</span>}
        <span className="secondary mono">{record.id}</span>
      </div>

      <p className="secondary">
        {record.actor_kind === "operator"
          ? `You (${record.actor_id || "unattributed"})`
          : record.actor_kind === "run"
            ? `Run ${record.actor_id || record.provenance.run_id || "unnamed"}`
            : "No actor is recorded — this is a sweep the deployment performed, not a statement anybody made"}
        {record.provenance.blinded && (
          <span
            className="not-observed"
            title="Prior evaluations were withheld from the initial read. That is what was served, not a claim about model memory."
          >
            {" "}blinded read
          </span>
        )}
        {record.provenance.model && (
          <span className="secondary mono"> {record.provenance.model}</span>
        )}
      </p>

      {assessment && <AssessmentBody assessment={assessment} />}

      {record.criteria && record.criteria.length > 0 && (
        <ul className="evaluation-criteria">
          {record.criteria.map((criterion) => (
            <li key={criterion.id}>
              <span className="mono">{criterion.id}</span>
              <span className="untrusted-inline">{criterion.description}</span>
            </li>
          ))}
        </ul>
      )}

      {record.reason && (
        <Quoted label={record.actor_kind === "operator" ? "Your reason" : "Reason"} text={record.reason} />
      )}

      {record.kind === "reconsider_decision" && record.decision && (
        <p className="secondary">{reconsiderDecisionBasis(record.decision)}</p>
      )}

      {record.attempt && (
        <p className="secondary">
          Attempt {record.attempt.state}
          {record.attempt.reason ? `: ${record.attempt.reason}` : ""} — a skip or a failure is
          neither a vote nor a completed review.{" "}
          <AttemptCost attempt={record.attempt} />
        </p>
      )}

      {record.checkpoint && (
        <p className="secondary">
          A coverage sweep covering {record.checkpoint.covered.toLocaleString()} records finished.
          Completing the sweep is not the same as everything being reviewed.
        </p>
      )}

      {record.related_id && (
        <p className="secondary mono" title="The record this statement is scoped to">
          scoped to {record.related_id}
        </p>
      )}
    </li>
  );
}

function voteTone(vote: string) {
  if (vote === "support") return "green" as const;
  if (vote === "oppose") return "red" as const;
  return "amber" as const;
}

// AttemptCost says what an attempt was charged, and says which of the two
// kinds of number it is.
//
// An unpriced attempt is one the provider reported no cost for. The charge is
// then the reservation the draw had already set aside — a conservative figure
// kept deliberately, because the alternative is treating unobserved spend as
// zero, and a budget that reads unmeasured work as free is a budget that can
// be exhausted without ever showing a cost. So an unpriced attempt renders as
// a reserved charge with the provider's silence named, never as "$0.00" and
// never as an observation.
function AttemptCost({ attempt }: { attempt: EvaluationAttempt }) {
  if (attempt.unpriced) {
    return (
      <span className="not-observed" title="No price was reported for this attempt, so the reservation was charged instead of assuming the work was free.">
        charged {attempt.cost.toFixed(4)} as the reservation: the provider reported no cost, and
        unmeasured work is not free work
      </span>
    );
  }
  return (
    <span className="secondary">
      cost {attempt.cost.toFixed(4)}, as the provider reported it
    </span>
  );
}

// AssessmentBody renders a review's optional half. The absence of a
// contribution is rendered as the absence it is — one short line — rather than
// as an empty section that reads like a missing field.
function AssessmentBody({ assessment }: { assessment: EvaluationAssessment }) {
  const contributions = assessment.contributions ?? [];
  if (contributions.length === 0 && !assessment.uncertainty) {
    return (
      <p className="muted">
        {assessment.vote ? "A bare vote. No argument was offered, and none is invented here." : ""}
      </p>
    );
  }
  return (
    <div className="evaluation-contributions">
      {contributions.map((contribution, position) => (
        <div className="evaluation-contribution" key={`${contribution.kind}-${position}`}>
          <span className="context-label">{contribution.kind}</span>
          {contribution.text && (
            <Quoted label="Reviewer" text={contribution.text} />
          )}
          {contribution.evidence && contribution.evidence.length > 0 && (
            <EvidenceItems items={contribution.evidence} kind="supporting" />
          )}
          {contribution.alternatives && contribution.alternatives.length > 0 && (
            <p className="secondary">
              Compared with{" "}
              {contribution.alternatives.map((alternative) => (
                <Link className="mono" to={`/evaluation/${encodeURIComponent(alternative.kind)}/${encodeURIComponent(alternative.id)}`} key={alternative.id}>
                  {alternative.id}{" "}
                </Link>
              ))}
              {contribution.preferred && (
                <span className="not-observed" title="A preference inside this comparison. It is not a vote for either record and does not claim one dominates everywhere.">
                  preferred here: {contribution.preferred.id}
                </span>
              )}
            </p>
          )}
          {contribution.would_change && (
            <p className="counter-evidence untrusted-inline">
              Would change this: {unescapeWhitespace(contribution.would_change)}
            </p>
          )}
        </div>
      ))}
      {assessment.uncertainty && (
        <p className="counter-evidence untrusted-inline">
          Unresolved: {unescapeWhitespace(assessment.uncertainty)}
        </p>
      )}
    </div>
  );
}

// FeedbackForm records a scoped reason and nothing else (§5.8).
//
// The reason may accompany an existing decision and must not create one, which
// is why there is no disposition control in this form and why the scope field
// takes the id of a decision the operator already made. Collecting the reason
// alone silently creating a refusal is the exact failure the split exists to
// prevent, and the response's own sentence says so after every write.
function FeedbackForm({
  subject,
  reasons,
  onRecorded,
}: {
  subject: EvaluationSubject;
  reasons: string[];
  onRecorded: (message: string) => void;
}) {
  const [reason, setReason] = useState("");
  const [scope, setScope] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFailure(null);
    try {
      const result = await recordEvaluationOperator({
        subject,
        kind: "feedback",
        reason,
        // A feedback reason performs no act on a reconsider item, so it
        // states none. The field is not optional on the request type: a
        // caller that could leave it out is a caller that could send one by
        // accident.
        decision: "",
        criteria: [],
        related_id: scope,
      });
      setReason("");
      setScope("");
      onRecorded(result.decided);
    } catch (error) {
      setFailure(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="card evaluation-feedback" data-form="feedback">
      <h2>Tell Babel why</h2>
      <p className="muted">
        Your own reason, attributed to you and scoped to this record. It changes no status: a
        reason may accompany a decision you made, and recording one here does not make one.
      </p>
      <form onSubmit={submit} className="focus-form">
        <label>
          <span>Reason</span>
          <input
            type="text"
            value={reason}
            list="evaluation-feedback-reasons"
            onChange={(event) => setReason(event.target.value)}
            placeholder="not-now, wrong-problem, wrong-remedy, or your own words"
            required
          />
        </label>
        {/* Suggestions, not a vocabulary. The service accepts any wording,
            because the three common reasons are not the whole of what an
            operator may have to say about a proposal. */}
        <datalist id="evaluation-feedback-reasons">
          {reasons.map((suggestion) => (
            <option value={suggestion} key={suggestion} />
          ))}
        </datalist>
        <label>
          <span>Scoped to a decision (optional)</span>
          <input
            type="text"
            value={scope}
            onChange={(event) => setScope(event.target.value)}
            placeholder="the id of the decision this reason accompanies"
          />
        </label>
        <button type="submit" className="primary-button" disabled={busy || reason.trim() === ""}>
          {busy && <span className="spinner small" />}
          {busy ? "Recording…" : "Record this reason"}
        </button>
      </form>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
    </article>
  );
}

// CriteriaForm records what would count as this proposal having worked.
//
// Criteria settled after acceptance stay identifiable as a later decision:
// they are a new attributed record linked to the subject, never an edit of the
// earlier set, which is what keeps Babel from rewriting its target and then
// verifying itself against the replacement.
function CriteriaForm({
  subject,
  onRecorded,
}: {
  subject: EvaluationSubject;
  onRecorded: (message: string) => void;
}) {
  const [text, setText] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFailure(null);
    try {
      // One criterion per line, in the order written. The identifier is the
      // operator's own numbering within this set rather than a generated
      // one, so a later outcome assessment can name the line it judged.
      const criteria = text
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line !== "")
        .map((description, position) => ({ id: `c${position + 1}`, description }));
      const result = await recordEvaluationOperator({
        subject,
        kind: "criteria",
        reason: note,
        decision: "",
        criteria,
        related_id: "",
      });
      setText("");
      setNote("");
      onRecorded(result.decided);
    } catch (error) {
      setFailure(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="card evaluation-criteria-form" data-form="criteria">
      <h2>Settle the criteria</h2>
      <p className="muted">
        What would count as this having worked, in your words. A later set is a new attributed
        record rather than an edit: an outcome assessment names the criterion version it was
        judged against, so the target cannot be moved after the fact.
      </p>
      <form onSubmit={submit} className="focus-form">
        <label>
          <span>One criterion per line</span>
          <textarea
            value={text}
            rows={4}
            onChange={(event) => setText(event.target.value)}
            placeholder={"the retry loop no longer appears in new sessions\nno new failure mode in its place"}
            required
          />
        </label>
        <label>
          <span>Why these (optional)</span>
          <input type="text" value={note} onChange={(event) => setNote(event.target.value)} />
        </label>
        <button type="submit" className="primary-button" disabled={busy || text.trim() === ""}>
          {busy && <span className="spinner small" />}
          {busy ? "Recording…" : "Record these criteria"}
        </button>
      </form>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
    </article>
  );
}

// ReconsiderForm answers a Reconsider item with an explicit act.
//
// The act is a choice between two named opposites, and it starts unmade: no
// radio is preselected and the submit stays disabled until the operator picks
// one, because a default here would be a record saying he reopened — or
// declined to — something he never looked at. The reason is required beside
// the act and carries none of its meaning: what the page sends is the word he
// chose, so "reopen this now" in the reason cannot reopen a record he retained
// and a retraction written into the prose cannot retain one he reopened.
//
// The two acts differ in consequence, not only in wording. A reopen reopens
// the record: the service records the decision and the reopened disposition
// in one transaction, so the earlier decision keeps its place in the history
// and the record is undecided again. A retain moves no disposition at all.
// The response's own sentence says which of the two just happened.
function ReconsiderForm({
  subject,
  items,
  decisions,
  onRecorded,
}: {
  subject: EvaluationSubject;
  items: EvaluationRecord[];
  decisions: string[];
  onRecorded: (message: string) => void;
}) {
  const [related, setRelated] = useState(items[0]?.id ?? "");
  const [decision, setDecision] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    // The guard is not only the disabled button: a submit reaching here with
    // no act chosen must send nothing rather than send a guess.
    if (decision === "") {
      setFailure("Choose whether to reopen this or keep the earlier decision. Nothing is recorded until you do.");
      return;
    }
    setBusy(true);
    setFailure(null);
    try {
      const result = await recordEvaluationOperator({
        subject,
        kind: "reconsider_decision",
        reason,
        decision,
        criteria: [],
        related_id: related,
      });
      setReason("");
      setDecision("");
      onRecorded(result.decided);
    } catch (error) {
      setFailure(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="card evaluation-reconsider" data-form="reconsider">
      <h2>Reconsider</h2>
      <p className="muted">
        Something changed about a record you already decided. Say which of the two things you are
        doing about it. Reopening returns the record to undecided — the decision you reopen keeps
        its place in its history, and nothing is accepted or rejected by reopening it. Keeping the
        earlier decision leaves it exactly as it stands.
      </p>
      <form onSubmit={submit} className="focus-form">
        <label>
          <span>Which change</span>
          <select value={related} onChange={(event) => setRelated(event.target.value)}>
            {items.map((record) => (
              <option value={record.id} key={record.id}>
                {record.reason ? record.reason.slice(0, 80) : record.id}
              </option>
            ))}
          </select>
        </label>
        {/* The two acts, from the served vocabulary rather than from a list
            written here, so this control cannot offer one the store refuses.
            Nothing is checked until the operator checks it. */}
        <fieldset>
          <legend>What you are doing</legend>
          {decisions.map((option) => (
            <label className="focus-option" key={option}>
              <input
                type="radio"
                name="reconsider-decision"
                value={option}
                checked={decision === option}
                onChange={() => setDecision(option)}
                data-decision={option}
              />
              <span className="focus-option-body">
                <span className="focus-option-head">
                  {reconsiderDecisionAction(option)}
                </span>
                <span className="focus-option-means">{reconsiderDecisionMeans(option)}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <label>
          <span>Why — your reason, recorded beside the act and never read as one</span>
          <input
            type="text"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            required
          />
        </label>
        <button
          type="submit"
          className="primary-button"
          disabled={busy || decision === "" || reason.trim() === ""}
        >
          {busy && <span className="spinner small" />}
          {busy ? "Recording…" : "Record this decision"}
        </button>
      </form>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
    </article>
  );
}

export default EvaluationItemPage;
