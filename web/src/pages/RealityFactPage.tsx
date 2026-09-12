import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityFact, type FactDetail, type FactView } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, TimelineEntry } from "../analysis";
import { EntityName, FactValue, factTone } from "../reality";

// One fact, with the chain it sits in (SPEC.md §4.8, §8.4).
//
// §4.8 gives the ledger no update path: a correction is a new revision, and the
// one it replaces keeps its bytes and gains a status event saying it is no
// longer in force. That rule is the ledger's central promise, and it is only
// worth anything if a reader can check it — which is what this page is for. It
// shows what the revision asserts, who asserted it and on what authority, what
// it replaced, what replaced it, every status it has passed through, and any
// dispute it is party to.
//
// There is no control on this page that changes a fact, and there is not
// supposed to be. §4.8 admits exactly two ways a fact comes to exist — an
// operator's own attributed statement, which the Focus page makes for the one
// predicate that carries it, and an interpretation the operator accepted — so
// the decision this record admits is offered where those live, not here.
function RealityFactPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<FactDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getRealityFact(id)
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
        <Link className="back-link" to="/ask/facts">← Beliefs</Link>
        <div className="surface state-note error-state">
          <strong>This fact could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="surface state-note"><span className="spinner" /> Loading fact…</div>
      </section>
    );
  }

  const { fact } = detail;
  const observed = formatTime(fact.observed_at);
  const recorded = formatTime(fact.recorded_at);
  const validFrom = formatTime(fact.valid_from);
  const validUntil = formatTime(fact.valid_until);
  const expires = formatTime(fact.expires_at);
  const authority = formatTime(fact.authority.at);

  return (
    <section className="page detail-page fact-page">
      <Link className="back-link" to="/ask/facts">← Beliefs</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <Badge label={fact.status} tone={factTone(fact.status)} />
            {fact.sensitivity !== "routine" && <Badge label={fact.sensitivity} tone="red" />}
          </div>
          <h1 className="fact-claim">
            <EntityName entity={detail.subject} />{" "}
            <span className="mono">{fact.predicate}</span>{" "}
            <FactValue fact={fact} />
          </h1>
          <p className="subtitle mono">{fact.id}</p>
        </div>
        <div className="heading-meta">
          <Link
            className="secondary-button"
            to={`/ask/entities/${encodeURIComponent(fact.subject_id)}`}
          >
            Everything about this subject
          </Link>
        </div>
      </div>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Who says so</p>
            <h2>Authority and confidence</h2>
          </div>
        </div>
        <p className="muted">
          {/* §4.8's authority rule, said in the place a reader can act on it:
              an observation can only ever propose, and a proposal is not
              something Babel believes. */}
          Only an operator or a registered trusted source can make a fact authoritative.
          Anything derived from observation — Git activity, repository inspection, Babel's own
          analysis — can only ever be a proposal.
        </p>
        <dl className="metadata-list compact">
          <div>
            <dt>Authority</dt>
            <dd>
              {fact.authority.kind}
              {fact.authority.id && <span className="mono"> {fact.authority.id}</span>}
            </dd>
          </div>
          <div>
            <dt>Stated</dt>
            <dd>{authority ? <span title={authority.absolute}>{authority.relative}</span> : "—"}</dd>
          </div>
          <div>
            <dt>Confidence</dt>
            <dd>{fact.confidence}</dd>
          </div>
          <div>
            <dt>Sensitivity</dt>
            <dd>{fact.sensitivity}</dd>
          </div>
        </dl>
        {fact.note && (
          <Quoted label="Reasoning given by the authority — kept verbatim" text={fact.note} />
        )}
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">When it holds</p>
            <h2>Time</h2>
          </div>
        </div>
        <p className="muted">
          A fact's valid time is not when it was written down. Babel can record today something
          that was true last month, and freshness is measured from when it was observed rather
          than from when it arrived.
        </p>
        <dl className="metadata-list compact">
          <div>
            <dt>Valid from</dt>
            <dd>{validFrom ? <span title={validFrom.absolute}>{validFrom.relative}</span> : "—"}</dd>
          </div>
          <div>
            <dt>Valid until</dt>
            <dd>
              {validUntil
                ? <span title={validUntil.absolute}>{validUntil.relative}</span>
                : <span className="muted">open-ended</span>}
            </dd>
          </div>
          <div>
            <dt>Observed</dt>
            <dd>{observed ? <span title={observed.absolute}>{observed.relative}</span> : "—"}</dd>
          </div>
          <div>
            <dt>Recorded</dt>
            <dd>{recorded ? <span title={recorded.absolute}>{recorded.relative}</span> : "—"}</dd>
          </div>
          <div>
            <dt>Freshness</dt>
            <dd>
              {expires
                ? <span title={expires.absolute}>expires {expires.relative}</span>
                : <span className="muted">does not expire</span>}
            </dd>
          </div>
        </dl>
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only</p>
            <h2>Revision chain</h2>
          </div>
        </div>
        <p className="muted">
          Nothing in this ledger is edited or deleted. What a revision replaced is still here, and
          what replaced it says so.
        </p>
        <div className="chain">
          <ChainLink
            label="Replaced"
            fact={detail.supersedes}
            absent="This is the first thing Babel recorded on this point."
          />
          <ChainLink
            label="Replaced by"
            fact={detail.superseded_by}
            absent="Nothing has replaced this revision."
          />
        </div>
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">History</p>
            <h2>Status events</h2>
          </div>
          <span className="count-label">{detail.history.length}</span>
        </div>
        <p className="muted">
          Every status this revision has held, oldest first. Expiry and supersession are appended
          events rather than changes — which is what makes "marked stale, never deleted" something
          a reader can verify instead of trust.
        </p>
        <ol className="timeline">
          {detail.history.map((event) => (
            <TimelineEntry
              key={event.id}
              badge={event.status}
              tone={factTone(event.status)}
              at={event.recorded_at}
            >
              {event.note && <span className="untrusted-inline">{event.note}</span>}
            </TimelineEntry>
          ))}
        </ol>
      </article>

      {detail.disputes.length > 0 && (
        <article className="surface">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Contradiction</p>
              <h2>Disputes</h2>
            </div>
            <span className="count-label">{detail.disputes.length}</span>
          </div>
          <p className="muted">
            Two facts that cannot both hold open a dispute rather than letting the newer one win.
            An open dispute means nobody has decided yet.
          </p>
          {detail.disputes.map((dispute) => (
            <div className="dispute-entry" key={dispute.id}>
              <div className="action-heading">
                <Badge label={dispute.state} tone={dispute.state === "open" ? "red" : "neutral"} />
                <span className="mono">{dispute.predicate}</span>
                <span className="mono secondary">{dispute.id}</span>
              </div>
              {dispute.reason && <p className="action-rationale untrusted-inline">{dispute.reason}</p>}
              <p className="secondary">
                Between:{" "}
                {dispute.fact_ids.map((factID, index) => (
                  <span key={factID}>
                    {index > 0 && ", "}
                    {factID === fact.id ? (
                      <span className="mono">this revision</span>
                    ) : (
                      <Link className="mono" to={`/ask/facts/${encodeURIComponent(factID)}`}>
                        {factID}
                      </Link>
                    )}
                  </span>
                ))}
              </p>
            </div>
          ))}
        </article>
      )}
    </section>
  );
}

// ChainLink renders one end of the revision chain, or states its absence.
// "Nothing replaced this" and "this replaced nothing" are real, useful facts
// about a revision, and an empty space would leave the reader unable to tell
// them from a page that failed to load them.
function ChainLink({
  label,
  fact,
  absent,
}: {
  label: string;
  fact: FactView | undefined;
  absent: string;
}) {
  if (!fact) {
    return (
      <div className="chain-link empty">
        <p className="eyebrow">{label}</p>
        <p className="muted">{absent}</p>
      </div>
    );
  }
  const recorded = formatTime(fact.recorded_at);
  return (
    <div className="chain-link">
      <p className="eyebrow">{label}</p>
      <div className="fact-heading">
        <Badge label={fact.status} tone={factTone(fact.status)} />
        <Link className="fact-predicate mono" to={`/ask/facts/${encodeURIComponent(fact.id)}`}>
          {fact.predicate}
        </Link>
        <FactValue fact={fact} />
      </div>
      <p className="secondary">
        authority {fact.authority.kind}
        {recorded && <span title={recorded.absolute}> · recorded {recorded.relative}</span>}
      </p>
    </div>
  );
}

export default RealityFactPage;
