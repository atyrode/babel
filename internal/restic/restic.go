// Package restic wraps the restic command-line interface, which owns
// Babel's archival storage: deduplicating snapshots, integrity checks, and
// restores. Babel never reimplements repository formats and never deletes
// history — no forget, no prune, no repair — because never-delete is policy.
// Unlock is the one operation here that removes anything, and what it removes
// is a lock file: the coordination record restic writes while a command runs.
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
	"encoding/json"
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
// restic's exit code, and an executable that cannot be run carries a
// *BinaryError, both matched with errors.As.
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

	// AccessKeyID and SecretAccessKey authenticate an object-store repository.
	// Both empty means the repository needs no credential, which is the case
	// for a local path.
	//
	// These are the one secret this package puts in the child's environment,
	// and only because restic offers no file reference for them the way it does
	// for the repository password. The exposure is bounded to the same user:
	// argv still carries nothing, and the password file's contents are equally
	// readable by whoever can read this process's environment.
	AccessKeyID     string
	SecretAccessKey string

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
//
// An explicit Config.Binary goes through exec.LookPath too, not only a bare
// name. LookPath on a path checks that the file exists and is executable, so
// an unusable executable is caught here, once, as a *BinaryError — rather than
// once per operation as "fork/exec /some/restic: no such file or directory",
// which names neither what Babel was doing nor what would fix it.
func (r *Repo) prepare() error {
	r.prepOnce.Do(func() {
		name := r.cfg.Binary
		if name == "" {
			name = "restic"
		}
		bin, err := exec.LookPath(name)
		if err != nil {
			r.prepErr = &BinaryError{Path: name, err: err}
			return
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
	// The object-store credential, when the repository has one. restic reads
	// these names and offers no file-based alternative for them.
	if r.cfg.AccessKeyID != "" {
		env = append(env,
			"AWS_ACCESS_KEY_ID="+r.cfg.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+r.cfg.SecretAccessKey,
		)
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

// BinaryError reports that the restic executable itself could not be run:
// absent, not executable, or not where it was said to be. It is a distinct
// type because it is a distinct problem — the repository was never contacted,
// no locator and no password is implicated, and nothing was written — and
// because a caller has to be able to tell it apart to say what fixes it.
//
// Saying that is deliberately not this package's job. The setting that selects
// the executable belongs to whoever configured this Repo; a storage wrapper
// that printed a flag name would be describing a command line it does not own.
// So this names the path that was tried and stops there, and callers match it
// with errors.As to add their own remedy.
type BinaryError struct {
	// Path is the executable that was looked up: Config.Binary, or "restic"
	// when the lookup went through PATH.
	Path string
	err  error
}

func (e *BinaryError) Error() string { return fmt.Sprintf("restic: locating binary: %v", e.err) }

// Unwrap exposes exec.LookPath's failure, so exec.ErrNotFound and
// fs.ErrPermission stay matchable.
func (e *BinaryError) Unwrap() error { return e.err }

// ExitError reports a restic invocation that failed. Code is restic's exit
// status, or -1 when the process could not be run or was killed by a
// signal. Stderr is a bounded tail of restic's diagnostics, rendered as one
// line and with restic's --json error envelope unwrapped to the prose inside
// it (see unwrapExitEnvelope); restic never prints the repository password, so
// it is safe to surface.
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
// wrapped errors. restic's --json error envelope is unwrapped to the prose
// inside it, whose own newlines then fold into the same separator as every
// other line. Truncation is marked with a leading ellipsis.
func (t *tailBuffer) String() string {
	var parts []string
	for _, line := range strings.Split(string(t.buf), "\n") {
		// Split again: an unwrapped envelope message is multi-line whenever
		// restic's fatal error was, and a raw line simply has no newline
		// left to split on.
		for _, part := range strings.Split(unwrapExitEnvelope(line), "\n") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
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

// exitEnvelope is restic's fatal-error message. restic reports a fatal error
// as prose on stderr normally, but as this one JSON object when --json is in
// effect, which is every invocation Babel needs machine-readable output from:
// snapshots, ls, and backup. backupMessage in ops.go parses the same
// message_type out of the backup stream, where it is one of several; here it is
// the only one that matters.
type exitEnvelope struct {
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
}

// unwrapExitEnvelope returns the prose inside restic's --json fatal-error
// envelope, or line unchanged when line is not one.
//
// A failing `restic snapshots --json` writes its fatal error as
//
//	{"message_type":"exit_error","code":12,"message":"Fatal: wrong password or no key found"}
//
// which is accurate and unlike every other error Babel writes: the operator has
// to read past message_type to reach the cause. Only the framing is removed.
// The message is surfaced whole, never summarized or trimmed, because restic's
// diagnostics carry the remedy — `restic repair packs <id>` after a failed
// `check --read-data` is the one that matters most — and a shortened one would
// cost the operator the fix. The exit code is not read from here either:
// ExitError.Code already has it from the process, which is what isMissingRepo
// tests and what survives a tail truncated mid-object.
//
// Anything that is not the envelope is returned unchanged rather than dropped:
// restic invoked without --json, an older restic, a wrapper's own noise, or a
// tail cut mid-object must all still reach the caller. A parser that assumed
// JSON would turn a real diagnostic into an empty error, which is a worse
// failure than an ugly one.
func unwrapExitEnvelope(line string) string {
	trimmed := strings.TrimSpace(line)
	// Cheap rejection first: the tail of a backup mirrors every ndjson line
	// restic emitted, and none of the status ones are worth unmarshalling.
	if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"exit_error"`) {
		return line
	}
	var env exitEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return line
	}
	if env.MessageType != "exit_error" || strings.TrimSpace(env.Message) == "" {
		return line
	}
	return env.Message
}
