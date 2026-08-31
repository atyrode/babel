package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

// Title inference behind the operator ceremony (issue #86, decision 2).
//
// `sessions title infer` used to take the titler as a flag, and the reasoning
// was written down beside it: a stored titler is one cron entry away from being
// automatic, so the spend had to be chosen at every invocation. That reasoning
// answered a different question than the one that matters. Typing a command on
// a command line is not an operator deciding which model reads his sessions —
// an agent types flags, and afterwards nothing in the record can tell the two
// apart. What an agent cannot do is sit at a terminal. So decision 2 puts title
// inference behind the same one-time setup the analysis profile goes through:
// the model that writes a title traces to an operator who was handed this
// terminal, saw Code's own dials, and confirmed one.
//
// The two gates are therefore different questions, and both are asked.
// Configuration asks who chose this model, once. `--confirm` asks whether this
// material may leave the machine, every time.
//
// What this command group deliberately is not: a place where a model, a
// provider, a credential or a prompt lives. It stores the reference Code
// reported and the launch that reaches it, exactly like the analysis profile
// (SPEC.md §2.6, decision 18), and it stores them in that document's own
// `titles` block so that configuring one never silently configures the other.

const titlesUsage = `Usage: babel titles <command> [flags]

Commands:
  configure    hand this terminal to Code and store the reference title
               inference will use
  show         show the stored reference and the launch it names

Title inference is the one path in Babel that pays a provider for session
metadata. It runs only when an operator has sat through the configuration
ceremony here, and only when "babel sessions title infer" is given --confirm.
Babel does not own the profile: the provider, the model, and the credential
belong to Code (SPEC.md §2.6, decision 18).

Titles that were already inferred are unaffected by anything in this group,
including a reconfiguration. They keep the value and the "inferred" provenance
they were recorded with.
`

const titlesConfigureUsage = `Usage: babel titles configure [flags]

Hands this terminal to Code so the operator chooses the profile that writes
session titles, then stores the reference Code reports. Babel never sees or
stores the provider configuration behind it, and never picks one: every model
invocation Babel makes has to trace back to an operator who sat through this
ceremony (issue #86).

The worker is launched as

  WORKER [ARG]... --configure --result-file PATH

with this terminal on its stdin, stdout, and stderr - the same ceremony
"babel analysis profile configure" performs, against this machine's own titles
block. Code opens its dials, the operator picks and confirms, and Code writes
the reference it saved to PATH. Babel stores that reference and the launch it
was obtained from, and nothing else.

Until it is stored, "babel sessions title infer --confirm" refuses. Once it is
stored, inference uses exactly it; re-run this command to change it.

A terminal is required, and no flag stands in for one: a profile nobody
watched being chosen is what this command exists to prevent.

A dial is refused rather than forwarded. --worker-arg is how the executable is
put into its worker mode (Code speaks the protocol under a subcommand), not how
a model is chosen, so a "--set"-shaped argument is rejected and
$CODE_SELECTION_STATE is removed from the worker's environment.

Nothing is analysed, no session is read, and no title is written by this
command.

Flags:
  --worker PATH        Code executable speaking babel.analysis-worker
                       (default $BABEL_ANALYSIS_WORKER, else the stored one)
  --worker-arg ARG     extra argument for the worker; repeatable
  --json               emit the stored reference as JSON on stdout
`

const titlesShowUsage = `Usage: babel titles show [--json]

Shows the reference the titles ceremony stored and the launch inference will
use. This is the whole of what Babel knows about how a title gets written; the
profile itself lives in Code (SPEC.md §2.6).

Flags:
  --json    emit the stored reference as JSON on stdout
`

// titlesModeFlag asks the worker for titles rather than an exploration, and
// profileFlag names the reference it must use. They are Babel's half of the
// titler launch, the way --configure and --result-file are Babel's half of the
// ceremony: the worker's own arguments put the executable into its mode, and
// these two say what Babel wants done and under whose authority.
const (
	titlesModeFlag = "--titles"
	profileFlag    = "--profile"
)

// titlesRecord is what the ceremony produced: the Code profile the operator
// confirmed for title inference, and the launch that reaches it.
//
// The launch is stored beside the reference rather than borrowed from the
// analysis block because a reference means nothing without the executable that
// resolves it. Two Code builds can both hold a profile named "p-3", and
// inference has to reach the one the operator was looking at.
type titlesRecord struct {
	Profile      string   `json:"profile"`
	Revision     int      `json:"revision"`
	ConfiguredAt string   `json:"configured_at"`
	Worker       string   `json:"worker"`
	WorkerArgs   []string `json:"worker_args,omitempty"`
}

// ref returns the profile reference inference runs under.
func (t *titlesRecord) ref() worker.ProfileRef {
	return worker.ProfileRef{ID: t.Profile, Revision: t.Revision}
}

// launch is the argv one inference passes the stored executable: the worker's
// own arguments, the mode, and the confirmed reference. Nothing in it is
// chosen at invocation time, which is the whole point — an operator who runs
// inference twice gets the same model both times, and the only way to change
// that is to sit through the ceremony again.
func (t *titlesRecord) launch() []string {
	argv := make([]string, 0, len(t.WorkerArgs)+3)
	argv = append(argv, t.WorkerArgs...)
	return append(argv, titlesModeFlag, profileFlag, t.ref().String())
}

// command is the whole launch for display: what would run, in the order it
// would run, so the disclosure names the process that receives the material
// rather than describing it.
func (t *titlesRecord) command() []string {
	return append([]string{t.Worker}, t.launch()...)
}

// titlesResult is the machine-readable titles document, shared by configure
// and show so a script sees one shape whichever produced it.
type titlesResult struct {
	Configured bool          `json:"configured"`
	Titler     *titlesRecord `json:"titler,omitempty"`
	// Launch is the argv inference would run, resolved, because a caller that
	// only reads JSON should not have to reassemble it from the flags this
	// build happens to append.
	Launch []string `json:"launch,omitempty"`
	// Owner states the ownership boundary in the machine-readable document
	// too: a caller seeing a "profile" field would otherwise reasonably
	// conclude Babel owns one.
	Owner string `json:"profile_owner"`
	Path  string `json:"settings_path"`
}

// titlesCmd routes `babel titles <verb>`.
func (a *app) titlesCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "titles requires a subcommand", usage: titlesUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, titlesUsage)
		return nil
	case "configure":
		return a.titlesConfigure(ctx, args[1:])
	case "show":
		return a.titlesShow(args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown titles subcommand %q", args[0]), usage: titlesUsage}
	}
}

func (a *app) titlesConfigure(ctx context.Context, args []string) error {
	c := newCmd("titles configure", titlesConfigureUsage)
	var wf workerFlags
	wf.bind(c.fs)
	asJSON := c.fs.Bool("json", false, "emit the stored reference as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	// The terminal is checked before anything else is resolved, so an
	// invocation that could never have reached an operator fails without
	// touching the settings document or launching a process.
	tty, ok := a.operatorTerminal()
	if !ok {
		return errors.New(`titles configure needs a terminal on stdin and stdout: the model that writes titles is chosen in Code's own interface, and there is no non-interactive substitute (until one is chosen, "babel sessions title infer --confirm" refuses)`)
	}

	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	// The stored launch this ceremony may fall back on is the titles block's
	// own, never the analysis block's: inheriting the other configuration's
	// executable is the silent substitution this whole ceremony exists to
	// remove.
	var storedBinary string
	var storedArgs []string
	if stored := settings.Titles; stored != nil {
		storedBinary, storedArgs = stored.Worker, stored.WorkerArgs
	}
	wcfg, ok := wf.resolveFrom(storedBinary, storedArgs)
	if !ok {
		return a.reportNoTitlesWorker()
	}
	if err := refuseDials(c, wcfg.Args, len(wf.args) == 0); err != nil {
		return err
	}

	ref, err := a.runConfigureCeremony(ctx, wcfg.Binary, wcfg.Args, tty, titlesCeremony)
	if err != nil {
		return err
	}

	record := &titlesRecord{
		Profile:      ref.ID,
		Revision:     ref.Revision,
		ConfiguredAt: formatTime(time.Now().UTC()),
		Worker:       wcfg.Binary,
		WorkerArgs:   wcfg.Args,
	}
	// Only the titles block is replaced. The analysis profile beside it was
	// confirmed in its own ceremony and is not this command's to touch, and
	// the titles already in the durable store are not configuration at all:
	// they are results the operator paid for, and they keep their value and
	// their "inferred" provenance across every reconfiguration.
	settings.Titles = record
	path, err := saveAnalysisSettings(settings)
	if err != nil {
		return err
	}

	res := titlesResult{
		Configured: true,
		Titler:     sanitizeTitles(record),
		Launch:     sanitizeAll(record.command()),
		Owner:      profileOwner,
		Path:       Sanitize(path),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeTitles(res)
}

func (a *app) titlesShow(args []string) error {
	c := newCmd("titles show", titlesShowUsage)
	asJSON := c.fs.Bool("json", false, "emit the stored reference as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	path, err := analysisPath()
	if err != nil {
		return err
	}
	res := titlesResult{
		Configured: settings.Titles != nil,
		Titler:     sanitizeTitles(settings.Titles),
		Owner:      profileOwner,
		Path:       Sanitize(path),
	}
	if settings.Titles != nil {
		res.Launch = sanitizeAll(settings.Titles.command())
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeTitles(res)
}

// writeTitles renders the titles document for a terminal.
func (a *app) writeTitles(res titlesResult) error {
	if !res.Configured {
		fmt.Fprint(a.stdout, "no title-inference profile is stored\n")
		fmt.Fprint(a.stdout, "run \"babel titles configure\" to choose one in Code's own interface\n")
		fmt.Fprint(a.stdout, "until then \"babel sessions title infer --confirm\" refuses; titles already inferred are unaffected\n")
		return nil
	}
	return writeDetail(a.stdout, [][2]string{
		{"profile", res.Titler.Profile},
		{"revision", strconv.Itoa(res.Titler.Revision)},
		{"owner", "Code; Babel stores this reference only (SPEC.md §2.6)"},
		{"configured", orMissing(res.Titler.ConfiguredAt)},
		{"worker", orMissing(res.Titler.Worker)},
		{"launch", orMissing(strings.Join(res.Launch, " "))},
		{"settings", orMissing(res.Path)},
	})
}

// sanitizeTitles renders every dynamic field of a stored record. The values
// come from Code and from a document on disk, which are outside Babel's trust
// boundary like any other producer (SPEC.md §3).
func sanitizeTitles(t *titlesRecord) *titlesRecord {
	if t == nil {
		return nil
	}
	out := *t
	out.Profile = Sanitize(t.Profile)
	out.ConfiguredAt = Sanitize(t.ConfiguredAt)
	out.Worker = Sanitize(t.Worker)
	out.WorkerArgs = sanitizeAll(t.WorkerArgs)
	return &out
}

// reportNoTitlesWorker explains that there is no executable to configure
// titles against, and reports that the explanation has been given.
//
// It is separate from reportNoWorker because that one is about an exploration
// that cannot start, and sending an operator who asked for titles to
// `babel explore --worker PATH` would answer a question he did not ask.
func (a *app) reportNoTitlesWorker() error {
	fmt.Fprint(a.stderr, `babel: no Code executable is available, so there is nothing to configure titles against.

Title inference runs through Code, which owns the profile, the model, and the
provider credential (SPEC.md §2.6). Babel launches an executable speaking the
babel.analysis-worker protocol and stores the reference the operator confirms
in its interface; it never chooses a model itself. This machine has none.

To name one:
  babel titles configure --worker PATH
  or set $BABEL_ANALYSIS_WORKER

Nothing else is affected: recorded and derived titles never needed a model,
and titles already inferred keep the value they were given.
`)
	return errReported
}

// reportTitlesUnconfigured refuses an inference that no operator ever set up,
// and reports that the explanation has been given.
//
// This is decision 2 of issue #86 at the point it bites. The message is the
// product: an operator - or an agent acting for one - has just asked Babel to
// send session material to a model, and the true account is that no model has
// been chosen for this. So it says that, gives the one command that chooses
// one, says what that command does, and states plainly that nothing was sent.
func (a *app) reportTitlesUnconfigured() error {
	fmt.Fprint(a.stderr, `babel: no title-inference profile is configured, so nothing was sent.

Every model Babel invokes traces to a setup an operator sat through (issue
#86). The profile that writes titles is chosen in Code's own interface, once:

  babel titles configure

That hands this terminal to Code, and stores the reference Code reports back.
"babel titles show" prints what is stored, and re-running configure replaces
it. Inference then uses exactly that reference, and still sends nothing
without --confirm.

No session material left this machine and nothing was recorded. Titles
already inferred are unaffected, and "babel sessions title infer" without
--confirm still prints exactly what a configured run would send.
`)
	return errReported
}
