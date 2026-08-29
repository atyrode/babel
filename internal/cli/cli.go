// Package cli implements Babel's headless command surface (SPEC.md §8):
// build identity, restic-backed archive push, status, and verify, and
// local session list, inspect, fetch, and prune.
//
// Contracts this package owns:
//
//   - machine-readable output goes to stdout and every progress line,
//     warning, and error goes to stderr, so `--json` output is always a
//     single parseable document;
//   - every untrusted dynamic value reaches a terminal only through
//     Sanitize, the one terminal-safe renderer (SPEC.md §8, §9) — that
//     includes the restic child process's own diagnostics, which embed
//     source paths;
//   - status, verify, list, inspect, and fetch never write to the
//     repository, and local prune never even constructs a restic.Repo, so
//     it cannot reach the repository by construction (SPEC.md §8). Babel
//     never runs `restic forget` or `restic prune`: never-delete is
//     policy, so no command exposes them;
//   - exit codes are 0 for success, 1 for failure, and 2 for a rejected
//     invocation;
//   - bare `babel` prints a fast offline status overview; the web
//     interface (`babel web`) is the primary interactive surface
//     (operator decision 2026-08-28) and the TUI stays minimal.
//
// Repository selection is resolved from per-invocation flags, environment
// variables, and persistent storage.json configuration. The password itself
// never appears on the child process's argv: only a password file is accepted,
// and it is handed to restic through the environment.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/restic"
)

// Exit codes. Usage errors are distinguishable from failures so a wrapper
// script can tell a bad invocation from a bad archive.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// dirPerm keeps every Babel-created directory private (SPEC.md §9).
const dirPerm = 0o700

// maxHostIDLen bounds a host identity, which becomes a restic snapshot
// host and therefore a grouping key in every status report.
const maxHostIDLen = 64

// babelTag tags every snapshot Babel creates, so an operator can tell
// Babel's snapshots apart from anything else sharing the repository.
const babelTag = "babel"

const rootUsage = `Usage: babel <command> [flags]

Commands:
  version                     print Babel's build identity
  web                         serve the local web interface (primary surface)
  storage configure           replace persistent repository configuration
  storage status              report persistent repository configuration
  archive push                back up this host's source roots into restic
  archive status              report snapshots per host
  archive verify              check repository integrity
  sessions list               list this host's local sessions
  sessions inspect SELECTOR   show one local session in full
  sessions fetch SELECTOR     restore one session's files from a snapshot
  sessions prune --local      remove locally fetched session directories

A selector is "HARNESS/SOURCE-ID", or any unambiguous suffix of one.

Repository selection for the archive commands and for sessions fetch:
  --repo REPOSITORY           else $BABEL_RESTIC_REPO, else storage.json
  --password-file FILE        else $BABEL_RESTIC_PASSWORD_FILE, else storage.json

Machine-readable output goes to stdout; diagnostics go to stderr.
Run "babel <command> -h" for a command's flags.
`

// errHelp reports that a help request was already served on stdout.
var errHelp = errors.New("cli: help served")

// usageError marks an invocation rejected before any work was attempted.
// It maps to exit code 2 and prints the offending command's usage.
type usageError struct {
	msg   string
	usage string
}

func (e *usageError) Error() string { return e.msg }

// Run executes one babel invocation and returns its process exit code.
// args excludes the program name. Run never touches os.Stdout or
// os.Stderr, so tests drive the whole surface in-process.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, os.Stdin, stdout, stderr)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	err := a.dispatch(context.Background(), args)
	switch {
	case err == nil, errors.Is(err, errHelp):
		return exitOK
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(a.stderr, "babel: %s\n", Sanitize(ue.msg))
		if ue.usage != "" {
			fmt.Fprint(a.stderr, "\n"+ue.usage)
		}
		return exitUsage
	}
	// Error text may embed adapter-supplied paths and identifiers, and
	// restic's own stderr, so it is rendered like any other untrusted
	// value.
	fmt.Fprintf(a.stderr, "babel: %s\n", Sanitize(err.Error()))
	return exitFailure
}

// app carries one invocation's input and output streams.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// dispatch routes "babel <noun> <verb>".
func (a *app) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.bare()
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, rootUsage)
		return nil
	case "version":
		return a.version(args[1:])
	case "storage":
		return a.storage(ctx, args[1:])
	case "archive":
		return a.archive(ctx, args[1:])
	case "sessions":
		return a.sessions(ctx, args[1:])
	case "web":
		return a.webCmd(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown command %q", args[0]), usage: rootUsage}
	}
}

// bare serves `babel` with no arguments: a fast, offline status overview.
// The web interface is the primary interactive surface (operator decision
// 2026-08-28); bare stays a dashboard pointer rather than growing into a
// rich TUI. It never opens the repository, so it is safe and instant.
func (a *app) bare() error {
	fmt.Fprintf(a.stdout, "%s\n\n", readBuildIdentity().String())

	cfg, found, err := config.Load()
	repository := firstNonEmpty(os.Getenv("BABEL_RESTIC_REPO"), cfg.Repository)
	passwordFile := firstNonEmpty(os.Getenv("BABEL_RESTIC_PASSWORD_FILE"), cfg.PasswordFile)
	switch {
	case err != nil:
		fmt.Fprintf(a.stdout, "storage:  unreadable configuration: %s\n", Sanitize(err.Error()))
	case repository != "" && passwordFile != "":
		fmt.Fprintf(a.stdout, "storage:  %s\n", Sanitize(repository))
	case found || repository != "" || passwordFile != "":
		fmt.Fprint(a.stdout, "storage:  incomplete; run \"babel storage status\"\n")
	default:
		fmt.Fprint(a.stdout, "storage:  not configured; run \"babel storage configure\"\n")
	}

	if d, err := babelDirs(); err == nil {
		if n, err := catalog.Count(d.data); err == nil {
			fmt.Fprintf(a.stdout, "catalog:  %d cached sessions (\"babel sessions list\" refreshes)\n", n)
		}
	}

	fmt.Fprint(a.stdout, "web:      run \"babel web\" for the browser interface\n\n")
	fmt.Fprint(a.stdout, rootUsage)
	return nil
}

// adapters returns the source adapters in a stable order. Every command
// that reads local sessions goes through this one registry, so a harness
// is never visible to one command and invisible to another.
func adapters() []adapter.Adapter {
	return []adapter.Adapter{omp.New(), codex.New(), claude.New()}
}

// cmd is one subcommand's parser: a flag set, the usage text shown for -h
// and for every rejected invocation of it, and the positional arguments the
// command accepted.
type cmd struct {
	fs    *flag.FlagSet
	usage string
	rest  []string
}

// newCmd builds a subcommand parser. Flag-package output is discarded so
// help always goes to stdout and diagnostics always go to stderr, which the
// package's default handling does not respect.
func newCmd(name, usage string) *cmd {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return &cmd{fs: fs, usage: usage}
}

// parse parses argv. Flags are accepted after positional arguments, which
// the standard flag package stops at, because "babel sessions inspect
// SELECTOR --json" is the natural invocation order. An explicit -h prints
// usage on stdout and reports errHelp; a malformed flag is a usage error.
func (c *cmd) parse(a *app, args []string) error {
	for {
		if err := c.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(a.stdout, c.usage)
				return errHelp
			}
			return &usageError{msg: err.Error(), usage: c.usage}
		}
		remaining := c.fs.Args()
		if len(remaining) == 0 {
			return nil
		}
		c.rest = append(c.rest, remaining[0])
		args = remaining[1:]
	}
}

// args returns the accepted positional arguments.
func (c *cmd) args() []string { return c.rest }

// usagef rejects an invocation with this command's usage attached.
func (c *cmd) usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...), usage: c.usage}
}

// noArgs rejects positional arguments for commands that take none.
func (c *cmd) noArgs() error {
	if len(c.rest) > 0 {
		return c.usagef("%s takes no positional arguments, got %q", c.fs.Name(), c.rest[0])
	}
	return nil
}

// oneSelector requires exactly one positional selector.
func (c *cmd) oneSelector() (string, error) {
	switch len(c.rest) {
	case 1:
		return c.rest[0], nil
	case 0:
		return "", c.usagef("%s requires a SELECTOR", c.fs.Name())
	default:
		return "", c.usagef("%s takes exactly one SELECTOR, got %d", c.fs.Name(), len(c.rest))
	}
}

// repoHint is the one-line remedy attached to every rejected repository
// selection, so a bad invocation is self-correcting.
const repoHint = `pass --repo REPOSITORY --password-file FILE, set $BABEL_RESTIC_REPO and $BABEL_RESTIC_PASSWORD_FILE, or run "babel storage configure --from-json FILE"`

// repoFlags holds repository selection from flags. Resolution adds environment
// variables and persistent storage configuration in that order.
type repoFlags struct {
	repository   string
	passwordFile string
	binary       string
	host         string
}

func (rf *repoFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&rf.repository, "repo", "", "restic repository (default $BABEL_RESTIC_REPO, else storage.json)")
	fs.StringVar(&rf.passwordFile, "password-file", "", "file holding the repository password (default $BABEL_RESTIC_PASSWORD_FILE, else storage.json)")
	fs.StringVar(&rf.binary, "restic-binary", "", `restic executable to run (default storage.json, else "restic" from $PATH)`)
	fs.StringVar(&rf.host, "host", "", "archive host identity (default $BABEL_HOST_ID, else storage.json, else the system hostname)")
}

// open resolves the repository selection and opens it. Open performs no
// repository I/O, so this is cheap and each command decides what it
// reads. The resolved values are written back onto rf, so a caller
// reports the repository it actually opened rather than the flag it was
// given. diagnostics, when non-nil, receives restic's per-file warning
// lines; callers pass a sanitizing writer because those lines carry
// source paths.
func (rf *repoFlags) open(c *cmd, d dirs, diagnostics io.Writer) (*restic.Repo, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, err
	}
	rf.repository = firstNonEmpty(rf.repository, os.Getenv("BABEL_RESTIC_REPO"), cfg.Repository)
	if rf.repository == "" {
		return nil, c.usagef("no restic repository selected: %s", repoHint)
	}
	rf.passwordFile = firstNonEmpty(rf.passwordFile, os.Getenv("BABEL_RESTIC_PASSWORD_FILE"), cfg.PasswordFile)
	if rf.passwordFile == "" {
		return nil, c.usagef("no repository password file selected: %s", repoHint)
	}
	rf.binary = firstNonEmpty(rf.binary, cfg.ResticBinary)
	cacheDir := filepath.Join(d.cache, "restic")
	if err := ensureDir(cacheDir); err != nil {
		return nil, err
	}
	var keyID, keySecret string
	if s := cfg.RepositoryStore; s != nil {
		keyID, keySecret = s.AccessKeyID, s.SecretAccessKey
	}
	repo, err := restic.Open(restic.Config{
		Repository:      rf.repository,
		PasswordFile:    rf.passwordFile,
		Binary:          rf.binary,
		CacheDir:        cacheDir,
		AccessKeyID:     keyID,
		SecretAccessKey: keySecret,
		Diagnostics:     diagnostics,
	})
	if err != nil {
		return nil, fmt.Errorf("select repository: %w", err)
	}
	return repo, nil
}

// hostID resolves this host's stable archive identity (SPEC.md §6.1): the
// --host flag, else $BABEL_HOST_ID, else storage.json, else the system
// hostname lowercased and sanitized. It becomes the restic snapshot host,
// which is how status groups an archive shared by several machines.
func (rf *repoFlags) hostID(c *cmd) (string, error) {
	if rf.host != "" {
		if !validHostID(rf.host) {
			return "", c.usagef("invalid --host %q: host ids are 1-%d characters of [a-z0-9._-] starting alphanumeric", rf.host, maxHostIDLen)
		}
		return rf.host, nil
	}
	if v := os.Getenv("BABEL_HOST_ID"); v != "" {
		if !validHostID(v) {
			return "", fmt.Errorf("BABEL_HOST_ID %q is not a valid host id", v)
		}
		return v, nil
	}
	cfg, _, err := config.Load()
	if err != nil {
		return "", err
	}
	if cfg.HostID != "" {
		if !validHostID(cfg.HostID) {
			return "", fmt.Errorf("storage configuration host_id %q is not a valid host id", cfg.HostID)
		}
		return cfg.HostID, nil
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "", errors.New("cannot determine host identity: pass --host ID, set BABEL_HOST_ID, or configure host_id")
	}
	id := sanitizeHostID(name)
	if !validHostID(id) {
		return "", fmt.Errorf("system hostname %q yields no valid host id: pass --host ID", name)
	}
	return id, nil
}

// validHostID delegates to the persistent configuration package so flags,
// environment variables, and storage.json share one validation rule.
func validHostID(s string) bool {
	return config.ValidHostID(s)
}

// sanitizeHostID maps a system hostname onto validHostID: lowercased,
// characters outside [a-z0-9._-] replaced by "-", leading punctuation
// dropped, and the result bounded to the host-id length limit.
func sanitizeHostID(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.TrimLeft(b.String(), "._-")
	if len(s) > maxHostIDLen {
		s = s[:maxHostIDLen]
	}
	return s
}

// dirs holds Babel's private XDG locations (SPEC.md §9): the data
// directory owns fetched session trees, which local prune is the only
// command allowed to remove from, and the cache directory owns restic's
// disposable metadata cache. Babel keeps no state directory: restic owns
// the archive's state, so there is nothing local to journal.
type dirs struct {
	data  string
	cache string
}

func babelDirs() (dirs, error) {
	data, err := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return dirs{}, err
	}
	cache, err := xdgDir("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return dirs{}, err
	}
	return dirs{data: data, cache: cache}, nil
}

// xdgDir resolves one XDG base directory's babel subdirectory, honouring
// the environment override and falling back to the specified default
// relative to the home directory.
func xdgDir(env, fallback string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return filepath.Join(v, "babel"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot resolve %s: no home directory", env)
	}
	return filepath.Join(home, fallback, "babel"), nil
}

// ensureDir creates one private Babel directory.
func ensureDir(path string) error {
	if err := os.MkdirAll(path, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// sessionsRoot is where fetched session trees live (SPEC.md §9): a
// rebuildable tree under the data directory, and the only tree local
// prune is allowed to remove from.
func (d dirs) sessionsRoot() string { return filepath.Join(d.data, "sessions") }

// emitJSON writes one machine-readable result document to stdout. It is the
// only thing a --json invocation writes there.
func (a *app) emitJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// diagf writes one diagnostic line to stderr. Untrusted values must be
// rendered through Sanitize by the caller, because a diagnostic is
// composed of layout plus values and Sanitize deliberately escapes layout.
func (a *app) diagf(format string, args ...any) {
	fmt.Fprintf(a.stderr, format, args...)
}

// maxDiagLine bounds one buffered subprocess diagnostic line, so a child
// that never emits a newline cannot grow Babel's memory without limit.
const maxDiagLine = 64 << 10

// sanitizingWriter forwards a child process's diagnostics to a stream one
// sanitized line at a time. restic's per-file warnings quote source
// paths, which are as untrusted as any other adapter-supplied value, so
// they must not reach a terminal raw. Sanitize escapes newlines, so
// splitting on them first is what keeps the output readable.
type sanitizingWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func (sw *sanitizingWriter) Write(p []byte) (int, error) {
	sw.buf = append(sw.buf, p...)
	for {
		i := bytes.IndexByte(sw.buf, '\n')
		if i < 0 {
			break
		}
		sw.emit(sw.buf[:i])
		sw.buf = append(sw.buf[:0], sw.buf[i+1:]...)
	}
	if len(sw.buf) > maxDiagLine {
		sw.emit(sw.buf)
		sw.buf = sw.buf[:0]
	}
	return len(p), nil
}

func (sw *sanitizingWriter) emit(line []byte) {
	text := strings.TrimRight(string(line), "\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	fmt.Fprintf(sw.w, "%s%s\n", sw.prefix, Sanitize(text))
}

// firstNonEmpty returns the first non-empty value, which is how every
// flag-else-environment default is resolved.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// plural renders a count's unit without inventing a word for zero.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sortedUnique returns the distinct values of in in ascending order.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
