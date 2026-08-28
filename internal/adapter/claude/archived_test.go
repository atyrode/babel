package claude

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
)

// The adapter must satisfy the snapshot-identification port.
var _ adapter.SnapshotIdentifier = New()

// sessionConflict is the fourth synthetic session: the transcript whose
// records disagree about the workspace. Identification must recognize it
// from its path like any other, because the disagreement lives inside the
// file and a listing never reads it.
const sessionConflict = "dddddddd-0000-4000-8000-000000000004"

// newArchivedRoot materializes the synthetic root discovery is tested
// against and adds the conflicting-cwd session, so identification is
// compared against discovery over every fixture shape the package has:
// three transcripts across two projects, a sibling subagent tree,
// root-relative task and session-env trees, a transient lock file, a
// non-transcript decoy, and a transcript whose contents are inconsistent.
func newArchivedRoot(t *testing.T) string {
	t.Helper()
	root := newSyntheticRoot(t)
	writeFixture(t, "session-cwd-conflict.jsonl",
		filepath.Join(root, "projects", projectAlpha, sessionConflict+".jsonl"))
	return root
}

// archivedListing renders a live tree as the file listing a snapshot of
// that tree holds: one slash-separated absolute source path per regular
// file, with its size, and nothing else. Identification sees only this.
func archivedListing(t *testing.T, roots ...string) []adapter.ArchivedFile {
	t.Helper()
	var out []adapter.ArchivedFile
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, adapter.ArchivedFile{Path: filepath.ToSlash(p), Size: fi.Size()})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// identify runs identification and indexes the result by SourceID, checking
// the ordering and deduplication the port requires.
func identify(t *testing.T, files []adapter.ArchivedFile) ([]adapter.ArchivedSession, map[string]adapter.ArchivedSession) {
	t.Helper()
	sessions, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	byID := make(map[string]adapter.ArchivedSession, len(sessions))
	for i, s := range sessions {
		if i > 0 && sessions[i-1].SourceID >= s.SourceID {
			t.Errorf("results are not ordered by SourceID: %q then %q", sessions[i-1].SourceID, s.SourceID)
		}
		if !adapter.ValidSourceID(s.SourceID) {
			t.Errorf("identified invalid source id %q", s.SourceID)
		}
		byID[s.SourceID] = s
	}
	return sessions, byID
}

func archivedIDs(sessions []adapter.ArchivedSession) []string {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.SourceID)
	}
	return ids
}

// TestIdentifyArchivedMatchesDiscover is the acceptance criterion:
// identification from a snapshot's paths alone must agree with discovery
// from disk over the same tree — same identities, same transcript per
// identity — and its closure must name exactly the files Describe reads in
// place for that session.
func TestIdentifyArchivedMatchesDiscover(t *testing.T) {
	root := newArchivedRoot(t)

	discovered, discoveredByID := discover(t, root)
	identified, identifiedByID := identify(t, archivedListing(t, root))

	wantIDs := []string{
		projectAlpha + "/" + sessionAlpha,
		projectAlpha + "/" + sessionConflict,
		projectBeta + "/" + sessionMalformed,
		projectBeta + "/" + sessionBare,
	}
	if got := sourceIDs(discovered); !slices.Equal(got, wantIDs) {
		t.Fatalf("Discover ids = %v, want %v", got, wantIDs)
	}
	if got := archivedIDs(identified); !slices.Equal(got, wantIDs) {
		t.Fatalf("IdentifyArchived ids = %v, want %v", got, wantIDs)
	}

	for id, src := range discoveredByID {
		arch, ok := identifiedByID[id]
		if !ok {
			t.Fatalf("discovered session %q was not identified from the listing", id)
		}
		if arch.PrimaryPath != filepath.ToSlash(src.PrimaryPath) {
			t.Errorf("%s: PrimaryPath = %q, want the discovered %q", id, arch.PrimaryPath, src.PrimaryPath)
		}

		desc := describe(t, src)
		if arch.PrimarySize != desc.PrimarySize {
			t.Errorf("%s: PrimarySize = %d, want %d", id, arch.PrimarySize, desc.PrimarySize)
		}
		want := []string{filepath.ToSlash(src.PrimaryPath)}
		for _, a := range desc.Artifacts {
			want = append(want, filepath.ToSlash(a.SourcePath))
		}
		slices.Sort(want)
		if !slices.Equal(arch.Files, want) {
			t.Errorf("%s: Files = %v, want the described closure %v", id, arch.Files, want)
		}
		for _, f := range arch.Files {
			if strings.Contains(f, "/.") {
				t.Errorf("%s: closure names transient dot-prefixed entry %q", id, f)
			}
		}
	}
}

// TestIdentifyArchivedClosureIsAttributedByPath pins the closure the paths
// alone can attribute: the transcript, the sibling tree named after the
// session UUID inside the project directory, and the root-relative
// tasks/<uuid> and session-env/<uuid> trees. Claude Code declares no
// referenced-artifact closure, so a session with no such trees is a single
// file and no blob is ever named.
func TestIdentifyArchivedClosureIsAttributedByPath(t *testing.T) {
	root := newArchivedRoot(t)
	_, byID := identify(t, archivedListing(t, root))

	alpha := byID[projectAlpha+"/"+sessionAlpha]
	wantAlpha := []string{
		filepath.ToSlash(filepath.Join(root, "projects", projectAlpha, sessionAlpha+".jsonl")),
		filepath.ToSlash(filepath.Join(root, "projects", projectAlpha, sessionAlpha, "subagents", subagentName)),
		filepath.ToSlash(filepath.Join(root, "session-env", sessionAlpha, "env.json")),
		filepath.ToSlash(filepath.Join(root, "tasks", sessionAlpha, "1.json")),
	}
	slices.Sort(wantAlpha)
	if !slices.Equal(alpha.Files, wantAlpha) {
		t.Errorf("alpha closure = %v, want %v", alpha.Files, wantAlpha)
	}

	bare := byID[projectBeta+"/"+sessionBare]
	if !slices.Equal(bare.Files, []string{bare.PrimaryPath}) {
		t.Errorf("bare closure = %v, want just the transcript %q", bare.Files, bare.PrimaryPath)
	}
	if notes := filepath.ToSlash(filepath.Join(root, "projects", projectAlpha, "notes.txt")); slices.Contains(alpha.Files, notes) {
		t.Errorf("closure claimed the non-transcript decoy %q", notes)
	}
}

func TestIdentifyArchivedEmptyListing(t *testing.T) {
	for name, files := range map[string][]adapter.ArchivedFile{
		"nil":   nil,
		"empty": {},
	} {
		sessions, err := New().IdentifyArchived(files)
		if err != nil {
			t.Errorf("%s listing: IdentifyArchived: %v", name, err)
		}
		if len(sessions) != 0 {
			t.Errorf("%s listing: identified %d sessions, want none", name, len(sessions))
		}
	}
}

// TestIdentifyArchivedIgnoresOtherHarnesses records that one snapshot holds
// several harnesses' trees: an unrecognized entry is ignored, never an
// error.
func TestIdentifyArchivedIgnoresOtherHarnesses(t *testing.T) {
	files := []adapter.ArchivedFile{
		{Path: "/home/synthetic/.codex/sessions/2026/06/15/rollout-01.jsonl", Size: 10},
		{Path: "/home/synthetic/.local/share/omp/sessions/synthetic-01.jsonl", Size: 11},
		{Path: "/home/synthetic/.local/share/omp/blobs/sha256/ab/cdef", Size: 12},
		{Path: "/home/synthetic/.claude/settings.json", Size: 13},
		{Path: "/home/synthetic/.claude/projects.jsonl", Size: 14},
		{Path: "/home/synthetic/.claude/tasks/orphan-session/1.json", Size: 15},
	}
	sessions, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("identified %v from another harness's paths, want none", archivedIDs(sessions))
	}
}

// TestIdentifyArchivedSkipsUnattributablePaths defends the pure-function
// boundary: a path is never resolved, because resolving one would invent a
// location the snapshot did not record. Anything that is not literally the
// <root>/projects/<project>/<session>.jsonl layout is skipped.
func TestIdentifyArchivedSkipsUnattributablePaths(t *testing.T) {
	cases := map[string]string{
		"dot-dot project segment":  "/home/synthetic/.claude/projects/../s.jsonl",
		"dot-dot session segment":  "/home/synthetic/.claude/projects/proj/../s.jsonl",
		"dot project segment":      "/home/synthetic/.claude/projects/./s.jsonl",
		"dot-dot inside root":      "/home/synthetic/../.claude/projects/proj/s.jsonl",
		"doubled separator":        "/home/synthetic/.claude//projects/proj/s.jsonl",
		"trailing separator":       "/home/synthetic/.claude/projects/proj/s.jsonl/",
		"empty path":               "",
		"empty session stem":       "/home/synthetic/.claude/projects/proj/.jsonl",
		"wrong extension":          "/home/synthetic/.claude/projects/proj/s.json",
		"transcript above project": "/home/synthetic/.claude/projects/s.jsonl",
		"transcript below project": "/home/synthetic/.claude/projects/proj/nested/s.jsonl",
		"no projects directory":    "/home/synthetic/.claude/proj/s.jsonl",
	}
	for name, p := range cases {
		sessions, err := New().IdentifyArchived([]adapter.ArchivedFile{{Path: p, Size: 1}})
		if err != nil {
			t.Errorf("%s: IdentifyArchived(%q): %v", name, p, err)
			continue
		}
		if len(sessions) != 0 {
			t.Errorf("%s: identified %v from %q, want nothing", name, archivedIDs(sessions), p)
		}
	}
}

// TestIdentifyArchivedIsOrderIndependent records that a listing's order
// never changes the result: identities, transcripts, and closures are the
// same however restic enumerated the tree.
func TestIdentifyArchivedIsOrderIndependent(t *testing.T) {
	files := archivedListing(t, newArchivedRoot(t))
	forward, _ := identify(t, files)

	reversed := slices.Clone(files)
	slices.Reverse(reversed)
	backward, _ := identify(t, reversed)

	if len(forward) != len(backward) {
		t.Fatalf("identified %d sessions forward and %d backward", len(forward), len(backward))
	}
	for i := range forward {
		if forward[i].SourceID != backward[i].SourceID ||
			forward[i].PrimaryPath != backward[i].PrimaryPath ||
			forward[i].PrimarySize != backward[i].PrimarySize ||
			!slices.Equal(forward[i].Files, backward[i].Files) {
			t.Errorf("session %d differs by listing order:\n forward  %+v\n backward %+v", i, forward[i], backward[i])
		}
	}
}

// TestIdentifyArchivedDeduplicatesRepeatedIdentities mirrors Discover:
// one snapshot may hold two Claude Code roots — a second machine's home, or
// a relocated one — whose trees carry the same identity. The identity is
// reported once, deterministically.
func TestIdentifyArchivedDeduplicatesRepeatedIdentities(t *testing.T) {
	rel := "/projects/" + projectAlpha + "/" + sessionAlpha
	files := []adapter.ArchivedFile{
		{Path: "/snap/b/.claude" + rel + ".jsonl", Size: 22},
		{Path: "/snap/b/.claude/tasks/" + sessionAlpha + "/1.json", Size: 23},
		{Path: "/snap/a/.claude" + rel + ".jsonl", Size: 20},
		{Path: "/snap/a/.claude/tasks/" + sessionAlpha + "/1.json", Size: 21},
	}
	sessions, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	want := []string{projectAlpha + "/" + sessionAlpha}
	if got := archivedIDs(sessions); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if sessions[0].PrimaryPath != "/snap/a/.claude"+rel+".jsonl" || sessions[0].PrimarySize != 20 {
		t.Errorf("kept %q (%d bytes), want the first tree by path", sessions[0].PrimaryPath, sessions[0].PrimarySize)
	}
	wantFiles := []string{
		"/snap/a/.claude" + rel + ".jsonl",
		"/snap/a/.claude/tasks/" + sessionAlpha + "/1.json",
	}
	if !slices.Equal(sessions[0].Files, wantFiles) {
		t.Errorf("Files = %v, want only the kept tree %v", sessions[0].Files, wantFiles)
	}
}

// TestIdentifyArchivedSharesRootArtifactTree records the one ambiguity the
// on-disk format allows: tasks/<uuid> and session-env/<uuid> are named
// after the session UUID alone, so two project directories holding the same
// UUID both claim that tree, exactly as Describe collects it for each.
func TestIdentifyArchivedSharesRootArtifactTree(t *testing.T) {
	shared := "/snap/.claude/tasks/" + sessionAlpha + "/1.json"
	files := []adapter.ArchivedFile{
		{Path: "/snap/.claude/projects/" + projectAlpha + "/" + sessionAlpha + ".jsonl", Size: 1},
		{Path: "/snap/.claude/projects/" + projectBeta + "/" + sessionAlpha + ".jsonl", Size: 2},
		{Path: shared, Size: 3},
	}
	sessions, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("identified %v, want both project directories", archivedIDs(sessions))
	}
	for _, s := range sessions {
		if !slices.Contains(s.Files, shared) {
			t.Errorf("%s: closure %v omits the shared artifact tree %q", s.SourceID, s.Files, shared)
		}
	}
}
