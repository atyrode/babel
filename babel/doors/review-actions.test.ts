import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import { PluginManifestSchema, type PluginRoster, type ServiceReply } from "@manifold/protocol";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { z } from "zod";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_PLUGIN_ID,
  JEV_ASK_CAP,
  JEV_REVIEW_CALLS,
  JEV_REVIEW_INPUT_BYTES,
  OPERATIONS,
  ReviewActionReceiptSchema,
} from "../contract.ts";
import jevManifest from "../jev/manifest.json";
import { reviewJudgment } from "../jev/review.ts";
import { JEV_SERVICE, type JevServices } from "../jev/server/credential.ts";
import { stamp } from "../store/acts.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import { reviewActionDoors } from "./review-actions.ts";
import type { Door } from "./door.ts";

const NOW = Date.UTC(2026, 8, 29, 12);
const stores: TestStore[] = [];
afterEach(() => {
  for (const held of stores.splice(0)) held.close();
});

async function fixture(assisted = false) {
  const held = await openTestStore(NOW);
  stores.push(held);
  const review = {
    assignmentId: "claim",
    recordId: "pro_00000001",
    revisionId: "pro_00000001",
    rootId: "pro_00000001",
    kind: "proposal",
    role: "reception",
    lane: "blind",
    policyVersion: "policy",
    fence: 1,
    ordinal: 0,
    seed: "seed",
    refinementDepth: 0,
    maxRefinementDepth: 2,
    inputDigest: "digest",
    blinded: true,
    recipe: { id: "review", version: 1 },
  };
  await insert(held.db, "records", {
    id: review.recordId,
    kind: "proposal",
    root_id: review.recordId,
    seq: 0,
    actor_kind: "run",
    actor_id: "earlier-run",
    run_id: "earlier-run",
    title: "A proposal",
    created_at: stamp(NOW - 1000),
    payload: "{}",
  });
  await insert(held.db, "claims", {
    id: "claim",
    record_id: review.recordId,
    role: review.role,
    lane: "blind",
    policy_version: "policy",
    run_id: "cycle",
    fence: 1,
    granted_at: stamp(NOW),
    expires_at: stamp(NOW + 60000),
  });
  await insert(held.db, "runs", {
    id: "review",
    kind: OPERATIONS.evaluate,
    authority_kind: "conductor",
    authority_id: "cycle",
    started_at: stamp(NOW),
    preparation: JSON.stringify({
      review,
      reviewTarget: { id: review.recordId, kind: "proposal", payload: {} },
    }),
    payload: JSON.stringify({
      claim: { id: "claim", runId: "cycle", fence: 1 },
      stopRequested: false,
      reviewSubmission: {
        mode: "tools",
        state: "pending",
        actions: 0,
        complete: false,
        agentRunId: "native-run",
        agentId: "agent",
        ...(assisted ? { judgments: [] } : {}),
      },
    }),
  });
  const door = reviewActionDoors(held.store)[0];
  if (door === undefined) throw new Error("missing review action door");
  return { ...held, door };
}

let serviceRevision = 0;
function judgmentHost(
  held: Pick<TestStore, "store">,
  actor = { runId: "native-run", agentId: "agent" },
) {
  const revision = `synthetic-policy-${String(++serviceRevision)}`;
  const ctx = { agentRun: actor, callerPlugin: JEV_PLUGIN_ID } as GuestCtx;
  const requests: unknown[] = [];
  const pending = {
    reply: {
      type: "service_result",
      requestId: "service-call",
      ok: true,
      result: { worth_first: 2.5, specific: 0.8, unknown_provider_field: "not published" },
    } as ServiceReply,
    invoke: async () => {},
  };
  const roster: PluginRoster = [
    {
      manifest: PluginManifestSchema.parse(jevManifest),
      enabled: true,
      source: "plugin",
      actions: [],
      install: {
        sha256: "a".repeat(64),
        source: "synthetic",
        installedBy: "operator",
        installedAt: 1,
        grantedCaps: [JEV_ASK_CAP],
      },
    },
  ];
  const services: JevServices = {
    listInstances: async () => ({
      defaultOwner: null,
      services: [
        {
          serviceId: JEV_SERVICE.serviceId,
          defaultOwner: null,
          owner: { machineId: "synthetic", name: "synthetic", online: true },
          configuration: {
            revision,
            pluginId: JEV_PLUGIN_ID,
            enabled: true,
            policySha256: "b".repeat(64),
          },
          connected: true,
          state: "ready",
          reason: null,
        },
      ],
    }),
    invokeInstance: async (args) => {
      requests.push(args);
      await pending.invoke();
      return pending.reply;
    },
  };
  const actions: GuestCtx["actions"] = {
    call: async ({ plugin, action, input }) => {
      if (plugin !== BABEL_PLUGIN_ID) throw new Error("unexpected plugin call");
      const door = reviewActionDoors(held.store).find((row) => row.action.name === action);
      if (!door) throw new Error("unknown baseline door");
      const result = await door.handler(ctx, door.action.input.parse(input) as never);
      if (typeof result === "object" && result !== null && "refused" in result)
        throw new ActionCallError(`refused: ${String(result.refused)}`);
      return door.action.result.parse(result);
    },
  };
  const host = { roster: async () => roster, enabled: async () => true };
  return {
    ctx,
    actions,
    host,
    services,
    requests,
    pending,
    roster,
    ask: async (key: string, state = "Retrieved synthetic evidence") =>
      await reviewJudgment({ ...ctx, actions, host, services }, { key, state }),
  };
}

async function finish(held: TestStore & { door: Door }) {
  const ctx = {
    agentRun: { runId: "native-run", agentId: "agent" },
    callerPlugin: null,
  } as GuestCtx;
  const dispatch = async (input: unknown) =>
    ReviewActionReceiptSchema.parse(
      await held.door.handler(ctx, held.door.action.input.parse(input) as never),
    );
  const assessment = await dispatch({
    kind: "assessment",
    key: "vote",
    result: { vote: "support" },
  });
  const completion = await dispatch({ kind: "complete", key: "done", actions: ["vote"] });
  return {
    assessment,
    completion,
    rows: await held.db.query("SELECT record_id,run_id,vote FROM assessments"),
    records: await held.db.query("SELECT id,kind FROM records ORDER BY id"),
  };
}

test("only host-authenticated Agent Run provenance can submit; root and model authority fields do not substitute", async () => {
  const held = await fixture();
  const input = held.door.action.input.parse({
    kind: "assessment",
    key: "vote",
    result: { vote: "support" },
  });
  const root = {
    agentRun: null,
    principal: { id: "root", kind: "human" },
    auth: { isRoot: true },
  } as unknown as GuestCtx;
  expect(await held.door.handler(root, input as never)).toMatchObject({
    refused: expect.stringContaining("Agent Run"),
  });
  const foreign = { ...root, agentRun: { runId: "native-run", agentId: "different" } } as GuestCtx;
  expect(await held.door.handler(foreign, input as never)).toMatchObject({
    refused: expect.stringContaining("Agent"),
  });
  expect(() =>
    held.door.action.input.parse({
      kind: "assessment",
      key: "vote",
      result: { vote: "support" },
      agentId: "agent",
      runId: "native-run",
      role: "reception",
    }),
  ).toThrow();
  expect(await held.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 0n }]);

  const trusted = { ...root, agentRun: { runId: "native-run", agentId: "agent" } } as GuestCtx;
  const receipt = held.door.action.result.parse(await held.door.handler(trusted, input as never));
  expect(receipt).toMatchObject({
    key: "vote",
    runId: "review",
    kind: "assessment",
    completed: false,
  });
  expect(await held.door.handler(trusted, input as never)).toEqual(receipt);
  expect(await held.db.query("SELECT run_id,vote FROM assessments")).toEqual([
    { run_id: "review", vote: "support" },
  ]);
});

test("typed intake refuses semantic shape errors without writes and cannot enter a pinned text run", async () => {
  const held = await fixture();
  const ctx = { agentRun: { runId: "native-run", agentId: "agent" } } as GuestCtx;
  const invalid = held.door.action.input.parse({
    kind: "assessment",
    key: "bad",
    result: { vote: "support", skip: "Did not read" },
  });
  expect(await held.door.handler(ctx, invalid as never)).toMatchObject({
    refused: expect.any(String),
  });
  expect(await held.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 0n }]);
  await held.db.run(
    "UPDATE runs SET payload=json_set(payload,'$.reviewSubmission',json(?)) WHERE id='review'",
    [JSON.stringify({ mode: "text", state: "pending", actions: 0 })],
  );
  const input = held.door.action.input.parse({
    kind: "assessment",
    key: "vote",
    result: { vote: "support" },
  });
  expect(await held.door.handler(ctx, input as never)).toMatchObject({
    refused: expect.stringContaining("assignment"),
  });
  expect(await held.db.query("SELECT count(*) AS n FROM assessments")).toEqual([{ n: 0n }]);
});

test("the governed tool parameters fit the host declaration ceiling", async () => {
  const held = await fixture();
  const schema = z.toJSONSchema(held.door.action.input, { io: "input" });
  expect(new TextEncoder().encode(JSON.stringify(schema)).byteLength).toBeLessThanOrEqual(16384);
});

test("Jev grades assembled state with trusted lineage, and assisted assessments use the ordinary durable intake", async () => {
  const ordinary = await fixture();
  const assisted = await fixture(true);
  const f = judgmentHost(assisted);
  const state = "The run retrieved synthetic evidence, including contrary observations.";
  expect(await f.ask("evidence-1", state)).toMatchObject({
    status: "judged",
    key: "evidence-1",
    agentRunId: "native-run",
    agentId: "agent",
    runId: "review",
    recordId: "pro_00000001",
    role: "reception",
    stage: "review",
    assignmentId: "claim",
    fence: 1,
    stateDigest: createHash("sha256").update(state).digest("hex"),
    requestKey: expect.stringMatching(/^[a-f0-9]{64}$/),
    answers: [
      { question: "worth_first", answer: 2.5 },
      { question: "specific", answer: 0.8 },
    ],
  });
  expect(f.requests).toEqual([
    {
      serviceId: JEV_SERVICE.serviceId,
      expectedRevision: expect.any(String),
      operationId: JEV_SERVICE.operations.judge,
      input: { state },
    },
  ]);
  expect(await assisted.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 0n }]);
  expect(
    await f.actions
      .call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.reviewAction,
        input: { kind: "assessment", key: "from-jev", result: { vote: "support" } },
      })
      .then(
        () => "accepted",
        () => "refused",
      ),
  ).toBe("refused");
  const invalid = assisted.door.action.input.parse({
    kind: "assessment",
    key: "bad",
    result: { vote: "support", skip: "not read" },
  });
  expect(
    await assisted.door.handler({ ...f.ctx, callerPlugin: null }, invalid as never),
  ).toMatchObject({ refused: expect.any(String) });
  expect(await finish(assisted)).toEqual(await finish(ordinary));
});

test.each(["absent", "disabled", "ungranted", "unavailable", "unfunded", "unreadable"] as const)(
  "optional %s yields no judgment and exactly the ordinary durable review result",
  async (state) => {
    const ordinary = await fixture();
    const assisted = await fixture(state !== "ungranted");
    const f = judgmentHost(assisted);
    if (state === "absent") f.roster.splice(0);
    if (state === "disabled") f.roster[0]!.enabled = false;
    if (state === "unavailable")
      f.services.listInstances = async () => ({ defaultOwner: null, services: [] });
    if (state === "unfunded")
      f.pending.reply = {
        type: "service_result",
        requestId: "service-call",
        ok: false,
        refusal: "service_upstream_refused",
      };
    if (state === "unreadable")
      f.pending.reply = {
        type: "service_result",
        requestId: "service-call",
        ok: true,
        result: { worth_first: { score: 3 }, unknown: "not a bank answer" },
      };
    expect(await f.ask("optional")).toEqual({ key: "optional", status: "absent" });
    expect(f.requests.length).toBe(state === "unfunded" || state === "unreadable" ? 1 : 0);
    expect(await finish(assisted)).toEqual(await finish(ordinary));
  },
);

test("input bytes, forged authority, replay keys and attempt limits cannot buy unbounded judgments", async () => {
  const held = await fixture(true);
  const f = judgmentHost(held);
  await expect(
    reviewJudgment(
      { ...f.ctx, ...f },
      {
        key: "forged",
        state: "evidence",
        agentId: "other",
        runId: "other",
        role: "evidence",
      },
    ),
  ).rejects.toThrow();
  expect(await f.ask("too-big", "é".repeat(JEV_REVIEW_INPUT_BYTES / 2))).toEqual({
    key: "too-big",
    status: "absent",
  });
  expect(
    await reviewJudgment({ ...f, agentRun: null }, { key: "root", state: "evidence" }),
  ).toEqual({ key: "root", status: "absent" });
  const foreign = judgmentHost(held, { runId: "native-run", agentId: "foreign" });
  expect(await foreign.ask("foreign", "evidence")).toEqual({ key: "foreign", status: "absent" });
  expect(f.requests).toEqual([]);
  for (let call = 0; call < JEV_REVIEW_CALLS; call += 1) {
    const key = `try-${String(call)}`;
    expect(await f.ask(key, `unique state ${String(call)}`)).toMatchObject({ status: "judged" });
    expect(await f.ask(key, "different state under reused key")).toEqual({ key, status: "absent" });
  }
  expect(await f.ask("excess", "new state")).toEqual({ key: "excess", status: "absent" });
  expect(f.requests).toHaveLength(JEV_REVIEW_CALLS);
  expect((await finish(held)).completion.completed).toBe(true);
});

test.each([
  "serviceId",
  "machineId",
  "operationId",
  "expectedRevision",
  "policy",
  "target",
] as const)("model-supplied %s cannot redirect the reviewed Jev service", async (field) => {
  const held = await fixture(true);
  const f = judgmentHost(held);
  await expect(
    reviewJudgment(
      { ...f.ctx, ...f },
      {
        key: "redirect",
        state: "evidence",
        [field]: "model-selected",
      },
    ),
  ).rejects.toThrow();
  expect(f.requests).toEqual([]);
  expect(await f.ask("ordinary")).toMatchObject({ status: "judged" });
});

test.each(["stop", "fence", "expired", "complete", "policy", "provider"] as const)(
  "an in-flight judgment is discarded after %s without late review writes",
  async (transition) => {
    const held = await fixture(true);
    const f = judgmentHost(held);
    f.pending.invoke = async () => {
      if (transition === "stop")
        await held.db.run(
          "UPDATE runs SET payload=json_set(payload,'$.stopRequested',json('true')) WHERE id='review'",
        );
      if (transition === "fence") await held.db.run("UPDATE claims SET fence=2 WHERE id='claim'");
      if (transition === "expired") held.at(NOW + 60001);
      if (transition === "complete") await finish(held);
      if (transition === "policy")
        f.services.listInstances = async () => ({ defaultOwner: null, services: [] });
      if (transition === "provider") f.roster[0]!.enabled = false;
    };
    expect(await f.ask("pending")).toEqual({ key: "pending", status: "absent" });
    expect(f.requests).toHaveLength(1);
    if (transition !== "complete")
      expect(await held.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 0n }]);
    if (["stop", "fence", "expired"].includes(transition)) {
      expect(await f.ask("late", "new evidence")).toEqual({ key: "late", status: "absent" });
      expect(f.requests).toHaveLength(1);
      expect(
        await held.door.handler(
          { ...f.ctx, callerPlugin: null },
          held.door.action.input.parse({
            kind: "assessment",
            key: "late",
            result: { vote: "support" },
          }) as never,
        ),
      ).toMatchObject({ refused: expect.any(String) });
    }
  },
);

test("the encoded input boundary is inclusive and unreadable oversized answers disclose no judgment", async () => {
  const held = await fixture(true);
  const f = judgmentHost(held);
  const overhead = new TextEncoder().encode(JSON.stringify({ key: "limit", state: "" })).byteLength;
  const state = "x".repeat(JEV_REVIEW_INPUT_BYTES - overhead);
  expect(await f.ask("limit", state)).toMatchObject({ status: "judged" });
  f.pending.reply = {
    type: "service_result",
    requestId: "service-call",
    ok: true,
    result: { worth_first: "x".repeat(129) },
  };
  expect(await f.ask("oversized", "different evidence")).toEqual({
    key: "oversized",
    status: "absent",
  });
  expect(f.requests).toHaveLength(2);
});

test("refused invocations retain their attempt fence instead of becoming repeatable spend", async () => {
  const held = await fixture(true);
  const f = judgmentHost(held);
  f.pending.invoke = async () => {
    throw new Error("synthetic connection lost after invocation");
  };
  for (let attempt = 0; attempt < JEV_REVIEW_CALLS; attempt += 1) {
    const key = `refused-${String(attempt)}`;
    expect(await f.ask(key)).toEqual({ key, status: "absent" });
  }
  const restarted = judgmentHost(held);
  expect(await restarted.ask("refused-0")).toEqual({ key: "refused-0", status: "absent" });
  expect(await restarted.ask("next")).toEqual({ key: "next", status: "absent" });
  expect(restarted.requests).toEqual([]);
  expect(f.requests).toHaveLength(JEV_REVIEW_CALLS);
  expect((await finish(held)).completion.completed).toBe(true);
});
