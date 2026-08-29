package sharedcatalog_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/jackc/pgx/v5/pgconn"
)

// Registration is the precondition for publishing, not bookkeeping: a lease
// cannot be taken by an instance the catalog does not know.
func TestRegisterMakesAnInstanceAbleToTakeALease(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	if _, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute); !errors.Is(err, sharedcatalog.ErrUnknownInstance) {
		t.Fatalf("before registration: err = %v, want ErrUnknownInstance", err)
	}

	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute); err != nil {
		t.Fatalf("after registration: acquire failed: %v", err)
	}
}

// Every push registers, so the second call must be a refresh rather than a
// conflict - and it must move last_seen_at, which is how a fleet view tells a
// live machine from an abandoned one.
func TestRegisterIsIdempotentAndRefreshesLastSeen(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	var first time.Time
	if err := db.QueryRow(`SELECT last_seen_at FROM instances WHERE instance_id = 'inst-a'`).Scan(&first); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}

	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	var second time.Time
	if err := db.QueryRow(`SELECT last_seen_at FROM instances WHERE instance_id = 'inst-a'`).Scan(&second); err != nil {
		t.Fatalf("read last_seen_at again: %v", err)
	}
	if !second.After(first) {
		t.Errorf("last_seen_at did not advance: %v then %v", first, second)
	}

	var deployments, hosts, instances int
	if err := db.QueryRow(`SELECT
		(SELECT count(*) FROM deployments),
		(SELECT count(*) FROM hosts),
		(SELECT count(*) FROM instances)`).Scan(&deployments, &hosts, &instances); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if deployments != 1 || hosts != 1 || instances != 1 {
		t.Errorf("registering twice duplicated rows: %d deployments, %d hosts, %d instances",
			deployments, hosts, instances)
	}
}

// An older binary must not rewind the version a newer one recorded, so the
// deployment row is written once and then left alone.
func TestRegisterDoesNotRewriteARecordedSchemaVersion(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', $1)`,
		sharedcatalog.SchemaVersion+5); err != nil {
		t.Fatalf("seed a newer deployment: %v", err)
	}
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var version int
	if err := db.QueryRow(`SELECT schema_version FROM deployments WHERE deployment_id = 'd1'`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != sharedcatalog.SchemaVersion+5 {
		t.Errorf("schema_version = %d, want the recorded %d left untouched",
			version, sharedcatalog.SchemaVersion+5)
	}
}

func TestRegisterRequiresEveryIdentity(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	for _, tc := range []struct{ deployment, host, instance string }{
		{"", "h1", "inst-a"},
		{"d1", "", "inst-a"},
		{"d1", "h1", ""},
	} {
		if err := sharedcatalog.Register(context.Background(), db, tc.deployment, tc.host, tc.instance); err == nil {
			t.Errorf("Register(%q, %q, %q) succeeded; every id is required",
				tc.deployment, tc.host, tc.instance)
		}
	}
}

// publication_order comes from the catalog, so a host that lost its local state
// continues its own history instead of restarting at 1 and appearing older than
// snapshots it already published.
func TestNextPublicationOrderContinuesTheCatalogsHistory(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	first, err := sharedcatalog.NextPublicationOrder(ctx, db, "h1")
	if err != nil {
		t.Fatalf("NextPublicationOrder: %v", err)
	}
	if first != 1 {
		t.Fatalf("first order = %d, want 1", first)
	}

	l, err := sharedcatalog.AcquireHostLease(ctx, db, "h1", "inst-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	applied, err := sharedcatalog.PublishSnapshot(ctx, db, l, "snapshot:aaa",
		sharedcatalog.SnapshotRow{
			SnapshotID:       "aaa",
			PublicationOrder: first,
			SnapshotTime:     time.Now().UTC(),
			CommitState:      sharedcatalog.CommitCommitted,
			PublishedBy:      "inst-a",
		}, nil)
	if err != nil || !applied {
		t.Fatalf("publish: applied = %v, err = %v", applied, err)
	}

	next, err := sharedcatalog.NextPublicationOrder(ctx, db, "h1")
	if err != nil {
		t.Fatalf("NextPublicationOrder after publishing: %v", err)
	}
	if next != first+1 {
		t.Errorf("next order = %d, want %d", next, first+1)
	}

	// Another host's history is its own.
	if err := sharedcatalog.Register(ctx, db, "d1", "h2", "inst-b"); err != nil {
		t.Fatalf("Register h2: %v", err)
	}
	other, err := sharedcatalog.NextPublicationOrder(ctx, db, "h2")
	if err != nil {
		t.Fatalf("NextPublicationOrder for h2: %v", err)
	}
	if other != 1 {
		t.Errorf("h2 first order = %d, want 1: publication_order is per host", other)
	}
}

// The classification decides whether a failed push degrades to catalog-pending
// or fails loudly, so a wrong answer either hides a misconfiguration or breaks
// an offline machine's backup. Reconciliation can repair an outage; it cannot
// repair a rejected password.
func TestUnreachableSeparatesOutagesFromRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "catalog.invalid", IsNotFound: true}, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"os deadline", os.ErrDeadlineExceeded, true},
		{"closed connection", net.ErrClosed, true},
		{"wrapped dial failure", fmt.Errorf("reach shared catalog: %w",
			&net.OpError{Op: "dial", Err: errors.New("network is unreachable")}), true},

		// The server answered. However the driver wraps it, that is a refusal.
		{"invalid password", &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, false},
		{"insufficient privilege", &pgconn.PgError{Code: "42501", Message: "permission denied"}, false},
		{"undefined table", &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}, false},
		{"unique violation", &pgconn.PgError{Code: "23505", Message: "duplicate key"}, false},
		{"password rejected inside a connect failure", fmt.Errorf("failed to connect: %w",
			&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}), false},

		// Babel's own refusals are decisions, not outages.
		{"schema too new", sharedcatalog.ErrSchemaTooNew, false},
		{"lease held", sharedcatalog.ErrLeaseHeld, false},
		{"lease lost", sharedcatalog.ErrLeaseLost, false},
		{"unknown instance", sharedcatalog.ErrUnknownInstance, false},
		{"plain error", errors.New("something went wrong"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sharedcatalog.Unreachable(tc.err); got != tc.want {
				t.Errorf("Unreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The rule is stated as "did PostgreSQL answer", so prove it against a real
// server rather than only against constructed errors: a closed port must read
// as unreachable, and a real authentication failure must not.
func TestUnreachableAgainstARealServer(t *testing.T) {
	base := sharedcatalog.TestingBaseDSN()
	if base == "" {
		t.Skip("no harness DSN")
	}
	ctx := context.Background()

	// Port 1 on loopback: reachable host, nothing listening.
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse harness DSN: %v", err)
	}
	u.Host = net.JoinHostPort(u.Hostname(), "1")
	if _, err := sharedcatalog.Open(ctx, u.String()); err == nil {
		t.Error("connecting to a closed port succeeded")
	} else if !sharedcatalog.Unreachable(err) {
		t.Errorf("a closed port must read as unreachable, got: %v", err)
	}

	// The same server, reached, rejecting a password. Not an outage.
	u, err = url.Parse(base)
	if err != nil {
		t.Fatalf("parse harness DSN: %v", err)
	}
	u.User = url.UserPassword("babel_no_such_role_"+t.Name(), "wrong-password")
	if _, err := sharedcatalog.Open(ctx, u.String()); err == nil {
		t.Skip("this server accepts any credential, so it cannot refuse one")
	} else if sharedcatalog.Unreachable(err) {
		t.Errorf("a server that answered and refused must not read as unreachable, got: %v", err)
	}
}
