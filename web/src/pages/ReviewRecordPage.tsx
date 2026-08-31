import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  addReviewContext,
  decideReview,
  getExportJSON,
  getExportMarkdown,
  getReviewHistory,
  type Disposition,
  type ReviewHistory,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, reviewTone, TimelineEntry } from "../analysis";
import { RecordLinks } from "../references";

// The four §4.7 dispositions, each with the sentence a reviewer needs before
// choosing it. `reject-and-refine` is deliberately absent: it authorizes a
// refinement request and belongs to the CLI until this page grows the full
// guidance flow.
const DISPOSITIONS: Array<{ value: Disposition; label: string; hint: string }> = [
  { value: "accept", label: "Accept", hint: "Endorse this record for projection and follow-on work." },
  { value: "reject", label: "Reject", hint: "Record disagreement. The record is kept, visibly rejected." },
  { value: "defer", label: "Defer", hint: "Not now. The record stays in the queue's history." },
  { value: "duplicate", label: "Duplicate", hint: "Points at an original record, which you name below." },
];

function ReviewRecordPage() {
  const { type: routeType, id: routeID } = useParams();
  const type = routeType ?? "";
  const id = routeID ?? "";
  const [history, setHistory] = useState<ReviewHistory | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const load = useCallback(() => {
    setError(null);
    getReviewHistory(type, id)
      .then((value) => setHistory(value))
      .catch((reason) => setError(errorMessage(reason)));
  }, [type, id]);

  useEffect(load, [load]);

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

  const recordHref =
    type === "hypothesis"
      ? `/hypotheses/${encodeURIComponent(id)}`
      : type === "finding"
        ? `/findings/${encodeURIComponent(id)}`
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
          <h1>Review record</h1>
          <p className="subtitle mono">{id}</p>
        </div>
        {recordHref && (
          <div className="heading-meta">
            <Link className="review-link" to={recordHref}>View the record →</Link>
          </div>
        )}
      </div>

      <p className="sr-only" role="status" aria-live="polite">{announcement}</p>

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
                          : decision.disposition === "defer" ? "deferred" : "duplicate",
                    )}
                    at={decision.recorded_at}
                  >
                    <span>
                      #{decision.sequence} by <strong>{decision.reviewer_id}</strong>
                      {decision.duplicate_of_id && (
                        <span className="mono secondary"> duplicate of {decision.duplicate_of_id}</span>
                      )}
                    </span>
                    {decision.note && <span className="untrusted-inline">{decision.note}</span>}
                    {decision.context && (
                      <div className="context-note">
                        <span className="context-label">
                          Guidance from {decision.context.author} — attributed context, never evidence
                        </span>
                        <span className="untrusted-inline">{decision.context.text}</span>
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
                    <p className="untrusted-inline">{refinement.request.guidance}</p>
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
          <DecideForm
            type={type}
            id={id}
            onDecided={(message) => {
              setAnnouncement(message);
              load();
            }}
          />
          <ExportCard type={type} id={id} />
          {/* The citation section is on the disposition surface too, because
              the question a reviewer asks before deciding is what else rests on
              the record: a candidate four observations cite is not the same
              decision as an isolated one. */}
          <RecordLinks record={{ type, id }} heading="What this record cites" />
        </aside>
      </div>
    </section>
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
    const prompt =
      `Record "${disposition}" for this ${type}?\n\nReview decisions are append-only: the ` +
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
          <p className="eyebrow">Consequential</p>
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

        <label className="decide-field">
          Note <span className="muted">(optional, recorded with the event)</span>
          <textarea value={note} onChange={(event) => setNote(event.target.value)} rows={2} />
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
          <p className="eyebrow">Raw private view</p>
          <h2>Export</h2>
        </div>
      </div>
      <p className="muted">
        The whole record with provenance intact, for a human to read. The document opens with
        its own fallibility notice; it is shown here as text, never rendered as markup.
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
