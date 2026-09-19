import { afterEach, beforeEach, expect, test } from "bun:test";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { ACTIONS, BABEL_PLUGIN_ID, JEV_PLUGIN_ID, type ActionName } from "../babel/contract.ts";
import type { Door } from "../babel/doors/door.ts";
import { readDoors } from "../babel/doors/read.ts";
import { stamp } from "../babel/store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import babelManifest from "../babel/manifest.json";

/*
  THE PART IS OPTIONAL, AND THAT IS A PROPERTY OF THE BASELINE.

  `atyrode.babel.jev` is judgement Babel never requires: absent, disabled or out of credit, every
  door answers what it answers today. The whole epic rests on that fallback, and a fallback nobody
  has run is not a fallback — so the case is dispatched here rather than described.

  WHAT MAKES IT CHECKABLE IS THE HOST'S OWN RULE. A plugin reaches a sibling only through
  `ctx.actions.call`, and the host refuses a call whose callee the CALLER's manifest does not
  declare — `undeclared_dependency`, "composition is declared, never discovered" (manifold
  `packages/protocol/src/plugin.ts`) — and refuses a declared edge that is not composed with
  `dependency_unavailable`. The baseline's manifest declares `atyrode.code` and nothing else, so
  a door that consulted the part would meet one of those two refusals on every hub, whether or
  not the part is installed. {@link refusing} is that hub: it answers every call the way the host
  would answer it with the part gone.

  So each read door is dispatched twice over ONE seeded store — once where a call to the part
  would resolve, once where it is refused at the edge — and the two answers must be equal. Today
  nothing calls the part and the answers are trivially the same; the day a child ranks the front
  page on a tally, this is the test that refuses a door which cannot answer without it.

  The reading doors are the surface the panels read through and the surface every child of the
  epic touches (the feed's ranking, the peel, the topic). The acts are not dispatched here: a
  write's effect is the store, and the part has no database, no store and no reach into one —
  its only road is a door, which is what this file holds. That a cycle survives a refused sibling
  call is `babel/server.test.ts`'s ("a cycle that stumbles never fails the door it
  followed"), and it is proved there rather than restated here.
*/

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const RECORD = "pro_00000001";
const OBSERVATION = "obs_00000002";
const TOPIC = "ent_00000001";
const TOPIC_NAME = "tyrode-infra";
const RUN = "run-a";

/*
  `babelManifest.dependencies` is read at both hubs below because it is the ceiling the host
  enforces: a callee the caller's manifest does not name is refused before the callee is asked.
*/

let harness: TestStore;
let doors: readonly Door[];

/**
 * A hub where the part is installed and answering. Anything the baseline never declared is
 * still refused, because the host refuses the edge and not the plugin.
 */
const serving: GuestCtx["actions"] = {
  call: async ({ plugin, action }) => {
    if (plugin === JEV_PLUGIN_ID) return await Promise.resolve({});
    if (Object.hasOwn(babelManifest.dependencies, plugin)) return await Promise.resolve({});
    throw new ActionCallError(`undeclared_dependency: ${BABEL_PLUGIN_ID} -> ${plugin}.${action}`);
  },
};

/**
 * THE HUB AS IT IS TODAY: the part is not installed. A call naming it is `undeclared_dependency`
 * — the baseline's manifest names no such edge — and the sentence is the host's own shape, which
 * is the one `server/engine/session.ts` reads a class off.
 */
const refusing: GuestCtx["actions"] = {
  call: async ({ plugin, action }) => {
    if (Object.hasOwn(babelManifest.dependencies, plugin)) return await Promise.resolve({});
    throw new ActionCallError(`undeclared_dependency: ${BABEL_PLUGIN_ID} -> ${plugin}.${action}`);
  },
};

/** One dispatch, as the kit does it: the door's own input, its handler, its own result. */
async function dispatch(
  name: string,
  args: unknown,
  actions: GuestCtx["actions"],
): Promise<unknown> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const asked = found.action.input.parse(args);
  const ctx = {
    actions,
    storage: { get: async () => await Promise.resolve(null) },
  } as unknown as GuestCtx;
  const produced = await found.handler(ctx, asked as never);
  if (typeof produced === "object" && produced !== null && "refused" in produced) return produced;
  return found.action.result.parse(produced);
}

/**
 * What each reading door is asked for. Every door has a row: the test below refuses a roster
 * this table does not cover, so a door added later cannot quietly opt out of the fallback.
 */
const ASKED: Readonly<Partial<Record<ActionName, unknown>>> = {
  [ACTIONS.feed]: { surface: "all", window: "all" },
  [ACTIONS.record]: { id: RECORD },
  [ACTIONS.thread]: { id: RECORD },
  [ACTIONS.topics]: {},
  [ACTIONS.topic]: { topic: TOPIC_NAME },
  [ACTIONS.pulse]: {},
  [ACTIONS.runs]: {},
  [ACTIONS.run]: { id: RUN },
  [ACTIONS.policy]: {},
};

beforeEach(async () => {
  harness = await openTestStore(NOW);
  const { db } = harness;
  await insert(db, "entities", {
    id: TOPIC,
    kind: "repository",
    name: TOPIC_NAME,
    canonical_id: TOPIC,
    created_by: "operator",
    created_at: stamp(NOW - HOUR),
  });
  await insert(db, "records", {
    id: RECORD,
    kind: "proposal",
    root_id: RECORD,
    seq: 1,
    run_id: RUN,
    recipe_id: "outcome-integrity",
    recipe_version: 3,
    actor_kind: "run",
    actor_id: RUN,
    title: "a proposal a reader can open",
    created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1, title: "a proposal", problem: "p", outcome: "o" }),
  });
  await insert(db, "records", {
    id: OBSERVATION,
    kind: "observation",
    root_id: OBSERVATION,
    seq: 1,
    run_id: RUN,
    actor_kind: "run",
    actor_id: RUN,
    title: "an observation it rests on",
    created_at: stamp(NOW - HOUR),
    payload: JSON.stringify({ schema: 1, claim: "an observation", evidence: [] }),
  });
  await insert(db, "edges", {
    id: "edg_0001",
    kind: "addresses",
    from_kind: "record",
    from_id: RECORD,
    to_kind: "record",
    to_id: OBSERVATION,
    actor_kind: "run",
    actor_id: RUN,
    created_at: stamp(NOW - HOUR),
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
    id: RUN,
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
    payload: JSON.stringify({ runId: RUN, counts: { records: 1 } }),
  });
  doors = readDoors(harness.store);
});

afterEach(() => {
  harness.close();
});

test("every reading door is held to the fallback, and none escapes the table", () => {
  expect(doors.map((entry) => entry.action.name).sort()).toEqual(Object.keys(ASKED).sort());
});

test("a hub without the part answers every read exactly as one with it", async () => {
  for (const [name, args] of Object.entries(ASKED)) {
    const withPart = await dispatch(name, args, serving);
    const withoutPart = await dispatch(name, args, refusing);
    // Equal, not merely present: a door that degraded its answer when the part was refused —
    // a missing tally, a null score, an added notice — is the fallback failing, and a door that
    // raised the refusal instead would never reach this line.
    expect(withoutPart).toEqual(withPart);
  }
});
