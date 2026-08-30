package reality

import (
	"fmt"
	"sort"
	"time"
)

// sortPredicates orders predicates deterministically. Every derived key and
// every stored rule set depends on it, so ordering is never left to map
// iteration.
func sortPredicates(ps []Predicate) {
	sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
}

// Allowance is what a focus decision permits analysis to spend on an entity.
//
// The values read like the analysis-policy predicate's, and the type is
// deliberately different. §4.8's rule is that lifecycle — and, for the same
// reason, any other predicate — never silently implies an expenditure policy:
// the mapping is performed by an explicit versioned focus rule. Two distinct
// types make that unavoidable in code, because there is no assignment from a
// predicate value to an allowance, only an evaluated rule.
type Allowance string

// The allowances, ordered from most to least permissive by restriction().
const (
	// AllowanceFull permits everything the run's granted capabilities allow.
	AllowanceFull Allowance = "full"
	// AllowanceLearnOnly permits reading the corpus but not spending on
	// repository work: no cloning, no test execution, no repository-specific
	// proposals (§4.8's deferral list).
	AllowanceLearnOnly Allowance = "learn-only"
	// AllowanceNoCodeInvestigation permits synthesis over material already
	// held but no new code investigation at all.
	AllowanceNoCodeInvestigation Allowance = "no-code-investigation"
	// AllowanceExcluded permits nothing. The hypothesis is still kept:
	// §4.8 and §5.2 both forbid deleting it, so exclusion is a decision
	// recorded against it, not its removal.
	AllowanceExcluded Allowance = "excluded"
)

func (a Allowance) valid() bool {
	switch a {
	case AllowanceFull, AllowanceLearnOnly, AllowanceNoCodeInvestigation, AllowanceExcluded:
		return true
	}
	return false
}

// restriction ranks allowances so several entities' decisions can be combined.
func (a Allowance) restriction() int {
	switch a {
	case AllowanceFull:
		return 0
	case AllowanceLearnOnly:
		return 1
	case AllowanceNoCodeInvestigation:
		return 2
	case AllowanceExcluded:
		return 3
	}
	return 0
}

// MoreRestrictiveThan reports whether a permits strictly less than other. A
// hypothesis touching several entities takes the most restrictive of their
// decisions, because the alternative — the most permissive — would let one
// unrelated entity unlock work on an excluded one.
func (a Allowance) MoreRestrictiveThan(other Allowance) bool {
	return a.restriction() > other.restriction()
}

// FocusCondition is one requirement on the ledger: the entity's newest active
// fact for Predicate must equal Value.
//
// It is a slice element rather than a map entry so a rule set serializes and
// diffs in a stable order — a versioned policy whose bytes depend on map
// iteration is not really versioned.
type FocusCondition struct {
	Predicate Predicate `json:"predicate"`
	// Equals is the enum value the fact must carry. Only enum predicates
	// can be matched: a rule keyed on a path or an object entity would make
	// policy depend on values the operator cannot enumerate, and §4.8 wants
	// focus rules deterministic and reviewable.
	Equals string `json:"equals"`
}

// FocusRule maps a ledger state to an expenditure decision. Every condition
// must hold; an empty condition list matches everything, which is how a rule
// set states its own floor rather than leaving one implied.
type FocusRule struct {
	// Name identifies the rule in a decision's explanation, so an operator
	// reading a deferral learns which rule deferred it.
	Name string           `json:"name"`
	When []FocusCondition `json:"when,omitempty"`
	Then Allowance        `json:"then"`
	// Because is the rule's stated reason, rendered with the decision.
	Because string `json:"because"`
}

// FocusRuleSet is a versioned, deterministic policy. §4.8 requires the mapping
// from ledger state to expenditure to be explicit and versioned, and this is
// that artifact: rules are evaluated in order, first match wins, and the
// default applies when none matches.
//
// A version is immutable once stored. Re-deciding under new policy means
// storing a new version, which is what makes two decisions over one unchanged
// ledger comparable — the difference is the policy, and it is named.
type FocusRuleSet struct {
	Version int         `json:"version"`
	Rules   []FocusRule `json:"rules"`
	Default Allowance   `json:"default"`
	// Note explains the version's intent for a reviewer.
	Note string `json:"note,omitempty"`
	// CreatedAt is set by the store when the version is installed.
	CreatedAt time.Time `json:"-"`
}

func (rs FocusRuleSet) validate() error {
	if rs.Version <= 0 {
		return fmt.Errorf("%w: focus rule set version must be positive", ErrInvalidValue)
	}
	if !rs.Default.valid() {
		return fmt.Errorf("%w: focus rule set default allowance %q", ErrInvalidValue, rs.Default)
	}
	seen := make(map[string]struct{}, len(rs.Rules))
	for i, rule := range rs.Rules {
		if rule.Name == "" {
			return fmt.Errorf("%w: focus rule %d has no name", ErrInvalidValue, i)
		}
		if _, dup := seen[rule.Name]; dup {
			return fmt.Errorf("%w: focus rule name %q is used twice", ErrInvalidValue, rule.Name)
		}
		seen[rule.Name] = struct{}{}
		if !rule.Then.valid() {
			return fmt.Errorf("%w: focus rule %q allowance %q", ErrInvalidValue, rule.Name, rule.Then)
		}
		if rule.Because == "" {
			return fmt.Errorf("%w: focus rule %q states no reason", ErrInvalidValue, rule.Name)
		}
		for _, cond := range rule.When {
			spec, ok := predicateSpecs[cond.Predicate]
			if !ok {
				return fmt.Errorf("%w: focus rule %q matches unknown predicate %q",
					ErrInvalidValue, rule.Name, cond.Predicate)
			}
			if spec.kind != ValueEnum {
				return fmt.Errorf("%w: focus rule %q matches non-enum predicate %q",
					ErrInvalidValue, rule.Name, cond.Predicate)
			}
			allowed := false
			for _, value := range spec.values {
				if value == cond.Equals {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("%w: focus rule %q matches predicate %s against %q, outside its vocabulary",
					ErrInvalidValue, rule.Name, cond.Predicate, cond.Equals)
			}
		}
	}
	return nil
}

// DefaultFocusRules is the rule set version 1 this build ships.
//
// Read what it does not do. It contains no rule that maps a lifecycle value to
// an allowance, because §4.8's example of the failure mode is exactly that: a
// dormant project is not by itself a project analysis may not spend on, and an
// operator who wants that mapping installs a version that says so. What it
// does map is the analysis-policy predicate, which exists to carry operator
// intent about expenditure — and even that is a rule here rather than an
// implication in the fact model, so a later version can decide differently
// about the same ledger.
func DefaultFocusRules() FocusRuleSet {
	return FocusRuleSet{
		Version: 1,
		Default: AllowanceFull,
		Note:    "maps stated analysis policy only; lifecycle and ownership carry no expenditure meaning in this version",
		Rules: []FocusRule{
			{
				Name:    "policy-excluded",
				When:    []FocusCondition{{Predicate: PredicateAnalysisPolicy, Equals: PolicyExcluded}},
				Then:    AllowanceExcluded,
				Because: "the operator stated an analysis policy of excluded for this entity",
			},
			{
				Name:    "policy-no-code-investigation",
				When:    []FocusCondition{{Predicate: PredicateAnalysisPolicy, Equals: PolicyNoCodeInvestigation}},
				Then:    AllowanceNoCodeInvestigation,
				Because: "the operator stated an analysis policy of no-code-investigation for this entity",
			},
			{
				Name:    "policy-learn-only",
				When:    []FocusCondition{{Predicate: PredicateAnalysisPolicy, Equals: PolicyLearnOnly}},
				Then:    AllowanceLearnOnly,
				Because: "the operator stated an analysis policy of learn-only for this entity",
			},
		},
	}
}

// FocusInput records one fact a decision read, so the decision explains itself
// without a second query.
type FocusInput struct {
	FactID    string
	Predicate Predicate
	Value     string
	Status    FactStatus
	// ExpiresAt is the fact's freshness horizon, zero when it does not
	// expire.
	ExpiresAt time.Time
}

// FocusDecision is one evaluation's result.
//
// It is not stored as an entity's property. §4.8 has analysis query the ledger
// by entity, predicate, valid time, freshness, and conflict, so a decision is
// computed against a named policy version at a named instant and either used
// immediately or frozen into a context snapshot. A decision cached as an
// entity column would be a fourth thing that can disagree with the ledger.
type FocusDecision struct {
	EntityID string
	// RuleSetVersion and RuleName say which policy decided, which is what
	// makes two different answers over one unchanged ledger explainable.
	RuleSetVersion int
	RuleName       string
	Allowance      Allowance
	Because        string
	AsOf           time.Time
	// Inputs are the facts the evaluation read, in predicate order.
	Inputs []FocusInput
	// Contested reports that a fact behind the matched rule is stale or
	// disputed. §4.8 has the challenger check exactly this, so the
	// evaluation surfaces it rather than deciding silently on shaky input.
	Contested bool
	// ContestedFactIDs names the stale or disputed inputs that made it so.
	ContestedFactIDs []string
}

// evaluateFocus applies a rule set to one entity's facts.
//
// Determinism is the requirement, so this is a pure function over a decoded
// fact list: the caller reads the ledger as of an instant, this decides, and
// nothing in between consults a clock, a map iteration order, or a model.
//
// Facts are grouped by predicate and the newest one wins within a predicate,
// ranked by observed time and then by recorded time and ID so two facts
// observed at the same instant still order deterministically.
//
// A decision is contested when any fact it depends on is stale or disputed.
// "Depends on" is per predicate rather than per matched condition, and that
// breadth is deliberate: a disputed lifecycle fact changes which rule matches,
// so a decision that fell through to the default *because* of a dispute is
// exactly the case §4.8 gives the challenger to check, and looking only at the
// matched rule's own conditions would miss it. A stale fact on a predicate no
// rule in this version mentions cannot move the decision, so it does not
// contest it.
func evaluateFocus(entityID string, rules FocusRuleSet, facts []Fact, asOf time.Time) FocusDecision {
	current := make(map[Predicate]Fact, len(facts))
	for _, fact := range facts {
		if fact.Status == FactProposed || fact.Status == FactSuperseded {
			// A proposal is not reality and a superseded revision is
			// no longer it. Neither may move a decision.
			continue
		}
		if !overlaps(fact.ValidFrom, fact.ValidUntil, asOf, asOf.Add(time.Nanosecond)) {
			continue
		}
		best, seen := current[fact.Predicate]
		if !seen || newer(fact, best) {
			current[fact.Predicate] = fact
		}
	}

	predicates := make([]Predicate, 0, len(current))
	for p := range current {
		predicates = append(predicates, p)
	}
	sortPredicates(predicates)

	decision := FocusDecision{
		EntityID:       entityID,
		RuleSetVersion: rules.Version,
		Allowance:      rules.Default,
		Because:        "no rule in this version matched the ledger",
		AsOf:           asOf.UTC(),
	}
	for _, p := range predicates {
		fact := current[p]
		decision.Inputs = append(decision.Inputs, FocusInput{
			FactID:    fact.ID,
			Predicate: p,
			Value:     factMatchValue(fact),
			Status:    fact.Status,
			ExpiresAt: fact.ExpiresAt,
		})
	}

	// The predicates this version's rules actually consult. Shakiness
	// anywhere else is real but cannot have affected this decision.
	consulted := make(map[Predicate]struct{})
	for _, rule := range rules.Rules {
		for _, cond := range rule.When {
			consulted[cond.Predicate] = struct{}{}
		}
	}
	for _, p := range predicates {
		fact := current[p]
		if _, used := consulted[p]; !used {
			continue
		}
		if fact.Status == FactStale || fact.Status == FactDisputed {
			decision.Contested = true
			decision.ContestedFactIDs = append(decision.ContestedFactIDs, fact.ID)
		}
	}

	for _, rule := range rules.Rules {
		matched := true
		for _, cond := range rule.When {
			fact, ok := current[cond.Predicate]
			if !ok || factMatchValue(fact) != cond.Equals {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		decision.RuleName = rule.Name
		decision.Allowance = rule.Then
		decision.Because = rule.Because
		return decision
	}
	return decision
}

// newer ranks two facts for "which one is current". Observed time first
// because that is when reality was as the fact says; recorded time and ID
// break ties so the answer does not depend on scan order.
func newer(candidate, incumbent Fact) bool {
	if !candidate.ObservedAt.Equal(incumbent.ObservedAt) {
		return candidate.ObservedAt.After(incumbent.ObservedAt)
	}
	if !candidate.RecordedAt.Equal(incumbent.RecordedAt) {
		return candidate.RecordedAt.After(incumbent.RecordedAt)
	}
	return candidate.ID > incumbent.ID
}

// factMatchValue renders the part of a value a rule can match on. Only enum
// predicates are matchable, so anything else renders empty and no condition
// can accidentally match it.
func factMatchValue(f Fact) string {
	if f.Value.Kind == ValueEnum {
		return f.Value.Enum
	}
	return ""
}

// FocusQuery evaluates focus for one entity under one stored policy version.
type FocusQuery struct {
	EntityID string
	// RuleSetVersion names the stored policy. It is required: evaluating
	// against "the latest" would make a decision unreproducible the moment
	// a new version is installed.
	RuleSetVersion int
	// AsOf is the instant the ledger is read at, defaulting to now.
	AsOf time.Time
}
