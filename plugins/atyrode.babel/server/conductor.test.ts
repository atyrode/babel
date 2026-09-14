import { Database } from "bun:sqlite";
import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import {
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  JOB_OUTPUT_FILES,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  RUN_STAGES,
} from "../contract.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import type { BabelStore } from "../store/store.ts";
import type {
  Assignment,
  Coordinator,
  Fence,
  Gap,
  OpenClaims,
  Policy,
  Stop,
} from "../store/coordinator.ts";
import {
  BEAT_OPERATION,
  CONDUCTOR_SCHEDULE_ID,
  conductor,
  type FollowEvent,
  type FollowRead,
  ingestOutputs,
  type InferenceUsage,
  type JobLaunch,
  type JobOutput,
  type JobRunState,
  type JobsSlice,
  type JobState,
  type KeysSlice,
  type MachineReadiness,
  type MachinesSlice,
  type RepositoryFact,
  type RepositoryOutcome,
  type RunPlan,
  type ScheduleRow,
  type ScheduleTiming,
} from "./conductor.ts";

/*
  The loop, against a real SQLite file and a fake machine fleet. The database is real because
  every claim this file makes is about what the store holds afterwards — that ingestion is
  idempotent, that a scan's partial session row does not clobber an archive's snapshot, that a
  failed job does not leave a claim open. The fleet is fake because the point of the JobsSlice
  is that the hub half can be driven without one.
*/

// ---------------------------------------------------------------------------- a real database

const temporaries: string[] = [];

afterEach(() => {
  for (const directory of temporaries.splice(0)) rmSync(directory, { recursive: true, force: true });
});

function openDatabase(): PluginDatabase {
  const directory = mkdtempSync(join(tmpdir(), "babel-conductor-"));
  temporaries.push(directory);
  const db = new Database(join(directory, "data.db"), { create: true, strict: true, safeIntegers: true });
  // The options and pragmas the engine opens a plugin's file with (`server/src/plugin-database.ts`),
  // so the triggers, the STRICT tables, the foreign keys and — `safeIntegers` — the BIGINT every
  // INTEGER column answers with behave here exactly as they do in the hub.
  db.exec(`PRAGMA journal_mode = WAL`);
  db.exec(`PRAGMA trusted_schema = OFF`);
  db.exec(`PRAGMA foreign_keys = ON`);
  for (const statement of SCHEMA_V1) db.exec(statement);
  const bind = (params: readonly SqlParam[] | undefined): SqlParam[] => [...(params ?? [])];
  return {
    pluginId: BABEL_PLUGIN_ID,
    query: async <Row extends SqlRow = SqlRow>(sql: string, params?: readonly SqlParam[]) =>
      db.prepare(sql).all(...(bind(params) as never[])) as Row[],
    run: async (sql: string, params?: readonly SqlParam[]) => {
      const result = db.prepare(sql).run(...(bind(params) as never[]));
      return { changes: Number(result.changes), lastInsertRowid: BigInt(result.lastInsertRowid) };
    },
    batch: async (statements: readonly SqlStatement[]) =>
      db.transaction(() =>
        statements.map(
          (statement) => db.prepare(statement.sql).all(...(bind(statement.params) as never[])) as SqlRow[],
        ),
      )(),
  };
}

let clock = Date.parse("2026-09-12T09:00:00.000Z");

function openStore(db: PluginDatabase): BabelStore & { touched: number } {
  const store = {
    db,
    now: () => clock,
    touched: 0,
    touch(): void {
      store.touched += 1;
    },
  };
  return store as BabelStore & { touched: number };
}

const TABLES = [
  "sessions",
  "records",
  "edges",
  "status_events",
  "assessments",
  "filings",
  "questions",
  "plans",
  "steering",
  "runs",
  "claims",
] as const;

async function snapshot(db: PluginDatabase): Promise<string> {
  const dump: Record<string, readonly SqlRow[]> = {};
  for (const table of TABLES) dump[table] = await db.query(`SELECT * FROM ${table}`);
  // An INTEGER column answers as a BIGINT, which JSON has no word for: it is rendered with the
  // suffix it is written with, so a dump still compares byte for byte against the one before it.
  return JSON.stringify(dump, (_key: string, value: unknown) =>
    typeof value === "bigint" ? `${String(value)}n` : value,
  );
}

// ---------------------------------------------------------------------------- a ustar archive

/** The archive shape the agent seals an output as: regular files, mode 0600, no extensions. */
function tar(files: Readonly<Record<string, unknown>>): Buffer {
  const blocks: Buffer[] = [];
  for (const [name, document] of Object.entries(files)) {
    const body = Buffer.from(JSON.stringify(document), "utf8");
    const header = Buffer.alloc(512);
    header.write(name, 0, 100, "utf8");
    header.write("0000600\0", 100, 8, "ascii");
    header.write("0000000\0", 108, 8, "ascii");
    header.write("0000000\0", 116, 8, "ascii");
    header.write(`${body.byteLength.toString(8).padStart(11, "0")}\0`, 124, 12, "ascii");
    header.write("00000000000\0", 136, 12, "ascii");
    header.write("        ", 148, 8, "ascii");
    header[156] = 0x30;
    header.write("ustar\0", 257, 6, "ascii");
    header.write("00", 263, 2, "ascii");
    let sum = 0;
    for (const byte of header) sum += byte;
    header.write(`${sum.toString(8).padStart(6, "0")}\0 `, 148, 8, "ascii");
    blocks.push(header, body, Buffer.alloc((512 - (body.byteLength % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return Buffer.concat(blocks);
}

// ---------------------------------------------------------------------------- a fake fleet

interface FakeJob {
  state: JobState;
  exitCode: number | null;
  machineId: string;
  operationId: string;
  archive: Buffer | null;
  /** What the hub's replay ring holds for this job, in sequence; the fold reads it whole. */
  journal: FollowEvent[];
  /** What the OWNER metered, as `usage.inference` on the result of a settled job. */
  inference: InferenceUsage | null;
}

/**
 * WHAT THE RING HOLDS PER FRAME: a sequence and the frame. No instant of the hub's — the ring
 * is not the journal and stamps nothing — so the only clock in one of these is the owner's own,
 * inside a `job_progress` body.
 */
function progressed(seq: number, at: number, stage: string, message?: string): FollowEvent {
  return {
    seq,
    event: {
      type: "job_progress",
      jobId: "job",
      requestDigest: "d".repeat(64),
      ownerId: "owner",
      ownerGeneration: 1,
      stage,
      ...(message === undefined ? {} : { message }),
      at,
    },
  };
}

function called(
  seq: number,
  over: Partial<{ model: string; inputTokens: number; outputTokens: number; cachedInputTokens: number; costMicros: number }> = {},
): FollowEvent {
  return {
    seq,
    event: {
      type: "inference_call",
      jobId: "job",
      requestDigest: "d".repeat(64),
      ownerId: "owner",
      ownerGeneration: 1,
      serviceId: "atyrode.code.inference",
      operationId: "messages",
      model: over.model ?? "claude-opus-4",
      inputTokens: over.inputTokens ?? 1_000,
      outputTokens: over.outputTokens ?? 200,
      cachedInputTokens: over.cachedInputTokens ?? 50,
      costMicros: over.costMicros ?? 30_000,
      elapsedMs: 4_000,
      status: 200,
    },
  };
}

/** `MAX_JOB_FOLLOW_EVENTS`: how many frames of any kind one job's ring holds. */
const FOLLOW_RING = 128;

class Fleet implements JobsSlice {
  readonly launched: JobLaunch[] = [];
  readonly scheduled: (JobLaunch & ScheduleTiming)[] = [];
  readonly disabled: { scheduleId: string; revision: string }[] = [];
  registered: ScheduleRow[] = [];
  readonly jobs = new Map<string, FakeJob>();
  readonly beats = new Set<string>();
  ready = true;
  connected = true;
  /** What `execute` refuses every posting with, as the hub does when a machine will not take it. */
  refusal: string | null = null;
  /** What the HUB refuses an ADMITTED posting with: a job that never ran, whose reason is on the
   *  authority decision rather than in a result it never got — `concurrency_limit` when the
   *  operation's declared `limits.concurrentJobs` is full (atyrode/manifold#551). */
  decision: string | null = null;
  /** Jobs the hub can no longer report at all: a machine that vanished mid-review. */
  readonly silent = new Set<string>();
  /** Every job this fake was asked to follow, and every subscription it was asked to close. */
  readonly followed: string[] = [];
  readonly closed: string[] = [];

  describe(args: { machineId: string; pluginId: string }): MachineReadiness {
    expect(args.pluginId).toBe(BABEL_PLUGIN_ID);
    return {
      connected: this.connected,
      operations: {
        [OPERATIONS.evaluate]: { ready: this.ready, reason: null },
        [OPERATIONS.scan]: { ready: this.ready, reason: null },
      },
      installation: {
        revision: "rev-7",
        artifactSha256: "a".repeat(64),
        enabled: true,
        ready: true,
      },
    };
  }

  execute(args: JobLaunch): JobRunState {
    if (this.refusal !== null) throw new Error(this.refusal);
    if (this.decision !== null) {
      return {
        jobId: args.jobId,
        machineId: args.machineId,
        operationId: args.operationId,
        state: "refused",
        result: null,
        authority: { decision: { refusal: this.decision } },
      };
    }
    this.launched.push(args);
    this.jobs.set(args.jobId, {
      state: "started",
      exitCode: null,
      machineId: args.machineId,
      operationId: args.operationId,
      archive: null,
      journal: [],
      inference: null,
    });
    return this.status({ jobId: args.jobId });
  }

  status(node: { jobId: string }): JobRunState {
    const job = this.jobs.get(node.jobId);
    if (job === undefined) throw new Error(`unknown job ${node.jobId}`);
    if (this.silent.has(node.jobId)) throw new Error(`the machine holding ${node.jobId} is gone`);
    const outputs: JobOutput[] =
      job.archive === null
        ? []
        : [
            {
              outputId: `out_${node.jobId}`,
              name: OUTPUT_BINDING,
              bytes: job.archive.byteLength,
              files: 1,
            },
          ];
    return {
      jobId: node.jobId,
      machineId: job.machineId,
      operationId: job.operationId,
      state: job.state,
      result:
        job.state === "started" || job.state === "queued"
          ? null
          : {
              state: job.state,
              exitCode: job.exitCode,
              reason: null,
              outputs,
              ...(job.inference === null ? {} : { usage: { inference: job.inference } }),
            },
    };
  }

  listRuns(args: { machineId: string; operationId?: string | undefined }): {
    runs: { job: JobRunState | null }[];
  } {
    const runs: { job: JobRunState | null }[] = [];
    for (const jobId of this.beats) {
      const job = this.jobs.get(jobId);
      if (job === undefined) continue;
      if (job.machineId !== args.machineId) continue;
      if (args.operationId !== undefined && job.operationId !== args.operationId) continue;
      runs.push({ job: this.status({ jobId }) });
    }
    return { runs };
  }

  output(args: { node: { jobId: string }; offset: number; maxBytes: number }): {
    data: string;
    eof: boolean;
  } {
    const job = this.jobs.get(args.node.jobId);
    if (job?.archive == null) throw new Error(`job ${args.node.jobId} sealed no output`);
    const end = Math.min(job.archive.byteLength, args.offset + args.maxBytes);
    return {
      data: job.archive.subarray(args.offset, end).toString("base64"),
      eof: end === job.archive.byteLength,
    };
  }

  /**
   * WHAT THE HUB SERVES FOR A JOB THAT IS STILL RUNNING, and it is only this. `follow` answers
   * with a snapshot of the replay ring — the frames it still holds, the oldest sequence in it,
   * and what it says it cannot replay — plus a subscription to close. It takes no `receive`
   * here: the loop closes in the same turn, so nothing would ever be delivered, and this fake
   * is the ring rather than a live hub.
   */
  follow(node: { jobId: string }): FollowRead {
    const job = this.jobs.get(node.jobId);
    if (job === undefined) throw new Error(`unknown job ${node.jobId}`);
    if (this.silent.has(node.jobId)) throw new Error(`the machine holding ${node.jobId} is gone`);
    this.followed.push(node.jobId);
    const frames = job.journal.slice(-FOLLOW_RING);
    const firstSeq = frames[0]?.seq ?? null;
    const missingThrough = firstSeq === null ? (job.journal[job.journal.length - 1]?.seq ?? 0) : firstSeq - 1;
    return {
      snapshot: {
        events: frames,
        firstSeq,
        unavailable: missingThrough > 0 ? { fromSeq: 1, toSeq: missingThrough } : null,
      },
      close: () => {
        this.closed.push(node.jobId);
      },
    };
  }

  /**
   * WHAT THE HUB REFUSES FOR ONE: `JobService.journal` is retrieval for a FINISHED job, and a
   * running one is refused `job_unfinished` because watching it is what `follow` is for
   * (`packages/server/src/job-service.ts`; its own suite pins the refusal). It is on the fake
   * although the slice no longer declares it, so a loop that went back to reading a journal
   * fails here the way it would fail on a hub.
   */
  journal(args: { node: { jobId: string } }): never {
    const job = this.jobs.get(args.node.jobId);
    if (job !== undefined && job.state !== "started" && job.state !== "queued") {
      throw new Error(`this fake serves no finished journal for ${args.node.jobId}`);
    }
    throw new Error("job_unfinished");
  }

  schedule(args: JobLaunch & ScheduleTiming): Record<string, never> {
    this.scheduled.push(args);
    this.registered = [
      ...this.registered,
      { ...args, machineId: args.machineId, operationId: args.operationId },
    ];
    return {};
  }

  schedules(): readonly ScheduleRow[] {
    return this.registered;
  }

  disableSchedule(args: { scheduleId: string; revision: string }): Record<string, never> {
    this.disabled.push(args);
    this.registered = this.registered.filter(
      (row) => row.scheduleId !== args.scheduleId || row.revision !== args.revision,
    );
    return {};
  }

  /** A job the machine finished, with the files it sealed (none for a job that wrote nothing). */
  finish(jobId: string, exitCode: number, files: Readonly<Record<string, unknown>> | null): void {
    const job = this.jobs.get(jobId);
    if (job === undefined) throw new Error(`unknown job ${jobId}`);
    job.state = "exited";
    job.exitCode = exitCode;
    job.archive = files === null ? null : tar(files);
  }

  /** A job the hub ended without the machine finishing it: a cancel, or an agent that died. */
  kill(jobId: string, state: "cancelled" | "interrupted"): void {
    const job = this.jobs.get(jobId);
    if (job === undefined) throw new Error(`unknown job ${jobId}`);
    job.state = state;
    job.exitCode = null;
    job.archive = null;
  }

  /** A job that sealed something the hub cannot read as an archive. */
  seal(jobId: string, archive: Buffer): void {
    const job = this.jobs.get(jobId);
    if (job === undefined) throw new Error(`unknown job ${jobId}`);
    job.state = "exited";
    job.exitCode = 0;
    job.archive = archive;
  }

  /** A scheduled beat that ran: nobody requested it, so no run row exists for it. */
  beat(jobId: string, machineId: string, files: Readonly<Record<string, unknown>>): void {
    this.jobs.set(jobId, {
      state: "exited",
      exitCode: 0,
      machineId,
      operationId: BEAT_OPERATION,
      archive: tar(files),
      journal: [],
      inference: null,
    });
    this.beats.add(jobId);
  }

  /** What this job's replay ring holds, as the owner would have emitted it. */
  journals(jobId: string, entries: readonly FollowEvent[]): void {
    const job = this.jobs.get(jobId);
    if (job === undefined) throw new Error(`unknown job ${jobId}`);
    job.journal = [...entries];
  }

  /** What the owner metered for this job, as the result of a settled one carries it. */
  metered(jobId: string, inference: InferenceUsage): void {
    const job = this.jobs.get(jobId);
    if (job === undefined) throw new Error(`unknown job ${jobId}`);
    job.inference = inference;
  }
}

/**
 * THE SLICE A SETTLEMENT'S HOOK IS SERVED: every verb of the fake but the live subscription,
 * which is exactly what `GuestHookJobs` is (`plugin-kit/src/server.ts`: "every job verb but
 * `follow`"). A cycle over this one can settle what ended and cannot read what has not.
 */
function hookWoken(fleet: Fleet): JobsSlice {
  return {
    describe: (args) => fleet.describe(args),
    execute: (args) => fleet.execute(args),
    status: (node) => fleet.status(node),
    listRuns: (args) => fleet.listRuns(args),
    output: (args) => fleet.output(args),
    schedule: (args) => fleet.schedule(args),
    schedules: () => fleet.schedules(),
    disableSchedule: (args) => fleet.disableSchedule(args),
  };
}

// ---------------------------------------------------------------------------- a fake host

/**
 * What the agent standing on a host answers about one folder (#535). `facts` is what the disk
 * holds, by path; a folder it does not name is a folder that exists and is not a checkout,
 * which is the ordinary answer rather than an error. `refusal` is the hub saying nobody could
 * be asked at all — offline, too old a transport, silence — and is never a fact.
 */
class Folders implements MachinesSlice {
  readonly asked: string[] = [];
  facts: Record<string, RepositoryFact> = {};
  refusal: string | null = null;

  repository(machineId: string, path: string): RepositoryOutcome {
    this.asked.push(`${machineId}:${path}`);
    if (this.refusal !== null) return { ok: false, reason: this.refusal };
    return {
      ok: true,
      fact: this.facts[path] ?? {
        path,
        identity: null,
        remote: null,
        reason: "not_a_repository",
        observedAt: clock,
      },
    };
  }
}

// ---------------------------------------------------------------------------- the plugin's keys

/**
 * `ctx.storage` as the host serves it, narrowed to the two verbs the loop uses: one map of
 * values, and a refusal that stands for a host that will not serve the key at all.
 */
class Keys implements KeysSlice {
  readonly held: Record<string, string> = {};
  refusal: string | null = null;

  get(key: string): string | null {
    if (this.refusal !== null) throw new Error(this.refusal);
    return this.held[key] ?? null;
  }

  set(key: string, value: string): void {
    if (this.refusal !== null) throw new Error(this.refusal);
    this.held[key] = value;
  }
}

// ---------------------------------------------------------------------------- a fake coordinator

const POLICY = {
  version: "pol_1",
  enabled: true,
  cadenceSeconds: 900,
  overdueSeconds: 604800,
  initialReviews: 2,
  cooldownSeconds: 3600,
  coverageShare: 0.4,
  explorationShare: 0.2,
  discoveryShare: 0.2,
  filingShare: 0.1,
  backlogShare: 0.1,
  maxItemReviews: 6,
  perCycleCost: 0.5,
  dailyCost: 5,
  leaseSeconds: 900,
  batchSize: 4,
};

const ASSIGNMENT = {
  id: "asg_a1b2",
  recordId: "hyp_00000001",
  rootId: "hyp_00000001",
  kind: "hypothesis",
  role: "reception",
  lane: "coverage",
  policyVersion: "pol_1",
  ordinal: 0,
  seed: "7",
  inputDigest: "sha256:d1",
  reservedCost: 0.1,
  drawnAt: clock,
  topics: [],
} satisfies Record<string, unknown>;

class Draws {
  draws = 0;
  readonly claimed: { assignmentId: string; runId: string; jobId: string | undefined }[] = [];
  readonly finished: { id: string; runId: string; fence: Fence; cost: number; outcome: string }[] =
    [];
  readonly abandoned: { id: string; fence: Fence; reason: string }[] = [];
  enabled = true;
  version = POLICY.version;
  /** Assignments this coordinator still has to give; it answers its {@link stop} when they run out. */
  pending: Record<string, unknown>[] = [];
  /** Why it stops handing work out, in the coordinator's own closed vocabulary. */
  stop: Stop = { reason: "no-candidates", detail: "nothing due" };
  /** The candidates it declined on the way, which every draw carries whatever it answers. */
  declined: Gap[] = [];
  /** What a budget overlay moves on top of the standing policy, or null: nothing is overlaid. */
  overlay: Partial<Policy> | null = null;
  /** The machines each draw was told this cycle could dispatch to (#260). */
  readonly offered: (readonly string[])[] = [];

  constructor(private readonly db: PluginDatabase) {}

  async policy(): Promise<Record<string, unknown>> {
    const standing = { ...POLICY, enabled: this.enabled, version: this.version };
    return {
      policy: this.overlay === null ? standing : { ...standing, ...this.overlay },
      standing,
      overlay: null,
      source: "stored",
      version: this.version,
      recordedAt: clock,
    };
  }

  /**
   * `coordinator.open`: the batch slots held, per machine. The real one counts claims whose job
   * has a run row naming a machine, which is what the loop writes for every job it posts — so
   * counting the unfinished run rows reads the same evidence, and the slots a cycle takes are
   * visible to its own later dispatches.
   */
  async open(): Promise<OpenClaims> {
    const rows = await this.db.query<{ machine: string; open: bigint }>(
      `SELECT machine_id AS machine, COUNT(*) AS open FROM runs
        WHERE finished_at IS NULL AND machine_id IS NOT NULL GROUP BY machine_id`,
    );
    const byMachine: Record<string, number> = {};
    let total = 0;
    for (const row of rows) {
      byMachine[row.machine] = Number(row.open);
      total += Number(row.open);
    }
    return { total, byMachine };
  }

  async draw(request: {
    runId: string;
    machines?: readonly string[] | undefined;
  }): Promise<Record<string, unknown>> {
    this.draws += 1;
    expect(request.runId.startsWith("cyc_")).toBe(true);
    this.offered.push([...(request.machines ?? [])]);
    const next = this.pending.shift();
    if (next === undefined) {
      return { outcome: "gap", gap: this.stop, gaps: this.declined };
    }
    return { outcome: "assignment", assignment: next, gaps: this.declined };
  }

  async claim(request: {
    assignment: Assignment;
    runId: string;
    jobId?: string | undefined;
  }): Promise<Record<string, unknown>> {
    this.claimed.push({
      assignmentId: request.assignment.id,
      runId: request.runId,
      jobId: request.jobId,
    });
    const claim = {
      id: `clm_${request.assignment.id}`,
      recordId: request.assignment.recordId,
      role: request.assignment.role,
      lane: request.assignment.lane,
      policyVersion: request.assignment.policyVersion,
      jobId: request.jobId ?? null,
      runId: request.runId,
      fence: 1,
      reservedCost: request.assignment.reservedCost,
      actualCost: null,
      grantedAt: clock,
      expiresAt: clock + POLICY.leaseSeconds * 1000,
      finishedAt: null,
      outcome: null,
    };
    await this.db.run(
      `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                          reserved_cost, granted_at, expires_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        claim.id,
        claim.recordId,
        claim.role,
        claim.lane,
        claim.policyVersion,
        claim.jobId,
        claim.runId,
        claim.fence,
        claim.reservedCost,
        new Date(claim.grantedAt).toISOString(),
        new Date(claim.expiresAt).toISOString(),
      ],
    );
    return { outcome: "granted", claim };
  }

  async finish(request: {
    id: string;
    runId: string;
    fence: Fence;
    cost: number;
    outcome: string;
  }): Promise<Record<string, unknown>> {
    this.finished.push(request);
    await this.db.run(
      `UPDATE claims SET finished_at = ?, actual_cost = ?, outcome = ? WHERE id = ? AND fence = ?`,
      [new Date(clock).toISOString(), request.cost, request.outcome, request.id, request.fence],
    );
    return { outcome: "finished", cost: request.cost, reserved: 0.1, overrun: false };
  }

  /** `coordinator.abandon`, doing what the real one does: closes the row and charges the
   *  reservation, because a job that died mid-review cannot say what it spent. */
  async abandon(request: { id: string; fence: Fence; reason: string }): Promise<Record<string, unknown>> {
    this.abandoned.push(request);
    const rows = await this.db.query<{ reserved_cost: number }>(
      `SELECT reserved_cost FROM claims WHERE id = ? AND fence = ? AND finished_at IS NULL`,
      [request.id, request.fence],
    );
    const reserved = rows[0]?.reserved_cost;
    if (reserved === undefined) {
      return { outcome: "refused", refusal: { reason: "finished", detail: "nothing to abandon" } };
    }
    await this.db.run(
      `UPDATE claims SET finished_at = ?, actual_cost = reserved_cost, outcome = 'abandoned'
        WHERE id = ? AND fence = ?`,
      [new Date(clock).toISOString(), request.id, request.fence],
    );
    return { outcome: "abandoned", cost: reserved, reason: request.reason };
  }

  async renew(): Promise<Record<string, unknown>> {
    return { outcome: "renewed", expiresAt: clock };
  }

  async spend(): Promise<Record<string, unknown>> {
    return { day: "2026-09-12", total: 0, byRun: {} };
  }
}

/**
 * The plan a cycle runs under here, with evaluate METERED: the review lane binds a service the
 * owner meters, which is what makes its silence at the model a judgeable thing. {@link
 * UNMETERED_PLAN} is the same plan for a deployment that binds none, which is this repository
 * today (#256).
 */
const PLAN: RunPlan = {
  engine: { binary: "/runtime/bin/omp", args: [] },
  session: {
    model: "anthropic/claude-sonnet-5",
    thinking: "high",
    account: {
      provider: "anthropic",
      scope: "atyrode.omp.accounts.broker@7/m-dev-01",
      credentialId: "3",
      identityKey: "victorballu@gmail.com",
    },
  },
  caps: { perRunUsd: 0.25, toolCalls: 40, idleMs: 120000, handshakeMs: 30000 },
  recipes: {
    reception: { id: "reception-vote", version: 1, title: "Reception", body: "Does it hold?" },
  },
  metered: { [OPERATIONS.evaluate]: true },
  requireContainment: true,
  limits: { timeoutMs: 900000, memoryBytes: 2147483648, processes: 64, outputBytes: 67108864 },
};

const UNMETERED_PLAN: RunPlan = { ...PLAN, metered: {} };

// ---------------------------------------------------------------------------- the corpus

async function seed(db: PluginDatabase): Promise<void> {
  await db.batch([
    {
      sql: `INSERT INTO sessions(selector, host, harness, source_id, title, snapshot_id, seen_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)`,
      params: ["omp/s1", "dev-01", "omp", "s1", "first title", "snap-1", "2026-09-01T00:00:00Z"],
    },
    {
      sql: `INSERT INTO records(id, kind, root_id, seq, actor_kind, actor_id, title, created_at, payload)
            VALUES (?, 'hypothesis', ?, 0, 'run', 'run_seed', ?, ?, ?)`,
      params: [
        "hyp_00000001",
        "hyp_00000001",
        "The catalog forgets archived sessions",
        "2026-09-01T00:00:00Z",
        JSON.stringify({ statement: "…" }),
      ],
    },
    {
      sql: `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note,
                              actor_kind, actor_id, created_at)
            VALUES (?, 'cites', 'hypothesis', ?, 'session', ?, 0, ?, 'run', 'run_seed', ?)`,
      params: ["edg_1", "hyp_00000001", "omp/s1", "the third turn", "2026-09-01T00:00:00Z"],
    },
  ]);
}

/** Everything a finished review writes: one of each file the contract names. */
function outputs(runId: string): Record<string, unknown> {
  const at = "2026-09-12T09:05:00Z";
  return {
    [JOB_OUTPUT_FILES.records]: [
      {
        id: "fnd_00000002",
        kind: "finding",
        root_id: "fnd_00000002",
        seq: 0,
        run_id: runId,
        actor_kind: "run",
        actor_id: runId,
        title: "Archived sessions are dropped on rescan",
        created_at: at,
        payload: JSON.stringify({ finding: "…" }),
      },
    ],
    [JOB_OUTPUT_FILES.edges]: [
      {
        id: "edg_2",
        kind: "consolidates",
        from_kind: "finding",
        from_id: "fnd_00000002",
        to_kind: "hypothesis",
        to_id: "hyp_00000001",
        position: 0,
        actor_kind: "run",
        actor_id: runId,
        created_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.statusEvents]: [
      {
        id: "ste_1",
        record_id: "hyp_00000001",
        seq: 1,
        status: "promoted",
        run_id: runId,
        actor_kind: "run",
        actor_id: runId,
        recorded_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.assessments]: [
      {
        id: "ass_1",
        record_id: "hyp_00000001",
        revision_id: "hyp_00000001",
        run_id: runId,
        role: "reception",
        vote: "support",
        lane: "coverage",
        claim_id: "clm_asg_a1b2",
        // A real submission, because the store now accepts an assessment's payload under the
        // same contract the engine was prompted with: a row whose column said `support` while
        // its payload stated no vote at all is the producer/store drift #263 closes.
        payload: JSON.stringify({ vote: "support", uncertainty: "the second criterion is untested" }),
        recorded_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.filings]: [
      {
        id: "fil_1",
        record_id: "hyp_00000001",
        entity_id: "ent_00000001",
        rationale: "it is about the catalog",
        author_kind: "run",
        author_id: runId,
        created_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.plans]: [
      {
        id: "pln_1",
        kind: "topic",
        subject_kind: "proposal",
        subject_id: "pro_00000001",
        operation: "create-entity",
        payload: JSON.stringify({ name: "the catalog" }),
        proposed_by_kind: "run",
        proposed_by_id: runId,
        created_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.questions]: [
      {
        id: "que_1",
        kind: "clarify",
        class: "entity",
        text: "Is dev-01 the same machine as dev?",
        why: "two aliases",
        raised_by_kind: "run",
        raised_by_id: runId,
        payload: JSON.stringify({}),
        created_at: at,
      },
    ],
    [JOB_OUTPUT_FILES.steeringReplies]: [
      {
        id: "str_1",
        root_id: "str_0",
        reply_to_id: "str_0",
        seq: 1,
        actor_kind: "run",
        actor_id: runId,
        text: "noted, and acted on",
        recorded_at: at,
      },
    ],
    // A rescan observes no snapshot, so it names no snapshot column: the archive's value stands.
    [JOB_OUTPUT_FILES.sessions]: [
      {
        selector: "omp/s1",
        host: "dev-01",
        harness: "omp",
        source_id: "s1",
        title: "a better title",
        seen_at: "2026-09-12T09:00:00Z",
      },
    ],
    [JOB_OUTPUT_FILES.receipt]: {
      runId,
      kind: "evaluate",
      machineId: "dev-01",
      recipeId: "reception-vote",
      role: "reception",
      startedAt: "2026-09-12T09:00:10Z",
      finishedAt: at,
      closure: "completed",
      costUsd: 0.42,
      tokens: 12345,
      counts: { records: 1, assessments: 1 },
    },
  };
}

// ---------------------------------------------------------------------------- the tests

test("a running job's stage and spend are folded out of its replay ring, and the meter settles the run", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  // The job says where it is and the owner meters two calls against it. This is served through
  // `follow` and nothing else: the fake refuses `journal` for a running job the way the hub
  // does, so a fold that read one would fail here rather than pass against a friendly fake.
  fleet.journals("job_asg_a1b2", [
    progressed(1, started, RUN_STAGES.preparing, "reception: composing the prompt"),
    progressed(4, started + 20_000, RUN_STAGES.atModel, "reception"),
    called(6, { inputTokens: 12_000, outputTokens: 900, cachedInputTokens: 400, costMicros: 250_000 }),
    called(9, {
      model: "claude-sonnet-4",
      inputTokens: 400,
      outputTokens: 100,
      cachedInputTokens: 0,
      costMicros: 30_000,
    }),
  ]);
  clock = started + 60_000;
  const running = await loop.tick();
  expect(running.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });

  const row = (
    await db.query(`SELECT stage, message, since, calls, input_tokens, output_tokens, cache_tokens,
                           cost_usd, last_model, stalled
                      FROM run_progress WHERE run_id = 'run_asg_a1b2'`)
  )[0];
  expect(row).toMatchObject({
    stage: "at the model",
    message: "reception",
    // The stage's own instant, not the newest frame's: the job has been at the model since it
    // said so, and the two calls after it did not restart that clock.
    since: new Date(started + 20_000).toISOString(),
    calls: 2n,
    input_tokens: 12_400n,
    output_tokens: 1_000n,
    cache_tokens: 400n,
    last_model: "claude-sonnet-4",
    stalled: 0n,
  });
  expect(Number(row?.["cost_usd"])).toBeCloseTo(0.28, 6);
  // Every subscription this cycle opened was closed in the same turn.
  expect(fleet.followed).toEqual(["job_asg_a1b2"]);
  expect(fleet.closed).toEqual(["job_asg_a1b2"]);

  // A second cycle over the same ring folds nothing twice: the loop reads above the newest
  // sequence it has already seen.
  clock = started + 70_000;
  await loop.tick();
  const again = await db.query(`SELECT calls FROM run_progress WHERE run_id = 'run_asg_a1b2'`);
  expect(again[0]?.["calls"]).toBe(2n);

  // THE SECOND PROMPT IS A SECOND WAIT. Composing it is sub-second and the owner coalesces
  // newest-wins every five seconds, so `preparing` between two prompts is usually never seen at
  // all and the stage word repeats. At the model the message says WHICH prompt is out, so a
  // changed message restamps the clock: otherwise "at the model since" would count the first
  // turn's wait across every turn of the run.
  fleet.journals("job_asg_a1b2", [
    progressed(1, started, RUN_STAGES.preparing, "reception: composing the prompt"),
    progressed(4, started + 20_000, RUN_STAGES.atModel, "reception"),
    called(6, { inputTokens: 12_000, outputTokens: 900, cachedInputTokens: 400, costMicros: 250_000 }),
    called(9, { model: "claude-sonnet-4", inputTokens: 400, outputTokens: 100, cachedInputTokens: 0, costMicros: 30_000 }),
    progressed(11, started + 72_000, RUN_STAGES.atModel, "challenge"),
  ]);
  clock = started + 75_000;
  const turning = await loop.tick();
  expect(turning.notes.filter((note) => note.includes("was not retained"))).toEqual([]);
  const second = await db.query(`SELECT message, since FROM run_progress`);
  expect(second[0]).toMatchObject({
    message: "challenge",
    since: new Date(started + 72_000).toISOString(),
  });

  // The job ends and the OWNER's meter — not the receipt's own numbers — fills the run row.
  fleet.metered("job_asg_a1b2", {
    calls: 3,
    inputTokens: 20_000,
    outputTokens: 1_500,
    cachedInputTokens: 400,
    costMicros: 410_000,
  });
  fleet.finish("job_asg_a1b2", 0, outputs("run_asg_a1b2"));
  clock = started + 80_000;
  await loop.tick();

  const settledRow = (
    await db.query(`SELECT closure, tokens, cost_usd, payload FROM runs WHERE id = 'run_asg_a1b2'`)
  )[0];
  expect(settledRow?.["closure"]).toBe("completed");
  expect(settledRow?.["tokens"]).toBe(21_500n);
  expect(Number(settledRow?.["cost_usd"])).toBeCloseTo(0.41, 6);
  // The whole of what the meter counted is kept with the receipt, because the in-flight row is
  // dropped and two columns cannot hold five numbers: the calls and the cache survive the run.
  const kept = JSON.parse(String(settledRow?.["payload"])) as Record<string, unknown>;
  expect(kept["runId"]).toBe("run_asg_a1b2");
  expect(kept["inference"]).toEqual({
    calls: 3,
    inputTokens: 20_000,
    outputTokens: 1_500,
    cachedInputTokens: 400,
    costMicros: 410_000,
  });
  // …and the in-flight row is gone: the receipt is the record of a run that ended.
  expect(await db.query(`SELECT run_id FROM run_progress`)).toEqual([]);
  clock = started;
});

test("a metered job at the model with nothing metered for ninety seconds is stalled, and one call clears it", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await loop.tick();
  fleet.journals("job_asg_a1b2", [progressed(2, started, RUN_STAGES.atModel, "reception")]);

  // Eighty-nine seconds is a slow turn, not a stall.
  clock = started + 89_000;
  const patient = await loop.tick();
  expect(patient.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });

  clock = started + 91_000;
  const stalled = await loop.tick();
  expect(stalled.runs).toEqual({ running: 1, atModel: 1, stalled: 1 });
  const quiet = await db.query(`SELECT stalled FROM run_progress`);
  expect(quiet[0]?.["stalled"]).toBe(1n);

  // A metered call is the answer arriving: the flag goes, and the clock now runs from the call.
  fleet.journals("job_asg_a1b2", [
    progressed(2, started, RUN_STAGES.atModel, "reception"),
    called(5),
  ]);
  clock = started + 93_000;
  const answered = await loop.tick();
  expect(answered.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });
  const moving = await db.query(`SELECT stalled, calls, last_model FROM run_progress`);
  expect(moving[0]).toMatchObject({ stalled: 0n, calls: 1n, last_model: "claude-opus-4" });
  clock = started;
});

test("a job at the model that nothing meters is never called stalled", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    // The deployment this repository actually is: the review lane binds no inference service,
    // so nothing will ever meter a call of it (#256).
    plan: UNMETERED_PLAN,
    now: () => clock,
  });
  await loop.tick();
  fleet.journals("job_asg_a1b2", [progressed(2, started, RUN_STAGES.atModel, "reception")]);

  // Ten minutes at the model with nothing counted is what an unmetered review looks like from
  // the hub: the run is at the model, and "nothing metered" says nothing about it at all.
  clock = started + 600_000;
  const quiet = await loop.tick();
  expect(quiet.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });
  const row = await db.query(`SELECT stage, stalled FROM run_progress`);
  expect(row[0]).toMatchObject({ stage: "at the model", stalled: 0n });
  clock = started;
});

test("a cycle a settlement woke folds nothing and says nothing about a running job", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const deps = {
    store,
    coordinator: draws as unknown as Coordinator,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  };
  const woken = conductor({ ...deps, jobs: fleet });
  await woken.tick();
  fleet.journals("job_asg_a1b2", [
    progressed(2, started, RUN_STAGES.atModel, "reception"),
    called(5, { inputTokens: 1_000, outputTokens: 200, cachedInputTokens: 50, costMicros: 30_000 }),
  ]);
  clock = started + 30_000;
  await woken.tick();

  // The hook's slice has no `follow`, so this cycle cannot read a running job at all. Another
  // call arrives in the ring and nothing folds it: the row stands as the dispatch left it, the
  // report repeats that row, and no note claims a thing about the job either way.
  fleet.journals("job_asg_a1b2", [
    progressed(2, started, RUN_STAGES.atModel, "reception"),
    called(5, { inputTokens: 1_000, outputTokens: 200, cachedInputTokens: 50, costMicros: 30_000 }),
    called(7, { inputTokens: 9_000, outputTokens: 900, cachedInputTokens: 0, costMicros: 90_000 }),
  ]);
  clock = started + 60_000;
  const settledWake = await conductor({ ...deps, jobs: hookWoken(fleet) }).tick();
  expect(settledWake.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });
  expect(settledWake.notes.filter((note) => note.includes("job_asg_a1b2"))).toEqual([]);
  const held = await db.query(`SELECT calls, input_tokens, updated_at FROM run_progress`);
  expect(held[0]).toMatchObject({
    calls: 1n,
    input_tokens: 1_000n,
    // Not even the fold's own clock moved: nothing was written.
    updated_at: new Date(started + 30_000).toISOString(),
  });

  // And the next dispatch-woken cycle folds what the hook could not see.
  clock = started + 90_000;
  await woken.tick();
  expect((await db.query(`SELECT calls FROM run_progress`))[0]?.["calls"]).toBe(2n);
  clock = started;
});

test("a running job that has said nothing has no in-flight row to read", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await loop.tick();

  // The ring holds nothing — a job the owner has not launched yet, or one inside the five-second
  // window its first frame is coalesced in. A row of empty strings would read in the panel as a
  // blank stage over a clock counting from this fold, so there is no row: `runProgress()`
  // answers null for it and the panel says "no word yet".
  clock = started + 45_000;
  const silent = await loop.tick();
  expect(silent.runs).toEqual({ running: 1, atModel: 0, stalled: 0 });
  expect(await db.query(`SELECT run_id FROM run_progress`)).toEqual([]);

  // The ring keeps the newest frames and drops the rest, so a job that outran the loop has a
  // prefix nobody folded. The hub says so with `firstSeq`, and the note names the sequence and
  // what it costs the running total — once, not once a page.
  fleet.journals("job_asg_a1b2", [
    progressed(300, started + 50_000, RUN_STAGES.atModel, "reception"),
    called(301, { costMicros: 120_000 }),
  ]);
  clock = started + 60_000;
  const short = await loop.tick();
  expect(short.notes.filter((note) => note.includes("was not retained"))).toEqual([
    "job job_asg_a1b2: progress before seq 300 was not retained; its spend so far is short by " +
      "what those frames carried",
  ]);
  const folded = await db.query(`SELECT stage, calls, seq FROM run_progress`);
  expect(folded[0]).toMatchObject({ stage: "at the model", calls: 1n, seq: 301n });
  clock = started;
});

test("a cycle draws, claims, requests the job, then ingests every output file it wrote", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const requesting = await loop.tick();
  expect(requesting.enabled).toBe(true);
  expect(requesting.requested).toEqual([
    {
      runId: "run_asg_a1b2",
      jobId: "job_asg_a1b2",
      machineId: "dev-01",
      claimId: "clm_asg_a1b2",
      recordId: "hyp_00000001",
      role: "reception",
      lane: "coverage",
    },
  ]);
  expect(requesting.stop?.reason).toBe("no-candidates");
  expect(requesting.pending).toBe(1);

  // The claim is taken before the job is executed and carries the job it authorized.
  expect(draws.claimed).toEqual([
    { assignmentId: "asg_a1b2", runId: requesting.cycleRunId, jobId: "job_asg_a1b2" },
  ]);

  const launch = fleet.launched[0];
  expect(launch?.operationId).toBe(OPERATIONS.evaluate);
  expect(launch?.machineId).toBe("dev-01");
  expect(launch?.installationRevision).toBe("rev-7");
  expect(launch?.outputs).toEqual([
    { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: ["job_asg_a1b2"] },
  ]);
  const document = JSON.parse(String(launch?.input[INPUT_FIELD])) as Record<string, unknown>;
  expect(document["runId"]).toBe("run_asg_a1b2");
  expect(document["assignment"]).toMatchObject({
    id: "clm_asg_a1b2",
    recordId: "hyp_00000001",
    role: "reception",
    lane: "coverage",
    fence: 1,
    blinded: true,
  });
  expect(document["recipe"]).toEqual(PLAN.recipes["reception"]);
  // The blinded projection carries the record and its cited sessions, and nothing about how
  // the record has been received.
  expect(document["target"]).toMatchObject({ id: "hyp_00000001", kind: "hypothesis" });
  expect(JSON.stringify(document["target"])).not.toContain("vote");
  expect(document["sources"]).toMatchObject([{ selector: "omp/s1", snapshot: "snap-1" }]);

  const queued = await db.query(`SELECT closure, records FROM runs WHERE id = 'run_asg_a1b2'`);
  expect(queued[0]).toEqual({ closure: null, records: 0n });

  // The machine finishes and seals its files; the next cycle ingests them.
  fleet.finish("job_asg_a1b2", 0, outputs("run_asg_a1b2"));
  const ingesting = await loop.tick();
  expect(ingesting.ingested).toEqual([
    {
      runId: "run_asg_a1b2",
      jobId: "job_asg_a1b2",
      closure: "completed",
      costUsd: 0.42,
      rows: {
        [JOB_OUTPUT_FILES.sessions]: 1,
        [JOB_OUTPUT_FILES.records]: 1,
        [JOB_OUTPUT_FILES.edges]: 1,
        [JOB_OUTPUT_FILES.statusEvents]: 1,
        [JOB_OUTPUT_FILES.assessments]: 1,
        [JOB_OUTPUT_FILES.filings]: 1,
        [JOB_OUTPUT_FILES.questions]: 1,
        [JOB_OUTPUT_FILES.plans]: 1,
        [JOB_OUTPUT_FILES.steeringReplies]: 1,
      },
      skipped: 0,
    },
  ]);
  expect(ingesting.notes).toEqual([]);
  expect(ingesting.pending).toBe(0);

  for (const [table, count] of [
    ["records", 2],
    ["edges", 2],
    ["status_events", 1],
    ["assessments", 1],
    ["filings", 1],
    ["questions", 1],
    ["plans", 1],
    ["steering", 1],
  ] as const) {
    const rows = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM ${table}`);
    expect(`${table}=${String(rows[0]?.n)}`).toBe(`${table}=${String(count)}`);
  }

  // A rescan that names no snapshot column leaves the archive's snapshot alone.
  const session = await db.query<{ title: string; snapshot_id: string | null }>(
    `SELECT title, snapshot_id FROM sessions WHERE selector = 'omp/s1'`,
  );
  expect(session[0]).toEqual({ title: "a better title", snapshot_id: "snap-1" });

  const run = await db.query(
    `SELECT closure, cost_usd, tokens, records, finished_at, job_id FROM runs WHERE id = 'run_asg_a1b2'`,
  );
  expect(run[0]).toEqual({
    closure: "completed",
    cost_usd: 0.42,
    tokens: 12345n,
    records: 1n,
    finished_at: "2026-09-12T09:05:00Z",
    job_id: "job_asg_a1b2",
  });

  // The claim settles at what the receipt says the run cost, not at what the draw reserved.
  expect(ingesting.settled).toEqual([
    {
      claimId: "clm_asg_a1b2",
      outcome: "completed",
      cost: 0.42,
      overrun: false,
      refused: null,
      reason: null,
    },
  ]);
  expect(draws.finished).toEqual([
    {
      id: "clm_asg_a1b2",
      runId: requesting.cycleRunId,
      fence: 1n,
      cost: 0.42,
      outcome: "completed",
    },
  ]);
  const claim = await db.query(
    `SELECT actual_cost, outcome FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ actual_cost: 0.42, outcome: "completed" });
});

test("a cycle names its online machines to the coordinator, and no machine takes more than its bound", async () => {
  const db = openDatabase();
  await seed(db);
  // A second enrolled machine, known because it holds a session of its own.
  await db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, title, snapshot_id, seen_at)
     VALUES ('omp/s2', 'dev-02', 'omp', 's2', 'a second host', 'snap-2', '2026-09-01T00:00:00Z')`,
  );
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  // A drain's overlay, at its narrowest: one assignment per machine.
  draws.overlay = { concurrentPerMachine: 1 };
  draws.pending = [
    { ...ASSIGNMENT },
    { ...ASSIGNMENT, id: "asg_c3d4" },
    { ...ASSIGNMENT, id: "asg_e5f6" },
  ];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const cycle = await loop.tick();
  // The coordinator is told the fleet, because its batch is a bound PER MACHINE and the
  // deployment's cap is that bound over these machines (#260).
  expect(draws.offered[0]).toEqual(["dev-01", "dev-02"]);

  // dev-01 cites the record's session and takes the first job; the second goes to dev-02 rather
  // than to the machine that is already at its bound — which is what makes the bound real, and
  // is the 2026-09-13 failure inverted: thirty-six draws on one host.
  expect(cycle.requested.map((job) => job.machineId)).toEqual(["dev-01", "dev-02"]);
  // The third has nowhere to go, and the refusal says so by name rather than as "no machine".
  expect(cycle.refused).toEqual([
    {
      assignmentId: "asg_e5f6",
      recordId: "hyp_00000001",
      reason: "no-machine",
      detail: "no online machine has a free slot under the bound of 1: dev-01, dev-02",
    },
  ]);
});

test("a job that died with no receipt abandons its claim at the reservation and closes its run", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  fleet.finish("job_asg_a1b2", 3, null);
  const report = await loop.tick();

  expect(report.ingested).toEqual([
    {
      runId: "run_asg_a1b2",
      jobId: "job_asg_a1b2",
      closure: "failed",
      costUsd: 0,
      rows: {},
      skipped: 0,
    },
  ]);
  // A job that fell over said nothing about what it spent before it did: the claim is released
  // so the batch slot goes back, and charged at what it reserved so a crash loop cannot spend
  // the day's allowance many times over.
  expect(report.settled).toEqual([
    {
      claimId: "clm_asg_a1b2",
      outcome: "abandoned",
      cost: 0.1,
      overrun: false,
      refused: null,
      reason: "job job_asg_a1b2 closed as failed and wrote no receipt",
    },
  ]);
  expect(draws.finished).toEqual([]);
  const claim = await db.query(
    `SELECT finished_at IS NOT NULL AS closed, outcome, actual_cost FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ closed: 1n, outcome: "abandoned", actual_cost: 0.1 });
  const run = await db.query(`SELECT closure, cost_usd FROM runs WHERE id = 'run_asg_a1b2'`);
  expect(run[0]).toEqual({ closure: "failed", cost_usd: null });
});

test("a job the hub cancelled abandons its claim on the next tick", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  // The operator stops a running review — or the machine's agent dies and the hub interrupts
  // it. Either way nothing was sealed and nobody will report what it cost.
  fleet.kill("job_asg_a1b2", "cancelled");
  const report = await loop.tick();

  expect(report.settled).toEqual([
    {
      claimId: "clm_asg_a1b2",
      outcome: "abandoned",
      cost: 0.1,
      overrun: false,
      refused: null,
      reason: "job job_asg_a1b2 closed as stopped and wrote no receipt",
    },
  ]);
  expect(draws.abandoned).toEqual([
    {
      id: "clm_asg_a1b2",
      fence: 1n,
      reason: "job job_asg_a1b2 closed as stopped and wrote no receipt",
    },
  ]);
  const claim = await db.query(
    `SELECT outcome, actual_cost, finished_at IS NOT NULL AS closed FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ outcome: "abandoned", actual_cost: 0.1, closed: 1n });
  // And the run is closed, so the next cycle has nothing left to reconcile.
  expect(report.pending).toBe(0);
});

test("an enabled policy registers the beat at its cadence; a disabled one makes the tick a no-op", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const registering = await loop.tick();
  expect(registering.schedule).toBe("registered");
  const registered = fleet.scheduled[0];
  expect(registered).toMatchObject({
    scheduleId: CONDUCTOR_SCHEDULE_ID,
    revision: "pol_1",
    operationId: BEAT_OPERATION,
    machineId: "dev-01",
    intervalMs: 900_000,
    deadlineMs: 900_000,
    offlinePolicy: "coalesce-one",
  });
  expect(registered?.firstNominalAt).toBe(clock + 900_000);
  // The beat's input is fixed at registration, so it carries no run id to collide on.
  expect(JSON.parse(String(registered?.input[INPUT_FIELD]))).toEqual({
    runId: "",
    machineId: "dev-01",
    roots: [],
    harnesses: [],
  });

  // A second cycle keeps the schedule it already has rather than churning it.
  const keeping = await loop.tick();
  expect(keeping.schedule).toBe("kept");
  expect(fleet.scheduled).toHaveLength(1);

  // The operator turns evaluation off: the schedule goes, and the cycle does nothing at all.
  draws.enabled = false;
  const drewBefore = draws.draws;
  const launchedBefore = fleet.launched.length;
  const idle = await loop.tick();
  expect(idle.enabled).toBe(false);
  expect(idle.schedule).toBe("unregistered");
  expect(fleet.disabled).toEqual([{ scheduleId: CONDUCTOR_SCHEDULE_ID, revision: "pol_1" }]);
  expect(idle.requested).toEqual([]);
  expect(idle.ingested).toEqual([]);
  expect(idle.settled).toEqual([]);
  expect(idle.stop).toBeNull();
  expect(draws.draws).toBe(drewBefore);
  expect(fleet.launched).toHaveLength(launchedBefore);

  // And a tick under a disabled policy leaves the schedule absent rather than re-disabling it.
  const again = await loop.tick();
  expect(again.schedule).toBe("absent");
  expect(fleet.disabled).toHaveLength(1);
});

test("ingesting the same outputs twice changes nothing", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  fleet.execute({
    jobId: "job_twice",
    machineId: "dev-01",
    operationId: OPERATIONS.evaluate,
    input: {},
    outputs: [],
  });
  fleet.finish("job_twice", 0, outputs("run_twice"));
  const target = {
    runId: "run_twice",
    jobId: "job_twice",
    machineId: "dev-01",
    operationId: OPERATIONS.evaluate,
    outputs: fleet.status({ jobId: "job_twice" }).result?.outputs ?? [],
    closure: "completed",
  };

  const first = await ingestOutputs(store, fleet, target);
  expect(first.skipped).toBe(0);
  expect(first.notes).toEqual([]);
  const after = await snapshot(db);

  const second = await ingestOutputs(store, fleet, target);
  expect(second.rows).toEqual(first.rows);
  expect(await snapshot(db)).toBe(after);
  // Rows arrived, so the feed index is told each time: the store rebuilds it, not the loop.
  expect(store.touched).toBe(2);
});

test("an assessment the review contract refuses is not written, and the run still settles with its cost", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  // A machine half that drifted from the contract: an environment scoping nothing, which is the
  // rule the Go store and the Go review contract disagreed about (#263, post-mortem F8).
  const files = outputs("run_drift");
  const drifted = (files[JOB_OUTPUT_FILES.assessments] as Record<string, unknown>[])[0] ?? {};
  files[JOB_OUTPUT_FILES.assessments] = [{ ...drifted, payload: JSON.stringify({ environment: "dev-01" }) }];
  fleet.execute({
    jobId: "job_drift",
    machineId: "dev-01",
    operationId: OPERATIONS.evaluate,
    input: {},
    outputs: [],
  });
  fleet.finish("job_drift", 0, files);

  const result = await ingestOutputs(store, fleet, {
    runId: "run_drift",
    jobId: "job_drift",
    machineId: "dev-01",
    operationId: OPERATIONS.evaluate,
    outputs: fleet.status({ jobId: "job_drift" }).result?.outputs ?? [],
    closure: "completed",
  });

  expect(result.rows[JOB_OUTPUT_FILES.assessments]).toBe(0);
  expect(result.skipped).toBe(1);
  expect(result.notes.join(" | ")).toContain("schema:");
  expect(await db.query(`SELECT id FROM assessments`, [])).toEqual([]);
  // The row was refused; the RUN was not. Its receipt is what settles the claim, with the cost.
  expect(result.receipt?.costUsd).toBe(0.42);
  expect(await db.query(`SELECT cost_usd FROM runs WHERE id = 'run_drift'`, [])).toEqual([{ cost_usd: 0.42 }]);
  // Everything else the job wrote still landed: one refused row is not a refused output.
  expect(result.rows[JOB_OUTPUT_FILES.records]).toBe(1);
});

test("the beat's own job is ingested although the hub never requested it", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  // A scan the schedule started: no run row, and a run id the machine minted for itself.
  fleet.beat("schedule-abc", "dev-01", {
    [JOB_OUTPUT_FILES.sessions]: [
      {
        selector: "omp/s2",
        host: "dev-01",
        harness: "omp",
        source_id: "s2",
        seen_at: "2026-09-12T09:00:00Z",
      },
    ],
    [JOB_OUTPUT_FILES.receipt]: {
      runId: "run_minted_by_the_machine",
      kind: "scan",
      machineId: "dev-01",
      startedAt: "2026-09-12T08:59:00Z",
      finishedAt: "2026-09-12T09:00:00Z",
      closure: "completed",
      counts: { sessions: 1 },
    },
  });

  const report = await loop.tick();
  expect(report.ingested).toMatchObject([
    { runId: "run_minted_by_the_machine", jobId: "schedule-abc", closure: "completed" },
  ]);
  const rows = await db.query(
    `SELECT id, job_id, kind, closure FROM runs WHERE job_id = 'schedule-abc'`,
  );
  // The row is keyed to the OPERATION the beat ran as, not to the word its receipt used for
  // itself: `stop` addresses a job node with this column.
  expect(rows).toEqual([
    {
      id: "run_minted_by_the_machine",
      job_id: "schedule-abc",
      kind: OPERATIONS.scan,
      closure: "completed",
    },
  ]);
  const sessions = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM sessions`);
  expect(sessions[0]?.n).toBe(2n);

  // A second cycle sees the run is already recorded and does not ingest it again.
  const repeat = await loop.tick();
  expect(repeat.ingested).toEqual([]);
});

test("a draw the hub cannot place is refused rather than claimed", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  fleet.connected = false;
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }, { ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const report = await loop.tick();
  expect(report.refused).toEqual([
    {
      assignmentId: "asg_a1b2",
      recordId: "hyp_00000001",
      reason: "no-machine",
      detail: `no online machine has ${OPERATIONS.evaluate} ready`,
    },
  ]);
  expect(draws.claimed).toEqual([]);
  expect(fleet.launched).toEqual([]);
  // The cycle stops rather than spinning on an assignment it has already failed to place.
  expect(report.stop).toEqual({
    reason: "no-candidates",
    detail: "the cycle redrew asg_a1b2, which it could not dispatch",
  });
  expect(report.schedule).toBe("absent");
});

test("an output larger than one served chunk is read whole, and a row the table cannot hold is left out", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  fleet.execute({
    jobId: "job_big",
    machineId: "dev-01",
    operationId: OPERATIONS.explore,
    input: {},
    outputs: [],
  });
  // Six hundred records carry the archive well past the 64 KiB the engine serves at a time,
  // and past the 256-statement batch, so both the chunked read and the split write run.
  const many = Array.from({ length: 600 }, (_, at) => ({
    id: `fnd_${String(at).padStart(8, "0")}`,
    kind: "finding",
    root_id: `fnd_${String(at).padStart(8, "0")}`,
    seq: 0,
    actor_kind: "run",
    actor_id: "run_big",
    title: `finding ${String(at)}`,
    created_at: "2026-09-12T09:05:00Z",
    payload: JSON.stringify({ body: "x".repeat(120) }),
  }));
  fleet.finish("job_big", 0, {
    [JOB_OUTPUT_FILES.records]: many,
    // One row naming a column `records` does not have, and one that is not a row at all.
    [JOB_OUTPUT_FILES.edges]: [
      {
        id: "edg_bad",
        kind: "consolidates",
        from_kind: "finding",
        from_id: "fnd_00000000",
        to_kind: "hypothesis",
        to_id: "hyp_00000001",
        actor_kind: "run",
        actor_id: "run_big",
        created_at: "2026-09-12T09:05:00Z",
        weight: 3,
      },
      "not a row",
    ],
    [JOB_OUTPUT_FILES.receipt]: {
      runId: "run_big",
      kind: "explore",
      machineId: "dev-01",
      startedAt: "2026-09-12T09:00:00Z",
      finishedAt: "2026-09-12T09:05:00Z",
      closure: "completed",
      counts: { records: 600 },
    },
  });
  const sealed = fleet.status({ jobId: "job_big" }).result?.outputs ?? [];
  expect(sealed[0]?.bytes).toBeGreaterThan(65536);

  const result = await ingestOutputs(store, fleet, {
    runId: "run_big",
    jobId: "job_big",
    machineId: "dev-01",
    operationId: OPERATIONS.explore,
    outputs: sealed,
    closure: "completed",
  });
  expect(result.rows[JOB_OUTPUT_FILES.records]).toBe(600);
  expect(result.rows[JOB_OUTPUT_FILES.edges]).toBe(0);
  expect(result.skipped).toBe(2);
  const counted = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM records`);
  expect(counted[0]?.n).toBe(601n);
  const edges = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM edges WHERE id = 'edg_bad'`);
  expect(edges[0]?.n).toBe(0n);
});

test("a new policy version re-registers the beat instead of leaving two firing", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  const loop = conductor({
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  draws.version = "pol_2";
  const report = await loop.tick();

  expect(report.schedule).toBe("registered");
  expect(fleet.disabled).toEqual([{ scheduleId: CONDUCTOR_SCHEDULE_ID, revision: "pol_1" }]);
  expect(fleet.scheduled.map((row) => row.revision)).toEqual(["pol_1", "pol_2"]);
  expect(fleet.schedules()).toHaveLength(1);
});

test("an output the hub cannot read closes its run instead of being retried for ever", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  fleet.seal("job_asg_a1b2", Buffer.alloc(1024, 0x41));
  const report = await loop.tick();

  expect(report.notes[0]).toContain("outputs were refused");
  const run = await db.query(`SELECT closure FROM runs WHERE id = 'run_asg_a1b2'`);
  expect(run[0]).toEqual({ closure: "failed" });
  // The job exited cleanly, so it told us what it spent — nothing — and its claim is finished
  // at zero rather than charged for a review nobody can read.
  expect(report.settled).toEqual([
    {
      claimId: "clm_asg_a1b2",
      outcome: "failed",
      cost: 0,
      overrun: false,
      refused: null,
      reason: null,
    },
  ]);
  // And the next cycle has nothing left to reconcile.
  const next = await loop.tick();
  expect(next.ingested).toEqual([]);
  expect(next.notes).toEqual([]);
});

test("what a scan catalogued as folders is asked of the host, once per folder", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const folders = new Folders();
  const draws = new Draws(db);
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: folders,
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // A beat's catalogue of dev-01: two sessions of one checkout, one of a folder that is not a
  // repository, and one whose "workspace" is Claude's lossy project-directory name rather than
  // a path. The scan ran inside a job where none of the three were mounted, so every row it
  // shipped carries the sandbox's own prose instead of an identity.
  const catalogued = (selector: string, workspace: string): Record<string, unknown> => ({
    selector,
    host: "dev-01",
    harness: "omp",
    source_id: selector,
    workspace,
    repository_identity: null,
    repository_remote: null,
    repository_reason: "workspace absent on this host",
    seen_at: "2026-09-12T09:00:00Z",
  });
  fleet.beat("scan-1", "dev-01", {
    [JOB_OUTPUT_FILES.sessions]: [
      catalogued("omp/a", "/home/alex/babel"),
      catalogued("omp/b", "/home/alex/babel"),
      catalogued("omp/c", "/home/alex/notes"),
      catalogued("claude/d", "-home-alex-babel"),
    ],
  });
  folders.facts["/home/alex/babel"] = {
    path: "/home/alex/babel",
    identity: "/home/alex/babel/.git",
    remote: "github.com/atyrode/babel",
    reason: "repository",
    observedAt: clock,
  };

  const catalogue = async (): Promise<readonly SqlRow[]> =>
    await db.query(
      `SELECT selector, repository_identity AS identity, repository_remote AS remote,
              repository_reason AS reason
         FROM sessions WHERE workspace IS NOT NULL ORDER BY selector`,
    );
  const asWritten = [
    { selector: "claude/d", identity: null, remote: null, reason: "workspace absent on this host" },
    { selector: "omp/a", identity: null, remote: null, reason: "workspace absent on this host" },
    { selector: "omp/b", identity: null, remote: null, reason: "workspace absent on this host" },
    { selector: "omp/c", identity: null, remote: null, reason: "workspace absent on this host" },
  ];

  // A HOST THAT CANNOT BE ASKED is asked ONCE, not once per folder — offline is a fact about
  // the machine — and the rows it would have answered for are left exactly as the scan wrote
  // them rather than stamped with a refusal.
  folders.refusal = "dev-01 is not connected";
  const refused = await loop.tick();
  expect(refused.ingested).toMatchObject([{ jobId: "scan-1" }]);
  expect(folders.asked).toEqual(["dev-01:/home/alex/babel"]);
  expect(refused.notes).toEqual([
    "dev-01 could not say what /home/alex/babel is: dev-01 is not connected",
  ]);
  expect(await catalogue()).toEqual(asWritten);

  // The next tick asks again, because a refusal wrote nothing that says the folder was asked
  // about. One question per DISTINCT folder answers every session standing in it, and the
  // project-directory name is never asked about at all: it is not a path.
  folders.refusal = null;
  folders.asked.length = 0;
  const identified = await loop.tick();

  expect(identified.ingested).toEqual([]);
  expect(identified.notes).toEqual([]);
  expect(folders.asked).toEqual(["dev-01:/home/alex/babel", "dev-01:/home/alex/notes"]);
  expect(await catalogue()).toEqual([
    { selector: "claude/d", identity: null, remote: null, reason: "workspace absent on this host" },
    {
      selector: "omp/a",
      identity: "/home/alex/babel/.git",
      remote: "github.com/atyrode/babel",
      reason: "repository",
    },
    {
      selector: "omp/b",
      identity: "/home/alex/babel/.git",
      remote: "github.com/atyrode/babel",
      reason: "repository",
    },
    // A folder that is no checkout is a FACT about it, and the reason is what records it.
    { selector: "omp/c", identity: null, remote: null, reason: "not_a_repository" },
  ]);

  // And a folder the host has answered for is not asked about again on every tick after.
  folders.asked.length = 0;
  await loop.tick();
  expect(folders.asked).toEqual([]);
});

test("a posting the machine refuses abandons its claim in the same breath", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  fleet.refusal = "dev-01 has no room for another job";
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const report = await loop.tick();

  // The claim was taken before the job existed, so the refusal has to give it back here: the
  // reaper would, but a whole lease later, with the slot held by a job that never ran.
  expect(report.refused).toEqual([
    {
      assignmentId: "asg_a1b2",
      recordId: "hyp_00000001",
      reason: "refused-job",
      detail: "dev-01 has no room for another job",
    },
  ]);
  expect(draws.abandoned).toEqual([
    {
      id: "clm_asg_a1b2",
      fence: 1,
      reason: "the job was never posted: dev-01 has no room for another job",
    },
  ]);
  expect(draws.finished).toEqual([]);
  const claim = await db.query(
    `SELECT outcome, actual_cost, finished_at IS NOT NULL AS closed FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ outcome: "abandoned", actual_cost: 0.1, closed: 1n });
  // No job was posted, so no run row was written for one.
  const runs = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM runs`);
  expect(runs[0]?.n).toBe(0n);
});

test("a posting the hub refuses at admission is reported with the reason the hub named", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  // The hub admitted nothing: the operation's `limits.concurrentJobs` is full on that machine,
  // so the job is `refused` with no result at all and its reason is on the authority decision.
  fleet.decision = "concurrency_limit";
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const report = await loop.tick();

  // "dev-01 refused job_…" is a sentence nobody can act on; the fleet being at the ceiling this
  // plugin's manifest declares is a bound to raise or a drain to slow (#281).
  expect(report.refused).toEqual([
    {
      assignmentId: "asg_a1b2",
      recordId: "hyp_00000001",
      reason: "refused-job",
      detail: "dev-01 refused job_asg_a1b2: concurrency_limit",
    },
  ]);
  expect(draws.abandoned).toEqual([
    {
      id: "clm_asg_a1b2",
      fence: 1,
      reason: "the job was never posted: dev-01 refused job_asg_a1b2: concurrency_limit",
    },
  ]);
});

test("the reaper releases a grant whose job was never posted, once its lease has run out", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // A grant nothing will ever match `WHERE job_id = ?`: the cycle that took it died between the
  // claim and the posting. One inside its lease, one past it.
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                        reserved_cost, granted_at, expires_at)
     VALUES ('clm_stale', 'hyp_00000001', 'reception', 'coverage', 'pol_1', NULL, 'cyc_dead', 1,
             0.1, ?, ?)`,
    [new Date(clock - 20 * 60_000).toISOString(), new Date(clock - 5 * 60_000).toISOString()],
  );
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                        reserved_cost, granted_at, expires_at)
     VALUES ('clm_fresh', 'hyp_00000001', 'evidence', 'coverage', 'pol_1', NULL, 'cyc_now', 1,
             0.1, ?, ?)`,
    [new Date(clock - 60_000).toISOString(), new Date(clock + 840_000).toISOString()],
  );

  const report = await loop.tick();

  expect(draws.abandoned.map((row) => row.id)).toEqual(["clm_stale"]);
  expect(report.settled).toEqual([
    {
      claimId: "clm_stale",
      outcome: "abandoned",
      cost: 0.1,
      overrun: false,
      refused: null,
      reason: `granted at ${new Date(clock - 20 * 60_000).toISOString()} and never posted to a machine`,
    },
  ]);
  expect(report.notes.some((note) => note.startsWith("claim clm_stale abandoned:"))).toBe(true);
  const rows = await db.query<{ id: string; outcome: string | null; actual_cost: number | null }>(
    `SELECT id, outcome, actual_cost FROM claims ORDER BY id`,
  );
  expect(rows).toEqual([
    // A grant still inside its lease is a cycle that may yet post its job.
    { id: "clm_fresh", outcome: null, actual_cost: null },
    { id: "clm_stale", outcome: "abandoned", actual_cost: 0.1 },
  ]);
});

test("a run closed by another path leaves no claim behind: the reaper takes it on the next tick", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // The `stop` door closes the run itself and settles the claim in the same breath, and it
  // reads a refusal as nothing to do. This is the backstop for every such path: a closed run
  // is polled by nobody, so its claim would otherwise be held until the lease expired.
  await db.run(
    `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure, records, payload)
     VALUES ('run_stopped', ?, 'dev-01', 'job_stopped', ?, ?, 'stopped', 0, '{}')`,
    [OPERATIONS.evaluate, new Date(clock - 600_000).toISOString(), new Date(clock).toISOString()],
  );
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                        reserved_cost, granted_at, expires_at)
     VALUES ('clm_stopped', 'hyp_00000001', 'reception', 'coverage', 'pol_1', 'job_stopped',
             'cyc_1', 1, 0.1, ?, ?)`,
    [new Date(clock - 600_000).toISOString(), new Date(clock + 300_000).toISOString()],
  );

  const report = await loop.tick();

  expect(draws.abandoned).toEqual([
    { id: "clm_stopped", fence: 1n, reason: "job job_stopped is closed and its claim was left open" },
  ]);
  const claim = await db.query(
    `SELECT outcome, actual_cost FROM claims WHERE id = 'clm_stopped'`,
  );
  expect(claim[0]).toEqual({ outcome: "abandoned", actual_cost: 0.1 });
  expect(report.notes.some((note) => note.includes("clm_stopped abandoned"))).toBe(true);
});

test("a job the hub cannot report twice running loses its claim; once is a hiccup", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  // The machine holding the review falls off the fleet: the hub cannot answer for its job at
  // all, so no settlement will ever reach the claim.
  fleet.silent.add("job_asg_a1b2");

  const first = await loop.tick();
  expect(draws.abandoned).toEqual([]);
  expect(first.pending).toBe(1);

  const second = await loop.tick();
  expect(draws.abandoned).toEqual([
    {
      id: "clm_asg_a1b2",
      fence: 1n,
      reason: "the hub has not been able to report job job_asg_a1b2 for 2 cycles",
    },
  ]);
  expect(second.settled.map((row) => [row.claimId, row.outcome, row.cost])).toEqual([
    ["clm_asg_a1b2", "abandoned", 0.1],
  ]);

  // And once released it is released once: the next cycles say nothing more about it.
  const third = await loop.tick();
  expect(third.settled).toEqual([]);
  expect(draws.abandoned).toHaveLength(1);
});

// ---------------------------------------------------------------------- the park and the pulse

/** Three assignments of one record, so a cycle dispatches three jobs and takes three claims. */
function three(): Record<string, unknown>[] {
  return ["asg_p1", "asg_p2", "asg_p3"].map((id) => ({ ...ASSIGNMENT, id }));
}

/**
 * What a review the model answered and the contract then refused seals: a receipt with the
 * refusal's own code in its `reason`, and NO COST — the brokered lane meters at the owner
 * (ADR 0038), so a receipt that reports nothing about money is not a receipt that reports no
 * model. The refusal code is the evidence a model answered, and it is what keeps this run out
 * of the park's streak.
 */
function refusedReview(runId: string): Record<string, unknown> {
  return {
    [JOB_OUTPUT_FILES.receipt]: {
      runId,
      kind: "evaluate",
      machineId: "dev-01",
      recipeId: "reception-vote",
      role: "reception",
      startedAt: new Date(clock).toISOString(),
      finishedAt: new Date(clock).toISOString(),
      closure: "failed",
      reason: "schema: the reception result names a field the contract has not got",
      costUsd: 0,
      tokens: 0,
      counts: {},
    },
  };
}

test("three reviews the model answered and the contract refused are spend, not a parked loop", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = three();
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const dispatching = await loop.tick();
  expect(dispatching.requested).toHaveLength(3);

  // Each one reached the model and had its submission refused. The machine exits cleanly: a
  // refused submission is a recipe to review, not a boundary that broke.
  clock += 60_000;
  for (const job of dispatching.requested) fleet.finish(job.jobId, 0, refusedReview(job.runId));

  const settling = await loop.tick();
  expect(settling.settled.map((row) => [row.outcome, row.cost])).toEqual([
    ["failed", 0],
    ["failed", 0],
    ["failed", 0],
  ]);
  // The loop is not parked and drew again: three refusals are three answers Babel paid for.
  expect(settling.parked).toBe(null);
  expect(settling.notes.some((note) => note.includes("parked"))).toBe(false);
  expect(settling.stop?.reason).toBe("no-candidates");
  // …and the pulse says what they were, by the code `results.ts` names.
  expect(settling.pulse.tick.refusals).toEqual({ schema: 3 });
  expect(settling.pulse.today.refusals).toEqual({ schema: 3 });

  // A fourth cycle with nothing to draw still does not park: the window holds three refusals.
  const after = await loop.tick();
  expect(after.parked).toBe(null);
  expect(after.pulse.tick.refusals).toEqual({});
  expect(after.pulse.today.refusals).toEqual({ schema: 3 });
  clock = started;
});

test("three jobs that never reached the model park the loop, and an hour of quiet lifts it", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.pending = three();
  const loop = conductor({
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const dispatching = await loop.tick();
  expect(dispatching.requested).toHaveLength(3);

  // The three die where a broken machine kills them: mid-review, with no receipt, so nobody can
  // say a model was ever asked anything. Each claim is abandoned at its reservation (#271).
  clock += 60_000;
  for (const job of dispatching.requested) fleet.kill(job.jobId, "interrupted");
  draws.pending = [{ ...ASSIGNMENT, id: "asg_p4" }];
  const asked = draws.draws;

  const parking = await loop.tick();
  expect(parking.settled.map((row) => [row.outcome, row.cost])).toEqual([
    ["abandoned", 0.1],
    ["abandoned", 0.1],
    ["abandoned", 0.1],
  ]);
  expect(parking.parked?.barren).toBe(3);
  expect(parking.parked?.reason).toContain("reached no model and produced nothing");
  expect(parking.notes.some((note) => note.startsWith("the loop is parked:"))).toBe(true);
  // A parked cycle asks the coordinator for nothing at all, so the fourth assignment is still
  // waiting and no reservation was spent to learn the same thing a fourth time.
  expect(draws.draws).toBe(asked);
  expect(parking.requested).toEqual([]);
  expect(parking.stop).toBe(null);

  // An hour of quiet lifts it without an operator: a machine that has been fixed is tried again,
  // and a machine that has not re-parks after three more.
  clock += 61 * 60_000;
  const resumed = await loop.tick();
  expect(resumed.parked).toBe(null);
  expect(resumed.requested.map((job) => job.jobId)).toEqual(["job_asg_p4"]);
  clock = started;
});

test("the pulse counts why a draw returned nothing, by the coordinator's own reason", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const draws = new Draws(db);
  draws.stop = { reason: "batch", detail: "the cycle batch of 4 assignments is already claimed" };
  draws.declined = [
    {
      recordId: "hyp_00000001",
      role: "reception",
      reason: "claimed",
      detail: "held by another worker until 14:08",
    },
  ];
  const loop = conductor({
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const first = await loop.tick();
  expect(first.stop).toEqual({
    reason: "batch",
    detail: "the cycle batch of 4 assignments is already claimed",
  });
  expect(first.gaps).toEqual(draws.declined);
  expect(first.pulse.tick.gaps).toEqual({ batch: 1, claimed: 1 });
  expect(first.pulse.today.gaps).toEqual({ batch: 1, claimed: 1 });

  // The day accumulates across the wakes that make the cycles, which is why it is kept in the
  // plugin's keys rather than in the loop: every tick of a real day is a new conductor.
  const second = await loop.tick();
  expect(second.pulse.tick.gaps).toEqual({ batch: 1, claimed: 1 });
  expect(second.pulse.today.gaps).toEqual({ batch: 2, claimed: 2 });

  // …and it is a DAY: the tally starts again at the boundary the spend ledger is kept by.
  clock += 24 * 60 * 60_000;
  const tomorrow = await loop.tick();
  expect(tomorrow.pulse.today.gaps).toEqual({ batch: 1, claimed: 1 });

  // A disabled policy is a reason a cycle did not spend like any other, and the loop counts it
  // itself: the cycle never gets far enough to be told so by the coordinator.
  draws.enabled = false;
  const off = await loop.tick();
  expect(off.enabled).toBe(false);
  expect(off.pulse.tick.gaps).toEqual({ disabled: 1 });
  expect(off.pulse.today.gaps).toEqual({ batch: 1, claimed: 1, disabled: 1 });
  clock = started;
});
