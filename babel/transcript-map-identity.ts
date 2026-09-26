import { createHash } from "node:crypto";
import {
  TranscriptMapSourceSchema,
  TranscriptMapSegmentationSchema,
  TranscriptMapSpanSchema,
  TranscriptMapNodeSchema,
  type TranscriptMapCapture,
  type TranscriptMapSource,
  type TranscriptMapSegmentation,
  type TranscriptMapSpan,
  type TranscriptMapNode,
} from "./contract.ts";

const hash = (value: unknown): string =>
  createHash("sha256").update(JSON.stringify(value)).digest("hex");
export function transcriptMapCaptureId(
  capture: Omit<TranscriptMapCapture, "id"> | TranscriptMapCapture,
): string {
  return `tmcap_${hash([capture.host, capture.harness, capture.session, capture.snapshot, capture.path])}`;
}
export function transcriptMapPlanId(
  source: TranscriptMapSource,
  segmentation: TranscriptMapSegmentation,
): string {
  return `tmplan_${hash([TranscriptMapSourceSchema.parse(source), TranscriptMapSegmentationSchema.parse(segmentation)])}`;
}
export function transcriptMapNodeId(
  planId: string,
  level: number,
  ordinal: number,
  span: TranscriptMapSpan,
  children: readonly string[],
  gap: TranscriptMapNode["gap"],
): string {
  return `tmnode_${hash([planId, level, ordinal, TranscriptMapSpanSchema.parse(span), children, gap])}`;
}
export function transcriptMapManifestDigest(nodes: readonly TranscriptMapNode[]): string {
  const digest = createHash("sha256").update("[");
  nodes.forEach((node, index) => {
    if (index) digest.update(",");
    digest.update(JSON.stringify(TranscriptMapNodeSchema.parse(node)));
  });
  return `sha256:${digest.update("]").digest("hex")}`;
}
