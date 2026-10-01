import { createHash } from "node:crypto";
import {
  TranscriptMapSegmentationSchema,
  type TranscriptMapSegmentation,
  type TranscriptMapSpan,
  type TranscriptMapNode,
  type SessionRecordPosition,
  type NavigationMapSource,
  type NavigationMapPlan,
} from "./contract.ts";
import {
  transcriptMapPlanId,
  transcriptMapNodeId,
  transcriptMapManifestDigest,
} from "./transcript-map-identity.ts";

/** The one deterministic segmenter for archive transcripts and bounded hub-record snapshots. */
export async function buildNavigationMap<S extends NavigationMapSource>(options: {
  source(bytes: number, records: number): S;
  sourceDigest: string;
  segmentation: TranscriptMapSegmentation;
  replay(
    visit: (text: string, position: SessionRecordPosition, boundary: boolean) => void,
  ): Promise<void>;
  rangeDigest(offset: number, bytes: number): Promise<string>;
  record?(position: SessionRecordPosition): void;
}): Promise<{
  header: Omit<NavigationMapPlan, "source"> & { source: S };
  nodes: TranscriptMapNode[];
}> {
  const segmentation = TranscriptMapSegmentationSchema.parse(options.segmentation);
  type Leaf = { span: TranscriptMapSpan; gap: TranscriptMapNode["gap"] };
  const leaves: Leaf[] = [];
  const capacity = segmentation.fanout ** (segmentation.maxDepth - 1);
  let first: SessionRecordPosition | undefined;
  let last: SessionRecordPosition | undefined;
  let bytes = 0;
  let records = 0;
  let length = 0;
  let overflow = false;
  let leafHash = createHash("sha256");
  const flush = (gap: TranscriptMapNode["gap"] = null): void => {
    if (!first || !last) return;
    leaves.push({
      span: {
        firstRecord: first.line,
        lastRecord: last.line,
        byteOffset: first.byteOffset,
        byteLength: length,
        digest: `sha256:${leafHash.digest("hex")}`,
        anchor: first,
      },
      gap,
    });
    first = undefined;
    last = undefined;
    length = 0;
    leafHash = createHash("sha256");
  };
  await options.replay((text, position, boundary) => {
    options.record?.(position);
    bytes += position.byteLength;
    records++;
    if (
      !overflow &&
      first &&
      (length + position.byteLength > segmentation.leafBytes ||
        (boundary && length >= Math.max(segmentation.directBytes, segmentation.leafBytes / 2)))
    ) {
      if (leaves.length + 1 >= capacity) overflow = true;
      else flush();
    }
    first ??= position;
    last = position;
    length += position.byteLength;
    leafHash.update(text).update("\n");
    if (!overflow && position.byteLength > segmentation.leafBytes) {
      if (leaves.length + 1 >= capacity) overflow = true;
      else flush("record-too-large");
    }
  });
  flush(
    overflow
      ? first?.line === last?.line && length > segmentation.leafBytes
        ? "record-too-large"
        : "depth-bound"
      : null,
  );
  const source = options.source(bytes, records);
  const planId = transcriptMapPlanId(source, segmentation);
  const nodes: TranscriptMapNode[] = [];
  let gapBytes = 0;
  let layer = leaves.map(({ span, gap }, ordinal): TranscriptMapNode => {
    if (gap) gapBytes += span.byteLength;
    return {
      id: transcriptMapNodeId(planId, 0, ordinal, span, [], gap),
      planId,
      parentId: null,
      level: 0,
      ordinal,
      span,
      children: [],
      gap,
    };
  });
  for (const node of layer) nodes.push(node);
  for (let level = 1; layer.length > 1; level++) {
    const parents: TranscriptMapNode[] = [];
    for (let at = 0; at < layer.length; at += segmentation.fanout) {
      const group = layer.slice(at, at + segmentation.fanout);
      const start = group[0]!.span;
      const end = group[group.length - 1]!.span;
      const byteLength = end.byteOffset + end.byteLength - start.byteOffset;
      const span: TranscriptMapSpan = {
        ...start,
        lastRecord: end.lastRecord,
        byteLength,
        digest: await options.rangeDigest(start.byteOffset, byteLength),
      };
      const children = group.map((child) => child.id);
      const ordinal = parents.length;
      const id = transcriptMapNodeId(planId, level, ordinal, span, children, null);
      for (const child of group) child.parentId = id;
      parents.push({ id, planId, parentId: null, level, ordinal, span, children, gap: null });
    }
    for (const node of parents) nodes.push(node);
    layer = parents;
  }
  if (
    (layer[0]?.span.digest ?? `sha256:${createHash("sha256").digest("hex")}`) !==
    options.sourceDigest
  )
    throw new Error("Canonical source digest mismatch.");
  return {
    header: {
      id: planId,
      source,
      segmentation,
      rootId: layer[0]?.id ?? null,
      nodeCount: nodes.length,
      digest: transcriptMapManifestDigest(nodes),
      direct: !("kind" in source) && bytes <= segmentation.directBytes,
      gapBytes,
    },
    nodes,
  };
}
