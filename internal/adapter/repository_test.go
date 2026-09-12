package adapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNormalizeRemoteCollapsesOneRepositoryToOneIdentity is the claim topics
// rest on: the four ways git writes one GitHub repository are one topic.
//
// They are four because git's grammar makes them four — the ssh short form,
// the ssh URL, https, and https with a token a credential helper wrote — and
// a surface that treated them as four would file the same project under four
// names on the operator's own machine.
func TestNormalizeRemoteCollapsesOneRepositoryToOneIdentity(t *testing.T) {
	const want = "github.com/atyrode/manifold"
	for _, url := range []string{
		"git@github.com:atyrode/manifold.git",
		"git@github.com:atyrode/manifold",
		"ssh://git@github.com/atyrode/manifold.git",
		"ssh://git@github.com:22/atyrode/manifold.git",
		"https://github.com/atyrode/manifold",
		"https://github.com/atyrode/manifold.git",
		"https://github.com/atyrode/manifold.git/",
		"https://ghp_secret@github.com/atyrode/manifold.git",
		"https://user:password@github.com/atyrode/manifold.git/",
		"  https://github.com/atyrode/manifold.git  ",
	} {
		if got := NormalizeRemote(url); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestNormalizeRemoteRefusesWhatIsNotARepositoryIdentity keeps the identity
// honest. A path remote names a directory on one machine, which is the
// locator §4.13 says is never the topic, and the common directory is already
// the better answer for it.
func TestNormalizeRemoteRefusesWhatIsNotARepositoryIdentity(t *testing.T) {
	for _, url := range []string{
		"",
		"   ",
		"/srv/git/thing.git",
		"../sibling",
		"./here",
		"file:///srv/git/thing.git",
		"https://github.com/",
		"https://github.com",
	} {
		if got := NormalizeRemote(url); got != "" {
			t.Errorf("NormalizeRemote(%q) = %q, want no identity", url, got)
		}
	}
}

// TestNormalizeRemoteKeepsASelfHostedRemote is why the rule is "a host and at
// least one element" rather than literally three: a repository served from
// git.example.com/tools.git has no owner segment and is still one repository,
// and refusing it would file a whole self-hosted project under nothing.
func TestNormalizeRemoteKeepsASelfHostedRemote(t *testing.T) {
	for url, want := range map[string]string{
		"https://git.example.com/tools.git":  "git.example.com/tools",
		"git@git.example.com:tools.git":      "git.example.com/tools",
		"https://gitlab.com/group/sub/thing": "gitlab.com/group/sub/thing",
		"ssh://git@localhost/srv/thing.git":  "localhost/srv/thing",
	} {
		if got := NormalizeRemote(url); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestObserveCollapsesAWorktreeIntoItsRepository is the whole point of
// binding a topic to the common directory: `~/.local/state/code/wt/
// witty-sage-crab` and the checkout it was branched from are two workspaces
// and one project, and the operator's feed had a topic for each.
func TestObserveCollapsesAWorktreeIntoItsRepository(t *testing.T) {
	main := gitFixture(t, "git@github.com:atyrode/manifold.git")
	worktree := filepath.Join(t.TempDir(), "witty-sage-crab")
	runGitFixture(t, main, "worktree", "add", "-b", "feature", worktree)

	observer := NewRepositoryObserver()
	fromMain := observer.Observe(t.Context(), &main)
	fromWorktree := observer.Observe(t.Context(), &worktree)

	if fromMain.Identity == "" {
		t.Fatalf("the main checkout has no identity: %+v", fromMain)
	}
	if fromMain.Identity != fromWorktree.Identity {
		t.Errorf("worktree identity = %q, want the repository's %q",
			fromWorktree.Identity, fromMain.Identity)
	}
	if fromWorktree.Remote != "github.com/atyrode/manifold" {
		t.Errorf("worktree remote = %q, want the repository's", fromWorktree.Remote)
	}
	if fromMain.Reason != "" || fromWorktree.Reason != "" {
		t.Errorf("an observed repository carries a reason: %+v %+v", fromMain, fromWorktree)
	}
	// The identity is the common directory, which is inside the main
	// checkout: that is what makes the parent of it the repository's name.
	if base := filepath.Base(fromMain.Identity); base != ".git" {
		t.Errorf("identity = %q, want the repository's common directory", fromMain.Identity)
	}
}

// TestObserveExplainsEveryAbsentIdentity is SPEC.md §3 applied to this
// column: a session that files under no topic says why, so "unfiled" is a
// state the operator can read rather than an empty cell.
func TestObserveExplainsEveryAbsentIdentity(t *testing.T) {
	plain := t.TempDir()
	absent := filepath.Join(t.TempDir(), "deleted-yesterday")
	empty := ""
	observer := NewRepositoryObserver()

	for _, tc := range []struct {
		name      string
		workspace *string
		want      string
	}{
		{"not a repository", &plain, ReasonNotARepository},
		{"absent workspace", &absent, ReasonWorkspaceAbsent},
		{"no workspace recorded", nil, ReasonNoWorkspace},
		{"empty workspace", &empty, ReasonNoWorkspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := observer.Observe(t.Context(), tc.workspace)
			if observed.Identity != "" || observed.Remote != "" {
				t.Fatalf("an unobservable workspace produced an identity: %+v", observed)
			}
			if observed.Reason != tc.want {
				t.Errorf("reason = %q, want %q", observed.Reason, tc.want)
			}
		})
	}
}

// TestObserveReportsARepositoryWithNoRemote keeps a local-only repository a
// repository. Nothing about a project the operator never published makes it
// less of a topic, and the common directory identifies it.
func TestObserveReportsARepositoryWithNoRemote(t *testing.T) {
	local := gitFixture(t, "")
	observed := NewRepositoryObserver().Observe(t.Context(), &local)
	if observed.Identity == "" {
		t.Fatalf("a repository with no origin was not observed: %+v", observed)
	}
	if observed.Remote != "" || observed.Reason != "" {
		t.Errorf("a repository with no origin invented one: %+v", observed)
	}
}

// TestObserveAsksGitOncePerWorkspace is the scan's cost contract: a machine
// with thousands of sessions in a handful of checkouts must not run two
// subprocesses per session.
func TestObserveAsksGitOncePerWorkspace(t *testing.T) {
	repo := gitFixture(t, "git@github.com:atyrode/babel.git")
	observer := NewRepositoryObserver()
	first := observer.Observe(t.Context(), &repo)

	// Moving the checkout out from under the observer proves the second
	// answer came from the cache: a second probe would find nothing.
	moved := repo + "-moved"
	if err := os.Rename(repo, moved); err != nil {
		t.Fatalf("move the fixture: %v", err)
	}
	second := observer.Observe(t.Context(), &repo)
	if second != first {
		t.Errorf("second observation = %+v, want the cached %+v", second, first)
	}
}

// TestObserveWithoutGitSaysSo keeps a machine with no git working: every
// session files under no topic, with the reason that says why, and nothing
// fails.
func TestObserveWithoutGitSaysSo(t *testing.T) {
	repo := gitFixture(t, "git@github.com:atyrode/babel.git")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	observed := NewRepositoryObserver().Observe(t.Context(), &repo)
	if observed.Identity != "" {
		t.Fatalf("git was found on an empty PATH: %+v", observed)
	}
	if observed.Reason != ReasonGitUnavailable {
		t.Errorf("reason = %q, want %q", observed.Reason, ReasonGitUnavailable)
	}
}

// TestObserveNeverWritesToTheWorkspace is the read-only guarantee stated as a
// test: observing where work happened must not disturb the work. Every file
// under the checkout, including git's own index and logs, is compared before
// and after.
func TestObserveNeverWritesToTheWorkspace(t *testing.T) {
	repo := gitFixture(t, "git@github.com:atyrode/babel.git")
	before := treeState(t, repo)
	if observed := NewRepositoryObserver().Observe(t.Context(), &repo); observed.Identity == "" {
		t.Fatalf("the fixture was not observed: %+v", observed)
	}
	if after := treeState(t, repo); after != before {
		t.Errorf("observation modified the workspace:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// treeState is every path under root with its size and modification time, as
// one comparable string.
func treeState(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		b.WriteString("\t")
		if !info.IsDir() {
			b.WriteString(info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000"))
			b.WriteString("\t")
			b.WriteString(info.Mode().String())
			b.WriteString("\t")
			b.WriteString(strconv.FormatInt(info.Size(), 10))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}

// gitFixture is a real git repository, because the identity this package
// derives comes out of git's own answers and a hand-built .git directory
// would only prove this test agrees with itself.
func gitFixture(t *testing.T, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create the fixture directory: %v", err)
	}
	runGitFixture(t, dir, "init", "--initial-branch=main", ".")
	if origin != "" {
		runGitFixture(t, dir, "remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write the fixture file: %v", err)
	}
	runGitFixture(t, dir, "add", "README.md")
	runGitFixture(t, dir, "commit", "-m", "fixture")
	return dir
}

// runGitFixture runs one git command in a hermetic environment: the
// operator's own configuration must not decide what the fixture looks like.
func runGitFixture(t *testing.T, dir string, args ...string) {
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
