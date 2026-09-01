package complaint

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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sync"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the durable database's shared
// migration ledger, beside internal/frontier's, internal/run's,
// internal/review's, internal/explore's and internal/disposition's. A new
// durable component joins the one file with its own component key and table
// prefix rather than opening a second, because the point of one durable file is
// that a single pending-sync journal is what an operator has to preserve.
const component = "complaint"

// storeVersion is this component's schema version.
const storeVersion = 1

// databaseFile is the durable, pending-sync database, named the way every other
// component of it names it. A complaint exists nowhere else - it is the only
// record of a sentence the operator said once - so it belongs with the analysis
// rather than in the rebuildable cache.
const databaseFile = run.DatabaseName

// schema is this component's one table and its append-only enforcement.
//
// The §9 split is the usual one: payload_json holds the operator's words, which
// a later sync seals, and every other column is identifier or attribution
// metadata the plaintext allowlist admits.
//
// The revision chain is columns on the row rather than a sibling table, which
// is where internal/frontier keeps its own. The difference is that a frontier
// revision carries an actor, a reason and a payload of its own - it is a record
// about a record - while a complaint's amendment IS the complaint said again:
// there is nothing to store beside it, and a second table would be a join for
// no fact.
//
// One leaf per chain is a unique index rather than a check the writer performs,
// because a writer's check is a race and this one is not. Two operators (or one
// operator in two terminals) amending the same wording concurrently is exactly
// the case where the app-level "does a successor exist" read has already
// returned false for both.
const schema = `
CREATE TABLE IF NOT EXISTS complaint(
	id             TEXT PRIMARY KEY,
	root_id        TEXT NOT NULL,
	ancestor_id    TEXT REFERENCES complaint(id),
	seq            INTEGER NOT NULL,
	operator_id    TEXT NOT NULL,
	host_id        TEXT NOT NULL,
	redacted       INTEGER NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	UNIQUE(root_id, seq)
);
CREATE INDEX IF NOT EXISTS complaint_by_root ON complaint(root_id, seq);
CREATE INDEX IF NOT EXISTS complaint_by_created ON complaint(created_at);
CREATE UNIQUE INDEX IF NOT EXISTS complaint_one_successor
	ON complaint(ancestor_id) WHERE ancestor_id IS NOT NULL;

CREATE TRIGGER IF NOT EXISTS complaint_immutable BEFORE UPDATE ON complaint
BEGIN SELECT RAISE(ABORT, 'a complaint is immutable; saying it better appends an amendment'); END;
CREATE TRIGGER IF NOT EXISTS complaint_kept BEFORE DELETE ON complaint
BEGIN SELECT RAISE(ABORT, 'complaints are never deleted; an unaddressed one is information'); END;`

// Store owns the complaint component of the durable database.
//
// It reads no other store and writes to none. A complaint is free-standing by
// design: it names no record it is about, requires no record to exist, and can
// be told on a machine that has archived nothing - which is the state an
// operator is most likely to be annoyed in.
type Store struct {
	db   *sql.DB
	path string

	// sync publishes this store's durable records to the shared catalog. It
	// is nil in local-only mode, which is why the write path guards on it
	// rather than relying on a no-op implementation being installed.
	sync sync.Hook

	// refs and refsDiag are the typed reference graph's write half (#113),
	// nil when the caller asked for no edges. See reference.go.
	refs     reference.Appender
	refsDiag func(error)

	// now supplies timestamps. It is a field so a test can make ordering
	// deterministic without sleeping; production never replaces it.
	now func() time.Time
}

// Open opens the complaint component of the durable database in dir.
//
// It takes no sibling store, and that absence is the feature. A complaint is
// the operator's unprompted input, so requiring a frontier to validate it
// against would make "Babel has analysed nothing yet" a reason to refuse the
// operator's first sentence about it.
//
// A schema version this build does not know is an error, never a rebuild: what
// a person said is not derivable from anything.
func Open(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("complaint: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("complaint: open durable database: %w", err)
	}
	// One connection: the pragmas are per connection, and §9 gives the
	// durable file a single writer.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if s.sync != nil {
		// The journal tables have to exist on this handle, because this
		// store stages inside its own transactions on its own connection.
		// It is cheap and idempotent, and doing it here rather than at the
		// first write means a misconfigured deployment fails at Open rather
		// than halfway through the operator's sentence.
		if err := sync.EnsureSchema(s.db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close releases this store's database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database path, which is the same file the frontier,
// run, review, explore and disposition components use.
func (s *Store) Path() string { return s.path }

// migrate prepares the connection and brings this component's schema up to
// storeVersion, following the pragmas and the per-component ledger the file's
// other writers already established.
func (s *Store) migrate() error {
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("complaint: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("complaint: set durable busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("complaint: enable durable foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("complaint: create migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(schema); err != nil {
			return fmt.Errorf("complaint: create durable schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)`,
			component, storeVersion); err != nil {
			return fmt.Errorf("complaint: record durable schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("complaint: read durable schema version: %w", err)
	case version != storeVersion:
		return fmt.Errorf("complaint: durable schema version %d is not supported by this build", version)
	}
	return nil
}

// transact runs fn in one transaction, rolling back on any error.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complaint: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complaint: commit transaction: %w", err)
	}
	return nil
}

// TellInput is one thing the operator wants to say.
type TellInput struct {
	// Text is what they wrote. Capture stores it verbatim except for secret
	// redaction; it is neither summarized, reworded, nor classified, because
	// every one of those is Babel deciding what the operator meant.
	Text string
	// By is the operator, required. See Complaint.By.
	By string
	// Host is the machine capturing it, required. See Complaint.Host.
	Host string
	// Addresses names records this complaint is about, and is empty for the
	// usual free-standing case.
	//
	// It is a courtesy rather than a classification: the operator who already
	// knows which hypothesis annoyed them should not have to find the record
	// page to say so. An edge is minted per entry after the complaint is
	// durable, and a refused one is a warning - the complaint is what
	// mattered.
	Addresses []frontier.Ref
}

// Tell captures one complaint.
//
// It refuses exactly three things: no words, no operator, and prose past
// MaxTextBytes. There is no validation of what the complaint says, no
// classification of what it is about, and no requirement that it name
// anything - a complaint that had to be well-formed to be heard would be a
// form, and #115 is explicit that this is not one.
//
// Secret redaction runs before anything is stored. Operators paste secrets into
// text boxes, this text becomes an immutable sealed object and a row in a
// retrieval index, and neither of those can be un-said; the report that
// redaction happened rides on the record so a later reader can tell a
// placeholder from the operator's own words.
//
// The single insert runs in a transaction because staging shares it: a
// complaint that committed locally while its journal row did not would be
// steering the fleet never hears about and nothing reports as owed.
func (s *Store) Tell(ctx context.Context, in TellInput) (Complaint, error) {
	text, redacted, err := capture(in.Text)
	if err != nil {
		return Complaint{}, err
	}
	if in.By == "" {
		return Complaint{}, fmt.Errorf("%w: a complaint is attributed to an operator", ErrInvalidValue)
	}
	if in.Host == "" {
		return Complaint{}, fmt.Errorf("%w: a complaint records the host it was captured on", ErrInvalidValue)
	}
	id, err := newID("cmp")
	if err != nil {
		return Complaint{}, err
	}
	told := Complaint{
		ID: id, RootID: id, Sequence: 1,
		By: in.By, Host: in.Host, Text: text, Redacted: redacted,
		CreatedAt: s.now(),
	}
	if err := s.write(ctx, told); err != nil {
		return Complaint{}, err
	}
	s.mintAddresses(ctx, told, in.Addresses)
	return told, nil
}

// AmendInput restates one complaint the operator already told.
type AmendInput struct {
	// ComplaintID is the wording being amended, which must be its chain's
	// head.
	ComplaintID string
	Text        string
	By          string
	Host        string
	// Addresses names records the amended wording is about. It is not
	// inherited from the wording it replaces: an edge is an assertion the
	// operator made about a particular record, and copying it forward would
	// be this package asserting it again on their behalf.
	Addresses []frontier.Ref
}

// Amend appends a better wording of a complaint the operator already told.
//
// The earlier wording stays readable and stays cited: an amendment is the same
// complaint said again, so a hypothesis that addressed the first wording has
// not stopped addressing this complaint, and the chain is what tells a reader
// that. Amending anything but the head is refused with ErrSuperseded rather
// than branched, because two current wordings of one complaint is two answers
// to "what does the operator say".
//
// There is no transition here that ends a chain, and adding one would be a
// change to what a complaint means rather than a feature: #115's guard is that
// steering pressure has no resolved state, and the only honest way to see
// whether a complaint was answered is to look at what cites it.
func (s *Store) Amend(ctx context.Context, in AmendInput) (Complaint, error) {
	text, redacted, err := capture(in.Text)
	if err != nil {
		return Complaint{}, err
	}
	if in.By == "" {
		return Complaint{}, fmt.Errorf("%w: an amendment is attributed to an operator", ErrInvalidValue)
	}
	if in.Host == "" {
		return Complaint{}, fmt.Errorf("%w: an amendment records the host it was captured on", ErrInvalidValue)
	}
	parent, err := s.Complaint(ctx, in.ComplaintID)
	if err != nil {
		return Complaint{}, err
	}
	id, err := newID("cmp")
	if err != nil {
		return Complaint{}, err
	}
	amended := Complaint{
		ID: id, RootID: parent.RootID, AncestorID: parent.ID, Sequence: parent.Sequence + 1,
		By: in.By, Host: in.Host, Text: text, Redacted: redacted,
		CreatedAt: s.now(),
	}
	if err := s.write(ctx, amended); err != nil {
		return Complaint{}, err
	}
	s.mintSupersedes(ctx, amended)
	s.mintAddresses(ctx, amended, in.Addresses)
	return amended, nil
}

// write makes one complaint durable and stages it for the shared catalog in the
// same transaction.
//
// It is one function for both wordings because the two differ in what they say
// about their chain and in nothing else, and a second copy of the insert is how
// an original and an amendment end up published on different terms.
func (s *Store) write(ctx context.Context, c Complaint) error {
	body, err := marshalPayload(payload{Text: c.Text})
	if err != nil {
		return err
	}
	// An operator's act, so producedBy is empty and the complaint is its own
	// closure of one: no run produced it, and the runs that later answer it
	// did not exist when the operator wrote it.
	var (
		closure sync.Closure
		publish bool
	)
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO complaint(
			id, root_id, ancestor_id, seq, operator_id, host_id, redacted,
			schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.RootID, nullable(c.AncestorID), c.Sequence, c.By, c.Host, c.Redacted,
			RecordSchema, formatTime(c.CreatedAt), string(body)); err != nil {
			if isUniqueViolation(err) && c.AncestorID != "" {
				return fmt.Errorf("%w: %q", ErrSuperseded, c.AncestorID)
			}
			return fmt.Errorf("insert complaint: %w", err)
		}
		if s.sync == nil {
			return nil
		}
		wire, err := marshalPayload(publishedComplaint{
			ID:         c.ID,
			RootID:     c.RootID,
			AncestorID: c.AncestorID,
			Sequence:   c.Sequence,
			OperatorID: c.By,
			HostID:     c.Host,
			Redacted:   c.Redacted,
			Text:       c.Text,
			CreatedAt:  formatTime(c.CreatedAt),
		})
		if err != nil {
			return err
		}
		closure, publish, err = s.stage(ctx, tx, "", sync.Record{
			EntityID: c.ID, Kind: complaintKind, Schema: RecordSchema, Payload: wire,
		})
		return err
	})
	if err != nil {
		return err
	}
	if publish {
		// Best-effort by contract: CommitInline returns nil for every
		// transient failure and reports one diagnostic line itself, leaving
		// the complaint durable and visibly pending-sync. It errors only on a
		// caller bug, which is a fault in this write path rather than a
		// condition of the deployment.
		if err := s.sync.CommitInline(ctx, closure); err != nil {
			return err
		}
	}
	return nil
}

// capture validates and redacts what the operator wrote.
//
// Redaction runs on the bytes that will be stored rather than on a copy shown
// to the operator, so the record and the report of it cannot disagree about
// what was kept. Trailing whitespace goes, because a shell heredoc adds a
// newline the operator did not type; nothing else about the text is touched.
func capture(text string) (string, bool, error) {
	trimmed := strings.TrimSpace(text)
	switch {
	case trimmed == "":
		return "", false, fmt.Errorf("%w: a complaint with no words in it says nothing", ErrInvalidValue)
	case len(trimmed) > MaxTextBytes:
		return "", false, fmt.Errorf("%w: complaint text is %d bytes, over the %d-byte bound",
			ErrInvalidValue, len(trimmed), MaxTextBytes)
	case !utf8.ValidString(trimmed):
		return "", false, fmt.Errorf("%w: complaint text is not valid UTF-8", ErrInvalidValue)
	}
	redacted := preflight.Redact(trimmed)
	return redacted, redacted != trimmed, nil
}

const complaintSelect = `SELECT id, root_id, COALESCE(ancestor_id, ''), seq, operator_id, host_id,
	redacted, created_at, payload_json FROM complaint`

// Complaint reads one wording by id.
func (s *Store) Complaint(ctx context.Context, id string) (Complaint, error) {
	row := s.db.QueryRowContext(ctx, complaintSelect+` WHERE id = ?`, id)
	found, err := scanComplaint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Complaint{}, fmt.Errorf("%w: %q", ErrUnknownComplaint, id)
	}
	return found, err
}

// Revisions reads one complaint's whole chain, oldest wording first.
//
// The argument may be any revision of the chain, because an operator holding an
// id from a citation should not have to find the root to read the history it
// belongs to.
func (s *Store) Revisions(ctx context.Context, id string) ([]Complaint, error) {
	anchor, err := s.Complaint(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.query(ctx, complaintSelect+` WHERE root_id = ? ORDER BY seq`, anchor.RootID)
}

// Heads reads the current wording of every complaint, newest first.
//
// "Head" is "nothing amends it", read off the ancestor column that the unique
// index makes single-valued, so the set is exactly what the operator currently
// says. Superseded wordings are absent for the reason internal/frontier gives
// for its own heads: an amended sentence is not what the operator says now, and
// offering it would make a listing show one complaint twice.
func (s *Store) Heads(ctx context.Context) ([]Complaint, error) {
	return s.query(ctx, complaintSelect+` c WHERE NOT EXISTS (
		SELECT 1 FROM complaint successor WHERE successor.ancestor_id = c.id)
		ORDER BY c.created_at DESC, c.id`)
}

func (s *Store) query(ctx context.Context, statement string, args ...any) ([]Complaint, error) {
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("read complaints: %w", err)
	}
	defer rows.Close()
	var found []Complaint
	for rows.Next() {
		c, err := scanComplaint(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, c)
	}
	return found, rows.Err()
}

// scanner is the shared shape of *sql.Row and *sql.Rows, so one scan serves the
// single read and the listings.
type scanner interface {
	Scan(dest ...any) error
}

func scanComplaint(src scanner) (Complaint, error) {
	var (
		c       Complaint
		created string
		body    []byte
	)
	if err := src.Scan(&c.ID, &c.RootID, &c.AncestorID, &c.Sequence, &c.By, &c.Host,
		&c.Redacted, &created, &body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Complaint{}, err
		}
		return Complaint{}, fmt.Errorf("read complaint: %w", err)
	}
	var stored payload
	if err := json.Unmarshal(body, &stored); err != nil {
		return Complaint{}, fmt.Errorf("decode complaint %s payload: %w", c.ID, err)
	}
	c.Text = stored.Text
	at, err := parseTime(created)
	if err != nil {
		return Complaint{}, fmt.Errorf("complaint %s: %w", c.ID, err)
	}
	c.CreatedAt = at
	return c, nil
}

// nullable keeps an absent ancestor out of the column as NULL rather than as
// the empty string, so the partial unique index that enforces one leaf per
// chain does not treat every original as a successor of nothing-in-particular.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// isUniqueViolation reports whether err is SQLite refusing a duplicate key.
//
// It matches on the driver's message because modernc.org/sqlite reports
// constraint failures as a plain error, and the alternative - reading the
// successor first and trusting that nothing raced - is the check this index
// exists to replace.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// newID mints a globally unique, client-generated record id: 128 random bits
// behind a kind prefix, the same shape internal/frontier, internal/disposition
// and internal/reference mint, so an id is self-describing without revealing
// anything about the corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// marshalPayload encodes the §9 encryption-bound half of a row, so the later
// sync slice has one place to wrap in an AEAD envelope.
func marshalPayload(body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return encoded, nil
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction, matching
// internal/frontier's and internal/disposition's for the same reason: a trimmed
// fraction makes text order disagree with time order exactly when two events
// land within a tenth of a second of each other, which is when ordering
// matters.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
