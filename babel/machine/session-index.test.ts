import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { MAX_MATERIAL_BYTES } from "../contract.ts";
import { sessionRef } from "./adapters/index.ts";
import type { Observation, Reading, ReadingContext } from "./cache.ts";
import type { RecordSink } from "./output.ts";
import {
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

test("a failed replacement rolls back its tokens and releases the writer for another handle", async () => {
  const { index, dir } = await open();
  const old = candidate("rollback");
  const next = { ...old, seen: { ...old.seen, size: 20 } };
  await index.build(old, content(old, "durable"));
  const failure = index.build(next, async (sink) => {
    sink.write('{"text":"partial"}\n');
    throw new Error("credential-and-transcript-must-not-escape");
  });
  await expect(failure).rejects.toBeInstanceOf(SessionIndexError);
  await expect(failure).rejects.toMatchObject({
    kind: "unavailable",
    message: "Session content index is unavailable.",
  });
  const { index: other } = await open(dir);
  expect(other.search("durable", [old], 10, 100).selection).toEqual([old.session]);
  expect(other.search("partial", [old], 10, 100).matches).toBe(0);
  expect(await other.build(next, content(next, "complete"))).toBe("indexed");
  expect(other.search("complete", [next], 10, 100).selection).toEqual([next.session]);
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
    { ...original, seen: { ...original.seen, modifiedAt: 0 } },
  ];
  for (const changed of different) {
    expect(index.holds(changed)).toBe(false);
    expect(index.search("identityword", [changed], 10, 100).matches).toBe(0);
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
    expect(other.search("identityword", [original], 10, 100).matches).toBe(0);
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
  }
  expect(index.search('"***()"', [escaped, absent], 10, 100)).toEqual({
    selection: [],
    matches: 0,
    overBound: 0,
  });
});

test("invalid or oversized normalized records refuse atomically, even if the callback swallows a sink error", async () => {
  const { index } = await open();
  const old = candidate("malformed");
  const next = { ...old, seen: { ...old.seen, modifiedAt: 2000 } };
  await index.build(old, content(old, "retained"));
  await expect(
    index.build(next, async (sink) => {
      sink.write('{"text":"partial"}\n');
      try {
        sink.write('{"text":invalid}\n');
      } catch {
        /* A faulty producer must not publish partial coverage. */
      }
      return { reading: reading(next.seen), after: next.seen };
    }),
  ).rejects.toMatchObject({ kind: "unavailable" });
  await expect(
    index.build(next, async (sink) => {
      const chunk = "x".repeat(1024 * 1024);
      for (let i = 0; i < 65; i += 1) sink.write(chunk);
      return { reading: reading(next.seen), after: next.seen };
    }),
  ).rejects.toMatchObject({ kind: "unavailable" });
  expect(index.holds(next)).toBe(false);
  expect(index.search("retained", [old], 10, 100).selection).toEqual([old.session]);
  expect(index.search("partial", [old], 10, 100).matches).toBe(0);
});

test("search counts only distinct eligible identities, preserves original refs and skips byte-overbound hits", async () => {
  const { index } = await open();
  const a = candidate("a", 11);
  const b = candidate("b", 6);
  const c = candidate("c", 4);
  const excluded = candidate("excluded", 1);
  for (const entry of [excluded, c, b, a]) await index.build(entry, content(entry, "matching"));
  const result = index.search("matching", [c, b, a, b], 2, 10);
  expect(result).toEqual({ selection: [b.session, c.session], matches: 3, overBound: 1 });
  expect(result.selection[0]).toBe(b.session);
  expect(result.selection[1]).toBe(c.session);
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
