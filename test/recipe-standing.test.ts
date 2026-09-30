import { afterEach, beforeEach, expect, test } from "bun:test";
import type { PluginDatabase, SqlRow } from "@manifold/plugin";
import type { GuestActions, GuestCtx } from "@manifold/plugin-kit/server";
import type { InstanceServiceDescription } from "@manifold/protocol";
import {
  ACTIONS,
  PolicyResultSchema,
  RECIPE_READING_BATCH,
  RECIPE_READING_TEXT_MAX,
  RecipeStandingSchema,
} from "../babel/contract.ts";
import { readDoors } from "../babel/doors/read.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import { recipeRecords } from "../babel/store/recipe-records.ts";
import { recipeStanding } from "../babel/jev/recipes.ts";
import { JEV_SERVICE, memoKey, type JevServices } from "../babel/jev/server/credential.ts";
import { JevAnswers, requestFor, requestKey } from "../babel/jev/server/judge.ts";

const AT = "2026-09-29T12:00:00.000Z";
let store: TestStore;
let actions: GuestActions;
let answers: JevAnswers;
let service: InstanceServiceDescription;
let invokes: number;
let services: JevServices;

beforeEach(async () => {
  store = await openTestStore(Date.parse(AT));
  const doors = readDoors(store.store);
  actions = {
    call: async ({ action, input }) => {
      const door = doors.find((entry) => entry.action.name === action);
      if (!door) throw new Error(`unexpected action ${action}`);
      return door.action.result.parse(
        await door.handler({} as GuestCtx, door.action.input.parse(input) as never),
      );
    },
  };
  answers = new JevAnswers();
  invokes = 0;
  service = {
    serviceId: JEV_SERVICE.serviceId,
    defaultOwner: null,
    owner: { machineId: "synthetic", name: "synthetic", online: true },
    configuration: {
      revision: "r7",
      pluginId: "atyrode.babel.jev",
      enabled: true,
      policySha256: "a".repeat(64),
    },
    connected: true,
    state: "ready",
    reason: null,
  };
  services = {
    listInstances: async () => ({ defaultOwner: null, services: [service] }),
    invokeInstance: async () => {
      invokes += 1;
      throw new Error("synthetic exhausted credit: cache-only reads must never reach this");
    },
  };
  await insert(store.db, "policies", {
    version: "p1",
    seq: 1,
    actor_id: "operator",
    reason: "fixture",
    recorded_at: AT,
    payload: JSON.stringify({
      recipes: [
        { id: "one", title: "One", looksFor: "one", enabled: true },
        { id: "zero", title: "Zero", looksFor: "zero", enabled: true },
      ],
    }),
  });
});
afterEach(() => store.close());

async function run(
  id: string,
  options: { worker?: unknown; native?: unknown; scalar?: string } = {},
) {
  await insert(store.db, "runs", {
    id,
    kind: "explore",
    recipe_id: options.scalar ?? "one",
    started_at: AT,
    records: 0,
    preparation: options.native === undefined ? null : JSON.stringify({ recipes: options.native }),
    payload: JSON.stringify(
      options.worker === undefined ? {} : { worker: { Recipes: options.worker } },
    ),
  });
}
async function record(
  n: number,
  runId: string | null,
  text: string,
  supersedes: string | null = null,
  options: { createdAt?: string; payload?: Record<string, unknown> } = {},
) {
  const id = `fnd_${n.toString(16).padStart(8, "0")}`;
  await insert(store.db, "records", {
    id,
    kind: "finding",
    root_id: supersedes ?? id,
    supersedes_id: supersedes,
    seq: supersedes === null ? 0 : 1,
    run_id: runId,
    recipe_id: "misleading-record-scalar",
    actor_kind: "run",
    actor_id: runId ?? "absent",
    title: "not the judgement text",
    created_at: options.createdAt ?? AT,
    payload: JSON.stringify(options.payload ?? { pattern: text }),
  });
  return id;
}
function hold(text: string, revision = "r7") {
  answers.set(memoKey(requestKey(requestFor("finding", text)), revision), {});
}
async function reading() {
  return RecipeStandingSchema.parse(await recipeStanding({ actions, services, answers }));
}

test("full worker/native selections override the first scalar; zero-output lenses and unknown funding remain explicit", async () => {
  await run("single", { worker: [{ id: "one" }] });
  await run("multi", { worker: [{ id: "one" }, { id: "two" }] });
  await run("empty", { worker: [] });
  await run("malformed", { worker: "one" });
  await run("native", { native: [{ id: "one" }, { id: "two" }] });
  await run("native-one", { native: [{ id: "one" }], scalar: "wrong" });
  await run("legacy");
  await record(1, "single", "known");
  await record(2, "multi", "known");
  await record(3, "empty", "known");
  await record(4, "malformed", "known");
  await record(5, "native", "known");
  await record(6, null, "known");
  await record(7, "legacy", "missing");
  await record(8, "native-one", "known");
  hold("known");
  const baseline = await actions.call({
    plugin: "atyrode.babel",
    action: ACTIONS.policy,
    input: {},
  });
  expect(PolicyResultSchema.parse(baseline).recipeRecords).toBeUndefined();
  const value = await reading();
  expect(value).toMatchObject({
    total: 8,
    eligible: 3,
    multiRecipe: 2,
    excluded: 3,
    funding: "unknown",
    coverage: "partial-cache",
    policyRevision: "r7",
    counts: {
      knownCached: 2,
      missingOrEvicted: 1,
      notInspected: 0,
      unjudged: null,
      bands: { unheard: 2, unjudged: 0 },
    },
  });
  expect(value?.recipes).toContainEqual({
    recipeId: "zero",
    eligible: 0,
    knownCached: 0,
    missingOrEvicted: 0,
    notInspected: 0,
    unjudged: null,
    bands: { unjudged: 0, unheard: 0, unremarked: 0, backed: 0, objected: 0, contested: 0 },
  });
  expect(
    await actions.call({ plugin: "atyrode.babel", action: ACTIONS.policy, input: {} }),
  ).toEqual(baseline);
  expect(invokes).toBe(0);
});

test("a replaced record cannot inherit the old text's position; policy changes and eviction are not never-judged evidence", async () => {
  await run("single");
  const old = await record(1, "single", "old");
  await record(2, "single", "replacement", old);
  await record(3, "single", "survivor");
  answers = new JevAnswers(2);
  hold("old");
  hold("survivor");
  expect(await reading()).toMatchObject({
    eligible: 2,
    excluded: 1,
    counts: { knownCached: 1, missingOrEvicted: 1, unjudged: null },
  });
  hold("replacement");
  hold("unrelated"); // evicts survivor after the prior reading kept it warm
  expect(await reading()).toMatchObject({
    counts: { knownCached: 1, missingOrEvicted: 1, unjudged: null },
  });
  service = { ...service, configuration: { ...service.configuration!, revision: "r8" } };
  expect(await reading()).toBeNull();
  hold("replacement", "r8");
  expect(await reading()).toMatchObject({ policyRevision: "r8", counts: { knownCached: 1 } });
  expect(invokes).toBe(0);
});

test("the full census is not capped by the inspected sample and older held answers do not become coverage", async () => {
  await run("single");
  await record(1, "single", "old-known");
  for (let n = 2; n <= RECIPE_READING_BATCH + 1; n++) {
    await record(n, "single", n === RECIPE_READING_BATCH + 1 ? "new-known" : `missing ${n}`);
  }
  await record(RECIPE_READING_BATCH + 2, "single", "x".repeat(8193));
  hold("old-known");
  hold("new-known");
  expect(await reading()).toMatchObject({
    eligible: RECIPE_READING_BATCH + 2,
    counts: {
      knownCached: 1,
      missingOrEvicted: RECIPE_READING_BATCH - 2,
      notInspected: 3,
      unjudged: null,
    },
  });
  expect(invokes).toBe(0);
});

test("the inspected page follows creation time rather than import insertion order", async () => {
  await run("single");
  await record(1, "single", "newer-known", null, { createdAt: "2026-09-30T00:00:00Z" });
  for (let n = 2; n <= RECIPE_READING_BATCH + 1; n++) {
    await record(n, "single", `older ${n}`);
  }
  hold("newer-known");
  expect(await reading()).toMatchObject({
    eligible: RECIPE_READING_BATCH + 1,
    counts: { knownCached: 1, missingOrEvicted: RECIPE_READING_BATCH - 1, notInspected: 1 },
  });
});

test("the sample cutoff preserves imported nanoseconds when id order favors older records", async () => {
  await run("single");
  for (let n = 1; n <= RECIPE_READING_BATCH + 1; n++) {
    const nanos = String(RECIPE_READING_BATCH + 2 - n).padStart(9, "0");
    await record(n, "single", `claim ${n}`, null, {
      createdAt: `2026-09-30T00:00:00.${nanos}Z`,
    });
  }
  const result = await recipeRecords(store.db);
  expect(result.records.map((row) => row.recordId).sort()).toEqual(
    Array.from(
      { length: RECIPE_READING_BATCH },
      (_, index) => `fnd_${(index + 1).toString(16).padStart(8, "0")}`,
    ),
  );
});

test.each([
  ["2026-09-30T00:00:00.123000001Z", "2026-09-30T00:00:00.123Z"],
  ["2026-09-30T00:00:00.000000001Z", "2026-09-30T00:00:00Z"],
])("the sample keeps %s ahead of the shorter UTC spelling %s", async (newer, older) => {
  await run("single");
  const newest = await record(1, "single", "at the cutoff", null, { createdAt: newer });
  const oldest = await record(2, "single", "past the cutoff", null, { createdAt: older });
  for (let n = 3; n <= RECIPE_READING_BATCH + 1; n++) {
    await record(n, "single", `later ${n}`, null, {
      createdAt: "2026-10-01T00:00:00.000Z",
    });
  }
  const result = await recipeRecords(store.db);
  const selected = result.records.map((row) => row.recordId);
  expect(selected).toContain(newest);
  expect(selected).not.toContain(oldest);
});

test("large unrelated payloads and NUL-hidden claim tails never cross the bounded database handoff", async () => {
  await run("single");
  await record(1, "single", "short claim", null, {
    payload: { pattern: "short claim", retainedImport: "x".repeat(100_000) },
  });
  await record(2, "single", "x".repeat(8193));
  await record(3, "single", `prefix\u0000${"x".repeat(100_000)}`);
  const bounded: PluginDatabase = {
    ...store.db,
    query: async <Row extends SqlRow>(...args: Parameters<PluginDatabase["query"]>) => {
      const rows = await store.db.query<Row>(...args);
      const transferred = JSON.stringify(rows, (_, value: unknown) =>
        typeof value === "bigint" ? String(value) : value,
      );
      expect(Buffer.byteLength(transferred)).toBeLessThan(32_768);
      return rows;
    },
  };
  const result = await recipeRecords(bounded);
  expect(result.records.map(({ recordId, text }) => ({ recordId, text }))).toEqual([
    { recordId: "fnd_00000003", text: null },
    { recordId: "fnd_00000002", text: null },
    { recordId: "fnd_00000001", text: "short claim" },
  ]);
});

test("the database byte guard preserves complete multibyte claims at its character bound", async () => {
  await run("single");
  const bmp = "\u754c".repeat(RECIPE_READING_TEXT_MAX);
  const astral = "\u{10400}".repeat(RECIPE_READING_TEXT_MAX / 2);
  const scalarBound = "\u{10400}".repeat(RECIPE_READING_TEXT_MAX);
  await record(1, "single", bmp);
  await record(2, "single", astral);
  await record(3, "single", scalarBound);
  const bounded: PluginDatabase = {
    ...store.db,
    query: async <Row extends SqlRow>(...args: Parameters<PluginDatabase["query"]>) => {
      const rows = await store.db.query<Row>(...args);
      // SQL counts scalars; the existing API's UTF-16 limit is applied after this handoff.
      expect(rows.find((row) => row["id"] === "fnd_00000003")?.["text"]).toBe(scalarBound);
      return rows;
    },
  };
  const result = await recipeRecords(bounded);
  expect(result.records.find((row) => row.recordId === "fnd_00000001")?.text).toBe(bmp);
  expect(result.records.find((row) => row.recordId === "fnd_00000002")?.text).toBe(astral);
});

test("absent, disabled, cold and changed-during-read Jev all remove the enhancement without invoking", async () => {
  await run("single");
  await record(1, "single", "known");
  expect(await reading()).toBeNull();
  hold("known");
  service = { ...service, state: "stopped" };
  expect(await reading()).toBeNull();
  services.listInstances = async () => ({ defaultOwner: null, services: [] });
  expect(await reading()).toBeNull();
  service = { ...service, state: "ready" };
  let rosters = 0;
  services.listInstances = async () => ({
    defaultOwner: null,
    services: ++rosters === 1 ? [service] : [],
  });
  expect(await reading()).toBeNull();
  expect(invokes).toBe(0);
});

test("old bank and document answers cannot be read as current cached standings", async () => {
  await run("single");
  await record(1, "single", "old bank");
  await record(2, "single", "old document");
  await record(3, "single", "current");
  const bank = requestFor("finding", "old bank");
  const document = requestFor("finding", "old document");
  answers.set(memoKey(requestKey({ ...bank, bankVersion: bank.bankVersion + 1 }), "r7"), {});
  answers.set(
    memoKey(requestKey({ ...document, documentVersion: document.documentVersion + 1 }), "r7"),
    {},
  );
  hold("current");
  expect(await reading()).toMatchObject({
    eligible: 3,
    counts: { knownCached: 1, missingOrEvicted: 2, notInspected: 0, unjudged: null },
  });
  expect(invokes).toBe(0);
});
