package reality

import (
	"bytes"
	"context"
	"testing"

	babelsync "github.com/atyrode/babel/internal/sync"
)

func TestRestageRecoversCanonicalRealityBundlesOnce(t *testing.T) {
	ctx := context.Background()
	// The recording hook captures live canonical publications but does not
	// journal them, reproducing the historical missing-journal condition.
	hook := &recordingHook{}
	fixture := newPlanFixture(t, WithSync(hook))
	store, clock := fixture.store, fixture.clock
	if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{PlanID: fixture.plan.ID, Actor: "operator", Note: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AttachContext(ctx, ContextInput{Author: "operator", At: clock.now(), Text: "preserve <literal> guidance"}); err != nil {
		t.Fatal(err)
	}
	parent := mustEntity(t, store, EntityRepository, "combined repository")
	split, _, err := store.SplitEntity(ctx, SplitInput{
		ParentID: parent.ID, Actor: "operator",
		Parts: []EntityInput{
			{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "first"}},
			{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "second"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoResolution(ctx, UndoInput{ResolutionID: split.ID, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	machine := mustEntity(t, store, EntityMachine, "machine")
	service := mustEntity(t, store, EntityService, "service")
	source := registerInventory(t, store)
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID, BatchKey: "batch-1",
		Facts: []FactInput{inventoryPlacement(service.ID, machine.ID, clock.now())},
	}); err != nil {
		t.Fatal(err)
	}
	first, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateLifecycle, enum(LifecycleActive), clock.now()))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateOwnership, enum(OwnershipOwned), clock.now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DisputeFacts(ctx, DisputeInput{FactIDs: []string{first.ID, second.ID}, Actor: "original-operator", Reason: "operator judgment"}); err != nil {
		t.Fatal(err)
	}
	store.sync = babelsync.NewStager()
	count, err := store.Restage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(hook.staged) {
		t.Fatalf("restaged %d records, want %d live publications", count, len(hook.staged))
	}
	kinds := make(map[PublishedKind]bool)
	for _, want := range hook.staged {
		var runID string
		var payload []byte
		if err := store.db.QueryRowContext(ctx, `SELECT r.run_id, p.payload FROM sync_record r JOIN sync_payload p ON p.record_id = r.record_id WHERE r.record_id = ?`, want.EntityID).Scan(&runID, &payload); err != nil {
			t.Fatal(err)
		}
		if runID != want.RunID || !bytes.Equal(payload, want.Payload) {
			t.Fatalf("record %s recovered under %s (want %s) or with changed canonical bytes\ngot %s\nwant %s", want.EntityID, runID, want.RunID, payload, want.Payload)
		}
		wire, err := DecodePublishedRecord(payload)
		if err != nil {
			t.Fatal(err)
		}
		kinds[wire.Kind] = true
	}
	for _, kind := range []PublishedKind{PublishedEntity, PublishedFact, PublishedAnswer, PublishedContext, PublishedImport, PublishedResolution, PublishedMembership, PublishedDispute, PublishedPlan, PublishedAcceptance} {
		if !kinds[kind] {
			t.Errorf("missing coverage for %s", kind)
		}
	}
	if count, err := store.Restage(ctx); err != nil || count != 0 {
		t.Fatalf("second restage = %d, %v; want 0, nil", count, err)
	}
}
