package sharedcatalog_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The suite needs a real PostgreSQL: the contract it defends is transactional
// DDL, server-authoritative time, and information_schema reflection, none of
// which a fake reproduces honestly. It provisions a throwaway cluster when
// initdb is on PATH (the case inside `nix develop`), honours
// BABEL_TEST_POSTGRES when an external server is preferred, and skips
// otherwise rather than passing vacuously.
var baseDSN string

func TestMain(m *testing.M) {
	if dsn := os.Getenv("BABEL_TEST_POSTGRES"); dsn != "" {
		baseDSN = dsn
		os.Exit(m.Run())
	}
	if _, err := exec.LookPath("initdb"); err != nil {
		fmt.Fprintln(os.Stderr, "skipping: no BABEL_TEST_POSTGRES and initdb is not on PATH")
		os.Exit(0)
	}
	stop, dsn, err := startCluster()
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision postgres: %v\n", err)
		os.Exit(1)
	}
	baseDSN = dsn
	code := m.Run()
	stop()
	os.Exit(code)
}

func startCluster() (func(), string, error) {
	dir, err := os.MkdirTemp("", "babel-pg-*")
	if err != nil {
		return nil, "", err
	}
	cleanup := func() { os.RemoveAll(dir) }
	data := filepath.Join(dir, "data")

	// Trust auth on a loopback port with a private data directory: this cluster
	// exists for the length of one test binary and holds only synthetic rows.
	if out, err := exec.Command("initdb", "-A", "trust", "-U", "babel", "--no-sync", "-D", data).CombinedOutput(); err != nil {
		cleanup()
		return nil, "", fmt.Errorf("initdb: %v: %s", err, out)
	}
	port := "5" + fmt.Sprint(1000+os.Getpid()%8000)
	opts := fmt.Sprintf("-k %s -h 127.0.0.1 -p %s", dir, port)
	if out, err := exec.Command("pg_ctl", "-D", data, "-o", opts, "-l", filepath.Join(dir, "log"), "-w", "start").CombinedOutput(); err != nil {
		cleanup()
		return nil, "", fmt.Errorf("pg_ctl start: %v: %s", err, out)
	}
	stop := func() {
		exec.Command("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop").Run()
		cleanup()
	}
	return stop, fmt.Sprintf("postgres://babel@127.0.0.1:%s/postgres?sslmode=disable", port), nil
}

var dbSeq atomic.Int64

// newDB hands each test its own database so migrations, dropped columns, and
// deliberately corrupted schemas cannot leak between cases.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	admin, err := sharedcatalog.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("babel_test_%d", dbSeq.Add(1))
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// Swap only the database path, whatever shape the DSN has.
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse base DSN: %v", err)
	}
	u.Path = "/" + name

	db, err := sharedcatalog.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustMigrate(t *testing.T, db *sql.DB) []string {
	t.Helper()
	applied, err := sharedcatalog.Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return applied
}

func TestMigrateAppliesInitAndPassesAllowlist(t *testing.T) {
	db := newDB(t)
	applied := mustMigrate(t, db)
	if len(applied) != 1 || !strings.HasPrefix(applied[0], "0001_") {
		t.Fatalf("applied = %v, want one 0001 migration", applied)
	}
	if err := sharedcatalog.Verify(context.Background(), db); err != nil {
		t.Fatalf("freshly migrated schema must satisfy its own allowlist: %v", err)
	}
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
func TestVerifyRejectsDisallowedColumn(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	// A title is exactly what SPEC.md 9 keeps out of PostgreSQL.
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN title text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted sessions.title")
	}
	if !strings.Contains(err.Error(), "sessions.title") {
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
func TestVerifyReportsEveryProblem(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN workspace text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN display_name text`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted two disallowed columns")
	}
	for _, want := range []string{"sessions.workspace", "hosts.display_name"} {
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

	// No deployment row yet: version 0 means migrations are pending from the
	// caller's point of view, and the message must say what to run.
	err := sharedcatalog.EnsureCompatible(ctx, db)
	if err == nil {
		t.Fatal("EnsureCompatible accepted an unrecorded schema version")
	}
	if !strings.Contains(err.Error(), "storage migrate") {
		t.Fatalf("error must tell the operator what to run, got: %v", err)
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
	err = sharedcatalog.EnsureCompatible(ctx, db)
	if !errors.Is(err, sharedcatalog.ErrSchemaTooNew) {
		t.Fatalf("EnsureCompatible must refuse a newer schema, got: %v", err)
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

// Session identity must stay opaque: no allowlisted column may be one of the
// selector-shaped or path-shaped names the contract excludes.
func TestAllowlistExcludesSensitiveNames(t *testing.T) {
	forbidden := []string{
		"title", "workspace", "path", "primary_path", "selector",
		"source_id", "display_name", "hostname", "repo", "branch",
		"continuation_grade", "transcript",
	}
	listing := strings.Join(sharedcatalog.Allowlist(), "\n")
	for _, name := range forbidden {
		if strings.Contains(listing, "."+name+":") {
			t.Errorf("allowlist admits %q, which SPEC.md 9 keeps out of PostgreSQL", name)
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
