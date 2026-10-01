import {
  SESSION_RECORD_COORDINATES,
  TranscriptMapCaptureSchema,
  TranscriptMapSourceSchema,
  type TranscriptMapCapture,
  type TranscriptMapSegmentation,
  type TranscriptMapNode,
  type TranscriptMapPlan,
  type SessionRecordPosition,
} from "../contract.ts";
import { readNormalizedRecords } from "./session-index.ts";
import type { RecordSink } from "./output.ts";

import { transcriptMapCaptureId } from "../transcript-map-identity.ts";
import { buildNavigationMap } from "../transcript-map-tree.ts";

export interface TranscriptMapTree {
  header: TranscriptMapPlan;
  nodes: TranscriptMapNode[];
}

/** One bounded record at a time. The caller must independently verify the whole replay digest.
 * Ranges are read at most once per parent level, never once per leaf request. */
export async function buildTranscriptMap(options: {
  capture: TranscriptMapCapture;
  captureDigest: string;
  sourceDigest: string;
  segmentation: TranscriptMapSegmentation;
  replay(sink: RecordSink): Promise<void>;
  rangeDigest(offset: number, bytes: number): Promise<string>;
  record?(position: SessionRecordPosition): void;
}): Promise<TranscriptMapTree> {
  const capture = TranscriptMapCaptureSchema.parse(options.capture);
  if (transcriptMapCaptureId(capture) !== capture.id) throw new Error("Capture identity mismatch.");
  return buildNavigationMap({
    ...options,
    replay: async (visit) => {
      await options.replay(
        readNormalizedRecords((text, position, parsed) => {
          const fields =
            parsed !== null && typeof parsed === "object"
              ? (parsed as Record<string, unknown>)
              : {};
          const message =
            fields.message !== null && typeof fields.message === "object"
              ? (fields.message as Record<string, unknown>)
              : {};
          visit(
            text,
            position,
            fields.type === "turn_context" || fields.role === "user" || message.role === "user",
          );
        }),
      );
    },
    source: (bytes, records) =>
      TranscriptMapSourceSchema.parse({
        ...capture,
        coordinates: SESSION_RECORD_COORDINATES,
        captureDigest: options.captureDigest,
        sourceDigest: options.sourceDigest,
        bytes,
        records,
      }),
  });
}
