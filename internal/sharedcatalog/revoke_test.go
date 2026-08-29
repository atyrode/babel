package sharedcatalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests live inside the package because the mid-publication case needs the
// publishDelayForTests seam, and because the allowlist case asserts against
// Verify rather than against the allowlist map.

// The point of revocation: the evicted instance's host is available again at
// once, not after its lease TTL runs out.
func TestRevokeForceExpiresLeasesSoAnotherInstanceTakesOver(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	held, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Hour)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("before revocation inst-b must be excluded, got %v", err)
	}

	if err := RevokeInstance(ctx, db, "inst-a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	revoked, err := InstanceRevoked(ctx, db, "inst-a")
	if err != nil || !revoked {
		t.Fatalf("InstanceRevoked = %v, %v; want true, nil", revoked, err)
	}

	// No sleeping: the hour-long lease is dead because revocation expired it.
	took, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire after revocation: %v", err)
	}
	if took.HolderID != "inst-b" {
		t.Errorf("holder = %q, want inst-b", took.HolderID)
	}
	if took.Fence <= held.Fence {
		t.Errorf("fence = %d, want greater than %d", took.Fence, held.Fence)
	}

	// The instance that was not revoked is unaffected.
	if revoked, err := InstanceRevoked(ctx, db, "inst-b"); err != nil || revoked {
		t.Fatalf("InstanceRevoked(inst-b) = %v, %v; want false, nil", revoked, err)
	}
}

func TestRevokedInstanceCannotAcquireOrRenewALease(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Hour)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := RenewHostLease(ctx, db, l, time.Hour); err != nil {
		t.Fatalf("renew before revocation: %v", err)
	}

	if err := RevokeInstance(ctx, db, "inst-a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := RenewHostLease(ctx, db, l, time.Hour); !errors.Is(err, ErrInstanceRevoked) {
		t.Fatalf("renew after revocation: err = %v, want ErrInstanceRevoked", err)
	}
	// The host is free now, so a refusal here can only come from revocation.
	if _, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Hour); !errors.Is(err, ErrInstanceRevoked) {
		t.Fatalf("acquire after revocation: err = %v, want ErrInstanceRevoked", err)
	}
	// Nor can it re-enter by stealing a host it never held.
	seedHost(t, db, "h2")
	if _, err := AcquireHostLease(ctx, db, "h2", "inst-a", time.Hour); !errors.Is(err, ErrInstanceRevoked) {
		t.Fatalf("acquire of an unheld host: err = %v, want ErrInstanceRevoked", err)
	}
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM host_leases
		 WHERE holder_id = 'inst-a' AND expires_at > now()`).Scan(&live); err != nil {
		t.Fatalf("count live leases: %v", err)
	}
	if live != 0 {
		t.Errorf("revoked instance holds %d live leases, want 0", live)
	}
}

// An instance revoked while a long push is in flight must land nothing: the
// pre-commit revalidation is what makes the eviction take effect immediately
// rather than after the current publication finishes.
func TestRevocationMidPublicationLandsNothing(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	// An hour-long lease, so a refusal cannot be attributed to expiry.
	l, err := AcquireHostLease(ctx, db, "h1", "inst-a", time.Hour)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	var revokeErr error
	publishDelayForTests = func() {
		// Synchronous: revocation must not block behind the publication's lease
		// row lock, or it could only ever commit after the rows it must prevent.
		revokeErr = RevokeInstance(ctx, db, "inst-a")
	}
	t.Cleanup(func() { publishDelayForTests = nil })

	applied, err := PublishSnapshot(ctx, db, l, "key-1",
		sampleSnapshot("snap-1", 1), sampleSessions("uid-1"))
	if revokeErr != nil {
		t.Fatalf("revoke during publication: %v", revokeErr)
	}
	if !errors.Is(err, ErrInstanceRevoked) {
		t.Fatalf("publish across revocation: err = %v, want ErrInstanceRevoked", err)
	}
	if applied {
		t.Error("publish reported applied after its instance was revoked")
	}
	for _, table := range []string{"snapshots", "sessions", "idempotency_keys"} {
		if n := countRows(t, db, table); n != 0 {
			t.Errorf("revoked publisher landed %d rows in %s, want 0", n, table)
		}
	}

	// The lease row was locked by the publication, so revocation left it to its
	// TTL. Re-running the revocation once that transaction has rolled back is
	// idempotent and completes the eviction.
	if err := RevokeInstance(ctx, db, "inst-a"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := AcquireHostLease(ctx, db, "h1", "inst-b", time.Minute); err != nil {
		t.Fatalf("takeover after revocation: %v", err)
	}
}

// Re-running a revocation is how an operator confirms it, so it must not
// rewrite when the eviction actually happened.
func TestRevocationIsIdempotentAndKeepsTheOriginalTime(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	if err := RevokeInstance(ctx, db, "inst-a"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	var first time.Time
	if err := db.QueryRow(
		`SELECT revoked_at FROM instances WHERE instance_id = 'inst-a'`).Scan(&first); err != nil {
		t.Fatalf("read revoked_at: %v", err)
	}

	// Let the server clock advance far enough that a rewrite would be visible.
	time.Sleep(20 * time.Millisecond)

	if err := RevokeInstance(ctx, db, "inst-a"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	var second time.Time
	if err := db.QueryRow(
		`SELECT revoked_at FROM instances WHERE instance_id = 'inst-a'`).Scan(&second); err != nil {
		t.Fatalf("re-read revoked_at: %v", err)
	}
	if !second.Equal(first) {
		t.Errorf("revoked_at moved from %v to %v", first, second)
	}
}

func TestRevokingAnUnknownInstanceIsAnError(t *testing.T) {
	db := newInternalDB(t)
	seedHost(t, db, "h1")
	ctx := context.Background()

	if err := RevokeInstance(ctx, db, "inst-typo"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("revoke unknown instance: err = %v, want ErrUnknownInstance", err)
	}
	if _, err := InstanceRevoked(ctx, db, "inst-typo"); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("query unknown instance: err = %v, want ErrUnknownInstance", err)
	}
	// A failed revocation must not have evicted anyone else.
	var evicted int
	if err := db.QueryRow(
		`SELECT count(*) FROM instances WHERE revoked_at IS NOT NULL`).Scan(&evicted); err != nil {
		t.Fatalf("count revoked instances: %v", err)
	}
	if evicted != 0 {
		t.Errorf("%d instances revoked, want 0", evicted)
	}
}

// revoked_at crosses the plaintext boundary, so it is only legitimate because
// the allowlist admits it as a timestamp. Both directions are asserted through
// Verify: a freshly migrated schema passes, and the same schema with the column
// removed fails - which it could not do unless the allowlist named it.
func TestRevokedAtIsInsideThePlaintextAllowlist(t *testing.T) {
	db := newInternalDB(t)
	ctx := context.Background()

	if err := Verify(ctx, db); err != nil {
		t.Fatalf("migrated schema must satisfy the allowlist: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE instances DROP COLUMN revoked_at`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	err := Verify(ctx, db)
	if err == nil {
		t.Fatal("Verify accepted a schema missing instances.revoked_at")
	}
	if !strings.Contains(err.Error(), "instances.revoked_at") {
		t.Fatalf("error must name the absent column, got: %v", err)
	}
}
