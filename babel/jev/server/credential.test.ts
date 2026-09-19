import { expect, test } from "bun:test";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { ServicePolicySchema, servicePolicyCredentialRefs } from "@manifold/protocol";
import { askJev, JEV_SERVICE, type JevServices } from "./credential.ts";

/*
  THE THREE ABSENCES ARE ONE ANSWER, AND THE KEY IS NEVER HERE.

  What is worth proving about credential plumbing is not that a happy call returns a value. It is
  the two properties a plausible change would break:

  - NO BINDING MEANS NO CALL, mechanically. Not "the call fails politely" — the invocation is
    never reached, so an unconfigured hub makes no request, spends nothing, and cannot refuse. The
    test asserts the invocation count, because a version that called and caught would pass any
    assertion about the return value while sending a request nobody authorized.
  - NO BINDING, NO CREDIT AND A HOST THAT THROWS ARE INDISTINGUISHABLE to a caller. That is what
    makes "out of credits" mechanical rather than a policy: there is one value in the type, so a
    caller cannot branch on which absence it got even if it wanted to, and the branch it takes
    when the operator never installed Jev is the branch it takes when the account runs dry.

  The key itself is absent by construction rather than by assertion: nothing in the outbound
  arguments could carry one. What CAN be asserted is that the part does not acquire one by another
  road — so an API key is planted in the environment and the part is still silent, which is the
  shape a well-meaning "fall back to an env var" change would fail.
*/

/** A planted key. If a future `askJev` ever reads ambient state, this is what it would find. */
const PLANTED = "apikey_planted_by_the_test_never_a_real_shape";

/** One answer as a policy projecting two leaves per question would return it. */
const ANSWER = { gate: { score: 2.4, confidence: 0.81 } };

const READY: InstanceServiceDescription = {
  serviceId: JEV_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "dev-01", name: "dev-01", online: true },
  configuration: {
    revision: "r7",
    pluginId: "atyrode.babel.jev",
    enabled: true,
    policySha256: "a".repeat(64),
  },
  connected: true,
  state: "ready",
  reason: null,
};

/** The host, as much of it as this needs: a roster and one invocation, both recorded. */
function host(options: {
  readonly roster?: readonly InstanceServiceDescription[];
  readonly reply?: ServiceReply;
  readonly throws?: boolean;
}): { readonly services: JevServices; readonly asks: unknown[] } {
  const asks: unknown[] = [];
  return {
    asks,
    services: {
      listInstances: async () => {
        if (options.throws) throw new Error("the owner went offline mid-read");
        return { defaultOwner: null, services: [...(options.roster ?? [])] };
      },
      invokeInstance: async (args) => {
        asks.push(args);
        return (
          options.reply ?? { type: "service_result", requestId: "q1", ok: true, result: ANSWER }
        );
      },
    },
  };
}

test("with no service bound, nothing is called and nothing refuses", async () => {
  process.env["TYPESAFE_API_KEY"] = PLANTED;
  process.env["JEV_API_KEY"] = PLANTED;
  try {
    const { services, asks } = host({ roster: [] });
    // A key in the environment is not a binding. A part that read one would be a part whose
    // behaviour depended on a machine's shell, which is the whole thing a credentialRef avoids.
    await expect(
      askJev(services, JEV_SERVICE.operations.judge, { state: "a record" }),
    ).resolves.toBeNull();
    expect(asks).toEqual([]);
  } finally {
    delete process.env["TYPESAFE_API_KEY"];
    delete process.env["JEV_API_KEY"];
  }
});

test("a service that is configured but not answering is not called either", async () => {
  // Every state short of `ready`, because each is a real deployment moment — never installed,
  // disabled by the operator, retiring a revision, starting, or the credential revoked — and the
  // part's behaviour in all of them is the behaviour it has without the part at all.
  for (const state of ["unconfigured", "stopped", "stopping", "starting", "unavailable"] as const) {
    const { services, asks } = host({
      roster: [{ ...READY, state, reason: "instance_service_unavailable" }],
    });
    await expect(
      askJev(services, JEV_SERVICE.operations.judge, { state: "a record" }),
    ).resolves.toBeNull();
    expect(asks).toEqual([]);
  }
});

test("no binding, no credit and a host that fails are one answer", async () => {
  const unbound = await askJev(host({ roster: [] }).services, JEV_SERVICE.operations.judge, {
    state: "a record",
  });
  // `service_upstream_refused` is what an exhausted account arrives as: the provider answered a
  // status the proxy would not disclose, and the host reports the class rather than the body.
  const refusals = ["service_upstream_refused", "service_credential_unavailable"] as const;
  for (const refusal of refusals) {
    const broke = host({
      roster: [READY],
      reply: { type: "service_result", requestId: "q1", ok: false, refusal },
    });
    expect(
      await askJev(broke.services, JEV_SERVICE.operations.judge, { state: "a record" }),
    ).toEqual(unbound);
    // It was asked, once: out of credit is discovered by calling, and that is the only
    // difference between these absences that exists anywhere.
    expect(broke.asks).toHaveLength(1);
  }
  const thrown = host({ throws: true });
  expect(
    await askJev(thrown.services, JEV_SERVICE.operations.judge, { state: "a record" }),
  ).toEqual(unbound);
  expect(thrown.asks).toEqual([]);
});

test("a bound and funded service answers, and the ask carries only the caller's input", async () => {
  const { services, asks } = host({ roster: [READY] });
  expect(await askJev(services, JEV_SERVICE.operations.judge, { state: "a record" })).toEqual(
    ANSWER,
  );
  // The whole outbound argument, exactly: the service, the revision the roster reported, the
  // operation and the caller's input. No header, no query, no credential, nowhere to put one.
  expect(asks).toEqual([
    {
      serviceId: JEV_SERVICE.serviceId,
      expectedRevision: "r7",
      operationId: "judge",
      input: { state: "a record" },
    },
  ]);
});

test("a projection this cannot read is an absence, not an answer", async () => {
  // A policy the operator revised to follow the provider's wire may project something else. That
  // is the operator's to fix, and until he does Jev has not answered.
  for (const result of [["gate", 2.4], "2.4", 2.4, null]) {
    const { services } = host({
      roster: [READY],
      reply: { type: "service_result", requestId: "q1", ok: true, result },
    });
    await expect(
      askJev(services, JEV_SERVICE.operations.judge, { state: "a record" }),
    ).resolves.toBeNull();
  }
});

test("the service the part names is installable, and its key is a reference the host resolves", () => {
  /*
    The operator installs the policy; the part declares the names it is installed under. This
    builds the smallest policy those names compose into and asks the host's own two questions of
    it: does it parse, and what credential would the owner go looking for? `servicePolicyCredentialRefs`
    is the function the engine itself calls to decide which sources a binding needs available
    (`serviceAvailability` refuses `service_credential_unavailable` when one is not), so its
    answer is the operative meaning of "the host injects it".
  */
  const policy = ServicePolicySchema.parse({
    serviceId: JEV_SERVICE.serviceId,
    revision: "r7",
    origin: JEV_SERVICE.origin,
    allowLoopbackHttp: false,
    credential: { ref: JEV_SERVICE.credentialRef, header: "authorization", prefix: "Bearer " },
    maxConcurrent: 4,
    operations: {
      [JEV_SERVICE.operations.judge]: {
        method: "POST",
        invocable: true,
        path: "/v1/systemone",
        input: { [JEV_SERVICE.stateField]: { type: "string", required: true, maxBytes: 65536 } },
        query: {},
        body: [
          { path: [JEV_SERVICE.stateField], value: { input: JEV_SERVICE.stateField } },
          { path: ["model"], value: { literal: "jev-1.13.0" } },
        ],
        timeoutMs: 15000,
        maxRequestBytes: 65536,
        maxResponseBytes: 262144,
        maxResultBytes: 98304,
        response: {
          kind: "projected-json",
          fields: [
            ["gate", "score"],
            ["gate", "confidence"],
          ],
          maxArrayItems: 16,
        },
      },
    },
  });
  expect(servicePolicyCredentialRefs(policy)).toEqual([JEV_SERVICE.credentialRef]);
});
