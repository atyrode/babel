import { expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { mkdir, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { sessionRef } from "./adapters/index.ts";
import { readingCache, type Observation } from "./cache.ts";

test("a session slot reuses only its exact immutable capture, never another capture or the live source", async () => {
  const dir = mkdtempSync(join(tmpdir(), "babel-reading-captures-"));
  // No source exists here: cache reuse must only replay the supplied normalized stream.
  const session = sessionRef("omp", "capture-boundary", join(dir, "unopened.jsonl"));
  const context = { schema: 1, detectors: "test-detectors/1", mode: "redact" as const };
  const versions: { seen: Observation; text: string }[] = [
    { seen: { size: 17, modifiedAt: 1000, capture: "snapshot-a" }, text: "alpha" },
    { seen: { size: 17, modifiedAt: 1000, capture: "snapshot-b" }, text: "bravo" },
    { seen: { size: 17, modifiedAt: 1000 }, text: "local" },
  ];

  try {
    for (const version of versions) {
      const cache = readingCache(dir, context);
      expect(await cache.reuse(session, version.seen)).toBeNull();
      const observed = { ...version.seen };
      const opening = cache.keep(session, observed);
      // Even a caller retaining a mutable object cannot retag an in-flight entry.
      observed.capture = "changed-while-opening";
      const kept = await opening;
      if (kept === null) throw new Error("could not keep fixture reading");
      const record = `${JSON.stringify({ text: version.text })}\n`;
      const digest = `sha256:${new Bun.CryptoHasher("sha256").update(record).digest("hex")}`;
      kept.sink.write(record);
      await kept.sink.close();
      observed.capture = "changed-before-commit";
      await kept.commit({
        captureDigest: digest,
        sourceDigest: digest,
        bytes: version.seen.size,
        records: 1,
        report: null,
      });

      // Reopen from disk: the slot holds only the latest observation, not earlier captures.
      const reopened = readingCache(dir, context);
      for (const candidate of versions) {
        const requested = { ...candidate.seen };
        const reusing = reopened.reuse(session, requested);
        requested.capture = "changed-while-reusing";
        const reused = await reusing;
        if (candidate !== version) {
          expect(reused).toBeNull();
          continue;
        }
        if (reused === null) throw new Error("exact capture was not reusable");
        const chunks: Uint8Array[] = [];
        const replayed = await reopened.replay(reused, {
          write(chunk) {
            chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : chunk);
          },
          close: async () => {},
        });
        expect(Buffer.concat(chunks).toString("utf8")).toBe(record);
        expect(replayed).toBe(digest);
        expect(reused.sourceDigest).toBe(replayed);
      }
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("reading metadata accepts only canonical SHA-256 digests", async () => {
  const dir = mkdtempSync(join(tmpdir(), "babel-reading-digests-"));
  const session = sessionRef("omp", "digest-boundary", "/synthetic/session.jsonl");
  const seen = { size: 7, modifiedAt: 1000, capture: "snapshot" };
  const cache = readingCache(dir, { schema: 1, detectors: "test/1", mode: "redact" });
  const digest = `sha256:${"a".repeat(64)}`;
  try {
    const kept = await cache.keep(session, seen);
    if (kept === null) throw new Error("could not keep fixture reading");
    kept.sink.write("record\n");
    await kept.sink.close();
    await kept.commit({
      captureDigest: digest,
      sourceDigest: digest,
      bytes: seen.size,
      records: 1,
      report: null,
    });
    const file = (await readdir(dir)).find((path) => path.endsWith(".json"));
    if (file === undefined) throw new Error("missing reading metadata");
    const path = join(dir, file);
    const document = await Bun.file(path).json();
    for (const field of ["captureDigest", "sourceDigest"]) {
      for (const invalid of ["arbitrary", digest.slice(0, -1), digest.toUpperCase(), `${digest}\n`]) {
        await Bun.write(path, JSON.stringify({ ...document, [field]: invalid }));
        expect(await cache.reuse(session, seen)).toBeNull();
      }
    }
    await Bun.write(path, JSON.stringify(document));
    expect((await cache.reuse(session, seen))?.sourceDigest).toBe(digest);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("failed metadata renames clean unique staging files without removing the destination", async () => {
  const dir = mkdtempSync(join(tmpdir(), "babel-reading-cleanup-"));
  const session = sessionRef("omp", "cleanup-boundary", "/synthetic/session.jsonl");
  const seen = { size: 7, modifiedAt: 1000, capture: "snapshot" };
  const cache = readingCache(dir, { schema: 1, detectors: "test/1", mode: "redact" });
  const slot = new Bun.CryptoHasher("sha256").update(session.selector).digest("hex");
  const destination = join(dir, `${slot}.json`);
  const digest = `sha256:${new Bun.CryptoHasher("sha256").update("record\n").digest("hex")}`;
  try {
    await mkdir(destination);
    await Bun.write(join(destination, "unrelated"), "preserve me");
    for (let attempt = 0; attempt < 3; attempt++) {
      const kept = await cache.keep(session, seen);
      if (kept === null) throw new Error("could not keep fixture reading");
      kept.sink.write("record\n");
      await kept.sink.close();
      await kept.commit({
        captureDigest: digest,
        sourceDigest: digest,
        bytes: seen.size,
        records: 1,
        report: null,
      });
      expect(await cache.reuse(session, seen)).toBeNull();
      expect((await readdir(dir)).sort()).toEqual([`${slot}.json`, `${slot}.records`]);
      expect(await Bun.file(join(destination, "unrelated")).text()).toBe("preserve me");
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
