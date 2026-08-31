import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  getOverview,
  type Overview,
  type OverviewReviewRow,
  type OverviewSection,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { AuthorityMark, Badge, FallibilityNote, statusTone, reviewTone, type Tone } from "../analysis";

// The landing page. It aggregates and it does not act: every number here is
// durable state one of the six services already held, read through the single
// /api/overview snapshot, and nothing on this page starts a run, invokes a
// model, or writes a record. A panel is a glance plus a way in — the totals,
// the few most recent rows, and the link to the page that owns them.
//
// Every panel answers a different question, so every panel looks different:
//
//   - Archive is a freshness read: when did a snapshot last land, per host.
//   - Corpus is a composition read: how much is described, of what.
//   - Frontier is a distribution read: how candidates spread across §4.2's
//     lifecycle. The full listing lives on the Hypotheses page; showing the
//     same rows here and in the review queue would render one fact twice.
//   - Review is the attention read: the records only a human can move. It is
//     the one panel that lists analytical records in full, because it is the
//     queue the operator works from.
//   - Runs is a receipt strip: what each exploration recorded, as a stub.
//   - Activity is a feed: what this machine's harnesses touched, newest first.
//
// Color is a status vocabulary, not decoration: hypothesis statuses keep the
// tones the Badge vocabulary already gives them, harnesses each hold one hue
// across the corpus bar and the activity feed, and freshness dots grade only
// the age of a timestamp — a fact, never an estimate.

// GUIDE_KEY records that the operator has seen the pointer to Help. The banner
// is one line and appears once: a first visit deserves an orientation, and a
// tour that reappeared would be an interface arguing with someone who already
// knows where things are. Storage is best-effort, exactly as the token
// bootstrap treats it — a locked-down browser shows the banner again rather
// than failing to render the dashboard.
const GUIDE_KEY = "babel.web.guide-dismissed";

type Flavor = "archive" | "corpus" | "frontier" | "review" | "runs" | "activity";

// One 12px stroke glyph per panel, drawn inline so nothing is fetched at
// runtime. The glyph sits in the eyebrow, small enough to aid scanning without
// becoming a decoration the eyebrow text merely captions.
const GLYPHS: Record<Flavor, ReactNode> = {
  archive: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <rect x="1.5" y="1.5" width="9" height="3" rx="0.8" />
      <path d="M2.5 4.5v4.2A1.8 1.8 0 0 0 4.3 10.5h3.4a1.8 1.8 0 0 0 1.8-1.8V4.5M4.7 6.8h2.6" />
    </svg>
  ),
  corpus: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <path d="M6 1.4 10.6 3.9 6 6.4 1.4 3.9Z M1.4 6.3 6 8.8l4.6-2.5M1.4 8.7 6 11.2l4.6-2.5" />
    </svg>
  ),
  frontier: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <circle cx="3" cy="2.7" r="1.3" />
      <circle cx="3" cy="9.3" r="1.3" />
      <circle cx="9.2" cy="6" r="1.3" />
      <path d="M3 4v4M4.2 3.4 8 5.4M4.2 8.6 8 6.6" />
    </svg>
  ),
  review: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <path d="M1.5 6.7 2.9 2.6a1 1 0 0 1 .9-.7h4.4a1 1 0 0 1 .9.7l1.4 4.1v2.7a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1Z" />
      <path d="M1.5 6.7h2.6l.7 1.4h2.4l.7-1.4h2.6" />
    </svg>
  ),
  runs: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <path d="M2.7 1.5h6.6v9l-1.6-1.1-1.7 1.1-1.7-1.1-1.6 1.1Z M4.6 4.1h2.8M4.6 6.1h2.8" />
    </svg>
  ),
  activity: (
    <svg viewBox="0 0 12 12" aria-hidden="true">
      <path d="M1 6.2h2.1L4.7 2.7l2.5 6.6 1.6-3.1H11" />
    </svg>
  ),
};

// Panel is the shared frame every tile uses: an eyebrow, a heading, the link to
// the page that owns the records, and either the body or the server's own note
// about why this section could not be read. The note is the server's wording
// rather than a generic failure line, because "no repository is configured" and
// "the frontier could not be read" call for different actions.
//
// The flavor names the panel's identity: it selects the accent hue, the glyph,
// and the grid area the panel composes into. Identity is the point — six tiles
// that all rendered eyebrow + rows were indistinguishable at a glance.
//
// footer renders whether or not this section is available, which is what makes
// the review panel's two halves independent: a machine can hold a Reality
// ledger and no review log, and the question inbox must not disappear because
// the panel it shares a tile with could not be read.
function Panel({
  flavor,
  eyebrow,
  title,
  section,
  to,
  linkLabel,
  children,
  footer,
}: {
  flavor: Flavor;
  eyebrow: string;
  title: string;
  section: OverviewSection;
  to: string;
  linkLabel: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <article className={`card panel panel--${flavor}`}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">
            {GLYPHS[flavor]}
            {eyebrow}
          </p>
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

// Facts is the small-print stat row for numbers that qualify rather than lead.
// Absence is rendered as an em dash rather than as a zero, because "no
// snapshots" and "not observed" are different facts.
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

// Hero is the number that leads a panel. Numerals were rendering at label size
// while captions carried the weight; a glance page has that exactly backwards.
function Hero({
  value,
  label,
  tone,
  title,
  dot,
  small,
}: {
  value: ReactNode;
  label: string;
  tone?: Tone;
  title?: string;
  dot?: Tone;
  small?: boolean;
}) {
  return (
    <div
      className={`stat-hero${tone ? ` tone-${tone}` : ""}${small ? " small" : ""}`}
      title={title}
    >
      <strong className="stat-value">
        {dot && <span className={`pulse-dot tone-${dot}`} aria-hidden="true" />}
        {value}
      </strong>
      <span className="stat-label">{label}</span>
    </div>
  );
}

function Relative({ at }: { at: string | null | undefined }) {
  const time = formatTime(at);
  if (!time) return <span className="muted">—</span>;
  return <span title={time.absolute}>{time.relative}</span>;
}

// freshnessTone grades only the age of a timestamp, which is a fact this page
// read, never an estimate: a day-old snapshot is green, a week-old one amber,
// an older one red — because an archive nothing has pushed to for weeks is
// exactly what this panel exists to make visible. Unknown stays neutral.
function freshnessTone(at: string | null | undefined): Tone {
  if (!at) return "neutral";
  const age = Date.now() - new Date(at).getTime();
  if (!Number.isFinite(age)) return "neutral";
  if (age <= 86_400_000) return "green";
  if (age <= 7 * 86_400_000) return "amber";
  return "red";
}

// Each harness holds one hue everywhere on this page — the corpus composition
// bar, its legend, and the activity feed — so "which harness is this" is a
// color read before it is a text read. Unknown harnesses hash onto the spare
// hues rather than all falling into one bucket.
const HARNESS_TONES: Record<string, Tone> = {
  codex: "blue",
  omp: "green",
  claude: "amber",
  "claude-code": "amber",
  gemini: "cyan",
};

const HARNESS_FALLBACK: Tone[] = ["cyan", "violet", "red"];

function harnessTone(harness: string): Tone {
  const known = HARNESS_TONES[harness];
  if (known) return known;
  let hash = 0;
  for (let index = 0; index < harness.length; index += 1) {
    hash = (hash * 31 + harness.charCodeAt(index)) >>> 0;
  }
  return HARNESS_FALLBACK[hash % HARNESS_FALLBACK.length];
}

// DistributionBar renders counts as proportional segments. It draws counts the
// page also states as numbers — never a grading, never a confidence, which §10
// keeps as words. flex-grow does the arithmetic; a populated segment keeps a
// minimum width so a single candidate does not vanish at panel width.
function DistributionBar({
  segments,
  label,
}: {
  segments: Array<{ key: string; count: number; tone: Tone }>;
  label: string;
}) {
  const populated = segments.filter((segment) => segment.count > 0);
  return (
    <div className={`dist-bar${populated.length === 0 ? " empty" : ""}`} role="img" aria-label={label}>
      {populated.map((segment) => (
        <span
          key={segment.key}
          className={`dist-seg tone-${segment.tone}`}
          style={{ flexGrow: segment.count }}
          title={`${segment.key}: ${segment.count}`}
        />
      ))}
    </div>
  );
}

// Meter is a filled proportion of two counts the page also states in words.
function Meter({ value, max, tone }: { value: number; max: number; tone: Tone }) {
  const share = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  return (
    <span className="meter" role="img" aria-label={`${value} of ${max}`}>
      <span className={`meter-fill tone-${tone}`} style={{ width: `${share}%` }} />
    </span>
  );
}

function subjectTone(type: string): Tone {
  switch (type) {
    case "hypothesis":
      return "violet";
    case "finding":
      return "cyan";
    case "proposal":
      return "green";
    default:
      return "neutral";
  }
}

// QueueRow is one record awaiting a decision. The glance is deliberately
// small — a type glyph, two clamped lines, an age — and the disclosure button
// reveals what exceeds glance value: the full statement and the identifier.
// Expansion is visual only; the full wording is in the document either way,
// and the title link is the navigation, so disclosure never hijacks a click
// that meant "open this record".
function QueueRow({ row }: { row: OverviewReviewRow }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <li className={expanded ? "expanded" : undefined}>
      <span className={`subject-glyph tone-${subjectTone(row.type)}`} aria-hidden="true">
        {row.type.charAt(0).toUpperCase()}
      </span>
      <div className="panel-row-main">
        <Link
          className="panel-row-link untrusted-inline"
          to={`/review/${encodeURIComponent(row.type)}/${encodeURIComponent(row.id)}`}
        >
          {row.excerpt || "Untitled record"}
        </Link>
        {expanded && <span className="secondary mono">{row.id}</span>}
        <span className="panel-row-meta">
          <span className="row-kind">{row.type}</span>
          {/* Every queued record is new by construction; a row of identical
              "new" chips said nothing. A status that differs still shows. */}
          {row.status !== "new" && <Badge label={row.status} tone={reviewTone(row.status)} />}
          <Relative at={row.enrolled_at} />
        </span>
      </div>
      <button
        type="button"
        className="disclose-button"
        aria-expanded={expanded}
        aria-label={
          expanded
            ? "Collapse this record's full statement"
            : "Show this record's full statement and identifier"
        }
        onClick={() => setExpanded((value) => !value)}
      >
        <svg viewBox="0 0 12 12" aria-hidden="true">
          <path d="M2.5 4.5 6 8l3.5-3.5" />
        </svg>
      </button>
    </li>
  );
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

  // The receipt strip composes as one row, never a stub marooned on a ragged
  // second line beside three tile-widths of void: the list measures how many
  // stub widths fit and folds the rest into the "+N more in Explore" link.
  // 13rem mirrors the flex-basis .receipt-list li declares in styles.css.
  const receiptListRef = useRef<HTMLUListElement | null>(null);
  const [receiptFit, setReceiptFit] = useState(Number.POSITIVE_INFINITY);
  const receiptCount = data?.runs.available ? data.runs.rows.length : 0;
  useLayoutEffect(() => {
    const list = receiptListRef.current;
    if (!list) return;
    const measure = () => {
      const gap = parseFloat(window.getComputedStyle(list).columnGap) || 0;
      const stub = parseFloat(window.getComputedStyle(document.documentElement).fontSize) * 13 || 208;
      setReceiptFit(Math.max(1, Math.floor((list.clientWidth + gap) / (stub + gap))));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(list);
    return () => observer.disconnect();
  }, [receiptCount]);

  // The spotlight is the frontier's one glimpse of a statement, and the
  // review queue is the panel that lists statements in full — so the
  // spotlight prefers the newest candidate the queue does not already show.
  // When every recent candidate awaits review, the frontier says so instead
  // of repeating a statement the same screen renders two panels away.
  const queued = new Set(data?.review.rows.map((row) => row.id) ?? []);
  const newest = data?.frontier.rows.find((row) => !queued.has(row.id));
  const frontierListed = (data?.frontier.rows.length ?? 0) > 0;
  const receiptRows = data?.runs.rows.slice(0, receiptFit) ?? [];

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
            flavor="archive"
            eyebrow="Restic repository"
            title="Archive health"
            section={data.archive}
            to="/archive"
            linkLabel="Archive"
          >
            {/* Freshness leads: "when did a snapshot last land" is the fact an
                operator glances for, so it renders first and carries the age-
                graded dot. The totals follow it. */}
            <div className="hero-strip">
              <Hero
                value={<Relative at={data.archive.latest_time} />}
                label="newest snapshot"
                dot={freshnessTone(data.archive.latest_time)}
                small
              />
              <Hero value={data.archive.snapshots} label="snapshots" />
              <Hero value={data.archive.hosts_total} label="hosts" small />
            </div>
            <Facts
              items={[
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
              <ul className="panel-rows host-list">
                {data.archive.hosts.map((host) => (
                  <li key={host.host}>
                    <span
                      className={`pulse-dot tone-${freshnessTone(host.latest_time)}`}
                      aria-hidden="true"
                    />
                    <div className="panel-row-main">
                      <strong className="untrusted-inline">{host.host}</strong>
                      <span className="panel-row-meta">
                        <span className="mono">{host.latest_short_id || "no snapshot"}</span>
                        <span>
                          {host.snapshots} {host.snapshots === 1 ? "snapshot" : "snapshots"}
                        </span>
                        <Relative at={host.latest_time} />
                      </span>
                    </div>
                  </li>
                ))}
              </ul>
            )}
            <p className="panel-caption mono-caption panel-bottom">
              {data.archive.repository || "no repository"} · host {data.archive.host_id || "unknown"}
            </p>
          </Panel>

          <Panel
            flavor="corpus"
            eyebrow="Local catalog"
            title="Corpus"
            section={data.corpus}
            to="/sessions"
            linkLabel="Sessions"
          >
            <div className="hero-strip">
              <Hero value={data.corpus.sessions} label="sessions" />
              <Hero value={data.corpus.titled} label="titled" />
              <Hero
                value={data.corpus.pending}
                label="awaiting description"
                title="Sessions the running catalog scan has not described yet."
                small
              />
            </div>
            {/* Two proportions of the same corpus: how much of it is titled,
                and which harnesses it came from. Both are counts the captions
                also state — filled bars, because an outline that carried no
                fill read as an empty track. */}
            <div className="coverage-line">
              <Meter value={data.corpus.titled} max={data.corpus.sessions} tone="cyan" />
              <span className="coverage-label">
                {data.corpus.titled} of {data.corpus.sessions} titled
              </span>
            </div>
            {/* A scan over nothing has nothing to narrate: the line renders
                only when the scan actually holds sessions to describe. */}
            {data.corpus.scan.running && data.corpus.scan.total > 0 && (
              <p className="panel-caption scan-line" role="status">
                <span className="spinner small" />
                Describing {data.corpus.scan.described} of {data.corpus.scan.total} sessions. The
                Sessions page follows the scan; this page shows what the catalog already holds.
              </p>
            )}
            {data.corpus.harnesses.length === 0 ? (
              <p className="muted">No sessions are catalogued on this machine yet.</p>
            ) : (
              <>
                <DistributionBar
                  label={`Sessions by harness: ${data.corpus.harnesses
                    .map((harness) => `${harness.harness} ${harness.sessions}`)
                    .join(", ")}`}
                  segments={data.corpus.harnesses.map((harness) => ({
                    key: harness.harness,
                    count: harness.sessions,
                    tone: harnessTone(harness.harness),
                  }))}
                />
                <ul className="corpus-legend">
                  {data.corpus.harnesses.map((harness) => (
                    <li key={harness.harness}>
                      <span
                        className={`swatch tone-${harnessTone(harness.harness)}`}
                        aria-hidden="true"
                      />
                      <span className="legend-name mono">{harness.harness}</span>
                      <span className="legend-count">
                        {harness.sessions} {harness.sessions === 1 ? "session" : "sessions"}
                      </span>
                      <Meter
                        value={harness.titled}
                        max={harness.sessions}
                        tone={harnessTone(harness.harness)}
                      />
                      <span className="legend-count">{harness.titled} titled</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
            <p className="panel-caption panel-bottom">
              Titles: {data.corpus.recorded} recorded by the harness, {data.corpus.derived} derived
              from the session's own records, {data.corpus.inferred} inferred by a model. The three
              are different kinds of claim, so they are counted separately.
            </p>
          </Panel>

          <Panel
            flavor="frontier"
            eyebrow="Candidate ideas"
            title="Hypothesis frontier"
            section={data.frontier}
            to="/hypotheses"
            linkLabel="Hypotheses"
          >
            <div className="hero-strip">
              <Hero
                value={data.frontier.hypotheses}
                label={data.frontier.truncated ? "candidates at least" : "candidates"}
                title={
                  data.frontier.truncated
                    ? "Enumeration reached its bound, so this is a floor rather than a total."
                    : undefined
                }
              />
            </div>
            {/* The frontier is a shape, not a list: how candidates distribute
                across the lifecycle. The full listing is the Hypotheses page's;
                the records a human must act on are the review queue's. Only the
                newest candidate renders here in the model's own words, as the
                one glimpse of what exploration just produced. */}
            <DistributionBar
              label={`Candidates by status: ${data.frontier.statuses
                .map((entry) => `${entry.status} ${entry.count}`)
                .join(", ")}`}
              segments={data.frontier.statuses.map((entry) => ({
                key: entry.status,
                count: entry.count,
                tone: statusTone(entry.status),
              }))}
            />
            <div className="panel-chips" aria-label="Candidates by exploration status">
              {data.frontier.statuses.map((entry) => (
                <span className={`panel-chip${entry.count === 0 ? " zero" : ""}`} key={entry.status}>
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
            {newest ? (
              <div className="spotlight">
                <p className="spotlight-eyebrow">Newest candidate</p>
                <Link
                  className="panel-row-link untrusted-inline"
                  to={`/hypotheses/${encodeURIComponent(newest.id)}`}
                >
                  {newest.statement}
                </Link>
                <span className="panel-row-meta">
                  <Badge label={newest.status} tone={statusTone(newest.status)} />
                  <Relative at={newest.created_at} />
                </span>
              </div>
            ) : frontierListed ? (
              <p className="quiet-line">
                <span className="quiet-mark" aria-hidden="true">✓</span>
                Every recent candidate is awaiting review — the inbox on this page holds their
                statements.
              </p>
            ) : (
              <p className="muted">
                No exploration has recorded candidates yet. They appear here the moment a run
                persists them, before any sorting.
              </p>
            )}
            <div className="panel-bottom">
              <FallibilityNote />
            </div>
          </Panel>

          <Panel
            flavor="review"
            eyebrow="Waiting on a human"
            title="Review inbox"
            section={data.review}
            to="/review"
            linkLabel="Review"
            footer={
              data.review.questions.available && data.review.questions.rows.length === 0 ? (
                // A settled inbox is one line — the ledger's name, the fact,
                // and the way in on a shared baseline. A heading over an empty
                // band read as dead space, not as an answer.
                <div className="panel-subsection subsection-settled">
                  <p className="eyebrow">Reality ledger</p>
                  <p className="quiet-line">
                    <span className="quiet-mark" aria-hidden="true">✓</span>
                    No open questions. Nothing is waiting on an answer.
                  </p>
                  <Link className="panel-link" to="/reality">
                    Reality <span aria-hidden="true">→</span>
                  </Link>
                </div>
              ) : (
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
                ) : (
                  <>
                    <ul className="panel-rows question-rows">
                      {data.review.questions.rows.map((row) => (
                        <li key={row.id}>
                          <div className="panel-row-main">
                            <Link className="panel-row-link untrusted-inline" to="/reality">
                              {row.prompt}
                            </Link>
                            <span className="panel-row-meta">
                              <Badge label={row.state} tone="neutral" />
                              <span className="muted">{row.class}</span>
                              <span className="not-observed" title="The ledger's attention ranking. It orders the inbox and says nothing about whether an answer is true.">
                                rank {row.score}
                              </span>
                            </span>
                          </div>
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
              )
            }
          >
            <div className="hero-strip">
              <Hero
                value={data.review.awaiting}
                label="awaiting a decision"
                tone={data.review.awaiting > 0 ? "amber" : undefined}
              />
              <Hero
                value={data.review.questions.available ? data.review.questions.open : "—"}
                label="open questions"
                title={
                  data.review.questions.available
                    ? "Ledger questions only an operator can move."
                    : data.review.questions.unavailable
                }
                small
              />
              {/* #87's proposed next actions. It is a third number rather than
                  folded into "awaiting a decision" because the two are
                  different questions: one is a verdict on a record, the other
                  authorizes an action about it, and an operator who saw one
                  total could not tell how many of each are waiting. */}
              <Hero
                value={data.review.dispositions.available ? data.review.dispositions.pending : "—"}
                label="proposed actions"
                title={
                  data.review.dispositions.available
                    ? "Next actions a run proposed. Authorizing one records that you authorized it; Babel performs none of them."
                    : data.review.dispositions.unavailable
                }
                small
              />
            </div>
            {data.review.rows.length === 0 ? (
              <p className="quiet-line">
                <span className="quiet-mark" aria-hidden="true">✓</span>
                Nothing awaits a decision. Records enter this queue when exploration develops them
                far enough for review.
              </p>
            ) : (
              <>
                <ul className="panel-rows queue-list">
                  {data.review.rows.map((row) => (
                    <QueueRow key={`${row.type}-${row.id}`} row={row} />
                  ))}
                </ul>
                {data.review.awaiting > data.review.rows.length && (
                  <p className="panel-caption">
                    <Link className="more-link" to="/review">
                      {data.review.awaiting - data.review.rows.length} more await in Review{" "}
                      <span aria-hidden="true">→</span>
                    </Link>
                  </p>
                )}
              </>
            )}
          </Panel>

          <Panel
            flavor="runs"
            eyebrow="Receipts"
            title="Recent runs"
            section={data.runs}
            to="/explore"
            linkLabel="Explore"
          >
            {data.runs.rows.length === 0 ? (
              <p className="muted">
                No exploration runs are recorded. A receipt appears here as soon as{" "}
                <code>babel explore</code> records one — including a failed run, which keeps its
                receipt too.
              </p>
            ) : (
              <div className="receipt-strip">
                <div className="receipt-lead">
                  <Hero value={data.runs.total} label="recorded runs" />
                  {data.runs.total > receiptRows.length && (
                    <Link className="more-link receipt-more" to="/explore">
                      +{data.runs.total - receiptRows.length} more in Explore{" "}
                      <span aria-hidden="true">→</span>
                    </Link>
                  )}
                </div>
                <ul className="panel-rows receipt-list" ref={receiptListRef}>
                  {receiptRows.map((row) => (
                    <li key={row.receipt_id}>
                      <span className="receipt-head">
                        <span
                          className={`sync-dot tone-${row.sync === "committed" ? "green" : "amber"}`}
                          aria-hidden="true"
                        />
                        <span className="receipt-sync">{row.sync}</span>
                        <span className="receipt-time">
                          <Relative at={row.recorded_at} />
                        </span>
                      </span>
                      <AuthorityMark authority={row.authority} />
                      <strong className="mono receipt-id" title={row.receipt_id}>
                        {row.receipt_id}
                      </strong>
                      <span className="receipt-recipe untrusted-inline">
                        {row.recipes.length > 0
                          ? row.recipes.map((recipe) => `${recipe.id} v${recipe.version}`).join(", ")
                          : "recipe not recorded in the frontier"}
                      </span>
                      <span className="receipt-counts">
                        <span className="count-cell">
                          <strong>{row.retrievals}</strong>
                          <small>retrieved</small>
                        </span>
                        <span className="count-cell">
                          <strong>{row.hypotheses}</strong>
                          <small>candidates</small>
                        </span>
                        {row.failures > 0 && (
                          <span className="count-cell tone-red">
                            <strong>{row.failures}</strong>
                            <small>failed</small>
                          </span>
                        )}
                        {row.redactions > 0 && (
                          <span
                            className="count-cell tone-amber"
                            title="Credential-shaped values were removed while building this receipt."
                          >
                            <strong>{row.redactions}</strong>
                            <small>redacted</small>
                          </span>
                        )}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <p className="panel-caption panel-bottom">
              Candidate counts come from the frontier and recipes from the observations a run
              recorded, because a receipt's header carries neither. The authority is the header's
              own: it says why the run happened, not whether what it found is true.
            </p>
          </Panel>

          <Panel
            flavor="activity"
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
              <ul className="panel-rows feed-list">
                {data.activity.rows.map((row) => (
                  <li key={row.selector}>
                    <span
                      className={`feed-dot tone-${harnessTone(row.harness)}`}
                      aria-hidden="true"
                    />
                    <div className="panel-row-main">
                      <Link
                        className="panel-row-link untrusted-inline"
                        to={`/sessions/${encodeURIComponent(row.selector)}`}
                        title={row.selector}
                      >
                        {row.title ?? row.selector}
                      </Link>
                      <span className="panel-row-meta">
                        <span className={`harness-name tone-${harnessTone(row.harness)}`}>
                          {row.harness}
                        </span>
                        {row.title === null ? (
                          <span className="not-observed" title="The catalog scan has not described this session yet.">
                            not described
                          </span>
                        ) : (
                          row.title_provenance && (
                            <span className="provenance" title="Where this title came from: recorded by the harness, derived by Babel, or inferred by a model.">
                              {row.title_provenance}
                            </span>
                          )
                        )}
                        <Relative at={row.modified} />
                      </span>
                    </div>
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
