// Package title is the durable store of model-inferred session titles.
//
// It exists because an inferred title is the one piece of session metadata
// Babel cannot recompute. Every other row in the local catalog is derivable
// from the live sources — that is why internal/catalog may discard and
// rebuild its whole database on a schema surprise — but a title a model wrote
// exists only here, and it cost the operator money. Losing it is not a
// re-scan; it is a second bill.
//
// So these rows join the durable database internal/frontier, internal/run and
// internal/reality already share: one `durable.db`, one
// `schema_migration(component, version)` ledger keyed by component, and a
// `title_` table prefix. A schema version this build does not recognize is an
// error and never a rebuild, for the same reason it is one there.
//
// The store is deliberately not a cache of titles in general. A recorded title
// is read from the harness's own files on every describe, and a derived one is
// recomputed from them for free; caching either here would create a second
// authority that could disagree with the session. Only the value that has no
// other source is kept.
package title

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// storeComponent names this package's migrations in the shared ledger.
const storeComponent = "title"

// storeVersion is this component's schema version.
const storeVersion = 1

// DatabaseName is the durable database file, shared with the other durable
// components in Babel's private local state directory.
const DatabaseName = "durable.db"

// MaxTitleRunes bounds a stored title. It matches what the source adapters
// produce, so an inferred title and a derived one occupy the same column
// width and a listing does not reflow depending on which it holds.
const MaxTitleRunes = 72

// maxModelRunes bounds the recorded model identity. It is a label a titler
// reports about itself, so it is untrusted and bounded on the way in.
const maxModelRunes = 128

// ErrInvalid reports a title or identity this store refuses to hold.
var ErrInvalid = errors.New("title: invalid inferred title")

// Inferred is one model-written title and the disclosure facts that explain
// it. Every field but the title itself is there to answer "why does this
// session have a title the harness never wrote", which is a question an
// operator will ask months later about a value he paid for.
type Inferred struct {
	// Selector is the session's local identity, "HARNESS/SOURCE-ID".
	Selector string
	// Title is the model's text, bounded and single-line.
	Title string
	// Titler is the argv[0] of the command that produced it, and Model the
	// identity that command reported. Both are recorded because the same
	// session titled by two different models is two different values, and a
	// row that cannot say which produced it cannot be re-judged.
	Titler string
	Model  string
	// InferredAt is when the value was recorded here.
	InferredAt time.Time
}

// Store is the durable inferred-title database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens the durable database in dir, creating the directory and this
// component's table if they are absent.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("title: create state directory: %w", err)
	}
	path := filepath.Join(dir, DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("title: open durable database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database's path.
func (s *Store) Path() string { return s.path }

// schema is this component's table.
//
// The primary key is the selector, so re-inferring a session's title replaces
// the previous value rather than accumulating history. That is the right shape
// here and not merely the simple one: unlike a run receipt, an inferred title
// is not evidence of anything — it is a display value the operator is free to
// regenerate with a better model, and keeping every superseded guess would
// leave two answers to "what is this session called" with nothing to choose
// between them. What the row does retain is which titler and model produced
// the value it holds, so the current answer is always attributable.
const schema = `
CREATE TABLE IF NOT EXISTS title_inferred(
	selector    TEXT PRIMARY KEY,
	title       TEXT NOT NULL,
	titler      TEXT NOT NULL,
	model       TEXT NOT NULL,
	inferred_at TEXT NOT NULL
);`

func (s *Store) migrate() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("title: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("title: set durable busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("title: create migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`,
		storeComponent).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(schema); err != nil {
			return fmt.Errorf("title: create durable schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			storeComponent, storeVersion); err != nil {
			return fmt.Errorf("title: record durable schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("title: read durable schema version: %w", err)
	case version != storeVersion:
		return fmt.Errorf("title: durable schema version %d is not supported by this build", version)
	}
	return nil
}

// Put records one inferred title, replacing any previous value for the same
// selector.
//
// Validation happens here rather than at the call site because this is the
// boundary a titler's output crosses into durable state, and a titler is an
// external command whose output is untrusted: it may return a multi-line
// essay, an empty string, or a title for a session that was never sent. The
// caller checks the last of those — only it knows what it sent — and the first
// two are checked here, where every future caller gets them for free.
func (s *Store) Put(ctx context.Context, in Inferred) error {
	title, err := Normalize(in.Title)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Selector) == "" {
		return fmt.Errorf("%w: no selector", ErrInvalid)
	}
	titler := boundLabel(in.Titler)
	model := boundLabel(in.Model)
	if titler == "" {
		return fmt.Errorf("%w: no titler identity", ErrInvalid)
	}
	at := in.InferredAt
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO title_inferred(
		selector, title, titler, model, inferred_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(selector) DO UPDATE SET
			title=excluded.title, titler=excluded.titler,
			model=excluded.model, inferred_at=excluded.inferred_at`,
		in.Selector, title, titler, model, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("title: store inferred title for %q: %w", in.Selector, err)
	}
	return nil
}

// All returns every inferred title keyed by selector.
//
// The whole table is read at once because that is how it is used: a session
// listing overlays inferred titles onto thousands of rows, and one query per
// row would make the overlay cost scale with the corpus to save reading a
// table that holds only the sessions the operator chose to pay for.
func (s *Store) All(ctx context.Context) (map[string]Inferred, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT selector, title, titler, model, inferred_at FROM title_inferred`)
	if err != nil {
		return nil, fmt.Errorf("title: read inferred titles: %w", err)
	}
	defer rows.Close()

	out := map[string]Inferred{}
	for rows.Next() {
		var in Inferred
		var at string
		if err := rows.Scan(&in.Selector, &in.Title, &in.Titler, &in.Model, &at); err != nil {
			return nil, fmt.Errorf("title: scan inferred title: %w", err)
		}
		// An unparsable timestamp leaves the time zero rather than failing the
		// read: the title is the value worth having, and it is not lost
		// because a clock string is malformed.
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			in.InferredAt = t
		}
		out[in.Selector] = in
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("title: read inferred titles: %w", err)
	}
	return out, nil
}

// Delete removes one selector's inferred title, returning whether a row went.
//
// This exists because an inferred title is a guess the operator may judge
// worse than the derived one, and a value he cannot withdraw is a value he
// will hesitate to generate. Deleting it restores the derived title, because
// the derivation was never replaced — only overlaid.
func (s *Store) Delete(ctx context.Context, selector string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM title_inferred WHERE selector = ?`, selector)
	if err != nil {
		return false, fmt.Errorf("title: delete inferred title for %q: %w", selector, err)
	}
	n, err := res.RowsAffected()
	return err == nil && n > 0, nil
}

// Normalize states an external titler's output as a storable title: one line,
// bounded, non-empty. It is exported because the CLI reports a rejected title
// before storing anything, and reporting one rule and enforcing another is how
// the two come to disagree.
func Normalize(raw string) (string, error) {
	var b strings.Builder
	b.Grow(len(raw))
	space := false
	for _, r := range raw {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "", fmt.Errorf("%w: empty after normalization", ErrInvalid)
	}
	if utf8.RuneCountInString(out) > MaxTitleRunes {
		return "", fmt.Errorf("%w: %d runes exceeds the %d-rune bound",
			ErrInvalid, utf8.RuneCountInString(out), MaxTitleRunes)
	}
	return out, nil
}

// boundLabel collapses and bounds an identity a titler reported about itself.
func boundLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxModelRunes {
		s = string([]rune(s)[:maxModelRunes])
	}
	return s
}
