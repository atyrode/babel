/*
  What a run runs under, and what a hardened context can actually ask for.

  Two things are worth pinning here and nothing else is. First, that the plan is DERIVED: the
  engine's path comes from the machine block's own runtime tools, the job's limits come from the
  operation's own declaration, and the per-run ceiling comes from the policy in force — so a
  manifest or a policy that changes changes the run, and a plan that disagreed with either would
  be a second set of numbers nobody edited. Second, that the slice says what the boundary cannot
  do rather than pretending: the three schedule verbs refuse by name.
*/

import { expect, test } from "bun:test";
import { PluginManifestSchema, type PluginManifest } from "@manifold/protocol";
import type { GuestSettledJobs } from "@manifold/plugin-kit/server";
import { OPERATIONS } from "../contract.ts";
import { PolicySchema } from "../store/coordinator.ts";
import type { JobLaunch } from "./conductor.ts";
import {
  DEFAULT_LIMITS,
  ENABLE_WITHOUT_JOBS,
  ENGINE_BINARY,
  SCHEDULE_UNAVAILABLE,
  jobsSlice,
  operationLimits,
  perRunUsd,
  runPlan,
  unauthorized,
} from "./plan.ts";
import manifestJson from "../manifest.json";

const HASH = "a".repeat(64);
const ARTIFACT = {
  url: "https://example.invalid/babel-machine.tar.gz",
  sha256: HASH,
  format: "tar.gz",
  entry: ["machine.js"],
  entrySha256: HASH,
  maxBytes: 4_000_000,
  maxExpandedBytes: 8_000_000,
  maxMembers: 64,
};

function operation(runtimeTools: readonly string[], timeoutMs: number): Record<string, unknown> {
  return {
    argv: [{ literal: "/job/artifact" }],
    input: { input: { type: "string", required: true, maxLength: 65536 } },
    inputFiles: { input: { input: "input" } },
    runtimeTools: [...runtimeTools],
    executable: { runtimeTool: "bun" },
    locations: [{ locationId: "outputs", access: "write" }],
    outputs: ["outputs"],
    network: "none",
    limits: { timeoutMs, memoryBytes: 1_073_741_824, processes: 32, outputBytes: 1_048_576 },
    stdin: false,
  };
}

function manifestWith(operations: Record<string, unknown>): PluginManifest {
  return PluginManifestSchema.parse({
    ...manifestJson,
    machine: {
      artifacts: { "linux-x64": ARTIFACT },
      tools: { bun: { "linux-x64": ARTIFACT }, code: { "linux-x64": ARTIFACT } },
      operations,
      locations: {
        outputs: {
          anchor: "state",
          managed: true,
          kind: "directory",
          components: ["outputs"],
          revision: "1",
        },
      },
    },
  });
}

const MANIFEST = manifestWith({
  [OPERATIONS.scan]: operation(["bun"], 600_000),
  [OPERATIONS.explore]: operation(["bun", "code"], 3_600_000),
  [OPERATIONS.evaluate]: operation(["bun", "code"], 3_600_000),
});

const POLICY = PolicySchema.parse({ enabled: true, perCycleCost: 0.25, batchSize: 4, dailyCost: 2 });

const RECIPE = { id: "reception-vote", version: 1, title: "Reception", body: "Does it hold?" };

test("the plan drives the engine the machine block binds, under the operation's own limits", () => {
  const plan = runPlan({ manifest: MANIFEST, policy: POLICY, operationId: OPERATIONS.evaluate });

  expect(plan.engine).toEqual({ binary: ENGINE_BINARY, args: [] });
  expect(ENGINE_BINARY).toBe("/runtime/bin/code");
  expect(plan.limits.timeoutMs).toBe(3_600_000);
  // The beat is a cheaper operation and its own declaration is what bounds it, not the review's.
  expect(operationLimits(MANIFEST.machine ?? null, OPERATIONS.scan).timeoutMs).toBe(600_000);
  // A job's limits are compared key by key against the operation's, so an operation this
  // manifest does not declare falls back to what a refused request would have been judged by.
  expect(operationLimits(MANIFEST.machine ?? null, OPERATIONS.archive)).toEqual(DEFAULT_LIMITS);
  expect(plan.requireContainment).toBe(true);
});

test("an operation that drives Code without requiring it is a manifest this refuses to run", () => {
  const wrong = manifestWith({
    [OPERATIONS.scan]: operation(["bun"], 600_000),
    [OPERATIONS.evaluate]: operation(["bun"], 3_600_000),
  });
  expect(() => runPlan({ manifest: wrong, policy: POLICY })).toThrow(/does not require the code/);
});

test("one run may spend one claim's reservation, which is the policy's own arithmetic", () => {
  expect(perRunUsd(POLICY)).toBe(0.0625);
  const plan = runPlan({ manifest: MANIFEST, policy: POLICY });
  expect(plan.caps.perRunUsd).toBe(0.0625);
  // A policy that batches one reserves the whole cycle for it.
  expect(perRunUsd(PolicySchema.parse({ perCycleCost: 0.25, batchSize: 1 }))).toBe(0.25);
});

test("a role whose recipe the cookbook does not hold is left with none, and is never dispatched", () => {
  const plan = runPlan({
    manifest: MANIFEST,
    policy: POLICY,
    cookbook: { [RECIPE.id]: RECIPE },
    roles: { reception: RECIPE.id, challenge: "a-recipe-nobody-wrote" },
  });
  expect(plan.recipes["reception"]).toEqual(RECIPE);
  expect(Object.hasOwn(plan.recipes, "challenge")).toBe(false);
});

test("the boundary's verbs pass through, and the three it does not serve refuse by name", async () => {
  const calls: unknown[] = [];
  const host = {
    describe: async (args: unknown) => {
      calls.push(args);
      return await Promise.resolve({ connected: true, installation: null });
    },
    execute: async (args: unknown) => {
      calls.push(args);
      return await Promise.resolve({ jobId: "j1", machineId: "m", operationId: "scan", state: "queued", result: null });
    },
    cancel: async (node: unknown) => {
      calls.push(node);
      await Promise.resolve();
    },
  } as unknown as GuestSettledJobs;
  const jobs = jobsSlice(host);

  const launch: JobLaunch = {
    jobId: "j1",
    machineId: "m",
    operationId: OPERATIONS.scan,
    input: { input: "{}" },
    outputs: [{ name: "outputs", locationId: "outputs", components: ["j1"] }],
  };
  await jobs.execute(launch);
  await jobs.cancel({ kind: "job", machineId: "m", operationId: OPERATIONS.scan, jobId: "j1" });

  // The request the host is handed owns its arrays; the loop's is frozen and stays that way.
  expect(calls[0]).toEqual({ ...launch, outputs: [{ name: "outputs", locationId: "outputs", components: ["j1"] }] });
  expect(calls[1]).toEqual({ kind: "job", machineId: "m", operationId: OPERATIONS.scan, jobId: "j1" });

  expect(await jobs.schedules()).toEqual([]);
  expect(() => jobs.schedule({ ...launch, scheduleId: "s", revision: "1", firstNominalAt: 0, intervalMs: 1, deadlineMs: 1, expiresAt: 2, offlinePolicy: "skip" })).toThrow(
    SCHEDULE_UNAVAILABLE,
  );
  expect(() => jobs.disableSchedule({ scheduleId: "s", revision: "1" })).toThrow(SCHEDULE_UNAVAILABLE);
});

test("a context with no job authority refuses every verb with the reason it has none", () => {
  const jobs = unauthorized(ENABLE_WITHOUT_JOBS);
  expect(() => jobs.describe({ machineId: "m", pluginId: "atyrode.babel" })).toThrow(/no job slice/);
  expect(() => jobs.schedules()).toThrow(/no job slice/);
});
