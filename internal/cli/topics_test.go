package cli

// `babel topics` end to end (SPEC.md §4.13).
//
// The command lists and nothing else. `babel topics seed` was the other half
// of this file and is gone: §4.13's second reading gives every topic change
// to a proposal a run published and a ruling the operator gave, so a command
// that minted proposals from a directory scan would be Babel's naming decided
// by a heuristic nobody reviewed. What is left to assert is that the listing
// shows the operator what awaits him — the plan, what it would do, and the
// proposal record he rules on — and that it creates nothing.

import (
	"context"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// TestTopicsListsWhatAwaitsARulingAndCreatesNothing writes one plan through
// the ledger, which is the only way one exists, and reads the command's two
// renderings of it.
func TestTopicsListsWhatAwaitsARulingAndCreatesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	store, err := reality.Open(f.dataDir)
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	if err := store.ProposeTopic(ctx, reality.TopicPlan{
		ProposalID: "pro_listed",
		Operation:  reality.TopicCreate,
		Identity:   "github.com/atyrode/manifold",
		Entity: &reality.EntityDraft{
			Subject: reality.NewSubject{
				Kind:        reality.EntityRepository,
				DisplayName: "manifold",
			},
			Binding: []reality.FactInput{{
				Predicate: reality.PredicateRepositoryRemote,
				Value:     reality.FactValue{Kind: reality.ValueText, Text: "github.com/atyrode/manifold"},
			}},
		},
		Reasoning: "2 sessions in 2 checkouts cite this repository",
		Sessions:  2,
		By:        reality.Provenance{RunID: "run-7", RecipeID: "babel-files-its-output"},
	}); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stdout, _ := f.ok("topics", "--json")
	listed := decode[topicsResult](t, stdout)
	if len(listed.Topics) != 0 {
		t.Errorf("a proposal made %d topics; only a ruling does that", len(listed.Topics))
	}
	if len(listed.Proposed) != 1 {
		t.Fatalf("the listing offers %d proposals, want the one published", len(listed.Proposed))
	}
	proposal := listed.Proposed[0]
	if proposal.ProposalID != "pro_listed" || proposal.Operation != "create" {
		t.Fatalf("the proposal reads %+v, want the create on the published record", proposal)
	}
	if proposal.Name != "manifold" || proposal.Sessions != 2 || proposal.RunID != "run-7" {
		t.Errorf("the proposal reads %+v, want the topic, the evidence and the run", proposal)
	}
	if proposal.Why != "2 sessions in 2 checkouts cite this repository" {
		t.Errorf("the proposal says %q", proposal.Why)
	}

	// The terminal rendering names the proposal record, because ruling on
	// one takes that identifier.
	plain, _ := f.ok("topics")
	if !strings.Contains(plain, "pro_listed") || !strings.Contains(plain, "manifold") {
		t.Errorf("the listing does not name the proposal:\n%s", plain)
	}

	// `seed` is not a subcommand any more: it is read as a stray argument
	// rather than quietly listing.
	if _, stderr, code := f.run("topics", "seed"); code == exitOK {
		t.Errorf("babel topics seed still succeeds\nstderr:\n%s", stderr)
	}
}
