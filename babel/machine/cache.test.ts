import { expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
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
