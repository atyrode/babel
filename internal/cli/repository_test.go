package cli

// The scan's repository observation (SPEC.md §4.13), end to end through
// `sessions list`.
//
// It is driven through the command rather than against the observer because
// the claim is about the scan: that the identity is observed after whichever
// adapter described the session, cached in the catalog with the row, and read
// back from the catalog on the next listing. An observer test cannot fail on
// a column that was written and never read.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheScanFilesEveryWorktreeOfARepositoryUnderOneIdentity is the whole of
// stage 1 as the catalog sees it.
//
// Three sessions, three workspaces, one project: a checkout, a worktree of
// it, and a scratch directory that is not a repository at all. The first two
// must arrive at one identity and one remote, and the third must arrive at
// none with the reason that says why — which is what makes a later unfiled
// record readable rather than merely empty.
func TestTheScanFilesEveryWorktreeOfARepositoryUnderOneIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	f := newFixture(t)
	checkout := gitCheckout(t, f.root, "main-checkout", "git@github.com:atyrode/manifold.git")
	worktree := filepath.Join(f.root, "witty-sage-crab")
	runGit(t, checkout, "worktree", "add", "-b", "feature", worktree)
	scratch := filepath.Join(f.root, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}

	f.writeSession(sessionSpec{
		project: "p", stem: "in-checkout", id: "s-checkout",
		title: "work in the checkout", workspace: checkout,
	})
	f.writeSession(sessionSpec{
		project: "p", stem: "in-worktree", id: "s-worktree",
		title: "work in the worktree", workspace: worktree,
	})
	f.writeSession(sessionSpec{
		project: "p", stem: "in-scratch", id: "s-scratch",
		title: "work in a scratch directory", workspace: scratch,
	})

	stdout, _ := f.ok("sessions", "list", "--json")
	rows := bySourceStem(t, decode[sessionsResult](t, stdout))

	inCheckout, inWorktree, inScratch := rows["in-checkout"], rows["in-worktree"], rows["in-scratch"]
	if inCheckout.RepositoryIdentity == nil || inWorktree.RepositoryIdentity == nil {
		t.Fatalf("a session in a checkout has no repository: %+v %+v", inCheckout, inWorktree)
	}
	if *inCheckout.RepositoryIdentity != *inWorktree.RepositoryIdentity {
		t.Errorf("the worktree resolves to %q and its repository to %q; they are one repository",
			*inWorktree.RepositoryIdentity, *inCheckout.RepositoryIdentity)
	}
	for stem, row := range map[string]sessionRow{"in-checkout": inCheckout, "in-worktree": inWorktree} {
		if row.RepositoryRemote == nil || *row.RepositoryRemote != "github.com/atyrode/manifold" {
			t.Errorf("%s remote = %v, want the normalized origin", stem, row.RepositoryRemote)
		}
		if row.RepositoryReason != nil {
			t.Errorf("%s carries both a repository and a reason: %v", stem, *row.RepositoryReason)
		}
	}
	if inScratch.RepositoryIdentity != nil || inScratch.RepositoryRemote != nil {
		t.Errorf("a scratch directory produced a repository: %+v", inScratch)
	}
	if inScratch.RepositoryReason == nil || *inScratch.RepositoryReason != "not a git repository" {
		t.Errorf("scratch reason = %v, want the absence explained", inScratch.RepositoryReason)
	}

	// The second listing is served from the catalog rather than from a
	// fresh describe, so it proves the columns round-trip: a value observed
	// once and lost on the way to SQLite would make the feed's topics
	// depend on whether a session had changed since the last scan.
	cachedOut, _ := f.ok("sessions", "list", "--json")
	cached := bySourceStem(t, decode[sessionsResult](t, cachedOut))["in-worktree"]
	if cached.RepositoryIdentity == nil || *cached.RepositoryIdentity != *inWorktree.RepositoryIdentity {
		t.Errorf("cached identity = %v, want the observed %q", cached.RepositoryIdentity, *inWorktree.RepositoryIdentity)
	}
	if cached.RepositoryRemote == nil || *cached.RepositoryRemote != "github.com/atyrode/manifold" {
		t.Errorf("cached remote = %v, want the observed one", cached.RepositoryRemote)
	}
}

// bySourceStem indexes a listing by the fixture's own session stems, which is
// how a test names the row it planted.
func bySourceStem(t *testing.T, listed sessionsResult) map[string]sessionRow {
	t.Helper()
	out := make(map[string]sessionRow, len(listed.Sessions))
	for _, row := range listed.Sessions {
		out[row.SourceID[strings.LastIndexByte(row.SourceID, '/')+1:]] = row
	}
	return out
}

// gitCheckout is a real repository under dir, because what the scan records
// comes out of git's own answers.
func gitCheckout(t *testing.T, dir, name, origin string) string {
	t.Helper()
	checkout := filepath.Join(dir, name)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "init", "--initial-branch=main", ".")
	runGit(t, checkout, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "README.md")
	runGit(t, checkout, "commit", "-m", "fixture")
	return checkout
}

// runGit runs one git command hermetically: the fixture must not depend on
// the developer's own git configuration.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
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
