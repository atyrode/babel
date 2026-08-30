package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/transcript"
	"github.com/atyrode/babel/internal/web"
	webdist "github.com/atyrode/babel/web"
)

const webUsage = `Usage: babel web [flags]

Serves the local web interface on 127.0.0.1 and prints a launch URL
containing a one-time access token. The page browses this host's sessions,
renders transcripts, and drives archive status, verify, and fetch. Nothing
binds beyond loopback and no command served here can delete archived data.

Repository selection follows the usual precedence (flags, then
$BABEL_RESTIC_REPO/$BABEL_RESTIC_PASSWORD_FILE, then storage.json). Without
a repository the browser still works read-only; archive actions report
"not configured".

Flags:
  --port N                    listen port (default: an ephemeral free port)
  --open                      also open the URL with the system browser
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --host ID                   archive host identity for fetches
`

// webCmd implements `babel web`. Every application dependency the server
// needs is an in-process invocation of the corresponding headless command
// in --json mode, so the web API cannot drift from the CLI contract: both
// surfaces share one implementation, one sanitizer, and one never-delete
// command set (SPEC.md §2.4, §8.2).
func (a *app) webCmd(ctx context.Context, args []string) error {
	c := newCmd("web", webUsage)
	var rf repoFlags
	rf.bind(c.fs)
	port := c.fs.Int("port", 0, "listen port (default: ephemeral)")
	open := c.fs.Bool("open", false, "open the URL with the system browser")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	srv, err := a.buildWebServer(rf, *port)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(a.stdout, "babel web listening at %s\n", srv.URL())
	fmt.Fprint(a.stdout, "the token in the URL is required; press Ctrl-C to stop\n")
	if *open {
		if err := openBrowser(srv.URL()); err != nil {
			a.diagf("warning: open browser: %s\n", Sanitize(err.Error()))
		}
	}
	return srv.Serve(ctx)
}

// buildWebServer assembles the server against this process's CLI surface.
// Kept separate from flag handling so tests can drive a live server.
func (a *app) buildWebServer(rf repoFlags, port int) (*web.Server, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, err
	}
	d, err := babelDirs()
	if err != nil {
		return nil, err
	}
	repository := firstNonEmpty(rf.repository, os.Getenv("BABEL_RESTIC_REPO"), cfg.Repository)
	passwordFile := firstNonEmpty(rf.passwordFile, os.Getenv("BABEL_RESTIC_PASSWORD_FILE"), cfg.PasswordFile)
	configured := repository != "" && passwordFile != ""

	// Explicitly forward the resolved repository selection so every archive
	// sub-invocation sees exactly the state the server advertised, even if
	// the environment changes while the server runs.
	var forward []string
	if configured {
		forward = append(forward, "--repo", repository, "--password-file", passwordFile)
	}
	if b := firstNonEmpty(rf.binary, cfg.ResticBinary); b != "" {
		forward = append(forward, "--restic-binary", b)
	}

	hostID := firstNonEmpty(rf.host, os.Getenv("BABEL_HOST_ID"), cfg.HostID)
	if hostID == "" {
		if name, err := os.Hostname(); err == nil {
			hostID = sanitizeHostID(name)
		}
	}

	// One coordinator per catalog, shared by every request this process
	// serves, so concurrent listings can never multiply the scan. Its
	// diagnostics go to the process's own stderr because a scan outlives
	// the request that started it.
	sc := scanner(d.data)
	sc.attach(a.stderr)

	opts := web.Options{
		Port:        port,
		Static:      webdist.Dist(),
		Diagnostics: a.stderr,
		State: web.StateProviderFunc(func(context.Context) (web.State, error) {
			return web.State{
				Configured: configured,
				Repository: Sanitize(repository),
				HostID:     Sanitize(hostID),
			}, nil
		}),
		Lister: web.SessionListerFunc(func(context.Context) (web.SessionsResult, error) {
			// The request's context is deliberately unused: the listing is
			// answered from the catalog immediately, and the scan that keeps
			// the catalog current belongs to this process, so a browser
			// reload can never cancel work already done.
			rows, refreshedAt, state := sc.Listing(adapters(), nil)
			return web.SessionsResult{
				Sessions:    webSessionRows(rows),
				RefreshedAt: refreshedAt,
				Scan:        webScanState(state),
			}, nil
		}),
		Scanner: &webScanner{coordinator: sc},
		Inspector: web.SessionInspectorFunc(func(ctx context.Context, selector string) (web.InspectResult, error) {
			var res web.InspectResult
			err := a.runJSON(ctx, &res, "sessions", "inspect", selector, "--json")
			return res, err
		}),
		Transcripts: web.TranscriptReaderFunc(transcript.Events),
	}
	if configured {
		opts.Archive = &webArchive{
			app:          a,
			forward:      forward,
			host:         rf.host,
			sessionsRoot: d.sessionsRoot(),
		}
	}
	return web.New(opts)
}

// webScanner exposes the process-wide background scanner to the server.
type webScanner struct {
	coordinator *scanCoordinator
}

func (w *webScanner) State() web.ScanState { return webScanState(w.coordinator.State()) }

func (w *webScanner) StartRefresh() web.ScanState {
	return webScanState(w.coordinator.Start(adapters(), nil))
}

// webScanState maps the CLI's scan state onto its web mirror.
func webScanState(state scanState) web.ScanState {
	return web.ScanState{
		Running:    state.Running,
		Described:  state.Described,
		Total:      state.Total,
		Failed:     state.Failed,
		Harness:    state.Harness,
		StartedAt:  state.StartedAt,
		FinishedAt: state.FinishedAt,
		Error:      state.Error,
	}
}

// webSessionRows maps the CLI's listing rows onto their web mirrors. Both
// shapes are already sanitized; this is the same document the headless
// `sessions list --json` emits, restated in the server's types.
func webSessionRows(rows []sessionRow) []web.SessionRow {
	out := make([]web.SessionRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, web.SessionRow{
			Harness:           row.Harness,
			SourceID:          row.SourceID,
			Selector:          row.Selector,
			Size:              row.Size,
			Modified:          row.Modified,
			Title:             row.Title,
			TitleProvenance:   row.TitleProvenance,
			Workspace:         row.Workspace,
			ContinuationGrade: row.Continuous,
		})
	}
	return out
}

// webArchive drives the archive commands in process. Only status, verify,
// listing, and fetch exist here; the CLI exposes no deletion, so neither can
// the web.
type webArchive struct {
	app *app
	// forward is the resolved repository selection every sub-invocation
	// receives: --repo, --password-file, and --restic-binary.
	forward []string
	// host is `babel web --host`, the launch-time archive host identity. It
	// is not folded into forward because it is not part of the repository
	// selection: `archive status` and `archive verify` bind the flag and
	// never read it, while `sessions list` and `sessions fetch` give it a
	// meaning — read that host's snapshot instead of this machine — that a
	// per-request value must be able to override.
	host string
	// sessionsRoot is where fetched session trees live. Resolved once at
	// launch rather than per request, because the data directory does not
	// move while a server runs.
	sessionsRoot string
}

func (w *webArchive) ArchiveStatus(ctx context.Context) (web.StatusResult, error) {
	var res web.StatusResult
	err := w.app.runJSON(ctx, &res, append([]string{"archive", "status", "--json"}, w.forward...)...)
	return res, err
}

func (w *webArchive) ArchiveVerify(ctx context.Context, deep bool) (web.VerifyResult, error) {
	args := []string{"archive", "verify", "--json"}
	if deep {
		args = append(args, "--deep")
	}
	var res web.VerifyResult
	err := w.app.runJSON(ctx, &res, append(args, w.forward...)...)
	// A failed verification still emits its result document (exit 1 with
	// ok=false); surface the parsed report rather than a bare error.
	if err != nil && res.Repository != "" {
		return res, nil
	}
	return res, err
}

// ArchiveSessions runs `babel sessions list --host`, the command that reads a
// snapshot's file listing instead of this machine's source trees.
//
// Nothing here reimplements that listing: the flag combinations the command
// refuses — --roots and --no-cache against --host, --snapshot without one —
// stay refused because this builds an invocation rather than calling past it,
// and a refusal arrives as the web bad-request sentinel through runJSON.
func (w *webArchive) ArchiveSessions(ctx context.Context, host, snapshot string) (web.ArchiveSessionsResult, error) {
	args := []string{"sessions", "list", "--host", host, "--json"}
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
	}
	var listing sessionsResult
	if err := w.app.runJSON(ctx, &listing, append(args, w.forward...)...); err != nil {
		return web.ArchiveSessionsResult{}, err
	}
	return web.ArchiveSessionsResult{
		Host:     Sanitize(host),
		Snapshot: Sanitize(snapshot),
		Sessions: w.archiveRows(listing.Sessions),
	}, nil
}

// archiveRows narrows the command's listing rows to what a snapshot listing
// actually observed, and adds the one local fact this view needs.
//
// The four nullable fields are dropped rather than forwarded as nulls. They
// are null by construction here — rowFromArchived fills in only the identity,
// the selector, and the primary log's recorded size — so carrying them would
// hand a client a title field that is always absent and invite it to render
// the absence as an empty cell.
func (w *webArchive) archiveRows(rows []sessionRow) []web.ArchiveSessionRow {
	out := make([]web.ArchiveSessionRow, 0, len(rows))
	for _, row := range rows {
		archived := web.ArchiveSessionRow{
			Harness:  row.Harness,
			SourceID: row.SourceID,
			Selector: row.Selector,
			Size:     row.Size,
		}
		if dir, ok := w.materialization(row.Selector); ok {
			archived.Fetched, archived.FetchedPath = true, Sanitize(dir)
		}
		out = append(out, archived)
	}
	return out
}

// materialization reports whether this machine already holds a fetched copy of
// one archived session, and where.
//
// The answer is a directory test rather than a repository read, because the
// fetch naming rule is deterministic from the selector alone — that is what
// lets local prune find a fetched tree without opening the archive. The test
// is per selector and not per snapshot: the operator's question is whether
// this session was recovered here at all, and a session fetched from an older
// snapshot is still a session this machine holds.
//
// The selector arrives sanitized, which is exact here rather than approximate:
// adapter source ids are [A-Za-z0-9._-] segments and harness names are
// lowercase words, so the terminal-safe renderer is the identity on both.
func (w *webArchive) materialization(selector string) (string, bool) {
	if w.sessionsRoot == "" || selector == "" {
		return "", false
	}
	dir := filepath.Join(w.sessionsRoot, safeSessionDir(selector))
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// FetchSession runs `babel sessions fetch`. A request that names no host
// inherits the launch selection, which is the same precedence the CLI applies
// to a flag against its configured default.
func (w *webArchive) FetchSession(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
	args := []string{"sessions", "fetch", req.Selector, "--json"}
	if req.Snapshot != "" {
		args = append(args, "--snapshot", req.Snapshot)
	}
	if host := firstNonEmpty(req.Host, w.host); host != "" {
		args = append(args, "--host", host)
	}
	var res web.FetchResult
	err := w.app.runJSON(ctx, &res, append(args, w.forward...)...)
	return res, err
}

// runJSON executes one headless command in process, decoding its --json
// stdout into out. Usage rejections map to the web bad-request sentinel and
// unmatched selectors to not-found, so the HTTP layer reports honest codes.
func (a *app) runJSON(ctx context.Context, out any, args ...string) error {
	var stdout, stderr bytes.Buffer
	scratch := &app{stdout: &stdout, stderr: &stderr}
	runErr := scratch.dispatch(ctx, args)

	var decodeErr error
	if stdout.Len() > 0 {
		decodeErr = json.Unmarshal(stdout.Bytes(), out)
	}
	if runErr != nil {
		var ue *usageError
		switch {
		case errors.As(runErr, &ue):
			return fmt.Errorf("%w: %s", web.ErrBadRequest, runErr.Error())
		case strings.Contains(runErr.Error(), "no local session matches"),
			strings.Contains(runErr.Error(), "no snapshot"):
			return fmt.Errorf("%w: %s", web.ErrNotFound, runErr.Error())
		}
		if decodeErr == nil && stdout.Len() > 0 {
			// The command failed after emitting its result document (e.g.
			// a failed verify); the caller decides what to surface.
			return runErr
		}
		return runErr
	}
	if stdout.Len() == 0 {
		return fmt.Errorf("command %q produced no output", strings.Join(args, " "))
	}
	return decodeErr
}

// openBrowser best-effort launches the system browser.
func openBrowser(url string) error {
	candidates := [][]string{{"xdg-open", url}, {"open", url}}
	var firstErr error
	for _, c := range candidates {
		path, err := exec.LookPath(c[0])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return exec.Command(path, c[1:]...).Start()
	}
	return firstErr
}
