package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ParamConformance is the job parameter through which Babel's conformance
// suite tells a worker which obligation is being exercised.
//
// It is part of the contract rather than a testing hack: several obligations —
// that a denial does not end a run, that no result follows an error, that
// cancellation is prompt — cannot be observed unless the worker can be asked
// to reach that state. A production job never sets this key, and a worker must
// treat an unrecognized value as ConformanceWellBehaved.
const ParamConformance = "babel.conformance"

// Conformance directives. A worker that intends to pass Conformance must
// implement each one.
const (
	// ConformanceWellBehaved runs a minimal successful analysis: the resolved
	// configuration, at least one progress event, then a result.
	ConformanceWellBehaved = "well-behaved"

	// ConformanceRequestTool makes exactly one tool request for
	// CapabilityCorpusSearch, waits for the decision, records it in the
	// result payload, and finishes with a result whichever way it was
	// decided.
	ConformanceRequestTool = "request-tool"

	// ConformanceRequestUngranted makes one request for
	// CapabilitySandboxExec, which the conformance job does not grant. The
	// worker must survive the denial and still deliver a result.
	ConformanceRequestUngranted = "request-ungranted"

	// ConformanceErrorOnly emits one error event and then exits, emitting no
	// result.
	ConformanceErrorOnly = "error-only"

	// ConformanceSlow emits progress and then keeps working long enough to be
	// cancelled.
	ConformanceSlow = "slow"
)

// conformanceToken is the synthetic run-scoped broker credential the suite
// plants in every job. Conformance asserts it never comes back in a receipt or
// an error string. It is long and non-dictionary so a substring search cannot
// match anything a worker emits by coincidence.
const conformanceToken = "BROKERTOKEN4f19c8d3a72b6e05f8341ca9"

// Conformance drives the worker executable at workerPath through every
// obligation the analysis-worker protocol places on the counterpart.
//
// It is exported because it is the contract, not a private test: Code — or any
// other implementation of this protocol — should call it from its own test
// suite against its own binary. Babel's side is exercised by the same code, so
// the two cannot drift apart silently.
//
// Each subtest is one obligation, and the subtest name is the contract item:
//
//	handshake/accept              hello first, then a configuration, then exit
//	handshake/refuse              a refused worker exits without a job
//	run/well-behaved              configuration, progress, result, exit 0
//	run/forward-compatible-job    unknown job fields are ignored, not fatal
//	run/tool-allow                a tool request blocks for its decision
//	run/tool-denial-continues     a denial does not end the run
//	run/grant-boundary            an ungranted capability is denied
//	run/error-is-terminal         no result may follow an error
//	run/cancellation              cancellation ends the run promptly
//	run/no-credential-leak        the broker token never returns to Babel
//
// It requires no network, no credential and no transcript.
func Conformance(t *testing.T, workerPath string) {
	t.Helper()

	t.Run("handshake/accept", func(t *testing.T) {
		client := conformanceClient(t, workerPath, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cfg, err := client.Configure(ctx)
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if cfg.Profile.ID == "" {
			t.Error("configuration reports no profile ID; Babel has nothing stable to persist")
		}
		if cfg.Profile.Revision <= 0 {
			t.Errorf("profile revision = %d, want a positive revision", cfg.Profile.Revision)
		}
		switch cfg.Privacy.Disclosure {
		case DisclosureLocal, DisclosureHosted:
		default:
			t.Errorf("privacy disclosure = %q, want %q or %q",
				cfg.Privacy.Disclosure, DisclosureLocal, DisclosureHosted)
		}
		if len(cfg.Metadata) == 0 {
			t.Error("configuration reports no resolved metadata; a receipt requires provider/model metadata")
		}
		if cfg.Worker.Name == "" {
			t.Error("hello reports no worker name; a run cannot be attributed to a build")
		}
		if cfg.ProtocolVersion != ProtocolVersion {
			t.Errorf("negotiated version = %d, want %d", cfg.ProtocolVersion, ProtocolVersion)
		}
	})

	t.Run("handshake/refuse", func(t *testing.T) {
		// Babel offers a version no worker can support, so the worker must be
		// refused. The refusal is written to it, and it must exit rather than
		// wait for a job that will never arrive.
		client := conformanceClient(t, workerPath, func(cfg *Config) {
			cfg.Versions = []int{ProtocolVersion + 1_000_000}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		started := time.Now()
		_, err := client.Configure(ctx)
		if !errors.Is(err, ErrVersionMismatch) {
			t.Fatalf("Configure error = %v, want ErrVersionMismatch", err)
		}
		if errors.Is(err, ErrWorkerLingered) {
			t.Error("the refused worker did not exit; it must not wait for a job it will never be sent")
		}
		if elapsed := time.Since(started); elapsed > 20*time.Second {
			t.Errorf("refusal took %s; a refused worker must exit promptly", elapsed)
		}
	})

	t.Run("run/well-behaved", func(t *testing.T) {
		receipt, err := conformanceRun(t, workerPath, ConformanceWellBehaved, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("run produced no result record")
		}
		if receipt.Result.Status != StatusOK && receipt.Result.Status != StatusPartial {
			t.Errorf("result status = %q", receipt.Result.Status)
		}
		if receipt.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0 after a result", receipt.ExitCode)
		}
		if len(receipt.Metadata) == 0 {
			t.Error("receipt carries no resolved provider metadata")
		}
		if receipt.Profile.ID == "" || receipt.ProtocolVersion == 0 {
			t.Errorf("receipt is missing its profile or protocol version: %+v", receipt.Profile)
		}
		if receipt.Duration <= 0 {
			t.Error("receipt records no duration")
		}
		if len(receipt.Progress) == 0 {
			t.Error("no progress event was recorded; Babel cannot keep an interface responsive without them")
		}
		if len(receipt.Recipes) == 0 || len(receipt.Sources) == 0 {
			t.Error("the receipt does not record which recipes ran over which sources")
		}
		if receipt.Failure != nil {
			t.Errorf("a successful run recorded a failure: %+v", receipt.Failure)
		}
	})

	t.Run("run/forward-compatible-job", func(t *testing.T) {
		// A newer Babel adds a field; an older worker must ignore it rather
		// than fail. Nothing else about the run changes.
		receipt, err := conformanceRun(t, workerPath, ConformanceWellBehaved, AllowWithinGrant(),
			func(job *Job) {
				job.Extra = map[string]json.RawMessage{
					"x-babel-future": json.RawMessage(`{"unknown":"to this worker"}`),
				}
			})
		if err != nil {
			t.Fatalf("Run with an unknown job field: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("an unknown job field prevented a result")
		}
	})

	t.Run("run/tool-allow", func(t *testing.T) {
		receipt, err := conformanceRun(t, workerPath, ConformanceRequestTool, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if !request.Allowed {
			t.Fatalf("request was denied: %s %s", request.DenyCode, request.Reason)
		}
		if request.RequestID == "" {
			t.Error("tool request carries no request_id, so it could not have been answered")
		}
		if request.ArgumentsDigest == "" {
			t.Error("tool request recorded no argument digest")
		}
		if receipt.Result == nil {
			t.Fatal("run produced no result after an allowed tool request")
		}
		if !strings.Contains(string(receipt.Result.Payload), decisionAllow) {
			t.Errorf("result payload does not report the decision the worker received: %s",
				receipt.Result.Payload)
		}
	})

	t.Run("run/tool-denial-continues", func(t *testing.T) {
		receipt, err := conformanceRun(t, workerPath, ConformanceRequestTool,
			DenyAll("conformance: policy denies this request"))
		if err != nil {
			t.Fatalf("Run after a denial: %v; a denial must not end the run", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if request.Allowed {
			t.Fatal("the request was allowed; the injected policy denies everything")
		}
		if request.DenyCode != DenyPolicy {
			t.Errorf("deny code = %q, want %q", request.DenyCode, DenyPolicy)
		}
		if receipt.Result == nil {
			t.Fatal("no result after a denial; the worker must adapt, not abort")
		}
		if !strings.Contains(string(receipt.Result.Payload), decisionDeny) {
			t.Errorf("result payload does not report the denial: %s", receipt.Result.Payload)
		}
	})

	t.Run("run/grant-boundary", func(t *testing.T) {
		// The policy allows everything inside the grant, and the request is
		// outside it. The grant must win: it is the boundary, the policy is
		// only allowed to narrow it.
		receipt, err := conformanceRun(t, workerPath, ConformanceRequestUngranted, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if request.Allowed {
			t.Fatal("an ungranted capability was allowed")
		}
		if request.DenyCode != DenyNotGranted {
			t.Errorf("deny code = %q, want %q", request.DenyCode, DenyNotGranted)
		}
		if receipt.Result == nil {
			t.Fatal("no result after an out-of-grant denial")
		}
	})

	t.Run("run/error-is-terminal", func(t *testing.T) {
		receipt, err := conformanceRun(t, workerPath, ConformanceErrorOnly, AllowWithinGrant())
		if !errors.Is(err, ErrWorkerReported) {
			t.Fatalf("Run error = %v, want the worker's own reported error", err)
		}
		if errors.Is(err, ErrResultAfterError) {
			t.Fatal("the worker emitted a result after its error; an error is terminal")
		}
		var reported *WorkerError
		if !errors.As(err, &reported) || reported.Code == "" {
			t.Fatalf("error event carried no code: %v", err)
		}
		if receipt.Result != nil {
			t.Error("a result was recorded after an error")
		}
		if receipt.Failure == nil || receipt.Failure.Origin != FailureWorker {
			t.Errorf("failure record = %+v, want one attributed to the worker", receipt.Failure)
		}
	})

	t.Run("run/cancellation", func(t *testing.T) {
		client := conformanceClient(t, workerPath, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel as soon as the worker is demonstrably working, so the run is
		// interrupted mid-flight rather than before it starts.
		client.cfg.OnProgress = func(ProgressRecord) { cancel() }

		job := conformanceJob(ConformanceSlow)
		started := time.Now()
		receipt, err := client.Run(ctx, job)
		elapsed := time.Since(started)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if elapsed > 30*time.Second {
			t.Errorf("Run returned %s after cancellation; it must not wait for the worker's own schedule", elapsed)
		}
		if receipt == nil {
			t.Fatal("no receipt after cancellation; a cancelled run still has an audit record")
		}
	})

	t.Run("run/no-credential-leak", func(t *testing.T) {
		// Every subtest above plants the same synthetic broker token. This one
		// re-runs the tool path and checks the whole receipt and error text,
		// because a credential returning to Babel is the one failure that
		// must be impossible rather than merely unlikely.
		receipt, err := conformanceRun(t, workerPath, ConformanceRequestTool, AllowWithinGrant())
		rendered := fmt.Sprintf("%+v", receipt)
		if err != nil {
			rendered += " " + err.Error()
		}
		if strings.Contains(rendered, conformanceToken) {
			t.Error("the run-scoped broker credential came back to Babel")
		}
	})
}

// conformanceClient builds a Client for one obligation, with budgets tight
// enough to fail fast and loose enough not to be flaky on a busy machine.
func conformanceClient(t *testing.T, workerPath string, adjust func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Binary: workerPath,
		Limits: Limits{
			HandshakeTimeout: 15 * time.Second,
			IdleTimeout:      20 * time.Second,
			ExitGrace:        10 * time.Second,
		},
	}
	if adjust != nil {
		adjust(&cfg)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// conformanceJob is the job every run obligation uses: one recipe, a grant
// covering corpus search and repository reads but deliberately not sandbox
// execution, and a synthetic broker credential.
func conformanceJob(directive string) Job {
	return Job{
		JobID:   "conformance-job",
		RunID:   "conformance-run",
		Profile: ProfileRef{ID: "synthetic-profile", Revision: 1},
		Recipes: []RecipeRef{{ID: "outcome-integrity", Version: 1}},
		Grant: Grant{
			Capabilities: []Capability{CapabilityCorpusSearch, CapabilityRepoRead},
			Disclosure:   DisclosureLocal,
		},
		Sources: []Source{{
			Kind:     "session",
			Selector: "omp/synthetic-session",
			Digest:   "sha256:" + strings.Repeat("0", 64),
		}},
		Broker: Broker{Endpoint: "http://127.0.0.1:1/evidence", Token: conformanceToken},
		Params: map[string]string{ParamConformance: directive},
	}
}

// conformanceRun executes one directive and returns its receipt.
func conformanceRun(t *testing.T, workerPath, directive string, policy Authorizer, adjust ...func(*Job)) (*Receipt, error) {
	t.Helper()
	client := conformanceClient(t, workerPath, func(cfg *Config) { cfg.Authorizer = policy })
	job := conformanceJob(directive)
	for _, apply := range adjust {
		apply(&job)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	receipt, err := client.Run(ctx, job)
	if receipt == nil {
		t.Fatalf("Run returned no receipt (error %v)", err)
	}
	return receipt, err
}
