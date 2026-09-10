package explore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// plantFrontier writes one candidate into the durable frontier and reconciles
// the index against it, which is the state a run finds when the frontier
// already holds prior work.
func plantFrontier(t *testing.T, h *harness, statement string) string {
	t.Helper()
	ctx := context.Background()
	record, err := h.frontier.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-prior",
		Payload: frontier.HypothesisPayload{Statement: statement, Novelty: 0.5, Priority: 0.5},
	})
	if err != nil {
		t.Fatalf("plant a prior candidate: %v", err)
	}
	outputs, err := h.frontier.Outputs(ctx)
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}
	if _, err := h.index.IndexFrontier(ctx, outputs); err != nil {
		t.Fatalf("index the frontier: %v", err)
	}
	return record.ID
}

// oneCandidate is a minimal exploration result: one candidate with the wording
// the test wants Babel to read against the frontier, and nothing else, so a
// dedup warning is attributable to the statement rather than to the fixture.
func oneCandidate(ref, statement string) explore.Result {
	return explore.Result{Candidates: []explore.Candidate{{
		Ref:        ref,
		Hypothesis: frontier.HypothesisPayload{Statement: statement, Novelty: 0.5, Priority: 0.5},
	}}}
}

// TestFrontierScopeSearchIsServedAndReceipted is #87's on-demand
// self-retrieval, driven through the real worker boundary: the synthetic worker
// asks corpus-search for the frontier scope, Babel answers out of its own
// records, and the receipt records the retrieval as a frontier one naming the
// records it disclosed.
func TestFrontierScopeSearchIsServedAndReceipted(t *testing.T) {
	h := newHarness(t)
	prior := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")

	payload := h.writeResult("discovery.json", oneCandidate("c-1", "an unrelated documentation formatting question"))
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-call", worker.ToolSearch,
		"-search-scope", explore.ScopeFrontier,
		"-search-query", "release pipeline integration suite")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-frontier"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}

	if len(outcome.Retrieval) != 1 {
		t.Fatalf("the run served %d retrievals, want 1", len(outcome.Retrieval))
	}
	served := outcome.Retrieval[0]
	if served.Step.Scope != explore.ScopeFrontier {
		t.Errorf("retrieval scope = %q, want %q", served.Step.Scope, explore.ScopeFrontier)
	}
	if len(served.FrontierHits) == 0 {
		t.Fatal("the frontier search served no hits, so a run cannot find its own prior work")
	}
	if served.FrontierHits[0].ID != prior {
		t.Errorf("served hit = %s, want the planted candidate %s", served.FrontierHits[0].ID, prior)
	}

	// The payload the worker received: its own schema, the refine-first
	// note, and the record id a refinement would name.
	var results explore.FrontierResults
	if err := json.Unmarshal(served.Served, &results); err != nil {
		t.Fatalf("the served payload is not a FrontierResults document: %v", err)
	}
	if results.Schema != explore.FrontierResultsSchema {
		t.Errorf("payload schema = %q, want %q", results.Schema, explore.FrontierResultsSchema)
	}
	if results.Note != explore.FramingRefine {
		t.Error("the served payload did not repeat the refine-first framing")
	}
	if len(results.Hits) == 0 || results.Hits[0].ID != prior {
		t.Fatalf("payload hits = %+v, want the planted candidate", results.Hits)
	}
	if results.Hits[0].Summary == "" || !strings.Contains(results.Hits[0].Text, "integration suite") {
		t.Errorf("payload hit carries no readable record: %+v", results.Hits[0])
	}

	// Receipted like a corpus retrieval: one step, scoped, naming the
	// records it disclosed rather than evidence it cannot have.
	body := outcome.Receipt.Body
	if len(body.Retrieval) != 1 {
		t.Fatalf("receipt records %d retrieval steps, want 1", len(body.Retrieval))
	}
	step := body.Retrieval[0]
	if step.Index != 1 || step.Tool != string(worker.CapabilityCorpusSearch) {
		t.Errorf("retrieval step = %+v, want step 1 of corpus-search", step)
	}
	if step.Scope != explore.ScopeFrontier {
		t.Errorf("receipted scope = %q, want %q", step.Scope, explore.ScopeFrontier)
	}
	if len(step.Records) == 0 || step.Records[0] != prior {
		t.Errorf("receipted records = %v, want the disclosed candidate %s", step.Records, prior)
	}
	if len(step.Results) != 0 {
		t.Errorf("a frontier step recorded %d evidence results; a frontier record has no locator to cite",
			len(step.Results))
	}
	if outcome.Receipt.Header.Counts.Retrieval != 1 {
		t.Errorf("receipt counts %d retrievals, want 1", outcome.Receipt.Header.Counts.Retrieval)
	}
	// The step never carries the record's own wording: §9 keeps the payload
	// on the wire and identifiers in the record an operator exports. The
	// query is recorded on purpose, so the phrase checked here is one only
	// the stored candidate holds.
	encoded, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("encode the retrieval step: %v", err)
	}
	if strings.Contains(string(encoded), "claims to run") {
		t.Error("the receipted step carried the record's text")
	}
}

// TestUnservedSearchScopeIsDenied covers the other half of the scope argument.
// A worker that asked for a surface this build does not have has to learn that:
// corpus hits served under a name it did not ask for would read to it as an
// answer about the frontier.
func TestUnservedSearchScopeIsDenied(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", oneCandidate("c-1", "a candidate about nothing in particular"))
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-call", worker.ToolSearch,
		"-search-scope", "everything")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-scope"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Retrieval) != 0 {
		t.Errorf("an unserved scope was answered with %d retrievals", len(outcome.Retrieval))
	}
	// The refused search and the submission that followed it.
	requests := outcome.Receipt.Body.Worker.ToolRequests
	if len(requests) != 2 || requests[1].Tool != worker.ToolSubmit {
		t.Fatalf("receipt records %d tool requests, want the search and the submission: %+v", len(requests), requests)
	}
	if requests[0].Allowed {
		t.Error("a search naming an unserved scope was allowed")
	}
}

// TestNearDuplicateCandidateIsRecordedWithAWarning is #87's honesty rule made
// checkable. A candidate restating a record the frontier already holds is
// written, with a warning naming what it resembles; a distinct candidate is
// written with no warning at all. Neither is dropped, because a duplicate
// silently discarded cannot be recovered and a duplicate recorded can be merged
// by a later revision.
func TestNearDuplicateCandidateIsRecordedWithAWarning(t *testing.T) {
	prior := "the release pipeline skips the integration suite it claims to run"

	t.Run("a near-duplicate is warned about", func(t *testing.T) {
		h := newHarness(t)
		existing := plantFrontier(t, h, prior)
		payload := h.writeResult("discovery.json",
			oneCandidate("c-1", "release runs skip the integration suite they claim to run"))
		controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

		outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-dup"})
		if err != nil {
			t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
		}
		if len(outcome.Hypotheses) != 1 {
			t.Fatalf("the run recorded %d candidates, want the duplicate kept", len(outcome.Hypotheses))
		}
		if len(outcome.Duplicates) != 1 {
			t.Fatalf("warnings = %+v, want one against the planted candidate", outcome.Duplicates)
		}
		warning := outcome.Duplicates[0]
		if warning.HypothesisID != outcome.Hypotheses[0] || warning.DuplicateOf != existing {
			t.Errorf("warning = %+v, want %s resembling %s",
				warning, outcome.Hypotheses[0], existing)
		}
		if warning.Overlap < explore.DuplicateOverlap {
			t.Errorf("warning overlap = %.2f, below the threshold that produced it", warning.Overlap)
		}

		// The warning is durable and reads back with the record, so an
		// operator opening the candidate is told what to compare it with.
		stored, err := h.frontier.Hypothesis(context.Background(), outcome.Hypotheses[0])
		if err != nil {
			t.Fatalf("read the warned candidate: %v", err)
		}
		if len(stored.Duplicates) != 1 || stored.Duplicates[0].DuplicateOf != existing {
			t.Errorf("stored warnings = %+v, want one naming %s", stored.Duplicates, existing)
		}
		if stored.Payload.Statement == "" {
			t.Error("the warned candidate lost its own wording")
		}
	})

	t.Run("a distinct candidate is not", func(t *testing.T) {
		h := newHarness(t)
		plantFrontier(t, h, prior)
		payload := h.writeResult("discovery.json",
			oneCandidate("c-1", "the changelog omits the migration checklist for archived hosts"))
		controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

		outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-distinct"})
		if err != nil {
			t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
		}
		if len(outcome.Hypotheses) != 1 {
			t.Fatalf("the run recorded %d candidates, want 1", len(outcome.Hypotheses))
		}
		if len(outcome.Duplicates) != 0 {
			t.Errorf("a distinct candidate was warned about: %+v", outcome.Duplicates)
		}
	})
}

// TestARunIsWarnedAboutRestatingItsOwnCandidate closes the gap the frontier
// index cannot: it is refreshed before the run starts, so a run's second
// candidate was measured against a snapshot that predates its first.
//
// The shape is the one the real store shows. On 2026-09-10 two runs each
// restated their own explore-stage candidate in the challenge stage five
// minutes later, at 0.61 and 0.71 overlap, and neither restatement carried a
// warning: nothing had indexed the record in between. Nothing here is
// dropped, as ever — the second candidate is durable, and the warning is what
// tells an operator the two belong together.
func TestARunIsWarnedAboutRestatingItsOwnCandidate(t *testing.T) {
	h := newHarness(t)
	result := explore.Result{Candidates: []explore.Candidate{
		{
			Ref:        "c-1",
			Hypothesis: frontier.HypothesisPayload{Statement: "the release pipeline skips the integration suite it claims to run", Novelty: 0.5, Priority: 0.5},
		},
		{
			Ref:        "c-2",
			Hypothesis: frontier.HypothesisPayload{Statement: "release runs skip the integration suite they claim to run", Novelty: 0.5, Priority: 0.5},
		},
	}}
	payload := h.writeResult("self.json", result)
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-self-dup"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 2 {
		t.Fatalf("the run recorded %d candidates, want both kept", len(outcome.Hypotheses))
	}
	if len(outcome.Duplicates) != 1 {
		t.Fatalf("warnings = %+v, want one against the run's own earlier candidate", outcome.Duplicates)
	}
	warning := outcome.Duplicates[0]
	if warning.HypothesisID != outcome.Hypotheses[1] || warning.DuplicateOf != outcome.Hypotheses[0] {
		t.Errorf("warning = %+v, want the second candidate warned against the first (%v)",
			warning, outcome.Hypotheses)
	}
}

// TestPromptCarriesTheRefineFirstContext is the injection half: a preparation
// that names prior outputs puts them in the prompt with their ids and the
// framing that says what they are, and the framing changes when the scope was
// drawn for serendipity.
func TestPromptCarriesTheRefineFirstContext(t *testing.T) {
	for _, tc := range []struct {
		name          string
		serendipitous bool
		framing       string
	}{
		{name: "directed", framing: explore.FramingRefine},
		{name: "serendipitous", serendipitous: true, framing: explore.FramingSerendipity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			prior := plantFrontier(t, h, "the release pipeline skips the integration suite")

			// The same scope, re-fixed with the prior record named.
			prep, err := run.NewPreparation(h.prep.PreparedAt, h.prep.Selection, run.PreparationContext{
				Related:       []run.RelatedOutput{{Kind: string(frontier.OutputHypothesis), ID: prior}},
				Serendipitous: tc.serendipitous,
			})
			if err != nil {
				t.Fatalf("NewPreparation: %v", err)
			}

			payload := h.writeResult("discovery.json", oneCandidate("c-1", "a candidate about something else"))
			promptFile := filepath.Join(t.TempDir(), "prompt.md")
			controller, err := explore.New(h.config(
				append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
					"-prompt-file", promptFile),
				func(cfg *explore.Config) { cfg.Preparation = prep }))
			if err != nil {
				t.Fatalf("explore.New: %v", err)
			}
			outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-context-" + tc.name})
			if err != nil {
				t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
			}

			// The prompt as the engine received it: the context reaches the
			// model as a section of it, not as a document of its own.
			prompt, err := os.ReadFile(promptFile)
			if err != nil {
				t.Fatalf("the fixture wrote no prompt: %v", err)
			}
			_, section, ok := strings.Cut(string(prompt), "## Prior records\n")
			if !ok {
				t.Fatalf("the prompt carries no prior-records section:\n%s", prompt)
			}
			section, _, _ = strings.Cut(section, "\n## ")
			if !strings.Contains(section, tc.framing) {
				t.Errorf("the section does not carry the %s framing:\n%s", tc.name, section)
			}
			if !strings.Contains(section, prior) {
				t.Errorf("the section does not name the planted candidate %s:\n%s", prior, section)
			}
			if !strings.Contains(section, "release pipeline") {
				t.Errorf("the section carries no summary of the record:\n%s", section)
			}
		})
	}
}

// TestFrontierIndexRefreshIsAutomatic pins the ordering that makes the two
// consumers of the frontier surface agree. A run that had to be told to
// reconcile would answer dedup and self-retrieval against whatever earlier
// command last looked, so the controller does it once before any job reads it.
func TestFrontierIndexRefreshIsAutomatic(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Planted directly, with no reconcile: the run has to do it.
	record, err := h.frontier.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-prior",
		Payload: frontier.HypothesisPayload{Statement: "the release pipeline skips the integration suite", Priority: 0.5},
	})
	if err != nil {
		t.Fatalf("plant a prior candidate: %v", err)
	}
	before, err := h.index.FrontierSearch(ctx, index.FrontierQuery{Match: "integration suite"})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("the index already held %d records before the run", len(before))
	}

	payload := h.writeResult("discovery.json", oneCandidate("c-1", "a candidate about something else"))
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))
	if _, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-refresh"}); err != nil {
		t.Fatalf("Explore: %v", err)
	}

	after, err := h.index.FrontierSearch(ctx, index.FrontierQuery{Match: "integration suite"})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("the run did not bring the frontier surface up to date")
	}
	if after[0].ID != record.ID {
		t.Errorf("indexed record = %s, want the planted candidate %s", after[0].ID, record.ID)
	}
}
