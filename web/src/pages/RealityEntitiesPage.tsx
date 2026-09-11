import { useCallback, useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { getRealityEntities, type EntitiesResponse, type SubjectCreateResult } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import NameSubjectForm from "./NameSubject";

// What Babel thinks exists (SPEC.md §4.8, §8.4).
//
// An entity is the ledger's stable subject: a project, a repository, a machine,
// a service. Every fact Babel holds is about one of these, and until this page
// the only way to one was an identifier — from a focus rule, from an edge on
// another entity, or from a URL somebody had memorized. §8.4 calls a record
// only a URL reaches a record that is not in the product, so this is the way
// in: the subjects, and how much the ledger has to say about each.
//
// It is also where a subject starts existing. The listing is where an operator
// comes to see what Babel knows about, and the answer on a new deployment is
// "nothing" — so the empty state is not a dead end here either: naming the
// first subject is the act this page offers, and the operator lands on its
// analysis policy afterwards, because deciding what Babel may spend on a thing
// is the reason he had for naming it.
function RealityEntitiesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const kind = params.get("kind") ?? "";
  const [data, setData] = useState<EntitiesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // naming is the form that mints a subject. It is closed by default on a
  // populated ledger and opened from either the heading or the empty state,
  // both of which are places an operator has just discovered that the thing
  // he is looking for is not here.
  const [naming, setNaming] = useState(false);

  // named takes the operator to the policy surface for the subject he just
  // created. Naming a thing is almost never the errand — deciding what Babel
  // may spend on it is — and the focus page resolves a canonical identifier,
  // which is exactly what a creation hands back.
  function named(result: SubjectCreateResult) {
    setNaming(false);
    navigate(`/reality/focus?subject=${encodeURIComponent(result.subject.entity_id)}`);
  }

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getRealityEntities(kind || undefined)
      .then(setData)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [kind]);

  useEffect(load, [load]);

  const items = data?.items ?? [];
  const kinds = data?.kinds ?? [];
  const total = kinds.reduce((sum, entry) => sum + entry.count, 0);

  return (
    <section className="page entities-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Reality Ledger</p>
          <h1>Subjects</h1>
          <p className="subtitle">
            The things Babel knows about, and how much it holds on each. A subject keeps its
            identity through renames and merges, so nothing here is ever lost — only folded.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {kind
                ? `${items.length.toLocaleString()} of ${total.toLocaleString()} subjects`
                : `${total.toLocaleString()} ${total === 1 ? "subject" : "subjects"}`}
            </span>
          )}
          {!naming && (
            <button type="button" className="primary-button" onClick={() => setNaming(true)}>
              Name a subject
            </button>
          )}
        </div>
      </div>

      {naming && (
        <article className="card">
          <div className="section-heading">
            <div>
              <p className="eyebrow">A subject Babel does not know yet</p>
              <h2>Name a subject</h2>
            </div>
          </div>
          <p className="muted">
            A subject is the thing facts are about: a project, a repository, a machine, a
            service. Naming one records that it exists and what you call it, and nothing
            else — the same record <span className="mono">babel reality entity create</span>
            {" "}writes.
          </p>
          <NameSubjectForm onCreated={named} onCancel={() => setNaming(false)} />
        </article>
      )}

      {kinds.length > 0 && (
        <div className="toolbar card">
          <div className="filter-chips" aria-label="Filter by kind">
            <button
              type="button"
              className={kind === "" ? "chip active" : "chip"}
              onClick={() => setParams({})}
            >
              All {total}
            </button>
            {kinds.map((entry) => (
              <button
                type="button"
                key={entry.kind}
                className={kind === entry.kind ? "chip active" : "chip"}
                onClick={() => setParams({ kind: entry.kind })}
              >
                {entry.kind} {entry.count}
              </button>
            ))}
          </div>
        </div>
      )}

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading the ledger…</div>
      )}
      {error && (
        <div className="state-card error-state">
          <strong>The subjects could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="state-card empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>{kind ? `No ${kind} subjects` : "Babel knows of nothing yet"}</strong>
          <span>
            {kind
              ? "The ledger holds no subject of that kind."
              : "Subjects appear as analysis recognizes the projects, repositories and machines " +
                "your sessions talk about, and you can name one yourself at any time."}
          </span>
          {!naming && (
            <button type="button" className="primary-button" onClick={() => setNaming(true)}>
              Name the first subject
            </button>
          )}
        </div>
      )}

      {items.length > 0 && (
        <div className="table-card">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Subject</th>
                  <th>Kind</th>
                  <th className="numeric">Names</th>
                  <th className="numeric">Believed</th>
                  <th className="numeric">Revisions</th>
                  <th>Last recorded</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const latest = formatTime(item.latest_fact);
                  const merged = item.canonical_id !== item.id;
                  const open = () => navigate(`/reality/entities/${encodeURIComponent(item.id)}`);
                  return (
                    <tr
                      key={item.id}
                      tabIndex={0}
                      role="link"
                      onClick={open}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") open();
                      }}
                    >
                      <td className="statement-cell">
                        <strong className="untrusted-inline">{item.display_name}</strong>
                        <span className="secondary mono">{item.id}</span>
                      </td>
                      <td>
                        <Badge label={item.kind} tone="cyan" />
                        {/* A folded identity is still listed, because §4.8
                            forbids losing one and a merge stays reversible.
                            It is marked so that a reader does not take it for
                            a second subject. */}
                        {merged && <Badge label="merged away" tone="amber" />}
                      </td>
                      <td className="numeric mono">{item.aliases}</td>
                      <td className="numeric mono">{item.active_facts}</td>
                      <td className="numeric mono">{item.facts}</td>
                      <td>
                        {latest
                          ? <span title={latest.absolute}>{latest.relative}</span>
                          : <span className="muted">nothing yet</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </section>
  );
}

export default RealityEntitiesPage;
