import { Database } from "bun:sqlite";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import {
  RECALL_MAX_PAYLOAD_BYTES, RECALL_REQUEST_TTL_MS, RECALL_UNTRUSTED_BEGIN, RECALL_UNTRUSTED_END,
  RECALL_MAX_SERVED_BYTES,
  TRANSCRIPT_MAP_MAX_SPAN_BYTES, TranscriptMapNativeRequestSchema, TranscriptMapSegmentationSchema,
  type TranscriptMapCapture, type TranscriptMapSource, type TranscriptMapSpan,
  type TranscriptMapNativeRequest, type TranscriptMapNativeResult, type TranscriptMapContext,
  type TranscriptMapSegmentation, type RecallPolicy, type RecallResult, type SessionRecordPosition,
} from "../contract.ts";
import { transcriptMapCaptureId } from "../transcript-map-identity.ts";
import { buildTranscriptMap, type TranscriptMapTree } from "./transcript-map-tree.ts";
import type { ReusedReading } from "./cache.ts";
import type { RecordSink } from "./output.ts";
import { clipUtf8 } from "./recall-records.ts";

const digest = (value: unknown): string => `sha256:${createHash("sha256").update(JSON.stringify(value)).digest("hex")}`;
type Refusal = NonNullable<TranscriptMapNativeResult["refusal"]>;
class Refused extends Error { constructor(readonly reason: Refusal) { super(reason); } }
export interface MappingCapture {
  capture: TranscriptMapCapture;
  sensitivity: number;
  load(cost: RecallResult["cost"]): Promise<ReusedReading>;
  verify(reading: ReusedReading, sink: RecordSink, cost: RecallResult["cost"]): Promise<void>;
}
interface Kept {
  reading: ReusedReading;
  source: TranscriptMapSource;
  db: Database;
  directory: string;
  tree: TranscriptMapTree;
  offsets: Map<string, number>;
  expires: number;
}
interface Preview {
  classId: string;
  text: string | null;
  bytes: number;
  offset: number;
  expires: number;
  previous: NonNullable<TranscriptMapNativeResult["page"]> | null;
}

/** Uses the archive's normalized, redacted reading and the same class byte/handle accounts.
 * Coordinates are built from a verified native replay, never from a supplied span or digest. */
export function transcriptMapArchive(options: {
  policy: RecallPolicy;
  temporaryDir: string;
  now(): number;
  inventory(ceiling: number, cost: RecallResult["cost"]): Promise<{ captures: MappingCapture[]; inventory: unknown }>;
  reserve(classId: string, bytes: number): boolean;
  release(classId: string, bytes: number): void;
  reason(error: unknown): NonNullable<RecallResult["refusal"]>;
}) {
  const kept = new Map<string, Kept>();
  const previews = new Map<string, Preview>();
  const policyDigest = digest(options.policy);
  let directory: string | undefined;
  const drop = async (id: string, value: Kept): Promise<void> => {
    kept.delete(id);
    value.db.close();
    await rm(value.directory, { recursive: true, force: true });
  };
  const release = (value: Preview): void => {
    if (value.text !== null) options.release(value.classId, value.bytes);
    value.text = null;
  };
  const expire = async (): Promise<void> => {
    for (const [id, value] of previews) if (value.expires <= options.now()) { release(value); previews.delete(id); }
    for (const [id, value] of kept) if (value.expires <= options.now()) await drop(id, value);
  };
  const rangeDigest = async (path: string, offset: number, bytes: number): Promise<string> => {
    const hash = createHash("sha256");
    let count = 0;
    for await (const chunk of Bun.file(path).slice(offset, offset + bytes).stream()) { count += chunk.length; hash.update(chunk); }
    if (count !== bytes) throw new Refused("capture-changed");
    return `sha256:${hash.digest("hex")}`;
  };
  const load = async (entry: MappingCapture, segmentation: TranscriptMapSegmentation, cost: RecallResult["cost"]): Promise<Kept> => {
    const reading = await entry.load(cost);
    let existing = kept.get(entry.capture.id);
    if (existing && (existing.reading.captureDigest !== reading.captureDigest || existing.reading.sourceDigest !== reading.sourceDigest || existing.reading.stream !== reading.stream)) {
      await drop(entry.capture.id, existing);
      existing = undefined;
    }
    if (existing && JSON.stringify(existing.tree.header.segmentation) === JSON.stringify(segmentation)) {
      existing.expires = options.now() + RECALL_REQUEST_TTL_MS;
      return existing;
    }
    if (existing) await drop(entry.capture.id, existing);
    // One active coordinate/tree cache bounds amplification independently of source bytes.
    // The durable normalized reading still survives capture switches.
    if (kept.size >= 1) {
      const oldest = kept.entries().next().value;
      if (oldest) await drop(oldest[0], oldest[1]);
    }
    directory ??= await mkdtemp(join(options.temporaryDir, "babel-transcript-maps-"));
    const owned = join(directory, crypto.randomUUID());
    await mkdir(owned, { mode: 0o700 });
    const db = new Database(join(owned, "coordinates.sqlite"));
    try {
      db.exec(`PRAGMA page_size = 4096; PRAGMA max_page_count = ${Math.floor(RECALL_MAX_SERVED_BYTES / 4 / 4096)}`);
      db.exec("CREATE TABLE records (line INTEGER PRIMARY KEY, offset INTEGER NOT NULL, bytes INTEGER NOT NULL, digest TEXT NOT NULL, time TEXT)");
      const insert = db.query("INSERT INTO records VALUES (?, ?, ?, ?, ?)");
      db.exec("BEGIN");
      const tree = await buildTranscriptMap({
        capture: entry.capture, captureDigest: reading.captureDigest, sourceDigest: reading.sourceDigest, segmentation,
        replay: (sink) => entry.verify(reading, sink, cost),
        rangeDigest: async (offset, bytes) => {
          const found = await rangeDigest(reading.stream, offset, bytes);
          cost.replayedBytes += bytes;
          return found;
        },
        record(position) { insert.run(position.line, position.byteOffset, position.byteLength, position.digest, position.time); },
      });
      db.exec("COMMIT");
      if (tree.header.source.records !== reading.records) throw new Refused("capture-changed");
      const value: Kept = { reading, source: tree.header.source, db, directory: owned, tree, offsets: new Map(tree.nodes.map((node, offset) => [node.id, offset])), expires: options.now() + RECALL_REQUEST_TTL_MS };
      kept.set(entry.capture.id, value);
      return value;
    } catch (error) {
      db.close();
      await rm(owned, { recursive: true, force: true });
      if (error !== null && typeof error === "object" && "code" in error && error.code === "SQLITE_FULL")
        throw new Refused("fetch-bound");
      throw error;
    }
  };
  const position = (value: Kept, line: number): SessionRecordPosition | null => value.db.query<SessionRecordPosition, [number]>(
    "SELECT line, offset AS byteOffset, bytes AS byteLength, digest, time FROM records WHERE line = ?",
  ).get(line);
  const readSpan = async (value: Kept, source: TranscriptMapSource, span: TranscriptMapSpan, cost: RecallResult["cost"]): Promise<string> => {
    if (JSON.stringify(source) !== JSON.stringify(value.source)) throw new Refused("locator-mismatch");
    const first = position(value, span.firstRecord);
    const last = position(value, span.lastRecord);
    if (!first || !last || JSON.stringify(first) !== JSON.stringify(span.anchor) || first.byteOffset !== span.byteOffset || last.byteOffset + last.byteLength !== span.byteOffset + span.byteLength) throw new Refused("locator-mismatch");
    if (span.byteLength > TRANSCRIPT_MAP_MAX_SPAN_BYTES) throw new Refused("fetch-bound");
    const bytes = new Uint8Array(await Bun.file(value.reading.stream).slice(span.byteOffset, span.byteOffset + span.byteLength).arrayBuffer());
    cost.replayedBytes += bytes.length;
    if (bytes.length !== span.byteLength) throw new Refused("capture-changed");
    const actual = `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
    if (actual !== span.digest) throw new Refused("locator-mismatch");
    // Check EVERY record against coordinates derived independently of the caller. A forged
    // span digest cannot bless cache corruption or cause a destructive cache repair.
    let line = span.firstRecord;
    let offset = 0;
    const records = value.db.query<{ line: number; offset: number; bytes: number; digest: string }, [number, number]>(
      "SELECT line, offset, bytes, digest FROM records WHERE line BETWEEN ? AND ? ORDER BY line",
    );
    for (const record of records.iterate(span.firstRecord, span.lastRecord)) {
      if (record.line !== line++ || record.offset !== span.byteOffset + offset || record.bytes > bytes.length - offset)
        throw new Refused("capture-changed");
      const digest = `sha256:${createHash("sha256").update(bytes.subarray(offset, offset + record.bytes)).digest("hex")}`;
      if (digest !== record.digest) throw new Refused("capture-changed");
      offset += record.bytes;
    }
    if (line !== span.lastRecord + 1 || offset !== bytes.length) throw new Refused("capture-changed");
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  };
  return {
    expire,
    async execute(classId: string, input: TranscriptMapNativeRequest, privileged: boolean): Promise<TranscriptMapNativeResult> {
      const result: TranscriptMapNativeResult = { operation: input.kind, entries: [], nextCursor: null, accesses: [], cost: { fetchedFiles: 0, fetchedBytes: 0, replayedBytes: 0, cacheHits: 0, indexedFiles: 0, listedSnapshots: 0, listedEntries: 0 }, refusal: null };
      try {
        const request = TranscriptMapNativeRequestSchema.parse(input);
        const disclosure = options.policy.classes.find((value) => value.id === classId);
        if (!disclosure) throw new Refused("disclosure");
        if (!["map-context", "map-inventory", "map-authorize", "map-span"].includes(request.kind) && (!privileged || options.policy.mappingClassId !== classId)) throw new Refused("disclosure");
        await expire();
        if (request.kind === "map-page" || request.kind === "map-release") {
          const value = previews.get(request.previewId);
          if (!value) throw new Refused("preview-expired");
          if (value.classId !== classId) throw new Refused("disclosure");
          if (request.kind === "map-release") { release(value); previews.delete(request.previewId); return result; }
          if (value.previous?.offset === request.offset) {
            if (Buffer.byteLength(value.previous.text) > request.maxBytes) throw new Refused("invalid-offset");
            result.page = value.previous;
            return result;
          }
          if (request.offset !== value.offset) throw new Refused("invalid-offset");
          const clipped = clipUtf8(value.text ?? "", Math.min(request.maxBytes, Math.floor((RECALL_MAX_PAYLOAD_BYTES - 4096) / 6)));
          if (!clipped.bytes && value.offset < value.bytes) throw new Refused("response-bound");
          const next = value.offset + clipped.bytes;
          const page = { text: clipped.text, offset: value.offset, nextOffset: next, totalBytes: value.bytes, complete: next === value.bytes };
          result.page = page;
          if (value.text !== null) {
            value.previous = page;
            value.offset = next;
            if (page.complete) release(value);
            else value.text = value.text.slice(clipped.text.length);
            value.expires = options.now() + RECALL_REQUEST_TTL_MS;
          }
          return result;
        }
        const inventory = await options.inventory(disclosure.ceiling, result.cost);
        inventory.captures.sort((a, b) => a.capture.id.localeCompare(b.capture.id));
        const context: TranscriptMapContext = { digest: digest([policyDigest, inventory.inventory]), policyDigest, classId, ceiling: disclosure.ceiling, eligibleCaptures: inventory.captures.length, observedAt: new Date(options.now()).toISOString() };
        result.context = context;
        const access = (entry: MappingCapture) => ({ captureId: entry.capture.id, contextDigest: context.digest, sensitivity: entry.sensitivity });
        const selected = (capture: TranscriptMapCapture): MappingCapture => {
          if (transcriptMapCaptureId(capture) !== capture.id) throw new Refused("locator-mismatch");
          const entry = inventory.captures.find((candidate) => candidate.capture.id === capture.id);
          if (!entry) throw new Refused("disclosure");
          if (JSON.stringify(entry.capture) !== JSON.stringify({ id: capture.id, host: capture.host, harness: capture.harness, session: capture.session, snapshot: capture.snapshot, path: capture.path, capturedAt: capture.capturedAt })) throw new Refused("locator-mismatch");
          return entry;
        };
        if (request.kind === "map-context") return result;
        if (request.kind === "map-inventory") {
          let offset = 0;
          if (request.cursor) {
            const match = /^([0-9a-f]{64}):([0-9]+)$/.exec(request.cursor);
            if (!match || match[1] !== digest([context.digest, classId]).slice(7)) throw new Refused("stale-context");
            offset = Number(match[2]);
            if (!Number.isSafeInteger(offset) || offset > inventory.captures.length) throw new Refused("invalid-offset");
          }
          while (offset < inventory.captures.length && result.entries.length < request.maxCaptures) {
            const entry = inventory.captures[offset]!;
            result.entries.push({ capture: entry.capture, access: access(entry) });
            if (Buffer.byteLength(JSON.stringify(result)) > RECALL_MAX_PAYLOAD_BYTES - 256) { result.entries.pop(); break; }
            offset++;
          }
          result.nextCursor = offset < inventory.captures.length ? `${digest([context.digest, classId]).slice(7)}:${offset}` : null;
          return result;
        }
        if (request.kind === "map-authorize") {
          for (const capture of request.captures) {
            try { result.accesses.push(access(selected(capture))); } catch { /* No authority for missing or reclassified captures. */ }
          }
          return result;
        }
        const entry = selected(request.kind === "map-plan" ? request.capture : request.source);
        result.accesses = [access(entry)];
        const segmentation = request.kind === "map-plan" || request.kind === "map-node" ? request.segmentation : kept.get(entry.capture.id)?.tree.header.segmentation ?? TranscriptMapSegmentationSchema.parse({});
        const value = await load(entry, segmentation, result.cost);
        if (request.kind === "map-node") {
          if (JSON.stringify(request.source) !== JSON.stringify(value.source)) throw new Refused("locator-mismatch");
          const offset = value.offsets.get(request.nodeId);
          if (offset === undefined) throw new Refused("locator-mismatch");
          result.plan = { header: value.tree.header, nodes: [value.tree.nodes[offset]!], offset, nextOffset: null };
          return result;
        }
        if (request.kind === "map-plan") {
          if (request.offset > value.tree.nodes.length) throw new Refused("invalid-offset");
          const nodes = value.tree.nodes.slice(request.offset, request.offset + request.maxNodes);
          result.plan = { header: value.tree.header, nodes, offset: request.offset, nextOffset: null };
          while (Buffer.byteLength(JSON.stringify(result)) > RECALL_MAX_PAYLOAD_BYTES - 128 && nodes.length) nodes.pop();
          if (!nodes.length && request.offset < value.tree.nodes.length) throw new Refused("response-bound");
          const next = request.offset + nodes.length;
          result.plan.nextOffset = next < value.tree.nodes.length ? next : null;
          return result;
        }
        const text = await readSpan(value, request.source, request.span, result.cost);
        if (request.kind === "map-span") {
          result.span = { source: value.source, span: request.span, excerpt: { trust: "archived-untrusted", begin: RECALL_UNTRUSTED_BEGIN, end: RECALL_UNTRUSTED_END, text: "", bytes: 0, maxBytes: request.maxBytes, truncated: true, firstRecord: 0, lastRecord: 0 } };
          const budget = Math.floor((RECALL_MAX_PAYLOAD_BYTES - Buffer.byteLength(JSON.stringify(result)) - 128) / 6);
          if (budget < 4) throw new Refused("response-bound");
          const clipped = clipUtf8(text, Math.min(request.maxBytes, budget));
          const lines = clipped.text.split("\n").length - (clipped.text.endsWith("\n") ? 1 : 0);
          result.span.excerpt = { ...result.span.excerpt, text: clipped.text, bytes: clipped.bytes, truncated: clipped.bytes < request.span.byteLength, firstRecord: clipped.bytes ? request.span.firstRecord : 0, lastRecord: clipped.bytes ? request.span.firstRecord + lines - 1 : 0 };
        } else {
          for (const [id, preview] of previews) if (preview.classId === classId && preview.text === null) previews.delete(id);
          if (!options.reserve(classId, request.span.byteLength)) throw new Refused("fetch-bound");
          const previewId = crypto.randomUUID();
          previews.set(previewId, { classId, text, bytes: request.span.byteLength, offset: 0, expires: options.now() + RECALL_REQUEST_TTL_MS, previous: null });
          result.preview = { previewId, bytes: request.span.byteLength, sourceDigest: value.source.sourceDigest, spanDigest: request.span.digest };
        }
      } catch (error) {
        result.refusal = error instanceof Refused ? error.reason : options.reason(error);
        result.entries = []; result.accesses = []; result.nextCursor = null;
        delete result.span; delete result.plan; delete result.preview; delete result.page;
      }
      return result;
    },
    async close(): Promise<void> {
      for (const value of previews.values()) release(value);
      previews.clear();
      for (const [id, value] of kept) await drop(id, value);
      if (directory) await rm(directory, { recursive: true, force: true });
    },
  };
}
