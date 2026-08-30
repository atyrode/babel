import { useEffect, useState } from "react";
import { Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  APIError,
  dismissAPIError,
  getVersion,
  lockServer,
  subscribeAPIErrors,
  type VersionInfo,
} from "./api";
import ArchivePage from "./pages/ArchivePage";
import ExplorePage from "./pages/ExplorePage";
import FindingPage from "./pages/FindingPage";
import FindingsPage from "./pages/FindingsPage";
import HypothesesPage from "./pages/HypothesesPage";
import HypothesisPage from "./pages/HypothesisPage";
import RealityEntityPage from "./pages/RealityEntityPage";
import RealityPage from "./pages/RealityPage";
import ReviewPage from "./pages/ReviewPage";
import ReviewRecordPage from "./pages/ReviewRecordPage";
import SessionPage from "./pages/SessionPage";
import SessionsPage from "./pages/SessionsPage";

const LOCK_PROMPT =
  "Lock and stop the server?\n\nThe launch token is revoked immediately and this " +
  "page stops working. Run `babel web` again to get a new URL.";

function App() {
  const location = useLocation();
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [apiError, setAPIError] = useState<string | null>(null);
  const [stopping, setStopping] = useState(false);
  const [stopped, setStopped] = useState(false);

  useEffect(() => subscribeAPIErrors(setAPIError), []);
  useEffect(() => {
    let live = true;
    getVersion()
      .then((value) => {
        if (live) setVersion(value);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);

  const versionLabel = version
    ? `${version.version}${version.dirty ? " · dirty" : ""}`
    : "version unavailable";

  // The confirmation is a native dialog, matching how the archive page guards
  // its own expensive action, so the control needs a second deliberate
  // acknowledgement and cannot be triggered by one stray click.
  async function lockAndStop() {
    if (!window.confirm(LOCK_PROMPT)) return;
    setStopping(true);
    try {
      await lockServer();
      setStopped(true);
    } catch (reason) {
      // A 401 means the token is already revoked, so the lock did land and
      // this page simply never read the confirmation; reporting anything but
      // the terminal state would be wrong. Any other failure is honestly
      // unknown, so api.ts's banner stands and the control stays usable.
      if (reason instanceof APIError && reason.status === 401) setStopped(true);
    } finally {
      setStopping(false);
    }
  }

  // Once the server is gone there is nothing left to navigate to, so the shell
  // is replaced outright. Keeping the nav and the pages mounted would leave an
  // interface that looks alive, retries in the background, and reports the
  // stop the operator asked for as a string of errors.
  if (stopped) {
    return (
      <div className="app-shell">
        <section className="page stopped-page">
          <div className="state-card stopped-card" role="alert" aria-live="assertive">
            <span className="empty-icon" aria-hidden="true">■</span>
            <strong>Server stopped</strong>
            <span>
              The launch token was revoked and the listener has shut down. This page can no longer
              reach Babel, and its URL will not work again.
            </span>
            <span className="muted">
              Run <code>babel web</code> in a terminal to start a new session with a new token.
            </span>
          </div>
        </section>
      </div>
    );
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand-block">
          <span className="brand-mark" aria-hidden="true">B</span>
          <div>
            <div className="brand">Babel</div>
            <div className="version" title={version ? `${version.commit} · ${version.go} · ${version.platform}` : undefined}>
              {versionLabel}
            </div>
          </div>
        </div>
        <div className="topbar-actions">
          <nav aria-label="Primary navigation">
            <NavLink to="/sessions" className={({ isActive }) => isActive ? "active" : undefined}>
              Sessions
            </NavLink>
            <NavLink to="/archive" className={({ isActive }) => isActive ? "active" : undefined}>
              Archive
            </NavLink>
            <NavLink to="/explore" className={({ isActive }) => isActive ? "active" : undefined}>
              Explore
            </NavLink>
            {/* The findings routes belong to the Hypotheses area: candidates
                and their consolidations are one frontier. */}
            <NavLink
              to="/hypotheses"
              className={({ isActive }) =>
                isActive || location.pathname.startsWith("/findings") ? "active" : undefined}
            >
              Hypotheses
            </NavLink>
            <NavLink to="/reality" className={({ isActive }) => isActive ? "active" : undefined}>
              Reality
            </NavLink>
            <NavLink to="/review" className={({ isActive }) => isActive ? "active" : undefined}>
              Review
            </NavLink>
          </nav>
          {/* The stop control lives in the shell rather than on a page because
              it ends the whole session, not one page's work. */}
          <button
            type="button"
            className="danger-button lock-button"
            onClick={lockAndStop}
            disabled={stopping}
            title="Revoke the launch token and stop this server"
          >
            {stopping && <span className="spinner small" />}
            {stopping ? "Stopping…" : "Lock & stop"}
          </button>
        </div>
      </header>

      {apiError && (
        <div className="error-banner" role="alert">
          <span>{apiError}</span>
          <button type="button" className="icon-button" onClick={dismissAPIError} aria-label="Dismiss error">
            ×
          </button>
        </div>
      )}

      <main>
        <Routes>
          <Route path="/sessions" element={<SessionsPage />} />
          <Route path="/sessions/:selector" element={<SessionPage />} />
          <Route path="/archive" element={<ArchivePage />} />
          <Route path="/explore" element={<ExplorePage />} />
          <Route path="/hypotheses" element={<HypothesesPage />} />
          <Route path="/hypotheses/:id" element={<HypothesisPage />} />
          <Route path="/findings" element={<FindingsPage />} />
          <Route path="/findings/:id" element={<FindingPage />} />
          <Route path="/reality" element={<RealityPage />} />
          <Route path="/reality/entities/:id" element={<RealityEntityPage />} />
          <Route path="/review" element={<ReviewPage />} />
          <Route path="/review/:type/:id" element={<ReviewRecordPage />} />
          {/* `replace` is load-bearing, not styling: the launch URL's
              "#token=…" fragment matches no route and lands here, so a
              replacing redirect drops that entry instead of leaving it
              reachable by Back with a live credential in it. web/browser
              asserts the property; see api.ts for the measurement. */}
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </main>
    </div>
  );
}

export default App;
