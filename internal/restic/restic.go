// Package restic wraps the restic command-line interface, which owns
// Babel's archival storage: deduplicating snapshots, integrity checks, and
// restores. Babel never reimplements repository formats and never deletes
// history — no forget, no prune — because never-delete is policy.
//
// The wrapper is a thin, contract-bearing shell over the `restic` binary:
//
//   - The repository password is NEVER passed on argv and never logged.
//     Credentials reach the child only as RESTIC_PASSWORD_FILE in a minimal
//     environment, so a password cannot leak through process listings.
//   - Every child process gets a minimal environment
//     (RESTIC_REPOSITORY, RESTIC_PASSWORD_FILE, RESTIC_CACHE_DIR, plus HOME,
//     PATH and TMPDIR when the parent has them), so behaviour does not drift
//     with the ambient environment of whoever launched babel.
//   - Every operation honours its context and returns errors naming the
//     operation, carrying a bounded tail of restic's stderr for diagnosis.
//
// Snapshots are crash-consistent per file, not transactional across files:
// a backup taken while a session file is being appended to may capture a
// torn final line. Readers tolerate that; the next snapshot supersedes it.
package restic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Sentinel errors reported by this package. Callers match them with
// errors.Is; nonzero-exit failures additionally carry an *ExitError with
// restic's exit code, matched with errors.As.
var (
	// ErrRepositoryRequired reports a Config without a repository location.
	ErrRepositoryRequired = errors.New("restic: repository is required")

	// ErrPasswordFileRequired reports a Config without a password file. The
	// password itself is never accepted inline, so it can never reach argv.
	ErrPasswordFileRequired = errors.New("restic: password file is required")

	// ErrIncomplete reports that restic finished a backup but could not read
	// every source file (restic exit status 3). The snapshot exists and the
	// BackupSummary is returned alongside this error: the caller decides
	// whether a partial snapshot is acceptable.
	ErrIncomplete = errors.New("restic: backup incomplete")

	// ErrNoSummary reports a backup that exited successfully without
	// emitting the summary message the JSON protocol promises.
	ErrNoSummary = errors.New("restic: backup produced no summary")
)

// restic exit statuses this package interprets. See restic's documentation
// on exit codes; unlisted statuses are surfaced verbatim in *ExitError.
const (
	exitIncomplete  = 3  // backup completed, some source files unreadable
	exitNoSuchRepo  = 10 // repository does not exist
	stderrTailLimit = 4 << 10
)

// Config describes one restic repository and how to talk to it. A zero
// Binary, CacheDir or Diagnostics selects the documented default.
type Config struct {
	// Repository is the restic repository location (RESTIC_REPOSITORY),
	// e.g. a local path or a backend URL. Required.
	Repository string

	// PasswordFile is the path to a file holding the repository password
	// (RESTIC_PASSWORD_FILE). Required: the password is only ever handed to
	// restic by path, never on argv and never in this process's memory.
	PasswordFile string

	// Binary is the restic executable. Empty means "restic", resolved
	// through PATH on first use rather than at Open time.
	Binary string

	// CacheDir is the restic cache directory (RESTIC_CACHE_DIR). Empty
	// selects os.UserCacheDir()/babel-restic, falling back to
	// os.TempDir()/babel-restic when the user cache dir is undiscoverable.
	// It is created on first use with mode 0700.
	CacheDir string

	// Diagnostics receives non-fatal, per-item restic warnings emitted
	// during Backup — one line per unreadable file, naming the path and
	// restic's message. Never session content. Nil discards them.
	Diagnostics io.Writer
}

// Repo is an opened handle on one restic repository. It holds no
// connection: each operation runs one restic child process. A Repo is safe
// for concurrent use.
type Repo struct {
	cfg Config

	prepOnce sync.Once
	binPath  string
	cacheDir string
	prepErr  error

	// diagMu serializes writes to Config.Diagnostics, which Backup feeds
	// from both of restic's output streams at once.
	diagMu sync.Mutex
}

// Open validates cfg and returns a handle. It performs no I/O: neither the
// repository nor the password file nor the restic binary is touched, so
// Open never blocks and never reports repository state. Use EnsureInit for
// that.
func Open(cfg Config) (*Repo, error) {
	if strings.TrimSpace(cfg.Repository) == "" {
		return nil, ErrRepositoryRequired
	}
	if strings.TrimSpace(cfg.PasswordFile) == "" {
		return nil, ErrPasswordFileRequired
	}
	return &Repo{cfg: cfg}, nil
}

// prepare resolves the restic binary and materializes the cache directory,
// exactly once per Repo.
func (r *Repo) prepare() error {
	r.prepOnce.Do(func() {
		bin := r.cfg.Binary
		if bin == "" {
			resolved, err := exec.LookPath("restic")
			if err != nil {
				r.prepErr = fmt.Errorf("restic: locating binary: %w", err)
				return
			}
			bin = resolved
		}

		dir := r.cfg.CacheDir
		if dir == "" {
			base, err := os.UserCacheDir()
			if err != nil {
				base = os.TempDir()
			}
			dir = filepath.Join(base, "babel-restic")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r.prepErr = fmt.Errorf("restic: preparing cache dir: %w", err)
			return
		}

		r.binPath, r.cacheDir = bin, dir
	})
	return r.prepErr
}

// command builds the child process for one restic invocation: minimal
// environment, credentials by file reference only, no stdin. The returned
// Cmd's Args never contain the repository password.
func (r *Repo) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if err := r.prepare(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.binPath, args...)
	cmd.Env = r.env()
	cmd.Stdin = nil
	return cmd, nil
}

// env is the child environment: repository coordinates plus the few
// variables a subprocess legitimately needs. Nothing else is inherited, and
// RESTIC_PASSWORD is deliberately absent.
func (r *Repo) env() []string {
	env := []string{
		"RESTIC_REPOSITORY=" + r.cfg.Repository,
		"RESTIC_PASSWORD_FILE=" + r.cfg.PasswordFile,
		"RESTIC_CACHE_DIR=" + r.cacheDir,
	}
	for _, key := range [...]string{"HOME", "PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// run executes one restic invocation, buffering stdout and keeping a
// bounded tail of stderr for error reporting. op names the operation in
// errors.
func (r *Repo) run(ctx context.Context, op string, args ...string) ([]byte, error) {
	cmd, err := r.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	stderr := &tailBuffer{limit: stderrTailLimit}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), wrapExit(ctx, op, err, stderr)
	}
	return stdout.Bytes(), nil
}

// ExitError reports a restic invocation that failed. Code is restic's exit
// status, or -1 when the process could not be run or was killed by a
// signal. Stderr is a bounded tail of restic's diagnostics; restic never
// prints the repository password, so it is safe to surface.
type ExitError struct {
	Op     string
	Code   int
	Stderr string
	err    error
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("restic %s: %v", e.Op, e.err)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Unwrap exposes the underlying *exec.ExitError or exec failure.
func (e *ExitError) Unwrap() error { return e.err }

// wrapExit converts a child-process failure into an *ExitError, or into the
// context's error when the context ended the process (an exec failure
// caused by cancellation says only "signal: killed", which is useless to a
// caller).
func wrapExit(ctx context.Context, op string, err error, stderr *tailBuffer) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("restic %s: %w", op, ctxErr)
	}
	code := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	tail := ""
	if stderr != nil {
		tail = stderr.String()
	}
	return &ExitError{Op: op, Code: code, Stderr: tail, err: err}
}

// exitCode reports the restic exit status carried by err, or -1.
func exitCode(err error) int {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return -1
}

// tailBuffer keeps at most the last limit bytes written to it, so a
// runaway child cannot balloon an error message.
type tailBuffer struct {
	limit   int
	buf     []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if t.limit <= 0 {
		return n, nil
	}
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - t.limit; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
		t.dropped = true
	}
	return n, nil
}

// String renders the retained tail as a single line, so it composes with
// wrapped errors. Truncation is marked with a leading ellipsis.
func (t *tailBuffer) String() string {
	var parts []string
	for _, line := range strings.Split(string(t.buf), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	joined := strings.Join(parts, "; ")
	if joined == "" {
		return ""
	}
	if t.dropped {
		return "..." + joined
	}
	return joined
}
