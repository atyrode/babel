package web

import (
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// TestTriageAdviceReachesThePageTheOperatorDecidesOn is the visibility half of
// the authority line.
//
// Advice that a person has to go and look for is advice that arrives after the
// decision it was written for, so the property is not that the record exists
// but that it is on the document the review page loads for a proposal — and
// that it is on the alternative's document too, because from there the advice
// is the only account of where that second record came from.
func TestTriageAdviceReachesThePageTheOperatorDecidesOn(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	advice, alternative, err := h.front.Triage().AdviseWithAlternative(h.ctx,
		frontier.TriageInput{
			ProposalID: h.proposal.ID,
			RunID:      "run-triage",
			Payload: frontier.TriageAdvicePayload{
				Rank: 1, Cohort: 2,
				Ranking:         "it names the smaller change",
				CounterArgument: "one session is thin evidence for a convention",
			},
		},
		frontier.ProposalPayload{
			Title:          "state the one constraint that was dropped",
			Problem:        "the broader wording touches every handoff",
			Outcome:        "a narrower change with the same support",
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
		},
	)
	if err != nil {
		t.Fatalf("advise with an alternative: %v", err)
	}

	var detail proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+h.proposal.ID), &detail)
	if len(detail.Triage) != 1 {
		t.Fatalf("proposal detail carries %d pieces of advice, want 1", len(detail.Triage))
	}
	shown := detail.Triage[0]
	if shown.CounterArgument != "one session is thin evidence for a convention" {
		t.Errorf("counter-argument = %q, want the pass's own words", shown.CounterArgument)
	}
	if shown.Rank != 1 || shown.Cohort != 2 {
		t.Errorf("rank = %d of %d, want 1 of 2", shown.Rank, shown.Cohort)
	}
	if shown.AlternativeID != alternative.ID {
		t.Errorf("advice names alternative %q, want %q", shown.AlternativeID, alternative.ID)
	}
	// The record it is about is still waiting. A page that showed advice
	// beside a ruling would be presenting a settled question as open, and
	// the review status is where that would show first.
	if detail.ReviewStatus != string(frontier.ReviewNew) {
		t.Errorf("advised proposal review status = %q, want %q", detail.ReviewStatus, frontier.ReviewNew)
	}

	var offered proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+alternative.ID), &offered)
	if len(offered.Triage) != 1 || offered.Triage[0].ID != advice.ID {
		t.Fatalf("the alternative's page carries %+v, want the advice that offered it", offered.Triage)
	}
	if offered.Triage[0].ProposalID != h.proposal.ID {
		t.Errorf("the alternative's advice names %q as its subject, want %q",
			offered.Triage[0].ProposalID, h.proposal.ID)
	}

	// An untriaged proposal renders as one: the field is absent rather than
	// present and empty, so a client cannot show an advice block for a
	// record nothing was said about.
	plain, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-3",
		FindingIDs: []string{h.finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "a proposal no pass has read",
			Problem:        "nothing was said about it",
			Outcome:        "it renders without an advice block",
			Impact:         frontier.ImpactLow,
			Classification: frontier.ClassificationPrivate,
		},
	})
	if err != nil {
		t.Fatalf("create an untriaged proposal: %v", err)
	}
	var untriaged proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+plain.ID), &untriaged)
	if len(untriaged.Triage) != 0 {
		t.Errorf("an untriaged proposal carries %+v, want no advice", untriaged.Triage)
	}
}
