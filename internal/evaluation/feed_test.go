package evaluation

// The deployment-wide reception read (SPEC.md §8.7), held to what a feed shows.

import (
	"context"
	"testing"
)

// TestOperatorFeedbackCarryingOnlyAReasonIsAComment is §8.7's comment box at
// the gate it passes through.
//
// A comment is a feedback record with a reason and no polarity: the operator
// says something without voting, which §4.12 already admits — "a scoped reason
// with no position is still the act it named" — and §8.7 asks for by name. The
// assertion is that the stance stays empty rather than acquiring one, because
// a box that voted for the person typing into it would make one gesture do two
// things and leave no way to tell which he meant.
func TestOperatorFeedbackCarryingOnlyAReasonIsAComment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	record, err := h.store.Operator(ctx, OperatorInput{
		Subject:  proposalSubject(),
		Kind:     KindFeedback,
		Operator: "alex",
		Reason:   "the verification criterion is the part I care about",
	})
	if err != nil {
		t.Fatalf("record a comment: %v", err)
	}
	if record.Stance != "" {
		t.Errorf("stance = %q, want none: a comment states no position", record.Stance)
	}
	if record.Reason != "the verification criterion is the part I care about" {
		t.Errorf("reason = %q, want his words kept verbatim", record.Reason)
	}
	if record.ActorKind != ActorOperator || record.ActorID != "alex" {
		t.Errorf("attribution = %s/%s", record.ActorKind, record.ActorID)
	}

	// The empty act is still refused, which is what makes the one above a
	// statement rather than a click: a feedback record with neither a reason
	// nor a stance would record attention nobody paid.
	if _, err := h.store.Operator(ctx, OperatorInput{
		Subject: proposalSubject(), Kind: KindFeedback, Operator: "alex",
	}); err == nil {
		t.Error("an empty feedback record was accepted")
	}
}

// TestFeedbackAndVotesFoldIntoOneTallyPerSubject is the grouping the front
// page counts with.
//
// Three rules are asserted together because they are one rule applied to the
// three things a subject accumulates. One vote per run per role, which is
// §4.12's own limit: the same reviewer answering the same question twice is a
// changed mind rather than two votes. The operator's newest stance, which is
// the newest *stance* and not the newest record. And prose counted as a
// comment where a bare vote is not, because the score already carries the vote
// and an empty row in a conversation says nothing.
func TestFeedbackAndVotesFoldIntoOneTallyPerSubject(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first := h.claim(t, "asg_reception_a", RoleReception, testRun)
	original := submit(t, h, first, &Assessment{Vote: VoteSupport, Contributions: []Contribution{
		{Kind: ContributionComment, Text: "the scope reads wider than the evidence"},
	}})
	second := h.claim(t, "asg_reception_b", RoleReception, otherRun)
	submit(t, h, second, &Assessment{Vote: VoteOppose})
	third := h.claim(t, "asg_reception_c", RoleReception, "run_0003")
	submit(t, h, third, &Assessment{Vote: VoteSupport})
	// A contribution with no vote is a complete assessment and is a comment
	// rather than a vote, which is the distinction the two columns keep.
	fourth := h.claim(t, "asg_evidence_a", RoleEvidence, testRun)
	submit(t, h, fourth, &Assessment{Contributions: []Contribution{
		{Kind: ContributionObjection, Text: "the locator does not show what the claim says"},
	}})

	if _, err := h.store.Operator(ctx, OperatorInput{
		Subject: proposalSubject(), Kind: KindFeedback, Operator: "alex",
		Stance: StanceAgree, Reason: "this is the right remedy",
	}); err != nil {
		t.Fatalf("record a stance: %v", err)
	}
	// A later reason with no position withdraws nothing, which is the one
	// way a newest-record rule and a newest-stance rule differ.
	if _, err := h.store.Operator(ctx, OperatorInput{
		Subject: proposalSubject(), Kind: KindFeedback, Operator: "alex",
		Reason: "still waiting on the benchmark",
	}); err != nil {
		t.Fatalf("record a comment: %v", err)
	}

	tallies, err := h.store.Tallies(ctx)
	if err != nil {
		t.Fatalf("Tallies: %v", err)
	}
	tally := tallies[proposalSubject()]
	if tally.Support != 2 || tally.Oppose != 1 || tally.Unsure != 0 {
		t.Errorf("votes = %d/%d/%d, want two supports and one opposition",
			tally.Support, tally.Oppose, tally.Unsure)
	}
	if tally.Stance != StanceAgree {
		t.Errorf("stance = %q, want the newest position rather than the newest record", tally.Stance)
	}
	// Two pieces of reviewer prose plus two operator reasons; the two bare
	// votes contribute nothing.
	if tally.Comments != 4 {
		t.Errorf("comments = %d, want the four things that were actually said", tally.Comments)
	}
	if tally.LastActivity.IsZero() || len(tally.Activity) == 0 {
		t.Errorf("tally = %+v, want the times a rising rank counts", tally)
	}

	// A correction replaces the statement it supersedes rather than adding
	// to it: the run that supported now opposes, and the deployment holds
	// one vote from it either way.
	// A correction cannot claim a blinded read: it revises a statement its
	// own author had to read, which §5.8's blinding was never about.
	if _, err := h.store.Correct(ctx, original.ID, Submission{
		AssignmentID: first.ID,
		RunID:        testRun,
		Fence:        first.Fence,
		Assessment:   &Assessment{Vote: VoteOppose},
		Provenance:   Provenance{Model: "m", Profile: "p", Recipe: "r", RecipeVersion: 1},
	}); err != nil {
		t.Fatalf("Correct: %v", err)
	}
	corrections, err := h.store.Tallies(ctx)
	if err != nil {
		t.Fatalf("Tallies: %v", err)
	}
	after := corrections[proposalSubject()]
	if after.Support != 1 || after.Oppose != 2 {
		t.Errorf("votes after a correction = %d/%d, want the superseded support dropped",
			after.Support, after.Oppose)
	}
}

// TestTheOperatorReadsOneSubjectsWholeThread is what the comment route
// renders from.
//
// It reads the durable records rather than the projection, which is the
// property worth asserting: this harness has never swept, so a thread served
// from the projection would be empty and the conversation under every record
// would disappear until the next rebuild.
func TestTheOperatorReadsOneSubjectsWholeThread(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	granted := h.claim(t, "asg_thread", RoleEvidence, testRun)
	submit(t, h, granted, &Assessment{Contributions: []Contribution{
		{Kind: ContributionObjection, Text: "the locator does not show what the claim says"},
	}})
	if _, err := h.store.Operator(ctx, OperatorInput{
		Subject: proposalSubject(), Kind: KindFeedback, Operator: "alex",
		Reason: "the objection is the part I want answered",
	}); err != nil {
		t.Fatalf("record a comment: %v", err)
	}

	thread, err := h.store.Thread(ctx, proposalSubject())
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	var assessments, feedback int
	for _, entry := range thread {
		switch entry.Record.Kind {
		case KindAssessment:
			assessments++
			if entry.Role != RoleEvidence {
				t.Errorf("role = %q, want the one the grant authorized", entry.Role)
			}
		case KindFeedback:
			feedback++
			if entry.Role != "" {
				t.Errorf("an operator's comment carries role %q", entry.Role)
			}
		}
	}
	if assessments != 1 || feedback != 1 {
		t.Fatalf("thread = %d assessments and %d comments, want one of each", assessments, feedback)
	}
	// A subject nobody has said anything about has an empty conversation
	// rather than a failure: it is a record with no comments, which is the
	// ordinary case on a young deployment.
	quiet, err := h.store.Thread(ctx, Subject{Kind: "proposal", ID: "prop_silent"})
	if err != nil || len(quiet) != 0 {
		t.Fatalf("a quiet subject gave %d records and %v", len(quiet), err)
	}
}

// submit records one completed assessment against a granted assignment and
// hands back the record, which is what a correction has to name.
func submit(t *testing.T, h *harness, granted Assignment, assessment *Assessment) Record {
	t.Helper()
	record, err := h.store.Submit(context.Background(), Submission{
		AssignmentID: granted.ID,
		RunID:        granted.RunID,
		Fence:        granted.Fence,
		Assessment:   assessment,
		Provenance:   testProvenance(),
	})
	if err != nil {
		t.Fatalf("submit %s: %v", granted.ID, err)
	}
	return record
}

func testProvenance() Provenance {
	return Provenance{Model: "m", Profile: "p", Recipe: "r", RecipeVersion: 1, Blinded: true}
}
