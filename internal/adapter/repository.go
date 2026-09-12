package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Repository is the repository identity observed for one session workspace:
// what the work was about, as opposed to where it happened (SPEC.md §4.13).
//
// A workspace path is a locator. Two worktrees of one repository are two
// paths and one project, a path under /tmp names nothing durable, and a
// generated worktree name ("witty-sage-crab") is a directory rather than a
// subject. The repository's own identity is the thing that survives all
// three, so it is what Babel observes and files under.
//
// Exactly one of Identity and Reason is set. Identity present means the
// observation succeeded; Reason present means it did not and says why, which
// is §3's rule that an absent value is explained rather than synthesized. A
// Reason is never a guessed identity, and Remote is empty whenever the
// checkout declares no origin — a repository nobody published is still one
// repository.
type Repository struct {
	// Identity is the absolute path of the repository's git common
	// directory. It is the identity rather than the remote because every
	// worktree of a repository shares exactly one common directory, while a
	// remote can be absent, renamed, or shared by a fork.
	Identity string
	// Remote is the origin URL normalized to host/owner/repo — no scheme, no
	// credentials, no ".git", no trailing slash — and empty when the
	// checkout has no origin. It is what makes the same repository cloned to
	// two paths on two machines recognisable as one.
	Remote string
	// Reason explains an absent Identity in the operator's language, and is
	// one of the reasons below.
	Reason string
}

// The reasons an identity is absent. They are the whole vocabulary: each
// names a state of this machine, none names a fault of the session.
const (
	// ReasonNotARepository is a workspace that exists and is not under git.
	ReasonNotARepository = "not a git repository"
	// ReasonWorkspaceAbsent is a workspace this host does not hold — a
	// session fetched from another machine, or a directory since deleted.
	ReasonWorkspaceAbsent = "workspace absent on this host"
	// ReasonNoWorkspace is a session whose harness recorded no workspace at
	// all, so there is nothing to observe.
	ReasonNoWorkspace = "session records no workspace"
	// ReasonGitUnavailable is this machine having no git to ask.
	ReasonGitUnavailable = "git unavailable"
)

// repositoryProbeTimeout bounds one git invocation. Both probes read local
// metadata and answer in milliseconds; the bound exists so that a workspace
// on an unresponsive network mount costs a scan one second rather than
// stalling it, and a timeout is reported as an absent identity like any other
// failed observation.
const repositoryProbeTimeout = time.Second

// RepositoryObserver observes the repository identity of session workspaces,
// once per distinct workspace.
//
// The cache is the reason this is a type rather than a function: a scan
// describes thousands of sessions and the operator's machine holds tens of
// workspaces, so observing per session would run two subprocesses per session
// to re-derive an answer the scan already has. One observer lives for one
// scan, which is also what keeps the answer internally consistent: every row
// a scan writes saw the same state of the disk.
//
// Every probe is read-only. `rev-parse` and `remote get-url` read git's own
// metadata; nothing here runs a command that touches the index, the worktree,
// or the network, because observing where a session's work happened must
// never modify it.
type RepositoryObserver struct {
	mu       sync.Mutex
	observed map[string]Repository
	// git is the resolved git binary, and gitMissing records that PATH
	// holds none. Resolution happens once: a machine without git answers
	// every workspace with the same reason instead of paying a PATH walk
	// per workspace.
	git        string
	gitMissing bool
	resolved   bool
}

// NewRepositoryObserver returns an observer with an empty cache.
func NewRepositoryObserver() *RepositoryObserver {
	return &RepositoryObserver{observed: map[string]Repository{}}
}

// Observe reports the repository identity of one workspace. A nil or empty
// workspace, an absent directory, a directory outside git, and a machine
// without git each answer with a reason rather than an identity; nothing here
// returns an error, because a session whose origin cannot be observed is
// still a session and a scan must not fail over one.
func (o *RepositoryObserver) Observe(ctx context.Context, workspace *string) Repository {
	if workspace == nil || strings.TrimSpace(*workspace) == "" {
		return Repository{Reason: ReasonNoWorkspace}
	}
	path := strings.TrimSpace(*workspace)
	o.mu.Lock()
	if found, ok := o.observed[path]; ok {
		o.mu.Unlock()
		return found
	}
	o.mu.Unlock()

	observed := o.observe(ctx, path)

	o.mu.Lock()
	o.observed[path] = observed
	o.mu.Unlock()
	return observed
}

func (o *RepositoryObserver) observe(ctx context.Context, workspace string) Repository {
	git, ok := o.gitPath()
	if !ok {
		return Repository{Reason: ReasonGitUnavailable}
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return Repository{Reason: ReasonWorkspaceAbsent}
	}
	common, ok := runGit(ctx, git, workspace, "rev-parse", "--git-common-dir")
	if !ok || common == "" {
		return Repository{Reason: ReasonNotARepository}
	}
	// git answers relative to the directory it ran in, so a plain checkout
	// reports ".git" and only a linked worktree reports an absolute path.
	// Resolving against the workspace is what collapses both to one identity.
	if !filepath.IsAbs(common) {
		common = filepath.Join(workspace, common)
	}
	common = filepath.Clean(common)
	// Symlinks are resolved because two workspaces reached through different
	// symlinked prefixes are one repository, and a comparison of unresolved
	// paths would file them as two. A common directory that cannot be
	// resolved is used as it stands rather than discarded: the path git
	// printed is still the repository's.
	if real, err := filepath.EvalSymlinks(common); err == nil {
		common = real
	}
	remote, _ := runGit(ctx, git, workspace, "remote", "get-url", "origin")
	return Repository{Identity: common, Remote: NormalizeRemote(remote)}
}

// gitPath resolves the git binary once per observer.
func (o *RepositoryObserver) gitPath() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.resolved {
		path, err := exec.LookPath("git")
		o.git, o.gitMissing, o.resolved = path, err != nil, true
	}
	return o.git, !o.gitMissing
}

// runGit runs one read-only git probe in workspace and returns its first line
// of output. A failure of any kind — git refusing, the directory vanishing,
// the timeout expiring — is reported as no answer, because every caller here
// has a reason to record and nothing to retry.
func runGit(ctx context.Context, git, workspace string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, repositoryProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, append([]string{"-C", workspace}, args...)...)
	// The probe never prompts and never takes a lock: GIT_OPTIONAL_LOCKS=0
	// is what makes this read-only in git's own terms, and a terminal
	// prompt in a background scan would hang it. The operator's own git
	// configuration is left in place deliberately — safe.directory and
	// url.insteadOf are how this machine's git reads this machine's
	// checkouts, and observing through a different configuration would
	// report a repository the operator does not have.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
	)
	cmd.Stdin = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(out))
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	return line, line != ""
}

// NormalizeRemote states a git remote URL as host/owner/repo, and returns ""
// for a URL it cannot read that way.
//
// The normalization is what makes one repository one topic. git's own URL
// grammar writes the same GitHub repository as
// git@github.com:atyrode/manifold.git, https://github.com/atyrode/manifold,
// https://token@github.com/atyrode/manifold.git/ and
// ssh://git@github.com/atyrode/manifold — four strings, one project — so the
// scheme, the credentials, the ".git" suffix and the trailing slash are
// removed and the ssh short form's colon becomes the separator it means.
//
// A local path remote ("/srv/git/thing", "../other") normalizes to nothing:
// it names a directory on one machine, which is a locator and not an
// identity, and the common directory is already the better answer for it.
func NormalizeRemote(url string) string {
	remote := strings.TrimSpace(url)
	if remote == "" {
		return ""
	}
	if scheme := strings.Index(remote, "://"); scheme >= 0 {
		remote = remote[scheme+len("://"):]
	} else if colon := strings.IndexByte(remote, ':'); colon >= 0 && !strings.Contains(remote[:colon], "/") {
		// The scp-like short form, [user@]host:owner/repo. Its colon is a
		// separator rather than a port, which is why it is rewritten here
		// and not for a URL that carried a scheme.
		remote = remote[:colon] + "/" + remote[colon+1:]
	}
	// Credentials in a URL that had a scheme: user[:password]@host.
	if _, rest, found := strings.Cut(remote, "@"); found {
		remote = rest
	}
	remote = strings.Trim(remote, "/")
	if remote == "" || strings.HasPrefix(remote, ".") {
		return ""
	}
	// A local path remote reaches here as an absolute or relative path with
	// no host element; it is refused above by the leading "." or below by
	// having fewer than two elements once cleaned.
	parts := make([]string, 0, 3)
	for _, part := range strings.Split(remote, "/") {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) < 2 {
		return ""
	}
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".git")
	if parts[last] == "" {
		return ""
	}
	// A host element carries a dot or is localhost; anything else is a path,
	// and a path remote is a locator rather than a repository identity.
	host := parts[0]
	if hostPort, _, found := strings.Cut(host, ":"); found {
		host = hostPort
	}
	if !strings.Contains(host, ".") && host != "localhost" {
		return ""
	}
	parts[0] = host
	return strings.Join(parts, "/")
}
