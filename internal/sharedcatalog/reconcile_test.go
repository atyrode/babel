package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func repoList(host string, ids ...string) []RepoSnapshot {
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	out := make([]RepoSnapshot, 0, len(ids))
	for i, id := range ids {
		out = append(out, RepoSnapshot{
			SnapshotID: id,
			Host:       host,
			Time:       base.Add(time.Duration(i) * time.Hour),
		})
	}
	return out
}

func snapshotState(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT snapshot_id, commit_state FROM snapshots ORDER BY publication_order`)
	if err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			t.Fatalf("scan snapshot: %v", err)
		}
		out[id] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	return out
}

func TestReconcileAdoptsSnapshotsAsPending(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	rep, err := Reconcile(ctx, db, "h1", repoList("h1", "s1", "s2"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Added != 2 || rep.Confirmed != 0 {
		t.Errorf("report = %+v, want 2 added and 0 confirmed", rep)
	}

	state := snapshotState(t, db)
	for _, id := range []string{"s1", "s2"} {
		if state[id] != CommitPending {
			t.Errorf("%s state = %q, want %q", id, state[id], CommitPending)
		}
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()
	list := repoList("h1", "s1", "s2")

	if _, err := Reconcile(ctx, db, "h1", list); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	rep, err := Reconcile(ctx, db, "h1", list)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if rep.Added != 0 || rep.Confirmed != 2 {
		t.Errorf("second report = %+v, want 0 added and 2 confirmed", rep)
	}
}

// A published snapshot keeps its committed state and its session rows: the
// repository listing carries no counts, so reconciliation must never downgrade
// what a push already established.
func TestReconcileDoesNotDowngradeCommittedSnapshots(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("s1", 1), sampleSessions("uid-1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if _, err := Reconcile(ctx, db, "h1", repoList("h1", "s1", "s2")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	state := snapshotState(t, db)
	if state["s1"] != CommitCommitted {
		t.Errorf("published snapshot downgraded to %q", state["s1"])
	}
	if state["s2"] != CommitPending {
		t.Errorf("adopted snapshot state = %q, want pending", state["s2"])
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("sessions = %d, want the published row preserved", n)
	}
}

// Retention is append-only and Babel never prunes, so a snapshot the catalog
// knows but the repository no longer reports is an anomaly to surface - not
// something to delete.
func TestReconcileReportsSnapshotsMissingFromRepository(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	if _, err := Reconcile(ctx, db, "h1", repoList("h1", "s1", "s2")); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	rep, err := Reconcile(ctx, db, "h1", repoList("h1", "s1"))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(rep.MissingFromRepository) != 1 || rep.MissingFromRepository[0] != "s2" {
		t.Errorf("missing = %v, want [s2]", rep.MissingFromRepository)
	}
	if _, ok := snapshotState(t, db)["s2"]; !ok {
		t.Error("reconciliation deleted a snapshot row; retention is append-only")
	}
}

// Attribution must be checked, not assumed: adopting another host's snapshots,
// or a snapshot recorded without --host, would corrupt fleet identity.
func TestReconcileRefusesForeignOrUnattributedSnapshots(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	cases := map[string][]RepoSnapshot{
		"another host":   repoList("h2", "s1"),
		"no host at all": {{SnapshotID: "s1", Time: time.Now().UTC()}},
		"mixed hosts": append(repoList("h1", "s1"),
			RepoSnapshot{SnapshotID: "s2", Host: "h2", Time: time.Now().UTC()}),
	}
	for name, list := range cases {
		if _, err := Reconcile(ctx, db, "h1", list); !errors.Is(err, ErrHostMismatch) {
			t.Errorf("%s: err = %v, want ErrHostMismatch", name, err)
		}
	}
	if n := countRows(t, db, "snapshots"); n != 0 {
		t.Errorf("refused reconciliation still wrote %d rows", n)
	}
}

// A host id the rest of Babel would reject must not reach a primary key through
// this path. Shape validation reuses config.ValidHostID.
func TestReconcileRejectsMalformedHostIDs(t *testing.T) {
	db := newInternalDB(t)
	ctx := context.Background()

	for _, bad := range []string{"", "Upper", "has space", "has/slash", ".leading"} {
		if _, err := Reconcile(ctx, db, bad, nil); err == nil {
			t.Errorf("Reconcile accepted malformed host id %q", bad)
		}
		if _, err := Rebuild(ctx, db, "d1", bad, nil); err == nil {
			t.Errorf("Rebuild accepted malformed host id %q", bad)
		}
	}
}

// The disaster-recovery property SPEC.md 9 promises: losing the Phase A
// database is recoverable from the repository plus source rescans.
func TestRebuildRecoversFromAnEmptyDatabase(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	// Establish a fully published catalog.
	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	for i, id := range []string{"s1", "s2"} {
		if _, err := PublishSnapshot(ctx, db, l, "key-"+id,
			sampleSnapshot(id, int64(i+1)), sampleSessions("uid-"+id)); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	if n := countRows(t, db, "sessions"); n != 2 {
		t.Fatalf("sessions before loss = %d, want 2", n)
	}

	// Lose everything, as a dropped database would. Order follows the foreign
	// keys: leaves first, then hosts and instances, then the deployment.
	for _, table := range []string{
		"idempotency_keys", "sessions", "snapshots", "host_leases",
		"instances", "hosts", "deployments",
	} {
		if _, err := db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}

	// Rebuild from the repository listing alone.
	rep, err := Rebuild(ctx, db, "d1", "h1", repoList("h1", "s1", "s2"))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rep.Added != 2 {
		t.Errorf("rebuild added %d, want 2", rep.Added)
	}

	state := snapshotState(t, db)
	if len(state) != 2 {
		t.Fatalf("rebuilt snapshots = %v, want two", state)
	}
	for id, s := range state {
		if s != CommitPending {
			t.Errorf("%s rebuilt as %q; the listing carries no counts, so it must be pending", id, s)
		}
	}
	// Session metadata is genuinely lost until a rescan: say so rather than
	// pretending the listing could supply it.
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("sessions after rebuild = %d, want 0 until the owning host pushes again", n)
	}

	// The owning host's next push restores committed state and session rows.
	// The instance row must exist first: leases and snapshots reference it.
	if _, err := db.Exec(
		`INSERT INTO instances (instance_id, deployment_id) VALUES ('inst-a', 'd1')
		 ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("reseed instance: %v", err)
	}
	l2, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if _, err := PublishSnapshot(ctx, db, l2, "key-after-rebuild",
		sampleSnapshot("s2", 2), sampleSessions("uid-s2")); err != nil {
		t.Fatalf("publish after rebuild: %v", err)
	}
	if snapshotState(t, db)["s2"] != CommitCommitted {
		t.Error("a push after rebuild did not restore committed state")
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("sessions after re-push = %d, want 1", n)
	}
}

// Rebuild is reproducible: the same repository state yields the same ordering,
// so two instances recovering independently agree.
func TestRebuildIsDeterministic(t *testing.T) {
	db := newInternalDB(t)
	ctx := context.Background()

	// All three share one timestamp, so ordering rests entirely on the snapshot
	// id tie-breaker - restic snapshots taken in the same second do collide.
	// The two rebuilds receive the same set in different input orders, which is
	// what two instances recovering independently would see from their own
	// listings.
	shared := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	tied := func(ids ...string) []RepoSnapshot {
		out := make([]RepoSnapshot, 0, len(ids))
		for _, id := range ids {
			out = append(out, RepoSnapshot{SnapshotID: id, Host: "h1", Time: shared})
		}
		return out
	}

	if _, err := Rebuild(ctx, db, "d1", "h1", tied("s3", "s1", "s2")); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	first := publicationOrders(t, db)

	if _, err := Rebuild(ctx, db, "d1", "h1", tied("s2", "s3", "s1")); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	second := publicationOrders(t, db)

	if len(first) != 3 {
		t.Fatalf("orders = %v, want three", first)
	}
	for id, order := range first {
		if second[id] != order {
			t.Errorf("%s order changed between rebuilds: %d then %d", id, order, second[id])
		}
	}
	// And the tie-breaker must be the id, so the order is predictable rather
	// than merely stable.
	for id, want := range map[string]int64{"s1": 1, "s2": 2, "s3": 3} {
		if first[id] != want {
			t.Errorf("%s order = %d, want %d from the id tie-breaker", id, first[id], want)
		}
	}
}

func publicationOrders(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT snapshot_id, publication_order FROM snapshots`)
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var order int64
		if err := rows.Scan(&id, &order); err != nil {
			t.Fatalf("scan order: %v", err)
		}
		out[id] = order
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read orders: %v", err)
	}
	return out
}
