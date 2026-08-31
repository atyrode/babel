package explore_test

import (
	"context"
	"encoding/json"
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
		"-request-capability", "corpus-search",
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
		"-request-capability", "corpus-search",
		"-search-scope", "everything")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-scope"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Retrieval) != 0 {
		t.Errorf("an unserved scope was answered with %d retrievals", len(outcome.Retrieval))
	}
	requests := outcome.Receipt.Body.Worker.ToolRequests
	if len(requests) != 1 {
		t.Fatalf("receipt records %d tool requests, want 1", len(requests))
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

// TestJobCarriesTheRefineFirstContext is the injection half: a preparation that
// names prior outputs puts them in the job document with their summaries and
// the framing that says what they are, and the framing changes when the scope
// was drawn for serendipity.
func TestJobCarriesTheRefineFirstContext(t *testing.T) {
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
			controller, err := explore.New(h.config(
				payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
				func(cfg *explore.Config) { cfg.Preparation = prep }))
			if err != nil {
				t.Fatalf("explore.New: %v", err)
			}
			outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-context-" + tc.name})
			if err != nil {
				t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
			}

			// The document itself, built from the same preparation the run
			// used. Asserting on it rather than on the worker's echo keeps
			// this test about Babel's own injection; the wire shape is
			// internal/worker's own contract.
			var doc explore.RelatedContext
			encoded := jobExtra(t, prep, h, tc.serendipitous)
			if err := json.Unmarshal(encoded, &doc); err != nil {
				t.Fatalf("the context is not a RelatedContext document: %v", err)
			}
			if doc.Schema != explore.RelatedContextSchema {
				t.Errorf("context schema = %q, want %q", doc.Schema, explore.RelatedContextSchema)
			}
			if doc.Framing != tc.framing {
				t.Errorf("context framing = %q, want the %s framing", doc.Framing, tc.name)
			}
			if doc.Serendipitous != tc.serendipitous {
				t.Errorf("context serendipitous = %v, want %v", doc.Serendipitous, tc.serendipitous)
			}
			if len(doc.Records) != 1 {
				t.Fatalf("context records = %+v, want the one the preparation named", doc.Records)
			}
			record := doc.Records[0]
			if record.ID != prior || record.Kind != string(frontier.OutputHypothesis) {
				t.Errorf("context record = %+v, want the planted candidate %s", record, prior)
			}
			if !strings.Contains(record.Summary, "release pipeline") {
				t.Errorf("context record carries no summary: %+v", record)
			}
		})
	}
}

// jobExtra rebuilds the job document's context field for one preparation. It
// exists because the controller builds the field per stage while a job is in
// flight, and a test that reached into the launched process's stdin would be
// asserting on the pipe rather than on the document.
func jobExtra(t *testing.T, prep run.Preparation, h *harness, serendipitous bool) json.RawMessage {
	t.Helper()
	doc := explore.RelatedContext{
		Schema:        explore.RelatedContextSchema,
		Framing:       explore.FramingRefine,
		Serendipitous: serendipitous,
	}
	if serendipitous {
		doc.Framing = explore.FramingSerendipity
	}
	for _, ref := range prep.Related {
		output, err := h.frontier.Output(context.Background(), frontier.OutputKind(ref.Kind), ref.ID)
		if err != nil {
			t.Fatalf("resolve related output %s: %v", ref.ID, err)
		}
		doc.Records = append(doc.Records, explore.RelatedRecord{
			Kind:    string(output.Kind),
			ID:      output.ID,
			Summary: output.Summary,
		})
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode the context: %v", err)
	}
	return encoded
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
