import { useEffect, useState } from "react";
import {
  Link,
  Navigate,
  NavLink,
  Route,
  Routes,
  useLocation,
  useParams,
} from "react-router-dom";
import {
  APIError,
  dismissAPIError,
  getVersion,
  lockServer,
  subscribeAPIErrors,
  type APIFailure,
  type VersionInfo,
} from "./api";
import AskPage from "./pages/AskPage";
import ComplaintPage from "./pages/ComplaintPage";
import DecidePage from "./pages/DecidePage";
import ReadPage from "./pages/ReadPage";
import RealityEntitiesPage from "./pages/RealityEntitiesPage";
import RealityEntityPage from "./pages/RealityEntityPage";
import RealityFactPage from "./pages/RealityFactPage";
import RealityFactsPage from "./pages/RealityFactsPage";
import RealityPage from "./pages/RealityPage";
import RealityQuestionPage from "./pages/RealityQuestionPage";
import RealityQuestionsPage from "./pages/RealityQuestionsPage";
import RecordPage from "./pages/RecordPage";
import RunPage from "./pages/RunPage";
import SessionPage from "./pages/SessionPage";
import SessionsPage from "./pages/SessionsPage";
import SettingsPage from "./pages/SettingsPage";
import WatchPage from "./pages/WatchPage";
import Palette from "./palette";
import RenderBoundary from "./boundary";
import { KeyHints, LiveIndicator, ShellControls, ShellFooter, useDensity } from "./shell";

const LOCK_PROMPT =
  "Lock and stop the server?\n\nThe session is revoked immediately and this " +
  "page stops working. Run `babel web` again to get a new URL.";

// One record has one page, so every route that used to open a record by its
// kind now redirects to it by its id. The kind stays in the URL of the old
// link — a bookmark, a link in an issue, a terminal's printed path — and is
// simply not needed to resolve the record any more.
function RecordRedirect() {
  const { id } = useParams();
  return <Navigate to={`/r/${encodeURIComponent(id ?? "")}`} replace />;
}

// The Reality Ledger kept its shape and lost its name: the operator asks Babel
// things and Babel asks him things, so the section is "Ask" and every path
// under /reality moves across unchanged. The splat carries the rest of the
// path so a bookmarked fact or subject lands on the same record.
function AskRedirect() {
  const params = useParams();
  const rest = params["*"] ?? "";
  return <Navigate to={rest ? `/ask/${rest}` : "/ask"} replace />;
}

// Settings sections are query state rather than routes, so a redirect into one
// has to carry the section and whatever the old link already asked for —
// /reality/focus?subject=… is a real link the subjects page writes.
function SettingsRedirect({ section }: { section: string }) {
  const { search } = useLocation();
  const query = new URLSearchParams(search);
  query.set("section", section);
  return <Navigate to={`/settings?${query.toString()}`} replace />;
}

function App() {
  const location = useLocation();
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [failure, setFailure] = useState<APIFailure | null>(null);
  const [stopping, setStopping] = useState(false);
  const [stopped, setStopped] = useState(false);
  const [hintsOpen, setHintsOpen] = useState(false);
  const { density, setDensity } = useDensity();

  // Arriving 900px into a record because the previous page was scrolled there
  // is the kind of fault that makes an interface feel haunted. Every route
  // change starts at the top; a fragment is left alone, because a link that
  // names a place in the page asked to land there.
  useEffect(() => {
    if (!location.hash) window.scrollTo(0, 0);
  }, [location.pathname, location.hash]);

  // "?" is the only key the shell claims for itself — the palette owns ⌘K, and
  // the listings and the record page own the letters. Every one of them must
  // ignore a reader who is typing, so the test is the same in all of them: the
  // event came from a field, or it did not.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable='true']")) return;
      if (event.key === "?") {
        event.preventDefault();
        setHintsOpen(true);
      } else if (event.key === "Escape") {
        setHintsOpen(false);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

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
  // The header wears the release and the footer wears the whole build string.
  // A describe-style version is forty characters of commit and timestamp, and
  // in the wordmark it pushed the navigation onto a second row on a 1440px
  // screen — the chrome growing to fit an identifier nobody reads at a glance.
  const shortVersion = version
    ? `${version.version.split("-")[0]}${version.dirty ? " · dirty" : ""}`
    : "version unavailable";

  // The confirmation is a native dialog, matching how the archive section
  // guards its own expensive action, so the control needs a second deliberate
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
          <div className="surface state-note stopped-note" role="alert" aria-live="assertive">
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
        {/* The wordmark is the way back to Decide, which is where a launched
            session lands and the only page that claims the operator's
            attention. It carries no nav entry of its own. */}
        <Link className="brand-block" to="/" title="What needs me?">
          <span className="brand-mark" aria-hidden="true">B</span>
          <div>
            <div className="brand">Babel</div>
            <div className="version" title={version ? `${versionLabel} · ${version.commit} · ${version.go} · ${version.platform}` : undefined}>
              {shortVersion}
            </div>
          </div>
        </Link>
        <div className="topbar-actions">
          {/* Four questions, in the order an operator asks them, and nothing
              in the row is the name of a record kind or of a place Babel
              keeps bytes. The row used to hold eleven entries — Findings,
              Proposals, Hypotheses, Reality, Focus, Evaluation, Review,
              Sessions, Explore, ?, Lock & stop — which required knowing the
              data model before you could pick one (#234).

              Sessions is deliberately absent. Nobody opens Babel to browse
              transcripts; a transcript is where a citation lands, so it stays
              routed and is reached from the evidence that cites it.

              Settings is last and is a container rather than a question: the
              archive, what evaluation may spend, what Babel may spend on a
              subject, and the orientation text. None of them is something the
              operator comes here to read. */}
          <nav aria-label="Primary navigation">
            <NavLink
              end
              to="/"
              className={({ isActive }) => isActive ? "active" : undefined}
              title="What needs me?"
            >
              Decide
            </NavLink>
            <NavLink
              to="/read"
              className={({ isActive }) => isActive ? "active" : undefined}
              title="What has Babel found?"
            >
              Read
            </NavLink>
            <NavLink
              to="/watch"
              className={({ isActive }) => isActive ? "active" : undefined}
              title="What is it doing, and what did it cost?"
            >
              Watch
            </NavLink>
            <NavLink
              to="/ask"
              className={({ isActive }) => isActive ? "active" : undefined}
              title="What does it know, and what does it need from me?"
            >
              Ask
            </NavLink>
            <NavLink
              to="/settings"
              className={({ isActive }) => isActive ? "active" : undefined}
              title="The archive, what analysis may spend, and what Babel is"
            >
              Settings
            </NavLink>
          </nav>
          {/* The instruments and the stop, in one cluster. They are grouped
              rather than loose in the row because the narrow header stacks
              them under the navigation as a unit: brand and destinations on
              one row, what is running and what can be pressed on the next.

              The live mark renders only while something is running, so the
              cluster is one control shorter on a quiet deployment; search,
              density and the key hints fold into a single … menu below 640px,
              which ShellControls decides. */}
          <div className="shell-instruments">
            <LiveIndicator />
            <ShellControls
              density={density}
              setDensity={setDensity}
              onKeyHints={() => setHintsOpen(true)}
            />
            {/* The stop control lives in the shell rather than on a page
                because it ends the whole session, not one page's work, and it
                is never folded into a menu: it is the one thing the operator
                may need in a hurry. */}
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
          <Route path="/" element={<DecidePage />} />
          <Route path="/read" element={<ReadPage />} />
          <Route path="/watch" element={<WatchPage />} />
          {/* One run, whole: what it searched, what it fetched, what it
              declined and what it cost. It hangs under Watch because a run is
              only ever reached from the control room that lists it. */}
          <Route path="/watch/runs/:id" element={<RunPage />} />
          {/* Ask is one nav entry and seven destinations, nested under a
              layout so the ledger's own row is present on every one of them
              — including a belief or a question reached by clicking a
              record, which is where a reader most needs to know what else
              the ledger holds (§8.4). */}
          <Route path="/ask" element={<AskPage />}>
            <Route index element={<RealityPage />} />
            <Route path="questions" element={<RealityQuestionsPage />} />
            <Route path="questions/:id" element={<RealityQuestionPage />} />
            <Route path="entities" element={<RealityEntitiesPage />} />
            <Route path="entities/:id" element={<RealityEntityPage />} />
            <Route path="facts" element={<RealityFactsPage />} />
            <Route path="facts/:id" element={<RealityFactPage />} />
          </Route>
          <Route path="/r/:id" element={<RecordPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="/sessions" element={<SessionsPage />} />
          <Route path="/sessions/:selector" element={<SessionPage />} />
          {/* #115's capture rides the Decide surface, so a complaint's record
              page sits beside it and gains no nav entry of its own: the
              listing that reaches this page is above the queue. */}
          <Route path="/complaints/:id" element={<ComplaintPage />} />

          {/* Everything below is a path this build no longer serves. They are
              kept as redirects rather than deleted because an operator's
              bookmarks, an issue's links and a terminal's printed URLs all
              outlive a navigation redesign, and a 404 would make the redesign
              look like data loss. Every one of them replaces its history
              entry, so Back leaves the old surface rather than bouncing. */}
          <Route path="/review" element={<Navigate to="/" replace />} />
          <Route path="/review/:type/:id" element={<RecordRedirect />} />
          <Route path="/findings" element={<Navigate to="/read?kind=finding" replace />} />
          <Route path="/findings/:id" element={<RecordRedirect />} />
          <Route path="/proposals" element={<Navigate to="/read?kind=proposal" replace />} />
          <Route path="/proposals/:id" element={<RecordRedirect />} />
          <Route path="/hypotheses" element={<Navigate to="/read?kind=hypothesis" replace />} />
          <Route path="/hypotheses/:id" element={<RecordRedirect />} />
          <Route path="/evaluation" element={<Navigate to="/read" replace />} />
          <Route path="/evaluation/coverage" element={<Navigate to="/read?coverage=unreviewed" replace />} />
          <Route path="/evaluation/policy" element={<SettingsRedirect section="policy" />} />
          {/* Ranked below the two named paths above, so "coverage" is never
              read as a record kind. */}
          <Route path="/evaluation/:kind/:id" element={<RecordRedirect />} />
          <Route path="/explore" element={<Navigate to="/watch" replace />} />
          {/* The fleet was a page about machines, and the machine is no longer
              a dimension of the reading path: Watch is deployment-wide and
              reads no view parameter, so the old bookmark lands on Watch
              rather than on Watch carrying a query nothing answers. */}
          <Route path="/fleet" element={<Navigate to="/watch" replace />} />
          <Route path="/archive" element={<SettingsRedirect section="archive" />} />
          <Route path="/help" element={<SettingsRedirect section="help" />} />
          <Route path="/reality/focus" element={<SettingsRedirect section="ceilings" />} />
          <Route path="/reality" element={<Navigate to="/ask" replace />} />
          <Route path="/reality/*" element={<AskRedirect />} />
          {/* `replace` is load-bearing, not styling: the launch URL's
              "#nonce=…" fragment matches no route and lands here, so a
              replacing redirect drops that entry instead of leaving it
              reachable by Back with a bootstrap credential in it. web/browser
              asserts the property; see api.ts for the measurement. */}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </RenderBoundary>
      </main>

      <ShellFooter version={versionLabel} />

      {/* Mounted once, for every route. The palette is the only way to reach a
          record whose name you remember and whose page you do not. */}
      <Palette />
      {hintsOpen && <KeyHints onClose={() => setHintsOpen(false)} />}
    </div>
  );
}

export default App;
