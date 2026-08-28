package claude

import (
	"slices"
	"strings"

	"github.com/atyrode/babel/internal/adapter"
)

var _ adapter.SnapshotIdentifier = (*Adapter)(nil)

// archivedKey is one session's on-disk location inside a snapshot, in the
// source machine's layout. It is exactly what Discover reads from the
// filesystem — the Claude Code root, the encoded project directory, and the
// session UUID — so the same session identified from a listing and
// discovered from disk lands on the same identity.
type archivedKey struct {
	root    string
	project string
	session string
}

// archivedCandidate accumulates one identified session while the listing is
// scanned: its transcript, plus the session-linked artifact paths the
// listing itself attributes to it.
type archivedCandidate struct {
	primaryPath string
	primarySize int64
	artifacts   []string
}

// IdentifyArchived recognizes Claude Code sessions in a snapshot's file
// listing, without touching the filesystem: the files it names normally
// live on another machine.
//
// A session is one transcript directly inside a project directory —
// <root>/projects/<project>/<session>.jsonl — mirroring Discover, which
// enumerates exactly that level. The identity is derived from the project
// directory name and the transcript stem through the same sourceID helper,
// so a session archived on one host carries the identity the owning host
// assigns it locally.
//
// The closure is what the paths alone can attribute: the transcript plus
// the session-linked artifact trees named after the same session UUID that
// Describe collects — the sibling <session>/ directory inside the project
// directory and <root>/tasks/<session>/ and <root>/session-env/<session>/.
// Claude Code declares no referenced-artifact closure, so no blobs are ever
// part of it and nothing outside those trees is attributed. Dot-prefixed
// artifact entries hold transient state and are skipped here as they are in
// Describe.
//
// Entries belonging to another harness, or to no recognizable Claude Code
// layout, are ignored rather than rejected: one snapshot holds several
// harnesses' trees.
func (*Adapter) IdentifyArchived(files []adapter.ArchivedFile) ([]adapter.ArchivedSession, error) {
	candidates := make(map[archivedKey]*archivedCandidate)
	for _, f := range files {
		segs, ok := archivedSegments(f.Path)
		if !ok {
			continue
		}
		key, ok := archivedPrimaryKey(segs)
		if !ok {
			continue
		}
		if _, dup := candidates[key]; dup {
			continue
		}
		candidates[key] = &archivedCandidate{primaryPath: f.Path, primarySize: f.Size}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// One identity per session, matching Discover's deduplication. Two
	// distinct trees can sanitize onto one identity, so the winner is
	// chosen by transcript path to keep the result independent of the
	// listing's order.
	byID := make(map[string]archivedKey, len(candidates))
	for key, cand := range candidates {
		id := sourceID(key.project, key.session)
		if !adapter.ValidSourceID(id) {
			continue
		}
		if prev, dup := byID[id]; dup && candidates[prev].primaryPath <= cand.primaryPath {
			continue
		}
		byID[id] = key
	}
	if len(byID) == 0 {
		return nil, nil
	}

	// Only the winning trees can receive artifacts. Root-relative artifact
	// trees are named after the session UUID alone, so they are indexed
	// without the project directory and may attribute one tree to several
	// sessions — exactly as Describe collects them per session.
	siblingTrees := make(map[archivedKey]*archivedCandidate, len(byID))
	rootTrees := make(map[[2]string][]*archivedCandidate, len(byID))
	for _, key := range byID {
		cand := candidates[key]
		siblingTrees[key] = cand
		rk := [2]string{key.root, key.session}
		rootTrees[rk] = append(rootTrees[rk], cand)
	}

	for _, f := range files {
		segs, ok := archivedSegments(f.Path)
		if !ok {
			continue
		}
		for _, cand := range archivedArtifactOwners(segs, siblingTrees, rootTrees) {
			cand.artifacts = append(cand.artifacts, f.Path)
		}
	}

	out := make([]adapter.ArchivedSession, 0, len(byID))
	for id, key := range byID {
		cand := candidates[key]
		out = append(out, adapter.ArchivedSession{
			SourceID:    id,
			PrimaryPath: cand.primaryPath,
			PrimarySize: cand.primarySize,
			Files:       archivedFileSet(cand),
		})
	}
	slices.SortFunc(out, func(a, b adapter.ArchivedSession) int {
		return strings.Compare(a.SourceID, b.SourceID)
	})
	return out, nil
}

// archivedSegments splits a snapshot path on "/" — snapshot paths are always
// slash-separated, so nothing here may depend on the host separator — and
// rejects any path a listing cannot attribute: an empty path, a trailing
// slash, a doubled separator, or a "." / ".." segment. Such a path is not
// resolved, because resolving it would invent a location the snapshot never
// recorded. A single leading empty segment is the absolute-path prefix.
func archivedSegments(p string) ([]string, bool) {
	if p == "" {
		return nil, false
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		switch seg {
		case "":
			if i != 0 || len(segs) == 1 {
				return nil, false
			}
		case ".", "..":
			return nil, false
		}
	}
	return segs, true
}

// archivedPrimaryKey reports the session a transcript path names, matching
// Discover's layout rule: the file sits directly inside a project directory
// under a "projects" directory, and its name is a non-empty stem plus the
// transcript extension.
func archivedPrimaryKey(segs []string) (archivedKey, bool) {
	// segs = [<root>..., "projects", <project>, "<session>.jsonl"]
	if len(segs) < 4 {
		return archivedKey{}, false
	}
	i := len(segs) - 3
	if segs[i] != projectsDirName {
		return archivedKey{}, false
	}
	base := segs[len(segs)-1]
	if !strings.HasSuffix(base, sessionExt) {
		return archivedKey{}, false
	}
	session := strings.TrimSuffix(base, sessionExt)
	if session == "" {
		return archivedKey{}, false
	}
	return archivedKey{root: strings.Join(segs[:i], "/"), project: segs[i+1], session: session}, true
}

// archivedArtifactOwners reports the identified sessions a non-transcript
// path belongs to. A path can only be attributed through a tree named after
// a session UUID, so the segment naming the tree is located in the path and
// matched against the identified sessions; the same directory name may
// occur more than once in a path, and every occurrence that resolves to an
// identified session attributes the file.
func archivedArtifactOwners(segs []string, siblingTrees map[archivedKey]*archivedCandidate, rootTrees map[[2]string][]*archivedCandidate) []*archivedCandidate {
	var owners []*archivedCandidate
	for i, seg := range segs {
		switch {
		case seg == projectsDirName:
			// <root>/projects/<project>/<session>/<rest...>
			if len(segs) <= i+3 || archivedHidden(segs[i+3:]) {
				continue
			}
			key := archivedKey{root: strings.Join(segs[:i], "/"), project: segs[i+1], session: segs[i+2]}
			if cand, ok := siblingTrees[key]; ok {
				owners = append(owners, cand)
			}
		case isSessionArtifactDir(seg):
			// <root>/{tasks,session-env}/<session>/<rest...>
			if len(segs) <= i+2 || archivedHidden(segs[i+2:]) {
				continue
			}
			rk := [2]string{strings.Join(segs[:i], "/"), segs[i+1]}
			owners = append(owners, rootTrees[rk]...)
		}
	}
	return owners
}

// isSessionArtifactDir reports whether a directory name is one of the
// root-relative artifact trees whose children are named after a session
// UUID.
func isSessionArtifactDir(name string) bool {
	return slices.Contains(sessionArtifactDirs, name)
}

// archivedHidden reports whether any path component inside an artifact tree
// is dot-prefixed. Describe skips those entries — lock files and other
// transient state — so identification must not name them either.
func archivedHidden(rest []string) bool {
	for _, seg := range rest {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// archivedFileSet returns the session's closure inside the listing: the
// transcript plus its attributed artifacts, sorted and deduplicated so the
// result is stable for one listing regardless of its order.
func archivedFileSet(cand *archivedCandidate) []string {
	out := make([]string, 0, len(cand.artifacts)+1)
	out = append(out, cand.primaryPath)
	out = append(out, cand.artifacts...)
	slices.Sort(out)
	return slices.Compact(out)
}
