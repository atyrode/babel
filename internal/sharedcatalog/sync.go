package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/atyrode/babel/internal/envelope"
)

// Sync states of a Phase B run (SPEC.md 6.5, 9). They deliberately match
// internal/run's local vocabulary: a record staged locally is pending-sync
// until the shared backend holds it, and the two stores must not describe the
// same condition with different words.
//
// SyncCommitted shares its value with CommitCommitted by coincidence of English
// rather than by design. They answer different questions - whether a restic
// snapshot's session rows are complete, versus whether an analysis run's rows
// and objects have both committed remotely - and live in different columns.
const (
	SyncPending   = "pending-sync"
	SyncCommitted = "committed"
)

// RecordKind names a Phase B record type the shared catalog carries. SPEC.md 9
// admits entity kind in plaintext, and the vocabulary is closed: the database
// CHECK in migrations/0003 holds exactly these values, so a new record type
// reaching PostgreSQL costs a migration and a review.
type RecordKind string

// The kinds Phase B commits. The first six are internal/frontier's records and
// their append-only review material, the last two internal/run's; Context is
// attributed operator guidance (SPEC.md 4.7), which is a durable record of its
// own even though it can never satisfy an evidence requirement.
const (
	KindHypothesis  RecordKind = "hypothesis"
	KindObservation RecordKind = "observation"
	KindFinding     RecordKind = "finding"
	KindProposal    RecordKind = "proposal"
	KindLink        RecordKind = "link"
	KindDisposition RecordKind = "disposition"
	KindContext     RecordKind = "context"
	KindPreparation RecordKind = "preparation"
	KindReceipt     RecordKind = "receipt"
)

func (k RecordKind) valid() bool {
	switch k {
	case KindHypothesis, KindObservation, KindFinding, KindProposal,
		KindLink, KindDisposition, KindContext, KindPreparation, KindReceipt:
		return true
	}
	return false
}

var (
	// ErrIncompleteClosure reports a run whose declared record closure is not
	// all present, so it stays pending-sync. SPEC.md 6.5 commits a run only
	// when its rows and objects have both committed; a partial commit is not a
	// commit, and reporting one would make a half-synced run globally
	// reviewable.
	ErrIncompleteClosure = errors.New("analysis run closure is incomplete")

	// ErrRunConflict reports a run id the catalog already holds with a
	// different identity or a different declared closure. Run ids are global
	// and client-generated, so this means two different runs were given one id
	// - which silently accepting would corrupt lineage for every instance.
	ErrRunConflict = errors.New("analysis run is already recorded with different identity")

	// ErrRecordConflict reports a record id the catalog already holds under a
	// different run, for the same reason: identity is global.
	ErrRecordConflict = errors.New("analysis record is already recorded under a different run")

	// ErrRunNotFound reports a run the catalog does not hold.
	ErrRunNotFound = errors.New("analysis run not found")
)

// validRecordID bounds every client-generated Phase B identifier. It is
// stricter than PostgreSQL needs because these ids are also spliced into
// object-store keys: a value containing a path separator or a traversal segment
// could name an object outside the namespace Babel manages, and a store that
// happens to be filesystem-backed would honour it.
var validRecordID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// StagedRecord is one Phase B durable record on its way to the shared backend.
//
// Payload is plaintext, and that is deliberate: this package seals it (see
// sealRecord) rather than accepting something a caller claims is already
// sealed. There is no API here that puts unsealed bytes anywhere remote.
type StagedRecord struct {
	// RecordID is globally unique and client-generated (SPEC.md 9). It is the
	// idempotency key: a second commit of the same id is a no-op.
	RecordID string
	Kind     RecordKind
	// Schema is the record type's own schema version, independent of
	// SchemaVersion - a payload shape may evolve without announcing that older
	// Phase A instances must stop writing.
	Schema int
	// Ordinal totally orders the run's closure, so a reader reconstructs the
	// output in the order it was produced without trusting timestamps.
	Ordinal int64
	Payload []byte
	// Edge is the record's plaintext citation shape, and nil for every record
	// that is not a reference edge (issue #113, migrations/0008). When it is
	// set, its row is written in the same transaction as the record row: the
	// two are one fact, and a record row whose edge row is missing would be a
	// citation no reader can see and no retry will supply.
	Edge *RecordEdge
}

// RunClosure is one analysis run and the records it must commit.
//
// RecordCount is declared separately from Records because a sync may carry a
// prefix of the closure: an outage can interrupt after some records are durable
// and the rest are not, and the run must then stay pending-sync rather than
// looking complete because the caller happened to offer fewer records.
type RunClosure struct {
	RunID            string
	DeploymentID     string
	OriginInstanceID string
	// ExecutionHostID pins repository-dependent work to the host that can
	// rerun it (SPEC.md 9). Empty means unpinned. It never restricts reads.
	ExecutionHostID string
	// ContinuesRunID links this run to the committed run it continues, which
	// is how a second instance's follow-on work stays attached to the first
	// instance's output rather than merely resembling it.
	ContinuesRunID string
	RecordCount    int
	Records        []StagedRecord
}

// SyncResult reports what one sync attempt achieved. It is returned even
// alongside an error, because a partial sync is progress a retry builds on
// rather than work to redo.
type SyncResult struct {
	// State is the run's sync state after the attempt: SyncPending or
	// SyncCommitted.
	State string
	// ObjectsWritten counts sealed objects this attempt put and verified. A
	// retry that finds every record already recorded writes none.
	ObjectsWritten int
	// RecordsCommitted counts record rows this attempt inserted.
	RecordsCommitted int
	// RecordsPresent is how many of the closure the catalog holds in total.
	RecordsPresent int
}

// SyncRun commits an analysis run to the shared backend object-first and
// database-last, and is idempotent (SPEC.md 6.5, 7, 9).
//
// The ordering is the contract, and each step's failure leaves a state a retry
// can finish:
//
//  1. Declare the run as pending-sync. This row names no object, so it cannot
//     dangle, and it is what makes an interrupted sync visible to every
//     instance instead of being a fact only the producing machine knows.
//  2. For each record the catalog does not already hold: seal the payload,
//     put the sealed object, read it back and verify its digest, and only then
//     insert the row that names it. A store failure or a mismatched read-back
//     returns before any row exists, so no row can reference a missing or wrong
//     object. A database failure here leaves a durable object with no row - an
//     orphan, which is the harmless direction, and which the next attempt
//     supersedes rather than reuses.
//  3. Flip the run to committed, conditional on the catalog holding the full
//     declared closure. A short closure leaves the run pending-sync and returns
//     ErrIncompleteClosure.
//
// Idempotency is keyed on the two client-generated global ids and nothing else:
// the run id for the run row, the record id for each record row. It is not
// keyed on content, because sealing is randomized and a retry's ciphertext
// differs by construction - a digest-keyed check would reject exactly the
// retries this exists to permit. Records are immutable, so the first committed
// object for an id remains the record's content.
//
// No host lease is taken. A lease fences host rows because two instances can
// both describe the same host, and nothing here has that ambiguity: a run
// belongs to the instance that generated its id, and no other instance writes
// it. The schema-compatibility gate still applies, once per sync.
func SyncRun(ctx context.Context, db *sql.DB, store ObjectStore, ring *envelope.Keyring, c RunClosure) (SyncResult, error) {
	if store == nil {
		return SyncResult{}, errors.New("sync analysis run: object store is required")
	}
	if ring == nil {
		return SyncResult{}, errors.New("sync analysis run: keyring is required")
	}
	if err := c.validate(); err != nil {
		return SyncResult{}, fmt.Errorf("sync analysis run: %w", err)
	}
	if err := EnsureCompatible(ctx, db); err != nil {
		return SyncResult{}, err
	}

	res := SyncResult{State: SyncPending}
	if err := declareRun(ctx, db, c); err != nil {
		return res, err
	}

	for _, rec := range c.Records {
		present, err := recordPresent(ctx, db, c.RunID, rec.RecordID)
		if err != nil {
			return res, err
		}
		if present {
			continue
		}
		inserted, err := commitRecord(ctx, db, store, ring, c.RunID, rec)
		if err != nil {
			return res, err
		}
		res.ObjectsWritten++
		if inserted {
			res.RecordsCommitted++
		}
	}

	state, present, err := finishRun(ctx, db, c.RunID)
	if err != nil {
		return res, err
	}
	res.State, res.RecordsPresent = state, present
	if state != SyncCommitted {
		return res, fmt.Errorf("sync analysis run %s: %w: %d of %d records present",
			c.RunID, ErrIncompleteClosure, present, c.RecordCount)
	}
	return res, nil
}

func (c RunClosure) validate() error {
	if !validRecordID.MatchString(c.RunID) {
		return fmt.Errorf("run id must match %s", validRecordID)
	}
	if c.DeploymentID == "" || c.OriginInstanceID == "" {
		return errors.New("deployment id and origin instance id are required")
	}
	if c.ContinuesRunID != "" && !validRecordID.MatchString(c.ContinuesRunID) {
		return fmt.Errorf("continued run id must match %s", validRecordID)
	}
	if c.ContinuesRunID == c.RunID && c.RunID != "" && c.ContinuesRunID != "" {
		return errors.New("a run cannot continue itself")
	}
	if c.RecordCount < 1 {
		return errors.New("a run must declare a closure of at least one record")
	}
	if len(c.Records) > c.RecordCount {
		return fmt.Errorf("closure declares %d records but %d were offered",
			c.RecordCount, len(c.Records))
	}
	seenID := make(map[string]bool, len(c.Records))
	seenOrdinal := make(map[int64]bool, len(c.Records))
	for _, rec := range c.Records {
		if !validRecordID.MatchString(rec.RecordID) {
			return fmt.Errorf("record id must match %s", validRecordID)
		}
		if seenID[rec.RecordID] {
			return fmt.Errorf("record %s is offered twice in one closure", rec.RecordID)
		}
		seenID[rec.RecordID] = true
		if !rec.Kind.valid() {
			return fmt.Errorf("record %s has unknown kind %q", rec.RecordID, rec.Kind)
		}
		if rec.Schema < 1 {
			return fmt.Errorf("record %s must carry a schema version", rec.RecordID)
		}
		if rec.Ordinal < 0 || rec.Ordinal >= int64(c.RecordCount) {
			return fmt.Errorf("record %s has ordinal %d outside a closure of %d",
				rec.RecordID, rec.Ordinal, c.RecordCount)
		}
		if seenOrdinal[rec.Ordinal] {
			return fmt.Errorf("ordinal %d is offered twice in one closure", rec.Ordinal)
		}
		seenOrdinal[rec.Ordinal] = true
		if len(rec.Payload) == 0 {
			return fmt.Errorf("record %s has an empty payload", rec.RecordID)
		}
		if rec.Edge != nil {
			if err := rec.Edge.Validate(); err != nil {
				return fmt.Errorf("record %s: %w", rec.RecordID, err)
			}
			// An edge publishes as a link. The pairing is checked here rather
			// than assumed because the two halves are supplied separately: a
			// hypothesis offered with endpoint columns would put a citation
			// row on a record that is not one, and analysis_edges has no
			// column that says so.
			if rec.Kind != KindLink {
				return fmt.Errorf("record %s carries edge endpoints but is a %s, not a %s",
					rec.RecordID, rec.Kind, KindLink)
			}
		}
	}
	return nil
}

// declareRun writes the pending-sync run row, or verifies that the row the
// catalog already holds is the same run.
//
// The verification is what keeps global ids meaningful. Two different runs
// sharing an id would give every instance a lineage that disagrees with every
// other's, and the failure would appear as a nonsensical record set long after
// the collision.
func declareRun(ctx context.Context, db *sql.DB, c RunClosure) error {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO analysis_runs (run_id, deployment_id, origin_instance_id,
		                           execution_host_id, continues_run_id,
		                           sync_state, record_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (run_id) DO NOTHING`,
		c.RunID, c.DeploymentID, c.OriginInstanceID,
		nullable(c.ExecutionHostID), nullable(c.ContinuesRunID),
		SyncPending, c.RecordCount); err != nil {
		return fmt.Errorf("declare analysis run %s: %w", c.RunID, err)
	}

	var (
		deployment, origin string
		host, continues    sql.NullString
		count              int
	)
	if err := db.QueryRowContext(ctx, `
		SELECT deployment_id, origin_instance_id, execution_host_id,
		       continues_run_id, record_count
		  FROM analysis_runs WHERE run_id = $1`, c.RunID).
		Scan(&deployment, &origin, &host, &continues, &count); err != nil {
		return fmt.Errorf("read analysis run %s: %w", c.RunID, err)
	}
	if deployment != c.DeploymentID || origin != c.OriginInstanceID ||
		host.String != c.ExecutionHostID || continues.String != c.ContinuesRunID ||
		count != c.RecordCount {
		return fmt.Errorf("analysis run %s: %w", c.RunID, ErrRunConflict)
	}
	return nil
}

// recordPresent reports whether the catalog already holds this record, and
// refuses one recorded under a different run.
//
// Checking before sealing is what makes a retry cheap: a record whose row
// survived an interrupted sync is neither re-sealed nor re-uploaded, so
// completing a partially synced run costs only the records that are missing.
func recordPresent(ctx context.Context, db *sql.DB, runID, recordID string) (bool, error) {
	var owner string
	err := db.QueryRowContext(ctx,
		`SELECT run_id FROM analysis_records WHERE record_id = $1`, recordID).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read analysis record %s: %w", recordID, err)
	case owner != runID:
		return false, fmt.Errorf("analysis record %s: %w", recordID, ErrRecordConflict)
	}
	return true, nil
}

// commitRecord performs the object-first half of the protocol for one record:
// seal, put, verify by reading back, and only then insert the row.
//
// The read-back is not paranoia about disks. The store is a remote service
// reached over a network, and SPEC.md 6.5 requires the object to have committed
// before a row claims it; an acknowledged write that the store cannot return is
// exactly the case a row would turn into a permanent dangling reference.
func commitRecord(
	ctx context.Context,
	db *sql.DB,
	store ObjectStore,
	ring *envelope.Keyring,
	runID string,
	rec StagedRecord,
) (bool, error) {
	object, env, err := sealRecord(ring, rec)
	if err != nil {
		return false, err
	}
	digest := digestOf(object)
	key := objectKey(rec.RecordID, digest)

	if err := store.Put(ctx, key, object); err != nil {
		return false, fmt.Errorf("write sealed object for record %s: %w", rec.RecordID, err)
	}
	readBack, err := store.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("verify sealed object for record %s: %w", rec.RecordID, err)
	}
	if digestOf(readBack) != digest {
		return false, fmt.Errorf("record %s: %w", rec.RecordID, ErrObjectVerification)
	}

	// The object is durable and proven readable, so a row naming it is now
	// safe to write. ON CONFLICT covers the narrow race where another instance
	// committed the same record between the presence check and here; its
	// object stands and ours becomes an orphan, which costs storage rather
	// than correctness.
	//
	// The insert is a transaction rather than a statement because an edge
	// record writes two rows - its identity and its plaintext citation - and
	// SyncRun never revisits a record the catalog already holds, so a crash
	// between them would leave the citation invisible forever.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("record analysis record %s: begin: %w", rec.RecordID, err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO analysis_records (record_id, run_id, kind, record_schema, ordinal,
		                              object_key, key_id, ciphertext_size, object_digest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (record_id) DO NOTHING`,
		rec.RecordID, runID, string(rec.Kind), rec.Schema, rec.Ordinal,
		key, string(env.KeyID), len(env.CT), digest)
	if err != nil {
		return false, fmt.Errorf("record analysis record %s: %w", rec.RecordID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record analysis record %s: %w", rec.RecordID, err)
	}
	if err := commitEdge(ctx, tx, rec.RecordID, rec.Edge); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("record analysis record %s: %w", rec.RecordID, err)
	}
	return n == 1, nil
}

// finishRun flips a run to committed if and only if the catalog holds its whole
// declared closure, and reports the resulting state.
//
// The count and the flip are one statement so a concurrent insert cannot land
// between them, and the whole thing runs in a transaction with the read-back so
// the state reported is the state committed. Zero rows updated is not an error
// here: it means either the closure is short, or the run was already committed
// by an earlier attempt, and the subsequent read tells those apart.
func finishRun(ctx context.Context, db *sql.DB, runID string) (state string, present int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("commit analysis run %s: begin: %w", runID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE analysis_runs r
		   SET sync_state   = $2,
		       committed_at = now(),
		       updated_at   = now()
		 WHERE r.run_id = $1
		   AND r.sync_state = $3
		   AND (SELECT count(*) FROM analysis_records WHERE run_id = r.run_id) = r.record_count`,
		runID, SyncCommitted, SyncPending); err != nil {
		return "", 0, fmt.Errorf("commit analysis run %s: %w", runID, err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT r.sync_state,
		       (SELECT count(*) FROM analysis_records WHERE run_id = r.run_id)
		  FROM analysis_runs r WHERE r.run_id = $1`, runID).Scan(&state, &present); err != nil {
		return "", 0, fmt.Errorf("read analysis run %s: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("commit analysis run %s: %w", runID, err)
	}
	return state, present, nil
}

// AnalysisRunRow is what the shared catalog records about one Phase B run.
// Every field is inside SPEC.md 9's plaintext allowlist, so an instance can
// list and reason about the fleet's analysis output without a decryption key.
type AnalysisRunRow struct {
	RunID            string
	DeploymentID     string
	OriginInstanceID string
	// ExecutionHostID is the host that can rerun repository-dependent work,
	// empty when the run is not pinned. Reading is never restricted by it.
	ExecutionHostID string
	ContinuesRunID  string
	SyncState       string
	// RecordCount is the declared closure; RecordsPresent is how much of it
	// the catalog holds. They differ exactly while a run is pending-sync.
	RecordCount    int
	RecordsPresent int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// CommittedAt is nil until the run commits.
	CommittedAt *time.Time
}

// AnalysisRecordRow is one record's plaintext row: its identity, its kind and
// schema, its place in the run's order, and the reference to the sealed object
// that holds everything else.
type AnalysisRecordRow struct {
	RecordID string
	RunID    string
	Kind     RecordKind
	Schema   int
	Ordinal  int64
	// ObjectKey and ObjectDigest together name and pin the sealed object;
	// OpenRecord checks the digest before decrypting.
	ObjectKey    string
	ObjectDigest string
	// KeyID says which key opens it, so an instance can tell whether it can
	// read a record before fetching the object (SPEC.md 9).
	KeyID envelope.KeyID
	// CiphertextSize is the sealed payload's length, for accounting. It is the
	// ciphertext alone, not the encoded envelope.
	CiphertextSize int64
	CreatedAt      time.Time
}

const analysisRunColumns = `
	r.run_id, r.deployment_id, r.origin_instance_id, r.execution_host_id,
	r.continues_run_id, r.sync_state, r.record_count,
	(SELECT count(*) FROM analysis_records WHERE run_id = r.run_id),
	r.created_at, r.updated_at, r.committed_at`

// AnalysisRun reports one run, whatever its sync state. A pending run is
// readable on purpose: SPEC.md 9 makes staged output visible rather than
// silent, and an operator asking why a run is not reviewable needs to see the
// row that says so.
func AnalysisRun(ctx context.Context, db *sql.DB, runID string) (AnalysisRunRow, error) {
	row := db.QueryRowContext(ctx,
		`SELECT`+analysisRunColumns+` FROM analysis_runs r WHERE r.run_id = $1`, runID)
	out, err := scanAnalysisRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AnalysisRunRow{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	if err != nil {
		return AnalysisRunRow{}, fmt.Errorf("read analysis run %s: %w", runID, err)
	}
	return out, nil
}

// CommittedAnalysisRuns lists a deployment's globally committed runs, newest
// commit first.
//
// Pending runs are excluded because this is the browse surface for what is
// reviewable, and SPEC.md 9 states pending-sync output is not globally
// reviewable and is not eligible as a committed chaos atom. AnalysisRun still
// reports a pending run by id.
func CommittedAnalysisRuns(ctx context.Context, db *sql.DB, deploymentID string) ([]AnalysisRunRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT`+analysisRunColumns+`
		   FROM analysis_runs r
		  WHERE r.deployment_id = $1 AND r.sync_state = $2
		  ORDER BY r.committed_at DESC, r.run_id`, deploymentID, SyncCommitted)
	if err != nil {
		return nil, fmt.Errorf("list committed analysis runs: %w", err)
	}
	defer rows.Close()

	var out []AnalysisRunRow
	for rows.Next() {
		r, err := scanAnalysisRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan analysis run: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list committed analysis runs: %w", err)
	}
	return out, nil
}

// AnalysisRecords lists a run's records in closure order.
func AnalysisRecords(ctx context.Context, db *sql.DB, runID string) ([]AnalysisRecordRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT record_id, run_id, kind, record_schema, ordinal,
		       object_key, object_digest, key_id, ciphertext_size, created_at
		  FROM analysis_records WHERE run_id = $1 ORDER BY ordinal`, runID)
	if err != nil {
		return nil, fmt.Errorf("list analysis records for run %s: %w", runID, err)
	}
	defer rows.Close()

	var out []AnalysisRecordRow
	for rows.Next() {
		var r AnalysisRecordRow
		var kind, keyID string
		if err := rows.Scan(&r.RecordID, &r.RunID, &kind, &r.Schema, &r.Ordinal,
			&r.ObjectKey, &r.ObjectDigest, &keyID, &r.CiphertextSize, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan analysis record: %w", err)
		}
		r.Kind, r.KeyID = RecordKind(kind), envelope.KeyID(keyID)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list analysis records for run %s: %w", runID, err)
	}
	return out, nil
}

// scanner is what QueryRow and Rows have in common, so one scan serves both.
type scanner interface{ Scan(dest ...any) error }

func scanAnalysisRun(s scanner) (AnalysisRunRow, error) {
	var (
		r               AnalysisRunRow
		host, continues sql.NullString
		committed       sql.NullTime
	)
	if err := s.Scan(&r.RunID, &r.DeploymentID, &r.OriginInstanceID, &host,
		&continues, &r.SyncState, &r.RecordCount, &r.RecordsPresent,
		&r.CreatedAt, &r.UpdatedAt, &committed); err != nil {
		return AnalysisRunRow{}, err
	}
	r.ExecutionHostID, r.ContinuesRunID = host.String, continues.String
	if committed.Valid {
		t := committed.Time
		r.CommittedAt = &t
	}
	return r, nil
}

// nullable maps an empty optional identifier to SQL NULL, so "not pinned" and
// "not a continuation" are absent rather than an empty string that a foreign
// key would reject.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
