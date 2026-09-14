import { describe, expect, test } from "bun:test";
import { ActionCallError } from "@manifold/plugin-kit/errors";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import { ENGINE_REFUSALS, MATERIAL_OUTPUT } from "../../contract.ts";
import {
  ENGINE_WITHOUT_ACTIONS,
  codeEngine,
  materialInput,
  type ActionsSlice,
} from "./session.ts";

/*
  BABEL'S SIDE OF CODE'S DOORS, held to the two roads a refusal arrives by (ADR 0041).

  The HOST refuses the EDGE as a REJECTION whose message is `<class>: <offenders>`; CODE refuses
  the REQUEST as a RESOLVED `{ refused: "code_…" }`. Every test here uses the REAL sentences —
  the ones the kit and Code publish — because the whole of this module is a translation of
  them, and a fake that invented its own wording would be testing the translation against
  itself.
*/

/** An `actions` slice that throws whatever the host would, or resolves whatever Code would. */
function actions(answer: () => unknown): ActionsSlice & { readonly calls: unknown[] } {
  const calls: unknown[] = [];
  return {
    calls,
    call: async (args) => {
      calls.push(args);
      return await Promise.resolve(answer());
    },
  };
}

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
      hostRefusal(`refused: atyrode.babel -> ${CODE_PLUGIN_ID}.runSession (code_stale_preferences)`),
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

test("a session is posted with its material bound, and Code's own schema takes the request", async () => {
  const slice = actions(() => ({
    jobId: "omp_7",
    machineId: "m-dev-01",
    operationId: "atyrode.omp.session",
    pluginId: "atyrode.omp",
    installationRevision: "1",
    artifactSha256: "a".repeat(64),
    inputDigest: "b".repeat(64),
    resourceBindingDigest: "c".repeat(64),
    inputs: [
      { name: MATERIAL_OUTPUT, from: { jobId: "job_1_material", output: MATERIAL_OUTPUT } },
    ],
    state: "queued",
    nextInputSeq: null,
    result: null,
    authority: {
      origin: { kind: "action", traceId: "t1", door: null },
      requester: "operator",
      executor: null,
      decision: null,
    },
  }));

  const answered = await codeEngine(slice).runSession({
    profile: { containerId: "ctr_a", expectedRevision: 4 },
    machineId: "m-dev-01",
    prompt: "read the material",
    prepareJobId: "job_1_material",
  });

  expect(answered.ok).toBe(true);
  if (!answered.ok) return;
  expect(answered.value.jobId).toBe("omp_7");

  /*
    THE BINDING IS WHAT PUTS BYTES AT `/inputs/material` (ADR 0044). The prompt tells the model
    everything it may read is there; a request posted without this would send it to an empty
    directory and have Babel record the answer as evidence-backed analysis. The input went
    through CODE'S OWN schema on the way out — `codeEngine` parses with it before calling — so
    a shape Code would refuse is this plugin's bug and is caught here, not on a machine.
  */
  expect(slice.calls).toHaveLength(1);
  const sent = slice.calls[0];
  expect(sent !== null && typeof sent === "object" && "input" in sent ? sent.input : null).toEqual({
    containerId: "ctr_a",
    machineId: "m-dev-01",
    expectedRevision: 4,
    prompt: "read the material",
    inputs: [
      { name: MATERIAL_OUTPUT, from: { jobId: "job_1_material", output: MATERIAL_OUTPUT } },
    ],
  });
});

test("the material input names one binding: prepare's own sealed output", () => {
  expect(materialInput("job_7_material")).toEqual({
    inputs: [
      { name: MATERIAL_OUTPUT, from: { jobId: "job_7_material", output: MATERIAL_OUTPUT } },
    ],
  });
});
