package web

// The Watch surface: what this deployment is doing now, what it has done per
// day, what one run actually did, and the operator's own start and stop
// (SPEC.md §8.6, decision 90, Contract W).
//
// §8.4 refused to start runs from the browser and that refusal was withdrawn on
// 2026-09-12, so this file holds the first two routes in Babel that spawn and
// signal a process. Five rules hold across all of it, and they are what keep a
// control room from becoming a second scheduler.
//
// A launch is this machine's own binary, with the CLI's own flags, under the
// CLI's own refusals. POST /api/watch/launch does not reimplement an
// exploration: it runs os.Executable() with the subcommand and flags `babel
// explore`, `babel evaluate` and `babel conductor run` already take, so the
// ceilings, the stored profile, the worker resolution, the authority and the
// receipt are the terminal's. A browser that could assemble a run of its own
// would be a second implementation of the one thing §14 requires to have
// exactly one.
//
// A stop is graceful or it is nothing. internal/cli's stop file interrupts at a
// safe point and SIGTERM asks a loop to finish the cycle in flight; both leave
// every committed record durable and the unexplored frontier deferred. There is
// no SIGKILL here, not by convention but because the Launcher interface has no
// method that could: a killed run loses the receipt that says what it spent.
//
// The machine is not a dimension of this surface. Presence rows are read
// deployment-wide and no host field reaches the wire — a run is the
// deployment's work, and decision 90 keeps "which laptop" off the reading path.
// The one asymmetry that does reach it is stoppability, which is a fact about
// this process rather than about a machine: this server can stop a child it
// started and nothing else, because cross-machine invocation is explicitly out
// of scope (#118).
//
// Absence is never zero. A run with no readable receipt has no spend, a day
// whose receipts recorded no usage has no cost, a receipt written before a
// field existed has no value for it — and every one of those is an omitted key
// rather than a 0.0 that reads as a measurement. The same rule is why each
// series says which source answered: a build whose evaluation store would not
// open has no reviews-per-day, which is different from a fortnight of nobody
// reviewing anything.
//
// Nothing here is durable state. This file reads presence, the frontier's own
// per-day counts, the evaluation store's, the session listing and local receipt
// bodies; it writes no record, and the two POSTs write nothing but a process and
// a stop file.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/run"
)

// Launcher starts and stops the analysis this machine runs for itself, and
// holds the spend ceilings it runs under.
//
// It is an interface for the reason every other service on this surface is:
// the method set is the whole authority. There is no method that reaches
// another host, none that kills a process, and none that launches anything but
// the three subcommands LaunchRequest can name, so what a browser can do here
// is bounded by a type rather than by a handler's care.
//
// Launched is the process's own list rather than a query over the deployment.
// A child this server started is the only run it can stop, and pairing a pid
// with a run this server did not start is exactly the mistake that would stop
// the wrong work.
//
// The ceilings belong here rather than beside the analysis settings for the
// same reason the launch does: they are `babel conductor configure`'s own
// document, they are the thing a refused launch tells the operator to set, and
// the only honest way to write them from a browser is to run the command that
// owns them. A second writer of conductor.json would be a second set of
// refusals about the same two numbers.
type Launcher interface {
	// Launched lists the children this machine started that have not exited.
	Launched() []LaunchedRun
	// Launch starts one run and reports what it started. A request the
	// CLI's own preflight refuses comes back as ErrConflict wrapping the
	// CLI's own refusal text, and a request that names no work as
	// ErrBadRequest.
	Launch(ctx context.Context, req LaunchRequest) (LaunchedRun, error)
	// Stop asks one child to stop at a safe point and reports how it asked.
	Stop(ctx context.Context, pid int) (StopResult, error)
	// Ceilings reads the stored conductor configuration — the same document
	// `babel conductor status --json` reports, read from the same file.
	Ceilings(ctx context.Context) (CeilingSettings, error)
	// Configure stores it by running `babel conductor configure` with the
	// flags this request names, so a ceiling set from a browser is a ceiling
	// set by the command: same validation, same incremental semantics, same
	// refusals. A malformed one comes back as ErrBadRequest wrapping the
	// command's own sentence.
	Configure(ctx context.Context, req CeilingRequest) (CeilingSettings, error)
}

// LaunchRequest is one run the operator asked for.
//
// Kind names the subcommand and Args names its flags. Every numeric flag is a
// pointer because the CLI distinguishes a flag named as zero from a flag not
// named at all — `--consolidate 0` is how an operator runs a pure discovery
// loop on a machine configured to consolidate — and a shape that could not
// carry that distinction would silently change what the loop does.
type LaunchRequest struct {
	Kind string     `json:"kind"`
	Args LaunchArgs `json:"args"`
}

// LaunchArgs is the union of the flags the three launchable subcommands take.
// It is one struct rather than three because the browser sends one document,
// and the Launcher refuses an argument its kind has no flag for rather than
// dropping it: a launch that silently ignored `--until` would run until the
// operator stopped it.
type LaunchArgs struct {
	// explore
	Preparation string   `json:"preparation,omitempty"`
	Recipe      []string `json:"recipe,omitempty"`
	Develop     *int     `json:"develop,omitempty"`
	// explore and evaluate
	Retrievals *int `json:"retrievals,omitempty"`
	Fetches    *int `json:"fetches,omitempty"`
	// evaluate
	Correct string `json:"correct,omitempty"`
	// conductor run
	Until       string `json:"until,omitempty"`
	Concurrent  *int   `json:"concurrent,omitempty"`
	Consolidate *int   `json:"consolidate,omitempty"`
	Evaluate    *int   `json:"evaluate,omitempty"`
	Once        bool   `json:"once,omitempty"`
	// explore and conductor run
	Challenge  bool `json:"challenge,omitempty"`
	Synthesize bool `json:"synthesize,omitempty"`
}

// LaunchedRun is one child this server started.
//
// RunID is empty while the run has not said what it is: a child mints its own
// identity and reports it in its receipt, so this surface knows the pid long
// before it knows the run. An empty run id is reported as absent rather than
// filled in with the pid, because a pid is not a run identity and a page that
// linked one to a run record would link to nothing.
type LaunchedRun struct {
	PID       int
	Kind      string
	RunID     string
	StartedAt time.Time
	LogPath   string
}

// StopResult is how a stop was asked for, which the operator has to be able to
// read: a stop file is honoured at the next safe point and a signal is honoured
// at the next cycle boundary, so "stopping" means something different in each
// case and neither is instant.
type StopResult struct {
	// Method is "stop-file" or "signal".
	Method string
	// Detail is one sentence naming what will happen, in the CLI's terms.
	Detail string
}

// CeilingSettings is the conductor configuration this machine holds: the two
// spend ceilings autonomy is bounded by, the scheduling dials, and the
// standing duties the operator has authorized.
//
// It is the shape `babel conductor configure --json` and `babel conductor
// status --json` already emit, under the same field names, because it is the
// same document read from the same file. A surface that renamed these keys
// would make an operator comparing the browser with the terminal wonder
// whether they are looking at two configurations.
//
// Configured is what keeps "no ceilings" apart from "ceilings of zero", which
// the conductor refuses for the same reason but tells the operator differently:
// PerCycle and PerDay are absent entirely on a machine that has never set
// them, because a limit nobody chose is not a limit of nothing.
type CeilingSettings struct {
	Configured bool     `json:"configured"`
	Currency   string   `json:"currency,omitempty"`
	PerCycle   *float64 `json:"per_cycle,omitempty"`
	PerDay     *float64 `json:"per_day,omitempty"`
	// The dials below always have a value: where the operator set none, the
	// figure is the default the loop actually runs under, which is what
	// `conductor status` reports and is a fact rather than a blank.
	Floor                int    `json:"serendipity_floor"`
	IntervalSeconds      int    `json:"interval_seconds"`
	SliceSessions        int    `json:"slice_sessions"`
	ConsolidateOneIn     int    `json:"consolidate_one_in"`
	ConsolidateRoots     int    `json:"consolidate_roots"`
	EvaluateOneIn        int    `json:"evaluate_one_in"`
	EvaluateCadence      string `json:"evaluate_cadence"`
	BabelImprovesBabel   bool   `json:"babel_improves_babel"`
	BabelTunesItself     bool   `json:"babel_tunes_itself"`
	BabelTriagesTheQueue bool   `json:"babel_triages_the_queue"`
	ConfiguredAt         string `json:"configured_at,omitempty"`
	// Path is where the document is, so an operator who would rather edit it
	// in a terminal knows which file the browser just wrote.
	Path string `json:"path,omitempty"`
}

// CeilingRequest is one `babel conductor configure` invocation.
//
// Every field is one of that command's flags, and every one is a pointer or an
// empty-able string because the command is incremental: an operator raising the
// day's ceiling has not withdrawn a standing duty, so "not named" has to stay
// distinguishable from "named as zero" and from "named as off". The Launcher
// turns a named field into a flag and leaves an unnamed one off the command
// line entirely.
//
// Interval and EvaluateCadence travel as the durations the flags take —
// "45m", "1h30m" — so the parse, and the refusal for a malformed one, stay the
// flag package's inside the command rather than becoming a second dialect here.
type CeilingRequest struct {
	PerCycle *float64 `json:"per_cycle,omitempty"`
	PerDay   *float64 `json:"per_day,omitempty"`
	Currency string   `json:"currency,omitempty"`
	Floor    *int     `json:"floor,omitempty"`
	Interval string   `json:"interval,omitempty"`
	// Slice is --slice-sessions: how many sessions a serendipity draw reads.
	Slice            *int   `json:"slice_sessions,omitempty"`
	Consolidate      *int   `json:"consolidate,omitempty"`
	ConsolidateRoots *int   `json:"consolidate_roots,omitempty"`
	Evaluate         *int   `json:"evaluate,omitempty"`
	EvaluateCadence  string `json:"evaluate_cadence,omitempty"`
	// The three standing-duty authorizations, each tri-state: true is the
	// duty's flag, false is its --no- form, and absent names neither.
	ImprovesBabel   *bool `json:"babel_improves_babel,omitempty"`
	TunesItself     *bool `json:"babel_tunes_itself,omitempty"`
	TriagesTheQueue *bool `json:"babel_triages_the_queue,omitempty"`
}

// DrainReader reports the last automatic publication attempt this process
// made.
//
// The second result is false until an attempt has happened, which is a state
// rather than an empty report: a server that has been up for ten seconds has
// published nothing and owes nothing, and rendering that as "0 records
// published at the zero time" would be a measurement nobody took.
type DrainReader interface {
	LastDrain() (DrainReport, bool)
}

// DrainReport is one publication attempt's outcome. Every field was measured by
// the attempt, so they are plain numbers; an attempt that has not happened is
// the absence DrainReader's second result reports.
type DrainReport struct {
	At        time.Time
	Published int
	Sealed    int
	Pending   int
}

// watchPathPrefix is the Watch surface's own path space. It is resolved by
// prefix rather than by whole paths because one of its routes carries a run id
// in the path, and routeAPI's table is whole paths.
const watchPathPrefix = "/api/watch/"

// watchSeriesDefaultDays and watchSeriesMaxDays bound the series. Ninety days
// is the cap because the series is rendered as small multiples: past that a
// day is less than a pixel, and the cost is a scan per kind per request.
const (
	watchSeriesDefaultDays = 30
	watchSeriesMaxDays     = 90
)

// watchRunsDefaultLimit and watchRunsMaxLimit bound the recent-runs listing,
// which reads receipt bodies and is therefore the most expensive read here.
const (
	watchRunsDefaultLimit = 50
	watchRunsMaxLimit     = 200
)

// receiptScanPages bounds how far back a spend series reads. The store pages
// receipts newest first with no date predicate, so the window is honoured by
// stopping at the first page that falls out of it; this cap is what keeps a
// deployment with a very long history from turning one request into a full
// scan. A window that is not fully covered is reported rather than silently
// truncated.
const receiptScanPages = 20

// routeWatch resolves the Watch surface's routes, and reports whether it
// recognized the path at all so routeAPI's default branch can keep answering
// 404 for everything else.
func (s *Server) routeWatch(w http.ResponseWriter, r *http.Request) bool {
	rest, found := strings.CutPrefix(r.URL.Path, watchPathPrefix)
	if !found {
		return false
	}
	switch {
	case rest == "live":
		if s.requireMethod(w, r, http.MethodGet) {
			s.handleWatchLive(w, r)
		}
	case rest == "series":
		if s.requireMethod(w, r, http.MethodGet) {
			s.handleWatchSeries(w, r)
		}
	case rest == "runs":
		if s.requireMethod(w, r, http.MethodGet) {
			s.handleWatchRuns(w, r)
		}
	case strings.HasPrefix(rest, "runs/"):
		if s.requireMethod(w, r, http.MethodGet) {
			s.handleWatchRun(w, r, strings.TrimPrefix(rest, "runs/"))
		}
	case rest == "launch":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleWatchLaunch(w, r)
		}
	case rest == "stop":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleWatchStop(w, r)
		}
	// The one path on this surface that both reads and writes the same
	// document: what the machine may spend, and the operator setting it.
	case rest == "ceilings":
		switch r.Method {
		case http.MethodGet:
			s.handleWatchCeilings(w, r)
		case http.MethodPost:
			s.handleWatchConfigureCeilings(w, r)
		default:
			s.writeError(w, http.StatusBadRequest, "unsupported method")
		}
	default:
		return false
	}
	return true
}

// watchLive is GET /api/watch/live.
type watchLive struct {
	Runs []watchLiveRun `json:"runs"`
	// Drain is the last automatic publication attempt, absent until one has
	// happened. A served surface drains every minute (SPEC.md §9.1), so this
	// is what tells an operator whether the disk he is reading from is still
	// the only place his analysis exists.
	Drain *watchDrain `json:"drain,omitempty"`
	// Presence says whether this machine can see the deployment's announced
	// runs at all. A machine in local mode cannot, which is configuration
	// rather than a fault, and the rows it does have are its own children.
	Presence overviewSection `json:"presence"`
	// Launcher says whether this session can start and stop runs.
	Launcher overviewSection `json:"launcher"`
}

// watchLiveRun is one run in flight.
//
// It carries two things a presence row cannot: whether this server can stop it,
// and the pid to stop it by. Everything else is the announcement's own, because
// this surface observes nothing about another process that the process did not
// say.
type watchLiveRun struct {
	// Source is "announced" for a presence row and "launched" for a child
	// this server started. The two are different evidence, not two renderings
	// of one thing: an announcement is a claim a run made into the shared
	// catalog, and a launched row is a process this server holds. A shared-mode
	// machine can therefore see one of its own children from both sides, which
	// is honest — nothing here guesses which announced row is which pid, since
	// attaching the wrong pid to a row is how a stop stops the wrong run.
	Source string `json:"source"`
	RunID  string `json:"run_id,omitempty"`
	// Kind is the work's own word: "explore" or "conductor" from an
	// announcement, and the launched subcommand for a child.
	Kind      string `json:"kind"`
	StartedAt string `json:"started_at,omitempty"`
	// Stage is the phase this row can honestly report: the recipe a run
	// announced, "launching" for a child that has not announced anything yet,
	// and otherwise the announced state.
	Stage  string `json:"stage,omitempty"`
	Recipe string `json:"recipe,omitempty"`
	State  string `json:"state,omitempty"`
	// Freshness and HeartbeatAgeSeconds travel together, always. A row says a
	// run was alive at its last heartbeat and nothing about now, so a page
	// that rendered a live dot from the word alone would be asserting
	// liveness nobody observed (internal/presence states this at length).
	Freshness           string        `json:"freshness,omitempty"`
	HeartbeatAgeSeconds *int64        `json:"heartbeat_age_s,omitempty"`
	Authority           *RunAuthority `json:"authority,omitempty"`
	// SpendUSD is what this run's own receipt recorded, absent while it has
	// none. A run in flight usually has no receipt at all — it is written when
	// the run ends — so absence is the normal case here and reads as "not yet
	// accounted", never as free.
	SpendUSD *float64 `json:"spend_usd,omitempty"`
	// Records counts what this run has already written to the frontier,
	// absent when this build's frontier cannot answer by run id.
	Records *int `json:"records,omitempty"`
	// PID and Stoppable are this process's own facts. A run this server did
	// not start carries neither: it may be on another machine, and remote
	// control is out of scope.
	PID       *int `json:"pid,omitempty"`
	Stoppable bool `json:"stoppable"`
	// LogPath is where a launched child's output is being written, so an
	// operator can follow a run this browser started from a terminal.
	LogPath string `json:"log_path,omitempty"`
}

// watchDrain is the last publication attempt.
type watchDrain struct {
	LastAt    string `json:"last_at"`
	Published int    `json:"published"`
	Sealed    int    `json:"sealed"`
	Pending   int    `json:"pending"`
}

func (s *Server) handleWatchLive(w http.ResponseWriter, r *http.Request) {
	live := watchLive{Runs: []watchLiveRun{}}
	live.Presence = s.livePresence(r, &live)
	if s.opts.Launcher == nil {
		live.Launcher = sectionMissing(launcherAbsent)
	} else {
		live.Launcher = sectionReady()
		for _, child := range s.opts.Launcher.Launched() {
			live.Runs = append(live.Runs, s.viewLaunchedRun(r.Context(), child))
		}
	}
	if s.opts.Drain != nil {
		if report, ok := s.opts.Drain.LastDrain(); ok {
			live.Drain = &watchDrain{
				LastAt:    rfc3339(report.At),
				Published: report.Published,
				Sealed:    report.Sealed,
				Pending:   report.Pending,
			}
		}
	}
	s.writeJSON(w, http.StatusOK, live)
}

// launcherAbsent is what a session with no launcher says. It names the one
// thing that makes launching possible rather than a failure, because a build
// wired without a launcher is a build that cannot start runs from here and the
// terminal still can.
const launcherAbsent = "this session cannot start runs: no launcher is wired, so use " +
	"\"babel explore\", \"babel evaluate\" or \"babel conductor run\" from a terminal"

// livePresence reads the deployment's announced runs into live.Runs and reports
// whether this machine could see them.
//
// The three unavailable sentences are presence.go's own, verbatim: the read is
// the same read, so an operator meeting it on two surfaces is told the same
// thing. Only running rows are carried — this is a list of work in flight, and
// a finished announcement belongs to the Fleet view's retention window.
func (s *Server) livePresence(r *http.Request, live *watchLive) overviewSection {
	if s.opts.Presence == nil {
		return sectionMissing(presenceAbsent)
	}
	rows, err := s.opts.Presence.Fleet(r.Context())
	if err != nil {
		switch {
		case presence.NotConfigured(err):
			return sectionMissing(presenceAbsent)
		case presence.Unreachable(err):
			s.logf("watch live: the shared catalog could not be reached")
			return sectionMissing(presenceUnreachable)
		default:
			s.logf("watch live: the shared catalog refused the read")
			return sectionMissing(presenceRefused)
		}
	}
	for _, row := range rows {
		if row.State != presence.StateRunning {
			continue
		}
		live.Runs = append(live.Runs, s.viewAnnouncedRun(r.Context(), row))
	}
	return sectionReady()
}

// viewAnnouncedRun renders one announced run in flight.
func (s *Server) viewAnnouncedRun(ctx context.Context, row presence.Row) watchLiveRun {
	age := ageSeconds(row.HeartbeatAge)
	view := watchLiveRun{
		Source:              "announced",
		RunID:               row.RunID,
		Kind:                string(row.Kind),
		StartedAt:           rfc3339(row.StartedAt),
		Stage:               announcedStage(row),
		Recipe:              row.Recipe,
		State:               string(row.State),
		Freshness:           string(row.Freshness),
		HeartbeatAgeSeconds: &age,
	}
	if row.Authority.Recorded() {
		view.Authority = &RunAuthority{Kind: string(row.Authority.Kind), Ref: row.Authority.Ref}
	}
	view.SpendUSD = s.runSpend(ctx, row.RunID)
	view.Records = s.runRecordCount(ctx, row.RunID)
	return view
}

// announcedStage is the phase an announcement can state. The recipe is the
// closest thing to a stage a presence row carries — it is what the run is
// applying — and a conductor cycle that has not resolved an assignment yet has
// none, which is reported as its state instead of as an invented phase.
func announcedStage(row presence.Row) string {
	if row.Recipe != "" {
		return row.Recipe
	}
	return string(row.State)
}

// viewLaunchedRun renders one child this server started.
func (s *Server) viewLaunchedRun(ctx context.Context, child LaunchedRun) watchLiveRun {
	pid := child.PID
	view := watchLiveRun{
		Source:    "launched",
		RunID:     child.RunID,
		Kind:      child.Kind,
		StartedAt: rfc3339(child.StartedAt),
		Stage:     "launching",
		PID:       &pid,
		Stoppable: true,
		LogPath:   child.LogPath,
	}
	if child.RunID != "" {
		view.Stage = ""
		view.SpendUSD = s.runSpend(ctx, child.RunID)
		view.Records = s.runRecordCount(ctx, child.RunID)
	}
	return view
}

// runSpend is what one run's own receipt recorded, or nothing.
//
// It reads the run's newest revision because that is the run's current account
// of itself, and it reports absence for every reason a cost can be missing: no
// receipt yet, a receipt from a run that never reached the worker, or a worker
// that returned no usage. None of those is zero dollars.
func (s *Server) runSpend(ctx context.Context, runID string) *float64 {
	if s.opts.Receipts == nil || runID == "" {
		return nil
	}
	revisions, err := s.opts.Receipts.Revisions(ctx, runID)
	if err != nil || len(revisions) == 0 {
		return nil
	}
	return runCostUSD(revisions[len(revisions)-1])
}

// runRecordCount counts what one run has written to the frontier, or nothing.
func (s *Server) runRecordCount(ctx context.Context, runID string) *int {
	outputs, ok := s.runOutputs(ctx, runID)
	if !ok {
		return nil
	}
	count := len(outputs)
	return &count
}

// runOutputs reads one run's own records, reporting whether this session could
// answer at all.
//
// The second result is what keeps "the frontier is not open here" apart from
// "this run wrote nothing". Both render as no rows, and only one of them is a
// statement about the run.
func (s *Server) runOutputs(ctx context.Context, runID string) ([]frontier.RunOutput, bool) {
	if s.opts.Frontier == nil || runID == "" {
		return nil, false
	}
	outputs, err := s.opts.Frontier.OutputsOfRun(ctx, runID)
	if err != nil {
		s.logf("watch: the frontier could not list the records of one run")
		return nil, false
	}
	return outputs, true
}

// watchSeries is GET /api/watch/series.
type watchSeries struct {
	// Days covers every day in the window, oldest first, including the ones
	// on which nothing happened: a series with holes in it cannot be drawn on
	// an axis, and a day that produced nothing is a fact the sources answered
	// for.
	Days []watchSeriesDay `json:"days"`
	// From and To are the window this answer covers, so a page renders the
	// axis the server bucketed by rather than one derived from its own clock.
	From string `json:"from"`
	To   string `json:"to"`
	// Requested is the window the caller asked for, after the cap, so a page
	// that asked for a year can say it was answered with ninety days.
	Requested int `json:"days_requested"`
	// Sources says which of the four answered. A source that did not is the
	// reason its key is absent from every day, and saying so is what keeps an
	// unopened store from reading as an idle fortnight.
	Sources watchSeriesSources `json:"sources"`
}

// watchSeriesSources is the availability of each series.
type watchSeriesSources struct {
	Records  overviewSection `json:"records"`
	Reviews  overviewSection `json:"reviews"`
	Sessions overviewSection `json:"sessions"`
	Spend    overviewSection `json:"spend"`
}

// watchSeriesDay is one UTC day.
type watchSeriesDay struct {
	Day string `json:"day"`
	// Records is absent when the frontier could not answer, and otherwise
	// carries a measured count per kind — including zero, which the query
	// observed.
	Records *watchRecordCounts `json:"records,omitempty"`
	Reviews *int               `json:"reviews,omitempty"`
	// Sessions counts the sessions whose last recorded activity falls on this
	// day. It is the listing's own timestamp: the catalog records when a
	// session was last modified, so this is "days the corpus grew", not "days
	// somebody opened a terminal".
	Sessions *int `json:"sessions,omitempty"`
	// SpendUSD is summed from local receipt bodies and absent where no
	// receipt on that day recorded a cost. A day with runs and no recorded
	// usage is unknown spend, never free.
	SpendUSD *float64 `json:"spend_usd,omitempty"`
}

// watchRecordCounts is one day's records by kind.
type watchRecordCounts struct {
	Hypothesis  int `json:"hypothesis"`
	Observation int `json:"observation"`
	Finding     int `json:"finding"`
	Proposal    int `json:"proposal"`
}

func (s *Server) handleWatchSeries(w http.ResponseWriter, r *http.Request) {
	days, ok := queryInt(r, "days", watchSeriesDefaultDays)
	if !ok || days <= 0 {
		s.writeError(w, http.StatusBadRequest, "days must be a positive number of days")
		return
	}
	if days > watchSeriesMaxDays {
		days = watchSeriesMaxDays
	}
	// The window is whole UTC days ending today, because a day is the bucket
	// every source is counted in: a window that started mid-day would put a
	// partial day at each end and make the first bar of every chart a lie.
	to := time.Now().UTC().Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -(days - 1))
	series := watchSeries{From: dayKey(from), To: dayKey(to), Requested: days}

	buckets := make(map[string]*watchSeriesDay, days)
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		series.Days = append(series.Days, watchSeriesDay{Day: dayKey(day)})
	}
	for i := range series.Days {
		buckets[series.Days[i].Day] = &series.Days[i]
	}

	series.Sources.Records = s.seriesRecords(r.Context(), from, buckets)
	series.Sources.Reviews = s.seriesReviews(r.Context(), from, buckets)
	series.Sources.Sessions = s.seriesSessions(r.Context(), buckets)
	series.Sources.Spend = s.seriesSpend(r.Context(), from, buckets)
	s.writeJSON(w, http.StatusOK, series)
}

// The sentences each unavailable series says. They name the store rather than
// the failure, on fleet.go's terms: the store is what an operator can look at.
const (
	seriesNoFrontier    = "this session holds no analysis frontier, so records per day cannot be counted"
	seriesNoRecords     = "the analysis frontier could not be counted by day"
	seriesNoReviews     = "this session holds no evaluation store, so reviews per day cannot be counted"
	seriesNoReviewsRead = "the evaluation store could not be counted by day"
	seriesNoSessions    = "this session holds no session listing, so sessions per day cannot be counted"
	seriesNoReceipts    = "this session holds no run receipts, so spend per day cannot be summed"
	seriesNoSpend       = "the run receipts could not be read"
	seriesSpendCut      = "the spend window reaches further back than this many receipts, so the " +
		"earliest days of it are incomplete"
)

// seriesRecords fills in records per day and kind.
func (s *Server) seriesRecords(ctx context.Context, from time.Time,
	buckets map[string]*watchSeriesDay) overviewSection {
	if s.opts.Frontier == nil {
		return sectionMissing(seriesNoFrontier)
	}
	rows, err := s.opts.Frontier.RecordDays(ctx, from)
	if err != nil {
		s.logf("watch series: the frontier could not be counted by day")
		return sectionMissing(seriesNoRecords)
	}
	// The zero counts are written first, for every day, because the store
	// answered for all of them: a day the query returned no row for produced
	// nothing, which is a measurement rather than an absence.
	for _, bucket := range buckets {
		bucket.Records = &watchRecordCounts{}
	}
	for _, row := range rows {
		bucket, ok := buckets[row.Day]
		if !ok {
			continue
		}
		switch frontier.EntityType(row.Kind) {
		case frontier.EntityHypothesis:
			bucket.Records.Hypothesis += row.Count
		case frontier.EntityObservation:
			bucket.Records.Observation += row.Count
		case frontier.EntityFinding:
			bucket.Records.Finding += row.Count
		case frontier.EntityProposal:
			bucket.Records.Proposal += row.Count
		}
	}
	return sectionReady()
}

// seriesReviews fills in assessments per day.
func (s *Server) seriesReviews(ctx context.Context, from time.Time,
	buckets map[string]*watchSeriesDay) overviewSection {
	if s.opts.Evaluation == nil {
		return sectionMissing(seriesNoReviews)
	}
	rows, err := s.opts.Evaluation.AssessmentDays(ctx, from)
	if err != nil {
		s.logf("watch series: the evaluation store could not be counted by day")
		return sectionMissing(seriesNoReviewsRead)
	}
	for _, bucket := range buckets {
		zero := 0
		bucket.Reviews = &zero
	}
	for _, row := range rows {
		if bucket, ok := buckets[row.Day]; ok {
			*bucket.Reviews += row.Count
		}
	}
	return sectionReady()
}

// seriesSessions fills in sessions per day from the listing this machine
// already serves, which is why it costs nothing: the catalog answers from
// memory and the scan that keeps it current belongs to the process.
func (s *Server) seriesSessions(ctx context.Context,
	buckets map[string]*watchSeriesDay) overviewSection {
	if s.opts.Lister == nil {
		return sectionMissing(seriesNoSessions)
	}
	result, err := s.opts.Lister.ListSessions(ctx)
	if err != nil {
		s.logf("watch series: the session listing could not be read")
		return sectionMissing(seriesNoSessions)
	}
	for _, bucket := range buckets {
		zero := 0
		bucket.Sessions = &zero
	}
	for _, row := range result.Sessions {
		if row.Modified == nil {
			// A row the catalog has no modification time for belongs to no
			// day. Assigning it to today would invent activity.
			continue
		}
		at, err := time.Parse(time.RFC3339, *row.Modified)
		if err != nil {
			continue
		}
		if bucket, ok := buckets[dayKey(at)]; ok {
			*bucket.Sessions++
		}
	}
	return sectionReady()
}

// seriesSpend sums each day's cost from local receipt bodies.
//
// The bucket is the run's own start when its receipt recorded one and the
// receipt's recorded time otherwise, because what an operator reads off a
// spend chart is when the money was being spent rather than when the record of
// it was filed.
func (s *Server) seriesSpend(ctx context.Context, from time.Time,
	buckets map[string]*watchSeriesDay) overviewSection {
	if s.opts.Receipts == nil {
		return sectionMissing(seriesNoReceipts)
	}
	complete := true
	for page := range receiptScanPages {
		receipts, total, err := s.opts.Receipts.Receipts(ctx, run.MaxListLimit, page*run.MaxListLimit)
		if err != nil {
			s.logf("watch series: the run receipts could not be read")
			return sectionMissing(seriesNoSpend)
		}
		reached := false
		for _, receipt := range receipts {
			at := receiptTime(receipt)
			if at.Before(from) {
				// Receipts arrive newest first, so the first one out of the
				// window ends the read: everything behind it is older.
				reached = true
				break
			}
			cost := runCostUSD(receipt)
			if cost == nil {
				continue
			}
			bucket, ok := buckets[dayKey(at)]
			if !ok {
				continue
			}
			if bucket.SpendUSD == nil {
				sum := 0.0
				bucket.SpendUSD = &sum
			}
			*bucket.SpendUSD += *cost
		}
		if reached || len(receipts) == 0 || (page+1)*run.MaxListLimit >= total {
			break
		}
		if page+1 == receiptScanPages {
			complete = false
		}
	}
	if !complete {
		return sectionMissing(seriesSpendCut)
	}
	return sectionReady()
}

// watchRunList is GET /api/watch/runs.
type watchRunList struct {
	Runs []watchRunRow `json:"runs"`
	// Total is how many runs this machine has receipts for, so a listing can
	// say what it is a page of.
	Total int `json:"total"`
	overviewSection
}

// watchRunRow is one run as the recent-runs table shows it: the listing's own
// header fields plus the three numbers that were only in the body.
type watchRunRow struct {
	ReceiptID  string       `json:"receipt_id"`
	RunID      string       `json:"run_id"`
	Revision   int          `json:"revision"`
	RecordedAt string       `json:"recorded_at"`
	Sync       string       `json:"sync"`
	Authority  RunAuthority `json:"authority"`
	Counts     RunCounts    `json:"counts"`
	// Kind is derived from the authority reference, which is the only thing a
	// receipt records about why it ran. It is absent when the reference names
	// nothing this build recognizes, rather than guessed.
	Kind string `json:"kind,omitempty"`
	// The three numbers from the body, each absent when the receipt does not
	// say: a run that never reached the worker has no usage, and a receipt
	// written before timing existed has no duration.
	DurationSeconds *float64 `json:"duration_s,omitempty"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	Outputs         *int     `json:"outputs,omitempty"`
}

func (s *Server) handleWatchRuns(w http.ResponseWriter, r *http.Request) {
	list := watchRunList{Runs: []watchRunRow{}}
	if s.opts.Receipts == nil {
		list.overviewSection = sectionMissing(seriesNoReceipts)
		s.writeJSON(w, http.StatusOK, list)
		return
	}
	limit, ok := queryInt(r, "limit", watchRunsDefaultLimit)
	if !ok || limit <= 0 {
		s.writeError(w, http.StatusBadRequest, "limit must be a positive number of runs")
		return
	}
	if limit > watchRunsMaxLimit {
		limit = watchRunsMaxLimit
	}
	receipts, total, err := s.opts.Receipts.Receipts(r.Context(), limit, 0)
	if err != nil {
		s.logf("watch runs: the run receipts could not be read")
		s.writeError(w, http.StatusInternalServerError, "the run receipts could not be read")
		return
	}
	list.Total = total
	list.overviewSection = sectionReady()
	for _, receipt := range receipts {
		list.Runs = append(list.Runs, s.viewRunRow(r.Context(), receipt))
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) viewRunRow(ctx context.Context, receipt run.Receipt) watchRunRow {
	row := watchRunRow{
		ReceiptID:  string(receipt.Header.ID),
		RunID:      receipt.Header.RunID,
		Revision:   receipt.Header.Revision,
		RecordedAt: rfc3339(receipt.Header.RecordedAt),
		Sync:       receipt.Header.Sync,
		Authority: RunAuthority{
			Kind: string(receipt.Header.Authority.Kind),
			Ref:  receipt.Header.Authority.Ref,
		},
		Counts:          runCounts(receipt.Header.Counts),
		Kind:            runKind(receipt.Header.Authority),
		DurationSeconds: receiptDuration(receipt),
		CostUSD:         runCostUSD(receipt),
	}
	if outputs, ok := s.runOutputs(ctx, receipt.Header.RunID); ok {
		count := len(outputs)
		row.Outputs = &count
	}
	return row
}

// watchRun is GET /api/watch/runs/{run_id}: the header, and the half of the
// receipt that has never reached this surface before.
type watchRun struct {
	overviewSection
	watchRunRow
	// PreparationID is the corpus scope the run read, which is the one
	// header field a listing does not need and a run page does.
	PreparationID string `json:"preparation_id,omitempty"`
	Supersedes    string `json:"supersedes,omitempty"`
	// AmendmentReason is why this revision exists, present from revision 2.
	AmendmentReason string `json:"amendment_reason,omitempty"`
	// Revisions is how many receipts this run's chain holds, so a reader can
	// tell a corrected run from a first account of one.
	Revisions int `json:"revisions"`

	Timing    *watchTiming    `json:"timing,omitempty"`
	Resources *watchResources `json:"resources,omitempty"`
	Usage     *watchUsage     `json:"usage,omitempty"`
	Versions  watchVersions   `json:"versions"`
	Cookbook  []watchAsset    `json:"cookbook"`
	Retrieval []watchStep     `json:"retrieval"`
	Research  []watchSource   `json:"research"`
	// Candidates are what the run surfaced and did not develop, deferred and
	// rejected together with which it was: §5.2's promise that limits choose
	// what is explored now rather than what may exist is only auditable if
	// both are readable.
	Candidates []watchCandidate `json:"candidates"`
	Failures   []watchFailure   `json:"failures"`
	// Outputs are the run's own records: null when this build's frontier
	// cannot answer by run id, and an empty array when it answered that the
	// run published nothing.
	//
	// The two must not collapse, which is why this field carries no
	// `omitempty`: with it, a run that genuinely wrote nothing serialized
	// identically to a frontier that could not be asked, and the page then
	// told the operator "the record index cannot answer for this run" about a
	// run the index had answered for perfectly well. Every other array on
	// this receipt follows the same rule for the same reason.
	//
	// It deliberately shadows the embedded listing row's count of the same
	// name: a page that is showing the records themselves has no use for the
	// number beside them, and encoding/json resolves the collision in favour
	// of the shallower field, which is this one. The run-detail test asserts
	// the array, so the resolution is pinned rather than relied upon.
	Outputs []watchOutput `json:"outputs"`
}

type watchTiming struct {
	StartedAt  string   `json:"started_at,omitempty"`
	FinishedAt string   `json:"finished_at,omitempty"`
	DurationS  *float64 `json:"duration_s,omitempty"`
}

// watchResources mirrors run.Resources, and keeps every field a pointer for
// the same reason that type does: the receipt distinguishes a counted zero
// from nobody counting, and a surface that flattened the two would report a
// sandbox that wrote nothing as a sandbox nobody measured.
type watchResources struct {
	CPUSeconds          *float64 `json:"cpu_s,omitempty"`
	MaxRSSBytes         *int64   `json:"max_rss_bytes,omitempty"`
	SandboxBytesWritten *int64   `json:"sandbox_bytes_written,omitempty"`
	ToolCalls           *int     `json:"tool_calls,omitempty"`
}

// watchUsage is what the engine reported it spent, and what Code resolved the
// run onto. §7 requires a receipt to carry the resolved provider, model and
// thinking metadata Code returned and the profile it ran under; all of it is
// read from the worker's own receipt rather than from Babel's request, because
// what Babel asked for and what the engine used are two different facts.
type watchUsage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	TotalTokens  *int64 `json:"total_tokens,omitempty"`
	// CostUSD is the engine's own session accounting, quoted in the
	// profile's currency — which Currency names, because a figure whose unit
	// is assumed is a figure that will eventually be wrong.
	CostUSD  *float64 `json:"cost_usd,omitempty"`
	Currency string   `json:"currency,omitempty"`
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Profile  string   `json:"profile,omitempty"`
	// Engine is the Code build that ran, name and version, which is what a
	// containment question asked months later needs beside the model.
	Engine string `json:"engine,omitempty"`
}

// watchVersions is what enforced and shaped the run: which build of each
// capability facility, which job schema and prompt, which redaction and
// disclosure policy (§7).
type watchVersions struct {
	Capability map[string]string `json:"capability"`
	Job        map[string]string `json:"job"`
	Policy     map[string]string `json:"policy"`
}

type watchAsset struct {
	ID      string `json:"id"`
	Kind    string `json:"kind,omitempty"`
	Version int    `json:"version,omitempty"`
}

// watchStep is one retrieval the run performed. The hits carry their locator
// and the note recorded beside them, which is the point of the trace: an
// investigator that never searched for the contradicting term and one that
// searched and found nothing look identical without it.
type watchStep struct {
	Index   int        `json:"index"`
	Tool    string     `json:"tool"`
	Query   string     `json:"query"`
	At      string     `json:"at,omitempty"`
	Scope   string     `json:"scope,omitempty"`
	Results []watchHit `json:"results,omitempty"`
	Records []string   `json:"records,omitempty"`
	// ResultCount is the whole number of hits the step returned, which stays
	// exact even where Results is capped.
	ResultCount int `json:"result_count"`
}

type watchHit struct {
	Rank int    `json:"rank"`
	Path string `json:"path"`
	Line int    `json:"line"`
	// Digest is the record digest that recovers the bytes this hit claims,
	// which is what makes the citation checkable months later.
	Digest string `json:"digest,omitempty"`
	// Note is the model's own words about the hit. It is untrusted text and
	// renders through the client's sanitizer inside a quote.
	Note string `json:"note,omitempty"`
}

// stepResultCap bounds how many hits of one step reach the wire. A page shows
// a trace, not a corpus: the count beside it stays exact, so a capped step
// reads as "42 hits, the first 20 of them" rather than as a shorter search.
const stepResultCap = 20

type watchSource struct {
	URL         string   `json:"url"`
	RetrievedAt string   `json:"retrieved_at,omitempty"`
	MediaType   string   `json:"media_type,omitempty"`
	Bytes       int64    `json:"bytes"`
	Truncated   bool     `json:"truncated,omitempty"`
	Redirects   []string `json:"redirects,omitempty"`
	Digest      string   `json:"digest,omitempty"`
}

type watchCandidate struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	At     string `json:"at,omitempty"`
	// Disposition is "deferred" or "rejected". They are one list with a field
	// rather than two lists because they are the same act with different
	// outcomes, and a reader comparing them wants them in one column.
	Disposition string `json:"disposition"`
}

type watchFailure struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
	At      string `json:"at,omitempty"`
}

type watchOutput struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
}

func (s *Server) handleWatchRun(w http.ResponseWriter, r *http.Request, runID string) {
	if runID == "" {
		s.writeError(w, http.StatusBadRequest, "a run id is required")
		return
	}
	if !s.requireService(w, s.opts.Receipts != nil, "the run receipts") {
		return
	}
	revisions, err := s.opts.Receipts.Revisions(r.Context(), runID)
	if err != nil {
		s.logf("watch run: the run's receipts could not be read")
		s.writeError(w, http.StatusInternalServerError, "the run's receipts could not be read")
		return
	}
	if len(revisions) == 0 {
		s.writeError(w, http.StatusNotFound, "no receipt names this run")
		return
	}
	// The newest revision is the run's current account of itself. The
	// prior ones are not merged into it — an amendment states what it
	// corrects and the chain is how that is read — so this reports how many
	// there are and serves the last.
	receipt := revisions[len(revisions)-1]
	detail := watchRun{
		overviewSection: sectionReady(),
		watchRunRow:     s.viewRunRow(r.Context(), receipt),
		PreparationID:   string(receipt.Header.PreparationID),
		Supersedes:      string(receipt.Header.Supersedes),
		AmendmentReason: receipt.Body.AmendmentReason,
		Revisions:       len(revisions),
		Timing:          viewTiming(receipt.Body.Timing),
		Resources:       viewResources(receipt.Body.Resources),
		Usage:           viewUsage(receipt),
		Versions:        viewVersions(receipt.Body),
		Cookbook:        viewCookbook(receipt.Body),
		Retrieval:       viewRetrieval(receipt.Body),
		Research:        viewResearch(receipt.Body),
		Candidates:      viewCandidates(receipt.Body),
		Failures:        viewFailures(receipt.Body),
	}
	if outputs, ok := s.runOutputs(r.Context(), receipt.Header.RunID); ok {
		detail.Outputs = make([]watchOutput, 0, len(outputs))
		for _, output := range outputs {
			detail.Outputs = append(detail.Outputs, watchOutput{
				ID:    output.ID,
				Kind:  string(output.Kind),
				Title: output.Title,
			})
		}
	}
	s.writeJSON(w, http.StatusOK, detail)
}

func viewTiming(timing run.Timing) *watchTiming {
	if timing.StartedAt.IsZero() && timing.FinishedAt.IsZero() {
		return nil
	}
	view := &watchTiming{
		StartedAt:  rfc3339(timing.StartedAt),
		FinishedAt: rfc3339(timing.FinishedAt),
	}
	if !timing.StartedAt.IsZero() && !timing.FinishedAt.IsZero() {
		seconds := timing.Duration().Seconds()
		view.DurationS = &seconds
	}
	return view
}

func viewResources(resources run.Resources) *watchResources {
	if resources.CPUSeconds == nil && resources.MaxRSSBytes == nil &&
		resources.SandboxBytesWritten == nil && resources.ToolCalls == nil {
		return nil
	}
	return &watchResources{
		CPUSeconds:          resources.CPUSeconds,
		MaxRSSBytes:         resources.MaxRSSBytes,
		SandboxBytesWritten: resources.SandboxBytesWritten,
		ToolCalls:           resources.ToolCalls,
	}
}

// viewUsage reads what the engine reported, from the worker receipt embedded
// whole in the body.
//
// The resolved provider and model are read from the profile metadata Code
// returned, which is the same key internal/explore reads them under: Babel
// never chooses a model (§2.6), so the only honest source for which one ran is
// the engine's own answer, and a run whose engine returned none reports none.
func viewUsage(receipt run.Receipt) *watchUsage {
	boundary := receipt.Body.Worker
	if boundary == nil {
		return nil
	}
	view := &watchUsage{
		Currency: boundary.Cost.Currency,
		Model:    boundary.Metadata["model"],
		Provider: boundary.Metadata["provider"],
	}
	if boundary.Profile.ID != "" {
		view.Profile = boundary.Profile.String()
	}
	if boundary.Worker.Name != "" {
		view.Engine = strings.TrimSpace(boundary.Worker.Name + " " + boundary.Worker.Version)
	}
	if usage := boundary.Usage; usage != nil {
		// The token counts are the engine's measurements, so a zero among
		// them is a counted zero and travels as one; it is the whole Usage
		// block that is absent when the engine never answered
		// get_session_stats.
		input, output, total := usage.InputTokens, usage.OutputTokens, usage.TotalTokens
		view.InputTokens = &input
		view.OutputTokens = &output
		view.TotalTokens = &total
		if usage.Cost != 0 {
			cost := usage.Cost
			view.CostUSD = &cost
		}
	}
	if *view == (watchUsage{}) {
		return nil
	}
	return view
}

func viewVersions(body run.Body) watchVersions {
	versions := watchVersions{
		Capability: map[string]string{},
		Job:        map[string]string{},
		Policy:     map[string]string{},
	}
	// Each version is written only when the receipt holds one: an empty
	// string means the facility was not part of the run, and a key with an
	// empty value would render as a version nobody recorded.
	putVersion(versions.Capability, "sandbox", body.Capabilities.Sandbox)
	putVersion(versions.Capability, "tool", body.Capabilities.Tool)
	putVersion(versions.Capability, "repository", body.Capabilities.Repository)
	putVersion(versions.Capability, "public_research", body.Capabilities.PublicResearch)
	putVersion(versions.Job, "job", jobVersion(body.Job.Job))
	putVersion(versions.Job, "prompt", body.Job.Prompt)
	putVersion(versions.Job, "schema", body.Job.Schema)
	putVersion(versions.Policy, "redaction", body.Policy.Redaction)
	putVersion(versions.Policy, "disclosure", body.Policy.Disclosure)
	return versions
}

func putVersion(into map[string]string, key, value string) {
	if value != "" {
		into[key] = value
	}
}

// jobVersion renders the job document's own schema number, and renders an
// unrecorded one as nothing: receipts written before the field existed carry
// zero, and "job schema 0" would read as a version rather than as its absence.
func jobVersion(version int) string {
	if version == 0 {
		return ""
	}
	return strconv.Itoa(version)
}

func viewCookbook(body run.Body) []watchAsset {
	assets := make([]watchAsset, 0, len(body.Cookbook))
	for _, asset := range body.Cookbook {
		assets = append(assets, watchAsset{
			ID:      asset.Ref.ID,
			Kind:    asset.Kind,
			Version: asset.Ref.Version,
		})
	}
	return assets
}

func viewRetrieval(body run.Body) []watchStep {
	steps := make([]watchStep, 0, len(body.Retrieval))
	for _, step := range body.Retrieval {
		view := watchStep{
			Index:       step.Index,
			Tool:        step.Tool,
			Query:       step.Query,
			At:          rfc3339(step.At),
			Scope:       step.Scope,
			Records:     step.Records,
			ResultCount: len(step.Results),
		}
		for i, hit := range step.Results {
			if i == stepResultCap {
				break
			}
			locator := hit.Evidence.Locator()
			view.Results = append(view.Results, watchHit{
				Rank:   hit.Rank,
				Path:   locator.Path,
				Line:   locator.Line,
				Digest: locator.Digest,
				Note:   hit.Evidence.Note(),
			})
		}
		steps = append(steps, view)
	}
	return steps
}

// viewResearch lists the public documents the run was allowed to fetch. The
// fetched bytes are deliberately absent: the digest is what makes the citation
// checkable, and a receipt carrying pages would put a copy of the public web
// in the operator's store (internal/run says the same).
func viewResearch(body run.Body) []watchSource {
	sources := make([]watchSource, 0)
	for _, step := range body.Retrieval {
		if step.Research == nil {
			continue
		}
		source := step.Research
		sources = append(sources, watchSource{
			URL:         source.URL,
			RetrievedAt: rfc3339(source.RetrievedAt),
			MediaType:   source.MediaType,
			Bytes:       source.Bytes,
			Truncated:   source.Truncated,
			Redirects:   source.Redirects,
			Digest:      string(source.Digest),
		})
	}
	return sources
}

func viewCandidates(body run.Body) []watchCandidate {
	candidates := make([]watchCandidate, 0, len(body.Deferred)+len(body.Rejected))
	for _, candidate := range body.Deferred {
		candidates = append(candidates, viewCandidate(candidate, "deferred"))
	}
	for _, candidate := range body.Rejected {
		candidates = append(candidates, viewCandidate(candidate, "rejected"))
	}
	return candidates
}

func viewCandidate(candidate run.Candidate, disposition string) watchCandidate {
	return watchCandidate{
		ID:          candidate.ID,
		Reason:      candidate.Reason,
		At:          rfc3339(candidate.At),
		Disposition: disposition,
	}
}

func viewFailures(body run.Body) []watchFailure {
	failures := make([]watchFailure, 0, len(body.Failures))
	for _, failure := range body.Failures {
		failures = append(failures, watchFailure{
			Stage:   failure.Stage,
			Code:    failure.Code,
			Message: failure.Message,
			At:      rfc3339(failure.At),
		})
	}
	return failures
}

// watchLaunched is POST /api/watch/launch.
type watchLaunched struct {
	// RunID is absent because a child mints its own identity and reports it
	// in its receipt: this answer names the process, and the live strip names
	// the run once the run does.
	RunID string `json:"run_id,omitempty"`
	PID   int    `json:"pid"`
	Kind  string `json:"kind"`
	// StartedAt and LogPath are what make the launch followable from
	// somewhere other than this page.
	StartedAt string `json:"started_at"`
	LogPath   string `json:"log_path,omitempty"`
}

func (s *Server) handleWatchLaunch(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Launcher != nil, "starting runs") {
		return
	}
	// A launch is an attributed act: the run it starts spends the operator's
	// budget and records his authority, so a session that could not name one
	// refuses rather than starting work nobody is accountable for. It is the
	// same rule every §4.7 mutation on this surface follows.
	if s.opts.Operator == "" {
		s.writeError(w, http.StatusConflict,
			"this session cannot start runs because it could not name an operator; "+
				"relaunch with --operator ID or set $BABEL_OPERATOR")
		return
	}
	var req LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "the launch request could not be read")
		return
	}
	child, err := s.opts.Launcher.Launch(r.Context(), req)
	if err != nil {
		s.launchError(w, err)
		return
	}
	s.logf("watch: launched %s as pid %d for %s", child.Kind, child.PID, s.opts.Operator)
	s.writeJSON(w, http.StatusOK, watchLaunched{
		RunID:     child.RunID,
		PID:       child.PID,
		Kind:      child.Kind,
		StartedAt: rfc3339(child.StartedAt),
		LogPath:   child.LogPath,
	})
}

// launchError reports a refused launch with the refusing layer's own words.
//
// The CLI's refusals are the product here: "the conductor has no budget
// ceilings, so it will not run" is a stated boundary with a remedy, and
// replacing it with "conflict" would leave an operator with a button that does
// nothing and no way to find out why. So a ErrConflict's message is carried
// verbatim to the browser, which renders it as text.
func (s *Server) launchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadRequest), errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
		s.operationError(w, err)
	default:
		s.logf("watch: a launch failed")
		s.writeError(w, http.StatusInternalServerError, "the run could not be started")
	}
}

// watchStopRequest is POST /api/watch/stop.
type watchStopRequest struct {
	PID int `json:"pid"`
}

// watchStopped reports what the stop actually did. Stopped means the request
// was delivered, never that the process is gone: both mechanisms are honoured
// at the next safe point, which is what keeps a stop from discarding a cycle's
// committed work.
type watchStopped struct {
	Stopped bool   `json:"stopped"`
	PID     int    `json:"pid"`
	Method  string `json:"method"`
	Detail  string `json:"detail"`
}

func (s *Server) handleWatchStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Launcher != nil, "stopping runs") {
		return
	}
	var req watchStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "the stop request could not be read")
		return
	}
	if req.PID <= 0 {
		s.writeError(w, http.StatusBadRequest, "a pid is required")
		return
	}
	result, err := s.opts.Launcher.Stop(r.Context(), req.PID)
	if err != nil {
		s.launchError(w, err)
		return
	}
	s.logf("watch: asked pid %d to stop by %s", req.PID, result.Method)
	s.writeJSON(w, http.StatusOK, watchStopped{
		Stopped: true,
		PID:     req.PID,
		Method:  result.Method,
		Detail:  result.Detail,
	})
}

// handleWatchCeilings is GET /api/watch/ceilings: what this machine is allowed
// to spend on autonomous work, read from the document the CLI keeps it in.
//
// It is a launcher read rather than a settings read because the ceilings are
// the CLI's own file and the CLI is what enforces them. A second reader that
// parsed conductor.json here would be a second answer to "what is in force",
// and the two would disagree the first time a default changed.
func (s *Server) handleWatchCeilings(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Launcher != nil, "reading the spend ceilings") {
		return
	}
	settings, err := s.opts.Launcher.Ceilings(r.Context())
	if err != nil {
		// The path of a settings document and whatever a decoder said about
		// its contents stay out of both the answer and the log (§9): what a
		// reader can act on is that this machine's configuration could not be
		// read, and the terminal says the rest.
		s.logf("watch: the stored spend ceilings could not be read")
		s.writeError(w, http.StatusInternalServerError,
			"the stored spend ceilings could not be read on this machine; "+
				"\"babel conductor status\" reports the same document")
		return
	}
	s.writeJSON(w, http.StatusOK, settings)
}

// handleWatchConfigureCeilings is POST /api/watch/ceilings: the operator
// setting them.
//
// It is an attributed act for the launch's reason — the ceilings are what
// bounds every future autonomous run's spend, so a session that cannot name an
// operator does not get to widen them — and it answers with the stored
// document read back rather than with the request, because `conductor
// configure` is incremental and fills defaults: echoing the request would show
// the operator a configuration that is not the one in force.
func (s *Server) handleWatchConfigureCeilings(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Launcher != nil, "setting the spend ceilings") {
		return
	}
	if s.opts.Operator == "" {
		s.writeError(w, http.StatusConflict,
			"this session cannot set the spend ceilings because it could not name an operator; "+
				"relaunch with --operator ID or set $BABEL_OPERATOR")
		return
	}
	var req CeilingRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	stored, err := s.opts.Launcher.Configure(r.Context(), req)
	if err != nil {
		s.ceilingError(w, err)
		return
	}
	s.logf("watch: %s set the spend ceilings", s.opts.Operator)
	s.writeJSON(w, http.StatusOK, stored)
}

// ceilingError reports a refused configuration with the refusing layer's own
// words, on launchError's terms: "--per-cycle 9.00 is above --per-day 5.00,
// which would refuse every cycle" is the product, and a bare 400 would leave an
// operator with two numbers and no idea which one the machine objected to.
func (s *Server) ceilingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadRequest), errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
		s.operationError(w, err)
	default:
		s.logf("watch: the spend ceilings could not be stored")
		s.writeError(w, http.StatusInternalServerError, "the spend ceilings could not be stored")
	}
}

// runCounts mirrors a receipt's counts onto the listing shape this package
// already serves, so the Watch surface and the analysis page cannot disagree
// about what a run's numbers are called.
func runCounts(counts run.Counts) RunCounts {
	return RunCounts{
		ToolRequests: counts.ToolRequests,
		ToolsDenied:  counts.ToolsDenied,
		Retrieval:    counts.Retrieval,
		Deferred:     counts.Deferred,
		Rejected:     counts.Rejected,
		Failures:     counts.Failures,
		Redactions:   counts.Redactions,
	}
}

// runKind names the work a receipt records, from the only thing it says about
// why it ran: #96's authority reference.
//
// It is a derivation of a recorded value rather than a new field, and it is
// empty where the reference names nothing this build knows — a receipt written
// before authorities existed, or a duty a later build added. An empty kind is
// reported as absent, because "this run's kind is unrecorded" is true and
// "this run was an exploration" would be a guess.
func runKind(authority run.Authority) string {
	ref := authority.Ref
	switch {
	case ref == "":
		return ""
	case strings.HasPrefix(ref, "command:explore"), strings.HasPrefix(ref, "invitation:"),
		strings.HasPrefix(ref, "draw:"):
		return "explore"
	case strings.HasPrefix(ref, "evaluation:"), strings.HasPrefix(ref, "babel evaluate"):
		return "evaluate"
	case strings.HasPrefix(ref, "duty:"), strings.HasPrefix(ref, "consolidation:"):
		return "conductor"
	case strings.HasPrefix(ref, "command:prepare"):
		return "prepare"
	}
	return ""
}

// runCostUSD is what one receipt says the run spent, or nothing.
//
// The figure is the engine's own session accounting (worker.Usage.Cost) and
// never the profile's price schedule beside it: a rate is what a token would
// cost, and what an operator reading a spend column needs is what was actually
// billed. It is named for the wire's field rather than for the receipt,
// because internal/web already has a receiptCost — the evaluation surface's,
// which states what a review grant cost.
func runCostUSD(receipt run.Receipt) *float64 {
	worker := receipt.Body.Worker
	if worker == nil || worker.Usage == nil || worker.Usage.Cost == 0 {
		return nil
	}
	cost := worker.Usage.Cost
	return &cost
}

// receiptDuration is the run's wall clock, or nothing when the receipt records
// no timing.
func receiptDuration(receipt run.Receipt) *float64 {
	timing := receipt.Body.Timing
	if timing.StartedAt.IsZero() || timing.FinishedAt.IsZero() {
		return nil
	}
	seconds := timing.Duration().Seconds()
	return &seconds
}

// receiptTime is when the run this receipt describes was happening: its own
// start when it recorded one, and the time the receipt was filed otherwise.
//
// The instant is returned in whatever zone it was stored in. dayKey is the one
// place a zone becomes a bucket, so a second normalization here would be a
// second answer to the only question that matters about it.
func receiptTime(receipt run.Receipt) time.Time {
	if !receipt.Body.Timing.StartedAt.IsZero() {
		return receipt.Body.Timing.StartedAt
	}
	return receipt.Header.RecordedAt
}

// dayKey is the UTC date a timestamp falls on, which is the bucket every
// series here is counted in.
func dayKey(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// rfc3339 renders a timestamp for the wire, and renders the zero time as
// nothing: a page that received year one would render an in-flight run as
// having finished at the dawn of time.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
