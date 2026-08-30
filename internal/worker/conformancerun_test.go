package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunConformanceFailsANonWorker points the suite at a program that exits
// without saying anything. The command's whole purpose is grading an
// implementation that does not work yet, so a candidate that is not a worker
// at all has to produce a full report with reasons rather than a crash, a
// truncated run, or a silent pass.
func TestRunConformanceFailsANonWorker(t *testing.T) {
	results := RunConformance(context.Background(), buildSilentBinary(t))

	if len(results) != len(conformanceObligations(conformanceTarget{})) {
		t.Fatalf("reported %d obligations, want the whole suite", len(results))
	}
	for _, result := range results {
		// run/no-credential-leak asserts an absence, and a program that
		// says nothing leaks nothing, so it is the one obligation a
		// silent binary satisfies honestly.
		if result.Passed && result.Name != "run/no-credential-leak" {
			t.Errorf("obligation %s passed against a program that never speaks the protocol", result.Name)
		}
		if !result.Passed && len(result.Failures) == 0 {
			t.Errorf("obligation %s failed without saying why", result.Name)
		}
		for _, failure := range result.Failures {
			if strings.Contains(failure, conformanceToken) {
				t.Errorf("obligation %s leaked the broker credential into its report", result.Name)
			}
		}
	}
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
