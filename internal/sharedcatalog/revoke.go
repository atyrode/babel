package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrInstanceRevoked reports that this instance has been evicted from the
// deployment and may no longer write to the shared catalog.
var ErrInstanceRevoked = errors.New("instance is revoked")

// ErrUnknownInstance reports an instance id the deployment never registered.
// Revoking a mistyped id must not look like a successful eviction.
var ErrUnknownInstance = errors.New("unknown instance")

// RevokeInstance evicts one instance from the deployment without re-keying the
// repository or rotating the fleet's credential (SPEC.md 11; pre-deployment
// gate 14). It is idempotent: re-running it is how an operator confirms the
// state, and it never moves the timestamp of the original eviction.
//
// The eviction is enforced by this package, not by the database, because the
// first deployment's provider cannot issue a database user per instance
// (migrations/0003_instance_revocation.sql). It is therefore only as strong as
// the instance's cooperation: it stops a machine that is out of service - a
// decommissioned server, a retired laptop - and it bounds a compromised
// instance only until someone notices, because whoever holds the shared
// credential can clear their own revoked_at. Against a hostile holder the real
// controls are fleet-wide credential rotation and repository-password custody.
//
// Both effects land in one transaction: the instance is marked, and every live
// lease it holds is force-expired so another instance can take that host over
// at once instead of waiting out the TTL.
func RevokeInstance(ctx context.Context, db *sql.DB, instanceID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke instance: begin: %w", err)
	}
	defer tx.Rollback()

	// coalesce keeps the first revocation's timestamp, so a second call cannot
	// rewrite when the eviction happened. Server time, never the client's
	// (SPEC.md 9). No row means no such instance, which is a real error rather
	// than a silent no-op - the operator's intent was not carried out.
	//
	// revoked_at belongs to no unique index, so this is a non-key update: it
	// does not conflict with the key-share locks a concurrent publication takes
	// on this row through its foreign keys, and cannot stall behind one.
	var revoked bool
	err = tx.QueryRowContext(ctx, `
		UPDATE instances
		   SET revoked_at = coalesce(revoked_at, `+serverNow+`)
		 WHERE instance_id = $1
		RETURNING true`, instanceID).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrUnknownInstance
	case err != nil:
		return fmt.Errorf("revoke instance: mark revoked: %w", err)
	}

	// Force-expire this instance's leases, skipping any row a publication has
	// locked.
	//
	// Waiting for that lock would defeat the mechanism: the lock is held by the
	// very publication this revocation must stop, and it is released only at
	// that transaction's commit - so a blocking revocation could commit only
	// after the rows it was meant to prevent had landed. Skipping keeps this
	// transaction short and lets the publication observe the revocation at its
	// pre-commit check instead (see checkLease). A skipped lease stays live
	// until its TTL; the doomed publication cannot use it, and re-running the
	// revocation once that transaction has rolled back expires it immediately.
	if _, err := tx.ExecContext(ctx, `
		UPDATE host_leases SET expires_at = `+serverNow+`
		 WHERE host_id IN (
		       SELECT host_id FROM host_leases
		        WHERE holder_id = $1 AND expires_at > `+serverNow+`
		        FOR UPDATE SKIP LOCKED)`, instanceID); err != nil {
		return fmt.Errorf("revoke instance: expire held leases: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke instance: commit: %w", err)
	}
	return nil
}

// InstanceRevoked reports whether an instance is currently revoked. An id the
// deployment does not know is ErrUnknownInstance rather than "not revoked": a
// caller asking about a typo deserves to hear about it.
func InstanceRevoked(ctx context.Context, db *sql.DB, instanceID string) (bool, error) {
	var revoked bool
	err := db.QueryRowContext(ctx,
		`SELECT revoked_at IS NOT NULL FROM instances WHERE instance_id = $1`,
		instanceID).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, ErrUnknownInstance
	case err != nil:
		return false, fmt.Errorf("read instance revocation: %w", err)
	}
	return revoked, nil
}

// classifyRefusal explains why a write statement that gates on revocation
// declined to write.
//
// The decision itself was already made atomically inside that statement; this
// read only chooses which error to report, so it adds no race. fallback is the
// reason that applies when the instance is in good standing.
func classifyRefusal(ctx context.Context, db *sql.DB, instanceID string, fallback error) error {
	revoked, err := InstanceRevoked(ctx, db, instanceID)
	switch {
	case err != nil:
		return err
	case revoked:
		return ErrInstanceRevoked
	}
	return fallback
}
