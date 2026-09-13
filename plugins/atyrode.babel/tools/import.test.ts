/*
  THE CROSSING, TESTED AGAINST THE GO SCHEMA IT READS.

  The fixture below is the Go tree's own DDL — the tables `internal/frontier/store.go`,
  `internal/evaluation/store.go`, `internal/reality/store.go`, `internal/disposition` and
  `internal/run` migrate into `durable.db`, plus `internal/catalog`'s local `sessions` — copied
  verbatim so that a column the Go stores drop or rename breaks this test rather than the
  operator's one-off import. Nothing here mocks the target either: the rows go through the real
  `openPluginDatabase` from ADR 0034, with its foreign keys on and its append-only triggers armed,
  into the real `SCHEMA_V1`.
*/

import { Database } from "bun:sqlite";
import { afterAll, beforeAll, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabaseAdmin } from "@manifold/plugin";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID } from "../contract.ts";
import { applyPlan, ensureSchema, planImport, resolveTarget, summarize, type TablePlan } from "./import.ts";

// ---------------------------------------------------------------------------- the Go schema

/** `durable.db` as the Go migrations leave it, restricted to the tables the importer reads. */
const GO_DURABLE_SCHEMA: readonly string[] = [
  `CREATE TABLE run_preparation(
  id             TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  prepared_at    TEXT NOT NULL,
  source_count   INTEGER NOT NULL,
  sync_state     TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed')),
  payload        BLOB NOT NULL
)`,
  `CREATE TABLE frontier_revision(
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
)`,
  `CREATE TABLE frontier_hypothesis(
  id             TEXT PRIMARY KEY,
  ancestor_id    TEXT REFERENCES frontier_hypothesis(id),
  run_id         TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE frontier_observation(
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
)`,
  `CREATE TABLE frontier_finding(
  id             TEXT PRIMARY KEY,
  ancestor_id    TEXT REFERENCES frontier_finding(id),
  run_id         TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE frontier_proposal(
  id             TEXT PRIMARY KEY,
  ancestor_id    TEXT REFERENCES frontier_proposal(id),
  run_id         TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE reference_edge(
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
)`,
  `CREATE TABLE frontier_hypothesis_link(
  id           TEXT PRIMARY KEY,
  from_id      TEXT NOT NULL REFERENCES frontier_hypothesis(id),
  to_id        TEXT NOT NULL REFERENCES frontier_hypothesis(id),
  link_type    TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  UNIQUE(from_id, to_id, link_type)
)`,
  `CREATE TABLE frontier_finding_observation(
  finding_id     TEXT NOT NULL REFERENCES frontier_finding(id),
  observation_id TEXT NOT NULL REFERENCES frontier_observation(id),
  position       INTEGER NOT NULL,
  PRIMARY KEY(finding_id, position)
)`,
  `CREATE TABLE frontier_proposal_finding(
  proposal_id TEXT NOT NULL REFERENCES frontier_proposal(id),
  finding_id  TEXT NOT NULL REFERENCES frontier_finding(id),
  position    INTEGER NOT NULL,
  PRIMARY KEY(proposal_id, position)
)`,
  `CREATE TABLE frontier_proposal_hypothesis(
  proposal_id   TEXT NOT NULL REFERENCES frontier_proposal(id),
  hypothesis_id TEXT NOT NULL REFERENCES frontier_hypothesis(id),
  position      INTEGER NOT NULL,
  PRIMARY KEY(proposal_id, position)
)`,
  `CREATE TABLE frontier_status_event(
  id            TEXT PRIMARY KEY,
  hypothesis_id TEXT NOT NULL REFERENCES frontier_hypothesis(id),
  seq           INTEGER NOT NULL,
  status        TEXT NOT NULL,
  run_id        TEXT NOT NULL,
  recorded_at   TEXT NOT NULL,
  payload_json  TEXT NOT NULL, actor_kind TEXT NOT NULL DEFAULT '', actor_id TEXT NOT NULL DEFAULT '',
  UNIQUE(hypothesis_id, seq)
)`,
  `CREATE TABLE frontier_disposition(
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
)`,
  `CREATE TABLE frontier_filing(
  id             TEXT PRIMARY KEY,
  record_kind    TEXT NOT NULL,
  record_id      TEXT NOT NULL,
  entity_id      TEXT NOT NULL,
  author         TEXT NOT NULL,
  author_id      TEXT NOT NULL,
  heuristic      INTEGER NOT NULL,
  withdrawn      INTEGER NOT NULL,
  supersedes_id  TEXT REFERENCES frontier_filing(id),
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE evaluation_claim(
  id              TEXT PRIMARY KEY,
  subject_kind    TEXT NOT NULL,
  subject_id      TEXT NOT NULL,
  run_id          TEXT NOT NULL,
  role            TEXT NOT NULL,
  policy_version  TEXT NOT NULL,
  context_version TEXT NOT NULL,
  seed            TEXT NOT NULL,
  input_digest    TEXT NOT NULL,
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
)`,
  `CREATE TABLE evaluation_record(
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
)`,
  `CREATE TABLE reality_entity(
  id             TEXT PRIMARY KEY,
  kind           TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE reality_entity_membership(
  entity_id     TEXT NOT NULL REFERENCES reality_entity(id),
  seq           INTEGER NOT NULL,
  role          TEXT NOT NULL,
  canonical_id  TEXT NOT NULL REFERENCES reality_entity(id),
  resolution_id TEXT,
  recorded_at   TEXT NOT NULL,
  PRIMARY KEY(entity_id, seq)
)`,
  `CREATE TABLE reality_entity_alias(
  id             TEXT PRIMARY KEY,
  entity_id      TEXT NOT NULL REFERENCES reality_entity(id),
  alias_kind     TEXT NOT NULL,
  value_key      TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  payload_json   TEXT NOT NULL,
  UNIQUE(alias_kind, value_key, entity_id)
)`,
  `CREATE TABLE reality_alias_event(
  id           TEXT PRIMARY KEY,
  alias_id     TEXT NOT NULL REFERENCES reality_entity_alias(id),
  seq          INTEGER NOT NULL,
  state        TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  UNIQUE(alias_id, seq)
)`,
  `CREATE TABLE reality_fact(
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
  source_id      TEXT,
  import_id      TEXT,
  payload_json   TEXT NOT NULL
)`,
  `CREATE TABLE reality_fact_status(
  id           TEXT PRIMARY KEY,
  fact_id      TEXT NOT NULL REFERENCES reality_fact(id),
  seq          INTEGER NOT NULL,
  status       TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  UNIQUE(fact_id, seq)
)`,
  `CREATE TABLE reality_resolution(
  id              TEXT PRIMARY KEY,
  resolution_kind TEXT NOT NULL,
  reverses_id     TEXT UNIQUE REFERENCES reality_resolution(id),
  actor           TEXT NOT NULL,
  recorded_at     TEXT NOT NULL,
  payload_json    TEXT NOT NULL
)`,
  `CREATE TABLE reality_resolution_member(
  resolution_id TEXT NOT NULL REFERENCES reality_resolution(id),
  member_role   TEXT NOT NULL CHECK(member_role IN ('source', 'result')),
  position      INTEGER NOT NULL,
  entity_id     TEXT NOT NULL REFERENCES reality_entity(id),
  PRIMARY KEY(resolution_id, member_role, position)
)`,
  `CREATE TABLE reality_question(
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
)`,
  `CREATE TABLE reality_question_work(
  question_id TEXT NOT NULL REFERENCES reality_question(id),
  work_kind   TEXT NOT NULL,
  work_id     TEXT NOT NULL,
  blocking    INTEGER NOT NULL,
  PRIMARY KEY(question_id, work_kind, work_id)
)`,
  `CREATE TABLE reality_question_entity(
  question_id TEXT NOT NULL REFERENCES reality_question(id),
  entity_id   TEXT NOT NULL REFERENCES reality_entity(id),
  PRIMARY KEY(question_id, entity_id)
)`,
  `CREATE TABLE reality_question_evidence(
  question_id TEXT NOT NULL REFERENCES reality_question(id),
  item        TEXT NOT NULL,
  PRIMARY KEY(question_id, item)
)`,
  `CREATE TABLE reality_question_event(
  id           TEXT PRIMARY KEY,
  question_id  TEXT NOT NULL REFERENCES reality_question(id),
  seq          INTEGER NOT NULL,
  state        TEXT NOT NULL,
  actor        TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  UNIQUE(question_id, seq)
)`,
  `CREATE TABLE reality_answer(
  id             TEXT PRIMARY KEY,
  question_id    TEXT NOT NULL REFERENCES reality_question(id),
  schema_version INTEGER NOT NULL,
  seq            INTEGER NOT NULL,
  author         TEXT NOT NULL,
  answered_at    TEXT NOT NULL,
  recorded_at    TEXT NOT NULL,
  outcome        TEXT NOT NULL,
  context_id     TEXT,
  payload_json   TEXT NOT NULL,
  UNIQUE(question_id, seq)
)`,
  `CREATE TABLE reality_topic_plan(
  proposal_id     TEXT PRIMARY KEY,
  subject_key     TEXT NOT NULL,
  operation       TEXT NOT NULL,
  entity_kind     TEXT NOT NULL,
  evidence_weight INTEGER NOT NULL,
  created_at      TEXT NOT NULL,
  payload_json    TEXT NOT NULL
)`,
  `CREATE TABLE reality_topic_ruling(
  id            TEXT PRIMARY KEY,
  proposal_id   TEXT NOT NULL UNIQUE REFERENCES reality_topic_plan(proposal_id),
  verdict       TEXT NOT NULL,
  entity_id     TEXT REFERENCES reality_entity(id),
  resolution_id TEXT REFERENCES reality_resolution(id),
  actor         TEXT NOT NULL,
  recorded_at   TEXT NOT NULL,
  payload_json  TEXT NOT NULL
)`,
  `CREATE TABLE reality_plan(
  id                  TEXT PRIMARY KEY,
  question_id         TEXT NOT NULL REFERENCES reality_question(id),
  answer_id           TEXT NOT NULL REFERENCES reality_answer(id),
  schema_version      INTEGER NOT NULL,
  interpreter_version INTEGER NOT NULL,
  created_at          TEXT NOT NULL,
  payload_json        TEXT NOT NULL
)`,
  `CREATE TABLE reality_plan_action(
  id           TEXT PRIMARY KEY,
  plan_id      TEXT NOT NULL REFERENCES reality_plan(id),
  position     INTEGER NOT NULL,
  action_kind  TEXT NOT NULL,
  state        TEXT NOT NULL,
  result_id    TEXT,
  applied_at   TEXT,
  payload_json TEXT NOT NULL,
  UNIQUE(plan_id, position)
)`,
  `CREATE TABLE reality_plan_acceptance(
  id           TEXT PRIMARY KEY,
  plan_id      TEXT NOT NULL UNIQUE REFERENCES reality_plan(id),
  actor        TEXT NOT NULL,
  context_id   TEXT,
  recorded_at  TEXT NOT NULL,
  payload_json TEXT NOT NULL
)`,
  `CREATE TABLE reality_plan_rejection(
  id           TEXT PRIMARY KEY,
  plan_id      TEXT NOT NULL UNIQUE REFERENCES reality_plan(id),
  actor        TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  payload_json TEXT NOT NULL
)`,
  `CREATE TABLE run_receipt(
  id             TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  run_id         TEXT NOT NULL,
  preparation_id TEXT NOT NULL REFERENCES run_preparation(id),
  revision       INTEGER NOT NULL,
  supersedes     TEXT REFERENCES run_receipt(id),
  recorded_at    TEXT NOT NULL,
  authority_kind TEXT,
  authority_ref  TEXT,
  sync_state     TEXT NOT NULL CHECK (sync_state IN ('pending-sync', 'committed')),
  counts         TEXT NOT NULL,
  payload        BLOB NOT NULL,
  UNIQUE(run_id, revision),
  UNIQUE(supersedes)
)`,
  `CREATE TABLE complaint(
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
)`,
  `CREATE TABLE disposition_proposal(
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
)`,
];

/** `catalog.db` as `internal/catalog` migrates it (schema_version 4). */
const GO_CATALOG_SCHEMA: readonly string[] = [
  `CREATE TABLE sessions(
  selector TEXT PRIMARY KEY,
  harness TEXT,
  source_id TEXT,
  primary_path TEXT,
  primary_size INTEGER,
  primary_mtime_unixnano INTEGER,
  title TEXT,
  title_provenance TEXT,
  workspace TEXT,
  created_at TEXT,
  modified_at TEXT,
  continuation_grade INTEGER,
  artifact_count INTEGER,
  blob_count INTEGER,
  unresolved_blob_count INTEGER,
  cost_usd REAL,
  total_tokens INTEGER,
  turns INTEGER,
  tool_errors INTEGER,
  row_json TEXT
)`,
  `CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
];

// ---------------------------------------------------------------------------- the fixture

const DEPLOYMENT = "babel-test";
const HOST = "dev-fixture";

/** `internal/sharedcatalog.SessionUID`, which is what a Go `evidence` edge addresses. */
function sessionUid(harness: string, sourceId: string): string {
  const digest = createHash("sha256");
  for (const part of [DEPLOYMENT, HOST, harness, sourceId]) {
    digest.update(`${String(Buffer.byteLength(part, "utf8"))}:${part}`);
  }
  return digest.digest("hex");
}

const CITED_UID = sessionUid("omp", "-code/alpha");
/** A session the catalog no longer holds: its citation must keep the raw digest. */
const ROTATED_UID = sessionUid("omp", "-code/rotated-away");

/** A statement whose whitespace must collapse and whose length is under the bound. */
const STATEMENT = "The release  pipeline\n  treats a green suite as evidence of function.";
/** 239 ASCII bytes, then a two-byte rune straddling the 240-byte cut. */
const LONG_STATEMENT = `${"a".repeat(239)}éxyz`;

let dir = "";
let durablePath = "";
let catalogPath = "";

function seedDurable(path: string): void {
  const db = new Database(path, { create: true });
  for (const statement of GO_DURABLE_SCHEMA) db.run(statement);

  db.run(
    `INSERT INTO run_preparation VALUES (?, 1, '2026-09-01T00:00:00Z', 1, 'committed', ?)`,
    [
      "prep-1",
      JSON.stringify({
        schema: 1,
        id: "prep-1",
        selection: [
          { host: HOST, harness: "omp", source_id: "-code/alpha", source_digest: "sha256:beef", capture_digest: "sha256:cafe" },
        ],
      }),
    ],
  );

  const record = (
    table: string,
    id: string,
    runId: string,
    createdAt: string,
    body: Record<string, unknown>,
    extra: readonly (string | number)[] = [],
  ): void => {
    const columns = table === "frontier_observation"
      ? `(id, ancestor_id, hypothesis_id, run_id, recipe_id, recipe_version, schema_version, evidence_count, created_at, payload_json)`
      : `(id, ancestor_id, run_id, schema_version, created_at, payload_json)`;
    const values = table === "frontier_observation"
      ? [id, null, ...extra, runId, "outcome-integrity", 3, 1, 2, createdAt, JSON.stringify(body)]
      : [id, null, runId, 1, createdAt, JSON.stringify(body)];
    db.run(`INSERT INTO ${table} ${columns} VALUES (${values.map(() => "?").join(", ")})`, values as never);
  };

  record("frontier_hypothesis", "hyp_a", "run-1", "2026-09-01T01:00:00Z", {
    statement: STATEMENT, origin_cues: ["a comment"], provisional_labels: ["verification"], novelty: 0.6, priority: 0.8, notes: "",
  });
  record("frontier_hypothesis", "hyp_a2", "run-2", "2026-09-02T01:00:00Z", {
    statement: LONG_STATEMENT, origin_cues: [], provisional_labels: [], novelty: 0.1, priority: 0.2, notes: "refined",
  });
  record("frontier_observation", "obs_a", "run-1", "2026-09-01T01:05:00Z", {
    claim: "The agent rewrote the assertion.", category: "verification-integrity", confidence: "high", impact: "high",
    evidence: [{ locator: { path: "/s.jsonl", line: 2073, byte_offset: 7300451, digest: "a2d7" }, note: "states it will adjust" }],
    counter_evidence: [], temporal_status: "current",
  }, ["hyp_a"]);
  record("frontier_finding", "fnd_a", "run-2", "2026-09-02T02:00:00Z", {
    title: "One envelope, two payload classes", pattern: "…", significance: "…", scope: ["omp"], recurrence: 2,
  });
  record("frontier_proposal", "pro_a", "run-2", "2026-09-02T03:00:00Z", {
    title: "Gate the preflight batch on unmerged paths", problem: "…", outcome: "…", targets: ["code"],
  });

  const revision = (
    id: string, type: string, entityId: string, root: string, supersedes: string, seq: number, at: string,
  ): void => {
    db.run(`INSERT INTO frontier_revision VALUES (?, ?, ?, ?, ?, ?, 'run', ?, ?, '{}')`, [
      id, type, entityId, root, supersedes, seq, `run-${String(seq)}`, at,
    ]);
  };
  // hyp_a2 is a second wording of hyp_a's chain, which is the only shape that makes the target's
  // immediate foreign key on records.supersedes_id say anything.
  revision("rev_1", "hypothesis", "hyp_a", "hyp_a", "", 1, "2026-09-01T01:00:00Z");
  revision("rev_2", "hypothesis", "hyp_a2", "hyp_a", "hyp_a", 2, "2026-09-02T01:00:00Z");
  revision("rev_3", "observation", "obs_a", "obs_a", "", 1, "2026-09-01T01:05:00Z");
  revision("rev_4", "finding", "fnd_a", "fnd_a", "", 1, "2026-09-02T02:00:00Z");
  revision("rev_5", "proposal", "pro_a", "pro_a", "", 1, "2026-09-02T03:00:00Z");

  const edge = (
    id: string, kind: string, fromKind: string, fromId: string, toKind: string, toId: string, note: string,
  ): void => {
    db.run(`INSERT INTO reference_edge VALUES (?, ?, ?, ?, ?, ?, 'run', 'run-2', 1, '2026-09-02T04:00:00Z', ?)`, [
      id, kind, fromKind, fromId, toKind, toId, JSON.stringify({ schema: 1, note }),
    ]);
  };
  edge("ref_1", "evidence", "observation", "obs_a", "session", CITED_UID, "the session the claim read");
  edge("ref_2", "evidence", "observation", "obs_a", "session", ROTATED_UID, "a session since rotated out");
  edge("ref_3", "inspired_by", "hypothesis", "hyp_a2", "observation", "obs_a", "the run that produced this read that");
  edge("ref_4", "addresses", "proposal", "pro_a", "hypothesis", "hyp_a", "suggested change");
  edge("ref_5", "duplicates", "hypothesis", "hyp_a2", "hypothesis", "hyp_a", "candidate restates");

  db.run(
    `INSERT INTO frontier_hypothesis_link VALUES ('lnk_1', 'hyp_a2', 'hyp_a', 'contradicts', '2026-09-02T05:00:00Z', ?)`,
    [JSON.stringify({ note: "challenger objection" })],
  );
  db.run(`INSERT INTO frontier_finding_observation VALUES ('fnd_a', 'obs_a', 0)`);
  db.run(`INSERT INTO frontier_proposal_finding VALUES ('pro_a', 'fnd_a', 0)`);
  // The same pair reference_edge already carries as `addresses`: one edge, not two.
  db.run(`INSERT INTO frontier_proposal_hypothesis VALUES ('pro_a', 'hyp_a', 0)`);

  db.run(
    `INSERT INTO frontier_status_event VALUES ('ste_1', 'hyp_a', 1, 'untriaged', 'run-1', '2026-09-01T01:00:01Z', '{}', 'run', 'run-1')`,
  );
  db.run(
    `INSERT INTO frontier_status_event VALUES ('ste_2', 'hyp_a', 2, 'promoted', 'run-2', '2026-09-02T02:00:01Z', ?, '', '')`,
    [JSON.stringify({ note: "consolidated into a finding" })],
  );
  db.run(
    `INSERT INTO frontier_disposition VALUES ('dsp_1', 'proposal', 'pro_a', 1, 'reject', 'alex', '', '', '2026-09-03T00:00:00Z', ?)`,
    [JSON.stringify({ note: "the symptom, not the problem" })],
  );
  db.run(
    `INSERT INTO frontier_filing VALUES ('fil_1', 'finding', 'fnd_a', 'ent_1', 'run', 'run-2', 0, 0, NULL, 1, '2026-09-02T06:00:00Z', ?)`,
    [JSON.stringify({ rationale: "the record concerns the repository" })],
  );
  db.run(
    `INSERT INTO frontier_filing VALUES ('fil_2', 'finding', 'fnd_a', 'ent_1', 'run', 'run-2', 0, 1, 'fil_1', 1, '2026-09-02T07:00:00Z', ?)`,
    [JSON.stringify({ rationale: "withdrawn: the machine is not the subject" })],
  );

  db.run(
    `INSERT INTO evaluation_claim VALUES ('eval-a-1', 'observation', 'obs_a', 'eval-1', 'reception', 'eval-policy-1',
      'ctx-1', '42', 'in-1', '', 'exploration', '[]', 1, 3.125, '2026-09-04', '2026-09-04T00:00:00Z',
      '2026-09-04T00:00:08Z', '2026-09-04T00:00:05Z', 'eval-1', 1, 0.25)`,
  );
  const evaluation = (
    id: string, kind: string, subjectKind: string, subjectId: string, assignment: string, actorKind: string,
    actorId: string, runId: string, role: string, readHead: string, seq: number, at: string,
    body: Record<string, unknown>,
  ): void => {
    db.run(
      `INSERT INTO evaluation_record VALUES (?, ?, ?, ?, ?, 1, '', '', '', ?, ?, ?, ?, 'ctx-1', ?, '', ?, 1, ?, ?)`,
      [id, kind, subjectKind, subjectId, assignment, actorKind, actorId, runId, role, readHead, seq, at,
        JSON.stringify({ schema: 1, record: { id, kind, created_at: at, ...body } })],
    );
  };
  evaluation("evr_1", "assessment", "observation", "obs_a", "eval-a-1", "run", "eval-1", "eval-1", "reception",
    "obs_a", 1, "2026-09-04T00:00:04Z", { assessment: { vote: "support", context_version: "ctx-1" } });
  evaluation("evr_2", "feedback", "proposal", "pro_a", "", "operator", "alex", "", "", "", 2,
    "2026-09-04T01:00:00Z", { stance: "disagree", reason: "ask Babel what it means", question: true });
  evaluation("evr_3", "policy", "", "", "", "operator", "alex", "", "", "", 3, "2026-09-04T02:00:00Z",
    { reason: "changed: version", policy: { version: "eval-policy-1", enabled: false, daily_cost: 25 } });
  // A kind with no table of its own: it must not appear anywhere.
  evaluation("evr_4", "attempt", "", "", "eval-a-1", "run", "eval-1", "eval-1", "reception", "", 4,
    "2026-09-04T03:00:00Z", { attempt: { state: "failed", reason: "the lease expired" } });

  db.run(`INSERT INTO reality_entity VALUES ('ent_1', 'machine', 1, '2026-09-05T00:00:00Z', ?)`, [
    JSON.stringify({ display_name: "dev-01" }),
  ]);
  db.run(`INSERT INTO reality_entity_membership VALUES ('ent_1', 1, 'self', 'ent_1', NULL, '2026-09-05T00:00:00Z')`);
  db.run(`INSERT INTO reality_entity_alias VALUES ('als_1', 'ent_1', 'hostname', 'k1', 1, '2026-09-05T00:00:01Z', ?)`, [
    JSON.stringify({ value: "dev-01" }),
  ]);
  db.run(`INSERT INTO reality_alias_event VALUES ('ath_1', 'als_1', 1, 'retired', '2026-09-06T00:00:00Z', '{}')`);
  db.run(
    `INSERT INTO reality_fact VALUES ('fct_1', 1, 'ent_1', 'repository-remote', 'text', NULL, '2026-09-05T00:00:00Z',
      NULL, '2026-09-05T00:00:00Z', '2026-09-05T00:00:02Z', NULL, 'operator', 'alex', '2026-09-05T00:00:00Z',
      'stated', 'routine', NULL, NULL, NULL, ?)`,
    [JSON.stringify({ value: { kind: "text", text: "github.com/atyrode/code" }, note: "the operator said so" })],
  );
  db.run(`INSERT INTO reality_fact_status VALUES ('fst_1', 'fct_1', 1, 'active', '2026-09-05T00:00:03Z', ?)`, [
    JSON.stringify({ actor: "alex", reason: "asserted" }),
  ]);
  db.run(`INSERT INTO reality_question VALUES ('qst_1', 1, 'acquire-context', 'blocking', 'routine', 'operator', 'dk1', 0, NULL, '2026-09-05T01:00:00Z', ?)`, [
    JSON.stringify({ prompt: "Is the object still readable?", why_asked: "the local copy was deleted" }),
  ]);
  db.run(`INSERT INTO reality_question_work VALUES ('qst_1', 'hypothesis', 'hyp_a', 1)`);
  db.run(`INSERT INTO reality_question_entity VALUES ('qst_1', 'ent_1')`);
  db.run(`INSERT INTO reality_question_evidence VALUES ('qst_1', 'hyp_a')`);
  db.run(`INSERT INTO reality_question_event VALUES ('qse_1', 'qst_1', 1, 'open', 'asker', '2026-09-05T01:00:01Z', '{}')`);
  db.run(`INSERT INTO reality_answer VALUES ('ans_1', 'qst_1', 1, 1, 'alex', '2026-09-05T02:00:00Z', '2026-09-05T02:00:01Z', 'answered', NULL, ?)`, [
    JSON.stringify({ text: "Yes, it is still there." }),
  ]);
  db.run(`INSERT INTO reality_topic_plan VALUES ('pro_a', 'sk1', 'create', 'repository', 21, '2026-09-05T03:00:00Z', ?)`, [
    JSON.stringify({ identity: "github.com/atyrode/code" }),
  ]);
  db.run(`INSERT INTO reality_topic_ruling VALUES ('trl_1', 'pro_a', 'accept', 'ent_1', NULL, 'alex', '2026-09-05T04:00:00Z', ?)`, [
    JSON.stringify({ reason: "the repository is the subject" }),
  ]);
  db.run(`INSERT INTO reality_plan VALUES ('pln_1', 'qst_1', 'ans_1', 1, 2, '2026-09-05T05:00:00Z', ?)`, [
    JSON.stringify({ summary: "assert the object is readable" }),
  ]);
  db.run(`INSERT INTO reality_plan_action VALUES ('pac_1', 'pln_1', 0, 'assert-fact', 'pending', NULL, NULL, '{}')`);
  db.run(`INSERT INTO reality_plan_rejection VALUES ('prj_1', 'pln_1', 'alex', '2026-09-05T06:00:00Z', ?)`, [
    JSON.stringify({ reason: "the question was already answered" }),
  ]);

  const receipt = (
    id: string, runId: string, revisionNumber: number, authorityKind: string, authorityRef: string,
    recordedAt: string, body: Record<string, unknown>,
  ): void => {
    db.run(
      `INSERT INTO run_receipt VALUES (?, 2, ?, 'prep-1', ?, NULL, ?, ?, ?, 'pending-sync', ?, ?)`,
      [id, runId, revisionNumber, recordedAt, authorityKind, authorityRef,
        JSON.stringify({ tool_requests: 1, failures: 0 }), JSON.stringify(body)],
    );
  };
  receipt("rcpt-1", "run-2", 1, "operator", "command:explore", "2026-09-02T09:00:00Z", {
    checkpoint: { state: "running", stage: "explore", records: ["hyp_a2"] },
    timing: { started_at: "2026-09-02T08:00:00Z", finished_at: "" },
    worker: { JobID: "run-2/job", Profile: { id: "code", revision: 2 }, Recipes: [{ id: "outcome-integrity", version: 3 }] },
  });
  receipt("rcpt-2", "run-2", 2, "operator", "command:explore", "2026-09-02T10:00:00Z", {
    checkpoint: { state: "closed", stage: "synthesize", records: ["hyp_a2", "fnd_a", "pro_a"] },
    timing: { started_at: "2026-09-02T08:00:00Z", finished_at: "2026-09-02T10:00:00Z" },
    worker: {
      JobID: "run-2/synthesize/job", Profile: { id: "code", revision: 2 },
      Recipes: [{ id: "outcome-integrity", version: 3 }],
      Usage: { total_tokens: 2555525, cost: 2.198 },
    },
  });
  receipt("rcpt-3", "eval-1", 1, "policy", "evaluation:reception:eval-a-1", "2026-09-04T00:00:06Z", {
    checkpoint: { state: "interrupted", stage: "review", records: ["evr_1"] },
    timing: { started_at: "2026-09-04T00:00:00Z", finished_at: "2026-09-04T00:00:06Z" },
    worker: { JobID: "eval-1/review", Profile: { id: "code", revision: 3 }, Recipes: [] },
  });

  db.run(`INSERT INTO complaint VALUES ('cmp_1', 'cmp_1', NULL, 1, 'alex', ?, 0, 1, '2026-09-07T00:00:00Z', ?)`, [
    HOST, JSON.stringify({ text: "stop repinning tests" }),
  ]);
  db.run(
    `INSERT INTO disposition_proposal VALUES ('dis_1', 'hypothesis', 'hyp_a', 'develop-further', 'run', 'run-2', 'd1', 1, '2026-09-02T11:00:00Z', ?)`,
    [JSON.stringify({ summary: "extend the observation", rationale: "avoids a sixth copy" })],
  );
  db.close();
}

function seedCatalog(path: string): void {
  const db = new Database(path, { create: true });
  for (const statement of GO_CATALOG_SCHEMA) db.run(statement);
  db.run(`INSERT INTO meta VALUES ('schema_version', '4')`);
  db.run(
    `INSERT INTO sessions (selector, harness, source_id, primary_path, primary_size, title, title_provenance,
      workspace, created_at, modified_at, cost_usd, total_tokens, turns, tool_errors, row_json)
     VALUES ('omp/-code/alpha', 'omp', '-code/alpha', '/s.jsonl', 15106038, 'Begin issue #91', 'recorded',
      '/home/alex/code', '2026-08-22T22:22:59Z', '2026-08-23T15:17:45Z', 162.19, 159846281, 1065, 47, '{}')`,
  );
  db.run(
    `INSERT INTO sessions (selector, harness, source_id, primary_path, primary_size, title, title_provenance,
      workspace, created_at, modified_at, cost_usd, total_tokens, turns, tool_errors, row_json)
     VALUES ('babel/run-2/explore', 'babel', 'run-2/explore', '/a.jsonl', 382, 'babel explore pass', 'recorded',
      '/home/alex', '2026-09-02T08:00:00Z', '2026-09-02T10:00:00Z', NULL, NULL, NULL, NULL, '{}')`,
  );
  db.close();
}

let plans: readonly TablePlan[] = [];
let notes: readonly string[] = [];
let store: PluginDatabaseAdmin | null = null;

function planned(table: string): TablePlan {
  const found = plans.find((plan) => plan.table === table);
  if (found === undefined) throw new Error(`no plan for ${table}`);
  return found;
}

async function scalar(sql: string, params: readonly string[] = []): Promise<Record<string, unknown>> {
  const rows = await (store as PluginDatabaseAdmin).query(sql, params);
  const first = rows[0];
  if (first === undefined) throw new Error(`no row for ${sql}`);
  return first as Record<string, unknown>;
}

beforeAll(async () => {
  dir = mkdtempSync(join(tmpdir(), "babel-import-"));
  durablePath = join(dir, "durable.db");
  catalogPath = join(dir, "catalog.db");
  seedDurable(durablePath);
  seedCatalog(catalogPath);
  const plan = planImport({
    from: durablePath, catalog: catalogPath, host: HOST, deployment: DEPLOYMENT,
    now: () => "2026-09-12T00:00:00Z",
  });
  plans = plan.plans;
  notes = plan.notes;
  store = openPluginDatabase({ dataDir: join(dir, "hub"), pluginId: BABEL_PLUGIN_ID });
  expect(await ensureSchema(store)).toBe(true);
  await applyPlan(store, plans, () => "2026-09-12T00:00:00Z");
});

afterAll(() => {
  store?.close();
  if (dir !== "") rmSync(dir, { recursive: true, force: true });
});

// ---------------------------------------------------------------------------- the crossing

test("every planned row lands in its table, and a second import lands nothing more", async () => {
  const handle = store as PluginDatabaseAdmin;
  for (const plan of plans) {
    const row = await scalar(`SELECT count(*) AS n FROM ${plan.table}`);
    expect([plan.table, row["n"]]).toEqual([plan.table, plan.rows.length]);
  }
  // The store's triggers abort an UPDATE on a record, a ruling, a filing or an assessment, so a
  // re-import that tried to refresh a row rather than skip it would throw here rather than pass.
  await applyPlan(handle, plans, () => "2026-09-13T00:00:00Z");
  for (const plan of plans) {
    const row = await scalar(`SELECT count(*) AS n FROM ${plan.table}`);
    expect([plan.table, row["n"]]).toEqual([plan.table, plan.rows.length]);
  }
  const ledger = await handle.query(`SELECT table_name, rows FROM imports ORDER BY table_name`);
  expect(ledger.length).toBe(plans.length);
  const counts: Record<string, number> = {};
  for (const row of ledger) counts[String(row["table_name"])] = Number(row["rows"]);
  for (const plan of plans) expect(counts[plan.table]).toBe(plan.rows.length);
});

test("a record keeps its id, its chain and the title the Go tree derived", async () => {
  const head = await scalar(`SELECT * FROM records WHERE id = ?`, ["hyp_a2"]);
  expect(head["kind"]).toBe("hypothesis");
  expect(head["root_id"]).toBe("hyp_a");
  expect(head["supersedes_id"]).toBe("hyp_a");
  expect(head["seq"]).toBe(2);
  expect(head["actor_kind"]).toBe("run");
  // 239 bytes of the statement, cut before the two-byte rune that straddles byte 240.
  expect(head["title"]).toBe(`${"a".repeat(239)}…`);

  const first = await scalar(`SELECT * FROM records WHERE id = ?`, ["hyp_a"]);
  expect(first["supersedes_id"]).toBe(null);
  expect(first["title"]).toBe("The release pipeline treats a green suite as evidence of function.");
  expect(JSON.parse(String(first["payload"]))).toMatchObject({ statement: STATEMENT, schema: 1 });

  const observation = await scalar(`SELECT * FROM records WHERE id = ?`, ["obs_a"]);
  expect(observation["parent_id"]).toBe("hyp_a");
  expect(observation["recipe_id"]).toBe("outcome-integrity");
  expect(observation["recipe_version"]).toBe(3);
  expect(observation["title"]).toBe("The agent rewrote the assertion.");

  const proposal = await scalar(`SELECT title FROM records WHERE id = ?`, ["pro_a"]);
  expect(proposal["title"]).toBe("Gate the preflight batch on unmerged paths");
});

test("summarize is the Go bound: whitespace collapsed, 240 bytes, never half a rune", () => {
  expect(summarize("  two   words\n here ")).toBe("two words here");
  expect(summarize("a".repeat(240))).toBe("a".repeat(240));
  expect(summarize(LONG_STATEMENT)).toBe(`${"a".repeat(239)}…`);
  const wide = `${"b".repeat(238)}日本`;
  expect(summarize(wide)).toBe(`${"b".repeat(238)}…`);
});

test("edges speak the rewrite's vocabulary and say each relation once", async () => {
  const handle = store as PluginDatabaseAdmin;
  const kinds = await handle.query(`SELECT kind, count(*) AS n FROM edges GROUP BY kind ORDER BY kind`);
  expect(kinds.map((row) => [row["kind"], row["n"]])).toEqual([
    ["addresses", 2], ["cites", 2], ["consolidates", 1], ["contradicts", 1], ["derived_from", 1], ["duplicates", 1],
  ]);
  // reference_edge's `addresses` and frontier_proposal_hypothesis's row are the same relation; the
  // reference edge wins because it carries the real id and the note.
  const addressed = await scalar(`SELECT id, note FROM edges WHERE kind = 'addresses' AND to_id = 'hyp_a'`);
  expect(addressed["id"]).toBe("ref_4");
  expect(addressed["note"]).toBe("suggested change");
  const consolidates = await scalar(`SELECT * FROM edges WHERE kind = 'consolidates'`);
  expect([consolidates["from_id"], consolidates["to_id"], consolidates["position"]]).toEqual(["fnd_a", "obs_a", 0]);
});

test("a citation resolves to the session's selector, and keeps the digest when it cannot", async () => {
  const handle = store as PluginDatabaseAdmin;
  const cited = await handle.query(`SELECT to_id FROM edges WHERE kind = 'cites' ORDER BY id`);
  expect(cited.map((row) => row["to_id"])).toEqual(["omp/-code/alpha", ROTATED_UID]);
  const joined = await scalar(
    `SELECT s.host, s.content_digest FROM edges e JOIN sessions s ON s.selector = e.to_id WHERE e.id = 'ref_1'`,
  );
  expect(joined["host"]).toBe(HOST);
  expect(joined["content_digest"]).toBe("beef");
  expect(notes.some((note) => note.includes("keep the raw 64-hex shared-catalog session uid"))).toBe(true);
});

test("the operator's acts and Babel's votes arrive with their provenance", async () => {
  const ruling = await scalar(`SELECT * FROM dispositions WHERE record_id = 'pro_a'`);
  expect([ruling["disposition"], ruling["actor_id"], ruling["note"]])
    .toEqual(["reject", "alex", "the symptom, not the problem"]);

  const withdrawal = await scalar(`SELECT * FROM filings WHERE id = 'fil_2'`);
  expect([withdrawal["withdrawn"], withdrawal["supersedes_id"], withdrawal["author_kind"]])
    .toEqual([1, "fil_1", "run"]);

  const asked = await scalar(`SELECT * FROM feedback WHERE id = 'evr_2'`);
  expect([asked["stance"], asked["question"], asked["reason"]])
    .toEqual(["disagree", 1, "ask Babel what it means"]);

  const vote = await scalar(`SELECT * FROM assessments WHERE id = 'evr_1'`);
  expect([vote["vote"], vote["role"], vote["revision_id"], vote["claim_id"], vote["lane"]])
    .toEqual(["support", "reception", "obs_a", "eval-a-1", "exploration"]);

  const claim = await scalar(`SELECT * FROM claims WHERE id = 'eval-a-1'`);
  expect([claim["record_id"], claim["reserved_cost"], claim["actual_cost"], claim["finished_at"]])
    .toEqual(["obs_a", 3.125, 0.25, "2026-09-04T00:00:05Z"]);

  const policy = await scalar(`SELECT * FROM policies WHERE version = 'eval-policy-1'`);
  expect(JSON.parse(String(policy["payload"]))).toMatchObject({ version: "eval-policy-1", dailyCost: 25 });

  const status = await scalar(`SELECT * FROM status_events WHERE id = 'ste_2'`);
  expect([status["status"], status["seq"], status["actor_kind"], status["reason"]])
    .toEqual(["promoted", 2, "run", "consolidated into a finding"]);
});

test("a run is one row per run, from its newest receipt revision", async () => {
  const handle = store as PluginDatabaseAdmin;
  expect((await handle.query(`SELECT id FROM runs ORDER BY id`)).map((row) => row["id"]))
    .toEqual(["eval-1", "run-2"]);

  const explore = await scalar(`SELECT * FROM runs WHERE id = 'run-2'`);
  expect([explore["kind"], explore["closure"], explore["records"], explore["tokens"], explore["cost_usd"]])
    .toEqual(["explore", "completed", 3, 2555525, 2.198]);
  expect([explore["machine_id"], explore["recipe_id"], explore["profile"], explore["finished_at"]])
    .toEqual([HOST, "outcome-integrity", "code@2", "2026-09-02T10:00:00Z"]);
  expect(JSON.parse(String(explore["preparation"]))).toMatchObject({ id: "prep-1" });
  expect(JSON.parse(String(explore["payload"]))).toMatchObject({ receipt_id: "rcpt-2", counts: { tool_requests: 1 } });

  const evaluate = await scalar(`SELECT * FROM runs WHERE id = 'eval-1'`);
  expect([evaluate["kind"], evaluate["closure"], evaluate["authority_kind"]])
    .toEqual(["evaluate", "stopped", "policy"]);
});

test("the Reality Ledger crosses whole, and a plan carries the ruling that settled it", async () => {
  const entity = await scalar(`SELECT * FROM entities WHERE id = 'ent_1'`);
  expect([entity["name"], entity["canonical_id"], entity["kind"]]).toEqual(["dev-01", "ent_1", "machine"]);

  const alias = await scalar(`SELECT * FROM aliases WHERE id = 'als_1'`);
  // The hub resolves a topic by name through value_key, so the key is the normalized value,
  // not the sealed digest the Go store kept.
  expect([alias["value"], alias["value_key"], alias["retired_at"]]).toEqual([
    "dev-01", "dev-01", "2026-09-06T00:00:00Z",
  ]);

  const fact = await scalar(`SELECT * FROM facts WHERE id = 'fct_1'`);
  expect([fact["value"], fact["predicate"], fact["authority_id"], fact["note"]])
    .toEqual(["github.com/atyrode/code", "repository-remote", "alex", "the operator said so"]);

  const question = await scalar(`SELECT * FROM questions WHERE id = 'qst_1'`);
  expect([question["text"], question["why"], question["class"]])
    .toEqual(["Is the object still readable?", "the local copy was deleted", "blocking"]);
  expect(JSON.parse(String(question["payload"]))).toMatchObject({
    expected_authority: "operator",
    work: [{ kind: "hypothesis", id: "hyp_a", blocking: true }],
    entities: ["ent_1"],
  });

  const answer = await scalar(`SELECT * FROM answers WHERE id = 'ans_1'`);
  expect([answer["text"], answer["outcome"]]).toEqual(["Yes, it is still there.", "answered"]);

  const topic = await scalar(`SELECT * FROM plans WHERE kind = 'topic'`);
  expect([topic["id"], topic["subject_id"], topic["operation"], topic["state"], topic["ruled_by"], topic["result"]])
    .toEqual(["pro_a", "pro_a", "create", "applied", "alex", "ent_1"]);

  const interpretation = await scalar(`SELECT * FROM plans WHERE kind = 'answer'`);
  expect([interpretation["subject_id"], interpretation["state"], interpretation["operation"]])
    .toEqual(["qst_1", "declined", "assert-fact"]);
});

test("what the schema has no home for is reported rather than dropped in silence", () => {
  const joined = notes.join("\n");
  expect(joined).toContain("disposition_proposal holds 1 rows with no home");
  expect(joined).toContain("Add `'action'` to plans.kind's CHECK");
  expect(joined).toContain("entities.created_by is empty");
  expect(joined).toContain("questions.raised_by_id is empty");
  expect(joined).toContain("policies.record_id");
  // An `attempt` record is an evaluation row with no table; it is named in the notes and, because
  // nothing invented a home for it, it is in no table either.
  expect(joined).toContain("`assignment`, `attempt` and `checkpoint` have no table");
  expect(planned("assessments").rows.length).toBe(1);
  expect(planned("feedback").rows.length).toBe(1);
});

test("--into names the path the engine would open, and refuses any other file", () => {
  const resolved = resolveTarget(join(dir, "hub"));
  expect(resolved.path).toBe(join(dir, "hub", "plugins", BABEL_PLUGIN_ID, "data.db"));
  expect(resolveTarget(resolved.path).dataDir).toBe(join(dir, "hub"));
  expect(() => resolveTarget(join(dir, "somewhere.db"))).toThrow(/is not a path the engine would open/u);
});
