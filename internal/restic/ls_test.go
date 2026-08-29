package restic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Listing a snapshot's tree is the basis of cross-host fetch: one machine
// enumerates another's archived sessions from metadata alone, without
// downloading contents.
func TestLsListsSnapshotTree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}

	f.writeFile(t, "src/a.txt", []byte("hello"))
	f.writeFile(t, "src/sub/b.txt", []byte("world!"))
	if _, err := f.Backup(ctx,
		[]string{f.root + "/src"}, "h1", []string{"babel"}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snaps, err := f.Snapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}

	entries, err := f.Ls(ctx, snaps[0].ID)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}

	files := map[string]int64{}
	for _, e := range entries {
		if e.IsFile() {
			files[e.Path] = e.Size
		}
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want two", files)
	}
	var sawA, sawB bool
	for path, size := range files {
		switch {
		case strings.HasSuffix(path, "/src/a.txt"):
			sawA = true
			if size != 5 {
				t.Errorf("a.txt size = %d, want 5", size)
			}
		case strings.HasSuffix(path, "/src/sub/b.txt"):
			sawB = true
			if size != 6 {
				t.Errorf("b.txt size = %d, want 6", size)
			}
		}
	}
	if !sawA || !sawB {
		t.Errorf("missing entries: a=%v b=%v in %v", sawA, sawB, files)
	}

	// The snapshot header line must not appear as a node.
	for _, e := range entries {
		if e.Path == "" {
			t.Error("ls yielded an entry with no path")
		}
	}
}

func TestLsRejectsEmptySnapshotID(t *testing.T) {
	f := newFixture(t)
	if _, err := f.Ls(context.Background(), ""); err == nil {
		t.Fatal("Ls accepted an empty snapshot id")
	}
}

// restic records a backup summary in the snapshot list, so Babel must read it
// rather than treating snapshot counts as unavailable.
func TestSnapshotsCarryStoredSummary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	f.writeFile(t, "src/a.txt", []byte("hello"))
	if _, err := f.Backup(ctx,
		[]string{f.root + "/src"}, "h1", nil); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snaps, err := f.Snapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	s := snaps[0]
	if s.Summary == nil {
		t.Fatal("snapshot carries no summary: the catalog would lose counts it could have recorded")
	}
	if s.Summary.FilesNew != 1 {
		t.Errorf("files_new = %d, want 1", s.Summary.FilesNew)
	}
	if s.Summary.TotalBytesProcessed != 5 {
		t.Errorf("total_bytes_processed = %d, want 5", s.Summary.TotalBytesProcessed)
	}
	if s.Summary.DataAdded <= 0 {
		t.Errorf("data_added = %d, want positive", s.Summary.DataAdded)
	}
}

// A record without a summary must stay nil rather than becoming zeros: a caller
// distinguishing "unknown" from "backed up nothing" depends on it.
func TestSnapshotsPreserveAbsentSummary(t *testing.T) {
	const raw = `[
	  {"id":"aaaa","short_id":"aaaa","time":"2026-08-01T12:00:00Z","hostname":"h1"},
	  {"id":"bbbb","short_id":"bbbb","time":"2026-08-01T13:00:00Z","hostname":"h1",
	   "summary":{"files_new":4,"files_changed":1,"files_unmodified":9,"data_added":2048}}
	]`
	var parsed []snapshotJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	snaps := snapshotsFromJSON(parsed)
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	if snaps[0].Summary != nil {
		t.Errorf("absent summary became %+v; must stay nil", snaps[0].Summary)
	}
	if snaps[1].Summary == nil {
		t.Fatal("present summary was dropped")
	}
	if snaps[1].Summary.FilesNew != 4 || snaps[1].Summary.FilesUnmodified != 9 || snaps[1].Summary.DataAdded != 2048 {
		t.Errorf("summary = %+v, want files_new 4, unmodified 9, data_added 2048", snaps[1].Summary)
	}
}
