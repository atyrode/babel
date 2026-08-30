package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/atyrode/babel/internal/config"
)

// RepoSnapshot is one entry from the repository's own snapshot list - what
// `restic snapshots` reports, as internal/restic.Snapshot exposes it. It is
// archive truth: this catalog is derived from it, never the other way round
// (SPEC.md 9).
//
// Host is the snapshot's recorded host, which equals Babel's operator-assigned
// host ID because `restic backup --host` is passed that ID. It is carried here
// so attribution can be checked rather than assumed: a snapshot whose Host does
// not match the host being reconciled is refused, never adopted. That matters
// because when BABEL_HOST_ID is unset restic falls back to the machine's system
// hostname, and adopting one of those would put infrastructure identity into
// the shared catalog - outside the SPEC.md 9 allowlist.
//
// Counts is the summary restic stored with the snapshot, or nil when the record
// has none. restic does keep these counts in the snapshot list, so a rebuilt
// snapshot carries its real file counts and bytes rather than zeros. Session
// rows are the part that genuinely cannot be reconstructed from the listing,
// which is why a rebuilt host stays catalog-pending.
type RepoSnapshot struct {
	SnapshotID string
	Host       string
	Time       time.Time
	Counts     *SnapshotCounts
}

// SnapshotCounts mirrors the allowlisted measures Babel records for a snapshot.
// A nil *SnapshotCounts means restic recorded none, which is distinct from all
// four being zero.
type SnapshotCounts struct {
	FilesNew        int64
	FilesChanged    int64
	FilesUnmodified int64
	BytesAdded      int64
}

// ErrHostMismatch reports a snapshot attributed to a different host than the one
// being reconciled.
var ErrHostMismatch = errors.New("snapshot belongs to a different host")

// ReconcileReport describes what reconciliation found. Counts rather than
// prose, so a caller can decide whether the fleet needs attention.
type ReconcileReport struct {
	// Added counts snapshots the repository holds that the catalog did not.
	// They are recorded as catalog-pending: their session rows are unknown
	// until the owning host pushes again or a restore-and-rescan runs.
	Added int
	// Confirmed counts snapshots present in both.
	Confirmed int
	// MissingFromRepository lists catalog snapshots the repository no longer
	// reports. Retention is append-only and Babel never prunes, so this is an
	// anomaly worth surfacing - not something to clean up automatically.
	MissingFromRepository []string
}

// Reconcile makes the catalog agree with the repository's snapshot list for one
// host, without deleting anything.
//
// It is safe to run from any authorized instance and needs no lease: it only
// adds snapshots the repository already committed, and records that a check
// happened. A host's own publications remain the way session rows arrive.
func Reconcile(ctx context.Context, db *sql.DB, hostID string, repo []RepoSnapshot) (ReconcileReport, error) {
	var rep ReconcileReport

	if err := checkAttribution(hostID, repo); err != nil {
		return rep, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("reconcile: begin: %w", err)
	}
	defer tx.Rollback()

	known, maxOrder, err := knownSnapshots(ctx, tx, hostID)
	if err != nil {
		return rep, err
	}

	// Deterministic order so publication_order assignment is reproducible when
	// the same repository state is reconciled twice.
	pending := append([]RepoSnapshot(nil), repo...)
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Time.Equal(pending[j].Time) {
			return pending[i].SnapshotID < pending[j].SnapshotID
		}
		return pending[i].Time.Before(pending[j].Time)
	})

	seen := make(map[string]bool, len(pending))
	for _, s := range pending {
		seen[s.SnapshotID] = true
		if known[s.SnapshotID] {
			rep.Confirmed++
			continue
		}
		maxOrder++
		// nil counts become SQL NULL, not zeros: a snapshot whose restic record
		// carries no summary has counts that are unknown, and claiming zero
		// would assert the snapshot backed up nothing.
		var filesNew, filesChanged, filesUnmodified, bytesAdded any
		if n := s.Counts; n != nil {
			filesNew, filesChanged = n.FilesNew, n.FilesChanged
			filesUnmodified, bytesAdded = n.FilesUnmodified, n.BytesAdded
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO snapshots (snapshot_id, host_id, publication_order, snapshot_time,
			                       commit_state, files_new, files_changed,
			                       files_unmodified, bytes_added, reconciled_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, `+serverNow+`)`,
			s.SnapshotID, hostID, maxOrder, s.Time, CommitPending,
			filesNew, filesChanged, filesUnmodified, bytesAdded); err != nil {
			return rep, fmt.Errorf("reconcile: record snapshot: %w", err)
		}
		rep.Added++
	}

	for id := range known {
		if !seen[id] {
			rep.MissingFromRepository = append(rep.MissingFromRepository, id)
		}
	}
	sort.Strings(rep.MissingFromRepository)

	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshots SET reconciled_at = `+serverNow+` WHERE host_id = $1`,
		hostID); err != nil {
		return rep, fmt.Errorf("reconcile: stamp reconciled_at: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("reconcile: commit: %w", err)
	}
	return rep, nil
}

func knownSnapshots(ctx context.Context, tx *sql.Tx, hostID string) (map[string]bool, int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT snapshot_id, publication_order FROM snapshots WHERE host_id = $1`, hostID)
	if err != nil {
		return nil, 0, fmt.Errorf("reconcile: read snapshots: %w", err)
	}
	defer rows.Close()

	known := make(map[string]bool)
	var maxOrder int64
	for rows.Next() {
		var id string
		var order int64
		if err := rows.Scan(&id, &order); err != nil {
			return nil, 0, fmt.Errorf("reconcile: scan snapshot: %w", err)
		}
		known[id] = true
		if order > maxOrder {
			maxOrder = order
		}
	}
	return known, maxOrder, rows.Err()
}

// Rebuild reconstructs a host's derived rows from the repository snapshot list
// alone, discarding what the catalog held for that host.
//
// It is the repair path, not the ordinary recovery path. An empty catalog needs
// nothing but `storage migrate` and each host's next push: Register plus
// Reconcile adopt every snapshot the repository reports, which is what the
// acceptance suite exercises. Rebuild exists for the case that cannot fix
// itself - rows for a host that are present but wrong - and `babel storage
// rebuild` is what invokes it, explicitly and per host, because it is
// destructive to *derived* state: it never touches the repository, and it never
// removes a snapshot the repository still reports.
//
// Session rows cannot be reconstructed from the snapshot list, because their
// sizes and counts come from the sessions themselves. So a rebuilt host arrives
// as catalog-pending snapshots with no session rows, and session identity
// returns with the owning host's next push or a restore-and-rescan - which is
// exactly what SPEC.md 9 specifies.
func Rebuild(ctx context.Context, db *sql.DB, deploymentID, hostID string, repo []RepoSnapshot) (ReconcileReport, error) {
	var rep ReconcileReport

	if err := checkAttribution(hostID, repo); err != nil {
		return rep, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("rebuild: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deployments (deployment_id, schema_version) VALUES ($1, $2)
		ON CONFLICT (deployment_id) DO NOTHING`, deploymentID, SchemaVersion); err != nil {
		return rep, fmt.Errorf("rebuild: ensure deployment: %w", err)
	}
	// DO NOTHING, so a rebuild preserves the host's identity and first-seen
	// time rather than asserting them. Rebuild may be run from any instance
	// against any host, and this one does not know another machine's display
	// name, operating system or architecture; overwriting them with what this
	// process happens to be would be a lie about a machine (migrations/0004).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hosts (host_id, deployment_id) VALUES ($1, $2)
		ON CONFLICT (host_id) DO NOTHING`, hostID, deploymentID); err != nil {
		return rep, fmt.Errorf("rebuild: ensure host: %w", err)
	}

	// Order matters: sessions and idempotency keys reference snapshots.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE host_id = $1`, hostID); err != nil {
		return rep, fmt.Errorf("rebuild: clear sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM idempotency_keys
		 WHERE snapshot_id IN (SELECT snapshot_id FROM snapshots WHERE host_id = $1)`,
		hostID); err != nil {
		return rep, fmt.Errorf("rebuild: clear idempotency keys: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE host_id = $1`, hostID); err != nil {
		return rep, fmt.Errorf("rebuild: clear snapshots: %w", err)
	}

	ordered := append([]RepoSnapshot(nil), repo...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Time.Equal(ordered[j].Time) {
			return ordered[i].SnapshotID < ordered[j].SnapshotID
		}
		return ordered[i].Time.Before(ordered[j].Time)
	})

	// publication_order is rederived from repository time ordering. The host's
	// next push reasserts its own numbering; until then this ordering is what
	// readers use to find a newest snapshot.
	for i, s := range ordered {
		var filesNew, filesChanged, filesUnmodified, bytesAdded any
		if n := s.Counts; n != nil {
			filesNew, filesChanged = n.FilesNew, n.FilesChanged
			filesUnmodified, bytesAdded = n.FilesUnmodified, n.BytesAdded
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO snapshots (snapshot_id, host_id, publication_order, snapshot_time,
			                       commit_state, files_new, files_changed,
			                       files_unmodified, bytes_added, reconciled_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, `+serverNow+`)`,
			s.SnapshotID, hostID, int64(i+1), s.Time, CommitPending,
			filesNew, filesChanged, filesUnmodified, bytesAdded); err != nil {
			return rep, fmt.Errorf("rebuild: record snapshot: %w", err)
		}
		rep.Added++
	}

	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("rebuild: commit: %w", err)
	}
	return rep, nil
}

// checkAttribution refuses a listing that mixes hosts, carries a host the
// caller did not ask for, or names a host id the rest of Babel would reject.
//
// Attribution is the caller's job - it filters the repository listing - but a
// mistake there would silently write another machine's snapshots under this
// host. An empty Host is refused for the same reason: it would mean the
// snapshot was taken without `--host`, so its identity is unknown rather than
// merely absent.
//
// Shape validation reuses config.ValidHostID, the same rule --host,
// BABEL_HOST_ID, and storage.json already enforce, so a malformed identity
// cannot reach a primary key through this path. Note what it does not do: a
// machine's system hostname is usually a valid host id, so shape alone cannot
// tell an operator-chosen identity from an infrastructure one. Keeping
// infrastructure identity out of the shared catalog rests on the operator
// supplying BABEL_HOST_ID, and on the mismatch check below refusing to adopt
// snapshots recorded under anything else.
func checkAttribution(hostID string, repo []RepoSnapshot) error {
	if !config.ValidHostID(hostID) {
		return fmt.Errorf("invalid host id %q", hostID)
	}
	for _, s := range repo {
		if s.Host == "" {
			return fmt.Errorf("%w: snapshot %s records no host", ErrHostMismatch, s.SnapshotID)
		}
		if s.Host != hostID {
			return fmt.Errorf("%w: snapshot %s is attributed to %q, reconciling %q",
				ErrHostMismatch, s.SnapshotID, s.Host, hostID)
		}
	}
	return nil
}
