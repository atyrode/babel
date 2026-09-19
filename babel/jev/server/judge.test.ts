import { expect, test } from "bun:test";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { JEV_CALL_CAP_BYTES, JEV_SERVICE, type JevServices } from "./credential.ts";
import { JevAnswers, judge, requestFor, type JevRequest } from "./judge.ts";

/*
  WHAT IS PROVED HERE IS THE CALL COUNT, NOT THE CACHE.

  A memo is only worth having if it removes a payment, so every assertion below counts
  invocations against a fake host. "The second call was a hit" is a statement about a Map;
  "the service was invoked once for two judgements" is the property the operator is buying, and
  it is the one a plausible change breaks — a key that picked up the clock, an eviction that
  dropped the hot entry, a refusal remembered as an answer.

  The cap is proved the same way and at its own boundary, because a cap nobody can locate is a
  cap that will be missed by one: the largest record that IS sent and the smallest that is not
  are one byte apart, and the byte is the unit.
*/

/** The host's own measure of an input is `JSON.stringify` in UTF-8, so `{"state":"..."}` costs
 *  twelve bytes before a single byte of the record. The largest text a call may carry: */
const CAP_TEXT_BYTES = JEV_CALL_CAP_BYTES - 12;

/** The installed policy, as the roster reports it. Held apart from the row because a test
 *  installs a revision over it and a spread of a nullable field would lose the other three. */
const POLICY = {
  revision: "r7",
  pluginId: "atyrode.babel.jev",
  enabled: true,
  policySha256: "a".repeat(64),
};

const READY: InstanceServiceDescription = {
  serviceId: JEV_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "dev-01", name: "dev-01", online: true },
  configuration: POLICY,
  connected: true,
  state: "ready",
  reason: null,
};

/**
 * The host, recording the state of every invocation. `at` is mutable so a test can do what an
 * operator does — install a policy revision, or remove the binding — between two judgements.
 */
function host(options: { readonly reply?: (nth: number) => ServiceReply | undefined } = {}): {
  readonly services: JevServices;
  readonly asks: string[];
  readonly at: { revision: string; bound: boolean };
} {
  const asks: string[] = [];
  const at = { revision: "r7", bound: true };
  return {
    asks,
    at,
    services: {
      listInstances: async () => ({
        defaultOwner: null,
        services: at.bound
          ? [{ ...READY, configuration: { ...POLICY, revision: at.revision } }]
          : [],
      }),
      invokeInstance: async (args) => {
        asks.push(String(args.input[JEV_SERVICE.stateField]));
        return (
          options.reply?.(asks.length) ?? {
            type: "service_result",
            requestId: `q${String(asks.length)}`,
            ok: true,
            // The answer says which call produced it, so a second call cannot be mistaken for
            // a memo hit and a memo hit cannot be mistaken for a second call.
            result: { gate: asks.length },
          }
        );
      },
    },
  };
}

test("the largest record is sent, the next byte is not and is every other absence", async () => {
  const answers = new JevAnswers();
  const admitted = host();
  expect(
    await judge(admitted.services, requestFor("finding", "x".repeat(CAP_TEXT_BYTES)), answers),
  ).toEqual({ gate: 1 });
  expect(admitted.asks).toHaveLength(1);

  // One byte more. Nothing is sent, nothing is read, and the caller is handed the same value it
  // gets when the operator never installed the part — which is the whole point of refusing
  // rather than truncating: a judgement of a shortened record is a judgement of a record the
  // operator cannot read back.
  const refused = host();
  refused.at.bound = false;
  const unbound = await judge(refused.services, requestFor("finding", "a record"), answers);
  const oversized = host();
  expect(
    await judge(oversized.services, requestFor("finding", "x".repeat(CAP_TEXT_BYTES + 1)), answers),
  ).toEqual(unbound);
  expect(oversized.asks).toEqual([]);

  // AND THE UNIT IS BYTES, NOT CHARACTERS. Half as many two-byte runes is the same cap, which is
  // what makes the number comparable to the frame the host and the provider count in.
  const runes = host();
  expect(
    await judge(runes.services, requestFor("finding", "é".repeat(CAP_TEXT_BYTES / 2 + 1)), answers),
  ).toBeNull();
  expect(runes.asks).toEqual([]);
});

test("two identical judgements make one call", async () => {
  const answers = new JevAnswers();
  const hosted = host();
  const request = requestFor("observation", "the router retries twice and logs once");
  const first = await judge(hosted.services, request, answers);
  const second = await judge(hosted.services, request, answers);
  expect(first).toEqual({ gate: 1 });
  expect(second).toEqual(first);
  expect(hosted.asks).toHaveLength(1);

  // A request built separately from the same facts is the same request: the key is the content,
  // never the object.
  await judge(hosted.services, requestFor("observation", request.text), answers);
  expect(hosted.asks).toHaveLength(1);
});

test("a bank bump, a document bump and another kind are each a new question", async () => {
  const answers = new JevAnswers();
  const hosted = host();
  const text = "the advisor channel records that its blockers were not acted on";
  const asked = requestFor("finding", text);
  expect(await judge(hosted.services, asked, answers)).toEqual({ gate: 1 });

  // The wording an assessment cites as `kind@version` is what the answer answers, so an answer
  // held under the old version is not an answer to the new one.
  const reworded: JevRequest = { ...asked, documentVersion: asked.documentVersion + 1 };
  expect(await judge(hosted.services, reworded, answers)).toEqual({ gate: 2 });

  // And the bank's own version moves independently of any document's, which is why both are in
  // the key: `bank/versions.json` carries them as separate numbers.
  const rebanked: JevRequest = { ...asked, bankVersion: asked.bankVersion + 1 };
  expect(await judge(hosted.services, rebanked, answers)).toEqual({ gate: 3 });

  // Same text, other document. A record kind is which questions were asked.
  expect(await judge(hosted.services, requestFor("proposal", text), answers)).toEqual({ gate: 4 });

  // Every one of the four is still held, and the first is still the first answer.
  expect(await judge(hosted.services, asked, answers)).toEqual({ gate: 1 });
  expect(hosted.asks).toHaveLength(4);
});

test("a policy the operator replaced is not answered out of the store", async () => {
  const answers = new JevAnswers();
  const hosted = host();
  const request = requestFor("hypothesis", "the drain stops on the first refusal");
  expect(await judge(hosted.services, request, answers)).toEqual({ gate: 1 });

  // The policy carries the question literals, the model and the projection, so a revision the
  // operator installed is a different question asked of a different instrument.
  hosted.at.revision = "r8";
  expect(await judge(hosted.services, request, answers)).toEqual({ gate: 2 });
  expect(hosted.asks).toHaveLength(2);
  // A sweep sized under r7 may not label an r8 answer as r7, even with both answers cached.
  expect(await judge(hosted.services, request, answers, "r7")).toBeNull();
  expect(hosted.asks).toHaveLength(2);
});

test("an absence is never remembered", async () => {
  // Out of credit arrives as an upstream refusal. Remembering it would make one dry minute last
  // until the process restarts, which is a part that switched itself off.
  // `reply` overrides one call and the host answers the rest, so the second judgement is the
  // ordinary funded one.
  const dry: ServiceReply = {
    type: "service_result",
    requestId: "q1",
    ok: false,
    refusal: "service_upstream_refused",
  };
  const hosted = host({ reply: (nth) => (nth === 1 ? dry : undefined) });
  const answers = new JevAnswers();
  const request = requestFor("finding", "the receipt carries tokens the panel never shows");
  expect(await judge(hosted.services, request, answers)).toBeNull();
  expect(await judge(hosted.services, request, answers)).toEqual({ gate: 2 });
  expect(hosted.asks).toHaveLength(2);
  expect(answers.size).toBe(1);
});

test("the store is bounded, and what goes is the answer nobody asked for lately", async () => {
  const answers = new JevAnswers(2);
  const hosted = host();
  const a = requestFor("finding", "record a");
  const b = requestFor("finding", "record b");
  const c = requestFor("finding", "record c");

  expect(await judge(hosted.services, a, answers)).toEqual({ gate: 1 });
  expect(await judge(hosted.services, b, answers)).toEqual({ gate: 2 });
  // `a` is asked for again, which makes it the freshest entry rather than the oldest one.
  expect(await judge(hosted.services, a, answers)).toEqual({ gate: 1 });
  expect(hosted.asks).toHaveLength(2);

  // `c` does not fit. Insertion order would have evicted `a`, the entry a loop is hammering;
  // least-recently-used evicts `b`.
  expect(await judge(hosted.services, c, answers)).toEqual({ gate: 3 });
  expect(answers.size).toBe(2);
  expect(await judge(hosted.services, a, answers)).toEqual({ gate: 1 });
  expect(hosted.asks).toHaveLength(3);
  expect(await judge(hosted.services, b, answers)).toEqual({ gate: 4 });
  expect(answers.size).toBe(2);
});

test("with no binding, a warm store is still nothing", async () => {
  // The fallback is one path whether the store is full or empty. A memo consulted in front of
  // the roster read would be a part that kept answering after the operator disabled it.
  const answers = new JevAnswers();
  const warm = host();
  const request = requestFor("observation", "the panel renders an empty section");
  expect(await judge(warm.services, request, answers)).toEqual({ gate: 1 });

  const gone = host();
  gone.at.bound = false;
  expect(await judge(gone.services, request, answers)).toBeNull();
  expect(gone.asks).toEqual([]);
});

test("the server half's own store is where an answer already paid for lives", async () => {
  // No store argument: `judge` uses the module's, which is one per running server half. Two
  // callers with no shared state between them still make one call.
  const hosted = host();
  const text = "the settlement writes a receipt and no records";
  expect(await judge(hosted.services, requestFor("proposal", text))).toEqual({ gate: 1 });
  expect(await judge(hosted.services, requestFor("proposal", text))).toEqual({ gate: 1 });
  expect(hosted.asks).toHaveLength(1);
});
