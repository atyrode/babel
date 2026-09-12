package cli

// `babel topics seed` and `babel topics` end to end (SPEC.md §4.13).
//
// The claim is about the whole path rather than about the seeder: two sessions
// in two worktrees of one repository must reach the ledger as *one* proposal
// bound by the repository's own identity, the pass must be idempotent, and the
// command must create nothing — the operator creates every topic he sees.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTopicsSeedProposesOneTopicPerRepositoryAndRepeatsNothing drives the
// command over a synthesized corpus: a checkout, a worktree of it, and a
// scratch directory that is not a repository at all.
func TestTopicsSeedProposesOneTopicPerRepositoryAndRepeatsNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	f := newFixture(t)
	checkout := gitCheckout(t, f.root, "main-checkout", "git@github.com:atyrode/manifold.git")
	worktree := filepath.Join(f.root, "witty-sage-crab")
	runGit(t, checkout, "worktree", "add", "-b", "feature", worktree)

	f.writeSession(sessionSpec{
		project: "p", stem: "in-checkout", id: "s-checkout",
		title: "work in the checkout", workspace: checkout,
	})
	f.writeSession(sessionSpec{
		project: "p", stem: "in-worktree", id: "s-worktree",
		title: "work in the worktree", workspace: worktree,
	})

	stdout, _ := f.ok("topics", "seed", "--json")
	pass := decode[seedResult](t, stdout)
	if pass.Observed != 1 || pass.Raised != 1 {
		t.Fatalf("the pass observed %d identities and raised %d, want one topic for two worktrees: %+v",
			pass.Observed, pass.Raised, pass.Results)
	}
	raised := pass.Results[0]
	if raised.Name != "manifold" || raised.Outcome != "raised" || raised.QuestionID == "" {
		t.Fatalf("the proposal reads %+v, want a raised question about manifold", raised)
	}
	if raised.Identity != "github.com/atyrode/manifold" {
		t.Errorf("the topic is bound to %q, want the repository's remote", raised.Identity)
	}

	// Idempotent: the same catalog raises nothing the second time, because
	// the identity already has an open question and a topic question is
	// keyed by the identity it proposes.
	repeatOut, _ := f.ok("topics", "seed", "--json")
	repeat := decode[seedResult](t, repeatOut)
	if repeat.Raised != 0 || repeat.Skipped != 1 {
		t.Fatalf("the second pass raised %d and skipped %d, want nothing new: %+v",
			repeat.Raised, repeat.Skipped, repeat.Results)
	}
	if repeat.Results[0].Outcome != "proposed" {
		t.Errorf("the repeated identity was skipped as %q, want its open proposal",
			repeat.Results[0].Outcome)
	}

	listOut, _ := f.ok("topics", "--json")
	listed := decode[topicsResult](t, listOut)
	if len(listed.Topics) != 0 {
		t.Errorf("seeding created %d topics; only the operator creates one", len(listed.Topics))
	}
	if len(listed.Proposed) != 1 {
		t.Fatalf("the listing offers %d proposals, want the one seeded", len(listed.Proposed))
	}
	proposal := listed.Proposed[0]
	if proposal.Sessions != 2 {
		t.Errorf("the proposal counts %d sessions, want both worktrees' work", proposal.Sessions)
	}
	if proposal.Why != "2 sessions in 2 checkouts cite this repository" {
		t.Errorf("the proposal says %q", proposal.Why)
	}
	// The workspaces are the proposal's evidence, and a path the ledger
	// refuses to hold — a checkout under a high-entropy temporary
	// directory, which is what a test's own corpus is — is withheld rather
	// than costing the operator the topic.
	if len(proposal.Paths)+raised.Withheld != 2 {
		t.Errorf("the proposal offers %v and withheld %d, want both workspaces accounted for",
			proposal.Paths, raised.Withheld)
	}

	// The terminal rendering names the question, because accepting or
	// declining one takes that identifier.
	plain, _ := f.ok("topics")
	if !strings.Contains(plain, proposal.QuestionID) || !strings.Contains(plain, "manifold") {
		t.Errorf("the listing does not name the proposal:\n%s", plain)
	}
}
