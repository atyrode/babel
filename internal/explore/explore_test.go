package explore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/synth"
	"github.com/atyrode/babel/internal/worker"
)

// fakeWorkerPath is the synthetic analysis worker, built once per test binary.
// Building it here rather than per test keeps a dozen supervised runs from
// paying a dozen compiles of the same fixture, and nothing is prebuilt or
// committed: the suite needs no worker, no credential and no transcript.
var fakeWorkerPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "babel-explore-fixture-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating fixture dir: %v\n", err)
		os.Exit(1)
	}
	fakeWorkerPath = filepath.Join(dir, "fakeworker")
	build := exec.Command("go", "build", "-o", fakeWorkerPath,
		"github.com/atyrode/babel/internal/worker/testdata/fakeworker")
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

// testRecipe is the recipe every synthetic claim cites. It declares all three
// stages, so one identity serves discovery, critique and synthesis.
var testRecipe = worker.RecipeRef{ID: "outcome-integrity", Version: 1}

// smallProfile is a corpus with no deliberate defects: three tiny sessions
// across two harnesses. Defects have their own coverage in internal/preflight,
// and a clean corpus is what makes a preflight verdict in these tests
// attributable to the one thing a test planted.
func smallProfile() synth.Profile {
	return synth.Profile{
		Seed:                7,
		OMPSessions:         2,
		CodexSessions:       1,
		SizeBuckets:         []synth.SizeBucket{{Bytes: 4 << 10, Weight: 1}},
		ArtifactsPerSession: [2]int{0, 0},
		BlobCount:           1,
	}
}

// harness is one exploration's world: a synthetic corpus, the durable stores
// that share one file, a retrieval index over the corpus, and the locators a
// synthetic result can cite as evidence.
type harness struct {
	t        *testing.T
	dir      string
	prep     run.Preparation
	inputs   []preflight.Input
	frontier *frontier.Store
	runs     *run.Store
	ledger   *explore.Ledger
	index    *index.Index
	recipes  *cookbook.Set
	locators []event.Locator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	corpus, err := synth.Generate(filepath.Join(root, "corpus"), smallProfile())
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}

	state := filepath.Join(root, "state")
	idx, err := index.Open(state)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	h := &harness{t: t, dir: state, index: idx}
	var selection []run.Selected
	for _, session := range corpus.Sessions {
		stream := event.Stream{
			Harness:       session.Harness,
			AdapterSchema: 1,
			SourceID:      session.ID,
			Path:          session.Path,
		}
		file, err := os.Open(session.Path)
		if err != nil {
			t.Fatalf("open %s: %v", session.Path, err)
		}
		sum, _, err := digest.Compute(file)
		file.Close()
		if err != nil {
			t.Fatalf("digest %s: %v", session.Path, err)
		}
		if _, err := idx.IndexSession(context.Background(), stream); err != nil {
			t.Fatalf("index %s: %v", session.Path, err)
		}
		h.inputs = append(h.inputs, preflight.Input{Stream: stream, Digest: string(sum)})
		selection = append(selection, run.Selected{
			Host:    "synthetic-host",
			Harness: session.Harness,
			// The capture and the normalized stream are the same bytes for a
			// fixture, which is not true of a real corpus; §7 wants both
			// digests recorded and a test has only one to record.
			SourceID:      session.ID,
			CaptureDigest: sum,
			SourceDigest:  sum,
			Adapter:       run.AdapterRef{Schema: 1, Version: "synthetic"},
		})
	}

	h.prep, err = run.NewPreparation(time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC), selection)
	if err != nil {
		t.Fatalf("new preparation: %v", err)
	}
	if h.frontier, err = frontier.Open(state); err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { h.frontier.Close() })
	if h.runs, err = run.Open(state); err != nil {
		t.Fatalf("open receipts: %v", err)
	}
	t.Cleanup(func() { h.runs.Close() })
	if h.ledger, err = explore.OpenLedger(state); err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { h.ledger.Close() })
	if h.recipes, err = cookbook.Embedded(); err != nil {
		t.Fatalf("load cookbook: %v", err)
	}
	h.locators = h.corpusLocators(h.inputs[0].Stream, 3)
	return h
}

// corpusLocators returns locators for the first intact events of a session,
// which is what a synthetic observation cites so its evidence recovers real
// bytes rather than being asserted.
func (h *harness) corpusLocators(stream event.Stream, want int) []event.Locator {
	h.t.Helper()
	file, err := os.Open(stream.Path)
	if err != nil {
		h.t.Fatalf("open %s: %v", stream.Path, err)
	}
	defer file.Close()
	var out []event.Locator
	stop := errors.New("enough")
	err = event.Scan(file, stream, func(e event.Event) error {
		if e.Partial || e.Text == "" {
			return nil
		}
		out = append(out, e.Locator)
		if len(out) == want {
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		h.t.Fatalf("scan %s: %v", stream.Path, err)
	}
	if len(out) < want {
		h.t.Fatalf("corpus yielded %d usable locators, want %d", len(out), want)
	}
	return out
}

// config is the controller configuration these tests share.
func (h *harness) config(args []string, mutate ...func(*explore.Config)) explore.Config {
	h.t.Helper()
	cfg := explore.Config{
		Preparation: h.prep,
		Recipes:     h.recipes,
		Grant: worker.Grant{
			Capabilities: []worker.Capability{worker.CapabilityCorpusSearch},
			Disclosure:   worker.DisclosureLocal,
		},
		Profile: worker.ProfileRef{ID: "synthetic-profile", Revision: 1},
		Worker: worker.Config{
			Binary: fakeWorkerPath,
			Args:   args,
			Limits: worker.Limits{
				HandshakeTimeout: 10 * time.Second,
				IdleTimeout:      10 * time.Second,
				ExitGrace:        5 * time.Second,
				TerminateGrace:   500 * time.Millisecond,
			},
		},
		Frontier:     h.frontier,
		Runs:         h.runs,
		Ledger:       h.ledger,
		Index:        h.index,
		Inputs:       h.inputs,
		Capabilities: run.CapabilityVersions{Tool: "explore-test/1"},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	return cfg
}

func (h *harness) controller(args []string, mutate ...func(*explore.Config)) *explore.Controller {
	h.t.Helper()
	controller, err := explore.New(h.config(args, mutate...))
	if err != nil {
		h.t.Fatalf("explore.New: %v", err)
	}
	return controller
}

// evidence builds one citation, which the frontier only allows through its own
// constructor.
func (h *harness) evidence(i int, note string) frontier.Evidence {
	h.t.Helper()
	ev, err := frontier.NewEvidence(h.locators[i], note)
	if err != nil {
		h.t.Fatalf("new evidence: %v", err)
	}
	return ev
}

// writeResult marshals a structured result into a payload file the fake worker
// emits verbatim.
func (h *harness) writeResult(name string, res explore.Result) string {
	h.t.Helper()
	encoded, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		h.t.Fatalf("encode result: %v", err)
	}
	return h.writeRaw(name, string(encoded))
}

// writeRaw writes a payload template verbatim, for the results that must name
// identifiers the run has not minted yet.
func (h *harness) writeRaw(name, text string) string {
	h.t.Helper()
	path := filepath.Join(h.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		h.t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// discovery is the well-behaved exploration result: three candidates, the
// first two developed, one consolidated into a finding and a proposal, the
// third deferred by the worker itself.
func (h *harness) discovery() explore.Result {
	return explore.Result{
		Candidates: []explore.Candidate{
			{
				Ref:        "c-1",
				Hypothesis: frontier.HypothesisPayload{Statement: "the first synthetic candidate", Novelty: 0.4, Priority: 0.9},
				Observations: []explore.Observation{{
					Ref:    "o-1",
					Recipe: testRecipe,
					Claim: frontier.ObservationPayload{
						Claim:                 "the first synthetic claim",
						Category:              "outcome",
						Confidence:            frontier.ConfidenceModerate,
						Impact:                frontier.ImpactModerate,
						Evidence:              []frontier.Evidence{h.evidence(0, "first cited record")},
						CounterEvidenceAbsent: true,
					},
				}},
			},
			{
				Ref:        "c-2",
				Hypothesis: frontier.HypothesisPayload{Statement: "the second synthetic candidate", Novelty: 0.7, Priority: 0.5},
				Observations: []explore.Observation{{
					Ref:    "o-2",
					Recipe: testRecipe,
					Claim: frontier.ObservationPayload{
						Claim:                 "the second synthetic claim",
						Confidence:            frontier.ConfidenceLow,
						Impact:                frontier.ImpactLow,
						Evidence:              []frontier.Evidence{h.evidence(1, "second cited record")},
						CounterEvidenceAbsent: true,
					},
				}},
			},
			{
				Ref:        "c-3",
				Hypothesis: frontier.HypothesisPayload{Statement: "the third synthetic candidate, left speculative", Priority: 0.1},
			},
		},
		Consolidations: []explore.Consolidation{{
			Ref:          "con-1",
			Observations: []string{"o-1", "o-2"},
			Finding: frontier.FindingPayload{
				Title:                 "a synthetic consolidation",
				Pattern:               "both synthetic claims describe the same synthetic shape",
				Significance:          "it exercises the development path end to end",
				CounterEvidenceAbsent: true,
			},
			Proposal: &frontier.ProposalPayload{
				Title:          "a synthetic proposal",
				Problem:        "the synthetic shape recurs",
				Outcome:        "record it once",
				Impact:         frontier.ImpactLow,
				Classification: frontier.ClassificationPrivate,
			},
		}},
		Deferred: []explore.Disposal{{Hypothesis: "c-3", Reason: "the worker chose not to develop it in this pass"}},
	}
}

// payloadArgs are the fixture flags that make one stage emit one payload.
func payloadArgs(perStage map[explore.Stage]string) []string {
	args := []string{"-result-payload-selector", explore.ParamStage}
	for _, stage := range slices.Sorted(maps.Keys(perStage)) {
		args = append(args, "-result-payload", string(stage)+"="+perStage[stage])
	}
	return args
}

// TestFullRunProducesDurableRecordsAndAReceipt is the whole path §6.5
// describes: a scope, a worker, and durable hypotheses, observations, a
// finding, a proposal and a receipt at the end of it.
func TestFullRunProducesDurableRecordsAndAReceipt(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-full"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 3 {
		t.Errorf("persisted %d hypotheses, want 3", len(outcome.Hypotheses))
	}
	if len(outcome.Observations) != 2 {
		t.Errorf("persisted %d observations, want 2", len(outcome.Observations))
	}
	if len(outcome.Findings) != 1 || len(outcome.Proposals) != 1 {
		t.Errorf("persisted %d findings and %d proposals, want one of each",
			len(outcome.Findings), len(outcome.Proposals))
	}
	if outcome.Receipt == nil {
		t.Fatal("no receipt was written")
	}

	// The records are durable, not merely reported.
	ctx := context.Background()
	finding, err := h.frontier.Finding(ctx, outcome.Findings[0])
	if err != nil {
		t.Fatalf("read finding: %v", err)
	}
	if !slices.Equal(finding.ObservationIDs, outcome.Observations) {
		t.Errorf("finding supports %v, want %v", finding.ObservationIDs, outcome.Observations)
	}
	if len(finding.HypothesisIDs) != 2 {
		t.Errorf("finding lineage names %d hypotheses, want 2", len(finding.HypothesisIDs))
	}
	proposal, err := h.frontier.Proposal(ctx, outcome.Proposals[0])
	if err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if !slices.Contains(proposal.FindingIDs, finding.ID) {
		t.Errorf("proposal %s does not name finding %s", proposal.ID, finding.ID)
	}

	// The two developed candidates were promoted by the control plane; the
	// third was deferred rather than erased.
	for _, id := range finding.HypothesisIDs {
		record, err := h.frontier.Hypothesis(ctx, id)
		if err != nil {
			t.Fatalf("read hypothesis: %v", err)
		}
		if record.Status != frontier.StatusPromoted {
			t.Errorf("hypothesis %s is %q, want promoted", id, record.Status)
		}
	}
	if len(outcome.Deferred) != 1 {
		t.Fatalf("deferred %d candidates, want 1", len(outcome.Deferred))
	}
	unexplored, err := h.frontier.Unexplored(ctx, 0)
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}
	if len(unexplored) != 1 || unexplored[0].ID != outcome.Deferred[0] {
		t.Errorf("the frontier holds %d unexplored candidates, want exactly the deferred one", len(unexplored))
	}

	// And the receipt is stored, references the preparation, and reports what
	// the run did without a caller having to hold the outcome.
	stored, err := h.runs.Receipt(ctx, outcome.Receipt.Header.ID)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if stored.Header.PreparationID != h.prep.ID {
		t.Errorf("receipt names preparation %s, want %s", stored.Header.PreparationID, h.prep.ID)
	}
	if stored.Header.Counts.Deferred != 1 {
		t.Errorf("receipt counts %d deferred candidates, want 1", stored.Header.Counts.Deferred)
	}
	if stored.Body.Worker == nil {
		t.Fatal("receipt embeds no worker receipt")
	}
	if stored.Body.Job.Schema != worker.ResultSchema {
		t.Errorf("receipt records result schema %q, want %q", stored.Body.Job.Schema, worker.ResultSchema)
	}
	if len(stored.Body.Failures) != 0 {
		t.Errorf("a clean run recorded failures: %+v", stored.Body.Failures)
	}
}

// TestReceiptRecordsEveryToolDecisionAndTheRetrievalTrace is what makes a run
// inspectable after the fact: the receipt has to say what the worker asked for,
// what Babel decided, and what retrieval returned.
func TestReceiptRecordsEveryToolDecisionAndTheRetrievalTrace(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		// One served corpus search and one request for a capability the
		// grant does not carry, so the receipt has both decisions to record.
		"-request-capability", "corpus-search,sandbox-exec",
		"-search-query", "")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-trace"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	body := outcome.Receipt.Body
	if len(body.Worker.ToolRequests) != 2 {
		t.Fatalf("receipt records %d tool decisions, want 2", len(body.Worker.ToolRequests))
	}
	first, second := body.Worker.ToolRequests[0], body.Worker.ToolRequests[1]
	if !first.Allowed || first.Capability != worker.CapabilityCorpusSearch {
		t.Errorf("first decision = %+v, want an allowed corpus search", first)
	}
	if second.Allowed || second.DenyCode != worker.DenyNotGranted {
		t.Errorf("second decision = %+v, want a not-granted denial", second)
	}
	if outcome.Receipt.Header.Counts.ToolsDenied != 1 {
		t.Errorf("receipt counts %d denials, want 1", outcome.Receipt.Header.Counts.ToolsDenied)
	}

	if len(body.Retrieval) != 1 {
		t.Fatalf("receipt records %d retrieval steps, want 1", len(body.Retrieval))
	}
	step := body.Retrieval[0]
	if step.Index != 1 || step.Tool != string(worker.CapabilityCorpusSearch) {
		t.Errorf("retrieval step = %+v, want step 1 of corpus-search", step)
	}
	if len(step.Results) == 0 {
		t.Fatal("the retrieval step recorded no results, so nothing is reproducible")
	}
	for i, result := range step.Results {
		if result.Rank != i+1 {
			t.Errorf("result %d has rank %d, want %d", i, result.Rank, i+1)
		}
		if result.Evidence.Locator().Path == "" || result.Evidence.Locator().Digest == "" {
			t.Errorf("result %d cannot recover its bytes: %+v", i, result.Evidence.Locator())
		}
	}
	if len(outcome.Retrieval) != 1 || len(outcome.Retrieval[0].Hits) != len(step.Results) {
		t.Errorf("the outcome's served retrieval disagrees with the receipt's trace")
	}
}

// TestEveryCandidateIsPersistedWhenTheRunIsCancelled is §5.2's rule under the
// worst timing: cancellation lands after the first candidate is durable, and
// every candidate the worker emitted still has to be in the frontier with the
// remainder queryable afterwards.
func TestEveryCandidateIsPersistedWhenTheRunIsCancelled(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := true
	outcome, err := controller.Explore(ctx, explore.Options{
		RunID:     "r-cancelled",
		Challenge: true,
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
	if len(outcome.Hypotheses) != 3 {
		t.Fatalf("persisted %d candidates, want all 3 the worker emitted", len(outcome.Hypotheses))
	}
	if len(outcome.Observations) != 0 {
		t.Errorf("developed %d observations after cancellation, want none", len(outcome.Observations))
	}
	if outcome.Challenge != nil {
		t.Error("a cancelled run still ran the challenger")
	}

	ctx = context.Background()
	for _, id := range outcome.Hypotheses {
		if _, err := h.frontier.Hypothesis(ctx, id); err != nil {
			t.Errorf("candidate %s did not survive the cancellation: %v", id, err)
		}
	}
	unexplored, err := h.frontier.Unexplored(ctx, 0)
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}
	if len(unexplored) != 3 {
		t.Errorf("the frontier holds %d unexplored candidates, want 3", len(unexplored))
	}
	if outcome.Receipt == nil {
		t.Fatal("a cancelled run wrote no receipt")
	}
	if !hasFailure(outcome.Receipt.Body.Failures, explore.FailureCancelled) {
		t.Errorf("the receipt does not record the cancellation: %+v", outcome.Receipt.Body.Failures)
	}
}

// TestResumeAfterCancellationNeitherLosesNorDuplicates is §6.5's resumability
// rule. The second attempt must recognize the first one's committed records
// rather than writing second copies of them, and must be able to finish the
// development the cancellation cut short.
func TestResumeAfterCancellationNeitherLosesNorDuplicates(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := true
	interrupted, err := controller.Explore(ctx, explore.Options{
		RunID: "r-resumed",
		OnRecord: func(e explore.RecordEvent) {
			if first && e.Type == frontier.EntityHypothesis {
				first = false
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first attempt error = %v, want a cancellation", err)
	}

	resumed, err := controller.Explore(context.Background(), explore.Options{RunID: "r-resumed"})
	if err != nil {
		t.Fatalf("resumed attempt: %v (failures %+v)", err, resumed.Failures)
	}
	if !slices.Equal(interrupted.Hypotheses, resumed.Hypotheses) {
		t.Errorf("resumption changed the candidate identities:\n first %v\nsecond %v",
			interrupted.Hypotheses, resumed.Hypotheses)
	}
	if resumed.Reused != len(interrupted.Hypotheses) {
		t.Errorf("resumption reused %d records, want the %d already committed",
			resumed.Reused, len(interrupted.Hypotheses))
	}
	if len(resumed.Observations) != 2 || len(resumed.Findings) != 1 {
		t.Errorf("the resumed attempt developed %d observations and %d findings, want 2 and 1",
			len(resumed.Observations), len(resumed.Findings))
	}

	// Nothing was duplicated: the frontier holds exactly the three candidates
	// the worker ever emitted, two of them now promoted.
	total := 0
	for _, id := range resumed.Hypotheses {
		if _, err := h.frontier.Hypothesis(context.Background(), id); err != nil {
			t.Fatalf("read hypothesis: %v", err)
		}
		total++
	}
	if total != 3 {
		t.Errorf("the run owns %d candidates, want 3", total)
	}
	if len(resumed.Promoted) != 2 {
		t.Errorf("promoted %d candidates, want 2", len(resumed.Promoted))
	}

	// And the run's receipt is a chain rather than a replacement.
	revisions, err := h.runs.Revisions(context.Background(), "r-resumed")
	if err != nil {
		t.Fatalf("read revisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("the run has %d receipt revisions, want 2", len(revisions))
	}
	if revisions[1].Header.Supersedes != revisions[0].Header.ID {
		t.Errorf("revision 2 supersedes %q, want %q", revisions[1].Header.Supersedes, revisions[0].Header.ID)
	}
	if revisions[1].Body.AmendmentReason == "" {
		t.Error("the amendment does not say why it exists")
	}
}

// TestResultSkippingTheDevelopmentPathIsRefused covers §4.2's mandatory path.
// A consolidation whose supporting observations do not exist is refused and
// recorded, never repaired by inventing the missing step.
func TestResultSkippingTheDevelopmentPathIsRefused(t *testing.T) {
	h := newHarness(t)
	result := h.discovery()
	// The candidate keeps its finding but loses the observations behind it.
	result.Candidates[0].Observations = nil
	result.Candidates[1].Observations = nil
	payload := h.writeResult("skipped.json", result)
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-skipped"})
	if !errors.Is(err, explore.ErrUnknownReference) {
		t.Fatalf("Explore error = %v, want an unresolvable consolidation", err)
	}
	if len(outcome.Findings) != 0 {
		t.Errorf("wrote %d findings from a result with no observations", len(outcome.Findings))
	}
	if len(outcome.Hypotheses) != 3 {
		t.Errorf("persisted %d candidates, want 3: a refused consolidation does not discard the result",
			len(outcome.Hypotheses))
	}
	if !hasFailure(outcome.Receipt.Body.Failures, explore.FailureUnknownRecord) {
		t.Errorf("the receipt does not record the refusal: %+v", outcome.Receipt.Body.Failures)
	}
}

// TestDeniedCapabilityDoesNotEndTheRun covers the boundary §6.5 draws: Babel
// authorizes every request, a facility it cannot broker is denied cleanly
// rather than answered with fabricated evidence, and the worker keeps working.
func TestDeniedCapabilityDoesNotEndTheRun(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-request-capability", "public-research")
	controller := h.controller(args, func(cfg *explore.Config) {
		cfg.Grant.Capabilities = append(cfg.Grant.Capabilities, worker.CapabilityPublicResearch)
		cfg.Capabilities.PublicResearch = "unavailable"
	})

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-denied"})
	if err != nil {
		t.Fatalf("a denial ended the run: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Findings) != 1 {
		t.Errorf("the run produced %d findings, want 1: a denial is not a termination", len(outcome.Findings))
	}
	requests := outcome.Receipt.Body.Worker.ToolRequests
	if len(requests) != 1 {
		t.Fatalf("recorded %d tool decisions, want 1", len(requests))
	}
	if requests[0].Allowed {
		t.Error("a capability with no facility behind it was allowed")
	}
	if requests[0].DenyCode != worker.DenyPolicy {
		t.Errorf("denial code = %q, want a policy denial", requests[0].DenyCode)
	}
	if !strings.Contains(requests[0].Reason, "public-research") {
		t.Errorf("the denial reason does not name the facility: %q", requests[0].Reason)
	}
}

// TestHostedRunWithASecretIsBlockedUntilRedactionIsApplied is §6.4 enforced
// rather than reported: preflight is prior to inference, and a hosted class
// with a secret finding does not get to launch.
func TestHostedRunWithASecretIsBlockedUntilRedactionIsApplied(t *testing.T) {
	h := newHarness(t)
	h.plantSecret()
	payload := h.writeResult("discovery.json", h.discovery())
	args := payloadArgs(map[explore.Stage]string{explore.StageExplore: payload})
	hosted := func(cfg *explore.Config) { cfg.Grant.Disclosure = worker.DisclosureHosted }

	blocked := h.controller(args, hosted)
	outcome, err := blocked.Explore(context.Background(), explore.Options{RunID: "r-hosted"})
	if !errors.Is(err, explore.ErrRedactionRequired) {
		t.Fatalf("Explore error = %v, want a refusal to launch", err)
	}
	if len(outcome.Hypotheses) != 0 {
		t.Errorf("a blocked run wrote %d candidates", len(outcome.Hypotheses))
	}
	if outcome.Receipt == nil {
		t.Fatal("a blocked run wrote no receipt")
	}
	if outcome.Receipt.Body.Worker != nil {
		t.Error("a blocked run launched a worker")
	}
	if !hasFailure(outcome.Receipt.Body.Failures, explore.FailureRedaction) {
		t.Errorf("the receipt does not say why the run was blocked: %+v", outcome.Receipt.Body.Failures)
	}
	if outcome.Preflight == nil || len(outcome.Preflight.Disclosure.Forcing) == 0 {
		t.Error("the report does not name the findings that force redaction")
	}

	// The same scope proceeds once redaction is applied, and what the run
	// serves the worker is redacted rather than merely declared to be.
	redacting := h.controller(append(args,
		"-request-capability", "corpus-search", "-search-query", secretProbe),
		hosted, func(cfg *explore.Config) { cfg.Redact = true })
	outcome, err = redacting.Explore(context.Background(), explore.Options{RunID: "r-hosted-redacted"})
	if err != nil {
		t.Fatalf("the redacted run was refused too: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Findings) != 1 {
		t.Errorf("the redacted run produced %d findings, want 1", len(outcome.Findings))
	}
	if len(outcome.Retrieval) != 1 || len(outcome.Retrieval[0].Hits) == 0 {
		t.Fatalf("the redacted run served no retrieval, so nothing proves the redaction")
	}
	for _, hit := range outcome.Retrieval[0].Hits {
		if strings.Contains(hit.Text, secretProbe) {
			t.Errorf("a hosted run was served the planted credential verbatim")
		}
		if !strings.Contains(hit.Text, "babel-redacted") {
			t.Errorf("the served excerpt carries no redaction placeholder: %q", hit.Text)
		}
		if hit.Locator.Path == "" || hit.Locator.Digest == "" {
			t.Errorf("redaction destroyed the locator back to the original bytes")
		}
	}
}

// TestChallengerCannotCreateAFinding is §5.4's limit on the challenger. It may
// object; it may not consolidate, and trying is a recorded failure rather than
// a finding nobody authorized.
func TestChallengerCannotCreateAFinding(t *testing.T) {
	h := newHarness(t)
	explorePayload := h.writeResult("discovery.json", h.discovery())
	// The challenger names observations from its own brief, which is exactly
	// the material it is allowed to read and not allowed to consolidate.
	challengePayload := h.writeRaw("challenge.json", `{
	  "consolidations": [{
	    "ref": "con-challenger",
	    "observations": ${paramlist:`+explore.ParamBriefObservations+`},
	    "finding": {"title": "the challenger's finding", "pattern": "asserted without authority",
	                "counter_evidence_absent": true}
	  }]
	}`)
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:   explorePayload,
		explore.StageChallenge: challengePayload,
	}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-challenger", Challenge: true})
	if !errors.Is(err, explore.ErrUnauthorizedFinding) {
		t.Fatalf("Explore error = %v, want the challenger refused finding authority", err)
	}
	if len(outcome.Findings) != 1 {
		t.Errorf("the run holds %d findings, want only the exploration's 1", len(outcome.Findings))
	}
	if outcome.Challenge == nil {
		t.Fatal("the challenger wrote no receipt of its own")
	}
	if outcome.Challenge.Header.RunID == outcome.Receipt.Header.RunID {
		t.Error("the challenger shares the exploration's run identity")
	}
	if !hasFailure(outcome.Challenge.Body.Failures, explore.FailureAuthority) {
		t.Errorf("the challenger's receipt does not record the refusal: %+v", outcome.Challenge.Body.Failures)
	}
	if len(outcome.Receipt.Body.Failures) != 0 {
		t.Errorf("the challenger's refusal was charged to the exploration: %+v", outcome.Receipt.Body.Failures)
	}
}

// TestChallengerRecordsObjectionsByWhatBacksThem is the other half of §5.4: a
// locator-backed objection is a counter-observation, and one resting on a
// missing check is a contradicting candidate, because §4.3 forbids an
// observation with no locator.
func TestChallengerRecordsObjectionsByWhatBacksThem(t *testing.T) {
	h := newHarness(t)
	explorePayload := h.writeResult("discovery.json", h.discovery())
	grounded, err := json.Marshal(h.evidence(2, "the record the objection rests on"))
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	challengePayload := h.writeRaw("challenge.json", `{
	  "objections": [
	    {"ref": "obj-grounded", "hypothesis": "${paramitem:`+explore.ParamBriefHypotheses+`:0}",
	     "grounds": "evidence", "recipe": {"id": "outcome-integrity", "version": 1},
	     "claim": {"claim": "the cited record says otherwise", "confidence": "moderate",
	               "impact": "moderate", "counter_evidence_absent": true,
	               "evidence": [`+string(grounded)+`]}},
	    {"ref": "obj-ungrounded", "hypothesis": "${paramitem:`+explore.ParamBriefHypotheses+`:0}",
	     "grounds": "missing-check", "recipe": {"id": "outcome-integrity", "version": 1},
	     "claim": {"claim": "nothing verified the claim", "confidence": "low", "impact": "low",
	               "counter_evidence_absent": true}}
	  ]
	}`)
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:   explorePayload,
		explore.StageChallenge: challengePayload,
	}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-objections", Challenge: true})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Objections) != 2 {
		t.Fatalf("recorded %d objections, want 2", len(outcome.Objections))
	}
	ctx := context.Background()
	if _, err := h.frontier.Observation(ctx, outcome.Objections[0]); err != nil {
		t.Errorf("the grounded objection is not an observation: %v", err)
	}
	candidate, err := h.frontier.Hypothesis(ctx, outcome.Objections[1])
	if err != nil {
		t.Fatalf("the ungrounded objection is not a candidate: %v", err)
	}
	links, err := h.frontier.LinksFrom(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	if len(links) != 1 || links[0].Type != frontier.LinkContradicts {
		t.Errorf("the ungrounded objection links %+v, want one contradiction", links)
	}
}

// TestSynthesizerCannotConsolidateAnObservationWithNoLocator covers §5.4's rule
// that a synthesizer consolidates locator-backed observations and recorded
// objections. Citing a candidate hypothesis as if it were evidence is refused;
// an unsupported addition stays a hypothesis.
func TestSynthesizerCannotConsolidateAnObservationWithNoLocator(t *testing.T) {
	h := newHarness(t)
	result := h.discovery()
	result.Consolidations = nil // synthesis is the stage under test
	explorePayload := h.writeResult("discovery.json", result)
	synthesisPayload := h.writeRaw("synthesis.json", `{
	  "candidates": [
	    {"ref": "add-1", "hypothesis": {"statement": "an addition the record does not support"}}
	  ],
	  "consolidations": [{
	    "ref": "con-synth",
	    "observations": ["${paramitem:`+explore.ParamBriefHypotheses+`:0}"],
	    "finding": {"title": "consolidated from a candidate", "pattern": "no locator behind it",
	                "counter_evidence_absent": true}
	  }]
	}`)
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:    explorePayload,
		explore.StageSynthesize: synthesisPayload,
	}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-synth", Synthesize: true})
	if !errors.Is(err, explore.ErrDevelopmentPath) {
		t.Fatalf("Explore error = %v, want a refused consolidation", err)
	}
	if len(outcome.Findings) != 0 {
		t.Errorf("wrote %d findings from a citation with no locator", len(outcome.Findings))
	}
	// The unsupported addition is still a durable candidate.
	if len(outcome.Hypotheses) != 4 {
		t.Fatalf("persisted %d candidates, want 4 including the synthesizer's addition", len(outcome.Hypotheses))
	}
	addition, err := h.frontier.Hypothesis(context.Background(), outcome.Hypotheses[3])
	if err != nil {
		t.Fatalf("read the addition: %v", err)
	}
	if addition.Status == frontier.StatusPromoted {
		t.Error("an unsupported addition was promoted")
	}
	if !hasFailure(outcome.Synthesis.Body.Failures, explore.FailureDevelopmentPath) {
		t.Errorf("the synthesis receipt does not record the refusal: %+v", outcome.Synthesis.Body.Failures)
	}
}

// TestChallengerFailureLeavesExplorationIntact is §6.5's rule that a failed
// independent exploration does not erase successful work.
func TestChallengerFailureLeavesExplorationIntact(t *testing.T) {
	h := newHarness(t)
	explorePayload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore: explorePayload,
		// A payload the challenger's process cannot read: it dies without a
		// terminal event, which is a worker-level failure of that job alone.
		explore.StageChallenge: filepath.Join(t.TempDir(), "absent.json"),
	}))

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-challenge-fails", Challenge: true})
	if err == nil {
		t.Fatal("a failed challenger was reported as a clean run")
	}
	if len(outcome.Findings) != 1 || len(outcome.Observations) != 2 || len(outcome.Hypotheses) != 3 {
		t.Errorf("the challenger's failure disturbed the exploration: %d hypotheses, %d observations, %d findings",
			len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings))
	}
	ctx := context.Background()
	if _, err := h.frontier.Finding(ctx, outcome.Findings[0]); err != nil {
		t.Errorf("the exploration's finding did not survive: %v", err)
	}
	if len(outcome.Receipt.Body.Failures) != 0 && outcome.Challenge != nil {
		t.Errorf("the exploration's receipt absorbed the challenger's failure: %+v", outcome.Receipt.Body.Failures)
	}
	if !hasFailure(outcome.Failures, explore.FailureWorker) {
		t.Errorf("the challenger's failure was not recorded anywhere: %+v", outcome.Failures)
	}
}

// TestObservationOrderIsIndependentOfRetrievalRank is §5.4's rule that
// retrieval rank never becomes evidence strength. This package is the consumer
// that could break it, so the test drives a real retrieval and then checks
// that the stored observations follow the worker's emission order rather than
// the order the index returned hits in.
func TestObservationOrderIsIndependentOfRetrievalRank(t *testing.T) {
	h := newHarness(t)
	// The result cites the corpus in the reverse of the order retrieval
	// returns it, one observation per candidate.
	result := explore.Result{Consolidations: nil}
	for i := len(h.locators) - 1; i >= 0; i-- {
		result.Candidates = append(result.Candidates, explore.Candidate{
			Ref:        fmt.Sprintf("c-%d", i),
			Hypothesis: frontier.HypothesisPayload{Statement: fmt.Sprintf("candidate citing record %d", i)},
			Observations: []explore.Observation{{
				Ref:    fmt.Sprintf("o-%d", i),
				Recipe: testRecipe,
				Claim: frontier.ObservationPayload{
					Claim:                 fmt.Sprintf("claim citing record %d", i),
					Confidence:            frontier.ConfidenceLow,
					Impact:                frontier.ImpactLow,
					Evidence:              []frontier.Evidence{h.evidence(i, "cited record")},
					CounterEvidenceAbsent: true,
				},
			}},
		})
	}
	payload := h.writeResult("reversed.json", result)
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-request-capability", "corpus-search", "-search-query", "")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{RunID: "r-order"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Retrieval) != 1 {
		t.Fatalf("served %d retrievals, want 1", len(outcome.Retrieval))
	}
	ranked := outcome.Retrieval[0].Hits
	if len(ranked) < len(h.locators) {
		t.Fatalf("retrieval returned %d hits, want at least %d", len(ranked), len(h.locators))
	}

	var stored []string
	for _, id := range outcome.Observations {
		record, err := h.frontier.Observation(context.Background(), id)
		if err != nil {
			t.Fatalf("read observation: %v", err)
		}
		stored = append(stored, record.Payload.Evidence[0].Locator().Digest)
	}
	var emitted []string
	for _, cand := range result.Candidates {
		emitted = append(emitted, cand.Observations[0].Claim.Evidence[0].Locator().Digest)
	}
	if !slices.Equal(stored, emitted) {
		t.Errorf("observations were stored in %v, want the worker's emission order %v", stored, emitted)
	}
	// And the trace still records the ranked order, so the two are genuinely
	// independent rather than accidentally equal.
	var rankedDigests []string
	for _, hit := range ranked[:len(h.locators)] {
		rankedDigests = append(rankedDigests, hit.Locator.Digest)
	}
	if slices.Equal(emitted, rankedDigests) {
		t.Skip("the corpus returned hits in the emission order, so this run cannot distinguish the two")
	}
}

// TestConfidenceIsNeverEvidence checks the claim the package documentation
// makes: no control-plane decision reads a worker's confidence. Two runs whose
// results differ only in their gradings must produce the same records, the same
// promotions and the same failures.
func TestConfidenceIsNeverEvidence(t *testing.T) {
	h := newHarness(t)
	low := h.discovery()
	high := h.discovery()
	for i := range high.Candidates {
		for j := range high.Candidates[i].Observations {
			high.Candidates[i].Observations[j].Claim.Confidence = frontier.ConfidenceHigh
			high.Candidates[i].Observations[j].Claim.Impact = frontier.ImpactHigh
		}
	}
	for i := range low.Candidates {
		for j := range low.Candidates[i].Observations {
			low.Candidates[i].Observations[j].Claim.Confidence = frontier.ConfidenceLow
			low.Candidates[i].Observations[j].Claim.Impact = frontier.ImpactLow
		}
	}

	shape := func(name string, res explore.Result, runID string) (int, int, int, int, int) {
		payload := h.writeResult(name, res)
		controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))
		outcome, err := controller.Explore(context.Background(), explore.Options{RunID: runID})
		if err != nil {
			t.Fatalf("%s: %v (failures %+v)", runID, err, outcome.Failures)
		}
		return len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings),
			len(outcome.Promoted), len(outcome.Deferred)
	}
	lh, lo, lf, lp, ld := shape("low.json", low, "r-low")
	hh, ho, hf, hp, hd := shape("high.json", high, "r-high")
	if lh != hh || lo != ho || lf != hf || lp != hp || ld != hd {
		t.Errorf("confidence changed what the control plane did: low (%d,%d,%d,%d,%d) high (%d,%d,%d,%d,%d)",
			lh, lo, lf, lp, ld, hh, ho, hf, hp, hd)
	}
}

// TestBudgetDefersRatherThanErases is §5.2's rule that a resource limit
// chooses what is explored now, not which ideas may exist.
func TestBudgetDefersRatherThanErases(t *testing.T) {
	h := newHarness(t)
	result := h.discovery()
	result.Consolidations = nil // a consolidation the budget prevents is not the subject here
	payload := h.writeResult("discovery.json", result)
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		RunID:  "r-budget",
		Budget: explore.Budget{Develop: 1},
	})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 3 {
		t.Errorf("persisted %d candidates, want 3", len(outcome.Hypotheses))
	}
	if len(outcome.Observations) != 1 {
		t.Errorf("developed %d observations, want 1 under a budget of 1", len(outcome.Observations))
	}
	if len(outcome.Deferred) != 3 {
		t.Fatalf("deferred %d candidates, want the 3 the pass did not finish", len(outcome.Deferred))
	}
	reasons := map[string]string{}
	for _, candidate := range outcome.Receipt.Body.Deferred {
		reasons[candidate.ID] = candidate.Reason
	}
	if len(reasons) != 3 {
		t.Errorf("the receipt records %d deferred candidates, want 3", len(reasons))
	}
	if !slices.ContainsFunc(outcome.Receipt.Body.Deferred, func(c run.Candidate) bool {
		return strings.Contains(c.Reason, "budget")
	}) {
		t.Errorf("no deferral names the budget that caused it: %+v", outcome.Receipt.Body.Deferred)
	}
}

// TestDurableComponentsShareOneFile protects the wave 2 arrangement: this
// package's resume ledger joins the frontier and the receipts in one durable
// file under its own component key, in either opening order.
func TestDurableComponentsShareOneFile(t *testing.T) {
	dir := t.TempDir()
	ledger, err := explore.OpenLedger(dir)
	if err != nil {
		t.Fatalf("OpenLedger first: %v", err)
	}
	defer ledger.Close()
	f, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open after the ledger: %v", err)
	}
	defer f.Close()
	r, err := run.Open(dir)
	if err != nil {
		t.Fatalf("run.Open after the ledger: %v", err)
	}
	defer r.Close()
	if ledger.Path() != r.Path() || ledger.Path() != f.Path() {
		t.Errorf("the three components disagree on the file: %q, %q, %q",
			ledger.Path(), f.Path(), r.Path())
	}

	// The ledger is idempotent for one binding and refuses a second answer.
	ctx := context.Background()
	commit := explore.Commit{Ref: "c-1", Type: frontier.EntityHypothesis, ID: "hyp-1", At: time.Now()}
	if err := ledger.Record(ctx, "r-1", explore.StageExplore, commit); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := ledger.Record(ctx, "r-1", explore.StageExplore, commit); err != nil {
		t.Errorf("recording the same binding twice: %v", err)
	}
	commit.ID = "hyp-2"
	if err := ledger.Record(ctx, "r-1", explore.StageExplore, commit); !errors.Is(err, explore.ErrLedgerConflict) {
		t.Errorf("rebinding a reference: %v, want a conflict", err)
	}
	committed, err := ledger.Committed(ctx, "r-1", explore.StageExplore)
	if err != nil {
		t.Fatalf("read commits: %v", err)
	}
	if got := committed["c-1"].ID; got != "hyp-1" {
		t.Errorf("the ledger binds c-1 to %q, want hyp-1", got)
	}
}

// secretProbe is the marker inside the planted credential. It doubles as the
// search term that proves what a hosted run is served, and it says what it is
// so nobody mistakes the fixture for a leak.
const secretProbe = "PROBEONLYNOTREAL"

// plantSecret appends a credential-shaped record to the first session and
// re-indexes it, so a preflight over this corpus has a secret finding to force
// redaction with and a retrieval can reach the planted bytes.
//
// The armour header is assembled from parts. A contiguous literal in a
// documented credential format makes the forge reject every push carrying the
// file, which is a remote failure with no local signal; see the note in
// internal/preflight/secret_test.go.
func (h *harness) plantSecret() {
	h.t.Helper()
	stream := h.inputs[0].Stream
	armour := "-----BEGIN" + " RSA PRIVATE KEY-----\\n" + secretProbe + "\\n-----END" + " RSA PRIVATE KEY-----"
	record := `{"type":"message","id":"planted","timestamp":"2026-01-02T03:09:00.000Z",` +
		`"message":{"role":"user","content":[{"type":"text","text":"` + armour + `"}]}}` + "\n"
	file, err := os.OpenFile(stream.Path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		h.t.Fatalf("open %s: %v", stream.Path, err)
	}
	if _, err := file.WriteString(record); err != nil {
		file.Close()
		h.t.Fatalf("append to %s: %v", stream.Path, err)
	}
	file.Close()

	// The retrieval index is a cache over the corpus, so it has to see the
	// appended record before a search can reach it.
	if _, err := h.index.IndexSession(context.Background(), stream); err != nil {
		h.t.Fatalf("re-index %s: %v", stream.Path, err)
	}

	// The preparation and the preflight input must both describe the corpus
	// as it now is: a digest that no longer matches its bytes is what §7's
	// provenance exists to catch.
	reopened, err := os.Open(stream.Path)
	if err != nil {
		h.t.Fatalf("reopen %s: %v", stream.Path, err)
	}
	sum, _, err := digest.Compute(reopened)
	reopened.Close()
	if err != nil {
		h.t.Fatalf("digest %s: %v", stream.Path, err)
	}
	h.inputs[0].Digest = string(sum)
	selection := slices.Clone(h.prep.Selection)
	for i := range selection {
		if selection[i].SourceID == stream.SourceID {
			selection[i].CaptureDigest = sum
			selection[i].SourceDigest = sum
		}
	}
	h.prep, err = run.NewPreparation(h.prep.PreparedAt, selection)
	if err != nil {
		h.t.Fatalf("re-derive the preparation: %v", err)
	}
}

// hasFailure reports whether a receipt records a failure with this code.
func hasFailure(failures []run.Failure, code string) bool {
	return slices.ContainsFunc(failures, func(f run.Failure) bool { return f.Code == code })
}
