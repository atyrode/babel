package frontier

import (
	"context"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/reference"
)

// The tests here are #114's contract: a truth-claim and a value-claim stop
// sharing one body, and everything that follows from that being true rather
// than merely intended.
//
// What each one defends is a decision an operator could otherwise not trust.
// That a remedy cannot address a claim nobody made is what stops the split
// from manufacturing subjects. That the two forms are distinguishable on every
// read is what stops an unbacked want from being shown with an
// evidence-backed consolidation's authority. That the dispositions are
// independent is the whole reason the split was asked for. And that §4.5's
// finding rule still refuses a consolidation with no finding is what says the
// split added a form rather than removing a rule.

func TestACandidateProposalAddressesItsClaimAndRestsOnNoFinding(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	claim, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.7),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("create candidate proposal: %v", err)
	}
	if remedy.Form != ProposalCandidate {
		t.Errorf("form = %q, want %q", remedy.Form, ProposalCandidate)
	}
	if len(remedy.FindingIDs) != 0 {
		t.Errorf("candidate proposal rests on findings %v, want none", remedy.FindingIDs)
	}
	if got := remedy.HypothesisIDs; len(got) != 1 || got[0] != claim.ID {
		t.Errorf("addresses %v, want [%s]", got, claim.ID)
	}

	// The read path derives the same answers as the write path. A form that
	// were only set at creation would be right in the returned value and
	// wrong on every page that loads the record afterwards.
	loaded, err := store.Proposal(ctx, remedy.ID)
	if err != nil {
		t.Fatalf("read the candidate proposal back: %v", err)
	}
	if loaded.Form != ProposalCandidate {
		t.Errorf("loaded form = %q, want %q", loaded.Form, ProposalCandidate)
	}
	if got := loaded.HypothesisIDs; len(got) != 1 || got[0] != claim.ID {
		t.Errorf("loaded addresses %v, want [%s]", got, claim.ID)
	}
}

func TestTheTwoProposalFormsAreDistinguishableOnEveryRead(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	claim, _, finding, consolidated := developPath(t, store)

	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("create candidate proposal: %v", err)
	}

	for _, tc := range []struct {
		name string
		id   string
		want ProposalForm
	}{
		{"consolidated", consolidated.ID, ProposalConsolidated},
		{"candidate", remedy.ID, ProposalCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, err := store.Proposal(ctx, tc.id)
			if err != nil {
				t.Fatalf("read the %s proposal: %v", tc.name, err)
			}
			if record.Form != tc.want {
				t.Errorf("form = %q, want %q", record.Form, tc.want)
			}
		})
	}

	// The consolidated proposal reaches the same claim transitively, through
	// the finding it rests on. Both forms therefore answer "which claims does
	// this proposal answer" and only one of them asserted the answer.
	if got := consolidated.HypothesisIDs; len(got) != 1 || got[0] != claim.ID {
		t.Errorf("consolidated proposal reaches %v, want [%s]", got, claim.ID)
	}
	if len(consolidated.FindingIDs) != 1 || consolidated.FindingIDs[0] != finding.ID {
		t.Errorf("consolidated proposal rests on %v, want [%s]", consolidated.FindingIDs, finding.ID)
	}
}

func TestARemedyCannotAddressAClaimNobodyMade(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	if _, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:   "run-1",
		Payload: proposalPayload("a want about nothing"),
	}); !errors.Is(err, ErrNoAddressedHypotheses) {
		t.Errorf("remedy addressing nothing: got %v, want ErrNoAddressedHypotheses", err)
	}
	if _, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{"hyp_absent"},
		Payload:       proposalPayload("a want about an invented claim"),
	}); !errors.Is(err, ErrUnknownEntity) {
		t.Errorf("remedy addressing an absent claim: got %v, want ErrUnknownEntity", err)
	}
}

// TestTheDevelopmentPathStillRefusesAConsolidationWithNoFinding is the other
// half of the split, and the reason the candidate form is a second constructor
// rather than a relaxed argument to the first. §4.2's path is unchanged.
func TestTheDevelopmentPathStillRefusesAConsolidationWithNoFinding(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	if _, err := store.CreateProposal(ctx, ProposalInput{
		RunID:   "run-1",
		Payload: proposalPayload("premature"),
	}); !errors.Is(err, ErrNoFindings) {
		t.Errorf("consolidation with no finding: got %v, want ErrNoFindings", err)
	}
}

// TestCompetingRemediesCoexistAgainstOneClaim is #114's many-to-many stated as
// a property of the store: nothing collapses two suggestions into one, and
// nothing prefers the first or the last.
func TestCompetingRemediesCoexistAgainstOneClaim(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	claim, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.7),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	other, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("templates are ignored under time pressure", 0.4),
	})
	if err != nil {
		t.Fatalf("create the second hypothesis: %v", err)
	}

	first, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints in the handoff template"),
	})
	if err != nil {
		t.Fatalf("create the first remedy: %v", err)
	}
	second, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("refuse a handoff that names no constraints"),
	})
	if err != nil {
		t.Fatalf("create the competing remedy: %v", err)
	}
	// One remedy may answer several claims, which is the other direction of
	// the same relation.
	both, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID, other.ID},
		Payload:       proposalPayload("make the template the only handoff route"),
	})
	if err != nil {
		t.Fatalf("create the remedy addressing both: %v", err)
	}
	if got := len(both.HypothesisIDs); got != 2 {
		t.Errorf("the remedy addressing both names %d claims, want 2", got)
	}

	addressing, err := store.ProposalsAddressing(ctx, claim.ID)
	if err != nil {
		t.Fatalf("read the remedies addressing the claim: %v", err)
	}
	found := make(map[string]bool, len(addressing))
	for _, record := range addressing {
		found[record.ID] = true
	}
	for _, want := range []string{first.ID, second.ID, both.ID} {
		if !found[want] {
			t.Errorf("remedy %s is missing from the claim's remedies %v", want, found)
		}
	}
	if len(addressing) != 3 {
		t.Errorf("the claim has %d remedies, want 3", len(addressing))
	}
}

// TestRejectingARemedyLeavesItsClaimStanding is the decision #114 exists to
// make possible. It is checked through the disposition history rather than
// through a flag, because the history is what internal/review derives every
// review status from.
func TestRejectingARemedyLeavesItsClaimStanding(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	claim, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.7),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("create candidate proposal: %v", err)
	}

	if _, err := store.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityHypothesis, ID: claim.ID},
		Disposition: DispositionAccept,
		ReviewerID:  "operator",
		Note:        "the claim holds",
	}); err != nil {
		t.Fatalf("accept the claim: %v", err)
	}
	if _, err := store.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityProposal, ID: remedy.ID},
		Disposition: DispositionReject,
		ReviewerID:  "operator",
		Note:        "true, and not what I want done about it",
	}); err != nil {
		t.Fatalf("reject the remedy: %v", err)
	}

	claimStatus, err := store.ReviewStatus(ctx, Ref{Type: EntityHypothesis, ID: claim.ID})
	if err != nil {
		t.Fatalf("read the claim's review status: %v", err)
	}
	if claimStatus != ReviewAccepted {
		t.Errorf("the claim's review status = %q, want %q after its remedy was rejected",
			claimStatus, ReviewAccepted)
	}
	remedyStatus, err := store.ReviewStatus(ctx, Ref{Type: EntityProposal, ID: remedy.ID})
	if err != nil {
		t.Fatalf("read the remedy's review status: %v", err)
	}
	if remedyStatus != ReviewRejected {
		t.Errorf("the remedy's review status = %q, want %q", remedyStatus, ReviewRejected)
	}

	// The claim's own record is untouched by the rejection: §4.7 forbids a
	// decision from deleting or rewriting anything, and the split means a
	// rejected remedy is not even a decision about the claim.
	reloaded, err := store.Hypothesis(ctx, claim.ID)
	if err != nil {
		t.Fatalf("read the claim back: %v", err)
	}
	if reloaded.Payload.Statement != "handoffs drop constraints" {
		t.Errorf("the claim's statement changed to %q", reloaded.Payload.Statement)
	}
}

// TestARemedyMintsAnAddressesEdgeToEachClaimItAnswers checks the graph shadow.
// Both forms emit it: the edge answers "what does this suggestion answer",
// which is one question whichever table established the answer.
func TestARemedyMintsAnAddressesEdgeToEachClaimItAnswers(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)
	claim, _, _, consolidated := developPath(t, store)

	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-2",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints up front"),
		Actor:         Run("run-2"),
	})
	if err != nil {
		t.Fatalf("create candidate proposal: %v", err)
	}

	edges := appender.of(reference.KindAddresses)
	want := map[string]bool{consolidated.ID: false, remedy.ID: false}
	for _, e := range edges {
		if e.To.Kind != string(EntityHypothesis) || e.To.ID != claim.ID {
			t.Errorf("an addresses edge points at %s, want hypothesis:%s", e.To, claim.ID)
		}
		if e.From.Kind != string(EntityProposal) {
			t.Errorf("an addresses edge comes from %s, want a proposal", e.From)
		}
		if e.Note == "" {
			t.Errorf("the addresses edge from %s carries no hedge; a keyless reader "+
				"would see an arrow into a claim and nothing calling it a want", e.From)
		}
		if _, ok := want[e.From.ID]; ok {
			want[e.From.ID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("proposal %s minted no addresses edge to the claim it answers", id)
		}
	}
	if len(*warnings) != 0 {
		t.Errorf("edge emission warned: %v", *warnings)
	}
}

// TestAnEdgeStoreThatRefusesDoesNotCostAProposalItsDurability holds the line
// frontier/reference.go's package prose draws: the record is the authority and
// the graph is its shadow, so a refused edge is a warning and never a failed
// write. §5.2 requires every emitted candidate to be persisted, and #114 makes
// the remedy part of that emission.
func TestAnEdgeStoreThatRefusesDoesNotCostAProposalItsDurability(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)
	claim, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.7),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	appender.fail = errors.New("the edge store is unreachable")

	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{claim.ID},
		Payload:       proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("a refused edge cost the remedy its durability: %v", err)
	}
	if _, err := store.Proposal(ctx, remedy.ID); err != nil {
		t.Fatalf("the remedy is not readable: %v", err)
	}
	if len(*warnings) == 0 {
		t.Error("a refused edge produced no warning; the missing edge would be silent")
	}
}
