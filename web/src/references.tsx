import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getRecordLinks, type RecordReferences, type ReferenceDirection, type ReferenceEdge, type ReferenceEndpoint } from "./api";
import { Badge, type Tone } from "./analysis";
import { formatTime } from "./format";

// Issue #113's citation section, shared by every record surface.
//
// Three rules are implemented here rather than restated per page.
//
// A destination is derived, never received. recordRoute maps a namespace and an
// identifier onto this app's own route; the API carries no URL and this file
// builds none from anything a record says. A namespace with no page here renders
// as identified text with the reason, which is exactly what the lineage panel has
// always done for a kind it cannot open — and what the server does for a record
// this instance does not hold.
//
// A note is somebody's prose. It is rendered as attributed untrusted text, on the
// same terms as a model claim or a reviewer's guidance, and it is never a title,
// never a link, and never markup.
//
// An absent graph is absent, not broken. A build with no reference store answers
// `available: false`, and this section then renders nothing at all: a record page
// in a build that records no citations is a page with one fewer panel, not a
// page with an error on it.

// EDGE_TONES gives each edge kind its own chip colour, matching the palette the
// dashboard already uses for record state. The mapping is semantic rather than
// decorative: supersedes and duplicates are the two that demote the record a
// reader is looking at, evidence is the one that grounds it, and a kind this
// build has never heard of falls through to neutral rather than being hidden.
const EDGE_TONES: Record<string, Tone> = {
  evidence: "green",
  supersedes: "amber",
  refines: "cyan",
  addresses: "blue",
  inspired_by: "violet",
  duplicates: "red",
};

// EDGE_PHRASING is what each edge kind claims, in the direction the reader is
// standing in. #113 closes the vocabulary, so the phrasing is a table rather
// than a sentence assembled from the kind's own identifier: "duplicates" read
// from the far side is "is duplicated by", and a surface that printed the raw
// kind in both columns would invert half of them.
const EDGE_PHRASING: Record<string, { out: string; in: string }> = {
  evidence: { out: "rests on", in: "is evidence for" },
  supersedes: { out: "supersedes", in: "is superseded by" },
  refines: { out: "refines", in: "is refined by" },
  addresses: { out: "addresses", in: "is addressed by" },
  inspired_by: { out: "grew out of", in: "inspired" },
  duplicates: { out: "duplicates", in: "is duplicated by" },
};

// The record namespaces this app has a page for, and the route that opens one.
// A namespace absent from this table is one no page here renders, and its row
// says so instead of linking into the catch-all redirect.
const ROUTES: Record<string, (id: string) => string> = {
  session: (id) => `/sessions/${encodeURIComponent(id)}`,
  hypothesis: (id) => `/hypotheses/${encodeURIComponent(id)}`,
  finding: (id) => `/findings/${encodeURIComponent(id)}`,
  proposal: (id) => `/review/proposal/${encodeURIComponent(id)}`,
  // #115's record pages exist now, so a complaint endpoint is followable
  // rather than inert.
  complaint: (id) => `/complaints/${encodeURIComponent(id)}`,
};

// RecordLinks is the panel. `record` names the subject in the same vocabulary
// the other record reads on the page use: the namespace and the identifier the
// route already has, which for a session is its selector.
export function RecordLinks({
  record,
  heading = "Citations",
}: {
  record: { type: string; id: string };
  heading?: string;
}) {
  const [links, setLinks] = useState<RecordReferences | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setLinks(null);
    setError(null);
    getRecordLinks(record.type, record.id)
      .then((value) => {
        if (live) setLinks(value);
      })
      .catch((reason) => {
        // The panel keeps its own failure. A citation section that could not
        // load is one panel's problem, and the page around it renders the
        // record itself, which is what the operator came for.
        if (live) setError(reason instanceof Error ? reason.message : String(reason));
      });
    return () => {
      live = false;
    };
  }, [record.type, record.id]);

  // Nothing is rendered while the first read is in flight, and nothing is
  // rendered at all on a build with no graph. A spinner for a panel that may
  // turn out not to exist would announce a feature this build does not have.
  if (!links && !error) return null;
  if (links && !links.available) return null;

  return (
    <article className="card references-card">
      <div className="section-heading">
        <div>
          <p className="eyebrow">Typed references</p>
          <h2>{heading}</h2>
        </div>
        {links && (
          <span className="count-label">{links.cites.total + links.cited_by.total}</span>
        )}
      </div>
      <p className="muted">
        Append-only citations recorded beside the record, never inside it. Each names who asserted
        it; none is evidence on its own.
      </p>
      {error && (
        <p className="inline-error" role="alert">
          Citations could not be loaded: {error}
        </p>
      )}
      {links && (
        <>
          <CitationDirection
            label="Cites"
            empty="This record cites nothing."
            direction={links.cites}
            outgoing
          />
          <CitationDirection
            label="Cited by"
            empty="Nothing cites this record."
            direction={links.cited_by}
            outgoing={false}
          />
        </>
      )}
    </article>
  );
}

// KIND_ABSENCE says where a cited kind is read when Babel has no page for it.
//
// It replaces the sentence that used to repeat under every such row — seven
// times on a finding, in a paragraph beginning "No page in this build opens a
// observation". A reader does not need to be told seven times, does not need
// to be told about builds, and is owed something better than an absence: an
// observation is not missing from Babel, it is read inside the candidate it
// develops, and the candidates are in this same list.
const KIND_ABSENCE: Record<string, string> = {
  observation:
    "an observation is read inside the candidate it develops, where its claim, its evidence " +
    "and its counter-evidence render together",
};

// citationRoute derives where an endpoint opens, or nothing.
//
// The destination is built from the namespace and an identifier and from
// nowhere else: ROUTES holds every page this app has, so a namespace missing
// from it resolves to nothing rather than linking into the catch-all redirect,
// and an endpoint the server marked inert never resolves at all.
function citationRoute(endpoint: ReferenceEndpoint): string | undefined {
  if (endpoint.inert) return undefined;
  return ROUTES[endpoint.kind]?.(endpoint.route_id ?? endpoint.id);
}

function CitationDirection({
  label,
  empty,
  direction,
  outgoing,
}: {
  label: string;
  empty: string;
  direction: ReferenceDirection;
  outgoing: boolean;
}) {
  const shown = direction.edges.length;
  // What cannot be opened is counted for the whole direction and explained
  // once underneath it, by kind. Which record is unreachable is on its own row;
  // why a kind has no page is one fact about Babel and belongs in one sentence.
  const unopened = new Map<string, number>();
  for (const edge of direction.edges) {
    if (citationRoute(edge.other) || edge.other.reason) continue;
    unopened.set(edge.other.kind, (unopened.get(edge.other.kind) ?? 0) + 1);
  }
  // A run that asserts a dozen citations in one pass writes the same sentence
  // on every one of them, and twelve identical notes are one note. Any note
  // that repeats is said once above the rows and dropped from them; a note
  // carried by exactly one citation keeps its own line, because then it is
  // prose about that citation and a reader has to be able to tell which.
  const noteCounts = new Map<string, number>();
  for (const edge of direction.edges) {
    if (edge.note) noteCounts.set(edge.note, (noteCounts.get(edge.note) ?? 0) + 1);
  }
  const repeated = [...noteCounts].filter(([, count]) => count > 1);
  const hoisted = new Set(repeated.map(([note]) => note));
  return (
    <section className="citation-direction">
      <div className="citation-heading">
        <h3>{label}</h3>
        <span className="citation-chips">
          {direction.counts.map((count) => (
            <Badge
              key={count.kind}
              label={`${count.kind} ${count.count}`}
              tone={EDGE_TONES[count.kind] ?? "neutral"}
            />
          ))}
        </span>
      </div>
      {repeated.map(([note, count]) => (
        <p className="citation-note" key={note}>
          <span className="secondary">{count} of these carry the same note: </span>
          <span className="untrusted-inline">{note}</span>
        </p>
      ))}
      {direction.total === 0 ? (
        <p className="muted">{empty}</p>
      ) : (
        <ul className="link-list citation-list">
          {direction.edges.map((edge) => (
            <CitationRow
              key={edge.id}
              edge={edge}
              outgoing={outgoing}
              hideNote={edge.note !== undefined && hoisted.has(edge.note)}
            />
          ))}
        </ul>
      )}
      {[...unopened].map(([kind, count]) => (
        <p className="secondary citation-absence" key={kind}>
          {count === 1 ? `The ${kind} above has no page of its own` : `The ${count} ${kind} rows above have no page of their own`}
          {KIND_ABSENCE[kind] ? ` — ${KIND_ABSENCE[kind]}.` : ". Its identifier is what reopens it from the record's own store."}
        </p>
      ))}
      {/* The store bounds its own answer and this page bounds it again, so a
          reader who is seeing part of a direction is told rather than left to
          infer it from a chip count that does not match the rows. */}
      {shown < direction.total && (
        <p className="secondary">
          Showing {shown} of {direction.total}. The rest are in the record's own store.
        </p>
      )}
    </section>
  );
}

// CitationRow is one citation, on one line: what the relation claims, the
// record at the far end, and when it was asserted.
//
// It used to be three lines and a paragraph, thirteen times over, between a
// reader and the decision control below. The provenance is still here — an
// edge nobody is attributed for is not a citation — but "asserted by run
// <id>" is what a reader checks once, so it rides the row's own tooltip
// instead of a line of its own. A note keeps its line unless the direction
// above already said it for every row.
function CitationRow({
  edge,
  outgoing,
  hideNote,
}: {
  edge: ReferenceEdge;
  outgoing: boolean;
  hideNote: boolean;
}) {
  const phrasing = EDGE_PHRASING[edge.kind];
  const relation = phrasing ? (outgoing ? phrasing.out : phrasing.in) : edge.kind;
  const created = formatTime(edge.created_at);
  const asserted = `Asserted by ${edge.actor.kind}${edge.actor.id ? ` ${edge.actor.id}` : ""}`;
  return (
    <li
      className="citation-entry"
      data-citation={edge.id}
      data-citation-kind={edge.kind}
      title={asserted}
    >
      <div className="citation-relation">
        <Badge label={edge.kind} tone={EDGE_TONES[edge.kind] ?? "neutral"} />
        <span className="citation-phrase">{relation}</span>
        <CitationTarget endpoint={edge.other} />
        {created && (
          <time className="secondary" dateTime={edge.created_at} title={created.absolute}>
            {created.relative}
          </time>
        )}
      </div>
      {edge.note && !hideNote && (
        <span className="untrusted-inline citation-note">{edge.note}</span>
      )}
    </li>
  );
}

// CitationTarget is where the inert rule lands. An endpoint this app has no
// page for renders as identified text rather than as a link that would fail,
// and the server's own reason renders beside it when there is one: "no
// finding with that identifier could be read" is about one record and cannot
// be hoisted into a sentence about a kind.
function CitationTarget({ endpoint }: { endpoint: ReferenceEndpoint }) {
  const route = citationRoute(endpoint);
  const name = endpoint.label ?? endpoint.id;
  if (!route) {
    return (
      <span className="citation-target inert">
        <span className="kind-label">{endpoint.kind}</span>
        <span className="mono">{name}</span>
        {endpoint.reason && (
          <span className="unopened-note untrusted-inline">{endpoint.reason}</span>
        )}
      </span>
    );
  }
  return (
    <Link className="citation-target link-target" to={route}>
      <span className="kind-label">{endpoint.kind}</span>
      <span className={endpoint.label ? "untrusted-inline" : "mono"}>{name}</span>
    </Link>
  );
}

// CitationCount is the inbox row's compact form of the same fact: how many
// citations leave a record and how many arrive. It renders nothing when the
// count is absent, because absent means nobody counted — a queue that showed a
// zero there would be reporting a measurement it never took.
export function CitationCount({
  citations,
}: {
  citations: { cites: number; cited_by: number } | undefined;
}) {
  if (!citations) return null;
  if (citations.cites === 0 && citations.cited_by === 0) {
    return <span className="citation-count none">no citations</span>;
  }
  return (
    <span
      className="citation-count"
      title="Typed references out of and into this record."
      data-citations={`${citations.cites}/${citations.cited_by}`}
    >
      <span className="citation-out" title="Typed references this record makes.">
        ↗ {citations.cites} cites
      </span>
      <span className="citation-in" title="Typed references that name this record.">
        ↘ {citations.cited_by} cited by
      </span>
    </span>
  );
}
