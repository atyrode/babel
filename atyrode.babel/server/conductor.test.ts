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
  MATERIAL_SCHEMA,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  RUN_STAGES,
  type MaterialIndex,
} from "../contract.ts";
import type {
  CodeEngine,
  CodeJob,
  EngineAnswer,
  SessionRead,
  SessionRequest,
  SessionUsage,
} from "./engine/session.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { openStore as openReadStore, type BabelStore } from "../store/store.ts";
import type { Assignment, Coordinator, Fence, Policy } from "../store/coordinator.ts";
import {
  BEAT_OPERATION,
  CONDUCTOR_SCHEDULE_ID,
  conductor,
  type Conductor,
  type SettledClaim,
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

  A ROUTED CYCLE DRAWS REVIEWS THROUGH CODE. The coordinator selects the immutable record and
  role, grants one fenced claim, and the loop posts a blinded prompt through the profile the
  policy records. The acceptance below proves that dispatch and its settlement end to end;
  older ingestion and reaper scenarios seed in-flight rows directly because their subject is
  what happens after a job already exists.

  A RUN THAT REACHES A MODEL IS RECONCILED THROUGH CODE, not through `ctx.jobs`: its job is
  `atyrode.omp`'s and neither `jobs.status` nor `onJobSettled` is Babel's for it. So the fleet
  never sees such a job at all, and the tests for that lane drive CodeEngine instead — a fake
  whose `readSession` answers the shapes Code's own published schemas describe.
*/

// ---------------------------------------------------------------------------- a real database

const temporaries: string[] = [];

afterEach(() => {
  for (const directory of temporaries.splice(0))
    rmSync(directory, { recursive: true, force: true });
});

function openDatabase(): PluginDatabase {
  const directory = mkdtempSync(join(tmpdir(), "babel-conductor-"));
  temporaries.push(directory);
  const db = new Database(join(directory, "data.db"), {
    create: true,
    strict: true,
    safeIntegers: true,
  });
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
          (statement) =>
            db.prepare(statement.sql).all(...(bind(statement.params) as never[])) as SqlRow[],
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
  over: Partial<{
    model: string;
    inputTokens: number;
    outputTokens: number;
    cachedInputTokens: number;
    costMicros: number;
  }> = {},
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
  connected = true;
  /** Jobs the hub can no longer report at all: a machine that vanished mid-review. */
  readonly silent = new Set<string>();
  /** Every job this fake was asked to follow, and every subscription it was asked to close. */
  readonly followed: string[] = [];
  readonly closed: string[] = [];
  /**
   * EVERY IDENTIFIER THIS FAKE WAS HANDED, in order. It is what a test asserts to prove which
   * string the loop actually gave the hub: an id, or a name that could only ever be refused.
   */
  readonly identifiers: string[] = [];
  /**
   * THE MACHINES THE HUB HAS A ROW FOR, or null for a fake that answers about anything.
   *
   * `describe` is keyed on the machine id, and an identifier with no machine behind it is
   * REFUSED rather than reported as offline (atyrode/manifold#725) — which is what a host name
   * is. A test that sets this is a test against the hub that tells the truth.
   */
  enrolled: readonly string[] | null = null;

  describe(args: { machineId: string; pluginId: string }): MachineReadiness {
    expect(args.pluginId).toBe(BABEL_PLUGIN_ID);
    this.identifiers.push(args.machineId);
    if (this.enrolled !== null && !this.enrolled.includes(args.machineId)) {
      throw new Error(`machine_unknown: ${args.machineId}`);
    }
    return {
      connected: this.connected,
      operations: {
        [OPERATIONS.evaluate]: { ready: true, reason: null },
        [OPERATIONS.scan]: { ready: true, reason: null },
      },
      installation: {
        revision: "rev-7",
        artifactSha256: "a".repeat(64),
        enabled: true,
        ready: true,
      },
    };
  }

  /**
   * A POSTING NO CYCLE MAKES ANY MORE (#279). It is still here because {@link JobsSlice}
   * declares it and because `launched` staying empty is what pins the absence: a loop that
   * posted a review would be posting it nowhere. The ingestion tests that need a finished job
   * of their own call it directly, which is a fixture writing a job rather than a loop
   * launching one.
   */
  execute(args: JobLaunch): JobRunState {
    this.launched.push(args);
    this.running(args.jobId, args.machineId, args.operationId);
    return this.status({ jobId: args.jobId });
  }

  /** A job the hub is already reporting as started: what a posting used to leave behind. */
  running(jobId: string, machineId: string, operationId: string): void {
    this.jobs.set(jobId, {
      state: "started",
      exitCode: null,
      machineId,
      operationId,
      archive: null,
      journal: [],
      inference: null,
    });
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
    this.identifiers.push(args.machineId);
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
    const missingThrough =
      firstSeq === null ? (job.journal[job.journal.length - 1]?.seq ?? 0) : firstSeq - 1;
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

/**
 * The model a fixture's session says answered it. NOT A PUBLISHED ID, deliberately: the fixtures
 * here named `openrouter/stealth/union-alpha` until the provider withdrew it, and a test naming a
 * model that no longer exists sends the next reader looking for one. Nothing in this file depends
 * on which model answered — a receipt records the name it was given — so the name says what it
 * is.
 */
const FIXTURE_MODEL = "fixture/model-under-test";

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
} satisfies Assignment;

/**
 * THE HUB MACHINE ID, and it is deliberately not {@link HOST_NAME}.
 *
 * The hub keys `describe`, `listRuns` and `machines.repository` on the machine id and resolves
 * no names, so the two being different strings is what these tests are for: a loop that picked
 * its machine out of `sessions.host` picked a NAME, was answered `connected: false`, and
 * registered no cadence at all — silently, for a whole day (atyrode/manifold#725).
 */
const MACHINE = "05df7eaa-efd8-4d9c-bb0c-334706555c77";

/**
 * What the importer wrote into `sessions.host` for every row of the operator's corpus: the Go
 * deployment's own host name (`storage.json` `host_id`). It names a box; it identifies nothing
 * the hub can be asked about.
 */
const HOST_NAME = "dev-01";

/**
 * A POLICY'S REVIEW DISPATCH ROUTE, which is where the machine id comes from.
 *
 * Naming the machine is part of authorizing the spend, so the route is where a deployment
 * records which box its autonomous work runs on — and it is therefore also where the beat's
 * cadence is registered. Tests that want an UNROUTED policy leave `draws.review` alone.
 */
const ROUTE: NonNullable<Policy["review"]> = {
  machineId: MACHINE,
  profile: { containerId: "ctr_union", expectedRevision: 1 },
  roleRecipes: {
    reception: "babel-triages-the-queue",
    evidence: "babel-triages-the-queue",
    challenge: "babel-triages-the-queue",
    comparison: "babel-triages-the-queue",
    outcome: "babel-triages-the-queue",
    relevance: "babel-triages-the-queue",
    filing: "babel-triages-the-queue",
    backlog: "babel-triages-the-queue",
  },
  recipes: [
    {
      id: "babel-triages-the-queue",
      version: 2,
      body: "Assess the assigned record under the role contract.",
    },
  ],
};

class Draws {
  /**
   * How many times a cycle asked this coordinator for work. IT IS NEVER MORE THAN ZERO (#279),
   * and the counter exists to say so: a loop that went back to drawing would move it.
   */
  draws = 0;
  readonly finished: { id: string; runId: string; fence: Fence; cost: number; outcome: string }[] =
    [];
  readonly abandoned: { id: string; fence: Fence; reason: string }[] = [];
  enabled = true;
  version = POLICY.version;
  /** Work this coordinator would hand out the moment anything asked it for some. */
  pending: Record<string, unknown>[] = [];
  review: Policy["review"] = undefined;
  claimFence = 1;

  constructor(private readonly db: PluginDatabase) {}

  async policy(): Promise<Record<string, unknown>> {
    const standing = {
      ...POLICY,
      enabled: this.enabled,
      version: this.version,
      ...(this.review === undefined ? {} : { review: this.review }),
    };
    return {
      policy: standing,
      standing,
      overlay: null,
      source: "stored",
      version: this.version,
      recordedAt: clock,
    };
  }

  /**
   * `coordinator.draw`, which no cycle calls: a claim taken for work nobody can post holds a
   * record under a fence and a batch slot for a whole lease, and is then abandoned once the
   * posting is refused — the ghost-claim shape of 2026-09-13, for work nobody could have done.
   *
   * It still answers what it holds, because the day Code's door exists this is the fake that
   * hands work out, and every call is counted into {@link draws}.
   */
  async draw(): Promise<Record<string, unknown>> {
    this.draws += 1;
    const next = this.pending.shift();
    return next === undefined
      ? { outcome: "gap", gap: { reason: "no-candidates", detail: "nothing due" }, gaps: [] }
      : { outcome: "assignment", assignment: next, gaps: [] };
  }

  async open(): Promise<{ total: number; byMachine: Record<string, number> }> {
    const rows = await this.db.query<{ machine_id: string | null; n: bigint }>(
      `SELECT r.machine_id AS machine_id, COUNT(*) AS n
         FROM claims c LEFT JOIN runs r ON r.job_id = c.job_id
        WHERE c.finished_at IS NULL GROUP BY r.machine_id`,
    );
    const byMachine: Record<string, number> = {};
    let total = 0;
    for (const row of rows) {
      const count = Number(row.n);
      total += count;
      if (row.machine_id !== null) byMachine[row.machine_id] = count;
    }
    return { total, byMachine };
  }

  async claim(request: {
    assignment: Assignment;
    runId: string;
  }): Promise<Record<string, unknown>> {
    const at = new Date(clock).toISOString();
    const expires = new Date(clock + POLICY.leaseSeconds * 1000).toISOString();
    await this.db.run(
      `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                          reserved_cost, actual_cost, granted_at, expires_at, finished_at, outcome)
       VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, ?, ?, NULL, NULL)`,
      [
        request.assignment.id,
        request.assignment.recordId,
        request.assignment.role,
        request.assignment.lane,
        request.assignment.policyVersion,
        request.runId,
        this.claimFence,
        request.assignment.reservedCost,
        at,
        expires,
      ],
    );
    return {
      outcome: "granted",
      claim: {
        id: request.assignment.id,
        recordId: request.assignment.recordId,
        role: request.assignment.role,
        lane: request.assignment.lane,
        policyVersion: request.assignment.policyVersion,
        jobId: null,
        runId: request.runId,
        fence: this.claimFence,
        reservedCost: request.assignment.reservedCost,
        actualCost: null,
        grantedAt: clock,
        expiresAt: clock + POLICY.leaseSeconds * 1000,
        finishedAt: null,
        outcome: null,
      },
    };
  }

  async bind(request: {
    id: string;
    runId: string;
    fence: Fence;
    jobId: string;
  }): Promise<Record<string, unknown>> {
    await this.db.run(`UPDATE claims SET job_id = ? WHERE id = ? AND run_id = ? AND fence = ?`, [
      request.jobId,
      request.id,
      request.runId,
      request.fence,
    ]);
    const rows = await this.db.query<{
      record_id: string;
      role: string;
      lane: string;
      policy_version: string;
      reserved_cost: number;
      granted_at: string;
      expires_at: string;
    }>(
      `SELECT record_id, role, lane, policy_version, reserved_cost, granted_at, expires_at
         FROM claims WHERE id = ?`,
      [request.id],
    );
    const row = rows[0];
    if (row === undefined) {
      return { outcome: "refused", refusal: { reason: "not-found", detail: "missing claim" } };
    }
    return {
      outcome: "bound",
      claim: {
        id: request.id,
        recordId: row.record_id,
        role: row.role,
        lane: row.lane,
        policyVersion: row.policy_version,
        jobId: request.jobId,
        runId: request.runId,
        fence: Number(request.fence),
        reservedCost: row.reserved_cost,
        actualCost: null,
        grantedAt: Date.parse(row.granted_at),
        expiresAt: Date.parse(row.expires_at),
        finishedAt: null,
        outcome: null,
      },
    };
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
  async abandon(request: {
    id: string;
    fence: Fence;
    reason: string;
  }): Promise<Record<string, unknown>> {
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
}

/**
 * The plan a cycle runs under here, with evaluate METERED: a stall is only judgeable of a run
 * whose job carries a metered binding, and a review Code posts is one. {@link UNMETERED_PLAN}
 * is the same plan for a deployment whose jobs bind no inference service at all, which is every
 * operation THIS bundle declares (#256, #279).
 */
const PLAN: RunPlan = {
  metered: { [OPERATIONS.evaluate]: true },
  limits: { timeoutMs: 900000, memoryBytes: 2147483648, processes: 64, outputBytes: 67108864 },
};

const UNMETERED_PLAN: RunPlan = { ...PLAN, metered: {} };

// ---------------------------------------------------------------------------- the corpus

async function seed(db: PluginDatabase): Promise<void> {
  await db.batch([
    {
      sql: `INSERT INTO sessions(selector, host, harness, source_id, title, snapshot_id, seen_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)`,
      params: ["omp/s1", HOST_NAME, "omp", "s1", "first title", "snap-1", "2026-09-01T00:00:00Z"],
    },
    {
      sql: `INSERT INTO records(id, kind, root_id, seq, actor_kind, actor_id, title, created_at, payload)
            VALUES (?, 'hypothesis', ?, 0, 'run', 'run_seed', ?, ?, ?)`,
      params: [
        "hyp_00000001",
        "hyp_00000001",
        "The catalog forgets archived sessions",
        "2026-09-01T00:00:00Z",
        // An imported record carries the review state §4.12 withholds from a reviewer, so the
        // projection must strip it rather than the leak check refusing the dispatch (#301).
        JSON.stringify({ statement: "…", novelty: { rank: 3 }, reception: { support: 2 } }),
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

/** The run id a seeded claim is accounted to, standing in for the cycle that took it. */
const SEEDED_CYCLE = "cyc_seeded";

/**
 * A REVIEW ALREADY IN FLIGHT, written into the store rather than drawn.
 *
 * The loop posts nothing (#279), so the two rows a dispatch used to leave behind are seeded
 * here: the open `runs` row `reconcileRuns` polls every cycle, and the claim a settlement
 * closes. Neither is what the tests below are about — the fold, the receipt, the reaper and the
 * park all begin at a job that EXISTS — and the job is put on the fleet in the state the hub
 * would report it in, because `execute` is a verb no cycle calls.
 */
async function inFlight(
  db: PluginDatabase,
  fleet: Fleet,
  assignmentId: string = ASSIGNMENT.id,
): Promise<{ runId: string; jobId: string; claimId: string }> {
  const runId = `run_${assignmentId}`;
  const jobId = `job_${assignmentId}`;
  const claimId = `clm_${assignmentId}`;
  fleet.running(jobId, "dev-01", OPERATIONS.evaluate);
  await db.batch([
    {
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, records, payload)
            VALUES (?, ?, 'dev-01', ?, ?, 0, '{}')`,
      params: [runId, OPERATIONS.evaluate, jobId, new Date(clock).toISOString()],
    },
    {
      sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                               reserved_cost, granted_at, expires_at)
            VALUES (?, ?, 'reception', 'coverage', ?, ?, ?, 1, ?, ?, ?)`,
      params: [
        claimId,
        ASSIGNMENT.recordId,
        POLICY.version,
        jobId,
        SEEDED_CYCLE,
        ASSIGNMENT.reservedCost,
        new Date(clock).toISOString(),
        new Date(clock + POLICY.leaseSeconds * 1000).toISOString(),
      ],
    },
  ]);
  return { runId, jobId, claimId };
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
        payload: JSON.stringify({
          vote: "support",
          uncertainty: "the second criterion is untested",
        }),
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
        id: "qst_1",
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

// ------------------------------------------------------------------- the engine, which is Code

/** Code refused, in the shape `codeEngine` folds every refusal onto. */
function refusedByCode<T>(code: string, detail: string): EngineAnswer<T> {
  return { ok: false, code: code as never, refused: `${code}: ${detail}` };
}

/**
 * A HUB WITH NO CODE. Every test that predates the Code lane drives one, because every job
 * those tests wait on is Babel's own and none of them has a container: the engine is never
 * asked, and a fake that threw would hide a call this loop is not supposed to make.
 */
const NO_CODE: CodeEngine = {
  profiles: async () => await Promise.resolve(refusedByCode("engine_unavailable", "no Code here")),
  runSession: async () =>
    await Promise.resolve(refusedByCode("engine_unavailable", "no Code here")),
  readSession: async () => {
    throw new Error("a run with no container must never be read through Code");
  },
  cancelSession: async () => {
    throw new Error("a run with no container must never be cancelled through Code");
  },
};

/**
 * One Code session as `readSession` answers for it: where the job is, and what it yielded.
 *
 * `session: null` is the answer for a job Code posted that never sealed a transcript — still
 * running, cancelled, interrupted, or exited non-zero — and it is a SUCCESSFUL read, which is
 * the distinction the whole reconcile turns on.
 */
function sessionRead(over: {
  /** The hub's own states, so a fake cannot answer a word `JobStateSchema` does not have. */
  readonly state: SessionRead["job"]["state"];
  readonly jobId?: string;
  readonly sealed?: boolean;
  readonly finalMessage?: string;
  readonly exitCode?: number;
  readonly usage?: SessionUsage | null;
  readonly model?: string;
}): SessionRead {
  return {
    job: {
      jobId: over.jobId ?? "job_code_1",
      machineId: "dev-01",
      operationId: "atyrode.omp.session",
      pluginId: "atyrode.omp",
      state: over.state,
    },
    session:
      over.sealed === false
        ? null
        : {
            sessionId: "ses_1",
            sessionPath: "/home/job/.omp/agent/sessions/ses_1.jsonl",
            model: over.model ?? "anthropic/claude-opus-4-1",
            finalMessage: over.finalMessage ?? "",
            usage:
              over.usage === undefined
                ? { input: 12_000, output: 900, cacheRead: 400, cacheWrite: 0, cost: 0.31 }
                : over.usage,
            exitCode: over.exitCode ?? 0,
          },
  };
}

class ReviewCode implements CodeEngine {
  readonly posted: SessionRequest[] = [];
  read: SessionRead = sessionRead({ state: "started", sealed: false });

  async profiles(): Promise<EngineAnswer<readonly never[]>> {
    return await Promise.resolve({ ok: true, value: [] });
  }

  async runSession(request: SessionRequest): Promise<EngineAnswer<CodeJob>> {
    this.posted.push(request);
    return await Promise.resolve({
      ok: true,
      value: {
        jobId: "job_code_review",
        machineId: request.machineId,
        operationId: "atyrode.omp.session",
        pluginId: "atyrode.omp",
        state: "started",
      },
    });
  }

  async readSession(): Promise<EngineAnswer<SessionRead>> {
    return await Promise.resolve({ ok: true, value: this.read });
  }

  async cancelSession(): Promise<EngineAnswer<CodeJob>> {
    return await Promise.resolve({ ok: true, value: this.read.job });
  }
}

/** A Code that answers one read, then counts how many times it was asked. */
function codeAnswering(
  answer: () => EngineAnswer<SessionRead>,
): CodeEngine & { readonly asked: { containerId: string; jobId: string }[] } {
  const asked: { containerId: string; jobId: string }[] = [];
  return {
    asked,
    profiles: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
    runSession: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
    readSession: async (args) => {
      asked.push(args);
      return await Promise.resolve(answer());
    },
    cancelSession: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
  };
}

/** The `prepare` run whose sealed material a session read, as the hub ingested its receipt. */
function materialIndex(file: string, digest: string): MaterialIndex {
  return {
    schema: MATERIAL_SCHEMA,
    preparationId: "prep-1",
    preparedAt: new Date(clock).toISOString(),
    machineId: "dev-01",
    sessions: [
      {
        selector: "omp/s1",
        harness: "omp",
        sourceId: "s1",
        captureDigest: "c".repeat(64),
        sourceDigest: digest,
        file,
        records: 12,
        bytes: 4096,
      },
    ],
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await inFlight(db, fleet);
  // The job says where it is and the owner meters two calls against it. This is served through
  // `follow` and nothing else: the fake refuses `journal` for a running job the way the hub
  // does, so a fold that read one would fail here rather than pass against a friendly fake.
  fleet.journals("job_asg_a1b2", [
    progressed(1, started, RUN_STAGES.preparing, "reception: composing the prompt"),
    progressed(4, started + 20_000, RUN_STAGES.atModel, "reception"),
    called(6, {
      inputTokens: 12_000,
      outputTokens: 900,
      cachedInputTokens: 400,
      costMicros: 250_000,
    }),
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
    called(6, {
      inputTokens: 12_000,
      outputTokens: 900,
      cachedInputTokens: 400,
      costMicros: 250_000,
    }),
    called(9, {
      model: "claude-sonnet-4",
      inputTokens: 400,
      outputTokens: 100,
      cachedInputTokens: 0,
      costMicros: 30_000,
    }),
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await inFlight(db, fleet);
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
  const loop = conductor({
    engine: NO_CODE,
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
  await inFlight(db, fleet);
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
  const deps = {
    store,
    coordinator: draws as unknown as Coordinator,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  };
  const woken = conductor({ engine: NO_CODE, ...deps, jobs: fleet });
  await inFlight(db, fleet);
  fleet.journals("job_asg_a1b2", [
    progressed(2, started, RUN_STAGES.atModel, "reception"),
    called(5, { inputTokens: 1_000, outputTokens: 200, cachedInputTokens: 50, costMicros: 30_000 }),
  ]);
  clock = started + 30_000;
  await woken.tick();

  // The hook's slice has no `follow`, so this cycle cannot read a running job at all. Another
  // call arrives in the ring and nothing folds it: the row stands as the last cycle that could
  // read one left it, the report repeats that row, and no note claims a thing about the job.
  fleet.journals("job_asg_a1b2", [
    progressed(2, started, RUN_STAGES.atModel, "reception"),
    called(5, { inputTokens: 1_000, outputTokens: 200, cachedInputTokens: 50, costMicros: 30_000 }),
    called(7, { inputTokens: 9_000, outputTokens: 900, cachedInputTokens: 0, costMicros: 90_000 }),
  ]);
  clock = started + 60_000;
  const settledWake = await conductor({ engine: NO_CODE, ...deps, jobs: hookWoken(fleet) }).tick();
  expect(settledWake.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });
  expect(settledWake.notes.filter((note) => note.includes("job_asg_a1b2"))).toEqual([]);
  const held = await db.query(`SELECT calls, input_tokens, updated_at FROM run_progress`);
  expect(held[0]).toMatchObject({
    calls: 1n,
    input_tokens: 1_000n,
    // Not even the fold's own clock moved: nothing was written.
    updated_at: new Date(started + 30_000).toISOString(),
  });

  // And the next cycle that is served a `follow` folds what the hook could not see.
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await inFlight(db, fleet);

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

test("a settled job's every output file lands in the store, and its run and claim close on the receipt", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // A review already in flight, because no cycle posts one (#279). The machine finishes and
  // seals its files; the cycle that polls the job ingests them.
  await inFlight(db, fleet);
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
      runId: SEEDED_CYCLE,
      fence: 1n,
      cost: 0.42,
      outcome: "completed",
    },
  ]);
  const claim = await db.query(`SELECT actual_cost, outcome FROM claims WHERE id = 'clm_asg_a1b2'`);
  expect(claim[0]).toEqual({ actual_cost: 0.42, outcome: "completed" });
});

test("a job that died with no receipt abandons its claim at the reservation and closes its run", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await inFlight(db, fleet);
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await inFlight(db, fleet);
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
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
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
    machineId: MACHINE,
    intervalMs: 900_000,
    deadlineMs: 900_000,
    offlinePolicy: "coalesce-one",
  });
  expect(registered?.firstNominalAt).toBe(clock + 900_000);
  // The beat's input is fixed at registration, so it carries no run id to collide on.
  expect(JSON.parse(String(registered?.input[INPUT_FIELD]))).toEqual({
    runId: "",
    machineId: MACHINE,
    roots: [],
    harnesses: [],
  });
  // THE ONE POSTING BABEL STILL MAKES binds its own output location, so what a scan seals lands
  // where this hub ingests it from. The review job that used to carry the same binding is
  // Code's now (#279); the beat's is the loop's own.
  expect(registered?.outputs).toEqual([
    { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [BEAT_OPERATION] },
  ]);

  // A second cycle keeps the schedule it already has rather than churning it.
  const keeping = await loop.tick();
  expect(keeping.schedule).toBe("kept");
  expect(fleet.scheduled).toHaveLength(1);

  // The operator turns evaluation off: the schedule goes, and the cycle does nothing at all.
  draws.enabled = false;
  const idle = await loop.tick();
  expect(idle.enabled).toBe(false);
  expect(idle.schedule).toBe("unregistered");
  expect(fleet.disabled).toEqual([{ scheduleId: CONDUCTOR_SCHEDULE_ID, revision: "pol_1" }]);
  expect(idle.requested).toEqual([]);
  expect(idle.ingested).toEqual([]);
  expect(idle.settled).toEqual([]);
  // A disabled policy is the one cycle that reports no stop at all: it never gets as far as the
  // engine-pending verdict an enabled one answers with.
  expect(idle.stop).toBeNull();

  // And a tick under a disabled policy leaves the schedule absent rather than re-disabling it.
  const again = await loop.tick();
  expect(again.schedule).toBe("absent");
  expect(fleet.disabled).toHaveLength(1);

  // AND A FLEET NOBODY IS HOME ON REGISTERS NOTHING rather than failing the cycle: the beat is
  // one wake, and a policy turned back on with no machine to run it on leaves the schedule
  // absent until there is one.
  draws.enabled = true;
  fleet.connected = false;
  const offline = await loop.tick();
  expect(offline.schedule).toBe("absent");
  // AND IT NAMES THE MACHINE IT TRIED AND WHY. `absent` with nothing in `notes` is the shape
  // that hid this for a day: a cycle with no complaint in it, beside an empty `job_schedules`.
  expect(offline.notes).toEqual([`the beat cannot be registered: ${MACHINE} is offline`]);
  expect(fleet.scheduled).toHaveLength(1);
});

test("the beat is registered by machine id, and the name a session row holds is never asked about", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  // The hub as it answers once it refuses an unknown identifier instead of calling it offline:
  // one enrolled machine, reachable only by its id.
  fleet.enrolled = [MACHINE];
  const draws = new Draws(db);
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // AN IMPORTED CORPUS holds the host NAME on both columns the loop used to read for machines:
  // `sessions.host` from the seed, and `runs.machine_id`, which the same importer writes from
  // the same `--host`. Neither is an identifier the hub has a machine for.
  await db.run(
    `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure, records, payload)
     VALUES ('run_imported', ?, ?, 'job_imported', ?, ?, 'completed', 0, '{}')`,
    [
      OPERATIONS.explore,
      HOST_NAME,
      new Date(clock - 600_000).toISOString(),
      new Date(clock).toISOString(),
    ],
  );
  expect(await db.query(`SELECT DISTINCT host FROM sessions`)).toEqual([{ host: HOST_NAME }]);

  const report = await loop.tick();

  expect(report.schedule).toBe("registered");
  expect(fleet.scheduled[0]?.machineId).toBe(MACHINE);
  // THE WHOLE POINT. Every identifier this cycle handed the hub is the id the policy recorded;
  // the name is not among them. A cycle that asked about `dev-01` was answered
  // `connected: false`, found no usable host, and registered no cadence at all.
  expect([...new Set(fleet.identifiers)]).toEqual([MACHINE]);
  expect(report.notes).toEqual([]);
});

test("a cycle with no usable host for the beat says which machine it tried and why", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  fleet.enrolled = [MACHINE];
  const draws = new Draws(db);
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // AN ENABLED POLICY THAT NAMES NO MACHINE has no id to register a cadence on, and says so.
  // Reporting `absent` in silence here is what let a whole feature disappear: the cycle looked
  // healthy and `job_schedules` stayed empty.
  const unrouted = await loop.tick();
  expect(unrouted.schedule).toBe("absent");
  expect(unrouted.notes).toEqual([
    "the beat cannot be registered: policy pol_1 names no machine for its work, so there is no " +
      "machine id to register the cadence on",
  ]);
  expect(fleet.scheduled).toEqual([]);

  // A ROUTE THAT NAMES A HOST NAME reaches the hub as an identifier it has no machine for, and
  // the note carries the hub's own sentence beside the string that was sent.
  draws.review = { ...ROUTE, machineId: HOST_NAME };
  const unknown = await loop.tick();
  expect(unknown.schedule).toBe("absent");
  expect(unknown.notes).toEqual([
    `the beat cannot be registered: ${HOST_NAME} cannot be described: ` +
      `machine_unknown: ${HOST_NAME}`,
  ]);
  expect(fleet.scheduled).toEqual([]);

  // AND AN ENROLLED ONE WHOSE OPERATION IS NOT READY names the operation and the machine, which
  // is a different refusal from an offline host and has to read as one.
  draws.review = ROUTE;
  fleet.connected = false;
  const offline = await loop.tick();
  expect(offline.schedule).toBe("absent");
  expect(offline.notes).toEqual([`the beat cannot be registered: ${MACHINE} is offline`]);

  // …and the moment the machine the policy names can run it, the cadence is registered and the
  // cycle has nothing to complain about.
  fleet.connected = true;
  const registered = await loop.tick();
  expect(registered.schedule).toBe("registered");
  expect(registered.notes).toEqual([]);
  expect(fleet.scheduled[0]?.machineId).toBe(MACHINE);
});

test("folders catalogued under a host name are named in a note, not silently never identified", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  fleet.enrolled = [MACHINE];
  const folders = new Folders();
  const draws = new Draws(db);
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: folders,
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // An imported row: a real absolute workspace, recorded against the operator's host NAME.
  // `engine.machines.repository` is keyed on the machine id, so there is nobody to ask about it.
  await db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, workspace, seen_at)
     VALUES ('omp/imported', ?, 'omp', 'imported', '/home/alex/babel', '2026-09-01T00:00:00Z')`,
    [HOST_NAME],
  );

  const report = await loop.tick();

  expect(folders.asked).toEqual([]);
  expect(report.notes).toEqual([
    `the folders catalogued under ${HOST_NAME} are not asked about: sessions.host holds a host ` +
      "name there rather than a machine id, and the hub resolves no names",
  ]);
  // The row is left exactly as the import wrote it: nothing invents an identity for a folder on
  // a machine nobody could be asked about.
  expect(
    await db.query(`SELECT repository_reason FROM sessions WHERE selector = 'omp/imported'`),
  ).toEqual([{ repository_reason: null }]);
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
  files[JOB_OUTPUT_FILES.assessments] = [
    { ...drifted, payload: JSON.stringify({ environment: "dev-01" }) },
  ];
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
  expect(await db.query(`SELECT cost_usd FROM runs WHERE id = 'run_drift'`, [])).toEqual([
    { cost_usd: 0.42 },
  ]);
  // Everything else the job wrote still landed: one refused row is not a refused output.
  expect(result.rows[JOB_OUTPUT_FILES.records]).toBe(1);
});

test("the beat's own job is ingested although the hub never requested it", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  // A scan the schedule started: no run row, and a run id the machine minted for itself.
  fleet.beat("schedule-abc", MACHINE, {
    [JOB_OUTPUT_FILES.sessions]: [
      {
        selector: "omp/s2",
        host: MACHINE,
        harness: "omp",
        source_id: "s2",
        seen_at: "2026-09-12T09:00:00Z",
      },
    ],
    [JOB_OUTPUT_FILES.receipt]: {
      runId: "run_minted_by_the_machine",
      kind: "scan",
      machineId: MACHINE,
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
  const edges = await db.query<{ n: bigint }>(
    `SELECT COUNT(*) AS n FROM edges WHERE id = 'edg_bad'`,
  );
  expect(edges[0]?.n).toBe(0n);
});

test("a new policy version re-registers the beat instead of leaving two firing", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
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
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await inFlight(db, fleet);
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
  draws.review = ROUTE;
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: folders,
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // A beat's catalogue of the machine the policy routes to: two sessions of one checkout, one
  // of a folder that is not a repository, and one whose "workspace" is Claude's lossy
  // project-directory name rather than a path. The scan ran inside a job where none of the
  // three were mounted, so every row it shipped carries the sandbox's own prose instead of an
  // identity. The rows are keyed by the machine's ID, which is what `scan` records and what
  // makes the folder question askable at all.
  const catalogued = (selector: string, workspace: string): Record<string, unknown> => ({
    selector,
    host: MACHINE,
    harness: "omp",
    source_id: selector,
    workspace,
    repository_identity: null,
    repository_remote: null,
    repository_reason: "workspace absent on this host",
    seen_at: "2026-09-12T09:00:00Z",
  });
  fleet.beat("scan-1", MACHINE, {
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
  folders.refusal = "machine is not connected";
  const refused = await loop.tick();
  expect(refused.ingested).toMatchObject([{ jobId: "scan-1" }]);
  expect(folders.asked).toEqual([`${MACHINE}:/home/alex/babel`]);
  expect(refused.notes).toEqual([
    `${MACHINE} could not say what /home/alex/babel is: machine is not connected`,
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
  expect(folders.asked).toEqual([`${MACHINE}:/home/alex/babel`, `${MACHINE}:/home/alex/notes`]);
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

test("the reaper releases a grant whose job was never posted, once its lease has run out", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const loop = conductor({
    engine: NO_CODE,
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
    engine: NO_CODE,
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
    {
      id: "clm_stopped",
      fence: 1n,
      reason: "job job_stopped is closed and its claim was left open",
    },
  ]);
  const claim = await db.query(`SELECT outcome, actual_cost FROM claims WHERE id = 'clm_stopped'`);
  expect(claim[0]).toEqual({ outcome: "abandoned", actual_cost: 0.1 });
  expect(report.notes.some((note) => note.includes("clm_stopped abandoned"))).toBe(true);
});

test("a job the hub cannot report twice running loses its claim; once is a hiccup", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  await inFlight(db, fleet);
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

// ----------------------------------------------------------------------- drawn Code reviews

test("an enabled policy without a review route reserves nothing", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  // Work the coordinator would hand out the moment anything asked it for some.
  draws.pending = [{ ...ASSIGNMENT }];
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const report = await loop.tick();

  // An unrouted policy is refused before the coordinator is asked. It leaves neither a ghost
  // claim nor a Code session that nobody can reconcile.
  expect(report.enabled).toBe(true);
  expect(draws.draws).toBe(0);
  expect(draws.pending).toHaveLength(1);
  expect(report.requested).toEqual([]);
  expect(report.gaps).toEqual([]);

  // The stop names the missing policy route rather than claiming that Code itself is absent.
  expect(report.stop?.reason).toBe("unrouted");
  expect(report.stop?.detail).toContain("names no Code profile and machine");
  // Counted rather than narrated: "why did nothing happen today" is answered by the tally.
  expect(report.pulse.tick.gaps).toEqual({ unrouted: 1 });

  // AND NOTHING WAS TAKEN FOR IT: no claim row, and no posting.
  const claims = await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM claims`);
  expect(claims[0]?.n).toBe(0n);
  expect(fleet.launched).toEqual([]);
});

test("a drawn review is blinded, fenced, settled, and promotes granular refinements", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  draws.claimFence = 2;
  await db.batch([
    {
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure,
                             records, payload)
            VALUES (?, ?, 'dev-01', 'job_code_review_old', ?, ?, 'failed', 0, ?)`,
      params: [
        `run_${ASSIGNMENT.id}_1`,
        OPERATIONS.evaluate,
        new Date(clock - 1_000).toISOString(),
        new Date(clock).toISOString(),
        JSON.stringify({ epoch: 1 }),
      ],
    },
    {
      sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                               reserved_cost, actual_cost, granted_at, expires_at, finished_at, outcome)
            VALUES (?, ?, 'reception', 'coverage', ?, 'job_code_review_old', 'cyc_old', 1,
                    ?, ?, ?, ?, ?, 'abandoned')`,
      params: [
        `${ASSIGNMENT.id}~1`,
        ASSIGNMENT.recordId,
        POLICY.version,
        ASSIGNMENT.reservedCost,
        ASSIGNMENT.reservedCost,
        new Date(clock - 2_000).toISOString(),
        new Date(clock - 1_000).toISOString(),
        new Date(clock).toISOString(),
      ],
    },
  ]);
  const recipeId = "babel-triages-the-queue";
  draws.review = {
    machineId: MACHINE,
    profile: { containerId: "ctr_union", expectedRevision: 1 },
    roleRecipes: {
      reception: recipeId,
      evidence: recipeId,
      challenge: recipeId,
      comparison: recipeId,
      outcome: recipeId,
      relevance: recipeId,
      filing: recipeId,
      backlog: recipeId,
    },
    recipes: [
      {
        id: recipeId,
        version: 2,
        title: "Triage the queue",
        body: "Assess the assigned record under the role contract. Prefer a precise refinement over vague criticism.",
      },
    ],
  };
  draws.pending = [{ ...ASSIGNMENT }];
  const code = new ReviewCode();
  const loop = conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const posted = await loop.tick();
  expect(posted.requested).toEqual([
    {
      runId: `run_${ASSIGNMENT.id}_2`,
      jobId: "job_code_review",
      machineId: MACHINE,
      claimId: ASSIGNMENT.id,
      recordId: ASSIGNMENT.recordId,
      role: "reception",
      lane: "coverage",
    },
  ]);
  expect(code.posted).toHaveLength(1);
  expect(code.posted[0]?.prepareJobId).toBeUndefined();
  expect(code.posted[0]?.prompt).toContain("This initial assessment is blind");
  expect(code.posted[0]?.prompt).toContain('"statement": "…"');
  expect(code.posted[0]?.prompt).not.toContain("assessments");
  // The withheld keys the record itself carries never reach the reviewer, and their presence
  // does not stop the dispatch. ("reception" is the role's own name, so the prompt says it.)
  expect(code.posted[0]?.prompt).not.toContain("novelty");
  expect(code.posted[0]?.prompt).not.toContain('"support": 2');
  const held = await db.query<{ job_id: string; fence: bigint }>(
    `SELECT job_id, fence FROM claims WHERE id = ?`,
    [ASSIGNMENT.id],
  );
  expect(held).toEqual([{ job_id: "job_code_review", fence: 2n }]);

  code.read = sessionRead({
    jobId: "job_code_review",
    state: "exited",
    model: FIXTURE_MODEL,
    usage: { input: 4200, output: 300, cacheRead: 0, cacheWrite: 0, cost: 0 },
    finalMessage:
      "```json\n" +
      JSON.stringify({
        vote: "support",
        contributions: [
          {
            kind: "refinement",
            text: "The scope does not say whether archived sessions from every host are affected.",
            target: { path: "/payload/statement" },
            would_change: "The fleet catalog forgets archived sessions fetched from another host.",
          },
        ],
      }) +
      "\n```",
  });
  const settled = await loop.tick();
  expect(settled.notes).toEqual([]);
  expect(settled.settled.map((row) => [row.outcome, row.cost])).toEqual([["completed", 0]]);
  const reviewed = await store.record(ASSIGNMENT.recordId);
  expect(reviewed?.reception.byRole).toEqual([
    { role: "reception", support: 1, oppose: 0, unsure: 0, opposingRationales: [] },
  ]);
  const proposals = await db.query<{ id: string; payload: string }>(
    `SELECT id, payload FROM records WHERE kind = 'proposal'`,
  );
  expect(proposals).toHaveLength(1);
  expect(JSON.parse(proposals[0]?.payload ?? "{}")["refinement"]).toEqual({
    targetRecordId: ASSIGNMENT.recordId,
    targetRevisionId: ASSIGNMENT.recordId,
    targetPath: "/payload/statement",
    depth: 1,
    reason: "The scope does not say whether archived sessions from every host are affected.",
    replacement: "The fleet catalog forgets archived sessions fetched from another host.",
    sourceRole: "reception",
  });
  expect(
    await db.query<{ kind: string; to_id: string }>(
      `SELECT kind, to_id FROM edges WHERE from_id = ?`,
      [proposals[0]?.id ?? ""],
    ),
  ).toEqual([{ kind: "refines", to_id: ASSIGNMENT.recordId }]);
  expect(
    await db.query<{ id: string; payload: string }>(
      `SELECT id, payload FROM runs WHERE id LIKE ? ORDER BY id`,
      [`run_${ASSIGNMENT.id}_%`],
    ),
  ).toEqual([
    { id: `run_${ASSIGNMENT.id}_1`, payload: JSON.stringify({ epoch: 1 }) },
    {
      id: `run_${ASSIGNMENT.id}_2`,
      payload: expect.stringContaining(`"closure":"completed"`),
    },
  ]);
});

test("a review with one refused contribution records the rest, and its receipt counts the refusal", async () => {
  /*
    #305: seven drawn reviews on a non-reasoning model spent 202k tokens and recorded two. Five
    were discarded whole because one contribution broke a rule about itself — the vote and the
    good contributions beside it were paid for and thrown away. This is the run that used to
    close `failed` with nothing in the store.
  */
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const recipeId = "babel-triages-the-queue";
  draws.review = {
    machineId: MACHINE,
    profile: { containerId: "ctr_union", expectedRevision: 1 },
    roleRecipes: {
      reception: recipeId,
      evidence: recipeId,
      challenge: recipeId,
      comparison: recipeId,
      outcome: recipeId,
      relevance: recipeId,
      filing: recipeId,
      backlog: recipeId,
    },
    recipes: [
      { id: recipeId, version: 2, body: "Assess the assigned record under the role contract." },
    ],
  };
  draws.pending = [{ ...ASSIGNMENT }];
  const code = new ReviewCode();
  const loop = conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await loop.tick();

  code.read = sessionRead({
    jobId: "job_code_review",
    state: "exited",
    model: FIXTURE_MODEL,
    finalMessage:
      "```json\n" +
      JSON.stringify({
        vote: "oppose",
        contributions: [
          {
            kind: "objection",
            text: "the statement does not survive the archived case",
            alternatives: [{ kind: "hypothesis", id: "hyp_other" }],
          },
          { kind: "comment", text: "the scope should name the harness it was observed on" },
        ],
      }) +
      "\n```",
  });
  const settled = await loop.tick();

  expect(settled.settled.map((row) => row.outcome)).toEqual(["completed"]);
  const run = (
    await db.query<{ closure: string; payload: string }>(
      `SELECT closure, payload FROM runs WHERE id = ?`,
      [`run_${ASSIGNMENT.id}_1`],
    )
  )[0]!;
  expect(run.closure).toBe("completed");
  const receipt = JSON.parse(run.payload) as Record<string, unknown>;
  // The review stood, so nothing says it failed; what the contract refused is its own field, in
  // the same `<code>: <sentence>` shape a reason carries, and counted so a coverage rate can be
  // read off the receipts rather than guessed at.
  expect(receipt["reason"]).toBeUndefined();
  expect(receipt["refusedContributions"]).toEqual([
    {
      contribution: 1,
      reason: "schema: contribution 1 is an objection and may not name alternatives",
    },
  ]);
  expect((receipt["counts"] as Record<string, number>)["contributionsRefused"]).toBe(1);
  expect(settled.pulse.tick.refusals).toEqual({ schema: 1 });
  expect(settled.notes.join(" | ")).toContain("may not name alternatives");

  // AND THE JUDGEMENT IS DURABLE, minus the contribution the contract refused: the assessment
  // the store accepted carries the vote and the surviving contribution, and nothing of the
  // refused one in any form.
  const assessments = await db.query<{ vote: string; payload: string }>(
    `SELECT vote, payload FROM assessments WHERE record_id = ?`,
    [ASSIGNMENT.recordId],
  );
  expect(assessments).toHaveLength(1);
  expect(assessments[0]?.vote).toBe("oppose");
  const held = JSON.parse(assessments[0]?.payload ?? "{}") as { contributions: unknown[] };
  expect(held.contributions).toEqual([
    expect.objectContaining({
      kind: "comment",
      text: "the scope should name the harness it was observed on",
    }),
  ]);
  expect(assessments[0]?.payload).not.toContain("hyp_other");
});

test("a stale review completion retains usage without writing or settling the newer epoch", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const recipeId = "babel-triages-the-queue";
  draws.review = {
    machineId: MACHINE,
    profile: { containerId: "ctr_union", expectedRevision: 1 },
    roleRecipes: {
      reception: recipeId,
      evidence: recipeId,
      challenge: recipeId,
      comparison: recipeId,
      outcome: recipeId,
      relevance: recipeId,
      filing: recipeId,
      backlog: recipeId,
    },
    recipes: [
      {
        id: recipeId,
        version: 2,
        body: "Assess the assigned record under the role contract.",
      },
    ],
  };
  draws.pending = [{ ...ASSIGNMENT }];
  const code = new ReviewCode();
  const loop = conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
  await loop.tick();

  const takenAt = new Date(clock).toISOString();
  await db.batch([
    {
      sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                               reserved_cost, actual_cost, granted_at, expires_at, finished_at, outcome)
            SELECT id || '~' || CAST(fence AS TEXT), record_id, role, lane, policy_version,
                   job_id, run_id, fence, reserved_cost, reserved_cost, granted_at, expires_at,
                   ?, 'abandoned'
              FROM claims WHERE id = ? AND fence = 1 AND finished_at IS NULL`,
      params: [takenAt, ASSIGNMENT.id],
    },
    {
      sql: `UPDATE claims
               SET job_id = 'job_code_review_new', run_id = 'cyc_new', fence = 2,
                   actual_cost = NULL, granted_at = ?, expires_at = ?,
                   finished_at = NULL, outcome = NULL
             WHERE id = ? AND fence = 1 AND finished_at IS NULL`,
      params: [takenAt, new Date(clock + POLICY.leaseSeconds * 1_000).toISOString(), ASSIGNMENT.id],
    },
  ]);
  code.read = sessionRead({
    jobId: "job_code_review",
    state: "exited",
    finalMessage:
      "```json\n" +
      JSON.stringify({
        vote: "support",
        contributions: [
          {
            kind: "refinement",
            text: "tighten the claim",
            target: { path: "/payload/statement" },
            would_change: "A stale epoch must not publish this replacement.",
          },
        ],
      }) +
      "\n```",
  });

  const report = await loop.tick();
  expect(report.settled).toEqual([]);
  expect(draws.finished).toEqual([]);
  expect(await db.query(`SELECT id FROM assessments`)).toEqual([]);
  expect(await db.query(`SELECT id FROM records WHERE kind = 'proposal'`)).toEqual([]);
  const run = (
    await db.query<{ closure: string; cost_usd: number; tokens: bigint; payload: string }>(
      `SELECT closure, cost_usd, tokens, payload FROM runs WHERE id = ?`,
      [`run_${ASSIGNMENT.id}_1`],
    )
  )[0]!;
  expect(run.closure).toBe("failed");
  expect(run.cost_usd).toBeCloseTo(0.31, 6);
  expect(run.tokens).toBe(12_900n);
  expect(JSON.parse(run.payload)).toMatchObject({
    closure: "failed",
    counts: {},
    inference: {
      calls: 1,
      inputTokens: 12_000,
      outputTokens: 900,
      cachedInputTokens: 400,
      costMicros: 310_000,
    },
  });
  expect(
    await db.query<{ id: string; fence: bigint; job_id: string; finished_at: string | null }>(
      `SELECT id, fence, job_id, finished_at FROM claims WHERE id = ?`,
      [ASSIGNMENT.id],
    ),
  ).toEqual([{ id: ASSIGNMENT.id, fence: 2n, job_id: "job_code_review_new", finished_at: null }]);
});

// ---------------------------------------------------------------------- the park and the pulse

/** Three reviews of one record in flight, which is what the park's window is three of. */
async function threeInFlight(
  db: PluginDatabase,
  fleet: Fleet,
): Promise<{ runId: string; jobId: string; claimId: string }[]> {
  const flights: { runId: string; jobId: string; claimId: string }[] = [];
  for (const id of ["asg_p1", "asg_p2", "asg_p3"]) flights.push(await inFlight(db, fleet, id));
  return flights;
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const flights = await threeInFlight(db, fleet);

  // Each one reached the model and had its submission refused. The machine exits cleanly: a
  // refused submission is a recipe to review, not a boundary that broke.
  clock += 60_000;
  for (const flight of flights) fleet.finish(flight.jobId, 0, refusedReview(flight.runId));

  const settling = await loop.tick();
  expect(settling.settled.map((row) => [row.outcome, row.cost])).toEqual([
    ["failed", 0],
    ["failed", 0],
    ["failed", 0],
  ]);
  // The loop is not parked and drew again: three refusals are three answers Babel paid for.
  expect(settling.parked).toBe(null);
  expect(settling.notes.some((note) => note.includes("parked"))).toBe(false);
  // …and the cycle's one reason for spending nothing is the door that is not there, not a lane
  // that is broken.
  expect(settling.stop?.reason).toBe("unrouted");
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
  const loop = conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  const flights = await threeInFlight(db, fleet);

  // The three die where a broken machine kills them: mid-review, with no receipt, so nobody can
  // say a model was ever asked anything. Each claim is abandoned at its reservation (#271).
  clock += 60_000;
  for (const flight of flights) fleet.kill(flight.jobId, "interrupted");

  const parking = await loop.tick();
  expect(parking.settled.map((row) => [row.outcome, row.cost])).toEqual([
    ["abandoned", 0.1],
    ["abandoned", 0.1],
    ["abandoned", 0.1],
  ]);
  expect(parking.parked?.barren).toBe(3);
  expect(parking.parked?.reason).toContain("reached no model and produced nothing");
  expect(parking.notes.some((note) => note.startsWith("the loop is parked:"))).toBe(true);
  // The park is the loop's own verdict and is reported BESIDE the cycle's stop rather than
  // instead of it: nothing is drawn either way today, and an operator reading "parked" is
  // reading that the lane is broken rather than that the door is missing.
  expect(parking.requested).toEqual([]);
  expect(parking.stop).toBe(null);

  // An hour of quiet lifts it without an operator: a machine that has been fixed is tried again,
  // and a machine that has not re-parks after three more.
  clock += 61 * 60_000;
  const resumed = await loop.tick();
  expect(resumed.parked).toBe(null);
  expect(resumed.notes.some((note) => note.startsWith("the loop is parked:"))).toBe(false);
  clock = started;
});

test("the pulse counts why a cycle did not spend, and the day accumulates across the wakes", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const draws = new Draws(db);
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });

  // One reason a cycle spent nothing, counted once: the enabled policy names no route. It is a
  // word of the coordinator's own `STOP_REASONS`, so the pulse can tally it.
  const first = await loop.tick();
  expect(first.gaps).toEqual([]);
  expect(first.pulse.tick.gaps).toEqual({ unrouted: 1 });
  expect(first.pulse.today.gaps).toEqual({ unrouted: 1 });

  // The day accumulates across the wakes that make the cycles, which is why it is kept in the
  // plugin's keys rather than in the loop: every tick of a real day is a new conductor.
  const second = await loop.tick();
  expect(second.pulse.tick.gaps).toEqual({ unrouted: 1 });
  expect(second.pulse.today.gaps).toEqual({ unrouted: 2 });

  // …and it is a DAY: the tally starts again at the boundary the spend ledger is kept by.
  clock += 24 * 60 * 60_000;
  const tomorrow = await loop.tick();
  expect(tomorrow.pulse.today.gaps).toEqual({ unrouted: 1 });

  // A disabled policy is a reason a cycle did not spend like any other, and the loop counts it
  // itself: the cycle never gets far enough to reach the routing verdict.
  draws.enabled = false;
  const off = await loop.tick();
  expect(off.enabled).toBe(false);
  expect(off.pulse.tick.gaps).toEqual({ disabled: 1 });
  expect(off.pulse.today.gaps).toEqual({ unrouted: 1, disabled: 1 });
  clock = started;
});

// ------------------------------------------------------- a run that is a Code session (#279)

/** The file the material served, and the digest it served it at. */
const SERVED_FILE = "0001-omp-s1.jsonl";
const SERVED_DIGEST = "a".repeat(64);

/**
 * ONE VALID EXPLORATION ANSWER, in the fenced block the prompt asks the model to end with.
 *
 * It is the whole development path on purpose — a candidate, the locator-backed observation
 * that develops it, the finding that consolidates it, the proposal that addresses the finding,
 * and one question the corpus could not settle — because the settlement's job is to turn all of
 * it into rows and a fixture with only a candidate would prove nothing about the edges.
 */
function answered(path: string, digest: string): string {
  const result = {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "the catalog forgets archived sessions" },
        observations: [
          {
            ref: "o1",
            recipe: { id: "catalog-integrity", version: 3 },
            claim: {
              claim: "the archive wrote a snapshot the rescan did not carry",
              confidence: "high",
              impact: "moderate",
              evidence: [
                { locator: { path, line: 12, byte_offset: 0, digest }, note: "the rescan's row" },
              ],
              counter_evidence_absent: true,
            },
          },
        ],
      },
    ],
    consolidations: [
      {
        ref: "f1",
        observations: ["o1"],
        finding: {
          title: "a rescan drops the archive's snapshot",
          pattern: "every rescan after an archive loses snapshot_id",
          significance: "the corpus cannot be restored from the catalog",
          counter_evidence_absent: true,
        },
        proposal: {
          title: "keep the snapshot a rescan did not observe",
          problem: "a rescan names no snapshot and the upsert clears the column",
          outcome: "an unobserved column is never mentioned by the statement",
          impact: "high",
          classification: "private",
        },
      },
    ],
    deferred: [],
    rejected: [],
    questions: [
      {
        ref: "q1",
        subjects: ["the archive"],
        hypothesis: "h1",
        prompt: "is the snapshot column authoritative over the archive's own index?",
        why_asked: "the corpus cannot say which of the two a restore should trust",
      },
    ],
  };
  return `Here is what I found.\n\n\`\`\`json\n${JSON.stringify(result)}\n\`\`\`\n`;
}

/**
 * A CODE SESSION ALREADY IN FLIGHT: the run row `startExplore` writes after Code accepts the
 * job, plus the settled `prepare` run whose receipt carries the material this session read.
 *
 * The fleet is NOT told about the job, and that is the property under test: Code's job belongs
 * to `atyrode.omp` and `ctx.jobs` has no business with it, so a loop that polled it would fail
 * here rather than pass against a friendly fake.
 */
async function sessionInFlight(
  db: PluginDatabase,
  material: MaterialIndex | null = materialIndex(SERVED_FILE, SERVED_DIGEST),
): Promise<{ runId: string; jobId: string; claimId: string }> {
  const runId = "run_session_1";
  const jobId = "job_code_1";
  const claimId = "clm_session_1";
  const statements: SqlStatement[] = [
    {
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id, profile,
                             preparation, started_at, records, payload)
            VALUES (?, ?, 'dev-01', ?, 'ctr_workbench', 'job_prep_1', ?, ?, ?, 0, '{}')`,
      params: [
        runId,
        OPERATIONS.explore,
        jobId,
        JSON.stringify({
          containerId: "ctr_workbench",
          expectedRevision: 7,
          account: { provider: "anthropic", identityKey: "victorballu@gmail.com" },
        }),
        JSON.stringify({ preset: "read-whats-new", selected: 1 }),
        new Date(clock).toISOString(),
      ],
    },
    {
      sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                               reserved_cost, granted_at, expires_at)
            VALUES (?, ?, 'reception', 'coverage', ?, ?, ?, 1, ?, ?, ?)`,
      params: [
        claimId,
        ASSIGNMENT.recordId,
        POLICY.version,
        jobId,
        SEEDED_CYCLE,
        ASSIGNMENT.reservedCost,
        new Date(clock).toISOString(),
        new Date(clock + POLICY.leaseSeconds * 1000).toISOString(),
      ],
    },
  ];
  if (material !== null) {
    statements.push({
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure,
                             records, payload)
            VALUES ('run_prep_1', ?, 'dev-01', 'job_prep_1', ?, ?, 'completed', 0, ?)`,
      params: [
        OPERATIONS.prepare,
        new Date(clock).toISOString(),
        new Date(clock).toISOString(),
        JSON.stringify({ runId: "run_prep_1", kind: "prepare", closure: "completed", material }),
      ],
    });
  }
  await db.batch(statements);
  return { runId, jobId, claimId };
}

test("a finished Code session whose citations the material served writes its receipt and settles the claim", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST),
    }),
  }));
  const { runId, jobId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  // Code was asked with the pair `readSession` takes, and nothing else was: the job is not
  // Babel's, so its container is the whole of how the loop addresses it.
  expect(code.asked).toEqual([{ containerId: "ctr_workbench", jobId }]);

  const run = (
    await db.query(`SELECT closure, cost_usd, tokens, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("completed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["model"]).toBe("anthropic/claude-opus-4-1");
  expect(receipt["models"]).toEqual(["anthropic/claude-opus-4-1"]);
  expect(receipt["reason"]).toBeUndefined();
  // The account is the one the LAUNCH named: Code chooses it and its session receipt reports
  // none, so the run row is where "which window did this spend" is answered (#267).
  expect(receipt["account"]).toEqual({
    provider: "anthropic",
    identityKey: "victorballu@gmail.com",
  });
  // ONE CALL, because a posted session is omp's one-shot; the tokens and the cost are the
  // meter's, in the same columns a metered Babel job writes.
  expect(receipt["inference"]).toEqual({
    calls: 1,
    inputTokens: 12_000,
    outputTokens: 900,
    cachedInputTokens: 400,
    costMicros: 310_000,
  });
  expect(run["cost_usd"]).toBeCloseTo(0.31, 6);
  expect(Number(run["tokens"])).toBe(12_900);

  expect(report.settled).toEqual([
    { claimId, outcome: "completed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
  expect(report.pulse.tick.refusals).toEqual({});
});

test("a citation the material never served is refused, and the refusal is spend with its claim settled", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    // The path is one the index names; the digest is not the one it was served at, which is a
    // retyped digest and exactly what `unservedLocator` exists to catch.
    value: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, "b".repeat(64)),
    }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (
    await db.query(`SELECT closure, cost_usd, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("failed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(String(receipt["reason"])).toStartWith("unknown-reference:");
  // THE MONEY IS STILL SPENT. The model answered and the deployment paid for it; a refusal
  // recorded at zero is how a fan reads a refused lane as free and relaunches into it.
  expect(run["cost_usd"]).toBeCloseTo(0.31, 6);
  expect(report.settled).toEqual([
    { claimId, outcome: "failed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
  // …and it is counted by the code the contract refused with, not as a failure of the loop.
  expect(report.pulse.tick.refusals).toEqual({ "unknown-reference": 1 });
});

test("a Code session still running leaves its run open and settles nothing", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  // A RUNNING JOB SEALS NO TRANSCRIPT, so Code answers the job and a null receipt. That is a
  // successful read of a live run — it used to be `code_omp_result_unavailable`, which read
  // as a fault and would have closed the run at the reaper's bound.
  const code = codeAnswering(() => ({
    ok: true,
    // `started` and not "running": the states are the hub's (`JobStateSchema`), and a fake
    // answering a word the protocol does not have would prove this reconcile against a world
    // that cannot occur.
    value: sessionRead({ state: "started", sealed: false }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  expect(report.runs).toEqual({ running: 1, atModel: 1, stalled: 0 });
  expect(report.settled).toEqual([]);
  const run = (await db.query(`SELECT closure FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBeNull();
  const claim = (await db.query(`SELECT finished_at FROM claims WHERE id = ?`, [claimId]))[0]!;
  expect(claim["finished_at"]).toBeNull();
});

test("a session Code cancelled closes as stopped with no receipt, and its claim is settled", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  // An operator pressed Stop: the job is terminal, Code sealed no transcript, and nothing was
  // submitted. It used to be unreadable; now it is a read that says exactly that.
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({ state: "cancelled", sealed: false }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (
    await db.query(`SELECT closure, cost_usd, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  // STOPPED, NOT FAILED. The job reported its own ending and the receipt records it; a panel
  // that called every unreadable run a failure is how an operator's Stop looks like a fault.
  expect(run["closure"]).toBe("stopped");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(String(receipt["reason"])).toStartWith("empty:");
  expect(String(receipt["reason"])).toContain("cancelled");
  expect(receipt["model"]).toBeUndefined();
  expect(run["cost_usd"]).toBe(0);
  // …and the claim is SKIPPED rather than failed: nobody answered, so there is no paid
  // refusal here and the park heuristic must not read a streak of operator stops as a lane
  // that is broken.
  expect(report.settled.map((entry: SettledClaim) => [entry.claimId, entry.outcome])).toEqual([
    [claimId, "skipped"],
  ]);
});

test("a read Code refuses is recorded on the run, retried once, and then closed rather than asked for ever", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() =>
    refusedByCode<SessionRead>(
      "engine_stale_profile",
      "atyrode.code.readSession (code_stale_preferences)",
    ),
  );
  const { runId, claimId } = await sessionInFlight(db);
  /*
    A NEW CONDUCTOR PER WAKE, which is what `server.ts` actually does: every door and every
    settlement builds one. The count of consecutive silent cycles therefore cannot live in the
    loop's closure — it did, and the bound could never fire because the object was gone before
    the second cycle read it. It is a column on the run row now, and this test drives the
    hub's own shape rather than one long-lived loop.
  */
  const wake = (): Conductor =>
    conductor({
      engine: code,
      store,
      coordinator: draws as unknown as Coordinator,
      jobs: new Fleet(),
      machines: new Folders(),
      keys: new Keys(),
      plan: PLAN,
      now: () => clock,
    });

  // ONE REFUSAL IS A HICCUP. The sentence is on the row so a reader sees it without the
  // journal, and the run stays open for the next wake to ask again.
  const first = await wake().tick();
  expect(first.runs.running).toBe(1);
  const held = (
    await db.query(`SELECT closure, payload, unreadable FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(Number(held["unreadable"])).toBe(1);
  expect(held["closure"]).toBeNull();
  expect(String(held["payload"])).toContain("code_stale_preferences");

  // TWO IN A ROW IS A RUN NOBODY WILL EVER READ. It is closed with that sentence and its claim
  // released, rather than retried on every wake for the life of the deployment.
  const second = await wake().tick();
  expect(second.runs.running).toBe(0);
  const closed = (
    await db.query(`SELECT closure, finished_at, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(closed["closure"]).toBe("failed");
  expect(String(closed["payload"])).toContain("engine_stale_profile");
  expect(second.settled.map((entry: SettledClaim) => [entry.claimId, entry.outcome])).toEqual([
    [claimId, "abandoned"],
  ]);

  // …and a third wake asks Code nothing more about it: two reads, and no run left to poll.
  await wake().tick();
  expect(code.asked).toHaveLength(2);
});

// ------------------------------------------- the rows one answer becomes (records.ts)

/** The conductor one wake builds, as `server.ts` builds it: a new one per tick. */
function wakeOn(
  store: BabelStore,
  draws: Draws,
  code: CodeEngine & { asked: unknown[] },
): Conductor {
  return conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  });
}

test("an accepted answer becomes the records, edges, statuses and questions it claimed", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST),
    }),
  }));
  const { runId } = await sessionInFlight(db);

  const report = await wakeOn(store, draws, code).tick();
  expect(report.pulse.tick.refusals).toEqual({});

  // FOUR RECORDS AND THE PATH BETWEEN THEM. Every one is this run's, written as a run and not
  // as an operator, and each is its own root at sequence zero: a correction supersedes.
  const records = await db.query(
    `SELECT id, kind, parent_id, run_id, recipe_id, recipe_version, actor_kind, actor_id, seq,
            root_id, title
       FROM records WHERE run_id = ? ORDER BY kind`,
    [runId],
  );
  expect(records.map((row) => row["kind"])).toEqual([
    "finding",
    "hypothesis",
    "observation",
    "proposal",
  ]);
  for (const row of records) {
    expect([row["actor_kind"], row["actor_id"], row["run_id"]]).toEqual(["run", runId, runId]);
    expect([row["seq"], row["root_id"]]).toEqual([0n, row["id"]]);
  }
  const byKind = new Map(records.map((row) => [String(row["kind"]), row]));
  const hypothesis = String(byKind.get("hypothesis")?.["id"]);
  const observation = String(byKind.get("observation")?.["id"]);
  const finding = String(byKind.get("finding")?.["id"]);
  const proposal = String(byKind.get("proposal")?.["id"]);
  expect(hypothesis).toMatch(/^hyp_[0-9a-f]{32}$/);
  expect(observation).toMatch(/^obs_[0-9a-f]{32}$/);
  // AN OBSERVATION HANGS OFF EXACTLY ONE HYPOTHESIS, and only it carries the recipe: the lens
  // is what the answer attributes, and naming one on a finding would be an attribution nobody
  // made.
  expect(byKind.get("observation")?.["parent_id"]).toBe(hypothesis);
  expect([
    byKind.get("observation")?.["recipe_id"],
    byKind.get("observation")?.["recipe_version"],
  ]).toEqual(["catalog-integrity", 3n]);
  expect(byKind.get("finding")?.["recipe_id"]).toBeNull();
  expect(byKind.get("hypothesis")?.["parent_id"]).toBeNull();

  const edges = await db.query(
    `SELECT kind, from_kind, from_id, to_kind, to_id, note FROM edges
      WHERE actor_id = ? ORDER BY kind`,
    [runId],
  );
  expect(edges.map((row) => [row["kind"], row["from_id"], row["to_kind"], row["to_id"]])).toEqual([
    ["addresses", proposal, "finding", finding],
    ["cites", observation, "session", "omp/s1"],
    ["consolidates", finding, "observation", observation],
  ]);
  expect(edges.find((row) => row["kind"] === "cites")?.["note"]).toBe("the rescan's row");

  // THE LIFECYCLE STARTS WITH THE RECORD. A hypothesis with no status event is one no listing
  // can rank or defer, and only a hypothesis has one: §4.12's lifecycle is the candidate's.
  const statuses = await db.query(
    `SELECT record_id, seq, status, actor_kind, run_id FROM status_events WHERE run_id = ?`,
    [runId],
  );
  expect(statuses).toEqual([
    { record_id: hypothesis, seq: 0n, status: "untriaged", actor_kind: "run", run_id: runId },
  ]);

  // A question a run raised is the one ledger write it may make: it authorizes nothing, and it
  // is BLOCKING because it names the candidate it holds up rather than because it said so.
  const questions = await db.query(
    `SELECT id, kind, class, text, raised_by_kind, raised_by_id, payload FROM questions
      WHERE raised_by_id = ?`,
    [runId],
  );
  expect(questions).toHaveLength(1);
  const question = questions[0]!;
  expect([question["kind"], question["class"]]).toEqual(["acquire-context", "blocking"]);
  expect([question["raised_by_kind"], question["raised_by_id"]]).toEqual(["run", runId]);
  expect(JSON.parse(String(question["payload"]))["work"]).toEqual([
    { kind: "hypothesis", id: hypothesis, blocking: true },
  ]);

  // The receipt counts what landed, in the same per-file shape an ingested job's does, and the
  // run row's own `records` column is the record count a listing reads.
  const run = (
    await db.query(`SELECT closure, records, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("completed");
  expect(Number(run["records"])).toBe(4);
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["counts"]).toEqual({
    [JOB_OUTPUT_FILES.records]: 4,
    [JOB_OUTPUT_FILES.edges]: 3,
    [JOB_OUTPUT_FILES.statusEvents]: 1,
    [JOB_OUTPUT_FILES.questions]: 1,
  });

  // …and no ruling. A disposition is the operator's alone, and a run that could write one would
  // make Babel an agent that agrees with itself.
  expect(await db.query(`SELECT COUNT(*) AS n FROM dispositions`, [])).toEqual([{ n: 0n }]);
});

test("a refused answer writes no record at all, and the receipt is still written at cost", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, "b".repeat(64)),
    }),
  }));
  const { runId } = await sessionInFlight(db);

  const report = await wakeOn(store, draws, code).tick();

  // NOTHING LANDED. A partial development path is worse than none: a finding consolidating
  // observations nobody holds is exactly the shape §4.2 forbids.
  expect(await db.query(`SELECT COUNT(*) AS n FROM records WHERE run_id = ?`, [runId])).toEqual([
    { n: 0n },
  ]);
  expect(await db.query(`SELECT COUNT(*) AS n FROM edges WHERE actor_id = ?`, [runId])).toEqual([
    { n: 0n },
  ]);
  expect(
    await db.query(`SELECT COUNT(*) AS n FROM status_events WHERE run_id = ?`, [runId]),
  ).toEqual([{ n: 0n }]);
  expect(
    await db.query(`SELECT COUNT(*) AS n FROM questions WHERE raised_by_id = ?`, [runId]),
  ).toEqual([{ n: 0n }]);

  const run = (
    await db.query(`SELECT closure, cost_usd, records, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("failed");
  expect(Number(run["records"])).toBe(0);
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(String(receipt["reason"])).toStartWith("unknown-reference:");
  expect(receipt["counts"]).toEqual({});
  expect(run["cost_usd"]).toBeCloseTo(0.31, 6);
  expect(report.pulse.tick.refusals).toEqual({ "unknown-reference": 1 });
});

test("a consolidation resting on a candidate is the development path skipped, and writes nothing", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const result = {
    candidates: [{ ref: "h1", hypothesis: { statement: "the catalog forgets sessions" } }],
    consolidations: [
      {
        ref: "f1",
        // A CANDIDATE, not the observation that would have developed it: §4.2's path is
        // mandatory, and citing a guess as if it were evidence is its own refusal.
        observations: ["h1"],
        finding: { title: "a pattern", pattern: "it recurs", counter_evidence_absent: true },
      },
    ],
    questions: [],
  };
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: `\`\`\`json\n${JSON.stringify(result)}\n\`\`\``,
    }),
  }));
  const { runId } = await sessionInFlight(db);

  const report = await wakeOn(store, draws, code).tick();

  expect(await db.query(`SELECT COUNT(*) AS n FROM records WHERE run_id = ?`, [runId])).toEqual([
    { n: 0n },
  ]);
  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("failed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(String(receipt["reason"])).toStartWith("development-path:");
  expect(report.pulse.tick.refusals).toEqual({ "development-path": 1 });
});

test("settling the same run twice writes the rows once: the identifiers are the run's own", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST),
    }),
  }));
  const { runId, jobId } = await sessionInFlight(db);

  await wakeOn(store, draws, code).tick();
  const first = await db.query(`SELECT id FROM records WHERE run_id = ? ORDER BY id`, [runId]);
  expect(first).toHaveLength(4);

  /*
    A CRASH BETWEEN THE ROWS AND THE RUN ROW IS REPAIRED, NOT DOUBLED. The run is re-opened the
    way an unsettled one looks to the next wake, and the same session is read again: every
    identifier is a digest of the run and the model's own handle, so the replay's inserts are
    `INSERT OR IGNORE` no-ops. A counter would have produced a second corpus of the same claims.
  */
  await db.run(
    `UPDATE runs SET closure = NULL, finished_at = NULL, records = 0, payload = '{}' WHERE id = ?`,
    [runId],
  );
  await db.run(`UPDATE claims SET finished_at = NULL WHERE job_id = ?`, [jobId]);
  await wakeOn(store, draws, code).tick();

  expect(await db.query(`SELECT id FROM records WHERE run_id = ? ORDER BY id`, [runId])).toEqual(
    first,
  );
  expect(await db.query(`SELECT COUNT(*) AS n FROM edges WHERE actor_id = ?`, [runId])).toEqual([
    { n: 3n },
  ]);
  expect(
    await db.query(`SELECT COUNT(*) AS n FROM status_events WHERE run_id = ?`, [runId]),
  ).toEqual([{ n: 1n }]);
  expect(
    await db.query(`SELECT COUNT(*) AS n FROM questions WHERE raised_by_id = ?`, [runId]),
  ).toEqual([{ n: 1n }]);
});
