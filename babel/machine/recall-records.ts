import {
  RECALL_MAX_EXCERPT_BYTES,
  RECALL_SEARCH_EXCERPT_BYTES,
  RECALL_UNTRUSTED_BEGIN,
  RECALL_UNTRUSTED_END,
  RecallMetadataSchema,
  type Harness,
  type RecallExcerpt,
  type RecallMetadata,
  type RecallShowRequest,
  type SessionRecordPosition,
} from "../contract.ts";
import {
  decodeThreadSource,
  deriveTitle,
  injectedBlock,
  MAX_REQUEST_BYTES,
  MAX_REQUEST_CANDIDATES,
  NO_THREAD_SOURCE,
  type TitleEvidence,
} from "./adapters/codex-title.ts";
import type { RecordSink } from "./output.ts";
import { readNormalizedRecords } from "./session-index.ts";

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { ignoreBOM: true });
const titleBytes = RecallMetadataSchema.shape.title.unwrap().maxLength!;
const workspaceBytes = RecallMetadataSchema.shape.workspace.unwrap().maxLength!;

/** Clip only already-redacted text; never allocate an encoding of its unbounded suffix. */
export function clipUtf8(
  text: string,
  maxBytes: number,
): {
  text: string;
  bytes: number;
  truncated: boolean;
} {
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new RangeError("Invalid byte bound");
  let end = 0;
  let bytes = 0;
  while (end < text.length) {
    const point = text.codePointAt(end)!;
    const width = point <= 0x7f ? 1 : point <= 0x7ff ? 2 : point <= 0xffff ? 3 : 4;
    if (bytes + width > maxBytes) break;
    bytes += width;
    end += point > 0xffff ? 2 : 1;
  }
  const truncated = end < text.length;
  // Decode a bounded allocation so a tiny slice cannot retain a multi-megabyte record.
  const clipped = truncated ? decoder.decode(encoder.encode(text.slice(0, end))) : text;
  return { text: clipped, bytes, truncated };
}

function object(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function metadataText(value: unknown, maxBytes: number): string | null {
  return typeof value === "string" && value !== "" ? clipUtf8(value, maxBytes).text || null : null;
}

/** Same Codex message fields as its adapter, without retaining or encoding oversized parts. */
function requestText(body: Record<string, unknown>): string {
  const message = body["message"];
  if (typeof message === "string" && message !== "")
    return clipUtf8(message, MAX_REQUEST_BYTES).text;
  const content = body["content"];
  if (typeof content === "string") return clipUtf8(content, MAX_REQUEST_BYTES).text;
  if (!Array.isArray(content)) return "";
  let text = "";
  let bytes = 0;
  for (const part of content) {
    const value = object(part)?.["text"];
    if (typeof value !== "string" || value === "") continue;
    if (text !== "") {
      if (bytes === MAX_REQUEST_BYTES) break;
      text += "\n";
      bytes++;
    }
    const clipped = clipUtf8(value, MAX_REQUEST_BYTES - bytes);
    text += clipped.text;
    bytes += clipped.bytes;
    if (clipped.truncated || bytes === MAX_REQUEST_BYTES) break;
  }
  return text;
}

type Selection = RecallShowRequest["selection"];

/**
 * The user request one Codex record carries — a `response_item` user message — or null for any
 * other record and any other harness. It is computed once per record because two readers want
 * it: the metadata fold (a title's fallback evidence) and the turn counter.
 */
export function userRequest(harness: Harness, fields: Record<string, unknown>): string | null {
  if (harness !== "codex" || fields["type"] !== "response_item") return null;
  const body = object(fields["payload"]);
  return body?.["type"] === "message" && body["role"] === "user" ? requestText(body) : null;
}

/** What a session's own records say about it, as a reading of its normalized stream found. */
export interface SessionMetadata {
  readonly title: string | null;
  /** `recorded` when the harness wrote the title into its log, `derived` when Babel's rule over
   *  the log produced it; null exactly when there is no title. */
  readonly titleProvenance: "recorded" | "derived" | null;
  readonly workspace: string | null;
}

export interface MetadataFold {
  /** Folds one parsed record; `request` is {@link userRequest} of the same record. */
  observe(fields: Record<string, unknown>, request: string | null): void;
  finish(): SessionMetadata;
}

/**
 * THE ARCHIVE-SAFE METADATA RULE, spelled once for every reader of a normalized stream: Recall
 * over an archived capture and a preparation over the capture it seals (#453). It reads records
 * only, never a file, so what it finds is what the stream it was handed says — after redaction,
 * when that stream was redacted. Every value is clipped to Recall's published bounds.
 *
 * OMP and Claude Code write their title into the log, so theirs is `recorded`; Codex keeps none,
 * and the title is derived from the thread's own request by `codex-title.ts`'s rule.
 */
export function metadataFold(harness: Harness): MetadataFold {
  let title: string | null = null;
  let workspace: string | null = null;
  const codex: TitleEvidence = { source: NO_THREAD_SOURCE, request: "", requestFallback: "" };
  let fallbackTried = 0;
  let codexWorkspace: string | null = null;
  let workspaceDigest: string | null = null;
  let workspaceConflict = false;
  let ompTitle = false;
  return {
    observe(fields, request) {
      if (harness === "omp") {
        if (fields["type"] === "title") {
          const recorded = metadataText(fields["title"], titleBytes);
          if (!ompTitle && recorded !== null) {
            title = recorded;
            ompTitle = true;
          }
        } else if (fields["type"] === "session") {
          if (title === null) title = metadataText(fields["title"], titleBytes);
          if (workspace === null) workspace = metadataText(fields["cwd"], workspaceBytes);
        }
      } else if (harness === "claude") {
        const recorded = metadataText(fields["aiTitle"], titleBytes);
        if (recorded !== null) title = recorded;
        const cwd = fields["cwd"];
        if (!workspaceConflict && typeof cwd === "string" && cwd !== "") {
          const digest = new Bun.CryptoHasher("sha256").update(cwd).digest("hex");
          if (workspaceDigest === null) {
            workspaceDigest = digest;
            workspace = metadataText(cwd, workspaceBytes);
          } else if (workspaceDigest !== digest) {
            workspaceConflict = true;
            workspace = null;
          }
        }
      } else {
        const body = object(fields["payload"]);
        if (body === null) return;
        if (fields["type"] === "session_meta") {
          if (codexWorkspace === null) codexWorkspace = metadataText(body["cwd"], workspaceBytes);
          if (codex.source === NO_THREAD_SOURCE) {
            const source = decodeThreadSource(body["source"]);
            if (source !== NO_THREAD_SOURCE)
              codex.source = {
                role: clipUtf8(source.role, MAX_REQUEST_BYTES).text,
                spawn: source.spawn,
                agentPath: clipUtf8(source.agentPath, MAX_REQUEST_BYTES).text,
                agentRole: clipUtf8(source.agentRole, MAX_REQUEST_BYTES).text,
              };
          }
        } else if (fields["type"] === "turn_context") {
          const cwd = metadataText(body["cwd"], workspaceBytes);
          if (cwd !== null) workspace = cwd;
        } else if (fields["type"] === "event_msg" && body["type"] === "user_message") {
          if (codex.request === "") codex.request = requestText(body);
        } else if (
          request !== null &&
          codex.requestFallback === "" &&
          fallbackTried < MAX_REQUEST_CANDIDATES
        ) {
          fallbackTried++;
          if (request.trim() !== "" && !injectedBlock(request)) codex.requestFallback = request;
        }
      }
    },
    finish() {
      if (harness !== "codex")
        return { title, titleProvenance: title === null ? null : "recorded", workspace };
      const derived = metadataText(deriveTitle(codex).title, titleBytes);
      return {
        title: derived,
        titleProvenance: derived === null ? null : "derived",
        workspace: codexWorkspace ?? workspace,
      };
    },
  };
}

/** Whether this record begins an actual user exchange, rather than tool traffic. */
function startsTurn(
  harness: Harness,
  fields: Record<string, unknown>,
  codexRequest: string | null,
): boolean {
  if (harness === "codex") return codexRequest !== null && !injectedBlock(codexRequest);
  const message = object(fields["message"]);
  if (message?.["role"] !== "user") return false;
  if (harness === "omp") return fields["type"] === "message";
  if (fields["type"] !== "user") return false;
  const content = message["content"];
  // Claude wraps tool responses in a user envelope, but no new exchange begins there.
  return (
    !Array.isArray(content) ||
    content.length === 0 ||
    !content.every((part) => object(part)?.["type"] === "tool_result")
  );
}

export interface RecallRecordReading {
  excerpt: RecallExcerpt;
  metadata: RecallMetadata;
  records: number;
  bytes: number;
  anchor: SessionRecordPosition | null;
  anchorMatches: boolean;
  turns: number;
  turnsSupported: boolean;
}

/**
 * Replay an already-normalized, mandatory-redacted archive. All records, including those after
 * the excerpt fills, go through the shared framing/hash validator. The caller must also verify
 * the replay's whole-stream source digest before publishing the result.
 */
export function recallRecordReader(options: {
  harness: Harness;
  anchor?: SessionRecordPosition;
  selection?: Selection;
  maxBytes?: number;
}): {
  sink: RecordSink;
  finish(): Promise<RecallRecordReading>;
} {
  const maxBytes = options.maxBytes ?? RECALL_SEARCH_EXCERPT_BYTES;
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 1 || maxBytes > RECALL_MAX_EXCERPT_BYTES)
    throw new RangeError("Invalid excerpt byte bound");
  const metadata: RecallMetadata = {
    title: null,
    workspace: null,
    repository: null,
    metadataOrigin: "archive",
  };
  const fold = metadataFold(options.harness);
  let records = 0;
  let bytes = 0;
  let anchor: SessionRecordPosition | null = null;
  let anchorMatches = options.anchor === undefined;
  let turns = 0;
  const excerpt: RecallExcerpt = {
    trust: "archived-untrusted",
    begin: RECALL_UNTRUSTED_BEGIN,
    text: "",
    end: RECALL_UNTRUSTED_END,
    maxBytes,
    bytes: 0,
    truncated: false,
    firstRecord: 0,
    lastRecord: 0,
  };

  const sink = readNormalizedRecords((text, position, parsed) => {
    records++;
    bytes += position.byteLength;
    if (options.anchor?.line === position.line) {
      anchor = position;
      anchorMatches =
        options.anchor.byteOffset === position.byteOffset &&
        options.anchor.byteLength === position.byteLength &&
        options.anchor.digest === position.digest &&
        options.anchor.time === position.time;
    }
    const fields = object(parsed);
    if (fields !== null) {
      const codexRequest = userRequest(options.harness, fields);
      fold.observe(fields, codexRequest);
      if (startsTurn(options.harness, fields, codexRequest)) turns++;
    }
    const selection = options.selection;
    const selected =
      selection?.kind === "turns"
        ? turns >= selection.first && turns <= selection.last
        : options.anchor !== undefined &&
          Math.abs(position.line - options.anchor.line) <= (selection?.records ?? 0);
    if (!selected) return;
    if (excerpt.truncated) return;
    const clipped = clipUtf8(text, maxBytes - excerpt.bytes);
    if (clipped.bytes > 0) {
      excerpt.text += clipped.text;
      excerpt.bytes += clipped.bytes;
      if (excerpt.firstRecord === 0) excerpt.firstRecord = position.line;
      excerpt.lastRecord = position.line;
    }
    if (clipped.truncated) {
      excerpt.truncated = true;
      return;
    }
    // The shared reader removes framing, not evidence: restore only a newline actually present.
    if (position.byteLength > clipped.bytes) {
      if (excerpt.bytes === maxBytes) excerpt.truncated = true;
      else {
        excerpt.text += "\n";
        excerpt.bytes++;
        if (excerpt.firstRecord === 0) excerpt.firstRecord = position.line;
        excerpt.lastRecord = position.line;
      }
    }
  });
  let finished: Promise<RecallRecordReading> | undefined;
  return {
    sink,
    finish() {
      return (finished ??= (async () => {
        await sink.close();
        const found = fold.finish();
        metadata.title = found.title;
        metadata.workspace = found.workspace;
        return {
          excerpt,
          metadata,
          records,
          bytes,
          anchor,
          anchorMatches,
          turns,
          turnsSupported: turns > 0,
        };
      })());
    },
  };
}
