import { afterEach, expect, test } from "bun:test";
import type { SqlStatement } from "@manifold/plugin";
import { OPERATIONS } from "../contract.ts";
import { ReviewActionInputSchema } from "../machine/results.ts";
import type { ReviewPreparation } from "../server/engine/review.ts";
import { ActRefused, rule, stamp } from "./acts.ts";
import { reviewAction, reviewActionStatus } from "./review-actions.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 29, 12);
const ACTOR = { runId: "agent-run-1", agentId: "reviewer-1" };
const stores: TestStore[] = [];
afterEach(() => {
  for (const store of stores.splice(0)) store.close();
});

async function fixture(role: ReviewPreparation["role"] = "reception"): Promise<TestStore> {
  const held = await openTestStore(NOW);
  stores.push(held);
  const review: ReviewPreparation = {
    assignmentId: "claim-1",
    recordId: "pro_00000001",
    revisionId: "pro_00000001",
    rootId: "pro_00000001",
    kind: "proposal",
    role,
    lane: "blind",
    policyVersion: "policy-1",
    fence: 1,
    ordinal: 0,
    seed: "seed",
    refinementDepth: 0,
    maxRefinementDepth: 2,
    inputDigest: "digest",
    blinded: true,
    recipe: { id: "review", version: 1 },
  };
  const target = {
    id: review.recordId,
    kind: "proposal",
    payload: { problem: "The scope is unclear." },
  };
  await insert(held.db, "records", {
    id: review.recordId,
    kind: "proposal",
    root_id: review.recordId,
    seq: 0,
    run_id: "earlier-run",
    actor_kind: "run",
    actor_id: "earlier-run",
    title: "A proposal",
    created_at: stamp(NOW - 1000),
    payload: JSON.stringify(target.payload),
  });
  await insert(held.db, "claims", {
    id: review.assignmentId,
    record_id: review.recordId,
    role,
    lane: review.lane,
    policy_version: review.policyVersion,
    run_id: "cycle-1",
    fence: 1,
    granted_at: stamp(NOW),
    expires_at: stamp(NOW + 60000),
  });
  await insert(held.db, "runs", {
    id: "review-1",
    kind: OPERATIONS.evaluate,
    authority_kind: "conductor",
    authority_id: "cycle-1",
    started_at: stamp(NOW),
    preparation: JSON.stringify({ review, reviewTarget: target }),
    payload: JSON.stringify({
      claim: { id: review.assignmentId, runId: "cycle-1", fence: 1 },
      stopRequested: false,
      reviewSubmission: {
        mode: "tools",
        state: "pending",
        actions: 0,
        complete: false,
        agentRunId: ACTOR.runId,
        agentId: ACTOR.agentId,
      },
    }),
  });
  return held;
}

async function submit(held: TestStore, action: unknown) {
  return await reviewAction(held.store, ACTOR, ReviewActionInputSchema.parse(action));
}

function assessment(key = "a", result: unknown = { vote: "support" }, supersedes?: string) {
  return { kind: "assessment", key, result, ...(supersedes === undefined ? {} : { supersedes }) };
}

function refinement(key: string, supersedes?: string) {
  return {
    kind: "refinement",
    key,
    ...(supersedes === undefined ? {} : { supersedes }),
    contribution: {
      kind: "refinement",
      text: "Name the observed scope",
      target: { path: "/payload/problem" },
      would_change: `Observed on staging (${key}).`,
    },
  };
}

async function counts(held: TestStore) {
  return await held.db.query(`SELECT (SELECT count(*) FROM assessments) AS assessments,
    (SELECT count(*) FROM records) AS records,(SELECT count(*) FROM review_actions) AS actions,
    (SELECT payload FROM runs WHERE id='review-1') AS payload`);
}

test("strict invalid actions leave no rows or changed state, rather than salvaging a valid vote", async () => {
  const held = await fixture();
  const before = await counts(held);
  for (const result of [
    {
      vote: "support",
      contributions: [{ kind: "comment", text: "Elsewhere", target: { path: "/absent" } }],
    },
    {
      vote: "support",
      contributions: [
        {
          kind: "evidence",
          evidence: [{ locator: { path: "unserved", digest: "abc" }, note: "not served" }],
        },
      ],
    },
    { vote: "support", contributions: [refinement("r").contribution] },
    { outcome: "implemented" },
    { skip: "Unreadable", vote: "oppose" },
    {},
  ]) {
    await expect(submit(held, assessment("invalid", result))).rejects.toThrow();
    expect(await counts(held)).toEqual(before);
  }
  await expect(
    submit(held, { kind: "complete", key: "done", actions: ["missing"] }),
  ).rejects.toThrow(ActRefused);
  expect(await counts(held)).toEqual(before);
  await submit(held, assessment());
  expect(await held.db.query("SELECT vote FROM assessments")).toEqual([{ vote: "support" }]);
  expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
    state: "partial",
    actions: 1,
    complete: false,
  });
});

test("a lost acknowledgement and simultaneous duplicate delivery return one immutable durable receipt", async () => {
  const held = await fixture();
  const [first, duplicate] = await Promise.all([
    submit(held, assessment()),
    submit(held, assessment()),
  ]);
  expect(duplicate).toEqual(first);
  expect(await held.db.query("SELECT count(*) AS n FROM assessments")).toEqual([{ n: 1n }]);
  expect(await held.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 1n }]);
  await expect(submit(held, assessment("a", { vote: "oppose" }))).rejects.toThrow(ActRefused);
  // Interruption after the commit never removes work or marks the submission completed.
  await held.db.run("UPDATE runs SET closure='failed',finished_at=? WHERE id='review-1'", [
    stamp(NOW + 1),
  ]);
  expect(await submit(held, assessment())).toEqual(first);
  expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
    state: "partial",
    actions: 1,
    complete: false,
  });
  await expect(submit(held, refinement("later"))).rejects.toThrow(ActRefused);
});

test("fenced, expired, cancelled and foreign actors cannot append actions", async () => {
  const held = await fixture();
  const input = ReviewActionInputSchema.parse(assessment());
  await expect(reviewAction(held.store, { ...ACTOR, agentId: "other" }, input)).rejects.toThrow(
    ActRefused,
  );
  await expect(reviewAction(held.store, { ...ACTOR, runId: "other" }, input)).rejects.toThrow(
    ActRefused,
  );
  await held.db.run("UPDATE claims SET fence=2 WHERE id='claim-1'");
  await expect(submit(held, assessment())).rejects.toThrow(ActRefused);
  await held.db.run("UPDATE claims SET fence=1,expires_at=? WHERE id='claim-1'", [stamp(NOW)]);
  await expect(submit(held, assessment())).rejects.toThrow(ActRefused);
  await held.db.run("UPDATE claims SET expires_at=? WHERE id='claim-1'", [stamp(NOW + 60000)]);
  await held.db.run(
    "UPDATE runs SET payload=json_set(payload,'$.stopRequested',json('true')) WHERE id='review-1'",
  );
  await expect(submit(held, assessment())).rejects.toThrow(ActRefused);
  expect(await held.db.query("SELECT count(*) AS n FROM review_actions")).toEqual([{ n: 0n }]);
});

test("assessment corrections retain history with one active vote and require active same-kind lineage", async () => {
  const held = await fixture();
  const first = await submit(held, assessment());
  await expect(submit(held, assessment("b", { vote: "oppose" }))).rejects.toThrow(ActRefused);
  const next = await submit(held, assessment("b", { vote: "oppose" }, "a"));
  expect(next.supersedes).toBe("a");
  expect(
    await held.db.query("SELECT id,supersedes_id,vote FROM assessments ORDER BY rowid"),
  ).toEqual([
    { id: first.assessmentId!, supersedes_id: null, vote: "support" },
    { id: next.assessmentId!, supersedes_id: first.assessmentId!, vote: "oppose" },
  ]);
  await expect(submit(held, assessment("c", { vote: "unsure" }, "a"))).rejects.toThrow(ActRefused);
  await expect(submit(held, refinement("wrong-kind", "b"))).rejects.toThrow(ActRefused);
});

test("refinement corrections append proposal revisions without manufacturing assessments or rulings", async () => {
  const held = await fixture("challenge");
  const first = await submit(held, refinement("r1"));
  const next = await submit(held, refinement("r2", "r1"));
  expect(
    await held.db.query(
      "SELECT id,root_id,supersedes_id,seq FROM records WHERE run_id='review-1' ORDER BY seq",
    ),
  ).toEqual([
    { id: first.proposalId!, root_id: first.proposalId!, supersedes_id: null, seq: 0n },
    { id: next.proposalId!, root_id: first.proposalId!, supersedes_id: first.proposalId!, seq: 1n },
  ]);
  expect(
    await held.db.query(
      "SELECT (SELECT count(*) FROM assessments) AS assessments,(SELECT count(*) FROM dispositions) AS rulings",
    ),
  ).toEqual([{ assessments: 0n, rulings: 0n }]);
  expect(await held.db.query("SELECT kind,from_id,to_id FROM edges ORDER BY rowid")).toEqual([
    { kind: "refines", from_id: first.proposalId!, to_id: "pro_00000001" },
    { kind: "refines", from_id: next.proposalId!, to_id: "pro_00000001" },
  ]);
  expect(await held.db.query("SELECT records FROM runs WHERE id='review-1'")).toEqual([
    { records: 2n },
  ]);
});

test.each(["challenge", "relevance"] as const)(
  "%s refinement alone cannot complete an assignment, but an explicit skip can",
  async (role) => {
    const held = await fixture(role);
    await submit(held, refinement("r"));
    const before = await counts(held);
    await expect(submit(held, { kind: "complete", key: "done", actions: ["r"] })).rejects.toThrow(
      ActRefused,
    );
    expect(await counts(held)).toEqual(before);
    expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
      state: "partial",
      complete: false,
    });
    const skip = await submit(held, assessment("skip", { skip: "Evidence is insufficient" }));
    await submit(held, { kind: "complete", key: "done", actions: ["r", "skip"] });
    expect(await held.db.query("SELECT id,vote FROM assessments")).toEqual([
      { id: skip.assessmentId!, vote: null },
    ]);
    expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
      state: "partial",
      complete: true,
    });
  },
);

test("completion names the exact active actions once and remains partial until native settlement", async () => {
  const held = await fixture();
  await submit(held, assessment());
  await submit(held, assessment("b", { vote: "oppose" }, "a"));
  await submit(held, refinement("r"));
  for (const actions of [["a", "r"], ["b"], ["b", "r", "r"], ["b", "r", "missing"]]) {
    await expect(submit(held, { kind: "complete", key: "done", actions })).rejects.toThrow(
      ActRefused,
    );
  }
  const done = { kind: "complete", key: "done", actions: ["b", "r"] };
  const receipt = await submit(held, done);
  expect(receipt.completed).toBe(true);
  expect(await submit(held, done)).toEqual(receipt);
  expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
    state: "partial",
    actions: 3,
    complete: true,
  });
  await expect(submit(held, refinement("late"))).rejects.toThrow(ActRefused);
  await expect(submit(held, { ...done, key: "again" })).rejects.toThrow(ActRefused);
});

test("a transaction failure rolls back the rows and submission state with the absent receipt", async () => {
  const held = await fixture();
  const before = await counts(held);
  await held.db.run(
    "CREATE TRIGGER reject_action BEFORE INSERT ON review_actions BEGIN SELECT RAISE(ABORT,'synthetic receipt storage failure'); END",
  );
  await expect(submit(held, assessment())).rejects.toThrow("synthetic receipt storage failure");
  expect(await counts(held)).toEqual(before);
});

test("the payload and call ceilings refuse writes while leaving room for the completion marker", async () => {
  const held = await fixture("challenge");
  await expect(
    submit(held, {
      ...refinement("large"),
      contribution: { ...refinement("large").contribution, text: "x".repeat(32768) },
    }),
  ).rejects.toThrow(ActRefused);
  await submit(held, assessment("review", { skip: "Insufficient evidence for a judgment" }));
  for (let index = 0; index < 30; index += 1) await submit(held, refinement(`r${String(index)}`));
  await expect(submit(held, refinement("overflow"))).rejects.toThrow(ActRefused);
  const done = await submit(held, {
    kind: "complete",
    key: "done",
    actions: ["review", ...Array.from({ length: 30 }, (_, index) => `r${String(index)}`)],
  });
  expect(done.sequence).toBe(32);
  expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
    actions: 31,
    complete: true,
  });
});

test("filing uses its bound role rather than the shared superset or refinement authority", async () => {
  const held = await fixture("filing");
  await expect(submit(held, assessment())).rejects.toThrow(ActRefused);
  await expect(submit(held, refinement("r"))).rejects.toThrow(ActRefused);
  await submit(held, assessment("unfiled", { no_topic: { reason: "No existing topic fits" } }));
  expect(await held.db.query("SELECT entity_id,rationale FROM filings")).toEqual([
    { entity_id: "", rationale: "No existing topic fits" },
  ]);
});

test("filing corrections supersede the earlier link and a skip withdraws only the run's own filing", async () => {
  const held = await fixture("filing");
  await submit(
    held,
    assessment("a", { filing: { entity: "ent_00000001", rationale: "The first topic" } }),
  );
  await submit(
    held,
    assessment("b", { no_topic: { reason: "The proposed topic does not fit" } }, "a"),
  );
  await submit(held, assessment("c", { skip: "Cannot establish a topic from this material" }, "b"));
  const filings = await held.db.query<{
    id: string;
    supersedes_id: string | null;
    withdrawn: bigint;
    entity_id: string;
  }>("SELECT id,supersedes_id,withdrawn,entity_id FROM filings ORDER BY rowid");
  expect(filings.map((row) => ({ ...row, id: undefined }))).toEqual([
    { id: undefined, supersedes_id: null, withdrawn: 0n, entity_id: "ent_00000001" },
    { id: undefined, supersedes_id: filings[0]!.id, withdrawn: 0n, entity_id: "" },
    { id: undefined, supersedes_id: filings[1]!.id, withdrawn: 1n, entity_id: "" },
  ]);
  expect(await held.db.query("SELECT count(*) AS n FROM dispositions")).toEqual([{ n: 0n }]);
});

test("a correction retracting proposed work preserves its plan history but removes its actionable surface", async () => {
  const held = await fixture("backlog");
  const proposed = await submit(
    held,
    assessment("retire", { retire: { reason: "The original problem no longer exists" } }),
  );
  const proposalId = proposed.proposalId;
  if (proposalId === undefined) throw new Error("backlog produced no proposal");
  await submit(
    held,
    assessment("skip", { skip: "The evidence does not establish retirement" }, "retire"),
  );
  await expect(
    rule(held.store, { id: proposalId, ruling: "accept", note: "" }, "operator"),
  ).rejects.toThrow(ActRefused);
  const historical = await held.store.record(proposalId);
  expect(historical?.claim).toMatchObject({ standing: "superseded", act: "" });
  expect(historical?.plan?.state).toBe("superseded");
  const feed = await held.store.feed({
    sort: "new",
    window: "all",
    kinds: [],
    surface: "all",
    established: [],
    group: "none",
    limit: 25,
    offset: 0,
  });
  expect(feed.posts.map((post) => post.id)).not.toContain(proposalId);
  expect(await held.db.query("SELECT state FROM plans WHERE subject_id=?", [proposalId])).toEqual([
    { state: "open" },
  ]);
  expect(await held.db.query("SELECT count(*) AS n FROM dispositions")).toEqual([{ n: 0n }]);
  await expect(
    held.db.batch([
      {
        sql: `INSERT INTO dispositions(id,record_id,seq,disposition,actor_id,note,recorded_at)
      VALUES('late-ruling',?,1,'accept','operator','',?)`,
        params: [proposalId, stamp(NOW)],
      },
    ]),
  ).rejects.toThrow("review_proposal_superseded");
});

test("a stop between validation and commit fences every row in the real database batch", async () => {
  const held = await fixture();
  const raced = {
    now: () => held.store.now(),
    touch: () => held.store.touch(),
    db: new Proxy(held.db, {
      get(target, property, receiver) {
        if (property !== "batch") return Reflect.get(target, property, receiver);
        return async (statements: readonly SqlStatement[]) => {
          await target.run(
            "UPDATE runs SET payload=json_set(payload,'$.stopRequested',json('true')) WHERE id='review-1'",
          );
          return await target.batch(statements);
        };
      },
    }),
  };
  await expect(
    reviewAction(raced, ACTOR, ReviewActionInputSchema.parse(assessment())),
  ).rejects.toThrow(ActRefused);
  expect(
    await held.db.query(
      "SELECT (SELECT count(*) FROM assessments) AS assessments,(SELECT count(*) FROM review_actions) AS actions",
    ),
  ).toEqual([{ assessments: 0n, actions: 0n }]);
  expect(await reviewActionStatus(held.store, "review-1")).toMatchObject({
    state: "pending",
    actions: 0,
    complete: false,
  });
});
