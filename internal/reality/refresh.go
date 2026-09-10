package reality

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Facts go stale on their own, and something has to notice.
//
// §4.8 gives every predicate a refresh expectation — where a service runs is
// worth doubting after a month, whether it is deployed after a week — and
// ExpireStale marks the ones that lapsed. Marking is not asking, though, and
// until this existed a stale fact simply sat there: the ledger knew the claim
// had aged out and no one was ever told.
//
// This is the producer the Question inbox was built for and never had. It
// derives its questions from facts Babel already holds, on no authority of
// its own: a Question is a request for someone else to authorize something,
// which is exactly why analysis may raise one and may not answer it. §4.8
// keeps facts to attributed operator actions and configured trusted sources,
// and nothing here writes a fact.

// Refresh is what one pass over the stale facts did.
//
// The counts are separate because they answer different questions: expired is
// what the ledger changed, asked is what an operator will see in the inbox,
// and the two differ whenever a question about that subject and predicate is
// already open — which is the normal case for a fact that has been stale for
// a while.
type Refresh struct {
	// ExpiredFactIDs are the facts this pass marked stale.
	ExpiredFactIDs []string
	// Asked are the questions it raised, in the order it raised them.
	Asked []Question
	// Existing counts the stale facts that already had a live question, so a
	// pass that asks nothing new is legible rather than silent.
	Existing int
	// Suppressed counts the ones a declined predecessor still covers: the
	// operator said no, and asking again with nothing new to show would be
	// nagging rather than noticing.
	Suppressed int
}

// RefreshStale expires what has lapsed and asks about it.
//
// One question per stale fact, targeting the fact's subject and predicate, so
// the dedupe key collapses repeated passes over the same lapsed claim into
// the one question that is already waiting. A question that cannot be asked
// is counted rather than returned as an error: a pass over a hundred facts
// must not stop at the first one an operator declined last week.
func (s *Store) RefreshStale(ctx context.Context, asOf time.Time) (Refresh, error) {
	expired, err := s.ExpireStale(ctx, asOf)
	if err != nil {
		return Refresh{}, err
	}
	out := Refresh{ExpiredFactIDs: expired}
	for _, id := range expired {
		fact, err := s.Fact(ctx, id)
		if err != nil {
			return out, fmt.Errorf("read stale fact %s: %w", id, err)
		}
		input, err := s.refreshQuestion(ctx, fact)
		if err != nil {
			return out, err
		}
		question, err := s.Ask(ctx, input)
		switch {
		case errors.Is(err, ErrDuplicateQuestion):
			out.Existing++
		case errors.Is(err, ErrSuppressed):
			out.Suppressed++
		case err != nil:
			return out, fmt.Errorf("ask about stale fact %s: %w", id, err)
		default:
			out.Asked = append(out.Asked, question)
		}
	}
	return out, nil
}

// refreshQuestion renders the question one stale fact deserves.
//
// The prompt names the subject the way the operator named it and states what
// the ledger currently believes, because a question that asks "is this still
// true?" without saying what "this" is cannot be answered from the inbox
// alone. Expected authority follows the fact's own: a claim a trusted source
// authored is refreshed by that source's next batch rather than by a person
// retyping it, and the inbox says so instead of routing every lapse to the
// operator.
func (s *Store) refreshQuestion(ctx context.Context, fact Fact) (QuestionInput, error) {
	subject, err := s.entityName(ctx, fact.SubjectID)
	if err != nil {
		return QuestionInput{}, err
	}
	// A value that is an entity is rendered by name too. The identifier
	// would read as noise to the operator answering, and a long opaque
	// token in prose is exactly what the ledger's credential check refuses
	// to store — correctly, since it cannot tell one from a secret.
	value := factValueText(fact.Value)
	if fact.Value.Kind == ValueEntity {
		value, err = s.entityName(ctx, fact.Value.ObjectID)
		if err != nil {
			return QuestionInput{}, err
		}
	}
	ttl, why := fact.Predicate.TTL()

	expected := fact.Authority.Kind
	if !expected.authorizes() {
		expected = AuthorityOperator
	}

	return QuestionInput{
		Kind:              KindRefreshStale,
		Class:             ClassMaintenance,
		Sensitivity:       fact.Sensitivity,
		ExpectedAuthority: expected,
		TargetEntityIDs:   []string{fact.SubjectID},
		TargetPredicates:  []Predicate{fact.Predicate},
		ExistingFactIDs:   []string{fact.ID},
		// The lapsed fact is the material evidence, so a question declined
		// once is reopened by a newer fact rather than by the clock.
		MaterialEvidence: []string{fact.ID},
		Payload: QuestionPayload{
			Prompt: fmt.Sprintf("Is %s still %s for %s?", fact.Predicate, value, subject),
			WhyAsked: fmt.Sprintf(
				"The ledger has held this since %s and %s expects a refresh every %s: %s.",
				fact.ObservedAt.UTC().Format(time.DateOnly), fact.Predicate,
				humanTTL(ttl), why),
		},
	}, nil
}

// entityName is what to call an entity in a sentence a person reads.
//
// An entity with no display name falls back to its kind rather than to its
// identifier: "the service" is less precise and more useful than a token,
// and the question carries the entity id in its target field anyway, where a
// reader's tools can resolve it.
func (s *Store) entityName(ctx context.Context, id string) (string, error) {
	entity, err := s.Entity(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read entity %s: %w", id, err)
	}
	if entity.Payload.DisplayName != "" {
		return entity.Payload.DisplayName, nil
	}
	return "the " + string(entity.Kind), nil
}

// factValueText renders whichever field the value's kind selects, which is
// the same rule the CLI renders by and the only one that does not print the
// zero value of a field this value does not use.
func factValueText(v FactValue) string {
	switch v.Kind {
	case ValueEnum:
		return v.Enum
	case ValueEntity:
		return v.ObjectID
	default:
		return v.Text
	}
}

// humanTTL renders a refresh expectation in the unit it was written in.
// Predicate TTLs are whole days, and "720h0m0s" in a question an operator
// reads is a number they have to divide.
func humanTTL(ttl time.Duration) string {
	days := int(ttl.Hours() / 24)
	switch {
	case ttl <= 0:
		return "never"
	case days <= 1:
		return "day"
	default:
		return fmt.Sprintf("%d days", days)
	}
}
