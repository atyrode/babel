package frontier

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// The authority line, checked at the level that survives a future caller.
//
// A triage pass may rank, cluster, argue against and re-propose. It may not
// accept, reject, defer or mark a duplicate, and the operator's own note on
// this design is that autonomous disposition is explicitly unresolved rather
// than merely unimplemented — so the property these tests defend is not "the
// pass happens not to call Decide" but that a holder of the triage handle has
// no way to reach it.

// TestTriageSurfaceHoldsNoDisposition is the §4.7 property for the triage
// pass: the whole authority a pass holds over the frontier is these three
// operations, and none of them is a ruling.
//
// It is written against the method set rather than against a call, for the
// reason TestFleetReaderSurfaceHoldsNoWriter is: a test that searched this
// package for the word Decide would say nothing about the next method somebody
// adds to *Triage. The *Store assertions are what keep the test honest — each
// names a ruling operation that demonstrably exists, so a rename that removed
// one fails here loudly instead of leaving an assertion that checks nothing.
func TestTriageSurfaceHoldsNoDisposition(t *testing.T) {
	surface := reflect.TypeOf((*Triage)(nil))
	permitted := map[string]bool{
		"Pending": true, "Advise": true, "AdviseWithAlternative": true,
	}
	for i := range surface.NumMethod() {
		if name := surface.Method(i).Name; !permitted[name] {
			t.Errorf("*Triage exposes %s, which is outside the advice contract", name)
		}
	}
	if surface.NumMethod() != len(permitted) {
		t.Errorf("*Triage has %d methods, want %d", surface.NumMethod(), len(permitted))
	}
	// The four operations that settle or move a record. Decide and
	// RejectAndRefine are the review vocabulary; SetStatus and DeferFrontier
	// are the lifecycle's, and a pass that could sort a record off the
	// frontier would be disposing of it under another name.
	store := reflect.TypeOf((*Store)(nil))
	for _, forbidden := range []string{"Decide", "RejectAndRefine", "SetStatus", "DeferFrontier"} {
		if _, found := store.MethodByName(forbidden); !found {
			t.Fatalf("*Store no longer has %s, so this assertion checks nothing", forbidden)
		}
		if _, found := surface.MethodByName(forbidden); found {
			t.Errorf("*Triage exposes %s", forbidden)
		}
	}
	// Embedding is the other way a ruling could arrive, and it would arrive
	// silently: an embedded *Store promotes every one of its methods onto
	// this type, and the loop above would then be listing them. One
	// unexported field, and it is not the store's methods.
	concrete := reflect.TypeOf(Triage{})
	if concrete.NumField() != 1 {
		t.Errorf("Triage has %d fields, want 1", concrete.NumField())
	}
	for i := range concrete.NumField() {
		if field := concrete.Field(i); field.Anonymous {
			t.Errorf("Triage embeds %s, which promotes its methods onto the triage surface", field.Type)
		}
	}
}

// TestTriageAdviceLeavesTheRulingUnmade is the same property from the record's
// side: advising a proposal writes no disposition, and the proposal is still
// waiting when the advice is there.
//
// The two halves are both needed. The method set says a pass cannot call for a
// ruling; this says the act of advising does not produce one as a side effect,
// which is the failure a derived review status would hide.
func TestTriageAdviceLeavesTheRulingUnmade(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)

	advice, err := store.Triage().Advise(ctx, TriageInput{
		ProposalID: proposal.ID,
		RunID:      "run-triage",
		Payload: TriageAdvicePayload{
			Rank: 1, Cohort: 1,
			CounterArgument: "the same constraint is already stated in the preamble",
		},
	})
	if err != nil {
		t.Fatalf("advise: %v", err)
	}

	loaded, err := store.Proposal(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("read the advised proposal: %v", err)
	}
	if loaded.ReviewStatus != ReviewNew {
		t.Errorf("advised proposal review status = %q, want %q", loaded.ReviewStatus, ReviewNew)
	}
	history, err := store.DispositionHistory(ctx, Ref{Type: EntityProposal, ID: proposal.ID})
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("advising recorded %d dispositions, want none", len(history))
	}
	if advice.Payload.CounterArgument == "" {
		t.Error("advice came back without its counter-argument")
	}
}

// TestTriageRefusesAProposalAlreadyRuledOn is the authority line's other
// direction. A pass may reach a reviewer before they decide; it may not append
// its opinion beside a decision they have made, because a record page showing
// advice next to an accept would present a settled question as open.
func TestTriageRefusesAProposalAlreadyRuledOn(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)
	if _, err := store.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityProposal, ID: proposal.ID},
		Disposition: DispositionAccept,
		ReviewerID:  "operator",
	}); err != nil {
		t.Fatalf("accept the proposal: %v", err)
	}

	if _, err := store.Triage().Advise(ctx, TriageInput{
		ProposalID: proposal.ID,
		RunID:      "run-triage",
		Payload: TriageAdvicePayload{
			Rank: 1, Cohort: 1, CounterArgument: "too late to matter",
		},
	}); !errors.Is(err, ErrAlreadyRuled) {
		t.Errorf("advise a ruled proposal: got %v, want ErrAlreadyRuled", err)
	}
}

// TestAnAlternativeIsANewRecordBesideTheOriginal is the promise that makes a
// triage pass safe to point at the operator's queue: a better proposal is a
// proposal, not an edit.
//
// Every assertion here is one way the promise could be broken. The original
// keeps its id, its wording and its place in the queue; the alternative has an
// id of its own and no ancestor, so it is not a revision that superseded
// anything; it rests on exactly what the original rests on, so it is a peer
// rather than a consolidation the pass invented; and the advice names both, so
// the operator can see where the second record came from.
func TestAnAlternativeIsANewRecordBesideTheOriginal(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, finding, proposal := developPath(t, store)

	advice, alternative, err := store.Triage().AdviseWithAlternative(ctx,
		TriageInput{
			ProposalID: proposal.ID,
			RunID:      "run-triage",
			Payload: TriageAdvicePayload{
				Rank: 2, Cohort: 2,
				Ranking:         "the narrower wording is cheaper to act on",
				CounterArgument: "acting on this would touch every handoff at once",
			},
		},
		proposalPayload("state the one constraint the handoff drops"),
	)
	if err != nil {
		t.Fatalf("advise with an alternative: %v", err)
	}

	if alternative.ID == proposal.ID {
		t.Fatal("the alternative reused the original's id, so the original was rewritten")
	}
	if alternative.AncestorID != "" {
		t.Errorf("alternative ancestor = %q, want none: an alternative supersedes nothing", alternative.AncestorID)
	}
	if got := alternative.FindingIDs; !reflect.DeepEqual(got, []string{finding.ID}) {
		t.Errorf("alternative rests on %v, want the original's findings [%s]", got, finding.ID)
	}
	if alternative.Form != proposal.Form {
		t.Errorf("alternative form = %q, want the original's %q", alternative.Form, proposal.Form)
	}
	if advice.AlternativeID != alternative.ID {
		t.Errorf("advice names alternative %q, want %q", advice.AlternativeID, alternative.ID)
	}

	// The original is unchanged and still present. Read it back rather than
	// trusting the value the write returned: a rewrite would be invisible in
	// the returned copy and visible only here.
	original, err := store.Proposal(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("read the original back: %v", err)
	}
	if original.Payload.Title != proposal.Payload.Title {
		t.Errorf("original title = %q, want %q", original.Payload.Title, proposal.Payload.Title)
	}
	if !original.CreatedAt.Equal(proposal.CreatedAt) {
		t.Errorf("original created at %v, want %v", original.CreatedAt, proposal.CreatedAt)
	}
	if original.ReviewStatus != ReviewNew {
		t.Errorf("original review status = %q, want %q", original.ReviewStatus, ReviewNew)
	}
	// Both are in the queue: the operator chooses between two records, and a
	// pass that replaced one with the other would leave one here.
	pending, err := store.Triage().Pending(ctx, 0)
	if err != nil {
		t.Fatalf("read the pending pile: %v", err)
	}
	waiting := make(map[string]bool, len(pending))
	for _, record := range pending {
		waiting[record.ID] = true
	}
	if !waiting[proposal.ID] || !waiting[alternative.ID] {
		t.Errorf("pending holds %v, want both %s and %s", waiting, proposal.ID, alternative.ID)
	}

	// The advice is readable from either end, because one row is a statement
	// about the pair. From the alternative it is the only account of where
	// that record came from.
	for _, from := range []string{proposal.ID, alternative.ID} {
		found, err := store.TriageAdvice(ctx, from)
		if err != nil {
			t.Fatalf("read advice from %s: %v", from, err)
		}
		if len(found) != 1 || found[0].ID != advice.ID {
			t.Fatalf("advice read from %s = %v, want the one advice %s", from, found, advice.ID)
		}
	}
}

// TestClusteringRanksAndArguesWithoutDeciding walks what the operator actually
// receives from a pass over two near-duplicate proposals: which records to
// compare, in which order to read them, and the case against the one it put
// first.
//
// The clustering assertion is the load-bearing one. A pass that clustered a
// proposal with itself, or with a record nobody wrote, would send a reviewer
// comparing against nothing, so both are refused at the write.
func TestClusteringRanksAndArguesWithoutDeciding(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, finding, first := developPath(t, store)
	second, err := store.CreateProposal(ctx, ProposalInput{
		RunID:      "run-2",
		FindingIDs: []string{finding.ID},
		Payload:    proposalPayload("say the constraint before the handoff"),
	})
	if err != nil {
		t.Fatalf("create the near-duplicate: %v", err)
	}

	triage := store.Triage()
	advice, err := triage.Advise(ctx, TriageInput{
		ProposalID: first.ID,
		RunID:      "run-triage",
		// Named twice on purpose: a pass that found the same peer by two
		// routes has found one peer.
		Cluster: []string{second.ID, second.ID},
		Payload: TriageAdvicePayload{
			Rank: 1, Cohort: 2,
			Ranking:         "it states the outcome the other leaves implicit",
			CounterArgument: "both rest on one finding, so neither is more supported than the other",
		},
	})
	if err != nil {
		t.Fatalf("advise the first: %v", err)
	}
	if got := advice.Cluster; !reflect.DeepEqual(got, []string{second.ID}) {
		t.Errorf("cluster = %v, want [%s] once", got, second.ID)
	}
	if advice.Payload.Rank != 1 || advice.Payload.Cohort != 2 {
		t.Errorf("rank = %d of %d, want 1 of 2", advice.Payload.Rank, advice.Payload.Cohort)
	}

	if _, err := triage.Advise(ctx, TriageInput{
		ProposalID: second.ID,
		RunID:      "run-triage",
		Cluster:    []string{second.ID},
		Payload: TriageAdvicePayload{
			Rank: 2, Cohort: 2, CounterArgument: "restates the first",
		},
	}); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("cluster with itself: got %v, want ErrInvalidValue", err)
	}
	if _, err := triage.Advise(ctx, TriageInput{
		ProposalID: second.ID,
		RunID:      "run-triage",
		Cluster:    []string{"pro_absent"},
		Payload: TriageAdvicePayload{
			Rank: 2, Cohort: 2, CounterArgument: "restates the first",
		},
	}); !errors.Is(err, ErrUnknownEntity) {
		t.Errorf("cluster with an absent record: got %v, want ErrUnknownEntity", err)
	}

	// A rank outside its own cohort is not a place in an ordering, and a
	// counter-argument nobody wrote is the field this record exists to carry.
	for _, refused := range []TriageAdvicePayload{
		{Rank: 3, Cohort: 2, CounterArgument: "out of range"},
		{Rank: 0, Cohort: 2, CounterArgument: "no place at all"},
		{Rank: 1, Cohort: 2},
	} {
		if _, err := triage.Advise(ctx, TriageInput{
			ProposalID: second.ID, RunID: "run-triage", Payload: refused,
		}); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("advice %+v: got %v, want ErrInvalidValue", refused, err)
		}
	}
}

// TestSeveralPassesEachLeaveTheirAdvice is the append-only property for this
// record. Advice is what one pass thought at one moment, so a second pass adds
// its own rather than correcting the first, and both stay readable beside the
// proposal — which is what lets an operator see that Babel has changed its mind
// and what it changed it from.
func TestSeveralPassesEachLeaveTheirAdvice(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)
	triage := store.Triage()
	for _, pass := range []string{"run-triage-1", "run-triage-2"} {
		if _, err := triage.Advise(ctx, TriageInput{
			ProposalID: proposal.ID,
			RunID:      pass,
			Payload: TriageAdvicePayload{
				Rank: 1, Cohort: 1, CounterArgument: "the evidence is one session",
			},
		}); err != nil {
			t.Fatalf("advise in %s: %v", pass, err)
		}
	}
	found, err := store.TriageAdvice(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("read advice: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("proposal carries %d pieces of advice, want 2", len(found))
	}
	if found[0].RunID == found[1].RunID {
		t.Errorf("both pieces of advice are attributed to %s", found[0].RunID)
	}
}
