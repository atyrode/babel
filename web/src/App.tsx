import { useEffect, useState } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
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
import FocusPage from "./pages/FocusPage";
import HelpPage from "./pages/HelpPage";
import HypothesesPage from "./pages/HypothesesPage";
import HypothesisPage from "./pages/HypothesisPage";
import ProposalPage from "./pages/ProposalPage";
import ProposalsPage from "./pages/ProposalsPage";
import RealityEntityPage from "./pages/RealityEntityPage";
import RealityPage from "./pages/RealityPage";
import ReviewPage from "./pages/ReviewPage";
import ReviewRecordPage from "./pages/ReviewRecordPage";
import SessionPage from "./pages/SessionPage";
import SessionsPage from "./pages/SessionsPage";
import RenderBoundary from "./boundary";

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
        {/* The wordmark is the way back to the overview, which is where a
            launched session lands. It carries no nav entry of its own: the row
            beside it is for what Babel found, and a second link to the page
            the logo already reaches would cost an entry and answer nothing. */}
        <Link className="brand-block" to="/" title="Overview">
          <span className="brand-mark" aria-hidden="true">B</span>
          <div>
            <div className="brand">Babel</div>
            <div className="version" title={version ? `${version.commit} · ${version.go} · ${version.platform}` : undefined}>
              {versionLabel}
            </div>
          </div>
        </Link>
        <div className="topbar-actions">
          {/* The row reads as the product: what Babel found, then the material
              it found it in. Findings and Proposals lead because they are the
              output an operator comes here to read; Sessions and Explore are
              the corpus and the machinery behind them.

              Archive and the fleet diagnostic are deliberately not here. A
              repository of snapshots and a list of which computer announced
              which run are operational surfaces, not destinations in a product
              whose subject is one body of work; both stay routed and are
              reached from Help. */}
          <nav aria-label="Primary navigation">
            <NavLink
              to="/findings"
              className={({ isActive }) => isActive ? "active" : undefined}
            >
              Findings
            </NavLink>
            <NavLink to="/proposals" className={({ isActive }) => isActive ? "active" : undefined}>
              Proposals
            </NavLink>
            <NavLink to="/hypotheses" className={({ isActive }) => isActive ? "active" : undefined}>
              Hypotheses
            </NavLink>
            <NavLink to="/reality" className={({ isActive }) => isActive ? "active" : undefined}>
              Reality
            </NavLink>
            {/* Focus is its own entry rather than a tab inside Reality, and
                the reason is the question it answers: "stop spending on this"
                is a thing an operator comes here to *do*, on a page he has to
                be able to find without being told where it is. Reality is the
                ledger's inbox — questions Babel is asking — and burying the one
                control that stops work inside it is how it stayed a CLI-only
                act. */}
            <NavLink to="/reality/focus" className={({ isActive }) => isActive ? "active" : undefined}>
              Focus
            </NavLink>
            <NavLink to="/review" className={({ isActive }) => isActive ? "active" : undefined}>
              Review
            </NavLink>
            <NavLink to="/sessions" className={({ isActive }) => isActive ? "active" : undefined}>
              Sessions
            </NavLink>
            <NavLink to="/explore" className={({ isActive }) => isActive ? "active" : undefined}>
              Explore
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
        {/* Keyed by path so navigating away from a faulted page clears the
            fault instead of stranding the reader on it. */}
        <RenderBoundary key={location.pathname}>
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
          <Route path="/proposals" element={<ProposalsPage />} />
          <Route path="/proposals/:id" element={<ProposalPage />} />
          <Route path="/reality" element={<RealityPage />} />
          <Route path="/reality/entities/:id" element={<RealityEntityPage />} />
          {/* §4.8's expenditure policy. It sits under /reality because that is
              what it edits — the ledger's own facts — while carrying its own
              nav entry because it is an act rather than a record. */}
          <Route path="/reality/focus" element={<FocusPage />} />
          <Route path="/review" element={<ReviewPage />} />
          <Route path="/review/:type/:id" element={<ReviewRecordPage />} />
          {/* #115's capture rides the review surface, so a complaint's record
              page sits beside the review routes and gains no nav entry of its
              own: the listing that reaches this page lives on /review, above
              the queue. */}
          <Route path="/complaints/:id" element={<ComplaintPage />} />
          {/* `replace` is load-bearing, not styling: the launch URL's
              "#nonce=…" fragment matches no route and lands here, so a
              replacing redirect drops that entry instead of leaving it
              reachable by Back with a bootstrap credential in it. web/browser
              asserts the property; see api.ts for the measurement. */}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </RenderBoundary>
      </main>
    </div>
  );
}

export default App;
