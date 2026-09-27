/*
  The coordinator's own tests: the behaviours v0.4.0:internal/evaluation's tests defended, ported onto
  the plugin's tables. Each one is a rule an operator would notice the loss of — the order of the
  refusals, the reservations the lanes actually are, the fence, and the floor under a lease — and
  none of them asserts a sentence or a shape for its own sake.

  The database is the real schema (SCHEMA_V1) on a real SQLite file, served through the same three
  verbs the engine serves a plugin: `query`, `run` and a `batch` that is one immediate
  transaction. A draw that passed against a mock of those and failed against SQL would have
  tested nothing.
*/

import { Database } from "bun:sqlite";
import { createHash } from "node:crypto";
import { afterEach, expect, test } from "bun:test";
import type {
  GuestDatabase,
  GuestSqlParam,
  GuestSqlRow,
  GuestSqlStatement,
} from "@manifold/plugin-kit";
import { SCHEMA_V1 } from "./schema.ts";
import { analysisOffers } from "./analysis.ts";
import { transcriptMaps } from "./transcript-maps.ts";
import {
  transcriptMapCaptureId,
  transcriptMapManifestDigest,
  transcriptMapNodeId,
  transcriptMapPlanId,
} from "../transcript-map-identity.ts";
import {
  ANALYSIS_BRIEF_BYTE_LIMIT,
  ANALYSIS_BRIEF_LIMIT,
  ANALYSIS_SOURCE_LIMIT,
  CHALLENGE_RELATION,
  MATERIAL_SCHEMA,
  MAX_MATERIAL_BYTES,
  OPERATIONS,
  ROLES,
  SESSION_RECORD_COORDINATES,
  TranscriptMapNodeSchema,
  TranscriptMapPlanSchema,
  TranscriptMapSourceSchema,
  type Stage,
} from "../contract.ts";
import {
  applyBudget,
  budgetChanges,
  coordinator,
  DEFAULT_POLICY,
  PolicySchema,
  leaseFloor,
  validateBudget,
  mappingPolicy,
  validateNewPolicy,
  validatePolicy,
  type Assignment,
  type Coordinator,
  type DrawResult,
  type Policy,
} from "./coordinator.ts";

const NOW = Date.parse("2026-09-12T12:00:00.000Z");
const DAY = 86_400_000;

function ago(days: number): string {
  return new Date(NOW - days * DAY).toISOString();
}

const open: Database[] = [];

afterEach(() => {
  while (open.length > 0) open.pop()?.close();
});

/** The engine's three verbs over a real file; `batch` is BEGIN IMMEDIATE … COMMIT, as ADR 0034
 *  specifies and as `openPluginDatabase` implements it, and the file is opened with the options
 *  the engine opens a plugin's with (`server/src/plugin-database.ts`) — `safeIntegers` above all,
 *  which is what makes every INTEGER column answer as a BIGINT here as it does in the hub. */
function store(): { db: GuestDatabase } {
  const file = new Database(":memory:", { strict: true, safeIntegers: true });
  open.push(file);
  for (const statement of SCHEMA_V1) file.run(statement);
  const rowsOf = (sql: string, params: readonly GuestSqlParam[] | undefined): GuestSqlRow[] =>
    file.prepare(sql).all(...((params ?? []) as never[])) as GuestSqlRow[];
  return {
    db: {
      pluginId: "atyrode.babel",
      query: async <Row extends GuestSqlRow>(sql: string, params?: readonly GuestSqlParam[]) =>
        rowsOf(sql, params) as unknown as readonly Row[],
      run: async (sql: string, params?: readonly GuestSqlParam[]) => {
        const result = file.run(sql, ...((params ?? []) as never[]));
        return { changes: Number(result.changes), lastInsertRowid: BigInt(result.lastInsertRowid) };
      },
      batch: async (statements: readonly GuestSqlStatement[]) => {
        file.run("BEGIN IMMEDIATE");
        try {
          const out = statements.map((statement) => rowsOf(statement.sql, statement.params));
          file.run("COMMIT");
          return out;
        } catch (error) {
          file.run("ROLLBACK");
          throw error;
        }
      },
    },
  };
}

// ---------------------------------------------------------------------------- fixtures

/** The manifest's `limits.concurrentJobs` for explore and evaluate, as `server.ts` reads it off
 *  the real manifest: the ceiling no policy and no overlay may name a bound above. */
const CONCURRENT_JOBS = 16;

interface Seeded {
  readonly db: GuestDatabase;
  readonly coord: Coordinator;
}

async function deployment(policy?: Partial<Policy>): Promise<Seeded> {
  const handle = store();
  const coord = coordinator(handle, () => NOW, CONCURRENT_JOBS);
  if (policy !== undefined) {
    const full: Policy = { ...DEFAULT_POLICY, enabled: true, ...policy };
    await handle.db.run(
      `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
       VALUES(?,?,?,?,?,?)`,
      [full.version, 1, "operator", "the test's policy", JSON.stringify(full), ago(1)],
    );
  }
  return { db: handle.db, coord };
}

async function record(
  db: GuestDatabase,
  id: string,
  kind: string,
  createdDaysAgo: number,
): Promise<string> {
  await db.run(
    `INSERT INTO records(id, kind, root_id, seq, actor_kind, actor_id, title, created_at, payload)
     VALUES(?,?,?,0,'run','run_seed',?,?,'{}')`,
    [id, kind, id, `a ${kind}`, ago(createdDaysAgo)],
  );
  return id;
}

async function status(
  db: GuestDatabase,
  recordId: string,
  state: string,
  daysAgo: number,
): Promise<void> {
  await db.run(
    `INSERT INTO status_events(id, record_id, seq, status, actor_kind, actor_id, recorded_at)
     VALUES(?,?,?,?,'run','run_seed',?)`,
    [`sev_${recordId}_${state}`, recordId, 1, state, ago(daysAgo)],
  );
}

async function filing(
  db: GuestDatabase,
  recordId: string,
  entityId: string,
  heuristic = 0,
): Promise<void> {
  await db.run(
    `INSERT INTO filings(id, record_id, entity_id, rationale, author_kind, author_id, heuristic,
       withdrawn, created_at) VALUES(?,?,?,?,'run','run_seed',?,0,?)`,
    [`fil_${recordId}_${entityId || "none"}`, recordId, entityId, "seeded", heuristic, ago(1)],
  );
}

async function fact(
  db: GuestDatabase,
  entityId: string,
  predicate: string,
  value: string,
): Promise<void> {
  await db.run(
    `INSERT INTO entities(id, kind, name, canonical_id, created_by, created_at)
     VALUES(?,'project',?,?,'operator',?) ON CONFLICT(id) DO NOTHING`,
    [entityId, entityId, entityId, ago(30)],
  );
  await db.run(
    `INSERT INTO facts(id, entity_id, predicate, value, valid_from, observed_at, authority_kind,
       authority_id, recorded_at) VALUES(?,?,?,?,?,?,'operator','operator',?)`,
    [`fct_${entityId}_${predicate}`, entityId, predicate, value, ago(20), ago(20), ago(20)],
  );
}

async function assessment(
  db: GuestDatabase,
  recordId: string,
  role: string,
  daysAgo: number,
  vote: string | null = null,
): Promise<void> {
  await db.run(
    `INSERT INTO assessments(id, record_id, revision_id, run_id, role, vote, payload, recorded_at)
     VALUES(?,?,?,'run_old',?,?,'{}',?)`,
    [`ass_${recordId}_${role}_${String(daysAgo)}`, recordId, recordId, role, vote, ago(daysAgo)],
  );
}

/** Every review role a hypothesis carries, each assessed once and long enough ago that nothing is
 *  resting: the record is neither an initial-review obligation nor untouched, so the coverage and
 *  discovery reservations are empty and the weighted share is what decides. */
async function reviewedOnce(db: GuestDatabase, recordId: string): Promise<void> {
  for (const role of ["reception", "evidence", "challenge", "relevance"]) {
    await assessment(db, recordId, role, 30);
  }
}

async function claimRow(
  db: GuestDatabase,
  id: string,
  runId: string,
  reserved: number,
  actual: number | null,
  grantedDaysAgo = 0,
  jobId: string | null = null,
): Promise<void> {
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, job_id, run_id, fence,
       reserved_cost, actual_cost, granted_at, expires_at, finished_at, outcome)
     VALUES(?,?,'reception','weighted','1',?,?,1,?,?,?,?,?,?)`,
    [
      id,
      "hyp_ffffffff",
      jobId,
      runId,
      reserved,
      actual,
      new Date(NOW - grantedDaysAgo * DAY).toISOString(),
      new Date(NOW - grantedDaysAgo * DAY + 900_000).toISOString(),
      actual === null ? null : new Date(NOW).toISOString(),
      actual === null ? null : "completed",
    ],
  );
}

function drawn(result: DrawResult): Assignment {
  if (result.outcome !== "assignment") {
    throw new Error(
      `expected an assignment, got the gap ${result.gap.reason}: ${result.gap.detail}`,
    );
  }
  return result.assignment;
}

/** One draw per seed, so a claim of "never drawn" is a claim about the sampler and not about one
 *  lucky roll. Nothing is claimed, so every draw sees the same store. */
async function sampleDraws(coord: Coordinator, count: number): Promise<Assignment[]> {
  const out: Assignment[] = [];
  for (let seed = 1; seed <= count; seed += 1) {
    const result = await coord.draw({ runId: "cycle_1", now: NOW, seed: BigInt(seed) });
    if (result.outcome === "assignment") out.push(result.assignment);
  }
  return out;
}

// ---------------------------------------------------------------------------- the policy

test("the policy in force is the newest row, and a deployment with none is disabled", async () => {
  const { db, coord } = await deployment();

  const fresh = await coord.policy();
  expect(fresh.source).toBe("default");
  expect(fresh.policy.enabled).toBe(false);
  expect(fresh.policy.leaseSeconds).toBe(900);
  expect(fresh.policy.batchSize).toBe(4);
  expect(fresh.policy.initialReviews).toBe(2);
  expect(fresh.policy.maxItemReviews).toBe(6);
  expect(fresh.policy.coverageShare).toBe(0.5);
  expect(fresh.policy.perCycleCost).toBe(0.25);
  expect(fresh.policy.dailyCost).toBe(2);

  for (const [seq, version, lease] of [
    [1, "1", 600],
    [2, "2026-09-operator-tuned", 1200],
  ] as const) {
    await db.run(
      `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
       VALUES(?,?,'operator','',?,?)`,
      [version, seq, JSON.stringify({ enabled: true, leaseSeconds: lease }), ago(2 - seq)],
    );
  }

  const inForce = await coord.policy();
  expect(inForce.source).toBe("stored");
  expect(inForce.version).toBe("2026-09-operator-tuned");
  expect(inForce.policy.leaseSeconds).toBe(1200);
  // A setting the stored payload never mentioned is the measured constant, not a zero.
  expect(inForce.policy.overdueSeconds).toBe(14 * 24 * 3600);
});

test("the validator refuses the policies that would make something else lie", async () => {
  const ceiling = CONCURRENT_JOBS;
  expect(validatePolicy(DEFAULT_POLICY, ceiling)).toBeNull();
  expect(validatePolicy({ ...DEFAULT_POLICY, explorationShare: 0 }, ceiling)).toContain(
    "protected",
  );
  expect(validatePolicy({ ...DEFAULT_POLICY, discoveryShare: 0 }, ceiling)).toContain("protected");
  expect(
    validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0.6, filingShare: 0.3 }, ceiling),
  ).toContain("over-commit");
  expect(validatePolicy({ ...DEFAULT_POLICY, maxItemReviews: 1 }, ceiling)).toContain(
    "below initial reviews",
  );
  expect(validatePolicy({ ...DEFAULT_POLICY, dailyCost: 0.1 }, ceiling)).toContain(
    "below the per-cycle cost",
  );
  expect(validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0 }, ceiling)).toBeNull();
  expect(
    validatePolicy({ ...DEFAULT_POLICY, filingShare: 0, backlogShare: 0 }, ceiling),
  ).toBeNull();
});

test("a per-machine bound above the manifest's ceiling is refused, by the policy door and by the overlay's", () => {
  // The hub refuses every posting past `limits.concurrentJobs` at `execute` (`concurrency_limit`)
  // and the conductor releases the claim charged at its reservation — so a governor that admitted
  // draws above the ceiling would spend the day's allowance on postings that never run. The
  // refusal names the ceiling, because the remedy is either a smaller bound or a new manifest.
  const ceiling = CONCURRENT_JOBS;
  const above = validatePolicy({ ...DEFAULT_POLICY, concurrentPerMachine: ceiling + 1 }, ceiling);
  expect(above).toContain("17 concurrent assignments per machine");
  expect(above).toContain("16 jobs a machine runs at once");
  expect(validatePolicy({ ...DEFAULT_POLICY, concurrentPerMachine: ceiling }, ceiling)).toBeNull();
  // A policy that states no bound is bounded by the batch it was written with, and that is the
  // number judged: the ceiling is on what one MACHINE holds, however the policy spells it.
  expect(validatePolicy({ ...DEFAULT_POLICY, batchSize: ceiling + 1 }, ceiling)).toContain(
    "17 concurrent assignments per machine",
  );

  // And the same rule at the overlay's door, through the one validator: #260's acceptance
  // number (a drain naming eight) passes, and a drain that asks for more than the machine half
  // will run is refused before a reservation is spent rather than after eight of them are.
  const standing: Policy = { ...DEFAULT_POLICY, enabled: true, leaseSeconds: 900, batchSize: 4 };
  const drain = {
    id: "bdg_1",
    createdAt: NOW,
    expiresAt: NOW + 3_600_000,
    perCycleCost: 2,
    dailyCost: 4,
    concurrentPerMachine: ceiling + 1,
    reason: "draining victorballu",
  };
  const refused = validateBudget(standing, drain, ceiling);
  expect(refused).toContain("17 concurrent assignments per machine");
  expect(refused).toContain("16 jobs a machine runs at once");
  expect(validateBudget(standing, { ...drain, concurrentPerMachine: ceiling }, ceiling)).toBeNull();
  expect(validateBudget(standing, { ...drain, concurrentPerMachine: 8 }, ceiling)).toBeNull();
});

test("a manifest that declares no ceiling bounds nothing, rather than bounding by an invented number", () => {
  /*
    THE DEFECT THIS PINS (#279). The ceiling is read off `limits.concurrentJobs` on the
    operations this bundle DECLARES, and the two that declared one were the two a launcher
    posted. With them gone the reader used to fall back to the policy's own default batch —
    four — so an operator's stored policy at eight was refused by a sentence citing "the jobs a
    machine runs at once under this plugin's manifest", a manifest that says nothing about it.
    A bound exists because the hub refuses postings past a DECLARED number; where none is
    declared there is no refusal to protect anyone from, so the absence travels as `null` and
    the bound is skipped.
  */
  const standing: Policy = { ...DEFAULT_POLICY, enabled: true, leaseSeconds: 900, batchSize: 4 };
  expect(validatePolicy({ ...standing, concurrentPerMachine: 8 }, null)).toBeNull();
  expect(validatePolicy({ ...standing, concurrentPerMachine: 256 }, null)).toBeNull();
  // Everything else a policy is judged by is untouched: the absence lifts ONE rule.
  expect(validatePolicy({ ...standing, concurrentPerMachine: 0 }, null)).toContain("below one");
  expect(validateNewPolicy({ ...standing, concurrentPerMachine: 8 }, null)).toBeNull();

  const drain = {
    id: "bdg_1",
    createdAt: NOW,
    expiresAt: NOW + 3_600_000,
    perCycleCost: 2,
    dailyCost: 4,
    concurrentPerMachine: 8,
    reason: "draining victorballu",
  };
  expect(validateBudget(standing, drain, null)).toBeNull();
  // …and the lease still has to cover the fan the overlay names, which is the rule that is
  // about this deployment's own numbers rather than about a manifest.
  expect(
    validateBudget(
      { ...standing, leaseSeconds: 300 },
      { ...drain, concurrentPerMachine: 16 },
      null,
    ),
  ).toContain("320s");
});

test("the lease floor refuses a new policy that would need renewal to work at all", () => {
  // Measured: 20s per assignment in the batch, never under five minutes.
  expect(leaseFloor(1)).toBe(300);
  expect(leaseFloor(4)).toBe(300);
  expect(leaseFloor(24)).toBe(480);

  // The policy four runs were lost under, with a bound the manifest's ceiling allows: what the
  // lease has to cover is the larger of the two, and twenty-four claims behind one lease is
  // twenty-four whether they are spread over a fleet or held by one host.
  const lost: Policy = {
    ...DEFAULT_POLICY,
    leaseSeconds: 240,
    batchSize: 24,
    concurrentPerMachine: 4,
  };
  expect(validateNewPolicy(lost, CONCURRENT_JOBS)).toContain("480s");
  // …and the same policy already stored keeps drawing: refusing it at draw time would stop every
  // review on the deployment until the operator noticed.
  expect(validatePolicy(lost, CONCURRENT_JOBS)).toBeNull();
  expect(validateNewPolicy(DEFAULT_POLICY, CONCURRENT_JOBS)).toBeNull();
});

test("legacy cookbooks remain readable but cannot install a second policy shape", () => {
  const parsed = PolicySchema.parse({
    ...DEFAULT_POLICY,
    recipes: [{ id: "outcome-integrity", title: "Outcome integrity" }],
  });
  expect(validatePolicy(parsed, CONCURRENT_JOBS)).toBeNull();
  expect(validateNewPolicy(parsed, CONCURRENT_JOBS)).toContain("legacy read format");
});

// ---------------------------------------------------------------------------- the order of refusals

test("a disabled policy refuses before any budget is consulted", async () => {
  const { db, coord } = await deployment({ enabled: false });
  await record(db, "hyp_00000001", "hypothesis", 40);
  // A day already over its allowance, so a budget-first reading would answer "daily".
  await claimRow(db, "asg_spent", "cycle_1", 5, null);

  const result = await coord.draw({ runId: "cycle_1", now: NOW });
  expect(result.outcome).toBe("gap");
  if (result.outcome !== "gap") throw new Error("unreachable");
  expect(result.gap.reason).toBe("disabled");
});

test("the budget refuses before any candidate is built", async () => {
  const { db, coord } = await deployment({ enabled: true });
  await record(db, "hyp_00000001", "hypothesis", 40);
  await claimRow(db, "asg_spent", "other_cycle", 5, null);

  const daily = await coord.draw({ runId: "cycle_1", now: NOW });
  expect(daily.outcome).toBe("gap");
  if (daily.outcome !== "gap") throw new Error("unreachable");
  expect(daily.gap.reason).toBe("daily");
  // Nothing was built, so nothing is explained: the refusal is the whole answer.
  expect(daily.gaps).toEqual([]);

  const batched = await deployment({ enabled: true, batchSize: 1 });
  await record(batched.db, "hyp_00000001", "hypothesis", 40);
  // With a job behind it: a grant whose posting never landed is not a batch slot (#259).
  await claimRow(batched.db, "asg_open", "cycle_1", 0.01, null, 0, "job_open");
  const held = await batched.coord.draw({ runId: "cycle_1", now: NOW });
  if (held.outcome !== "gap") throw new Error("the batch bound did not refuse");
  expect(held.gap.reason).toBe("batch");
});

test("a claim whose job is over holds no batch slot, whatever its lease still says", async () => {
  const { db, coord } = await deployment({ enabled: true, batchSize: 1 });
  await record(db, "hyp_00000001", "hypothesis", 40);
  // The one slot this deployment has, held by a claim whose job is still running. The draw is
  // refused, and that is the bound doing exactly what it is for.
  await claimRow(db, "asg_live", "cycle_0", 0.01, null, 0, "job_live");
  await runOn(db, "job_live", "dev-01");
  expect(await coord.open(NOW)).toEqual({ total: 1, byMachine: { "dev-01": 1 } });
  const wedged = await coord.draw({ runId: "cycle_1", now: NOW });
  if (wedged.outcome !== "gap") throw new Error("the batch bound did not refuse");
  expect(wedged.gap.reason).toBe("batch");

  // Now the job ends and nothing settles the claim: an ingestion that could not write it, a
  // hub that restarted between the two, an operator who killed the worker. THE LEASE STILL
  // HAS FIFTEEN MINUTES TO RUN, and on 2026-09-13 a lease raised to 5200 seconds is what
  // seventy of these held the top-ranked subjects with for 86 minutes each (F3). A slot is
  // work in progress, and there is no work here: the deployment draws again at once, without
  // waiting for the conductor's reaper to get to the row.
  await db.run(`UPDATE runs SET closure = 'failed', finished_at = ? WHERE job_id = ?`, [
    new Date(NOW).toISOString(),
    "job_live",
  ]);
  expect(await coord.open(NOW)).toEqual({ total: 0, byMachine: {} });
  expect(drawn(await coord.draw({ runId: "cycle_1", now: NOW })).recordId).toBe("hyp_00000001");
});

test("a day with nothing to review says so, and says why each candidate was declined", async () => {
  const { db, coord } = await deployment({ enabled: true });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await status(db, id, "superseded", 2);

  const result = await coord.draw({ runId: "cycle_1", now: NOW });
  if (result.outcome !== "gap") throw new Error("a superseded record was drawn");
  expect(result.gap.reason).toBe("no-candidates");
  expect(result.gaps).toContainEqual({
    recordId: id,
    role: "",
    reason: "record-replaced",
    detail: "superseded, so no review of it is outstanding",
  });
  // The two work shares owe the same answer: nothing needed drawing is not nothing was drawn.
  expect(result.gaps.map((gap) => `${gap.role}:${gap.reason}`)).toContain("filing:empty");
  expect(result.gaps.map((gap) => `${gap.role}:${gap.reason}`)).toContain("backlog:empty");
});

test("the topic's lifecycle and the record's own are declined under different words", async () => {
  const { db, coord } = await deployment({ enabled: true });
  const underRetired = await record(db, "hyp_00000001", "hypothesis", 40);
  const superseded = await record(db, "hyp_00000002", "hypothesis", 40);
  await filing(db, underRetired, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "retired");
  await status(db, superseded, "superseded", 2);

  const result = await coord.draw({ runId: "cycle_1", now: NOW, seed: 1n });
  const wordFor = (id: string): string | undefined =>
    result.gaps.find((gap) => gap.recordId === id)?.reason;
  // THE TWO SAY WHOSE LIFECYCLE THEY ARE ABOUT (#382). A panel shows these words counted and
  // nothing else — "4 × retired, 9 × replaced" reads as one fact tallied twice, when one is
  // about the topic the work is filed under and never about a record, and the other is about
  // the record and silently contains the retired ones.
  expect(wordFor(underRetired)).toBe("topic-retired");
  expect(wordFor(superseded)).toBe("record-replaced");
});

// ---------------------------------------------------------------------------- the stance gate

test("a record filed under an excluded topic is a gap and is never drawn", async () => {
  const { db, coord } = await deployment({ enabled: true });
  const withheld = await record(db, "hyp_00000001", "hypothesis", 40);
  const open = await record(db, "hyp_00000002", "hypothesis", 40);
  await filing(db, withheld, "ent_0000000a");
  await filing(db, open, "ent_0000000b");
  await fact(db, "ent_0000000a", "analysis-policy", "excluded");
  await fact(db, "ent_0000000b", "lifecycle", "active");

  const draws = await sampleDraws(coord, 60);
  expect(draws.length).toBe(60);
  expect(draws.some((assignment) => assignment.recordId === withheld)).toBe(false);
  expect(draws.every((assignment) => assignment.recordId === open)).toBe(true);

  const result = await coord.draw({ runId: "cycle_1", now: NOW, seed: 1n });
  expect(result.gaps).toContainEqual({
    recordId: withheld,
    role: "",
    reason: "excluded",
    detail: "ent_0000000a is excluded, so work filed under it is withheld",
  });

  // Rescinding the stance makes the same record drawable again with nothing to undo.
  await db.run(
    `INSERT INTO facts(id, entity_id, predicate, value, valid_from, observed_at, authority_kind,
       authority_id, supersedes_id, recorded_at)
     VALUES('fct_rescind','ent_0000000a','analysis-policy','normal',?,?,'operator','operator',
       'fct_ent_0000000a_analysis-policy',?)`,
    [ago(1), ago(1), ago(1)],
  );
  const after = await sampleDraws(coord, 60);
  expect(after.some((assignment) => assignment.recordId === withheld)).toBe(true);
});

test("a dormant topic loses the weighted draw to an active one, and is never shut out", async () => {
  const { db, coord } = await deployment({ enabled: true });
  const active = await record(db, "hyp_00000001", "hypothesis", 40);
  const dormant = await record(db, "hyp_00000002", "hypothesis", 40);
  await filing(db, active, "ent_0000000a");
  await filing(db, dormant, "ent_0000000b");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  await fact(db, "ent_0000000b", "lifecycle", "dormant");
  await reviewedOnce(db, active);
  await reviewedOnce(db, dormant);

  const draws = await sampleDraws(coord, 200);
  expect(draws.length).toBe(200);
  const forActive = draws.filter((assignment) => assignment.recordId === active).length;
  const forDormant = draws.length - forActive;
  expect(forActive).toBeGreaterThan(forDormant * 2);
  // Damped, not excluded: §4.13 says not interested is a signal and never a deletion, and the
  // uniform exploration share is what keeps the dormant one visible at all.
  expect(forDormant).toBeGreaterThan(0);
});

// ---------------------------------------------------------------------------- the work lanes

test("the filing lane draws unfiled records and nothing else", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    coverageShare: 0,
    discoveryShare: 0.02,
    explorationShare: 0.02,
    filingShare: 0.9,
    backlogShare: 0.05,
  });
  const unfiled = await record(db, "hyp_00000001", "hypothesis", 40);
  const filed = await record(db, "hyp_00000002", "hypothesis", 60);
  const heuristically = await record(db, "hyp_00000003", "hypothesis", 50);
  await filing(db, filed, "ent_0000000a");
  // A heuristic filing is one a run should revisit, so it does not take the record out of the
  // backlog the filing share exists to clear.
  await filing(db, heuristically, "ent_0000000a", 1);
  await fact(db, "ent_0000000a", "lifecycle", "active");

  const draws = await sampleDraws(coord, 80);
  const filings = draws.filter((assignment) => assignment.role === "filing");
  expect(filings.length).toBeGreaterThan(0);
  expect(filings.every((assignment) => assignment.lane === "filing")).toBe(true);
  // The oldest record carries a filing, so the share skips it however long it has been there;
  // the oldest record whose only filing is heuristic is what it draws.
  expect(new Set(filings.map((assignment) => assignment.recordId))).toEqual(
    new Set([heuristically]),
  );
  // A filing draw is work, never a review: it must not arrive at a reviewer. And a challenge is
  // accounted to its own lane whichever reservation drew it, so a cycle can say how much went to
  // arguing rather than to reviewing.
  expect(
    draws.every((assignment) => (assignment.lane === "filing") === (assignment.role === "filing")),
  ).toBe(true);
  const challenges = draws.filter((assignment) => assignment.role === "challenge");
  expect(challenges.length).toBeGreaterThan(0);
  expect(challenges.every((assignment) => assignment.lane === "challenge")).toBe(true);

  // A later filing on the same record and entity takes it out of the backlog — the newest row
  // per record and entity is what decides — and the share moves to the next unfiled record
  // rather than idling.
  await db.run(
    `INSERT INTO filings(id, record_id, entity_id, rationale, author_kind, author_id, heuristic,
       withdrawn, supersedes_id, created_at)
     VALUES('fil_confirmed',?,'ent_0000000a','a run confirmed it','run','run_seed',0,0,?,?)`,
    [heuristically, `fil_${heuristically}_ent_0000000a`, ago(0)],
  );
  const after = (await sampleDraws(coord, 80)).filter((assignment) => assignment.role === "filing");
  expect(after.length).toBeGreaterThan(0);
  expect(new Set(after.map((assignment) => assignment.recordId))).toEqual(new Set([unfiled]));

  // And a withdrawal puts it back: §4.13 withdraws a filing rather than deleting it, so the
  // newest row saying `withdrawn` must leave the record unfiled rather than uncovering the
  // filing it replaced.
  await db.run(
    `INSERT INTO filings(id, record_id, entity_id, rationale, author_kind, author_id, heuristic,
       withdrawn, supersedes_id, created_at)
     VALUES('fil_withdrawn',?,'ent_0000000a','the operator disagreed','operator','alex',0,1,
       'fil_confirmed',?)`,
    [heuristically, new Date(NOW + 1000).toISOString()],
  );
  const withdrawn = (await sampleDraws(coord, 80)).filter(
    (assignment) => assignment.role === "filing",
  );
  expect(new Set(withdrawn.map((assignment) => assignment.recordId))).toEqual(
    new Set([heuristically]),
  );
});

test("the backlog lane draws deferred hypotheses and nothing else", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    coverageShare: 0,
    discoveryShare: 0.02,
    explorationShare: 0.02,
    filingShare: 0.02,
    backlogShare: 0.9,
  });
  const deferred = await record(db, "hyp_00000001", "hypothesis", 60);
  const later = await record(db, "hyp_00000002", "hypothesis", 90);
  const untriaged = await record(db, "hyp_00000003", "hypothesis", 40);
  const proposal = await record(db, "pro_00000004", "proposal", 40);
  for (const id of [deferred, later, untriaged, proposal]) await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  await status(db, deferred, "deferred", 30);
  await status(db, later, "deferred", 5);
  await status(db, untriaged, "untriaged", 30);
  await status(db, proposal, "deferred", 30);

  const draws = await sampleDraws(coord, 80);
  const backlog = draws.filter((assignment) => assignment.role === "backlog");
  expect(backlog.length).toBeGreaterThan(0);
  // Longest set down first, and the record's own age is not what orders it: `later` is the older
  // record and was deferred most recently.
  expect(new Set(backlog.map((assignment) => assignment.recordId))).toEqual(new Set([deferred]));

  const gaps = (await coord.draw({ runId: "cycle_1", now: NOW, seed: 1n })).gaps;
  expect(gaps).toContainEqual({
    recordId: proposal,
    role: "backlog",
    reason: "unsupported",
    detail: "deferred, and only a hypothesis is worked from the backlog",
  });
});

test("a live claim withholds its own role and nothing else", async () => {
  const { db, coord } = await deployment({ enabled: true, batchSize: 8 });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");

  const first = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 7n }));
  const granted = await coord.claim({ assignment: first, runId: "cycle_1", now: NOW });
  expect(granted.outcome).toBe("granted");

  const draws = await sampleDraws(coord, 40);
  expect(draws.every((assignment) => assignment.role !== first.role)).toBe(true);
  const result = await coord.draw({ runId: "cycle_1", now: NOW, seed: 7n });
  expect(result.gaps).toContainEqual({
    recordId: id,
    role: first.role,
    reason: "claimed",
    detail: "already claimed by a live worker",
  });
});

// ---------------------------------------------------------------------------- claims

async function oneAssignment(): Promise<{
  db: GuestDatabase;
  coord: Coordinator;
  assignment: Assignment;
}> {
  const { db, coord } = await deployment({ enabled: true });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const assignment = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n }));
  return { db, coord, assignment };
}

test("a claim is granted once, re-delivered to its own holder, and refused to anyone else", async () => {
  const { coord, assignment } = await oneAssignment();

  const first = await coord.claim({ assignment, runId: "run_a", jobId: "job_1", now: NOW });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  expect(first.claim.fence).toBe(1);
  expect(first.claim.jobId).toBe("job_1");
  expect(first.claim.expiresAt).toBe(NOW + 900_000);
  expect(first.claim.reservedCost).toBeCloseTo(0.0625, 10);

  // The same worker asking again gets back what it holds: a retry after a lost answer must not
  // advance the fence or reserve a second time.
  const retry = await coord.claim({ assignment, runId: "run_a", now: NOW + 1000 });
  if (retry.outcome !== "granted") throw new Error(retry.refusal.detail);
  expect(retry.claim.fence).toBe(1);
  expect(retry.claim.expiresAt).toBe(first.claim.expiresAt);

  const other = await coord.claim({ assignment, runId: "run_b", now: NOW + 1000 });
  expect(other.outcome).toBe("refused");
  if (other.outcome !== "refused") throw new Error("unreachable");
  expect(other.refusal.reason).toBe("conflict");
});

test("a posted Code job binds only to the live fenced claim", async () => {
  const { coord, assignment } = await oneAssignment();
  const granted = await coord.claim({ assignment, runId: "run_a", now: NOW });
  if (granted.outcome !== "granted") throw new Error(granted.refusal.detail);
  expect(granted.claim.jobId).toBeNull();

  const bound = await coord.bind({
    id: assignment.id,
    runId: "run_a",
    fence: granted.claim.fence,
    jobId: "job_review",
  });
  if (bound.outcome !== "bound") throw new Error(bound.refusal.detail);
  expect(bound.claim.jobId).toBe("job_review");

  const repeated = await coord.bind({
    id: assignment.id,
    runId: "run_a",
    fence: granted.claim.fence,
    jobId: "job_review",
  });
  expect(repeated.outcome).toBe("bound");

  const replaced = await coord.bind({
    id: assignment.id,
    runId: "run_a",
    fence: granted.claim.fence,
    jobId: "job_other",
  });
  if (replaced.outcome !== "refused") throw new Error("a bound claim changed jobs");
  expect(replaced.refusal.reason).toBe("conflict");
  const stale = await coord.bind({
    id: assignment.id,
    runId: "run_b",
    fence: granted.claim.fence + 1,
    jobId: "job_stale",
  });
  if (stale.outcome !== "refused") throw new Error("a stale holder bound the claim");
  expect(stale.refusal.reason).toBe("taken-over");
});

test("renewal moves the lease forward only, and is refused after expiry or under another fence", async () => {
  const { coord, assignment } = await oneAssignment();
  const granted = await coord.claim({ assignment, runId: "run_a", now: NOW });
  if (granted.outcome !== "granted") throw new Error(granted.refusal.detail);

  const early = await coord.renew({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    now: NOW + 60_000,
  });
  if (early.outcome !== "renewed") throw new Error(early.refusal.detail);
  expect(early.expiresAt).toBe(NOW + 60_000 + 900_000);

  // The expiry never moves backwards: a renewal is the holder keeping the authority it has.
  const backwards = await coord.renew({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    now: NOW + 1000,
  });
  if (backwards.outcome !== "renewed") throw new Error(backwards.refusal.detail);
  expect(backwards.expiresAt).toBe(early.expiresAt);

  const wrongFence = await coord.renew({
    id: assignment.id,
    runId: "run_a",
    fence: 2,
    now: NOW + 1000,
  });
  if (wrongFence.outcome !== "refused") throw new Error("a superseded fence renewed a lease");
  expect(wrongFence.refusal.reason).toBe("taken-over");

  const lapsed = await coord.renew({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    now: early.expiresAt + 1000,
  });
  if (lapsed.outcome !== "refused") throw new Error("an expired lease was resurrected");
  expect(lapsed.refusal.reason).toBe("expired");
});

test("a takeover fences the stale holder and keeps its reservation charged", async () => {
  const { coord, assignment } = await oneAssignment();
  const first = await coord.claim({ assignment, runId: "run_a", now: NOW });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);

  const after = first.claim.expiresAt + 1000;
  const second = await coord.claim({ assignment, runId: "run_b", now: after });
  if (second.outcome !== "granted") throw new Error(second.refusal.detail);
  expect(second.claim.fence).toBe(2);
  expect(second.claim.runId).toBe("run_b");

  // The stale holder's result belongs to a superseded epoch; its spend is already charged there.
  const stale = await coord.finish({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    cost: 0.05,
    outcome: "completed",
    now: after + 1000,
  });
  if (stale.outcome !== "refused") throw new Error("a fenced holder finished the claim");
  expect(stale.refusal.reason).toBe("taken-over");

  const live = await coord.finish({
    id: assignment.id,
    runId: "run_b",
    fence: 2,
    cost: 0.01,
    outcome: "completed",
    now: after + 2000,
  });
  if (live.outcome !== "finished") throw new Error(live.refusal.detail);
  expect(live.overrun).toBe(false);

  // An abandoned attempt may have burned its whole reservation before the machine died, so the
  // day carries it in full beside what the new holder actually spent.
  const spend = await coord.spend(after + 2000);
  expect(spend.total).toBeCloseTo(assignment.reservedCost + 0.01, 10);
  expect(spend.byRun["run_a"]).toBeCloseTo(assignment.reservedCost, 10);
  expect(spend.byRun["run_b"]).toBeCloseTo(0.01, 10);
});

test("a finish reconciles the reservation, reports an overrun, and accepts only the identical retry", async () => {
  const { coord, assignment } = await oneAssignment();
  const granted = await coord.claim({ assignment, runId: "run_a", now: NOW });
  if (granted.outcome !== "granted") throw new Error(granted.refusal.detail);

  // A lapsed lease does not refuse a finish: that spend really happened and that work really
  // exists.
  const lapsed = granted.claim.expiresAt + 60_000;
  const first = await coord.finish({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    cost: 0.2,
    outcome: "completed",
    now: lapsed,
  });
  if (first.outcome !== "finished") throw new Error(first.refusal.detail);
  expect(first.reserved).toBeCloseTo(0.0625, 10);
  expect(first.overrun).toBe(true);

  const identical = await coord.finish({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    cost: 0.2,
    outcome: "completed",
    now: lapsed + 1000,
  });
  if (identical.outcome !== "finished") throw new Error(identical.refusal.detail);
  // An overspend that stopped being reported on the second call would be one a caller can retry
  // its way out of.
  expect(identical.overrun).toBe(true);

  const different = await coord.finish({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    cost: 0.3,
    outcome: "completed",
    now: lapsed + 2000,
  });
  if (different.outcome !== "refused") throw new Error("a second receipt was accepted");
  expect(different.refusal.reason).toBe("finished");

  // The day is charged what was spent rather than what was reserved.
  const spend = await coord.spend(lapsed);
  expect(spend.total).toBeCloseTo(0.2, 10);
});

test("a fence read back out of the database settles the claim it names, bigint or not", async () => {
  // Every caller reads the fence out of a query of its own — the loop's settlement, its reaper,
  // the stop door — and the engine's database answers an INTEGER column with a BIGINT. Compared
  // strictly against this store's number it refused the caller its own claim, and refused it
  // silently: on 2026-09-13 that is a run that reads `stopped` with its batch slot still held.
  const first = await oneAssignment();
  const heldA = await first.coord.claim({
    assignment: first.assignment,
    runId: "run_a",
    jobId: "job_a",
    now: NOW,
  });
  if (heldA.outcome !== "granted") throw new Error(heldA.refusal.detail);
  const finished = await first.coord.finish({
    id: first.assignment.id,
    runId: "run_a",
    fence: 1n,
    cost: 0,
    outcome: "skipped",
    now: NOW,
  });
  if (finished.outcome !== "finished") throw new Error(finished.refusal.detail);
  expect(
    (await first.db.query(`SELECT outcome FROM claims WHERE id = ?`, [first.assignment.id]))[0],
  ).toEqual({ outcome: "skipped" });

  const second = await oneAssignment();
  const heldB = await second.coord.claim({
    assignment: second.assignment,
    runId: "run_b",
    jobId: "job_b",
    now: NOW,
  });
  if (heldB.outcome !== "granted") throw new Error(heldB.refusal.detail);
  const abandoned = await second.coord.abandon({
    id: second.assignment.id,
    fence: 1n,
    reason: "job_b was killed",
    now: NOW,
  });
  expect(abandoned.outcome).toBe("abandoned");

  // And a bigint that names another epoch is still refused: the coercion normalizes the shape,
  // never the value.
  const stale = await second.coord.abandon({
    id: second.assignment.id,
    fence: 2n,
    reason: "a fence that is not this one",
    now: NOW,
  });
  if (stale.outcome !== "refused") throw new Error("a fence that had moved was accepted");
  expect(stale.refusal.reason).toBe("finished");
});

test("an abandoned claim is finished at what it reserved, and only its own live epoch is", async () => {
  const { db, coord, assignment } = await oneAssignment();
  const granted = await coord.claim({ assignment, runId: "run_a", jobId: "job_a", now: NOW });
  if (granted.outcome !== "granted") throw new Error(granted.refusal.detail);

  const abandoned = await coord.abandon({
    id: assignment.id,
    fence: 1,
    reason: "the hub cancelled job_a",
    now: NOW + 60_000,
  });
  expect(abandoned).toEqual({
    outcome: "abandoned",
    cost: granted.claim.reservedCost,
    reason: "the hub cancelled job_a",
  });

  // The row is closed, and closed at the reservation: a job that died mid-review may have spent
  // all of it and cannot say, so the day keeps the charge.
  const row = await db.query<{ outcome: string; actual_cost: number; finished_at: string | null }>(
    `SELECT outcome, actual_cost, finished_at FROM claims WHERE id = ?`,
    [assignment.id],
  );
  expect(row[0]?.outcome).toBe("abandoned");
  expect(row[0]?.actual_cost).toBeCloseTo(granted.claim.reservedCost, 10);
  expect(row[0]?.finished_at).toBe(new Date(NOW + 60_000).toISOString());
  expect((await coord.spend(NOW)).total).toBeCloseTo(granted.claim.reservedCost, 10);

  // A second abandonment does not charge the day twice.
  const again = await coord.abandon({ id: assignment.id, fence: 1, reason: "again", now: NOW });
  if (again.outcome !== "refused") throw new Error("a finished claim was abandoned twice");
  expect(again.refusal.reason).toBe("finished");
  expect((await coord.spend(NOW)).total).toBeCloseTo(granted.claim.reservedCost, 10);

  const missing = await coord.abandon({ id: "asg_nothing", fence: 1, reason: "reaped", now: NOW });
  if (missing.outcome !== "refused") throw new Error("a claim that does not exist was abandoned");
  expect(missing.refusal.reason).toBe("not-found");
});

test("abandoning a stale epoch never closes the claim its successor holds", async () => {
  const { coord, assignment } = await oneAssignment();
  const first = await coord.claim({ assignment, runId: "run_a", jobId: "job_a", now: NOW });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  const later = first.claim.expiresAt + 1000;
  const second = await coord.claim({ assignment, runId: "run_b", jobId: "job_b", now: later });
  if (second.outcome !== "granted") throw new Error(second.refusal.detail);
  expect(second.claim.fence).toBe(2);

  // The reaper catches up with the dead first job after the takeover: its epoch is gone, and
  // closing the live successor in its name would abandon a review that is running.
  const stale = await coord.abandon({
    id: assignment.id,
    fence: 1,
    reason: "job_a was never reported again",
    now: later + 1000,
  });
  if (stale.outcome !== "refused") throw new Error("a stale epoch closed the live claim");
  expect(stale.refusal.reason).toBe("taken-over");
  const held = await coord.renew({
    id: assignment.id,
    runId: "run_b",
    fence: 2,
    now: later + 2000,
  });
  expect(held.outcome).toBe("renewed");
});

test("a batch is held by claims with a job, and abandoning four dead ones admits the next draw", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    batchSize: 4,
    perCycleCost: 0.4,
    dailyCost: 5,
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  for (let n = 1; n <= 4; n += 1) {
    await claimRow(db, `asg_ghost${String(n)}`, "cycle_dead", 0.1, null, 0, `job_${String(n)}`);
  }

  // Four jobs the operator killed: their leases run to 14:15Z, and until #259 that is how long
  // the deployment reviewed nothing.
  const blocked = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n });
  if (blocked.outcome !== "gap") throw new Error("a full batch drew work");
  expect(blocked.gap.reason).toBe("batch");

  for (let n = 1; n <= 4; n += 1) {
    const abandoned = await coord.abandon({
      id: `asg_ghost${String(n)}`,
      fence: 1,
      reason: `job_${String(n)} was killed`,
      now: NOW,
    });
    expect(abandoned.outcome).toBe("abandoned");
  }

  expect(drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n })).recordId).toBe(id);
  // And what they reserved is still charged to the day, so a crash loop cannot spend it twice.
  expect((await coord.spend(NOW)).total).toBeCloseTo(0.4, 10);
});

test("an abandoned review drawn again is granted at the next fence, its dead epoch charged once", async () => {
  // Reviews only: an analysis assignment names itself by its retry, not by an ordinal.
  const { db, coord } = await deployment({
    enabled: true,
    activityWeights: { review: 1, explore: 0, challenge: 0, synthesize: 0 },
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const assignment = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n }));
  expect(assignment.activity).toBe("review");
  const first = await coord.claim({ assignment, runId: "run_a", jobId: "job_a", now: NOW });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  const abandoned = await coord.abandon({
    id: assignment.id,
    fence: 1,
    reason: "job_a died",
    now: NOW,
  });
  expect(abandoned.outcome).toBe("abandoned");

  // The abandonment withholds nothing, so a later draw offers the same record and role — under
  // the same assignment id. Refusing that as finished stopped every cycle that drew it first.
  let again: Assignment | undefined;
  for (let seed = 0n; seed < 64n && again === undefined; seed += 1n) {
    const offered = drawn(await coord.draw({ runId: "cycle_2", now: NOW + 1000, seed }));
    if (offered.role === assignment.role) again = offered;
  }
  if (again === undefined) throw new Error(`no draw offered the ${assignment.role} role again`);
  expect(again.id).toBe(assignment.id);
  // Two workers reaching for the reopened epoch get one winner and one conflict.
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  const raced = await Promise.all([
    coord.claim({ assignment: again, runId: "run_b", jobId: "job_b", now: NOW + 1000 }),
    other.claim({ assignment: again, runId: "run_x", jobId: "job_x", now: NOW + 1000 }),
  ]);
  expect(raced.map((result) => result.outcome).sort()).toEqual(["granted", "refused"]);
  const loser = raced.find((result) => result.outcome === "refused");
  expect(loser?.outcome === "refused" ? loser.refusal.reason : null).toBe("conflict");
  const second = raced.find((result) => result.outcome === "granted");
  if (second?.outcome !== "granted") throw new Error("no worker was granted the reopened epoch");
  expect(second.claim.fence).toBe(2);
  const winner = second.claim.runId;

  // The dead epoch keeps its charge on its own row and cannot report into the new one.
  const archived = await db.query<{ outcome: string; run_id: string }>(
    `SELECT outcome, run_id FROM claims WHERE id = ?`,
    [`${assignment.id}~1`],
  );
  expect(archived).toEqual([{ outcome: "abandoned", run_id: "run_a" }]);
  const stale = await coord.finish({
    id: assignment.id,
    runId: "run_a",
    fence: 1,
    cost: 0.01,
    outcome: "completed",
    now: NOW + 2000,
  });
  expect(stale.outcome).toBe("refused");
  const spend = await coord.spend(NOW + 2000);
  expect(spend.byRun["run_a"]).toBeCloseTo(assignment.reservedCost, 10);
  expect(spend.total).toBeCloseTo(2 * assignment.reservedCost, 10);

  // A claim that finished any other way is still finished.
  const done = await coord.finish({
    id: assignment.id,
    runId: winner,
    fence: 2,
    cost: 0.01,
    outcome: "completed",
    now: NOW + 3000,
  });
  expect(done.outcome).toBe("finished");
  const third = await coord.claim({ assignment: again, runId: "run_c", now: NOW + 4000 });
  if (third.outcome !== "refused") throw new Error("a completed assignment was granted again");
  expect(third.refusal.reason).toBe("finished");
});

test("a grant whose job was never posted holds no batch slot", async () => {
  const { db, coord } = await deployment({ enabled: true, batchSize: 1, perCycleCost: 0.4 });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");

  // `claim` reserves before the conductor posts the job, so a refused posting leaves this: a
  // grant with no worker. It is the reaper's to release, and never a reason to refuse a draw.
  await claimRow(db, "asg_unposted", "cycle_dead", 0.1, null, 0, null);
  expect(drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n })).recordId).toBe(id);

  // The same row with a job behind it is work in progress, and does hold the batch.
  await claimRow(db, "asg_running", "cycle_dead", 0.1, null, 0, "job_running");
  const blocked = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n });
  if (blocked.outcome !== "gap") throw new Error("a held batch drew work");
  expect(blocked.gap.reason).toBe("batch");
});

test("the per-cycle ceiling bounds one cycle's whole day and the daily ceiling bounds the rest", async () => {
  const { db, coord } = await deployment({ enabled: true, perCycleCost: 0.1, dailyCost: 0.2 });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const assignment = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 5n }));
  expect(assignment.reservedCost).toBeCloseTo(0.025, 10);

  // One cycle has already spent its whole ceiling; the day has room for another cycle.
  await claimRow(db, "asg_cycle1_spent", "cycle_1", 0.1, 0.1);
  const refused = await coord.claim({ assignment, runId: "cycle_1", now: NOW });
  if (refused.outcome !== "refused") throw new Error("the per-cycle ceiling did not bite");
  expect(refused.refusal.reason).toBe("budget");
  expect(refused.refusal.detail).toContain("a cycle may spend");

  const granted = await coord.claim({ assignment, runId: "cycle_2", now: NOW });
  expect(granted.outcome).toBe("granted");

  // And the draw refuses the exhausted cycle before it builds a single candidate.
  const stop = await coord.draw({ runId: "cycle_1", now: NOW });
  if (stop.outcome !== "gap") throw new Error("an exhausted cycle drew work");
  expect(stop.gap.reason).toBe("per-cycle");
});

test("the assignment id is the work, not the worker, so two conductors contend for one claim", async () => {
  const { db, coord, assignment } = await oneAssignment();
  // Two conductors are two processes: they share the ledger and nothing else, so neither can see
  // what the other has drawn and not yet claimed. One store, two coordinators, one seed.
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  const again = drawn(await other.draw({ runId: "cycle_2", now: NOW, seed: 3n }));
  expect(again.id).toBe(assignment.id);

  const first = await coord.claim({ assignment, runId: "cycle_1", now: NOW });
  const second = await other.claim({ assignment: again, runId: "cycle_2", now: NOW });
  expect(first.outcome).toBe("granted");
  expect(second.outcome).toBe("refused");
});

test("a draw is a pure function of its seed and reserves nothing", async () => {
  const { db, coord, assignment } = await oneAssignment();
  const repeat = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n }));
  expect(repeat).toEqual(assignment);
  expect(assignment.seed).toBe("3");
  expect(assignment.inputDigest).toBe(repeat.inputDigest);

  const rows = await db.query(`SELECT COUNT(*) AS claims FROM claims`);
  expect(rows[0]?.["claims"]).toBe(0n);
  expect((await coord.spend(NOW)).total).toBe(0);
});

test("concurrent draws over a stocked frontier hand out distinct assignments and no conflicts", async () => {
  const { db, coord } = await deployment({ enabled: true });
  // Twelve records, four review roles each: far more eligible work than there are workers. The
  // coverage share reserves half of every cycle for the oldest due, which is ONE deterministic
  // head, and that is what every worker drawing in the same window used to reach for (#233).
  for (let n = 1; n <= 12; n += 1) {
    const id = await record(db, `hyp_${n.toString(16).padStart(8, "0")}`, "hypothesis", 40 - n);
    await filing(db, id, "ent_0000000a");
  }
  await fact(db, "ent_0000000a", "lifecycle", "active");

  const workers = 6;
  const drawn6 = await Promise.all(
    Array.from({ length: workers }, async (_slot, index) => {
      const runId = `cycle_${String(index + 1)}`;
      const assignment = drawn(await coord.draw({ runId, now: NOW }));
      return { assignment, claimed: await coord.claim({ assignment, runId, now: NOW }) };
    }),
  );

  expect(new Set(drawn6.map((worker) => worker.assignment.id)).size).toBe(workers);
  expect(
    drawn6
      .filter((worker) => worker.claimed.outcome === "refused")
      .map((worker) => worker.assignment.id),
  ).toEqual([]);
});

test("an assignment the ledger already holds is stepped over rather than handed out again", async () => {
  const { db, coord } = await deployment({ enabled: true });
  for (let n = 1; n <= 3; n += 1) {
    const id = await record(db, `hyp_${n.toString(16).padStart(8, "0")}`, "hypothesis", 40 - n);
    await filing(db, id, "ent_0000000a");
  }
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const head = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 4n }));

  // The ledger holds that assignment and the candidate scan does not account for it: the state
  // every draw is in when a claim lands after its own scan of the claims table, which over
  // several thousand records is most of the draw. A second process, so nothing but the ledger
  // tells it the head is taken.
  await claimRow(db, head.id, "run_other", 0.01, null);
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  const next = await other.draw({ runId: "cycle_2", now: NOW, seed: 4n });
  const stepped = drawn(next);
  expect(stepped.id).not.toBe(head.id);
  expect(next.gaps).toContainEqual({
    recordId: head.recordId,
    role: head.role,
    reason: "claimed",
    detail: "claimed by another worker while this draw was reading its candidates",
  });
  expect((await other.claim({ assignment: stepped, runId: "cycle_2", now: NOW })).outcome).toBe(
    "granted",
  );
});

test("two processes reaching for the last eligible review get one winner and one conflict", async () => {
  const { db, coord } = await deployment({ enabled: true });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  await reviewedOnce(db, id);
  // Only the reception role is left to draw, so re-selection has nowhere to go: the refusal has
  // to reach the caller, because a draw that quietly retried until something was free would
  // leave a cycle unable to say it found nothing.
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  const [mine, theirs] = await Promise.all([
    coord.draw({ runId: "cycle_1", now: NOW, seed: 9n }),
    other.draw({ runId: "cycle_2", now: NOW, seed: 9n }),
  ]);
  const first = drawn(mine);
  const second = drawn(theirs);
  expect(second.id).toBe(first.id);

  const claims = await Promise.all([
    coord.claim({ assignment: first, runId: "cycle_1", now: NOW }),
    other.claim({ assignment: second, runId: "cycle_2", now: NOW }),
  ]);
  expect(claims.map((result) => result.outcome).sort()).toEqual(["granted", "refused"]);
  const loser = claims.find((result) => result.outcome === "refused");
  expect(loser?.outcome === "refused" ? loser.refusal.reason : null).toBe("conflict");
});

test("a corpus larger than one page is read whole: the first page and the last both draw", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    coverageShare: 0.5,
    discoveryShare: 0.04,
    explorationShare: 0.05,
    filingShare: 0.01,
    backlogShare: 0.4,
  });
  // More records than one scan reads per call, so a paging bug drops the tail of the corpus and
  // the deployment silently stops seeing half its own frontier.
  const size = 2100;
  await db.run(
    `INSERT INTO records(id, kind, root_id, seq, actor_kind, actor_id, title, created_at, payload)
     WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
     SELECT printf('hyp_%08x', i), 'hypothesis', printf('hyp_%08x', i), 0, 'run', 'run_seed',
            'bulk', strftime('%Y-%m-%dT%H:%M:%fZ', ?, printf('-%d seconds', ? - i)), '{}'
       FROM n`,
    [size, new Date(NOW).toISOString(), size],
  );
  await db.run(
    `INSERT INTO filings(id, record_id, entity_id, rationale, author_kind, author_id, heuristic,
       withdrawn, created_at)
     SELECT 'fil_' || r.id, r.id, 'ent_0000000a', 'bulk', 'run', 'run_seed', 0, 0, r.created_at
       FROM records r`,
  );
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const oldest = "hyp_00000001";
  const newest = `hyp_${size.toString(16).padStart(8, "0")}`;
  await status(db, newest, "deferred", 1);

  const draws = await sampleDraws(coord, 12);
  expect(draws.length).toBe(12);
  const backlog = draws.filter((assignment) => assignment.lane === "backlog");
  expect(backlog.length).toBeGreaterThan(0);
  // The only deferred candidate is on the last page…
  expect(backlog.every((assignment) => assignment.recordId === newest)).toBe(true);
  // …and the reserved coverage lane draws the oldest due across the WHOLE corpus, which is the
  // first row of the first page.
  expect(draws.some((assignment) => assignment.recordId === oldest)).toBe(true);
});

// ---------------------------------------------------------------------------- the budget overlay

/** One `budgets` row, as the `setBudget` act writes one: what it moves and when it stops. */
async function overlay(
  db: GuestDatabase,
  id: string,
  moves: {
    readonly expiresAt: number;
    readonly createdAt?: number;
    readonly perCycleCost?: number;
    readonly dailyCost?: number;
    readonly concurrentPerMachine?: number;
    readonly clearedAt?: number;
  },
): Promise<void> {
  await db.run(
    `INSERT INTO budgets(id, created_at, expires_at, per_cycle_cost, daily_cost,
                         concurrent_per_machine, reason, cleared_at)
     VALUES(?,?,?,?,?,?,?,?)`,
    [
      id,
      new Date(moves.createdAt ?? NOW - 1000).toISOString(),
      new Date(moves.expiresAt).toISOString(),
      moves.perCycleCost ?? null,
      moves.dailyCost ?? null,
      moves.concurrentPerMachine ?? null,
      "a drain",
      moves.clearedAt === undefined ? null : new Date(moves.clearedAt).toISOString(),
    ],
  );
}

/** The run row that binds a claim's job to a machine; `openClaims` reads the machine off it. */
async function runOn(db: GuestDatabase, jobId: string, machineId: string): Promise<void> {
  await db.run(
    `INSERT INTO runs(id, kind, machine_id, job_id, started_at, records, payload)
     VALUES(?, 'atyrode.babel.evaluate', ?, ?, ?, 0, '{}')`,
    [`run_${jobId}`, machineId, jobId, new Date(NOW).toISOString()],
  );
}

test("an overlay moves the bound and the ceilings while it lasts, and nothing when it has expired", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    batchSize: 1,
    perCycleCost: 0.1,
    dailyCost: 0.2,
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  // One slot, and it is held: the standing policy draws nothing.
  await claimRow(db, "asg_open", "cycle_1", 0.01, null, 0, "job_open");
  const standing = await coord.draw({ runId: "cycle_1", now: NOW });
  if (standing.outcome !== "gap") throw new Error("a full batch drew work");
  expect(standing.gap.reason).toBe("batch");

  await overlay(db, "bdg_drain", {
    expiresAt: NOW + 600_000,
    concurrentPerMachine: 4,
    perCycleCost: 1,
    dailyCost: 2,
  });
  const inForce = await coord.policy(NOW);
  expect(inForce.overlay?.id).toBe("bdg_drain");
  // What admission is judged by is the overlaid number; what a draw is replayable against — the
  // version, the lease, the shares — is the standing row, untouched. The bound carries the batch
  // with it, so one review still reserves one bound's worth of the cycle's allowance.
  expect(inForce.policy.concurrentPerMachine).toBe(4);
  expect(inForce.policy.batchSize).toBe(4);
  expect(inForce.standing.batchSize).toBe(1);
  expect(inForce.standing.concurrentPerMachine).toBeUndefined();
  expect(inForce.policy.leaseSeconds).toBe(inForce.standing.leaseSeconds);
  expect(inForce.policy.version).toBe(inForce.standing.version);
  expect(drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n })).recordId).toBe(id);

  // NOBODY UNWINDS IT. The same store, ten minutes later, admits what the standing policy admits.
  const later = NOW + 601_000;
  expect((await coord.policy(later)).overlay).toBeNull();
  expect((await coord.policy(later)).policy.batchSize).toBe(1);
  const expired = await coord.draw({ runId: "cycle_1", now: later });
  if (expired.outcome !== "gap") throw new Error("an expired overlay still admitted a draw");
  expect(expired.gap.reason).toBe("batch");
});

test("a cleared overlay stops applying, and the one it covered applies again for what is left of its own TTL", async () => {
  const { db, coord } = await deployment({ enabled: true, batchSize: 2 });
  await overlay(db, "bdg_first", {
    createdAt: NOW - 5000,
    expiresAt: NOW + 600_000,
    concurrentPerMachine: 8,
  });
  await overlay(db, "bdg_second", {
    createdAt: NOW - 1000,
    expiresAt: NOW + 60_000,
    concurrentPerMachine: 16,
    clearedAt: NOW,
  });
  expect((await coord.policy(NOW)).policy.batchSize).toBe(8);

  await db.run(`UPDATE budgets SET cleared_at = ? WHERE id = 'bdg_first'`, [
    new Date(NOW).toISOString(),
  ]);
  expect((await coord.policy(NOW)).policy.batchSize).toBe(2);
});

test("an in-flight assignment's id is identical before and after an overlay is set", async () => {
  const { db, coord, assignment } = await oneAssignment();

  await overlay(db, "bdg_drain", {
    expiresAt: NOW + 3_600_000,
    concurrentPerMachine: 8,
    perCycleCost: 1,
    dailyCost: 2,
  });

  // THE WHOLE OF F5. A policy rewrite mid-cycle re-digested `policyVersion` and minted a second
  // id for this very review, so one vote had two live claims and the ghost of the first held a
  // batch slot for the rest of its lease. The same draw under an eight-times batch is the same
  // assignment, named identically, and only its reservation moves.
  const again = drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n }));
  expect(again.id).toBe(assignment.id);
  expect(again.policyVersion).toBe(assignment.policyVersion);
  expect(again.reservedCost).not.toBe(assignment.reservedCost);

  // And the claim taken BEFORE the overlay is the one the assignment still names: a second
  // claimer is refused the conflict, and its own holder may still renew and finish it.
  const claimed = await coord.claim({ assignment, runId: "run_a", jobId: "job_a", now: NOW });
  if (claimed.outcome !== "granted") throw new Error(claimed.refusal.detail);
  const contended = await coord.claim({ assignment: again, runId: "run_b", now: NOW + 1000 });
  if (contended.outcome !== "refused") throw new Error("an overlay minted a second claim");
  expect(contended.refusal.reason).toBe("conflict");
  const renewed = await coord.renew({
    id: again.id,
    runId: "run_a",
    fence: claimed.claim.fence,
    now: NOW + 2000,
  });
  expect(renewed.outcome).toBe("renewed");
});

test("the batch is per machine: two machines hold four, the fifth draw is refused naming them, and a settlement admits the next", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    batchSize: 8,
    concurrentPerMachine: 2,
    perCycleCost: 4,
    dailyCost: 8,
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  const fleet = ["dev-01", "dev-02"];

  for (const [n, machine] of ["dev-01", "dev-01", "dev-02", "dev-02"].entries()) {
    const jobId = `job_${String(n)}`;
    await claimRow(db, `asg_live${String(n)}`, "cycle_dead", 0.1, null, 0, jobId);
    await runOn(db, jobId, machine);
  }
  expect(await coord.open(NOW)).toEqual({ total: 4, byMachine: { "dev-01": 2, "dev-02": 2 } });

  const full = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: fleet });
  if (full.outcome !== "gap") throw new Error("a full fleet drew a fifth assignment");
  expect(full.gap.reason).toBe("batch");
  // Naming WHERE, which is what seven hundred "the cycle batch is already claimed" lines never did.
  expect(full.gap.detail).toContain("dev-01 holds 2");
  expect(full.gap.detail).toContain("dev-02 holds 2");

  // One machine's claim settles…
  const settled = await coord.finish({
    id: "asg_live0",
    runId: "cycle_dead",
    fence: 1,
    cost: 0.1,
    outcome: "completed",
    now: NOW,
  });
  expect(settled.outcome).toBe("finished");
  expect(
    drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: fleet })).recordId,
  ).toBe(id);

  // …and a caller that cannot say where the work would run is judged against ONE machine's
  // worth, so a draw with no fleet named never claims the fan a fleet would allow.
  const alone = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n });
  if (alone.outcome !== "gap")
    throw new Error("an unnamed fleet drew against the whole deployment");
  expect(alone.gap.reason).toBe("batch");
});

test("a machine that has gone offline holds no slot the online fleet could use", async () => {
  // #281/3: the cap is the bound over the machines this cycle found online, so two claims held
  // by a host that dropped off do not freeze the one still running. Counting them would idle
  // the fleet until their lease ran out — fifteen minutes by default, eighty-six under a
  // drain-sized one — and the refusal would name hosts holding nothing.
  const { db, coord } = await deployment({
    enabled: true,
    batchSize: 8,
    concurrentPerMachine: 2,
    perCycleCost: 4,
    dailyCost: 8,
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  for (const n of [0, 1]) {
    const jobId = `job_gone${String(n)}`;
    await claimRow(db, `asg_gone${String(n)}`, "cycle_dead", 0.1, null, 0, jobId);
    await runOn(db, jobId, "dev-03");
  }
  expect(await coord.open(NOW)).toEqual({ total: 2, byMachine: { "dev-03": 2 } });

  const admitted = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: ["dev-01"] });
  expect(drawn(admitted).recordId).toBe(id);

  // What the ONLINE fleet holds still bounds it, and the refusal names only what it holds.
  for (const n of [0, 1]) {
    const jobId = `job_here${String(n)}`;
    await claimRow(db, `asg_here${String(n)}`, "cycle_dead", 0.1, null, 0, jobId);
    await runOn(db, jobId, "dev-01");
  }
  const full = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: ["dev-01"] });
  if (full.outcome !== "gap") throw new Error("a machine at its bound took a third job");
  expect(full.gap.detail).toContain("dev-01 holds 2");
  expect(full.gap.detail).not.toContain("dev-03");
});

test("an overlay that raises the per-machine bound raises the fleet's cap with it", async () => {
  const { db, coord } = await deployment({
    enabled: true,
    batchSize: 8,
    concurrentPerMachine: 1,
    perCycleCost: 4,
    dailyCost: 8,
  });
  const id = await record(db, "hyp_00000001", "hypothesis", 40);
  await filing(db, id, "ent_0000000a");
  await fact(db, "ent_0000000a", "lifecycle", "active");
  await claimRow(db, "asg_live", "cycle_dead", 0.1, null, 0, "job_live");
  await runOn(db, "job_live", "dev-01");

  const bounded = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: ["dev-01"] });
  if (bounded.outcome !== "gap") throw new Error("a machine at its bound took a second job");
  expect(bounded.gap.reason).toBe("batch");

  await overlay(db, "bdg_drain", { expiresAt: NOW + 600_000, concurrentPerMachine: 4 });
  expect(
    drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: ["dev-01"] }))
      .recordId,
  ).toBe(id);
});

test("the overlay's own validator refuses what a policy being installed would be refused for", async () => {
  const standing: Policy = { ...DEFAULT_POLICY, enabled: true, leaseSeconds: 900, batchSize: 4 };
  const drain = {
    id: "bdg_1",
    createdAt: NOW,
    expiresAt: NOW + 3_600_000,
    perCycleCost: null,
    dailyCost: null,
    concurrentPerMachine: null,
    reason: "a drain",
  };
  // An overlay that moves nothing is not one; the CHECK in the table says the same thing.
  expect(validateBudget(standing, drain, CONCURRENT_JOBS)).toContain("moves no number");
  // A lease is the standing policy's and an overlay may not move it, so a bound the lease cannot
  // cover is refused here rather than discovered as expired claims.
  const short: Policy = { ...standing, leaseSeconds: 300 };
  expect(validateBudget(short, { ...drain, concurrentPerMachine: 16 }, CONCURRENT_JOBS)).toContain(
    "320s",
  );
  // The standing rules, judged against the policy the overlay would produce.
  expect(validateBudget(standing, { ...drain, dailyCost: 0.1 }, CONCURRENT_JOBS)).toContain(
    "below the per-cycle cost",
  );
  expect(validateBudget(standing, { ...drain, perCycleCost: 0 }, CONCURRENT_JOBS)).toContain(
    "must be positive",
  );
  expect(
    validateBudget(
      standing,
      { ...drain, expiresAt: NOW, concurrentPerMachine: 8 },
      CONCURRENT_JOBS,
    ),
  ).toContain("no time at all");
  expect(
    validateBudget(
      standing,
      { ...drain, concurrentPerMachine: 8, perCycleCost: 1, dailyCost: 2 },
      CONCURRENT_JOBS,
    ),
  ).toBeNull();

  // And what it produces is the standing policy with those numbers and nothing else moved: the
  // bound carries the batch, because that is what one review reserves against the cycle.
  const overlaid = applyBudget(standing, { ...drain, concurrentPerMachine: 8, dailyCost: 9 });
  expect(overlaid).toEqual({ ...standing, batchSize: 8, concurrentPerMachine: 8, dailyCost: 9 });
  expect(budgetChanges(standing, { ...drain, concurrentPerMachine: 8, dailyCost: 9 })).toEqual([
    { field: "dailyCost", standing: 2, overlaid: 9 },
    { field: "concurrentPerMachine", standing: 4, overlaid: 8 },
  ]);

  // THE INERT OVERLAY (#281/2). Admission reads the per-machine bound, so the number an overlay
  // names is compared against the bound in force — never against a batch nothing consults,
  // which is how an overlay could once report `Batch 4 → 16` and admit not one more draw.
  const bounded: Policy = { ...standing, concurrentPerMachine: 2 };
  expect(validateBudget(bounded, { ...drain, concurrentPerMachine: 2 }, CONCURRENT_JOBS)).toContain(
    "moves no number",
  );
  expect(budgetChanges(bounded, { ...drain, concurrentPerMachine: 16 })).toEqual([
    { field: "concurrentPerMachine", standing: 2, overlaid: 16 },
  ]);
});

// ---------------------------------------------------------------------------- weighted analysis

function stagePolicy(stage: Stage, over: Partial<Policy> = {}): Policy {
  return PolicySchema.parse({
    ...DEFAULT_POLICY,
    enabled: true,
    activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0, [stage]: 1 },
    review: {
      machineId: "dev-01",
      profile: { containerId: "ctr_stage", expectedRevision: 1 },
      roleRecipes: Object.fromEntries(ROLES.map((role) => [role, "installed"])),
      stageRecipes: { [stage]: "installed" },
      recipes: [{ id: "installed", version: 1, body: "Read the offered evidence and report." }],
    },
    ...over,
  });
}

/**
 * One catalogued session. By default it names an archived capture, which is what a preparation
 * can read (#453); `captured: false` is a row the catalog has not listed from the archive yet —
 * an imported one — and `machine` is the host its label maps to, which selection never reads.
 */
async function catalog(
  db: GuestDatabase,
  selector: string,
  over: {
    machine?: string;
    live?: number;
    kind?: string;
    bytes?: number;
    captured?: boolean;
  } = {},
): Promise<void> {
  const captured = over.captured ?? true;
  await db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, live, kind, size, content_digest,
                          archive_label, archive_path, snapshot_id, archived_at, modified_at, seen_at)
     VALUES(?,?,'omp',?,?,?,?,?,?,?,?,?,?,?)`,
    [
      selector,
      over.machine ?? "dev-01",
      selector,
      over.live ?? 0,
      over.kind ?? "operator",
      over.bytes ?? 100,
      `digest-${selector}`,
      captured ? "dev-01" : null,
      captured ? `/home/alex/.omp/agent/sessions/${selector}.jsonl` : null,
      captured ? "a".repeat(64) : null,
      captured ? ago(1) : null,
      captured ? ago(1) : null,
      ago(1),
    ],
  );
}

async function analysisRecord(
  db: GuestDatabase,
  id: string,
  runId: string | null,
  parent: string | null = null,
  payload: Record<string, unknown> = {},
  ageDays = 1,
): Promise<void> {
  await db.run(
    `INSERT INTO records(id, root_id, kind, parent_id, run_id, seq, actor_kind, actor_id, title, created_at, payload)
     VALUES(?,?,?,?,?,0,'run','fixture',?,?,?)`,
    [
      id,
      id,
      id.startsWith("obs_") ? "observation" : "hypothesis",
      parent,
      runId,
      id,
      ago(ageDays),
      JSON.stringify(payload),
    ],
  );
}

async function citation(db: GuestDatabase, recordId: string, selector: string): Promise<void> {
  await db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
     VALUES(?,'cites',?,?,'session',?,'run','fixture',?)`,
    [
      `edg_${recordId}_${selector}`,
      recordId.startsWith("obs_") ? "observation" : "hypothesis",
      recordId,
      selector,
      ago(1),
    ],
  );
}

test("legacy policies select only review and all-zero activities never fall back to analysis", async () => {
  const { db, coord, assignment } = await oneAssignment();
  await catalog(db, "omp/new");
  expect(assignment.activity).toBe("review");
  expect(drawn(await coord.draw({ runId: "cycle_1", seed: 3n })).activity).toBe("review");
  await db.run(
    `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
    VALUES('zero',2,'operator','',?,?)`,
    [
      JSON.stringify({
        enabled: true,
        activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0 },
      }),
      ago(0),
    ],
  );
  expect((await coord.draw({ runId: "zero" })).outcome).toBe("gap");
});

test("analysis weights authorize only installed stage recipes", async () => {
  const policy = stagePolicy("challenge");
  expect(validatePolicy(policy, CONCURRENT_JOBS)).toBeNull();
  expect(validatePolicy({ ...policy, review: undefined }, CONCURRENT_JOBS)).not.toBeNull();
  expect(
    validatePolicy({ ...policy, review: { ...policy.review!, stageRecipes: {} } }, CONCURRENT_JOBS),
  ).not.toBeNull();
  expect(
    validatePolicy(
      { ...policy, activityWeights: { ...policy.activityWeights, explore: 1 } },
      CONCURRENT_JOBS,
    ),
  ).not.toBeNull();
  const { db, coord } = await deployment({
    ...policy,
    review: { ...policy.review!, stageRecipes: {} },
  });
  await catalog(db, "omp/a");
  const refused = await coord.draw({ runId: "cycle" });
  if (refused.outcome !== "gap") throw new Error("missing method launched");
  expect(refused.gap.reason).toBe("invalid-policy");
});

test("exploration consumes eligible material once, including across conductors and policy revisions", async () => {
  const { db, coord } = await deployment(stagePolicy("explore", { cooldownSeconds: 0 }));
  await catalog(db, "omp/live", { live: 1 });
  await catalog(db, "omp/agent", { kind: "agent" });
  // A preparation reads the archive only, so a row naming no capture is never material…
  await catalog(db, "omp/unarchived", { captured: false });
  await catalog(db, "omp/huge", { bytes: MAX_MATERIAL_BYTES + 1 });
  // …and a capture is material whichever machine its label maps to (#453).
  await catalog(db, "omp/real", { machine: "dev-02" });
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  const one = drawn(await coord.draw({ runId: "a", seed: 1n }));
  const two = drawn(await other.draw({ runId: "b", seed: 99n }));
  if (one.activity !== "explore") throw new Error("wrong activity");
  expect(one.selectors).toEqual(["omp/real"]);
  expect(two.id).toBe(one.id);
  const claims = await Promise.all([
    coord.claim({ assignment: one, runId: "a", jobId: "prepare_a" }),
    other.claim({ assignment: two, runId: "b", jobId: "prepare_b" }),
  ]);
  expect(claims.filter((result) => result.outcome === "granted")).toHaveLength(1);
  const granted = claims.find((result) => result.outcome === "granted");
  if (granted?.outcome !== "granted") throw new Error("no grant");
  await coord.finish({
    id: one.id,
    runId: granted.claim.runId,
    fence: granted.claim.fence,
    outcome: "completed",
    cost: 0.01,
  });
  await db.run(
    `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
    VALUES('renamed',2,'operator','',?,?)`,
    [JSON.stringify(stagePolicy("explore", { version: "renamed", cooldownSeconds: 0 })), ago(0)],
  );
  expect((await other.draw({ runId: "c" })).outcome).toBe("gap");
  await db.run(
    `UPDATE sessions SET seen_at = ?, snapshot_id = 'new-archive' WHERE selector = 'omp/real'`,
    [ago(0)],
  );
  expect((await other.draw({ runId: "d" })).outcome).toBe("gap");
  await db.run(`UPDATE sessions SET content_digest = 'changed' WHERE selector = 'omp/real'`);
  const changed = drawn(await other.draw({ runId: "e" }));
  expect(changed.id).not.toBe(one.id);
  expect(changed.inputDigest).not.toBe(one.inputDigest);
});

test("challenge carries whole prior claims and recovers selectors from the native material receipt", async () => {
  const { db, coord } = await deployment(stagePolicy("challenge"));
  await catalog(db, "omp/served");
  await catalog(db, "omp/unrelated");
  await analysisRecord(db, "hyp_00000001", "source", null, {
    statement: "bounded claim",
    limits: ["do not generalize"],
  });
  await analysisRecord(db, "obs_00000001", "source", "hyp_00000001", {
    evidence: [{ quote: "original" }],
    limits: "one case",
  });
  await analysisRecord(db, "obs_00000002", null, "hyp_00000001", {
    claim: "prior objection",
    evidence: [],
  });
  await db.run(
    `INSERT INTO edges(id,kind,from_kind,from_id,to_kind,to_id,note,actor_kind,actor_id,created_at)
    VALUES('edg_objection',?,'observation','obs_00000002','hypothesis','hyp_00000001','missing-check','run','prior',?)`,
    [CHALLENGE_RELATION, ago(1)],
  );
  const material = {
    schema: MATERIAL_SCHEMA,
    preparationId: "prepare_source",
    preparedAt: ago(1),
    machineId: "dev-01",
    sessions: [
      {
        selector: "omp/served",
        harness: "omp",
        sourceId: "served",
        captureDigest: "capture",
        sourceDigest: "source",
        file: "served.jsonl",
        records: 2,
        bytes: 100,
      },
    ],
  };
  await db.run(
    `INSERT INTO runs(id,kind,job_id,closure,started_at,records,payload)
    VALUES('native',?,'prepare_source','completed',?,0,?)`,
    [OPERATIONS.prepare, ago(1), JSON.stringify({ material })],
  );
  await db.run(
    `INSERT INTO runs(id,kind,prepare_job_id,closure,started_at,records,payload)
    VALUES('source',?,'prepare_source','completed',?,3,'{}')`,
    [OPERATIONS.explore, ago(1)],
  );
  const assignment = drawn(await coord.draw({ runId: "challenge" }));
  if (assignment.activity !== "challenge") throw new Error("wrong activity");
  expect(assignment.selectors).toEqual(["omp/served"]);
  expect(assignment.brief.map((record) => record.id)).toEqual([
    "hyp_00000001",
    "obs_00000001",
    "obs_00000002",
  ]);
  expect(assignment.brief[0]?.payload).toEqual({
    statement: "bounded claim",
    limits: ["do not generalize"],
  });
  expect(assignment.brief[1]?.payload).toEqual({
    evidence: [{ quote: "original" }],
    limits: "one case",
  });
  expect(assignment.brief[2]?.runId).toBeNull();
  expect(assignment.brief[2]?.objectionTo).toEqual(["hyp_00000001"]);
  await db.run(`UPDATE sessions SET live = 1 WHERE selector = 'omp/served'`);
  expect((await coord.draw({ runId: "no-material" })).outcome).toBe("gap");
});

test("synthesis needs two known source runs connected by the actual candidate", async () => {
  const { db, coord } = await deployment(stagePolicy("synthesize"));
  await catalog(db, "omp/a");
  await catalog(db, "omp/unrelated");
  await analysisRecord(db, "hyp_00000001", null);
  await analysisRecord(db, "obs_00000001", "source-a", "hyp_00000001");
  await analysisRecord(db, "obs_00000002", "source-a", "hyp_00000001");
  await analysisRecord(db, "obs_00000003", null, "hyp_00000001");
  await analysisRecord(db, "obs_00000004", "source-b");
  await citation(db, "obs_00000001", "omp/a");
  await citation(db, "obs_00000004", "omp/unrelated");
  expect((await coord.draw({ runId: "insufficient" })).outcome).toBe("gap");
  await analysisRecord(db, "obs_00000005", "source-b", "hyp_00000001");
  const assignment = drawn(await coord.draw({ runId: "sufficient" }));
  if (assignment.activity !== "synthesize") throw new Error("wrong activity");
  expect(
    new Set(
      assignment.brief
        .filter((record) => record.kind === "observation")
        .map((record) => record.runId),
    ),
  ).toEqual(new Set(["source-a", "source-b"]));
  expect(assignment.brief.some((record) => record.id === "obs_00000004")).toBe(false);
  expect(assignment.selectors).toEqual(["omp/a"]);
});

test("a shared active topic or cited session joins synthesis, but excluded topics cannot spend", async () => {
  for (const join of ["topic", "session"] as const) {
    const { db, coord } = await deployment(stagePolicy("synthesize"));
    await catalog(db, "omp/a");
    await analysisRecord(db, "obs_00000001", "source-a");
    await analysisRecord(db, "obs_00000002", "source-b");
    await citation(db, "obs_00000001", "omp/a");
    if (join === "session") await citation(db, "obs_00000002", "omp/a");
    else {
      await filing(db, "obs_00000001", "ent_00000001");
      await filing(db, "obs_00000002", "ent_00000001");
      expect((await coord.draw({ runId: "not-active" })).outcome).toBe("gap");
      await fact(db, "ent_00000001", "lifecycle", "active");
    }
    const assignment = drawn(await coord.draw({ runId: "joined" }));
    expect(assignment.activity).toBe("synthesize");
    if (join === "topic") {
      await fact(db, "ent_00000001", "analysis-policy", "excluded");
      const refused = await coord.claim({ assignment, runId: "joined", jobId: "prepare_joined" });
      expect(refused.outcome).toBe("refused");
      expect((await coord.spend()).total).toBe(0);
      expect((await coord.draw({ runId: "excluded" })).outcome).toBe("gap");
    }
  }
});

test("analysis bounds whole records and exact source selectors without truncating claim payloads", async () => {
  const { db, coord } = await deployment(stagePolicy("challenge"));
  await analysisRecord(db, "hyp_00000001", "run_a", null, {
    statement: "target",
    limits: "l".repeat(500),
  });
  for (let n = 1; n <= 30; n += 1) {
    const id = `obs_${n.toString(16).padStart(8, "0")}`;
    await analysisRecord(db, id, "run_a", "hyp_00000001", {
      claim: "x".repeat(1000),
      limits: ["whole"],
    });
    await catalog(db, `omp/${String(n)}`, { bytes: 30 * 1024 * 1024 });
    await citation(db, id, `omp/${String(n)}`);
  }
  const assignment = drawn(await coord.draw({ runId: "bounded" }));
  if (assignment.activity !== "challenge") throw new Error("wrong activity");
  expect(assignment.brief.length).toBeLessThanOrEqual(ANALYSIS_BRIEF_LIMIT);
  expect(new TextEncoder().encode(JSON.stringify(assignment.brief)).byteLength).toBeLessThanOrEqual(
    ANALYSIS_BRIEF_BYTE_LIMIT,
  );
  expect(assignment.selectors.length).toBeLessThanOrEqual(ANALYSIS_SOURCE_LIMIT);
  expect(assignment.selectors.length * 30 * 1024 * 1024).toBeLessThanOrEqual(MAX_MATERIAL_BYTES);
  for (const record of assignment.brief.filter((record) => record.kind === "observation")) {
    expect(record.payload).toEqual({ claim: "x".repeat(1000), limits: ["whole"] });
  }
});

test("changed analysis inputs still obey cooldown and per-item caps", async () => {
  const { db, coord } = await deployment(
    stagePolicy("explore", { cooldownSeconds: 60, initialReviews: 1, maxItemReviews: 1 }),
  );
  await catalog(db, "omp/a");
  const assignment = drawn(await coord.draw({ runId: "first" }));
  const claimed = await coord.claim({ assignment, runId: "first", jobId: "prepare_first" });
  if (claimed.outcome !== "granted") throw new Error(claimed.refusal.detail);
  await coord.finish({
    id: assignment.id,
    runId: "first",
    fence: claimed.claim.fence,
    outcome: "completed",
    cost: 0.01,
  });
  await db.run(`UPDATE sessions SET content_digest = 'changed' WHERE selector = 'omp/a'`);
  const capped = await coord.draw({ runId: "changed", now: NOW + 61_000 });
  expect(capped.outcome).toBe("gap");
  expect(capped.gaps.some((gap) => gap.reason === "capped")).toBe(true);
  await db.run(
    `INSERT INTO policies(version, seq, actor_id, reason, payload, recorded_at)
    VALUES('more',2,'operator','',?,?)`,
    [JSON.stringify(stagePolicy("explore", { version: "more", cooldownSeconds: 60 })), ago(0)],
  );
  const cooling = await coord.draw({ runId: "cooling", now: NOW + 1_000 });
  expect(cooling.outcome).toBe("gap");
  expect(cooling.gaps.some((gap) => gap.reason === "cooling")).toBe(true);
  expect(drawn(await coord.draw({ runId: "rested", now: NOW + 61_000 })).id).not.toBe(
    assignment.id,
  );
});

test("a preparation keeps one slot through native closure and lease expiry until its parent closes", async () => {
  const { db, coord } = await deployment(
    stagePolicy("explore", { concurrentPerMachine: 1, cooldownSeconds: 0 }),
  );
  await catalog(db, "omp/a");
  await catalog(db, "omp/b");
  const assignment = drawn(await coord.draw({ runId: "first", seed: 1n }));
  const first = await coord.claim({ assignment, runId: "first", jobId: "prepare_first" });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  // Before native posting, the known job already occupies an unplaced slot.
  expect(await coord.open()).toEqual({ total: 1, byMachine: {} });
  // Missing retention and failed cancellation leave the native worker's termination unknown.
  expect(await coord.open(first.claim.expiresAt + 1)).toEqual({ total: 1, byMachine: {} });
  expect(
    (
      await coord.claim({
        assignment,
        runId: "unconfirmed-takeover",
        jobId: "duplicate",
        now: first.claim.expiresAt + 1,
      })
    ).outcome,
  ).toBe("refused");
  await runOn(db, "prepare_first", "dev-01");
  await db.run(
    `UPDATE runs SET closure = 'completed', finished_at = ? WHERE job_id = 'prepare_first'`,
    [ago(0)],
  );
  await db.run(
    `INSERT INTO runs(id,kind,machine_id,prepare_job_id,started_at,records,payload)
    VALUES('parent',?,'dev-01','prepare_first',?,0,'{}')`,
    [OPERATIONS.explore, ago(0)],
  );
  const expired = first.claim.expiresAt + 1;
  expect(await coord.open(expired)).toEqual({ total: 1, byMachine: { "dev-01": 1 } });
  const takeover = await coord.claim({
    assignment,
    runId: "second",
    jobId: "prepare_second",
    now: expired,
  });
  expect(takeover.outcome).toBe("refused");
  const blocked = await coord.draw({ runId: "other", now: expired });
  if (blocked.outcome !== "gap") throw new Error("a live parent freed its slot");
  expect(blocked.gap.reason).toBe("batch");
  await db.run(`UPDATE runs SET closure = 'stopped' WHERE id = 'parent'`);
  expect(await coord.open(expired)).toEqual({ total: 0, byMachine: {} });
});

test("concurrent distinct preparation grants cannot oversubscribe a machine or the daily budget", async () => {
  for (const bound of ["machine", "daily"] as const) {
    const { db, coord } = await deployment(
      stagePolicy(
        "explore",
        bound === "machine" ? { concurrentPerMachine: 1 } : { perCycleCost: 0.2, dailyCost: 0.2 },
      ),
    );
    await catalog(db, "omp/a");
    await catalog(db, "omp/b");
    if (bound === "daily") await claimRow(db, "asg_spent", "old", 0.15, 0.15);
    const assignments = await Promise.all([
      coord.draw({ runId: "a", seed: 1n }),
      coord.draw({ runId: "b", seed: 1n }),
    ]);
    const one = drawn(assignments[0]!);
    const two = drawn(assignments[1]!);
    expect(one.id).not.toBe(two.id);
    const granted = await Promise.all([
      coord.claim({ assignment: one, runId: "a", jobId: "prepare_a" }),
      coord.claim({ assignment: two, runId: "b", jobId: "prepare_b" }),
    ]);
    expect(granted.filter((result) => result.outcome === "granted")).toHaveLength(1);
    expect((await coord.open()).total).toBe(1);
    expect((await coord.spend()).total).toBeLessThanOrEqual(
      (await coord.policy()).policy.dailyCost,
    );
  }
});

test("prepare-to-Code binding requires live exact ownership and the expected previous job", async () => {
  const { coord, assignment } = await oneAssignment();
  const first = await coord.claim({ assignment, runId: "owner", jobId: "prepare" });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  const fence = first.claim.fence;
  for (const over of [
    { previousJobId: "unexpected" },
    { runId: "stranger" },
    { fence: fence + 1 },
    { now: first.claim.expiresAt },
  ]) {
    expect(
      (
        await coord.bind({
          id: assignment.id,
          runId: "owner",
          fence,
          jobId: "code",
          previousJobId: "prepare",
          ...over,
        })
      ).outcome,
    ).toBe("refused");
  }
  expect(
    (await coord.bind({ id: assignment.id, runId: "owner", fence, jobId: "code" })).outcome,
  ).toBe("refused");
  const bound = await coord.bind({
    id: assignment.id,
    runId: "owner",
    fence,
    jobId: "code",
    previousJobId: "prepare",
  });
  if (bound.outcome !== "bound") throw new Error(bound.refusal.detail);
  expect(bound.claim.jobId).toBe("code");
  expect(
    (
      await coord.bind({
        id: assignment.id,
        runId: "owner",
        fence,
        jobId: "code",
        previousJobId: "prepare",
      })
    ).outcome,
  ).toBe("bound");
  expect(
    (
      await coord.bind({
        id: assignment.id,
        runId: "owner",
        fence,
        jobId: "different",
        previousJobId: "prepare",
      })
    ).outcome,
  ).toBe("refused");
  await coord.finish({
    id: assignment.id,
    runId: "owner",
    fence,
    outcome: "completed",
    cost: 0.01,
  });
  expect(
    (
      await coord.bind({
        id: assignment.id,
        runId: "owner",
        fence,
        jobId: "after",
        previousJobId: "code",
      })
    ).outcome,
  ).toBe("refused");
});

test("activity weights, not the number of review lanes, determine the stage share", async () => {
  const { db, coord } = await deployment(
    stagePolicy("challenge", {
      activityWeights: { review: 0.1, explore: 0, challenge: 1, synthesize: 0 },
    }),
  );
  await catalog(db, "omp/a");
  await analysisRecord(db, "hyp_00000001", "source");
  await citation(db, "hyp_00000001", "omp/a");
  const samples = await sampleDraws(coord, 100);
  expect(
    samples.filter((assignment) => assignment.activity === "challenge").length,
  ).toBeGreaterThan(75);
  expect(samples.some((assignment) => assignment.activity === "review")).toBe(true);
  expect(
    samples.every(
      (assignment) => assignment.activity === "review" || assignment.activity === "challenge",
    ),
  ).toBe(true);
  const replay = drawn(await coord.draw({ runId: "cycle_1", seed: 17n }));
  expect(samples[16]).toEqual(replay);
});

test("an expired Code lease neither frees running work nor permits a second worker", async () => {
  const { db, coord, assignment } = await oneAssignment();
  const first = await coord.claim({ assignment, runId: "first", jobId: "code_first" });
  if (first.outcome !== "granted") throw new Error(first.refusal.detail);
  await runOn(db, "code_first", "dev-01");
  const expired = first.claim.expiresAt + 1;
  expect(await coord.open(expired)).toEqual({ total: 1, byMachine: { "dev-01": 1 } });
  expect(
    (await coord.claim({ assignment, runId: "second", jobId: "code_second", now: expired }))
      .outcome,
  ).toBe("refused");
  // Late settlement can still charge the actual owner: expiry never changes its fence.
  expect(
    (
      await coord.finish({
        id: assignment.id,
        runId: "first",
        fence: first.claim.fence,
        cost: 0.01,
        outcome: "completed",
        now: expired,
      })
    ).outcome,
  ).toBe("finished");
});

test("terminal recovery requires a settled attributed parent and accounts its Code cost once", async () => {
  const { db, coord } = await deployment(stagePolicy("explore"));
  await catalog(db, "omp/recovery");
  const assignment = drawn(await coord.draw({ runId: "recovery" }));
  const grant = await coord.claim({
    assignment,
    runId: "recovery",
    jobId: "prepare_recovery",
  });
  if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
  await runOn(db, "prepare_recovery", "dev-01");
  await db.run(`UPDATE runs SET closure = 'completed' WHERE job_id = 'prepare_recovery'`);
  const analysis = {
    stage: "explore",
    selectors: ["omp/recovery"],
    brief: [],
    claim: { id: assignment.id, runId: "recovery", fence: grant.claim.fence },
  };
  await db.run(
    `INSERT INTO runs(id,kind,machine_id,job_id,prepare_job_id,authority_kind,authority_id,
                      preparation,started_at,records,cost_usd,payload)
     VALUES('recovery_parent',?,'dev-01','code_recovery','prepare_recovery','conductor',
            'recovery',?,?,0,0.02,'{}')`,
    [OPERATIONS.explore, JSON.stringify({ analysis }), ago(0)],
  );
  const request = {
    id: assignment.id,
    runId: "recovery",
    fence: grant.claim.fence,
    cost: 0.02,
    outcome: "failed" as const,
    now: grant.claim.expiresAt + 1,
    terminalJob: { jobId: "code_recovery", previousJobId: "prepare_recovery" },
  };
  // A live worker is not a terminal receipt, even when the permission to continue expired.
  expect((await coord.finish(request)).outcome).toBe("refused");
  expect((await coord.open(request.now)).total).toBe(1);
  await db.run(
    `UPDATE runs SET closure = 'failed', finished_at = ?, preparation = ? WHERE id = 'recovery_parent'`,
    [
      new Date(request.now).toISOString(),
      JSON.stringify({
        analysis: { ...analysis, claim: { ...analysis.claim, fence: grant.claim.fence + 1 } },
      }),
    ],
  );
  expect((await coord.finish(request)).outcome).toBe("refused");
  await db.run(`UPDATE runs SET preparation = ? WHERE id = 'recovery_parent'`, [
    JSON.stringify({ analysis }),
  ]);
  expect(
    (
      await coord.finish({
        ...request,
        terminalJob: { ...request.terminalJob, previousJobId: "unrelated" },
      })
    ).outcome,
  ).toBe("refused");
  expect((await coord.finish(request)).outcome).toBe("finished");
  expect((await coord.spend(request.now)).total).toBeCloseTo(0.02);
  expect((await coord.finish(request)).outcome).toBe("finished");
  expect((await coord.spend(request.now)).total).toBeCloseTo(0.02);
  expect((await coord.open(request.now)).total).toBe(0);
});

test("exploration reaches sources beyond 256 capped catalog entries", async () => {
  const { db, coord } = await deployment(
    stagePolicy("explore", { initialReviews: 1, maxItemReviews: 1, cooldownSeconds: 0 }),
  );
  for (let n = 0; n < 256; n++) {
    const selector = `omp/${String(n).padStart(4, "0")}`;
    await catalog(db, selector);
    await db.run(
      `INSERT INTO claims(id,record_id,role,lane,policy_version,run_id,fence,
                          reserved_cost,actual_cost,granted_at,expires_at,finished_at,outcome)
       VALUES(?,?,'analysis:explore','exploration','1','past',1,0.01,0.01,?,?,?,'completed')`,
      [`old_${n}`, selector, ago(2), ago(1), ago(1)],
    );
  }
  await catalog(db, "omp/older");
  const assignment = drawn(await coord.draw({ runId: "after-window" }));
  if (assignment.activity !== "explore") throw new Error("wrong activity");
  expect(assignment.selectors).toEqual(["omp/older"]);
});

test("challenge reaches an older eligible head beyond 64 capped heads", async () => {
  const { db, coord } = await deployment(
    stagePolicy("challenge", { initialReviews: 1, maxItemReviews: 1, cooldownSeconds: 0 }),
  );
  await catalog(db, "omp/shared");
  for (let n = 1; n <= 65; n++) {
    const id = `hyp_${n.toString(16).padStart(8, "0")}`;
    await analysisRecord(db, id, "source", null, {}, n === 65 ? 10 : 1);
    await citation(db, id, "omp/shared");
    if (n === 65) continue;
    await db.run(
      `INSERT INTO claims(id,record_id,role,lane,policy_version,run_id,fence,
                          reserved_cost,actual_cost,granted_at,expires_at,finished_at,outcome)
       VALUES(?,?,'analysis:challenge','challenge','1','past',1,0.01,0.01,?,?,?,'completed')`,
      [`old_${n}`, id, ago(2), ago(1), ago(1)],
    );
  }
  expect(drawn(await coord.draw({ runId: "after-window" })).recordId).toBe("hyp_00000041");
});

test("claim refresh finds its drawn analysis after one new offer shifts the draw cap", async () => {
  const { db, coord } = await deployment(stagePolicy("explore"));
  for (let n = 0; n < 63; n++) await catalog(db, `omp/a-${String(n).padStart(3, "0")}`);
  await catalog(db, "omp/z-drawn");
  // Separate hand-outs exhaust the admitted window without reserving or settling any offer.
  let selected: { assignment: Assignment; runId: string } | undefined;
  for (let n = 0; n < 64; n++) {
    const runId = `owner_${n}`;
    const assignment = drawn(await coord.draw({ runId, seed: 1n }));
    if (assignment.recordId === "omp/z-drawn") {
      selected = { assignment, runId };
      break;
    }
  }
  if (selected === undefined) throw new Error("the last admitted source was not drawn");
  await catalog(db, "omp/0-new");
  const grant = await coord.claim({ ...selected, jobId: "prepare_drawn" });
  if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
  expect(grant.claim.id).toBe(selected.assignment.id);
  expect(grant.claim.recordId).toBe("omp/z-drawn");
});

test("a full offer stage stops independently while challenge and synthesis remain reachable", async () => {
  const { db } = await deployment(stagePolicy("explore"));
  for (let n = 0; n < 65; n++) await catalog(db, `omp/${String(n).padStart(3, "0")}`);
  const ids = ["hyp_00000001", "obs_00000001", "obs_00000002"];
  for (const [n, id] of ids.entries()) {
    await analysisRecord(db, id, `source_${n}`, n === 0 ? null : ids[0]!);
    await citation(db, id, "omp/000");
  }
  const full = new Set<Stage>();
  const offers = analysisOffers(
    db,
    "dev-01",
    1,
    ["explore", "challenge", "synthesize"],
    new Set(ids),
    new Map(),
    new Set(),
    (stage) => !full.has(stage),
  );
  try {
    for (let n = 0; n < 64; n++) {
      const next = await offers.next();
      expect(next.done).toBe(false);
      if (next.done || "missing" in next.value) throw new Error("expected an explore offer");
      expect(next.value.stage).toBe("explore");
    }
    full.add("explore");
    const challenge = await offers.next();
    if (challenge.done || "missing" in challenge.value) throw new Error("expected challenge");
    expect(challenge.value.stage).toBe("challenge");
    full.add("challenge");
    const synthesis = await offers.next();
    if (synthesis.done || "missing" in synthesis.value) throw new Error("expected synthesis");
    expect(synthesis.value.stage).toBe("synthesize");
    expect(new Set(synthesis.value.brief.map((row) => row.runId))).toContain("source_2");
    full.add("synthesize");
    expect((await offers.next()).done).toBe(true);
  } finally {
    await offers.return(undefined);
  }
});

test("synthesis joins original runs across record pages and keeps provisional critique", async () => {
  const { db, coord } = await deployment(stagePolicy("synthesize"));
  await catalog(db, "omp/shared");
  await analysisRecord(db, "hyp_00000001", null, null, {}, 11);
  const objection = { statement: "An alternative cause remains plausible", limits: ["untested"] };
  await analysisRecord(db, "hyp_00000002", "critic", null, objection);
  await db.run(
    `INSERT INTO edges(id,kind,from_kind,from_id,to_kind,to_id,actor_kind,actor_id,created_at)
     VALUES('edg_critique',?,'hypothesis','hyp_00000002','hypothesis','hyp_00000001','run','critic',?)`,
    [CHALLENGE_RELATION, ago(1)],
  );
  for (let n = 1; n <= 70; n++) {
    const id = `obs_${n.toString(16).padStart(8, "0")}`;
    await analysisRecord(
      db,
      id,
      n === 70 ? "source-b" : "source-a",
      "hyp_00000001",
      { claim: `whole observation ${n}`, limits: "l".repeat(600) },
      n === 70 ? 10 : 1,
    );
    await citation(db, id, "omp/shared");
  }
  const assignment = drawn(await coord.draw({ runId: "cross-page" }));
  if (assignment.activity !== "synthesize") throw new Error("wrong activity");
  expect(
    new Set(assignment.brief.filter((row) => row.kind === "observation").map((row) => row.runId)),
  ).toEqual(new Set(["source-a", "source-b"]));
  expect(assignment.brief.find((row) => row.id === "hyp_00000002")?.payload).toEqual(objection);
  expect(assignment.brief.some((row) => row.id === "hyp_00000001")).toBe(true);
  expect(assignment.brief.length).toBeLessThanOrEqual(ANALYSIS_BRIEF_LIMIT);
  expect(new TextEncoder().encode(JSON.stringify(assignment.brief)).byteLength).toBeLessThanOrEqual(
    ANALYSIS_BRIEF_BYTE_LIMIT,
  );
});

test("synthesis carries hypothesis objections and their targets through a shared session", async () => {
  const { db, coord } = await deployment(stagePolicy("synthesize"));
  await catalog(db, "omp/shared");
  await analysisRecord(db, "hyp_00000001", null);
  await analysisRecord(db, "hyp_00000002", "critic", null, {
    alternative: "unmeasured confounder",
  });
  await analysisRecord(db, "obs_00000001", "source-a", "hyp_00000001");
  await analysisRecord(db, "obs_00000002", "source-b");
  await citation(db, "obs_00000001", "omp/shared");
  await citation(db, "obs_00000002", "omp/shared");
  await db.run(
    `INSERT INTO edges(id,kind,from_kind,from_id,to_kind,to_id,actor_kind,actor_id,created_at)
     VALUES('edg_critique',?,'hypothesis','hyp_00000002','hypothesis','hyp_00000001','run','critic',?)`,
    [CHALLENGE_RELATION, ago(1)],
  );
  const assignment = drawn(await coord.draw({ runId: "session-critique" }));
  if (assignment.activity !== "synthesize") throw new Error("wrong activity");
  expect(assignment.brief.map((row) => row.id)).toEqual([
    "hyp_00000001",
    "hyp_00000002",
    "obs_00000001",
    "obs_00000002",
  ]);
  expect(assignment.brief.find((row) => row.id === "hyp_00000002")?.objectionTo).toEqual([
    "hyp_00000001",
  ]);
});

test("zero-cost analysis failures retry after cooldown with fresh identities and bounded setbacks", async () => {
  const { db, coord } = await deployment(stagePolicy("explore", { cooldownSeconds: 60 }));
  await catalog(db, "omp/retry");
  const ids = new Set<string>();
  for (let attempt = 0; attempt < 3; attempt++) {
    const moment = NOW + attempt * 61_000;
    const runId = `attempt_${attempt}`;
    const assignment = drawn(await coord.draw({ runId, now: moment }));
    expect(ids.has(assignment.id)).toBe(false);
    ids.add(assignment.id);
    const grant = await coord.claim({
      assignment,
      runId,
      jobId: `prepare_${attempt}`,
      now: moment,
    });
    if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
    await coord.finish({
      id: assignment.id,
      runId,
      fence: grant.claim.fence,
      cost: 0,
      outcome: "failed",
      now: moment,
    });
    const immediate = await coord.draw({ runId: "not-yet", now: moment + 1 });
    expect(immediate.outcome).toBe("gap");
    expect(
      immediate.gaps.some((gap) => gap.reason === (attempt === 2 ? "exhausted" : "cooling")),
    ).toBe(true);
  }
  const exhausted = await coord.draw({ runId: "exhausted", now: NOW + 4 * 61_000 });
  expect(exhausted.outcome).toBe("gap");
  expect(exhausted.gaps.some((gap) => gap.reason === "exhausted")).toBe(true);
});

test("completed free analysis and paid failures remain settled across policy revisions", async () => {
  for (const receipt of [
    { cost: 0, outcome: "completed" },
    { cost: 0.01, outcome: "failed" },
  ] as const) {
    const { db, coord } = await deployment(stagePolicy("explore", { cooldownSeconds: 0 }));
    await catalog(db, "omp/settled");
    const assignment = drawn(await coord.draw({ runId: "first" }));
    const grant = await coord.claim({ assignment, runId: "first", jobId: "prepare_first" });
    if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
    await coord.finish({ id: assignment.id, runId: "first", fence: grant.claim.fence, ...receipt });
    await db.run(
      `INSERT INTO policies(version,seq,actor_id,reason,payload,recorded_at)
       VALUES('renamed',2,'operator','',?,?)`,
      [JSON.stringify(stagePolicy("explore", { version: "renamed", cooldownSeconds: 0 })), ago(0)],
    );
    const refused = await coord.draw({ runId: "second" });
    expect(refused.outcome).toBe("gap");
    expect(refused.gaps.some((gap) => gap.reason === "settled")).toBe(true);
  }
});

test("unposted finish requires expired exact analysis ownership and zero-cost failure", async () => {
  const { db, coord } = await deployment(stagePolicy("explore"));
  await catalog(db, "omp/unposted");
  const assignment = drawn(await coord.draw({ runId: "owner" }));
  const grant = await coord.claim({ assignment, runId: "owner", jobId: "prepare_unposted" });
  if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
  const request = {
    id: assignment.id,
    runId: "owner",
    fence: grant.claim.fence,
    cost: 0,
    outcome: "failed" as const,
    unpostedJobId: "prepare_unposted",
    now: grant.claim.expiresAt + 1,
  };
  for (const changed of [
    { now: NOW },
    { runId: "other" },
    { fence: grant.claim.fence + 1 },
    { unpostedJobId: "wrong" },
    { cost: 0.01 },
    { outcome: "completed" as const },
    { terminalJob: { jobId: "code", previousJobId: "prepare_unposted" } },
  ])
    expect((await coord.finish({ ...request, ...changed })).outcome).toBe("refused");
  expect((await coord.open(request.now)).total).toBe(1);
  expect((await coord.finish(request)).outcome).toBe("finished");
  expect((await coord.open(request.now)).total).toBe(0);
  expect((await coord.spend(request.now)).total).toBe(0);

  const review = await oneAssignment();
  const reviewGrant = await review.coord.claim({
    assignment: review.assignment,
    runId: "owner",
    jobId: "review_job",
  });
  if (reviewGrant.outcome !== "granted") throw new Error(reviewGrant.refusal.detail);
  expect(
    (
      await review.coord.finish({
        ...request,
        id: review.assignment.id,
        fence: reviewGrant.claim.fence,
        unpostedJobId: "review_job",
        now: reviewGrant.claim.expiresAt + 1,
      })
    ).outcome,
  ).toBe("refused");
});

test("unposted finish atomically refuses a native run or parent inserted after its read", async () => {
  for (const column of ["job_id", "prepare_job_id"] as const) {
    const { db, coord } = await deployment(stagePolicy("explore"));
    await catalog(db, "omp/unposted");
    const assignment = drawn(await coord.draw({ runId: "owner" }));
    const grant = await coord.claim({ assignment, runId: "owner", jobId: "prepare_race" });
    if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
    let inserted = false;
    const raced = coordinator(
      {
        db: {
          ...db,
          batch: async (statements) => {
            if (!inserted) {
              inserted = true;
              await db.run(
                `INSERT INTO runs(id,kind,machine_id,${column},started_at,records,payload)
             VALUES('durable_intent',?,'dev-01','prepare_race',?,0,'{}')`,
                [OPERATIONS.explore, ago(0)],
              );
            }
            return db.batch(statements);
          },
        },
      },
      () => NOW,
      CONCURRENT_JOBS,
    );
    expect(
      (
        await raced.finish({
          id: assignment.id,
          runId: "owner",
          fence: grant.claim.fence,
          cost: 0,
          outcome: "failed",
          unpostedJobId: "prepare_race",
          now: grant.claim.expiresAt + 1,
        })
      ).outcome,
    ).toBe("refused");
    const held = await db.query(`SELECT finished_at, actual_cost FROM claims WHERE id = ?`, [
      assignment.id,
    ]);
    expect(held[0]?.["finished_at"]).toBeNull();
    expect(held[0]?.["actual_cost"]).toBeNull();
  }
});

test("unposted finish refuses retained parent authority even after closure and Code rebinding", async () => {
  for (const closure of [null, "failed"]) {
    const { db, coord } = await deployment(stagePolicy("explore"));
    await catalog(db, "omp/intent");
    const assignment = drawn(await coord.draw({ runId: "owner" }));
    const grant = await coord.claim({ assignment, runId: "owner", jobId: "prepare_intent" });
    if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
    const bound = await coord.bind({
      id: assignment.id,
      runId: "owner",
      fence: grant.claim.fence,
      previousJobId: "prepare_intent",
      jobId: "code_unretained",
    });
    if (bound.outcome !== "bound") throw new Error(bound.refusal.detail);
    const raced = coordinator(
      {
        db: {
          ...db,
          batch: async (statements) => {
            // The parent still carries the prepare id, not the claim's newly bound Code id.
            // Insert after finish reads ownership to exercise the atomic guard as well.
            await db.run(
              `INSERT INTO runs(id,kind,machine_id,prepare_job_id,authority_kind,authority_id,
                                preparation,closure,started_at,records,payload)
               VALUES('retained_parent',?,'dev-01','prepare_intent','conductor','owner',?,?,?,0,'{}')`,
              [
                OPERATIONS.explore,
                JSON.stringify({
                  analysis: {
                    claim: { id: assignment.id, runId: "owner", fence: grant.claim.fence },
                  },
                }),
                closure,
                ago(0),
              ],
            );
            return db.batch(statements);
          },
        },
      },
      () => NOW,
      CONCURRENT_JOBS,
    );
    const expired = grant.claim.expiresAt + 1;
    expect(
      (
        await raced.finish({
          id: assignment.id,
          runId: "owner",
          fence: grant.claim.fence,
          cost: 0,
          outcome: "failed",
          unpostedJobId: "code_unretained",
          now: expired,
        })
      ).outcome,
    ).toBe("refused");
    const held = await db.query(`SELECT finished_at, actual_cost FROM claims WHERE id = ?`, [
      assignment.id,
    ]);
    expect(held[0]?.["finished_at"]).toBeNull();
    expect(held[0]?.["actual_cost"]).toBeNull();
    expect((await coord.open(expired)).total).toBe(1);
  }
});

test("synthesis reaches a later bounded brief after the earlier window is completed", async () => {
  const { db, coord } = await deployment(stagePolicy("synthesize", { cooldownSeconds: 0 }));
  await catalog(db, "omp/shared");
  await analysisRecord(db, "hyp_00000001", null);
  for (let n = 1; n <= 30; n++) {
    const id = `obs_${n.toString(16).padStart(8, "0")}`;
    await analysisRecord(db, id, n === 30 ? "source-b" : "source-a", "hyp_00000001");
    await citation(db, id, "omp/shared");
  }
  const first = drawn(await coord.draw({ runId: "first-window", seed: 1n }));
  if (first.activity !== "synthesize") throw new Error("wrong activity");
  const grant = await coord.claim({
    assignment: first,
    runId: "first-window",
    jobId: "prepare_first",
  });
  if (grant.outcome !== "granted") throw new Error(grant.refusal.detail);
  await coord.finish({
    id: first.id,
    runId: "first-window",
    fence: grant.claim.fence,
    cost: 0.01,
    outcome: "completed",
  });
  const next = drawn(await coord.draw({ runId: "next-window", seed: 1n }));
  if (next.activity !== "synthesize") throw new Error("wrong activity");
  const previous = new Set(first.brief.map((row) => row.id));
  expect(next.brief.some((row) => row.kind === "observation" && !previous.has(row.id))).toBe(true);
  expect(
    new Set(next.brief.filter((row) => row.kind === "observation").map((row) => row.runId)),
  ).toEqual(new Set(["source-a", "source-b"]));
});

// ---------------------------------------------------------------------------- transcript mapping

function mapPolicy(over: Partial<Policy> = {}): Policy {
  return PolicySchema.parse({
    enabled: true,
    activityWeights: { review: 0, explore: 0, challenge: 0, synthesize: 0 },
    batchSize: 4,
    perCycleCost: 4,
    dailyCost: 4,
    review: {
      machineId: "review-host",
      profile: { containerId: "review-profile", expectedRevision: 1 },
      roleRecipes: Object.fromEntries(ROLES.map((role) => [role, "generate"])),
      recipes: [
        { id: "generate", version: 1, body: "Describe the supplied navigation span." },
        { id: "map-review", version: 1, body: "Review the supplied navigation summary." },
      ],
    },
    mapping: {
      sourceMachineId: "mapping-source",
      executorMachineId: "mapping-host",
      profile: { containerId: "mapping-profile", expectedRevision: 2 },
      dailyCost: 4,
      generateRecipe: "generate",
      reviewRecipe: "map-review",
      segmentation: { leafBytes: 1024, directBytes: 0, fanout: 64, maxDepth: 4 },
    },
    ...over,
  });
}

async function mapCapture(db: GuestDatabase, policy: Policy, session: string): Promise<void> {
  const route = mappingPolicy(policy);
  if (route === null) throw new Error("missing mapping policy");
  const now = new Date(NOW).toISOString();
  const hash = (value: string) => `sha256:${createHash("sha256").update(value).digest("hex")}`;
  const capture = {
    host: "archive-host",
    harness: "omp" as const,
    session,
    snapshot: "a".repeat(64),
    path: `sessions/${session}.jsonl`,
    capturedAt: now,
  };
  const source = TranscriptMapSourceSchema.parse({
    ...capture,
    id: transcriptMapCaptureId(capture),
    coordinates: SESSION_RECORD_COORDINATES,
    captureDigest: hash(session),
    sourceDigest: hash(session),
    bytes: 1024,
    records: 1,
  });
  const planId = transcriptMapPlanId(source, route.segmentation);
  const span = {
    firstRecord: 1,
    lastRecord: 1,
    byteOffset: 0,
    byteLength: 1024,
    digest: hash(session),
    anchor: { line: 1, byteOffset: 0, byteLength: 1024, digest: hash(session), time: null },
  };
  const node = TranscriptMapNodeSchema.parse({
    id: transcriptMapNodeId(planId, 0, 0, span, [], null),
    planId,
    parentId: null,
    level: 0,
    ordinal: 0,
    span,
    children: [],
    gap: null,
  });
  const plan = TranscriptMapPlanSchema.parse({
    id: planId,
    source,
    segmentation: route.segmentation,
    rootId: node.id,
    nodeCount: 1,
    digest: transcriptMapManifestDigest([node]),
    direct: false,
    gapBytes: 0,
  });
  const context = {
    digest: hash("inventory"),
    policyDigest: hash("classification"),
    classId: "private",
    ceiling: 2,
    eligibleCaptures: 2,
    observedAt: now,
  };
  await transcriptMaps({ db }).recordPlan({
    machineId: route.sourceMachineId,
    context,
    plan,
    nodes: [node],
    access: { captureId: source.id, contextDigest: context.digest, sensitivity: 2 },
    offset: 0,
    nextOffset: null,
    now,
  });
}

test("mapping requires selected enabled recipes from the shared library and a valid profile", () => {
  const policy = mapPolicy();
  expect(validatePolicy(policy, CONCURRENT_JOBS)).toBeNull();
  expect(validatePolicy({ ...policy, review: undefined }, CONCURRENT_JOBS)).not.toBeNull();
  expect(
    validatePolicy(
      { ...policy, mapping: { ...policy.mapping!, generateRecipe: "absent" } },
      CONCURRENT_JOBS,
    ),
  ).not.toBeNull();
  expect(
    PolicySchema.safeParse({
      ...policy,
      mapping: {
        ...policy.mapping!,
        profile: { containerId: "mapping-profile", expectedRevision: -1 },
      },
    }).success,
  ).toBe(false);
});

test("a standing draw never offers mapping, even with review enabled", async () => {
  const policy = mapPolicy({
    activityWeights: { review: 1, explore: 0, challenge: 0, synthesize: 0 },
  });
  const { db, coord } = await deployment(policy);
  await mapCapture(db, policy, "standing");
  const standing = await coord.draw({ runId: "standing", seed: 1n });
  expect(standing.outcome === "assignment" && standing.assignment.activity === "mapping").toBe(
    false,
  );
  expect(drawn(await coord.draw({ only: "mapping", runId: "drain", seed: 1n })).activity).toBe(
    "mapping",
  );
});

test("disabled mapping and a zero subcap cannot draw or newly claim queued work", async () => {
  for (const mode of ["disabled", "subcap"] as const) {
    const policy = mapPolicy();
    const { db, coord } = await deployment(policy);
    await mapCapture(db, policy, mode);
    const assignment = drawn(await coord.draw({ only: "mapping", runId: "before", seed: 1n }));
    const stopped =
      mode === "disabled"
        ? { ...policy, enabled: false }
        : { ...policy, mapping: { ...policy.mapping!, dailyCost: 0 } };
    await db.run(
      `INSERT INTO policies(version,seq,actor_id,reason,payload,recorded_at)
      VALUES('stopped',2,'operator','stop mapping',?,?)`,
      [JSON.stringify(stopped), ago(0)],
    );
    expect((await coord.draw({ only: "mapping", runId: "after", seed: 1n })).outcome).toBe("gap");
    expect((await coord.claim({ assignment, runId: "after", jobId: "prepare" })).outcome).toBe(
      "refused",
    );
    expect((await coord.spend()).total).toBe(0);
  }
});

test("mapping and review obey their own machine routes without treating maps as frontier records", async () => {
  const policy = mapPolicy({ concurrentPerMachine: 1 });
  const { db, coord } = await deployment(policy);
  await claimRow(db, "asg_review", "review-run", 1, null, 0, "review-job");
  await runOn(db, "review-job", policy.review!.machineId);
  await mapCapture(db, policy, "route");
  const assignment = drawn(await coord.draw({ only: "mapping", runId: "mapping-run", seed: 1n }));
  expect(assignment.activity).toBe("mapping");
  expect(
    (await coord.claim({ assignment, runId: "mapping-run", jobId: "map-prepare" })).outcome,
  ).toBe("granted");
  expect(await db.query(`SELECT id FROM records`)).toEqual([]);
});

test("racing mapping claims share the global ledger, mapping subcap and occupied machine slots", async () => {
  for (const bound of ["global", "mapping", "machine"] as const) {
    const base = mapPolicy();
    const policy = mapPolicy(
      bound === "mapping"
        ? { mapping: { ...base.mapping!, dailyCost: 1 } }
        : bound === "machine"
          ? { concurrentPerMachine: 1 }
          : {},
    );
    const { db, coord } = await deployment(policy);
    await mapCapture(db, policy, "one");
    await mapCapture(db, policy, "two");
    if (bound === "global") await claimRow(db, "asg_other_activity", "other", 3, 3);
    const one = drawn(await coord.draw({ only: "mapping", runId: "one", seed: 1n }));
    const two = drawn(await coord.draw({ only: "mapping", runId: "two", seed: 1n }));
    expect(two.id).not.toBe(one.id);
    const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
    const results = await Promise.all([
      coord.claim({ assignment: one, runId: "one", jobId: "prepare-one" }),
      other.claim({ assignment: two, runId: "two", jobId: "prepare-two" }),
    ]);
    expect(results.filter((result) => result.outcome === "granted")).toHaveLength(1);
    expect((await coord.spend()).mapping).toBe(1);
    expect((await coord.spend()).total).toBe(bound === "global" ? 4 : 1);
    expect((await coord.open()).total).toBe(1);
  }
});

test("mapping grant atomically rejects a queue attempt that changes after eligibility was read", async () => {
  const policy = mapPolicy();
  const { db, coord } = await deployment(policy);
  await mapCapture(db, policy, "stale-attempt");
  const assignment = drawn(await coord.draw({ only: "mapping", runId: "draw", seed: 1n }));
  if (assignment.activity !== "mapping") throw new Error("not mapping");
  let moved = false;
  const raced: GuestDatabase = {
    ...db,
    batch: async (statements) => {
      if (!moved && statements.some((statement) => statement.sql.includes("INSERT INTO claims"))) {
        moved = true;
        await db.run(
          `UPDATE transcript_map_work SET attempt=attempt+1,payload=json_set(payload,'$.attempt',attempt+1) WHERE id=?`,
          [assignment.work.id],
        );
      }
      return db.batch(statements);
    },
  };
  const other = coordinator({ db: raced }, () => NOW, CONCURRENT_JOBS);
  expect((await other.claim({ assignment, runId: "claim", jobId: "prepare" })).outcome).toBe(
    "refused",
  );
  expect(moved).toBe(true);
  expect((await coord.spend()).total).toBe(0);
});

test("mapping identities survive policy edits while a backed-off retry gets a new paid identity", async () => {
  const policy = mapPolicy();
  const { db, coord } = await deployment(policy);
  await mapCapture(db, policy, "retry");
  const first = drawn(await coord.draw({ only: "mapping", runId: "first", seed: 1n }));
  if (first.activity !== "mapping") throw new Error("not mapping");
  await db.run(
    `INSERT INTO policies(version,seq,actor_id,reason,payload,recorded_at)
    VALUES('edited',2,'operator','cadence edit',?,?)`,
    [JSON.stringify({ ...policy, version: "edited", cadenceSeconds: 90 }), ago(0)],
  );
  const other = coordinator({ db }, () => NOW, CONCURRENT_JOBS);
  expect(drawn(await other.draw({ only: "mapping", runId: "other", seed: 1n })).id).toBe(first.id);
  const grant = await coord.claim({ assignment: first, runId: "first", jobId: "prepare-first" });
  if (grant.outcome !== "granted") throw new Error("claim refused");
  const maps = transcriptMaps({ db });
  expect(await maps.startWork(first.work.id, grant.claim, ago(0))).toBe(true);
  await db.batch(
    await maps.failureStatements({
      workId: first.work.id,
      now: ago(0),
      guard: { sql: "1", params: [] },
      reason: "refused output",
    }),
  );
  await coord.finish({
    id: first.id,
    runId: "first",
    fence: grant.claim.fence,
    cost: 0,
    outcome: "failed",
  });
  expect(
    (await other.draw({ only: "mapping", runId: "too-soon", now: NOW + 59_000 })).outcome,
  ).toBe("gap");
  const retry = drawn(
    await other.draw({ only: "mapping", runId: "retry", now: NOW + 60_000, seed: 1n }),
  );
  if (retry.activity !== "mapping") throw new Error("not mapping");
  expect(retry.work.id).toBe(first.work.id);
  expect(retry.id).not.toBe(first.id);
  expect(
    (
      await other.claim({
        assignment: retry,
        runId: "retry",
        jobId: "prepare-retry",
        now: NOW + 60_000,
      })
    ).outcome,
  ).toBe("granted");
});

test("mapping records refused overruns in full and stops admission until the claim day rolls over", async () => {
  const base = mapPolicy();
  const policy = mapPolicy({ mapping: { ...base.mapping!, dailyCost: 2 } });
  const { db, coord } = await deployment(policy);
  await mapCapture(db, policy, "overrun-one");
  await mapCapture(db, policy, "overrun-two");
  const assignment = drawn(await coord.draw({ only: "mapping", runId: "spent", seed: 1n }));
  const waiting = drawn(await coord.draw({ only: "mapping", runId: "waiting", seed: 1n }));
  const grant = await coord.claim({ assignment, runId: "spent", jobId: "prepare-spent" });
  if (grant.outcome !== "granted") throw new Error("claim refused");
  expect(
    await coord.finish({
      id: assignment.id,
      runId: "spent",
      fence: grant.claim.fence,
      cost: 2.5,
      outcome: "failed",
    }),
  ).toMatchObject({ outcome: "finished", cost: 2.5, overrun: true });
  expect((await coord.spend()).mapping).toBe(2.5);
  expect((await coord.spend()).total).toBe(2.5);
  expect(
    (await coord.claim({ assignment: waiting, runId: "waiting", jobId: "prepare-waiting" }))
      .outcome,
  ).toBe("refused");
  expect((await coord.draw({ only: "mapping", runId: "blocked" })).outcome).toBe("gap");
  expect((await coord.spend(NOW + DAY)).mapping).toBe(0);
  expect(
    (
      await coord.claim({
        assignment: waiting,
        runId: "tomorrow",
        jobId: "prepare-tomorrow",
        now: NOW + DAY,
      })
    ).outcome,
  ).toBe("granted");
});

test("an unknown mapping posting stays occupied after expiry and retained mapping authority forbids a free release", async () => {
  const policy = mapPolicy();
  const { db, coord } = await deployment(policy);
  await mapCapture(db, policy, "unknown-post");
  const assignment = drawn(await coord.draw({ only: "mapping", runId: "held", seed: 1n }));
  const granted = await coord.claim({ assignment, runId: "held", jobId: "prepare-unknown" });
  if (granted.outcome !== "granted") throw new Error("claim refused");
  const expired = granted.claim.expiresAt + 1;
  expect((await coord.open(expired)).total).toBe(1);
  expect(
    (
      await coord.claim({
        assignment,
        runId: "replacement",
        jobId: "prepare-replacement",
        now: expired,
      })
    ).outcome,
  ).toBe("refused");
  await runOn(db, "retained-code", policy.mapping!.executorMachineId);
  await db.run(`UPDATE runs SET preparation=?,closure='failed' WHERE job_id='retained-code'`, [
    JSON.stringify({
      mapping: {
        claim: { id: granted.claim.id, runId: granted.claim.runId, fence: granted.claim.fence },
      },
    }),
  ]);
  expect(
    (
      await coord.finish({
        id: assignment.id,
        runId: "held",
        fence: granted.claim.fence,
        now: expired,
        cost: 0,
        outcome: "failed",
        unpostedJobId: "prepare-unknown",
      })
    ).outcome,
  ).toBe("refused");
  expect((await coord.spend(expired)).mapping).toBe(assignment.reservedCost);
});
