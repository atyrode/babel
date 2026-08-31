// Package index is Babel's local retrieval index: the provenance-preserving
// full-text search, structured filters, and temporal filters SPEC.md §5.4
// requires of v1 retrieval, and deliberately nothing more. §5.4 defers
// semantic/vector retrieval until its privacy, cost, diversity, and
// contradiction behavior have been evaluated, so this package has no
// embedding, no similarity, and no notion of a "closest" record.
//
// Three design decisions carry the package, and each answers a measured
// property of the corpus rather than a preference.
//
// The indexing unit is one source record. The median record in the operator
// corpus is about a kilobyte and the 99th percentile is a few tens of
// kilobytes, so a record already fits any analysis context window and
// splitting one into chunks would buy nothing while inventing chunk
// boundaries that no locator addresses. A record is also exactly what
// event.Locator recovers, so indexing per record keeps every hit's evidence
// addressable by construction: a hit names the file, line, byte offset, and
// record digest that produce its bytes again.
//
// The index is a rebuildable cache and therefore lives in its own database
// file. Nothing here is durable analysis state: every row is derived from
// the corpus by streaming event.Scan, so a corrupt, incompatible, or deleted
// index costs a re-index and never loses data. That is why Open may discard
// the file and rebuild it rather than failing, and why the frontier,
// receipts, and preparation records — which are durable and pending remote
// sync — must not share this file.
//
// Cost is per byte, not per file. The largest single sessions hold a
// disproportionate share of the corpus, so a session is indexed by streaming
// it: one event at a time from event.Scan into a bounded insert batch inside
// one transaction per session. A third-of-a-gigabyte session indexes in
// memory proportional to the batch, not to the file.
//
// One rule constrains the whole query surface: retrieval rank is not
// evidence strength (§5.4). Search may order results, and Order says how it
// ordered them, but no Hit carries an FTS5 score or any other
// confidence-shaped number, so nothing downstream can mistake "found first"
// for "better supported".
package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schemaVersion is bumped whenever the indexed columns, the tokenizer, or
// the classification the rows hold changes. A mismatch makes Open discard
// and rebuild the file, which is safe precisely because the index is
// derived: the alternative — migrating a cache — would be work spent to
// preserve rows that a re-index reproduces exactly.
// Version 2 adds the frontier surface: full-text search over Babel's own
// output beside the search over the corpus (#87 item 4). A version 1 file is
// discarded and rebuilt rather than altered, which costs one re-index of
// material that is derived from the corpus and the durable store.
const schemaVersion = "2"

// FileName is the index database's name inside Babel's private local state
// directory. It is a separate file from the durable local state on purpose
// (see the package doc), and callers that place it themselves need the name.
const FileName = "index.db"

// Index is a retrieval index database.
type Index struct {
	db   *sql.DB
	path string
}

// Open opens <dir>/index.db, creating the directory and the schema when
// needed. A corrupt or incompatible index is removed and rebuilt once; only
// a failure of that clean rebuild is returned. Rebuilding is not data loss:
// the caller re-indexes the corpus, which is the same work that produced the
// discarded rows.
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create index directory: %w", err)
	}
	path := filepath.Join(dir, FileName)
	idx, err := open(path)
	if err == nil {
		return idx, nil
	}
	removeDatabase(path)
	idx, err = open(path)
	if err != nil {
		return nil, fmt.Errorf("open index after rebuild: %w", err)
	}
	return idx, nil
}

func open(path string) (*Index, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	idx := &Index{db: db, path: path}
	if err := idx.init(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

// Close releases the database handle.
func (x *Index) Close() error { return x.db.Close() }

// Path reports the index database path, which operators need in order to
// delete the cache and diagnostics need in order to report its size.
func (x *Index) Path() string { return x.path }

// schema is the whole index. Two properties are worth reading twice.
//
// events_fts is an external-content FTS5 table over events.text: the text is
// stored once in events and the virtual table holds only the inverted index,
// which is what keeps a full-corpus index a fraction of the corpus rather
// than a copy of it plus an index. The two triggers are the documented way
// to keep an external-content index consistent, and there are only two
// because an event row is only ever inserted or deleted — a changed session
// is deleted whole and re-inserted, never updated in place.
//
// The indexes cover the filters where SQLite can actually use one. Session,
// time, tool, and repository path are selective enough to pay for their
// bytes; kind, role, outcome, and partial have a handful of distinct values
// each, so scanning them after a selective filter costs less than the index
// pages would in a cache whose size is a product concern.
const schema = `
CREATE TABLE IF NOT EXISTS sessions(
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	harness TEXT NOT NULL,
	adapter_schema INTEGER NOT NULL,
	source_id TEXT NOT NULL,
	size INTEGER NOT NULL,
	mtime_unixnano INTEGER NOT NULL,
	records INTEGER NOT NULL,
	events INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events(
	id INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL,
	event_index INTEGER NOT NULL,
	kind TEXT NOT NULL,
	role TEXT NOT NULL,
	tool TEXT NOT NULL,
	outcome TEXT NOT NULL,
	event_time INTEGER,
	partial INTEGER NOT NULL,
	line INTEGER NOT NULL,
	byte_offset INTEGER NOT NULL,
	digest TEXT NOT NULL,
	text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_by_session ON events(session_id, event_index);
CREATE INDEX IF NOT EXISTS events_by_time ON events(event_time);
CREATE INDEX IF NOT EXISTS events_by_tool ON events(tool, id) WHERE tool <> '';
CREATE TABLE IF NOT EXISTS event_paths(
	event_id INTEGER NOT NULL,
	path TEXT NOT NULL,
	PRIMARY KEY(event_id, path)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS event_paths_by_path ON event_paths(path, event_id);
CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
	text,
	content='events',
	content_rowid='id',
	tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS events_fts_insert AFTER INSERT ON events BEGIN
	INSERT INTO events_fts(rowid, text) VALUES(new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS events_fts_delete AFTER DELETE ON events BEGIN
	INSERT INTO events_fts(events_fts, rowid, text) VALUES('delete', old.id, old.text);
END;
CREATE TABLE IF NOT EXISTS frontier_records(
	id           INTEGER PRIMARY KEY,
	record_id    TEXT NOT NULL UNIQUE,
	kind         TEXT NOT NULL,
	root_id      TEXT NOT NULL,
	subject_kind TEXT NOT NULL,
	subject_id   TEXT NOT NULL,
	run_id       TEXT NOT NULL,
	status       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	fingerprint  TEXT NOT NULL,
	summary      TEXT NOT NULL,
	text         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS frontier_records_by_kind ON frontier_records(kind, created_at);
CREATE INDEX IF NOT EXISTS frontier_records_by_root ON frontier_records(root_id);
CREATE VIRTUAL TABLE IF NOT EXISTS frontier_fts USING fts5(
	text,
	content='frontier_records',
	content_rowid='id',
	tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS frontier_fts_insert AFTER INSERT ON frontier_records BEGIN
	INSERT INTO frontier_fts(rowid, text) VALUES(new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS frontier_fts_delete AFTER DELETE ON frontier_records BEGIN
	INSERT INTO frontier_fts(frontier_fts, rowid, text) VALUES('delete', old.id, old.text);
END;
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);`

func (x *Index) init() error {
	var integrity string
	if err := x.db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("check index integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("check index integrity: %s", integrity)
	}

	// WAL lets the web process answer a search against the sessions an
	// in-flight indexing run has already committed, instead of blocking on
	// the writer; busy_timeout absorbs the overlap when a session commits
	// while a search is starting. journal_mode persists in the file, the
	// timeout is per connection.
	var journal string
	if err := x.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enable index WAL: %w", err)
	}
	if _, err := x.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("set index busy timeout: %w", err)
	}

	if _, err := x.db.Exec(schema); err != nil {
		return fmt.Errorf("initialize index schema: %w", err)
	}

	var version string
	err := x.db.QueryRow(`SELECT v FROM meta WHERE k = 'schema_version'`).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := x.db.Exec(`INSERT INTO meta(k, v) VALUES('schema_version', ?)`, schemaVersion); err != nil {
			return fmt.Errorf("record index schema: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read index schema: %w", err)
	case version != schemaVersion:
		return fmt.Errorf("unsupported index schema %q", version)
	}

	// Naming every expected column turns a partially created or drifted
	// table into a schema error, which Open handles with the same clean
	// rebuild a version mismatch gets.
	rows, err := x.db.Query(`SELECT e.id, e.session_id, e.event_index, e.kind, e.role,
		e.tool, e.outcome, e.event_time, e.partial, e.line, e.byte_offset,
		e.digest, e.text, s.path, s.harness, s.adapter_schema, s.source_id, s.size,
		s.mtime_unixnano, s.records, s.events
		FROM events e JOIN sessions s ON s.id = e.session_id LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate index schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// The frontier surface is validated the same way and separately: it has
	// no join to the corpus tables, and a query spanning both would report a
	// drifted frontier row as a drifted event row.
	rows, err = x.db.Query(`SELECT f.id, f.record_id, f.kind, f.root_id, f.subject_kind,
		f.subject_id, f.run_id, f.status, f.created_at, f.fingerprint, f.summary, f.text
		FROM frontier_records f LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate frontier index schema: %w", err)
	}
	return rows.Close()
}

func removeDatabase(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-shm")
	_ = os.Remove(path + "-wal")
}

// Stats describes what the index currently holds. Bytes is the database file
// as SQLite accounts for it, which is the number that matters when the
// question is what fraction of the corpus the cache costs.
type Stats struct {
	Sessions int
	Events   int
	Bytes    int64
}

// Stats reports the index's contents and size. It exists because the size a
// retrieval cache costs relative to the corpus it indexes is an operational
// fact, not a detail: the corpus is measured in gigabytes and the cache
// lives in the operator's private state directory.
func (x *Index) Stats() (Stats, error) {
	var s Stats
	if err := x.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&s.Sessions); err != nil {
		return Stats{}, fmt.Errorf("count indexed sessions: %w", err)
	}
	if err := x.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&s.Events); err != nil {
		return Stats{}, fmt.Errorf("count indexed events: %w", err)
	}
	var pages, pageSize int64
	if err := x.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return Stats{}, fmt.Errorf("read index page count: %w", err)
	}
	if err := x.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return Stats{}, fmt.Errorf("read index page size: %w", err)
	}
	s.Bytes = pages * pageSize
	return s, nil
}
