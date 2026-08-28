import { useEffect, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  fetchSession,
  getSession,
  getTranscript,
  type FetchResult,
  type SessionDetail,
  type TranscriptEvent,
} from "../api";
import { errorMessage, formatBytes, formatTime } from "../format";

const TRANSCRIPT_PAGE_SIZE = 200;

function SessionPage() {
  const { selector: routeSelector } = useParams();
  const selector = routeSelector ?? "";
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [sessionError, setSessionError] = useState<string | null>(null);
  const [transcript, setTranscript] = useState<TranscriptEvent[]>([]);
  const [transcriptTotal, setTranscriptTotal] = useState(0);
  const [transcriptLoading, setTranscriptLoading] = useState(true);
  const [transcriptError, setTranscriptError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [snapshot, setSnapshot] = useState("");
  const [fetching, setFetching] = useState(false);
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [fetchResult, setFetchResult] = useState<FetchResult | null>(null);

  useEffect(() => {
    let live = true;
    setSession(null);
    setSessionError(null);
    setTranscript([]);
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

    getTranscript(selector, 0, TRANSCRIPT_PAGE_SIZE)
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

    return () => {
      live = false;
    };
  }, [selector]);

  async function loadMoreTranscript() {
    setLoadingMore(true);
    setTranscriptError(null);
    try {
      const page = await getTranscript(selector, transcript.length, TRANSCRIPT_PAGE_SIZE);
      setTranscript((current) => [...current, ...page.events]);
      setTranscriptTotal(page.total);
    } catch (reason) {
      setTranscriptError(errorMessage(reason));
    } finally {
      setLoadingMore(false);
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
        <div className="state-card error-state">
          <strong>Session could not be loaded.</strong>
          <span>{sessionError}</span>
        </div>
      </section>
    );
  }

  if (!session) {
    return <section className="page"><div className="state-card"><span className="spinner" /> Loading session…</div></section>;
  }

  const described = formatTime(session.described_at);
  const created = formatTime(session.created_at);
  const modified = formatTime(session.modified_at);
  const completeness = session.completeness ?? [];
  const artifacts = session.artifacts ?? [];
  const blobs = session.blobs ?? [];
  const unresolvedRefs = session.unresolved_blob_refs ?? [];

  return (
    <section className="page detail-page">
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

      <div className="detail-grid">
        <article className="card metadata-card">
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

        <aside className="card fetch-card">
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
        <article className="card warning-card">
          <div className="section-heading"><div><p className="eyebrow">Attention</p><h2>Unresolved blob references</h2></div></div>
          <p>These referenced blobs could not be resolved and may make recovery incomplete.</p>
          <ul className="mono-list">{unresolvedRefs.map((ref) => <li key={ref}>{ref}</li>)}</ul>
        </article>
      )}

      <article className="card transcript-card">
        <div className="section-heading">
          <div><p className="eyebrow">Conversation</p><h2>Transcript</h2></div>
          {!transcriptLoading && <span className="count-label">{transcript.length} of {transcriptTotal} events</span>}
        </div>
        {transcriptLoading && <div className="inline-state"><span className="spinner" /> Loading transcript…</div>}
        {transcriptError && transcript.length === 0 && <div className="inline-error" role="alert">Transcript could not be loaded: {transcriptError}</div>}
        {!transcriptLoading && !transcriptError && transcriptTotal === 0 && <div className="inline-state muted">No transcript events reported.</div>}
        <div className="transcript-events">
          {transcript.map((entry) => <TranscriptEntry key={entry.index} entry={entry} />)}
        </div>
        {transcriptError && transcript.length > 0 && <p className="inline-error" role="alert">More events could not be loaded: {transcriptError}</p>}
        {transcript.length < transcriptTotal && (
          <button type="button" className="load-more" onClick={loadMoreTranscript} disabled={loadingMore}>
            {loadingMore && <span className="spinner small" />}
            {loadingMore ? "Loading…" : `Load ${Math.min(TRANSCRIPT_PAGE_SIZE, transcriptTotal - transcript.length)} more`}
          </button>
        )}
      </article>
    </section>
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
    <article className="card file-card">
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

function TranscriptEntry({ entry }: { entry: TranscriptEvent }) {
  const raw = entry.kind.toLocaleLowerCase() === "raw" || entry.role.toLocaleLowerCase() === "raw";
  const role = raw ? "raw" : ["user", "assistant"].includes(entry.role.toLocaleLowerCase()) ? entry.role.toLocaleLowerCase() : "other";
  const timestamp = formatTime(entry.time);
  const heading = (
    <div className="event-heading">
      <span className={`role-label ${role}`}>{raw ? "raw" : entry.role || "other"}</span>
      <span className="kind-label">{entry.kind}</span>
      <span className="event-index">#{entry.index}</span>
      {timestamp && <time dateTime={entry.time ?? undefined} title={timestamp.absolute}>{timestamp.relative}</time>}
    </div>
  );
  if (raw) {
    return <details className="transcript-entry raw-entry"><summary>{heading}<span className="disclosure-label">Show raw entry</span></summary><pre>{entry.text}</pre></details>;
  }
  return <article className={`transcript-entry ${role}-entry`}>{heading}<pre>{entry.text}</pre></article>;
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
