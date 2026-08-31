package reference

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// Most of this package's behaviour is local and needs no PostgreSQL: an edge is
// validated, written and read back against a SQLite file. Publication is the
// exception, and what it turns on - a commit ordering across two stores and the
// crash windows between them - is exactly what a fake would reproduce the happy
// path of and none of.
//
// So the cluster is provisioned when it can be and the suite runs either way:
// the local tests always run, and the publication tests skip with a reason
// rather than the whole binary exiting green. An environment that promised a
// server still fails, because a silently retired publication suite is the
// failure mode worth failing over.
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
		fmt.Fprintln(os.Stderr,
			"no BABEL_TEST_POSTGRES and initdb is not on PATH: publication tests will skip")
		os.Exit(m.Run())
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

const (
	testDeployment = "fixturedeployment"
	testInstance   = "fixtureinstance"
	testHost       = "fixturehost"
)

// sentinel marks every note these tests write. It must never appear in a
// PostgreSQL column and must be gone from the local journal once an edge has
// committed - the two properties the split between an edge's shape and its note
// exists to make checkable.
const sentinel = "BABEL-SYNTHETIC-EDGE-NOTE-do-not-leak"

var dbSeq atomic.Int64

// newCatalog hands each test its own migrated database with the deployment,
// host and instance rows a Phase B run's foreign keys require - the rows
// Register writes on a real machine's first push.
func newCatalog(t *testing.T) *sql.DB {
	t.Helper()
	if baseDSN == "" {
		t.Skip("no PostgreSQL available: publication is not exercised")
	}
	ctx := t.Context()

	admin, err := sharedcatalog.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("babel_reference_test_%d", dbSeq.Add(1))
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

// memStore is the injected object store. Failures are programmable per key so
// the protocol's remote failure points can be exercised separately.
type memStore struct {
	mu      stdsync.Mutex
	objects map[string][]byte
	failPut func(key string) error
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			return err
		}
	}
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return append([]byte(nil), stored...), nil
}

func (s *memStore) refuseEverything(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPut = func(string) error { return err }
}

func (s *memStore) acceptEverything() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPut = nil
}

func (s *memStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// stubResolver is one namespace's record set. It stands in for a durable store
// on the terms internal/reference actually depends on: a set of ids it can
// vouch for, and the ability to fail.
type stubResolver struct {
	ids map[string]bool
	err error
}

func (r *stubResolver) Exists(ctx context.Context, id string) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	return r.ids[id], nil
}

func (r *stubResolver) add(ids ...string) {
	if r.ids == nil {
		r.ids = map[string]bool{}
	}
	for _, id := range ids {
		r.ids[id] = true
	}
}

// deferredCommit is the crash. It stages exactly as the real hook does and then
// does nothing at all when the write path asks it to publish, which is what a
// process that dies between its durable commit and its publication attempt
// looks like from the journal's side.
type deferredCommit struct {
	hook babelsync.Hook
	// deferred counts the publications that were skipped, so a test can prove
	// the crash it is simulating actually happened.
	deferred int
}

func (d *deferredCommit) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec babelsync.Record) (babelsync.Closure, bool, error) {
	return d.hook.Append(ctx, tx, producedBy, rec)
}

func (d *deferredCommit) StageTx(ctx context.Context, tx *sql.Tx, rec babelsync.Record) error {
	return d.hook.StageTx(ctx, tx, rec)
}

func (d *deferredCommit) DeclareTx(ctx context.Context, tx *sql.Tx, c babelsync.Closure) error {
	return d.hook.DeclareTx(ctx, tx, c)
}

func (d *deferredCommit) CommitInline(ctx context.Context, c babelsync.Closure) error {
	d.deferred++
	return nil
}

// fixture is one edge store wired to a journal, a publisher, a throwaway
// catalog and an in-memory object store, with resolvers that vouch for a
// handful of synthetic records.
type fixture struct {
	store    *Store
	journal  *babelsync.Journal
	pub      *babelsync.Publisher
	catalog  *sql.DB
	objects  *memStore
	ring     *envelope.Keyring
	dir      string
	failures []error

	sessions  *stubResolver
	findings  *stubResolver
	deferring *deferredCommit

	clock atomic.Int64
}

// newLocalFixture is an edge store with no publication at all: local-only mode,
// which is the default deployment and the one most of these tests need.
func newLocalFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), sessions: &stubResolver{}, findings: &stubResolver{}}
	f.sessions.add(testSessionKey)
	f.findings.add(testFindingID, otherFindingID)
	store, err := Open(f.dir, WithResolvers(f.registry(t)), WithClock(f.now))
	if err != nil {
		t.Fatalf("open edge store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	f.store = store
	return f
}

// newPublishingFixture is the same store with the real publication hook, plus
// the journal and catalog behind it. It skips when no PostgreSQL is available.
func newPublishingFixture(t *testing.T, deferCommits bool) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), sessions: &stubResolver{}, findings: &stubResolver{}}
	f.sessions.add(testSessionKey)
	f.findings.add(testFindingID, otherFindingID)
	f.catalog = newCatalog(t)
	f.objects = newMemStore()

	journal, err := babelsync.OpenJournal(f.dir)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { journal.Close() })
	f.journal = journal

	key, err := envelope.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := envelope.RingFrom("phase-b-1", map[envelope.KeyID][]byte{"phase-b-1": key})
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	f.ring = ring

	pub, err := babelsync.New(babelsync.Options{
		Config: config.Config{
			Mode:         config.ModeShared,
			DeploymentID: testDeployment,
			InstanceID:   testInstance,
		},
		Journal: journal,
		Catalog: f.catalog,
		Store:   f.objects,
		Keyring: ring,
		Diag:    func(err error) { f.failures = append(f.failures, err) },
	})
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	f.pub = pub

	var hook babelsync.Hook = pub
	if deferCommits {
		f.deferring = &deferredCommit{hook: pub}
		hook = f.deferring
	}
	store, err := Open(f.dir, WithResolvers(f.registry(t)), WithSync(hook), WithClock(f.now))
	if err != nil {
		t.Fatalf("open edge store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	f.store = store
	return f
}

// registry vouches for the synthetic records these tests cite. Two namespaces
// are enough to exercise the gate: one that holds records and one that does
// not appear at all.
func (f *fixture) registry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	for kind, resolver := range map[string]Resolver{
		"session": f.sessions,
		"finding": f.findings,
	} {
		if err := registry.Register(kind, resolver); err != nil {
			t.Fatalf("register %s resolver: %v", kind, err)
		}
	}
	return registry
}

// now is a monotonic fake clock. Edges are ordered newest-first by timestamp,
// and a real clock makes two edges written in one microsecond order by luck.
func (f *fixture) now() time.Time {
	return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC).
		Add(time.Duration(f.clock.Add(1)) * time.Millisecond)
}

const (
	testSessionKey = "1f0e2d3c4b5a69788796a5b4c3d2e1f01f0e2d3c4b5a69788796a5b4c3d2e1f0"
	testFindingID  = "fnd_0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	otherFindingID = "fnd_112233445566778899aabbccddeeff00"
)

// evidence is the edge these tests write most: a finding resting on a session.
func evidence(note string) Edge {
	return Edge{
		Kind:      KindEvidence,
		From:      RecordRef{Kind: "finding", ID: testFindingID},
		To:        RecordRef{Kind: "session", ID: testSessionKey},
		ActorKind: ActorRun,
		ActorRef:  "run-fixture",
		Note:      note,
	}
}

// operatorAsserted is the same citation with a person behind it, which is the
// edge that is complete the moment it is written: it is its own closure of one,
// declared inside the writer's transaction, so it publishes immediately instead
// of waiting for a run to end.
func operatorAsserted(note string) Edge {
	edge := evidence(note)
	edge.ActorKind, edge.ActorRef = ActorOperator, "alex"
	return edge
}

// endRun is what internal/explore does when an exploration finishes: declare
// the run's closure and publish it. Nothing else can do it, because nothing
// else knows the run has stopped producing records.
func (f *fixture) endRun(t *testing.T, runID string) {
	t.Helper()
	if err := f.pub.CommitInline(t.Context(), babelsync.Closure{RunID: runID}); err != nil {
		t.Fatalf("end run %s: %v", runID, err)
	}
}

// edgeRows counts the durable edge rows, which is how "no duplicates" is
// measured on the file rather than on a return value.
func (f *fixture) edgeRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM reference_edge`).Scan(&n); err != nil {
		t.Fatalf("count durable edges: %v", err)
	}
	return n
}

// remoteEdges reads the plaintext citation rows the shared catalog holds.
func (f *fixture) remoteEdges(t *testing.T) []sharedcatalog.FleetEdge {
	t.Helper()
	edges, err := sharedcatalog.RecordEdges(t.Context(), f.catalog, sharedcatalog.EdgeFilter{
		DeploymentID:   testDeployment,
		IncludePending: true,
	})
	if err != nil {
		t.Fatalf("read remote edges: %v", err)
	}
	return edges
}

// remoteRecordCount counts the Phase B record rows, so a duplicate published
// edge is visible even if the citation row deduplicated.
func (f *fixture) remoteRecordCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.catalog.QueryRowContext(t.Context(),
		`SELECT count(*) FROM analysis_records`).Scan(&n); err != nil {
		t.Fatalf("count remote records: %v", err)
	}
	return n
}

// stagedEdgeRows counts the journal's staged endpoint rows. They are released
// when a record commits, so a fully published corpus leaves none.
func (f *fixture) stagedEdgeRows(t *testing.T) int {
	t.Helper()
	db, err := sql.Open("sqlite", f.journal.Path())
	if err != nil {
		t.Fatalf("open journal handle: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sync_record_edge`).Scan(&n); err != nil {
		t.Fatalf("count staged edges: %v", err)
	}
	return n
}

// syncState reports one edge's local publication state.
func (f *fixture) syncState(t *testing.T, id string) string {
	t.Helper()
	state, err := f.journal.SyncState(t.Context(), id)
	if err != nil {
		t.Fatalf("read sync state for %s: %v", id, err)
	}
	return state
}

// retry is `babel sync`: publish everything the journal still owes.
func (f *fixture) retry(t *testing.T) babelsync.Report {
	t.Helper()
	rep, err := f.pub.Retry(t.Context())
	if err != nil {
		t.Fatalf("sync retry: %v", err)
	}
	for _, failure := range rep.Failures {
		if errors.Is(failure.Err, context.Canceled) {
			t.Fatalf("sync retry cancelled: %v", failure.Err)
		}
	}
	return rep
}

// scanForText reads every character-typed column of every table in the shared
// schema and reports where needle appears.
//
// It is internal/sharedcatalog's own leak scan, restated here because this
// package's claim is narrower and needs checking from this side: an edge's note
// must not reach PostgreSQL through the one Phase B writer that deliberately
// puts columns of its record in the clear. Reflecting the live schema rather
// than naming columns is what makes it survive a migration nobody told it
// about.
func scanForText(t *testing.T, db *sql.DB, needle string) []string {
	t.Helper()
	ctx := t.Context()
	cols, err := db.QueryContext(ctx, `
		SELECT table_name, column_name
		  FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND data_type IN ('text', 'character varying', 'character', 'json', 'jsonb', 'bytea')
		 ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("list text columns: %v", err)
	}
	type ref struct{ table, column string }
	var refs []ref
	for cols.Next() {
		var r ref
		if err := cols.Scan(&r.table, &r.column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		refs = append(refs, r)
	}
	cols.Close()
	if err := cols.Err(); err != nil {
		t.Fatalf("list text columns: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("no text columns found; this scan would pass vacuously")
	}

	var hits []string
	for _, r := range refs {
		// Identifiers come from information_schema, not from a caller, and are
		// quoted by PostgreSQL's own format().
		var stmt string
		if err := db.QueryRowContext(ctx,
			`SELECT format('SELECT count(*) FROM %I WHERE %I::text LIKE $1', $1::text, $2::text)`,
			r.table, r.column).Scan(&stmt); err != nil {
			t.Fatalf("render scan statement: %v", err)
		}
		var n int
		if err := db.QueryRowContext(ctx, stmt, "%"+needle+"%").Scan(&n); err != nil {
			t.Fatalf("scan %s.%s: %v", r.table, r.column, err)
		}
		if n > 0 {
			hits = append(hits, fmt.Sprintf("%s.%s (%d rows)", r.table, r.column, n))
		}
	}
	return hits
}

func contains(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}
