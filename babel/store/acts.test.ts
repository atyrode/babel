import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlRow } from "@manifold/plugin";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID, isRecordId, SessionRowSchema } from "../contract.ts";
import {
  parseReviewResult,
  ResultRefusal,
  type RefusalCode,
  type Role,
} from "../machine/results.ts";
import {
  ActRefused,
  DEFAULT_POLICY,
  NO_TOPIC,
  applyPlan,
  answer,
  clearBudget,
  setBudget,
  comment,
  decide,
  declinePlan,
  file,
  importLedger,
  importableTables,
  interest,
  leaseFloor,
  machineColumns,
  newId,
  recordColumns,
  refuseRow,
  validateNewPolicy,
  rehostSessions,
  rule,
  setPolicy,
  stamp,
  standingOf,
  suggestionsOf,
  tell,
  unfile,
  type ActsStore,
} from "./acts.ts";
import { nameableRecordSql, SCHEMA_V1 } from "./schema.ts";
import { upsertSessionRows } from "./sessions.ts";

/*
  These run against a REAL plugin database — the engine's own file, opened by
  `openPluginDatabase`, migrated with `SCHEMA_V1` — because every guarantee under test is the
  store's rather than this module's: the triggers that refuse an edit, the uniqueness that makes
  a double-click impossible, the foreign keys that refuse a filing under nothing. A fake handle
  would pass while the real one refused.
*/

const cleanup: string[] = [];

/** The manifest's `limits.concurrentJobs` for explore and evaluate, as `server.ts` reads it and
 *  hands it to the acts that write a bound. */
const CONCURRENT_JOBS = 16;

function openStore(
  at = Date.UTC(2026, 8, 12, 12, 0, 0),
): ActsStore & { db: PluginDatabase; clock: { now: number } } {
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
  args: {
    id: string;
    kind: "topic" | "backlog";
    subjectId: string;
    operation: string;
    payload: unknown;
    by?: string;
  },
): Promise<string> {
  await store.db.run(
    `INSERT INTO plans(id, kind, subject_kind, subject_id, operation, dedupe_key, payload,
       proposed_by_kind, proposed_by_id, state, created_at)
     VALUES(?, ?, 'proposal', ?, ?, NULL, ?, 'run', ?, 'open', ?)`,
    [
      args.id,
      args.kind,
      args.subjectId,
      args.operation,
      JSON.stringify(args.payload),
      args.by ?? "run_1",
      stamp(store.now()),
    ],
  );
  return args.id;
}

async function rows<Row extends SqlRow>(
  store: ActsStore,
  sql: string,
  params: readonly string[] = [],
): Promise<readonly Row[]> {
  return await store.db.query<Row>(sql, params);
}

const OPERATOR = "alex";

// ---------------------------------------------------------------------------- rulings

test("a ruling appends, and the row it wrote can never be edited or deleted", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000001");

  const first = await rule(
    store,
    { id: "pro_00000001", ruling: "defer", note: "next week" },
    OPERATOR,
  );
  expect(first).toMatchObject({ standing: "deferred", seq: 1, plan: null });

  const second = await rule(store, { id: "pro_00000001", ruling: "accept", note: "" }, OPERATOR);
  expect(second).toMatchObject({ standing: "accepted", seq: 2 });

  const history = await rows<{ seq: bigint; disposition: string; note: string }>(
    store,
    `SELECT seq, disposition, note FROM dispositions WHERE record_id = ? ORDER BY seq`,
    ["pro_00000001"],
  );
  expect(history).toEqual([
    { seq: 1n, disposition: "defer", note: "next week" },
    { seq: 2n, disposition: "accept", note: "" },
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

  expect(
    rule(store, { id: "pro_00000002", ruling: "reopen", note: "new evidence" }, OPERATOR),
  ).rejects.toThrow(/nothing has been decided here to reopen/);
  await rule(store, { id: "pro_00000002", ruling: "reject", note: "no" }, OPERATOR);
  expect(
    rule(store, { id: "pro_00000002", ruling: "reject", note: "still no" }, OPERATOR),
  ).rejects.toThrow(/already rejected/);
  expect(rule(store, { id: "pro_00000002", ruling: "reopen", note: "" }, OPERATOR)).rejects.toThrow(
    /states no reason/,
  );
  const reopened = await rule(
    store,
    { id: "pro_00000002", ruling: "reopen", note: "new evidence" },
    OPERATOR,
  );
  expect(reopened.standing).toBe("new");

  await rule(
    store,
    { id: "pro_00000003", ruling: "duplicate", note: "", duplicateOf: "pro_00000002" },
    OPERATOR,
  );
  expect(rule(store, { id: "pro_00000003", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /decided at the record it duplicates/,
  );
  expect(
    rule(store, { id: "pro_00000002", ruling: "duplicate", note: "" }, OPERATOR),
  ).rejects.toThrow(/names no original/);
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
  const refined = await rule(
    store,
    { id: "pro_00000004", ruling: "refine", note: "narrow it" },
    OPERATOR,
  );
  expect(refined.standing).toBe("refine-requested");
  expect(rule(store, { id: "pro_00000004", ruling: "accept", note: "" }, OPERATOR)).rejects.toThrow(
    /decided at the descendant/,
  );
});

// ---------------------------------------------------------------------------- next actions

/** One proposed next action, as a run's settled output writes it (#340). */
async function seedNextAction(
  store: ActsStore,
  args: { id: string; recordId: string; kind: string; by?: string },
): Promise<string> {
  await store.db.run(
    `INSERT INTO next_actions(id, record_id, kind, proposed_by_kind, proposed_by_id, summary,
       created_at, payload)
     VALUES(?, ?, ?, 'run', ?, 'draft the issue', ?, '{"rationale":"one bounded change"}')`,
    [args.id, args.recordId, args.kind, args.by ?? "run_1", stamp(store.now())],
  );
  return args.id;
}

test("answering a proposed action appends, reconsidering appends again, and neither row is rewritable", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "fnd_00000070", "finding", "a finding");
  await seedNextAction(store, {
    id: "nxt_00000070",
    recordId: "fnd_00000070",
    kind: "draft-issue",
  });

  const declined = await decide(
    store,
    { nextActionId: "nxt_00000070", decision: "declined", note: "not this quarter" },
    OPERATOR,
  );
  expect(declined).toMatchObject({
    id: "nxt_00000070",
    recordId: "fnd_00000070",
    standing: "declined",
    seq: 1,
  });
  // The same answer twice says nothing new and would put two indistinguishable rows in the
  // history a later reader has to interpret.
  expect(
    decide(store, { nextActionId: "nxt_00000070", decision: "declined", note: "" }, OPERATOR),
  ).rejects.toThrow(/already declined/);

  const accepted = await decide(
    store,
    { nextActionId: "nxt_00000070", decision: "accepted", note: "the repository moved" },
    OPERATOR,
  );
  expect(accepted.seq).toBe(2);
  expect(
    await rows<{ seq: bigint; decision: string; note: string; operator_id: string }>(
      store,
      `SELECT seq, decision, note, operator_id FROM next_action_rulings
        WHERE next_action_id = ? ORDER BY seq`,
      ["nxt_00000070"],
    ),
  ).toEqual([
    { seq: 1n, decision: "declined", note: "not this quarter", operator_id: OPERATOR },
    { seq: 2n, decision: "accepted", note: "the repository moved", operator_id: OPERATOR },
  ]);

  expect(
    store.db.run(`UPDATE next_action_rulings SET decision = 'declined' WHERE seq = 2`),
  ).rejects.toThrow(/never edited/);
  expect(store.db.run(`DELETE FROM next_action_rulings WHERE seq = 1`)).rejects.toThrow(
    /never deleted/,
  );
  // A proposal is a claim about what to do and is corrected by a new proposal, never in place.
  expect(store.db.run(`UPDATE next_actions SET kind = 'store-memory'`)).rejects.toThrow(
    /never edited/,
  );

  // AND THE RECORD IS STILL UNRULED. Accepting "draft an issue about this finding" is not
  // accepting the finding, which is the distinction the two tables exist for.
  expect(await rows(store, `SELECT seq FROM dispositions`)).toEqual([]);
});

test("a decision needs a proposal that exists and an operator who is somebody", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "fnd_00000071", "finding", "a finding");
  await seedNextAction(store, {
    id: "nxt_00000071",
    recordId: "fnd_00000071",
    kind: "develop-further",
  });

  expect(
    decide(store, { nextActionId: "nxt_00000072", decision: "accepted", note: "" }, OPERATOR),
  ).rejects.toThrow(/no proposed action nxt_00000072/);
  expect(
    decide(store, { nextActionId: "nxt_00000071", decision: "accepted", note: "" }, ""),
  ).rejects.toThrow(/has no operator/);
  expect(await rows(store, `SELECT seq FROM next_action_rulings`)).toEqual([]);
});

test("a run cannot write an answer to its own proposal, and the store refuses to spell one", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "fnd_00000073", "finding", "a finding");
  await seedNextAction(store, {
    id: "nxt_00000073",
    recordId: "fnd_00000073",
    kind: "draft-issue",
  });

  // THE LEDGER HAS NO ACTOR KIND, so there is no column a run could be named in as the answerer
  // — the only writer is `decide`, which takes the operator from the dispatch's principal. The
  // shape is the guarantee: a caller reaching past the door and writing raw SQL still cannot
  // record WHO ruled as anything but a person, and the crossing's owner-only import is the one
  // other path that reaches this table at all.
  expect(
    (
      await rows<{ name: string }>(
        store,
        `SELECT name FROM pragma_table_info('next_action_rulings')`,
      )
    )
      .map((row) => row.name)
      .filter((name) => name.includes("kind")),
  ).toEqual([]);

  // And the vocabulary is closed at the table: a third answer that would half-authorize an
  // action is refused by the CHECK rather than stored as a word nothing renders.
  expect(
    store.db.run(
      `INSERT INTO next_action_rulings(id, next_action_id, seq, decision, operator_id, note,
         recorded_at)
       VALUES('nxr_x', 'nxt_00000073', 1, 'maybe', 'run_1', '', '2026-09-12T12:00:00.000000000Z')`,
    ),
  ).rejects.toThrow(/CHECK/);

  // A proposal on a record nobody holds is refused too, so a run cannot attach one to nothing.
  expect(
    seedNextAction(store, {
      id: "nxt_00000074",
      recordId: "fnd_99999999",
      kind: "draft-issue",
    }),
  ).rejects.toThrow(/FOREIGN KEY/);
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
  expect(ruled.plan).toMatchObject({
    kind: "topic",
    operation: "create",
    applied: true,
    declined: false,
  });
  const entityId = ruled.plan?.entityId ?? "";
  expect(entityId).toMatch(/^ent_[0-9a-f]{16}$/);

  const entity = await rows<{
    name: string;
    kind: string;
    canonical_id: string;
    created_by: string;
  }>(store, `SELECT name, kind, canonical_id, created_by FROM entities WHERE id = ?`, [entityId]);
  expect(entity).toEqual([
    { name: "babel", kind: "repository", canonical_id: entityId, created_by: OPERATOR },
  ]);

  const aliases = await rows<{ kind: string; value: string; value_key: string }>(
    store,
    `SELECT kind, value, value_key FROM aliases WHERE entity_id = ? ORDER BY kind`,
    [entityId],
  );
  expect(aliases).toEqual([
    {
      kind: "identifier",
      value: "git@github.com:atyrode/babel.git",
      value_key: "git@github.com:atyrode/babel.git",
    },
    { kind: "name", value: "babel", value_key: "babel" },
    { kind: "path", value: "/home/alex/babel", value_key: "/home/alex/babel" },
  ]);

  const facts = await rows<{
    predicate: string;
    value: string;
    authority_kind: string;
    authority_id: string;
  }>(
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
  const filings = await rows<{
    record_id: string;
    entity_id: string;
    author_kind: string;
    author_id: string;
    rationale: string;
  }>(
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
  expect(JSON.parse(String(plan[0]?.result))).toMatchObject({
    entityId,
    filed: [expect.any(String)],
  });
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

  expect(
    rule(store, { id: "pro_00000020", ruling: "reject", note: "   " }, OPERATOR),
  ).rejects.toThrow(/keeps the reason verbatim/);
  // Refused BEFORE the ruling: the record is still undecided and the plan still open.
  expect(
    await rows(store, `SELECT seq FROM dispositions WHERE record_id = ?`, ["pro_00000020"]),
  ).toEqual([]);
  expect(
    (
      await rows<{ state: string }>(store, `SELECT state FROM plans WHERE id = ?`, ["pln_00000020"])
    )[0]?.state,
  ).toBe("open");

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
  expect(
    (
      await rows<{ state: string }>(store, `SELECT state FROM plans WHERE id = ?`, ["pln_00000030"])
    )[0]?.state,
  ).toBe("open");
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
    (
      await rows<{ canonical_id: string }>(
        store,
        `SELECT canonical_id FROM entities WHERE id = ?`,
        [source],
      )
    )[0],
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
    (
      await rows<{ name: string; kind: string }>(
        store,
        `SELECT name, kind FROM entities WHERE id = ?`,
        [part],
      )
    )[0],
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
    await rows<{ record_id: string; entity_id: string }>(
      store,
      `SELECT record_id, entity_id FROM filings`,
    ),
  ).toEqual([{ record_id: "fnd_00000055", entity_id: part }]);

  await seedPlan(store, {
    id: "pln_00000056",
    kind: "topic",
    subjectId: "pro_00000056",
    operation: "retire",
    payload: { reasoning: "gone", targets: [parent] },
  });
  const declined = await declinePlan(store, "pro_00000056", OPERATOR, "the topic is still in use");
  expect(declined).toMatchObject({
    kind: "topic",
    operation: "retire",
    declined: true,
    applied: false,
  });
  expect(
    (
      await rows<{ state: string; ruling_reason: string }>(
        store,
        `SELECT state, ruling_reason FROM plans WHERE id = ?`,
        ["pln_00000056"],
      )
    )[0],
  ).toEqual({ state: "declined", ruling_reason: "the topic is still in use" });
  expect(declinePlan(store, "pro_00000056", OPERATOR, "  ")).rejects.toThrow(
    /keeps the operator's reason/,
  );
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
    await rows<{ seq: bigint; status: string; actor_kind: string; actor_id: string }>(
      store,
      `SELECT seq, status, actor_kind, actor_id FROM status_events WHERE record_id = ? ORDER BY seq`,
      ["hyp_00000060"],
    ),
  ).toEqual([
    { seq: 1n, status: "deferred", actor_kind: "run", actor_id: "run_1" },
    { seq: 2n, status: "superseded", actor_kind: "operator", actor_id: OPERATOR },
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
    JSON.parse(
      String(
        (
          await rows<{ result: string }>(store, `SELECT result FROM plans WHERE id = ?`, [
            "pln_00000065",
          ])
        )[0]?.result,
      ),
    ),
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
  ).toEqual([
    { predicate: "ownership", value: "alex", confidence: "high", authority_id: OPERATOR },
  ]);
  expect(
    (
      await rows<{ status: string }>(
        store,
        `SELECT status FROM status_events WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
        ["hyp_00000080"],
      )
    )[0],
  ).toEqual({ status: "promoted" });
  // One plan, one ruling: a second application finds nothing open.
  expect(applyPlan(store, "pro_00000080", OPERATOR, "again")).rejects.toThrow(
    /carries no open plan/,
  );
  expect(declinePlan(store, "pro_00000080", OPERATOR, "no")).rejects.toThrow(
    /carries no open plan/,
  );
});

// ---------------------------------------------------------------------------- refinement

/*
  A REFINEMENT'S APPLICATION (§4.7, #341). A review proposes the exact revision, the exact JSON
  Pointer and the replacement; the operator's acceptance of that proposal is what writes the
  reworded revision, and a record is never edited, so it writes a supersession.
*/

/** One revision with a payload worth refining, and the proposal that would reword it. */
async function seedRefinement(
  store: ActsStore,
  args: {
    readonly proposalId: string;
    readonly targetId: string;
    readonly targetPath: string;
    readonly replacement: string;
  },
): Promise<void> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES(?, 'finding', ?, NULL, 0, NULL, 'run_7', NULL, NULL, 'run', 'run_7',
       'the drain stalls', ?, ?)`,
    [
      args.targetId,
      args.targetId,
      stamp(store.now()),
      JSON.stringify({
        title: "the drain stalls",
        pattern: "every drain stalls on the third batch",
        scope: ["dev-01", "dev-02"],
      }),
    ],
  );
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES(?, 'proposal', ?, NULL, 0, NULL, 'run_8', NULL, NULL, 'run', 'run_8',
       'Refine pattern', ?, ?)`,
    [
      args.proposalId,
      args.proposalId,
      stamp(store.now()),
      JSON.stringify({
        title: "Refine pattern",
        problem: "the pattern overstates it",
        outcome: args.replacement,
        impact: "moderate",
        classification: "private",
        refinement: {
          targetRecordId: args.targetId,
          targetRevisionId: args.targetId,
          depth: 1,
          targetPath: args.targetPath,
          reason: "the pattern overstates it",
          replacement: args.replacement,
          sourceRole: "challenge",
        },
      }),
    ],
  );
}

test("accepting a refinement writes the superseding revision, with its lineage", async () => {
  const store = openStore();
  await migrate(store);
  await seedRefinement(store, {
    proposalId: "pro_00000100",
    targetId: "fnd_00000100",
    targetPath: "/payload/pattern",
    replacement: "two of the three drains stalled on the third batch",
  });
  // The record's own relations: a cited session and a consolidated observation. A rewording is
  // the same claim, so the revision that replaces it has to keep them.
  await seedRecord(store, "obs_00000100", "observation", "the third batch stalled");
  await store.db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, position, note, actor_kind,
       actor_id, created_at)
     VALUES(?, 'consolidates', 'finding', 'fnd_00000100', 'observation', 'obs_00000100', 0, NULL,
       'run', 'run_7', ?)`,
    [newId("edg"), stamp(store.now())],
  );

  const ruled = await rule(store, { id: "pro_00000100", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.refinement).toMatchObject({
    targetRecordId: "fnd_00000100",
    targetPath: "/payload/pattern",
    applied: true,
  });
  const revisionId = ruled.refinement?.revisionId ?? "";

  const chain = await rows<{
    id: string;
    root_id: string;
    seq: bigint;
    supersedes_id: string | null;
    actor_kind: string;
    actor_id: string;
    run_id: string;
    title: string;
    payload: string;
  }>(
    store,
    `SELECT id, root_id, seq, supersedes_id, actor_kind, actor_id, run_id, title, payload
       FROM records WHERE root_id = 'fnd_00000100' ORDER BY seq`,
  );
  expect(chain).toHaveLength(2);
  expect(chain[1]).toMatchObject({
    id: revisionId,
    root_id: "fnd_00000100",
    seq: 1n,
    supersedes_id: "fnd_00000100",
    // The operator is the authority the acceptance rests on; the run that proposed it is not.
    actor_kind: "operator",
    actor_id: OPERATOR,
    // The claim's provenance is the original's: the same run under the same method said it.
    run_id: "run_7",
    title: "the drain stalls",
  });
  // Only the wording under the pointer moved. The rest of the payload is the revision it
  // superseded, and the wording it replaced is still readable on that row.
  expect(JSON.parse(String(chain[1]?.payload))).toEqual({
    title: "the drain stalls",
    pattern: "two of the three drains stalled on the third batch",
    scope: ["dev-01", "dev-02"],
  });
  expect(JSON.parse(String(chain[0]?.payload))).toMatchObject({
    pattern: "every drain stalls on the third batch",
  });

  const edges = await rows<{ kind: string; from_id: string; to_id: string; note: string | null }>(
    store,
    `SELECT kind, from_id, to_id, note FROM edges ORDER BY kind`,
  );
  expect(edges).toEqual([
    // The support, carried onto the revision so the reworded finding still rests on it.
    { kind: "consolidates", from_id: "fnd_00000100", to_id: "obs_00000100", note: null },
    { kind: "consolidates", from_id: revisionId, to_id: "obs_00000100", note: null },
    {
      kind: "supersedes",
      from_id: revisionId,
      to_id: "fnd_00000100",
      note: "the pattern overstates it (pro_00000100, /payload/pattern)",
    },
  ]);
  // NOTHING MARKS THE SUPERSEDED REVISION. `status_events` is read per root, so a `superseded`
  // row here would gap out every review of the wording the refinement just installed.
  expect(await rows(store, `SELECT id FROM status_events`)).toEqual([]);
});

test("a refinement against a superseded revision is refused, and the acceptance still stands", async () => {
  const store = openStore();
  await migrate(store);
  await seedRefinement(store, {
    proposalId: "pro_00000110",
    targetId: "fnd_00000110",
    targetPath: "/payload/pattern",
    replacement: "a later wording",
  });
  // Something already replaced the wording the reviewer read.
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES('fnd_00000111', 'finding', 'fnd_00000110', 'fnd_00000110', 1, NULL, 'run_7', NULL,
       NULL, 'operator', 'alex', 'the drain stalls', ?, '{"pattern":"corrected already"}')`,
    [stamp(store.now())],
  );

  const ruled = await rule(store, { id: "pro_00000110", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.refinement).toEqual({
    targetRecordId: "fnd_00000110",
    targetPath: "/payload/pattern",
    revisionId: "",
    applied: false,
    error:
      "revision fnd_00000110 was superseded by fnd_00000111 after this refinement was written, " +
      "so it no longer names the record's current wording; refine the newest revision instead",
  });
  // §4.7 does not un-append a ruling: his answer to the proposal was still his answer.
  expect(ruled.standing).toBe("accepted");
  expect(await rows(store, `SELECT id FROM records WHERE root_id = 'fnd_00000110'`)).toHaveLength(
    2,
  );
});

test("a refinement that would change the record's identity or its shape is refused", async () => {
  const store = openStore();
  await migrate(store);
  await seedRefinement(store, {
    proposalId: "pro_00000120",
    targetId: "fnd_00000120",
    targetPath: "/id",
    replacement: "fnd_ffffffff",
  });
  const identity = await rule(store, { id: "pro_00000120", ruling: "accept", note: "" }, OPERATOR);
  expect(identity.refinement?.error).toMatch(/the record's identity rather than its wording/);

  await seedRefinement(store, {
    proposalId: "pro_00000121",
    targetId: "fnd_00000121",
    targetPath: "/payload/scope",
    replacement: "dev-01, dev-02",
  });
  const shape = await rule(store, { id: "pro_00000121", ruling: "accept", note: "" }, OPERATOR);
  expect(shape.refinement?.error).toMatch(/holds object, and a refinement replaces text/);

  expect(await rows(store, `SELECT id FROM records WHERE seq = 1`)).toEqual([]);
});

test("a refinement names a title as readily as a payload field", async () => {
  const store = openStore();
  await migrate(store);
  await seedRefinement(store, {
    proposalId: "pro_00000130",
    targetId: "fnd_00000130",
    targetPath: "/title",
    replacement: "the drain stalls on the third batch",
  });

  const ruled = await rule(store, { id: "pro_00000130", ruling: "accept", note: "" }, OPERATOR);
  expect(ruled.refinement?.applied).toBe(true);
  expect(await rows<{ title: string }>(store, `SELECT title FROM records WHERE seq = 1`)).toEqual([
    { title: "the drain stalls on the third batch" },
  ]);
});

test("a ruling other than accept applies no refinement", async () => {
  const store = openStore();
  await migrate(store);
  await seedRefinement(store, {
    proposalId: "pro_00000140",
    targetId: "fnd_00000140",
    targetPath: "/payload/pattern",
    replacement: "a wording nobody asked for",
  });

  const deferred = await rule(
    store,
    { id: "pro_00000140", ruling: "defer", note: "later" },
    OPERATOR,
  );
  expect(deferred.refinement).toBeNull();
  expect(await rows(store, `SELECT id FROM records WHERE seq = 1`)).toEqual([]);
});

// ---------------------------------------------------------------------------- interest

test("interest round-trips through the facts and supersedes the stance it replaces", async () => {
  const store = openStore();
  await migrate(store);
  const entityId = await seedEntity(store, "ent_00000090", "babel");

  const working = await interest(
    store,
    { entityId, state: "working", reason: "shipping it" },
    OPERATOR,
  );
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
  const paused = await interest(
    store,
    { entityId, state: "not-now", reason: "the benchmark lands first" },
    OPERATOR,
  );
  expect(paused.facts).toHaveLength(2);
  const lifecycle = await rows<{
    id: string;
    value: string;
    supersedes_id: string | null;
    status: string;
  }>(
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
  const excluded = await interest(
    store,
    { entityId, state: "excluded", reason: "not Babel's business" },
    OPERATOR,
  );
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
  expect(
    interest(store, { entityId: "ent_ffffffff", state: "working", reason: "" }, OPERATOR),
  ).rejects.toThrow(/no topic/);
});

test("interest follows a merged topic to the one that speaks for it", async () => {
  const store = openStore();
  await migrate(store);
  const source = await seedEntity(store, "ent_000000a0", "babel-old");
  const target = await seedEntity(store, "ent_000000a1", "babel");
  await store.db.run(`UPDATE entities SET canonical_id = ? WHERE id = ?`, [target, source]);
  const stated = await interest(
    store,
    { entityId: source, state: "watching", reason: "keep an eye" },
    OPERATOR,
  );
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

  const filed = await file(
    store,
    { id: "fnd_000000b0", entity: "babel", rationale: "it is about babel" },
    OPERATOR,
  );
  expect(filed).toMatchObject({ entityId, withdrawn: false, supersedes: "" });

  const withdrawn = await unfile(
    store,
    { id: "fnd_000000b0", entity: entityId, reason: "wrong topic" },
    OPERATOR,
  );
  expect(withdrawn).toMatchObject({ withdrawn: true, supersedes: filed.id });
  expect(
    unfile(store, { id: "fnd_000000b0", entity: entityId, reason: "again" }, OPERATOR),
  ).rejects.toThrow(/is not filed under/);

  const refiled = await file(
    store,
    { id: "fnd_000000b0", entity: entityId, rationale: "it is, after all" },
    OPERATOR,
  );
  expect(refiled.supersedes).toBe(withdrawn.id);

  const history = await rows<{ withdrawn: bigint; rationale: string; author_kind: string }>(
    store,
    `SELECT withdrawn, rationale, author_kind FROM filings WHERE record_id = ? ORDER BY rowid`,
    ["fnd_000000b0"],
  );
  expect(history).toEqual([
    { withdrawn: 0n, rationale: "it is about babel", author_kind: "operator" },
    { withdrawn: 1n, rationale: "wrong topic", author_kind: "operator" },
    { withdrawn: 0n, rationale: "it is, after all", author_kind: "operator" },
  ]);
  expect(store.db.run(`DELETE FROM filings WHERE record_id = 'fnd_000000b0'`)).rejects.toThrow(
    /never deleted/,
  );
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
    await rows<{ entity_id: string; rationale: string }>(
      store,
      `SELECT entity_id, rationale FROM filings`,
    ),
  ).toEqual([{ entity_id: "", rationale: "it is about the harness, not a topic" }]);
  expect(
    file(store, { id: "obs_000000c0", entity: "nowhere", rationale: "x" }, OPERATOR),
  ).rejects.toThrow(/no topic "nowhere"/);
  expect(
    file(store, { id: "obs_000000c0", entity: NO_TOPIC, rationale: "  " }, OPERATOR),
  ).rejects.toThrow(/says why/);
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
  const said = await comment(
    store,
    { id: "pro_000000d0", text: "reads well", kind: "comment" },
    OPERATOR,
  );
  expect(said.question).toBe(false);

  expect(
    await rows<{
      id: string;
      question: bigint;
      reason: string;
      actor_id: string;
      stance: string | null;
    }>(
      store,
      `SELECT id, question, reason, actor_id, stance FROM feedback WHERE record_id = ? ORDER BY recorded_at, id`,
      ["pro_000000d0"],
    ),
  ).toEqual(
    expect.arrayContaining([
      {
        id: asked.id,
        question: 1n,
        reason: "what would this cost per week?",
        actor_id: OPERATOR,
        stance: null,
      },
      { id: said.id, question: 0n, reason: "reads well", actor_id: OPERATOR, stance: null },
    ]),
  );
  expect(
    comment(store, { id: "pro_000000d0", text: "   ", kind: "comment" }, OPERATOR),
  ).rejects.toThrow(/has none/);
});

test("an answer records the words and moves the question's state", async () => {
  const store = openStore();
  await migrate(store);
  await store.db.run(
    `INSERT INTO questions(id, kind, class, text, why, dedupe_key, raised_by_kind, raised_by_id, payload, created_at)
     VALUES('qst_000000e0', 'resolve-entity', 'blocking', 'which repository is this?', 'two remotes match',
            NULL, 'run', 'run_1', '{}', ?)`,
    [stamp(store.now())],
  );

  const answered = await answer(
    store,
    { id: "qst_000000e0", outcome: "answered", text: "the babel one" },
    OPERATOR,
  );
  expect(answered.state).toBe("answered-uninterpreted");
  expect(
    await rows<{ outcome: string; text: string; actor_id: string }>(
      store,
      `SELECT outcome, text, actor_id FROM answers WHERE question_id = ?`,
      ["qst_000000e0"],
    ),
  ).toEqual([{ outcome: "answered", text: "the babel one", actor_id: OPERATOR }]);
  expect(
    await rows<{ seq: bigint; state: string }>(
      store,
      `SELECT seq, state FROM question_events WHERE question_id = ? ORDER BY seq`,
      ["qst_000000e0"],
    ),
  ).toEqual([{ seq: 1n, state: "answered-uninterpreted" }]);

  // `answered-uninterpreted` has no edge to `declined`: the state machine refuses it.
  expect(
    answer(store, { id: "qst_000000e0", outcome: "declined", text: "" }, OPERATOR),
  ).rejects.toThrow(/cannot become declined/);
  expect(
    answer(store, { id: "qst_ffffffff", outcome: "unknown", text: "" }, OPERATOR),
  ).rejects.toThrow(/no question/);
});

test("a substantive answer with no words is refused", async () => {
  const store = openStore();
  await migrate(store);
  await store.db.run(
    `INSERT INTO questions(id, kind, class, text, why, dedupe_key, raised_by_kind, raised_by_id, payload, created_at)
     VALUES('qst_000000e1', 'set-focus', 'curiosity', 'what next?', 'nothing queued', NULL, 'run', 'run_1', '{}', ?)`,
    [stamp(store.now())],
  );
  expect(
    answer(store, { id: "qst_000000e1", outcome: "answered", text: " " }, OPERATOR),
  ).rejects.toThrow(/substantive answer has no text/);
  const declined = await answer(
    store,
    { id: "qst_000000e1", outcome: "declined", text: "" },
    OPERATOR,
  );
  expect(declined.state).toBe("declined");
});

test("steering threads by root and carries its target", async () => {
  const store = openStore();
  await migrate(store);
  const first = await tell(
    store,
    {
      text: "I am having a hard time enforcing my repository rules",
      target: { kind: "entity", id: "ent_1" },
    },
    OPERATOR,
  );
  expect(first).toMatchObject({ rootId: first.id, seq: 1 });

  const reply = await tell(
    store,
    { text: "and it is worse on dev-01", replyTo: first.id },
    OPERATOR,
  );
  expect(reply).toMatchObject({ rootId: first.id, seq: 2 });

  expect(
    await rows<{
      id: string;
      root_id: string;
      reply_to_id: string | null;
      seq: bigint;
      target_kind: string | null;
      target_id: string | null;
    }>(
      store,
      `SELECT id, root_id, reply_to_id, seq, target_kind, target_id FROM steering ORDER BY seq`,
    ),
  ).toEqual([
    {
      id: first.id,
      root_id: first.id,
      reply_to_id: null,
      seq: 1n,
      target_kind: "entity",
      target_id: "ent_1",
    },
    {
      id: reply.id,
      root_id: first.id,
      reply_to_id: first.id,
      seq: 2n,
      target_kind: null,
      target_id: null,
    },
  ]);
  expect(tell(store, { text: "nobody there", replyTo: "stg_ffffffff" }, OPERATOR)).rejects.toThrow(
    /no steering stg_ffffffff to reply to/,
  );
});

// ---------------------------------------------------------------------------- the policy

test("a policy below the measured lease floor is refused, and the floor is the measurement", async () => {
  expect(leaseFloor(1)).toBe(300);
  expect(leaseFloor(24)).toBe(480);
  expect(validateNewPolicy(DEFAULT_POLICY, CONCURRENT_JOBS)).toBeNull();

  // The policy this deployment actually lost four runs under, on 2026-09-12, with a per-machine
  // bound the manifest's ceiling allows: what the lease must cover is the batch behind it.
  const lost = { ...DEFAULT_POLICY, leaseSeconds: 240, batchSize: 24, concurrentPerMachine: 4 };
  expect(validateNewPolicy(lost, CONCURRENT_JOBS)).toMatch(
    /cannot cover a batch of 24.*needs 480s/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, explorationShare: 0 }, CONCURRENT_JOBS)).toMatch(
    /protected allocation/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, discoveryShare: 0 }, CONCURRENT_JOBS)).toMatch(
    /protected allocation/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, coverageShare: 0.8 }, CONCURRENT_JOBS)).toMatch(
    /over-commit one cycle/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, maxItemReviews: 1 }, CONCURRENT_JOBS)).toMatch(
    /below initial reviews/,
  );
  expect(validateNewPolicy({ ...DEFAULT_POLICY, dailyCost: 0.1 }, CONCURRENT_JOBS)).toMatch(
    /below the per-cycle cost/,
  );
  // A zero filing or backlog share is a policy, not a fault.
  expect(
    validateNewPolicy({ ...DEFAULT_POLICY, filingShare: 0, backlogShare: 0 }, CONCURRENT_JOBS),
  ).toBeNull();

  const store = openStore();
  await migrate(store);
  expect(setPolicy(store, lost, "faster", OPERATOR, CONCURRENT_JOBS)).rejects.toThrow(/needs 480s/);
  // And a policy whose per-machine bound is above what the machine half will run is refused
  // here too, naming the ceiling: the hub would refuse those postings at execute and the
  // deployment would pay a reservation for each one.
  expect(
    setPolicy(
      store,
      { ...DEFAULT_POLICY, enabled: true, concurrentPerMachine: 32, leaseSeconds: 900 },
      "a drain by another name",
      OPERATOR,
      CONCURRENT_JOBS,
    ),
  ).rejects.toThrow(/32 concurrent assignments per machine is above the 16 jobs/);
  expect(await rows(store, `SELECT version FROM policies`)).toEqual([]);

  const installed = await setPolicy(
    store,
    { ...DEFAULT_POLICY, enabled: true },
    "turning it on",
    OPERATOR,
    CONCURRENT_JOBS,
  );
  expect(installed).toMatchObject({ version: "1", seq: 1 });
  const next = await setPolicy(
    store,
    { ...DEFAULT_POLICY, version: "2026-09-tuned", enabled: true, batchSize: 8, leaseSeconds: 900 },
    "bigger batches",
    OPERATOR,
    CONCURRENT_JOBS,
  );
  expect(next.seq).toBe(2);
  expect(
    setPolicy(store, { ...DEFAULT_POLICY, enabled: true }, "again", OPERATOR, CONCURRENT_JOBS),
  ).rejects.toThrow(/already stored/);
  const stored = await rows<{ version: string; payload: string; actor_id: string }>(
    store,
    `SELECT version, payload, actor_id FROM policies ORDER BY seq`,
  );
  expect(stored.map((row) => row.version)).toEqual(["1", "2026-09-tuned"]);
  expect(JSON.parse(String(stored[0]?.payload))).toMatchObject({
    enabled: true,
    leaseSeconds: 900,
  });
});

test("an overlay is a row of its own: setting and clearing one writes no policies row", async () => {
  const store = openStore();
  await migrate(store);
  await setPolicy(
    store,
    { ...DEFAULT_POLICY, enabled: true, batchSize: 4 },
    "turning it on",
    OPERATOR,
    CONCURRENT_JOBS,
  );
  const before = await rows<{ version: string; payload: string; seq: bigint }>(
    store,
    `SELECT version, payload, seq FROM policies ORDER BY seq`,
  );

  const overlaid = await setBudget(
    store,
    {
      expiresAt: new Date(store.now() + 2 * 3_600_000).toISOString(),
      concurrentPerMachine: 8,
      perCycleCost: 2,
      dailyCost: 20,
      reason: "draining victorballu before the 13:00Z reset",
    },
    OPERATOR,
    CONCURRENT_JOBS,
  );
  expect(overlaid.changes).toEqual([
    { field: "perCycleCost", standing: 0.25, overlaid: 2 },
    { field: "dailyCost", standing: 2, overlaid: 20 },
    { field: "concurrentPerMachine", standing: 4, overlaid: 8 },
  ]);
  const cleared = await clearBudget(
    store,
    { id: overlaid.id, reason: "the window reset early" },
    OPERATOR,
  );
  expect(cleared.id).toBe(overlaid.id);

  // THE ACCEPTANCE OF #260: the standing policy is byte-for-byte what it was, so every
  // assignment id derived from its version is where it was too.
  expect(
    await rows<{ version: string; payload: string; seq: bigint }>(
      store,
      `SELECT version, payload, seq FROM policies ORDER BY seq`,
    ),
  ).toEqual(before);
  // And the overlay's own row keeps both instants AND both reasons: why it was set, and why
  // somebody ended it early — a drain that finished and a box that fell over are not one row.
  const held = await rows<{
    id: string;
    concurrent_per_machine: bigint | null;
    cleared_at: string | null;
    reason: string;
    cleared_reason: string | null;
  }>(store, `SELECT id, concurrent_per_machine, cleared_at, reason, cleared_reason FROM budgets`);
  expect(held).toEqual([
    {
      id: overlaid.id,
      concurrent_per_machine: 8n,
      cleared_at: cleared.at,
      reason: "draining victorballu before the 13:00Z reset",
      cleared_reason: "the window reset early",
    },
  ]);

  // Clearing it twice is refused rather than rewriting when it ended.
  expect(clearBudget(store, { id: overlaid.id, reason: "" }, OPERATOR)).rejects.toThrow(
    /was cleared at/,
  );
  expect(clearBudget(store, { id: "bdg_nothing", reason: "" }, OPERATOR)).rejects.toThrow(
    /no budget overlay bdg_nothing/,
  );
});

test("an overlay the standing lease cannot cover is refused, and so is one that moves nothing", async () => {
  const store = openStore();
  await migrate(store);
  await setPolicy(
    store,
    { ...DEFAULT_POLICY, enabled: true, batchSize: 4, leaseSeconds: 300 },
    "on",
    OPERATOR,
    CONCURRENT_JOBS,
  );
  const expiresAt = new Date(store.now() + 3_600_000).toISOString();

  // eval-policy-8's batch, against the machine half this plugin ships: sixteen jobs is what a
  // host runs at once, so two hundred and fifty-six is refused at the door rather than admitted
  // and refused a posting at a time, each one paid for.
  expect(
    setBudget(
      store,
      { expiresAt, concurrentPerMachine: 256, perCycleCost: 4, dailyCost: 8, reason: "drain" },
      OPERATOR,
      CONCURRENT_JOBS,
    ),
  ).rejects.toThrow(/above the 16 jobs a machine runs at once/);
  // And at the ceiling, the lease the deployment actually has is what refuses: a bound of
  // sixteen needs 320s and this policy grants 300.
  expect(
    setBudget(
      store,
      { expiresAt, concurrentPerMachine: 16, perCycleCost: 4, dailyCost: 8, reason: "drain" },
      OPERATOR,
      CONCURRENT_JOBS,
    ),
  ).rejects.toThrow(/needs 320s/);
  expect(
    setBudget(store, { expiresAt, reason: "drain" }, OPERATOR, CONCURRENT_JOBS),
  ).rejects.toThrow(/moves no number/);
  expect(
    setBudget(
      store,
      { expiresAt: "not an instant", concurrentPerMachine: 8, reason: "drain" },
      OPERATOR,
      CONCURRENT_JOBS,
    ),
  ).rejects.toThrow(/is not an instant/);
  expect(
    setBudget(
      store,
      {
        expiresAt: new Date(store.now() - 1000).toISOString(),
        concurrentPerMachine: 8,
        reason: "drain",
      },
      OPERATOR,
      CONCURRENT_JOBS,
    ),
  ).rejects.toThrow(/no time at all/);
  expect(await rows(store, `SELECT id FROM budgets`)).toEqual([]);
});

// ---------------------------------------------------------------------------- the crossing

test("the importable tables are derived from the migration itself", () => {
  const tables = importableTables();
  // `record_terms` is NOT here and cannot be: it is a VIRTUAL table and this derivation reads
  // `CREATE TABLE` only, which is the right answer for it — an FTS5 index has no column list a
  // chunk could be validated against, and it is rebuilt from `records` rather than imported.
  expect(tables["record_terms"]).toBeUndefined();
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
  // `runs` is the table whose body carries a prose comment, and prose carries apostrophes: the
  // note about "where this run's job is" opened a quote nothing closed, and the two columns
  // after it were read as part of the literal. Deriving them wrong is silent until the crossing
  // refuses a run receipt for having no `payload`, so the whole list is pinned here.
  expect(tables["runs"]).toEqual([
    "id",
    "kind",
    "machine_id",
    "job_id",
    "container_id",
    "prepare_job_id",
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
    "unreadable",
    "payload",
  ]);
  expect(tables["sqlite_master"]).toBeUndefined();
});

test("every machine column of the migration is one the crossing guard covers", () => {
  // THE PIN IS THE EXHAUSTIVENESS. The guard's worth is that it covers ALL of them: two of three
  // reads as a statement that the third is fine, which is how `runs.machine_id` went untested
  // and `drains.machine_id` went unguarded (#378, #379). TypeScript cannot carry this — the
  // columns live inside SQL text, so no union it can check is anything but a second hand-written
  // list — and the derivation reads that text, so this is where the schema and the guard are
  // held to each other. A column added to `SCHEMA_V1` under either machine spelling lands here
  // and fails this line; one added under a third spelling fails nothing, which is why the rule
  // is a shape and not a set of table names.
  expect(machineColumns()).toEqual({
    sessions: ["host"],
    runs: ["machine_id"],
    drains: ["machine_id"],
    run_calls: ["transcript_host"],
    // The label a capture was taken under is a NAME and stays out of this map, which is why its
    // column is `sessions.archive_label` and not `archive_host`; what the operator says it means
    // is a machine id, and is guarded like every other (#453).
    archive_labels: ["machine_id"],
    // Transcript maps (#223) are keyed by the source owner that answered for them. The capture's
    // `host` is its archived label, so it is over-guarded by shape: the crossing refuses such a
    // row rather than admitting one, and no imported Go-era store carries a transcript map.
    transcript_map_access: ["machine_id"],
    transcript_map_captures: ["host"],
    transcript_map_contexts: ["machine_id"],
    transcript_map_heads: ["machine_id"],
    transcript_map_scan: ["machine_id"],
    transcript_map_versions: ["machine_id"],
  });
  // A count of jobs per machine is not a machine, and the crossing must still be able to carry
  // an overlay row: `concurrent_per_machine` is in `budgets` and `budgets` is not in the map.
  expect(importableTables()["budgets"]).toContain("concurrent_per_machine");
  expect(machineColumns()["budgets"]).toBeUndefined();
});

test("every record-identifier column of the migration is one the crossing guard covers", () => {
  // The same pin as the machine columns above, for the same reason and against the same failure
  // mode. A column added to `SCHEMA_V1` that holds a record id — by name, or by saying
  // `REFERENCES records(id)` — lands here and fails this line rather than quietly becoming the
  // one place an unopenable row can still get in (#414).
  expect(recordColumns()).toEqual({
    records: ["id", "root_id", "supersedes_id", "parent_id"],
    status_events: ["record_id"],
    dispositions: ["record_id", "duplicate_of_id"],
    next_actions: ["record_id"],
    filings: ["record_id"],
    feedback: ["record_id"],
    steering: ["root_id"],
    assessments: ["record_id", "revision_id"],
    claims: ["record_id"],
    record_vectors: ["record_id"],
  });
  // A `supersedes_id` that references its OWN table is not a record id, and the crossing must
  // still carry those rows: the name family is the columns whose meaning is fixed, and the
  // REFERENCES clause carries the rest. `filings` and `facts` supersede themselves.
  expect(importableTables()["filings"]).toContain("supersedes_id");
  expect(recordColumns()["filings"]).toEqual(["record_id"]);
  expect(recordColumns()["facts"]).toBeUndefined();
  // `edges` is absent because its ends are polymorphic; the row's own kind decides, which the
  // import path checks and the test below proves.
  expect(recordColumns()["edges"]).toBeUndefined();
});
test("the crossing refuses a record id no door could read back, and imports none of the chunk", async () => {
  const store = openStore();
  await migrate(store);
  const row = (id: string) => ({
    id,
    kind: "hypothesis",
    root_id: id,
    seq: 0,
    actor_kind: "run",
    actor_id: "run_old",
    title: "a candidate",
    created_at: "2026-03-01T09:00:00.000000000Z",
    payload: "{}",
  });
  // THE DEFECT: twelve rows like this were accepted, listed by the feed, and refused by the
  // `record` door when opened — and `records_kept` means they can never be removed.
  await expect(
    importLedger(store, {
      source: "seed.db",
      table: "records",
      rows: [row("hyp_00000001"), row("rec_seed_001")],
    }),
  ).rejects.toThrow(/records\.id holds "rec_seed_001"/);
  // Not the bad row only: the good one beside it stayed out too, because an append-only table
  // cannot be rolled back and a half-delivered chunk is the worse answer.
  expect(await rows<{ id: string }>(store, `SELECT id FROM records`)).toEqual([]);

  await expect(
    importLedger(store, {
      source: "seed.db",
      table: "status_events",
      rows: [
        {
          id: "ste_00000001",
          record_id: "rec_seed_001",
          seq: 1,
          status: "open",
          actor_kind: "run",
          actor_id: "run_old",
          recorded_at: "2026-03-01T09:00:00.000000000Z",
        },
      ],
    }),
  ).rejects.toThrow(/status_events\.record_id holds "rec_seed_001"/);

  // An edge's end is checked against the kind beside it: a record end must be a record id, and
  // an entity end is left alone. Guarding `from_id` by name would refuse every entity edge.
  const edge = (fromKind: string, fromId: string) => ({
    id: "edg_00000001",
    kind: "cites",
    from_kind: fromKind,
    from_id: fromId,
    to_kind: "entity",
    to_id: "ent_00000001",
    actor_kind: "run",
    actor_id: "run_old",
    created_at: "2026-03-01T09:00:00.000000000Z",
  });
  await expect(
    importLedger(store, {
      source: "seed.db",
      table: "edges",
      rows: [edge("record", "rec_seed_1")],
    }),
  ).rejects.toThrow(/edges\.from_id holds "rec_seed_1"/);
  expect(
    await importLedger(store, {
      source: "seed.db",
      table: "edges",
      rows: [edge("entity", "ent_00000002")],
    }),
  ).toEqual({ source: "seed.db", table: "edges", inserted: 1, skipped: 0 });
});

test("importing a chunk is idempotent by primary key and keeps its own ledger", async () => {
  const store = openStore();
  await migrate(store);
  // The ids are the shape the Go tree actually minted — `newID` in `internal/frontier/store.go`
  // at v0.4.0 is a family, an underscore and sixteen random bytes in hex — because a fixture
  // that invents an id shape is how an import path came to accept one no door can read (#414).
  const rowsIn = [
    {
      id: "hyp_1a3b5c7d9e0f24683b5d7f91a2c4e608",
      kind: "hypothesis",
      root_id: "hyp_1a3b5c7d9e0f24683b5d7f91a2c4e608",
      seq: 0,
      actor_kind: "run",
      actor_id: "run_old",
      title: "a candidate the Go tree held",
      created_at: "2026-03-01T09:00:00.000000000Z",
      payload: "{}",
    },
    {
      id: "hyp_9f2c4d0b7a1e58630c4d2f9a8b7e6150",
      kind: "hypothesis",
      root_id: "hyp_9f2c4d0b7a1e58630c4d2f9a8b7e6150",
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
    await rows<{ title: string; created_at: string }>(
      store,
      `SELECT title, created_at FROM records ORDER BY id`,
    ),
  ).toEqual([
    { title: "a candidate the Go tree held", created_at: "2026-03-01T09:00:00.000000000Z" },
    { title: "another", created_at: "2026-03-01T09:00:01.000000000Z" },
  ]);
  expect(
    await rows<{ source: string; table_name: string; rows: bigint }>(
      store,
      `SELECT source, table_name, rows FROM imports ORDER BY imported_at, id`,
    ),
  ).toEqual([
    { source: "durable.db", table_name: "records", rows: 2n },
    { source: "durable.db", table_name: "records", rows: 0n },
  ]);

  expect(
    importLedger(store, { source: "x", table: "records; DROP TABLE records", rows: rowsIn }),
  ).rejects.toThrow(/holds no table named/);
  expect(
    importLedger(store, { source: "x", table: "records", rows: [{ id: "hyp_x", nonsense: "1" }] }),
  ).rejects.toThrow(/has no column "nonsense"/);
});

test("a re-host maps the label, moves what it names by host and by label, and hosts every later capture there", async () => {
  const store = openStore();
  await migrate(store);
  // A Go-era row hosted at the name `dev-01`, and captures catalogued under the labels
  // `dev-01` and `workstation-linux` before anybody said what either means.
  await store.db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, seen_at)
     VALUES('omp/imported', 'dev-01', 'omp', 'imported', '2026-09-01T00:00:00Z')`,
  );
  const capture = (id: string, label: string) =>
    SessionRowSchema.parse({
      selector: `omp/${id}`,
      harness: "omp",
      source_id: id,
      kind: "operator",
      archive_label: label,
      archive_path: `/home/operator/.omp/agent/sessions/${id}.jsonl`,
      snapshot_id: "a".repeat(64),
      archived_at: "2026-09-24T21:00:00.000Z",
      size: 10,
      modified_at: "2026-09-24T20:00:00.000Z",
    });
  const seen = "2026-09-24T22:00:00.000Z";
  await upsertSessionRows(
    store,
    [capture("captured", "dev-01"), capture("elsewhere", "workstation-linux")],
    seen,
  );
  const hosts = async () =>
    Object.fromEntries(
      (await rows(store, `SELECT selector, host FROM sessions ORDER BY selector`)).map((row) => [
        row["selector"],
        row["host"],
      ]),
    );

  expect(await rehostSessions(store, { from: "dev-01", to: "m-1" })).toEqual({
    from: "dev-01",
    to: "m-1",
    sessions: 2,
    labelled: 1,
  });
  expect(await hosts()).toEqual({
    "omp/captured": "m-1",
    "omp/elsewhere": "",
    "omp/imported": "m-1",
  });
  // The mapping is what makes it stick: the next capture under the label is hosted there.
  await upsertSessionRows(store, [capture("later", "dev-01")], seen);
  expect((await hosts())["omp/later"]).toBe("m-1");
  // Idempotent, and it says what the label holds either way.
  expect(await rehostSessions(store, { from: "dev-01", to: "m-1" })).toMatchObject({
    sessions: 0,
    labelled: 2,
  });

  // Mapping the label again moves what was captured under it, and nothing it merely hosted.
  expect(await rehostSessions(store, { from: "dev-01", to: "m-2" })).toMatchObject({
    sessions: 2,
    labelled: 2,
  });
  expect(await hosts()).toMatchObject({
    "omp/captured": "m-2",
    "omp/later": "m-2",
    "omp/imported": "m-1",
  });
  expect(await rows(store, `SELECT label, machine_id FROM archive_labels`)).toEqual([
    { label: "dev-01", machine_id: "m-2" },
  ]);

  // A label that is itself a machine id — one an owner may choose for `archive` — means nothing
  // to the hub until it is recorded, so naming it twice is the mapping and not a mistake.
  await upsertSessionRows(store, [capture("archived", "m-3")], seen);
  expect((await hosts())["omp/archived"]).toBe("");
  expect(await rehostSessions(store, { from: "m-3", to: "m-3" })).toMatchObject({
    sessions: 1,
    labelled: 1,
  });
  expect((await hosts())["omp/archived"]).toBe("m-3");
});

// ---------------------------------------------------------------------------- what a job wrote

/*
  The store's acceptance of a review submission is the validator the engine's per-role JSON
  Schema is generated from, and the cases below are the pair that proves it: what the producer
  accepts the store writes, and what the producer refuses the store refuses under the SAME code.
  The Go tree stated that rule three times and the three disagreed, so an evidence check — a
  criterion result with an environment and no outcome — was paid for and refused at submit
  (docs/postmortem-2026-09-13-drain.md, F8).
*/

const LOCATOR = { path: "omp/session-1.jsonl", line: 12, byte_offset: 480, digest: "sha256:abc" };
const EVIDENCE = { locator: LOCATOR, note: "the transcript says the router retries twice" };

/** One submission as the row carries it: the normalized result plus the assignment's own facts. */
function stored(submission: Record<string, unknown>): string {
  const { as_of: asOf, ...rest } = submission;
  return JSON.stringify({
    ...rest,
    ...(asOf === undefined ? {} : { asOf }),
    blinded: false,
    corrects: "",
    policyVersion: "pol_0003",
    contextVersion: "ctx_0007",
    recipe: { id: "evidence-check", version: 2 },
  });
}

const CHECKED = {
  results: [{ criterion_id: "crit_1", satisfied: true, evidence: [EVIDENCE] }],
  environment: "dev-01",
  as_of: "2026-09-12T10:00:00Z",
};

test("the store writes an evidence check: criterion results with an environment and no outcome", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "hyp_00000001", "hypothesis", "the router drops a retry");

  const row = {
    id: "asm_00000001",
    record_id: "hyp_00000001",
    revision_id: "hyp_00000001",
    run_id: "run_1",
    role: "evidence",
    vote: null,
    lane: "coverage",
    claim_id: "clm_0001",
    payload: stored(CHECKED),
    recorded_at: stamp(store.now()),
  };
  expect(refuseRow("assessments", row)).toBeNull();

  await store.db.run(
    `INSERT INTO assessments(id, record_id, revision_id, run_id, role, vote, lane, claim_id, payload, recorded_at)
     VALUES(?,?,?,?,?,?,?,?,?,?)`,
    [
      row.id,
      row.record_id,
      row.revision_id,
      row.run_id,
      row.role,
      row.vote,
      row.lane,
      row.claim_id,
      row.payload,
      row.recorded_at,
    ],
  );
  // A review with no vote is still a review: the criterion results are what it judged, and the
  // scope it judged them in is in the row. `outcome` is not in the payload at all, which is the
  // answer rather than a missing field — and the answer the store accepts.
  const held = await rows<{
    role: string;
    vote: string | null;
    outcome: string | null;
    environment: string;
    criteria: string;
  }>(
    store,
    `SELECT role, vote, json_extract(payload, '$.outcome') AS outcome,
            json_extract(payload, '$.environment') AS environment,
            json_extract(payload, '$.results[0].criterion_id') AS criteria
       FROM assessments WHERE id = ?`,
    ["asm_00000001"],
  );
  expect(held).toEqual([
    { role: "evidence", vote: null, outcome: null, environment: "dev-01", criteria: "crit_1" },
  ]);
});

test("the store refuses what the engine's schema refuses, under the same code", () => {
  const cases: { role: Role; submission: Record<string, unknown>; code: RefusalCode | null }[] = [
    { role: "evidence", submission: CHECKED, code: null },
    { role: "reception", submission: { vote: "support" }, code: null },
    {
      role: "reception",
      submission: { skip: "the evidence is unreachable from here" },
      code: null,
    },
    // F8 itself, the other way round: a setting with no claim about it scopes nothing.
    {
      role: "evidence",
      submission: {
        contributions: [{ kind: "comment", text: "the criteria are not stated" }],
        environment: "dev-01",
      },
      code: "schema",
    },
    // A criterion result with no setting is the claim about every setting at every time.
    {
      role: "evidence",
      submission: { results: [{ criterion_id: "crit_1", satisfied: false }] },
      code: "support",
    },
    // A satisfied criterion nobody can check is the manufactured result §4.12 forbids.
    {
      role: "evidence",
      submission: {
        results: [{ criterion_id: "crit_1", satisfied: true }],
        environment: "dev-01",
        as_of: "2026-09-12T10:00:00Z",
      },
      code: "support",
    },
    { role: "reception", submission: {}, code: "empty" },
    // The role's authority: the schema a reception review is prompted with has no `results`.
    { role: "reception", submission: CHECKED, code: "schema" },
    { role: "reception", submission: { vote: "approve" }, code: "schema" },
  ];

  for (const { role, submission, code } of cases) {
    let produced: RefusalCode | null = null;
    try {
      parseReviewResult(role, submission);
    } catch (error) {
      if (!(error instanceof ResultRefusal)) throw error;
      produced = error.refusal;
    }
    const refused = refuseRow("assessments", { role, payload: stored(submission) });
    expect({ role, submission, code: produced }).toEqual({ role, submission, code });
    expect({ role, submission, code: refused?.code ?? null }).toEqual({ role, submission, code });
  }
});

test("the store refuses a row whose role this build never heard of, and reads a payload object or its JSON", () => {
  const later = refuseRow("assessments", {
    role: "provenance",
    payload: stored({ vote: "support" }),
  });
  expect(later?.code).toBe("schema");
  expect(later?.message).toContain("not a review role");
  expect(refuseRow("assessments", { role: "reception", payload: "{" })?.message).toContain(
    "not JSON",
  );
  expect(refuseRow("assessments", { role: "reception", payload: { vote: "support" } })).toBeNull();
  // Every other table is the store's own shape; only a submission has this contract.
  expect(refuseRow("records", { id: "hyp_1", payload: "{}" })).toBeNull();
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
  const caught = await rule(
    store,
    { id: "pro_missing", ruling: "accept", note: "" },
    OPERATOR,
  ).catch((error: unknown) => error);
  expect(caught).toBeInstanceOf(ActRefused);
  expect(touched).toBeGreaterThan(0);
});

// -------------------------------------------------------------- what a sweep may be offered

/** The suggester a sweep's marks are attributed to; any plugin id serves here. */
const SWEEPER = "atyrode.babel.jev";

test("the gap holds no record a sweep could not name, in the count or in any page of it", async () => {
  const store = openStore();
  await migrate(store);
  // Interleaved on purpose: the unnameable rows sit BETWEEN the two a pass can deliver, so a
  // page filtered after its own LIMIT would answer nothing and leave `after` where it was —
  // a sweep that walks the gap one row at a time would never reach the second record.
  await seedRecord(store, "pro_00000001", "proposal", "worth judging");
  await seedRecord(store, "rec_seed_001", "proposal", "imported before the guard");
  await seedRecord(store, "pro_00ABCDEF", "proposal", "hex, but not as the schema spells it");
  await seedRecord(store, "pro_00000002", "proposal", "also worth judging");

  const counted = await suggestionsOf(store, SWEEPER, {
    pending: 10,
    basis: "",
    kinds: [],
    after: "",
  });
  expect(counted.unjudged).toBe(2);
  expect(counted.pending.map((row) => row.recordId)).toEqual(["pro_00000001", "pro_00000002"]);

  const firstPage = await suggestionsOf(store, SWEEPER, {
    pending: 1,
    basis: "",
    kinds: [],
    after: "",
  });
  expect(firstPage.pending.map((row) => row.recordId)).toEqual(["pro_00000001"]);
  const nextPage = await suggestionsOf(store, SWEEPER, {
    pending: 1,
    basis: "",
    kinds: [],
    after: "pro_00000001",
  });
  // The continuation moved past the two rows nothing can name rather than stalling on them.
  expect(nextPage.pending.map((row) => row.recordId)).toEqual(["pro_00000002"]);
  expect(nextPage.unjudged).toBe(2);
});

test("a ruled record is still offered to a sweep, marked as one no suggestion may land on", async () => {
  const store = openStore();
  await migrate(store);
  await seedRecord(store, "pro_00000001", "proposal", "open, and nobody has ruled");
  await seedRecord(store, "pro_00000002", "proposal", "the operator has accepted this one");
  await rule(store, { id: "pro_00000002", ruling: "accept", note: "" }, OPERATOR);

  const queue = await suggestionsOf(store, SWEEPER, {
    pending: 10,
    basis: "",
    kinds: [],
    after: "",
  });
  // A ruling decides what may be OFFERED on a record, not whether a screener may read it: the
  // imported corpus is mostly ruled, and a gap that skipped those would leave it unscreened.
  expect(queue.unjudged).toBe(2);
  expect(queue.pending.map((row) => [row.recordId, row.suggestible])).toEqual([
    ["pro_00000001", true],
    ["pro_00000002", false],
  ]);
  // Reopening is the operator's own act and returns the record to `new`, which is exactly when
  // `suggest` admits a suggestion again — so the flag follows the newest ruling and not the
  // existence of a ruling.
  await rule(store, { id: "pro_00000002", ruling: "reopen", note: "new evidence" }, OPERATOR);
  const reopened = await suggestionsOf(store, SWEEPER, {
    pending: 10,
    basis: "",
    kinds: [],
    after: "",
  });
  expect(reopened.pending.map((row) => row.suggestible)).toEqual([true, true]);
});

test("the store's own test of a nameable id answers exactly as the contract's does", async () => {
  const store = openStore();
  await migrate(store);
  // Two spellings of one rule drift apart silently, and the count and the page would then
  // disagree with every schema the rows are validated against on the way out.
  const ids = [
    "fnd_00000001",
    `hyp_${"a".repeat(64)}`,
    `hyp_${"a".repeat(65)}`,
    "obs_0000001",
    "obs_00000001",
    "pro_00ABCDEF",
    "rec_seed_001",
    "qst_deadbeef",
    "pro_0000000g",
    "fnd_0000_001",
    "pro_",
  ];
  for (const id of ids) await seedRecord(store, id, "proposal", "a row of some vintage");
  const named = await rows<{ id: string }>(
    store,
    `SELECT id FROM records r WHERE ${nameableRecordSql("r.id")} ORDER BY id`,
  );
  expect(named.map((row) => row.id)).toEqual([...ids].filter(isRecordId).sort());
});
