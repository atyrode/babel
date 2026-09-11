package evaluation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/durable"
	"github.com/atyrode/babel/internal/frontier"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// component names this package's rows in the durable file's shared migration
// table. The durable database already carries internal/frontier's records,
// internal/reality's ledger and the run journal, and §9 versions migrations per
// component so two packages advancing independently never have to agree on one
// global number.
const component = "evaluation"

// databaseFile is the same durable, pending-sync database every other Phase B
// writer uses. Evaluation records are not rebuildable - a vote nobody
// published is lost with the file - so they belong beside the analysis they
// are about rather than in the retrieval cache, and they stage into the same
// journal in the same transaction that writes them.
const databaseFile = "durable.db"

// migrations are applied in order and never edited once released.
//
// The shape mirrors §9's split exactly: one payload_json column holding the
// canonical publication - the votes, arguments, reasons, evidence notes and
// criteria that §9 requires sealed before they leave the machine - and
// otherwise only allowlisted identifiers, kinds, counts, lifecycle state and
// timestamps. No tally, rank, score or vote value is ever a column here, which
// is what keeps the plaintext catalog projection free of content-derived
// judgements when a record publishes.
var migrations = []string{`
CREATE TABLE evaluation_record(
	id              TEXT PRIMARY KEY,
	kind            TEXT NOT NULL,
	subject_kind    TEXT NOT NULL,
	subject_id      TEXT NOT NULL,
	assignment_id   TEXT NOT NULL,
	fence           INTEGER NOT NULL,
	attempt_state   TEXT NOT NULL,
	supersedes_id   TEXT NOT NULL,
	related_id      TEXT NOT NULL,
	actor_kind      TEXT NOT NULL,
	actor_id        TEXT NOT NULL,
	run_id          TEXT NOT NULL,
	role            TEXT NOT NULL,
	context_version TEXT NOT NULL,
	read_head_id    TEXT NOT NULL,
	digest          TEXT NOT NULL,
	seq             INTEGER NOT NULL,
	schema_version  INTEGER NOT NULL,
	created_at      TEXT NOT NULL,
	payload_json    TEXT NOT NULL
);
CREATE UNIQUE INDEX evaluation_record_order ON evaluation_record(seq);
CREATE INDEX evaluation_record_subject ON evaluation_record(subject_kind, subject_id);
CREATE INDEX evaluation_record_assignment ON evaluation_record(assignment_id);
CREATE INDEX evaluation_record_kind ON evaluation_record(kind, created_at);

-- One logical assignment has at most one active assessment. A correction
-- names the statement it supersedes, so the chain is what grows; a second
-- original assessment for one grant is refused by the database rather than by
-- a convention a later write path could forget.
CREATE UNIQUE INDEX evaluation_record_active_assessment
	ON evaluation_record(assignment_id) WHERE kind = 'assessment' AND supersedes_id = '';
-- A statement is corrected at most once, so a correction chain is a line
-- rather than a tree: two live corrections of one vote would be two active
-- votes wearing one supersede link.
CREATE UNIQUE INDEX evaluation_record_correction
	ON evaluation_record(supersedes_id) WHERE supersedes_id <> '';
-- The judgement-free families are idempotent by their own identity, which is
-- what makes a redelivered exposure, a re-granted claim, a repeated sweep and
-- a re-detected material change record one fact rather than inflating counts.
CREATE UNIQUE INDEX evaluation_record_grant
	ON evaluation_record(assignment_id, fence) WHERE kind = 'assignment';
CREATE UNIQUE INDEX evaluation_record_attempt
	ON evaluation_record(assignment_id, fence, attempt_state) WHERE kind = 'attempt';
CREATE UNIQUE INDEX evaluation_record_checkpoint
	ON evaluation_record(created_at, digest) WHERE kind = 'checkpoint';
CREATE UNIQUE INDEX evaluation_record_reconsider
	ON evaluation_record(subject_kind, subject_id, related_id, digest) WHERE kind = 'reconsider';

CREATE TABLE evaluation_attempt(
	id            TEXT PRIMARY KEY,
	assignment_id TEXT NOT NULL,
	fence         INTEGER NOT NULL,
	run_id        TEXT NOT NULL,
	state         TEXT NOT NULL,
	reason        TEXT NOT NULL,
	cost          REAL NOT NULL,
	recorded_at   TEXT NOT NULL,
	UNIQUE(assignment_id, fence, state)
);
CREATE INDEX evaluation_attempt_assignment ON evaluation_attempt(assignment_id);

-- The claim table. Unlike the record tables this one is updated: a fence
-- advances on takeover and a finish stamps its receipt. The append-only
-- history of what happened lives in evaluation_attempt and evaluation_record,
-- so mutating the current claim state loses nothing.
CREATE TABLE evaluation_claim(
	id              TEXT PRIMARY KEY,
	subject_kind    TEXT NOT NULL,
	subject_id      TEXT NOT NULL,
	run_id          TEXT NOT NULL,
	role            TEXT NOT NULL,
	policy_version  TEXT NOT NULL,
	context_version TEXT NOT NULL,
	seed            TEXT NOT NULL,
	input_digest    TEXT NOT NULL,
	-- corrects_id names the statement a paid follow-up grant exists to
	-- revise, and is empty for an ordinary review. It is part of the grant
	-- because it decides what the worker is shown before it says anything.
	corrects_id     TEXT NOT NULL,
	lane            TEXT NOT NULL,
	subjects_json   TEXT NOT NULL,
	fence           INTEGER NOT NULL,
	reserved_cost   REAL NOT NULL,
	day             TEXT NOT NULL,
	created_at      TEXT NOT NULL,
	expires_at      TEXT NOT NULL,
	finished_at     TEXT NOT NULL,
	finished_run    TEXT NOT NULL,
	finished_fence  INTEGER NOT NULL,
	finished_cost   REAL NOT NULL
);
CREATE INDEX evaluation_claim_day ON evaluation_claim(day);

-- The spend ledger is per (claim, fence) rather than per claim, because a
-- takeover must not release the superseded attempt's reservation: an expired
-- lease whose spend was never reported is charged at what it reserved, not at
-- zero, and the new attempt reserves again on top. Guessing that an
-- unobserved attempt cost nothing is how a fleet-wide allowance gets spent
-- twice.
CREATE TABLE evaluation_spend(
	id            TEXT PRIMARY KEY,
	day           TEXT NOT NULL,
	assignment_id TEXT NOT NULL,
	run_id        TEXT NOT NULL,
	fence         INTEGER NOT NULL,
	lane          TEXT NOT NULL,
	reserved      REAL NOT NULL,
	actual        REAL NOT NULL,
	settled       INTEGER NOT NULL,
	recorded_at   TEXT NOT NULL,
	UNIQUE(assignment_id, fence)
);
CREATE INDEX evaluation_spend_day ON evaluation_spend(day, run_id);

CREATE TABLE evaluation_budget_day(
	day            TEXT PRIMARY KEY,
	daily_cost     REAL NOT NULL CHECK(daily_cost >= 0),
	per_cycle_cost REAL NOT NULL CHECK(per_cycle_cost >= 0)
);
CREATE TRIGGER evaluation_budget_day_tighten BEFORE UPDATE ON evaluation_budget_day
WHEN NEW.day <> OLD.day OR NEW.daily_cost > OLD.daily_cost OR NEW.per_cycle_cost > OLD.per_cycle_cost
BEGIN SELECT RAISE(ABORT, 'evaluation allowance may only tighten within its UTC day'); END;

-- The settlement ledger is what makes a result land exactly once across the
-- three steps a completion has to survive: the claim was still valid, the
-- spend was reported to whoever owns the allowance, and the record became
-- durable here. It is written before the coordinator is told anything, so a
-- crash in the window leaves the exact receipt that was owed rather than a
-- decision to re-make from memory. See settle.
CREATE TABLE evaluation_settlement(
	id                TEXT PRIMARY KEY,
	assignment_id     TEXT NOT NULL,
	run_id            TEXT NOT NULL,
	fence             INTEGER NOT NULL,
	state             TEXT NOT NULL,
	attempt_state     TEXT NOT NULL,
	digest            TEXT NOT NULL,
	cost              REAL NOT NULL,
	record_id         TEXT NOT NULL,
	attempt_record_id TEXT NOT NULL,
	read_head_id      TEXT NOT NULL,
	reason            TEXT NOT NULL,
	created_at        TEXT NOT NULL,
	settled_at        TEXT NOT NULL,
	submission_json   TEXT NOT NULL,
	UNIQUE(assignment_id, fence)
);
CREATE INDEX evaluation_settlement_state ON evaluation_settlement(state);
`,
	// Migration 2 moves append-only from this package's discipline into the
	// engine. It is a separate migration rather than an edit to the first
	// because SQLite attaches triggers at CREATE time: editing migration 1
	// would protect only files created by this build and leave an existing
	// durable database accepting updates while every test passed.
	//
	// The claim, spend and settlement tables are deliberately not frozen
	// against UPDATE - a fence advances, a reservation settles, a settlement
	// state moves from pending to settled or fenced - but none of them may be
	// deleted, because a deleted reservation is an allowance silently handed
	// back and a deleted settlement is a receipt nobody can reconcile.
	`
CREATE TRIGGER evaluation_record_immutable BEFORE UPDATE ON evaluation_record
BEGIN SELECT RAISE(ABORT, 'an evaluation record is immutable; append a correction that supersedes it'); END;
CREATE TRIGGER evaluation_record_kept BEFORE DELETE ON evaluation_record
BEGIN SELECT RAISE(ABORT, 'evaluation records are never deleted; a correction preserves the earlier statement'); END;

CREATE TRIGGER evaluation_attempt_immutable BEFORE UPDATE ON evaluation_attempt
BEGIN SELECT RAISE(ABORT, 'attempt history is append-only; append the next state instead'); END;
CREATE TRIGGER evaluation_attempt_kept BEFORE DELETE ON evaluation_attempt
BEGIN SELECT RAISE(ABORT, 'attempt history is append-only; an exposure or a skip stays visible as a gap'); END;

CREATE TRIGGER evaluation_claim_kept BEFORE DELETE ON evaluation_claim
BEGIN SELECT RAISE(ABORT, 'a claim is never deleted; expiry and takeover are recorded, not erased'); END;
CREATE TRIGGER evaluation_spend_kept BEFORE DELETE ON evaluation_spend
BEGIN SELECT RAISE(ABORT, 'reserved attention is never deleted; unobserved spend is charged, not released'); END;
CREATE TRIGGER evaluation_settlement_kept BEFORE DELETE ON evaluation_settlement
BEGIN SELECT RAISE(ABORT, 'a settlement receipt is never deleted; it is what makes a completion land once'); END;
`}

// Store is the durable, append-only evaluation store.
//
// It exposes no operation that removes or rewrites a record. A correction is a
// descendant that names what it supersedes, a takeover is a new fence, and an
// abandoned reservation is charged rather than released - so the only
// direction this store moves in is forward.
type Store struct {
	db   *sql.DB
	path string

	// resolver reads the artifact a subject names, local or remote. It is
	// required: a store that cannot resolve a subject cannot tell an
	// assessment of a real revision from one of an imagined revision, and
	// #219's whole point is that a second instance assesses the first
	// instance's output through the same call.
	resolver Resolver

	// records resolves evaluation records this instance did not write, for
	// the authority checks that cannot be answered by shape alone: the
	// criteria an outcome claims to be measured against, and the reconsider
	// item an operator's decision answers. Nil is lawful and means local
	// mode; an authority that is neither local nor resolvable is then an
	// explicit ErrUnavailable rather than an assumed yes.
	records RecordResolver

	// dispositions is the frontier writer an explicit reopen moves, in the
	// same transaction that records the operator's decision.
	//
	// Nil is lawful and means this deployment records evaluation decisions
	// without a review surface to move, so an explicit reopen is
	// ErrUnavailable rather than a decision that says "reopened" beside a
	// record still reading rejected. A retain never needs it: retaining is
	// the decision to move nothing.
	dispositions *frontier.Store

	// coord is the authority over claims, fences and the allowance. It is
	// never nil after Open: without a shared coordinator this store installs
	// the local one, which enforces the same rules against this file. A
	// deployment that wants fleet-wide accounting passes WithCoordinator, and
	// a shared deployment that failed to reach coordination must not
	// construct a Store at all rather than silently accept local budgets.
	coord Coordinator
	// shared reports whether coord is a fleet coordinator rather than the
	// local one. It decides the ordering in settle and it decides whether
	// post-finish spend may be recorded at all.
	shared bool

	// sync stages every record this store writes into the shared catalog,
	// inside the transaction that writes it. Nil is local-only mode, which is
	// a supported deployment; see publish.go.
	sync babelsync.Hook

	// now supplies timestamps. It is a field so a test can make ordering
	// deterministic without sleeping; production never replaces it.
	now func() time.Time
}

// Open opens the durable database in dir and applies any pending migrations.
//
// A schema or integrity surprise is an error and is never resolved by
// discarding the file: these rows are the only copy of evaluation work that
// has not yet published. A database migrated by a newer build is refused
// explicitly for the same reason - an older reader that silently ignored rows
// it did not understand would drop published records on the floor.
func Open(dir string, resolver Resolver, opts ...Option) (*Store, error) {
	if resolver == nil {
		return nil, fmt.Errorf("%w: an evaluation store needs a resolver: a subject it cannot read "+
			"is a subject it cannot validate", ErrInvalid)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := durable.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open durable database %s: %w", databaseFile, err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{
		db:       db,
		path:     path,
		resolver: resolver,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(store)
	}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	if store.coord == nil {
		store.coord = &localCoordinator{db: db, now: store.now}
	}
	if store.sync != nil {
		// This store stages on its own connection, so the journal's tables
		// have to exist here before any write path opens a transaction. It is
		// cheap and idempotent.
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
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enable durable database WAL: %w", err)
	}
	if _, err := s.db.Exec(durable.BusyPragma); err != nil {
		return fmt.Errorf("set durable database busy timeout: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable durable database foreign keys: %w", err)
	}
	if _, err := s.db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migration(component TEXT PRIMARY KEY, version INTEGER)`); err != nil {
		return fmt.Errorf("create durable migration table: %w", err)
	}
	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read evaluation schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("evaluation schema version %d is newer than this build supports (%d); "+
			"the records it wrote are retained and this binary refuses to read them rather than discard them",
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
		return fmt.Errorf("begin evaluation migration %d: %w", index+1, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("apply evaluation migration %d: %w", index+1, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version`, component, index+1); err != nil {
		return fmt.Errorf("record evaluation migration %d: %w", index+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit evaluation migration %d: %w", index+1, err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database path, which an operator needs when the
// pending-sync journal has to be backed up before a machine is rebuilt.
func (s *Store) Path() string { return s.path }

// transact runs fn in one transaction, rolling back on any error.
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

// Events reports every canonical local record in order.
//
// It is a refresh of what this instance holds, not a page request: the
// projection that pages and ranks is built from this and rebuilt without it,
// and a caller that needs one record by id uses Record. Order is commit order
// - timestamp then local sequence - which makes it stable across reads even
// when two records share an instant.
func (s *Store) Events(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload_json FROM evaluation_record ORDER BY created_at, seq`)
	if err != nil {
		return nil, fmt.Errorf("read evaluation records: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan evaluation record: %w", err)
		}
		record, err := Decode(payload)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation records: %w", err)
	}
	return records, nil
}

// Record reads one canonical record by id.
func (s *Store) Record(ctx context.Context, id string) (Record, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT payload_json FROM evaluation_record WHERE id = ?`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: evaluation record %q", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, fmt.Errorf("read evaluation record %s: %w", id, err)
	}
	return Decode(payload)
}

// Attempts reports every attempt, in order.
//
// Exposure, completion, skip and failure are separate states and stay
// separate: a caller counting reviews must not count an assignment nobody
// opened, and a caller looking for gaps must be able to see repeated skips.
// Retries do not inflate the history - the rows are keyed by the fence that
// produced them, so a redelivered exposure records the same fact once while a
// takeover records a genuinely new attempt.
func (s *Store) Attempts(ctx context.Context) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT assignment_id, state, reason, cost, recorded_at
		 FROM evaluation_attempt ORDER BY recorded_at, id`)
	if err != nil {
		return nil, fmt.Errorf("read evaluation attempts: %w", err)
	}
	defer rows.Close()
	var attempts []Attempt
	for rows.Next() {
		var (
			attempt  Attempt
			recorded string
		)
		if err := rows.Scan(&attempt.AssignmentID, &attempt.State, &attempt.Reason,
			&attempt.Cost, &recorded); err != nil {
			return nil, fmt.Errorf("scan evaluation attempt: %w", err)
		}
		at, err := parseTime(recorded)
		if err != nil {
			return nil, err
		}
		attempt.RecordedAt = at
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation attempts: %w", err)
	}
	return attempts, nil
}

// Policy reports the most recent locally recorded operator configuration.
//
// A deployment that never configured one gets DefaultPolicy, which is a
// conservative disabled policy rather than an implied authorization: §5.8 does
// not start compute because a backlog exists. The effective fleet-wide
// selection policy is the Service's business; this is the local record.
func (s *Store) Policy(ctx context.Context) (Policy, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM evaluation_record
		WHERE kind = ? ORDER BY created_at DESC, seq DESC LIMIT 1`, KindPolicy).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("read evaluation policy: %w", err)
	}
	record, err := Decode(payload)
	if err != nil {
		return Policy{}, err
	}
	if record.Policy == nil {
		return Policy{}, fmt.Errorf("%w: policy record %s carries no policy", ErrInvalid, record.ID)
	}
	return *record.Policy, nil
}

// Operator records one operator-authored act: criteria, scoped feedback, a
// reconsideration decision, or a policy configuration.
//
// The authority checks are the point of this method, and two of them cannot be
// answered by shape. A reconsideration decision must name a reconsider item
// that actually exists and is about the same subject, because a decision that
// reopens nothing identifiable is not auditable. Feedback's reference is the
// opposite case on purpose: it is a citation, it authorizes nothing, and it
// may stay unresolved - a reason scoped to a decision this instance cannot see
// is still the operator's actual reason.
//
// One act moves a §4.7 disposition and only one: an explicit reopen. It is
// staged in the same transaction that records the decision, so the two cannot
// disagree - a decision reading "reopened" beside a record still reading
// rejected is the failure docs/evaluation-lifecycle.md's "reopening is
// explicitly operator-driven" exists to prevent, and so is the reverse. Every
// other act here sets no disposition whatever: collecting criteria, a reason
// or a retain decision must not silently create one, and accept, reject,
// defer and shelve stay internal/review's.
func (s *Store) Operator(ctx context.Context, in OperatorInput) (Record, error) {
	if err := in.validate(); err != nil {
		return Record{}, err
	}
	record := Record{
		Kind:      in.Kind,
		Subject:   in.Subject,
		ActorKind: ActorOperator,
		ActorID:   strings.TrimSpace(in.Operator),
		CreatedAt: s.now(),
		Criteria:  in.Criteria,
		Reason:    in.Reason,
		Context:   in.Context,
		Policy:    in.Policy,
		RelatedID: in.RelatedID,
		Decision:  in.Decision,
	}
	reopen := frontier.DispositionInput{}
	switch in.Kind {
	case KindPolicy:
		if in.Policy == nil {
			return Record{}, fmt.Errorf("%w: a policy act carries a policy", ErrInvalid)
		}
		if err := ValidatePolicy(*in.Policy); err != nil {
			return Record{}, err
		}
	case KindCriteria:
		if err := s.requireArtifact(ctx, in.Subject); err != nil {
			return Record{}, err
		}
	case KindFeedback:
		if err := s.requireArtifact(ctx, in.Subject); err != nil {
			return Record{}, err
		}
	case KindReconsiderDecision:
		artifact, err := s.artifact(ctx, in.Subject)
		if err != nil {
			return Record{}, err
		}
		// The item being answered is a run's observation, so the author is
		// checked as well as the kind: a decision that answered an operator's
		// own record would be answering itself.
		if _, err := s.requireAuthority(ctx, in.RelatedID, KindReconsider, ActorRun, in.Subject); err != nil {
			return Record{}, err
		}
		if in.Decision == ReconsiderReopen {
			reopen, err = s.reopenDisposition(artifact, record.ActorID, in.Reason)
			if err != nil {
				return Record{}, err
			}
		}
	}
	id, err := newID("evr")
	if err != nil {
		return Record{}, err
	}
	record.ID = id
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	var (
		pub      publication
		reopened func(context.Context) error
	)
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var err error
		if pub, err = s.writeRecord(ctx, tx, recordWrite{record: record}); err != nil {
			return err
		}
		if reopen.Disposition == "" {
			return nil
		}
		// The frontier writer stages into the same durable file this
		// transaction is already in, so the reopened disposition and the
		// decision that ordered it commit together or not at all. Its
		// publication is deferred to the callback for the same reason this
		// package defers its own: a publication attempt inside the
		// transaction would hold the local write open on a network.
		_, publish, err := s.dispositions.StageResolvedDisposition(ctx, tx, reopen)
		if err != nil {
			return fmt.Errorf("reopen %s: %w", in.Subject, err)
		}
		reopened = publish
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Record{}, err
	}
	if reopened != nil {
		if err := reopened(ctx); err != nil {
			return Record{}, fmt.Errorf("publish the reopened disposition of %s: %w", in.Subject, err)
		}
	}
	return record, nil
}

// reopenDisposition builds the §4.7 reopen this decision orders, from the
// artifact the caller has already resolved.
//
// The review state travels on the input rather than being derived by the
// frontier writer, because the subject may be a record another instance
// published: its disposition history is not in this machine's durable tables,
// so a writer deriving the state here would read every remote reopen as new.
// What this store does have is the state the resolver reported, so it states
// it and the writer enforces the transition against it.
//
// The states that may be reopened are the decided ones. A record that is
// already new has nothing to reopen, and a duplicate or a refine-requested
// record is closed through its own surface rather than through this one - a
// reopen there would leave two answers to one question. A state this store
// could not read at all is unavailable and never an assumed yes.
func (s *Store) reopenDisposition(artifact Artifact, operator, reason string) (
	frontier.DispositionInput, error) {
	if strings.TrimSpace(reason) == "" {
		return frontier.DispositionInput{}, fmt.Errorf("%w: a reopen carries the operator's reason",
			ErrInvalid)
	}
	// What the request asks for is checked before what this deployment can
	// do about it: a reopen of a record that is already open is a refusal
	// wherever it is asked, and saying "no review writer" to it would be
	// answering a question nobody asked.
	status := frontier.ReviewStatus(artifact.ReviewStatus)
	switch status {
	case frontier.ReviewAccepted, frontier.ReviewRejected, frontier.ReviewDeferred:
	case frontier.ReviewNew:
		return frontier.DispositionInput{}, fmt.Errorf("%w: %s is already open for review, so there is "+
			"nothing to reopen", ErrInvalid, artifact.Subject)
	case frontier.ReviewDuplicate, frontier.ReviewRefineRequested:
		return frontier.DispositionInput{}, fmt.Errorf("%w: %s reads %s, which is answered through the "+
			"surface that closed it rather than by reopening", ErrInvalid, artifact.Subject, status)
	default:
		return frontier.DispositionInput{}, fmt.Errorf("%w: %s reports review state %q, which this build "+
			"cannot read, so whether it is decided work that can be reopened is unknown",
			ErrUnavailable, artifact.Subject, artifact.ReviewStatus)
	}
	if s.dispositions == nil {
		return frontier.DispositionInput{}, fmt.Errorf("%w: no review writer is wired, so %s cannot be "+
			"reopened; recording a decision that says it was would be a reopening that never happened",
			ErrUnavailable, artifact.Subject)
	}
	return frontier.DispositionInput{
		Subject:        frontier.Ref{Type: frontier.EntityType(artifact.Subject.Kind), ID: artifact.Subject.ID},
		Disposition:    frontier.DispositionReopen,
		ReviewerID:     operator,
		ResolvedStatus: status,
		Note:           reason,
	}, nil
}

// Reconsider raises one run-authored reconsider item: what materially changed
// about work that was already decided.
//
// It exists as its own entry point because a reconsider item is an
// observation, not a decision. Only an operator's explicit
// reconsider_decision reopens anything, and this record preserves the prior
// decision untouched - it is about the change, not about the ruling.
//
// It is idempotent per material change. The change identity is the recorded
// context version the detection ran under together with the decided work it
// revisits, so one material change raises exactly one item however many times
// the sweep that found it runs. A second, genuinely different change carries a
// different context version and raises its own item.
func (s *Store) Reconsider(ctx context.Context, in ReconsiderInput) (Record, error) {
	if err := in.validate(); err != nil {
		return Record{}, err
	}
	if err := s.requireArtifact(ctx, in.Subject); err != nil {
		return Record{}, err
	}
	provenance := in.Provenance
	if provenance.RunID == "" {
		provenance.RunID = in.RunID
	}
	if baseRun(provenance.RunID) != baseRun(in.RunID) {
		return Record{}, fmt.Errorf("%w: reconsider provenance names run %q but the item is authored by %q",
			ErrInvalid, provenance.RunID, in.RunID)
	}
	change := provenance.ContextVersion
	if change == "" && in.Context != nil {
		change = in.Context.Version
		provenance.ContextVersion = change
	}
	if strings.TrimSpace(change) == "" {
		return Record{}, fmt.Errorf("%w: a reconsider item must carry the context version its detection ran "+
			"under, or one material change would raise an item on every sweep", ErrInvalid)
	}
	id, err := newID("evr")
	if err != nil {
		return Record{}, err
	}
	record := Record{
		ID:         id,
		Kind:       KindReconsider,
		Subject:    in.Subject,
		ActorKind:  ActorRun,
		ActorID:    in.RunID,
		CreatedAt:  s.now(),
		Provenance: provenance,
		Reason:     in.Reason,
		Context:    in.Context,
		RelatedID:  in.RelatedID,
	}
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	var (
		pub      publication
		existing Record
		found    bool
	)
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var payload []byte
		err := tx.QueryRowContext(ctx, `SELECT payload_json FROM evaluation_record
			WHERE kind = ? AND subject_kind = ? AND subject_id = ? AND related_id = ? AND digest = ?`,
			KindReconsider, in.Subject.Kind, in.Subject.ID, in.RelatedID, change).Scan(&payload)
		switch {
		case err == nil:
			existing, err = Decode(payload)
			found = true
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read reconsider item: %w", err)
		}
		pub, err = s.writeRecord(ctx, tx, recordWrite{
			record:     record,
			digest:     change,
			producedBy: in.RunID,
		})
		return err
	})
	if err != nil {
		return Record{}, err
	}
	if found {
		return existing, nil
	}
	if err := s.commit(ctx, pub); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Checkpoint records that one coverage sweep finished.
//
// The record it writes carries no opinion and no author: when the sweep ran,
// the digest of the inventory it examined, and how many subjects that
// inventory held. That is deliberate on both counts. §E4 makes "coverage
// inspection completed" and "all eligible output reviewed" separate facts, so
// this says only the first; and a sweep is not a judgement, so nothing in it
// could leak a tally, a rank or a vote into the catalog.
//
// It is idempotent for one (instant, inventory) pair, so a retried sweep does
// not invent a second completion.
func (s *Store) Checkpoint(ctx context.Context, at time.Time, inputDigest string, covered int) error {
	checkpoint := CoverageCheckpoint{At: at.UTC(), InputDigest: inputDigest, Covered: covered}
	if err := checkpoint.validate(); err != nil {
		return err
	}
	if !validID.MatchString(inputDigest) {
		return fmt.Errorf("%w: coverage inventory digest %q is not a well-formed identifier",
			ErrInvalid, inputDigest)
	}
	id, err := newID("evr")
	if err != nil {
		return err
	}
	record := Record{
		ID:         id,
		Kind:       KindCheckpoint,
		CreatedAt:  checkpoint.At,
		Checkpoint: &checkpoint,
	}
	if err := record.validate(); err != nil {
		return err
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM evaluation_record
			WHERE kind = ? AND created_at = ? AND digest = ?`,
			KindCheckpoint, formatTime(checkpoint.At), inputDigest).Scan(&found); err != nil {
			return fmt.Errorf("read coverage checkpoint: %w", err)
		}
		if found > 0 {
			return nil
		}
		var err error
		pub, err = s.writeRecord(ctx, tx, recordWrite{record: record, digest: inputDigest})
		return err
	})
	if err != nil {
		return err
	}
	return s.commit(ctx, pub)
}

// Expose records that a worker was actually shown the content of its
// assignment.
//
// It is a separate fact from the assignment and from the assessment because
// §4.12 requires it to be: an assignment nobody opened, an exposure that never
// produced a result and a completed review are three different states of
// coverage, and a store that could not tell them apart would report the first
// two as the third or as nothing at all.
//
// The claim is validated first - locally and, in shared mode, against the
// coordinator - so a stale worker cannot record that it read something it was
// no longer entitled to read. A redelivered exposure under the same fence is a
// no-op rather than a second exposure.
func (s *Store) Expose(ctx context.Context, id, runID string, fence int64) error {
	if err := s.ValidateClaim(ctx, id, runID, fence); err != nil {
		return err
	}
	claim, err := s.Assignment(ctx, id)
	if err != nil {
		return err
	}
	attempt := Attempt{AssignmentID: id, State: AttemptExposed, RecordedAt: s.now()}
	recordID, err := newID("evr")
	if err != nil {
		return err
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		inserted, err := s.appendAttempt(ctx, tx, claim, attempt, fence, runID)
		if err != nil || !inserted {
			return err
		}
		pub, err = s.attemptRecord(ctx, tx, recordID, claim, attempt, fence, runID)
		return err
	})
	if err != nil {
		return err
	}
	return s.commit(ctx, pub)
}

// Submit records one worker's result for one assignment.
//
// An assessment, a skip and a failure are all completions of the claim: each
// reconciles the reservation with what was actually spent, and only the first
// produces a record. A skip and a failure return the zero Record - there is no
// statement to return - and they are never a vote, because §4.12 is explicit
// that an unsupported source or a repeated skip is a visible gap rather than
// negative reception.
//
// A retry of the identical submission returns the identical record. A
// different result under the same fence is refused: one grant has at most one
// active statement, and changing it is what Correct is for.
func (s *Store) Submit(ctx context.Context, in Submission) (Record, error) {
	if err := in.validate(); err != nil {
		return Record{}, err
	}
	state, err := in.state()
	if err != nil {
		return Record{}, err
	}
	claim, err := s.Assignment(ctx, in.AssignmentID)
	if err != nil {
		return Record{}, err
	}
	if claim.RunID != in.RunID || claim.Fence != in.Fence {
		return Record{}, fmt.Errorf("%w: assignment %s is held by run %q at fence %d, not by %q at fence %d",
			ErrConflict, claim.ID, claim.RunID, claim.Fence, in.RunID, in.Fence)
	}
	readHead := ""
	if in.Assessment != nil {
		head, err := s.validateAssessment(ctx, claim, in)
		if err != nil {
			return Record{}, err
		}
		readHead = head
	}
	// Drain any settlement this instance owes from an earlier interrupted
	// completion before starting a new one, so a crashed run's result lands
	// rather than waiting for a command somebody has to remember to type.
	if _, err := s.Recover(ctx); err != nil && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrConflict) {
		return Record{}, err
	}
	return s.settle(ctx, claim, in, state, readHead)
}

// Correct appends a linked correction of an earlier statement.
//
// The earlier statement is not rewritten and not hidden: both records remain
// readable and the correction names what it supersedes. Exactly one statement
// per grant is active at a time - the one nothing supersedes - so a tally reads
// one vote per review while the history keeps every wording.
//
// There are two ways in and the difference is who paid for the second look.
// A submission naming the original grant restates what that grant already
// bought, so it reports no new spend. A submission naming a follow-up claim
// whose Corrects names the target is paid work: it was reserved before the
// model ran and it settles through the same receipt every completion does, so
// the allowance sees it once rather than after the fact. There is no third
// way, because a cost charged after a claim is finished has no receipt in the
// shared coordination ABI and charging it locally would be the silent local
// fallback shared mode forbids.
func (s *Store) Correct(ctx context.Context, id string, in Submission) (Record, error) {
	if in.Assessment == nil {
		return Record{}, fmt.Errorf("%w: a correction restates the assessment; a skip or failure corrects nothing",
			ErrInvalid)
	}
	if err := in.validate(); err != nil {
		return Record{}, err
	}
	target, err := s.Record(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if target.Kind != KindAssessment {
		return Record{}, fmt.Errorf("%w: record %s is a %s, and only an assessment is corrected",
			ErrInvalid, id, target.Kind)
	}
	claim, err := s.Assignment(ctx, in.AssignmentID)
	if err != nil {
		return Record{}, err
	}
	if claim.RunID != in.RunID || claim.Fence != in.Fence {
		return Record{}, fmt.Errorf("%w: assignment %s is held by run %q at fence %d, "+
			"so %q at fence %d cannot correct its statement",
			ErrConflict, claim.ID, claim.RunID, claim.Fence, in.RunID, in.Fence)
	}
	if claim.ID != target.AssignmentID {
		// A paid follow-up. The claim itself is the authority to supersede
		// this statement, which is what makes the reveal and the spend one
		// decision taken before the work rather than two taken after it.
		if claim.Corrects != id {
			return Record{}, fmt.Errorf(
				"%w: assignment %s was not claimed to correct record %s", ErrInvalid, claim.ID, id)
		}
		if claim.Subject != target.Subject {
			return Record{}, fmt.Errorf(
				"%w: assignment %s is about %s, not %s", ErrInvalid, claim.ID, claim.Subject, target.Subject)
		}
		state, err := in.state()
		if err != nil {
			return Record{}, err
		}
		readHead, err := s.validateAssessment(ctx, claim, in)
		if err != nil {
			return Record{}, err
		}
		return s.settle(ctx, claim, in, state, readHead)
	}
	if in.Cost > 0 {
		return Record{}, fmt.Errorf(
			"%w: assignment %s is already finished, so a correction under it reports no new spend; "+
				"claim a follow-up assignment for a paid second pass",
			ErrInvalid, claim.ID)
	}
	readHead, err := s.validateAssessment(ctx, claim, in)
	if err != nil {
		return Record{}, err
	}
	assessment := *in.Assessment
	provenance := s.provenance(claim, in)
	recordID, err := newID("evr")
	if err != nil {
		return Record{}, err
	}
	record := Record{
		ID:           recordID,
		Kind:         KindAssessment,
		Subject:      claim.Subject,
		AssignmentID: claim.ID,
		SupersedesID: id,
		ActorKind:    ActorRun,
		ActorID:      in.RunID,
		CreatedAt:    s.now(),
		Provenance:   provenance,
		Assessment:   &assessment,
	}
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var superseded int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM evaluation_record WHERE supersedes_id = ?`, id).Scan(&superseded); err != nil {
			return fmt.Errorf("read correction chain: %w", err)
		}
		if superseded > 0 {
			return fmt.Errorf("%w: record %s has already been corrected; correct the active statement",
				ErrConflict, id)
		}

		var err error
		pub, err = s.writeRecord(ctx, tx, recordWrite{
			record:     record,
			role:       claim.Role,
			fence:      claim.Fence,
			readHead:   readHead,
			producedBy: in.RunID,
		})
		return err
	})
	if err != nil {
		return Record{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Record{}, err
	}
	return record, nil
}

// validateAssessment applies every check an assessment must pass before it can
// become a durable statement, and reports the artifact head that was current
// when it was read.
//
// The head is returned rather than stored by the caller because it is the
// honest answer to a question the record itself cannot answer: the assessment
// binds to Subject, which is one exact revision, and the head says what the
// chain looked like at the moment of the write. A reader comparing the two
// sees "this vote is about revision n while the chain is at n+1" instead of
// silently reading an old endorsement as a current one.
func (s *Store) validateAssessment(ctx context.Context, claim Assignment, in Submission) (string, error) {
	assessment := in.Assessment
	if err := assessment.validate(claim.Role); err != nil {
		return "", err
	}
	if assessment.ContextVersion != "" && assessment.ContextVersion != claim.ContextVersion {
		return "", fmt.Errorf("%w: the assessment was made under context %q but assignment %s was granted "+
			"under %q; a result cannot claim a context it did not read",
			ErrInvalid, assessment.ContextVersion, claim.ID, claim.ContextVersion)
	}
	if in.Provenance.ContextVersion != "" && in.Provenance.ContextVersion != claim.ContextVersion {
		return "", fmt.Errorf("%w: provenance names context %q but assignment %s was granted under %q",
			ErrInvalid, in.Provenance.ContextVersion, claim.ID, claim.ContextVersion)
	}
	if in.Provenance.RunID != "" && baseRun(in.Provenance.RunID) != baseRun(in.RunID) {
		return "", fmt.Errorf("%w: provenance names run %q but the submission is from %q",
			ErrInvalid, in.Provenance.RunID, in.RunID)
	}
	artifact, err := s.artifact(ctx, claim.Subject)
	if err != nil {
		return "", err
	}
	alternatives, err := s.resolveAlternatives(ctx, *assessment)
	if err != nil {
		return "", err
	}
	if err := refuseSelfBoost(artifact, in, alternatives); err != nil {
		return "", err
	}
	if assessment.CriteriaID != "" {
		// The criteria version is what keeps Babel from rewriting its own
		// target and verifying itself against the replacement: it must be an
		// operator-authored criteria record about this exact subject, and an
		// authority this instance can neither find nor resolve is an explicit
		// failure rather than an assumed yes.
		//
		// Resolving it is half the check. The answers have to be answers to
		// the conditions that record actually states, which is what binds an
		// outcome to the operator's target rather than to a target shaped
		// like it.
		criteria, err := s.requireAuthority(ctx, assessment.CriteriaID, KindCriteria, ActorOperator,
			claim.Subject)
		if err != nil {
			return "", err
		}
		if err := bindResults(*assessment, criteria.Criteria); err != nil {
			return "", err
		}
	}
	return artifact.HeadID, nil
}

// bindResults binds one assessment's criterion answers to the operator's
// criteria version.
//
// Two holes close here, and neither is reachable from shape alone. A result
// naming a criterion the operator never wrote is a target the run chose for
// itself, which is the retrospective substitution §4.12 refuses - "an
// evaluator cannot replace the target retrospectively and declare victory". A
// verified outcome that answers three of five stated conditions is a
// verification of something narrower than what was promised, and §4.12 admits
// verification only "when the recorded success criteria have been satisfied":
// all of them, not the ones that were checked.
//
// The weaker outcomes are deliberately allowed to answer a subset. A partial
// outcome says exactly that some conditions were answered and some were not,
// and an unverifiable one says the checks could not be made at all.
func bindResults(a Assessment, criteria []Criterion) error {
	defined := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		defined[criterion.ID] = struct{}{}
	}
	answered := make(map[string]struct{}, len(a.Results))
	for _, result := range a.Results {
		if _, ok := defined[result.CriterionID]; !ok {
			return fmt.Errorf("%w: criterion %s is not one the operator's criteria version %s states",
				ErrInvalid, result.CriterionID, a.CriteriaID)
		}
		answered[result.CriterionID] = struct{}{}
	}
	if a.Outcome != OutcomeVerified {
		return nil
	}
	for _, criterion := range criteria {
		if _, ok := answered[criterion.ID]; !ok {
			return fmt.Errorf("%w: criteria version %s states criterion %s and the outcome does not answer "+
				"it, which is at most a partial outcome", ErrInvalid, a.CriteriaID, criterion.ID)
		}
	}
	return nil
}

// resolveAlternatives resolves every alternative a comparison names.
//
// It holds an alternative to the same rule as the subject: an assessment of an
// artifact whose existence was assumed is not an assessment, so a comparison
// that names a remedy nothing can resolve is a refused write rather than a
// comparison of one record with a rumour. The resolved artifacts are returned
// because the self-boost check needs the producing run of the one that was
// preferred, and resolving that twice would ask the same question over the
// wire again.
func (s *Store) resolveAlternatives(ctx context.Context, a Assessment) (map[Subject]Artifact, error) {
	var resolved map[Subject]Artifact
	for i, contribution := range a.Contributions {
		for j, alternative := range contribution.Alternatives {
			if _, seen := resolved[alternative]; seen {
				continue
			}
			artifact, err := s.artifact(ctx, alternative)
			if err != nil {
				return nil, fmt.Errorf("contribution %d alternative %d: %w", i, j, err)
			}
			if resolved == nil {
				resolved = make(map[Subject]Artifact, len(contribution.Alternatives))
			}
			resolved[alternative] = artifact
		}
	}
	return resolved, nil
}

// refuseSelfBoost refuses a run's endorsement of what that same run just
// wrote.
//
// Only endorsement is refused, and the list is exactly the positive claims: a
// support vote, an implemented, verified or partial outcome, and a preference
// for its own alternative. A bare opposition or an uncertainty vote from the
// producing run is legitimate self-criticism - the hazard §4.12 names is a run
// manufacturing support for its own freshly authored alternative, not a run
// doubting it - and so are its own comments and refinements, because an
// immutable descendant that improves an idea is how §4.12 says an idea is
// improved. What none of those may become is independent corroboration, and
// none of them can: the provenance travels on the record.
//
// An artifact whose producing run is unknown is unavailable for an
// endorsement rather than permitted. "I could not check" reading the same as
// "checked, fine" is the failure this package refuses everywhere else, and an
// endorsement is where it would matter most.
//
// The run identity is compared at its base, because internal/explore spells a
// challenger's or synthesizer's identity `<run>/<stage>` and a self-boost
// submitted from a second stage of the same run is the same run.
func refuseSelfBoost(artifact Artifact, in Submission, alternatives map[Subject]Artifact) error {
	assessment := in.Assessment
	endorses := assessment.Vote == VoteSupport || assessment.Outcome == OutcomeImplemented ||
		assessment.Outcome == OutcomeVerified || assessment.Outcome == OutcomePartial
	if endorses {
		if err := refuseSelfAuthored(artifact, in.RunID, "boost"); err != nil {
			return err
		}
	}
	for _, contribution := range assessment.Contributions {
		if contribution.Preferred == nil {
			continue
		}
		preferred, resolved := alternatives[*contribution.Preferred]
		if !resolved {
			return fmt.Errorf("%w: the preferred alternative %s was not resolved, so whether run %s "+
				"authored it cannot be checked", ErrUnavailable, contribution.Preferred, in.RunID)
		}
		if err := refuseSelfAuthored(preferred, in.RunID, "prefer"); err != nil {
			return err
		}
	}
	return nil
}

// refuseSelfAuthored is the comparison behind both refusals: the run that
// produced an artifact may not be the run endorsing it, and an artifact with
// no recorded producer cannot answer the question at all.
func refuseSelfAuthored(artifact Artifact, runID, act string) error {
	if artifact.RunID == "" {
		return fmt.Errorf("%w: %s records no producing run, so whether run %s would be endorsing its own "+
			"output cannot be checked", ErrUnavailable, artifact.Subject, runID)
	}
	if baseRun(artifact.RunID) == baseRun(runID) {
		return fmt.Errorf("%w: run %s authored %s and cannot %s it in the same run",
			ErrInvalid, runID, artifact.Subject, act)
	}
	return nil
}

// requireAuthority resolves the record a judgement rests on, checks that it is
// the authority it claims, and returns it.
//
// Shape is not enough and the gap is specific. An outcome that named any
// identifier at all as its criteria version would be verifying itself against
// a target it chose; a reconsideration decision that named anything would be
// reopening something nobody can identify. So the reference is resolved -
// locally first, then through the published-record resolver for a record
// another instance wrote - and checked for kind, author and exact subject.
//
// The record is returned rather than discarded because resolving it proves
// only that an authority exists: what the caller has to check next is inside
// it, and re-reading it there would be a second lookup that could answer
// differently.
//
// The two resolution paths are held to one standard. A local record came back
// through Decode and has therefore already passed every gate this build
// applies to its own writes; a remote one arrives as a struct another process
// filled in, so it passes the same gate here. Without that, a resolver
// returning an unsigned criteria record with no criteria in it - or the wrong
// record entirely - would grant an authority its own shape cannot support.
//
// An unresolvable authority is ErrUnavailable, never an accepted write. That
// distinction is the whole reason this function exists: "I cannot check"
// must not read the same as "checked, fine".
func (s *Store) requireAuthority(ctx context.Context, id, kind, actorKind string,
	subject Subject) (Record, error) {
	if !validID.MatchString(id) {
		return Record{}, fmt.Errorf("%w: %q is not a well-formed record identifier", ErrInvalid, id)
	}
	record, err := s.Record(ctx, id)
	if errors.Is(err, ErrNotFound) {
		if s.records == nil {
			return Record{}, fmt.Errorf("%w: %s record %s is not on this instance and no published-record "+
				"resolver is wired, so its authority cannot be verified", ErrUnavailable, kind, id)
		}
		record, err = s.records.EvaluationRecord(ctx, id)
		switch {
		case errors.Is(err, ErrNotFound):
			return Record{}, fmt.Errorf("%w: %s record %s does not exist on this instance or in the "+
				"shared catalog", ErrInvalid, kind, id)
		case err != nil:
			return Record{}, fmt.Errorf("%w: %s record %s could not be read, so its authority cannot be "+
				"verified: %w", ErrUnavailable, kind, id, err)
		}
		if record.ID != id {
			return Record{}, fmt.Errorf("%w: %s record %s was asked for and record %q came back, so the "+
				"authority that was checked is not the one being relied on", ErrInvalid, kind, id, record.ID)
		}
		if err := record.validate(); err != nil {
			return Record{}, fmt.Errorf("%w: %s record %s is not a record this build would accept, so it "+
				"cannot be the authority a judgement rests on: %w", ErrInvalid, kind, id, err)
		}
	} else if err != nil {
		return Record{}, err
	}
	if record.Kind != kind {
		return Record{}, fmt.Errorf("%w: record %s is a %s, not a %s", ErrInvalid, id, record.Kind, kind)
	}
	if actorKind != "" && record.ActorKind != actorKind {
		return Record{}, fmt.Errorf("%w: record %s was authored by a %s, and only a %s may author the %s "+
			"a judgement rests on", ErrInvalid, id, record.ActorKind, actorKind, kind)
	}
	if record.Subject != subject {
		return Record{}, fmt.Errorf("%w: record %s is about %s, not %s", ErrInvalid, id, record.Subject, subject)
	}
	return record, nil
}

// artifact resolves one subject, local or remote, through the resolver this
// store was opened with.
func (s *Store) artifact(ctx context.Context, subject Subject) (Artifact, error) {
	if err := subject.validate(); err != nil {
		return Artifact{}, err
	}
	artifact, err := s.resolver.Artifact(ctx, subject)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable) {
			return Artifact{}, err
		}
		return Artifact{}, fmt.Errorf("resolve %s: %w", subject, err)
	}
	if artifact.Subject != subject {
		return Artifact{}, fmt.Errorf("%w: resolving %s returned %s", ErrInvalid, subject, artifact.Subject)
	}
	return artifact, nil
}

// requireArtifact resolves a subject and discards it, for the write paths that
// need the subject to exist but nothing from inside it.
func (s *Store) requireArtifact(ctx context.Context, subject Subject) error {
	_, err := s.artifact(ctx, subject)
	return err
}

// provenance fills in what the submission left implicit: a result with no run
// or context of its own belongs to the run and context the grant named.
func (s *Store) provenance(claim Assignment, in Submission) Provenance {
	provenance := in.Provenance
	if provenance.RunID == "" {
		provenance.RunID = in.RunID
	}
	if provenance.ContextVersion == "" {
		provenance.ContextVersion = claim.ContextVersion
	}
	return provenance
}

// recordWrite is one record on its way into the durable store: the record
// itself, the plaintext-eligible columns a reader indexes by, and the run
// whose publication closure it joins.
type recordWrite struct {
	record Record
	role   string
	fence  int64
	// readHead is the chain head observed when the record was written, which
	// is how a reader tells a vote on the current wording from a vote on a
	// superseded one without re-resolving the artifact.
	readHead string
	// digest is the record family's own idempotency discriminator: a coverage
	// inventory's digest, or the context version that identifies one material
	// change.
	digest string
	// producedBy is the run whose closure this record joins, and empty for an
	// operator's act or a coverage sweep. internal/sync resolves the choice in
	// one place; see publish.go.
	producedBy string
}

// writeRecord inserts one record and stages it for publication in the same
// transaction.
//
// The two happen together because that is the property this package exists to
// keep: "durable on this machine" and "owed to the fleet" are one event, so a
// crash cannot leave a vote that nothing will ever publish. The canonical
// bytes are stored verbatim in payload_json and staged verbatim, so the local
// row and the published object cannot disagree about what was said.
func (s *Store) writeRecord(ctx context.Context, tx *sql.Tx, w recordWrite) (publication, error) {
	if err := w.record.validate(); err != nil {
		return publication{}, err
	}
	payload, err := encodeRecord(w.record)
	if err != nil {
		return publication{}, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM evaluation_record`).Scan(&seq); err != nil {
		return publication{}, fmt.Errorf("allocate evaluation record sequence: %w", err)
	}
	attemptState := ""
	if w.record.Attempt != nil {
		attemptState = w.record.Attempt.State
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_record(
		id, kind, subject_kind, subject_id, assignment_id, fence, attempt_state, supersedes_id, related_id,
		actor_kind, actor_id, run_id, role, context_version, read_head_id, digest, seq, schema_version,
		created_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.record.ID, w.record.Kind, w.record.Subject.Kind, w.record.Subject.ID, w.record.AssignmentID,
		w.fence, attemptState, w.record.SupersedesID, w.record.RelatedID, w.record.ActorKind,
		w.record.ActorID, baseRun(w.record.Provenance.RunID), w.role, w.record.Provenance.ContextVersion,
		w.readHead, w.digest, seq, RecordSchema, formatTime(w.record.CreatedAt), payload); err != nil {
		return publication{}, fmt.Errorf("insert evaluation record: %w", err)
	}
	return s.stage(ctx, tx, w.producedBy, staged(w.record.ID, payload))
}

// appendAttempt appends one attempt state, reporting whether it was new.
//
// A redelivered exposure or a retried completion under the same fence records
// the same fact once, which is what keeps a retry from inflating review
// counts. A takeover carries a new fence and therefore a genuinely new
// attempt, because "tried twice" and "delivered twice" are different facts.
func (s *Store) appendAttempt(ctx context.Context, tx *sql.Tx, claim Assignment, attempt Attempt,
	fence int64, runID string) (bool, error) {
	if err := attempt.validate(); err != nil {
		return false, err
	}
	id, err := newID("eva")
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_attempt(
		id, assignment_id, fence, run_id, state, reason, cost, recorded_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		id, claim.ID, fence, runID, attempt.State, attempt.Reason, attempt.Cost,
		formatTime(attempt.RecordedAt))
	if err != nil {
		return false, fmt.Errorf("append evaluation attempt: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("append evaluation attempt: %w", err)
	}
	return affected > 0, nil
}

// attemptRecord publishes one attempt.
//
// Attempts publish because coverage on a second instance would otherwise be
// derived from completed assessments alone: an assignment nobody opened and an
// exposure that produced nothing would be indistinguishable from a record
// nobody was ever assigned, and "reviewed" would quietly come to mean
// "someone published a vote".
func (s *Store) attemptRecord(ctx context.Context, tx *sql.Tx, id string, claim Assignment,
	attempt Attempt, fence int64, runID string) (publication, error) {
	record := Record{
		ID:           id,
		Kind:         KindAttempt,
		AssignmentID: claim.ID,
		ActorKind:    ActorRun,
		ActorID:      runID,
		CreatedAt:    attempt.RecordedAt,
		Provenance:   Provenance{RunID: runID, ContextVersion: claim.ContextVersion},
		Attempt:      &attempt,
	}
	return s.writeRecord(ctx, tx, recordWrite{
		record:     record,
		role:       claim.Role,
		fence:      fence,
		producedBy: runID,
	})
}

// digestSubmission is the idempotency key of one result: the same result
// resubmitted digests identically, and a different result under the same fence
// does not.
//
// It hashes the canonical JSON of the submission rather than a field subset,
// because a retry that differed in its cost, its skip reason or one evidence
// note is a different statement and must not be silently accepted as a
// redelivery of the first.
func digestSubmission(in Submission) (string, error) {
	encoded, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("encode submission: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Restage recovers canonical local publications missing from the sync journal.
//
// It exists because local-only mode is a supported deployment: a machine that
// recorded evaluations before shared publication was configured owes every one
// of them to the fleet the moment it is. Nothing is published or changed here
// - each record is staged from its own stored canonical bytes, in its own
// transaction, so an interrupted pass is resumable - and every record is
// re-validated through Decode on the way out, so a row this build cannot
// serialize faithfully is refused rather than staged as something else.
func (s *Store) Restage(ctx context.Context) (int, error) {
	if s.sync == nil {
		return 0, fmt.Errorf("restage evaluation: sync hook is required")
	}
	ids, err := babelsync.Missing(ctx, s.db, "evaluation_record", "id")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		added := 0
		err := s.transact(ctx, func(tx *sql.Tx) error {
			var missing bool
			if err := tx.QueryRowContext(ctx,
				`SELECT NOT EXISTS (SELECT 1 FROM sync_record WHERE record_id = ?)`, id).Scan(&missing); err != nil {
				return fmt.Errorf("read sync journal: %w", err)
			}
			if !missing {
				return nil
			}
			var payload []byte
			var runID string
			if err := tx.QueryRowContext(ctx,
				`SELECT payload_json, run_id FROM evaluation_record WHERE id = ?`, id).Scan(&payload, &runID); err != nil {
				return fmt.Errorf("read evaluation record %s: %w", id, err)
			}
			record, err := Decode(payload)
			if err != nil {
				return err
			}
			if record.ID != id {
				return fmt.Errorf("%w: record row %s carries canonical bytes for %s", ErrInvalid, id, record.ID)
			}
			if _, err := s.stage(ctx, tx, runID, staged(id, payload)); err != nil {
				return err
			}
			added = 1
			return nil
		})
		if err != nil {
			return count, fmt.Errorf("restage evaluation record %s: %w", id, err)
		}
		count += added
	}
	return count, nil
}

// newID generates a globally unique client-side identifier. The prefix makes a
// stray id in a log self-describing without revealing anything about the
// corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// timestampLayout is RFC 3339 in UTC with a fixed nine-digit fraction, so text
// order is time order. time.RFC3339Nano trims trailing zeros, which would make
// two records written within a tenth of a second sort by measuring 'Z'
// against a digit - and that is exactly when ordering matters.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

// formatSeed stores a seed as decimal text rather than as an INTEGER, because
// SQLite's INTEGER is signed: a seed above 2^63 would round-trip as a negative
// number and a replay would draw something else.
func formatSeed(seed uint64) string { return strconv.FormatUint(seed, 10) }

func parseSeed(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	seed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse seed %q: %w", value, err)
	}
	return seed, nil
}
