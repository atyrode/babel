import {
  RECALL_MAX_EXCERPT_BYTES,
  RECALL_SEARCH_EXCERPT_BYTES,
  RECALL_UNTRUSTED_BEGIN,
  RECALL_UNTRUSTED_END,
  RecallMetadataSchema,
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
export function clipUtf8(text: string, maxBytes: number): {
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
  if (typeof message === "string" && message !== "") return clipUtf8(message, MAX_REQUEST_BYTES).text;
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
type Harness = "omp" | "codex" | "claude";

/** Whether this record begins an actual user exchange, rather than tool traffic. */
function startsTurn(harness: Harness, fields: Record<string, unknown>): boolean {
  if (harness === "codex") {
    const payload = object(fields["payload"]);
    return fields["type"] === "response_item" && payload?.["type"] === "message" &&
      payload["role"] === "user";
  }
  const message = object(fields["message"]);
  if (message?.["role"] !== "user") return false;
  if (harness === "omp") return fields["type"] === "message";
  if (fields["type"] !== "user") return false;
  const content = message["content"];
  // Claude wraps tool responses in a user envelope, but no new exchange begins there.
  return !Array.isArray(content) || content.length === 0 ||
    !content.every(part => object(part)?.["type"] === "tool_result");
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
    title: null, workspace: null, repository: null, metadataOrigin: "archive",
  };
  const codex: TitleEvidence = { source: NO_THREAD_SOURCE, request: "", requestFallback: "" };
  let fallbackTried = 0;
  let codexWorkspace: string | null = null;
  let workspaceDigest: string | null = null;
  let workspaceConflict = false;
  let ompTitle = false;
  let records = 0;
  let bytes = 0;
  let anchor: SessionRecordPosition | null = null;
  let anchorMatches = options.anchor === undefined;
  let turns = 0;
  const excerpt: RecallExcerpt = {
    trust: "archived-untrusted", begin: RECALL_UNTRUSTED_BEGIN, text: "",
    end: RECALL_UNTRUSTED_END, maxBytes, bytes: 0, truncated: false,
    firstRecord: 0, lastRecord: 0,
  };

  const observeMetadata = (fields: Record<string, unknown>): void => {
    if (options.harness === "omp") {
      if (fields["type"] === "title") {
        const title = metadataText(fields["title"], titleBytes);
        if (!ompTitle && title !== null) {
          metadata.title = title;
          ompTitle = true;
        }
      } else if (fields["type"] === "session") {
        if (metadata.title === null) metadata.title = metadataText(fields["title"], titleBytes);
        if (metadata.workspace === null) metadata.workspace = metadataText(fields["cwd"], workspaceBytes);
      }
    } else if (options.harness === "claude") {
      const title = metadataText(fields["aiTitle"], titleBytes);
      if (title !== null) metadata.title = title;
      const cwd = fields["cwd"];
      if (!workspaceConflict && typeof cwd === "string" && cwd !== "") {
        const digest = new Bun.CryptoHasher("sha256").update(cwd).digest("hex");
        if (workspaceDigest === null) {
          workspaceDigest = digest;
          metadata.workspace = metadataText(cwd, workspaceBytes);
        } else if (workspaceDigest !== digest) {
          workspaceConflict = true;
          metadata.workspace = null;
        }
      }
    } else {
      const body = object(fields["payload"]);
      if (body === null) return;
      if (fields["type"] === "session_meta") {
        if (codexWorkspace === null) codexWorkspace = metadataText(body["cwd"], workspaceBytes);
        if (codex.source === NO_THREAD_SOURCE) {
          const source = decodeThreadSource(body["source"]);
          if (source !== NO_THREAD_SOURCE) codex.source = {
            role: clipUtf8(source.role, MAX_REQUEST_BYTES).text,
            spawn: source.spawn,
            agentPath: clipUtf8(source.agentPath, MAX_REQUEST_BYTES).text,
            agentRole: clipUtf8(source.agentRole, MAX_REQUEST_BYTES).text,
          };
        }
      } else if (fields["type"] === "turn_context") {
        const cwd = metadataText(body["cwd"], workspaceBytes);
        if (cwd !== null) metadata.workspace = cwd;
      } else if (fields["type"] === "event_msg" && body["type"] === "user_message") {
        if (codex.request === "") codex.request = requestText(body);
      } else if (fields["type"] === "response_item" && body["type"] === "message" &&
        body["role"] === "user" && codex.requestFallback === "" && fallbackTried < MAX_REQUEST_CANDIDATES) {
        fallbackTried++;
        const text = requestText(body);
        if (text.trim() !== "" && !injectedBlock(text)) codex.requestFallback = text;
      }
    }
  };

  const sink = readNormalizedRecords((text, position, parsed) => {
    records++;
    bytes += position.byteLength;
    if (options.anchor?.line === position.line) {
      anchor = position;
      anchorMatches = options.anchor.byteOffset === position.byteOffset &&
        options.anchor.byteLength === position.byteLength && options.anchor.digest === position.digest &&
        options.anchor.time === position.time;
    }
    const fields = object(parsed);
    if (fields !== null) {
      observeMetadata(fields);
      if (startsTurn(options.harness, fields)) turns++;
    }
    const selection = options.selection;
    const selected = selection?.kind === "turns"
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
      return finished ??= (async () => {
        await sink.close();
        if (options.harness === "codex") {
          metadata.title = metadataText(deriveTitle(codex).title, titleBytes);
          metadata.workspace = codexWorkspace ?? metadata.workspace;
        }
        return { excerpt, metadata, records, bytes, anchor, anchorMatches, turns, turnsSupported: turns > 0 };
      })();
    },
  };
}
