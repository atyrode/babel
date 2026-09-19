import { afterEach, beforeEach, expect, test } from "bun:test";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  GOVERNED_CAPS,
  hasCap,
  isEngineCap,
  PluginManifestSchema,
  type AuthoredCap,
  type Cap,
  type PluginManifest,
} from "@manifold/protocol";
import { ACTIONS, BABEL_PLUGIN_ID, JEV_PLUGIN_ID, type ActionName } from "../babel/contract.ts";
import { actDoors } from "../babel/doors/acts.ts";
import type { Door } from "../babel/doors/door.ts";
import { readDoors } from "../babel/doors/read.ts";
import { stamp } from "../babel/store/feedindex.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import jevManifest from "../babel/jev/manifest.json";

/*
  THE PART CAN REACH THE BASELINE, AND THAT IS A PROPERTY OF THE PART'S OWN MANIFEST.

  `test/optional-part.test.ts` holds the other direction — the baseline answers the same with the
  part gone — and it is the property the epic rests on. This file holds the direction the part
  needs to exist at all: a call from `atyrode.babel.jev` to one of Babel's reading doors has to
  OPEN. The part declares `atyrode.babel` required, which buys the edge and the install order and
  nothing else; whether the call is admitted is a second question with a different answer, and
  until #404 the answer was no for every door in the family.

  A CROSS-PLUGIN CALL IS BOUNDED BY THE CALLER'S OWN CEILING. The host grades the CALLEE door's
  declared `caps` against what the CALLING plugin's manifest could have declared for itself —
  `granted ∩ declared`, minus the governed caps, which no flat install grant consents to — and
  refuses `caller_ceiling` for anything outside it (manifold
  `packages/server/src/plugin-host.ts:3135-3143`, the operator's ruling of 2026-09-14, ADR 0041
  §3). A plugin never does through a sibling what it could not have asked for on its own manifest.
  There is no implication between the names: `hasCap` is exact membership or the engine's `*`
  (`packages/protocol/src/capabilities.ts:126`), so `services:invoke` buys no read of anything.

  Every reading door declares `containers:read` (`babel/doors/read.ts:46`). A part declaring only
  `services:invoke` is therefore refused at nine doors out of nine before the callee is asked, and
  the part as shipped in 0.1.0 declared exactly that.

  SO THE HOST BELOW GRADES THE CEILING, and that is the whole reason it exists. Every other fake
  in this repository hands a handler a `ctx` and calls it — which is the right shape for asking
  what a door DOES, and cannot see this class of defect at all, because the refusal happens one
  layer above the door and the door is never reached. A test that asserted the manifest string
  would pass just as happily against a host that refused the call. So {@link callsFrom} walks the
  rungs in the same order the engine walks them (declared edge, enabled row, caller ceiling,
  callee's own ladder, dispatch) and raises the host's own sentence, `${class}: ${offenders}`,
  caller first — the shape `server/engine/session.ts` already reads a refusal class off.

  It is a MODEL and not the engine, so it is kept to the rungs this question turns on: the trace
  bounds (cycle, depth) and the builtin-callee refusal are not modelled, because nothing here is
  a builtin and no chain is longer than one hop.
*/

const jev: PluginManifest = PluginManifestSchema.parse(jevManifest);

/** What the part shipped with, before #404: the state this file refuses to let it return to. */
const SHIPPED_0_1_0: readonly AuthoredCap[] = ["services:invoke"];

const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);
const HOUR = 60 * 60 * 1000;
const RECORD = "pro_00000001";
const OBSERVATION = "obs_00000002";
const TOPIC = "ent_00000001";
const TOPIC_NAME = "tyrode-infra";
const RUN = "run-a";

/** The operator's own principal, which holds the read: the ceiling is not about him. */
const READER: readonly Cap[] = ["containers:read", "containers:write"];

/** `plugin-host.ts`'s own ceiling test, copied because it is not exported and it is two lines. */
function withinCeiling(cap: AuthoredCap, declared: readonly AuthoredCap[]): boolean {
  return cap === "*" ? declared.includes("*") : hasCap(declared, cap);
}

/**
 * `ctx.actions` as the host builds it for one caller: the rungs, in the engine's order, ending
 * in the callee's real handler over the real store.
 *
 * `principal` is the authority the dispatch is already serving, and it is graded at the CALLEE
 * — a second bound and a different question. Both have to pass, which is what makes the missing
 * capability invisible to anyone reasoning from "the operator can read this".
 */
function callsFrom(
  published: Readonly<Record<string, Door>>,
  enabled: readonly string[],
  caller: PluginManifest,
  principal: readonly Cap[],
): GuestCtx["actions"] {
  /*
    WHAT THE CALLER COULD HAVE DECLARED FOR ITSELF, which is `granted ∩ declared` minus the
    governed caps. An operator who consented to the manifest granted what it asked, so the
    interesting subtraction here is the governed one: `services:invoke` is version-bound consent
    per artifact revision and never a flat grant, so the host drops it from the ceiling rather
    than letting an edge carry it. The part's remaining authority is therefore the read alone.
  */
  const ceiling = caller.capabilities.filter((cap) => !GOVERNED_CAPS.includes(cap));
  return {
    call: async ({ plugin: callee, action, input }) => {
      const door = `${callee}.${action}`;
      const edge = `${caller.id} -> ${callee}`;
      const declared = caller.dependencies?.[callee];
      if (declared === undefined || declared.type === "incompatible") {
        throw new ActionCallError(`undeclared_dependency: ${edge}`);
      }
      if (!enabled.includes(callee)) throw new ActionCallError(`dependency_unavailable: ${edge}`);
      const entry = published[door];
      for (const cap of entry?.action.caps ?? []) {
        if (!isEngineCap(cap) || withinCeiling(cap, ceiling)) continue;
        throw new ActionCallError(`caller_ceiling: ${caller.id} -> ${door} (${cap})`);
      }
      // A door nobody published has no caps to check and falls through to here, which is the
      // order the host's vocabulary publishes.
      if (entry === undefined) throw new ActionCallError(`unknown_action: ${door}`);
      for (const cap of entry.action.caps) {
        if (withinCeiling(cap, principal)) continue;
        throw new ActionCallError(
          `capability: ${caller.id} -> ${door} (${cap} capability required)`,
        );
      }
      const ctx = {
        actions: { call: async () => await Promise.reject(new Error("one hop")) },
        storage: { get: async () => await Promise.resolve(null) },
      } as unknown as GuestCtx;
      const produced = await entry.handler(ctx, entry.action.input.parse(input) as never);
      if (typeof produced === "object" && produced !== null && "refused" in produced) {
        throw new ActionCallError(`refused: ${caller.id} -> ${door} (${String(produced.refused)})`);
      }
      return entry.action.result.parse(produced);
    },
  };
}

/** The refusal sentence, or a failure naming the answer that came back instead. */
async function refusalOf(
  actions: GuestCtx["actions"],
  action: string,
  input: unknown,
): Promise<string> {
  try {
    await actions.call({ plugin: BABEL_PLUGIN_ID, action, input });
  } catch (error) {
    if (error instanceof ActionCallError) return error.message;
    throw error;
  }
  throw new Error(`${BABEL_PLUGIN_ID}.${action} was not refused`);
}

/**
 * What each reading door is asked for. Every door has a row, and the first test refuses a roster
 * this table does not cover: a door added later cannot quietly skip the reach it is part of.
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

let harness: TestStore;
let doors: Readonly<Record<string, Door>>;
let reads: readonly Door[];
let writes: readonly Door[];

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
    title: "a proposal the part would judge",
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
  reads = readDoors(harness.store);
  // The acts, for the ceiling they demand and not for their behaviour: a door refused above the
  // callee is never dispatched, so the job slice below is never asked anything.
  writes = actDoors({ db, now: () => NOW, touch: () => {} }, 16, (() => ({
    describe: async () => await Promise.resolve({}),
  })) as never);
  // Keyed the way the host keys a published door: `${plugin}.${action}`.
  doors = Object.fromEntries(
    [...reads, ...writes].map((entry) => [`${BABEL_PLUGIN_ID}.${entry.action.name}`, entry]),
  );
});

afterEach(() => {
  harness.close();
});

test("every reading door is held to the reach, and none escapes the table", () => {
  expect(reads.map((entry) => entry.action.name).sort()).toEqual(Object.keys(ASKED).sort());
});

test("the part can open every one of Babel's reading doors", async () => {
  const actions = callsFrom(doors, [BABEL_PLUGIN_ID, JEV_PLUGIN_ID], jev, READER);
  for (const [action, input] of Object.entries(ASKED)) {
    // Resolved, not thrown: the ceiling admitted it, the callee's ladder admitted it, and the
    // answer came back through the door's own result schema.
    await actions.call({ plugin: BABEL_PLUGIN_ID, action, input });
  }
  // And the answer is the door's own, not an empty shape a refusal could be mistaken for.
  const peel = (await actions.call({
    plugin: BABEL_PLUGIN_ID,
    action: ACTIONS.record,
    input: { id: RECORD },
  })) as { post: { id: string; title: string } };
  expect(peel.post.id).toBe(RECORD);
  expect(peel.post.title).toBe("a proposal the part would judge");
});

test("the manifest as it shipped could open none of them, and the refusal names the capability", async () => {
  /*
    #404 ITSELF, kept executable. This is not the manifest on disk — it is the set the part
    carried at 0.1.0 — so the case survives the fix and states what the fix was for: declaring
    the dependency establishes the edge and buys no authority through it, and `services:invoke`
    implies no read of anything.
  */
  const asShipped: PluginManifest = { ...jev, capabilities: [...SHIPPED_0_1_0] };
  const actions = callsFrom(doors, [BABEL_PLUGIN_ID, JEV_PLUGIN_ID], asShipped, READER);
  for (const [action, input] of Object.entries(ASKED)) {
    expect(await refusalOf(actions, action, input)).toBe(
      `caller_ceiling: ${JEV_PLUGIN_ID} -> ${BABEL_PLUGIN_ID}.${action} (containers:read)`,
    );
  }
});

test("the ceiling is the caller's manifest, and the operator's own authority does not lift it", async () => {
  /*
    THE TWO BOUNDS ARE DIFFERENT QUESTIONS, and this is why the defect was invisible: the
    principal a judgement runs under is the operator's, and he holds `containers:read` over his
    own workspace. That buys the part nothing. Read the other way, widening the principal is
    never the repair for a `caller_ceiling`, and narrowing the manifest is never repaired by a
    root token.
  */
  const asShipped: PluginManifest = { ...jev, capabilities: [...SHIPPED_0_1_0] };
  const withRoot = callsFrom(doors, [BABEL_PLUGIN_ID, JEV_PLUGIN_ID], asShipped, ["*"]);
  expect(await refusalOf(withRoot, ACTIONS.record, { id: RECORD })).toBe(
    `caller_ceiling: ${JEV_PLUGIN_ID} -> ${BABEL_PLUGIN_ID}.${ACTIONS.record} (containers:read)`,
  );
  // And the callee's own ladder still grades the principal, with the part's ceiling unchanged.
  const withNothing = callsFrom(doors, [BABEL_PLUGIN_ID, JEV_PLUGIN_ID], jev, []);
  expect(await refusalOf(withNothing, ACTIONS.record, { id: RECORD })).toBe(
    `capability: ${JEV_PLUGIN_ID} -> ${BABEL_PLUGIN_ID}.${ACTIONS.record} (containers:read capability required)`,
  );
});

test("the part holds no write authority, and every ruling door is closed to it", async () => {
  /*
    THE PIN, AND IT IS BEHAVIOUR RATHER THAN A STRING. Whether the part may write anything at
    all is #360's decision and not a manifest edit's: Babel has two writer classes — the
    operator, authenticated through a door under his own principal, and a run, mediated, whose
    output the conductor ingests against a schema the baseline owns — and a dependent plugin is
    neither. `containers:write` is the authority every ruling door carries, so adding it here
    would settle that question in a JSON array and show the operator an authority nothing
    spends. Adding it fails this test, which is the point of it.
  */
  // Said plainly first, so widening the array fails with the sentence rather than with a
  // parse error from a ruling door that should never have been reached.
  expect(jev.capabilities).not.toContain("containers:write");
  const actions = callsFrom(doors, [BABEL_PLUGIN_ID, JEV_PLUGIN_ID], jev, READER);
  const ruling = writes.filter((entry) => entry.action.caps.includes("containers:write"));
  // The crossing and its repair ask `isRoot` instead of a capability, so they are not in this
  // set and are not the part's to call either — they are refused at the callee, as an owner's.
  expect(ruling.length).toBe(writes.length - 2);
  for (const entry of ruling) {
    expect(await refusalOf(actions, entry.action.name, {})).toBe(
      `caller_ceiling: ${JEV_PLUGIN_ID} -> ${BABEL_PLUGIN_ID}.${entry.action.name} (containers:write)`,
    );
  }
});
