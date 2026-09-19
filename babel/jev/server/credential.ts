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
  upstream refusing — which is what "out of credits" is — an input this part will not pay to
  send, or an answer that is not a document. There is exactly one of them in the type, so a
  caller has exactly one branch, and the branch it takes when the operator never installed
  anything is the branch it takes when the account runs dry. Nothing here throws, so no cycle can
  refuse because of Jev, and nothing is called at all when there is no binding: the size check is
  the first statement, the roster read the second, and the invocation behind both.

  THE TWO BOUNDS THE CEILINGS DO NOT GIVE (#369) are both here, because both are properties of
  THE CALL rather than of a budget. Per-cycle and per-day ceilings in micro-dollars meter what a
  deployment spends over a window; neither says how large one call may be, and neither notices
  the same record being judged twice for two payments. So:

  - A PER-CALL SIZE CAP, refused before the roster is read. The failure it defends against is
    not a malicious loop but one call carrying a pasted session — roughly thirty times the
    intended rate, paid once, with nothing looking wrong. It is on this line rather than in a
    wrapper because a wrapper is bypassable: this is the only function in the bundle that can
    invoke the service, so a cap here is a cap on every call the part can make.
  - AN IDEMPOTENCY MEMO, consulted behind the binding check and before the invocation, which is
    the one statement here that costs anything. The caller owns the key, because what determines
    an answer is a fact about the bank and the record and this file knows neither — `judge.ts`
    spells it — and this file joins the one part of it the caller cannot know: the policy
    revision the roster just reported. Only an ANSWER is remembered; an absence never is,
    because a minute of unavailability that stuck would be the part switching itself off until
    the process restarts.
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
  /**
   * The operations the part may name. A policy may declare more; the part calls these.
   *
   * `pair` is a SECOND operation rather than a second question on the first, because the first
   * carries ONE state and a relation is asked of two. `bun babel/jev/tools/seed-questions.ts
   * policy` prints the literals and the projection for both; a deployment whose policy declares
   * only `judge` reaches nothing here — the host refuses an operation the policy does not
   * declare, which lands in `askJev`'s one absence like every other.
   */
  operations: { judge: "judge", pair: "pair" },
  /** Jev's own word for the material a question is asked of, and the one field a caller fills. */
  stateField: "state",
  /**
   * The two states an ORDERED pair is asked about. The order is the question's subject, not
   * decoration: `supersedes` asks whether the second describes a later state of the first, so a
   * policy that mapped these two literals to one another's leaves would invert every direction
   * it reported.
   */
  pairFields: { a: "state_a", b: "state_b" },
} as const;

/** Pin a pass to a ready policy without invoking it. Guest RPCs need only be awaitable. */
export async function jevPolicyRevision(services: JevServices): Promise<string | null> {
  try {
    const roster = await services.listInstances({});
    const bound = roster.services.find((service) => service.serviceId === JEV_SERVICE.serviceId);
    return bound?.state === "ready" ? (bound.configuration?.revision ?? null) : null;
  } catch {
    return null;
  }
}

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
 * THE MOST ONE CALL MAY CARRY, in the host's own unit.
 *
 * `ServiceInputSchema` refines an input to
 * `new TextEncoder().encode(JSON.stringify(input)).length <= 65536` (manifold's
 * `packages/protocol/src/services.ts`), and this is that same measure at a lower number so the
 * two are comparable: 65,536 is the FRAME the host will carry, 8,192 is the SPEND this part will
 * pay for.
 *
 * BYTES RATHER THAN TOKENS, and the part holds no tokenizer because it does not need one: no
 * token is shorter than a byte, so a cap on bytes bounds the token bill from above. 8,192 bytes
 * is therefore at most 8,192 tokens — a quarter of Jev's 32k-token frame for a state plus its
 * longest question, so a call this admits can never be refused upstream for size — and in
 * practice around two thousand, twice the thousand-token call the study's "thirty times the
 * intended rate" implies was intended. The bank's own exemplars are records' claims verbatim and
 * run 400 to 900 bytes, so the cap admits a record several times the largest anyone has written
 * and refuses a 30,000-token paste by a factor of fifteen.
 */
export const JEV_CALL_CAP_BYTES = 8192;

const ENCODER = new TextEncoder();

/**
 * Whether an input is one this part will pay to send, measured as the host measures it.
 *
 * Exported because the cap is the one absence knowable BEFORE the call, so a caller that wants
 * to say why it did not ask can ask this — rather than `askJev` growing a second return value
 * and every caller a second branch.
 */
export function withinCallCap(input: ServiceInput): boolean {
  return ENCODER.encode(JSON.stringify(input)).length <= JEV_CALL_CAP_BYTES;
}

/**
 * WHERE AN ANSWER ALREADY PAID FOR IS HELD: a `Map`'s two methods and nothing more.
 *
 * The transport neither knows nor bounds the store, and that is deliberate — how many answers
 * are worth holding and when they stop being worth holding is a question about the bank and the
 * process, which `judge.ts` answers.
 */
export interface JevAnswerStore {
  get(key: string): JevAnswer | undefined;
  set(key: string, answer: JevAnswer): void;
}

/**
 * ONE CALL'S MEMO: the store, and the caller's own statement of what determines the answer.
 *
 * `key` is a digest the caller mints, because the facts that decide a judgement — whose text,
 * which bank, which document version — are not in this file's vocabulary. `askJev` joins the one
 * part of it the caller cannot know, the policy revision the roster reported, so an answer
 * computed under a policy the operator has since replaced is never handed back as the new one's.
 */
export interface JevMemo {
  readonly key: string;
  readonly answers: JevAnswerStore;
  /** A sweep pins the policy it used to size and label this work. */
  readonly expectedRevision?: string;
}

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
  memo?: JevMemo,
): Promise<JevAnswer | null> {
  // TOO LARGE IS AN ABSENCE, AND IT IS THE CHEAPEST ONE: no roster is read and no request is
  // made. It is not a truncation, because a judgement of a record with its middle silently
  // removed is a judgement of something the operator never sees, and it is not a refusal the
  // caller has to handle, because the caller's right move is the one it already makes for every
  // other absence — do what Babel does without this part.
  if (!withinCallCap(input)) return null;
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
    if (memo?.expectedRevision !== undefined && memo.expectedRevision !== configuration.revision) {
      return null;
    }
    // THE MEMO IS BEHIND THE BINDING CHECK, so a warm store cannot make an unbound, disabled or
    // dry part answer — the fallback stays the same single path with a full cache as with none —
    // and IN FRONT OF THE INVOCATION, which is the only statement here that spends anything.
    // The revision joins the caller's key because the policy is what carries the wording, the
    // model and the projection: two identical questions asked under two revisions are two
    // questions. The key is fixed-length hex, so the pair cannot alias whatever a revision is.
    const answers = memo?.answers;
    const held = memo === undefined ? "" : `${memo.key}/${configuration.revision}`;
    const remembered = answers?.get(held);
    if (remembered !== undefined) return remembered;
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
    if (typeof answer !== "object" || answer === null || Array.isArray(answer)) return null;
    // ONLY AN ANSWER IS REMEMBERED. An absence is never stored: every one of them is a state of
    // the deployment rather than a fact about the question, and a cached unavailability would
    // make one bad minute last until the process restarts.
    answers?.set(held, answer);
    return answer;
  } catch {
    // The host's own conflicts — the configuration moved, the owner went offline, the frame was
    // too large — are Jev being unavailable. They are not Babel's to report and not a caller's to
    // handle, so they arrive as the same absence.
    return null;
  }
}
