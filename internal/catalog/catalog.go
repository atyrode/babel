// Package catalog maintains the rebuildable local session-list cache.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "modernc.org/sqlite"
)

const schemaVersion = "1"

// Ref is the inexpensive identity returned by adapter discovery. Refresh stats
// PrimaryPath to decide whether the cached description is still current.
type Ref struct {
	Selector    string
	Harness     string
	SourceID    string
	PrimaryPath string
}

// Row is one cached session-list row. RowJSON is retained byte-for-byte so the
// caller's complete presentation shape can round-trip without this package
// depending on it.
type Row struct {
	Selector             string
	Harness              string
	SourceID             string
	PrimaryPath          string
	PrimarySize          int64
	PrimaryMtimeUnixNano int64
	Title                *string
	Workspace            *string
	CreatedAt            *string
	ModifiedAt           *string
	ContinuationGrade    bool
	RowJSON              []byte
}

// Cache is a catalog database rooted in Babel's private XDG data directory.
type Cache struct {
	db   *sql.DB
	path string
}

// Open opens <dir>/catalog.db. A corrupt or incompatible database is removed
// and rebuilt once; only a failure of that clean rebuild is returned.
func Open(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	path := filepath.Join(dir, "catalog.db")
	cache, err := open(path)
	if err == nil {
		return cache, nil
	}
	removeDatabase(path)
	cache, err = open(path)
	if err != nil {
		return nil, fmt.Errorf("open catalog after rebuild: %w", err)
	}
	return cache, nil
}

// Count reports how many sessions the catalog at dir currently caches. A
// missing database is zero sessions, not an error: callers use Count for
// fast offline overviews that must never fail a healthy install.
func Count(dir string) (int, error) {
	path := filepath.Join(dir, "catalog.db")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cache, err := open(path)
	if err != nil {
		return 0, err
	}
	defer cache.Close()
	var n int
	if err := cache.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func open(path string) (*Cache, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	cache := &Cache{db: db, path: path}
	if err := cache.init(); err != nil {
		db.Close()
		return nil, err
	}
	return cache, nil
}

func (c *Cache) init() error {
	var integrity string
	if err := c.db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("check catalog integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("check catalog integrity: %s", integrity)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS sessions(
	selector TEXT PRIMARY KEY,
	harness TEXT,
	source_id TEXT,
	primary_path TEXT,
	primary_size INTEGER,
	primary_mtime_unixnano INTEGER,
	title TEXT,
	workspace TEXT,
	created_at TEXT,
	modified_at TEXT,
	continuation_grade INTEGER,
	row_json TEXT
);
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);`
	if _, err := c.db.Exec(schema); err != nil {
		return fmt.Errorf("initialize catalog schema: %w", err)
	}

	var version string
	err := c.db.QueryRow(`SELECT v FROM meta WHERE k = 'schema_version'`).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := c.db.Exec(`INSERT INTO meta(k, v) VALUES('schema_version', ?)`, schemaVersion); err != nil {
			return fmt.Errorf("record catalog schema: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read catalog schema: %w", err)
	case version != schemaVersion:
		return fmt.Errorf("unsupported catalog schema %q", version)
	}

	// Naming every expected column turns a pre-versioned or partially-created
	// table into a schema error, which Open handles with the same clean rebuild.
	rows, err := c.db.Query(`SELECT selector, harness, source_id, primary_path,
		primary_size, primary_mtime_unixnano, title, workspace, created_at,
		modified_at, continuation_grade, row_json FROM sessions LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate catalog schema: %w", err)
	}
	return rows.Close()
}

func removeDatabase(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-shm")
	_ = os.Remove(path + "-wal")
}

// Close releases the database handle.
func (c *Cache) Close() error { return c.db.Close() }

// Path reports the cache database path.
func (c *Cache) Path() string { return c.path }

type liveRef struct {
	ref       Ref
	size      int64
	mtimeNano int64
}

// Refresh reconciles the cache with the inexpensive discovery result. It calls
// describe only for new or changed primary files. A false result is omitted
// and removes any stale cached row for that selector.
func (c *Cache) Refresh(ctx context.Context, refs []Ref, describe func(Ref) (Row, bool)) ([]Row, error) {
	cached, err := c.readRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	bySelector := make(map[string]Row, len(cached))
	for _, row := range cached {
		bySelector[row.Selector] = row
	}

	unique := make(map[string]Ref, len(refs))
	for _, ref := range refs {
		if ref.Selector != "" {
			unique[ref.Selector] = ref
		}
	}
	selectors := make([]string, 0, len(unique))
	for selector := range unique {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)

	live := make(map[string]liveRef, len(selectors))
	updates := make(map[string]Row)
	failed := make(map[string]struct{})
	for _, selector := range selectors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref := unique[selector]
		info, err := os.Stat(ref.PrimaryPath)
		if err != nil || !info.Mode().IsRegular() {
			failed[selector] = struct{}{}
			continue
		}
		current := liveRef{ref: ref, size: info.Size(), mtimeNano: info.ModTime().UnixNano()}
		live[selector] = current
		old, found := bySelector[selector]
		if found && old.PrimaryPath == ref.PrimaryPath && old.PrimarySize == current.size &&
			old.PrimaryMtimeUnixNano == current.mtimeNano && json.Valid(old.RowJSON) {
			continue
		}

		row, ok := describe(ref)
		if !ok {
			failed[selector] = struct{}{}
			continue
		}
		row.Selector = ref.Selector
		row.Harness = ref.Harness
		row.SourceID = ref.SourceID
		row.PrimaryPath = ref.PrimaryPath
		row.PrimarySize = current.size
		row.PrimaryMtimeUnixNano = current.mtimeNano
		if !json.Valid(row.RowJSON) {
			failed[selector] = struct{}{}
			continue
		}
		updates[selector] = row
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog refresh: %w", err)
	}
	defer tx.Rollback()

	for selector := range bySelector {
		_, stillLive := live[selector]
		_, describeFailed := failed[selector]
		if stillLive && !describeFailed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE selector = ?`, selector); err != nil {
			return nil, fmt.Errorf("delete catalog row: %w", err)
		}
	}
	for _, selector := range selectors {
		row, ok := updates[selector]
		if !ok {
			continue
		}
		if err := upsert(ctx, tx, row); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog refresh: %w", err)
	}

	rows, err := c.readRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("read refreshed catalog: %w", err)
	}
	return rows, nil
}

func upsert(ctx context.Context, tx *sql.Tx, row Row) error {
	const query = `INSERT INTO sessions(
		selector, harness, source_id, primary_path, primary_size,
		primary_mtime_unixnano, title, workspace, created_at, modified_at,
		continuation_grade, row_json
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(selector) DO UPDATE SET
		harness=excluded.harness, source_id=excluded.source_id,
		primary_path=excluded.primary_path, primary_size=excluded.primary_size,
		primary_mtime_unixnano=excluded.primary_mtime_unixnano,
		title=excluded.title, workspace=excluded.workspace,
		created_at=excluded.created_at, modified_at=excluded.modified_at,
		continuation_grade=excluded.continuation_grade, row_json=excluded.row_json`
	grade := 0
	if row.ContinuationGrade {
		grade = 1
	}
	_, err := tx.ExecContext(ctx, query, row.Selector, row.Harness, row.SourceID,
		row.PrimaryPath, row.PrimarySize, row.PrimaryMtimeUnixNano, nullable(row.Title),
		nullable(row.Workspace), nullable(row.CreatedAt), nullable(row.ModifiedAt), grade,
		string(row.RowJSON))
	if err != nil {
		return fmt.Errorf("write catalog row %q: %w", row.Selector, err)
	}
	return nil
}

func nullable(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (c *Cache) readRows(ctx context.Context) ([]Row, error) {
	const query = `SELECT selector, harness, source_id, primary_path, primary_size,
		primary_mtime_unixnano, title, workspace, created_at, modified_at,
		continuation_grade, row_json FROM sessions ORDER BY selector`
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Row, 0)
	for rows.Next() {
		var row Row
		var title, workspace, createdAt, modifiedAt sql.NullString
		var grade int
		var rowJSON string
		if err := rows.Scan(&row.Selector, &row.Harness, &row.SourceID, &row.PrimaryPath,
			&row.PrimarySize, &row.PrimaryMtimeUnixNano, &title, &workspace, &createdAt,
			&modifiedAt, &grade, &rowJSON); err != nil {
			return nil, err
		}
		row.Title = stringPtr(title)
		row.Workspace = stringPtr(workspace)
		row.CreatedAt = stringPtr(createdAt)
		row.ModifiedAt = stringPtr(modifiedAt)
		row.ContinuationGrade = grade != 0
		row.RowJSON = []byte(rowJSON)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
