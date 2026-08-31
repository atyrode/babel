package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestRunConformanceFailsANonWorker points the suite at two programs that are
// not workers: one that exits at once without writing a byte, and one that
// holds the pipes open and never speaks. The command's whole purpose is grading
// an implementation that does not work yet, so a candidate that is not a worker
// at all has to produce a full report with reasons rather than a crash, a
// truncated run, or a silent pass.
//
// Every obligation must fail, with no exemptions. That is the point of this
// test and it is worth more than any single obligation: an obligation that
// grades only an absence is satisfied by a program that produces nothing, so
// "nothing happened" reads as a pass. run/no-credential-leak was exactly that
// for a while — a silent binary scored 1 of 11 on a credential guarantee it had
// never been exposed to, and run/reports-resources would be the same shape if
// it graded the absence of a resource report without first establishing that
// the run reached a result. Any obligation reintroducing that shape fails
// here.
func TestRunConformanceFailsANonWorker(t *testing.T) {
	candidates := map[string]conformanceTarget{
		// Exits 0 immediately, no bytes on either stream.
		"silent": {binary: buildSilentBinary(t)},
		// Speaks never but stays alive, so every obligation spends its whole
		// handshake budget waiting. The obligations are independent processes,
		// so they are graded concurrently here: run serially this candidate
		// costs the handshake timeout once per obligation for no extra evidence.
		"never handshakes": {binary: fakeWorkerPath, args: []string{"-no-hello"}},
	}

	for name, target := range candidates {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			results := gradeConcurrently(target)
			if len(results) != len(conformanceObligations(conformanceTarget{})) {
				t.Fatalf("reported %d obligations, want the whole suite", len(results))
			}
			for _, result := range results {
				if result.Passed {
					t.Errorf("obligation %s passed against a program that never speaks the protocol; an obligation that grades an absence must first establish that the run happened", result.Name)
				}
				if len(result.Failures) == 0 {
					t.Errorf("obligation %s failed without saying why", result.Name)
				}
				for _, failure := range result.Failures {
					if strings.Contains(failure, conformanceToken) {
						t.Errorf("obligation %s leaked the broker credential into its report", result.Name)
					}
				}
			}
		})
	}
}

// gradeConcurrently grades every obligation against one target at the same
// time. RunConformance is deliberately serial — an operator reads a report in
// order, and one worker process per obligation at once would distort the timing
// obligations — but each obligation launches its own process and shares nothing,
// so a test that only needs the verdicts can afford the parallelism.
func gradeConcurrently(target conformanceTarget) []ObligationResult {
	obligations := conformanceObligations(target)
	results := make([]ObligationResult, len(obligations))
	var wg sync.WaitGroup
	wg.Add(len(obligations))
	for i, obligation := range obligations {
		go func() {
			defer wg.Done()
			results[i] = runObligation(obligation)
		}()
	}
	wg.Wait()
	return results
}

// TestRunConformanceLaunchesTheWorkerWithItsArguments checks that the suite can
// grade a worker that does not speak the protocol at argv[0]. Code is an
// interactive program that speaks it under a subcommand, so a suite that could
// only launch a bare executable would grade a wrapper script instead of the
// implementation.
func TestRunConformanceLaunchesTheWorkerWithItsArguments(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stdin.jsonl")

	for _, result := range RunConformance(context.Background(), fakeWorkerPath, "-record", record) {
		if !result.Passed {
			t.Errorf("obligation %s failed: %q", result.Name, result.Failures)
		}
	}

	// The fixture only writes this file when it is given the argument, so
	// its content is proof the argument reached the child process.
	info, err := os.Stat(record)
	if err != nil {
		t.Fatalf("the worker was launched without its arguments: %v", err)
	}
	if info.Size() == 0 {
		t.Error("the worker recorded nothing; it was launched but never spoken to")
	}
}

// TestObligationOutcomes covers what one obligation's verdict is made of: a
// Fatal ends that obligation and nothing else, a panic inside it is a failure
// rather than a dead command, and a clean run reports no messages at all.
func TestObligationOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		run        func(t conformanceT)
		wantPassed bool
		wantFirst  string
	}{
		{
			name:       "clean",
			run:        func(conformanceT) {},
			wantPassed: true,
		},
		{
			name:      "error keeps going",
			run:       func(t conformanceT) { t.Error("first"); t.Errorf("%s", "second") },
			wantFirst: "first",
		},
		{
			name:      "fatal aborts",
			run:       func(t conformanceT) { t.Fatal("stop here"); t.Error("unreachable") },
			wantFirst: "stop here",
		},
		{
			name:      "panic is a verdict",
			run:       func(conformanceT) { panic("worker client defect") },
			wantFirst: "panic: worker client defect",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A deferred cleanup is how an obligation releases its
			// child process, so Fatal must not skip it.
			cleaned := false
			run := test.run
			result := runObligation(conformanceObligation{
				name: test.name,
				run: func(ct conformanceT) {
					defer func() { cleaned = true }()
					run(ct)
				},
			})

			if !cleaned {
				t.Error("the obligation's deferred cleanup did not run")
			}
			if result.Passed != test.wantPassed {
				t.Errorf("passed = %v, want %v (%q)", result.Passed, test.wantPassed, result.Failures)
			}
			if result.Name != test.name {
				t.Errorf("name = %q, want %q", result.Name, test.name)
			}
			if test.wantPassed {
				if len(result.Failures) != 0 {
					t.Errorf("a passing obligation reported %q", result.Failures)
				}
				return
			}
			if len(result.Failures) == 0 {
				t.Fatal("a failing obligation reported nothing")
			}
			if result.Failures[0] != test.wantFirst {
				t.Errorf("first message = %q, want %q", result.Failures[0], test.wantFirst)
			}
			if test.name == "fatal aborts" && len(result.Failures) != 1 {
				t.Errorf("Fatal did not abort the obligation: %q", result.Failures)
			}
		})
	}
}

// buildSilentBinary compiles a program that exits at once, which is the
// cheapest thing that is executable and is not a worker. It is built rather
// than committed for the same reason the fake worker is: the fixtures stay
// hermetic and nothing prebuilt can go stale.
func buildSilentBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "silent.go")
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture source: %v", err)
	}
	binary := filepath.Join(dir, "silent")
	build := exec.Command("go", "build", "-o", binary, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the fixture: %v\n%s", err, out)
	}
	return binary
}

// TestUnsandboxedGradingIsolatesTheContainmentFinding is the difference between
// two findings that need different work: an implementation that has not built a
// sandbox yet, and one that does not speak the protocol.
//
// A worker declaring weak containment is refused at launch, so under the strict
// default every obligation that reaches worker mode fails with the same
// containment error and none of them says anything about the protocol. Relaxing
// the requirement lets the rest of the contract be graded — while
// run/declares-containment still fails, because checking the sandbox is that
// obligation's entire purpose and relaxing the run must not relax it.
func TestUnsandboxedGradingIsolatesTheContainmentFinding(t *testing.T) {
	weak := func(unsandboxed bool) map[string]bool {
		t.Helper()
		outcome := map[string]bool{}
		for _, result := range RunConformanceWith(context.Background(), ConformanceOptions{
			Worker:      fakeWorkerPath,
			Args:        []string{"-containment", "weak"},
			Unsandboxed: unsandboxed,
		}) {
			outcome[result.Name] = result.Passed
		}
		return outcome
	}

	strict := weak(false)
	relaxed := weak(true)

	// Under the strict default the containment refusal swamps the report: a
	// worker-mode obligation cannot even start.
	for _, name := range []string{"run/well-behaved", "run/tool-allow", "run/cancellation"} {
		if strict[name] {
			t.Errorf("%s passed under the strict requirement; a weak declaration must be refused at launch", name)
		}
		if !relaxed[name] {
			t.Errorf("%s failed under relaxed grading; the protocol obligations should be reachable once containment is not the gate", name)
		}
	}

	// The one obligation that must never be relaxed.
	if relaxed["run/declares-containment"] {
		t.Error("run/declares-containment passed under relaxed grading; the obligation exists to check the sandbox, so relaxing the run must not relax it")
	}

	// The handshake never depended on containment either way.
	for _, name := range []string{"handshake/accept", "handshake/refuse"} {
		if !strict[name] || !relaxed[name] {
			t.Errorf("%s should not depend on the containment requirement (strict=%v relaxed=%v)", name, strict[name], relaxed[name])
		}
	}
}

// TestStreamConformanceDeliversEachVerdictBeforeTheNextObligation is the whole
// point of streaming: a verdict that arrives only once the suite is finished
// tells an operator nothing while the suite is running, which is the situation
// this exists to fix (issue #78).
//
// The obligations are synthetic because what is being graded is the loop's
// delivery discipline, not the worker's: a real candidate's timing is its own,
// and the cheapest one that stalls costs a 15-second handshake budget per
// obligation to watch. Each obligation announces that it started, so the
// recorded sequence proves delivery happened between obligations rather than
// after all of them.
func TestStreamConformanceDeliversEachVerdictBeforeTheNextObligation(t *testing.T) {
	var mu sync.Mutex
	var events []string
	note := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}

	obligations := []conformanceObligation{
		{name: "first", run: func(conformanceT) { note("start first") }},
		{name: "second", run: func(t conformanceT) { note("start second"); t.Error("no hello") }},
		{name: "third", run: func(conformanceT) { note("start third") }},
	}

	var streamed []ObligationResult
	results := streamObligations(context.Background(), obligations, func(result ObligationResult) {
		note("settled " + result.Name)
		streamed = append(streamed, result)
	})

	want := []string{
		"start first", "settled first",
		"start second", "settled second",
		"start third", "settled third",
	}
	if !slices.Equal(events, want) {
		t.Errorf("sequence = %q, want %q", events, want)
	}
	// The stream and the report must be the same verdicts: a caller that
	// prints the stream and then summarizes the slice would otherwise
	// contradict itself.
	if !reflect.DeepEqual(streamed, results) {
		t.Errorf("streamed %+v, returned %+v", streamed, results)
	}
	if len(results) != 3 || results[1].Passed || !results[0].Passed || !results[2].Passed {
		t.Errorf("verdicts = %+v, want the second one failed and the others passed", results)
	}
}

// TestStreamConformanceReportsEverySettledObligationBeforeOneThatStalls is the
// operator's question during those 165 seconds: which obligation is stuck?
//
// The stalling obligation is held open until the test has read every earlier
// verdict, so the assertion is not a race won by a fast machine: the verdicts
// are observed while the suite is provably still inside the obligation that has
// not returned. Its own failure — the suite's existing path for a candidate
// that never answers — then arrives last.
func TestStreamConformanceReportsEverySettledObligationBeforeOneThatStalls(t *testing.T) {
	release := make(chan struct{})
	settled := make(chan ObligationResult)

	obligations := []conformanceObligation{
		{name: "first", run: func(conformanceT) {}},
		{name: "second", run: func(conformanceT) {}},
		{name: "stalls", run: func(ct conformanceT) {
			<-release
			ct.Fatal("worker: handshake timed out: no hello within 15s")
		}},
	}

	report := make(chan []ObligationResult, 1)
	go func() {
		report <- streamObligations(context.Background(), obligations, func(result ObligationResult) {
			settled <- result
		})
	}()

	// Everything decided before the stall must already have been reported,
	// while the suite is still blocked inside the obligation that stalls.
	for _, name := range []string{"first", "second"} {
		result := <-settled
		if result.Name != name {
			t.Fatalf("streamed %q, want %q", result.Name, name)
		}
		if !result.Passed {
			t.Errorf("obligation %s failed: %q", result.Name, result.Failures)
		}
	}
	select {
	case result := <-settled:
		t.Fatalf("obligation %s was reported settled while it had not returned", result.Name)
	default:
	}

	close(release)
	last := <-settled
	if last.Name != "stalls" || last.Passed {
		t.Errorf("last verdict = %+v, want the stalled obligation failed", last)
	}
	if len(last.Failures) == 0 || !strings.Contains(last.Failures[0], "handshake timed out") {
		t.Errorf("the stalled obligation reported %q, want the reason it stalled", last.Failures)
	}
	if got := <-report; len(got) != 3 {
		t.Errorf("report covered %d obligations, want the whole list", len(got))
	}
}

// TestStreamConformanceStreamsObligationsItDoesNotRun covers the exported
// entry point and the one case where no worker is launched at all: a context
// that is already done. Every obligation is still accounted for, in the stream
// as well as the report, because a report that silently omitted what it skipped
// would read as a shorter contract.
func TestStreamConformanceStreamsObligationsItDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var streamed []string
	results := StreamConformance(ctx, ConformanceOptions{Worker: fakeWorkerPath}, func(result ObligationResult) {
		streamed = append(streamed, result.Name)
		if result.Passed {
			t.Errorf("obligation %s passed without being run", result.Name)
		}
		if len(result.Failures) != 1 || !strings.HasPrefix(result.Failures[0], "not run: ") {
			t.Errorf("obligation %s reported %q, want it was not run", result.Name, result.Failures)
		}
	})

	whole := conformanceObligations(conformanceTarget{})
	if len(results) != len(whole) || len(streamed) != len(whole) {
		t.Fatalf("streamed %d and reported %d obligations, want the whole suite of %d", len(streamed), len(results), len(whole))
	}
	for i, obligation := range whole {
		if streamed[i] != obligation.name {
			t.Errorf("streamed[%d] = %q, want %q", i, streamed[i], obligation.name)
		}
	}

	// RunConformanceWith is the same grading with nothing listening: the
	// callers that predate streaming must be unaffected by it.
	batched := RunConformanceWith(ctx, ConformanceOptions{Worker: fakeWorkerPath})
	if !reflect.DeepEqual(batched, results) {
		t.Errorf("RunConformanceWith reported %+v, want the same report as the stream", batched)
	}
}
