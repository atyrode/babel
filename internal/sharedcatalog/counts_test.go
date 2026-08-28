package sharedcatalog

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// readCounts returns the snapshot's measures, using pointers so a NULL is
// distinguishable from a stored zero - which is the whole point of 0002.
func readCounts(t *testing.T, db *sql.DB, snapshotID string) (filesNew, bytesAdded, sessionCount *int64) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT files_new, bytes_added, session_count FROM snapshots WHERE snapshot_id = $1`,
		snapshotID).Scan(&filesNew, &bytesAdded, &sessionCount); err != nil {
		t.Fatalf("read counts for %s: %v", snapshotID, err)
	}
	return filesNew, bytesAdded, sessionCount
}

// A snapshot whose restic record carries a summary keeps its real counts through
// reconciliation: restic does store them in the snapshot list, so discarding
// them would lose recoverable truth.
func TestReconcilePreservesRepositoryCounts(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	list := []RepoSnapshot{{
		SnapshotID: "s1",
		Host:       "h1",
		Time:       time.Now().UTC(),
		Counts: &SnapshotCounts{
			FilesNew:        7,
			FilesChanged:    2,
			FilesUnmodified: 100,
			BytesAdded:      4096,
		},
	}}
	if _, err := Reconcile(ctx, db, "h1", list); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	filesNew, bytesAdded, sessionCount := readCounts(t, db, "s1")
	if filesNew == nil || *filesNew != 7 {
		t.Errorf("files_new = %v, want 7", filesNew)
	}
	if bytesAdded == nil || *bytesAdded != 4096 {
		t.Errorf("bytes_added = %v, want 4096", bytesAdded)
	}
	// Session count is not in the snapshot list at any version, so it stays
	// unknown until a push.
	if sessionCount != nil {
		t.Errorf("session_count = %d, want NULL until the owning host pushes", *sessionCount)
	}
}

// A snapshot with no stored summary must read as unknown, never as a snapshot
// that backed up nothing.
func TestUnknownCountsAreNullNotZero(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	list := []RepoSnapshot{{SnapshotID: "s1", Host: "h1", Time: time.Now().UTC()}}
	if _, err := Reconcile(ctx, db, "h1", list); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	filesNew, bytesAdded, sessionCount := readCounts(t, db, "s1")
	for name, got := range map[string]*int64{
		"files_new":     filesNew,
		"bytes_added":   bytesAdded,
		"session_count": sessionCount,
	} {
		if got != nil {
			t.Errorf("%s = %d, want NULL: zero would claim the snapshot backed up nothing", name, *got)
		}
	}
}

// The owning host's push is the authority: it replaces unknown counts with real
// ones, so a reconciled row does not stay ambiguous forever.
func TestPushSupersedesUnknownCounts(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	// Reconciliation records the snapshot with no summary available.
	if _, err := Reconcile(ctx, db, "h1",
		[]RepoSnapshot{{SnapshotID: "s1", Host: "h1", Time: time.Now().UTC()}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if filesNew, _, _ := readCounts(t, db, "s1"); filesNew != nil {
		t.Fatalf("precondition: files_new = %d, want NULL", *filesNew)
	}

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	snap := sampleSnapshot("s1", 1)
	snap.FilesNew = 5
	snap.BytesAdded = 8192
	snap.SessionCount = 1
	if _, err := PublishSnapshot(ctx, db, l, "key-1", snap, sampleSessions("uid-1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	filesNew, bytesAdded, sessionCount := readCounts(t, db, "s1")
	if filesNew == nil || *filesNew != 5 {
		t.Errorf("files_new = %v, want 5 after the push", filesNew)
	}
	if bytesAdded == nil || *bytesAdded != 8192 {
		t.Errorf("bytes_added = %v, want 8192 after the push", bytesAdded)
	}
	if sessionCount == nil || *sessionCount != 1 {
		t.Errorf("session_count = %v, want 1 after the push", sessionCount)
	}
}

// Rebuild carries repository counts through too, so recovery is not needlessly
// lossy about what the archive already records.
func TestRebuildPreservesRepositoryCounts(t *testing.T) {
	db := newInternalDB(t)
	ctx := context.Background()

	list := []RepoSnapshot{
		{SnapshotID: "s1", Host: "h1", Time: time.Now().UTC(),
			Counts: &SnapshotCounts{FilesNew: 3, BytesAdded: 512}},
		{SnapshotID: "s2", Host: "h1", Time: time.Now().UTC().Add(time.Hour)},
	}
	if _, err := Rebuild(ctx, db, "d1", "h1", list); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if filesNew, bytes, _ := readCounts(t, db, "s1"); filesNew == nil || *filesNew != 3 || bytes == nil || *bytes != 512 {
		t.Errorf("s1 counts = (%v, %v), want (3, 512)", filesNew, bytes)
	}
	if filesNew, bytes, _ := readCounts(t, db, "s2"); filesNew != nil || bytes != nil {
		t.Errorf("s2 counts = (%v, %v), want NULL for a summary-less snapshot", filesNew, bytes)
	}
}
