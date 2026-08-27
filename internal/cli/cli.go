// Package cli implements Babel's headless command surface (SPEC.md §8) for
// the local development/recovery milestone: version reporting, archive
// publication, catalog/status/verify reads, and session list, inspect,
// fetch, and local prune.
//
// Contracts this package owns:
//
//   - machine-readable output goes to stdout and every progress line,
//     warning, and error goes to stderr, so `--json` output is always a
//     single parseable document;
//   - every untrusted dynamic value reaches a terminal only through
//     Sanitize, the one terminal-safe renderer (SPEC.md §8, §9);
//   - catalog, status, verify, inspect, and fetch are read-only with
//     respect to the object store, and local prune never opens a store at
//     all — it cannot touch the remote by construction (SPEC.md §8);
//   - exit codes are 0 for success, 1 for failure, and 2 for a rejected
//     invocation;
//   - bare `babel` is reserved for the future TUI (SPEC.md §2.4, §8.1); it
//     prints a notice plus usage and succeeds.
//
// Storage selection is the ad-hoc `--archive-backend`/`--archive-root`
// development workflow of SPEC.md §8; persistent configuration
// (`storage.json`) is out of scope for this milestone, so the flags are
// required wherever a store is needed.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/objectstore/local"
	"github.com/atyrode/babel/internal/objectstore/rclonestore"
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

const rootUsage = `Usage: babel <command> [flags]

Commands:
  version                     print Babel's build identity
  archive push                publish this host's local sessions
  archive catalog             list committed host generations
  archive status              report head, bootstrap, hint, and journal state
  archive verify              verify committed state
  sessions list               list committed sessions
  sessions inspect SELECTOR   show one revision in full
  sessions fetch SELECTOR     materialize one revision locally
  sessions prune --local      remove locally fetched bundles

A selector is "SESSION" (newest committed revision) or "SESSION@sha256:<hex>"
(exactly that revision).

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
	a := &app{stdout: stdout, stderr: stderr}
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
	// Error text may embed adapter-supplied paths and identifiers, so it is
	// rendered like any other untrusted value.
	fmt.Fprintf(a.stderr, "babel: %s\n", Sanitize(err.Error()))
	return exitFailure
}

// app carries one invocation's output streams.
type app struct {
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
	case "archive":
		return a.archive(ctx, args[1:])
	case "sessions":
		return a.sessions(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown command %q", args[0]), usage: rootUsage}
	}
}

// bare serves `babel` with no arguments. The interactive TUI is the
// intended primary surface (SPEC.md §2.4, §8.1) and lands with its own
// phase; until then the name prints a notice plus the headless usage and
// exits successfully rather than pretending to be a TUI.
func (a *app) bare() error {
	fmt.Fprint(a.stdout, "babel: the interactive TUI is not implemented yet; the headless commands below are the current surface.\n\n")
	fmt.Fprint(a.stdout, rootUsage)
	return nil
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

// stringList collects a repeatable string flag, such as --host.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	if v == "" {
		return errors.New("empty value")
	}
	*l = append(*l, v)
	return nil
}

// storeFlags is the ad-hoc store selection of SPEC.md §8. Both flags are
// required for this milestone because persistent storage configuration
// (`$XDG_CONFIG_HOME/babel/storage.json`) is not implemented yet.
type storeFlags struct {
	backend string
	root    string
}

func (s *storeFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&s.backend, "archive-backend", "", "archive backend: local or rclone (required)")
	fs.StringVar(&s.root, "archive-root", "", "local archive root directory, or rclone remote (required)")
}

// open selects and opens the object store. It performs no archive I/O: a
// store value is cheap and every command below decides what it reads.
func (s *storeFlags) open(c *cmd) (objectstore.Store, error) {
	switch s.backend {
	case "":
		return nil, c.usagef("--archive-backend local|rclone is required (storage.json configuration is not implemented yet)")
	case "local":
		if s.root == "" {
			return nil, c.usagef("--archive-backend local requires --archive-root PATH")
		}
		st, err := local.New(s.root)
		if err != nil {
			return nil, fmt.Errorf("open local archive %s: %w", s.root, err)
		}
		return st, nil
	case "rclone":
		if s.root == "" {
			return nil, c.usagef("--archive-backend rclone requires --archive-root REMOTE:PATH")
		}
		return rclonestore.New(s.root), nil
	default:
		return nil, c.usagef("unknown --archive-backend %q (want local or rclone)", s.backend)
	}
}

// hostFilter validates a repeatable --host selection used by the read
// commands. An empty selection means every discoverable host.
func hostFilter(c *cmd, hosts stringList) ([]string, error) {
	for _, h := range hosts {
		if !archive.ValidName(h) {
			return nil, c.usagef("invalid --host %q: host ids are 1-64 characters of [a-z0-9._-] starting alphanumeric", h)
		}
	}
	return hosts, nil
}

// dirs holds Babel's private XDG locations (SPEC.md §9): the state
// directory owns the publication journal, the data directory owns fetched
// bundles, and the cache directory owns disposable staging.
type dirs struct {
	state string
	data  string
	cache string
}

func babelDirs() (dirs, error) {
	state, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if err != nil {
		return dirs{}, err
	}
	data, err := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return dirs{}, err
	}
	cache, err := xdgDir("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return dirs{}, err
	}
	return dirs{state: state, data: data, cache: cache}, nil
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

// bundlesRoot is where fetched immutable bundles live (SPEC.md §9): a
// rebuildable tree under the data directory, which local prune is the only
// command allowed to remove from.
func (d dirs) bundlesRoot() string { return filepath.Join(d.data, "bundles") }

// stagingRoot is the disposable publication staging area under the cache
// directory.
func (d dirs) stagingRoot() string { return filepath.Join(d.cache, "staging") }

// resolveHostID resolves this host's stable archive identity (SPEC.md
// §6.1): the --host flag, else $BABEL_HOST_ID, else the system hostname
// lowercased and sanitized to a valid archive name.
func resolveHostID(c *cmd, flagValue string) (string, error) {
	if flagValue != "" {
		if !archive.ValidName(flagValue) {
			return "", c.usagef("invalid --host %q: host ids are 1-64 characters of [a-z0-9._-] starting alphanumeric", flagValue)
		}
		return flagValue, nil
	}
	if v := os.Getenv("BABEL_HOST_ID"); v != "" {
		if !archive.ValidName(v) {
			return "", fmt.Errorf("BABEL_HOST_ID %q is not a valid archive host id", v)
		}
		return v, nil
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "", errors.New("cannot determine host identity: pass --host ID or set BABEL_HOST_ID")
	}
	id := sanitizeHostID(name)
	if !archive.ValidName(id) {
		return "", fmt.Errorf("system hostname %q yields no valid archive host id: pass --host ID", name)
	}
	return id, nil
}

// sanitizeHostID maps a system hostname onto archive.ValidName: lowercased,
// characters outside [a-z0-9._-] replaced by "-", leading punctuation
// dropped, and the result bounded to the 64-byte name limit.
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
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

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
// rendered through cell or Sanitize by the caller, because a diagnostic is
// composed of layout plus values and Sanitize deliberately escapes layout.
func (a *app) diagf(format string, args ...any) {
	fmt.Fprintf(a.stderr, format, args...)
}

// plural renders a count's unit without inventing a word for zero.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
