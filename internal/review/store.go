package review

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the durable database's shared
// migration ledger. The file already holds internal/frontier's and
// internal/run's tables; a new durable component joins it with its own
// component key and table prefix rather than opening a second file, because
// the whole point of one durable file is that a single pending-sync journal is
// what an operator has to preserve.
const component = "review"

// storeVersion is this component's schema version.
const storeVersion = 1

// databaseFile is the durable, pending-sync database, named the same way
// internal/frontier and internal/run name it. It is deliberately not the
// retrieval index's file: the index is a rebuildable cache, while a review
// decision exists only here until it is remotely committed.
const databaseFile = run.DatabaseName

// schema is this component's tables and its append-only enforcement.
//
// The §9 split is mirrored per table: one payload_json column carrying
// everything a later sync slice seals in an AEAD envelope, and otherwise only
// allowlisted identifiers, kinds, schema versions, ordering, and timestamps.
// Reviewer, operator, and agent identities are plaintext for the same reason
// internal/frontier stores reviewer_id in the clear — attribution is an
// identifier, and a decision nobody can attribute is not a decision.
//
// Every table carries immutability and delete triggers rather than trusting
// this package's SQL. §4.7 says rejection never deletes a record, and an
// append-only log whose append-only-ness depends on nobody writing the wrong
// statement is not append-only.
const schema = `
CREATE TABLE IF NOT EXISTS review_context(
	id             TEXT PRIMARY KEY,
	author         TEXT NOT NULL,
	recorded_at    TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS review_queue(
	subject_type TEXT NOT NULL,
	subject_id   TEXT NOT NULL,
	enrolled_at  TEXT NOT NULL,
	PRIMARY KEY(subject_type, subject_id)
);

CREATE TABLE IF NOT EXISTS review_assessment(
	id             TEXT PRIMARY KEY,
	request_id     TEXT NOT NULL UNIQUE,
	subject_type   TEXT NOT NULL,
	subject_id     TEXT NOT NULL,
	mode           TEXT NOT NULL CHECK(mode IN ('none', 'alongside', 'instead')),
	agent_id       TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	recorded_at    TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS review_memory(
	id             TEXT PRIMARY KEY,
	assessment_id  TEXT NOT NULL UNIQUE REFERENCES review_assessment(id),
	destination    TEXT NOT NULL,
	sensitivity    TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS review_memory_disposition(
	id           TEXT PRIMARY KEY,
	memory_id    TEXT NOT NULL REFERENCES review_memory(id),
	seq          INTEGER NOT NULL,
	disposition  TEXT NOT NULL,
	authority_id TEXT NOT NULL,
	context_id   TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(memory_id, seq)
);

CREATE TABLE IF NOT EXISTS review_refinement(
	id            TEXT PRIMARY KEY,
	request_id    TEXT NOT NULL UNIQUE,
	subject_type  TEXT NOT NULL,
	subject_id    TEXT NOT NULL,
	assessment_id TEXT NOT NULL UNIQUE REFERENCES review_assessment(id),
	mode          TEXT NOT NULL,
	revision_kind TEXT NOT NULL,
	revision_id   TEXT NOT NULL,
	memory_id     TEXT NOT NULL,
	agent_id      TEXT NOT NULL,
	recorded_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS review_refinement_subject
	ON review_refinement(subject_type, subject_id);

CREATE TABLE IF NOT EXISTS review_lineage(
	id         TEXT PRIMARY KEY,
	relation   TEXT NOT NULL,
	from_kind  TEXT NOT NULL,
	from_id    TEXT NOT NULL,
	to_kind    TEXT NOT NULL,
	to_id      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(from_kind, from_id, to_kind, to_id, relation)
);
CREATE INDEX IF NOT EXISTS review_lineage_to ON review_lineage(to_kind, to_id);

CREATE TRIGGER IF NOT EXISTS review_context_immutable
BEFORE UPDATE ON review_context
BEGIN SELECT RAISE(ABORT, 'operator context is immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_context_append_only
BEFORE DELETE ON review_context
BEGIN SELECT RAISE(ABORT, 'operator context is append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_queue_append_only
BEFORE DELETE ON review_queue
BEGIN SELECT RAISE(ABORT, 'review enrolment is append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_assessment_immutable
BEFORE UPDATE ON review_assessment
BEGIN SELECT RAISE(ABORT, 'assessments are immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_assessment_append_only
BEFORE DELETE ON review_assessment
BEGIN SELECT RAISE(ABORT, 'assessments are append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_memory_immutable
BEFORE UPDATE ON review_memory
BEGIN SELECT RAISE(ABORT, 'durable-learning proposals are immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_memory_append_only
BEFORE DELETE ON review_memory
BEGIN SELECT RAISE(ABORT, 'durable-learning proposals are append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_memory_disposition_immutable
BEFORE UPDATE ON review_memory_disposition
BEGIN SELECT RAISE(ABORT, 'dispositions are immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_memory_disposition_append_only
BEFORE DELETE ON review_memory_disposition
BEGIN SELECT RAISE(ABORT, 'dispositions are append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_refinement_immutable
BEFORE UPDATE ON review_refinement
BEGIN SELECT RAISE(ABORT, 'refinement outcomes are immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_refinement_append_only
BEFORE DELETE ON review_refinement
BEGIN SELECT RAISE(ABORT, 'refinement outcomes are append-only'); END;

CREATE TRIGGER IF NOT EXISTS review_lineage_immutable
BEFORE UPDATE ON review_lineage
BEGIN SELECT RAISE(ABORT, 'lineage edges are immutable'); END;
CREATE TRIGGER IF NOT EXISTS review_lineage_append_only
BEFORE DELETE ON review_lineage
BEGIN SELECT RAISE(ABORT, 'lineage edges are append-only'); END;`

// Service is the review and refinement service of §4.7 and §6.7.
//
// It owns the review component of the durable database and reads the two
// stores it sits above: internal/frontier for the analysis records and their
// dispositions, internal/run for the run receipts §6.7 also exports. It never
// writes to either beyond the two disposition operations §4.7 defines, and it
// has no publishing path of any kind.
type Service struct {
	db       *sql.DB
	path     string
	frontier *frontier.Store
	runs     *run.Store

	// now supplies record timestamps. It is a field so a test can make
	// ordering deterministic without sleeping; production never replaces
	// it, and ordering never depends on it — every history has an explicit
	// sequence or insertion order behind it.
	now func() time.Time
}

// Open opens the review component of the durable database in dir and binds it
// to the frontier and run stores it reads.
//
// Both stores are required. A review service that cannot read the frontier
// could not validate a single subject, and one that cannot read run receipts
// could not answer §6.7's export of raw private run records; a half-configured
// service that fails only on the path a caller happens to take later is worse
// than one that refuses to open.
//
// A schema version this build does not know is an error, never a rebuild:
// review decisions are not derivable from anything, so meeting an unfamiliar
// shape means stopping rather than deleting the only copy of them.
func Open(dir string, front *frontier.Store, runs *run.Store) (*Service, error) {
	if front == nil {
		return nil, errInvalid("review service needs a frontier store")
	}
	if runs == nil {
		return nil, errInvalid("review service needs a run store")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("review: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("review: open durable database: %w", err)
	}
	// One connection: the pragmas are per connection, and §9 gives the
	// durable file a single writer.
	db.SetMaxOpenConns(1)
	s := &Service{
		db:       db,
		path:     path,
		frontier: front,
		runs:     runs,
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases this service's database handle. The frontier and run stores
// belong to the caller and are left open.
func (s *Service) Close() error { return s.db.Close() }

// Path reports the durable database path, which is the same file the frontier
// and run components use.
func (s *Service) Path() string { return s.path }

// migrate prepares the connection and brings this component's schema up to
// storeVersion, following the pragmas and the per-component ledger
// internal/frontier and internal/run already established for this file.
func (s *Service) migrate() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("review: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("review: set durable busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("review: enable durable foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("review: create migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(schema); err != nil {
			return fmt.Errorf("review: create durable schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			component, storeVersion); err != nil {
			return fmt.Errorf("review: record durable schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("review: read durable schema version: %w", err)
	case version != storeVersion:
		return fmt.Errorf("review: durable schema version %d is not supported by this build", version)
	}
	return nil
}

// transact runs fn in one transaction, rolling back on any error. A refinement
// outcome writes an assessment, a proposal, an outcome row, and several
// lineage edges, and a half-written outcome would leave a descendant whose
// assessment does not exist — exactly the record §4.7 says must never be
// separable.
func (s *Service) transact(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("review: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("review: commit transaction: %w", err)
	}
	return nil
}

// errInvalid wraps ErrInvalidValue with a reason. Reasons name fields, never
// values: an error message is a surface §9 keeps corpus content out of.
func errInvalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidValue, reason)
}

// newID generates a globally unique client-side identifier, which §9 requires
// for immutable entities and events. The prefix makes a stray ID in a log
// self-describing without revealing anything about the corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("review: generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// marshalPayload encodes the §9 encryption-bound half of a record. Every
// payload passes through here, so the later sync slice has one place to wrap
// in an AEAD envelope.
func marshalPayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("review: encode payload: %w", err)
	}
	return encoded, nil
}

// formatTime stores timestamps as UTC RFC 3339 with nanoseconds, which sorts
// lexicographically in the same order it sorts chronologically. It matches
// what the durable file's other components write, so a human reading the
// database sees one format.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("review: parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
