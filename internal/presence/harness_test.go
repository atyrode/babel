package presence_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// Half of this package is arithmetic over a duration and a state, and those
// tests need nothing. The other half is a table with a trigger on it, and what
// that turns on - server-authoritative time, a CHECK, a plpgsql guard refusing
// an UPDATE that touches the wrong column - is exactly what a fake would
// reproduce the happy path of and none of.
//
// So the cluster is provisioned when it can be and the suite runs either way:
// the pure tests always run, and the database tests skip with a reason rather
// than the whole binary exiting green. An environment that promised a server
// still fails, because a silently retired guard suite is the failure mode worth
// failing over.
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
			"no BABEL_TEST_POSTGRES and initdb is not on PATH: the presence table tests will skip")
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
	testHost       = "fixturehost"
	otherHost      = "fixtureotherhost"
)

var dbSeq atomic.Int64

// newCatalog hands each test its own migrated database.
//
// No deployment, host or instance row is registered, and that is deliberate
// rather than an omission: the presence table declares no foreign keys, because
// a machine that has analysed but never pushed an archive has no `hosts` row
// and must still be visible to the fleet. A harness that registered those rows
// would make every test pass whether or not that held.
func newCatalog(t *testing.T) *sql.DB {
	t.Helper()
	if baseDSN == "" {
		t.Skip("no PostgreSQL available: the presence table is not exercised")
	}
	ctx := t.Context()

	admin, err := sharedcatalog.Open(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("babel_presence_test_%d", dbSeq.Add(1))
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

// diagSink collects what the store diagnosed. Every failure this package
// swallows must land here, so a test that proves a write did not fail also has
// to prove the operator was told.
type diagSink struct{ errs []error }

func (d *diagSink) report(err error) { d.errs = append(d.errs, err) }

func (d *diagSink) joined() string {
	out := ""
	for _, err := range d.errs {
		out += err.Error() + "\n"
	}
	return out
}

// newStore builds a store for testHost over db.
func newStore(t *testing.T, db *sql.DB, sink *diagSink) *presence.Store {
	t.Helper()
	return newStoreFor(t, db, testHost, sink)
}

func newStoreFor(t *testing.T, db *sql.DB, host string, sink *diagSink) *presence.Store {
	t.Helper()
	opt := presence.Options{DB: db, DeploymentID: testDeployment, HostID: host}
	if sink != nil {
		opt.Diag = sink.report
	}
	store, err := presence.New(opt)
	if err != nil {
		t.Fatalf("presence.New: %v", err)
	}
	return store
}

// plant inserts a row directly, with the ages a classification test needs.
//
// It goes around the store on purpose. The trigger refuses a heartbeat that
// moves backwards - which is the property that makes a live row trustworthy -
// so there is no legitimate write path that produces an old heartbeat, and
// backdating one is only possible at INSERT, where nothing this package writes
// ever supplies a timestamp at all.
func plant(t *testing.T, db *sql.DB, id, host, deployment string,
	kind presence.Kind, state presence.State, ageSeconds float64) {
	t.Helper()
	finished := "NULL"
	if state != presence.StateRunning {
		finished = "now() - make_interval(secs => $6::double precision)"
	}
	stmt := `INSERT INTO presence (
	    presence_id, deployment_id, host_id, kind, run_id, state,
	    started_at, heartbeat_at, finished_at)
	VALUES ($1, $2, $3, $4, $5, $7,
	        now() - make_interval(secs => $6::double precision) - interval '1 minute',
	        now() - make_interval(secs => $6::double precision),
	        ` + finished + `)`
	if _, err := db.ExecContext(t.Context(), stmt,
		id, deployment, host, string(kind), "run-"+id, ageSeconds, string(state)); err != nil {
		t.Fatalf("plant %s: %v", id, err)
	}
}

// rowByID finds one row in a fleet read, so an assertion names what it is about
// rather than depending on the ordering it is not testing.
func rowByID(t *testing.T, rows []presence.Row, id presence.PresenceID) presence.Row {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no row %s in %d rows", id, len(rows))
	return presence.Row{}
}

// countRows is the no-reaper assertion's instrument: it reads the table
// directly, because Fleet deliberately hides rows past the retention window and
// a test of "nothing was deleted" must look past that.
func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM presence`).Scan(&n); err != nil {
		t.Fatalf("count presence rows: %v", err)
	}
	return n
}

// mustClosedDB is a handle every statement fails on, with no server anywhere
// near it. It is how the tests that are about refusing to touch the database
// prove they did not: reaching it produces an error rather than a connection
// attempt that might succeed on a developer's machine.
func mustClosedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://127.0.0.1:1/nothing")
	if err != nil {
		t.Fatalf("open a deliberately unusable handle: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close it: %v", err)
	}
	return db
}

// fakeAnnouncer is the announcer a wiring test uses: it records the
// choreography and cannot fail. It lives here rather than in each wiring
// package because the pure tests in this package need it too.
type fakeAnnouncer struct {
	announced  []presence.Announcement
	heartbeats int
	finalized  []presence.Outcome
}

func (f *fakeAnnouncer) Announce(_ context.Context, a presence.Announcement) (presence.PresenceID, error) {
	f.announced = append(f.announced, a)
	return presence.PresenceID(fmt.Sprintf("prs_fake_%d", len(f.announced))), nil
}

func (f *fakeAnnouncer) Heartbeat(context.Context, presence.PresenceID) error {
	f.heartbeats++
	return nil
}

func (f *fakeAnnouncer) Finalize(_ context.Context, _ presence.PresenceID, o presence.Outcome) error {
	f.finalized = append(f.finalized, o)
	return nil
}
