import { afterEach, expect, test } from "bun:test";
import type { GuestActions, GuestCtx } from "@manifold/plugin-kit/server";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  RecordQuerySchema,
  SuggestionsQuerySchema,
  type Swept,
  type UnjudgedRecord,
} from "../babel/contract.ts";
import { readDoors } from "../babel/doors/read.ts";
import { suggestDoors } from "../babel/doors/suggest.ts";
import type { Door } from "../babel/doors/door.ts";
import { DEFAULT_POLICY, setPolicy } from "../babel/store/acts.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import { JevAnswers } from "../babel/jev/server/judge.ts";
import { JEV_SERVICE, type JevServices } from "../babel/jev/server/credential.ts";
import type { Screener } from "../babel/jev/screen/screener.ts";
import { basisFor, sweep, sweepPlan } from "../babel/jev/sweep/sweep.ts";

/*
  THE SWEEP AS A CONSUMER EXPERIENCES IT: over a real Babel store for the mutation boundary, and
  through the same service roster/invocation shape the host gives the part for the spending one.

  A store census rather than an implementation assertion holds the first contract: the records,
  claims and the ranked feed are serialized before and after a pass and must be BYTE-IDENTICAL.
  If a future driver "helpfully" updates a status, moves a score or reserves a claim, the observed
  documents move and this test fails.
*/

const NOW = Date.parse("2026-09-19T12:00:00.000Z");
const PRINCIPAL = "prn_jev";
const JEV = "atyrode.babel.jev";
const cleanup: TestStore[] = [];

const POLICY = {
  revision: "r7",
  pluginId: JEV,
  enabled: true,
  policySha256: "a".repeat(64),
};

const READY: InstanceServiceDescription = {
  serviceId: JEV_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "dev-01", name: "dev-01", online: true },
  configuration: POLICY,
  connected: true,
  state: "ready",
  reason: null,
};

type Answered = Extract<ServiceReply, { ok: true }>["result"];

/** A bound or absent judgement service, counting only paid-origin invocations. */
function host(options: {
  readonly bound?: boolean;
  readonly refused?: boolean;
  readonly result?: Answered;
  readonly throwOnInvoke?: boolean;
}): { readonly services: JevServices; readonly asks: string[]; invocations: number } {
  const state = {
    asks: [] as string[],
    invocations: 0,
    services: {} as JevServices,
  };
  state.services = {
    listInstances: async () => ({
      defaultOwner: null,
      services: options.bound === false ? [] : [READY],
    }),
    invokeInstance: async (args) => {
      state.invocations += 1;
      if (options.throwOnInvoke === true) throw new Error("an absent part was invoked");
      state.asks.push(String(args.input[JEV_SERVICE.stateField]));
      if (options.refused === true) {
        return {
          type: "service_result",
          requestId: `q${String(state.invocations)}`,
          ok: false,
          refusal: "service_ceiling_exceeded",
        };
      }
      return {
        type: "service_result",
        requestId: `q${String(state.invocations)}`,
        ok: true,
        result: options.result ?? { vague: 1 },
      };
    },
  };
  return state;
}

/** One voter that always proposes, so the sweep and not a threshold is under test. */
const ALWAYS: Screener = {
  id: "always",
  kinds: ["finding"],
  screen: ({ record }) => ({
    kind: "draft-issue",
    summary: `act on ${record.title}`,
    rationale: "the bounded sweep proposed it",
  }),
};

/** An in-memory reading half: exactly the gap and peel calls the sweep makes. */
function memoryActions(rows: readonly UnjudgedRecord[]): GuestActions {
  return {
    call: async ({ plugin, action, input }) => {
      if (plugin !== BABEL_PLUGIN_ID) throw new Error(`unexpected plugin ${plugin}`);
      if (action === ACTIONS.suggestions) {
        const ask = SuggestionsQuerySchema.parse(input);
        const eligible = rows.filter(
          (row) => ask.kinds.length === 0 || ask.kinds.includes(row.kind),
        );
        const offset =
          ask.after === "" ? 0 : eligible.findIndex((row) => row.recordId === ask.after) + 1;
        return {
          suggester: JEV,
          outstanding: 0,
          answered: 0,
          judged: 0,
          unjudged: eligible.length,
          pending: eligible.slice(Math.max(0, offset), Math.max(0, offset) + ask.pending),
        };
      }
      if (action === ACTIONS.record) {
        const { id } = RecordQuerySchema.parse(input);
        const row = rows.find((candidate) => candidate.recordId === id);
        if (row === undefined) throw new Error(`no record ${id}`);
        return {
          post: {
            id,
            kind: row.kind,
            surface: "desk",
            title: `record ${id}`,
            standing: "new",
            established: "unsettled",
            createdAt: new Date(NOW).toISOString(),
            author: null,
            topics: [],
            score: 0,
            support: 0,
            oppose: 0,
            unsure: 0,
            votes: [],
            contested: false,
            reviewing: false,
            comments: 0,
            awaiting: true,
            why: "",
            lastActivityAt: new Date(NOW).toISOString(),
          },
          claim: { statement: `claim ${id}`, standing: "new", act: "Rule on this" },
          case: {},
          evidence: [],
          corroboration: { supports: 0, distinctRuns: 0 },
          repository: [],
          reception: { byRole: [], contested: false, operatorHistory: [] },
          machinery: {},
          related: [],
          plan: null,
          nextActions: [],
        };
      }
      throw new Error(`unexpected action ${action}`);
    },
  };
}

/** Real baseline doors under the principal the operator allow-listed for Jev. */
function realActions(harness: TestStore): GuestActions {
  const doors: readonly Door[] = [...readDoors(harness.store), ...suggestDoors(harness.store)];
  const ctx = {
    pluginId: BABEL_PLUGIN_ID,
    principal: { id: PRINCIPAL, kind: "service", name: PRINCIPAL },
    auth: {
      principal: { id: PRINCIPAL },
      caps: ["containers:read", "containers:write"],
      containerScope: null,
    },
    emit: () => {},
  } as unknown as GuestCtx;
  return {
    call: async ({ plugin, action, input }) => {
      if (plugin !== BABEL_PLUGIN_ID) throw new Error(`unexpected plugin ${plugin}`);
      const door = doors.find((candidate) => candidate.action.name === action);
      if (door === undefined) throw new Error(`no door named ${action}`);
      const result = (await door.handler(ctx, door.action.input.parse(input) as never)) as {
        refused?: unknown;
      };
      if (typeof result.refused === "string") throw new Error(result.refused);
      return door.action.result.parse(result);
    },
  };
}

async function seedFinding(harness: TestStore, id: string, title: string): Promise<void> {
  await insert(harness.db, "records", {
    id,
    kind: "finding",
    root_id: id,
    supersedes_id: null,
    seq: 0,
    parent_id: null,
    run_id: null,
    recipe_id: null,
    recipe_version: null,
    actor_kind: "run",
    actor_id: "run_1",
    title,
    created_at: new Date(NOW).toISOString(),
    payload: JSON.stringify({ pattern: `claim ${id}`, why_it_matters: "it matters" }),
  });
}

/** The database's integer representation normalized to bytes that can be compared exactly. */
function bytesOf(value: unknown): string {
  return JSON.stringify(value, (_key, member: unknown) =>
    typeof member === "bigint" ? member.toString() : member,
  );
}

async function snapshot(harness: TestStore): Promise<{
  readonly records: string;
  readonly claims: string;
  readonly ranking: string;
}> {
  const records = await harness.db.query(`SELECT * FROM records ORDER BY rowid`);
  const claims = await harness.db.query(`SELECT * FROM claims ORDER BY rowid`);
  const ranking = await harness.store.feed({
    sort: "next",
    window: "all",
    kinds: [],
    surface: "desk",
    established: [],
    group: "none",
    limit: 25,
    offset: 0,
  });
  return {
    records: bytesOf(records),
    claims: bytesOf(claims),
    ranking: bytesOf(ranking),
  };
}

async function deliver(actions: GuestActions, result: Swept): Promise<void> {
  for (const suggestion of result.suggestions) {
    await actions.call({
      plugin: BABEL_PLUGIN_ID,
      action: ACTIONS.suggest,
      input: {
        recordId: suggestion.recordId,
        revision: suggestion.revision,
        kind: suggestion.kind,
        summary: suggestion.summary,
        rationale: suggestion.rationale,
        basis: suggestion.basis,
      },
    });
  }
}

afterEach(() => {
  for (const harness of cleanup.splice(0)) harness.close();
});

test("the plan sizes the current-bank gap before any judgement can be spent", async () => {
  const plan = await sweepPlan(
    memoryActions([
      { recordId: "fnd_00000001", revision: 0, kind: "finding", suggestible: true },
      { recordId: "fnd_00000002", revision: 0, kind: "finding", suggestible: true },
      { recordId: "obs_00000001", revision: 0, kind: "observation", suggestible: false },
    ]),
    { limit: 1, kinds: [], policyRevision: POLICY.revision },
  );
  expect(plan).toMatchObject({
    unjudged: 3,
    outstanding: 0,
    unreadable: 0,
    batch: 1,
    silent: "",
    kinds: expect.arrayContaining([
      {
        kind: "finding",
        basis: basisFor("finding", POLICY.revision),
        judged: 0,
        unjudged: 2,
      },
    ]),
  });
});

test("the continuation walks past a silent voter without becoming stored authority", async () => {
  const actions = memoryActions([
    { recordId: "obs_00000001", revision: 0, kind: "observation", suggestible: true },
    { recordId: "fnd_00000001", revision: 0, kind: "finding", suggestible: true },
  ]);
  const jev = host({ result: { vague: 1 } });
  const answers = new JevAnswers();
  const first = await sweep(
    { actions, services: jev.services, screeners: [ALWAYS], answers },
    { limit: 1, kinds: [], after: "" },
  );
  expect(first).toMatchObject({
    read: 1,
    judged: 1,
    suggestions: [],
    continuation: "observation/obs_00000001",
  });

  const second = await sweep(
    { actions, services: jev.services, screeners: [ALWAYS], answers },
    { limit: 1, kinds: [], after: first.continuation },
  );
  expect(second.suggestions.map((row) => row.recordId)).toEqual(["fnd_00000001"]);
  expect(jev.asks).toEqual(["claim obs_00000001", "claim fnd_00000001"]);
});
test("a bounded sweep touches no record, claim or ranking, and a second pass skips delivered work", async () => {
  const harness = await openTestStore(NOW);
  cleanup.push(harness);
  await setPolicy(
    harness.store,
    {
      ...DEFAULT_POLICY,
      suggesters: [{ principalId: PRINCIPAL, pluginId: JEV, note: "the judgement part" }],
    },
    "",
    "alex",
    16,
  );
  await seedFinding(harness, "fnd_00000001", "one");
  await seedFinding(harness, "fnd_00000002", "two");
  await seedFinding(harness, "fnd_00000003", "three");
  await insert(harness.db, "claims", {
    id: "clm_00000001",
    record_id: "fnd_00000003",
    role: "critic",
    lane: "review",
    policy_version: "1",
    job_id: null,
    run_id: null,
    fence: 1,
    reserved_cost: 0,
    actual_cost: null,
    granted_at: new Date(NOW - 1000).toISOString(),
    expires_at: new Date(NOW + 60_000).toISOString(),
    finished_at: null,
    outcome: null,
  });
  const actions = realActions(harness);
  const jev = host({ result: { vague: 1 } });
  const answers = new JevAnswers();
  const before = await snapshot(harness);
  const first = await sweep(
    { actions, services: jev.services, screeners: [ALWAYS], answers },
    { limit: 2, kinds: ["finding"], after: "" },
  );
  expect(first).toMatchObject({ read: 2, judged: 2, unjudged: 0, stopped: "" });
  expect(first.suggestions).toHaveLength(2);
  expect(jev.asks).toEqual(["claim fnd_00000001", "claim fnd_00000002"]);
  expect(await snapshot(harness)).toEqual(before);

  await deliver(actions, first);
  const second = await sweep(
    { actions, services: jev.services, screeners: [ALWAYS], answers },
    { limit: 2, kinds: ["finding"], after: "" },
  );
  expect(second).toMatchObject({ read: 1, judged: 1, unjudged: 0 });
  expect(second.suggestions.map((row) => row.recordId)).toEqual(["fnd_00000003"]);
  expect(jev.asks).toEqual(["claim fnd_00000001", "claim fnd_00000002", "claim fnd_00000003"]);
  expect(first.suggestions.map((row) => row.basis)).toEqual([
    basisFor("finding", POLICY.revision),
    basisFor("finding", POLICY.revision),
  ]);
});

test("without a bound Jev the pass says it did nothing and never invokes the throwing fake", async () => {
  const absent = host({ bound: false, throwOnInvoke: true });
  const result = await sweep(
    {
      actions: memoryActions([{ recordId: "fnd_00000001", revision: 0, kind: "finding", suggestible: true }]),
      services: absent.services,
      screeners: [ALWAYS],
      answers: new JevAnswers(),
    },
    { limit: 1, kinds: ["finding"], after: "" },
  );
  expect(absent.invocations).toBe(0);
  expect(result).toMatchObject({ read: 0, judged: 0, unjudged: 0, suggestions: [], positions: [] });
});

test("a pass that is out of credit stops on the first record instead of failing", async () => {
  const dry = host({ refused: true });
  const result = await sweep(
    {
      actions: memoryActions([
        { recordId: "fnd_00000001", revision: 0, kind: "finding", suggestible: true },
        { recordId: "fnd_00000002", revision: 0, kind: "finding", suggestible: true },
        { recordId: "fnd_00000003", revision: 0, kind: "finding", suggestible: true },
      ]),
      services: dry.services,
      screeners: [ALWAYS],
      answers: new JevAnswers(),
    },
    { limit: 3, kinds: ["finding"], after: "" },
  );
  expect(dry.invocations).toBe(1);
  expect(result).toMatchObject({ read: 1, judged: 0, unjudged: 1, suggestions: [] });
});
