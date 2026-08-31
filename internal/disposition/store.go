package disposition

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the durable database's shared
// migration ledger, beside internal/frontier's, internal/run's,
// internal/review's and internal/explore's. A new durable component joins the
// one file with its own component key and table prefix rather than opening a
// second, because the point of one durable file is that a single pending-sync
// journal is what an operator has to preserve.
const component = "disposition"

// storeVersion is this component's schema version.
const storeVersion = 1

// databaseFile is the durable, pending-sync database, named the way every
// other component of it names it. A disposition ledger is not derivable from
// anything: it is the record of what a person decided, so it belongs with the
// analysis rather than in the rebuildable cache.
const databaseFile = run.DatabaseName

// schema is this component's tables and its append-only enforcement.
//
// The §9 split is mirrored per table — one payload_json column holding what a
// later sync slice seals, everything else drawn from the plaintext allowlist —
// with one deliberate exception. disposition_invitation has no payload column
// at all, because #87's invitation carries no operator words: a column for
// them would be a place for an instruction to appear in a later release, and
// the whole distinction between an invitation and a brief is that there is
// nowhere to put one.
//
// Consumption is a second table rather than a column on the invitation. A run
// taking an invitation would otherwise have to UPDATE the row an operator
// wrote, which the immutability triggers refuse and which would make the
// operator's record and the run's claim about it one row that two parties
// write. As its own table with the invitation as primary key, consume-once is
// the primary key doing its job.
const schema = `
CREATE TABLE IF NOT EXISTS disposition_proposal(
	id             TEXT PRIMARY KEY,
	record_type    TEXT NOT NULL,
	record_id      TEXT NOT NULL,
	kind           TEXT NOT NULL,
	proposer_kind  TEXT NOT NULL,
	proposer_id    TEXT NOT NULL,
	emitted_ref    TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS disposition_proposal_record
	ON disposition_proposal(record_type, record_id);
CREATE UNIQUE INDEX IF NOT EXISTS disposition_proposal_emitted
	ON disposition_proposal(proposer_id, emitted_ref) WHERE emitted_ref != '';

CREATE TABLE IF NOT EXISTS disposition_ledger(
	id             TEXT PRIMARY KEY,
	disposition_id TEXT NOT NULL REFERENCES disposition_proposal(id),
	seq            INTEGER NOT NULL,
	ruling         TEXT NOT NULL,
	operator_id    TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	recorded_at    TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	UNIQUE(disposition_id, seq)
);

CREATE TABLE IF NOT EXISTS disposition_invitation(
	id          TEXT PRIMARY KEY,
	record_type TEXT NOT NULL,
	record_id   TEXT NOT NULL,
	operator_id TEXT NOT NULL,
	created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS disposition_invitation_record
	ON disposition_invitation(record_type, record_id);

CREATE TABLE IF NOT EXISTS disposition_invitation_consumption(
	invitation_id TEXT PRIMARY KEY REFERENCES disposition_invitation(id),
	run_id        TEXT NOT NULL,
	consumed_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS disposition_invitation_consumption_run
	ON disposition_invitation_consumption(run_id);

CREATE TRIGGER IF NOT EXISTS disposition_proposal_immutable BEFORE UPDATE ON disposition_proposal
BEGIN SELECT RAISE(ABORT, 'a proposed action is immutable; decline it and propose the corrected one'); END;
CREATE TRIGGER IF NOT EXISTS disposition_proposal_kept BEFORE DELETE ON disposition_proposal
BEGIN SELECT RAISE(ABORT, 'proposed actions are never deleted; declining one leaves it readable'); END;

CREATE TRIGGER IF NOT EXISTS disposition_ledger_immutable BEFORE UPDATE ON disposition_ledger
BEGIN SELECT RAISE(ABORT, 'the disposition ledger is append-only; reconsidering appends another entry'); END;
CREATE TRIGGER IF NOT EXISTS disposition_ledger_kept BEFORE DELETE ON disposition_ledger
BEGIN SELECT RAISE(ABORT, 'the disposition ledger is append-only; it is the provenance later evaluation reads'); END;

CREATE TRIGGER IF NOT EXISTS disposition_invitation_immutable BEFORE UPDATE ON disposition_invitation
BEGIN SELECT RAISE(ABORT, 'an invitation is immutable; consumption is recorded beside it, never onto it'); END;
CREATE TRIGGER IF NOT EXISTS disposition_invitation_kept BEFORE DELETE ON disposition_invitation
BEGIN SELECT RAISE(ABORT, 'invitations are never deleted; a consumed one records which run answered it'); END;

CREATE TRIGGER IF NOT EXISTS disposition_invitation_consumption_immutable
BEFORE UPDATE ON disposition_invitation_consumption
BEGIN SELECT RAISE(ABORT, 'an invitation is consumed once; a second run cannot take it from the first'); END;
CREATE TRIGGER IF NOT EXISTS disposition_invitation_consumption_kept
BEFORE DELETE ON disposition_invitation_consumption
BEGIN SELECT RAISE(ABORT, 'consumption is never deleted; it is what makes an invitation answerable once'); END;`

// Store owns the disposition component of the durable database.
//
// It reads internal/frontier to validate that a proposed action and an
// invitation name a record that exists, and writes nothing to it: a
// disposition is a statement about a record, never a change to one.
type Store struct {
	db       *sql.DB
	path     string
	frontier *frontier.Store

	// now supplies timestamps. It is a field so a test can make ordering
	// deterministic without sleeping; production never replaces it.
	now func() time.Time
}

// Open opens the disposition component of the durable database in dir and
// binds it to the frontier store it validates references against.
//
// The frontier store is required. A disposition service that could not read
// the frontier would accept an action proposed against a record that does not
// exist, and the queue #96 draws from would then contain work nobody can do.
//
// A schema version this build does not know is an error, never a rebuild: an
// operator's accept and decline clicks are not derivable from anything.
func Open(dir string, front *frontier.Store) (*Store, error) {
	if front == nil {
		return nil, fmt.Errorf("%w: the disposition store needs a frontier store", ErrInvalidValue)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("disposition: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("disposition: open durable database: %w", err)
	}
	// One connection: the pragmas are per connection, and §9 gives the
	// durable file a single writer.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, frontier: front, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases this store's database handle. The frontier store belongs to
// the caller and is left open.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database path, which is the same file the frontier,
// run, review and explore components use.
func (s *Store) Path() string { return s.path }

// migrate prepares the connection and brings this component's schema up to
// storeVersion, following the pragmas and the per-component ledger the file's
// other writers already established.
func (s *Store) migrate() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("disposition: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("disposition: set durable busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("disposition: enable durable foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("disposition: create migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(schema); err != nil {
			return fmt.Errorf("disposition: create durable schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			component, storeVersion); err != nil {
			return fmt.Errorf("disposition: record durable schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("disposition: read durable schema version: %w", err)
	case version != storeVersion:
		return fmt.Errorf("disposition: durable schema version %d is not supported by this build", version)
	}
	return nil
}

// transact runs fn in one transaction, rolling back on any error.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("disposition: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("disposition: commit transaction: %w", err)
	}
	return nil
}

// ProposeInput proposes one next action against a record revision.
type ProposeInput struct {
	// Record is the record revision the action is proposed against.
	Record frontier.Ref
	Kind   Kind
	// ProposedBy is the run whose result emitted the action, or the
	// operator who synthesized it.
	ProposedBy frontier.Actor
	// Ref is the reference a proposing run emitted this action under. It is
	// required of a run and refused from an operator, because it is the
	// resume key: see Propose.
	Ref     string
	Payload Payload
}

// Propose records one proposed next action.
//
// A run's proposal is idempotent under its own reference. §6.5 requires an
// interrupted run to resume without duplicating committed state, and a run
// that replayed its result would otherwise propose the same draft-issue twice
// against the same record; a second Propose under a reference the same run
// already used returns the stored action unchanged, on exactly the terms
// internal/explore's resume ledger uses for records. An operator's proposal
// carries no reference and is never deduplicated: two hand-written actions
// that happen to say the same thing are two decisions a person made.
func (s *Store) Propose(ctx context.Context, in ProposeInput) (Disposition, error) {
	if !in.Kind.valid() {
		return Disposition{}, fmt.Errorf("%w: disposition kind %q", ErrInvalidValue, in.Kind)
	}
	if err := requireActor(in.ProposedBy); err != nil {
		return Disposition{}, err
	}
	switch {
	case in.ProposedBy.Kind == frontier.ActorRun && in.Ref == "":
		return Disposition{}, fmt.Errorf("%w: a run's proposed action carries the ref it emitted it under", ErrInvalidValue)
	case in.ProposedBy.Kind == frontier.ActorOperator && in.Ref != "":
		return Disposition{}, fmt.Errorf("%w: an operator's proposed action has no emitted ref", ErrInvalidValue)
	}
	if err := in.Payload.validate(in.Kind); err != nil {
		return Disposition{}, err
	}
	if err := s.requireRecord(ctx, in.Record); err != nil {
		return Disposition{}, err
	}
	if in.Ref != "" {
		existing, err := s.byEmittedRef(ctx, in.ProposedBy.ID, in.Ref)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrUnknownDisposition) {
			return Disposition{}, err
		}
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Disposition{}, err
	}
	id, err := newID("dis")
	if err != nil {
		return Disposition{}, err
	}
	created := s.now()
	record := Disposition{
		ID:            id,
		Record:        in.Record,
		Kind:          in.Kind,
		ProposedBy:    in.ProposedBy,
		Ref:           in.Ref,
		SchemaVersion: RecordSchema,
		CreatedAt:     created,
		Status:        StatusProposed,
		Payload:       in.Payload,
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO disposition_proposal(
			id, record_type, record_id, kind, proposer_kind, proposer_id, emitted_ref,
			schema_version, created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, string(in.Record.Type), in.Record.ID, string(in.Kind),
			string(in.ProposedBy.Kind), in.ProposedBy.ID, in.Ref,
			RecordSchema, formatTime(created), payload)
		if err != nil {
			return fmt.Errorf("insert proposed action: %w", err)
		}
		return nil
	})
	if err != nil {
		return Disposition{}, err
	}
	return record, nil
}

const dispositionSelect = `SELECT id, record_type, record_id, kind, proposer_kind, proposer_id,
	emitted_ref, schema_version, created_at, payload_json FROM disposition_proposal`

// Disposition reads one proposed action with its derived status.
func (s *Store) Disposition(ctx context.Context, id string) (Disposition, error) {
	row := s.db.QueryRowContext(ctx, dispositionSelect+` WHERE id = ?`, id)
	record, err := scanDisposition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Disposition{}, fmt.Errorf("%w: %q", ErrUnknownDisposition, id)
	}
	if err != nil {
		return Disposition{}, err
	}
	return s.withStatus(ctx, record)
}

func (s *Store) byEmittedRef(ctx context.Context, proposer, ref string) (Disposition, error) {
	row := s.db.QueryRowContext(ctx,
		dispositionSelect+` WHERE proposer_id = ? AND emitted_ref = ?`, proposer, ref)
	record, err := scanDisposition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Disposition{}, fmt.Errorf("%w: %s under ref %q", ErrUnknownDisposition, proposer, ref)
	}
	if err != nil {
		return Disposition{}, err
	}
	return s.withStatus(ctx, record)
}

func (s *Store) withStatus(ctx context.Context, record Disposition) (Disposition, error) {
	status, err := s.status(ctx, record.ID)
	if err != nil {
		return Disposition{}, err
	}
	record.Status = status
	return record, nil
}

// status derives a proposed action's state from the last ledger entry.
func (s *Store) status(ctx context.Context, id string) (Status, error) {
	var ruling string
	err := s.db.QueryRowContext(ctx,
		`SELECT ruling FROM disposition_ledger WHERE disposition_id = ? ORDER BY seq DESC LIMIT 1`,
		id).Scan(&ruling)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return StatusProposed, nil
	case err != nil:
		return "", fmt.Errorf("read disposition status: %w", err)
	case Ruling(ruling) == RulingAccepted:
		return StatusAccepted, nil
	default:
		return StatusDeclined, nil
	}
}

func scanDisposition(row interface{ Scan(...any) error }) (Disposition, error) {
	var (
		record       Disposition
		recordType   string
		kind         string
		proposerKind string
		proposerID   string
		created      string
		payload      []byte
	)
	if err := row.Scan(&record.ID, &recordType, &record.Record.ID, &kind, &proposerKind, &proposerID,
		&record.Ref, &record.SchemaVersion, &created, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Disposition{}, err
		}
		return Disposition{}, fmt.Errorf("read proposed action: %w", err)
	}
	record.Record.Type = frontier.EntityType(recordType)
	record.Kind = Kind(kind)
	record.ProposedBy = frontier.Actor{Kind: frontier.ActorKind(proposerKind), ID: proposerID}
	parsed, err := parseTime(created)
	if err != nil {
		return Disposition{}, fmt.Errorf("proposed action %s: %w", record.ID, err)
	}
	record.CreatedAt = parsed
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Disposition{}, fmt.Errorf("decode proposed action %s payload: %w", record.ID, err)
	}
	return record, nil
}

// ListFilter bounds an enumeration of proposed actions. A zero Limit means
// DefaultListLimit rather than everything, on the same terms as
// frontier.ListFilter.
type ListFilter struct {
	// Record narrows to one record revision. The zero value matches every
	// record.
	Record frontier.Ref
	// Kinds narrows to these action kinds; empty matches all of them.
	Kinds []Kind
	// Statuses narrows to these derived statuses; empty matches all.
	Statuses []Status
	Limit    int
	Offset   int
}

// DefaultListLimit and MaxListLimit bound every enumeration, matching
// internal/frontier's so a caller pages both the same way.
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

func (f ListFilter) bounds() (limit, offset int) {
	limit = f.Limit
	switch {
	case limit <= 0:
		limit = DefaultListLimit
	case limit > MaxListLimit:
		limit = MaxListLimit
	}
	offset = f.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// List enumerates proposed actions, oldest first, with the total that matched
// so a caller pages by arithmetic rather than by requesting until it gets
// nothing.
//
// Status is derived, so it cannot be a WHERE clause over a stored column
// without storing it — which is the drift this package refuses. The filter is
// applied after the derivation, and the total counts what matched, so a paged
// status filter still reports how many there are.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Disposition, int, error) {
	query := dispositionSelect
	var args []any
	var clauses []string
	if filter.Record.ID != "" {
		clauses = append(clauses, `record_type = ? AND record_id = ?`)
		args = append(args, string(filter.Record.Type), filter.Record.ID)
	}
	if len(filter.Kinds) > 0 {
		placeholders := ""
		for i, kind := range filter.Kinds {
			if i > 0 {
				placeholders += ", "
			}
			placeholders += "?"
			args = append(args, string(kind))
		}
		clauses = append(clauses, `kind IN (`+placeholders+`)`)
	}
	for i, clause := range clauses {
		if i == 0 {
			query += ` WHERE `
		} else {
			query += ` AND `
		}
		query += clause
	}
	query += ` ORDER BY created_at, id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list proposed actions: %w", err)
	}
	defer rows.Close()
	var matched []Disposition
	for rows.Next() {
		record, err := scanDisposition(rows)
		if err != nil {
			return nil, 0, err
		}
		matched = append(matched, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list proposed actions: %w", err)
	}

	wanted := make(map[Status]bool, len(filter.Statuses))
	for _, status := range filter.Statuses {
		wanted[status] = true
	}
	var kept []Disposition
	for _, record := range matched {
		resolved, err := s.withStatus(ctx, record)
		if err != nil {
			return nil, 0, err
		}
		if len(wanted) > 0 && !wanted[resolved.Status] {
			continue
		}
		kept = append(kept, resolved)
	}

	limit, offset := filter.bounds()
	total := len(kept)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return kept[offset:end], total, nil
}

// DecideInput records one operator answer to a proposed action.
type DecideInput struct {
	DispositionID string
	Ruling        Ruling
	// By is the operator. There is no default identity: an anonymous
	// acceptance would record that an action was authorized without
	// recording that anyone authorized it, which is the one thing #87's
	// "suggestions, never side effects" rests on.
	By   string
	Note string
}

// Decide appends one attributable answer to a proposed action.
//
// It appends rather than updates, so reconsidering is another entry and both
// stay readable in order. Nothing else happens: accepting a draft-issue opens
// no issue, accepting a propose-reality-fact writes no fact. Each of those is
// a separate, explicit operator act through the surface that owns it, and this
// entry is the durable record that the operator authorized it — which is
// exactly the provenance #88 reads back as an acceptance rate.
func (s *Store) Decide(ctx context.Context, in DecideInput) (LedgerEntry, error) {
	if !in.Ruling.valid() {
		return LedgerEntry{}, fmt.Errorf("%w: ruling %q", ErrInvalidValue, in.Ruling)
	}
	if in.By == "" {
		return LedgerEntry{}, fmt.Errorf("%w: a decision on a proposed action is attributed", ErrInvalidValue)
	}
	id, err := newID("dld")
	if err != nil {
		return LedgerEntry{}, err
	}
	payload := LedgerPayload{Note: in.Note}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return LedgerEntry{}, err
	}
	entry := LedgerEntry{
		ID:            id,
		DispositionID: in.DispositionID,
		Ruling:        in.Ruling,
		By:            in.By,
		Payload:       payload,
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM disposition_proposal WHERE id = ?`,
			in.DispositionID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", ErrUnknownDisposition, in.DispositionID)
		}
		if err != nil {
			return fmt.Errorf("check proposed action: %w", err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM disposition_ledger WHERE disposition_id = ?`,
			in.DispositionID).Scan(&entry.Sequence); err != nil {
			return fmt.Errorf("next ledger sequence: %w", err)
		}
		entry.RecordedAt = s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO disposition_ledger(
			id, disposition_id, seq, ruling, operator_id, schema_version, recorded_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.DispositionID, entry.Sequence, string(in.Ruling), in.By,
			RecordSchema, formatTime(entry.RecordedAt), encoded); err != nil {
			return fmt.Errorf("append ledger entry: %w", err)
		}
		return nil
	})
	if err != nil {
		return LedgerEntry{}, err
	}
	return entry, nil
}

// Ledger reads a proposed action's answers in the order they were recorded.
func (s *Store) Ledger(ctx context.Context, dispositionID string) ([]LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, disposition_id, seq, ruling, operator_id,
		recorded_at, payload_json FROM disposition_ledger WHERE disposition_id = ? ORDER BY seq`,
		dispositionID)
	if err != nil {
		return nil, fmt.Errorf("read disposition ledger: %w", err)
	}
	defer rows.Close()
	var ledger []LedgerEntry
	for rows.Next() {
		var (
			entry    LedgerEntry
			ruling   string
			recorded string
			payload  []byte
		)
		if err := rows.Scan(&entry.ID, &entry.DispositionID, &entry.Sequence, &ruling, &entry.By,
			&recorded, &payload); err != nil {
			return nil, fmt.Errorf("read disposition ledger: %w", err)
		}
		entry.Ruling = Ruling(ruling)
		if entry.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("ledger entry %s: %w", entry.ID, err)
		}
		if err := json.Unmarshal(payload, &entry.Payload); err != nil {
			return nil, fmt.Errorf("decode ledger entry %s payload: %w", entry.ID, err)
		}
		ledger = append(ledger, entry)
	}
	return ledger, rows.Err()
}

// InviteInput records one operator "process this further".
type InviteInput struct {
	Record frontier.Ref
	// By is the operator. An invitation outranks the conductor's own policy
	// (#96), so an unattributed one would be a way of borrowing operator
	// authority without an operator.
	By string
}

// Invite records an instruction-free invitation against a record revision.
//
// There is no note parameter, and adding one later would be a change to what
// an invitation means rather than a convenience: #87 makes the nudge
// instruction-free so that refine, question, amend, or abandon stays the
// model's judgement. Inviting the same record twice is two invitations, which
// is honest — the operator asked twice — and the queue shows both.
func (s *Store) Invite(ctx context.Context, in InviteInput) (Invitation, error) {
	if in.By == "" {
		return Invitation{}, fmt.Errorf("%w: an invitation is attributed to an operator", ErrInvalidValue)
	}
	if err := s.requireRecord(ctx, in.Record); err != nil {
		return Invitation{}, err
	}
	id, err := newID("inv")
	if err != nil {
		return Invitation{}, err
	}
	created := s.now()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO disposition_invitation(
		id, record_type, record_id, operator_id, created_at) VALUES(?, ?, ?, ?, ?)`,
		id, string(in.Record.Type), in.Record.ID, in.By, formatTime(created)); err != nil {
		return Invitation{}, fmt.Errorf("insert invitation: %w", err)
	}
	return Invitation{ID: id, Record: in.Record, By: in.By, CreatedAt: created}, nil
}

const invitationSelect = `SELECT i.id, i.record_type, i.record_id, i.operator_id, i.created_at,
	COALESCE(c.run_id, ''), COALESCE(c.consumed_at, '')
	FROM disposition_invitation i
	LEFT JOIN disposition_invitation_consumption c ON c.invitation_id = i.id`

// Invitation reads one invitation with its consumption state.
func (s *Store) Invitation(ctx context.Context, id string) (Invitation, error) {
	row := s.db.QueryRowContext(ctx, invitationSelect+` WHERE i.id = ?`, id)
	record, err := scanInvitation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, fmt.Errorf("%w: %q", ErrUnknownInvitation, id)
	}
	return record, err
}

// InvitationFilter bounds the queue query.
type InvitationFilter struct {
	// Record narrows to one record revision.
	Record frontier.Ref
	// All includes invitations a run already consumed. The default is the
	// open queue, because that is what #96's rung one asks for: a
	// conductor cycle wants the work nobody has taken, not the history.
	All   bool
	Limit int
}

// Invitations reads the process-further queue, oldest first.
//
// Oldest first is the whole ordering, and it is deliberately not sorted by
// anything about the records. §5.2 confines novelty and priority to ordering
// the frontier; a queue of operator nudges re-sorted by a model-produced score
// would be the loop deciding which of the operator's requests mattered, which
// is exactly the inversion #96's ladder puts operator invitations above policy
// to prevent.
func (s *Store) Invitations(ctx context.Context, filter InvitationFilter) ([]Invitation, error) {
	query := invitationSelect
	var args []any
	var clauses []string
	if !filter.All {
		clauses = append(clauses, `c.invitation_id IS NULL`)
	}
	if filter.Record.ID != "" {
		clauses = append(clauses, `i.record_type = ? AND i.record_id = ?`)
		args = append(args, string(filter.Record.Type), filter.Record.ID)
	}
	for i, clause := range clauses {
		if i == 0 {
			query += ` WHERE `
		} else {
			query += ` AND `
		}
		query += clause
	}
	query += ` ORDER BY i.created_at, i.id`
	limit := filter.Limit
	switch {
	case limit <= 0:
		limit = DefaultListLimit
	case limit > MaxListLimit:
		limit = MaxListLimit
	}
	query += ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read invitation queue: %w", err)
	}
	defer rows.Close()
	var queue []Invitation
	for rows.Next() {
		record, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		queue = append(queue, record)
	}
	return queue, rows.Err()
}

func scanInvitation(row interface{ Scan(...any) error }) (Invitation, error) {
	var (
		record     Invitation
		recordType string
		created    string
		consumedAt string
	)
	if err := row.Scan(&record.ID, &recordType, &record.Record.ID, &record.By, &created,
		&record.ConsumedBy, &consumedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invitation{}, err
		}
		return Invitation{}, fmt.Errorf("read invitation: %w", err)
	}
	record.Record.Type = frontier.EntityType(recordType)
	parsed, err := parseTime(created)
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation %s: %w", record.ID, err)
	}
	record.CreatedAt = parsed
	if consumedAt != "" {
		if record.ConsumedAt, err = parseTime(consumedAt); err != nil {
			return Invitation{}, fmt.Errorf("invitation %s consumption: %w", record.ID, err)
		}
	}
	return record, nil
}

// Consume claims open invitations for one run, oldest first, and reports the
// ones it took.
//
// Consume-once is the point. #96 makes invitations rung one of the conductor's
// work ladder, and a queue entry two cycles could both claim would spend the
// operator's budget twice on one nudge; a resumed run that already consumed an
// invitation must also not consume it again. Both are the same guarantee, and
// it is the consumption table's primary key rather than a check this code
// performs: the claim is a single INSERT, so two runs racing produce one
// winner and one refusal instead of two readers who both saw an open row.
//
// A run that finds nothing gets an empty list rather than an error. An idle
// queue is the normal state of a conductor that is keeping up.
func (s *Store) Consume(ctx context.Context, runID string, limit int) ([]Invitation, error) {
	if runID == "" {
		return nil, fmt.Errorf("%w: consuming an invitation names the run that took it", ErrInvalidValue)
	}
	open, err := s.Invitations(ctx, InvitationFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	var taken []Invitation
	for _, invitation := range open {
		consumed := s.now()
		claimed, err := s.claim(ctx, invitation.ID, runID, consumed)
		if err != nil {
			return nil, err
		}
		// Not claimed means another run took it between the read and the
		// write, which is the race this design expects: leave it with the
		// winner and move on.
		if !claimed {
			continue
		}
		invitation.ConsumedBy = runID
		invitation.ConsumedAt = consumed
		taken = append(taken, invitation)
	}
	return taken, nil
}

// ConsumeOne claims one named invitation for a run, refusing a second claim.
//
// It exists beside Consume because the two answer different questions. Consume
// is the conductor draining a queue and treating a lost race as somebody
// else's turn; this is a caller naming one invitation, for which a lost race
// is the answer rather than a reason to move on.
func (s *Store) ConsumeOne(ctx context.Context, invitationID, runID string) (Invitation, error) {
	if runID == "" {
		return Invitation{}, fmt.Errorf("%w: consuming an invitation names the run that took it", ErrInvalidValue)
	}
	invitation, err := s.Invitation(ctx, invitationID)
	if err != nil {
		return Invitation{}, err
	}
	if !invitation.Open() {
		return Invitation{}, fmt.Errorf("%w: %s took %q", ErrAlreadyConsumed, invitation.ConsumedBy, invitationID)
	}
	consumed := s.now()
	claimed, err := s.claim(ctx, invitationID, runID, consumed)
	if err != nil {
		return Invitation{}, err
	}
	if !claimed {
		return Invitation{}, fmt.Errorf("%w: %q", ErrAlreadyConsumed, invitationID)
	}
	invitation.ConsumedBy = runID
	invitation.ConsumedAt = consumed
	return invitation, nil
}

// claim writes one consumption and reports whether this caller won it.
//
// INSERT OR IGNORE rather than a read followed by an insert: the primary key
// is what makes an invitation consumable once, so the decision has to be the
// write itself. A caller that checked first and inserted second would have a
// window in which two runs both saw an open invitation.
func (s *Store) claim(ctx context.Context, invitationID, runID string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO disposition_invitation_consumption(
		invitation_id, run_id, consumed_at) VALUES(?, ?, ?)`,
		invitationID, runID, formatTime(at))
	if err != nil {
		return false, fmt.Errorf("consume invitation %s: %w", invitationID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume invitation %s: %w", invitationID, err)
	}
	return affected == 1, nil
}

// requireRecord refuses a reference to a record the frontier does not hold, so
// a dangling proposal is a named error before the insert rather than a queue
// entry pointing at nothing.
func (s *Store) requireRecord(ctx context.Context, ref frontier.Ref) error {
	switch ref.Type {
	case frontier.EntityHypothesis:
		_, err := s.frontier.Hypothesis(ctx, ref.ID)
		return err
	case frontier.EntityObservation:
		_, err := s.frontier.Observation(ctx, ref.ID)
		return err
	case frontier.EntityFinding:
		_, err := s.frontier.Finding(ctx, ref.ID)
		return err
	case frontier.EntityProposal:
		_, err := s.frontier.Proposal(ctx, ref.ID)
		return err
	}
	return fmt.Errorf("%w: record type %q", ErrInvalidValue, ref.Type)
}

func requireActor(actor frontier.Actor) error {
	switch {
	case actor.ID == "":
		return fmt.Errorf("%w: a proposed action names who proposed it", ErrInvalidValue)
	case actor.Kind != frontier.ActorRun && actor.Kind != frontier.ActorOperator:
		return fmt.Errorf("%w: proposer kind %q", ErrInvalidValue, actor.Kind)
	}
	return nil
}

// newID generates a globally unique client-side identifier, which §9 requires
// for immutable entities and events. The prefix makes a stray ID in a log
// self-describing without revealing anything about the corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// marshalPayload encodes the §9 encryption-bound half of a row, so the later
// sync slice has one place to wrap in an AEAD envelope.
func marshalPayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return encoded, nil
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction, matching
// internal/frontier's for the same reason: a trimmed fraction makes text order
// disagree with time order exactly when two events land within a tenth of a
// second of each other, which is when ordering matters.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
