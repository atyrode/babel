package fleet_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The fleet read path is a read over a real shared catalog, so the suite needs
// one. The properties it defends - a join across four tables, NULLS LAST
// ordering, a filtered aggregate, server-side timestamps - are exactly the
// things a fake reproduces dishonestly, which is the same reasoning
// internal/sharedcatalog's harness gives.
//
// The provisioning discipline is that harness's, deliberately: a throwaway
// cluster when initdb is on PATH (the case inside `nix develop`),
// BABEL_TEST_POSTGRES when an external server is preferred, and an honest skip
// otherwise - except where the environment promised a server, which fails
// loudly rather than retiring the gate in silence.
var baseDSN string

func TestMain(m *testing.M) {
	if dsn := os.Getenv("BABEL_TEST_POSTGRES"); dsn != "" {
		baseDSN = dsn
		os.Exit(m.Run())
	}
	if !pgtest.Available() {
		if pgtest.Required() {
			fmt.Fprintf(os.Stderr,
				"%s is set but neither BABEL_TEST_POSTGRES nor initdb is available\n",
				pgtest.RequireEnv)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "skipping: no BABEL_TEST_POSTGRES and initdb is not on PATH")
		os.Exit(0)
	}
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

// newDB hands each test its own migrated database, so one test's committed runs
// cannot become another's fleet.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := t.Context()

	admin, err := sharedcatalog.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("babel_fleet_test_%d", dbSeq.Add(1))
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

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
	if _, err := sharedcatalog.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// memStore is the injected object store. Phase B objects are content-addressed
// and never deleted, so an in-memory map is a faithful stand-in for what the
// read path asks of a store: put once, get by key.
type memStore struct {
	objects map[string][]byte
	// failGet, if set, decides whether a fetch fails, so a store outage can be
	// exercised as the per-record failure it is rather than a fatal one.
	failGet func(key string) error
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Put(ctx context.Context, key string, data []byte) error {
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.failGet != nil {
		if err := s.failGet(key); err != nil {
			return nil, err
		}
	}
	stored, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return append([]byte(nil), stored...), nil
}

func newKeyring(t *testing.T, id string) *envelope.Keyring {
	t.Helper()
	key, err := envelope.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := envelope.NewKeyring(envelope.KeyID(id), key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	return ring
}
