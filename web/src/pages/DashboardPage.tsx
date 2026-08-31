import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { getOverview, type Overview, type OverviewSection } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, FallibilityNote, statusTone, reviewTone } from "../analysis";

// The landing page. It aggregates and it does not act: every number here is
// durable state one of the six services already held, read through the single
// /api/overview snapshot, and nothing on this page starts a run, invokes a
// model, or writes a record. A panel is a glance plus a way in — the totals,
// the few most recent rows, and the link to the page that owns them.

// GUIDE_KEY records that the operator has seen the pointer to Help. The banner
// is one line and appears once: a first visit deserves an orientation, and a
// tour that reappeared would be an interface arguing with someone who already
// knows where things are. Storage is best-effort, exactly as the token
// bootstrap treats it — a locked-down browser shows the banner again rather
// than failing to render the dashboard.
const GUIDE_KEY = "babel.web.guide-dismissed";

// Panel is the shared frame every tile uses: an eyebrow, a heading, the link to
// the page that owns the records, and either the body or the server's own note
// about why this section could not be read. The note is the server's wording
// rather than a generic failure line, because "no repository is configured" and
// "the frontier could not be read" call for different actions.
//
// footer renders whether or not this section is available, which is what makes
// the review panel's two halves independent: a machine can hold a Reality
// ledger and no review log, and the question inbox must not disappear because
// the panel it shares a tile with could not be read.
function Panel({
  eyebrow,
  title,
  section,
  to,
  linkLabel,
  children,
  footer,
}: {
  eyebrow: string;
  title: string;
  section: OverviewSection;
  to: string;
  linkLabel: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <article className="card panel">
      <div className="section-heading">
        <div>
          <p className="eyebrow">{eyebrow}</p>
          <h2>{title}</h2>
        </div>
        <Link className="panel-link" to={to}>
          {linkLabel} <span aria-hidden="true">→</span>
        </Link>
      </div>
      {section.available ? children : (
        <p className="panel-note" role="status">{section.unavailable}</p>
      )}
      {footer}
    </article>
  );
}

// Facts is the at-a-glance row: a few large numbers with their labels. Absence
// is rendered as an em dash rather than as a zero, because "no snapshots" and
// "not observed" are different facts.
function Facts({ items }: { items: Array<{ label: string; value: ReactNode; title?: string }> }) {
  return (
    <dl className="panel-facts">
      {items.map((item) => (
        <div key={item.label}>
          <dt>{item.label}</dt>
          <dd title={item.title}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Relative({ at }: { at: string | null | undefined }) {
  const time = formatTime(at);
  if (!time) return <span className="muted">—</span>;
  return <span title={time.absolute}>{time.relative}</span>;
}

function DashboardPage() {
  const [data, setData] = useState<Overview | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // A first visit is the only one that gets the pointer to Help; storage is
  // best-effort exactly as the token bootstrap treats it, so a locked-down
  // browser shows the banner again rather than failing to render.
  const [guide, setGuide] = useState(() => {
    try {
      return window.localStorage.getItem(GUIDE_KEY) !== "1";
    } catch {
      return true;
    }
  });

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getOverview()
      .then((value) => setData(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(load, [load]);

  function dismissGuide() {
    setGuide(false);
    try {
      window.localStorage.setItem(GUIDE_KEY, "1");
    } catch {
      // The banner stays dismissed for this page view either way.
    }
  }

  return (
    <section className="page dashboard-page">
      {guide && (
        <div className="guide-banner" role="note">
          <span>
            First time here? <Link to="/help">What Babel is — and what it is not</Link> explains the
            lifecycle, the vocabulary, and how each command reaches these pages.
          </span>
          <button type="button" className="icon-button" onClick={dismissGuide} aria-label="Dismiss the guide pointer">
            ×
          </button>
        </div>
      )}

      <div className="page-heading">
        <div>
          <p className="eyebrow">This machine</p>
          <h1>Dashboard</h1>
          <p className="subtitle">
            What Babel holds right now: the archive it can reach, the corpus it has described, and
            the analytical records waiting for a human. Nothing here starts work — exploration runs
            from a terminal, and this page reads what it left behind.
          </p>
        </div>
        <div className="heading-meta">
          {data?.corpus.available && data.corpus.refreshed_at && (
            <span className="refresh-time">
              catalog read <Relative at={data.corpus.refreshed_at} />
            </span>
          )}
          <button type="button" onClick={load} disabled={loading}>
            {loading && <span className="spinner small" />}
            {loading ? "Reading…" : "Refresh"}
          </button>
        </div>
      </div>

      {loading && !data && (
        <div className="state-card"><span className="spinner" /> Reading this machine's state…</div>
      )}
      {error && !data && (
        <div className="state-card error-state">
          <strong>The dashboard could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}

      {data && (
        <div className="panel-grid">
          <Panel
            eyebrow="Restic repository"
            title="Archive health"
            section={data.archive}
            to="/archive"
            linkLabel="Archive"
          >
            <Facts
              items={[
                { label: "Snapshots", value: data.archive.snapshots },
                { label: "Hosts", value: data.archive.hosts_total },
                { label: "Newest snapshot", value: <Relative at={data.archive.latest_time} /> },
                // Null is unknown, never zero: nothing read the shared catalog,
                // so "0 uncatalogued" would state a fact this page did not observe.
                {
                  label: "Uncatalogued",
                  value: data.archive.uncatalogued ?? <span className="not-observed">unknown</span>,
                },
                {
                  label: "Catalog-pending",
                  value: data.archive.pending ?? <span className="not-observed">unknown</span>,
                },
              ]}
            />
            <p className="panel-caption">
              {data.archive.catalog_reachable === null
                ? "No shared catalog was read, so there is nothing for this repository to be behind."
                : data.archive.catalog_reachable
                  ? "Uncatalogued snapshots are durable but unrecorded; the next push records them."
                  : "The shared catalog did not answer, so the lag is unknown rather than zero."}
            </p>
            {data.archive.hosts.length === 0 ? (
              <p className="muted">This repository holds no snapshots yet.</p>
            ) : (
              <ul className="panel-rows">
                {data.archive.hosts.map((host) => (
                  <li key={host.host}>
                    <div className="panel-row-main">
                      <strong className="untrusted-inline">{host.host}</strong>
                      <span className="secondary mono">
                        {host.latest_short_id || "no snapshot"}
                      </span>
                    </div>
                    <span className="panel-row-meta">
                      {host.snapshots} {host.snapshots === 1 ? "snapshot" : "snapshots"} ·{" "}
                      <Relative at={host.latest_time} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="panel-caption mono-caption">
              {data.archive.repository || "no repository"} · host {data.archive.host_id || "unknown"}
            </p>
          </Panel>

          <Panel
            eyebrow="Local catalog"
            title="Corpus"
            section={data.corpus}
            to="/sessions"
            linkLabel="Sessions"
          >
            <Facts
              items={[
                { label: "Sessions", value: data.corpus.sessions },
                { label: "Titled", value: `${data.corpus.titled} of ${data.corpus.sessions}` },
                {
                  label: "Awaiting description",
                  value: data.corpus.pending,
                  title: "Sessions the running catalog scan has not described yet.",
                },
              ]}
            />
            <p className="panel-caption">
              Titles: {data.corpus.recorded} recorded by the harness, {data.corpus.derived} derived
              from the session's own records, {data.corpus.inferred} inferred by a model. The three
              are different kinds of claim, so they are counted separately.
            </p>
            {data.corpus.scan.running && (
              <p className="panel-caption scan-line" role="status">
                <span className="spinner small" />
                Describing {data.corpus.scan.described} of {data.corpus.scan.total} sessions. The
                Sessions page follows the scan; this page shows what the catalog already holds.
              </p>
            )}
            {data.corpus.harnesses.length === 0 ? (
              <p className="muted">No sessions are catalogued on this machine yet.</p>
            ) : (
              <ul className="panel-rows">
                {data.corpus.harnesses.map((harness) => (
                  <li key={harness.harness}>
                    <div className="panel-row-main">
                      <span className="harness-badge">{harness.harness}</span>
                    </div>
                    <span className="panel-row-meta">
                      {harness.sessions} {harness.sessions === 1 ? "session" : "sessions"} ·{" "}
                      {harness.titled} titled
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          <Panel
            eyebrow="Candidate ideas"
            title="Hypothesis frontier"
            section={data.frontier}
            to="/hypotheses"
            linkLabel="Hypotheses"
          >
            <Facts
              items={[
                {
                  label: "Candidates",
                  value: data.frontier.truncated
                    ? `at least ${data.frontier.hypotheses}`
                    : data.frontier.hypotheses,
                  title: data.frontier.truncated
                    ? "Enumeration reached its bound, so this is a floor rather than a total."
                    : undefined,
                },
              ]}
            />
            <div className="panel-chips" aria-label="Candidates by exploration status">
              {data.frontier.statuses.map((entry) => (
                <span className="panel-chip" key={entry.status}>
                  <Badge label={entry.status} tone={statusTone(entry.status)} />
                  <span className="mono">{entry.count}</span>
                </span>
              ))}
            </div>
            <p className="panel-caption">
              Every status is listed, zeros included: rejected and deferred candidates are kept and
              stay visible. Novelty and priority order the frontier — ordering estimates only, never
              evidence.
            </p>
            {data.frontier.rows.length === 0 ? (
              <p className="muted">
                No exploration has recorded candidates yet. They appear here the moment a run
                persists them, before any sorting.
              </p>
            ) : (
              <ul className="panel-rows">
                {data.frontier.rows.map((row) => (
                  <li key={row.id}>
                    <div className="panel-row-main">
                      <Link className="panel-row-link untrusted-inline" to={`/hypotheses/${encodeURIComponent(row.id)}`}>
                        {row.statement}
                      </Link>
                      <span className="secondary mono">{row.id}</span>
                    </div>
                    <span className="panel-row-meta">
                      <Badge label={row.status} tone={statusTone(row.status)} />
                      <Relative at={row.created_at} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <FallibilityNote />
          </Panel>

          <Panel
            eyebrow="Waiting on a human"
            title="Review inbox"
            section={data.review}
            to="/review"
            linkLabel="Review"
            footer={
              <div className="panel-subsection">
                <div className="section-heading">
                  <div>
                    <p className="eyebrow">Reality ledger</p>
                    <h3>Question inbox</h3>
                  </div>
                  <Link className="panel-link" to="/reality">
                    Reality <span aria-hidden="true">→</span>
                  </Link>
                </div>
                {!data.review.questions.available ? (
                  <p className="panel-note" role="status">{data.review.questions.unavailable}</p>
                ) : data.review.questions.rows.length === 0 ? (
                  <p className="muted">No open questions. Nothing is waiting on an answer.</p>
                ) : (
                  <>
                    <ul className="panel-rows">
                      {data.review.questions.rows.map((row) => (
                        <li key={row.id}>
                          <div className="panel-row-main">
                            <Link className="panel-row-link untrusted-inline" to="/reality">
                              {row.prompt}
                            </Link>
                            <span className="secondary mono">{row.id}</span>
                          </div>
                          <span className="panel-row-meta">
                            <Badge label={row.state} tone="neutral" />
                            <span className="muted">{row.class}</span>
                            <span className="not-observed" title="The ledger's attention ranking. It orders the inbox and says nothing about whether an answer is true.">
                              rank {row.score}
                            </span>
                          </span>
                        </li>
                      ))}
                    </ul>
                    <p className="panel-caption">
                      Ordered by the ledger's own attention ranking — ordering estimates only, never
                      evidence. The Reality page shows each score's factors so the policy can be
                      argued with.
                    </p>
                  </>
                )}
              </div>
            }
          >
            <Facts
              items={[
                { label: "Awaiting a decision", value: data.review.awaiting },
                {
                  label: "Open questions",
                  value: data.review.questions.available ? data.review.questions.open : "—",
                  title: data.review.questions.available
                    ? "Ledger questions only an operator can move."
                    : data.review.questions.unavailable,
                },
              ]}
            />
            {data.review.rows.length === 0 ? (
              <p className="muted">
                Nothing awaits a decision. Records enter this queue when exploration develops them
                far enough for review.
              </p>
            ) : (
              <ul className="panel-rows">
                {data.review.rows.map((row) => (
                  <li key={`${row.type}-${row.id}`}>
                    <div className="panel-row-main">
                      <Link
                        className="panel-row-link untrusted-inline"
                        to={`/review/${encodeURIComponent(row.type)}/${encodeURIComponent(row.id)}`}
                      >
                        {row.excerpt || "Untitled record"}
                      </Link>
                      <span className="secondary mono">{row.id}</span>
                    </div>
                    <span className="panel-row-meta">
                      <Badge label={row.type} tone="neutral" />
                      <Badge label={row.status} tone={reviewTone(row.status)} />
                      <Relative at={row.enrolled_at} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          <Panel
            eyebrow="Receipts"
            title="Recent runs"
            section={data.runs}
            to="/explore"
            linkLabel="Explore"
          >
            <Facts items={[{ label: "Recorded runs", value: data.runs.total }]} />
            {data.runs.rows.length === 0 ? (
              <p className="muted">
                No exploration runs are recorded. A receipt appears here as soon as{" "}
                <code>babel explore</code> records one — including a failed run, which keeps its
                receipt too.
              </p>
            ) : (
              <ul className="panel-rows">
                {data.runs.rows.map((row) => (
                  <li key={row.receipt_id}>
                    <div className="panel-row-main">
                      <strong className="mono" title={row.receipt_id}>{row.receipt_id.slice(0, 14)}</strong>
                      <span className="secondary mono">
                        {row.recipes.length > 0
                          ? row.recipes.map((recipe) => `${recipe.id} v${recipe.version}`).join(", ")
                          : "recipe not recorded in the frontier"}
                      </span>
                    </div>
                    <span className="panel-row-meta">
                      <Badge label={row.sync} tone={row.sync === "committed" ? "green" : "amber"} />
                      <span>{row.retrievals} retrievals</span>
                      <span>{row.hypotheses} candidates</span>
                      {row.failures > 0 && <span>{row.failures} failures</span>}
                      {row.redactions > 0 && (
                        <span className="redaction-alert" title="Credential-shaped values were removed while building this receipt.">
                          {row.redactions} redactions
                        </span>
                      )}
                      <Relative at={row.recorded_at} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="panel-caption">
              Candidate counts come from the frontier and recipes from the observations a run
              recorded, because a receipt's header carries neither.
            </p>
          </Panel>

          <Panel
            eyebrow="Newest first"
            title="Recent activity"
            section={data.activity}
            to="/sessions"
            linkLabel="Sessions"
          >
            {data.activity.rows.length === 0 ? (
              <p className="muted">
                The catalog is empty. Sessions appear as Babel describes what this machine's
                harnesses have written.
              </p>
            ) : (
              <ul className="panel-rows">
                {data.activity.rows.map((row) => (
                  <li key={row.selector}>
                    <div className="panel-row-main">
                      <Link
                        className="panel-row-link untrusted-inline"
                        to={`/sessions/${encodeURIComponent(row.selector)}`}
                      >
                        {row.title ?? row.selector}
                      </Link>
                      {row.title !== null && <span className="secondary mono">{row.selector}</span>}
                    </div>
                    <span className="panel-row-meta">
                      <span className="harness-badge">{row.harness}</span>
                      {row.title === null ? (
                        <span className="not-observed" title="The catalog scan has not described this session yet.">
                          not described
                        </span>
                      ) : (
                        row.title_provenance && (
                          <span className="tag" title="Where this title came from: recorded by the harness, derived by Babel, or inferred by a model.">
                            {row.title_provenance}
                          </span>
                        )
                      )}
                      <Relative at={row.modified} />
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>
      )}
    </section>
  );
}

export default DashboardPage;
