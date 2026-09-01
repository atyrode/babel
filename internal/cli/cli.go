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
//     it cannot reach the repository by construction (SPEC.md §8). Exactly
//     one command removes anything from the repository: `archive unlock`
//     clears restic's own stale lock files - coordination state, never
//     archived data - and only when an operator types it, since no timer or
//     conductor duty can reach a CLI verb. Babel never runs `restic
//     forget`, `restic prune` or `restic repair`: never-delete is policy,
//     so no command exposes them;
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
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/presence"
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
  storage migrate             apply pending shared catalog migrations
  storage status              report persistent repository configuration
  storage verify              check the configured shared catalog live
  storage rebuild --host ID   rebuild one host's catalog rows from restic
  archive init                create the deployment's restic repository, once
  archive push                back up this host's source roots into restic
  archive status              report snapshots per host
  archive fleet               report whether every host has published recently
  archive verify              check repository integrity
  archive unlock              remove stale repository locks, one operator step
  sync                        publish this host's durable records to the fleet
  fleet records               list every host's committed analysis
  fleet ingest                index every host's committed analysis locally
  sessions list               list this host's local sessions
  sessions inspect SELECTOR   show one local session in full
  sessions title infer        have a model write titles for untitled sessions
  sessions title clear        withdraw model-written titles
  sessions fetch SELECTOR     restore one session's files from a snapshot
  sessions prune --local      remove locally fetched session directories
  prepare [SELECTOR...]       fix an exploration's corpus scope
  explore --preparation ID    run one exploration through Code
  conductor run               run the autonomous cycle loop in the foreground
  conductor status            report the loop, its queues and its spend
  conductor configure         set the budget ceilings the loop runs under
  hypotheses                  list the candidate frontier
  hypothesis show ID          show one candidate with its whole history
  findings                    list consolidated findings
  finding show ID             show one finding with its evidence
  revisions ID                show one record's append-only revision chain
  revise ID                   append an attributed revision to a candidate
  revive ID                   return a resting candidate to the frontier
  invite ID                   invite the next run to process a record further
  tell [TEXT]                 tell Babel something it did not ask about
  invitations                 list the open process-further queue
  dispositions                list the proposed next actions on records
  disposition show ID         show one proposed action with its ledger
  disposition propose ID      attach a proposed action to a record by hand
  disposition accept ID       record that the operator authorized it
  disposition decline ID      record that the operator declined it
  review queue                list records awaiting a decision
  review decide ID            record one attributed review decision
  review history ID           show one record's append-only decisions
  export ID                   render one record to stdout or a file
  reality inbox               list the prioritized Question inbox
  reality entity ID           show one entity, its aliases and its facts
  reality answer QUESTION_ID  record an attributed answer
  reality accept PLAN_ID      accept one interpreter plan
  reality import --source ID  apply one trusted source's versioned fact batch
  cookbook list               list the analysis recipes
  cookbook check              check recipe versions against their bodies
  analysis profile configure  hand this terminal to Code's configuration
  analysis profile show       show the stored Code profile reference
  titles configure            hand this terminal to Code and store the
                              reference title inference uses
  titles show                 show the stored title-inference reference
  conformance WORKER          run the analysis-worker contract suite

A selector is "HARNESS/SOURCE-ID", or any unambiguous suffix of one. It may
begin with "-" — every Claude Code and OMP source id does, because they encode
a workspace path — and is then still read as a selector, never as a flag.

Repository selection for the archive commands and for sessions fetch:
  --repo REPOSITORY           else $BABEL_RESTIC_REPO, else storage.json
  --password-file FILE        else $BABEL_RESTIC_PASSWORD_FILE, else storage.json

Review, answer, and plan acceptance are attributed decisions (SPEC.md §4.7,
§4.8): pass --operator ID or set $BABEL_OPERATOR. No command publishes an
issue, writes to a source repository, or applies a proposal (§4.6).

Machine-readable output goes to stdout; diagnostics go to stderr.
Run "babel <command> -h" for a command's flags.
`

// errHelp reports that a help request was already served on stdout.
var errHelp = errors.New("cli: help served")

// errReported marks a failure whose explanation the command already wrote to
// stderr itself. It exists for the diagnostics that are several lines of
// remedy rather than one sentence: Sanitize escapes newlines, because it
// renders values and never layout, so a multi-line remedy has to be composed
// by the command that owns the layout instead of handed to run as one string.
var errReported = errors.New("cli: failure already reported")

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
	case errors.Is(err, errReported):
		// The command already wrote its own multi-line explanation.
		return exitFailure
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

// app carries one invocation's input and output streams, and the two shared-mode
// dependencies a fleet read needs.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// fleetRead is the shared-catalog read surface. It is injected rather than
	// constructed at every call site so that exactly one function resolves it
	// (fleetReader) and everything above that takes it as an argument, which is
	// what lets the fleet rendering be exercised without a PostgreSQL server.
	// Nil means unresolved, not absent: a command that needs one asks
	// fleetReader, which opens one or reports why there is no fleet.
	fleetRead *fleet.Reader
	// journal is this machine's publication journal, the only thing that can
	// answer "is this staged" about a record with no remote row at all — the
	// visibly pending outage staging SPEC.md §9 requires. Nil means unresolved
	// rather than absent, on fleetRead's terms: a listing that renders the sync
	// column asks syncJournalRead, which opens one in shared mode and reports
	// nothing in local mode, where "local" is what the records are.
	journal syncJournal
	// presenceRead is the fleet presence read surface (#118), injected for
	// fleetRead's reason and one more: presence's interesting states are a
	// stale heartbeat and a lost one, which a healthy deployment cannot
	// produce on demand, so the only way the rendering of them is reachable at
	// all is by handing the command a reader. Nil means unresolved rather than
	// absent, and presenceReader opens one or reports why there is no fleet.
	presenceRead presence.Reader
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
	case "sync":
		return a.syncCmd(ctx, args[1:])
	case "fleet":
		return a.fleetCmd(ctx, args[1:])
	case "sessions":
		return a.sessions(ctx, args[1:])
	case "web":
		return a.webCmd(ctx, args[1:])
	case "prepare":
		return a.prepare(ctx, args[1:])
	case "explore":
		return a.explore(ctx, args[1:])
	case "analysis":
		return a.analysis(ctx, args[1:])
	case "titles":
		return a.titlesCmd(ctx, args[1:])
	case "hypotheses":
		return a.hypothesesCmd(ctx, args[1:])
	case "hypothesis":
		return a.hypothesisCmd(ctx, args[1:])
	case "findings":
		return a.findingsCmd(ctx, args[1:])
	case "finding":
		return a.findingCmd(ctx, args[1:])
	case "revisions":
		return a.revisionsCmd(ctx, args[1:])
	case "revise":
		return a.reviseCmd(ctx, args[1:])
	case "revive":
		return a.reviveCmd(ctx, args[1:])
	case "tell":
		return a.tellCmd(ctx, args[1:])
	case "invite":
		return a.inviteCmd(ctx, args[1:])
	case "invitations":
		return a.invitationsCmd(ctx, args[1:])
	case "dispositions":
		return a.dispositionsCmd(ctx, args[1:])
	case "disposition":
		return a.dispositionCmd(ctx, args[1:])
	case "review":
		return a.review(ctx, args[1:])
	case "export":
		return a.exportCmd(ctx, args[1:])
	case "reality":
		return a.reality(ctx, args[1:])
	case "cookbook":
		return a.cookbookCmd(args[1:])
	case "conductor":
		return a.conductorCmd(ctx, args[1:])
	case "conformance":
		return a.conformanceCmd(ctx, args[1:])
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
//
// Operands are separated from the flags before the flag package sees them,
// so a positional argument beginning with "-" is still an operand. That is
// not an edge case: every Claude Code and OMP project directory encodes its
// workspace path by replacing the separators with dashes, so a pasted bare
// source id normally starts with one, and requiring the operator to discover
// the "--" terminator before a copy-paste works is a trap. Flag validation
// is not weakened in exchange: classify states exactly which tokens the flag
// package still rules on, and the arity checks below reject an operand no
// command had room for, naming the flag reading it may have been.
func (c *cmd) parse(a *app, args []string) error {
	flags, operands := c.partition(args)
	if err := c.fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(a.stdout, c.usage)
			return errHelp
		}
		return &usageError{msg: err.Error(), usage: c.usage}
	}
	c.rest = operands
	return nil
}

// tokenKind classifies one argv token standing at flag position.
type tokenKind int

const (
	// tokenOperand is a positional argument: either the flag package would
	// stop at it, or it is a dash-leading operand it must not be shown.
	tokenOperand tokenKind = iota
	// tokenFlag is a token the flag package parses, or rejects, on its own.
	tokenFlag
	// tokenValueFlag is a defined flag that consumes the following token.
	tokenValueFlag
	// tokenTerminator is the bare "--".
	tokenTerminator
)

// partition splits argv into the flag arguments handed to the flag package
// and the positional operands it never sees. Order is preserved within each
// half, which is what lets flags follow operands without the flag package
// stopping at the first operand.
func (c *cmd) partition(args []string) (flags, operands []string) {
	for i := 0; i < len(args); i++ {
		switch c.classify(args[i]) {
		case tokenTerminator:
			// The explicit terminator keeps working and keeps meaning exactly
			// what it says: every remaining token is an operand.
			return flags, append(operands, args[i+1:]...)
		case tokenValueFlag:
			// A non-boolean flag spelled without "=" takes the next token as
			// its value, dash or no dash, so that token is never an operand.
			flags = append(flags, args[i])
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case tokenFlag:
			flags = append(flags, args[i])
		case tokenOperand:
			operands = append(operands, args[i])
		}
	}
	return flags, operands
}

// classify decides what one token at flag position is, mirroring the flag
// package's own syntax rules so that every token handed back to it parses
// exactly as it would have.
//
// The single judgement call is an undefined name, because a mistyped "-jsn"
// and a source id like "-home-operator-project" have the same shape. It is
// read as a flag whenever only a flag could have been meant: the long
// "--jsn" spelling, which no source id has because no workspace path begins
// with two separators; the malformed spellings ("---x", "-=x") only the flag
// package words correctly; and the help names the flag package serves
// itself. Everything else is an operand, and an operand no command had room
// for is restated as an undefined flag by the arity check, so a short
// misspelling is still rejected rather than swallowed.
func (c *cmd) classify(s string) tokenKind {
	if s == "--" {
		return tokenTerminator
	}
	if !dashLeading(s) {
		return tokenOperand
	}
	name, _, hasValue := strings.Cut(strings.TrimPrefix(s[1:], "-"), "=")
	f := c.fs.Lookup(name)
	switch {
	case f != nil && (hasValue || isBoolFlag(f)):
		return tokenFlag
	case f != nil:
		return tokenValueFlag
	case name == "" || name[0] == '-':
		return tokenFlag
	case strings.HasPrefix(s, "--"), name == "h", name == "help":
		return tokenFlag
	default:
		return tokenOperand
	}
}

// boolFlag is how the flag package itself asks whether a flag stands alone.
// The interface is unexported there, so it is matched structurally, exactly
// as the flag package matches it.
type boolFlag interface{ IsBoolFlag() bool }

// isBoolFlag reports whether a defined flag stands alone, which is what
// decides whether the following token is its value or an operand.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(boolFlag)
	return ok && bf.IsBoolFlag()
}

// dashLeading reports whether a token has the shape parse has to judge. A
// lone "-" is a conventional operand and the flag package stops at it too.
func dashLeading(s string) bool { return len(s) > 1 && s[0] == '-' }

// args returns the accepted positional arguments.
func (c *cmd) args() []string { return c.rest }

// usagef rejects an invocation with this command's usage attached.
func (c *cmd) usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...), usage: c.usage}
}

// noArgs rejects positional arguments for commands that take none.
func (c *cmd) noArgs() error {
	if len(c.rest) > 0 {
		// A command with no operand at all leaves nothing to weigh: a
		// dash-leading argument here can only have been a flag, so it earns
		// the flag package's own wording rather than an arity complaint.
		if arg, ok := firstDashLeading(c.rest); ok {
			return c.usagef("flag provided but not defined: %s", arg)
		}
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
		// Both readings of a surplus dash-leading operand stay live: a
		// mistyped short flag and a Claude or OMP source id are both a dash
		// followed by a word, and this command's one selector is spent under
		// either reading. Picking one would hand the operator the wrong
		// remedy every other time, so the rejection names the operands it
		// saw and states the rule that routed them there — which is the hint
		// the bare failure never gave.
		if _, ok := firstDashLeading(c.rest); ok {
			return "", c.usagef("%s takes exactly one SELECTOR, got %d (%s); a token beginning with \"-\" is read as a selector and never as a flag, so a mistyped flag arrives here too — run with -h for the flags",
				c.fs.Name(), len(c.rest), quoteArgs(c.rest))
		}
		return "", c.usagef("%s takes exactly one SELECTOR, got %d", c.fs.Name(), len(c.rest))
	}
}

// firstDashLeading returns the first operand that could have been meant as a
// flag. parse defers an undefined short name to the operands so that a
// dash-leading selector resolves, so this is where an operand that no
// command had room for is read back the other way.
func firstDashLeading(args []string) (string, bool) {
	for _, arg := range args {
		if dashLeading(arg) {
			return arg, true
		}
	}
	return "", false
}

// quoteArgs renders an operand list for one diagnostic. Each value is quoted
// so that an empty or space-carrying operand is still visible as one token;
// the whole message reaches the terminal through Sanitize like any other.
func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = fmt.Sprintf("%q", arg)
	}
	return strings.Join(quoted, ", ")
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

// bindRepo binds repository selection alone, without the host identity. It
// exists for a command that reads an archive several machines share and has no
// use for which one this is: defining --host there would accept a flag the
// command ignores, and on a report about other hosts that flag would read as a
// filter.
func (rf *repoFlags) bindRepo(fs *flag.FlagSet) {
	fs.StringVar(&rf.repository, "repo", "", "restic repository (default $BABEL_RESTIC_REPO, else storage.json)")
	fs.StringVar(&rf.passwordFile, "password-file", "", "file holding the repository password (default $BABEL_RESTIC_PASSWORD_FILE, else storage.json)")
	fs.StringVar(&rf.binary, "restic-binary", "", `restic executable to run (default storage.json, else "restic" from $PATH)`)
}

func (rf *repoFlags) bind(fs *flag.FlagSet) {
	rf.bindRepo(fs)
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
// --host flag, else the flagless resolution localHostID performs. It becomes
// the restic snapshot host, which is how status groups an archive shared by
// several machines.
func (rf *repoFlags) hostID(c *cmd) (string, error) {
	if rf.host != "" {
		if !validHostID(rf.host) {
			return "", c.usagef("invalid --host %q: host ids are 1-%d characters of [a-z0-9._-] starting alphanumeric", rf.host, maxHostIDLen)
		}
		return rf.host, nil
	}
	return localHostID()
}

// localHostID resolves this machine's identity from everything except a flag:
// $BABEL_HOST_ID, else storage.json, else the system hostname lowercased and
// sanitized.
//
// It is separate from repoFlags.hostID because a command with no --host flag of
// its own still needs the identity — `babel fleet records` labels this
// machine's own rows with it, and on that command --host is a filter rather
// than an identity — and two resolution orders would be two answers to "which
// machine is this".
func localHostID() (string, error) {
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
