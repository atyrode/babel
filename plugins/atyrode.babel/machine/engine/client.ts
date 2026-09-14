/*
  THE OMP RPC CLIENT. Babel speaks nothing of its own on this pipe: `launch.ts` spawns
  `omp --mode rpc` inside the job's sandbox, so what travels here is OMP's native protocol
  (packages/coding-agent/src/modes/rpc/rpc-types.ts), ported from internal/worker/{rpc.go,worker.go}.

  Until #279 the process on the other end was `code engine`, forwarding omp's stdio byte for
  byte. Code's engine is gone (atyrode/code#153) and Babel launches omp itself — and not one line
  of the wire changed, which is why this module is nearly untouched: it always spoke to omp.

  Babel sends four commands — negotiate_protocol, set_host_tools, prompt, get_session_stats (plus
  abort on cancellation) — and answers host_tool_call frames. Everything Babel needs from a run
  travels as a host tool the model calls: the evidence facilities the job granted, and one tool
  that records the result under the job's own JSON Schema, which the engine validates before
  Babel ever sees a call.

  What this module owns is the boundary (SPEC.md §2.6): the admission check before any prompt is
  written, authorization of every tool call, the lifetime of the whole process tree, and the run's
  final status. What changed at #279 is what admission READS. It used to read Code's
  `code.runtime/1` sidecar — a declaration by the process being judged. It now reads the job's own
  surroundings and the job's own home: the sandbox the machine owner built, and the two files the
  owner materialized out of the inference binding. A run that could reach a model outside the
  owner's metered proxy is refused there, before a byte of prompt is written.

  What this module never does is choose a model, retry, compact or steer the model's turn — those
  are the engine's, and this client is thin so that they stay there. The one number it keeps about
  them is a COUNT: provider refusals the engine reported, bounded, so a run inside a window that
  has run out ends `rate_limited` instead of spending an hour saying nothing (K-5).

  Two rules shape the reader. v2's chunked framing is negotiated because it is what makes a large
  tool result or a long final message lossless rather than truncated at the engine's physical
  frame bound; and every stop is final — a stream that has lost its framing cannot be trusted to
  have kept its content, so the reader never resynchronizes. Everything arriving off the pipe is
  parsed by a schema here rather than asserted into a shape, because it is outside-controlled
  input and a frame Babel misread would produce durable records nobody wrote.
*/

import type { Readable } from "node:stream";
import { z } from "zod";
import {
  containmentShortfall,
  DEFAULT_LIMITS,
  diagnoseFailure,
  ENGINE_FAILURES,
  EngineFailure,
  inferenceShortfall,
  LAUNCH_FAILURES,
  LAUNCH_REPORT_SCHEMA,
  launchEngine,
  observeContainment,
  SANDBOXED_RUN,
  type EngineFailureCode,
  type EngineLimits,
  type EngineProcess,
  type LaunchReport,
  type LaunchSpec,
  type Requirement,
  type SessionRef,
} from "./launch.ts";

// ---------------------------------------------------------------------------- the wire

/** The transport Babel negotiates. v1 has no chunking and cannot carry a long result intact. */
export const RPC_PROTOCOL_VERSION = 2;

/** Frame types Babel reads. The names are OMP's; nothing here is Babel's to define. */
const FRAME = {
  ready: "ready",
  response: "response",
  chunk: "rpc_chunk",
  hostToolCall: "host_tool_call",
  hostToolCancel: "host_tool_cancel",
  hostURIRequest: "host_uri_request",
  extensionUI: "extension_ui_request",
  agentEnd: "agent_end",
  promptResult: "prompt_result",
  modelChanged: "model_changed",
  retryFallback: "retry_fallback_applied",
  fallbackSucceeded: "retry_fallback_succeeded",
  extensionError: "extension_error",
  messageEnd: "message_end",
} as const;

/** The lifecycle frames a run records as progress and nothing else. */
const LIFECYCLE_FRAMES: Record<string, true> = {
  agent_start: true,
  turn_start: true,
  turn_end: true,
  tool_execution_start: true,
  tool_execution_end: true,
  auto_compaction_start: true,
  auto_compaction_end: true,
  auto_retry_start: true,
  auto_retry_end: true,
};

/** Commands Babel writes. */
export const COMMAND = {
  negotiate: "negotiate_protocol",
  setHostTools: "set_host_tools",
  prompt: "prompt",
  sessionStats: "get_session_stats",
  abort: "abort",
} as const;

/**
 * Host tool names Babel registers with the engine. They are Babel's to define: the engine learns
 * them from set_host_tools and the model reads them from the tool list, so nothing but this
 * module and the facilities behind these names ever spells them.
 *
 * `babel_submit_result` is registered for every job. The evidence names are here because the
 * instructions tell a model how to copy a locator out of what each one served; the facility that
 * answers them is the retrieval index, which is not part of this wave, so a run registers no
 * evidence tool yet and every call to one is refused as unknown.
 */
export const TOOL_SUBMIT = "babel_submit_result";
export const TOOL_SEARCH = "babel_corpus_search";
export const TOOL_FETCH = "babel_research_fetch";

/** What the model reads about the submit tool. The job's instructions say what the result means. */
const SUBMIT_DESCRIPTION =
  "Record this job's result. The arguments are the complete result as it stands; calling again " +
  "replaces the earlier submission, so include everything to be kept. A rejected submission " +
  "leaves the previous accepted one in place and explains what to fix.";

/** How many over-budget tool calls the engine may make before Babel gives up on it. */
const TOOL_BUDGET_SLACK = 16;

/**
 * One decoded stdout object. Loose because the set of native frames is OMP's to grow: a frame
 * Babel does not interpret is counted as unknown rather than refused.
 */
const FrameSchema = z.looseObject({
  type: z.string().min(1),
  id: z.string().optional(),
  command: z.string().optional(),
  success: z.boolean().optional(),
  error: z.string().optional(),
  data: z.unknown().optional(),
  supportedProtocolVersions: z.array(z.number()).optional(),
  toolCallId: z.string().optional(),
  toolName: z.string().optional(),
  arguments: z.unknown().optional(),
  targetId: z.string().optional(),
  isTerminal: z.boolean().optional(),
  agentInvoked: z.boolean().optional(),
  model: z.unknown().optional(),
  event: z.string().optional(),
  extensionPath: z.string().optional(),
});
export type Frame = z.infer<typeof FrameSchema>;

/** One rpc_chunk frame of a v2 logical frame. */
const ChunkSchema = z.looseObject({
  chunkId: z.string(),
  index: z.number().int(),
  count: z.number().int(),
  byteLength: z.number().int(),
  data: z.string(),
});

/** What set_host_tools answers with: the names the engine actually registered. */
const ToolNamesSchema = z.looseObject({ toolNames: z.array(z.string()) });

/** What `prompt` answers with. `agentInvoked:false` is a prompt the engine completed locally. */
const PromptAckSchema = z.looseObject({ agentInvoked: z.boolean().optional() });

/** What get_session_stats answers with: the engine's own accounting of the model work it did. */
const SessionStatsSchema = z.looseObject({
  tokens: z
    .looseObject({
      input: z.number().default(0),
      output: z.number().default(0),
      reasoning: z.number().default(0),
      cacheRead: z.number().default(0),
      cacheWrite: z.number().default(0),
      total: z.number().default(0),
    })
    .optional(),
  cost: z.number().default(0),
  toolCalls: z.number().optional(),
  assistantMessages: z.number().default(0),
});

// ---------------------------------------------------------------------------- the reader

/** One chunk sequence in flight. */
interface PendingChunks {
  id: string;
  count: number;
  length: number;
  next: number;
  parts: Buffer[];
  held: number;
}

/**
 * Reads the engine's stdout as newline-delimited JSON with v2 chunk sequences reassembled into
 * the logical frame they carry. It is a faithful reimplementation of the engine's own decoder
 * rules — chunk id, index order, count, byte length and the reassembly ceiling are all validated,
 * and an interrupted or interleaved sequence is a decode failure.
 */
export async function* readFrames(stream: Readable, limits: EngineLimits): AsyncGenerator<Frame> {
  let buffer = "";
  let pending: PendingChunks | null = null;
  const decoder = new TextDecoder();
  for await (const piece of stream) {
    buffer += typeof piece === "string" ? piece : decoder.decode(piece, { stream: true });
    for (;;) {
      const cut = buffer.indexOf("\n");
      if (cut < 0) {
        if (buffer.length > limits.maxFrameBytes) {
          throw new EngineFailure(
            ENGINE_FAILURES.oversizedFrame,
            `a stdout line passed ${limits.maxFrameBytes} bytes without a newline`,
          );
        }
        break;
      }
      const line = buffer.slice(0, cut).trim();
      buffer = buffer.slice(cut + 1);
      if (line === "") continue;
      if (line.length > limits.maxFrameBytes) {
        throw new EngineFailure(
          ENGINE_FAILURES.oversizedFrame,
          `a stdout line of ${line.length} bytes is over the ${limits.maxFrameBytes} byte limit`,
        );
      }
      let raw: unknown;
      try {
        raw = JSON.parse(line);
      } catch (error) {
        throw new EngineFailure(ENGINE_FAILURES.malformedFrame, `a stdout line is not JSON: ${String(error)}`);
      }
      const chunk = ChunkSchema.safeParse(raw);
      if (chunk.success && chunk.data.type === FRAME.chunk) {
        const folded = foldChunk(chunk.data, limits, pending);
        pending = folded.pending;
        if (folded.frame !== null) yield folded.frame;
        continue;
      }
      const frame = FrameSchema.safeParse(raw);
      if (!frame.success) {
        throw new EngineFailure(ENGINE_FAILURES.malformedFrame, "a stdout line is not one typed JSON frame");
      }
      if (pending !== null) {
        throw new EngineFailure(
          ENGINE_FAILURES.malformedFrame,
          `chunk sequence ${JSON.stringify(pending.id)} was interrupted by a ${frame.data.type} frame`,
        );
      }
      yield frame.data;
    }
  }
}

/** Folds one chunk into the pending sequence and reports the whole frame once the last one lands. */
function foldChunk(
  chunk: z.infer<typeof ChunkSchema>,
  limits: EngineLimits,
  pending: PendingChunks | null,
): { frame: Frame | null; pending: PendingChunks | null } {
  const { chunkId: id, index, count, byteLength } = chunk;
  if (id === "" || count <= 0 || index < 0 || index >= count || byteLength <= 0) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk ${JSON.stringify(id)} index ${index} of ${count} (${byteLength} bytes) is not well formed`,
    );
  }
  if (byteLength > limits.maxReassembledBytes) {
    throw new EngineFailure(
      ENGINE_FAILURES.oversizedFrame,
      `chunk sequence ${JSON.stringify(id)} declares ${byteLength} bytes over a ${limits.maxReassembledBytes} byte reassembly limit`,
    );
  }
  let open = pending;
  if (open === null) {
    if (index !== 0) {
      throw new EngineFailure(
        ENGINE_FAILURES.malformedFrame,
        `chunk sequence ${JSON.stringify(id)} starts at index ${index}`,
      );
    }
    open = { id, count, length: byteLength, next: 0, parts: [], held: 0 };
  } else if (open.id !== id) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} is interleaved with ${JSON.stringify(id)}`,
    );
  } else if (open.count !== count || open.length !== byteLength) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} changed its count or length mid-stream`,
    );
  } else if (open.next !== index) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} expected index ${open.next}, got ${index}`,
    );
  }
  const segment = Buffer.from(chunk.data, "base64");
  if (open.held + segment.length > open.length) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} exceeds its declared ${open.length} bytes`,
    );
  }
  open.parts.push(segment);
  open.held += segment.length;
  open.next += 1;
  if (open.next < open.count) return { frame: null, pending: open };
  if (open.held !== open.length) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} reassembled ${open.held} bytes, declared ${open.length}`,
    );
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(Buffer.concat(open.parts).toString("utf8"));
  } catch (error) {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      `chunk sequence ${JSON.stringify(open.id)} does not reassemble into JSON: ${String(error)}`,
    );
  }
  const frame = FrameSchema.safeParse(parsed);
  if (!frame.success) {
    throw new EngineFailure(ENGINE_FAILURES.malformedFrame, `chunk sequence ${JSON.stringify(open.id)} carries no frame`);
  }
  return { frame: frame.data, pending: null };
}

// ---------------------------------------------------------------------------- the inbox

/** One thing the reader delivered, or the reason it stopped. */
type Inbound =
  | { kind: "frame"; frame: Frame }
  | { kind: "eof" }
  | { kind: "error"; failure: EngineFailure }
  | { kind: "timeout" };

/**
 * The supervisor's view of the stream: frames arrive as the reader decodes them and are taken one
 * at a time under a wait budget. A child that has exited does not end a wait immediately — frames
 * it already wrote may still be in the pipe — so the budget collapses to the shorter drain grace
 * instead of the full idle timeout.
 */
class Inbox {
  #queue: Inbound[] = [];
  #wake: ((delivered: boolean) => void) | null = null;
  #stopped: Inbound | null = null;

  constructor(
    stream: Readable,
    private readonly limits: EngineLimits,
    private readonly engine: EngineProcess,
  ) {
    void this.#pump(stream);
  }

  async #pump(stream: Readable): Promise<void> {
    try {
      for await (const frame of readFrames(stream, this.limits)) {
        this.#queue.push({ kind: "frame", frame });
        this.#wake?.(true);
      }
      this.#stopped = { kind: "eof" };
    } catch (error) {
      this.#stopped =
        error instanceof EngineFailure
          ? { kind: "error", failure: error }
          : { kind: "error", failure: new EngineFailure(ENGINE_FAILURES.malformedFrame, String(error)) };
    }
    this.#wake?.(true);
  }

  async next(budgetMs: number): Promise<Inbound> {
    const budget = this.engine.hasExited() ? Math.min(budgetMs, this.limits.drainGraceMs) : budgetMs;
    const deadline = Date.now() + budget;
    for (;;) {
      const head = this.#queue.shift();
      if (head !== undefined) return head;
      if (this.#stopped !== null) return this.#stopped;
      const left = deadline - Date.now();
      if (left <= 0) return { kind: "timeout" };
      const waiter = Promise.withResolvers<boolean>();
      const timer = setTimeout(() => {
        this.#wake = null;
        waiter.resolve(false);
      }, left);
      this.#wake = (woken) => {
        clearTimeout(timer);
        this.#wake = null;
        waiter.resolve(woken);
      };
      const delivered = await waiter.promise;
      if (!delivered) return { kind: "timeout" };
    }
  }
}

// ---------------------------------------------------------------------------- the job

/** One tool Babel registers for a job: the definition the model reads. */
export interface HostTool {
  name: string;
  description: string;
  /** The JSON Schema of the call's arguments; the engine validates every call against it. */
  parameters: unknown;
  loadMode?: "essential" | "discoverable";
}

/** One engine call to an evidence facility, as handed to the broker. */
export interface ToolCall {
  name: string;
  callId: string;
  arguments: unknown;
  index: number;
}

/** What one served call answered with. A denial carries no payload, only the reason. */
export interface ToolAnswer {
  allow: boolean;
  reason: string;
  results?: string;
}

/**
 * What answers an evidence call. A denial returns no payload even when the facility had one: a
 * facility that both refused a call and answered it would be sending two contradictory things
 * down one pipe.
 */
export interface ToolBroker {
  serve(call: ToolCall): Promise<ToolAnswer>;
}

/** One analysis or evaluation job: what the engine is asked, what it may call, what it must produce. */
export interface EngineJob {
  runId: string;
  /** WHO ANSWERS AND ON WHOSE ACCOUNT; the launch report records it verbatim (#279). */
  session: SessionRef;
  /** The whole of what the model is told. Written only after admission passes. */
  prompt: string;
  /** Registered as the submit tool's parameters; the engine enforces it structurally. */
  submitSchema: unknown;
  tools?: readonly HostTool[];
  /**
   * Validates one submission as the caller's own domain shape and returns the reason it is
   * refused, or "" to accept. The engine has already enforced the JSON Schema; this is the
   * semantic check, and a refusal is what the model reads back so it can correct itself.
   */
  accept?: (payload: unknown) => string;
  requirement?: Requirement;
}

/** The engine's own accounting at the end of a turn. It is a measurement, not a cost guard. */
export interface Usage {
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  totalTokens: number;
  costUsd: number;
  toolCalls: number;
  messages: number;
}

/**
 * The words a {@link ProgressRecord} is stamped with. They are the run's own lifecycle rather
 * than the model's turn structure, and `prompt` is the one a caller acts on: it is written the
 * instant the job's material leaves Babel, which is what "at the model since T" means (#261).
 */
export const PROGRESS_STAGE = {
  launch: "launch",
  prompt: "prompt",
  tool: "tool",
  agent: "agent",
  model: "model",
  extension: "extension",
} as const;

/** One lifecycle event, so an interface stays responsive while a run is in flight. */
export interface ProgressRecord {
  seq: number;
  stage: string;
  message: string;
  at: string;
}

/** One tool decision: what was called, whether it was allowed, and the reason the model was given. */
export interface ToolDecision {
  index: number;
  tool: string;
  allowed: boolean;
  reason: string;
  argumentBytes: number;
}

/** One supervised run's failure, as the receipt records it. */
export interface FailureRecord {
  code: EngineFailureCode;
  message: string;
  origin: "engine" | "babel";
  /**
   * THE NAMED CAUSE, when one was diagnosed (#277): `broker_unavailable`, `rate_limited`,
   * `inference_unbound`; "" when the code is the whole of what is known.
   *
   * On 2026-09-13 every run of an hour failed as "the engine closed its stdout before a ready
   * frame, exit status -1" while the real reason was one line earlier on stderr and nowhere in
   * any receipt. The code says WHERE the run stopped; this says WHY, and it is the field an
   * operator acts on.
   */
  named: string;
}

/** What one supervised run did. It is returned whenever a process started, including on failure. */
export interface EngineOutcome {
  closure: "completed" | "failed";
  failure: FailureRecord | null;
  /**
   * BABEL'S OWN LAUNCH REPORT, present whenever a process was started — including a run refused
   * before its prompt. It replaces Code's `code.runtime/1`, and every field in it is something
   * this process observed or was handed as a job input.
   */
  report: LaunchReport | null;
  reportUnknown: readonly string[];
  /** The same report rewritten after the engine exits: the exit status and the retries. */
  finished: LaunchReport | null;
  /** The accepted submission's payload, or null when nothing was accepted. */
  result: unknown;
  submissions: number;
  prompted: boolean;
  usage: Usage | null;
  /**
   * The models that answered, first heard from first: the launch report's, then every model the
   * engine changed to. The profile block of a receipt says what the run was LAUNCHED under, and
   * a fallback or a retry moves it (#261); these are what actually spoke.
   */
  models: readonly string[];
  tools: readonly ToolDecision[];
  progress: readonly ProgressRecord[];
  unknownFrames: readonly string[];
  stderrTail: string;
  exitCode: number;
}

/** What a run needs from the machine around it. */
export interface EngineDeps {
  launch: LaunchSpec;
  broker?: ToolBroker;
  limits?: Partial<EngineLimits>;
  now?: () => Date;
  onProgress?: (record: ProgressRecord) => void;
  /** A cancellation: the engine gets its own abort, the teardown's kill is the safety net. */
  signal?: AbortSignal;
}

// ---------------------------------------------------------------------------- the run

/** Per-run supervision state. Single-threaded: only the functions below mutate it. */
class Run {
  readonly limits: EngineLimits;
  readonly now: () => Date;
  ids = 0;
  events = 0;
  toolCount = 0;
  progressSeq = 0;
  ended = false;
  prompted = false;
  submissions = 0;
  result: unknown = null;
  usage: Usage | null = null;
  report: LaunchReport | null = null;
  reportUnknown: readonly string[] = [];
  finished: LaunchReport | null = null;
  /** Provider refusals the engine reported on its diagnostics; bounded by `limits.maxRetries`. */
  retries = 0;
  /** Set once `retries` passes the bound; the supervisor ends the run by name on its next turn. */
  rateLimited = false;
  tools: ToolDecision[] = [];
  progress: ProgressRecord[] = [];
  models: string[] = [];
  progressDropped = 0;
  unknown: Record<string, true> = {};
  stderr = "";

  constructor(
    readonly job: EngineJob,
    readonly deps: EngineDeps,
    readonly engine: EngineProcess,
    readonly inbox: Inbox,
  ) {
    this.limits = { ...DEFAULT_LIMITS, ...deps.limits };
    this.now = deps.now ?? (() => new Date());
  }

  /** Mints a correlation id for one of Babel's commands. */
  command(): string {
    this.ids += 1;
    return `babel-${this.ids}`;
  }

  /** Keeps a bounded lifecycle trail and notifies the caller so an interface can stay responsive. */
  record(stage: string, message: string): void {
    this.progressSeq += 1;
    const entry: ProgressRecord = { seq: this.progressSeq, stage, message, at: this.now().toISOString() };
    if (this.progress.length < this.limits.maxProgress) this.progress.push(entry);
    else this.progressDropped += 1;
    this.deps.onProgress?.(entry);
  }

  /** Notes a model that answered. The trail is ordered by first appearance and never repeats. */
  spoke(model: string): void {
    const named = model.trim();
    if (named !== "" && !this.models.includes(named)) this.models.push(named);
  }
}

/**
 * Runs one job and returns what happened. It never throws for anything the far side did: a
 * failure is the outcome's own field, because the record of a failed run is exactly when the
 * record is needed. A malformed job — no prompt, no schema, a duplicate tool name — throws,
 * because that is a defect in the caller and no engine should be launched for it.
 */
export async function runEngineJob(job: EngineJob, deps: EngineDeps): Promise<EngineOutcome> {
  if (job.prompt.trim() === "") throw new EngineFailure(ENGINE_FAILURES.launch, "the job carries no prompt", "babel");
  if (job.submitSchema === undefined || job.submitSchema === null) {
    throw new EngineFailure(ENGINE_FAILURES.launch, "the job carries no result schema", "babel");
  }
  const registered: Record<string, true> = { [TOOL_SUBMIT]: true };
  for (const tool of job.tools ?? []) {
    if (tool.name.trim() === "") throw new EngineFailure(ENGINE_FAILURES.launch, "a host tool has no name", "babel");
    if (registered[tool.name] === true) {
      throw new EngineFailure(
        ENGINE_FAILURES.launch,
        `host tool ${JSON.stringify(tool.name)} is registered twice`,
        "babel",
      );
    }
    if (tool.description.trim() === "") {
      // The engine refuses a tool with no description at registration; refusing here keeps that
      // from costing a launch.
      throw new EngineFailure(
        ENGINE_FAILURES.launch,
        `host tool ${JSON.stringify(tool.name)} has no description`,
        "babel",
      );
    }
    registered[tool.name] = true;
  }

  const limits = { ...DEFAULT_LIMITS, ...deps.limits };
  const engine = launchEngine(deps.launch);
  const run = new Run(job, deps, engine, new Inbox(engine.stdout, limits, engine));
  collectDiagnostics(run);
  // THE LAUNCH REPORT IS WRITTEN BEFORE THE HANDSHAKE, not after it. A run that dies before its
  // ready frame is exactly the run whose report is needed (#277): it names what was launched,
  // under which session, inside which observed boundary. The post-exit copy below adds only the
  // things that cannot be known yet — the exit status, the retries, the named cause.
  run.report = {
    schema: LAUNCH_REPORT_SCHEMA,
    engine: { name: "omp", version: "" },
    session: job.session,
    containment: observeContainment(deps.launch.home, engine.env),
    models: [],
    failure: "",
    reason: "",
    retries: 0,
    finished: false,
  };

  let failure: EngineFailure | null = null;
  try {
    await execute(run);
  } catch (error) {
    failure = error instanceof EngineFailure ? error : new EngineFailure(ENGINE_FAILURES.launch, String(error), "babel");
  }
  // A FAILED RUN'S CAUSE IS READ OFF THE ENGINE'S OWN DIAGNOSTICS, once, here. It is read only
  // for a failure: a run that answered is explained by its answer, and a 429 it recovered from
  // is a retry rather than a cause.
  const named =
    failure === null
      ? ""
      : failure.named !== ""
        ? failure.named
        : (diagnoseFailure(run.stderr)?.name ?? "");
  const reason =
    named === "" ? "" : (diagnoseFailure(run.stderr)?.reason ?? failure?.message ?? "");
  run.finished = {
    ...(run.report as LaunchReport),
    models: [...run.models],
    failure: named,
    reason,
    retries: run.retries,
    finished: true,
    exit_code: engine.exitCode(),
  };
  return {
    closure: failure === null ? "completed" : "failed",
    failure:
      failure === null
        ? null
        : { code: failure.code, message: failure.message, origin: failure.origin, named },
    report: run.report,
    reportUnknown: run.reportUnknown,
    finished: run.finished,
    result: run.result,
    submissions: run.submissions,
    prompted: run.prompted,
    usage: run.usage,
    models: run.models,
    tools: run.tools,
    progress: run.progress,
    unknownFrames: Object.keys(run.unknown).sort(),
    stderrTail: run.stderr,
    exitCode: engine.exitCode(),
  };
}

/**
 * The launch, the containment check, the registration, the prompt and the supervision. Teardown
 * always runs; whether it kills the tree depends on whether anything is still alive when the
 * stream ends.
 */
async function execute(run: Run): Promise<void> {
  try {
    await ready(run);
  } catch (error) {
    await run.engine.killTree(run.limits.terminateGraceMs);
    throw error;
  }
  try {
    await admit(run);
  } catch (error) {
    // Nothing is owed to a launch Babel refuses, and the prompt is what is not owed. Stdin
    // closes with no command written; the engine disposes on EOF, and the grace is what makes
    // that obligation observable rather than hidden behind an immediate kill.
    run.engine.closeStdin();
    const left = await run.engine.awaitExit(run.limits.exitGraceMs);
    if (!left) await run.engine.killTree(run.limits.terminateGraceMs);
    throw error;
  }
  try {
    await register(run);
    await prompt(run);
    await supervise(run);
    await stats(run);
  } catch (error) {
    await run.engine.killTree(run.limits.terminateGraceMs);
    throw error;
  }
  await finish(run);
}

/** Waits for the ready frame and checks that the engine offers the transport Babel speaks. */
async function ready(run: Run): Promise<void> {
  const inbound = await run.inbox.next(run.limits.handshakeMs);
  if (inbound.kind === "timeout") {
    throw new EngineFailure(ENGINE_FAILURES.handshake, `no ready frame within ${run.limits.handshakeMs}ms`);
  }
  if (inbound.kind === "error") throw inbound.failure;
  if (inbound.kind === "eof") {
    // #277: AN EOF BEFORE THE READY FRAME IS NAMED, not described. The exit status alone was
    // what every run of 2026-09-13 09:07–11:24 reported while the broker was down and the real
    // sentence sat one line earlier on stderr. `diagnoseFailure` reads that line; when it names
    // nothing, the wording below stands, because a run that said nothing is honestly described
    // as a run that said nothing.
    const diagnosed = diagnoseFailure(run.stderr);
    throw new EngineFailure(
      ENGINE_FAILURES.handshake,
      diagnosed === null
        ? `the engine closed its stdout before a ready frame, exit status ${run.engine.exitCode()}`
        : `${diagnosed.name}: ${diagnosed.reason}`,
      "engine",
      diagnosed?.name ?? "",
    );
  }
  const frame = inbound.frame;
  if (frame.type !== FRAME.ready) {
    throw new EngineFailure(ENGINE_FAILURES.protocol, `the first frame was ${JSON.stringify(frame.type)}, not ready`);
  }
  const offered = frame.supportedProtocolVersions ?? [];
  if (!offered.includes(RPC_PROTOCOL_VERSION)) {
    throw new EngineFailure(
      ENGINE_FAILURES.protocol,
      `the engine offers RPC versions ${JSON.stringify(offered)}, Babel needs ${RPC_PROTOCOL_VERSION}`,
    );
  }
}

/**
 * DECIDES WHETHER THIS ENGINE MAY BE PROMPTED AT ALL, over facts rather than over a declaration.
 *
 * Every refusal here happens before a byte of the prompt is written: what a refused engine has
 * seen is the session it was launched under and the tool names Babel would have registered.
 *
 * The two checks are what #279 replaced the `code.runtime/1` sidecar with, and each is something
 * this process can verify rather than believe:
 *
 * - the BOUNDARY is read out of the job's own surroundings (`observeContainment`), so a run
 *   outside a Manifold job sandbox is refused by the same rule that used to refuse a Code
 *   reporting no sandbox — except that nobody is being taken at their word;
 * - the METERED LANE is read out of the job's own home (`inferenceShortfall`): the two files the
 *   owner materialized out of the inference binding are there, and the launch environment holds
 *   no provider credential with which the engine could reach a model any other way. That is the
 *   check the sidecar's `lane` string was standing in for, done on the facts.
 */
async function admit(run: Run): Promise<void> {
  const containment = observeContainment(run.deps.launch.home, run.engine.env);
  if (run.report !== null) run.report = { ...run.report, containment };
  const shortfall = containmentShortfall(containment, run.job.requirement ?? SANDBOXED_RUN);
  if (shortfall !== "") throw new EngineFailure(ENGINE_FAILURES.containment, shortfall);
  const unbound = await inferenceShortfall(run.deps.launch.home, run.engine.env);
  if (unbound !== "") {
    throw new EngineFailure(
      ENGINE_FAILURES.inference,
      unbound,
      "babel",
      LAUNCH_FAILURES.inferenceUnbound,
    );
  }
  // The model the run ASKED for is the first name in the trail; every later one is a model that
  // actually answered and moved it (#261).
  run.spoke(run.job.session.model);
  run.record(
    PROGRESS_STAGE.launch,
    `admitted under ${containment.backend === "" ? "no backend" : containment.backend}`,
  );
}

/** Negotiates the transport and registers the job's tools. */
async function register(run: Run): Promise<void> {
  await call(run, { id: run.command(), type: COMMAND.negotiate, protocolVersion: RPC_PROTOCOL_VERSION });
  const tools = [
    ...(run.job.tools ?? []).map((tool) => ({
      name: tool.name,
      description: tool.description,
      parameters: tool.parameters,
      ...(tool.loadMode === undefined ? {} : { loadMode: tool.loadMode }),
    })),
    { name: TOOL_SUBMIT, description: SUBMIT_DESCRIPTION, parameters: run.job.submitSchema, loadMode: "essential" },
  ];
  const answer = ToolNamesSchema.safeParse(await call(run, { id: run.command(), type: COMMAND.setHostTools, tools }));
  if (!answer.success) {
    throw new EngineFailure(ENGINE_FAILURES.malformedFrame, "set_host_tools answered without the registered tool names");
  }
  for (const tool of tools) {
    if (!answer.data.toolNames.includes(tool.name)) {
      throw new EngineFailure(
        ENGINE_FAILURES.commandFailed,
        `the engine registered ${JSON.stringify(answer.data.toolNames)}, not ${JSON.stringify(tool.name)}`,
      );
    }
  }
}

/** Writes the job's prompt. It is the first moment the run's material leaves Babel. */
async function prompt(run: Run): Promise<void> {
  // BEFORE the write, not after: the record's instant is what "at the model since T" means, and
  // the response to this command does not arrive until the engine has been to the model at
  // least once. A run that reported the stage on the answer would report it on the way back.
  run.record(PROGRESS_STAGE.prompt, `${String(run.job.prompt.length)} characters written to the engine`);
  const data = await call(run, { id: run.command(), type: COMMAND.prompt, message: run.job.prompt });
  const ack = PromptAckSchema.safeParse(data);
  if (ack.success && ack.data.agentInvoked === false) {
    // The engine completed the prompt without a model turn. There is nothing to supervise and
    // nothing was submitted.
    run.ended = true;
    return;
  }
  run.prompted = true;
}

/** Writes one command and waits for its response, handling every other frame that arrives first. */
async function call(run: Run, command: { id: string; type: string } & Record<string, unknown>): Promise<unknown> {
  run.engine.write(command);
  for (;;) {
    const inbound = await run.inbox.next(run.limits.idleMs);
    if (inbound.kind === "timeout") {
      throw new EngineFailure(
        ENGINE_FAILURES.stalled,
        `the engine went silent for ${run.limits.idleMs}ms waiting for ${command.type}`,
      );
    }
    if (inbound.kind === "error") throw inbound.failure;
    if (inbound.kind === "eof") {
      throw new EngineFailure(
        ENGINE_FAILURES.engineExited,
        `the engine left before answering ${command.type}, exit status ${run.engine.exitCode()}`,
      );
    }
    const frame = inbound.frame;
    if (frame.type === FRAME.response && frame.id === command.id) {
      if (frame.success !== true) {
        throw new EngineFailure(
          ENGINE_FAILURES.commandFailed,
          `${frame.command ?? command.type}: ${frame.error ?? "refused with no reason"}`,
        );
      }
      return frame.data;
    }
    await handle(run, frame);
  }
}

/** Supervises the stream until the model's turn ends. */
async function supervise(run: Run): Promise<void> {
  while (!run.ended) {
    if (run.deps.signal?.aborted === true) {
      // The engine's own abort is the courteous half; the teardown's kill is the safety net.
      run.engine.write({ id: run.command(), type: COMMAND.abort });
      throw new EngineFailure(ENGINE_FAILURES.stalled, "the run was cancelled", "babel");
    }
    // THE RETRY CAP (K-5). Past the bound the run ends by name rather than spending its whole
    // idle budget on a provider that is refusing: the engine is asked to abort, and the receipt
    // says `rate_limited` with the count, which is a sentence an operator acts on.
    if (run.rateLimited) {
      run.engine.write({ id: run.command(), type: COMMAND.abort });
      throw new EngineFailure(
        ENGINE_FAILURES.rateLimited,
        `the provider refused ${String(run.retries)} times, past the ${String(run.limits.maxRetries)} this run allows`,
        "engine",
        LAUNCH_FAILURES.rateLimited,
      );
    }
    const inbound = await run.inbox.next(run.limits.idleMs);
    if (inbound.kind === "timeout") {
      throw new EngineFailure(ENGINE_FAILURES.stalled, `no frame for ${run.limits.idleMs}ms`);
    }
    if (inbound.kind === "error") throw inbound.failure;
    if (inbound.kind === "eof") {
      throw new EngineFailure(
        ENGINE_FAILURES.engineExited,
        `the engine closed its stdout before its turn ended, exit status ${run.engine.exitCode()}`,
      );
    }
    await handle(run, inbound.frame);
  }
}

/** Applies one inbound frame that is not the response Babel is waiting for. */
async function handle(run: Run, frame: Frame): Promise<void> {
  run.events += 1;
  if (run.events > run.limits.maxEvents) {
    throw new EngineFailure(ENGINE_FAILURES.eventBudget, `more than ${run.limits.maxEvents} frames`);
  }
  switch (frame.type) {
    case FRAME.hostToolCall:
      await answerToolCall(run, frame);
      return;
    case FRAME.hostToolCancel:
      // Every call is answered before the next frame is read, so a cancellation can only name a
      // call already answered. It is noted and nothing is withdrawn.
      run.record(PROGRESS_STAGE.tool, `the engine withdrew call ${frame.targetId ?? ""} after it was answered`);
      return;
    case FRAME.agentEnd:
      if (frame.isTerminal !== false) run.ended = true;
      run.record(PROGRESS_STAGE.agent, "turn ended");
      return;
    case FRAME.promptResult:
      if (frame.agentInvoked === false) run.ended = true;
      return;
    case FRAME.response:
      // A response to nothing Babel is waiting for: a late error for an accepted prompt, or a
      // stray. The prompt's asynchronous failure is the one that matters.
      if (frame.command === COMMAND.prompt && frame.success === false) {
        throw new EngineFailure(ENGINE_FAILURES.commandFailed, `prompt: ${frame.error ?? ""}`);
      }
      return;
    case FRAME.extensionUI:
      // Babel has no operator at the other end of a supervised run, and an unanswered dialog
      // would stall the engine until its own timeout.
      run.engine.write({ type: "extension_ui_response", id: frame.id ?? "", cancelled: true });
      return;
    case FRAME.hostURIRequest:
      run.engine.write({
        type: "host_uri_result",
        id: frame.id ?? "",
        isError: true,
        error: "Babel registers no URI schemes",
      });
      return;
    case FRAME.messageEnd:
      return;
    case FRAME.modelChanged:
      run.spoke(modelName(frame.model));
      run.record(PROGRESS_STAGE.model, `model changed: ${JSON.stringify(frame.model ?? null)}`);
      return;
    case FRAME.retryFallback:
    case FRAME.fallbackSucceeded:
      run.record(PROGRESS_STAGE.agent, frame.type);
      return;
    case FRAME.extensionError:
      run.record(PROGRESS_STAGE.extension, `extension error in ${frame.event ?? frame.extensionPath ?? "an extension"}`);
      return;
    case FRAME.ready:
      throw new EngineFailure(ENGINE_FAILURES.protocol, "a second ready frame");
    default:
      if (LIFECYCLE_FRAMES[frame.type] === true) {
        run.record(PROGRESS_STAGE.agent, frame.type);
        return;
      }
      run.unknown[frame.type] = true;
      return;
  }
}

/**
 * The name inside a `model_changed` frame. OMP sends either the bare identifier or an object
 * describing it, and neither is Babel's to define — so a shape this does not recognize
 * contributes no name rather than a stringified object nobody can compare to a price list.
 */
function modelName(model: unknown): string {
  if (typeof model === "string") return model;
  if (model === null || typeof model !== "object") return "";
  for (const key of ["id", "model", "name"]) {
    const value = Reflect.get(model, key);
    if (typeof value === "string" && value.trim() !== "") return value;
  }
  return "";
}

/**
 * Answers one host_tool_call: a submission is validated and recorded, an evidence call is
 * authorized and served, and anything else is refused. A refusal is answered, not fatal: the run
 * continues so the model can correct itself.
 */
async function answerToolCall(run: Run, frame: Frame): Promise<void> {
  const id = frame.id ?? "";
  if (id === "") {
    throw new EngineFailure(
      ENGINE_FAILURES.malformedFrame,
      "a host_tool_call arrived with no id, which cannot be answered",
    );
  }
  run.toolCount += 1;
  if (run.toolCount > run.limits.maxToolCalls + TOOL_BUDGET_SLACK) {
    throw new EngineFailure(
      ENGINE_FAILURES.toolBudget,
      `${run.toolCount} calls against a budget of ${run.limits.maxToolCalls}`,
    );
  }
  const name = frame.toolName ?? "";
  const argumentBytes = frame.arguments === undefined ? 0 : JSON.stringify(frame.arguments).length;
  let allowed: boolean;
  let reason: string;
  let text: string;
  if (name === TOOL_SUBMIT) {
    const verdict = submit(run, frame.arguments);
    allowed = verdict.accepted;
    reason = verdict.reason;
    text = verdict.reason;
  } else if (run.toolCount > run.limits.maxToolCalls) {
    allowed = false;
    reason = "tool call budget exhausted";
    text = `refused (limit): ${reason}`;
  } else if (run.deps.broker === undefined) {
    allowed = false;
    reason = `the job registered no tool named ${name}`;
    text = `refused (unknown-tool): ${reason}`;
  } else {
    const served = await run.deps.broker.serve({
      name,
      callId: frame.toolCallId ?? "",
      arguments: frame.arguments,
      index: run.toolCount,
    });
    allowed = served.allow;
    reason = served.reason;
    text = served.allow ? (served.results ?? served.reason) : `refused (policy): ${served.reason}`;
  }
  // The receipt records the decision and the reason the model was given, and never the served
  // payload: the pipe carries content to the model because a model that cannot read a record
  // cannot form an observation about it, while the durable record keeps locators only.
  run.tools.push({ index: run.toolCount, tool: name, allowed, reason, argumentBytes });
  run.engine.write({
    type: "host_tool_result",
    id,
    result: { content: [{ type: "text", text }] },
    ...(allowed ? {} : { isError: true }),
  });
}

/**
 * Records one call to the submit tool. The engine validated the arguments against the job's
 * schema; the job's own `accept` decides the rest, and a refusal is what the model reads so it
 * can correct itself. An accepted submission replaces the earlier one; a refused one never does.
 */
function submit(run: Run, args: unknown): { accepted: boolean; reason: string } {
  run.submissions += 1;
  if (args === undefined || args === null) {
    return { accepted: false, reason: "the submission carries no arguments" };
  }
  const refusal = run.job.accept?.(args) ?? "";
  if (refusal !== "") return { accepted: false, reason: `submission refused: ${refusal}` };
  run.result = args;
  return { accepted: true, reason: "submission accepted as the job's result" };
}

/**
 * Asks the engine for its own accounting once the turn has ended. A refusal or a malformed answer
 * is recorded as no usage rather than as a run failure: the result is already in hand.
 */
async function stats(run: Run): Promise<void> {
  let data: unknown;
  try {
    data = await call(run, { id: run.command(), type: COMMAND.sessionStats });
  } catch {
    return;
  }
  const stated = SessionStatsSchema.safeParse(data);
  if (!stated.success) return;
  const tokens = stated.data.tokens ?? {
    input: 0,
    output: 0,
    reasoning: 0,
    cacheRead: 0,
    cacheWrite: 0,
    total: 0,
  };
  run.usage = {
    inputTokens: tokens.input,
    outputTokens: tokens.output,
    reasoningTokens: tokens.reasoning,
    cacheReadTokens: tokens.cacheRead,
    cacheWriteTokens: tokens.cacheWrite,
    totalTokens: tokens.total,
    costUsd: stated.data.cost,
    toolCalls: stated.data.toolCalls ?? run.toolCount,
    messages: stated.data.assistantMessages,
  };
}

/**
 * Ends the process: stdin closes, the tree is given the exit grace, and what is still alive after
 * that is killed. The verdict follows the precedence lingering > missing result > dirty exit.
 */
async function finish(run: Run): Promise<void> {
  run.engine.closeStdin();
  const left = await run.engine.awaitExit(run.limits.exitGraceMs);
  await run.engine.killTree(run.limits.terminateGraceMs);
  if (!left) {
    throw new EngineFailure(
      ENGINE_FAILURES.lingered,
      `the engine did not exit within ${run.limits.exitGraceMs}ms of its stdin closing`,
    );
  }
  if (run.result === null) {
    if (!run.prompted) {
      throw new EngineFailure(ENGINE_FAILURES.noResult, "the engine completed the prompt without a model turn");
    }
    throw new EngineFailure(ENGINE_FAILURES.noResult, `${run.submissions} submission(s), none accepted`);
  }
  const code = run.engine.exitCode();
  if (code !== 0) throw new EngineFailure(ENGINE_FAILURES.dirtyExit, `the engine exited with status ${code}`);
}

/**
 * The engine's own word for a provider refusal it is about to retry. It is the one thing on
 * stderr this client counts, and it is counted rather than parsed: the line's content decides
 * nothing, only that one more refusal happened.
 */
const RETRY_LINE = /\b429\b|rate.?limit|too many requests/i;

/**
 * Drains the engine's stderr into a bounded tail, and COUNTS the provider refusals in it.
 *
 * The tail is never treated as protocol: stderr carries the engine's own logging, and a run
 * steered by a log line would be a run anything in a transcript could steer. The bound is the
 * point — a process writing a gigabyte must not take Babel down.
 *
 * The count is the other half, and it is K-5 (#279 moved it here, the only place it was ever
 * consumed). A window that has run out does not refill inside one run: on 2026-09-13, 152.7 MB
 * went out and 2.0 MB came back across 21 sockets, and nothing anywhere could tell a retry from
 * an answer. Past `limits.maxRetries` the run is torn down with a NAMED failure, so the receipt
 * says `rate_limited` and an operator stops rather than adding fans.
 */
function collectDiagnostics(run: Run): void {
  const limit = run.limits.stderrTailBytes;
  run.engine.stderr.setEncoding("utf8");
  let pending = "";
  run.engine.stderr.on("data", (piece: string) => {
    run.stderr = (run.stderr + piece).slice(-limit);
    pending += piece;
    const lines = pending.split("\n");
    pending = lines.pop() ?? "";
    for (const line of lines) {
      if (!RETRY_LINE.test(line)) continue;
      run.retries += 1;
      // The tear-down is the abort the supervisor is already watching for: writing it here
      // would race the command in flight, and the run's own loop turns an abort into the
      // failure below on its next frame.
      if (run.retries > run.limits.maxRetries) run.rateLimited = true;
    }
  });
  run.engine.stderr.on("error", () => {});
}
