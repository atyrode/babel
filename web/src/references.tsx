import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { getRecordLinks, type RecordReferences, type ReferenceDirection, type ReferenceEdge, type ReferenceEndpoint } from "./api";
import { Badge, UnopenedNote, type Tone } from "./analysis";
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
// this host does not hold.
//
// A note is somebody's prose. It is rendered as attributed untrusted text, on the
// same terms as a model claim or a reviewer's guidance, and it is never a title,
// never a link, and never markup.
//
// An absent graph is absent, not broken. A build with no reference store answers
// `available: false`, and this section then renders nothing at all: a record page
// on a machine that records no citations is a page with one fewer panel, not a
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
  // turn out not to exist would announce a feature this machine does not have.
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
          {links.host && (
            <p className="secondary references-host">
              Read from <span className="mono">{links.host}</span>'s catalog. Another host's
              citations of this record are not in this list.
            </p>
          )}
        </>
      )}
    </article>
  );
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
      {direction.total === 0 ? (
        <p className="muted">{empty}</p>
      ) : (
        <ul className="link-list citation-list">
          {direction.edges.map((edge) => (
            <CitationRow key={edge.id} edge={edge} outgoing={outgoing} />
          ))}
        </ul>
      )}
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

function CitationRow({ edge, outgoing }: { edge: ReferenceEdge; outgoing: boolean }) {
  const phrasing = EDGE_PHRASING[edge.kind];
  const relation = phrasing ? (outgoing ? phrasing.out : phrasing.in) : edge.kind;
  const created = formatTime(edge.created_at);
  return (
    <li className="citation-entry" data-citation={edge.id} data-citation-kind={edge.kind}>
      <div className="citation-relation">
        <Badge label={edge.kind} tone={EDGE_TONES[edge.kind] ?? "neutral"} />
        <span className="citation-phrase">{relation}</span>
        <CitationTarget endpoint={edge.other} />
      </div>
      <div className="citation-provenance">
        <span className="secondary">
          asserted by {edge.actor.kind}
          {edge.actor.id && <span className="mono"> {edge.actor.id}</span>}
        </span>
        {created && (
          <time dateTime={edge.created_at} title={created.absolute}>{created.relative}</time>
        )}
      </div>
      {edge.note && <span className="untrusted-inline citation-note">{edge.note}</span>}
    </li>
  );
}

// CitationTarget is where the inert rule lands. An endpoint the server could not
// resolve, and one whose namespace no page here opens, both render as identified
// text with the reason beside them — the fleet read's sealed row, applied to a
// citation.
function CitationTarget({ endpoint }: { endpoint: ReferenceEndpoint }) {
  // The destination is built here from the namespace and an identifier, and
  // from nowhere else: ROUTES holds every page this build has, so a namespace
  // missing from it renders inert rather than linking into the catch-all.
  const openRoute = endpoint.inert ? undefined : ROUTES[endpoint.kind];
  const route = openRoute?.(endpoint.route_id ?? endpoint.id);
  const name = endpoint.label ?? endpoint.id;
  if (!route) {
    return (
      <span className="citation-target inert">
        <span className="kind-label">{endpoint.kind}</span>
        <span className="mono">{name}</span>
        <UnopenedNote
          reason={
            endpoint.reason ??
            `No page in this build opens a ${endpoint.kind}, so this reference is recorded but not followable here.`
          }
        />
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
      <span className="citation-out">↗ {citations.cites}</span>
      <span className="citation-in">↘ {citations.cited_by}</span>
    </span>
  );
}
