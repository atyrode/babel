import type { SqlParam, SqlStatement } from "@manifold/plugin";
import {
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  JOB_OUTPUT_FILES,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  ReceiptSchema,
  type Receipt,
} from "../contract.ts";
import type { Assignment, Coordinator, Gap, Policy, Stop } from "../store/coordinator.ts";
import type { BabelStore } from "../store/store.ts";

/*
  THE CONDUCTOR — Babel's loop, on the hub (plan §4: `babel conductor run` becomes a server-half
  loop over `engine.jobs`, and the loop's policy is store state rather than a process).

  One `tick()` is one cycle, and a cycle is four things in a fixed order:

    1. the policy in force decides whether the loop exists at all. `enabled` is the activation
       gate (§14): a disabled policy registers nothing, draws nothing, ingests nothing and
       spends nothing. Turning Babel off is one operator record, not a migration.
    2. the schedule is reconciled. `engine.jobs.schedule` is the only cadence primitive a plugin
       has, and it schedules a JOB on a machine — so the loop's beat is the cheapest useful job
       Babel owns, `scan`: no model spend, a fresh catalog every cadence, and a settlement that
       wakes the hub. Drawing stays here, where the coordinator's lanes and ceilings govern
       every model dollar; a fixed `explore` registered at schedule time would spend outside them.
    3. finished jobs are ingested — the ones the loop requested and the beat's own, which nobody
       requested. A run's records, edges, statuses, assessments, filings, plans, questions,
       steering replies and sessions arrive as ONE sealed output holding the files
       `JOB_OUTPUT_FILES` names, each a list of rows in the store's own shapes; ingestion is a
       `batch` of `INSERT OR IGNORE` (append-only tables are never rewritten) and an upsert for
       the two tables that are a projection rather than an act — `sessions` and `runs`. Every
       insert is keyed by the row's own identifier, so ingesting the same output twice writes
       the same rows and changes nothing.
    4. the receipt settles the claim. What a review cost is what the machine recorded, not what
       the draw reserved; a job that failed, was cancelled or wrote no receipt settles `failed`
       at the cost it did incur, so a lane's allowance is never held by a dead worker.

  What the loop does NOT own: a timer. A plugin may not poll as an alternate scheduler
  (`docs/PLUGINS.md`), and a server half has no clock of its own — so `tick()` is called by the
  plugin's own dispatch (any door) and by the job-settled lifecycle hook for this plugin's jobs.
  Every step above is idempotent, which is what makes that safe: two ticks in the same second
  do the work of one, and a cycle that is retried re-derives the same assignment ids, the same
  job ids and the same claim.
*/

// ---------------------------------------------------------------------------- the jobs slice

/** In-realm the engine answers directly, isolated it answers over a promise; the loop takes both. */
export type Awaitable<T> = T | Promise<T>;

/** The engine's own address of a job and of one of its sealed outputs (`ManifoldRef`). */
export interface JobRef {
  readonly kind: "job";
  readonly machineId: string;
  readonly operationId: string;
  readonly jobId: string;
}
export interface OutputRef {
  readonly kind: "output";
  readonly machineId: string;
  readonly operationId: string;
  readonly jobId: string;
  readonly outputId: string;
}

export type JobState =
  | "queued"
  | "admitted"
  | "start-committed"
  | "started"
  | "exited"
  | "interrupted"
  | "cancelled"
  | "refused";

/** One sealed output as a result names it; `bytes` is the archive's, not the files' sum. */
export interface JobOutput {
  readonly outputId: string;
  readonly name: string;
  readonly bytes: number;
  readonly files: number;
}

export interface JobRunState {
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly state: JobState;
  readonly result: {
    readonly state: JobState;
    readonly exitCode: number | null;
    readonly reason: string | null;
    readonly outputs: readonly JobOutput[];
  } | null;
}

/** What `describe` answers, narrowed to the four facts that decide where a job may run. */
export interface MachineReadiness {
  readonly connected: boolean;
  readonly operations?:
    | Readonly<Record<string, { readonly ready: boolean; readonly reason: string | null }>>
    | undefined;
  readonly installation: {
    readonly revision: string;
    readonly artifactSha256: string;
    readonly enabled: boolean;
    readonly ready: boolean;
  } | null;
}

export interface JobLimits {
  readonly timeoutMs: number;
  readonly memoryBytes: number;
  readonly processes: number;
  readonly outputBytes: number;
}

/** A job request as the loop makes one: the engine fills in authority, permit and digest. */
export interface JobLaunch {
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly input: Readonly<Record<string, string | number | boolean>>;
  readonly outputs: readonly {
    readonly name: string;
    readonly locationId: string;
    readonly components: readonly string[];
  }[];
  readonly limits?: JobLimits | undefined;
  readonly installationRevision?: string | undefined;
  readonly artifactSha256?: string | undefined;
}

/** `JobScheduleTiming`, restated so the loop compiles against the slice rather than the host. */
export interface ScheduleTiming {
  readonly scheduleId: string;
  readonly revision: string;
  readonly firstNominalAt: number;
  readonly intervalMs: number;
  readonly deadlineMs: number;
  readonly expiresAt: number;
  readonly offlinePolicy: "skip" | "coalesce-one";
}

export interface ScheduleRow extends ScheduleTiming {
  readonly machineId: string;
  readonly operationId: string;
}

/**
 * The eight verbs the loop uses, and no more: `engine.jobs` in-realm (`PluginJobContext`) and
 * the kit's `GuestJobs` with its schedule verbs both satisfy it, and a test satisfies it with a
 * fake. Everything returns `Awaitable` because in-realm the engine answers synchronously and
 * across the isolate boundary it answers with a promise (ADR 0016's one contract, both ends).
 */
export interface JobsSlice {
  describe(args: { machineId: string; pluginId: string }): Awaitable<MachineReadiness>;
  execute(args: JobLaunch): Awaitable<JobRunState>;
  status(node: JobRef): Awaitable<JobRunState>;
  listRuns(args: {
    machineId: string;
    operationId?: string | undefined;
    limit?: number | undefined;
  }): Awaitable<{ runs: readonly { job: JobRunState | null }[] }>;
  output(args: {
    node: OutputRef;
    offset: number;
    maxBytes: number;
  }): Awaitable<{ data: string; eof: boolean }>;
  schedule(args: JobLaunch & ScheduleTiming): Awaitable<unknown>;
  schedules(): Awaitable<readonly ScheduleRow[]>;
  disableSchedule(args: { scheduleId: string; revision: string }): Awaitable<unknown>;
}

// ---------------------------------------------------------------------------- what a run needs

export interface Recipe {
  readonly id: string;
  readonly version: number;
  readonly title: string;
  readonly body: string;
}

/**
 * What a drawn review needs that is neither store state nor a coordinator decision: the engine
 * to drive, the profile it runs under, the caps it runs inside, and the cookbook recipe each
 * role performs. It is configuration the plugin's wiring holds, passed in rather than read
 * here, for the reason the Go loop never configured a profile either — a loop that could mint
 * its own profile or its own recipe would be choosing its own spending limit and its own
 * question.
 */
export interface RunPlan {
  readonly engine: { readonly binary: string; readonly args?: readonly string[] | undefined };
  readonly profile: { readonly id: string; readonly revision: number };
  readonly caps: {
    readonly perRunUsd: number;
    readonly toolCalls: number;
    readonly idleMs: number;
    readonly handshakeMs: number;
  };
  /** The recipe a role performs. A role with none is never dispatched, and says so. */
  readonly recipes: Readonly<Record<string, Recipe>>;
  readonly requireContainment: boolean;
  readonly limits: JobLimits;
}

export interface ConductorDeps {
  readonly store: BabelStore;
  readonly coordinator: Coordinator;
  readonly jobs: JobsSlice;
  readonly plan: RunPlan;
  readonly now: () => number;
}

// ---------------------------------------------------------------------------- the report

export type ScheduleState = "registered" | "kept" | "unregistered" | "absent";

export interface RequestedJob {
  readonly runId: string;
  readonly jobId: string;
  readonly machineId: string;
  readonly claimId: string;
  readonly recordId: string;
  readonly role: string;
  readonly lane: string;
}

export interface IngestedRun {
  readonly runId: string;
  readonly jobId: string;
  readonly closure: string;
  readonly costUsd: number;
  /** Rows written per output file, by the file's own name. */
  readonly rows: Readonly<Record<string, number>>;
  readonly skipped: number;
}

export interface SettledClaim {
  readonly claimId: string;
  readonly outcome: "completed" | "failed" | "skipped";
  readonly cost: number;
  readonly overrun: boolean;
  readonly refused: string | null;
}

export interface RefusedDraw {
  readonly assignmentId: string;
  readonly recordId: string;
  readonly reason: string;
  readonly detail: string;
}

export interface TickReport {
  readonly at: number;
  /** The cycle's own run id: what this tick's draws and their claims are accounted to. */
  readonly cycleRunId: string;
  readonly policyVersion: string;
  readonly enabled: boolean;
  readonly schedule: ScheduleState;
  readonly requested: readonly RequestedJob[];
  readonly ingested: readonly IngestedRun[];
  readonly settled: readonly SettledClaim[];
  readonly refused: readonly RefusedDraw[];
  /** Why drawing stopped; null only when the cycle never drew (a disabled policy). */
  readonly stop: Stop | null;
  readonly gaps: readonly Gap[];
  /** Jobs still in flight when the cycle ended. */
  readonly pending: number;
  readonly notes: readonly string[];
}

export interface Conductor {
  tick(): Promise<TickReport>;
}

// ---------------------------------------------------------------------------- constants

/** The loop's one schedule. Its revision is the policy version, so a policy change re-registers. */
export const CONDUCTOR_SCHEDULE_ID = `${BABEL_PLUGIN_ID}.conductor`;
/** The beat: cheap, model-free, useful, and its settlement is what wakes the hub. */
export const BEAT_OPERATION: string = OPERATIONS.scan;
/** A schedule is registered for a bounded life and renewed by the loop that still wants it. */
export const SCHEDULE_LIFETIME_MS = 30 * 24 * 60 * 60 * 1000;
/** How far back a tick looks for beat jobs nobody has ingested yet. */
const BEAT_RUNS_SCANNED = 20;

/** The engine serves an output in chunks of at most this many bytes (`job-doors.ts`). */
const OUTPUT_CHUNK_BYTES = 65536;
/** An output larger than this is refused rather than buffered; the note says what was lost. */
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;
/** `MAX_SQL_BATCH_STATEMENTS`: a batch holds the write lock, so ingestion writes it in pieces. */
const MAX_BATCH_STATEMENTS = 256;
/** The states a job is done in; anything else is still in flight. */
const TERMINAL_STATES: Record<string, true> = {
  exited: true,
  interrupted: true,
  cancelled: true,
  refused: true,
};

// ---------------------------------------------------------------------------- ingestion tables

interface TableIngest {
  readonly table: string;
  readonly columns: readonly string[];
  /**
   * `ignore` is every table that records an act: a row is written once under its own id and a
   * second delivery of it is the same row. `upsert` is the two projections — a session's
   * catalog entry and a run — where a later observation completes an earlier one.
   */
  readonly conflict: "ignore" | "upsert";
  readonly key?: string;
}

const SESSION_COLUMNS = [
  "selector",
  "host",
  "harness",
  "source_id",
  "title",
  "title_provenance",
  "workspace",
  "repository_identity",
  "repository_remote",
  "repository_reason",
  "modified_at",
  "size",
  "cost_usd",
  "total_tokens",
  "turns",
  "tool_errors",
  "content_digest",
  "snapshot_id",
  "archived_at",
  "seen_at",
] as const;

const RUN_COLUMNS = [
  "id",
  "kind",
  "machine_id",
  "job_id",
  "recipe_id",
  "profile",
  "authority_kind",
  "authority_id",
  "preparation",
  "started_at",
  "finished_at",
  "closure",
  "cost_usd",
  "tokens",
  "records",
  "payload",
] as const;

/** One output file, one table. */
const INGEST: Record<string, TableIngest> = {
  [JOB_OUTPUT_FILES.sessions]: {
    table: "sessions",
    columns: SESSION_COLUMNS,
    conflict: "upsert",
    key: "selector",
  },
  [JOB_OUTPUT_FILES.records]: {
    table: "records",
    columns: [
      "id",
      "kind",
      "root_id",
      "supersedes_id",
      "seq",
      "parent_id",
      "run_id",
      "recipe_id",
      "recipe_version",
      "actor_kind",
      "actor_id",
      "title",
      "created_at",
      "payload",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.edges]: {
    table: "edges",
    columns: [
      "id",
      "kind",
      "from_kind",
      "from_id",
      "to_kind",
      "to_id",
      "position",
      "note",
      "actor_kind",
      "actor_id",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.statusEvents]: {
    table: "status_events",
    columns: [
      "id",
      "record_id",
      "seq",
      "status",
      "run_id",
      "actor_kind",
      "actor_id",
      "reason",
      "recorded_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.assessments]: {
    table: "assessments",
    columns: [
      "id",
      "record_id",
      "revision_id",
      "run_id",
      "role",
      "vote",
      "lane",
      "claim_id",
      "supersedes_id",
      "payload",
      "recorded_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.filings]: {
    table: "filings",
    columns: [
      "id",
      "record_id",
      "entity_id",
      "rationale",
      "author_kind",
      "author_id",
      "heuristic",
      "withdrawn",
      "supersedes_id",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.questions]: {
    table: "questions",
    columns: [
      "id",
      "kind",
      "class",
      "text",
      "why",
      "dedupe_key",
      "raised_by_kind",
      "raised_by_id",
      "payload",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.plans]: {
    table: "plans",
    columns: [
      "id",
      "kind",
      "subject_kind",
      "subject_id",
      "operation",
      "dedupe_key",
      "payload",
      "proposed_by_kind",
      "proposed_by_id",
      "state",
      "ruled_by",
      "ruled_at",
      "ruling_reason",
      "result",
      "created_at",
    ],
    conflict: "ignore",
  },
  [JOB_OUTPUT_FILES.steeringReplies]: {
    table: "steering",
    columns: [
      "id",
      "root_id",
      "reply_to_id",
      "seq",
      "actor_kind",
      "actor_id",
      "target_kind",
      "target_id",
      "text",
      "recorded_at",
    ],
    conflict: "ignore",
  },
};

/** Subjects before the rows that reference them; the receipt is read last, as it is written last. */
const INGEST_ORDER: readonly string[] = [
  JOB_OUTPUT_FILES.sessions,
  JOB_OUTPUT_FILES.records,
  JOB_OUTPUT_FILES.edges,
  JOB_OUTPUT_FILES.statusEvents,
  JOB_OUTPUT_FILES.assessments,
  JOB_OUTPUT_FILES.filings,
  JOB_OUTPUT_FILES.questions,
  JOB_OUTPUT_FILES.plans,
  JOB_OUTPUT_FILES.steeringReplies,
];

// ---------------------------------------------------------------------------- the tar reader

/**
 * The engine seals every bound output as a POSIX ustar archive (`agent/src/job-outputs.ts`:
 * regular files only, no extension records), so reading a run's files is reading its members.
 * Nothing here tolerates a path: a member is taken by its base name, which is the only thing
 * the file table is keyed by, and a member that is not a regular file is not a file.
 */
export interface TarMember {
  readonly name: string;
  readonly body: Uint8Array;
}

const TEXT = new TextDecoder();

function field(header: Uint8Array, at: number, length: number): string {
  let end = at;
  const limit = at + length;
  while (end < limit && header[end] !== 0) end += 1;
  return TEXT.decode(header.subarray(at, end)).trim();
}

export function tarMembers(bytes: Uint8Array): TarMember[] {
  const members: TarMember[] = [];
  for (let offset = 0; offset + 512 <= bytes.length; ) {
    const header = bytes.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const digits = field(header, 124, 12);
    const size = digits === "" ? 0 : Number.parseInt(digits, 8);
    if (!Number.isSafeInteger(size) || size < 0 || offset + 512 + size > bytes.length) {
      throw new Error(`sealed output is not a readable archive at byte ${String(offset)}`);
    }
    const name = field(header, 0, 100);
    const prefix = field(header, 345, 155);
    const type = header[156];
    offset += 512;
    if (type === 0x30 || type === 0x00) {
      const path = prefix === "" ? name : `${prefix}/${name}`;
      members.push({
        name: path.slice(path.lastIndexOf("/") + 1),
        body: bytes.subarray(offset, offset + size),
      });
    }
    offset += Math.ceil(size / 512) * 512;
  }
  return members;
}

/** Reads one sealed output whole, in the chunks the engine serves. */
async function readOutput(jobs: JobsSlice, node: OutputRef, bytes: number): Promise<Uint8Array> {
  if (bytes > MAX_OUTPUT_BYTES) {
    throw new Error(`output ${node.outputId} is ${String(bytes)} bytes, past what the hub ingests`);
  }
  const chunks: Buffer[] = [];
  let offset = 0;
  for (;;) {
    const chunk = await jobs.output({ node, offset, maxBytes: OUTPUT_CHUNK_BYTES });
    const decoded = Buffer.from(chunk.data, "base64");
    chunks.push(decoded);
    offset += decoded.byteLength;
    if (chunk.eof) break;
    if (decoded.byteLength === 0) {
      throw new Error(`output ${node.outputId} stopped short of its end`);
    }
    if (offset > MAX_OUTPUT_BYTES) {
      throw new Error(`output ${node.outputId} grew past the ingest limit`);
    }
  }
  return Buffer.concat(chunks);
}

// ---------------------------------------------------------------------------- row statements

/**
 * One row into one statement, naming only the columns the row actually carries — a scan that
 * observed no snapshot must not clobber the snapshot an archive recorded, and the way to
 * promise that is never to mention the column. A row carrying a column this table does not
 * have, or a value SQLite cannot hold, is not written at all: a machine half that changed shape
 * is a fault to see in the report, not a half-written row.
 */
function rowStatement(ingest: TableIngest, row: Record<string, unknown>): SqlStatement | null {
  const columns: string[] = [];
  const params: SqlParam[] = [];
  for (const column of ingest.columns) {
    if (!Object.hasOwn(row, column)) continue;
    const value = row[column];
    if (value === null || typeof value === "string" || typeof value === "number") {
      params.push(value);
    } else if (typeof value === "boolean") {
      params.push(value ? 1 : 0);
    } else {
      return null;
    }
    columns.push(column);
  }
  if (columns.length === 0) return null;
  for (const key of Object.keys(row)) if (!ingest.columns.includes(key)) return null;
  const names = columns.join(", ");
  const holes = columns.map(() => "?").join(", ");
  if (ingest.conflict === "ignore") {
    return { sql: `INSERT OR IGNORE INTO ${ingest.table}(${names}) VALUES (${holes})`, params };
  }
  const key = ingest.key ?? "id";
  const updates = columns
    .filter((column) => column !== key)
    .map((column) => `${column} = excluded.${column}`)
    .join(", ");
  return {
    sql:
      `INSERT INTO ${ingest.table}(${names}) VALUES (${holes}) ON CONFLICT(${key}) ` +
      (updates === "" ? "DO NOTHING" : `DO UPDATE SET ${updates}`),
    params,
  };
}

/** The run row a finished job leaves behind: the receipt as the machine wrote it, or its absence. */
function runStatement(
  runId: string,
  target: IngestTarget,
  receipt: Receipt | null,
  rows: Readonly<Record<string, number>>,
): SqlStatement {
  const closure = receipt?.closure ?? target.closure;
  const produced = receipt?.counts["records"] ?? rows[JOB_OUTPUT_FILES.records] ?? 0;
  const values: Record<string, SqlParam | undefined> = {
    id: runId,
    // THE OPERATION ID, never the receipt's word. `kind` is the operation the job ran as: it is
    // what `stop` builds a job node out of and what a surface labels a row by, and both of those
    // speak the engine's namespaced id. The receipt's `kind` is the machine half's own account
    // of which verb its binary ran, it stays inside the receipt payload below, and letting it
    // overwrite this column turned a finished `atyrode.babel.scan` into a bare `scan` — a node
    // no hub can address.
    kind: target.operationId,
    machine_id: target.machineId,
    job_id: target.jobId,
    recipe_id: receipt?.recipeId,
    profile: receipt?.profile === undefined ? undefined : JSON.stringify(receipt.profile),
    preparation:
      receipt?.preparation === undefined ? undefined : JSON.stringify(receipt.preparation),
    started_at: receipt?.startedAt ?? "",
    finished_at: receipt?.finishedAt ?? "",
    closure,
    cost_usd: receipt?.costUsd ?? null,
    tokens: receipt?.tokens ?? null,
    records: produced,
    payload: JSON.stringify(receipt ?? { closure, reason: target.closure }),
  };
  const columns = RUN_COLUMNS.filter((column) => values[column] !== undefined);
  const updates = columns.filter((column) => column !== "id").map((c) => `${c} = excluded.${c}`);
  return {
    sql:
      `INSERT INTO runs(${columns.join(", ")}) VALUES (${columns.map(() => "?").join(", ")}) ` +
      `ON CONFLICT(id) DO UPDATE SET ${updates.join(", ")}`,
    params: columns.map((column) => values[column] ?? null),
  };
}

// ---------------------------------------------------------------------------- ingestion

export interface IngestResult {
  readonly runId: string;
  readonly rows: Record<string, number>;
  readonly skipped: number;
  readonly receipt: Receipt | null;
  readonly notes: string[];
}

export interface IngestTarget {
  /**
   * The run row's id when the hub minted one (a requested job), null for a job nobody
   * requested — the scheduled beat, whose run id the machine half mints and puts in its
   * receipt, because a schedule's input is fixed at registration and cannot carry one.
   */
  readonly runId: string | null;
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly outputs: readonly JobOutput[];
  /** What the engine says became of the job, for a run whose receipt never arrived. */
  readonly closure: string;
}

/**
 * Reads a finished job's sealed outputs and writes them into the store. Idempotent by
 * construction: every act is inserted under its own identifier and ignored when it is already
 * there, the catalog and the run are upserted to the same values, so a second ingestion of the
 * same outputs is a no-op the store cannot tell from the first.
 */
export async function ingestOutputs(
  store: BabelStore,
  jobs: JobsSlice,
  target: IngestTarget,
): Promise<IngestResult> {
  const files = new Map<string, unknown>();
  const notes: string[] = [];
  for (const output of target.outputs) {
    const node: OutputRef = {
      kind: "output",
      machineId: target.machineId,
      operationId: target.operationId,
      jobId: target.jobId,
      outputId: output.outputId,
    };
    const bytes = await readOutput(jobs, node, output.bytes);
    for (const member of tarMembers(bytes)) {
      if (INGEST[member.name] === undefined && member.name !== JOB_OUTPUT_FILES.receipt) {
        notes.push(`${output.name}/${member.name} is not a file this hub ingests`);
        continue;
      }
      try {
        files.set(member.name, JSON.parse(TEXT.decode(member.body)));
      } catch {
        notes.push(`${member.name} is not JSON`);
      }
    }
  }

  const rows: Record<string, number> = {};
  let skipped = 0;
  const statements: SqlStatement[] = [];
  for (const file of INGEST_ORDER) {
    const ingest = INGEST[file];
    const document = files.get(file);
    if (ingest === undefined || document === undefined) continue;
    if (!Array.isArray(document)) {
      notes.push(`${file} is not a list of rows`);
      continue;
    }
    let written = 0;
    for (const row of document) {
      const statement =
        typeof row === "object" && row !== null && !Array.isArray(row)
          ? rowStatement(ingest, row as Record<string, unknown>)
          : null;
      if (statement === null) {
        skipped += 1;
        continue;
      }
      statements.push(statement);
      written += 1;
    }
    rows[file] = written;
  }

  const document = files.get(JOB_OUTPUT_FILES.receipt);
  const parsed = document === undefined ? null : ReceiptSchema.safeParse(document);
  if (parsed !== null && !parsed.success) {
    notes.push(`${JOB_OUTPUT_FILES.receipt} is not a receipt`);
  }
  const receipt = parsed !== null && parsed.success ? parsed.data : null;
  if (receipt !== null && target.runId !== null && receipt.runId !== target.runId) {
    notes.push(`the receipt calls this run ${receipt.runId}, the hub asked for ${target.runId}`);
  }
  const runId = target.runId ?? receipt?.runId ?? `run_${target.jobId}`;

  statements.push(runStatement(runId, target, receipt, rows));
  for (let at = 0; at < statements.length; at += MAX_BATCH_STATEMENTS) {
    await store.db.batch(statements.slice(at, at + MAX_BATCH_STATEMENTS));
  }
  store.touch();
  return { runId, rows, skipped, receipt, notes };
}

// ---------------------------------------------------------------------------- the loop

type PendingRun = { id: string; job_id: string; machine_id: string; kind: string };
type OpenClaim = { id: string; run_id: string; fence: number; reserved_cost: number };
type MachineCount = { machineId: string; cited: number };
type MachineRow = { machineId: string };
type Existing = { id: string };
type RecordRow = {
  id: string;
  kind: string;
  root_id: string;
  parent_id: string | null;
  title: string;
  created_at: string;
  payload: string;
};
type SourceRow = {
  selector: string;
  position: number | null;
  note: string | null;
  digest: string | null;
  snapshot: string | null;
};

export function conductor(deps: ConductorDeps): Conductor {
  const { store, coordinator, jobs, plan } = deps;
  let cycle = 0;

  /** Machines are described once per tick: the answer is the same for every draw in it. */
  async function readiness(
    seen: Map<string, MachineReadiness | null>,
    machineId: string,
  ): Promise<MachineReadiness | null> {
    const known = seen.get(machineId);
    if (known !== undefined) return known;
    let described: MachineReadiness | null = null;
    try {
      described = await jobs.describe({ machineId, pluginId: BABEL_PLUGIN_ID });
    } catch {
      described = null;
    }
    seen.set(machineId, described);
    return described;
  }

  /** Every machine Babel has evidence of: one that holds sessions, or one that has run for it. */
  async function knownMachines(): Promise<string[]> {
    const rows = await store.db.query<MachineRow>(
      `SELECT DISTINCT host AS machineId FROM sessions WHERE host <> ''
       UNION
       SELECT DISTINCT machine_id AS machineId FROM runs
        WHERE machine_id IS NOT NULL AND machine_id <> ''
       ORDER BY machineId`,
    );
    return rows.map((row) => row.machineId);
  }

  /**
   * Where the work belongs: the machine holding the sessions the record cites, because that is
   * where the evidence can be read, and the one that holds most of them first. Failing that, any
   * enrolled machine that is online with the operation ready — a review of a record whose
   * sessions sit on an offline machine is still a review Babel can perform, it just reads what
   * the hub already holds.
   */
  async function machineFor(
    seen: Map<string, MachineReadiness | null>,
    operationId: string,
    recordId: string,
  ): Promise<{ machineId: string; readiness: MachineReadiness } | null> {
    const order: string[] = [];
    if (recordId !== "") {
      const preferred = await store.db.query<MachineCount>(
        `SELECT s.host AS machineId, COUNT(*) AS cited
           FROM edges e JOIN sessions s ON s.selector = e.to_id
          WHERE e.from_id = ? AND e.kind = 'cites' AND e.to_kind = 'session'
          GROUP BY s.host
          ORDER BY cited DESC, s.host`,
        [recordId],
      );
      for (const row of preferred) order.push(row.machineId);
    }
    for (const machineId of await knownMachines()) {
      if (!order.includes(machineId)) order.push(machineId);
    }
    for (const machineId of order) {
      const described = await readiness(seen, machineId);
      if (described === null || !described.connected) continue;
      const installed = described.installation;
      if (installed === null || !installed.enabled || !installed.ready) continue;
      if (described.operations?.[operationId]?.ready === false) continue;
      return { machineId, readiness: described };
    }
    return null;
  }

  /**
   * The blinded target (§E3): what the record says, and nothing about how it has been received.
   * The projection reads the record and the sessions it cites; it never reads a tally, a
   * ranking, a disposition or another assessment, so a blinded role cannot be shown one.
   */
  async function project(recordId: string): Promise<{
    target: Record<string, unknown>;
    sources: Record<string, unknown>[];
  } | null> {
    const found = await store.db.query<RecordRow>(
      `SELECT id, kind, root_id, parent_id, title, created_at, payload FROM records WHERE id = ?`,
      [recordId],
    );
    const record = found[0];
    if (record === undefined) return null;
    let payload: unknown = {};
    try {
      payload = JSON.parse(record.payload);
    } catch {
      payload = {};
    }
    const cited = await store.db.query<SourceRow>(
      `SELECT e.to_id AS selector, e.position AS position, e.note AS note,
              s.content_digest AS digest, s.snapshot_id AS snapshot
         FROM edges e LEFT JOIN sessions s ON s.selector = e.to_id
        WHERE e.from_id = ? AND e.kind = 'cites' AND e.to_kind = 'session'
        ORDER BY e.position, e.to_id`,
      [recordId],
    );
    return {
      target: {
        id: record.id,
        kind: record.kind,
        rootId: record.root_id,
        parentId: record.parent_id ?? "",
        title: record.title,
        createdAt: record.created_at,
        payload,
      },
      sources: cited.map((row) => ({
        kind: "session",
        selector: row.selector,
        digest: row.digest ?? "",
        snapshot: row.snapshot ?? "",
        note: row.note ?? "",
        position: row.position ?? 0,
      })),
    };
  }

  /**
   * The one schedule the loop keeps: the policy's cadence, on a machine that can run the beat.
   *
   * A HOST THAT WILL NOT REGISTER IT IS NOT A REASON TO STOP. The three schedule verbs are the
   * only ones a cycle can be refused for structurally rather than for this policy's sake — a
   * hardened server half reaches none of them (`ISOLATE_CTX_METHODS` serves no `jobs.schedule`),
   * and an authority that has lapsed refuses the other two — so the refusal is recorded as a
   * note and the cycle carries on. The beat is one WAKE; ingesting what has already finished
   * and drawing what the policy allows are the work, and they do not need it.
   */
  async function reconcileSchedule(
    policy: Policy,
    at: number,
    notes: string[],
  ): Promise<ScheduleState> {
    let listed: readonly ScheduleRow[];
    try {
      listed = await jobs.schedules();
    } catch (error) {
      notes.push(`the beat's schedule cannot be read: ${message(error)}`);
      return "absent";
    }
    const registered = listed.filter((row) => row.scheduleId === CONDUCTOR_SCHEDULE_ID);
    if (!policy.enabled) {
      try {
        for (const row of registered) {
          await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
        }
      } catch (error) {
        notes.push(`the beat cannot be unregistered: ${message(error)}`);
        return "kept";
      }
      return registered.length === 0 ? "absent" : "unregistered";
    }
    const intervalMs = Math.max(1, policy.cadenceSeconds) * 1000;
    const current = registered.find(
      (row) =>
        row.revision === policy.version &&
        row.intervalMs === intervalMs &&
        row.expiresAt - at > intervalMs,
    );
    if (current !== undefined) return "kept";
    const host = await machineFor(new Map(), BEAT_OPERATION, "");
    if (host === null) return registered.length === 0 ? "absent" : "kept";
    try {
      for (const row of registered) {
        await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
      }
    } catch (error) {
      notes.push(`the beat cannot be re-registered: ${message(error)}`);
      return "kept";
    }
    const installation = host.readiness.installation;
    try {
      await jobs.schedule({
        jobId: `${CONDUCTOR_SCHEDULE_ID}.${policy.version}`,
        machineId: host.machineId,
        operationId: BEAT_OPERATION,
        // The beat's input is fixed at registration, so it carries no run id: the machine half
        // mints one per occurrence and the receipt is what names it.
        input: {
          [INPUT_FIELD]: JSON.stringify({
            runId: "",
            machineId: host.machineId,
            roots: [],
            harnesses: [],
          }),
        },
        outputs: [
          { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [BEAT_OPERATION] },
        ],
        limits: plan.limits,
        ...(installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            }),
        scheduleId: CONDUCTOR_SCHEDULE_ID,
        revision: policy.version,
        firstNominalAt: at + intervalMs,
        intervalMs,
        deadlineMs: intervalMs,
        expiresAt: at + SCHEDULE_LIFETIME_MS,
        offlinePolicy: "coalesce-one",
      });
    } catch (error) {
      notes.push(`the beat cannot be registered: ${message(error)}`);
      return "absent";
    }
    return "registered";
  }

  /** Ingests one finished job and settles whatever claim authorized it. */
  async function settle(
    at: number,
    target: IngestTarget,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
  ): Promise<void> {
    let result: IngestResult | null = null;
    try {
      result = await ingestOutputs(store, jobs, target);
    } catch (error) {
      // An output the hub cannot read closes its run as failed rather than being retried every
      // cycle for ever: the run row is the loop's memory of what it has already dealt with, and
      // a job nobody requested (the beat) has none until this writes one.
      notes.push(`job ${target.jobId} outputs were refused: ${message(error)}`);
      const failed = JSON.stringify({ closure: "failed", reason: message(error) });
      await store.db.run(
        `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure, records, payload)
         VALUES (?, ?, ?, ?, ?, ?, 'failed', 0, ?)
         ON CONFLICT(id) DO UPDATE SET closure = 'failed', finished_at = excluded.finished_at,
                                       payload = excluded.payload`,
        [
          target.runId ?? `run_${target.jobId}`,
          target.operationId,
          target.machineId,
          target.jobId,
          new Date(at).toISOString(),
          new Date(at).toISOString(),
          failed,
        ],
      );
    }
    for (const note of result?.notes ?? []) notes.push(`${target.jobId}: ${note}`);
    const receipt = result?.receipt ?? null;
    ingested.push({
      runId: result?.runId ?? target.runId ?? `run_${target.jobId}`,
      jobId: target.jobId,
      closure: receipt?.closure ?? target.closure,
      costUsd: receipt?.costUsd ?? 0,
      rows: result?.rows ?? {},
      skipped: result?.skipped ?? 0,
    });

    // What a review cost is what the run recorded; a job that wrote no receipt spent nothing,
    // and its claim is released rather than held by a worker that is not coming back.
    const open = await store.db.query<OpenClaim>(
      `SELECT id, run_id, fence, reserved_cost FROM claims WHERE job_id = ? AND finished_at IS NULL`,
      [target.jobId],
    );
    const outcome =
      receipt?.closure === "completed"
        ? "completed"
        : receipt?.closure === "skipped"
          ? "skipped"
          : "failed";
    const cost = receipt?.costUsd ?? 0;
    for (const claim of open) {
      const finished = await coordinator.finish({
        id: claim.id,
        runId: claim.run_id,
        fence: claim.fence,
        cost,
        outcome,
      });
      settled.push(
        finished.outcome === "finished"
          ? {
              claimId: claim.id,
              outcome,
              cost: finished.cost,
              overrun: finished.overrun,
              refused: null,
            }
          : { claimId: claim.id, outcome, cost, overrun: false, refused: finished.refusal.reason },
      );
    }
  }

  /** Every job the hub is waiting on, plus the beat's own, which nobody requested. */
  async function reconcileRuns(
    at: number,
    ingested: IngestedRun[],
    settled: SettledClaim[],
    notes: string[],
  ): Promise<number> {
    const pending = await store.db.query<PendingRun>(
      `SELECT id, job_id, machine_id, kind FROM runs
        WHERE closure IS NULL AND job_id IS NOT NULL AND machine_id IS NOT NULL
        ORDER BY started_at`,
    );
    let inFlight = 0;
    for (const run of pending) {
      let state: JobRunState | null = null;
      try {
        state = await jobs.status({
          kind: "job",
          machineId: run.machine_id,
          operationId: run.kind,
          jobId: run.job_id,
        });
      } catch (error) {
        notes.push(`job ${run.job_id} cannot be read: ${message(error)}`);
      }
      if (state === null || TERMINAL_STATES[state.state] !== true) {
        inFlight += 1;
        continue;
      }
      await settle(
        at,
        {
          runId: run.id,
          jobId: run.job_id,
          machineId: run.machine_id,
          operationId: run.kind,
          outputs: state.result?.outputs ?? [],
          closure: closureOf(state),
        },
        ingested,
        settled,
        notes,
      );
    }

    for (const machineId of await knownMachines()) {
      let listed: readonly { job: JobRunState | null }[];
      try {
        listed = (
          await jobs.listRuns({
            machineId,
            operationId: BEAT_OPERATION,
            limit: BEAT_RUNS_SCANNED,
          })
        ).runs;
      } catch (error) {
        notes.push(`the beat's runs on ${machineId} cannot be listed: ${message(error)}`);
        continue;
      }
      for (const row of listed) {
        const job = row.job;
        if (job === null) continue;
        if (TERMINAL_STATES[job.state] !== true) {
          inFlight += 1;
          continue;
        }
        const recorded = await store.db.query<Existing>(`SELECT id FROM runs WHERE job_id = ?`, [
          job.jobId,
        ]);
        if (recorded.length > 0) continue;
        await settle(
          at,
          {
            runId: null,
            jobId: job.jobId,
            machineId,
            operationId: BEAT_OPERATION,
            outputs: job.result?.outputs ?? [],
            closure: closureOf(job),
          },
          ingested,
          settled,
          notes,
        );
      }
    }
    return inFlight;
  }

  /** One drawn review, turned into a claimed job on a machine — or a refusal that says why. */
  async function dispatch(
    assignment: Assignment,
    cycleRunId: string,
    at: number,
    seen: Map<string, MachineReadiness | null>,
    requested: RequestedJob[],
    refused: RefusedDraw[],
  ): Promise<void> {
    const recipe = plan.recipes[assignment.role];
    if (recipe === undefined) {
      refused.push({
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        reason: "no-recipe",
        detail: `no cookbook recipe runs the ${assignment.role} role`,
      });
      return;
    }
    const projection = await project(assignment.recordId);
    if (projection === null) {
      refused.push({
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        reason: "no-record",
        detail: "the drawn record is not in the store",
      });
      return;
    }
    const operationId = OPERATIONS.evaluate;
    const host = await machineFor(seen, operationId, assignment.recordId);
    if (host === null) {
      refused.push({
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        reason: "no-machine",
        detail: `no online machine has ${operationId} ready`,
      });
      return;
    }
    // Both ids are derived from the assignment, which is itself deterministic: a cycle that is
    // retried claims the same claim and asks for the same job rather than running it twice.
    const jobId = `job_${assignment.id}`;
    const runId = `run_${assignment.id}`;
    const claimed = await coordinator.claim({ assignment, runId: cycleRunId, jobId, now: at });
    if (claimed.outcome === "refused") {
      refused.push({
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        reason: claimed.refusal.reason,
        detail: claimed.refusal.detail,
      });
      return;
    }
    const claim = claimed.claim;
    const document = {
      runId,
      machineId: host.machineId,
      engine: { binary: plan.engine.binary, args: plan.engine.args ?? [] },
      profile: plan.profile,
      assignment: {
        id: claim.id,
        recordId: assignment.recordId,
        revisionId: assignment.recordId,
        rootId: assignment.rootId,
        kind: assignment.kind,
        role: assignment.role,
        lane: assignment.lane,
        policyVersion: assignment.policyVersion,
        fence: claim.fence,
        ordinal: assignment.ordinal,
        seed: assignment.seed,
        inputDigest: assignment.inputDigest,
        blinded: true,
        expiresAt: new Date(claim.expiresAt).toISOString(),
      },
      target: projection.target,
      sources: projection.sources,
      recipe,
      caps: {
        ...plan.caps,
        perRunUsd: claim.reservedCost > 0 ? claim.reservedCost : plan.caps.perRunUsd,
      },
      requireContainment: plan.requireContainment,
    };
    const installation = host.readiness.installation;
    try {
      await jobs.execute({
        jobId,
        machineId: host.machineId,
        operationId,
        input: { [INPUT_FIELD]: JSON.stringify(document) },
        outputs: [{ name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [jobId] }],
        limits: plan.limits,
        ...(installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            }),
      });
    } catch (error) {
      const finished = await coordinator.finish({
        id: claim.id,
        runId: cycleRunId,
        fence: claim.fence,
        cost: 0,
        outcome: "skipped",
      });
      refused.push({
        assignmentId: assignment.id,
        recordId: assignment.recordId,
        reason: "refused-job",
        detail:
          finished.outcome === "refused"
            ? `${message(error)}; the claim also refused ${finished.refusal.reason}`
            : message(error),
      });
      return;
    }
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, job_id, recipe_id, profile, authority_kind,
                        authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, ?, 'policy', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        runId,
        operationId,
        host.machineId,
        jobId,
        recipe.id,
        JSON.stringify(plan.profile),
        assignment.policyVersion,
        JSON.stringify({
          claimId: claim.id,
          cycleRunId,
          lane: assignment.lane,
          role: assignment.role,
          recordId: assignment.recordId,
        }),
        new Date(at).toISOString(),
        JSON.stringify({ closure: null, requestedAt: at }),
      ],
    );
    store.touch();
    requested.push({
      runId,
      jobId,
      machineId: host.machineId,
      claimId: claim.id,
      recordId: assignment.recordId,
      role: assignment.role,
      lane: assignment.lane,
    });
  }

  return {
    async tick(): Promise<TickReport> {
      const at = deps.now();
      cycle += 1;
      const cycleRunId = `cyc_${String(at)}_${String(cycle)}`;
      const policy = (await coordinator.policy()).policy;
      const notes: string[] = [];
      const schedule = await reconcileSchedule(policy, at, notes);
      const requested: RequestedJob[] = [];
      const ingested: IngestedRun[] = [];
      const settled: SettledClaim[] = [];
      const refused: RefusedDraw[] = [];

      if (!policy.enabled) {
        return {
          at,
          cycleRunId,
          policyVersion: policy.version,
          enabled: false,
          schedule,
          requested,
          ingested,
          settled,
          refused,
          stop: null,
          gaps: [],
          pending: 0,
          notes,
        };
      }

      const pending = await reconcileRuns(at, ingested, settled, notes);

      // Draw until the coordinator says stop. It owns the batch, the per-cycle and the daily
      // bound; the loop's own bound is that a cycle never draws the same assignment twice, so a
      // draw the hub cannot dispatch ends the cycle instead of spinning on it.
      const seen = new Map<string, MachineReadiness | null>();
      const drawn = new Set<string>();
      let stop: Stop | null = null;
      let gaps: readonly Gap[] = [];
      for (;;) {
        const draw = await coordinator.draw({ runId: cycleRunId, now: at });
        gaps = draw.gaps;
        if (draw.outcome === "gap") {
          stop = draw.gap;
          break;
        }
        const assignment = draw.assignment;
        if (drawn.has(assignment.id)) {
          stop = {
            reason: "no-candidates",
            detail: `the cycle redrew ${assignment.id}, which it could not dispatch`,
          };
          break;
        }
        drawn.add(assignment.id);
        await dispatch(assignment, cycleRunId, at, seen, requested, refused);
      }

      return {
        at,
        cycleRunId,
        policyVersion: policy.version,
        enabled: true,
        schedule,
        requested,
        ingested,
        settled,
        refused,
        stop,
        gaps,
        pending: pending + requested.length,
        notes,
      };
    },
  };
}

/** What the engine's own verdict says a run closed as, before any receipt is read. */
function closureOf(state: JobRunState): string {
  if (state.state === "cancelled") return "stopped";
  if (state.state === "exited" && (state.result?.exitCode ?? 1) === 0) return "completed";
  return "failed";
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
