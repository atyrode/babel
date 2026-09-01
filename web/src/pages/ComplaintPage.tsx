import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getComplaint, type ComplaintDetail, type ComplaintRevision } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, TimelineEntry } from "../analysis";
import { RecordLinks } from "../references";

// Issue #115's record page: one complaint, in the operator's own words.
//
// There are no action controls on this page at all, and both halves of that
// are deliberate. Nothing here edits a record's wording — a hypothesis page
// cannot revise a hypothesis either; amending is `babel tell --amend`'s
// append, and this surface only reads the chain it produced. And nothing
// here closes, because #115's guard is that steering pressure has no
// resolved state: the only answer to "was this addressed?" is the Cited by
// direction of #113's reference graph, rendered below, and a button that
// marked a complaint done would turn it into the ticket it must never be.

function ComplaintPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<ComplaintDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getComplaint(id)
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
        <Link className="back-link" to="/review">← Review</Link>
        <div className="state-card error-state">
          <strong>Complaint could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="state-card"><span className="spinner" /> Loading complaint…</div>
      </section>
    );
  }

  const { complaint, revisions } = detail;
  const told = formatTime(complaint.at);

  return (
    <section className="page complaint-page">
      <Link className="back-link" to="/review">← Review</Link>
      <div className="page-heading">
        <div>
          <p className="eyebrow">Operator steering</p>
          <h1>Complaint</h1>
          <p className="subtitle">
            The operator's own words, verbatim. A complaint has no status, no closure and no
            resolved marker; whether it was ever addressed is what cites it below.
          </p>
        </div>
        <div className="heading-meta">
          <span className="count-label">revision {complaint.sequence}</span>
          {/* "superseded" grades this wording against the chain's head, never
              the want behind it: an amended complaint's earlier text is still
              a readable record at its own id, which is why this page opened
              it at all. */}
          <Badge
            label={complaint.head ? "current wording" : "superseded wording"}
            tone={complaint.head ? "green" : "amber"}
          />
        </div>
      </div>

      <p className="secondary complaint-attribution">
        Told by <span className="mono">{complaint.by}</span> on{" "}
        <span className="mono">{complaint.host}</span>
        {told ? ` · ${told.relative} · ${told.absolute}` : ` · ${complaint.at}`}
      </p>

      <article className="card statement-card">
        <Quoted label="What the operator said" text={complaint.text} />
        {complaint.redacted && (
          <p className="secondary">Secret-shaped material was replaced with placeholders before this was stored.</p>
        )}
      </article>

      <article className="card revisions-card">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only</p>
            <h2>Revision history</h2>
          </div>
          <span className="count-label">{revisions.length}</span>
        </div>
        <p className="muted">
          Every wording of this complaint, oldest first. Amending appends: an earlier wording
          stays readable at its own identifier, and nothing here ends a chain.
        </p>
        <ol className="timeline revision-timeline">
          {revisions.map((revision) => (
            <RevisionRow key={revision.id} revision={revision} viewing={complaint.id} />
          ))}
        </ol>
      </article>

      <RecordLinks record={{ type: "complaint", id }} />

      <p className="muted complaint-no-status">
        "Was this addressed?" is the Cited by list above and nothing else. Babel records no
        closure, and an unaddressed complaint is information rather than a task nobody did.
      </p>
    </section>
  );
}

// RevisionRow is one wording in the chain. The mono id links to that
// wording's own page, because a superseded wording is a readable record
// rather than history's leftovers (§4.7).
function RevisionRow({ revision, viewing }: { revision: ComplaintRevision; viewing: string }) {
  return (
    <TimelineEntry badge={`revision ${revision.sequence}`} tone="violet" at={revision.at}>
      <span className="revision-record">
        <Link className="mono link-target" to={`/complaints/${encodeURIComponent(revision.id)}`}>
          {revision.id}
        </Link>
        {revision.id === viewing && <span className="revision-mark"> — the wording shown above</span>}
        {revision.head && <span className="revision-mark"> — current</span>}
      </span>
      <span className="untrusted-inline">{revision.summary}</span>
    </TimelineEntry>
  );
}

export default ComplaintPage;
