import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  getRealityEntity,
  type EntityDetail,
  type FactView,
  type ResolutionView,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import { EntityName, FactValue, factTone } from "../reality";
import { Identifiers } from "./RealityData";

// One subject read as a history rather than as four tables.
//
// What the ledger holds about a subject is append-only and dated: a fact is
// asserted, another supersedes it, a dispute opens, two identities are judged
// one thing. Those used to be three sections in three shapes — a fact list,
// a resolution timeline, an alias table — which made the one question a
// reader actually has ("what has happened to this thing, and when?")
// answerable only by reading all three and merging them in your head. They
// are one timeline here, newest first, because they happened in one order and
// the order is the point.
//
// Every fact status is included, superseded revisions and proposals alike:
// reviewing what was proposed is a real need, and a chain that showed only
// its head would hide how reality was corrected. Each revision links to its
// own page, which is where the chain is readable end to end.

// Event is one dated thing that happened to a subject, whatever kind of
// record it came from. The three sources are folded into one shape here
// rather than rendered separately because the timeline is a single sequence;
// `sort` is the whole reason the type exists.
type Event = {
  id: string;
  at: string;
  tone: "active" | "disputed" | "proposed" | "neutral";
  badge: string;
  fact?: FactView;
  resolution?: ResolutionView;
};

function RealityEntityPage() {
  const { id: routeID } = useParams();
  const id = routeID ?? "";
  const [detail, setDetail] = useState<EntityDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setDetail(null);
    setError(null);
    getRealityEntity(id)
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

  // The timeline is derived rather than served: the ledger's three record
  // kinds each have their own route and their own order, and interleaving
  // them is a reading decision rather than a storage one. Recorded time is
  // the axis, because it is the one instant every kind of record has.
  const events = useMemo<Event[]>(() => {
    if (!detail) return [];
    const facts: Event[] = detail.facts.map((fact) => ({
      id: fact.id,
      at: fact.recorded_at,
      tone:
        fact.status === "active" ? "active"
        : fact.status === "disputed" ? "disputed"
        : fact.status === "proposed" ? "proposed"
        : "neutral",
      badge: fact.status,
      fact,
    }));
    const resolutions: Event[] = detail.resolutions.map((resolution) => ({
      id: resolution.id,
      at: resolution.recorded_at,
      tone: resolution.kind === "undo" ? "proposed" : "neutral",
      badge: resolution.kind,
      resolution,
    }));
    return [...facts, ...resolutions].sort((a, b) => b.at.localeCompare(a.at));
  }, [detail]);

  if (error && !detail) {
    return (
      <section className="page">
        <Link className="back-link" to="/ask/entities">← Who and what</Link>
        <div className="surface state-note error-state">
          <strong>Subject could not be loaded.</strong>
          <span>{error}</span>
        </div>
      </section>
    );
  }

  if (!detail) {
    return (
      <section className="page">
        <div className="surface state-note"><span className="spinner" /> Loading subject…</div>
      </section>
    );
  }

  const { entity, aliases, relationships, facts, resolutions } = detail;
  const candidates = detail.candidates ?? [];
  const merged = entity.canonical_id !== entity.id;
  const active = facts.filter((fact) => fact.status === "active").length;

  return (
    <section className="page detail-page entity-page">
      <Link className="back-link" to="/ask/entities">← Who and what</Link>
      <div className="page-heading detail-heading subject-head">
        <div>
          <div className="heading-badges">
            <Badge label={entity.kind} tone="cyan" />
            {merged && <Badge label="merged away" tone="amber" />}
          </div>
          {/* The name, and no identifier under it. The identifier is at the
              foot of the page with the rest of the machinery. */}
          <h1 className="untrusted-inline entity-name">{entity.display_name}</h1>
          <p className="subtitle">
            {active === 0
              ? "Babel believes nothing about this subject yet."
              : `${active} standing ${active === 1 ? "belief" : "beliefs"}, ${facts.length} ${
                  facts.length === 1 ? "revision" : "revisions"
                } in all.`}
          </p>

          {/* What it is called and what it is attached to, as chips: two
              short vocabularies a reader scans rather than two tables a
              reader parses. */}
          {(aliases.length > 0 || relationships.length > 0) && (
            <ul className="ask-chips">
              {aliases.map((alias) => (
                <li
                  className={alias.state === "asserted" ? "ask-chip" : "ask-chip retired"}
                  key={alias.id}
                >
                  <span className="ask-chip-kind">{alias.kind}</span>
                  <span className="untrusted-inline">{alias.value}</span>
                </li>
              ))}
              {relationships.map((relationship) => {
                const other = relationship.from.id === entity.id ? relationship.to : relationship.from;
                const outward = relationship.from.id === entity.id;
                return (
                  <li className="ask-chip" key={relationship.id}>
                    <span className="ask-chip-kind">
                      {outward ? relationship.kind : `${relationship.kind} of`}
                    </span>
                    <EntityName entity={other} current={entity.id} />
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        {/* The way to "stop spending on this" from the record it is about.
            The subject travels as the entity id rather than as its display
            name: an id is an identifier this build minted, while a name is
            operator vocabulary that may mean two things, and the focus page
            has to be able to report that ambiguity rather than be handed a
            guess. */}
        <div className="heading-meta">
          <Link className="secondary-button" to={`/settings?section=ceilings&subject=${encodeURIComponent(entity.id)}`}>
            Analysis policy
          </Link>
        </div>
      </div>

      {merged && (
        <div className="surface state-note">
          <strong>This subject was folded into another.</strong>
          <span>
            It now speaks as{" "}
            <Link to={`/ask/entities/${encodeURIComponent(entity.canonical_id)}`}>
              its canonical identity
            </Link>
            . Merges are append-only history, so this record and its facts remain readable.
          </span>
        </div>
      )}

      {entity.notes && (
        <article className="surface">
          <p className="eyebrow">Notes</p>
          <p className="untrusted-inline">{entity.notes}</p>
        </article>
      )}

      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Append-only, newest first</p>
            <h2>What has happened to it</h2>
          </div>
          <span className="count-label">{events.length}</span>
        </div>
        {/* A subject with nothing recorded got two paragraphs: the rule the
            timeline follows, and then the sentence saying there is no
            timeline to follow it. An empty section is one sentence — the one
            that says what would put something here — and the explanation of
            how the ledger writes arrives when there is something written to
            explain. */}
        {events.length === 0 ? (
          <p className="muted">
            Nothing recorded yet: an entry appears when an answer about this subject is
            interpreted and accepted, or when its identity is judged.
          </p>
        ) : (
          <>
            <p className="muted">
              Every revision Babel recorded about this subject and every judgement about its
              identity, in the order they were written. A correction is a new entry rather than an
              edit, so what was replaced is still here and says so.
            </p>
            <ol className="subject-timeline">
              {events.map((event) => (
                <SubjectEvent key={event.id} event={event} current={entity.id} />
              ))}
            </ol>
          </>
        )}
      </article>

      {/* The one stored link between this subject and the analysis that
          concerns it: a run resolves a candidate to the entities it is about
          and records that resolution. Findings and proposals reach a subject
          only through the candidates they develop, which is what each
          record's own page continues from. */}
      <article className="surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Scoped to it</p>
            <h2>What Babel explored here</h2>
          </div>
          <span className="count-label">{candidates.length}</span>
        </div>
        {candidates.length === 0 ? (
          <p className="muted">
            No exploration has been scoped to it yet — a candidate joins a subject when a run
            resolves what it is about.
          </p>
        ) : (
          <ul className="subject-candidates">
            {candidates.map((candidate) => {
              const created = formatTime(candidate.created_at);
              return (
                <li className="subject-candidate" key={candidate.id}>
                  <Link className="untrusted-inline" to={`/r/${encodeURIComponent(candidate.id)}`}>
                    {candidate.statement}
                  </Link>
                  <p className="subject-candidate-meta">
                    <Badge label={candidate.status} tone="neutral" />
                    {created && <span title={created.absolute}>{created.relative}</span>}
                  </p>
                </li>
              );
            })}
          </ul>
        )}
      </article>

      <Identifiers
        rows={[
          ["Subject", entity.id],
          ...(merged ? ([["Canonical", entity.canonical_id]] as [string, string][]) : []),
          ...resolutions.map((resolution): [string, string] => [
            `${resolution.kind} by ${resolution.actor}`,
            resolution.id,
          ]),
        ]}
      />
    </section>
  );
}

// SubjectEvent is one dot on the timeline: what happened, when, and the
// reasoning the record carries. A fact reads as its claim; a resolution reads
// as the judgement and the reason given for it.
function SubjectEvent({ event, current }: { event: Event; current: string }) {
  const at = formatTime(event.at);
  const { fact, resolution } = event;
  return (
    <li className={`subject-event ${event.tone}`}>
      <div className="subject-event-head">
        <Badge label={event.badge} tone={fact ? factTone(fact.status) : "violet"} />
        {/* The date in the ledger's own form rather than the locale's: a
            column of YYYY-MM-DD is scannable and sorts by eye, which is the
            whole reason it is mono and tabular. The locale rendering is the
            tooltip, and the relative one is beside it. */}
        {at && (
          <time className="subject-event-when" dateTime={event.at} title={at.absolute}>
            {event.at.slice(0, 10)}
          </time>
        )}
        {at && <span className="subject-event-when">{at.relative}</span>}
      </div>

      {fact && (
        <>
          <p className="subject-event-claim">
            <Link to={`/ask/facts/${encodeURIComponent(fact.id)}`}>
              <span className="mono">{fact.predicate}</span> <FactValue fact={fact} />
            </Link>
          </p>
          <p className="subject-event-note">
            <span className="secondary">
              {fact.authority.kind}
              {fact.confidence && ` · confidence ${fact.confidence}`}
            </span>
            {fact.note && <> — <span className="untrusted-inline">{fact.note}</span></>}
          </p>
        </>
      )}

      {resolution && (
        <>
          <p className="subject-event-claim">
            {resolution.sources.map((source, index) => (
              <span key={source.id}>
                {index > 0 && ", "}
                <EntityName entity={source} current={current} />
              </span>
            ))}
            {resolution.results.length > 0 && " → "}
            {resolution.results.map((result, index) => (
              <span key={result.id}>
                {index > 0 && ", "}
                <EntityName entity={result} current={current} />
              </span>
            ))}
          </p>
          <p className="subject-event-note">
            <span className="secondary">decided by {resolution.actor}</span>
            {resolution.reason && <> — <span className="untrusted-inline">{resolution.reason}</span></>}
          </p>
        </>
      )}
    </li>
  );
}

export default RealityEntityPage;
