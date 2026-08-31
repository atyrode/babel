package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/event"
)

// Order is how Search sorts a result page. It is presentation only: §5.4
// forbids retrieval rank from becoming evidence strength, so a hit's
// position under any Order says nothing about how well the corpus supports
// it. Changing Order changes the order of a result set, never its
// membership.
type Order string

// The orderings Search supports. OrderRelevance is FTS5's own ranking and
// therefore requires a match expression; the temporal orders are what §5.4's
// temporal filters are usually read with, and they place events whose
// harness recorded no timestamp last rather than pretending they are old.
const (
	OrderRelevance Order = "relevance"
	OrderNewest    Order = "newest"
	OrderOldest    Order = "oldest"
)

const (
	// DefaultLimit is the page size for a query that does not choose one.
	DefaultLimit = 50
	// MaxLimit bounds one page. Retrieval feeds analysis contexts and
	// review surfaces, both of which consume a page at a time; a caller
	// that wants more pages asks for them with Offset, which keeps one
	// query's memory bounded on a corpus of millions of events.
	MaxLimit = 500
)

// Errors a malformed query produces. They are sentinels because a caller —
// including an analysis worker driving retrieval through the evidence broker
// — has to be able to tell a rejected query from a failed index.
var (
	ErrLimit                 = errors.New("index: limit out of range")
	ErrOffset                = errors.New("index: negative offset")
	ErrOrder                 = errors.New("index: unknown order")
	ErrRelevanceWithoutMatch = errors.New("index: relevance order requires a match expression")
	// ErrFrontierKind reports a self-retrieval filter naming a surface this
	// build does not index. It is refused rather than dropped: a filter
	// silently ignored would answer a question about all of Babel's output
	// while looking like an answer about one part of it.
	ErrFrontierKind = errors.New("index: unknown frontier record kind")
)

// Query is one retrieval request: §5.4's full-text search, structured
// filters, session/repository links, and temporal filters.
//
// Every slice field is an any-of filter and an empty slice is no filter at
// all. Filters combine with AND across fields, so Kinds plus Since plus
// Match asks for events of those kinds, at or after that time, matching that
// text.
type Query struct {
	// Match is a full-text search expression. Empty searches no text,
	// which is how a caller browses by structure alone.
	//
	// The grammar is small and it is the whole grammar: whitespace
	// separates terms and terms are optional, so a record matching any of
	// them is a hit; "a quoted phrase" matches its words adjacently and ""
	// inside one is a literal quote; a leading - excludes a term and an
	// exclusion is not optional; a trailing * on an unquoted term matches
	// by prefix. Everything else is data. FTS5's own operators — AND, OR,
	// NOT, NEAR, column filters, parentheses, carets — carry no meaning
	// here and are matched as the words they are, because a caller's query
	// is untrusted text rather than syntax. Control characters cannot be
	// tokens and are treated as separators; an expression with no
	// searchable term at all is ErrNoSearchableTerm, and one longer than
	// MaxMatchBytes is ErrMatchTooLong.
	//
	// Optional terms make membership broad on purpose, and the two things
	// that keep a result set useful are OrderRelevance — bm25 ranks by how
	// many of the terms a record matched and how rare they are — and
	// Limit. A caller that reads a match as a filter rather than as a
	// ranked question will be disappointed by the tail of a long page;
	// that is what buildMatch's doc means by membership broad, order
	// discriminating.
	Match string

	// Harnesses and SourceIDs are the session links: a harness name as the
	// adapters report it, and an adapter's session identity.
	Harnesses []string
	SourceIDs []string

	Kinds    []event.Kind
	Tools    []string
	Outcomes []string

	// RepositoryPath restricts results to events whose recorded repository
	// paths are this path or lie beneath it. Matching is exact or by
	// directory, never by substring: half a path component is not a link
	// to a repository.
	RepositoryPath string

	// Since and Until bound event time, inclusive and exclusive
	// respectively. A time filter excludes events whose harness recorded
	// no timestamp: §3 forbids synthesizing one, so such an event has no
	// defensible position on a timeline.
	Since time.Time
	Until time.Time

	// Partial, when set, selects torn or malformed records (true) or
	// intact ones (false). It is a filter of its own because a degraded
	// record is evidence about the capture as much as about the session.
	Partial *bool

	Order  Order
	Limit  int
	Offset int
}

// Hit is one retrieved event, with the identity needed to fetch its session
// and the locator needed to recover its bytes.
//
// There is deliberately no score, rank, relevance, or confidence field.
// §5.4's rule is that retrieval rank never becomes evidence strength, and
// the way to enforce it is to make it unrepresentable: the only thing Search
// communicates about ordering is the order of the returned slice, and
// Query.Order says what that order means.
//
// Text is a bounded excerpt of the record's normalized text — the
// normalizer's rune clip, re-bounded here by MaxIndexedTextBytes — and never
// the record. Nothing downstream may treat it as the whole evidence: Locator
// is the authority and recovers the bytes.
type Hit struct {
	// Harness, AdapterSchema, SourceID, and Locator.Path together identify
	// the session, which is what a caller needs to fetch or open it.
	Harness       string
	AdapterSchema int
	SourceID      string

	// Index is the event's position in its session's event order, so hits
	// from one session can be put back in conversation order.
	Index int

	Kind    event.Kind
	Role    string
	Tool    string
	Outcome string
	Time    *time.Time
	Paths   []string
	Partial bool

	Text string

	Locator event.Locator
}

// hitColumns is the projection every search shares. sessions.path is the
// locator's path: a locator addresses a record inside one primary log, and
// storing that path once per session instead of once per event is what keeps
// the row small.
const hitColumns = `s.harness, s.adapter_schema, s.source_id, s.path,
	e.id, e.event_index, e.kind, e.role, e.tool, e.outcome, e.event_time,
	e.partial, e.line, e.byte_offset, e.digest, e.text`

// Search runs one query and returns its page of hits in Order's order.
//
// The returned order is ordering only (see Hit): a caller may present it,
// but nothing downstream may read position as confidence. An empty result is
// not an error, and neither is a page shorter than Limit — that is how a
// caller learns it has reached the end.
func (x *Index) Search(ctx context.Context, q Query) ([]Hit, error) {
	limit, err := pageSize(q.Limit, q.Offset)
	if err != nil {
		return nil, err
	}
	order, err := orderClause(q)
	if err != nil {
		return nil, err
	}

	var (
		from  string
		where []string
		args  []any
	)
	if q.Match != "" {
		expression, err := buildMatch(q.Match)
		if err != nil {
			return nil, err
		}
		// The FTS5 table is not aliased: a MATCH operand and the hidden
		// rank column resolve against the table's own name, and an alias
		// makes SQLite read "alias MATCH ?" as a missing column.
		from = `FROM events_fts
			JOIN events e ON e.id = events_fts.rowid
			JOIN sessions s ON s.id = e.session_id`
		where = append(where, `events_fts MATCH ?`)
		args = append(args, expression)
	} else {
		from = `FROM events e JOIN sessions s ON s.id = e.session_id`
	}
	where, args = appendFilters(where, args, q)

	query := `SELECT ` + hitColumns + `
` + from + `
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ` + order + `
LIMIT ? OFFSET ?`
	args = append(args, limit, q.Offset)

	rows, err := x.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search index: %w", err)
	}
	defer rows.Close()

	var (
		hits []Hit
		ids  []any
		byID = map[int64]int{}
	)
	for rows.Next() {
		var (
			hit      Hit
			id       int64
			eventAt  sql.NullInt64
			digest   string
			line     int
			offset   int64
			pathText string
		)
		if err := rows.Scan(&hit.Harness, &hit.AdapterSchema, &hit.SourceID, &pathText,
			&id, &hit.Index, &hit.Kind, &hit.Role, &hit.Tool, &hit.Outcome, &eventAt,
			&hit.Partial, &line, &offset, &digest, &hit.Text); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		if eventAt.Valid {
			at := time.Unix(0, eventAt.Int64).UTC()
			hit.Time = &at
		}
		hit.Locator = event.Locator{Path: pathText, Line: line, ByteOffset: offset, Digest: digest}
		if hit.Locator.Digest == "" {
			// §4.3: evidence is never separable from its locator. A row
			// that lost its digest cannot recover its bytes, so it is
			// not a hit; the index is a cache and rebuilding it is the
			// repair.
			return nil, fmt.Errorf("search index: indexed event %d has no evidence locator", hit.Index)
		}
		byID[id] = len(hits)
		ids = append(ids, id)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read search results: %w", err)
	}
	if err := x.attachPaths(ctx, hits, ids, byID); err != nil {
		return nil, err
	}
	return hits, nil
}

// attachPaths fills in the repository paths of one page of hits with a
// single query, because a query per hit would make a page of results cost a
// round trip per row for the minority of events that name a path at all.
func (x *Index) attachPaths(ctx context.Context, hits []Hit, ids []any, byID map[int64]int) error {
	if len(ids) == 0 {
		return nil
	}
	query := `SELECT event_id, path FROM event_paths WHERE event_id IN (` +
		placeholders(len(ids)) + `) ORDER BY event_id, path`
	rows, err := x.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return fmt.Errorf("read hit paths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			path string
		)
		if err := rows.Scan(&id, &path); err != nil {
			return fmt.Errorf("scan hit path: %w", err)
		}
		if i, ok := byID[id]; ok {
			hits[i].Paths = append(hits[i].Paths, path)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read hit paths: %w", err)
	}
	return nil
}

// pageSize bounds one page. It takes the two numbers rather than a Query
// because the frontier surface asks the same question about the same bounds,
// and a second copy of the ceiling is a second ceiling.
func pageSize(limit, offset int) (int, error) {
	switch {
	case offset < 0:
		return 0, fmt.Errorf("%w: %d", ErrOffset, offset)
	case limit < 0 || limit > MaxLimit:
		return 0, fmt.Errorf("%w: %d, limit %d", ErrLimit, limit, MaxLimit)
	case limit == 0:
		return DefaultLimit, nil
	}
	return limit, nil
}

// orderClause resolves the ORDER BY. Every ordering ends in the session and
// event order so that ties are broken the same way on every run: a paged
// result whose ties reordered between pages would drop and duplicate hits.
func orderClause(q Query) (string, error) {
	switch q.Order {
	case OrderRelevance, "":
		if q.Match == "" {
			if q.Order == "" {
				// A structured-only browse has no ranking to fall back
				// on, so newest-first is the default rather than an
				// error.
				return `e.event_time IS NULL, e.event_time DESC, e.session_id, e.event_index`, nil
			}
			return "", ErrRelevanceWithoutMatch
		}
		return `events_fts.rank, e.session_id, e.event_index`, nil
	case OrderNewest:
		return `e.event_time IS NULL, e.event_time DESC, e.session_id, e.event_index`, nil
	case OrderOldest:
		return `e.event_time IS NULL, e.event_time ASC, e.session_id, e.event_index`, nil
	}
	return "", fmt.Errorf("%w: %q", ErrOrder, q.Order)
}

// appendFilters translates the structured filters into parameterized SQL.
// Column names come from this file and values only ever arrive as bound
// parameters.
func appendFilters(where []string, args []any, q Query) ([]string, []any) {
	// A clause is always present so the caller can join with AND
	// unconditionally, and an unfiltered browse is a legitimate query.
	where = append(where, `1 = 1`)

	where, args = appendAnyOf(where, args, `s.harness`, q.Harnesses)
	where, args = appendAnyOf(where, args, `s.source_id`, q.SourceIDs)
	where, args = appendAnyOf(where, args, `e.tool`, q.Tools)
	where, args = appendAnyOf(where, args, `e.outcome`, q.Outcomes)
	if len(q.Kinds) > 0 {
		kinds := make([]string, len(q.Kinds))
		for i, k := range q.Kinds {
			kinds[i] = string(k)
		}
		where, args = appendAnyOf(where, args, `e.kind`, kinds)
	}
	if q.RepositoryPath != "" {
		// Exact path, or anything under it. The upper bound replaces the
		// separator with its successor byte, which is what makes this a
		// range the path index can seek instead of a LIKE or GLOB whose
		// own metacharacters would have to be escaped out of a path.
		prefix := strings.TrimSuffix(q.RepositoryPath, "/")
		where = append(where, `EXISTS (SELECT 1 FROM event_paths ep
			WHERE ep.event_id = e.id
			AND (ep.path = ? OR (ep.path >= ? AND ep.path < ?)))`)
		args = append(args, prefix, prefix+"/", prefix+"0")
	}
	if !q.Since.IsZero() {
		where = append(where, `e.event_time IS NOT NULL AND e.event_time >= ?`)
		args = append(args, nanoBound(q.Since, math.MinInt64))
	}
	if !q.Until.IsZero() {
		where = append(where, `e.event_time IS NOT NULL AND e.event_time < ?`)
		args = append(args, nanoBound(q.Until, math.MaxInt64))
	}
	if q.Partial != nil {
		where = append(where, `e.partial = ?`)
		args = append(args, *q.Partial)
	}
	return where, args
}

// nanoBound converts a filter bound to the nanosecond epoch event times are
// stored in, saturating instead of wrapping. A bound outside the range that
// epoch can express means "from the beginning" or "until the end", which is
// what the caller asked for; time.Time.UnixNano would answer it with a
// wrapped instant.
func nanoBound(at time.Time, saturated int64) int64 {
	if !indexableTime(at) {
		return saturated
	}
	return at.UnixNano()
}

func appendAnyOf(where []string, args []any, column string, values []string) ([]string, []any) {
	if len(values) == 0 {
		return where, args
	}
	where = append(where, column+` IN (`+placeholders(len(values))+`)`)
	for _, v := range values {
		args = append(args, v)
	}
	return where, args
}

// placeholders renders n bound-parameter markers. It is generated from a
// count, never from data.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
