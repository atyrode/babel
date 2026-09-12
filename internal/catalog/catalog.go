// Package catalog maintains the rebuildable local session-list cache.
package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/atyrode/babel/internal/durable"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// schemaVersion is bumped when the cached columns change. A mismatch makes Open
// discard and rebuild the cache, which is safe because every row is derivable
// from live sources.
//
// Version 4 added the usage columns (issue #89). The bump is what makes the
// addition safe: a cache written by a binary that never extracted usage would
// otherwise be read as a corpus that genuinely cost nothing, and one clean
// rebuild recovers the numbers from files this machine already holds.
//
// Version 5 added the repository columns (SPEC.md §4.13). A catalog written
// before them holds no observation at all, and a surface that read that
// absence as "no session belongs to a repository" would file the whole corpus
// as unfiled; the rebuild observes every workspace once from checkouts this
// machine already holds.
const schemaVersion = "5"

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
	// TitleProvenance says where Title came from — recorded by the harness,
	// derived by Babel, or inferred by a model — and is nil exactly when
	// Title is. It is cached rather than recomputed because the publish path
	// reads it on every push and a reader on another machine must not have
	// to assume; nil there means unknown, never "recorded".
	TitleProvenance *string
	Workspace       *string
	CreatedAt       *string
	ModifiedAt      *string
	// RepositoryIdentity, RepositoryRemote and RepositoryReason are the
	// repository the session's workspace belongs to, as observed during the
	// scan (adapter.Repository). The workspace says where the work happened;
	// this says what it was about, which is the distinction SPEC.md §4.13
	// draws between a locator and a topic — every worktree of one repository
	// shares one identity, and a directory under /tmp has none.
	//
	// They are cached for the same reason the usage columns are: the
	// observation runs two subprocesses against a checkout, and re-deriving
	// it per page would make reading the feed scale with the corpus rather
	// than with what changed. RepositoryReason is set exactly when
	// RepositoryIdentity is nil and says why nothing was observed, so an
	// unfiled session is explained rather than merely empty (§3).
	RepositoryIdentity *string
	RepositoryRemote   *string
	RepositoryReason   *string
	ContinuationGrade  bool
	// ArtifactCount, BlobCount, and UnresolvedBlobCount are the session's
	// closure as the adapter observed it. They are cached because shared-catalog
	// publication needs them on every push, and re-describing an unchanged
	// session to recover a number the describe already computed would make an
	// hourly push scale with the whole corpus instead of with what changed.
	ArtifactCount       int
	BlobCount           int
	UnresolvedBlobCount int
	// CostUSD, TotalTokens, Turns and ToolErrors are the published summary of
	// the session's recorded usage: what the harness itself wrote down about
	// what the session cost, summed by the adapter over the raw transcript
	// (adapter.Usage). They are cached for the same reason the counts above
	// are - the publish path reads them on every push, and recomputing one
	// means re-reading a log that has not changed.
	//
	// All four are nil when the adapter reported no usage at all, and nil is
	// the load-bearing state: a zero cost is a session that ran for free,
	// while nil is a session nothing has measured.
	CostUSD     *float64
	TotalTokens *int64
	Turns       *int64
	ToolErrors  *int64
	RowJSON     []byte
}

// Progress reports describe throughput while Refresh runs. Total counts the
// stale sessions this run has to describe; Described counts the attempts it
// has finished, so Described reaches Total exactly once the run is complete.
type Progress struct {
	Total     int
	Described int
	Failed    int
	Harness   string
}

// Cache is a catalog database rooted in Babel's private XDG data directory.
type Cache struct {
	db   *sql.DB
	path string
}

// Open opens <dir>/catalog.db. A corrupt or incompatible database is removed
// and rebuilt once; only a failure of that clean rebuild is returned.
//
// Only corruption or an unrecognised schema earns the rebuild. Every other
// failure — a lock held past busy_timeout, an I/O error, a permission — is
// returned as it is, because the database behind it is intact and removing it
// would destroy a catalog that was merely busy. The web server showed why
// that matters: two requests opening the same fresh catalog at once, one
// losing the lock race, and the loser deleting the WAL index out from under
// the winner's memory mapping, which macOS answers with SIGBUS.
func Open(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	path := filepath.Join(dir, "catalog.db")
	cache, err := open(path)
	if err == nil {
		return cache, nil
	}
	if !rebuildable(err) {
		return nil, fmt.Errorf("open catalog: %w", err)
	}
	removeDatabase(path)
	cache, err = open(path)
	if err != nil {
		return nil, fmt.Errorf("open catalog after rebuild: %w", err)
	}
	return cache, nil
}

// errSchema marks an init failure whose remedy is a clean rebuild: the file
// is a readable database, but not one this binary knows how to use.
var errSchema = errors.New("catalog schema")

// rebuildable reports whether err describes a database that a rebuild fixes:
// SQLite's own verdict that the file is corrupt or not a database, or this
// package's verdict that its schema is not ours.
func rebuildable(err error) bool {
	if errors.Is(err, errSchema) {
		return true
	}
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	switch sqlErr.Code() & 0xff {
	case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
		return true
	}
	return false
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
	db, err := durable.Open(path)
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
	// busy_timeout comes first because every statement after it can need a
	// lock: quick_check reads, and the WAL switch and the schema write on a
	// fresh file both write. Set any later, a catalog another connection was
	// writing at that instant read as failed rather than as busy.
	if _, err := c.db.Exec(durable.BusyPragma); err != nil {
		return fmt.Errorf("set catalog busy timeout: %w", err)
	}

	var integrity string
	if err := c.db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("check catalog integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%w: check catalog integrity: %s", errSchema, integrity)
	}

	// WAL lets a reader (the web process answering /api/sessions) see
	// batches a running scan has already committed, instead of blocking on
	// the writer or being skipped entirely; busy_timeout absorbs the brief
	// overlap when a batch commits while a read is starting. Both are set
	// per connection, and journal_mode persists in the file.
	var journal string
	if err := c.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enable catalog WAL: %w", err)
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
	title_provenance TEXT,
	workspace TEXT,
	created_at TEXT,
	modified_at TEXT,
	repository_identity TEXT,
	repository_remote TEXT,
	repository_reason TEXT,
	continuation_grade INTEGER,
	artifact_count INTEGER,
	blob_count INTEGER,
	unresolved_blob_count INTEGER,
	cost_usd REAL,
	total_tokens INTEGER,
	turns INTEGER,
	tool_errors INTEGER,
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
		return fmt.Errorf("%w: read catalog schema: %w", errSchema, err)
	case version != schemaVersion:
		return fmt.Errorf("%w: unsupported catalog schema %q", errSchema, version)
	}

	// Naming every expected column turns a pre-versioned or partially-created
	// table into a schema error, which Open handles with the same clean rebuild.
	rows, err := c.db.Query(`SELECT selector, harness, source_id, primary_path,
		primary_size, primary_mtime_unixnano, title, title_provenance, workspace,
		created_at, modified_at, repository_identity, repository_remote,
		repository_reason, continuation_grade, artifact_count, blob_count,
		unresolved_blob_count, cost_usd, total_tokens, turns, tool_errors,
		row_json FROM sessions LIMIT 0`)
	if err != nil {
		return fmt.Errorf("%w: validate catalog schema: %w", errSchema, err)
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

// SessionIdentity is one cached session's identity, and nothing else about it:
// the selector this machine fetches it by, the harness that wrote it, and the
// adapter's source id.
//
// It exists for the typed reference graph (issue #113). A reference to a
// session is addressed by its durable session key - the
// `sharedcatalog.SessionUID` digest over deployment, host, harness and source
// id - and nothing stores that digest locally, so resolving one means deriving
// it from the sessions this machine holds. These three fields are the whole
// input to that derivation, which is why this is its own shape rather than a
// Row: reading a Row per session would pull every cached title, path and
// row_json blob through the resolver to answer a question about identity.
type SessionIdentity struct {
	Selector string
	Harness  string
	SourceID string
}

// SessionIdentities reports the identity of every cached session.
//
// It is the whole corpus rather than a lookup because a durable session key is
// a one-way digest: a caller holding one cannot ask the cache for it, only
// derive the keys of what it has and compare. The projection is three short
// columns, so that comparison costs a scan of identities rather than of
// descriptions.
func (c *Cache) SessionIdentities(ctx context.Context) ([]SessionIdentity, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT selector, COALESCE(harness, ''), COALESCE(source_id, '') FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("read session identities: %w", err)
	}
	defer rows.Close()
	var out []SessionIdentity
	for rows.Next() {
		var id SessionIdentity
		if err := rows.Scan(&id.Selector, &id.Harness, &id.SourceID); err != nil {
			return nil, fmt.Errorf("scan session identity: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read session identities: %w", err)
	}
	return out, nil
}

type liveRef struct {
	ref       Ref
	size      int64
	mtimeNano int64
}

// refreshBatchSize bounds how many pending writes Refresh accumulates before
// committing. Small batches keep a cancelled run's completed describes durable
// without paying a transaction per session.
const refreshBatchSize = 25

// Refresh reconciles the cache with the inexpensive discovery result. It calls
// describe only for new or changed primary files.
//
// scope names the harnesses this refresh actually covered, and is the only
// authority for deletion: a cached row is pruned only when its harness is in
// scope and the session is either gone or failed to describe. An empty scope
// therefore prunes nothing, so a partial refresh can never gut the rows it did
// not look at, while an in-scope harness whose sessions have all vanished does
// lose its rows. The returned rows are restricted to the scoped harnesses;
// an empty scope returns the whole catalog.
//
// Describing happens outside any transaction and the resulting writes are
// committed in batches, so a cancelled run keeps every describe it already
// finished: the next Refresh resumes instead of starting over. Cancellation
// commits the batch in flight and then returns ctx.Err(). onProgress may be
// nil; otherwise it is called once before the describe loop with Total set and
// Described zero, then after every describe attempt.
func (c *Cache) Refresh(ctx context.Context, scope []string, refs []Ref, describe func(Ref) (Row, bool), onProgress func(Progress)) ([]Row, error) {
	scoped := make(map[string]struct{}, len(scope))
	for _, harness := range scope {
		scoped[harness] = struct{}{}
	}
	inScope := func(harness string) bool {
		_, ok := scoped[harness]
		return ok
	}

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

	live := make(map[string]struct{}, len(selectors))
	stale := make([]liveRef, 0, len(selectors))
	for _, selector := range selectors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ref := unique[selector]
		info, err := os.Stat(ref.PrimaryPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		live[selector] = struct{}{}
		current := liveRef{ref: ref, size: info.Size(), mtimeNano: info.ModTime().UnixNano()}
		old, found := bySelector[selector]
		if found && old.PrimaryPath == ref.PrimaryPath && old.PrimarySize == current.size &&
			old.PrimaryMtimeUnixNano == current.mtimeNano && json.Valid(old.RowJSON) {
			continue
		}
		stale = append(stale, current)
	}

	// Writes use a context detached from the caller's: a cancelled scan still
	// has to commit the describes it completed.
	writeCtx := context.WithoutCancel(ctx)
	removals := make([]string, 0)
	for selector, row := range bySelector {
		if !inScope(row.Harness) {
			continue
		}
		if _, stillLive := live[selector]; !stillLive {
			removals = append(removals, selector)
		}
	}
	sort.Strings(removals)
	if err := c.commit(writeCtx, removals, nil); err != nil {
		return nil, err
	}

	progress := Progress{Total: len(stale)}
	report := func() {
		if onProgress != nil {
			onProgress(progress)
		}
	}
	report()

	removals = removals[:0]
	pending := make([]Row, 0, refreshBatchSize)
	flush := func() error {
		if err := c.commit(writeCtx, removals, pending); err != nil {
			return err
		}
		removals = removals[:0]
		pending = pending[:0]
		return nil
	}

	var cancelled error
	for _, current := range stale {
		if err := ctx.Err(); err != nil {
			cancelled = err
			break
		}
		ref := current.ref
		row, ok := describe(ref)
		if ok {
			row.Selector = ref.Selector
			row.Harness = ref.Harness
			row.SourceID = ref.SourceID
			row.PrimaryPath = ref.PrimaryPath
			row.PrimarySize = current.size
			row.PrimaryMtimeUnixNano = current.mtimeNano
			ok = json.Valid(row.RowJSON)
		}
		if ok {
			pending = append(pending, row)
		} else {
			progress.Failed++
			if old, found := bySelector[ref.Selector]; found && inScope(old.Harness) {
				removals = append(removals, ref.Selector)
			}
		}
		progress.Described++
		progress.Harness = ref.Harness
		report()

		if len(pending)+len(removals) >= refreshBatchSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if cancelled != nil {
		return nil, cancelled
	}

	rows, err := c.readRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("read refreshed catalog: %w", err)
	}
	if len(scoped) == 0 {
		return rows, nil
	}
	inside := make([]Row, 0, len(rows))
	for _, row := range rows {
		if inScope(row.Harness) {
			inside = append(inside, row)
		}
	}
	return inside, nil
}

// commit applies one batch of catalog writes in a single transaction. The
// database allows one connection, so batches never overlap.
func (c *Cache) commit(ctx context.Context, removals []string, rows []Row) error {
	if len(removals) == 0 && len(rows) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog refresh: %w", err)
	}
	defer tx.Rollback()

	for _, selector := range removals {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE selector = ?`, selector); err != nil {
			return fmt.Errorf("delete catalog row: %w", err)
		}
	}
	for _, row := range rows {
		if err := upsert(ctx, tx, row); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog refresh: %w", err)
	}
	return nil
}

func upsert(ctx context.Context, tx *sql.Tx, row Row) error {
	const query = `INSERT INTO sessions(
		selector, harness, source_id, primary_path, primary_size,
		primary_mtime_unixnano, title, title_provenance, workspace, created_at,
		modified_at, repository_identity, repository_remote, repository_reason,
		continuation_grade, artifact_count, blob_count,
		unresolved_blob_count, cost_usd, total_tokens, turns, tool_errors,
		row_json
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(selector) DO UPDATE SET
		harness=excluded.harness, source_id=excluded.source_id,
		primary_path=excluded.primary_path, primary_size=excluded.primary_size,
		primary_mtime_unixnano=excluded.primary_mtime_unixnano,
		title=excluded.title, title_provenance=excluded.title_provenance,
		workspace=excluded.workspace,
		created_at=excluded.created_at, modified_at=excluded.modified_at,
		repository_identity=excluded.repository_identity,
		repository_remote=excluded.repository_remote,
		repository_reason=excluded.repository_reason,
		continuation_grade=excluded.continuation_grade,
		artifact_count=excluded.artifact_count, blob_count=excluded.blob_count,
		unresolved_blob_count=excluded.unresolved_blob_count,
		cost_usd=excluded.cost_usd, total_tokens=excluded.total_tokens,
		turns=excluded.turns, tool_errors=excluded.tool_errors,
		row_json=excluded.row_json`
	grade := 0
	if row.ContinuationGrade {
		grade = 1
	}
	_, err := tx.ExecContext(ctx, query, row.Selector, row.Harness, row.SourceID,
		row.PrimaryPath, row.PrimarySize, row.PrimaryMtimeUnixNano, nullable(row.Title),
		nullable(row.TitleProvenance), nullable(row.Workspace), nullable(row.CreatedAt),
		nullable(row.ModifiedAt), nullable(row.RepositoryIdentity),
		nullable(row.RepositoryRemote), nullable(row.RepositoryReason), grade,
		row.ArtifactCount, row.BlobCount, row.UnresolvedBlobCount,
		nullableFloat(row.CostUSD), nullableInt(row.TotalTokens),
		nullableInt(row.Turns), nullableInt(row.ToolErrors),
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

// nullableFloat and nullableInt keep an absent measure absent. A usage column
// filled with zero would say the session was measured and cost nothing.
func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (c *Cache) readRows(ctx context.Context) ([]Row, error) {
	const query = `SELECT selector, harness, source_id, primary_path, primary_size,
		primary_mtime_unixnano, title, title_provenance, workspace, created_at,
		modified_at, repository_identity, repository_remote, repository_reason,
		continuation_grade, artifact_count, blob_count,
		unresolved_blob_count, cost_usd, total_tokens, turns, tool_errors,
		row_json FROM sessions ORDER BY selector`
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Row, 0)
	for rows.Next() {
		var row Row
		var title, provenance, workspace, createdAt, modifiedAt sql.NullString
		var repoIdentity, repoRemote, repoReason sql.NullString
		var cost sql.NullFloat64
		var totalTokens, turns, toolErrors sql.NullInt64
		var grade int
		var rowJSON string
		if err := rows.Scan(&row.Selector, &row.Harness, &row.SourceID, &row.PrimaryPath,
			&row.PrimarySize, &row.PrimaryMtimeUnixNano, &title, &provenance, &workspace,
			&createdAt, &modifiedAt, &repoIdentity, &repoRemote, &repoReason,
			&grade, &row.ArtifactCount, &row.BlobCount,
			&row.UnresolvedBlobCount, &cost, &totalTokens, &turns, &toolErrors,
			&rowJSON); err != nil {
			return nil, err
		}
		row.Title = stringPtr(title)
		row.TitleProvenance = stringPtr(provenance)
		row.Workspace = stringPtr(workspace)
		row.CreatedAt = stringPtr(createdAt)
		row.ModifiedAt = stringPtr(modifiedAt)
		row.RepositoryIdentity = stringPtr(repoIdentity)
		row.RepositoryRemote = stringPtr(repoRemote)
		row.RepositoryReason = stringPtr(repoReason)
		row.ContinuationGrade = grade != 0
		row.CostUSD = floatPtr(cost)
		row.TotalTokens = intPtr(totalTokens)
		row.Turns = intPtr(turns)
		row.ToolErrors = intPtr(toolErrors)
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

// floatPtr and intPtr are stringPtr's counterparts for the usage columns:
// a NULL reads back as nil, never as zero.
func floatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func intPtr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}
