import { chmod, mkdir, mkdtemp, rename, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { z } from "zod";
import { join } from "node:path";
import {
  MAX_MATERIAL_BYTES,
  RECALL_MAX_HITS,
  RECALL_MAX_PAYLOAD_BYTES,
  RECALL_MAX_REQUESTS,
  RECALL_MAX_SERVED_BYTES,
  RECALL_REQUEST_TTL_MS,
  RECALL_SEARCH_EXCERPT_BYTES,
  RECALL_UNTRUSTED_BEGIN,
  RECALL_UNTRUSTED_END,
  SESSION_RECORD_COORDINATES,
  RecallMetadataSchema,
  RecallPolicySchema,
  RecallRequestSchema,
  type RecallFilter,
  type RecallHit,
  type RecallLocator,
  type RecallMetadata,
  type RecallPolicy,
  type RecallRequest,
  type RecallResult,
} from "../contract.ts";
import { claim } from "./adapters/index.ts";
import {
  readingCache,
  type ReadingCache,
  type ReadingContext,
  type ReusedReading,
} from "./cache.ts";
import { type RecordSink } from "./output.ts";
import { PREFLIGHT_DETECTORS, secretScan } from "./preflight.ts";
import { PREPARATION_SCHEMA } from "./prepare.ts";
import { clipUtf8, recallRecordReader } from "./recall-records.ts";
import { BABEL_TAG, type ArchivedEntry, type Repo, type Snapshot } from "./restic.ts";
import { sessionDigester } from "./session-records.ts";
import { sessionIndex, SessionIndexError, type IndexedSession } from "./session-index.ts";

const context: ReadingContext = {
  schema: PREPARATION_SCHEMA,
  detectors: PREFLIGHT_DETECTORS,
  mode: "redact",
};
const METADATA_MAX_BYTES = 64 * 1024;
const CachedMetadataSchema = z.strictObject({
  key: z.string().regex(/^[0-9a-f]{64}$/),
  value: RecallMetadataSchema,
});
type Subject = RecallPolicy["subjects"][number];
type Refusal = NonNullable<RecallResult["refusal"]>;
class Refused extends Error {
  constructor(readonly reason: Refusal) {
    super(reason);
  }
}
interface Capture extends IndexedSession {
  host: string;
  snapshot: Snapshot;
  subjects: Subject[];
  cache: ReadingCache;
}
interface Widening {
  classId: string;
  path: string | null;
  expires: number;
  bytes: number;
  records: number;
  hit: RecallHit;
  offset: number;
  line: number;
  previous: { offset: number; hit: RecallHit; page: NonNullable<RecallResult["page"]> } | null;
}
const hash = (value: string): string => new Bun.CryptoHasher("sha256").update(value).digest("hex");
const sameDigests = (
  left: Pick<ReusedReading, "captureDigest" | "sourceDigest">,
  right: Pick<ReusedReading, "captureDigest" | "sourceDigest">,
): boolean =>
  left.captureDigest === right.captureDigest && left.sourceDigest === right.sourceDigest;
const sourceKey = (host: string, selector: string): string => JSON.stringify([host, selector]);
const matchesSubject = (
  subject: Subject,
  host: string,
  harness: string,
  selector: string,
): boolean =>
  subject.host === host &&
  (subject.harness === undefined || subject.harness === harness) &&
  (subject.selectorPrefix === undefined || selector.startsWith(subject.selectorPrefix));

/** Human-readable policy labels share the mandatory scanner, never locator identities. */
function publishedText(value: string, maxCharacters: number): string {
  const text = z.string().parse(JSON.parse(secretScan().redact(JSON.stringify(value), 1)));
  return text.length <= maxCharacters ? text : clipUtf8(text, maxCharacters).text;
}

function publishedMetadata(value: RecallMetadata): RecallMetadata {
  return {
    ...value,
    title: value.title === null ? null : publishedText(value.title, 256),
    workspace: value.workspace === null ? null : publishedText(value.workspace, 2048),
    repository: value.repository === null ? null : publishedText(value.repository, 2048),
  };
}

/** Only the selected repository is consulted. Native routes, not requests, supply classId. */
export interface RecallArchive {
  execute(classId: string, request: RecallRequest): Promise<RecallResult>;
  close(): Promise<void>;
}

export async function createRecallArchive(options: {
  repo: Repo;
  cacheDir: string;
  policy: RecallPolicy;
  /** The native service fixes this to its job-private /tmp, independently of TMPDIR. */
  temporaryDir?: string;
  now?: () => number;
}): Promise<RecallArchive> {
  const policy = RecallPolicySchema.parse(options.policy);
  const now = options.now ?? Date.now;
  const root = join(options.cacheDir, "recall", hash(options.repo.repository));
  await mkdir(root, { recursive: true, mode: 0o700 });
  const index = await sessionIndex(root, context);
  const caches = new Map<string, ReadingCache>();
  // One bounded, rebuildable sidecar per kept session, never transcript bytes in the catalog.
  const tokens = new Map<string, Widening>();
  let staging: string | null = null;
  const previewByteLimit = Math.floor(RECALL_MAX_SERVED_BYTES / policy.classes.length);
  const previewHandleLimit = Math.floor(RECALL_MAX_REQUESTS / policy.classes.length);
  const stagedBytes = new Map(policy.classes.map((entry) => [entry.id, 0]));
  let closed = false;
  const cacheFor = (host: string): ReadingCache => {
    let cache = caches.get(host);
    if (cache === undefined) {
      cache = readingCache(join(root, "hosts", hash(host)), context);
      caches.set(host, cache);
    }
    return cache;
  };
  const metadataKey = (entry: Capture, reading: ReusedReading): string =>
    hash(
      JSON.stringify([
        1,
        context,
        entry.namespace,
        entry.session.selector,
        entry.seen,
        reading.captureDigest,
        reading.sourceDigest,
      ]),
    );
  const recalledMetadata = async (
    key: string,
    reading: ReusedReading,
  ): Promise<RecallMetadata | undefined> => {
    try {
      const bytes = await Bun.file(`${reading.stream}.recall-metadata.json`)
        .slice(0, METADATA_MAX_BYTES + 1)
        .arrayBuffer();
      if (bytes.byteLength > METADATA_MAX_BYTES) return undefined;
      const parsed = CachedMetadataSchema.safeParse(
        JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)),
      );
      return parsed.success && parsed.data.key === key ? parsed.data.value : undefined;
    } catch {
      return undefined;
    }
  };
  const remember = async (
    key: string,
    reading: ReusedReading,
    value: RecallMetadata,
  ): Promise<void> => {
    const document = JSON.stringify(CachedMetadataSchema.parse({ key, value }));
    if (Buffer.byteLength(document) > METADATA_MAX_BYTES) return;
    const path = `${reading.stream}.recall-metadata.json`;
    const temporary = `${path}.${crypto.randomUUID()}`;
    try {
      await Bun.write(temporary, document, { mode: 0o600 });
      await rename(temporary, path);
    } catch {
      // Metadata is a rebuildable optimization, never a condition of source eligibility.
    } finally {
      await rm(temporary, { force: true }).catch(() => undefined);
    }
  };
  const release = async (entry: Widening): Promise<void> => {
    if (entry.path === null) return;
    await rm(entry.path, { force: true });
    entry.path = null;
    stagedBytes.set(entry.classId, stagedBytes.get(entry.classId)! - entry.bytes);
  };
  const expire = async (): Promise<void> => {
    for (const [token, entry] of tokens) {
      if (entry.expires > now()) continue;
      await release(entry);
      tokens.delete(token);
    }
  };
  const association = (entry: Capture, archived: RecallMetadata): RecallMetadata => {
    const associated = entry.subjects.find(
      (subject) => subject.workspace !== undefined || subject.repository !== undefined,
    );
    return associated === undefined
      ? archived
      : {
          title: archived.title,
          workspace: associated.workspace ?? archived.workspace,
          repository: associated.repository ?? null,
          metadataOrigin: "owner-association",
        };
  };
  const verify = async (
    entry: Capture,
    reading: ReusedReading,
    sink: RecordSink,
    result: RecallResult,
  ): Promise<void> => {
    let failed = false;
    let failure: unknown;
    try {
      const digest = await entry.cache.replay(reading, {
        write(chunk) {
          result.cost.replayedBytes +=
            typeof chunk === "string" ? Buffer.byteLength(chunk) : chunk.byteLength;
          if (failed) return;
          try {
            sink.write(chunk);
          } catch (error) {
            failed = true;
            failure = error;
          }
        },
        close: async () => {},
      });
      if (digest !== reading.sourceDigest) {
        await entry.cache.forget(entry.session);
        throw new Refused("capture-changed");
      }
    } catch (error) {
      if (error instanceof Refused) throw error;
      throw new Refused("source-unavailable");
    }
    // Sink errors belong to their consumer. In particular, index.build must see SQLite's
    // typed busy/locked code rather than a source-unavailable wrapper.
    if (failed) throw failure;
    await sink.close();
  };
  const load = async (
    entry: Capture,
    result: RecallResult,
    budget: number,
    fetched: Set<Capture>,
    expected?: Pick<ReusedReading, "captureDigest" | "sourceDigest">,
  ): Promise<ReusedReading> => {
    const reused = await entry.cache.reuse(entry.session, entry.seen);
    if (reused !== null) {
      if (
        reused.bytes === entry.seen.size &&
        (expected === undefined || sameDigests(reused, expected))
      ) {
        result.cost.cacheHits++;
        return reused;
      }
      // Digest expectations come only from a held index, never untrusted locator hashes.
      await entry.cache.forget(entry.session);
    }
    if (fetched.has(entry)) throw new Refused("capture-changed");
    if (entry.seen.size > MAX_MATERIAL_BYTES || entry.seen.size > budget - result.cost.fetchedBytes)
      throw new Refused("fetch-bound");
    const kept = await entry.cache.keep(entry.session, entry.seen);
    if (kept === null) throw new Refused("source-unavailable");
    const scan = secretScan();
    let served = 0;
    const digest = sessionDigester(
      {
        write(chunk) {
          served += typeof chunk === "string" ? Buffer.byteLength(chunk) : chunk.byteLength;
          if (served > RECALL_MAX_SERVED_BYTES) throw new Refused("fetch-bound");
          kept.sink.write(chunk);
        },
        close: async () => {},
      },
      scan,
    );
    let ended = false;
    try {
      fetched.add(entry);
      result.cost.fetchedFiles++;
      await options.repo.dumpTo(
        entry.snapshot.id,
        entry.session.primaryPath,
        (chunk) => {
          result.cost.fetchedBytes += chunk.byteLength;
          digest.write(chunk);
        },
        {
          maxBytes: Math.min(
            entry.seen.size,
            budget - result.cost.fetchedBytes,
            MAX_MATERIAL_BYTES,
          ),
        },
      );
      const reading = { ...digest.finish(), report: scan.report() };
      await kept.sink.close();
      ended = true;
      if (reading.bytes !== entry.seen.size) throw new Refused("capture-changed");
      await kept.commit(reading);
      const saved = await entry.cache.reuse(entry.session, entry.seen);
      if (saved === null) throw new Refused("source-unavailable");
      if (!sameDigests(saved, reading)) {
        await entry.cache.forget(entry.session);
        throw new Refused("capture-changed");
      }
      if (expected !== undefined && !sameDigests(saved, expected)) {
        // The refetched immutable bytes settle a cache/index disagreement. Repair derived
        // index rows with a verified replay, not with any digest supplied by a request.
        let refused: Refused | undefined;
        const built = await index
          .build(
            entry,
            async (sink) => {
              try {
                await verify(entry, saved, sink, result);
                return { reading: saved, after: entry.seen };
              } catch (error) {
                if (error instanceof Refused) refused = error;
                throw error;
              }
            },
            reading,
          )
          .catch((error) => {
            throw refused ?? error;
          });
        if (built === "busy") throw new Refused("index-busy");
        if (built === "changed") throw new Refused("capture-changed");
        if (built === "indexed") result.cost.indexedFiles++;
      }
      return saved;
    } catch (error) {
      if (!ended) await kept.sink.close().catch(() => undefined);
      await kept.abandon();
      if (error instanceof Refused) throw error;
      throw new Refused("source-unavailable");
    }
  };
  const enumerate = async (
    filter: RecallFilter,
    result: RecallResult,
    ceiling: number,
  ): Promise<Capture[]> => {
    const refused = new Set<string>();
    const newest = new Map<string, Capture>();
    const snapshots = (await options.repo.snapshots()).filter(
      (snapshot) =>
        snapshot.tags.includes(BABEL_TAG) &&
        /^[0-9a-f]{64}$/.test(snapshot.id) &&
        Number.isFinite(Date.parse(snapshot.time)) &&
        snapshot.host.length > 0 &&
        snapshot.host.length <= 128 &&
        (filter.host === undefined || filter.host === snapshot.host) &&
        // Unknown hosts cannot acquire authority or disclose freshness through archived metadata.
        policy.subjects.some((subject) => subject.host === snapshot.host),
    );
    snapshots.sort((a, b) => Date.parse(b.time) - Date.parse(a.time) || a.id.localeCompare(b.id));
    result.newestSnapshotAt =
      snapshots[0] === undefined ? null : new Date(snapshots[0].time).toISOString();
    for (const snapshot of snapshots) {
      result.cost.listedSnapshots++;
      const roots = new Set(snapshot.paths.map((path) => path.replace(/\/+$/, "") || "/"));
      const directories = new Set<string>();
      const deferred: ArchivedEntry[] = [];
      const collect = (node: ArchivedEntry, exists: (path: string) => boolean): void => {
        const session = claim(node.path, exists, roots);
        if (
          session === null ||
          session.selector.length > 600 ||
          (filter.harness !== undefined && filter.harness !== session.harness)
        )
          return;
        const key = sourceKey(snapshot.host, session.selector);
        const previous = newest.get(key);
        if (
          previous !== undefined &&
          (previous.snapshot.id !== snapshot.id ||
            previous.session.primaryPath.localeCompare(node.path) <= 0)
        )
          return;
        const subjects = policy.subjects.filter((subject) =>
          matchesSubject(subject, snapshot.host, session.harness, session.selector),
        );
        const modified = Date.parse(node.modifiedAt);
        newest.set(key, {
          host: snapshot.host,
          snapshot,
          session,
          subjects,
          cache: cacheFor(snapshot.host),
          namespace: hash(JSON.stringify([options.repo.repository, snapshot.host])),
          seen: {
            size: node.size,
            modifiedAt:
              Number.isFinite(modified) && modified > 0 ? modified : Date.parse(snapshot.time),
            capture: JSON.stringify([snapshot.id, node.path]),
          },
        });
      };
      await options.repo.lsTo(snapshot.id, (node) => {
        result.cost.listedEntries++;
        if (node.path.length > 4096) return;
        if (node.type === "dir") {
          directories.add(node.path);
          return;
        }
        if (node.type !== "file") return;
        let needsListing = false;
        collect(node, () => {
          needsListing = true;
          return false;
        });
        if (needsListing) deferred.push(node);
      });
      // A history file can precede its sibling directory in restic's listing. No adapter
      // consults this service host's filesystem, and listing order cannot decide identity.
      for (const node of deferred) collect(node, (path) => directories.has(path));
    }
    const eligible: Capture[] = [];
    for (const entry of newest.values()) {
      if (entry.subjects.length === 0) continue;
      const sensitivity = Math.max(...entry.subjects.map((subject) => subject.sensitivity));
      if (sensitivity > ceiling) {
        for (const subject of entry.subjects)
          if (subject.sensitivity > ceiling) refused.add(subject.name);
      } else eligible.push(entry);
    }
    result.refusedSubjects = [...refused];
    return eligible;
  };
  const locator = (
    entry: Capture,
    reading: ReusedReading,
    record: RecallLocator["record"],
  ): RecallLocator => ({
    coordinates: SESSION_RECORD_COORDINATES,
    host: entry.host,
    harness: entry.session.harness,
    session: entry.session.selector,
    snapshot: entry.snapshot.id,
    path: entry.session.primaryPath,
    captureDigest: reading.captureDigest,
    sourceDigest: reading.sourceDigest,
    record,
  });
  const readHit = async (
    entry: Capture,
    reading: ReusedReading,
    target: RecallLocator,
    result: RecallResult,
    selection?: Extract<RecallRequest, { kind: "show" }>["selection"],
    maxBytes = RECALL_SEARCH_EXCERPT_BYTES,
  ): Promise<RecallHit> => {
    if (
      reading.captureDigest !== target.captureDigest ||
      reading.sourceDigest !== target.sourceDigest
    )
      throw new Refused("locator-mismatch");
    const reader = recallRecordReader({
      harness: entry.session.harness,
      anchor: target.record,
      maxBytes,
      ...(selection === undefined ? {} : { selection }),
    });
    await verify(entry, reading, reader.sink, result);
    const readback = await reader.finish();
    if (!readback.anchorMatches) throw new Refused("locator-mismatch");
    if (selection?.kind === "turns" && !readback.turnsSupported)
      throw new Refused("unsupported-turns");
    return {
      locator: target,
      snapshotAt: new Date(entry.snapshot.time).toISOString(),
      ...publishedMetadata(association(entry, readback.metadata)),
      excerpt: readback.excerpt,
    };
  };
  const bounded = (result: RecallResult): RecallResult => {
    result.refusedSubjects = result.refusedSubjects.map((name) => publishedText(name, 120));
    while (Buffer.byteLength(JSON.stringify(result)) > RECALL_MAX_PAYLOAD_BYTES) {
      if (result.hits.length > 0) {
        result.hits.pop();
        result.omitted++;
      } else if (result.refusedSubjects.length > 0) {
        result.refusedSubjects.pop();
        result.omittedSubjects++;
        result.refusal = "response-bound";
      } else {
        delete result.preview;
        delete result.page;
        result.refusal = "response-bound";
        break;
      }
    }
    return result;
  };

  return {
    async execute(classId, input) {
      const result: RecallResult = {
        operation: input.kind,
        observedAt: new Date(now()).toISOString(),
        newestSnapshotAt: null,
        previewByteLimit,
        cost: {
          fetchedFiles: 0,
          fetchedBytes: 0,
          replayedBytes: 0,
          cacheHits: 0,
          indexedFiles: 0,
          listedSnapshots: 0,
          listedEntries: 0,
        },
        coverage: { eligible: 0, indexed: 0, complete: false, overBound: 0 },
        matches: null,
        omitted: 0,
        omittedSubjects: 0,
        refusedSubjects: [],
        refusal: null,
        hits: [],
      };
      const fetched = new Set<Capture>();
      try {
        if (closed) throw new Refused("archive-unavailable");
        const request = RecallRequestSchema.parse(input);
        const disclosure = policy.classes.find((entry) => entry.id === classId);
        if (disclosure === undefined) throw new Refused("disclosure");
        await expire();
        if (request.kind === "session") {
          const token = tokens.get(request.previewId);
          if (token === undefined) throw new Refused("preview-expired");
          if (token.classId !== classId) throw new Refused("disclosure");
          if (token.previous?.offset === request.offset) {
            if (token.previous.hit.excerpt.bytes > request.maxBytes)
              throw new Refused("invalid-offset");
            result.hits = [token.previous.hit];
            result.page = token.previous.page;
          } else {
            if (request.offset !== token.offset) throw new Refused("invalid-offset");
            // Reserve worst-case JSON escaping before advancing: a page can never disappear
            // under the serialized response bound after its offset has been consumed.
            const overhead =
              Buffer.byteLength(JSON.stringify({ ...result, hits: [token.hit] })) + 2048;
            const pageBytes = Math.min(
              request.maxBytes,
              Math.floor((RECALL_MAX_PAYLOAD_BYTES - overhead) / 6),
            );
            if (pageBytes < 4) throw new Refused("response-bound");
            const bytes =
              token.path === null
                ? new Uint8Array(0)
                : new Uint8Array(
                    await Bun.file(token.path)
                      .slice(token.offset, Math.min(token.bytes, token.offset + pageBytes + 3))
                      .arrayBuffer(),
                  );
            const clipped = clipUtf8(
              new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
                bytes.subarray(0, utf8End(bytes, Math.min(pageBytes, bytes.length))),
              ),
              pageBytes,
            );
            if (clipped.bytes === 0 && token.offset < token.bytes)
              throw new Refused("source-unavailable");
            const start = token.line;
            let newlines = 0;
            for (let at = 0; at < clipped.text.length; at++)
              if (clipped.text.charCodeAt(at) === 10) newlines++;
            const next = token.offset + clipped.bytes;
            const hit: RecallHit = {
              ...token.hit,
              excerpt: {
                trust: "archived-untrusted",
                begin: RECALL_UNTRUSTED_BEGIN,
                end: RECALL_UNTRUSTED_END,
                text: clipped.text,
                maxBytes: request.maxBytes,
                bytes: clipped.bytes,
                truncated: next < token.bytes,
                firstRecord: clipped.bytes === 0 ? 0 : start,
                lastRecord:
                  clipped.bytes === 0
                    ? 0
                    : start + newlines - (clipped.text.endsWith("\n") ? 1 : 0),
              },
            };
            const page = {
              offset: token.offset,
              nextOffset: next,
              totalBytes: token.bytes,
              complete: next === token.bytes,
            };
            // Check the exact final shape before consuming the sequential offset.
            if (
              Buffer.byteLength(
                JSON.stringify({
                  ...result,
                  hits: [hit],
                  page,
                  newestSnapshotAt: token.hit.snapshotAt,
                  coverage: { eligible: 1, indexed: 1, complete: true, overBound: 0 },
                }),
              ) > RECALL_MAX_PAYLOAD_BYTES
            )
              throw new Refused("response-bound");
            // A terminal-offset probe must not replace the retained final content page.
            if (token.path !== null) {
              if (page.complete) await release(token);
              token.previous = { offset: token.offset, hit, page };
              if (next > token.offset) token.expires = now() + RECALL_REQUEST_TTL_MS;
              token.offset = next;
              token.line += newlines;
            }
            result.hits = [hit];
            result.page = page;
          }
          result.newestSnapshotAt = token.hit.snapshotAt;
          result.coverage = { eligible: 1, indexed: 1, complete: true, overBound: 0 };
          return bounded(result);
        }
        if (request.kind !== "search") {
          const target = request.locator;
          const rules = policy.subjects.filter((subject) =>
            matchesSubject(subject, target.host, target.harness, target.session),
          );
          if (rules.length === 0) throw new Refused("unclassified");
          if (Math.max(...rules.map((rule) => rule.sensitivity)) > disclosure.ceiling) {
            result.refusedSubjects = rules
              .filter((rule) => rule.sensitivity > disclosure.ceiling)
              .map((rule) => rule.name);
            throw new Refused("disclosure");
          }
        }
        let entries: Capture[];
        try {
          entries = await enumerate(
            request.kind === "search"
              ? request.filter
              : {
                  host: request.locator.host,
                  harness: request.locator.harness,
                },
            result,
            disclosure.ceiling,
          );
        } catch {
          throw new Refused("archive-unavailable");
        }
        if (request.kind === "search") {
          result.coverage.eligible = entries.length;
          const covered: Capture[] = [];
          const readings = new Map<Capture, ReusedReading>();
          // Missing captures first: a finite cold budget makes progress on subsequent requests.
          entries.sort((a, b) => Number(index.holds(a)) - Number(index.holds(b)));
          for (const entry of entries) {
            for (let attempt = 0; attempt < 2; attempt++) {
              try {
                if (entry.seen.size > MAX_MATERIAL_BYTES) throw new Refused("fetch-bound");
                const reading = await load(entry, result, request.maxFetchBytes, fetched);
                const key = metadataKey(entry, reading);
                let about = await recalledMetadata(key, reading);
                if (!index.holds(entry)) {
                  const reader = recallRecordReader({ harness: entry.session.harness });
                  let refused: Refused | undefined;
                  const built = await index
                    .build(entry, async (sink) => {
                      try {
                        await verify(
                          entry,
                          reading,
                          {
                            write(chunk) {
                              sink.write(chunk);
                              reader.sink.write(chunk);
                            },
                            close: async () => {
                              await sink.close();
                              await reader.sink.close();
                            },
                          },
                          result,
                        );
                        return { reading, after: entry.seen };
                      } catch (error) {
                        if (error instanceof Refused) refused = error;
                        throw error;
                      }
                    })
                    .catch((error) => {
                      throw refused ?? error;
                    });
                  if (built === "busy") throw new Refused("index-busy");
                  if (built === "changed") throw new Refused("capture-changed");
                  if (built === "indexed") {
                    result.cost.indexedFiles++;
                    about = (await reader.finish()).metadata;
                    await remember(key, reading, about);
                  }
                }
                if (about === undefined) {
                  const reader = recallRecordReader({ harness: entry.session.harness });
                  await verify(entry, reading, reader.sink, result);
                  about = (await reader.finish()).metadata;
                  await remember(key, reading, about);
                }
                result.coverage.indexed++;
                const associated = association(entry, about);
                if (
                  (request.filter.workspace !== undefined &&
                    request.filter.workspace !== associated.workspace) ||
                  (request.filter.repository !== undefined &&
                    request.filter.repository !== associated.repository)
                )
                  break;
                covered.push(entry);
                readings.set(entry, reading);
                break;
              } catch (error) {
                if (
                  attempt === 0 &&
                  error instanceof Refused &&
                  error.reason === "capture-changed" &&
                  index.holds(entry) &&
                  !fetched.has(entry)
                )
                  continue;
                if (error instanceof Refused && error.reason === "fetch-bound")
                  result.coverage.overBound++;
                else result.refusal = safeReason(error);
                break;
              }
            }
          }
          result.coverage.complete = result.coverage.indexed === result.coverage.eligible;
          const found = index.searchRecords(
            request.query,
            covered,
            Math.min(request.limit, RECALL_MAX_HITS),
            {
              ...(request.filter.since === undefined ? {} : { since: request.filter.since }),
              ...(request.filter.until === undefined ? {} : { until: request.filter.until }),
            },
          );
          result.matches = found.matches;
          for (const foundHit of found.hits) {
            const entry = foundHit.candidate as Capture;
            let reading = readings.get(entry);
            if (reading === undefined) throw new Refused("capture-changed");
            try {
              if (!sameDigests(reading, foundHit)) {
                reading = await load(entry, result, request.maxFetchBytes, fetched, foundHit);
                readings.set(entry, reading);
              }
              const target = locator(entry, reading, foundHit.position);
              result.hits.push(await readHit(entry, reading, target, result));
            } catch (error) {
              result.refusal = safeReason(error);
              result.coverage.complete = false;
            }
          }
          result.omitted = found.matches - result.hits.length;
        } else {
          const target = request.locator;
          const entry = entries.find(
            (candidate) =>
              candidate.host === target.host && candidate.session.selector === target.session,
          );
          if (
            entry === undefined ||
            entry.snapshot.id !== target.snapshot ||
            entry.session.primaryPath !== target.path ||
            entry.session.harness !== target.harness
          )
            throw new Refused("locator-mismatch");
          result.coverage.eligible = 1;
          if (entry.seen.size > MAX_MATERIAL_BYTES) throw new Refused("fetch-bound");
          const held = index.digests(entry);
          // Locator hashes are claims, not cache invalidation authority. Without a held index,
          // a warm reading can still be verified and compared, but never evicted for that claim.
          const reading = await load(entry, result, MAX_MATERIAL_BYTES, fetched, held ?? undefined);
          if (request.kind === "show") {
            result.hits = [
              await readHit(entry, reading, target, result, request.selection, request.maxBytes),
            ];
          } else {
            // No content leaves this branch. Token publication follows the whole replay's digest.
            if (
              reading.captureDigest !== target.captureDigest ||
              reading.sourceDigest !== target.sourceDigest
            )
              throw new Refused("locator-mismatch");
            let owned = 0;
            let completed: string | undefined;
            for (const [token, widening] of tokens) {
              if (widening.classId !== classId) continue;
              owned++;
              if (completed === undefined && widening.path === null) completed = token;
            }
            if (owned >= previewHandleLimit && completed === undefined)
              throw new Refused("fetch-bound");
            // The native service supplies its private /tmp tmpfs: Manifold 89b065d,
            // packages/agent/src/job-linux.ts:229-230,742-747. That mount disappears with the
            // job, even on a crash. Never sweep another service's staging or durable cache.
            staging ??= await mkdtemp(
              join(options.temporaryDir ?? tmpdir(), "babel-recall-widening-"),
            );
            await chmod(staging, 0o700);
            const token = crypto.randomUUID();
            const path = join(staging, token);
            const writer = Bun.file(path).writer();
            const reader = recallRecordReader({
              harness: entry.session.harness,
              anchor: target.record,
              maxBytes: 1,
            });
            let bytes = 0;
            let ended = false;
            try {
              await verify(
                entry,
                reading,
                {
                  write(chunk) {
                    bytes +=
                      typeof chunk === "string" ? Buffer.byteLength(chunk) : chunk.byteLength;
                    if (bytes > previewByteLimit - stagedBytes.get(classId)!)
                      throw new Refused("fetch-bound");
                    writer.write(chunk);
                    reader.sink.write(chunk);
                  },
                  close: async () => {
                    await writer.end();
                    ended = true;
                    await reader.sink.close();
                  },
                },
                result,
              );
              const readback = await reader.finish();
              if (!readback.anchorMatches) throw new Refused("locator-mismatch");
              const hit: RecallHit = {
                locator: target,
                snapshotAt: new Date(entry.snapshot.time).toISOString(),
                ...publishedMetadata(association(entry, readback.metadata)),
                excerpt: {
                  ...readback.excerpt,
                  text: "",
                  bytes: 0,
                  firstRecord: 0,
                  lastRecord: 0,
                  truncated: false,
                },
              };
              // Completed handles are replay cache entries, not a lifetime admission quota.
              if (owned >= previewHandleLimit && completed !== undefined) tokens.delete(completed);
              tokens.set(token, {
                classId,
                path,
                expires: now() + RECALL_REQUEST_TTL_MS,
                bytes,
                records: readback.records,
                hit,
                offset: 0,
                line: 1,
                previous: null,
              });
              stagedBytes.set(classId, stagedBytes.get(classId)! + bytes);
              result.preview = {
                previewId: token,
                sourceBytes: reading.bytes,
                servedBytes: bytes,
                records: readback.records,
                sourceDigest: reading.sourceDigest,
              };
            } catch (error) {
              try {
                if (!ended) await writer.end();
              } catch {
                // Always unlink an unpublished widening, even when its writer failed.
              } finally {
                await rm(path, { force: true });
              }
              throw error;
            }
          }
          result.coverage.indexed = 1;
          result.coverage.complete = true;
        }
      } catch (error) {
        result.refusal = safeReason(error);
        result.hits = [];
        delete result.preview;
        delete result.page;
      }
      return bounded(result);
    },
    async close() {
      if (closed) return;
      closed = true;
      try {
        index.close();
      } finally {
        tokens.clear();
        stagedBytes.clear();
        if (staging !== null) await rm(staging, { recursive: true, force: true });
      }
    },
  };
}

function safeReason(error: unknown): Refusal {
  if (error instanceof Refused) return error.reason;
  if (error instanceof SessionIndexError && error.kind === "busy") return "index-busy";
  return "source-unavailable";
}

/** End before any UTF-8 codepoint split by the requested byte boundary. */
function utf8End(bytes: Uint8Array, bound: number): number {
  let end = bound;
  while (end > 0 && end < bytes.length && ((bytes[end] ?? 0) & 0xc0) === 0x80) end--;
  return end;
}
