package codex

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
)

// listingOf renders a real tree the way a snapshot records it: "/"-separated
// paths and recorded sizes, with no directories and no file contents.
func listingOf(t *testing.T, roots ...string) []adapter.ArchivedFile {
	t.Helper()
	var out []adapter.ArchivedFile
	for _, root := range roots {
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
			out = append(out, adapter.ArchivedFile{Path: filepath.ToSlash(p), Size: info.Size()})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

func identify(t *testing.T, files []adapter.ArchivedFile) []adapter.ArchivedSession {
	t.Helper()
	found, err := New().IdentifyArchived(files)
	if err != nil {
		t.Fatalf("IdentifyArchived: %v", err)
	}
	for i := 1; i < len(found); i++ {
		if found[i-1].SourceID >= found[i].SourceID {
			t.Errorf("results not sorted and deduplicated: %q then %q", found[i-1].SourceID, found[i].SourceID)
		}
	}
	return found
}

func archivedIDs(sessions []adapter.ArchivedSession) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.SourceID
	}
	sort.Strings(out)
	return out
}

// requireIdentifiesLikeDiscover is the acceptance criterion for cross-host
// identification: reading only a listing of a tree must yield exactly the
// identities, primaries, and sizes that scanning the tree itself yields.
func requireIdentifiesLikeDiscover(t *testing.T, root string) []adapter.ArchivedSession {
	t.Helper()
	discovered := discover(t, root)
	identified := identify(t, listingOf(t, root))

	if got, want := archivedIDs(identified), sourceIDs(discovered); !reflect.DeepEqual(got, want) {
		t.Fatalf("identified ids %v, discovered %v", got, want)
	}
	byID := make(map[string]adapter.ArchivedSession, len(identified))
	for _, s := range identified {
		byID[s.SourceID] = s
	}
	for _, want := range discovered {
		got := byID[want.SourceID]
		if wantPath := filepath.ToSlash(want.PrimaryPath); got.PrimaryPath != wantPath {
			t.Errorf("%s: identified primary %q, discovered %q", want.SourceID, got.PrimaryPath, wantPath)
		}
		info, err := os.Stat(want.PrimaryPath)
		if err != nil {
			t.Fatalf("stat %s: %v", want.PrimaryPath, err)
		}
		if got.PrimarySize != info.Size() {
			t.Errorf("%s: identified size %d, want %d", want.SourceID, got.PrimarySize, info.Size())
		}
		if len(got.Files) == 0 {
			t.Errorf("%s: identified closure is empty", want.SourceID)
		}
		for i := 1; i < len(got.Files); i++ {
			if got.Files[i-1] >= got.Files[i] {
				t.Errorf("%s: closure not sorted and deduplicated: %v", want.SourceID, got.Files)
			}
		}
	}
	return identified
}

// TestIdentifyArchivedMatchesDiscover pins identification to discovery over
// the synthetic tree: several rollouts across two dated directories, the host
// history log, its session index, and attachment directories that belong to no
// session's path-derived closure.
func TestIdentifyArchivedMatchesDiscover(t *testing.T) {
	root := fixtureRoot(t)
	identified := requireIdentifiesLikeDiscover(t, root)

	want := []string{StateSourceID, fullID, malformedID, sparseID}
	sort.Strings(want)
	if got := archivedIDs(identified); !reflect.DeepEqual(got, want) {
		t.Errorf("identified %v, want %v", got, want)
	}
}

// TestIdentifyArchivedIsPure identifies a tree that exists nowhere: the paths
// name another machine's home directory, which is the whole point of
// identifying from a listing.
func TestIdentifyArchivedIsPure(t *testing.T) {
	const remote = "/home/someone-else/.codex"
	files := []adapter.ArchivedFile{
		{Path: remote + "/history.jsonl", Size: 11},
		{Path: remote + "/session_index.jsonl", Size: 22},
		{Path: remote + "/sessions/2026/01/02/rollout-2026-01-02T03-04-05-a.jsonl", Size: 33},
		{Path: remote + "/attachments/aaaa/capture.png", Size: 44},
	}
	found := identify(t, files)

	if got, want := archivedIDs(found), []string{"sessions/2026/01/02/rollout-2026-01-02T03-04-05-a.jsonl", StateSourceID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("identified %v, want %v", got, want)
	}
	for _, s := range found {
		switch s.SourceID {
		case StateSourceID:
			if s.PrimaryPath != remote+"/history.jsonl" || s.PrimarySize != 11 {
				t.Errorf("state primary = %q (%d bytes)", s.PrimaryPath, s.PrimarySize)
			}
			want := []string{remote + "/history.jsonl", remote + "/session_index.jsonl"}
			if !reflect.DeepEqual(s.Files, want) {
				t.Errorf("state closure = %v, want %v", s.Files, want)
			}
		default:
			// Attachment closure is scraped from message text, so a listing
			// cannot attribute "attachments/<id>/" to a session.
			if !reflect.DeepEqual(s.Files, []string{s.PrimaryPath}) {
				t.Errorf("rollout closure = %v, want only %q", s.Files, s.PrimaryPath)
			}
			if s.PrimarySize != 33 {
				t.Errorf("rollout size = %d, want 33", s.PrimarySize)
			}
		}
	}
}

func TestIdentifyArchivedEmptyListing(t *testing.T) {
	for _, files := range [][]adapter.ArchivedFile{nil, {}} {
		found, err := New().IdentifyArchived(files)
		if err != nil {
			t.Fatalf("IdentifyArchived(%v): %v", files, err)
		}
		if len(found) != 0 {
			t.Errorf("identified %d sessions from an empty listing", len(found))
		}
	}
}

// TestIdentifyArchivedIgnoresForeignHarnesses proves entries from other
// harnesses' trees are ignored rather than claimed, including OMP's, which
// also stores its logs under a "sessions" directory.
func TestIdentifyArchivedIgnoresForeignHarnesses(t *testing.T) {
	files := []adapter.ArchivedFile{
		{Path: "/home/a/.local/share/omp/agent/sessions/-synthetic/2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001.jsonl", Size: 1},
		{Path: "/home/a/.local/share/omp/agent/sessions/-synthetic/2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001/Helper.jsonl", Size: 2},
		{Path: "/home/a/.local/share/omp/agent/blobs/4c52cb66c34a0ec2179a503794277ad40135af36c43243942841a65a7df4686a", Size: 3},
		{Path: "/home/a/.claude/projects/-synthetic/00000000-0000-4000-8000-000000000001.jsonl", Size: 4},
		{Path: "/home/a/.cache/other/history.jsonl", Size: 5},
		{Path: "/home/a/notes.txt", Size: 6},
	}
	if found := identify(t, files); len(found) != 0 {
		t.Errorf("identified %v from foreign trees, want nothing", archivedIDs(found))
	}
}

// TestIdentifyArchivedSkipsUnattributablePaths drops entries whose recorded
// path was never normalized: an identity containing "." or ".." is not a valid
// source id and Discover can never produce one, so guessing at it would invent
// an identity no local scan agrees with.
func TestIdentifyArchivedSkipsUnattributablePaths(t *testing.T) {
	const root = "/home/a/.codex"
	files := []adapter.ArchivedFile{
		{Path: root + "/session_index.jsonl", Size: 1},
		{Path: root + "/sessions/2026/01/02/../02/rollout-dotdot.jsonl", Size: 2},
		{Path: root + "/sessions/2026/01/./02/rollout-dot.jsonl", Size: 3},
		{Path: root + "/sessions//2026/01/02/rollout-empty.jsonl", Size: 4},
		{Path: root + "/sessions/2026/01/02/rollout-clean.jsonl", Size: 5},
	}
	found := identify(t, files)

	want := []string{"sessions/2026/01/02/rollout-clean.jsonl"}
	if got := archivedIDs(found); !reflect.DeepEqual(got, want) {
		t.Fatalf("identified %v, want %v", got, want)
	}
}

// TestIdentifyArchivedUnusualNamesMatchDiscover checks the degraded identity
// path: a rollout name outside the identity alphabet becomes the same digest
// identity a local scan assigns it, rather than being dropped or aliased.
func TestIdentifyArchivedUnusualNamesMatchDiscover(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex")
	writeFile := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	writeFile("history.jsonl", "{}\n")
	writeFile("session_index.jsonl", "{}\n")
	writeFile("sessions/2026/01/02/rollout with spaces.jsonl", "{}\n")
	// Not date-partitioned, and named nothing like a rollout: Discover takes
	// every "*.jsonl" under "sessions", so identification must too.
	writeFile("sessions/loose.jsonl", "{}\n")

	identified := requireIdentifiesLikeDiscover(t, root)

	var digested int
	for _, s := range identified {
		if s.SourceID == "sessions/loose.jsonl" {
			continue
		}
		if s.SourceID != StateSourceID {
			digested++
			if !adapter.ValidSourceID(s.SourceID) {
				t.Errorf("degraded identity %q is not a valid source id", s.SourceID)
			}
			if want := "path-"; len(s.SourceID) <= len(want) || s.SourceID[:len(want)] != want {
				t.Errorf("identity %q is not the degraded digest form", s.SourceID)
			}
		}
	}
	if digested != 1 {
		t.Errorf("degraded %d identities, want 1", digested)
	}
}

// TestIdentifyArchivedMultipleRoots keeps the host-state identity unique: it
// names one host's state, so a snapshot holding two Codex roots reports it
// once, deterministically, the way Discover keeps the first root that carried
// it.
func TestIdentifyArchivedMultipleRoots(t *testing.T) {
	files := []adapter.ArchivedFile{
		{Path: "/home/b/.codex/history.jsonl", Size: 20},
		{Path: "/home/b/.codex/sessions/2026/01/03/rollout-b.jsonl", Size: 21},
		{Path: "/home/a/.codex/history.jsonl", Size: 10},
		{Path: "/home/a/.codex/sessions/2026/01/03/rollout-a.jsonl", Size: 11},
	}
	found := identify(t, files)

	want := []string{"sessions/2026/01/03/rollout-a.jsonl", "sessions/2026/01/03/rollout-b.jsonl", StateSourceID}
	sort.Strings(want)
	if got := archivedIDs(found); !reflect.DeepEqual(got, want) {
		t.Fatalf("identified %v, want %v", got, want)
	}
	for _, s := range found {
		if s.SourceID != StateSourceID {
			continue
		}
		if s.PrimaryPath != "/home/a/.codex/history.jsonl" || s.PrimarySize != 10 {
			t.Errorf("state primary = %q (%d bytes), want the lexicographically first root", s.PrimaryPath, s.PrimarySize)
		}
	}
}

// TestIdentifyArchivedRootWithoutHostState identifies rollouts from their
// dated partitioning alone, so a root whose operator disabled the history log
// still reports its sessions.
func TestIdentifyArchivedRootWithoutHostState(t *testing.T) {
	files := []adapter.ArchivedFile{
		{Path: "/srv/backup/home/c/.codex/sessions/2026/02/03/rollout-c.jsonl", Size: 7},
	}
	found := identify(t, files)

	if got, want := archivedIDs(found), []string{"sessions/2026/02/03/rollout-c.jsonl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("identified %v, want %v", got, want)
	}
}

// TestIdentifyArchivedDeduplicatesRepeatedEntries tolerates a listing that
// names the same file twice, as a merged multi-snapshot listing may.
func TestIdentifyArchivedDeduplicatesRepeatedEntries(t *testing.T) {
	entry := adapter.ArchivedFile{Path: "/home/d/.codex/sessions/2026/01/02/rollout-d.jsonl", Size: 9}
	found := identify(t, []adapter.ArchivedFile{entry, entry})

	if len(found) != 1 {
		t.Fatalf("identified %d sessions from a duplicated entry, want 1", len(found))
	}
	if !reflect.DeepEqual(found[0].Files, []string{entry.Path}) {
		t.Errorf("closure = %v, want %v", found[0].Files, []string{entry.Path})
	}
}
