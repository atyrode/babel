package sharedcatalog_test

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// openAs connects using an application role rather than the migration
// credential, which is how a normal instance reaches the catalog.
//
// The DSN is rebuilt through net/url rather than string surgery so the suite
// works against any BABEL_TEST_POSTGRES, including CI's password-authenticated
// service. Guarding this with t.Skip on a DSN pattern would let the security
// tests silently retire wherever the DSN was shaped differently.
func openAs(t *testing.T, db *sql.DB, role, password string) *sql.DB {
	t.Helper()
	var database string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database name: %v", err)
	}
	u, err := url.Parse(baseDSN())
	if err != nil {
		t.Fatalf("parse base DSN: %v", err)
	}
	u.User = url.UserPassword(role, password)
	u.Path = "/" + database

	appDB, err := sharedcatalog.Open(context.Background(), u.String())
	if err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	t.Cleanup(func() { appDB.Close() })
	return appDB
}

// The central security property of SPEC.md 9: an instance holding an
// application credential can write catalog rows but cannot change schema.
func TestAppRoleCanWriteRowsButNotSchema(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	const role, password = "babel_instance_a", "instance-a-secret"
	if err := sharedcatalog.EnsureAppRole(ctx, db, role, password); err != nil {
		t.Fatalf("EnsureAppRole: %v", err)
	}
	app := openAs(t, db, role, password)

	// Permitted: the rows an instance publishes.
	if _, err := app.Exec(
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', $1)`,
		sharedcatalog.SchemaVersion); err != nil {
		t.Fatalf("app role must be able to insert catalog rows: %v", err)
	}
	if _, err := app.Exec(`INSERT INTO hosts (host_id, deployment_id) VALUES ('h1', 'd1')`); err != nil {
		t.Fatalf("app role must be able to insert hosts: %v", err)
	}

	// Refused: every route to changing the schema.
	for _, ddl := range []string{
		`CREATE TABLE sneaky (session_uid text, title text)`,
		`ALTER TABLE sessions ADD COLUMN title text`,
		`DROP TABLE idempotency_keys`,
		`ALTER TABLE snapshots DROP COLUMN commit_state`,
	} {
		if _, err := app.Exec(ddl); err == nil {
			t.Errorf("app role was allowed to run DDL: %s", ddl)
		}
	}
}

// The migration ledger is readable but not writable: an instance must never be
// able to claim a migration it did not apply.
func TestAppRoleCannotForgeMigrationLedger(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	const role, password = "babel_instance_b", "instance-b-secret"
	if err := sharedcatalog.EnsureAppRole(ctx, db, role, password); err != nil {
		t.Fatalf("EnsureAppRole: %v", err)
	}
	app := openAs(t, db, role, password)

	var n int
	if err := app.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("app role must be able to read the ledger: %v", err)
	}
	if n == 0 {
		t.Fatal("ledger is empty after migrating")
	}
	if _, err := app.Exec(`INSERT INTO schema_migrations (version) VALUES ('9999_forged')`); err == nil {
		t.Fatal("app role forged a migration ledger entry")
	}
}

// Phase B analysis rows are the only copy of what exploration produced, and
// SPEC.md 4.7 says rejection never deletes a record. An application credential
// therefore holds no DELETE on them, so the trigger that refuses the statement
// and the privilege that refuses the credential fail independently.
func TestAppRoleCannotDeleteAnalysisRows(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	const role, password = "babel_instance_c", "instance-c-secret"
	if err := sharedcatalog.EnsureAppRole(ctx, db, role, password); err != nil {
		t.Fatalf("EnsureAppRole: %v", err)
	}
	app := openAs(t, db, role, password)

	// An instance must be able to commit its own analysis output.
	for _, stmt := range []string{
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', 1)`,
		`INSERT INTO instances (instance_id, deployment_id) VALUES ('inst-a', 'd1')`,
		`INSERT INTO analysis_runs (run_id, deployment_id, origin_instance_id, sync_state, record_count)
		 VALUES ('r1', 'd1', 'inst-a', 'pending-sync', 1)`,
		`INSERT INTO analysis_records (record_id, run_id, kind, record_schema, ordinal,
		                               object_key, key_id, ciphertext_size, object_digest)
		 VALUES ('rec1', 'r1', 'finding', 1, 0, 'analysis/rec1/abc', 'k1', 64, 'abc')`,
		`UPDATE analysis_runs SET sync_state = 'committed', committed_at = now() WHERE run_id = 'r1'`,
	} {
		if _, err := app.Exec(stmt); err != nil {
			t.Fatalf("app role must be able to commit analysis output: %v\n%s", err, stmt)
		}
	}

	// And must not be able to remove it.
	for _, stmt := range []string{
		`DELETE FROM analysis_records WHERE record_id = 'rec1'`,
		`DELETE FROM analysis_runs WHERE run_id = 'r1'`,
	} {
		if _, err := app.Exec(stmt); err == nil {
			t.Errorf("app role was allowed to delete analysis output: %s", stmt)
		}
	}

	var records, runs int
	if err := db.QueryRow(`SELECT
	        (SELECT count(*) FROM analysis_records),
	        (SELECT count(*) FROM analysis_runs)`).Scan(&records, &runs); err != nil {
		t.Fatalf("count analysis rows: %v", err)
	}
	if records != 1 || runs != 1 {
		t.Errorf("analysis rows = %d records, %d runs; want 1 and 1", records, runs)
	}
}

// Revocation is per-instance: removing one credential must not disturb another.
func TestRevokeAppRoleIsPerInstance(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	const roleA, roleB = "babel_revoke_a", "babel_revoke_b"
	for _, r := range []string{roleA, roleB} {
		if err := sharedcatalog.EnsureAppRole(ctx, db, r, r+"-secret"); err != nil {
			t.Fatalf("EnsureAppRole %s: %v", r, err)
		}
	}
	appB := openAs(t, db, roleB, roleB+"-secret")

	if err := sharedcatalog.RevokeAppRole(ctx, db, roleA); err != nil {
		t.Fatalf("RevokeAppRole: %v", err)
	}

	// B keeps working, and its data is untouched.
	if _, err := appB.Exec(
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('dB', $1)`,
		sharedcatalog.SchemaVersion); err != nil {
		t.Fatalf("revoking one instance broke another: %v", err)
	}

	var canLogin bool
	if err := db.QueryRow(`SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, roleA).Scan(&canLogin); err != nil {
		t.Fatalf("read revoked role: %v", err)
	}
	if canLogin {
		t.Fatal("revoked role can still log in")
	}
}

func TestEnsureAppRoleRejectsUnsafeNames(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, bad := range []string{
		``,
		`babel"; DROP TABLE sessions; --`,
		`Babel_Upper`,
		`1leading_digit`,
		strings.Repeat("a", 64),
	} {
		if err := sharedcatalog.EnsureAppRole(ctx, db, bad, "pw"); err == nil {
			t.Errorf("EnsureAppRole accepted unsafe role name %q", bad)
		}
	}
}

// A password must never reach an error message, since callers log errors. The
// guarantee is unconditional: it holds at the shortest accepted length, not
// only for comfortably long secrets.
func TestEnsureAppRoleErrorsOmitThePassword(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// No schema_migrations table yet, so a grant fails while the password is in
	// scope. Exactly 12 characters: the minimum EnsureAppRole accepts.
	const password = "abcdefghijkl"
	err := sharedcatalog.EnsureAppRole(ctx, db, "babel_leak_probe", password)
	if err == nil {
		t.Skip("grants unexpectedly succeeded against an unmigrated database")
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error leaked the password: %v", err)
	}
}

// A password too short to redact safely is refused outright, which is what
// makes the redaction guarantee unconditional.
func TestEnsureAppRoleRequiresARealPassword(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	for _, short := range []string{"", "pw", "abcdefghijk"} {
		err := sharedcatalog.EnsureAppRole(ctx, db, "babel_short_pw", short)
		if err == nil {
			t.Errorf("EnsureAppRole accepted a %d-character password", len(short))
		}
	}
}

// Migrate claims transactional DDL. Prove it: a migration that fails partway
// must leave no trace, not half a schema.
func TestFailedMigrationLeavesNoPartialSchema(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// A table the first migration will collide with, forcing 0001 to fail after
	// its earlier statements have already run inside the transaction. The schema
	// is created here because Migrate has not run yet; Migrate creating it is
	// idempotent, and this proves the collision path rather than the schema one.
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS ` + sharedcatalog.Schema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (placeholder text)`); err != nil {
		t.Fatalf("seed conflicting table: %v", err)
	}

	if _, err := sharedcatalog.Migrate(ctx, db); err == nil {
		t.Fatal("Migrate reported success despite a colliding table")
	}

	// Everything 0001 created before the collision must be gone.
	for _, table := range []string{"deployments", "instances", "hosts", "snapshots", "host_leases"} {
		var exists bool
		if err := db.QueryRow(
			`SELECT exists(SELECT 1 FROM information_schema.tables
			  WHERE table_schema = current_schema() AND table_name = $1)`, table).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if exists {
			t.Errorf("failed migration left %s behind: DDL was not atomic", table)
		}
	}

	// And the ledger must not claim the migration applied.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if n != 0 {
		t.Fatalf("ledger recorded %d migrations after a failure, want 0", n)
	}
}

// The allowlist must cover every table the migrations actually create, or the
// gate has a blind spot.
func TestAllowlistCoversEveryMigratedTable(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	rows, err := db.Query(`SELECT table_name FROM information_schema.tables
	                       WHERE table_schema = current_schema()`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	listing := strings.Join(sharedcatalog.Allowlist(), "\n")
	var count int
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		count++
		if !strings.Contains(listing, table+".") {
			t.Errorf("table %q exists but contributes no allowlist entries", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if count == 0 {
		t.Fatal("migrated database reports no tables")
	}
}
