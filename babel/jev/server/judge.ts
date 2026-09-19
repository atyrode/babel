import { createHash, type Hash } from "node:crypto";
import type { RecordKind } from "../../contract.ts";
import { BANK, bankFor } from "../bank/bank.ts";
import {
  askJev,
  JEV_SERVICE,
  type JevAnswer,
  type JevAnswerStore,
  type JevOperationId,
  type JevServices,
} from "./credential.ts";

/*
  ONE JUDGEMENT OF ONE RECORD: WHAT DETERMINES IT, AND WHY IT IS PAID FOR ONCE (#369).

  `credential.ts` is the transport and deliberately knows nothing about records: it carries an
  input document out, caps its size, and consults a memo whose key somebody else minted. This
  file is that somebody. It owns the question "what would make two calls the same call", which is
  a question about the bank and the record, and it owns the store the answers sit in.

  WHAT THE KEY CARRIES, and it is spelled exactly once, in `requestKey`:

    sha256( domain || lp(operation) || lp(bank version)
                   || lp(kind) || lp(document version) || lp(text) )

  - THE OPERATION, because a second one would be a different endpoint, a different projection and
    a different price under the same policy.
  - THE BANK'S VERSION AND THE DOCUMENT'S, both, because `bank/versions.json` carries them as
    independent numbers: a reworded question bumps its own document and an assessment cites
    `kind@version`, so keying on the bank version alone — which is what the study proposed —
    would serve an answer to the old wording as though it answered the new. This is the one
    invalidation that matters most, because the wording is what the answer is an answer TO.
  - THE KIND, which is which document was asked, and is not implied by the text.
  - THE RECORD'S TEXT, verbatim and unnormalized, because it is verbatim and unnormalized that it
    goes out. Trimming it here would make the key describe something other than the call.

  Every variable-length field is length-prefixed, the same discipline and for the same reason as
  `machine/prepare.ts`'s preparation id: plain concatenation lets a kind of "a" with a text of
  "bc" collide with a kind of "ab" and a text of "c", and a collision here is one record wearing
  another's judgement.

  WHAT IS DELIBERATELY NOT IN IT:

  - THE RECORD'S ID, ITS RUN AND ITS RECIPE. An answer is a function of the text that was sent,
    and none of the three is sent. Keying on an id would pay a second time for a record that was
    re-filed or superseded without an edit, and pay twice over for two records that say the same
    thing — which is exactly the loop the memo exists to make free.
  - THE CLOCK. Nothing about a judgement decays, so a key with a day in it is a cache that
    charges once a day for the same question. What bounds staleness here is the bank version and
    the policy revision, both of which are edits somebody made.
  - THE THRESHOLDS AND THE ADMISSIONS. `votesFor` turns an answer into votes AFTER the call, and
    two operators who draw the line differently are asking the same question. Putting a
    calibration in the key would charge for re-tallying.
  - THE VOTER. Thirteen of them read one call: "you pay for the state once, however many
    questions you attach", so a key per voter would be twelve extra payments for nothing.
  - THE POLICY REVISION, which is in the EFFECTIVE key rather than this one — `askJev` joins it
    from the roster read it already does, because this file cannot see a binding and should not
    learn how.

  AND THE STORE IS NOT A SECOND STORE. It is a `Map` in this module's own scope, bounded, holding
  nothing durable: a part with no store stays a part with no store, and an answer that is worth
  keeping past a restart is an assessment the baseline's doors are asked to write. The bound is
  what makes it a memo instead of a leak, and the bank version inside the key is what keeps it
  from outliving the wording it was computed under — a stranded entry is unreachable the moment
  the version moves and is evicted in its turn.
*/

/**
 * HOW MANY ANSWERS ARE HELD AT ONCE, and the whole of the bound.
 *
 * 256 is an order of magnitude more than the records one cycle judges, so the runaway this
 * defends against — a loop asking about one record, or a sweep re-asking about a batch — never
 * reaches it. An entry is a key of 64 hex characters and a policy revision, and one projected
 * answer document whose size the operator's own `response.fields` and `maxResponseBytes` bound,
 * so the whole store is tens of kilobytes and cannot grow past that however long the half lives.
 */
export const JEV_ANSWERS_HELD = 256;

/**
 * ONE JUDGEMENT, AS THE CALL DETERMINES IT: every field is something that changes the answer,
 * and nothing that changes the answer is outside it. That is the property `requestKey` rests on.
 */
export interface JevRequest {
  /** The operation the policy is asked for, which decides the endpoint and the projection. */
  readonly operation: JevOperationId;
  /** `Bank.version`: the artifact the operator rendered the policy's question literals from. */
  readonly bankVersion: number;
  /** Which document was rendered, which is which questions the policy carries. */
  readonly kind: RecordKind;
  /** `BankDocument.version`: the wording, which is what an assessment cites as `kind@version`. */
  readonly documentVersion: number;
  /** The record's own text, and the only thing this part sends. */
  readonly text: string;
}

/** A judgement of this record's text under the bank as it ships. The versions are read here and
 *  not passed in, so no caller can cite a wording the bundle does not carry. */
export function requestFor(kind: RecordKind, text: string): JevRequest {
  return {
    operation: JEV_SERVICE.operations.judge,
    bankVersion: BANK.version,
    kind,
    documentVersion: bankFor(kind).version,
    text,
  };
}

const REQUEST_DOMAIN = "atyrode.babel.jev/request/1";

/** One reusable four-byte frame for the length prefixes: `update` copies synchronously and
 *  nothing awaits between the write and the read, so one buffer serves every derivation. */
const U32_BYTES = new Uint8Array(4);
const U32 = new DataView(U32_BYTES.buffer);
const ENCODER = new TextEncoder();

function writeLP(hasher: Hash, value: string): void {
  const bytes = ENCODER.encode(value);
  U32.setUint32(0, bytes.length, false);
  hasher.update(U32_BYTES);
  hasher.update(bytes);
}

/**
 * THE IDEMPOTENCY KEY, spelled here and nowhere else. See this file's head for what it carries
 * and what it deliberately does not.
 *
 * It is a digest rather than the fields joined because the text is up to the call cap long and
 * the store holds keys, not records: 64 hex characters per entry instead of eight kilobytes.
 */
export function requestKey(request: JevRequest): string {
  const hasher = createHash("sha256");
  hasher.update(REQUEST_DOMAIN);
  writeLP(hasher, request.operation);
  writeLP(hasher, String(request.bankVersion));
  writeLP(hasher, request.kind);
  writeLP(hasher, String(request.documentVersion));
  writeLP(hasher, request.text);
  return hasher.digest("hex");
}

/**
 * THE BOUNDED MEMO: `JEV_ANSWERS_HELD` answers, least recently used evicted first.
 *
 * Least recently USED rather than inserted, because the shape this defends against is a loop
 * asking one question over and over: the entry that must survive a busy cycle is the hot one,
 * and insertion order would evict it for being old.
 */
export class JevAnswers implements JevAnswerStore {
  readonly #held = new Map<string, JevAnswer>();
  readonly #capacity: number;

  constructor(capacity: number = JEV_ANSWERS_HELD) {
    this.#capacity = capacity;
  }

  /** How many answers are held. The bound is a promise about this number, so it is readable. */
  get size(): number {
    return this.#held.size;
  }

  get(key: string): JevAnswer | undefined {
    const answer = this.#held.get(key);
    if (answer === undefined) return undefined;
    // Re-inserting moves the entry to the end of the Map's own order, which is what makes the
    // eviction below least-recently-used rather than first-in.
    this.#held.delete(key);
    this.#held.set(key, answer);
    return answer;
  }

  set(key: string, answer: JevAnswer): void {
    this.#held.delete(key);
    this.#held.set(key, answer);
    for (const oldest of this.#held.keys()) {
      if (this.#held.size <= this.#capacity) break;
      this.#held.delete(oldest);
    }
  }
}

/**
 * THE ANSWERS THIS SERVER HALF HAS ALREADY PAID FOR.
 *
 * Module scope, so there is one per running server half and it dies with the process. A part
 * that held this per call would pay for every loop; one that wrote it down would have become a
 * store.
 */
const HELD = new JevAnswers();

/**
 * Jev's judgement of one record, or `null` because Jev did not judge it.
 *
 * The one branch is the whole contract, and it now covers one more absence than it did: the part
 * absent, disabled or dry, and the record too large to be worth a call. A caller that wants to
 * distinguish the last one asks `withinCallCap` before asking here.
 *
 * `answers` is a parameter so a test and a future voter with its own lifetime can own a store;
 * left out, this is the server half's own.
 */
export async function judge(
  services: JevServices,
  request: JevRequest,
  answers: JevAnswerStore = HELD,
): Promise<JevAnswer | null> {
  return await askJev(
    services,
    request.operation,
    { [JEV_SERVICE.stateField]: request.text },
    { key: requestKey(request), answers },
  );
}
