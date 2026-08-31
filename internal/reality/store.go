package reality

import (
	"context"
	"database/sql"
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
// migration ledger. §9's durable file holds several components' tables — the
// hypothesis frontier's and the run records' beside these — and keying
// migrations by component lets each own its schema without a coordinating
// global version.
const component = "reality"

// databaseFile is the durable, pending-sync database. The Reality Ledger joins
// it rather than taking a file of its own because it is not rebuildable:
// losing it loses facts an operator answered for, which no re-index recovers.
// The retrieval index stays separate for the opposite reason.
const databaseFile = "durable.db"

// migrations are applied in order and never edited once released; §9 requires
// local SQLite migrations to be forward and transactional.
//
// Two things about the shape are deliberate. Every table mirrors §9's split as
// allowlisted columns plus one payload_json, so the sync slice replaces one
// column per table. And immutability is enforced by triggers rather than by
// this package's SQL: a ledger whose immutability depends on nobody writing
// the wrong statement is not immutable, and this is the file where a wrong
// statement would destroy the only copy of an answer. The single exception is
// a plan action's application state, which records what Babel did with the row
// rather than what the row says.
var migrations = []string{`
CREATE TABLE reality_entity(
	id             TEXT PRIMARY KEY,
	kind           TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE reality_resolution(
	id              TEXT PRIMARY KEY,
	resolution_kind TEXT NOT NULL,
	reverses_id     TEXT UNIQUE REFERENCES reality_resolution(id),
	actor           TEXT NOT NULL,
	recorded_at     TEXT NOT NULL,
	payload_json    TEXT NOT NULL
);

CREATE TABLE reality_resolution_member(
	resolution_id TEXT NOT NULL REFERENCES reality_resolution(id),
	member_role   TEXT NOT NULL CHECK(member_role IN ('source', 'result')),
	position      INTEGER NOT NULL,
	entity_id     TEXT NOT NULL REFERENCES reality_entity(id),
	PRIMARY KEY(resolution_id, member_role, position)
);
CREATE INDEX reality_resolution_member_entity ON reality_resolution_member(entity_id);

CREATE TABLE reality_entity_membership(
	entity_id     TEXT NOT NULL REFERENCES reality_entity(id),
	seq           INTEGER NOT NULL,
	role          TEXT NOT NULL,
	canonical_id  TEXT NOT NULL REFERENCES reality_entity(id),
	resolution_id TEXT REFERENCES reality_resolution(id),
	recorded_at   TEXT NOT NULL,
	PRIMARY KEY(entity_id, seq)
);
CREATE INDEX reality_entity_membership_canonical ON reality_entity_membership(canonical_id);

CREATE TABLE reality_entity_alias(
	id             TEXT PRIMARY KEY,
	entity_id      TEXT NOT NULL REFERENCES reality_entity(id),
	alias_kind     TEXT NOT NULL,
	value_key      TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	UNIQUE(alias_kind, value_key, entity_id)
);
CREATE INDEX reality_entity_alias_lookup ON reality_entity_alias(alias_kind, value_key);
CREATE INDEX reality_entity_alias_entity ON reality_entity_alias(entity_id);

CREATE TABLE reality_alias_event(
	id           TEXT PRIMARY KEY,
	alias_id     TEXT NOT NULL REFERENCES reality_entity_alias(id),
	seq          INTEGER NOT NULL,
	state        TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(alias_id, seq)
);

CREATE TABLE reality_relationship(
	id             TEXT PRIMARY KEY,
	from_id        TEXT NOT NULL REFERENCES reality_entity(id),
	to_id          TEXT NOT NULL REFERENCES reality_entity(id),
	relation_kind  TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	UNIQUE(from_id, to_id, relation_kind)
);
CREATE INDEX reality_relationship_to ON reality_relationship(to_id);

CREATE TABLE reality_relationship_event(
	id              TEXT PRIMARY KEY,
	relationship_id TEXT NOT NULL REFERENCES reality_relationship(id),
	seq             INTEGER NOT NULL,
	state           TEXT NOT NULL,
	recorded_at     TEXT NOT NULL,
	payload_json    TEXT NOT NULL,
	UNIQUE(relationship_id, seq)
);

CREATE TABLE reality_trusted_source(
	id             TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	source_version INTEGER NOT NULL,
	registered_at  TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE reality_trusted_source_predicate(
	source_id TEXT NOT NULL REFERENCES reality_trusted_source(id),
	predicate TEXT NOT NULL,
	PRIMARY KEY(source_id, predicate)
);

CREATE TABLE reality_trusted_source_entity(
	source_id TEXT NOT NULL REFERENCES reality_trusted_source(id),
	entity_id TEXT NOT NULL REFERENCES reality_entity(id),
	PRIMARY KEY(source_id, entity_id)
);

CREATE TABLE reality_trusted_source_kind(
	source_id   TEXT NOT NULL REFERENCES reality_trusted_source(id),
	entity_kind TEXT NOT NULL,
	PRIMARY KEY(source_id, entity_kind)
);

CREATE TABLE reality_import(
	id          TEXT PRIMARY KEY,
	source_id   TEXT NOT NULL REFERENCES reality_trusted_source(id),
	batch_key   TEXT NOT NULL,
	imported_at TEXT NOT NULL,
	fact_count  INTEGER NOT NULL,
	UNIQUE(source_id, batch_key)
);

CREATE TABLE reality_fact(
	id             TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	subject_id     TEXT NOT NULL REFERENCES reality_entity(id),
	predicate      TEXT NOT NULL,
	value_kind     TEXT NOT NULL,
	object_id      TEXT REFERENCES reality_entity(id),
	valid_from     TEXT NOT NULL,
	valid_until    TEXT,
	observed_at    TEXT NOT NULL,
	recorded_at    TEXT NOT NULL,
	expires_at     TEXT,
	authority_kind TEXT NOT NULL,
	authority_id   TEXT NOT NULL,
	authority_at   TEXT NOT NULL,
	confidence     TEXT NOT NULL,
	sensitivity    TEXT NOT NULL,
	supersedes     TEXT UNIQUE REFERENCES reality_fact(id),
	source_id      TEXT REFERENCES reality_trusted_source(id),
	import_id      TEXT REFERENCES reality_import(id),
	payload_json   TEXT NOT NULL,
	CHECK (authority_kind <> 'trusted-source' OR source_id IS NOT NULL),
	CHECK (authority_kind <> 'operator' OR source_id IS NULL)
);
CREATE INDEX reality_fact_subject ON reality_fact(subject_id, predicate);
CREATE INDEX reality_fact_expiry ON reality_fact(expires_at);
CREATE INDEX reality_fact_object ON reality_fact(object_id);

CREATE TABLE reality_fact_status(
	id           TEXT PRIMARY KEY,
	fact_id      TEXT NOT NULL REFERENCES reality_fact(id),
	seq          INTEGER NOT NULL,
	status       TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(fact_id, seq)
);

CREATE TABLE reality_dispute(
	id             TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	subject_id     TEXT NOT NULL REFERENCES reality_entity(id),
	predicate      TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	payload_json   TEXT NOT NULL
);

CREATE TABLE reality_dispute_member(
	dispute_id TEXT NOT NULL REFERENCES reality_dispute(id),
	fact_id    TEXT NOT NULL REFERENCES reality_fact(id),
	PRIMARY KEY(dispute_id, fact_id)
);
CREATE INDEX reality_dispute_member_fact ON reality_dispute_member(fact_id);

CREATE TABLE reality_dispute_event(
	id           TEXT PRIMARY KEY,
	dispute_id   TEXT NOT NULL REFERENCES reality_dispute(id),
	seq          INTEGER NOT NULL,
	state        TEXT NOT NULL,
	actor        TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(dispute_id, seq)
);

CREATE TABLE reality_focus_ruleset(
	version      INTEGER PRIMARY KEY,
	created_at   TEXT NOT NULL,
	payload_json TEXT NOT NULL
);

CREATE TABLE reality_context(
	id           TEXT PRIMARY KEY,
	author       TEXT NOT NULL,
	supplied_at  TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL
);

CREATE TABLE reality_question(
	id                 TEXT PRIMARY KEY,
	schema_version     INTEGER NOT NULL,
	question_kind      TEXT NOT NULL,
	question_class     TEXT NOT NULL,
	sensitivity        TEXT NOT NULL,
	expected_authority TEXT NOT NULL,
	dedupe_key         TEXT NOT NULL,
	avoided_cost       INTEGER NOT NULL,
	prompted_by_id     TEXT REFERENCES reality_question(id),
	created_at         TEXT NOT NULL,
	payload_json       TEXT NOT NULL
);
CREATE INDEX reality_question_dedupe ON reality_question(dedupe_key);

CREATE TABLE reality_question_entity(
	question_id TEXT NOT NULL REFERENCES reality_question(id),
	entity_id   TEXT NOT NULL REFERENCES reality_entity(id),
	PRIMARY KEY(question_id, entity_id)
);
CREATE INDEX reality_question_entity_entity ON reality_question_entity(entity_id);

CREATE TABLE reality_question_predicate(
	question_id TEXT NOT NULL REFERENCES reality_question(id),
	predicate   TEXT NOT NULL,
	PRIMARY KEY(question_id, predicate)
);

CREATE TABLE reality_question_fact(
	question_id TEXT NOT NULL REFERENCES reality_question(id),
	fact_id     TEXT NOT NULL REFERENCES reality_fact(id),
	fact_role   TEXT NOT NULL,
	PRIMARY KEY(question_id, fact_id, fact_role)
);

CREATE TABLE reality_question_work(
	question_id TEXT NOT NULL REFERENCES reality_question(id),
	work_kind   TEXT NOT NULL,
	work_id     TEXT NOT NULL,
	blocking    INTEGER NOT NULL,
	PRIMARY KEY(question_id, work_kind, work_id)
);

CREATE TABLE reality_question_evidence(
	question_id TEXT NOT NULL REFERENCES reality_question(id),
	item        TEXT NOT NULL,
	PRIMARY KEY(question_id, item)
);

CREATE TABLE reality_question_event(
	id           TEXT PRIMARY KEY,
	question_id  TEXT NOT NULL REFERENCES reality_question(id),
	seq          INTEGER NOT NULL,
	state        TEXT NOT NULL,
	actor        TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	UNIQUE(question_id, seq)
);

CREATE TABLE reality_answer(
	id             TEXT PRIMARY KEY,
	question_id    TEXT NOT NULL REFERENCES reality_question(id),
	schema_version INTEGER NOT NULL,
	seq            INTEGER NOT NULL,
	author         TEXT NOT NULL,
	answered_at    TEXT NOT NULL,
	recorded_at    TEXT NOT NULL,
	outcome        TEXT NOT NULL,
	context_id     TEXT REFERENCES reality_context(id),
	payload_json   TEXT NOT NULL,
	UNIQUE(question_id, seq)
);

CREATE TABLE reality_plan(
	id                  TEXT PRIMARY KEY,
	question_id         TEXT NOT NULL REFERENCES reality_question(id),
	answer_id           TEXT NOT NULL REFERENCES reality_answer(id),
	schema_version      INTEGER NOT NULL,
	interpreter_version INTEGER NOT NULL,
	created_at          TEXT NOT NULL,
	payload_json        TEXT NOT NULL
);
CREATE INDEX reality_plan_question ON reality_plan(question_id);

CREATE TABLE reality_plan_action(
	id           TEXT PRIMARY KEY,
	plan_id      TEXT NOT NULL REFERENCES reality_plan(id),
	position     INTEGER NOT NULL,
	action_kind  TEXT NOT NULL,
	state        TEXT NOT NULL,
	result_id    TEXT,
	applied_at   TEXT,
	payload_json TEXT NOT NULL,
	UNIQUE(plan_id, position)
);

CREATE TABLE reality_plan_acceptance(
	id           TEXT PRIMARY KEY,
	plan_id      TEXT NOT NULL UNIQUE REFERENCES reality_plan(id),
	actor        TEXT NOT NULL,
	context_id   TEXT REFERENCES reality_context(id),
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL
);

CREATE TABLE reality_plan_rejection(
	id           TEXT PRIMARY KEY,
	plan_id      TEXT NOT NULL UNIQUE REFERENCES reality_plan(id),
	actor        TEXT NOT NULL,
	recorded_at  TEXT NOT NULL,
	payload_json TEXT NOT NULL
);

CREATE TABLE reality_request(
	id            TEXT PRIMARY KEY,
	action_id     TEXT NOT NULL UNIQUE REFERENCES reality_plan_action(id),
	request_kind  TEXT NOT NULL,
	hypothesis_id TEXT,
	subject_kind  TEXT,
	subject_id    TEXT,
	created_at    TEXT NOT NULL,
	payload_json  TEXT NOT NULL
);

CREATE TABLE reality_snapshot(
	id              TEXT PRIMARY KEY,
	schema_version  INTEGER NOT NULL,
	hypothesis_id   TEXT NOT NULL,
	ruleset_version INTEGER NOT NULL REFERENCES reality_focus_ruleset(version),
	as_of           TEXT NOT NULL,
	created_at      TEXT NOT NULL,
	allowance       TEXT NOT NULL,
	payload_json    TEXT NOT NULL
);
CREATE INDEX reality_snapshot_hypothesis ON reality_snapshot(hypothesis_id);

CREATE TABLE reality_snapshot_entity(
	snapshot_id  TEXT NOT NULL REFERENCES reality_snapshot(id),
	position     INTEGER NOT NULL,
	entity_id    TEXT NOT NULL REFERENCES reality_entity(id),
	canonical_id TEXT NOT NULL REFERENCES reality_entity(id),
	allowance    TEXT NOT NULL,
	PRIMARY KEY(snapshot_id, position)
);

CREATE TABLE reality_snapshot_fact(
	snapshot_id TEXT NOT NULL REFERENCES reality_snapshot(id),
	position    INTEGER NOT NULL,
	fact_id     TEXT NOT NULL REFERENCES reality_fact(id),
	PRIMARY KEY(snapshot_id, position)
);

CREATE TRIGGER reality_entity_immutable BEFORE UPDATE ON reality_entity
BEGIN SELECT RAISE(ABORT, 'reality entities are immutable'); END;
CREATE TRIGGER reality_entity_kept BEFORE DELETE ON reality_entity
BEGIN SELECT RAISE(ABORT, 'reality entities are never deleted'); END;

CREATE TRIGGER reality_resolution_immutable BEFORE UPDATE ON reality_resolution
BEGIN SELECT RAISE(ABORT, 'entity resolutions are immutable'); END;
CREATE TRIGGER reality_resolution_kept BEFORE DELETE ON reality_resolution
BEGIN SELECT RAISE(ABORT, 'entity resolutions are never deleted'); END;

CREATE TRIGGER reality_resolution_member_immutable BEFORE UPDATE ON reality_resolution_member
BEGIN SELECT RAISE(ABORT, 'resolution members are immutable'); END;
CREATE TRIGGER reality_resolution_member_kept BEFORE DELETE ON reality_resolution_member
BEGIN SELECT RAISE(ABORT, 'resolution members are never deleted'); END;

CREATE TRIGGER reality_membership_immutable BEFORE UPDATE ON reality_entity_membership
BEGIN SELECT RAISE(ABORT, 'entity membership history is append-only'); END;
CREATE TRIGGER reality_membership_kept BEFORE DELETE ON reality_entity_membership
BEGIN SELECT RAISE(ABORT, 'entity membership history is append-only'); END;

CREATE TRIGGER reality_alias_immutable BEFORE UPDATE ON reality_entity_alias
BEGIN SELECT RAISE(ABORT, 'aliases are immutable; retract instead'); END;
CREATE TRIGGER reality_alias_kept BEFORE DELETE ON reality_entity_alias
BEGIN SELECT RAISE(ABORT, 'aliases are never deleted; retract instead'); END;

CREATE TRIGGER reality_alias_event_immutable BEFORE UPDATE ON reality_alias_event
BEGIN SELECT RAISE(ABORT, 'alias history is append-only'); END;
CREATE TRIGGER reality_alias_event_kept BEFORE DELETE ON reality_alias_event
BEGIN SELECT RAISE(ABORT, 'alias history is append-only'); END;

CREATE TRIGGER reality_relationship_immutable BEFORE UPDATE ON reality_relationship
BEGIN SELECT RAISE(ABORT, 'relationships are immutable; retract instead'); END;
CREATE TRIGGER reality_relationship_kept BEFORE DELETE ON reality_relationship
BEGIN SELECT RAISE(ABORT, 'relationships are never deleted; retract instead'); END;

CREATE TRIGGER reality_relationship_event_immutable BEFORE UPDATE ON reality_relationship_event
BEGIN SELECT RAISE(ABORT, 'relationship history is append-only'); END;
CREATE TRIGGER reality_relationship_event_kept BEFORE DELETE ON reality_relationship_event
BEGIN SELECT RAISE(ABORT, 'relationship history is append-only'); END;

CREATE TRIGGER reality_source_immutable BEFORE UPDATE ON reality_trusted_source
BEGIN SELECT RAISE(ABORT, 'a trusted source registration is immutable; register a new version'); END;
CREATE TRIGGER reality_source_kept BEFORE DELETE ON reality_trusted_source
BEGIN SELECT RAISE(ABORT, 'trusted source registrations are never deleted'); END;

CREATE TRIGGER reality_source_predicate_immutable BEFORE UPDATE ON reality_trusted_source_predicate
BEGIN SELECT RAISE(ABORT, 'a declared predicate scope is immutable'); END;
CREATE TRIGGER reality_source_predicate_kept BEFORE DELETE ON reality_trusted_source_predicate
BEGIN SELECT RAISE(ABORT, 'a declared predicate scope is never deleted'); END;

CREATE TRIGGER reality_source_entity_immutable BEFORE UPDATE ON reality_trusted_source_entity
BEGIN SELECT RAISE(ABORT, 'a declared entity scope is immutable'); END;
CREATE TRIGGER reality_source_entity_kept BEFORE DELETE ON reality_trusted_source_entity
BEGIN SELECT RAISE(ABORT, 'a declared entity scope is never deleted'); END;

CREATE TRIGGER reality_source_kind_immutable BEFORE UPDATE ON reality_trusted_source_kind
BEGIN SELECT RAISE(ABORT, 'a declared entity-kind scope is immutable'); END;
CREATE TRIGGER reality_source_kind_kept BEFORE DELETE ON reality_trusted_source_kind
BEGIN SELECT RAISE(ABORT, 'a declared entity-kind scope is never deleted'); END;

CREATE TRIGGER reality_import_immutable BEFORE UPDATE ON reality_import
BEGIN SELECT RAISE(ABORT, 'import batches are immutable'); END;
CREATE TRIGGER reality_import_kept BEFORE DELETE ON reality_import
BEGIN SELECT RAISE(ABORT, 'import batches are never deleted'); END;

CREATE TRIGGER reality_fact_immutable BEFORE UPDATE ON reality_fact
BEGIN SELECT RAISE(ABORT, 'facts are immutable; supersede instead'); END;
CREATE TRIGGER reality_fact_kept BEFORE DELETE ON reality_fact
BEGIN SELECT RAISE(ABORT, 'facts are never deleted; supersede or expire instead'); END;

CREATE TRIGGER reality_fact_status_immutable BEFORE UPDATE ON reality_fact_status
BEGIN SELECT RAISE(ABORT, 'fact status history is append-only'); END;
CREATE TRIGGER reality_fact_status_kept BEFORE DELETE ON reality_fact_status
BEGIN SELECT RAISE(ABORT, 'fact status history is append-only'); END;

CREATE TRIGGER reality_dispute_immutable BEFORE UPDATE ON reality_dispute
BEGIN SELECT RAISE(ABORT, 'disputes are immutable'); END;
CREATE TRIGGER reality_dispute_kept BEFORE DELETE ON reality_dispute
BEGIN SELECT RAISE(ABORT, 'disputes are never deleted'); END;

CREATE TRIGGER reality_dispute_member_immutable BEFORE UPDATE ON reality_dispute_member
BEGIN SELECT RAISE(ABORT, 'dispute membership is immutable'); END;
CREATE TRIGGER reality_dispute_member_kept BEFORE DELETE ON reality_dispute_member
BEGIN SELECT RAISE(ABORT, 'dispute membership is never deleted'); END;

CREATE TRIGGER reality_dispute_event_immutable BEFORE UPDATE ON reality_dispute_event
BEGIN SELECT RAISE(ABORT, 'dispute history is append-only'); END;
CREATE TRIGGER reality_dispute_event_kept BEFORE DELETE ON reality_dispute_event
BEGIN SELECT RAISE(ABORT, 'dispute history is append-only'); END;

CREATE TRIGGER reality_focus_ruleset_immutable BEFORE UPDATE ON reality_focus_ruleset
BEGIN SELECT RAISE(ABORT, 'a focus rule set version is immutable; install a new version'); END;
CREATE TRIGGER reality_focus_ruleset_kept BEFORE DELETE ON reality_focus_ruleset
BEGIN SELECT RAISE(ABORT, 'focus rule set versions are never deleted'); END;

CREATE TRIGGER reality_context_immutable BEFORE UPDATE ON reality_context
BEGIN SELECT RAISE(ABORT, 'operator context is immutable'); END;
CREATE TRIGGER reality_context_kept BEFORE DELETE ON reality_context
BEGIN SELECT RAISE(ABORT, 'operator context is never deleted'); END;

CREATE TRIGGER reality_question_immutable BEFORE UPDATE ON reality_question
BEGIN SELECT RAISE(ABORT, 'questions are immutable; state is an appended event'); END;
CREATE TRIGGER reality_question_kept BEFORE DELETE ON reality_question
BEGIN SELECT RAISE(ABORT, 'questions are never deleted'); END;

CREATE TRIGGER reality_question_entity_kept BEFORE DELETE ON reality_question_entity
BEGIN SELECT RAISE(ABORT, 'question targets are never deleted'); END;
CREATE TRIGGER reality_question_entity_immutable BEFORE UPDATE ON reality_question_entity
BEGIN SELECT RAISE(ABORT, 'question targets are immutable'); END;

CREATE TRIGGER reality_question_predicate_kept BEFORE DELETE ON reality_question_predicate
BEGIN SELECT RAISE(ABORT, 'question targets are never deleted'); END;
CREATE TRIGGER reality_question_predicate_immutable BEFORE UPDATE ON reality_question_predicate
BEGIN SELECT RAISE(ABORT, 'question targets are immutable'); END;

CREATE TRIGGER reality_question_fact_kept BEFORE DELETE ON reality_question_fact
BEGIN SELECT RAISE(ABORT, 'question fact links are never deleted'); END;
CREATE TRIGGER reality_question_fact_immutable BEFORE UPDATE ON reality_question_fact
BEGIN SELECT RAISE(ABORT, 'question fact links are immutable'); END;

CREATE TRIGGER reality_question_work_kept BEFORE DELETE ON reality_question_work
BEGIN SELECT RAISE(ABORT, 'dependent work links are never deleted'); END;
CREATE TRIGGER reality_question_work_immutable BEFORE UPDATE ON reality_question_work
BEGIN SELECT RAISE(ABORT, 'dependent work links are immutable'); END;

CREATE TRIGGER reality_question_evidence_kept BEFORE DELETE ON reality_question_evidence
BEGIN SELECT RAISE(ABORT, 'question evidence keys are never deleted'); END;
CREATE TRIGGER reality_question_evidence_immutable BEFORE UPDATE ON reality_question_evidence
BEGIN SELECT RAISE(ABORT, 'question evidence keys are immutable'); END;

CREATE TRIGGER reality_question_event_immutable BEFORE UPDATE ON reality_question_event
BEGIN SELECT RAISE(ABORT, 'question state history is append-only'); END;
CREATE TRIGGER reality_question_event_kept BEFORE DELETE ON reality_question_event
BEGIN SELECT RAISE(ABORT, 'question state history is append-only'); END;

CREATE TRIGGER reality_answer_immutable BEFORE UPDATE ON reality_answer
BEGIN SELECT RAISE(ABORT, 'an answer is retained verbatim and is immutable'); END;
CREATE TRIGGER reality_answer_kept BEFORE DELETE ON reality_answer
BEGIN SELECT RAISE(ABORT, 'answers are never deleted'); END;

CREATE TRIGGER reality_plan_immutable BEFORE UPDATE ON reality_plan
BEGIN SELECT RAISE(ABORT, 'plans are immutable; interpret again for a new plan'); END;
CREATE TRIGGER reality_plan_kept BEFORE DELETE ON reality_plan
BEGIN SELECT RAISE(ABORT, 'plans are never deleted'); END;

CREATE TRIGGER reality_plan_action_immutable
BEFORE UPDATE OF id, plan_id, position, action_kind, payload_json ON reality_plan_action
BEGIN SELECT RAISE(ABORT, 'a plan action content is immutable'); END;
CREATE TRIGGER reality_plan_action_kept BEFORE DELETE ON reality_plan_action
BEGIN SELECT RAISE(ABORT, 'plan actions are never deleted'); END;

CREATE TRIGGER reality_acceptance_immutable BEFORE UPDATE ON reality_plan_acceptance
BEGIN SELECT RAISE(ABORT, 'an acceptance is immutable'); END;
CREATE TRIGGER reality_acceptance_kept BEFORE DELETE ON reality_plan_acceptance
BEGIN SELECT RAISE(ABORT, 'acceptances are never deleted'); END;

CREATE TRIGGER reality_rejection_immutable BEFORE UPDATE ON reality_plan_rejection
BEGIN SELECT RAISE(ABORT, 'a rejection is immutable'); END;
CREATE TRIGGER reality_rejection_kept BEFORE DELETE ON reality_plan_rejection
BEGIN SELECT RAISE(ABORT, 'rejections are never deleted'); END;

CREATE TRIGGER reality_request_immutable BEFORE UPDATE ON reality_request
BEGIN SELECT RAISE(ABORT, 'a recorded request is immutable'); END;
CREATE TRIGGER reality_request_kept BEFORE DELETE ON reality_request
BEGIN SELECT RAISE(ABORT, 'recorded requests are never deleted'); END;

CREATE TRIGGER reality_snapshot_immutable BEFORE UPDATE ON reality_snapshot
BEGIN SELECT RAISE(ABORT, 'a context snapshot is immutable'); END;
CREATE TRIGGER reality_snapshot_kept BEFORE DELETE ON reality_snapshot
BEGIN SELECT RAISE(ABORT, 'context snapshots are never deleted'); END;

CREATE TRIGGER reality_snapshot_entity_immutable BEFORE UPDATE ON reality_snapshot_entity
BEGIN SELECT RAISE(ABORT, 'a context snapshot is immutable'); END;
CREATE TRIGGER reality_snapshot_entity_kept BEFORE DELETE ON reality_snapshot_entity
BEGIN SELECT RAISE(ABORT, 'context snapshots are never deleted'); END;

CREATE TRIGGER reality_snapshot_fact_immutable BEFORE UPDATE ON reality_snapshot_fact
BEGIN SELECT RAISE(ABORT, 'a context snapshot is immutable'); END;
CREATE TRIGGER reality_snapshot_fact_kept BEFORE DELETE ON reality_snapshot_fact
BEGIN SELECT RAISE(ABORT, 'context snapshots are never deleted'); END;
`}

// HypothesisSink retains a candidate hypothesis a plan produced.
//
// It is an injected interface rather than a direct call into internal/frontier
// because the two are separate durable components sharing one file, and one
// component's transaction must not hold the write lock while the other's
// connection tries to insert. Retention therefore happens before the plan's
// transaction opens, and cross-component atomicity is not claimed — §9's
// invariant says exactly that about multi-store commits. The consequence is
// stated rather than hidden: if the plan write then fails, the frontier holds a
// candidate no plan references, which §5.2 keeps anyway because every emitted
// candidate persists.
type HypothesisSink interface {
	// RecordHypothesis persists the candidate and returns its ID.
	RecordHypothesis(ctx context.Context, draft HypothesisDraft) (string, error)
}

// Store is the durable Reality Ledger.
type Store struct {
	db   *sql.DB
	path string

	// now supplies record timestamps. It is replaceable so a test can make
	// ordering and freshness deterministic without sleeping.
	now func() time.Time

	// sink retains plan-produced hypotheses. Nil until configured, and a
	// plan that would create one is refused rather than silently dropping
	// the candidate.
	sink HypothesisSink

	// sync publishes this ledger's durable records to the shared backend.
	// Nil is local-only mode and a supported deployment: nothing is staged,
	// no write path changes, and the records stay on this disk. See
	// publish.go for what is published and what deliberately is not.
	sync babelsync.Hook

	// faultBeforeDisposition is a fault-injection seam used only by the
	// acceptance atomicity test. It runs inside AcceptPlan's transaction,
	// after the acceptance and the authoritative actions are written and
	// before the question's disposition is appended, so a test can prove
	// the two commit together or not at all. It is nil on every production
	// path.
	faultBeforeDisposition func() error
}

// Option configures a Store at open time.
type Option func(*Store)

// WithClock replaces the store's clock. Freshness, expiry, and inbox
// staleness are all time-dependent, so a test that could not control the clock
// would have to sleep for a TTL to observe expiry at all.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// WithHypothesisSink configures where a plan's retained hypotheses go.
func WithHypothesisSink(sink HypothesisSink) Option {
	return func(s *Store) { s.sink = sink }
}

// WithSync attaches the Phase B publication hook.
//
// SPEC.md §266 makes globally durable encrypted Reality Ledger state a Phase B
// capability, and this is what makes it one: without a hook the ledger is
// durable only on the machine that wrote it, so a dead workstation disk loses
// facts an operator answered for and no re-index recovers them.
//
// A nil hook is local-only mode rather than a misconfiguration, which is why
// this is an option and not a parameter of Open.
func WithSync(h babelsync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// Open opens the durable database in dir, creating the directory and applying
// this component's pending migrations.
//
// A schema surprise is an error and never a rebuild. The retrieval index may
// be discarded because every row in it is derivable; a fact an operator
// answered for is not, so meeting an unfamiliar schema means stopping rather
// than deleting reality to make the shape fit.
func Open(dir string, opts ...Option) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reality: create durable state directory: %w", err)
	}
	path := filepath.Join(dir, databaseFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("reality: open durable database: %w", err)
	}
	// One connection: the pragmas below are per-connection, and §9 gives the
	// durable file a single local state writer.
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
		// The journal's tables have to exist on this handle before a write
		// path can stage on it, because staging shares the writer's own
		// transaction on the writer's own connection. It runs after the
		// migrations so an unfamiliar ledger schema still stops the store
		// before anything touches the file, and it is cheap and idempotent.
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
		return fmt.Errorf("reality: check durable database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("reality: check durable database integrity: %s", integrity)
	}
	var journal string
	if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("reality: enable durable WAL: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fmt.Errorf("reality: set durable busy timeout: %w", err)
	}
	// Foreign keys carry the ledger's referential rules into the engine: a
	// fact about an entity that is not there, or a dispute over a fact that
	// is not, are exactly the states the authority rules exist to prevent.
	if _, err := s.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("reality: enable durable foreign keys: %w", err)
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration(
		component TEXT PRIMARY KEY,
		version   INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("reality: create durable migration ledger: %w", err)
	}

	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reality: read durable schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("reality: durable schema version %d is newer than this build supports (%d)",
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
		return fmt.Errorf("reality: begin migration %d: %w", index+1, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("reality: apply migration %d: %w", index+1, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, ?)
		ON CONFLICT(component) DO UPDATE SET version = excluded.version`, component, index+1); err != nil {
		return fmt.Errorf("reality: record migration %d: %w", index+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reality: commit migration %d: %w", index+1, err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Path reports the durable database path, which an operator needs when the
// pending-sync ledger has to be backed up before a machine is rebuilt.
func (s *Store) Path() string { return s.path }

// transact runs fn in one transaction, rolling back on any error. Every
// multi-row write goes through it, because a half-applied plan or a merge
// whose membership rows are missing would leave the ledger saying something
// nobody decided.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reality: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reality: commit transaction: %w", err)
	}
	return nil
}

// querier is the read subset shared by *sql.DB and *sql.Tx, so one read serves
// both a standalone query and one inside a write transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// requireRow turns a dangling reference into ErrUnknownRecord before the
// insert, so a caller learns which reference dangled instead of reading an
// opaque foreign-key message.
func requireRow(ctx context.Context, q querier, table, column, id string) error {
	var found int
	// table and column are never caller-supplied: every call site passes
	// constants.
	err := q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE `+column+` = ?`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %q", ErrUnknownRecord, strings.TrimPrefix(table, "reality_"), id)
	}
	if err != nil {
		return fmt.Errorf("reality: check %s %q: %w", table, id, err)
	}
	return nil
}

// nextSeq reads the next position in an append-only history. The history's
// unique index on (subject, seq) is what actually prevents two concurrent
// appends from sharing a position; this only chooses the value.
func nextSeq(ctx context.Context, q querier, table, column, id string) (int, error) {
	var seq sql.NullInt64
	if err := q.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM `+table+` WHERE `+column+` = ?`, id).Scan(&seq); err != nil {
		return 0, fmt.Errorf("reality: read %s sequence: %w", table, err)
	}
	return int(seq.Int64) + 1, nil
}

func queryStrings(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reality: read ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("reality: read ids: %w", err)
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// Plans returns every plan proposed for one question, oldest first. A question
// can be interpreted more than once — §4.8 sends a rejected plan back to
// answered-uninterpreted for another attempt — so the set matters, not just the
// latest.
//
// Without this, a caller had to recover plan identifiers from the question's
// append-only history and verify each through Plan, which works only as long as
// the note format holds. A query is not merely tidier: it cannot drift.
func (s *Store) Plans(ctx context.Context, questionID string) ([]Plan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM reality_plan WHERE question_id = ? ORDER BY created_at, id`, questionID)
	if err != nil {
		return nil, fmt.Errorf("list plans for question %s: %w", questionID, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list plans for question %s: %w", questionID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list plans for question %s: %w", questionID, err)
	}
	rows.Close()

	out := make([]Plan, 0, len(ids))
	for _, id := range ids {
		plan, err := s.Plan(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, nil
}
