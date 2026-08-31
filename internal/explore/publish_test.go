package explore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sync"
	"github.com/atyrode/babel/internal/worker"
)

// recordingHook is a sync.Hook that remembers what the control plane asked of
// it and nothing else.
//
// A real *sync.Publisher needs PostgreSQL, an encrypted object store and a
// payload keyring, and reaching any of them would test internal/sync rather
// than this package. What is under test here is a decision made before the
// first of them is touched: which closure an exploration declares, on which
// context, and on which of its exits.
//
// It needs no lock. Every method is called from the goroutine that called
// Explore — publication is deliberately inline, on the caller's goroutine —
// and a mutex here would imply a concurrency this package does not have.
type recordingHook struct {
	// closures are the declarations, in call order.
	closures []sync.Closure
	// ctxErrs is each call's context state at the moment of the call, which
	// is how a test tells "published on the run's context" from "published on
	// the detached one" rather than assuming it from the call happening.
	ctxErrs []error
	// err is what CommitInline returns: internal/sync reserves a returned
	// error for a caller bug, so this stands in for one.
	err error
}

func (h *recordingHook) Append(context.Context, *sql.Tx, string, sync.Record) (sync.Closure, bool, error) {
	return sync.Closure{}, false, nil
}

func (h *recordingHook) StageTx(context.Context, *sql.Tx, sync.Record) error { return nil }

func (h *recordingHook) DeclareTx(context.Context, *sql.Tx, sync.Closure) error { return nil }

func (h *recordingHook) CommitInline(ctx context.Context, c sync.Closure) error {
	h.closures = append(h.closures, c)
	h.ctxErrs = append(h.ctxErrs, ctx.Err())
	return h.err
}

// publishing configures a controller with this hook.
func publishing(h *recordingHook) func(*explore.Config) {
	return func(cfg *explore.Config) { cfg.Sync = h }
}

// declaredOnce reports the single closure the hook was handed, failing the test
// when a run declared its closure a different number of times: a second
// declaration at another size is what migration 0003 refuses outright, and a
// run that declared none leaves its records pending with nobody to complete
// them.
func declaredOnce(t *testing.T, hook *recordingHook, runID string) sync.Closure {
	t.Helper()
	if len(hook.closures) != 1 {
		t.Fatalf("the run declared %d closures, want exactly 1: %+v", len(hook.closures), hook.closures)
	}
	if hook.closures[0] != (sync.Closure{RunID: runID}) {
		t.Errorf("declared %+v, want the run's own closure %+v",
			hook.closures[0], sync.Closure{RunID: runID})
	}
	return hook.closures[0]
}

// TestCompletedRunDeclaresItsOwnClosureOnce is the ordinary path. The stores
// stage each record into the closure the run id names and declare nothing,
// because a closure may not be declared while the run can still grow; ending
// the run is what completes it, and this control plane is the only thing that
// observes that.
func TestCompletedRunDeclaresItsOwnClosureOnce(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	hook := &recordingHook{}
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		publishing(hook))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-published"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	declaredOnce(t, hook, "r-published")
	if len(outcome.Findings) != 1 {
		t.Errorf("publication changed what the run produced: %d findings, want 1", len(outcome.Findings))
	}
}

// TestRefusedRunStillDeclaresItsClosure is why the call sits on the refusal
// path too. A run §6.4 refuses before inference still writes the receipt that
// says it was refused, that receipt is a durable record the fleet is owed, and
// a closure nobody declares stays pending forever with no remedy.
func TestRefusedRunStillDeclaresItsClosure(t *testing.T) {
	h := newHarness(t)
	h.plantSecret()
	payload := h.writeResult("discovery.json", h.discovery())
	hook := &recordingHook{}
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		func(cfg *explore.Config) { cfg.Grant.Disclosure = worker.DisclosureHosted },
		publishing(hook))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-refused"})
	if !errors.Is(err, explore.ErrRedactionRequired) {
		t.Fatalf("Explore error = %v, want a refusal to launch", err)
	}
	if outcome.Receipt == nil {
		t.Fatal("a refused run wrote no receipt, so there is nothing to publish")
	}
	declaredOnce(t, hook, "r-refused")
}

// TestCancelledRunDeclaresItsClosureOnTheDetachedContext is the placement's
// second reason. Recording and publishing what a run already produced is not
// work a budget may cut short, so the declaration runs on the context detached
// from the run's cancellation — otherwise a cancellation would cost the fleet
// the work rather than the remainder.
func TestCancelledRunDeclaresItsClosureOnTheDetachedContext(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	hook := &recordingHook{}
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		publishing(hook))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := true
	outcome, err := controller.Explore(ctx, explore.Options{
		Authority: testAuthority,
		RunID:     "r-cancelled-publish",
		OnRecord: func(e explore.RecordEvent) {
			if first && e.Type == frontier.EntityHypothesis {
				first = false
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Explore error = %v, want a cancellation", err)
	}
	if !outcome.Cancelled {
		t.Error("the outcome does not report the cancellation")
	}
	declaredOnce(t, hook, "r-cancelled-publish")
	if hook.ctxErrs[0] != nil {
		t.Errorf("the closure was declared on a context reporting %v, want the detached one", hook.ctxErrs[0])
	}
}

// TestFailedPublicationDoesNotChangeTheRunsOutcome is the invariant the whole
// ordering exists for: publication is never a precondition for a local write.
// A run whose closure could not be declared keeps its records, its receipt,
// its returned error and its exit status, and the failure is visible rather
// than inferred from silence.
func TestFailedPublicationDoesNotChangeTheRunsOutcome(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	// internal/sync returns an error only for a caller bug: a closure with no
	// staged records, or one already declared at another size. A transient
	// failure returns nil after reporting its own diagnostic line.
	hook := &recordingHook{err: errors.New("declared 2, staged 3")}
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		publishing(hook))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-unpublished"})
	if err != nil {
		t.Fatalf("a failed publication became the run's error: %v", err)
	}
	declaredOnce(t, hook, "r-unpublished")
	if len(outcome.Findings) != 1 || len(outcome.Observations) != 2 || len(outcome.Hypotheses) != 3 {
		t.Errorf("a failed publication changed the run's output: %d hypotheses, %d observations, %d findings",
			len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings))
	}
	if !hasFailure(outcome.Failures, explore.FailureSyncPublish) {
		t.Errorf("the outcome does not report the failed publication: %+v", outcome.Failures)
	}

	// The records are durable, and the receipt is exactly the one that was
	// declared. A failure to publish the receipt cannot also be inside it:
	// the bytes were stored, and their closure was declared over them.
	ctx := context.Background()
	if _, err := h.frontier.Finding(ctx, outcome.Findings[0]); err != nil {
		t.Errorf("the finding did not survive the failed publication: %v", err)
	}
	if outcome.Receipt == nil {
		t.Fatal("no receipt was written")
	}
	stored, err := h.runs.Receipt(ctx, outcome.Receipt.Header.ID)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if hasFailure(stored.Body.Failures, explore.FailureSyncPublish) {
		t.Errorf("the stored receipt claims a publication failure it was written before: %+v",
			stored.Body.Failures)
	}
}

// TestRunWithNoSyncHookIsUnchanged is local-only mode, which is a supported
// deployment rather than a degraded one: no hook, no declaration, no failure,
// and the same durable output.
func TestRunWithNoSyncHookIsUnchanged(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-local-only"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 3 || len(outcome.Observations) != 2 || len(outcome.Findings) != 1 {
		t.Errorf("a local-only run produced %d hypotheses, %d observations, %d findings",
			len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings))
	}
	if len(outcome.Failures) != 0 {
		t.Errorf("a local-only run recorded failures: %+v", outcome.Failures)
	}
}

// TestSeparateJobsJoinTheRunsOneClosure is §5.4's separate jobs meeting §6.5's
// closure. The challenger and the synthesizer are logically separate jobs with
// their own receipts, but they are jobs within one exploration, and their
// records and receipts stage under the exploration's run id. So the run
// declares one closure covering all of it: three closures would make one
// exploration become globally reviewable in three unrelated pieces, and a
// reader would see a critique with no exploration behind it.
func TestSeparateJobsJoinTheRunsOneClosure(t *testing.T) {
	h := newHarness(t)
	explorePayload := h.writeResult("discovery.json", h.discovery())
	grounded, err := json.Marshal(h.evidence(2, "the record the objection rests on"))
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	challengePayload := h.writeRaw("challenge.json", `{
	  "objections": [
	    {"ref": "obj-grounded", "hypothesis": "${paramitem:`+explore.ParamBriefHypotheses+`:0}",
	     "grounds": "evidence", "recipe": {"id": "outcome-integrity", "version": 2},
	     "claim": {"claim": "the cited record says otherwise", "confidence": "moderate",
	               "impact": "moderate", "counter_evidence_absent": true,
	               "evidence": [`+string(grounded)+`]}}
	  ]
	}`)
	// The synthesizer only adds a candidate: consolidation has its own
	// coverage, and what this test needs is a third job that writes durable
	// records under a third receipt.
	synthesisPayload := h.writeRaw("synthesis.json", `{
	  "candidates": [
	    {"ref": "add-1", "hypothesis": {"statement": "an addition the synthesizer proposes"}}
	  ]
	}`)
	hook := &recordingHook{}
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:    explorePayload,
		explore.StageChallenge:  challengePayload,
		explore.StageSynthesize: synthesisPayload,
	}), publishing(hook))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		Authority:  testAuthority,
		RunID:      "r-three-jobs",
		Challenge:  true,
		Synthesize: true,
	})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if outcome.Challenge == nil || outcome.Synthesis == nil {
		t.Fatal("the separate jobs wrote no receipts, so there is nothing extra in the closure")
	}
	declaredOnce(t, hook, "r-three-jobs")
}
