import { z } from "zod";
import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  canonicalJobJson,
  type InstanceServiceDescription,
  type ServiceConfigurationRead,
  type ServicePolicy,
  type ServiceReply,
} from "@manifold/protocol";
import {
  ACCOUNTS_SERVICE,
  ACTIONS,
  AccountsQuerySchema,
  AccountsResultSchema,
  INFERENCE_SERVICE,
  SetupInferenceInputSchema,
  SetupInferenceResultSchema,
  type AccountRow,
} from "../contract.ts";
import {
  buildInferencePolicy,
  inferenceRuntime,
  INFERENCE_PRICES,
  recordServiceSetup,
  type InferencePolicy,
} from "../server/inference.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE TWO DOORS THAT MAKE THE INFERENCE SERVICE USABLE WITHOUT A TEXT EDITOR (#279).

  `accounts` is a dry read: which accounts the machine's broker has observed, so the Start panel
  offers the operator one to spend instead of asking him to type an identity key he would have to
  find in another plugin's UI. It reads through Babel's OWN binding to the omp accounts broker's
  projected `metadata` operation — `ctx.services.readInstance`, the same call manifold-omp's
  `accountObservation` makes, and the same one Code makes for its own picker. Nothing about it is
  a cross-plugin door call, which Manifold does not have (`plugin-host.ts`: `ActionCtx` carries no
  way to reach another plugin's actions); an Instance Service is the seam, and this is it.

  `setupInference` is the owner's act: it assembles the `atyrode.babel.inference` policy against
  the omp gateway actually installed on that machine and compare-and-sets it into the machine's
  service configuration. It exists because the policy CANNOT be a document in a README: three of
  its fields are the pins of the gateway installation on that host, so only a live
  `readConfiguration` can produce them, and an operator retyping a 120-line JSON policy with two
  digests in it is the failure mode that document would be.

  BOTH ARE HONEST ABOUT WHAT THEY COULD NOT SEE. `accounts` answers `unavailable` with a reason
  rather than an empty list, because "no account is enrolled" and "nobody could be asked" send an
  operator in two different directions; `setupInference` previews before it writes, and reports
  the hub's own refusal by name when the write is refused.
*/

/** The owner's act on a machine's whole service configuration. Not governed; root-only, checked. */
const SETUP_CAPS = ["services:configure"] as const;

/** A dry read of one Instance Service. No machine is asked to run anything. */
const ACCOUNTS_CAPS = ["services:read"] as const;

/**
 * The broker's projected snapshot, as `metadata` answers it. It is the SUBSET
 * manifold-omp's `buildSharedBrokerPolicy` projects — ids, providers, identities, credential
 * type and email, and the blocks — restated loosely here because Babel reads it and does not own
 * it: a broker that starts projecting one more field must not fail Babel's picker.
 */
const ProjectedSnapshotSchema = z.looseObject({
  credentials: z
    .array(
      z.looseObject({
        id: z.number().int().positive(),
        provider: z.string().min(1),
        identityKey: z.string().nullable().optional(),
        credential: z
          .looseObject({
            type: z.string().default(""),
            email: z.string().nullable().optional(),
          })
          .optional(),
        disabled: z.boolean().optional(),
        blocks: z
          .array(z.looseObject({ blockScope: z.string().default(""), blockedUntilMs: z.number() }))
          .optional(),
      }),
    )
    .max(1024),
});
export interface InferenceDeps {
  now(): number;
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/** The broker reference a read was taken at; every slot of one pool must agree on its scope. */
interface BrokerReference {
  readonly revision: string;
  readonly machineId: string;
}

export function inferenceDoors(store: BabelStore, _deps: InferenceDeps): readonly Door[] {
  /** The broker's reference, or the sentence naming why it cannot be read. */
  async function broker(ctx: GuestCtx): Promise<BrokerReference | { unavailable: string }> {
    let described: InstanceServiceDescription;
    try {
      described = await ctx.services.describeInstance({ serviceId: ACCOUNTS_SERVICE.serviceId });
    } catch (error) {
      return {
        unavailable:
          `this hub could not describe ${ACCOUNTS_SERVICE.serviceId}: ${message(error)}`,
      };
    }
    const configuration = described.configuration;
    if (configuration === null || !configuration.enabled) {
      return {
        unavailable:
          `${ACCOUNTS_SERVICE.serviceId} is not installed on this hub, so no account can be ` +
          `offered; enrol one through the omp accounts plugin`,
      };
    }
    if (described.owner === null || !described.owner.online || !described.connected) {
      return { unavailable: `${ACCOUNTS_SERVICE.serviceId} has no online owner` };
    }
    if (described.state !== "ready") {
      return {
        unavailable:
          `${ACCOUNTS_SERVICE.serviceId} is ${described.state}${
            described.reason === null ? "" : `: ${described.reason}`
          }`,
      };
    }
    return { revision: configuration.revision, machineId: described.owner.machineId };
  }

  const accounts = defineDoor(
    defineServerAction({
      name: ACTIONS.accounts,
      title: "Read the accounts a run may spend",
      caps: ACCOUNTS_CAPS,
      input: AccountsQuerySchema,
      result: AccountsResultSchema,
    }),
    async (ctx, _input) => {
      const reference = await broker(ctx);
      if ("unavailable" in reference) return { accounts: [], unavailable: reference.unavailable };
      let reply: ServiceReply;
      try {
        reply = await ctx.services.readInstance({
          serviceId: ACCOUNTS_SERVICE.serviceId,
          expectedRevision: reference.revision,
          operationId: ACCOUNTS_SERVICE.metadataOperationId,
          input: {},
        });
      } catch (error) {
        return {
          accounts: [],
          unavailable: `${ACCOUNTS_SERVICE.serviceId} refused the read: ${message(error)}`,
        };
      }
      if (!reply.ok) {
        return {
          accounts: [],
          unavailable: `${ACCOUNTS_SERVICE.serviceId} refused the read: ${reply.refusal}`,
        };
      }
      const parsed = ProjectedSnapshotSchema.safeParse(reply.result);
      if (!parsed.success) {
        return {
          accounts: [],
          unavailable:
            `${ACCOUNTS_SERVICE.serviceId} answered a snapshot this build does not read: ` +
            parsed.error.issues.map((issue) => issue.message).join("; "),
        };
      }
      /*
        THE OBSERVATION SCOPE every slot of one pool must agree on.

        manifold-omp derives it as the sha256 of the broker reference (`broker.ts`
        `accountObservation` calls `projectAccounts(metadata, digestOf(reference), …)`), and the
        gateway worker checks only that every slot in ONE pool carries the SAME scope
        (`plugins/workers/gateway/inputs.ts`: a differing scope is `gateway_unavailable`, and no
        canonical value is compared against). So a scope is an opaque tag naming the observation
        a pool was frozen from, and Babel names it in a form a reader can act on rather than as a
        digest nothing can be looked up by: the pool is internally consistent, which is the whole
        of what is enforced, and an operator reading a receipt can see WHICH broker revision the
        account was observed at.
      */
      const scope = `${ACCOUNTS_SERVICE.serviceId}@${reference.revision}/${reference.machineId}`;
      const now = ctx.now();
      const rows: AccountRow[] = parsed.data.credentials.map((row) => {
        const blocked = (row.blocks ?? []).some((block) => block.blockedUntilMs > now);
        return {
          provider: row.provider,
          scope,
          credentialId: String(row.id),
          identityKey: row.identityKey ?? "",
          label: row.credential?.email ?? "",
          disabled: (row.disabled ?? false) || blocked,
        };
      });
      return { accounts: rows, unavailable: "" };
    },
  );

  const setup = defineDoor(
    defineServerAction({
      name: ACTIONS.setupInference,
      title: "Install the inference service on a machine",
      caps: SETUP_CAPS,
      input: SetupInferenceInputSchema,
      result: SetupInferenceResultSchema,
    }),
    async (ctx, input) => {
      // The owner's act, and only the owner's: `readConfiguration` and `configureConfiguration`
      // are admitted to a root caller holding `services:configure` AT THE MACHINE, so the door
      // asks the same question rather than discovering the refusal halfway through a write.
      if (
        !ctx.auth.isRoot ||
        !(await ctx.auth.allows("services:configure", {
          kind: "machine",
          machineId: input.machineId,
        }))
      ) {
        return {
          refused:
            `installing ${INFERENCE_SERVICE.serviceId} is the machine owner's act: it needs ` +
            `services:configure at ${input.machineId}`,
        };
      }
      let read: ServiceConfigurationRead;
      try {
        read = await ctx.services.readConfiguration({ machineId: input.machineId });
      } catch (error) {
        return {
          refused: `${input.machineId}'s service configuration could not be read: ${message(error)}`,
        };
      }
      if (!read.connected) return { refused: `${input.machineId} is offline` };
      if (read.configuration.revision !== input.expectedServiceRevision) {
        return {
          refused:
            `${input.machineId}'s service configuration is at ` +
            `${read.configuration.revision ?? "none"} and this act was written against ` +
            `${input.expectedServiceRevision ?? "none"}: read it again`,
        };
      }
      // The gateway that will SERVE this policy has to be the one installed here, at the
      // revision and the artifact installed here: those three pins are the policy's runtime and
      // cannot be written from a repository. An instance-scoped candidate is refused by name,
      // because an instance runtime may hold only literal inputs — so it could not carry the
      // job's account pool, which is the whole mechanism (#267).
      const candidates = read.runtimeCandidates.filter(
        (candidate) =>
          candidate.runtime.pluginId === INFERENCE_SERVICE.gatewayPluginId &&
          candidate.runtime.operationId === INFERENCE_SERVICE.gatewayOperationId,
      );
      const usable = candidates.filter(
        (candidate) => candidate.runtime.scope !== "instance" && candidate.ready,
      );
      const chosen = usable[0];
      if (chosen === undefined) {
        const named = candidates[0];
        return {
          refused:
            candidates.length === 0
              ? `${input.machineId} has no ${INFERENCE_SERVICE.gatewayPluginId} installed, so ` +
                `there is no gateway for ${INFERENCE_SERVICE.serviceId} to run on`
              : `${INFERENCE_SERVICE.gatewayOperationId} on ${input.machineId} is not ready to ` +
                `serve a job-scoped service${named?.reason === null || named?.reason === undefined ? "" : `: ${named.reason}`}`,
        };
      }
      const existing = read.configuration.policies.find(
        (entry) => entry.serviceId === INFERENCE_SERVICE.serviceId,
      );
      /*
        THE PRICE TABLE IS THE OPERATOR'S, AND A REFRESH DOES NOT TAKE IT BACK (#284).

        `INFERENCE_PRICES` is a DEFAULT, so that installing the service does not mean retyping a
        table, and `plugins/README.md` tells the operator the installed one is his to edit — an
        enterprise rate, a batch discount, a model this list does not name. A refresh exists for
        the RUNTIME PINS (a reinstalled gateway, a new artifact sha), so it carries the installed
        table through verbatim: reinstating the defaults over it would reprice every run behind
        the owner's back, and it would move the policy digest that `resourceBindings.services`
        pins at deployment — a re-review of Babel's whole machine half for a price nobody changed.
      */
      const built = buildInferencePolicy(inferenceRuntime(chosen.runtime));
      const policy: InferencePolicy =
        existing?.prices === undefined ? built : { ...built, prices: existing.prices };
      const models = Object.keys(policy.prices.models);
      /*
        COMPARED CANONICALLY, because the hub does not answer the bytes it was handed (#284): a
        policy read back is key-sorted (`canonicalJobJson` is the protocol's own digest encoding,
        and the configuration revision is taken over it), so `JSON.stringify` against the
        document this side assembles matched nothing after the first install — every refresh said
        `refreshed`, wrote a configuration revision nobody needed, and every written revision is
        another deployment re-review.
      */
      const unchanged =
        existing !== undefined && canonicalJobJson(existing) === canonicalJobJson(policy);
      if (!input.apply) {
        return {
          serviceId: INFERENCE_SERVICE.serviceId,
          revision: null,
          state: "previewed" as const,
          models,
          note: unchanged
            ? `${INFERENCE_SERVICE.serviceId} is already installed exactly as this would install it`
            : `this would ${existing === undefined ? "install" : "refresh"} ` +
              `${INFERENCE_SERVICE.serviceId} on ${input.machineId}, pricing ` +
              `${String(models.length)} models and metering every call`,
        };
      }
      if (unchanged) {
        return {
          serviceId: INFERENCE_SERVICE.serviceId,
          revision: read.configuration.revision,
          state: "unchanged" as const,
          models,
          note: `${INFERENCE_SERVICE.serviceId} was already installed exactly so; nothing was written`,
        };
      }
      // EVERY UNRELATED POLICY SURVIVES. The machine's configuration is one document holding
      // every service its owner installed — restic's, omp's gateway, Code's classifier — so this
      // replaces exactly the one entry under Babel's own service id and rewrites nothing else.
      // It is `ServicePolicy[]` and not a cast: `MANIFOLD_REV` 0bc76660 carries the
      // `pi-native-usage` meter kind (manifold#570, landed as #572), so the hub's own schema is
      // what typechecks the document this side assembled. A HUB older than the pin is a
      // different fact and refuses the write below, by name.
      const policies: ServicePolicy[] = [
        ...read.configuration.policies.filter(
          (entry) => entry.serviceId !== INFERENCE_SERVICE.serviceId,
        ),
        policy,
      ];
      let revision: string | null;
      try {
        const configured = await ctx.services.configureConfiguration({
          machineId: input.machineId,
          expectedRevision: input.expectedServiceRevision,
          policies,
        });
        revision = configured.revision;
      } catch (error) {
        // WHAT THE HUB ANSWERED IS KEPT (#284), because the write left nothing behind and the
        // launch preview cannot otherwise tell this machine from one nobody has set up. A hub
        // that refused the meter kind is a hub older than this plugin, and the preview says so
        // by name (`unsupported`) with this sentence as its evidence.
        const refusal = message(error);
        await recordServiceSetup(store, input.machineId, INFERENCE_SERVICE.serviceId, {
          state: "refused",
          detail: refusal,
        });
        return {
          refused: `${input.machineId} refused the ${INFERENCE_SERVICE.serviceId} policy: ${refusal}`,
        };
      }
      await recordServiceSetup(store, input.machineId, INFERENCE_SERVICE.serviceId, {
        state: "installed",
        detail: "",
      });
      return {
        serviceId: INFERENCE_SERVICE.serviceId,
        revision,
        state: (existing === undefined ? "installed" : "refreshed") as "installed" | "refreshed",
        models,
        note:
          `${INFERENCE_SERVICE.serviceId} is installed on ${input.machineId} at revision ` +
          `${revision ?? "none"}, metering ${INFERENCE_SERVICE.streamPath} and pricing ` +
          `${String(models.length)} models`,
      };
    },
  );

  return [accounts, setup];
}

/** Re-exported so a test can assert the default price table without reaching into the server. */
export { INFERENCE_PRICES, type InferencePolicy };
