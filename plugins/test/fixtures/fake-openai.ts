// A fake OpenAI-compatible provider for proving ADR 0038 end to end without spending tokens.
//
// It answers exactly the three routes the `atyrode.babel.inference` policy proxies and reports a
// `usage` object the owner's meter reads - JSON or SSE, chat or responses. Every request is logged
// to stderr as one line (method, path, model, stream, bearer prefix) so a proof can show which
// credential reached the provider: the owner-held one, never the job's bearer.
//
//   bun test/fixtures/fake-openai.ts <port> [expected-bearer]
//
// With an expected bearer, any other Authorization is answered 401, which is what a real provider
// would do to a job that tried to reach it with its per-job capability.
//
// `export {}` makes this a MODULE rather than a global script: fakeengine.ts is a script too,
// and two scripts in one program share one top-level scope, so their helpers collide by name.

export {};

const port = Number(process.argv[2] ?? "8781");
const expected = process.argv[3];
const INPUT = 137;
const OUTPUT = 41;
let calls = 0;

function line(request: Request, body: { model?: string; stream?: boolean } | null): void {
  const auth = request.headers.get("authorization") ?? "";
  console.error(
    JSON.stringify({
      call: ++calls,
      method: request.method,
      path: new URL(request.url).pathname,
      model: body?.model ?? null,
      stream: body?.stream ?? false,
      bearer: auth.startsWith("Bearer ") ? auth.slice(7, 15) + "…" : auth || null,
    }),
  );
}

function unauthorized(request: Request): Response | null {
  if (!expected) return null;
  if (request.headers.get("authorization") === `Bearer ${expected}`) return null;
  return Response.json({ error: { code: "invalid_api_key", message: "bad bearer" } }, { status: 401 });
}

function sse(frames: unknown[]): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    async start(controller) {
      for (const frame of frames) {
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(frame)}\n\n`));
        await Bun.sleep(5);
      }
      controller.enqueue(encoder.encode("data: [DONE]\n\n"));
      controller.close();
    },
  });
  return new Response(stream, { headers: { "content-type": "text/event-stream" } });
}

Bun.serve({
  port,
  hostname: "127.0.0.1",
  async fetch(request) {
    const path = new URL(request.url).pathname;
    const refused = unauthorized(request);
    if (path === "/v1/models" && request.method === "GET") {
      line(request, null);
      if (refused) return refused;
      return Response.json({
        object: "list",
        data: [{ id: "fake-1", object: "model", owned_by: "fake" }],
      });
    }
    if (request.method !== "POST") return new Response("not found", { status: 404 });
    const body = (await request.json()) as { model?: string; stream?: boolean; stream_options?: unknown };
    line(request, body);
    if (refused) return refused;
    const model = body.model ?? "fake-1";
    const id = `fake-${calls}`;
    if (path === "/v1/chat/completions") {
      const usage = { prompt_tokens: INPUT, completion_tokens: OUTPUT, total_tokens: INPUT + OUTPUT };
      if (body.stream)
        return sse([
          { id, object: "chat.completion.chunk", model, choices: [{ index: 0, delta: { role: "assistant" } }] },
          { id, object: "chat.completion.chunk", model, choices: [{ index: 0, delta: { content: "Nothing to report." } }] },
          { id, object: "chat.completion.chunk", model, choices: [{ index: 0, delta: {}, finish_reason: "stop" }] },
          { id, object: "chat.completion.chunk", model, choices: [], usage },
        ]);
      return Response.json({
        id,
        object: "chat.completion",
        model,
        choices: [{ index: 0, message: { role: "assistant", content: "Nothing to report." }, finish_reason: "stop" }],
        usage,
      });
    }
    if (path === "/v1/responses") {
      const usage = { input_tokens: INPUT, output_tokens: OUTPUT, total_tokens: INPUT + OUTPUT };
      const output = [{ type: "message", id: `msg-${calls}`, role: "assistant", content: [{ type: "output_text", text: "Nothing to report." }] }];
      const response = { id, object: "response", model, status: "completed", output, usage };
      if (body.stream)
        return sse([
          { type: "response.created", response: { ...response, status: "in_progress", output: [], usage: null } },
          { type: "response.output_text.delta", delta: "Nothing to report." },
          { type: "response.completed", response },
        ]);
      return Response.json(response);
    }
    return new Response("not found", { status: 404 });
  },
});
console.error(`fake-openai listening on http://127.0.0.1:${port}${expected ? " (bearer required)" : ""}`);
