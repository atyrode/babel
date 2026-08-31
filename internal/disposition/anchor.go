package disposition

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Anchor is the repository a draft-issue disposition binds to, as read out of
// a local checkout's own git configuration (issue #88).
//
// The point is structural rather than defensive. A model asked to name a
// repository can name one that does not exist, or one that does and is not the
// operator's; a draft that binds only to what a checkout on this machine says
// its origin is cannot name either. Babel holds no GitHub credential and makes
// no network call to check — existence is proven by the checkout being here,
// which is a stronger claim than an API answering, because it also proves the
// operator works on it.
type Anchor struct {
	// Workspace is the checkout the configuration was read from. It is an
	// absolute path, so a draft rendered later can be traced to the file
	// that authorized it.
	Workspace string `json:"workspace"`
	// Remote is the remote name the URL came from. It is always "origin":
	// the field exists so a stored anchor says which remote was believed
	// rather than leaving a reader to assume.
	Remote string `json:"remote"`
	// URL is the origin URL verbatim, in whatever form git stores it —
	// ssh, https, or a local path. It is not normalized to an owner/repo
	// pair: normalizing would be Babel guessing at a hosting provider's
	// URL grammar, and a draft that named the wrong repository because a
	// guess was close is exactly what this type prevents.
	URL string `json:"url"`
	// Branch is the checked-out branch, empty for a detached HEAD. #88 asks
	// for origin URL, branch, and existence; a detached HEAD is an honest
	// "no branch" rather than a refusal, because the repository is still
	// verified and the branch is not what a draft binds to.
	Branch string `json:"branch,omitempty"`
}

func (a Anchor) validate() error {
	switch {
	case a.Workspace == "":
		return fmt.Errorf("%w: repository anchor names no workspace", ErrInvalidValue)
	case a.Remote == "":
		return fmt.Errorf("%w: repository anchor names no remote", ErrInvalidValue)
	case a.URL == "":
		return fmt.Errorf("%w: repository anchor has no url", ErrInvalidValue)
	}
	return nil
}

// originRemote is the only remote a draft may bind to. A checkout can carry
// any number of remotes, several of them somebody else's fork, and letting a
// caller pick which one to trust would hand back the choice #88 removes.
const originRemote = "origin"

// VerifyAnchor reads a repository anchor out of the git checkout at dir.
//
// It refuses everything it cannot prove: a directory that is not a checkout, a
// checkout with no origin remote, and a git file layout it cannot read. There
// is no fallback that infers a repository from a directory name, because an
// inferred anchor is the hallucinated target with extra steps.
//
// git is never executed. The three files this needs — the gitdir pointer, the
// config, and HEAD — are stable, documented, plain text, and reading them
// keeps a draft-issue proposal from being able to run a subprocess with a
// path a model chose.
func VerifyAnchor(dir string) (Anchor, error) {
	if dir == "" {
		return Anchor{}, fmt.Errorf("%w: no workspace given", ErrNoRepository)
	}
	workspace, err := filepath.Abs(dir)
	if err != nil {
		return Anchor{}, fmt.Errorf("resolve workspace %q: %w", dir, err)
	}
	gitDir, commonDir, err := resolveGitDir(workspace)
	if err != nil {
		return Anchor{}, err
	}
	url, err := originURL(filepath.Join(commonDir, "config"))
	if err != nil {
		return Anchor{}, err
	}
	branch, err := headBranch(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return Anchor{}, err
	}
	return Anchor{Workspace: workspace, Remote: originRemote, URL: url, Branch: branch}, nil
}

// resolveGitDir finds a checkout's git directory and the common directory its
// configuration lives in, following both indirections git uses.
//
// A linked worktree stores "gitdir: PATH" in a .git file, and the directory it
// points at holds that worktree's own HEAD but not the repository's config —
// the config is shared, and a commondir file says where. Worktrees matter here
// rather than being an edge case: Babel's own analysis of Babel runs against
// sessions whose workspaces are frequently worktrees, so refusing them would
// refuse anchors for exactly the repository #88 is most interested in.
//
// The two paths are the same directory for an ordinary checkout, which is why
// they are returned separately rather than collapsed: HEAD is per worktree and
// config is per repository, and reading either from the wrong one gives an
// answer that looks right on a machine with no worktrees.
func resolveGitDir(workspace string) (gitDir, commonDir string, err error) {
	git := filepath.Join(workspace, ".git")
	info, err := os.Stat(git)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "", fmt.Errorf("%w: %s is not a git checkout", ErrNoRepository, workspace)
	case err != nil:
		return "", "", fmt.Errorf("read %s: %w", git, err)
	case info.IsDir():
		return git, git, nil
	}
	raw, err := os.ReadFile(git)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", git, err)
	}
	pointer, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", "", fmt.Errorf("%w: %s is a file that does not point at a git directory", ErrNoRepository, git)
	}
	gitDir = strings.TrimSpace(pointer)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workspace, gitDir)
	}
	if _, err := os.Stat(gitDir); err != nil {
		return "", "", fmt.Errorf("%w: %s points at %s, which is not there", ErrNoRepository, git, gitDir)
	}
	common, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		// No commondir means this is not a linked worktree's directory
		// but a plain git directory somewhere else, so it owns its own
		// configuration.
		return gitDir, gitDir, nil
	}
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", filepath.Join(gitDir, "commondir"), err)
	}
	commonDir = strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return gitDir, filepath.Clean(commonDir), nil
}

// originURL reads the origin remote's URL out of a git config file.
//
// The parser is deliberately small and deliberately strict. git's config
// grammar has more in it than this reads — includes, conditional includes,
// subsection quoting rules — and a parser that guessed at the rest would be a
// second, worse implementation of git's own. What it understands is the shape
// git itself writes: a `[remote "origin"]` header and a `url =` line under it.
// Anything else means the anchor could not be proven, which is a refusal.
func originURL(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A git directory with no config is the shape a linked worktree's
		// own directory has; the caller is expected to have followed
		// commondir already, so reaching here means the checkout is not
		// one this can read.
		return "", fmt.Errorf("%w: %s has no git config", ErrNoRepository, filepath.Dir(path))
	}
	if err != nil {
		return "", fmt.Errorf("read git config: %w", err)
	}
	defer file.Close()

	inOrigin := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(line, `[remote "`+originRemote+`"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		if url := strings.TrimSpace(value); url != "" {
			return url, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read git config: %w", err)
	}
	return "", fmt.Errorf("%w: %s declares no %s url", ErrNoRepository, path, originRemote)
}

// headBranch reads the checked-out branch, reporting "" for a detached HEAD.
func headBranch(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s has no HEAD", ErrNoRepository, filepath.Dir(path))
	}
	if err != nil {
		return "", fmt.Errorf("read git HEAD: %w", err)
	}
	head := strings.TrimSpace(string(raw))
	ref, ok := strings.CutPrefix(head, "ref:")
	if !ok {
		return "", nil
	}
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"), nil
}

// Draft renders a draft-issue disposition as the markdown an operator would
// paste, and returns it as a string rather than writing it anywhere.
//
// The rendering is minimal on purpose. #87 keeps publishing out of scope and
// #88 puts publication operator-side under their own credentials, so what this
// owes the operator is the proposal's own words plus the provenance they need
// to judge it — which record, which repository, which branch — and not a
// simulation of an issue form. A richer template would be a place for Babel to
// start sounding like it filed something.
func Draft(d Disposition) (string, error) {
	if d.Kind != KindDraftIssue {
		return "", fmt.Errorf("%w: %s is not a draft-issue disposition", ErrInvalidValue, d.Kind)
	}
	if d.Payload.Anchor == nil {
		return "", ErrAnchorRequired
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", d.Payload.Summary)
	if d.Payload.Rationale != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Payload.Rationale)
	}
	fmt.Fprintf(&b, "Repository: %s\n", d.Payload.Anchor.URL)
	if d.Payload.Anchor.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", d.Payload.Anchor.Branch)
	}
	fmt.Fprintf(&b, "Verified against: %s\n", d.Payload.Anchor.Workspace)
	fmt.Fprintf(&b, "Babel record: %s %s\n", d.Record.Type, d.Record.ID)
	fmt.Fprintf(&b, "Disposition: %s\n\n", d.ID)
	b.WriteString("Babel drafted this and published nothing. Filing it is the operator's act, under their own credentials.\n")
	return b.String(), nil
}
