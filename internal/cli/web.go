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
	"strings"
	"syscall"
	"time"

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
	repository := firstNonEmpty(rf.repository, os.Getenv("BABEL_RESTIC_REPO"), cfg.Repository)
	passwordFile := firstNonEmpty(rf.passwordFile, os.Getenv("BABEL_RESTIC_PASSWORD_FILE"), cfg.PasswordFile)
	configured := repository != "" && passwordFile != ""

	// Explicitly forward the resolved selection so every archive
	// sub-invocation sees exactly the state the server advertised, even if
	// the environment changes while the server runs.
	var forward []string
	if configured {
		forward = append(forward, "--repo", repository, "--password-file", passwordFile)
	}
	if b := firstNonEmpty(rf.binary, cfg.ResticBinary); b != "" {
		forward = append(forward, "--restic-binary", b)
	}
	if rf.host != "" {
		forward = append(forward, "--host", rf.host)
	}

	hostID := firstNonEmpty(rf.host, os.Getenv("BABEL_HOST_ID"), cfg.HostID)
	if hostID == "" {
		if name, err := os.Hostname(); err == nil {
			hostID = sanitizeHostID(name)
		}
	}

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
		Lister: web.SessionListerFunc(func(ctx context.Context) (web.SessionsResult, error) {
			var res web.SessionsResult
			if err := a.runJSON(ctx, &res, "sessions", "list", "--json"); err != nil {
				return web.SessionsResult{}, err
			}
			res.RefreshedAt = formatTime(time.Now().UTC())
			return res, nil
		}),
		Inspector: web.SessionInspectorFunc(func(ctx context.Context, selector string) (web.InspectResult, error) {
			var res web.InspectResult
			err := a.runJSON(ctx, &res, "sessions", "inspect", selector, "--json")
			return res, err
		}),
		Transcripts: web.TranscriptReaderFunc(transcript.Events),
	}
	if configured {
		opts.Archive = &webArchive{app: a, forward: forward}
	}
	return web.New(opts)
}

// webArchive drives the archive commands in process. Only status, verify,
// and fetch exist here; the CLI exposes no deletion, so neither can the web.
type webArchive struct {
	app     *app
	forward []string
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

func (w *webArchive) FetchSession(ctx context.Context, selector, snapshot string) (web.FetchResult, error) {
	args := []string{"sessions", "fetch", selector, "--json"}
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
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
