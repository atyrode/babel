package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// These tests live inside the package because two of them need the
// publishDelayForTests seam: with the lease row locked, no other connection can
// move expires_at, so an in-flight expiry can only be produced by real elapsed
// time inside the transaction.

func seedHost(t *testing.T, db *sql.DB, hostID string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', $1)
		 ON CONFLICT DO NOTHING`, SchemaVersion); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	for _, id := range []string{"inst-a", "inst-b"} {
		if _, err := db.Exec(
			`INSERT INTO instances (instance_id, deployment_id) VALUES ($1, 'd1')
			 ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatalf("seed instance: %v", err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO hosts (host_id, deployment_id) VALUES ($1, 'd1')
		 ON CONFLICT DO NOTHING`, hostID); err != nil {
		t.Fatalf("seed host: %v", err)
	}
}

func sampleSnapshot(id string, order int64) SnapshotRow {
	return SnapshotRow{
		SnapshotID:       id,
		PublicationOrder: order,
		SnapshotTime:     time.Now().UTC(),
		CommitState:      CommitCommitted,
		FilesNew:         3,
		BytesAdded:       4096,
		SessionCount:     1,
		PublishedBy:      "inst-a",
	}
}

func sampleSessions(uid string) []SessionRow {
	return []SessionRow{{
		SessionUID:  uid,
		Harness:     "omp",
		PrimarySize: 1234,
	}}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestAcquireLeaseExcludesASecondInstance(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	first, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first.Fence != 1 {
		t.Errorf("first fence = %d, want 1", first.Fence)
	}

	if _, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second acquire error = %v, want ErrLeaseHeld", err)
	}
}

func TestExpiredLeaseIsStealableAndBumpsTheFence(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	first, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Expire it using the server's own clock rather than sleeping.
	if _, err := db.Exec(`UPDATE host_leases SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	stolen, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute)
	if err != nil {
		t.Fatalf("steal expired lease: %v", err)
	}
	if stolen.Fence <= first.Fence {
		t.Errorf("stolen fence = %d, want greater than %d", stolen.Fence, first.Fence)
	}
	if stolen.HolderID != "inst-b" {
		t.Errorf("holder = %q, want inst-b", stolen.HolderID)
	}
}

// The core fencing property: a writer holding a superseded fence lands nothing.
func TestStaleFenceCannotPublish(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	stale, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := db.Exec(`UPDATE host_leases SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}

	applied, err := PublishSnapshot(ctx, db, stale, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("publish under stale fence: err = %v, want ErrLeaseLost", err)
	}
	if applied {
		t.Error("publish reported applied under a stale fence")
	}
	if n := countRows(t, db, "snapshots"); n != 0 {
		t.Errorf("stale writer landed %d snapshot rows, want 0", n)
	}
	if n := countRows(t, db, "sessions"); n != 0 {
		t.Errorf("stale writer landed %d session rows, want 0", n)
	}
	if n := countRows(t, db, "idempotency_keys"); n != 0 {
		t.Errorf("stale writer left %d idempotency keys, want 0", n)
	}
}

// A lease that expires after validation but before commit must land nothing,
// otherwise the TTL bounds nothing for a slow publisher.
func TestLeaseExpiringMidPublicationLandsNothing(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	// Short enough that the delay below outlives it by a wide margin.
	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	publishDelayForTests = func() { time.Sleep(600 * time.Millisecond) }
	t.Cleanup(func() { publishDelayForTests = nil })

	applied, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("publish across expiry: err = %v, want ErrLeaseLost", err)
	}
	if applied {
		t.Error("publish reported applied after its lease expired")
	}
	for _, table := range []string{"snapshots", "sessions", "idempotency_keys"} {
		if n := countRows(t, db, table); n != 0 {
			t.Errorf("expired publisher landed %d rows in %s, want 0", n, table)
		}
	}
}

// The same interleaving, but with a real takeover landing while the slow
// publisher is inside its transaction: the stealer must wait for the lock, and
// the publisher must still be refused once its lease has expired.
func TestTakeoverDuringSlowPublicationSerializes(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	takeover := make(chan error, 1)
	blocked := make(chan struct{})

	publishDelayForTests = func() {
		// Start a competing takeover while this transaction holds the lease row.
		go func() {
			close(blocked)
			_, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute)
			takeover <- err
		}()
		<-blocked
		time.Sleep(600 * time.Millisecond)
	}
	t.Cleanup(func() { publishDelayForTests = nil })

	applied, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("slow publish: err = %v, want ErrLeaseLost", err)
	}
	if applied {
		t.Error("slow publish reported applied")
	}

	select {
	case err := <-takeover:
		if err != nil {
			t.Fatalf("takeover after rollback: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("takeover never completed: the lease row stayed locked")
	}

	// The refused publisher left nothing, and the new holder owns the lease.
	if n := countRows(t, db, "snapshots"); n != 0 {
		t.Errorf("refused publisher landed %d snapshots, want 0", n)
	}
	var holder string
	if err := db.QueryRow(`SELECT holder_id FROM host_leases WHERE host_id = 'h1'`).Scan(&holder); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if holder != "inst-b" {
		t.Errorf("holder = %q, want inst-b", holder)
	}
}

func TestPublishIsIdempotent(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	applied, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if err != nil || !applied {
		t.Fatalf("first publish: applied=%v err=%v", applied, err)
	}

	// A retry after, say, a lost response must not duplicate anything.
	applied, err = PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if applied {
		t.Error("retry reported applied; the key should have suppressed it")
	}
	if n := countRows(t, db, "snapshots"); n != 1 {
		t.Errorf("snapshots = %d, want 1", n)
	}
	if n := countRows(t, db, "idempotency_keys"); n != 1 {
		t.Errorf("idempotency keys = %d, want 1", n)
	}
}

// A session seen again in a later snapshot keeps where it was first seen and
// moves its latest pointer forward.
func TestRepublishedSessionKeepsFirstSnapshot(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1")); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	grown := sampleSessions("uid-1")
	grown[0].PrimarySize = 9999
	if _, err := PublishSnapshot(ctx, db, l, "key-2",
		sampleSnapshot("snap-2", 2), grown); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	var first, latest string
	var size int64
	if err := db.QueryRow(
		`SELECT first_snapshot_id, latest_snapshot_id, primary_size FROM sessions
		  WHERE session_uid = 'uid-1'`).Scan(&first, &latest, &size); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if first != "snap-1" {
		t.Errorf("first_snapshot_id = %q, want snap-1", first)
	}
	if latest != "snap-2" {
		t.Errorf("latest_snapshot_id = %q, want snap-2", latest)
	}
	if size != 9999 {
		t.Errorf("primary_size = %d, want 9999", size)
	}
}

func TestRenewRequiresTheCurrentFence(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	renewed, err := RenewHostLease(ctx, db, l, 2*time.Minute)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.ExpiresAt.After(l.ExpiresAt) {
		t.Errorf("renew did not extend: %v then %v", l.ExpiresAt, renewed.ExpiresAt)
	}

	stale := l
	stale.Fence = l.Fence - 1
	if _, err := RenewHostLease(ctx, db, stale, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew with stale fence: err = %v, want ErrLeaseLost", err)
	}
}

func TestReleaseLetsAnotherInstanceAcquire(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := ReleaseHostLease(ctx, db, l); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	// Releasing a lease we no longer hold is not an error: the goal is already true.
	if err := ReleaseHostLease(ctx, db, l); err != nil {
		t.Fatalf("release of a superseded lease: %v", err)
	}
}

// Expiry is judged by the server, never by the client, so a skewed clock cannot
// extend a lease or make a live one look dead.
func TestLeaseExpiryComesFromServerTime(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var serverNow time.Time
	if err := db.QueryRow(`SELECT now()`).Scan(&serverNow); err != nil {
		t.Fatalf("read server time: %v", err)
	}
	delta := l.ExpiresAt.Sub(serverNow)
	if delta <= 0 || delta > 61*time.Second {
		t.Errorf("expires_at is %v from server now; want a positive value near the 60s ttl", delta)
	}
}
