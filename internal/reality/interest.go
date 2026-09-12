package reality

// Interest: the operator's stance toward a subject, as §4.13 records it.
//
// The section is emphatic that this is not a preference knob, and the storage
// is what makes that true rather than a claim. There is no interest column, no
// per-topic setting and no second vocabulary: *working on it*, *keep an eye*,
// *not now* and *excluded* are spellings of §4.8's lifecycle and
// analysis-policy facts, each written as an attributed operator act with the
// reason kept verbatim. So the same stance is readable by Recall (§4.10),
// watches (§4.9), the conductor (§7) and the evaluation lanes (§4.12) without
// any of them learning a new concept — a paused project is paused everywhere
// Babel looks, because what was recorded is a fact about the world and not a
// flag on a page.
//
// Two consequences are deliberate.
//
// Nothing is deleted and nothing is un-set. Changing a stance supersedes the
// revision that held the old one, so "the operator changed his mind, and why"
// is a readable pair of facts. *Not interested is a signal, not a deletion.*
//
// The derived state is a reading of the facts, never a stored copy of it. A
// lifecycle fact asserted by `babel reality` or by an accepted plan moves the
// topic page exactly as a click on the page would, because the page is
// rendering the ledger rather than its own idea of it.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The interest states of §4.13. They are the operator's words; the facts they
// map to are §4.8's.
const (
	// InterestWorking is "working on it": the subject is active and
	// analysis spends on it normally.
	InterestWorking = "working"
	// InterestWatching is "keep an eye": Babel keeps filing into it and
	// spends nothing there.
	InterestWatching = "watching"
	// InterestNotNow is "not now": dormant, and the review lane's draws
	// move elsewhere without deleting a record, a filing or the topic.
	InterestNotNow = "not-now"
	// InterestExcluded withholds analysis outright. It is a policy
	// statement and leaves lifecycle alone: a repository can be excluded
	// from analysis and still be actively worked on, and §4.8's separation
	// exists precisely so one does not imply the other.
	InterestExcluded = "excluded"
)

// InterestStates lists the vocabulary in the order §4.13 presents it, so a
// picker or a route's validation enumerates it rather than keeping a private
// copy that could offer a fifth state nothing can store.
func InterestStates() []string {
	return []string{InterestWorking, InterestWatching, InterestNotNow, InterestExcluded}
}

// Interest is what the ledger currently says about the operator's stance, and
// which attributed act said it.
//
// State is empty when neither predicate has a fact in force: that is "nobody
// has said", which is different from every stated stance and must not be
// rendered as one. A retired subject is also empty — retirement is not a
// degree of interest, and §4.13 has a retired topic leave the list rather than
// sit in it at the bottom.
type Interest struct {
	State string
	// Reason is the operator's own words, kept verbatim from the deciding
	// fact's note.
	Reason string
	// At and By are the attribution of the deciding fact: when the operator
	// stated this, and who he is.
	At time.Time
	By string
}

// interestFacts maps a stance onto the facts that record it.
//
// An empty lifecycle means "leave it alone", which is what `excluded` does:
// §4.8 keeps lifecycle and analysis policy separate, and writing a lifecycle
// value here would make excluding a subject silently claim something about
// whether anybody is working on it.
func interestFacts(state string) (lifecycle, policy string, ok bool) {
	switch state {
	case InterestWorking:
		return LifecycleActive, PolicyNormal, true
	case InterestWatching:
		return LifecycleMaintenanceOnly, PolicyLearnOnly, true
	case InterestNotNow:
		return LifecycleDormant, PolicyLearnOnly, true
	case InterestExcluded:
		return "", PolicyExcluded, true
	}
	return "", "", false
}

// EntityInterest derives one subject's stance from the facts in force.
//
// Precedence is the facts', not a preference order. An excluded analysis
// policy is the strongest thing the ledger can say about expenditure and it
// answers first, whatever the lifecycle says; otherwise the lifecycle names
// the stance, because *working*, *keep an eye* and *not now* differ in
// lifecycle and share their policy. A subject with neither fact answers empty,
// and a retired one answers empty too.
func (s *Store) EntityInterest(ctx context.Context, entityID string) (Interest, error) {
	if entityID == "" {
		return Interest{}, fmt.Errorf("%w: interest names no entity", ErrInvalidValue)
	}
	current, err := s.currentInterestFacts(ctx, entityID)
	if err != nil {
		return Interest{}, err
	}
	policy, hasPolicy := current[PredicateAnalysisPolicy]
	if hasPolicy && policy.Value.Enum == PolicyExcluded {
		return interestFrom(InterestExcluded, policy), nil
	}
	lifecycle, hasLifecycle := current[PredicateLifecycle]
	if !hasLifecycle {
		return Interest{}, nil
	}
	switch lifecycle.Value.Enum {
	case LifecycleActive:
		return interestFrom(InterestWorking, lifecycle), nil
	case LifecycleMaintenanceOnly:
		return interestFrom(InterestWatching, lifecycle), nil
	case LifecycleDormant:
		return interestFrom(InterestNotNow, lifecycle), nil
	}
	// Retired, and anything a later build adds to the lifecycle
	// vocabulary: the ledger holds a fact this vocabulary does not spell,
	// and inventing a stance for it would be the surface deciding what the
	// operator meant.
	return Interest{}, nil
}

// interestFrom carries the deciding fact's attribution into the answer, so a
// surface showing a stance can show whose act it was and what he said.
func interestFrom(state string, fact Fact) Interest {
	at := fact.Authority.At
	if at.IsZero() {
		at = fact.ObservedAt
	}
	return Interest{State: state, Reason: fact.Payload.Note, At: at, By: fact.Authority.ID}
}

// currentInterestFacts reads the lifecycle and analysis-policy facts in force
// for one subject, through the same currentFacts a focus decision uses — so a
// topic page and a deferral never disagree about which revision is current.
func (s *Store) currentInterestFacts(ctx context.Context, entityID string) (map[Predicate]Fact, error) {
	var facts []Fact
	for _, predicate := range []Predicate{PredicateLifecycle, PredicateAnalysisPolicy} {
		found, err := s.Facts(ctx, FactQuery{SubjectID: entityID, Predicate: predicate})
		if err != nil {
			return nil, err
		}
		facts = append(facts, found...)
	}
	return currentFacts(facts, s.now()), nil
}

// SetInterest records the operator's stance toward a subject.
//
// Each predicate is one attributed operator act carrying the reason verbatim:
// a supersession when the ledger already holds a revision in force, and an
// assertion when it does not. Restating the same value is a supersession too —
// the operator said it again, possibly for a different reason, and an
// append-only ledger records that rather than deciding it was redundant.
//
// The two facts are two transactions, and the order is lifecycle first. They
// cannot be one: AssertFact and SupersedeFact each stage their own
// publication, and a half-applied stance is reported with the error rather
// than hidden — the first fact is real, the caller learns the second did not
// land, and retrying is idempotent because a restatement is a legal
// supersession.
func (s *Store) SetInterest(ctx context.Context, entityID, operator, state, reason string) error {
	lifecycle, policy, ok := interestFacts(state)
	if !ok {
		return fmt.Errorf("%w: interest state %q", ErrInvalidValue, state)
	}
	if operator == "" {
		return fmt.Errorf("%w: interest has no operator", ErrInvalidValue)
	}
	canonical, err := s.requireEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if lifecycle != "" {
		if err := s.stateFact(ctx, canonical, PredicateLifecycle, lifecycle, operator, reason); err != nil {
			return err
		}
	}
	return s.stateFact(ctx, canonical, PredicateAnalysisPolicy, policy, operator, reason)
}

// RetireEntity records that a subject should never have existed, or has
// stopped existing.
//
// Nothing is deleted: §4.13's retirement is a lifecycle fact with an
// attributed reason, so the topic, its filings and its history all remain
// readable and the retirement is reversible by superseding it. What changes is
// what reads of the ledger conclude — a retired subject is not a stance, so
// EntityInterest answers empty for it, and §4.13 has the filings under it
// return to the triage backlog rather than disappear with it.
func (s *Store) RetireEntity(ctx context.Context, entityID, operator, reason string) error {
	if operator == "" {
		return fmt.Errorf("%w: retirement has no operator", ErrInvalidValue)
	}
	canonical, err := s.requireEntity(ctx, entityID)
	if err != nil {
		return err
	}
	return s.stateFact(ctx, canonical, PredicateLifecycle, LifecycleRetired, operator, reason)
}

// retireEntity writes §4.13's retirement inside a caller's transaction, so a
// topic proposal the operator accepted retires its topic and records the
// ruling in one commit.
//
// It is the same fact stateFact writes and it is written the same way — a
// supersession when a lifecycle revision is in force, an assertion when none
// is — and it is a second body rather than a shared one because stateFact's
// two writes each open their own transaction, which is exactly what an
// application inside a transaction cannot do.
func (s *Store) retireEntity(ctx context.Context, tx *sql.Tx, entityID string,
	authority Authority, reason string, set *recordSet) (Fact, error) {
	canonical, err := resolve(ctx, tx, entityID)
	if err != nil {
		return Fact{}, err
	}
	held, err := readFacts(ctx, tx, `WHERE f.subject_id = ? AND f.predicate = ?`,
		canonical, string(PredicateLifecycle))
	if err != nil {
		return Fact{}, err
	}
	input := FactInput{
		SubjectID: canonical,
		Predicate: PredicateLifecycle,
		Value:     FactValue{Kind: ValueEnum, Enum: LifecycleRetired},
		ValidFrom: authority.At,
		// Open-ended and observed now, for stateFact's reason: an
		// operator stating intent is observing his own intent.
		ObservedAt:  authority.At,
		Authority:   authority,
		Confidence:  ConfidenceHigh,
		Sensitivity: SensitivityRoutine,
		Note:        reason,
	}
	if err := input.validate(); err != nil {
		return Fact{}, err
	}
	var fact Fact
	if prior, ok := currentFacts(held, s.now())[PredicateLifecycle]; ok {
		fact, err = s.supersedeFact(ctx, tx, SupersedeInput{PriorID: prior.ID, Fact: input}, "", "")
	} else {
		fact, _, err = s.assertFact(ctx, tx, input, "", "", "")
	}
	if err != nil {
		return Fact{}, err
	}
	if err := set.add(stagedFact(fact)); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

// EntityRetired reports whether the lifecycle fact in force retires this
// subject. It is the one question a listing asks about retirement, and a
// caller answering it from Facts would have to re-derive which revision is
// current.
func (s *Store) EntityRetired(ctx context.Context, entityID string) (bool, error) {
	current, err := s.currentInterestFacts(ctx, entityID)
	if err != nil {
		return false, err
	}
	fact, ok := current[PredicateLifecycle]
	return ok && fact.Value.Enum == LifecycleRetired, nil
}

// stateFact writes one operator-intent enum fact, superseding whatever
// revision is in force. It is the shared body of SetInterest and
// RetireEntity, so both attribute, timestamp and supersede identically.
func (s *Store) stateFact(ctx context.Context, subjectID string, predicate Predicate,
	value, operator, reason string) error {
	current, err := s.currentInterestFacts(ctx, subjectID)
	if err != nil {
		return err
	}
	at := s.now()
	input := FactInput{
		SubjectID: subjectID,
		Predicate: predicate,
		Value:     FactValue{Kind: ValueEnum, Enum: value},
		ValidFrom: at,
		// Open-ended and observed now, for FocusPolicy's reason: an
		// operator stating intent is observing his own intent, and
		// intent holds until it is superseded.
		ObservedAt:  at,
		Authority:   Authority{Kind: AuthorityOperator, ID: operator, At: at},
		Confidence:  ConfidenceHigh,
		Sensitivity: SensitivityRoutine,
		Note:        reason,
	}
	if prior, ok := current[predicate]; ok {
		_, err := s.SupersedeFact(ctx, SupersedeInput{PriorID: prior.ID, Fact: input})
		return err
	}
	_, _, err = s.AssertFact(ctx, input)
	return err
}

// requireEntity resolves a subject through the merge history and refuses one
// the ledger does not hold, so a stance cannot be recorded against a name
// nobody minted.
func (s *Store) requireEntity(ctx context.Context, entityID string) (string, error) {
	if entityID == "" {
		return "", fmt.Errorf("%w: no entity named", ErrInvalidValue)
	}
	if err := requireRow(ctx, s.db, "reality_entity", "id", entityID); err != nil {
		return "", err
	}
	return s.Resolve(ctx, entityID)
}
