package disposition

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// gitRepo builds a real checkout with git itself rather than by writing the
// files this package parses.
//
// Writing them by hand would make the test agree with the parser by
// construction: both would be this author's idea of what git stores, and issue
// #88's claim is that a draft binds to what git actually wrote. Any drift —
// a default branch that is no longer master, a config layout that changed —
// shows up here instead of on the day an operator's draft names nothing.
func gitRepo(t *testing.T, origin string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		// A hermetic environment: the operator's own git configuration must
		// not decide what this fixture looks like.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=babel-test",
			"GIT_AUTHOR_EMAIL=babel@example.invalid",
			"GIT_COMMITTER_NAME=babel-test",
			"GIT_COMMITTER_EMAIL=babel@example.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch=main", ".")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "fixture")
	return dir
}

// TestVerifyAnchorReadsARealCheckout is issue #88's core claim made testable:
// what a draft binds to comes out of a checkout that exists on this machine.
func TestVerifyAnchorReadsARealCheckout(t *testing.T) {
	const origin = "git@github.com:atyrode/babel"
	dir := gitRepo(t, origin)

	anchor, err := VerifyAnchor(dir)
	if err != nil {
		t.Fatalf("verify anchor: %v", err)
	}
	if anchor.URL != origin {
		t.Errorf("anchor url = %q, want %q", anchor.URL, origin)
	}
	if anchor.Remote != "origin" {
		t.Errorf("anchor remote = %q, want origin", anchor.Remote)
	}
	if anchor.Branch != "main" {
		t.Errorf("anchor branch = %q, want main", anchor.Branch)
	}
	// The workspace is absolute so a draft rendered later names the file
	// that authorized it rather than a path relative to whoever rendered it.
	if !filepath.IsAbs(anchor.Workspace) {
		t.Errorf("anchor workspace %q is not absolute", anchor.Workspace)
	}
	resolved, err := filepath.EvalSymlinks(anchor.Workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	wanted, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	if resolved != wanted {
		t.Errorf("anchor workspace = %q, want %q", resolved, wanted)
	}
}

// TestVerifyAnchorRefusesWhatItCannotProve is the property that makes a
// hallucinated repository structurally impossible. Each case is a way a
// caller could have named a repository nobody can point at.
func TestVerifyAnchorRefusesWhatItCannotProve(t *testing.T) {
	t.Run("a directory that is not a checkout", func(t *testing.T) {
		if _, err := VerifyAnchor(t.TempDir()); !errors.Is(err, ErrNoRepository) {
			t.Fatalf("got %v, want ErrNoRepository", err)
		}
	})
	t.Run("a directory that does not exist", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "no-such-repository")
		if _, err := VerifyAnchor(absent); !errors.Is(err, ErrNoRepository) {
			t.Fatalf("got %v, want ErrNoRepository", err)
		}
	})
	t.Run("no workspace at all", func(t *testing.T) {
		if _, err := VerifyAnchor(""); !errors.Is(err, ErrNoRepository) {
			t.Fatalf("got %v, want ErrNoRepository", err)
		}
	})
	t.Run("a checkout with no origin remote", func(t *testing.T) {
		// A local-only repository is a real checkout, so existence alone is
		// not the test: without an origin there is no repository to bind a
		// public draft to.
		if _, err := VerifyAnchor(gitRepo(t, "")); !errors.Is(err, ErrNoRepository) {
			t.Fatalf("got %v, want ErrNoRepository", err)
		}
	})
	t.Run("a .git file pointing nowhere", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"),
			[]byte("gitdir: /nonexistent/git/dir\n"), 0o600); err != nil {
			t.Fatalf("write .git pointer: %v", err)
		}
		if _, err := VerifyAnchor(dir); !errors.Is(err, ErrNoRepository) {
			t.Fatalf("got %v, want ErrNoRepository", err)
		}
	})
}

// TestVerifyAnchorFollowsAWorktree covers the layout Babel's own sessions run
// in most often. A linked worktree keeps its HEAD beside itself and shares the
// repository's configuration, so reading the config from the worktree's own
// directory would refuse exactly the checkouts #88 cares about.
func TestVerifyAnchorFollowsAWorktree(t *testing.T) {
	const origin = "git@github.com:atyrode/babel"
	main := gitRepo(t, origin)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	cmd := exec.Command(git, "worktree", "add", "-b", "side", linked)
	cmd.Dir = main
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	anchor, err := VerifyAnchor(linked)
	if err != nil {
		t.Fatalf("verify worktree anchor: %v", err)
	}
	if anchor.URL != origin {
		t.Errorf("worktree anchor url = %q, want %q", anchor.URL, origin)
	}
	if anchor.Branch != "side" {
		t.Errorf("worktree anchor branch = %q, want side", anchor.Branch)
	}
}

// TestDraftIssueRequiresAVerifiedAnchor pins where the rule is enforced: at
// the store, not at the caller. A draft-issue with no anchor cannot be written
// at all, and every other kind is refused an anchor it has no use for.
func TestDraftIssueRequiresAVerifiedAnchor(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("the deploy script re-reads a stale manifest")
	anchor, err := VerifyAnchor(gitRepo(t, "git@github.com:atyrode/babel"))
	if err != nil {
		t.Fatalf("verify anchor: %v", err)
	}

	if _, err := h.store.Propose(h.ctx, ProposeInput{
		Record: record, Kind: KindDraftIssue, ProposedBy: frontier.Operator("alex"),
		Payload: Payload{Summary: "re-read the manifest per deploy"},
	}); !errors.Is(err, ErrAnchorRequired) {
		t.Fatalf("unanchored draft: got %v, want ErrAnchorRequired", err)
	}
	if _, err := h.store.Propose(h.ctx, ProposeInput{
		Record: record, Kind: KindAskQuestion, ProposedBy: frontier.Operator("alex"),
		Payload: Payload{Summary: "who owns deploys?", Anchor: &anchor},
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("anchored question: got %v, want ErrInvalidValue", err)
	}

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record: record, Kind: KindDraftIssue, ProposedBy: frontier.Operator("alex"),
		Payload: Payload{
			Summary:   "re-read the manifest per deploy",
			Rationale: "three sessions show the same stale read",
			Anchor:    &anchor,
		},
	})
	if err != nil {
		t.Fatalf("anchored draft: %v", err)
	}
	reread, err := h.store.Disposition(h.ctx, action.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if reread.Payload.Anchor == nil || reread.Payload.Anchor.URL != anchor.URL {
		t.Fatalf("the anchor did not round-trip: %+v", reread.Payload.Anchor)
	}

	draft, err := Draft(reread)
	if err != nil {
		t.Fatalf("render draft: %v", err)
	}
	for _, want := range []string{
		reread.Payload.Summary,
		anchor.URL,
		anchor.Branch,
		record.ID,
		"published nothing",
	} {
		if !strings.Contains(draft, want) {
			t.Errorf("the draft omits %q:\n%s", want, draft)
		}
	}
}
