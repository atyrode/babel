package durable

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestWriterWaitsBehindAnotherWriter is the shape of the loss this package
// exists to close: two processes recording into one durable file, the second
// reading before it writes while the first holds the write lock. With a
// deferred BEGIN the second's INSERT fails with SQLITE_BUSY the instant the
// first commits — its snapshot is stale and no busy handler is consulted —
// and the record it carried is gone. Opened through this package, the second
// waits at BEGIN and then writes.
func TestWriterWaitsBehindAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable.db")
	setup, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE records(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	ctx := context.Background()
	holding, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holding.ExecContext(ctx, `INSERT INTO records(body) VALUES('first')`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		holding.Commit()
	}()

	// The second writer's transaction is the store's exact pattern: read,
	// then write, in one transaction.
	started := time.Now()
	tx, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("second BEGIN while the first held the lock: %v", err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO records(body) VALUES('second')`); err != nil {
		t.Fatalf("second INSERT lost after waiting %s: %v", time.Since(started), err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the second writer read %d rows inside its transaction, want the first's 1: it began before the first committed", n)
	}
	var total int
	if err := second.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("records = %d, want both writers' rows", total)
	}
}

// TestDeferredTransactionLosesTheWrite documents the failure the package
// closes, against a plain handle: the same sequence, and the second INSERT
// fails with SQLITE_BUSY. If SQLite ever changes this, the package's reason
// changes with it, and this test says so.
func TestDeferredTransactionLosesTheWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable.db")
	setup, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE records(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	plain := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
			t.Fatal(err)
		}
		return db
	}
	first, second := plain(), plain()
	defer first.Close()
	defer second.Close()

	ctx := context.Background()
	holding, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holding.ExecContext(ctx, `INSERT INTO records(body) VALUES('first')`); err != nil {
		t.Fatal(err)
	}
	tx, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := holding.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO records(body) VALUES('second')`)
	tx.Rollback()
	if err == nil {
		t.Fatal("a deferred transaction that read before another writer committed wrote anyway; the immediate mode is no longer needed for this case")
	}
}
