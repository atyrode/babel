import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  getComplaints,
  tellComplaint,
  type AdjacentOutput,
  type CaptureResult,
  type ComplaintsResponse,
  type ComplaintSummary,
} from "./api";
import { errorMessage, formatTime } from "./format";
import { Badge, Quoted } from "./analysis";

// Issue #115's capture surface: the box the operator tells Babel what is
// going badly into, and the listing of what has been told so far.
//
// It rides the review surfaces by operator decision (2026-08-31) rather than
// being a destination of its own: the operator who came to review is the
// operator with something to say, and a separate page would put the box one
// navigation away from the moment the complaint forms.
//
// The box records a want, not a fact — pressure on where exploration goes,
// never a claim about the repository — and it deliberately offers no status,
// closure, or assignment control. A complaint that acquired one would make
// Babel a work tracker, and GitHub already is one. "Was this addressed?" is
// answered by what cites the complaint (#113's cited_by direction) and by
// nothing rendered here.

// The record kinds an adjacency row can name that this app has a page for,
// and the route that opens one. A kind absent from this table renders as
// identified mono text rather than a link into the catch-all redirect — the
// same rule references.tsx states for an unopenable endpoint. The destination
// is derived from this table and the row's id, never from anything a record
// says: a URL built from record content would make the adjacency list an
// injection surface.
const ADJACENT_ROUTES: Record<string, (id: string) => string> = {
  hypothesis: (id) => `/hypotheses/${encodeURIComponent(id)}`,
  finding: (id) => `/findings/${encodeURIComponent(id)}`,
  complaint: (id) => `/complaints/${encodeURIComponent(id)}`,
};

export function SteeringSection() {
  const [text, setText] = useState("");
  const [capturing, setCapturing] = useState(false);
  const [captureError, setCaptureError] = useState<string | null>(null);
  const [result, setResult] = useState<CaptureResult | null>(null);
  const [listing, setListing] = useState<ComplaintsResponse | null>(null);
  const [listingError, setListingError] = useState<string | null>(null);

  const loadListing = useCallback(() => {
    getComplaints()
      .then((value) => {
        setListing(value);
        setListingError(null);
      })
      .catch((reason) => setListingError(errorMessage(reason)));
  }, []);

  useEffect(() => loadListing(), [loadListing]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (capturing || text.trim() === "") return;
    setCapturing(true);
    setCaptureError(null);
    // The result block reports the capture just made; keeping the previous
    // one under a new attempt would attribute it to the new text.
    setResult(null);
    tellComplaint(text)
      .then((value) => {
        setResult(value);
        setText("");
        // A successful capture reloads the listing: the row the operator just
        // minted belongs at the top of "what has been told".
        loadListing();
      })
      .catch((reason) => {
        // A failed capture is one card's problem. The error renders inline
        // here, the text stays in the box for another attempt, and the
        // listing keeps whatever it had.
        setCaptureError(errorMessage(reason));
      })
      .finally(() => setCapturing(false));
  }

  return (
    <section className="steering-section">
      <article className="card capture-card">
        <p className="eyebrow">Operator steering</p>
        <h2>Tell Babel</h2>
        <p className="muted">
          Say what is going badly. This is steering pressure, not a ticket: it opens nothing,
          assigns nothing and schedules nothing, and it has no status to close.
        </p>
        <form onSubmit={submit}>
          <textarea
            className="capture-input"
            aria-label="What is going badly"
            placeholder="I am having a hard time enforcing my repository rules…"
            value={text}
            onChange={(event) => setText(event.target.value)}
          />
          <button
            type="submit"
            className="primary-button capture-submit"
            disabled={capturing || text.trim() === ""}
          >
            {capturing && <span className="spinner small" />}
            {capturing ? "Capturing…" : "Tell Babel"}
          </button>
        </form>
        {captureError && (
          <p className="inline-error" role="alert">The complaint was not captured: {captureError}</p>
        )}
        {result && <CaptureOutcome result={result} />}
      </article>
      <ComplaintListing listing={listing} error={listingError} />
    </section>
  );
}

// CaptureOutcome is what one successful capture answered, kept on screen
// until the next attempt replaces it: the minted id, the verbatim wording as
// stored, and what Babel already holds touching it.
function CaptureOutcome({ result }: { result: CaptureResult }) {
  const { complaint } = result;
  return (
    <div className="capture-result">
      <p className="capture-recorded">
        Captured as <span className="mono">{complaint.id}</span>
      </p>
      <Quoted label="What you told Babel" text={complaint.text} />
      {complaint.redacted && (
        <p className="secondary">Secret-shaped material was replaced with placeholders before this was stored.</p>
      )}
      <h3>What Babel already has touching this</h3>
      {result.adjacency_note && (
        <p className="secondary capture-adjacency-note">{result.adjacency_note}</p>
      )}
      {result.adjacent.length > 0 ? (
        <ul className="capture-adjacent">
          {result.adjacent.map((row) => (
            <li key={`${row.kind}-${row.id}`}>
              <Badge label={row.kind} />
              <AdjacentId row={row} />
              <span className="untrusted-inline">{row.summary}</span>
            </li>
          ))}
        </ul>
      ) : (
        !result.adjacency_note && (
          <p className="muted">Nothing Babel has said so far touches this.</p>
        )
      )}
      {/* The steering sentence is the server's fixed wording, rendered
          verbatim and never re-worded here. It exists to state what capturing
          did NOT do — opened, assigned and scheduled none of it — and a
          client-side paraphrase would drift into promising the work tracker
          #115 forbids, one adjective at a time. */}
      <p className="muted capture-steering">{result.steering}</p>
    </div>
  );
}

// AdjacentId renders the row's identifier: a link when the kind has a page
// here, identified mono text when it does not.
function AdjacentId({ row }: { row: AdjacentOutput }) {
  const route = ADJACENT_ROUTES[row.kind];
  if (!route) return <span className="mono">{row.id}</span>;
  return (
    <Link className="mono link-target" to={route(row.id)}>{row.id}</Link>
  );
}

// ComplaintListing is the "what has been told" card. Its rows navigate like
// ReviewPage's queue rows — role, tabIndex, click and Enter/Space — because
// they are the same gesture: open the record the row summarizes.
function ComplaintListing({
  listing,
  error,
}: {
  listing: ComplaintsResponse | null;
  error: string | null;
}) {
  const navigate = useNavigate();

  function open(item: ComplaintSummary) {
    navigate(`/complaints/${encodeURIComponent(item.id)}`);
  }

  const items = listing?.items ?? [];

  return (
    <article className="card steering-list-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Steering pressure</p>
          <h2>Complaints</h2>
        </div>
        {listing && <span className="count-label">{listing.total}</span>}
      </div>
      <p className="muted">
        What the operator has told Babel, newest first. Nothing here has a status: whether a
        complaint was ever addressed is what cites it.
      </p>
      {error && (
        <p className="inline-error" role="alert">Complaints could not be listed: {error}</p>
      )}
      {!listing && !error && (
        <p className="muted"><span className="spinner" /> Reading what has been told…</p>
      )}
      {listing && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing has been told yet</strong>
          <span>The box above is where steering pressure enters: tell Babel what is going badly and it is recorded here.</span>
        </div>
      )}
      {items.length > 0 && (
        <div className="table-scroll">
          <table className="frontier-table steering-table">
            <thead>
              <tr>
                <th>Complaint</th>
                <th>Told by</th>
                <th>Host</th>
                <th>Told</th>
                <th className="numeric">Addressed by</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const told = formatTime(item.at);
                return (
                  <tr
                    key={item.id}
                    role="link"
                    tabIndex={0}
                    onClick={() => open(item)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter" || event.key === " ") open(item);
                    }}
                  >
                    <td className="statement-cell">
                      <strong className="untrusted-inline">{item.summary}</strong>
                      <span className="secondary mono">{item.id}</span>
                    </td>
                    <td>{item.by}</td>
                    <td className="mono">{item.host}</td>
                    <td>
                      {told ? <span title={told.absolute}>{told.relative}</span> : item.at}
                    </td>
                    {/* Absent citations render as an em dash, never a zero:
                        absent means nobody counted, and a 0 would report a
                        measurement never taken (CitationCount's rule). */}
                    <td className="numeric">
                      {item.citations ? (
                        <span className="mono" title="records that cite this complaint">
                          {item.citations.cited_by}
                        </span>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </article>
  );
}
