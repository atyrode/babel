package frontier

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRevisionChainRoundTrips covers the three questions issue #87 says a
// record's history has to answer: what the chain is, what the current state
// is, and who changed it and why. It walks a three-generation chain because
// two generations cannot distinguish "the chain" from "the ancestor link",
// which is what already existed before #87.
func TestRevisionChainRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.4),
	})
	if err != nil {
		t.Fatalf("create original: %v", err)
	}
	second, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-2",
		AncestorID: original.ID,
		Reason:     "the second run narrowed it to one repository",
		Payload:    hypothesisPayload("handoffs drop constraints in one repository", 0.5),
	})
	if err != nil {
		t.Fatalf("revise as a run: %v", err)
	}
	third, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-2",
		AncestorID: second.ID,
		Actor:      Operator("alex"),
		Reason:     "the wording implied causation nobody established",
		Payload:    hypothesisPayload("handoffs and dropped constraints coincide", 0.5),
	})
	if err != nil {
		t.Fatalf("revise as an operator: %v", err)
	}

	// The chain reads the same from every member, which is what lets an
	// operator paste whichever identifier a listing printed.
	for _, from := range []string{original.ID, second.ID, third.ID} {
		chain, err := store.Revisions(ctx, Ref{Type: EntityHypothesis, ID: from})
		if err != nil {
			t.Fatalf("read chain from %s: %v", from, err)
		}
		if len(chain) != 3 {
			t.Fatalf("chain from %s has %d revisions, want 3", from, len(chain))
		}
		for i, want := range []string{original.ID, second.ID, third.ID} {
			if chain[i].Entity.ID != want {
				t.Fatalf("chain from %s position %d = %s, want %s", from, i, chain[i].Entity.ID, want)
			}
			if chain[i].Sequence != int64(i+1) {
				t.Errorf("revision %d sequence = %d, want %d", i, chain[i].Sequence, i+1)
			}
			if chain[i].RootID != original.ID {
				t.Errorf("revision %d root = %s, want %s", i, chain[i].RootID, original.ID)
			}
		}
		head, err := store.Head(ctx, Ref{Type: EntityHypothesis, ID: from})
		if err != nil {
			t.Fatalf("read head from %s: %v", from, err)
		}
		if head.ID != third.ID {
			t.Fatalf("head from %s = %s, want %s", from, head.ID, third.ID)
		}
	}

	chain, err := store.Revisions(ctx, Ref{Type: EntityHypothesis, ID: third.ID})
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	if chain[0].SupersedesID != "" {
		t.Errorf("the original supersedes %q, want nothing", chain[0].SupersedesID)
	}
	if chain[2].SupersedesID != second.ID {
		t.Errorf("the head supersedes %q, want %s", chain[2].SupersedesID, second.ID)
	}

	// Attribution is the whole reason the row exists: the run's revision and
	// the operator's must be distinguishable, and neither may be inferred
	// from the record's run_id, which is "run-2" for both.
	if want := (Actor{Kind: ActorRun, ID: "run-1"}); chain[0].Actor != want {
		t.Errorf("original actor = %+v, want %+v", chain[0].Actor, want)
	}
	if want := (Actor{Kind: ActorRun, ID: "run-2"}); chain[1].Actor != want {
		t.Errorf("run revision actor = %+v, want %+v", chain[1].Actor, want)
	}
	if want := (Actor{Kind: ActorOperator, ID: "alex"}); chain[2].Actor != want {
		t.Errorf("operator revision actor = %+v, want %+v", chain[2].Actor, want)
	}
	if chain[1].Payload.Reason != "the second run narrowed it to one repository" {
		t.Errorf("run revision reason = %q", chain[1].Payload.Reason)
	}
	if chain[0].Payload.Reason != "" {
		t.Errorf("the original carries a reason %q; it supersedes nothing", chain[0].Payload.Reason)
	}
}

// TestChainRefusesASecondSuccessor pins the property that makes "current state
// = head" true. Two descendants of one record would be two current wordings,
// and every reader that asked for the current one would have to pick.
func TestChainRefusesASecondSuccessor(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("an idea", 0.2),
	})
	if err != nil {
		t.Fatalf("create original: %v", err)
	}
	first, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-2", AncestorID: original.ID, Reason: "reworded",
		Payload: hypothesisPayload("an idea, reworded", 0.2),
	})
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	_, err = store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-3", AncestorID: original.ID, Reason: "reworded differently",
		Payload: hypothesisPayload("an idea, reworded differently", 0.2),
	})
	if !errors.Is(err, ErrSuperseded) {
		t.Fatalf("second successor: got %v, want ErrSuperseded", err)
	}
	// The refusal names the head so the caller can retry against it.
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("the refusal does not name the successor: %v", err)
	}
	// Nothing was written: the chain is still two long.
	chain, err := store.Revisions(ctx, Ref{Type: EntityHypothesis, ID: original.ID})
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain has %d revisions after a refused fork, want 2", len(chain))
	}
}

// TestEveryRecordKindJoinsAChain checks that the chain is uniform across the
// four record kinds rather than a hypothesis feature. A history that existed
// for candidates and not for findings would make "show me how this changed"
// an answer that depends on which page you are on.
func TestEveryRecordKindJoinsAChain(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, observation, finding, proposal := developPath(t, store)

	for _, ref := range []Ref{
		{Type: EntityHypothesis, ID: hypothesis.ID},
		{Type: EntityObservation, ID: observation.ID},
		{Type: EntityFinding, ID: finding.ID},
		{Type: EntityProposal, ID: proposal.ID},
	} {
		chain, err := store.Revisions(ctx, ref)
		if err != nil {
			t.Fatalf("read %s chain: %v", ref.Type, err)
		}
		if len(chain) != 1 {
			t.Fatalf("%s chain has %d revisions, want 1", ref.Type, len(chain))
		}
		if chain[0].Entity != ref {
			t.Errorf("%s chain names %+v, want %+v", ref.Type, chain[0].Entity, ref)
		}
		if chain[0].Actor.Kind != ActorRun {
			t.Errorf("%s original actor kind = %q, want run", ref.Type, chain[0].Actor.Kind)
		}
	}
}

// TestReviveIsLegalFromEveryRestingStatusAndNoOther enumerates §4.2's whole
// status vocabulary rather than the two that read like endings. #87 says
// nothing closes, so the three statuses a stopped candidate can be in must all
// be revivable — and the three a live one can be in must all refuse, because
// reviving there would either mean nothing or rewrite a running exploration's
// lifecycle from outside it.
func TestReviveIsLegalFromEveryRestingStatusAndNoOther(t *testing.T) {
	ctx := context.Background()

	cases := map[Status]bool{
		StatusUntriaged:     false,
		StatusQueued:        false,
		StatusInvestigating: false,
		StatusDeferred:      true,
		StatusRejected:      true,
		StatusPromoted:      true,
	}
	// Every §4.2 status is covered, so a status added later fails here
	// rather than quietly having no revive rule.
	for _, status := range []Status{
		StatusUntriaged, StatusQueued, StatusInvestigating,
		StatusDeferred, StatusRejected, StatusPromoted,
	} {
		if _, ok := cases[status]; !ok {
			t.Fatalf("status %q has no revive expectation", status)
		}
	}

	for status, revivable := range cases {
		t.Run(string(status), func(t *testing.T) {
			store := openStore(t)
			record, err := store.CreateHypothesis(ctx, HypothesisInput{
				RunID:   "run-1",
				Status:  status,
				Payload: hypothesisPayload("a candidate at rest", 0.3),
			})
			if err != nil {
				t.Fatalf("create candidate: %v", err)
			}
			event, err := store.Revive(ctx, ReviveInput{
				HypothesisID: record.ID,
				Actor:        Operator("alex"),
				Reason:       "a later session showed the pattern again",
			})
			if !revivable {
				if !errors.Is(err, ErrNotResting) {
					t.Fatalf("revive from %s: got %v, want ErrNotResting", status, err)
				}
				history, err := store.StatusHistory(ctx, record.ID)
				if err != nil {
					t.Fatalf("read status history: %v", err)
				}
				if len(history) != 1 {
					t.Fatalf("a refused revive appended %d events", len(history)-1)
				}
				return
			}
			if err != nil {
				t.Fatalf("revive from %s: %v", status, err)
			}
			if event.Status != StatusQueued {
				t.Errorf("revived into %q, want queued", event.Status)
			}
			// The transition is attributable to the person and to no run:
			// #88 reads this back as evidence about who is curating.
			if want := (Actor{Kind: ActorOperator, ID: "alex"}); event.Actor != want {
				t.Errorf("revive actor = %+v, want %+v", event.Actor, want)
			}
			if event.RunID != "" {
				t.Errorf("an operator's revive borrowed run identity %q", event.RunID)
			}
			if event.Payload.Note == "" {
				t.Error("the revive recorded no reason")
			}

			// The history keeps the resting status: reviving does not erase
			// the fact that the candidate stopped there.
			history, err := store.StatusHistory(ctx, record.ID)
			if err != nil {
				t.Fatalf("read status history: %v", err)
			}
			if len(history) != 2 {
				t.Fatalf("status history has %d entries, want 2", len(history))
			}
			if history[0].Status != status {
				t.Errorf("the resting status became %q", history[0].Status)
			}
			reread, err := store.Hypothesis(ctx, record.ID)
			if err != nil {
				t.Fatalf("reread candidate: %v", err)
			}
			if reread.Status != StatusQueued {
				t.Errorf("current status = %q, want queued", reread.Status)
			}
		})
	}
}

// TestReviveRefusesAnUnattributedOrUnarguedTransition covers the two things
// that make a revive readable later. Both are refusals rather than defaults:
// a revive with nobody behind it and a revive with no argument behind it are
// each indistinguishable from a candidate that was never at rest.
func TestReviveRefusesAnUnattributedOrUnarguedTransition(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	record, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Status:  StatusRejected,
		Payload: hypothesisPayload("a rejected candidate", 0.1),
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}

	if _, err := store.Revive(ctx, ReviveInput{
		HypothesisID: record.ID, Reason: "it came back",
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unattributed revive: got %v, want ErrInvalidValue", err)
	}
	if _, err := store.Revive(ctx, ReviveInput{
		HypothesisID: record.ID, Actor: Operator("alex"),
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unargued revive: got %v, want ErrInvalidValue", err)
	}
	if _, err := store.Revive(ctx, ReviveInput{
		HypothesisID: record.ID, Actor: Operator("alex"),
		Status: StatusRejected, Reason: "back to where it was",
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("revive into a resting status: got %v, want ErrInvalidValue", err)
	}
	history, err := store.StatusHistory(ctx, record.ID)
	if err != nil {
		t.Fatalf("read status history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("refused revives appended %d events", len(history)-1)
	}
}

// TestARunMayReviveWithItsOwnIdentity covers #87's other revive author. A
// run's proposal to revive is the same transition with a different authority,
// and it keeps run_id populated so the receipt and the lifecycle agree.
func TestARunMayReviveWithItsOwnIdentity(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	record, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Status:  StatusDeferred,
		Payload: hypothesisPayload("a deferred candidate", 0.1),
	})
	if err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	event, err := store.Revive(ctx, ReviveInput{
		HypothesisID: record.ID,
		Actor:        Run("run-9"),
		Reason:       "retrieval surfaced three more instances",
	})
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if event.RunID != "run-9" {
		t.Errorf("run revive recorded run %q, want run-9", event.RunID)
	}
	if event.Actor.Kind != ActorRun {
		t.Errorf("run revive actor kind = %q, want run", event.Actor.Kind)
	}
}
