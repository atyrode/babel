package sharedcatalog_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// publishTo records one snapshot for a host under a fresh lease. Each test
// below builds a specific catalog shape, so the helper takes the order rather
// than deriving it: a case that pins publication order against snapshot time
// must be able to assign both.
func publishTo(t *testing.T, db *sql.DB, hostID, instanceID, snapshotID string,
	when time.Time, order int64, state string, sessions []sharedcatalog.SessionRow) {
	t.Helper()
	ctx := context.Background()
	lease, err := sharedcatalog.AcquireHostLease(ctx, db, hostID, instanceID, time.Minute)
	if err != nil {
		t.Fatalf("acquire lease for %s: %v", snapshotID, err)
	}
	applied, err := sharedcatalog.PublishSnapshot(ctx, db, lease, "snapshot:"+snapshotID,
		sharedcatalog.SnapshotRow{
			SnapshotID:       snapshotID,
			PublicationOrder: order,
			SnapshotTime:     when,
			CommitState:      state,
			SessionCount:     len(sessions),
			PublishedBy:      instanceID,
		}, sessions)
	if err != nil || !applied {
		t.Fatalf("publish %s: applied = %v, err = %v", snapshotID, applied, err)
	}
}

func hostCatalog(t *testing.T, db *sql.DB) []sharedcatalog.HostCatalogRow {
	t.Helper()
	rows, err := sharedcatalog.HostCatalog(context.Background(), db)
	if err != nil {
		t.Fatalf("HostCatalog: %v", err)
	}
	return rows
}

// A new deployment has a migrated catalog nothing has published into, which is a
// normal state rather than a malfunction: browsing it must report nothing, not
// fail.
func TestHostCatalogOnAnEmptyCatalog(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	rows, err := sharedcatalog.HostCatalog(context.Background(), db)
	if err != nil {
		t.Fatalf("HostCatalog on an empty catalog: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("an empty catalog reported %d hosts: %+v", len(rows), rows)
	}

	// A host that registered but never pushed still has nothing to describe.
	if err := sharedcatalog.Register(context.Background(), db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if rows := hostCatalog(t, db); len(rows) != 0 {
		t.Errorf("a registered host with no snapshots reported a row: %+v", rows)
	}
}

// The point of the browse surface is seeing the fleet, so one host's counts must
// not absorb another's. Ordering is by host id and not by insertion, so a second
// instance sees a stable list whichever host published first.
func TestHostCatalogSeparatesAndOrdersHosts(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	// Register the later-sorting host first, so insertion order and the
	// expected order disagree.
	for _, host := range []string{"h-zulu", "h-alpha"} {
		if err := sharedcatalog.Register(ctx, db, "d1", host, "inst-"+host); err != nil {
			t.Fatalf("Register %s: %v", host, err)
		}
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h-zulu", "inst-h-zulu", "s-z1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{SessionUID: "uid-z1", Harness: "omp"}})
	publishTo(t, db, "h-alpha", "inst-h-alpha", "s-a1", base.Add(time.Minute), 1,
		sharedcatalog.CommitCommitted, []sharedcatalog.SessionRow{
			{SessionUID: "uid-a1", Harness: "omp"},
			{SessionUID: "uid-a2", Harness: "codex"},
		})
	publishTo(t, db, "h-alpha", "inst-h-alpha", "s-a2", base.Add(2*time.Minute), 2,
		sharedcatalog.CommitCommitted, nil)

	rows := hostCatalog(t, db)
	if len(rows) != 2 {
		t.Fatalf("HostCatalog reported %d hosts, want 2: %+v", len(rows), rows)
	}
	if rows[0].HostID != "h-alpha" || rows[1].HostID != "h-zulu" {
		t.Fatalf("hosts = %q, %q; want them ordered by host id",
			rows[0].HostID, rows[1].HostID)
	}
	if rows[0].Snapshots != 2 || rows[0].Sessions != 2 {
		t.Errorf("h-alpha reported %d snapshots and %d sessions, want 2 and 2",
			rows[0].Snapshots, rows[0].Sessions)
	}
	if rows[1].Snapshots != 1 || rows[1].Sessions != 1 {
		t.Errorf("h-zulu reported %d snapshots and %d sessions, want 1 and 1; "+
			"one host's rows leaked into the other's counts",
			rows[1].Snapshots, rows[1].Sessions)
	}
}

// A session lives across pushes: every push that still finds it republishes it
// under the same opaque uid. Sessions answers "how many distinct sessions has
// this host archived", so counting publications instead would inflate with every
// push and tell an operator nothing.
func TestHostCatalogCountsARepublishedSessionOnce(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	lasting := sharedcatalog.SessionRow{SessionUID: "uid-lasting", Harness: "omp"}
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{lasting})
	publishTo(t, db, "h1", "inst-a", "s2", base.Add(time.Minute), 2, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{lasting, {SessionUID: "uid-new", Harness: "claude"}})

	rows := hostCatalog(t, db)
	if len(rows) != 1 {
		t.Fatalf("HostCatalog reported %d hosts, want 1: %+v", len(rows), rows)
	}
	if rows[0].Snapshots != 2 {
		t.Errorf("snapshots = %d, want 2", rows[0].Snapshots)
	}
	if rows[0].Sessions != 2 {
		t.Errorf("sessions = %d, want 2: the session present in both snapshots "+
			"must count once, and the second snapshot's new session must count",
			rows[0].Sessions)
	}
}

// Pending is a second reading of the same rows, not a different set of them: a
// catalog-pending snapshot is one restic committed, so leaving it out of
// Snapshots would understate what the host archived while overstating how
// complete the catalog is.
func TestHostCatalogCountsPendingRowsAmongSnapshots(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s-committed", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{SessionUID: "uid-1", Harness: "omp"}})

	// An outage stranded a snapshot; reconciliation adopts it with no session
	// rows, which is what leaves it pending.
	if _, err := sharedcatalog.Reconcile(ctx, db, "h1", []sharedcatalog.RepoSnapshot{
		{SnapshotID: "s-committed", Host: "h1", Time: base},
		{SnapshotID: "s-stranded", Host: "h1", Time: base.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	rows := hostCatalog(t, db)
	if len(rows) != 1 {
		t.Fatalf("HostCatalog reported %d hosts, want 1: %+v", len(rows), rows)
	}
	if rows[0].Pending != 1 {
		t.Errorf("pending = %d, want 1", rows[0].Pending)
	}
	if rows[0].Snapshots != 2 {
		t.Errorf("snapshots = %d, want 2: a pending row is a snapshot restic holds "+
			"and must still be counted", rows[0].Snapshots)
	}
	// The adopted snapshot carries no session rows, so the host's session count
	// does not move. That is an observation, not an omission.
	if rows[0].Sessions != 1 {
		t.Errorf("sessions = %d, want 1: adoption cannot invent session detail "+
			"nobody observed", rows[0].Sessions)
	}
}

// publication_order, not snapshot_time, identifies a host's newest row, and
// after an outage the two genuinely disagree: Reconcile gives a stranded
// snapshot the next order above the maximum, so the highest order can carry the
// OLDER time (ordering_test.go). This test is built so the two answers differ -
// picking max(snapshot_time) would return s-newest's time and pass a weaker
// test, which is why the times are deliberately inverted here.
func TestHostCatalogNewestFollowsPublicationOrderNotTime(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	strandedTime := base
	newestTime := base.Add(20 * time.Minute)

	publishTo(t, db, "h1", "inst-a", "s-newest", newestTime, 1, sharedcatalog.CommitCommitted, nil)
	// Adopting the older stranded snapshot now hands it order 2, above the
	// snapshot with the later time.
	if _, err := sharedcatalog.Reconcile(ctx, db, "h1", []sharedcatalog.RepoSnapshot{
		{SnapshotID: "s-newest", Host: "h1", Time: newestTime},
		{SnapshotID: "s-stranded", Host: "h1", Time: strandedTime},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var strandedOrder int64
	if err := db.QueryRowContext(ctx,
		`SELECT publication_order FROM snapshots WHERE snapshot_id = 's-stranded'`).
		Scan(&strandedOrder); err != nil {
		t.Fatalf("read stranded order: %v", err)
	}
	if strandedOrder <= 1 {
		t.Fatalf("the stranded snapshot took order %d, so this case no longer "+
			"separates publication order from snapshot time", strandedOrder)
	}

	rows := hostCatalog(t, db)
	if len(rows) != 1 {
		t.Fatalf("HostCatalog reported %d hosts, want 1: %+v", len(rows), rows)
	}
	if rows[0].NewestOrder != strandedOrder {
		t.Errorf("newest order = %d, want %d", rows[0].NewestOrder, strandedOrder)
	}
	if !rows[0].NewestSnapshotTime.Equal(strandedTime) {
		t.Errorf("newest snapshot time = %s, want %s (the highest-order row's time); "+
			"got %s would mean the newest is chosen by timestamp",
			rows[0].NewestSnapshotTime.UTC(), strandedTime, newestTime)
	}
}
