import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { ACTIONS, BABEL_PLUGIN_ID, EVENTS } from "../contract.ts";
import { DEFAULT_POLICY, newId, stamp, type ActsStore } from "../store/acts.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { actDoors } from "./acts.ts";
import type { Door } from "./door.ts";

/*
  The doors, exercised the way the runtime exercises them: the arguments go through the action's
  OWN input schema first — that is what the kit does before a handler is called, so a test that
  skipped it would be testing a function the host never calls — and the handler then runs against
  a real plugin database. What is asserted here is the door's own business: that the actor comes
  from the dispatch's principal and never from the arguments, that a refusal comes back as
  `{ refused }` rather than raising, that each act emits its event, and that the crossing is the
  owner's alone.
*/

const cleanup: string[] = [];

interface Emission {
  kind: string;
  payload: unknown;
}

interface Harness {
  store: ActsStore;
  doors: readonly Door[];
  ctx: GuestCtx;
  emitted: Emission[];
}

/**
 * `describable` is the machine ids this hub knows. A re-host writes a `sessions.host` and the
 * door will not write one the hub cannot describe, so the harness has to be able to answer
 * that question both ways (#310).
 */
function openHarness(
  principal = "alex",
  isRoot = false,
  describable: readonly string[] = [],
): Harness {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-doors-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  const store: ActsStore = { db, now: () => Date.UTC(2026, 8, 12, 12, 0, 0), touch: () => {} };
  const emitted: Emission[] = [];
  const ctx = {
    pluginId: BABEL_PLUGIN_ID,
    principal: { id: principal, kind: "human", name: principal },
    auth: {
      principal: { id: principal },
      caps: ["containers:write"],
      containerScope: null,
      isRoot,
    },
    emit: (_ref: unknown, kind: string, payload: unknown) => {
      emitted.push({ kind, payload });
    },
  } as unknown as GuestCtx;
  // The manifest's `limits.concurrentJobs`, as `server.ts` hands it to the act doors.
  const jobs = () => ({
    describe: async ({ machineId }: { machineId: string }) => {
      if (!describable.includes(machineId)) throw new Error("machine_unknown");
      return { machineId, connected: true } as never;
    },
  });
  return { store, doors: actDoors(store, 16, jobs as never), ctx, emitted };
}

afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

/** Parses the arguments against the door's own schema and runs it, exactly as the kit does. */
async function knock(harness: Harness, name: string, args: unknown): Promise<unknown> {
  const door = harness.doors.find((candidate) => candidate.action.name === name);
  if (door === undefined) throw new Error(`no door named ${name}`);
  const parsed = door.action.input.parse(args);
  const result = await door.handler(harness.ctx, parsed as never);
  return door.action.result.parse(result);
}

/** The same, for a call the door is expected to refuse: a refusal is not the result's shape. */
async function refusal(harness: Harness, name: string, args: unknown): Promise<string> {
  const door = harness.doors.find((candidate) => candidate.action.name === name);
  if (door === undefined) throw new Error(`no door named ${name}`);
  const result = (await door.handler(harness.ctx, door.action.input.parse(args) as never)) as {
    refused?: string;
  };
  if (typeof result.refused !== "string")
    throw new Error(`${name} did not refuse: ${JSON.stringify(result)}`);
  return result.refused;
}

async function migrate(store: ActsStore): Promise<void> {
  for (const statement of SCHEMA_V1) await store.db.run(statement);
}

async function seedRecord(store: ActsStore, id: string, kind = "proposal"): Promise<void> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES(?, ?, ?, NULL, 0, NULL, 'run_1', 'recipe', 1, 'run', 'run_1', 'a record', ?, '{}')`,
    [id, kind, id, stamp(store.now())],
  );
}

test("the twelve acts are declared, each carrying the write capability except the crossing's two", () => {
  const harness = openHarness();
  const names = harness.doors.map((door) => door.action.name);
  expect(names).toEqual([
    ACTIONS.rule,
    ACTIONS.comment,
    ACTIONS.answer,
    ACTIONS.interest,
    ACTIONS.file,
    ACTIONS.unfile,
    ACTIONS.tell,
    ACTIONS.setPolicy,
    ACTIONS.setBudget,
    ACTIONS.clearBudget,
    ACTIONS.importLedger,
    ACTIONS.rehostSessions,
  ]);
  // The crossing and its repair ask `isRoot` instead: no capability in the vocabulary means
  // "the owner". The repair also declares the machine read it needs to check its destination.
  const owners: readonly string[] = [ACTIONS.importLedger, ACTIONS.rehostSessions];
  for (const door of harness.doors) {
    expect(door.action.caps).toEqual(owners.includes(door.action.name) ? [] : ["containers:write"]);
    expect(door.action.title.length).toBeGreaterThan(0);
  }
  for (const name of owners) {
    const door = harness.doors.find((candidate) => candidate.action.name === name);
    expect(door?.action.delegates).toEqual(["machines:read"]);
  }
});

test("a ruling is attributed to the dispatch's principal and emits `ruled`", async () => {
  const harness = openHarness("alex");
  await migrate(harness.store);
  await seedRecord(harness.store, "pro_00000001");

  const ruled = await knock(harness, ACTIONS.rule, { id: "pro_00000001", ruling: "accept" });
  expect(ruled).toEqual({ id: "pro_00000001", standing: "accepted", seq: 1, plan: null });
  expect(harness.emitted).toEqual([
    {
      kind: EVENTS.ruled,
      payload: { id: "pro_00000001", ruling: "accept", standing: "accepted", seq: 1 },
    },
  ]);
  expect(
    await harness.store.db.query<{ actor_id: string }>(`SELECT actor_id FROM dispositions`),
  ).toEqual([{ actor_id: "alex" }]);
});

test("a ruling the store refuses comes back as a refusal, not a failure", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  expect(await refusal(harness, ACTIONS.rule, { id: "pro_ffffffff", ruling: "accept" })).toMatch(
    /no record pro_ffffffff/,
  );
  expect(harness.emitted).toEqual([]);
});

test("accepting a topic proposal through the door emits the plan event with the entity it created", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await seedRecord(harness.store, "pro_00000002");
  await harness.store.db.run(
    `INSERT INTO plans(id, kind, subject_kind, subject_id, operation, dedupe_key, payload,
       proposed_by_kind, proposed_by_id, state, created_at)
     VALUES(?, 'topic', 'proposal', 'pro_00000002', 'create', NULL, ?, 'run', 'run_1', 'open', ?)`,
    [
      newId("pln"),
      JSON.stringify({
        reasoning: "one checkout, many sessions",
        identity: "babel.git",
        name: "babel",
      }),
      stamp(harness.store.now()),
    ],
  );

  const ruled = (await knock(harness, ACTIONS.rule, { id: "pro_00000002", ruling: "accept" })) as {
    plan: { applied: boolean; entityId?: string };
  };
  expect(ruled.plan.applied).toBe(true);
  expect(harness.emitted.map((emission) => emission.kind)).toEqual([
    EVENTS.ruled,
    EVENTS.planApplied,
  ]);
  expect(harness.emitted[1]?.payload).toEqual({
    id: "pro_00000002",
    kind: "topic",
    operation: "create",
    entityId: ruled.plan.entityId,
  });
});

test("a comment defaults to a comment and a question is marked as one", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await seedRecord(harness.store, "pro_00000003");

  const said = (await knock(harness, ACTIONS.comment, {
    id: "pro_00000003",
    text: "reads well",
  })) as {
    question: boolean;
  };
  expect(said.question).toBe(false);
  const asked = (await knock(harness, ACTIONS.comment, {
    id: "pro_00000003",
    text: "what does it cost?",
    kind: "question",
  })) as { question: boolean };
  expect(asked.question).toBe(true);
  expect(
    await harness.store.db.query<{ question: bigint }>(
      `SELECT question FROM feedback ORDER BY question`,
    ),
  ).toEqual([{ question: 0n }, { question: 1n }]);
  expect(harness.emitted.map((emission) => emission.kind)).toEqual([
    EVENTS.recordWritten,
    EVENTS.recordWritten,
  ]);
});

test("the stance, the filing and its withdrawal all go through their own doors", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await seedRecord(harness.store, "fnd_00000004", "finding");
  const entityId = "ent_00000004";
  await harness.store.db.run(
    `INSERT INTO entities(id, kind, name, canonical_id, created_by, created_at)
     VALUES(?, 'repository', 'babel', ?, 'alex', ?)`,
    [entityId, entityId, stamp(harness.store.now())],
  );

  const stated = (await knock(harness, ACTIONS.interest, {
    entityId,
    state: "watching",
    reason: "keep an eye",
  })) as { facts: string[] };
  expect(stated.facts).toHaveLength(2);

  const filed = (await knock(harness, ACTIONS.file, {
    id: "fnd_00000004",
    entity: entityId,
    rationale: "it is about babel",
  })) as { id: string; withdrawn: boolean };
  expect(filed.withdrawn).toBe(false);

  const withdrawn = (await knock(harness, ACTIONS.unfile, {
    id: "fnd_00000004",
    entity: entityId,
    reason: "wrong topic",
  })) as { withdrawn: boolean; supersedes: string };
  expect(withdrawn).toMatchObject({ withdrawn: true, supersedes: filed.id });
  expect(
    await refusal(harness, ACTIONS.unfile, {
      id: "fnd_00000004",
      entity: entityId,
      reason: "again",
    }),
  ).toMatch(/is not filed under/);
});

test("telling Babel something threads, and a policy under the floor is refused at the door", async () => {
  const harness = openHarness();
  await migrate(harness.store);

  const first = (await knock(harness, ACTIONS.tell, {
    text: "the repository rules keep slipping",
  })) as { id: string; rootId: string; seq: number };
  expect(first).toMatchObject({ rootId: first.id, seq: 1 });
  const reply = (await knock(harness, ACTIONS.tell, {
    text: "on dev-01 especially",
    replyTo: first.id,
  })) as {
    rootId: string;
    seq: number;
  };
  expect(reply).toMatchObject({ rootId: first.id, seq: 2 });

  expect(
    await refusal(harness, ACTIONS.setPolicy, {
      policy: { ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24, concurrentPerMachine: 4 },
      reason: "faster",
    }),
  ).toMatch(/needs 480s/);
  // And a bound above what a machine will run is refused at the same door, naming the ceiling.
  expect(
    await refusal(harness, ACTIONS.setPolicy, {
      policy: { ...DEFAULT_POLICY, enabled: true, concurrentPerMachine: 24 },
      reason: "a drain by another name",
    }),
  ).toMatch(/above the 16 jobs a machine runs at once/);
  const installed = (await knock(harness, ACTIONS.setPolicy, {
    policy: { ...DEFAULT_POLICY, enabled: true },
    reason: "turning it on",
  })) as { version: string; seq: number };
  expect(installed).toMatchObject({ version: "1", seq: 1 });
});

test("the crossing is owner-only, hub-checked and idempotent by (table, id)", async () => {
  const guest = openHarness("someone-else", false);
  await migrate(guest.store);
  const row = {
    selector: "omp/abc",
    harness: "omp",
    source_id: "abc",
    seen_at: "2026-03-01T09:00:00.000000000Z",
  };
  const machineId = "05df7eaa-efd8-4d9c-bb0c-334706555c77";
  const chunk = { source: "durable.db", table: "sessions", rows: [{ ...row, host: machineId }] };
  expect(await refusal(guest, ACTIONS.importLedger, chunk)).toMatch(/the owner's act/);
  expect(await guest.store.db.query(`SELECT selector FROM sessions`)).toEqual([]);

  const owner = openHarness("alex", true, [machineId]);
  await migrate(owner.store);
  // A HOST THE HUB CANNOT DESCRIBE IS REFUSED BEFORE IT IS WRITTEN. `sessions.host` is handed to
  // `describe`, `listRuns` and `machines.repository` afterwards, so a Go host name there is a row
  // nothing can read back — which is how 588 of them arrived (#310).
  expect(
    await refusal(owner, ACTIONS.importLedger, {
      ...chunk,
      rows: [{ ...row, host: "dev-01" }],
    }),
  ).toMatch(/dev-01 is not a machine this hub can describe/);
  expect(await owner.store.db.query(`SELECT selector FROM sessions`)).toEqual([]);
  expect(await knock(owner, ACTIONS.importLedger, chunk)).toEqual({
    source: "durable.db",
    table: "sessions",
    inserted: 1,
    skipped: 0,
  });
  expect(await knock(owner, ACTIONS.importLedger, chunk)).toEqual({
    source: "durable.db",
    table: "sessions",
    inserted: 0,
    skipped: 1,
  });
  expect(await owner.store.db.query<{ host: string }>(`SELECT host FROM sessions`)).toEqual([
    { host: machineId },
  ]);
  // The run log's machine column is guarded by the same check, and needs its own case: with only
  // the sessions column asserted, dropping `runs.machine_id` from the guard breaks no test.
  // `machine_id` is handed to `describe` and `listRuns` exactly as a session's host is, so a run
  // crossed under a name is a run nothing can locate afterwards (#309's other half).
  const run = {
    id: "run_1",
    kind: "explore",
    started_at: "2026-03-01T09:00:00.000Z",
    payload: "{}",
  };
  expect(
    await refusal(owner, ACTIONS.importLedger, {
      source: "durable.db",
      table: "runs",
      rows: [{ ...run, machine_id: "dev-01" }],
    }),
  ).toMatch(/dev-01 is not a machine this hub can describe/);
  expect(await owner.store.db.query(`SELECT id FROM runs`)).toEqual([]);
  expect(
    await knock(owner, ACTIONS.importLedger, {
      source: "durable.db",
      table: "runs",
      rows: [{ ...run, machine_id: machineId }],
    }),
  ).toMatchObject({ table: "runs", inserted: 1 });
});

test("a re-host moves a catalogued corpus onto an id the hub knows, and refuses one it does not", async () => {
  const machineId = "05df7eaa-efd8-4d9c-bb0c-334706555c77";
  const owner = openHarness("alex", true, [machineId]);
  await migrate(owner.store);
  // Seeded as the store already holds them: these rows were written by a crossing that had no
  // hub check, which is what the act exists to repair. The door itself now refuses to make more.
  for (const selector of ["omp:one", "omp:two"]) {
    await owner.store.db.run(
      `INSERT INTO sessions(selector, host, harness, source_id, seen_at) VALUES(?, 'dev-01', 'omp', ?, ?)`,
      [selector, selector, "2026-09-12T12:00:00.000Z"],
    );
  }
  // A destination the hub cannot describe is refused: writing it would leave the rows exactly as
  // unreachable as the name they already carry, which is the defect and not the repair.
  expect(await refusal(owner, ACTIONS.rehostSessions, { from: "dev-01", to: "dev-02" })).toMatch(
    /dev-02 is not a machine this hub can describe/,
  );
  expect(
    await owner.store.db.query<{ host: string }>(`SELECT DISTINCT host FROM sessions`),
  ).toEqual([{ host: "dev-01" }]);
  expect(await knock(owner, ACTIONS.rehostSessions, { from: "dev-01", to: machineId })).toEqual({
    from: "dev-01",
    to: machineId,
    sessions: 2,
  });
  expect(
    await owner.store.db.query<{ host: string }>(`SELECT DISTINCT host FROM sessions`),
  ).toEqual([{ host: machineId }]);
  // Idempotent: the second run has nothing left under the name and says so rather than refusing.
  expect(
    await knock(owner, ACTIONS.rehostSessions, { from: "dev-01", to: machineId }),
  ).toMatchObject({
    sessions: 0,
  });
  // The owner's act, like the crossing it repairs.
  const guest = openHarness("guest", false, [machineId]);
  await migrate(guest.store);
  expect(await refusal(guest, ACTIONS.rehostSessions, { from: "dev-01", to: machineId })).toMatch(
    /the owner's act/,
  );
});

test("a door refuses arguments its schema does not admit before any handler runs", () => {
  const harness = openHarness();
  const rule = harness.doors.find((door) => door.action.name === ACTIONS.rule);
  expect(() => rule?.action.input.parse({ id: "pro_00000005", ruling: "burn-it" })).toThrow();
  expect(() => rule?.action.input.parse({ id: "not-a-record-id", ruling: "accept" })).toThrow();
  const interest = harness.doors.find((door) => door.action.name === ACTIONS.interest);
  expect(() =>
    interest?.action.input.parse({ entityId: "ent_00000001", state: "curious" }),
  ).toThrow();
});
