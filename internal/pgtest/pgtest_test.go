package pgtest_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/atyrode/babel/internal/pgtest"
)

// The harness is infrastructure two suites depend on, and its TLS mode is the
// part that can silently degrade: a cluster started without ssl=on still
// accepts `sslmode=require` connections in some configurations, which would let
// a CLI-level test pass while proving nothing about encryption. So this asserts
// the server's own view of the connection rather than the client's request.
func TestClusterServesTLSWhenAsked(t *testing.T) {
	if !pgtest.Available() {
		t.Skip("initdb/pg_ctl not on PATH")
	}
	c, err := pgtest.Start(pgtest.Options{TLS: true})
	if err != nil {
		t.Fatalf("start TLS cluster: %v", err)
	}
	t.Cleanup(c.Stop)

	db := open(t, c.BaseDSN)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var active bool
	var version string
	err = db.QueryRowContext(ctx,
		`SELECT ssl, coalesce(version, '') FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).
		Scan(&active, &version)
	if err != nil {
		t.Fatalf("read pg_stat_ssl: %v\nserver log:\n%s", err, c.Log())
	}
	if !active {
		t.Fatalf("cluster started with ssl=on served a plaintext connection\nserver log:\n%s", c.Log())
	}
	if version == "" {
		t.Fatal("TLS reported active with no protocol version")
	}
}

// The plaintext mode exists for suites that open connections directly, and must
// stay plaintext: if it silently required TLS, the sharedcatalog suite would
// pass for the wrong reason on a machine whose defaults differ.
func TestClusterIsPlaintextByDefault(t *testing.T) {
	if !pgtest.Available() {
		t.Skip("initdb/pg_ctl not on PATH")
	}
	c, err := pgtest.Start(pgtest.Options{})
	if err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	t.Cleanup(c.Stop)

	db := open(t, c.BaseDSN)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var active bool
	if err := db.QueryRowContext(ctx,
		`SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&active); err != nil {
		t.Fatalf("read pg_stat_ssl: %v\nserver log:\n%s", err, c.Log())
	}
	if active {
		t.Fatal("default cluster negotiated TLS")
	}
}

// Two clusters must be able to run at once: `go test ./...` provisions one per
// package, and a port derived from anything but the kernel collides.
func TestTwoClustersCoexist(t *testing.T) {
	if !pgtest.Available() {
		t.Skip("initdb/pg_ctl not on PATH")
	}
	first, err := pgtest.Start(pgtest.Options{})
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	t.Cleanup(first.Stop)
	second, err := pgtest.Start(pgtest.Options{TLS: true})
	if err != nil {
		t.Fatalf("start second: %v", err)
	}
	t.Cleanup(second.Stop)

	if first.Port == second.Port {
		t.Fatalf("both clusters bound port %d", first.Port)
	}
	for _, c := range []*pgtest.Cluster{first, second} {
		db := open(t, c.BaseDSN)
		var one int
		if err := db.QueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("cluster on port %d unusable: %v", c.Port, err)
		}
	}
}

func open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
