package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/frontier"
)

// This file is the frontier surface of the retrieval index: full-text search
// over Babel's own output, beside the full-text search over the corpus.
//
// It lives in this package and in this database file rather than in a second
// one, for two reasons that are the same reason. The FTS5 match grammar is
// buildMatch's, and it is the only thing in Babel that turns untrusted text
// into an FTS5 expression safely — a second copy of it somewhere else is the
// drift this repository has already paid for twice. And a frontier row is
// derived from the durable store exactly as an event row is derived from the
// corpus, so it belongs to the half of local state that may be discarded and
// rebuilt: losing it costs one reconcile, and holding it in the durable file
// would tie a cache's lifecycle to analysis nobody can recompute.
//
// Nothing here is authoritative about anything. A hit names a record and shows
// its text; the record in internal/frontier is what that text means, and
// §5.4's rule that retrieval rank is never evidence strength applies with more
// force here than it does to the corpus — the things being ranked are already
// somebody's claims.

// MaxFrontierTextBytes bounds the searchable text one frontier record
// contributes.
//
// It is far below MaxIndexedTextBytes and deliberately so. A corpus record is
// whatever a harness logged, up to megabytes of tool output; a frontier record
// is a statement, a claim, or a finding's pattern and significance, which are
// prose a person wrote or a model wrote for a person to read. Eight kilobytes
// is several pages of that. Anything past it is a payload that has stopped
// being a summary of an idea, and truncating its tail loses nothing a search
// needed: the record itself is one lookup away by the id every hit carries.
const MaxFrontierTextBytes = 8 << 10

// FrontierResult reports what one reconcile did.
//
// The four counts are separate because they answer different questions about
// the same pass. Added and Removed are the frontier moving — new analysis, and
// a wording that a revision superseded. Updated is a record whose derived text
// changed without its identity changing, which today means a candidate's
// status moved. Skipped is the incremental part working: an unchanged record
// costs a fingerprint comparison and no write, so re-indexing a frontier that
// did not change touches no FTS5 row at all.
type FrontierResult struct {
	Records int
	Added   int
	Updated int
	Removed int
	Skipped int
}

// IndexFrontier reconciles the index against the frontier's current output.
//
// The argument is the complete current set — frontier.Store.Outputs is what
// produces it — and not a page or a delta. That is a requirement rather than a
// convenience: this function deletes the rows the set does not name, because
// that is how a superseded wording stops being searchable, and a caller
// offering half the frontier would silently delete the other half. The cost of
// asking for all of it is one scan of the analysis tables, which hold
// thousands of records where the corpus index holds millions of events.
//
// Incremental by content, not by trust. Every row carries a fingerprint of
// what was indexed, so a record whose derived text is byte-identical to what
// is stored is skipped without touching the FTS5 index; anything else is
// deleted and re-inserted, which is also how the external-content triggers
// keep the inverted index consistent. One transaction, so a search never
// observes a frontier that is half old and half new.
func (x *Index) IndexFrontier(ctx context.Context, outputs []frontier.Output) (FrontierResult, error) {
	var res FrontierResult
	stored, err := x.frontierFingerprints(ctx)
	if err != nil {
		return res, err
	}

	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin frontier index transaction: %w", err)
	}
	defer tx.Rollback()

	offered := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		if output.ID == "" {
			return res, fmt.Errorf("frontier index: a %s output carries no id", output.Kind)
		}
		if _, twice := offered[output.ID]; twice {
			return res, fmt.Errorf("frontier index: record %q is offered twice", output.ID)
		}
		offered[output.ID] = struct{}{}
		row := newFrontierRow(output)
		prior, found := stored[output.ID]
		if found && prior == row.fingerprint {
			res.Skipped++
			continue
		}
		if found {
			if err := deleteFrontierRow(ctx, tx, output.ID); err != nil {
				return res, err
			}
			res.Updated++
		} else {
			res.Added++
		}
		if err := insertFrontierRow(ctx, tx, row); err != nil {
			return res, err
		}
	}
	for id := range stored {
		if _, kept := offered[id]; kept {
			continue
		}
		if err := deleteFrontierRow(ctx, tx, id); err != nil {
			return res, err
		}
		res.Removed++
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("commit frontier index: %w", err)
	}
	res.Records = len(offered)
	return res, nil
}

func (x *Index) frontierFingerprints(ctx context.Context) (map[string]string, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT record_id, fingerprint FROM frontier_records`)
	if err != nil {
		return nil, fmt.Errorf("read indexed frontier records: %w", err)
	}
	defer rows.Close()
	stored := map[string]string{}
	for rows.Next() {
		var id, fingerprint string
		if err := rows.Scan(&id, &fingerprint); err != nil {
			return nil, fmt.Errorf("scan indexed frontier record: %w", err)
		}
		stored[id] = fingerprint
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read indexed frontier records: %w", err)
	}
	return stored, nil
}

// frontierRow is one row about to be written, with the fingerprint of exactly
// the values it holds.
type frontierRow struct {
	recordID    string
	kind        string
	rootID      string
	subjectKind string
	subjectID   string
	runID       string
	status      string
	createdAt   int64
	fingerprint string
	summary     string
	text        string
}

// newFrontierRow flattens one output and fingerprints it.
//
// The fingerprint covers every stored column, length-prefixed, so no two
// different rows hash the same way — the same reason internal/run's
// preparation identity is length-prefixed. It covers the text after
// truncation, because what is compared has to be what was indexed: a record
// whose payload grew past the bound without changing its first eight kilobytes
// indexes to the same row and is correctly skipped.
func newFrontierRow(output frontier.Output) frontierRow {
	row := frontierRow{
		recordID:    output.ID,
		kind:        string(output.Kind),
		rootID:      output.RootID,
		subjectKind: string(output.Subject.Type),
		subjectID:   output.Subject.ID,
		runID:       output.RunID,
		status:      string(output.Status),
		createdAt:   output.CreatedAt.UTC().UnixNano(),
		summary:     output.Summary,
		text:        clipUTF8(output.Text, MaxFrontierTextBytes),
	}
	if row.rootID == "" {
		row.rootID = row.recordID
	}
	h := sha256.New()
	for _, field := range []string{row.recordID, row.kind, row.rootID, row.subjectKind,
		row.subjectID, row.runID, row.status, row.summary, row.text} {
		fmt.Fprintf(h, "%d:%s", len(field), field)
	}
	fmt.Fprintf(h, "%d", row.createdAt)
	row.fingerprint = hex.EncodeToString(h.Sum(nil))
	return row
}

func insertFrontierRow(ctx context.Context, tx *sql.Tx, row frontierRow) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_records(
		record_id, kind, root_id, subject_kind, subject_id, run_id, status,
		created_at, fingerprint, summary, text)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.recordID, row.kind, row.rootID, row.subjectKind, row.subjectID, row.runID,
		row.status, row.createdAt, row.fingerprint, row.summary, row.text); err != nil {
		return fmt.Errorf("index frontier record %s: %w", row.recordID, err)
	}
	return nil
}

func deleteFrontierRow(ctx context.Context, tx *sql.Tx, recordID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM frontier_records WHERE record_id = ?`, recordID); err != nil {
		return fmt.Errorf("remove indexed frontier record %s: %w", recordID, err)
	}
	return nil
}

// FrontierQuery is one self-retrieval request.
//
// It is a deliberately smaller surface than Query. There is no session filter,
// because a frontier record is about the corpus rather than in it; no
// repository path, for the same reason; and no partial filter, because a
// durable record is never torn — the store refuses a malformed one instead of
// storing a degraded one.
type FrontierQuery struct {
	// Match is a full-text expression in buildMatch's grammar, identical to
	// Query.Match: whitespace separates optional terms, a quoted phrase
	// matches adjacently, a leading - excludes, a trailing * is a prefix,
	// and everything else is data. Empty browses by structure alone.
	Match string

	// Kinds restricts the surfaces searched. Empty searches all of them,
	// which is what a run looking for prior work on a subject wants: a
	// finding, the observations under it and the operator's answer about it
	// are three different things to have already been said.
	Kinds []frontier.OutputKind
	// Statuses restricts candidate lifecycle state. It is an any-of filter
	// over hypotheses and matches nothing else, because no other kind has a
	// status — so naming one narrows the search to candidates.
	Statuses []frontier.Status

	Order  Order
	Limit  int
	Offset int
}

// FrontierHit is one retrieved frontier record.
//
// There is no score here either, and the omission matters more than it does
// for a corpus hit: a corpus hit is a record of something that happened, while
// this is a claim somebody made, and a number beside it that looked like
// confidence would be read as one.
type FrontierHit struct {
	Kind frontier.OutputKind
	ID   string
	// RootID is the chain identity, so a caller can tell two hits that are
	// two wordings of one candidate from two candidates.
	RootID string
	// Subject is the record a review answer answers about, zero otherwise.
	Subject frontier.Ref
	RunID   string
	Status  frontier.Status

	CreatedAt time.Time
	Summary   string
	// Text is the indexed text, bounded by MaxFrontierTextBytes. Truncated
	// says the bound was reached, so a reader cannot honestly claim the
	// record says no more than this.
	Text      string
	Truncated bool
}

// frontierColumns is the projection every frontier search shares.
const frontierColumns = `f.record_id, f.kind, f.root_id, f.subject_kind, f.subject_id,
	f.run_id, f.status, f.created_at, f.summary, f.text`

// FrontierSearch runs one query over Babel's own output.
//
// An empty result is an answer and not an error: it says the frontier holds
// nothing matching, which is exactly what a run needs to know before minting a
// candidate. A short page means the matches are exhausted.
func (x *Index) FrontierSearch(ctx context.Context, q FrontierQuery) ([]FrontierHit, error) {
	limit, err := pageSize(q.Limit, q.Offset)
	if err != nil {
		return nil, err
	}
	order, err := frontierOrderClause(q)
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
		// The FTS5 table is unaliased for the reason Search's is: MATCH and
		// the hidden rank column resolve against the table's own name.
		from = `FROM frontier_fts JOIN frontier_records f ON f.id = frontier_fts.rowid`
		where = append(where, `frontier_fts MATCH ?`)
		args = append(args, expression)
	} else {
		from = `FROM frontier_records f`
	}
	where = append(where, `1 = 1`)
	if len(q.Kinds) > 0 {
		kinds := make([]string, 0, len(q.Kinds))
		for _, kind := range q.Kinds {
			if !frontier.ValidOutputKind(kind) {
				return nil, fmt.Errorf("%w: %q", ErrFrontierKind, kind)
			}
			kinds = append(kinds, string(kind))
		}
		where, args = appendAnyOf(where, args, `f.kind`, kinds)
	}
	if len(q.Statuses) > 0 {
		statuses := make([]string, 0, len(q.Statuses))
		for _, status := range q.Statuses {
			statuses = append(statuses, string(status))
		}
		where, args = appendAnyOf(where, args, `f.status`, statuses)
	}

	query := `SELECT ` + frontierColumns + `
` + from + `
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY ` + order + `
LIMIT ? OFFSET ?`
	args = append(args, limit, q.Offset)

	rows, err := x.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search frontier index: %w", err)
	}
	defer rows.Close()
	var hits []FrontierHit
	for rows.Next() {
		var (
			hit         FrontierHit
			kind        string
			subjectKind string
			status      string
			createdAt   int64
		)
		if err := rows.Scan(&hit.ID, &kind, &hit.RootID, &subjectKind, &hit.Subject.ID,
			&hit.RunID, &status, &createdAt, &hit.Summary, &hit.Text); err != nil {
			return nil, fmt.Errorf("scan frontier search result: %w", err)
		}
		hit.Kind = frontier.OutputKind(kind)
		hit.Subject.Type = frontier.EntityType(subjectKind)
		hit.Status = frontier.Status(status)
		hit.CreatedAt = time.Unix(0, createdAt).UTC()
		hit.Truncated = len(hit.Text) >= MaxFrontierTextBytes
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read frontier search results: %w", err)
	}
	return hits, nil
}

// frontierOrderClause resolves the ORDER BY, ending every ordering in the
// record id so ties break identically on every page.
func frontierOrderClause(q FrontierQuery) (string, error) {
	switch q.Order {
	case OrderRelevance, "":
		if q.Match == "" {
			if q.Order == "" {
				return `f.created_at DESC, f.record_id`, nil
			}
			return "", ErrRelevanceWithoutMatch
		}
		return `frontier_fts.rank, f.record_id`, nil
	case OrderNewest:
		return `f.created_at DESC, f.record_id`, nil
	case OrderOldest:
		return `f.created_at ASC, f.record_id`, nil
	}
	return "", fmt.Errorf("%w: %q", ErrOrder, q.Order)
}

// clipUTF8 bounds text at max without splitting a rune, because half a rune is
// invalid UTF-8 and SQLite's TEXT storage and FTS5's tokenizer both require
// valid UTF-8.
func clipUTF8(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
