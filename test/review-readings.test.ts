import { afterEach, expect, test } from "bun:test";
import type { GuestCtx, ServerActionDef } from "@manifold/plugin-kit/server";
import {
  GOVERNED_CAPS,
  hasCap,
  PluginManifestSchema,
  type Cap,
  type PluginManifest,
  type PluginRoster,
} from "@manifold/protocol";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  JEV_PLUGIN_ID,
  JEV_SERVICE_ID,
  REVIEW_READINGS_HELD,
  REVIEW_READINGS_TTL_MS,
  ROLES,
  ReviewReadingsInputSchema,
  ReviewReadingsResultSchema,
  reviewReadingProviderRevision,
} from "../babel/contract.ts";
import { reviewReadingsDoor } from "../babel/doors/review-readings.ts";
import {
  refreshReviewReadings,
  refreshReviewReadingsAction,
} from "../babel/jev/review-readings.ts";
import { JevAnswers, judge, requestFor } from "../babel/jev/server/judge.ts";
import { ReviewReadings, type ReadingMetadata } from "../babel/server/review-readings.ts";
import {
  coordinator,
  DEFAULT_POLICY,
  ReviewDispatchSchema,
  type Policy,
  type ReviewAssignment,
} from "../babel/store/coordinator.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import manifest from "../babel/jev/manifest.json";
import baselineManifest from "../babel/manifest.json";

const NOW = Date.parse("2026-09-29T12:00:00Z");
const OLD = "hyp_00000001";
const NEW = "hyp_00000002";
const stores: TestStore[] = [];
afterEach(() => {
  for (const store of stores.splice(0)) store.close();
});

async function fixture(policy: Partial<Policy> = {}, principal?: readonly Cap[]) {
  const held = await openTestStore(NOW);
  stores.push(held);
  let moment = NOW;
  const control = {
    enabled: true,
    policy: "r1",
    bundle: "a".repeat(64),
    epoch: 1,
    metadata: true,
    invokes: 0,
    serviceAuthority: true,
    onList: undefined as (() => Promise<void>) | undefined,
  };
  const roster = (): PluginRoster => [
    {
      manifest: PluginManifestSchema.parse(manifest),
      enabled: control.enabled,
      source: "plugin",
      actions: [],
      changedAt: control.epoch,
      install: {
        sha256: control.bundle,
        source: "synthetic",
        grantedCaps: ["containers:read", "services:invoke"],
        installedBy: "operator",
        installedAt: 1,
      },
    },
  ];
  const metadata: ReadingMetadata = {
    host: {
      enabled: async () => control.enabled,
      roster: async () => {
        if (!control.metadata) throw new Error("metadata unavailable");
        return roster();
      },
    },
    services: {
      listInstances: async () => {
        await control.onList?.();
        return {
          defaultOwner: null,
          services: control.serviceAuthority
            ? [
                {
                  serviceId: JEV_SERVICE_ID,
                  defaultOwner: null,
                  owner: { machineId: "test", name: "test", online: true },
                  connected: true,
                  state: "ready",
                  configuration: {
                    revision: control.policy,
                    pluginId: JEV_PLUGIN_ID,
                    enabled: true,
                    policySha256: "b".repeat(64),
                  },
                  reason: null,
                },
              ]
            : [],
        };
      },
    },
  };
  const inbox = new ReviewReadings(held.store, () => moment);
  const door = reviewReadingsDoor(inbox);
  const jev = PluginManifestSchema.parse(manifest);
  const baseline = PluginManifestSchema.parse(baselineManifest);
  // Model the pinned host's native cap intersection and per-operation service visibility.
  // Delegates are a native ceiling, not extra caller permission or a flat governed grant.
  function served(action: ServerActionDef, plugin: PluginManifest): ReadingMetadata {
    if (principal === undefined) return metadata;
    for (const cap of action.caps) {
      if (!(cap === "*" ? principal.includes("*") : hasCap(principal, cap)))
        throw new Error("caller capability absent");
    }
    const declared = [...action.caps, ...(action.delegates ?? [])];
    const visible = (["services:read", "services:invoke"] as const).some(
      (cap) => hasCap(principal, cap) && hasCap(declared, cap) && hasCap(plugin.capabilities, cap),
    );
    return {
      host: metadata.host,
      services: {
        listInstances: async (input) => {
          const listed = await metadata.services.listInstances(input);
          return { ...listed, services: visible ? listed.services : [] };
        },
      },
    };
  }
  const actions: GuestCtx["actions"] = {
    call: async (request) => {
      if (request.plugin !== BABEL_PLUGIN_ID || request.action !== ACTIONS.reviewReadings)
        throw new Error("undeclared door");
      const ceiling = jev.capabilities.filter((cap) => !GOVERNED_CAPS.includes(cap));
      if (
        door.action.caps.some(
          (cap) => !(cap === "*" ? ceiling.includes("*") : hasCap(ceiling, cap)),
        )
      )
        throw new Error("caller ceiling");
      const ctx = {
        ...served(door.action, baseline),
        callerPlugin: JEV_PLUGIN_ID,
      } as unknown as GuestCtx;
      const result = await door.handler(
        ctx,
        ReviewReadingsInputSchema.parse(request.input) as never,
      );
      return ReviewReadingsResultSchema.parse(result);
    },
  };
  const answers = new JevAnswers();
  await insert(held.db, "policies", {
    version: "test",
    seq: 1,
    actor_id: "operator",
    reason: "fixture",
    recorded_at: new Date(NOW - 100_000).toISOString(),
    payload: JSON.stringify({
      ...DEFAULT_POLICY,
      enabled: true,
      version: "test",
      coverageShare: 1 - 2e-9,
      discoveryShare: 1e-9,
      explorationShare: 1e-9,
      filingShare: 0,
      backlogShare: 0,
      batchSize: 16,
      perCycleCost: 100,
      dailyCost: 1000,
      ...policy,
    }),
  });
  for (const [id, age, statement] of [
    [OLD, 100_000, "Older claim"],
    [NEW, 1000, "Newer claim"],
  ] as const) {
    await insert(held.db, "records", {
      id,
      kind: "hypothesis",
      root_id: id,
      seq: 0,
      actor_kind: "run",
      actor_id: "seed",
      title: statement,
      created_at: new Date(NOW - age).toISOString(),
      payload: JSON.stringify({ statement }),
    });
  }
  const makeCoordinator = (advisory = true) =>
    coordinator(
      held.store,
      () => moment,
      null,
      advisory ? async () => await inbox.snapshot(metadata) : undefined,
    );
  async function draw(advisory = true, seed = 42n) {
    const result = await makeCoordinator(advisory).draw({ runId: "draw", now: moment, seed });
    if (result.outcome !== "assignment" || result.assignment.activity !== "review")
      throw new Error("no review");
    return result.assignment;
  }
  async function warm(id: string, result: Record<string, string | number>) {
    const record = (await inbox.records([id]))[0]!;
    await judge(
      {
        ...metadata.services,
        invokeInstance: async () => {
          control.invokes += 1;
          return { type: "service_result", requestId: "fixture", ok: true, result };
        },
      },
      requestFor(record.kind, record.text),
      answers,
    );
  }
  return {
    held,
    inbox,
    door,
    metadata,
    control,
    answers,
    actions,
    makeCoordinator,
    draw,
    warm,
    refresh: async (records = [OLD, NEW]) =>
      await refreshReviewReadings(
        {
          ...served(refreshReviewReadingsAction, jev),
          actions,
          answers,
        },
        records,
      ),
    advance: () => {
      moment += REVIEW_READINGS_TTL_MS;
    },
  };
}

function withoutAdvice(assignment: ReviewAssignment) {
  const { reviewSelection: _reading, ...baseline } = assignment;
  return baseline;
}

test("zero assessments and a cold memo preserve the exact draw in every review lane", async () => {
  for (const lane of ["coverageShare", "discoveryShare", "explorationShare", "weighted"] as const) {
    const f = await fixture({
      coverageShare: 0,
      ...(lane === "weighted" ? {} : { [lane]: 1 - 2e-9 }),
    });
    expect(await f.draw()).toEqual(await f.draw(false));
    expect((await f.refresh()).accepted).toBe(0);
    for (const seed of [1n, 19n, 42n, 900n]) {
      expect(withoutAdvice(await f.draw(true, seed))).toEqual(await f.draw(false, seed));
    }
    expect(f.control.invokes).toBe(0);
    expect(await f.held.db.query("SELECT id FROM assessments")).toEqual([]);
  }
});

test.each(["coverageShare", "discoveryShare"] as const)(
  "%s consumes cached advice without another call or eligibility veto",
  async (lane) => {
    const f = await fixture({ coverageShare: 0, [lane]: 1 - 2e-9 });
    await f.warm(NEW, { worth_first: 100 });
    await f.warm(OLD, { worth_first: -100 });
    expect((await f.refresh()).accepted).toBe(2);
    expect((await f.draw(false)).recordId).toBe(OLD);
    const selected = await f.draw();
    expect(selected.recordId).toBe(NEW);
    expect(selected.reviewSelection).toMatchObject({
      mode: "cached-advisory",
      reason: "cached-current",
      funding: "unknown",
    });
    expect(f.control.invokes).toBe(2);
    // Claim every eligible role of the favored hypothesis. Its advice cannot defeat a live claim,
    // and the objected record remains work when the favored one has no available role.
    const shared = f.makeCoordinator();
    for (let role = 0; role < 4; role += 1) {
      const result = await shared.draw({ runId: "claiming", now: NOW, seed: 42n });
      if (result.outcome !== "assignment") throw new Error("expected review");
      expect(result.assignment.recordId).toBe(NEW);
      expect(
        (await shared.claim({ assignment: result.assignment, runId: "claiming", now: NOW }))
          .outcome,
      ).toBe("granted");
    }
    const remaining = await shared.draw({ runId: "claiming", now: NOW, seed: 42n });
    expect(remaining.outcome === "assignment" && remaining.assignment.recordId).toBe(OLD);
    expect(await f.held.db.query("SELECT id FROM assessments")).toEqual([]);
    expect(await f.held.db.query("SELECT id FROM dispositions")).toEqual([]);
  },
);

test("the weighted lane favors cached backing without removing the other record", async () => {
  const f = await fixture({ coverageShare: 0 });
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  let ordinary = 0;
  let advised = 0;
  for (let index = 1; index <= 256; index += 1) {
    const seed = BigInt(index) * 0x9e3779b97f4a7c15n;
    if ((await f.draw(false, seed)).recordId === NEW) ordinary += 1;
    if ((await f.draw(true, seed)).recordId === NEW) advised += 1;
  }
  expect(advised).toBeGreaterThan(ordinary);
  expect(advised).toBeLessThan(256);
  expect(f.control.invokes).toBe(1);
});

test("exploration stays uniform and unusable voter answers remain the exact baseline", async () => {
  const f = await fixture({ coverageShare: 0, explorationShare: 1 - 2e-9 });
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  for (const seed of [1n, 9n, 61n, 187n]) {
    const advised = await f.draw(true, seed);
    expect(advised.recordId).toBe((await f.draw(false, seed)).recordId);
    expect(advised.reviewSelection?.reason).toBe("uniform-exploration");
  }
  const unanswered = await fixture();
  await unanswered.warm(OLD, { worth_first: "malformed" });
  await unanswered.refresh();
  const result = await unanswered.draw();
  expect(withoutAdvice(result)).toEqual(await unanswered.draw(false));
  expect(result.reviewSelection).toMatchObject({ mode: "lane-age", reason: "no-numeric-reading" });
});

test.each([
  ["disabled", "provider-unavailable"],
  ["bundle", "provider-changed"],
  ["policy", "policy-changed"],
  ["metadata", "metadata-unavailable"],
  ["expired", "readings-expired"],
] as const)("%s advice degrades to the exact seeded baseline", async (change, reason) => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  if (change === "disabled") f.control.enabled = false;
  if (change === "bundle") f.control.bundle = "c".repeat(64);
  if (change === "policy") f.control.policy = "r2";
  if (change === "metadata") f.control.metadata = false;
  if (change === "expired") f.advance();
  const result = await f.draw();
  expect(withoutAdvice(result)).toEqual(await f.draw(false));
  expect(result.reviewSelection).toMatchObject({ mode: "lane-age", reason, funding: "unknown" });
  expect(f.control.invokes).toBe(1);
});

test("a revised record cannot inherit the previous text's cached reading", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  const revised = "hyp_00000003";
  await insert(f.held.db, "records", {
    id: revised,
    kind: "hypothesis",
    root_id: NEW,
    seq: 1,
    supersedes_id: NEW,
    actor_kind: "run",
    actor_id: "seed",
    title: "Changed claim",
    created_at: new Date(NOW).toISOString(),
    payload: JSON.stringify({ statement: "Changed claim" }),
  });
  expect((await f.refresh([NEW, revised])).accepted).toBe(0);
  expect(withoutAdvice(await f.draw())).toEqual(await f.draw(false));
  expect(f.control.invokes).toBe(1);
});

test("only the immediate authenticated part can publish; caller fields grant nothing", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  const snapshot = (await f.inbox.snapshot(f.metadata))!;
  const publish = {
    providerRevision: snapshot.providerRevision,
    policyRevision: snapshot.policyRevision,
    records: [NEW],
    readings: [...snapshot.readings.values()],
  };
  for (const callerPlugin of [null, "untrusted.part", undefined]) {
    const ctx = { ...f.metadata, callerPlugin } as unknown as GuestCtx;
    const result = await f.door.handler(ctx, { publish, callerPlugin: JEV_PLUGIN_ID } as never);
    expect(result).toHaveProperty("refused");
  }
  expect(refreshReviewReadingsAction.input.safeParse({ records: [NEW], publish }).success).toBe(
    false,
  );
  expect(
    ReviewReadingsInputSchema.safeParse({ records: Array(REVIEW_READINGS_HELD + 1).fill(NEW) })
      .success,
  ).toBe(false);
});

test("a warm cached reading says funding unknown, and policy edits during refresh discard it", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  const refreshed = await f.refresh();
  expect(refreshed.funding).toBe("unknown");
  expect((await f.draw()).recordId).toBe(NEW);
  const changingActions: GuestCtx["actions"] = {
    call: async (request) => {
      const result = await f.actions.call(request);
      if ("records" in (request.input as object)) f.control.policy = "r2";
      return result;
    },
  };
  expect(
    (
      await refreshReviewReadings({ ...f.metadata, actions: changingActions, answers: f.answers }, [
        NEW,
      ])
    ).accepted,
  ).toBe(0);
  expect(withoutAdvice(await f.draw())).toEqual(await f.draw(false));
  expect(f.control.invokes).toBe(1);
});

test("a forged text digest and stale record revision cannot enter the inbox", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  const snapshot = (await f.inbox.snapshot(f.metadata))!;
  const reading = snapshot.readings.get(NEW)!;
  for (const changed of [
    { ...reading, textDigest: "d".repeat(64) },
    { ...reading, revision: 77 },
  ]) {
    expect(
      await f.inbox.publish(f.metadata, {
        providerRevision: reviewReadingProviderRevision(await f.metadata.host.roster())!,
        policyRevision: "r1",
        records: [NEW],
        readings: [changed],
      }),
    ).toBe(0);
    expect(withoutAdvice(await f.draw())).toEqual(await f.draw(false));
  }
});

test("non-root cache refresh uses existing service authority, never caller ceiling escalation", async () => {
  for (const principal of [["containers:read", "services:invoke"], ["containers:read"]] as const) {
    const f = await fixture({}, principal);
    await f.warm(NEW, { worth_first: 100 });
    const permitted = principal.length === 2;
    expect((await f.refresh()).accepted).toBe(permitted ? 1 : 0);
    expect((await f.draw()).recordId).toBe(permitted ? NEW : OLD);
    // Keeping a flat cap while withdrawing its operation target must also hide the service.
    f.control.serviceAuthority = false;
    expect((await f.refresh()).accepted).toBe(0);
    expect(withoutAdvice(await f.draw())).toEqual(await f.draw(false));
    expect(f.control.invokes).toBe(1);
  }
});

test("advice expiring while metadata is awaited cannot change the in-progress draw", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  const baseline = await f.draw(false);
  const entered = Promise.withResolvers<void>();
  const released = Promise.withResolvers<void>();
  f.control.onList = async () => {
    entered.resolve();
    await released.promise;
  };
  const drawing = f.draw();
  await entered.promise;
  f.advance();
  released.resolve();
  const result = await drawing;
  expect(withoutAdvice(result)).toEqual(baseline);
  expect(result.reviewSelection?.reason).toBe("readings-expired");
  expect((await f.inbox.snapshot(f.metadata))?.readings.has(NEW)).toBe(false);
});

test("implicit seed preserves activity and lane entropy across advice publication and disablement", async () => {
  const f = await fixture({
    coverageShare: 0.3,
    discoveryShare: 0.2,
    explorationShare: 0.2,
    activityWeights: { review: 1, explore: 1, challenge: 0, synthesize: 0, map: 0 },
    review: ReviewDispatchSchema.parse({
      machineId: "test",
      profile: { containerId: "ctr_test", expectedRevision: 1 },
      roleRecipes: Object.fromEntries(ROLES.map((role) => [role, "installed"])),
      stageRecipes: { explore: "installed" },
      recipes: [{ id: "installed", version: 1, body: "Read the offered evidence." }],
    }),
  });
  await insert(f.held.db, "sessions", {
    selector: "omp/advice-seed",
    host: "test",
    harness: "omp",
    source_id: "advice-seed",
    live: 0,
    kind: "operator",
    size: 100,
    content_digest: "session-digest",
    archive_label: "test",
    archive_path: "/synthetic/advice-seed.jsonl",
    snapshot_id: "e".repeat(64),
    archived_at: new Date(NOW - 1000).toISOString(),
    modified_at: new Date(NOW - 1000).toISOString(),
    seen_at: new Date(NOW - 1000).toISOString(),
  });
  const request = { runId: "implicit", now: NOW };
  expect(await f.makeCoordinator().draw(request)).toEqual(
    await f.makeCoordinator(false).draw(request),
  );
  await f.warm(NEW, { worth_first: 100 });
  await f.warm(OLD, { worth_first: -100 });
  await f.refresh();
  const activities = new Set<string>();
  let changedReview = false;
  let uniformReview = false;
  for (let index = 0; index < 64; index += 1) {
    const ask = { ...request, runId: `implicit-${String(index)}` };
    const advised = await f.makeCoordinator().draw(ask);
    const baseline = await f.makeCoordinator(false).draw(ask);
    if (advised.outcome !== "assignment" || baseline.outcome !== "assignment")
      throw new Error("expected an eligible activity");
    const a = advised.assignment;
    const b = baseline.assignment;
    expect(a.seed).toBe(b.seed);
    expect(a.activity).toBe(b.activity);
    activities.add(a.activity);
    if (a.activity !== "review") {
      expect(a.id).toBe(b.id);
      expect(a.lane).toBe(b.lane);
    } else if (a.reviewSelection?.reason === "uniform-exploration") {
      uniformReview = true;
      expect(a.id).toBe(b.id);
    } else {
      changedReview ||= a.recordId !== b.recordId;
    }
  }
  expect([...activities].sort()).toEqual(["explore", "review"]);
  expect(changedReview).toBe(true);
  expect(uniformReview).toBe(true);
  f.control.enabled = false;
  const disabled = await f.makeCoordinator().draw(request);
  const baseline = await f.makeCoordinator(false).draw(request);
  if (disabled.outcome === "assignment" && disabled.assignment.activity === "review") {
    expect(baseline).toEqual({ ...disabled, assignment: withoutAdvice(disabled.assignment) });
  } else {
    expect(disabled).toEqual(baseline);
  }
  expect(f.control.invokes).toBe(2);
});

test("a metadata-less wake borrows no prior dispatch authority and does not revoke later advice", async () => {
  const f = await fixture();
  await f.warm(NEW, { worth_first: 100 });
  await f.refresh();
  expect((await f.draw()).recordId).toBe(NEW);
  // This is the current pin's lifecycle consumption seam, also needed for a revoked installer.
  // No captured dispatch context is available to the new wake.
  const wake = coordinator(
    f.held.store,
    () => NOW,
    null,
    async () => await f.inbox.snapshot(undefined),
  );
  const result = await wake.draw({ runId: "draw", now: NOW, seed: 42n });
  if (result.outcome !== "assignment" || result.assignment.activity !== "review")
    throw new Error("expected an ordinary review");
  expect(withoutAdvice(result.assignment)).toEqual(await f.draw(false));
  expect(result.assignment.reviewSelection?.reason).toBe("metadata-unavailable");
  // Lack of this wake's metadata is not a global provider revocation.
  expect((await f.draw()).recordId).toBe(NEW);
  f.control.enabled = false;
  expect(withoutAdvice(await f.draw())).toEqual(await f.draw(false));
  expect(f.control.invokes).toBe(1);
});
