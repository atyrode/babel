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
// so attribution is recorded rather than assumed: an adopted row names the
// host restic says produced the snapshot, never the host that adopted it.
// Reconcile takes one host's listing and refuses any other's, which is what
// keeps a caller's filter mistake from writing another machine's snapshots
// under this one; AdoptForeign takes the whole repository and writes each
// snapshot under its own host, which is how a snapshot a retired or idle
// machine stranded is catalogued at all (SPEC.md 9.1). Neither adopts a
// snapshot that names no host: that would mean the snapshot was taken without
// `--host`, so its identity is unknown rather than merely absent.
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
	// They are recorded as catalog-pending: their session rows are not in the
	// listing, so they arrive unknown and a push's restore-and-rescan or the
	// owning host's next publication is what supplies them.
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
// It only adds snapshots the repository already committed, and records that a
// check happened. Writing one host's rows still needs that host's publication
// lease, because publication_order is assigned from the host's own current
// maximum and two instances computing it at once would collide on UNIQUE
// (host_id, publication_order): a push holds its own lease for the whole
// catalog phase, and AdoptForeign takes each other host's for the length of
// one adoption. A host's own publications remain the way session rows arrive,
// or a restore-and-rescan recovers them.
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
// tell an operator-chosen identity from an infrastructure one, and nothing
// here can. Keeping infrastructure identity out of the shared catalog rests
// on the operator supplying BABEL_HOST_ID on every machine: `archive push`
// falls back to the system hostname when he has not, so such a name would
// reach the catalog as that host's own primary key long before any other
// instance saw the snapshot. What this check does guarantee is narrower and
// still worth having - a snapshot naming no host is never adopted, and a
// snapshot naming a host the rest of Babel would reject is never adopted.
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

// ForeignAdoption is what one instance's pass over the other machines'
// snapshots achieved.
//
// It is reported per host rather than as one number because the host is the
// unit of write authority: adoption writes another machine's rows under that
// machine's publication lease, so a host some instance is publishing for right
// now is skipped rather than waited for, and a host whose adoption failed must
// not cost the operator the rest of the fleet.
type ForeignAdoption struct {
	// Adopted counts snapshots recorded across every host in this pass.
	Adopted int
	// Hosts names the hosts something was adopted for, in order.
	Hosts []string
	// Deferred names the hosts whose lease another instance held. Nothing is
	// owed: that instance's own push adopts them, or the next pass does.
	Deferred []string
	// Failed carries one "host: reason" per host whose adoption failed, so a
	// single misconfigured or mid-migration host is reported rather than
	// silently dropped.
	Failed []string
	// Refused names the snapshots that identify no host Babel would accept -
	// an empty host, or one config.ValidHostID rejects. They are named rather
	// than adopted, and named rather than fatal: a nameless snapshot is an
	// anomaly in the repository, and failing the whole pass over it would
	// strand every other machine's snapshots behind it.
	Refused []string
}

// AdoptForeign records every snapshot the repository holds for a host other
// than the caller's that the catalog has no row for.
//
// This is the archive half of SPEC.md 9.1: a record Babel produced reaches the
// shared catalog without an operator action. Reconcile alone could not deliver
// that, because it adopts one host's snapshots and the caller can only pass its
// own: a snapshot stranded by a machine that has since been retired, has died,
// or is simply idle would then wait for a push that never comes. Any pushing
// host now drains the whole repository, and the hourly timer is what runs it.
//
// Attribution stays truthful in both directions. Each adopted row names the
// host restic recorded, and publication_order is taken from that host's own
// current maximum - never mixed between hosts, because the column totally
// orders one host's snapshots (migrations/0001_init.sql) and interleaving two
// machines' numbering would make a reader's "newest" meaningless.
//
// Concurrency is handled with the mechanism that already exists for it rather
// than a new one: writing a host's rows requires that host's publication lease,
// so two instances pushing at once cannot both adopt one snapshot into
// conflicting orders. The loser of the race gets ErrLeaseHeld for that host and
// reports it deferred. selfHost is refused in the listing rather than adopted
// for the same reason: the caller already holds its own lease, and re-acquiring
// it here would mint a fresh fence and invalidate the publication in flight.
func AdoptForeign(ctx context.Context, db *sql.DB, deploymentID, instanceID, selfHost string,
	repo []RepoSnapshot, ttl time.Duration) (ForeignAdoption, error) {
	var rep ForeignAdoption
	if deploymentID == "" || instanceID == "" || selfHost == "" {
		return rep, errors.New("adopt foreign snapshots: deployment, instance, and host ids are all required")
	}

	byHost := make(map[string][]RepoSnapshot)
	var hosts []string
	for _, s := range repo {
		if s.Host == "" || !config.ValidHostID(s.Host) {
			rep.Refused = append(rep.Refused, s.SnapshotID)
			continue
		}
		if s.Host == selfHost {
			return rep, fmt.Errorf("adopt foreign snapshots: snapshot %s is attributed to %q, the adopting host",
				s.SnapshotID, s.Host)
		}
		if _, seen := byHost[s.Host]; !seen {
			hosts = append(hosts, s.Host)
		}
		byHost[s.Host] = append(byHost[s.Host], s)
	}
	sort.Strings(hosts)
	sort.Strings(rep.Refused)

	for _, host := range hosts {
		added, err := adoptOneHost(ctx, db, deploymentID, instanceID, host, byHost[host], ttl)
		switch {
		case err == nil:
			if added > 0 {
				rep.Adopted += added
				rep.Hosts = append(rep.Hosts, host)
			}
		case errors.Is(err, ErrLeaseHeld), errors.Is(err, ErrLeaseLost):
			rep.Deferred = append(rep.Deferred, host)
		case Unreachable(err):
			// The conversation with PostgreSQL has stopped, so no later host in
			// this pass can succeed either. Reporting the outage once beats
			// reporting it per host.
			return rep, err
		default:
			rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %s", host, err))
		}
	}
	return rep, nil
}

// adoptOneHost adopts one other machine's snapshots under that machine's lease.
func adoptOneHost(ctx context.Context, db *sql.DB, deploymentID, instanceID, hostID string,
	repo []RepoSnapshot, ttl time.Duration) (int, error) {
	// The host row is the precondition for both what follows: host_leases and
	// snapshots both reference it. It asserts nothing about the machine - no
	// display name, no operating system, no architecture - for the reason
	// Rebuild gives: this process does not know another machine's identity, and
	// writing what this one happens to be would be a lie about a machine
	// (migrations/0004). Those columns stay NULL until that host registers.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO hosts (host_id, deployment_id) VALUES ($1, $2)
		ON CONFLICT (host_id) DO NOTHING`, hostID, deploymentID); err != nil {
		return 0, fmt.Errorf("record host: %w", err)
	}

	lease, err := AcquireHostLease(ctx, db, hostID, instanceID, ttl)
	if err != nil {
		return 0, err
	}
	defer func() {
		// Released early so the owning host's next push does not wait out the
		// TTL, and a failure to release is not worth losing an adoption that
		// already committed: the lease expires on its own.
		_ = ReleaseHostLease(ctx, db, lease)
	}()

	rep, err := Reconcile(ctx, db, hostID, repo)
	return rep.Added, err
}

// PendingSnapshot is one snapshot the catalog holds with restic's real counts
// and no record of which sessions it held.
//
// It is what an outage, a rebuild, or an adoption leaves behind, and what a
// restore-and-rescan completes. Order is the host's own publication order, so a
// caller draining the backlog can take a host's oldest first.
type PendingSnapshot struct {
	SnapshotID string
	HostID     string
	Order      int64
}

// PendingSnapshots lists every catalog-pending row, by host and then by that
// host's publication order.
//
// The whole fleet's rows are returned rather than one host's, because the work
// they name is not the owning host's to do: restoring a snapshot and rescanning
// it needs the repository and the adapters, both of which every authorized
// instance has, and waiting for the owning machine is exactly the wait SPEC.md
// 9.1 refuses.
func PendingSnapshots(ctx context.Context, db *sql.DB) ([]PendingSnapshot, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT snapshot_id, host_id, publication_order
		  FROM snapshots
		 WHERE commit_state = $1
		 ORDER BY host_id, publication_order`, CommitPending)
	if err != nil {
		return nil, fmt.Errorf("read catalog-pending snapshots: %w", err)
	}
	defer rows.Close()

	var out []PendingSnapshot
	for rows.Next() {
		var s PendingSnapshot
		if err := rows.Scan(&s.SnapshotID, &s.HostID, &s.Order); err != nil {
			return nil, fmt.Errorf("scan catalog-pending snapshot: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog-pending snapshots: %w", err)
	}
	return out, nil
}
