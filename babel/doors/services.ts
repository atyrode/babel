import { createHash } from "node:crypto";
import { defineServerAction, type GuestServices } from "@manifold/plugin-kit/server";
import {
  canonicalJobJson,
  ServicePolicySchema,
  type PluginManifest,
  type ServiceConfigurationRead,
  type ServicePolicy,
  type ServiceProxyOperationPolicy,
} from "@manifold/protocol";
import {
  ACTIONS,
  InstallServicesInputSchema,
  PreviewServicesInputSchema,
  RESTIC_SERVICE,
  ServicesInstalledSchema,
  ServicesPreviewSchema,
  type ServicePreview,
  type ServiceStanding,
  type ServicesPreview,
} from "../contract.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE DOORS A SERVICE POLICY IS INSTALLED THROUGH (#400).

  Babel binds one host service today — `atyrode.babel.restic`, named by the `archive` and
  `verify` operations — and installing its policy was a hand-written owner call to
  `engine.services.configureConfiguration` with its arguments spelled out in prose in
  `docs/runbook.md` §4. A procedure living in a document is the shape this repository keeps
  deciding is not good enough: an archive whose restore path lives only in an operator's head is
  an archive nobody has tested (#338), and a policy whose install lives only in a runbook is a
  policy nobody can verify they installed correctly.

  THE POLICY IS COMPOSED FROM THE MANIFEST, never typed twice. `serviceId`, `revision` and the
  operation ids come out of the `services` block the operations already declare, so the two
  halves of a binding cannot drift: editing the manifest's revision changes what this composes,
  and an operation id the manifest binds that this bundle calls no route for is refused by name
  rather than installed as a policy no job can use. The one thing it cannot know is the ORIGIN —
  where this deployment's store lives is provisioning, not a fact about Babel — so that is the
  request's, and a service already configured keeps the origin it has.

  PREVIEW, DIGEST, COMPARE-AND-SWAP, which is `atyrode.code`'s `service-setup.ts` three-step, and
  it has that shape because a policy installed from a stale preview is a policy the operator did
  not read. The digest covers the machine, the configuration revision the preview was composed
  against, and the policies themselves — so both ways a preview goes stale, the configuration
  moving underneath and the composition changing, fall out of one comparison.

  NO CREDENTIAL VALUE PASSES THROUGH HERE, and not as a discipline: there is no field for one in
  the input schemas, none in `ServicePolicySchema`, and none in the host call's arguments. The
  machine's own agent opens `serviceCredentials[<ref>].source` off its disk, and the protocol
  advertises "references and allowed origins, never source paths or values" — so no hub and no
  plugin anywhere can hand a person a key (atyrode/manifold#768). What a panel CAN do is name the
  file, and `RESTIC_SERVICE.credentialFile` is this deployment's own convention, stated so the
  operator has somewhere to write rather than the silence an unpaid account also produces.

  OWNER ONLY, WITH NO CAPABILITY THAT SAYS SO. `engine.services` admits a configuration read or
  write only from the hub's owner holding `services:configure` on that machine, and no cap in
  Manifold's closed vocabulary means "the owner" — so these carry `services:configure` as a
  DELEGATE, which is the native-API ceiling, and ask `ctx.auth.isRoot` themselves exactly as the
  crossing's two doors do. The host enforces it again regardless; what the check here buys is a
  sentence naming why, instead of a bare `service_unauthorized`.
*/

// ---------------------------------------------------------------------------- what is declared

/** One service the manifest's operations bind, folded across every operation that binds it. */
export interface DeclaredService {
  readonly serviceId: string;
  readonly revision: string;
  readonly operationIds: readonly string[];
}

/**
 * The services this bundle's machine half binds, read off the manifest it ships.
 *
 * Two operations binding one service at two revisions is a manifest that cannot be installed —
 * the hub fingerprints one policy per service, not one per operation — so it raises at load,
 * beside the duplicate-door check, rather than composing something no job could bind.
 */
export function declaredServices(manifest: PluginManifest): readonly DeclaredService[] {
  const found = new Map<string, { revision: string; operationIds: Set<string> }>();
  for (const [operationId, operation] of Object.entries(manifest.machine?.operations ?? {})) {
    for (const binding of operation.services ?? []) {
      const held = found.get(binding.serviceId);
      if (held === undefined) {
        found.set(binding.serviceId, {
          revision: binding.revision,
          operationIds: new Set(binding.operationIds),
        });
        continue;
      }
      if (held.revision !== binding.revision) {
        throw new Error(
          `${operationId} binds ${binding.serviceId} at revision ${binding.revision}, ` +
            `and another operation binds it at ${held.revision}`,
        );
      }
      for (const id of binding.operationIds) held.operationIds.add(id);
    }
  }
  return [...found].map(([serviceId, held]) => ({
    serviceId,
    revision: held.revision,
    operationIds: [...held.operationIds].sort(),
  }));
}

// ---------------------------------------------------------------------------- what is composed

/** The half of a credential a repository may hold: two names, and never a value. */
interface CredentialNames {
  readonly ref: string;
  readonly file: string;
}

/** What this bundle knows about a service it binds: the key it names, and the routes it calls. */
interface ServiceKnowledge {
  readonly credential: CredentialNames;
  /** The header the owner writes the resolved value into, and what it prefixes it with. */
  readonly header: string;
  readonly prefix: "" | "Bearer " | "Basic ";
  /** How many of this service's calls one machine may have in flight at once. */
  readonly maxConcurrent: number;
  /** One route per operation id the policy may declare, keyed as the manifest's binding names. */
  readonly routes: Readonly<Record<string, ServiceProxyOperationPolicy>>;
}

/**
 * THE STORAGE ROUTE, as `machine/restic.ts` calls it: one GET for one JSON document.
 *
 * It is a PROXY operation because the caller is a job — an operation of kind `http-proxy` is
 * refused `service_unauthorized` to a direct invoker — and a job reaches it by method and path
 * through the loopback proxy the engine opens for it. Both come from `RESTIC_SERVICE` rather
 * than being spelled a second time here.
 *
 * The two bounds sit UNDER the machine half's own, deliberately. `machine/restic.ts` aborts its
 * fetch at 15 s and refuses a body over 64 KiB; a proxy that gives up first names the failure as
 * a status the job can report, where an abort at the client is a bare network error. A storage
 * document is four short strings, so neither bound is anywhere near a real one.
 */
const STORAGE_ROUTE: ServiceProxyOperationPolicy = {
  kind: "http-proxy",
  method: "GET",
  path: RESTIC_SERVICE.path,
  request: { kind: "none" },
  response: { kind: "stream", disclosure: "full", contentTypes: ["application/json"], headers: [] },
  timeoutMs: 10_000,
  maxRequestBytes: 1024,
  maxResponseBytes: 64 << 10,
};

const KNOWN: Readonly<Record<string, ServiceKnowledge>> = {
  [RESTIC_SERVICE.serviceId]: {
    credential: { ref: RESTIC_SERVICE.credentialRef, file: RESTIC_SERVICE.credentialFile },
    header: "Authorization",
    prefix: "Bearer ",
    // Two: the operations that bind it ask once each, and a verification may follow an archive
    // on the same machine.
    maxConcurrent: 2,
    routes: { [RESTIC_SERVICE.operationId]: STORAGE_ROUTE },
  },
};

/** A composition: the policy, or the sentence saying why there is none to install. */
export type Composition =
  | { readonly policy: ServicePolicy; readonly reason: "" }
  | { readonly policy: null; readonly reason: string };

export function composePolicy(declared: DeclaredService, origin: string): Composition {
  const known = KNOWN[declared.serviceId];
  if (known === undefined) {
    return {
      policy: null,
      reason:
        `the manifest binds ${declared.serviceId} and this bundle knows no policy for it, ` +
        `so none can be composed here`,
    };
  }
  const operations: Record<string, ServiceProxyOperationPolicy> = {};
  const unrouted: string[] = [];
  for (const id of declared.operationIds) {
    const route = known.routes[id];
    if (route === undefined) unrouted.push(id);
    else operations[id] = route;
  }
  if (unrouted.length > 0) {
    return {
      policy: null,
      reason:
        `the manifest binds ${declared.serviceId}/${unrouted.join(", ")} and this bundle calls ` +
        `no such route, so the binding and the policy disagree`,
    };
  }
  if (origin === "") {
    return { policy: null, reason: `name the endpoint ${declared.serviceId} may reach` };
  }
  return {
    policy: ServicePolicySchema.parse({
      serviceId: declared.serviceId,
      revision: declared.revision,
      origin,
      allowLoopbackHttp: false,
      credential: { ref: known.credential.ref, header: known.header, prefix: known.prefix },
      maxConcurrent: known.maxConcurrent,
      operations,
    }),
    reason: "",
  };
}

// ---------------------------------------------------------------------------- the preview

/**
 * The hub's own fingerprint of a policy: sha256 over the protocol's canonical JSON. It is that
 * function and not `JSON.stringify` because the engine digests a configuration the same way, so
 * "installed" here means byte-identical to what the hub would compute.
 */
export function digestOf(value: unknown): string {
  return createHash("sha256").update(canonicalJobJson(value)).digest("hex");
}

function standingOf(
  composed: ServicePolicy | null,
  installed: ServicePolicy | undefined,
): ServiceStanding {
  if (installed === undefined) return "absent";
  if (composed !== null && digestOf(composed) === digestOf(installed)) return "installed";
  return "different";
}

/**
 * WHY A JOB BINDING THIS WOULD NOT ADMIT, in the order an operator can act on.
 *
 * The engine's own `serviceAvailability` asks the same questions of the machine's advertised
 * resources and answers them with one word (`service_credential_unavailable`); this answers them
 * with the remedy, because *not configured* and *configured and its key is not there* are the two
 * states that produce identical silence today, and they have entirely different fixes.
 */
function reasonFor(options: {
  readonly machineId: string;
  readonly connected: boolean;
  readonly composed: Composition;
  readonly standing: ServiceStanding;
  readonly serviceId: string;
  readonly credential: CredentialNames;
  readonly advertised: boolean;
  readonly readable: boolean;
}): string {
  const { machineId, serviceId, credential } = options;
  if (!options.connected) {
    return `the hub cannot reach ${machineId}, so nothing about its services is known yet`;
  }
  if (options.composed.reason !== "") return options.composed.reason;
  if (options.standing === "absent") {
    return `no policy is installed under ${serviceId} on ${machineId}`;
  }
  if (options.standing === "different") {
    return `a different policy is installed under ${serviceId} on ${machineId}`;
  }
  if (!options.advertised) {
    return (
      `${machineId} advertises no credential named ${credential.ref}: write the token to ` +
      `${credential.file} on that machine, then check again`
    );
  }
  if (!options.readable) {
    return (
      `${machineId} advertises ${credential.ref} and cannot use it here: the agent could not ` +
      `read ${credential.file}, or it is not allowed for this origin`
    );
  }
  return "";
}

export interface PreviewOutcome {
  readonly preview: ServicesPreview;
  /** The whole configuration as it would stand, every unrelated policy kept. */
  readonly policies: readonly ServicePolicy[];
  /** Every declared service that composed to nothing; an install naming one is refused. */
  readonly uncomposed: readonly string[];
}

export async function composePreview(
  services: Pick<GuestServices, "readConfiguration">,
  declared: readonly DeclaredService[],
  args: {
    readonly machineId: string;
    readonly origins: readonly { readonly serviceId: string; readonly origin: string }[];
  },
): Promise<PreviewOutcome> {
  const read: ServiceConfigurationRead = await services.readConfiguration({
    machineId: args.machineId,
  });
  const rows: ServicePreview[] = [];
  const composedById = new Map<string, ServicePolicy>();
  const uncomposed: string[] = [];
  for (const service of declared) {
    const installed = read.configuration.policies.find(
      (policy) => policy.serviceId === service.serviceId,
    );
    // The request's origin, else the one already installed: re-checking a configured machine
    // needs nothing typed, and a machine with neither says so rather than composing a policy
    // pointed at a guess.
    const asked = args.origins.find((entry) => entry.serviceId === service.serviceId);
    const composed = composePolicy(service, asked?.origin ?? installed?.origin ?? "");
    if (composed.policy === null) uncomposed.push(service.serviceId);
    else composedById.set(service.serviceId, composed.policy);
    const credential = KNOWN[service.serviceId]?.credential ?? { ref: "", file: "" };
    const source = read.credentialReferences.find((entry) => entry.ref === credential.ref);
    const origin = composed.policy?.origin ?? installed?.origin ?? "";
    const standing = standingOf(composed.policy, installed);
    const advertised = source !== undefined;
    const readable = source?.available === true && origin !== "" && source.origins.includes(origin);
    rows.push({
      serviceId: service.serviceId,
      revision: service.revision,
      origin,
      operations: [...service.operationIds],
      credential: { ref: credential.ref, file: credential.file, advertised, readable },
      standing,
      reason: reasonFor({
        machineId: args.machineId,
        connected: read.connected,
        composed,
        standing,
        serviceId: service.serviceId,
        credential,
        advertised,
        readable,
      }),
    });
  }
  // EVERY UNRELATED POLICY SURVIVES. A machine's configuration is one document shared by every
  // plugin that installs into it, so this replaces Babel's own entries and copies the rest
  // through untouched — and a service Babel declares that composed to nothing keeps whatever is
  // installed rather than being dropped by the swap.
  const policies = read.configuration.policies.map(
    (policy) => composedById.get(policy.serviceId) ?? policy,
  );
  for (const [serviceId, policy] of composedById) {
    if (!read.configuration.policies.some((existing) => existing.serviceId === serviceId)) {
      policies.push(policy);
    }
  }
  const expectedRevision = read.configuration.revision;
  return {
    preview: {
      machineId: args.machineId,
      connected: read.connected,
      expectedRevision,
      services: rows,
      previewDigest: digestOf({ machineId: args.machineId, expectedRevision, policies }),
      current: digestOf(policies) === digestOf(read.configuration.policies),
    },
    policies,
    uncomposed,
  };
}

// ---------------------------------------------------------------------------- the doors

/**
 * `services:configure` is a DELEGATE rather than a cap: it is the native ceiling these two need
 * to reach `engine.services` at all, and never the caller's permission. The caller's permission
 * is the owner check inside each handler, and the host's own on top of it.
 */
const SERVICE_DELEGATES = ["services:configure"] as const;

const previewServicesAction = defineServerAction({
  name: ACTIONS.previewServices,
  title:
    "Compose the service policies this bundle binds, and say whether they came up (owner only)",
  caps: [],
  delegates: SERVICE_DELEGATES,
  input: PreviewServicesInputSchema,
  result: ServicesPreviewSchema,
});

const installServicesAction = defineServerAction({
  name: ACTIONS.installServices,
  title:
    "Install the previewed service policies against the revision they were read at (owner only)",
  caps: [],
  delegates: SERVICE_DELEGATES,
  input: InstallServicesInputSchema,
  result: ServicesInstalledSchema,
});

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function serviceDoors(declared: readonly DeclaredService[]): readonly Door[] {
  return [
    defineDoor(previewServicesAction, async (ctx, args) => {
      if (!ctx.auth.isRoot) {
        return {
          refused:
            "reading this machine's service configuration is the owner's act; this principal is not the owner",
        };
      }
      try {
        return (await composePreview(ctx.services, declared, args)).preview;
      } catch (error) {
        return { refused: message(error) };
      }
    }),

    defineDoor(installServicesAction, async (ctx, args) => {
      if (!ctx.auth.isRoot) {
        return {
          refused:
            "installing a service policy is the owner's act; this principal is not the owner",
        };
      }
      let composed: PreviewOutcome;
      try {
        composed = await composePreview(ctx.services, declared, args);
      } catch (error) {
        return { refused: message(error) };
      }
      const { preview, policies, uncomposed } = composed;
      // THE TWO WAYS A PREVIEW GOES STALE, told apart because the remedies read differently:
      // somebody else moved this machine's configuration, or this request no longer composes
      // what was shown. Both are the one digest comparison — the revision is inside it — and the
      // revision is checked first only so the sentence can name the two revisions rather than
      // say that something changed.
      const stood = preview.expectedRevision ?? "no configuration";
      const read = args.expectedRevision ?? "no configuration";
      if (preview.expectedRevision !== args.expectedRevision) {
        return {
          refused:
            `the preview was composed against ${read} and ${args.machineId} now stands at ` +
            `${stood}: read the preview again before installing`,
        };
      }
      if (preview.previewDigest !== args.previewDigest) {
        return {
          refused:
            `the preview no longer composes what would be installed on ${args.machineId}: ` +
            `read the preview again before installing`,
        };
      }
      const missing = uncomposed[0];
      if (missing !== undefined) {
        const why = preview.services.find((row) => row.serviceId === missing)?.reason ?? "";
        return { refused: `${uncomposed.join(", ")} has no policy to install: ${why}` };
      }
      try {
        const configuration = await ctx.services.configureConfiguration({
          machineId: args.machineId,
          expectedRevision: args.expectedRevision,
          policies: [...policies],
        });
        return {
          machineId: args.machineId,
          revision: configuration.revision,
          services: configuration.policies.map((policy) => ({
            serviceId: policy.serviceId,
            revision: policy.revision,
          })),
        };
      } catch (error) {
        // The host runs this same compare-and-swap inside its own transaction, so a
        // configuration that moved between the read above and this call is refused there. That
        // is a refusal of the ask, not a fault of this plugin's.
        return { refused: message(error) };
      }
    }),
  ];
}
