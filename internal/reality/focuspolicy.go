package reality

// The operator's expenditure-intent surface: install the versioned mapping
// §4.8 requires, state a policy for a subject, and reverse it.
//
// It exists as a type of its own because of what the ledger's own writers can
// do. AssertFact will assert any predicate, SupersedeFact will replace any
// fact, and PutFocusRules will install any rule set — and the surface that
// reaches this from a browser (internal/web/focus.go) must be able to do
// exactly one of those things and be unable to do the rest. A handler that
// held *Store and promised to only ever pass PredicateAnalysisPolicy would be
// a promise in a comment; a handler that holds *FocusPolicy cannot assert a
// lifecycle fact, cannot merge two entities, and cannot install a rule set
// somebody wrote in a request body, because there is no method for it.
//
// Three rules are enforced here rather than by the caller.
//
// The predicate is fixed. Only analysis-policy carries operator intent about
// expenditure (§4.8), and it is the one predicate this type will write.
//
// The authority is the operator's and is never optional. Assert takes an
// operator identity and attributes the fact to it as an attributed operator
// action, which is the same authority `babel`'s own operator-facing writes
// carry — no more, and never a synthesized one: an empty identity is refused
// by FactInput's own validation rather than defaulted to a name nobody
// answers for.
//
// Nothing is deleted and nothing is un-marked. Changing intent is a
// superseding revision whose ancestor stays byte-identical, so "the operator
// changed his mind" is a readable pair of facts rather than an edit.

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// FocusPolicy is the ledger's focus surface. It holds the store rather than
// embedding it, so its method set is exactly the methods below.
type FocusPolicy struct {
	store *Store
}

// Focus returns the store's focus surface. It is a view, not a second store:
// every write below is the store's own, inside the store's own transaction,
// with the store's own clock.
func (s *Store) Focus() *FocusPolicy { return &FocusPolicy{store: s} }

// Rules reads one installed policy version. A version that was never
// installed is ErrUnknownRecord, which is a state rather than a fault: until
// something installs a version, §4.8's mapping has no artifact and nothing is
// withheld.
func (p *FocusPolicy) Rules(ctx context.Context, version int) (FocusRuleSet, error) {
	return p.store.FocusRules(ctx, version)
}

// Shipped is the rule set version this build's consumers evaluate against. A
// caller reading Rules needs a version to name, and guessing "the newest
// installed" would make a decision unreproducible the moment another is
// installed.
func (p *FocusPolicy) Shipped() FocusRuleSet { return DefaultFocusRules() }

// Install stores the rule set version this build ships.
//
// It takes no rules. A browser that could post a rule set would be authoring
// policy in a request body, and the argument §4.8 makes for versioning — that
// a past decision is explainable from the version's own bytes — survives only
// if those bytes came from something reviewable. Installing an already
// installed version is ErrConflict, because a version is immutable once
// stored.
func (p *FocusPolicy) Install(ctx context.Context) (FocusRuleSet, error) {
	return p.store.PutFocusRules(ctx, DefaultFocusRules())
}

// Decide evaluates focus for one entity under one stored version. It is
// EvaluateFocus: the mapping is performed by the ledger, so a surface showing
// an allowance is showing the same arithmetic a run's deferral came from.
func (p *FocusPolicy) Decide(ctx context.Context, q FocusQuery) (FocusDecision, error) {
	return p.store.EvaluateFocus(ctx, q)
}

// Resolve names the canonical entity an untyped term refers to, through the
// ledger's aliases. It is ResolveSubject, so a name that means two entities is
// ambiguous here exactly as it is for a run.
func (p *FocusPolicy) Resolve(ctx context.Context, value string) (string, error) {
	return p.store.ResolveSubject(ctx, value)
}

// Entity reads one entity's identity, for the display name a rule is listed
// under.
func (p *FocusPolicy) Entity(ctx context.Context, id string) (Entity, error) {
	return p.store.Entity(ctx, id)
}

// Aliases reads the names an entity is known by, which is what lets a listing
// say "this is the thing you called dev-01".
func (p *FocusPolicy) Aliases(ctx context.Context, entityID string) ([]Alias, error) {
	return p.store.Aliases(ctx, entityID)
}

// Subjects names every entity the ledger holds an analysis-policy fact about,
// canonicalized and deduplicated, in a stable order.
//
// Merged identities collapse: a policy asserted about a name that has since
// been folded into another entity is that entity's policy, which is what
// Facts already does per subject and what a decision about it reads.
func (p *FocusPolicy) Subjects(ctx context.Context) ([]string, error) {
	ids, err := queryStrings(ctx, p.store.db,
		`SELECT DISTINCT subject_id FROM reality_fact WHERE predicate = ? ORDER BY subject_id`,
		string(PredicateAnalysisPolicy))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		canonical, err := resolve(ctx, p.store.db, id)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, canonical) {
			out = append(out, canonical)
		}
	}
	slices.Sort(out)
	return out, nil
}

// InForce is the analysis-policy fact a decision about this subject would
// read: the newest one actually in force at asOf, or absent.
//
// It shares currentFacts with evaluateFocus rather than ranking revisions
// again, so the fact a surface names as the reason for an allowance is the
// fact that produced it. A zero asOf means the store's own now.
func (p *FocusPolicy) InForce(ctx context.Context, subjectID string, asOf time.Time) (Fact, bool, error) {
	if subjectID == "" {
		return Fact{}, false, fmt.Errorf("%w: focus subject is empty", ErrInvalidValue)
	}
	facts, err := p.store.Facts(ctx, FactQuery{SubjectID: subjectID, Predicate: PredicateAnalysisPolicy})
	if err != nil {
		return Fact{}, false, err
	}
	current, ok := currentFacts(facts, p.store.asOfOr(asOf))[PredicateAnalysisPolicy]
	return current, ok, nil
}

// History is every analysis-policy revision the ledger holds about one
// subject, oldest first, superseded revisions included.
//
// The superseded ones are the point. Reversing a policy is a superseding
// revision, so a listing that showed only the fact in force could not show
// that the operator had changed his mind, which is the one thing an
// append-only ledger keeps that an editable field would not.
func (p *FocusPolicy) History(ctx context.Context, subjectID string) ([]Fact, error) {
	return p.store.Facts(ctx, FactQuery{SubjectID: subjectID, Predicate: PredicateAnalysisPolicy})
}

// Fact reads one revision. It is how a caller holding an identifier learns
// which subject the fact is about and whether it is a policy at all, before
// asking to replace it.
func (p *FocusPolicy) Fact(ctx context.Context, id string) (Fact, error) {
	return p.store.Fact(ctx, id)
}

// FocusPolicyInput is one operator statement of intent about a subject.
type FocusPolicyInput struct {
	// SubjectID is the canonical entity. Resolve turns a name into one.
	SubjectID string
	// Policy is an analysis-policy value. It is not checked here: the
	// predicate's own vocabulary check refuses an unknown one by name, so
	// there is one place that decides what a policy may be.
	Policy string
	// By is the operator identity the fact is attributed to. Empty is
	// refused: a decision recorded against nobody is worse than one not
	// recorded.
	By string
	// Note is the operator's stated reason, kept in the fact's payload.
	Note string
	// At is the instant the intent is stated, defaulting to the store's
	// clock. It is both the observation time and the start of valid time:
	// an operator stating a policy is observing his own intent, now.
	At time.Time
}

func (in FocusPolicyInput) fact(at time.Time) FactInput {
	return FactInput{
		SubjectID: in.SubjectID,
		Predicate: PredicateAnalysisPolicy,
		Value:     FactValue{Kind: ValueEnum, Enum: in.Policy},
		ValidFrom: at,
		// Open-ended on purpose. Operator intent holds until it is
		// superseded, and the predicate carries no TTL for the same
		// reason: an expiring analysis policy would silently widen what
		// analysis may spend.
		ObservedAt:  at,
		Authority:   Authority{Kind: AuthorityOperator, ID: in.By, At: at},
		Confidence:  ConfidenceHigh,
		Sensitivity: SensitivityRoutine,
		Note:        in.Note,
	}
}

// Assert records an operator's analysis policy for a subject as an
// authoritative fact.
//
// The dispute the underlying assertion may open is returned rather than
// swallowed: two contradicting active policies for one subject is §4.8's
// dispute, not a silent winner, and a caller that discarded it would leave the
// operator looking at a page that cannot explain why the decision did not
// change. Changing an existing policy is Supersede, which cannot contradict
// anything because it replaces.
func (p *FocusPolicy) Assert(ctx context.Context, in FocusPolicyInput) (Fact, Dispute, error) {
	return p.store.AssertFact(ctx, in.fact(p.store.asOfOr(in.At)))
}

// FocusPolicyRevision replaces one policy fact with another.
type FocusPolicyRevision struct {
	// PriorID is the revision being replaced. It stays byte-identical and
	// gains a superseded status event.
	PriorID string
	FocusPolicyInput
}

// Supersede replaces a subject's policy with a later revision.
//
// This is how an operator reverses himself, and it is the only way the content
// of a policy changes: the prior revision is untouched, the chain cannot fork,
// and both revisions stay readable in order. Lifting a restriction is a
// revision whose value is `normal` rather than a deletion, because there is no
// deletion.
func (p *FocusPolicy) Supersede(ctx context.Context, in FocusPolicyRevision) (Fact, error) {
	if in.PriorID == "" {
		return Fact{}, fmt.Errorf("%w: a focus policy revision names no prior fact", ErrInvalidValue)
	}
	prior, err := p.store.Fact(ctx, in.PriorID)
	if err != nil {
		return Fact{}, err
	}
	// The predicate check is here rather than left to the store's own
	// subject-and-predicate match, because the store would report a
	// mismatch between two facts while the real defect is a request that
	// named something that is not a focus policy at all.
	if prior.Predicate != PredicateAnalysisPolicy {
		return Fact{}, fmt.Errorf("%w: fact %q states %s, which is not an analysis policy",
			ErrInvalidValue, in.PriorID, prior.Predicate)
	}
	input := in.FocusPolicyInput
	// The subject is the prior revision's own. A supersession that could
	// move a fact to another subject would be an edit of identity wearing a
	// revision's name.
	input.SubjectID = prior.SubjectID
	return p.store.SupersedeFact(ctx, SupersedeInput{
		PriorID: in.PriorID,
		Fact:    input.fact(p.store.asOfOr(in.At)),
	})
}

// PolicyOutcome is what one installed version does with one analysis-policy
// value: which rule matches it, and therefore what is withheld.
type PolicyOutcome struct {
	Policy    string
	Rule      string
	Allowance Allowance
	// Conditional reports that a rule earlier in this version could match
	// first depending on facts other than the policy — so this outcome is
	// what the value maps to on its own, and the decision for a particular
	// subject is Decide's.
	Conditional bool
}

// PolicyOutcomes maps every analysis-policy value through one installed
// version.
//
// It answers the question a picker has to answer before the operator clicks:
// choosing `excluded` withholds *this much*, under *this* version. The
// arithmetic is here rather than in the surface rendering it, because it is
// rule evaluation — the same first-match-wins order evaluateFocus uses — and a
// surface that reimplemented it could offer a consequence the ledger would not
// deliver.
//
// Conditional is set rather than guessed at. A version whose rules also match
// lifecycle or ownership can pre-empt a policy rule for some subjects, and
// claiming a flat outcome there would be stating something this function
// cannot know.
func (rs FocusRuleSet) PolicyOutcomes() []PolicyOutcome {
	values := predicateSpecs[PredicateAnalysisPolicy].values
	out := make([]PolicyOutcome, 0, len(values))
	for _, value := range values {
		outcome := PolicyOutcome{Policy: value, Allowance: rs.Default}
		for _, rule := range rs.Rules {
			policyOnly, matches := true, true
			for _, cond := range rule.When {
				if cond.Predicate != PredicateAnalysisPolicy {
					policyOnly = false
					continue
				}
				if cond.Equals != value {
					matches = false
				}
			}
			if !matches {
				// This rule wants another policy value, so it cannot
				// match a subject carrying this one whatever else is
				// true about it.
				continue
			}
			if policyOnly {
				outcome.Rule = rule.Name
				outcome.Allowance = rule.Then
				break
			}
			outcome.Conditional = true
		}
		out = append(out, outcome)
	}
	return out
}
