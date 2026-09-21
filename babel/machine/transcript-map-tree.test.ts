import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { TranscriptMapSegmentationSchema, type TranscriptMapSegmentation } from "../contract.ts";
import { transcriptMapCaptureId, transcriptMapManifestDigest } from "../transcript-map-identity.ts";
import { buildTranscriptMap } from "./transcript-map-tree.ts";

const sha = (bytes: string | Uint8Array) => `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
async function tree(text: string, options: Partial<TranscriptMapSegmentation> = {}) {
  const identity = { host: "synthetic", harness: "omp" as const, session: "omp:synthetic", snapshot: "a".repeat(64), path: "/synthetic/session.jsonl", capturedAt: "2026-09-01T00:00:00.000Z" };
  const bytes = Buffer.from(text);
  return buildTranscriptMap({ capture: { id: transcriptMapCaptureId(identity), ...identity }, captureDigest: sha(text), sourceDigest: sha(text), segmentation: TranscriptMapSegmentationSchema.parse(options), async replay(sink) {
    // Force framing across UTF-8 and record boundaries.
    for (let offset = 0; offset < bytes.length; offset += 7) sink.write(bytes.subarray(offset, offset + 7));
    await sink.close();
  }, rangeDigest: async (offset, length) => sha(bytes.subarray(offset, offset + length)) });
}
const record = (text: string) => JSON.stringify({ role: "user", text }) + "\n";

test("every leaf and parent digest names its exact contiguous canonical byte span", async () => {
  const text = Array.from({ length: 13 }, (_, i) => record(`${i} ${"λ🙂".repeat(90)}`)).join("");
  const built = await tree(text, { leafBytes: 1024, directBytes: 256, fanout: 3 });
  const bytes = Buffer.from(text);
  const leaves = built.nodes.filter((node) => !node.children.length);
  expect(leaves.map((node) => node.span.byteLength).reduce((a, b) => a + b, 0)).toBe(bytes.length);
  let offset = 0;
  for (const leaf of leaves) { expect(leaf.span.byteOffset).toBe(offset); offset += leaf.span.byteLength; }
  for (const node of built.nodes) {
    expect(node.span.digest).toBe(sha(bytes.subarray(node.span.byteOffset, node.span.byteOffset + node.span.byteLength)));
    expect(node.span.anchor.digest).toBe(sha(bytes.subarray(node.span.byteOffset, node.span.byteOffset + node.span.anchor.byteLength)));
    expect(node.span.anchor.line).toBe(node.span.firstRecord);
  }
  expect(built.header.digest).toBe(transcriptMapManifestDigest(built.nodes));
  expect(built.nodes.at(-1)?.span.digest).toBe(built.header.source.sourceDigest);
});

test("empty/direct captures and oversized records never create partial record summaries", async () => {
  const empty = await tree("");
  expect(empty.header.rootId).toBeNull();
  expect(empty.nodes).toEqual([]);
  const direct = await tree(record("a".repeat(600)) + record("b".repeat(600)), { leafBytes: 2048, directBytes: 2048 });
  expect(direct.header.direct).toBe(true);
  expect(direct.nodes).toHaveLength(1);
  const oversized = await tree(record("a".repeat(1400)) + record("short"), { leafBytes: 1024, directBytes: 256 });
  expect(oversized.nodes[0]?.gap).toBe("record-too-large");
  expect(oversized.nodes[0]?.span.firstRecord).toBe(oversized.nodes[0]?.span.lastRecord);
  expect(oversized.header.gapBytes).toBe(Buffer.byteLength(record("a".repeat(1400))));
});

test("depth overflow is one explicit contiguous gap and source growth preserves completed prefix spans", async () => {
  const rows = Array.from({ length: 9 }, (_, i) => record(`${i}:${"x".repeat(700)}`));
  const capped = await tree(rows.join(""), { leafBytes: 1024, directBytes: 0, fanout: 2, maxDepth: 2 });
  expect(Math.max(...capped.nodes.map((node) => node.level))).toBe(1);
  expect(capped.nodes.filter((node) => node.gap === "depth-bound")).toHaveLength(1);
  expect(capped.nodes[1]?.span.lastRecord).toBe(9);
  expect(capped.nodes.at(-1)?.span.byteLength).toBe(Buffer.byteLength(rows.join("")));
  const before = await tree(rows.slice(0, 4).join(""), { leafBytes: 1024, directBytes: 0 });
  const after = await tree(rows.join(""), { leafBytes: 1024, directBytes: 0 });
  expect(after.nodes.slice(0, 3).map((node) => node.span)).toEqual(before.nodes.slice(0, 3).map((node) => node.span));
  expect(after.header.id).not.toBe(before.header.id);
});
