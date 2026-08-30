package review_test

import (
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
)

// TestEachModeProducesExactlyItsDescendants is §4.7's rule, mode by mode:
// `none` a revised descendant and nothing lasting, `alongside` both, `instead`
// only the lasting context. The negative half matters as much as the positive
// one — a mode that quietly accepted an extra descendant would let a worker
// propose durable learning without assessing that it should.
func TestEachModeProducesExactlyItsDescendants(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         review.Mode
		destination  review.Destination
		withRevision bool
		withMemory   bool
		wantErr      error
	}{
		{name: "none produces a revision only", mode: review.ModeNone, withRevision: true},
		{
			name: "none refuses a lasting-context proposal", mode: review.ModeNone,
			withRevision: true, withMemory: true, wantErr: review.ErrModeMismatch,
		},
		{name: "none refuses no descendant at all", mode: review.ModeNone, wantErr: review.ErrModeMismatch},
		{
			name: "alongside produces both", mode: review.ModeAlongside,
			destination: review.DestinationCookbookChange, withRevision: true, withMemory: true,
		},
		{
			name: "alongside refuses a revision alone", mode: review.ModeAlongside,
			destination: review.DestinationCookbookChange, withRevision: true, wantErr: review.ErrModeMismatch,
		},
		{
			name: "instead produces the proposal only", mode: review.ModeInstead,
			destination: review.DestinationRealityPlan, withMemory: true,
		},
		{
			name: "instead refuses a replacement", mode: review.ModeInstead,
			destination: review.DestinationRealityPlan, withRevision: true, withMemory: true,
			wantErr: review.ErrModeMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			prop := h.chain("verify independently")
			subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
			request := h.rejectAndRefine(subject, "cite the command output")

			in := review.RefinementInput{
				Subject:    subject,
				RequestID:  request.ID,
				Agent:      h.agent,
				Mode:       tc.mode,
				Assessment: h.assessment(tc.mode, tc.destination),
			}
			if tc.withRevision {
				revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")
				in.Revision = &frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}
			}
			if tc.withMemory {
				in.Memory = h.memory("cite command output")
			}

			outcome, err := h.svc.RecordRefinement(h.ctx, in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("RecordRefinement error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RecordRefinement: %v", err)
			}
			if (outcome.Revision != nil) != tc.withRevision {
				t.Errorf("revision present = %v, want %v", outcome.Revision != nil, tc.withRevision)
			}
			if (outcome.Memory != nil) != tc.withMemory {
				t.Errorf("memory present = %v, want %v", outcome.Memory != nil, tc.withMemory)
			}
			if outcome.Assessment.Mode != tc.mode {
				t.Errorf("assessment mode = %q, want %q", outcome.Assessment.Mode, tc.mode)
			}

			// Every part carries its own immutable identity (§4.7).
			ids := map[string]string{"outcome": outcome.ID, "assessment": outcome.Assessment.ID}
			if outcome.Memory != nil {
				ids["memory"] = outcome.Memory.ID
			}
			seen := map[string]string{}
			for name, id := range ids {
				if id == "" {
					t.Errorf("%s has no id", name)
				}
				if other, dup := seen[id]; dup {
					t.Errorf("%s and %s share the id %q", name, other, id)
				}
				seen[id] = name
			}
			if outcome.Memory != nil && outcome.Memory.Status != frontier.ReviewNew {
				t.Errorf("a freshly proposed artifact is already %q", outcome.Memory.Status)
			}
		})
	}
}

// TestAssessmentValidationRefusesAnUnjustifiedProposal covers the parts of
// §4.7's assessment that are easy to leave empty.
func TestAssessmentValidationRefusesAnUnjustifiedProposal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*review.AssessmentPayload)
		mode    review.Mode
		wantErr error
	}{
		{
			name:   "no rationale",
			mutate: func(p *review.AssessmentPayload) { p.Rationale = "" },
			mode:   review.ModeAlongside, wantErr: review.ErrInvalidValue,
		},
		{
			name:   "no intended scope",
			mutate: func(p *review.AssessmentPayload) { p.Scope = "" },
			mode:   review.ModeAlongside, wantErr: review.ErrInvalidValue,
		},
		{
			name:   "no sensitivity",
			mutate: func(p *review.AssessmentPayload) { p.Sensitivity = "" },
			mode:   review.ModeAlongside, wantErr: review.ErrInvalidValue,
		},
		{
			name:   "a destination outside the enumerated set",
			mutate: func(p *review.AssessmentPayload) { p.Destination = "remember-this" },
			mode:   review.ModeAlongside, wantErr: review.ErrInvalidValue,
		},
		{
			name:   "lasting context with no supporting evidence",
			mutate: func(p *review.AssessmentPayload) { p.Supporting = nil },
			mode:   review.ModeAlongside, wantErr: review.ErrInvalidValue,
		},
		{
			name:   "an output-specific correction that names a destination anyway",
			mutate: func(p *review.AssessmentPayload) { p.Destination = review.DestinationOperatorNote },
			mode:   review.ModeNone, wantErr: review.ErrInvalidValue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			prop := h.chain("verify independently")
			subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
			request := h.rejectAndRefine(subject, "cite the command output")

			destination := review.Destination("")
			if tc.mode != review.ModeNone {
				destination = review.DestinationCookbookChange
			}
			assessment := h.assessment(tc.mode, destination)
			tc.mutate(&assessment)

			in := review.RefinementInput{
				Subject:    subject,
				RequestID:  request.ID,
				Agent:      h.agent,
				Mode:       tc.mode,
				Assessment: assessment,
			}
			if tc.mode != review.ModeInstead {
				revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")
				in.Revision = &frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}
			}
			if tc.mode != review.ModeNone {
				in.Memory = h.memory("cite command output")
			}
			if _, err := h.svc.RecordRefinement(h.ctx, in); !errors.Is(err, tc.wantErr) {
				t.Fatalf("RecordRefinement error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestRefinementRefusesADescendantThatIsNotOne keeps lineage honest: a
// revision has to revise the record it claims to correct.
func TestRefinementRefusesADescendantThatIsNotOne(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	unrelated := h.chain("an unrelated proposal")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	request := h.rejectAndRefine(subject, "cite the command output")

	_, err := h.svc.RecordRefinement(h.ctx, review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeNone,
		Assessment: h.assessment(review.ModeNone, ""),
		Revision:   &frontier.Ref{Type: frontier.EntityProposal, ID: unrelated.ID},
	})
	if !errors.Is(err, review.ErrModeMismatch) {
		t.Fatalf("RecordRefinement error = %v, want ErrModeMismatch", err)
	}
}

// TestOneRequestIsAnsweredOnce. A second correction is a second rejection with
// its own request, not a second answer to the first.
func TestOneRequestIsAnsweredOnce(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	request := h.rejectAndRefine(subject, "cite the command output")
	revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")

	in := review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeNone,
		Assessment: h.assessment(review.ModeNone, ""),
		Revision:   &frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID},
	}
	if _, err := h.svc.RecordRefinement(h.ctx, in); err != nil {
		t.Fatalf("RecordRefinement: %v", err)
	}
	if _, err := h.svc.RecordRefinement(h.ctx, in); !errors.Is(err, review.ErrAlreadyRecorded) {
		t.Fatalf("second RecordRefinement error = %v, want ErrAlreadyRecorded", err)
	}
}

// TestAcceptingARevisionLeavesItsMemoryProposalUndisposed is the invariant
// §4.7 names explicitly and the one most easily lost: the two decisions are
// separate records reached by separate methods, so neither can move the other.
func TestAcceptingARevisionLeavesItsMemoryProposalUndisposed(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	request := h.rejectAndRefine(subject, "cite the command output")
	revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")
	revisionRef := frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}

	outcome, err := h.svc.RecordRefinement(h.ctx, review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeAlongside,
		Assessment: h.assessment(review.ModeAlongside, review.DestinationCookbookChange),
		Revision:   &revisionRef,
		Memory:     h.memory("cite command output"),
	})
	if err != nil {
		t.Fatalf("RecordRefinement: %v", err)
	}
	memoryID := outcome.Memory.ID

	// Accepting the revision.
	h.decide(revisionRef, frontier.DispositionAccept)

	memory, err := h.svc.Memory(h.ctx, memoryID)
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	if memory.Status != frontier.ReviewNew {
		t.Fatalf("accepting the revision moved the memory proposal to %q", memory.Status)
	}
	memoryHistory, err := h.svc.MemoryHistory(h.ctx, memoryID)
	if err != nil {
		t.Fatalf("MemoryHistory: %v", err)
	}
	if len(memoryHistory) != 0 {
		t.Fatalf("accepting the revision recorded %d disposition(s) against the memory proposal", len(memoryHistory))
	}

	// Accepting the memory proposal.
	event, err := h.svc.DisposeMemory(h.ctx, memoryID, frontier.DispositionAccept, h.op, "", "adopt into the lens")
	if err != nil {
		t.Fatalf("DisposeMemory: %v", err)
	}
	if event.ID == "" || event.Sequence != 1 {
		t.Errorf("memory disposition = %+v, want an id and sequence 1", event)
	}
	if event.ID == outcome.ID || event.ID == outcome.Assessment.ID || event.ID == memoryID {
		t.Errorf("the memory disposition reuses another record's id: %q", event.ID)
	}

	memory, err = h.svc.Memory(h.ctx, memoryID)
	if err != nil {
		t.Fatalf("Memory after acceptance: %v", err)
	}
	if memory.Status != frontier.ReviewAccepted {
		t.Errorf("memory status = %q, want accepted", memory.Status)
	}

	// And the revision's own disposition history is untouched by it.
	revisionHistory, err := h.svc.History(h.ctx, revisionRef)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got, want := len(revisionHistory.Decisions), 1; got != want {
		t.Fatalf("revision decisions = %d, want %d", got, want)
	}
	if revisionHistory.Status != frontier.ReviewAccepted {
		t.Errorf("revision status = %q, want accepted", revisionHistory.Status)
	}
	if revisionHistory.Decisions[0].Event.Disposition != frontier.DispositionAccept {
		t.Errorf("revision decision = %q, want accept", revisionHistory.Decisions[0].Event.Disposition)
	}
}

// TestRejectingAMemoryProposalLeavesTheRevisionAccepted is the same invariant
// from the other side: §4.7's "or vice versa".
func TestRejectingAMemoryProposalLeavesTheRevisionAccepted(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	request := h.rejectAndRefine(subject, "cite the command output")
	revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")
	revisionRef := frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}

	outcome, err := h.svc.RecordRefinement(h.ctx, review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeAlongside,
		Assessment: h.assessment(review.ModeAlongside, review.DestinationSkillDraft),
		Revision:   &revisionRef,
		Memory:     h.memory("cite command output"),
	})
	if err != nil {
		t.Fatalf("RecordRefinement: %v", err)
	}
	h.decide(revisionRef, frontier.DispositionAccept)

	if _, err := h.svc.DisposeMemory(h.ctx, outcome.Memory.ID,
		frontier.DispositionReject, h.op, "", "the generalization is not supported yet"); err != nil {
		t.Fatalf("DisposeMemory: %v", err)
	}

	status, err := h.front.ReviewStatus(h.ctx, revisionRef)
	if err != nil {
		t.Fatalf("ReviewStatus: %v", err)
	}
	if status != frontier.ReviewAccepted {
		t.Fatalf("rejecting the memory proposal moved the revision to %q", status)
	}
	memory, err := h.svc.Memory(h.ctx, outcome.Memory.ID)
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	if memory.Status != frontier.ReviewRejected {
		t.Errorf("memory status = %q, want rejected", memory.Status)
	}
}

// TestLineageAcrossTwoRefinementGenerations walks a proposal, the revision
// that refined it, and the revision that refined that, from both ends.
func TestLineageAcrossTwoRefinementGenerations(t *testing.T) {
	h := newHarness(t)
	first := h.chain("verify independently")
	firstRef := frontier.Ref{Type: frontier.EntityProposal, ID: first.ID}

	second := h.refine(firstRef, first.FindingIDs, "verify by citing command output")
	third := h.refine(second, first.FindingIDs, "verify by citing command output and exit status")

	// Downward: from the original, both later generations are reachable.
	lineage, err := h.svc.Lineage(h.ctx, review.Node{Kind: review.KindProposal, ID: first.ID})
	if err != nil {
		t.Fatalf("Lineage: %v", err)
	}
	if len(lineage.Ancestors) != 0 {
		t.Errorf("the original proposal has %d ancestor edge(s)", len(lineage.Ancestors))
	}
	descendants := map[string]int{}
	for _, edge := range lineage.Descendants {
		if edge.Relation != review.RelationRefines {
			continue
		}
		descendants[edge.From.ID] = edge.Generation
	}
	if got, want := descendants[second.ID], 1; got != want {
		t.Errorf("first-generation descendant %q at generation %d, want %d", second.ID, got, want)
	}
	if got, want := descendants[third.ID], 2; got != want {
		t.Errorf("second-generation descendant %q at generation %d, want %d", third.ID, got, want)
	}

	// Upward: from the newest, both earlier generations are reachable.
	lineage, err = h.svc.Lineage(h.ctx, review.Node{Kind: review.KindProposal, ID: third.ID})
	if err != nil {
		t.Fatalf("Lineage: %v", err)
	}
	ancestors := map[string]int{}
	for _, edge := range lineage.Ancestors {
		if edge.Relation != review.RelationRefines {
			continue
		}
		ancestors[edge.To.ID] = edge.Generation
	}
	if got, want := ancestors[second.ID], 1; got != want {
		t.Errorf("nearest ancestor %q at generation %d, want %d", second.ID, got, want)
	}
	if got, want := ancestors[first.ID], 2; got != want {
		t.Errorf("older ancestor %q at generation %d, want %d", first.ID, got, want)
	}
	if len(lineage.Descendants) != 0 {
		t.Errorf("the newest revision has %d descendant edge(s)", len(lineage.Descendants))
	}

	// The assessment that produced each generation hangs off the same
	// lineage, so a reviewer can reach the reasoning from the record.
	var assessments int
	for _, edge := range lineage.Ancestors {
		if edge.To.Kind == review.KindAssessment {
			assessments++
		}
	}
	if assessments == 0 {
		t.Error("no assessment is reachable from the newest revision")
	}
}

// refine rejects a proposal, records a `none` refinement, and returns the
// revision's reference.
func (h *harness) refine(subject frontier.Ref, findingIDs []string, title string) frontier.Ref {
	h.t.Helper()
	request := h.rejectAndRefine(subject, "cite the command output")
	revision := h.proposal(findingIDs, subject.ID, title)
	ref := frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}
	if _, err := h.svc.RecordRefinement(h.ctx, review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeNone,
		Assessment: h.assessment(review.ModeNone, ""),
		Revision:   &ref,
	}); err != nil {
		h.t.Fatalf("RecordRefinement: %v", err)
	}
	return ref
}

// TestHistoryShowsAnAuthorizedRequestAndItsOutcome. §4.7 lets a refinement run
// independently of its parent, so an authorized request with no outcome is a
// normal state a reviewer has to be able to see, and the outcome arriving
// later must attach to it rather than to a second record.
func TestHistoryShowsAnAuthorizedRequestAndItsOutcome(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	request := h.rejectAndRefine(subject, "cite the command output")

	history, err := h.svc.History(h.ctx, subject)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got, want := len(history.Refinements), 1; got != want {
		t.Fatalf("refinements = %d, want %d", got, want)
	}
	if history.Refinements[0].Outcome != nil {
		t.Fatal("an unanswered request already carries an outcome")
	}
	if history.Refinements[0].Request.DispositionID != history.Decisions[0].Event.ID {
		t.Errorf("the request is not attached to the rejection that authorized it")
	}
	if history.Status != frontier.ReviewRefineRequested {
		t.Errorf("status = %q, want refine-requested", history.Status)
	}

	revision := h.proposal(prop.FindingIDs, prop.ID, "verify by citing command output")
	revisionRef := frontier.Ref{Type: frontier.EntityProposal, ID: revision.ID}
	outcome, err := h.svc.RecordRefinement(h.ctx, review.RefinementInput{
		Subject:    subject,
		RequestID:  request.ID,
		Agent:      h.agent,
		Mode:       review.ModeNone,
		Assessment: h.assessment(review.ModeNone, ""),
		Revision:   &revisionRef,
	})
	if err != nil {
		t.Fatalf("RecordRefinement: %v", err)
	}

	history, err = h.svc.History(h.ctx, subject)
	if err != nil {
		t.Fatalf("History after the outcome: %v", err)
	}
	if history.Refinements[0].Outcome == nil {
		t.Fatal("the recorded outcome is not attached to its request")
	}
	if got := history.Refinements[0].Outcome.ID; got != outcome.ID {
		t.Errorf("outcome id = %q, want %q", got, outcome.ID)
	}
	if got := history.Refinements[0].Outcome.Assessment.Payload.Rationale; got == "" {
		t.Error("the outcome's assessment came back without its rationale")
	}

	// The revised descendant is on the review queue in its own right.
	queue, err := h.svc.Queue(h.ctx, review.QueueFilter{})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	var found bool
	for _, item := range queue {
		if item.Subject == revisionRef {
			found = true
		}
	}
	if !found {
		t.Error("the revised descendant is not awaiting review")
	}
}
