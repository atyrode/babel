package reference

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	babelsync "github.com/atyrode/babel/internal/sync"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the durable database's shared
// migration ledger, beside internal/frontier's, internal/run's,
// internal/disposition's, internal/review's, internal/reality's and
// internal/sync's. Keying migrations by component is what lets another one join
// the file without a coordinating global version.
const component = "reference"

// databaseFile is the durable, pending-sync database. The reference graph joins
// it rather than taking a file of its own because it is not rebuildable:
// nothing can re-derive that a run cited this session as evidence for that
// finding, and the edge is the only record of it. The retrieval index stays
// separate for the opposite reason.
const databaseFile = "durable.db"

// RecordSchema is this package's record schema version, published with every
// edge so a reader on another host knows which shape it is decoding.
const RecordSchema = 1

// DefaultEdgeLimit and MaxEdgeLimit bound one listing.
//
// A zero limit means DefaultEdgeLimit rather than everything, for the reason
// internal/frontier's ListFilter gives: a record that has accumulated thousands
// of citations must not arrive whole because a render surface forgot a bound.
// The Lister contract has no limit argument on purpose - a surface asking "what
// does this record cite" is asking for the page it can show - and a caller that
// needs more pages says so through Query.
const (
	DefaultEdgeLimit = 200
	MaxEdgeLimit     = 1000
)

// migrations are applied in order and never edited once released; §9 requires
// local SQLite migrations to be forward and transactional.
//
// The shape mirrors the §9 split the durable file's other components use: one
// payload_json column carrying the half that is sealed on publication, and
// otherwise only identifiers, kinds, a schema version and a timestamp.
//
// Two things are load-bearing beyond that. The UNIQUE over
// (edge_kind, from_kind, from_id, to_kind, to_id) is what makes a re-append
// idempotent in the database rather than in this package's head: emitters
// retry, and two rows asserting the same citation would make "how many times
// was this asserted" a question the graph answers wrongly. And immutability is
// enforced by triggers rather than by this package's SQL, because an
// append-only history that depends on nobody writing an UPDATE is append-only
// by convention - a wrong edge is answered by a later edge, never by editing
// the one that was written.
var migrations = []string{`
CREATE TABLE reference_edge(
	id             TEXT PRIMARY KEY,
	edge_kind      TEXT NOT NULL,
	from_kind      TEXT NOT NULL,
	from_id        TEXT NOT NULL,
	to_kind        TEXT NOT NULL,
	to_id          TEXT NOT NULL,
	actor_kind     TEXT NOT NULL,
	actor_ref      TEXT,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	UNIQUE(edge_kind, from_kind, from_id, to_kind, to_id)
);

CREATE INDEX reference_edge_from ON reference_edge(from_kind, from_id, created_at);
CREATE INDEX reference_edge_to ON reference_edge(to_kind, to_id, created_at);

CREATE TRIGGER reference_edge_immutable BEFORE UPDATE ON reference_edge
BEGIN SELECT RAISE(ABORT, 'an edge is immutable: a wrong link is answered by a later edge, never by rewriting one'); END;

CREATE TRIGGER reference_edge_append_only BEFORE DELETE ON reference_edge
BEGIN SELECT RAISE(ABORT, 'edges are never deleted; a citation that can be removed is one nobody has to answer for'); END;
`}

// Store is the durable, append-only edge store: the concrete Appender and
// Lister the frozen contract in reference.go describes.
//
// It exposes no operation that removes or rewrites an edge. Every write is an
// insert, and a repeat of one is the insert that already happened.
type Store struct {
	db   *sql.DB
	path string

	// now supplies edge timestamps. It is a field so a test can make ordering
	// deterministic without sleeping; production never replaces it.
	now func() time.Time

	// resolvers is the anchoring gate. A nil registry refuses every append,
	// which is the correct direction to fail; see resolver.go.
	resolvers *Registry

	// sync stages every edge for the shared catalog inside the transaction
	// that writes it. Nil is local-only mode, which is the default and a
	// supported deployment; see publish.go.
	sync babelsync.Hook
}

// Option configures a Store at Open time. Every option is optional and the
// zero configuration is meaningful: a local-only store with no publication and
// no resolvers, which reads back everything it is given and accepts nothing.
type Option func(*Store)

// WithClock replaces the timestamp source, so a test can order edges within a
// microsecond without sleeping.
func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// WithResolvers attaches the resolver registry every append is validated
// against. Without it no append succeeds, because there is nothing that can
// vouch for an endpoint.
func WithResolvers(r *Registry) Option {
	return func(s *Store) { s.resolvers = r }
}

// WithSync attaches the Phase B publication hook. Without it - the default -
// this is a purely local durable store: nothing is staged and nothing is
// published, and no write path behaves differently.
func WithSync(h babelsync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// Open opens the durable database in dir, creating the directory and applying
// any pending migrations.
//
// A schema or integrity surprise is returned as an error and never resolved by
// discarding the file, on the same terms as the frontier's: these rows are the
// only record that one Babel record cites another.
func Open(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reference: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("reference: open durable database %s: %w", databaseFile, err)
	}
	// One connection: the pragmas below are per-connection, and the durable
	// file has a single writer per §9's local state-writer lock invariant.
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(store)
	}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	if store.sync != nil {
		// The staging journal is a component of this same durable file, and
		// this store stages on its own connection, so its tables have to exist
		// here before any write path opens a transaction. It is cheap and
		// idempotent.
		if err := babelsync.EnsureSchema(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) init() error {
	var integrity string
	if err := s.db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("reference: check durable database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("reference: check durable database integrity: %s", integrity)
	}
	// WAL keeps a reader - the web surface rendering backlinks - unblocked
	// while a write commits, and busy_timeout absorbs the brief overlap. All
	// are per connection; journal_mode persists in the file.
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("reference: enable durable database WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("reference: set durable database busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("reference: enable durable database foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(component TEXT PRIMARY KEY, version INTEGER)`); err != nil {
		return fmt.Errorf("reference: create durable migration table: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reference: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("reference: schema version %d is newer than this build supports (%d)",
			version, len(migrations))
	}
	for next := version; next < len(migrations); next++ {
		if err := s.applyMigration(next); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and records its version in the same
// transaction, so an interrupted upgrade leaves the file at a version that
// matches its actual shape.
func (s *Store) applyMigration(index int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("reference: begin migration %d: %w", index+1, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("reference: apply migration %d: %w", index+1, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version`, component, index+1); err != nil {
		return fmt.Errorf("reference: record migration %d: %w", index+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reference: commit migration %d: %w", index+1, err)
	}
	return nil
}

// Close releases the database handle. A nil store closes nothing, so a caller
// that degraded to no edge store needs no branch around its cleanup.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// Path reports the durable database path, which an operator needs when the
// pending-sync journal has to be backed up before a machine is rebuilt.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Append validates, persists, and returns the edge with its minted id and
// creation time, implementing Appender.
//
// The order of the three refusals is the design. Shape first, because a
// malformed edge is a caller bug that costs nothing to catch. Then both
// endpoints against the resolver registry, before any transaction is open: a
// resolver reads another component's tables on another connection, and holding
// this store's write transaction while doing so would be a lock held across a
// call into code that knows nothing about it. Only then the write.
//
// Resolving outside the transaction is sound because existence here is
// monotone. Every store this validates against is append-only - §4.7's rule
// that rejection never deletes - so a record that exists cannot stop existing
// between the check and the insert, and the only race the wider window admits
// is an endpoint created concurrently, which is a refusal that a retry turns
// into a success.
//
// Appending an edge identical in (Kind, From, To) to one already recorded
// returns that edge unchanged and stages nothing. That is the contract's
// idempotency, and it is answered from the row rather than from the request: an
// emitter retrying after a crash gets the id and the timestamp the first
// attempt minted, which is what makes the retry indistinguishable from the
// original write to everything downstream.
func (s *Store) Append(ctx context.Context, e Edge) (Edge, error) {
	if s == nil {
		return Edge{}, ErrNotConfigured
	}
	if err := e.Validate(); err != nil {
		return Edge{}, fmt.Errorf("%w: %s", ErrInvalidValue, err)
	}
	if len(e.Note) > maxNoteLen {
		return Edge{}, fmt.Errorf("%w: edge note is %d characters, over the %d-character bound",
			ErrInvalidValue, len(e.Note), maxNoteLen)
	}
	if len(e.ActorRef) > maxIDLen {
		return Edge{}, fmt.Errorf("%w: actor reference is %d characters, over the %d-character bound",
			ErrInvalidValue, len(e.ActorRef), maxIDLen)
	}
	if err := s.resolvers.require(ctx, "from", e.From); err != nil {
		return Edge{}, err
	}
	if err := s.resolvers.require(ctx, "to", e.To); err != nil {
		return Edge{}, err
	}

	id, err := newID("ref")
	if err != nil {
		return Edge{}, err
	}
	record := Edge{
		ID:        id,
		Kind:      e.Kind,
		From:      e.From,
		To:        e.To,
		ActorKind: e.ActorKind,
		ActorRef:  e.ActorRef,
		Note:      e.Note,
		CreatedAt: s.now(),
	}
	payload, err := marshalPayload(record)
	if err != nil {
		return Edge{}, err
	}

	var (
		pub      publication
		existing *Edge
	)
	err = s.transact(ctx, func(tx *sql.Tx) error {
		found, err := s.edgeByEndpoints(ctx, tx, e.Kind, e.From, e.To)
		switch {
		case err != nil:
			return err
		case found != nil:
			existing = found
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reference_edge(
			id, edge_kind, from_kind, from_id, to_kind, to_id,
			actor_kind, actor_ref, schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, string(record.Kind),
			record.From.Kind, record.From.ID, record.To.Kind, record.To.ID,
			record.ActorKind, nullableID(record.ActorRef), RecordSchema,
			formatTime(record.CreatedAt), payload); err != nil {
			return fmt.Errorf("reference: insert edge: %w", err)
		}
		staged, err := stagedEdge(record, payload)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, producingRun(record), staged)
		return err
	})
	if err != nil {
		return Edge{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	if err := s.commit(ctx, pub); err != nil {
		return Edge{}, err
	}
	return record, nil
}

// Direction names which end of an edge a query fixes: the citing record, or the
// cited one.
type Direction string

const (
	// DirectionFrom asks what a record cites - its outgoing links.
	DirectionFrom Direction = "from"
	// DirectionTo asks what cites a record - its backlinks. It is the same
	// table read from the other side, which is why there is no separate
	// backlink index to fall out of step with the edges.
	DirectionTo Direction = "to"
)

// Query is one bounded listing of a record's edges.
type Query struct {
	// Ref is the record the listing is about, and Direction which of its two
	// questions is being asked.
	Ref       RecordRef
	Direction Direction
	// Kinds narrows to relation kinds. Empty means every kind, which is what a
	// record page shows.
	Kinds []Kind
	// Limit bounds the page; zero means DefaultEdgeLimit and anything above
	// MaxEdgeLimit is reduced to it.
	Limit int
}

// From lists the edges that start at ref, newest first, implementing Lister.
func (s *Store) From(ctx context.Context, ref RecordRef) ([]Edge, error) {
	return s.List(ctx, Query{Ref: ref, Direction: DirectionFrom})
}

// To lists the edges that end at ref - the backlinks - newest first,
// implementing Lister.
func (s *Store) To(ctx context.Context, ref RecordRef) ([]Edge, error) {
	return s.List(ctx, Query{Ref: ref, Direction: DirectionTo})
}

// List answers one bounded query.
//
// A nil store returns nothing and no error, which is what a render surface
// wired without an edge store shows: a record with no links section rather
// than an error page. That is the degrade the injection contract asks for, and
// it is safe precisely because the absence of a store and the absence of edges
// look the same to a reader either way.
//
// Ordering is creation time descending, then id descending: newest first, and
// total, so two edges written inside one microsecond still page deterministically.
func (s *Store) List(ctx context.Context, q Query) ([]Edge, error) {
	if s == nil {
		return nil, nil
	}
	if q.Ref.Kind == "" || q.Ref.ID == "" {
		return nil, fmt.Errorf("%w: a listing names one record", ErrInvalidValue)
	}
	var kindColumn, idColumn string
	switch q.Direction {
	case DirectionFrom:
		kindColumn, idColumn = "from_kind", "from_id"
	case DirectionTo:
		kindColumn, idColumn = "to_kind", "to_id"
	default:
		return nil, fmt.Errorf("%w: edge direction %q", ErrInvalidValue, q.Direction)
	}

	where := []string{kindColumn + " = ?", idColumn + " = ?"}
	args := []any{q.Ref.Kind, q.Ref.ID}
	if len(q.Kinds) > 0 {
		placeholders := make([]string, len(q.Kinds))
		for i, kind := range q.Kinds {
			if !kind.Valid() {
				return nil, fmt.Errorf("%w: edge kind %q", ErrInvalidValue, kind)
			}
			placeholders[i], args = "?", append(args, string(kind))
		}
		where = append(where, "edge_kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	args = append(args, boundLimit(q.Limit))

	rows, err := s.db.QueryContext(ctx, `SELECT `+edgeColumns+`
		FROM reference_edge WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("reference: list edges %s of %s: %w", q.Direction, q.Ref, err)
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		edge, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reference: list edges %s of %s: %w", q.Direction, q.Ref, err)
	}
	return out, nil
}

// Edge reads one edge by id, which is what a surface rendering a single
// citation and a test asserting on one both need.
func (s *Store) Edge(ctx context.Context, id string) (Edge, error) {
	if s == nil {
		return Edge{}, ErrNotConfigured
	}
	edge, err := scanEdge(s.db.QueryRowContext(ctx,
		`SELECT `+edgeColumns+` FROM reference_edge WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Edge{}, fmt.Errorf("%w: edge %q", ErrNoSuchTarget, id)
	}
	return edge, err
}

const edgeColumns = `id, edge_kind, from_kind, from_id, to_kind, to_id,
	actor_kind, COALESCE(actor_ref, ''), created_at, payload_json`

// scanEdge reads one row.
//
// The note comes off payload_json rather than a column of its own, because the
// payload is the §9 encryption-bound half and the note is the whole of it: a
// second copy in a plaintext column would be the copy that reached the shared
// catalog by accident.
func scanEdge(row interface{ Scan(...any) error }) (Edge, error) {
	var (
		edge      Edge
		kind      string
		createdAt string
		payload   []byte
	)
	if err := row.Scan(&edge.ID, &kind, &edge.From.Kind, &edge.From.ID,
		&edge.To.Kind, &edge.To.ID, &edge.ActorKind, &edge.ActorRef,
		&createdAt, &payload); err != nil {
		return Edge{}, err
	}
	edge.Kind = Kind(kind)
	created, err := parseTime(createdAt)
	if err != nil {
		return Edge{}, fmt.Errorf("reference: edge %s: %w", edge.ID, err)
	}
	edge.CreatedAt = created
	note, err := unmarshalNote(payload)
	if err != nil {
		return Edge{}, fmt.Errorf("reference: edge %s: %w", edge.ID, err)
	}
	edge.Note = note
	return edge, nil
}

// edgeByEndpoints reads the edge already recorded for one (kind, from, to), or
// nil. It is the read half of idempotency, and it runs inside the caller's
// transaction so the answer cannot change between the check and the insert.
func (s *Store) edgeByEndpoints(ctx context.Context, tx *sql.Tx, kind Kind, from, to RecordRef) (*Edge, error) {
	edge, err := scanEdge(tx.QueryRowContext(ctx, `SELECT `+edgeColumns+`
		FROM reference_edge
		 WHERE edge_kind = ? AND from_kind = ? AND from_id = ? AND to_kind = ? AND to_id = ?`,
		string(kind), from.Kind, from.ID, to.Kind, to.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reference: read existing edge: %w", err)
	}
	return &edge, nil
}

// transact runs fn in one transaction, rolling back on any error. Every write
// goes through it, because an edge and its staged publication row are one
// event: a durable edge nothing owes the fleet would be invisible to every
// other machine with nothing saying so.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reference: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reference: commit transaction: %w", err)
	}
	return nil
}

// boundLimit applies the page bounds. A caller that passes nothing gets one
// page; a caller that passes more than the ceiling gets the ceiling, silently,
// because a listing bound is a protection rather than a request to honour.
func boundLimit(limit int) int {
	if limit <= 0 {
		return DefaultEdgeLimit
	}
	if limit > MaxEdgeLimit {
		return MaxEdgeLimit
	}
	return limit
}

// newID generates a globally unique client-side identifier, which §9 requires
// for immutable entities. It is the shape internal/frontier and internal/reality
// mint - a kind prefix and 128 random bits - rather than a second convention:
// the prefix makes a stray id in a log self-describing, and the width is what
// makes a client-generated id safe as a global primary key without coordination.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("reference: generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// nullableID stores an absent actor reference as SQL NULL, so "no run asserted
// this" is absent rather than an empty string a later reader has to know to
// treat as absent.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction, the
// layout every component of the durable file uses. The fixed width is what
// makes lexicographic order and chronological order the same relation, which
// this package depends on: newest-first is an ORDER BY over this column.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
