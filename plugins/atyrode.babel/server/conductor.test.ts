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
} from "../contract.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import type { BabelStore } from "../store/store.ts";
import type { Assignment, Coordinator } from "../store/coordinator.ts";
import {
  BEAT_OPERATION,
  CONDUCTOR_SCHEDULE_ID,
  conductor,
  ingestOutputs,
  type JobLaunch,
  type JobOutput,
  type JobRunState,
  type JobsSlice,
  type JobState,
  type MachineReadiness,
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
  const db = new Database(join(directory, "data.db"), { create: true, strict: true });
  // The pragmas the engine opens a plugin's file with (`server/src/plugin-database.ts`), so the
  // triggers, the STRICT tables and the foreign keys behave here exactly as they do in the hub.
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
      return { changes: result.changes, lastInsertRowid: Number(result.lastInsertRowid) };
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
  return JSON.stringify(dump);
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
}

class Fleet implements JobsSlice {
  readonly launched: JobLaunch[] = [];
  readonly scheduled: (JobLaunch & ScheduleTiming)[] = [];
  readonly disabled: { scheduleId: string; revision: string }[] = [];
  registered: ScheduleRow[] = [];
  readonly jobs = new Map<string, FakeJob>();
  readonly beats = new Set<string>();
  ready = true;
  connected = true;

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
    this.launched.push(args);
    this.jobs.set(args.jobId, {
      state: "started",
      exitCode: null,
      machineId: args.machineId,
      operationId: args.operationId,
      archive: null,
    });
    return this.status({ jobId: args.jobId });
  }

  status(node: { jobId: string }): JobRunState {
    const job = this.jobs.get(node.jobId);
    if (job === undefined) throw new Error(`unknown job ${node.jobId}`);
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
          : { state: job.state, exitCode: job.exitCode, reason: null, outputs },
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
    });
    this.beats.add(jobId);
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
  readonly finished: { id: string; runId: string; fence: number; cost: number; outcome: string }[] =
    [];
  enabled = true;
  version = POLICY.version;
  /** Assignments this coordinator still has to give; it answers a gap when they run out. */
  pending: Record<string, unknown>[] = [];

  constructor(private readonly db: PluginDatabase) {}

  async policy(): Promise<Record<string, unknown>> {
    return {
      policy: { ...POLICY, enabled: this.enabled, version: this.version },
      source: "stored",
      version: this.version,
      recordedAt: clock,
    };
  }

  async draw(request: { runId: string }): Promise<Record<string, unknown>> {
    this.draws += 1;
    expect(request.runId.startsWith("cyc_")).toBe(true);
    const next = this.pending.shift();
    if (next === undefined) {
      return { outcome: "gap", gap: { reason: "no-candidates", detail: "nothing due" }, gaps: [] };
    }
    return { outcome: "assignment", assignment: next, gaps: [] };
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
    fence: number;
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

  async renew(): Promise<Record<string, unknown>> {
    return { outcome: "renewed", expiresAt: clock };
  }

  async spend(): Promise<Record<string, unknown>> {
    return { day: "2026-09-12", total: 0, byRun: {} };
  }
}

const PLAN: RunPlan = {
  engine: { binary: "code", args: [] },
  profile: { id: "analysis", revision: 3 },
  caps: { perRunUsd: 0.25, toolCalls: 40, idleMs: 120000, handshakeMs: 30000 },
  recipes: {
    reception: { id: "reception-vote", version: 1, title: "Reception", body: "Does it hold?" },
  },
  requireContainment: true,
  limits: { timeoutMs: 900000, memoryBytes: 2147483648, processes: 64, outputBytes: 67108864 },
};

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
        payload: JSON.stringify({ because: "…" }),
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
  expect(queued[0]).toEqual({ closure: null, records: 0 });

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
    const rows = await db.query<{ n: number }>(`SELECT COUNT(*) AS n FROM ${table}`);
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
    tokens: 12345,
    records: 1,
    finished_at: "2026-09-12T09:05:00Z",
    job_id: "job_asg_a1b2",
  });

  // The claim settles at what the receipt says the run cost, not at what the draw reserved.
  expect(ingesting.settled).toEqual([
    { claimId: "clm_asg_a1b2", outcome: "completed", cost: 0.42, overrun: false, refused: null },
  ]);
  expect(draws.finished).toEqual([
    {
      id: "clm_asg_a1b2",
      runId: requesting.cycleRunId,
      fence: 1,
      cost: 0.42,
      outcome: "completed",
    },
  ]);
  const claim = await db.query(
    `SELECT actual_cost, outcome FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ actual_cost: 0.42, outcome: "completed" });
});

test("a failed job settles its claim as failed and closes its run", async () => {
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
  expect(report.settled).toEqual([
    { claimId: "clm_asg_a1b2", outcome: "failed", cost: 0, overrun: false, refused: null },
  ]);
  const claim = await db.query(
    `SELECT finished_at IS NOT NULL AS closed, outcome FROM claims WHERE id = 'clm_asg_a1b2'`,
  );
  expect(claim[0]).toEqual({ closed: 1, outcome: "failed" });
  const run = await db.query(`SELECT closure, cost_usd FROM runs WHERE id = 'run_asg_a1b2'`);
  expect(run[0]).toEqual({ closure: "failed", cost_usd: null });
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
  const sessions = await db.query<{ n: number }>(`SELECT COUNT(*) AS n FROM sessions`);
  expect(sessions[0]?.n).toBe(2);

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
  const counted = await db.query<{ n: number }>(`SELECT COUNT(*) AS n FROM records`);
  expect(counted[0]?.n).toBe(601);
  const edges = await db.query<{ n: number }>(`SELECT COUNT(*) AS n FROM edges WHERE id = 'edg_bad'`);
  expect(edges[0]?.n).toBe(0);
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
    plan: PLAN,
    now: () => clock,
  });

  await loop.tick();
  fleet.seal("job_asg_a1b2", Buffer.alloc(1024, 0x41));
  const report = await loop.tick();

  expect(report.notes[0]).toContain("outputs were refused");
  const run = await db.query(`SELECT closure FROM runs WHERE id = 'run_asg_a1b2'`);
  expect(run[0]).toEqual({ closure: "failed" });
  // The claim is released at zero rather than held by a run whose output is unreadable.
  expect(report.settled).toEqual([
    { claimId: "clm_asg_a1b2", outcome: "failed", cost: 0, overrun: false, refused: null },
  ]);
  // And the next cycle has nothing left to reconcile.
  const next = await loop.tick();
  expect(next.ingested).toEqual([]);
  expect(next.notes).toEqual([]);
});
