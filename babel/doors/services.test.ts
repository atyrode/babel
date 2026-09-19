import { expect, test } from "bun:test";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  PluginManifestSchema,
  ServicePolicySchema,
  servicePolicyCredentialRefs,
  type ServiceConfiguration,
  type ServiceConfigurationRead,
  type ServicePolicy,
} from "@manifold/protocol";
import { ACTIONS, RESTIC_SERVICE } from "../contract.ts";
import babelManifest from "../manifest.json";
import type { Door } from "./door.ts";
import { composePolicy, declaredServices, digestOf, serviceDoors } from "./services.ts";

/*
  COMPOSING AND INSTALLING A SERVICE POLICY (#400).

  Three properties are worth holding, and a plausible change breaks each.

  - THE POLICY IS THE MANIFEST'S. A composer that spelled the service id, its revision or its
    operations a second time is the binding mismatch this replaces: the hub fingerprints the
    installed policy and refuses a job whose declaration disagrees, so the two must come from
    one place. The tests derive what they expect from `declaredServices(manifest)`.
  - A PREVIEW IS A PROMISE ABOUT ONE REVISION. An install that applied a preview composed
    against a configuration that has since moved is the failure the three-step exists to
    prevent, and it must refuse rather than overwrite.
  - THE KEY IS NEVER HERE, which is `jev/server/credential.test.ts`'s own terms: absent by
    construction rather than by assertion, so what is asserted is that no other road acquires
    one. A value is planted in the environment, the composition is run, and every byte that
    leaves this plugin — the preview and the arguments of the host call — is searched for it.
*/

/** A planted key. If a future composer ever read ambient state, this is what it would find. */
const PLANTED = "sk-planted-by-the-test-never-a-real-shape";
const MACHINE = "m-dev-01";
const ORIGIN = "https://store.example";
const OTHER_ORIGIN = "https://store.elsewhere";
/** A configuration revision as the engine mints them: sha256 over the policies. */
const REVISION = "a".repeat(64);
const MOVED = "b".repeat(64);

/** A policy of somebody else's on the same machine, which the swap must carry through. */
const FOREIGN: ServicePolicy = ServicePolicySchema.parse({
  serviceId: "someone.else.thing",
  revision: "3",
  origin: "https://elsewhere.example",
  allowLoopbackHttp: false,
  maxConcurrent: 1,
  operations: {
    ask: {
      kind: "http-proxy",
      method: "GET",
      path: "/ask",
      request: { kind: "none" },
      response: {
        kind: "stream",
        disclosure: "full",
        contentTypes: ["application/json"],
        headers: [],
      },
      timeoutMs: 1000,
      maxRequestBytes: 512,
      maxResponseBytes: 512,
    },
  },
});

const MANIFEST = PluginManifestSchema.parse(babelManifest);
const DECLARED = declaredServices(MANIFEST);

interface Hub {
  readonly ctx: GuestCtx;
  readonly reads: string[];
  readonly configures: unknown[];
}

/**
 * The hub, as much of it as these two doors reach: one configuration read and one write, both
 * recorded. `refusal` is the host refusing the write, which is what a lost compare-and-swap
 * race looks like from in here.
 */
function hub(options: {
  readonly isRoot?: boolean;
  readonly read?: Partial<ServiceConfigurationRead>;
  readonly refusal?: string;
}): Hub {
  const reads: string[] = [];
  const configures: unknown[] = [];
  const read: ServiceConfigurationRead = {
    configuration: { revision: null, policies: [] },
    connected: true,
    credentialReferences: [],
    runtimeCandidates: [],
    ...options.read,
  };
  const ctx = {
    auth: { isRoot: options.isRoot ?? true },
    services: {
      readConfiguration: async ({ machineId }: { machineId: string }) => {
        reads.push(machineId);
        return read;
      },
      configureConfiguration: async (args: {
        machineId: string;
        expectedRevision: string | null;
        policies: ServicePolicy[];
      }): Promise<ServiceConfiguration> => {
        configures.push(args);
        if (options.refusal !== undefined) throw new Error(options.refusal);
        return { revision: MOVED, policies: args.policies };
      },
    },
  } as unknown as GuestCtx;
  return { ctx, reads, configures };
}

function doorNamed(name: string): Door {
  const door = serviceDoors(DECLARED).find((candidate) => candidate.action.name === name);
  if (door === undefined) throw new Error(`no door named ${name}`);
  return door;
}

/** One dispatch, as the kit does it: the door's own input schema, then its handler. */
async function knock(name: string, ctx: GuestCtx, args: unknown): Promise<unknown> {
  const door = doorNamed(name);
  return await door.handler(ctx, door.action.input.parse(args) as never);
}

/** The same, parsed against the door's declared result: a refusal is not that shape. */
async function answer(name: string, ctx: GuestCtx, args: unknown): Promise<Record<string, never>> {
  const produced = await knock(name, ctx, args);
  return doorNamed(name).action.result.parse(produced) as Record<string, never>;
}

async function refusal(name: string, ctx: GuestCtx, args: unknown): Promise<string> {
  const produced = (await knock(name, ctx, args)) as { refused?: string };
  if (typeof produced.refused !== "string") {
    throw new Error(`${name} did not refuse: ${JSON.stringify(produced)}`);
  }
  return produced.refused;
}

/** The policy the composer builds for the one service the manifest declares. */
function resticPolicy(origin = ORIGIN): ServicePolicy {
  const declared = DECLARED.find((service) => service.serviceId === RESTIC_SERVICE.serviceId);
  if (declared === undefined) throw new Error("the manifest declares no restic binding");
  const composed = composePolicy(declared, origin);
  if (composed.policy === null) throw new Error(composed.reason);
  return composed.policy;
}

test("the composed policy is the manifest's binding, not a second spelling of it", () => {
  // A binding mismatch is installable exactly when these two are typed apart, so the expected
  // values come out of the manifest rather than out of this file.
  expect(DECLARED).toEqual([
    {
      serviceId: RESTIC_SERVICE.serviceId,
      revision: RESTIC_SERVICE.revision,
      operationIds: [RESTIC_SERVICE.operationId],
    },
  ]);
  const policy = resticPolicy();
  expect(policy.serviceId).toBe(RESTIC_SERVICE.serviceId);
  expect(policy.revision).toBe(RESTIC_SERVICE.revision);
  expect(Object.keys(policy.operations)).toEqual([RESTIC_SERVICE.operationId]);
  // And the route is the one the machine half calls: `machine/restic.ts` fetches this path.
  const storage = policy.operations[RESTIC_SERVICE.operationId];
  expect(storage).toMatchObject({ kind: "http-proxy", method: "GET", path: RESTIC_SERVICE.path });
});

test("an operation the manifest binds and this bundle calls no route for is refused, not composed", () => {
  // The other half of "a binding mismatch cannot be installed by hand-editing one of the two":
  // a manifest that grows an operation id before the code that calls it composes nothing.
  const composed = composePolicy(
    { serviceId: RESTIC_SERVICE.serviceId, revision: "1", operationIds: ["storage", "forget"] },
    ORIGIN,
  );
  expect(composed.policy).toBeNull();
  expect(composed.reason).toContain("forget");
  expect(composed.reason).toContain("no such route");
});

test("the policy names its credential and the host is what resolves it", () => {
  // `servicePolicyCredentialRefs` is the function the engine itself calls to decide which
  // sources a binding needs available, so its answer is the operative meaning of "the owner
  // injects it". The policy carries the name; nothing carries the value.
  expect(servicePolicyCredentialRefs(resticPolicy())).toEqual([RESTIC_SERVICE.credentialRef]);
});

test("no credential value passes through, by any road", async () => {
  // Planted where a well-meaning "read it from the environment" change would find it. The
  // composition is not offered one and cannot ask for one: what is asserted is that it does not
  // acquire one another way, and that nothing it emits could carry one.
  process.env["BABEL_RESTIC_TOKEN"] = PLANTED;
  process.env["RESTIC_PASSWORD"] = PLANTED;
  try {
    const fleet = hub({ read: { configuration: { revision: REVISION, policies: [FOREIGN] } } });
    const preview = await answer(ACTIONS.previewServices, fleet.ctx, {
      machineId: MACHINE,
      origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
    });
    await knock(ACTIONS.installServices, fleet.ctx, {
      machineId: MACHINE,
      origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
      expectedRevision: REVISION,
      previewDigest: preview["previewDigest"],
    });
    expect(JSON.stringify(preview)).not.toContain(PLANTED);
    expect(JSON.stringify(fleet.configures)).not.toContain(PLANTED);
    // And there is nowhere to write one either: both inputs are strict objects, so a caller
    // that tried to hand Babel a key is refused before a handler runs.
    for (const name of [ACTIONS.previewServices, ACTIONS.installServices]) {
      expect(() =>
        doorNamed(name).action.input.parse({
          machineId: MACHINE,
          origins: [],
          expectedRevision: REVISION,
          previewDigest: "d",
          credential: PLANTED,
        }),
      ).toThrow();
    }
  } finally {
    delete process.env["BABEL_RESTIC_TOKEN"];
    delete process.env["RESTIC_PASSWORD"];
  }
});

test("both doors hold the owner's authority and no capability that stands for it", () => {
  for (const name of [ACTIONS.previewServices, ACTIONS.installServices]) {
    const door = doorNamed(name);
    expect(door.action.caps).toEqual([]);
    expect(door.action.delegates).toEqual(["services:configure"]);
  }
});

test("a principal who is not the owner is refused, and the hub is never asked", async () => {
  // The count, not the sentence: a version that asked the hub and caught its refusal would pass
  // any assertion about the return value while reading a configuration nobody authorized.
  const fleet = hub({ isRoot: false });
  expect(await refusal(ACTIONS.previewServices, fleet.ctx, { machineId: MACHINE })).toBe(
    "reading this machine's service configuration is the owner's act; this principal is not the owner",
  );
  expect(
    await refusal(ACTIONS.installServices, fleet.ctx, {
      machineId: MACHINE,
      expectedRevision: null,
      previewDigest: "d",
    }),
  ).toBe("installing a service policy is the owner's act; this principal is not the owner");
  expect(fleet.reads).toEqual([]);
  expect(fleet.configures).toEqual([]);
});

test("a machine with no policy previews as absent, naming the file the operator must write", async () => {
  const fleet = hub({});
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
  })) as unknown as {
    expectedRevision: string | null;
    current: boolean;
    services: {
      standing: string;
      origin: string;
      reason: string;
      credential: { ref: string; file: string; advertised: boolean; readable: boolean };
    }[];
  };
  expect(preview.expectedRevision).toBeNull();
  expect(preview.current).toBe(false);
  const [row] = preview.services;
  expect(row?.standing).toBe("absent");
  expect(row?.origin).toBe(ORIGIN);
  expect(row?.reason).toBe(
    `no policy is installed under ${RESTIC_SERVICE.serviceId} on ${MACHINE}`,
  );
  expect(row?.credential).toEqual({
    ref: RESTIC_SERVICE.credentialRef,
    file: RESTIC_SERVICE.credentialFile,
    advertised: false,
    readable: false,
  });
});

test("a policy installed whose credential the machine does not hold is not the same as no policy", async () => {
  // THIS IS THE WHOLE POINT OF THE PREVIEW. Both states produce identical silence at the job —
  // the archive refuses and says nothing an operator can act on — so the two sentences must
  // differ, and the second must name the file.
  const installed = resticPolicy();
  const fleet = hub({
    read: { configuration: { revision: REVISION, policies: [installed] } },
  });
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [],
  })) as unknown as { current: boolean; services: { standing: string; reason: string }[] };
  // No origin was typed and none was needed: the installed policy's own is what it composes
  // against, so re-checking a configured machine changes nothing.
  expect(preview.current).toBe(true);
  expect(preview.services[0]?.standing).toBe("installed");
  expect(preview.services[0]?.reason).toBe(
    `${MACHINE} advertises no credential named ${RESTIC_SERVICE.credentialRef}: write the ` +
      `token to ${RESTIC_SERVICE.credentialFile} on that machine, then check again`,
  );

  // Advertised, readable, and allowed for this origin: nothing left to say.
  const ready = hub({
    read: {
      configuration: { revision: REVISION, policies: [installed] },
      credentialReferences: [
        { ref: RESTIC_SERVICE.credentialRef, origins: [ORIGIN], available: true },
      ],
    },
  });
  const green = (await answer(ACTIONS.previewServices, ready.ctx, {
    machineId: MACHINE,
    origins: [],
  })) as unknown as { services: { standing: string; reason: string }[] };
  expect(green.services[0]).toMatchObject({ standing: "installed", reason: "" });

  // Advertised for somewhere else is a third state, and it is not silence either.
  const misdirected = hub({
    read: {
      configuration: { revision: REVISION, policies: [installed] },
      credentialReferences: [
        { ref: RESTIC_SERVICE.credentialRef, origins: [OTHER_ORIGIN], available: true },
      ],
    },
  });
  const amber = (await answer(ACTIONS.previewServices, misdirected.ctx, {
    machineId: MACHINE,
    origins: [],
  })) as unknown as { services: { reason: string; credential: { readable: boolean } }[] };
  expect(amber.services[0]?.credential.readable).toBe(false);
  expect(amber.services[0]?.reason).toContain("not allowed for this origin");
});

test("an install swaps on the revision it previewed and leaves every other policy alone", async () => {
  const fleet = hub({ read: { configuration: { revision: REVISION, policies: [FOREIGN] } } });
  const args = {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
  };
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, args)) as unknown as {
    previewDigest: string;
  };
  const installed = (await answer(ACTIONS.installServices, fleet.ctx, {
    ...args,
    expectedRevision: REVISION,
    previewDigest: preview.previewDigest,
  })) as unknown as { machineId: string; revision: string | null; services: unknown[] };
  expect(fleet.configures).toEqual([
    { machineId: MACHINE, expectedRevision: REVISION, policies: [FOREIGN, resticPolicy()] },
  ]);
  expect(installed.machineId).toBe(MACHINE);
  expect(installed.revision).toBe(MOVED);
  expect(installed.services).toEqual([
    { serviceId: FOREIGN.serviceId, revision: FOREIGN.revision },
    { serviceId: RESTIC_SERVICE.serviceId, revision: RESTIC_SERVICE.revision },
  ]);
});

test("a preview composed against a configuration that has since moved is refused, naming both", async () => {
  const fleet = hub({ read: { configuration: { revision: MOVED, policies: [FOREIGN] } } });
  const refused = await refusal(ACTIONS.installServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
    // What the operator read before somebody else installed something.
    expectedRevision: REVISION,
    previewDigest: digestOf({ machineId: MACHINE, expectedRevision: REVISION, policies: [] }),
  });
  expect(refused).toBe(
    `the preview was composed against ${REVISION} and ${MACHINE} now stands at ${MOVED}: ` +
      `read the preview again before installing`,
  );
  // AND NOTHING WAS WRITTEN. A refusal that had already called the host would be the overwrite
  // this refusal exists to prevent.
  expect(fleet.configures).toEqual([]);
});

test("an install that would compose something other than what was previewed is refused", async () => {
  // The configuration has not moved; the REQUEST has. An operator who previewed one endpoint
  // and pressed with another installed a policy nobody read.
  const fleet = hub({ read: { configuration: { revision: REVISION, policies: [FOREIGN] } } });
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
  })) as unknown as { previewDigest: string };
  const refused = await refusal(ACTIONS.installServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: OTHER_ORIGIN }],
    expectedRevision: REVISION,
    previewDigest: preview.previewDigest,
  });
  expect(refused).toBe(
    `the preview no longer composes what would be installed on ${MACHINE}: ` +
      `read the preview again before installing`,
  );
  expect(fleet.configures).toEqual([]);
});

test("a service nobody has named an endpoint for is previewed as itself and refuses the install", async () => {
  const fleet = hub({});
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [],
  })) as unknown as { previewDigest: string; services: { origin: string; reason: string }[] };
  expect(preview.services[0]?.origin).toBe("");
  expect(preview.services[0]?.reason).toBe(
    `name the endpoint ${RESTIC_SERVICE.serviceId} may reach`,
  );
  const refused = await refusal(ACTIONS.installServices, fleet.ctx, {
    machineId: MACHINE,
    origins: [],
    expectedRevision: null,
    previewDigest: preview.previewDigest,
  });
  expect(refused).toContain(`${RESTIC_SERVICE.serviceId} has no policy to install`);
  expect(fleet.configures).toEqual([]);
});

test("the hub losing the same race refuses the act rather than failing the plugin", async () => {
  // The engine runs the compare-and-swap again inside its own transaction. A caller that got
  // there between the read and the write is the host's refusal, and it comes back as one.
  const fleet = hub({
    read: { configuration: { revision: REVISION, policies: [] } },
    refusal: "service_configuration_changed",
  });
  const args = {
    machineId: MACHINE,
    origins: [{ serviceId: RESTIC_SERVICE.serviceId, origin: ORIGIN }],
  };
  const preview = (await answer(ACTIONS.previewServices, fleet.ctx, args)) as unknown as {
    previewDigest: string;
  };
  expect(
    await refusal(ACTIONS.installServices, fleet.ctx, {
      ...args,
      expectedRevision: REVISION,
      previewDigest: preview.previewDigest,
    }),
  ).toBe("service_configuration_changed");
});
