import { createHash } from "node:crypto";
import {
  NeighborhoodMapSourceSchema,
  NeighborhoodSourceRecordSchema,
  type NeighborhoodRecords,
  type NeighborhoodMapSource,
  type TranscriptMapPolicy,
} from "../contract.ts";
import { buildNavigationMap } from "../transcript-map-tree.ts";
import { secretScan } from "../machine/preflight.ts";

export const neighborhoodDigest = (value: unknown): string =>
  `sha256:${createHash("sha256").update(JSON.stringify(value)).digest("hex")}`;

/** The response byte counter is transport, not a source revision. All status and coverage are. */
export function neighborhoodRevision(result: NeighborhoodRecords): string {
  return neighborhoodDigest({ ...result, coverage: { ...result.coverage, resultBytes: 0 } });
}

/** Only the source adapter differs. Segmentation, versions, work, reviews and prose are shared. */
export async function neighborhoodMapSource(
  result: NeighborhoodRecords,
  policy: TranscriptMapPolicy,
  now: string,
  retained?: NeighborhoodMapSource,
) {
  const query = { entityId: result.entityId, ...result.limits };
  const revision = neighborhoodRevision(result);
  const scan = secretScan();
  const lines: string[] = [];
  let disputed = 0;
  let stale = 0;
  for (const kind of NeighborhoodSourceRecordSchema.shape.kind.options) {
    const values = kind === "coverage" ? [{ ...result.coverage, resultBytes: 0 }] : result[kind];
    for (const value of values) {
      const row: Record<string, unknown> = value;
      const status = "status" in row ? (row.status as { state: string } | null) : null;
      if (status?.state === "disputed") disputed++;
      if (status?.state === "stale" || ("replacedBy" in row && row.replacedBy !== null)) stale++;
      const record = {
        kind,
        id: String(row.id ?? row.selector ?? result.entityId),
        revision: neighborhoodDigest(value),
        value,
      };
      lines.push(scan.redact(JSON.stringify(record), lines.length + 1) + "\n");
    }
  }
  const text = lines.join("");
  const buffer = Buffer.from(text);
  const sourceDigest = `sha256:${createHash("sha256").update(buffer).digest("hex")}`;
  const id = `tmcap_${neighborhoodDigest(["neighborhood", policy.sourceMachineId, query, revision]).slice(7)}`;
  const tree = await buildNavigationMap({
    source: (bytes, records) =>
      NeighborhoodMapSourceSchema.parse(
        retained ?? {
          kind: "neighborhood",
          id,
          query,
          revision,
          sourceDigest,
          bytes,
          records,
          capturedAt: now,
          coverage: { ...result.coverage, resultBytes: 0 },
          disputed,
          stale,
          redactions: scan.report().redactions,
        },
      ),
    sourceDigest,
    segmentation: policy.segmentation,
    replay: async (visit) => {
      let offset = 0;
      for (const [index, line] of lines.entries()) {
        const byteLength = Buffer.byteLength(line);
        visit(
          line.slice(0, -1),
          {
            line: index + 1,
            byteOffset: offset,
            byteLength,
            digest: `sha256:${createHash("sha256").update(line).digest("hex")}`,
            time: null,
          },
          false,
        );
        offset += byteLength;
      }
    },
    rangeDigest: async (offset, bytes) =>
      `sha256:${createHash("sha256")
        .update(buffer.subarray(offset, offset + bytes))
        .digest("hex")}`,
  });
  return { ...tree, text };
}
