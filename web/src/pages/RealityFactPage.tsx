import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { getRealityFact, type FactDetail, type FactView } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, Quoted, TimelineEntry } from "../analysis";
import { EntityName, FactValue, factTone } from "../reality";
import { Identifiers } from "./RealityData";

// One belief, with the chain it sits in (SPEC.md §4.8, §8.4).
//
// The page is ordered the way the belief is read rather than the way it is
// stored: where it stands, what it says, who says so and why, and only then
// the timestamps and identifiers that make it addressable. The old order was
// the record's own field order — an identifier, then four metadata lists,
// then the reasoning — which put the one sentence a reader came for in the
// middle of the page.
//
// §4.8 gives the ledger no update path: a correction is a new revision, and
// the one it replaces keeps its bytes and gains a status event saying it is
// no longer in force. That rule is the ledger's central promise, and it is
// only worth anything if a reader can check it, so both ends of the chain,
// every status the revision has passed through, and any dispute it is party
// to are all on this page.
//
// There is no control here that changes a fact, and there is not supposed to
// be. §4.8 admits exactly two ways a fact comes to exist — an operator's own
// attributed statement, which the analysis-policy surface makes for the one
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
        <Link className="back-link" to="/ask/facts">← What it believes</Link>
        <div className="surface state-note error-state">
          <strong>This belief could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="surface state-note"><span className="spinner" /> Loading belief…</div>
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
  const open = detail.disputes.filter((dispute) => dispute.state === "open");

  return (
    <section className="page detail-page fact-page">
      <Link className="back-link" to="/ask/facts">← What it believes</Link>

      <div className="page-heading detail-heading">
        <div>
          {/* Standing first, because it decides whether the sentence below
              is something Babel believes, something it used to believe, or
              something nobody has ruled on. */}
          <div className="belief-standing">
            <Badge label={fact.status} tone={factTone(fact.status)} />
            {fact.sensitivity !== "routine" && <Badge label={fact.sensitivity} tone="red" />}
            {open.length > 0 && <Badge label="contested" tone="red" />}
          </div>
          <h1 className="belief-claim">
            <span className="belief-predicate">
              <EntityName entity={detail.subject} /> · {fact.predicate}
            </span>
            <FactValue fact={fact} />
          </h1>
        </div>
        <div className="heading-meta">
          <Link to={`/ask/entities/${encodeURIComponent(fact.subject_id)}`}>
            Everything about this subject
          </Link>
        </div>
      </div>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Why Babel holds this</p>
            <h2>
              {fact.authority.kind === "operator"
                ? "You said so"
                : `Asserted on ${fact.authority.kind} authority`}
            </h2>
          </div>
        </div>
        {/* §4.8's authority rule, said in the place a reader can act on it:
            an observation can only ever propose, and a proposal is not
            something Babel believes. */}
        <p className="belief-prose muted">
          Only an operator or a registered trusted source can make a fact authoritative.
          Anything derived from observation — Git activity, repository inspection, Babel's own
          analysis — can only ever be a proposal, and a proposal is not a belief.
        </p>
        {fact.note ? (
          <Quoted label="Reasoning given by the authority — kept verbatim" text={fact.note} />
        ) : (
          <p className="belief-prose muted">
            No reasoning was recorded with this revision — only the claim, its authority and
            its dates.
          </p>
        )}
        {detail.disputes.length > 0 && (
          <>
            <p className="belief-prose">
              Two revisions that cannot both hold open a dispute rather than letting the newer
              one win. An open dispute means nobody has decided yet.
            </p>
            {detail.disputes.map((dispute) => (
              <div className="dispute-entry" key={dispute.id}>
                <div className="action-heading">
                  <Badge label={dispute.state} tone={dispute.state === "open" ? "red" : "neutral"} />
                  <span className="mono">{dispute.predicate}</span>
                </div>
                {dispute.reason && (
                  <p className="action-rationale untrusted-inline">{dispute.reason}</p>
                )}
                <p className="secondary">
                  Between:{" "}
                  {dispute.fact_ids.map((factID, index) => (
                    <span key={factID}>
                      {index > 0 && ", "}
                      {factID === fact.id ? (
                        <span>this revision</span>
                      ) : (
                        <Link to={`/ask/facts/${encodeURIComponent(factID)}`}>
                          the other revision
                        </Link>
                      )}
                    </span>
                  ))}
                </p>
              </div>
            ))}
          </>
        )}
      </article>

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only</p>
            <h2>What it replaced, and what replaced it</h2>
          </div>
        </div>
        <p className="belief-prose muted">
          Nothing in this ledger is edited or deleted. What a revision replaced is still here,
          and what replaced it says so.
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
        <p className="belief-prose muted">
          Every status this revision has held, oldest first. Expiry and supersession are
          appended events rather than changes — which is what makes "marked stale, never
          deleted" something a reader can verify instead of trust.
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

      {/* Provenance last and in mono: a belief's dates and authorities are
          what make it checkable, and checking is what a reader does after
          reading the claim rather than before. A fact's valid time is not
          when it was written down — Babel can record today something that
          was true last month, and freshness is measured from when it was
          observed rather than from when it arrived. */}
      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Provenance</p>
            <h2>Dates and authority</h2>
          </div>
        </div>
        <dl className="belief-provenance">
          <div>
            <dt>Authority</dt>
            <dd>{fact.authority.kind}</dd>
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
          <div>
            <dt>Valid from</dt>
            <dd>{validFrom ? <span title={validFrom.absolute}>{validFrom.relative}</span> : "—"}</dd>
          </div>
          <div>
            <dt>Valid until</dt>
            <dd>
              {validUntil
                ? <span title={validUntil.absolute}>{validUntil.relative}</span>
                : "open-ended"}
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
                : "does not expire"}
            </dd>
          </div>
        </dl>
        <Identifiers
          rows={[
            ["Revision", fact.id],
            ["Subject", fact.subject_id],
            ...(fact.authority.id ? ([["Authority", fact.authority.id]] as [string, string][]) : []),
            ...(fact.supersedes ? ([["Replaced", fact.supersedes]] as [string, string][]) : []),
            ...detail.disputes.map((dispute): [string, string] => ["Dispute", dispute.id]),
          ]}
        />
      </article>
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
