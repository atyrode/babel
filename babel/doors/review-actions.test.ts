import { afterEach, expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { z } from "zod";
import { OPERATIONS } from "../contract.ts";
import { stamp } from "../store/acts.ts";
import { insert, openTestStore, type TestStore } from "../store/testdb.ts";
import { reviewActionDoors } from "./review-actions.ts";

const NOW = Date.UTC(2026, 8, 29, 12);
const stores: TestStore[] = [];
afterEach(() => {
  for (const held of stores.splice(0)) held.close();
});

async function fixture() {
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
      },
    }),
  });
  const door = reviewActionDoors(held.store)[0];
  if (door === undefined) throw new Error("missing review action door");
  return { ...held, door };
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
