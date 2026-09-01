import { useEffect, useState } from "react";
import { Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  APIError,
  dismissAPIError,
  getVersion,
  lockServer,
  subscribeAPIErrors,
  type APIFailure,
  type VersionInfo,
} from "./api";
import ArchivePage from "./pages/ArchivePage";
import ComplaintPage from "./pages/ComplaintPage";
import DashboardPage from "./pages/DashboardPage";
import ExplorePage from "./pages/ExplorePage";
import FindingPage from "./pages/FindingPage";
import FindingsPage from "./pages/FindingsPage";
import FleetPage from "./pages/FleetPage";
import HelpPage from "./pages/HelpPage";
import HypothesesPage from "./pages/HypothesesPage";
import HypothesisPage from "./pages/HypothesisPage";
import RealityEntityPage from "./pages/RealityEntityPage";
import RealityPage from "./pages/RealityPage";
import ReviewPage from "./pages/ReviewPage";
import ReviewRecordPage from "./pages/ReviewRecordPage";
import SessionPage from "./pages/SessionPage";
import SessionsPage from "./pages/SessionsPage";

const LOCK_PROMPT =
  "Lock and stop the server?\n\nThe session is revoked immediately and this " +
  "page stops working. Run `babel web` again to get a new URL.";

function App() {
  const location = useLocation();
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [failure, setFailure] = useState<APIFailure | null>(null);
  const [stopping, setStopping] = useState(false);
  const [stopped, setStopped] = useState(false);

  useEffect(() => subscribeAPIErrors(setFailure), []);
  // The banner reports the failure of a request, and a request belongs to the
  // route that made it. `currentError` lives at module scope in ./api and is
  // replayed to every new subscriber, so without this a 409 from a service this
  // build did not wire — the frontier on a machine with no analysis state —
  // would keep accusing every page the operator visited afterwards, including
  // the ones that loaded perfectly.
  //
  // Whether the banner belongs here is decided during render, from the route
  // the failure was published against. It used to be decided by an effect that
  // cleared the banner when the path changed, and that was one frame too late:
  // effects run after the commit, so the new page painted once carrying the old
  // page's refusal before a second render removed it. A page that renders
  // perfectly and accuses another page of failing is the exact falsehood this
  // rule exists to prevent, so it must not be reachable in any frame — and a
  // one-frame version of it is a race that only shows up when something else
  // on the page happens to be slow.
  //
  // The module-level state is still released, in an effect, because that is a
  // side effect rather than a rendering decision. It is now timing-insensitive:
  // the banner is already gone by the time this runs.
  const stale = failure !== null && failure.route !== location.pathname;
  useEffect(() => {
    if (stale) dismissAPIError();
  }, [stale]);
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
      // A 401 means the session is already revoked, so the lock did land and
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
              The session was revoked and the listener has shut down. This page can no longer
              reach Babel, and its URL will not work again.
            </span>
            <span className="muted">
              Run <code>babel web</code> in a terminal to start a new session.
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
            {/* Home is the dashboard, so it is `end`: without it every route
                would match "/" and two entries would read as active at once. */}
            <NavLink to="/" end className={({ isActive }) => isActive ? "active" : undefined}>
              Dashboard
            </NavLink>
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
            {/* Fleet is last of the destinations because it is the only one
                whose subject is not this machine. The row reads "this machine,
                then everywhere", and an entry placed among the local surfaces
                would quietly suggest the pages beside it were fleet-wide too. */}
            <NavLink to="/fleet" className={({ isActive }) => isActive ? "active" : undefined}>
              Fleet
            </NavLink>
            {/* Help is a destination, not a mode: one persistent character, at
                the end of the row, reachable from every page including the
                ones that could not load their data. */}
            <NavLink
              to="/help"
              className={({ isActive }) => isActive ? "help-link active" : "help-link"}
              title="What Babel is, the lifecycle, and the vocabulary"
              aria-label="Help"
            >
              ?
            </NavLink>
          </nav>
          {/* The stop control lives in the shell rather than on a page because
              it ends the whole session, not one page's work. */}
          <button
            type="button"
            className="danger-button lock-button"
            onClick={lockAndStop}
            disabled={stopping}
            title="Revoke this session and stop this server"
          >
            {stopping && <span className="spinner small" />}
            {stopping ? "Stopping…" : "Lock & stop"}
          </button>
        </div>
      </header>

      {failure && !stale && (
        <div className="error-banner" role="alert">
          <span>{failure.message}</span>
          <button type="button" className="icon-button" onClick={dismissAPIError} aria-label="Dismiss error">
            ×
          </button>
        </div>
      )}

      <main>
        <Routes>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/help" element={<HelpPage />} />
          <Route path="/sessions" element={<SessionsPage />} />
          <Route path="/sessions/:selector" element={<SessionPage />} />
          <Route path="/archive" element={<ArchivePage />} />
          <Route path="/explore" element={<ExplorePage />} />
          <Route path="/fleet" element={<FleetPage />} />
          <Route path="/hypotheses" element={<HypothesesPage />} />
          <Route path="/hypotheses/:id" element={<HypothesisPage />} />
          <Route path="/findings" element={<FindingsPage />} />
          <Route path="/findings/:id" element={<FindingPage />} />
          <Route path="/reality" element={<RealityPage />} />
          <Route path="/reality/entities/:id" element={<RealityEntityPage />} />
          <Route path="/review" element={<ReviewPage />} />
          <Route path="/review/:type/:id" element={<ReviewRecordPage />} />
          {/* #115's capture rides the review surface, so a complaint's record
              page sits beside the review routes and the nav row deliberately
              gains no seventh destination: the listing that reaches this page
              lives on /review, above the queue. */}
          <Route path="/complaints/:id" element={<ComplaintPage />} />
          {/* `replace` is load-bearing, not styling: the launch URL's
              "#nonce=…" fragment matches no route and lands here, so a
              replacing redirect drops that entry instead of leaving it
              reachable by Back with a bootstrap credential in it. web/browser
              asserts the property; see api.ts for the measurement. */}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  );
}

export default App;
