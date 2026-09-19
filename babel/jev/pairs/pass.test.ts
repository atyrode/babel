import { expect, test } from "bun:test";
import type { GuestActions } from "@manifold/plugin-kit/server";
import type { InstanceServiceDescription, ServiceInput } from "@manifold/protocol";
import {
  ACTIONS,
  JEV_ACTIONS,
  PairsInputSchema,
  PairsReportSchema,
  type PairsReport,
} from "../../contract.ts";
import { JEV_SERVICE, type JevServices } from "../server/credential.ts";
import { JevAnswers } from "../server/judge.ts";
import { PAIR_HANDLERS } from "./doors.ts";
import { pairPass } from "./pass.ts";

/*
  THE PAIR DOOR AS THE RUNTIME REACHES IT, AND THE THREE THINGS IT HAS TO GET RIGHT.

  The detectors and the proposer are pure and have their own files. What only a pass can be wrong
  about is the wiring between them, and each case here is a way that wiring fails while every
  pure test still passes:

  - IT ACTUALLY ASKS, AND IT ASKS ABOUT TWO RECORDS. The per-record `judge` operation carries one
    state and projects the per-record leaves, so a pass that reused it would send half a pair and
    read `contradicts` off a projection that never names it: every pair would come back
    unanswered and the report would look like a corpus with no contradictions in it. So the
    operation, both states, and both relations off the ONE answer are asserted together.
  - NO POLICY IS NO INVOCATION. #360's standing requirement: with nothing bound the part behaves
    as though it were not installed. The invocation count is the assertion, because a pass that
    called and handled the refusal would spend on a deployment that never opted in.
  - NOTHING IS WRITTEN. The part holds no `containers:write`; a census of the doors it knocked on
    is what proves the suggestions came back rather than went in.
*/

const POLICY = {
  revision: "r7",
  pluginId: "atyrode.babel.jev",
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

/** The host, with the binding the operator either installed or did not, counting invocations. */
function host(options: { readonly bound?: boolean; readonly answer?: Record<string, number> }): {
  readonly services: JevServices;
  readonly asked: { operationId: string; input: ServiceInput }[];
} {
  const asked: { operationId: string; input: ServiceInput }[] = [];
  return {
    asked,
    services: {
      listInstances: async () => ({
        defaultOwner: null,
        services: options.bound === false ? [] : [READY],
      }),
      invokeInstance: async (args) => {
        asked.push({ operationId: args.operationId, input: args.input });
        return {
          type: "service_result",
          requestId: `q${String(asked.length)}`,
          ok: true,
          result: options.answer ?? { contradicts: 0.9, supersedes: 0.8 },
        };
      },
    },
  };
}

const CLAIMS: Readonly<Record<string, string>> = {
  fnd_00000001: "secret preflight refuses every run that would carry a plaintext key",
  fnd_00000002: "secret preflight passes runs carrying plaintext keys when the cache is warm",
};

/** The peel as the reading door serves it: the words, the instant, and the shape it requires. */
function peel(id: string, createdAt: string): unknown {
  return {
    post: {
      id,
      kind: "finding",
      surface: "desk",
      title: `about ${id}`,
      standing: "new",
      established: "unsettled",
      createdAt,
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
      awaiting: false,
      why: "",
      lastActivityAt: createdAt,
    },
    claim: { statement: CLAIMS[id] ?? "", standing: "new", act: "" },
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

/** The baseline's reading doors, answering the two this pass is allowed to knock on. */
function baseline(): { readonly actions: GuestActions; readonly knocked: string[] } {
  const knocked: string[] = [];
  return {
    knocked,
    actions: {
      call: async (args) => {
        knocked.push(args.action);
        if (args.action === ACTIONS.record) {
          const id = (args.input as { id: string }).id;
          return await Promise.resolve(
            peel(
              id,
              id === "fnd_00000001" ? "2026-09-01T10:00:00.000Z" : "2026-09-08T10:00:00.000Z",
            ),
          );
        }
        if (args.action === ACTIONS.search) {
          return await Promise.resolve({
            hits: Object.keys(CLAIMS).map((id) => ({
              id,
              title: `about ${id}`,
              via: "keyword" as const,
              score: 1,
              keyword: -1,
              meaning: null,
            })),
            coverage: {
              records: 2,
              keyworded: 2,
              embedded: 0,
              empty: 0,
              stale: 0,
              model: "",
              unnameable: 0,
            },
            meaning: "absent" as const,
            meaningAbsent: "no embedding service is installed, so this answer is by keyword alone",
            scanned: 2,
            rescored: 0,
            approximate: false,
          });
        }
        throw new Error(`the part knocked on ${args.action}`);
      },
    },
  };
}

const ANCHORS = [
  { recordId: "fnd_00000001", revision: 0, kind: "finding" as const },
  { recordId: "fnd_00000002", revision: 0, kind: "finding" as const },
];

const CUTS = { contradicts: 0.7, supersedes: 0.7 };

/** The door as the kit runs it: arguments through the action's own schema, result through it. */
async function knock(
  services: JevServices,
  actions: GuestActions,
  args: unknown,
): Promise<PairsReport> {
  const handler = PAIR_HANDLERS[JEV_ACTIONS.pairs];
  if (handler === undefined) throw new Error("no pairs handler is registered");
  const parsed = PairsInputSchema.parse(args);
  const ctx = { actions, services } as unknown as Parameters<typeof handler>[0];
  return PairsReportSchema.parse(await handler(ctx, parsed as never));
}

test("one paid call carries both claims, and both relations come off that one answer", async () => {
  const live = host({});
  const doors = baseline();
  const report = await knock(live.services, doors.actions, { anchors: ANCHORS, cuts: CUTS });

  // ONE ordered pair, ONE invocation, under the pair operation and not the per-record one.
  expect(report.candidates).toBe(1);
  expect(report.attempted).toBe(1);
  expect(report.judged).toBe(1);
  expect(live.asked).toHaveLength(1);
  expect(live.asked[0]?.operationId).toBe(JEV_SERVICE.operations.pair);
  // Both records' claims went out, in the fields the policy's literals are rendered against.
  expect(live.asked[0]?.input).toEqual({
    [JEV_SERVICE.pairFields.a]: CLAIMS.fnd_00000001 as string,
    [JEV_SERVICE.pairFields.b]: CLAIMS.fnd_00000002 as string,
  });

  // Both relations, off the one answer: the contradiction beside each record with the same
  // sentence, the supersession only beside the stale one.
  const contradiction = report.suggestions.filter((row) => row.detector === "contradiction");
  const supersession = report.suggestions.filter((row) => row.detector === "supersession");
  expect(contradiction.map((row) => row.recordId).sort()).toEqual(["fnd_00000001", "fnd_00000002"]);
  expect(contradiction.map((row) => row.subject).sort()).toEqual(["fnd_00000001", "fnd_00000002"]);
  expect(new Set(contradiction.map((row) => row.summary)).size).toBe(1);
  expect(supersession).toHaveLength(1);
  expect(supersession[0]?.recordId).toBe("fnd_00000001");
  expect(supersession[0]?.subject).toBe("fnd_00000002");
  // Every suggestion is deliverable as it stands: the door requires a counterpart that is not
  // the record itself, and the basis names the policy the answer was bought under.
  for (const row of report.suggestions) {
    expect(row.subject).not.toBe(row.recordId);
    expect(row.basis).toStartWith("pair/1/");
  }
  expect(new Set(report.suggestions.map((row) => row.basis)).size).toBe(1);

  // NOTHING WAS WRITTEN. The part knocked on the two reading doors and on nothing else.
  expect(new Set(doors.knocked)).toEqual(new Set([ACTIONS.record, ACTIONS.search]));
});

test("with no judgement service bound, nothing is invoked and nothing is read", async () => {
  const off = host({ bound: false });
  const doors = baseline();
  const report = await knock(off.services, doors.actions, { anchors: ANCHORS, cuts: CUTS });

  expect(off.asked).toHaveLength(0);
  expect(doors.knocked).toEqual([]);
  expect(report.judged).toBe(0);
  expect(report.candidates).toBe(0);
  expect(report.suggestions).toEqual([]);
  expect(report.stopped).toContain("nothing was read and nothing was spent");
});

test("a question this deployment has stated no line for is reported, and buys nothing", async () => {
  const live = host({});
  const doors = baseline();
  // One cut stated: the other detector is named as uncalibrated and the pass still runs.
  const half = await knock(live.services, doors.actions, {
    anchors: ANCHORS,
    cuts: { contradicts: 0.7 },
  });
  expect(half.uncalibrated).toEqual([{ detector: "supersession", question: "supersedes" }]);
  expect(half.suggestions.every((row) => row.detector === "contradiction")).toBe(true);
  expect(half.judged).toBe(1);

  // No cut at all: nothing is read, nothing is invoked, and it does not read as a clean corpus.
  const silent = host({});
  const unread = baseline();
  const none = await knock(silent.services, unread.actions, { anchors: ANCHORS, cuts: {} });
  expect(none.uncalibrated.map((row) => row.detector).sort()).toEqual([
    "contradiction",
    "supersession",
  ]);
  expect(silent.asked).toHaveLength(0);
  expect(unread.knocked).toEqual([]);
  expect(none.stopped).toContain("no detector has a stated cut");
});

test("a policy replaced between the pass's own read and the call is an absence, not a mislabel", async () => {
  // The revision goes on every suggestion's basis, so the pass reads it before it spends. If the
  // operator installs a new policy in between, the answer that comes back was bought under a
  // policy the mark does not name — which is exactly the mark being a lie.
  let seen = 0;
  const moving: JevServices = {
    listInstances: async () => {
      seen += 1;
      return await Promise.resolve({
        defaultOwner: null,
        services: [seen === 1 ? READY : { ...READY, configuration: { ...POLICY, revision: "r8" } }],
      });
    },
    invokeInstance: async () => {
      throw new Error("the pass invoked under a revision it had not read");
    },
  };
  const doors = baseline();
  const report = await pairPass(
    { services: moving, actions: doors.actions, answers: new JevAnswers() },
    { anchors: ANCHORS, cuts: CUTS, judgements: 8 },
  );
  expect(report.candidates).toBe(1);
  expect(report.attempted).toBe(1);
  expect(report.judged).toBe(0);
  expect(report.suggestions).toEqual([]);
  expect(report.stopped).toContain("r7");
});
