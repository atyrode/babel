#!/usr/bin/env bun
/*
  THE CROSSING (plan §8, phase P2): the one-off importer that reads the Go tree's per-machine
  `durable.db` and the local session `catalog.db` and writes them into the plugin's own database
  (`store/schema.ts`), ids kept, so provenance survives the rewrite.

  It is a dev-time Bun CLI and never enters a packed artifact. It reads the Go stores read-only
  through `bun:sqlite` and writes the target exclusively through ADR 0034's `PluginDatabase`
  contract — `openPluginDatabase` + `batch` — so the rows the hub will read are rows the hub's own
  primitive wrote, under its own bounds (999 parameters a statement, 256 statements a batch, one
  immediate transaction each).

  Three properties make a re-run safe. Every insert is `INSERT OR IGNORE` on the target's primary
  key, so importing twice imports nothing the second time; every synthesized identifier is a digest
  of the tuple it stands for, so the second run mints the same one; and the `imports` ledger row per
  table is upserted, so "how many rows came from where" is one row rather than a growing pile.

  What it refuses to do is invent. A column the Go stores never held is written NULL or empty and
  named in the report, never filled with a plausible value: the whole point of keeping the ids is
  that the imported half of the store can be told from the half Babel writes next.
*/

import { Database } from "bun:sqlite";
import { createHash } from "node:crypto";
import { isAbsolute, join, resolve, sep } from "node:path";
import type { PluginDatabase, SqlParam, SqlStatement } from "@manifold/plugin";
import { openPluginDatabase, pluginDatabasePath } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID } from "../contract.ts";
import { SCHEMA_V1 } from "../store/schema.ts";

// ---------------------------------------------------------------------------- the bounds we write under

/** ADR 0034's caps, restated so a chunk is sized against them rather than against a guess. */
const MAX_SQL_PARAMS = 999;
const MAX_SQL_BATCH_STATEMENTS = 256;
/** Rows per transaction. Small enough to stay far inside the 5-second batch deadline. */
const CHUNK_ROWS = 500;
/**
 * The byte cap the baseline's manifest requests, restated because a CLI opens the file itself and
 * the engine's default is 256 MiB — the operator's own store crosses at ~153 MiB of edges today and
 * grows with every run, so the default would eventually refuse the import as SQLITE_FULL.
 */
const MANIFEST_DATABASE_MAX_BYTES = 1024 * 1024 * 1024;

// ---------------------------------------------------------------------------- reading Go rows

const DECODER = new TextDecoder();
const ENCODER = new TextEncoder();

type Row = Record<string, unknown>;

/** A Go payload column is TEXT on some tables and BLOB on others; both are UTF-8 JSON. */
function text(value: unknown): string {
  if (typeof value === "string") return value;
  if (value instanceof Uint8Array) return DECODER.decode(value);
  if (value === null || value === undefined) return "";
  return String(value);
}

/** The empty string is how the Go stores spell "absent" in a NOT NULL column; SQL spells it NULL. */
function blank(value: unknown): string | null {
  const out = text(value);
  return out === "" ? null : out;
}

function count(value: unknown): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  return 0;
}

function real(value: unknown): number | null {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  return null;
}

function payload(value: unknown): Record<string, unknown> {
  const raw = text(value);
  if (raw === "") return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function field(from: Record<string, unknown>, name: string): string {
  const value = from[name];
  return typeof value === "string" ? value : "";
}

function nested(from: Record<string, unknown>, name: string): Record<string, unknown> {
  const value = from[name];
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : {};
}

/**
 * `internal/frontier`'s own one-line summary, restated: whitespace collapsed, cut at 240 BYTES on a
 * rune boundary, an ellipsis appended. It is a byte bound rather than a character bound because the
 * Go original is, and a title that disagreed with the Go tree's would make the same record read as
 * two different rows in a listing during the crossing.
 */
const MAX_SUMMARY_BYTES = 240;

export function summarize(value: string): string {
  const line = value.split(/\s+/u).filter((word) => word !== "").join(" ");
  const bytes = ENCODER.encode(line);
  if (bytes.length <= MAX_SUMMARY_BYTES) return line;
  let cut = MAX_SUMMARY_BYTES;
  while (cut > 0 && ((bytes[cut] ?? 0) & 0xc0) === 0x80) cut -= 1;
  return `${DECODER.decode(bytes.subarray(0, cut)).trim()}…`;
}

/** A deterministic identifier for a row the Go tree keyed by a tuple rather than by an id. */
function minted(prefix: string, ...parts: readonly string[]): string {
  const digest = createHash("sha256");
  for (const part of parts) digest.update(`${String(part.length)}:${part}`);
  return `${prefix}_${digest.digest("hex").slice(0, 32)}`;
}

/**
 * The shared catalog's durable session key, as `internal/sharedcatalog.SessionUID` derives it. It is
 * what a `cites` edge addresses, and resolving it back to a selector is the only way the peel can
 * name the session a record read.
 */
function sessionUid(deployment: string, host: string, harness: string, sourceId: string): string {
  const digest = createHash("sha256");
  for (const part of [deployment, host, harness, sourceId]) {
    digest.update(`${String(Buffer.byteLength(part, "utf8"))}:${part}`);
  }
  return digest.digest("hex");
}

// ---------------------------------------------------------------------------- the plan

/** One target table's rows, ready to write, with the Go tables they came from. */
export interface TablePlan {
  readonly table: string;
  readonly source: string;
  readonly columns: readonly string[];
  readonly rows: readonly (readonly SqlParam[])[];
}

export interface ImportOptions {
  /** The Go per-machine store. */
  readonly from: string;
  /** The Go local session catalog; without it the plan carries no `sessions` rows. */
  readonly catalog?: string | undefined;
  /** The operator-assigned host id the catalog's rows belong to; required with `catalog`. */
  readonly host?: string | undefined;
  /** The deployment id, which with `host` resolves a cited session's digest back to its selector. */
  readonly deployment?: string | undefined;
  /** The wall clock, injected so a test's plan is reproducible. */
  readonly now?: () => string;
}

export interface ImportPlan {
  readonly plans: readonly TablePlan[];
  /** Everything the crossing lost or had to derive, in the operator's language. */
  readonly notes: readonly string[];
}

/** Reads both Go stores and maps every row, without touching the target. */
export function planImport(options: ImportOptions): ImportPlan {
  const durable = new Database(options.from, { readonly: true });
  const catalog = options.catalog === undefined ? null : new Database(options.catalog, { readonly: true });
  try {
    return build(durable, catalog, options);
  } finally {
    durable.close();
    catalog?.close();
  }
}

function rowsOf(db: Database, sql: string): Row[] {
  return db.query(sql).all() as Row[];
}

function build(durable: Database, catalog: Database | null, options: ImportOptions): ImportPlan {
  const notes: string[] = [];
  const plans: TablePlan[] = [];
  const now = options.now ?? (() => new Date().toISOString());
  const host = options.host ?? "";
  const deployment = options.deployment ?? "";

  // -------------------------------------------------------------- the corpus the runs read
  // A preparation's selection is the only place the per-machine store records which machine a
  // session lived on and what its bytes digested to; the local catalog records neither.
  const sessionHost = new Map<string, string>();
  const sessionDigest = new Map<string, string>();
  const preparations = new Map<string, string>();
  for (const row of rowsOf(durable, `SELECT id, payload FROM run_preparation`)) {
    const document = payload(row["payload"]);
    preparations.set(text(row["id"]), JSON.stringify(document));
    const selection = document["selection"];
    if (!Array.isArray(selection)) continue;
    for (const entry of selection as readonly unknown[]) {
      if (typeof entry !== "object" || entry === null) continue;
      const source = entry as Record<string, unknown>;
      const key = `${field(source, "harness")}\u0000${field(source, "source_id")}`;
      const machine = field(source, "host");
      if (machine !== "" && !sessionHost.has(key)) sessionHost.set(key, machine);
      const digest = field(source, "source_digest").replace(/^sha256:/u, "");
      if (digest !== "" && !sessionDigest.has(key)) sessionDigest.set(key, digest);
    }
  }

  // -------------------------------------------------------------- sessions
  const uidToSelector = new Map<string, string>();
  if (catalog !== null) {
    if (host === "") {
      throw new Error(
        "--catalog needs --host <id>: the local catalog records no machine, and sessions.host is the " +
          "operator-assigned identity the shared catalog keyed on (storage.json's host_id, e.g. dev-01)",
      );
    }
    const columns = [
      "selector", "host", "harness", "source_id", "title", "title_provenance", "workspace",
      "repository_identity", "repository_remote", "repository_reason", "modified_at", "size",
      "cost_usd", "total_tokens", "turns", "tool_errors", "content_digest", "snapshot_id",
      "archived_at", "seen_at",
    ] as const;
    const seenAt = now();
    const rows: SqlParam[][] = [];
    for (const row of rowsOf(catalog, `SELECT * FROM sessions`)) {
      const harness = text(row["harness"]);
      const sourceId = text(row["source_id"]);
      const selector = text(row["selector"]);
      const key = `${harness}\u0000${sourceId}`;
      if (deployment !== "") uidToSelector.set(sessionUid(deployment, host, harness, sourceId), selector);
      rows.push([
        selector, sessionHost.get(key) ?? host, harness, sourceId,
        blank(row["title"]), blank(row["title_provenance"]), blank(row["workspace"]),
        null, null, null,
        blank(row["modified_at"]), real(row["primary_size"]),
        real(row["cost_usd"]), real(row["total_tokens"]), real(row["turns"]), real(row["tool_errors"]),
        sessionDigest.get(key) ?? null, null, null, seenAt,
      ]);
    }
    plans.push({ table: "sessions", source: "catalog.db:sessions", columns, rows });
    notes.push(
      "sessions.repository_identity/remote/reason are NULL: the local catalog (schema_version 4) has " +
        "no repository columns — internal/adapter observes repository identity at scan time and only " +
        "the retired PostgreSQL catalog stored it. A `scan` job (P4) fills them.",
    );
    notes.push(
      "sessions.snapshot_id/archived_at are NULL: restic snapshot ids lived in the shared catalog, " +
        "which the crossing retires. An `archive` job (P4) fills them.",
    );
    notes.push(
      "sessions.seen_at is the import's own timestamp: the local catalog stores no scan time, only " +
        "the session's own modified_at.",
    );
    notes.push(
      "sessions.content_digest comes from run_preparation selections (the source_digest a run read), " +
        "so it is present only for sessions some run prepared.",
    );
  }

  // -------------------------------------------------------------- records
  const recordIds = new Set<string>();
  const revisions = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM frontier_revision`)) {
    revisions.set(text(row["entity_id"]), row);
  }
  const recordColumns = [
    "id", "kind", "root_id", "supersedes_id", "seq", "parent_id", "run_id", "recipe_id",
    "recipe_version", "actor_kind", "actor_id", "title", "created_at", "payload",
  ] as const;
  const recordRows: SqlParam[][] = [];
  const recordRuns = new Map<string, string>();
  const titleFields: Record<string, string> = {
    hypothesis: "statement",
    observation: "claim",
    finding: "title",
    proposal: "title",
  };
  for (const kind of ["hypothesis", "observation", "finding", "proposal"] as const) {
    const source = rowsOf(durable, `SELECT * FROM frontier_${kind}`);
    const staged: { seq: number; values: SqlParam[] }[] = [];
    for (const row of source) {
      const id = text(row["id"]);
      recordIds.add(id);
      const revision = revisions.get(id);
      const document = payload(row["payload_json"]);
      const runId = text(row["run_id"]);
      recordRuns.set(id, runId);
      const seq = revision === undefined ? 0 : count(revision["seq"]);
      const actorKind = revision === undefined ? "run" : text(revision["actor_kind"]);
      staged.push({
        seq,
        values: [
          id,
          kind,
          revision === undefined ? id : text(revision["root_id"]),
          revision === undefined ? null : blank(revision["supersedes_id"]),
          seq,
          kind === "observation" ? blank(row["hypothesis_id"]) : null,
          blank(runId),
          kind === "observation" ? blank(row["recipe_id"]) : null,
          kind === "observation" ? real(row["recipe_version"]) : null,
          actorKind === "run" || actorKind === "operator" || actorKind === "engine" ? actorKind : "engine",
          revision === undefined ? runId : text(revision["actor_id"]),
          summarize(field(document, titleFields[kind] ?? "title")),
          text(row["created_at"]),
          JSON.stringify({ ...document, schema: count(row["schema_version"]) }),
        ],
      });
    }
    // A revision references the wording it supersedes, and the target's foreign key is immediate,
    // so an ancestor is written before its descendant.
    staged.sort((left, right) => left.seq - right.seq);
    for (const item of staged) recordRows.push(item.values);
  }
  for (const row of recordRows) {
    if (typeof row[3] === "string" && !recordIds.has(row[3])) row[3] = null;
  }
  plans.push({
    table: "records",
    source: "durable.db:frontier_hypothesis+observation+finding+proposal × frontier_revision",
    columns: recordColumns,
    rows: recordRows,
  });

  // -------------------------------------------------------------- edges
  // One vocabulary, the rewrite's: the Go `evidence` is `cites` and the Go `inspired_by` is
  // `derived_from`. A record under an entity is a filings row, never an `about` edge.
  const edgeKinds: Record<string, string> = {
    evidence: "cites",
    inspired_by: "derived_from",
    addresses: "addresses",
    duplicates: "duplicates",
  };
  const edgeColumns = [
    "id", "kind", "from_kind", "from_id", "to_kind", "to_id", "position", "note",
    "actor_kind", "actor_id", "created_at",
  ] as const;
  const edgeRows: SqlParam[][] = [];
  const edgeSeen = new Set<string>();
  let unresolvedCites = 0;
  const unresolvedSessions = new Set<string>();
  const pushEdge = (
    id: string, kind: string, fromKind: string, fromId: string, toKind: string, toId: string,
    position: number | null, note: string | null, actorKind: string, actorId: string, createdAt: string,
  ): void => {
    const key = `${kind}\u0000${fromKind}\u0000${fromId}\u0000${toKind}\u0000${toId}`;
    if (edgeSeen.has(key)) return;
    edgeSeen.add(key);
    edgeRows.push([id, kind, fromKind, fromId, toKind, toId, position, note, actorKind, actorId, createdAt]);
  };
  for (const row of rowsOf(durable, `SELECT * FROM reference_edge`)) {
    const goKind = text(row["edge_kind"]);
    const kind = edgeKinds[goKind] ?? goKind;
    let target = text(row["to_id"]);
    if (kind === "cites") {
      const selector = uidToSelector.get(target);
      if (selector === undefined) {
        unresolvedCites += 1;
        unresolvedSessions.add(target);
      } else {
        target = selector;
      }
    }
    pushEdge(
      text(row["id"]), kind, text(row["from_kind"]), text(row["from_id"]), text(row["to_kind"]), target,
      null, blank(field(payload(row["payload_json"]), "note")),
      text(row["actor_kind"]), text(row["actor_ref"]), text(row["created_at"]),
    );
  }
  for (const row of rowsOf(durable, `SELECT * FROM frontier_hypothesis_link`)) {
    pushEdge(
      text(row["id"]), text(row["link_type"]), "hypothesis", text(row["from_id"]),
      "hypothesis", text(row["to_id"]), null, blank(field(payload(row["payload_json"]), "note")),
      "run", "", text(row["created_at"]),
    );
  }
  const join = (
    sql: string, kind: string, fromKind: string, fromColumn: string, toKind: string, toColumn: string,
  ): void => {
    for (const row of rowsOf(durable, sql)) {
      const from = text(row[fromColumn]);
      const to = text(row[toColumn]);
      pushEdge(
        minted("edg", kind, fromKind, from, toKind, to), kind, fromKind, from, toKind, to,
        count(row["position"]), null, "run", recordRuns.get(from) ?? "", text(row["created_at"]),
      );
    }
  };
  join(
    `SELECT j.*, f.created_at FROM frontier_finding_observation j JOIN frontier_finding f ON f.id = j.finding_id`,
    "consolidates", "finding", "finding_id", "observation", "observation_id",
  );
  join(
    `SELECT j.*, p.created_at FROM frontier_proposal_finding j JOIN frontier_proposal p ON p.id = j.proposal_id`,
    "addresses", "proposal", "proposal_id", "finding", "finding_id",
  );
  join(
    `SELECT j.*, p.created_at FROM frontier_proposal_hypothesis j JOIN frontier_proposal p ON p.id = j.proposal_id`,
    "addresses", "proposal", "proposal_id", "hypothesis", "hypothesis_id",
  );
  plans.push({
    table: "edges",
    source: "durable.db:reference_edge+frontier_hypothesis_link+finding_observation+proposal_finding+proposal_hypothesis",
    columns: edgeColumns,
    rows: edgeRows,
  });
  if (unresolvedCites > 0) {
    notes.push(
      `${String(unresolvedCites)} \`cites\` edges across ${String(unresolvedSessions.size)} sessions keep the ` +
        "raw 64-hex shared-catalog session uid as to_id because no catalog row derives to it (the session " +
        "was rotated out of the local catalog, or --deployment/--host were not given). The uid is sha256 " +
        "of the length-prefixed deployment|host|harness|source_id.",
    );
  }

  // -------------------------------------------------------------- status, rulings, filings
  const statusRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM frontier_status_event ORDER BY seq`)) {
    const recordId = text(row["hypothesis_id"]);
    if (!recordIds.has(recordId)) continue;
    const actorKind = text(row["actor_kind"]);
    statusRows.push([
      text(row["id"]), recordId, count(row["seq"]), text(row["status"]), blank(row["run_id"]),
      actorKind === "" ? "run" : actorKind, text(row["actor_id"]) || text(row["run_id"]),
      blank(field(payload(row["payload_json"]), "note")), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "status_events",
    source: "durable.db:frontier_status_event",
    columns: ["id", "record_id", "seq", "status", "run_id", "actor_kind", "actor_id", "reason", "recorded_at"],
    rows: statusRows,
  });

  const dispositionRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM frontier_disposition ORDER BY seq`)) {
    const recordId = text(row["subject_id"]);
    if (!recordIds.has(recordId)) continue;
    dispositionRows.push([
      text(row["id"]), recordId, count(row["seq"]), text(row["disposition"]),
      blank(row["duplicate_of_id"]), blank(field(payload(row["payload_json"]), "note")),
      blank(row["context_id"]), text(row["reviewer_id"]), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "dispositions",
    source: "durable.db:frontier_disposition",
    columns: [
      "id", "record_id", "seq", "disposition", "duplicate_of_id", "note", "context_id",
      "actor_id", "recorded_at",
    ],
    rows: dispositionRows,
  });

  const filingIds = new Set<string>();
  const filingRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM frontier_filing ORDER BY created_at`)) {
    const recordId = text(row["record_id"]);
    if (!recordIds.has(recordId)) continue;
    const id = text(row["id"]);
    filingIds.add(id);
    const author = text(row["author"]);
    const supersedes = blank(row["supersedes_id"]);
    filingRows.push([
      id, recordId, text(row["entity_id"]), field(payload(row["payload_json"]), "rationale"),
      author === "operator" || author === "run" || author === "heuristic" ? author : "run",
      text(row["author_id"]), count(row["heuristic"]), count(row["withdrawn"]),
      supersedes !== null && filingIds.has(supersedes) ? supersedes : null, text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "filings",
    source: "durable.db:frontier_filing",
    columns: [
      "id", "record_id", "entity_id", "rationale", "author_kind", "author_id", "heuristic",
      "withdrawn", "supersedes_id", "created_at",
    ],
    rows: filingRows,
  });

  // -------------------------------------------------------------- evaluation
  const claims = new Map<string, Row>();
  const claimRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM evaluation_claim ORDER BY created_at`)) {
    const id = text(row["id"]);
    claims.set(id, row);
    const finishedAt = blank(row["finished_at"]);
    claimRows.push([
      id, text(row["subject_id"]), text(row["role"]), text(row["lane"]), text(row["policy_version"]),
      null, blank(row["run_id"]), count(row["fence"]), real(row["reserved_cost"]) ?? 0,
      finishedAt === null ? null : real(row["finished_cost"]),
      text(row["created_at"]), text(row["expires_at"]), finishedAt, null,
    ]);
  }
  plans.push({
    table: "claims",
    source: "durable.db:evaluation_claim",
    columns: [
      "id", "record_id", "role", "lane", "policy_version", "job_id", "run_id", "fence",
      "reserved_cost", "actual_cost", "granted_at", "expires_at", "finished_at", "outcome",
    ],
    rows: claimRows,
  });
  if (claimRows.length > 0) {
    notes.push(
      "claims drops seven Go columns with no home: context_version, seed, input_digest, corrects_id, " +
        "subjects_json, finished_run and finished_fence. The reproducibility triple " +
        "(seed, input_digest, context_version) is what makes a draw replayable, so if replay matters " +
        "add `claims.draw TEXT` holding them as JSON; `day` is derivable from granted_at and " +
        "`finished_run`/`finished_fence` from run_id/fence.",
    );
  }

  const evaluation = rowsOf(durable, `SELECT * FROM evaluation_record ORDER BY seq`);
  const assessmentIds = new Set<string>();
  const assessmentRows: SqlParam[][] = [];
  const feedbackRows: SqlParam[][] = [];
  const policyRows: SqlParam[][] = [];
  for (const row of evaluation) {
    const kind = text(row["kind"]);
    const id = text(row["id"]);
    const document = nested(payload(row["payload_json"]), "record");
    if (kind === "assessment") {
      assessmentIds.add(id);
      const assessment = nested(document, "assessment");
      const assignment = text(row["assignment_id"]);
      const claim = claims.get(assignment);
      const supersedes = blank(row["supersedes_id"]);
      assessmentRows.push([
        id, text(row["subject_id"]), text(row["read_head_id"]) || text(row["subject_id"]),
        text(row["run_id"]), text(row["role"]), blank(field(assessment, "vote")),
        claim === undefined ? null : blank(claim["lane"]), blank(assignment),
        supersedes !== null && assessmentIds.has(supersedes) ? supersedes : null,
        JSON.stringify(assessment), text(row["created_at"]),
      ]);
    } else if (kind === "feedback") {
      feedbackRows.push([
        id, text(row["subject_id"]), text(row["actor_id"]), blank(field(document, "stance")),
        field(document, "reason"), document["question"] === true ? 1 : 0,
        blank(text(row["related_id"]) || field(document, "related_id")), text(row["created_at"]),
      ]);
    } else if (kind === "policy") {
      // The Go policy is snake_case on the wire; the coordinator's PolicySchema is camelCase
      // with a measured default per field, so an unrenamed payload would parse as every default.
      const policy = nested(document, "policy");
      const renamed: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(policy)) {
        renamed[key.replace(/_([a-z])/gu, (_, letter: string) => letter.toUpperCase())] = value;
      }
      policyRows.push([
        field(policy, "version"), count(row["seq"]), text(row["actor_id"]),
        field(document, "reason"), JSON.stringify(renamed), text(row["created_at"]),
      ]);
    }
  }
  plans.push({
    table: "assessments",
    source: "durable.db:evaluation_record(kind=assessment) × evaluation_claim",
    columns: [
      "id", "record_id", "revision_id", "run_id", "role", "vote", "lane", "claim_id",
      "supersedes_id", "payload", "recorded_at",
    ],
    rows: assessmentRows,
  });
  plans.push({
    table: "feedback",
    source: "durable.db:evaluation_record(kind=feedback)",
    columns: ["id", "record_id", "actor_id", "stance", "reason", "question", "related_id", "recorded_at"],
    rows: feedbackRows,
  });
  plans.push({
    table: "policies",
    source: "durable.db:evaluation_record(kind=policy)",
    columns: ["version", "seq", "actor_id", "reason", "payload", "recorded_at"],
    rows: policyRows,
  });
  notes.push(
    "policies is keyed by the policy's own version, so the evaluation_record id (evr_…) that carried " +
      "each edit is dropped. Add `policies.record_id TEXT` to keep it.",
  );
  notes.push(
    "evaluation_record kinds `assignment`, `attempt` and `checkpoint` have no table. An assignment is " +
      "the grant `claims` already holds, but an attempt is how a draw FAILED (state + reason) and a " +
      "checkpoint is the coordinator's coverage mark; both are lost. Add `claims.outcome`'s companion " +
      "`claims.attempt TEXT` (state+reason JSON), or a `claim_events` table if the conductor needs the " +
      "sequence. evaluation_spend/settlement/budget_day are also unmapped: the spend ledger is " +
      "`claims.reserved_cost`/`actual_cost` in the rewrite, but per-day budget rows are not.",
  );

  // -------------------------------------------------------------- the Reality Ledger
  const canonical = new Map<string, string>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_entity_membership ORDER BY seq`)) {
    canonical.set(text(row["entity_id"]), text(row["canonical_id"]));
  }
  const entityIds = new Set<string>();
  const entityRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_entity ORDER BY created_at`)) {
    const id = text(row["id"]);
    entityIds.add(id);
    entityRows.push([
      id, text(row["kind"]), field(payload(row["payload_json"]), "display_name"),
      canonical.get(id) ?? id, "", text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "entities",
    source: "durable.db:reality_entity × reality_entity_membership",
    columns: ["id", "kind", "name", "canonical_id", "created_by", "created_at"],
    rows: entityRows,
  });
  if (entityRows.length > 0) {
    notes.push(
      "entities.created_by is empty: reality_entity records no author. The operator's acceptance of a " +
        "topic plan is what mints an entity and it lives in reality_topic_ruling.actor, which is empty " +
        "in this store. Either fill it from the ruling once rulings exist, or drop NOT NULL.",
    );
  }

  const aliasState = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_alias_event ORDER BY seq`)) {
    aliasState.set(text(row["alias_id"]), row);
  }
  const aliasRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_entity_alias ORDER BY created_at`)) {
    const entityId = text(row["entity_id"]);
    if (!entityIds.has(entityId)) continue;
    const id = text(row["id"]);
    const event = aliasState.get(id);
    // The Go store keyed an alias by a sealed digest; on the hub the value is plain, and the
    // acts resolve a topic by name through value_key, so the key is the normalized value itself.
    const value = field(payload(row["payload_json"]), "value");
    aliasRows.push([
      id, entityId, text(row["alias_kind"]), value,
      value.trim().toLowerCase(),
      event !== undefined && text(event["state"]) === "retired" ? text(event["recorded_at"]) : null,
      text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "aliases",
    source: "durable.db:reality_entity_alias × reality_alias_event",
    columns: ["id", "entity_id", "kind", "value", "value_key", "retired_at", "created_at"],
    rows: aliasRows,
  });

  const factIds = new Set<string>();
  const factRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_fact ORDER BY recorded_at`)) {
    const entityId = text(row["subject_id"]);
    if (!entityIds.has(entityId)) continue;
    const id = text(row["id"]);
    factIds.add(id);
    const document = payload(row["payload_json"]);
    const value = nested(document, "value");
    const supersedes = blank(row["supersedes"]);
    factRows.push([
      id, entityId, text(row["predicate"]),
      typeof value["text"] === "string" ? value["text"] : JSON.stringify(value),
      blank(row["object_id"]), text(row["valid_from"]), blank(row["valid_until"]),
      text(row["observed_at"]), text(row["authority_kind"]), text(row["authority_id"]),
      text(row["confidence"]) || "stated", blank(field(document, "note")),
      supersedes !== null && factIds.has(supersedes) ? supersedes : null, text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "facts",
    source: "durable.db:reality_fact",
    columns: [
      "id", "entity_id", "predicate", "value", "object_id", "valid_from", "valid_until",
      "observed_at", "authority_kind", "authority_id", "confidence", "note", "supersedes_id",
      "recorded_at",
    ],
    rows: factRows,
  });

  const factStatusRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_fact_status ORDER BY seq`)) {
    const factId = text(row["fact_id"]);
    if (!factIds.has(factId)) continue;
    const document = payload(row["payload_json"]);
    factStatusRows.push([
      text(row["id"]), factId, count(row["seq"]), text(row["status"]),
      blank(field(document, "actor")), blank(field(document, "reason")), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "fact_status",
    source: "durable.db:reality_fact_status",
    columns: ["id", "fact_id", "seq", "status", "actor_id", "reason", "recorded_at"],
    rows: factStatusRows,
  });

  const resolutionIds = new Set<string>();
  const resolutionRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_resolution ORDER BY recorded_at`)) {
    const id = text(row["id"]);
    resolutionIds.add(id);
    const kind = text(row["resolution_kind"]);
    resolutionRows.push([
      id, kind === "merge" || kind === "split" || kind === "undo" ? kind : "undo",
      blank(row["reverses_id"]), text(row["actor"]),
      field(payload(row["payload_json"]), "reason"), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "resolutions",
    source: "durable.db:reality_resolution",
    columns: ["id", "kind", "reverses_id", "actor_id", "reason", "recorded_at"],
    rows: resolutionRows,
  });
  const memberRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_resolution_member`)) {
    const resolutionId = text(row["resolution_id"]);
    if (!resolutionIds.has(resolutionId)) continue;
    memberRows.push([resolutionId, text(row["member_role"]), count(row["position"]), text(row["entity_id"])]);
  }
  plans.push({
    table: "resolution_members",
    source: "durable.db:reality_resolution_member",
    columns: ["resolution_id", "role", "position", "entity_id"],
    rows: memberRows,
  });

  const questionWork = new Map<string, Row[]>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_question_work`)) {
    const key = text(row["question_id"]);
    const list = questionWork.get(key);
    if (list === undefined) questionWork.set(key, [row]);
    else list.push(row);
  }
  const questionEntities = new Map<string, string[]>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_question_entity`)) {
    const key = text(row["question_id"]);
    const list = questionEntities.get(key);
    if (list === undefined) questionEntities.set(key, [text(row["entity_id"])]);
    else list.push(text(row["entity_id"]));
  }
  const questionEvidence = new Map<string, string[]>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_question_evidence`)) {
    const key = text(row["question_id"]);
    const list = questionEvidence.get(key);
    if (list === undefined) questionEvidence.set(key, [text(row["item"])]);
    else list.push(text(row["item"]));
  }
  const firstEvent = new Map<string, Row>();
  const questionEventRows: SqlParam[][] = [];
  const events = rowsOf(durable, `SELECT * FROM reality_question_event ORDER BY seq`);
  for (const row of events) {
    const key = text(row["question_id"]);
    if (!firstEvent.has(key)) firstEvent.set(key, row);
  }
  const questionIds = new Set<string>();
  const questionRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_question ORDER BY created_at`)) {
    const id = text(row["id"]);
    questionIds.add(id);
    const document = payload(row["payload_json"]);
    const opened = firstEvent.get(id);
    questionRows.push([
      id, text(row["question_kind"]), text(row["question_class"]), field(document, "prompt"),
      field(document, "why_asked"), blank(row["dedupe_key"]),
      opened === undefined ? "asker" : text(opened["actor"]),
      "",
      JSON.stringify({
        ...document,
        schema: count(row["schema_version"]),
        sensitivity: text(row["sensitivity"]),
        expected_authority: text(row["expected_authority"]),
        avoided_cost: count(row["avoided_cost"]),
        prompted_by_id: blank(row["prompted_by_id"]),
        work: (questionWork.get(id) ?? []).map((item) => ({
          kind: text(item["work_kind"]), id: text(item["work_id"]), blocking: count(item["blocking"]) === 1,
        })),
        entities: questionEntities.get(id) ?? [],
        evidence: questionEvidence.get(id) ?? [],
      }),
      text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "questions",
    source: "durable.db:reality_question × question_work/entity/evidence",
    columns: [
      "id", "kind", "class", "text", "why", "dedupe_key", "raised_by_kind", "raised_by_id",
      "payload", "created_at",
    ],
    rows: questionRows,
  });
  if (questionRows.length > 0) {
    notes.push(
      "questions.raised_by_id is empty and raised_by_kind is the first question_event's actor " +
        "(`asker`): reality_question records who should ANSWER (expected_authority) and never who " +
        "asked. Add `questions.expected_authority TEXT` and either drop raised_by_id's NOT NULL or " +
        "let the asking run write it going forward.",
    );
  }
  for (const row of events) {
    const questionId = text(row["question_id"]);
    if (!questionIds.has(questionId)) continue;
    const document = payload(row["payload_json"]);
    questionEventRows.push([
      text(row["id"]), questionId, count(row["seq"]), text(row["state"]),
      blank(row["actor"]), blank(field(document, "reason")), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "question_events",
    source: "durable.db:reality_question_event",
    columns: ["id", "question_id", "seq", "state", "actor_id", "reason", "recorded_at"],
    rows: questionEventRows,
  });

  const answerRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_answer ORDER BY seq`)) {
    const questionId = text(row["question_id"]);
    if (!questionIds.has(questionId)) continue;
    answerRows.push([
      text(row["id"]), questionId, text(row["author"]), text(row["outcome"]),
      field(payload(row["payload_json"]), "text"), text(row["recorded_at"]),
    ]);
  }
  plans.push({
    table: "answers",
    source: "durable.db:reality_answer",
    columns: ["id", "question_id", "actor_id", "outcome", "text", "recorded_at"],
    rows: answerRows,
  });

  // Plans: a topic plan is keyed on the proposal that carries it, which is the id it keeps; an
  // answer's interpretation is keyed on itself.
  const topicRulings = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_topic_ruling`)) {
    topicRulings.set(text(row["proposal_id"]), row);
  }
  const planRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM reality_topic_plan ORDER BY created_at`)) {
    const proposalId = text(row["proposal_id"]);
    const ruling = topicRulings.get(proposalId);
    const verdict = ruling === undefined ? "" : text(ruling["verdict"]);
    planRows.push([
      proposalId, "topic", "proposal", proposalId, text(row["operation"]), blank(row["subject_key"]),
      JSON.stringify({
        ...payload(row["payload_json"]),
        entity_kind: text(row["entity_kind"]),
        evidence_weight: count(row["evidence_weight"]),
      }),
      "run", recordRuns.get(proposalId) ?? "",
      verdict === "accept" ? "applied" : verdict === "" ? "open" : "declined",
      ruling === undefined ? null : text(ruling["actor"]),
      ruling === undefined ? null : text(ruling["recorded_at"]),
      ruling === undefined ? null : blank(field(payload(ruling["payload_json"]), "reason")),
      ruling === undefined ? null : blank(ruling["entity_id"]),
      text(row["created_at"]),
    ]);
  }
  const planActions = new Map<string, Row[]>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_plan_action ORDER BY position`)) {
    const key = text(row["plan_id"]);
    const list = planActions.get(key);
    if (list === undefined) planActions.set(key, [row]);
    else list.push(row);
  }
  const acceptances = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_plan_acceptance`)) acceptances.set(text(row["plan_id"]), row);
  const rejections = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM reality_plan_rejection`)) rejections.set(text(row["plan_id"]), row);
  for (const row of rowsOf(durable, `SELECT * FROM reality_plan ORDER BY created_at`)) {
    const id = text(row["id"]);
    const accepted = acceptances.get(id);
    const rejected = rejections.get(id);
    const ruled = accepted ?? rejected;
    const actions = planActions.get(id) ?? [];
    planRows.push([
      id, "answer", "question", text(row["question_id"]),
      actions.length === 0 ? "interpret" : text(actions[0]?.["action_kind"]), null,
      JSON.stringify({
        ...payload(row["payload_json"]),
        schema: count(row["schema_version"]),
        answer_id: text(row["answer_id"]),
        interpreter_version: count(row["interpreter_version"]),
        actions: actions.map((action) => ({
          id: text(action["id"]), position: count(action["position"]),
          kind: text(action["action_kind"]), state: text(action["state"]),
          result_id: blank(action["result_id"]), applied_at: blank(action["applied_at"]),
          payload: payload(action["payload_json"]),
        })),
      }),
      "engine", `interpreter/${String(count(row["interpreter_version"]))}`,
      accepted !== undefined ? "applied" : rejected !== undefined ? "declined" : "open",
      ruled === undefined ? null : text(ruled["actor"]),
      ruled === undefined ? null : text(ruled["recorded_at"]),
      ruled === undefined ? null : blank(field(payload(ruled["payload_json"]), "reason")),
      null,
      text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "plans",
    source: "durable.db:reality_topic_plan+reality_plan × plan_action/acceptance/rejection/topic_ruling",
    columns: [
      "id", "kind", "subject_kind", "subject_id", "operation", "dedupe_key", "payload",
      "proposed_by_kind", "proposed_by_id", "state", "ruled_by", "ruled_at", "ruling_reason",
      "result", "created_at",
    ],
    rows: planRows,
  });
  notes.push(
    "a topic plan keeps the proposal id as its plan id, because reality_topic_plan is keyed on the " +
      "proposal and has no id of its own.",
  );

  // -------------------------------------------------------------- runs
  const receipts = new Map<string, Row>();
  for (const row of rowsOf(durable, `SELECT * FROM run_receipt ORDER BY revision`)) {
    receipts.set(text(row["run_id"]), row);
  }
  const runRows: SqlParam[][] = [];
  for (const [runId, row] of receipts) {
    const document = payload(row["payload"]);
    const worker = nested(document, "worker");
    const usage = nested(worker, "Usage");
    const profile = nested(worker, "Profile");
    const checkpoint = nested(document, "checkpoint");
    const timing = nested(document, "timing");
    const recipes = worker["Recipes"];
    const firstRecipe = Array.isArray(recipes) && recipes.length > 0 ? (recipes[0] as Record<string, unknown>) : {};
    const state = field(checkpoint, "state");
    const records = checkpoint["records"];
    const stage = field(checkpoint, "stage");
    const evaluate = stage === "review" || runId.startsWith("eval-") || text(row["authority_kind"]) === "policy";
    runRows.push([
      runId, evaluate ? "evaluate" : "explore", host === "" ? null : host,
      blank(field(worker, "JobID")), blank(field(firstRecipe, "id")),
      profile["id"] === undefined ? null : `${field(profile, "id")}@${String(count(profile["revision"]))}`,
      blank(row["authority_kind"]), blank(row["authority_ref"]),
      preparations.get(text(row["preparation_id"])) ?? null,
      field(timing, "started_at") || text(row["recorded_at"]),
      blank(field(timing, "finished_at")),
      state === "closed" ? "completed" : state === "interrupted" ? "stopped" : null,
      real(usage["cost"]), real(usage["total_tokens"]),
      Array.isArray(records) ? records.length : 0,
      JSON.stringify({ ...document, counts: payload(row["counts"]), receipt_id: text(row["id"]) }),
    ]);
  }
  plans.push({
    table: "runs",
    source: "durable.db:run_receipt(newest revision per run) × run_preparation",
    columns: [
      "id", "kind", "machine_id", "job_id", "recipe_id", "profile", "authority_kind",
      "authority_id", "preparation", "started_at", "finished_at", "closure", "cost_usd",
      "tokens", "records", "payload",
    ],
    rows: runRows,
  });
  notes.push(
    "runs.machine_id is --host (or NULL without it): a Go receipt records the worker and the profile " +
      "but never the machine; the shared catalog carried that. runs.kind is `evaluate` when the " +
      "receipt's stage is `review`, the run id starts with `eval-`, or the authority is `policy`, and " +
      "`explore` otherwise — the Go receipt has no operation field, so P4's scan/archive/prepare runs " +
      "cannot appear. A run's superseded receipt revisions are dropped; only the newest survives, with " +
      "its own id under payload.receipt_id.",
  );

  // -------------------------------------------------------------- steering
  const complaintRows: SqlParam[][] = [];
  for (const row of rowsOf(durable, `SELECT * FROM complaint ORDER BY seq`)) {
    const document = payload(row["payload_json"]);
    complaintRows.push([
      text(row["id"]), text(row["root_id"]), blank(row["ancestor_id"]), count(row["seq"]),
      "operator", text(row["operator_id"]), null, null, field(document, "text"), text(row["created_at"]),
    ]);
  }
  plans.push({
    table: "steering",
    source: "durable.db:complaint",
    columns: [
      "id", "root_id", "reply_to_id", "seq", "actor_kind", "actor_id", "target_kind",
      "target_id", "text", "recorded_at",
    ],
    rows: complaintRows,
  });

  // -------------------------------------------------------------- what the crossing leaves behind
  const orphaned = durable
    .query<{ c: number }, []>(`SELECT count(*) AS c FROM disposition_proposal`)
    .get();
  if (orphaned !== null && orphaned.c > 0) {
    notes.push(
      `disposition_proposal holds ${String(orphaned.c)} rows with no home: internal/disposition's ` +
        "proposed next actions (draft-issue, propose-fact, store-memory, ask-operator, " +
        "develop-further, keep-going) — Babel's actionable output, which the operator accepts or " +
        "declines. `plans` is the right shape (subject, operation, payload, state, ruled_by/at/reason, " +
        "result) but its CHECK forbids the kind. Add `'action'` to plans.kind's CHECK and I will " +
        "import them as kind=action, subject_kind=the record kind, operation=the disposition kind, " +
        "state from disposition_ledger. disposition_invitation (#87's instruction-free nudges) and " +
        "frontier_refinement_request need the same decision.",
    );
  }
  notes.push(
    "reality_focus_ruleset has no table (plan §3 lists `focus_rules`; schema.ts has none). It is " +
      "empty in this store, so nothing is lost today.",
  );
  notes.push(
    "frontier_duplicate_warning's 151 rows are already the 151 `duplicates` edges in reference_edge, " +
      "so they are not imported twice. review_queue, explore_commit, run_lease, title_inferred and " +
      "every sync_* table are process state the rewrite retires.",
  );

  return { plans, notes };
}

// ---------------------------------------------------------------------------- writing

/** Creates the store's tables when the target is empty. Reports whether it had to. */
export async function ensureSchema(db: PluginDatabase): Promise<boolean> {
  const existing = await db.query<{ name: string }>(
    `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'records'`,
  );
  if (existing.length > 0) return false;
  for (let start = 0; start < SCHEMA_V1.length; start += MAX_SQL_BATCH_STATEMENTS) {
    const slice = SCHEMA_V1.slice(start, start + MAX_SQL_BATCH_STATEMENTS);
    await db.batch(slice.map((sql) => ({ sql })));
  }
  return true;
}

/** One table's rows, written in transactions of `CHUNK_ROWS`, idempotent on the primary key. */
async function writeTable(db: PluginDatabase, plan: TablePlan): Promise<void> {
  if (plan.rows.length === 0) return;
  const perStatement = Math.max(1, Math.floor(MAX_SQL_PARAMS / plan.columns.length));
  const head = `INSERT OR IGNORE INTO ${plan.table} (${plan.columns.join(", ")}) VALUES `;
  const tuple = `(${plan.columns.map(() => "?").join(", ")})`;
  for (let start = 0; start < plan.rows.length; start += CHUNK_ROWS) {
    const chunk = plan.rows.slice(start, start + CHUNK_ROWS);
    const statements: SqlStatement[] = [];
    for (let at = 0; at < chunk.length; at += perStatement) {
      const slice = chunk.slice(at, at + perStatement);
      const params: SqlParam[] = [];
      for (const row of slice) for (const value of row) params.push(value);
      statements.push({ sql: head + slice.map(() => tuple).join(", "), params });
    }
    for (let at = 0; at < statements.length; at += MAX_SQL_BATCH_STATEMENTS) {
      await db.batch(statements.slice(at, at + MAX_SQL_BATCH_STATEMENTS));
    }
  }
}

/** Writes every table in foreign-key order and upserts the crossing's own ledger. */
export async function applyPlan(
  db: PluginDatabase,
  plans: readonly TablePlan[],
  now: () => string,
): Promise<void> {
  const at = now();
  for (const plan of plans) {
    await writeTable(db, plan);
    await db.run(
      `INSERT INTO imports (id, source, table_name, rows, imported_at) VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET source = excluded.source, rows = excluded.rows,
       imported_at = excluded.imported_at`,
      [minted("imp", plan.table), plan.source, plan.table, plan.rows.length, at],
    );
  }
}

// ---------------------------------------------------------------------------- the command

/** `--into` names the hub's data directory, or the `data.db` the engine would open inside it. */
export function resolveTarget(into: string): { dataDir: string; path: string } {
  const absolute = isAbsolute(into) ? into : resolve(into);
  const suffix = `${sep}${join("plugins", BABEL_PLUGIN_ID, "data.db")}`;
  if (absolute.endsWith(suffix)) {
    const dataDir = absolute.slice(0, absolute.length - suffix.length);
    return { dataDir, path: absolute };
  }
  if (absolute.endsWith(".db")) {
    throw new Error(
      `--into ${into} is not a path the engine would open: a plugin's database is ` +
        `<dataDir>/plugins/${BABEL_PLUGIN_ID}/data.db (ADR 0034). Pass the data directory, or that path.`,
    );
  }
  return { dataDir: absolute, path: pluginDatabasePath(absolute, BABEL_PLUGIN_ID) };
}

const USAGE = `bun tools/import.ts --from <durable.db> [--catalog <catalog.db>] --into <data.db> [options]

  --from <path>        the Go per-machine store (read-only)
  --catalog <path>     the Go local session catalog (read-only); needs --host
  --into <path>        the hub data directory, or <dataDir>/plugins/${BABEL_PLUGIN_ID}/data.db
  --host <id>          the operator-assigned machine id the catalog's sessions belong to
  --deployment <id>    with --host, resolves a cited session's catalog digest back to its selector
  --dry-run            map everything and print the counts; write nothing
  --max-bytes <n>      the target's page cap; defaults to the manifest's 1 GiB request
`;

function parse(argv: readonly string[]): Record<string, string | true> {
  const flags: Record<string, string | true> = {};
  for (let at = 0; at < argv.length; at += 1) {
    const token = argv[at] ?? "";
    if (!token.startsWith("--")) throw new Error(`unexpected argument ${token}\n\n${USAGE}`);
    const name = token.slice(2);
    const next = argv[at + 1];
    if (next === undefined || next.startsWith("--")) flags[name] = true;
    else {
      flags[name] = next;
      at += 1;
    }
  }
  return flags;
}

function value(flags: Record<string, string | true>, name: string): string | undefined {
  const found = flags[name];
  return typeof found === "string" ? found : undefined;
}

export async function main(argv: readonly string[]): Promise<number> {
  const flags = parse(argv);
  if (flags["help"] === true) {
    process.stdout.write(USAGE);
    return 0;
  }
  const from = value(flags, "from");
  if (from === undefined) {
    process.stderr.write(`--from <durable.db> is required\n\n${USAGE}`);
    return 2;
  }
  const dryRun = flags["dry-run"] === true;
  const into = value(flags, "into");
  if (!dryRun && into === undefined) {
    process.stderr.write(`--into <data.db> is required unless --dry-run\n\n${USAGE}`);
    return 2;
  }

  const { plans, notes } = planImport({
    from,
    catalog: value(flags, "catalog"),
    host: value(flags, "host"),
    deployment: value(flags, "deployment"),
  });

  let total = 0;
  for (const plan of plans) total += plan.rows.length;
  const width = Math.max(...plans.map((plan) => plan.table.length));
  for (const plan of plans) {
    process.stdout.write(
      `${plan.table.padEnd(width)}  ${String(plan.rows.length).padStart(7)}  ${plan.source}\n`,
    );
  }
  process.stdout.write(`${"total".padEnd(width)}  ${String(total).padStart(7)}\n`);
  if (notes.length > 0) {
    process.stdout.write("\nnotes:\n");
    for (const note of notes) process.stdout.write(`  - ${note}\n`);
  }
  if (dryRun || into === undefined) return 0;

  const target = resolveTarget(into);
  const requested = value(flags, "max-bytes");
  const db = openPluginDatabase({
    dataDir: target.dataDir,
    pluginId: BABEL_PLUGIN_ID,
    maxBytes: requested === undefined ? MANIFEST_DATABASE_MAX_BYTES : Number(requested),
  });
  try {
    const created = await ensureSchema(db);
    process.stdout.write(`\n${created ? "created" : "found"} the store at ${target.path}\n`);
    await applyPlan(db, plans, () => new Date().toISOString());
    process.stdout.write(`wrote ${String(total)} rows\n`);
  } finally {
    db.close();
  }
  return 0;
}

if (import.meta.main) {
  process.exitCode = await main(process.argv.slice(2));
}
