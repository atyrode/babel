package sharedcatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Commit states a snapshot row may carry (SPEC.md 9).
const (
	// CommitPending means restic holds the snapshot but its session rows are
	// incomplete. Any authorized instance may reconcile it.
	CommitPending = "catalog-pending"
	// CommitCommitted means the snapshot and its session rows are published.
	CommitCommitted = "committed"
)

// SnapshotRow is one restic snapshot as the shared catalog records it. Every
// field is inside the Phase A allowlist: opaque locators, ordering, counts,
// sizes, commit state, and timestamps.
type SnapshotRow struct {
	SnapshotID       string
	PublicationOrder int64
	SnapshotTime     time.Time
	CommitState      string
	FilesNew         int64
	FilesChanged     int64
	FilesUnmodified  int64
	BytesAdded       int64
	SessionCount     int
	PublishedBy      string
}

// SessionRow is one session as the shared catalog records it: its opaque
// identity, its measures, and the browsable metadata migrations/0004 admits.
//
// It still carries no selector and no adapter source id. Those are the fetch
// locator, and keeping them out is what makes resolving a uid to something
// fetchable require the repository or a local index (migrations/0001_init.sql).
//
// Title, Workspace, and ContinuationGrade are pointers because absent is a
// distinct answer from empty and, for the grade, from false. Only the
// publishing host can resolve them - they come from its own local sources - so
// a nil here is what every other instance reads until this host pushes.
type SessionRow struct {
	SessionUID          string
	Harness             string
	PrimarySize         int64
	ArtifactCount       int
	BlobCount           int
	UnresolvedBlobCount int
	SourceModifiedAt    *time.Time
	Title               *string
	// TitleProvenance says whether Title was recorded by the harness, derived
	// by Babel, or inferred by a model. It travels with the title because this
	// row is what an instance on another machine reads instead of the session:
	// it cannot re-derive the value, so a title here without its origin is a
	// claim it has no way to check. NULL means unknown - notably for every row
	// written before this column existed - and never "recorded".
	TitleProvenance   *string
	Workspace         *string
	ContinuationGrade *bool
}

// SessionUID derives a session's opaque catalog identity.
//
// The inputs include the real source id, which embeds a workspace-derived
// project slug - so the digest, not the inputs, is what reaches PostgreSQL. It
// is stable across pushes for the same session, and distinct across hosts and
// deployments, so two machines with identically named projects never collide.
func SessionUID(deploymentID, hostID, harness, sourceID string) string {
	h := sha256.New()
	// Length-prefix each field so ("a","bc") cannot collide with ("ab","c").
	for _, part := range []string{deploymentID, hostID, harness, sourceID} {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PublishSnapshot records one snapshot and its session rows under a fenced
// lease, exactly once.
//
// It reports whether the write was applied: a repeated call with the same
// idempotency key is a no-op returning false, which is what makes a retried
// push after a network failure safe.
//
// The lease is validated with a row lock inside the same transaction as the
// writes, so a takeover cannot commit between validation and publication: a
// stealing instance must update the lease row, and that row is locked until this
// transaction ends. A writer whose fence is stale is refused and lands nothing.
//
// Both validations assert the lease is still live, so a publication whose lease
// expired while a long push was in flight lands nothing either.
func PublishSnapshot(
	ctx context.Context,
	db *sql.DB,
	l Lease,
	idempotencyKey string,
	snap SnapshotRow,
	sessions []SessionRow,
) (applied bool, err error) {
	if idempotencyKey == "" {
		return false, fmt.Errorf("publish snapshot: idempotency key is required")
	}
	if snap.CommitState != CommitPending && snap.CommitState != CommitCommitted {
		return false, fmt.Errorf("publish snapshot: invalid commit state %q", snap.CommitState)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("publish snapshot: begin: %w", err)
	}
	defer tx.Rollback()

	if err := checkLease(ctx, tx, l); err != nil {
		return false, err
	}

	// Claim the key before writing anything. If another attempt already claimed
	// it, this publication has happened and must not be repeated.
	//
	// snapshot_id is backfilled after the snapshot row exists: it references
	// snapshots, and claiming the key first is the property worth keeping -
	// two concurrent attempts with the same key must not both proceed to write.
	var claimed bool
	err = tx.QueryRowContext(ctx, `
		INSERT INTO idempotency_keys (idempotency_key, instance_id)
		VALUES ($1, $2)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING true`,
		idempotencyKey, l.HolderID).Scan(&claimed)
	if err == sql.ErrNoRows {
		// Already published. Commit so the lease lock is released promptly.
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("publish snapshot: commit no-op: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("publish snapshot: claim idempotency key: %w", err)
	}

	// On conflict the counts are overwritten too: a snapshot first recorded by
	// reconciliation carries NULL counts when restic stored no summary, and the
	// owning host's push is the authority that replaces them.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO snapshots (snapshot_id, host_id, publication_order, snapshot_time,
		                       commit_state, files_new, files_changed, files_unmodified,
		                       bytes_added, session_count, published_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (snapshot_id) DO UPDATE
		   SET commit_state     = excluded.commit_state,
		       files_new        = excluded.files_new,
		       files_changed    = excluded.files_changed,
		       files_unmodified = excluded.files_unmodified,
		       bytes_added      = excluded.bytes_added,
		       session_count    = excluded.session_count,
		       published_by     = excluded.published_by,
		       updated_at       = now()`,
		snap.SnapshotID, l.HostID, snap.PublicationOrder, snap.SnapshotTime,
		snap.CommitState, snap.FilesNew, snap.FilesChanged, snap.FilesUnmodified,
		snap.BytesAdded, snap.SessionCount, snap.PublishedBy); err != nil {
		return false, fmt.Errorf("publish snapshot: upsert snapshot: %w", err)
	}

	for _, s := range sessions {
		// first_snapshot_id records where a session was first seen and is never
		// rewritten; latest_snapshot_id moves forward with each publication.
		//
		// title, workspace and continuation_grade are overwritten from this
		// push rather than coalesced with what the row held. The publishing
		// host is the authority on its own sessions: a renamed workspace or a
		// session that stopped being continuable must be able to say so, and
		// coalescing would make the first value ever published permanent. A
		// push cannot silently blank them by failing to describe - a session
		// whose describe fails is pruned from the local cache and is not
		// published at all (internal/catalog.Refresh).
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (session_uid, host_id, harness, first_snapshot_id,
			                      latest_snapshot_id, primary_size, artifact_count,
			                      blob_count, unresolved_blob_count, source_modified_at,
			                      title, title_provenance, workspace, continuation_grade)
			VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (session_uid) DO UPDATE
			   SET latest_snapshot_id    = excluded.latest_snapshot_id,
			       primary_size          = excluded.primary_size,
			       artifact_count        = excluded.artifact_count,
			       blob_count            = excluded.blob_count,
			       unresolved_blob_count = excluded.unresolved_blob_count,
			       source_modified_at    = excluded.source_modified_at,
			       title                 = excluded.title,
			       title_provenance      = excluded.title_provenance,
			       workspace             = excluded.workspace,
			       continuation_grade    = excluded.continuation_grade,
			       updated_at            = now()`,
			s.SessionUID, l.HostID, s.Harness, snap.SnapshotID,
			s.PrimarySize, s.ArtifactCount, s.BlobCount, s.UnresolvedBlobCount,
			s.SourceModifiedAt, s.Title, s.TitleProvenance, s.Workspace, s.ContinuationGrade); err != nil {
			return false, fmt.Errorf("publish snapshot: upsert session: %w", err)
		}
	}

	// Now that the snapshot row exists, point the claimed key at it so a later
	// audit can tell which publication a key belongs to.
	if _, err := tx.ExecContext(ctx,
		`UPDATE idempotency_keys SET snapshot_id = $1 WHERE idempotency_key = $2`,
		snap.SnapshotID, idempotencyKey); err != nil {
		return false, fmt.Errorf("publish snapshot: link idempotency key: %w", err)
	}

	if publishDelayForTests != nil {
		publishDelayForTests()
	}

	// Revalidate before committing, still holding the row lock.
	//
	// The lock guarantees no other instance wrote this host's rows meanwhile,
	// but it does not stop this instance's own lease from expiring mid-flight:
	// without this check a slow publisher could hold the lock past its TTL and
	// commit anyway, so the TTL would bound nothing. Re-evaluating expires_at
	// against server time makes the documented invariant true - a lease that
	// expired during publication lands nothing.
	if err := checkLease(ctx, tx, l); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("publish snapshot: commit: %w", err)
	}
	return true, nil
}

// publishDelayForTests runs after the row upserts and before the final lease
// revalidation. It exists so the refused-mid-publication paths can be exercised
// deterministically: with the lease row locked, no other connection can move
// expires_at, so only real elapsed time can make a lease expire in flight.
var publishDelayForTests func()
