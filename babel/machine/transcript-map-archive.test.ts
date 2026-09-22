import { expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import {
  RecallRequestSchema,
  TranscriptMapSegmentationSchema,
  RECALL_REQUEST_TTL_MS,
  type RecallPolicy,
  type TranscriptMapNativeRequest,
  TRANSCRIPT_MAP_RESULT_PROJECTION,
  TranscriptMapNativeReplySchema,
} from "../contract.ts";
import { createRecallArchive } from "./recall-archive.ts";
import { BABEL_TAG, type Repo, type Snapshot } from "./restic.ts";
import { mapPrepare, mapCatalog } from "./transcript-map-jobs.ts";
import { directorySink, materialSink } from "./output.ts";
import { projectJson, compileJsonProjection } from "@manifold/protocol";
import { run } from "./main.ts";

const path = "/synthetic/.omp/agent/sessions/project/2026-09-01T00-00-00-000Z_fixture.jsonl";
const host = "synthetic-map-host";
const policy: RecallPolicy = {
  version: 1,
  mappingClassId: "private",
  classes: [
    { id: "public", label: "Public", ceiling: 0 },
    { id: "private", label: "Private", ceiling: 3 },
  ],
  subjects: [{ name: "synthetic", host, sensitivity: 0 }],
};
const header = '{"type":"session","version":3,"timestamp":"2026-09-01T00:00:00.000Z"}\n';
const body = (text: string) =>
  header +
  JSON.stringify({
    type: "message",
    message: { role: "user", content: [{ type: "text", text }] },
  }) +
  "\n";
async function fixture(source?: string) {
  const directory = await mkdtemp(join(tmpdir(), "babel-map-test-"));
  const clock = { now: Date.parse("2026-09-21T00:00:00.000Z") };
  const captures = [source ?? body("historicalneedle 🙂 café"), body("newestneedle")];
  let retained = 2;
  let dumps = 0;
  const forbidden = async (): Promise<never> => {
    throw new Error("Unexpected synthetic archive operation.");
  };
  const snapshots = (): Snapshot[] =>
    captures.slice(0, retained).map((_text, index) => ({
      id: (index ? "b" : "a").repeat(64),
      shortId: (index ? "b" : "a").repeat(8),
      time: `2026-09-0${index + 1}T00:00:00.000Z`,
      parentId: null,
      host,
      paths: ["/synthetic/.omp/agent/sessions"],
      tags: [BABEL_TAG],
    }));
  const indexOf = (snapshot: string) => (snapshot.startsWith("a") ? 0 : 1);
  const repo: Repo = {
    repository: "synthetic-map-archive",
    exists: forbidden,
    init: forbidden,
    backup: forbidden,
    check: forbidden,
    restore: forbidden,
    dump: forbidden,
    ls: forbidden,
    snapshots: async () => snapshots(),
    lsTo: async (snapshot, sink) => {
      await sink({
        path,
        type: "file",
        size: Buffer.byteLength(captures[indexOf(snapshot)]!),
        modifiedAt: "2026-09-01T00:00:00.000Z",
      });
    },
    dumpTo: async (snapshot, _path, sink) => {
      dumps++;
      const bytes = Buffer.from(captures[indexOf(snapshot)]!);
      await sink(bytes);
      return { bytes: bytes.length };
    },
  };
  const archive = await createRecallArchive({
    repo,
    policy,
    cacheDir: directory,
    temporaryDir: directory,
    now: () => clock.now,
  });
  return {
    archive,
    repo,
    directory,
    clock,
    removeNewest() {
      retained = 1;
    },
    dumps: () => dumps,
    async close() {
      await archive.close();
      await rm(directory, { recursive: true, force: true });
    },
  };
}

test("mapping enumerates retained history while raw Recall stays newest-only", async () => {
  const f = await fixture();
  try {
    const catalog = await f.archive.executeMap("public", {
      kind: "map-inventory",
      maxCaptures: 64,
    });
    expect(catalog.entries).toHaveLength(2);
    const old = catalog.entries.find((entry) => entry.capture.snapshot.startsWith("a"))!.capture;
    const plan = await f.archive.executeMap(
      "private",
      {
        kind: "map-plan",
        capture: old,
        segmentation: TranscriptMapSegmentationSchema.parse({}),
        offset: 0,
        maxNodes: 128,
      },
      true,
    );
    expect(plan.refusal).toBeNull();
    const source = plan.plan!.header.source;
    const span = plan.plan!.nodes[0]!.span;
    const read = await f.archive.executeMap("public", {
      kind: "map-span",
      source,
      span,
      maxBytes: 8192,
    });
    expect(read.span?.excerpt.text).toContain("historicalneedle");
    expect(read.span?.source.snapshot).toBe(old.snapshot);
    const raw = await f.archive.execute(
      "public",
      RecallRequestSchema.parse({ kind: "search", query: "historicalneedle" }),
    );
    expect(raw.hits).toEqual([]);
    const newest = await f.archive.execute(
      "public",
      RecallRequestSchema.parse({ kind: "search", query: "newestneedle" }),
    );
    expect(newest.hits[0]?.locator.snapshot).toBe("b".repeat(64));
    const warmed = await f.archive.executeMap("public", {
      kind: "map-span",
      source,
      span,
      maxBytes: 8192,
    });
    expect(warmed.span?.excerpt.text).toContain("historicalneedle");
    const before = f.dumps();
    const forged = await f.archive.executeMap("public", {
      kind: "map-span",
      source: { ...source, sourceDigest: `sha256:${"c".repeat(64)}` },
      span,
      maxBytes: 8192,
    });
    expect(forged.refusal).toBe("locator-mismatch");
    const repaired = await f.archive.executeMap("public", {
      kind: "map-span",
      source,
      span,
      maxBytes: 8192,
    });
    expect(repaired.span?.excerpt.text).toContain("historicalneedle");
    expect(f.dumps()).toBe(before);
    expect(repaired.cost.replayedBytes).toBe(span.byteLength);
  } finally {
    await f.close();
  }
});

test("historical capture switches replace one normalized session reading without losing exact source", async () => {
  const f = await fixture();
  try {
    const inventory = await f.archive.executeMap("private", {
      kind: "map-inventory",
      maxCaptures: 64,
    });
    const oldest = inventory.entries.find(({ capture }) =>
      capture.snapshot.startsWith("a"),
    )!.capture;
    const newest = inventory.entries.find(({ capture }) =>
      capture.snapshot.startsWith("b"),
    )!.capture;
    const segmentation = TranscriptMapSegmentationSchema.parse({});
    let largestReading = 0;
    for (const [capture, text] of [
      [oldest, "historicalneedle"],
      [newest, "newestneedle"],
      [oldest, "historicalneedle"],
    ] as const) {
      const planned = await f.archive.executeMap(
        "private",
        { kind: "map-plan", capture, segmentation, offset: 0, maxNodes: 128 },
        true,
      );
      if (!planned.plan || planned.refusal) throw new Error("Historical fixture plan unavailable.");
      const source = planned.plan.header.source;
      const span = planned.plan.nodes[0]!.span;
      const served = await f.archive.executeMap("public", {
        kind: "map-span",
        source,
        span,
        maxBytes: 8192,
      });
      expect(served.span?.source.snapshot).toBe(capture.snapshot);
      expect(served.span?.excerpt.text).toContain(text);
      largestReading = Math.max(largestReading, source.bytes);
      let retainedBytes = 0;
      for await (const file of new Bun.Glob("**/*.records").scan({
        cwd: f.directory,
        absolute: true,
      }))
        retainedBytes += Bun.file(file).size;
      expect(retainedBytes).toBeLessThanOrEqual(largestReading);
    }
  } finally {
    await f.close();
  }
});

test("native refusals without a context survive the SDK primitive-leaf projection", async () => {
  const f = await fixture();
  try {
    const result = await f.archive.executeMap("unconfigured", { kind: "map-context" });
    const projection = TRANSCRIPT_MAP_RESULT_PROJECTION;
    const delivered = TranscriptMapNativeReplySchema.parse(
      projectJson(
        { requestId: crypto.randomUUID(), state: "complete", result },
        compileJsonProjection(projection.fields, projection.textFields),
        projection.maxArrayItems,
      ),
    );
    expect(delivered.result?.refusal).toBe("disclosure");
    expect(delivered.result?.context).toBeUndefined();
    expect(delivered.result?.entries).toEqual([]);
  } finally {
    await f.close();
  }
});

test("native coordinates reject forged records and independently detect cache bytes even with a matching caller digest", async () => {
  const f = await fixture();
  try {
    const inventory = await f.archive.executeMap("private", {
      kind: "map-inventory",
      maxCaptures: 64,
    });
    const planned = await f.archive.executeMap(
      "private",
      {
        kind: "map-plan",
        capture: inventory.entries[0]!.capture,
        segmentation: TranscriptMapSegmentationSchema.parse({}),
        offset: 0,
        maxNodes: 128,
      },
      true,
    );
    const source = planned.plan!.header.source;
    const span = planned.plan!.nodes[0]!.span;
    const bad = await f.archive.executeMap("public", {
      kind: "map-span",
      source,
      span: {
        ...span,
        firstRecord: span.firstRecord + 1,
        anchor: { ...span.anchor, line: span.firstRecord + 1 },
      },
      maxBytes: 8192,
    });
    expect(bad.refusal).toBe("locator-mismatch");
    const files = await Array.fromAsync(
      new Bun.Glob("**/*.records").scan({ cwd: f.directory, absolute: true }),
    );
    const cache = files[0]!;
    const original = await Bun.file(cache).text();
    const corrupted = original.replace("needle", "xxxxxx");
    expect(corrupted).not.toBe(original);
    await Bun.write(cache, corrupted);
    const forgedDigest = `sha256:${createHash("sha256").update(corrupted).digest("hex")}`;
    const refused = await f.archive.executeMap("public", {
      kind: "map-span",
      source,
      span: { ...span, digest: forgedDigest },
      maxBytes: 8192,
    });
    expect(refused.refusal).toBe("capture-changed");
    expect(await Bun.file(cache).text()).toBe(corrupted);
  } finally {
    await f.close();
  }
});

test("inventory cursors bind context, attestations use maximum classification, and preview offsets/expiry are strict", async () => {
  const f = await fixture();
  try {
    const first = await f.archive.executeMap("public", { kind: "map-inventory", maxCaptures: 1 });
    expect(first.nextCursor).not.toBeNull();
    const privateContext = await f.archive.executeMap("private", { kind: "map-context" });
    expect(privateContext.context?.digest).toBe(first.context?.digest);
    const all = await f.archive.executeMap("private", { kind: "map-inventory", maxCaptures: 64 });
    const capture = all.entries.find((entry) => entry.capture.snapshot.startsWith("a"))!.capture;
    const plan = await f.archive.executeMap(
      "private",
      {
        kind: "map-plan",
        capture,
        segmentation: TranscriptMapSegmentationSchema.parse({}),
        offset: 0,
        maxNodes: 128,
      },
      true,
    );
    const denied = await f.archive.executeMap("private", {
      kind: "map-preview",
      source: plan.plan!.header.source,
      span: plan.plan!.nodes[0]!.span,
    });
    expect(denied.refusal).toBe("disclosure");
    const preview = await f.archive.executeMap(
      "private",
      { kind: "map-preview", source: plan.plan!.header.source, span: plan.plan!.nodes[0]!.span },
      true,
    );
    const request = {
      kind: "map-page" as const,
      previewId: preview.preview!.previewId,
      offset: 0,
      maxBytes: 11,
    };
    const page = await f.archive.executeMap("private", request, true);
    expect((await f.archive.executeMap("private", request, true)).page).toEqual(page.page);
    expect((await f.archive.executeMap("private", { ...request, offset: 1 }, true)).refusal).toBe(
      "invalid-offset",
    );
    let current = page.page!;
    let restored = current.text;
    while (!current.complete) {
      const response = await f.archive.executeMap(
        "private",
        { ...request, offset: current.nextOffset },
        true,
      );
      expect(response.refusal).toBeNull();
      current = response.page!;
      restored += current.text;
    }
    expect(restored).toContain("historicalneedle 🙂 café");
    expect(`sha256:${createHash("sha256").update(restored).digest("hex")}`).toBe(
      plan.plan!.nodes[0]!.span.digest,
    );
    expect(
      (await f.archive.executeMap("private", { ...request, offset: current.offset }, true)).page,
    ).toEqual(current);
    f.clock.now += RECALL_REQUEST_TTL_MS + 1;
    expect((await f.archive.executeMap("private", request, true)).refusal).toBe("preview-expired");
    f.removeNewest();
    expect(
      (
        await f.archive.executeMap("public", {
          kind: "map-inventory",
          maxCaptures: 1,
          cursor: first.nextCursor!,
        })
      ).refusal,
    ).toBe("stale-context");
    const restricted = await createRecallArchive({
      repo: f.repo,
      cacheDir: f.directory,
      policy: {
        ...policy,
        subjects: [...policy.subjects, { name: "restricted", host, sensitivity: 3 }],
      },
    });
    try {
      const deniedCatalog = await restricted.executeMap("public", {
        kind: "map-inventory",
        maxCaptures: 64,
      });
      expect(deniedCatalog.entries).toEqual([]);
      expect(deniedCatalog.context?.digest).not.toBe(first.context?.digest);
      expect(
        (await restricted.executeMap("public", { kind: "map-authorize", captures: [capture] }))
          .accesses,
      ).toEqual([]);
    } finally {
      await restricted.close();
    }
  } finally {
    await f.close();
  }
});

test("finite material sealing uses exact leaves but only ordered summaries/gaps for parents", async () => {
  const source =
    header +
    Array.from(
      { length: 8 },
      (_, i) =>
        JSON.stringify({
          type: "message",
          message: {
            role: "user",
            content: [{ type: "text", text: `source-only-${i} ${"x".repeat(400)}` }],
          },
        }) + "\n",
    ).join("");
  const f = await fixture(source);
  try {
    const client = async (request: TranscriptMapNativeRequest) =>
      f.archive.executeMap("private", request, true);
    const inventory = await client({ kind: "map-inventory", maxCaptures: 64 });
    const capture = inventory.entries.find((entry) =>
      entry.capture.snapshot.startsWith("a"),
    )!.capture;
    const segmentation = TranscriptMapSegmentationSchema.parse({
      leafBytes: 1024,
      directBytes: 0,
      fanout: 2,
    });
    const receipt = await mapCatalog(
      {
        runId: "catalog-synthetic",
        sourceMachineId: "source-synthetic",
        executorMachineId: "machine-synthetic",
        request: { kind: "map-plan", capture, segmentation, offset: 0, maxNodes: 1 },
      },
      directorySink(join(f.directory, "catalog")),
      client,
    );
    if (receipt.mapping?.kind !== "catalog" || !receipt.mapping.plan)
      throw new Error("Missing finite plan.");
    const plan = receipt.mapping.plan;
    expect(plan.nextOffset).toBeNull();
    expect(plan.nodes.length).toBe(plan.header.nodeCount);
    const parent = plan.nodes.find((node) => node.children.length)!;
    const input = {
      runId: "material-synthetic",
      sourceMachineId: "source-synthetic",
      executorMachineId: "machine-synthetic",
      source: plan.header.source,
      nodeId: parent.id,
      segmentation,
      expectedPolicyDigest: inventory.context!.policyDigest,
      mode: "generate" as const,
      children: parent.children.map((_id, index) =>
        index === 0
          ? {
              summaryId: `tmsum_${"c".repeat(64)}`,
              text: "Previously summarized navigation: café 日本語.",
              gap: null,
            }
          : { summaryId: null, text: null, gap: "unmapped" as const },
      ),
    };
    const material = join(f.directory, "parent-material");
    const prepared = await mapPrepare(
      input,
      directorySink(join(f.directory, "parent-out")),
      materialSink(material),
      client,
    );
    const document = await Bun.file(join(material, "transcript-map.json")).json();
    expect(document.text).toBeNull();
    expect(document.children.map((child: { nodeId: string }) => child.nodeId)).toEqual(
      parent.children,
    );
    expect(JSON.stringify(document)).not.toContain("source-only");
    expect(prepared.counts.suppliedBytes).toBe(0);
    if (prepared.mapping?.kind !== "material") throw new Error("Missing material receipt.");
    expect(prepared.mapping.inputDigest).toBe(
      `sha256:${createHash("sha256")
        .update(await Bun.file(join(material, "transcript-map.json")).text())
        .digest("hex")}`,
    );
    expect(prepared.mapping.materialBytes).toBe(
      (await Bun.file(join(material, "transcript-map.json")).arrayBuffer()).byteLength,
    );
    expect(prepared.mapping.materialBytes).toBeGreaterThan(JSON.stringify(document).length);
    await expect(
      mapPrepare(
        { ...input, expectedPolicyDigest: `sha256:${"f".repeat(64)}` },
        directorySink(join(f.directory, "stale-out")),
        materialSink(join(f.directory, "stale-material")),
        client,
      ),
    ).rejects.toThrow();
    await expect(
      mapPrepare(
        {
          ...input,
          children: input.children.map((child, index) =>
            index === 0 ? { ...child, text: "ghp_SYNTHETICabcdefghijklmnopqrstuv" } : child,
          ),
        },
        directorySink(join(f.directory, "secret-out")),
        materialSink(join(f.directory, "secret-material")),
        client,
      ),
    ).rejects.toThrow();
    const leaf = plan.nodes.find((node) => node.children.length === 0 && node.gap === null)!;
    const leafMaterial = join(f.directory, "leaf-material");
    const leafReceipt = await mapPrepare(
      { ...input, nodeId: leaf.id, children: [] },
      directorySink(join(f.directory, "leaf-out")),
      materialSink(leafMaterial),
      client,
    );
    const leafDocument = await Bun.file(join(leafMaterial, "transcript-map.json")).json();
    expect(`sha256:${createHash("sha256").update(leafDocument.text).digest("hex")}`).toBe(
      leaf.span.digest,
    );
    expect(leafReceipt.counts.suppliedBytes).toBe(leaf.span.byteLength);
    if (leafReceipt.mapping?.kind !== "material") throw new Error("Missing material receipt.");
    expect(leafReceipt.mapping.materialBytes).toBe(
      (await Bun.file(join(leafMaterial, "transcript-map.json")).arrayBuffer()).byteLength,
    );
    expect(leafReceipt.mapping.materialBytes).toBeGreaterThan(leaf.span.byteLength);
  } finally {
    await f.close();
  }
});

test("catalog cadences complete without a filesystem lease or Recall binding", async () => {
  const directory = await mkdtemp(join(tmpdir(), "babel-map-wake-"));
  try {
    const outputDir = join(directory, "unbound-output");
    await Bun.write(outputDir, "not a native output lease");
    const inputPath = join(directory, "wake.json");
    await Bun.write(
      inputPath,
      JSON.stringify({
        kind: "catalog-wake",
        sourceMachineId: "source-synthetic",
        executorMachineId: "synthetic",
      }),
    );
    const first = await run({
      operation: "mapCatalog",
      inputPath,
      outputDir,
      materialDir: "",
    });
    const second = await run({
      operation: "mapCatalog",
      inputPath,
      outputDir,
      materialDir: "",
    });
    if (!first || !second) throw new Error("A finite cadence must return its native receipt.");
    expect(first).toMatchObject({
      kind: "mapCatalog",
      closure: "completed",
      counts: { wakes: 1 },
    });
    expect(first.mapping).toBeUndefined();
    expect(second.mapping).toBeUndefined();
    expect(second.runId).not.toBe(first.runId);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
