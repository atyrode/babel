import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  JobRequestSchema,
  ServicePolicySchema,
  type ServicePolicy,
  type ServiceRuntime,
} from "@manifold/protocol";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  MACHINE_OPERATIONS,
  RECALL_MAX_REQUEST_BYTES,
  RECALL_MAX_REQUEST_BODY_BYTES,
  RECALL_MAX_RESULT_BYTES,
  RECALL_RESULT_FIELDS,
  RECALL_SERVICE_ID,
  RECALL_SERVICE_REVISION,
  RecallInstallInputSchema,
  RecallInstalledSchema,
  RecallPolicySchema,
  RecallSetupInputSchema,
  RecallSetupPreviewSchema,
  RecallRuntimeInputSchema,
  type RecallPolicy,
} from "../contract.ts";
import { defineDoor, type Door } from "./door.ts";
import { digestOf } from "./services.ts";

export type RecallRuntime = Pick<
  ServiceRuntime,
  "installationRevision" | "artifactSha256" | "resourceBindingDigest"
>;

/** Only the owner supplies policy. Runtime inputs and class routes never interpolate caller data. */
export function composeRecallServicePolicy(policy: RecallPolicy, runtime: RecallRuntime): ServicePolicy {
  try {
    const parsed = RecallPolicySchema.parse(policy);
    const input = JobRequestSchema.shape.input.parse({
      [INPUT_FIELD]: JSON.stringify(RecallRuntimeInputSchema.parse({ policy: parsed })),
    });
    return ServicePolicySchema.parse({
      serviceId: RECALL_SERVICE_ID,
      revision: RECALL_SERVICE_REVISION,
      runtime: {
        scope: "instance",
        pluginId: BABEL_PLUGIN_ID,
        operationId: MACHINE_OPERATIONS.recall,
        installationRevision: runtime.installationRevision,
        artifactSha256: runtime.artifactSha256,
        resourceBindingDigest: runtime.resourceBindingDigest,
        input: { [INPUT_FIELD]: { literal: input[INPUT_FIELD] } },
      },
      maxConcurrent: 4,
      operations: Object.fromEntries(parsed.classes.map(({ id }) => [id, {
        method: "POST",
        invocable: true,
        path: `/recall/${id}`,
        input: { request: { type: "string", required: true, maxBytes: RECALL_MAX_REQUEST_BYTES } },
        query: {},
        body: [{ path: ["request"], value: { input: "request" } }],
        timeoutMs: 30_000,
        maxRequestBytes: RECALL_MAX_REQUEST_BODY_BYTES,
        maxResponseBytes: RECALL_MAX_RESULT_BYTES,
        maxResultBytes: RECALL_MAX_RESULT_BYTES,
        response: {
          kind: "projected-json",
          fields: RECALL_RESULT_FIELDS,
          maxArrayItems: 256,
        },
      }])),
    });
  } catch {
    // Zod errors may quote owner metadata or static literals. Never expose their details.
    throw new Error("Recall policy cannot be represented within the native service bounds.");
  }
}

async function composePreview(
  ctx: GuestCtx,
  args: { machineId: string; policy: RecallPolicy },
) {
  const [described, native, installed] = await Promise.all([
    ctx.jobs.describe({ machineId: args.machineId, pluginId: BABEL_PLUGIN_ID }),
    ctx.services.readConfiguration({ machineId: args.machineId }),
    ctx.services.readInstanceConfiguration({ serviceId: RECALL_SERVICE_ID }),
  ]);
  const installation = described.installation;
  const operation = described.operations?.[MACHINE_OPERATIONS.recall];
  const runtime: RecallRuntime | null = installation && operation ? {
    installationRevision: installation.revision,
    artifactSha256: installation.artifactSha256,
    resourceBindingDigest: operation.resourceBindingDigest,
  } : null;
  const candidate = runtime === null ? undefined : native.runtimeCandidates.find(({ runtime: found }) =>
    found.pluginId === BABEL_PLUGIN_ID &&
    found.operationId === MACHINE_OPERATIONS.recall &&
    found.installationRevision === runtime.installationRevision &&
    found.artifactSha256 === runtime.artifactSha256 &&
    found.resourceBindingDigest === runtime.resourceBindingDigest,
  );
  let reason = "";
  let policy: ServicePolicy | null = null;
  if (described.machineId !== args.machineId || described.pluginId !== BABEL_PLUGIN_ID) {
    reason = "The native runtime description does not identify the selected Babel installation.";
  } else if (!described.connected || !native.connected) {
    reason = "The selected native owner is offline.";
  } else if (!installation || !installation.enabled || !installation.ready || installation.purgeRequested) {
    reason = "The selected Babel installation is absent or unavailable.";
  } else if (!runtime || !operation?.ready || !candidate?.ready) {
    reason = "The selected installation does not provide a ready Recall service runtime.";
  } else {
    try {
      policy = composeRecallServicePolicy(args.policy, runtime);
    } catch {
      reason = "Recall policy cannot be represented within the native service bounds.";
    }
  }
  const description = installed.description;
  const expectedRevision = description.configuration?.revision ?? null;
  const configuration = {
    serviceId: RECALL_SERVICE_ID,
    machineId: args.machineId,
    expectedRevision,
    enabled: true,
    policy,
  };
  const changed = policy === null ||
    description.owner?.machineId !== args.machineId ||
    description.configuration?.enabled !== true ||
    description.configuration.pluginId !== BABEL_PLUGIN_ID ||
    digestOf(installed.policy) !== digestOf(policy);
  return {
    policy,
    preview: {
      machineId: args.machineId,
      expectedRevision,
      // The tuple is explicit even when the runtime is unavailable and no policy composes.
      previewDigest: digestOf({ configuration, runtime, ownerPolicy: args.policy }),
      ready: policy !== null,
      reason,
      changed,
      classes: args.policy.classes.map(({ id }) => ({
        id,
        target: {
          kind: "service" as const,
          machineId: args.machineId,
          serviceId: RECALL_SERVICE_ID,
          operationId: id,
        },
      })),
    },
  };
}

// A delegate is the native ceiling, not the caller's grant. The host checks root again.
const DELEGATES = ["services:configure", "machines:read"] as const;
const previewRecallAction = defineServerAction({
  name: ACTIONS.previewRecall,
  title: "Preview owner-installed Recall disclosure classes",
  caps: [],
  delegates: DELEGATES,
  trace: "opaque",
  input: RecallSetupInputSchema,
  result: RecallSetupPreviewSchema,
});
const installRecallAction = defineServerAction({
  name: ACTIONS.installRecall,
  title: "Install the exact previewed owner Recall policy",
  caps: [],
  delegates: DELEGATES,
  trace: "opaque",
  input: RecallInstallInputSchema,
  result: RecallInstalledSchema,
});

export function recallServiceDoors(): readonly Door[] {
  return [
    defineDoor(previewRecallAction, async (ctx, args) => {
      if (!ctx.auth.isRoot) return { refused: "Recall configuration requires the owner." };
      try {
        return (await composePreview(ctx, args)).preview;
      } catch {
        return { refused: "Recall configuration could not be read." };
      }
    }),
    defineDoor(installRecallAction, async (ctx, args) => {
      if (!ctx.auth.isRoot) return { refused: "Recall configuration requires the owner." };
      try {
        // No cached preview is trusted: re-read the live tuple and service revision immediately
        // before the native compare-and-swap, which rechecks both the revision and runtime tuple.
        const { preview, policy } = await composePreview(ctx, args);
        if (preview.expectedRevision !== args.expectedRevision || preview.previewDigest !== args.previewDigest) {
          return { refused: "Recall configuration changed; preview it again before installing." };
        }
        if (!preview.ready || policy === null) return { refused: preview.reason };
        if (!preview.changed) {
          return { serviceId: RECALL_SERVICE_ID, revision: preview.expectedRevision, installed: false, reason: "" };
        }
        const configured = await ctx.services.configureInstance({
          serviceId: RECALL_SERVICE_ID,
          expectedRevision: args.expectedRevision,
          machineId: args.machineId,
          policy,
          enabled: true,
        });
        return {
          serviceId: RECALL_SERVICE_ID,
          revision: configured.configuration?.revision ?? null,
          installed: true,
          reason: "",
        };
      } catch {
        return { refused: "Recall configuration could not be installed; preview it again before retrying." };
      }
    }),
  ];
}
