/*
  The engine boundary against the fake engine: the protocol Babel speaks, and every refusal it
  owes. What each case is here to hold is the obligation in its name — the admission refusals in
  particular are checked by the ABSENCE of the prompt file, because "refused before any prompt is
  written" is a claim about what the far side saw and nothing else can observe it.

  WHAT #279 CHANGED HERE. Admission used to read Code's runtime-info sidecar, so the cases
  were about a DECLARATION: a weak containment claim, a missing sidecar, a profile mismatch,
  credential-shaped metadata. Babel launches omp itself now and admission reads FACTS — the job's
  own home and the job's own environment — so those cases are gone rather than re-pinned, because
  the contract they held no longer exists. What stands in their place is below: the boundary this
  process observes around itself, and the two files the OWNER materializes from the inference
  binding.

  Every run here is therefore OUTSIDE a job sandbox (`HOME` is not `/home/job`), which is exactly
  why the default requirement is relaxed: the wire cases are about the wire, and the boundary has
  its own two cases that assert the refusal instead of working around it.
*/

import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { writeJobHome } from "../test/fixtures.ts";
import { runEngineJob, type EngineJob, type EngineOutcome } from "./client.ts";
import {
  engineArgv,
  ENGINE_FAILURES,
  LAUNCH_FAILURES,
  LAUNCH_REPORT_SCHEMA,
  SANDBOXED_RUN,
  UNSANDBOXED,
  type EngineLimits,
  type SessionRef,
} from "./launch.ts";

const FIXTURE = join(import.meta.dir, "fakeengine.ts");

/** The session a run is asked to be; the launch report records it verbatim. */
const SESSION: SessionRef = {
  model: "anthropic/claude-sonnet-5",
  thinking: "high",
  account: { provider: "anthropic", identityKey: "victorballu@gmail.com" },
};

const directories: string[] = [];

afterEach(async () => {
  for (const directory of directories.splice(0)) await rm(directory, { recursive: true, force: true });
});

/**
 * A job home as the OWNER leaves one, or — unbound — an empty directory: the two states
 * admission decides between.
 */
async function jobHome(bound: boolean): Promise<string> {
  const parent = await mkdtemp(join(tmpdir(), "babel-job-home-"));
  directories.push(parent);
  return bound ? await writeJobHome(parent) : parent;
}

/** One run against the fixture. `fake` are the fixture's flags, which precede Babel's in argv. */
async function run(
  fake: readonly string[],
  job: Partial<EngineJob> = {},
  options: { bound?: boolean; limits?: Partial<EngineLimits> } = {},
): Promise<{ outcome: EngineOutcome; promptPath: string; home: string }> {
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const promptPath = join(directory, "prompt.txt");
  const home = await jobHome(options.bound ?? true);
  const outcome = await runEngineJob(
    {
      runId: "run_test",
      session: SESSION,
      prompt: "the job document",
      submitSchema: { type: "object", additionalProperties: true },
      requirement: UNSANDBOXED,
      ...job,
    },
    {
      launch: {
        binary: process.execPath,
        args: [FIXTURE, "--fake-prompt-out", promptPath, ...fake],
        session: SESSION,
        home,
        cwd: directory,
      },
      // The graces are SHORT on purpose: every budget here bounds silence or departure, and a
      // fixture that misbehaves on purpose must be caught inside one test's own timeout rather
      // than by the runner killing a dangling process out from under the assertion.
      limits: { handshakeMs: 15_000, idleMs: 15_000, exitGraceMs: 1_000, terminateGraceMs: 200, ...options.limits },
    },
  );
  return { outcome, promptPath, home };
}

test("a well-behaved engine negotiates, registers, is prompted, and submits its result", async () => {
  const { outcome, promptPath } = await run(["--fake-submit-json", '{"answer":"forty-two"}']);

  expect(outcome.closure).toBe("completed");
  expect(outcome.failure).toBeNull();
  expect(outcome.result).toEqual({ answer: "forty-two" });
  expect(outcome.submissions).toBe(1);
  expect(outcome.prompted).toBe(true);
  expect(outcome.exitCode).toBe(0);
  // BABEL'S OWN LAUNCH REPORT is what the receipt states a run under; the post-exit copy adds
  // the exit status and the retry count.
  expect(outcome.report?.schema).toBe(LAUNCH_REPORT_SCHEMA);
  expect(outcome.report?.session).toEqual(SESSION);
  expect(outcome.finished?.finished).toBe(true);
  expect(outcome.finished?.exit_code).toBe(0);
  expect(outcome.finished?.failure).toBe("");
  expect(outcome.finished?.retries).toBe(0);
  // The model the run asked for is the first name in the trail; a model that answered moves it.
  expect(outcome.models[0]).toBe(SESSION.model);
  expect(outcome.usage?.totalTokens).toBe(1540);
  expect(outcome.usage?.costUsd).toBeCloseTo(0.0123, 6);
  // The prompt is the one thing that reaches the model, and it reached it exactly once.
  expect(await Bun.file(promptPath).text()).toBe("the job document");
});

test("a run outside a Manifold job sandbox is refused before any prompt is written", async () => {
  // The boundary is OBSERVED, not declared: this test process's `HOME` is not the job's private
  // home, so a run that demands containment is refused — and the refusal names the fact rather
  // than repeating a claim somebody made about themselves.
  const { outcome, promptPath } = await run(["--fake-submit-json", "{}"], {
    requirement: SANDBOXED_RUN,
  });

  expect(outcome.closure).toBe("failed");
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.containment);
  expect(outcome.failure?.message).toContain("not inside a Manifold job sandbox");
  // What a refused engine saw is the session it was launched under and nothing else.
  expect(await Bun.file(promptPath).exists()).toBe(false);
  expect(outcome.result).toBeNull();
  expect(outcome.prompted).toBe(false);
  expect(outcome.report?.session).toEqual(SESSION);
});

test("a job whose inference binding was never materialized is refused by name", async () => {
  // ADR 0038's boundary, checked on the facts: the two files the OWNER writes out of the
  // `atyrode.babel.inference` binding are the only route from this sandbox to a model, so a home
  // without them is a run with no metered lane — and it is named, not described, because
  // `inference_unbound` tells an operator to look at the binding and nothing else does.
  const { outcome, promptPath } = await run(["--fake-submit-json", "{}"], {}, { bound: false });

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.inference);
  expect(outcome.failure?.named).toBe(LAUNCH_FAILURES.inferenceUnbound);
  expect(outcome.failure?.message).toContain("models.yml");
  expect(outcome.finished?.failure).toBe(LAUNCH_FAILURES.inferenceUnbound);
  expect(await Bun.file(promptPath).exists()).toBe(false);
});

test("argv, the environment and the receipt carry nothing that could reach a provider", async () => {
  // The one property this whole lane exists for. The session travels as two files in the job's
  // private home, which the OWNER wrote and spliced the bearer into, so the command line — which
  // any process listing on the host can read — says only which mode omp runs in and which files
  // to read, and the receipt's report says which account was spent and never how.
  const { outcome, home } = await run(["--fake-submit-json", '{"ok":true}']);
  const argv = engineArgv({
    binary: "/runtime/bin/omp",
    session: SESSION,
    home: "/home/job",
    cwd: "/home/job/.run/omp",
  });

  expect(argv).toEqual([
    "--mode",
    "rpc",
    "--no-tools",
    "--no-lsp",
    "--no-session",
    "--no-extensions",
    "--no-rules",
    "--no-skills",
    "--no-title",
    "--auto-approve",
    "--config",
    "/home/job/.omp/agent/config.yml",
    "--cwd",
    "/home/job/.run/omp",
  ]);
  // Not the model, not the account, not a key: nothing of the session is on the command line.
  expect(argv.join(" ")).not.toContain(SESSION.model);
  expect(argv.join(" ")).not.toContain(SESSION.account.identityKey);
  // HOME points at the job's home so omp discovers exactly the two files the owner wrote, and
  // the environment is an ALLOW-LIST rather than a filter of this process's own.
  const bearer = await Bun.file(join(home, ".omp", "agent", "models.yml")).text();
  expect(bearer).not.toBe("");
  expect(JSON.stringify(outcome.report)).not.toContain("apiKey");
  expect(JSON.stringify(outcome.finished)).not.toContain("apiKey");
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
  const { outcome } = await run(["--fake-ignore-eof", "--fake-submit-json", '{"answer":"ok"}']);

  // The result was accepted and is kept; the verdict is still that the tree had to be killed.
  expect(outcome.result).toEqual({ answer: "ok" });
  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.lingered);
});

test("an engine that goes silent is stalled rather than waited on forever", async () => {
  const { outcome } = await run(["--fake-stall-after", "prompt"], {}, { limits: { idleMs: 300 } });

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.stalled);
});

test("an engine that never becomes ready fails the handshake, and says what it said", async () => {
  const { outcome } = await run(
    ["--fake-no-ready", "--fake-stall-after", "ready"],
    {},
    { limits: { handshakeMs: 300 } },
  );

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.handshake);
  expect(outcome.failure?.message).toContain("no ready frame within 300ms");
  // #277: an engine that said nothing is honestly described as one that said nothing. The NAMED
  // case is the one beside it, where the diagnostics carry a cause.
  expect(outcome.failure?.named).toBe("");
});

test("a handshake that dies with the broker down is named broker_unavailable (#277)", async () => {
  // The two hours of 2026-09-13 this closes: every run failed as "closed its stdout before a
  // ready frame, exit status -1" while `account_unavailable` sat one line earlier on stderr and
  // nowhere in any receipt. The cause is read off the engine's own diagnostics and named.
  const { outcome } = await run([
    "--fake-die-before-ready",
    "--fake-stderr",
    "engine: the account snapshot is unavailable, so the run would launch with no account policy",
  ]);

  expect(outcome.failure?.code).toBe(ENGINE_FAILURES.handshake);
  expect(outcome.failure?.named).toBe(LAUNCH_FAILURES.brokerUnavailable);
  expect(outcome.finished?.failure).toBe(LAUNCH_FAILURES.brokerUnavailable);
  expect(outcome.finished?.reason).toContain("account snapshot is unavailable");
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
  const home = await jobHome(true);
  const directory = await mkdtemp(join(tmpdir(), "babel-client-test-"));
  directories.push(directory);
  const deps = {
    launch: { binary: process.execPath, args: [FIXTURE], session: SESSION, home, cwd: directory },
  };
  const job: EngineJob = {
    runId: "run_invalid",
    session: SESSION,
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

test("argv is the operator's arguments, then Babel's fixed omp flags", () => {
  // The operator's own arguments come FIRST, so a machine that needs one can add it without
  // being able to displace the lockdown behind it: `--no-tools` and its four siblings are what
  // make the session's registry exactly the host tools Babel registers.
  expect(
    engineArgv({
      binary: "/runtime/bin/omp",
      args: ["--profile", "babel"],
      session: SESSION,
      home: "/home/job",
      cwd: "/home/job/.run/omp",
    }).slice(0, 4),
  ).toEqual(["--profile", "babel", "--mode", "rpc"]);
});
