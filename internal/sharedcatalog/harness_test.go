package sharedcatalog

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/atyrode/babel/internal/pgtest"
)

// The suite needs a real PostgreSQL: the contract it defends is transactional
// DDL, server-authoritative time, fenced leases, and information_schema
// reflection, none of which a fake reproduces honestly. It provisions a
// throwaway cluster when initdb is on PATH (the case inside `nix develop`),
// honours BABEL_TEST_POSTGRES when an external server is preferred, and skips
// otherwise rather than passing vacuously.
//
// The harness lives in the internal test package because lease tests need the
// publishDelayForTests seam, and only one TestMain may exist per test binary.
// The external test package reaches it through NewTestDB and TestingBaseDSN.
var baseDSN string

func TestMain(m *testing.M) {
	if dsn := os.Getenv("BABEL_TEST_POSTGRES"); dsn != "" {
		baseDSN = dsn
		os.Exit(m.Run())
	}
	if !pgtest.Available() {
		fmt.Fprintln(os.Stderr, "skipping: no BABEL_TEST_POSTGRES and initdb is not on PATH")
		os.Exit(0)
	}
	// Plaintext loopback is enough here: this suite opens connections itself, so
	// it never passes through the configuration document that requires TLS. The
	// end-to-end suite drives the shipped commands and therefore does.
	cluster, err := pgtest.Start(pgtest.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision postgres: %v\n", err)
		os.Exit(1)
	}
	baseDSN = cluster.BaseDSN
	code := m.Run()
	cluster.Stop()
	os.Exit(code)
}

var dbSeq atomic.Int64

// newDB hands each test its own database so migrations, dropped columns, and
// deliberately corrupted schemas cannot leak between cases.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := t.Context()

	admin, err := Open(ctx, baseDSN)
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

	db, err := Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newInternalDB migrates a fresh database, which is what lease and publication
// tests want: they exercise behaviour on top of the schema rather than the
// schema itself.
func newInternalDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newDB(t)
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// NewTestDB gives the external test package a fresh unmigrated database.
// Exported identifiers declared in a package's own test files are visible to
// its external test package, which is what lets one harness serve both.
func NewTestDB(t *testing.T) *sql.DB { return newDB(t) }

// TestingBaseDSN reports the DSN the suite is running against, for tests that
// need to reconnect as a different role or to a different database.
func TestingBaseDSN() string { return baseDSN }
