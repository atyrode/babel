package explore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"

	_ "modernc.org/sqlite"
)

// ledgerComponent names this package's rows in the durable database's shared
// migration ledger, beside internal/frontier's and internal/run's. Keying
// migrations by component is what lets a third component join the file without
// a coordinating global version.
const ledgerComponent = "explore"

// ledgerVersion is this component's schema version. A version this build does
// not know is an error rather than a rebuild: the frontier records the ledger
// points at are not derivable from anything, so a resumed run that could not
// read the ledger would duplicate them.
const ledgerVersion = 1

// ErrLedgerConflict reports a ledger row that already binds an emitted
// reference to a different durable record. It is a corrupt ledger rather than
// a duplicate write: the binding is what makes resumption non-duplicating, so
// two answers for one reference means the answer cannot be trusted at all.
var ErrLedgerConflict = errors.New("explore: ledger already binds this reference to another record")

// ledgerSchema is the resume ledger: which item a worker emitted under which
// reference became which durable frontier record, per run and per stage.
//
// §6.5 requires interrupted exploration to resume without losing or
// duplicating committed state, and the frontier cannot answer that question by
// itself. Its record identities are minted per write, so a re-run that simply
// replayed a worker's result would create a second hypothesis with the same
// wording and no way to tell which one the run meant. This table is the
// missing binding, and it is append-only for the same reason every other
// durable table here is: a resumed run must be able to trust it.
const ledgerSchema = `
CREATE TABLE IF NOT EXISTS explore_commit(
	run_id      TEXT NOT NULL,
	stage       TEXT NOT NULL,
	ref         TEXT NOT NULL,
	entity_type TEXT NOT NULL,
	entity_id   TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY(run_id, stage, ref)
);

CREATE INDEX IF NOT EXISTS explore_commit_entity_idx ON explore_commit(entity_type, entity_id);

CREATE TRIGGER IF NOT EXISTS explore_commit_immutable
BEFORE UPDATE ON explore_commit
BEGIN SELECT RAISE(ABORT, 'explore commits are immutable'); END;

CREATE TRIGGER IF NOT EXISTS explore_commit_append_only
BEFORE DELETE ON explore_commit
BEGIN SELECT RAISE(ABORT, 'explore commits are append-only'); END;`

// Commit is one binding between an item a worker emitted and the durable
// record Babel wrote for it.
type Commit struct {
	// Ref is the worker's own reference for the emitted item, unique within
	// one run and stage. It is the worker's string and never an identifier
	// Babel assigned.
	Ref string
	// Type and ID name the durable frontier record the reference produced.
	Type frontier.EntityType
	ID   string
	At   time.Time
}

// Ledger is the durable record of what a run has already committed.
//
// It lives in the same file as the hypothesis frontier and the run receipts —
// SPEC.md §9's durable, pending-sync state — under its own component key and
// table prefix, because a resume ledger that could be lost independently of
// the records it indexes would be worse than no ledger at all: the run would
// resume, find the frontier already holding its candidates, and not know it.
type Ledger struct {
	db   *sql.DB
	path string
}

// OpenLedger opens the durable database in dir and creates this component's
// table if it is absent, on the same terms as internal/run's store: a schema
// version this build does not know is reported, never resolved by discarding
// the file.
func OpenLedger(dir string) (*Ledger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("explore: create state directory: %w", err)
	}
	path := filepath.Join(dir, run.DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("explore: open durable database: %w", err)
	}
	db.SetMaxOpenConns(1)
	l := &Ledger{db: db, path: path}
	if err := l.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

// Close releases the database handle.
func (l *Ledger) Close() error { return l.db.Close() }

// Path reports the durable database's path, which is the same file the
// frontier and the receipts use.
func (l *Ledger) Path() string { return l.path }

// migrate prepares the connection and brings this component's schema up to
// ledgerVersion. The pragmas are internal/run's, because the two components
// share the file and a connection that disagreed about journalling or foreign
// keys would be a property of whichever store happened to open it first.
func (l *Ledger) migrate() error {
	var journal string
	if err := l.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("explore: enable durable WAL: %w", err)
	}
	if _, err := l.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("explore: set durable busy timeout: %w", err)
	}
	if _, err := l.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("explore: enable durable foreign keys: %w", err)
	}
	if _, err := l.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("explore: create migration ledger: %w", err)
	}

	var version int
	err := l.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, ledgerComponent).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := l.db.Exec(ledgerSchema); err != nil {
			return fmt.Errorf("explore: create resume ledger: %w", err)
		}
		if _, err := l.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			ledgerComponent, ledgerVersion); err != nil {
			return fmt.Errorf("explore: record resume ledger version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("explore: read resume ledger version: %w", err)
	case version != ledgerVersion:
		return fmt.Errorf("explore: resume ledger version %d is not supported by this build", version)
	}
	return nil
}

// Committed reads what one run's stage has already written, keyed by the
// reference the worker emitted it under. A resumed attempt consults this
// before every write, so replaying a result recognizes its own prior records
// instead of adding second copies of them.
func (l *Ledger) Committed(ctx context.Context, runID string, stage Stage) (map[string]Commit, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT ref, entity_type, entity_id, recorded_at
		FROM explore_commit WHERE run_id = ? AND stage = ?`, runID, string(stage))
	if err != nil {
		return nil, fmt.Errorf("explore: read resume ledger: %w", err)
	}
	defer rows.Close()
	committed := make(map[string]Commit)
	for rows.Next() {
		var (
			c        Commit
			kind     string
			recorded string
		)
		if err := rows.Scan(&c.Ref, &kind, &c.ID, &recorded); err != nil {
			return nil, fmt.Errorf("explore: read resume ledger: %w", err)
		}
		c.Type = frontier.EntityType(kind)
		if c.At, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, fmt.Errorf("explore: resume ledger row %q has an unreadable time: %w", c.Ref, err)
		}
		committed[c.Ref] = c
	}
	return committed, rows.Err()
}

// Record binds an emitted reference to the durable record it produced.
//
// Writing the same binding twice is not an error: a run that was interrupted
// between the frontier write and this one re-derives the same pair, and
// refusing it would make the ledger's own crash window unrecoverable. A
// reference already bound to a different record is ErrLedgerConflict.
func (l *Ledger) Record(ctx context.Context, runID string, stage Stage, c Commit) error {
	if runID == "" || stage == "" || c.Ref == "" || c.ID == "" || c.Type == "" {
		return fmt.Errorf("explore: incomplete ledger commit")
	}
	res, err := l.db.ExecContext(ctx, `INSERT OR IGNORE INTO explore_commit(
		run_id, stage, ref, entity_type, entity_id, recorded_at) VALUES(?, ?, ?, ?, ?, ?)`,
		runID, string(stage), c.Ref, string(c.Type), c.ID, c.At.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("explore: record resume ledger commit: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return nil
	}
	var storedType, storedID string
	if err := l.db.QueryRowContext(ctx, `SELECT entity_type, entity_id FROM explore_commit
		WHERE run_id = ? AND stage = ? AND ref = ?`, runID, string(stage), c.Ref).
		Scan(&storedType, &storedID); err != nil {
		return fmt.Errorf("explore: read stored ledger commit: %w", err)
	}
	if storedType != string(c.Type) || storedID != c.ID {
		return fmt.Errorf("explore: reference %q in run %q stage %q: %w", c.Ref, runID, stage, ErrLedgerConflict)
	}
	return nil
}
