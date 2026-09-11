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

// hostOrders reports one host's publication orders, which is the column the
// cross-host tests are about: it totally orders that host's snapshots and must
// never be shared with another machine's.
func hostOrders(t *testing.T, db *sql.DB, hostID string) map[string]int64 {
	t.Helper()
	rows, err := db.Query(
		`SELECT snapshot_id, publication_order FROM snapshots WHERE host_id = $1`, hostID)
	if err != nil {
		t.Fatalf("read %s orders: %v", hostID, err)
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
		t.Fatalf("read %s orders: %v", hostID, err)
	}
	return out
}

// The defect this exists for: `uncatalogued` used to self-heal only if the
// snapshot's OWN host pushed again, so a snapshot stranded by a retired, dead,
// or merely idle machine stayed outside the catalog indefinitely - which
// SPEC.md 9.1 forbids in the same terms it forbids an unpublished record.
//
// So the pushing host is h-b and the stranded snapshots are h-a's, and h-a has
// no row in this catalog at all: it is the machine that is not coming back.
// What the adoption must get right is the numbering. publication_order totally
// orders ONE host's snapshots (migrations/0001_init.sql), so h-a's adopted
// snapshots start at 1 in h-a's own space rather than continuing h-b's, and
// h-b's own published snapshot keeps the number it was published under.
func TestAdoptForeignAdoptsAnotherHostsStrandedSnapshots(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h-b")
	ctx := context.Background()

	// h-b has published once, so its order space is occupied and a mistake
	// that mixed the two hosts would be visible as a collision or a gap.
	lease, err := AcquireHostLease(ctx, db, "h-b", "inst-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire h-b's lease: %v", err)
	}
	if _, err := PublishSnapshot(ctx, db, lease, "key-b1",
		sampleSnapshot("s-b1", 1), sampleSessions("uid-b1")); err != nil {
		t.Fatalf("publish h-b's own snapshot: %v", err)
	}

	rep, err := AdoptForeign(ctx, db, "d1", "inst-b", "h-b",
		repoList("h-a", "s-a1", "s-a2"), time.Minute)
	if err != nil {
		t.Fatalf("adopt h-a's snapshots: %v", err)
	}
	if rep.Adopted != 2 {
		t.Errorf("adopted %d, want both of h-a's stranded snapshots", rep.Adopted)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0] != "h-a" {
		t.Errorf("adopted for %v, want [h-a]", rep.Hosts)
	}
	if len(rep.Deferred) != 0 || len(rep.Failed) != 0 || len(rep.Refused) != 0 {
		t.Errorf("report = %+v, want nothing deferred, failed, or refused", rep)
	}

	// Attribution: the rows name the host restic recorded, never the adopter.
	if got := hostOrders(t, db, "h-a"); len(got) != 2 {
		t.Fatalf("h-a's snapshots = %v, want two attributed to h-a", got)
	} else if got["s-a1"] != 1 || got["s-a2"] != 2 {
		t.Errorf("h-a's orders = %v, want s-a1 at 1 and s-a2 at 2 in h-a's own space", got)
	}
	if got := hostOrders(t, db, "h-b"); len(got) != 1 || got["s-b1"] != 1 {
		t.Errorf("h-b's orders = %v, want only its own published snapshot at 1", got)
	}

	// Session detail is the part a listing cannot supply, so the adopted rows
	// are catalog-pending and carry none of it.
	state := snapshotState(t, db)
	for _, id := range []string{"s-a1", "s-a2"} {
		if state[id] != CommitPending {
			t.Errorf("%s adopted as %q, want %q", id, state[id], CommitPending)
		}
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("sessions = %d, want only h-b's own published row", n)
	}

	// The adopter must not assert anything about the machine it adopted for:
	// it does not know h-a's display name, operating system, or architecture.
	var name, opsys, arch *string
	if err := db.QueryRow(
		`SELECT display_name, os, arch FROM hosts WHERE host_id = 'h-a'`).Scan(&name, &opsys, &arch); err != nil {
		t.Fatalf("read the adopted host row: %v", err)
	}
	if name != nil || opsys != nil || arch != nil {
		t.Errorf("adoption asserted h-a's identity as %v/%v/%v; only h-a may", name, opsys, arch)
	}

	// Each host's next number continues its own sequence.
	for host, want := range map[string]int64{"h-a": 3, "h-b": 2} {
		next, err := NextPublicationOrder(ctx, db, host)
		if err != nil {
			t.Fatalf("next order for %s: %v", host, err)
		}
		if next != want {
			t.Errorf("%s's next publication order = %d, want %d", host, next, want)
		}
	}
}

// Two hosts pushing at once must not both adopt one snapshot into conflicting
// orders. Write authority per host is the existing mechanism for that, so a
// host some instance is already publishing for is reported deferred and left
// entirely alone - that instance's own push adopts it, or the next pass does.
func TestAdoptForeignDefersAHostAnotherInstanceIsPublishingFor(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h-a")
	seedHost(t, db, "h-b")
	ctx := context.Background()

	if _, err := AcquireHostLease(ctx, db, "h-a", "inst-a", time.Minute); err != nil {
		t.Fatalf("let inst-a take h-a's lease: %v", err)
	}

	rep, err := AdoptForeign(ctx, db, "d1", "inst-b", "h-b",
		repoList("h-a", "s-a1"), time.Minute)
	if err != nil {
		t.Fatalf("adopt while h-a is publishing: %v", err)
	}
	if rep.Adopted != 0 {
		t.Errorf("adopted %d rows under a lease another instance holds", rep.Adopted)
	}
	if len(rep.Deferred) != 1 || rep.Deferred[0] != "h-a" {
		t.Errorf("deferred = %v, want [h-a]", rep.Deferred)
	}
	if n := countRows(t, db, "snapshots"); n != 0 {
		t.Errorf("a deferred adoption still wrote %d rows", n)
	}
}

// Attribution stays truthful in the other direction too. A snapshot naming no
// host, or naming one the rest of Babel would reject, is named rather than
// adopted - and named rather than fatal, because one anomalous snapshot must
// not strand every other machine's. The adopting host's own snapshots are a
// caller mistake: it already holds its own lease, and re-acquiring it here
// would invalidate the publication in flight.
func TestAdoptForeignRefusesSnapshotsItCannotAttribute(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h-b")
	ctx := context.Background()

	listing := append(repoList("h-a", "s-a1"),
		RepoSnapshot{SnapshotID: "s-nameless", Time: time.Now().UTC()},
		RepoSnapshot{SnapshotID: "s-malformed", Host: "Not A Host", Time: time.Now().UTC()})
	rep, err := AdoptForeign(ctx, db, "d1", "inst-b", "h-b", listing, time.Minute)
	if err != nil {
		t.Fatalf("adopt a listing with anomalies: %v", err)
	}
	if rep.Adopted != 1 {
		t.Errorf("adopted %d, want the one attributable snapshot", rep.Adopted)
	}
	want := []string{"s-malformed", "s-nameless"}
	if len(rep.Refused) != len(want) || rep.Refused[0] != want[0] || rep.Refused[1] != want[1] {
		t.Errorf("refused = %v, want %v", rep.Refused, want)
	}
	if _, ok := snapshotState(t, db)["s-nameless"]; ok {
		t.Error("a snapshot naming no host reached the catalog")
	}

	if _, err := AdoptForeign(ctx, db, "d1", "inst-b", "h-b",
		repoList("h-b", "s-b9"), time.Minute); err == nil {
		t.Error("AdoptForeign accepted the adopting host's own snapshots")
	}
}

// The restore-and-rescan write path. A catalog-pending row gains the session
// detail a rescan recovered and leaves the state, and it keeps restic's counts:
// those were never the missing part, and a rescan rewriting them would replace
// archive truth with a second reading of it.
func TestCompletePendingRecordsSessionDetailAndCommits(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	if _, err := Reconcile(ctx, db, "h1", repoList("h1", "s1")); err != nil {
		t.Fatalf("adopt s1: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE snapshots SET files_new = 7, bytes_added = 4096 WHERE snapshot_id = 's1'`); err != nil {
		t.Fatalf("give s1 restic's counts: %v", err)
	}

	lease, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	applied, err := CompletePending(ctx, db, lease, "s1", "inst-a", sampleSessions("uid-1"))
	if err != nil {
		t.Fatalf("complete s1: %v", err)
	}
	if !applied {
		t.Fatal("completing a catalog-pending row reported no change")
	}

	var state string
	var sessionCount, filesNew, bytesAdded int64
	if err := db.QueryRow(`
		SELECT commit_state, session_count, files_new, bytes_added
		  FROM snapshots WHERE snapshot_id = 's1'`).Scan(
		&state, &sessionCount, &filesNew, &bytesAdded); err != nil {
		t.Fatalf("read s1: %v", err)
	}
	if state != CommitCommitted {
		t.Errorf("s1 state = %q, want %q", state, CommitCommitted)
	}
	if sessionCount != 1 {
		t.Errorf("s1 session_count = %d, want 1", sessionCount)
	}
	if filesNew != 7 || bytesAdded != 4096 {
		t.Errorf("s1 counts = %d files and %d bytes; restic's own must survive a rescan",
			filesNew, bytesAdded)
	}
	if n := countRows(t, db, "sessions"); n != 1 {
		t.Errorf("sessions = %d, want the recovered row", n)
	}

	// Idempotent by the commit state rather than by a key: a repeat finds the
	// work done, which is what makes two instances draining the same backlog
	// safe.
	again, err := CompletePending(ctx, db, lease, "s1", "inst-a", sampleSessions("uid-1"))
	if err != nil {
		t.Fatalf("second completion: %v", err)
	}
	if again {
		t.Error("completing an already committed row reported a change")
	}
}

// A rescan recovers detail nothing else holds; it never overwrites detail a
// later push already wrote. The row a push published names the newest snapshot
// that held the session, and rewinding it to what an older snapshot held would
// replace current truth with history.
func TestCompletePendingDoesNotRewindASessionAPushAlreadyPublished(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	lease, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// s2 is the newer snapshot and it published the session; s1 is the older
	// one an outage left catalog-pending.
	if _, err := Reconcile(ctx, db, "h1", repoList("h1", "s1")); err != nil {
		t.Fatalf("adopt s1: %v", err)
	}
	if _, err := PublishSnapshot(ctx, db, lease, "key-s2",
		sampleSnapshot("s2", 2), []SessionRow{{
			SessionUID: "uid-1", Harness: "omp", PrimarySize: 9000,
		}}); err != nil {
		t.Fatalf("publish s2: %v", err)
	}

	applied, err := CompletePending(ctx, db, lease, "s1", "inst-a", []SessionRow{{
		SessionUID: "uid-1", Harness: "omp", PrimarySize: 1234,
	}})
	if err != nil {
		t.Fatalf("complete s1: %v", err)
	}
	if !applied {
		t.Fatal("completing the older catalog-pending row reported no change")
	}

	var latest string
	var size int64
	if err := db.QueryRow(
		`SELECT latest_snapshot_id, primary_size FROM sessions WHERE session_uid = 'uid-1'`).Scan(
		&latest, &size); err != nil {
		t.Fatalf("read the session row: %v", err)
	}
	if latest != "s2" || size != 9000 {
		t.Errorf("session row = %s at %d bytes, want s2 at 9000: a rescan of an older snapshot must not rewind it",
			latest, size)
	}
	if state := snapshotState(t, db)["s1"]; state != CommitCommitted {
		t.Errorf("s1 state = %q, want %q", state, CommitCommitted)
	}
}
