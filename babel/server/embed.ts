import type { GuestServices } from "@manifold/plugin-kit/server";
import { EMBEDDING_SERVICE, type EmbeddingOperationId } from "../contract.ts";
import type { Embedder, Embedding } from "../store/corpus.ts";

/*
  THE BASELINE'S ONE CALL OUT, AND THE KEY IS NEVER IN IT (#337).

  Until this file the server half of Babel reached nothing. It read its own SQLite file, posted
  jobs to the hub, and called `atyrode.code`'s doors; no origin, no socket, no credential. The
  corpus index's meaning half needs a model, Babel holds no model and will hold no key, and this
  is the only shape in the family that resolves that: the plugin names a service and an operation
  the OPERATOR installed, the host resolves the credential by reference against a source it holds
  and writes it into the outbound request, and the value is never in this bundle's address space.
  `jev/server/credential.ts` argues the whole of it and this is that argument applied once more —
  deliberately, in one function, in the only module of the baseline that can make a call.

  THE PROPERTY THAT REPLACES "IT CANNOT REACH ANYTHING" is "it does not unless the operator
  installed something saying it may", and it is mechanical rather than promised:

    - the roster is read first and the invocation is unreachable unless a service under this id is
      configured, enabled and `ready`. No binding is no call.
    - every absence — never installed, disabled, a revision retiring, starting, its credential
      revoked, the upstream refusing, an unusable answer — is ONE return value, `null`, and
      `store/corpus.ts` has one branch for it: do what Babel does without any of this. A
      deployment that installs no policy is indistinguishable from this shape not existing.
    - nothing here throws, so no cycle, no drain and no door can fail because of an embedding.

  WHAT LEAVES IS THE TEXT AND NOTHING ELSE. One field, named by the policy, carrying the prose
  `recordTextSql` selected: a record's title and the claim fields the peel shows. No record id, no
  run id, no session, no locator, no instant, no digest. There is no second field in the request
  this could smuggle one into, and the size cap is applied before the roster is read.

  THE MODEL IS READ OUT OF THE ANSWER, NOT OUT OF THE CONFIGURATION, and that is the point of
  recording it: a configuration says what the operator asked to route to and an answer says what
  answered. An answer that does not name its model is refused, because a vector stored under a
  guessed model name is a vector nobody can tell is stale, which is how a re-embed becomes
  guesswork.
*/

/** The two host doors this needs, as a shape, so a test can stand in for the host. */
export type EmbeddingServices = Pick<GuestServices, "listInstances" | "invokeInstance">;

/**
 * The most one request may carry, in the host's own unit.
 *
 * `ServiceInputSchema` refines an input to 65,536 bytes of serialized JSON; this is the same
 * measure at an eighth of it, so the two are comparable and a call this admits can never be
 * refused upstream for size. `store/corpus.ts` caps the TEXT at 8,192 bytes, which is what makes
 * this bound unreachable in practice rather than a truncation nobody sees.
 */
export const EMBED_REQUEST_CAP_BYTES = 8192;

const ENCODER = new TextEncoder();

/** A vector as a projected leaf: an array of finite numbers, and nothing else is accepted. */
function vectorOf(value: unknown): readonly number[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const values: number[] = [];
  for (const entry of value) {
    if (typeof entry !== "number" || !Number.isFinite(entry)) return null;
    values.push(entry);
  }
  return values;
}

/**
 * Embeds one text through the installed service, or answers `null` because it cannot.
 *
 * The order of the statements is the guarantee: the size check first and free, the roster second
 * and free, and the invocation — the one statement that costs anything and the one that reaches
 * an origin — behind both.
 */
export async function askEmbedding(
  services: EmbeddingServices,
  text: string,
  operationId: EmbeddingOperationId = EMBEDDING_SERVICE.operations.embed,
): Promise<Embedding | null> {
  if (text === "") return null;
  const input = { [EMBEDDING_SERVICE.textField]: text };
  if (ENCODER.encode(JSON.stringify(input)).length > EMBED_REQUEST_CAP_BYTES) return null;
  try {
    const roster = await services.listInstances({});
    const bound = roster.services.find(
      (service) => service.serviceId === EMBEDDING_SERVICE.serviceId,
    );
    const configuration = bound?.state === "ready" ? bound.configuration : null;
    if (configuration === null) return null;
    const reply = await services.invokeInstance({
      serviceId: EMBEDDING_SERVICE.serviceId,
      // The revision the roster just reported: a policy edited between these two statements is a
      // mismatch the host refuses, which lands in the same absence as everything else.
      expectedRevision: configuration.revision,
      operationId,
      // The input and nothing else.
      input,
    });
    if (!reply.ok) return null;
    const answer = reply.result;
    if (typeof answer !== "object" || answer === null || Array.isArray(answer)) return null;
    const leaves = answer as Record<string, unknown>;
    const values = vectorOf(leaves[EMBEDDING_SERVICE.vectorField]);
    const model = leaves[EMBEDDING_SERVICE.modelField];
    // A vector with no model is refused rather than stored under a made-up name. See the header.
    if (values === null || typeof model !== "string" || model.trim() === "") return null;
    return { model: model.trim(), values };
  } catch {
    // The host's own conflicts — the configuration moved, the owner went offline, the frame was
    // too large — are the service being unavailable, which is the same one branch.
    return null;
  }
}

/**
 * The embedder the index and the door are handed, bound to one dispatch's service authority.
 *
 * It is a closure rather than an object because the index needs exactly one verb, and because
 * `GuestServices` is served to a DISPATCH and not to a hook: the settled-job hook's context
 * (`GuestJobSettledCtx`) carries jobs and actions and no services at all, so a background wake
 * has no authority here to hand over. `null` is what those callers pass, and it is the same
 * absence as an uninstalled policy.
 */
export function embedder(services: EmbeddingServices): Embedder {
  return async (text: string) => await askEmbedding(services, text);
}
