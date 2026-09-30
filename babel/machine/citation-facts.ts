import {
  CITATION_OUTCOMES,
  CITATION_FACTS_VERSION,
  CITATION_CAPTURE_MAX_BYTES,
  CITATION_EXCERPT_MAX_BYTES,
  type ArchivedCitationFacts,
  type ArchivedCitationInput,
  type CitationFactPosition,
  type CitationFactExcerpt,
} from "../contract.ts";
import { checkQuote } from "../server/engine/citations.ts";
import { PREFLIGHT_DETECTORS, secretScan } from "./preflight.ts";
import { clipUtf8 } from "./recall-records.ts";
import type { Repo } from "./restic.ts";
import { sessionDigester, type SessionDigests } from "./session-records.ts";

interface Candidate {
  position: CitationFactPosition;
  excerpt: CitationFactExcerpt;
  redactions: number;
  matchesQuote: boolean;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const carriage = new Uint8Array([13]);
const hash = (bytes: Uint8Array): string =>
  new Bun.CryptoHasher("sha256").update(bytes).digest("hex");

/**
 * Inspect only the named immutable archive member. No catalog, latest snapshot, local file,
 * model, persistent cache or clock participates. All streamed findings remain provisional until
 * the archive read succeeds and its capture digest matches. Normalized readings also require
 * source-digest equality; historical Go event digests are explicitly not comparable.
 */
export async function inspectArchivedCitation(
  repo: Pick<Repo, "dumpTo">,
  input: ArchivedCitationInput,
): Promise<ArchivedCitationFacts> {
  const sourceMode = input.sourceMode ?? "off";
  const source = {
    snapshotId: input.snapshotId,
    path: input.path,
    label: input.label,
    host: input.host,
    harness: input.harness,
    selector: input.selector,
    captureDigest: input.captureDigest,
    sourceDigest: input.sourceDigest,
    sourceMode,
    sourceDetectors:
      sourceMode === "off" ? null : (input.sourceDetectors ?? PREFLIGHT_DETECTORS),
  };
  const disclosure: ArchivedCitationFacts["disclosure"] = {
    mode: "redact" as const,
    detectors: PREFLIGHT_DETECTORS,
    version: CITATION_FACTS_VERSION,
    redactions: 0,
  };
  const initialCheck = checkQuote(input.quote, [], 0);
  const sourceReading =
    input.locator.coordinates === "raw" ? "historical-events" : "normalized-records";
  let measured: SessionDigests | null = null;
  const unavailable = (reason: string): ArchivedCitationFacts => ({
    status: "unavailable",
    reason,
    check:
      initialCheck.outcome === CITATION_OUTCOMES.unquoted
        ? initialCheck
        : { outcome: CITATION_OUTCOMES.unchecked, detail: reason },
    source,
    sourceReading,
    measured,
    position: null,
    excerpt: null,
    disclosure,
  });
  const maxBytes = input.maxCaptureBytes ?? CITATION_CAPTURE_MAX_BYTES;
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 1) return unavailable("invalid-capture-bound");
  // Prefixes and `latest` resolve names rather than identifying one immutable snapshot.
  if (!/^[0-9a-f]{64}$/.test(input.snapshotId) || !input.path.startsWith("/"))
    return unavailable("exact-source-required");
  if (source.sourceDetectors !== null && source.sourceDetectors !== PREFLIGHT_DETECTORS)
    return unavailable("source-reading-unsupported");
  const locator = input.locator;
  const raw = locator.coordinates === "raw";
  if (
    locator.coordinates !== undefined &&
    locator.coordinates !== "raw" &&
    locator.coordinates !== "normalized"
  )
    return unavailable("coordinates-unsupported");
  const line = locator.line === 0 ? undefined : locator.line;
  if (
    (line !== undefined && (!Number.isSafeInteger(line) || line < 1)) ||
    (locator.byteOffset !== undefined &&
      (!Number.isSafeInteger(locator.byteOffset) || locator.byteOffset < 0))
  )
    return unavailable("position-invalid");
  if (!raw && locator.digest !== undefined && locator.digest !== input.sourceDigest)
    return unavailable("locator-digest-mismatch");
  // Current material uses zero as "offset unknown"; Go's raw zero is an exact first byte.
  const positionedOffset = !raw && locator.byteOffset === 0 ? undefined : locator.byteOffset;
  if (
    line === undefined &&
    positionedOffset === undefined &&
    locator.recordDigest === undefined &&
    !(raw && locator.digest !== undefined)
  )
    return unavailable("position-missing");
  if (raw && (locator.digest === undefined || !/^[0-9a-f]{64}$/.test(locator.digest)))
    return unavailable("raw-record-digest-required");

  let normalizedLine = 0;
  let normalizedOffset = 0;
  let firstMatch = 0;
  let matches = 0;
  let selected: Candidate | null = null;
  let ambiguous = false;
  let rawLine = 1;
  let rawOffset = 0;
  let rawBytes = 0;
  let rawRecords = 0;
  let rawCandidate: Candidate | null = null;
  let rawHasher = new Bun.CryptoHasher("sha256");
  let heldCarriage = false;
  const rawPositionMatches = (): boolean =>
    (line === undefined || line === rawLine) &&
    (locator.byteOffset === undefined || locator.byteOffset === rawOffset);
  const select = (candidate: Candidate): void => {
    matches++;
    selected = matches === 1 ? candidate : null;
  };
  // Like preparation, both scanned modes redact before hashing; refuse publishes nothing
  // when the complete capture's scan identifies a secret, even outside the cited record.
  const sourceScan = sourceMode === "off" ? undefined : secretScan();
  const digester = sessionDigester(
    {
      write(chunk) {
        // sessionDigester seals exactly one complete normalized record per write.
        const bytes = typeof chunk === "string" ? encoder.encode(chunk) : chunk;
        const text = decoder.decode(bytes);
        const position = {
          line: ++normalizedLine,
          byteOffset: normalizedOffset,
          byteLength: bytes.byteLength,
          digest: `sha256:${hash(bytes)}`,
        };
        normalizedOffset += bytes.byteLength;
        rawRecords++;
        const matchesQuote =
          initialCheck.outcome === CITATION_OUTCOMES.absent &&
          checkQuote(input.quote, [text], 1).outcome === CITATION_OUTCOMES.verified;
        if (matchesQuote && firstMatch === 0) firstMatch = normalizedLine;
        if (
          raw
            ? !rawPositionMatches() || rawRecords !== 1
            : (line !== undefined && line !== position.line) ||
              (positionedOffset !== undefined && positionedOffset !== position.byteOffset)
        )
          return;
        if (locator.recordDigest !== undefined && locator.recordDigest !== position.digest) return;
        const scan = secretScan();
        const clean = scan.redact(text, position.line);
        const clipped = clipUtf8(clean, CITATION_EXCERPT_MAX_BYTES);
        const candidate: Candidate = {
          position,
          excerpt: {
            ...clipped,
            maxBytes: CITATION_EXCERPT_MAX_BYTES,
            trust: "archived-untrusted",
          },
          redactions: scan.report().redactions,
          matchesQuote,
        };
        if (raw) rawCandidate = candidate;
        else select(candidate);
      },
      async close() {},
    },
    sourceScan,
  );
  const endRawRecord = (): void => {
    const digest = rawHasher.digest("hex");
    if (rawPositionMatches() && (locator.digest === undefined || locator.digest === digest)) {
      if (rawRecords > 1) ambiguous = true;
      else if (rawCandidate !== null) {
        rawCandidate.position = {
          ...rawCandidate.position,
          raw: { line: rawLine, byteOffset: rawOffset, digest },
        };
        select(rawCandidate);
      }
    }
    rawOffset += rawBytes;
    rawLine++;
    rawBytes = 0;
    rawRecords = 0;
    rawCandidate = null;
    heldCarriage = false;
    rawHasher = new Bun.CryptoHasher("sha256");
  };
  try {
    await repo.dumpTo(
      input.snapshotId,
      input.path,
      (chunk) => {
        if (!raw) {
          digester.write(chunk);
          return;
        }
        // Preserve historical physical lines/offsets while feeding the same bytes to the shared
        // normalizer. A raw record that normalizes to zero or several records is never guessed.
        let start = 0;
        while (start < chunk.byteLength) {
          const newline = chunk.indexOf(10, start);
          const end = newline < 0 ? chunk.byteLength : newline;
          let body = chunk.subarray(start, end);
          if (heldCarriage && body.byteLength > 0) {
            rawHasher.update(carriage);
            heldCarriage = false;
          }
          if (body.at(-1) === 13) {
            body = body.subarray(0, -1);
            heldCarriage = true;
          }
          rawHasher.update(body);
          const next = newline < 0 ? end : end + 1;
          rawBytes += next - start;
          digester.write(chunk.subarray(start, next));
          if (newline >= 0) endRawRecord();
          start = next;
        }
      },
      { maxBytes, stopOnBound: true },
    );
    measured = digester.finish();
    if (raw && rawBytes > 0) {
      if (heldCarriage) rawHasher.update(carriage);
      endRawRecord();
    }
  } catch {
    // Restic errors can quote paths or source bytes: never publish its diagnostic as evidence.
    return unavailable("source-unavailable");
  }
  if (measured.captureDigest !== input.captureDigest) return unavailable("capture-digest-mismatch");
  if (sourceMode === "refuse" && sourceScan!.report().redactions > 0)
    return unavailable("source-secrets-refused");
  if (!raw && measured.sourceDigest !== input.sourceDigest)
    return unavailable("source-digest-mismatch");
  if (ambiguous || matches > 1) return unavailable("record-ambiguous");
  // The sink owns assignment; TypeScript does not infer assignments across callbacks.
  const found = selected as Candidate | null;
  if (found === null) return unavailable("record-missing");
  const check: ArchivedCitationFacts["check"] =
    initialCheck.outcome !== CITATION_OUTCOMES.absent
      ? initialCheck
      : found.matchesQuote
        ? { outcome: CITATION_OUTCOMES.verified, detail: "" }
        : firstMatch > 0
          ? {
              outcome: CITATION_OUTCOMES.moved,
              detail: `the quoted text is at line ${String(firstMatch)} of this session, not at line ${String(found.position.line)}`,
            }
          : initialCheck;
  return {
    status: "available",
    reason: null,
    check,
    source,
    sourceReading,
    measured,
    position: found.position,
    excerpt: found.excerpt,
    disclosure: { ...disclosure, redactions: found.redactions },
  };
}
