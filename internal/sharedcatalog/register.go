package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/jackc/pgx/v5/pgconn"
)

// HostIdentity is what a machine asserts about itself when it registers
// (migrations/0004). It is the newest-wins payload decision 8 promises.
//
// DisplayName is operator-assigned - never the system hostname, which
// reconcile.go refuses to let into this catalog as infrastructure identity. OS
// and Arch are the machine's own build platform, the same pair `babel version`
// already prints. An empty field asserts nothing and leaves whatever the row
// holds, so a binary or an operator with no name to supply cannot blank one
// another machine set.
type HostIdentity struct {
	DisplayName string
	OS          string
	Arch        string
}

// Register records the deployment, host, and instance this process publishes
// as, refreshes the instance's last-seen time, and asserts this host's
// identity.
//
// Publication depends on all three rows existing: host_leases.holder_id
// references instances, snapshots.host_id references hosts, and both reference
// the deployment. Registering them is therefore not bookkeeping that can be
// skipped - it is the precondition for taking a lease at all, which is why
// AcquireHostLease refuses an instance it cannot find (ErrUnknownInstance).
//
// It is idempotent: every push calls it, and a machine that has published
// before updates last_seen_at and its own identity.
//
// Identity is newest-wins by update, with no history retained (decision 8, and
// see migrations/0004 for why an audit trail would be the wrong feature here).
// hosts.created_at is left alone by ON CONFLICT DO NOTHING semantics elsewhere
// and by omission here, which is what keeps it meaning first-seen.
func Register(ctx context.Context, db *sql.DB, deploymentID, hostID, instanceID string, identity HostIdentity) error {
	if deploymentID == "" || hostID == "" || instanceID == "" {
		return errors.New("register: deployment, host, and instance ids are all required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("register: begin: %w", err)
	}
	defer tx.Rollback()

	// The deployment's recorded schema_version is written here, at first
	// registration, which is what `storage verify` reports as the version the
	// fleet was bootstrapped as. Later registrations leave it alone: an older
	// binary must not quietly rewind a deployment a newer one recorded.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deployments (deployment_id, schema_version) VALUES ($1, $2)
		ON CONFLICT (deployment_id) DO NOTHING`, deploymentID, SchemaVersion); err != nil {
		return fmt.Errorf("register deployment: %w", err)
	}
	// Host identity is asserted here, newest value wins (decision 8). Each
	// field is coalesced so an empty assertion is silence rather than an
	// erasure: a binary that supplies no display name, or an operator who has
	// set none, must not blank a name this host published earlier.
	//
	// identity_updated_at advances only when something was actually asserted,
	// which is what lets an operator tell "this host has never reported its
	// identity" from "it reported this identity at that time". It is server
	// time for the same reason every other timestamp here is: a machine with a
	// skewed clock must not be able to claim its name is the newer one.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hosts (host_id, deployment_id, display_name, os, arch, identity_updated_at)
		VALUES ($1, $2, $3, $4, $5,
		        CASE WHEN $3::text IS NULL AND $4::text IS NULL AND $5::text IS NULL
		             THEN NULL ELSE `+serverNow+` END)
		ON CONFLICT (host_id) DO UPDATE
		   SET display_name        = coalesce(excluded.display_name, hosts.display_name),
		       os                  = coalesce(excluded.os, hosts.os),
		       arch                = coalesce(excluded.arch, hosts.arch),
		       identity_updated_at = CASE
		           WHEN excluded.display_name IS NULL
		            AND excluded.os           IS NULL
		            AND excluded.arch         IS NULL
		           THEN hosts.identity_updated_at
		           ELSE `+serverNow+` END`,
		hostID, deploymentID, nullable(identity.DisplayName),
		nullable(identity.OS), nullable(identity.Arch)); err != nil {
		return fmt.Errorf("register host: %w", err)
	}
	// last_seen_at is server time, like every other timestamp here: an
	// instance with a skewed clock must not be able to claim it checked in
	// later than it did.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO instances (instance_id, deployment_id, last_seen_at)
		VALUES ($1, $2, `+serverNow+`)
		ON CONFLICT (instance_id) DO UPDATE SET last_seen_at = `+serverNow, instanceID, deploymentID); err != nil {
		return fmt.Errorf("register instance: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("register: commit: %w", err)
	}
	return nil
}

// NextPublicationOrder returns the order value for this host's next snapshot.
//
// publication_order totally orders one host's snapshots (migrations/0001), and
// the owning host assigns it. It is derived from the catalog rather than from a
// local counter so a host that lost its local state cannot restart at 1 and
// collide with, or appear older than, its own history.
//
// The value is advisory until PublishSnapshot commits: two concurrent attempts
// for the same host would compute the same number, and the UNIQUE
// (host_id, publication_order) constraint is what actually rejects the loser.
// A host lease already makes that race unusual rather than routine.
func NextPublicationOrder(ctx context.Context, db *sql.DB, hostID string) (int64, error) {
	var next int64
	if err := db.QueryRowContext(ctx, `
		SELECT coalesce(max(publication_order), 0) + 1
		  FROM snapshots WHERE host_id = $1`, hostID).Scan(&next); err != nil {
		return 0, fmt.Errorf("read publication order: %w", err)
	}
	return next, nil
}

// SnapshotStates maps every snapshot id the catalog holds to its commit state.
//
// It exists so an operator can ask what is uncatalogued without pushing.
// Phase A keeps no local journal of that, deliberately: the repository lists
// what exists and the catalog lists what it has recorded, so the difference is
// derivable from the two authorities. A third local copy could be lost - the
// local database is explicitly rebuildable - or go stale when another instance
// reconciles, and would then disagree with both.
func SnapshotStates(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT snapshot_id, commit_state FROM snapshots`)
	if err != nil {
		return nil, fmt.Errorf("read snapshot states: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, fmt.Errorf("scan snapshot state: %w", err)
		}
		out[id] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read snapshot states: %w", err)
	}
	return out, nil
}

// Unreachable reports whether an error means the catalog could not be reached,
// rather than that it refused what it was asked to do.
//
// The distinction decides whether a push degrades to catalog-pending or fails.
// An outage is recoverable: the snapshot is already durable in the repository,
// and reconciliation restores catalog visibility from the repository's snapshot
// list without republishing bytes (SPEC.md 9). Authentication, privilege,
// schema, and constraint failures are not recoverable that way - reconciliation
// would hit exactly the same wall - so degrading them would hide a
// misconfiguration behind a state that resolves itself, which is worse than a
// failed push.
//
// The rule is whether PostgreSQL answered. A pgconn.PgError means the server
// received the request and rejected it, including a wrong password, and that is
// a refusal however it is wrapped. Anything else that is a network or timeout
// error means the conversation never happened.
//
// Connection failures arrive with the sentinel because redaction deliberately
// destroys their error chain (see redactDSN): Open classifies while it still
// has the original error and carries the answer forward, since the alternative
// - unwrapping to an error whose text may hold the DSN - is a leak waiting for
// one careless %v.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnreachable) {
		return true
	}
	return transportFailure(err)
}

// transportFailure inspects an intact error chain. It is what Open evaluates
// before redaction, and what query-path errors - which are wrapped normally and
// keep their pgx chain - are judged by directly.
func transportFailure(err error) bool {
	// A server-side error outranks the transport error it may be wrapped in:
	// pgx reports a rejected password as a connect failure carrying a PgError.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	for _, target := range []error{
		os.ErrDeadlineExceeded,
		net.ErrClosed,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	var connErr *pgconn.ConnectError
	return errors.As(err, &connErr)
}
