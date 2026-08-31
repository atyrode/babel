package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These cases drive issue #87's whole operator loop through the shipped
// command surface — Run, the real durable file, the real stores — rather than
// through the packages underneath it. What they are checking is the wiring: a
// store method that works and a command that never reaches it is the failure
// mode a package test cannot see.

// TestRevisionChainIsReadableFromTheCommandSurface walks the loop an operator
// actually performs: revise a candidate, read its history back, and confirm
// the superseded wording is still there.
func TestRevisionChainIsReadableFromTheCommandSurface(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.ok("revise", ids.deferred, "--statement", "the deferred candidate, narrowed",
		"--reason", "it covered three systems at once", "--operator", "synthetic-operator", "--json")
	revised := decodeJSON[reviseResult](t, stdout)
	if revised.Supersedes != ids.deferred {
		t.Fatalf("the revision supersedes %q, want %q", revised.Supersedes, ids.deferred)
	}
	if revised.Revision.Sequence != 2 {
		t.Errorf("revision sequence = %d, want 2", revised.Revision.Sequence)
	}
	if revised.Revision.Actor != "operator synthetic-operator" {
		t.Errorf("revision actor = %q", revised.Revision.Actor)
	}
	assertNoRawControls(t, "revise --json", stdout, stderr)

	// The chain reads the same from the ancestor an operator still has in
	// their scrollback and from the descendant the command just printed.
	for _, from := range []string{ids.deferred, revised.Revision.RecordID} {
		stdout, stderr := f.ok("revisions", from, "--json")
		chain := decodeJSON[revisionsResult](t, stdout)
		if len(chain.Revisions) != 2 {
			t.Fatalf("chain from %s has %d revisions, want 2", from, len(chain.Revisions))
		}
		if chain.HeadID != revised.Revision.RecordID {
			t.Errorf("head from %s = %q, want %q", from, chain.HeadID, revised.Revision.RecordID)
		}
		if !chain.Revisions[1].Head || chain.Revisions[0].Head {
			t.Errorf("the head marker is on the wrong revision: %+v", chain.Revisions)
		}
		if chain.Revisions[1].Reason != "it covered three systems at once" {
			t.Errorf("the reason was lost: %q", chain.Revisions[1].Reason)
		}
		assertNoRawControls(t, "revisions --json", stdout, stderr)
	}

	// The ancestor is untouched and still readable at its own identifier,
	// which is what "nothing is deleted" means for a revision.
	stdout, _ = f.ok("hypothesis", "show", ids.deferred, "--json")
	ancestor := decodeJSON[hypothesisResult](t, stdout)
	if ancestor.Hypothesis.Statement != "the deferred candidate" {
		t.Errorf("the ancestor's wording changed to %q", ancestor.Hypothesis.Statement)
	}
}

// TestReviseRefusesWhatItCannotRecord covers the three refusals that keep a
// chain worth reading: an unargued revision, an unattributed one, and one
// aimed at a record kind an operator cannot retype.
func TestReviseRefusesWhatItCannotRecord(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no reason", []string{"revise", ids.deferred, "--statement", "reworded", "--operator", "op"}},
		{"no statement", []string{"revise", ids.deferred, "--reason", "because", "--operator", "op"}},
		{"no operator", []string{"revise", ids.deferred, "--statement", "reworded", "--reason", "because"}},
		{"not a candidate", []string{"revise", ids.finding, "--statement", "reworded",
			"--reason", "because", "--operator", "op"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := f.mustExit(exitUsage, tc.args...)
			if stdout != "" {
				t.Errorf("a rejected invocation wrote to stdout: %q", stdout)
			}
		})
	}
}

// TestReviveReturnsARestingCandidate is #87's "nothing closes" at the command
// surface, including the refusal that keeps the transition meaningful.
func TestReviveReturnsARestingCandidate(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.ok("revive", ids.rejected, "--reason", "a later session showed it again",
		"--operator", "synthetic-operator", "--json")
	revived := decodeJSON[reviveResult](t, stdout)
	if revived.Status.Status != "queued" {
		t.Fatalf("revived into %q, want queued", revived.Status.Status)
	}
	if revived.Status.Actor != "operator synthetic-operator" {
		t.Errorf("revive actor = %q", revived.Status.Actor)
	}
	if revived.Status.Note != "a later session showed it again" {
		t.Errorf("the reason was lost: %q", revived.Status.Note)
	}
	assertNoRawControls(t, "revive --json", stdout, stderr)

	// The rejection stays in the history: reviving argues with it rather
	// than erasing it.
	stdout, _ = f.ok("hypothesis", "show", ids.rejected, "--json")
	shown := decodeJSON[hypothesisResult](t, stdout)
	if shown.Hypothesis.Status != "queued" {
		t.Errorf("current status = %q, want queued", shown.Hypothesis.Status)
	}
	var sawRejected bool
	for _, e := range shown.StatusHistory {
		if e.Status == "rejected" {
			sawRejected = true
		}
	}
	if !sawRejected {
		t.Errorf("the rejection vanished from the history: %+v", shown.StatusHistory)
	}

	// A candidate already on the frontier is not at rest, so there is
	// nothing to revive it from.
	if _, _, code := f.run("revive", ids.rejected, "--reason", "again",
		"--operator", "synthetic-operator"); code != exitFailure {
		t.Errorf("reviving a queued candidate exited %d, want %d", code, exitFailure)
	}
}

// TestInvitationQueueIsInstructionFree covers #96's rung one from the command
// surface: an operator can put a record in front of the next run and cannot
// tell it what to do there.
func TestInvitationQueueIsInstructionFree(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.ok("invite", ids.hypothesis, "--operator", "synthetic-operator", "--json")
	invited := decodeJSON[inviteResult](t, stdout)
	if invited.Invitation.RecordID != ids.hypothesis {
		t.Fatalf("invited %q, want %q", invited.Invitation.RecordID, ids.hypothesis)
	}
	if !invited.Invitation.Open {
		t.Error("a fresh invitation is not open")
	}
	assertNoRawControls(t, "invite --json", stdout, stderr)

	stdout, _ = f.ok("invitations", "--json")
	queue := decodeJSON[invitationsResult](t, stdout)
	if len(queue.Invitations) != 1 || queue.Invitations[0].ID != invited.Invitation.ID {
		t.Fatalf("queue = %+v, want the invitation just recorded", queue.Invitations)
	}

	// There is no way to attach an instruction, which is the invariant
	// rather than an omission: a flag that took one would make an
	// invitation a brief.
	if _, _, code := f.run("invite", ids.hypothesis, "--operator", "synthetic-operator",
		"--note", "refine the second clause"); code != exitUsage {
		t.Errorf("--note was accepted; exit %d, want %d", code, exitUsage)
	}
	if _, _, code := f.run("invite", ids.hypothesis); code != exitUsage {
		t.Errorf("an unattributed invitation exited %d, want %d", code, exitUsage)
	}
}

// TestDispositionLoopRecordsAndPublishesNothing walks propose, list, accept
// and show, and checks the one property every one of them shares: the record
// says what an operator decided and nothing left the machine.
func TestDispositionLoopRecordsAndPublishesNothing(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.ok("disposition", "propose", ids.finding,
		"--kind", "develop-further", "--summary", "search the other repositories",
		"--rationale", "the same handoff appears in two more", "--operator", "synthetic-operator", "--json")
	proposed := decodeJSON[dispositionResult](t, stdout)
	if proposed.Disposition.Status != "proposed" {
		t.Fatalf("a fresh action is %q, want proposed", proposed.Disposition.Status)
	}
	if proposed.Disposition.ProposedBy != "operator synthetic-operator" {
		t.Errorf("proposed by %q", proposed.Disposition.ProposedBy)
	}
	assertNoRawControls(t, "disposition propose --json", stdout, stderr)

	stdout, _ = f.ok("dispositions", "--json")
	listed := decodeJSON[dispositionsResult](t, stdout)
	if listed.Total != 1 || listed.Dispositions[0].ID != proposed.Disposition.ID {
		t.Fatalf("listing = %+v", listed)
	}

	stdout, stderr = f.ok("disposition", "accept", proposed.Disposition.ID,
		"--operator", "synthetic-operator", "--note", "worth the budget", "--json")
	decided := decodeJSON[decideDispositionResult](t, stdout)
	if decided.Status != "accepted" {
		t.Fatalf("state after accept = %q", decided.Status)
	}
	if decided.Entry.By != "synthetic-operator" {
		t.Errorf("the entry is attributed to %q", decided.Entry.By)
	}
	if !strings.Contains(decided.Published, "nothing") {
		t.Errorf("the result does not state that nothing was published: %q", decided.Published)
	}
	assertNoRawControls(t, "disposition accept --json", stdout, stderr)

	// Reconsidering appends rather than replacing, which is what #88 reads
	// back as provenance.
	f.ok("disposition", "decline", proposed.Disposition.ID, "--operator", "synthetic-operator", "--json")
	stdout, _ = f.ok("disposition", "show", proposed.Disposition.ID, "--json")
	shown := decodeJSON[dispositionResult](t, stdout)
	if len(shown.Ledger) != 2 {
		t.Fatalf("ledger has %d entries, want both decisions", len(shown.Ledger))
	}
	if shown.Ledger[0].Ruling != "accepted" || shown.Ledger[1].Ruling != "declined" {
		t.Errorf("ledger = %+v, want accepted then declined", shown.Ledger)
	}
	if shown.Disposition.Status != "declined" {
		t.Errorf("state = %q, want the latest ruling", shown.Disposition.Status)
	}

	if _, _, code := f.run("disposition", "accept", proposed.Disposition.ID); code != exitUsage {
		t.Errorf("an unattributed acceptance exited %d, want %d", code, exitUsage)
	}
}

// TestDraftIssueBindsOnlyToAVerifiedCheckout is issue #88's anchoring rule at
// the command surface. The repository is a real checkout created for this
// test, and the refusals are the two ways a draft could otherwise name a
// repository nobody can point at.
func TestDraftIssueBindsOnlyToAVerifiedCheckout(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	repo := testCheckout(t, "git@github.com:atyrode/babel")

	stdout, _ := f.ok("disposition", "propose", ids.proposal,
		"--kind", "draft-issue", "--summary", "re-read the manifest per deploy",
		"--repo", repo, "--operator", "synthetic-operator", "--json")
	proposed := decodeJSON[dispositionResult](t, stdout)
	if proposed.Disposition.Anchor == nil {
		t.Fatal("the draft carries no repository anchor")
	}
	if proposed.Disposition.Anchor.URL != "git@github.com:atyrode/babel" {
		t.Errorf("anchor url = %q", proposed.Disposition.Anchor.URL)
	}

	stdout, stderr := f.ok("disposition", "show", proposed.Disposition.ID, "--json")
	shown := decodeJSON[dispositionResult](t, stdout)
	if !strings.Contains(shown.Draft, "published nothing") {
		t.Errorf("the rendered draft does not say Babel published nothing:\n%s", shown.Draft)
	}
	assertNoRawControls(t, "disposition show --json", stdout, stderr)

	if _, _, code := f.run("disposition", "propose", ids.proposal,
		"--kind", "draft-issue", "--summary", "unanchored",
		"--operator", "synthetic-operator"); code != exitUsage {
		t.Errorf("an unanchored draft exited %d, want %d", code, exitUsage)
	}
	if _, _, code := f.run("disposition", "propose", ids.proposal,
		"--kind", "draft-issue", "--summary", "hallucinated",
		"--repo", filepath.Join(t.TempDir(), "no-such-repository"),
		"--operator", "synthetic-operator"); code != exitFailure {
		t.Errorf("a draft aimed at a repository that is not there exited %d, want %d", code, exitFailure)
	}
	if _, _, code := f.run("disposition", "propose", ids.proposal,
		"--kind", "ask-question", "--summary", "who owns deploys?",
		"--repo", repo, "--operator", "synthetic-operator"); code != exitUsage {
		t.Errorf("a non-draft action accepted --repo; exit %d, want %d", code, exitUsage)
	}
}

// testCheckout builds a real git checkout for the anchoring cases. It is git's
// own output rather than hand-written files, so a change in what git stores
// fails the test instead of the operator's next draft.
func testCheckout(t *testing.T, origin string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main", "."},
		{"remote", "add", "origin", origin},
	} {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}
