package reality

import (
	"context"
	"testing"
	"time"
)

// TestFactIsNeverMutatedOnlySuperseded is §4.8's immutability rule, checked at
// the level that matters: the ancestor's row is byte-identical after the
// supersession, not merely equal in the fields a reader decodes.
//
// It also checks the two things that make immutability real rather than
// conventional. The database refuses an update and a delete outright, so the
// invariant does not depend on this package writing the right statements. And
// the revision chain cannot fork: a second attempt to supersede one revision
// loses, because two competing successors would leave no answer to "what does
// the ledger say now".
func TestFactIsNeverMutatedOnlySuperseded(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	observed := clock.now()
	original, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), observed))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	before := rowSnapshot(t, store, `SELECT * FROM reality_fact WHERE id = ?`, original.ID)

	later := clock.now()
	revision, err := store.SupersedeFact(ctx, SupersedeInput{
		PriorID: original.ID,
		Fact: operatorFact(project.ID, PredicateLifecycle,
			enum(LifecycleMaintenanceOnly), later),
	})
	if err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	after := rowSnapshot(t, store, `SELECT * FROM reality_fact WHERE id = ?`, original.ID)
	if before != after {
		t.Errorf("the superseded fact's row changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	ancestor, err := store.Fact(ctx, original.ID)
	if err != nil {
		t.Fatalf("Fact(ancestor): %v", err)
	}
	if ancestor.Value.Enum != LifecycleActive {
		t.Errorf("the ancestor's value changed to %q", ancestor.Value.Enum)
	}
	if ancestor.Status != FactSuperseded {
		t.Errorf("the ancestor is %s, want %s", ancestor.Status, FactSuperseded)
	}
	if revision.Supersedes != original.ID || revision.Status != FactActive {
		t.Errorf("the revision is %+v, want an active successor of the ancestor", revision)
	}

	// The status history keeps both states, so the transition is auditable.
	history, err := store.FactStatusHistory(ctx, original.ID)
	if err != nil {
		t.Fatalf("FactStatusHistory: %v", err)
	}
	if len(history) != 2 || history[0].Status != FactActive || history[1].Status != FactSuperseded {
		t.Errorf("status history is %+v, want active then superseded", history)
	}

	// The chain cannot fork.
	if _, err := store.SupersedeFact(ctx, SupersedeInput{
		PriorID: original.ID,
		Fact:    operatorFact(project.ID, PredicateLifecycle, enum(LifecycleRetired), clock.now()),
	}); !isErr(err, ErrConflict) {
		t.Errorf("a second supersession of one revision returned %v, want ErrConflict", err)
	}

	// And the engine refuses what this package has no method for.
	if _, err := store.db.Exec(`UPDATE reality_fact SET payload_json = ? WHERE id = ?`,
		`{"value":{"kind":"enum","enum":"retired"}}`, original.ID); err == nil {
		t.Error("a fact row accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_fact WHERE id = ?`, original.ID); err == nil {
		t.Error("a fact row accepted a delete")
	}
}

// TestContradictionCreatesADisputeRatherThanOneSilentlyWinning is §4.8's
// conflict rule. The second assertion does not overwrite, does not lose, and
// does not quietly become the answer: both facts become disputed and a dispute
// row records them, so the ledger can hold "these two disagree and nobody has
// decided" instead of preferring whichever arrived second.
func TestContradictionCreatesADisputeRatherThanOneSilentlyWinning(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	first, dispute, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact(first): %v", err)
	}
	if dispute.ID != "" {
		t.Fatalf("the first assertion created a dispute: %+v", dispute)
	}

	second, dispute, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleRetired), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact(second): %v", err)
	}
	if dispute.ID == "" {
		t.Fatal("two contradicting facts produced no dispute")
	}
	if dispute.State != DisputeOpen {
		t.Errorf("dispute is %s, want %s", dispute.State, DisputeOpen)
	}
	if len(dispute.FactIDs) != 2 {
		t.Errorf("dispute names %d facts, want both", len(dispute.FactIDs))
	}

	// Neither side is in force, which is the whole point: nothing silently won.
	active, err := store.Facts(ctx, FactQuery{
		SubjectID: project.ID,
		Predicate: PredicateLifecycle,
		Statuses:  []FactStatus{FactActive},
	})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("%d facts are still active during a dispute", len(active))
	}
	for _, id := range []string{first.ID, second.ID} {
		fact, err := store.Fact(ctx, id)
		if err != nil {
			t.Fatalf("Fact: %v", err)
		}
		if fact.Status != FactDisputed {
			t.Errorf("fact %q is %s, want %s", id, fact.Status, FactDisputed)
		}
	}
	linked, err := store.DisputesFor(ctx, first.ID)
	if err != nil {
		t.Fatalf("DisputesFor: %v", err)
	}
	if len(linked) != 1 || linked[0].ID != dispute.ID {
		t.Errorf("the fact does not link back to its dispute: %+v", linked)
	}

	// An operator resolving it upholds one side explicitly; the other is set
	// aside without being deleted.
	if err := store.ResolveDispute(ctx, ResolveDisputeInput{
		DisputeID:  dispute.ID,
		KeepFactID: second.ID,
		Actor:      "operator",
		Note:       "the project was retired last month",
	}); err != nil {
		t.Fatalf("ResolveDispute: %v", err)
	}
	upheld, err := store.Fact(ctx, second.ID)
	if err != nil {
		t.Fatalf("Fact(upheld): %v", err)
	}
	if upheld.Status != FactActive {
		t.Errorf("the upheld fact is %s, want %s", upheld.Status, FactActive)
	}
	setAside, err := store.Fact(ctx, first.ID)
	if err != nil {
		t.Fatalf("Fact(set aside): %v", err)
	}
	if setAside.Status != FactSuperseded {
		t.Errorf("the set-aside fact is %s, want %s", setAside.Status, FactSuperseded)
	}
	if setAside.Value.Enum != LifecycleActive {
		t.Error("the set-aside fact's value was rewritten")
	}
}

// TestDisjointValidTimesAndAgreementAreNotConflicts checks that the
// contradiction rule is narrow. A fact that stopped being true is history, and
// a repeated assertion of the same value is corroboration; treating either as a
// conflict would fill the inbox with disputes nobody needs to resolve.
func TestDisjointValidTimesAndAgreementAreNotConflicts(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	closed := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleActive), clock.now())
	closed.ValidFrom = baseTime.Add(-72 * time.Hour)
	closed.ValidUntil = baseTime.Add(-24 * time.Hour)
	if _, dispute, err := store.AssertFact(ctx, closed); err != nil || dispute.ID != "" {
		t.Fatalf("a closed interval conflicted: %v, %+v", err, dispute)
	}

	current := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleDormant), clock.now())
	current.ValidFrom = baseTime.Add(-24 * time.Hour)
	if _, dispute, err := store.AssertFact(ctx, current); err != nil || dispute.ID != "" {
		t.Fatalf("a later interval conflicted with an ended one: %v, %+v", err, dispute)
	}

	agreeing := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleDormant), clock.now())
	agreeing.ValidFrom = baseTime.Add(-12 * time.Hour)
	if _, dispute, err := store.AssertFact(ctx, agreeing); err != nil || dispute.ID != "" {
		t.Fatalf("an agreeing assertion conflicted: %v, %+v", err, dispute)
	}
}

// TestExpiryMarksStaleRatherThanDeleting is §4.8's freshness rule, and it has
// two halves that must both hold. A volatile predicate's fact goes stale on
// schedule and its row survives untouched, with its provenance and authority
// intact. And operator intent never expires on its own, however long it sits,
// because §4.8 says so and because an analysis policy that lapsed silently
// would widen what analysis may spend.
func TestExpiryMarksStaleRatherThanDeleting(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")

	observed := clock.now()
	volatile, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateDeploymentState,
		enum(DeploymentDeployed), observed))
	if err != nil {
		t.Fatalf("AssertFact(volatile): %v", err)
	}
	intent, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateAnalysisPolicy,
		enum(PolicyLearnOnly), observed))
	if err != nil {
		t.Fatalf("AssertFact(intent): %v", err)
	}
	if volatile.ExpiresAt.IsZero() {
		t.Error("a volatile predicate's fact has no freshness horizon")
	}
	if !intent.ExpiresAt.IsZero() {
		t.Error("operator intent was given a freshness horizon")
	}

	before := rowSnapshot(t, store, `SELECT * FROM reality_fact WHERE id = ?`, volatile.ID)

	// Nothing expires before its horizon.
	if marked, err := store.ExpireStale(ctx, observed.Add(24*time.Hour)); err != nil || len(marked) != 0 {
		t.Fatalf("ExpireStale before the horizon marked %v (%v)", marked, err)
	}

	// A decade later, intent is still in force and the volatile observation
	// is stale.
	marked, err := store.ExpireStale(ctx, observed.Add(10*365*24*time.Hour))
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if len(marked) != 1 || marked[0] != volatile.ID {
		t.Fatalf("ExpireStale marked %v, want only the volatile fact", marked)
	}

	stale, err := store.Fact(ctx, volatile.ID)
	if err != nil {
		t.Fatalf("the expired fact is gone: %v", err)
	}
	if stale.Status != FactStale {
		t.Errorf("the expired fact is %s, want %s", stale.Status, FactStale)
	}
	if after := rowSnapshot(t, store, `SELECT * FROM reality_fact WHERE id = ?`, volatile.ID); after != before {
		t.Errorf("expiry changed the fact's row:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	history, err := store.FactStatusHistory(ctx, volatile.ID)
	if err != nil {
		t.Fatalf("FactStatusHistory: %v", err)
	}
	if len(history) != 2 || history[1].Status != FactStale {
		t.Errorf("status history is %+v, want active then stale", history)
	}

	survivor, err := store.Fact(ctx, intent.ID)
	if err != nil {
		t.Fatalf("Fact(intent): %v", err)
	}
	if survivor.Status != FactActive {
		t.Errorf("operator intent is %s a decade later, want %s", survivor.Status, FactActive)
	}

	// Expiry is idempotent: a second pass does not re-mark what is already
	// stale, which would otherwise grow the history without adding anything.
	if again, err := store.ExpireStale(ctx, observed.Add(20*365*24*time.Hour)); err != nil ||
		len(again) != 0 {
		t.Errorf("a second expiry pass marked %v (%v)", again, err)
	}
}

// TestPredicateFreshnessIsDeclaredPerPredicate pins the registry, because the
// freshness rule §4.8 describes is only meaningful if each predicate's answer
// is stated rather than inherited. Every predicate must also give a reason for
// its choice: a TTL with no stated reason is a number someone will change
// without knowing what it meant.
func TestPredicateFreshnessIsDeclaredPerPredicate(t *testing.T) {
	expected := map[Predicate]bool{
		PredicateLifecycle:        false,
		PredicateOwnership:        false,
		PredicateAnalysisPolicy:   false,
		PredicateServicePlacement: true,
		PredicateDeploymentState:  true,
		PredicateLocalPath:        true,
	}
	known := Predicates()
	if len(known) != len(expected) {
		t.Fatalf("Predicates() returned %d entries, want %d", len(known), len(expected))
	}
	for _, predicate := range known {
		wantExpires, ok := expected[predicate]
		if !ok {
			t.Errorf("unexpected predicate %q", predicate)
			continue
		}
		ttl, why := predicate.TTL()
		if (ttl > 0) != wantExpires {
			t.Errorf("predicate %s has ttl %v, want expiring=%v", predicate, ttl, wantExpires)
		}
		if why == "" {
			t.Errorf("predicate %s states no reason for its freshness rule", predicate)
		}
	}
}

// TestValueVocabularyIsClosed checks that a focus rule matching on a value can
// be deterministic: an out-of-vocabulary value never enters the ledger, and a
// predicate's declared value type is enforced.
func TestValueVocabularyIsClosed(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")
	machine := mustEntity(t, store, EntityMachine, "a machine")

	cases := []struct {
		name  string
		value FactValue
		pred  Predicate
	}{
		{"unknown enum value", enum("mothballed"), PredicateLifecycle},
		{"text where an enum belongs", FactValue{Kind: ValueText, Text: "active"}, PredicateLifecycle},
		{"enum where an entity belongs", enum("somewhere"), PredicateServicePlacement},
		{"empty text", FactValue{Kind: ValueText, Text: "   "}, PredicateLocalPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := store.AssertFact(ctx,
				operatorFact(project.ID, tc.pred, tc.value, clock.now())); !isErr(err, ErrInvalidValue) {
				t.Errorf("got %v, want ErrInvalidValue", err)
			}
		})
	}

	// An object-entity value must name an entity that exists, and the ledger
	// becomes queryable by that object.
	placement := operatorFact(project.ID, PredicateServicePlacement,
		FactValue{Kind: ValueEntity, ObjectID: machine.ID}, clock.now())
	if _, _, err := store.AssertFact(ctx, placement); err != nil {
		t.Fatalf("AssertFact(placement): %v", err)
	}
	missing := operatorFact(project.ID, PredicateServicePlacement,
		FactValue{Kind: ValueEntity, ObjectID: "ent_missing"}, clock.now())
	if _, _, err := store.AssertFact(ctx, missing); !isErr(err, ErrUnknownRecord) {
		t.Errorf("a fact about a missing object entity was accepted")
	}
}

// TestObservationAuthorityCanOnlyPropose is §4.8's authority rule. Git
// activity, repository inspection, and Babel's own analysis are observations,
// so they can enter the ledger as proposed revisions and never as reality —
// whatever status the caller would prefer.
func TestObservationAuthorityCanOnlyPropose(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	observed := clock.now()
	active, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), observed))
	if err != nil {
		t.Fatalf("AssertFact(operator): %v", err)
	}

	proposal := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleRetired), clock.now())
	proposal.Authority = Authority{Kind: AuthorityObservation, ID: "git-activity", At: observed}
	proposal.Provenance = syntheticLocator(2)
	record, dispute, err := store.AssertFact(ctx, proposal)
	if err != nil {
		t.Fatalf("AssertFact(observation): %v", err)
	}
	if record.Status != FactProposed {
		t.Errorf("an observation-derived fact is %s, want %s", record.Status, FactProposed)
	}
	// A proposal contradicts nothing: it is a proposed revision, not a claim
	// in force, so it must not drag the active fact into a dispute.
	if dispute.ID != "" {
		t.Errorf("a proposal opened a dispute: %+v", dispute)
	}
	current, err := store.Fact(ctx, active.ID)
	if err != nil {
		t.Fatalf("Fact: %v", err)
	}
	if current.Status != FactActive {
		t.Errorf("the operator's fact became %s because of a proposal", current.Status)
	}

	// A caller cannot claim trusted-source authority directly; that path
	// exists only where the declared scope is checked.
	sourced := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleDormant), clock.now())
	sourced.Authority = Authority{Kind: AuthorityTrustedSource, ID: "inventory", At: observed}
	sourced.Provenance = syntheticLocator(3)
	if _, _, err := store.AssertFact(ctx, sourced); !isErr(err, ErrOutsideScope) {
		t.Errorf("a direct trusted-source assertion returned %v, want ErrOutsideScope", err)
	}
}
