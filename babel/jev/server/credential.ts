import type { GuestServices } from "@manifold/plugin-kit/server";
import type { ServiceInput } from "@manifold/protocol";

/*
  THE PART'S ONE CALL OUT, AND THE KEY IS NEVER IN IT.

  Jev is TypeSafe's typed-judgement endpoint, reached over HTTPS with an API key. Plugin code
  cannot `fetch`, and the two ways it could legally reach an origin are not equal:

  - A SANDBOXED JOB WITH `network: "host"`, which manifold's own plugin manager labels "HIGH RISK
    — host network, including reachable local services". Trading broad host networking for one
    HTTPS origin is a bad trade, and it puts the part in the business of launching jobs.
  - A SERVICE THE OPERATOR INSTALLED, invoked through the host. The plugin names the service and
    the operation; the host holds the credential by REFERENCE, resolves it against the owner's own
    credential source, and writes it into the outbound request. The part sends an input document
    and receives projected leaves. It never sees, stores, forwards or could log the key, because
    the key is never in its address space — not by discipline, by construction.

  This module is the second, and it is `machine/restic.ts`'s discipline in the half that has no
  job: there too the repository and its password arrive through a service the operator installed
  (`RESTIC_SERVICE`, babel/contract.ts), the manifest's own `environment` is fixed
  reviewed values and never a secret's home, and no ambient variable can redirect the call. The
  difference is only which door the host opens — a job's loopback proxy there, `services.invoke`
  here — and that difference is forced: an operation of kind `http-proxy` is refused
  `service_unauthorized` to a direct invoker (manifold packages/server/src/job-service.ts, the
  `"kind" in operation` test in `directServiceAuthority`), so it is reachable from a job and from
  nowhere else. A projected operation discloses strictly less in both directions: fixed leaves in,
  fixed leaf paths out, rather than a body and a stream.

  WHAT THE PART DECLARES is the four names below and `services:invoke` in its manifest. What the
  OPERATOR installs is the policy behind them: the origin, the credential reference, the question
  literals the bank supplies, the response projection and the prices. That split is the point —
  the part holds no value, and a policy is revisioned and consented, so a question's wording or a
  price is an operator's decision with an audit trail rather than a release of this repository.

  AND ABSENCE IS ONE PATH. `askJev` answers `null` for every reason Jev cannot answer: no service
  bound, one bound but unconfigured, disabled, starting, unavailable, its credential revoked, the
  upstream refusing — which is what "out of credits" is — or an answer that is not a document.
  There is exactly one of them in the type, so a caller has exactly one branch, and the branch it
  takes when the operator never installed anything is the branch it takes when the account runs
  dry. Nothing here throws, so no cycle can refuse because of Jev, and nothing is called at all
  when there is no binding: the roster read is the first statement and the call is behind it.
*/

/**
 * THE SERVICE THIS PART CALLS THROUGH, spelled once.
 *
 * `serviceId` is namespaced under the part the way `atyrode.babel.restic` is namespaced under the
 * baseline, and `test/contract.test.ts` pins it to `JEV_PLUGIN_ID`: this file does not import
 * `contract.ts`, because the kit inlines every imported module into the bundle and the part would
 * carry the baseline's whole vocabulary for one string.
 *
 * `credentialRef` is a NAME, and the only half of a credential that may appear in a repository.
 * The operator's policy carries it as `credential.ref`; the owner resolves it against a source it
 * holds and injects the value into the request. Nothing in this family ever holds the other half.
 */
export const JEV_SERVICE = {
  serviceId: "atyrode.babel.jev.typesafe",
  credentialRef: "typesafe-api",
  origin: "https://api.typesafe.ai",
  /** The operations the part may name. A policy may declare more; the part calls these. */
  operations: { judge: "judge" },
  /** Jev's own word for the material a question is asked of, and the one field a caller fills. */
  stateField: "state",
} as const;

/** An operation id the part may ask for. A string the policy does not declare is refused by the
 *  host, but it is also a typo, and this keeps it from compiling. */
export type JevOperationId = (typeof JEV_SERVICE.operations)[keyof typeof JEV_SERVICE.operations];

/**
 * One answer, as the operator's policy projected it: the leaves its `response.fields` named.
 *
 * Deliberately unparsed. The transport does not know what a judgement means — a voter reads its
 * own leaves with its own schema — and a projection the operator revised to follow the provider's
 * wire must not require a release of this repository to keep working.
 */
export type JevAnswer = Readonly<Record<string, unknown>>;

/** The two host doors this needs, named as a shape so a test can stand in for the host without
 *  building the other nine. */
export type JevServices = Pick<GuestServices, "listInstances" | "invokeInstance">;

/**
 * Jev's answer, or `null` because Jev cannot answer.
 *
 * The caller's whole obligation is the one branch: `null` means do exactly what Babel does
 * without this part installed. It is never an error, never a refusal, and never distinguishes an
 * empty credit balance from an absent service, because a caller that branched on the difference
 * would be a caller whose behaviour changes when the operator's card expires.
 */
export async function askJev(
  services: JevServices,
  operationId: JevOperationId,
  input: ServiceInput,
): Promise<JevAnswer | null> {
  try {
    // NO BINDING, NO CALL: the roster is read first, and the invocation is unreachable unless it
    // names a service the operator configured, enabled and got to ready. `listInstances` answers
    // with what this caller may see, so an absent service is an absent row rather than a refusal.
    const roster = await services.listInstances({});
    const bound = roster.services.find((service) => service.serviceId === JEV_SERVICE.serviceId);
    // Every state short of `ready` is a real deployment moment — never installed, disabled by
    // the operator, retiring a revision, starting, or its credential revoked — and the part's
    // behaviour in all of them is the behaviour it has when the part is not installed at all.
    const configuration = bound?.state === "ready" ? bound.configuration : null;
    if (configuration === null) return null;
    const reply = await services.invokeInstance({
      serviceId: JEV_SERVICE.serviceId,
      // The revision the roster just reported. A policy edited between these two statements is a
      // mismatch the host refuses, which lands in the same `null` as everything else.
      expectedRevision: configuration.revision,
      operationId,
      // The input and nothing else. There is no header, no query and no second field this could
      // smuggle a credential into even if it held one.
      input,
    });
    if (!reply.ok) return null;
    const answer = reply.result;
    // A projected reply is a document of leaves. An array or a scalar is a policy projecting
    // something this cannot read, which is Jev not answering rather than Jev answering oddly.
    return typeof answer === "object" && answer !== null && !Array.isArray(answer) ? answer : null;
  } catch {
    // The host's own conflicts — the configuration moved, the owner went offline, the frame was
    // too large — are Jev being unavailable. They are not Babel's to report and not a caller's to
    // handle, so they arrive as the same absence.
    return null;
  }
}
