/*
  The engine boundary against the fake engine: the protocol Babel speaks, and every refusal it
  owes. What each case is here to hold is the obligation in its name — the containment refusal in
  particular is checked by the ABSENCE of the prompt file, because "refused before any prompt is
  written" is a claim about what the far side saw and nothing else can observe it.
*/

import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runEngineJob, type EngineJob, type EngineOutcome } from "./client.ts";
import { describeProfile, engineArgv, ENGINE_FAILURES, UNSANDBOXED } from "./launch.ts";

const FIXTURE = join(import.meta.dir, "fakeengine.ts");
const PROFILE = { id: "analysis", revision: 3 };

const directories: string[] = [];

afterEach(async () => {
  for (const directory of directories.splice(0)) await rm(directory, { recursive: true, force: true });
});

/** One run against the fixture. `fake` are the fixture's flags, which precede `engine` in argv. */
async function run(
  fake: readonly string[],
  job: Partial<EngineJob> = {},
): Promise<{ outcome: EngineOutcome; promptPath: string }> {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const promptPath = join(directory, "prompt.txt");
  const outcome = await runEngineJob(
    {
      runId: "run_test",
      profile: PROFILE,
      prompt: "the job document",
      submitSchema: { type: "object", additionalProperties: true },
      ...job,
    },
    {
      launch: {
        binary: process.execPath,
        args: [FIXTURE, "--fake-prompt-out", promptPath, ...fake],
        profile: PROFILE,
        runtimeInfoPath: join(directory, "runtime.json"),
      },
      limits: { handshakeMs: 15_000, idleMs: 15_000, exitGraceMs: 5_000 },
    },
  );
  return { outcome, promptPath };
}

test("a well-behaved engine negotiates, registers, is prompted, and submits its result", async () => {
  const { outcome, promptPath } = await run(["--fake-submit-json", '{"answer":"forty-two"}']);

  expect(outcome.closure).toBe("completed");
  expect(outcome.failure).toBeNull();
  expect(outcome.result).toEqual({ answer: "forty-two" });
  expect(outcome.submissions).toBe(1);
  expect(outcome.prompted).toBe(true);
  expect(outcome.exitCode).toBe(0);
  // The runtime report is what the receipt states a run under, and Code rewrites it after exit.
  expect(outcome.runtime?.profile).toEqual(PROFILE);
  expect(outcome.runtime?.metadata?.["model"]).toBe("synthetic-1");
  expect(outcome.finished?.resources?.cpu_seconds).toBe(0.42);
  expect(outcome.usage?.totalTokens).toBe(1540);
  expect(outcome.usage?.costUsd).toBeCloseTo(0.0123, 6);
  // The prompt is the one thing that reaches the model, and it reached it exactly once.
  expect(await Bun.file(promptPath).text()).toBe("the job document");
});

test("a refused containment stops before any prompt is written", async () => {
  const { outcome, promptPath } = await run(["--fake-containment", "weak", "--fake-submit-json", "{}"]);

  expect(outcome.closure).toBe("failed");
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.containment);
  expect(outcome.failure?.message).toContain("network default-deny");
  expect(outcome.failure?.message).toContain("resource ceilings");
  // What a refused engine saw is the profile it was launched under and nothing else.
  expect(await Bun.file(promptPath).exists()).toBe(false);
  expect(outcome.result).toBeNull();
  expect(outcome.prompted).toBe(false);
  expect(outcome.runtime?.profile).toEqual(PROFILE);
});

test("a launch with no containment declaration at all is refused", async () => {
  const { outcome, promptPath } = await run(["--fake-containment", "missing", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.containment);
  expect(outcome.failure?.message).toContain("no containment");
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("an unnamed sandbox backend is refused even when it claims every property", async () => {
  const { outcome } = await run(["--fake-containment", "none", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.containment);
  expect(outcome.failure?.message).toContain("no sandbox backend");
});

test("a run that relaxed its requirement is admitted under a weaker sandbox — but never under none", async () => {
  const relaxed = await run(["--fake-containment", "weak", "--fake-submit-json", '{"ok":true}'], {
    requirement: UNSANDBOXED,
  });
  expect(relaxed.outcome.closure).toBe("completed");
  expect(relaxed.outcome.result).toEqual({ ok: true });
  expect(await Bun.file(relaxed.promptPath).exists()).toBe(true);

  // Relaxing what the run demands never relaxes the obligation to declare: a launch Code said
  // nothing about is one whose boundary nobody stated, and it is refused either way.
  const undeclared = await run(["--fake-containment", "missing", "--fake-submit-json", "{}"], {
    requirement: UNSANDBOXED,
  });
  expect(undeclared.outcome.failure?.code).toBe(ENGINE_FAILURES.containment);
  expect(await Bun.file(undeclared.promptPath).exists()).toBe(false);
});

test("a missing runtime-info sidecar means the process on the pipe is not Code", async () => {
  const { outcome, promptPath } = await run(["--fake-no-runtime-info", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.runtimeInfo);
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("credential-shaped profile metadata is refused whole", async () => {
  const { outcome, promptPath } = await run(["--fake-secret-metadata", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.secretDeclared);
  expect(outcome.failure?.message).toContain("api_key");
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("an engine running under another profile than the job named is refused", async () => {
  const { outcome, promptPath } = await run(["--fake-profile", "other@9", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.profileMismatch);
  expect(outcome.failure?.message).toContain("other@9");
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("an engine that offers no v2 transport is refused", async () => {
  const { outcome } = await run(["--fake-ready-versions", "1", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.protocol);
  expect(outcome.failure?.message).toContain("Babel needs 2");
});

test("a v2 chunk sequence reassembles into the logical frame it carries", async () => {
  // Every frame after negotiation arrives in three chunks, including the submission, so the
  // result is proof the reader reassembled rather than read a line.
  const payload = JSON.stringify({ answer: "x".repeat(4096) });
  const { outcome } = await run(["--fake-chunk", "--fake-submit-json", payload]);

  expect(outcome.closure).toBe("completed");
  expect(outcome.result).toEqual(JSON.parse(payload));
});

test("a malformed frame ends the run without resynchronizing", async () => {
  const { outcome } = await run(["--fake-bad-frame", "malformed", "--fake-submit-json", "{}"]);

  expect(outcome.closure).toBe("failed");
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.malformedFrame);
});

test("an interrupted chunk sequence is a decode failure", async () => {
  const { outcome } = await run(["--fake-bad-frame", "chunk-short", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.malformedFrame);
  expect(outcome.failure?.message).toContain("chunk sequence");
});

test("a refused submission is answered, not fatal, and leaves the run with no result", async () => {
  const { outcome } = await run(["--fake-submit-json", '{"answer":"no"}'], {
    accept: () => "the answer must be a number",
  });

  expect(outcome.closure).toBe("failed");
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.noResult);
  expect(outcome.result).toBeNull();
  expect(outcome.submissions).toBe(1);
  // The reason the model was given is in the trail; the served payload never is.
  const submit = outcome.tools.find((decision) => decision.tool === "babel_submit_result");
  expect(submit?.allowed).toBe(false);
  expect(submit?.reason).toContain("the answer must be a number");
});

test("a second submission replaces the first", async () => {
  let seen = 0;
  const { outcome } = await run(["--fake-submit-json", '{"n":1}', "--fake-submit-json", '{"n":2}'], {
    accept: () => {
      seen += 1;
      return "";
    },
  });

  expect(seen).toBe(2);
  expect(outcome.submissions).toBe(2);
  expect(outcome.result).toEqual({ n: 2 });
});

test("an engine the host refuses a tool registration to does not get prompted", async () => {
  const { outcome, promptPath } = await run(["--fake-drop-tool", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.commandFailed);
  expect(outcome.failure?.message).toContain("babel_submit_result");
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("a refused command ends the run with the engine's own reason", async () => {
  const { outcome } = await run(["--fake-refuse-command", "negotiate_protocol", "--fake-submit-json", "{}"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.commandFailed);
  expect(outcome.failure?.message).toContain("refused by fixture");
});

test("a prompt the engine completed with no model turn is not a result", async () => {
  const { outcome } = await run(["--fake-local-prompt"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.noResult);
  expect(outcome.failure?.message).toContain("without a model turn");
  expect(outcome.prompted).toBe(false);
});

test("a turn that submits nothing is a run with no result, not a crash", async () => {
  const { outcome } = await run(["--fake-no-submit"]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.noResult);
  expect(outcome.submissions).toBe(0);
  expect(outcome.exitCode).toBe(0);
});

test("a non-zero exit after an accepted result is a dirty exit, and the result is kept", async () => {
  const { outcome } = await run(["--fake-exit", "3", "--fake-submit-json", '{"answer":"kept"}']);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.dirtyExit);
  expect(outcome.result).toEqual({ answer: "kept" });
  expect(outcome.exitCode).toBe(3);
});

test("an engine that outstays its stdin is killed and reported as lingering", async () => {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const outcome = await runEngineJob(
    {
      runId: "run_linger",
      profile: PROFILE,
      prompt: "the job document",
      submitSchema: { type: "object", additionalProperties: true },
    },
    {
      launch: {
        binary: process.execPath,
        args: [FIXTURE, "--fake-ignore-eof", "--fake-submit-json", '{"answer":"ok"}'],
        profile: PROFILE,
        runtimeInfoPath: join(directory, "runtime.json"),
      },
      limits: { handshakeMs: 10_000, idleMs: 10_000, exitGraceMs: 300, terminateGraceMs: 200 },
    },
  );

  // The result was accepted and is kept; the verdict is still that the tree had to be killed.
  expect(outcome.result).toEqual({ answer: "ok" });
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.lingered);
  // Nothing the run started outlives it: the report Code writes on the way out never appeared.
  expect(outcome.finished).toBeNull();
});

test("an engine that goes silent is stalled rather than waited on forever", async () => {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const outcome = await runEngineJob(
    {
      runId: "run_stall",
      profile: PROFILE,
      prompt: "the job document",
      submitSchema: { type: "object" },
    },
    {
      launch: {
        binary: process.execPath,
        args: [FIXTURE, "--fake-stall-after", "prompt"],
        profile: PROFILE,
        runtimeInfoPath: join(directory, "runtime.json"),
      },
      limits: { handshakeMs: 10_000, idleMs: 300, exitGraceMs: 1_000, terminateGraceMs: 200 },
    },
  );

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.stalled);
});

test("an engine that never becomes ready fails the handshake", async () => {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const outcome = await runEngineJob(
    {
      runId: "run_ready",
      profile: PROFILE,
      prompt: "the job document",
      submitSchema: { type: "object" },
    },
    {
      launch: {
        binary: process.execPath,
        args: [FIXTURE, "--fake-no-ready", "--fake-stall-after", "ready"],
        profile: PROFILE,
        runtimeInfoPath: join(directory, "runtime.json"),
      },
      limits: { handshakeMs: 300, idleMs: 300, exitGraceMs: 1_000, terminateGraceMs: 200 },
    },
  );

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.handshake);
});

test("an unknown frame is counted, not refused, and an extension dialog is cancelled", async () => {
  const { outcome } = await run([
    "--fake-unknown-frame",
    "--fake-extension-ui",
    "--fake-submit-json",
    '{"answer":"ok"}',
  ]);

  expect(outcome.closure).toBe("completed");
  expect(outcome.unknownFrames).toEqual(["telemetry_sample"]);
});

test("stats the engine refuses leave the run successful with no usage", async () => {
  const { outcome } = await run(["--fake-stats-refused", "--fake-submit-json", '{"answer":"ok"}']);

  expect(outcome.closure).toBe("completed");
  expect(outcome.usage).toBeNull();
  expect(outcome.result).toEqual({ answer: "ok" });
});

test("a job with no prompt or a tool with no description is a caller defect, not a launch", async () => {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const deps = {
    launch: {
      binary: process.execPath,
      args: [FIXTURE],
      profile: PROFILE,
      runtimeInfoPath: join(directory, "runtime.json"),
    },
  };
  const job: EngineJob = {
    runId: "run_invalid",
    profile: PROFILE,
    prompt: "   ",
    submitSchema: { type: "object" },
  };
  await expect(runEngineJob(job, deps)).rejects.toThrow("no prompt");
  await expect(
    runEngineJob(
      {
        ...job,
        prompt: "the job document",
        tools: [{ name: "babel_corpus_search", description: "  ", parameters: { type: "object" } }],
      },
      deps,
    ),
  ).rejects.toThrow("no description");
});

test("describing a profile resolves it without launching an engine", async () => {
  // Watch states what will run before the first byte, from the same source the receipt records.
  const configuration = await describeProfile({
    binary: process.execPath,
    args: [FIXTURE],
    profile: PROFILE,
    runtimeInfoPath: "/unused: --describe writes the document on stdout",
  });

  expect(configuration.profile).toEqual(PROFILE);
  expect(configuration.disclosure).toBe("local");
  expect(configuration.costPer1k).toEqual({ input: 0.001, output: 0.002 });
  expect(configuration.metadata["model"]).toBe("synthetic-1");
  expect(configuration.worker.name).toBe("fakeengine");

  // A profile that declares credential-shaped metadata is refused whole, never redacted.
  await expect(
    describeProfile({
      binary: process.execPath,
      args: [FIXTURE, "--fake-secret-metadata"],
      profile: PROFILE,
      runtimeInfoPath: "/unused",
    }),
  ).rejects.toThrow("declares a credential");
});

test("argv is the operator's arguments, then the subcommand, then Babel's flags", () => {
  const spec = {
    binary: "code",
    args: ["--config", "/etc/code.toml"],
    profile: PROFILE,
    runtimeInfoPath: "/tmp/runtime.json",
  };
  expect(engineArgv(spec)).toEqual([
    "--config",
    "/etc/code.toml",
    "engine",
    "--profile",
    "analysis@3",
    "--runtime-info",
    "/tmp/runtime.json",
  ]);
  // A describe resolves a profile and writes no sidecar, so it carries no runtime-info flag.
  expect(engineArgv(spec, true)).toEqual([
    "--config",
    "/etc/code.toml",
    "engine",
    "--describe",
    "--profile",
    "analysis@3",
  ]);
});
