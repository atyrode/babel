package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// fakeWorkerPath is the synthetic counterpart, built once per test binary.
//
// Building it in TestMain rather than in each test is deliberate: a dozen
// tests each spending a compile on the same fixture would cost more than every
// test in this file combined, and the binary is identical for all of them.
// Nothing is prebuilt or committed — the suite stays hermetic.
var fakeWorkerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "babel-fakeworker-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating fixture dir: %v\n", err)
		os.Exit(1)
	}
	fakeWorkerPath = filepath.Join(dir, "fakeworker")
	build := exec.Command("go", "build", "-o", fakeWorkerPath, "./testdata/fakeworker")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "building the fake worker: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testToken is the synthetic run-scoped broker credential these tests plant in
// every job. It is long and non-dictionary so a substring search cannot match
// anything the fixture or the supervisor emits by coincidence.
const testToken = "BROKERTOKEN0f2a71c94b8e3d65a0f7cb12"

// fixture is one supervised run: the worker's misbehaviour flags, the budgets,
// the policy, and the job.
type fixture struct {
	args     []string
	limits   Limits
	policy   Authorizer
	versions []int
	progress func(ProgressRecord)
	job      Job

	// diag captures what the worker wrote to stderr, which is one of the
	// channels a credential could escape through.
	diag bytes.Buffer
}

// newFixture is a well-behaved run with budgets tight enough that a failing
// assertion fails fast rather than waiting out a default.
func newFixture(directive string) *fixture {
	return &fixture{
		limits: Limits{
			HandshakeTimeout: 10 * time.Second,
			IdleTimeout:      10 * time.Second,
			ExitGrace:        5 * time.Second,
			TerminateGrace:   500 * time.Millisecond,
		},
		policy: AllowWithinGrant(),
		job: Job{
			JobID:   "test-job",
			RunID:   "test-run",
			Profile: ProfileRef{ID: "synthetic-profile", Revision: 1},
			Recipes: []RecipeRef{{ID: "outcome-integrity", Version: 1}},
			Grant: Grant{
				Capabilities: []Capability{CapabilityCorpusSearch, CapabilityRepoRead},
				Disclosure:   DisclosureLocal,
			},
			Sources: []Source{{Kind: "session", Selector: "omp/synthetic-session"}},
			Broker:  Broker{Endpoint: "http://127.0.0.1:1/evidence", Token: testToken},
			Params:  map[string]string{ParamConformance: directive},
		},
	}
}

// client builds the Client for this fixture.
func (f *fixture) client(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{
		Binary:      fakeWorkerPath,
		Args:        f.args,
		Versions:    f.versions,
		Authorizer:  f.policy,
		Limits:      f.limits,
		OnProgress:  f.progress,
		Diagnostics: &f.diag,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// run executes the fixture with a deadline that bounds a hung supervisor
// without competing with the fixture's own budgets.
func (f *fixture) run(t *testing.T) (*Receipt, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	receipt, err := f.client(t).Run(ctx, f.job)
	if receipt == nil {
		t.Fatalf("Run returned no receipt (error %v)", err)
	}
	return receipt, err
}

// TestConformanceAgainstFakeWorker runs the exported contract suite against a
// worker that implements the protocol correctly. It is what keeps Conformance
// itself honest: a suite no implementation passes would assert nothing.
func TestConformanceAgainstFakeWorker(t *testing.T) {
	Conformance(t, fakeWorkerPath)
}

// TestHandshakeFailures covers every way version negotiation can end without
// an accepted worker. Each one is a distinct error because "the worker is
// wrong" is not something an operator can act on.
func TestHandshakeFailures(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		limits  func(*Limits)
		wantErr error
	}{
		{
			name:    "no mutually supported version",
			args:    []string{"-versions", "99"},
			wantErr: ErrVersionMismatch,
		},
		{
			name:    "different protocol",
			args:    []string{"-protocol", "some.other-protocol"},
			wantErr: ErrProtocolMismatch,
		},
		{
			name:    "first line is not a hello",
			args:    []string{"-no-hello"},
			limits:  func(l *Limits) { l.HandshakeTimeout = 300 * time.Millisecond },
			wantErr: ErrHandshakeTimeout,
		},
		{
			name:    "worker mode not offered",
			args:    []string{"-modes", ModeConfigure},
			wantErr: ErrModeUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(ConformanceWellBehaved)
			f.args = test.args
			if test.limits != nil {
				test.limits(&f.limits)
			}
			_, err := f.run(t)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestRefusalPrecedesAnyJobMaterial is the reason the worker speaks first. A
// counterpart Babel cannot supervise must never see the job: not the broker
// credential, not the source selectors, not the capability grant.
func TestRefusalPrecedesAnyJobMaterial(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stdin.jsonl")
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-versions", "99", "-record", record}

	_, err := f.run(t)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("Run error = %v, want ErrVersionMismatch", err)
	}

	written, readErr := os.ReadFile(record)
	if readErr != nil {
		t.Fatalf("reading what the worker was sent: %v", readErr)
	}
	got := string(written)
	// Non-vacuity: the worker must have been told why, or this file would be
	// empty for the wrong reason.
	if !strings.Contains(got, `"type":"`+MessageRefuse+`"`) {
		t.Fatalf("the refused worker was never told why:\n%s", got)
	}
	for _, forbidden := range []string{
		`"type":"` + MessageJob + `"`,
		testToken,
		"omp/synthetic-session",
		string(CapabilityCorpusSearch),
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("a refused worker received %q:\n%s", forbidden, got)
		}
	}
}

// TestStreamViolations covers every way a worker can break the event contract.
// Each row must produce its own error and its own receipt failure code: a
// single "protocol error" would tell an operator nothing about whether to
// retry, upgrade Code, or file a bug.
func TestStreamViolations(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		limits      func(*Limits)
		wantErr     error
		wantFailure string
	}{
		{
			name:        "line is not json",
			args:        []string{"-malformed"},
			wantErr:     ErrMalformedEvent,
			wantFailure: "malformed-event",
		},
		{
			name:        "undefined event type",
			args:        []string{"-unknown-event"},
			wantErr:     ErrUnknownEventType,
			wantFailure: "unknown-event-type",
		},
		{
			name:        "oversized line",
			args:        []string{"-oversized", "4096"},
			limits:      func(l *Limits) { l.MaxLineBytes = 1024 },
			wantErr:     ErrOversizedLine,
			wantFailure: "oversized-line",
		},
		{
			name:        "exits before a result",
			args:        []string{"-exit-before-result"},
			wantErr:     ErrNoResult,
			wantFailure: "no-result",
		},
		{
			name:        "repeated sequence number",
			args:        []string{"-bad-seq"},
			wantErr:     ErrSequence,
			wantFailure: "sequence-violation",
		},
		{
			name:        "two results",
			args:        []string{"-duplicate-result"},
			wantErr:     ErrDuplicateResult,
			wantFailure: "duplicate-result",
		},
		{
			name:        "event after the result",
			args:        []string{"-event-after-result"},
			wantErr:     ErrEventAfterResult,
			wantFailure: "event-after-result",
		},
		{
			// The worker's own error record is kept, because the worker did
			// report a failure; the contradiction is what Run reports.
			name:    "result after the error",
			args:    []string{"-result-after-error"},
			wantErr: ErrResultAfterError,
		},
		{
			name:        "resolves another profile",
			args:        []string{"-wrong-profile"},
			wantErr:     ErrProfileMismatch,
			wantFailure: "profile-mismatch",
		},
		{
			name:        "does not exit after its result",
			args:        []string{"-linger", "30s"},
			limits:      func(l *Limits) { l.ExitGrace = 300 * time.Millisecond },
			wantErr:     ErrWorkerLingered,
			wantFailure: "lingered",
		},
		{
			// The stream ends and the process does not. Waiting for a reap
			// that is never coming would be an unbounded hang.
			name:        "closes stdout but keeps running",
			args:        []string{"-close-stdout"},
			limits:      func(l *Limits) { l.ExitGrace = 300 * time.Millisecond },
			wantErr:     ErrWorkerLingered,
			wantFailure: "lingered",
		},
		{
			name:        "contradicts its own result with a nonzero exit",
			args:        []string{"-exit-code", "3"},
			wantErr:     ErrDirtyExit,
			wantFailure: "dirty-exit",
		},
		{
			// Stderr traffic must not count as progress, or a worker could
			// hold a run open forever by logging.
			name:        "writes only to stderr and never exits",
			args:        []string{"-stderr-only"},
			limits:      func(l *Limits) { l.IdleTimeout = 400 * time.Millisecond },
			wantErr:     ErrWorkerStalled,
			wantFailure: "stalled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(ConformanceWellBehaved)
			f.args = test.args
			if test.limits != nil {
				test.limits(&f.limits)
			}
			receipt, err := f.run(t)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, test.wantErr)
			}
			if test.wantFailure == "" {
				return
			}
			if receipt.Failure == nil {
				t.Fatalf("no failure recorded in the receipt for %v", err)
			}
			if receipt.Failure.Code != test.wantFailure {
				t.Errorf("failure code = %q, want %q", receipt.Failure.Code, test.wantFailure)
			}
			if receipt.Failure.Origin != FailureBabel {
				t.Errorf("failure origin = %q, want %q", receipt.Failure.Origin, FailureBabel)
			}
		})
	}
}

// TestCredentialShapedMetadataIsRefused covers both modes. Babel stores only
// non-secret execution metadata, so a worker offering a credential is refused
// rather than silently filtered: the filter would hide a broken worker from
// whoever has to fix it.
func TestCredentialShapedMetadataIsRefused(t *testing.T) {
	t.Run("configure", func(t *testing.T) {
		f := newFixture(ConformanceWellBehaved)
		f.args = []string{"-secret-metadata"}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := f.client(t).Configure(ctx); !errors.Is(err, ErrSecretDeclared) {
			t.Fatalf("Configure error = %v, want ErrSecretDeclared", err)
		}
	})
	t.Run("worker", func(t *testing.T) {
		f := newFixture(ConformanceWellBehaved)
		f.args = []string{"-secret-metadata"}
		receipt, err := f.run(t)
		if !errors.Is(err, ErrSecretDeclared) {
			t.Fatalf("Run error = %v, want ErrSecretDeclared", err)
		}
		if receipt.Metadata != nil {
			t.Errorf("refused metadata was recorded anyway: %v", receipt.Metadata)
		}
	})
}

// TestDeniedToolRequestDoesNotEndRun is SPEC.md §2.6's authorization rule: an
// unauthorized request is denied without terminating the run, and the denial
// is part of the receipt.
func TestDeniedToolRequestDoesNotEndRun(t *testing.T) {
	f := newFixture(ConformanceRequestTool)
	f.policy = DenyAll("test policy denies everything")

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v; a denial must not end the run", err)
	}
	if receipt.Result == nil {
		t.Fatal("the run produced no result after a denial")
	}
	if len(receipt.ToolRequests) != 1 {
		t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
	}
	request := receipt.ToolRequests[0]
	switch {
	case request.Allowed:
		t.Error("the request was allowed by a policy that denies everything")
	case request.DenyCode != DenyPolicy:
		t.Errorf("deny code = %q, want %q", request.DenyCode, DenyPolicy)
	case request.Reason != "test policy denies everything":
		t.Errorf("reason = %q, want the policy's own reason", request.Reason)
	}
	if receipt.Denied() != 1 {
		t.Errorf("Denied() = %d, want 1", receipt.Denied())
	}
	// The worker must have observed the decision, not merely been sent one.
	if !strings.Contains(string(receipt.Result.Payload), decisionDeny) {
		t.Errorf("the worker did not report the denial it received: %s", receipt.Result.Payload)
	}
}

// TestGrantIsCheckedBeforePolicy proves the grant is a boundary rather than a
// default. A capability the run never granted is denied even by the most
// permissive policy — and the policy is not even consulted, so no policy bug
// can widen a grant.
func TestGrantIsCheckedBeforePolicy(t *testing.T) {
	consulted := false
	f := newFixture(ConformanceRequestUngranted)
	f.policy = AuthorizerFunc(func(context.Context, ToolRequest) Decision {
		consulted = true
		return Decision{Allow: true, Reason: "a policy that allows everything"}
	})

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(receipt.ToolRequests) != 1 {
		t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
	}
	request := receipt.ToolRequests[0]
	if request.Allowed {
		t.Error("sandbox-exec was allowed although the run never granted it")
	}
	if request.DenyCode != DenyNotGranted {
		t.Errorf("deny code = %q, want %q", request.DenyCode, DenyNotGranted)
	}
	if consulted {
		t.Error("the policy was consulted for a capability outside the grant")
	}
	// Non-vacuity: a granted capability must reach the same policy, or the
	// assertion above would pass for a policy that is never called at all.
	consulted = false
	granted := newFixture(ConformanceRequestTool)
	granted.policy = f.policy
	if _, err := granted.run(t); err != nil {
		t.Fatalf("granted run: %v", err)
	}
	if !consulted {
		t.Error("the policy was never consulted for a granted capability")
	}
}

// TestUnknownCapabilityIsDeniedNotFatal covers a capability name Babel does
// not define: it is denied with its own code, because a policy cannot reason
// about a boundary Babel has no implementation for.
func TestUnknownCapabilityIsDeniedNotFatal(t *testing.T) {
	f := newFixture("request-unknown")
	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(receipt.ToolRequests) != 1 {
		t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
	}
	if code := receipt.ToolRequests[0].DenyCode; code != DenyUnknownCapability {
		t.Errorf("deny code = %q, want %q", code, DenyUnknownCapability)
	}
}

// TestMissingAuthorizerFailsClosed: a run configured without a policy denies
// everything. A run with no policy is not a run with a permissive one.
func TestMissingAuthorizerFailsClosed(t *testing.T) {
	f := newFixture(ConformanceRequestTool)
	f.policy = nil

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(receipt.ToolRequests) != 1 {
		t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
	}
	request := receipt.ToolRequests[0]
	if request.Allowed {
		t.Error("a request was allowed with no authorizer configured")
	}
	if !strings.Contains(request.Reason, "no authorizer") {
		t.Errorf("reason = %q, want it to name the missing authorizer", request.Reason)
	}
}

// TestCredentialNeverReachesReceiptOrError plants a worker that deliberately
// echoes the run's broker credential into its stderr, its result payload, its
// result schema and its tool arguments. None of that may come back.
//
// The adversarial worker is what makes this non-vacuous: with a well-behaved
// fixture the assertion would hold whether or not Babel scrubs anything.
func TestCredentialNeverReachesReceiptOrError(t *testing.T) {
	f := newFixture(ConformanceRequestTool)
	f.args = []string{"-echo-token"}

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rendered := fmt.Sprintf("%+v", *receipt)
	if receipt.Result != nil {
		rendered += " " + string(receipt.Result.Payload)
	}
	if strings.Contains(rendered, testToken) {
		t.Error("the run-scoped broker credential is in the receipt")
	}
	if receipt.StderrTail == "" {
		t.Fatal("no stderr was captured, so the scrubbing check is vacuous")
	}
	if !strings.Contains(rendered, redactedMarker) {
		t.Errorf("nothing was redacted, so the worker's echo never reached Babel:\n%s", rendered)
	}

	// The live diagnostics sink is a separate channel from the receipt's tail,
	// and an operator pastes it into bug reports.
	diagnostics := f.diag.String()
	if diagnostics == "" {
		t.Fatal("the diagnostics sink received nothing, so its check is vacuous")
	}
	if strings.Contains(diagnostics, testToken) {
		t.Errorf("the broker credential reached the diagnostics sink:\n%s", diagnostics)
	}
	if !strings.Contains(diagnostics, redactedMarker) {
		t.Errorf("the worker's stderr echo was not redacted:\n%s", diagnostics)
	}

	// The same guarantee on the error path: a failing adversarial worker must
	// not put the credential into an error string either.
	failing := newFixture(ConformanceRequestTool)
	failing.args = []string{"-echo-token", "-malformed"}
	_, failErr := failing.run(t)
	if failErr == nil {
		t.Fatal("the malformed fixture produced no error")
	}
	if strings.Contains(failErr.Error(), testToken) {
		t.Errorf("the credential is in an error string: %v", failErr)
	}
}

// TestJobSecretNeverReachesArgvOrEnvironment checks the claim against the real
// process rather than against Babel's own scrubbed diagnostics: the worker
// dumps its argv and environment to a file, and the test reads it unfiltered.
// The job document travels on stdin precisely so a process listing cannot
// expose the broker credential.
func TestJobSecretNeverReachesArgvOrEnvironment(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "context.txt")
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-dump", dump}

	if _, err := f.run(t); err != nil {
		t.Fatalf("Run: %v", err)
	}
	contents, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("reading the process dump: %v", err)
	}
	got := string(contents)
	// Non-vacuity: the dump must actually describe a process.
	if !strings.Contains(got, "argv: ") || !strings.Contains(got, "PATH=") {
		t.Fatalf("the process dump is not a process dump:\n%s", got)
	}
	if strings.Contains(got, testToken) {
		t.Error("the broker credential is visible in the worker's argv or environment")
	}
	if strings.Contains(got, "omp/synthetic-session") {
		t.Error("a source selector is visible in the worker's argv or environment")
	}
}

// TestToolArgumentsAreNotRecorded: the receipt fingerprints arguments instead
// of storing them, so a private locator or an echoed credential inside one
// cannot enter Babel's durable audit record at all.
func TestToolArgumentsAreNotRecorded(t *testing.T) {
	const marker = "ARGUMENTMARKER5c8b1e70da234f96"
	f := newFixture(ConformanceRequestTool)
	f.args = []string{"-argument-marker", marker}

	var seen json.RawMessage
	f.policy = AuthorizerFunc(func(_ context.Context, req ToolRequest) Decision {
		seen = req.Arguments
		return Decision{Allow: true}
	})

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Non-vacuity: the policy must have seen the marker, or its absence from
	// the receipt would prove nothing about recording.
	if !strings.Contains(string(seen), marker) {
		t.Fatalf("the policy never saw the arguments: %s", seen)
	}
	if strings.Contains(fmt.Sprintf("%+v", *receipt), marker) {
		t.Error("tool arguments were recorded in the receipt")
	}
	request := receipt.ToolRequests[0]
	if want := digest.Bytes(seen); request.ArgumentsDigest != want {
		t.Errorf("argument digest = %q, want %q", request.ArgumentsDigest, want)
	}
	if request.ArgumentsBytes != len(seen) {
		t.Errorf("argument size = %d, want %d", request.ArgumentsBytes, len(seen))
	}
}

// TestUnknownFieldsAreIgnoredAndRecorded: forward compatibility is a protocol
// requirement, so a newer counterpart's extra fields must not fail a run — but
// they are named in the receipt, which is how an operator notices that Code is
// ahead of this build.
func TestUnknownFieldsAreIgnoredAndRecorded(t *testing.T) {
	const extension = "x-fakeworker-extension"

	t.Run("worker mode", func(t *testing.T) {
		f := newFixture(ConformanceWellBehaved)
		f.args = []string{"-unknown-fields"}
		receipt, err := f.run(t)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("an unknown field prevented a result")
		}
		found := false
		for _, name := range receipt.UnknownFields {
			if name == extension {
				found = true
			}
		}
		if !found {
			t.Errorf("UnknownFields = %v, want it to name %q", receipt.UnknownFields, extension)
		}
	})

	t.Run("configure mode", func(t *testing.T) {
		f := newFixture(ConformanceWellBehaved)
		f.args = []string{"-unknown-fields"}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cfg, err := f.client(t).Configure(ctx)
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		raw, ok := cfg.Extra[extension]
		if !ok {
			t.Fatalf("Extra = %v, want the preserved %q", cfg.Unknown, extension)
		}
		if !strings.Contains(string(raw), "Babel does not define") {
			t.Errorf("the preserved value was not preserved verbatim: %s", raw)
		}
	})
}

// TestEventBudgetEndsARunawayStream: the stream is bounded, because a worker
// that never stops emitting would otherwise hold a run open forever.
func TestEventBudgetEndsARunawayStream(t *testing.T) {
	f := newFixture(ConformanceWellBehaved)
	// A well-behaved run emits three events: the resolved configuration,
	// progress, then the result. Two is one short of that on purpose.
	f.limits.MaxEvents = 2

	receipt, err := f.run(t)
	if !errors.Is(err, ErrEventBudget) {
		t.Fatalf("Run error = %v, want ErrEventBudget", err)
	}
	if receipt.Result != nil {
		t.Error("a result was recorded past the event budget")
	}
}

// TestToolBudgetDeniesThenEndsALoopingWorker: past the budget every request is
// denied with its own code, and a worker that keeps asking anyway is stopped.
// Denials keep a run alive; ignoring them indefinitely is a loop, not a
// strategy.
func TestToolBudgetDeniesThenEndsALoopingWorker(t *testing.T) {
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-tool-requests", "60"}
	f.limits.MaxToolRequests = 2

	receipt, err := f.run(t)
	if !errors.Is(err, ErrToolBudget) {
		t.Fatalf("Run error = %v, want ErrToolBudget", err)
	}
	if len(receipt.ToolRequests) != f.limits.MaxToolRequests+toolBudgetSlack {
		t.Fatalf("recorded %d tool requests, want the budget plus its slack (%d)",
			len(receipt.ToolRequests), f.limits.MaxToolRequests+toolBudgetSlack)
	}
	allowed, limited := 0, 0
	for _, request := range receipt.ToolRequests {
		switch {
		case request.Allowed:
			allowed++
		case request.DenyCode == DenyLimit:
			limited++
		}
	}
	if allowed != f.limits.MaxToolRequests {
		t.Errorf("allowed %d requests, want %d", allowed, f.limits.MaxToolRequests)
	}
	if limited == 0 {
		t.Error("no request was denied with the limit code, so the budget denial is untested")
	}
}

// TestRunLeavesNoGoroutines: Run owns every goroutine and pipe reader it
// starts, including on the failure paths, where a leak is likeliest.
func TestRunLeavesNoGoroutines(t *testing.T) {
	// One warm-up run first: the baseline must be taken after any lazy
	// runtime initialization, or a first-run goroutine would look like a leak.
	if _, err := newFixture(ConformanceWellBehaved).run(t); err != nil {
		t.Fatalf("warm-up run: %v", err)
	}
	settle(t, runtime.NumGoroutine())
	before := runtime.NumGoroutine()

	for _, args := range [][]string{
		nil,
		{"-malformed"},
		{"-exit-before-result"},
		{"-stderr-only"},
	} {
		f := newFixture(ConformanceRequestTool)
		f.args = args
		f.limits.IdleTimeout = 400 * time.Millisecond
		if _, err := f.run(t); err != nil && args == nil {
			t.Fatalf("run %v: %v", args, err)
		}
	}

	settle(t, before)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: %d before, %d after; Run must outlive nothing", before, after)
	}
}

// settle waits, bounded, for the goroutine count to fall back to want. A
// goroutine that has been told to stop still needs to be scheduled.
func settle(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNegotiateSelectsHighestCommonVersion pins the negotiation rule itself.
func TestNegotiateSelectsHighestCommonVersion(t *testing.T) {
	tests := []struct {
		name       string
		local      []int
		remote     []int
		want       int
		wantAgreed bool
	}{
		{name: "identical", local: []int{1}, remote: []int{1}, want: 1, wantAgreed: true},
		{name: "highest common", local: []int{1, 2, 3}, remote: []int{2, 3, 9}, want: 3, wantAgreed: true},
		{name: "worker is newer", local: []int{1}, remote: []int{1, 2}, want: 1, wantAgreed: true},
		{name: "no overlap", local: []int{1}, remote: []int{2}, want: 0, wantAgreed: false},
		{name: "worker offers none", local: []int{1}, remote: nil, want: 0, wantAgreed: false},
		{name: "unordered", local: []int{3, 1}, remote: []int{1, 3}, want: 3, wantAgreed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, agreed := negotiate(test.local, test.remote)
			if got != test.want || agreed != test.wantAgreed {
				t.Errorf("negotiate(%v, %v) = %d, %t; want %d, %t",
					test.local, test.remote, got, agreed, test.want, test.wantAgreed)
			}
		})
	}
}

// TestValidateMetadataNamesEverySecretShapedKey: the refusal must name what to
// fix, and must catch a credential hidden inside a longer field name.
func TestValidateMetadataNamesEverySecretShapedKey(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		wantErr  bool
		wantName string
	}{
		{
			name:     "non-secret metadata",
			metadata: map[string]string{"provider": "local", "model": "m", "thinking": "high"},
		},
		{
			name:     "exact name",
			metadata: map[string]string{"token": "x"},
			wantErr:  true,
			wantName: "token",
		},
		{
			name:     "name that hides the marker",
			metadata: map[string]string{"openai_api_key_value": "x"},
			wantErr:  true,
			wantName: "openai_api_key_value",
		},
		{
			name:     "mixed case",
			metadata: map[string]string{"Provider-Secret": "x"},
			wantErr:  true,
			wantName: "Provider-Secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMetadata(test.metadata)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("validateMetadata = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrSecretDeclared) {
				t.Fatalf("validateMetadata = %v, want ErrSecretDeclared", err)
			}
			if !strings.Contains(err.Error(), test.wantName) {
				t.Errorf("error %v does not name %q", err, test.wantName)
			}
		})
	}
}

// TestJobEncodingIsTheDocumentedShape pins the wire format of the one message
// Babel authors that carries the run's whole boundary. Code reads these exact
// names, so a rename here is a break there.
func TestJobEncodingIsTheDocumentedShape(t *testing.T) {
	expires := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	job := Job{
		JobID:   "j-1",
		RunID:   "r-1",
		Profile: ProfileRef{ID: "p-1", Revision: 4},
		Recipes: []RecipeRef{{ID: "outcome-integrity", Version: 1}},
		Grant: Grant{
			Capabilities: []Capability{CapabilityCorpusSearch},
			Disclosure:   DisclosureLocal,
			ExpiresAt:    expires,
		},
		Sources: []Source{{Kind: "session", Selector: "omp/s", Digest: "sha256:0", Snapshot: "snap"}},
		Broker:  Broker{Endpoint: "http://127.0.0.1:1", Token: testToken},
		Params:  map[string]string{"k": "v"},
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, name := range []string{
		"type", "protocol", "job_id", "run_id", "profile", "recipes",
		"grant", "sources", "broker", "params",
	} {
		if _, ok := decoded[name]; !ok {
			t.Errorf("encoded job has no %q field: %s", name, encoded)
		}
	}
	if decoded["type"] != MessageJob || decoded["protocol"] != ProtocolName {
		t.Errorf("job header = %v/%v", decoded["type"], decoded["protocol"])
	}
	grant, _ := decoded["grant"].(map[string]any)
	if grant["disclosure"] != DisclosureLocal {
		t.Errorf("grant disclosure = %v", grant["disclosure"])
	}
	if grant["expires"] != "2026-08-29T12:00:00Z" {
		t.Errorf("grant expires = %v, want RFC 3339 UTC", grant["expires"])
	}

	t.Run("extra fields merge", func(t *testing.T) {
		job.Extra = map[string]json.RawMessage{"x-future": json.RawMessage(`1`)}
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if !strings.Contains(string(encoded), `"x-future":1`) {
			t.Errorf("extra field was not merged: %s", encoded)
		}
	})

	t.Run("extra fields may not shadow the contract", func(t *testing.T) {
		job.Extra = map[string]json.RawMessage{"grant": json.RawMessage(`{}`)}
		if _, err := json.Marshal(job); err == nil {
			t.Error("an Extra field silently replaced the capability grant")
		}
	})
}

// TestExpiredGrantDeniesEveryRequest: the grant carries its own expiry, so a
// run that outlives its authorization stops being able to gather evidence
// even if the policy would still say yes.
func TestExpiredGrantDeniesEveryRequest(t *testing.T) {
	f := newFixture(ConformanceRequestTool)
	f.job.Grant.ExpiresAt = time.Now().Add(-time.Minute)

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(receipt.ToolRequests) != 1 {
		t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
	}
	request := receipt.ToolRequests[0]
	if request.Allowed {
		t.Error("a request was allowed under an expired grant")
	}
	if !strings.Contains(request.Reason, "expired") {
		t.Errorf("reason = %q, want it to name the expiry", request.Reason)
	}
}

// TestReadLineEnforcesTheConfiguredLimit pins the reason this package reads
// lines itself instead of using bufio.Scanner: Scanner reports ErrTooLong only
// once a token outgrows its buffer, so a small configured maximum would not be
// enforced at all.
func TestReadLineEnforcesTheConfiguredLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int
		want    string
		wantErr error
	}{
		{name: "exactly at the limit", input: "abcd\n", max: 4, want: "abcd"},
		{name: "one byte over", input: "abcde\n", max: 4, wantErr: ErrOversizedLine},
		{name: "far over a small limit", input: strings.Repeat("x", 300_000) + "\n", max: 8, wantErr: ErrOversizedLine},
		// A worker killed mid-write leaves a line with no newline. It is
		// returned alongside EOF rather than dropped: SPEC.md §6.1's torn-line
		// rule is to account for the record, never to discard it silently.
		{name: "unterminated final line", input: "abc", max: 8, want: "abc", wantErr: io.EOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, err := readLine(bufio.NewReaderSize(strings.NewReader(test.input), 64), test.max)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("readLine error = %v, want %v", err, test.wantErr)
			}
			if got := string(line); got != test.want {
				t.Errorf("readLine = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDiagnosticLinesAreBounded: stderr is not protocol, so an over-long line
// is truncated rather than fatal — but it must be bounded, because buffering a
// worker's runaway log line would let it exhaust Babel's memory.
func TestDiagnosticLinesAreBounded(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		max           int
		want          string
		wantTruncated bool
		wantErr       error
	}{
		{name: "short line", input: "abc\n", max: 16, want: "abc\n"},
		{
			name:          "longer than the limit",
			input:         strings.Repeat("x", 64) + "\n",
			max:           8,
			want:          strings.Repeat("x", 8),
			wantTruncated: true,
		},
		{
			// Far larger than the read buffer, so the discarding path runs
			// many times. Nothing past the limit may be retained.
			name:          "far longer than the read buffer",
			input:         strings.Repeat("x", 400_000) + "\n",
			max:           8,
			want:          strings.Repeat("x", 8),
			wantTruncated: true,
		},
		{name: "unterminated", input: "abc", max: 16, want: "abc", wantErr: io.EOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, truncated, err := readDiagnosticLine(
				bufio.NewReaderSize(strings.NewReader(test.input), 64), test.max)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if got := string(line); got != test.want {
				t.Errorf("line = %q, want %q", got, test.want)
			}
			if truncated != test.wantTruncated {
				t.Errorf("truncated = %t, want %t", truncated, test.wantTruncated)
			}
		})
	}

	t.Run("the next line is still read", func(t *testing.T) {
		// The discarded remainder must be consumed, or every later diagnostic
		// would arrive as a fragment of the one that overflowed.
		reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 500)+"\nnext\n"), 64)
		if _, truncated, err := readDiagnosticLine(reader, 8); err != nil || !truncated {
			t.Fatalf("first line: truncated %t, error %v", truncated, err)
		}
		line, truncated, err := readDiagnosticLine(reader, 8)
		if err != nil || truncated {
			t.Fatalf("second line: truncated %t, error %v", truncated, err)
		}
		if got := string(line); got != "next\n" {
			t.Errorf("second line = %q, want %q", got, "next\n")
		}
	})
}
