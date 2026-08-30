package reality

import (
	"context"
	"testing"
)

// TestRelationshipsAreTypedFindableFromBothSidesAndRetractable covers §4.8's
// typed relationships and the reason analysis queries the ledger by
// relationship: an edge is as findable from its object as from its subject, and
// a wrong edge is retracted rather than deleted.
func TestRelationshipsAreTypedFindableFromBothSidesAndRetractable(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	machine := mustEntity(t, store, EntityMachine, "a machine")
	project := mustEntity(t, store, EntityProject, "a project")

	edge, err := store.AddRelationship(ctx, RelationshipInput{
		FromID:  service.ID,
		ToID:    machine.ID,
		Kind:    RelationDeployedOn,
		Payload: RelationshipPayload{Note: "from the inventory document"},
	})
	if err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: project.ID, ToID: service.ID, Kind: RelationContains,
	}); err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}

	// Findable from the object side, which is the direction a "what runs
	// here" query needs.
	fromMachine, err := store.Relationships(ctx, machine.ID)
	if err != nil {
		t.Fatalf("Relationships: %v", err)
	}
	if len(fromMachine) != 1 || fromMachine[0].ID != edge.ID {
		t.Errorf("the machine sees %+v, want the deployment edge", fromMachine)
	}
	fromService, err := store.Relationships(ctx, service.ID)
	if err != nil {
		t.Fatalf("Relationships: %v", err)
	}
	if len(fromService) != 2 {
		t.Errorf("the service sees %d edges, want both", len(fromService))
	}

	// One typed edge per pair: asserting it twice is refused rather than
	// duplicated, so a relationship graph has no repeated edges to reconcile.
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: service.ID, ToID: machine.ID, Kind: RelationDeployedOn,
	}); err == nil {
		t.Error("a duplicate typed edge was accepted")
	}
	// A different type between the same pair is a different claim.
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: service.ID, ToID: machine.ID, Kind: RelationHostedBy,
	}); err != nil {
		t.Errorf("a second edge type between one pair returned %v", err)
	}

	if err := store.RetractRelationship(ctx, edge.ID, "it moved"); err != nil {
		t.Fatalf("RetractRelationship: %v", err)
	}
	after, err := store.Relationships(ctx, machine.ID)
	if err != nil {
		t.Fatalf("Relationships: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("the machine sees %d edges after a retraction, want both still present", len(after))
	}
	var retracted bool
	for _, record := range after {
		if record.ID == edge.ID {
			retracted = record.State == StateRetracted
			if record.Payload.Note == "" {
				t.Error("the retracted edge lost its original note")
			}
		}
	}
	if !retracted {
		t.Error("the retracted edge is not marked retracted")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_relationship WHERE id = ?`, edge.ID); err == nil {
		t.Error("a relationship row accepted a delete")
	}

	// Refusals: an unknown kind, a self edge, and a dangling endpoint.
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: service.ID, ToID: machine.ID, Kind: "invented",
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("an unknown relationship kind returned %v", err)
	}
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: service.ID, ToID: service.ID, Kind: RelationRelatedTo,
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("a self edge returned %v", err)
	}
	if _, err := store.AddRelationship(ctx, RelationshipInput{
		FromID: service.ID, ToID: "ent_missing", Kind: RelationRelatedTo,
	}); !isErr(err, ErrUnknownRecord) {
		t.Errorf("a dangling endpoint returned %v", err)
	}
}

// TestExplicitDisputeCoversWhatTheDeterministicCheckCannotSee covers the
// judgement case: two facts a human or a plan considers incompatible in a way
// the value comparison cannot detect.
func TestExplicitDisputeCoversWhatTheDeterministicCheckCannotSee(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	machine := mustEntity(t, store, EntityMachine, "a machine")

	placement, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateServicePlacement,
		FactValue{Kind: ValueEntity, ObjectID: machine.ID}, clock.now()))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	deployment, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateDeploymentState,
		enum(DeploymentUndeployed), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	// Different predicates, so no value comparison could have caught it.
	dispute, err := store.DisputeFacts(ctx, DisputeInput{
		FactIDs: []string{placement.ID, deployment.ID},
		Actor:   "operator",
		Reason:  "a service cannot be placed on a machine and undeployed at once",
	})
	if err != nil {
		t.Fatalf("DisputeFacts: %v", err)
	}
	if len(dispute.FactIDs) != 2 || dispute.State != DisputeOpen {
		t.Errorf("dispute is %+v", dispute)
	}
	for _, id := range []string{placement.ID, deployment.ID} {
		fact, err := store.Fact(ctx, id)
		if err != nil {
			t.Fatalf("Fact: %v", err)
		}
		if fact.Status != FactDisputed {
			t.Errorf("fact %q is %s, want %s", id, fact.Status, FactDisputed)
		}
	}

	// Resolving it twice is refused: a dispute is settled once.
	if err := store.ResolveDispute(ctx, ResolveDisputeInput{
		DisputeID: dispute.ID, KeepFactID: deployment.ID, Actor: "operator",
	}); err != nil {
		t.Fatalf("ResolveDispute: %v", err)
	}
	if err := store.ResolveDispute(ctx, ResolveDisputeInput{
		DisputeID: dispute.ID, KeepFactID: deployment.ID, Actor: "operator",
	}); !isErr(err, ErrInvalidTransition) {
		t.Errorf("a second resolution returned %v, want ErrInvalidTransition", err)
	}

	// And a resolution cannot uphold a fact outside the dispute.
	other, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateOwnership,
		enum(OwnershipOwned), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	second, err := store.DisputeFacts(ctx, DisputeInput{
		FactIDs: []string{placement.ID, deployment.ID},
		Actor:   "operator",
		Reason:  "still incompatible",
	})
	if err != nil {
		t.Fatalf("DisputeFacts: %v", err)
	}
	if err := store.ResolveDispute(ctx, ResolveDisputeInput{
		DisputeID: second.ID, KeepFactID: other.ID, Actor: "operator",
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("upholding an unrelated fact returned %v", err)
	}

	// A dispute needs at least two facts and an actor.
	if _, err := store.DisputeFacts(ctx, DisputeInput{
		FactIDs: []string{placement.ID}, Actor: "operator",
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("a one-sided dispute returned %v", err)
	}
	if _, err := store.DisputeFacts(ctx, DisputeInput{
		FactIDs: []string{placement.ID, deployment.ID},
	}); !isErr(err, ErrInvalidValue) {
		t.Errorf("an unattributed dispute returned %v", err)
	}
}

// TestAMergeDoesNotHideAContradictionItCreates checks the interaction between
// the two mechanisms: once a merge recognizes two names as one subject, facts
// asserted under either name are facts about that subject, so a later
// contradiction is detected rather than sitting quietly under the other name.
func TestAMergeDoesNotHideAContradictionItCreates(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	folded := mustEntity(t, store, EntityProject, "old name")
	surviving := mustEntity(t, store, EntityProject, "current name")

	if _, _, err := store.AssertFact(ctx, operatorFact(folded.ID, PredicateLifecycle,
		enum(LifecycleActive), clock.now())); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{folded.ID},
		TargetID:  surviving.ID,
		Actor:     "operator",
		Reason:    "one project under two names",
	}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	_, dispute, err := store.AssertFact(ctx, operatorFact(surviving.ID, PredicateLifecycle,
		enum(LifecycleRetired), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	if dispute.ID == "" {
		t.Fatal("a contradiction across a merged identity produced no dispute")
	}
}
