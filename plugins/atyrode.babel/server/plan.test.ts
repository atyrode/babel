/*
  What a run runs under, and what a hardened context can actually ask for.

  Two things are worth pinning here and nothing else is. First, that the plan is DERIVED: the
  engine's path comes from the machine block's own runtime tools, the job's limits come from the
  operation's own declaration, and the per-run ceiling comes from the policy in force — so a
  manifest or a policy that changes changes the run, and a plan that disagreed with either would
  be a second set of numbers nobody edited. Second, that the slice is a NARROWING and not a
  translation: all eight job verbs cross the boundary now (#534), the schedule verbs among them,
  and the one slice a hook is served none of refuses with the reason it has none.
*/

import { expect, test } from "bun:test";
import { JobLimitsSchema, PluginManifestSchema, type PluginManifest } from "@manifold/protocol";
import type { GuestCtx, GuestHookJobs, GuestJobs } from "@manifold/plugin-kit/server";
import { INFERENCE_SERVICE, OPERATIONS } from "../contract.ts";
import { DEFAULT_POLICY, PolicySchema } from "../store/coordinator.ts";
import type { JobLaunch } from "./conductor.ts";
import {
  DEFAULT_LIMITS,
  ENABLE_WITHOUT_JOBS,
  ENGINE_BINARY,
  ENGINE_TOOL,
  HOOK_WITHOUT_MACHINES,
  inferenceCeiling,
  jobCeiling,
  jobsSlice,
  machinesSlice,
  operationLimits,
  perRunUsd,
  runPlan,
  unaskable,
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

function operation(
  runtimeTools: readonly string[],
  timeoutMs: number,
  concurrentJobs?: number,
): Record<string, unknown> {
  return {
    argv: [{ literal: "/job/artifact" }],
    input: { input: { type: "string", required: true, maxLength: 65536 } },
    inputFiles: { input: { input: "input" } },
    runtimeTools: [...runtimeTools],
    executable: { runtimeTool: "bun" },
    locations: [{ locationId: "outputs", access: "write" }],
    outputs: ["outputs"],
    network: "none",
    limits: {
      timeoutMs,
      memoryBytes: 1_073_741_824,
      processes: 32,
      outputBytes: 1_048_576,
      ...(concurrentJobs === undefined ? {} : { concurrentJobs }),
    },
    stdin: false,
  };
}

function manifestWith(operations: Record<string, unknown>): PluginManifest {
  return PluginManifestSchema.parse({
    ...manifestJson,
    machine: {
      artifacts: { "linux-x64": ARTIFACT },
      tools: { bun: { "linux-x64": ARTIFACT }, [ENGINE_TOOL]: { "linux-x64": ARTIFACT } },
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
  [OPERATIONS.explore]: operation(["bun", ENGINE_TOOL], 3_600_000),
  [OPERATIONS.evaluate]: operation(["bun", ENGINE_TOOL], 3_600_000),
});

const POLICY = PolicySchema.parse({ enabled: true, perCycleCost: 0.25, batchSize: 4, dailyCost: 2 });

const RECIPE = { id: "reception-vote", version: 1, title: "Reception", body: "Does it hold?" };

test("the plan drives the engine the machine block binds, under the operation's own limits", () => {
  const plan = runPlan({ manifest: MANIFEST, policy: POLICY, operationId: OPERATIONS.evaluate });

  expect(plan.engine).toEqual({ binary: ENGINE_BINARY, args: [] });
  expect(ENGINE_BINARY).toBe("/runtime/bin/omp");
  expect(plan.limits.timeoutMs).toBe(3_600_000);
  // The beat is a cheaper operation and its own declaration is what bounds it, not the review's.
  expect(operationLimits(MANIFEST.machine ?? null, OPERATIONS.scan).timeoutMs).toBe(600_000);
  // A job's limits are compared key by key against the operation's, so an operation this
  // manifest does not declare falls back to what a refused request would have been judged by.
  expect(operationLimits(MANIFEST.machine ?? null, OPERATIONS.archive)).toEqual(DEFAULT_LIMITS);
  expect(plan.requireContainment).toBe(true);
});

test("the ceiling a bound is judged against is the manifest's, and it never rides in a request", () => {
  // What one machine will actually run at once. The coordinator governs inside this number and
  // the acts that write a bound refuse above it (#281), so the manifest and the governor cannot
  // disagree — every draw past it would be a posting the hub refuses at a reservation's cost.
  const shipped = PluginManifestSchema.parse(manifestJson);
  expect(jobCeiling(shipped)).toBe(16);

  // One number governs both lanes, so it is the LOWER of the two: a bound honoured by explore
  // and refused by evaluate is not a bound.
  const mixed = manifestWith({
    [OPERATIONS.scan]: operation(["bun"], 600_000),
    [OPERATIONS.explore]: operation(["bun", ENGINE_TOOL], 3_600_000, 16),
    [OPERATIONS.evaluate]: operation(["bun", ENGINE_TOOL], 3_600_000, 4),
  });
  expect(jobCeiling(mixed)).toBe(4);
  // A manifest declaring no ceiling runs no fan either; the batch a policy is written with
  // stands in rather than an invented one.
  expect(jobCeiling(MANIFEST)).toBe(DEFAULT_POLICY.batchSize);

  // AND THE PLAN CARRIES THE JOB'S HALF ONLY. `JobExecuteArgsSchema.limits` is strict, so a
  // `concurrentJobs` key in a request is an unrecognised key: every posting would be refused
  // for the declaration it was supposed to run under. `inference` is the other way round — it
  // is a request's to carry and the owner's to enforce (#279), and it is here.
  const plan = runPlan({ manifest: shipped, policy: POLICY, operationId: OPERATIONS.evaluate });
  expect(Object.keys(plan.limits).toSorted()).toEqual([
    "inference",
    "memoryBytes",
    "outputBytes",
    "processes",
    "timeoutMs",
  ]);
  expect(JobLimitsSchema.safeParse(plan.limits).success).toBe(true);
});

test("the run the operator's allowance pays for carries its ceiling in micro-dollars, and only where a model answers", () => {
  /*
    ADR 0038's whole enforcement point, and the one number that leaves the hub. The operator
    states an allowance in dollars; the OWNER refuses the call that would pass it, in integer
    micro-dollars, so the conversion is the contract: a plan that rounded down would tighten a
    ceiling the operator was promised, and one that emitted a zero for an exhausted allowance
    would refuse the first call instead of saying nothing was allowed.
  */
  const shipped = PluginManifestSchema.parse(manifestJson);
  // The two operations that bind the inference service are the two that get a ceiling: 0.25 / 4
  // = $0.0625 a run, which is 62,500 micro-dollars.
  for (const operationId of [OPERATIONS.explore, OPERATIONS.evaluate]) {
    const metered = runPlan({ manifest: shipped, policy: POLICY, operationId });
    expect(metered.limits.inference).toEqual({ costMicros: 62_500 });
    // The binding the ceiling is enforced against is the manifest's own, at the revision this
    // build ships: the plan never names a service, so a drift here is a job the owner refuses.
    const declared = shipped.machine?.operations[operationId]?.services ?? [];
    expect(declared).toEqual([
      {
        serviceId: INFERENCE_SERVICE.serviceId,
        revision: INFERENCE_SERVICE.revision,
        operationIds: [...INFERENCE_SERVICE.operationIds],
      },
    ]);
  }
  // A ceiling on an operation that reaches no model would be a bound on calls it cannot make.
  for (const operationId of [OPERATIONS.scan, OPERATIONS.prepare, OPERATIONS.archive]) {
    expect(runPlan({ manifest: shipped, policy: POLICY, operationId }).limits.inference).toBeUndefined();
  }
  // Rounding is UP, and an exhausted allowance yields NO ceiling rather than a zero one.
  expect(inferenceCeiling(0.000_000_4)).toBe(1);
  expect(inferenceCeiling(0)).toBeNull();
  expect(inferenceCeiling(-1)).toBeNull();
});

test("the plan says which operations the owner may meter, out of the manifest's own bindings", () => {
  const bound = manifestWith({
    [OPERATIONS.scan]: operation(["bun"], 600_000),
    [OPERATIONS.explore]: operation(["bun", ENGINE_TOOL], 3_600_000),
    [OPERATIONS.evaluate]: {
      ...operation(["bun", ENGINE_TOOL], 3_600_000),
      services: [
        { serviceId: INFERENCE_SERVICE.serviceId, revision: INFERENCE_SERVICE.revision, operationIds: ["stream"] },
      ],
    },
  });
  expect(runPlan({ manifest: bound, policy: POLICY }).metered).toEqual({
    [OPERATIONS.scan]: false,
    [OPERATIONS.explore]: false,
    [OPERATIONS.evaluate]: true,
  });

  // What this repository actually ships: both model-driving lanes bind the inference service,
  // so the owner CAN meter them — whether it does is its installed policy's to say, which no
  // server half can read, and a call the hub already counted is the fold's other proof (#256).
  const shipped = runPlan({
    manifest: PluginManifestSchema.parse(manifestJson),
    policy: POLICY,
  }).metered;
  expect(shipped[OPERATIONS.explore]).toBe(true);
  expect(shipped[OPERATIONS.evaluate]).toBe(true);
  expect(shipped[OPERATIONS.archive]).toBe(true);
  expect(shipped[OPERATIONS.scan]).toBe(false);
  expect(shipped[OPERATIONS.prepare]).toBe(false);
});

test("an operation that drives the engine without requiring it is a manifest this refuses to run", () => {
  const wrong = manifestWith({
    [OPERATIONS.scan]: operation(["bun"], 600_000),
    [OPERATIONS.evaluate]: operation(["bun"], 3_600_000),
  });
  expect(() => runPlan({ manifest: wrong, policy: POLICY })).toThrow(/does not require the omp/);
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

test("every verb the boundary serves passes straight through, arrays and all", async () => {
  const calls: unknown[] = [];
  /** The outputs array the host was handed, to prove it is a copy rather than the loop's own. */
  let handed: readonly unknown[] = [];
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
    schedule: async (args: { outputs: readonly unknown[] }) => {
      handed = args.outputs;
      calls.push(args);
      return await Promise.resolve({});
    },
    schedules: async () =>
      await Promise.resolve([
        {
          scheduleId: "atyrode.babel.conductor",
          revision: "pol_1",
          machineId: "m",
          pluginId: "atyrode.babel",
          operationId: OPERATIONS.scan,
          installationRevision: "rev-7",
          artifactSha256: "a".repeat(64),
          firstNominalAt: 10,
          intervalMs: 900_000,
          deadlineMs: 900_000,
          expiresAt: 2_000_000,
          offlinePolicy: "coalesce-one",
        },
      ]),
    disableSchedule: async (args: unknown) => {
      calls.push(args);
      return await Promise.resolve({});
    },
  } as unknown as GuestHookJobs;
  const jobs = jobsSlice(host);

  const launch: JobLaunch = {
    jobId: "j1",
    machineId: "m",
    operationId: OPERATIONS.scan,
    input: { input: "{}" },
    outputs: [{ name: "outputs", locationId: "outputs", components: ["j1"] }],
  };
  const timing = {
    scheduleId: "atyrode.babel.conductor",
    revision: "pol_1",
    firstNominalAt: 10,
    intervalMs: 900_000,
    deadlineMs: 900_000,
    expiresAt: 2_000_000,
    offlinePolicy: "coalesce-one",
  } as const;
  await jobs.execute(launch);
  await jobs.cancel({ kind: "job", machineId: "m", operationId: OPERATIONS.scan, jobId: "j1" });
  await jobs.schedule({ ...launch, ...timing });
  await jobs.disableSchedule({ scheduleId: timing.scheduleId, revision: "pol_1" });

  // The request the host is handed owns its arrays; the loop's is frozen and stays that way.
  expect(calls[0]).toEqual({ ...launch, outputs: [{ name: "outputs", locationId: "outputs", components: ["j1"] }] });
  expect(calls[1]).toEqual({ kind: "job", machineId: "m", operationId: OPERATIONS.scan, jobId: "j1" });
  // A cadence is one request plus its timing, and the copy is made for it too: a beat this
  // plugin registers for itself is what closed the hole the loop used to record a refusal for.
  expect(calls[2]).toEqual({ ...launch, ...timing, outputs: [{ name: "outputs", locationId: "outputs", components: ["j1"] }] });
  expect(handed[0]).not.toBe(launch.outputs[0]);
  expect(calls[3]).toEqual({ scheduleId: timing.scheduleId, revision: "pol_1" });

  // What the host lists is a schedule row with the plugin id and the pinned artifact still on
  // it: more than the loop reads, and read as the loop's own shape without a translation.
  const listed = await jobs.schedules();
  expect(listed).toMatchObject([
    { scheduleId: "atyrode.babel.conductor", revision: "pol_1", machineId: "m", intervalMs: 900_000 },
  ]);
});

test("only a dispatch's slice carries follow, and it hands back the snapshot and its close", async () => {
  const host = {} as unknown as GuestHookJobs;
  const node = { kind: "job" as const, machineId: "m", operationId: OPERATIONS.evaluate, jobId: "j1" };
  const snapshot = { events: [], firstSeq: null, unavailable: null };
  const asked: unknown[] = [];
  let closes = 0;
  const served = async (followed: unknown): Promise<unknown> => {
    asked.push(followed);
    return await Promise.resolve({
      snapshot,
      close: async () => {
        closes += 1;
        await Promise.resolve();
      },
    });
  };

  // A HOOK'S. `GuestHookJobs` has no live subscription to pass, so the slice has no member —
  // not a member that refuses — and the loop reads that as "this cycle cannot see a running
  // job" and folds nothing.
  expect(jobsSlice(host).follow).toBeUndefined();

  // A DISPATCH'S. What the hub answered is handed back whole, and closing the subscription is
  // the caller's: the fold takes the snapshot and closes in the same turn.
  const dispatch = jobsSlice(host, served as unknown as GuestJobs["follow"]);
  const read = await dispatch.follow?.(node, () => {});
  expect(read?.snapshot).toBe(snapshot);
  await read?.close();
  expect(asked).toEqual([node]);
  expect(closes).toBe(1);
});

test("a hook served no job authority refuses every verb with the reason it has none", () => {
  const jobs = unauthorized(ENABLE_WITHOUT_JOBS);
  expect(() => jobs.describe({ machineId: "m", pluginId: "atyrode.babel" })).toThrow(/no job slice/);
  expect(() => jobs.schedules()).toThrow(/no job slice/);
  expect(() => jobs.schedule({} as never)).toThrow(/no job slice/);
});

test("what a folder is is asked in the shape the host that served the slice takes", async () => {
  const fact = {
    path: "/home/alex/babel",
    identity: "/home/alex/babel/.git",
    remote: "github.com/atyrode/babel",
    reason: "repository",
    observedAt: 1_757_000_000_000,
  };

  // A HARDENED half is served the kit's handle: one query object, because that is what crosses
  // the ipc frame.
  const queries: unknown[] = [];
  const hardened = machinesSlice({
    repository: async (query: unknown) => {
      queries.push(query);
      return await Promise.resolve({ ok: true, fact });
    },
  } as unknown as GuestCtx["machines"]);
  expect(await hardened.repository("m", "/home/alex/babel")).toMatchObject({ ok: true, fact });
  expect(queries).toEqual([{ machineId: "m", path: "/home/alex/babel" }]);

  // A bundle the host IMPORTED — the default for an installed server half — is handed the
  // machine gateway's own admission, which takes the machine and the path as two arguments.
  // Handing that one a query object would ask about a machine called "[object Object]".
  const positional: unknown[] = [];
  const inRealm = machinesSlice({
    repository: (machineId: string, path: string) => {
      positional.push([machineId, path]);
      return { ok: true, fact };
    },
  } as unknown as GuestCtx["machines"]);
  expect(await inRealm.repository("m", "/home/alex/babel")).toMatchObject({ ok: true, fact });
  expect(positional).toEqual([["m", "/home/alex/babel"]]);

  // A hook's context carries no machines member at all, and the refusal says so rather than
  // answering with a fact nobody observed.
  expect(await unaskable(HOOK_WITHOUT_MACHINES).repository("m", "/home/alex/babel")).toEqual({
    ok: false,
    reason: HOOK_WITHOUT_MACHINES,
  });
});
