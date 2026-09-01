package sync

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The journal's schema has moved twice. Version 1 to 2 added sync_record_edge
// so a typed reference edge's endpoints survive the crash window between a
// durable write and its publication (issue #113); version 2 to 3 added
// sync_record_subject so a proposal's provenance survives the same window
// (issue #114). A durable file is never discarded to make its shape fit, so both
// upgrades have to happen in place on a file this build did not create - and
// that path is the one nothing else in this suite exercises, because every other
// test starts from an empty directory.
//
// Both hops are covered, not only the newest. The upgrade is one statement
// serving every older version, which is exactly the arrangement where a
// version-1 file quietly stops being handled while the version-2 case still
// passes.

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

// v2Schema is journalSchema as version 2 shipped it: v1Schema plus
// sync_record_edge and the three triggers version 2 also carried, and without
// sync_record_subject. It is a literal copy for v1Schema's reason - an upgrade
// test that built the old shape by editing the new constant would test the new
// constant against itself, and would keep passing after an edit that made the
// upgrade a no-op.
const v2Schema = `
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
BEGIN SELECT RAISE(ABORT, 'a staged edge names the endpoints that were validated at write time; it is released, never rewritten'); END;`

// seedLegacyJournal writes a journal at the given older version, holding one
// staged, undeclared record: the state a machine is in when it is upgraded
// mid-outage, which is the case where losing the file's contents would lose
// analysis.
func seedLegacyJournal(t *testing.T, dir, schema string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, DatabaseName))
	if err != nil {
		t.Fatalf("open durable file: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create version %d schema: %v", version, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_migration(component, version) VALUES(?, ?)`, component, version); err != nil {
		t.Fatalf("record version %d: %v", version, err)
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

func TestOpeningAnOlderJournalUpgradesItInPlace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		schema  string
		version int
	}{
		{name: "from version 1", schema: v1Schema, version: 1},
		{name: "from version 2", schema: v2Schema, version: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seedLegacyJournal(t, dir, tc.schema, tc.version)

			journal, err := OpenJournal(dir)
			if err != nil {
				t.Fatalf("open a version %d journal: %v", tc.version, err)
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
			// Both tables the two upgrades added must be present whichever
			// version the file started at: the upgrade is one statement serving
			// every older version, so a file two hops behind must arrive at the
			// same shape as one hop behind.
			for _, table := range []string{"sync_record_edge", "sync_record_subject"} {
				if _, err := journal.db.Exec(`SELECT count(*) FROM ` + table); err != nil {
					t.Errorf("the upgrade did not create %s: %v", table, err)
				}
			}

			// The record that was already owed to the fleet is still owed, with
			// its payload: an upgrade that discarded it would discard analysis
			// that exists nowhere else.
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
		})
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
