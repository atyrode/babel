package frontier

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
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/reference"
	babelsync "github.com/atyrode/babel/internal/sync"

	_ "modernc.org/sqlite"
)

// component names this package's rows in the shared migration table. The
// durable database is shared with the other pending-sync writers (run
// preparations and receipts), so migrations are versioned per component
// rather than per file: two packages advancing independently must not have to
// agree on one global number.
const component = "frontier"

// databaseFile is the durable, pending-sync database. §9 splits local state in
// two by lifecycle, and this is the half that is not rebuildable: losing it
// loses analysis, so it is deliberately not the retrieval cache's file and is
// never discarded and recreated on a schema surprise.
const databaseFile = "durable.db"

// migrations are applied in order and never edited once released; §9 requires
// local SQLite migrations to be forward and transactional.
//
// The schema mirrors the §9 split exactly: one payload_json column per table
// carrying everything a later sync slice seals in an AEAD envelope, and
// otherwise only allowlisted identifiers, kinds, schema versions, counts,
// lifecycle state, and timestamps.
//
// Immutability is enforced by triggers rather than by this package's SQL. A
// frontier whose append-only history depends on nobody ever writing an UPDATE
// is append-only by convention, and §14's frontier gate — plus §4.7's review
// semantics, which internal/review builds on these same tables — leans on the
// property itself. The triggers live in migration 2 rather than in the table
// definitions on purpose; see that migration's comment.
var migrations = []string{`
CREATE TABLE frontier_hypothesis(
	id             TEXT PRIMARY KEY,
	ancestor_id    TEXT REFERENCES frontier_hypothesis(id),
	run_id         TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);
CREATE INDEX frontier_hypothesis_ancestor ON frontier_hypothesis(ancestor_id);

CREATE TABLE frontier_status_event(
	id            TEXT PRIMARY KEY,
	hypothesis_id TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	seq           INTEGER NOT NULL,
	status        TEXT NOT NULL,
	run_id        TEXT NOT NULL,
	recorded_at   TEXT NOT NULL,
	payload_json  TEXT NOT NULL,
	UNIQUE(hypothesis_id, seq)
);

CREATE TABLE frontier_hypothesis_link(
	id           TEXT PRIMARY KEY,
	from_id      TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	to_id        TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	link_type    TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(from_id, to_id, link_type)
);
CREATE INDEX frontier_hypothesis_link_to ON frontier_hypothesis_link(to_id);

CREATE TABLE frontier_observation(
	id             TEXT PRIMARY KEY,
	ancestor_id    TEXT REFERENCES frontier_observation(id),
	hypothesis_id  TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	run_id         TEXT NOT NULL,
	recipe_id      TEXT NOT NULL,
	recipe_version INTEGER NOT NULL,
	schema_version INTEGER NOT NULL,
	evidence_count INTEGER NOT NULL CHECK(evidence_count > 0),
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);
CREATE INDEX frontier_observation_hypothesis ON frontier_observation(hypothesis_id);

CREATE TABLE frontier_finding(
	id             TEXT PRIMARY KEY,
	ancestor_id    TEXT REFERENCES frontier_finding(id),
	run_id         TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE frontier_finding_observation(
	finding_id     TEXT NOT NULL REFERENCES frontier_finding(id),
	observation_id TEXT NOT NULL REFERENCES frontier_observation(id),
	position       INTEGER NOT NULL,
	PRIMARY KEY(finding_id, position)
);
CREATE INDEX frontier_finding_observation_observation ON frontier_finding_observation(observation_id);

CREATE TABLE frontier_proposal(
	id             TEXT PRIMARY KEY,
	ancestor_id    TEXT REFERENCES frontier_proposal(id),
	run_id         TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE frontier_proposal_finding(
	proposal_id TEXT NOT NULL REFERENCES frontier_proposal(id),
	finding_id  TEXT NOT NULL REFERENCES frontier_finding(id),
	position    INTEGER NOT NULL,
	PRIMARY KEY(proposal_id, position)
);
CREATE INDEX frontier_proposal_finding_finding ON frontier_proposal_finding(finding_id);

CREATE TABLE frontier_disposition(
	id              TEXT PRIMARY KEY,
	subject_type    TEXT NOT NULL,
	subject_id      TEXT NOT NULL,
	seq             INTEGER NOT NULL,
	disposition     TEXT NOT NULL,
	reviewer_id     TEXT NOT NULL,
	context_id      TEXT NOT NULL,
	duplicate_of_id TEXT NOT NULL,
	recorded_at     TEXT NOT NULL,
	payload_json    TEXT NOT NULL,
	UNIQUE(subject_type, subject_id, seq)
);

CREATE TABLE frontier_refinement_request(
	id             TEXT PRIMARY KEY,
	disposition_id TEXT NOT NULL UNIQUE REFERENCES frontier_disposition(id),
	subject_type   TEXT NOT NULL,
	subject_id     TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);
`,
	// Migration 2 moves the append-only rule from this package's discipline
	// into the engine, so no future caller can overwrite a claim or drop a
	// disposition regardless of what SQL it writes.
	//
	// It is a separate migration rather than an edit to the first because a
	// durable file that already reached version 1 must gain the triggers
	// too: SQLite attaches triggers at CREATE time, so editing migration 1
	// would protect only databases created after this build, and an
	// existing frontier would keep accepting updates while every test
	// passed. Every frontier_* table is covered, because none of them
	// mutates by design — a hypothesis revision links its ancestor, status
	// and disposition are appended events, and the join tables belong to
	// the immutable record that owns them. The one row this component does
	// update, its schema_migration version, is deliberately outside the
	// prefix: it is shared with the durable file's other writers and has to
	// advance.
	`
CREATE TRIGGER frontier_hypothesis_immutable BEFORE UPDATE ON frontier_hypothesis
BEGIN SELECT RAISE(ABORT, 'a hypothesis is immutable; record a revision that links this one as its ancestor'); END;
CREATE TRIGGER frontier_hypothesis_kept BEFORE DELETE ON frontier_hypothesis
BEGIN SELECT RAISE(ABORT, 'hypotheses are never deleted; sorting the frontier defers a candidate rather than removing it'); END;

CREATE TRIGGER frontier_status_event_immutable BEFORE UPDATE ON frontier_status_event
BEGIN SELECT RAISE(ABORT, 'hypothesis status history is append-only; append the next status instead'); END;
CREATE TRIGGER frontier_status_event_kept BEFORE DELETE ON frontier_status_event
BEGIN SELECT RAISE(ABORT, 'hypothesis status history is append-only'); END;

CREATE TRIGGER frontier_hypothesis_link_immutable BEFORE UPDATE ON frontier_hypothesis_link
BEGIN SELECT RAISE(ABORT, 'a hypothesis link is immutable; add the link that states the new relation'); END;
CREATE TRIGGER frontier_hypothesis_link_kept BEFORE DELETE ON frontier_hypothesis_link
BEGIN SELECT RAISE(ABORT, 'hypothesis links are never deleted; lineage has to stay readable'); END;

CREATE TRIGGER frontier_observation_immutable BEFORE UPDATE ON frontier_observation
BEGIN SELECT RAISE(ABORT, 'an observation is immutable; record a revision that links this one as its ancestor'); END;
CREATE TRIGGER frontier_observation_kept BEFORE DELETE ON frontier_observation
BEGIN SELECT RAISE(ABORT, 'observations are never deleted; the findings that cite them would lose their evidence'); END;

CREATE TRIGGER frontier_finding_immutable BEFORE UPDATE ON frontier_finding
BEGIN SELECT RAISE(ABORT, 'a finding is immutable; record a revision that links this one as its ancestor'); END;
CREATE TRIGGER frontier_finding_kept BEFORE DELETE ON frontier_finding
BEGIN SELECT RAISE(ABORT, 'findings are never deleted; the proposals that cite them would lose their support'); END;

CREATE TRIGGER frontier_finding_observation_immutable BEFORE UPDATE ON frontier_finding_observation
BEGIN SELECT RAISE(ABORT, 'the observation set of a finding is immutable; revise the finding instead'); END;
CREATE TRIGGER frontier_finding_observation_kept BEFORE DELETE ON frontier_finding_observation
BEGIN SELECT RAISE(ABORT, 'the observation set of a finding is never deleted; revise the finding instead'); END;

CREATE TRIGGER frontier_proposal_immutable BEFORE UPDATE ON frontier_proposal
BEGIN SELECT RAISE(ABORT, 'a proposal is immutable; record a revision that links this one as its ancestor'); END;
CREATE TRIGGER frontier_proposal_kept BEFORE DELETE ON frontier_proposal
BEGIN SELECT RAISE(ABORT, 'proposals are never deleted; rejection is an appended disposition'); END;

CREATE TRIGGER frontier_proposal_finding_immutable BEFORE UPDATE ON frontier_proposal_finding
BEGIN SELECT RAISE(ABORT, 'the finding set of a proposal is immutable; revise the proposal instead'); END;
CREATE TRIGGER frontier_proposal_finding_kept BEFORE DELETE ON frontier_proposal_finding
BEGIN SELECT RAISE(ABORT, 'the finding set of a proposal is never deleted; revise the proposal instead'); END;

CREATE TRIGGER frontier_disposition_immutable BEFORE UPDATE ON frontier_disposition
BEGIN SELECT RAISE(ABORT, 'review disposition history is append-only; record a later decision as a new event'); END;
CREATE TRIGGER frontier_disposition_kept BEFORE DELETE ON frontier_disposition
BEGIN SELECT RAISE(ABORT, 'review disposition history is append-only; rejection never deletes'); END;

CREATE TRIGGER frontier_refinement_request_immutable BEFORE UPDATE ON frontier_refinement_request
BEGIN SELECT RAISE(ABORT, 'a refinement request is immutable; reject and refine again for a new one'); END;
CREATE TRIGGER frontier_refinement_request_kept BEFORE DELETE ON frontier_refinement_request
BEGIN SELECT RAISE(ABORT, 'refinement requests are never deleted; each carries the authority for a descendant run'); END;
`,
	// Migration 3 gives the records that were already revisions of each
	// other an editorial history: who revised, and why (#87).
	//
	// The ancestor links were always there, so the chain existed; what did
	// not exist was any way to ask it a question. "Show me this candidate's
	// history" meant walking ancestor pointers one query at a time, "is this
	// the current wording" had no answer at all, and "who reworded it" had
	// none either, because a record's run_id names the run that wrote it and
	// says nothing about an operator who edited it afterwards. One row per
	// record fixes all three: the chain identity is written once at insert,
	// the reason lives beside the record instead of inside the model's own
	// wording of it, and the actor distinguishes inference from its owner.
	//
	// The backfill reconstructs the chains that already exist rather than
	// starting the history at this migration. A candidate revised last month
	// would otherwise show as an original with a descendant that came from
	// nowhere. Every backfilled revision is attributed to the run that wrote
	// the record — which is the truth: before this migration no operator
	// could revise anything — and carries no reason, because inventing one
	// would be this migration putting words in a run's mouth.
	//
	// frontier_status_event gains its actor as two defaulted columns and no
	// UPDATE. The immutability trigger would abort a backfill, and rightly:
	// a migration that drops the guard protecting an append-only history in
	// order to rewrite that history is the exact move the guard exists to
	// stop. An empty actor_kind on an old row is not missing data — it means
	// "the run in run_id", which is what every row written before #87 was —
	// and the read path says so in one place.
	`
CREATE TABLE frontier_revision(
	id            TEXT PRIMARY KEY,
	entity_type   TEXT NOT NULL,
	entity_id     TEXT NOT NULL UNIQUE,
	root_id       TEXT NOT NULL,
	supersedes_id TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	actor_kind    TEXT NOT NULL,
	actor_id      TEXT NOT NULL,
	recorded_at   TEXT NOT NULL,
	payload_json  TEXT NOT NULL
);
CREATE INDEX frontier_revision_chain ON frontier_revision(root_id, seq);
CREATE INDEX frontier_revision_supersedes ON frontier_revision(supersedes_id);

CREATE TRIGGER frontier_revision_immutable BEFORE UPDATE ON frontier_revision
BEGIN SELECT RAISE(ABORT, 'a revision is immutable; the next revision supersedes this one'); END;
CREATE TRIGGER frontier_revision_kept BEFORE DELETE ON frontier_revision
BEGIN SELECT RAISE(ABORT, 'revision chains are never deleted; a record whose history vanished could not be audited'); END;

INSERT INTO frontier_revision(id, entity_type, entity_id, root_id, supersedes_id, seq,
	actor_kind, actor_id, recorded_at, payload_json)
WITH RECURSIVE chain(entity_id, root_id, supersedes_id, seq) AS (
	SELECT id, id, '', 1 FROM frontier_hypothesis WHERE ancestor_id IS NULL
	UNION ALL
	SELECT r.id, c.root_id, c.entity_id, c.seq + 1
		FROM frontier_hypothesis r JOIN chain c ON r.ancestor_id = c.entity_id
)
SELECT 'rev_' || lower(hex(randomblob(16))), 'hypothesis', c.entity_id, c.root_id, c.supersedes_id,
	c.seq, 'run', r.run_id, r.created_at, '{}'
	FROM chain c JOIN frontier_hypothesis r ON r.id = c.entity_id;

INSERT INTO frontier_revision(id, entity_type, entity_id, root_id, supersedes_id, seq,
	actor_kind, actor_id, recorded_at, payload_json)
WITH RECURSIVE chain(entity_id, root_id, supersedes_id, seq) AS (
	SELECT id, id, '', 1 FROM frontier_observation WHERE ancestor_id IS NULL
	UNION ALL
	SELECT r.id, c.root_id, c.entity_id, c.seq + 1
		FROM frontier_observation r JOIN chain c ON r.ancestor_id = c.entity_id
)
SELECT 'rev_' || lower(hex(randomblob(16))), 'observation', c.entity_id, c.root_id, c.supersedes_id,
	c.seq, 'run', r.run_id, r.created_at, '{}'
	FROM chain c JOIN frontier_observation r ON r.id = c.entity_id;

INSERT INTO frontier_revision(id, entity_type, entity_id, root_id, supersedes_id, seq,
	actor_kind, actor_id, recorded_at, payload_json)
WITH RECURSIVE chain(entity_id, root_id, supersedes_id, seq) AS (
	SELECT id, id, '', 1 FROM frontier_finding WHERE ancestor_id IS NULL
	UNION ALL
	SELECT r.id, c.root_id, c.entity_id, c.seq + 1
		FROM frontier_finding r JOIN chain c ON r.ancestor_id = c.entity_id
)
SELECT 'rev_' || lower(hex(randomblob(16))), 'finding', c.entity_id, c.root_id, c.supersedes_id,
	c.seq, 'run', r.run_id, r.created_at, '{}'
	FROM chain c JOIN frontier_finding r ON r.id = c.entity_id;

INSERT INTO frontier_revision(id, entity_type, entity_id, root_id, supersedes_id, seq,
	actor_kind, actor_id, recorded_at, payload_json)
WITH RECURSIVE chain(entity_id, root_id, supersedes_id, seq) AS (
	SELECT id, id, '', 1 FROM frontier_proposal WHERE ancestor_id IS NULL
	UNION ALL
	SELECT r.id, c.root_id, c.entity_id, c.seq + 1
		FROM frontier_proposal r JOIN chain c ON r.ancestor_id = c.entity_id
)
SELECT 'rev_' || lower(hex(randomblob(16))), 'proposal', c.entity_id, c.root_id, c.supersedes_id,
	c.seq, 'run', r.run_id, r.created_at, '{}'
	FROM chain c JOIN frontier_proposal r ON r.id = c.entity_id;

ALTER TABLE frontier_status_event ADD COLUMN actor_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE frontier_status_event ADD COLUMN actor_id TEXT NOT NULL DEFAULT '';
`,
	// Migration 4 gives a candidate somewhere to carry Babel's own warning
	// that it looks like a near-duplicate of an existing head (#87 item 4).
	//
	// The warning is a row rather than a field of the candidate's payload,
	// and rather than a `same-concept` link, and both choices are the same
	// decision made twice. A payload holds the model's own wording, which
	// this is not: it is Babel's mechanical reading of that wording, and
	// putting it there would also move the analysis-result schema shared
	// with a worker that never emits it. A link would be worse: a link is an
	// assertion that two ideas relate, and a reader could not tell an
	// investigator's judgement that two candidates are one concept from a
	// term-overlap heuristic's guess that they might be. Those are different
	// claims with different authority and they get different tables.
	//
	// Nothing here suppresses anything. The candidate is inserted whatever
	// the overlap says, in the same transaction, because §5.2 requires every
	// emitted candidate to be persisted and a duplicate silently dropped
	// would be a candidate the frontier cannot show anybody. The row records
	// the suspicion; the operator and the next run decide what it means.
	//
	// The overlap sits in payload_json rather than in a column of its own,
	// which is the same call §9 already forced on novelty and priority: the
	// plaintext allowlist admits identifiers, counts and timestamps but not
	// scores derived from content, and a fraction of two statements' shared
	// vocabulary is derived from those statements. The two record ids beside
	// it are relationship identifiers and travel in the clear.
	`
CREATE TABLE frontier_duplicate_warning(
	id            TEXT PRIMARY KEY,
	hypothesis_id TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	duplicate_of  TEXT NOT NULL REFERENCES frontier_hypothesis(id),
	recorded_at   TEXT NOT NULL,
	payload_json  TEXT NOT NULL,
	UNIQUE(hypothesis_id, duplicate_of),
	CHECK(hypothesis_id <> duplicate_of)
);
CREATE INDEX frontier_duplicate_warning_of ON frontier_duplicate_warning(duplicate_of);

CREATE TRIGGER frontier_duplicate_warning_immutable BEFORE UPDATE ON frontier_duplicate_warning
BEGIN SELECT RAISE(ABORT, 'a duplicate warning is immutable; it records what the heuristic said when the candidate was written'); END;
CREATE TRIGGER frontier_duplicate_warning_kept BEFORE DELETE ON frontier_duplicate_warning
BEGIN SELECT RAISE(ABORT, 'duplicate warnings are never deleted; a warning that can be removed is one nobody has to answer'); END;
`}

// Store is the durable hypothesis frontier. It exposes no operation that
// removes a record: §5.2 requires every candidate and its origin to persist,
// and §4.7 states that rejection never deletes. Every write is either an
// insert of a new immutable revision or an append to an event history.
type Store struct {
	db   *sql.DB
	path string

	// now supplies record timestamps. It is a field so that a test can make
	// ordering deterministic without sleeping; production never replaces it.
	now func() time.Time

	// faultAfterReject is a fault-injection seam used only by the atomicity
	// test for RejectAndRefine. It runs inside that operation's transaction,
	// between appending the rejection and creating the refinement request,
	// so a test can prove the transaction leaves neither behind. It is nil
	// on every production path.
	faultAfterReject func() error

	// sync stages every durable record for the shared catalog inside the
	// transaction that writes it. It is nil in local-only mode, which is the
	// default and a supported deployment; see publish.go.
	sync babelsync.Hook

	// refs mints the typed reference graph's shadow of every link this store
	// writes, and refsDiag receives an emission failure. Both are nil unless
	// WithReferences was passed, which is the default and leaves every write
	// path behaving exactly as it did before #113; see reference.go.
	refs     reference.Appender
	refsDiag func(error)
}

// Open opens the durable database in dir, creating the directory and applying
// any pending migrations. Unlike the rebuildable retrieval cache, a schema or
// integrity surprise here is returned as an error and never resolved by
// discarding the file: these rows are the only copy of analysis that has not
// yet synchronized.
func Open(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open durable database %s: %w", databaseFile, err)
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
		// this store stages on its own connection, so its tables have to
		// exist here before any write path opens a transaction. It is cheap
		// and idempotent.
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
		return fmt.Errorf("check durable database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("check durable database integrity: %s", integrity)
	}
	// WAL keeps a reader (the web surface listing the frontier) unblocked
	// while a run commits, and busy_timeout absorbs the brief overlap.
	// foreign_keys carries the development path's referential rules into the
	// engine, so a finding can never reference an observation that is not
	// there. All three are per connection; journal_mode persists in the file.
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enable durable database WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("set durable database busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable durable database foreign keys: %w", err)
	}
	// The migration table is shared with the durable file's other writers,
	// keyed by component so each advances independently.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(component TEXT PRIMARY KEY, version INTEGER)`); err != nil {
		return fmt.Errorf("create durable migration table: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read frontier schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("frontier schema version %d is newer than this build supports (%d)", version, len(migrations))
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
		return fmt.Errorf("begin frontier migration %d: %w", index+1, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("apply frontier migration %d: %w", index+1, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version`, component, index+1); err != nil {
		return fmt.Errorf("record frontier migration %d: %w", index+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit frontier migration %d: %w", index+1, err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database path, which operators need when the
// pending-sync journal has to be backed up before a machine is rebuilt.
func (s *Store) Path() string { return s.path }

// transact runs fn in one transaction, rolling back on any error. Every
// multi-row write in this package goes through it, because a half-written
// development path would violate the invariants the reader relies on.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// HypothesisInput creates one candidate revision. AncestorID turns the write
// into a revision of an existing candidate: the ancestor is left untouched and
// the descendant is linked to it, which is §4.7's immutability rule.
type HypothesisInput struct {
	RunID      string
	AncestorID string
	// Status is the candidate's initial lifecycle state; the empty value
	// means untriaged, which is what §5.2's persist-before-sorting rule
	// implies for a freshly emitted candidate.
	Status  Status
	Payload HypothesisPayload
	// Actor attributes the revision this write appends to the candidate's
	// chain (#87). The zero value means the run in RunID, which is what a
	// worker's own output is; a command an operator typed passes
	// Operator(id) so the chain records the person rather than the run
	// identity the command happened to borrow.
	Actor Actor
	// Reason is why this revision supersedes its ancestor. It is required
	// of a descendant and meaningless on an original: replacing an existing
	// wording without saying why produces exactly the history #87 exists to
	// prevent, one where the current state is visible and the argument for
	// it is not.
	Reason string
	// NearDuplicates are the existing candidates a caller's dedup heuristic
	// found this one resembling (#87 item 4). They are recorded as warnings
	// in the same transaction as the candidate: the write is never refused
	// or reduced because of them, so a run that emits a duplicate still
	// gets a durable record, and the record says what it is suspected of.
	NearDuplicates []NearDuplicate
}

// CreateHypothesis persists a candidate and opens its append-only status
// history. §5.2 requires every emitted candidate to be persisted before any
// sorting, so this refuses only malformed input, never uninteresting input.
func (s *Store) CreateHypothesis(ctx context.Context, in HypothesisInput) (Hypothesis, error) {
	if in.RunID == "" {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis run id is empty", ErrInvalidValue)
	}
	if err := in.Payload.validate(); err != nil {
		return Hypothesis{}, err
	}
	status := in.Status
	if status == "" {
		status = StatusUntriaged
	}
	if !status.valid() {
		return Hypothesis{}, fmt.Errorf("%w: status %q", ErrInvalidValue, status)
	}
	actor, err := revisionActor(in.Actor, in.RunID, in.AncestorID, in.Reason)
	if err != nil {
		return Hypothesis{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Hypothesis{}, err
	}
	id, err := newID("hyp")
	if err != nil {
		return Hypothesis{}, err
	}
	created := s.now()
	record := Hypothesis{
		ID:            id,
		AncestorID:    in.AncestorID,
		RunID:         in.RunID,
		SchemaVersion: RecordSchema,
		CreatedAt:     created,
		Status:        status,
		Payload:       in.Payload,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if in.AncestorID != "" {
			if err := requireRow(ctx, tx, "frontier_hypothesis", in.AncestorID); err != nil {
				return fmt.Errorf("hypothesis ancestor: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_hypothesis(
			id, ancestor_id, run_id, schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?)`,
			id, nullableID(in.AncestorID), in.RunID, RecordSchema, formatTime(created), payload); err != nil {
			return fmt.Errorf("insert hypothesis: %w", err)
		}
		revision, err := s.appendRevision(ctx, tx, revisionWrite{
			entity:     Ref{Type: EntityHypothesis, ID: id},
			supersedes: in.AncestorID,
			actor:      actor,
			reason:     in.Reason,
			recordedAt: created,
		})
		if err != nil {
			return err
		}
		if _, err := s.appendStatus(ctx, tx, statusWrite{
			hypothesisID: id,
			status:       status,
			runID:        in.RunID,
			actor:        actor,
		}); err != nil {
			return err
		}
		warnings, err := appendDuplicateWarnings(ctx, tx, id, in.NearDuplicates, created)
		if err != nil {
			return err
		}
		record.Duplicates = warnings
		// The chain root comes off the revision this transaction just wrote
		// rather than from a second walk of the ancestor column: the two agree,
		// and reading the same fact twice is how they would eventually stop
		// agreeing.
		staged, err := stagedHypothesis(record, revision.RootID, payload)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, in.RunID, staged)
		return err
	})
	if err != nil {
		return Hypothesis{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Hypothesis{}, err
	}
	// The record and its authoritative rows are durable at this point, so
	// the graph shadow is minted here and its failure is a warning: an edge
	// that could not be written is one this write is missing, never a
	// candidate the frontier does not have (#113, reference.go).
	s.mintSupersedes(ctx, EntityHypothesis, id, in.AncestorID, actor)
	s.mintDuplicates(ctx, id, record.Duplicates, actor)
	return record, nil
}

// Hypothesis reads one candidate revision with its current status.
func (s *Store) Hypothesis(ctx context.Context, id string) (Hypothesis, error) {
	row := s.db.QueryRowContext(ctx, hypothesisSelect+` WHERE h.id = ?`, id)
	record, err := scanHypothesis(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Hypothesis{}, fmt.Errorf("%w: hypothesis %q", ErrUnknownEntity, id)
	}
	return record, err
}

const hypothesisSelect = `SELECT h.id, h.ancestor_id, h.run_id, h.schema_version, h.created_at, h.payload_json,
	(SELECT e.status FROM frontier_status_event e WHERE e.hypothesis_id = h.id ORDER BY e.seq DESC LIMIT 1),
	(SELECT json_group_array(json_object('id', w.id, 'duplicate_of', w.duplicate_of,
		'overlap', json_extract(w.payload_json, '$.overlap'), 'recorded_at', w.recorded_at))
		FROM frontier_duplicate_warning w WHERE w.hypothesis_id = h.id)
	FROM frontier_hypothesis h`

func scanHypothesis(row interface{ Scan(...any) error }) (Hypothesis, error) {
	var (
		record   Hypothesis
		ancestor sql.NullString
		created  string
		payload  []byte
		status   string
		warnings []byte
	)
	if err := row.Scan(&record.ID, &ancestor, &record.RunID, &record.SchemaVersion, &created,
		&payload, &status, &warnings); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Hypothesis{}, err
		}
		return Hypothesis{}, fmt.Errorf("read hypothesis: %w", err)
	}
	record.AncestorID = ancestor.String
	record.Status = Status(status)
	parsed, err := parseTime(created)
	if err != nil {
		return Hypothesis{}, fmt.Errorf("hypothesis %s: %w", record.ID, err)
	}
	record.CreatedAt = parsed
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Hypothesis{}, fmt.Errorf("decode hypothesis %s payload: %w", record.ID, err)
	}
	record.Duplicates, err = decodeDuplicateWarnings(record.ID, warnings)
	if err != nil {
		return Hypothesis{}, err
	}
	return record, nil
}

// appendDuplicateWarnings records what a caller's dedup heuristic found about
// one candidate, inside the transaction that wrote it.
//
// A warning naming a candidate that is not there, or naming the candidate
// itself, is a caller bug and fails the write: a warning pointing at nothing
// is worse than no warning, because a reader would read it as "compare this
// against a record you cannot open". Everything else about the input is
// tolerated — the same target twice collapses to one row, and a heuristic that
// found nothing writes nothing.
func appendDuplicateWarnings(ctx context.Context, tx *sql.Tx, hypothesisID string,
	found []NearDuplicate, at time.Time) ([]DuplicateWarning, error) {
	if len(found) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(found))
	out := make([]DuplicateWarning, 0, len(found))
	for _, near := range found {
		if near.HypothesisID == hypothesisID {
			return nil, fmt.Errorf("%w: a candidate cannot be a near-duplicate of itself", ErrInvalidValue)
		}
		if near.Overlap <= 0 || near.Overlap > 1 {
			return nil, fmt.Errorf("%w: near-duplicate overlap %v is outside (0,1]",
				ErrInvalidValue, near.Overlap)
		}
		if _, dup := seen[near.HypothesisID]; dup {
			continue
		}
		seen[near.HypothesisID] = struct{}{}
		if err := requireRow(ctx, tx, "frontier_hypothesis", near.HypothesisID); err != nil {
			return nil, fmt.Errorf("near-duplicate target: %w", err)
		}
		id, err := newID("dup")
		if err != nil {
			return nil, err
		}
		payload, err := marshalPayload(DuplicateWarningPayload{Overlap: near.Overlap})
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_duplicate_warning(
			id, hypothesis_id, duplicate_of, recorded_at, payload_json)
			VALUES(?, ?, ?, ?, ?)`,
			id, hypothesisID, near.HypothesisID, formatTime(at), payload); err != nil {
			return nil, fmt.Errorf("record duplicate warning: %w", err)
		}
		out = append(out, DuplicateWarning{
			ID:           id,
			HypothesisID: hypothesisID,
			DuplicateOf:  near.HypothesisID,
			Overlap:      near.Overlap,
			RecordedAt:   at,
		})
	}
	sortDuplicateWarnings(out)
	return out, nil
}

// duplicateWire is the shape json_group_array produces for one warning. The
// aggregate is used rather than a second query so that every path reading a
// candidate reads its warnings, in one statement: a listing that quietly
// dropped them would show a frontier with no duplicates in it.
type duplicateWire struct {
	ID          string  `json:"id"`
	DuplicateOf string  `json:"duplicate_of"`
	Overlap     float64 `json:"overlap"`
	RecordedAt  string  `json:"recorded_at"`
}

func decodeDuplicateWarnings(hypothesisID string, encoded []byte) ([]DuplicateWarning, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	var wire []duplicateWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return nil, fmt.Errorf("decode duplicate warnings of %s: %w", hypothesisID, err)
	}
	if len(wire) == 0 {
		return nil, nil
	}
	out := make([]DuplicateWarning, 0, len(wire))
	for _, w := range wire {
		at, err := parseTime(w.RecordedAt)
		if err != nil {
			return nil, fmt.Errorf("duplicate warning %s: %w", w.ID, err)
		}
		out = append(out, DuplicateWarning{
			ID:           w.ID,
			HypothesisID: hypothesisID,
			DuplicateOf:  w.DuplicateOf,
			Overlap:      w.Overlap,
			RecordedAt:   at,
		})
	}
	sortDuplicateWarnings(out)
	return out, nil
}

// sortDuplicateWarnings puts the strongest resemblance first and breaks ties on
// the record it names, so the order is total and the same on every read. It is
// presentation order: §5.4's rule that rank is not strength holds here too, and
// a warning is a prompt to compare rather than a verdict about either record.
func sortDuplicateWarnings(warnings []DuplicateWarning) {
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Overlap != warnings[j].Overlap {
			return warnings[i].Overlap > warnings[j].Overlap
		}
		return warnings[i].DuplicateOf < warnings[j].DuplicateOf
	})
}

// StatusInput appends one lifecycle transition to a candidate's history.
type StatusInput struct {
	HypothesisID string
	Status       Status
	RunID        string
	Note         string
	// Actor is who caused the transition. The zero value means the run in
	// RunID; an operator-driven transition names the person, because #87
	// makes a resting candidate revivable by hand and such a transition
	// belongs to no run.
	Actor Actor
}

// SetStatus appends a lifecycle transition. It appends rather than updates so
// that the candidate's whole exploration history stays inspectable: §5.2's
// guarantee that sorting never deletes is only checkable if the transitions
// themselves survive.
func (s *Store) SetStatus(ctx context.Context, in StatusInput) (StatusEvent, error) {
	if !in.Status.valid() {
		return StatusEvent{}, fmt.Errorf("%w: status %q", ErrInvalidValue, in.Status)
	}
	actor, err := statusActor(in.Actor, in.RunID)
	if err != nil {
		return StatusEvent{}, err
	}
	var recorded StatusEvent
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "frontier_hypothesis", in.HypothesisID); err != nil {
			return fmt.Errorf("status subject: %w", err)
		}
		event, err := s.appendStatus(ctx, tx, statusWrite{
			hypothesisID: in.HypothesisID,
			status:       in.Status,
			runID:        in.RunID,
			actor:        actor,
			note:         in.Note,
		})
		recorded = event
		return err
	})
	if err != nil {
		return StatusEvent{}, err
	}
	return recorded, nil
}

// statusWrite is one lifecycle transition about to be appended. It is a
// struct rather than a parameter list because the actor made it the sixth
// argument, and six positional strings at four call sites is where a status
// silently lands under the wrong run identity.
type statusWrite struct {
	hypothesisID string
	status       Status
	runID        string
	actor        Actor
	note         string
}

func (s *Store) appendStatus(ctx context.Context, tx *sql.Tx, in statusWrite) (StatusEvent, error) {
	id, err := newID("sta")
	if err != nil {
		return StatusEvent{}, err
	}
	payload := StatusPayload{Note: in.note}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return StatusEvent{}, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM frontier_status_event WHERE hypothesis_id = ?`,
		in.hypothesisID).Scan(&seq); err != nil {
		return StatusEvent{}, fmt.Errorf("next status sequence: %w", err)
	}
	recorded := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_status_event(
		id, hypothesis_id, seq, status, run_id, actor_kind, actor_id, recorded_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.hypothesisID, seq, string(in.status), in.runID,
		string(in.actor.Kind), in.actor.ID, formatTime(recorded), encoded); err != nil {
		return StatusEvent{}, fmt.Errorf("append status event: %w", err)
	}
	return StatusEvent{
		ID:           id,
		HypothesisID: in.hypothesisID,
		Sequence:     seq,
		Status:       in.status,
		RunID:        in.runID,
		Actor:        in.actor,
		RecordedAt:   recorded,
		Payload:      payload,
	}, nil
}

// StatusHistory reads a candidate's lifecycle history in order.
func (s *Store) StatusHistory(ctx context.Context, hypothesisID string) ([]StatusEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, hypothesis_id, seq, status, run_id,
		actor_kind, actor_id, recorded_at, payload_json
		FROM frontier_status_event WHERE hypothesis_id = ? ORDER BY seq`, hypothesisID)
	if err != nil {
		return nil, fmt.Errorf("read status history: %w", err)
	}
	defer rows.Close()
	var history []StatusEvent
	for rows.Next() {
		var (
			record    StatusEvent
			status    string
			actorKind string
			actorID   string
			recorded  string
			payload   []byte
		)
		if err := rows.Scan(&record.ID, &record.HypothesisID, &record.Sequence, &status, &record.RunID,
			&actorKind, &actorID, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("read status history: %w", err)
		}
		record.Status = Status(status)
		record.Actor = storedActor(actorKind, actorID, record.RunID)
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("status event %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("decode status event %s payload: %w", record.ID, err)
		}
		history = append(history, record)
	}
	return history, rows.Err()
}

// unexploredStatuses are the lifecycle states that leave a candidate on the
// frontier: never triaged, waiting for a run, or deferred by a finite run.
// `investigating` is excluded because a run owns it, and `rejected` and
// `promoted` are terminal for exploration without the record going anywhere.
var unexploredStatuses = []Status{StatusUntriaged, StatusQueued, StatusDeferred}

// Unexplored answers "what is still unexplored" after a process restart,
// which §5.2 requires: a finite run defers its remainder rather than erasing
// it, so the frontier must survive the run that produced it.
//
// Only leaf revisions are returned. A candidate that has been revised is
// represented by its descendant, so a resumed run explores the current
// wording rather than a superseded one.
//
// Ordering is by priority then creation, and priority is read out of the
// payload because §9's plaintext allowlist has no room for a content-derived
// score. That is affordable here — the durable file is local and its payloads
// are in the clear — and after the sync slice the same ordering happens over
// the rebuildable decrypted local index rather than in PostgreSQL. Per §5.4,
// this order is attention triage and never evidence strength.
func (s *Store) Unexplored(ctx context.Context, limit int) ([]Hypothesis, error) {
	placeholders := make([]string, len(unexploredStatuses))
	args := make([]any, 0, len(unexploredStatuses)+1)
	for i, status := range unexploredStatuses {
		placeholders[i] = "?"
		args = append(args, string(status))
	}
	query := hypothesisSelect + `
		WHERE NOT EXISTS(SELECT 1 FROM frontier_hypothesis d WHERE d.ancestor_id = h.id)
		AND (SELECT e.status FROM frontier_status_event e WHERE e.hypothesis_id = h.id ORDER BY e.seq DESC LIMIT 1)
			IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY json_extract(h.payload_json, '$.priority') DESC, h.created_at, h.id`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read unexplored frontier: %w", err)
	}
	defer rows.Close()
	var frontier []Hypothesis
	for rows.Next() {
		record, err := scanHypothesis(rows)
		if err != nil {
			return nil, err
		}
		frontier = append(frontier, record)
	}
	return frontier, rows.Err()
}

// DeferFrontier records that a finite run stopped with these candidates
// unexplored. §5.2: finite runs defer the unexplored frontier, they do not
// erase it. One transaction, so a run's checkpoint is all or nothing.
func (s *Store) DeferFrontier(ctx context.Context, runID string, ids []string, note string) ([]StatusEvent, error) {
	actor, err := statusActor(Actor{}, runID)
	if err != nil {
		return nil, err
	}
	var deferred []StatusEvent
	err = s.transact(ctx, func(tx *sql.Tx) error {
		deferred = deferred[:0]
		for _, id := range ids {
			if err := requireRow(ctx, tx, "frontier_hypothesis", id); err != nil {
				return fmt.Errorf("defer subject: %w", err)
			}
			event, err := s.appendStatus(ctx, tx, statusWrite{
				hypothesisID: id,
				status:       StatusDeferred,
				runID:        runID,
				actor:        actor,
				note:         note,
			})
			if err != nil {
				return err
			}
			deferred = append(deferred, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deferred, nil
}

// LinkInput asserts one typed relationship between two candidates.
type LinkInput struct {
	FromID string
	ToID   string
	Type   LinkType
	Note   string
}

// Link asserts a §4.2 typed relationship. It records how two ideas relate and
// nothing else: no status changes, no record is superseded in place, and a
// `contradicts` link neither rejects nor demotes either side. Deciding what a
// relationship means about a candidate's lifecycle is a separate, explicit
// act.
func (s *Store) Link(ctx context.Context, in LinkInput) (Link, error) {
	if !in.Type.valid() {
		return Link{}, fmt.Errorf("%w: link type %q", ErrInvalidValue, in.Type)
	}
	if in.FromID == in.ToID {
		return Link{}, fmt.Errorf("%w: hypothesis %q linked to itself", ErrInvalidValue, in.FromID)
	}
	payload := LinkPayload{Note: in.Note}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Link{}, err
	}
	id, err := newID("lnk")
	if err != nil {
		return Link{}, err
	}
	created := s.now()
	record := Link{ID: id, FromID: in.FromID, ToID: in.ToID, Type: in.Type, CreatedAt: created, Payload: payload}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "frontier_hypothesis", in.FromID); err != nil {
			return fmt.Errorf("link source: %w", err)
		}
		if err := requireRow(ctx, tx, "frontier_hypothesis", in.ToID); err != nil {
			return fmt.Errorf("link target: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_hypothesis_link(
			id, from_id, to_id, link_type, created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
			id, in.FromID, in.ToID, string(in.Type), formatTime(created), encoded); err != nil {
			return fmt.Errorf("insert hypothesis link: %w", err)
		}
		staged, err := stagedLink(record, encoded)
		if err != nil {
			return err
		}
		// A link carries no run id: LinkInput has none, and asserting a
		// relationship is an act in its own right rather than part of a run's
		// closure. So it publishes as its own commit of one, immediately.
		pub, err = s.stage(ctx, tx, "", staged)
		return err
	})
	if err != nil {
		return Link{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Link{}, err
	}
	return record, nil
}

// LinksFrom reads the relationships this candidate asserts about others.
func (s *Store) LinksFrom(ctx context.Context, hypothesisID string) ([]Link, error) {
	return s.links(ctx, `from_id = ?`, hypothesisID)
}

// LinksTo reads the relationships other candidates assert about this one. It
// is the reverse direction of LinksFrom, so lineage is traversable from either
// end: a superseded candidate finds what superseded it without scanning.
func (s *Store) LinksTo(ctx context.Context, hypothesisID string) ([]Link, error) {
	return s.links(ctx, `to_id = ?`, hypothesisID)
}

func (s *Store) links(ctx context.Context, where, id string) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, from_id, to_id, link_type, created_at, payload_json
		FROM frontier_hypothesis_link WHERE `+where+` ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("read hypothesis links: %w", err)
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var (
			record    Link
			linkType  string
			createdAt string
			payload   []byte
		)
		if err := rows.Scan(&record.ID, &record.FromID, &record.ToID, &linkType, &createdAt, &payload); err != nil {
			return nil, fmt.Errorf("read hypothesis links: %w", err)
		}
		record.Type = LinkType(linkType)
		if record.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("link %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("decode link %s payload: %w", record.ID, err)
		}
		links = append(links, record)
	}
	return links, rows.Err()
}

// ObservationInput creates one observation revision against a hypothesis.
type ObservationInput struct {
	HypothesisID  string
	RunID         string
	AncestorID    string
	RecipeID      string
	RecipeVersion int
	Payload       ObservationPayload
	// Actor and Reason carry the revision chain entry this write appends,
	// on the same terms as HypothesisInput's.
	Actor  Actor
	Reason string
}

// CreateObservation persists a §4.3 claim. It refuses an observation with no
// evidence, refuses one whose counter-evidence position is unstated, and
// refuses one that develops no hypothesis, because §4.2's path has no step
// that produces a free-floating claim.
func (s *Store) CreateObservation(ctx context.Context, in ObservationInput) (Observation, error) {
	if in.HypothesisID == "" {
		return Observation{}, fmt.Errorf("%w: observation names no hypothesis", ErrInvalidValue)
	}
	if in.RunID == "" {
		return Observation{}, fmt.Errorf("%w: observation run id is empty", ErrInvalidValue)
	}
	if in.RecipeID == "" {
		return Observation{}, fmt.Errorf("%w: observation recipe id is empty", ErrInvalidValue)
	}
	if err := in.Payload.validate(); err != nil {
		return Observation{}, err
	}
	actor, err := revisionActor(in.Actor, in.RunID, in.AncestorID, in.Reason)
	if err != nil {
		return Observation{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Observation{}, err
	}
	id, err := newID("obs")
	if err != nil {
		return Observation{}, err
	}
	created := s.now()
	record := Observation{
		ID:            id,
		AncestorID:    in.AncestorID,
		HypothesisID:  in.HypothesisID,
		RunID:         in.RunID,
		RecipeID:      in.RecipeID,
		RecipeVersion: in.RecipeVersion,
		SchemaVersion: RecordSchema,
		EvidenceCount: len(in.Payload.Evidence),
		CreatedAt:     created,
		Payload:       in.Payload,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "frontier_hypothesis", in.HypothesisID); err != nil {
			return fmt.Errorf("observation hypothesis: %w", err)
		}
		if in.AncestorID != "" {
			if err := requireRow(ctx, tx, "frontier_observation", in.AncestorID); err != nil {
				return fmt.Errorf("observation ancestor: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_observation(
			id, ancestor_id, hypothesis_id, run_id, recipe_id, recipe_version,
			schema_version, evidence_count, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, nullableID(in.AncestorID), in.HypothesisID, in.RunID, in.RecipeID, in.RecipeVersion,
			RecordSchema, record.EvidenceCount, formatTime(created), payload); err != nil {
			return fmt.Errorf("insert observation: %w", err)
		}
		revision, err := s.appendRevision(ctx, tx, revisionWrite{
			entity:     Ref{Type: EntityObservation, ID: id},
			supersedes: in.AncestorID,
			actor:      actor,
			reason:     in.Reason,
			recordedAt: created,
		})
		if err != nil {
			return err
		}
		staged, err := stagedObservation(record, revision.RootID, payload)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, in.RunID, staged)
		return err
	})
	if err != nil {
		return Observation{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Observation{}, err
	}
	s.mintSupersedes(ctx, EntityObservation, id, in.AncestorID, actor)
	return record, nil
}

const observationSelect = `SELECT id, ancestor_id, hypothesis_id, run_id, recipe_id, recipe_version,
	schema_version, evidence_count, created_at, payload_json FROM frontier_observation`

// Observation reads one claim revision.
func (s *Store) Observation(ctx context.Context, id string) (Observation, error) {
	row := s.db.QueryRowContext(ctx, observationSelect+` WHERE id = ?`, id)
	record, err := scanObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{}, fmt.Errorf("%w: observation %q", ErrUnknownEntity, id)
	}
	return record, err
}

// ObservationsFor reads every claim developed against one candidate, which is
// what a finding consolidates and what a challenger pass examines (§5.4).
func (s *Store) ObservationsFor(ctx context.Context, hypothesisID string) ([]Observation, error) {
	rows, err := s.db.QueryContext(ctx, observationSelect+` WHERE hypothesis_id = ? ORDER BY created_at, id`, hypothesisID)
	if err != nil {
		return nil, fmt.Errorf("read observations: %w", err)
	}
	defer rows.Close()
	var observations []Observation
	for rows.Next() {
		record, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, record)
	}
	return observations, rows.Err()
}

func scanObservation(row interface{ Scan(...any) error }) (Observation, error) {
	var (
		record   Observation
		ancestor sql.NullString
		created  string
		payload  []byte
	)
	if err := row.Scan(&record.ID, &ancestor, &record.HypothesisID, &record.RunID, &record.RecipeID,
		&record.RecipeVersion, &record.SchemaVersion, &record.EvidenceCount, &created, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Observation{}, err
		}
		return Observation{}, fmt.Errorf("read observation: %w", err)
	}
	record.AncestorID = ancestor.String
	parsed, err := parseTime(created)
	if err != nil {
		return Observation{}, fmt.Errorf("observation %s: %w", record.ID, err)
	}
	record.CreatedAt = parsed
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Observation{}, fmt.Errorf("decode observation %s payload: %w", record.ID, err)
	}
	return record, nil
}

// FindingInput consolidates observations into one finding revision.
type FindingInput struct {
	RunID          string
	AncestorID     string
	ObservationIDs []string
	Payload        FindingPayload
	// Actor and Reason carry the revision chain entry this write appends,
	// on the same terms as HypothesisInput's.
	Actor  Actor
	Reason string
}

// CreateFinding consolidates §4.4 observations. A finding with no supporting
// observation is refused rather than warned about: §4.2 states that developed
// hypotheses never skip observations, so a finding assembled directly from a
// hypothesis would be a claim with no provenance behind it.
func (s *Store) CreateFinding(ctx context.Context, in FindingInput) (Finding, error) {
	if in.RunID == "" {
		return Finding{}, fmt.Errorf("%w: finding run id is empty", ErrInvalidValue)
	}
	if len(in.ObservationIDs) == 0 {
		return Finding{}, ErrNoObservations
	}
	if err := in.Payload.validate(); err != nil {
		return Finding{}, err
	}
	actor, err := revisionActor(in.Actor, in.RunID, in.AncestorID, in.Reason)
	if err != nil {
		return Finding{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Finding{}, err
	}
	id, err := newID("fnd")
	if err != nil {
		return Finding{}, err
	}
	created := s.now()
	record := Finding{
		ID:             id,
		AncestorID:     in.AncestorID,
		RunID:          in.RunID,
		SchemaVersion:  RecordSchema,
		CreatedAt:      created,
		ObservationIDs: append([]string(nil), in.ObservationIDs...),
		Payload:        in.Payload,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if in.AncestorID != "" {
			if err := requireRow(ctx, tx, "frontier_finding", in.AncestorID); err != nil {
				return fmt.Errorf("finding ancestor: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_finding(
			id, ancestor_id, run_id, schema_version, created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
			id, nullableID(in.AncestorID), in.RunID, RecordSchema, formatTime(created), payload); err != nil {
			return fmt.Errorf("insert finding: %w", err)
		}
		for position, observationID := range in.ObservationIDs {
			if err := requireRow(ctx, tx, "frontier_observation", observationID); err != nil {
				return fmt.Errorf("finding observation: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_finding_observation(
				finding_id, observation_id, position) VALUES(?, ?, ?)`, id, observationID, position); err != nil {
				return fmt.Errorf("link finding observation: %w", err)
			}
		}
		revision, err := s.appendRevision(ctx, tx, revisionWrite{
			entity:     Ref{Type: EntityFinding, ID: id},
			supersedes: in.AncestorID,
			actor:      actor,
			reason:     in.Reason,
			recordedAt: created,
		})
		if err != nil {
			return err
		}
		hypotheses, err := findingHypotheses(ctx, tx, id)
		if err != nil {
			return err
		}
		record.HypothesisIDs = hypotheses
		staged, err := stagedFinding(record, revision.RootID, payload)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, in.RunID, staged)
		return err
	})
	if err != nil {
		return Finding{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Finding{}, err
	}
	s.mintSupersedes(ctx, EntityFinding, id, in.AncestorID, actor)
	return record, nil
}

// Finding reads one consolidation revision with its supporting observations
// and the candidates they developed.
func (s *Store) Finding(ctx context.Context, id string) (Finding, error) {
	var (
		record   Finding
		ancestor sql.NullString
		created  string
		payload  []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, ancestor_id, run_id, schema_version, created_at, payload_json
		FROM frontier_finding WHERE id = ?`, id).
		Scan(&record.ID, &ancestor, &record.RunID, &record.SchemaVersion, &created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Finding{}, fmt.Errorf("%w: finding %q", ErrUnknownEntity, id)
	}
	if err != nil {
		return Finding{}, fmt.Errorf("read finding: %w", err)
	}
	record.AncestorID = ancestor.String
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Finding{}, fmt.Errorf("finding %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Finding{}, fmt.Errorf("decode finding %s payload: %w", id, err)
	}
	if record.ObservationIDs, err = queryIDs(ctx, s.db, `SELECT observation_id FROM frontier_finding_observation
		WHERE finding_id = ? ORDER BY position`, id); err != nil {
		return Finding{}, err
	}
	if record.HypothesisIDs, err = findingHypotheses(ctx, s.db, id); err != nil {
		return Finding{}, err
	}
	return record, nil
}

// findingHypotheses derives the candidates a finding developed by walking its
// observations, so §4.5's "linked hypotheses" cannot be asserted independently
// of the path the records actually took.
func findingHypotheses(ctx context.Context, q querier, findingID string) ([]string, error) {
	return queryIDs(ctx, q, `SELECT DISTINCT o.hypothesis_id FROM frontier_finding_observation fo
		JOIN frontier_observation o ON o.id = fo.observation_id
		WHERE fo.finding_id = ? ORDER BY o.hypothesis_id`, findingID)
}

// ProposalInput assembles findings into one proposal revision.
type ProposalInput struct {
	RunID      string
	AncestorID string
	FindingIDs []string
	Payload    ProposalPayload
	// Actor and Reason carry the revision chain entry this write appends,
	// on the same terms as HypothesisInput's.
	Actor  Actor
	Reason string
}

// CreateProposal persists a §4.5 review artifact. A proposal with no finding
// is refused: §4.5 defines a proposal as suggested by one or more findings,
// and §4.8 separately forbids any path that reaches a proposal without
// travelling hypothesis -> observation -> finding first.
func (s *Store) CreateProposal(ctx context.Context, in ProposalInput) (Proposal, error) {
	if in.RunID == "" {
		return Proposal{}, fmt.Errorf("%w: proposal run id is empty", ErrInvalidValue)
	}
	if len(in.FindingIDs) == 0 {
		return Proposal{}, ErrNoFindings
	}
	if err := in.Payload.validate(); err != nil {
		return Proposal{}, err
	}
	actor, err := revisionActor(in.Actor, in.RunID, in.AncestorID, in.Reason)
	if err != nil {
		return Proposal{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Proposal{}, err
	}
	id, err := newID("pro")
	if err != nil {
		return Proposal{}, err
	}
	created := s.now()
	record := Proposal{
		ID:            id,
		AncestorID:    in.AncestorID,
		RunID:         in.RunID,
		SchemaVersion: RecordSchema,
		CreatedAt:     created,
		FindingIDs:    append([]string(nil), in.FindingIDs...),
		ReviewStatus:  ReviewNew,
		Payload:       in.Payload,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if in.AncestorID != "" {
			if err := requireRow(ctx, tx, "frontier_proposal", in.AncestorID); err != nil {
				return fmt.Errorf("proposal ancestor: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_proposal(
			id, ancestor_id, run_id, schema_version, created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
			id, nullableID(in.AncestorID), in.RunID, RecordSchema, formatTime(created), payload); err != nil {
			return fmt.Errorf("insert proposal: %w", err)
		}
		for position, findingID := range in.FindingIDs {
			if err := requireRow(ctx, tx, "frontier_finding", findingID); err != nil {
				return fmt.Errorf("proposal finding: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_proposal_finding(
				proposal_id, finding_id, position) VALUES(?, ?, ?)`, id, findingID, position); err != nil {
				return fmt.Errorf("link proposal finding: %w", err)
			}
		}
		revision, err := s.appendRevision(ctx, tx, revisionWrite{
			entity:     Ref{Type: EntityProposal, ID: id},
			supersedes: in.AncestorID,
			actor:      actor,
			reason:     in.Reason,
			recordedAt: created,
		})
		if err != nil {
			return err
		}
		hypotheses, err := proposalHypotheses(ctx, tx, id)
		if err != nil {
			return err
		}
		record.HypothesisIDs = hypotheses
		staged, err := stagedProposal(record, revision.RootID, payload)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, in.RunID, staged)
		return err
	})
	if err != nil {
		return Proposal{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Proposal{}, err
	}
	s.mintSupersedes(ctx, EntityProposal, id, in.AncestorID, actor)
	return record, nil
}

// Proposal reads one review artifact with its derived lineage and its review
// status, which is computed from the disposition history rather than stored.
func (s *Store) Proposal(ctx context.Context, id string) (Proposal, error) {
	var (
		record   Proposal
		ancestor sql.NullString
		created  string
		payload  []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, ancestor_id, run_id, schema_version, created_at, payload_json
		FROM frontier_proposal WHERE id = ?`, id).
		Scan(&record.ID, &ancestor, &record.RunID, &record.SchemaVersion, &created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, fmt.Errorf("%w: proposal %q", ErrUnknownEntity, id)
	}
	if err != nil {
		return Proposal{}, fmt.Errorf("read proposal: %w", err)
	}
	record.AncestorID = ancestor.String
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Proposal{}, fmt.Errorf("proposal %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal %s payload: %w", id, err)
	}
	if record.FindingIDs, err = queryIDs(ctx, s.db, `SELECT finding_id FROM frontier_proposal_finding
		WHERE proposal_id = ? ORDER BY position`, id); err != nil {
		return Proposal{}, err
	}
	if record.HypothesisIDs, err = proposalHypotheses(ctx, s.db, id); err != nil {
		return Proposal{}, err
	}
	if record.ReviewStatus, err = s.ReviewStatus(ctx, Ref{Type: EntityProposal, ID: id}); err != nil {
		return Proposal{}, err
	}
	return record, nil
}

func proposalHypotheses(ctx context.Context, q querier, proposalID string) ([]string, error) {
	return queryIDs(ctx, q, `SELECT DISTINCT o.hypothesis_id FROM frontier_proposal_finding pf
		JOIN frontier_finding_observation fo ON fo.finding_id = pf.finding_id
		JOIN frontier_observation o ON o.id = fo.observation_id
		WHERE pf.proposal_id = ? ORDER BY o.hypothesis_id`, proposalID)
}

// DispositionInput records one §4.7 review decision.
type DispositionInput struct {
	Subject     Ref
	Disposition Disposition
	// ReviewerID attributes the decision; §4.7 makes operator context
	// attributed guidance, and an anonymous decision could not be attributed.
	ReviewerID    string
	ContextID     string
	DuplicateOfID string
	Note          string
}

// Decide appends a §4.7 disposition. There is no update or delete path:
// rejection never removes the record, and reconsidering appends another event
// so both remain readable in order.
//
// The vocabulary is the four §4.7 values. `refine` is not among them by
// design, so a refinement can only be created by RejectAndRefine, where a
// recorded rejection authorizes it.
func (s *Store) Decide(ctx context.Context, in DispositionInput) (DispositionEvent, error) {
	var (
		recorded DispositionEvent
		pub      publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		event, err := s.appendDisposition(ctx, tx, in)
		if err != nil {
			return err
		}
		recorded = event
		encoded, err := marshalPayload(event.Payload)
		if err != nil {
			return err
		}
		staged, err := stagedDisposition(event, encoded)
		if err != nil {
			return err
		}
		// A decision names no producing run, and that is not an omission: a
		// disposition is an operator's own act about a record, so it is its own
		// commit of one rather than part of the closure of whichever run
		// happened to produce the record it is about - a closure that run has
		// already declared and closed.
		pub, err = s.stage(ctx, tx, "", staged)
		return err
	})
	if err != nil {
		return DispositionEvent{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return DispositionEvent{}, err
	}
	return recorded, nil
}

func (s *Store) appendDisposition(ctx context.Context, tx *sql.Tx, in DispositionInput) (DispositionEvent, error) {
	if !in.Disposition.valid() {
		return DispositionEvent{}, fmt.Errorf("%w: disposition %q", ErrInvalidValue, in.Disposition)
	}
	if in.ReviewerID == "" {
		return DispositionEvent{}, fmt.Errorf("%w: disposition reviewer is empty", ErrInvalidValue)
	}
	if err := s.requireSubject(ctx, tx, in.Subject, true); err != nil {
		return DispositionEvent{}, err
	}
	if in.Disposition == DispositionDuplicate && in.DuplicateOfID == "" {
		return DispositionEvent{}, fmt.Errorf("%w: duplicate disposition names no original", ErrInvalidValue)
	}
	if in.DuplicateOfID != "" {
		if err := s.requireSubject(ctx, tx, Ref{Type: in.Subject.Type, ID: in.DuplicateOfID}, false); err != nil {
			return DispositionEvent{}, fmt.Errorf("duplicate original: %w", err)
		}
	}
	id, err := newID("dsp")
	if err != nil {
		return DispositionEvent{}, err
	}
	payload := DispositionPayload{Note: in.Note}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return DispositionEvent{}, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM frontier_disposition
		WHERE subject_type = ? AND subject_id = ?`, string(in.Subject.Type), in.Subject.ID).Scan(&seq); err != nil {
		return DispositionEvent{}, fmt.Errorf("next disposition sequence: %w", err)
	}
	recorded := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_disposition(
		id, subject_type, subject_id, seq, disposition, reviewer_id, context_id,
		duplicate_of_id, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(in.Subject.Type), in.Subject.ID, seq, string(in.Disposition), in.ReviewerID,
		in.ContextID, in.DuplicateOfID, formatTime(recorded), encoded); err != nil {
		return DispositionEvent{}, fmt.Errorf("append disposition: %w", err)
	}
	return DispositionEvent{
		ID:            id,
		Subject:       in.Subject,
		Sequence:      seq,
		Disposition:   in.Disposition,
		ReviewerID:    in.ReviewerID,
		ContextID:     in.ContextID,
		DuplicateOfID: in.DuplicateOfID,
		RecordedAt:    recorded,
		Payload:       payload,
	}, nil
}

// RejectAndRefine is §4.7's single atomic operation: it appends a `reject`
// event and creates the refinement request that rejection authorizes, in one
// transaction. The two exist together or not at all, which is why there is no
// standalone `refine` disposition and no way to author a refinement request
// that no reviewer asked for.
//
// The disposition argument's Disposition field is ignored: this operation
// records a rejection by definition.
func (s *Store) RejectAndRefine(ctx context.Context, in DispositionInput, refinement RefinementPayload) (DispositionEvent, RefinementRequest, error) {
	if err := refinement.validate(); err != nil {
		return DispositionEvent{}, RefinementRequest{}, err
	}
	in.Disposition = DispositionReject
	var (
		rejection DispositionEvent
		request   RefinementRequest
		closure   babelsync.Closure
		publish   bool
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		event, err := s.appendDisposition(ctx, tx, in)
		if err != nil {
			return err
		}
		if s.faultAfterReject != nil {
			if err := s.faultAfterReject(); err != nil {
				return fmt.Errorf("reject and refine: %w", err)
			}
		}
		id, err := newID("rfr")
		if err != nil {
			return err
		}
		encoded, err := marshalPayload(refinement)
		if err != nil {
			return err
		}
		created := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_refinement_request(
			id, disposition_id, subject_type, subject_id, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?)`,
			id, event.ID, string(in.Subject.Type), in.Subject.ID, formatTime(created), encoded); err != nil {
			return fmt.Errorf("create refinement request: %w", err)
		}
		rejection = event
		request = RefinementRequest{
			ID:            id,
			DispositionID: event.ID,
			Subject:       in.Subject,
			CreatedAt:     created,
			Payload:       refinement,
		}
		if s.sync == nil {
			return nil
		}
		// The two records publish as ONE closure of two rather than as two
		// closures of one, and that is §4.7's atomicity carried across the
		// boundary this store cannot reach on its own. Locally the transaction
		// makes the rejection and the refinement exist together or not at all;
		// remotely, two independent commits could leave the fleet holding a
		// rejection that authorized nothing, or a refinement request no
		// recorded rejection authorizes - which is exactly the state SPEC.md
		// §4.7 says cannot exist. A run row is the visibility boundary, so one
		// closure is what makes the pair atomic there too.
		//
		// Its run id is the rejection's, because the rejection is what
		// authorized the other half; the refinement can never exist without it.
		notePayload, err := marshalPayload(event.Payload)
		if err != nil {
			return err
		}
		stagedReject, err := stagedDisposition(event, notePayload)
		if err != nil {
			return err
		}
		stagedReject.RunID = event.ID
		if err := s.sync.StageTx(ctx, tx, stagedReject); err != nil {
			return err
		}
		stagedRequest, err := stagedRefinement(request, in.ReviewerID, encoded)
		if err != nil {
			return err
		}
		stagedRequest.RunID = event.ID
		if err := s.sync.StageTx(ctx, tx, stagedRequest); err != nil {
			return err
		}
		closure = babelsync.Closure{RunID: event.ID}
		publish = true
		return s.sync.DeclareTx(ctx, tx, closure)
	})
	if err != nil {
		return DispositionEvent{}, RefinementRequest{}, err
	}
	if err := s.commit(ctx, publication{closure: closure, publish: publish}); err != nil {
		return DispositionEvent{}, RefinementRequest{}, err
	}
	return rejection, request, nil
}

// DispositionHistory reads a subject's review decisions in the order they were
// recorded. §4.7's history is append-only, so a rejected then reconsidered
// record shows both events rather than only the latest.
func (s *Store) DispositionHistory(ctx context.Context, subject Ref) ([]DispositionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, subject_type, subject_id, seq, disposition,
		reviewer_id, context_id, duplicate_of_id, recorded_at, payload_json
		FROM frontier_disposition WHERE subject_type = ? AND subject_id = ? ORDER BY seq`,
		string(subject.Type), subject.ID)
	if err != nil {
		return nil, fmt.Errorf("read disposition history: %w", err)
	}
	defer rows.Close()
	var history []DispositionEvent
	for rows.Next() {
		var (
			record      DispositionEvent
			subjectType string
			disposition string
			recorded    string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &subjectType, &record.Subject.ID, &record.Sequence, &disposition,
			&record.ReviewerID, &record.ContextID, &record.DuplicateOfID, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("read disposition history: %w", err)
		}
		record.Subject.Type = EntityType(subjectType)
		record.Disposition = Disposition(disposition)
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("disposition %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("decode disposition %s payload: %w", record.ID, err)
		}
		history = append(history, record)
	}
	return history, rows.Err()
}

// RefinementRequests reads the refinement requests authorized against one
// subject, oldest first.
func (s *Store) RefinementRequests(ctx context.Context, subject Ref) ([]RefinementRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.disposition_id, r.subject_type, r.subject_id,
		r.created_at, r.payload_json FROM frontier_refinement_request r
		JOIN frontier_disposition d ON d.id = r.disposition_id
		WHERE r.subject_type = ? AND r.subject_id = ? ORDER BY d.seq`,
		string(subject.Type), subject.ID)
	if err != nil {
		return nil, fmt.Errorf("read refinement requests: %w", err)
	}
	defer rows.Close()
	var requests []RefinementRequest
	for rows.Next() {
		var (
			record      RefinementRequest
			subjectType string
			created     string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &record.DispositionID, &subjectType, &record.Subject.ID,
			&created, &payload); err != nil {
			return nil, fmt.Errorf("read refinement requests: %w", err)
		}
		record.Subject.Type = EntityType(subjectType)
		if record.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("refinement request %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("decode refinement request %s payload: %w", record.ID, err)
		}
		requests = append(requests, record)
	}
	return requests, rows.Err()
}

// ReviewStatus derives §4.5's review status from the append-only disposition
// history. Deriving rather than storing removes the possibility that a status
// disagrees with the events behind it, and makes `refine-requested` provably
// mean "a rejection authorized a refinement request", since that is the only
// way both rows can exist.
func (s *Store) ReviewStatus(ctx context.Context, subject Ref) (ReviewStatus, error) {
	var (
		disposition string
		refinements int
	)
	err := s.db.QueryRowContext(ctx, `SELECT d.disposition,
		(SELECT COUNT(*) FROM frontier_refinement_request r WHERE r.disposition_id = d.id)
		FROM frontier_disposition d WHERE d.subject_type = ? AND d.subject_id = ?
		ORDER BY d.seq DESC LIMIT 1`, string(subject.Type), subject.ID).Scan(&disposition, &refinements)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewNew, nil
	}
	if err != nil {
		return "", fmt.Errorf("read review status: %w", err)
	}
	switch Disposition(disposition) {
	case DispositionAccept:
		return ReviewAccepted, nil
	case DispositionReject:
		if refinements > 0 {
			return ReviewRefineRequested, nil
		}
		return ReviewRejected, nil
	case DispositionDefer:
		return ReviewDeferred, nil
	case DispositionDuplicate:
		return ReviewDuplicate, nil
	}
	return "", fmt.Errorf("%w: stored disposition %q", ErrInvalidValue, disposition)
}

// querier is the subset of *sql.DB and *sql.Tx the read helpers need, so one
// derivation query serves both a fresh read and the transaction that created
// the record.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// tableFor maps an entity kind to its table. It is a closed switch rather than
// string interpolation of a caller value, so no caller-supplied kind reaches a
// query.
func tableFor(kind EntityType) (string, error) {
	switch kind {
	case EntityHypothesis:
		return "frontier_hypothesis", nil
	case EntityObservation:
		return "frontier_observation", nil
	case EntityFinding:
		return "frontier_finding", nil
	case EntityProposal:
		return "frontier_proposal", nil
	}
	return "", fmt.Errorf("%w: entity type %q", ErrInvalidValue, kind)
}

// requireSubject checks that a Ref names a record this store holds, and when
// mustReview is set, that §6.7 makes that kind reviewable at all.
func (s *Store) requireSubject(ctx context.Context, q querier, subject Ref, mustReview bool) error {
	if !subject.Type.valid() {
		return fmt.Errorf("%w: entity type %q", ErrInvalidValue, subject.Type)
	}
	if mustReview && !subject.Type.reviewable() {
		return fmt.Errorf("%w: %s", ErrNotReviewable, subject.Type)
	}
	table, err := tableFor(subject.Type)
	if err != nil {
		return err
	}
	return requireRow(ctx, q, table, subject.ID)
}

// requireRow turns a dangling reference into ErrUnknownEntity before the
// insert, so callers get the reason rather than a foreign-key message.
func requireRow(ctx context.Context, q querier, table, id string) error {
	var found int
	// table is never caller-supplied: every call site passes a constant or a
	// value from tableFor's closed switch.
	err := q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %q", ErrUnknownEntity, strings.TrimPrefix(table, "frontier_"), id)
	}
	if err != nil {
		return fmt.Errorf("check %s %q: %w", table, id, err)
	}
	return nil
}

func queryIDs(ctx context.Context, q querier, query, arg string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("read related ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read related ids: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// marshalPayload encodes the §9 encryption-bound half of a record. Every
// payload passes through here, so the later sync slice has exactly one place
// to wrap in an AEAD envelope.
func marshalPayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return encoded, nil
}

// newID generates a globally unique client-side identifier, which §9 requires
// for immutable entities and events. The prefix makes a stray ID in a log or
// error message self-describing without revealing anything about the corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// nullableID stores an absent ancestor as SQL NULL so the self-referencing
// foreign key does not have to match an empty string.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction. The
// fixed width is the whole point: time.RFC3339Nano trims trailing zeros, so
// "12:00:00.1Z" and "12:00:00.12Z" compare as text by measuring 'Z' against
// '2' and place the earlier instant second. Any ORDER BY over a timestamp
// column would then be subtly wrong in a way that only shows up when two
// events land within a tenth of a second of each other — which is exactly when
// ordering matters. A fixed fraction makes lexicographic order and
// chronological order the same relation, and time.RFC3339Nano still parses it.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime stores timestamps as UTC RFC 3339 with a fixed nine-digit
// fraction, so text order is time order.
func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

// ListFilter bounds an enumeration. A zero Limit means DefaultListLimit rather
// than everything: a corpus of thousands of records must not arrive whole
// merely because a caller forgot a bound.
type ListFilter struct {
	// Statuses restricts hypotheses to these lifecycle states. Empty means
	// every state, including rejected — §5.2 keeps every candidate, so an
	// enumeration that hid rejected ones by default would make the guarantee
	// invisible exactly where an operator looks for it.
	Statuses []Status
	// LeavesOnly returns only revisions with no descendant, so a list shows
	// current wording rather than every superseded draft beside it.
	LeavesOnly bool
	Limit      int
	Offset     int
}

// DefaultListLimit and MaxListLimit bound every enumeration.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

func (f ListFilter) bounds() (limit, offset int) {
	limit = f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	if offset = f.Offset; offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Hypotheses enumerates the frontier. It exists because the durable record is
// only as useful as it is reachable: before this, a candidate that was neither
// unexplored nor enrolled for review could be read by id and found by no
// listing, which makes §5.2's promise that nothing is lost true in storage and
// false in practice.
//
// Ordering is creation then id — a total order, and deliberately not priority.
// Unexplored triages attention; this enumerates a record set, and per §5.4 a
// list position must not read as strength.
func (s *Store) Hypotheses(ctx context.Context, filter ListFilter) ([]Hypothesis, int, error) {
	var conditions []string
	var args []any
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, len(filter.Statuses))
		for i, status := range filter.Statuses {
			if !status.valid() {
				return nil, 0, fmt.Errorf("%w: status %q", ErrInvalidValue, status)
			}
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		conditions = append(conditions, `(SELECT e.status FROM frontier_status_event e
			WHERE e.hypothesis_id = h.id ORDER BY e.seq DESC LIMIT 1) IN (`+
			strings.Join(placeholders, ", ")+`)`)
	}
	if filter.LeavesOnly {
		conditions = append(conditions,
			`NOT EXISTS(SELECT 1 FROM frontier_hypothesis d WHERE d.ancestor_id = h.id)`)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	total, err := s.count(ctx, `SELECT count(*) FROM frontier_hypothesis h`+where, args...)
	if err != nil {
		return nil, 0, err
	}

	limit, offset := filter.bounds()
	query := hypothesisSelect + where + ` ORDER BY h.created_at, h.id LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list hypotheses: %w", err)
	}
	defer rows.Close()
	var out []Hypothesis
	for rows.Next() {
		record, err := scanHypothesis(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, record)
	}
	return out, total, rows.Err()
}

// Findings enumerates consolidations, newest last, with the same bounding rules
// as Hypotheses. Statuses does not apply: a finding's lifecycle lives in its
// review dispositions rather than in status events.
func (s *Store) Findings(ctx context.Context, filter ListFilter) ([]Finding, int, error) {
	where := ""
	if filter.LeavesOnly {
		where = ` WHERE NOT EXISTS(SELECT 1 FROM frontier_finding d WHERE d.ancestor_id = frontier_finding.id)`
	}
	total, err := s.count(ctx, `SELECT count(*) FROM frontier_finding`+where)
	if err != nil {
		return nil, 0, err
	}
	limit, offset := filter.bounds()
	ids, err := s.pageIDs(ctx, `SELECT id FROM frontier_finding`+where+
		` ORDER BY created_at, id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	// Read each through Finding so a listed record carries the same derived
	// observation and hypothesis links a detail view shows. A leaner query
	// would return a differently shaped record under the same type, which is
	// how a list and a detail view start disagreeing.
	out := make([]Finding, 0, len(ids))
	for _, id := range ids {
		record, err := s.Finding(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, record)
	}
	return out, total, nil
}

// Proposals enumerates review artifacts under the same rules as Findings.
func (s *Store) Proposals(ctx context.Context, filter ListFilter) ([]Proposal, int, error) {
	where := ""
	if filter.LeavesOnly {
		where = ` WHERE NOT EXISTS(SELECT 1 FROM frontier_proposal d WHERE d.ancestor_id = frontier_proposal.id)`
	}
	total, err := s.count(ctx, `SELECT count(*) FROM frontier_proposal`+where)
	if err != nil {
		return nil, 0, err
	}
	limit, offset := filter.bounds()
	ids, err := s.pageIDs(ctx, `SELECT id FROM frontier_proposal`+where+
		` ORDER BY created_at, id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Proposal, 0, len(ids))
	for _, id := range ids {
		record, err := s.Proposal(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, record)
	}
	return out, total, nil
}

// count runs a scalar count query, which every enumeration needs so a caller
// can page without discovering the end by hitting it.
func (s *Store) count(ctx context.Context, query string, args ...any) (int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count records: %w", err)
	}
	return total, nil
}

// pageIDs reads one page of identifiers. It is separate from queryIDs because
// that helper takes exactly one argument, and widening it to a variadic would
// cost every existing call site its readability for one new caller.
func (s *Store) pageIDs(ctx context.Context, query string, limit, offset int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list ids: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
