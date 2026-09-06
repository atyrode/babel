package explore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/worker"
)

// TestForgedCitationIsRefusedAtSubmissionAndTheCorrectionPersists is the
// provenance check made on Babel's side of the boundary, at the moment it can
// still be corrected: a submission citing a locator the run never served —
// here a real path with a retyped digest, and a served digest moved to a line
// the model did not receive — is answered as a refusal naming the claim, and
// the corrected submission that follows is what becomes durable. Nothing of
// the forgery reaches the frontier.
func TestForgedCitationIsRefusedAtSubmissionAndTheCorrectionPersists(t *testing.T) {
	h := newHarness(t)
	retyped := h.locators[0]
	retyped.Digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	moved := h.locators[1]
	moved.Line++
	forge := func(loc event.Locator) frontier.Evidence {
		ev, err := frontier.NewEvidence(loc, "a citation that looks like provenance")
		if err != nil {
			t.Fatalf("a well-formed forgery must decode: %v", err)
		}
		return ev
	}
	forged := h.discovery()
	forged.Consolidations = nil
	forged.Deferred = nil
	forged.Candidates[0].Observations[0].Claim.Evidence = []frontier.Evidence{forge(retyped)}
	// The second claim's own locator is real; its counter-evidence is not,
	// and a forged counter-citation is the same lie about what was read.
	forged.Candidates[1].Observations[0].Claim.CounterEvidenceAbsent = false
	forged.Candidates[1].Observations[0].Claim.CounterEvidence = []frontier.Evidence{forge(moved)}
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	honest := h.discovery()
	served := filepath.Join(t.TempDir(), "served")
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: h.writeResult("honest.json", honest)}),
		"-submit-invalid", string(forgedJSON), "-submit-invalid-first", "-served-file", served)
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-forged"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	// The model read the refusal, naming the claim and not the path. The
	// served file holds every tool result, and a corpus hit legitimately
	// carries its locator, so only the refusal line is held to that.
	servedBytes, _ := os.ReadFile(served)
	var refusal string
	for _, line := range strings.Split(string(servedBytes), "\n") {
		if strings.HasPrefix(line, worker.ToolSubmit+"\ttrue\t") {
			refusal = line
		}
	}
	if !strings.Contains(refusal, `observation "o-1" cites`) || !strings.Contains(refusal, "no retrieval served") {
		t.Errorf("the model did not read the provenance refusal: %q", refusal)
	}
	if strings.Contains(refusal, h.locators[0].Path) {
		t.Error("the refusal disclosed the cited path")
	}
	requests := outcome.Receipt.Body.Worker.ToolRequests
	var submissions []worker.ToolRecord
	for _, r := range requests {
		if r.Tool == worker.ToolSubmit {
			submissions = append(submissions, r)
		}
	}
	if len(submissions) != 2 || submissions[0].Allowed || !submissions[1].Allowed {
		t.Fatalf("submissions = %+v, want the forgery refused and the correction accepted", submissions)
	}
	// Only the honest result is durable: both observations, one finding.
	if len(outcome.Hypotheses) != 3 || len(outcome.Observations) != 2 || len(outcome.Findings) != 1 {
		t.Errorf("persisted %d candidates, %d observations, %d findings; want 3, 2, 1",
			len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings))
	}
	for _, id := range outcome.Observations {
		record, err := h.frontier.Observation(context.Background(), id)
		if err != nil {
			t.Fatalf("read observation: %v", err)
		}
		for _, ev := range append(record.Payload.Evidence, record.Payload.CounterEvidence...) {
			if ev.Locator() == retyped || ev.Locator() == moved {
				t.Errorf("a forged locator reached the frontier: %+v", ev.Locator())
			}
		}
	}
	if hasFailure(outcome.Receipt.Body.Failures, explore.FailureProvenance) {
		t.Errorf("a refused submission was recorded as a persistence failure: %+v", outcome.Receipt.Body.Failures)
	}
}

// TestSynthesizerConsolidatesServedObservationsUnderItsContract is the
// synthesis path end to end: an exploration develops two claims against served
// evidence, the synthesizer is briefed with them, and its result — one the
// stage's own generated schema admits — becomes a finding, a proposal and two
// promotions.
func TestSynthesizerConsolidatesServedObservationsUnderItsContract(t *testing.T) {
	h := newHarness(t)
	exploration := h.discovery()
	exploration.Consolidations = nil
	explorePayload := h.writeResult("discovery.json", exploration)

	synthesis := explore.Result{
		Consolidations: []explore.Consolidation{{
			Ref: "con-synth",
			// The template below names the brief's identifiers; this copy
			// names two refs so the shape can be checked against the
			// schema before the run mints anything.
			Observations: []string{"obs-a", "obs-b"},
			Finding: frontier.FindingPayload{
				Title:                 "the synthesizer's consolidation",
				Pattern:               "both developed claims describe one shape",
				Significance:          "it is the finding the brief supports",
				Scope:                 []string{"the synthetic corpus"},
				CounterEvidenceAbsent: true,
			},
			Proposal: &frontier.ProposalPayload{
				Title:          "record the shape once",
				Problem:        "the shape recurs across the brief",
				Outcome:        "one record instead of two",
				Impact:         frontier.ImpactModerate,
				Classification: frontier.ClassificationPrivate,
				Supporting:     []frontier.Evidence{h.evidence(0, "the served record behind the first claim")},
			},
		}},
	}
	encoded, err := json.Marshal(synthesis)
	if err != nil {
		t.Fatal(err)
	}
	if err := conforms(explore.OutputContract(explore.StageSynthesize).JSONSchema, encoded); err != nil {
		t.Fatalf("the synthesis result does not conform to the contract the stage is handed: %v", err)
	}
	supporting, _ := json.Marshal(synthesis.Consolidations[0].Proposal.Supporting[0])
	synthesisPayload := h.writeRaw("synthesis.json", `{
	  "consolidations": [{
	    "ref": "con-synth",
	    "observations": ${paramlist:`+explore.ParamBriefObservations+`},
	    "finding": {"title": "the synthesizer's consolidation",
	                "pattern": "both developed claims describe one shape",
	                "significance": "it is the finding the brief supports",
	                "scope": ["the synthetic corpus"], "counter_evidence_absent": true},
	    "proposal": {"title": "record the shape once", "problem": "the shape recurs across the brief",
	                 "outcome": "one record instead of two", "impact": "moderate",
	                 "classification": "private", "supporting": [`+string(supporting)+`]}
	  }]
	}`)
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:    explorePayload,
		explore.StageSynthesize: synthesisPayload,
	}))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-synthesis", Synthesize: true})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if outcome.Synthesis == nil || len(outcome.Synthesis.Body.Failures) != 0 {
		t.Fatalf("the synthesis pass degraded: %+v", outcome.Synthesis)
	}
	if len(outcome.Findings) != 1 || len(outcome.Proposals) != 1 {
		t.Fatalf("synthesis produced %d findings and %d proposals, want one of each",
			len(outcome.Findings), len(outcome.Proposals))
	}
	ctx := context.Background()
	finding, err := h.frontier.Finding(ctx, outcome.Findings[0])
	if err != nil {
		t.Fatalf("read the finding: %v", err)
	}
	if len(finding.ObservationIDs) != 2 || len(finding.HypothesisIDs) != 2 {
		t.Errorf("the finding rests on %d observations across %d candidates, want 2 and 2",
			len(finding.ObservationIDs), len(finding.HypothesisIDs))
	}
	proposal, err := h.frontier.Proposal(ctx, outcome.Proposals[0])
	if err != nil {
		t.Fatalf("read the proposal: %v", err)
	}
	if proposal.Form != frontier.ProposalConsolidated || len(proposal.FindingIDs) != 1 {
		t.Errorf("proposal form = %s over %v, want a consolidated proposal of the one finding",
			proposal.Form, proposal.FindingIDs)
	}
	if len(outcome.Promoted) != 2 {
		t.Errorf("promoted %d candidates, want the 2 the finding consolidated", len(outcome.Promoted))
	}
}
