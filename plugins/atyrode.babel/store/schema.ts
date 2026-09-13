/*
  THE STORE OF atyrode.babel, spelled once as SQL (ADR 0034: a plugin's tables are SQL, and SQL
  is the schema DSL). One migration per shape change, named and ledgered by the engine; this is
  the first, and it carries every fact the Go stores held that the product still needs — the
  importer (`tools/import.ts`) reads `durable.db` into exactly these tables, ids kept, so
  provenance survives the rewrite (decision 91: import once, then retire).

  What changed in the crossing, and why:
  - ninety-four tables become twenty-three. The Go tree kept a table per concept per package; here a
    record is a record whatever its kind, an edge is an edge whatever it relates, and a revision
    is a row that supersedes another rather than a parallel table of revisions.
  - nothing is sealed and nothing is synced. The hub is the one place (§9 is retired); a row is
    plaintext on the operator's own server, and "published" is a word this schema does not need.
  - every table that records an act is append-only by trigger: a ruling, a vote, a filing, a
    status, a fact are written once and superseded by a later row, never edited or deleted. The
    triggers below are the whole of that guarantee, and a purge is the engine deleting the file.
  - the operator is the boundary: `actor_kind` is `operator`, `run` or `engine` on every row that
    something wrote, so "who did this" is a column and never an inference.
 */

export const STORE_DATA_VERSION = { major: 1, minor: 0 } as const;

/** Statements of the first migration, in order; each is one `run`. */
export const SCHEMA_V1: readonly string[] = [
  // ---------------------------------------------------------------- the catalog
  `CREATE TABLE sessions(
     selector TEXT PRIMARY KEY,
     host TEXT NOT NULL,
     harness TEXT NOT NULL,
     source_id TEXT NOT NULL,
     title TEXT,
     title_provenance TEXT,
     workspace TEXT,
     repository_identity TEXT,
     repository_remote TEXT,
     repository_reason TEXT,
     modified_at TEXT,
     size INTEGER,
     cost_usd REAL,
     total_tokens INTEGER,
     turns INTEGER,
     tool_errors INTEGER,
     content_digest TEXT,
     snapshot_id TEXT,
     archived_at TEXT,
     seen_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX sessions_by_repository ON sessions(repository_identity)`,
  `CREATE INDEX sessions_by_host ON sessions(host, modified_at DESC)`,

  // ---------------------------------------------------------------- records and their relations
  // Every revision of every hypothesis, observation, finding and proposal is a row; the head of
  // a record is the row no other row supersedes. `parent_id` is an observation's hypothesis
  // (§4.3: an observation hangs off exactly one). `payload` is the record's own fields as JSON,
  // versioned inside by `schema`, so a shape change in a record is a payload version, not a
  // column.
  `CREATE TABLE records(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL CHECK (kind IN ('hypothesis','observation','finding','proposal')),
     root_id TEXT NOT NULL,
     supersedes_id TEXT REFERENCES records(id),
     seq INTEGER NOT NULL DEFAULT 0,
     parent_id TEXT,
     run_id TEXT,
     recipe_id TEXT,
     recipe_version INTEGER,
     actor_kind TEXT NOT NULL CHECK (actor_kind IN ('run','operator','engine')),
     actor_id TEXT NOT NULL,
     title TEXT NOT NULL,
     created_at TEXT NOT NULL,
     payload TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX records_by_kind_created ON records(kind, created_at DESC)`,
  `CREATE INDEX records_by_root ON records(root_id, seq)`,
  `CREATE INDEX records_by_parent ON records(parent_id)`,
  `CREATE INDEX records_by_run ON records(run_id)`,
  `CREATE TRIGGER records_immutable BEFORE UPDATE ON records BEGIN
     SELECT RAISE(ABORT, 'a record is never edited; supersede it');
   END`,
  `CREATE TRIGGER records_kept BEFORE DELETE ON records BEGIN
     SELECT RAISE(ABORT, 'a record is never deleted');
   END`,

  // One table for every relation: a citation of a session (evidence), the chain
  // (derived_from, consolidates, addresses), supersession, refinement, duplication, and
  // `about` — a record filed under an entity (§4.13). `to_kind` names the namespace of the
  // target: a record kind, `session`, or `entity`.
  `CREATE TABLE edges(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL,
     from_kind TEXT NOT NULL,
     from_id TEXT NOT NULL,
     to_kind TEXT NOT NULL,
     to_id TEXT NOT NULL,
     position INTEGER,
     note TEXT,
     actor_kind TEXT NOT NULL,
     actor_id TEXT NOT NULL,
     created_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX edges_from ON edges(from_kind, from_id, kind)`,
  `CREATE INDEX edges_to ON edges(to_kind, to_id, kind)`,
  `CREATE TRIGGER edges_kept BEFORE DELETE ON edges BEGIN
     SELECT RAISE(ABORT, 'an edge is never deleted');
   END`,

  // A hypothesis's lifecycle, append-only: untriaged, deferred, promoted, rejected, superseded,
  // retired. The current status is the newest row.
  `CREATE TABLE status_events(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL REFERENCES records(id),
     seq INTEGER NOT NULL,
     status TEXT NOT NULL,
     run_id TEXT,
     actor_kind TEXT NOT NULL,
     actor_id TEXT NOT NULL,
     reason TEXT,
     recorded_at TEXT NOT NULL,
     UNIQUE (record_id, seq)
   ) STRICT`,
  `CREATE TRIGGER status_events_kept BEFORE DELETE ON status_events BEGIN
     SELECT RAISE(ABORT, 'a status is never deleted');
   END`,

  // ---------------------------------------------------------------- the operator's acts
  // Rulings (§4.7): accept, reject, defer, duplicate, reopen, refine. Every one is the
  // operator's; the newest per record is its standing.
  `CREATE TABLE dispositions(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL REFERENCES records(id),
     seq INTEGER NOT NULL,
     disposition TEXT NOT NULL CHECK (disposition IN ('accept','reject','defer','duplicate','reopen','refine')),
     duplicate_of_id TEXT,
     note TEXT,
     context_id TEXT,
     actor_id TEXT NOT NULL,
     recorded_at TEXT NOT NULL,
     UNIQUE (record_id, seq)
   ) STRICT`,
  `CREATE INDEX dispositions_by_record ON dispositions(record_id, seq DESC)`,
  `CREATE TRIGGER dispositions_immutable BEFORE UPDATE ON dispositions BEGIN
     SELECT RAISE(ABORT, 'a ruling is never edited');
   END`,
  `CREATE TRIGGER dispositions_kept BEFORE DELETE ON dispositions BEGIN
     SELECT RAISE(ABORT, 'a ruling is never deleted');
   END`,

  // Filings (§4.13): a record under an entity, with the rationale and who filed it; a re-filing
  // supersedes, a withdrawal is a row with `withdrawn = 1`, and `entity_id = ''` with a reason is
  // "about nothing in particular". `heuristic` marks a filing a run should revisit.
  `CREATE TABLE filings(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL REFERENCES records(id),
     entity_id TEXT NOT NULL,
     rationale TEXT NOT NULL,
     author_kind TEXT NOT NULL CHECK (author_kind IN ('operator','run','heuristic')),
     author_id TEXT NOT NULL,
     heuristic INTEGER NOT NULL DEFAULT 0,
     withdrawn INTEGER NOT NULL DEFAULT 0,
     supersedes_id TEXT REFERENCES filings(id),
     created_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX filings_by_record ON filings(record_id, created_at DESC)`,
  `CREATE INDEX filings_by_entity ON filings(entity_id, created_at DESC)`,
  `CREATE TRIGGER filings_kept BEFORE DELETE ON filings BEGIN
     SELECT RAISE(ABORT, 'a filing is never deleted; withdraw it');
   END`,

  // The operator's own words on a record (§8.6/§8.7): a comment, a question to Babel, or an
  // earlier stance kept as history. `question = 1` is what the next review must answer.
  `CREATE TABLE feedback(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL,
     actor_id TEXT NOT NULL,
     stance TEXT,
     reason TEXT NOT NULL DEFAULT '',
     question INTEGER NOT NULL DEFAULT 0,
     related_id TEXT,
     recorded_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX feedback_by_record ON feedback(record_id, recorded_at DESC)`,
  `CREATE TRIGGER feedback_kept BEFORE DELETE ON feedback BEGIN
     SELECT RAISE(ABORT, 'feedback is never deleted');
   END`,

  // Steering (§4.7's complaints, `Tell Babel`): what the operator told Babel, threaded by
  // `root_id`, and the replies runs record against it.
  `CREATE TABLE steering(
     id TEXT PRIMARY KEY,
     root_id TEXT NOT NULL,
     reply_to_id TEXT,
     seq INTEGER NOT NULL,
     actor_kind TEXT NOT NULL,
     actor_id TEXT NOT NULL,
     target_kind TEXT,
     target_id TEXT,
     text TEXT NOT NULL,
     recorded_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX steering_by_root ON steering(root_id, seq)`,
  `CREATE INDEX steering_by_target ON steering(target_kind, target_id)`,

  // ---------------------------------------------------------------- Babel's reviewers (§4.12)
  // One assessment per run per role on one exact revision: a vote, contributions, an outcome
  // or a filing/backlog result, as JSON. A correction supersedes.
  `CREATE TABLE assessments(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL,
     revision_id TEXT NOT NULL,
     run_id TEXT NOT NULL,
     role TEXT NOT NULL,
     vote TEXT CHECK (vote IN ('support','oppose','unsure') OR vote IS NULL),
     lane TEXT,
     claim_id TEXT,
     supersedes_id TEXT REFERENCES assessments(id),
     payload TEXT NOT NULL,
     recorded_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX assessments_by_record ON assessments(record_id, recorded_at DESC)`,
  `CREATE INDEX assessments_by_run ON assessments(run_id)`,
  `CREATE TRIGGER assessments_immutable BEFORE UPDATE ON assessments BEGIN
     SELECT RAISE(ABORT, 'an assessment is never edited; supersede it');
   END`,
  `CREATE TRIGGER assessments_kept BEFORE DELETE ON assessments BEGIN
     SELECT RAISE(ABORT, 'an assessment is never deleted');
   END`,

  // Review coordination: a claim is one reviewer's grant on one record in one role, leased and
  // renewed by the job that holds it; finished rows are the spend ledger.
  `CREATE TABLE claims(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL,
     role TEXT NOT NULL,
     lane TEXT NOT NULL,
     policy_version TEXT NOT NULL,
     job_id TEXT,
     run_id TEXT,
     fence INTEGER NOT NULL DEFAULT 1,
     reserved_cost REAL NOT NULL DEFAULT 0,
     actual_cost REAL,
     granted_at TEXT NOT NULL,
     expires_at TEXT NOT NULL,
     finished_at TEXT,
     outcome TEXT
   ) STRICT`,
  `CREATE INDEX claims_open ON claims(record_id, role) WHERE finished_at IS NULL`,
  `CREATE INDEX claims_by_day ON claims(granted_at)`,

  // The evaluation policy, versioned; the newest row is in force. Written only by the operator.
  `CREATE TABLE policies(
     version TEXT PRIMARY KEY,
     seq INTEGER NOT NULL UNIQUE,
     actor_id TEXT NOT NULL,
     reason TEXT NOT NULL DEFAULT '',
     payload TEXT NOT NULL,
     recorded_at TEXT NOT NULL
   ) STRICT`,

  // ---------------------------------------------------------------- the Reality Ledger (§4.8)
  `CREATE TABLE entities(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL,
     name TEXT NOT NULL,
     canonical_id TEXT NOT NULL,
     created_by TEXT NOT NULL,
     created_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX entities_by_canonical ON entities(canonical_id)`,
  `CREATE TABLE aliases(
     id TEXT PRIMARY KEY,
     entity_id TEXT NOT NULL REFERENCES entities(id),
     kind TEXT NOT NULL,
     value TEXT NOT NULL,
     value_key TEXT NOT NULL,
     retired_at TEXT,
     created_at TEXT NOT NULL,
     UNIQUE (kind, value_key, entity_id)
   ) STRICT`,
  `CREATE INDEX aliases_by_value ON aliases(kind, value_key)`,
  // A fact is immutable; its status (proposed, active, superseded, disputed, stale) is the
  // newest status row; lifecycle and analysis-policy are predicates like any other.
  `CREATE TABLE facts(
     id TEXT PRIMARY KEY,
     entity_id TEXT NOT NULL REFERENCES entities(id),
     predicate TEXT NOT NULL,
     value TEXT NOT NULL,
     object_id TEXT,
     valid_from TEXT NOT NULL,
     valid_until TEXT,
     observed_at TEXT NOT NULL,
     authority_kind TEXT NOT NULL,
     authority_id TEXT NOT NULL,
     confidence TEXT NOT NULL DEFAULT 'stated',
     note TEXT,
     supersedes_id TEXT REFERENCES facts(id),
     recorded_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX facts_by_entity ON facts(entity_id, predicate, recorded_at DESC)`,
  `CREATE TABLE fact_status(
     id TEXT PRIMARY KEY,
     fact_id TEXT NOT NULL REFERENCES facts(id),
     seq INTEGER NOT NULL,
     status TEXT NOT NULL,
     actor_id TEXT,
     reason TEXT,
     recorded_at TEXT NOT NULL,
     UNIQUE (fact_id, seq)
   ) STRICT`,
  // Merge, split and their undoing, append-only, with the members each named.
  `CREATE TABLE resolutions(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL CHECK (kind IN ('merge','split','undo')),
     reverses_id TEXT,
     actor_id TEXT NOT NULL,
     reason TEXT NOT NULL,
     recorded_at TEXT NOT NULL
   ) STRICT`,
  `CREATE TABLE resolution_members(
     resolution_id TEXT NOT NULL REFERENCES resolutions(id),
     role TEXT NOT NULL,
     position INTEGER NOT NULL,
     entity_id TEXT NOT NULL,
     PRIMARY KEY (resolution_id, role, position)
   ) STRICT`,
  // Questions Babel raises and their states; answers verbatim; plans an interpreter proposed
  // and the operator's acceptance or rejection of them.
  `CREATE TABLE questions(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL,
     class TEXT NOT NULL,
     text TEXT NOT NULL,
     why TEXT NOT NULL,
     dedupe_key TEXT,
     raised_by_kind TEXT NOT NULL,
     raised_by_id TEXT NOT NULL,
     payload TEXT NOT NULL,
     created_at TEXT NOT NULL
   ) STRICT`,
  `CREATE TABLE question_events(
     id TEXT PRIMARY KEY,
     question_id TEXT NOT NULL REFERENCES questions(id),
     seq INTEGER NOT NULL,
     state TEXT NOT NULL,
     actor_id TEXT,
     reason TEXT,
     recorded_at TEXT NOT NULL,
     UNIQUE (question_id, seq)
   ) STRICT`,
  `CREATE TABLE answers(
     id TEXT PRIMARY KEY,
     question_id TEXT NOT NULL REFERENCES questions(id),
     actor_id TEXT NOT NULL,
     outcome TEXT NOT NULL,
     text TEXT NOT NULL,
     recorded_at TEXT NOT NULL
   ) STRICT`,
  // Plans: a topic plan or a backlog plan keyed on the proposal that carries it, or an answer's
  // interpretation keyed on the question; applied or declined by one ruling.
  `CREATE TABLE plans(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL CHECK (kind IN ('topic','backlog','answer')),
     subject_kind TEXT NOT NULL,
     subject_id TEXT NOT NULL,
     operation TEXT NOT NULL,
     dedupe_key TEXT,
     payload TEXT NOT NULL,
     proposed_by_kind TEXT NOT NULL,
     proposed_by_id TEXT NOT NULL,
     state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','applied','declined')),
     ruled_by TEXT,
     ruled_at TEXT,
     ruling_reason TEXT,
     result TEXT,
     created_at TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX plans_open ON plans(kind, state, created_at)`,
  `CREATE INDEX plans_by_subject ON plans(subject_kind, subject_id)`,

  // ---------------------------------------------------------------- runs (§7)
  // A run is a job on a machine: what it was asked, what it read, what it produced, what it
  // cost. `payload` is the receipt as the machine half wrote it.
  `CREATE TABLE runs(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL,
     machine_id TEXT,
     job_id TEXT,
     recipe_id TEXT,
     profile TEXT,
     authority_kind TEXT,
     authority_id TEXT,
     preparation TEXT,
     started_at TEXT NOT NULL,
     finished_at TEXT,
     closure TEXT,
     cost_usd REAL,
     tokens INTEGER,
     records INTEGER NOT NULL DEFAULT 0,
     payload TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX runs_by_started ON runs(started_at DESC)`,
  `CREATE INDEX runs_by_machine ON runs(machine_id, started_at DESC)`,

  // ---------------------------------------------------------------- the crossing
  // The one-off import's own ledger: where each table's rows came from and how many.
  `CREATE TABLE imports(
     id TEXT PRIMARY KEY,
     source TEXT NOT NULL,
     table_name TEXT NOT NULL,
     rows INTEGER NOT NULL,
     imported_at TEXT NOT NULL
   ) STRICT`,
];
