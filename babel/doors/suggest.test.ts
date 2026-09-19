import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { ACTIONS, BABEL_PLUGIN_ID, EVENTS, type RecordPeel } from "../contract.ts";
import { DEFAULT_POLICY, setPolicy, stamp, type ActsStore } from "../store/acts.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { openStore } from "../store/store.ts";
import { actDoors } from "./acts.ts";
import type { Door } from "./door.ts";
import { suggestDoors } from "./suggest.ts";

/*
  THE ONE DOOR A DEPENDENT PLUGIN MAY WRITE THROUGH (#410), exercised the way the runtime
  exercises it: the arguments go through the action's own input schema first, then the handler
  runs against a REAL plugin database with the real triggers, foreign keys and CHECKs.

  Every refusal here is load-bearing, and each test below fails if its check is removed — that is
  the whole deliverable, so the assertions are written against what a caller observes rather than
  against how the act is spelled.
*/

const cleanup: string[] = [];

/** A plugin the operator has allowed, and the principal its calls arrive under. */
const JEV = "atyrode.babel.jev";
const JEV_PRINCIPAL = "prn_jev";

interface Emission {
  kind: string;
  payload: unknown;
}

interface Harness {
  store: ActsStore;
  doors: readonly Door[];
  ctx: GuestCtx;
  emitted: Emission[];
  /** Every statement the doors issued since the last reset, in order. */
  statements: string[];
}

/** Records the SQL every door issues, so the tables a path can reach are a fact and not a claim. */
function recording(db: PluginDatabase, statements: string[]): PluginDatabase {
  return {
    pluginId: db.pluginId,
    query: async <Row extends SqlRow = SqlRow>(sql: string, params?: readonly SqlParam[]) => {
      statements.push(sql);
      return await db.query<Row>(sql, params);
    },
    run: async (sql: string, params?: readonly SqlParam[]) => {
      statements.push(sql);
      return await db.run(sql, params);
    },
    batch: async (batched: readonly SqlStatement[]) => {
      for (const statement of batched) statements.push(statement.sql);
      return await db.batch(batched);
    },
  };
}

function openHarness(principal = JEV_PRINCIPAL): Harness {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-suggest-"));
  cleanup.push(dataDir);
  const statements: string[] = [];
  const db = recording(openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID }), statements);
  const store: ActsStore = { db, now: () => Date.UTC(2026, 8, 12, 12, 0, 0), touch: () => {} };
  const emitted: Emission[] = [];
  const ctx = {
    pluginId: BABEL_PLUGIN_ID,
    principal: { id: principal, kind: "service", name: principal },
    auth: { principal: { id: principal }, caps: ["containers:write"], containerScope: null },
    emit: (_ref: unknown, kind: string, payload: unknown) => {
      emitted.push({ kind, payload });
    },
  } as unknown as GuestCtx;
  const doors = [
    ...suggestDoors(store),
    ...actDoors(store, 16, (() => ({ describe: async () => ({}) })) as never),
  ];
  return { store, doors, ctx, emitted, statements };
}

afterEach(() => {
  for (const dir of cleanup.splice(0)) rmSync(dir, { recursive: true, force: true });
});

/** Parses the arguments against the door's own schema and runs it, exactly as the kit does. */
async function knock(harness: Harness, name: string, args: unknown): Promise<unknown> {
  const door = harness.doors.find((candidate) => candidate.action.name === name);
  if (door === undefined) throw new Error(`no door named ${name}`);
  const result = await door.handler(harness.ctx, door.action.input.parse(args) as never);
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

/** Installs a policy allowing `suggesters`, through the operator's own door. */
async function allow(
  store: ActsStore,
  suggesters: readonly { principalId: string; pluginId: string }[],
): Promise<void> {
  const listed = suggesters.map((suggester) => ({ ...suggester, note: "allowed by the operator" }));
  await setPolicy(store, { ...DEFAULT_POLICY, suggesters: listed }, "", "alex", 16);
}

/** The record as a reader opens it, with the nullability a missing record would carry. */
async function peeled(harness: Harness): Promise<RecordPeel> {
  const peel = await openStore(harness.store.db, harness.store.now).record(SUGGESTION.recordId);
  if (peel === null) throw new Error(`the store holds no ${SUGGESTION.recordId}`);
  return peel;
}

/** One record revision, as a run would have written it. */
async function seedRecord(
  store: ActsStore,
  id: string,
  args: { root?: string; seq?: number; supersedes?: string } = {},
): Promise<string> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id, recipe_id,
       recipe_version, actor_kind, actor_id, title, created_at, payload)
     VALUES(?, 'finding', ?, ?, ?, NULL, 'run_1', 'recipe', 1, 'run', 'run_1', 'a finding', ?, '{}')`,
    [id, args.root ?? id, args.supersedes ?? null, args.seq ?? 0, stamp(store.now())],
  );
  return id;
}

const SUGGESTION = {
  recordId: "fnd_00000001",
  revision: 0,
  kind: "draft-issue",
  summary: "this finding is specific enough to become an issue",
  rationale: "it names the file and the change",
} as const;

test("the two doors are declared, and only the write one asks for write", () => {
  const harness = openHarness();
  const suggest = harness.doors.find((door) => door.action.name === ACTIONS.suggest);
  const suggestions = harness.doors.find((door) => door.action.name === ACTIONS.suggestions);
  expect(suggest?.action.caps).toEqual(["containers:write"]);
  expect(suggestions?.action.caps).toEqual(["containers:read"]);
});

test("an allowed plugin's suggestion is attributed to the plugin and never to the operator", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  const suggested = (await knock(harness, ACTIONS.suggest, SUGGESTION)) as {
    id: string;
    suggester: string;
    supersedes: string;
    outstanding: number;
  };
  expect(suggested.suggester).toBe(JEV);
  expect(suggested.supersedes).toBe("");
  expect(suggested.outstanding).toBe(1);

  // THE ROW ITSELF. `proposed_by_kind` is the author slot that is neither the operator nor a
  // run, and `proposed_by_id` is the plugin the allow-list named — not the principal the call
  // arrived under, which is what an argument-supplied actor would have written here.
  const [row] = await harness.store.db.query(
    `SELECT proposed_by_kind, proposed_by_id, summary, json_extract(payload, '$.revision') AS revision
       FROM next_actions WHERE id = ?`,
    [suggested.id],
  );
  expect(row?.["proposed_by_kind"]).toBe("engine");
  expect(row?.["proposed_by_id"]).toBe(JEV);
  expect(row?.["proposed_by_id"]).not.toBe(JEV_PRINCIPAL);
  expect(Number(row?.["revision"])).toBe(0);
  expect(harness.emitted.map((event) => event.kind)).toEqual([EVENTS.recordWritten]);
});

test("the operator answers a suggestion through the accept/decline path that already exists", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);
  const suggested = (await knock(harness, ACTIONS.suggest, SUGGESTION)) as { id: string };

  // Where the record is read: the peel offers it as one more proposed action, carrying the
  // plugin's name, and `decide` — the operator's door, unchanged — answers it.
  const peel = await peeled(harness);
  expect(peel.nextActions.map((action) => [action.id, action.proposedBy, action.standing])).toEqual(
    [[suggested.id, JEV, "proposed"]],
  );

  await knock(harness, ACTIONS.decide, {
    nextActionId: suggested.id,
    decision: "accepted",
    note: "",
  });
  const answered = await peeled(harness);
  expect(answered.nextActions[0]?.standing).toBe("accepted");
  expect(answered.nextActions[0]?.history[0]?.by).toBe(JEV_PRINCIPAL);

  // And the queue drains as he answers: outstanding is what he has NOT answered.
  const counted = (await knock(harness, ACTIONS.suggestions, {})) as {
    outstanding: number;
    answered: number;
    judged: number;
  };
  expect([counted.outstanding, counted.answered, counted.judged]).toEqual([0, 1, 1]);
});

test("an unlisted caller is refused by name, and told what would allow it", async () => {
  const harness = openHarness("prn_stranger");
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  const refused = await refusal(harness, ACTIONS.suggest, SUGGESTION);
  expect(refused).toContain("prn_stranger is not allowed to suggest");
  expect(refused).toContain("suggesters");
  expect(refused).toContain("setPolicy");
  expect(await harness.store.db.query(`SELECT id FROM next_actions`)).toEqual([]);
  // The reading half is the same gate: a stranger cannot count another plugin's queue either.
  expect(await refusal(harness, ACTIONS.suggestions, {})).toContain("not allowed to suggest");
});

test("no policy at all allows nobody: the authority is granted, never inherited", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await seedRecord(harness.store, SUGGESTION.recordId);
  const refused = await refusal(harness, ACTIONS.suggest, SUGGESTION);
  expect(refused).toContain("is not allowed to suggest");
});

test("the input document has nowhere to name an author", () => {
  const harness = openHarness();
  const door = harness.doors.find((candidate) => candidate.action.name === ACTIONS.suggest);
  // A strict object refuses a field nobody spelled, so the first buggy caller that tries to
  // write as somebody — the operator, a run, another plugin — is refused before the handler runs.
  for (const forged of ["proposedBy", "proposedByKind", "suggester", "actor", "operator"]) {
    expect(() => door?.action.input.parse({ ...SUGGESTION, [forged]: "operator" })).toThrow();
  }
});

test("a suggestion carries the revision it judged, and is refused where it does not match", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  const refused = await refusal(harness, ACTIONS.suggest, { ...SUGGESTION, revision: 1 });
  expect(refused).toBe(
    "fnd_00000001 is revision 0 and this suggestion was made against revision 1",
  );
  expect(await harness.store.db.query(`SELECT id FROM next_actions`)).toEqual([]);
});

test("a suggestion about a wording a refinement has replaced is refused, not inherited", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);
  // The operator accepted a refinement: the same root, a new revision, the old wording superseded.
  await seedRecord(harness.store, "fnd_00000002", {
    root: SUGGESTION.recordId,
    seq: 1,
    supersedes: SUGGESTION.recordId,
  });

  const refused = await refusal(harness, ACTIONS.suggest, SUGGESTION);
  expect(refused).toBe(
    "fnd_00000001 has been superseded by fnd_00000002 (revision 1): a suggestion is about the " +
      "revision it read",
  );
  // And the live revision takes one, so the refusal is about staleness rather than the record.
  const landed = (await knock(harness, ACTIONS.suggest, {
    ...SUGGESTION,
    recordId: "fnd_00000002",
    revision: 1,
  })) as { suggester: string };
  expect(landed.suggester).toBe(JEV);
});

test("a record the operator has ruled on takes no suggestion", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);
  // Ruled through the operator's own door, so the refusal is against a standing the store
  // actually derives rather than against a row this test invented.
  await knock(harness, ACTIONS.rule, { id: SUGGESTION.recordId, ruling: "reject", note: "no" });

  expect(await refusal(harness, ACTIONS.suggest, SUGGESTION)).toBe(
    "fnd_00000001 is rejected: the operator has ruled on it",
  );
  expect(await harness.store.db.query(`SELECT id FROM next_actions`)).toEqual([]);
});

test("the same suggestion twice is one live suggestion, the second superseding the first", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  const first = (await knock(harness, ACTIONS.suggest, SUGGESTION)) as { id: string };
  const second = (await knock(harness, ACTIONS.suggest, {
    ...SUGGESTION,
    summary: "restated, with the file named",
  })) as { id: string; supersedes: string; outstanding: number };
  const third = (await knock(harness, ACTIONS.suggest, {
    ...SUGGESTION,
    summary: "restated again",
  })) as { id: string; supersedes: string; outstanding: number };

  expect(second.supersedes).toBe(first.id);
  expect(third.supersedes).toBe(second.id);
  // Bounded at the reader: three calls, one choice in front of him, the newest.
  expect(third.outstanding).toBe(1);
  const peel = await peeled(harness);
  expect(peel.nextActions.map((action) => action.id)).toEqual([third.id]);
  // And the replaced rows are still there: `next_actions` is append-only, and what a suggester
  // said before is the evidence its later opinion is weighed against.
  expect((await harness.store.db.query(`SELECT id FROM next_actions`)).length).toBe(3);
});

test("the mark is per suggester, revision and kind: a second kind is a second suggestion", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  await knock(harness, ACTIONS.suggest, SUGGESTION);
  const other = (await knock(harness, ACTIONS.suggest, {
    ...SUGGESTION,
    kind: "develop-further",
    summary: "one more pass would settle it",
  })) as { supersedes: string; outstanding: number };
  expect(other.supersedes).toBe("");
  expect(other.outstanding).toBe(2);
});

test("a sweep can state its size before it runs", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, "fnd_00000001");
  await seedRecord(harness.store, "fnd_00000002");
  await seedRecord(harness.store, "fnd_00000003");
  // A revision something has replaced is not a live record and is not swept.
  await seedRecord(harness.store, "fnd_00000004", {
    root: "fnd_00000003",
    seq: 1,
    supersedes: "fnd_00000003",
  });

  const before = (await knock(harness, ACTIONS.suggestions, {})) as {
    suggester: string;
    judged: number;
    unjudged: number;
  };
  expect([before.suggester, before.judged, before.unjudged]).toEqual([JEV, 0, 3]);

  await knock(harness, ACTIONS.suggest, SUGGESTION);
  const after = (await knock(harness, ACTIONS.suggestions, {})) as {
    judged: number;
    unjudged: number;
    outstanding: number;
  };
  expect([after.judged, after.unjudged, after.outstanding]).toEqual([1, 2, 1]);
});

test("the frontier is untouched: the only table this door writes is next_actions", async () => {
  const harness = openHarness();
  await migrate(harness.store);
  await allow(harness.store, [{ principalId: JEV_PRINCIPAL, pluginId: JEV }]);
  await seedRecord(harness.store, SUGGESTION.recordId);

  const tables = (
    await harness.store.db.query<{ name: string }>(
      `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
    )
  ).map((row) => row.name);
  expect(tables.length).toBeGreaterThan(20);
  const census = async (): Promise<Record<string, number>> => {
    const counts: Record<string, number> = {};
    for (const table of tables) {
      const [row] = await harness.store.db.query(`SELECT COUNT(*) AS rows FROM "${table}"`);
      counts[table] = Number(row?.["rows"] ?? 0);
    }
    return counts;
  };

  const before = await census();
  harness.statements.length = 0;
  await knock(harness, ACTIONS.suggest, SUGGESTION);
  const after = await census();

  // WHAT CHANGED, over every table the migration creates rather than over a list somebody
  // remembered to keep current: exactly one more `next_actions` row, and nothing else moved.
  const moved = tables.filter((table) => before[table] !== after[table]);
  expect(moved).toEqual(["next_actions"]);
  expect(after["next_actions"]).toBe((before["next_actions"] ?? 0) + 1);

  // AND WHAT IT COULD HAVE CHANGED, from the statements themselves: no path this door opens
  // carries an INSERT, UPDATE or DELETE naming anything but `next_actions`, so the result above
  // is a property of the code and not of this particular input.
  const written = new Set<string>();
  for (const sql of harness.statements) {
    for (const match of sql.matchAll(/\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+"?(\w+)"?/gi)) {
      written.add(String(match[1]));
    }
  }
  expect([...written]).toEqual(["next_actions"]);
});
