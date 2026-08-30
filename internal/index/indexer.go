package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/event"
)

// MaxIndexedTextBytes bounds the searchable text one event contributes.
//
// The number is the byte ceiling of what event.Scan can already emit —
// event.TextRuneLimit runes at UTF-8's four bytes per rune — so it truncates
// nothing the normalizer produces today. It exists anyway because a single
// FTS5 row has to be bounded by this package's own contract rather than by
// another package's constant: a later increase to the normalizer's rune clip
// must not silently make one index row unbounded, and an oversized record,
// whose retained prefix is the only text there is, must land in the index as
// a bounded excerpt rather than as megabytes of tokens.
//
// Truncation loses nothing recoverable, and neither does the normalizer's
// own clip: indexed text is always an excerpt for retrieval, never
// authoritative content, so a hit's locator — file, offset, digest — is the
// only way to the record's bytes and is always present.
const MaxIndexedTextBytes = 4 * event.TextRuneLimit

const (
	// batchEvents is how many event rows one INSERT statement carries. It
	// trades statement compilations against buffered rows; a few hundred
	// rows per statement is where SQLite's per-statement overhead stops
	// being visible against the row work itself.
	batchEvents = 128

	// batchTextBytes flushes a batch early when its buffered text reaches
	// this size. The row count alone is not a memory bound, because a
	// batch of text-heavy events holds far more bytes than a batch of
	// ordinary ones: the corpus's own cost is per byte, so the batch's
	// bound has to be too.
	batchTextBytes = 2 << 20
)

// Result reports what indexing one session did. Skipped and Replaced are
// separate facts: a skipped session was not read at all, while a replaced
// one had its previous rows removed and rebuilt in the same transaction.
type Result struct {
	Path    string
	Harness string
	// SourceID is the adapter's session identity, carried through so a
	// caller logging progress does not have to re-derive it.
	SourceID string
	// Bytes is the primary log's size as indexing observed it.
	Bytes int64
	// Records counts source records read; Events counts the analysis events
	// they classified into, which is larger because one record can carry
	// several semantic units.
	Records int
	Events  int
	Skipped bool
	// Replaced reports that a previously indexed version of this session
	// was removed and rebuilt.
	Replaced bool
}

// IndexSession indexes the primary log at s.Path, replacing any previously
// indexed version of it.
//
// Incremental by observation, not by trust: the recorded size, modification
// time, and session identity of the last index decide whether the file is
// read at all, so an unchanged session costs one stat and one row lookup.
// Anything else — a different size, a newer mtime, a different adapter
// schema or source id — re-reads the file and replaces every row of that
// session inside one transaction, so a search never observes a session that
// is half old and half new.
//
// Memory is bounded by the insert batch, not by the session: the file is
// streamed through event.Scan and no more than batchEvents rows or
// batchTextBytes of text are buffered at a time. A session of hundreds of
// megabytes indexes in the same memory as a small one.
func (x *Index) IndexSession(ctx context.Context, s event.Stream) (Result, error) {
	res := Result{Path: s.Path, Harness: s.Harness, SourceID: s.SourceID}
	info, err := os.Stat(s.Path)
	if err != nil {
		return res, fmt.Errorf("stat session %s: %w", s.Path, err)
	}
	res.Bytes = info.Size()
	mtime := info.ModTime().UnixNano()

	prior, found, err := x.priorSession(ctx, s.Path)
	if err != nil {
		return res, err
	}
	if found && prior.matches(s, info.Size(), mtime) {
		res.Skipped = true
		res.Records = prior.records
		res.Events = prior.events
		return res, nil
	}

	file, err := os.Open(s.Path)
	if err != nil {
		return res, fmt.Errorf("open session %s: %w", s.Path, err)
	}
	defer file.Close()

	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin index transaction: %w", err)
	}
	defer tx.Rollback()

	if found {
		if err := deleteSession(ctx, tx, prior.id); err != nil {
			return res, err
		}
		res.Replaced = true
	}

	sessionID, err := insertSession(ctx, tx, s, info.Size(), mtime)
	if err != nil {
		return res, err
	}

	w, err := newWriter(ctx, tx, sessionID)
	if err != nil {
		return res, err
	}
	defer w.close()

	lastLine := 0
	err = event.Scan(file, s, func(e event.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// §4.3: an observation cannot exist without evidence. An event
		// whose locator cannot recover its bytes would be a searchable
		// claim with no way back to the corpus, so it is a hard error
		// rather than a row.
		if e.Locator.Digest == "" || e.Locator.Path == "" {
			return fmt.Errorf("event %d at line %d has no evidence locator", e.Index, e.Locator.Line)
		}
		if e.Locator.Line != lastLine {
			lastLine = e.Locator.Line
			res.Records++
		}
		res.Events++
		return w.add(e)
	})
	if err != nil {
		return res, fmt.Errorf("index session %s: %w", s.Path, err)
	}
	if err := w.flush(); err != nil {
		return res, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET records = ?, events = ? WHERE id = ?`,
		res.Records, res.Events, sessionID); err != nil {
		return res, fmt.Errorf("record session counts for %s: %w", s.Path, err)
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("commit index of %s: %w", s.Path, err)
	}
	return res, nil
}

// priorRow is what the last index of a session recorded about the file it
// read, which is everything needed to decide whether reading it again would
// produce the same rows.
type priorRow struct {
	id            int64
	harness       string
	adapterSchema int
	sourceID      string
	size          int64
	mtime         int64
	records       int
	events        int
}

func (p priorRow) matches(s event.Stream, size, mtime int64) bool {
	return p.harness == s.Harness &&
		p.adapterSchema == s.AdapterSchema &&
		p.sourceID == s.SourceID &&
		p.size == size &&
		p.mtime == mtime
}

func (x *Index) priorSession(ctx context.Context, path string) (priorRow, bool, error) {
	var p priorRow
	err := x.db.QueryRowContext(ctx,
		`SELECT id, harness, adapter_schema, source_id, size, mtime_unixnano, records, events
		 FROM sessions WHERE path = ?`, path).
		Scan(&p.id, &p.harness, &p.adapterSchema, &p.sourceID, &p.size, &p.mtime, &p.records, &p.events)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return priorRow{}, false, nil
	case err != nil:
		return priorRow{}, false, fmt.Errorf("look up indexed session %s: %w", path, err)
	}
	return p, true, nil
}

// deleteSession removes a session's rows. The order is the dependency order
// — paths, then events, then the session — because the index deliberately
// declares no foreign keys: enforcement would depend on a per-connection
// pragma, and a cache whose integrity silently depended on one would be
// worse than two explicit statements. The FTS5 entries follow from the
// delete trigger.
func deleteSession(ctx context.Context, tx *sql.Tx, sessionID int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM event_paths WHERE event_id IN (SELECT id FROM events WHERE session_id = ?)`,
		sessionID); err != nil {
		return fmt.Errorf("delete indexed event paths: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete indexed events: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete indexed session: %w", err)
	}
	return nil
}

func insertSession(ctx context.Context, tx *sql.Tx, s event.Stream, size, mtime int64) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`INSERT INTO sessions(path, harness, adapter_schema, source_id, size, mtime_unixnano, records, events)
		 VALUES(?, ?, ?, ?, ?, ?, 0, 0)`,
		s.Path, s.Harness, s.AdapterSchema, s.SourceID, size, mtime)
	if err != nil {
		return 0, fmt.Errorf("record indexed session %s: %w", s.Path, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("record indexed session %s: %w", s.Path, err)
	}
	return id, nil
}

// eventColumns are the event row's columns in insert order. The count drives
// the generated multi-row statement, so the two can never disagree.
var eventColumns = []string{
	"id", "session_id", "event_index", "kind", "role", "tool", "outcome",
	"event_time", "partial", "line", "byte_offset", "digest", "text",
}

// batchInsertSQL builds the multi-row insert for exactly rows rows. The SQL
// is generated from the column list and a row count, never from data: every
// value is a placeholder.
func batchInsertSQL(rows int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO events(")
	b.WriteString(strings.Join(eventColumns, ", "))
	b.WriteString(") VALUES ")
	group := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(eventColumns)), ", ") + ")"
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(group)
	}
	return b.String()
}

// The two event-insert statements, built once: one carrying a whole batch
// and one carrying a single row for a batch's remainder.
var (
	batchInsert  = batchInsertSQL(batchEvents)
	singleInsert = batchInsertSQL(1)
)

// eventRow is one buffered event. It holds only what the row needs, so a
// batch's memory is its text plus a fixed header per event rather than the
// whole event.Event.
type eventRow struct {
	kind    string
	role    string
	tool    string
	outcome string
	time    any // int64 unix nanos, or nil when the harness recorded none
	index   int
	line    int
	offset  int64
	digest  string
	text    string
	partial bool
	paths   []string
}

// writer buffers event rows and flushes them into one session's transaction.
type writer struct {
	ctx       context.Context
	tx        *sql.Tx
	batch     *sql.Stmt
	single    *sql.Stmt
	path      *sql.Stmt
	sessionID int64
	nextID    int64

	rows      []eventRow
	textBytes int
	args      []any
}

func newWriter(ctx context.Context, tx *sql.Tx, sessionID int64) (*writer, error) {
	// Event ids are assigned here rather than taken from the insert,
	// because a batch insert reports only its last rowid while
	// event_paths needs the id of each row. Starting past the highest id
	// in the table also means a rowid is never reused, which an
	// external-content FTS5 index requires: a reused rowid could inherit
	// the tokens of the row that held it before.
	var maxID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&maxID); err != nil {
		return nil, fmt.Errorf("read highest indexed event id: %w", err)
	}
	w := &writer{
		ctx:       ctx,
		tx:        tx,
		sessionID: sessionID,
		nextID:    maxID.Int64 + 1,
		rows:      make([]eventRow, 0, batchEvents),
		args:      make([]any, 0, batchEvents*len(eventColumns)),
	}
	var err error
	if w.batch, err = tx.PrepareContext(ctx, batchInsert); err != nil {
		return nil, fmt.Errorf("prepare batched event insert: %w", err)
	}
	if w.single, err = tx.PrepareContext(ctx, singleInsert); err != nil {
		return nil, fmt.Errorf("prepare event insert: %w", err)
	}
	// A record that names the same path twice is one repository change to
	// that path, so the duplicate is ignored rather than being an error
	// that would fail a whole session's index.
	if w.path, err = tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO event_paths(event_id, path) VALUES(?, ?)`); err != nil {
		return nil, fmt.Errorf("prepare event path insert: %w", err)
	}
	return w, nil
}

func (w *writer) close() {
	for _, stmt := range []*sql.Stmt{w.batch, w.single, w.path} {
		if stmt != nil {
			stmt.Close()
		}
	}
}

func (w *writer) add(e event.Event) error {
	text, _ := truncateText(e.Text)
	row := eventRow{
		kind:    string(e.Kind),
		role:    e.Role,
		tool:    e.Tool,
		outcome: e.Outcome,
		index:   e.Index,
		line:    e.Locator.Line,
		offset:  e.Locator.ByteOffset,
		digest:  e.Locator.Digest,
		text:    text,
		partial: e.Partial,
	}
	if e.Time != nil && indexableTime(*e.Time) {
		row.time = e.Time.UnixNano()
	}
	if len(e.Paths) > 0 {
		// event.Scan reuses its record buffer, and documents Paths as
		// one of the two fields a callback may retain; the slice itself
		// is still the event's, so the batch keeps its own copy.
		row.paths = append(make([]string, 0, len(e.Paths)), e.Paths...)
	}
	w.rows = append(w.rows, row)
	w.textBytes += len(text)
	if len(w.rows) >= batchEvents || w.textBytes >= batchTextBytes {
		return w.flush()
	}
	return nil
}

// flush writes every buffered row: whole batches through the multi-row
// statement, then the remainder one row at a time. Both statements are
// prepared once and the argument slice is reused, so flushing allocates
// nothing per event. The row count has to match the statement's placeholders
// exactly — a driver given more arguments than a statement has parameters
// binds the ones it has and silently drops the rest — so the batch size is
// passed to exec rather than inferred there.
func (w *writer) flush() error {
	for len(w.rows) >= batchEvents {
		if err := w.exec(w.batch, batchEvents); err != nil {
			return err
		}
	}
	for len(w.rows) > 0 {
		if err := w.exec(w.single, 1); err != nil {
			return err
		}
	}
	w.textBytes = 0
	return nil
}

// exec inserts the first n buffered rows with stmt, which must carry exactly
// n rows of placeholders, and then their repository paths.
func (w *writer) exec(stmt *sql.Stmt, n int) error {
	w.args = w.args[:0]
	for _, row := range w.rows[:n] {
		w.args = append(w.args,
			w.nextID, w.sessionID, row.index, row.kind, row.role, row.tool,
			row.outcome, row.time, row.partial, row.line,
			row.offset, row.digest, row.text)
		w.nextID++
	}
	if _, err := stmt.ExecContext(w.ctx, w.args...); err != nil {
		return fmt.Errorf("insert indexed events: %w", err)
	}
	// The ids just consumed are contiguous and end at nextID-1.
	id := w.nextID - int64(n)
	for _, row := range w.rows[:n] {
		for _, p := range row.paths {
			if _, err := w.path.ExecContext(w.ctx, id, p); err != nil {
				return fmt.Errorf("insert indexed event path: %w", err)
			}
		}
		id++
	}
	rest := copy(w.rows, w.rows[n:])
	clear(w.rows[rest:])
	w.rows = w.rows[:rest]
	return nil
}

// truncateText bounds an event's indexed text at MaxIndexedTextBytes without
// splitting a rune, because a partial rune would be an invalid token and
// SQLite stores TEXT as UTF-8.
func truncateText(text string) (string, bool) {
	if len(text) <= MaxIndexedTextBytes {
		return text, false
	}
	cut := MaxIndexedTextBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// Event time is stored as a nanosecond epoch, which covers 1678 through
// 2262. Transcript timestamps are untrusted input, so the bounds are checked
// rather than assumed: time.Time.UnixNano is undefined outside this range,
// and a record dated in the year 9999 would otherwise be indexed at a
// silently wrapped instant.
var (
	earliestIndexable = time.Unix(0, math.MinInt64)
	latestIndexable   = time.Unix(0, math.MaxInt64)
)

// indexableTime reports whether an event's timestamp can be stored as a
// nanosecond epoch. One that cannot is indexed with no time at all — §3
// forbids synthesizing a value, and the record's own bytes stay recoverable
// through its locator.
func indexableTime(at time.Time) bool {
	return at.After(earliestIndexable) && at.Before(latestIndexable)
}
