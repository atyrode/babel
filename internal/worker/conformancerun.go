package worker

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// ObligationResult is one obligation's verdict, as reported to a caller that
// has no *testing.T: the contract item's name, whether it held, and the
// messages the assertions produced.
//
// Messages are only carried for an obligation that failed, which is what
// `go test` shows without -v: a passing obligation's narration is noise, and a
// failing one's is the whole point.
type ObligationResult struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// ConformanceOptions selects what is examined and how strictly.
//
// Unsandboxed grades the worker against a relaxed containment requirement
// instead of the strict default. It exists because a worker declaring honestly
// weak containment fails every obligation that reaches worker mode with the
// same containment error, which says nothing about whether it implements the
// rest of the protocol. Separating the two is the difference between "needs a
// sandbox" and "does not speak the protocol", and those are different jobs.
//
// It is a bool rather than a Requirement on purpose. Which containment a real
// run demands is Babel's decision at launch, not a dial on a grading tool, so
// the only choice offered here is strict or relaxed.
type ConformanceOptions struct {
	Worker      string
	Args        []string
	Unsandboxed bool
}

// RunConformance drives the worker executable at workerPath, launched with
// args, through the same obligations Conformance runs, without a testing.T and
// without go test. It is how an implementation outside this module sits the
// exam: `babel conformance WORKER` is a thin front end over this function.
//
// Every obligation runs, whatever the ones before it did, and a failure —
// including a panic inside the suite or a worker that is not a worker at all —
// is reported as that obligation's verdict rather than ending the run. Once ctx
// is done the remaining obligations are reported unrun, so the returned slice
// always covers the whole contract and never credits a pass to something that
// was never attempted.
func RunConformance(ctx context.Context, workerPath string, args ...string) []ObligationResult {
	return RunConformanceWith(ctx, ConformanceOptions{Worker: workerPath, Args: args})
}

// RunConformanceWith is RunConformance with the examination's options stated
// explicitly.
func RunConformanceWith(ctx context.Context, opts ConformanceOptions) []ObligationResult {
	target := conformanceTarget{binary: opts.Worker, args: opts.Args}
	if opts.Unsandboxed {
		relaxed := Unsandboxed()
		target.requirement = &relaxed
	}
	obligations := conformanceObligations(target)
	results := make([]ObligationResult, 0, len(obligations))
	for _, obligation := range obligations {
		if err := ctx.Err(); err != nil {
			results = append(results, ObligationResult{
				Name:     obligation.name,
				Failures: []string{"not run: " + err.Error()},
			})
			continue
		}
		results = append(results, runObligation(obligation))
	}
	return results
}

// runObligation executes one obligation in its own goroutine, which is what
// makes Fatal abort exactly that obligation: the recorder calls runtime.Goexit,
// so the deferred cancels and process teardown inside the obligation still run,
// the goroutine ends, and the loop continues. It is how testing.T does it, for
// the same reason.
func runObligation(obligation conformanceObligation) ObligationResult {
	rec := &conformanceRecorder{}
	done := make(chan struct{})
	go func() {
		defer func() {
			// A panic here is a defect in the suite or in the client, not a
			// verdict on any other obligation, so it is recorded and the run
			// continues. recover reports nil during a Goexit, so a Fatal
			// passes through this untouched.
			if r := recover(); r != nil {
				rec.Errorf("panic: %v", r)
				for _, line := range strings.Split(string(debug.Stack()), "\n") {
					if strings.TrimSpace(line) != "" {
						rec.messages = append(rec.messages, line)
					}
				}
			}
			close(done)
		}()
		obligation.run(rec)
	}()
	<-done

	result := ObligationResult{Name: obligation.name, Passed: !rec.failed}
	if rec.failed {
		result.Failures = rec.messages
	}
	return result
}

// conformanceRecorder is the non-test conformanceT: it collects what the
// assertions say instead of reporting it to a test binary.
type conformanceRecorder struct {
	// messages holds everything the obligation said, in the order it said
	// it, so a failure reads as the sequence that produced it.
	messages []string
	failed   bool
}

func (r *conformanceRecorder) Helper() {}

func (r *conformanceRecorder) Error(args ...any) {
	r.messages = append(r.messages, fmt.Sprint(args...))
	r.failed = true
}

func (r *conformanceRecorder) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	r.failed = true
}

func (r *conformanceRecorder) Fatal(args ...any) {
	r.Error(args...)
	runtime.Goexit()
}

func (r *conformanceRecorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *conformanceRecorder) Logf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}
