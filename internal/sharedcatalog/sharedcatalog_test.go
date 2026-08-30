package sharedcatalog_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The harness lives in the internal test package (harness_test.go) because
// lease tests need an unexported seam, and only one TestMain may exist per test
// binary. Exported identifiers declared in a package's own _test.go files are
// visible from its external test package - the standard export_test.go idiom.
func newDB(t *testing.T) *sql.DB { return sharedcatalog.NewTestDB(t) }

func baseDSN() string { return sharedcatalog.TestingBaseDSN() }

func mustMigrate(t *testing.T, db *sql.DB) []string {
	t.Helper()
	applied, err := sharedcatalog.Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return applied
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	again, err := sharedcatalog.Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v, want nothing", again)
	}
}

// The allowlist exists to stop a future migration from widening the plaintext
// boundary. These are the shapes that widening actually takes.
//
// The column is `transcript_body`, not `title`: migrations/0004 admitted a
// title by explicit operator decision, and transcript content is what the
// boundary is now for. A column named after the thing SPEC.md 9 says must never
// leave the encrypted repository is the sharpest case the gate has to catch.
func TestVerifyRejectsDisallowedColumn(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN transcript_body text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted sessions.transcript_body")
	}
	if !strings.Contains(err.Error(), "sessions.transcript_body") {
		t.Fatalf("error must name the offending column, got: %v", err)
	}
}

func TestVerifyRejectsUnknownTable(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`CREATE TABLE transcripts (session_uid text, body text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted an unlisted table")
	}
	if !strings.Contains(err.Error(), "transcripts") {
		t.Fatalf("error must name the table, got: %v", err)
	}
}

func TestVerifyReportsMissingColumn(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`ALTER TABLE snapshots DROP COLUMN reconciled_at`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted a schema missing an allowlisted column")
	}
	if !strings.Contains(err.Error(), "snapshots.reconciled_at") {
		t.Fatalf("error must name the absent column, got: %v", err)
	}
}

// Verify reports every problem at once: a migration review wants the whole
// picture, not the first failure.
//
// The two columns are chosen to be ones the contract still refuses after
// migrations/0004 widened it. sessions.primary_path is a filesystem path to the
// transcript itself, and hosts.hostname is infrastructure identity; admitting a
// title and a workspace did not admit either, and this is where that stays
// true. It is also the non-vacuity check for the widening: if the allowlist had
// been loosened by class rather than by column, these would sail through.
func TestVerifyReportsEveryProblem(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN primary_path text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN hostname text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted two disallowed columns")
	}
	for _, want := range []string{"sessions.primary_path", "hosts.hostname"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error omits %s: %v", want, err)
		}
	}
}

// Migrate verifies before returning success, so a migration that widens the
// boundary fails at apply time rather than after rows are written.
func TestMigrateFailsWhenSchemaLeavesAllowlist(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN primary_path text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if _, err := sharedcatalog.Migrate(context.Background(), db); err == nil {
		t.Fatal("Migrate reported success against an out-of-contract schema")
	}
}

func TestEnsureCompatible(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	// Migrated but nothing registered yet: version 0 means unrecorded, not
	// older. Refusing here would tell an operator to run the migration they
	// just ran, which is what the ledger check exists to avoid.
	if err := sharedcatalog.EnsureCompatible(ctx, db); err != nil {
		t.Fatalf("EnsureCompatible refused a migrated but unregistered catalog: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', $1)`,
		sharedcatalog.SchemaVersion); err != nil {
		t.Fatalf("insert deployment: %v", err)
	}
	if err := sharedcatalog.EnsureCompatible(ctx, db); err != nil {
		t.Fatalf("EnsureCompatible rejected a matching version: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d2', $1)`,
		sharedcatalog.SchemaVersion+1); err != nil {
		t.Fatalf("insert newer deployment: %v", err)
	}
	err := sharedcatalog.EnsureCompatible(ctx, db)
	if !errors.Is(err, sharedcatalog.ErrSchemaTooNew) {
		t.Fatalf("EnsureCompatible must refuse a newer schema, got: %v", err)
	}
}

// Accepting an unrecorded version costs an accidental guard: version 0 used to
// refuse a half-migrated database too. The ledger is what genuinely answers
// "is anything pending", so the guard must come from there instead.
func TestEnsureCompatibleRefusesAPendingMigration(t *testing.T) {
	db := newDB(t)
	applied := mustMigrate(t, db)
	ctx := context.Background()
	if len(applied) < 2 {
		t.Skipf("this test needs at least two migrations, got %d", len(applied))
	}

	// Forget the last migration: from the ledger's point of view it is pending,
	// which is the state an interrupted `storage migrate` leaves behind.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version = $1`, applied[len(applied)-1]); err != nil {
		t.Fatalf("forget a migration: %v", err)
	}

	err := sharedcatalog.EnsureCompatible(ctx, db)
	if err == nil {
		t.Fatal("EnsureCompatible accepted a catalog with a pending migration")
	}
	if !strings.Contains(err.Error(), "storage migrate") {
		t.Errorf("the refusal must name the command that fixes it, got: %v", err)
	}

	// And write authority is refused with it, which is the property that
	// matters: a half-migrated catalog must not be published into.
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a", sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute); err == nil {
		t.Error("a lease was granted against a catalog with a pending migration")
	}
}

// A connection error must never echo the DSN: it carries the catalog password.
func TestOpenErrorsRedactTheDSN(t *testing.T) {
	dsn := "postgres://babel:sup3rsecret@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=1"
	db, err := sharedcatalog.Open(context.Background(), dsn)
	if err == nil {
		db.Close()
		t.Skip("port 1 unexpectedly accepted a connection")
	}
	if strings.Contains(err.Error(), "sup3rsecret") {
		t.Fatalf("error leaked the password: %v", err)
	}
}

// A fetch locator must stay out of the catalog, and so must transcript content
// and infrastructure identity.
//
// migrations/0004 admitted a session's title, its workspace and its
// continuation grade, and a host's display name, by explicit operator decision.
// This list is what did NOT come with them, and the distinction is the point:
// `workspace` is a directory a human reads, while `selector`, `source_id` and
// `primary_path` are how a session is addressed and fetched, and admitting them
// would make the catalog alone sufficient to pull transcript bytes. `hostname`
// is a machine's infrastructure identity, which is not what a display name is
// (reconcile.go refuses to adopt snapshots recorded under one). `transcript` is
// the content itself.
func TestAllowlistExcludesSensitiveNames(t *testing.T) {
	forbidden := []string{
		"path", "primary_path", "selector", "source_id",
		"hostname", "repo", "branch", "transcript",
	}
	listing := strings.Join(sharedcatalog.Allowlist(), "\n")
	for _, name := range forbidden {
		if strings.Contains(listing, "."+name+":") {
			t.Errorf("allowlist admits %q, which SPEC.md 9 keeps out of PostgreSQL", name)
		}
	}

	// The other half of the contract: the four columns the operator did admit
	// must actually be listed, or Verify would reject the schema this
	// repository's own migration creates and the widening would be a
	// half-change nobody could apply.
	for _, want := range []string{
		"sessions.title:", "sessions.title_provenance:",
		"sessions.workspace:", "sessions.continuation_grade:",
		"hosts.display_name:",
	} {
		if !strings.Contains(listing, want) {
			t.Errorf("allowlist omits %q, admitted by operator decision 2026-08-30", want)
		}
	}
}

// Server time, not client time, orders leases (SPEC.md 9). Proving the source
// here keeps #17's fencing work honest.
func TestNowIsServerAuthoritative(t *testing.T) {
	db := newDB(t)
	var fromServer, alsoServer string
	if err := db.QueryRow(`SELECT now()::text, clock_timestamp()::text`).Scan(&fromServer, &alsoServer); err != nil {
		t.Fatalf("read server time: %v", err)
	}
	if fromServer == "" || alsoServer == "" {
		t.Fatal("server returned no time")
	}
}
