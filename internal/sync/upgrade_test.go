package sync

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The journal's schema moved from version 1 to version 2 when the typed
// reference graph needed an edge's endpoints to survive the crash window
// between a durable write and its publication (issue #113). A durable file is
// never discarded to make its shape fit, so the upgrade has to happen in place
// on a file this build did not create - and that path is the one nothing else
// in this suite exercises, because every other test starts from an empty
// directory.

// v1Schema is journalSchema as version 1 shipped it: the same three tables and
// five triggers, without sync_record_edge. It is a literal copy on purpose - an
// upgrade test that built the old shape by editing the new constant would test
// the new constant against itself.
const v1Schema = `
CREATE TABLE IF NOT EXISTS sync_run(
	run_id            TEXT PRIMARY KEY,
	execution_host_id TEXT,
	continues_run_id  TEXT,
	record_count      INTEGER NOT NULL CHECK (record_count > 0),
	declared_at       TEXT NOT NULL,
	sync_state        TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed'))
);

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

CREATE TABLE IF NOT EXISTS sync_payload(
	record_id TEXT PRIMARY KEY REFERENCES sync_record(record_id),
	payload   BLOB NOT NULL
);

CREATE TRIGGER IF NOT EXISTS sync_record_append_only
BEFORE DELETE ON sync_record
BEGIN SELECT RAISE(ABORT, 'the sync journal is append-only'); END;`

// seedVersion1 writes a version-1 journal holding one staged, undeclared
// record: the state a machine is in when it is upgraded mid-outage, which is
// the case where losing the file's contents would lose analysis.
func seedVersion1(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, DatabaseName))
	if err != nil {
		t.Fatalf("open durable file: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatalf("create version 1 schema: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_migration(component, version) VALUES(?, 1)`, component); err != nil {
		t.Fatalf("record version 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_record(
		record_id, run_id, kind, record_schema, ordinal, staged_at, sync_state)
		VALUES('legacy-hyp', 'legacy-run', 'hypothesis', 1, 0, '2026-08-30T00:00:00.000000000Z', 'pending-sync')`); err != nil {
		t.Fatalf("stage a legacy record: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sync_payload(record_id, payload) VALUES('legacy-hyp', ?)`,
		[]byte(`{"claim":"written before the upgrade"}`)); err != nil {
		t.Fatalf("stage a legacy payload: %v", err)
	}
}

func TestOpeningAVersion1JournalUpgradesItInPlace(t *testing.T) {
	dir := t.TempDir()
	seedVersion1(t, dir)

	journal, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("open a version 1 journal: %v", err)
	}
	defer journal.Close()

	var version int
	if err := journal.db.QueryRow(
		`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != journalVersion {
		t.Errorf("schema version = %d after the upgrade, want %d", version, journalVersion)
	}
	if _, err := journal.db.Exec(`SELECT count(*) FROM sync_record_edge`); err != nil {
		t.Errorf("the upgrade did not create sync_record_edge: %v", err)
	}

	// The record that was already owed to the fleet is still owed, with its
	// payload: an upgrade that discarded it would discard analysis that exists
	// nowhere else.
	if got, err := journal.SyncState(t.Context(), "legacy-hyp"); err != nil || got != "pending-sync" {
		t.Errorf("the legacy record reports %q (%v), want pending-sync", got, err)
	}
	if n, err := journal.UndeclaredRecords(t.Context()); err != nil || n != 1 {
		t.Errorf("undeclared records = %d (%v), want the legacy record", n, err)
	}
	var payload []byte
	if err := journal.db.QueryRow(
		`SELECT payload FROM sync_payload WHERE record_id = 'legacy-hyp'`).Scan(&payload); err != nil {
		t.Fatalf("read the legacy payload: %v", err)
	}
	if !bytesContains(payload, "written before the upgrade") {
		t.Error("the legacy payload did not survive the upgrade")
	}
}

// EnsureSchema is what a durable writer calls on its own handle, with no
// version check of its own. It must create what is missing and it must never
// relabel a file a newer build migrated: an old binary writing "2" over a "3"
// would leave every build reading a shape the label does not describe.
func TestEnsureSchemaNeverLowersTheRecordedVersion(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, DatabaseName))
	if err != nil {
		t.Fatalf("open durable file: %v", err)
	}
	defer db.Close()

	if err := EnsureSchema(db); err != nil {
		t.Fatalf("ensure schema on an empty file: %v", err)
	}
	var version int
	if err := db.QueryRow(
		`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != journalVersion {
		t.Fatalf("schema version = %d, want %d", version, journalVersion)
	}

	// A file a future build migrated further keeps its version, and this build
	// refuses it rather than publishing from a shape it cannot see whole.
	if _, err := db.Exec(`UPDATE schema_migration SET version = ? WHERE component = ?`,
		journalVersion+1, component); err != nil {
		t.Fatalf("simulate a newer file: %v", err)
	}
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("ensure schema on a newer file: %v", err)
	}
	if err := db.QueryRow(
		`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != journalVersion+1 {
		t.Errorf("schema version = %d, want the newer %d it already held", version, journalVersion+1)
	}
	if _, err := OpenJournal(dir); err == nil {
		t.Error("a journal migrated past this build was opened rather than refused")
	}
}
