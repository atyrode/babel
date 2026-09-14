/*
  THE REAL ENGINE. One `explore` run that launches the actual `omp --mode rpc` binary — the same
  argv `machine/engine/launch.ts` composes, the same two files in the job's private home, the same
  RPC conversation — against a canned pi-native endpoint standing where the machine owner's
  metered proxy stands.

  Everything else in this suite drives `fakeengine.ts`, which is honest about the wire because it
  is written from literals; what it cannot prove is that the wire is OMP's. This test is the one
  that does, and it is the only place in the repository where a second process is a real product:

  - `providers.anthropic.baseUrl` points at a loopback server, which is what ADR 0038's binding
    materializes (`atyrode.omp.execution` writes exactly `{baseUrl, apiKey, transport:
    "pi-native", discovery: {type: "proxy"}}`), so omp discovers its models through
    `GET /v1/models` and streams a turn through `POST /v1/pi/stream` and has no other route out;
  - the stream is the SDK's own canonical `AssistantMessageEvent` sequence
    (`@oh-my-pi/pi-ai/providers/pi-native-client.ts` reads it back over SSE): one assistant turn
    that calls `babel_submit_result`, then — after Babel answers the host tool call — a second
    turn that stops, and usage on both;
  - no provider credential exists anywhere in the run. The bearer in `models.yml` is a literal
    this file wrote and the loopback server ignores it.

  It is SKIPPED, by name, when `omp` is not on PATH: the binary is a pinned job artifact on a real
  machine (`manifest.json` `machine.tools.omp`) and a checkout has no obligation to carry one.
*/

import { afterEach, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WorkerProgressSchema } from "@manifold/protocol";
import { RUN_STAGES } from "../contract.ts";
import { explore, ExploreInputSchema } from "./explore.ts";
import type { OutputFile, OutputSink } from "./output.ts";
import { openProgress } from "./progress.ts";
import type { Receipt } from "../contract.ts";

const OMP = Bun.which("omp");
/**
 * Why this test did not run, or "". It is in the test's own name AND on stderr, because the
 * whole value of this file is that somebody notices when it stops proving anything: bun prints
 * a skip as a count and not as a name.
 */
const UNAVAILABLE =
  OMP === null ? "omp is not on PATH; the engine is a pinned job artifact, not a checkout's" : "";
if (UNAVAILABLE !== "") console.warn(`babel: the real-engine smoke test is skipped: ${UNAVAILABLE}`);

const PROVIDER = "anthropic";
const MODEL = "claude-sonnet-5";
const SESSION = {
  model: `${PROVIDER}/${MODEL}`,
  thinking: "low" as const,
  account: { provider: PROVIDER, identityKey: "smoke@example.invalid" },
};

const RECIPE = {
  id: "read-whats-new",
  version: 3,
  title: "Read what is new",
  body: "Read the sessions and say what changed.",
  stages: ["explore" as const],
};

/** The one submission the canned turn makes: the smallest result the explore stage admits. */
const SUBMISSION = {
  candidates: [
    {
      ref: "c1",
      hypothesis: {
        statement: "the engine's own wire is what this run exercised",
        origin_cues: ["the prompt named one tool and the turn called it"],
        novelty: 0.1,
        priority: 0.1,
      },
    },
  ],
};

const directories: string[] = [];

afterEach(async () => {
  for (const directory of directories.splice(0)) await rm(directory, { recursive: true, force: true });
});

class MemorySink implements OutputSink {
  readonly files: Partial<Record<OutputFile, readonly unknown[]>> = {};

  async write(file: OutputFile, rows: readonly unknown[]): Promise<void> {
    this.files[file] = rows;
  }

  async receipt(_receipt: Receipt): Promise<void> {}
}

/** The usage the endpoint reports per turn; the receipt sums both turns. */
const USAGE = {
  input: 120,
  output: 40,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 160,
  cost: { input: 0.00024, output: 0.0004, cacheRead: 0, cacheWrite: 0, total: 0.00064 },
};

function assistant(content: readonly unknown[], stopReason: string): Record<string, unknown> {
  return {
    role: "assistant",
    content: [...content],
    api: "anthropic-messages",
    provider: PROVIDER,
    model: MODEL,
    usage: USAGE,
    stopReason,
    timestamp: Date.now(),
  };
}

/** The SDK reads this back with `readSseJson`: one JSON event per `data:` frame. */
function sse(events: readonly Record<string, unknown>[]): string {
  return events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("");
}

/**
 * The canned turn. The first one calls the submit tool, which is what makes Babel's host-tool
 * half run for real; the second stops, because an agent loop handed a tool result asks the model
 * again and a stream that called the tool twice would be a second submission.
 */
function turn(nth: number): string {
  if (nth === 0) {
    const call = {
      type: "toolCall",
      id: "toolu_smoke_1",
      name: "babel_submit_result",
      arguments: SUBMISSION,
    };
    const partial = assistant([call], "toolUse");
    return sse([
      { type: "start", partial: assistant([], "stop") },
      { type: "toolcall_start", contentIndex: 0, partial },
      { type: "toolcall_end", contentIndex: 0, toolCall: call, partial },
      { type: "done", reason: "toolUse", message: partial },
    ]);
  }
  const text = { type: "text", text: "submitted" };
  const partial = assistant([text], "stop");
  return sse([
    { type: "start", partial: assistant([], "stop") },
    { type: "text_start", contentIndex: 0, partial },
    { type: "text_delta", contentIndex: 0, delta: "submitted", partial },
    { type: "text_end", contentIndex: 0, content: "submitted", partial },
    { type: "done", reason: "stop", message: partial },
  ]);
}

/** How many tool results the request's context already carries; the turn index. */
function answered(body: unknown): number {
  if (body === null || typeof body !== "object" || !("context" in body)) return 0;
  const context = body.context;
  if (context === null || typeof context !== "object" || !("messages" in context)) return 0;
  const messages = context.messages;
  if (!Array.isArray(messages)) return 0;
  return messages.filter(
    (message) =>
      message !== null &&
      typeof message === "object" &&
      "role" in message &&
      message.role === "toolResult",
  ).length;
}

test.skipIf(UNAVAILABLE !== "")(
  `the real omp discovers its model through the binding, is prompted, calls the submit tool, and its usage reaches the receipt${
    UNAVAILABLE === "" ? "" : ` — SKIPPED: ${UNAVAILABLE}`
  }`,
  async () => {
    const routes: string[] = [];
    const endpoint = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      async fetch(request) {
        const url = new URL(request.url);
        routes.push(`${request.method} ${url.pathname}`);
        if (request.method === "GET" && url.pathname === "/v1/models") {
          // Proxy discovery: the owner's gateway answers an OpenAI-compatible listing, and it is
          // the only place this run learns a model exists.
          return Response.json({
            object: "list",
            data: [
              {
                id: MODEL,
                object: "model",
                owned_by: PROVIDER,
                context_length: 1_000_000,
                max_output_tokens: 128_000,
                input_modalities: ["text"],
              },
            ],
          });
        }
        if (request.method === "POST" && url.pathname === "/v1/pi/stream") {
          return new Response(turn(answered(await request.json())), {
            headers: { "content-type": "text/event-stream", "cache-control": "no-cache" },
          });
        }
        return new Response("not found", { status: 404 });
      },
    });

    const directory = await mkdtemp(join(tmpdir(), "babel-omp-smoke-"));
    directories.push(directory);
    const home = join(directory, "home");
    await mkdir(join(home, ".omp", "agent"), { recursive: true });
    // THE TWO FILES THE OWNER MATERIALIZES, with the binding's url and bearer spliced in — which
    // is the one thing this test writes that `server/plan.ts` `sessionInputs` does not: there is
    // no owner here to splice them.
    await writeFile(
      join(home, ".omp", "agent", "models.yml"),
      JSON.stringify({
        providers: {
          [PROVIDER]: {
            baseUrl: `http://127.0.0.1:${String(endpoint.port)}`,
            apiKey: "smoke-bearer-000000000000000000000000000000",
            transport: "pi-native",
            discovery: { type: "proxy" },
          },
        },
      }),
    );
    await writeFile(
      join(home, ".omp", "agent", "config.yml"),
      JSON.stringify({
        modelRoles: { default: SESSION.model },
        defaultThinkingLevel: SESSION.thinking,
        extensions: [],
        extendedContext: true,
        startup: { setupWizard: false },
      }),
    );

    const written: string[] = [];
    const progress = openProgress({ sink: { write: (line) => written.push(line) } });
    const sink = new MemorySink();
    try {
      const receipt = await explore(
        ExploreInputSchema.parse({
          runId: "run_omp_smoke",
          machineId: "dev-01",
          // `args` is empty: the argv is exactly what `engineArgv` composes, and nothing here
          // gets to add a flag the launch does not already fix.
          engine: { binary: OMP ?? "omp" },
          session: SESSION,
          preparation: {
            id: "prep_smoke",
            selection: [
              {
                harness: "omp",
                sourceId: "session-1",
                selector: "omp/session-1",
                path: "/archive/omp/session-1.jsonl",
                digest: "sha256:capture",
              },
            ],
          },
          recipes: [RECIPE],
          stages: ["explore"],
          caps: { toolCalls: 4, minutes: 0, perRunUsd: 0, idleMs: 120_000, handshakeMs: 60_000 },
          // This process is a development shell, not a Manifold job sandbox.
          requireContainment: false,
        }),
        sink,
        { workDir: directory, home, progress },
      );

      // THE RUN REACHED THE MODEL, and said so at the instant the prompt left Babel.
      const stages = written.map((line) => WorkerProgressSchema.parse(JSON.parse(line)).stage);
      expect(stages).toContain(RUN_STAGES.atModel);
      expect(progress.refused).toBe(0);

      // THE MODEL WAS DISCOVERED THROUGH THE BINDING AND ANSWERED THROUGH IT, twice: the turn
      // that called the tool, and the turn after Babel answered it.
      expect(routes).toEqual(["GET /v1/models", "POST /v1/pi/stream", "POST /v1/pi/stream"]);

      // THE SUBMISSION WAS ACCEPTED and became the run's records.
      expect(receipt.closure).toBe("completed");
      expect(receipt.counts["submissions"]).toBe(1);
      expect(receipt.counts["babel_submit_result.served"]).toBe(1);
      expect(receipt.counts["hypotheses"]).toBe(1);

      // AND THE RECEIPT CARRIES WHOSE WINDOW WAS SPENT AND WHAT ANSWERED. `models` is what the
      // engine reported through `model_changed`, so it is the fully qualified reference omp
      // resolved and not the string this test asked for.
      expect(receipt.account).toEqual({ provider: PROVIDER, identityKey: SESSION.account.identityKey });
      expect(receipt.model).toBe(SESSION.model);
      expect(receipt.models).toEqual([SESSION.model]);
      expect(receipt.tokens).toBe(2 * USAGE.totalTokens);
      expect(receipt.costUsd).toBeCloseTo(2 * USAGE.cost.total, 8);
      expect(receipt.profile?.["model"]).toBe(SESSION.model);
      expect(receipt.profile?.["account"]).toBe(SESSION.account.identityKey);
      expect(receipt.profile?.["failure"]).toBeUndefined();
    } finally {
      endpoint.stop(true);
    }
  },
  120_000,
);
