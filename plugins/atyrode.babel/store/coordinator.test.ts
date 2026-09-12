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
  coordinator,
  DEFAULT_POLICY,
  leaseFloor,
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
 *  specifies and as `openPluginDatabase` implements it. */
function store(): { db: GuestDatabase } {
  const file = new Database(":memory:");
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
        return { changes: result.changes, lastInsertRowid: Number(result.lastInsertRowid) };
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

interface Seeded {
  readonly db: GuestDatabase;
  readonly coord: Coordinator;
}

async function deployment(policy?: Partial<Policy>): Promise<Seeded> {
  const handle = store();
  const coord = coordinator(handle, () => NOW);
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
): Promise<void> {
  await db.run(
    `INSERT INTO claims(id, record_id, role, lane, policy_version, run_id, fence, reserved_cost,
       actual_cost, granted_at, expires_at, finished_at, outcome)
     VALUES(?,?,'reception','weighted','1',?,1,?,?,?,?,?,?)`,
    [
      id,
      "hyp_ffffffff",
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
  expect(validatePolicy(DEFAULT_POLICY)).toBeNull();
  expect(validatePolicy({ ...DEFAULT_POLICY, explorationShare: 0 })).toContain("protected");
  expect(validatePolicy({ ...DEFAULT_POLICY, discoveryShare: 0 })).toContain("protected");
  expect(validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0.6, filingShare: 0.3 })).toContain(
    "over-commit",
  );
  expect(validatePolicy({ ...DEFAULT_POLICY, maxItemReviews: 1 })).toContain("below initial reviews");
  expect(validatePolicy({ ...DEFAULT_POLICY, dailyCost: 0.1 })).toContain("below the per-cycle cost");
  expect(validatePolicy({ ...DEFAULT_POLICY, coverageShare: 0 })).toBeNull();
  expect(validatePolicy({ ...DEFAULT_POLICY, filingShare: 0, backlogShare: 0 })).toBeNull();
});

test("the lease floor refuses a new policy that would need renewal to work at all", () => {
  // Measured: 20s per assignment in the batch, never under five minutes.
  expect(leaseFloor(1)).toBe(300);
  expect(leaseFloor(4)).toBe(300);
  expect(leaseFloor(24)).toBe(480);

  const lost: Policy = { ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24 };
  expect(validateNewPolicy(lost)).toContain("480s");
  // …and the same policy already stored keeps drawing: refusing it at draw time would stop every
  // review on the deployment until the operator noticed.
  expect(validatePolicy(lost)).toBeNull();
  expect(validateNewPolicy(DEFAULT_POLICY)).toBeNull();
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
  await claimRow(batched.db, "asg_open", "cycle_1", 0.01, null);
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
  expect(rows[0]?.["claims"]).toBe(0);
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
