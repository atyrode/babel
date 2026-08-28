import { useEffect, useState } from "react";
import { Navigate, NavLink, Route, Routes } from "react-router-dom";
import {
  dismissAPIError,
  getVersion,
  subscribeAPIErrors,
  type VersionInfo,
} from "./api";
import ArchivePage from "./pages/ArchivePage";
import SessionPage from "./pages/SessionPage";
import SessionsPage from "./pages/SessionsPage";

function App() {
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [apiError, setAPIError] = useState<string | null>(null);

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
        <nav aria-label="Primary navigation">
          <NavLink to="/sessions" className={({ isActive }) => isActive ? "active" : undefined}>
            Sessions
          </NavLink>
          <NavLink to="/archive" className={({ isActive }) => isActive ? "active" : undefined}>
            Archive
          </NavLink>
        </nav>
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
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </main>
    </div>
  );
}

export default App;
