import { describe, expect, test } from "bun:test";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import { ENGINE_REFUSALS } from "../../contract.ts";
import { ENGINE_WITHOUT_ACTIONS, codeEngine, type ActionsSlice } from "./session.ts";

/*
  Babel translates the host's rejection sentences, including Code's own refusal token.
  Successful replies are checked against Code's published result schemas.
*/

/** An `actions` slice that throws whatever the host would, or resolves whatever Code would. */
function actions(
  answer: (args: { plugin: string; action: string; input: unknown }) => unknown,
): ActionsSlice & { readonly calls: { plugin: string; action: string; input: unknown }[] } {
  const calls: { plugin: string; action: string; input: unknown }[] = [];
  return {
    calls,
    call: async (args) => {
      calls.push(args);
      return await Promise.resolve(answer(args));
    },
  };
}

/**
 * ONE PROFILE AS CODE PUBLISHES ONE, so a test states what the deployment installed and nothing
 * else. `accounts: []` with `resolved: true` is the deployment that installed NOTHING — Code
 * looked and this profile pays for no model — and it is the state every test of the gate is
 * built on.
 */
function profile(over: {
  readonly containerId?: string;
  readonly accounts?: readonly { provider: string; identityKey: string | null }[];
  readonly resolved?: boolean;
}): unknown {
  return {
    containerId: over.containerId ?? "ctr_a",
    revision: 4,
    selected: {
      model: "anthropic/claude-opus-4-1",
      thinking: "high",
      capability: 4,
      advisor: "review",
    },
    machineId: "m-dev-01",
    accounts: over.accounts ?? [{ provider: "anthropic", identityKey: "victorballu@gmail.com" }],
    resolved: over.resolved ?? true,
  };
}

/** The job Code answers a posted session with, in the shape its own result schema takes. */
const POSTED = {
  jobId: "omp_7",
  machineId: "m-dev-01",
  operationId: "atyrode.omp.session",
  pluginId: "atyrode.omp",
  installationRevision: "1",
  artifactSha256: "a".repeat(64),
  inputDigest: "b".repeat(64),
  resourceBindingDigest: "c".repeat(64),
  inputs: [],
  state: "queued",
  nextInputSeq: null,
  result: null,
  authority: {
    origin: { kind: "action", traceId: "t1", door: null },
    requester: "operator",
    executor: null,
    decision: null,
  },
};

/** The host's rejection, in the class the hardened row raises it as. */
function hostRefusal(sentence: string): () => never {
  return () => {
    throw new ActionCallError(sentence);
  };
}

/** The in-realm engine's own error, which carries the SAME sentence under another name. */
function inRealmRefusal(sentence: string): () => never {
  return () => {
    const error = new Error(sentence);
    error.name = "ActionCallRefused";
    throw error;
  };
}

describe("a refusal the host raised", () => {
  test("an undeclared dependency is unavailable: there is no Code to ask", async () => {
    const slice = actions(hostRefusal(`undeclared_dependency: ${"atyrode.babel -> atyrode.code"}`));
    const answered = await codeEngine(slice).profiles();

    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    expect(answered.code).toBe(ENGINE_REFUSALS.unavailable);
    expect(answered.refused).toContain("atyrode.babel -> atyrode.code");
  });

  test("a ceiling the install does not hold is forbidden, whichever boundary raised it", async () => {
    const sentence = `caller_ceiling: atyrode.babel -> ${CODE_PLUGIN_ID}.runSession (containers:write)`;
    for (const raise of [hostRefusal(sentence), inRealmRefusal(sentence)]) {
      const answered = await codeEngine(actions(raise)).readSession({
        containerId: "ctr_1",
        jobId: "job_1",
      });
      expect(answered.ok).toBe(false);
      if (answered.ok) continue;
      // BOTH BOUNDARIES CARRY ONE SENTENCE, which is why the class is read off the message: a
      // bundle must not import the engine to recognise its errors.
      expect(answered.code).toBe(ENGINE_REFUSALS.forbidden);
      expect(answered.refused).toContain("containers:write");
    }
  });

  test("Code's own word inside the host's detail still moves a stale profile out of the generic refusal", async () => {
    const slice = actions(
      hostRefusal(
        `refused: atyrode.babel -> ${CODE_PLUGIN_ID}.runSession (code_stale_preferences)`,
      ),
    );
    const answered = await codeEngine(slice).readSession({ containerId: "c", jobId: "j" });

    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    // The operator re-reads the list and presses again; every other `code_…` is Code saying no.
    expect(answered.code).toBe(ENGINE_REFUSALS.staleProfile);
  });

  test("something that is not a refusal at all is reported as itself, not folded into the vocabulary", async () => {
    const slice = actions(() => {
      throw new TypeError("undefined is not a function");
    });
    const answered = await codeEngine(slice).profiles();

    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    expect(answered.code).toBe(ENGINE_REFUSALS.refused);
    expect(answered.refused).toContain("raised something that is not a refusal");
    expect(answered.refused).toContain("undefined is not a function");
  });
});

/*
  THERE IS NO SECOND ROAD. Two tests here proved that a RESOLVED `{ refused: "code_…" }` was
  folded onto Babel's names, and no such value ever reaches `ctx.actions.call`: that shape is
  what Code's ORDINARY-CLIENT adapter answers a session dispatch with. A Code refusal is a
  REJECTION the host raises, carrying Code's own word inside the class's detail, which is what
  the `refused: … (code_stale_preferences)` case above actually exercises. The branch and its
  tests are deleted rather than re-pinned against a shape nobody produces.
*/
test("a door answering outside its own published result is a fault, never a value passed on", async () => {
  const slice = actions(() => ({ profiles: "not a list" }));
  const answered = await codeEngine(slice).profiles();

  expect(answered.ok).toBe(false);
  if (answered.ok) return;
  expect(answered.refused).toContain("answered outside its own published result");
});

test("a profile carries Code's own accounts, and its silence is told from its saying none", async () => {
  const slice = actions(() => ({
    profiles: [
      {
        containerId: "ctr_a",
        revision: 4,
        selected: {
          model: "anthropic/claude-opus-4-1",
          thinking: "high",
          capability: 4,
          advisor: "review",
        },
        machineId: "m-dev-01",
        // An API-key slot has a credential and no login, so Code answers a null identity.
        accounts: [
          { provider: "anthropic", identityKey: "victorballu@gmail.com", label: "victorballu" },
          { provider: "openai", identityKey: null },
        ],
        resolved: true,
      },
      // No observation to resolve against: `resolved: false` with an empty list means ASK
      // AGAIN, and a reader that printed it as "spends nothing" would be inventing a fact.
      {
        containerId: "ctr_b",
        revision: 1,
        selected: null,
        machineId: null,
        accounts: [],
        resolved: false,
      },
    ],
  }));
  const answered = await codeEngine(slice).profiles();

  expect(answered.ok).toBe(true);
  if (!answered.ok) return;
  expect(answered.value).toEqual([
    {
      containerId: "ctr_a",
      revision: 4,
      model: "anthropic/claude-opus-4-1",
      thinking: "high",
      lastMachineId: "m-dev-01",
      accounts: [
        { provider: "anthropic", identityKey: "victorballu@gmail.com", label: "victorballu" },
        { provider: "openai", identityKey: "", label: "" },
      ],
      resolved: true,
    },
    {
      containerId: "ctr_b",
      revision: 1,
      model: "",
      thinking: "",
      lastMachineId: "",
      accounts: [],
      resolved: false,
    },
  ]);
});

test("a caller with no actions slice is told nobody was asked, and nothing is called", async () => {
  const answered = await codeEngine(undefined).profiles();

  expect(answered.ok).toBe(false);
  if (answered.ok) return;
  expect(answered.code).toBe(ENGINE_REFUSALS.unavailable);
  expect(answered.refused).toContain(ENGINE_WITHOUT_ACTIONS);
});

test("settlement between read and follow cannot turn a pending receipt into a failed run", async () => {
  const inference = {
    calls: 3,
    inputTokens: 100,
    outputTokens: 20,
    cachedInputTokens: 0,
    costMicros: 900,
  };
  const settled = {
    ...POSTED,
    state: "exited",
    result: {
      jobId: POSTED.jobId,
      requestDigest: "d".repeat(64),
      ownerId: "owner",
      ownerGeneration: 1,
      state: "exited",
      exitCode: 0,
      reason: null,
      startedAt: 1_000,
      finishedAt: 2_000,
      usage: { elapsedMs: 1_000, memoryBytes: 100, processes: 1, outputBytes: 20, inference },
      outputs: [],
      limits: { timeoutMs: 600_000, memoryBytes: 1 << 30, processes: 64, outputBytes: 1 << 20 },
    },
  };
  const receipt = {
    sessionId: "session-7",
    sessionPath: "/outputs/session/session.jsonl",
    model: "anthropic/haiku",
    finalMessage: "Analysis complete.",
    usage: null,
    exitCode: 0,
    failure: null,
    configuredModel: "anthropic/haiku",
  };
  let reads = 0;
  const engine = codeEngine(
    actions(({ action }) => {
      if (action === "readSession") {
        return ++reads === 1
          ? { job: { ...POSTED, state: "started" }, session: null, silence: "omp_session_running" }
          : { job: settled, session: receipt, silence: null };
      }
      if (action === "cancelSession") return { job: settled };
      if (action === "followSession") {
        return {
          job: settled,
          inferenceUsage: { ...inference, lastModel: "anthropic/haiku" },
          progress: { stage: "at the model", at: 1_100 },
          inferenceCalls: [],
          seq: 4,
          firstSeq: 1,
          unavailable: null,
        };
      }
      throw new Error(`Unexpected action ${action}`);
    }),
  );
  const first = await engine.readSession({ containerId: "ctr_a", jobId: POSTED.jobId });
  expect(first.ok).toBe(true);
  if (!first.ok) return;
  expect(first.value.job.state).toBe("started");
  expect(first.value.session).toBeNull();
  expect(first.value.activity?.inferenceUsage?.calls).toBe(3);
  const next = await engine.readSession({ containerId: "ctr_a", jobId: POSTED.jobId });
  expect(next.ok).toBe(true);
  if (!next.ok) return;
  expect(next.value.job.state).toBe("exited");
  expect(next.value.session?.finalMessage).toBe("Analysis complete.");
  expect(next.value.job.result?.usage?.inference).toEqual(inference);
  const cancelled = await engine.cancelSession({ containerId: "ctr_a", jobId: POSTED.jobId });
  expect(cancelled.ok).toBe(true);
  if (!cancelled.ok) return;
  expect(cancelled.value.jobId).toBe(POSTED.jobId);
  expect(cancelled.value.result?.usage?.inference).toEqual(inference);
});

test("missing activity cannot erase a confirmed live session", async () => {
  const engine = codeEngine(
    actions(({ action }) => {
      if (action === "readSession")
        return {
          job: { ...POSTED, state: "started" },
          session: null,
          silence: "omp_session_running",
        };
      throw new ActionCallError("dependency_unavailable: atyrode.code");
    }),
  );
  const read = await engine.readSession({ containerId: "ctr_a", jobId: POSTED.jobId });
  expect(read.ok).toBe(true);
  if (!read.ok) return;
  expect(read.value.job.state).toBe("started");
  expect(read.value.activity).toBeUndefined();
});

/*
  THE SPEND GATE (#255).

  A run spends a model; the model is paid for by an account on the Code profile; the key behind
  that account is the machine broker's and has never been in this process. What these pin is the
  half that was missing — that a deployment which installed no account cannot reach the call that
  spends — and they pin it by COUNTING THE INVOCATION rather than by reading a return value,
  because "refused with a good message" and "refused after it already reached Code" are the same
  value and only one of them is the property.
*/
describe("what a profile may spend, asked before anything is posted", () => {
  test("a profile Code resolved with no account refuses, and the session door is never reached", async () => {
    const slice = actions((args) => {
      if (args.action === "listProfiles") return { profiles: [profile({ accounts: [] })] };
      throw new Error("the spend must be unreachable when the deployment installed no account");
    });

    const answered = await codeEngine(slice).runSession({
      profile: { containerId: "ctr_a", expectedRevision: 4 },
      machineId: "m-dev-01",
      prompt: "read the material",
    });

    // THE ASSERTION IS THE COUNT. Code was asked what the profile spends and was never asked to
    // post: one call, and it is the free one.
    expect(slice.calls.map((call) => call.action)).toEqual(["listProfiles"]);
    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    expect(answered.code).toBe(ENGINE_REFUSALS.noAccount);
    // A sentence a person acts on: which profile, what is missing, and where to go and fix it.
    expect(answered.refused).toContain("ctr_a");
  });

  test("a container the roster does not hold is a stale profile, and still posts nothing", async () => {
    const slice = actions((args) => {
      if (args.action === "listProfiles") return { profiles: [profile({ containerId: "ctr_b" })] };
      throw new Error("a container Code does not publish must never be posted for");
    });

    const answered = await codeEngine(slice).runSession({
      profile: { containerId: "ctr_a", expectedRevision: 4 },
      machineId: "m-dev-01",
      prompt: "read the material",
    });

    expect(slice.calls.map((call) => call.action)).toEqual(["listProfiles"]);
    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    // The panel's own remedy, which is the one `engine_stale_profile` already names.
    expect(answered.code).toBe(ENGINE_REFUSALS.staleProfile);
  });

  test("a moved revision is stale, not evidence that the requested profile has no account", async () => {
    const slice = actions((args) => {
      if (args.action === "listProfiles") return { profiles: [profile({ accounts: [] })] };
      throw new Error("a stale profile must not post a session");
    });

    const answered = await codeEngine(slice).runSession({
      profile: { containerId: "ctr_a", expectedRevision: 3 },
      machineId: "m-dev-01",
      prompt: "read the material",
    });

    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    expect(answered.code).toBe(ENGINE_REFUSALS.staleProfile);
    expect(slice.calls.map((call) => call.action)).toEqual(["listProfiles"]);
  });

  test("an account observed during preflight does not override Code's later refusal", async () => {
    const slice = actions((args) => {
      if (args.action === "listProfiles") return { profiles: [profile({})] };
      return hostRefusal(
        "refused: atyrode.babel -> atyrode.code.runSession (code_stale_preferences)",
      )();
    });

    const answered = await codeEngine(slice).runSession({
      profile: { containerId: "ctr_a", expectedRevision: 4 },
      machineId: "m-dev-01",
      prompt: "read the material",
    });

    expect(answered.ok).toBe(false);
    if (answered.ok) return;
    expect(answered.code).toBe(ENGINE_REFUSALS.staleProfile);
    expect(slice.calls.map((call) => call.action)).toEqual(["listProfiles", "runSession"]);
  });

  test("an unresolved profile is posted, because an empty list Code could not resolve is not 'spends nothing'", async () => {
    const slice = actions((args) =>
      args.action === "listProfiles"
        ? { profiles: [profile({ accounts: [], resolved: false })] }
        : POSTED,
    );

    const answered = await codeEngine(slice).runSession({
      profile: { containerId: "ctr_a", expectedRevision: 4 },
      machineId: "m-dev-01",
      prompt: "read the material",
    });

    // Code stores account choices as EXCLUSIONS, so with no live observation there is no list to
    // give. Refusing here would be Babel inferring an account from Code's silence, which is the
    // one thing the profile contract says a caller may never do — Code decides, as it always did.
    expect(answered.ok).toBe(true);
    expect(slice.calls.map((call) => call.action)).toEqual(["listProfiles", "runSession"]);
  });
});

test.each([
  [
    "transport loss",
    () => {
      throw new Error("posting response lost");
    },
  ],
  ["malformed reply", () => ({ jobId: POSTED.jobId })],
  [
    "post-handler refusal",
    hostRefusal("refused: atyrode.babel -> atyrode.code.runSession (code_omp_review_changed)"),
  ],
] as const)("a %s after dispatch leaves spending unconfirmed", async (_name, post) => {
  const engine = codeEngine(
    actions((args) => (args.action === "listProfiles" ? { profiles: [profile({})] } : post())),
  );
  const answer = await engine.runSession({
    profile: { containerId: "ctr_a", expectedRevision: 4 },
    machineId: "m-dev-01",
    prompt: "read the material",
  });
  expect(answer).toMatchObject({ ok: false, code: ENGINE_REFUSALS.unconfirmed });
});

test("a locally invalid posting request is refused without pretending its spending is unknown", async () => {
  let posts = 0;
  const engine = codeEngine(
    actions((args) => {
      if (args.action === "listProfiles") return { profiles: [profile({})] };
      posts += 1;
      return POSTED;
    }),
  );
  const answer = await engine.runSession({
    profile: { containerId: "ctr_a", expectedRevision: 4 },
    machineId: "",
    prompt: "read the material",
  });
  expect(answer).toMatchObject({ ok: false, code: ENGINE_REFUSALS.refused });
  expect(posts).toBe(0);
});
