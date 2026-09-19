/*
  THE SEARCH DOOR (#337), dispatched the way the kit does it.

  What a door guarantees is a declaration and an answer, so every test here parses the arguments
  against the action's own input, runs the handler, and parses what it produced against the action's
  own result. The result schema is strict, so the coverage block and the three approximation
  numbers are not plumbing: an answer that omitted one refuses the dispatch here exactly as it
  would on a hub, which is what stops a partially built index from ever answering silently.

  The ceiling is the other half. `services:invoke` is declared as a DELEGATE rather than a `caps`
  entry, because the two panels hold no capabilities of their own and call the baseline's doors as
  the viewer — a `caps` entry would make the search box unreachable from the surface it exists for.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, door } from "../contract.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import { searchDoors } from "./search.ts";
import type { Door } from "./door.ts";

const NOW = Date.UTC(2026, 8, 19, 12, 0, 0);

let harness: TestStore;
let doors: readonly Door[];
/** Every invocation the handler asked the host for, so "no policy, no call" is a count. */
let asks: unknown[];

async function dispatch(name: string, args: unknown): Promise<unknown> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success)
    return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
  const ctx = {
    services: {
      listInstances: async () => ({ defaultOwner: null, services: [] }),
      invokeInstance: async (invocation: unknown) => {
        asks.push(invocation);
        throw new Error("a test host that was never meant to be called was called");
      },
    },
  } as unknown as GuestCtx;
  const produced = await found.handler(ctx, parsed.data as never);
  if (typeof produced === "object" && produced !== null && "refused" in produced) return produced;
  const result = found.action.result.safeParse(produced);
  if (!result.success) {
    throw new Error(`${name} produced a result outside its schema: ${result.error.message}`);
  }
  return result.data;
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  asks = [];
  doors = searchDoors(harness.store);
  await insert(harness.db, "records", {
    id: "fnd_00000001",
    kind: "finding",
    root_id: "fnd_00000001",
    seq: 0,
    actor_kind: "run",
    actor_id: "run_1",
    title: "The drain stalls at zero",
    created_at: stamp(NOW),
    payload: JSON.stringify({ pattern: "the fan never drains", significance: "a lost window" }),
  });
});

afterEach(() => {
  harness.close();
});

test("the door is one read, named once, holding the service ceiling as a delegate", () => {
  const found = doors[0];
  expect(doors).toHaveLength(1);
  expect(found?.action.name).toBe(ACTIONS.search);
  expect(door(ACTIONS.search)).toBe("atyrode.babel.search");
  expect(found?.action.caps).toEqual(["containers:read"]);
  expect(found?.action.delegates).toEqual(["services:invoke"]);
});

test("a search answers through the door, with its own account of what it could not use", async () => {
  const answer = (await dispatch(ACTIONS.search, { query: "the drain never drains" })) as Record<
    string,
    unknown
  >;
  expect((answer["hits"] as readonly Record<string, unknown>[])[0]?.["id"]).toBe("fnd_00000001");
  expect(answer["meaning"]).toBe("absent");
  expect(String(answer["meaningAbsent"])).toContain("keyword alone");
  expect(answer["coverage"]).toEqual({
    records: 1,
    keyworded: 1,
    embedded: 0,
    empty: 0,
    stale: 0,
    unnameable: 0,
    model: "",
  });
  // With no policy installed the handler reaches the roster and stops: the invocation is not
  // made, so a hub that configured nothing sends nothing and this door cannot spend.
  expect(asks).toEqual([]);
});

test("a query longer than the door admits is refused rather than truncated", async () => {
  const refused = (await dispatch(ACTIONS.search, { query: "x".repeat(513) })) as Record<
    string,
    unknown
  >;
  // A search whose last third was silently dropped answers confidently about something nobody
  // asked, which is worse than a refusal a caller can see.
  expect(refused["invalid"]).toBeDefined();
  expect(refused["hits"]).toBeUndefined();
});

test("one record the door cannot name does not take the dispatch with it", async () => {
  // A row of the shape a store imported before #416's guard holds, seeded straight into the
  // table because the write path now refuses it and `records_kept` refuses to delete it. It
  // ranks for this query: before #426 the hit's id failed `SearchResultSchema` on the way out
  // and the caller got a 500 with a validator dump instead of the record it could have had.
  await insert(harness.db, "records", {
    id: "rec_seed_001",
    kind: "finding",
    root_id: "rec_seed_001",
    seq: 0,
    actor_kind: "run",
    actor_id: "run_1",
    title: "The drain stalls at zero and the drain drains nothing",
    created_at: stamp(NOW),
    payload: JSON.stringify({ pattern: "the drain never drains", significance: "a lost window" }),
  });
  const answer = (await dispatch(ACTIONS.search, { query: "the drain never drains" })) as Record<
    string,
    unknown
  >;
  const hits = answer["hits"] as readonly Record<string, unknown>[];
  expect(hits.map((hit) => hit["id"])).toEqual(["fnd_00000001"]);
  expect((answer["coverage"] as Record<string, unknown>)["unnameable"]).toBe(1);
});
