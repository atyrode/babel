package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/web"
)

// webLaunchFixture is one machine the browser may start runs on: an isolated
// XDG tree, a fake executable standing in for this build's own binary, and the
// launcher under test.
//
// The fake executable is the whole point of the shape. A real launch runs
// os.Executable(), which under `go test` is the test binary — so a test that
// used it would run the test suite as a child. Substituting the field is what
// lets the argv, the log, the stop file and the signal be observed while
// everything around them stays the production path.
type webLaunchFixture struct {
	app        *app
	launcher   *webLauncher
	dirs       dirs
	executable string
	// argvFile is where the fake executable records the arguments it was
	// given, so a test can assert the CLI invocation a request became.
	argvFile string
	// signalFile is where it records a caught SIGTERM. It exists only if the
	// child ran its own handler, which is what proves the stop was graceful:
	// a SIGKILL cannot be caught, so this file cannot appear after one.
	signalFile string
	stderr     *bytes.Buffer
}

// newWebLaunchFixture prepares a machine with no analysis configuration at all,
// which is the state every refusal is asserted against.
func newWebLaunchFixture(t *testing.T) *webLaunchFixture {
	t.Helper()
	newFixture(t)
	d, err := babelDirs()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &webLaunchFixture{
		dirs:       d,
		executable: filepath.Join(root, "fake-babel"),
		argvFile:   filepath.Join(root, "argv"),
		signalFile: filepath.Join(root, "terminated"),
		stderr:     &bytes.Buffer{},
	}
	// A child that records its argv, catches the interrupt a graceful stop
	// sends, and otherwise waits for either that signal or its stop file.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + f.argvFile + "\n" +
		"trap 'printf term > " + f.signalFile + "; exit 0' TERM\n" +
		"i=0\n" +
		"while [ $i -lt 200 ]; do\n" +
		"  for a in \"$@\"; do case \"$a\" in *.stop) [ -f \"$a\" ] && exit 0;; esac; done\n" +
		"  sleep 0.05\n" +
		"  i=$((i+1))\n" +
		"done\n"
	if err := os.WriteFile(f.executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.app = &app{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: f.stderr}
	launcher, err := f.app.newWebLauncher(d, "alex")
	if err != nil {
		t.Fatal(err)
	}
	launcher.executable = f.executable
	f.launcher = launcher
	return f
}

// configure puts the machine in the state a launch needs: ceilings, review
// authorization, a worker and a stored profile. It writes the documents through
// the same savers the commands use, so a launch reads what a configured machine
// holds rather than a shape this test invented.
func (f *webLaunchFixture) configure(t *testing.T) {
	t.Helper()
	if _, err := saveConductorSettings(conductorSettings{
		Ceilings:             &ceilingRecord{PerCycle: 0.5, PerDay: 5, Currency: "USD"},
		BabelTriagesTheQueue: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAnalysisSettings(analysisSettings{
		Worker:  f.executable,
		Profile: &profileRecord{ID: "p-1", Revision: 2},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWebLaunchRefusesWithoutCeilings is the boundary the operator set: a
// browser cannot start the loop on a machine that has no budget, and what it
// gets back is the conductor command's own explanation rather than a status
// code.
func TestWebLaunchRefusesWithoutCeilings(t *testing.T) {
	f := newWebLaunchFixture(t)

	_, err := f.launcher.Launch(context.Background(), web.LaunchRequest{Kind: launchConductor})
	if err == nil {
		t.Fatal("a conductor launch succeeded on a machine with no ceilings")
	}
	if !errors.Is(err, web.ErrConflict) {
		t.Errorf("the refusal is %v, which the surface cannot render as a refused request", err)
	}
	for _, want := range []string{
		"has no budget ceilings",
		"babel conductor configure --per-cycle 0.50 --per-day 5.00",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry the command's own words %q:\n%s", want, err.Error())
		}
	}
	// Nothing was started, and nothing is listed. A refused launch that had
	// already forked would leave a row the operator could stop but never see
	// the output of.
	if runs := f.launcher.Launched(); len(runs) != 0 {
		t.Errorf("a refused launch left %d children", len(runs))
	}
	if _, err := os.Stat(f.argvFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused launch ran the executable anyway")
	}
}

// TestWebLaunchRefusesWithoutAWorker covers the other configuration a launch
// cannot stand in for: exploration happens inside Code's engine, and a machine
// with none says so in the words `babel explore` already uses.
func TestWebLaunchRefusesWithoutAWorker(t *testing.T) {
	f := newWebLaunchFixture(t)

	_, err := f.launcher.Launch(context.Background(), web.LaunchRequest{
		Kind: launchExplore,
		Args: web.LaunchArgs{Preparation: "prep_1"},
	})
	if err == nil {
		t.Fatal("an exploration launched on a machine with no Code executable")
	}
	if !errors.Is(err, web.ErrConflict) {
		t.Errorf("the refusal is %v, which the surface cannot render as a refused request", err)
	}
	if !strings.Contains(err.Error(), "no Code executable is configured") {
		t.Errorf("the refusal does not carry the command's own words:\n%s", err.Error())
	}
}

// TestWebLaunchRefusesAnExplorationWithNoScope keeps the one argument a launch
// cannot invent. A preparation is what makes what a run read explicit, and a
// browser that omitted it is told where one comes from.
func TestWebLaunchRefusesAnExplorationWithNoScope(t *testing.T) {
	f := newWebLaunchFixture(t)
	f.configure(t)

	_, err := f.launcher.Launch(context.Background(), web.LaunchRequest{Kind: launchExplore})
	if !errors.Is(err, web.ErrBadRequest) {
		t.Fatalf("an exploration with no preparation failed with %v, want a bad request", err)
	}
	if !strings.Contains(err.Error(), "babel prepare") {
		t.Errorf("the refusal does not say where a preparation comes from:\n%s", err.Error())
	}
}

// TestWebLaunchRefusesAFlagTheKindDoesNotHave is why the argv is assembled here
// rather than forwarded. `babel explore` has no --until, so a page that sent one
// would be asking for a bounded run and getting an unbounded one; the refusal is
// how it finds out at the moment it asks.
func TestWebLaunchRefusesAFlagTheKindDoesNotHave(t *testing.T) {
	f := newWebLaunchFixture(t)
	f.configure(t)

	_, err := f.launcher.Launch(context.Background(), web.LaunchRequest{
		Kind: launchExplore,
		Args: web.LaunchArgs{Preparation: "prep_1", Until: "60m"},
	})
	if !errors.Is(err, web.ErrBadRequest) {
		t.Fatalf("an exploration with --until failed with %v, want a bad request", err)
	}
	if !strings.Contains(err.Error(), "until") {
		t.Errorf("the refusal does not name the argument it refused:\n%s", err.Error())
	}
}

// TestWebLaunchRunsTheCLIsOwnInvocation is the file's central claim: a browser
// launch is a typed command. What reaches the child is the subcommand and the
// flags the CLI parses, with the operator the server was started with.
func TestWebLaunchRunsTheCLIsOwnInvocation(t *testing.T) {
	f := newWebLaunchFixture(t)
	f.configure(t)

	concurrent, evaluate := 3, 2
	child, err := f.launcher.Launch(context.Background(), web.LaunchRequest{
		Kind: launchConductor,
		Args: web.LaunchArgs{
			Until:      "60m",
			Concurrent: &concurrent,
			Evaluate:   &evaluate,
			Challenge:  true,
			Synthesize: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.launcher.Stop(context.Background(), child.PID) })
	if child.PID <= 0 {
		t.Fatalf("the launch reported pid %d", child.PID)
	}

	argv := f.waitForFile(t, f.argvFile)
	got := strings.Fields(argv)
	want := []string{
		"conductor", "run", "--json", "--until", "60m",
		"--concurrent", "3", "--evaluate", "2", "--challenge", "--synthesize",
	}
	for i, arg := range want {
		if i >= len(got) || got[i] != arg {
			t.Fatalf("the child was launched as %v, want it to begin %v", got, want)
		}
	}
	// The stop file is the last argument because the launcher appends it, and
	// it has to be a path the child watches rather than a name it invents.
	if len(got) < 2 || got[len(got)-2] != "--stop-file" {
		t.Fatalf("the child was launched without a stop file: %v", got)
	}

	// The run is listed while it lives, with the pid a stop needs.
	runs := f.launcher.Launched()
	if len(runs) != 1 || runs[0].PID != child.PID || runs[0].Kind != launchConductor {
		t.Fatalf("the live list is %+v, want the one conductor that was started", runs)
	}
	if runs[0].LogPath == "" {
		t.Error("the child's output is not being written anywhere an operator could read")
	}
}

// TestWebStopIsGraceful is the guarantee that makes a stop button safe to
// press: the kinds that have a stop file are asked to stop at their next safe
// point, the one that has none is sent the same interrupt Ctrl-C sends, and
// nothing is ever killed.
func TestWebStopIsGraceful(t *testing.T) {
	t.Run("a stop file for a kind that watches one", func(t *testing.T) {
		f := newWebLaunchFixture(t)
		f.configure(t)
		child, err := f.launcher.Launch(context.Background(), web.LaunchRequest{
			Kind: launchConductor,
			Args: web.LaunchArgs{Once: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.waitForFile(t, f.argvFile)

		result, err := f.launcher.Stop(context.Background(), child.PID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Method != "stop-file" {
			t.Errorf("the stop used %q, want the CLI's own stop file", result.Method)
		}
		if !strings.Contains(result.Detail, "safe point") {
			t.Errorf("the stop does not say when it takes effect: %q", result.Detail)
		}
		// The child exits because it saw the file, so the signal handler
		// never ran. A stop that killed it could not leave this absent.
		f.waitGone(t, child.PID)
		if _, err := os.Stat(f.signalFile); !errors.Is(err, os.ErrNotExist) {
			t.Error("the child was signalled as well as asked, which is not what a stop file is for")
		}
	})

	t.Run("the interrupt Ctrl-C sends for a kind that has no stop file", func(t *testing.T) {
		f := newWebLaunchFixture(t)
		f.configure(t)
		child, err := f.launcher.Launch(context.Background(), web.LaunchRequest{Kind: launchEvaluate})
		if err != nil {
			t.Fatal(err)
		}
		f.waitForFile(t, f.argvFile)

		result, err := f.launcher.Stop(context.Background(), child.PID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Method != "signal" {
			t.Errorf("the stop used %q, want the interrupt", result.Method)
		}
		// The child caught it and ran its own handler, which is only
		// possible for a catchable signal: SIGKILL would leave this file
		// absent and the exit uncontrolled.
		f.waitForFile(t, f.signalFile)
		f.waitGone(t, child.PID)
	})
}

// TestWebStopRefusesAPidItDidNotStart is the boundary that keeps an
// authenticated loopback surface from being a signal primitive.
func TestWebStopRefusesAPidItDidNotStart(t *testing.T) {
	f := newWebLaunchFixture(t)

	_, err := f.launcher.Stop(context.Background(), os.Getpid())
	if !errors.Is(err, web.ErrNotFound) {
		t.Fatalf("stopping an unknown pid failed with %v, want a not-found refusal", err)
	}
	if !strings.Contains(err.Error(), "did not start") {
		t.Errorf("the refusal does not say why it refused:\n%s", err.Error())
	}
}

// TestWebLaunchSurvivesARestartedServer is why the children are recorded on
// disk: a web server that was restarted while a loop ran must still be able to
// list it and stop it, or the only way to end the run is to find the pid by
// hand.
func TestWebLaunchSurvivesARestartedServer(t *testing.T) {
	f := newWebLaunchFixture(t)
	f.configure(t)
	child, err := f.launcher.Launch(context.Background(), web.LaunchRequest{Kind: launchConductor})
	if err != nil {
		t.Fatal(err)
	}
	f.waitForFile(t, f.argvFile)

	// A second launcher over the same state directory is what the next
	// server holds. It reads the record rather than the process table, and
	// it accepts the child because the pid is still running this executable.
	next, err := f.app.newWebLauncher(f.dirs, "alex")
	if err != nil {
		t.Fatal(err)
	}
	next.executable = f.executable
	runs := next.Launched()
	if len(runs) != 1 || runs[0].PID != child.PID {
		t.Fatalf("a restarted server lists %+v, want the child the previous one started", runs)
	}
	if _, err := next.Stop(context.Background(), child.PID); err != nil {
		t.Fatalf("a restarted server could not stop what it inherited: %v", err)
	}
	f.waitGone(t, child.PID)
}

// waitForFile waits for the child to write one of its markers and returns what
// it wrote. The child is a process, so every assertion about it is a wait.
func (f *webLaunchFixture) waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(path); err == nil {
			return string(content)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the child never wrote %s\nserver diagnostics: %s", path, f.stderr.String())
	return ""
}

// waitGone waits for the launcher to stop listing a pid, which is how a stopped
// child is observed: the reaper removes the row when the process ends.
func (f *webLaunchFixture) waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		listed := false
		for _, run := range f.launcher.Launched() {
			if run.PID == pid {
				listed = true
			}
		}
		if !listed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d is still listed after a stop\nserver diagnostics: %s", pid, f.stderr.String())
}
