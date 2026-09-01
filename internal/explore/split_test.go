package explore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
)

// remedyPayload is the value-claim half a candidate carries. It is separate
// from the fixture's consolidated proposal so a test can tell which of the two
// forms it is looking at by title alone.
func remedyPayload(title string) frontier.ProposalPayload {
	return frontier.ProposalPayload{
		Title:          title,
		Problem:        "constraints are restated late",
		Outcome:        "state constraints in the handoff template",
		Impact:         frontier.ImpactLow,
		Classification: frontier.ClassificationPrivate,
	}
}

// TestACandidateCarryingARemedyEmitsBothRecordsAndTheEdge is #114's emission
// contract: one candidate, two records, one `addresses` edge, and neither
// record standing in for the other.
func TestACandidateCarryingARemedyEmitsBothRecordsAndTheEdge(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("split.json", explore.Result{
		Candidates: []explore.Candidate{
			{
				Ref:        "c-claim-and-remedy",
				Hypothesis: frontier.HypothesisPayload{Statement: "handoffs drop constraints", Novelty: 0.4, Priority: 0.9},
				Remedy: &explore.Remedy{
					Ref:      "r-1",
					Proposal: remedyPayload("state constraints up front"),
				},
			},
			{
				// A candidate that says only what is the case. #114's
				// honest default, and the reason the remedy is a pointer.
				Ref:        "c-claim-only",
				Hypothesis: frontier.HypothesisPayload{Statement: "templates are ignored under time pressure", Priority: 0.2},
			},
		},
	})
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	ctx := context.Background()
	outcome, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-split"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 2 {
		t.Fatalf("persisted %d hypotheses, want 2", len(outcome.Hypotheses))
	}
	if len(outcome.Proposals) != 1 {
		t.Fatalf("persisted %d proposals, want 1 - the remedy of the first candidate only",
			len(outcome.Proposals))
	}

	remedy, err := h.frontier.Proposal(ctx, outcome.Proposals[0])
	if err != nil {
		t.Fatalf("the remedy is not a durable proposal: %v", err)
	}
	if remedy.Form != frontier.ProposalCandidate {
		t.Errorf("the remedy's form = %q, want %q", remedy.Form, frontier.ProposalCandidate)
	}
	if len(remedy.FindingIDs) != 0 {
		t.Errorf("the remedy rests on findings %v; a candidate remedy travels no part of "+
			"the development path", remedy.FindingIDs)
	}
	if remedy.Payload.Title != "state constraints up front" {
		t.Errorf("the remedy's title = %q, want the wording the worker emitted", remedy.Payload.Title)
	}

	// The remedy addresses the claim its own candidate produced, and the
	// claim's statement is untouched by carrying a remedy: the split is only
	// worth anything if the truth-claim survives it verbatim.
	claim, err := h.frontier.Hypothesis(ctx, outcome.Hypotheses[0])
	if err != nil {
		t.Fatalf("read the claim: %v", err)
	}
	if got := remedy.HypothesisIDs; len(got) != 1 || got[0] != claim.ID {
		t.Fatalf("the remedy addresses %v, want [%s]", got, claim.ID)
	}
	if claim.Payload.Statement != "handoffs drop constraints" {
		t.Errorf("the claim's statement = %q, want the wording the worker emitted",
			claim.Payload.Statement)
	}

	// The claim that carried no remedy has none. A run that invented one
	// would be putting words in the worker's mouth.
	other, err := h.frontier.ProposalsAddressing(ctx, outcome.Hypotheses[1])
	if err != nil {
		t.Fatalf("read the second claim's remedies: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("a claim emitted without a remedy has %d, want none", len(other))
	}
}

// TestAChallengerCannotSuggestAChange holds §5.4's boundary. The challenger is
// granted criticism; prescribing the fix inside the stage that raised the
// objection would let a critic answer itself.
func TestAChallengerCannotSuggestAChange(t *testing.T) {
	h := newHarness(t)
	explorePayload := h.writeResult("discovery.json", h.discovery())
	challengePayload := h.writeResult("challenge.json", explore.Result{
		Candidates: []explore.Candidate{{
			Ref:        "c-critic",
			Hypothesis: frontier.HypothesisPayload{Statement: "the critic's own candidate", Priority: 0.3},
			Remedy: &explore.Remedy{
				Ref:      "r-critic",
				Proposal: remedyPayload("the critic prescribes the fix"),
			},
		}},
	})
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:   explorePayload,
		explore.StageChallenge: challengePayload,
	}))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		Authority: testAuthority, RunID: "r-critic", Challenge: true,
	})
	// An authority violation ends the run, exactly as it does when a
	// challenger tries to consolidate: a job that exceeded its grant is a job
	// to distrust, not one item to drop.
	if !errors.Is(err, explore.ErrStageAuthority) {
		t.Fatalf("Explore error = %v, want the challenger refused remedy authority", err)
	}
	var refused bool
	for _, failure := range outcome.Failures {
		if failure.Stage == string(explore.StageChallenge) &&
			failure.Code == string(explore.FailureAuthority) &&
			strings.Contains(failure.Message, "remedy") {
			refused = true
		}
	}
	if !refused {
		t.Errorf("the refusal does not name the remedy; failures %+v", outcome.Failures)
	}
	// The exploration's three candidates keep their records, and so does the
	// critic's own fourth: §5.4 permits a challenger to emit a candidate, and
	// §5.2 requires every emitted candidate to be persisted. The refusal is
	// against the remedy alone, which is the split's promise carried into the
	// error path - an unauthorized suggestion must not take a persisted claim
	// down with it.
	if len(outcome.Hypotheses) != 4 {
		t.Errorf("the challenger's refused remedy cost a claim its record: %d hypotheses, want 4",
			len(outcome.Hypotheses))
	}
	// The challenger contributed no proposal. The one that exists is the
	// exploration fixture's consolidation.
	if len(outcome.Proposals) != 1 {
		t.Errorf("persisted %d proposals, want only the exploration's consolidated one",
			len(outcome.Proposals))
	}
}

// TestAResumedRunNeitherLosesNorDuplicatesARemedy checks that the remedy's own
// ref reaches the resume ledger. Sharing the candidate's ref would make a
// resumed attempt unable to say which half of the pair it had written, which is
// exactly the ambiguity the ledger exists to remove.
func TestAResumedRunNeitherLosesNorDuplicatesARemedy(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("split.json", explore.Result{
		Candidates: []explore.Candidate{{
			Ref:        "c-1",
			Hypothesis: frontier.HypothesisPayload{Statement: "handoffs drop constraints", Priority: 0.9},
			Remedy: &explore.Remedy{
				Ref:      "r-1",
				Proposal: remedyPayload("state constraints up front"),
			},
		}},
	})
	controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}))

	ctx := context.Background()
	first, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-resume"})
	if err != nil {
		t.Fatalf("first attempt: %v (failures %+v)", err, first.Failures)
	}
	if len(first.Proposals) != 1 {
		t.Fatalf("first attempt persisted %d proposals, want 1", len(first.Proposals))
	}

	second, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-resume"})
	if err != nil {
		t.Fatalf("resumed attempt: %v (failures %+v)", err, second.Failures)
	}
	if len(second.Proposals) != 1 || second.Proposals[0] != first.Proposals[0] {
		t.Errorf("resumed attempt produced proposals %v, want the same single %v",
			second.Proposals, first.Proposals)
	}
	remedies, err := h.frontier.ProposalsAddressing(ctx, first.Hypotheses[0])
	if err != nil {
		t.Fatalf("read the claim's remedies: %v", err)
	}
	if len(remedies) != 1 {
		t.Errorf("the claim has %d remedies after a resume, want 1", len(remedies))
	}
}

// TestTheRemedyEdgeReachesTheReferenceGraph checks the emission that makes the
// pair navigable: without it a reader browsing the corpus would meet two
// unrelated records where one candidate stood.
//
// The appender is attached to the frontier store rather than to the
// controller, because that is where the edge is minted and where a wired
// deployment attaches it (internal/cli's analysis state opens the frontier
// with WithReferences). Wiring the controller's own appender instead would
// assert nothing about this edge and would pass on a build that emitted none.
func TestTheRemedyEdgeReachesTheReferenceGraph(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("split.json", explore.Result{
		Candidates: []explore.Candidate{{
			Ref:        "c-1",
			Hypothesis: frontier.HypothesisPayload{Statement: "handoffs drop constraints", Priority: 0.9},
			Remedy: &explore.Remedy{
				Ref:      "r-1",
				Proposal: remedyPayload("state constraints up front"),
			},
		}},
	})
	appender := &recordingAppender{}
	front := h.referencedFrontier(appender)
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		func(cfg *explore.Config) { cfg.Frontier = front },
	)

	ctx := context.Background()
	outcome, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-edge"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Proposals) != 1 || len(outcome.Hypotheses) != 1 {
		t.Fatalf("persisted %d hypotheses and %d proposals, want one of each",
			len(outcome.Hypotheses), len(outcome.Proposals))
	}

	want := "addresses proposal:" + outcome.Proposals[0] + "->hypothesis:" + outcome.Hypotheses[0]
	if got := appender.keys(reference.KindAddresses); len(got) != 1 || got[0] != want {
		t.Errorf("addresses edges = %v, want [%s]", got, want)
	}
}

// referencedFrontier reopens the harness's durable frontier with the typed
// reference graph's write half attached, which is what internal/cli does in a
// wired deployment. The original store is closed first: one durable file, one
// writer.
func (h *harness) referencedFrontier(a reference.Appender) *frontier.Store {
	h.t.Helper()
	if err := h.frontier.Close(); err != nil {
		h.t.Fatalf("close the plain frontier: %v", err)
	}
	front, err := frontier.Open(h.dir, frontier.WithReferences(a, func(err error) {
		h.t.Errorf("edge emission warned: %v", err)
	}))
	if err != nil {
		h.t.Fatalf("reopen the frontier with references: %v", err)
	}
	h.frontier = front
	h.t.Cleanup(func() { _ = front.Close() })
	return front
}
