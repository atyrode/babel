package reality

import (
	"context"
	"testing"
)

// TestTheLedgerListsWhatItHoldsAndNotOnlyWhatIsPending covers the three reads a
// reading surface needs and the inbox cannot give it.
//
// Inbox is deliberately narrow — `open` and `plan-ready`, the two states only a
// human can move — and Facts deliberately requires a subject, because analysis
// asks about something in particular. Neither answers the question a reader
// arriving at the ledger has: what is in here. A question the operator already
// answered, an entity nobody linked to, and a fact whose subject the reader
// cannot name were each reachable only by an identifier nobody holds, which is
// what §8.4 counts as a record the product does not have.
//
// The fixture is one accepted plan, which is the cheapest way to a ledger with
// history in it: accepting asserts a fact, folds one identity into another, and
// asks a follow-up question, so the listings are read over questions in three
// states, an identity that was merged away, and facts about two subjects.
func TestTheLedgerListsWhatItHoldsAndNotOnlyWhatIsPending(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store := fixture.store
	if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID,
		Actor:  "operator",
	}); err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}

	t.Run("questions", func(t *testing.T) {
		listed, err := store.Questions(ctx, QuestionQuery{})
		if err != nil {
			t.Fatalf("Questions: %v", err)
		}
		// The accepted plan's question and the follow-up it asked. The
		// answered one is the whole point: it has left the inbox, and
		// before this read nothing could reach it.
		if len(listed) != 2 {
			t.Fatalf("Questions listed %d questions, want the answered one and its follow-up", len(listed))
		}
		if listed[0].Question.CreatedAt.Before(listed[1].Question.CreatedAt) {
			t.Errorf("Questions are oldest first; the newest question must lead")
		}
		pending, err := store.Inbox(ctx, InboxQuery{})
		if err != nil {
			t.Fatalf("Inbox: %v", err)
		}
		if len(pending) != 1 || pending[0].Question.ID != listed[0].Question.ID {
			t.Fatalf("the inbox holds %d questions; the listing is supposed to be wider than it", len(pending))
		}

		var answered *QuestionListing
		for i, item := range listed {
			if item.Question.ID == fixture.question.ID {
				answered = &listed[i]
			}
		}
		if answered == nil {
			t.Fatal("the answered question is missing from the listing of every question")
		}
		if answered.Question.State != QuestionAnswered {
			t.Errorf("answered question state = %q, want %q", answered.Question.State, QuestionAnswered)
		}
		if answered.Answers != 1 || answered.Plans != 1 {
			t.Errorf("answered question carries %d answers and %d plans, want 1 and 1",
				answered.Answers, answered.Plans)
		}
		if answered.Question.Payload.Prompt == "" {
			t.Error("the listing dropped the prompt, which is the only part a reader can read")
		}

		only, err := store.Questions(ctx, QuestionQuery{States: []QuestionState{QuestionOpen}})
		if err != nil {
			t.Fatalf("Questions(open): %v", err)
		}
		if len(only) != 1 || only[0].Question.State != QuestionOpen {
			t.Errorf("Questions(open) = %d rows, want only the follow-up", len(only))
		}
		if _, err := store.Questions(ctx, QuestionQuery{Class: "urgent"}); !isErr(err, ErrInvalidValue) {
			t.Errorf("Questions with a class outside the vocabulary returned %v, want ErrInvalidValue", err)
		}
		if _, err := store.Questions(ctx, QuestionQuery{
			States: []QuestionState{"forgotten"},
		}); !isErr(err, ErrInvalidValue) {
			t.Errorf("Questions with a state outside the state machine returned %v, want ErrInvalidValue", err)
		}
	})

	t.Run("entities", func(t *testing.T) {
		listed, err := store.Entities(ctx, EntityQuery{})
		if err != nil {
			t.Fatalf("Entities: %v", err)
		}
		rows := map[string]EntityListing{}
		for _, item := range listed {
			rows[item.Entity.ID] = item
		}
		// The identity the accepted plan folded away is still listed.
		// §4.8 forbids losing it, and a listing that hid it would be the
		// one surface where it was lost.
		folded, ok := rows[fixture.folded.ID]
		if !ok {
			t.Fatal("the merged-away identity is missing from the entity listing")
		}
		if folded.Entity.Role != RoleMerged || folded.Entity.CanonicalID != fixture.project.ID {
			t.Errorf("folded identity reads as role %q under %q, want %q under the project",
				folded.Entity.Role, folded.Entity.CanonicalID, RoleMerged)
		}
		project, ok := rows[fixture.project.ID]
		if !ok {
			t.Fatal("the project is missing from the entity listing")
		}
		if project.Facts != 1 || project.Active != 1 {
			t.Errorf("the project counts %d facts (%d active), want the one the plan asserted",
				project.Facts, project.Active)
		}
		if project.LatestFact.IsZero() {
			t.Error("the project's row reports no latest fact, though the plan asserted one")
		}
		if folded.Facts != 0 {
			t.Errorf("the folded identity counts %d facts of its own, want none", folded.Facts)
		}

		repositories, err := store.Entities(ctx, EntityQuery{Kind: EntityRepository})
		if err != nil {
			t.Fatalf("Entities(repository): %v", err)
		}
		if len(repositories) != 0 {
			t.Errorf("Entities(repository) = %d rows, want none; both fixtures are projects", len(repositories))
		}
		if _, err := store.Entities(ctx, EntityQuery{Kind: "spaceship"}); !isErr(err, ErrInvalidValue) {
			t.Errorf("Entities with a kind outside the vocabulary returned %v, want ErrInvalidValue", err)
		}
	})

	t.Run("recent facts", func(t *testing.T) {
		observed := fixture.clock.now()
		second, _, err := store.AssertFact(ctx, operatorFact(fixture.project.ID,
			PredicateOwnership, enum(OwnershipOwned), observed))
		if err != nil {
			t.Fatalf("AssertFact: %v", err)
		}
		facts, err := store.RecentFacts(ctx, 10)
		if err != nil {
			t.Fatalf("RecentFacts: %v", err)
		}
		if len(facts) != 2 {
			t.Fatalf("RecentFacts returned %d facts, want both the ledger holds", len(facts))
		}
		if facts[0].ID != second.ID {
			t.Errorf("RecentFacts leads with %s, want the newest revision %s", facts[0].ID, second.ID)
		}
		if got, err := store.RecentFacts(ctx, 1); err != nil || len(got) != 1 {
			t.Errorf("RecentFacts(1) = %d facts, %v; want exactly one", len(got), err)
		}
		if _, err := store.RecentFacts(ctx, 0); !isErr(err, ErrInvalidValue) {
			t.Errorf("RecentFacts with no bound returned %v, want ErrInvalidValue", err)
		}
	})
}
