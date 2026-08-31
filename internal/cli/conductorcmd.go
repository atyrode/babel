package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/worker"
)

const conductorUsage = `Usage: babel conductor <command> [flags]

Babel's runtime loop (SPEC.md §6.5, issue #96). A cycle asks "what deserves a
run?" and answers it from a work ladder: the operator's invitations first, then
the standing self-improvement duties the operator authorized, then a protected
fraction of chaotic serendipity draws. Every cycle is an ordinary run —
preparation, recipe, receipt, frontier, dispositions — and carries a recorded
authority saying why it happened.

Commands:
  configure    set the budget ceilings the loop runs under
  run          run cycles in the foreground until asked to stop
  status       report the loop, its queues, its spend and its last cycles

The conductor never configures a Code profile: a cycle inherits the one
"babel analysis profile configure" stored. It does not daemonize either — this
is a foreground loop, and supervision belongs to the OS.
`

const conductorConfigureUsage = `Usage: babel conductor configure --per-cycle USD --per-day USD [flags]

Records the budget ceilings the loop runs under, beside the analysis settings,
with the scheduling dials and the standing-duty authorizations.

Both ceilings are mandatory and neither has a default. Autonomy here is
budget-bounded rather than trust-bounded, and a default ceiling would be a limit
nobody chose. The conductor refuses to run until both are set.

Flags:
  --per-cycle AMOUNT   the most one cycle may cost
  --per-day AMOUNT     the most one UTC day of cycles may cost together
  --currency CODE      the unit both ceilings are quoted in (default USD)
  --floor N            guarantee one serendipity cycle in every N (default 4)
  --interval DURATION   wait this long between cycles (default 1h)
  --slice-sessions N   bound a serendipity draw to N sessions (default 3)
  --babel-improves-babel      schedule the product self-improvement duties
  --no-babel-improves-babel   withdraw them
  --babel-tunes-itself        schedule the personal tuning duty
  --no-babel-tunes-itself     withdraw it
  --json               emit the stored configuration as JSON

Both duties are off until you turn them on, and each takes an explicit --no-
form: an invocation that adjusts one dial leaves everything it does not name
alone, so "off" has to be said rather than implied.

A duty toggle grants no new authority. The cycle it schedules runs under the
profile the analysis ceremony stored, inside these ceilings, over the same
read-only corpus, and its outputs are proposals like any other. What the toggle
authorizes is scheduling: that Babel may analyse itself without being asked
each time.

Nothing here selects a provider, a model or a profile: those are Code's, and a
loop that could choose them would be choosing its own spending limit.
`

const conductorRunUsage = `Usage: babel conductor run [flags]

Runs cycles in the foreground until asked to stop.

Each cycle draws work from the ladder, enforces the day's ceiling against what
the receipts recorded, and runs an ordinary exploration. The first SIGTERM or
Ctrl-C stops the loop at the cycle boundary, letting the run in flight finish; a
second cancels the run itself, which leaves every committed record durable and
the unexplored frontier deferred.

Flags:
  --once               run exactly one cycle and stop
  --until TIME         stop at RFC 3339 time, HH:MM today, or after a duration
  --worker PATH        the Code executable that speaks the worker protocol
  --worker-arg ARG     extra argument for the worker; repeatable
  --json               emit the cycles this invocation ran as JSON

There is no daemon mode. Supervision, restart policy and wall-clock scheduling
belong to the OS, which already owns them.
`

const conductorStatusUsage = `Usage: babel conductor status [--cycles N] [--json]

Reports what the loop is doing: its state, the cycle in flight and the authority
it is running under, every ladder rung's queue depth, each standing duty's own
state, today's spend against the ceilings, and the last few cycle outcomes.

A duty the operator has not authorized is reported as off rather than omitted,
because "Babel has no such duty" and "you have not authorized it" are different
answers to "why is the loop not doing this".

Flags:
  --cycles N   show the last N cycles (default 10)
  --json       emit the report as JSON
`

// conductorSchema versions the settings document this file owns.
const conductorSchema = 1

// conductorFile is where the loop's ceilings live. It sits beside analysis.json
// rather than in the data directory for the same reason: it is configuration,
// nothing rebuilds it, and no prune path may reach it.
const conductorFile = "conductor.json"

// defaultConductorInterval is the wait between cycles when none was configured.
// An hour is a scheduling default rather than a safety one — the ceilings are
// what bound spend — but a loop with no gap at all would turn a day's budget
// into a few minutes of runs.
const defaultConductorInterval = time.Hour

// conductorSettings is everything Babel keeps about the loop: the ceilings,
// three scheduling dials and the two standing-duty authorizations. It holds no
// profile, no provider and no model: those belong to Code (§2.6, decision 18),
// and the loop inherits whatever the profile ceremony stored.
type conductorSettings struct {
	Schema int `json:"schema"`
	// Ceilings is a pointer so "never configured" and "configured with zeros"
	// are different documents. The conductor refuses to run on either.
	Ceilings        *ceilingRecord `json:"ceilings,omitempty"`
	Floor           int            `json:"serendipity_floor,omitempty"`
	IntervalSeconds int            `json:"interval_seconds,omitempty"`
	SliceSessions   int            `json:"slice_sessions,omitempty"`
	// BabelImprovesBabel and BabelTunesItself are #88's two self-improvement
	// dimensions. Both are absent from the document until the operator turns
	// one on, which is the same statement as off: a duty nobody authorized is
	// never scheduled, and a settings file that recorded `false` for it would
	// look like a decision rather than the default.
	BabelImprovesBabel bool   `json:"babel_improves_babel,omitempty"`
	BabelTunesItself   bool   `json:"babel_tunes_itself,omitempty"`
	ConfiguredAt       string `json:"configured_at,omitempty"`
}

// ceilingRecord is the operator's stated limits on autonomy.
type ceilingRecord struct {
	Currency string  `json:"currency"`
	PerCycle float64 `json:"per_cycle"`
	PerDay   float64 `json:"per_day"`
}

func (s conductorSettings) ceilings() conductor.Ceilings {
	if s.Ceilings == nil {
		return conductor.Ceilings{}
	}
	return conductor.Ceilings{
		Currency: s.Ceilings.Currency,
		PerCycle: s.Ceilings.PerCycle,
		PerDay:   s.Ceilings.PerDay,
	}
}

func (s conductorSettings) interval() time.Duration {
	if s.IntervalSeconds <= 0 {
		return defaultConductorInterval
	}
	return time.Duration(s.IntervalSeconds) * time.Second
}

// duties is the standing-duty authorization the duty rung reads.
func (s conductorSettings) duties() conductor.Duties {
	return conductor.Duties{
		ImprovesBabel: s.BabelImprovesBabel,
		TunesItself:   s.BabelTunesItself,
	}
}

func conductorPath() (string, error) {
	base := config.Path()
	if base == "" {
		return "", errors.New("cannot resolve the configuration directory for conductor settings")
	}
	return filepath.Join(filepath.Dir(base), conductorFile), nil
}

// loadConductorSettings reads the settings document. A missing file is not an
// error: an unconfigured machine is the normal state, and `conductor run` says
// so itself rather than failing here.
func loadConductorSettings() (conductorSettings, error) {
	path, err := conductorPath()
	if err != nil {
		return conductorSettings{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return conductorSettings{Schema: conductorSchema}, nil
	}
	if err != nil {
		return conductorSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s conductorSettings
	if err := json.Unmarshal(data, &s); err != nil {
		// The path is named and the content is not, like every other settings
		// document: it is not a place to quote values from.
		return conductorSettings{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if s.Schema > conductorSchema {
		return conductorSettings{}, fmt.Errorf("%s: schema %d, this build reads %d",
			path, s.Schema, conductorSchema)
	}
	return s, nil
}

// saveConductorSettings replaces the settings document atomically, so an
// interrupted write leaves the previous ceilings rather than a truncated one.
func saveConductorSettings(s conductorSettings) (string, error) {
	path, err := conductorPath()
	if err != nil {
		return "", err
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	s.Schema = conductorSchema
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode conductor settings: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	return path, nil
}

// conductor routes `babel conductor <verb>`.
func (a *app) conductorCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "conductor needs a command", usage: conductorUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, conductorUsage)
		return nil
	case "configure":
		return a.conductorConfigure(args[1:])
	case "run":
		return a.conductorRun(ctx, args[1:])
	case "status":
		return a.conductorStatus(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown conductor command %q", args[0]), usage: conductorUsage}
	}
}

// conductorConfigResult is the machine-readable configuration document, shared
// by configure and status so a script sees one shape whichever produced it.
type conductorConfigResult struct {
	Currency           string  `json:"currency"`
	PerCycle           float64 `json:"per_cycle"`
	PerDay             float64 `json:"per_day"`
	Floor              int     `json:"serendipity_floor"`
	IntervalSeconds    int     `json:"interval_seconds"`
	SliceSessions      int     `json:"slice_sessions"`
	BabelImprovesBabel bool    `json:"babel_improves_babel"`
	BabelTunesItself   bool    `json:"babel_tunes_itself"`
	ConfiguredAt       string  `json:"configured_at,omitempty"`
	Path               string  `json:"path"`
}

// conductorConfigure implements `babel conductor configure`.
//
// It is flags rather than a ceremony, and that is not an inconsistency with
// #86's profile configuration. That ceremony exists because the operator is
// choosing a model and a provider through Code's own interface, where the
// decision is made in front of them. Nothing here reaches a model: these are
// two numbers, a ratio, a wait, and two switches that authorize scheduling of
// recipes the ceremony's own profile already runs, so a terminal handover would
// add ritual without adding intent.
//
// The duty toggles belong here for exactly that reason. Turning one on grants
// no authority a cycle did not already have: same profile, same ceilings, same
// read-only corpus, same "suggestions, never side effects" boundary. What it
// authorizes is that a cookbook recipe whose subject is Babel itself may be
// scheduled without the operator typing the command — which is a scheduling
// decision, and scheduling decisions are what this document holds.
func (a *app) conductorConfigure(args []string) error {
	c := newCmd("conductor configure", conductorConfigureUsage)
	perCycle := c.fs.Float64("per-cycle", 0, "the most one cycle may cost")
	perDay := c.fs.Float64("per-day", 0, "the most one UTC day of cycles may cost")
	currency := c.fs.String("currency", "", "the unit both ceilings are quoted in")
	floor := c.fs.Int("floor", 0, "guarantee one serendipity cycle in every N")
	interval := c.fs.Duration("interval", 0, "wait this long between cycles")
	slice := c.fs.Int("slice-sessions", 0, "bound a serendipity draw to N sessions")
	// The flag names are the duty names, taken from the constants a receipt's
	// authority reference is built from, so a renamed duty cannot leave a flag
	// authorizing something the loop no longer knows.
	improves := c.fs.Bool(conductor.DutyImprovesBabel, false,
		"authorize the product self-improvement duties")
	noImproves := c.fs.Bool("no-"+conductor.DutyImprovesBabel, false,
		"withdraw the product self-improvement duties")
	tunes := c.fs.Bool(conductor.DutyTunesItself, false,
		"authorize the personal tuning duty")
	noTunes := c.fs.Bool("no-"+conductor.DutyTunesItself, false,
		"withdraw the personal tuning duty")
	asJSON := c.fs.Bool("json", false, "emit the stored configuration as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	settings, err := loadConductorSettings()
	if err != nil {
		return err
	}
	stored := settings.Ceilings
	if stored == nil {
		stored = &ceilingRecord{}
	}
	next := *stored
	if *currency != "" {
		next.Currency = strings.ToUpper(*currency)
	}
	if next.Currency == "" {
		next.Currency = "USD"
	}
	if *perCycle != 0 {
		next.PerCycle = *perCycle
	}
	if *perDay != 0 {
		next.PerDay = *perDay
	}
	if next.PerCycle <= 0 || next.PerDay <= 0 {
		return c.usagef("the conductor refuses to run without explicit ceilings: pass --per-cycle AMOUNT and --per-day AMOUNT")
	}
	if next.PerCycle > next.PerDay {
		return c.usagef("--per-cycle %.2f is above --per-day %.2f, which would refuse every cycle",
			next.PerCycle, next.PerDay)
	}
	if *floor < 0 {
		return c.usagef("--floor cannot be negative")
	}
	if *interval < 0 {
		return c.usagef("--interval cannot be negative")
	}
	if *slice < 0 {
		return c.usagef("--slice-sessions cannot be negative")
	}
	improvesBabel, err := resolveDutyToggle(c, conductor.DutyImprovesBabel,
		settings.BabelImprovesBabel, *improves, *noImproves)
	if err != nil {
		return err
	}
	tunesItself, err := resolveDutyToggle(c, conductor.DutyTunesItself,
		settings.BabelTunesItself, *tunes, *noTunes)
	if err != nil {
		return err
	}

	settings.Ceilings = &next
	if *floor > 0 {
		settings.Floor = *floor
	}
	if *interval > 0 {
		settings.IntervalSeconds = int(interval.Seconds())
	}
	if *slice > 0 {
		settings.SliceSessions = *slice
	}
	settings.BabelImprovesBabel = improvesBabel
	settings.BabelTunesItself = tunesItself
	settings.ConfiguredAt = formatTime(time.Now().UTC())
	path, err := saveConductorSettings(settings)
	if err != nil {
		return err
	}

	res := conductorConfigDocument(settings, path)
	if *asJSON {
		return a.emitJSON(res)
	}
	writeDetail(a.stdout, [][2]string{
		{"per cycle", fmt.Sprintf("%.2f %s", res.PerCycle, res.Currency)},
		{"per day", fmt.Sprintf("%.2f %s", res.PerDay, res.Currency)},
		{"serendipity floor", fmt.Sprintf("one cycle in %d", res.Floor)},
		{"interval", (time.Duration(res.IntervalSeconds) * time.Second).String()},
		{"serendipity slice", fmt.Sprintf("up to %d %s", res.SliceSessions,
			plural(res.SliceSessions, "session", "sessions"))},
		{"babel improves babel", onOrOff(res.BabelImprovesBabel)},
		{"babel tunes itself", onOrOff(res.BabelTunesItself)},
		{"stored in", Sanitize(res.Path)},
	})
	fmt.Fprintf(a.stdout, "\nrun the loop with: babel conductor run\n")
	return nil
}

func conductorConfigDocument(s conductorSettings, path string) conductorConfigResult {
	res := conductorConfigResult{
		Floor:              conductor.Floor{OneIn: s.Floor}.OneIn,
		IntervalSeconds:    int(s.interval().Seconds()),
		SliceSessions:      s.SliceSessions,
		ConfiguredAt:       s.ConfiguredAt,
		BabelImprovesBabel: s.BabelImprovesBabel,
		BabelTunesItself:   s.BabelTunesItself,
		Path:               path,
	}
	if res.Floor <= 0 {
		res.Floor = conductor.DefaultFloor
	}
	if res.SliceSessions <= 0 {
		res.SliceSessions = conductor.DefaultSliceSessions
	}
	if s.Ceilings != nil {
		res.Currency = s.Ceilings.Currency
		res.PerCycle = s.Ceilings.PerCycle
		res.PerDay = s.Ceilings.PerDay
	}
	return res
}

// resolveDutyToggle reads one standing-duty authorization out of its two flags.
//
// Two flags rather than one taking a value, because `conductor configure` is
// incremental: an operator adjusting the floor has not withdrawn a duty, so
// "not named" must stay distinguishable from "off", and a lone boolean flag
// cannot say off. Naming both is a contradiction rather than a precedence
// question — guessing which the operator meant is exactly the wrong place to
// guess, because one of the two answers schedules autonomous runs.
func resolveDutyToggle(c *cmd, name string, stored, on, off bool) (bool, error) {
	switch {
	case on && off:
		return stored, c.usagef("--%s and --no-%s contradict each other", name, name)
	case on:
		return true, nil
	case off:
		return false, nil
	default:
		return stored, nil
	}
}

// onOrOff renders a toggle for a terminal.
func onOrOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// conductorRunResult is `babel conductor run --json`: the cycles this
// invocation ran, in order.
type conductorRunResult struct {
	Cycles []conductorCycleRow `json:"cycles"`
	Parked string              `json:"parked,omitempty"`
}

// conductorRun implements `babel conductor run`.
func (a *app) conductorRun(ctx context.Context, args []string) error {
	c := newCmd("conductor run", conductorRunUsage)
	var wf workerFlags
	var sf scanFlags
	wf.bind(c.fs)
	sf.bindRoots(c)
	once := c.fs.Bool("once", false, "run exactly one cycle and stop")
	until := c.fs.String("until", "", "stop at this time, or after this duration")
	asJSON := c.fs.Bool("json", false, "emit the cycles this invocation ran as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	settings, err := loadConductorSettings()
	if err != nil {
		return err
	}
	if settings.Ceilings == nil {
		return a.reportUnconfiguredConductor()
	}
	analysis, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	wcfg, ok := wf.resolve(analysis)
	if !ok {
		return a.reportNoWorker()
	}
	// A cycle inherits the stored profile and nothing else: there is no
	// --profile here, because a loop that could name its own profile could
	// name its own spending limit. An unconfigured machine is refused with the
	// ceremony that fixes it.
	profileRef, err := storedProfile(c, analysis)
	if err != nil {
		return err
	}
	deadline := time.Time{}
	if *until != "" {
		deadline, err = parseUntil(time.Now(), *until)
		if err != nil {
			return c.usagef("%v", err)
		}
	}

	host, err := (&repoFlags{}).hostID(c)
	if err != nil {
		return err
	}
	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()
	d, err := babelDirs()
	if err != nil {
		return err
	}
	journal, err := conductor.OpenJournal(d.durableDir())
	if err != nil {
		return err
	}

	loop, err := conductor.New(conductor.Config{
		Ceilings: settings.ceilings(),
		Floor:    conductor.Floor{OneIn: settings.Floor},
		Interval: settings.interval(),
		Ladder: conductor.DefaultLadder(
			conductor.NewInvitationRung(state.dispositions,
				conductor.NewRecordOrigins(state.frontier, state.runs)),
			conductor.NewDutyRung(settings.duties(), journal, nil, 0),
			conductor.NewSerendipityRung(&hostCorpus{app: a, adapters: adapters(), roots: sf.rootList()},
				embeddedRecipes{}, drawGenerator(), settings.SliceSessions),
		),
		Runner: &conductorRunner{
			app:       a,
			cmd:       c,
			state:     state,
			worker:    wcfg,
			profile:   profileRef,
			host:      host,
			adapters:  adapters(),
			scanRoots: sf.rootList(),
		},
		Ledger:  conductor.NewReceiptLedger(state.runs),
		Journal: journal,
		Log:     a.diagf,
	})
	if err != nil {
		return err
	}

	stop, release := a.conductorSignals()
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-stop.hard
		cancel()
	}()

	a.diagf("conductor: ceilings %.2f per cycle, %.2f per day %s; profile %s\n",
		settings.Ceilings.PerCycle, settings.Ceilings.PerDay, settings.Ceilings.Currency,
		Sanitize(profileRef.String()))
	before := journal.NextSeq()
	runErr := loop.Run(ctx, conductor.RunOptions{Until: deadline, Once: *once, Stop: stop.soft})

	res := conductorRunResult{Cycles: []conductorCycleRow{}}
	for _, cycle := range journal.Recent(0) {
		if cycle.Seq >= before {
			res.Cycles = append([]conductorCycleRow{conductorCycleDocument(cycle)}, res.Cycles...)
		}
	}
	if errors.Is(runErr, conductor.ErrParked) {
		res.Parked = strings.TrimPrefix(runErr.Error(), "conductor: parked: ")
	}
	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		a.writeConductorCycles(res.Cycles)
		if res.Parked != "" {
			fmt.Fprintf(a.stdout, "\nparked: %s\n", Sanitize(res.Parked))
		}
	}
	switch {
	case errors.Is(runErr, conductor.ErrParked):
		// Parking is the ceilings working, so it is not a failure exit: the
		// reason is on stdout and in the journal, and a supervisor that
		// restarts the loop will find the day's ceiling refreshed.
		return nil
	case errors.Is(runErr, context.Canceled):
		return nil
	default:
		return runErr
	}
}

// storedProfile is the Code profile a cycle inherits.
//
// It is separate from resolveProfile because the two refusals differ in the one
// way that matters to whoever reads them: `babel explore` can be handed a
// profile on the command line, and the loop cannot. Offering `--profile` here
// would name a flag this command does not have, for a decision it deliberately
// cannot make.
func storedProfile(c *cmd, s analysisSettings) (worker.ProfileRef, error) {
	if s.Profile == nil {
		return worker.ProfileRef{}, c.usagef(
			"no Code analysis profile is stored, and the conductor never configures one: " +
				"run \"babel analysis profile configure\" first")
	}
	return s.Profile.ref(), nil
}

// reportUnconfiguredConductor explains an unconfigured loop and reports that
// the explanation has been given.
//
// The message is the product here. Refusing to run without ceilings is the
// design, so an operator meeting this has a correctly installed Babel, and the
// diagnostic has to read as a stated boundary with a remedy rather than as a
// malfunction.
func (a *app) reportUnconfiguredConductor() error {
	fmt.Fprint(a.stderr, `the conductor has no budget ceilings, so it will not run.

Autonomy here is budget-bounded, not trust-bounded: a loop that may spend
without a stated limit is a loop nobody set a limit on. Both ceilings are
mandatory and neither has a default.

  babel conductor configure --per-cycle 0.50 --per-day 5.00

Everything else still works: "babel prepare" and "babel explore" run one
exploration when you ask for one, which is what Babel did before the loop
existed.
`)
	return errReported
}

// conductorSignals is the two-stage stop a foreground loop needs.
//
// The first signal asks the loop to stop at the cycle boundary, which lets the
// run in flight finish and be receipted. The second cancels it, which
// internal/explore already makes safe: committed records stay durable and the
// unexplored frontier is deferred rather than erased. Collapsing the two would
// force a choice between losing a cycle's work and being unable to stop.
type conductorStop struct {
	soft chan struct{}
	hard chan struct{}
}

func (a *app) conductorSignals() (*conductorStop, func()) {
	stop := &conductorStop{soft: make(chan struct{}), hard: make(chan struct{})}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		asked := false
		for {
			select {
			case <-done:
				return
			case <-sig:
				if !asked {
					asked = true
					a.diagf("conductor: stopping after this cycle; signal again to cancel the run\n")
					close(stop.soft)
					continue
				}
				a.diagf("conductor: cancelling the run in flight; committed records stay durable\n")
				close(stop.hard)
				return
			}
		}
	}()
	return stop, func() {
		signal.Stop(sig)
		close(done)
	}
}

// parseUntil reads `--until`: an RFC 3339 instant, a wall clock time today (or
// tomorrow, if it has passed), or a duration from now. All three are things an
// operator plausibly means by "until", and guessing wrong about which would stop
// the loop at the wrong time.
func parseUntil(now time.Time, value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--until %q is not in the future", value)
		}
		return now.Add(d), nil
	}
	if t, err := time.Parse("15:04", value); err == nil {
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("--until %q is not an RFC 3339 time, an HH:MM time, or a duration", value)
}

// drawGenerator seeds the serendipity floor's generator.
//
// It is seeded from the system CSPRNG rather than from the clock: two conductors
// started in the same second must not make the same draws, and a draw an
// observer can predict is not a chaotic one. The generator is still a seeded
// PRNG so a recorded draw stays reproducible from its seed in a test.
func drawGenerator() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

// hostCorpus is the local session inventory the serendipity floor slices.
type hostCorpus struct {
	app      *app
	adapters []adapter.Adapter
	roots    []string
}

// Sessions reports every session this host can see, in a stable order.
//
// The order is the scan's, which is stable for a given tree, and the list is
// sorted so a seeded draw is reproducible across two conductors that scanned
// the same corpus in a different filesystem order.
func (h *hostCorpus) Sessions(ctx context.Context) ([]string, error) {
	sessions, _ := h.app.scan(ctx, h.adapters, h.roots)
	keys := make([]string, 0, len(sessions))
	for _, s := range sessions {
		keys = append(keys, s.key())
	}
	return sortedUnique(keys), nil
}

// embeddedRecipes is the build's own cookbook.
type embeddedRecipes struct{}

// Defaults reports the default-enabled recipe ids, which is the set a run
// applies when nobody named one.
func (embeddedRecipes) Defaults(context.Context) ([]string, error) {
	set, err := cookbook.Embedded()
	if err != nil {
		return nil, err
	}
	defaults := set.Defaults()
	ids := make([]string, 0, len(defaults))
	for _, r := range defaults {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// conductorRunner turns one cycle's assignment into an ordinary run.
//
// It is the whole of what scheduling may ask for, and it asks for it through the
// same two functions a typed command uses: fixScope for §6.5's preparation and
// runExploration for the run. Nothing here widens a grant, chooses a profile or
// reaches a capability, which is what "the conductor adds scheduling, never
// authority" has to mean in code.
type conductorRunner struct {
	app       *app
	cmd       *cmd
	state     *analysisState
	worker    worker.Config
	profile   worker.ProfileRef
	host      string
	adapters  []adapter.Adapter
	scanRoots []string
}

// Run prepares the assignment's corpus slice and explores it.
func (r *conductorRunner) Run(ctx context.Context, runID string,
	a conductor.Assignment) (conductor.Result, error) {
	sessions, _ := r.app.scan(ctx, r.adapters, r.scanRoots)
	chosen, missing := sliceSessions(sessions, a.Sessions)
	if len(missing) > 0 {
		// A session the assignment named is gone from this host. The cycle
		// runs over what is left and says what it lost: silently substituting
		// a different corpus would make the receipt's scope a guess.
		r.app.diagf("conductor: %d named %s no longer on this host: %s\n",
			len(missing), plural(len(missing), "session is", "sessions are"),
			Sanitize(strings.Join(missing, " ")))
	}
	if len(chosen) == 0 {
		return conductor.Result{}, fmt.Errorf(
			"none of the %d %s this cycle was pointed at are on this host",
			len(a.Sessions), plural(len(a.Sessions), "session", "sessions"))
	}

	set, err := r.recipes(a.Recipes)
	if err != nil {
		return conductor.Result{}, err
	}
	scoped, err := r.app.fixScope(ctx, r.state.runs, chosen, r.host,
		a.Rung == conductor.RungSerendipity)
	if err != nil {
		return conductor.Result{}, err
	}

	// A cycle runs the discovery pass alone. §5.4's challenger and synthesizer
	// are separate jobs with their own worker invocations, so scheduling them
	// unasked would multiply a cycle's cost against a ceiling the operator set
	// for one run; they stay operator choices on `babel explore`.
	res, outcome, runErr := r.app.runExploration(ctx, r.state, explorePlan{
		prep:      scoped.prep,
		profile:   r.profile,
		recipes:   set,
		worker:    r.worker,
		runID:     runID,
		authority: a.Authority,
		roots:     a.Roots,
		scanRoots: r.scanRoots,
	})
	result := conductor.Result{
		PreparationID: string(scoped.prep.ID),
		ReceiptID:     res.ReceiptID,
		Failures:      len(res.Failures),
		Cancelled:     res.Cancelled,
	}
	if outcome != nil && outcome.Receipt != nil && outcome.Receipt.Body.Worker != nil {
		cost := outcome.Receipt.Body.Worker.Cost
		result.Cost, result.Currency = cost.EstimatedRun, cost.Currency
	}
	return result, runErr
}

// recipes resolves the assignment's recipe ids, falling back to the cookbook's
// defaults when a named recipe is not in this build.
//
// That fallback is deliberate and narrow. An invitation can point at an
// observation an older cookbook produced, and refusing the operator's request
// because the recipe that produced the record has since been renamed would let
// Babel's own versioning override a person's explicit nudge. The substitution is
// stated on stderr and the receipt records what actually ran.
func (r *conductorRunner) recipes(ids []string) (*cookbook.Set, error) {
	set, err := recipeSet(ids)
	var unknown *cookbook.UnknownRecipeError
	if errors.As(err, &unknown) {
		r.app.diagf("conductor: recipe %s is not in this build's cookbook; running the defaults instead\n",
			Sanitize(unknown.ID))
		return recipeSet(nil)
	}
	return set, err
}

// sliceSessions narrows a scan to the named selectors, reporting the ones this
// host no longer has. An empty selector list is the whole corpus, which is what
// an assignment with no slice means.
func sliceSessions(sessions []localSession, selectors []string) (chosen []localSession, missing []string) {
	if len(selectors) == 0 {
		return sessions, nil
	}
	byKey := make(map[string]localSession, len(sessions))
	for _, s := range sessions {
		byKey[s.key()] = s
	}
	seen := make(map[string]struct{}, len(selectors))
	for _, selector := range selectors {
		if _, dup := seen[selector]; dup {
			continue
		}
		seen[selector] = struct{}{}
		s, ok := byKey[selector]
		if !ok {
			missing = append(missing, selector)
			continue
		}
		chosen = append(chosen, s)
	}
	return chosen, missing
}

// conductorCycleRow is one cycle in machine-readable output.
type conductorCycleRow struct {
	Seq           int      `json:"seq"`
	Outcome       string   `json:"outcome"`
	Reason        string   `json:"reason,omitempty"`
	Rung          string   `json:"rung,omitempty"`
	AuthorityKind string   `json:"authority_kind,omitempty"`
	AuthorityRef  string   `json:"authority_ref,omitempty"`
	RunID         string   `json:"run_id,omitempty"`
	Invitation    string   `json:"invitation,omitempty"`
	Resumed       bool     `json:"resumed,omitempty"`
	Sessions      int      `json:"sessions"`
	Recipes       []string `json:"recipes,omitempty"`
	Note          string   `json:"note,omitempty"`
	PreparationID string   `json:"preparation_id,omitempty"`
	ReceiptID     string   `json:"receipt_id,omitempty"`
	Cost          float64  `json:"cost,omitempty"`
	Currency      string   `json:"currency,omitempty"`
	Failures      int      `json:"failures,omitempty"`
	StartedAt     string   `json:"started_at"`
	FinishedAt    string   `json:"finished_at,omitempty"`
}

func conductorCycleDocument(c conductor.Cycle) conductorCycleRow {
	row := conductorCycleRow{
		Seq:           c.Seq,
		Outcome:       string(c.Outcome),
		Reason:        Sanitize(c.Reason),
		Rung:          Sanitize(c.Rung),
		AuthorityKind: string(c.Authority.Kind),
		AuthorityRef:  Sanitize(c.Authority.Ref),
		RunID:         Sanitize(c.RunID),
		Invitation:    Sanitize(c.Invitation),
		Resumed:       c.Resumed,
		Sessions:      len(c.Sessions),
		Recipes:       idList(c.Recipes),
		Note:          Sanitize(c.Note),
		PreparationID: Sanitize(c.PreparationID),
		ReceiptID:     Sanitize(c.ReceiptID),
		Cost:          c.Cost,
		Currency:      Sanitize(c.Currency),
		Failures:      c.Failures,
		StartedAt:     formatTime(c.StartedAt),
	}
	if !c.FinishedAt.IsZero() {
		row.FinishedAt = formatTime(c.FinishedAt)
	}
	return row
}

// conductorStatusResult is `babel conductor status --json`.
type conductorStatusResult struct {
	Configured bool                   `json:"configured"`
	State      string                 `json:"state"`
	Config     *conductorConfigResult `json:"config,omitempty"`
	// Current is the cycle in flight, present only while one is.
	Current *conductorCycleRow `json:"current,omitempty"`
	Rungs   []conductorRungRow `json:"rungs"`
	// Duties is every standing duty this build knows with its own state,
	// including the ones the operator has not authorized: a duty nobody turned
	// on is reported off rather than omitted, so "Babel has no such duty" and
	// "you have not authorized it" stay different answers.
	Duties  []conductorDutyRow  `json:"duties"`
	Spend   *conductorSpendRow  `json:"spend,omitempty"`
	Cycles  []conductorCycleRow `json:"cycles"`
	Journal string              `json:"journal"`
}

// conductorRungRow is one ladder rung's queue.
type conductorRungRow struct {
	Name        string `json:"name"`
	Waiting     int    `json:"waiting"`
	Implemented bool   `json:"implemented"`
	Note        string `json:"note"`
}

// conductorDutyRow is one standing duty's state (#88, #94).
type conductorDutyRow struct {
	Name      string `json:"name"`
	Recipe    string `json:"recipe"`
	Dimension string `json:"dimension"`
	Enabled   bool   `json:"enabled"`
	Due       bool   `json:"due"`
	// LastDrawn is when the loop last drew this duty, absent when never.
	LastDrawn string `json:"last_drawn,omitempty"`
	Note      string `json:"note"`
}

// conductorSpendRow is today's spend against the ceilings.
type conductorSpendRow struct {
	Currency  string  `json:"currency"`
	Spent     float64 `json:"spent"`
	PerDay    float64 `json:"per_day"`
	Remaining float64 `json:"remaining"`
	PerCycle  float64 `json:"per_cycle"`
	Runs      int     `json:"runs"`
	// Unpriced counts today's receipts whose profile reported no usable cost.
	// A non-zero count means the ceiling is bounding less than the figure
	// beside it suggests, which an operator should see rather than infer.
	Unpriced int `json:"unpriced"`
	// Journalled is what the loop's own cycles recorded, which can be less
	// than Spent: a run an operator started by hand counts against the day's
	// ceiling too, and a disagreement here is the honest way to show that.
	Journalled float64 `json:"journalled"`
}

// conductorStatus implements `babel conductor status`.
func (a *app) conductorStatus(ctx context.Context, args []string) error {
	c := newCmd("conductor status", conductorStatusUsage)
	count := c.fs.Int("cycles", 10, "show the last N cycles")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *count < 0 {
		return c.usagef("--cycles cannot be negative")
	}

	settings, err := loadConductorSettings()
	if err != nil {
		return err
	}
	path, err := conductorPath()
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	journal, err := conductor.OpenJournal(d.durableDir())
	if err != nil {
		return err
	}
	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	now := time.Now()
	res := conductorStatusResult{
		Configured: settings.Ceilings != nil,
		Journal:    Sanitize(journal.Path()),
		Cycles:     []conductorCycleRow{},
		Rungs:      []conductorRungRow{},
		Duties:     []conductorDutyRow{},
	}
	if res.Configured {
		cfg := conductorConfigDocument(settings, path)
		res.Config = &cfg
	}
	observed, current := journal.Observe()
	res.State = string(observed)
	if observed == conductor.StateRunning || observed == conductor.StateInterrupted {
		row := conductorCycleDocument(current)
		res.Current = &row
	}

	dutyRung := conductor.NewDutyRung(settings.duties(), journal, func() time.Time { return now }, 0)
	ladder := conductor.DefaultLadder(
		conductor.NewInvitationRung(state.dispositions,
			conductor.NewRecordOrigins(state.frontier, state.runs)),
		dutyRung,
		conductor.NewSerendipityRung(&hostCorpus{app: a, adapters: adapters()},
			embeddedRecipes{}, drawGenerator(), settings.SliceSessions),
	)
	rungs, err := conductor.Describe(ctx, ladder)
	if err != nil {
		return err
	}
	for _, rung := range rungs {
		res.Rungs = append(res.Rungs, conductorRungRow{
			Name:        Sanitize(rung.Name),
			Waiting:     rung.Depth.Waiting,
			Implemented: rung.Depth.Implemented,
			Note:        Sanitize(rung.Depth.Note),
		})
	}
	for _, duty := range dutyRung.States(now) {
		row := conductorDutyRow{
			Name:      Sanitize(duty.Name),
			Recipe:    Sanitize(duty.Recipe),
			Dimension: Sanitize(string(duty.Dimension)),
			Enabled:   duty.Enabled,
			Due:       duty.Due,
			Note:      Sanitize(duty.Note),
		}
		if !duty.LastDrawnAt.IsZero() {
			row.LastDrawn = formatTime(duty.LastDrawnAt)
		}
		res.Duties = append(res.Duties, row)
	}

	if res.Configured {
		ceilings := settings.ceilings()
		spend, err := conductor.NewReceiptLedger(state.runs).
			SpentSince(ctx, conductor.StartOfDay(now), ceilings.Currency)
		if err != nil {
			return err
		}
		res.Spend = &conductorSpendRow{
			Currency:   ceilings.Currency,
			Spent:      spend.Amount,
			PerDay:     ceilings.PerDay,
			Remaining:  spend.Remaining(ceilings),
			PerCycle:   ceilings.PerCycle,
			Runs:       spend.Runs,
			Unpriced:   spend.Unpriced,
			Journalled: journal.SpentToday(now, ceilings.Currency),
		}
	}
	for _, cycle := range journal.Recent(*count) {
		res.Cycles = append(res.Cycles, conductorCycleDocument(cycle))
	}

	if *asJSON {
		return a.emitJSON(res)
	}
	a.writeConductorStatus(res)
	return nil
}

// writeConductorStatus renders the report for a terminal, in the order an
// operator asks the questions: what is it doing, why, what is queued, what has
// it spent, what did it just do.
func (a *app) writeConductorStatus(res conductorStatusResult) {
	rows := [][2]string{{"state", res.State}}
	if res.Current != nil {
		rows = append(rows,
			[2]string{"cycle", strconv.Itoa(res.Current.Seq)},
			[2]string{"authority", authorityLabel(res.Current.AuthorityKind, res.Current.AuthorityRef)},
			[2]string{"run", orMissing(res.Current.RunID)})
	}
	if res.Config != nil {
		rows = append(rows,
			[2]string{"ceilings", fmt.Sprintf("%.2f per cycle, %.2f per day %s",
				res.Config.PerCycle, res.Config.PerDay, res.Config.Currency)},
			[2]string{"serendipity floor", fmt.Sprintf("one cycle in %d", res.Config.Floor)})
	} else {
		rows = append(rows, [2]string{"ceilings",
			"none; run \"babel conductor configure --per-cycle AMOUNT --per-day AMOUNT\""})
	}
	if s := res.Spend; s != nil {
		spent := fmt.Sprintf("%.2f of %.2f %s today, %.2f left", s.Spent, s.PerDay, s.Currency, s.Remaining)
		if s.Unpriced > 0 {
			spent += fmt.Sprintf("; %d %s no cost", s.Unpriced,
				plural(s.Unpriced, "run reported", "runs reported"))
		}
		rows = append(rows, [2]string{"spend", spent})
	}
	rows = append(rows, [2]string{"journal", res.Journal})
	writeDetail(a.stdout, rows)

	fmt.Fprintf(a.stdout, "\nladder\n")
	for _, rung := range res.Rungs {
		if !rung.Implemented {
			fmt.Fprintf(a.stdout, "  %-12s not implemented — %s\n", rung.Name, rung.Note)
			continue
		}
		fmt.Fprintf(a.stdout, "  %-12s %d — %s\n", rung.Name, rung.Waiting, rung.Note)
	}

	// The duties are printed whatever their state, and printed after the rung
	// whose queue they are: an operator asking why the loop is not improving
	// Babel needs to read that the duty exists and is off, which is one line
	// and the difference between an absent feature and an unauthorized one.
	if len(res.Duties) > 0 {
		fmt.Fprintf(a.stdout, "\nduties\n")
		for _, duty := range res.Duties {
			fmt.Fprintf(a.stdout, "  %-22s %s\n", duty.Name, duty.Note)
		}
	}
	fmt.Fprintln(a.stdout)
	a.writeConductorCycles(res.Cycles)
}

// writeConductorCycles renders a cycle list as a table. Authority is a column
// rather than a footnote: it is the answer to the question the loop exists to
// keep answerable.
func (a *app) writeConductorCycles(cycles []conductorCycleRow) {
	if len(cycles) == 0 {
		fmt.Fprintf(a.stdout, "no cycles recorded\n")
		return
	}
	table := make([][]string, 0, len(cycles))
	for _, c := range cycles {
		table = append(table, []string{
			strconv.Itoa(c.Seq),
			c.Outcome,
			orMissing(c.Rung),
			authorityLabel(c.AuthorityKind, c.AuthorityRef),
			orMissing(c.ReceiptID),
			firstLine(c.Note, c.Reason),
		})
	}
	writeTable(a.stdout, []string{"CYCLE", "OUTCOME", "RUNG", "AUTHORITY", "RECEIPT", "WHY"}, table)
}

// authorityLabel renders a recorded authority, and says so when there is none.
//
// A cycle with no authority is a parked or idle one, which never ran and so
// never needed a why. A *receipt* with no authority is a different statement —
// it predates the field — and that phrasing belongs where receipts are rendered,
// not here.
func authorityLabel(kind, ref string) string {
	if kind == "" {
		return "-"
	}
	if ref == "" {
		return kind
	}
	return kind + " " + ref
}

// firstLine picks the first non-empty explanation, which is what a table cell
// has room for.
func firstLine(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "-"
}
