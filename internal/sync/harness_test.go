package sync

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	stdsync "sync"
	"sync/atomic"
	"testing"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This suite needs a real PostgreSQL for the same reason internal/sharedcatalog's
// does: what it defends is a commit ordering across two stores, and the
// visibility boundary it turns on is a PostgreSQL transaction. A fake would
// reproduce the happy path and none of the crash windows.
//
// It provisions a throwaway cluster when initdb is on PATH (the case inside
// `nix develop`), honours BABEL_TEST_POSTGRES when an external server is
// preferred, and skips otherwise rather than passing vacuously - unless the
// environment promised a server, in which case a missing one fails.
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

// newCatalog hands each test its own migrated database with the deployment,
// host and instance rows a Phase B run's foreign keys require - exactly the
// rows Register writes on a real machine's first push.
func newCatalog(t *testing.T) *sql.DB {
	t.Helper()
	ctx := t.Context()

	admin, err := sharedcatalog.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("babel_sync_test_%d", dbSeq.Add(1))
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
	if err := sharedcatalog.Register(ctx, db, testDeployment, testHost, testInstance,
		sharedcatalog.HostIdentity{DisplayName: testHost, OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return db
}

const (
	testDeployment = "fixturedeployment"
	testInstance   = "fixtureinstance"
	testHost       = "fixturehost"
)

// sentinel marks every payload these tests stage. It must never appear in a
// PostgreSQL column, and it must be gone from the local journal once a record
// has committed - the two properties the journal's payload table exists to make
// checkable.
const sentinel = "BABEL-SYNTHETIC-PHASE-B-SENTINEL-do-not-leak"

// memStore is the injected object store. Failures are programmable per key so
// the protocol's remote failure points can be exercised separately, and onGet
// runs between an object's verified write and the row that names it - which is
// precisely the crash window migration 0003 was written around.
type memStore struct {
	mu      stdsync.Mutex
	objects map[string][]byte
	puts    int

	failPut func(key string) error
	onGet   func(key string)
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.puts++
	s.objects[key] = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	stored, ok := s.objects[key]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("object %q not found", key)
	}
	out := append([]byte(nil), stored...)
	hook := s.onGet
	s.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	return out, nil
}

func (s *memStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

func (s *memStore) objectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// contains reports whether any stored object holds the marker. Objects are
// sealed, so the answer must be no for the sentinel and yes only for a marker
// the test wrote in the clear as a positive control.
func (s *memStore) contains(marker string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, object := range s.objects {
		if bytesContains(object, marker) {
			return true
		}
	}
	return false
}

func bytesContains(haystack []byte, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack []byte, needle string) int {
	limit := len(haystack) - len(needle)
	for i := 0; i <= limit; i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}

// fixture is one publisher wired to a throwaway catalog, an in-memory object
// store, and a journal in a temporary durable database.
type fixture struct {
	pub      *Publisher
	journal  *Journal
	catalog  *sql.DB
	store    *memStore
	ring     *envelope.Keyring
	dir      string
	failures []error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	journal, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { journal.Close() })

	key, err := envelope.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := envelope.RingFrom("phase-b-1", map[envelope.KeyID][]byte{"phase-b-1": key})
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}

	f := &fixture{journal: journal, catalog: newCatalog(t), store: newMemStore(), ring: ring, dir: dir}
	pub, err := New(Options{
		Config: config.Config{
			Mode:         config.ModeShared,
			DeploymentID: testDeployment,
			InstanceID:   testInstance,
		},
		Journal: journal,
		Catalog: f.catalog,
		Store:   f.store,
		Keyring: ring,
		Diag:    func(err error) { f.failures = append(f.failures, err) },
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	f.pub = pub
	return f
}

// writerTx is the durable writer's own transaction. Staging shares it, which is
// the property every one of these tests depends on: a record is staged exactly
// when it becomes durable.
func (f *fixture) writerTx(t *testing.T) *sql.Tx {
	t.Helper()
	db, err := sql.Open("sqlite", f.journal.Path())
	if err != nil {
		t.Fatalf("open writer handle: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin writer transaction: %v", err)
	}
	return tx
}

// stageAndDeclare is the shape every single-writer path takes: stage each
// record and close the closure inside one transaction, then publish after it
// commits.
func (f *fixture) stageAndDeclare(t *testing.T, runID string, recs ...Record) {
	t.Helper()
	tx := f.writerTx(t)
	defer tx.Rollback()
	for _, rec := range recs {
		if err := f.pub.StageTx(t.Context(), tx, rec); err != nil {
			t.Fatalf("stage %s: %v", rec.EntityID, err)
		}
	}
	if err := f.pub.DeclareTx(t.Context(), tx, Closure{RunID: runID}); err != nil {
		t.Fatalf("declare %s: %v", runID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writer transaction: %v", err)
	}
}

func record(runID, id string, kind sharedcatalog.RecordKind) Record {
	return Record{
		RunID:    runID,
		EntityID: id,
		Kind:     kind,
		Schema:   1,
		Payload:  []byte(fmt.Sprintf(`{"claim":%q,"id":%q}`, sentinel, id)),
	}
}

// journalState reads one record's local sync state, or the empty string.
func (f *fixture) journalState(t *testing.T, id string) string {
	t.Helper()
	state, err := f.journal.SyncState(t.Context(), id)
	if err != nil {
		t.Fatalf("read journal state for %s: %v", id, err)
	}
	return state
}

// payloadRows counts the staged payload copies the journal still holds. They
// are released when a record commits, so a committed corpus leaves none.
func (f *fixture) payloadRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.journal.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sync_payload`).Scan(&n); err != nil {
		t.Fatalf("count staged payloads: %v", err)
	}
	return n
}

// journalHoldsSentinel reports whether any staged payload still carries the
// marker, which is how the release is measured on the bytes rather than on the
// row count.
func (f *fixture) journalHoldsSentinel(t *testing.T) bool {
	t.Helper()
	rows, err := f.journal.db.QueryContext(t.Context(), `SELECT payload FROM sync_payload`)
	if err != nil {
		t.Fatalf("read staged payloads: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan staged payload: %v", err)
		}
		if bytesContains(payload, sentinel) {
			return true
		}
	}
	return false
}

// remoteRun reads the run row the catalog holds.
func (f *fixture) remoteRun(t *testing.T, runID string) sharedcatalog.AnalysisRunRow {
	t.Helper()
	run, err := sharedcatalog.AnalysisRun(t.Context(), f.catalog, runID)
	if err != nil {
		t.Fatalf("read remote run %s: %v", runID, err)
	}
	return run
}

func (f *fixture) remoteRecords(t *testing.T, runID string) []sharedcatalog.AnalysisRecordRow {
	t.Helper()
	rows, err := sharedcatalog.AnalysisRecords(t.Context(), f.catalog, runID)
	if err != nil {
		t.Fatalf("read remote records for %s: %v", runID, err)
	}
	return rows
}
