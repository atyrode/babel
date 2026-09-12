import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { errorMessage, formatBytes, formatTime } from "../format";
import { Badge, Quoted } from "../analysis";
import {
  ABSENT,
  RUN_KIND_LABELS,
  count,
  dollars,
  getWatchRun,
  seconds,
  type RetrievalStep,
  type RunDetail,
} from "../watchapi";
import "../watch.css";

// One run, whole (SPEC.md §7).
//
// A receipt is the only durable account of what a run actually did, and until
// this page existed the surface showed five numbers off its header: retrievals,
// deferrals, failures, redactions, and when it was written. Everything that
// makes a run reviewable was in the body — the queries it ran and what came
// back, the public documents it fetched, the candidates it declined and why,
// what it cost and what it consumed, and which versions of which policies were
// in force — and the body never reached the wire.
//
// So this page is the receipt read as a story, in the order the questions
// occur: what was it asked, what did it read, what did it fetch, what did it
// produce, what did it decline, what went wrong, what did it cost, and which
// versions ran it. Observatory register (§8.6): mono figures, dense rows, no
// prose where a number will do.
//
// The one rule every section shares is the receipt's own: an unrecorded figure
// is absent. An empty section says the receipt recorded none, which is a
// different statement from a zero — a run that recorded no failures and a
// receipt written before failures were recorded must not read the same.

// RetrievalTrace is one search and what came back.
//
// The hits are shown rather than counted because the count was already on the
// old page and answered nothing: "14 retrievals" cannot tell the operator
// whether the run searched for the right thing. The query can, and the locator
// beside each hit is how the excerpt is recovered.
//
// Rank is deliberately not rendered as a score. §5.4 forbids retrieval rank
// from becoming evidence strength, so the hits appear in the order the run saw
// them and carry no number.
function RetrievalTrace({ step }: { step: RetrievalStep }) {
  const at = formatTime(step.at);
  const results = step.results ?? [];
  const records = step.records ?? [];

  return (
    <li className="run-step">
      <div className="run-step-head">
        <span className="run-step-index mono">#{step.index}</span>
        <span className="run-step-tool mono">{step.tool}</span>
        {step.scope && <Badge label={step.scope} tone="cyan" />}
        {at && (
          <time dateTime={step.at} title={at.absolute}>
            {at.relative}
          </time>
        )}
      </div>
      <p className="run-step-query">{step.query}</p>
      {results.length === 0 && records.length === 0 ? (
        <p className="muted">
          {step.results ? "Nothing matched." : "The receipt does not record what came back."}
        </p>
      ) : (
        <ul className="run-hits">
          {results.map((result, index) => {
            const locator = result.evidence?.locator;
            const note = result.evidence?.note;
            return (
              <li key={`${locator?.path ?? "hit"}-${index}`}>
                <span className="evidence-locator mono">
                  {locator?.path ?? "no locator recorded"}
                  {locator?.line != null && locator.line > 0 ? `:${locator.line}` : ""}
                </span>
                {note && <Quoted label="The run's own note — untrusted, bounded" text={note} />}
              </li>
            );
          })}
          {records.map((id) => (
            <li key={id}>
              {/* A frontier record is addressed by its id, which is the whole
                  of its address, so a self-retrieval's hit is a link to the
                  record rather than a file locator. */}
              <Link className="mono" to={`/r/${encodeURIComponent(id)}`}>
                {id}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function RunPage() {
  const { id = "" } = useParams<{ id: string }>();
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getWatchRun(id)
      .then((value) => setRun(value))
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [id]);

  useEffect(load, [load]);

  if (loading && !run) {
    return (
      <section className="page run-page">
        <div className="surface state-note">
          <span className="spinner" /> Reading the receipt…
        </div>
      </section>
    );
  }

  if (error && !run) {
    return (
      <section className="page run-page">
        <div className="surface state-note error-state">
          <strong>This receipt could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>
            Try again
          </button>
          <Link to="/watch">← Back to Watch</Link>
        </div>
      </section>
    );
  }

  if (!run) return null;

  const recorded = formatTime(run.recorded_at);
  const started = formatTime(run.timing?.started_at);
  const finished = formatTime(run.timing?.finished_at);
  const retrieval = run.retrieval ?? [];
  const research = run.research ?? [];
  const candidates = run.candidates ?? [];
  const failures = run.failures ?? [];
  const outputs = run.outputs ?? [];
  const cookbook = run.cookbook ?? [];
  const capability = run.versions?.capability;
  const job = run.versions?.job;
  const policy = run.versions?.policy;

  return (
    <section className="page run-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">
            <Link to="/watch">Watch</Link> › Run
          </p>
          <h1 className="mono">{run.run_id}</h1>
          <p className="run-subhead">
            {RUN_KIND_LABELS[run.kind ?? ""] ?? "Kind not recorded"}
            {recorded && (
              <>
                {" · receipt written "}
                <time dateTime={run.recorded_at} title={recorded.absolute}>
                  {recorded.relative}
                </time>
              </>
            )}
            {run.revision != null && run.revision > 1 ? ` · revision ${run.revision}` : ""}
          </p>
        </div>
        <div className="run-figures">
          <div className="stat">
            <span className="stat-label">cost</span>
            <strong className="stat-value">{dollars(run.usage?.cost_usd)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">duration</span>
            <strong className="stat-value">{seconds(run.timing?.duration_s)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">outputs</span>
            <strong className="stat-value">{run.outputs ? count(outputs.length) : ABSENT}</strong>
          </div>
        </div>
      </div>

      {/* What it was asked. The authority is why the run happened at all, and
          the cookbook set is what it was told to do: a receipt records the whole
          applied set with versions rather than one titled recipe, so the set is
          what appears. */}
      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Asked</p>
            <h2>What it was asked to do</h2>
          </div>
        </div>
        <dl className="run-facts">
          <div>
            <dt>Authority</dt>
            <dd>
              {run.authority?.kind ? (
                <>
                  <span>{run.authority.kind}</span>
                  {run.authority.ref && <span className="mono run-ref">{run.authority.ref}</span>}
                </>
              ) : (
                <span className="muted">Written before receipts recorded one.</span>
              )}
            </dd>
          </div>
          <div>
            <dt>Profile</dt>
            <dd>{run.usage?.profile ? <span className="mono">{run.usage.profile}</span> : <span className="muted">{ABSENT}</span>}</dd>
          </div>
          <div>
            <dt>Model</dt>
            <dd>{run.usage?.model ? <span className="mono">{run.usage.model}</span> : <span className="muted">{ABSENT}</span>}</dd>
          </div>
          <div>
            <dt>Corpus scope</dt>
            <dd>
              {run.preparation_id ? (
                <span className="mono">{run.preparation_id}</span>
              ) : (
                <span className="muted">No preparation recorded.</span>
              )}
            </dd>
          </div>
          <div>
            <dt>Receipt</dt>
            <dd>
              <span className="mono">{run.receipt_id}</span>
              {run.supersedes && <span className="secondary mono">supersedes {run.supersedes}</span>}
            </dd>
          </div>
        </dl>

        {/* The applied set, with versions. A receipt records every cookbook
            asset that was in force rather than one titled recipe, and stores no
            title for any of them — so this is identifiers and versions, and not
            a table with a permanently blank title column. */}
        {cookbook.length === 0 ? (
          <p className="muted run-recipes-none">No cookbook asset is recorded for this run.</p>
        ) : (
          <ul className="run-recipes">
            {cookbook.map((asset) => (
              <li key={`${asset.id}-${asset.version ?? 0}`}>
                <span className="mono">{asset.id}</span>
                {asset.version != null && <span className="secondary">v{asset.version}</span>}
                {asset.kind && <Badge label={asset.kind} tone={asset.kind === "lens" ? "cyan" : "neutral"} />}
              </li>
            ))}
          </ul>
        )}
      </article>

      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Read</p>
            <h2>What it searched for</h2>
          </div>
          {run.retrieval && <span className="count-label">{retrieval.length}</span>}
        </div>
        {!run.retrieval ? (
          <p className="muted">This receipt does not record what it searched for.</p>
        ) : retrieval.length === 0 ? (
          <p className="muted">It searched for nothing.</p>
        ) : (
          <ol className="run-steps">
            {retrieval.map((step) => (
              <RetrievalTrace key={`${step.index}-${step.at}`} step={step} />
            ))}
          </ol>
        )}
      </article>

      {/* What it fetched. A fetched document is addressed by URL and digest,
          and the content is deliberately not in the receipt: the digest is what
          makes the citation checkable, and a stored copy of the public web
          would be a store the operator never asked for. */}
      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Fetched</p>
            <h2>What it read from outside</h2>
          </div>
          {run.research && <span className="count-label">{research.length}</span>}
        </div>
        {!run.research ? (
          <p className="muted">This receipt does not record public research, which a run without the grant cannot do.</p>
        ) : research.length === 0 ? (
          <p className="muted">No public document was fetched.</p>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Source</th>
                  <th>Retrieved</th>
                  <th>Type</th>
                  <th className="numeric">Bytes</th>
                </tr>
              </thead>
              <tbody>
                {research.map((source, index) => {
                  const at = formatTime(source.retrieved_at);
                  return (
                    <tr key={`${source.url}-${index}`}>
                      <td className="run-source">
                        {/* The URL is text, never a link: a run's fetched
                            source is a record of where bytes came from, and
                            turning it into something the operator's browser
                            will request is a different act from reading the
                            receipt. */}
                        <span className="mono">{source.url}</span>
                        {source.truncated && <Badge label="truncated" tone="amber" />}
                        {source.digest && <span className="secondary mono">{source.digest}</span>}
                      </td>
                      <td>
                        {at ? (
                          <time dateTime={source.retrieved_at} title={at.absolute}>
                            {at.relative}
                          </time>
                        ) : (
                          <span className="muted">{ABSENT}</span>
                        )}
                      </td>
                      <td className="mono">{source.media_type || ABSENT}</td>
                      <td className="numeric mono">
                        {source.bytes == null ? ABSENT : formatBytes(source.bytes)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </article>

      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Produced</p>
            <h2>What it wrote down</h2>
          </div>
          {run.outputs && <span className="count-label">{outputs.length}</span>}
        </div>
        {!run.outputs ? (
          <p className="muted">
            The record index cannot answer for this run, so this is unknown rather than none.
          </p>
        ) : outputs.length === 0 ? (
          <p className="muted">This run published no record.</p>
        ) : (
          <ul className="run-outputs">
            {outputs.map((output) => (
              <li key={output.id}>
                <Link to={`/r/${encodeURIComponent(output.id)}`}>
                  {output.title || <span className="mono">{output.id}</span>}
                </Link>
                {output.kind && <Badge label={output.kind} tone="neutral" />}
              </li>
            ))}
          </ul>
        )}
      </article>

      {/* What it declined. This is the section a receipt exists for: a run that
          considered something and did not publish it leaves no record anywhere
          else, and "deferred" and "rejected" are two different outcomes — one
          can still be picked up. */}
      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Declined</p>
            <h2>What it did not publish, and why</h2>
          </div>
          {run.candidates && <span className="count-label">{candidates.length}</span>}
        </div>
        {!run.candidates ? (
          <p className="muted">This receipt does not record candidates either way.</p>
        ) : candidates.length === 0 ? (
          <p className="muted">Nothing was deferred or rejected.</p>
        ) : (
          <ul className="run-candidates">
            {candidates.map((candidate, index) => {
              const at = formatTime(candidate.at);
              return (
                <li key={`${candidate.id}-${index}`}>
                  <div className="run-candidate-head">
                    <span className="mono">{candidate.id}</span>
                    <Badge
                      label={candidate.disposition ?? "declined"}
                      tone={candidate.disposition === "rejected" ? "red" : "amber"}
                    />
                    {at && (
                      <time dateTime={candidate.at} title={at.absolute}>
                        {at.relative}
                      </time>
                    )}
                  </div>
                  <Quoted label="The run's own reason — untrusted, bounded" text={candidate.reason} />
                </li>
              );
            })}
          </ul>
        )}
      </article>

      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Failed</p>
            <h2>What went wrong</h2>
          </div>
          {run.failures && <span className="count-label">{failures.length}</span>}
        </div>
        {!run.failures ? (
          <p className="muted">This receipt does not record failures.</p>
        ) : failures.length === 0 ? (
          <p className="muted">No stage recorded a failure.</p>
        ) : (
          <ul className="run-failures">
            {failures.map((failure, index) => {
              const at = formatTime(failure.at);
              return (
                <li key={`${failure.stage}-${failure.code}-${index}`}>
                  <div className="run-failure-head">
                    <Badge label={failure.stage} tone="red" />
                    <span className="mono">{failure.code}</span>
                    {at && (
                      <time dateTime={failure.at} title={at.absolute}>
                        {at.relative}
                      </time>
                    )}
                  </div>
                  <Quoted label="Failure message — untrusted, bounded" text={failure.message} />
                </li>
              );
            })}
          </ul>
        )}
      </article>

      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Cost</p>
            <h2>What it consumed</h2>
          </div>
        </div>
        <div className="run-metrics">
          <div className="stat">
            <span className="stat-label">cost</span>
            <strong className="stat-value">{dollars(run.usage?.cost_usd)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">input tokens</span>
            <strong className="stat-value">{count(run.usage?.input_tokens)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">output tokens</span>
            <strong className="stat-value">{count(run.usage?.output_tokens)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">wall time</span>
            <strong className="stat-value">{seconds(run.timing?.duration_s)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">cpu</span>
            <strong className="stat-value">{seconds(run.resources?.cpu_s)}</strong>
          </div>
          <div className="stat">
            <span className="stat-label">peak memory</span>
            <strong className="stat-value">
              {run.resources?.max_rss_bytes == null ? ABSENT : formatBytes(run.resources.max_rss_bytes)}
            </strong>
          </div>
          <div className="stat">
            <span className="stat-label">sandbox writes</span>
            <strong className="stat-value">
              {run.resources?.sandbox_bytes_written == null
                ? ABSENT
                : formatBytes(run.resources.sandbox_bytes_written)}
            </strong>
          </div>
          <div className="stat">
            <span className="stat-label">tool calls</span>
            <strong className="stat-value">{count(run.resources?.tool_calls)}</strong>
          </div>
        </div>
        <p className="run-window">
          {started ? (
            <>
              Started <time dateTime={run.timing?.started_at} title={started.absolute}>{started.relative}</time>
            </>
          ) : (
            "Start time not recorded"
          )}
          {finished ? (
            <>
              {", finished "}
              <time dateTime={run.timing?.finished_at} title={finished.absolute}>
                {finished.relative}
              </time>
            </>
          ) : (
            ", no finish recorded"
          )}
          .
        </p>
        {/* The header's own counters, in label-value form rather than as
            prose: "1 failures" is what pluralised prose produces on a receipt
            with one, and a counter row is read as an instrument anyway. */}
        {run.counts && (
          <p className="run-counts mono">
            tools {run.counts.tool_requests} requested, {run.counts.tools_denied} denied · retrieval{" "}
            {run.counts.retrieval} · candidates {run.counts.deferred} deferred, {run.counts.rejected} rejected
            {" · failures "}
            {run.counts.failures}
            {run.counts.redactions > 0 && (
              <span className="redaction-alert" title="Credential-shaped values were removed while building this receipt.">
                {" · redactions "}
                {run.counts.redactions}
              </span>
            )}
          </p>
        )}
      </article>

      {/* Which versions ran it. A later review of what was disclosed is
          meaningless without knowing which rules were in force, which is why
          these are recorded per run rather than looked up as they are today. */}
      <article className="surface run-section">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Versions</p>
            <h2>Which rules were in force</h2>
          </div>
        </div>
        <dl className="run-facts run-versions">
          <div>
            <dt>Redaction policy</dt>
            <dd className="mono">{policy?.redaction || ABSENT}</dd>
          </div>
          <div>
            <dt>Disclosure policy</dt>
            <dd className="mono">{policy?.disclosure || ABSENT}</dd>
          </div>
          <div>
            <dt>Job schema</dt>
            <dd className="mono">{job?.job == null ? ABSENT : `v${job.job}`}</dd>
          </div>
          <div>
            <dt>Prompt</dt>
            <dd className="mono">{job?.prompt || ABSENT}</dd>
          </div>
          <div>
            <dt>Result schema</dt>
            <dd className="mono">{job?.schema || ABSENT}</dd>
          </div>
          <div>
            <dt>Sandbox</dt>
            <dd className="mono">{capability?.sandbox || ABSENT}</dd>
          </div>
          <div>
            <dt>Tool facility</dt>
            <dd className="mono">{capability?.tool || ABSENT}</dd>
          </div>
          <div>
            <dt>Repository</dt>
            <dd className="mono">{capability?.repository || ABSENT}</dd>
          </div>
          <div>
            <dt>Public research</dt>
            <dd className="mono">{capability?.public_research || ABSENT}</dd>
          </div>
        </dl>
        <p className="muted">
          An empty version is a facility this run did not use, not one it used unversioned: a receipt is
          refused if it grants a capability whose facility carries no version.
        </p>
      </article>
    </section>
  );
}

export default RunPage;
