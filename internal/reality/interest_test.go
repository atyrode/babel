package reality

import (
	"context"
	"testing"
)

// TestInterestIsTheLedgersOwnFactsAndNothingElse is §4.13's central claim
// about stance: *interest is a fact about the world, not a preference knob*.
//
// So the test asserts the facts and not a stored state. Each stance is the
// lifecycle and analysis-policy pair §4.8 already had, attributed to the
// operator with his reason verbatim, and the derived state is a reading of
// them — which is what makes a paused project paused everywhere Babel looks
// rather than only on the page the operator clicked.
func TestInterestIsTheLedgersOwnFactsAndNothingElse(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	entity := mustEntity(t, store, EntityRepository, "manifold")

	cases := []struct {
		state     string
		lifecycle string
		policy    string
	}{
		{InterestWorking, LifecycleActive, PolicyNormal},
		{InterestWatching, LifecycleMaintenanceOnly, PolicyLearnOnly},
		{InterestNotNow, LifecycleDormant, PolicyLearnOnly},
		{InterestExcluded, LifecycleDormant, PolicyExcluded},
	}
	for _, tc := range cases {
		reason := "because " + tc.state
		if err := store.SetInterest(ctx, entity.ID, "operator", tc.state, reason); err != nil {
			t.Fatalf("SetInterest(%s): %v", tc.state, err)
		}
		current, err := store.currentInterestFacts(ctx, entity.ID)
		if err != nil {
			t.Fatalf("read facts: %v", err)
		}
		// Excluded leaves lifecycle alone, which is why the case above
		// expects the previous stance's dormant rather than a value of
		// its own: §4.8 keeps the two predicates separate, and excluding
		// a subject says nothing about whether anyone works on it.
		if got := current[PredicateLifecycle].Value.Enum; got != tc.lifecycle {
			t.Errorf("%s: lifecycle is %q, want %q", tc.state, got, tc.lifecycle)
		}
		if got := current[PredicateAnalysisPolicy].Value.Enum; got != tc.policy {
			t.Errorf("%s: analysis policy is %q, want %q", tc.state, got, tc.policy)
		}
		if got := current[PredicateAnalysisPolicy].Authority; got.Kind != AuthorityOperator ||
			got.ID != "operator" {
			t.Errorf("%s: the policy is attributed to %s/%s", tc.state, got.Kind, got.ID)
		}

		interest, err := store.EntityInterest(ctx, entity.ID)
		if err != nil {
			t.Fatalf("EntityInterest: %v", err)
		}
		if interest.State != tc.state {
			t.Errorf("the stance reads back as %q, want %q", interest.State, tc.state)
		}
		if interest.Reason != reason {
			t.Errorf("the reason reads back as %q, want it verbatim", interest.Reason)
		}
		if interest.By != "operator" || interest.At.IsZero() {
			t.Errorf("the stance is attributed to %q at %v", interest.By, interest.At)
		}
	}

	// Nothing was edited. Changing a stance four times leaves four
	// revisions of each predicate, the older ones superseded, which is the
	// difference between an append-only ledger and a settings page.
	policies, err := store.Facts(ctx, FactQuery{SubjectID: entity.ID, Predicate: PredicateAnalysisPolicy})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(policies) != len(cases) {
		t.Fatalf("the ledger holds %d analysis-policy revisions, want one per stance", len(policies))
	}
	active := 0
	for _, fact := range policies {
		switch fact.Status {
		case FactActive:
			active++
		case FactSuperseded:
		default:
			t.Errorf("revision %s is %s, want active or superseded", fact.ID, fact.Status)
		}
	}
	if active != 1 {
		t.Errorf("%d analysis-policy revisions are in force, want exactly one", active)
	}
}

// TestSetInterestRefusesWhatItCannotRecord keeps the vocabulary closed and the
// attribution required: a stance nobody answers for is worse than one nobody
// recorded, and a fifth state would be a value nothing can store.
func TestSetInterestRefusesWhatItCannotRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	entity := mustEntity(t, store, EntityRepository, "manifold")

	if err := store.SetInterest(ctx, entity.ID, "operator", "interested", "why not"); !isErr(err, ErrInvalidValue) {
		t.Errorf("an invented stance: %v, want ErrInvalidValue", err)
	}
	if err := store.SetInterest(ctx, entity.ID, "", InterestWorking, "why"); !isErr(err, ErrInvalidValue) {
		t.Errorf("an unattributed stance: %v, want ErrInvalidValue", err)
	}
	if err := store.SetInterest(ctx, "ent-nothing", "operator", InterestWorking, "why"); !isErr(err, ErrUnknownRecord) {
		t.Errorf("a stance about nothing: %v, want ErrUnknownRecord", err)
	}
	if got := InterestStates(); len(got) != 4 {
		t.Errorf("the vocabulary has %d states: %v", len(got), got)
	}
}

// TestRetireEntityKeepsEverythingItNamed is §4.13's retirement: a topic that
// should never have existed is retired with a reason, and nothing is deleted —
// the entity, its aliases and its facts all stay readable, and the retirement
// is itself a superseding revision rather than an edit.
func TestRetireEntityKeepsEverythingItNamed(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	entity := mustEntity(t, store, EntityRepository, "tmp")

	if err := store.SetInterest(ctx, entity.ID, "operator", InterestWorking, "starting"); err != nil {
		t.Fatalf("SetInterest: %v", err)
	}
	const reason = "/tmp is a directory, not a project"
	if err := store.RetireEntity(ctx, entity.ID, "operator", reason); err != nil {
		t.Fatalf("RetireEntity: %v", err)
	}

	retired, err := store.EntityRetired(ctx, entity.ID)
	if err != nil {
		t.Fatalf("EntityRetired: %v", err)
	}
	if !retired {
		t.Error("the subject does not read as retired")
	}
	interest, err := store.EntityInterest(ctx, entity.ID)
	if err != nil {
		t.Fatalf("EntityInterest: %v", err)
	}
	if interest.State != "" {
		t.Errorf("a retired subject reads as %q; retirement is not a degree of interest", interest.State)
	}
	if _, err := store.Entity(ctx, entity.ID); err != nil {
		t.Errorf("the retired subject is gone: %v", err)
	}

	lifecycle, err := store.Facts(ctx, FactQuery{SubjectID: entity.ID, Predicate: PredicateLifecycle})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(lifecycle) != 2 {
		t.Fatalf("the ledger holds %d lifecycle revisions, want the stance and its retirement", len(lifecycle))
	}
	var kept bool
	for _, fact := range lifecycle {
		if fact.Value.Enum == LifecycleRetired && fact.Payload.Note == reason {
			kept = true
			if fact.Supersedes == "" {
				t.Error("the retirement replaced nothing; it should supersede the stance in force")
			}
		}
	}
	if !kept {
		t.Error("the retirement's reason is not recorded verbatim")
	}

	// A retired identity does not bind: §4.13 retires a topic that should
	// never have existed, and a retirement that permanently forbade
	// re-proposing the thing would make the mistake unfixable.
	if _, _, err := store.AssertFact(ctx, operatorFact(entity.ID, PredicateRepositoryRemote,
		FactValue{Kind: ValueText, Text: "github.com/atyrode/tmp"}, baseTime)); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	bound, err := store.EntityBoundTo(ctx, "github.com/atyrode/tmp")
	if err != nil {
		t.Fatalf("EntityBoundTo: %v", err)
	}
	if bound != "" {
		t.Errorf("the retired subject still binds its identity as %s", bound)
	}
}
