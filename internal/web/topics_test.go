package web

// Topics stage 1 (SPEC.md §4.13), held to the surface the operator read.
//
// The failures these assert are the ones he reported: a topic named "tmp"
// that collected every scratch session, and one topic per generated worktree
// name, so a single repository read as three communities.

import (
	"sort"
	"testing"
)

// sessionIn is one catalog row as the feed sees it: a workspace, and the
// repository this host observed behind it.
func sessionIn(sourceID, workspace, identity, remote string) SessionRow {
	row := SessionRow{Harness: "omp", SourceID: sourceID, Selector: "omp/" + sourceID}
	if workspace != "" {
		row.Workspace = &workspace
	}
	if identity != "" {
		row.RepositoryIdentity = &identity
	}
	if remote != "" {
		row.RepositoryRemote = &remote
	}
	return row
}

func sessionIndex(rows ...SessionRow) map[string][]SessionRow {
	index := map[string][]SessionRow{}
	for _, row := range rows {
		index[row.SourceID] = append(index[row.SourceID], row)
	}
	return index
}

// TestTwoWorktreesOfOneRepositoryAreOneTopic is the direction in one
// assertion: the same project worked on from a checkout and from two
// generated worktrees is one topic, and a session under /tmp is none.
//
// The worktree names are the operator's own — the feed carried topics called
// "witty-sage-crab" and "tmp" — so the test fails on exactly the surface he
// objected to rather than on a paraphrase of it.
func TestTwoWorktreesOfOneRepositoryAreOneTopic(t *testing.T) {
	const identity = "/home/alex/manifold/.git"
	const remote = "github.com/atyrode/manifold"
	main := sessionIn("a", "/home/alex/manifold", identity, remote)
	worktreeOne := sessionIn("b", "/home/alex/.local/state/code/wt/witty-sage-crab", identity, remote)
	worktreeTwo := sessionIn("c", "/home/alex/.local/state/code/wt/bold-gold-koala", identity, remote)
	scratch := sessionIn("d", "/tmp/scratch", "", "")

	for _, row := range []SessionRow{main, worktreeOne, worktreeTwo} {
		if got := topicOf(row); got != "manifold" {
			t.Errorf("session in %s files under %q, want manifold", *row.Workspace, got)
		}
	}
	if got := topicOf(scratch); got != "" {
		t.Errorf("a session under /tmp files under %q, want no topic", got)
	}

	bindings := topicBindings(sessionIndex(main, worktreeOne, worktreeTwo, scratch))
	if len(bindings) != 1 {
		t.Fatalf("bindings = %v, want only manifold", names(bindings))
	}
	binding := bindings["manifold"]
	if binding == nil {
		t.Fatal("manifold has no binding, so the topic names nothing real")
	}
	if binding.Kind != topicKindRepository || binding.Identity != remote {
		t.Errorf("binding = %+v, want the repository %s", binding, remote)
	}
	// The paths are the locators the identity was observed from, and every
	// one of them belongs: they are the evidence for the topic, and the
	// operator judging a filing needs to see what Babel looked at.
	want := []string{
		"/home/alex/.local/state/code/wt/bold-gold-koala",
		"/home/alex/.local/state/code/wt/witty-sage-crab",
		"/home/alex/manifold",
	}
	if len(binding.Paths) != len(want) {
		t.Fatalf("paths = %v, want the three workspaces", binding.Paths)
	}
	for i, path := range want {
		if binding.Paths[i] != path {
			t.Errorf("paths[%d] = %q, want %q", i, binding.Paths[i], path)
		}
	}
}

// TestARepositoryWithNoRemoteIsStillATopic keeps a local-only project a
// project: the common directory is the identity every worktree shares, and
// the checkout it belongs to is the name.
func TestARepositoryWithNoRemoteIsStillATopic(t *testing.T) {
	row := sessionIn("a", "/home/alex/nix-dotfiles", "/home/alex/nix-dotfiles/.git", "")
	if got := topicOf(row); got != "nix-dotfiles" {
		t.Errorf("topic = %q, want nix-dotfiles", got)
	}
	binding := topicBindings(sessionIndex(row))["nix-dotfiles"]
	if binding == nil || binding.Identity != "/home/alex/nix-dotfiles/.git" {
		t.Fatalf("binding = %+v, want the common directory as the identity", binding)
	}
}

// TestTheTopicNameIsTheRepositoryNotTheDirectory is the rule that makes the
// worktree collapse work at all: the name comes off the repository the common
// directory belongs to, never off the workspace the session ran in.
func TestTheTopicNameIsTheRepositoryNotTheDirectory(t *testing.T) {
	for identity, want := range map[string]string{
		"github.com/atyrode/manifold":  "manifold",
		"gitlab.com/group/sub/Thing":   "thing",
		"/home/alex/babel/.git":        "babel",
		"/home/alex/Babel/.git":        "babel",
		"/srv/git/mirrors/tooling.git": "tooling",
		"":                             "",
		"/":                            "",
		".git":                         "",
	} {
		if got := topicName(identity); got != want {
			t.Errorf("topicName(%q) = %q, want %q", identity, got, want)
		}
	}
}

// TestAnAmbiguousNameIsBoundToNothing is §3's rule reaching the sidebar. Two
// unrelated repositories both called "babel" are two real things behind one
// word, and a binding that named one of them would tell the operator this
// topic is a repository it is not.
func TestAnAmbiguousNameIsBoundToNothing(t *testing.T) {
	mine := sessionIn("a", "/home/alex/babel", "/home/alex/babel/.git", "github.com/atyrode/babel")
	theirs := sessionIn("b", "/home/alex/forks/babel", "/home/alex/forks/babel/.git", "github.com/someone/babel")
	bindings := topicBindings(sessionIndex(mine, theirs))
	binding, filed := bindings["babel"]
	if !filed {
		t.Fatal("the ambiguous name is missing from the vocabulary entirely")
	}
	if binding != nil {
		t.Errorf("binding = %+v, want none: two repositories answer to this name", binding)
	}
}

// TestASessionWithNoObservedRepositoryFilesNothing is the honest unfiled
// state: a workspace this host does not hold, or one that is not a checkout,
// contributes no topic rather than a topic named after a path element.
func TestASessionWithNoObservedRepositoryFilesNothing(t *testing.T) {
	for _, row := range []SessionRow{
		sessionIn("a", "/tmp/tmp.Xj19Kd", "", ""),
		sessionIn("b", "/home/alex/.local/state/code/wt/witty-sage-crab", "", ""),
		sessionIn("c", "", "", ""),
	} {
		if got := topicOf(row); got != "" {
			t.Errorf("session %s files under %q, want no topic", row.SourceID, got)
		}
	}
	if bindings := topicBindings(sessionIndex(sessionIn("a", "/tmp/scratch", "", ""))); len(bindings) != 0 {
		t.Errorf("bindings = %v, want none", names(bindings))
	}
}

func names(bindings map[string]*topicBinding) []string {
	out := make([]string, 0, len(bindings))
	for name := range bindings {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
