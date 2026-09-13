import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlRow } from "@manifold/plugin";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID } from "../contract.ts";
import {
  ActRefused,
  DEFAULT_POLICY,
  NO_TOPIC,
  applyPlan,
  answer,
  comment,
  declinePlan,
  file,
  importLedger,
  importableTables,
  interest,
  leaseFloor,
  newId,
  validateNewPolicy,
  rule,
  setPolicy,
  stamp,
  standingOf,
  tell,
  unfile,
  type ActsStore,
} from "./acts.ts";
import { SCHEMA_V1 } from "./schema.ts";

/*
  These run against a REAL plugin database — the engine's own file, opened by
  `openPluginDatabase`, migrated with `SCHEMA_V1` — because every guarantee under test is the
  store's rather than this module's: the triggers that refuse an edit, the uniqueness that makes
  a double-click impossible, the foreign keys that refuse a filing under nothing. A fake handle
  would pass while the real one refused.
*/

const cleanup: string[] = [];

function openStore(at = Date.UTC(2026, 8, 12, 12, 0, 0)): ActsStore & { db: PluginDatabase; clock: { now: number } } {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-acts-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  const clock = { now: at };
  return {
    db,
    clock,
    now: () => clock.now,
    touch: () => {
      touched += 1;
    },
  };
}

let touched = 0;

afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

async function migrate(store: ActsStore): Promise<void> {
  for (const statement of SCHEMA_V1) await store.db.run(statement);
}

/** One record revision, as a run would have written it. */
async function seedRecord(
  store: ActsStore,
  id: string,
  kind = "proposal",
  title = "a proposal",
): Promise<string> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES(?, ?, ?, NULL, 0, NULL, 'run_1', 'recipe', 1, 'run', 'run_1', ?, ?, '{}')`,
    [id, kind, id, title, stamp(store.now())],
  );
  return id;
}

async function seedEntity(store: ActsStore, id: string, name: string): Promise<string> {
  await store.db.run(
    `INSERT INTO entities(id, kind, name, canonical_id, created_by, created_at) VALUES(?, 'repository', ?, ?, 'operator', ?)`,
    [id, name, id, stamp(store.now())],
  );
  await store.db.run(
    `INSERT INTO aliases(id, entity_id, kind, value, value_key, retired_at, created_at)
     VALUES(?, ?, 'name', ?, ?, NULL, ?)`,
    [newId("als"), id, name, name.toLowerCase(), stamp(store.now())],
  );
  return id;
}

async function seedPlan(
  store: ActsStore,
  args: { id: string; kind: "topic" | "backlog"; subjectId: string; operation: string; payload: unknown; by?: string },
): Promise<string> {
  await store.db.run(
    `INSERT INTO plans(id, kind, subject_kind, subject_id, operation, dedupe_key, payload,
       proposed_by_kind, proposed_by_id, state, created_at)
     VALUES(?, ?, 'proposal', ?, ?, NULL, ?, 'run', ?, 'open', ?)`,
    [args.id, args.kind, args.subjectId, args.operation, JSON.stringify(args.payload), args.by ?? "run_1", stamp(store.now())],
  );
  return args.id;
}

async function rows<Row extends SqlRow>(store: ActsStore, sql: string, params: readonly string[] = []): Promise<readonly Row[]> {
  return await store.db.query<Row>(sql, params);
}

const OPERATOR = "alex";

// ---------------------------------------------------------------------------- rulings

test("a ruling appends, and the row it wrote can never be edited or deleted", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000001");

  const first = await rule(store, { id: "pro_00000001", ruling: "defer", note: "next week" }, OPERATOR);
  expect(first).toMatchObject({ standing: "deferred", seq: 1, plan: null });

  const second = await rule(store, { id: "pro_00000001", ruling: "accept", note: "" }, OPERATOR);
  expect(second).toMatchObject({ standing: "accepted", seq: 2 });

  const history = await rows<{ seq: number; disposition: string; note: string }>(
    store,
    `SELECT seq, disposition, note FROM dispositions WHERE record_id = ? ORDER BY seq`,
    ["pro_00000001"],
  );
  expect(history).toEqual([
    { seq: 1, disposition: "defer", note: "next week" },
    { seq: 2, disposition: "accept", note: "" },
  ]);

  expect(store.db.run(`UPDATE dispositions SET note = 'rewritten' WHERE seq = 1`)).rejects.toThrow(
    /never edited/,
  );
  expect(store.db.run(`DELETE FROM dispositions WHERE seq = 1`)).rejects.toThrow(/never deleted/);
});

test("a ruling that says nothing new, and one on a record decided elsewhere, are refused", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000002");
  await seedRecord(store, "pro_00000003");

  expect(rule(store, { id: "pro_00000002", ruling: "reopen", note: "new evidence" }, OPERATOR)).rejects.toThrow(
    /nothing has been decided here to reopen/,
  );
  await rule(store, { id: "pro_00000002", ruling: "reject", note: "no" }, OPERATOR);
  expect(rule(store, { id: "pro_00000002", ruling: "reject", note: "still no" }, OPERATOR)).rejects.toThrow(
    /already rejected/,
  );
  expect(rule(store, { id: "pro_00000002", ruling: "reopen", note: "" }, OPERATOR)).rejects.toThrow(
    /states no reason/,
  );
  const reopened = await rule(store, { id: "pro_00000002", ruling: "reopen", note: "new evidence" }, OPERATOR);
  expect(reopened.standing).toBe("new");

  await rule(
    store,
    { id: "pro_00000003", ruling: "duplicate", note: "", duplicateOf: "pro_00000002" },
    OPERATOR,
  );
  expect(rule(store, { id: "pro_00000003", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /decided at the record it duplicates/,
  );
  expect(rule(store, { id: "pro_00000002", ruling: "duplicate", note: "" }, OPERATOR)).rejects.toThrow(
    /names no original/,
  );
  expect(rule(store, { id: "pro_99999999", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /no record pro_99999999/,
  );
});

test("standing is derived from the newest ruling, and refine is one row", async () => {
  expect(standingOf(null)).toBe("new");
  expect(standingOf("refine")).toBe("refine-requested");
  expect(standingOf("nonsense")).toBe("new");

  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000004");
  const refined = await rule(store, { id: "pro_00000004", ruling: "refine", note: "narrow it" }, OPERATOR);
  expect(refined.standing).toBe("refine-requested");
  expect(rule(store, { id: "pro_00000004", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /decided at the descendant/,
  );
});

// ---------------------------------------------------------------------------- topic plans

test("accepting a topic proposal creates the entity, binds it and files the records", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000010");
  await seedRecord(store, "hyp_00000011", "hypothesis", "a candidate");
  await seedPlan(store, {
    id: "pln_00000010",
    kind: "topic",
    subjectId: "pro_00000010",
    operation: "create",
    payload: {
      reasoning: "every session on dev-01 touches this checkout",
      identity: "git@github.com:atyrode/babel.git",
      name: "babel",
      entityKind: "repository",
      aliases: [{ kind: "path", value: "/home/alex/babel" }],
      binding: [{ predicate: "repository-remote", value: "git@github.com:atyrode/babel.git" }],
      records: [{ id: "hyp_00000011", rationale: "the candidate is about this repository" }],
      sessions: 12,
    },
  });

  const ruled = await rule(store, { id: "pro_00000010", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.plan).toMatchObject({ kind: "topic", operation: "create", applied: true, declined: false });
  const entityId = ruled.plan?.entityId ?? "";
  expect(entityId).toMatch(/^ent_[0-9a-f]{16}$/);

  const entity = await rows<{ name: string; kind: string; canonical_id: string; created_by: string }>(
    store,
    `SELECT name, kind, canonical_id, created_by FROM entities WHERE id = ?`,
    [entityId],
  );
  expect(entity).toEqual([{ name: "babel", kind: "repository", canonical_id: entityId, created_by: OPERATOR }]);

  const aliases = await rows<{ kind: string; value: string; value_key: string }>(
    store,
    `SELECT kind, value, value_key FROM aliases WHERE entity_id = ? ORDER BY kind`,
    [entityId],
  );
  expect(aliases).toEqual([
    { kind: "identifier", value: "git@github.com:atyrode/babel.git", value_key: "git@github.com:atyrode/babel.git" },
    { kind: "name", value: "babel", value_key: "babel" },
    { kind: "path", value: "/home/alex/babel", value_key: "/home/alex/babel" },
  ]);

  const facts = await rows<{ predicate: string; value: string; authority_kind: string; authority_id: string }>(
    store,
    `SELECT predicate, value, authority_kind, authority_id FROM facts WHERE entity_id = ?`,
    [entityId],
  );
  expect(facts).toEqual([
    {
      predicate: "repository-remote",
      value: "git@github.com:atyrode/babel.git",
      authority_kind: "operator",
      authority_id: OPERATOR,
    },
  ]);
  expect(
    await rows<{ status: string }>(
      store,
      `SELECT status FROM fact_status WHERE fact_id = (SELECT id FROM facts WHERE entity_id = ?)`,
      [entityId],
    ),
  ).toEqual([{ status: "active" }]);

  // The filing is the RUN's: it judged the membership, and the operator accepted the topic.
  const filings = await rows<{ record_id: string; entity_id: string; author_kind: string; author_id: string; rationale: string }>(
    store,
    `SELECT record_id, entity_id, author_kind, author_id, rationale FROM filings WHERE entity_id = ?`,
    [entityId],
  );
  expect(filings).toEqual([
    {
      record_id: "hyp_00000011",
      entity_id: entityId,
      author_kind: "run",
      author_id: "run_1",
      rationale: "the candidate is about this repository",
    },
  ]);

  const plan = await rows<{ state: string; ruled_by: string; result: string }>(
    store,
    `SELECT state, ruled_by, result FROM plans WHERE id = ?`,
    ["pln_00000010"],
  );
  expect(plan[0]?.state).toBe("applied");
  expect(plan[0]?.ruled_by).toBe(OPERATOR);
  expect(JSON.parse(String(plan[0]?.result))).toMatchObject({ entityId, filed: [expect.any(String)] });
});

test("rejecting a topic proposal declines the plan with the note as the reason, and an empty note is refused first", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000020");
  await seedPlan(store, {
    id: "pln_00000020",
    kind: "topic",
    subjectId: "pro_00000020",
    operation: "create",
    payload: { reasoning: "one session mentions it", identity: "acme", name: "acme" },
  });

  expect(rule(store, { id: "pro_00000020", ruling: "reject", note: "   " }, OPERATOR)).rejects.toThrow(
    /keeps the reason verbatim/,
  );
  // Refused BEFORE the ruling: the record is still undecided and the plan still open.
  expect(await rows(store, `SELECT seq FROM dispositions WHERE record_id = ?`, ["pro_00000020"])).toEqual([]);
  expect((await rows<{ state: string }>(store, `SELECT state FROM plans WHERE id = ?`, ["pln_00000020"]))[0]?.state).toBe(
    "open",
  );

  const ruled = await rule(
    store,
    { id: "pro_00000020", ruling: "reject", note: "one session is not a topic" },
    OPERATOR,
  );
  expect(ruled.plan).toMatchObject({ declined: true, applied: false });
  const plan = await rows<{ state: string; ruling_reason: string }>(
    store,
    `SELECT state, ruling_reason FROM plans WHERE id = ?`,
    ["pln_00000020"],
  );
  expect(plan).toEqual([{ state: "declined", ruling_reason: "one session is not a topic" }]);
  expect(await rows(store, `SELECT id FROM entities`)).toEqual([]);
});

test("a plan the ledger has moved past leaves the ruling standing and reports why", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000030");
  const target = await seedEntity(store, "ent_00000030", "dev-01");
  await seedPlan(store, {
    id: "pln_00000030",
    kind: "topic",
    subjectId: "pro_00000030",
    operation: "retire",
    payload: { reasoning: "the machine is gone", targets: [target] },
  });
  // Somebody merged the topic away after the proposal was written.
  await seedEntity(store, "ent_00000031", "dev-02");
  await store.db.run(`UPDATE entities SET canonical_id = ? WHERE id = ?`, ["ent_00000031", target]);

  const ruled = await rule(store, { id: "pro_00000030", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.seq).toBe(1);
  expect(ruled.plan?.applied).toBe(false);
  expect(ruled.plan?.error).toMatch(/was merged into ent_00000031/);
  expect((await rows<{ state: string }>(store, `SELECT state FROM plans WHERE id = ?`, ["pln_00000030"]))[0]?.state).toBe(
    "open",
  );
});

test("a proposal carrying two plans is refused before anything is appended", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000040");
  await seedPlan(store, {
    id: "pln_00000040",
    kind: "topic",
    subjectId: "pro_00000040",
    operation: "create",
    payload: { reasoning: "why", identity: "x", name: "x" },
  });
  await seedPlan(store, {
    id: "pln_00000041",
    kind: "backlog",
    subjectId: "pro_00000040",
    operation: "retire",
    payload: { reasoning: "why", hypotheses: ["hyp_00000041"] },
  });
  expect(rule(store, { id: "pro_00000040", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /two different changes/,
  );
  expect(await rows(store, `SELECT seq FROM dispositions`)).toEqual([]);
});

test("a merge folds the identity and a retirement is a lifecycle fact", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000050");
  await seedRecord(store, "pro_00000051");
  const source = await seedEntity(store, "ent_00000050", "babel-old");
  const target = await seedEntity(store, "ent_00000051", "babel");
  await seedPlan(store, {
    id: "pln_00000050",
    kind: "topic",
    subjectId: "pro_00000050",
    operation: "merge",
    payload: { reasoning: "one repository under two names", targets: [source, target] },
  });
  const merged = await rule(store, { id: "pro_00000050", ruling: "accept", note: "" }, OPERATOR);
  expect(merged.plan).toMatchObject({ applied: true, operation: "merge", entityId: target });
  expect(
    (await rows<{ canonical_id: string }>(store, `SELECT canonical_id FROM entities WHERE id = ?`, [source]))[0],
  ).toEqual({ canonical_id: target });
  const members = await rows<{ role: string; entity_id: string }>(
    store,
    `SELECT role, entity_id FROM resolution_members ORDER BY role`,
  );
  expect(members).toEqual([
    { role: "source", entity_id: source },
    { role: "target", entity_id: target },
  ]);

  // Two things of different kinds are not one thing said twice.
  await seedRecord(store, "pro_00000052");
  await store.db.run(
    `INSERT INTO entities(id, kind, name, canonical_id, created_by, created_at)
     VALUES('ent_00000052', 'project', 'the babel project', 'ent_00000052', 'alex', ?)`,
    [stamp(store.now())],
  );
  await seedPlan(store, {
    id: "pln_00000052",
    kind: "topic",
    subjectId: "pro_00000052",
    operation: "merge",
    payload: { reasoning: "same name", targets: [target, "ent_00000052"] },
  });
  const crossed = await rule(store, { id: "pro_00000052", ruling: "accept", note: "" }, OPERATOR);
  expect(crossed.plan?.error).toMatch(/is a repository and ent_00000052 is a project/);

  await seedPlan(store, {
    id: "pln_00000051",
    kind: "topic",
    subjectId: "pro_00000051",
    operation: "retire",
    payload: { reasoning: "it should never have existed", targets: [target] },
  });
  const retired = await rule(store, { id: "pro_00000051", ruling: "accept", note: "" }, OPERATOR);
  expect(retired.plan?.applied).toBe(true);
  expect(
    await rows<{ predicate: string; value: string; note: string }>(
      store,
      `SELECT predicate, value, note FROM facts WHERE entity_id = ?`,
      [target],
    ),
  ).toEqual([{ predicate: "lifecycle", value: "retired", note: "it should never have existed" }]);
});

test("a split carves the new topic out, naming the parent it came from, and declining one is its own act", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000055");
  await seedRecord(store, "pro_00000056");
  await seedRecord(store, "fnd_00000055", "finding", "about the web half only");
  const parent = await seedEntity(store, "ent_00000055", "babel");
  await seedPlan(store, {
    id: "pln_00000055",
    kind: "topic",
    subjectId: "pro_00000055",
    operation: "split",
    payload: {
      reasoning: "the web half is its own thing",
      identity: "babel/web",
      name: "babel-web",
      entityKind: "project",
      targets: [parent],
      records: [{ id: "fnd_00000055", rationale: "it is about the web half" }],
    },
  });

  const split = await rule(store, { id: "pro_00000055", ruling: "accept", note: "" }, OPERATOR);
  const part = split.plan?.entityId ?? "";
  expect(split.plan).toMatchObject({ operation: "split", applied: true });
  expect(
    (await rows<{ name: string; kind: string }>(store, `SELECT name, kind FROM entities WHERE id = ?`, [part]))[0],
  ).toEqual({ name: "babel-web", kind: "project" });
  expect(
    await rows<{ kind: string; role: string; entity_id: string }>(
      store,
      `SELECT r.kind, m.role, m.entity_id FROM resolutions r JOIN resolution_members m ON m.resolution_id = r.id
       ORDER BY m.role`,
    ),
  ).toEqual([
    { kind: "split", role: "parent", entity_id: parent },
    { kind: "split", role: "part", entity_id: part },
  ]);
  // The parent keeps its facts and its history; the records the plan named move by being filed
  // under the part.
  expect(
    await rows<{ record_id: string; entity_id: string }>(store, `SELECT record_id, entity_id FROM filings`),
  ).toEqual([{ record_id: "fnd_00000055", entity_id: part }]);

  await seedPlan(store, {
    id: "pln_00000056",
    kind: "topic",
    subjectId: "pro_00000056",
    operation: "retire",
    payload: { reasoning: "gone", targets: [parent] },
  });
  const declined = await declinePlan(store, "pro_00000056", OPERATOR, "the topic is still in use");
  expect(declined).toMatchObject({ kind: "topic", operation: "retire", declined: true, applied: false });
  expect(
    (
      await rows<{ state: string; ruling_reason: string }>(
        store,
        `SELECT state, ruling_reason FROM plans WHERE id = ?`,
        ["pln_00000056"],
      )
    )[0],
  ).toEqual({ state: "declined", ruling_reason: "the topic is still in use" });
  expect(declinePlan(store, "pro_00000056", OPERATOR, "  ")).rejects.toThrow(/keeps the operator's reason/);
});

test("a plan naming a record the store does not hold is refused whole, and the topic is not created", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000057");
  await seedPlan(store, {
    id: "pln_00000057",
    kind: "topic",
    subjectId: "pro_00000057",
    operation: "create",
    payload: {
      reasoning: "a topic for records that are not here",
      identity: "ghost",
      name: "ghost",
      records: [{ id: "hyp_deadbeef" }],
    },
  });
  const ruled = await rule(store, { id: "pro_00000057", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.plan?.error).toMatch(/no record hyp_deadbeef/);
  expect(await rows(store, `SELECT id FROM entities`)).toEqual([]);
  expect(await rows(store, `SELECT id FROM filings`)).toEqual([]);
  expect(ruled.seq).toBe(1);
});

// ---------------------------------------------------------------------------- backlog plans

test("accepting a backlog supersession settles the candidate and links the one that replaced it", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000060");
  await seedRecord(store, "hyp_00000060", "hypothesis", "the old wording");
  await seedRecord(store, "hyp_00000061", "hypothesis", "the better wording");
  await store.db.run(
    `INSERT INTO status_events(id, record_id, seq, status, run_id, actor_kind, actor_id, reason, recorded_at)
     VALUES(?, 'hyp_00000060', 1, 'deferred', 'run_1', 'run', 'run_1', 'the run stopped', ?)`,
    [newId("sev"), stamp(store.now())],
  );
  await seedPlan(store, {
    id: "pln_00000060",
    kind: "backlog",
    subjectId: "pro_00000060",
    operation: "supersede",
    payload: {
      reasoning: "the newer candidate says it better",
      hypotheses: ["hyp_00000060"],
      supersededBy: "hyp_00000061",
    },
  });

  const ruled = await rule(store, { id: "pro_00000060", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.plan).toMatchObject({ kind: "backlog", operation: "supersede", applied: true });
  expect(
    await rows<{ seq: number; status: string; actor_kind: string; actor_id: string }>(
      store,
      `SELECT seq, status, actor_kind, actor_id FROM status_events WHERE record_id = ? ORDER BY seq`,
      ["hyp_00000060"],
    ),
  ).toEqual([
    { seq: 1, status: "deferred", actor_kind: "run", actor_id: "run_1" },
    { seq: 2, status: "superseded", actor_kind: "operator", actor_id: OPERATOR },
  ]);
  expect(
    await rows<{ kind: string; from_id: string; to_id: string }>(
      store,
      `SELECT kind, from_id, to_id FROM edges`,
    ),
  ).toEqual([{ kind: "supersedes", from_id: "hyp_00000061", to_id: "hyp_00000060" }]);
});

test("a consolidation promotes every candidate the finding speaks for", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000065");
  for (const id of ["hyp_00000065", "hyp_00000066"]) {
    await seedRecord(store, id, "hypothesis", "a deferred candidate");
    await store.db.run(
      `INSERT INTO status_events(id, record_id, seq, status, run_id, actor_kind, actor_id, reason, recorded_at)
       VALUES(?, ?, 1, 'deferred', 'run_1', 'run', 'run_1', 'the run stopped', ?)`,
      [newId("sev"), id, stamp(store.now())],
    );
  }
  await seedPlan(store, {
    id: "pln_00000065",
    kind: "backlog",
    subjectId: "pro_00000065",
    operation: "consolidate",
    payload: {
      reasoning: "one finding speaks for both",
      hypotheses: ["hyp_00000065", "hyp_00000066"],
      finding: "fnd_00000065",
    },
  });

  const ruled = await rule(store, { id: "pro_00000065", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.plan).toMatchObject({ operation: "consolidate", applied: true });
  expect(
    await rows<{ record_id: string; status: string }>(
      store,
      `SELECT record_id, status FROM status_events WHERE seq = 2 ORDER BY record_id`,
    ),
  ).toEqual([
    { record_id: "hyp_00000065", status: "promoted" },
    { record_id: "hyp_00000066", status: "promoted" },
  ]);
  expect(
    JSON.parse(String((await rows<{ result: string }>(store, `SELECT result FROM plans WHERE id = ?`, ["pln_00000065"]))[0]?.result)),
  ).toMatchObject({ settled: ["hyp_00000065", "hyp_00000066"] });
  // Nothing was minted on the ledger: a consolidation is frontier history.
  expect(await rows(store, `SELECT id FROM facts`)).toEqual([]);
});

test("a candidate that is no longer deferred refuses the application", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000070");
  await seedRecord(store, "hyp_00000070", "hypothesis", "already promoted");
  await store.db.run(
    `INSERT INTO status_events(id, record_id, seq, status, run_id, actor_kind, actor_id, reason, recorded_at)
     VALUES(?, 'hyp_00000070', 1, 'promoted', NULL, 'operator', 'alex', 'an earlier plan', ?)`,
    [newId("sev"), stamp(store.now())],
  );
  await seedPlan(store, {
    id: "pln_00000070",
    kind: "backlog",
    subjectId: "pro_00000070",
    operation: "retire",
    payload: { reasoning: "not worth returning to", hypotheses: ["hyp_00000070"] },
  });
  const ruled = await rule(store, { id: "pro_00000070", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.plan?.error).toMatch(/is promoted, not deferred/);
});

test("a promotion asserts the fact under the operator's authority and applyPlan stands alone", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000080");
  await seedRecord(store, "hyp_00000080", "hypothesis", "the observation's claim");
  const entityId = await seedEntity(store, "ent_00000080", "dev-01");
  await store.db.run(
    `INSERT INTO status_events(id, record_id, seq, status, run_id, actor_kind, actor_id, reason, recorded_at)
     VALUES(?, 'hyp_00000080', 1, 'deferred', 'run_1', 'run', 'run_1', 'deferred', ?)`,
    [newId("sev"), stamp(store.now())],
  );
  await seedPlan(store, {
    id: "pln_00000080",
    kind: "backlog",
    subjectId: "pro_00000080",
    operation: "promote",
    payload: {
      reasoning: "the observation is established",
      hypotheses: ["hyp_00000080"],
      observation: "obs_00000080",
      fact: { entityId, predicate: "ownership", value: "alex" },
    },
  });

  const outcome = await applyPlan(store, "pro_00000080", OPERATOR, "accepted by hand");
  expect(outcome).toMatchObject({ kind: "backlog", operation: "promote", applied: true, entityId });
  expect(
    await rows<{ predicate: string; value: string; confidence: string; authority_id: string }>(
      store,
      `SELECT predicate, value, confidence, authority_id FROM facts WHERE entity_id = ?`,
      [entityId],
    ),
  ).toEqual([{ predicate: "ownership", value: "alex", confidence: "high", authority_id: OPERATOR }]);
  expect((await rows<{ status: string }>(store, `SELECT status FROM status_events WHERE record_id = ? ORDER BY seq DESC LIMIT 1`, ["hyp_00000080"]))[0]).toEqual(
    { status: "promoted" },
  );
  // One plan, one ruling: a second application finds nothing open.
  expect(applyPlan(store, "pro_00000080", OPERATOR, "again")).rejects.toThrow(/carries no open plan/);
  expect(declinePlan(store, "pro_00000080", OPERATOR, "no")).rejects.toThrow(/carries no open plan/);
});

// ---------------------------------------------------------------------------- interest

test("interest round-trips through the facts and supersedes the stance it replaces", async () => {
  const store = openStore();
  await migrate(store);
  const entityId = await seedEntity(store, "ent_00000090", "babel");

  const working = await interest(store, { entityId, state: "working", reason: "shipping it" }, OPERATOR);
  expect(working.facts).toHaveLength(2);
  expect(
    await rows<{ predicate: string; value: string; note: string }>(
      store,
      `SELECT predicate, value, note FROM facts WHERE entity_id = ? ORDER BY predicate`,
      [entityId],
    ),
  ).toEqual([
    { predicate: "analysis-policy", value: "normal", note: "shipping it" },
    { predicate: "lifecycle", value: "active", note: "shipping it" },
  ]);

  store.clock.now += 60_000;
  const paused = await interest(store, { entityId, state: "not-now", reason: "the benchmark lands first" }, OPERATOR);
  expect(paused.facts).toHaveLength(2);
  const lifecycle = await rows<{ id: string; value: string; supersedes_id: string | null; status: string }>(
    store,
    `SELECT f.id, f.value, f.supersedes_id,
            (SELECT s.status FROM fact_status s WHERE s.fact_id = f.id ORDER BY s.seq DESC LIMIT 1) AS status
     FROM facts f WHERE f.entity_id = ? AND f.predicate = 'lifecycle' ORDER BY f.recorded_at`,
    [entityId],
  );
  expect(lifecycle.map((row) => [row.value, row.status])).toEqual([
    ["active", "superseded"],
    ["dormant", "active"],
  ]);
  expect(lifecycle[1]?.supersedes_id).toBe(lifecycle[0]?.id);

  // `excluded` is a policy statement and leaves the lifecycle alone.
  store.clock.now += 60_000;
  const excluded = await interest(store, { entityId, state: "excluded", reason: "not Babel's business" }, OPERATOR);
  expect(excluded.facts).toHaveLength(1);
  expect(
    (
      await rows<{ value: string }>(
        store,
        `SELECT value FROM facts WHERE entity_id = ? AND predicate = 'lifecycle' ORDER BY recorded_at DESC LIMIT 1`,
        [entityId],
      )
    )[0],
  ).toEqual({ value: "dormant" });

  expect(interest(store, { entityId, state: "curious", reason: "" }, OPERATOR)).rejects.toThrow(
    /interest state "curious"/,
  );
  expect(interest(store, { entityId: "ent_ffffffff", state: "working", reason: "" }, OPERATOR)).rejects.toThrow(
    /no topic/,
  );
});

test("interest follows a merged topic to the one that speaks for it", async () => {
  const store = openStore();
  await migrate(store);
  const source = await seedEntity(store, "ent_000000a0", "babel-old");
  const target = await seedEntity(store, "ent_000000a1", "babel");
  await store.db.run(`UPDATE entities SET canonical_id = ? WHERE id = ?`, [target, source]);
  const stated = await interest(store, { entityId: source, state: "watching", reason: "keep an eye" }, OPERATOR);
  expect(stated.entityId).toBe(target);
});

// ---------------------------------------------------------------------------- filing

// The clock does NOT advance between these three acts: all three rows carry the same instant, so
// "the filing that currently holds" is decided by the order they were written and never by the
// random tail of an identifier.
test("unfile withdraws and file supersedes the withdrawal, within one millisecond", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "fnd_000000b0", "finding", "a finding");
  const entityId = await seedEntity(store, "ent_000000b0", "babel");

  const filed = await file(store, { id: "fnd_000000b0", entity: "babel", rationale: "it is about babel" }, OPERATOR);
  expect(filed).toMatchObject({ entityId, withdrawn: false, supersedes: "" });

  const withdrawn = await unfile(store, { id: "fnd_000000b0", entity: entityId, reason: "wrong topic" }, OPERATOR);
  expect(withdrawn).toMatchObject({ withdrawn: true, supersedes: filed.id });
  expect(unfile(store, { id: "fnd_000000b0", entity: entityId, reason: "again" }, OPERATOR)).rejects.toThrow(
    /is not filed under/,
  );

  const refiled = await file(store, { id: "fnd_000000b0", entity: entityId, rationale: "it is, after all" }, OPERATOR);
  expect(refiled.supersedes).toBe(withdrawn.id);

  const history = await rows<{ withdrawn: number; rationale: string; author_kind: string }>(
    store,
    `SELECT withdrawn, rationale, author_kind FROM filings WHERE record_id = ? ORDER BY rowid`,
    ["fnd_000000b0"],
  );
  expect(history).toEqual([
    { withdrawn: 0, rationale: "it is about babel", author_kind: "operator" },
    { withdrawn: 1, rationale: "wrong topic", author_kind: "operator" },
    { withdrawn: 0, rationale: "it is, after all", author_kind: "operator" },
  ]);
  expect(store.db.run(`DELETE FROM filings WHERE record_id = 'fnd_000000b0'`)).rejects.toThrow(/never deleted/);
});

test("a record about nothing in particular is filed under the reserved word", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "obs_000000c0", "observation", "an observation");
  const answered = await file(
    store,
    { id: "obs_000000c0", entity: NO_TOPIC, rationale: "it is about the harness, not a topic" },
    OPERATOR,
  );
  expect(answered.entityId).toBe("");
  expect(
    await rows<{ entity_id: string; rationale: string }>(store, `SELECT entity_id, rationale FROM filings`),
  ).toEqual([{ entity_id: "", rationale: "it is about the harness, not a topic" }]);
  expect(file(store, { id: "obs_000000c0", entity: "nowhere", rationale: "x" }, OPERATOR)).rejects.toThrow(
    /no topic "nowhere"/,
  );
  expect(file(store, { id: "obs_000000c0", entity: NO_TOPIC, rationale: "  " }, OPERATOR)).rejects.toThrow(
    /says why/,
  );
});

// ---------------------------------------------------------------------------- words

test("a question comment carries question=1 and a plain one does not", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_000000d0");

  const asked = await comment(
    store,
    { id: "pro_000000d0", text: "what would this cost per week?", kind: "question" },
    OPERATOR,
  );
  expect(asked.question).toBe(true);
  const said = await comment(store, { id: "pro_000000d0", text: "reads well", kind: "comment" }, OPERATOR);
  expect(said.question).toBe(false);

  expect(
    await rows<{ id: string; question: number; reason: string; actor_id: string; stance: string | null }>(
      store,
      `SELECT id, question, reason, actor_id, stance FROM feedback WHERE record_id = ? ORDER BY recorded_at, id`,
      ["pro_000000d0"],
    ),
  ).toEqual(
    expect.arrayContaining([
      { id: asked.id, question: 1, reason: "what would this cost per week?", actor_id: OPERATOR, stance: null },
      { id: said.id, question: 0, reason: "reads well", actor_id: OPERATOR, stance: null },
    ]),
  );
  expect(comment(store, { id: "pro_000000d0", text: "   ", kind: "comment" }, OPERATOR)).rejects.toThrow(
    /has none/,
  );
});

test("an answer records the words and moves the question's state", async () => {
  const store = openStore();
  await migrate(store);
  await store.db.run(
    `INSERT INTO questions(id, kind, class, text, why, dedupe_key, raised_by_kind, raised_by_id, payload, created_at)
     VALUES('que_000000e0', 'resolve-entity', 'blocking', 'which repository is this?', 'two remotes match',
            NULL, 'run', 'run_1', '{}', ?)`,
    [stamp(store.now())],
  );

  const answered = await answer(store, { id: "que_000000e0", outcome: "answered", text: "the babel one" }, OPERATOR);
  expect(answered.state).toBe("answered-uninterpreted");
  expect(
    await rows<{ outcome: string; text: string; actor_id: string }>(
      store,
      `SELECT outcome, text, actor_id FROM answers WHERE question_id = ?`,
      ["que_000000e0"],
    ),
  ).toEqual([{ outcome: "answered", text: "the babel one", actor_id: OPERATOR }]);
  expect(
    await rows<{ seq: number; state: string }>(
      store,
      `SELECT seq, state FROM question_events WHERE question_id = ? ORDER BY seq`,
      ["que_000000e0"],
    ),
  ).toEqual([{ seq: 1, state: "answered-uninterpreted" }]);

  // `answered-uninterpreted` has no edge to `declined`: the state machine refuses it.
  expect(answer(store, { id: "que_000000e0", outcome: "declined", text: "" }, OPERATOR)).rejects.toThrow(
    /cannot become declined/,
  );
  expect(answer(store, { id: "que_ffffffff", outcome: "unknown", text: "" }, OPERATOR)).rejects.toThrow(
    /no question/,
  );
});

test("a substantive answer with no words is refused", async () => {
  const store = openStore();
  await migrate(store);
  await store.db.run(
    `INSERT INTO questions(id, kind, class, text, why, dedupe_key, raised_by_kind, raised_by_id, payload, created_at)
     VALUES('que_000000e1', 'set-focus', 'curiosity', 'what next?', 'nothing queued', NULL, 'run', 'run_1', '{}', ?)`,
    [stamp(store.now())],
  );
  expect(answer(store, { id: "que_000000e1", outcome: "answered", text: " " }, OPERATOR)).rejects.toThrow(
    /substantive answer has no text/,
  );
  const declined = await answer(store, { id: "que_000000e1", outcome: "declined", text: "" }, OPERATOR);
  expect(declined.state).toBe("declined");
});

test("steering threads by root and carries its target", async () => {
  const store = openStore();
  await migrate(store);
  const first = await tell(
    store,
    { text: "I am having a hard time enforcing my repository rules", target: { kind: "entity", id: "ent_1" } },
    OPERATOR,
  );
  expect(first).toMatchObject({ rootId: first.id, seq: 1 });

  const reply = await tell(store, { text: "and it is worse on dev-01", replyTo: first.id }, OPERATOR);
  expect(reply).toMatchObject({ rootId: first.id, seq: 2 });

  expect(
    await rows<{ id: string; root_id: string; reply_to_id: string | null; seq: number; target_kind: string | null; target_id: string | null }>(
      store,
      `SELECT id, root_id, reply_to_id, seq, target_kind, target_id FROM steering ORDER BY seq`,
    ),
  ).toEqual([
    { id: first.id, root_id: first.id, reply_to_id: null, seq: 1, target_kind: "entity", target_id: "ent_1" },
    { id: reply.id, root_id: first.id, reply_to_id: first.id, seq: 2, target_kind: null, target_id: null },
  ]);
  expect(tell(store, { text: "nobody there", replyTo: "stg_ffffffff" }, OPERATOR)).rejects.toThrow(
    /no steering stg_ffffffff to reply to/,
  );
});

// ---------------------------------------------------------------------------- the policy

test("a policy below the measured lease floor is refused, and the floor is the measurement", async () => {
  expect(leaseFloor(1)).toBe(300);
  expect(leaseFloor(24)).toBe(480);
  expect(validateNewPolicy(DEFAULT_POLICY)).toBeNull();

  // The policy this deployment actually lost four runs under, on 2026-09-12.
  expect(validateNewPolicy({ ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24 })).toMatch(
    /cannot cover a batch of 24.*needs 480s/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, explorationShare: 0 })).toMatch(/protected allocation/);
  expect(validateNewPolicy({ ...DEFAULT_POLICY, discoveryShare: 0 })).toMatch(/protected allocation/);
  expect(validateNewPolicy({ ...DEFAULT_POLICY, coverageShare: 0.8 })).toMatch(/over-commit one cycle/);
  expect(validateNewPolicy({ ...DEFAULT_POLICY, maxItemReviews: 1 })).toMatch(/below initial reviews/);
  expect(validateNewPolicy({ ...DEFAULT_POLICY, dailyCost: 0.1 })).toMatch(/below the per-cycle cost/);
  // A zero filing or backlog share is a policy, not a fault.
  expect(validateNewPolicy({ ...DEFAULT_POLICY, filingShare: 0, backlogShare: 0 })).toBeNull();

  const store = openStore();
  await migrate(store);
  expect(setPolicy(store, { ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24 }, "faster", OPERATOR)).rejects.toThrow(
    /needs 480s/,
  );
  expect(await rows(store, `SELECT version FROM policies`)).toEqual([]);

  const installed = await setPolicy(store, { ...DEFAULT_POLICY, enabled: true }, "turning it on", OPERATOR);
  expect(installed).toMatchObject({ version: "1", seq: 1 });
  const next = await setPolicy(
    store,
    { ...DEFAULT_POLICY, version: "2026-09-tuned", enabled: true, batchSize: 8, leaseSeconds: 900 },
    "bigger batches",
    OPERATOR,
  );
  expect(next.seq).toBe(2);
  expect(setPolicy(store, { ...DEFAULT_POLICY, enabled: true }, "again", OPERATOR)).rejects.toThrow(
    /already stored/,
  );
  const stored = await rows<{ version: string; payload: string; actor_id: string }>(
    store,
    `SELECT version, payload, actor_id FROM policies ORDER BY seq`,
  );
  expect(stored.map((row) => row.version)).toEqual(["1", "2026-09-tuned"]);
  expect(JSON.parse(String(stored[0]?.payload))).toMatchObject({ enabled: true, leaseSeconds: 900 });
});

// ---------------------------------------------------------------------------- the crossing

test("the importable tables are derived from the migration itself", () => {
  const tables = importableTables();
  expect(Object.keys(tables)).toHaveLength(23);
  expect(tables["dispositions"]).toEqual([
    "id",
    "record_id",
    "seq",
    "disposition",
    "duplicate_of_id",
    "note",
    "context_id",
    "actor_id",
    "recorded_at",
  ]);
  expect(tables["resolution_members"]).toEqual(["resolution_id", "role", "position", "entity_id"]);
  expect(tables["sqlite_master"]).toBeUndefined();
});

test("importing a chunk is idempotent by primary key and keeps its own ledger", async () => {
  const store = openStore();
  await migrate(store);
  const rowsIn = [
    {
      id: "hyp_imported1",
      kind: "hypothesis",
      root_id: "hyp_imported1",
      seq: 0,
      actor_kind: "run",
      actor_id: "run_old",
      title: "a candidate the Go tree held",
      created_at: "2026-03-01T09:00:00.000000000Z",
      payload: "{}",
    },
    {
      id: "hyp_imported2",
      kind: "hypothesis",
      root_id: "hyp_imported2",
      seq: 0,
      actor_kind: "run",
      actor_id: "run_old",
      title: "another",
      created_at: "2026-03-01T09:00:01.000000000Z",
      payload: "{}",
    },
  ];
  const first = await importLedger(store, { source: "durable.db", table: "records", rows: rowsIn });
  expect(first).toEqual({ source: "durable.db", table: "records", inserted: 2, skipped: 0 });

  store.clock.now += 1000;
  const again = await importLedger(store, { source: "durable.db", table: "records", rows: rowsIn });
  expect(again).toEqual({ source: "durable.db", table: "records", inserted: 0, skipped: 2 });

  expect(
    await rows<{ title: string; created_at: string }>(store, `SELECT title, created_at FROM records ORDER BY id`),
  ).toEqual([
    { title: "a candidate the Go tree held", created_at: "2026-03-01T09:00:00.000000000Z" },
    { title: "another", created_at: "2026-03-01T09:00:01.000000000Z" },
  ]);
  expect(
    await rows<{ source: string; table_name: string; rows: number }>(
      store,
      `SELECT source, table_name, rows FROM imports ORDER BY imported_at, id`,
    ),
  ).toEqual([
    { source: "durable.db", table_name: "records", rows: 2 },
    { source: "durable.db", table_name: "records", rows: 0 },
  ]);

  expect(importLedger(store, { source: "x", table: "records; DROP TABLE records", rows: rowsIn })).rejects.toThrow(
    /holds no table named/,
  );
  expect(
    importLedger(store, { source: "x", table: "records", rows: [{ id: "hyp_x", nonsense: "1" }] }),
  ).rejects.toThrow(/has no column "nonsense"/);
});

// ---------------------------------------------------------------------------- the shared shapes

test("an instant is nine fractional digits so text order is time order", () => {
  expect(stamp(Date.UTC(2026, 8, 12, 17, 43, 5, 123))).toBe("2026-09-12T17:43:05.123000000Z");
  expect(stamp(1) < stamp(2)).toBe(true);
  expect(newId("ent")).toMatch(/^ent_[0-9a-f]{16}$/);
});

test("every refusal is an ActRefused, so a door can tell a mistake from a bug", async () => {
  const store = openStore();
  await migrate(store);
  const caught = await rule(store, { id: "pro_missing", ruling: "accept", note: "" }, OPERATOR).catch(
    (error: unknown) => error,
  );
  expect(caught).toBeInstanceOf(ActRefused);
  expect(touched).toBeGreaterThan(0);
});
