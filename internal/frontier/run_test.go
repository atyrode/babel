package frontier_test

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

// TestOneRunsOutputsIncludeItsSeparateJobs is the run page's missing half.
//
// SPEC.md §5.4 makes the challenger and the synthesizer separate jobs with
// their own run identity, and internal/explore spells those identities
// `<run>/<stage>` in the very column this query filters on. So an exploration
// that objected and then consolidated wrote its candidates under `run-x` and
// its finding under `run-x/synthesize`, and a query on the bare id answered
// with two thirds of a run's work while the page reported the rest as records
// the index could not answer for.
//
// The neighbours are the other half of the assertion. A prefix range is only
// right if it is exclusive at both ends, so the fixture holds the two run ids
// that bracket `run-x/`: `run-x-2`, whose separator sorts below it, and
// `run-x0`, which is the range's own exclusive upper bound.
func TestOneRunsOutputsIncludeItsSeparateJobs(t *testing.T) {
	store, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	explored := writeRunRecords(t, store, "run-x", "the explored candidate")
	objected := writeRunRecords(t, store, "run-x/challenge", "the challenger's objection")
	consolidated := writeRunRecords(t, store, "run-x/synthesize", "the synthesizer's claim")
	below := writeRunRecords(t, store, "run-x-2", "another run entirely")
	above := writeRunRecords(t, store, "run-x0", "the run one byte past the range")

	outputs, err := store.OutputsOfRun(ctx, "run-x")
	if err != nil {
		t.Fatalf("OutputsOfRun: %v", err)
	}
	got := map[string]bool{}
	for _, output := range outputs {
		got[output.ID] = true
	}
	for _, want := range append(append([]string{}, explored...), append(objected, consolidated...)...) {
		if !got[want] {
			t.Errorf("record %s is missing: a run's own jobs are the run's output", want)
		}
	}
	for _, unwanted := range append(below, above...) {
		if got[unwanted] {
			t.Errorf("record %s of a neighbouring run was credited to run-x", unwanted)
		}
	}
	if len(outputs) != len(got) {
		t.Errorf("OutputsOfRun returned %d rows for %d records", len(outputs), len(got))
	}

	// A stage names a job and not a run, so asking about one answers for that
	// job alone. The reduction runs downward only: a row for `run-x/synthesize`
	// beside a row for `run-x` must not report the same records twice.
	stage, err := store.OutputsOfRun(ctx, "run-x/synthesize")
	if err != nil {
		t.Fatalf("OutputsOfRun for a stage: %v", err)
	}
	if len(stage) != len(consolidated) {
		t.Fatalf("the synthesize stage answered with %d records, want its own %d",
			len(stage), len(consolidated))
	}
	for _, output := range stage {
		if !containsID(consolidated, output.ID) {
			t.Errorf("record %s is not the synthesize stage's own", output.ID)
		}
	}

	// An empty run id names no run, and a query on it must not answer with
	// every record whose run this reader could not read.
	none, err := store.OutputsOfRun(ctx, "")
	if err != nil || len(none) != 0 {
		t.Errorf("OutputsOfRun(\"\") = %d records, %v; want nothing", len(none), err)
	}
}

// writeRunRecords writes one candidate, one observation, one finding and one
// proposal under a single run identity, which is the four tables OutputsOfRun
// reads. It answers with their ids in no particular order: what the test
// asserts is membership, and the ordering is sortRunOutputs' own contract.
func writeRunRecords(t *testing.T, store *frontier.Store, runID, text string) []string {
	t.Helper()
	ctx := context.Background()

	hypothesis, err := store.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   runID,
		Payload: frontier.HypothesisPayload{Statement: text},
	})
	if err != nil {
		t.Fatalf("hypothesis for %s: %v", runID, err)
	}
	locator := event.Locator{Path: "/synthetic/log.jsonl", Line: 1, ByteOffset: 0,
		Digest: "0000000000000000000000000000000000000000000000000000000000000000"}
	evidence, err := frontier.NewEvidence(locator, text)
	if err != nil {
		t.Fatalf("evidence for %s: %v", runID, err)
	}
	observation, err := store.CreateObservation(ctx, frontier.ObservationInput{
		HypothesisID:  hypothesis.ID,
		RunID:         runID,
		RecipeID:      "synthetic-lens",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 text,
			Confidence:            frontier.ConfidenceLow,
			Impact:                frontier.ImpactLow,
			Evidence:              []frontier.Evidence{evidence},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("observation for %s: %v", runID, err)
	}
	finding, err := store.CreateFinding(ctx, frontier.FindingInput{
		RunID:          runID,
		ObservationIDs: []string{observation.ID},
		Payload: frontier.FindingPayload{
			Title:                 text,
			Pattern:               text + ", recurring",
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("finding for %s: %v", runID, err)
	}
	proposal, err := store.CreateProposal(ctx, frontier.ProposalInput{
		RunID:      runID,
		FindingIDs: []string{finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          text,
			Problem:        text + ", unaddressed",
			Outcome:        text + ", addressed",
			Impact:         frontier.ImpactLow,
			Classification: frontier.ClassificationPrivate,
		},
	})
	if err != nil {
		t.Fatalf("proposal for %s: %v", runID, err)
	}
	return []string{hypothesis.ID, observation.ID, finding.ID, proposal.ID}
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
