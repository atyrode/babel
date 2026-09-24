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
import {
  ACTIONS,
  CONDUCTOR_CYCLE_KEY,
  FeedResultSchema,
  RecordPeelSchema,
  door,
} from "../contract.ts";
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
/** This plugin's keys, as the host serves them: where the conductor leaves its last verdict. */
let kept: Record<string, string>;

/** What the kit does on one dispatch, minus the boundary it does it across. */
async function dispatch(name: string, args: unknown): Promise<unknown> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success)
    return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
  // The read handlers touch no slice of the host but this plugin's own keys, which is what
  // makes them dispatchable with nothing but their arguments and a key store; a handler that
  // reached for a machine or a job would fail here by name.
  const ctx = {
    storage: { get: async (key: string) => kept[key] ?? null },
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
  kept = {};
  const { db } = harness;
  await insert(db, "entities", {
    id: TOPIC,
    kind: "repository",
    name: "tyrode-infra",
    canonical_id: TOPIC,
    created_by: "operator",
    created_at: stamp(NOW - HOUR),
  });
  await insert(db, "records", {
    id: RECORD,
    kind: "proposal",
    root_id: RECORD,
    seq: 1,
    run_id: "run-a",
    recipe_id: "outcome-integrity",
    recipe_version: 3,
    actor_kind: "run",
    actor_id: "run-a",
    title: "a proposal a reader can open",
    created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({
      schema: 1,
      title: "a proposal a reader can open",
      problem: "p",
      outcome: "o",
    }),
  });
  await insert(db, "records", {
    id: OBSERVATION,
    kind: "observation",
    root_id: OBSERVATION,
    seq: 1,
    run_id: "run-a",
    actor_kind: "run",
    actor_id: "run-a",
    title: "an observation",
    created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1, claim: "an observation", evidence: [] }),
  });
  await insert(db, "filings", {
    id: "fil_0001",
    record_id: RECORD,
    entity_id: TOPIC,
    rationale: "it is about this",
    author_kind: "operator",
    author_id: "operator",
    created_at: stamp(NOW - HOUR),
  });
  await insert(db, "runs", {
    id: "run-a",
    kind: "explore",
    machine_id: "dev-01",
    job_id: "job-a",
    recipe_id: "outcome-integrity",
    started_at: stamp(NOW - 2 * HOUR),
    finished_at: stamp(NOW - HOUR),
    closure: "completed",
    cost_usd: 0.2,
    tokens: 1000,
    records: 1,
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
      ACTIONS.feed,
      ACTIONS.record,
      ACTIONS.thread,
      ACTIONS.topics,
      ACTIONS.topic,
      ACTIONS.pulse,
      ACTIONS.runs,
      ACTIONS.run,
      ACTIONS.policy,
    ]);
    expect(new Set(names).size).toBe(names.length);
    for (const entry of doors) {
      expect(entry.action.caps).toEqual(["containers:read"]);
      expect(entry.action.title).not.toBe("");
    }
    /*
      THE TWO READS A CYCLE FOLLOWS ARE LENT WHAT THAT CYCLE SPENDS, and the other seven are lent
      nothing. `jobs:read` is the ingestion behind a wake; `machines:read` is the describe
      `reconcileSchedule` registers the loop's cadence from, delegable since
      atyrode/manifold#740; `machines:run` is what the schedule itself, an analysis stage's
      preparation and a drain's relaunch are discharged against (#448). None is a cap: a reader
      asking for his own pulse holds `containers:read` and is asked for nothing else, which the
      loop above has just pinned.
    */
    const cycled = ["jobs:read", "machines:read", "machines:run"];
    const lent: Record<string, readonly string[]> = {};
    for (const entry of doors) lent[entry.action.name] = [...(entry.action.delegates ?? [])];
    expect(lent).toEqual({
      [ACTIONS.feed]: [],
      [ACTIONS.record]: [],
      [ACTIONS.thread]: [],
      [ACTIONS.topics]: [],
      [ACTIONS.topic]: [],
      [ACTIONS.pulse]: cycled,
      [ACTIONS.runs]: cycled,
      [ACTIONS.run]: [],
      [ACTIONS.policy]: [],
    });
    // The roster publishes the plugin's own prefix, which is what a button's `action` spells.
    expect(door(ACTIONS.feed)).toBe("atyrode.babel.feed");
  });
});

describe("the vocabulary", () => {
  // A misspelled sort answered with the default order would silently show a reader a different
  // feed from the one he asked for; a kind nothing matches answered with an empty list reads as
  // a deployment that has produced none of them. Both refuse, and the refusal names the value.
  test("a sort, a window and a kind this feed does not have are refused by name", async () => {
    for (const [field, value] of [
      ["sort", "popular"],
      ["window", "fortnight"],
      ["kinds", "rumour"],
    ] as const) {
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
    const answer = await dispatch(ACTIONS.feed, { surface: "all", window: "all" });
    expect(answer).toMatchObject({ total: 1, notice: "" });
  });

  test("the peel answers inside its own schema", async () => {
    const answer = await dispatch(ACTIONS.record, { id: RECORD });
    expect(answer).toMatchObject({
      claim: { statement: "o", standing: "new", act: "Rule on this" },
    });
  });

  test("a stored question returned by Feed can be opened through the record door", async () => {
    const id = "qst_0123456789abcdef";
    const text = "Which repository owns this configuration?";
    await insert(harness.db, "questions", {
      id,
      kind: "clarify",
      class: "curiosity",
      text,
      why: "two repositories share the name",
      dedupe_key: null,
      raised_by_kind: "run",
      raised_by_id: "run-a",
      payload: "{}",
      created_at: stamp(NOW - HOUR),
    });
    const feed = FeedResultSchema.parse(
      await dispatch(ACTIONS.feed, {
        kinds: ["question"],
        surface: "all",
        window: "all",
      }),
    );
    expect(feed.posts).toMatchObject([{ id, kind: "question" }]);
    const selected = feed.posts[0]!;
    expect(await dispatch(ACTIONS.record, { id: selected.id })).toMatchObject({
      claim: { statement: text },
    });
  });

  test("a record this deployment does not hold is refused, and the refusal names it", async () => {
    const answer = await dispatch(ACTIONS.record, { id: "pro_0000dead" });
    expect(answer).toEqual({ refused: "no record pro_0000dead" });
  });

  test("an observation opens in its own kind without becoming a feed post", async () => {
    const answer = RecordPeelSchema.parse(await dispatch(ACTIONS.record, { id: OBSERVATION }));
    expect(answer.post).toMatchObject({
      id: OBSERVATION,
      kind: "observation",
      title: "an observation",
    });
    expect(answer.claim.statement).toBe("an observation");
  });

  test("the thread, the topics, one topic and the pulse answer inside their schemas", async () => {
    expect(await dispatch(ACTIONS.thread, { id: RECORD })).toEqual({
      comments: [],
      acts: [],
      total: 0,
    });
    expect(await dispatch(ACTIONS.topics, {})).toMatchObject({ unfiled: 0 });
    expect(await dispatch(ACTIONS.topic, { topic: "tyrode-infra" })).toMatchObject({
      topic: { id: TOPIC, name: "tyrode-infra", posts: 1 },
    });
    expect(await dispatch(ACTIONS.pulse, {})).toMatchObject({
      since: stamp(Date.UTC(2026, 8, 12)),
      reviewing: [],
      // No cycle has run against this store, which is a state and not a missing field: a
      // deployment enabled a minute ago has a pulse and no verdict yet.
      cycle: null,
    });
  });

  test("the pulse carries the last cycle's stop and its gaps counted by reason", async () => {
    kept[CONDUCTOR_CYCLE_KEY] = JSON.stringify({
      at: stamp(NOW - 60_000),
      stop: { reason: "unrouted", detail: "policy pol_3 names no Code profile" },
      gaps: [
        { reason: "claimed", count: 412, recordId: "hyp_00000009", detail: "already claimed" },
        { reason: "cooling", count: 2, recordId: "fnd_0000000a", detail: "reviewed 9m ago" },
      ],
    });

    expect(await dispatch(ACTIONS.pulse, {})).toMatchObject({
      cycle: {
        at: stamp(NOW - 60_000),
        stop: { reason: "unrouted", detail: "policy pol_3 names no Code profile" },
        // Counted, never listed: four hundred contended draws are one row with a figure on it.
        gaps: [
          { reason: "claimed", count: 412, recordId: "hyp_00000009", detail: "already claimed" },
          { reason: "cooling", count: 2, recordId: "fnd_0000000a", detail: "reviewed 9m ago" },
        ],
      },
    });
  });

  test("a verdict from a build that spelled its reasons differently is no verdict", async () => {
    // The door's own vocabulary is the coordinator's, and a word outside it is a value this
    // build cannot render. Losing the explanation is the right loss; refusing the dispatch
    // would take today's counts off Home to report that an explanation could not be read.
    kept[CONDUCTOR_CYCLE_KEY] = JSON.stringify({
      at: stamp(NOW),
      stop: { reason: "out-of-cheese", detail: "redo from start" },
      gaps: [],
    });
    expect(await dispatch(ACTIONS.pulse, {})).toMatchObject({ cycle: null });

    kept[CONDUCTOR_CYCLE_KEY] = "{not json";
    expect(await dispatch(ACTIONS.pulse, {})).toMatchObject({ cycle: null });
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

  /*
    WHAT A PANEL LEARNS ABOUT A JOB THAT HAS NOT FINISHED (#261, #169).

    Through `runs` and nothing else: it is the door Watch's live table calls, and before this
    the only thing it could say about a running job was that it existed. The row below is one
    the conductor's fold would have written — a job eleven minutes at the model, three calls
    metered, and a SECOND model on the newest of them.
  */
  test("a running job's stage, spend and the models that answered come back through `runs`", async () => {
    const { db } = harness;
    await insert(db, "runs", {
      id: "run-live",
      kind: "explore",
      machine_id: "dev-01",
      job_id: "job-live",
      recipe_id: "outcome-integrity",
      started_at: stamp(NOW - HOUR),
      records: 0,
      payload: "{}",
    });
    await insert(db, "run_progress", {
      run_id: "run-live",
      job_id: "job-live",
      stage: "at the model",
      message: "reading the corpus",
      since: stamp(NOW - 11 * 60_000),
      calls: 3,
      input_tokens: 12_000,
      output_tokens: 900,
      cache_tokens: 400,
      cost_usd: 0.31,
      last_model: "anthropic/claude-sonnet-4",
      last_call_at: stamp(NOW - 20_000),
      seq: 40,
      models: JSON.stringify(["anthropic/claude-opus-4-1", "anthropic/claude-sonnet-4"]),
      stalled: 0,
      updated_at: stamp(NOW - 20_000),
    });
    harness.store.touch();

    const answer = (await dispatch(ACTIONS.runs, {})) as {
      runs: readonly {
        id: string;
        models: readonly string[];
        progress: Record<string, unknown> | null;
      }[];
    };
    const live = answer.runs.find((row) => row.id === "run-live");
    expect(live?.progress).toEqual({
      stage: "at the model",
      message: "reading the corpus",
      fraction: null,
      since: stamp(NOW - 11 * 60_000),
      calls: 3,
      inputTokens: 12_000,
      outputTokens: 900,
      cacheTokens: 400,
      costUsd: 0.31,
      lastModel: "anthropic/claude-sonnet-4",
      stalled: false,
      updatedAt: stamp(NOW - 20_000),
      unheard: false,
    });
    // THE FALLBACK IS THE POINT. `lastModel` is what answered the newest call and would have
    // been the whole record; both models are here, in the order this run first heard them, so
    // a reader can see that it did not start on the one that is answering now.
    expect(live?.models).toEqual(["anthropic/claude-opus-4-1", "anthropic/claude-sonnet-4"]);
  });

  test("the models a settled run was answered by are read off its receipt", async () => {
    await harness.db.run(`UPDATE runs SET payload = ? WHERE id = 'run-a'`, [
      JSON.stringify({
        runId: "run-a",
        counts: { records: 1 },
        model: "anthropic/claude-opus-4-1",
        models: ["anthropic/claude-sonnet-4"],
      }),
    ]);
    harness.store.touch();
    const answer = (await dispatch(ACTIONS.runs, {})) as {
      runs: readonly { id: string; models: readonly string[] }[];
    };
    // ONE FIELD, BOTH HALVES OF A RUN'S LIFE: the fold's row is gone, and the same `models` a
    // live row answered from is now the receipt's. What it ASKED for stays `model` on the
    // receipt the `run` door hands back whole, so the two are never one sentence again.
    expect(answer.runs.find((row) => row.id === "run-a")?.models).toEqual([
      "anthropic/claude-sonnet-4",
    ]);
    expect(await dispatch(ACTIONS.run, { id: "run-a" })).toMatchObject({
      receipt: { model: "anthropic/claude-opus-4-1", models: ["anthropic/claude-sonnet-4"] },
    });
  });

  /*
    A JOB THAT DIED BETWEEN TWO FOLDS (#261).

    Nothing deletes a progress row but a settlement, so a job whose hub stopped answering — or
    whose loop stopped waking — leaves its last fold standing for ever. The row is still shown,
    because it is the last true thing anyone observed, but it is marked: the stage is a reading
    taken at `updatedAt` and not a claim about now.
  */
  test("a fold nobody has refreshed is reported unheard rather than as the present", async () => {
    const { db } = harness;
    await insert(db, "runs", {
      id: "run-gone",
      kind: "explore",
      machine_id: "dev-01",
      job_id: "job-gone",
      started_at: stamp(NOW - HOUR),
      records: 0,
      payload: "{}",
    });
    await insert(db, "run_progress", {
      run_id: "run-gone",
      job_id: "job-gone",
      stage: "at the model",
      since: stamp(NOW - 40 * 60_000),
      calls: 1,
      last_model: "anthropic/claude-opus-4-1",
      last_call_at: stamp(NOW - 35 * 60_000),
      seq: 12,
      models: JSON.stringify(["anthropic/claude-opus-4-1"]),
      stalled: 0,
      updated_at: stamp(NOW - 6 * 60_000),
    });
    harness.store.touch();

    const answer = (await dispatch(ACTIONS.runs, {})) as {
      runs: readonly { id: string; progress: { stage: string; unheard: boolean } | null }[];
    };
    const gone = answer.runs.find((row) => row.id === "run-gone");
    expect(gone?.progress?.unheard).toBe(true);
    // The stage is NOT erased: "nobody has confirmed this since" is a different sentence from
    // "this job is nowhere", and only the first one is true.
    expect(gone?.progress?.stage).toBe("at the model");
  });
});
