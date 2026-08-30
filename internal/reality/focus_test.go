package reality

import (
	"context"
	"testing"
	"time"
)

// dormantLifecycleRules is a policy version that does map lifecycle to an
// expenditure decision. It exists to be compared against version 1, which does
// not, over one unchanged ledger.
func dormantLifecycleRules(version int) FocusRuleSet {
	return FocusRuleSet{
		Version: version,
		Default: AllowanceFull,
		Note:    "this version chooses to treat dormancy as a reason to spend less",
		Rules: []FocusRule{
			{
				Name:    "dormant-is-learn-only",
				When:    []FocusCondition{{Predicate: PredicateLifecycle, Equals: LifecycleDormant}},
				Then:    AllowanceLearnOnly,
				Because: "this policy version reads dormancy as a reason not to spend on repository work",
			},
			{
				Name:    "retired-is-excluded",
				When:    []FocusCondition{{Predicate: PredicateLifecycle, Equals: LifecycleRetired}},
				Then:    AllowanceExcluded,
				Because: "this policy version reads retirement as a reason to stop entirely",
			},
		},
	}
}

// TestLifecycleAloneNeverChangesAnExpenditureDecision is §4.8's most precise
// requirement: lifecycle never silently implies an expenditure policy, and
// explicit versioned focus rules perform that mapping.
//
// The test proves both directions over one ledger that is never rewritten
// between them. Under version 1, which ships no lifecycle rule, changing the
// lifecycle from active to dormant to retired leaves the decision exactly where
// it was — the fact changed and the expenditure did not. Under version 2, which
// maps lifecycle explicitly, the same facts yield a different decision. The
// difference between the two answers is the policy, and the decision names
// which rule produced it.
func TestLifecycleAloneNeverChangesAnExpenditureDecision(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	if _, err := store.PutFocusRules(ctx, DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules(1): %v", err)
	}
	if _, err := store.PutFocusRules(ctx, dormantLifecycleRules(2)); err != nil {
		t.Fatalf("PutFocusRules(2): %v", err)
	}

	decide := func(t *testing.T, version int) FocusDecision {
		t.Helper()
		decision, err := store.EvaluateFocus(ctx, FocusQuery{
			EntityID:       project.ID,
			RuleSetVersion: version,
			AsOf:           clock.now(),
		})
		if err != nil {
			t.Fatalf("EvaluateFocus(v%d): %v", version, err)
		}
		return decision
	}

	// No lifecycle fact at all: both versions fall through to their default.
	if got := decide(t, 1).Allowance; got != AllowanceFull {
		t.Fatalf("empty ledger under v1 gives %s, want %s", got, AllowanceFull)
	}

	current, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact(active): %v", err)
	}

	baseline := decide(t, 1)
	if baseline.Allowance != AllowanceFull || baseline.RuleName != "" {
		t.Fatalf("an active lifecycle under v1 gives %s via %q, want %s via no rule",
			baseline.Allowance, baseline.RuleName, AllowanceFull)
	}

	// Walk the lifecycle through every remaining value. Under version 1 the
	// decision must not move, because no rule in version 1 mentions
	// lifecycle.
	for _, value := range []string{LifecycleMaintenanceOnly, LifecycleDormant, LifecycleRetired} {
		revision, err := store.SupersedeFact(ctx, SupersedeInput{
			PriorID: current.ID,
			Fact:    operatorFact(project.ID, PredicateLifecycle, enum(value), clock.now()),
		})
		if err != nil {
			t.Fatalf("SupersedeFact(%s): %v", value, err)
		}
		current = revision

		under1 := decide(t, 1)
		if under1.Allowance != baseline.Allowance {
			t.Errorf("lifecycle %s changed the v1 decision to %s; only a rule may do that",
				value, under1.Allowance)
		}
		if under1.RuleName != "" {
			t.Errorf("lifecycle %s matched v1 rule %q, which does not exist", value, under1.RuleName)
		}
		// The decision must still have read the fact — it is not ignoring
		// the ledger, it is declining to infer from it.
		if len(under1.Inputs) != 1 || under1.Inputs[0].Value != value {
			t.Errorf("the v1 decision read %+v, want the lifecycle fact with value %q",
				under1.Inputs, value)
		}
	}

	// Same ledger, same instant, different policy version: a different
	// decision, named.
	asOf := clock.now()
	under1, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 1, AsOf: asOf,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v1): %v", err)
	}
	under2, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 2, AsOf: asOf,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v2): %v", err)
	}
	if under1.Allowance != AllowanceFull {
		t.Errorf("v1 gives %s over a retired project, want %s", under1.Allowance, AllowanceFull)
	}
	if under2.Allowance != AllowanceExcluded {
		t.Errorf("v2 gives %s over a retired project, want %s", under2.Allowance, AllowanceExcluded)
	}
	if under1.Allowance == under2.Allowance {
		t.Fatal("two policy versions produced one decision over one lifecycle")
	}
	if under2.RuleName != "retired-is-excluded" || under2.Because == "" {
		t.Errorf("v2's decision does not name the rule that made it: %+v", under2)
	}
	if under1.RuleSetVersion != 1 || under2.RuleSetVersion != 2 {
		t.Errorf("decisions do not record their policy version: %d, %d",
			under1.RuleSetVersion, under2.RuleSetVersion)
	}
}

// TestEvenAnalysisPolicyNeedsARule takes the separation to its limit. The
// analysis-policy predicate exists to carry operator intent about expenditure,
// and it still does not decide anything: a version with no rules maps a stated
// policy of `excluded` to its default, and only version 1's explicit rule turns
// it into an exclusion.
func TestEvenAnalysisPolicyNeedsARule(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")

	if _, err := store.PutFocusRules(ctx, DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules(1): %v", err)
	}
	if _, err := store.PutFocusRules(ctx, FocusRuleSet{
		Version: 7,
		Default: AllowanceFull,
		Note:    "a version that maps nothing at all",
	}); err != nil {
		t.Fatalf("PutFocusRules(7): %v", err)
	}

	if _, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateAnalysisPolicy,
		enum(PolicyExcluded), clock.now())); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	mapped, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 1})
	if err != nil {
		t.Fatalf("EvaluateFocus(v1): %v", err)
	}
	if mapped.Allowance != AllowanceExcluded || mapped.RuleName != "policy-excluded" {
		t.Errorf("v1 gives %s via %q, want %s via policy-excluded",
			mapped.Allowance, mapped.RuleName, AllowanceExcluded)
	}

	unmapped, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 7})
	if err != nil {
		t.Fatalf("EvaluateFocus(v7): %v", err)
	}
	if unmapped.Allowance != AllowanceFull {
		t.Errorf("a version with no rules gives %s, want its default %s",
			unmapped.Allowance, AllowanceFull)
	}
}

// TestFocusDecisionFlagsStaleAndDisputedInputs covers §4.8's challenger check:
// a focus decision resting on a stale or disputed fact is marked rather than
// taken quietly, and superseded revisions and proposals never move it at all.
func TestFocusDecisionFlagsStaleAndDisputedInputs(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")
	if _, err := store.PutFocusRules(ctx, dormantLifecycleRules(1)); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}

	// A proposal cannot move a decision.
	proposal := operatorFact(project.ID, PredicateLifecycle, enum(LifecycleDormant), clock.now())
	proposal.Authority = Authority{Kind: AuthorityObservation, ID: "git-activity", At: clock.now()}
	proposal.Provenance = syntheticLocator(4)
	if _, _, err := store.AssertFact(ctx, proposal); err != nil {
		t.Fatalf("AssertFact(proposal): %v", err)
	}
	quiet, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 1})
	if err != nil {
		t.Fatalf("EvaluateFocus: %v", err)
	}
	if quiet.Allowance != AllowanceFull || len(quiet.Inputs) != 0 {
		t.Errorf("a proposal moved the decision to %s reading %+v", quiet.Allowance, quiet.Inputs)
	}

	// An authorized dormant fact does, and once it is disputed the decision
	// is contested rather than silently unchanged.
	dormant, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleDormant), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact(dormant): %v", err)
	}
	decided, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 1})
	if err != nil {
		t.Fatalf("EvaluateFocus: %v", err)
	}
	if decided.Allowance != AllowanceLearnOnly || decided.Contested {
		t.Errorf("decision is %s contested=%v, want learn-only and uncontested",
			decided.Allowance, decided.Contested)
	}
	if len(decided.Inputs) != 1 || decided.Inputs[0].FactID != dormant.ID {
		t.Errorf("the decision read %+v, want the authorized dormant fact", decided.Inputs)
	}

	contradiction, dispute, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact(contradiction): %v", err)
	}
	if dispute.ID == "" {
		t.Fatal("the contradiction produced no dispute")
	}
	contested, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 1})
	if err != nil {
		t.Fatalf("EvaluateFocus: %v", err)
	}
	if !contested.Contested {
		t.Error("a decision resting on disputed facts is not marked contested")
	}
	// The newest fact for the predicate is the input the rule would read, so
	// it is the one named.
	if len(contested.ContestedFactIDs) != 1 || contested.ContestedFactIDs[0] != contradiction.ID {
		t.Errorf("the contested decision names %v, want the newest disputed fact %q",
			contested.ContestedFactIDs, contradiction.ID)
	}
	if len(contested.Inputs) != 1 || contested.Inputs[0].Status != FactDisputed {
		t.Errorf("the decision read %+v, want one disputed input", contested.Inputs)
	}
	// The rule stops matching, so the decision falls through to the default —
	// and it says so rather than presenting the fall-through as a clean
	// answer, which is the case §4.8 gives the challenger to check.
	if contested.Allowance != AllowanceFull || contested.RuleName != "" {
		t.Errorf("the contested decision is %s via %q, want the default via no rule",
			contested.Allowance, contested.RuleName)
	}

	// Staleness contests a decision the same way. A version that consults a
	// predicate with a TTL sees its fact go stale, and the decision that
	// still rests on it is marked.
	if _, err := store.PutFocusRules(ctx, FocusRuleSet{
		Version: 2,
		Default: AllowanceFull,
		Note:    "consults a volatile predicate on purpose",
		Rules: []FocusRule{{
			Name:    "undeployed-is-learn-only",
			When:    []FocusCondition{{Predicate: PredicateDeploymentState, Equals: DeploymentUndeployed}},
			Then:    AllowanceLearnOnly,
			Because: "there is nothing deployed to investigate against",
		}},
	}); err != nil {
		t.Fatalf("PutFocusRules(2): %v", err)
	}
	observed := clock.now()
	if _, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateDeploymentState,
		enum(DeploymentUndeployed), observed)); err != nil {
		t.Fatalf("AssertFact(deployment): %v", err)
	}
	fresh, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 2, AsOf: observed,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v2 fresh): %v", err)
	}
	if fresh.Allowance != AllowanceLearnOnly || fresh.Contested {
		t.Errorf("a fresh decision is %s contested=%v", fresh.Allowance, fresh.Contested)
	}
	asOf := observed.Add(30 * 24 * time.Hour)
	if _, err := store.ExpireStale(ctx, asOf); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	staleDecision, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 2, AsOf: asOf,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v2 stale): %v", err)
	}
	// A stale fact still decides — dropping it would silently change the
	// answer — and the decision is marked so the challenger can see what it
	// rests on.
	if staleDecision.Allowance != AllowanceLearnOnly {
		t.Errorf("a stale input changed the decision to %s", staleDecision.Allowance)
	}
	if !staleDecision.Contested {
		t.Error("a decision resting on a stale fact is not marked contested")
	}
}

// TestFocusRuleSetVersionsAreImmutable checks the property that makes a past
// decision explainable: a version's bytes cannot change, so re-deriving a
// decision from version 1 gives version 1's answer.
func TestFocusRuleSetVersionsAreImmutable(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if _, err := store.PutFocusRules(ctx, DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}
	if _, err := store.PutFocusRules(ctx, dormantLifecycleRules(1)); !isErr(err, ErrConflict) {
		t.Errorf("reinstalling version 1 returned %v, want ErrConflict", err)
	}
	if _, err := store.db.Exec(`UPDATE reality_focus_ruleset SET payload_json = '{}' WHERE version = 1`); err == nil {
		t.Error("a focus rule set version accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_focus_ruleset WHERE version = 1`); err == nil {
		t.Error("a focus rule set version accepted a delete")
	}

	stored, err := store.FocusRules(ctx, 1)
	if err != nil {
		t.Fatalf("FocusRules: %v", err)
	}
	want := DefaultFocusRules()
	if len(stored.Rules) != len(want.Rules) || stored.Default != want.Default {
		t.Errorf("stored version 1 is %+v, want %+v", stored, want)
	}
	if stored.CreatedAt.IsZero() {
		t.Error("stored version 1 has no installation time")
	}
	if _, err := store.FocusRules(ctx, 99); !isErr(err, ErrUnknownRecord) {
		t.Errorf("an uninstalled version returned %v, want ErrUnknownRecord", err)
	}
	if _, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: "ent_x", RuleSetVersion: 99}); err == nil {
		t.Error("evaluating against an uninstalled version was accepted")
	}
}

// TestFocusRuleValidationRefusesUndeterminableRules keeps the policy language
// deterministic and reviewable: a rule must name itself, state a reason, and
// match a closed value vocabulary, or a decision could not be explained.
func TestFocusRuleValidationRefusesUndeterminableRules(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	cases := []struct {
		name  string
		rules FocusRuleSet
	}{
		{"no version", FocusRuleSet{Default: AllowanceFull}},
		{"no default", FocusRuleSet{Version: 1}},
		{"unnamed rule", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{{Then: AllowanceExcluded, Because: "because"}}}},
		{"no stated reason", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{{Name: "r", Then: AllowanceExcluded}}}},
		{"duplicate rule name", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{
				{Name: "r", Then: AllowanceExcluded, Because: "because"},
				{Name: "r", Then: AllowanceFull, Because: "because"},
			}}},
		{"matching a non-enum predicate", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{{Name: "r", Then: AllowanceExcluded, Because: "because",
				When: []FocusCondition{{Predicate: PredicateLocalPath, Equals: "/somewhere"}}}}}},
		{"matching an unknown predicate", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{{Name: "r", Then: AllowanceExcluded, Because: "because",
				When: []FocusCondition{{Predicate: "invented", Equals: "x"}}}}}},
		{"matching outside the vocabulary", FocusRuleSet{Version: 1, Default: AllowanceFull,
			Rules: []FocusRule{{Name: "r", Then: AllowanceExcluded, Because: "because",
				When: []FocusCondition{{Predicate: PredicateLifecycle, Equals: "mothballed"}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.PutFocusRules(ctx, tc.rules); !isErr(err, ErrInvalidValue) {
				t.Errorf("got %v, want ErrInvalidValue", err)
			}
		})
	}
}

// TestAllowanceCombinesToTheMostRestrictive pins the combination rule a
// snapshot over several entities relies on: the most permissive would let one
// unrelated entity unlock work on an excluded one.
func TestAllowanceCombinesToTheMostRestrictive(t *testing.T) {
	ordered := []Allowance{
		AllowanceFull, AllowanceLearnOnly, AllowanceNoCodeInvestigation, AllowanceExcluded,
	}
	for i, less := range ordered {
		for j, more := range ordered {
			want := j > i
			if got := more.MoreRestrictiveThan(less); got != want {
				t.Errorf("%s.MoreRestrictiveThan(%s) = %v, want %v", more, less, got, want)
			}
		}
	}
}
