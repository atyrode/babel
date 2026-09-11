import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import {
  addReviewContext,
  decideReview,
  getExportJSON,
  getExportMarkdown,
  getFinding,
  getHypothesis,
  getProposal,
  getReviewHistory,
  type Disposition,
  type FindingDetail,
  type HypothesisDetail,
  type ProposalDetail,
  type ReviewHistory,
} from "../api";
import { errorMessage, formatTime } from "../format";
import {
  Badge,
  CounterEvidence,
  EvidenceItems,
  FallibilityNote,
  Quoted,
  reviewTone,
  TimelineEntry,
  unescapeWhitespace,
} from "../analysis";
import { EvaluationCrossLink } from "../evaluation";
import { ObservationCard } from "./HypothesisPage";
import { RecordLinks } from "../references";
import { TriageAdvice } from "../triage";

// The page that asks for a decision shows what is being decided.
//
// It used to show the record's identifier, the append-only semantics of the
// disposition it was asking for, and a button called "Show Markdown" behind
// which the whole artifact was hiding. An operator was being asked to accept
// or reject a hex id. Whatever else this interface gets wrong, that one was
// fatal: the product exists so that a person reads what Babel found and rules
// on it, and this is the surface where that happens.
//
// So the record is the page now, and the disposition control sits under it.
// Each kind is read from the endpoint that holds it whole, rather than from
// the export: a finding and a hypothesis arrive with the observations they
// rest on, and those carry the evidence locators a reviewer is supposed to
// follow before believing any of it. The export stays, at the bottom, as the
// document a person keeps.

// The five §4.7 dispositions, each with the sentence a reviewer needs before
// choosing it. `reject-and-refine` is deliberately absent: it authorizes a
// refinement request and belongs to the CLI until this page grows the full
// guidance flow.
//
// `reopen` is the one that opens rather than closes, and its sentence says so
// plainly: it is offered because an operator who is told a record has been
// reconsidered needs somewhere to act on that, and accepting a record he has
// not re-read would be the only alternative.
const DISPOSITIONS: Array<{ value: Disposition; label: string; hint: string }> = [
  { value: "accept", label: "Accept", hint: "Endorse this record for projection and follow-on work." },
  { value: "reject", label: "Reject", hint: "Record disagreement. The record is kept, visibly rejected." },
  { value: "defer", label: "Defer", hint: "Not now. The record stays in the queue's history." },
  { value: "duplicate", label: "Duplicate", hint: "Points at an original record, which you name below." },
  {
    value: "reopen",
    label: "Reopen",
    hint: "Undecide it. The earlier decision stays in the history, the status returns to new, and your reason is required.",
  },
];

// RecordBody is the subject, whole, in the shape its own endpoint returns.
// Keeping the three reviewable kinds as separate typed members rather than one
// bag of optional fields is what lets the renderer below say "a finding has
// observations" instead of checking whether this one happens to.
//
// An observation is deliberately absent: internal/review answers "this record
// kind carries no review decision" for one, so no decision is ever asked about
// an observation and this page is never its reader. Observations render here
// all the same — inside the hypothesis or the finding that rests on them.
type RecordBody =
  | { kind: "hypothesis"; detail: HypothesisDetail }
  | { kind: "finding"; detail: FindingDetail }
  | { kind: "proposal"; detail: ProposalDetail };

// readRecord fetches the subject of a review from the endpoint that holds it
// whole: a hypothesis and a finding arrive with the observations they rest on,
// and a proposal with the whole document a run wrote.
function readRecord(type: string, id: string): Promise<RecordBody> {
  switch (type) {
    case "hypothesis":
      return getHypothesis(id).then((detail): RecordBody => ({ kind: "hypothesis", detail }));
    case "finding":
      return getFinding(id).then((detail): RecordBody => ({ kind: "finding", detail }));
    case "proposal":
      return getProposal(id).then((detail): RecordBody => ({ kind: "proposal", detail }));
    default:
      return Promise.reject(new Error(`${type || "that"} is not a reviewable record kind`));
  }
}

function ReviewRecordPage() {
  const { type: routeType, id: routeID } = useParams();
  const type = routeType ?? "";
  const id = routeID ?? "";
  const [history, setHistory] = useState<ReviewHistory | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [body, setBody] = useState<RecordBody | null>(null);
  const [bodyError, setBodyError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(() => {
    setError(null);
    getReviewHistory(type, id)
      .then((value) => setHistory(value))
      .catch((reason) => setError(errorMessage(reason)));
  }, [type, id]);

  useEffect(load, [load]);

  useEffect(() => {
    let live = true;
    setBody(null);
    setBodyError(null);
    readRecord(type, id)
      .then((value) => {
        if (live) setBody(value);
      })
      .catch((reason) => {
        if (live) setBodyError(errorMessage(reason));
      });
    return () => {
      live = false;
    };
  }, [type, id]);

  if (error && !history) {
    return (
      <section className="page">
        <Link className="back-link" to="/review">← Review</Link>
        <div className="state-card error-state">
          <strong>Review history could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!history) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading review history…</div>
      </section>
    );
  }

  // Where the record itself is read. Every reviewable kind has a page now, so
  // a reviewer never has to decide from an identifier: the proposals route
  // completes the set. It is secondary navigation — the record is right here —
  // and it is what an operator follows to see the record in its own context,
  // beside its siblings and its lineage.
  const recordHref =
    type === "hypothesis"
      ? `/hypotheses/${encodeURIComponent(id)}`
      : type === "finding"
        ? `/findings/${encodeURIComponent(id)}`
        : type === "proposal"
          ? `/proposals/${encodeURIComponent(id)}`
          : null;

  return (
    <section className="page detail-page review-record-page">
      <Link className="back-link" to="/review">← Review</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={type} tone="neutral" />
            <Badge label={history.status} tone={reviewTone(history.status)} />
          </div>
          <RecordHeading body={body} type={type} />
          <p className="subtitle mono">{id}</p>
        </div>
        <div className="heading-meta">
          {/* The reception of this exact revision, beside the decision
              being made about it. It is a link rather than a panel: a vote
              count on the decision page would sit one line from the accept
              button, which is the one place §4.12's separation between
              reception and the operator's own authority most has to hold. */}
          <EvaluationCrossLink kind={type} id={id} />
          {recordHref && (
            <Link className="review-link" to={recordHref}>See it in context →</Link>
          )}
        </div>
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

      {bodyError && (
        <div className="state-card error-state">
          <strong>This record's content could not be read.</strong>
          <span>{bodyError}</span>
          <span className="secondary">
            The decision controls below still work, and the export at the foot of the page is
            another way to read the record. Ruling on a record you cannot see is not something
            this page is asking you to do.
          </span>
        </div>
      )}
      {!body && !bodyError && (
        <div className="state-card"><span className="spinner" /> Loading the record…</div>
      )}
      {body && <RecordSubstance body={body} />}

      <DecideForm
        type={type}
        id={id}
        onDecided={(message) => {
          setAnnouncement(message);
          load();
        }}
      />

      <div className="detail-grid">
        <div className="detail-main">
          <article className="card history-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Append-only</p>
                <h2>Decisions</h2>
              </div>
              <span className="count-label">{history.decisions.length}</span>
            </div>
            <p className="muted">
              In the order they were recorded. A reconsidered record shows every decision it has
              ever received; none is edited or removed.
            </p>
            {history.decisions.length === 0 ? (
              <p className="muted">No decision has been recorded yet.</p>
            ) : (
              <ol className="timeline">
                {history.decisions.map((decision) => (
                  <TimelineEntry
                    key={decision.id}
                    badge={decision.disposition}
                    tone={reviewTone(
                      decision.disposition === "accept" ? "accepted"
                        : decision.disposition === "reject" ? "rejected"
                          : decision.disposition === "defer" ? "deferred"
                            // A reopen returns the record to undecided, so it
                            // is toned as the status it produced rather than
                            // as one of the closing four.
                            : decision.disposition === "reopen" ? "new" : "duplicate",
                    )}
                    at={decision.recorded_at}
                  >
                    <span>
                      #{decision.sequence} by <strong>{decision.reviewer_id}</strong>
                      {decision.duplicate_of_id && (
                        <span className="mono secondary"> duplicate of {decision.duplicate_of_id}</span>
                      )}
                    </span>
                    {decision.note && (
                      <span className="untrusted-inline">{unescapeWhitespace(decision.note)}</span>
                    )}
                    {decision.context && (
                      <div className="context-note">
                        <span className="context-label">
                          Guidance from {decision.context.author} — attributed context, never evidence
                        </span>
                        <span className="untrusted-inline">
                          {unescapeWhitespace(decision.context.text)}
                        </span>
                      </div>
                    )}
                  </TimelineEntry>
                ))}
              </ol>
            )}
          </article>

          <article className="card refinements-card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">Authorized by rejection</p>
                <h2>Refinements</h2>
              </div>
              <span className="count-label">{history.refinements.length}</span>
            </div>
            {history.refinements.length === 0 ? (
              <p className="muted">
                No refinement requests. A request is created only by <code>reject and refine</code>,
                atomically with its rejection.
              </p>
            ) : (
              <div className="refinement-list">
                {history.refinements.map((refinement) => (
                  <div className="refinement-entry" key={refinement.request.id}>
                    <div className="observation-heading">
                      <Badge label="refinement request" tone="cyan" />
                      <span className="mono event-index">{refinement.request.id}</span>
                    </div>
                    <p className="untrusted-inline">
                      {unescapeWhitespace(refinement.request.guidance)}
                    </p>
                    {refinement.request.scope && refinement.request.scope.length > 0 && (
                      <p className="secondary">Added scope: {refinement.request.scope.join(", ")}</p>
                    )}
                    {refinement.outcome ? (
                      <p className="secondary">
                        Outcome: <Badge label={refinement.outcome.mode} tone="neutral" /> by{" "}
                        <span className="mono">{refinement.outcome.agent_id}</span>
                        {refinement.outcome.revision && (
                          <span className="mono"> · revision {refinement.outcome.revision.id}</span>
                        )}
                        {refinement.outcome.memory_proposal_id && (
                          <span className="mono"> · memory proposal {refinement.outcome.memory_proposal_id}</span>
                        )}
                      </p>
                    ) : (
                      <p className="secondary">
                        No outcome yet — a refinement runs independently of its parent, and an
                        authorized request without an outcome is a normal state.
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
          </article>
        </div>

        <aside className="detail-side">
          {/* The citation section is on the disposition surface too, because
              the question a reviewer asks before deciding is what else rests on
              the record: a candidate four observations cite is not the same
              decision as an isolated one. */}
          <RecordLinks record={{ type, id }} heading="What this record cites" />
          <ExportCard type={type} id={id} />
        </aside>
      </div>
    </section>
  );
}

// RecordHeading leads with the record's own title, which for a proposal and a
// finding is a sentence a person wrote down to be read. A hypothesis has no
// title — its statement is the whole record — so the kind is the heading and
// the statement follows immediately below, at full length, rather than being
// cut into something that looks like a title.
function RecordHeading({ body, type }: { body: RecordBody | null; type: string }) {
  if (body?.kind === "proposal") {
    return <h1 className="untrusted-inline">{body.detail.payload.title || "Untitled proposal"}</h1>;
  }
  if (body?.kind === "finding") {
    return (
      <h1 className="untrusted-inline">{body.detail.finding.payload.title || "Untitled finding"}</h1>
    );
  }
  if (body?.kind === "hypothesis") return <h1>Hypothesis</h1>;
  return <h1>{type ? `${type[0].toLocaleUpperCase()}${type.slice(1)}` : "Review record"}</h1>;
}

function RecordSubstance({ body }: { body: RecordBody }) {
  switch (body.kind) {
    case "proposal":
      return <ProposalSubstance detail={body.detail} />;
    case "finding":
      return <FindingSubstance detail={body.detail} />;
    case "hypothesis":
      return <HypothesisSubstance detail={body.detail} />;
  }
}

// Points names one of a record's lists in the words a reader would use.
//
// The labels are the ones /proposals/:id already uses, deliberately: the same
// field must not be "Suggested verification criteria" on one surface and
// "how you would know it worked" on the other, and a reader deciding on a
// proposal here and reading it there is the same person. A list the record
// does not hold renders nothing, because an empty "What could go wrong" would
// read as a claim that nothing could.
function Points({ label, note, items }: { label: string; note?: string; items: string[] | undefined }) {
  if (!items || items.length === 0) return null;
  return (
    <div className="proposal-points">
      <h3>{label}</h3>
      {note && <p className="muted">{note}</p>}
      <ul>
        {items.map((item, index) => (
          <li className="untrusted-inline" key={`${index}-${item}`}>{unescapeWhitespace(item)}</li>
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

function ProposalSubstance({ detail }: { detail: ProposalDetail }) {
  const payload = detail.payload;
  const created = formatTime(detail.created_at);
  // A proposal that rests on no finding, or on no candidate, arrives with a
  // JSON null rather than an empty list — Go's nil slice — and a candidate
  // proposal always rests on no finding. Reading the absence as an empty list
  // is this layer's job: the record is saying "none", not "unknown".
  const addresses = detail.finding_ids ?? [];
  const restsOn = detail.hypothesis_ids ?? [];
  return (
    <>
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
          <span className="untrusted-inline">{unescapeWhitespace(payload.uncertainty)}</span>
        </p>
      )}
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
      {payload.targets && payload.targets.length > 0 && (
        <div className="target-list">
          <h3 className="evidence-heading">Suggested targets — suggestions, never facts</h3>
          <ul>
            {payload.targets.map((target) => (
              <li key={target.system}>
                <span className="mono untrusted-inline">{target.system}</span>
                <span className="muted"> · confidence {target.confidence}</span>
                {target.rationale && (
                  <span className="untrusted-inline"> — {unescapeWhitespace(target.rationale)}</span>
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
      <dl className="metadata-list compact">
        {payload.applicability && (
          <Fact label="Where it applies">{unescapeWhitespace(payload.applicability)}</Fact>
        )}
        {payload.temporal_status && <Fact label="Still current?">{payload.temporal_status}</Fact>}
        {detail.form && <Fact label="Form">{detail.form}</Fact>}
        {addresses.length > 0 && (
          <div>
            <dt>Addresses</dt>
            <dd>
              {addresses.map((fid, index) => (
                <span key={fid}>
                  {index > 0 && " · "}
                  <Link className="mono" to={`/findings/${encodeURIComponent(fid)}`}>{fid}</Link>
                </span>
              ))}
            </dd>
          </div>
        )}
        {restsOn.length > 0 && (
          <div>
            <dt>Rests on</dt>
            <dd>
              {restsOn.map((hid, index) => (
                <span key={hid}>
                  {index > 0 && " · "}
                  <Link className="mono" to={`/hypotheses/${encodeURIComponent(hid)}`}>{hid}</Link>
                </span>
              ))}
            </dd>
          </div>
        )}
        <div>
          <dt>Created</dt>
          <dd>{created ? `${created.relative} · ${created.absolute}` : detail.created_at}</dd>
        </div>
      </dl>
    </article>
    {/* Babel's own reading of the record, on the page where the ruling is
        made: advice an operator has to go and find is advice that arrives
        after the decision it was written for. */}
    <TriageAdvice advice={detail.triage} subject={detail.id} />
    </>
  );
}

function FindingSubstance({ detail }: { detail: FindingDetail }) {
  const { finding } = detail;
  // A record that rests on nothing sends a JSON null rather than an empty
  // list, and "rests on nothing" is a state a reviewer has to be able to see.
  const observations = detail.observations ?? [];
  const payload = finding.payload;
  const created = formatTime(finding.created_at);
  return (
    <>
      <article className="card statement-card">
        <Quoted label="The pattern, in the model's wording — untrusted" text={payload.pattern} />
        <FallibilityNote />
        <dl className="metadata-list compact">
          {payload.significance && (
            <Fact label="Why it matters">{unescapeWhitespace(payload.significance)}</Fact>
          )}
          {payload.scope && payload.scope.length > 0 && (
            <Fact label="Where it shows up">{payload.scope.join(" · ")}</Fact>
          )}
          <div>
            <dt>Recurrence</dt>
            <dd>
              {payload.recurrence
                ? `${payload.recurrence} occurrences`
                : "not applicable to this finding"}
            </dd>
          </div>
          {payload.temporal_status && <Fact label="Still current?">{payload.temporal_status}</Fact>}
          <div>
            <dt>Created</dt>
            <dd>{created ? `${created.relative} · ${created.absolute}` : finding.created_at}</dd>
          </div>
        </dl>
        <CounterEvidence items={payload.counter_evidence} absent={payload.counter_evidence_absent} />
      </article>
      <article className="card observations-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">What it rests on</p>
            <h2>Observations</h2>
          </div>
          <span className="count-label">{observations.length}</span>
        </div>
        <p className="muted">
          A finding is only its observations, consolidated. Each keeps its own evidence and its
          own counter-evidence, and every citation that can be opened links into the conversation
          it came from.
        </p>
        {observations.length === 0 ? (
          <p className="muted">
            This finding names no observations. Consolidating nothing is a state worth seeing
            before ruling on it.
          </p>
        ) : (
          <div className="observation-list">
            {observations.map((observation) => (
              <ObservationCard key={observation.id} observation={observation} />
            ))}
          </div>
        )}
      </article>
    </>
  );
}

function HypothesisSubstance({ detail }: { detail: HypothesisDetail }) {
  const { hypothesis } = detail;
  const observations = detail.observations ?? [];
  const payload = hypothesis.payload;
  const created = formatTime(hypothesis.created_at);
  return (
    <>
      <article className="card statement-card">
        <Quoted label="Candidate statement, in the model's wording — untrusted" text={payload.statement} />
        <FallibilityNote />
        <dl className="metadata-list compact">
          {payload.notes && <Fact label="Notes">{unescapeWhitespace(payload.notes)}</Fact>}
          {payload.origin_cues && payload.origin_cues.length > 0 && (
            <Fact label="What suggested it">{payload.origin_cues.join(" · ")}</Fact>
          )}
          {payload.provisional_labels && payload.provisional_labels.length > 0 && (
            <Fact label="Provisional labels">{payload.provisional_labels.join(" · ")}</Fact>
          )}
          <div>
            <dt>Sorting signals</dt>
            <dd>
              novelty {payload.novelty} · priority {payload.priority}
              <span className="muted"> — ordering only, never strength</span>
            </dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>{created ? `${created.relative} · ${created.absolute}` : hypothesis.created_at}</dd>
          </div>
        </dl>
      </article>
      <article className="card observations-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">What was found while investigating it</p>
            <h2>Observations</h2>
          </div>
          <span className="count-label">{observations.length}</span>
        </div>
        {observations.length === 0 ? (
          <p className="muted">
            Nothing has been observed against this candidate yet. It is a statement awaiting
            evidence, which is what deciding on it has to account for.
          </p>
        ) : (
          <div className="observation-list">
            {observations.map((observation) => (
              <ObservationCard key={observation.id} observation={observation} />
            ))}
          </div>
        )}
      </article>
    </>
  );
}

function DecideForm({
  type,
  id,
  onDecided,
}: {
  type: string;
  id: string;
  onDecided: (message: string) => void;
}) {
  const [disposition, setDisposition] = useState<Disposition>("accept");
  const [note, setNote] = useState("");
  const [contextText, setContextText] = useState("");
  const [duplicateOf, setDuplicateOf] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    // The prompt says what this decision does, and a reopen does something
    // the other four do not: it returns the record to undecided. An operator
    // confirming "reopen" must be told that and not the generic sentence.
    const prompt = disposition === "reopen"
      ? `Reopen this ${type}?\n\nThe decision you are reopening stays in the history — nothing ` +
        "is edited or removed — and the record's status returns to new, so it can be decided " +
        "again on its merits."
      : `Record "${disposition}" for this ${type}?\n\nReview decisions are append-only: the ` +
        "event is recorded permanently, and reconsidering later appends another event rather " +
        "than replacing this one.";
    if (!window.confirm(prompt)) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      let contextId: string | undefined;
      if (contextText.trim()) {
        contextId = (await addReviewContext(contextText.trim())).id;
      }
      const result = await decideReview({
        subject: { type: type as never, id },
        disposition,
        contextId,
        duplicateOfId: disposition === "duplicate" ? duplicateOf.trim() || undefined : undefined,
        note: note.trim() || undefined,
      });
      onDecided(`Recorded ${disposition}. The record's status is now ${result.status}.`);
      setNote("");
      setContextText("");
      setDuplicateOf("");
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <article className="card decide-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Your ruling on what you just read</p>
          <h2>Record a decision</h2>
        </div>
      </div>
      <p className="muted">
        A disposition is an appended, attributed event — not a toggle. It cannot be edited or
        undone, only followed by another event.
      </p>
      <form onSubmit={submit}>
        <fieldset className="disposition-set">
          <legend className="sr-only">Disposition</legend>
          {DISPOSITIONS.map((option) => (
            <label
              className={disposition === option.value ? "disposition-option active" : "disposition-option"}
              key={option.value}
            >
              <input
                type="radio"
                name="disposition"
                value={option.value}
                checked={disposition === option.value}
                onChange={() => setDisposition(option.value)}
              />
              <span>
                <strong>{option.label}</strong>
                <span className="muted">{option.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {disposition === "duplicate" && (
          <label className="decide-field">
            Original record ID
            <input
              value={duplicateOf}
              onChange={(event) => setDuplicateOf(event.target.value)}
              placeholder="The record this duplicates"
              required
            />
          </label>
        )}

        {/* The note is the reviewer's own words, and optional on the four
            closing decisions. A reopen requires it: the service refuses a
            reopen with no reason, and asking here says why rather than
            letting the server say no. */}
        <label className="decide-field">
          Note{" "}
          <span className="muted">
            {disposition === "reopen"
              ? "(required: why the earlier decision stopped holding)"
              : "(optional, recorded with the event)"}
          </span>
          <textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            rows={2}
            required={disposition === "reopen"}
          />
        </label>

        <label className="decide-field">
          Attributed context <span className="muted">(optional)</span>
          <textarea
            value={contextText}
            onChange={(event) => setContextText(event.target.value)}
            rows={2}
            placeholder="Guidance later refinement runs will see. Guidance is never evidence."
          />
        </label>

        <button type="submit" className="primary-button" disabled={submitting}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : `Record ${disposition}`}
        </button>
        {submitError && <p className="inline-error" role="alert">{submitError}</p>}
      </form>
    </article>
  );
}

function ExportCard({ type, id }: { type: string; id: string }) {
  const [format, setFormat] = useState<"json" | "markdown" | null>(null);
  const [content, setContent] = useState<string | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);

  async function show(which: "json" | "markdown") {
    setExporting(true);
    setExportError(null);
    setFormat(which);
    try {
      if (which === "json") {
        setContent(JSON.stringify(await getExportJSON(type, id), null, 2));
      } else {
        setContent(await getExportMarkdown(type, id));
      }
    } catch (reason) {
      setExportError(errorMessage(reason));
      setContent(null);
    } finally {
      setExporting(false);
    }
  }

  return (
    <article className="card export-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">To keep or to paste elsewhere</p>
          <h2>Export</h2>
        </div>
      </div>
      {/* The export is no longer where the record is read — the page above is —
          so it is what it always should have been: the whole document with its
          provenance and its fallibility notice, for a human to take away. It is
          still shown as text and never as markup, and it is still redacted:
          internal/web's export route has no way to ask for raw secret values,
          so there is nothing here to disclose behind a further click. */}
      <p className="muted">
        The stored record with its provenance and its own fallibility notice, in the form Babel
        keeps it. Shown as text, never rendered as markup.
      </p>
      <div className="verify-actions">
        <button type="button" onClick={() => show("json")} disabled={exporting}>
          {exporting && format === "json" ? "Fetching…" : "Show JSON"}
        </button>
        <button type="button" onClick={() => show("markdown")} disabled={exporting}>
          {exporting && format === "markdown" ? "Fetching…" : "Show Markdown"}
        </button>
      </div>
      {exportError && <p className="inline-error" role="alert">Export failed: {exportError}</p>}
      {content !== null && !exportError && (
        <details className="json-disclosure" open>
          <summary>{format === "json" ? "JSON export" : "Markdown export (shown as text)"}</summary>
          <pre>{content}</pre>
        </details>
      )}
    </article>
  );
}

export default ReviewRecordPage;
