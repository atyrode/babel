package sharedcatalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// publication_order totally orders a host's snapshots so readers can select the
// newest without trusting clock skew (migrations/0001_init.sql). Reconcile
// assigns each adopted snapshot the next order above the current maximum, which
// makes the sequence of operations load-bearing: adopting a stranded OLDER
// snapshot AFTER publishing a newer one would hand the older snapshot the higher
// order and break the invariant.
//
// A push therefore reconciles first, excluding the snapshot it is about to
// publish, and only then takes its own order. This test pins that outcome so the
// two steps cannot be reordered back.
func TestPublicationOrderAgreesWithSnapshotTime(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	first := base
	stranded := base.Add(10 * time.Minute)
	newest := base.Add(20 * time.Minute)

	publish := func(id string, when time.Time, order int64) {
		t.Helper()
		lease, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
		if err != nil {
			t.Fatalf("acquire for %s: %v", id, err)
		}
		applied, err := sharedcatalog.PublishSnapshot(ctx, db, lease, "snapshot:"+id,
			sharedcatalog.SnapshotRow{
				SnapshotID:       id,
				PublicationOrder: order,
				SnapshotTime:     when,
				CommitState:      sharedcatalog.CommitCommitted,
				PublishedBy:      "inst-a",
			}, nil)
		if err != nil || !applied {
			t.Fatalf("publish %s: applied = %v, err = %v", id, applied, err)
		}
	}

	// An ordinary push.
	order, err := sharedcatalog.NextPublicationOrder(ctx, db, "h1")
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	publish("s-first", first, order)

	// Then an outage strands a snapshot, and the next push finds two snapshots
	// in the repository the catalog has never seen: the stranded one and its own.
	// It adopts only the earlier one.
	repo := []sharedcatalog.RepoSnapshot{
		{SnapshotID: "s-first", Host: "h1", Time: first},
		{SnapshotID: "s-stranded", Host: "h1", Time: stranded},
	}
	rep, err := sharedcatalog.Reconcile(ctx, db, "h1", repo)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rep.Added != 1 || rep.Confirmed != 1 {
		t.Fatalf("Reconcile added %d and confirmed %d; want 1 and 1", rep.Added, rep.Confirmed)
	}

	// Only now does this push take its order, which must land above the adopted
	// snapshot rather than below it.
	order, err = sharedcatalog.NextPublicationOrder(ctx, db, "h1")
	if err != nil {
		t.Fatalf("order after reconciling: %v", err)
	}
	publish("s-newest", newest, order)

	rows, err := db.QueryContext(ctx,
		`SELECT snapshot_id, publication_order FROM snapshots
		  WHERE host_id = 'h1' ORDER BY snapshot_time`)
	if err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	defer rows.Close()

	var ids []string
	var orders []int64
	for rows.Next() {
		var id string
		var got int64
		if err := rows.Scan(&id, &got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
		orders = append(orders, got)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}

	want := []string{"s-first", "s-stranded", "s-newest"}
	if len(ids) != len(want) {
		t.Fatalf("snapshots by time = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("snapshots by time = %v, want %v", ids, want)
		}
	}
	for i := 1; i < len(orders); i++ {
		if orders[i] <= orders[i-1] {
			t.Fatalf("publication_order %v does not ascend with snapshot_time for %v: "+
				"a reader selecting max(publication_order) would not get the newest snapshot",
				orders, ids)
		}
	}
}

// The trap is specific and worth demonstrating rather than only describing:
// adopting after publishing really does inverse the order, so the sequence in
// publishToCatalog is not a stylistic choice.
func TestAdoptingAfterPublishingWouldInvertTheOrder(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	stranded := base
	newest := base.Add(20 * time.Minute)

	// Publish the newer snapshot first, which is the wrong sequence.
	order, err := sharedcatalog.NextPublicationOrder(ctx, db, "h1")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	lease, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := sharedcatalog.PublishSnapshot(ctx, db, lease, "snapshot:s-newest",
		sharedcatalog.SnapshotRow{
			SnapshotID:       "s-newest",
			PublicationOrder: order,
			SnapshotTime:     newest,
			CommitState:      sharedcatalog.CommitCommitted,
			PublishedBy:      "inst-a",
		}, nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Then adopt the older stranded one.
	if _, err := sharedcatalog.Reconcile(ctx, db, "h1", []sharedcatalog.RepoSnapshot{
		{SnapshotID: "s-newest", Host: "h1", Time: newest},
		{SnapshotID: "s-stranded", Host: "h1", Time: stranded},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var strandedOrder, newestOrder int64
	if err := db.QueryRowContext(ctx,
		`SELECT
		   (SELECT publication_order FROM snapshots WHERE snapshot_id = 's-stranded'),
		   (SELECT publication_order FROM snapshots WHERE snapshot_id = 's-newest')`).
		Scan(&strandedOrder, &newestOrder); err != nil {
		t.Fatalf("read orders: %v", err)
	}
	if strandedOrder <= newestOrder {
		t.Fatalf("adopting after publishing gave the older snapshot order %d and the newer one %d; "+
			"if Reconcile no longer assigns max+1, publishToCatalog's ordering comment is stale",
			strandedOrder, newestOrder)
	}
}
