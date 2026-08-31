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
	"slices"
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

// workerStdin is everything Babel wrote to one worker's stdin, captured by the
// fixture itself rather than reconstructed from Babel's own intentions. The
// message types are what the ordering assertions read; the raw text is what a
// credential search reads, because a token can appear in a field no assertion
// here names.
type workerStdin struct {
	types []string
	raw   string
}

func readWorkerStdin(t *testing.T, path string) workerStdin {
	t.Helper()
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading what the worker was sent: %v", err)
	}
	captured := workerStdin{raw: string(written)}
	for line := range strings.SplitSeq(strings.TrimSpace(captured.raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("Babel wrote a line the worker could not decode: %q: %v", line, err)
		}
		captured.types = append(captured.types, msg.Type)
	}
	return captured
}

// line returns the one captured message of the given type, or fails: every
// assertion about a stage's contents is about a message that must exist
// exactly once.
func (w workerStdin) line(t *testing.T, kind string) string {
	t.Helper()
	var found string
	for line := range strings.SplitSeq(strings.TrimSpace(w.raw), "\n") {
		if strings.Contains(line, `"type":"`+kind+`"`) {
			if found != "" {
				t.Fatalf("Babel wrote more than one %s message:\n%s", kind, w.raw)
			}
			found = line
		}
	}
	if found == "" {
		t.Fatalf("Babel never wrote a %s message:\n%s", kind, w.raw)
	}
	return found
}

// TestRefusalPrecedesAnyJobMaterial is the reason the worker speaks first. A
// counterpart Babel cannot supervise must never see the job: not the broker
// credential, not the source selectors, not the capability grant, and not the
// preamble either — a refused worker is refused before the staging begins.
func TestRefusalPrecedesAnyJobMaterial(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stdin.jsonl")
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-versions", "99", "-record", record}

	_, err := f.run(t)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("Run error = %v, want ErrVersionMismatch", err)
	}

	stdin := readWorkerStdin(t, record)
	// Non-vacuity: the worker must have been told why, or this file would be
	// empty for the wrong reason.
	if !slices.Contains(stdin.types, MessageRefuse) {
		t.Fatalf("the refused worker was never told why:\n%s", stdin.raw)
	}
	// The refusal names the version Babel requires, because "unsupported" on
	// its own tells a counterpart nothing about what to build. This is the
	// clean-cutover half of the version bump: the old worker is refused, and
	// the refusal is the migration note.
	refusal := stdin.line(t, MessageRefuse)
	if !strings.Contains(refusal, fmt.Sprintf(`"supported":[%d]`, ProtocolVersion)) {
		t.Errorf("the refusal does not name the version Babel requires: %s", refusal)
	}
	for _, unwanted := range []string{MessageJobPreamble, MessageJob} {
		if slices.Contains(stdin.types, unwanted) {
			t.Errorf("a refused worker received a %s message:\n%s", unwanted, stdin.raw)
		}
	}
	for _, forbidden := range []string{
		testToken,
		"omp/synthetic-session",
		string(CapabilityCorpusSearch),
		"synthetic-profile",
	} {
		if strings.Contains(stdin.raw, forbidden) {
			t.Errorf("a refused worker received %q:\n%s", forbidden, stdin.raw)
		}
	}
}

// TestStagedJobOrdersTheCredentialAfterTheDeclaration is the property
// atyrode/babel#71 asked for, read off the worker's own stdin rather than off
// Babel's intentions: the preamble goes out first and carries no material, the
// worker's declaration answers it, and the recipes, grant, sources and broker
// credential travel only afterwards.
func TestStagedJobOrdersTheCredentialAfterTheDeclaration(t *testing.T) {
	record := filepath.Join(t.TempDir(), "stdin.jsonl")
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-record", record}

	receipt, err := f.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Result == nil || receipt.Containment.Backend == "" {
		t.Fatalf("the run did not complete behind a declared boundary: %+v", receipt)
	}

	stdin := readWorkerStdin(t, record)
	want := []string{MessageAccept, MessageJobPreamble, MessageJob}
	if !slices.Equal(stdin.types, want) {
		t.Fatalf("Babel wrote %v, want %v:\n%s", stdin.types, want, stdin.raw)
	}

	preamble := stdin.line(t, MessageJobPreamble)
	if !strings.Contains(preamble, "synthetic-profile") {
		t.Errorf("the preamble names no profile, so the worker had nothing to resolve: %s", preamble)
	}
	for _, forbidden := range []string{
		testToken,
		"omp/synthetic-session",
		string(CapabilityCorpusSearch),
		"outcome-integrity",
		"broker",
	} {
		if strings.Contains(preamble, forbidden) {
			t.Errorf("the preamble carries %q, which belongs in the stage after the declaration: %s",
				forbidden, preamble)
		}
	}

	material := stdin.line(t, MessageJob)
	for _, wanted := range []string{testToken, "omp/synthetic-session", string(CapabilityCorpusSearch)} {
		if !strings.Contains(material, wanted) {
			t.Errorf("the job material is missing %q, so the run could not have been performed: %s",
				wanted, material)
		}
	}
}

// TestRefusedDeclarationWithholdsTheCredential is the closure of the gap
// atyrode/babel#71 recorded. A worker that declares a boundary short of the
// run's requirement is refused, and the assertion is on the bytes it was
// actually sent: the run-scoped credential, the corpus selection and the grant
// were never written to it at all, which is the difference between a bounded
// exposure and none.
func TestRefusedDeclarationWithholdsTheCredential(t *testing.T) {
	for _, containment := range []string{"weak", "none", "insufficient"} {
		t.Run(containment, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "stdin.jsonl")
			f := newFixture(ConformanceWellBehaved)
			f.args = []string{"-containment", containment, "-record", record}

			receipt, err := f.run(t)
			if !errors.Is(err, ErrContainment) {
				t.Fatalf("Run error = %v, want ErrContainment", err)
			}
			if receipt.Result != nil {
				t.Errorf("a refused run produced a result: %+v", receipt.Result)
			}

			stdin := readWorkerStdin(t, record)
			want := []string{MessageAccept, MessageJobPreamble, MessageRefuse}
			if !slices.Equal(stdin.types, want) {
				t.Fatalf("Babel wrote %v, want %v: the material must not follow a refused declaration\n%s",
					stdin.types, want, stdin.raw)
			}
			if refusal := stdin.line(t, MessageRefuse); !strings.Contains(refusal, "containment") {
				t.Errorf("the refusal does not tell the worker what was wrong: %s", refusal)
			}
			for _, forbidden := range []string{
				testToken,
				"omp/synthetic-session",
				string(CapabilityCorpusSearch),
				"outcome-integrity",
			} {
				if strings.Contains(stdin.raw, forbidden) {
					t.Errorf("a worker whose containment was refused received %q:\n%s", forbidden, stdin.raw)
				}
			}
		})
	}
}

// TestDeclarationMustAnswerThePreamble covers the two ways a worker can break
// the staging from its own side: writing something else where its declaration
// belongs, and refusing to declare until it has been given the material. Both
// end the run with the credential unwritten, and each has its own error because
// they need different fixes — one is a worker emitting events too early, the
// other is a worker built against version 1's ordering.
func TestDeclarationMustAnswerThePreamble(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		limits   func(*Limits)
		wantErr  error
		wantText string
		wantSent []string
	}{
		{
			name:     "progress in place of the declaration",
			args:     []string{"-declare-late", MessageProgress},
			wantErr:  ErrEventOrder,
			wantText: "progress before the resolved configuration",
			wantSent: []string{MessageAccept, MessageJobPreamble, MessageRefuse},
		},
		{
			name:     "a result before anything was declared",
			args:     []string{"-declare-late", MessageResult},
			wantErr:  ErrEventOrder,
			wantText: "result before the resolved configuration",
			wantSent: []string{MessageAccept, MessageJobPreamble, MessageRefuse},
		},
		{
			name:     "waits for the material before declaring",
			args:     []string{"-await-material"},
			limits:   func(l *Limits) { l.IdleTimeout = 1500 * time.Millisecond },
			wantErr:  ErrWorkerStalled,
			wantText: "no event for",
			// No refusal: nothing was declared to refuse, and the worker is
			// not listening for an answer to a message it never sent.
			wantSent: []string{MessageAccept, MessageJobPreamble},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "stdin.jsonl")
			f := newFixture(ConformanceWellBehaved)
			f.args = append(test.args, "-record", record)
			if test.limits != nil {
				test.limits(&f.limits)
			}

			receipt, err := f.run(t)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Errorf("error %q does not say what was out of order", err)
			}
			if receipt.Result != nil {
				t.Errorf("a run that never declared containment produced a result: %+v", receipt.Result)
			}

			stdin := readWorkerStdin(t, record)
			if !slices.Equal(stdin.types, test.wantSent) {
				t.Fatalf("Babel wrote %v, want %v:\n%s", stdin.types, test.wantSent, stdin.raw)
			}
			if strings.Contains(stdin.raw, testToken) {
				t.Errorf("the run's credential reached a worker that declared nothing:\n%s", stdin.raw)
			}
		})
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

// gradeObligation runs exactly one contract obligation through the recorder
// `babel conformance WORKER` grades with, so a test can assert a verdict
// without paying for the whole suite. A name no obligation answers to is a
// failure rather than a skip: the contract list must not lose an item without
// a test noticing.
func gradeObligation(t *testing.T, name string, args ...string) ObligationResult {
	t.Helper()
	for _, obligation := range conformanceObligations(conformanceTarget{binary: fakeWorkerPath, args: args}) {
		if obligation.name == name {
			return runObligation(obligation)
		}
	}
	t.Fatalf("no obligation named %q in the contract list", name)
	return ObligationResult{}
}

// TestCredentialLeakObligationPassesAWorkerWithOutputDiscipline grades the
// obligation an operator sees in `babel conformance WORKER` against a worker
// that answers the echo-token directive the conforming way: it reports the
// credential where the directive asks for it, reports its own placeholder
// rather than the token, and finishes the job.
//
// The placeholder is the fixture's own string, not Babel's redaction marker.
// A grader that recognized Babel's marker would be grading Babel; this one
// must pass a worker whose discipline is entirely its own.
func TestCredentialLeakObligationPassesAWorkerWithOutputDiscipline(t *testing.T) {
	result := gradeObligation(t, "run/no-credential-leak")
	if !result.Passed {
		t.Errorf("run/no-credential-leak failed a worker that answered the directive without ever writing the token: %s",
			strings.Join(result.Failures, "; "))
	}
}

// TestCredentialLeakObligationFailsAWorkerThatWritesTheToken is the negative
// control that makes the obligation non-vacuous. The same worker, the same
// directive, one difference: -echo-token puts the credential verbatim on its
// stdout and stderr.
//
// Babel scrubs it out of everything it stores, so the receipt is identical to
// the disciplined worker's — which is exactly why the obligation reads the
// bytes the worker wrote instead. If that capture is ever lost, this test
// fails, and it fails for the right reason: the failure message must name the
// worker's own output, not the receipt.
func TestCredentialLeakObligationFailsAWorkerThatWritesTheToken(t *testing.T) {
	result := gradeObligation(t, "run/no-credential-leak", "-echo-token")
	if result.Passed {
		t.Fatal("run/no-credential-leak passed a worker that wrote the broker credential to its own stdout and stderr")
	}
	messages := strings.Join(result.Failures, "; ")
	if !strings.Contains(messages, "stdout or stderr") {
		t.Errorf("the failure does not attribute the leak to the worker's own output, so it names the wrong remedy: %s", messages)
	}
	if strings.Contains(messages, conformanceToken) {
		t.Errorf("the failure message quotes the credential it is complaining about: %s", messages)
	}
}

// TestWellBehavedObligationFailsAnUnreadableResultSchema covers the drift a
// conformance report would otherwise miss entirely: a worker that satisfies
// every other obligation while declaring a result schema Babel cannot read.
// Such a run is graded 14 of 14 and still delivers nothing, because
// internal/explore refuses a payload under an unknown schema rather than
// parsing it hopefully.
//
// The schema string is wire surface shared with a separately-developed worker,
// so the failure has to name both values — a report saying only "wrong schema"
// leaves the reader to guess which side moved.
func TestWellBehavedObligationFailsAnUnreadableResultSchema(t *testing.T) {
	const declared = "babel.analysis-result/99"
	result := gradeObligation(t, "run/well-behaved", "-result-schema", declared)
	if result.Passed {
		t.Fatalf("run/well-behaved passed a worker declaring %q; Babel cannot read that payload", declared)
	}
	messages := strings.Join(result.Failures, "; ")
	if !strings.Contains(messages, declared) || !strings.Contains(messages, ResultSchema) {
		t.Errorf("the failure names neither what the worker declared nor what this build requires: %s", messages)
	}
}

// TestJobDecodeObligationSeesWhetherTheWorkerReadTheJob is the obligation the
// receipt cannot supply. Receipt.Recipes and Receipt.Sources are copied from
// Babel's own outgoing job, so a worker that never looked at either array
// leaves a receipt identical to one that honoured both; the only evidence of
// the counterpart's reading is what the counterpart says.
//
// The negative fixture is the interesting half. -wrong-job answers with the
// conformance job published in conformance.go — a plausible, well-formed,
// entirely correct-looking reply that a candidate could write without ever
// parsing a byte. It fails only because the obligation plants a per-run nonce
// in the material it asks about, so this test is what proves the nonce is
// load-bearing rather than decorative.
func TestJobDecodeObligationSeesWhetherTheWorkerReadTheJob(t *testing.T) {
	t.Run("a worker that decoded the job", func(t *testing.T) {
		result := gradeObligation(t, "run/decodes-the-job")
		if !result.Passed {
			t.Errorf("run/decodes-the-job failed a worker that reported the recipes and sources it was given: %s",
				strings.Join(result.Failures, "; "))
		}
	})

	t.Run("a worker answering from the published fixture", func(t *testing.T) {
		result := gradeObligation(t, "run/decodes-the-job", "-wrong-job")
		if result.Passed {
			t.Fatal("run/decodes-the-job passed a worker that answered with a hardcoded job description; the per-run nonce is not reaching the material the obligation grades")
		}
		messages := strings.Join(result.Failures, "; ")
		// Both arrays are wrong, and the report has to name both: a worker
		// told only about its recipes would fix those and fail again on the
		// sources.
		for _, want := range []string{"recipe", "source"} {
			if !strings.Contains(messages, want) {
				t.Errorf("the failure does not mention the %s the worker misreported: %s", want, messages)
			}
		}
	})

	t.Run("a worker that never answers the directive", func(t *testing.T) {
		// The likeliest candidate of all: a run that is correct in every
		// other respect and simply does not implement echo-job. The failure
		// has to say so, because "no job object" and "the wrong job object"
		// send an implementer to different code.
		payload := filepath.Join(t.TempDir(), "payload.json")
		if err := os.WriteFile(payload, []byte(`{"hypotheses":[]}`), 0o600); err != nil {
			t.Fatalf("writing the payload fixture: %v", err)
		}
		result := gradeObligation(t, "run/decodes-the-job", "-result-payload", payload)
		if result.Passed {
			t.Fatal("run/decodes-the-job passed a worker that answered nothing; an obligation satisfied by silence certifies nothing")
		}
		if messages := strings.Join(result.Failures, "; "); !strings.Contains(messages, `no "job" object`) {
			t.Errorf("the failure does not say the answer is missing rather than wrong: %s", messages)
		}
	})
}

// TestServedEvidenceObligationSeesWhetherTheWorkerReadIt is the obligation for
// the defect that made retrieval useless. Babel's evidence broker computed
// hits, redacted them, recorded their locators, and discarded them: every tool
// decision was an adjudication with no evidence attached, so the whole of what
// a worker learned from an allowed corpus search was the sentence "served N
// hits from the corpus index". The first real exploration retrieved four times,
// was allowed four times, and wrote no hypothesis, no observation and no
// finding.
//
// Once the payload exists, a worker that ignores it is indistinguishable from
// one that received none — from Babel's side there is nothing to see, because
// the receipt records the decision and deliberately never the payload. So the
// worker is asked, and the four cases here are what make the asking worth
// anything.
func TestServedEvidenceObligationSeesWhetherTheWorkerReadIt(t *testing.T) {
	t.Run("a worker that read the served evidence", func(t *testing.T) {
		result := gradeObligation(t, "run/consumes-served-evidence")
		if !result.Passed {
			t.Errorf("run/consumes-served-evidence failed a worker that reported the hits its decision carried: %s",
				strings.Join(result.Failures, "; "))
		}
	})

	t.Run("a worker answering from a constant", func(t *testing.T) {
		// The same shape -wrong-job models one concept over: a plausible,
		// well-formed answer a candidate could write from the documentation
		// without ever decoding a decision. It fails only because the
		// obligation weaves a per-run nonce through the material, so this is
		// what proves the nonce is load-bearing here too.
		result := gradeObligation(t, "run/consumes-served-evidence", "-wrong-evidence")
		if result.Passed {
			t.Fatal("run/consumes-served-evidence passed a worker answering with a hardcoded hit; the per-run nonce is not reaching the material the obligation grades")
		}
	})

	t.Run("a worker that reports its request instead of the answer", func(t *testing.T) {
		// The failure that actually happened. Before the payload existed a
		// request was all a worker ever had, so a worker built against that
		// Babel reports the query it sent and calls it evidence.
		result := gradeObligation(t, "run/consumes-served-evidence", "-ignore-evidence")
		if result.Passed {
			t.Fatal("run/consumes-served-evidence passed a worker that echoed its own query back; a worker that never read the answer has not consumed any evidence")
		}
	})

	t.Run("a worker that never answers the directive", func(t *testing.T) {
		// The likeliest candidate: correct in every other respect and simply
		// does not implement echo-evidence. "No served_evidence object" and
		// "the wrong hits" send an implementer to different code, so the
		// failure has to distinguish them.
		payload := filepath.Join(t.TempDir(), "payload.json")
		if err := os.WriteFile(payload, []byte(`{"hypotheses":[]}`), 0o600); err != nil {
			t.Fatalf("writing the payload fixture: %v", err)
		}
		result := gradeObligation(t, "run/consumes-served-evidence", "-result-payload", payload)
		if result.Passed {
			t.Fatal("run/consumes-served-evidence passed a worker that answered nothing; an obligation satisfied by silence certifies nothing")
		}
		if messages := strings.Join(result.Failures, "; "); !strings.Contains(messages, `no "served_evidence" object`) {
			t.Errorf("the failure does not say the answer is missing rather than wrong: %s", messages)
		}
	})
}

// TestPublishedToolNameObligationCatchesAnInventedName is the regression test
// for the failure that closed this class.
//
// A worker requested "babel_corpus_search" — a name it had chosen for itself,
// which existed nowhere in Babel. It scored 14 of 14 and was then denied on
// every request of the first real exploration Babel ever drove, producing no
// evidence at all. The suite could not see it because the policy it graded with
// never inspected a tool name while the policy production installs always had.
//
// The three cases are the three things that must be true for the obligation to
// be worth having: it passes a worker that used the published name, it passes a
// worker that arrived at that name without reading the mapping, and it fails
// the exact name that failed in reality — naming both what was asked for and
// what was published, because an implementer told only "denied" learns nothing.
func TestPublishedToolNameObligationCatchesAnInventedName(t *testing.T) {
	t.Run("a worker that read the published mapping", func(t *testing.T) {
		result := gradeObligation(t, "run/published-tool-names")
		if !result.Passed {
			t.Errorf("run/published-tool-names failed a worker using the name the job published: %s",
				strings.Join(result.Failures, "; "))
		}
	})

	t.Run("a worker that guessed the published name", func(t *testing.T) {
		// How a worker arrived at the name is not the contract. A hardcoded
		// name that happens to be the served one is a worker that works, and
		// an obligation failing it would be grading source code rather than
		// behaviour.
		result := gradeObligation(t, "run/published-tool-names", "-tool-name", ToolSearch)
		if !result.Passed {
			t.Errorf("run/published-tool-names failed a worker whose hardcoded name is the served one: %s",
				strings.Join(result.Failures, "; "))
		}
	})

	t.Run("a worker that invented its own name", func(t *testing.T) {
		const invented = "babel_corpus_search"
		result := gradeObligation(t, "run/published-tool-names", "-tool-name", invented)
		if result.Passed {
			t.Fatalf("run/published-tool-names passed a worker requesting %q, which Babel serves for nothing; this is the run that produced zero retrievals at 14/14",
				invented)
		}
		messages := strings.Join(result.Failures, "; ")
		for _, want := range []string{invented, ToolSearch, string(CapabilityCorpusSearch)} {
			if !strings.Contains(messages, want) {
				t.Errorf("the failure does not mention %q, so it does not say what to change: %s", want, messages)
			}
		}
	})
}

// TestProfileObligationRequiresWhatBabelReadsByName covers the drift class the
// conformance suite was blind to: a worker whose resolved configuration is
// structurally perfect and semantically unusable. Every case below produces a
// run that satisfies every other obligation.
//
// Each case names the remedy in its own failure, because these are four
// different mistakes — a key Babel reads under another name, a capability
// vocabulary that drifted out of Babel's, a profile that omits what the run
// actually did, and a price with no unit — and a report that only said "bad
// configuration" would send the reader looking.
func TestProfileObligationRequiresWhatBabelReadsByName(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "well behaved",
			args: nil,
		},
		{
			// internal/run reads "model" by name; "model_name" is a receipt
			// with no model in it.
			name: "the model under a key Babel does not read",
			args: []string{"-rename-metadata"},
			want: []string{`"model"`},
		},
		{
			// Babel denies a capability it cannot name, so a profile
			// declaring one has told an operator it can do something no run
			// will ever authorize. The exercised capability is spelled
			// correctly here, so this case grades the vocabulary alone.
			name: "a capability under a drifted vocabulary",
			args: []string{"-drift-capability"},
			want: []string{"repo_read"},
		},
		{
			// Every name is one Babel defines; the claim is simply not the
			// profile that ran, and Babel watched it run.
			name: "a profile that omits what the run did",
			args: []string{"-hide-capability"},
			want: []string{"corpus-search"},
		},
		{
			// One unusable cost report, two symptoms. Babel shows an estimate
			// only when it has a unit for it, so an unnamed currency drops
			// the figure entirely; a negative rate reads as a discount. The
			// report has to name both, or fixing one leaves the other.
			name: "a cost report Babel cannot act on",
			args: []string{"-unusable-cost"},
			want: []string{"currency", "negative figure"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := gradeObligation(t, "run/declares-profile", test.args...)
			messages := strings.Join(result.Failures, "; ")
			if len(test.want) == 0 {
				if !result.Passed {
					t.Errorf("run/declares-profile failed a worker whose configuration carries everything Babel reads: %s", messages)
				}
				return
			}
			if result.Passed {
				t.Fatalf("run/declares-profile passed a worker run with %v; Babel's own consumers cannot read that configuration", test.args)
			}
			for _, want := range test.want {
				if !strings.Contains(messages, want) {
					t.Errorf("the failure does not name %q, so it does not name the remedy: %s", want, messages)
				}
			}
		})
	}
}

// TestResourceObligationGradesOnlyWhatBabelCanCheck pins both halves of the
// resource contract, which is the one part of a receipt Babel copies from the
// counterpart's word.
//
// -no-resources is a worker that declares resource ceilings and then reports
// nothing: reporting nothing is honest for an implementation that measures
// nothing, but not for one that has just claimed it bounds itself, because a
// bound is enforced by measuring what it bounds.
//
// -untracked-resources is the other failure and the more tempting one: an
// implementation with no accounting that reports anyway, with -1 standing in
// for "unknown" and a tool-call count it never kept. Babel answered the tool
// request itself, so that last figure is the one number in the object it can
// contradict without trusting anybody.
func TestResourceObligationGradesOnlyWhatBabelCanCheck(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "well behaved",
			args: nil,
		},
		{
			name: "ceilings declared, nothing measured",
			args: []string{"-no-resources"},
			want: []string{"resource ceilings"},
		},
		{
			name: "invented figures",
			args: []string{"-untracked-resources"},
			want: []string{"negative figure", "tool_calls"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := gradeObligation(t, "run/reports-resources", test.args...)
			messages := strings.Join(result.Failures, "; ")
			if len(test.want) == 0 {
				if !result.Passed {
					t.Errorf("run/reports-resources failed a worker whose self-report matches what Babel observed: %s", messages)
				}
				return
			}
			if result.Passed {
				t.Fatalf("run/reports-resources passed a worker run with %v; its self-report contradicts the run Babel supervised", test.args)
			}
			for _, want := range test.want {
				if !strings.Contains(messages, want) {
					t.Errorf("the failure does not name %q: %s", want, messages)
				}
			}
		})
	}
}

// TestRenderedReceiptCoversTheNestedRecords is what keeps the credential search
// from being vacuous in the other direction. A receipt's result, failure and
// resources are pointers, and fmt's %+v renders a pointer as an address — a
// search over that text would skip the result payload entirely, which is the
// first place a leaking worker puts a credential. renderReceipt encodes the
// whole tree instead, and this test fails if anyone simplifies it back.
func TestRenderedReceiptCoversTheNestedRecords(t *testing.T) {
	receipt, err := newFixture(ConformanceRequestTool).run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Result == nil || len(receipt.Result.Payload) == 0 {
		t.Fatal("the fixture produced no result payload, so this test would prove nothing")
	}
	rendered := renderReceipt(receipt)
	if !strings.Contains(rendered, decisionAllow) {
		t.Errorf("the rendered receipt does not contain the result payload %s:\n%s",
			receipt.Result.Payload, rendered)
	}
	for _, want := range []string{receipt.RunID, string(receipt.Result.Status)} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered receipt does not contain %q:\n%s", want, rendered)
		}
	}
}

// TestRawTranscriptIsOffLimitsToProduction pins the two properties that make an
// unscrubbed capture safe to have at all: a run that does not ask for one keeps
// nothing, and the obligation that does ask releases it when it is done.
//
// The field is unexported, so no caller outside this package can request the
// worker's credential-bearing bytes. What a test can still check is that the
// zero value means no capture — otherwise the safe default would depend on
// every caller remembering to leave a field alone.
func TestRawTranscriptIsOffLimitsToProduction(t *testing.T) {
	// newFixture builds its Config the way a production caller does, so the
	// safe default must not depend on anyone remembering to leave a field
	// alone.
	if client := newFixture(ConformanceWellBehaved).client(t); client.cfg.rawTranscript != nil {
		t.Error("a Config built without the conformance suite carries a raw transcript; the zero value must mean no capture")
	}

	// The obligation's own capture is bounded and released. discard is what
	// the obligation defers, so a credential does not outlive the grading.
	captured := &tail{limit: rawTranscriptBytes}
	captured.writeLine("worker wrote " + testToken)
	if !strings.Contains(captured.String(), testToken) {
		t.Fatal("the transcript captured nothing, so the obligation would search an empty string")
	}
	captured.discard()
	if captured.String() != "" {
		t.Errorf("the transcript still holds the credential after discard: %s", captured.String())
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

// TestJobEncodingIsTheDocumentedShape pins the wire format of the two messages
// Babel authors that carry the run's whole boundary. Code reads these exact
// names, so a rename here is a break there — and which name is in which stage
// is not a detail either: the preamble is the message Babel writes before the
// worker has declared anything, so a field that moved into it would be
// material sent to a worker that has not earned it yet.
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

	preamble := decodeStage(t, job.encodePreamble)
	if preamble["type"] != MessageJobPreamble || preamble["protocol"] != ProtocolName {
		t.Errorf("preamble header = %v/%v", preamble["type"], preamble["protocol"])
	}
	// The whole preamble, named exhaustively rather than as a subset: a field
	// that appears here without this test being changed is material that
	// started travelling before the declaration, which is the defect the
	// staging exists to prevent and the one a "contains at least" assertion
	// would never see.
	wantPreamble := []string{"type", "protocol", "job_id", "run_id", "profile", "params"}
	for _, name := range wantPreamble {
		if _, ok := preamble[name]; !ok {
			t.Errorf("the preamble has no %q field: %v", name, preamble)
		}
	}
	for name := range preamble {
		if !slices.Contains(wantPreamble, name) {
			t.Errorf("the preamble carries %q, which belongs in the material stage: a worker that has declared nothing yet must not be sent it", name)
		}
	}
	if profile, _ := preamble["profile"].(map[string]any); profile["id"] != "p-1" {
		t.Errorf("preamble profile = %v, want the profile the run named", preamble["profile"])
	}

	decoded := decodeStage(t, job.encodeMaterial)
	if decoded["type"] != MessageJob || decoded["protocol"] != ProtocolName {
		t.Errorf("material header = %v/%v", decoded["type"], decoded["protocol"])
	}
	for _, name := range []string{
		"type", "protocol", "job_id", "run_id", "recipes", "grant", "sources", "broker",
	} {
		if _, ok := decoded[name]; !ok {
			t.Errorf("the job material has no %q field: %v", name, decoded)
		}
	}
	// The run's identity travels in both stages so each line is
	// self-identifying; nothing else is sent twice.
	if decoded["job_id"] != preamble["job_id"] || decoded["run_id"] != preamble["run_id"] {
		t.Errorf("the two stages name different runs: %v/%v then %v/%v",
			preamble["job_id"], preamble["run_id"], decoded["job_id"], decoded["run_id"])
	}
	for _, name := range []string{"profile", "params"} {
		if _, present := decoded[name]; present {
			t.Errorf("the job material repeats %q from the preamble; one field, one stage", name)
		}
	}
	grant, _ := decoded["grant"].(map[string]any)
	if grant["disclosure"] != DisclosureLocal {
		t.Errorf("grant disclosure = %v", grant["disclosure"])
	}
	if grant["expires"] != "2026-08-29T12:00:00Z" {
		t.Errorf("grant expires = %v, want RFC 3339 UTC", grant["expires"])
	}
	tools, _ := grant["tools"].(map[string]any)
	if len(tools) != 1 {
		t.Fatalf("grant tools = %v, want exactly the one granted capability this build serves", tools)
	}
	served, _ := tools[string(CapabilityCorpusSearch)].([]any)
	if len(served) != 1 || served[0] != ToolSearch {
		t.Errorf("grant tools for %s = %v, want [%q]", CapabilityCorpusSearch, served, ToolSearch)
	}

	// Absence is the representation for a capability nothing brokers. An empty
	// array would encode the same claim in a shape that also reads as "not
	// published yet", and only one of those is ever true.
	t.Run("an unserved capability gets no key", func(t *testing.T) {
		unserved := job
		unserved.Grant.Capabilities = []Capability{CapabilityCorpusSearch, CapabilityRepoRead}
		encoded, err := unserved.encodeMaterial()
		if err != nil {
			t.Fatalf("encodeMaterial: %v", err)
		}
		var decoded struct {
			Grant struct {
				Tools map[string][]string `json:"tools"`
			} `json:"grant"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if _, present := decoded.Grant.Tools[string(CapabilityRepoRead)]; present {
			t.Errorf("granted-but-unserved %s appears in the published mapping: %v; absence is how this build says nothing brokers it",
				CapabilityRepoRead, decoded.Grant.Tools)
		}
		if got := decoded.Grant.Tools[string(CapabilityCorpusSearch)]; len(got) != 1 || got[0] != ToolSearch {
			t.Errorf("served %s = %v, want [%q]", CapabilityCorpusSearch, got, ToolSearch)
		}
	})

	t.Run("a grant reaching nothing served publishes no object", func(t *testing.T) {
		none := job
		none.Grant.Capabilities = []Capability{CapabilitySandboxExec}
		encoded, err := none.encodeMaterial()
		if err != nil {
			t.Fatalf("encodeMaterial: %v", err)
		}
		if strings.Contains(string(encoded), `"tools"`) {
			t.Errorf("a grant whose capabilities are all unserved still published a tools object: %s", encoded)
		}
	})

	// Each stage merges its own forward-compatible fields, and only its own.
	// One map for both would mean a field a newer Babel adds to the material
	// also travelling in the preamble, which is the one thing the preamble
	// promises about itself.
	t.Run("extra fields merge into their own stage", func(t *testing.T) {
		staged := job
		staged.PreambleExtra = map[string]json.RawMessage{"x-future-preamble": json.RawMessage(`1`)}
		staged.Extra = map[string]json.RawMessage{"x-future": json.RawMessage(`2`)}

		preamble, err := staged.encodePreamble()
		if err != nil {
			t.Fatalf("encodePreamble: %v", err)
		}
		if !strings.Contains(string(preamble), `"x-future-preamble":1`) {
			t.Errorf("the preamble's extra field was not merged: %s", preamble)
		}
		if strings.Contains(string(preamble), `"x-future":2`) {
			t.Errorf("a material extra field travelled in the preamble: %s", preamble)
		}

		material, err := staged.encodeMaterial()
		if err != nil {
			t.Fatalf("encodeMaterial: %v", err)
		}
		if !strings.Contains(string(material), `"x-future":2`) {
			t.Errorf("the material's extra field was not merged: %s", material)
		}
		if strings.Contains(string(material), `"x-future-preamble":1`) {
			t.Errorf("a preamble extra field travelled in the material: %s", material)
		}
	})

	t.Run("extra fields may not shadow the contract", func(t *testing.T) {
		shadowed := job
		shadowed.Extra = map[string]json.RawMessage{"grant": json.RawMessage(`{}`)}
		if _, err := shadowed.encodeMaterial(); err == nil {
			t.Error("an Extra field silently replaced the capability grant")
		}
		shadowed = job
		shadowed.PreambleExtra = map[string]json.RawMessage{"profile": json.RawMessage(`{}`)}
		if _, err := shadowed.encodePreamble(); err == nil {
			t.Error("a PreambleExtra field silently replaced the profile the worker declares against")
		}
	})
}

// decodeStage runs one stage's encoder and decodes it into the generic object a
// worker sees, failing the test on an encoder error so every caller reads as
// one expression.
func decodeStage(t *testing.T, encode func() ([]byte, error)) map[string]any {
	t.Helper()
	encoded, err := encode()
	if err != nil {
		t.Fatalf("encoding a job stage: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding %s: %v", encoded, err)
	}
	return decoded
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

// TestInsufficientContainmentIsRefused is the enforcement behind decision 53.
// Babel cannot verify a sandbox from outside the process, so its leverage is
// refusing to proceed against a declaration that falls short. Each case is a
// different operator situation: a worker that claims nothing, one that claims a
// boundary weaker than the run needs, and one that claims containment while
// stating no residual risk.
func TestInsufficientContainmentIsRefused(t *testing.T) {
	tests := []struct {
		name     string
		flag     string
		wantText string
	}{
		{name: "declares nothing", flag: "none", wantText: "declared no sandbox backend"},
		{name: "weaker than the run requires", flag: "weak", wantText: "network default-deny"},
		{name: "no escape assumption", flag: "no-escape", wantText: "no escape assumption"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(ConformanceWellBehaved)
			f.args = append(f.args, "-containment", tc.flag)
			receipt, err := f.run(t)
			if !errors.Is(err, ErrContainment) {
				t.Fatalf("Run error = %v, want ErrContainment", err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not name the shortfall %q", err, tc.wantText)
			}
			// A refused run still yields a receipt: an operator needs to see
			// what was declared in order to fix it.
			if receipt.Result != nil {
				t.Error("a refused run produced a result")
			}
		})
	}
}

// TestSandboxedRunIsTheDefault proves a caller who sets no requirement gets the
// strict one. That is what makes a forgotten field safe rather than silently
// permissive, and it is the property most likely to erode later.
func TestSandboxedRunIsTheDefault(t *testing.T) {
	client, err := New(Config{Binary: fakeWorkerPath, Authorizer: AllowWithinGrant()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := client.requirement(), SandboxedRun(); got != want {
		t.Errorf("default requirement = %+v, want %+v", got, want)
	}
}

// TestUnsandboxedStillRequiresANamedBackend keeps the escape hatch from
// becoming a way to run against nothing at all. Relaxing which properties are
// required is a legitimate operator choice; declining to say what mechanism is
// in use is not, because a receipt that names no boundary cannot tell a
// reviewer what the evidence was produced behind.
func TestUnsandboxedStillRequiresANamedBackend(t *testing.T) {
	unsandboxed := Unsandboxed()
	client, err := New(Config{
		Binary:      fakeWorkerPath,
		Args:        []string{"-containment", "none"},
		Authorizer:  AllowWithinGrant(),
		Requirement: &unsandboxed,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Run(context.Background(), newFixture(ConformanceWellBehaved).job); !errors.Is(err, ErrContainment) {
		t.Fatalf("Run error = %v, want ErrContainment naming the missing backend", err)
	}
}
