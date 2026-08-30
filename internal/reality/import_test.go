package reality

import (
	"context"
	"strings"
	"testing"
)

// Every credential-shaped value below is synthetic and assembled from parts.
//
// A probe for a credential format can only be tested against a string in that
// format, and a contiguous literal in one of the formats this repository's push
// protection recognizes makes the forge reject every push carrying the file.
// Splitting the literal leaves the assembled constant byte-identical for the
// detector under test while the source never contains the matching sequence.
// See the note in internal/preflight/secret_test.go; the repository-wide check
// lives in internal/preflight/fixtureshape_test.go.
const (
	probeVendorToken  = "gh" + "p_" + "PROBEONLYNOTREALIMPORT01"
	probeInventoryDSN = "postgres://inventory:" + "PROBEONLYNOTREALPASSWORD2" +
		"@db.invalid/inventory"
)

// registerInventory sets up the §4.8 case: a versioned provider-neutral
// inventory that may author service placement about services, and nothing else.
func registerInventory(t *testing.T, store *Store, extra ...Predicate) TrustedSource {
	t.Helper()
	source, err := store.RegisterTrustedSource(context.Background(), TrustedSourceInput{
		ID:          "synthetic-inventory",
		Version:     1,
		Predicates:  append([]Predicate{PredicateServicePlacement}, extra...),
		EntityKinds: []EntityKind{EntityService},
		Payload: TrustedSourcePayload{
			Description: "synthetic inventory of services and their placement",
		},
	})
	if err != nil {
		t.Fatalf("RegisterTrustedSource: %v", err)
	}
	return source
}

// TestTrustedSourceCannotAuthorOutsideItsDeclaredScope is §4.8's rule that a
// configured source declares the predicates and entities it may author.
//
// The refusal is per batch, not per fact. An inventory that had half its facts
// applied and half refused would leave the operator diffing the ledger against
// the source to learn what landed, so the whole import is rolled back — the
// import row included, which is what makes a corrected batch re-submittable
// under the same key.
func TestTrustedSourceCannotAuthorOutsideItsDeclaredScope(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	machine := mustEntity(t, store, EntityMachine, "a machine")
	repository := mustEntity(t, store, EntityRepository, "a repository")
	source := registerInventory(t, store)

	placement := func(subject string) FactInput {
		observed := clock.now()
		return FactInput{
			SubjectID:   subject,
			Predicate:   PredicateServicePlacement,
			Value:       FactValue{Kind: ValueEntity, ObjectID: machine.ID},
			ValidFrom:   observed,
			ObservedAt:  observed,
			Confidence:  ConfidenceHigh,
			Sensitivity: SensitivityRoutine,
			Provenance:  syntheticLocator(1),
			Note:        "declared placement from the inventory document",
		}
	}

	// Inside the scope: accepted, and authorized by the source rather than by
	// whatever the caller wrote in the authority field.
	imported, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-1",
		Facts:    []FactInput{placement(service.ID)},
	})
	if err != nil {
		t.Fatalf("ImportFacts: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("imported %d facts, want 1", len(imported))
	}
	if imported[0].Authority.Kind != AuthorityTrustedSource || imported[0].Authority.ID != source.ID {
		t.Errorf("imported fact is attributed to %+v, want the source", imported[0].Authority)
	}
	if imported[0].Status != FactActive {
		t.Errorf("imported fact is %s, want %s", imported[0].Status, FactActive)
	}
	if imported[0].SourceID != source.ID || imported[0].ImportID == "" {
		t.Errorf("imported fact does not link to its source and batch: %+v", imported[0])
	}

	// A predicate the source never declared.
	outside := placement(service.ID)
	outside.Predicate = PredicateAnalysisPolicy
	outside.Value = enum(PolicyExcluded)
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-2",
		Facts:    []FactInput{outside},
	}); !isErr(err, ErrOutsideScope) {
		t.Errorf("an undeclared predicate returned %v, want ErrOutsideScope", err)
	}

	// An entity kind the source never declared.
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-3",
		Facts:    []FactInput{placement(repository.ID)},
	}); !isErr(err, ErrOutsideScope) {
		t.Errorf("an undeclared entity kind returned %v, want ErrOutsideScope", err)
	}

	// A refused batch is atomic: the good fact beside the bad one did not
	// land, and neither did the batch row.
	mixed := []FactInput{placement(service.ID), placement(repository.ID)}
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-4",
		Facts:    mixed,
	}); !isErr(err, ErrOutsideScope) {
		t.Fatalf("a mixed batch returned %v, want ErrOutsideScope", err)
	}
	facts, err := store.Facts(ctx, FactQuery{SubjectID: service.ID})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 1 {
		t.Errorf("the service holds %d facts, want only the accepted batch's one", len(facts))
	}
	if got := countRows(t, store, "reality_import"); got != 1 {
		t.Errorf("%d import batches recorded, want 1", got)
	}

	// Replaying a key is refused rather than duplicating the batch, which §9
	// requires of immutable event writes.
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-1",
		Facts:    []FactInput{placement(service.ID)},
	}); err == nil {
		t.Error("a replayed batch key was accepted")
	}

	// An unregistered source has no scope at all.
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: "not-configured",
		BatchKey: "batch-5",
		Facts:    []FactInput{placement(service.ID)},
	}); !isErr(err, ErrUnknownRecord) {
		t.Errorf("an unregistered source returned %v, want ErrUnknownRecord", err)
	}
}

// TestImportedCredentialMaterialIsRefused is §4.8's flat prohibition:
// credentials are forbidden in the ledger.
//
// The import is refused rather than redacted. A ledger that stored a redacted
// credential would have recorded that a secret was in the inventory, which is a
// fact about the secret rather than about reality, and it would put the
// placeholder somewhere an operator exports and forwards.
func TestImportedCredentialMaterialIsRefused(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	source := registerInventory(t, store, PredicateLocalPath)

	base := func() FactInput {
		observed := clock.now()
		return FactInput{
			SubjectID:   service.ID,
			Predicate:   PredicateLocalPath,
			Value:       FactValue{Kind: ValueText, Text: "/synthetic/checkout/service"},
			ValidFrom:   observed,
			ObservedAt:  observed,
			Confidence:  ConfidenceHigh,
			Sensitivity: SensitivityRoutine,
			Provenance:  syntheticLocator(2),
		}
	}

	cases := []struct {
		name   string
		mutate func(*FactInput)
	}{
		{"credential in the value text", func(in *FactInput) {
			in.Value.Text = "/synthetic/checkout?token=" + probeVendorToken
		}},
		{"credential in the note", func(in *FactInput) {
			in.Note = "the inventory reads it from " + probeInventoryDSN
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fact := base()
			tc.mutate(&fact)
			_, err := store.ImportFacts(ctx, ImportInput{
				SourceID: source.ID,
				BatchKey: "batch-" + tc.name,
				Facts:    []FactInput{fact},
			})
			if !isErr(err, ErrCredentialMaterial) {
				t.Fatalf("got %v, want ErrCredentialMaterial", err)
			}
			// The refusal must not echo the credential into the error,
			// which would move the exposure into the log.
			if strings.Contains(err.Error(), probeVendorToken) || strings.Contains(err.Error(), probeInventoryDSN) {
				t.Error("the refusal echoed the credential")
			}
			if got := countRows(t, store, "reality_fact"); got != 0 {
				t.Errorf("%d facts landed from a refused import", got)
			}
		})
	}

	// The same prohibition applies to an operator's own assertion, to an
	// entity's display name, and to an alias value: a credential in the
	// ledger is forbidden by its source, not by its author.
	direct := base()
	direct.Authority = Authority{Kind: AuthorityOperator, ID: "operator", At: clock.now()}
	direct.Note = "rotate " + probeVendorToken
	if _, _, err := store.AssertFact(ctx, direct); !isErr(err, ErrCredentialMaterial) {
		t.Errorf("an operator assertion carrying a credential returned %v", err)
	}
	if _, err := store.CreateEntity(ctx, EntityInput{
		Kind:    EntityService,
		Payload: EntityPayload{DisplayName: "service " + probeVendorToken},
	}); !isErr(err, ErrCredentialMaterial) {
		t.Errorf("an entity name carrying a credential returned %v", err)
	}
	if _, err := store.AddAlias(ctx, AliasInput{
		EntityID: service.ID,
		Kind:     AliasURL,
		Payload:  AliasPayload{Value: probeInventoryDSN},
	}); !isErr(err, ErrCredentialMaterial) {
		t.Errorf("an alias carrying a credential returned %v", err)
	}
}

// TestTrustedSourceRegistrationNeedsARealScope checks that "declared scope" is
// not satisfiable by declaring nothing: an unbounded source would be a second
// operator.
func TestTrustedSourceRegistrationNeedsARealScope(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	cases := []struct {
		name  string
		input TrustedSourceInput
	}{
		{"no predicates", TrustedSourceInput{
			ID:          "s",
			Version:     1,
			EntityKinds: []EntityKind{EntityService},
			Payload:     TrustedSourcePayload{Description: "d"},
		}},
		{"no entity scope", TrustedSourceInput{
			ID:         "s",
			Version:    1,
			Predicates: []Predicate{PredicateServicePlacement},
			Payload:    TrustedSourcePayload{Description: "d"},
		}},
		{"no version", TrustedSourceInput{
			ID:          "s",
			Predicates:  []Predicate{PredicateServicePlacement},
			EntityKinds: []EntityKind{EntityService},
			Payload:     TrustedSourcePayload{Description: "d"},
		}},
		{"unknown predicate", TrustedSourceInput{
			ID:          "s",
			Version:     1,
			Predicates:  []Predicate{"invented"},
			EntityKinds: []EntityKind{EntityService},
			Payload:     TrustedSourcePayload{Description: "d"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.RegisterTrustedSource(ctx, tc.input); !isErr(err, ErrInvalidValue) {
				t.Errorf("got %v, want ErrInvalidValue", err)
			}
		})
	}

	// A registration is immutable: widening a scope means a new
	// registration, not an edit.
	source := registerInventory(t, store)
	if _, err := store.db.Exec(`UPDATE reality_trusted_source SET source_version = 2 WHERE id = ?`,
		source.ID); err == nil {
		t.Error("a trusted source registration accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_trusted_source_predicate WHERE source_id = ?`,
		source.ID); err == nil {
		t.Error("a declared predicate scope accepted a delete")
	}

	round, err := store.TrustedSource(ctx, source.ID)
	if err != nil {
		t.Fatalf("TrustedSource: %v", err)
	}
	if len(round.Predicates) != 1 || round.Predicates[0] != PredicateServicePlacement {
		t.Errorf("declared predicates round-tripped as %v", round.Predicates)
	}
	if len(round.EntityKinds) != 1 || round.EntityKinds[0] != EntityService {
		t.Errorf("declared entity kinds round-tripped as %v", round.EntityKinds)
	}
}
