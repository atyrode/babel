import {
  INFERENCE_SERVICE,
  type ModelPrice,
} from "../contract.ts";
import type { BabelStore } from "../store/store.ts";

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

  WHY THE SHAPE IS STATED HERE. It is this plugin's document, not a schema's: a literal assembled
  from one machine's pins, so the one thing a reader must be able to check by eye — that the meter
  is declared on the call that SPENDS and on nothing else — is in the type. The kind is
  `pi-native-usage` because omp's wire is pi-native (`modelId`, `context.messages`,
  `usage.input`/`.output`/`.cacheRead`) and `openai-usage` over it would refuse every call with
  `service_input_invalid` rather than silently mis-read one. `MANIFOLD_REV` 0bc76660 carries that
  kind (manifold#570, landed as #572), so this document is assignable to the SDK's own
  `ServicePolicy` and `doors/inference.ts` hands it to `configureConfiguration` WITH NO CAST: the
  typecheck is the hub's own schema. A hub older than the pin is a different fact and refuses the
  write by name, which the door records and `launchPreview` reports as `unsupported`.
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

/**
 * One proxied route of the policy, in `ServiceProxyOperationPolicy`'s own shape — and assignable
 * to it, which is why `contentTypes` and `headers` are not `readonly` arrays: the SDK states them
 * mutable and this document is handed to it unchanged.
 */
interface ProxyOperation {
  readonly kind: "http-proxy";
  readonly method: "GET" | "POST";
  readonly path: string;
  readonly request: { readonly kind: "none" } | { readonly kind: "json"; readonly disclosure: "full" };
  readonly response: {
    readonly kind: "stream";
    readonly disclosure: "full";
    readonly contentTypes: ("application/json" | "text/event-stream")[];
    readonly headers: never[];
  };
  readonly meter?: { readonly kind: typeof INFERENCE_SERVICE.meterKind } | undefined;
  readonly timeoutMs: number;
  readonly maxRequestBytes: number;
  readonly maxResponseBytes: number;
}

/**
 * WHAT A CALL COSTS, as the policy carries it.
 *
 * `default` is never written by this side. It is in the shape because the OPERATOR may write one:
 * the table is his to edit ({@link INFERENCE_PRICES}), so a refresh carries the INSTALLED table
 * through verbatim — `default` and all — rather than reinstating the defaults below over it.
 */
export interface InferencePrices {
  readonly models: Readonly<Record<string, ModelPrice>>;
  readonly default?: ModelPrice | undefined;
}

export interface InferencePolicy {
  readonly serviceId: string;
  readonly revision: string;
  readonly runtime: InferenceRuntime;
  readonly maxConcurrent: number;
  readonly operations: Readonly<Record<string, ProxyOperation>>;
  readonly prices: InferencePrices;
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

// ---------------------------------------------------------------- what the hub answered

/**
 * WHY THIS IS RECORDED AT ALL (#284). An absent `atyrode.babel.inference` policy has two
 * meanings and an operator acts on them differently: nobody has installed one, or THIS HUB
 * REFUSED the one Babel offers. A refused `configureConfiguration` leaves nothing behind — the
 * configuration read shows the same absence either way — so the answer is kept here, by the
 * only act that ever saw it, and the launch preview reads it back.
 *
 * It is a fact about a machine and a service, not an act on a record: one row, rewritten, and
 * `detail` is the hub's own sentence rather than Babel's paraphrase of it.
 */
export interface ServiceSetup {
  readonly state: "installed" | "refused";
  readonly detail: string;
}

/** The bound on the hub's sentence: enough to name a cause, never a log to store. */
const DETAIL_BYTES = 1024;

export async function recordServiceSetup(
  store: BabelStore,
  machineId: string,
  serviceId: string,
  setup: ServiceSetup,
): Promise<void> {
  await store.db.run(
    `INSERT INTO service_setup(machine_id, service_id, state, detail, observed_at)
     VALUES (?, ?, ?, ?, ?)
     ON CONFLICT(machine_id, service_id) DO UPDATE
       SET state = excluded.state, detail = excluded.detail, observed_at = excluded.observed_at`,
    [
      machineId,
      serviceId,
      setup.state,
      setup.detail.slice(0, DETAIL_BYTES),
      new Date(store.now()).toISOString(),
    ],
  );
}

/** What the hub last answered about this service on this machine, or null when nobody asked. */
export async function lastServiceSetup(
  store: BabelStore,
  machineId: string,
  serviceId: string,
): Promise<ServiceSetup | null> {
  const rows = await store.db.query<{ state: string; detail: string }>(
    `SELECT state, detail FROM service_setup WHERE machine_id = ? AND service_id = ?`,
    [machineId, serviceId],
  );
  const row = rows[0];
  if (row === undefined) return null;
  return { state: row.state === "refused" ? "refused" : "installed", detail: row.detail };
}

/**
 * WHAT MAKES A REFUSAL THE HUB BEING OLDER THAN THIS PLUGIN, rather than something the owner
 * can fix by trying again.
 *
 * The one thing in Babel's policy a hub may not know is the METER KIND: `ServicePolicySchema`
 * in the pinned SDK admits `openai-usage` alone and omp's wire needs `pi-native-usage`
 * (manifold#570, landing as #572). A hub answers the write with its own schema's complaint,
 * which names the path and the literal it expected — so these are the hub's words and not a
 * guess: `meter`, and either kind's name. Anything else is an ordinary refusal the preview
 * reports as itself rather than as a version gap.
 */
export const UNSUPPORTED_METER = /meter|pi-native-usage|openai-usage/i;
