import { expect, test } from "bun:test";

import {
  RecallExcerptSchema,
  RecallMetadataSchema,
  type SessionRecordPosition,
} from "../contract.ts";
import { secretScan } from "./preflight.ts";
import { clipUtf8, recallRecordReader } from "./recall-records.ts";
import { sessionDigester } from "./session-records.ts";

const encoder = new TextEncoder();

function position(text: string, line = 1, byteOffset = 0, time: string | null = null): SessionRecordPosition {
  return {
    line, byteOffset, byteLength: encoder.encode(text).byteLength,
    digest: `sha256:${new Bun.CryptoHasher("sha256").update(text).digest("hex")}`, time,
  };
}

function stream(records: readonly unknown[]): string {
  return records.map(record => `${JSON.stringify(record)}\n`).join("");
}

test("UTF-8 prefixes never split Unicode or spend unused bytes on a later character", () => {
  const text = "aé😀z";
  for (const [limit, expected] of [[0, ""], [1, "a"], [2, "a"], [3, "aé"], [6, "aé"], [7, "aé😀"], [8, text]] as const) {
    const clipped = clipUtf8(text, limit);
    expect(clipped).toEqual({
      text: expected, bytes: encoder.encode(expected).byteLength, truncated: expected !== text,
    });
  }
  expect(() => clipUtf8(text, Number.POSITIVE_INFINITY)).toThrow(RangeError);
});

test("clipping preserves an archived U+FEFF at the start of a page", () => {
  expect(clipUtf8("\ufeffabc", 4)).toEqual({ text: "\ufeffa", bytes: 4, truncated: true });
  expect(clipUtf8("\ufeffabc", 2)).toEqual({ text: "", bytes: 0, truncated: true });
});

test("full-record preflight precedes prefix clipping for both evidence and metadata", async () => {
  const key = `${"AKIA"}IOSFODNN7SYNTH01`;
  const raw = stream([
    { type: "title", title: `fix ${key}` },
    { type: "session", cwd: `/archive/${key}` },
    { type: "message", message: { role: "user", content: key } },
  ]);
  const reader = recallRecordReader({
    harness: "omp", selection: { kind: "turns", first: 1, last: 1 }, maxBytes: 45,
  });
  const scan = secretScan();
  const digester = sessionDigester(reader.sink, scan);
  digester.write(encoder.encode(raw));
  const measured = digester.finish();
  const result = await reader.finish();
  expect(scan.report().sites.map(site => site.line)).toEqual([1, 2, 3]);
  expect(result.excerpt.text).toContain("[[babel-redacted:");
  expect(result.excerpt.text).not.toContain("AKIA");
  expect(result.metadata.title).toContain("[[babel-redacted:");
  expect(result.metadata.workspace).toContain("[[babel-redacted:");
  expect(JSON.stringify(result)).not.toContain(key);
  expect(result.records).toBe(measured.records);
  expect(result.excerpt.bytes).toBe(encoder.encode(result.excerpt.text).byteLength);
  expect(result.excerpt.truncated).toBe(true);
});

test("anchor equality checks every coordinate, digest and archived timestamp", async () => {
  const time = "2026-09-20T10:00:00.000Z";
  const record = stream([{ type: "message", timestamp: time, message: { role: "user", content: "hello" } }]);
  const exact = position(record, 1, 0, time);
  const wrong: SessionRecordPosition[] = [
    { ...exact, line: 2 }, { ...exact, byteOffset: 1 }, { ...exact, byteLength: exact.byteLength + 1 },
    { ...exact, digest: `sha256:${"0".repeat(64)}` }, { ...exact, time: null },
  ];
  for (const anchor of [exact, ...wrong]) {
    const reader = recallRecordReader({ harness: "omp", anchor });
    reader.sink.write(record);
    const result = await reader.finish();
    expect(result.anchorMatches).toBe(anchor === exact);
    expect(result.anchor).toEqual(anchor.line === 1 ? exact : null);
    if (anchor === exact) expect(result.excerpt.text).toBe(record);
  }
});

test("around counts physical normalized records and preserves opaque evidence", async () => {
  const before = "!opaque 😀\n";
  const at = "null\n";
  const after = "\n";
  const reader = recallRecordReader({
    harness: "codex", anchor: position(at, 2, encoder.encode(before).byteLength),
    selection: { kind: "around", records: 1 },
  });
  const bytes = encoder.encode(before + at + after + "!outside\n");
  for (const byte of bytes) reader.sink.write(new Uint8Array([byte]));
  const result = await reader.finish();
  expect(result.excerpt.text).toBe(before + at + after);
  expect(result.excerpt.firstRecord).toBe(1);
  expect(result.excerpt.lastRecord).toBe(3);
  expect(result.records).toBe(4);
  expect(result.bytes).toBe(bytes.byteLength);
  expect(result.anchorMatches).toBe(true);
  expect(result.turnsSupported).toBe(false);
  expect(RecallExcerptSchema.safeParse(result.excerpt).success).toBe(true);
});

test("OMP exchange ranges leave prelude at zero and keep assistant and tools in their turn", async () => {
  const records = [
    { type: "session", cwd: "/archived" },
    { type: "message", message: { role: "user", content: "first" } },
    { type: "message", message: { role: "assistant", content: "tool call" } },
    { type: "message", message: { role: "toolResult", content: "tool result" } },
    { type: "message", message: { role: "assistant", content: "answer" } },
    { type: "message", message: { role: "user", content: "second" } },
  ];
  const reader = recallRecordReader({ harness: "omp", selection: { kind: "turns", first: 1, last: 1 } });
  reader.sink.write(stream(records));
  const result = await reader.finish();
  expect(result.excerpt.text).toBe(stream(records.slice(1, 5)));
  expect([result.excerpt.firstRecord, result.excerpt.lastRecord]).toEqual([2, 5]);
  expect(result.turns).toBe(2);
  expect(result.turnsSupported).toBe(true);
});

test("Codex counts canonical user messages once, not duplicate delivery events", async () => {
  const records = [
    { type: "session_meta", payload: { cwd: "/saved" } },
    { type: "event_msg", payload: { type: "user_message", message: "first request" } },
    { type: "response_item", payload: { type: "message", role: "user", content: "canonical prompt" } },
    { type: "response_item", payload: { type: "function_call_output", output: "result" } },
    { type: "event_msg", payload: { type: "user_message", message: "second request" } },
    { type: "response_item", payload: { type: "message", role: "user", content: "second request" } },
    { type: "response_item", payload: { type: "message", role: "assistant", content: "done" } },
  ];
  const reader = recallRecordReader({ harness: "codex", selection: { kind: "turns", first: 2, last: 2 } });
  reader.sink.write(stream(records));
  const result = await reader.finish();
  expect(result.excerpt.text).toBe(stream(records.slice(5)));
  expect(result.turns).toBe(2);
  expect(result.metadata.workspace).toBe("/saved");
  expect(result.metadata.title).toBe("first request");
});

test("Codex injected context does not shift requested user turns", async () => {
  const message = (content: string) => ({
    type: "response_item", payload: { type: "message", role: "user", content },
  });
  const records = [
    message("<environment_context>\n<cwd>/saved</cwd>\n</environment_context>"),
    message("<user_instructions>\nRepository policy\n</user_instructions>"),
    message("first actual request"),
    { type: "response_item", payload: { type: "message", role: "assistant", content: "first answer" } },
    message("<environment_context>\n<cwd>/saved</cwd>\n</environment_context>"),
    message("second actual request"),
    { type: "response_item", payload: { type: "message", role: "assistant", content: "second answer" } },
  ];
  const reader = recallRecordReader({ harness: "codex", selection: { kind: "turns", first: 2, last: 2 } });
  reader.sink.write(stream(records));
  const result = await reader.finish();
  expect(result.turns).toBe(2);
  expect(result.excerpt.text).toBe(stream(records.slice(5)));
  expect(result.metadata.title).toBe("first actual request");
});

test("Claude tool-result-only wrappers stay in their exchange but mixed user input starts another", async () => {
  const records = [
    { type: "user", message: { role: "user", content: [{ type: "text", text: "first" }] } },
    { type: "assistant", message: { role: "assistant", content: "call" } },
    { type: "user", message: { role: "user", content: [{ type: "tool_result", content: "one" }, { type: "tool_result", content: "two" }] } },
    { type: "assistant", message: { role: "assistant", content: "done" } },
    { type: "user", message: { role: "user", content: [{ type: "tool_result", content: "result" }, { type: "text", text: "next question" }] } },
  ];
  const reader = recallRecordReader({ harness: "claude", selection: { kind: "turns", first: 1, last: 1 } });
  reader.sink.write(stream(records));
  const result = await reader.finish();
  expect(result.excerpt.text).toBe(stream(records.slice(0, 4)));
  expect(result.turns).toBe(2);
});

test("opaque archives cannot masquerade as turns or supply guessed metadata", async () => {
  const content = '!not json\n{"title":"unrecognized","workspace":"/live","repository":"host/repo"}\n';
  for (const harness of ["omp", "codex", "claude"] as const) {
    const reader = recallRecordReader({ harness, selection: { kind: "turns", first: 1, last: 10 } });
    reader.sink.write(content);
    const result = await reader.finish();
    expect(result.metadata).toEqual({ title: null, workspace: null, repository: null, metadataOrigin: "archive" });
    expect(result.turnsSupported).toBe(false);
    expect(result.turns).toBe(0);
    expect(result.excerpt.text).toBe("");
    expect([result.excerpt.firstRecord, result.excerpt.lastRecord]).toEqual([0, 0]);
  }
});

test("metadata-only reads remain empty, bounded and faithful to archived field precedence", async () => {
  const reader = recallRecordReader({ harness: "omp" });
  reader.sink.write(stream([
    { type: "session", title: "fallback", cwd: "/" + "😀".repeat(1000) },
    { type: "title", title: "é".repeat(1000) },
  ]));
  const result = await reader.finish();
  expect(result.metadata.title).toBe("é".repeat(128));
  expect(result.metadata.workspace).toBe("/" + "😀".repeat(511));
  expect(RecallMetadataSchema.safeParse(result.metadata).success).toBe(true);
  expect(result.excerpt.text).toBe("");
  expect(result.excerpt.bytes).toBe(0);
  expect(result.records).toBe(2);
  expect(result.excerpt.firstRecord).toBe(0);
  expect(result.excerpt.lastRecord).toBe(0);
});

test("Claude conflicting archived workspaces remain unknown even when clipped prefixes agree", async () => {
  const reader = recallRecordReader({ harness: "claude" });
  const prefix = "/" + "a".repeat(3000);
  reader.sink.write(stream([
    { cwd: prefix + "/one", aiTitle: "first" },
    { cwd: prefix + "/two", aiTitle: "latest" },
    { cwd: prefix + "/one" },
  ]));
  const result = await reader.finish();
  expect(result.metadata.workspace).toBeNull();
  expect(result.metadata.title).toBe("latest");
});

test("tiny excerpts stay bounded across oversized selected records while consuming the entire replay", async () => {
  const huge = stream([{ type: "message", message: { role: "user", content: "x".repeat(2 << 20) } }]);
  const reader = recallRecordReader({ harness: "omp", selection: { kind: "turns", first: 1, last: 1000 }, maxBytes: 7 });
  reader.sink.write(huge);
  const following = stream([{ type: "message", message: { role: "assistant", content: "y".repeat(8192) } }]);
  for (let count = 0; count < 128; count++) reader.sink.write(following);
  const result = await reader.finish();
  expect(result.excerpt.text).toBe(huge.slice(0, 7));
  expect(result.excerpt.bytes).toBe(7);
  expect(result.excerpt.truncated).toBe(true);
  expect(result.excerpt.firstRecord).toBe(1);
  expect(result.excerpt.lastRecord).toBe(1);
  expect(result.records).toBe(129);
  expect(result.bytes).toBe(encoder.encode(huge).byteLength + 128 * encoder.encode(following).byteLength);
});

test("a full excerpt does not hide invalid UTF-8 later in the replay", async () => {
  const record = "!hello\n";
  const reader = recallRecordReader({ harness: "omp", anchor: position(record), maxBytes: 1 });
  reader.sink.write(record);
  reader.sink.write(new Uint8Array([0xf0, 0x9f]));
  await expect(reader.finish()).rejects.toThrow();
});

test("finish flushes an unterminated record once and does not invent a newline", async () => {
  const record = "!é😀";
  const reader = recallRecordReader({ harness: "omp", anchor: position(record), maxBytes: 7 });
  reader.sink.write(record.slice(0, 3));
  reader.sink.write(record.slice(3));
  const first = reader.finish();
  expect(reader.finish()).toBe(first);
  const result = await first;
  expect(result.excerpt.text).toBe(record);
  expect(result.excerpt.bytes).toBe(7);
  expect(result.excerpt.truncated).toBe(false);
  expect(result.records).toBe(1);
  expect(result.anchorMatches).toBe(true);
});

test("an unfilled UTF-8 byte budget never skips forward to later selected records", async () => {
  const first = "!😀\n";
  const reader = recallRecordReader({
    harness: "omp", anchor: position(first), selection: { kind: "around", records: 1 }, maxBytes: 2,
  });
  reader.sink.write(first + "!a\n");
  const result = await reader.finish();
  expect(result.excerpt.text).toBe("!");
  expect(result.excerpt.bytes).toBe(1);
  expect(result.excerpt.truncated).toBe(true);
  expect(result.excerpt.lastRecord).toBe(1);
  expect(result.records).toBe(2);
});
