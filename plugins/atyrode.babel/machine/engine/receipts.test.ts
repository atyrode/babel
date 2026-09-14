/*
  THE RECEIPT, held to the two questions a drain is answered by: whose window did this run spend,
  and which model did it ask for (#267, #279).

  They are asserted on a REFUSED run as much as on a completed one, because that is the case
  2026-09-13 had none of: every run of an hour failed with no account, no model and no named cause
  anywhere in its record, so nothing could be summed and nothing could be acted on. A receipt that
  carried the pair only when the run succeeded would be a drain nobody can measure.

  The outcomes here are built as plain objects rather than by running the fixture: this is the
  assembly, and a test that launched an engine to reach it would be proving the engine again.
*/

import { expect, test } from "bun:test";
import type { EngineOutcome } from "./client.ts";
import { buildReceipt } from "./receipts.ts";
import { ENGINE_FAILURES, LAUNCH_FAILURES, LAUNCH_REPORT_SCHEMA, type LaunchReport } from "./launch.ts";

const SESSION = {
  model: "anthropic/claude-sonnet-5",
  thinking: "high" as const,
  account: { provider: "anthropic", identityKey: "victorballu@gmail.com" },
};

const STARTED = new Date("2026-09-14T10:00:00.000Z");
const FINISHED = new Date("2026-09-14T10:04:00.000Z");

/** Babel's own launch report, as `client.ts` writes it before the handshake. */
function report(over: Partial<LaunchReport> = {}): LaunchReport {
  return {
    schema: LAUNCH_REPORT_SCHEMA,
    engine: { name: "omp", version: "18.1.14" },
    session: SESSION,
    containment: {
      backend: "manifold-job",
      filesystem_isolation: true,
      network_default_deny: false,
      resource_ceilings: true,
      disposable: true,
      escape: "the machine owner's job sandbox",
    },
    models: [],
    failure: "",
    reason: "",
    retries: 0,
    finished: false,
    ...over,
  };
}

/** One supervised job's outcome, in the shape `runEngineJob` returns. */
function outcome(over: Partial<EngineOutcome> = {}): EngineOutcome {
  return {
    closure: "completed",
    failure: null,
    report: report(),
    reportUnknown: [],
    finished: report({ finished: true, exit_code: 0, models: [SESSION.model] }),
    result: { answer: "ok" },
    submissions: 1,
    prompted: true,
    usage: {
      inputTokens: 1200,
      outputTokens: 340,
      reasoningTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      totalTokens: 1540,
      costUsd: 0.0123,
      toolCalls: 1,
      messages: 1,
    },
    models: [SESSION.model],
    tools: [{ index: 0, tool: "babel_submit_result", allowed: true, reason: "", argumentBytes: 17 }],
    progress: [],
    unknownFrames: [],
    stderrTail: "",
    exitCode: 0,
    ...over,
  };
}

function receiptOf(jobs: readonly EngineOutcome[], over: Partial<Parameters<typeof buildReceipt>[0]> = {}) {
  return buildReceipt({
    runId: "run_receipt_test",
    kind: "explore",
    machineId: "dev-01",
    startedAt: STARTED,
    finishedAt: FINISHED,
    closure: "completed",
    counts: { records: 3 },
    jobs,
    ...over,
  });
}

test("a receipt names the account the run spent and the model it asked for", () => {
  const receipt = receiptOf([outcome()]);

  // The flat pair, which is what a drain sums and what the run row shows.
  expect(receipt.account).toEqual({ provider: "anthropic", identityKey: "victorballu@gmail.com" });
  expect(receipt.model).toBe(SESSION.model);
  // And the same two facts in the launcher's own words, beside the boundary it observed.
  expect(receipt.profile).toMatchObject({
    schema: LAUNCH_REPORT_SCHEMA,
    engine: "omp@18.1.14",
    model: SESSION.model,
    thinking: "high",
    provider: "anthropic",
    account: "victorballu@gmail.com",
    containment: "manifold-job",
    exitCode: 0,
    retries: 0,
  });
  expect(receipt.tokens).toBe(1540);
  expect(receipt.costUsd).toBeCloseTo(0.0123, 6);
});

test("a run refused before its prompt still names the account, the model and the cause", () => {
  // #277's whole subject. Nothing was spent, nothing answered, and the record still has to say
  // which account the operator was about to spend and why it did not happen.
  const refused = outcome({
    closure: "failed",
    failure: {
      code: ENGINE_FAILURES.handshake,
      message: "broker_unavailable: the account snapshot is unavailable",
      origin: "engine",
      named: LAUNCH_FAILURES.brokerUnavailable,
    },
    finished: report({
      finished: true,
      exit_code: 1,
      failure: LAUNCH_FAILURES.brokerUnavailable,
      reason: "engine: the account snapshot is unavailable",
    }),
    result: null,
    submissions: 0,
    prompted: false,
    usage: null,
    models: [],
    tools: [],
  });

  const receipt = receiptOf([refused], { closure: "failed" });

  expect(receipt.account).toEqual({ provider: "anthropic", identityKey: "victorballu@gmail.com" });
  expect(receipt.model).toBe(SESSION.model);
  expect(receipt.profile).toMatchObject({
    failure: LAUNCH_FAILURES.brokerUnavailable,
    failureReason: "engine: the account snapshot is unavailable",
    exitCode: 1,
  });
  // A failed run with no reason is a receipt nobody can act on; the job's own code is it.
  expect(receipt.reason).toBe(`${ENGINE_FAILURES.handshake}: broker_unavailable: the account snapshot is unavailable`);
  expect(receipt.costUsd).toBeUndefined();
  expect(receipt.tokens).toBeUndefined();
});

test("a run that launched nothing claims no account and no model", () => {
  // `scan`, `archive` and `prepare` reach no model. An empty account here would put a run in a
  // drain's sum that spent nothing of it.
  const receipt = receiptOf([]);

  expect(receipt.account).toBeUndefined();
  expect(receipt.model).toBeUndefined();
  expect(receipt.profile).toBeUndefined();
  expect(receipt.models).toBeUndefined();
  expect(receipt.counts["jobs"]).toBe(0);
});

test("the models that answered are every stage's, deduplicated in the order first heard", () => {
  // A multi-stage run's receipt is one record over three jobs: a fallback in the second stage is
  // a model that answered, and the model the run was launched under is not evidence that it did.
  const receipt = receiptOf([
    outcome({ models: [SESSION.model] }),
    outcome({ models: [SESSION.model, "anthropic/claude-opus-5"] }),
    outcome({ models: ["anthropic/claude-opus-5"] }),
  ]);

  expect(receipt.models).toEqual([SESSION.model, "anthropic/claude-opus-5"]);
  expect(receipt.tokens).toBe(3 * 1540);
  expect(receipt.counts["jobs"]).toBe(3);
  expect(receipt.counts["submissions"]).toBe(3);
  expect(receipt.counts["babel_submit_result.served"]).toBe(3);
});
