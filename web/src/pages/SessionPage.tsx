import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  fetchSession,
  getSession,
  getSessionRow,
  getTranscript,
  type FetchResult,
  type SessionDetail,
  type SessionSummary,
  type TranscriptEvent,
} from "../api";
import { errorMessage, formatBytes, formatTime } from "../format";
import { Badge, unescapeWhitespace } from "../analysis";
import { RecordLinks } from "../references";
import { formatCount, formatUSD } from "./SessionsPage";
import "../sessions.css";

// The transcript is read as a moving window, not as a list with a cap.
//
// The bound is real and it is not the server's: one response materializes
// every event it carries, and a session here runs to ten thousand records of
// up to two thousand characters each, so a page that asked for all of them
// would hold tens of megabytes of untrusted text in the document. Raising the
// old constant would only move the wall.
//
// What was actually broken is that the window could only start at zero, so an
// evidence locator naming record 2073 was fifteen "load more" presses away and
// nothing said so. The window now starts where the reader is sent — see
// CITED_LEAD — and moves in both directions from there, which makes any cited
// position one request deep rather than one request per page before it.
const TRANSCRIPT_WINDOW = 250;

// How many records before a cited one the window opens on. A citation is read
// in its conversation: the lines that led to it are the difference between
// seeing the claim and seeing why the model made it.
const CITED_LEAD = 20;

// How long the records around a cited one stay stepped back. Long enough to
// be seen after the scroll settles, short enough that the conversation the
// citation belongs to is readable immediately afterwards — the context is the
// reason the window opens twenty records early.
const SPOTLIGHT_MS = 2_000;

// titleOriginLabel states a title's provenance in words rather than as a
// vocabulary token. The three values are not interchangeable claims — one is
// the harness's own record, one is babel's arithmetic over the session's
// records, one is a model's guess that cost money — and a detail page that
// printed the bare token would leave the reader to know that already.
//
// Null when there is no title: a provenance without a title names the origin
// of nothing, and Metadata renders a null as absent, which is the truth here.
function titleOriginLabel(title: string | null, provenance: string | null): string | null {
  if (!title) return null;
  switch (provenance) {
    case "recorded":
      return "recorded — the harness wrote this title in the session's own files";
    case "derived":
      return "derived — babel computed it offline from the session's records, with no model";
    case "inferred":
      return "inferred — a model wrote it, and session material was sent to a provider for it";
    default:
      return "unknown — nothing recorded where this title came from";
  }
}

function SessionPage() {
  const { selector: routeSelector } = useParams();
  const selector = routeSelector ?? "";
  const [params] = useSearchParams();
  // ?event=N is where an evidence locator lands. It is read as a record
  // position and nothing else: a value that is not a number names no record,
  // so it opens the transcript at its beginning rather than at a guess.
  const citedParam = params.get("event");
  const cited = citedParam !== null && /^\d+$/.test(citedParam) ? Number(citedParam) : null;
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [sessionError, setSessionError] = useState<string | null>(null);
  const [transcript, setTranscript] = useState<TranscriptEvent[]>([]);
  // The index of the window's first event, which is not zero when a citation
  // sent the reader into the middle of a conversation.
  const [windowStart, setWindowStart] = useState(0);
  const [transcriptTotal, setTranscriptTotal] = useState(0);
  const [transcriptLoading, setTranscriptLoading] = useState(true);
  const [transcriptError, setTranscriptError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [snapshot, setSnapshot] = useState("");
  const [fetching, setFetching] = useState(false);
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [fetchResult, setFetchResult] = useState<FetchResult | null>(null);
  // What the session cost. It is read from the listing row rather than from
  // the inspect document because that is where the catalog keeps it: the four
  // usage columns are summed at describe time and served with the listing,
  // which is answered from memory without touching a transcript.
  const [usage, setUsage] = useState<SessionSummary | null>(null);
  // Scrolling to the cited record happens once per arrival. A later "load
  // more" must not yank the reader back to where he came in.
  const landed = useRef<string | null>(null);
  // The two seconds after arrival, during which the records around the cited
  // one step back. It is state rather than a class written by hand because
  // the transcript re-renders while the window loads.
  const [spotlight, setSpotlight] = useState(false);

  const openAt = cited === null ? 0 : Math.max(0, cited - CITED_LEAD);

  useEffect(() => {
    let live = true;
    setSession(null);
    setUsage(null);
    setSessionError(null);
    setTranscript([]);
    setWindowStart(openAt);
    setTranscriptTotal(0);
    setTranscriptLoading(true);
    setTranscriptError(null);

    getSession(selector)
      .then((value) => {
        if (live) setSession(value);
      })
      .catch((reason) => {
        if (live) setSessionError(errorMessage(reason));
      });

    getTranscript(selector, openAt, TRANSCRIPT_WINDOW)
      .then((value) => {
        if (!live) return;
        setTranscript(value.events);
        setTranscriptTotal(value.total);
      })
      .catch((reason) => {
        if (live) setTranscriptError(errorMessage(reason));
      })
      .finally(() => {
        if (live) setTranscriptLoading(false);
      });

    getSessionRow(selector)
      .then((row) => {
        if (live) setUsage(row);
      })
      // A listing that could not be read costs the page its usage strip and
      // nothing else, so the failure is not raised to the reader: the strip
      // is a fact about the session, not the session.
      .catch(() => undefined);

    return () => {
      live = false;
    };
  }, [selector, openAt]);

  // The cited record is scrolled to after its window has rendered, and the
  // arrival is remembered by selector and position so that re-rendering the
  // list does not repeat it.
  useEffect(() => {
    if (cited === null || transcript.length === 0) return;
    const arrival = `${selector}#${cited}`;
    if (landed.current === arrival) return;
    const node = document.getElementById(`event-${cited}`);
    if (!node) return;
    landed.current = arrival;
    node.scrollIntoView({ block: "center" });
    // The dim is skipped outright when the reader asked for less motion. The
    // hero keeps its rule and its raise either way, so the answer to "which
    // line" never depends on an animation.
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    setSpotlight(true);
    const timer = window.setTimeout(() => setSpotlight(false), SPOTLIGHT_MS);
    return () => window.clearTimeout(timer);
  }, [cited, selector, transcript]);

  async function loadMoreTranscript() {
    setLoadingMore(true);
    setTranscriptError(null);
    try {
      const page = await getTranscript(selector, windowStart + transcript.length, TRANSCRIPT_WINDOW);
      setTranscript((current) => [...current, ...page.events]);
      setTranscriptTotal(page.total);
    } catch (reason) {
      setTranscriptError(errorMessage(reason));
    } finally {
      setLoadingMore(false);
    }
  }

  async function loadEarlierTranscript() {
    const start = Math.max(0, windowStart - TRANSCRIPT_WINDOW);
    setLoadingEarlier(true);
    setTranscriptError(null);
    try {
      const page = await getTranscript(selector, start, windowStart - start);
      setTranscript((current) => [...page.events, ...current]);
      setWindowStart(start);
      setTranscriptTotal(page.total);
    } catch (reason) {
      setTranscriptError(errorMessage(reason));
    } finally {
      setLoadingEarlier(false);
    }
  }

  async function submitFetch(event: FormEvent) {
    event.preventDefault();
    setFetching(true);
    setFetchError(null);
    setFetchResult(null);
    try {
      setFetchResult(await fetchSession(selector, snapshot));
    } catch (reason) {
      setFetchError(errorMessage(reason));
    } finally {
      setFetching(false);
    }
  }

  if (sessionError && !session) {
    return (
      <section className="page">
        <Link className="back-link" to="/sessions">← Sessions</Link>
        <div className="surface state-note error-state">
          <strong>Session could not be loaded.</strong>
          <span>{sessionError}</span>
        </div>
      </section>
    );
  }

  if (!session) {
    return <section className="page"><div className="surface state-note"><span className="spinner" /> Loading session…</div></section>;
  }

  const described = formatTime(session.described_at);
  const created = formatTime(session.created_at);
  const modified = formatTime(session.modified_at);
  const completeness = session.completeness ?? [];
  const artifacts = session.artifacts ?? [];
  const blobs = session.blobs ?? [];
  const unresolvedRefs = session.unresolved_blob_refs ?? [];

  return (
    <section className="page detail-page session-page">
      <Link className="back-link" to="/sessions">← Sessions</Link>
      <div className="page-heading detail-heading">
        <div>
          <div className="heading-badges">
            <span className="harness-badge">{session.harness}</span>
            <span className={session.continuation_grade ? "readiness ready" : "readiness partial"}>
              <span className={session.continuation_grade ? "grade-dot good" : "grade-dot partial"} />
              {session.continuation_grade ? "Continuation-ready" : "Partial metadata"}
            </span>
          </div>
          <h1>{session.title || "Untitled session"}</h1>
          <p className="subtitle mono">{session.selector}</p>
        </div>
      </div>

      {usage && <UsageStrip usage={usage} />}

      {/* A session's citations are almost entirely backlinks: an observation
          rests on a session, never the reverse, so this panel is where an
          operator finds what analysis was built on this conversation. It sits
          above the description because it is why a reader is here — the
          session's own file sizes are not.

          It is named by selector, and the server derives the durable key the
          edges were recorded against. */}
      <RecordLinks record={{ type: "session", id: session.selector }} heading="Analysis citing this session" />

      <div className="detail-grid">
        <article className="surface">
          <div className="section-heading">
            <div><p className="eyebrow">Description</p><h2>Metadata</h2></div>
          </div>
          <dl className="metadata-list">
            <Metadata label="Harness" value={session.harness} />
            <Metadata label="Source ID" value={session.source_id} mono />
            <Metadata label="Selector" value={session.selector} mono />
            <Metadata label="Primary path" value={session.primary_path} mono />
            <Metadata label="Primary size" value={formatBytes(session.primary_size)} />
            <Metadata label="Described" value={described ? `${described.relative} · ${described.absolute}` : session.described_at} />
            <Metadata label="Hint" value={session.hint} />
            <Metadata label="Title" value={session.title} />
            {/* Spelled out here rather than abbreviated to a mark: the list is
                where an operator comes to find out exactly what babel knows
                about one session, and "derived by babel" is the answer to
                "who named this?" that the sessions table only has room to
                hint at. */}
            <Metadata label="Title from" value={titleOriginLabel(session.title, session.title_provenance)} />
            <Metadata label="Workspace" value={session.workspace} mono />
            <Metadata label="Created" value={created ? `${created.relative} · ${created.absolute}` : null} />
            <Metadata label="Modified" value={modified ? `${modified.relative} · ${modified.absolute}` : null} />
            <Metadata label="Lifecycle" value={session.lifecycle} />
            <Metadata label="Continuation grade" value={session.continuation_grade ? "Yes" : "No"} />
            <Metadata label="Adapter metadata schema" value={String(session.adapter_metadata_schema)} />
          </dl>

          <h3>Repository fingerprint</h3>
          {session.repo ? (
            <dl className="metadata-list compact">
              <Metadata label="Remote" value={session.repo.remote} mono />
              <Metadata label="Commit" value={session.repo.commit} mono />
              <Metadata label="Branch" value={session.repo.branch} mono />
            </dl>
          ) : <p className="muted">No repository fingerprint reported.</p>}

          <h3>Completeness reasons</h3>
          {completeness.length ? (
            <div className="table-scroll inset-table">
              <table>
                <thead><tr><th>Field</th><th>Reason</th></tr></thead>
                <tbody>{completeness.map((item, index) => <tr key={`${item.field}-${index}`}><td className="mono">{item.field}</td><td>{item.reason}</td></tr>)}</tbody>
              </table>
            </div>
          ) : <p className="success-note">No completeness gaps reported.</p>}

          <details className="json-disclosure">
            <summary>Adapter metadata</summary>
            <pre>{session.adapter_metadata === undefined ? "No adapter metadata reported." : JSON.stringify(session.adapter_metadata, null, 2)}</pre>
          </details>
        </article>

        <aside className="surface fetch-form">
          <p className="eyebrow">Recovery</p>
          <h2>Fetch from archive</h2>
          <p className="muted">Materialize this session from an archived snapshot.</p>
          <form onSubmit={submitFetch}>
            <label>
              Snapshot <span className="muted">(optional)</span>
              <input value={snapshot} onChange={(event) => setSnapshot(event.target.value)} placeholder="latest or snapshot ID" />
            </label>
            <button type="submit" className="primary-button" disabled={fetching}>
              {fetching && <span className="spinner small" />}
              {fetching ? "Fetching…" : "Fetch session"}
            </button>
          </form>
          {fetchError && <p className="inline-error" role="alert">{fetchError}</p>}
          {fetchResult && <FetchOutcome result={fetchResult} />}
        </aside>
      </div>

      <FileTable
        title="Artifacts"
        subtitle="Files that form this session's closure."
        empty="No artifacts reported."
        headers={["Relative path", "Source path", "Size"]}
        rows={artifacts.map((artifact) => [artifact.rel_path, artifact.source_path, formatBytes(artifact.size)])}
      />

      <FileTable
        title="Blobs"
        subtitle="Resolved content-addressed references."
        empty="No resolved blobs reported."
        headers={["Digest", "Source path", "Size"]}
        rows={blobs.map((blob) => [blob.digest, blob.source_path, formatBytes(blob.size)])}
      />

      {unresolvedRefs.length > 0 && (
        <article className="surface warning-note">
          <div className="section-heading"><div><p className="eyebrow">Attention</p><h2>Unresolved blob references</h2></div></div>
          <p>These referenced blobs could not be resolved and may make recovery incomplete.</p>
          <ul className="mono-list">{unresolvedRefs.map((ref) => <li key={ref}>{ref}</li>)}</ul>
        </article>
      )}

      <article className="surface">
        <div className="section-heading">
          <div><p className="eyebrow">Conversation</p><h2>Transcript</h2></div>
          {!transcriptLoading && (
            <span className="count-label">
              {transcript.length === 0
                ? `${transcriptTotal} events`
                : `records ${windowStart + 1}–${windowStart + transcript.length} of ${transcriptTotal}`}
            </span>
          )}
        </div>
        {/* Arriving from a citation is stated, because the reader is looking at
            the middle of a conversation and the reason is not otherwise on the
            page. A cited record beyond the end is the more important case: it
            means the citation and this transcript disagree, which is a fact
            about the record and never something to round down silently. */}
        {cited !== null && !transcriptLoading && (
          <p className="muted">
            {cited < transcriptTotal ? (
              <>
                Opened at record <strong>#{cited + 1}</strong>, the line the citation names. The
                records before it are loaded for context.
              </>
            ) : (
              <>
                A citation names record <strong>#{cited + 1}</strong>, and this transcript holds{" "}
                {transcriptTotal}. The cited line is not in this file: the citation was recorded
                against different bytes, and its digest is what settles which.
              </>
            )}
          </p>
        )}
        {transcriptLoading && <div className="inline-state"><span className="spinner" /> Loading transcript…</div>}
        {transcriptError && transcript.length === 0 && <div className="inline-error" role="alert">Transcript could not be loaded: {transcriptError}</div>}
        {!transcriptLoading && !transcriptError && transcriptTotal === 0 && <div className="inline-state muted">No transcript events reported.</div>}
        {windowStart > 0 && (
          <button type="button" className="load-more" onClick={loadEarlierTranscript} disabled={loadingEarlier}>
            {loadingEarlier && <span className="spinner small" />}
            {loadingEarlier
              ? "Loading…"
              : `Load the ${Math.min(TRANSCRIPT_WINDOW, windowStart)} records before this`}
          </button>
        )}
        <div className={spotlight ? "transcript-events spotlight" : "transcript-events"}>
          {transcript.map((entry) => (
            <TranscriptEntry key={entry.index} entry={entry} cited={entry.index === cited} />
          ))}
        </div>
        {transcriptError && transcript.length > 0 && <p className="inline-error" role="alert">More events could not be loaded: {transcriptError}</p>}
        {windowStart + transcript.length < transcriptTotal && (
          <button type="button" className="load-more" onClick={loadMoreTranscript} disabled={loadingMore}>
            {loadingMore && <span className="spinner small" />}
            {loadingMore
              ? "Loading…"
              : `Load ${Math.min(TRANSCRIPT_WINDOW, transcriptTotal - windowStart - transcript.length)} more`}
          </button>
        )}
      </article>
    </section>
  );
}

// UsageStrip is what this session cost, at the top of its page.
//
// Four figures the harness recorded and the adapter summed: what was paid,
// how many tokens it took, how many assistant turns the conversation ran to,
// and how many tool calls came back as failures. A tool error is tinted only
// when there is one, because a zero there is a good answer and colour would
// make it an alarm.
//
// Every figure is nullable and a null renders as an absent measurement. Most
// harnesses record no usage at all, so the strip says "not recorded" rather
// than reporting a session that cost nothing.
function UsageStrip({ usage }: { usage: SessionSummary }) {
  const measured =
    usage.cost_usd !== null ||
    usage.total_tokens !== null ||
    usage.turns !== null ||
    usage.tool_errors !== null;
  return (
    <div className="surface panel session-usage">
      <div className="stat">
        <span className="stat-label">Cost</span>
        <strong className="stat-value">{usage.cost_usd === null ? "—" : formatUSD(usage.cost_usd)}</strong>
        <span className="stat-note">recorded by the harness</span>
      </div>
      <div className="stat">
        <span className="stat-label">Tokens</span>
        <strong className="stat-value" title={usage.total_tokens?.toLocaleString()}>
          {usage.total_tokens === null ? "—" : formatCount(usage.total_tokens)}
        </strong>
        <span className="stat-note">input, output and cache</span>
      </div>
      <div className="stat">
        <span className="stat-label">Turns</span>
        <strong className="stat-value">{usage.turns === null ? "—" : usage.turns.toLocaleString()}</strong>
        <span className="stat-note">assistant replies</span>
      </div>
      <div className="stat">
        <span className="stat-label">Tool errors</span>
        <strong className={usage.tool_errors ? "stat-value errors" : "stat-value"}>
          {usage.tool_errors === null ? "—" : usage.tool_errors.toLocaleString()}
        </strong>
        <span className="stat-note">failed tool results</span>
      </div>
      <p className="usage-note">
        {measured
          ? "Summed from the session's own transcript when Babel described it. No model was asked."
          : "This harness recorded no usage for the session. The dashes are absent measurements, not zeroes."}
      </p>
    </div>
  );
}

interface MetadataProps {
  label: string;
  value: string | null | undefined;
  mono?: boolean;
}

function Metadata({ label, value, mono }: MetadataProps) {
  return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{value || <span className="muted">—</span>}</dd></div>;
}

interface FileTableProps {
  title: string;
  subtitle: string;
  empty: string;
  headers: string[];
  rows: string[][];
}

function FileTable({ title, subtitle, empty, headers, rows }: FileTableProps) {
  return (
    <article className="surface file-block">
      <div className="section-heading"><div><h2>{title}</h2><p className="muted">{subtitle}</p></div><span className="count-label">{rows.length}</span></div>
      {rows.length ? (
        <div className="table-scroll">
          <table>
            <thead><tr>{headers.map((header) => <th key={header}>{header}</th>)}</tr></thead>
            <tbody>{rows.map((row, rowIndex) => <tr key={`${row[0]}-${rowIndex}`}>{row.map((cell, cellIndex) => <td className={cellIndex < 2 ? "mono" : "numeric mono"} key={headers[cellIndex]}>{cell}</td>)}</tr>)}</tbody>
          </table>
        </div>
      ) : <p className="muted">{empty}</p>}
    </article>
  );
}

// A "raw" event is a record internal/transcript declined to read as a message,
// and two thirds of a long session's records are that. They are not empty:
// they are tool calls, tool results, the harness's own notices, the injected
// reminders a model actually saw. Rendering them all as one collapsed "Show
// raw entry" row hid the half of the conversation an evidence locator is most
// likely to name.
//
// So the record's envelope is read here, at the display layer, and nothing is
// invented: the kind is the name the harness itself gave the record, the role
// is the role it recorded, and the body is the record's own content. A record
// whose shape this build does not recognize keeps its raw form rather than
// being described wrongly.
interface RawRecord {
  type?: unknown;
  customType?: unknown;
  role?: unknown;
  content?: unknown;
  message?: { role?: unknown; content?: unknown };
  payload?: { type?: unknown; role?: unknown; content?: unknown };
  data?: unknown;
}

interface RawView {
  role: string;
  kind: string;
  body: string;
  // True when the record on the page is the beginning of a longer one, which
  // the row has to say rather than presenting a fragment as the record.
  partial: boolean;
}

function asText(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// contentText renders one content part as the prose a reader wants from it: a
// tool call is its name and the arguments it was given, a result is its text.
// The part is stringified whole when its shape is unfamiliar, which keeps the
// bytes on the page instead of dropping a record nobody anticipated.
function contentText(part: unknown): string {
  if (typeof part === "string") return part;
  if (!part || typeof part !== "object") return "";
  const item = part as Record<string, unknown>;
  const text = asText(item.text) || asText(item.thinking) || asText(item.content);
  if (text) return text;
  const name = asText(item.name) || asText(item.toolName);
  if (name) {
    const args = item.arguments ?? item.args ?? item.input;
    const intent = asText(item.intent);
    return `${name}${args === undefined ? "" : ` ${JSON.stringify(args)}`}${intent ? `\n${intent}` : ""}`;
  }
  return JSON.stringify(item, null, 2);
}

// truncatedField reads one field out of a record's first bytes.
//
// It exists for the records internal/transcript had to cut: a 40-kilobyte tool
// result arrives as its first 2000 characters, which is not parseable JSON, and
// those are exactly the records an evidence locator is most likely to name. So
// the envelope's own fields — which a harness writes first, before the payload
// — are read off the prefix. This is a display fallback for text that is
// already known to be incomplete, never a way to read a whole record: a
// complete record is parsed.
function truncatedField(prefix: string, field: string): string {
  const match = prefix.match(new RegExp(`"${field}"\\s*:\\s*"([^"\\\\]{1,80})"`));
  return match ? match[1] : "";
}

function describeRaw(text: string): RawView | null {
  let record: RawRecord;
  try {
    record = JSON.parse(text) as RawRecord;
  } catch {
    const prefix = text.slice(0, 600);
    if (!prefix.startsWith("{")) return null;
    const kind = truncatedField(prefix, "customType") || truncatedField(prefix, "type");
    if (!kind) return null;
    const tool = truncatedField(prefix, "toolName") || truncatedField(prefix, "name");
    return {
      role: truncatedField(prefix, "role"),
      kind: tool ? `${kind} · ${tool}` : kind,
      body: text,
      partial: true,
    };
  }
  if (!record || typeof record !== "object") return null;
  const kind =
    asText(record.customType) || asText(record.payload?.type) || asText(record.type) || "record";
  const role = asText(record.message?.role) || asText(record.payload?.role) || asText(record.role);
  // `content` is checked at the top level too: an injected notice — the
  // reminders and advisories a model actually read — carries its text there
  // rather than inside a message envelope.
  const content = record.message?.content ?? record.payload?.content ?? record.content;
  let body = "";
  if (Array.isArray(content)) {
    body = content.map(contentText).filter((part) => part !== "").join("\n\n");
  } else if (typeof content === "string") {
    body = content;
  } else if (record.data !== undefined) {
    body = contentText(record.data) || JSON.stringify(record.data, null, 2);
  }
  if (!body) {
    // An envelope with no content of its own — a title change, a model
    // change — is its own fields, which are short and worth reading.
    body = JSON.stringify(record, null, 2);
  }
  return { role, kind, body, partial: false };
}

// How much decoded content renders open. Beyond it the row keeps its
// disclosure: a 2000-character tool result inlined for every one of 250
// records would bury the conversation it belongs to.
const INLINE_BODY_LIMIT = 800;

function TranscriptEntry({ entry, cited }: { entry: TranscriptEvent; cited: boolean }) {
  const unreadable = entry.kind.toLocaleLowerCase() === "raw" || entry.role.toLocaleLowerCase() === "raw";
  const decoded = unreadable ? describeRaw(entry.text) : null;
  const roleName = decoded?.role || (unreadable ? "" : entry.role);
  const role = unreadable && !decoded
    ? "raw"
    : ["user", "assistant"].includes(roleName.toLocaleLowerCase())
      ? roleName.toLocaleLowerCase()
      : "other";
  const kind = decoded?.kind ?? entry.kind;
  const body = unescapeWhitespace(decoded ? decoded.body : entry.text);
  const timestamp = formatTime(entry.time);
  // The cited record is the page's hero: the human sentence a proposal grew
  // from is what a citation sends a reader to, and it is the one record on the
  // page that has to be findable without reading the others.
  const shell = `transcript-entry ${cited ? "cited-hero " : ""}`;
  const heading = (
    <div className="event-heading">
      {/* A record the harness wrote no role for is the harness's own, and
          saying "record" is the truth; inventing a speaker for it would put
          words in somebody's mouth. */}
      <span className={`role-label ${role}`}>{roleName || (decoded ? "record" : "raw")}</span>
      <span className="kind-label">{kind}</span>
      {/* Records are numbered from one here and in the ?event= link, because a
          locator's line is 1-based and a reader comparing the two must not have
          to know that this list once counted from zero. */}
      <span className="event-index">#{entry.index + 1}</span>
      {cited && <Badge label="cited here" tone="violet" />}
      {timestamp && <time dateTime={entry.time ?? undefined} title={timestamp.absolute}>{timestamp.relative}</time>}
    </div>
  );
  if (!decoded && unreadable) {
    return (
      <details className={`${shell}raw-entry`} id={`event-${entry.index}`} open={cited}>
        <summary>
          {heading}
          <span className="disclosure-label">Show this record as it was stored</span>
        </summary>
        <pre>{body}</pre>
      </details>
    );
  }
  if (decoded?.partial || body.length > INLINE_BODY_LIMIT) {
    return (
      <details className={`${shell}${role}-entry`} id={`event-${entry.index}`} open={cited}>
        <summary>
          {heading}
          <span className="disclosure-label">
            {decoded?.partial
              ? "Longer than the transcript reader keeps — its first 2000 characters are here"
              : `${body.slice(0, 120)}…`}
          </span>
        </summary>
        <pre>{body}</pre>
      </details>
    );
  }
  return (
    <article className={`${shell}${role}-entry`} id={`event-${entry.index}`}>
      {heading}
      <pre>{body}</pre>
    </article>
  );
}

function FetchOutcome({ result }: { result: FetchResult }) {
  return (
    <div className="result-panel success-panel" role="status">
      <strong>{result.already_present ? "Already present" : "Fetch complete"}</strong>
      <dl>
        <div><dt>Snapshot</dt><dd className="mono">{result.snapshot_short_id || result.snapshot_id}</dd></div>
        <div><dt>Time</dt><dd>{formatTime(result.snapshot_time)?.absolute ?? result.snapshot_time}</dd></div>
        <div><dt>Target</dt><dd className="mono">{result.target}</dd></div>
        <div><dt>Recovered</dt><dd>{result.files} files · {formatBytes(result.bytes)}</dd></div>
      </dl>
      <PathDisclosure label="Included paths" paths={result.included} />
      <PathDisclosure label="Missing paths" paths={result.missing ?? []} warning />
    </div>
  );
}

function PathDisclosure({ label, paths, warning = false }: { label: string; paths: string[]; warning?: boolean }) {
  if (!paths.length) return null;
  return <details className={warning ? "path-disclosure warning" : "path-disclosure"}><summary>{label} ({paths.length})</summary><ul className="mono-list">{paths.map((path) => <li key={path}>{path}</li>)}</ul></details>;
}

export default SessionPage;
