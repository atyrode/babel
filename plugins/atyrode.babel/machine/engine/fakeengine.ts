#!/usr/bin/env bun
/*
  THE FAKE ENGINE: the synthetic counterpart of `code engine`. It writes Code's runtime-info
  sidecar and then speaks OMP's native RPC on stdio the way a contained engine would, with no
  model behind it. Ported from internal/worker/testdata/fakeengine.

  It exists so the machine half can be tested without Code, OMP, a provider or a credential, and
  so every misbehaviour the supervision must survive can be produced on demand: each flag breaks
  exactly one thing and leaves the rest well behaved, which is what makes the corresponding
  obligation discriminating rather than merely present.

  Everything it writes is built from literals and plain objects, never from the plugin's own
  types, so the fixture stays honest about the wire: a test that passed because both sides shared
  a type would prove only that TypeScript can copy a type. The one Babel-specific thing it reads
  is the `[babel-params]` block the prompt embeds, which lets a submission template name
  identifiers the operation minted mid-run.

  Its own flags come BEFORE the `engine` subcommand, which is exactly how Babel composes argv: the
  operator's arguments first, then the subcommand and Babel's flags.
*/

const argv = process.argv.slice(2);

interface Flags {
  containment: "full" | "weak" | "none" | "missing";
  runtimeInfo: string;
  profile: string;
  profileOverride: string;
  describe: boolean;
  noRuntimeInfo: boolean;
  secretMetadata: boolean;
  submit: string[];
  submitPath: string;
  noSubmit: boolean;
  submitTwice: boolean;
  chunk: boolean;
  badFrame: "" | "malformed" | "oversized" | "chunk-short";
  noReady: boolean;
  readyVersions: number[];
  promptOut: string;
  refuseCommand: string;
  dropTool: boolean;
  exitCode: number;
  localPrompt: boolean;
  stallAfter: "" | "ready" | "tools" | "prompt";
  statsRefused: boolean;
  unknownFrame: boolean;
  extensionUI: boolean;
  noFinished: boolean;
  /** Stay alive after stdin closes, which is the one thing a supervised engine may not do. */
  ignoreEof: boolean;
}

function parse(args: readonly string[]): Flags {
  const flags: Flags = {
    containment: "full",
    runtimeInfo: "",
    profile: "",
    profileOverride: "",
    describe: false,
    noRuntimeInfo: false,
    secretMetadata: false,
    ignoreEof: false,
    submit: [],
    submitPath: "",
    noSubmit: false,
    submitTwice: false,
    chunk: false,
    badFrame: "",
    noReady: false,
    readyVersions: [1, 2],
    promptOut: "",
    refuseCommand: "",
    dropTool: false,
    exitCode: 0,
    localPrompt: false,
    stallAfter: "",
    statsRefused: false,
    unknownFrame: false,
    extensionUI: false,
    noFinished: false,
  };
  let index = 0;
  const value = (): string => {
    index += 1;
    return args[index] ?? "";
  };
  for (; index < args.length; index += 1) {
    const flag = args[index];
    switch (flag) {
      case "--fake-containment":
        flags.containment = value() as Flags["containment"];
        break;
      case "--fake-profile":
        // Resolve a different profile than argv named, which is the one way a launch can be
        // running under a profile the job did not ask for.
        flags.profileOverride = value();
        break;
      case "--fake-no-runtime-info":
        flags.noRuntimeInfo = true;
        break;
      case "--fake-secret-metadata":
        flags.secretMetadata = true;
        break;
      case "--fake-submit-json":
        flags.submit.push(value());
        break;
      case "--fake-submit":
        flags.submitPath = value();
        break;
      case "--fake-no-submit":
        flags.noSubmit = true;
        break;
      case "--fake-submit-twice":
        flags.submitTwice = true;
        break;
      case "--fake-chunk":
        flags.chunk = true;
        break;
      case "--fake-bad-frame":
        flags.badFrame = value() as Flags["badFrame"];
        break;
      case "--fake-no-ready":
        flags.noReady = true;
        break;
      case "--fake-ready-versions":
        flags.readyVersions = value()
          .split(",")
          .filter((part) => part !== "")
          .map((part) => Number.parseInt(part, 10));
        break;
      case "--fake-prompt-out":
        flags.promptOut = value();
        break;
      case "--fake-refuse-command":
        flags.refuseCommand = value();
        break;
      case "--fake-drop-tool":
        flags.dropTool = true;
        break;
      case "--fake-exit":
        flags.exitCode = Number.parseInt(value(), 10);
        break;
      case "--fake-local-prompt":
        flags.localPrompt = true;
        break;
      case "--fake-stall-after":
        flags.stallAfter = value() as Flags["stallAfter"];
        break;
      case "--fake-stats-refused":
        flags.statsRefused = true;
        break;
      case "--fake-unknown-frame":
        flags.unknownFrame = true;
        break;
      case "--fake-extension-ui":
        flags.extensionUI = true;
        break;
      case "--fake-no-finished-report":
        flags.noFinished = true;
        break;
      case "--fake-ignore-eof":
        flags.ignoreEof = true;
        break;
      case "engine":
        break;
      case "--profile":
        flags.profile = value();
        break;
      case "--runtime-info":
        flags.runtimeInfo = value();
        break;
      case "--describe":
        flags.describe = true;
        break;
      default:
        throw new Error(`fakeengine: unexpected argument ${JSON.stringify(flag)}`);
    }
  }
  return flags;
}

const flags = parse(argv);

/** The `code.runtime/1` document for one launch. */
function runtimeReport(finished: boolean): Record<string, unknown> {
  const resolved = flags.profileOverride === "" ? flags.profile : flags.profileOverride;
  const [id = "synthetic-profile", revision = "1"] = resolved.split("@");
  const metadata: Record<string, string> = { provider: "synthetic", model: "synthetic-1", thinking: "low" };
  if (flags.secretMetadata) metadata["api_key"] = "sk-synthetic-should-never-be-stored";
  const containment: Record<string, unknown> | null =
    flags.containment === "full"
      ? {
          backend: "synthetic-bwrap",
          filesystem_isolation: true,
          network_default_deny: true,
          resource_ceilings: true,
          disposable: true,
          escape: "none modelled",
        }
      : flags.containment === "weak"
        ? {
            backend: "synthetic-bwrap",
            filesystem_isolation: true,
            network_default_deny: false,
            resource_ceilings: false,
            disposable: true,
            escape: "network is open",
          }
        : flags.containment === "none"
          ? { backend: "", escape: "" }
          : null;
  const report: Record<string, unknown> = {
    schema: "code.runtime/1",
    worker: { name: "fakeengine", version: "0.0.1-synthetic" },
    profile: { id, revision: Number.parseInt(revision, 10) },
    privacy: { disclosure: "local", redaction_required: false },
    cost: { currency: "USD", input_per_1k: 0.001, output_per_1k: 0.002, estimated_run: 0.05 },
    metadata,
    finished,
  };
  if (containment !== null) report["containment"] = containment;
  if (finished) {
    report["exit_code"] = flags.exitCode;
    report["resources"] = { cpu_seconds: 0.42, max_rss_bytes: 123_456_789 };
    report["resources_provenance"] = "synthetic";
  }
  return report;
}

async function writeReport(finished: boolean): Promise<void> {
  if (flags.noRuntimeInfo || flags.runtimeInfo === "") return;
  await Bun.write(`${flags.runtimeInfo}.tmp`, JSON.stringify(runtimeReport(finished)));
  // Code writes the sidecar atomically; a reader that caught a half-written file would refuse a
  // launch that was fine.
  await Bun.$`mv ${`${flags.runtimeInfo}.tmp`} ${flags.runtimeInfo}`.quiet();
}

// ---------------------------------------------------------------------------- the wire

let hostIds = 0;
let chunkIds = 0;

async function line(text: string): Promise<void> {
  await Bun.write(Bun.stdout, `${text}\n`);
}

async function emit(frame: Record<string, unknown>): Promise<void> {
  const encoded = JSON.stringify(frame);
  if (flags.badFrame === "malformed") {
    flags.badFrame = "";
    await line("{this is not json");
    return;
  }
  if (flags.badFrame === "oversized") {
    flags.badFrame = "";
    await line(`{"type":"notice","text":"${"x".repeat(2 << 20)}"}`);
    return;
  }
  if (flags.badFrame === "chunk-short") {
    flags.badFrame = "";
    chunkIds += 1;
    await line(
      JSON.stringify({
        type: "rpc_chunk",
        chunkId: `rpc-${chunkIds}`,
        index: 0,
        count: 2,
        byteLength: encoded.length,
        data: Buffer.from(encoded).toString("base64"),
      }),
    );
    await line(JSON.stringify({ type: "notice" }));
    return;
  }
  if (flags.chunk) {
    chunkIds += 1;
    const bytes = Buffer.from(encoded);
    const segment = Math.max(1, Math.ceil(bytes.length / 3));
    const count = Math.ceil(bytes.length / segment);
    for (let i = 0; i < count; i += 1) {
      await line(
        JSON.stringify({
          type: "rpc_chunk",
          chunkId: `rpc-${chunkIds}`,
          index: i,
          count,
          byteLength: bytes.length,
          data: bytes.subarray(i * segment, Math.min((i + 1) * segment, bytes.length)).toString("base64"),
        }),
      );
    }
    return;
  }
  await line(encoded);
}

async function respond(id: string, command: string, data: Record<string, unknown> | null): Promise<void> {
  if (flags.refuseCommand === command) {
    await emit({ type: "response", id, command, success: false, error: "refused by fixture" });
    return;
  }
  const frame: Record<string, unknown> = { type: "response", id, command, success: true };
  if (data !== null) frame["data"] = data;
  await emit(frame);
}

/** Reads one inbound command per line. Returns null at end of stream. */
async function* commands(): AsyncGenerator<Record<string, unknown>> {
  const decoder = new TextDecoder();
  let buffer = "";
  for await (const piece of Bun.stdin.stream()) {
    buffer += decoder.decode(piece, { stream: true });
    for (;;) {
      const cut = buffer.indexOf("\n");
      if (cut < 0) break;
      const text = buffer.slice(0, cut).trim();
      buffer = buffer.slice(cut + 1);
      if (text === "") continue;
      yield JSON.parse(text) as Record<string, unknown>;
    }
  }
}

/**
 * A stall the supervision has to notice: the fixture stops speaking and never leaves on its own.
 *
 * The timer is the load-bearing half. An unresolved promise does not keep Bun's event loop alive,
 * so a bare `await` on one would exit quietly — which is an engine that DIED, the opposite of the
 * misbehaviour this produces.
 */
function stall(): Promise<never> {
  const { promise } = Promise.withResolvers<never>();
  setInterval(() => {}, 1_000);
  return promise;
}

/** Reads the `[babel-params]` block the prompt embeds: one `key = value` per line. */
function promptParams(prompt: string): Record<string, string> {
  const params: Record<string, string> = {};
  const start = prompt.indexOf("[babel-params]\n");
  if (start < 0) return params;
  for (const text of prompt.slice(start + "[babel-params]\n".length).split("\n")) {
    if (text === "" || text === "[end]") break;
    const cut = text.indexOf(" = ");
    if (cut > 0) params[text.slice(0, cut)] = text.slice(cut + 3);
  }
  return params;
}

/** Expands `${param:KEY}` and `${paramitem:KEY:N}` in a submission template. */
function expand(text: string, params: Readonly<Record<string, string>>): string {
  let out = text;
  for (const [key, value] of Object.entries(params)) {
    out = out.replaceAll(`\${param:${key}}`, value);
    const items = value.split(",").filter((item) => item !== "");
    for (const [index, item] of items.entries()) out = out.replaceAll(`\${paramitem:${key}:${index}}`, item);
  }
  return out;
}

// ---------------------------------------------------------------------------- the run

async function main(): Promise<void> {
  if (flags.describe) {
    const report = runtimeReport(false);
    delete report["containment"];
    await line(JSON.stringify(report));
    process.exit(0);
  }

  await writeReport(false);
  if (!flags.noReady) {
    await emit({
      type: "ready",
      protocolVersion: 1,
      supportedProtocolVersions: flags.readyVersions,
      maxFrameBytes: 1 << 20,
      maxReassembledFrameBytes: 64 << 20,
    });
  }
  if (flags.stallAfter === "ready") await stall();

  let prompt = "";
  const inbound = commands();
  // Explicit `next()` rather than `for await`: breaking out of a for-await calls the generator's
  // return(), which would close stdin's reader half way through the conversation — and then the
  // fixture would stop answering the stats command Babel asks for after the turn.
  const nextCommand = async (): Promise<Record<string, unknown> | null> => {
    const step = await inbound.next();
    return step.done === true ? null : step.value;
  };
  let answered = false;

  for (;;) {
    const command = await nextCommand();
    if (command === null) break;
    const kind = typeof command["type"] === "string" ? command["type"] : "";
    const id = typeof command["id"] === "string" ? command["id"] : "";
    if (kind === "negotiate_protocol") {
      await respond(id, kind, { protocolVersion: 2 });
      continue;
    }
    if (kind === "set_host_tools") {
      const tools = Array.isArray(command["tools"]) ? command["tools"] : [];
      const toolNames = tools.map((tool) => {
        const named = tool as { name?: unknown };
        return typeof named.name === "string" ? named.name : "";
      });
      await respond(id, kind, { toolNames: flags.dropTool ? toolNames.slice(0, -1) : toolNames });
      if (flags.stallAfter === "tools") await stall();
      continue;
    }
    if (kind === "prompt") {
      prompt = typeof command["message"] === "string" ? command["message"] : "";
      if (flags.promptOut !== "") await Bun.write(flags.promptOut, prompt);
      if (flags.localPrompt) {
        await respond(id, kind, { agentInvoked: false });
        break;
      }
      await respond(id, kind, { agentInvoked: true });
      if (flags.stallAfter === "prompt") await stall();
      answered = true;
      break;
    }
    await respond(id, kind, null);
  }

  if (answered) {
    const params = promptParams(prompt);
    await emit({ type: "agent_start" });
    await emit({ type: "turn_start" });
    if (flags.unknownFrame) await emit({ type: "telemetry_sample", value: 1 });
    if (flags.extensionUI) {
      await emit({ type: "extension_ui_request", id: "ui-1", method: "confirm", message: "Continue?" });
      await nextCommand();
    }

    const payloads = [...flags.submit];
    if (flags.submitPath !== "") payloads.push(await Bun.file(flags.submitPath).text());
    if (!flags.noSubmit) {
      for (const payload of flags.submitTwice ? [...payloads, ...payloads] : payloads) {
        hostIds += 1;
        const callId = `toolu_${hostIds}`;
        const id = `host_${hostIds}`;
        await emit({ type: "tool_execution_start", toolCallId: callId, toolName: "babel_submit_result" });
        await emit({
          type: "host_tool_call",
          id,
          toolCallId: callId,
          toolName: "babel_submit_result",
          arguments: JSON.parse(expand(payload, params)),
        });
        if ((await nextCommand()) === null) break;
        await emit({ type: "tool_execution_end", toolCallId: callId, toolName: "babel_submit_result" });
      }
    }
    await emit({ type: "turn_end" });
    await emit({ type: "agent_end", messages: [], isTerminal: true });
  }

  // After the turn Babel asks for stats and closes stdin.
  for (;;) {
    const command = await nextCommand();
    if (command === null) break;
    const kind = typeof command["type"] === "string" ? command["type"] : "";
    const id = typeof command["id"] === "string" ? command["id"] : "";
    if (kind === "get_session_stats" && flags.statsRefused) {
      await emit({ type: "response", id, command: kind, success: false, error: "stats unavailable" });
      continue;
    }
    if (kind === "get_session_stats") {
      await respond(id, kind, {
        tokens: { input: 1200, output: 340, reasoning: 0, cacheRead: 0, cacheWrite: 0, total: 1540 },
        cost: 0.0123,
        toolCalls: hostIds,
        assistantMessages: 1,
      });
      continue;
    }
    await respond(id, kind, null);
  }

  // An engine that outstays its stdin is what the exit grace and the process-group kill exist
  // for; it never writes the finished report, because it never got to leave on its own.
  if (flags.ignoreEof) await stall();
  if (!flags.noFinished) await writeReport(true);
  process.exit(flags.exitCode);
}

// A fixture that breaks says so on stderr and exits distinctly, so a test failure reads as
// "the fixture broke" rather than as a supervision failure Babel is responsible for.
main().catch((error: unknown) => {
  process.stderr.write(`fakeengine: ${String(error)}\n`);
  process.exit(70);
});
