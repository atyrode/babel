package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// storeComponent names this package's migrations inside the durable database's
// shared migration ledger. The file holds several components' tables — the
// hypothesis frontier's beside these — and keying migrations by component lets
// each own its schema without a coordinating global version.
const storeComponent = "run"

// storeVersion is this component's schema version in the durable database.
const storeVersion = 1

// DatabaseName is the durable database file, held in Babel's private local
// state directory.
//
// It is deliberately not the retrieval index's file. The index is a
// rebuildable cache and losing it costs a re-index; a preparation record or a
// receipt is analysis that only exists here until it is remotely committed, so
// the two must not share a lifecycle in which discarding one discards the
// other (SPEC.md §9).
const DatabaseName = "durable.db"

// Errors callers match with errors.Is.
var (
	// ErrNotFound reports that the durable database holds no such record.
	ErrNotFound = errors.New("run: record not found")
	// ErrExists reports an attempt to write a record whose identity is
	// already taken by different content. Receipts are append-only, so this
	// is a caller bug rather than a condition to retry.
	ErrExists = errors.New("run: record already exists")
	// ErrAmendConflict reports an amendment that does not extend the run's
	// revision chain: a missing predecessor, one from another run, or one
	// another revision already supersedes. A run's history is a chain, and a
	// fork in it would leave no answer to "what does this run say now".
	ErrAmendConflict = errors.New("run: amendment does not extend the revision chain")
)

// Store is the durable, pending-sync database of preparation records and run
// receipts.
//
// Every write is an append. There is no update path for record content and no
// delete path at all, and the database enforces that with triggers rather than
// trusting this package's SQL: an immutable record whose immutability depends
// on nobody writing the wrong statement is not immutable. The one column that
// may change after insert is the sync state, which is not content — it records
// whether the record has reached the shared backend, and only ever moves from
// pending to committed.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens the durable database in dir, creating the directory and this
// component's tables if they are absent.
//
// A schema version this build does not know is an error, never a rebuild. The
// retrieval index may be discarded and recreated because every row in it is
// derivable; nothing here is, so meeting an unfamiliar schema means stopping
// rather than deleting analysis to make the shape fit.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("run: create state directory: %w", err)
	}
	path := filepath.Join(dir, DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("run: open durable database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database's path.
func (s *Store) Path() string { return s.path }

// schema is this component's tables, indexes and immutability triggers. Table
// names are prefixed so the file's other components cannot collide with them.
const schema = `
CREATE TABLE IF NOT EXISTS run_preparation(
	id             TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	prepared_at    TEXT NOT NULL,
	source_count   INTEGER NOT NULL,
	sync_state     TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed')),
	payload        BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS run_receipt(
	id             TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	run_id         TEXT NOT NULL,
	preparation_id TEXT NOT NULL REFERENCES run_preparation(id),
	revision       INTEGER NOT NULL,
	supersedes     TEXT REFERENCES run_receipt(id),
	recorded_at    TEXT NOT NULL,
	sync_state     TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed')),
	counts         TEXT NOT NULL,
	payload        BLOB NOT NULL,
	UNIQUE(run_id, revision),
	UNIQUE(supersedes)
);

CREATE INDEX IF NOT EXISTS run_receipt_run_idx ON run_receipt(run_id, revision);

CREATE TRIGGER IF NOT EXISTS run_preparation_immutable
BEFORE UPDATE OF id, schema_version, prepared_at, source_count, payload ON run_preparation
BEGIN SELECT RAISE(ABORT, 'preparation records are immutable'); END;

CREATE TRIGGER IF NOT EXISTS run_preparation_append_only
BEFORE DELETE ON run_preparation
BEGIN SELECT RAISE(ABORT, 'preparation records are append-only'); END;

CREATE TRIGGER IF NOT EXISTS run_receipt_immutable
BEFORE UPDATE OF id, schema_version, run_id, preparation_id, revision, supersedes,
	recorded_at, counts, payload ON run_receipt
BEGIN SELECT RAISE(ABORT, 'run receipts are immutable'); END;

CREATE TRIGGER IF NOT EXISTS run_receipt_append_only
BEFORE DELETE ON run_receipt
BEGIN SELECT RAISE(ABORT, 'run receipts are append-only'); END;`

// migrate prepares the connection and brings this component's schema up to
// storeVersion. The pragmas follow internal/catalog's: WAL so a reader is not
// blocked by the writer, and a busy timeout to absorb the brief overlap.
// Foreign keys are on because a receipt that references a preparation which is
// not there is exactly the state the corpus-scope requirement exists to
// prevent.
func (s *Store) migrate() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("run: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("run: set durable busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("run: enable durable foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("run: create migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, storeComponent).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(schema); err != nil {
			return fmt.Errorf("run: create durable schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			storeComponent, storeVersion); err != nil {
			return fmt.Errorf("run: record durable schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("run: read durable schema version: %w", err)
	case version != storeVersion:
		return fmt.Errorf("run: durable schema version %d is not supported by this build", version)
	}
	return nil
}

// PutPreparation stores a preparation record.
//
// Storing the same record twice is not an error. A preparation's identity is
// derived from its content, so a repeated write is the same record by
// definition, and preparation is described as an idempotent, resumable step
// (SPEC.md §8). The stored bytes are compared rather than assumed: an ID that
// already names different content is a corrupt database, not a duplicate.
func (s *Store) PutPreparation(ctx context.Context, p Preparation) error {
	return putPreparation(ctx, s.db, p)
}

// execer is the subset of *sql.DB and *sql.Tx one write needs, so a
// preparation is stored the same way whether it stands alone or shares a
// receipt's transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func putPreparation(ctx context.Context, q execer, p Preparation) error {
	payload, err := p.MarshalCanonical()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO run_preparation(
		id, schema_version, prepared_at, source_count, sync_state, payload)
		VALUES(?, ?, ?, ?, ?, ?)`,
		string(p.ID), p.Schema, formatTime(p.PreparedAt), len(p.Selection), SyncPending, payload)
	if err != nil {
		return fmt.Errorf("run: store preparation: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return nil
	}
	var stored []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM run_preparation WHERE id = ?`,
		string(p.ID)).Scan(&stored); err != nil {
		return fmt.Errorf("run: read stored preparation: %w", err)
	}
	if string(stored) != string(payload) {
		return fmt.Errorf("run: stored preparation differs from the one being written: %w", ErrExists)
	}
	return nil
}

// Preparation reads a preparation record and re-verifies that its content
// still derives its ID, so a row altered outside Babel is reported as corrupt
// rather than served as a scope.
func (s *Store) Preparation(ctx context.Context, id PreparationID) (Preparation, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM run_preparation WHERE id = ?`,
		string(id)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Preparation{}, ErrNotFound
	}
	if err != nil {
		return Preparation{}, fmt.Errorf("run: read preparation: %w", err)
	}
	return UnmarshalPreparation(payload)
}

// PutReceipt appends a receipt.
//
// It writes the receipt's preparation first, in the same transaction, because
// a receipt whose corpus scope is missing records nothing worth having: §6.5's
// whole point is that a run's scope is explicit. Storing the preparation
// separately rather than inside the receipt payload means several runs over
// one scope share one copy that cannot disagree with itself.
//
// An amendment must extend the run's chain: its predecessor must exist, belong
// to the same run, be the immediately prior revision, and not already have a
// successor. The last is enforced by the database as well, so a concurrent
// second amendment loses rather than forking the history.
//
// A newly appended receipt is stored pending-sync whatever its header says: it
// has by definition not reached the shared backend yet, and
// MarkReceiptCommitted is the only thing that may say otherwise.
func (s *Store) PutReceipt(ctx context.Context, r Receipt) error {
	if err := r.Preparation.Verify(); err != nil {
		return err
	}
	if r.Header.PreparationID != r.Preparation.ID {
		return fmt.Errorf("run: receipt names a different preparation than it carries")
	}
	payload, err := r.MarshalBody()
	if err != nil {
		return err
	}
	counts, err := json.Marshal(r.Header.Counts)
	if err != nil {
		return fmt.Errorf("run: encode receipt counts: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("run: begin receipt write: %w", err)
	}
	defer tx.Rollback()

	if err := putPreparation(ctx, tx, r.Preparation); err != nil {
		return err
	}

	if err := checkChain(ctx, tx, r.Header); err != nil {
		return err
	}
	var supersedes any
	if r.Header.Supersedes != "" {
		supersedes = string(r.Header.Supersedes)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_receipt(
		id, schema_version, run_id, preparation_id, revision, supersedes,
		recorded_at, sync_state, counts, payload)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(r.Header.ID), r.Header.Schema, r.Header.RunID, string(r.Header.PreparationID),
		r.Header.Revision, supersedes, formatTime(r.Header.RecordedAt), SyncPending,
		counts, payload); err != nil {
		return fmt.Errorf("run: store receipt: %w", wrapConstraint(err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("run: commit receipt: %w", err)
	}
	return nil
}

// checkChain rejects a write that would duplicate, fork or skip a run's
// history before the insert reports the same thing as an opaque constraint
// failure. SQLite reports whichever unique index it happens to check first,
// which is not a useful way to tell a caller what it did wrong.
func checkChain(ctx context.Context, tx *sql.Tx, h Header) error {
	var exists int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM run_receipt WHERE id = ?`,
		string(h.ID)).Scan(&exists); {
	case err == nil:
		return fmt.Errorf("run: this receipt is already recorded: %w", ErrExists)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("run: check for an existing receipt: %w", err)
	}
	if h.Revision == 1 {
		if h.Supersedes != "" {
			return fmt.Errorf("run: first revision cannot supersede another: %w", ErrAmendConflict)
		}
		return nil
	}
	if h.Supersedes == "" {
		return fmt.Errorf("run: amendment names no prior revision: %w", ErrAmendConflict)
	}
	var runID string
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT run_id, revision FROM run_receipt WHERE id = ?`,
		string(h.Supersedes)).Scan(&runID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("run: prior revision is not stored: %w", ErrAmendConflict)
	}
	if err != nil {
		return fmt.Errorf("run: read prior revision: %w", err)
	}
	if runID != h.RunID || revision != h.Revision-1 {
		return fmt.Errorf("run: prior revision belongs to another run or position: %w", ErrAmendConflict)
	}
	return nil
}

// wrapConstraint maps SQLite's uniqueness failures onto this package's
// sentinels. The database is the authority on which write actually lost a
// race, so the mapping happens here rather than being guessed in advance.
func wrapConstraint(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "run_receipt.id"):
		return fmt.Errorf("%w: %v", ErrExists, err)
	case strings.Contains(msg, "run_receipt.supersedes"), strings.Contains(msg, "run_receipt.run_id"):
		return fmt.Errorf("%w: %v", ErrAmendConflict, err)
	}
	return err
}

// Receipt reads one receipt with the preparation it ran over.
func (s *Store) Receipt(ctx context.Context, id ReceiptID) (Receipt, error) {
	row := s.db.QueryRowContext(ctx, receiptColumns+` WHERE id = ?`, string(id))
	r, err := decodeReceipt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	prep, err := s.Preparation(ctx, r.Header.PreparationID)
	if err != nil {
		return Receipt{}, err
	}
	r.Preparation = prep
	return r, nil
}

// Revisions reads a run's whole receipt history in revision order, which is
// the form review needs: an amendment never replaces what it corrects, so the
// prior revisions are part of the answer rather than superseded noise.
//
// The rows are decoded to completion before any preparation is read. The
// durable database allows one connection, so a query issued while a result set
// is still open would wait for a connection that result set is holding.
func (s *Store) Revisions(ctx context.Context, runID string) ([]Receipt, error) {
	rows, err := s.db.QueryContext(ctx, receiptColumns+` WHERE run_id = ? ORDER BY revision`, runID)
	if err != nil {
		return nil, fmt.Errorf("run: read revisions: %w", err)
	}
	var out []Receipt
	for rows.Next() {
		r, err := decodeReceipt(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("run: read revisions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("run: read revisions: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	// Every revision of a run shares its scope, so the preparation is read
	// once rather than once per revision.
	prep, err := s.Preparation(ctx, out[0].Header.PreparationID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Header.PreparationID != prep.ID {
			return nil, fmt.Errorf("run: a revision of this run names a different scope")
		}
		out[i].Preparation = prep
	}
	return out, nil
}

const receiptColumns = `SELECT id, schema_version, run_id, preparation_id, revision,
	supersedes, recorded_at, sync_state, counts, payload FROM run_receipt`

// scanner is satisfied by both *sql.Row and *sql.Rows, so one receipt decoder
// serves the single-row and multi-row reads.
type scanner interface{ Scan(dest ...any) error }

// decodeReceipt reads one row into a receipt without its preparation, which
// the caller attaches once the result set is closed.
func decodeReceipt(row scanner) (Receipt, error) {
	var (
		h          Header
		id         string
		prepID     string
		supersedes sql.NullString
		recordedAt string
		counts     []byte
		payload    []byte
	)
	if err := row.Scan(&id, &h.Schema, &h.RunID, &prepID, &h.Revision, &supersedes,
		&recordedAt, &h.Sync, &counts, &payload); err != nil {
		return Receipt{}, err
	}
	h.ID = ReceiptID(id)
	h.PreparationID = PreparationID(prepID)
	h.Supersedes = ReceiptID(supersedes.String)
	parsed, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return Receipt{}, fmt.Errorf("run: stored receipt has an unreadable time: %w", err)
	}
	h.RecordedAt = parsed.UTC()
	if err := json.Unmarshal(counts, &h.Counts); err != nil {
		return Receipt{}, fmt.Errorf("run: decode receipt counts: %w", err)
	}
	if h.Schema != ReceiptSchema {
		return Receipt{}, fmt.Errorf("run: stored receipt schema %d is not supported by this build", h.Schema)
	}
	body, err := unmarshalBody(payload)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Header: h, Body: body}, nil
}

// MarkReceiptCommitted records that a receipt has reached the shared backend.
//
// This is the only permitted mutation of a stored receipt, and it is not a
// mutation of the record: the payload, the counts and the chain are all
// protected by triggers, and what changes is Babel's knowledge of where the
// record now exists. Remote synchronization itself is not implemented here;
// this is the local half of it, so a receipt is visibly pending until
// something says otherwise (SPEC.md §6.5).
func (s *Store) MarkReceiptCommitted(ctx context.Context, id ReceiptID) error {
	return s.markCommitted(ctx, `UPDATE run_receipt SET sync_state = ? WHERE id = ?`, string(id))
}

// MarkPreparationCommitted records that a preparation record has reached the
// shared backend, on the same terms as MarkReceiptCommitted.
func (s *Store) MarkPreparationCommitted(ctx context.Context, id PreparationID) error {
	return s.markCommitted(ctx, `UPDATE run_preparation SET sync_state = ? WHERE id = ?`, string(id))
}

func (s *Store) markCommitted(ctx context.Context, query, id string) error {
	res, err := s.db.ExecContext(ctx, query, SyncCommitted, id)
	if err != nil {
		return fmt.Errorf("run: record commit state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("run: record commit state: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncState reports whether a receipt is still pending remote commitment.
func (s *Store) SyncState(ctx context.Context, id ReceiptID) (string, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT sync_state FROM run_receipt WHERE id = ?`,
		string(id)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("run: read commit state: %w", err)
	}
	return state, nil
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction. The
// fixed width matters even where this package orders by revision rather than by
// time: time.RFC3339Nano trims trailing zeros, so "12:00:00.1Z" sorts after
// "12:00:00.12Z" as text while being the earlier instant, and a future ORDER BY
// over a timestamp column would inherit that defect silently. Writing a fixed
// fraction makes text order and time order the same relation everywhere, and
// time.RFC3339Nano still parses it.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime renders a stored timestamp: UTC, RFC 3339 with a fixed nine-digit
// fraction, so a stored time reads the same way everywhere in Babel, sorts as
// text in chronological order, and parses back to the exact instant it was
// written from.
func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

// ListLimits bound an enumeration. A zero limit means the default rather than
// everything, because a deployment accumulates one receipt per run per host and
// an unbounded list would eventually be the whole history.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

// Receipts enumerates receipts, newest first, with a total so a caller can page
// without discovering the end by hitting it. It exists because a receipt that
// cannot be listed cannot be reviewed: §7 makes the receipt the record of what
// a run did, and a record reachable only by an identifier the operator would
// have to already know is not a record they can consult.
//
// Only the newest revision of each run is returned. An amended receipt
// supersedes its predecessor (§7 keeps both), so listing every revision would
// show one run several times with no indication which is current.
func (s *Store) Receipts(ctx context.Context, limit, offset int) ([]Receipt, int, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	if offset < 0 {
		offset = 0
	}

	const newest = `SELECT id FROM run_receipt r
		WHERE r.revision = (SELECT max(revision) FROM run_receipt x WHERE x.run_id = r.run_id)`

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM (`+newest+`)`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count receipts: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, newest+
		` ORDER BY r.recorded_at DESC, r.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list receipts: %w", err)
	}
	var ids []ReceiptID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("list receipts: %w", err)
		}
		ids = append(ids, ReceiptID(id))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("list receipts: %w", err)
	}
	rows.Close()

	// Read each through Receipt so a listed receipt is the same record a
	// detail view shows, verification and all. A leaner projection would let a
	// list and a detail view disagree about the same run.
	out := make([]Receipt, 0, len(ids))
	for _, id := range ids {
		receipt, err := s.Receipt(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, receipt)
	}
	return out, total, nil
}
