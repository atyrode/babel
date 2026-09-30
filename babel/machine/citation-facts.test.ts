import { afterAll, beforeAll, expect, test } from "bun:test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  ArchivedCitationFactsSchema,
  CITATION_EXCERPT_MAX_BYTES,
  type ArchivedCitationInput,
} from "../contract.ts";
import { inspectArchivedCitation } from "./citation-facts.ts";
import { PREFLIGHT_DETECTORS, secretScan } from "./preflight.ts";
import { recordReader, sessionDigester } from "./session-records.ts";
import { syntheticArchive, type SyntheticArchive } from "./test/restic-fixture.ts";

const TIMEOUT = 60_000;
const encoder = new TextEncoder();
const key = "AKIA" + "IOSFODNN7SYNTH01";
const quote = "the router retries twice before giving up";
const first = JSON.stringify({ text: "an unrelated historical record", type: "user" });
const cited = JSON.stringify({ text: `${quote}; aws_access_key_id ${key}`, type: "assistant" });
const duplicate = JSON.stringify({ text: "the duplicate record occurs in two physical places" });
const long = JSON.stringify({ text: `aws_access_key_id ${key} ` + "café deployment ".repeat(800) });
// Deliberately noncanonical key order, CRLF, a blank raw line and an unterminated final record.
const capture = `${first}\r\n\r\n${cited}\r\n${duplicate}\n${duplicate}\n${long}`;
const digest = (text: string): string => new Bun.CryptoHasher("sha256").update(text).digest("hex");
const normalized: string[] = [];
const reader = recordReader((text) => normalized.push(text));
reader.write(encoder.encode(capture));
reader.finish();
const measured = (mode: "off" | "redact" = "off") => {
  const digester = sessionDigester(undefined, mode === "redact" ? secretScan() : undefined);
  digester.write(encoder.encode(capture));
  return digester.finish();
};

let fx: SyntheticArchive;
let input: ArchivedCitationInput;
let path: string;
let cleanInput: ArchivedCitationInput;

beforeAll(async () => {
  fx = await syntheticArchive();
  path = join(fx.sessionRoot("omp"), "synthetic-citation.jsonl");
  await writeFile(path, capture);
  const snapshot = await fx.snapshot("archive-host", [fx.sessionRoot("omp")]);
  const cleanPath = join(fx.sessionRoot("omp"), "synthetic-clean-citation.jsonl");
  await writeFile(cleanPath, first);
  const cleanSnapshot = await fx.snapshot("archive-host", [fx.sessionRoot("omp")]);
  const cleanDigester = sessionDigester(undefined, secretScan());
  cleanDigester.write(encoder.encode(first));
  const cleanDigests = cleanDigester.finish();
  cleanInput = {
    snapshotId: cleanSnapshot.id,
    path: cleanPath,
    label: "archive-host",
    host: "historical-host",
    harness: "omp",
    selector: "omp/synthetic-clean-citation",
    captureDigest: cleanDigests.captureDigest,
    sourceDigest: cleanDigests.sourceDigest,
    sourceMode: "refuse",
    sourceDetectors: PREFLIGHT_DETECTORS,
    locator: { line: 1, digest: cleanDigests.sourceDigest },
    quote: "an unrelated historical record",
  };
  const digests = measured();
  input = {
    snapshotId: snapshot.id,
    path,
    label: "archive-host",
    host: "historical-host",
    harness: "omp",
    selector: "omp/synthetic-citation",
    captureDigest: digests.captureDigest,
    sourceDigest: digests.sourceDigest,
    locator: { line: 2, digest: digests.sourceDigest },
    quote,
  };
  // A newer capture and the current live file both contradict the cited snapshot.
  await writeFile(path, JSON.stringify({ text: "the router never retries" }) + "\n");
  await fx.snapshot("archive-host", [fx.sessionRoot("omp")]);
}, TIMEOUT);

afterAll(async () => {
  await fx?.close();
});

test(
  "the exact archived capture yields a checked quote and a separately redacted record",
  async () => {
    const result = await inspectArchivedCitation(fx.repo, input);
    expect(result.status).toBe("available");
    expect(result.check).toEqual({ outcome: "verified", detail: "" });
    expect(result.source).toMatchObject({
      snapshotId: input.snapshotId,
      path,
      host: "historical-host",
    });
    expect(result.measured).toEqual(measured());
    expect(result.position).toEqual({
      line: 2,
      byteOffset: Buffer.byteLength(normalized[0]!),
      byteLength: Buffer.byteLength(normalized[1]!),
      digest: `sha256:${digest(normalized[1]!)}`,
    });
    expect(result.excerpt?.text).toContain(quote);
    expect(result.excerpt?.text).not.toContain(key);
    expect(result.excerpt?.text).not.toBe(quote);
    expect(result.disclosure).toMatchObject({
      mode: "redact",
      detectors: PREFLIGHT_DETECTORS,
      redactions: 1,
    });
    expect(ArchivedCitationFactsSchema.parse(result)).toEqual(result);
  },
  TIMEOUT,
);

test(
  "zero normalized offset means unspecified, not the first record's position",
  async () => {
    const answer = await inspectArchivedCitation(fx.repo, {
      ...input,
      locator: { line: 2, byteOffset: 0, digest: input.sourceDigest },
    });
    expect(answer.status).toBe("available");
    expect(answer.position?.line).toBe(2);
    expect(answer.position?.byteOffset).toBe(Buffer.byteLength(normalized[0]!));
    const noLine = await inspectArchivedCitation(fx.repo, {
      ...input,
      locator: { byteOffset: 0, digest: input.sourceDigest },
    });
    expect(noLine.reason).toBe("position-missing");
    const rawZero = await inspectArchivedCitation(fx.repo, {
      ...input,
      locator: { coordinates: "raw", byteOffset: 0, digest: digest(cited) },
    });
    expect(rawZero.reason).toBe("record-missing");
  },
  TIMEOUT,
);

test(
  "raw historical coordinates and bare record digest map to the exact normalized record",
  async () => {
    const raw = {
      coordinates: "raw" as const,
      line: 3,
      byteOffset: Buffer.byteLength(`${first}\r\n\r\n`),
      digest: digest(cited),
    };
    const historicalDigest = `sha256:${digest("the historical classified event stream")}`;
    const result = await inspectArchivedCitation(fx.repo, {
      ...input,
      sourceDigest: historicalDigest,
      locator: raw,
    });
    expect(result.status).toBe("available");
    expect(result.position?.line).toBe(2);
    expect(result.position?.raw).toEqual({
      line: raw.line,
      byteOffset: raw.byteOffset,
      digest: raw.digest,
    });
    expect(result.check.outcome).toBe("verified");
    expect(result.sourceReading).toBe("historical-events");
    expect(result.source.sourceDigest).toBe(historicalDigest);
    expect(result.measured?.sourceDigest).toBe(input.sourceDigest);
    expect(result.measured?.sourceDigest).not.toBe(result.source.sourceDigest);
    const wrong = await inspectArchivedCitation(fx.repo, {
      ...input,
      locator: { ...raw, byteOffset: raw.byteOffset + 1 },
    });
    expect(wrong.reason).toBe("record-missing");
    expect(wrong.check.outcome).toBe("unchecked");
  },
  TIMEOUT,
);

test(
  "capture, source and locator mismatches never become checked absent",
  async () => {
    for (const change of [
      { captureDigest: `sha256:${"0".repeat(64)}` },
      { sourceDigest: `sha256:${"0".repeat(64)}`, locator: { line: 2 } },
      { locator: { line: 2, digest: `sha256:${"0".repeat(64)}` } },
      { locator: { coordinates: "raw" as const, line: 3, digest: "0".repeat(64) } },
    ]) {
      const result = await inspectArchivedCitation(fx.repo, {
        ...input,
        ...change,
        quote: "a quotation found nowhere in this archive",
      });
      expect(result.status).toBe("unavailable");
      expect(result.check.outcome).toBe("unchecked");
      expect(result.excerpt).toBeNull();
      expect(result.position).toBeNull();
    }
  },
  TIMEOUT,
);

test(
  "missing archive or position and ambiguous records are explicitly unavailable",
  async () => {
    const cases: [Partial<ArchivedCitationInput>, string][] = [
      [{ path: `${path}.missing` }, "source-unavailable"],
      [{ snapshotId: "0".repeat(64) }, "source-unavailable"],
      [{ snapshotId: "latest" }, "exact-source-required"],
      [{ locator: {} }, "position-missing"],
      [{ locator: { line: 1000 } }, "record-missing"],
      [{ locator: { recordDigest: `sha256:${digest(normalized[2]!)}` } }, "record-ambiguous"],
      [{ locator: { coordinates: "raw", digest: digest(duplicate) } }, "record-ambiguous"],
      [{ maxCaptureBytes: 64 }, "source-unavailable"],
    ];
    for (const [change, reason] of cases) {
      const result = await inspectArchivedCitation(fx.repo, { ...input, ...change });
      expect(result.reason).toBe(reason);
      expect(result.status).toBe("unavailable");
      expect(result.check.outcome).toBe("unchecked");
      expect(result.excerpt).toBeNull();
    }
  },
  TIMEOUT,
);

test(
  "newly retrieved text does not invent a quote for a quote-free legacy citation",
  async () => {
    const result = await inspectArchivedCitation(fx.repo, { ...input, quote: "" });
    expect(result.status).toBe("available");
    expect(result.check.outcome).toBe("unquoted");
    expect(result.excerpt?.text).toContain(quote);
    const missing = await inspectArchivedCitation(fx.repo, {
      ...input,
      quote: "",
      path: `${path}.missing`,
    });
    expect(missing.status).toBe("unavailable");
    expect(missing.check.outcome).toBe("unquoted");
  },
  TIMEOUT,
);

test(
  "a moved quote never moves the retrieved excerpt away from the cited record",
  async () => {
    const result = await inspectArchivedCitation(fx.repo, { ...input, locator: { line: 1 } });
    expect(result.check.outcome).toBe("moved");
    expect(result.check.detail).toContain("line 2");
    expect(result.position?.line).toBe(1);
    expect(result.excerpt?.text).toContain("unrelated historical record");
    expect(result.excerpt?.text).not.toContain(quote);
    const absent = await inspectArchivedCitation(fx.repo, {
      ...input,
      quote: "an entirely fabricated absent quotation",
    });
    expect(absent.status).toBe("available");
    expect(absent.check.outcome).toBe("absent");
  },
  TIMEOUT,
);

test(
  "redaction precedes bounded UTF-8 clipping and respects recorded source reading mode",
  async () => {
    const result = await inspectArchivedCitation(fx.repo, {
      ...input,
      locator: { line: 5 },
      quote: "",
    });
    expect(result.excerpt?.bytes).toBeLessThanOrEqual(CITATION_EXCERPT_MAX_BYTES);
    expect(Buffer.byteLength(result.excerpt!.text)).toBe(result.excerpt!.bytes);
    expect(result.excerpt?.truncated).toBe(true);
    expect(result.excerpt?.text).not.toContain(key);
    expect(result.excerpt?.text).not.toContain("\ufffd");
    const redacted = await inspectArchivedCitation(fx.repo, {
      ...input,
      sourceMode: "redact",
      sourceDetectors: PREFLIGHT_DETECTORS,
      sourceDigest: measured("redact").sourceDigest,
      locator: { line: 2 },
    });
    expect(redacted.status).toBe("available");
    expect(redacted.check.outcome).toBe("verified");
    expect(redacted.measured?.sourceDigest).toBe(measured("redact").sourceDigest);
    const unknown = await inspectArchivedCitation(fx.repo, {
      ...input,
      sourceMode: "redact",
      sourceDetectors: "unknown/99",
    });
    expect(unknown.reason).toBe("source-reading-unsupported");
  },
  TIMEOUT,
);

test(
  "successful refuse-mode preparation remains scanned and refuses secrets anywhere in its capture",
  async () => {
    const clean = await inspectArchivedCitation(fx.repo, cleanInput);
    expect(ArchivedCitationFactsSchema.parse(clean)).toMatchObject({
      status: "available",
      check: { outcome: "verified" },
      source: { sourceMode: "refuse", sourceDetectors: PREFLIGHT_DETECTORS },
      measured: { sourceDigest: cleanInput.sourceDigest },
      excerpt: { text: normalized[0] },
    });
    for (const coordinates of ["normalized", "raw"] as const) {
      const refused = await inspectArchivedCitation(fx.repo, {
        ...input,
        sourceMode: "refuse",
        sourceDetectors: PREFLIGHT_DETECTORS,
        // The cited first record is clean; later records must still refuse the whole capture.
        sourceDigest: measured("redact").sourceDigest,
        locator:
          coordinates === "raw"
            ? { coordinates, line: 1, digest: digest(first) }
            : { coordinates, line: 1 },
        quote: cleanInput.quote,
      });
      expect(refused).toMatchObject({
        status: "unavailable",
        reason: "source-secrets-refused",
        check: { outcome: "unchecked" },
        source: { sourceMode: "refuse", sourceDetectors: PREFLIGHT_DETECTORS },
        position: null,
        excerpt: null,
      });
      expect(JSON.stringify(refused)).not.toContain(key);
    }
    const unsupported = await inspectArchivedCitation(fx.repo, {
      ...cleanInput,
      sourceDetectors: "unknown/99",
    });
    expect(unsupported).toMatchObject({
      status: "unavailable",
      reason: "source-reading-unsupported",
      source: { sourceMode: "refuse", sourceDetectors: "unknown/99" },
      excerpt: null,
    });
  },
  TIMEOUT,
);

test(
  "re-reading is deterministic and does not change archived captures",
  async () => {
    const snapshots = await fx.repo.snapshots();
    const one = await inspectArchivedCitation(fx.repo, input);
    const two = await inspectArchivedCitation(fx.repo, input);
    expect(two).toEqual(one);
    expect(await fx.repo.snapshots()).toEqual(snapshots);
    expect(await fx.locks()).toBe(0);
  },
  TIMEOUT,
);
