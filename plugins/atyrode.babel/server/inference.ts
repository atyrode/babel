import {
  INFERENCE_SERVICE,
  type ModelPrice,
} from "../contract.ts";

/*
  THE POLICY THE OWNER INSTALLS FOR BABEL'S INFERENCE SERVICE (ADR 0038, #279).

  A job that drives a model never holds the model's credential, and since atyrode/code#153 there
  is no Code process to hold one on Babel's behalf either. So an `explore` or an `evaluate` binds
  ONE service, `atyrode.babel.inference`, whose RUNTIME is omp's own gateway operation
  (`atyrode.omp.gateway.serve`, `providesService: true`): the machine's owner starts that gateway
  with the account pool the job named, the gateway resolves a provider credential from the
  machine's broker, and the job receives a loopback url and a bearer minted for it alone.

  WHY THIS FILE EXISTS AT ALL. Nothing here is a Babel decision that could live in a manifest:
  the runtime's three pins (`installationRevision`, `artifactSha256`, `resourceBindingDigest`)
  are facts about the omp gateway installed on THAT machine, so the policy can only be assembled
  against a live `readConfiguration`. The alternative was a page of JSON in `plugins/README.md`
  for an operator to copy, retype and get wrong — which is what `doors/inference.ts` exists to
  make unnecessary.

  WHY THE TYPES ARE LOCAL. `ServicePolicySchema` in the pinned SDK admits exactly one meter kind,
  `openai-usage`, and omp's wire is pi-native (`modelId`, `context.messages`,
  `usage.input`/`.output`/`.cacheRead`) — declaring OpenAI's kind over it would refuse every call
  with `service_input_invalid` rather than silently mis-read one. manifold#570 adds
  `pi-native-usage`; until the SDK pin moves, the shape is stated here and cast once, at the
  `ctx.services.configureConfiguration` boundary, where a hub that does not yet know the kind
  refuses the write by name and the door reports that refusal. Nothing is faked: the policy Babel
  offers is the policy Babel will install the moment the hub accepts it.
*/

/** `ServiceRuntime`, as the policy carries it: the candidate's pins plus the input mapping. */
export interface InferenceRuntime {
  readonly scope?: "job" | "instance" | undefined;
  readonly pluginId: string;
  readonly operationId: string;
  readonly installationRevision: string;
  readonly artifactSha256: string;
  readonly resourceBindingDigest: string;
  /**
   * HOW THE JOB'S ACCOUNT POOL REACHES THE GATEWAY. A job-scoped runtime may map one of the
   * CALLING job's inputs into the service's operation (`ServiceRuntimeSchema.input`), and the
   * gateway declares `accountPool` as its only input — so the pool a Babel job posted is the pool
   * that gateway resolves a credential for. manifold-omp installs its own `omp` service exactly
   * this way (`plugins/atyrode.omp/service-setup.ts` `gatewayReview`), which is why #267 needs no
   * `credentialRef` selector and manifold#549 is unnecessary here.
   */
  readonly input: Readonly<Record<string, { readonly input: string }>>;
}

/** One proxied route of the policy, in `ServiceProxyOperationPolicy`'s own shape. */
interface ProxyOperation {
  readonly kind: "http-proxy";
  readonly method: "GET" | "POST";
  readonly path: string;
  readonly request: { readonly kind: "none" } | { readonly kind: "json"; readonly disclosure: "full" };
  readonly response: {
    readonly kind: "stream";
    readonly disclosure: "full";
    readonly contentTypes: readonly ("application/json" | "text/event-stream")[];
    readonly headers: readonly never[];
  };
  readonly meter?: { readonly kind: string } | undefined;
  readonly timeoutMs: number;
  readonly maxRequestBytes: number;
  readonly maxResponseBytes: number;
}

export interface InferencePolicy {
  readonly serviceId: string;
  readonly revision: string;
  readonly runtime: InferenceRuntime;
  readonly maxConcurrent: number;
  readonly operations: Readonly<Record<string, ProxyOperation>>;
  readonly prices: { readonly models: Readonly<Record<string, ModelPrice>> };
}

/**
 * ANTHROPIC'S PUBLIC LIST PRICES, observed 2026-09-14, in integer micro-dollars per million
 * tokens — the units `ServiceModelPriceSchema` states and the owner meters in.
 *
 * THIS IS OPERATOR-EDITABLE POLICY CONTENT AND NOT A FACT ABOUT ANTHROPIC. It is a default so
 * that installing the service does not require retyping a price table, and it is pinned by the
 * policy's own revision like the rest of the policy: a price change is a new revision the
 * operator consents to. An operator on an enterprise rate, a batch discount or a different
 * provider edits the installed policy, and Babel reads back whatever is there — `launchPreview`
 * states the price it FOUND, never the price in this table.
 *
 * Self-serve list at that date: Opus 5 and Opus 4.8 at $5/$25, Sonnet 5 and Sonnet 4.6 at
 * $2/$10, Haiku 4.5 at $1/$5, Fable 5.1 at $10/$50. Cache reads are 10% of the input price
 * except Fable's, which is $0.25 per million.
 *
 * The keys are FULLY QUALIFIED model references, because the metered proxy prices a call by the
 * verbatim `modelId` its request body carried and omp's gateway keys its model map by
 * `<provider>/<id>` (manifold-omp `plugins/workers/gateway/gateway.test.ts`: `anthropic/…` hits,
 * the bare id misses). A bare key here would price nothing.
 */
export const INFERENCE_PRICES: Readonly<Record<string, ModelPrice>> = {
  "anthropic/claude-opus-5": {
    inputPerMillion: 5_000_000,
    outputPerMillion: 25_000_000,
    cachedInputPerMillion: 500_000,
  },
  "anthropic/claude-opus-4-8": {
    inputPerMillion: 5_000_000,
    outputPerMillion: 25_000_000,
    cachedInputPerMillion: 500_000,
  },
  "anthropic/claude-sonnet-5": {
    inputPerMillion: 2_000_000,
    outputPerMillion: 10_000_000,
    cachedInputPerMillion: 200_000,
  },
  "anthropic/claude-sonnet-4-6": {
    inputPerMillion: 2_000_000,
    outputPerMillion: 10_000_000,
    cachedInputPerMillion: 200_000,
  },
  "anthropic/claude-haiku-4-5": {
    inputPerMillion: 1_000_000,
    outputPerMillion: 5_000_000,
    cachedInputPerMillion: 100_000,
  },
  "anthropic/claude-fable-5-1": {
    inputPerMillion: 10_000_000,
    outputPerMillion: 50_000_000,
    cachedInputPerMillion: 250_000,
  },
};

/**
 * HOW MANY CALLS THE OWNER'S PROXY SERVES AT ONCE for this service. Sixteen, which is the
 * gateway's own `maxConcurrent` in manifold-omp and the `concurrentJobs` this plugin's manifest
 * declares for `explore` and `evaluate`: one call in flight per job at the operation's ceiling.
 */
const MAX_CONCURRENT = 16;

/**
 * The two routes. `models` is a keyless listing an engine uses to resolve what it may ask for;
 * `stream` is the one that costs money, so it is the one that declares a meter — and a metered
 * operation must carry a JSON request (`ServiceProxyOperationPolicySchema`'s own refinement),
 * which omp's `/v1/pi/stream` does.
 *
 * The response byte and timeout bounds are omp's own gateway policy's
 * (`plugins/atyrode.omp/service-policies.ts` `buildGatewayPolicy`), copied rather than chosen: a
 * tighter bound here would truncate a stream the gateway is willing to serve, and a looser one
 * would be a number this side cannot honour.
 */
export function buildInferencePolicy(runtime: InferenceRuntime): InferencePolicy {
  const models: ProxyOperation = {
    kind: "http-proxy",
    method: "GET",
    path: INFERENCE_SERVICE.modelsPath,
    request: { kind: "none" },
    response: {
      kind: "stream",
      disclosure: "full",
      contentTypes: ["application/json"],
      headers: [],
    },
    timeoutMs: 60_000,
    maxRequestBytes: 65_536,
    maxResponseBytes: 4 * 1024 * 1024,
  };
  const stream: ProxyOperation = {
    kind: "http-proxy",
    method: "POST",
    path: INFERENCE_SERVICE.streamPath,
    request: { kind: "json", disclosure: "full" },
    response: {
      kind: "stream",
      disclosure: "full",
      contentTypes: ["application/json", "text/event-stream"],
      headers: [],
    },
    meter: { kind: INFERENCE_SERVICE.meterKind },
    timeoutMs: 300_000,
    maxRequestBytes: 16 * 1024 * 1024,
    maxResponseBytes: 256 * 1024 * 1024,
  };
  return {
    serviceId: INFERENCE_SERVICE.serviceId,
    revision: INFERENCE_SERVICE.revision,
    runtime,
    maxConcurrent: MAX_CONCURRENT,
    operations: { models, stream },
    prices: { models: INFERENCE_PRICES },
  };
}

/**
 * The runtime a candidate becomes: its pins, and the one input mapping that carries the calling
 * job's account pool into the gateway. `scope` is passed through rather than asserted, because
 * an INSTANCE-scoped candidate may hold only literal inputs
 * (`ServicePolicySchema`'s refinement) and so cannot carry a pool at all — `doors/inference.ts`
 * refuses such a candidate by name instead of installing a policy the hub would reject.
 */
export function inferenceRuntime(candidate: {
  readonly scope?: "job" | "instance" | undefined;
  readonly pluginId: string;
  readonly operationId: string;
  readonly installationRevision: string;
  readonly artifactSha256: string;
  readonly resourceBindingDigest: string;
}): InferenceRuntime {
  return {
    ...(candidate.scope === undefined ? {} : { scope: candidate.scope }),
    pluginId: candidate.pluginId,
    operationId: candidate.operationId,
    installationRevision: candidate.installationRevision,
    artifactSha256: candidate.artifactSha256,
    resourceBindingDigest: candidate.resourceBindingDigest,
    input: { accountPool: { input: "accountPool" } },
  };
}
