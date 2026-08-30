package review_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
)

// TestOnlyReviewableRecordsAcceptADisposition covers §6.7's list of what may be
// decided. An observation is evidence a finding consolidates, and accepting or
// rejecting one would be deciding a citation.
func TestOnlyReviewableRecordsAcceptADisposition(t *testing.T) {
	h := newHarness(t)
	hyp := h.hypothesis("verification may be reported rather than performed")
	obs := h.observation(hyp.ID, "the agent claimed the tests passed")
	fnd := h.finding([]string{obs.ID}, "claimed verification")
	prop := h.proposal([]string{fnd.ID}, "", "verify independently")

	for _, tc := range []struct {
		name    string
		subject frontier.Ref
		wantErr error
	}{
		{"hypothesis", frontier.Ref{Type: frontier.EntityHypothesis, ID: hyp.ID}, nil},
		{"finding", frontier.Ref{Type: frontier.EntityFinding, ID: fnd.ID}, nil},
		{"proposal", frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}, nil},
		{"observation", frontier.Ref{Type: frontier.EntityObservation, ID: obs.ID}, review.ErrNotReviewable},
		{"unknown proposal", frontier.Ref{Type: frontier.EntityProposal, ID: "prp_missing"}, review.ErrUnknownRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.Decide(h.ctx, review.Decision{
				Subject:     tc.subject,
				Disposition: frontier.DispositionAccept,
				By:          h.op,
			})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Decide: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Decide error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestInvalidTransitionsAreRefusedWithASpecificError is the state machine.
// Every refusal has to name its own rule: "no" is not something a reviewer can
// act on, and a closed state and a repeated decision call for different
// responses.
func TestInvalidTransitionsAreRefusedWithASpecificError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(h *harness, subject frontier.Ref)
		next    frontier.Disposition
		wantErr error
	}{
		{
			name:    "repeating an acceptance",
			setup:   func(h *harness, s frontier.Ref) { h.decide(s, frontier.DispositionAccept) },
			next:    frontier.DispositionAccept,
			wantErr: review.ErrNoChange,
		},
		{
			name:    "repeating a deferral",
			setup:   func(h *harness, s frontier.Ref) { h.decide(s, frontier.DispositionDefer) },
			next:    frontier.DispositionDefer,
			wantErr: review.ErrNoChange,
		},
		{
			name: "deciding a record already marked duplicate",
			setup: func(h *harness, s frontier.Ref) {
				original := h.chain("the original proposal")
				if _, err := h.svc.Decide(h.ctx, review.Decision{
					Subject:       s,
					Disposition:   frontier.DispositionDuplicate,
					By:            h.op,
					DuplicateOfID: original.ID,
				}); err != nil {
					h.t.Fatalf("Decide duplicate: %v", err)
				}
			},
			next:    frontier.DispositionAccept,
			wantErr: review.ErrTerminalStatus,
		},
		{
			name: "deciding a record whose rejection authorized a refinement",
			setup: func(h *harness, s frontier.Ref) {
				h.rejectAndRefine(s, "cite the command output rather than the claim")
			},
			next:    frontier.DispositionAccept,
			wantErr: review.ErrTerminalStatus,
		},
		{
			name:    "an unknown disposition",
			setup:   func(h *harness, s frontier.Ref) {},
			next:    frontier.Disposition("refine"),
			wantErr: review.ErrInvalidValue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			prop := h.chain("verify independently")
			subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
			tc.setup(h, subject)

			_, err := h.svc.Decide(h.ctx, review.Decision{
				Subject:     subject,
				Disposition: tc.next,
				By:          h.op,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Decide error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestDispositionsAreAppendOnlyAndARejectedRecordStaysReadable is §4.7's
// "rejection never deletes a record". The rejected proposal is still readable
// with every field intact, and the history holds both decisions in order.
func TestDispositionsAreAppendOnlyAndARejectedRecordStaysReadable(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}

	guidance, err := h.svc.RecordContext(h.ctx, h.op, "the second run showed the opposite")
	if err != nil {
		t.Fatalf("RecordContext: %v", err)
	}
	if _, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     subject,
		Disposition: frontier.DispositionReject,
		By:          h.op,
		ContextID:   guidance.ID,
		Note:        "the evidence does not support the outcome",
	}); err != nil {
		t.Fatalf("Decide reject: %v", err)
	}
	if _, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     subject,
		Disposition: frontier.DispositionAccept,
		By:          h.op,
		Note:        "reconsidered after the third run",
	}); err != nil {
		t.Fatalf("Decide accept: %v", err)
	}

	history, err := h.svc.History(h.ctx, subject)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got, want := len(history.Decisions), 2; got != want {
		t.Fatalf("decisions = %d, want %d", got, want)
	}
	if got := history.Decisions[0].Event.Disposition; got != frontier.DispositionReject {
		t.Errorf("first decision = %q, want reject", got)
	}
	if got := history.Decisions[1].Event.Disposition; got != frontier.DispositionAccept {
		t.Errorf("second decision = %q, want accept", got)
	}
	if history.Decisions[0].Context == nil || history.Decisions[0].Context.Text != "the second run showed the opposite" {
		t.Errorf("attributed context not resolved onto the rejection: %+v", history.Decisions[0].Context)
	}
	if history.Decisions[1].Context != nil {
		t.Errorf("a decision that cited no context carries one: %+v", history.Decisions[1].Context)
	}
	if history.Status != frontier.ReviewAccepted {
		t.Errorf("status = %q, want accepted", history.Status)
	}

	// The rejected record itself is unchanged and still readable.
	reread, err := h.front.Proposal(h.ctx, prop.ID)
	if err != nil {
		t.Fatalf("Proposal after rejection: %v", err)
	}
	if reread.Payload.Title != prop.Payload.Title || reread.CreatedAt != prop.CreatedAt {
		t.Errorf("the rejected record changed: %+v", reread.Payload)
	}
}

// TestAttributedContextIsGuidanceNotEvidence is §4.7's line, checked two ways:
// the type cannot be used where evidence is required, and the constructor path
// that could have accepted it as a substitute refuses by name.
func TestAttributedContextIsGuidanceNotEvidence(t *testing.T) {
	t.Run("the type does not satisfy the evidence requirement", func(t *testing.T) {
		var guidance any = review.Context{
			ID:     "ctx_1",
			Author: "operator-1",
			Text:   "I think this is the real cause",
		}
		if _, ok := guidance.(review.Evidence); ok {
			t.Fatal("review.Context satisfies review.Evidence: guidance can be passed where evidence is required")
		}
		if _, ok := any(&review.Context{}).(review.Evidence); ok {
			t.Fatal("*review.Context satisfies review.Evidence")
		}
		// The evidence field an assessment carries is a concrete type a
		// Context cannot be converted to, so there is no assignment
		// that would get past the interface check either.
		field, ok := reflect.TypeOf(review.AssessmentPayload{}).FieldByName("Supporting")
		if !ok {
			t.Fatal("AssessmentPayload has no Supporting field")
		}
		if reflect.TypeOf(review.Context{}).AssignableTo(field.Type.Elem()) {
			t.Fatalf("review.Context is assignable to the supporting-evidence element type %s", field.Type.Elem())
		}
	})

	t.Run("a constructor refuses context in place of evidence", func(t *testing.T) {
		h := newHarness(t)
		prop := h.chain("verify independently")
		subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
		request := h.rejectAndRefine(subject, "generalize only if the archive supports it")

		guidance, err := h.svc.RecordContext(h.ctx, h.op, "I have seen this before in other projects")
		if err != nil {
			t.Fatalf("RecordContext: %v", err)
		}
		assessment := h.assessment(review.ModeInstead, review.DestinationOperatorNote)
		assessment.Supporting = nil
		assessment.ContextIDs = []string{guidance.ID}

		_, err = h.svc.RecordRefinement(h.ctx, review.RefinementInput{
			Subject:    subject,
			RequestID:  request.ID,
			Agent:      h.agent,
			Mode:       review.ModeInstead,
			Assessment: assessment,
			Memory:     h.memory("verification must cite command output"),
		})
		if !errors.Is(err, review.ErrContextIsNotEvidence) {
			t.Fatalf("RecordRefinement error = %v, want ErrContextIsNotEvidence", err)
		}
	})
}

// TestQueueListsWhatAwaitsADecision covers the reviewer's first question.
func TestQueueListsWhatAwaitsADecision(t *testing.T) {
	h := newHarness(t)
	first := h.chain("verify independently")
	second := h.chain("record the command output")
	for _, prop := range []string{first.ID, second.ID} {
		if _, err := h.svc.Enroll(h.ctx, frontier.Ref{Type: frontier.EntityProposal, ID: prop}); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
	}

	queue, err := h.svc.Queue(h.ctx, review.QueueFilter{})
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if got, want := len(queue), 2; got != want {
		t.Fatalf("queue = %d items, want %d", got, want)
	}

	h.decide(frontier.Ref{Type: frontier.EntityProposal, ID: first.ID}, frontier.DispositionAccept)
	queue, err = h.svc.Queue(h.ctx, review.QueueFilter{})
	if err != nil {
		t.Fatalf("Queue after a decision: %v", err)
	}
	if got, want := len(queue), 1; got != want {
		t.Fatalf("pending queue = %d items, want %d", got, want)
	}
	if queue[0].Subject.ID != second.ID {
		t.Errorf("pending queue holds %q, want %q", queue[0].Subject.ID, second.ID)
	}

	// A decided record is still enrolled: nothing is removed, it is
	// filtered.
	all, err := h.svc.Queue(h.ctx, review.QueueFilter{AllStatuses: true})
	if err != nil {
		t.Fatalf("Queue all: %v", err)
	}
	if got, want := len(all), 2; got != want {
		t.Fatalf("full queue = %d items, want %d", got, want)
	}
	for _, item := range all {
		if item.Subject.ID != first.ID {
			continue
		}
		if item.Status != frontier.ReviewAccepted || item.Decisions != 1 {
			t.Errorf("decided item = %+v, want accepted with one decision", item)
		}
	}
}

// TestDecideRefusesGuidanceThatDoesNotExist keeps a decision from citing an
// identifier nothing backs. An unattributable context is the same failure as
// an unattributed decision, one indirection later.
func TestDecideRefusesGuidanceThatDoesNotExist(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	_, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID},
		Disposition: frontier.DispositionAccept,
		By:          h.op,
		ContextID:   "ctx_missing",
	})
	if !errors.Is(err, review.ErrUnknownRecord) {
		t.Fatalf("Decide error = %v, want ErrUnknownRecord", err)
	}
}

// TestAnonymousDecisionsAreRefused covers §4.7's attribution rule at the type's
// own boundary.
func TestAnonymousDecisionsAreRefused(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	if _, err := review.NewAuthority(""); !errors.Is(err, review.ErrInvalidValue) {
		t.Fatalf("NewAuthority(\"\") error = %v, want ErrInvalidValue", err)
	}
	_, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID},
		Disposition: frontier.DispositionAccept,
	})
	if !errors.Is(err, review.ErrInvalidValue) {
		t.Fatalf("Decide error = %v, want ErrInvalidValue", err)
	}
}

// decide records one disposition and fails the test if it is refused.
func (h *harness) decide(subject frontier.Ref, d frontier.Disposition) {
	h.t.Helper()
	if _, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     subject,
		Disposition: d,
		By:          h.op,
	}); err != nil {
		h.t.Fatalf("Decide %s: %v", d, err)
	}
}
