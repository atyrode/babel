import { afterEach, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { MAX_MATERIAL_BYTES, type SessionRecordPosition } from "../contract.ts";
import { sessionRef } from "./adapters/index.ts";
import type { Observation, Reading, ReadingContext } from "./cache.ts";
import type { RecordSink } from "./output.ts";
import {
  readNormalizedRecords,
  sessionIndex,
  SessionIndexError,
  type IndexedSession,
  type SessionIndex,
} from "./session-index.ts";

const CONTEXT: ReadingContext = { schema: 1, detectors: "test-detectors/1", mode: "redact" };
const roots: string[] = [];
const handles: SessionIndex[] = [];

afterEach(() => {
  for (const handle of handles.splice(0)) handle.close();
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

async function open(
  dir?: string,
  context: ReadingContext = CONTEXT,
): Promise<{ index: SessionIndex; dir: string }> {
  if (dir === undefined) {
    dir = mkdtempSync(join(tmpdir(), "babel-session-index-"));
    roots.push(dir);
  }
  const index = await sessionIndex(dir, context);
  handles.push(index);
  return { index, dir };
}

function candidate(id: string, size = 10): IndexedSession {
  // These paths intentionally do not exist: only the supplied callback may read a source.
  return {
    session: sessionRef("omp", id, `/unopened-session-index-fixture/${id}.jsonl`),
    seen: { size, modifiedAt: 1000 },
  };
}

function reading(seen: Observation): Reading {
  return {
    bytes: seen.size,
    records: 1,
    captureDigest: `sha256:${"a".repeat(64)}`,
    sourceDigest: `sha256:${"b".repeat(64)}`,
    report: null,
  };
}

function content(entry: IndexedSession, text: string) {
  return async (sink: RecordSink): Promise<{ reading: Reading; after: Observation }> => {
    sink.write(`${JSON.stringify({ text })}\n`);
    await sink.close();
    return { reading: reading(entry.seen), after: entry.seen };
  };
}

test("concurrent builders never duplicate a source read and readers retain the committed generation", async () => {
  const { index: first, dir } = await open();
  const { index: second } = await open(dir);
  const old = candidate("concurrent");
  await first.build(old, content(old, "previous"));
  const next = { ...old, seen: { ...old.seen, modifiedAt: 2000 } };
  let release = (): void => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let reads = 0;
  const building = first.build(next, async (sink) => {
    reads += 1;
    sink.write('{"text":"replacement"}\n');
    await gate;
    return { reading: reading(next.seen), after: next.seen };
  });
  try {
    const forbidden = async (sink: RecordSink) => {
      reads += 1;
      return content(next, "wrong")(sink);
    };
    expect(await first.build(next, forbidden)).toBe("busy");
    expect(await second.build(next, forbidden)).toBe("busy");
    expect(second.holds(old)).toBe(true);
    expect(second.search("previous", [old], 10, MAX_MATERIAL_BYTES).selection).toEqual([
      old.session,
    ]);
    expect(second.search("replacement", [old], 10, MAX_MATERIAL_BYTES).matches).toBe(0);
    expect(second.searchRecords("previous", [old], 10).hits[0]?.position.digest)
      .toBe(digest('{"text":"previous"}\n'));
    expect(second.searchRecords("replacement", [old], 10).matches).toBe(0);
    expect(reads).toBe(1);
  } finally {
    release();
    await building;
  }
  expect(
    await second.build(next, async (sink) => {
      reads += 1;
      return content(next, "wrong")(sink);
    }),
  ).toBe("reused");
  expect(reads).toBe(1);
  expect(second.holds(old)).toBe(false);
  expect(second.search("replacement", [next], 10, MAX_MATERIAL_BYTES).selection).toEqual([
    next.session,
  ]);
  expect(second.search("previous", [next], 10, MAX_MATERIAL_BYTES).matches).toBe(0);
});

test("a concurrent context replacement refuses incomplete eligible coverage", async () => {
  const { index, dir } = await open();
  const entry = candidate("context-race");
  await index.build(entry, content(entry, "needle"));
  const { index: newer } = await open(dir, { ...CONTEXT, detectors: "next-detectors" });
  await newer.build(entry, content(entry, "needle"));
  expect(() => index.search("needle", [entry], 10, 100)).toThrow(SessionIndexError);
  expect(() => index.searchRecords("needle", [entry], 10)).toThrow(SessionIndexError);
  expect(newer.search("needle", [entry], 10, 100).selection).toEqual([entry.session]);
  await index.build(entry, content(entry, "needle"));
  expect(index.search("needle", [entry], 10, 100).selection).toEqual([entry.session]);
});

test("a failed replacement rolls back its tokens and releases the writer for another handle", async () => {
  const { index, dir } = await open();
  const old = candidate("rollback");
  const next = { ...old, seen: { ...old.seen, size: 20 } };
  await index.build(old, content(old, "durable"));
  const failure = index.build(next, async (sink) => {
    sink.write('{"text":"partial"}\n');
    throw new Error("credential-and-transcript-must-not-escape");
  });
  const error: unknown = await failure.catch((error: unknown) => error);
  expect(error).toBeInstanceOf(SessionIndexError);
  expect(error).toMatchObject({ kind: "unavailable" });
  expect(String(error)).not.toContain("credential-and-transcript-must-not-escape");
  const { index: other } = await open(dir);
  expect(other.search("durable", [old], 10, 100).selection).toEqual([old.session]);
  expect(other.searchRecords("durable", [old], 10).matches).toBe(1);
  expect(other.searchRecords("partial", [old], 10).matches).toBe(0);
  expect(other.search("partial", [old], 10, 100).matches).toBe(0);
  expect(await other.build(next, content(next, "complete"))).toBe("indexed");
  expect(other.search("complete", [next], 10, 100).selection).toEqual([next.session]);
});

test("invalid normalized UTF-8 cannot publish replacement records, including a torn final codepoint", async () => {
  const { index } = await open();
  const old = candidate("invalid-utf8");
  const next = { ...old, seen: { ...old.seen, modifiedAt: 2000 } };
  await index.build(old, content(old, "retained"));
  for (const bytes of [Uint8Array.of(0x21, 0xff, 0x0a), Uint8Array.of(0x21, 0xf0, 0x9f)]) {
    const failure = index.build(next, async (sink) => {
      sink.write('{"text":"partial"}\n');
      sink.write(bytes);
      return { reading: reading(next.seen), after: next.seen };
    });
    await expect(failure).rejects.toMatchObject({ kind: "unavailable" });
    expect(index.holds(next)).toBe(false);
    expect(index.searchRecords("retained", [old], 10).matches).toBe(1);
    expect(index.searchRecords("partial", [old], 10).matches).toBe(0);
  }
});

test("a changed observation or incomplete byte count cannot publish a replacement", async () => {
  const { index } = await open();
  const old = candidate("changed");
  await index.build(old, content(old, "original"));
  const next = { ...old, seen: { ...old.seen, modifiedAt: 2000 } };
  for (const result of [
    { reading: reading(next.seen), after: { ...next.seen, modifiedAt: 3000 } },
    { reading: { ...reading(next.seen), bytes: next.seen.size - 1 }, after: next.seen },
  ]) {
    expect(
      await index.build(next, async (sink) => {
        sink.write('{"text":"uncommitted"}\n');
        return result;
      }),
    ).toBe("changed");
    expect(index.holds(next)).toBe(false);
    expect(index.search("original", [old], 10, 100).selection).toEqual([old.session]);
    expect(index.search("uncommitted", [old], 10, 100).matches).toBe(0);
  }
});

test("source identity, observation and every reading context field participate in coverage", async () => {
  const { index, dir } = await open();
  const original = candidate("identity");
  await index.build(original, content(original, "identityword"));
  const different: IndexedSession[] = [
    { ...original, session: { ...original.session, selector: "omp/other" } },
    { ...original, session: { ...original.session, sourceId: "other" } },
    { ...original, session: { ...original.session, harness: "codex" } },
    { ...original, session: { ...original.session, primaryPath: "/moved/log" } },
    { ...original, seen: { ...original.seen, size: 11 } },
    { ...original, seen: { ...original.seen, modifiedAt: 1001 } },
    { ...original, seen: { ...original.seen, capture: "immutable-capture" } },
    { ...original, namespace: "archive-host" },
    { ...original, seen: { ...original.seen, modifiedAt: 0 } },
  ];
  for (const changed of different) {
    expect(index.holds(changed)).toBe(false);
    expect(() => index.search("identityword", [changed], 10, 100)).toThrow(SessionIndexError);
    expect(() => index.searchRecords("identityword", [changed], 10)).toThrow(SessionIndexError);
  }
  let reads = 0;
  expect(
    await index.build({ ...original, seen: { size: 10, modifiedAt: 0 } }, async (sink) => {
      reads += 1;
      return content(original, "wrong")(sink);
    }),
  ).toBe("changed");
  expect(reads).toBe(0);
  for (const context of [
    { ...CONTEXT, schema: 2 },
    { ...CONTEXT, detectors: "different" },
  ]) {
    const { index: other } = await open(dir, context);
    expect(other.holds(original)).toBe(false);
    expect(() => other.search("identityword", [original], 10, 100)).toThrow(SessionIndexError);
  }
  await expect(sessionIndex(dir, { ...CONTEXT, mode: "off" })).rejects.toMatchObject({
    kind: "unavailable",
  });
  expect(index.holds(original)).toBe(true);
});

test("literal queries find escaped and UTF-8 content across arbitrary replay boundaries", async () => {
  const { index } = await open();
  const escaped = candidate("escaped");
  const absent = candidate("absent");
  await index.build(escaped, async (sink) => {
    const bytes = new TextEncoder().encode(
      '{"text":"needle caf\\u00e9 café \\ud83d\\ude80\\n東京"}\n!literal\\backslash\n',
    );
    // One-byte replay cuts through escapes, multibyte codepoints, JSON and record boundaries.
    for (const byte of bytes) sink.write(Uint8Array.of(byte));
    await sink.close();
    await sink.close();
    return { reading: reading(escaped.seen), after: escaped.seen };
  });
  await index.build(absent, content(absent, "absent"));
  expect(index.search('needle" NOT absent*', [escaped, absent], 10, 100).matches).toBe(2);
  for (const query of ["café", "東京", "backslash"]) {
    expect(index.search(query, [escaped], 10, 100).selection).toEqual([escaped.session]);
    const records = index.searchRecords(query, [escaped], 10);
    expect(records.matches).toBe(1);
    expect(records.hits.map((hit) => hit.candidate.session)).toEqual([escaped.session]);
  }
  expect(index.search('"***()"', [escaped, absent], 10, 100)).toEqual({
    selection: [],
    matches: 0,
    overBound: 0,
  });
});

test("oversized normalized records refuse atomically even if the callback swallows a sink error", async () => {
  const { index } = await open();
  const old = candidate("oversized");
  const next = { ...old, seen: { ...old.seen, modifiedAt: 2000 } };
  await index.build(old, content(old, "retained"));
  await expect(
    index.build(next, async (sink) => {
      sink.write('{"text":"partial"}\n');
      try {
        const chunk = "x".repeat(1024 * 1024);
        for (let i = 0; i < 65; i += 1) sink.write(chunk);
      } catch {
        /* A faulty producer must not publish partial coverage. */
      }
      return { reading: reading(next.seen), after: next.seen };
    }),
  ).rejects.toMatchObject({ kind: "unavailable" });
  expect(index.holds(next)).toBe(false);
  expect(index.search("retained", [old], 10, 100).selection).toEqual([old.session]);
  expect(index.search("partial", [old], 10, 100).matches).toBe(0);
});

test("search counts only distinct eligible identities and skips byte-overbound hits", async () => {
  const { index } = await open();
  const a = candidate("a", 11);
  const b = candidate("b", 6);
  const c = candidate("c", 4);
  const excluded = candidate("excluded", 1);
  for (const entry of [excluded, c, b, a]) await index.build(entry, content(entry, "matching"));
  const result = index.search("matching", [c, b, a, b], 2, 10);
  expect(result).toEqual({ selection: [b.session, c.session], matches: 3, overBound: 1 });
  expect(index.search("matching", [c, b, a], 1, 10)).toEqual({
    selection: [b.session],
    matches: 3,
    overBound: 1,
  });
  expect(index.search("matching", [c, b], 1, 100)).toEqual({
    selection: [b.session],
    matches: 2,
    overBound: 0,
  });
  expect(index.search("matching", [], 10, 100)).toEqual({
    selection: [],
    matches: 0,
    overBound: 0,
  });
});

test("ranking uses the best matching passage, not recency or the number of matching passages", async () => {
  const { index } = await open();
  const weak = candidate("a-weak");
  const strong = candidate("z-strong");
  await index.build(weak, async (sink) => {
    for (let i = 0; i < 20; i += 1)
      sink.write(`${JSON.stringify({ text: `needle ${"padding ".repeat(100)}` })}\n`);
    return { reading: reading(weak.seen), after: weak.seen };
  });
  await index.build(strong, content(strong, "needle"));
  expect(index.search("needle", [weak, strong], 1, 100)).toEqual({
    selection: [strong.session],
    matches: 2,
    overBound: 0,
  });
});

function digest(bytes: string | Uint8Array): string {
  return `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

function normalized(entry: IndexedSession, stream: string) {
  return async (sink: RecordSink): Promise<{ reading: Reading; after: Observation }> => {
    sink.write(stream);
    return {
      reading: { ...reading(entry.seen), sourceDigest: digest(stream) },
      after: entry.seen,
    };
  };
}

test("normalized record positions hash exact Unicode bytes and physical LF boundaries independently of chunks", async () => {
  const lines = [
    '{"text":"café 東京 🚀","timestamp":"2026-09-20T12:00:00+02:00"}\n',
    "\n",
    '!opaque café\\n🚀\n',
    '{"text":"escaped \\ud83d\\ude80"}',
  ];
  const stream = lines.join("");
  const bytes = Buffer.from(stream);
  const positions: SessionRecordPosition[] = [];
  let offset = 0;
  for (const [index, text] of lines.entries()) {
    positions.push({
      line: index + 1,
      byteOffset: offset,
      byteLength: Buffer.byteLength(text),
      digest: digest(text),
      time: index === 0 ? "2026-09-20T10:00:00.000Z" : null,
    });
    offset += Buffer.byteLength(text);
  }
  const expected = lines.map((line, index) => ({
    text: line.endsWith("\n") ? line.slice(0, -1) : line,
    position: positions[index]!,
    parsed: index === 0 || index === 3 ? JSON.parse(line) as unknown : undefined,
  }));
  const collect = async (chunks: Iterable<string | Uint8Array>) => {
    const records: { text: string; position: SessionRecordPosition; parsed: unknown }[] = [];
    const sink = readNormalizedRecords((text, position, parsed) => {
      records.push({ text, position, parsed });
    });
    for (const chunk of chunks) sink.write(chunk);
    await sink.close();
    await sink.close();
    return records;
  };
  expect(await collect([stream])).toEqual(expected);
  // UTF-16 string writes can split surrogate pairs just as byte writes split UTF-8 codepoints.
  expect(await collect(stream.split("").flatMap((char) => [char, new Uint8Array()]))).toEqual(expected);
  expect(await collect(Array.from(bytes, (byte) => Uint8Array.of(byte)))).toEqual(expected);
  for (let cut = 0; cut <= bytes.length; cut += 1)
    expect(await collect([bytes.subarray(0, cut), bytes.subarray(cut)])).toEqual(expected);
});

test("namespaces keep live and archive slots separate while only the current immutable capture is covered", async () => {
  const { index } = await open();
  const live = candidate("same-selector");
  const archived = {
    ...live,
    namespace: "archive-host",
    seen: { ...live.seen, capture: "snapshot-one" },
  };
  const next = { ...archived, seen: { ...archived.seen, capture: "snapshot-two" } };
  const otherHost = { ...archived, namespace: "other-host" };
  await index.build(live, content(live, "liveword"));
  await index.build(archived, content(archived, "oldword"));
  await index.build(otherHost, content(otherHost, "otherword"));
  expect(index.holds(live)).toBe(true);
  expect(index.holds(archived)).toBe(true);
  expect(index.holds(next)).toBe(false);
  expect(await index.build(next, async (sink) => {
    sink.write('{"text":"unpublished"}\n');
    return { reading: reading(next.seen), after: archived.seen };
  })).toBe("changed");
  expect(index.searchRecords("oldword", [archived], 10).matches).toBe(1);
  expect(index.searchRecords("unpublished", [archived], 10).matches).toBe(0);
  expect(await index.build(next, content(next, "newword"))).toBe("indexed");
  expect(index.holds(archived)).toBe(false);
  expect(index.holds(next)).toBe(true);
  expect(index.holds(otherHost)).toBe(true);
  expect(index.searchRecords("oldword", [next, live, otherHost], 10).matches).toBe(0);
  expect(index.searchRecords("newword", [live, otherHost], 10).matches).toBe(0);
  expect(index.searchRecords("newword", [next], 10).hits[0]?.candidate).toEqual(next);
  expect(() => index.searchRecords("newword", [next, archived], 10)).toThrow(SessionIndexError);
  expect(index.search("liveword", [live], 10, 100).selection).toEqual([live.session]);
});

test("record search counts distinct records, bounds metadata, and exposes replayable locators without scores", async () => {
  const { index, dir } = await open();
  const entry = candidate("multiple-records");
  const excluded = candidate("excluded-record");
  const lines = [
    `${JSON.stringify({ text: `needle ${"padding ".repeat(3000)}`, another: "needle" })}\n`,
    "\n",
    '!needle café 🚀\n',
    '{"text":"unmatched"}\n',
  ];
  const stream = lines.join("");
  await index.build(entry, normalized(entry, stream));
  await index.build(excluded, content(excluded, "needle"));
  const result = index.searchRecords("needle", [entry, entry], 120);
  expect(result.matches).toBe(2);
  expect(result.hits.map((hit) => hit.position.line).sort((a, b) => a - b)).toEqual([1, 3]);
  for (const hit of result.hits) {
    const bytes = Buffer.from(stream).subarray(
      hit.position.byteOffset,
      hit.position.byteOffset + hit.position.byteLength,
    );
    expect(bytes.toString()).toBe(lines[hit.position.line - 1]);
    expect(hit).toEqual({
      candidate: entry,
      position: {
        line: hit.position.line,
        byteOffset: Buffer.byteLength(lines.slice(0, hit.position.line - 1).join("")),
        byteLength: bytes.length,
        digest: digest(bytes),
        time: null,
      },
      captureDigest: reading(entry.seen).captureDigest,
      sourceDigest: digest(stream),
    });
  }
  expect(index.searchRecords("needle", [entry], 1)).toEqual({
    hits: result.hits.slice(0, 1),
    matches: 2,
  });
  expect(index.search("needle", [entry], 120, 100)).toEqual({
    selection: [entry.session], matches: 1, overBound: 0,
  });
  const { index: reopened } = await open(dir);
  expect(reopened.searchRecords("needle", [entry], 120)).toEqual(result);
  expect(index.searchRecords("needle", [], 120)).toEqual({ hits: [], matches: 0 });
  for (const limit of [0, 121])
    expect(() => index.searchRecords("needle", [entry], limit)).toThrow(SessionIndexError);
});

test("record windows use recognized archived timestamps and exclude unknown times at either bound", async () => {
  const { index } = await open();
  const entry = candidate("times");
  const stream = [
    { text: "needle", timestamp: "2026-09-20T12:00:00+02:00" },
    { text: "needle", type: "session_meta", payload: { timestamp: "2026-09-20T11:00:00Z" } },
    { text: "needle", ts: Date.parse("2026-09-20T12:00:00Z") / 1000 },
    { text: "needle", timestamp: "unparseable", createdAt: "2026-09-20T10:00:00Z" },
    { text: "needle", payload: { timestamp: "2026-09-20T10:00:00Z" } },
    { text: "needle" },
    { text: "needle", timestamp: "2026-09-20T10:00:00" },
    { text: "needle", timestamp: "2026-02-30T10:00:00Z" },
    { text: "needle", ts: 253402300800 },
  ].map((record) => `${JSON.stringify(record)}\n`).join("");
  await index.build(entry, normalized(entry, stream));
  const result = index.searchRecords("needle", [entry], 120);
  expect(result.matches).toBe(9);
  expect(result.hits.filter((hit) => hit.position.time === null).map((hit) => hit.position.line).sort())
    .toEqual([4, 5, 6, 7, 8, 9]);
  const cases = [
    { window: { since: "2026-09-20T10:00:00Z" }, lines: [1, 2, 3] },
    { window: { until: "2026-09-20T11:00:00Z" }, lines: [1, 2] },
    { window: { since: "2026-09-20T12:00:00+02:00", until: "2026-09-20T10:00:00Z" }, lines: [1] },
  ];
  for (const { window, lines } of cases) {
    const filtered = index.searchRecords("needle", [entry], 120, window);
    expect(filtered.matches).toBe(lines.length);
    expect(filtered.hits.map((hit) => hit.position.line).sort()).toEqual(lines);
  }
  expect(index.search("needle", [entry], 120, 100).matches).toBe(1);
});
