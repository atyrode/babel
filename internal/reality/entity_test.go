package reality

import (
	"context"
	"testing"
)

// TestMergeIsReversibleAndBothIdentitiesStayAddressable is §4.8's central
// promise about entity identity: a mistaken resolution remains reversible.
//
// It checks all four halves of that. The merge takes effect — the folded
// identity resolves to the target and its facts count for the target. Both rows
// stay addressable throughout, because §4.8 forbids losing an identity. The
// reversal restores both identities without rewriting anything, so each
// resolves to itself again and the alias that pointed at the folded identity
// points there again. And the whole history survives: the merge, its reversal,
// and the reason for each are all still readable, and the reversal cannot be
// applied twice.
func TestMergeIsReversibleAndBothIdentitiesStayAddressable(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	renamed := mustEntity(t, store, EntityRepository, "repository under its old name")
	current := mustEntity(t, store, EntityRepository, "repository under its current name")

	oldAlias, err := store.AddAlias(ctx, AliasInput{
		EntityID: renamed.ID,
		Kind:     AliasRepository,
		Payload:  AliasPayload{Value: "synthetic/old-name"},
	})
	if err != nil {
		t.Fatalf("AddAlias: %v", err)
	}

	// A fact asserted about the identity as it was understood then. After the
	// merge it must still speak for the target, and after the reversal it
	// must stop doing so.
	observed := clock.now()
	fact, _, err := store.AssertFact(ctx, operatorFact(renamed.ID, PredicateOwnership,
		enum(OwnershipOwned), observed))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	merge, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{renamed.ID},
		TargetID:  current.ID,
		Actor:     "operator",
		Reason:    "the repository was renamed; both names are one repository",
	})
	if err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	if got, err := store.Resolve(ctx, renamed.ID); err != nil || got != current.ID {
		t.Fatalf("Resolve(folded) = %q, %v; want %q", got, err, current.ID)
	}
	folded, err := store.Entity(ctx, renamed.ID)
	if err != nil {
		t.Fatalf("Entity(folded) after merge: %v", err)
	}
	if folded.Role != RoleMerged || folded.CanonicalID != current.ID {
		t.Errorf("folded identity is %s/%q, want %s/%q", folded.Role, folded.CanonicalID,
			RoleMerged, current.ID)
	}
	// The folded identity's facts speak for the target, or the merge lost
	// reality at the moment two names were recognized as one.
	targetFacts, err := store.Facts(ctx, FactQuery{SubjectID: current.ID})
	if err != nil {
		t.Fatalf("Facts after merge: %v", err)
	}
	if len(targetFacts) != 1 || targetFacts[0].ID != fact.ID {
		t.Errorf("target holds %d facts, want the folded identity's one", len(targetFacts))
	}
	// The alias now resolves to the target, since resolution follows the
	// merge.
	if got, err := store.ResolveAlias(ctx, AliasRepository, "synthetic/old-name"); err != nil ||
		got != current.ID {
		t.Errorf("ResolveAlias after merge = %q, %v; want %q", got, err, current.ID)
	}

	undo, err := store.UndoResolution(ctx, UndoInput{
		ResolutionID: merge.ID,
		Actor:        "operator",
		Reason:       "they are two different repositories after all",
	})
	if err != nil {
		t.Fatalf("UndoResolution: %v", err)
	}
	if undo.Kind != ResolutionUndo || undo.ReversesID != merge.ID {
		t.Errorf("reversal is %s reversing %q, want %s reversing %q",
			undo.Kind, undo.ReversesID, ResolutionUndo, merge.ID)
	}

	// Both identities are addressable and each speaks for itself again.
	for _, id := range []string{renamed.ID, current.ID} {
		record, err := store.Entity(ctx, id)
		if err != nil {
			t.Fatalf("Entity(%q) after reversal: %v", id, err)
		}
		if record.Role != RoleSelf || record.CanonicalID != id {
			t.Errorf("entity %q is %s/%q after reversal, want %s/%q",
				id, record.Role, record.CanonicalID, RoleSelf, id)
		}
		resolved, err := store.Resolve(ctx, id)
		if err != nil {
			t.Fatalf("Resolve(%q) after reversal: %v", id, err)
		}
		if resolved != id {
			t.Errorf("Resolve(%q) = %q after reversal", id, resolved)
		}
	}
	// The fact goes back to speaking only for the identity it was asserted
	// about.
	restored, err := store.Facts(ctx, FactQuery{SubjectID: current.ID})
	if err != nil {
		t.Fatalf("Facts after reversal: %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("target still holds %d facts after the merge was reversed", len(restored))
	}
	if got, err := store.ResolveAlias(ctx, AliasRepository, "synthetic/old-name"); err != nil ||
		got != renamed.ID {
		t.Errorf("ResolveAlias after reversal = %q, %v; want %q", got, err, renamed.ID)
	}
	if _, err := store.Entity(ctx, oldAlias.EntityID); err != nil {
		t.Errorf("the alias's entity is no longer addressable: %v", err)
	}

	// The history is append-only: the mistake and its correction are both
	// there, in order.
	history, err := store.ResolutionHistory(ctx, renamed.ID)
	if err != nil {
		t.Fatalf("ResolutionHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the merge and its reversal", len(history))
	}
	if history[0].Kind != ResolutionMerge || history[1].Kind != ResolutionUndo {
		t.Errorf("history is %s then %s", history[0].Kind, history[1].Kind)
	}
	if history[0].Payload.Reason == "" || history[1].Payload.Reason == "" {
		t.Error("a resolution lost its stated reason")
	}

	// A resolution can only be reversed once, and the database is what
	// enforces it.
	if _, err := store.UndoResolution(ctx, UndoInput{
		ResolutionID: merge.ID,
		Actor:        "operator",
		Reason:       "again",
	}); err == nil {
		t.Error("a resolution was reversed twice")
	} else if !isErr(err, ErrNotReversible) {
		t.Errorf("want ErrNotReversible, got %v", err)
	}
}

// TestSplitIsReversibleAndPartsSurvive checks the other half of §4.8's
// reversibility: a split's parts are never deleted, and reversing it folds them
// into the parent rather than removing them.
func TestSplitIsReversibleAndPartsSurvive(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	parent := mustEntity(t, store, EntityService, "one service, or so it seemed")

	split, parts, err := store.SplitEntity(ctx, SplitInput{
		ParentID: parent.ID,
		Parts: []EntityInput{
			{Kind: EntityService, Payload: EntityPayload{DisplayName: "first service"}},
			{Kind: EntityService, Payload: EntityPayload{DisplayName: "second service"}},
		},
		Actor:  "operator",
		Reason: "the name covered two services",
	})
	if err != nil {
		t.Fatalf("SplitEntity: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("split produced %d parts", len(parts))
	}
	marked, err := store.Entity(ctx, parent.ID)
	if err != nil {
		t.Fatalf("Entity(parent): %v", err)
	}
	if marked.Role != RoleSplit {
		t.Errorf("parent is %s after the split, want %s", marked.Role, RoleSplit)
	}

	if _, err := store.UndoResolution(ctx, UndoInput{
		ResolutionID: split.ID,
		Actor:        "operator",
		Reason:       "it really is one service",
	}); err != nil {
		t.Fatalf("UndoResolution: %v", err)
	}

	restored, err := store.Entity(ctx, parent.ID)
	if err != nil {
		t.Fatalf("Entity(parent) after reversal: %v", err)
	}
	if restored.Role != RoleSelf {
		t.Errorf("parent is %s after reversal, want %s", restored.Role, RoleSelf)
	}
	for _, part := range parts {
		record, err := store.Entity(ctx, part.ID)
		if err != nil {
			t.Fatalf("a split part is no longer addressable: %v", err)
		}
		if record.Role != RoleMerged || record.CanonicalID != parent.ID {
			t.Errorf("part %q is %s/%q after reversal, want %s/%q",
				part.ID, record.Role, record.CanonicalID, RoleMerged, parent.ID)
		}
		resolved, err := store.Resolve(ctx, part.ID)
		if err != nil {
			t.Fatalf("Resolve(part): %v", err)
		}
		if resolved != parent.ID {
			t.Errorf("part %q resolves to %q, want the parent", part.ID, resolved)
		}
	}
}

// TestMergeRefusesTheResolutionsItCannotReverseCleanly documents the deliberate
// limits: merging across entity kinds is a detectable resolution mistake, and
// merging an already-merged identity would build a chain whose reversal order
// matters, which is not the reversibility §4.8 promises.
func TestMergeRefusesTheResolutionsItCannotReverseCleanly(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	repository := mustEntity(t, store, EntityRepository, "a repository")
	machine := mustEntity(t, store, EntityMachine, "a machine")
	other := mustEntity(t, store, EntityRepository, "another repository")
	third := mustEntity(t, store, EntityRepository, "a third repository")

	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{repository.ID},
		TargetID:  machine.ID,
		Actor:     "operator",
		Reason:    "a mistake",
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("a cross-kind merge returned %v, want ErrInvalidValue", err)
	}

	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{repository.ID},
		TargetID:  other.ID,
		Actor:     "operator",
		Reason:    "one repository under two names",
	}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}
	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{repository.ID},
		TargetID:  third.ID,
		Actor:     "operator",
		Reason:    "chaining",
	}); !isErr(err, ErrConflict) {
		t.Errorf("re-merging a folded identity returned %v, want ErrConflict", err)
	}
	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{third.ID},
		TargetID:  repository.ID,
		Actor:     "operator",
		Reason:    "merging into a folded identity",
	}); !isErr(err, ErrConflict) {
		t.Errorf("merging into a folded identity returned %v, want ErrConflict", err)
	}
}

// TestOperatorContextIsGuidanceNotEvidence covers the shared §4.7 contract: a
// Context is attributed operator guidance, and it can never satisfy an
// evidence requirement. A non-operator authority needs a provenance locator
// that recovers bytes, and supplying guidance instead does not substitute.
func TestOperatorContextIsGuidanceNotEvidence(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	subject := mustEntity(t, store, EntityProject, "a project")

	guidance, err := store.AttachContext(ctx, ContextInput{
		Author: "operator",
		At:     clock.now(),
		Text:   "treat the dormant projects as still interesting for pattern learning",
	})
	if err != nil {
		t.Fatalf("AttachContext: %v", err)
	}
	if guidance.Author != "operator" || guidance.Text == "" || guidance.At.IsZero() {
		t.Errorf("context lost its attribution: %+v", guidance)
	}
	round, err := store.Context(ctx, guidance.ID)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if round != guidance {
		t.Errorf("context round-trip changed it: %+v vs %+v", round, guidance)
	}

	// An observation-derived revision with guidance attached but no locator
	// is still refused: guidance is not evidence.
	observed := clock.now()
	input := operatorFact(subject.ID, PredicateLifecycle, enum(LifecycleDormant), observed)
	input.Authority = Authority{Kind: AuthorityObservation, ID: "babel-analysis", At: observed}
	input.ContextID = guidance.ID
	if _, _, err := store.AssertFact(ctx, input); !isErr(err, ErrInvalidValue) {
		t.Errorf("an observation with guidance but no locator returned %v, want a refusal", err)
	}

	// With a locator it is accepted — and only as a proposal.
	input.Provenance = syntheticLocator(1)
	fact, _, err := store.AssertFact(ctx, input)
	if err != nil {
		t.Fatalf("AssertFact with a locator: %v", err)
	}
	if fact.Status != FactProposed {
		t.Errorf("an observation-derived fact is %s, want %s", fact.Status, FactProposed)
	}
	if fact.Payload.ContextID != guidance.ID {
		t.Errorf("the fact lost its guidance link")
	}
}
