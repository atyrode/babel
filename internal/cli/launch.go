package cli

// The web surface's supervised children: how a browser starts and stops the
// analysis this machine runs for itself (SPEC.md §8.4's withdrawn refusal,
// Contract W).
//
// The whole file is one idea: a launch from the browser is the same launch from
// the terminal, performed by the same binary. Nothing here reimplements an
// exploration, an evaluation or a cycle. It resolves this process's own
// executable, assembles the argv the CLI already parses, runs the same
// preflight the command would run, and starts it as a detached child whose
// stdout and stderr go to a file under the state directory. The run that
// results is receipted, authorized and attributed exactly as if it had been
// typed, because it was typed — by this process, with the operator the launch
// was started with.
//
// Three consequences follow, and they are why this is a file rather than a
// handler.
//
// The refusals are the CLI's, verbatim. A conductor with no ceilings, a machine
// with no Code executable and a deployment with no stored profile each already
// have a written explanation with a remedy, and those sentences are the product
// (internal/cli/conductorcmd.go says so about its own). So the preflight runs
// the command layer's own report functions against a captured stream and
// carries what they printed to the browser, rather than paraphrasing them into
// a second wording that would drift.
//
// A child outlives the page and may outlive the server. Setsid puts it in its
// own session, so closing the browser, ending the served session, or stopping
// `babel web` with Ctrl-C leaves a run that is minutes into a model job alone
// rather than killing it — which is the behaviour an operator watching a
// conductor loop from a laptop needs. That is also why the children are
// recorded in a small JSON file: a web server that was restarted has to be able
// to list and stop what the previous one started.
//
// A stop is graceful or it does not happen. `--stop-file` is the CLI's own way
// to interrupt at a safe point, and SIGTERM asks a loop to finish the cycle in
// flight; both leave every committed record durable. There is no SIGKILL path
// here at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	babelsync "github.com/atyrode/babel/internal/sync"
	"github.com/atyrode/babel/internal/web"
)

// launchDir holds what the browser started: one log per child and the record of
// which children exist. It is under the data directory rather than the cache
// because the record of a running process is not rebuildable — a cache wipe
// that lost it would leave a conductor loop nothing could stop.
func (d dirs) launchDir() string { return filepath.Join(d.data, "launch") }

// launchStateFile is the record of this machine's web-started children.
const launchStateFile = "children.json"

// launchStateSchema versions that record, so a future shape change is read as
// what it is instead of decoded optimistically into today's fields.
const launchStateSchema = 1

// The three kinds a browser may start. They are the three subcommands that
// perform analysis: one exploration, one review, or the loop. `prepare` is
// deliberately absent — a preparation is cheap, has no ceiling, and is the one
// step whose output a launch needs as an argument — and so is everything that
// writes to the archive.
const (
	launchExplore   = "explore"
	launchEvaluate  = "evaluate"
	launchConductor = "conductor"
)

// webLauncher starts and stops this machine's own analysis for the web
// surface.
//
// The operator is held rather than taken per call, on web.Options' terms: a
// launch is attributed to the identity the server was started with, and a
// field a request could set would be a request choosing whose budget it spends.
type webLauncher struct {
	app      *app
	dirs     dirs
	operator string
	// executable is this process's own binary, resolved once at construction.
	// A launch that re-resolved it per request could start a different build
	// from the one the operator is looking at.
	executable string

	mu       sync.Mutex
	children map[int]*launchedChild
}

// launchedChild is one child process, as both the live strip and the record
// under the state directory see it.
type launchedChild struct {
	PID       int       `json:"pid"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	LogPath   string    `json:"log_path"`
	// StopFile is the path passed to the child's own --stop-file, empty for a
	// kind that has no such flag. It is recorded rather than re-derived so a
	// restarted server stops the child by the path the child is watching.
	StopFile string `json:"stop_file,omitempty"`
}

// launchState is the JSON document under the state directory.
type launchState struct {
	Schema   int              `json:"schema"`
	Children []*launchedChild `json:"children"`
}

// newWebLauncher prepares the launcher, or reports why this machine cannot
// start runs from the browser.
//
// A machine whose own executable cannot be resolved is the one case that
// refuses: launching "babel" from $PATH instead would start whichever build
// happens to be installed, which is a different program from the one serving
// the page.
func (a *app) newWebLauncher(d dirs, operator string) (*webLauncher, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve this babel executable: %w", err)
	}
	if err := os.MkdirAll(d.launchDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create launch state directory: %w", err)
	}
	l := &webLauncher{
		app:        a,
		dirs:       d,
		operator:   operator,
		executable: executable,
		children:   map[int]*launchedChild{},
	}
	l.load()
	return l, nil
}

// load reads the children a previous server started.
//
// Nothing is tested for liveness here, and that is deliberate: the record is a
// claim about the past, and whether a pid is still a run of this executable is
// a question with one answer at the moment it matters — when the list is read,
// or when a stop is about to signal. Deciding it twice is how a list and a stop
// come to disagree about what is running.
func (l *webLauncher) load() {
	raw, err := os.ReadFile(l.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			l.app.diagf("warning: could not read which runs this machine started: %s\n",
				Sanitize(err.Error()))
		}
		return
	}
	var state launchState
	if err := json.Unmarshal(raw, &state); err != nil || state.Schema != launchStateSchema {
		l.app.diagf("warning: the record of web-started runs is not one this build reads, " +
			"so nothing it holds is listed or stoppable\n")
		return
	}
	for _, child := range state.Children {
		if child == nil || child.PID <= 0 {
			continue
		}
		l.children[child.PID] = child
	}
}

func (l *webLauncher) statePath() string {
	return filepath.Join(l.dirs.launchDir(), launchStateFile)
}

// ours reports whether a pid is still a process running this executable.
//
// The question it answers is the recycled-pid one. A pid that no longer exists
// is gone; a pid that exists may belong to something this server never started,
// and signalling that would be the feature stopping an innocent process. So
// where the command line is readable, the executable must appear in it.
//
// The whole command line is examined rather than only argv[0], because the
// kernel's own launch of an interpreted program puts the interpreter first and
// the program second. The match stays exact per element rather than becoming a
// substring test: a process merely mentioning the path in an argument is not
// this executable.
//
// Where /proc is unavailable the weaker existence probe is used, and the weaker
// answer is what bounds the guarantee: nothing here escalates past SIGTERM, so
// the worst a recycled pid can cost is a terminate signal to a process of the
// same user.
func (l *webLauncher) ours(pid int) bool {
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err == nil {
		return slices.Contains(strings.Split(string(cmdline), "\x00"), l.executable)
	}
	if !errors.Is(err, os.ErrNotExist) {
		// No /proc on this platform: fall back to the existence probe.
		return syscall.Kill(pid, 0) == nil
	}
	return false
}

// save writes the current children under the state directory.
//
// It is written whole and renamed into place, the way every other small
// document Babel keeps is: a server killed mid-write must find either the
// previous list or the new one, never half of one.
func (l *webLauncher) save() {
	state := launchState{Schema: launchStateSchema}
	for _, child := range l.children {
		state.Children = append(state.Children, child)
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		l.app.diagf("warning: could not record which runs this machine started: %s\n",
			Sanitize(err.Error()))
		return
	}
	temporary := l.statePath() + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		l.app.diagf("warning: could not record which runs this machine started: %s\n",
			Sanitize(err.Error()))
		return
	}
	if err := os.Rename(temporary, l.statePath()); err != nil {
		l.app.diagf("warning: could not record which runs this machine started: %s\n",
			Sanitize(err.Error()))
	}
}

// Launched lists the children still running, newest first.
//
// Each row is re-tested rather than trusted: a child this process started is
// reaped by its own goroutine, but one inherited from a previous server is not
// this process's child at all and can only be observed. A row that has exited
// is dropped here, which is what keeps a restarted server's list honest without
// a background sweeper.
func (l *webLauncher) Launched() []web.LaunchedRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	var runs []web.LaunchedRun
	changed := false
	for pid, child := range l.children {
		if !l.ours(pid) {
			delete(l.children, pid)
			changed = true
			continue
		}
		runs = append(runs, web.LaunchedRun{
			PID:       child.PID,
			Kind:      child.Kind,
			StartedAt: child.StartedAt,
			LogPath:   child.LogPath,
		})
	}
	if changed {
		l.save()
	}
	// Newest first, which is the order a live strip is read in: the thing
	// just started is the thing being watched. The pid breaks a tie so the
	// order is total, because two children started in the same instant must
	// not swap places between two reads of one page.
	slices.SortFunc(runs, func(a, b web.LaunchedRun) int {
		if !a.StartedAt.Equal(b.StartedAt) {
			return b.StartedAt.Compare(a.StartedAt)
		}
		return a.PID - b.PID
	})
	return runs
}

// Launch starts one run.
//
// The order is the contract: the request is validated into an argv, the CLI's
// own preflight is run, and only then is a process started. Validating after
// starting would leave a child that fails in a log file nobody is watching,
// which is exactly the experience §8.4's refusal was written about.
func (l *webLauncher) Launch(_ context.Context, req web.LaunchRequest) (web.LaunchedRun, error) {
	argv, stopFileFlag, err := launchArgv(req)
	if err != nil {
		return web.LaunchedRun{}, err
	}
	if err := l.preflight(req.Kind); err != nil {
		return web.LaunchedRun{}, err
	}

	started := time.Now().UTC()
	logPath := filepath.Join(l.dirs.launchDir(),
		fmt.Sprintf("%s-%s.log", req.Kind, started.Format("20060102-150405")))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return web.LaunchedRun{}, fmt.Errorf("open the run's log: %w", err)
	}
	defer logFile.Close()

	stopFile := ""
	if stopFileFlag {
		// The stop file is named for the run's start rather than for its pid,
		// because the argv has to be complete before the process exists.
		stopFile = filepath.Join(l.dirs.launchDir(),
			fmt.Sprintf("%s-%s.stop", req.Kind, started.Format("20060102-150405")))
		argv = append(argv, "--stop-file", stopFile)
	}

	cmd := exec.Command(l.executable, argv...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// The child gets no stdin at all: it is not attached to a terminal, and a
	// command that blocked on a prompt would hang with nobody to answer it.
	cmd.Stdin = nil
	// Its own session, so the browser's server can stop without taking a
	// model job with it, and so stopping the child can signal a group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The identity the server was started with, passed the way the CLI
	// already resolves one. Every attributed write the child performs is the
	// operator's, which is what makes a browser-started run as accountable as
	// a typed one.
	cmd.Env = append(os.Environ(), "BABEL_OPERATOR="+l.operator)
	if err := cmd.Start(); err != nil {
		return web.LaunchedRun{}, fmt.Errorf("start %s: %w", req.Kind, err)
	}

	child := &launchedChild{
		PID:       cmd.Process.Pid,
		Kind:      req.Kind,
		StartedAt: started,
		LogPath:   logPath,
		StopFile:  stopFile,
	}
	l.mu.Lock()
	l.children[child.PID] = child
	l.save()
	l.mu.Unlock()

	// Reaping is this process's own duty for as long as it lives: a child
	// nobody waits on stays a zombie in the table the live strip reads from.
	// The wait also removes the row, so a run that ended leaves the strip
	// without anything having to poll.
	go l.reap(cmd, child)

	return web.LaunchedRun{
		PID:       child.PID,
		Kind:      child.Kind,
		StartedAt: child.StartedAt,
		LogPath:   child.LogPath,
	}, nil
}

// reap waits for one child and forgets it.
//
// The exit status is reported to the diagnostics stream rather than swallowed,
// because a run that refused to start says why in its log and an operator
// reading the server's own stream should be told there is a log to read. The
// stop file is removed with the child: leaving it behind would stop the next
// run of the same kind before it began.
func (l *webLauncher) reap(cmd *exec.Cmd, child *launchedChild) {
	err := cmd.Wait()
	l.mu.Lock()
	delete(l.children, child.PID)
	l.save()
	l.mu.Unlock()
	if child.StopFile != "" {
		_ = os.Remove(child.StopFile)
	}
	if err != nil {
		l.app.diagf("note: the %s started from the browser ended with %s; its output is in %s\n",
			child.Kind, Sanitize(err.Error()), Sanitize(child.LogPath))
		return
	}
	l.app.diagf("note: the %s started from the browser finished; its output is in %s\n",
		child.Kind, Sanitize(child.LogPath))
}

// Stop asks one child to stop at a safe point.
//
// A pid this server did not start is refused rather than signalled. It is the
// one refusal that matters most here: a stop route that signalled arbitrary
// pids would be a loopback HTTP surface with a kill primitive, and the fact
// that the requests are authenticated is not a reason to hold one.
//
// A pid that is recorded and no longer running this executable is refused for
// the same reason and reported as ended rather than as unknown, because those
// are different answers to the operator who just pressed the button: one means
// the run finished, the other means this server never had it.
func (l *webLauncher) Stop(_ context.Context, pid int) (web.StopResult, error) {
	l.mu.Lock()
	child, known := l.children[pid]
	ended := known && !l.ours(pid)
	if ended {
		delete(l.children, pid)
		l.save()
	}
	l.mu.Unlock()
	switch {
	case ended:
		return web.StopResult{}, fmt.Errorf("%w: the run on process %d has already ended",
			web.ErrNotFound, pid)
	case !known:
		return web.StopResult{}, fmt.Errorf("%w: this server did not start the process %d, "+
			"so it will not signal it", web.ErrNotFound, pid)
	}
	if child.StopFile != "" {
		// The CLI's own mechanism: the command notices the file and stops
		// where stopping is safe. It is written empty because its existence
		// is the whole message.
		if err := os.WriteFile(child.StopFile, nil, 0o600); err != nil {
			return web.StopResult{}, fmt.Errorf("write the stop file for %d: %w", pid, err)
		}
		return web.StopResult{
			Method: "stop-file",
			Detail: fmt.Sprintf("the %s was asked to stop at its next safe point; "+
				"the work in flight finishes and is receipted", child.Kind),
		}, nil
	}
	// Nothing to watch a file: SIGTERM the child's own process group, which
	// is what setsid made it the leader of. A group signal is what reaches a
	// worker the command launched, and it is the same signal the command
	// handles when an operator presses Ctrl-C.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return web.StopResult{}, fmt.Errorf("signal the process group of %d: %w", pid, err)
	}
	return web.StopResult{
		Method: "signal",
		Detail: fmt.Sprintf("the %s was sent the same interrupt Ctrl-C sends; "+
			"everything it committed stays durable", child.Kind),
	}, nil
}

// launchArgv turns one request into the argv the CLI parses, and refuses an
// argument the named kind has no flag for.
//
// Refusing rather than ignoring is the point. A page that sent `--until` to an
// exploration would be asking for a bounded run and getting an unbounded one,
// and the only way it could find out is by watching the run not stop.
func launchArgv(req web.LaunchRequest) (argv []string, stopFile bool, err error) {
	args := req.Args
	switch req.Kind {
	case launchExplore:
		if err := refuseArgs(req.Kind, map[string]bool{
			"until":       args.Until != "",
			"concurrent":  args.Concurrent != nil,
			"consolidate": args.Consolidate != nil,
			"evaluate":    args.Evaluate != nil,
			"once":        args.Once,
			"correct":     args.Correct != "",
		}); err != nil {
			return nil, false, err
		}
		if args.Preparation == "" {
			return nil, false, fmt.Errorf("%w: explore requires a preparation: "+
				"run \"babel prepare\" to fix a corpus scope, then name it here",
				web.ErrBadRequest)
		}
		argv = []string{"explore", "--preparation", args.Preparation, "--json"}
		for _, recipe := range args.Recipe {
			argv = append(argv, "--recipe", recipe)
		}
		argv = appendCount(argv, "--develop", args.Develop)
		argv = appendCount(argv, "--retrievals", args.Retrievals)
		argv = appendCount(argv, "--fetches", args.Fetches)
		argv = appendFlag(argv, "--challenge", args.Challenge)
		argv = appendFlag(argv, "--synthesize", args.Synthesize)
		return argv, true, nil
	case launchEvaluate:
		if err := refuseArgs(req.Kind, map[string]bool{
			"preparation": args.Preparation != "",
			"recipe":      len(args.Recipe) > 0,
			"develop":     args.Develop != nil,
			"until":       args.Until != "",
			"concurrent":  args.Concurrent != nil,
			"consolidate": args.Consolidate != nil,
			"evaluate":    args.Evaluate != nil,
			"once":        args.Once,
			"challenge":   args.Challenge,
			"synthesize":  args.Synthesize,
		}); err != nil {
			return nil, false, err
		}
		argv = []string{"evaluate", "--json"}
		if args.Correct != "" {
			argv = append(argv, "--correct", args.Correct)
		}
		argv = appendCount(argv, "--retrievals", args.Retrievals)
		argv = appendCount(argv, "--fetches", args.Fetches)
		// `babel evaluate` draws one review and stops, so it has no
		// stop-file flag: there is no next safe point to stop at.
		return argv, false, nil
	case launchConductor:
		if err := refuseArgs(req.Kind, map[string]bool{
			"preparation": args.Preparation != "",
			"recipe":      len(args.Recipe) > 0,
			"develop":     args.Develop != nil,
			"retrievals":  args.Retrievals != nil,
			"fetches":     args.Fetches != nil,
			"correct":     args.Correct != "",
		}); err != nil {
			return nil, false, err
		}
		argv = []string{"conductor", "run", "--json"}
		if args.Until != "" {
			argv = append(argv, "--until", args.Until)
		}
		argv = appendCount(argv, "--concurrent", args.Concurrent)
		argv = appendCount(argv, "--consolidate", args.Consolidate)
		argv = appendCount(argv, "--evaluate", args.Evaluate)
		argv = appendFlag(argv, "--once", args.Once)
		argv = appendFlag(argv, "--challenge", args.Challenge)
		argv = appendFlag(argv, "--synthesize", args.Synthesize)
		return argv, true, nil
	}
	return nil, false, fmt.Errorf("%w: %q is not something this machine can start; "+
		"the kinds are explore, evaluate and conductor", web.ErrBadRequest, Sanitize(req.Kind))
}

// refuseArgs reports the first argument the kind has no flag for. The map is
// "was it named", so a zero a caller deliberately sent still counts as named.
func refuseArgs(kind string, named map[string]bool) error {
	for _, key := range sortedNames(named) {
		if named[key] {
			return fmt.Errorf("%w: %s takes no %s", web.ErrBadRequest, kind, key)
		}
	}
	return nil
}

// sortedNames keeps the refusal deterministic: a request naming two impossible
// arguments must always be told about the same one, or the same mistake reads
// as two different bugs.
func sortedNames(named map[string]bool) []string {
	return slices.Sorted(maps.Keys(named))
}

// appendCount names a numeric flag exactly when the request named it, so
// "--consolidate 0" reaches the command and an unnamed one does not.
func appendCount(argv []string, flag string, value *int) []string {
	if value == nil {
		return argv
	}
	return append(argv, flag, strconv.Itoa(*value))
}

func appendFlag(argv []string, flag string, set bool) []string {
	if !set {
		return argv
	}
	return append(argv, flag)
}

// preflight runs the refusals the named command runs before it starts work, so
// a browser meets them as an answer rather than as a log file.
//
// It is the same order each command applies: the ceilings the loop and a review
// spend against, the operator authorization a review needs, then the Code
// executable and the stored profile every one of them needs. Each refusal's
// text is the command's own.
func (l *webLauncher) preflight(kind string) error {
	if kind == launchConductor || kind == launchEvaluate {
		settings, err := loadConductorSettings()
		if err != nil {
			return err
		}
		if settings.Ceilings == nil {
			return refusedLaunch{text: l.app.refusalText((*app).reportUnconfiguredConductor)}
		}
		if kind == launchEvaluate && !settings.BabelTriagesTheQueue {
			// The authorization is the operator's and no surface stands in
			// for it: reviewing is Babel forming attributed judgements about
			// records the operator has not ruled on, which is the act the
			// toggle exists to consent to.
			return refusedLaunch{text: fmt.Sprintf("review work is not authorized on this "+
				"machine, so Babel will not form judgements about records you have not "+
				"ruled on. Authorize it with \"babel conductor configure --%s\"",
				conductor.DutyTriagesTheQueue)}
		}
	}
	analysis, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	if _, ok := (&workerFlags{}).resolve(analysis); !ok {
		return refusedLaunch{text: l.app.refusalText((*app).reportNoWorker)}
	}
	if analysis.Profile == nil {
		return refusedLaunch{text: "no Code analysis profile is stored, and a run started " +
			"from the browser never configures one: run \"babel analysis profile configure\" first"}
	}
	return nil
}

// refusedLaunch carries a refusal to the browser with the refusing layer's own
// words.
//
// It reports itself as web.ErrConflict, which is what makes the HTTP status
// right without the text having to mention HTTP: the request was well formed
// and this machine's configuration is what refused it. The text is flattened to
// one line because the response's sanitizer escapes control characters, and a
// paragraph arriving as \u{A} sequences would be less readable than the
// sentence it came from.
type refusedLaunch struct{ text string }

func (r refusedLaunch) Error() string { return oneLine(r.text) }

func (r refusedLaunch) Is(target error) bool { return target == web.ErrConflict }

// oneLine collapses a printed refusal into a single line, keeping the words and
// the remedy command exactly as the command layer wrote them.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// refusalText runs one of the command layer's own refusals against a captured
// stream and returns what it printed.
//
// Capturing beats copying. These messages are several sentences with a remedy
// in them, they are revised as the product is, and a second copy here would be
// the browser telling an operator something the terminal stopped saying months
// ago. The copy of the app is shallow on purpose: every handle stays the same
// and only the diagnostic stream is replaced.
func (a *app) refusalText(report func(*app) error) string {
	var captured bytes.Buffer
	quiet := *a
	quiet.stderr = &captured
	quiet.stdout = &captured
	_ = report(&quiet)
	return captured.String()
}

// webDrain records what the drainer this launch owns most recently achieved, so
// the Watch surface can say whether this disk is still the only place its
// analysis exists.
//
// It is a recorder rather than a reader of the journal because the two answer
// different questions: the journal says where one record stands, and an attempt
// is an event with an outcome. Only the drainer sees the attempt, so this
// observes the same report the diagnostics stream prints (internal/cli/sync.go
// reportDrain) and keeps the last one.
type webDrain struct {
	mu       sync.Mutex
	observed bool
	report   web.DrainReport
}

// observe records one attempt. Every report is kept, including the silent ones:
// reportDrain says nothing when nothing moved, and "the last attempt published
// nothing" is exactly what an operator watching a pending backlog needs to see.
func (d *webDrain) observe(rep babelsync.Report) {
	total := 0
	for _, count := range rep.Committed {
		total += count
	}
	pending := 0
	for _, count := range rep.Pending {
		pending += count
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observed = true
	d.report = web.DrainReport{
		At:        time.Now().UTC(),
		Published: total,
		Sealed:    len(rep.Sealed),
		Pending:   pending,
	}
}

// LastDrain reports the last attempt, and reports that there has not been one
// rather than an empty attempt.
func (d *webDrain) LastDrain() (web.DrainReport, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report, d.observed
}

// receipts is the run receipts' own store, which the Watch surface's run page
// and the record page's machinery peel read whole.
//
// It is its own accessor beside runs() for the reason every accessor in this
// file is separate: internal/web takes them as two fields, because a listing
// reads §9's plaintext half of a header and this reads the body. The nil test
// reaches inside the analysis state as well as at it, on references' terms.
func (s *webServices) receipts() web.RunReceiptReader {
	if s.analysis == nil || s.analysis.runs == nil {
		return nil
	}
	return s.analysis.runs
}
