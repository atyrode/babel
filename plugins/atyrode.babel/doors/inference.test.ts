/*
  THE OWNER'S ACT ON ONE MACHINE'S SERVICE CONFIGURATION, held to what it does to that document.

  The store is real and the hub is fake, but the fake is not an echo: it parses everything it is
  handed through the SDK's own `ServiceConfigurationSchema` — so a policy this side assembles
  wrong is refused here as a hub would refuse it — and it answers the configuration back
  CANONICALLY, key-sorted by `canonicalJobJson`, which is the protocol's own digest encoding and
  how a hub really answers one. Both findings this file holds are invisible against a hub that
  hands the bytes back: one is a door comparing what it assembled against what it is answered,
  the other a refresh rewriting a table the operator, not Babel, owns.
*/

import { afterEach, beforeEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import {
  canonicalJobJson,
  ServiceConfigurationSchema,
  type ServiceConfiguration,
  type ServiceConfigurationRead,
  type ServicePolicy,
  type ServiceRuntimeCandidate,
} from "@manifold/protocol";
import { ACTIONS, INFERENCE_SERVICE } from "../contract.ts";
import type { Door } from "./door.ts";
import { inferenceDoors, INFERENCE_PRICES } from "./inference.ts";
import { openTestStore, type TestStore } from "../store/testdb.ts";

const NOW = Date.UTC(2026, 8, 14, 12, 0, 0);
const MACHINE = "m-dev-01";
/** A model the defaults price, and the price the OPERATOR puts in its place. */
const MODEL = "anthropic/claude-sonnet-5";
const NEGOTIATED = { inputPerMillion: 1_200_000, outputPerMillion: 6_000_000 };
/** A model Babel's table does not name at all, so its survival proves a carry and not a merge. */
const OWN_MODEL = "anthropic/claude-enterprise-1";
const OWN_PRICE = { inputPerMillion: 900_000, outputPerMillion: 4_500_000 };

/** omp's gateway as `readConfiguration` offers it: job-scoped, ready, with its three pins. */
const GATEWAY: ServiceRuntimeCandidate = {
  runtime: {
    pluginId: INFERENCE_SERVICE.gatewayPluginId,
    operationId: INFERENCE_SERVICE.gatewayOperationId,
    installationRevision: "rev-7",
    artifactSha256: "a".repeat(64),
    resourceBindingDigest: "b".repeat(64),
  },
  ready: true,
  reason: null,
};

/**
 * The machine's service configuration, as the hub keeps one: written only through the schema,
 * answered only canonically, and every write minting a revision over the bytes it stored — which
 * is the revision a deployment's `resourceBindings.services` is pinned at.
 */
class Hub {
  configuration: ServiceConfiguration = { revision: null, policies: [] };
  candidates: readonly ServiceRuntimeCandidate[] = [GATEWAY];
  writes = 0;

  readConfiguration(args: { machineId: string }): Promise<ServiceConfigurationRead> {
    if (args.machineId !== MACHINE) throw new Error(`no machine ${args.machineId}`);
    return Promise.resolve({
      configuration: this.configuration,
      connected: true,
      credentialReferences: [],
      runtimeCandidates: [...this.candidates],
    });
  }

  configureConfiguration(args: {
    machineId: string;
    expectedRevision: string | null;
    policies: ServicePolicy[];
  }): Promise<ServiceConfiguration> {
    if (args.expectedRevision !== this.configuration.revision) {
      throw new Error("the machine's service configuration has already moved");
    }
    const canonical = JSON.parse(canonicalJobJson(args.policies)) as unknown;
    const parsed = ServiceConfigurationSchema.safeParse({
      revision: createHash("sha256").update(canonicalJobJson(canonical)).digest("hex"),
      policies: canonical,
    });
    if (!parsed.success) {
      throw new Error(
        `service_policy_invalid: ${parsed.error.issues.map((issue) => issue.message).join("; ")}`,
      );
    }
    this.configuration = parsed.data;
    this.writes += 1;
    return Promise.resolve(parsed.data);
  }
}

let harness: TestStore;
let hub: Hub;
let doors: readonly Door[];
let ctx: GuestCtx;

/** What the kit does on one dispatch, minus the boundary it does it across. */
async function dispatch(name: string, args: unknown): Promise<Record<string, unknown>> {
  const found = doors.find((entry) => entry.action.name === name);
  if (found === undefined) throw new Error(`no door ${name}`);
  const parsed = found.action.input.safeParse(args);
  if (!parsed.success) {
    return { invalid: parsed.error.issues.map((issue) => issue.message).join("; ") };
  }
  const produced = await found.handler(ctx, parsed.data as never);
  if (typeof produced === "object" && produced !== null && "refused" in produced) {
    return produced as Record<string, unknown>;
  }
  const result = found.action.result.safeParse(produced);
  if (!result.success) {
    throw new Error(`${name} produced a result outside its schema: ${result.error.message}`);
  }
  return result.data as Record<string, unknown>;
}

/** The one policy under Babel's own service id, as the hub now holds it. */
function installed(): ServicePolicy {
  const found = hub.configuration.policies.find(
    (entry) => entry.serviceId === INFERENCE_SERVICE.serviceId,
  );
  if (found === undefined) throw new Error(`no ${INFERENCE_SERVICE.serviceId} policy is installed`);
  return found;
}

beforeEach(async () => {
  harness = await openTestStore(NOW);
  hub = new Hub();
  doors = inferenceDoors(harness.store, { now: () => NOW });
  ctx = {
    principal: { id: "operator" },
    auth: { isRoot: true, allows: () => Promise.resolve(true) },
    services: hub,
    now: () => NOW,
  } as unknown as GuestCtx;
});

afterEach(() => {
  harness.close();
});

test("installing twice writes once: the second act reads the hub's canonical answer and says unchanged", async () => {
  const first = await dispatch(ACTIONS.setupInference, { machineId: MACHINE, apply: true });
  expect(first["state"]).toBe("installed");
  expect(hub.writes).toBe(1);
  // A first install prices exactly the defaults, which is what makes the refresh below a carry.
  expect(installed().prices?.models).toEqual(INFERENCE_PRICES);
  const revision = hub.configuration.revision;
  expect(first["revision"]).toBe(revision);

  const again = { machineId: MACHINE, expectedServiceRevision: revision };
  // The preview is the sentence Watch shows before the button, and it must not offer a write.
  const previewed = await dispatch(ACTIONS.setupInference, again);
  expect(previewed["state"]).toBe("previewed");
  expect(previewed["note"]).toContain("already installed");

  const second = await dispatch(ACTIONS.setupInference, { ...again, apply: true });
  expect(second["state"]).toBe("unchanged");
  expect(second["revision"]).toBe(revision);
  // Nothing was written, so the digest a deployment is pinned at did not move.
  expect(hub.writes).toBe(1);
  expect(hub.configuration.revision).toBe(revision);
});

test("a refresh carries the operator's own price table and updates the gateway's pins", async () => {
  expect((await dispatch(ACTIONS.setupInference, { machineId: MACHINE, apply: true }))["state"]).toBe(
    "installed",
  );

  // The operator edits the installed table, as `plugins/README.md` says it is his to edit: his
  // negotiated rate for one model, and a model Babel's defaults do not name.
  const edited = await hub.configureConfiguration({
    machineId: MACHINE,
    expectedRevision: hub.configuration.revision,
    policies: [
      {
        ...installed(),
        prices: {
          models: { ...installed().prices?.models, [MODEL]: NEGOTIATED, [OWN_MODEL]: OWN_PRICE },
        },
      },
    ],
  });

  // The gateway is reinstalled with new pins, which is the whole reason a refresh exists.
  hub.candidates = [
    {
      ...GATEWAY,
      runtime: { ...GATEWAY.runtime, installationRevision: "rev-8", artifactSha256: "c".repeat(64) },
    },
  ];
  const refreshed = await dispatch(ACTIONS.setupInference, {
    machineId: MACHINE,
    expectedServiceRevision: edited.revision,
    apply: true,
  });

  expect(refreshed["state"]).toBe("refreshed");
  const policy = installed();
  expect(policy.runtime?.installationRevision).toBe("rev-8");
  expect(policy.runtime?.artifactSha256).toBe("c".repeat(64));
  // The operator's table survived the refresh verbatim: his price, his model, and no default put
  // back over either.
  expect(policy.prices?.models[MODEL]).toEqual(NEGOTIATED);
  expect(policy.prices?.models[OWN_MODEL]).toEqual(OWN_PRICE);
  expect(refreshed["models"]).toContain(OWN_MODEL);
  expect(policy.prices?.models).toEqual({
    ...INFERENCE_PRICES,
    [MODEL]: NEGOTIATED,
    [OWN_MODEL]: OWN_PRICE,
  });
});
