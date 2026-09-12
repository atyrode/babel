/*
  The reading doors, held to the two things a door is: a declaration and an answer.

  Every test here dispatches the way the kit does — parse the arguments against the action's own
  input, run the handler, parse what it produced against the action's own result — because that
  is the whole of what a door guarantees. The result schemas are strict, so this is not plumbing:
  a peel with a field the contract does not name, a topic id that is not an entity id, a run
  state outside the five, all refuse the dispatch here exactly as they would on a hub.
*/

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, door } from "../contract.ts";
import { stamp } from "../store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import { readDoors } from "./read.ts";
import type { Door } from "./door.ts";

const HOUR = 60 * 60 * 1000;
const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const RECORD = "pro_00000001";
const OBSERVATION = "obs_00000002";
const TOPIC = "ent_00000001";

let harness: TestStore;
let doors: readonly Door[];

/** What the kit does on one dispatch, minus the boundary it does it across. */
async function dispatch(name: string, args: unknown): Promise<unknown> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success) return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
  // The read handlers touch no slice of the host, which is what makes them dispatchable with
  // nothing but their arguments; a handler that reached for one would fail here by name.
  const produced = await found.handler(undefined as unknown as GuestCtx, parsed.data as never);
  if (typeof produced === "object" && produced !== null && "refused" in produced) return produced;
  const result = found.action.result.safeParse(produced);
  if (!result.success) {
    throw new Error(`${name} produced a result outside its schema: ${result.error.message}`);
  }
  return result.data;
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  const { db } = harness;
  await insert(db, "entities", {
    id: TOPIC, kind: "repository", name: "tyrode-infra", canonical_id: TOPIC,
    created_by: "operator", created_at: stamp(NOW - HOUR),
  });
  await insert(db, "records", {
    id: RECORD, kind: "proposal", root_id: RECORD, seq: 1, run_id: "run-a",
    recipe_id: "outcome-integrity", recipe_version: 3, actor_kind: "run", actor_id: "run-a",
    title: "a proposal a reader can open", created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1, title: "a proposal a reader can open", problem: "p", outcome: "o" }),
  });
  await insert(db, "records", {
    id: OBSERVATION, kind: "observation", root_id: OBSERVATION, seq: 1, run_id: "run-a",
    actor_kind: "run", actor_id: "run-a", title: "an observation", created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1, claim: "an observation", evidence: [] }),
  });
  await insert(db, "filings", {
    id: "fil_0001", record_id: RECORD, entity_id: TOPIC, rationale: "it is about this",
    author_kind: "operator", author_id: "operator", created_at: stamp(NOW - HOUR),
  });
  await insert(db, "runs", {
    id: "run-a", kind: "explore", machine_id: "dev-01", job_id: "job-a",
    recipe_id: "outcome-integrity", started_at: stamp(NOW - 2 * HOUR),
    finished_at: stamp(NOW - HOUR), closure: "completed", cost_usd: 0.2, tokens: 1000, records: 1,
    payload: JSON.stringify({ runId: "run-a", counts: { records: 1 } }),
  });
  doors = readDoors(harness.store);
});

afterEach(() => {
  harness.close();
});

describe("the roster", () => {
  test("declares the nine reading doors, once each, read-only", () => {
    const names = doors.map((entry) => entry.action.name);
    expect(names).toEqual([
      ACTIONS.feed, ACTIONS.record, ACTIONS.thread, ACTIONS.topics, ACTIONS.topic,
      ACTIONS.pulse, ACTIONS.runs, ACTIONS.run, ACTIONS.policy,
    ]);
    expect(new Set(names).size).toBe(names.length);
    for (const entry of doors) {
      expect(entry.action.caps).toEqual(["containers:read"]);
      expect(entry.action.title).not.toBe("");
    }
    // The roster publishes the plugin's own prefix, which is what a button's `action` spells.
    expect(door(ACTIONS.feed)).toBe("atyrode.babel.feed");
  });
});

describe("the vocabulary", () => {
  // A misspelled sort answered with the default order would silently show a reader a different
  // feed from the one he asked for; a kind nothing matches answered with an empty list reads as
  // a deployment that has produced none of them. Both refuse, and the refusal names the value.
  test("a sort, a window and a kind this feed does not have are refused by name", async () => {
    for (const [field, value] of [["sort", "popular"], ["window", "fortnight"], ["kinds", "rumour"]] as const) {
      const args = field === "kinds" ? { kinds: [value] } : { [field]: value };
      const answer = await dispatch(ACTIONS.feed, args);
      expect(answer).toHaveProperty("invalid");
    }
  });

  // §4.13's last reading: observations are evidence, not posts, so asking for them by name is
  // refused like any other kind this feed does not have.
  test("asking the feed for observations is refused", async () => {
    expect(await dispatch(ACTIONS.feed, { kinds: ["observation"] })).toHaveProperty("invalid");
  });

  test("an empty request is the defaults rather than a refusal", async () => {
    const answer = await dispatch(ACTIONS.feed, {});
    expect(answer).toHaveProperty("posts");
    expect(answer).toHaveProperty("builtAt");
  });

  test("an identifier that names no record family is refused before the store is asked", async () => {
    expect(await dispatch(ACTIONS.record, { id: "not-an-id" })).toHaveProperty("invalid");
    expect(await dispatch(ACTIONS.record, { id: RECORD, extra: 1 })).toHaveProperty("invalid");
  });
});

describe("the answers", () => {
  test("the feed answers inside its own schema", async () => {
    const answer = await dispatch(ACTIONS.feed, { needs: "all", window: "all" });
    expect(answer).toMatchObject({ total: 1, notice: "" });
  });

  test("the peel answers inside its own schema", async () => {
    const answer = await dispatch(ACTIONS.record, { id: RECORD });
    expect(answer).toMatchObject({ claim: { statement: "o", standing: "new", act: "Rule on this" } });
  });

  test("a record this deployment does not hold is refused, and the refusal names it", async () => {
    const answer = await dispatch(ACTIONS.record, { id: "pro_0000dead" });
    expect(answer).toEqual({ refused: "no record pro_0000dead" });
  });

  test("an observation is refused by name rather than served as a post", async () => {
    const answer = await dispatch(ACTIONS.record, { id: OBSERVATION });
    expect(answer).toHaveProperty("refused");
    expect((answer as { refused: string }).refused).toContain(OBSERVATION);
    expect((answer as { refused: string }).refused).toContain("evidence");
  });

  test("the thread, the topics, one topic and the pulse answer inside their schemas", async () => {
    expect(await dispatch(ACTIONS.thread, { id: RECORD })).toEqual({ comments: [], acts: [], total: 0 });
    expect(await dispatch(ACTIONS.topics, {})).toMatchObject({ unfiled: 0 });
    expect(await dispatch(ACTIONS.topic, { topic: "tyrode-infra" })).toMatchObject({
      topic: { id: TOPIC, name: "tyrode-infra", posts: 1 },
    });
    expect(await dispatch(ACTIONS.pulse, {})).toMatchObject({
      since: stamp(Date.UTC(2026, 8, 12)),
      reviewing: [],
    });
  });

  test("the runs, one run and the policy answer inside their schemas", async () => {
    expect(await dispatch(ACTIONS.runs, {})).toMatchObject({ total: 1 });
    expect(await dispatch(ACTIONS.run, { id: "run-a" })).toMatchObject({
      run: { id: "run-a", state: "finished", freshness: "ended" },
      receipt: { runId: "run-a" },
    });
    // A deployment whose operator has recorded no policy answers with the empty one rather than
    // with a refusal: there is nothing in force, and saying so is the honest answer.
    expect(await dispatch(ACTIONS.policy, {})).toMatchObject({
      version: "",
      ceilings: { perRunUsd: 0, perDayUsd: 0, concurrent: 0 },
      recipes: [{ id: "outcome-integrity", runs: 1, lastRunId: "run-a" }],
    });
  });
});
