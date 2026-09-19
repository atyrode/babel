import { createHash, type Hash } from "node:crypto";
import { PAIR_QUESTIONS, type PairQuestionId } from "../../contract.ts";
import {
  askJev,
  JEV_SERVICE,
  type JevAnswer,
  type JevAnswerStore,
  type JevServices,
} from "../server/credential.ts";
import { JevAnswers } from "../server/judge.ts";
import type { RecordPair } from "./pair.ts";

/*
  THE ONE PAID CALL A PAIR COSTS, AND WHY IT IS A SECOND OPERATION RATHER THAN A SECOND QUESTION.

  `server/judge.ts` is this file's sibling and deliberately not its base class. It owns "one
  judgement of ONE record": its request carries a record kind, a bank document version and one
  text, because a per-record question is rendered from `bank/questions/<kind>.md` and the bank
  holds one document per RECORD KIND. A pair has no kind — the two records may be of two
  different ones — and the material is two states rather than one, so neither the input shape nor
  the key transfers. Pretending otherwise was the available shortcut and it would have been a
  lie in the most expensive place: the `judge` operation's policy projects the per-record leaves,
  so asking it about a pair would send one record's text, get a per-record answer back, and read
  `contradicts` off a projection that never names it. Every pair would come back unanswered and
  the report would say the corpus has no contradictions in it.

  SO THE PART NAMES A SECOND OPERATION, `pair`, and the operator installs its half exactly as he
  installs the first: `bun babel/jev/tools/seed-questions.ts policy` prints both, the wording
  below is what goes out under `pair`, and `response.fields` must name the two leaves
  {@link PAIR_QUESTIONS} spells. A deployment whose policy declares only `judge` reaches nothing:
  the host refuses an operation the policy does not declare, `askJev` turns that into its one
  absence, and the pass reports candidates with nothing judged. That is the honest state of a
  deployment that has installed half the policy, and it reads nothing like a clean corpus.

  ONE ANSWER SERVES BOTH RELATIONS, which is the study's own economics and the reason #358 cost
  nothing on top of #357: you pay for the state, not for the question. Both leaves come back from
  one invocation of one ordered pair, `pairs/detect.ts` hands the same answers to both detectors,
  and a design with one call per relation would have doubled the bill over two thousand pairs for
  no additional information.

  THE KEY IS ORDERED, unlike {@link pairKey}. `pairKey` identifies a pair for DEDUPLICATION —
  retrieval reaches the same two records from both ends and they must be judged once — but
  `supersedes` asks whether the SECOND describes a later state of the first, so the two orders
  are two different questions with two different right answers. A memo keyed symmetrically would
  serve the answer about `(a, b)` as the answer about `(b, a)` and invert half the directions it
  reported. The two functions are therefore both here and both named for what they identify.

  WHAT THE KEY CARRIES, spelled once in {@link pairRequestKey}:

    sha256( domain || lp(operation) || lp(wording version) || lp(a.text) || lp(b.text) )

  The wording version because a reworded question is a different question, and the two texts in
  order because they are what is sent. Length-prefixed for `judge.ts`'s reason: plain
  concatenation lets one pair wear another's answer. Deliberately NOT the ids, the revisions or
  the cuts — none of the three is sent, two records that say the same thing are the same call,
  and a cut is applied after the answer arrives.
*/

/** One pair question, verbatim, as the operator renders it into his policy's literals. */
export interface PairQuestion {
  readonly id: PairQuestionId;
  /** The sentence that goes out. */
  readonly asks: string;
  /** How the answer is to be given, which is what makes the leaf readable as a degree. */
  readonly criteria: string;
  /** Every pair question is a noul: a bounded degree of yes, 0 to 1, as `degreeOf` reads it. */
  readonly type: "noul";
}

/**
 * THE WORDING'S OWN VERSION. It is not the bank's: no pairwise document exists to hold one, for
 * the reasons `pairs/pair.ts` gives at length. It moves when a sentence below moves.
 */
export const PAIR_WORDING_VERSION = 1;

const BASIS_DOMAIN = "atyrode.babel.jev/pair-basis/1";

/**
 * WHAT A PAIR SUGGESTION WAS JUDGED UNDER, in `babel.suggest`'s own `basis` field.
 *
 * The door compares this by equality and never parses it, and `suggestionsOf` reads it back as
 * the durable "already judged" mark — so what belongs in it is exactly what, having changed,
 * makes a previous answer no longer an answer to the question now being asked. Three things
 * qualify and all three are here:
 *
 *  - THE WORDING VERSION, because a reworded question is a different question.
 *  - THE SERVICE POLICY REVISION, because the policy carries the literals that actually went
 *    out, the model that answered and the projection that was read. A deployment that revised
 *    its policy has judged nothing under the new one, and a mark that could not say so would
 *    hand back the old model's output as the new one's. The transport deliberately does not
 *    expose the revision — `askJev` joins it to the MEMO key and stops there — so the pass
 *    reads the roster for it, which costs a host call and no money.
 *  - THE STATED CUTS, because for a pair the cut is not a post-hoc calibration the way a bank
 *    threshold is: it is the whole of what makes a candidate a finding, there is no default for
 *    it anywhere, and a deployment that moved its line from 0.85 to 0.7 has not looked at its
 *    corpus at 0.7. Their VALUES are in the digest and no value is invented — an absent cut
 *    contributes nothing, exactly as its detector contributes nothing.
 *
 * A DIGEST BECAUSE THE FIELD IS 64 CHARACTERS and a revision is an opaque string of the host's
 * choosing. Spelling the parts would be more readable right up to the first revision that did
 * not fit, at which point the mark would silently become a prefix that two revisions share.
 */
export function pairBasis(policyRevision: string, cuts: Readonly<Record<string, number>>): string {
  const hasher = createHash("sha256");
  hasher.update(BASIS_DOMAIN);
  writeLP(hasher, String(PAIR_WORDING_VERSION));
  writeLP(hasher, policyRevision);
  // Sorted, so two callers stating the same lines in two orders have judged under one basis.
  for (const question of Object.keys(cuts).sort()) {
    writeLP(hasher, `${question}=${String(cuts[question])}`);
  }
  return `pair/${String(PAIR_WORDING_VERSION)}/${hasher.digest("hex").slice(0, 32)}`;
}

/**
 * THE TWO QUESTIONS, VERBATIM. They are in this repository rather than in the bank because the
 * bank is one document per record kind and a pair has no kind; they are data rather than prose
 * for the same reason `bank/questions/*.md` is — the operator installs the literals, and the
 * wording that goes out must be readable beside the answer it produced.
 *
 * The sentences are the study's own, and `pairs/contradiction.ts` and `pairs/supersession.ts`
 * quote them back in the rationale the operator reads, so what he is shown is the question that
 * was asked.
 */
export const PAIR_QUESTION_WORDING: readonly PairQuestion[] = [
  {
    id: PAIR_QUESTIONS.contradicts,
    asks:
      "do these two records make claims that cannot both be true about the same system? " +
      "the order of the two is irrelevant to this question.",
    criteria:
      "answer with a degree between 0 and 1. 0 is two records that are simply about different " +
      "things or that agree; 1 is two claims about one system that cannot both hold. two " +
      "records at different levels of detail about the same true thing do not contradict, and " +
      "neither does one that corrects an explicitly provisional statement in the other. do not " +
      "say which of the two is correct: that is not asked here.",
    type: "noul",
  },
  {
    id: PAIR_QUESTIONS.supersedes,
    asks:
      "does the second record describe a later state of what the first record describes? " +
      "the order of the two is the question.",
    criteria:
      "answer with a degree between 0 and 1. 0 is two records describing unrelated things, or " +
      "the same state twice, or the FIRST describing the later state; 1 is the second " +
      "describing the same subject after it changed. judge the states described, not the dates " +
      "the records carry: you are not shown them.",
    type: "noul",
  },
];

/** The input document one ordered pair becomes: the two states, in order, and nothing else. */
export function pairInput(pair: RecordPair): Readonly<Record<string, string>> {
  return {
    [JEV_SERVICE.pairFields.a]: pair.a.text,
    [JEV_SERVICE.pairFields.b]: pair.b.text,
  };
}

const PAIR_DOMAIN = "atyrode.babel.jev/pair/1";

/** One reusable frame for the length prefixes; `judge.ts` argues why one buffer serves. */
const U32_BYTES = new Uint8Array(4);
const U32 = new DataView(U32_BYTES.buffer);
const ENCODER = new TextEncoder();

function writeLP(hasher: Hash, value: string): void {
  const bytes = ENCODER.encode(value);
  U32.setUint32(0, bytes.length, false);
  hasher.update(U32_BYTES);
  hasher.update(bytes);
}

/** THE IDEMPOTENCY KEY OF ONE ORDERED PAIR. See this file's head for what it carries. */
export function pairRequestKey(pair: RecordPair): string {
  const hasher = createHash("sha256");
  hasher.update(PAIR_DOMAIN);
  writeLP(hasher, JEV_SERVICE.operations.pair);
  writeLP(hasher, String(PAIR_WORDING_VERSION));
  writeLP(hasher, pair.a.text);
  writeLP(hasher, pair.b.text);
  return hasher.digest("hex");
}

/**
 * THE ANSWERS THIS SERVER HALF HAS ALREADY PAID FOR, for pairs.
 *
 * A second store beside `judge.ts`'s rather than a shared one, because the two hold answers to
 * different questions under different keys and a pair pass of 64 judgements should not evict a
 * sweep's memo of 24 records. Same bound, same eviction, same lifetime: it dies with the process
 * and a part with no store stays a part with no store.
 */
const HELD = new JevAnswers();

/**
 * THE POLICY THE PAIR OPERATION WOULD BE ASKED UNDER, or `null` because none would answer.
 *
 * The pass needs the revision BEFORE it spends, because {@link pairBasis} puts it on every
 * suggestion and a mark naming a policy that did not answer is worse than no mark. `askJev`
 * reads the roster too and deliberately keeps the revision to itself — the transport does not
 * know what a judgement means and should not learn — so this is a second read of the same free
 * roster rather than a widening of that return type for one caller.
 *
 * It is also the whole of "no policy, no invocation": a pass that gets `null` here stops having
 * read nothing and spent nothing, and `invokeInstance` is never reached.
 */
export async function pairPolicyRevision(services: JevServices): Promise<string | null> {
  try {
    const roster = await services.listInstances({});
    const bound = roster.services.find((service) => service.serviceId === JEV_SERVICE.serviceId);
    // `ready` and a configuration are two facts, and the type keeps them apart: a row that
    // claims the first without the second names no revision, which is one more way Jev is not
    // available rather than a revision this pass may invent.
    const configuration = bound?.state === "ready" ? bound.configuration : null;
    if (configuration === null) return null;
    return String(configuration.revision);
  } catch {
    // The host's own failures are Jev being unavailable, which is the same absence as the rest.
    return null;
  }
}

/**
 * Jev's judgement of one ORDERED pair, or `null` because Jev did not judge it.
 *
 * The one branch is the whole contract and it is `judge`'s: absent, disabled, out of credit, a
 * policy that declares no `pair` operation, a pair too large to send, or a reply that is not a
 * document. A caller does with `null` what Babel does without this part installed — which for a
 * pair pass is to report the candidate as unjudged and never as unrelated.
 *
 * `revision` is the policy the caller read and is about to attribute the answer to. It is
 * checked against the roster inside `askJev`, so a policy the operator replaced mid-pass is one
 * more absence rather than an answer filed under the wrong revision.
 */
export async function askPair(
  services: JevServices,
  pair: RecordPair,
  options: { readonly revision: string; readonly answers?: JevAnswerStore },
): Promise<JevAnswer | null> {
  return await askJev(services, JEV_SERVICE.operations.pair, pairInput(pair), {
    key: pairRequestKey(pair),
    answers: options.answers ?? HELD,
    expectedRevision: options.revision,
  });
}
