package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the durable database's shared
// migration ledger, beside internal/frontier's, internal/run's,
// internal/explore's, internal/disposition's, internal/review's and
// internal/reality's. Keying migrations by component is what lets a seventh
// component join the file without a coordinating global version.
const component = "sync"

// journalVersion is this component's schema version. A version this build does
// not know is an error rather than a rebuild, for the same reason every other
// component of this file refuses one: a staged payload that has not reached the
// shared catalog exists nowhere else, so discarding the file to make the shape
// fit would discard analysis.
//
// Version 2 added sync_record_edge for the typed reference graph's plaintext
// endpoints (issue #113). The bump is what makes the addition safe in the one
// direction that matters: a newer file opened by an older binary keeps its
// version, and this build refuses a version it does not know rather than
// publishing a record whose plaintext half it cannot see.
//
// Version 3 added sync_record_subject for what a proposal rests on or addresses
// (issue #114). The bump does the same work in the same direction, and the
// consequence of skipping it would be sharper: an older binary that published a
// version-3 file's proposals would carry the records and drop the rows saying
// which of them rest on findings, so a candidate proposal - an unbacked want -
// and a consolidated one would reach the fleet indistinguishable. Refusing a
// version it does not know is what makes the addition safe rather than a
// silently narrower publication.
const journalVersion = 3

// DatabaseName is the durable database this journal shares with every other
// Phase B writer. It is named here rather than imported from one of them
// because a package that opened "whatever internal/run calls it" would silently
// follow that package to a different file.
const DatabaseName = "durable.db"

// timestampLayout is how the journal stores times: RFC 3339 with nanoseconds in
// UTC, which sorts lexicographically in the order it sorts chronologically. The
// journal's own ordering questions - which run was declared first, which record
// was staged first - are answered by ordinals and rowids, so this column is for
// an operator reading the journal rather than for the protocol.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

var (
	// ErrClosureConflict reports a run whose closure has already been declared
	// at a different size. A run's declared closure is what makes "a partial
	// commit is not a commit" checkable, and 0003's trigger holds the count
	// immutable remotely; a second local declaration at another size would
	// mean the first published a closure the second disagrees with, which no
	// retry can resolve.
	ErrClosureConflict = errors.New("sync: run closure already declared at a different size")

	// ErrRecordConflict reports an entity id already staged under another run.
	// Entity ids are global, so this is two different records given one id
	// rather than a repeat of one record, and publishing it would attach a
	// record to a lineage it does not belong to.
	ErrRecordConflict = errors.New("sync: record already staged under a different run")

	// ErrRunNotStaged reports a run the journal holds nothing for. Declaring a
	// closure of zero records is refused rather than recorded: 0003 requires
	// record_count > 0, and a run with no output is not a run to publish.
	ErrRunNotStaged = errors.New("sync: run has no staged records")
)

// journalSchema is this component's tables and triggers. Table names are
// prefixed so the file's other components cannot collide with them.
//
// The shape mirrors migration 0003 deliberately: a run row carrying the
// declared closure and the sync state, and a record row per member. That is not
// duplication for its own sake - it is what lets a retry answer "is this run's
// closure complete" and "which records still need publishing" from the local
// file alone, while PostgreSQL is unreachable, which is the exact condition the
// journal exists for.
//
// The payload lives in its own table, and that is the one structural difference
// from 0003. A staged payload is a second copy of bytes the writing component's
// own row already holds, kept only until the record is globally committed;
// keeping it in the record row would mean either carrying every payload Babel
// has ever published forever or making an immutable row's content writable so
// it could be cleared. A separate table is released by deleting a row, which
// needs no exception to immutability: sync_payload holds a row exactly while
// that record's publication is pending.
//
// sync_record_edge is the same arrangement for the same reason. A reference
// edge publishes its relation kind and both endpoints as plaintext columns
// (migrations/0008), and those columns have to survive the crash window
// between the transaction that made the edge durable and the sync that
// publishes it - so they are staged here rather than re-derived, which nothing
// could do from a sealed payload. They are a copy of internal/reference's own
// row, so they are released with the payload when the record commits: the edge
// store holds the plaintext and the shared catalog holds the published columns,
// and a third copy that lived forever would grow this file by every edge Babel
// has ever published.
//
// sync_record_subject is the same arrangement once more, for a proposal's
// provenance (migrations/0010, issue #114). It differs from sync_record_edge in
// one respect that shows in the schema: a proposal has many subjects, so the key
// is the record and a position rather than the record alone, and the position is
// what preserves the order the producer asserted them in. The reason for staging
// is the same and the cost of not staging is higher - nothing can re-derive
// these ids from a sealed payload, and a proposal published without them is a
// want a fleet host would render with the authority of a verified conclusion.
const journalSchema = `
CREATE TABLE IF NOT EXISTS sync_run(
	run_id            TEXT PRIMARY KEY,
	execution_host_id TEXT,
	continues_run_id  TEXT,
	record_count      INTEGER NOT NULL CHECK (record_count > 0),
	declared_at       TEXT NOT NULL,
	sync_state        TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed'))
);

CREATE INDEX IF NOT EXISTS sync_run_pending_idx ON sync_run(declared_at)
	WHERE sync_state = 'pending-sync';

CREATE TABLE IF NOT EXISTS sync_record(
	record_id     TEXT PRIMARY KEY,
	run_id        TEXT NOT NULL,
	kind          TEXT NOT NULL,
	record_schema INTEGER NOT NULL CHECK (record_schema > 0),
	ordinal       INTEGER NOT NULL CHECK (ordinal >= 0),
	staged_at     TEXT NOT NULL,
	sync_state    TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed')),
	UNIQUE(run_id, ordinal)
);

CREATE INDEX IF NOT EXISTS sync_record_run_idx ON sync_record(run_id, ordinal);

CREATE TABLE IF NOT EXISTS sync_payload(
	record_id TEXT PRIMARY KEY REFERENCES sync_record(record_id),
	payload   BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_record_edge(
	record_id TEXT PRIMARY KEY REFERENCES sync_record(record_id),
	edge_kind TEXT NOT NULL,
	from_kind TEXT NOT NULL,
	from_id   TEXT NOT NULL,
	to_kind   TEXT NOT NULL,
	to_id     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_record_subject(
	record_id    TEXT REFERENCES sync_record(record_id),
	position     INTEGER NOT NULL CHECK (position >= 0),
	subject_kind TEXT NOT NULL,
	subject_id   TEXT NOT NULL,
	PRIMARY KEY(record_id, position)
);

CREATE TRIGGER IF NOT EXISTS sync_record_immutable
BEFORE UPDATE OF record_id, run_id, kind, record_schema, ordinal, staged_at ON sync_record
BEGIN SELECT RAISE(ABORT, 'a staged record is fixed at stage time; only its sync state moves'); END;

CREATE TRIGGER IF NOT EXISTS sync_record_append_only
BEFORE DELETE ON sync_record
BEGIN SELECT RAISE(ABORT, 'the sync journal is append-only: a record that reached the shared catalog is never unrecorded'); END;

CREATE TRIGGER IF NOT EXISTS sync_run_forward_only
BEFORE UPDATE OF run_id, execution_host_id, continues_run_id, record_count, declared_at ON sync_run
BEGIN SELECT RAISE(ABORT, 'a run identity and its declared closure are fixed at declaration'); END;

CREATE TRIGGER IF NOT EXISTS sync_run_append_only
BEFORE DELETE ON sync_run
BEGIN SELECT RAISE(ABORT, 'the sync journal is append-only'); END;

CREATE TRIGGER IF NOT EXISTS sync_payload_immutable
BEFORE UPDATE OF payload ON sync_payload
BEGIN SELECT RAISE(ABORT, 'a staged payload is the bytes that were sealed; it is released, never rewritten'); END;

CREATE TRIGGER IF NOT EXISTS sync_record_edge_immutable
BEFORE UPDATE ON sync_record_edge
BEGIN SELECT RAISE(ABORT, 'a staged edge names the endpoints that were validated at write time; it is released, never rewritten'); END;

CREATE TRIGGER IF NOT EXISTS sync_record_subject_immutable
BEFORE UPDATE ON sync_record_subject
BEGIN SELECT RAISE(ABORT, 'a staged proposal subject was validated at write time; it is released, never rewritten'); END;`

// Journal is the local record of what has been staged for the shared catalog
// and what has reached it.
//
// It answers two questions no remote store can answer during an outage: which
// durable records this machine owes the fleet, and which of them are already
// safe. SPEC.md §9 requires staged Phase B output to be visibly pending-sync
// rather than quietly assumed durable, and this is where that visibility is
// held - a row per record, from the transaction that made the record durable
// until the transaction that published it committed.
//
// It lives in the durable database rather than a file of its own for the reason
// every other component there does: one durable file is what an operator has to
// preserve, and a journal that could be lost independently of the records it
// tracks would leave those records unpublishable and nothing saying so.
type Journal struct {
	db   *sql.DB
	path string

	// now supplies staging and declaration timestamps. It is a field so a test
	// can make ordering deterministic without sleeping; production never
	// replaces it.
	now func() time.Time
}

// OpenJournal opens the durable database in dir and creates this component's
// tables if they are absent.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sync: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sync: open durable database: %w", err)
	}
	// One connection: the pragmas below are per-connection, and the durable
	// file has a single writer per §9's local state-writer lock invariant.
	db.SetMaxOpenConns(1)
	j := &Journal{db: db, path: path, now: func() time.Time { return time.Now().UTC() }}
	if err := j.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

// Close releases the database handle.
func (j *Journal) Close() error { return j.db.Close() }

// Path reports the durable database's path, which is the same file the
// frontier, the receipts and the Reality Ledger use.
func (j *Journal) Path() string { return j.path }

// migrate prepares the connection and brings this component's schema up to
// journalVersion. The pragmas are the durable file's, because the components
// share it and a connection that disagreed about journalling or foreign keys
// would be a property of whichever store happened to open it first.
func (j *Journal) migrate() error {
	var journalMode string
	if err := j.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil {
		return fmt.Errorf("sync: enable durable WAL: %w", err)
	}
	if _, err := j.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("sync: set durable busy timeout: %w", err)
	}
	if _, err := j.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("sync: enable durable foreign keys: %w", err)
	}
	if _, err := j.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("sync: create migration ledger: %w", err)
	}

	var version int
	err := j.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := j.db.Exec(journalSchema); err != nil {
			return fmt.Errorf("sync: create journal schema: %w", err)
		}
		if _, err := j.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			component, journalVersion); err != nil {
			return fmt.Errorf("sync: record journal schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("sync: read journal schema version: %w", err)
	case version > journalVersion:
		return fmt.Errorf("sync: journal schema version %d is not supported by this build", version)
	case version < journalVersion:
		// Every statement in journalSchema is IF NOT EXISTS, so bringing an
		// older file forward is running the same schema again: what it already
		// has is left exactly as it is and what later versions added arrives.
		// That is only true because every addition so far is a new table or a
		// new trigger - an upgrade that had to alter an existing table would
		// need its own statement here and a version of its own, and could not
		// be folded into this one.
		if err := upgradeSchema(j.db, version); err != nil {
			return err
		}
	}
	return nil
}

// EnsureSchema creates this component's tables on a caller's own handle to the
// durable database.
//
// A durable writer stages inside its own transaction, on its own connection, so
// the tables have to exist there before it opens one. Every writer already
// opens the file at startup and this is cheap and idempotent, which is why the
// staging hook does not have to be handed a Journal to be usable.
func EnsureSchema(db *sql.DB) error {
	if _, err := db.Exec(journalSchema); err != nil {
		return fmt.Errorf("sync: create journal schema: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("sync: create migration ledger: %w", err)
	}
	return recordSchemaVersion(db)
}

// upgradeSchema brings a file written by an older build up to journalVersion.
//
// It runs the same journalSchema an empty file gets, which is what makes the
// upgrade one statement rather than a list: every statement there is IF NOT
// EXISTS, so the tables and triggers the file already has are untouched and
// whatever a later version added is created beside them. That is what lets one
// path serve both hops - version 1 to 3 and version 2 to 3 - rather than a
// chain. from is named in the error rather than used to choose statements,
// because no upgrade so far has to know where it started; the first one that
// alters an existing table will need its own case, and then this argument
// stops being only diagnostic.
func upgradeSchema(db *sql.DB, from int) error {
	if _, err := db.Exec(journalSchema); err != nil {
		return fmt.Errorf("sync: upgrade journal schema from version %d: %w", from, err)
	}
	return recordSchemaVersion(db)
}

// recordSchemaVersion records journalVersion without ever lowering what the
// file already claims.
//
// The guard is what keeps EnsureSchema safe on a file a newer build has
// migrated. A writer calls it on its own handle with no version check of its
// own - it wants the tables, not a verdict - and an unguarded write would let
// an old binary relabel a version-3 file as version 2, after which every
// build would read a shape the label does not describe. Lowering is refused
// silently rather than reported: the newer version is the correct value, so
// there is nothing for the writer to do about it.
func recordSchemaVersion(db *sql.DB) error {
	if _, err := db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version
		WHERE schema_migration.version < excluded.version`,
		component, journalVersion); err != nil {
		return fmt.Errorf("sync: record journal schema version: %w", err)
	}
	return nil
}

// stagedRun is one declared closure the journal holds.
type stagedRun struct {
	runID           string
	executionHostID string
	continuesRunID  string
	recordCount     int
	state           string
}

// stage records rec inside tx, which is the transaction that is making the
// record itself durable.
//
// Staging shares the writer's transaction rather than following it, and that is
// the whole point of the method taking a *sql.Tx. A record that committed
// locally while its journal row did not would be durable, invisible to the
// publisher, and reported by nothing - the one failure mode "never lose the
// output" cannot tolerate. Sharing the transaction makes "durable locally"
// and "known to be owed to the fleet" the same event.
//
// Re-staging an id the journal already holds under the same run is a no-op
// rather than an error: a writer whose transaction was retried re-derives the
// same pair, and refusing it would make that crash window unrecoverable. The
// payload is not compared, because a durable record is immutable - the bytes
// cannot legitimately differ, and reading a blob back to prove it would cost a
// read on every write to catch nothing.
func (j *Journal) stage(ctx context.Context, tx *sql.Tx, rec Record) error {
	if err := rec.validate(); err != nil {
		return err
	}

	var existingRun string
	err := tx.QueryRowContext(ctx,
		`SELECT run_id FROM sync_record WHERE record_id = ?`, rec.EntityID).Scan(&existingRun)
	switch {
	case err == nil:
		if existingRun != rec.RunID {
			return fmt.Errorf("sync: stage %s: %w: staged under %s", rec.EntityID, ErrRecordConflict, existingRun)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("sync: read staged record %s: %w", rec.EntityID, err)
	}

	// A declared closure is closed. Admitting a record into a run whose
	// record_count is already fixed would produce a run that can never
	// commit: 0003 flips to committed only when the catalog holds exactly the
	// declared count, and the count is immutable there, so the extra record
	// would leave the whole run permanently pending with no remedy but a
	// database edit. Refusing the write instead makes it a caller bug that a
	// writer's own test catches, which is the only place it is fixable.
	var declared int
	switch err := tx.QueryRowContext(ctx,
		`SELECT record_count FROM sync_run WHERE run_id = ?`, rec.RunID).Scan(&declared); {
	case err == nil:
		return fmt.Errorf("sync: stage %s into run %s: %w: the run declared %d records and is closed",
			rec.EntityID, rec.RunID, ErrClosureConflict, declared)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("sync: read declared closure for run %s: %w", rec.RunID, err)
	}

	// The ordinal is assigned here rather than by the caller so that closure
	// order is stage order by construction. A caller computing its own would
	// have to know what else the run has staged, which is exactly the shared
	// state this table already holds.
	var ordinal int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(ordinal) + 1, 0) FROM sync_record WHERE run_id = ?`,
		rec.RunID).Scan(&ordinal); err != nil {
		return fmt.Errorf("sync: next ordinal for run %s: %w", rec.RunID, err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_record(
		record_id, run_id, kind, record_schema, ordinal, staged_at, sync_state)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		rec.EntityID, rec.RunID, string(rec.Kind), rec.Schema, ordinal,
		j.now().Format(timestampLayout), sharedcatalog.SyncPending); err != nil {
		return fmt.Errorf("sync: stage record %s: %w", rec.EntityID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sync_payload(record_id, payload) VALUES(?, ?)`,
		rec.EntityID, rec.Payload); err != nil {
		return fmt.Errorf("sync: stage payload for %s: %w", rec.EntityID, err)
	}
	if rec.Edge != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_record_edge(
			record_id, edge_kind, from_kind, from_id, to_kind, to_id)
			VALUES(?, ?, ?, ?, ?, ?)`,
			rec.EntityID, string(rec.Edge.Kind),
			rec.Edge.FromKind, rec.Edge.FromID,
			rec.Edge.ToKind, rec.Edge.ToID); err != nil {
			return fmt.Errorf("sync: stage edge endpoints for %s: %w", rec.EntityID, err)
		}
	}
	// The slice index is the position, so the order the producer asserted its
	// subjects in is the order the publisher offers them in and the order the
	// shared catalog stores them in. One INSERT per subject rather than one
	// multi-row statement: a proposal has a handful of subjects, and a
	// statement built from a variable number of placeholders is a statement
	// nobody can read at a glance.
	for i, subject := range rec.Subjects {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_record_subject(
			record_id, position, subject_kind, subject_id)
			VALUES(?, ?, ?, ?)`,
			rec.EntityID, i, string(subject.Kind), subject.ID); err != nil {
			return fmt.Errorf("sync: stage proposal subject %d for %s: %w", i, rec.EntityID, err)
		}
	}
	return nil
}

// declareTx closes a run's closure at the records staged for it inside tx, and
// returns the run as the journal then holds it.
//
// The count is read from the journal rather than supplied, because the journal
// is the only thing that knows it: a writer staging its records one at a time
// does not learn the total until the last one is written, and a caller that had
// to predict it would either declare a closure it cannot fill or fill one it
// did not declare. Reading it inside the writer's own transaction is what makes
// the count include the record that transaction is still writing.
//
// Declaring is idempotent, and a second declaration at a different size is
// ErrClosureConflict rather than an update: the size is what 0003's flip to
// committed is conditional on, and it is immutable there.
func (j *Journal) declareTx(ctx context.Context, tx *sql.Tx, c Closure) (stagedRun, error) {
	if !validEntityID.MatchString(c.RunID) {
		return stagedRun{}, fmt.Errorf("sync: run id %q is not a well-formed Phase B identifier", c.RunID)
	}
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sync_record WHERE run_id = ?`, c.RunID).Scan(&count); err != nil {
		return stagedRun{}, fmt.Errorf("sync: count staged records for run %s: %w", c.RunID, err)
	}
	if count == 0 {
		return stagedRun{}, fmt.Errorf("sync: declare run %s: %w", c.RunID, ErrRunNotStaged)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_run(
		run_id, execution_host_id, continues_run_id, record_count, declared_at, sync_state)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO NOTHING`,
		c.RunID, nullable(c.ExecutionHostID), nullable(c.ContinuesRunID), count,
		j.now().Format(timestampLayout), sharedcatalog.SyncPending); err != nil {
		return stagedRun{}, fmt.Errorf("sync: declare run %s: %w", c.RunID, err)
	}

	run, err := scanRun(tx.QueryRowContext(ctx, runColumns+` WHERE run_id = ?`, c.RunID))
	if err != nil {
		return stagedRun{}, err
	}
	if run.recordCount != count {
		return stagedRun{}, fmt.Errorf("sync: declare run %s: %w: declared %d, staged %d",
			c.RunID, ErrClosureConflict, run.recordCount, count)
	}
	return run, nil
}

// declare closes a closure on the journal's own connection, for a caller that
// is no longer inside the transaction that produced the records.
//
// CommitInline uses it so that a run whose declaration was lost - a crash
// between the writer's commit and its DeclareTx, or a writer that staged
// without declaring - can still be completed by the next attempt rather than
// staying pending with no path forward. It is idempotent on an
// already-declared run, which is the normal case: DeclareTx has usually
// already written the row and this only reads it back.
func (j *Journal) declare(ctx context.Context, c Closure) (stagedRun, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return stagedRun{}, fmt.Errorf("sync: begin declaration: %w", err)
	}
	defer tx.Rollback()
	run, err := j.declareTx(ctx, tx, c)
	if err != nil {
		return stagedRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return stagedRun{}, fmt.Errorf("sync: commit declaration: %w", err)
	}
	return run, nil
}

// closureOpen reports whether a run may still take records: it has not
// declared its closure.
//
// Absence of a declaration is the whole test, and it is deliberately not "the
// journal has seen this run". A run's first record arrives before anything
// about the run is recorded, so requiring prior evidence would make every
// run's first record its own closure and the run itself empty. The cost of the
// simpler rule is that a record naming a run that never declares stays staged
// and undeclared - visible in `babel sync`'s report, publishable as soon as
// that run ends, and never silently dropped.
func (j *Journal) closureOpen(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	var declared int
	err := tx.QueryRowContext(ctx,
		`SELECT record_count FROM sync_run WHERE run_id = ?`, runID).Scan(&declared)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("sync: read declared closure for run %s: %w", runID, err)
	}
	return false, nil
}

const runColumns = `SELECT run_id, COALESCE(execution_host_id, ''), COALESCE(continues_run_id, ''),
	record_count, sync_state FROM sync_run`

func scanRun(s interface{ Scan(...any) error }) (stagedRun, error) {
	var run stagedRun
	if err := s.Scan(&run.runID, &run.executionHostID, &run.continuesRunID,
		&run.recordCount, &run.state); err != nil {
		return stagedRun{}, fmt.Errorf("sync: read declared run: %w", err)
	}
	return run, nil
}

// pendingRuns lists every declared run the journal still holds as pending, in
// declaration order.
//
// Declaration order is the order a retry should attempt them in: an earlier run
// is the one that has been owed to the fleet longer, and a run that continues
// another was declared after it.
func (j *Journal) pendingRuns(ctx context.Context) ([]stagedRun, error) {
	rows, err := j.db.QueryContext(ctx,
		runColumns+` WHERE sync_state = ? ORDER BY declared_at, run_id`, sharedcatalog.SyncPending)
	if err != nil {
		return nil, fmt.Errorf("sync: list pending runs: %w", err)
	}
	defer rows.Close()
	var out []stagedRun
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync: list pending runs: %w", err)
	}
	return out, nil
}

// run reports one declared run, or sql.ErrNoRows wrapped if the closure has not
// been declared.
func (j *Journal) run(ctx context.Context, runID string) (stagedRun, error) {
	return scanRun(j.db.QueryRowContext(ctx, runColumns+` WHERE run_id = ?`, runID))
}

// pendingRecords reads the still-unpublished members of one run's closure, in
// closure order, with the payloads and the plaintext columns that were staged
// for them.
//
// Only the pending ones are read. A record the catalog already holds needs
// neither its payload re-sealed nor its bytes carried, and its payload row has
// already been released, so offering it again would be offering nothing.
func (j *Journal) pendingRecords(ctx context.Context, runID string) ([]sharedcatalog.StagedRecord, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT r.record_id, r.kind, r.record_schema, r.ordinal, p.payload,
		       e.edge_kind, e.from_kind, e.from_id, e.to_kind, e.to_id
		  FROM sync_record r
		  JOIN sync_payload p ON p.record_id = r.record_id
		  LEFT JOIN sync_record_edge e ON e.record_id = r.record_id
		 WHERE r.run_id = ? AND r.sync_state = ?
		 ORDER BY r.ordinal`, runID, sharedcatalog.SyncPending)
	if err != nil {
		return nil, fmt.Errorf("sync: read staged records for run %s: %w", runID, err)
	}
	defer rows.Close()
	var out []sharedcatalog.StagedRecord
	for rows.Next() {
		var rec sharedcatalog.StagedRecord
		var kind string
		// The join is LEFT because almost no record is an edge, so all five
		// endpoint columns are absent for almost every row.
		var edgeKind, fromKind, fromID, toKind, toID sql.NullString
		if err := rows.Scan(&rec.RecordID, &kind, &rec.Schema, &rec.Ordinal, &rec.Payload,
			&edgeKind, &fromKind, &fromID, &toKind, &toID); err != nil {
			return nil, fmt.Errorf("sync: read staged record: %w", err)
		}
		rec.Kind = sharedcatalog.RecordKind(kind)
		if edgeKind.Valid {
			rec.Edge = &sharedcatalog.RecordEdge{
				Kind:     sharedcatalog.EdgeKind(edgeKind.String),
				FromKind: fromKind.String,
				FromID:   fromID.String,
				ToKind:   toKind.String,
				ToID:     toID.String,
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync: read staged records for run %s: %w", runID, err)
	}
	// The subjects are a second query, so it is worth not making it: a run with
	// nothing left to publish has nothing to attach them to.
	if len(out) == 0 {
		return out, nil
	}
	subjects, err := j.pendingSubjects(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Subjects = subjects[out[i].RecordID]
	}
	return out, nil
}

// pendingSubjects reads what each of a run's pending proposals rests on or
// addresses, keyed by record id and in the order the producer asserted it
// (issue #114).
//
// It is a second query rather than a third join in pendingRecords, and that is
// the whole reason it exists as its own method. An edge is one row per record,
// so it joins; a proposal's subjects are many rows per record, so joining them
// would multiply every record row by its subject count and make the payload
// blob arrive once per subject - a correctness problem for the ordinal-ordered
// scan above and a bandwidth problem for a payload that can reach mebibytes.
func (j *Journal) pendingSubjects(ctx context.Context, runID string) (map[string][]sharedcatalog.RecordSubject, error) {
	rows, err := j.db.QueryContext(ctx, `
		SELECT s.record_id, s.subject_kind, s.subject_id
		  FROM sync_record_subject s
		  JOIN sync_record r ON r.record_id = s.record_id
		 WHERE r.run_id = ? AND r.sync_state = ?
		 ORDER BY s.record_id, s.position`, runID, sharedcatalog.SyncPending)
	if err != nil {
		return nil, fmt.Errorf("sync: read staged proposal subjects for run %s: %w", runID, err)
	}
	defer rows.Close()
	var out map[string][]sharedcatalog.RecordSubject
	for rows.Next() {
		var recordID, kind string
		var subject sharedcatalog.RecordSubject
		if err := rows.Scan(&recordID, &kind, &subject.ID); err != nil {
			return nil, fmt.Errorf("sync: read staged proposal subject: %w", err)
		}
		subject.Kind = sharedcatalog.SubjectKind(kind)
		if out == nil {
			// Allocated on the first row rather than up front: almost no run
			// carries a proposal, and a map nothing is put in is a map nothing
			// needed.
			out = make(map[string][]sharedcatalog.RecordSubject)
		}
		out[recordID] = append(out[recordID], subject)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync: read staged proposal subjects for run %s: %w", runID, err)
	}
	return out, nil
}

// commit flips one run and its whole closure to committed and releases the
// staged payloads.
//
// It runs strictly after the PostgreSQL transaction that made the run globally
// visible has committed, and it is the last step of the protocol rather than a
// part of it: the local state is a cache of a remote fact here, so losing this
// flip costs a retry that finds every row already present and flips again,
// while performing it early would claim durability the fleet does not have.
//
// The payloads are deleted in the same transaction as the flip. A committed
// record's payload copy has no remaining reader - the record's own component
// holds the plaintext and Cellar holds the sealed object - and leaving it would
// grow the durable file by every byte Babel has ever published. A staged edge's
// endpoint columns and a staged proposal's subjects are released with it, and
// for the same reason: the shared catalog now holds them, and internal/reference
// and internal/frontier never stopped holding them.
func (j *Journal) commit(ctx context.Context, runID string) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sync: begin journal commit: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_payload WHERE record_id IN (SELECT record_id FROM sync_record WHERE run_id = ?)`,
		runID); err != nil {
		return fmt.Errorf("sync: release staged payloads for run %s: %w", runID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_record_edge WHERE record_id IN (SELECT record_id FROM sync_record WHERE run_id = ?)`,
		runID); err != nil {
		return fmt.Errorf("sync: release staged edge endpoints for run %s: %w", runID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_record_subject WHERE record_id IN (SELECT record_id FROM sync_record WHERE run_id = ?)`,
		runID); err != nil {
		return fmt.Errorf("sync: release staged proposal subjects for run %s: %w", runID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sync_record SET sync_state = ? WHERE run_id = ? AND sync_state <> ?`,
		sharedcatalog.SyncCommitted, runID, sharedcatalog.SyncCommitted); err != nil {
		return fmt.Errorf("sync: record commit state for run %s: %w", runID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sync_run SET sync_state = ? WHERE run_id = ? AND sync_state <> ?`,
		sharedcatalog.SyncCommitted, runID, sharedcatalog.SyncCommitted); err != nil {
		return fmt.Errorf("sync: record commit state for run %s: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sync: commit journal state for run %s: %w", runID, err)
	}
	return nil
}

// PendingByKind counts the records the journal still owes the fleet, per kind.
//
// It is exported because it answers an operator's question without publishing
// anything: `babel sync` reports it after an attempt, and a report that said
// only what it managed to commit would leave "and what is still stuck" to be
// inferred from silence.
func (j *Journal) PendingByKind(ctx context.Context) (map[sharedcatalog.RecordKind]int, error) {
	rows, err := j.db.QueryContext(ctx,
		`SELECT kind, count(*) FROM sync_record WHERE sync_state = ? GROUP BY kind ORDER BY kind`,
		sharedcatalog.SyncPending)
	if err != nil {
		return nil, fmt.Errorf("sync: count pending records: %w", err)
	}
	defer rows.Close()
	counts := map[sharedcatalog.RecordKind]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, fmt.Errorf("sync: count pending records: %w", err)
		}
		counts[sharedcatalog.RecordKind(kind)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync: count pending records: %w", err)
	}
	return counts, nil
}

// UndeclaredRecords counts staged records whose run has never declared a
// closure.
//
// They are pending and deliberately unpublishable: an exploration interrupted
// before its receipt has written records but has not finished producing them,
// and declaring a closure at whatever it happened to reach would publish a run
// that later grows - which 0003 refuses, permanently, because record_count is
// immutable. Resuming the run under the same id closes the closure and the next
// sync carries all of it. Reporting the count is what keeps that state visible
// rather than looking like a sync that did nothing.
func (j *Journal) UndeclaredRecords(ctx context.Context) (int, error) {
	var n int
	if err := j.db.QueryRowContext(ctx, `
		SELECT count(*) FROM sync_record r
		 WHERE r.sync_state = ?
		   AND NOT EXISTS (SELECT 1 FROM sync_run u WHERE u.run_id = r.run_id)`,
		sharedcatalog.SyncPending).Scan(&n); err != nil {
		return 0, fmt.Errorf("sync: count undeclared records: %w", err)
	}
	return n, nil
}

// SyncState reports one staged record's local sync state, which is what a
// per-record surface renders beside the record (SPEC.md §9). An id the journal
// never saw reports the empty string rather than an error: a record written
// before shared mode was configured was never staged, and that is not a fault.
func (j *Journal) SyncState(ctx context.Context, entityID string) (string, error) {
	var state string
	err := j.db.QueryRowContext(ctx,
		`SELECT sync_state FROM sync_record WHERE record_id = ?`, entityID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sync: read sync state for %s: %w", entityID, err)
	}
	return state, nil
}

// nullable maps an empty optional identifier to SQL NULL, so "not pinned" and
// "not a continuation" are absent rather than an empty string a later reader
// would have to know to treat as absent.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
