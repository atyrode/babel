import { useCallback, useEffect, useState } from "react";
import {
  getArchiveStatus,
  getState,
  verifyArchive,
  type ArchiveStatus,
  type StateInfo,
  type VerifyResult,
} from "../api";
import { errorMessage, formatTime } from "../format";

function ArchivePage() {
  const [configuration, setConfiguration] = useState<StateInfo | null>(null);
  const [status, setStatus] = useState<ArchiveStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState<"standard" | "deep" | null>(null);
  const [verifyResult, setVerifyResult] = useState<VerifyResult | null>(null);
  const [verifyError, setVerifyError] = useState<string | null>(null);

  const loadArchive = useCallback(() => {
    setLoading(true);
    setError(null);
    setStatus(null);
    getState()
      .then(async (value) => {
        setConfiguration(value);
        if (value.configured) setStatus(await getArchiveStatus());
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(loadArchive, [loadArchive]);

  async function runVerification(deep: boolean) {
    const prompt = deep
      ? "Run deep verification? This reads and re-hashes all pack data and can take several minutes."
      : "Verify the archive repository now?";
    if (!window.confirm(prompt)) return;
    setVerifying(deep ? "deep" : "standard");
    setVerifyResult(null);
    setVerifyError(null);
    try {
      setVerifyResult(await verifyArchive(deep));
    } catch (reason) {
      setVerifyError(errorMessage(reason));
    } finally {
      setVerifying(null);
    }
  }

  return (
    <section className="page archive-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Restic repository</p>
          <h1>Archive</h1>
          <p className="subtitle">Inspect repository coverage and verify archived data.</p>
        </div>
      </div>

      {loading && !configuration && <div className="state-card"><span className="spinner" /> Loading archive state…</div>}
      {error && (
        <div className="state-card error-state">
          <strong>Archive state could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={loadArchive}>Try again</button>
        </div>
      )}

      {configuration && !configuration.configured && (
        <div className="state-card empty-state configure-empty">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Archive not configured</strong>
          <span>Run <code>babel storage configure</code> to connect a repository.</span>
        </div>
      )}

      {configuration?.configured && (
        <>
          <article className="card repository-card">
            <div>
              <p className="eyebrow">Connected repository</p>
              <h2>{configuration.repository}</h2>
            </div>
            <div className="repository-facts">
              <span>Host ID</span>
              <strong className="mono">{configuration.host_id || "—"}</strong>
              <span>Snapshots</span>
              <strong>{status?.snapshots ?? "—"}</strong>
            </div>
          </article>

          {loading && !status && <div className="state-card"><span className="spinner" /> Reading snapshot status…</div>}
          {status && status.hosts.length === 0 && (
            <div className="state-card empty-state">
              <strong>No snapshots</strong>
              <span>The configured repository does not hold any snapshots yet.</span>
            </div>
          )}
          {status && status.hosts.length > 0 && (
            <article className="card snapshot-card">
              <div className="section-heading">
                <div><p className="eyebrow">Repository coverage</p><h2>Snapshots by host</h2></div>
                <span className="count-label">{status.snapshots} total</span>
              </div>
              <div className="table-scroll">
                <table>
                  <thead><tr><th>Host</th><th className="numeric">Snapshots</th><th>Latest</th><th>Snapshot ID</th><th>Tags</th></tr></thead>
                  <tbody>
                    {status.hosts.map((host) => {
                      const latest = formatTime(host.latest_time);
                      return (
                        <tr key={host.host}>
                          <td><strong>{host.host}</strong></td>
                          <td className="numeric mono">{host.snapshots}</td>
                          <td>{latest ? <><span>{latest.relative}</span><span className="secondary" title={latest.absolute}>{latest.absolute}</span></> : <span className="muted">—</span>}</td>
                          <td className="mono" title={host.latest_id}>{host.latest_short_id || host.latest_id || "—"}</td>
                          <td><div className="tag-list">{host.tags?.length ? host.tags.map((tag) => <span className="tag" key={tag}>{tag}</span>) : <span className="muted">—</span>}</div></td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </article>
          )}

          <article className="card verify-card">
            <div className="verify-copy">
              <p className="eyebrow">Integrity</p>
              <h2>Verify archive</h2>
              <p>Standard verification checks repository metadata and pack structure.</p>
              <p className="deep-note"><strong>Deep verification</strong> reads and re-hashes all pack data. It can take several minutes.</p>
            </div>
            <div className="verify-actions">
              <button type="button" onClick={() => runVerification(false)} disabled={verifying !== null}>
                {verifying === "standard" && <span className="spinner small" />}
                {verifying === "standard" ? "Verifying…" : "Verify"}
              </button>
              <button type="button" className="danger-button" onClick={() => runVerification(true)} disabled={verifying !== null}>
                {verifying === "deep" && <span className="spinner small" />}
                {verifying === "deep" ? "Deep verification running…" : "Deep verify"}
              </button>
            </div>
            {verifying && <div className="verify-running" role="status"><span className="spinner" /><span>{verifying === "deep" ? "Reading and re-hashing archive data. Keep this page open; this can take several minutes." : "Checking repository integrity…"}</span></div>}
            {verifyError && <p className="inline-error" role="alert">Verification failed: {verifyError}</p>}
            {verifyResult && (
              <div className={verifyResult.ok ? "result-panel success-panel" : "result-panel failure-panel"} role="status">
                <strong>{verifyResult.ok ? "Verification passed" : "Verification found a problem"}</strong>
                <span>{verifyResult.deep ? "Deep verification" : "Standard verification"} · {verifyResult.repository}</span>
                {verifyResult.error && <span>{verifyResult.error}</span>}
              </div>
            )}
          </article>
        </>
      )}
    </section>
  );
}

export default ArchivePage;
