/*
  THE STORE OF atyrode.babel, spelled once as SQL (ADR 0034: a plugin's tables are SQL, and SQL
  is the schema DSL). One migration per shape change, named and ledgered by the engine; this is
  the first, and it carries every fact the Go stores held that the product still needs — the
  importer (`tools/import.ts`) reads `durable.db` into exactly these tables, ids kept, so
  provenance survives the rewrite; the crossing runs once and the Go stores are then retired.

  What changed in the crossing, and why:
  - ninety-four tables become twenty-three, and the shapes since have added seven (`budgets`,
    #260, `run_progress`, #261, `drains`, #258, `next_actions` with `next_action_rulings`,
    #340, `run_calls`, #349, `session_titles`, #342, and the corpus index of `record_terms`
    and `record_vectors`, #337). The Go tree kept a table per concept per
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

export const STORE_DATA_VERSION = { major: 1, minor: 10 } as const;

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
     profile TEXT NOT NULL DEFAULT '{}',
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

/**
 * WHAT A RUN PROPOSES BE DONE NEXT, AND WHAT THE OPERATOR ANSWERED (#340) — spelled once and
 * created twice, for the reason `budgets` and `drains` are.
 *
 * A NEXT ACTION IS NOT A RULING, and these are two tables rather than one because that
 * distinction is the product. `dispositions` is a verdict on the RECORD — is this claim any
 * good — and is the operator's alone. A row in `next_actions` is a proposed action OUTSIDE
 * Babel — draft an issue, propose a fact, store a memory, ask a question, explore it further —
 * which a run may raise because raising it authorizes nothing. Folding the two together would
 * make "accepted" mean two things in one corpus, exactly where an acceptance rate is supposed
 * to be evidence about the output's quality.
 *
 * WHY A TABLE AND NOT A WIDER `plans.kind`. `plans` is the right SHAPE — subject, operation,
 * payload, state, ruled_by/at/reason — and the importer used to ask for `'action'` in its
 * CHECK. It cannot be given one. SQLite has no statement that widens a CHECK; the documented
 * route is to build a new table, copy the rows, drop and rename. `SCHEMA_ADDITIONS` carries
 * ONE statement per addition, applied only where the object it names is absent, so a store an
 * earlier enable created would keep the narrow CHECK forever while a fresh one got the wide
 * one — the two creation paths would stop producing the same shape, which is the failure the
 * drains index was left out to avoid. A NEW TABLE IS THE ONLY ADDITIVE MOVE THE ENABLE HOOK
 * CAN MAKE, and it is the move #258 and #261 made. Anyone reaching for a CHECK here should
 * reach for a table instead.
 *
 * Two more things follow from `plans` being the wrong home even where it fits. A plan is ruled
 * by UPDATE in place and this ledger appends, so a reconsideration stays readable. And
 * accepting a plan APPLIES something to the store — an entity created, records filed — whereas
 * accepting a next action applies nothing anywhere: §4.6 puts publishing and writing to a
 * source repository outside Babel, so an acceptance is a durable record that a person accepted
 * it and nothing else.
 *
 * `next_actions.kind` IS A CLOSED VOCABULARY where `edges.kind` is an open one, and the
 * difference is who acts on the word. A relation is read; a next action is OFFERED as a choice,
 * so a sixth kind nothing renders is a proposal that silently never appears.
 *
 * THE OPERATOR'S ANSWER IS A SECOND TABLE, not four columns on the first, because a run writes
 * the first and nothing but a door writes the second. `contract.ts`'s `INGESTIBLE_TABLES` is
 * the closed set a run's output may reach, `next_action_rulings` is not in it, and that is a
 * compile error rather than a rule somebody remembers. There is no actor KIND column here
 * either — `operator_id` is a person, on the same terms `dispositions` carries one — so no row
 * can spell a run as the answerer. Reconsidering appends a higher `seq` and the standing is the
 * newest entry, derived at read time so a stored status can never disagree with its ledger.
 */
const NEXT_ACTION_SCHEMA: readonly string[] = [
  `CREATE TABLE next_actions(
     id TEXT PRIMARY KEY,
     record_id TEXT NOT NULL REFERENCES records(id),
     kind TEXT NOT NULL CHECK (kind IN ('draft-issue','propose-reality-fact','store-memory',
                                       'ask-question','develop-further')),
     proposed_by_kind TEXT NOT NULL CHECK (proposed_by_kind IN ('run','operator','engine')),
     proposed_by_id TEXT NOT NULL,
     summary TEXT NOT NULL,
     created_at TEXT NOT NULL,
     payload TEXT NOT NULL
   ) STRICT`,
  `CREATE INDEX next_actions_by_record ON next_actions(record_id, created_at)`,
  `CREATE TRIGGER next_actions_immutable BEFORE UPDATE ON next_actions BEGIN
     SELECT RAISE(ABORT, 'a proposed action is never edited; decline it and propose the corrected one');
   END`,
  `CREATE TRIGGER next_actions_kept BEFORE DELETE ON next_actions BEGIN
     SELECT RAISE(ABORT, 'a proposed action is never deleted; declining one leaves it readable');
   END`,
  `CREATE TABLE next_action_rulings(
     id TEXT PRIMARY KEY,
     next_action_id TEXT NOT NULL REFERENCES next_actions(id),
     seq INTEGER NOT NULL,
     decision TEXT NOT NULL CHECK (decision IN ('accepted','declined')),
     operator_id TEXT NOT NULL,
     note TEXT NOT NULL DEFAULT '',
     recorded_at TEXT NOT NULL,
     UNIQUE (next_action_id, seq)
   ) STRICT`,
  `CREATE TRIGGER next_action_rulings_immutable BEFORE UPDATE ON next_action_rulings BEGIN
     SELECT RAISE(ABORT, 'a decision is never edited; reconsidering appends another');
   END`,
  `CREATE TRIGGER next_action_rulings_kept BEFORE DELETE ON next_action_rulings BEGIN
     SELECT RAISE(ABORT, 'a decision is never deleted; it is the provenance an acceptance rate reads');
   END`,
];

/**
 * WHAT A RUN'S CALLS WERE, SO A JUDGEMENT CAN BE RECHECKED RATHER THAN RE-RUN (#349) — spelled
 * once and created twice, for the reason `budgets`, `drains` and `next_actions` are.
 *
 * `runs.payload` is the receipt, and a receipt is SPEND ACCOUNTING: the model, the tokens, the
 * cost, the closure, in one total per run. A claim about how Babel judged something cannot be
 * checked against that; it can only be re-run, and a re-run is not the same event — the Jev
 * study measured repeated identical requests answering byte-identically 1 time in 16
 * (`docs/jev-case-study-audit.md`).
 *
 * WHAT THE HUB GIVES AND WHAT IT DOES NOT, because the columns below are shaped by the answer.
 * The hub's own per-call frame is METERING and says so in its protocol — "the model, the
 * tokens, the price applied, never a prompt or a byte of the answer" — and Babel is served none
 * of it regardless: no operation this bundle declares binds a model service (`server/plan.ts`),
 * and the run that reaches a model is a Code session whose job belongs to `atyrode.omp`, which
 * `ctx.jobs` may neither follow nor journal. What a settlement is handed is omp's receipt
 * through `atyrode.code.readSession`: a session id, the transcript's PATH, the model, the
 * agent's last message, ONE usage total summed over every turn, and an exit code.
 *
 * SO THE BYTES ARE NOT HERE AND ARE NOT MEANT TO BE. They are in the session's own transcript,
 * one JSONL record per message — the request it was posted with among them — on the machine
 * that ran it. `transcript_host`, `transcript_session` and `transcript_path` are the LOCATOR of
 * that log, on the discipline the secret preflight established (#339): the locator travels, the
 * bytes stay where they were, and resolving one needs the machine holding the session
 * (`machine/adapters/omp.ts` is what names the log, `machine/prepare.ts` what numbers its
 * records). A row here therefore carries no prompt, no answer, and nothing a credential could
 * be hiding inside.
 *
 * `response_digest` IS WHAT MAKES TWO RUNS COMPARABLE WITHOUT COPYING EITHER: sha256 of the
 * final message exactly as the receipt carried it. Two runs that answered identically share one
 * digest and two that did not are told apart without reading either, which is the whole of what
 * the 1-in-16 measurement needs and the reason for the index. Empty means no transcript was
 * sealed, which is a different fact from an answer that was empty.
 *
 * ONE ROW PER CALL, AND A RUN MAKES ONE TODAY. `runSession` is omp's one-shot and its per-turn
 * calls are summed before Babel sees them, so `seq` is 1 on every row this build writes. It is
 * a column rather than an assumption because `(run_id, seq)` is also the idempotency key: a
 * settlement replayed after a crash offers the same pair and `OR IGNORE` makes it a no-op.
 *
 * Append-only by trigger, like every other table here that records something that happened: a
 * corrected account of a call would be the only copy of the thing it corrects.
 */
const RUN_CALL_SCHEMA: readonly string[] = [
  `CREATE TABLE run_calls(
     run_id TEXT NOT NULL REFERENCES runs(id),
     seq INTEGER NOT NULL CHECK (seq >= 1),
     recorded_at TEXT NOT NULL,
     model TEXT NOT NULL DEFAULT '',
     input_tokens INTEGER NOT NULL DEFAULT 0,
     output_tokens INTEGER NOT NULL DEFAULT 0,
     cache_read_tokens INTEGER NOT NULL DEFAULT 0,
     cache_write_tokens INTEGER NOT NULL DEFAULT 0,
     cost_micros INTEGER NOT NULL DEFAULT 0,
     exit_code INTEGER,
     closure TEXT NOT NULL CHECK (closure IN ('completed','failed','stopped','skipped')),
     refusal TEXT NOT NULL DEFAULT '',
     response_digest TEXT NOT NULL DEFAULT '',
     response_bytes INTEGER NOT NULL DEFAULT 0,
     transcript_host TEXT NOT NULL DEFAULT '',
     transcript_session TEXT NOT NULL DEFAULT '',
     transcript_path TEXT NOT NULL DEFAULT '',
     PRIMARY KEY (run_id, seq)
   ) STRICT`,
  `CREATE INDEX run_calls_by_answer ON run_calls(response_digest)`,
  `CREATE TRIGGER run_calls_immutable BEFORE UPDATE ON run_calls BEGIN
     SELECT RAISE(ABORT, 'a call is never edited; it is an account of something that happened');
   END`,
  `CREATE TRIGGER run_calls_kept BEFORE DELETE ON run_calls BEGIN
     SELECT RAISE(ABORT, 'a call is never deleted; it is what a judgement is rechecked against');
   END`,
];

/**
 * THE DURABLE STORE OF MODEL-INFERRED SESSION TITLES (#342) — spelled once and created twice,
 * for the reason `budgets`, `drains`, `next_actions` and `run_calls` are.
 *
 * An inferred title is the ONE piece of session metadata Babel cannot recompute. Every other
 * column of `sessions` is derivable from the live log — a recorded title is read out of the
 * harness's own files on every scan and a derived one is recomputed from them for free — so
 * losing one of those is a rescan. Losing a title a model wrote is a second bill.
 *
 * THE ROW EXISTS WHETHER OR NOT A TITLE CAME BACK, and that is what makes the work happen
 * ONCE. `title` is the model's text; an empty `title` with a `reason` is the session the run
 * declined, or whose run never reached a model at all. Either way the selector is answered and
 * no later cycle offers it again — an automatic lane whose failures were not recorded would
 * re-offer the same batch on every wake for ever, which is the unbounded spend this whole
 * feature is fenced against. Retrying is an operator act: delete the row.
 *
 * IT IS NOT A CACHE OF TITLES IN GENERAL. Only the value with no other source is kept, because
 * a copy of a recorded title here would be a second authority that could disagree with the
 * session's own log. `sessions.title` carries the value a reader sees and
 * `sessions.title_provenance` says `inferred` while it is this one; a later scan that finds a
 * real title overwrites both, and this row stays as the account of what was paid for.
 *
 * `run_id` is the attribution: which run — and through it which profile, model and cost —
 * produced the text. It is a column rather than a join through the selector because a title is
 * re-judged by knowing what wrote it, and `runs` is where that is already written out.
 */
const SESSION_TITLE_SCHEMA: readonly string[] = [
  `CREATE TABLE session_titles(
     selector TEXT PRIMARY KEY,
     title TEXT NOT NULL,
     reason TEXT NOT NULL DEFAULT '',
     run_id TEXT NOT NULL,
     inferred_at TEXT NOT NULL
   ) STRICT`,
];

/**
 * THE CORPUS INDEX (#337) — spelled once and created twice, for the reason `budgets`, `drains`,
 * `next_actions`, `run_calls` and `session_titles` are.
 *
 * Nothing retrieved over the corpus before this. A preparation selected by recency or by topic,
 * the feed filtered structured columns, and the measurement of what that costs is in
 * `docs/jev-case-study-audit.md`: a correctly matched record scores 2.043 on real work, the
 * mechanism that picked one scored 0.606, and picking at random scored 0.680. Retrieval that
 * loses to a coin is not retrieval, and the reason is that no index existed for one to read.
 *
 * TWO INDEXES, NOT ONE, and they are here together because the features waiting on this —
 * duplicate detection, a run citing prior work it did not know about, a question finding the
 * records it is about — are the ones where MEANING is the requirement. A keyword index alone
 * would have shipped a door that worked over a capability that did not.
 *
 * BOTH ARE DERIVED STATE AND NEITHER IS BACKED UP. Every row below is reconstructible from
 * `records` alone on a machine that has never seen this file (`store/corpus.ts`, `rebuildTerms`
 * and the pending query), which is why nothing here is append-only: a re-embed REPLACES, and a
 * corrected index is not a correction of a fact but a recomputation of a derivation.
 *
 * `record_terms` IS FTS5, which the runtime already carries — `ENABLE_FTS5` is in the SQLite Bun
 * ships and `CREATE` is not among the leading keywords the engine refuses. It costs no
 * dependency, no model and no egress, and it is what makes a deployment that installs nothing
 * else searchable. Its rows are written by the trigger below as records arrive and by one
 * rebuild pass for the ones that arrived before this shape existed, so the keyword half is
 * never partial for long and never waits on anybody's account.
 *
 * `record_vectors` IS ONE ROW PER RECORD, KEYED ON THE RECORD, and the model that produced it is
 * a column rather than a note somewhere: a vector whose model nobody recorded is a vector nobody
 * can tell is stale, and two models' vectors compared against each other produce confident
 * nonsense that nothing downstream would notice. A search reads only the rows matching the model
 * it embedded its own query with; the rest are pending work, which is exactly what a model change
 * should mean.
 *
 * `probe` IS A SIGN BIT PER DIMENSION AND THE HOST'S BUDGET CHOSE IT. A plugin's SQL call may
 * return 4 MiB (`MAX_SQL_RESULT_BYTES`), and 6,038 records at 768 float32 dimensions is 18.5 MB —
 * so a brute-force scan over `vector` cannot cross this boundary at corpus scale, and
 * `load_extension` is refused, so there is no vector extension to do it below the boundary
 * either. One bit per dimension is 96 bytes a record and 580 KB for the whole corpus: it crosses
 * in one query, Hamming distance over it orders candidates, and the float vectors of a bounded
 * top slice are read back to score them exactly. The sketch is LOSSY and `store/corpus.ts` says
 * what that costs and reports when an answer was drawn from it.
 *
 * A RECORD WITH NO TEXT GETS A ROW ANYWAY, `dims` 0 and a `reason`, on `session_titles`' rule:
 * work that cannot succeed must be recorded as done or every later pass offers it again for
 * ever. Only a permanent, local fact is written that way — an empty text — never a service that
 * was unavailable, which is a fact about the minute rather than about the record.
 */
const CORPUS_INDEX_SCHEMA: readonly string[] = [
  `CREATE VIRTUAL TABLE record_terms USING fts5(
     record_id UNINDEXED,
     title,
     body,
     tokenize = 'unicode61 remove_diacritics 2'
   )`,
  `CREATE TRIGGER record_terms_follow AFTER INSERT ON records BEGIN
     INSERT INTO record_terms(record_id, title, body)
     VALUES (new.id, new.title, ${recordTextSql("new.")});
   END`,
  `CREATE TABLE record_vectors(
     record_id TEXT PRIMARY KEY REFERENCES records(id),
     model TEXT NOT NULL,
     dims INTEGER NOT NULL CHECK (dims >= 0),
     probe BLOB NOT NULL,
     vector BLOB NOT NULL,
     digest TEXT NOT NULL,
     reason TEXT NOT NULL DEFAULT '',
     embedded_at TEXT NOT NULL,
     CHECK ((dims = 0) = (reason <> ''))
   ) STRICT`,
  `CREATE INDEX record_vectors_by_model ON record_vectors(model, dims)`,
];

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
  //
  // `kind` IS AN OPEN VOCABULARY — no CHECK — and deliberately so: a relation is a word this
  // family agrees on, not a column, and the crossing carried in whatever the Go tree had
  // spelled. What a writer may spell today is `cites`, `consolidates`, `addresses`,
  // `contradicts` (an evidence-free challenger objection, and a record whose own text opens
  // CONTRADICTS), `corrects` (a record whose own text opens CORRECTION or CORRECTS),
  // `supersedes`, `refines` and `about`. A new word costs nothing at the table and everything
  // at the reader, so it is added here in prose before it is written anywhere.
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

  ...NEXT_ACTION_SCHEMA,

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
     /* Consecutive cycles the hub could not say where this run's job is; see SCHEMA_ADDITIONS. */
     unreadable INTEGER NOT NULL DEFAULT 0,
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
     /* The distinct models that have answered, JSON, first-heard order; see SCHEMA_ADDITIONS. */
     models TEXT NOT NULL DEFAULT '',
     stalled INTEGER NOT NULL DEFAULT 0 CHECK (stalled IN (0, 1)),
     updated_at TEXT NOT NULL
   ) STRICT`,

  // ---------------------------------------------------------------- a run's own traffic (#349)
  ...RUN_CALL_SCHEMA,

  // ---------------------------------------------------------------- an inferred title (#342)
  ...SESSION_TITLE_SCHEMA,

  // ---------------------------------------------------------------- the corpus index (#337)
  // After `records`, because the trigger names that table, and before the crossing, because the
  // importer's own inserts are what the trigger first fires for.
  ...CORPUS_INDEX_SCHEMA,

  // ---------------------------------------------------------------- a drain (#258)
  // No index, and now for one reason rather than two: a deployment accumulates drains at the
  // rate an operator decides to spend a window, and the running ones are read by `state` over
  // tens of rows. The second reason — that an addition could only name a table, so a store an
  // earlier enable created would have had the table and not the index — no longer holds:
  // `SCHEMA_ADDITIONS` names any schema object (#340).
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
 *
 * AN ADDITION NAMES ANY SCHEMA OBJECT, not only a table: SQLite gives every table, index and
 * trigger in one database a name of its own, so `sqlite_master` answers for all three by name
 * alone. That is why #340's two tables can arrive with their append-only triggers and their
 * index — the #258 note above records that a drain's index had to be left out when the hook
 * could only ask about tables, and a store that reached this shape by addition would otherwise
 * have the tables and none of the triggers, which is an append-only ledger that appends by
 * convention.
 */
export const SCHEMA_ADDITIONS: readonly SchemaAddition[] = [
  {
    object: "sessions",
    column: "live",
    sql: `ALTER TABLE sessions ADD COLUMN live INTEGER NOT NULL DEFAULT 0 CHECK (live IN (0, 1))`,
  },
  {
    object: "sessions",
    column: "kind",
    sql:
      `ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'operator' ` +
      `CHECK (kind IN ('operator', 'agent'))`,
  },
  {
    // A whole TABLE and therefore no column: the hook asks `sqlite_master` for it by name.
    object: "budgets",
    sql: BUDGETS_TABLE,
  },
  // #261: where a running job is and what it has spent. A whole table rather than a column,
  // and additive in exactly the same sense — a build that does not know it never reads it.
  {
    object: "run_progress",
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
     models TEXT NOT NULL DEFAULT '',
     stalled INTEGER NOT NULL DEFAULT 0 CHECK (stalled IN (0, 1)),
     updated_at TEXT NOT NULL
   ) STRICT`,
  },
  // #258: a drain. A whole table again, and the same additive sense: a build that does not know
  // it never reads it, and a store this enable creates gets it from `SCHEMA_V1` instead.
  {
    object: "drains",
    sql: DRAINS_TABLE,
  },
  // #279: a run that reaches a model is a Code session. Two nullable columns with no default,
  // which is additive in the strictest sense — every row an earlier shape wrote reads as NULL,
  // and NULL is the truth about it: those runs were posted by a launcher of Babel's own and
  // belong to no Code container.
  {
    object: "runs",
    column: "container_id",
    sql: `ALTER TABLE runs ADD COLUMN container_id TEXT`,
  },
  {
    object: "runs",
    column: "prepare_job_id",
    sql: `ALTER TABLE runs ADD COLUMN prepare_job_id TEXT`,
  },
  // #279: HOW MANY CYCLES IN A ROW NOBODY COULD SAY WHERE THIS RUN'S JOB IS.
  //
  // It was a `Map` in the conductor's closure, and the closure is the bug: `server.ts` builds a
  // NEW conductor for every wake, so "twice running" was counted in an object that never
  // survived to be read a second time and the reaper's bound could not fire. A counter whose
  // whole predicate is "the cycle before this one" has to be durable, so it is a column on the
  // row it is about; a run whose job answers resets it to zero.
  {
    object: "runs",
    column: "unreadable",
    sql: `ALTER TABLE runs ADD COLUMN unreadable INTEGER NOT NULL DEFAULT 0`,
  },
  // #279: a drain names a CODE PROFILE, not a model and an account of Babel's own. The column
  // it replaces held a `SessionChoice` the operator typed; this holds the profile plus the
  // ledger entry of what Code said that profile would run as, copied once at the start. A row
  // an earlier shape wrote reads `{}` and cannot be relaunched, which is the truth about it:
  // nobody can say which Code profile a drain that named none was spending.
  {
    object: "drains",
    column: "profile",
    sql: `ALTER TABLE drains ADD COLUMN profile TEXT NOT NULL DEFAULT '{}'`,
  },
  // #340: a run's proposed next actions and the operator's ledger over them, derived from the
  // one place they are spelled so the two creation paths cannot come to disagree.
  ...NEXT_ACTION_SCHEMA.map(objectAddition),
  // #349: one row per call of a run, its locator, and no byte of its traffic — derived from the
  // same list `SCHEMA_V1` spreads, for the reason above.
  ...RUN_CALL_SCHEMA.map(objectAddition),
  // #342: the titles a model wrote, which nothing else in this store could recover. Derived
  // from the same list, for the reason above.
  ...SESSION_TITLE_SCHEMA.map(objectAddition),
  // #337: the corpus index, keyword and meaning. Derived from the same list, for the reason
  // above — and the one addition in this file that names a VIRTUAL table, which `sqlite_master`
  // answers for by name exactly as it does for an ordinary one.
  ...CORPUS_INDEX_SCHEMA.map(objectAddition),
  // #169: the models that have answered a running job, JSON, in the order it first heard from
  // each. A column and not a table, because the table above already arrives by addition for a
  // store created before #261 — and an addition keyed only on the table's name would have left
  // such a store with the table and without this column, which is the one shape the fold's
  // INSERT cannot write. A row an earlier shape wrote reads `''`, which is the truth about it:
  // that fold recorded only the newest model, so which others answered is not recoverable.
  {
    object: "run_progress",
    column: "models",
    sql: `ALTER TABLE run_progress ADD COLUMN models TEXT NOT NULL DEFAULT ''`,
  },
];

/**
 * One addition a later shape made to the store the first migration created: a column on one of
 * its tables, or — with no `column` — a schema object of its own, named as `sqlite_master`
 * names it.
 */
export interface SchemaAddition {
  readonly object: string;
  readonly column?: string | undefined;
  readonly sql: string;
}

/**
 * One whole-object `CREATE` statement as an addition, keyed on the name it creates.
 *
 * Deriving the name from the statement is what makes a shape spelled once creatable twice: a
 * name written out beside the SQL is a second copy that a rename would leave behind, and the
 * addition would then run on every enable and fail on the second.
 */
function objectAddition(sql: string): SchemaAddition {
  const named = /^\s*CREATE\s+(?:VIRTUAL\s+)?(?:TABLE|INDEX|TRIGGER)\s+([a-z_][a-z_0-9]*)/iu.exec(
    sql,
  );
  const object = named?.[1];
  if (object === undefined) throw new Error(`a schema addition creates nothing named: ${sql}`);
  return { object, sql };
}

/**
 * A RECORD'S OWN TEXT, AS SQL, SPELLED ONCE — the trigger above and the rebuild pass in
 * `store/corpus.ts` both read it through this, so "what a record says" cannot come to mean two
 * things depending on which path indexed the row.
 *
 * It is the prose the peel puts at depths one and two (`store/store.ts`, `claimOf` and `caseOf`)
 * and nothing else: no id, no run, no locator, no timestamp. That matters twice over — an
 * identifier in a keyword index is noise a reader never searches for, and this same text is the
 * ONLY thing an embedding service is ever sent, so the discipline is enforced by what the
 * expression selects rather than by a caller remembering to strip something.
 *
 * The four kinds name their claim differently and a payload holds only its own kind's fields, so
 * the missing ones coalesce away; the list is flat rather than a `CASE` over `kind` because a
 * trigger that had to branch would need the branch repeated in the rebuild. List-valued fields
 * (`scope`, `risks`, `open_questions`) are left out: `json_extract` hands those back as JSON
 * text, and bracket-and-quote noise in an embedding's input buys less than it costs.
 *
 * `json_valid` guards the whole of it because an imported Go-era payload was never validated by
 * this contract, and `json_extract` RAISES on text that is not JSON — inside an `AFTER INSERT`
 * trigger that would abort the insert of the record itself. An index may be empty about a
 * record; it may never refuse one.
 */
export function recordTextSql(alias: string): string {
  const fields = [
    "statement",
    "claim",
    "pattern",
    "outcome",
    "problem",
    "impact",
    "significance",
    "category",
    "classification",
    "uncertainty",
    "estimated_scope",
  ];
  const parts = fields
    .map((field) => `COALESCE(json_extract(${alias}payload, '$.${field}'), '')`)
    .join(` || ' ' || `);
  return `CASE WHEN json_valid(${alias}payload) THEN TRIM(${parts}) ELSE '' END`;
}

/**
 * WHETHER A ROW'S IDENTIFIER IS ONE A CALLER COULD NAME, AS SQL (#426).
 *
 * `contract.ts`'s `isRecordId` is the same question in TypeScript and the two must answer alike;
 * they are spelled twice because a regular expression is not available to SQLite and a read that
 * filtered in TypeScript could not keep a COUNT and a LIMIT'd page agreeing about how many rows
 * there are. `store/acts.test.ts` holds the two to each other over a spread of identifiers.
 *
 * The family is the first four characters, the tail is 8 to 64 lowercase hex — so the whole id
 * is 12 to 68 characters — and `GLOB` is the case-sensitive match SQLite has: `LIKE` would admit
 * `FND_0000ABCD`, which `RecordIdSchema` refuses.
 */
export function nameableRecordSql(column: string): string {
  return `substr(${column}, 1, 4) IN ('hyp_', 'obs_', 'fnd_', 'pro_', 'qst_')
          AND length(${column}) BETWEEN 12 AND 68
          AND substr(${column}, 5) NOT GLOB '*[^0-9a-f]*'`;
}
