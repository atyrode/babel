package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// planFocus installs the shipped focus rule set, names a project by the
// workspace path its sessions record, and states an analysis policy for it.
//
// The alias is a path alias because that is what a session actually offers:
// the harness writes the working directory it ran in, and §4.8's typed
// aliases are the operator's own record that this path is that project.
func plantFocus(t *testing.T, dataDir, workspace, policy string) string {
	t.Helper()
	ctx := context.Background()
	store, err := reality.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.PutFocusRules(ctx, reality.DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}
	entity, err := store.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityProject,
		Payload: reality.EntityPayload{DisplayName: "a project nobody maintains"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(ctx, reality.AliasInput{
		EntityID: entity.ID,
		Kind:     reality.AliasPath,
		Payload:  reality.AliasPayload{Value: workspace},
	}); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Add(-time.Hour)
	if _, _, err := store.AssertFact(ctx, reality.FactInput{
		SubjectID:   entity.ID,
		Predicate:   reality.PredicateAnalysisPolicy,
		Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: policy},
		ValidFrom:   observed,
		ObservedAt:  observed,
		Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "seed-operator", At: observed},
		Confidence:  reality.ConfidenceHigh,
		Sensitivity: reality.SensitivityRoutine,
		Note:        "the operator stopped maintaining this project",
	}); err != nil {
		t.Fatal(err)
	}
	return entity.ID
}

// Corpus eligibility is where learn-only earns its name.
//
// The value exists so a subject can go on contributing cross-cutting lessons
// while nothing is spent on the subject itself, and that is only true if its
// sessions stay in the scope a run reads. The two stricter values withhold
// them, because §4.8 gives no-code-investigation only synthesis over material
// already held and gives excluded nothing at all — and a scope is new
// reading either way.
func TestPrepareConsultsFocusForCorpusEligibility(t *testing.T) {
	cases := []struct {
		policy   string
		prepared int
		withheld int
	}{
		{reality.PolicyNormal, 2, 0},
		{reality.PolicyLearnOnly, 2, 0},
		{reality.PolicyNoCodeInvestigation, 1, 1},
		{reality.PolicyExcluded, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.policy, func(t *testing.T) {
			f := newFixture(t)
			f.writeSession(sessionSpec{
				project: "-alpha", stem: "2026-01-02T03-04-05-000Z_" + testUUID(1),
				id: testUUID(1), title: "alpha", workspace: "/synthetic/alpha",
			})
			f.writeSession(sessionSpec{
				project: "-beta", stem: "2026-01-02T04-04-05-000Z_" + testUUID(2),
				id: testUUID(2), title: "beta", workspace: "/synthetic/beta",
			})
			plantFocus(t, f.dataDir, "/synthetic/alpha", tc.policy)

			stdout, _ := f.ok("prepare", "--json")
			res := decodeJSON[prepareResult](t, stdout)
			if len(res.Sessions) != tc.prepared {
				t.Errorf("%s prepared %d sessions, want %d", tc.policy, len(res.Sessions), tc.prepared)
			}
			if len(res.Withheld) != tc.withheld {
				t.Fatalf("%s withheld %+v, want %d", tc.policy, res.Withheld, tc.withheld)
			}
			if tc.withheld == 0 {
				return
			}
			row := res.Withheld[0]
			if !strings.Contains(row.Reason, "corpus-reading work is withheld") {
				t.Errorf("the withheld row reads %q, want it to name the work", row.Reason)
			}
			if !strings.Contains(row.Reason, `rule "policy-`+tc.policy+`"`) {
				t.Errorf("the withheld row reads %q, want it to name the rule", row.Reason)
			}
			for _, prepared := range res.Sessions {
				if prepared.Selector == row.Selector {
					t.Errorf("%s was both prepared and withheld", row.Selector)
				}
			}
		})
	}
}

// Withholding a session is not deleting it. The file is untouched, the
// catalog still knows it, and superseding the fact brings it back into the
// next scope with nothing to restore.
func TestAWithheldSessionReturnsWhenThePolicyIsLifted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.writeSession(sessionSpec{
		project: "-alpha", stem: "2026-01-02T03-04-05-000Z_" + testUUID(1),
		id: testUUID(1), title: "alpha", workspace: "/synthetic/alpha",
	})
	f.writeSession(sessionSpec{
		project: "-beta", stem: "2026-01-02T04-04-05-000Z_" + testUUID(2),
		id: testUUID(2), title: "beta", workspace: "/synthetic/beta",
	})
	subject := plantFocus(t, f.dataDir, "/synthetic/alpha", reality.PolicyExcluded)

	stdout, _ := f.ok("prepare", "--json")
	before := decodeJSON[prepareResult](t, stdout)
	if len(before.Withheld) != 1 {
		t.Fatalf("withheld %+v, want the excluded project's session", before.Withheld)
	}

	store, err := reality.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := store.Facts(ctx, reality.FactQuery{
		SubjectID: subject,
		Predicate: reality.PredicateAnalysisPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("the ledger holds %d analysis-policy facts, want one to supersede", len(facts))
	}
	// The replacement has to be in force at the instant the next
	// preparation reads the ledger. A supersession stamped into the future
	// would leave no applicable fact, and the session would come back for
	// the wrong reason — because the ledger says nothing, not because it
	// says the project is live again.
	later := time.Now().UTC().Add(-time.Minute)
	if _, err := store.SupersedeFact(ctx, reality.SupersedeInput{
		PriorID: facts[0].ID,
		Fact: reality.FactInput{
			SubjectID:   subject,
			Predicate:   reality.PredicateAnalysisPolicy,
			Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.PolicyNormal},
			ValidFrom:   later,
			ObservedAt:  later,
			Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "seed-operator", At: later},
			Confidence:  reality.ConfidenceHigh,
			Sensitivity: reality.SensitivityRoutine,
			Note:        "picked the project back up",
		},
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	stdout, _ = f.ok("prepare", "--json")
	after := decodeJSON[prepareResult](t, stdout)
	if len(after.Withheld) != 0 {
		t.Errorf("the lifted policy still withholds %+v", after.Withheld)
	}
	if len(after.Sessions) != 2 {
		t.Errorf("prepared %d sessions after the policy was lifted, want both", len(after.Sessions))
	}
}
