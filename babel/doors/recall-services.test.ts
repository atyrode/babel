import { expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  type ConfigureInstanceServiceArgs,
  type InstanceServiceConfigurationRead,
  type JobDescription,
  type ServiceConfigurationRead,
} from "@manifold/protocol";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  MACHINE_OPERATIONS,
  RECALL_SERVICE_ID,
  RecallInstalledSchema,
  RecallSetupPreviewSchema,
  type RecallPolicy,
} from "../contract.ts";
import {
  composeRecallServicePolicy,
  recallServiceDoors,
  type RecallRuntime,
} from "./recall-services.ts";

const MACHINE = "recall-owner";
const REVISION = "a".repeat(64);
const MOVED = "b".repeat(64);
const RUNTIME: RecallRuntime = {
  installationRevision: "babel-install-1",
  artifactSha256: "c".repeat(64),
  resourceBindingDigest: "d".repeat(64),
};
const POLICY: RecallPolicy = {
  version: 1,
  classes: [
    { id: "public", label: "Public", ceiling: 0 },
    { id: "private", label: "Private", ceiling: 3 },
  ],
  subjects: [{ name: "Sensitive owner label", host: "archive-owner", sensitivity: 2 }],
};

interface OwnerFixture {
  ctx: GuestCtx;
  reads: string[];
  writes: ConfigureInstanceServiceArgs[];
  described: JobDescription;
  native: ServiceConfigurationRead;
  installed: InstanceServiceConfigurationRead;
  control: { configureError: string };
}

function owner(isRoot = true): OwnerFixture {
  const reads: string[] = [];
  const writes: ConfigureInstanceServiceArgs[] = [];
  const described: JobDescription = {
    machineId: MACHINE,
    pluginId: BABEL_PLUGIN_ID,
    admissionPublicKey: "-----BEGIN PUBLIC KEY-----synthetic",
    connected: true,
    platforms: ["linux-x64"],
    operations: {
      [MACHINE_OPERATIONS.recall]: {
        ready: true,
        reason: null,
        resourceBindingDigest: RUNTIME.resourceBindingDigest,
      },
    },
    installation: {
      revision: RUNTIME.installationRevision,
      artifactSha256: RUNTIME.artifactSha256,
      enabled: true,
      ready: true,
      purgeRequested: false,
    },
    retainedInstallations: [],
    consents: [],
  };
  const native: ServiceConfigurationRead = {
    configuration: { revision: null, policies: [] },
    connected: true,
    credentialReferences: [],
    runtimeCandidates: [
      {
        runtime: { pluginId: BABEL_PLUGIN_ID, operationId: MACHINE_OPERATIONS.recall, ...RUNTIME },
        ready: true,
        reason: null,
      },
    ],
  };
  const installed: InstanceServiceConfigurationRead = {
    description: {
      serviceId: RECALL_SERVICE_ID,
      defaultOwner: null,
      owner: null,
      configuration: null,
      connected: true,
      state: "unconfigured",
      reason: null,
    },
    policy: null,
  };
  const control = { configureError: "" };
  const ctx = {
    auth: { isRoot },
    jobs: {
      describe: async () => {
        reads.push("runtime");
        return described;
      },
    },
    services: {
      readConfiguration: async () => {
        reads.push("native");
        return native;
      },
      readInstanceConfiguration: async () => {
        reads.push("instance");
        return installed;
      },
      configureInstance: async (args: ConfigureInstanceServiceArgs) => {
        if (control.configureError) throw new Error(control.configureError);
        if (args.expectedRevision !== (installed.description.configuration?.revision ?? null))
          throw new Error("conflict");
        writes.push(args);
        installed.policy = args.policy;
        installed.description.owner = { machineId: args.machineId!, name: "Owner", online: true };
        installed.description.configuration = {
          revision: REVISION,
          pluginId: BABEL_PLUGIN_ID,
          enabled: args.enabled,
          policySha256: "e".repeat(64),
        };
        installed.description.state = "starting";
        return installed.description;
      },
    },
  } as unknown as GuestCtx;
  return { ctx, reads, writes, described, native, installed, control };
}

async function knock(name: string, ctx: GuestCtx, args: unknown): Promise<unknown> {
  const door = recallServiceDoors().find((candidate) => candidate.action.name === name);
  if (!door) throw new Error("Missing Recall configuration door");
  return door.handler(ctx, door.action.input.parse(args) as never);
}

async function preview(ctx: GuestCtx, policy = POLICY) {
  return RecallSetupPreviewSchema.parse(
    await knock(ACTIONS.previewRecall, ctx, { machineId: MACHINE, policy }),
  );
}

async function install(
  ctx: GuestCtx,
  shown: { expectedRevision: string | null; previewDigest: string },
  policy = POLICY,
) {
  return knock(ACTIONS.installRecall, ctx, {
    machineId: MACHINE,
    policy,
    expectedRevision: shown.expectedRevision,
    previewDigest: shown.previewDigest,
  });
}

function expectRefusal(value: unknown) {
  if (value === null || typeof value !== "object" || !("refused" in value)) {
    throw new Error("Expected a configuration refusal");
  }
  expect(typeof value.refused).toBe("string");
}

test("non-owners cannot read or mutate Recall configuration", async () => {
  const fleet = owner(false);
  expectRefusal(
    await knock(ACTIONS.previewRecall, fleet.ctx, { machineId: MACHINE, policy: POLICY }),
  );
  expectRefusal(
    await knock(ACTIONS.installRecall, fleet.ctx, {
      machineId: MACHINE,
      policy: POLICY,
      expectedRevision: null,
      previewDigest: REVISION,
    }),
  );
  expect(fleet.reads).toEqual([]);
  expect(fleet.writes).toEqual([]);
});

test("preview refuses missing, wrong, or unready native service runtimes without mutation", async () => {
  const mutations: ((fleet: OwnerFixture) => void)[] = [
    (fleet) => {
      fleet.described.installation = null;
    },
    (fleet) => {
      fleet.described.operations = {};
    },
    (fleet) => {
      fleet.described.pluginId = "other.plugin";
    },
    (fleet) => {
      fleet.described.installation!.purgeRequested = true;
    },
    (fleet) => {
      fleet.native.runtimeCandidates = [];
    },
    (fleet) => {
      fleet.native.runtimeCandidates[0]!.runtime.operationId = MACHINE_OPERATIONS.archive;
    },
    (fleet) => {
      fleet.native.runtimeCandidates[0]!.runtime.resourceBindingDigest = MOVED;
    },
    (fleet) => {
      fleet.native.runtimeCandidates[0]!.ready = false;
    },
  ];
  for (const mutate of mutations) {
    const fleet = owner();
    mutate(fleet);
    const shown = await preview(fleet.ctx);
    expect(shown.ready).toBe(false);
    expectRefusal(await install(fleet.ctx, shown));
    expect(fleet.writes).toEqual([]);
  }
});

test("owner preview is read-only and install configures only the selected instance", async () => {
  const fleet = owner();
  const shown = await preview(fleet.ctx);
  expect(shown.ready).toBe(true);
  expect(shown.changed).toBe(true);
  expect(shown.classes.map((entry) => entry.target)).toEqual(
    POLICY.classes.map(({ id }) => ({
      kind: "service",
      machineId: MACHINE,
      serviceId: RECALL_SERVICE_ID,
      operationId: id,
    })),
  );
  expect(fleet.writes).toEqual([]);
  const result = RecallInstalledSchema.parse(await install(fleet.ctx, shown));
  expect(result.installed).toBe(true);
  expect(result.revision).toBe(REVISION);
  expect(fleet.installed.description.owner?.machineId).toBe(MACHINE);
  expect(fleet.installed.description.configuration?.enabled).toBe(true);
  expect(fleet.installed.policy).toEqual(composeRecallServicePolicy(POLICY, RUNTIME));
  const unchanged = await preview(fleet.ctx);
  expect(unchanged.changed).toBe(false);
  const repeated = RecallInstalledSchema.parse(await install(fleet.ctx, unchanged));
  expect(repeated.installed).toBe(false);
  expect(repeated.revision).toBe(REVISION);
  expect(fleet.writes).toHaveLength(1);
});

test("a changed owner policy or service revision invalidates the preview without mutation", async () => {
  const fleet = owner();
  const shown = await preview(fleet.ctx);
  const changed: RecallPolicy = {
    ...POLICY,
    subjects: [{ ...POLICY.subjects[0]!, sensitivity: 0 }],
  };
  expectRefusal(await install(fleet.ctx, shown, changed));
  fleet.installed.description.configuration = {
    revision: MOVED,
    pluginId: BABEL_PLUGIN_ID,
    enabled: true,
    policySha256: REVISION,
  };
  expectRefusal(await install(fleet.ctx, shown));
  expect(fleet.writes).toEqual([]);
});

test("each native installation pin invalidates an earlier preview", async () => {
  for (const pin of ["installationRevision", "artifactSha256", "resourceBindingDigest"] as const) {
    const fleet = owner();
    const shown = await preview(fleet.ctx);
    fleet.native.runtimeCandidates[0]!.runtime[pin] = MOVED;
    if (pin === "installationRevision") fleet.described.installation!.revision = MOVED;
    else if (pin === "artifactSha256") fleet.described.installation!.artifactSha256 = MOVED;
    else fleet.described.operations![MACHINE_OPERATIONS.recall]!.resourceBindingDigest = MOVED;
    // Still a valid, ready runtime: refusal must be the stale tuple, not unavailability.
    expect((await preview(fleet.ctx)).ready).toBe(true);
    expectRefusal(await install(fleet.ctx, shown));
    expect(fleet.writes).toEqual([]);
  }
});

test("native compare-and-swap refusal does not expose private diagnostic values", async () => {
  const fleet = owner();
  const shown = await preview(fleet.ctx);
  const secret = "private-native-error-value";
  fleet.control.configureError = secret;
  const result = await install(fleet.ctx, shown);
  expectRefusal(result);
  expect(JSON.stringify(result)).not.toContain(secret);
  expect(fleet.writes).toEqual([]);
});

test("oversized owner metadata is refused before native configuration without disclosure", async () => {
  const fleet = owner();
  const secret = "private-owner-metadata";
  const oversized: RecallPolicy = {
    ...POLICY,
    subjects: Array.from({ length: 256 }, (_, index) => ({
      name: `${secret}-${index}`,
      host: "archive-owner",
      sensitivity: 2,
      workspace: "世".repeat(2048),
    })),
  };
  const shown = await preview(fleet.ctx, oversized);
  expect(shown.ready).toBe(false);
  expect(JSON.stringify(shown)).not.toContain(secret);
  expectRefusal(await install(fleet.ctx, shown, oversized));
  expect(fleet.writes).toEqual([]);
});

test("mapping exports require owner opt-in and never widen either invocable reader", () => {
  const ordinary = composeRecallServicePolicy(POLICY, RUNTIME);
  expect(ordinary.operations.mapping).toBeUndefined();
  expect(ordinary.operations.public).toMatchObject({ invocable: true, path: "/recall/public" });
  expect(ordinary.operations["map.public"]).toMatchObject({
    invocable: true,
    path: "/maps/public",
  });
  const configured = composeRecallServicePolicy({ ...POLICY, mappingClassId: "private" }, RUNTIME);
  expect(configured.operations.public).toEqual(ordinary.operations.public);
  expect(configured.operations["map.public"]).toEqual(ordinary.operations["map.public"]);
  expect(configured.operations.mapping).toMatchObject({
    kind: "http-proxy",
    path: "/mapping",
    method: "POST",
    response: { kind: "stream", disclosure: "full", contentTypes: ["application/json"] },
  });
  expect(configured.operations.mapping).not.toHaveProperty("invocable");
  expect(() =>
    composeRecallServicePolicy({ ...POLICY, mappingClassId: "missing" }, RUNTIME),
  ).toThrow();
});
