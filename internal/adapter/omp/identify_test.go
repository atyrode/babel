package omp

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
)

// archivedListing renders a local tree the way a restic snapshot listing
// would: "/"-separated paths and recorded sizes, with no way to read the
// bytes. It is how a test stands in for another machine's snapshot.
func archivedListing(t *testing.T, root string) []adapter.ArchivedFile {
	t.Helper()
	var files []adapter.ArchivedFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, adapter.ArchivedFile{Path: filepath.ToSlash(p), Size: info.Size()})
		return nil
	})
	if err != nil {
		t.Fatalf("walk fixture tree: %v", err)
	}
	return files
}

// dataRoot returns the copied fixture's data root, the parent of both
// "agent/sessions" and "agent/blobs", so a listing covers BackupRoots
// rather than the session trees alone.
func dataRoot(sessionsRoot string) string {
	return filepath.Dir(filepath.Dir(sessionsRoot))
}

func identify(t *testing.T, files []adapter.ArchivedFile) []adapter.ArchivedSession {
	t.Helper()
	sessions, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	return sessions
}

// archived returns the identified session whose source id starts with
// prefix.
func archived(t *testing.T, sessions []adapter.ArchivedSession, prefix string) adapter.ArchivedSession {
	t.Helper()
	for _, s := range sessions {
		if strings.HasPrefix(s.SourceID, prefix) {
			return s
		}
	}
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.SourceID)
	}
	t.Fatalf("no identified session with prefix %q in %v", prefix, ids)
	return adapter.ArchivedSession{}
}

// TestIdentifyArchivedAgreesWithDiscover is the acceptance criterion of
// cross-host identification: identifying a tree from its paths alone must
// name exactly the sessions that discovering the same tree from disk does.
// The OMP fixture holds 4 sessions - one of them with a sibling artifact
// tree whose own "*.jsonl" file must not be counted as a fifth - plus a
// 4-entry blob store, so the agreement is a real check on a tree with
// every trap the layout allows, not two empty sets.
func TestIdentifyArchivedAgreesWithDiscover(t *testing.T) {
	sessionsRoot := fixtureRoot(t)
	identified := identify(t, archivedListing(t, dataRoot(sessionsRoot)))
	discovered := discover(t, sessionsRoot)

	if len(discovered) != 4 {
		t.Fatalf("fixture tree changed: Discover found %d sessions, want 4", len(discovered))
	}
	if len(identified) != len(discovered) {
		t.Fatalf("identified %d sessions, discovered %d", len(identified), len(discovered))
	}
	// Both orderings are ascending by source id, so comparing position by
	// position checks the identity sets and the promised order at once.
	for i, want := range discovered {
		got := identified[i]
		if got.SourceID != want.SourceID {
			t.Fatalf("session %d: identified id %q, discovered id %q", i, got.SourceID, want.SourceID)
		}
		if wantPath := filepath.ToSlash(want.PrimaryPath); got.PrimaryPath != wantPath {
			t.Errorf("%s: identified primary %q, discovered primary %q", got.SourceID, got.PrimaryPath, wantPath)
		}
	}
}

// TestIdentifyArchivedClosureCarriesArtifactsNotBlobs pins what a fetch
// would have to restore: the primary log and its sibling artifact tree,
// sorted and deduplicated. Blobs stay out because the listing cannot say
// which of them a session references.
func TestIdentifyArchivedClosureCarriesArtifactsNotBlobs(t *testing.T) {
	sessionsRoot := fixtureRoot(t)
	listing := archivedListing(t, dataRoot(sessionsRoot))
	identified := identify(t, listing)

	if !slices.ContainsFunc(listing, func(f adapter.ArchivedFile) bool {
		return strings.Contains(f.Path, "/"+blobsSubdir+"/")
	}) {
		t.Fatalf("fixture listing has no blob store; the blob-exclusion check would be vacuous")
	}

	withArtifacts := archived(t, identified, "-synthetic-project/")
	stem := strings.TrimSuffix(withArtifacts.PrimaryPath, sessionExt)
	want := []string{
		withArtifacts.PrimaryPath,
		stem + "/Helper.jsonl",
		stem + "/nested/7.bash.log",
	}
	if !slices.Equal(withArtifacts.Files, want) {
		t.Errorf("closure of %s:\n got %v\nwant %v", withArtifacts.SourceID, withArtifacts.Files, want)
	}

	for _, s := range identified {
		if !slices.IsSorted(s.Files) {
			t.Errorf("%s: closure not sorted: %v", s.SourceID, s.Files)
		}
		if len(slices.Compact(slices.Clone(s.Files))) != len(s.Files) {
			t.Errorf("%s: closure has duplicates: %v", s.SourceID, s.Files)
		}
		if !slices.Contains(s.Files, s.PrimaryPath) {
			t.Errorf("%s: closure %v omits primary %q", s.SourceID, s.Files, s.PrimaryPath)
		}
		for _, f := range s.Files {
			if strings.Contains(f, "/"+blobsSubdir+"/") {
				t.Errorf("%s: closure claims blob %q, which the listing cannot attribute", s.SourceID, f)
			}
		}
		if s.SourceID != withArtifacts.SourceID && len(s.Files) != 1 {
			t.Errorf("%s: closure %v, want the primary log alone", s.SourceID, s.Files)
		}
	}
}

// TestIdentifyArchivedRecordsSnapshotSizes checks that sizes come from the
// listing rather than from a local stat: identification never touches a
// filesystem, so the recorded size is the only size available.
func TestIdentifyArchivedRecordsSnapshotSizes(t *testing.T) {
	const primary = "/srv/backup/host-b/.omp/agent/sessions/proj/sess.jsonl"
	identified := identify(t, []adapter.ArchivedFile{{Path: primary, Size: 4242}})
	if len(identified) != 1 {
		t.Fatalf("identified %d sessions, want 1", len(identified))
	}
	if identified[0].PrimarySize != 4242 {
		t.Errorf("primary size %d, want 4242", identified[0].PrimarySize)
	}
	if identified[0].SourceID != "proj/sess" {
		t.Errorf("source id %q, want %q", identified[0].SourceID, "proj/sess")
	}
}

func TestIdentifyArchivedEmptyListing(t *testing.T) {
	for name, listing := range map[string][]adapter.ArchivedFile{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := identify(t, listing); len(got) != 0 {
				t.Errorf("identified %d sessions from an %s listing, want none", len(got), name)
			}
		})
	}
}

// TestIdentifyArchivedIgnoresForeignHarnesses defends the shared-snapshot
// rule: one repository holds several harnesses' trees, so another
// harness's paths must be ignored silently instead of failing the call.
func TestIdentifyArchivedIgnoresForeignHarnesses(t *testing.T) {
	listing := []adapter.ArchivedFile{
		{Path: "/home/other/.codex/sessions/2026/01/02/rollout-2026-01-02T03-04-05-abc.jsonl", Size: 10},
		{Path: "/home/other/.codex/history.jsonl", Size: 10},
		{Path: "/home/other/.claude/projects/-home-other-work/00000000-0000-4000-8000-000000000001.jsonl", Size: 10},
		{Path: "/home/other/.claude/todos/00000000-0000-4000-8000-000000000001.json", Size: 10},
		{Path: "/etc/hostname", Size: 10},
		{Path: "sessions", Size: 10},
		{Path: "/home/other/.omp/agent/blobs/" + strings.Repeat("a", 64), Size: 10},
		{Path: "/home/other/.omp/agent/sessions/proj/notes.txt", Size: 10},
		{Path: "/home/other/.omp/agent/sessions/loose.jsonl", Size: 10},
	}
	if got := identify(t, listing); len(got) != 0 {
		t.Fatalf("identified %d sessions from foreign paths: %v", len(got), got)
	}
}

// TestIdentifyArchivedSkipsUnwalkablePaths rejects paths a local walk
// could never have produced: os.ReadDir never yields an empty, "." or ".."
// component, so such a component is not a trustworthy identity even
// though idSegment would sanitize it into the source-id alphabet.
func TestIdentifyArchivedSkipsUnwalkablePaths(t *testing.T) {
	for _, p := range []string{
		"/home/a/.omp/agent/sessions/../sess.jsonl",
		"/home/a/.omp/agent/sessions/./sess.jsonl",
		"/home/a/.omp/agent/sessions//sess.jsonl",
		"/home/a/.omp/agent/sessions/proj/../sess.jsonl",
		"/home/a/../.omp/agent/sessions/proj/sess.jsonl",
	} {
		t.Run(p, func(t *testing.T) {
			if got := identify(t, []adapter.ArchivedFile{{Path: p, Size: 1}}); len(got) != 0 {
				t.Errorf("identified %v from unwalkable path %q", got, p)
			}
		})
	}
}

// TestIdentifyArchivedNeverMistakesArtifactForSession reproduces the trap
// Discover documents: a session's artifact tree is a directory sharing the
// session's stem, so its own "*.jsonl" files sit one level deeper and
// belong to the closure rather than being sessions of their own.
func TestIdentifyArchivedNeverMistakesArtifactForSession(t *testing.T) {
	const base = "/home/a/.omp/agent/sessions/proj/sess"
	listing := []adapter.ArchivedFile{
		{Path: base + "/nested/subagent.jsonl", Size: 3},
		{Path: base + sessionExt, Size: 1},
		{Path: base + "/subagent.jsonl", Size: 2},
		// Repeated entries - of the primary and of an artifact alike - must
		// collapse, because the closure is a deduplicated set of paths.
		{Path: base + sessionExt, Size: 1},
		{Path: base + "/subagent.jsonl", Size: 2},
	}
	identified := identify(t, listing)
	if len(identified) != 1 {
		t.Fatalf("identified %d sessions, want 1: %v", len(identified), identified)
	}
	got := identified[0]
	if got.SourceID != "proj/sess" {
		t.Errorf("source id %q, want %q", got.SourceID, "proj/sess")
	}
	want := []string{base + sessionExt, base + "/nested/subagent.jsonl", base + "/subagent.jsonl"}
	if !slices.Equal(got.Files, want) {
		t.Errorf("closure:\n got %v\nwant %v", got.Files, want)
	}
}

// TestIdentifyArchivedSanitizesUnsafeComponents checks that a path whose
// components fall outside the source-id alphabet earns the same sanitized
// identity Discover would give the same file locally.
func TestIdentifyArchivedSanitizesUnsafeComponents(t *testing.T) {
	const p = "/home/a/.omp/agent/sessions/odd project+name/sess ion.jsonl"
	identified := identify(t, []adapter.ArchivedFile{{Path: p, Size: 1}})
	if len(identified) != 1 {
		t.Fatalf("identified %d sessions, want 1", len(identified))
	}
	want := idSegment("odd project+name") + "/" + idSegment("sess ion")
	if identified[0].SourceID != want {
		t.Errorf("source id %q, want %q", identified[0].SourceID, want)
	}
	if !adapter.ValidSourceID(identified[0].SourceID) {
		t.Errorf("source id %q is not a valid source id", identified[0].SourceID)
	}
}
