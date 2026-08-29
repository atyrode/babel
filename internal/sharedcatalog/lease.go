package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrLeaseHeld reports that another instance holds a live lease on the host.
// The caller should not publish for that host: its owner is mid-push.
var ErrLeaseHeld = errors.New("host lease is held by another instance")

// ErrLeaseLost reports that this lease is no longer current - it expired, or
// another instance took over. Rows offered under it are refused, which is what
// stops a stale writer from landing a late publication.
var ErrLeaseLost = errors.New("host lease is no longer held")

// ErrUnknownInstance reports an instance id the deployment never registered.
// An instance must record its own identity before it can take a lease, so this
// names a missing registration step rather than letting it read as contention
// with an instance that does not exist.
var ErrUnknownInstance = errors.New("unknown instance")

// Every timestamp here comes from PostgreSQL rather than the client (SPEC.md 9),
// so an instance with a skewed clock cannot extend its own lease or judge
// whether it still holds one.
//
// Expiry is compared against clock_timestamp(), not now(). PostgreSQL's now() is
// transaction_timestamp(): it is frozen for the whole transaction, so a lease
// check inside a long publication transaction would keep reporting the instant
// the transaction began and could never observe an expiry that happened while
// the transaction ran. clock_timestamp() advances, which is what makes the TTL
// an actual bound on how long a publication may hold a host.
const serverNow = `clock_timestamp()`

// Lease is proof that an instance may write one host's rows.
//
// Fence is the authority, not HolderID: a resumed writer can present the right
// holder id with an old fence, and must still be refused.
type Lease struct {
	HostID    string
	HolderID  string
	Fence     int64
	ExpiresAt time.Time
}

// AcquireHostLease takes the lease for a host, or steals one that has expired,
// and returns the new fence.
//
// It is a single statement so acquisition is atomic under concurrency: two
// instances racing for a free or expired lease produce one winner and one
// ErrLeaseHeld, never two holders. A holder re-acquiring its own lease gets a
// fresh fence, which deliberately invalidates its earlier epoch.
// The row it would write is selected from instances, so an instance that never
// registered takes nothing.
//
// Write authority is also refused against an incompatible schema. This is the
// once-per-push gate: a binary must not publish into a database migrated by a
// newer one, and against an unmigrated database the error names the command to
// run. It constrains this binary only - an older one performs no such check.
func AcquireHostLease(ctx context.Context, db *sql.DB, hostID, instanceID string, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		return Lease{}, errors.New("lease ttl must be positive")
	}
	if err := EnsureCompatible(ctx, db); err != nil {
		return Lease{}, err
	}
	l := Lease{HostID: hostID}
	err := db.QueryRowContext(ctx, `
		INSERT INTO host_leases (host_id, holder_id, fence, acquired_at, expires_at)
		SELECT $1::text, $2::text, 1, `+serverNow+`, `+serverNow+` + make_interval(secs => $3)
		  FROM instances
		 WHERE instance_id = $2
		ON CONFLICT (host_id) DO UPDATE
		   SET holder_id   = excluded.holder_id,
		       fence       = host_leases.fence + 1,
		       acquired_at = `+serverNow+`,
		       expires_at  = `+serverNow+` + make_interval(secs => $3)
		 WHERE host_leases.expires_at <= `+serverNow+`
		    OR host_leases.holder_id = excluded.holder_id
		RETURNING holder_id, fence, expires_at`,
		hostID, instanceID, ttl.Seconds()).Scan(&l.HolderID, &l.Fence, &l.ExpiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The statement declined to write: either this instance never
		// registered, so the gating select produced no candidate row, or the
		// DO UPDATE WHERE clause found a live lease belonging to someone else.
		return Lease{}, refusalReason(ctx, db, instanceID, ErrLeaseHeld)
	case err != nil:
		return Lease{}, fmt.Errorf("acquire host lease: %w", err)
	}
	return l, nil
}

// RenewHostLease extends a lease that this exact fence still owns. A long push
// renews rather than taking a TTL longer than any plausible upload.
func RenewHostLease(ctx context.Context, db *sql.DB, l Lease, ttl time.Duration) (Lease, error) {
	if ttl <= 0 {
		return Lease{}, errors.New("lease ttl must be positive")
	}
	out := l
	err := db.QueryRowContext(ctx, `
		UPDATE host_leases
		   SET expires_at = `+serverNow+` + make_interval(secs => $4)
		 WHERE host_id = $1 AND holder_id = $2 AND fence = $3
		   AND expires_at > `+serverNow+`
		RETURNING expires_at`,
		l.HostID, l.HolderID, l.Fence, ttl.Seconds()).Scan(&out.ExpiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Lease{}, ErrLeaseLost
	case err != nil:
		return Lease{}, fmt.Errorf("renew host lease: %w", err)
	}
	return out, nil
}

// ReleaseHostLease gives up a lease early so another instance need not wait for
// it to expire. Releasing a lease that is no longer current is not an error:
// the outcome the caller wanted is already true.
func ReleaseHostLease(ctx context.Context, db *sql.DB, l Lease) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE host_leases SET expires_at = `+serverNow+`
		 WHERE host_id = $1 AND holder_id = $2 AND fence = $3`,
		l.HostID, l.HolderID, l.Fence); err != nil {
		return fmt.Errorf("release host lease: %w", err)
	}
	return nil
}

// checkLease asserts inside a transaction that a lease is still current, taking
// a row lock so a concurrent steal cannot slip between the check and the writes
// it guards: a stealing instance must update this row, and it stays locked
// until the transaction ends.
//
// Called twice by a publication - once before writing and once immediately
// before commit - because the lock stops other writers but not the passage of
// time, and a slow publisher must not commit under a lease that expired while
// it worked.
func checkLease(ctx context.Context, tx *sql.Tx, l Lease) error {
	var one int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		  FROM host_leases l
		 WHERE l.host_id = $1 AND l.holder_id = $2 AND l.fence = $3
		   AND l.expires_at > `+serverNow+`
		   FOR UPDATE OF l`,
		l.HostID, l.HolderID, l.Fence).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrLeaseLost
	case err != nil:
		return fmt.Errorf("check host lease: %w", err)
	}
	return nil
}

// refusalReason explains why a write statement that gates on registration
// declined to write.
//
// The decision itself was already made atomically inside that statement; this
// read only chooses which error to report, so it adds no race. Fallback is the
// reason that applies when the instance is registered.
func refusalReason(ctx context.Context, db *sql.DB, instanceID string, fallback error) error {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM instances WHERE instance_id = $1`, instanceID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrUnknownInstance
	case err != nil:
		return fmt.Errorf("read instance registration: %w", err)
	}
	return fallback
}
