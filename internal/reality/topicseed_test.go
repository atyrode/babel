package reality

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

func observedRepositories() []TopicObservation {
	return []TopicObservation{
		{
			Identity:  "github.com/atyrode/manifold",
			Remote:    "github.com/atyrode/manifold",
			Name:      "manifold",
			Paths:     []string{"/home/alex/manifold", "/home/alex/wt/manifold-fix"},
			Sessions:  32,
			Checkouts: 2,
			Records:   []frontier.Ref{{Type: frontier.EntityFinding, ID: "fnd-1"}},
		},
		{
			Identity:  "/home/alex/nix-dotfiles/.git",
			Name:      "nix-dotfiles",
			Paths:     []string{"/home/alex/nix-dotfiles"},
			Sessions:  1,
			Checkouts: 1,
		},
	}
}

// TestSeedTopicsIsIdempotentAndProposesRatherThanCreates is §4.13's seeding
// rule in one test: a deployment may seed from repository identity alone, and
// it must not create anything.
//
// Idempotence is a property of what it asks rather than of a marker it keeps,
// which is why the second pass is the real assertion: the identities are the
// same, the questions are the same questions, and the ledger recognizes them
// without the seeder remembering that it ran.
func TestSeedTopicsIsIdempotentAndProposesRatherThanCreates(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	by := Provenance{Actor: "topic-seed"}

	first, err := store.SeedTopics(ctx, observedRepositories(), by)
	if err != nil {
		t.Fatalf("SeedTopics: %v", err)
	}
	if first.Raised() != 2 || first.Skipped() != 0 {
		t.Fatalf("the first pass raised %d and skipped %d, want two proposals",
			first.Raised(), first.Skipped())
	}
	entities, err := store.Entities(ctx, EntityQuery{})
	if err != nil {
		t.Fatalf("Entities: %v", err)
	}
	if len(entities) != 0 {
		t.Fatalf("seeding created %d entities; only the operator creates a topic", len(entities))
	}

	second, err := store.SeedTopics(ctx, observedRepositories(), by)
	if err != nil {
		t.Fatalf("second SeedTopics: %v", err)
	}
	if second.Raised() != 0 || second.Skipped() != 2 {
		t.Fatalf("the second pass raised %d and skipped %d, want nothing new",
			second.Raised(), second.Skipped())
	}
	for _, result := range second.Results {
		if result.Outcome != SeedProposed {
			t.Errorf("%s was skipped as %q, want the open proposal it already has",
				result.Name, result.Outcome)
		}
	}
	open, err := store.TopicProposals(ctx)
	if err != nil {
		t.Fatalf("TopicProposals: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("the inbox holds %d proposals after two passes, want two", len(open))
	}
	// Heaviest evidence first, and the proposal says what it counted.
	if open[0].Proposal.Name != "manifold" || open[0].Proposal.Sessions != 32 {
		t.Errorf("the first proposal is %+v, want manifold with 32 sessions", open[0].Proposal)
	}
	if got := open[0].Proposal.Reasoning; got != "32 sessions in 2 checkouts cite this repository" {
		t.Errorf("the proposal's reason is %q", got)
	}
	if got := open[1].Proposal.Reasoning; got != "1 session cites this repository" {
		t.Errorf("the single-checkout proposal's reason is %q", got)
	}

	// A repository with no remote is bound by the common directory every
	// worktree shares, which is the only identity this host can observe for
	// it (§4.13).
	local := open[1].Proposal
	if len(local.Binding) != 1 || local.Binding[0].Predicate != PredicateLocalPath ||
		local.Binding[0].Value.Text != "/home/alex/nix-dotfiles/.git" {
		t.Errorf("the remoteless proposal binds %+v", local.Binding)
	}

	// Once accepted, the identity binds and the seeder reports it as such
	// rather than proposing a second subject for one repository.
	if _, err := store.AcceptTopic(ctx, open[0].Question.ID, "operator", &recordingFiler{}); err != nil {
		t.Fatalf("AcceptTopic: %v", err)
	}
	third, err := store.SeedTopics(ctx, observedRepositories(), by)
	if err != nil {
		t.Fatalf("third SeedTopics: %v", err)
	}
	var bound SeedResult
	for _, result := range third.Results {
		if result.Name == "manifold" {
			bound = result
		}
	}
	if bound.Outcome != SeedBound || bound.EntityID == "" {
		t.Errorf("manifold was reported as %+v, want bound to the entity it created", bound)
	}
}

// TestSeedTopicsReportsADeclinedIdentityUntilTheCatalogSaysMore keeps the
// operator's refusal refused across seeding passes: a background job that
// re-raised what he declined would be the repetition §4.13's suppression
// exists to stop, and the evidence that lifts it is more sessions than there
// were when he refused.
func TestSeedTopicsReportsADeclinedIdentityUntilTheCatalogSaysMore(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	by := Provenance{Actor: "topic-seed"}

	observed := []TopicObservation{{
		Identity: "/tmp/scratch/.git",
		Name:     "scratch",
		Paths:    []string{"/tmp/scratch"},
		Sessions: 2,
	}}
	report, err := store.SeedTopics(ctx, observed, by)
	if err != nil {
		t.Fatalf("SeedTopics: %v", err)
	}
	if report.Raised() != 1 {
		t.Fatalf("the first pass raised %d", report.Raised())
	}
	if err := store.DeclineTopic(ctx, report.Results[0].QuestionID, "operator",
		"a scratch checkout is not a project"); err != nil {
		t.Fatalf("DeclineTopic: %v", err)
	}

	again, err := store.SeedTopics(ctx, observed, by)
	if err != nil {
		t.Fatalf("second SeedTopics: %v", err)
	}
	if again.Raised() != 0 || again.Results[0].Outcome != SeedDeclined {
		t.Fatalf("the refused identity came back as %+v", again.Results[0])
	}

	grown := observed
	grown[0].Sessions = 12
	revived, err := store.SeedTopics(ctx, grown, by)
	if err != nil {
		t.Fatalf("third SeedTopics: %v", err)
	}
	if revived.Raised() != 1 {
		t.Fatalf("a grown identity was not re-proposed: %+v", revived.Results)
	}
}
