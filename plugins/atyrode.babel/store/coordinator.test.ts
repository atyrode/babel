/*
  The coordinator's own tests: the behaviours internal/evaluation's tests defended, ported onto
  the plugin's tables. Each one is a rule an operator would notice the loss of — the order of the
  refusals, the reservations the lanes actually are, the fence, and the floor under a lease — and
  none of them asserts a sentence or a shape for its own sake.

  The database is the real schema (SCHEMA_V1) on a real SQLite file, served through the same three
  verbs the engine serves a plugin: `query`, `run` and a `batch` that is one immediate
  transaction. A draw that passed against a mock of those and failed against SQL would have
  tested nothing.
*/

import { Database } from "bun:sqlite";
import { afterEach, expect, test } from "bun:test";
import type { GuestDatabase, GuestSqlParam, GuestSqlRow, GuestSqlStatement } from "@manifold/plugin-kit";
import { SCHEMA_V1 } from "./schema.ts";
import {
  applyBudget,
  budgetChanges,
  coordinator,
  DEFAULT_POLICY,
  leaseFloor,
  validateBudget,
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

async function status(db: GuestDatabase, recordId: string, state: string, daysAgo: number): Promise<void> {
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
    throw new Error(`expected an assignment, got the gap ${result.gap.reason}: ${result.gap.detail}`);
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
  expect(validatePolicy({ ...DEFAULT_POLICY, explorationShare: 0 }, ceiling)).toContain("protected");
  expect(validatePolicy({ ...DEFAULT_POLICY, discoveryShare: 0 }, ceiling)).toContain("protected");
  expect(validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0.6, filingShare: 0.3 }, ceiling)).toContain(
    "over-commit",
  );
  expect(validatePolicy({ ...DEFAULT_POLICY, maxItemReviews: 1 }, ceiling)).toContain("below initial reviews");
  expect(validatePolicy({ ...DEFAULT_POLICY, dailyCost: 0.1 }, ceiling)).toContain("below the per-cycle cost");
  expect(validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0 }, ceiling)).toBeNull();
  expect(validatePolicy({ ...DEFAULT_POLICY, filingShare: 0, backlogShare: 0 }, ceiling)).toBeNull();
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
    validateBudget({ ...standing, leaseSeconds: 300 }, { ...drain, concurrentPerMachine: 16 }, null),
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
  const lost: Policy = { ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24, concurrentPerMachine: 4 };
  expect(validateNewPolicy(lost, CONCURRENT_JOBS)).toContain("480s");
  // …and the same policy already stored keeps drawing: refusing it at draw time would stop every
  // review on the deployment until the operator noticed.
  expect(validatePolicy(lost, CONCURRENT_JOBS)).toBeNull();
  expect(validateNewPolicy(DEFAULT_POLICY, CONCURRENT_JOBS)).toBeNull();
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
    reason: "replaced",
    detail: "superseded, so no review of it is outstanding",
  });
  // The two work shares owe the same answer: nothing needed drawing is not nothing was drawn.
  expect(result.gaps.map((gap) => `${gap.role}:${gap.reason}`)).toContain("filing:empty");
  expect(result.gaps.map((gap) => `${gap.role}:${gap.reason}`)).toContain("backlog:empty");
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
  expect(new Set(filings.map((assignment) => assignment.recordId))).toEqual(new Set([heuristically]));
  // A filing draw is work, never a review: it must not arrive at a reviewer. And a challenge is
  // accounted to its own lane whichever reservation drew it, so a cycle can say how much went to
  // arguing rather than to reviewing.
  expect(draws.every((assignment) => (assignment.lane === "filing") === (assignment.role === "filing"))).toBe(
    true,
  );
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
  const withdrawn = (await sampleDraws(coord, 80)).filter((assignment) => assignment.role === "filing");
  expect(new Set(withdrawn.map((assignment) => assignment.recordId))).toEqual(new Set([heuristically]));
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

async function oneAssignment(): Promise<{ db: GuestDatabase; coord: Coordinator; assignment: Assignment }> {
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

test("renewal moves the lease forward only, and is refused after expiry or under another fence", async () => {
  const { coord, assignment } = await oneAssignment();
  const granted = await coord.claim({ assignment, runId: "run_a", now: NOW });
  if (granted.outcome !== "granted") throw new Error(granted.refusal.detail);

  const early = await coord.renew({ id: assignment.id, runId: "run_a", fence: 1, now: NOW + 60_000 });
  if (early.outcome !== "renewed") throw new Error(early.refusal.detail);
  expect(early.expiresAt).toBe(NOW + 60_000 + 900_000);

  // The expiry never moves backwards: a renewal is the holder keeping the authority it has.
  const backwards = await coord.renew({ id: assignment.id, runId: "run_a", fence: 1, now: NOW + 1000 });
  if (backwards.outcome !== "renewed") throw new Error(backwards.refusal.detail);
  expect(backwards.expiresAt).toBe(early.expiresAt);

  const wrongFence = await coord.renew({ id: assignment.id, runId: "run_a", fence: 2, now: NOW + 1000 });
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
  const held = await coord.renew({ id: assignment.id, runId: "run_b", fence: 2, now: later + 2000 });
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
  const { coord, assignment } = await oneAssignment();
  const again = drawn(await coord.draw({ runId: "cycle_2", now: NOW, seed: 3n }));
  expect(again.id).toBe(assignment.id);

  const first = await coord.claim({ assignment, runId: "cycle_1", now: NOW });
  const second = await coord.claim({ assignment: again, runId: "cycle_2", now: NOW });
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
  const { db, coord } = await deployment({ enabled: true, batchSize: 1, perCycleCost: 0.1, dailyCost: 0.2 });
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
  await overlay(db, "bdg_first", { createdAt: NOW - 5000, expiresAt: NOW + 600_000, concurrentPerMachine: 8 });
  await overlay(db, "bdg_second", {
    createdAt: NOW - 1000,
    expiresAt: NOW + 60_000,
    concurrentPerMachine: 16,
    clearedAt: NOW,
  });
  expect((await coord.policy(NOW)).policy.batchSize).toBe(8);

  await db.run(`UPDATE budgets SET cleared_at = ? WHERE id = 'bdg_first'`, [new Date(NOW).toISOString()]);
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
  expect(drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: fleet })).recordId).toBe(id);

  // …and a caller that cannot say where the work would run is judged against ONE machine's
  // worth, so a draw with no fleet named never claims the fan a fleet would allow.
  const alone = await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n });
  if (alone.outcome !== "gap") throw new Error("an unnamed fleet drew against the whole deployment");
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
    drawn(await coord.draw({ runId: "cycle_1", now: NOW, seed: 3n, machines: ["dev-01"] })).recordId,
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
  expect(validateBudget(short, { ...drain, concurrentPerMachine: 16 }, CONCURRENT_JOBS)).toContain("320s");
  // The standing rules, judged against the policy the overlay would produce.
  expect(validateBudget(standing, { ...drain, dailyCost: 0.1 }, CONCURRENT_JOBS)).toContain(
    "below the per-cycle cost",
  );
  expect(validateBudget(standing, { ...drain, perCycleCost: 0 }, CONCURRENT_JOBS)).toContain("must be positive");
  expect(
    validateBudget(standing, { ...drain, expiresAt: NOW, concurrentPerMachine: 8 }, CONCURRENT_JOBS),
  ).toContain("no time at all");
  expect(
    validateBudget(standing, { ...drain, concurrentPerMachine: 8, perCycleCost: 1, dailyCost: 2 }, CONCURRENT_JOBS),
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
