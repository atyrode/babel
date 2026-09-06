// Package durable opens Babel's SQLite files the one way every store must.
//
// Every durable store used to open its file with a bare path and a five-second
// busy timeout, and every transaction began deferred. In WAL mode a deferred
// transaction that has read and then writes is not made to wait when another
// connection committed in between: SQLite answers SQLITE_BUSY at once, without
// consulting the busy handler, because waiting could never make that
// transaction's snapshot current. With several explorations recording into one
// file at the same time, that was an observation the model produced and Babel
// refused to keep — "persist observation: database is locked" in the run's
// failures, and the record gone.
//
// So every transaction begins IMMEDIATE: the write lock is taken at BEGIN,
// before any read, and a writer arriving while another holds it waits for the
// busy timeout rather than failing on its first INSERT. The timeout is sized
// for a machine running many explorations at once, not for the brief overlap
// of one reader and one writer the five seconds were chosen for. A record is
// never dropped for want of patience; a lock held for a full minute is a fault
// worth reporting.
package durable

import (
	"database/sql"
	"net/url"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// BusyTimeout is how long a connection waits for another writer before an
// operation fails with SQLITE_BUSY.
const BusyTimeout = 60 * time.Second

// BusyPragma is the statement a store runs on its connection to apply
// BusyTimeout; DSN already applies it, and a store that also runs this keeps
// the two from disagreeing.
const BusyPragma = "PRAGMA busy_timeout=60000"

// DSN renders the connection string for one durable file: the path as a file
// URI, immediate transactions, and the busy timeout applied to every
// connection before any other statement.
func DSN(path string) string {
	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(int(BusyTimeout/time.Millisecond))+")")
	return "file:" + path + "?" + q.Encode()
}

// Open opens one durable SQLite file with DSN's settings and a single
// connection, which is §9's local state-writer lock invariant: one handle, one
// writer per process.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
