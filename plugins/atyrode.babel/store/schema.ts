/*
  THE STORE OF atyrode.babel, spelled once as SQL (ADR 0034: a plugin's tables are SQL, and SQL
  is the schema DSL). One migration per shape change, named and ledgered by the engine; this is
  the first, and it carries every fact the Go stores held that the product still needs — the
  importer (`tools/import.ts`) reads `durable.db` into exactly these tables, ids kept, so
  provenance survives the rewrite (decision 91: import once, then retire).

  What changed in the crossing, and why:
  - ninety-four tables become twenty-three, and the shapes since have added three (`budgets`,
    #260, `run_progress`, #261, and `drains`, #258). The Go tree kept a table per concept per
    package; here a record is a record whatever its kind, an edge is an edge whatever it
    relates, and a revision is a row that supersedes another rather than a parallel table of
    revisions.
  - nothing is sealed and nothing is synced. The hub is the one place (§9 is retired); a row is
    plaintext on the operator's own server, and "published" is a word this schema does not need.
  - every table that records an act is append-only by trigger: a ruling, a vote, a filing, a
    status, a fact are written once and superseded by a later row, never edited or deleted. The
    triggers below are the whole of that guarantee, and a purge is the engine deleting the file.
  - the operator is the boundary: `actor_kind` is `operator`, `run` or `engine` on every row that
    something wrote, so "who did this" is a column and never an inference.
 */

export const STORE_DATA_VERSION = { major: 1, minor: 5 } as const;

/**
 * THE BUDGET OVERLAY (#260), spelled once and created twice: by `SCHEMA_V1` for a store this
 * enable makes, and by `SCHEMA_ADDITIONS` for one an earlier enable already made.
 *
 * A drain is a bounded exception, not a new standing policy: the `policies` row says what the
 * deployment does every day, and on 2026-09-13 the only way to draw more than four reviews at
 * once was to rewrite it five times, which minted a new assignment id for every subject in
 * flight (F5). So a row here names the three numbers a drain needs to move and the instant it
 * stops being true, and nothing else — never a share, never a lease, never a version, because
 * those are what a draw is replayable against. `concurrent_per_machine` is the ONE admission
 * knob: it is what the coordinator bounds a machine by, and the batch it implies follows it
 * (`applyBudget`), so there is no second column an operator could move and see nothing happen.
 *
 * Every number is nullable: an overlay carries what it changes and the standing policy answers
 * for the rest. One that changes nothing is refused by the CHECK rather than stored as a no-op
 * nobody can tell from a mistake. `cleared_at` is written once, from NULL, by an operator
 * ending the overlay early, and `cleared_reason` beside it says why they ended it — which is
 * why this table has no append-only trigger: the row is one bounded exception with one end,
 * and clearing it is that end rather than a new fact about it.
 */
const BUDGETS_TABLE = `CREATE TABLE budgets(
     id TEXT PRIMARY KEY,
     created_at TEXT NOT NULL,
     expires_at TEXT NOT NULL,
     per_cycle_cost REAL,
     daily_cost REAL,
     concurrent_per_machine INTEGER,
     reason TEXT NOT NULL,
     cleared_at TEXT,
     cleared_reason TEXT,
     CHECK (per_cycle_cost IS NOT NULL OR daily_cost IS NOT NULL
            OR concurrent_per_machine IS NOT NULL)
   ) STRICT`;

/**
 * THE DRAIN (#258), spelled once and created twice, for the reason `budgets` is.
 *
 * A drain is one operator decision that outlives every process that carries it out: spend this
 * account's remaining window, on this preset, this many jobs at a time, until this target or
 * this deadline. The controller that does it is rebuilt from nothing on every wake (the server
 * half holds no state between them), so everything it needs to decide "launch another / stop
 * now" is a column here, and the row is the drain.
 *
 * WHY THE FIVE COLUMNS BEYOND THE OBVIOUS ONES EXIST, each because a tick cannot work without
 * it:
 *
 *   - `live` is the job the drain is actually holding — `[{runId, jobId, launchedAt}]`, never
 *     more than `concurrent` of them. It is the membership no other table can answer: a
 *     settled run's `preparation` is overwritten from its receipt (`runStatement`), so a mark
 *     the launch wrote there would not survive the settlement that needs reading, and "the
 *     open explores on this machine" would count a run the operator started by hand. It is
 *     also exactly what a stop has to cancel, by node, rather than guess at.
 *   - `spent` is the SETTLED total — `{calls, inputTokens, outputTokens, costMicros}`, folded
 *     once per job as it closes and never recomputed, because the `run_progress` row a job's
 *     spend was folded through is deleted the moment it settles. What is still in flight is
 *     added at read time; the sum is what a target is judged against.
 *   - `samples` is `[{at, outputTokens, costMicros}]`, one per tick, capped: a RATE needs two
 *     observations and `run_progress` is rewritten in place, so without a sample here "tokens
 *     per minute" could only ever be a total divided by an elapsed time — which never reads
 *     flat, and reading flat for three minutes is the one no-go the runbook names (§11.4).
 *   - `closures` and `refusals` are `{word: count}`, tallied as each job settles for the same
 *     reason `spent` is: the run row that says how a job closed, and the receipt whose reason
 *     names a refused submission, are on a run this drain will have forgotten by the time the
 *     panel asks. A refusal is PAID work with no result (#265), so it is counted where the
 *     receipt is and never inferred later from a failure.
 *
 * `knobs` is the preset's own request — the recipes, the window, the topic, the minutes — kept
 * because a relaunch three settlements later must ask for the SAME thing: a controller that
 * remembered only the preset would quietly widen or narrow the scope between the operator's
 * first job and its ninetieth. `session` sits beside it rather than inside it so the account a
 * drain spends is one column an operator and a panel read without parsing a request (#267).
 *
 * `started_by` is the principal whose act started it, and it is the `authority_id` every job
 * the controller launches afterwards records — a relaunch three settlements later is still
 * that operator's drain, and a tick has no principal of its own to put there.
 *
 * `ending` is the ending a `closing` drain will be recorded under once the last receipt lands.
 * A drain stops launching the instant its target is met, but the jobs it holds were paid for and
 * keep going, so their receipts are still owed to `spent`: the row goes to `closing` with them
 * still in `live`, folds each as it settles, and takes `ending` as its `state` when none is
 * left. Without it a self-stop that could not cancel — the ordinary case, since a tick woken by
 * a settlement holds no `jobs:cancel` — would drop up to (N−1) receipts from its own total.
 *
 * There is no append-only trigger, and `budgets` says why: the row is one bounded operation
 * with one end, `finished_at` and `state` are that end, and the projections (`spent`, `live`,
 * `samples`, `closures`, `refusals`) are the controller's own working record of it rather than
 * acts. There is no overlay column either: a drain's jobs are launched directly and consult no
 * ceiling of the standing policy, so it moves no number and has none to unwind (`doors/drain.ts`).
 */
const DRAINS_TABLE = `CREATE TABLE drains(
     id TEXT PRIMARY KEY,
     machine_id TEXT NOT NULL,
     preset TEXT NOT NULL,
     session TEXT NOT NULL,
     knobs TEXT NOT NULL DEFAULT '{}',
     concurrent INTEGER NOT NULL CHECK (concurrent >= 1),
     target TEXT NOT NULL,
     started_at TEXT NOT NULL,
     started_by TEXT NOT NULL,
     finished_at TEXT,
     state TEXT NOT NULL DEFAULT 'running'
       CHECK (state IN ('running','closing','stopped','target','deadline','failed')),
     ending TEXT NOT NULL DEFAULT ''
       CHECK (ending IN ('','stopped','target','deadline','failed')),
     reason TEXT NOT NULL DEFAULT '',
     spent TEXT NOT NULL DEFAULT '{}',
     live TEXT NOT NULL DEFAULT '[]',
     samples TEXT NOT NULL DEFAULT '[]',
     closures TEXT NOT NULL DEFAULT '{}',
     refusals TEXT NOT NULL DEFAULT '{}',
     jobs_launched INTEGER NOT NULL DEFAULT 0,
     jobs_settled INTEGER NOT NULL DEFAULT 0,
     CHECK ((state IN ('running','closing')) = (finished_at IS NULL)),
     CHECK (state != 'closing' OR ending != '')
   ) STRICT`;

/** Statements of the first migration, in order; each is one `run`. */
export const SCHEMA_V1: readonly string[] = [
  // ---------------------------------------------------------------- the catalog
  // `live` and `kind` are what a SELECTION honours (#262). A session whose log was written in
  // the last two minutes is `live`: its bytes are still moving, so a preparation over it is a
  // scope that changes under the run reading it — which is how every explore of 2026-09-13
  // earned "changed since the preparation was fixed". `kind` separates the operator's own work
  // from Babel's: a transcript one of Babel's runs wrote is `agent`, catalogued and archived
  // like every other session (#177) and skipped by default where a preparation is built, so
  // studying Babel itself is something a preset asks for rather than something it stumbles on.
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
     live INTEGER NOT NULL DEFAULT 0 CHECK (live IN (0, 1)),
     kind TEXT NOT NULL DEFAULT 'operator' CHECK (kind IN ('operator', 'agent')),
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
  //
  // A run that reaches a model is a CODE SESSION (#279), and two columns carry what that means.
  // `container_id` is the Code workspace whose profile answered it — the only handle
  // `code.readSession` takes beside the job id, and therefore the whole of how the conductor
  // reconciles a job it does not own (`job_id` on such a row is Code's, posted under
  // `atyrode.omp`'s operation, which `ctx.jobs` refuses to read). `prepare_job_id` is the
  // `prepare` job whose sealed `material` output that session read, which is what makes a
  // claim's citations checkable against the selection they were served from.
  `CREATE TABLE runs(
     id TEXT PRIMARY KEY,
     kind TEXT NOT NULL,
     machine_id TEXT,
     job_id TEXT,
     container_id TEXT,
     prepare_job_id TEXT,
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

  // ---------------------------------------------------------------- the budget overlay (#260)
  BUDGETS_TABLE,

  // ---------------------------------------------------------------- a run in flight (#261)
  // WHERE A RUNNING JOB IS AND WHAT IT HAS SPENT, folded once a cycle out of the replay ring the
  // hub keeps for it: the newest `job_progress` for the stage and every `inference_call` since
  // the last fold for the spend. One row per run, rewritten in place — this is a PROJECTION of
  // the hub's own record and never an act, so it is the one table here that is neither
  // append-only nor ingested from a machine.
  //
  // It exists because between `started` and a terminal state a job is invisible, and on
  // 2026-09-13 that invisibility cost seventy-five minutes with no engine running and nothing
  // saying so (post-mortem F12, O2). `seq` is the newest sequence folded, so a cycle
  // reads only what it has not seen; `stalled` is `at the model` with no metered call for
  // ninety seconds, and the next call clears it.
  `CREATE TABLE run_progress(
     run_id TEXT PRIMARY KEY,
     job_id TEXT NOT NULL,
     stage TEXT NOT NULL DEFAULT '',
     message TEXT NOT NULL DEFAULT '',
     fraction REAL,
     since TEXT NOT NULL,
     calls INTEGER NOT NULL DEFAULT 0,
     input_tokens INTEGER NOT NULL DEFAULT 0,
     output_tokens INTEGER NOT NULL DEFAULT 0,
     cache_tokens INTEGER NOT NULL DEFAULT 0,
     cost_usd REAL NOT NULL DEFAULT 0,
     last_model TEXT NOT NULL DEFAULT '',
     last_call_at TEXT NOT NULL DEFAULT '',
     seq INTEGER NOT NULL DEFAULT 0,
     stalled INTEGER NOT NULL DEFAULT 0 CHECK (stalled IN (0, 1)),
     updated_at TEXT NOT NULL
   ) STRICT`,

  // ---------------------------------------------------------------- a drain (#258)
  // No index: a deployment accumulates drains at the rate an operator decides to spend a
  // window, the running ones are read by `state` over tens of rows, and an index here would be
  // a statement `SCHEMA_ADDITIONS` cannot carry — an addition is one statement, so a store an
  // earlier enable created would have the table and not the index, and the two creation paths
  // would no longer produce the same shape.
  DRAINS_TABLE,
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

/**
 * ONE ADDITIVE SHAPE, TWICE: in `SCHEMA_V1` above for a store this enable creates, and here for
 * a store an earlier enable already created.
 *
 * A MINOR data version passes both ways and runs no chain (`planDataMigration`), which is the
 * right verdict for a column with a default and for a table nothing older reads — an older
 * build reading this file sees rows it understands, and a newer one sees `0` and `operator`
 * where nothing observed otherwise. So the additions are applied by the enable hook itself, BY
 * NAME and only where the thing is absent, and that is the pattern the next additive shape
 * follows. A MAJOR bump over data that already exists is the other mechanism, and it is the
 * engine's migration ledger, not this list.
 */
export const SCHEMA_ADDITIONS: readonly SchemaAddition[] = [
  {
    table: "sessions",
    column: "live",
    sql: `ALTER TABLE sessions ADD COLUMN live INTEGER NOT NULL DEFAULT 0 CHECK (live IN (0, 1))`,
  },
  {
    table: "sessions",
    column: "kind",
    sql:
      `ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'operator' ` +
      `CHECK (kind IN ('operator', 'agent'))`,
  },
  {
    // A whole TABLE and therefore no column: the hook asks `sqlite_master` for it by name.
    table: "budgets",
    sql: BUDGETS_TABLE,
  },
  // #261: where a running job is and what it has spent. A whole table rather than a column,
  // and additive in exactly the same sense — a build that does not know it never reads it.
  {
    table: "run_progress",
    sql: `CREATE TABLE run_progress(
     run_id TEXT PRIMARY KEY,
     job_id TEXT NOT NULL,
     stage TEXT NOT NULL DEFAULT '',
     message TEXT NOT NULL DEFAULT '',
     fraction REAL,
     since TEXT NOT NULL,
     calls INTEGER NOT NULL DEFAULT 0,
     input_tokens INTEGER NOT NULL DEFAULT 0,
     output_tokens INTEGER NOT NULL DEFAULT 0,
     cache_tokens INTEGER NOT NULL DEFAULT 0,
     cost_usd REAL NOT NULL DEFAULT 0,
     last_model TEXT NOT NULL DEFAULT '',
     last_call_at TEXT NOT NULL DEFAULT '',
     seq INTEGER NOT NULL DEFAULT 0,
     stalled INTEGER NOT NULL DEFAULT 0 CHECK (stalled IN (0, 1)),
     updated_at TEXT NOT NULL
   ) STRICT`,
  },
  // #258: a drain. A whole table again, and the same additive sense: a build that does not know
  // it never reads it, and a store this enable creates gets it from `SCHEMA_V1` instead.
  {
    table: "drains",
    sql: DRAINS_TABLE,
  },
  // #279: a run that reaches a model is a Code session. Two nullable columns with no default,
  // which is additive in the strictest sense — every row an earlier shape wrote reads as NULL,
  // and NULL is the truth about it: those runs were posted by a launcher of Babel's own and
  // belong to no Code container.
  {
    table: "runs",
    column: "container_id",
    sql: `ALTER TABLE runs ADD COLUMN container_id TEXT`,
  },
  {
    table: "runs",
    column: "prepare_job_id",
    sql: `ALTER TABLE runs ADD COLUMN prepare_job_id TEXT`,
  },
];

/**
 * One addition a later shape made to the store the first migration created: a column on one of
 * its tables, or — with no `column` — a table of its own.
 */
export interface SchemaAddition {
  readonly table: string;
  readonly column?: string | undefined;
  readonly sql: string;
}
