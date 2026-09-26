import { Database } from "bun:sqlite";
import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import { HostCallError } from "@manifold/plugin-kit/errors";
import {
  BABEL_PLUGIN_ID,
  CONDUCTOR_CYCLE_KEY,
  CONDUCTOR_TALLY_KEY,
  INPUT_FIELD,
  JOB_OUTPUT_FILES,
  MATERIAL_OUTPUT,
  MATERIAL_SCHEMA,
  MATERIAL_SESSIONS,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  RUN_STAGES,
  RECALL_SERVICE_ID,
  TranscriptMapCatalogInputSchema,
  TranscriptMapPolicySchema,
  type TranscriptMapJobReceipt,
  type TranscriptMapServiceBinding,
  TranscriptMapPrepareInputSchema,
  TRANSCRIPT_MAP_SESSION_OPERATION,
  MAP_DRAIN_PRESET,
  type TranscriptMapModelResult,
  diffRunTraces,
  type MaterialIndex,
  type RunTrace,
} from "../contract.ts";
import { insertDrain, readDrain } from "../store/drains.ts";
import { launchMachinery, type Started } from "../doors/launch.ts";
import { drainTick, endDrain, type DrainDeps } from "./drain.ts";
import { coordinator as governed } from "../store/coordinator.ts";
import type {
  CodeEngine,
  CodeJob,
  EngineAnswer,
  SessionRead,
  SessionRequest,
  SessionUsage,
} from "./engine/session.ts";
import { omp } from "../machine/adapters/omp.ts";
import { resolveRedaction } from "../machine/prepare.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { transcriptMapCaptureId } from "../transcript-map-identity.ts";
import { buildTranscriptMap } from "../machine/transcript-map-tree.ts";
import { transcriptMaps } from "../store/transcript-maps.ts";
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
  readRunTrace,
  type ScheduleRow,
  type ScheduleTiming,
  type TickReport,
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
  "run_calls",
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

/**
 * The archive shape the agent seals an output as: regular files, mode 0600, no extensions.
 *
 * A string member is written verbatim, which is what the MATERIAL is: one canonical JSON
 * record per line rather than one JSON document, and a citation's quote is checked against
 * exactly those bytes.
 */
function tar(files: Readonly<Record<string, unknown>>): Buffer {
  const blocks: Buffer[] = [];
  for (const [name, document] of Object.entries(files)) {
    const body = Buffer.from(
      typeof document === "string" ? document : JSON.stringify(document),
      "utf8",
    );
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
  /** The `material` output a `prepare` job seals beside its receipt: the bytes a citation's
   *  quote is checked against. */
  material: Buffer | null;
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

  cancel(node: { jobId: string }): void {
    this.kill(node.jobId, "cancelled");
  }

  /** A job the hub is already reporting as started: what a posting used to leave behind. */
  running(jobId: string, machineId: string, operationId: string): void {
    this.jobs.set(jobId, {
      state: "started",
      exitCode: null,
      machineId,
      operationId,
      archive: null,
      material: null,
      journal: [],
      inference: null,
    });
  }

  status(node: { jobId: string }): JobRunState {
    const job = this.jobs.get(node.jobId);
    if (job === undefined) throw new Error(`unknown job ${node.jobId}`);
    if (this.silent.has(node.jobId)) throw new Error(`the machine holding ${node.jobId} is gone`);
    const outputs: JobOutput[] = [];
    if (job.archive !== null) {
      outputs.push({
        outputId: `out_${node.jobId}`,
        name: OUTPUT_BINDING,
        bytes: job.archive.byteLength,
        files: 1,
      });
    }
    if (job.material !== null) {
      outputs.push({
        outputId: `mat_${node.jobId}`,
        name: MATERIAL_OUTPUT,
        bytes: job.material.byteLength,
        files: 1,
      });
    }
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

  output(args: { node: { jobId: string; outputId: string }; offset: number; maxBytes: number }): {
    data: string;
    eof: boolean;
  } {
    const job = this.jobs.get(args.node.jobId);
    // WHICH SEALED OUTPUT WAS ASKED FOR, because a `prepare` job has two and they are read by
    // different readers: the receipt by the ingest, the material by the citation check.
    const sealed = args.node.outputId.startsWith("mat_") ? job?.material : job?.archive;
    if (sealed == null) throw new Error(`job ${args.node.jobId} sealed no output`);
    const end = Math.min(sealed.byteLength, args.offset + args.maxBytes);
    return {
      data: sealed.subarray(args.offset, end).toString("base64"),
      eof: end === sealed.byteLength,
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
  kill(jobId: string, state: "cancelled" | "interrupted" | "refused"): void {
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
      material: null,
      journal: [],
      inference: null,
    });
    this.beats.add(jobId);
  }

  /**
   * A `prepare` job this machine finished, with the material it sealed: one file per session,
   * verbatim, under the names the index gives them. It is what a citation's quote is checked
   * against, and a run whose preparation the fake does not hold is how "the bytes could not be
   * read" is exercised.
   */
  prepared(jobId: string, machineId: string, sessions: Readonly<Record<string, string>>): void {
    this.jobs.set(jobId, {
      state: "exited",
      exitCode: 0,
      machineId,
      operationId: OPERATIONS.prepare,
      archive: null,
      material: tar(sessions),
      journal: [],
      inference: null,
    });
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
  activityWeights: { review: 1, explore: 0, challenge: 0, synthesize: 0 },
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
  activity: "review",
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
  stageRecipes: {},
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
  /** The candidates a draw declined on its way to whatever it answered. */
  declined: Record<string, unknown>[] = [];
  review: Policy["review"] = undefined;
  mapping: Policy["mapping"] = undefined;
  claimFence = 1;
  /** How many reviews one cycle may dispatch, so a test can let a cycle FILL its batch. */
  batchSize = POLICY.batchSize;

  constructor(private readonly db: PluginDatabase) {}

  async policy(): Promise<Record<string, unknown>> {
    const standing = {
      ...POLICY,
      enabled: this.enabled,
      version: this.version,
      batchSize: this.batchSize,
      ...(this.review === undefined ? {} : { review: this.review }),
      ...(this.mapping === undefined ? {} : { mapping: this.mapping }),
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
      ? {
          outcome: "gap",
          gap: { reason: "no-candidates", detail: "nothing due" },
          gaps: this.declined,
        }
      : { outcome: "assignment", assignment: next, gaps: this.declined };
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
  checkProfile: async () =>
    await Promise.resolve(refusedByCode("engine_unavailable", "no Code here")),
  runSession: async () =>
    await Promise.resolve(refusedByCode("engine_unavailable", "no Code here")),
  readSession: async () => {
    throw new Error("a run with no container must never be read through Code");
  },
  cancelSession: async () => {
    throw new Error("a run with no container must never be cancelled through Code");
  },
};

const SESSION_METER: InferenceUsage = {
  calls: 3,
  inputTokens: 20_000,
  outputTokens: 1_500,
  cachedInputTokens: 800,
  costMicros: 410_000,
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
  readonly inference?: InferenceUsage;
  readonly activity?: SessionRead["activity"];
  readonly model?: string;
  /** The transcript this session sealed, which is the locator a call row keeps (#349). */
  readonly sessionId?: string;
  readonly sessionPath?: string;
}): SessionRead {
  return {
    job: {
      jobId: over.jobId ?? "job_code_1",
      machineId: "dev-01",
      operationId: "atyrode.omp.session",
      pluginId: "atyrode.omp",
      state: over.state,
      ...(over.inference === undefined
        ? {}
        : {
            result: {
              jobId: over.jobId ?? "job_code_1",
              requestDigest: "d".repeat(64),
              ownerId: "owner",
              ownerGeneration: 1,
              state: over.state,
              exitCode: over.exitCode ?? 0,
              reason: null,
              startedAt: clock,
              finishedAt: clock,
              usage: {
                elapsedMs: 1_000,
                memoryBytes: 0,
                processes: 1,
                outputBytes: 0,
                inference: over.inference,
              },
              limits: { timeoutMs: 60_000, memoryBytes: 1024, processes: 1, outputBytes: 1024 },
              outputs: [],
            },
          }),
    },
    ...(over.activity === undefined ? {} : { activity: over.activity }),
    session:
      over.sealed === false
        ? null
        : {
            sessionId: over.sessionId ?? "ses_1",
            sessionPath: over.sessionPath ?? "/home/job/.omp/agent/sessions/ses_1.jsonl",
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
  async checkProfile(): Promise<EngineAnswer<null>> {
    return await Promise.resolve({ ok: true, value: null });
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
    checkProfile: async () =>
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
                           cost_usd, last_model, models, stalled
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
  // WHICH MODELS ANSWERED, AND NOT ONLY THE NEWEST (#169). The second call was served by a
  // different model; before this the row kept `last_model` alone and the run read as though
  // sonnet had answered both, which is how a fallback became invisible.
  expect(row?.["models"]).toBe(JSON.stringify(["claude-opus-4", "claude-sonnet-4"]));
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
    // The run comes BACK to the model it opened on. A list that appended every call would
    // grow without bound and read as a three-model run; first-heard order, kept once.
    called(13, {
      inputTokens: 300,
      outputTokens: 80,
      cachedInputTokens: 0,
      costMicros: 20_000,
    }),
  ]);
  clock = started + 75_000;
  const turning = await loop.tick();
  expect(turning.notes.filter((note) => note.includes("was not retained"))).toEqual([]);
  const second = await db.query(`SELECT message, since FROM run_progress`);
  expect(second[0]).toMatchObject({
    message: "challenge",
    since: new Date(started + 72_000).toISOString(),
  });
  expect((await db.query(`SELECT models FROM run_progress`))[0]?.["models"]).toBe(
    JSON.stringify(["claude-opus-4", "claude-sonnet-4"]),
  );

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
  // …AND SO ARE THE MODELS. `usage.inference` is five numbers and no name, and the row that
  // heard the names is deleted one statement later, so without this the answer to "what
  // answered this run" died with the run (#169).
  expect(kept["models"]).toEqual(["claude-opus-4", "claude-sonnet-4"]);
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

test("native logs and other leases cannot replace the sealed result records", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const fleet = new Fleet();
  fleet.running("job_native_results", "dev-01", OPERATIONS.evaluate);
  fleet.finish("job_native_results", 0, outputs("run_native_results"));
  const unrelated = new Map([
    ["stdout", Buffer.from(JSON.stringify({ progress: "catalog page ".repeat(128) }))],
    ["stderr", Buffer.from("native diagnostic\n".repeat(128))],
    [MATERIAL_OUTPUT, tar(outputs("run_native_material"))],
  ]);
  const read = fleet.output.bind(fleet);
  fleet.output = (args) => {
    const bytes = unrelated.get(args.node.outputId);
    if (bytes === undefined) return read(args);
    const end = Math.min(bytes.byteLength, args.offset + args.maxBytes);
    return {
      data: bytes.subarray(args.offset, end).toString("base64"),
      eof: end === bytes.byteLength,
    };
  };

  const result = await ingestOutputs(store, fleet, {
    runId: "run_native_results",
    jobId: "job_native_results",
    machineId: "dev-01",
    operationId: OPERATIONS.evaluate,
    outputs: [
      ...(fleet.status({ jobId: "job_native_results" }).result?.outputs ?? []),
      ...Array.from(unrelated, ([name, bytes]) => ({
        outputId: name,
        name,
        bytes: bytes.byteLength,
        files: 1,
      })),
    ],
    closure: "completed",
  });

  expect(result.receipt?.runId).toBe("run_native_results");
  expect(
    await db.query(
      "SELECT id FROM runs WHERE id IN ('run_native_results', 'run_native_material') ORDER BY id",
    ),
  ).toEqual([{ id: "run_native_results" }]);
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

test("a reap is bounded per cycle and takes the oldest ghosts first", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
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

  // A CLAIMS TABLE THAT HAS GONE WRONG: 130 grants whose posting never landed, every one of
  // them older than the lease it was granted under. 2026-09-13 left about seventy; this is
  // what the cycle after a worse one looks like, and a reaper with no bound would take the
  // store's write lock for all of them at once while a dispatch waited behind it.
  const ghosts = 130;
  const granted = clock - (POLICY.leaseSeconds + 600) * 1000;
  await db.batch(
    Array.from({ length: ghosts }, (_unused, index) => ({
      sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                               reserved_cost, granted_at, expires_at)
            VALUES (?, ?, 'reception', 'coverage', ?, NULL, 'cyc_ghosts', 1, ?, ?, ?)`,
      params: [
        `clm_ghost_${String(index).padStart(3, "0")}`,
        ASSIGNMENT.recordId,
        POLICY.version,
        ASSIGNMENT.reservedCost,
        // One second apart, so "oldest first" is a fact about this table and not a tie.
        new Date(granted + index * 1000).toISOString(),
        new Date(granted + index * 1000 + POLICY.leaseSeconds * 1000).toISOString(),
      ],
    })),
  );

  const first = await loop.tick();
  expect(first.settled).toHaveLength(128);
  expect(first.settled[0]?.claimId).toBe("clm_ghost_000");
  expect(first.settled[127]?.claimId).toBe("clm_ghost_127");
  // …and the cycle says what it left, so an operator reading one tick is not told the table
  // is clean when it is two rows short of it.
  expect(
    first.notes.some((note) => note.startsWith("128 dead claims were released this cycle")),
  ).toBe(true);

  // The next cycle continues from where this one stopped, and the one after has nothing left
  // to say: a bound that left the freshest rows for ever would be the ghost defect again.
  clock += 60_000;
  const second = await loop.tick();
  expect(second.settled.map((row) => row.claimId)).toEqual(["clm_ghost_128", "clm_ghost_129"]);
  expect(second.notes.some((note) => note.includes("dead claims were released"))).toBe(false);
  const third = await loop.tick();
  expect(third.settled).toEqual([]);
  expect(await db.query(`SELECT COUNT(*) AS open FROM claims WHERE finished_at IS NULL`)).toEqual([
    { open: 0n },
  ]);
  clock = started;
});

test("a batch every slot of which a dead job holds is drawn into the same cycle that reaps it", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.review = ROUTE;
  draws.pending = [{ ...ASSIGNMENT }];
  // Four reviews holding the whole batch, and every one of their jobs is over: the run rows
  // are closed and nothing polls them, so no settlement will ever reach these claims. This is
  // the 12:42 shape of 2026-09-13 — "held by another worker until 14:08" for workers that had
  // been killed at 12:07.
  for (const slot of [1, 2, 3, 4]) {
    const jobId = `job_dead_${String(slot)}`;
    await db.batch([
      {
        sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure,
                               records, payload)
              VALUES (?, ?, ?, ?, ?, ?, 'failed', 0, '{}')`,
        params: [
          `run_dead_${String(slot)}`,
          OPERATIONS.evaluate,
          MACHINE,
          jobId,
          new Date(clock).toISOString(),
          new Date(clock).toISOString(),
        ],
      },
      {
        sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                                 reserved_cost, granted_at, expires_at)
              VALUES (?, ?, 'reception', 'coverage', ?, ?, 'cyc_dead', 1, ?, ?, ?)`,
        params: [
          `clm_dead_${String(slot)}`,
          ASSIGNMENT.recordId,
          // Under the policy the operator replaced at 12:45 to escape this very wedge: the
          // park is read per version and these settlements are not this version's, while the
          // BATCH is not — a ghost from yesterday's policy holds a slot today all the same,
          // which is why the reap and not the version is what frees it.
          "pol_0",
          jobId,
          ASSIGNMENT.reservedCost,
          new Date(clock).toISOString(),
          // The lease still has hours to run: what frees the slot is the reap, not expiry.
          new Date(clock + POLICY.leaseSeconds * 1000).toISOString(),
        ],
      },
    ]);
  }
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

  const reaping = await loop.tick();
  expect(reaping.settled.slice(0, 4).map((row) => [row.claimId, row.outcome])).toEqual([
    ["clm_dead_1", "abandoned"],
    ["clm_dead_2", "abandoned"],
    ["clm_dead_3", "abandoned"],
    ["clm_dead_4", "abandoned"],
  ]);
  // THE SLOT IS REUSABLE IN THE SAME CYCLE THAT FREED IT. The reap runs before the cycle asks
  // what it may draw, so the batch the coordinator is asked about holds four free slots, the
  // cycle draws instead of stopping on `batch`, and the work it drew TOOK ONE OF THEM: the
  // fifth settlement is the assignment this cycle claimed and then released when there was no
  // engine to post it to. A cycle stopped on a full batch never claims anything, which is
  // what a ghost used to cost for as long as the lease it was granted under.
  expect(reaping.stop?.reason).not.toBe("batch");
  expect(reaping.pulse.tick.gaps["batch"]).toBeUndefined();
  expect(draws.draws).toBe(1);
  expect(reaping.settled[4]?.claimId).toBe(ASSIGNMENT.id);
  clock = started;
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
    stageRecipes: {},
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
  // One review is this cycle's whole batch, so the cycle FILLS it and stops on its own success.
  draws.batchSize = 1;
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
  // THE STOP THAT MEANS THE LOOP WORKED, and it is its own word: a cycle that filled the batch
  // itself says `batch-filled`, where a cycle that found every slot held by somebody else says
  // `batch` (#382). Watch is silent for this one and speaks for that one.
  expect(posted.stop?.reason).toBe("batch-filled");
  expect(posted.stop?.detail).toContain("dispatched");
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
    inference: SESSION_METER,
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
  expect(settled.settled.map((row) => [row.outcome, row.cost])).toEqual([["completed", 0.41]]);
  expect((await store.run(`run_${ASSIGNMENT.id}_2`)).run).toMatchObject({
    calls: 3,
    costUsd: 0.41,
    tokens: 21_500,
  });
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
    stageRecipes: {},
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
  expect(settled.pulse.tick.refusals.paid).toEqual({ schema: 1 });
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

test("a submission the one validator refuses is recorded nowhere and still settles the claim at cost", async () => {
  /*
    F8 (#263), through the hub's own review path. A contribution beside an `environment` that
    scopes nothing is the shape the Go tree stated in three places and enforced inconsistently:
    the review contract required an environment on criterion results, the store refused any
    environment without an outcome, and a results-only assessment counted as empty — so an
    evidence review was paid for and then refused at submit. It is one function now, and
    `store/acts.test.ts` asserts that this same shape is refused under this same code from the
    store's side. What this pins is the OTHER half of the acceptance: the refusal is the row's,
    never the run's, so the claim is finished with what the model was paid.
  */
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const recipeId = "babel-triages-the-queue";
  draws.review = {
    machineId: MACHINE,
    profile: { containerId: "ctr_union", expectedRevision: 1 },
    stageRecipes: {},
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
  draws.pending = [{ ...ASSIGNMENT, role: "evidence" }];
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
        contributions: [{ kind: "comment", text: "the criteria are not stated" }],
        environment: "dev-01",
      }) +
      "\n```",
  });
  const settled = await loop.tick();

  // NOTHING WAS RECORDED: the scope rule is about the review as a whole, so there is no subset
  // of it to keep — which is the line between this and a refused contribution.
  expect(
    await db.query(`SELECT id FROM assessments WHERE record_id = ?`, [ASSIGNMENT.recordId]),
  ).toEqual([]);
  const run = (
    await db.query<{ closure: string; cost_usd: number; payload: string }>(
      `SELECT closure, cost_usd, payload FROM runs WHERE id = ?`,
      [`run_${ASSIGNMENT.id}_1`],
    )
  )[0]!;
  expect(run.closure).toBe("failed");
  const receipt = JSON.parse(run.payload) as Record<string, unknown>;
  expect(String(receipt["reason"])).toStartWith("schema:");
  expect(String(receipt["reason"])).toContain("environment");
  // AND IT IS SPEND: the claim is finished at the cost of the session that earned the refusal,
  // which is what stops the park heuristic reading a paid refusal as a free failure (#265).
  expect(run.cost_usd).toBeCloseTo(0.31, 6);
  expect(settled.settled.map((row) => [row.outcome, row.cost])).toEqual([["failed", 0.31]]);
  expect(settled.pulse.tick.refusals.paid).toEqual({ schema: 1 });
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
    stageRecipes: {},
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
    costUsd: 0.31,
  });
  expect((await store.run(`run_${ASSIGNMENT.id}_1`)).run?.calls).toBeNull();
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
 * model. Where nothing metered the job, the refusal code is the only evidence a model
 * answered, and it is what files this run under the park's `spent` rather than its `barren`.
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

test("three reviews paid for and refused park the loop on spend, with their claims settled", async () => {
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
  // THE CLAIMS ARE FINISHED AND NOT ABANDONED, at what the receipt says they cost: a refused
  // submission is spend, and the reservation is not charged over it (§6.5).
  expect(settling.settled.map((row) => [row.outcome, row.cost])).toEqual([
    ["failed", 0],
    ["failed", 0],
    ["failed", 0],
  ]);
  // …and the pulse says what they were, by the code `results.ts` names, under PAID: the
  // deployment bought three answers and the contract threw all three away.
  expect(settling.pulse.tick.refusals).toEqual({ paid: { schema: 3 }, free: {} });
  expect(settling.pulse.today.refusals).toEqual({ paid: { schema: 3 }, free: {} });
  // …so the loop parks, under the word that names the remedy. It is NOT the barren park: no
  // machine here is broken, and an operator sent to look at one would find nothing. This is
  // where the build goes beyond #265 — the issue asked only that a paid refusal stop reading
  // as a free failure, and a lane that burns three reservations on answers nobody can use is
  // worth stopping for the recipe as much as a dead machine is worth stopping for the machine.
  expect(settling.parked?.reason).toBe("spent");
  expect(settling.parked?.spent).toBe(3);
  expect(settling.parked?.barren).toBe(0);
  expect(settling.parked?.detail).toContain("paid for and refused");
  expect(settling.notes.some((note) => note.startsWith("the loop is parked on spent:"))).toBe(true);
  expect(settling.requested).toEqual([]);

  // An hour of quiet lifts a spend park exactly as it lifts a barren one: a recipe that has
  // been fixed is tried again without an operator having to say so.
  clock += 61 * 60_000;
  const resumed = await loop.tick();
  expect(resumed.parked).toBe(null);
  expect(resumed.stop?.reason).toBe("unrouted");
  expect(resumed.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
  expect(resumed.pulse.today.refusals).toEqual({ paid: { schema: 3 }, free: {} });
  clock = started;
});

/**
 * Three reviews the contract refused, settled under a meter that says how many calls the
 * owner counted for them. THE MONEY IS ZERO IN BOTH DIRECTIONS — a brokered call is priced at
 * the owner and this receipt never sees it — so the meter is the only thing separating the
 * two runs, which is the point: it is the hub's own witness and the receipt is not.
 */
async function refusedUnderMeter(calls: number): Promise<TickReport> {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
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
  const flights = await threeInFlight(db, fleet);
  clock += 60_000;
  for (const flight of flights) {
    fleet.metered(flight.jobId, {
      calls,
      inputTokens: 0,
      outputTokens: 0,
      cachedInputTokens: 0,
      costMicros: 0,
    });
    fleet.finish(flight.jobId, 0, refusedReview(flight.runId));
  }
  return await loop.tick();
}

test("a refusal the meter says reached no model is free, and one call makes the same refusal spend", async () => {
  const started = clock;

  // Same code, same zero cost, same receipt: a job the owner metered and counted no call for
  // submitted something no model produced. Nothing was bought, so nothing is filed as bought.
  const free = await refusedUnderMeter(0);
  expect(free.pulse.tick.refusals).toEqual({ paid: {}, free: { schema: 3 } });
  expect(free.parked?.reason).toBe("barren");
  expect(free.parked?.barren).toBe(3);
  expect(free.parked?.spent).toBe(0);

  // One call moves every one of those counts to the other side of the tally and renames the
  // park, on evidence the receipt never carried.
  const paid = await refusedUnderMeter(1);
  expect(paid.pulse.tick.refusals).toEqual({ paid: { schema: 3 }, free: {} });
  expect(paid.parked?.reason).toBe("spent");
  expect(paid.parked?.spent).toBe(3);
  expect(paid.parked?.barren).toBe(0);
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
  expect(parking.parked?.reason).toBe("barren");
  expect(parking.parked?.barren).toBe(3);
  expect(parking.parked?.spent).toBe(0);
  expect(parking.parked?.detail).toContain("reached no model and produced nothing");
  expect(parking.notes.some((note) => note.startsWith("the loop is parked on barren:"))).toBe(true);
  // Nothing was refused, either: a machine that killed the job bought no answer for anybody to
  // refuse, and a tally that counted one here is the free-failure-as-spend defect inverted.
  expect(parking.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
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
  expect(resumed.notes.some((note) => note.startsWith("the loop is parked"))).toBe(false);
  clock = started;
});

test("a batch every slot of which is already claimed stops on `batch` and draws nothing", async () => {
  const db = openDatabase();
  await seed(db);
  const fleet = new Fleet();
  const draws = new Draws(db);
  draws.review = ROUTE;
  draws.pending = [{ ...ASSIGNMENT }];
  // Four reviews on the route's machine, all of them still open: the batch is full of work
  // somebody else holds. THE WORD FOR THAT IS NOT THE WORD FOR A BATCH THIS CYCLE FILLED
  // ITSELF (#382) — one is the loop working and the other is the loop wedged, and a panel given
  // one word for both stays silent for the wedge.
  for (const slot of [1, 2, 3, 4]) {
    const jobId = `job_held_${String(slot)}`;
    fleet.running(jobId, MACHINE, OPERATIONS.evaluate);
    await db.batch([
      {
        sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, records, payload)
              VALUES (?, ?, ?, ?, ?, 0, '{}')`,
        params: [
          `run_held_${String(slot)}`,
          OPERATIONS.evaluate,
          MACHINE,
          jobId,
          new Date(clock).toISOString(),
        ],
      },
      {
        sql: `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
                                 reserved_cost, granted_at, expires_at)
              VALUES (?, ?, 'reception', 'coverage', ?, ?, 'cyc_held', 1, ?, ?, ?)`,
        params: [
          `clm_held_${String(slot)}`,
          ASSIGNMENT.recordId,
          POLICY.version,
          jobId,
          ASSIGNMENT.reservedCost,
          new Date(clock).toISOString(),
          new Date(clock + POLICY.leaseSeconds * 1000).toISOString(),
        ],
      },
    ]);
  }
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

  const wedged = await loop.tick();
  expect(wedged.stop?.reason).toBe("batch");
  expect(wedged.stop?.detail).toContain("already holds 4 of 4 review slots");
  // …and it never asked for work it had nowhere to put, so nothing was claimed for it.
  expect(draws.draws).toBe(0);
  expect(wedged.requested).toEqual([]);
  expect(wedged.pulse.tick.gaps).toEqual({ batch: 1 });
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

test("a day kept under a word this build cannot spell is read without it", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const draws = new Draws(db);
  const keys = new Keys();
  // WHAT SOME OTHER BUILD LEFT UNDER THE KEY. A cycle is a fresh conductor over the wake that
  // caused it, so the day's counts make a round trip through JSON and come back as whatever
  // is there: a word since renamed, a word not invented here, a misspelling of a real one, a
  // count that is not a count. Every one of them would otherwise reach a reader as a reason
  // he has no label for and cannot act on.
  keys.held[CONDUCTOR_TALLY_KEY] = JSON.stringify({
    day: new Date(clock).toISOString().slice(0, 10),
    gaps: { unrouted: 2, "no-such-reason": 7, clamied: 4, daily: -1 },
    refusals: { paid: { schema: 3, "not-a-code": 9 }, free: { "unknown-reference": 1 } },
  });
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys,
    plan: PLAN,
    now: () => clock,
  });

  const tick = await loop.tick();
  // The words this build spells are carried and added to; the rest are gone, and so is the
  // count that arrived negative. `unrouted` is 2 from the kept day plus this cycle's own one.
  expect(tick.pulse.today.gaps).toEqual({ unrouted: 3 });
  expect(tick.pulse.today.refusals).toEqual({
    paid: { schema: 3 },
    free: { "unknown-reference": 1 },
  });
  // …and what is written back holds only what a reader can be promised, so the next wake
  // cannot re-inherit it.
  expect(JSON.parse(keys.held[CONDUCTOR_TALLY_KEY] ?? "null")).toEqual({
    day: new Date(clock).toISOString().slice(0, 10),
    gaps: { unrouted: 3 },
    refusals: { paid: { schema: 3 }, free: { "unknown-reference": 1 } },
  });
  clock = started;
});

test("the cycle leaves its stop and its gaps, folded by reason, where the pulse door reads them", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const draws = new Draws(db);
  draws.review = ROUTE;
  // Four candidates declined for two reasons, and then a draw with nothing left to offer.
  // Four hundred would be the same two rows: the fold is what keeps a contended cycle from
  // answering a panel with a list it cannot render.
  draws.declined = [
    { recordId: "hyp_00000001", role: "evidence", reason: "claimed", detail: "held by a worker" },
    { recordId: "hyp_00000002", role: "evidence", reason: "claimed", detail: "held by a worker" },
    { recordId: "fnd_00000003", role: "outcome", reason: "cooling", detail: "reviewed 9m ago" },
    { recordId: "hyp_00000004", role: "evidence", reason: "claimed", detail: "held by a worker" },
  ];
  const keys = new Keys();
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys,
    plan: PLAN,
    now: () => clock,
  });

  const drew = await loop.tick();
  expect(drew.stop?.reason).toBe("no-candidates");
  expect(JSON.parse(keys.held[CONDUCTOR_CYCLE_KEY] ?? "null")).toEqual({
    at: new Date(clock).toISOString(),
    stop: { reason: "no-candidates", detail: "nothing due" },
    // Most declined first, and the first record of each reason with it: the count says how
    // much and the record says where to look.
    gaps: [
      { reason: "claimed", count: 3, recordId: "hyp_00000001", detail: "held by a worker" },
      { reason: "cooling", count: 1, recordId: "fnd_00000003", detail: "reviewed 9m ago" },
    ],
  });

  // A DISABLED POLICY IS THE COMMONEST "why is nothing running", and the coordinator never gets
  // to say it: the loop stops before it is asked. It is recorded in the coordinator's own word
  // rather than left as the silence it used to be.
  draws.enabled = false;
  clock += 60_000;
  await loop.tick();
  expect(JSON.parse(keys.held[CONDUCTOR_CYCLE_KEY] ?? "null")).toEqual({
    at: new Date(clock).toISOString(),
    stop: {
      reason: "disabled",
      detail: "the policy in force is not enabled, so the loop draws nothing",
    },
    gaps: [],
  });
  clock = started;
});

test("a key the host will not keep costs the cycle its explanation and nothing else", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const draws = new Draws(db);
  const keys = new Keys();
  keys.refusal = "storage is unavailable";
  const loop = conductor({
    engine: NO_CODE,
    store: openStore(db),
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys,
    plan: PLAN,
    now: () => clock,
  });

  // The loop's work is done by the time the verdict is written, and losing the explanation
  // must not lose the tick: the cycle answers, and the note says what could not be kept.
  const report = await loop.tick();
  expect(report.stop?.reason).toBe("unrouted");
  expect(report.notes).toContain("the cycle's own verdict cannot be kept: storage is unavailable");
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
function answered(path: string, digest: string, statement?: string, quote = ""): string {
  const result = {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: statement ?? "the catalog forgets archived sessions" },
        observations: [
          {
            ref: "o1",
            recipe: { id: "catalog-integrity", version: 3 },
            claim: {
              claim: "the archive wrote a snapshot the rescan did not carry",
              confidence: "high",
              impact: "moderate",
              evidence: [
                {
                  locator: {
                    path,
                    line: 12,
                    byte_offset: 0,
                    digest,
                    ...(quote === "" ? {} : { quote }),
                  },
                  note: "the rescan's row",
                },
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
 * AN ANSWER NOTHING IN WHICH CAN BE RECORDED: its one candidate states no claim at all.
 *
 * A submission is kept in part, so "refused" has to be tested at the limit as well as in the
 * middle: with every item refused there is no path-closed subset to keep, and the run is spend
 * with a receipt that says what it refused (#231).
 */
function unusable(): string {
  const result = { candidates: [{ ref: "h1", hypothesis: { statement: "" } }], questions: [] };
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
          // What the launch door writes when a launch names a model (#169): the model ASKED
          // for, which is the only thing on this row a fallback can be read against.
          askedModel: "anthropic/claude-opus-4-1",
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
      inference: SESSION_METER,
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
  // The terminal owner meter wins even when the transcript reports different totals.
  expect(receipt["inference"]).toEqual(SESSION_METER);
  expect(receipt["costUsd"]).toBe(0.31);
  expect(run["cost_usd"]).toBeCloseTo(0.41, 6);
  expect(Number(run["tokens"])).toBe(21_500);
  expect((await traceOf(db, runId)).calls).toMatchObject([
    { seq: 1, inputTokens: 20_000, outputTokens: 1_500, cacheReadTokens: 800, costMicros: 410_000 },
  ]);
  expect((await traceOf(db, runId)).calls).toHaveLength(1);

  expect(report.settled).toEqual([
    { claimId, outcome: "completed", cost: 0.41, overrun: false, refused: null, reason: null },
  ]);
  expect(report.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
});

test("the receipt names the remarks this run was quoted, and how many the bound left out", async () => {
  /*
    A CLAIM IS READ AGAINST WHAT THE RUN WAS TOLD (#331). The posting wake decided which of the
    operator's remarks fit the prompt and wrote them onto the run row; the settlement puts them
    on the receipt, because a reviewer holding a receipt is the reader who needs them.

    THE COUNT IS HALF OF IT. "This run was told one thing" is a different fact from "this run
    was told one of four things", and only the second explains a claim about a subject he had
    already asked Babel to leave alone.
  */
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
  const quoted = {
    carried: [
      {
        id: "stg_0001",
        text: "stop proposing work on the staging queue, it is going away",
        about: "",
        at: "2026-09-14T09:00:00Z",
      },
    ],
    omitted: 3,
  };
  await db.run(`UPDATE runs SET preparation = ? WHERE id = ?`, [
    JSON.stringify({ preset: "read-whats-new", selected: 1, steering: quoted }),
    runId,
  ]);

  await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (await db.query(`SELECT payload FROM runs WHERE id = ?`, [runId]))[0]!;
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["steering"]).toEqual(quoted);
});

test("a citation the material never served costs the claim and what rested on it, and the rest of the paid run stands", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    // The path is one the index names; the digest is not the one it was served at, which is a
    // retyped digest and exactly what the per-item locator check exists to catch: the rule is
    // `engine/citations.ts`'s and the grain is `itemRefusal`'s.
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

  /*
    WHAT A RETYPED DIGEST COSTS (#231). The observation cited bytes this run was never served,
    so that claim is refused; the finding consolidating it would then rest on a record nobody
    wrote, so it falls with it. The candidate rests on nothing and the question authorizes
    nothing, and both were paid for — before this they were thrown away with the claim.
  */
  const records = await db.query<{ kind: string }>(
    `SELECT kind FROM records WHERE run_id = ? ORDER BY kind`,
    [runId],
  );
  expect(records.map((row) => row.kind)).toEqual(["hypothesis"]);
  expect(
    await db.query(`SELECT COUNT(*) AS n FROM questions WHERE raised_by_id = ?`, [runId]),
  ).toEqual([{ n: 1n }]);

  const run = (
    await db.query(`SELECT closure, cost_usd, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("completed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["reason"]).toBeUndefined();
  expect(receipt["refusedItems"]).toEqual([
    {
      item: "/candidates/0/observations/0",
      reason: expect.stringContaining("unknown-reference:") as unknown,
    },
    {
      item: "/consolidations/0",
      reason: `development-path: consolidation "f1" rests on "o1", which this submission refused`,
    },
  ]);
  expect((receipt["counts"] as Record<string, number>)["itemsRefused"]).toBe(2);
  // THE MONEY IS STILL SPENT. The model answered and the deployment paid for it; a refusal
  // recorded at zero is how a fan reads a refused lane as free and relaunches into it.
  expect(run["cost_usd"]).toBeCloseTo(0.31, 6);
  expect(report.settled).toEqual([
    { claimId, outcome: "completed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
  // …AND THE RUN IS NOT A REFUSAL. `refusals` answers "which submissions did the deployment
  // pay for and get no result from"; this one produced records, so counting its dropped items
  // there would put a paid refusal against a cycle that delivered (#424). The measurement of
  // what was dropped is on the receipt above and in the cycle's notes.
  expect(report.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
  expect(
    report.notes.some((note) => note.includes("recorded the answer and refused 2 of its items")),
  ).toBe(true);
});

/** The material as `prepare` sealed it: one canonical record per line, twelve of them, and the
 *  twelfth is the one the fixture's locator names. */
const SERVED_SESSION = [
  ...Array.from({ length: 11 }, (_, index) =>
    JSON.stringify({ type: "message", text: `record ${String(index + 1)} says nothing useful` }),
  ),
  JSON.stringify({
    type: "message",
    text: "the rescan wrote no snapshot_id at all, so the column was cleared",
  }),
  "",
].join("\n");

/** One prepare job holding that material, under the job id `sessionInFlight` points its run at. */
function withMaterial(): Fleet {
  const fleet = new Fleet();
  fleet.prepared("job_prep_1", "dev-01", {
    [`${MATERIAL_SESSIONS}/${SERVED_FILE}`]: SERVED_SESSION,
  });
  return fleet;
}

/** The verdict the settlement wrote beside the first citation of the first record. */
async function verdictOf(db: PluginDatabase): Promise<Record<string, unknown> | undefined> {
  const rows = await db.query(
    `SELECT payload FROM records WHERE kind = 'observation' ORDER BY id LIMIT 1`,
  );
  const payload = JSON.parse(String(rows[0]?.["payload"] ?? "{}")) as {
    evidence?: { verification?: Record<string, unknown> }[];
  };
  return payload.evidence?.[0]?.verification;
}

test("a quote that is at the line it cites is verified, and the record carries the verdict", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(
        `sessions/${SERVED_FILE}`,
        SERVED_DIGEST,
        undefined,
        "the rescan wrote no snapshot_id at all",
      ),
    }),
  }));
  const { runId } = await sessionInFlight(db);

  await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: withMaterial(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("completed");
  // THE HUB OPENED THE BYTES. Without the read this would be `unchecked`, which is why this
  // test exists beside the one below: a check that never reaches a corpus accuses nothing and
  // proves nothing.
  expect(await verdictOf(db)).toEqual({ outcome: "verified", detail: "" });
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["citations"]).toEqual({
    verified: 1,
    moved: 0,
    absent: 0,
    unquoted: 0,
    unchecked: 0,
  });
});

test("a quote that is nowhere in the session it cites marks the record and refuses nothing", async () => {
  /*
    THE DECISION THIS TEST PINS (#348). A fabricated quote is recorded, not refused: the claim
    is the model's and the run is paid for either way, and discarding the whole answer over one
    citation is the all-or-nothing waste #231 and #311 measured. What must not happen is the
    verdict living only in a log — a reader of the record has to see it, so it is on the
    record's own evidence and counted on the receipt.
  */
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(
        `sessions/${SERVED_FILE}`,
        SERVED_DIGEST,
        undefined,
        "the rescan deleted the snapshot and logged the deletion",
      ),
    }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: withMaterial(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("completed");
  const written = await db.query(`SELECT count(*) AS held FROM records`);
  expect(Number(written[0]?.["held"])).toBeGreaterThan(0);
  const verdict = await verdictOf(db);
  expect(verdict?.["outcome"]).toBe("absent");
  expect(String(verdict?.["detail"])).toContain("nowhere in the session");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["reason"]).toBeUndefined();
  expect(receipt["citations"]).toEqual({
    verified: 0,
    moved: 0,
    absent: 1,
    unquoted: 0,
    unchecked: 0,
  });
  expect(report.settled).toEqual([
    { claimId, outcome: "completed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
  expect(report.notes.some((note) => note.includes("nowhere in the session named"))).toBe(true);
});

test("a preparation this hub can no longer read leaves the quote unchecked, not accused", async () => {
  // The material's lease is gone — the fake holds no `job_prep_1` at all — and the index is
  // still on the run row, so the citation is admissible and uncheckable at the same time. A
  // hub that called that a fabrication would be reporting its own reach as the model's fault.
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(
        `sessions/${SERVED_FILE}`,
        SERVED_DIGEST,
        undefined,
        "the rescan wrote no snapshot_id at all",
      ),
    }),
  }));
  const { runId } = await sessionInFlight(db);

  await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: new Fleet(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("completed");
  expect((await verdictOf(db))?.["outcome"]).toBe("unchecked");
});

/**
 * ONE ANSWER THAT IS BOTH: a claim whose locator was served and whose quote is really at the
 * line it names, and beside it a claim citing a file nobody served.
 *
 * The two halves of a citation answer differently — the scope half refuses the item, the quote
 * half marks the record — and this fixture is the only place they meet in one submission.
 */
function answeredWithOneUnservedClaim(quote: string): string {
  const observation = (ref: string, path: string) => ({
    ref,
    recipe: { id: "catalog-integrity", version: 3 },
    claim: {
      claim: "the archive wrote a snapshot the rescan did not carry",
      confidence: "high",
      impact: "moderate",
      evidence: [
        { locator: { path, line: 12, byte_offset: 0, digest: SERVED_DIGEST, quote }, note: "row" },
      ],
      counter_evidence_absent: true,
    },
  });
  const result = {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "the catalog forgets archived sessions" },
        observations: [observation("o1", `${MATERIAL_SESSIONS}/${SERVED_FILE}`)],
      },
      {
        ref: "h2",
        hypothesis: { statement: "the reaper reads a sealed session twice" },
        observations: [observation("o2", `${MATERIAL_SESSIONS}/0002-never-served.jsonl`)],
      },
    ],
  };
  return `Here is what I found.\n\n\`\`\`json\n${JSON.stringify(result)}\n\`\`\`\n`;
}

test("one unserved claim is dropped and the claim beside it still has its quote checked", async () => {
  /*
    THE TWO PROPERTIES HELD TOGETHER, because a merge is exactly where one of them goes quiet.
    The scope check and the quote check are one module (`engine/citations.ts`) asked at two
    grains: per item, by the contract's one validator, where it REFUSES; and over the kept
    result, by the settlement, where it MARKS. An answer carrying one of each has to come out
    with the bad claim named on the receipt, the good claim recorded, and the good claim's
    verdict on its own evidence.
  */
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answeredWithOneUnservedClaim("the rescan wrote no snapshot_id at all"),
    }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await conductor({
    engine: code,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: withMaterial(),
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  // BOTH CANDIDATES STAND — a hypothesis rests on nothing — and only the claim that cited
  // bytes nobody served is gone, so one observation is recorded and not two.
  const records = await db.query<{ kind: string }>(
    `SELECT kind FROM records WHERE run_id = ? ORDER BY kind`,
    [runId],
  );
  expect(records.map((row) => row.kind)).toEqual(["hypothesis", "hypothesis", "observation"]);

  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("completed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["reason"]).toBeUndefined();
  expect(receipt["refusedItems"]).toEqual([
    {
      item: "/candidates/1/observations/0",
      reason: expect.stringContaining("0002-never-served.jsonl") as unknown,
    },
  ]);
  // AND THE SURVIVOR'S QUOTE WAS ACTUALLY OPENED: the verdict is on the record's own evidence,
  // and the receipt counts the one citation that reached the check rather than both.
  expect(await verdictOf(db)).toEqual({ outcome: "verified", detail: "" });
  expect(receipt["citations"]).toEqual({
    verified: 1,
    moved: 0,
    absent: 0,
    unquoted: 0,
    unchecked: 0,
  });
  expect(report.settled).toEqual([
    { claimId, outcome: "completed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
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

  expect(report.runs).toEqual({ running: 1, atModel: 0, stalled: 0 });
  expect(await db.query(`SELECT run_id FROM run_progress WHERE run_id = ?`, [runId])).toEqual([]);
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
  // WHAT IT ASKED FOR SURVIVES A SESSION THAT ANSWERED NOTHING, and what answered does not
  // exist to record: `model` is the launch's own word, `models` is the transcript's, and a
  // cancelled session sealed no transcript (#169).
  expect(receipt["model"]).toBe("anthropic/claude-opus-4-1");
  expect(receipt["models"]).toBeUndefined();
  expect(run["cost_usd"]).toBeNull();
  expect(receipt["inference"]).toBeUndefined();
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
  expect(report.pulse.tick.refusals).toEqual({ paid: {}, free: {} });

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

test("a record whose own text names what it contradicts gets the edge only for an existing target", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  // Correction markers retain their existing semantics, but never link to dangling targets.
  const missing = `hyp_${"f".repeat(32)}`;
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage: answered(
        `sessions/${SERVED_FILE}`,
        SERVED_DIGEST,
        `CONTRADICTS hyp_00000001 and ${missing} in their strongest form: the catalog keeps them`,
      ),
    }),
  }));
  const { runId } = await sessionInFlight(db);

  const report = await wakeOn(store, draws, code).tick();
  expect(report.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
  expect(
    await db.query(`SELECT to_id FROM edges WHERE kind = 'contradicts' AND actor_id = ?`, [runId]),
  ).toEqual([{ to_id: "hyp_00000001" }]);
  // The ungrounded marker does not discard the independently valid local result.
  expect(await db.query(`SELECT COUNT(*) AS n FROM records WHERE run_id = ?`, [runId])).toEqual([
    { n: 4n },
  ]);
});

test("a wholly unusable answer writes no record at all, and is still settled as spend", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({ state: "exited", finalMessage: unusable() }),
  }));
  const { runId, claimId } = await sessionInFlight(db);

  const report = await wakeOn(store, draws, code).tick();

  // NOTHING LANDED, because there was nothing to land: every item this answer carried was
  // refused, so the path-closed subset is empty and the floor is moot.
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
  expect(String(receipt["reason"])).toStartWith("schema:");
  // THE REFUSED ITEMS ARE ON THE RECEIPT EVEN HERE. They are what the whole refusal was made
  // of, and they are the only measurement the spend bought.
  expect(receipt["refusedItems"]).toEqual([
    { item: "/candidates/0", reason: expect.stringContaining("schema:") as unknown },
  ]);
  expect(receipt["counts"]).toEqual({ itemsRefused: 1 });
  // AND IT IS STILL SPEND, with the claim finished at what the model was paid.
  expect(run["cost_usd"]).toBeCloseTo(0.31, 6);
  expect(report.settled).toEqual([
    { claimId, outcome: "failed", cost: 0.31, overrun: false, refused: null, reason: null },
  ]);
  // A WHOLLY REFUSED SUBMISSION IS A REFUSAL OF THE RUN, and a PAID one: the hub's meter
  // attached a call to this job, so the money left the day's allowance (#424, `paidRefusal`).
  expect(report.pulse.tick.refusals).toEqual({ paid: { schema: 1 }, free: {} });
});

test("a consolidation resting on a candidate skips the development path, and the candidate still stands", async () => {
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

  // The candidate is a record this run produced and the finding is not one it could have: one
  // of the two is refused, and the store now holds the other instead of neither.
  const records = await db.query<{ kind: string }>(`SELECT kind FROM records WHERE run_id = ?`, [
    runId,
  ]);
  expect(records.map((row) => row.kind)).toEqual(["hypothesis"]);
  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("completed");
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["reason"]).toBeUndefined();
  expect(receipt["refusedItems"]).toEqual([
    {
      item: "/consolidations/0",
      reason: expect.stringContaining("hypothesis rather than an observation") as unknown,
    },
  ]);
  // One item dropped out of a run that recorded the rest is not a refusal of the run.
  expect(report.pulse.tick.refusals).toEqual({ paid: {}, free: {} });
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
  // AND THE CALL IS ONE CALL. `(run_id, seq)` is the trace's idempotency key, so a replayed
  // settlement leaves the account of what the model answered exactly as it found it — a second
  // row here would double a run's spend in every trace that ever reads it (#349).
  expect(await db.query(`SELECT COUNT(*) AS n FROM run_calls WHERE run_id = ?`, [runId])).toEqual([
    { n: 1n },
  ]);
});

// ------------------------------------------------------------- a run's replayable trace (#349)

/*
  WHAT THESE PROVE, AND WHAT NO TEST HERE COULD.

  THE HUB HANDS A PLUGIN NO PER-CALL TRAFFIC. Its `inference_call` frame is metering by its own
  protocol — the model, the tokens, the price, never a prompt or a byte of the answer — and a
  Code session's job belongs to `atyrode.omp`, which `ctx.jobs` may neither follow nor journal,
  so Babel is served not even that. What a settlement is given is omp's receipt: a session id, a
  PATH, the model, the agent's last message, one usage total, an exit code.

  So the subject below is the LOCATOR discipline rather than a transcript Babel keeps. The
  second test reaches into `machine/` on purpose: the claim under test is precisely that the
  string the server half wrote resolves with the reader the machine half already has, and a test
  that resolved it with a second implementation would prove only that the second one agreed
  with itself.
*/

/** A Code that answers each read with the session the asked job's id names. */
function codeReplying(
  replies: Readonly<Record<string, SessionRead>>,
): CodeEngine & { readonly asked: { containerId: string; jobId: string }[] } {
  const asked: { containerId: string; jobId: string }[] = [];
  return {
    asked,
    profiles: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
    checkProfile: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
    runSession: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
    readSession: async (args) => {
      asked.push(args);
      const reply = replies[args.jobId];
      return await Promise.resolve(
        reply === undefined
          ? refusedByCode<SessionRead>("engine_unavailable", `no session for ${args.jobId}`)
          : { ok: true, value: reply },
      );
    },
    cancelSession: async () =>
      await Promise.resolve(refusedByCode("engine_unavailable", "not asked here")),
  };
}

/**
 * One more Code session in flight beside the one {@link sessionInFlight} seeded, reading the
 * same preparation. No claim: what these runs are for is the trace they leave, and a claim
 * would only add a settlement the assertions never read.
 */
async function anotherSession(
  db: PluginDatabase,
  suffix: string,
  identityKey = "victorballu@gmail.com",
  askedModel = "anthropic/claude-opus-4-1",
): Promise<{ runId: string; jobId: string }> {
  const runId = `run_session_${suffix}`;
  const jobId = `job_code_${suffix}`;
  await db.run(
    `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id, profile,
                      preparation, started_at, records, payload)
     VALUES (?, ?, 'dev-01', ?, 'ctr_workbench', 'job_prep_1', ?, ?, ?, 0, '{}')`,
    [
      runId,
      OPERATIONS.explore,
      jobId,
      JSON.stringify({
        containerId: "ctr_workbench",
        expectedRevision: 7,
        account: { provider: "anthropic", identityKey },
        askedModel,
      }),
      JSON.stringify({ preset: "read-whats-new", selected: 1 }),
      new Date(clock).toISOString(),
    ],
  );
  return { runId, jobId };
}

/** One run's trace, refusing the null a run this hub never recorded would answer with. */
async function traceOf(db: PluginDatabase, runId: string): Promise<RunTrace> {
  const read = await readRunTrace(db, runId);
  if (read === null) throw new Error(`no trace for run ${runId}`);
  return read;
}

test.each(["explore", "title"] as const)(
  "a charged %s failure keeps its meter without a transcript",
  async (kind) => {
    const db = openDatabase();
    await seed(db);
    const store = openReadStore(db, () => clock);
    const draws = new Draws(db);
    const { runId, jobId } =
      kind === "explore" ? await sessionInFlight(db) : await titlingInFlight(db);
    const code = codeAnswering(() => ({
      ok: true,
      value: sessionRead({ jobId, state: "exited", sealed: false, inference: SESSION_METER }),
    }));

    const report = await wakeOn(store, draws, code).tick();

    expect((await store.run(runId)).run).toMatchObject({
      state: "failed",
      costUsd: 0.41,
      tokens: 21_500,
      calls: 3,
      progress: null,
      records: 0,
    });
    expect(report.ingested.find((run) => run.runId === runId)).toMatchObject({
      closure: "failed",
      costUsd: 0.41,
    });
    if (kind === "explore")
      expect(report.settled).toMatchObject([{ outcome: "failed", cost: 0.41 }]);
    expect((await traceOf(db, runId)).calls).toMatchObject([
      {
        seq: 1,
        costMicros: 410_000,
        inputTokens: 20_000,
        outputTokens: 1_500,
        closure: "failed",
        response: { digest: "", bytes: 0 },
        transcript: { host: "", sessionId: "", path: "" },
      },
    ]);
    expect(report.pulse.tick.refusals.paid).toEqual({ empty: 1 });
  },
);

test("a transcript without an owner meter corroborates spend but never supplies calls", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const { runId } = await sessionInFlight(db);
  const unknown = await anotherSession(db, "unknown");
  const code = codeReplying({
    job_code_1: sessionRead({
      state: "exited",
      finalMessage: answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST),
    }),
    [unknown.jobId]: sessionRead({ jobId: unknown.jobId, state: "exited", sealed: false }),
  });

  await wakeOn(store, draws, code).tick();

  const corroborated = await store.run(runId);
  expect(corroborated.run).toMatchObject({ calls: null, costUsd: 0.31, tokens: 12_900 });
  expect(corroborated.receipt?.["inference"]).toBeUndefined();
  const absent = await store.run(unknown.runId);
  expect(absent.run).toMatchObject({ calls: null, costUsd: null, tokens: null, state: "failed" });
  expect(absent.receipt?.["inference"]).toBeUndefined();
});

test("coalesced native model turns retain their own clock without restarting on repeated snapshots", async () => {
  const started = clock;
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const { runId } = await sessionInFlight(db);
  let stageAt = clock;
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "started",
      sealed: false,
      activity: {
        inferenceUsage: { ...SESSION_METER, calls: 1, lastModel: null },
        progress: { stage: RUN_STAGES.atModel, at: stageAt },
      },
    }),
  }));
  try {
    await wakeOn(store, draws, code).tick();
    clock += 100_000;
    stageAt = clock;
    expect((await wakeOn(store, draws, code).tick()).runs.stalled).toBe(0);
    expect((await store.run(runId)).run?.progress?.since).toBe(new Date(stageAt).toISOString());

    clock += 60_000;
    expect((await wakeOn(store, draws, code).tick()).runs.stalled).toBe(0);
    expect((await store.run(runId)).run?.progress?.since).toBe(new Date(stageAt).toISOString());
    clock += 30_000;
    expect((await wakeOn(store, draws, code).tick()).runs.stalled).toBe(1);
  } finally {
    clock = started;
  }
});

test("native live snapshots replace spend and yield to the terminal meter exactly once", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openReadStore(db, () => clock);
  const draws = new Draws(db);
  const { runId } = await sessionInFlight(db);
  let read = sessionRead({
    state: "started",
    sealed: false,
    activity: {
      inferenceUsage: {
        calls: 1,
        inputTokens: 1_000,
        outputTokens: 100,
        cachedInputTokens: 50,
        costMicros: 100_000,
        lastModel: FIXTURE_MODEL,
      },
      progress: null,
    },
  });
  const code = codeAnswering(() => ({ ok: true, value: read }));

  expect((await wakeOn(store, draws, code).tick()).runs).toEqual({
    running: 1,
    atModel: 0,
    stalled: 0,
  });
  await wakeOn(store, draws, code).tick();
  expect((await store.run(runId)).run?.progress).toMatchObject({
    stage: "",
    calls: 1,
    inputTokens: 1_000,
    outputTokens: 100,
    costUsd: 0.1,
    lastModel: FIXTURE_MODEL,
  });
  read = sessionRead({
    state: "started",
    sealed: false,
    activity: {
      inferenceUsage: null,
      progress: { stage: "reading material", at: clock, message: "opening sources", fraction: 0.5 },
    },
  });
  expect((await wakeOn(store, draws, code).tick()).runs.atModel).toBe(0);
  expect((await store.run(runId)).run?.progress).toMatchObject({
    stage: "reading material",
    message: "opening sources",
    fraction: 0.5,
    calls: 1,
    costUsd: 0.1,
  });
  read = sessionRead({ state: "started", sealed: false });
  await wakeOn(store, draws, code).tick();
  expect((await store.run(runId)).run?.progress).toMatchObject({
    stage: "reading material",
    calls: 1,
    costUsd: 0.1,
  });
  read = sessionRead({
    state: "exited",
    finalMessage: answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST),
    inference: SESSION_METER,
  });
  const settled = await wakeOn(store, draws, code).tick();
  expect(settled.settled).toMatchObject([{ outcome: "completed", cost: 0.41 }]);
  expect((await store.run(runId)).run).toMatchObject({
    calls: 3,
    costUsd: 0.41,
    tokens: 21_500,
    progress: null,
  });
  expect((await wakeOn(store, draws, code).tick()).settled).toEqual([]);
  expect((await traceOf(db, runId)).calls).toHaveLength(1);
  expect(await db.query(`SELECT actual_cost FROM claims WHERE job_id = 'job_code_1'`)).toEqual([
    { actual_cost: 0.41 },
  ]);
});

test("a settled session leaves a call row: what it cost, how it ended, and where the bytes are", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const finalMessage = answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({ state: "exited", finalMessage }),
  }));
  const { runId } = await sessionInFlight(db);

  await wakeOn(store, draws, code).tick();

  const call = (await db.query(`SELECT * FROM run_calls WHERE run_id = ?`, [runId]))[0]!;
  expect(call["seq"]).toBe(1n);
  expect(call["model"]).toBe("anthropic/claude-opus-4-1");
  expect(call["closure"]).toBe("completed");
  expect(call["refusal"]).toBe("");
  expect(call["exit_code"]).toBe(0n);
  // THE METER'S FIVE NUMBERS, KEPT APART. The receipt folds input and output into one `tokens`
  // and drops both cache buckets, so a spend read off a run row cannot be decomposed into what
  // was fresh, what was cached and what that cost — which is the arithmetic an audit redoes.
  expect([
    call["input_tokens"],
    call["output_tokens"],
    call["cache_read_tokens"],
    call["cache_write_tokens"],
    call["cost_micros"],
  ]).toEqual([12_000n, 900n, 400n, 0n, 310_000n]);
  // THE LOCATOR, AND NOT ONE BYTE OF WHAT IT POINTS AT.
  expect(call["transcript_host"]).toBe("dev-01");
  expect(call["transcript_session"]).toBe("ses_1");
  expect(call["transcript_path"]).toBe("/home/job/.omp/agent/sessions/ses_1.jsonl");
  expect(call["response_digest"]).toBe(createHash("sha256").update(finalMessage).digest("hex"));
  expect(call["response_bytes"]).toBe(BigInt(new TextEncoder().encode(finalMessage).byteLength));
  // NO BODY ANYWHERE ON THE ROW, asserted over the whole row rather than column by column: a
  // column added later that quietly kept the message would be caught by the message's own words.
  const rendered = JSON.stringify(call, (_key: string, value: unknown) =>
    typeof value === "bigint" ? String(value) : value,
  );
  expect(rendered).not.toContain("the catalog forgets archived sessions");
  expect(rendered).not.toContain("```");
});

test("the locator on a call row resolves to the transcript that holds the answer", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const finalMessage = answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST);
  /*
    ONE REAL omp TRANSCRIPT ON DISK, in the layout the adapter claims: the session record, the
    request Babel posted, and the turn that answered it. Its keys are written in the order the
    normalizer sorts them into, so each record's canonical form IS the line — which is what
    lets the offsets below address it without a second implementation of the numbering, the
    hazard `machine/prepare.ts` names about `digests` and `resolveRedaction`.
  */
  const directory = mkdtempSync(join(tmpdir(), "babel-transcript-"));
  temporaries.push(directory);
  const project = join(directory, "sessions", "babel");
  mkdirSync(project, { recursive: true });
  const transcript = join(project, "01K_ses_replay.jsonl");
  const turn = JSON.stringify({
    message: { content: [{ text: finalMessage, type: "text" }], role: "assistant" },
    type: "message",
  });
  writeFileSync(
    transcript,
    [
      JSON.stringify({ id: "ses_replay", type: "session" }),
      JSON.stringify({
        message: { content: [{ text: "read the material", type: "text" }], role: "user" },
        type: "message",
      }),
      turn,
      "",
    ].join("\n"),
  );
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      finalMessage,
      sessionId: "ses_replay",
      sessionPath: transcript,
    }),
  }));
  const { runId } = await sessionInFlight(db);

  await wakeOn(store, draws, code).tick();

  const call = (
    await db.query(`SELECT transcript_path, response_digest FROM run_calls WHERE run_id = ?`, [
      runId,
    ])
  )[0]!;

  // THE LOCATOR NAMES A SESSION BABEL'S OWN CATALOGUE KEYS ON. The adapter that claims a log is
  // the one thing that turns a path into a selector, so a row whose path it refuses would be a
  // row pointing at a session nothing can find.
  const ref = omp.claim(String(call["transcript_path"]));
  expect(ref?.selector).toBe("omp/babel/01K_ses_replay");

  // AND THE BYTES COME BACK, through the resolver the secret preflight already uses, on the
  // machine that holds the log. The row did none of this: the row only said where.
  const resolved =
    ref === null ? null : await resolveRedaction(ref, { line: 3, offset: 0, length: turn.length });
  expect(resolved?.value).toBe(turn);
  expect(String(call["response_digest"])).toBe(
    createHash("sha256").update(finalMessage).digest("hex"),
  );
});

test("a refused answer leaves a call row carrying the refusal and what it cost", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const code = codeAnswering(() => ({
    ok: true,
    // An answer with nothing in it the store may hold: the submission is refused whole.
    value: sessionRead({ state: "exited", finalMessage: unusable() }),
  }));
  const { runId } = await sessionInFlight(db);

  await wakeOn(store, draws, code).tick();

  // A REFUSED SUBMISSION IS SPEND, and the trace is where that stops being a sentence in a
  // receipt and becomes a countable row: the call happened, it cost 310,000 micro-dollars, and
  // the code beside it says the corpus kept none of what it bought.
  const call = (
    await db.query(
      `SELECT closure, refusal, cost_micros, response_digest FROM run_calls WHERE run_id = ?`,
      [runId],
    )
  )[0]!;
  expect(call["closure"]).toBe("failed");
  expect(call["refusal"]).toBe("schema");
  expect(call["cost_micros"]).toBe(310_000n);
  expect(call["response_digest"]).not.toBe("");
  expect(await db.query(`SELECT id FROM records WHERE run_id = ?`, [runId])).toEqual([]);
});

test("two runs are diffed on what actually differed between them", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  /*
    FOUR RUNS OF ONE PREPARATION, SETTLED IN ONE WAKE. A and B are the same request answered
    with the same bytes — the comparison the Jev bench made sixteen times to find one match. C
    is the same request answered differently, and its statement is the same length as A's so the
    only field that can move is the digest. D asked with a different account, which disqualifies
    any comparison of the answers whatever they were.
  */
  const same = answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST);
  const other = answered(
    `sessions/${SERVED_FILE}`,
    SERVED_DIGEST,
    "the catalog forgets restored sessions",
  );
  const a = await sessionInFlight(db);
  const b = await anotherSession(db, "2");
  const c = await anotherSession(db, "3");
  const d = await anotherSession(db, "4", "someone.else@example.invalid");
  const code = codeReplying({
    [a.jobId]: sessionRead({ state: "exited", finalMessage: same, sessionId: "ses_a" }),
    [b.jobId]: sessionRead({ state: "exited", finalMessage: same, sessionId: "ses_b" }),
    [c.jobId]: sessionRead({ state: "exited", finalMessage: other, sessionId: "ses_c" }),
    [d.jobId]: sessionRead({ state: "exited", finalMessage: same, sessionId: "ses_d" }),
  });

  await wakeOn(store, draws, code).tick();

  const traceA = await traceOf(db, a.runId);
  const traceB = await traceOf(db, b.runId);
  const traceC = await traceOf(db, c.runId);
  const traceD = await traceOf(db, d.runId);

  // THE SAME REQUEST, THE SAME ANSWER: nothing differed, and the verdict says which of the two
  // reasons that could be — they agreed, rather than neither of them having answered.
  const agreed = diffRunTraces(traceA, traceB);
  expect(agreed.verdict).toBe("same-answer");
  expect(agreed.differed).toEqual([]);
  expect(agreed.same).toContain("response");

  // THE SAME REQUEST, A DIFFERENT ANSWER, and the diff names the one field that moved with both
  // values in it — which is the whole of what "check the conclusion against the traffic" needs
  // from a pair, without either answer being kept.
  const disagreed = diffRunTraces(traceA, traceC);
  expect(disagreed.verdict).toBe("different-answer");
  expect(disagreed.differed.map((entry) => entry.field)).toEqual(["response"]);
  expect(disagreed.differed[0]?.a).toBe(traceA.calls[0]?.response.digest);
  expect(disagreed.differed[0]?.b).toBe(traceC.calls[0]?.response.digest);
  expect(disagreed.same).toContain("costMicros");

  // A DIFFERENT REQUEST IS NOT A DIFFERENT ANSWER. The two spent different windows, so the
  // verdict refuses to comment on agreement it has no grounds for, and names why.
  const incomparable = diffRunTraces(traceA, traceD);
  expect(incomparable.verdict).toBe("different-request");
  expect(incomparable.differed).toEqual([
    {
      field: "account",
      side: "request",
      a: "anthropic/victorballu@gmail.com",
      b: "anthropic/someone.else@example.invalid",
    },
  ]);
});

test("two runs that sealed no transcript agreed about nothing, and the verdict says so", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const a = await sessionInFlight(db);
  const b = await anotherSession(db, "2");
  const code = codeReplying({
    [a.jobId]: sessionRead({ state: "cancelled", sealed: false }),
    [b.jobId]: sessionRead({ state: "cancelled", sealed: false }),
  });

  await wakeOn(store, draws, code).tick();

  // A CANCELLED SESSION STILL LEAVES ITS CALL: the run reached Code, Code reported an ending,
  // and a trace that skipped the row would make a stopped run indistinguishable from one that
  // was never posted. It carries no locator, because there is no log to point at.
  const call = (
    await db.query(
      `SELECT closure, model, cost_micros, exit_code, response_digest, transcript_path
         FROM run_calls WHERE run_id = ?`,
      [a.runId],
    )
  )[0]!;
  expect(call["closure"]).toBe("stopped");
  expect([call["model"], call["response_digest"], call["transcript_path"]]).toEqual(["", "", ""]);
  expect([call["cost_micros"], call["exit_code"]]).toEqual([0n, null]);

  const diff = diffRunTraces(await traceOf(db, a.runId), await traceOf(db, b.runId));
  // NOT `same-answer`: neither run answered, and two silences are not an agreement.
  expect(diff.verdict).toBe("unanswered");
  expect(diff.differed).toEqual([]);
});

/*
  THE MODEL THAT ANSWERED IS NOT THE MODEL THAT WAS ASKED FOR (#169).

  Both runs asked for opus, as the launch door records on the run row; one of them was served
  by sonnet and said so in the transcript it sealed. Before this the receipt's `model` was
  written from the transcript, so the fallback was recorded as an intent nobody had — and the
  two runs, which asked for exactly the same thing, compared as `different-request`, a verdict
  that disqualifies every other field of the comparison.
*/
test("a fallback records the model that answered, and the request stays the one that was asked", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const same = answered(`sessions/${SERVED_FILE}`, SERVED_DIGEST);
  const fell = await sessionInFlight(db);
  const held = await anotherSession(db, "2");
  const code = codeReplying({
    [fell.jobId]: sessionRead({
      state: "exited",
      finalMessage: same,
      sessionId: "ses_fell",
      model: "anthropic/claude-sonnet-4",
    }),
    [held.jobId]: sessionRead({ state: "exited", finalMessage: same, sessionId: "ses_held" }),
  });

  await wakeOn(store, draws, code).tick();

  const receipt = JSON.parse(
    String((await db.query(`SELECT payload FROM runs WHERE id = ?`, [fell.runId]))[0]?.["payload"]),
  ) as Record<string, unknown>;
  // ASKED on the left, ANSWERED on the right, and they disagree — which is the whole fact.
  expect(receipt["model"]).toBe("anthropic/claude-opus-4-1");
  expect(receipt["models"]).toEqual(["anthropic/claude-sonnet-4"]);
  // The call row is the per-call account of the same thing: what ANSWERED this call.
  expect(
    (await db.query(`SELECT model FROM run_calls WHERE run_id = ?`, [fell.runId]))[0]?.["model"],
  ).toBe("anthropic/claude-sonnet-4");

  const diff = diffRunTraces(await traceOf(db, fell.runId), await traceOf(db, held.runId));
  // THE SAME REQUEST. Two identical asks, answered with identical bytes — and the one field
  // that moved is on the ANSWER side and names both models, so the substitution is readable
  // instead of being charged to the request.
  expect(diff.verdict).toBe("same-answer");
  expect(diff.differed).toEqual([
    {
      field: "answered",
      side: "answer",
      a: "anthropic/claude-sonnet-4",
      b: "anthropic/claude-opus-4-1",
    },
  ]);
});

// ---------------------------------------------- a run that names untitled sessions (#342)

/** The two untitled sessions a titling run is offered, and the material that sealed them. */
const NAMED_MATERIAL: MaterialIndex = {
  schema: MATERIAL_SCHEMA,
  preparationId: "prep-title",
  preparedAt: "2026-09-12T09:00:00Z",
  machineId: MACHINE,
  sessions: [
    {
      selector: "codex/untitled-a",
      harness: "codex",
      sourceId: "untitled-a",
      captureDigest: "c".repeat(64),
      sourceDigest: "d".repeat(64),
      file: "0001-codex-untitled-a.jsonl",
      records: 9,
      bytes: 2048,
    },
    {
      selector: "codex/untitled-b",
      harness: "codex",
      sourceId: "untitled-b",
      captureDigest: "e".repeat(64),
      sourceDigest: "f".repeat(64),
      file: "0002-codex-untitled-b.jsonl",
      records: 4,
      bytes: 1024,
    },
  ],
};

/**
 * A TITLING CODE SESSION IN FLIGHT: the two untitled catalog rows, the run row `inferTitles`
 * wrote with the selectors it offered, and the settled `prepare` whose receipt carries the
 * material those selectors were sealed into.
 */
async function titlingInFlight(
  db: PluginDatabase,
  offered: readonly string[] = ["codex/untitled-a", "codex/untitled-b"],
): Promise<{ runId: string; jobId: string }> {
  const runId = "run_title_1";
  const jobId = "job_code_title";
  await db.batch([
    ...NAMED_MATERIAL.sessions.map((entry) => ({
      sql:
        `INSERT INTO sessions(selector, host, harness, source_id, title, title_provenance, seen_at) ` +
        `VALUES (?, ?, 'codex', ?, NULL, NULL, '2026-09-01T00:00:00Z')`,
      params: [entry.selector, MACHINE, entry.sourceId],
    })),
    {
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id, profile,
                             preparation, started_at, records, payload)
            VALUES (?, ?, ?, ?, 'ctr_workbench', 'job_prep_title', ?, ?, ?, 0, '{}')`,
      params: [
        runId,
        OPERATIONS.title,
        MACHINE,
        jobId,
        JSON.stringify({ containerId: "ctr_workbench", expectedRevision: 7 }),
        JSON.stringify({ titles: { selectors: offered, reserved: 0.0625 } }),
        new Date(clock).toISOString(),
      ],
    },
    {
      sql: `INSERT INTO runs(id, kind, machine_id, job_id, started_at, finished_at, closure,
                             records, payload)
            VALUES ('run_prep_title', ?, ?, 'job_prep_title', ?, ?, 'completed', 0, ?)`,
      params: [
        OPERATIONS.prepare,
        MACHINE,
        new Date(clock).toISOString(),
        new Date(clock).toISOString(),
        JSON.stringify({
          runId: "run_prep_title",
          kind: "prepare",
          closure: "completed",
          material: NAMED_MATERIAL,
        }),
      ],
    },
  ]);
  return { runId, jobId };
}

/** One titling answer, in the fenced block the prompt asks the model to end with. */
function namedAnswer(titles: readonly Record<string, string>[]): string {
  return `Done.\n\n\`\`\`json\n${JSON.stringify({ titles })}\n\`\`\`\n`;
}

test("a titling session names the sessions it read, marks them inferred, and is never asked twice", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const { runId, jobId } = await titlingInFlight(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      jobId,
      inference: SESSION_METER,
      finalMessage: namedAnswer([
        { selector: "codex/untitled-a", title: "Restic retention on dev-01" },
        { selector: "codex/untitled-b", error: "the log holds one aborted turn and no request" },
      ]),
    }),
  }));

  await wakeOn(store, draws, code).tick();

  // THE TITLE LANDS ON THE CATALOG ROW WITH ITS PROVENANCE, which is what a listing reads.
  expect(
    await db.query(
      `SELECT selector, title, title_provenance FROM sessions
        WHERE selector LIKE 'codex/%' ORDER BY selector`,
    ),
  ).toEqual([
    {
      selector: "codex/untitled-a",
      title: "Restic retention on dev-01",
      title_provenance: "inferred",
    },
    // The session the model declined keeps its empty column: a guess it refused to make is
    // not a title, and inventing one is worse than the selector it would replace.
    { selector: "codex/untitled-b", title: null, title_provenance: null },
  ]);

  // BOTH ARE ANSWERED IN THE DURABLE LEDGER, and the declined one carries the model's reason —
  // that row is the whole of why it is never offered again.
  expect(
    await db.query(`SELECT selector, title, reason, run_id FROM session_titles ORDER BY selector`),
  ).toEqual([
    {
      selector: "codex/untitled-a",
      title: "Restic retention on dev-01",
      reason: "",
      run_id: runId,
    },
    {
      selector: "codex/untitled-b",
      title: "",
      reason: "the log holds one aborted turn and no request",
      run_id: runId,
    },
  ]);

  const run = (
    await db.query(`SELECT closure, cost_usd, payload FROM runs WHERE id = ?`, [runId])
  )[0]!;
  expect(run["closure"]).toBe("completed");
  expect(run["cost_usd"]).toBe(0.41);
  const receipt = JSON.parse(String(run["payload"])) as Record<string, unknown>;
  expect(receipt["kind"]).toBe("title");
  expect(receipt["counts"]).toEqual({ offered: 2, named: 1, unnamed: 1 });
  expect(receipt["inference"]).toEqual(SESSION_METER);
  expect((await traceOf(db, runId)).calls).toMatchObject([
    { seq: 1, inputTokens: 20_000, outputTokens: 1_500, costMicros: 410_000 },
  ]);

  // A SECOND WAKE OVER THE SAME RUN CHANGES NOTHING. The run is settled, so nothing reads the
  // session again; and were the settlement itself replayed, the ledger's first answer stands.
  clock += 60_000;
  await wakeOn(store, draws, code).tick();
  expect(await db.query<{ n: bigint }>(`SELECT COUNT(*) AS n FROM session_titles`)).toEqual([
    { n: 2n },
  ]);
  clock -= 60_000;
});

test("a session the run never read is answered with that, not left for the next cycle", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  // Three offered, two sealed: the third's log went away between the catalog and the machine.
  const { runId } = await titlingInFlight(db, [
    "codex/untitled-a",
    "codex/untitled-b",
    "codex/vanished",
  ]);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      jobId: "job_code_title",
      // The model answers one of the two it was shown and says nothing about the other.
      finalMessage: namedAnswer([{ selector: "codex/untitled-a", title: "Archive retention" }]),
    }),
  }));

  await wakeOn(store, draws, code).tick();

  expect(
    await db.query(`SELECT selector, title, reason FROM session_titles ORDER BY selector`),
  ).toEqual([
    { selector: "codex/untitled-a", title: "Archive retention", reason: "" },
    {
      selector: "codex/untitled-b",
      title: "",
      reason: "the run's answer said nothing about this session",
    },
    {
      selector: "codex/vanished",
      title: "",
      reason: "the preparation job_prep_title sealed no log for this session",
    },
  ]);
  expect(
    (await db.query<{ payload: string }>(`SELECT payload FROM runs WHERE id = ?`, [runId]))[0]
      ?.payload,
  ).toContain(`"offered":3`);
});

test("an answer naming a session the run was never served is refused, and every offer still answered", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  const { runId } = await titlingInFlight(db);
  const code = codeAnswering(() => ({
    ok: true,
    value: sessionRead({
      state: "exited",
      jobId: "job_code_title",
      finalMessage: namedAnswer([{ selector: "omp/s1", title: "the operator's own work" }]),
    }),
  }));

  await wakeOn(store, draws, code).tick();

  // The catalog row the answer reached for is untouched: a title may only be written onto a
  // session the run was actually served.
  expect(await db.query(`SELECT title FROM sessions WHERE selector = 'omp/s1'`)).toEqual([
    { title: "first title" },
  ]);
  const run = (await db.query(`SELECT closure, payload FROM runs WHERE id = ?`, [runId]))[0]!;
  expect(run["closure"]).toBe("failed");
  expect(String(run["payload"])).toContain("unknown-reference");
  // A REFUSED SUBMISSION IS STILL SPEND AND STILL AN ANSWER: both offered sessions carry the
  // refusal, so the next cycle pays to ask the same question again of nobody.
  expect(
    await db.query<{ n: bigint }>(
      `SELECT COUNT(*) AS n FROM session_titles WHERE title = '' AND reason LIKE 'unknown-reference%'`,
    ),
  ).toEqual([{ n: 2n }]);
});

test("a title the harness itself records displaces an inferred one, and a scan that reads none leaves it", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  draws.review = ROUTE;
  await titlingInFlight(db);
  await wakeOn(
    store,
    draws,
    codeAnswering(() => ({
      ok: true,
      value: sessionRead({
        state: "exited",
        jobId: "job_code_title",
        finalMessage: namedAnswer([
          { selector: "codex/untitled-a", title: "Restic retention on dev-01" },
          { selector: "codex/untitled-b", title: "Reading the drain postmortem" },
        ]),
      }),
    })),
  ).tick();

  // A LATER SCAN CATALOGUES BOTH AGAIN. One log now carries a title its harness wrote; the
  // other still carries none, and the adapter reports that as NULL rather than as a value.
  clock += 60_000;
  const fleet = new Fleet();
  fleet.beat("scan-titles", MACHINE, {
    [JOB_OUTPUT_FILES.sessions]: [
      {
        selector: "codex/untitled-a",
        host: MACHINE,
        harness: "codex",
        source_id: "untitled-a",
        title: "Retention, as the operator wrote it",
        title_provenance: "recorded",
        seen_at: new Date(clock).toISOString(),
      },
      {
        selector: "codex/untitled-b",
        host: MACHINE,
        harness: "codex",
        source_id: "untitled-b",
        title: null,
        title_provenance: null,
        seen_at: new Date(clock).toISOString(),
      },
    ],
    [JOB_OUTPUT_FILES.receipt]: {
      runId: "run_scan_titles",
      kind: "scan",
      machineId: MACHINE,
      startedAt: new Date(clock - 60_000).toISOString(),
      finishedAt: new Date(clock).toISOString(),
      closure: "completed",
      counts: { sessions: 2 },
    },
  });
  await conductor({
    engine: NO_CODE,
    store,
    coordinator: draws as unknown as Coordinator,
    jobs: fleet,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
  }).tick();

  expect(
    await db.query(
      `SELECT selector, title, title_provenance FROM sessions
        WHERE selector LIKE 'codex/%' ORDER BY selector`,
    ),
  ).toEqual([
    // THE READ TITLE WINS. The session's own word about itself outranks a guess that cost
    // money, and the provenance follows the value rather than staying behind on it.
    {
      selector: "codex/untitled-a",
      title: "Retention, as the operator wrote it",
      title_provenance: "recorded",
    },
    // AND A SCAN THAT READ NO TITLE ERASES NOTHING. "this reader found none" is not "there is
    // none": wiping it would lose what was paid for and queue the session to be paid for again.
    {
      selector: "codex/untitled-b",
      title: "Reading the drain postmortem",
      title_provenance: "inferred",
    },
  ]);
  clock -= 60_000;
});

test("a read title that landed while the run was in flight is not overwritten by its answer", async () => {
  const db = openDatabase();
  await seed(db);
  const store = openStore(db);
  const draws = new Draws(db);
  await titlingInFlight(db);
  // Between the press and the answer a scan read a title out of the log itself. The session's
  // own word outranks the guess the run is about to come back with — and the run is already
  // paid for, so the answer is recorded rather than thrown away.
  await db.run(`UPDATE sessions SET title = ?, title_provenance = 'recorded' WHERE selector = ?`, [
    "What the harness called it",
    "codex/untitled-a",
  ]);

  await wakeOn(
    store,
    draws,
    codeAnswering(() => ({
      ok: true,
      value: sessionRead({
        state: "exited",
        jobId: "job_code_title",
        finalMessage: namedAnswer([
          { selector: "codex/untitled-a", title: "What the model called it" },
          { selector: "codex/untitled-b", title: "Reading the drain postmortem" },
        ]),
      }),
    })),
  ).tick();

  expect(
    await db.query(
      `SELECT selector, title, title_provenance FROM sessions
        WHERE selector LIKE 'codex/%' ORDER BY selector`,
    ),
  ).toEqual([
    {
      selector: "codex/untitled-a",
      title: "What the harness called it",
      title_provenance: "recorded",
    },
    {
      selector: "codex/untitled-b",
      title: "Reading the drain postmortem",
      title_provenance: "inferred",
    },
  ]);
  // What was paid for stays readable even where it is not what the listing shows.
  expect(
    await db.query(`SELECT title FROM session_titles WHERE selector = 'codex/untitled-a'`),
  ).toEqual([{ title: "What the model called it" }]);
});

async function weightedCycle(stage: "challenge" | "synthesize") {
  const db = openDatabase();
  await seed(db);
  await db.run(
    `UPDATE sessions SET host = ?, live = 0, kind = 'operator', size = 4096 WHERE selector = 'omp/s1'`,
    [MACHINE],
  );
  for (const [id, runId] of [
    ["obs_00000001", "run_source_a"],
    ["obs_00000002", "run_source_b"],
  ]) {
    await db.run(
      `INSERT INTO records(id, kind, root_id, seq, parent_id, actor_kind, actor_id, run_id, title, created_at, payload)
       VALUES (?, 'observation', ?, 0, 'hyp_00000001', 'run', ?, ?, 'Source observation', ?, ?)`,
      [
        id!,
        id!,
        runId!,
        runId!,
        new Date(clock).toISOString(),
        JSON.stringify({
          claim: "The rescan loses a snapshot",
          evidence: [],
          limits: ["Only one local example"],
        }),
      ],
    );
  }
  const route = { ...ROUTE, stageRecipes: { [stage]: ROUTE.recipes[0]!.id } };
  const policy = {
    ...POLICY,
    batchSize: 1,
    review: route,
    activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0, [stage]: 1 },
  };
  await db.run(
    `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at) VALUES (?, 1, 'operator', 'weighted analysis', ?, ?)`,
    [POLICY.version, JSON.stringify(policy), new Date(clock).toISOString()],
  );
  if (stage === "synthesize") {
    for (const id of ["obs_00000001", "obs_00000002"]) {
      await db.run(
        `INSERT INTO steering(id, root_id, seq, actor_kind, actor_id, target_kind, target_id, text, recorded_at)
         VALUES (?, ?, 0, 'operator', 'operator', 'record', ?, ?, ?)`,
        [
          `stg_${id}`,
          `stg_${id}`,
          id,
          `Preserve the operator concern for ${id}`,
          new Date(clock).toISOString(),
        ],
      );
    }
  }
  const store = openReadStore(db, () => clock);
  const coordinator = governed(store, () => clock, 16);
  const fleet = new Fleet();
  const code = new ReviewCode();
  const cookbook = Object.fromEntries(route.recipes.map((recipe) => [recipe.id, recipe]));
  const launch = launchMachinery(store, {
    coordinator,
    jobs: () => fleet,
    engine: () => code,
    cookbook: async () => cookbook,
    plan: () => PLAN,
    now: () => clock,
  });
  const loop = conductor({
    store,
    coordinator,
    jobs: fleet,
    engine: code,
    machines: new Folders(),
    keys: new Keys(),
    plan: PLAN,
    now: () => clock,
    dispatchAnalysis: async (assignment, claim, owner, identity) =>
      await launch.startExplore(
        identity,
        fleet,
        code,
        {
          preset: "read-whats-new",
          machineId: MACHINE,
          profile: route.profile,
          recipes: [route.recipes[0]!.id],
        },
        PLAN,
        {
          stage: assignment.activity,
          selectors: assignment.selectors,
          brief: assignment.brief,
          claim: { id: claim.id, runId: owner, fence: claim.fence },
        },
      ),
  });
  return { db, store, coordinator, fleet, code, launch, loop };
}

test.each(["challenge", "synthesize"] as const)(
  "standing %s work keeps its reservation through both wakes and settles under its own stage",
  async (stage) => {
    const { db, coordinator, fleet, code, launch, loop } = await weightedCycle(stage);
    const first = await loop.tick();
    expect(first.requested).toHaveLength(1);
    const requested = first.requested[0]!;
    expect(requested.role).toBe(`analysis:${stage}`);
    expect(fleet.launched.map((job) => job.operationId)).toEqual([OPERATIONS.prepare]);
    expect(code.posted).toEqual([]);
    const material = { ...materialIndex(SERVED_FILE, SERVED_DIGEST), machineId: MACHINE };
    fleet.finish(requested.jobId, 0, {
      [JOB_OUTPUT_FILES.receipt]: {
        runId: `${requested.runId}_material`,
        kind: "prepare",
        machineId: MACHINE,
        startedAt: new Date(clock).toISOString(),
        finishedAt: new Date(clock).toISOString(),
        closure: "completed",
        costUsd: 0,
        tokens: 0,
        counts: {},
        material,
      },
    });
    const second = await loop.tick();
    expect(second.requested).toEqual([]);
    expect(second.settled).toEqual([]);
    expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
    expect(await launch.postPrepared(fleet, code, PLAN)).toEqual([
      { runId: requested.runId, jobId: "job_code_review" },
    ]);
    expect(code.posted[0]!.prompt).toContain(`babel.stage = ${stage}`);
    if (stage === "synthesize")
      for (const id of ["obs_00000001", "obs_00000002"])
        expect(code.posted[0]!.prompt).toContain(`Preserve the operator concern for ${id}`);
    expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
    const result =
      stage === "challenge"
        ? {
            objections: [
              {
                ref: "objection",
                hypothesis: "hyp_00000001",
                grounds: "missing-check",
                recipe: { id: ROUTE.recipes[0]!.id, version: 2 },
                claim: {
                  claim: "Check whether the restore still finds this snapshot",
                  confidence: "high",
                  impact: "moderate",
                  evidence: [],
                },
              },
            ],
          }
        : {
            consolidations: [
              {
                ref: "finding",
                observations: ["obs_00000001", "obs_00000002"],
                finding: {
                  title: "Rescan drops snapshots",
                  pattern: "Independent rescans lose their saved snapshot",
                  significance: "Restore cannot use the catalog",
                  counter_evidence_absent: true,
                },
              },
            ],
          };
    code.read = sessionRead({
      jobId: "job_code_review",
      state: "exited",
      finalMessage: `\`\`\`json\n${JSON.stringify(result)}\n\`\`\``,
    });
    const third = await loop.tick();
    expect(
      third.settled.some(
        (claim) => claim.claimId === requested.claimId && claim.outcome === "completed",
      ),
    ).toBe(true);
    const run = (
      await db.query<{ payload: string; closure: string }>(
        `SELECT payload, closure FROM runs WHERE id = ?`,
        [requested.runId],
      )
    )[0]!;
    expect(run.closure).toBe("completed");
    expect(JSON.parse(run.payload)["stage"]).toBe(stage);
    if (stage === "synthesize")
      expect(
        JSON.parse(run.payload)
          .steering.carried.map((remark: { id: string }) => remark.id)
          .sort(),
      ).toEqual(["stg_obs_00000001", "stg_obs_00000002"]);
    if (stage === "challenge") {
      expect(
        await db.query(
          `SELECT to_id, note, actor_id FROM edges WHERE kind = 'challenges' AND actor_id = ?`,
          [requested.runId],
        ),
      ).toEqual([{ to_id: "hyp_00000001", note: "missing-check", actor_id: requested.runId }]);
    } else {
      expect(
        await db.query(`SELECT kind FROM records WHERE run_id = ?`, [requested.runId]),
      ).toEqual([{ kind: "finding" }]);
    }
  },
);

test("an unusable analysis profile refuses before a claim or material job exists", async () => {
  const { db, code, fleet, loop } = await weightedCycle("challenge");
  code.checkProfile = async () => refusedByCode("engine_unavailable", "no authorized profile");
  const report = await loop.tick();
  expect(report.requested).toEqual([]);
  expect(fleet.launched).toEqual([]);
  expect(await db.query(`SELECT id FROM claims`)).toEqual([]);
});

test.each([
  "stale-fence",
  "wrong-owner",
  "wrong-kind",
  "wrong-source",
  "unoffered",
  "malformed",
] as const)(
  "analysis settlement rejects %s without writing the requested cross-run objection",
  async (boundary) => {
    const db = openDatabase();
    await seed(db);
    const { runId, jobId, claimId } = await sessionInFlight(db);
    const brief =
      boundary === "unoffered"
        ? []
        : [
            {
              id: "hyp_00000001",
              kind: boundary === "wrong-kind" ? "observation" : "hypothesis",
              runId: boundary === "wrong-source" ? "invented_source" : null,
              summary: "A held hypothesis",
              payload: { statement: "The catalog forgets archived sessions" },
              objectionTo: [],
            },
          ];
    await db.run(`UPDATE runs SET preparation = ? WHERE id = ?`, [
      JSON.stringify({
        analysis: {
          stage: boundary === "malformed" ? "ruling" : "challenge",
          selectors: ["omp/s1"],
          brief,
          claim: {
            id: claimId,
            runId: boundary === "wrong-owner" ? "cyc_other" : SEEDED_CYCLE,
            fence: boundary === "stale-fence" ? 2 : 1,
          },
        },
      }),
      runId,
    ]);
    const coordinator = governed(
      openReadStore(db, () => clock),
      () => clock,
      16,
    );
    const draws = new Draws(db);
    const result = {
      objections: [
        {
          ref: "o",
          hypothesis: "hyp_00000001",
          grounds: "missing-check",
          recipe: { id: "r", version: 1 },
          claim: {
            claim: "A check is missing",
            confidence: "high",
            impact: "moderate",
            evidence: [],
          },
        },
      ],
    };
    const code = codeAnswering(() => ({
      ok: true,
      value: sessionRead({
        jobId,
        state: "exited",
        finalMessage: `\`\`\`json\n${JSON.stringify(result)}\n\`\`\``,
      }),
    }));
    await conductor({
      store: openReadStore(db, () => clock),
      coordinator: { ...coordinator, policy: draws.policy.bind(draws) } as unknown as Coordinator,
      jobs: new Fleet(),
      engine: code,
      machines: new Folders(),
      keys: new Keys(),
      plan: PLAN,
      now: () => clock,
    }).tick();
    expect(await db.query(`SELECT id FROM records WHERE run_id = ?`, [runId])).toEqual([]);
    expect(
      await db.query(`SELECT id FROM edges WHERE kind = 'challenges' AND actor_id = ?`, [runId]),
    ).toEqual([]);
    expect(await db.query(`SELECT closure FROM runs WHERE id = ?`, [runId])).toEqual([
      { closure: "failed" },
    ]);
  },
);

test("an interrupted native analysis posting retains its reservation and reconciles the accepted job", async () => {
  const { db, coordinator, code, fleet, launch, loop } = await weightedCycle("challenge");
  const execute = fleet.execute.bind(fleet);
  fleet.execute = (request) => {
    execute(request);
    throw new Error("native response transport interrupted");
  };
  const report = await loop.tick();
  expect(report.requested).toEqual([]);
  expect(code.posted).toEqual([]);
  const jobId = fleet.launched[0]!.jobId;
  expect(await db.query(`SELECT closure, job_id FROM runs ORDER BY job_id`)).toEqual([
    { closure: null, job_id: null },
    { closure: null, job_id: jobId },
  ]);
  expect(await db.query(`SELECT outcome, actual_cost FROM claims`)).toEqual([
    { outcome: null, actual_cost: null },
  ]);
  expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
  const materialRun = (
    await db.query<{ id: string }>(`SELECT id FROM runs WHERE job_id = ?`, [jobId])
  )[0]!.id;
  fleet.finish(jobId, 0, {
    [JOB_OUTPUT_FILES.receipt]: {
      runId: materialRun,
      kind: "prepare",
      machineId: MACHINE,
      startedAt: new Date(clock).toISOString(),
      finishedAt: new Date(clock).toISOString(),
      closure: "completed",
      costUsd: 0,
      tokens: 0,
      counts: {},
      material: { ...materialIndex(SERVED_FILE, SERVED_DIGEST), machineId: MACHINE },
    },
  });
  await loop.tick();
  expect(await launch.postPrepared(fleet, code, PLAN)).toEqual([
    {
      runId: (
        await db.query<{ id: string }>(`SELECT id FROM runs WHERE prepare_job_id = ?`, [jobId])
      )[0]!.id,
      jobId: "job_code_review",
    },
  ]);
  expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
});

test("a terminal Code job whose cancellation failed accounts the unchanged preparation grant exactly once", async () => {
  const { db, coordinator, fleet, code, launch, loop } = await weightedCycle("challenge");
  const first = await loop.tick();
  const requested = first.requested[0]!;
  fleet.finish(requested.jobId, 0, {
    [JOB_OUTPUT_FILES.receipt]: {
      runId: `${requested.runId}_material`,
      kind: "prepare",
      machineId: MACHINE,
      startedAt: new Date(clock).toISOString(),
      finishedAt: new Date(clock).toISOString(),
      closure: "completed",
      costUsd: 0,
      tokens: 0,
      counts: {},
      material: { ...materialIndex(SERVED_FILE, SERVED_DIGEST), machineId: MACHINE },
    },
  });
  await loop.tick();
  const post = code.runSession.bind(code);
  code.runSession = async (request) => {
    const answered = await post(request);
    await db.run(`UPDATE claims SET expires_at = ? WHERE id = ?`, [
      new Date(clock - 1).toISOString(),
      requested.claimId,
    ]);
    return answered;
  };
  code.cancelSession = async () => refusedByCode("engine_unavailable", "cancellation unavailable");
  expect((await launch.postPrepared(fleet, code, PLAN))[0]).toHaveProperty("refused");
  expect(
    await db.query(`SELECT job_id, finished_at FROM claims WHERE id = ?`, [requested.claimId]),
  ).toEqual([{ job_id: requested.jobId, finished_at: null }]);
  const read = code.readSession.bind(code);
  code.readSession = async () => refusedByCode("engine_unavailable", "transport interrupted");
  await loop.tick();
  await loop.tick();
  expect(
    await db.query(`SELECT finished_at FROM claims WHERE id = ?`, [requested.claimId]),
  ).toEqual([{ finished_at: null }]);
  expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
  code.readSession = read;
  code.read = sessionRead({
    jobId: "job_code_review",
    state: "exited",
    usage: { input: 100, output: 40, cacheRead: 0, cacheWrite: 0, cost: 0.12 },
    finalMessage: "```json\n{}\n```",
  });
  await loop.tick();
  expect(
    await db.query(`SELECT job_id, outcome, actual_cost FROM claims WHERE id = ?`, [
      requested.claimId,
    ]),
  ).toEqual([{ job_id: "job_code_review", outcome: "failed", actual_cost: 0.12 }]);
  expect(await db.query(`SELECT id FROM records WHERE run_id = ?`, [requested.runId])).toEqual([]);
  expect((await coordinator.spend(clock)).total).toBeCloseTo(0.12, 8);
});

test.each(["missing", "known", "retained"] as const)(
  "the analysis reaper recovers only a provably unposted expired claim: %s",
  async (boundary) => {
    const { db, coordinator, fleet, loop } = await weightedCycle("challenge");
    await db.run(`UPDATE policies SET payload = json_set(payload, '$.activityWeights', json(?))`, [
      JSON.stringify({ review: 0, explore: 0, challenge: 0, synthesize: 0 }),
    ]);
    const old = new Date(clock - 24 * 60 * 60 * 1000).toISOString();
    await db.run(
      `INSERT INTO claims(id, record_id, role, lane, policy_version, run_id, job_id, fence,
                          reserved_cost, granted_at, expires_at)
       VALUES ('asg_unposted', 'hyp_00000001', 'analysis:challenge', 'exploration', ?, 'cyc_lost',
               'job_unposted', 1, 0.05, ?, ?)`,
      [POLICY.version, old, old],
    );
    if (boundary === "known") fleet.running("job_unposted", MACHINE, OPERATIONS.prepare);
    if (boundary === "retained") {
      await db.run(
        `INSERT INTO runs(id, kind, machine_id, container_id, prepare_job_id, started_at, records, payload)
         VALUES ('run_uncertain', ?, ?, 'ctr_union', 'job_unposted', ?, 0, ?)`,
        [OPERATIONS.explore, MACHINE, old, JSON.stringify({ closure: null, posting: true })],
      );
    }
    const report = await loop.tick();
    expect(
      await db.query(`SELECT outcome, actual_cost FROM claims WHERE id = 'asg_unposted'`),
    ).toEqual(
      boundary === "missing"
        ? [{ outcome: "failed", actual_cost: 0 }]
        : [{ outcome: null, actual_cost: null }],
    );
    expect(report.settled.filter((claim) => claim.claimId === "asg_unposted")).toHaveLength(
      boundary === "missing" ? 1 : 0,
    );
    expect((await coordinator.open(clock)).total).toBe(boundary === "missing" ? 0 : 1);
  },
);

test("a retained analysis preparation remains occupied through repeated transport silence", async () => {
  const { db, coordinator, fleet, loop } = await weightedCycle("challenge");
  const first = await loop.tick();
  const requested = first.requested[0]!;
  fleet.silent.add(requested.jobId);
  await db.run(`UPDATE policies SET payload = json_set(payload, '$.activityWeights', json(?))`, [
    JSON.stringify({ review: 0, explore: 0, challenge: 0, synthesize: 0 }),
  ]);
  await db.run(`UPDATE claims SET expires_at = ?`, [new Date(clock - 1).toISOString()]);
  await loop.tick();
  await loop.tick();
  expect(
    await db.query(`SELECT finished_at FROM claims WHERE id = ?`, [requested.claimId]),
  ).toEqual([{ finished_at: null }]);
  expect((await coordinator.open(clock)).byMachine[MACHINE]).toBe(1);
});

test.each(["hardened", "in-realm"] as const)(
  "a typed native admission refusal releases failed/0 and retries only after cooldown: %s",
  async (loader) => {
    const { db, coordinator, fleet, loop } = await weightedCycle("challenge");
    const execute = fleet.execute.bind(fleet);
    const status = fleet.status.bind(fleet);
    const refusal = (method: string, token: string) =>
      loader === "hardened"
        ? new HostCallError(method, token)
        : Object.assign(new Error(token), { name: "ServiceError", code: "forbidden" });
    fleet.execute = () => {
      throw refusal("jobs.execute", "installation_changed");
    };
    fleet.status = () => {
      throw refusal("jobs.status", "job_not_started");
    };
    const first = await loop.tick();
    expect(first.settled).toMatchObject([{ outcome: "failed", cost: 0, refused: null }]);
    expect(fleet.launched).toEqual([]);
    expect(await db.query(`SELECT closure FROM runs ORDER BY id`)).toEqual([
      { closure: "failed" },
      { closure: "failed" },
    ]);
    expect(await db.query(`SELECT outcome, actual_cost FROM claims`)).toEqual([
      { outcome: "failed", actual_cost: 0 },
    ]);
    expect((await coordinator.open(clock)).total).toBe(0);
    expect((await coordinator.spend(clock)).total).toBe(0);
    expect((await loop.tick()).requested).toEqual([]);
    expect(await db.query(`SELECT COUNT(*) AS n FROM claims`)).toEqual([{ n: 1n }]);
    clock += POLICY.cooldownSeconds * 1000 + 1;
    fleet.execute = execute;
    fleet.status = status;
    const retry = await loop.tick();
    expect(retry.requested).toHaveLength(1);
    expect(retry.requested[0]!.claimId).not.toBe(first.settled[0]!.claimId);
    expect((await coordinator.open(clock)).total).toBe(1);
  },
);

test.each([
  "transport-absent",
  "untyped-refusal-absent",
  "refusal-unreadable",
  "host-error-absent",
  "refusal-known",
] as const)(
  "native analysis uncertainty stays reserved across later cycles: %s",
  async (boundary) => {
    const { db, coordinator, fleet, loop } = await weightedCycle("challenge");
    fleet.execute = (request) => {
      if (boundary === "refusal-known")
        fleet.running(request.jobId, request.machineId, request.operationId);
      if (boundary === "transport-absent") throw new Error("response transport interrupted");
      if (boundary === "untyped-refusal-absent") throw new Error("installation_changed");
      throw new HostCallError(
        "jobs.execute",
        boundary === "host-error-absent" ? "dispatch unavailable" : "installation_changed",
      );
    };
    const jobs: JobsSlice = fleet;
    if (boundary !== "refusal-known") {
      jobs.status = () => {
        if (boundary === "refusal-unreadable") throw new Error("status transport interrupted");
        throw new HostCallError("jobs.status", "job_not_started");
      };
    }
    const first = await loop.tick();
    expect(first.settled).toEqual([]);
    await loop.tick();
    await loop.tick();
    expect(await db.query(`SELECT closure FROM runs ORDER BY id`)).toEqual([
      { closure: null },
      { closure: null },
    ]);
    expect(await db.query(`SELECT outcome, actual_cost FROM claims`)).toEqual([
      { outcome: null, actual_cost: null },
    ]);
    expect((await coordinator.open(clock)).total).toBe(1);
  },
);

test("retained analysis claims do not hide a real orphan behind the reaper work bound", async () => {
  const { db, loop } = await weightedCycle("challenge");
  await db.run(`UPDATE policies SET payload = json_set(payload, '$.activityWeights', json(?))`, [
    JSON.stringify({ review: 0, explore: 0, challenge: 0, synthesize: 0 }),
  ]);
  const old = new Date(clock - 48 * 60 * 60 * 1000).toISOString();
  for (let index = 0; index < 128; index += 1) {
    const id = `retained_${index}`;
    await db.run(
      `INSERT INTO claims(id, record_id, role, lane, policy_version, run_id, job_id, fence,
                          reserved_cost, granted_at, expires_at)
       VALUES (?, 'hyp_00000001', 'analysis:challenge', 'exploration', ?, 'cyc_old', ?, 1, 0.05, ?, ?)`,
      [id, POLICY.version, id, old, old],
    );
    await db.run(
      `INSERT INTO runs(id, kind, machine_id, job_id, started_at, records, unreadable, payload)
       VALUES (?, ?, ?, ?, ?, 0, 2, '{}')`,
      [id, OPERATIONS.prepare, MACHINE, id, old],
    );
  }
  const newer = new Date(clock - 24 * 60 * 60 * 1000).toISOString();
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, run_id, fence,
                        reserved_cost, granted_at, expires_at)
     VALUES ('orphan', 'hyp_00000001', 'reception', 'coverage', ?, 'cyc_old', 1, 0.05, ?, ?)`,
    [POLICY.version, newer, newer],
  );
  const report = await loop.tick();
  expect(report.settled).toMatchObject([
    { claimId: "orphan", outcome: "abandoned", cost: 0.05, refused: null },
  ]);
  expect(await db.query(`SELECT COUNT(*) AS n FROM claims WHERE finished_at IS NULL`)).toEqual([
    { n: 128n },
  ]);
  expect(await db.query(`SELECT outcome, actual_cost FROM claims WHERE id = 'orphan'`)).toEqual([
    { outcome: "abandoned", actual_cost: 0.05 },
  ]);
  expect(report.notes.some((note) => note.includes("dead claims were released"))).toBe(false);
});

async function catalogDeployment(sourceMachineId = "map-source") {
  const db = openDatabase();
  const store = openStore(db);
  const fleet = new Fleet();
  const jobs: JobsSlice = fleet;
  const draws = new Draws(db);
  draws.review = ROUTE;
  const route = TranscriptMapPolicySchema.parse({
    sourceMachineId,
    executorMachineId: MACHINE,
    profile: ROUTE.profile,
    dailyCost: 0,
    generateRecipe: ROUTE.recipes[0]!.id,
    reviewRecipe: ROUTE.recipes[0]!.id,
    recipes: ROUTE.recipes,
    segmentation: { leafBytes: 1024, directBytes: 0 },
  });
  const { recipes: _recipes, ...mapping } = route;
  draws.mapping = mapping;
  const describe = fleet.describe.bind(fleet);
  const nativeBinding: TranscriptMapServiceBinding = {
    machineId: route.sourceMachineId,
    serviceId: RECALL_SERVICE_ID,
    revision: "source-revision-1",
    policySha256: "a".repeat(64),
  };
  const bindingState = { digest: "b".repeat(64), available: true };
  fleet.describe = (args) => {
    const ready = describe(args);
    return {
      ...ready,
      operations: {
        ...ready.operations,
        [OPERATIONS.mapCatalog]: {
          ready: true,
          reason: null,
          resourceBindingDigest: bindingState.digest,
          ...(bindingState.available
            ? { serviceBindings: { [RECALL_SERVICE_ID]: { ...nativeBinding } } }
            : {}),
        },
      },
    };
  };
  const codeCalls: string[] = [];
  const unexpectedCode = async (): Promise<never> => {
    codeCalls.push("Code");
    throw new Error("free catalog must not call Code");
  };
  const engine: CodeEngine = {
    profiles: unexpectedCode,
    checkProfile: unexpectedCode,
    runSession: unexpectedCode,
    readSession: unexpectedCode,
    cancelSession: unexpectedCode,
  };
  const loop = () =>
    conductor({
      store,
      coordinator: draws as unknown as Coordinator,
      jobs,
      engine,
      machines: new Folders(),
      keys: new Keys(),
      plan: PLAN,
      catalogPlan: UNMETERED_PLAN,
      now: () => clock,
    });
  const tick = (machineId = MACHINE) =>
    loop().tickCatalog(
      machineId,
      draws.mapping === undefined
        ? undefined
        : {
            route: draws.mapping,
            serviceBinding: { ...nativeBinding },
            resourceBindingDigest: bindingState.digest,
          },
    );
  const ordinaryTick = () => loop().tick();
  const digest = `sha256:${"a".repeat(64)}`;
  const context = {
    digest,
    policyDigest: digest,
    classId: "private",
    ceiling: 2,
    eligibleCaptures: 3,
    observedAt: new Date(clock).toISOString(),
  };
  const captures = Array.from({ length: 3 }, (_, n) => {
    const identity = {
      host: "synthetic",
      harness: "omp" as const,
      session: `synthetic-${n}`,
      snapshot: String(n + 1).repeat(64),
      path: `/synthetic/${n}.jsonl`,
      capturedAt: new Date(clock + n).toISOString(),
    };
    return { id: transcriptMapCaptureId(identity), ...identity };
  });
  const entries = captures.map((capture) => ({
    capture,
    access: { captureId: capture.id, contextDigest: context.digest, sensitivity: 2 },
  }));
  const trees = await Promise.all(
    captures.map(async (capture) => {
      const text = `${JSON.stringify({ role: "user", text: "a".repeat(700) })}\n${JSON.stringify({ role: "assistant", text: "b".repeat(700) })}\n`;
      const bytes = Buffer.from(text);
      const sha = (data: Uint8Array) => `sha256:${createHash("sha256").update(data).digest("hex")}`;
      return buildTranscriptMap({
        capture,
        captureDigest: sha(bytes),
        sourceDigest: sha(bytes),
        segmentation: route.segmentation,
        async replay(sink) {
          sink.write(bytes);
          await sink.close();
        },
        rangeDigest: async (offset, length) => sha(bytes.subarray(offset, offset + length)),
      });
    }),
  );
  function finish(
    mapping: Omit<
      Extract<TranscriptMapJobReceipt, { kind: "catalog" }>,
      "sourceMachineId" | "executorMachineId"
    > &
      Partial<Pick<TranscriptMapJobReceipt, "sourceMachineId" | "executorMachineId">>,
  ) {
    const launch = fleet.launched.at(-1)!;
    const input = TranscriptMapCatalogInputSchema.parse(
      JSON.parse(String(launch.input[INPUT_FIELD])),
    );
    fleet.finish(launch.jobId, 0, {
      [JOB_OUTPUT_FILES.receipt]: {
        runId: input.runId,
        machineId: MACHINE,
        kind: "mapCatalog",
        startedAt: new Date(clock).toISOString(),
        finishedAt: new Date(clock).toISOString(),
        closure: "completed",
        counts: {},
        mapping: {
          sourceMachineId: input.sourceMachineId,
          executorMachineId: input.executorMachineId,
          ...mapping,
        },
      },
    });
  }
  return {
    db,
    store,
    fleet,
    jobs,
    draws,
    route,
    nativeBinding,
    bindingState,
    context,
    captures,
    entries,
    trees,
    finish,
    tick,
    ordinaryTick,
    codeCalls,
  };
}

test("catalog refuses a native binding to another source before any job is posted", async () => {
  const f = await catalogDeployment();
  f.nativeBinding.machineId = "wrong-owner";
  await f.tick();
  expect(f.fleet.launched).toEqual([]);
  expect(await f.db.query(`SELECT id FROM runs WHERE kind=?`, [OPERATIONS.mapCatalog])).toEqual([]);
  expect(f.codeCalls).toEqual([]);
});

test("catalog receipts cannot substitute either the source owner or executor", async () => {
  for (const field of ["sourceMachineId", "executorMachineId"] as const) {
    const f = await catalogDeployment();
    await f.tick();
    f.finish({
      kind: "catalog",
      context: f.context,
      entries: f.entries,
      nextCursor: null,
      [field]: "wrong-machine",
    });
    await f.tick();
    expect(await f.db.query(`SELECT id FROM transcript_map_captures`)).toEqual([]);
    expect(f.fleet.launched).toHaveLength(1);
    expect(f.codeCalls).toEqual([]);
  }
});

test("source binding replacement fences receipts in both layouts, even with an unchanged local policy digest", async () => {
  for (const sourceMachineId of ["map-source", MACHINE]) {
    const f = await catalogDeployment(sourceMachineId);
    await f.tick();
    f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: "old-cursor" });
    f.nativeBinding.revision = "source-revision-2";
    if (sourceMachineId !== MACHINE) f.bindingState.digest = "c".repeat(64);
    await f.tick();
    expect(await f.db.query(`SELECT id FROM transcript_map_captures`)).toEqual([]);
    const replacement = TranscriptMapCatalogInputSchema.parse(
      JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
    );
    expect(replacement.request).toEqual({ kind: "map-inventory", maxCaptures: 64 });
    expect(f.codeCalls).toEqual([]);
  }
});

test("same-machine catalog preserves explicit source identity and remains free", async () => {
  const f = await catalogDeployment(MACHINE);
  await f.tick();
  f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: null });
  await f.tick();
  expect((await transcriptMaps(f.store).catalogState(MACHINE)).context).toEqual(f.context);
  const plan = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(plan).toMatchObject({
    sourceMachineId: MACHINE,
    executorMachineId: MACHINE,
    request: { kind: "map-plan", capture: f.captures[0] },
  });
  expect(f.codeCalls).toEqual([]);
});

test("free catalog resumes bounded inventory and plan pages without Recall or Code", async () => {
  const f = await catalogDeployment();
  await Promise.all([f.tick(), f.tick()]);
  expect(f.fleet.launched).toHaveLength(1);
  f.finish({
    kind: "catalog",
    context: f.context,
    entries: f.entries.slice(0, 2),
    nextCursor: "page-two",
  });
  await f.tick();
  const second = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched[1]!.input[INPUT_FIELD])),
  );
  expect(second.request).toMatchObject({ kind: "map-inventory", cursor: "page-two" });
  f.finish({ kind: "catalog", context: f.context, entries: f.entries.slice(2), nextCursor: null });
  await f.tick();
  for (const [index, tree] of f.trees.entries()) {
    const input = TranscriptMapCatalogInputSchema.parse(
      JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
    );
    expect(input.request).toMatchObject({
      kind: "map-plan",
      capture: f.captures[index],
      offset: 0,
    });
    f.finish({
      kind: "catalog",
      context: f.context,
      entries: [],
      nextCursor: null,
      access: f.entries[index]!.access,
      plan: { header: tree.header, nodes: tree.nodes.slice(0, 1), offset: 0, nextOffset: 1 },
    });
    await f.tick();
    const resumed = TranscriptMapCatalogInputSchema.parse(
      JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
    );
    expect(resumed.request).toMatchObject({
      kind: "map-plan",
      capture: f.captures[index],
      offset: 1,
    });
    f.finish({
      kind: "catalog",
      context: f.context,
      entries: [],
      nextCursor: null,
      access: f.entries[index]!.access,
      plan: { header: tree.header, nodes: tree.nodes.slice(1), offset: 1, nextOffset: null },
    });
    await f.tick();
  }
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_plans WHERE complete=1`)).toEqual([
    { n: 3n },
  ]);
  expect(
    await f.db.query(`SELECT count(*) n FROM transcript_map_work WHERE state='queued'`),
  ).toEqual([{ n: 6n }]);
  expect(await f.db.query(`SELECT count(*) n FROM claims`)).toEqual([{ n: 0n }]);
  expect(f.codeCalls).toEqual([]);
  expect(f.fleet.launched).toHaveLength(8);
});

test("catalog posting interruption recovers the retained ID and never duplicates a known job", async () => {
  const f = await catalogDeployment();
  const execute = f.fleet.execute.bind(f.fleet);
  const status = f.fleet.status.bind(f.fleet);
  const attempts: string[] = [];
  f.fleet.execute = (launch) => {
    attempts.push(launch.jobId);
    if (attempts.length === 1) throw new Error("interrupted before native execute");
    execute(launch);
    throw new Error("lost native acknowledgement");
  };
  f.fleet.status = (node) => {
    if (!f.fleet.jobs.has(node.jobId)) throw new HostCallError("jobs.status", "job_not_started");
    return status(node);
  };
  await f.tick();
  await f.tick();
  await f.tick();
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toBe(attempts[0]);
  expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
    { n: 1n },
  ]);
  f.finish({
    kind: "catalog",
    context: { ...f.context, eligibleCaptures: 0 },
    entries: [],
    nextCursor: null,
  });
  await f.tick();
  expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
    { n: 0n },
  ]);
  expect(f.codeCalls).toEqual([]);
});

test("terminally refused capture advances the sweep and is retried only on a later bounded sweep", async () => {
  const f = await catalogDeployment();
  await f.tick();
  f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: null });
  await f.tick();
  const refused = f.fleet.launched.at(-1)!;
  const status = f.fleet.status.bind(f.fleet);
  const decision = "authority_or_consent_refused";
  f.fleet.status = (node) =>
    node.jobId === refused.jobId
      ? {
          ...status(node),
          state: "refused",
          result: null,
          authority: { decision: { refusal: decision } },
        }
      : status(node);
  const notes = await f.tick();
  const failure = (
    await f.db.query<{ closure: string; reason: string; gap: string }>(
      `SELECT closure,json_extract(payload,'$.reason') reason,
              json_extract(preparation,'$.progress.gap') gap FROM runs WHERE job_id=?`,
      [refused.jobId],
    )
  )[0]!;
  expect(failure.closure).toBe("failed");
  expect(failure.reason).toContain("refused");
  expect(failure.reason).toContain(decision);
  expect(failure.gap).toContain(decision);
  expect(notes.some((note) => note.includes(decision))).toBe(true);
  expect(f.fleet.launched).toHaveLength(2);
  clock += POLICY.cadenceSeconds * 1000;
  await f.tick();
  const next = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(next.request).toMatchObject({ kind: "map-plan", capture: f.captures[1] });
  for (const index of [1, 2]) {
    const tree = f.trees[index]!;
    f.finish({
      kind: "catalog",
      context: f.context,
      entries: [],
      nextCursor: null,
      access: f.entries[index]!.access,
      plan: { header: tree.header, nodes: tree.nodes, offset: 0, nextOffset: null },
    });
    await f.tick();
  }
  f.finish({
    kind: "catalog",
    context: { ...f.context, observedAt: new Date(clock).toISOString() },
    entries: f.entries,
    nextCursor: null,
  });
  await f.tick();
  const retry = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(retry.request).toMatchObject({ kind: "map-plan", capture: f.captures[0] });
  expect(
    await f.db.query(
      `SELECT count(*) n FROM runs WHERE json_extract(preparation,'$.progress.gap') IS NOT NULL`,
    ),
  ).toEqual([{ n: 1n }]);
  expect(f.codeCalls).toEqual([]);
});

test("a completed catalog with unreadable sealed output retries that output without a refusal or replacement job", async () => {
  const f = await catalogDeployment();
  await f.tick();
  const launch = f.fleet.launched[0]!;
  f.finish({
    kind: "catalog",
    context: f.context,
    entries: f.entries,
    nextCursor: null,
  });
  const output = f.fleet.output.bind(f.fleet);
  f.fleet.output = () => {
    throw new Error("sealed output transport interrupted");
  };
  await f.tick();
  clock += POLICY.cadenceSeconds * 1000;
  await f.tick();
  expect(f.fleet.launched).toHaveLength(1);
  expect(
    await f.db.query(
      `SELECT closure,json_extract(preparation,'$.progress') progress FROM runs WHERE job_id=?`,
      [launch.jobId],
    ),
  ).toEqual([{ closure: null, progress: null }]);
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_captures`)).toEqual([{ n: 0n }]);

  f.fleet.output = output;
  await f.tick();
  expect(
    await f.db.query(
      `SELECT closure,json_extract(preparation,'$.progress.gap') gap FROM runs WHERE job_id=?`,
      [launch.jobId],
    ),
  ).toEqual([{ closure: "completed", gap: null }]);
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_captures`)).toEqual([{ n: 3n }]);
  expect(f.fleet.launched).toHaveLength(2);
  const next = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(next.request.kind).toBe("map-plan");
  expect(f.codeCalls).toEqual([]);
});

test("stale native catalog receipt cannot overwrite a newer authorization context", async () => {
  const f = await catalogDeployment();
  await f.tick();
  await transcriptMaps(f.store).recordAccess({
    machineId: f.route.sourceMachineId,
    context: {
      ...f.context,
      digest: `sha256:${"b".repeat(64)}`,
      ceiling: 0,
      eligibleCaptures: 0,
      observedAt: new Date(clock + 1000).toISOString(),
    },
    entries: [],
    now: new Date(clock + 1000).toISOString(),
  });
  f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: null });
  await f.tick();
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_captures`)).toEqual([{ n: 0n }]);
  expect(
    await f.db.query(
      `SELECT count(*) n FROM runs WHERE json_extract(preparation,'$.progress.gap') IS NOT NULL`,
    ),
  ).toEqual([{ n: 1n }]);
  expect(f.fleet.launched).toHaveLength(1);
});

test("disabled and unconfigured policies never start free catalog work", async () => {
  const f = await catalogDeployment();
  f.draws.enabled = false;
  await f.tick();
  f.draws.enabled = true;
  f.draws.mapping = undefined;
  await f.tick();
  expect(f.fleet.launched).toEqual([]);
  expect(f.codeCalls).toEqual([]);
});

test("ordinary cycles observe catalog work but cannot post or retry its native job", async () => {
  const f = await catalogDeployment();
  await f.ordinaryTick();
  expect(f.fleet.launched).toEqual([]);
  const execute = f.fleet.execute.bind(f.fleet);
  let attempted = 0;
  f.fleet.execute = (launch) => {
    attempted += 1;
    if (attempted === 1) throw new Error("catalog post interrupted before arrival");
    return execute(launch);
  };
  const status = f.fleet.status.bind(f.fleet);
  f.fleet.status = (node) => {
    if (!f.fleet.jobs.has(node.jobId)) throw new HostCallError("jobs.status", "job_not_started");
    return status(node);
  };
  await f.tick();
  const retained = await f.db.query(`SELECT id,job_id,closure FROM runs WHERE kind=?`, [
    OPERATIONS.mapCatalog,
  ]);
  await f.ordinaryTick();
  expect(attempted).toBe(1);
  expect(
    await f.db.query(`SELECT id,job_id,closure FROM runs WHERE kind=?`, [OPERATIONS.mapCatalog]),
  ).toEqual(retained);
  await f.tick();
  expect(attempted).toBe(2);
  expect(f.fleet.launched).toHaveLength(1);
});

test("catalog admission cannot move with policy to another machine or reconcile paid sessions", async () => {
  const f = await catalogDeployment();
  await seed(f.db);
  await sessionInFlight(f.db);
  await f.tick();
  f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: null });
  const other = "another-machine";
  f.draws.mapping = { ...f.draws.mapping!, executorMachineId: other };
  await f.tick();
  expect(f.fleet.launched).toHaveLength(1);
  expect(f.codeCalls).toEqual([]);
  expect(f.fleet.scheduled).toEqual([]);
  expect(f.draws.finished).toEqual([]);

  await f.tick(other);
  expect(f.fleet.launched).toHaveLength(2);
  expect(f.fleet.launched[1]!.machineId).toBe(other);
  expect(f.codeCalls).toEqual([]);
  expect(f.fleet.scheduled).toEqual([]);
});

test("a late duplicate catalog projector cannot rewind a newer page's context or cursor", async () => {
  const f = await catalogDeployment();
  await f.tick();
  f.finish({
    kind: "catalog",
    context: f.context,
    entries: f.entries.slice(0, 2),
    nextCursor: "page-two",
  });
  let startBoth!: () => void;
  const bothReading = new Promise<void>((resolve) => {
    startBoth = resolve;
  });
  let arrived!: () => void;
  const lateWrite = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  let resume!: () => void;
  const heldWrite = new Promise<void>((resolve) => {
    resume = resolve;
  });
  let readers = 0;
  let writers = 0;
  const query = f.db.query.bind(f.db);
  const batch = f.db.batch.bind(f.db);
  f.db.query = async <Row extends SqlRow = SqlRow>(sql: string, params?: readonly SqlParam[]) => {
    const rows = await query<Row>(sql, params);
    if (sql.startsWith("SELECT id,preparation,payload,closure FROM runs") && readers < 2) {
      readers++;
      if (readers === 2) startBoth();
      await bothReading;
    }
    return rows;
  };
  f.db.batch = async (statements) => {
    if (
      statements.some(
        (statement) =>
          statement.sql.startsWith("INSERT INTO transcript_map_contexts") &&
          statement.params?.[6] === "page-two",
      )
    ) {
      writers++;
      if (writers === 2) {
        arrived();
        await heldWrite;
      }
    }
    return batch(statements);
  };
  const first = f.tick();
  const duplicate = f.tick();
  try {
    await lateWrite;
    await Promise.race([first, duplicate]);
    clock += 1000;
    const newer = { ...f.context, observedAt: new Date(clock).toISOString() };
    f.finish({ kind: "catalog", context: newer, entries: f.entries.slice(2), nextCursor: null });
    await f.tick();
    resume();
    await Promise.all([first, duplicate]);
    const state = await transcriptMaps(f.store).catalogState(f.route.sourceMachineId);
    expect(state.context).toEqual(newer);
    expect(state.nextCursor).toBeNull();
    expect(state.completedAt).toBe(new Date(clock).toISOString());
    expect(await f.db.query(`SELECT count(*) n FROM transcript_map_captures`)).toEqual([{ n: 3n }]);
  } finally {
    resume();
    await Promise.all([first, duplicate]);
  }
});

test("late native ingestion cannot reopen an applied catalog receipt after the next page advances", async () => {
  const f = await catalogDeployment();
  await f.tick();
  const first = f.fleet.launched[0]!;
  const input = TranscriptMapCatalogInputSchema.parse(JSON.parse(String(first.input[INPUT_FIELD])));
  f.finish({
    kind: "catalog",
    context: f.context,
    entries: f.entries.slice(0, 2),
    nextCursor: "page-two",
  });
  const sealed = f.fleet.status({ jobId: first.jobId }).result!.outputs;
  await f.tick();
  clock += 1000;
  const newer = { ...f.context, observedAt: new Date(clock).toISOString() };
  f.finish({ kind: "catalog", context: newer, entries: f.entries.slice(2), nextCursor: null });
  await f.tick();
  const currentPlan = f.fleet.launched.at(-1)!;
  await ingestOutputs(f.store, f.jobs, {
    runId: input.runId,
    jobId: first.jobId,
    machineId: MACHINE,
    operationId: OPERATIONS.mapCatalog,
    outputs: sealed,
    closure: "completed",
  });
  await f.tick();
  const state = await transcriptMaps(f.store).catalogState(f.route.sourceMachineId);
  expect(state.context).toEqual(newer);
  expect(state.nextCursor).toBeNull();
  expect(f.fleet.launched).toHaveLength(3);
  expect(f.fleet.launched.at(-1)!.jobId).toBe(currentPlan.jobId);
});

test.each(["captures", "nodes"] as const)(
  "interrupted %s projection replays the same sealed receipt without another native read",
  async (boundary) => {
    const f = await catalogDeployment();
    await f.tick();
    if (boundary === "nodes") {
      f.finish({
        kind: "catalog",
        context: { ...f.context, eligibleCaptures: 1 },
        entries: f.entries.slice(0, 1),
        nextCursor: null,
      });
      await f.tick();
      const tree = f.trees[0]!;
      f.finish({
        kind: "catalog",
        context: { ...f.context, eligibleCaptures: 1 },
        entries: [],
        nextCursor: null,
        access: f.entries[0]!.access,
        plan: { header: tree.header, nodes: tree.nodes, offset: 0, nextOffset: null },
      });
    } else {
      f.finish({ kind: "catalog", context: f.context, entries: f.entries, nextCursor: null });
    }
    const launched = f.fleet.launched.length;
    const batch = f.db.batch.bind(f.db);
    let interrupted = false;
    f.db.batch = async (statements) => {
      if (
        !interrupted &&
        statements.some((statement) =>
          statement.sql.startsWith(`INSERT OR IGNORE INTO transcript_map_${boundary}`),
        )
      ) {
        interrupted = true;
        await batch(statements.slice(0, boundary === "captures" ? 2 : 1));
        throw new Error("database transport interrupted after a committed projection chunk");
      }
      return batch(statements);
    };
    await f.tick();
    expect(f.fleet.launched).toHaveLength(launched);
    expect(await f.db.query(`SELECT count(*) n FROM transcript_map_${boundary}`)).toEqual([
      { n: 1n },
    ]);
    await f.tick();
    expect(await f.db.query(`SELECT count(*) n FROM transcript_map_${boundary}`)).toEqual([
      { n: 3n },
    ]);
    if (boundary === "nodes") {
      expect(f.fleet.launched).toHaveLength(launched);
      expect(
        await f.db.query(`SELECT count(*) n FROM transcript_map_work WHERE state='queued'`),
      ).toEqual([{ n: 2n }]);
    } else {
      expect(f.fleet.launched).toHaveLength(launched + 1);
      const next = TranscriptMapCatalogInputSchema.parse(
        JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
      );
      expect(next.request.kind).toBe("map-plan");
    }
    expect(
      await f.db.query(
        `SELECT count(*) n FROM runs WHERE json_extract(preparation,'$.progress.gap') IS NOT NULL`,
      ),
    ).toEqual([{ n: 0n }]);
    expect(f.codeCalls).toEqual([]);
  },
);

test("editing an unrelated ordinary recipe does not restart free inventory pagination", async () => {
  const f = await catalogDeployment();
  await f.tick();
  f.finish({
    kind: "catalog",
    context: f.context,
    entries: f.entries.slice(0, 2),
    nextCursor: "page-two",
  });
  f.draws.review = {
    ...ROUTE,
    recipes: [
      ...ROUTE.recipes,
      { id: "ordinary-exploration", version: 2, body: "An unrelated ordinary method." },
    ],
  };
  await f.tick();
  const next = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(next.request).toMatchObject({ kind: "map-inventory", cursor: "page-two" });
  f.finish({ kind: "catalog", context: f.context, entries: f.entries.slice(2), nextCursor: null });
  await f.tick();
  const plan = TranscriptMapCatalogInputSchema.parse(
    JSON.parse(String(f.fleet.launched.at(-1)!.input[INPUT_FIELD])),
  );
  expect(plan.request).toMatchObject({ kind: "map-plan", capture: f.captures[0] });
  expect(f.codeCalls).toEqual([]);
});

test("disablement cannot retire a native catalog post still arriving at the hub", async () => {
  const f = await catalogDeployment();
  const execute = f.fleet.execute.bind(f.fleet);
  const status = f.fleet.status.bind(f.fleet);
  let arrived!: () => void;
  const attempted = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let posts = 0;
  f.jobs.execute = async (launch) => {
    posts++;
    arrived();
    await held;
    return execute(launch);
  };
  f.jobs.status = (node) => {
    if (!f.fleet.jobs.has(node.jobId)) throw new HostCallError("jobs.status", "job_not_started");
    return status(node);
  };
  const posting = f.tick();
  try {
    await attempted;
    f.draws.enabled = false;
    await f.tick();
    expect(posts).toBe(1);
    expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
      { n: 1n },
    ]);
    release();
    await posting;
    await f.tick();
    expect(posts).toBe(1);
    expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
      { n: 1n },
    ]);
    f.fleet.kill(f.fleet.launched[0]!.jobId, "interrupted");
    await f.tick();
    expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
      { n: 0n },
    ]);
  } finally {
    release();
    await posting;
  }
});

test("overlapping catalog posts that all refuse do not strand the native slot", async () => {
  const f = await catalogDeployment();
  const execute = f.fleet.execute.bind(f.fleet);
  const attempts: string[] = [];
  let firstArrived!: () => void;
  let bothArrived!: () => void;
  let release!: () => void;
  const first = new Promise<void>((resolve) => {
    firstArrived = resolve;
  });
  const both = new Promise<void>((resolve) => {
    bothArrived = resolve;
  });
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  f.jobs.execute = async (launch) => {
    attempts.push(launch.jobId);
    if (attempts.length === 1) firstArrived();
    if (attempts.length === 2) bothArrived();
    await held;
    throw new HostCallError("jobs.execute", "installation_changed");
  };
  f.jobs.status = () => {
    throw new HostCallError("jobs.status", "job_not_started");
  };
  const posting = f.tick();
  let replay: Promise<unknown> | undefined;
  try {
    await first;
    replay = f.tick();
    await both;
    release();
    await Promise.all([posting, replay]);
    expect(attempts[1]).toBe(attempts[0]);
    expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
      { n: 0n },
    ]);
    f.jobs.execute = execute;
    clock += POLICY.cadenceSeconds * 1000;
    await f.tick();
    expect(f.fleet.launched).toHaveLength(1);
    expect(f.fleet.launched[0]!.jobId).not.toBe(attempts[0]);
    expect(f.codeCalls).toEqual([]);
  } finally {
    release();
    await Promise.all([posting, replay]);
  }
});

test("atomic service-binding admission refusals release a never-started catalog slot without paid work", async () => {
  for (const reason of ["service_bindings_changed", "service_bindings_protocol_unsupported"]) {
    const f = await catalogDeployment(MACHINE);
    const execute = f.fleet.execute.bind(f.fleet);
    f.jobs.execute = () => {
      throw new HostCallError("jobs.execute", reason);
    };
    f.jobs.status = () => {
      throw new HostCallError("jobs.status", "job_not_started");
    };
    await f.tick();
    expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
      { n: 0n },
    ]);
    expect(f.fleet.launched).toEqual([]);
    await f.tick();
    f.jobs.execute = execute;
    clock += POLICY.cadenceSeconds * 1000;
    await f.tick();
    expect(f.fleet.launched).toHaveLength(1);
    expect(f.codeCalls).toEqual([]);
  }
});

test("a replay refusal cannot retire an earlier unacknowledged catalog post", async () => {
  const f = await catalogDeployment();
  const execute = f.fleet.execute.bind(f.fleet);
  const status = f.fleet.status.bind(f.fleet);
  let delayed: Parameters<typeof execute>[0] | undefined;
  f.jobs.execute = (launch) => {
    if (delayed === undefined) {
      delayed = launch;
      throw new Error("native acknowledgement lost before visibility");
    }
    throw new HostCallError("jobs.execute", "installation_changed");
  };
  f.jobs.status = (node) => {
    if (!f.fleet.jobs.has(node.jobId)) throw new HostCallError("jobs.status", "job_not_started");
    return status(node);
  };
  await f.tick();
  await f.tick();
  expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
    { n: 1n },
  ]);
  f.draws.enabled = false;
  execute(delayed!);
  await f.tick();
  expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
    { n: 1n },
  ]);
  f.fleet.kill(delayed!.jobId, "interrupted");
  await f.tick();
  expect(await f.db.query(`SELECT count(*) n FROM runs WHERE closure IS NULL`)).toEqual([
    { n: 0n },
  ]);
  expect(f.codeCalls).toEqual([]);
});

test("a newer different disclosure class fences stale context insertion at the database boundary", async () => {
  const f = await catalogDeployment();
  const maps = transcriptMaps(f.store);
  const batch = f.db.batch.bind(f.db);
  let arrived!: () => void;
  const paused = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  f.db.batch = async (statements) => {
    if (
      statements.some(
        (statement) =>
          statement.sql.startsWith("INSERT INTO transcript_map_contexts") &&
          statement.params?.[1] === f.context.classId,
      )
    ) {
      arrived();
      await held;
    }
    return batch(statements);
  };
  const stale = maps
    .recordCatalog({
      machineId: f.route.sourceMachineId,
      context: f.context,
      entries: f.entries,
      nextCursor: "obsolete",
      now: new Date(clock).toISOString(),
    })
    .then(
      () => null,
      (error: unknown) => error,
    );
  try {
    await paused;
    const newer = {
      ...f.context,
      classId: "public",
      ceiling: 0,
      eligibleCaptures: 0,
      digest: `sha256:${"b".repeat(64)}`,
      observedAt: new Date(clock + 1000).toISOString(),
    };
    await maps.recordCatalog({
      machineId: f.route.sourceMachineId,
      context: newer,
      entries: [],
      nextCursor: null,
      now: newer.observedAt,
    });
    release();
    expect(await stale).toBeInstanceOf(Error);
    const state = await maps.catalogState(f.route.sourceMachineId);
    expect(state.context).toEqual(newer);
    expect(state.nextCursor).toBeNull();
    expect(await f.db.query(`SELECT count(*) n FROM transcript_map_captures`)).toEqual([{ n: 0n }]);
  } finally {
    release();
    await stale;
  }
});

/** Real queue/claims/SQLite and sealed receipt ingestion; inference is a synthetic Code answer. */
async function paidMapDeployment(sourceMachineId = "map-source") {
  clock = Date.parse("2026-09-22T09:00:00.000Z");
  const f = await catalogDeployment(sourceMachineId);
  const route = { ...f.route, dailyCost: 5 };
  const { recipes: _recipes, ...mapping } = route;
  const policy = {
    ...POLICY,
    batchSize: 1,
    review: ROUTE,
    mapping,
    activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0 },
  };
  await f.db.run(
    `INSERT INTO policies(version,seq,actor_id,reason,payload,recorded_at)
    VALUES(?,1,'operator','synthetic mapping',?,?)`,
    [policy.version, JSON.stringify(policy), new Date(clock).toISOString()],
  );
  // Paid mapping is drawn only for a running mapping drain on the route's executor.
  await insertDrain(f.store, {
    id: "drn_map",
    machineId: route.executorMachineId,
    preset: MAP_DRAIN_PRESET,
    profile: {
      profile: route.profile,
      model: "synthetic",
      thinking: "low",
      accounts: [],
      resolved: true,
    },
    knobs: { recipes: [] },
    concurrent: 1,
    target: { deadline: new Date(clock + 30 * 24 * 60 * 60 * 1000).toISOString() },
    startedBy: "operator",
  });
  const maps = transcriptMaps(f.store);
  await maps.recordCatalog({
    machineId: route.sourceMachineId,
    context: f.context,
    entries: [f.entries[0]!],
    nextCursor: null,
    now: new Date(clock).toISOString(),
  });
  const tree = f.trees[0]!;
  await maps.recordPlan({
    machineId: route.sourceMachineId,
    context: f.context,
    access: f.entries[0]!.access,
    plan: tree.header,
    nodes: tree.nodes,
    offset: 0,
    nextOffset: null,
    now: new Date(clock).toISOString(),
  });
  const version = await maps.ensureVersion(tree.header.id, route, new Date(clock).toISOString());
  const coordinator = governed(f.store, () => clock, 16);
  const describe = f.fleet.describe.bind(f.fleet);
  f.fleet.describe = (args) => {
    const ready = describe(args);
    return {
      ...ready,
      operations: {
        ...ready.operations,
        [OPERATIONS.mapPrepare]: ready.operations![OPERATIONS.mapCatalog]!,
      },
    };
  };
  const posted: SessionRequest[] = [];
  const readings = new Map<string, SessionRead>();
  const cancelled: string[] = [];
  const engine: CodeEngine = {
    profiles: async () => ({ ok: true, value: [] }),
    checkProfile: async () => ({ ok: true, value: null }),
    async runSession(request) {
      posted.push(request);
      const job = {
        jobId: `map_code_${posted.length}`,
        machineId: request.machineId,
        operationId: TRANSCRIPT_MAP_SESSION_OPERATION,
        pluginId: "atyrode.omp",
        state: "started" as const,
      };
      readings.set(job.jobId, { job, session: null });
      return { ok: true, value: job };
    },
    async readSession({ jobId }) {
      const value = readings.get(jobId);
      return value
        ? { ok: true, value }
        : refusedByCode("engine_unavailable", "synthetic read unavailable");
    },
    async cancelSession({ jobId }) {
      cancelled.push(jobId);
      const previous = readings.get(jobId)!;
      const job = { ...previous.job, state: "cancelled" as const };
      readings.set(jobId, { job, session: null });
      return { ok: true, value: job };
    },
  };
  const tick = (nativeDispatch = true) => {
    clock += 1_000;
    return conductor({
      store: f.store,
      coordinator,
      jobs: f.fleet,
      engine,
      machines: new Folders(),
      keys: new Keys(),
      plan: PLAN,
      mapPreparePlan: UNMETERED_PLAN,
      nativeDispatch,
      now: () => clock,
    }).tick();
  };
  const seal = () => {
    const launch = f.fleet.launched.at(-1)!;
    const input = TranscriptMapPrepareInputSchema.parse(
      JSON.parse(String(launch.input[INPUT_FIELD])),
    );
    const node = tree.nodes.find((candidate) => candidate.id === input.nodeId)!;
    const document = JSON.stringify({
      inference: true,
      source: input.source,
      node,
      mode: input.mode,
      text: node.children.length === 0 ? "synthetic retained source" : null,
      children: input.children,
      baseSummary: input.baseSummary ?? null,
      feedback: input.feedback ?? null,
    });
    f.fleet.finish(launch.jobId, 0, {
      [JOB_OUTPUT_FILES.receipt]: {
        runId: input.runId,
        machineId: route.executorMachineId,
        kind: "mapPrepare",
        startedAt: new Date(clock).toISOString(),
        finishedAt: new Date(clock).toISOString(),
        closure: "completed",
        counts: {},
        mapping: {
          kind: "material",
          sourceMachineId: route.sourceMachineId,
          executorMachineId: route.executorMachineId,
          source: input.source,
          node,
          mode: input.mode,
          context: f.context,
          access: f.entries[0]!.access,
          inputDigest: `sha256:${createHash("sha256").update(document).digest("hex")}`,
          materialBytes: Buffer.byteLength(document),
        },
      },
    });
    return input;
  };
  const answer = (result: TranscriptMapModelResult | string, cost = 0.02) => {
    const jobId = `map_code_${posted.length}`;
    const previous = readings.get(jobId)!;
    const value = sessionRead({
      state: "exited",
      jobId,
      finalMessage:
        typeof result === "string" ? result : `\`\`\`json\n${JSON.stringify(result)}\n\`\`\``,
      inference: {
        calls: 1,
        inputTokens: 90,
        outputTokens: 20,
        cachedInputTokens: 0,
        costMicros: cost * 1_000_000,
      },
      usage: { input: 90, output: 20, cacheRead: 0, cacheWrite: 0, cost: 99 },
    });
    readings.set(jobId, {
      ...value,
      job: {
        ...value.job,
        machineId: previous.job.machineId,
        operationId: previous.job.operationId,
      },
    });
  };
  return {
    ...f,
    maps,
    route,
    version,
    coordinator,
    engine,
    posted,
    readings,
    cancelled,
    tick,
    seal,
    answer,
  };
}

test("a door's read wake draws no mapping work; the next hook wake posts it with its attempt intact", async () => {
  const f = await paidMapDeployment();
  const queued = async () =>
    await f.db.query(`SELECT state,attempt,claim_id FROM transcript_map_work ORDER BY id`);
  await f.maps.refreshWork(f.route, new Date(clock).toISOString(), 64);
  const before = await queued();
  expect(before.length).toBeGreaterThan(0);
  await f.tick(false);
  expect(f.fleet.launched).toEqual([]);
  expect(await f.db.query(`SELECT count(*) n FROM claims`)).toEqual([{ n: 0n }]);
  expect(await queued()).toEqual(before);
  await f.tick(true);
  expect(f.fleet.launched.map((launch) => launch.operationId)).toEqual([OPERATIONS.mapPrepare]);
  expect(await f.db.query(`SELECT count(*) n FROM claims`)).toEqual([{ n: 1n }]);
});

/** The drain controller over the same fixture: a mapping drain launches nothing itself. */
function mapDrainDeps(f: Awaited<ReturnType<typeof paidMapDeployment>>): DrainDeps {
  const refuse = async (): Promise<Started> => ({ refused: "a mapping drain launches no preset" });
  return {
    store: f.store,
    coordinator: f.coordinator,
    launch: { startExplore: refuse, startBeat: refuse },
    jobs: f.fleet,
    engine: f.engine,
    plan: () => PLAN,
    now: () => clock,
  };
}

test("without a running mapping drain, even a native-capable wake draws no mapping work", async () => {
  const f = await paidMapDeployment();
  await f.db.run(`UPDATE drains SET state='stopped', finished_at=? WHERE id='drn_map'`, [
    new Date(clock).toISOString(),
  ]);
  await f.tick(true);
  expect(f.fleet.launched).toEqual([]);
  expect(await f.db.query(`SELECT count(*) n FROM claims`)).toEqual([{ n: 0n }]);
});

test("a mapping drain folds its runs, stops drawing at its target and ends when it is met", async () => {
  const f = await paidMapDeployment();
  await f.db.run(
    `UPDATE drains SET target=json_set(target,'$.costMicros',30000) WHERE id='drn_map'`,
  );
  await f.tick();
  let row = (await readDrain(f.store, "drn_map"))!;
  expect(row.jobsLaunched).toBe(1);
  expect(row.live.map((job) => job.jobId)).toEqual([f.fleet.launched[0]!.jobId]);
  // The drain's fan is one: nothing more is drawn while its run is held.
  await f.tick();
  expect(f.fleet.launched).toHaveLength(1);
  for (let round = 0; round < 2; round++) {
    f.seal();
    await f.tick();
    f.answer({ kind: "summary", text: `Navigation ${String(round)}` }, 0.02);
    await f.tick();
  }
  // Two runs of 0.02 passed the 0.03 target: the second was drawn at 0.02, the third never is.
  expect(f.fleet.launched.map((launch) => launch.operationId)).toEqual([
    OPERATIONS.mapPrepare,
    OPERATIONS.mapPrepare,
  ]);
  const [report] = await drainTick(mapDrainDeps(f));
  expect(report?.state).toBe("target");
  row = (await readDrain(f.store, "drn_map"))!;
  expect(row.spent.costMicros).toBe(40000);
  expect(row.closures).toEqual({ completed: 2 });
  await f.tick();
  expect(f.fleet.launched).toHaveLength(2);
});

test("a mapping drain ends on its own when every eligible transcript is mapped", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  for (let index = 0; index < 3; index++) {
    f.seal();
    await f.tick();
    f.answer({ kind: "summary", text: `Navigation ${String(index)}` });
    await f.tick();
  }
  const [report] = await drainTick(mapDrainDeps(f));
  expect(report?.state).toBe("target");
  expect(report?.reason).toBe("no eligible transcript-mapping work remains");
});

test("stopping a mapping drain cancels its preparation at map-prepare and releases the claim", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  const posted = f.fleet.launched[0]!;
  const row = (await readDrain(f.store, "drn_map"))!;
  const cancelled: string[] = [];
  const cancel = f.fleet.cancel.bind(f.fleet);
  f.fleet.cancel = (node: { jobId: string; operationId?: string }) => {
    cancelled.push(node.operationId ?? "");
    cancel(node);
  };
  await endDrain(mapDrainDeps(f), row, "stopped", "operator stop", row.live);
  expect(cancelled).toEqual([OPERATIONS.mapPrepare]);
  expect(f.fleet.status({ jobId: posted.jobId })).toMatchObject({ state: "cancelled" });
  await f.tick();
  expect(f.posted).toEqual([]);
  expect(await f.db.query(`SELECT finished_at IS NOT NULL done FROM claims`)).toEqual([
    { done: 1n },
  ]);
  expect(await f.db.query(`SELECT state FROM transcript_map_work WHERE state='running'`)).toEqual(
    [],
  );
});

test.each([MACHINE, "map-source"])(
  "paid map leaf/parent generation, served review and bounded correction survive fresh wakes (%s)",
  async (source) => {
    const f = await paidMapDeployment(source);
    await f.tick();
    for (let index = 0; index < 3; index++) {
      const input = f.seal();
      expect(input.children.length > 0).toBe(index === 2);
      await f.tick();
      f.answer({ kind: "summary", text: `Navigation ${index}` });
      await f.tick();
    }
    expect(
      await f.db.query(`SELECT state,count(*) n FROM transcript_map_work GROUP BY state`),
    ).toEqual([{ state: "complete", n: 3n }]);
    expect(f.posted).toHaveLength(3);
    const scope = { machineId: source, context: f.context };
    const root = (await f.maps.node(scope, f.version.id, f.trees[0]!.header.rootId!))!;
    expect(root.coverage.partial).toBe(false);
    const original = root.summary!;
    await f.maps.noteServed({
      readId: "synthetic-consumer",
      summaryIds: [original.id],
      now: new Date(clock).toISOString(),
    });
    await f.tick();
    expect(f.seal().mode).toBe("review");
    await f.tick();
    f.answer({ kind: "review", verdict: "correct", reason: "Preserve the source qualification." });
    await f.tick();
    expect(f.seal().mode).toBe("correct");
    await f.tick();
    f.answer({ kind: "summary", text: "Navigation with the qualification preserved." });
    await f.tick();
    const corrected = (await f.maps.node(scope, f.version.id, root.node.id))!.summary!;
    expect(corrected.supersedes).toBe(original.id);
    expect(corrected.versionId).toBe(original.versionId);
    expect(await f.maps.offers(f.route, new Date(clock).toISOString())).toEqual([]);
    expect((await f.coordinator.spend(clock)).mapping).toBeCloseTo(0.1);
    expect(await f.db.query(`SELECT count(*) n FROM transcript_map_summaries`)).toEqual([
      { n: 4n },
    ]);
    expect(await f.db.query(`SELECT count(*) n FROM run_calls`)).toEqual([{ n: 5n }]);
    await f.maps.noteServed({
      readId: "served-correction",
      summaryIds: [corrected.id],
      now: new Date(clock).toISOString(),
    });
    await f.tick();
    f.seal();
    await f.tick();
    f.answer({
      kind: "review",
      verdict: "reject",
      reason: "The corrected navigation still misses the qualification.",
    });
    await f.tick();
    expect(await f.maps.offers(f.route, new Date(clock).toISOString())).toEqual([]);
    const exhausted = (await f.maps.node(scope, f.version.id, root.node.id))!;
    expect(exhausted.summary).toBeNull();
    expect(exhausted.coverage.stale).toBe(true);
    expect(f.posted).toHaveLength(6);
  },
);

test("binding replacement before inference releases without spend; late paid answers cannot publish", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  f.nativeBinding.revision = "replacement";
  await f.tick();
  expect(f.posted).toEqual([]);
  expect(await f.db.query(`SELECT actual_cost FROM claims WHERE finished_at IS NOT NULL`)).toEqual([
    { actual_cost: 0 },
  ]);
  f.seal();
  await f.tick();
  const jobId = `map_code_${f.posted.length}`;
  f.answer({ kind: "summary", text: "Late summary must not publish." }, 0.12);
  const terminal = f.readings.get(jobId)!;
  f.engine.cancelSession = async () => ({ ok: true, value: terminal.job });
  f.nativeBinding.policySha256 = "c".repeat(64);
  await f.tick();
  expect(await f.db.query(`SELECT id FROM transcript_map_summaries`)).toEqual([]);
  expect((await f.coordinator.spend(clock)).mapping).toBeGreaterThanOrEqual(0.12);
  expect(await f.db.query(`SELECT actual_cost FROM claims WHERE job_id=?`, [jobId])).toEqual([
    { actual_cost: 0.12 },
  ]);
});

test("unconfirmed Code post remains visible and reserved across restart, disablement and reaping", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  let calls = 0;
  f.engine.runSession = async () => {
    calls++;
    return refusedByCode("engine_unconfirmed", "synthetic lost acknowledgement");
  };
  await f.tick();
  clock += POLICY.leaseSeconds * 4_000;
  await f.tick();
  await f.db.run(`UPDATE policies SET payload=json_set(payload,'$.enabled',json('false'))`);
  await f.tick();
  expect(calls).toBe(1);
  expect(await f.db.query(`SELECT actual_cost,finished_at FROM claims`)).toEqual([
    { actual_cost: null, finished_at: null },
  ]);
  expect((await f.coordinator.spend(clock)).mapping).toBe(0.5);
  expect(await f.db.query(`SELECT stage FROM run_progress`)).toEqual([
    { stage: "posting unconfirmed" },
  ]);
});

test("malformed paid answers back off, exhaust attempts and charge the terminal owner rather than transcript estimates", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  const first = f.fleet.launched[0]!;
  const input = TranscriptMapPrepareInputSchema.parse(JSON.parse(String(first.input[INPUT_FIELD])));
  await f.db.run(`UPDATE transcript_map_work SET state='obsolete' WHERE node_id!=?`, [
    input.nodeId,
  ]);
  for (let attempt = 0; attempt < 2; attempt++) {
    f.seal();
    await f.tick();
    f.answer("not a JSON answer", 0.07);
    await f.tick();
    if (attempt === 0) {
      clock += 61_000;
      await f.tick();
    }
  }
  expect(
    await f.db.query(`SELECT state,attempt FROM transcript_map_work WHERE node_id=?`, [
      input.nodeId,
    ]),
  ).toEqual([{ state: "failed", attempt: 2n }]);
  expect(await f.db.query(`SELECT id FROM transcript_map_summaries`)).toEqual([]);
  expect((await f.coordinator.spend(clock)).mapping).toBeCloseTo(0.14);
});

test("an already-bound mapping session retains earned settlement authority after lease expiry", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  await f.tick();
  clock += POLICY.leaseSeconds * 2_000;
  await f.tick();
  expect(f.cancelled).toEqual([]);
  f.answer({ kind: "summary", text: "Navigation earned by the original bound session." }, 0.07);
  await f.tick();
  expect(
    await f.db.query(`SELECT outcome,actual_cost FROM claims WHERE job_id='map_code_1'`),
  ).toEqual([{ outcome: "completed", actual_cost: 0.07 }]);
  expect(
    await f.db.query(`SELECT state FROM transcript_map_work WHERE run_id IN
    (SELECT id FROM runs WHERE job_id='map_code_1')`),
  ).toEqual([{ state: "complete" }]);
});

test("terminal mapping cancellation charges conservative exposure and clears running work", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  await f.tick();
  await f.db.run(`UPDATE policies SET payload=json_set(payload,'$.enabled',json('false'))`);
  await f.tick();
  expect(f.cancelled).toEqual(["map_code_1"]);
  expect(
    await f.db.query(`SELECT state FROM transcript_map_work WHERE run_id IS NOT NULL`),
  ).toEqual([]);
  expect(await f.db.query(`SELECT actual_cost,outcome FROM claims`)).toEqual([
    { actual_cost: 0.5, outcome: "failed" },
  ]);
  expect(await f.db.query(`SELECT id FROM transcript_map_summaries`)).toEqual([]);
});

test("native ambiguous post resumes the same preparation identity, while definitive admission refusal refunds", async () => {
  const f = await paidMapDeployment();
  const execute = f.fleet.execute.bind(f.fleet);
  const status = f.fleet.status.bind(f.fleet);
  let first = "";
  f.fleet.execute = (request) => {
    first = request.jobId;
    throw new HostCallError("jobs.execute", "transport unavailable");
  };
  f.fleet.status = (node) => {
    if (!f.fleet.jobs.has(node.jobId)) throw new HostCallError("jobs.status", "job_not_started");
    return status(node);
  };
  await f.tick();
  expect((await f.coordinator.spend(clock)).mapping).toBe(0.5);
  f.fleet.execute = execute;
  await f.tick();
  expect(f.fleet.launched.map((job) => job.jobId)).toEqual([first]);
  expect(await f.db.query(`SELECT count(*) n FROM claims`)).toEqual([{ n: 1n }]);
  const g = await paidMapDeployment();
  g.fleet.execute = () => {
    throw new HostCallError("jobs.execute", "service_bindings_changed");
  };
  g.fleet.status = () => {
    throw new HostCallError("jobs.status", "job_not_started");
  };
  await g.tick();
  expect((await g.coordinator.spend(clock)).mapping).toBe(0);
  expect(g.posted).toEqual([]);
});

test("mapping reconciles earned spend after a crash between artifact projection and ledger finish", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  await f.tick();
  f.answer({ kind: "summary", text: "Durable synthetic navigation." }, 0.09);
  const finish = f.coordinator.finish.bind(f.coordinator);
  f.coordinator.finish = async () => {
    throw new Error("synthetic ledger interruption");
  };
  await expect(f.tick()).rejects.toThrow("synthetic ledger interruption");
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_summaries`)).toEqual([{ n: 1n }]);
  expect(await f.db.query(`SELECT actual_cost FROM claims`)).toEqual([{ actual_cost: null }]);
  f.coordinator.finish = finish;
  await f.tick();
  expect(
    await f.db.query(`SELECT actual_cost,outcome FROM claims WHERE job_id='map_code_1'`),
  ).toEqual([{ actual_cost: 0.09, outcome: "completed" }]);
  expect(await f.db.query(`SELECT count(*) n FROM transcript_map_summaries`)).toEqual([{ n: 1n }]);
});

test.each(["source", "executor", "profile", "recipe", "bounds", "lease"] as const)(
  "prepared mapping cannot borrow changed %s authority",
  async (changed) => {
    const f = await paidMapDeployment();
    await f.tick();
    f.seal();
    if (changed === "lease") clock += POLICY.leaseSeconds * 2_000;
    else {
      const rows = await f.db.query<{ payload: string }>(`SELECT payload FROM policies`);
      const policy = JSON.parse(rows[0]!.payload) as Policy;
      const mapping = policy.mapping!;
      if (changed === "source") mapping.sourceMachineId = "other-source";
      if (changed === "executor") mapping.executorMachineId = "other-executor";
      if (changed === "profile") mapping.profile.expectedRevision++;
      if (changed === "recipe") policy.review!.recipes[0]!.version++;
      if (changed === "bounds") mapping.inferenceLimits = { calls: 2 };
      await f.db.run(`UPDATE policies SET payload=?`, [JSON.stringify(policy)]);
    }
    await f.tick();
    expect(f.posted).toEqual([]);
    expect(
      await f.db.query(`SELECT actual_cost FROM claims WHERE finished_at IS NOT NULL`),
    ).toEqual([{ actual_cost: 0 }]);
    expect(await f.db.query(`SELECT id FROM transcript_map_summaries`)).toEqual([]);
  },
);

test("an expired mapping claim that crashed before its durable intent is reaped without fictional spend", async () => {
  const f = await paidMapDeployment();
  const claim = f.coordinator.claim.bind(f.coordinator);
  f.coordinator.claim = async (request) => {
    await claim(request);
    throw new Error("synthetic crash after claim");
  };
  await expect(f.tick()).rejects.toThrow("synthetic crash after claim");
  const held = await f.db.query<{ id: string }>(`SELECT id FROM claims`);
  expect(held).toHaveLength(1);
  f.coordinator.claim = claim;
  clock += POLICY.leaseSeconds * 3_000;
  await f.tick();
  expect(
    await f.db.query(`SELECT actual_cost,outcome FROM claims WHERE id=?`, [held[0]!.id]),
  ).toEqual([{ actual_cost: 0, outcome: "failed" }]);
  expect(f.posted).toEqual([]);
});

test("a completed material receipt cannot authorize inference after its native job failed", async () => {
  const f = await paidMapDeployment();
  await f.tick();
  f.seal();
  f.fleet.jobs.get(f.fleet.launched.at(-1)!.jobId)!.exitCode = 7;
  await f.tick();
  expect(f.posted).toEqual([]);
  expect(await f.db.query(`SELECT actual_cost FROM claims WHERE finished_at IS NOT NULL`)).toEqual([
    { actual_cost: 0 },
  ]);
  expect(await f.db.query(`SELECT id FROM transcript_map_summaries`)).toEqual([]);
});
