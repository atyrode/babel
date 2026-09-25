/*
  THE prepare OPERATION (plan §4, #453): the captures the hub selected in, a preparation out —
  an immutable statement of one exploration's corpus scope, whose identity is DERIVED from the
  content of the selection rather than assigned.

  That is what makes `explore --preparation <id>` mean something: naming a preparation states
  which corpus a run read. Two preparations over the same captures are the same id, whichever
  machine prepared them, so two runs over one scope look like two runs over one scope; a session
  captured again with other bytes is a different id, because it is a different corpus.

  DELIBERATE DIFFERENCE FROM THE GO PRODUCT (v0.4.0:internal/run/preparation.go): `preparedAt` is
  recorded but NOT hashed. Go's derivation included the instant, so re-preparing an unchanged
  corpus minted a second identity for the same scope — which contradicts the idempotence that
  same-scope-same-id is for. Here the id is a function of the selection alone.

  WHAT IS READ, AND WHAT IS NOT (#453). A preparation reads the ARCHIVE and nothing else: the hub
  names each capture — the snapshot, the path inside it, the host label it was taken under, and
  the size and modification time the catalog recorded — and each is streamed out of restic with
  `dump`, straight into the single pass below. Nothing is discovered and no local session file is
  opened, so the machine a preparation runs on changes nothing about what it reads; there is no
  fallback to a local file when the archive does not answer. Raw bytes exist only in this
  process: what is kept is the redacted normalized stream.

  A preparation refuses WHOLE, with a code from PREPARE_REFUSALS leading its reason: a material
  that would not fit (`material_bound`, `material_storage_insufficient`, both decided before
  anything is fetched), a snapshot or path the archive does not hold (`capture_missing`), a fetch
  that is not the catalogued size (`capture_changed`), or an archive that cannot be opened or
  does not answer (`archive_unavailable`). A scope that quietly shrank would be worse than a
  refusal: it would be an immutable record of a corpus nobody chose. Babel's own run transcripts
  are refused for the same reason unless `agentSessions` asks for them, because an exploration's
  subject is the operator's work.

  Each entry carries both digests SPEC §7 requires of a selection: the CAPTURE digest over the
  archived bytes, which is what a restore is checked against, and the SOURCE digest over the
  normalized record stream, which is what analysis reads. A harness that rewrites its log with
  different spacing moves the first and not the second, and that difference is what a later
  reviewer needs in order to tell "the corpus changed" from "our reading of it changed". The
  entry's host is the capture's LABEL — the machine that recorded it — and never the machine
  that prepared it.

  WHAT A SECOND PREPARATION OVER THE SAME CAPTURES DOES NOT DO AGAIN (#236). Reading a capture is
  this operation's whole cost, and the answer does not depend on which run asked. So the reading
  is KEPT in the managed cache, one directory per repository and label, keyed on the capture it
  was derived from — snapshot, path, size, modification time, normalization, detector set,
  preflight mode — and a later pass over the same capture replays it and contacts no archive
  (`machine/cache.ts`). A capture never moves, so every reading is keepable. When each snapshot
  was taken is kept beside the readings, because it never changes either and every row states it.

  Normalization remains one canonical JSON record per line — object keys ordered, insignificant
  whitespace gone — with an explicit opaque marker for a line that is not a record, so nothing
  is dropped. The same pass folds what the capture says about itself — the title the harness
  recorded, the workspace, the harness's own usage — out of the redacted stream
  (`machine/session-facts.ts`), and the `sessions.json` rows carry it with the capture it
  describes.

  THE MATERIAL IS SCANNED BEFORE IT IS SEALED (#339, SPEC §6.4). `machine/preflight.ts` replaces
  every likely-credential span with a marker naming its class and the locator of the original,
  and the pass is the same one: the redacted record is what the source digest covers and what the
  sink receives, so a scan cannot disagree with the bytes a session was given. A preparation may
  also refuse the whole scope over what was found, by class and never by value, and the receipt
  carries the result either way — an absent report says nothing was scanned, never that a corpus
  was clean. The only way back to a redacted value is `resolveRedaction` over the capture's own
  bytes, which only a job holding the archive binding can fetch, so what crosses to the hub is a
  locator and a class.
*/

import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { z } from "zod";
import {
  ArchiveLabelSchema,
  CaptureInstantSchema,
  MATERIAL_SCHEMA,
  MATERIAL_RETRIEVAL,
  MaterialRetrievalSchema,
  MAX_MATERIAL_BYTES,
  PREFLIGHT_SCHEMA,
  PREPARE_REFUSALS,
  RECALL_MAX_HITS,
  RECALL_MAX_RESULT_BYTES,
  RUN_STAGES,
  materialFile,
  termsQuery,
  type MaterialEntry,
  type MaterialIndex,
  type MaterialRetrieval,
  type PreflightMode,
  type PreflightReport,
  type PrepareInput,
  type PrepareRefusal,
  type Receipt,
  type SessionContentQuery,
  type SessionRetrieval,
  type SessionRow,
} from "../contract.ts";
import { babelOwnLog, sessionRef, validSourceId, type SessionRef } from "./adapters/index.ts";
import { captureInstant } from "./archive-listing.ts";
import { readingCache, type Observation, type ReadingCache, type ReadingContext } from "./cache.ts";
import { teeRecords, type MaterialSink, type OutputSink, type RecordSink } from "./output.ts";
import {
  PREFLIGHT_DETECTORS,
  refusalMessage,
  secretScan,
  type ScanReport,
  type SecretScan,
} from "./preflight.ts";
import { SILENT, type ProgressChannel } from "./progress.ts";
import { recallRecordReader } from "./recall-records.ts";
import { ResticError, type Repo } from "./restic.ts";
import { captureFacts } from "./session-facts.ts";
import {
  sessionIndex,
  SessionIndexError,
  type IndexedRecord,
  type IndexedSession,
  type SessionIndex,
} from "./session-index.ts";
import { recordReader, sessionDigester, type SessionDigests } from "./session-records.ts";

/**
 * The one binding this operation reads from the environment, for the reason `VERIFY_ENV`'s is a
 * fixed, reviewed, non-secret path inside a location the manifest declares writable — here the
 * managed `atyrode.babel.cache`, shared by every `prepare` job on the machine, which is what
 * makes one reading serve twenty concurrent explorations (#236).
 *
 * Absent — a hand-run outside a job, the suites — keeps nothing: a cache a hand-run invented
 * under the system's temporary directory would be a surprise rather than a saving.
 */
export const PREPARE_ENV = {
  cacheDir: "BABEL_PREPARE_CACHE_DIR",
} as const;

/** What a preparation asks of the archive: the snapshots it names, and one capture's bytes —
 *  with the listing of one path to tell a capture that is missing from an archive that failed. */
export type PrepareRepo = Pick<Repo, "snapshots" | "dumpTo" | "ls">;

/** The job bindings this operation needs; the dispatcher owns them (machine/main.ts). */
export interface PrepareDeps {
  /**
   * The repository this job's service binding opens. It is asked for on the first capture no
   * kept reading holds, or the first snapshot whose time the machine does not remember: a
   * preparation served whole from kept readings contacts no archive at all. A failure to open
   * it refuses the preparation as `archive_unavailable`, and nothing local is read instead.
   */
  archive(): Promise<PrepareRepo>;
  /** The locator of the repository the binding names, which kept readings are filed under.
   *  Asked only when there is a cache to file them in. */
  repository(): Promise<string>;
  /** The material lease's capacity, or null where it cannot be measured. */
  capacity(): Promise<NonNullable<Receipt["outputCapacity"]> | null>;
  /**
   * Where the material is sealed: the second output lease (`machine/output.ts`). Null for a
   * hand-run that bound none, which prepares a selection and seals no evidence — the receipt
   * says which, so a run whose material nothing can read is never mistaken for one that has it.
   */
  material?: MaterialSink | null | undefined;
  /** Where this run says it is; a caller that hands none is not watched (`progress.ts`). */
  progress?: ProgressChannel | undefined;
  /**
   * Where readings are kept between preparations (`machine/cache.ts`, #236). Empty for an
   * invocation that was given no such directory — a hand-run, the tests that pass none — which
   * fetches every capture it selects, every time, and says so as `counts.reused` 0.
   */
  cacheDir?: string | undefined;
}

/** The version of the preparation record's shape AND of the normalization behind its source
 *  digest. It participates in the derivation, so a record written by a later schema can never
 *  collide with one written by this schema even if every other field matches. */
export const PREPARATION_SCHEMA = 3;

/** Separates this hash from every other use of SHA-256 in Babel: without a domain, a digest
 *  over some other structure that happened to serialize identically would be a valid
 *  preparation id. The `v3` is PREPARATION_SCHEMA's own, moved with it. */
const PREPARATION_DOMAIN = "babel/preparation/v3";

/** Marks a preparation id as one, so a mistyped identifier fails as the wrong kind of id
 *  rather than as a missing row. */
const PREPARATION_PREFIX = "prep-";

/** One session inside a preparation, identified the way every session is: the machine that
 *  recorded it — the capture's host label — the harness, and the adapter-defined source id. */
export type PreparationEntry = {
  readonly host: string;
  readonly harness: string;
  readonly sourceId: string;
  readonly captureDigest: string;
  readonly sourceDigest: string;
};

export type Preparation = {
  readonly id: string;
  readonly schema: number;
  /** When the scope was fixed. Recorded, never hashed — see the header. */
  readonly preparedAt: string;
  /** Canonically ordered, so neither the input's order nor a caller's later mutation can
   *  change what the record means. */
  readonly selection: readonly PreparationEntry[];
};

/**
 * Fixes a corpus scope and derives its identity.
 *
 * An empty selection is refused: an exploration over nothing is a mistake, and accepting it
 * would make a broken selection indistinguishable from a deliberate one. So is the same
 * session twice, which would let a scope claim a weight it does not have.
 */
export function newPreparation(
  preparedAt: string,
  selection: readonly PreparationEntry[],
): Preparation {
  if (selection.length === 0) throw new Error("preparation: the selection is empty");
  const canonical = [...selection].sort((a, b) => {
    if (a.host !== b.host) return a.host < b.host ? -1 : 1;
    if (a.harness !== b.harness) return a.harness < b.harness ? -1 : 1;
    if (a.sourceId === b.sourceId) return 0;
    return a.sourceId < b.sourceId ? -1 : 1;
  });
  for (const [index, entry] of canonical.entries()) {
    if (entry.host === "" || entry.harness === "" || entry.sourceId === "") {
      throw new Error(`preparation: selection entry ${index} names no session`);
    }
    if (entry.captureDigest === "") {
      throw new Error(`preparation: selection entry ${index} has no capture digest`);
    }
    const previous = canonical[index - 1];
    if (
      previous !== undefined &&
      previous.host === entry.host &&
      previous.harness === entry.harness &&
      previous.sourceId === entry.sourceId
    ) {
      throw new Error(`preparation: the selection holds ${entry.harness}/${entry.sourceId} twice`);
    }
  }
  return { id: derive(canonical), schema: PREPARATION_SCHEMA, preparedAt, selection: canonical };
}

/**
 * The id, as `prep-<64 lowercase hex>`.
 *
 * The hashed encoding is injective, which is what makes the id a function of the content
 * rather than of its punctuation:
 *
 *   PREPARATION_DOMAIN || u32(schema) || u32(entries)
 *     then per entry, in canonical order:
 *       lp(host) || lp(harness) || lp(sourceId) || lp(captureDigest) || lp(sourceDigest)
 *
 * where lp(s) is a four-byte big-endian length followed by s's UTF-8 bytes and u32 is four big
 * endian bytes. Every variable-length field is length-prefixed and every repetition is
 * count-prefixed, so no two distinct selections encode to the same bytes — plain concatenation
 * would let a host of "a" with a harness of "bc" collide with a host of "ab" and a harness
 * of "c".
 */
function derive(selection: readonly PreparationEntry[]): string {
  const hasher = new Bun.CryptoHasher("sha256");
  hasher.update(PREPARATION_DOMAIN);
  writeU32(hasher, PREPARATION_SCHEMA);
  writeU32(hasher, selection.length);
  for (const entry of selection) {
    writeLP(hasher, entry.host);
    writeLP(hasher, entry.harness);
    writeLP(hasher, entry.sourceId);
    writeLP(hasher, entry.captureDigest);
    writeLP(hasher, entry.sourceDigest);
  }
  return PREPARATION_PREFIX + hasher.digest("hex");
}

/** One reusable four-byte frame for the length prefixes: `update` copies synchronously and
 *  nothing awaits between the write and the read, so one buffer serves every derivation. */
const U32_BYTES = new Uint8Array(4);
const U32 = new DataView(U32_BYTES.buffer);

function writeU32(hasher: Bun.CryptoHasher, value: number): void {
  U32.setUint32(0, value, false);
  hasher.update(U32_BYTES);
}

function writeLP(hasher: Bun.CryptoHasher, value: string): void {
  const bytes = new TextEncoder().encode(value);
  writeU32(hasher, bytes.length);
  hasher.update(bytes);
}

/** What a redaction's locator recovers, and the digest saying it was recovered from the same
 *  bytes the preparation read. */
export interface ResolvedRedaction {
  /** The value that was redacted. It exists only in the process that resolved it. */
  readonly value: string;
  /** The capture digest of the bytes it was resolved against. A reader compares it against the
   *  material index's entry for this session: equal means the offsets address what they
   *  addressed, different means these are not the bytes the preparation read. */
  readonly captureDigest: string;
}

/**
 * WHAT A REDACTION'S LOCATOR RESOLVES AGAINST (#339, #453).
 *
 * A marker in the material names a class, a record and a range and carries no value; this is the
 * only way back to the bytes, and it needs the capture's own bytes — which only a job holding
 * the archive binding can fetch, by streaming the capture out of restic with `dump` into this
 * function. So a receipt, a run row and a refusal can all say exactly what was found and where
 * without any of them carrying a credential: the hub holds locators, and the door back to a
 * value is the archive's.
 *
 * The record is re-normalized rather than read out of the material, because the material holds
 * the redacted stream: the value is gone from it by design. Null when the stream holds no such
 * record, or when the range is not inside it — both of which mean it is not the stream the
 * preparation read, and the capture digest is how a caller confirms that.
 */
export async function resolveRedaction(
  stream: AsyncIterable<Uint8Array>,
  site: { readonly line: number; readonly offset: number; readonly length: number },
): Promise<ResolvedRedaction | null> {
  const capture = new Bun.CryptoHasher("sha256");
  let found = "";
  const reader = recordReader((normalized, line) => {
    if (line !== site.line) return;
    found = normalized.endsWith("\n") ? normalized.slice(0, -1) : normalized;
  });
  for await (const chunk of stream) {
    capture.update(chunk);
    reader.write(chunk);
  }
  reader.finish();
  const captureDigest = `sha256:${capture.digest("hex")}`;
  const end = site.offset + site.length;
  if (found === "" || end > found.length) return null;
  return { value: found.slice(site.offset, end), captureDigest };
}

/** One capture, as this preparation reads it. */
interface Capture {
  readonly label: string;
  readonly snapshotId: string;
  readonly ref: SessionRef;
  readonly size: number;
  /** Epoch milliseconds, as the catalog recorded the archived file's own modification time. */
  readonly modifiedAt: number;
  /** The observation a kept reading of it is filed under (`machine/cache.ts`): immutable
   *  capture metadata, never a `stat` of anything on this machine. */
  readonly seen: Observation;
}

/** A preparation refused whole. Its message is the receipt's reason: the refusal's code first
 *  when it is one of PREPARE_REFUSALS, then the sentence. */
class Refused extends Error {
  constructor(code: PrepareRefusal | null, sentence: string) {
    super(code === null ? sentence : `${code}: ${sentence}`);
    this.name = "Refused";
  }
}

/** What restic said, as a refusal of the archive. A failure that is not restic's is not the
 *  archive's either, and is returned as it is. */
function unavailable(error: unknown): unknown {
  if (!(error instanceof ResticError)) return error;
  return new Refused(
    PREPARE_REFUSALS.archive,
    error.stderr === "" ? error.message : `${error.message}: ${error.stderr}`,
  );
}

/**
 * WHAT ONE MATERIAL NEEDS OF THE LEASE, before anything is fetched: the catalogued bytes, one
 * 512-byte member per session and for the index, the retrieval sidecar and the receipt, and a
 * MiB of room for the documents themselves.
 */
function materialNeed(bytes: number, sessions: number): number {
  return bytes + 512 * (sessions + 3) + (1 << 20);
}

/** A kept snapshot time, as it lies on disk. */
const SnapshotMemorySchema = z.strictObject({
  label: ArchiveLabelSchema,
  archivedAt: CaptureInstantSchema,
});

const sha256 = (value: string): string =>
  new Bun.CryptoHasher("sha256").update(value).digest("hex");

export async function prepare(
  input: PrepareInput,
  out: OutputSink,
  deps: PrepareDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const counts = {
    /** Sessions the input named. */
    offered: 0,
    selected: 0,
    bytes: 0,
    records: 0,
    /** Babel's own run transcripts a content query left out of its eligible set. */
    agent: 0,
    /** Spans the secret preflight replaced, over every session in the scope (#339). */
    redacted: 0,
    /**
     * Sessions served from a reading this machine already had (#236). It is on the receipt
     * because it is the only place the saving is visible: two preparations over one scope cost
     * the same wall-clock to an operator watching them, and this is what says the second one
     * did not fetch the corpus again.
     */
    reused: 0,
    /** Captures streamed out of the archive by this run, and their bytes. */
    fetched: 0,
    fetchedBytes: 0,
  };
  /** What each session's scan found, folded into the receipt's report at the end. */
  const scans: { readonly selector: string; readonly report: ScanReport }[] = [];
  /** The material's own index, built as the loop seals each session's stream. */
  const sealed: MaterialEntry[] = [];
  const rows: SessionRow[] = [];
  const retrievalHits: MaterialRetrieval["hits"] = [];
  let preparation: Preparation | null = null;
  let queried: ContentSelection | null = null;
  let closure: Receipt["closure"] = "completed";
  let reason = "";
  const retrieval: SessionRetrieval | undefined =
    input.query === undefined
      ? undefined
      : {
          query: {
            digest: `sha256:${new Bun.CryptoHasher("sha256").update(termsQuery(input.query.text)).digest("hex")}`,
            limit: input.query.limit,
          },
          status: "complete",
          eligible: 0,
          indexed: 0,
          reused: 0,
          unavailable: 0,
          matches: null,
          overBound: 0,
        };
  const progress = deps.progress ?? SILENT;
  progress.report({
    stage: RUN_STAGES.preparing,
    message: "checking the captures the hub selected",
  });
  const capacity = await deps.capacity();

  /** The archive, opened once and only when something must be read out of it. */
  let opened: Promise<PrepareRepo> | null = null;
  const archive = async (): Promise<PrepareRepo> => {
    opened ??= deps.archive();
    try {
      return await opened;
    } catch (error) {
      throw unavailable(error);
    }
  };
  /** Streams one capture into a digester of `into`, and counts what crossed. */
  const fetch = async (
    capture: Capture,
    into: RecordSink | undefined,
    scan: SecretScan | undefined,
  ): Promise<SessionDigests> => {
    const repo = await archive();
    const digester = sessionDigester(into, scan);
    counts.fetched++;
    let fetched: { bytes: number };
    try {
      fetched = await repo.dumpTo(
        capture.snapshotId,
        capture.ref.primaryPath,
        (chunk) => {
          counts.fetchedBytes += chunk.byteLength;
          digester.write(chunk);
        },
        { maxBytes: capture.size },
      );
    } catch (error) {
      throw await fetchFailure(repo, capture, error);
    }
    const measured = digester.finish();
    if (fetched.bytes !== capture.size) {
      throw new Refused(
        PREPARE_REFUSALS.changed,
        `${capture.ref.selector} is ${String(fetched.bytes)} bytes in snapshot ` +
          `${capture.snapshotId} and was catalogued at ${String(capture.size)}`,
      );
    }
    return measured;
  };

  try {
    const offered = captures(input);
    counts.offered = offered.length;
    const invalid = offered.find(
      (capture) => !validSourceId(capture.ref.sourceId) || capture.ref.primaryPath.includes("\0"),
    );
    if (invalid !== undefined) {
      throw new Refused(null, `${invalid.ref.selector} is not a session a preparation can name`);
    }
    // A capture NAMED for a scope and excluded from it is refused, never dropped: what was
    // asked for is not what would be prepared. A content query only offers candidates, so there
    // Babel's own transcripts are left out of what is eligible and counted.
    const own = (capture: Capture): boolean =>
      !input.agentSessions && babelOwnLog(capture.ref.primaryPath);
    const named = input.query === undefined ? offered.find(own) : undefined;
    if (named !== undefined) {
      throw new Refused(
        null,
        `${named.ref.selector} is one of Babel's own runs' transcripts, which a preparation of ` +
          `the operator's work does not read`,
      );
    }
    const eligible = offered.filter((capture) => !own(capture));
    counts.agent = offered.length - eligible.length;
    if (offered.length === 0) {
      closure = "skipped";
      reason = "no capture was offered to prepare";
    } else {
      // NOTHING IS FETCHED BEFORE THE MATERIAL IS KNOWN TO FIT. A named scope is sized from the
      // catalogued bytes; a content query does not know its selection yet, so the same figures
      // bound what its search may select.
      if (input.query === undefined) {
        const need = materialNeed(
          offered.reduce((sum, capture) => sum + capture.size, 0),
          offered.length,
        );
        if (need > MAX_MATERIAL_BYTES) {
          throw new Refused(
            PREPARE_REFUSALS.bound,
            `${String(offered.length)} captures need ${String(need)} bytes of material, past ` +
              `the ${String(MAX_MATERIAL_BYTES)} one material holds`,
          );
        }
        if (capacity !== null && need > capacity.free) {
          throw new Refused(
            PREPARE_REFUSALS.storage,
            `${String(offered.length)} captures need ${String(need)} bytes of material and ` +
              `the material lease has ${String(capacity.free)} free`,
          );
        }
      }

      const cacheDir = deps.cacheDir ?? "";
      let repository: string | null = null;
      if (cacheDir !== "") {
        try {
          repository = await deps.repository();
        } catch (error) {
          throw unavailable(error);
        }
      }
      const repositoryDir =
        repository === null ? null : join(cacheDir, "archive", sha256(repository));
      if (retrieval !== undefined && repositoryDir === null) {
        // A query's coverage is an index over kept readings; without a cache there is neither.
        retrieval.status = "unavailable";
        throw new Refused(
          null,
          "content selection refused: a managed preparation cache is required",
        );
      }
      /*
        The readings this machine keeps, one directory per label, each under the context of the
        mode it was read in: a cache keyed on someone else's mode would serve an unscanned stream
        to a preparation that redacts.
      */
      const caches = new Map<string, ReadingCache>();
      const cacheFor = (label: string, mode: PreflightMode): ReadingCache | null => {
        if (repositoryDir === null) return null;
        const key = JSON.stringify([label, mode]);
        let cache = caches.get(key);
        if (cache === undefined) {
          cache = readingCache(join(repositoryDir, "labels", sha256(label)), {
            schema: PREPARATION_SCHEMA,
            detectors: PREFLIGHT_DETECTORS,
            mode,
          });
          caches.set(key, cache);
        }
        return cache;
      };

      const archivedAt = await snapshotTimes(input, repositoryDir, archive);

      let chosen: readonly Capture[] = offered;
      if (input.query !== undefined && retrieval !== undefined && repositoryDir !== null) {
        const room = Math.min(MAX_MATERIAL_BYTES, capacity?.free ?? MAX_MATERIAL_BYTES);
        queried = await contentSelection(
          eligible,
          input.query,
          input.preflight,
          Math.max(0, room - materialNeed(0, eligible.length)),
          retrieval,
          repositoryDir,
          (label) => cacheFor(label, "redact"),
          fetch,
        );
        chosen = queried.chosen;
      }
      if (chosen.length === 0) {
        closure = "skipped";
        reason = "no eligible session matches the content query within the material bounds";
      } else {
        const selection: PreparationEntry[] = [];
        // The loop already counts, so the fraction costs nothing and is the honest one: a fetch
        // of a large capture is where the minutes go, and "3/927" is what an operator wants to
        // see instead of silence.
        let examined = 0;
        for (const capture of chosen) {
          examined += 1;
          const selector = capture.ref.selector;
          progress.report({
            stage: RUN_STAGES.preparing,
            message: `${selector} (${String(examined)}/${String(chosen.length)})`,
            fraction: examined / chosen.length,
          });
          // THE MATERIAL IS SEALED IN THE SAME PASS THE DIGESTS ARE TAKEN IN (#279). The file is
          // opened before the read and closed after it whatever the read did, so a scope refused
          // half way leaves no half-written stream a later reader could mistake for a session.
          const file = materialFile(sealed.length, selector);
          const record = queried?.hits.get(selector);
          const excerpt =
            record === undefined
              ? null
              : recallRecordReader({ harness: capture.ref.harness, anchor: record.position });
          const facts = captureFacts(capture.ref.harness);
          const cache = cacheFor(capture.label, input.preflight);
          let measured: SessionDigests;
          let found: ScanReport | null;
          try {
            let reused = cache === null ? null : await cache.reuse(capture.ref, capture.seen);
            if (cache !== null && reused !== null && reused.bytes !== capture.size) {
              await cache.forget(capture.ref);
              reused = null;
            }
            // The facts fold rides every pass, so there is always somewhere the stream goes.
            const seal: RecordSink =
              teeRecords(
                teeRecords((await deps.material?.session(file)) ?? null, excerpt?.sink ?? null),
                facts.sink,
              ) ?? facts.sink;
            if (cache !== null && reused !== null) {
              /*
                A READING THIS MACHINE ALREADY HAD (#236). The archive is not contacted: the kept
                stream is replayed into the material and HASHED as it goes, and what it hashes to
                is what the selection records. So the digest a citation carries is always a
                digest of the bytes that were actually sealed.

                A stream that does not hash to what it was kept as refuses the scope rather than
                falling back to a fetch. The entry is dropped, so the next preparation fetches the
                capture and succeeds — and the alternative, sealing bytes nothing verified into a
                material a model reads, is the one outcome worth failing a run over.
              */
              let digested: string;
              try {
                digested = await cache.replay(reused, seal);
              } finally {
                await seal.close();
              }
              if (digested !== reused.sourceDigest) {
                await cache.forget(capture.ref);
                throw new Refused(
                  null,
                  `the reading kept for ${selector} does not digest to what it was kept as; ` +
                    `it has been dropped, and the next preparation fetches the capture`,
                );
              }
              measured = {
                captureDigest: reused.captureDigest,
                sourceDigest: reused.sourceDigest,
                bytes: reused.bytes,
                records: reused.records,
              };
              found = reused.report;
              counts.reused++;
            } else {
              // The reading is kept in the SAME pass, off the same bytes, for the reason the
              // scan is in it: a second pass to fill a cache would pay the cost it exists to
              // avoid. NOTHING IS SEALED UNSCANNED (#339): the scan is what the stream is
              // written THROUGH, so the redaction happens before the sink and the source digest.
              const kept = cache === null ? null : await cache.keep(capture.ref, capture.seen);
              const scan = input.preflight === "off" ? null : secretScan();
              const into: RecordSink = teeRecords(seal, kept?.sink ?? null) ?? seal;
              try {
                measured = await fetch(capture, into, scan ?? undefined);
              } catch (error) {
                // BOTH SINKS ARE CLOSED BEFORE EITHER IS JUDGED, and a kept stream of a fetch
                // that did not complete is never committed.
                await into.close();
                await kept?.abandon();
                throw error;
              }
              await into.close();
              found = scan === null ? null : scan.report();
              await kept?.commit({ ...measured, report: found });
            }
          } catch (error) {
            if (error instanceof Refused) throw error;
            throw new Refused(
              null,
              retrieval === undefined
                ? `read ${selector}: ${error instanceof Error ? error.message : String(error)}`
                : "content selection refused: a selected session or its reading is unavailable",
            );
          }
          if (found !== null) {
            scans.push({ selector, report: found });
            counts.redacted += found.redactions;
            if (input.preflight === "refuse" && found.redactions > 0) {
              // The scope is refused whole, by classes and never by value. What this session
              // already wrote into the lease is the REDACTED stream, and no index is written
              // for a failed preparation, so the material binds nothing and holds no secret.
              closure = "failed";
              reason = refusalMessage(selector, found);
              break;
            }
          }
          if (excerpt !== null && record !== undefined) {
            const read = await excerpt.finish();
            if (
              !read.anchorMatches ||
              measured.sourceDigest !== record.sourceDigest ||
              measured.captureDigest !== record.captureDigest
            ) {
              throw new Refused(
                null,
                "content selection refused: a selected session or its reading is unavailable",
              );
            }
            retrievalHits.push({
              selector,
              harness: capture.ref.harness,
              file,
              captureDigest: measured.captureDigest,
              sourceDigest: measured.sourceDigest,
              record: record.position,
              excerpt: read.excerpt,
            });
          }
          counts.bytes += measured.bytes;
          counts.records += measured.records;
          const origin = {
            label: capture.label,
            snapshotId: capture.snapshotId,
            path: capture.ref.primaryPath,
          };
          selection.push({
            host: capture.label,
            harness: capture.ref.harness,
            sourceId: capture.ref.sourceId,
            captureDigest: measured.captureDigest,
            sourceDigest: measured.sourceDigest,
          });
          sealed.push({
            selector,
            harness: capture.ref.harness,
            sourceId: capture.ref.sourceId,
            captureDigest: measured.captureDigest,
            sourceDigest: measured.sourceDigest,
            file,
            records: measured.records,
            bytes: measured.bytes,
            origin,
          });
          rows.push({
            selector,
            harness: capture.ref.harness,
            source_id: capture.ref.sourceId,
            kind: babelOwnLog(capture.ref.primaryPath) ? "agent" : "operator",
            archive_label: capture.label,
            archive_path: capture.ref.primaryPath,
            snapshot_id: capture.snapshotId,
            archived_at: archivedAt.get(capture.snapshotId) ?? "",
            size: capture.size,
            modified_at: new Date(capture.modifiedAt).toISOString(),
            content_digest: measured.captureDigest,
            ...(await facts.finish()),
          });
        }
        if (closure === "completed") {
          preparation = newPreparation(new Date().toISOString(), selection);
          counts.selected = selection.length;
        }
      }
    }
  } catch (error) {
    if (!(error instanceof Refused)) throw error;
    closure = "failed";
    reason = error.message;
    if (retrieval !== undefined && retrieval.status === "complete") {
      retrieval.status = "unavailable";
      retrieval.unavailable++;
    }
  }

  // Every scoped session is registered in the hub's catalog with the capture it was read from,
  // because a preparation that named sessions the hub holds no row for would be an identity
  // nothing could later resolve.
  progress.report({ stage: RUN_STAGES.submitting, message: `${String(counts.selected)} sessions` });
  await out.write("sessions", closure === "completed" ? rows : []);
  /*
    THE INDEX IS WRITTEN LAST, TWICE, AND ONLY FOR A COMPLETED PREPARATION (#279).

    It is the material's own manifest: the preparation it belongs to, and the digest a citation
    must carry per session. It goes into the LEASE, where the session's sandbox binds it and the
    model reads it, and into the RECEIPT, where the hub ingests it and checks a submitted claim's
    locators against the selection they were served from — one document, two readers, and the
    alternative for the second was pulling a sealed archive of every session's records back
    through the hub to read the twenty lines at the front of it.

    A refused or skipped scope indexes NEITHER. A bound material whose index nobody chose the
    selection of is exactly the corpus-nobody-chose failure this operation refuses whole; the
    receipt's `reason` is where that is said, and a consumer with no index has nothing to read
    and says so.
  */
  const index: MaterialIndex | null =
    closure === "completed" && preparation !== null
      ? {
          schema: MATERIAL_SCHEMA,
          preparationId: preparation.id,
          preparedAt: preparation.preparedAt,
          machineId: input.machineId,
          sessions: sealed,
          ...(queried !== null && input.preflight !== "off" && deps.material != null
            ? { retrievalFile: MATERIAL_RETRIEVAL }
            : {}),
        }
      : null;
  if (
    index?.retrievalFile !== undefined &&
    deps.material != null &&
    queried !== null &&
    retrieval !== undefined
  ) {
    const body = MaterialRetrievalSchema.parse({
      schema: "babel.material-retrieval/1",
      queryDigest: retrieval.query.digest,
      matches: queried.recordMatches,
      omitted: queried.recordMatches - retrievalHits.length,
      hits: retrievalHits,
    });
    let encoded = JSON.stringify(body);
    while (Buffer.byteLength(encoded) > RECALL_MAX_RESULT_BYTES && body.hits.length > 0) {
      body.hits.pop();
      body.omitted++;
      encoded = JSON.stringify(body);
    }
    const sidecar = await deps.material.session(MATERIAL_RETRIEVAL);
    try {
      sidecar.write(encoded);
    } finally {
      await sidecar.close();
    }
  }
  if (index !== null && deps.material != null) await deps.material.index(index);
  const preflight = preflightReport(input.preflight, scans);
  const receipt: Receipt = {
    runId,
    kind: "prepare",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure,
    counts: { ...counts },
    ...(preparation === null ? {} : { preparation }),
    ...(index === null ? {} : { material: index }),
    preflight,
    ...(retrieval === undefined ? {} : { retrieval }),
    ...(capacity === null ? {} : { outputCapacity: capacity }),
    ...(reason === "" ? {} : { reason }),
  };
  await out.receipt(receipt);
  return receipt;
}

/** The input's sessions as captures, in the order the hub named them. */
function captures(input: PrepareInput): Capture[] {
  const all: Capture[] = [];
  for (const group of input.captures) {
    for (const session of group.sessions) {
      all.push({
        label: group.label,
        snapshotId: group.snapshotId,
        ref: sessionRef(session.harness, session.sourceId, session.path),
        size: session.size,
        modifiedAt: session.modifiedAt,
        seen: {
          size: session.size,
          modifiedAt: session.modifiedAt,
          capture: JSON.stringify([group.snapshotId, session.path]),
        },
      });
    }
  }
  return all;
}

/**
 * WHEN EACH NAMED SNAPSHOT WAS TAKEN, as every row states it (`archived_at`).
 *
 * The hub names a snapshot by id and label; the time is the snapshot's own, read from restic
 * once — one `snapshots` call for every id the machine does not already remember — and kept
 * beside the readings, because a snapshot never changes. This is also where a snapshot the
 * archive does not hold, or holds under another label, is refused, before any capture of it is
 * fetched.
 */
async function snapshotTimes(
  input: PrepareInput,
  repositoryDir: string | null,
  archive: () => Promise<PrepareRepo>,
): Promise<ReadonlyMap<string, string>> {
  /** Each snapshot's own label and time, remembered or read. */
  const known = new Map<string, z.infer<typeof SnapshotMemorySchema>>();
  const unknown = new Set<string>();
  const memory = repositoryDir === null ? null : join(repositoryDir, "snapshots");
  for (const { snapshotId } of input.captures) {
    if (known.has(snapshotId) || unknown.has(snapshotId)) continue;
    const kept = memory === null ? null : await recallSnapshot(join(memory, `${snapshotId}.json`));
    if (kept === null) unknown.add(snapshotId);
    else known.set(snapshotId, kept);
  }
  if (unknown.size > 0) {
    const repo = await archive();
    let listed: readonly { id: string; host: string; time: string }[];
    try {
      listed = await repo.snapshots([...unknown]);
    } catch (error) {
      throw unavailable(error);
    }
    for (const snapshotId of unknown) {
      const snapshot = listed.find((candidate) => candidate.id === snapshotId);
      if (snapshot === undefined) {
        throw new Refused(PREPARE_REFUSALS.missing, `snapshot ${snapshotId} is not in the archive`);
      }
      const at = captureInstant(snapshot.time);
      const read = ArchiveLabelSchema.safeParse(snapshot.host);
      if (at === null || !read.success) {
        throw new Refused(
          PREPARE_REFUSALS.missing,
          `snapshot ${snapshotId} carries no label and time a catalog row can state`,
        );
      }
      const held = { label: read.data, archivedAt: at };
      known.set(snapshotId, held);
      if (memory !== null) await rememberSnapshot(memory, `${snapshotId}.json`, held);
    }
  }
  const times = new Map<string, string>();
  for (const { snapshotId, label } of input.captures) {
    const held = known.get(snapshotId)!;
    if (held.label !== label) {
      throw new Refused(
        PREPARE_REFUSALS.missing,
        `snapshot ${snapshotId} was taken under the label ${held.label}, not ${label}`,
      );
    }
    times.set(snapshotId, held.archivedAt);
  }
  return times;
}

/** A kept snapshot time, or null when none is kept or it is unreadable: it is rebuildable. */
async function recallSnapshot(path: string): Promise<z.infer<typeof SnapshotMemorySchema> | null> {
  try {
    const parsed = SnapshotMemorySchema.safeParse(JSON.parse(await readFile(path, "utf8")));
    return parsed.success ? parsed.data : null;
  } catch {
    return null;
  }
}

/** Keeps one snapshot's time, whole or not at all; a failure only costs the next run a lookup. */
async function rememberSnapshot(
  dir: string,
  name: string,
  kept: z.infer<typeof SnapshotMemorySchema>,
): Promise<void> {
  const staged = join(dir, `${name}.${crypto.randomUUID()}.tmp`);
  try {
    await mkdir(dir, { recursive: true, mode: 0o700 });
    await writeFile(staged, JSON.stringify(kept) + "\n", { mode: 0o600 });
    await rename(staged, join(dir, name));
  } catch {
    await rm(staged, { force: true }).catch(() => undefined);
  }
}

/**
 * WHY A FETCH FAILED, as the preparation's refusal.
 *
 * A fetch past the catalogued size is `capture_changed`. When restic itself failed, the one
 * question that separates "this snapshot does not hold that path" from "the archive failed" is
 * a listing of that path, which reads tree metadata and no data: absent is `capture_missing`,
 * and anything else — the listing failing too, or the file being there after all — is the
 * archive's. A failure that is not restic's (a sink that could not write) is returned as it is.
 */
async function fetchFailure(repo: PrepareRepo, capture: Capture, error: unknown): Promise<unknown> {
  if (!(error instanceof ResticError)) return error;
  if (error.kind === "refused") {
    return new Refused(
      PREPARE_REFUSALS.changed,
      `${capture.ref.selector} is larger in snapshot ${capture.snapshotId} than the ` +
        `${String(capture.size)} bytes it was catalogued at`,
    );
  }
  if (error.kind === "exit") {
    const path = capture.ref.primaryPath;
    const held = await repo
      .ls(capture.snapshotId, [path])
      .then((listing) =>
        listing.entries.some((entry) => entry.path === path && entry.type === "file"),
      )
      .catch(() => null);
    if (held === false) {
      return new Refused(
        PREPARE_REFUSALS.missing,
        `snapshot ${capture.snapshotId} holds no file at ${path}`,
      );
    }
  }
  return unavailable(error);
}

/** What a content query chose, and the record evidence it found in what it chose. */
interface ContentSelection {
  readonly chosen: readonly Capture[];
  readonly hits: ReadonlyMap<string, IndexedRecord>;
  readonly recordMatches: number;
}

/**
 * Pays for lexical coverage before choosing material. The captures offered are the candidates:
 * each one's redacted reading is indexed — replayed from the cache, or fetched once and kept —
 * and the query ranks them; the final selection still passes through the sealing loop and its
 * preflight. The index lives beside the readings, one per repository, and a label is the
 * namespace a selector is unique in. Failure is whole-scope, never a partial search presented as
 * complete coverage.
 */
async function contentSelection(
  eligible: readonly Capture[],
  query: SessionContentQuery,
  preflight: PreflightMode,
  bound: number,
  retrieval: SessionRetrieval,
  repositoryDir: string,
  cacheFor: (label: string) => ReadingCache | null,
  fetch: (
    capture: Capture,
    into: RecordSink | undefined,
    scan: SecretScan | undefined,
  ) => Promise<SessionDigests>,
): Promise<ContentSelection> {
  const context: ReadingContext = {
    schema: PREPARATION_SCHEMA,
    detectors: PREFLIGHT_DETECTORS,
    mode: "redact",
  };
  const candidates: IndexedSession[] = eligible.map((capture) => ({
    namespace: capture.label,
    session: capture.ref,
    seen: capture.seen,
  }));
  retrieval.eligible = candidates.length;
  let index: SessionIndex | null = null;
  /** A refusal a reading raised inside the index's builder, which reports only that it failed. */
  let refused: Refused | null = null;
  try {
    index = await sessionIndex(repositoryDir, context);
    for (const [at, candidate] of candidates.entries()) {
      if (index.holds(candidate)) {
        retrieval.reused++;
        continue;
      }
      const capture = eligible[at]!;
      const cache = cacheFor(capture.label);
      if (cache === null) throw new SessionIndexError("unavailable");
      const result = await index.build(candidate, async (sink) => {
        try {
          const reused = await cache.reuse(capture.ref, capture.seen);
          if (reused !== null && reused.bytes === capture.size) {
            // Finish hashing even if corrupt bytes fail the index parser early. Otherwise the
            // invalid reading survives every refused query; an index failure alone must not
            // discard a verified reading.
            let indexFailure: SessionIndexError | null = null;
            const digest = await cache.replay(reused, {
              ...sink,
              write(record) {
                if (indexFailure !== null) return;
                try {
                  sink.write(record);
                } catch (error) {
                  indexFailure =
                    error instanceof SessionIndexError
                      ? error
                      : new SessionIndexError("unavailable");
                }
              },
            });
            if (digest !== reused.sourceDigest) {
              await cache.forget(capture.ref);
              throw new Error("redacted reading failed verification");
            }
            if (indexFailure !== null) throw indexFailure;
            await sink.close();
            return { reading: reused, after: capture.seen };
          }
          const kept = await cache.keep(capture.ref, capture.seen);
          if (kept === null) throw new Error("redacted reading cannot be kept");
          const scan = secretScan();
          const into = teeRecords(sink, kept.sink);
          let closed = false;
          try {
            const measured = await fetch(capture, into ?? undefined, scan);
            await into?.close();
            closed = true;
            const reading = { ...measured, report: scan.report() };
            await kept.commit(reading);
            if ((await cache.reuse(capture.ref, capture.seen)) === null) {
              throw new Error("redacted reading could not be kept");
            }
            return { reading, after: capture.seen };
          } catch (error) {
            try {
              if (!closed) await into?.close();
            } finally {
              await kept.abandon();
            }
            throw error;
          }
        } catch (error) {
          if (error instanceof Refused) refused = error;
          throw error;
        }
      });
      if (result === "busy" || result === "changed") {
        retrieval.status = result === "busy" ? "busy" : "unavailable";
        retrieval.unavailable = candidates.length - retrieval.indexed - retrieval.reused;
        throw new Refused(
          null,
          result === "busy"
            ? "content selection refused: the session index is busy"
            : "content selection refused: an eligible capture's reading does not match it",
        );
      }
      if (result === "indexed") retrieval.indexed++;
      else retrieval.reused++;
    }
    const found = index.search(query.text, candidates, query.limit, bound);
    retrieval.matches = found.matches;
    retrieval.overBound = found.overBound;
    const bySelector = new Map(eligible.map((capture) => [capture.ref.selector, capture]));
    const chosen = found.selection.map((session) => bySelector.get(session.selector)!);
    const hits = new Map<string, IndexedRecord>();
    let recordMatches = 0;
    if (preflight !== "off") {
      const selected = new Set(found.selection.map((session) => session.selector));
      const records = index.searchRecords(
        query.text,
        candidates.filter((candidate) => selected.has(candidate.session.selector)),
        RECALL_MAX_HITS,
      );
      recordMatches = records.matches;
      for (const record of records.hits)
        if (!hits.has(record.candidate.session.selector))
          hits.set(record.candidate.session.selector, record);
    }
    return { chosen, hits, recordMatches };
  } catch (error) {
    if (error instanceof Refused) throw error;
    retrieval.status = error instanceof SessionIndexError ? error.kind : "unavailable";
    retrieval.unavailable = Math.max(1, candidates.length - retrieval.indexed - retrieval.reused);
    if (refused !== null) throw refused;
    throw new Refused(
      null,
      retrieval.status === "busy"
        ? "content selection refused: the session index is busy"
        : "content selection refused: eligible session content or its index is unavailable",
    );
  } finally {
    index?.close();
  }
}

/** How many sites one receipt carries. The class counts above them are complete, and every
 *  locator is in the material's own markers, so this bounds a document rather than losing
 *  evidence: a corpus of a thousand leaky sessions must not write a receipt nobody can read. */
const MAX_RECEIPT_SITES = 64;

/**
 * THE PREPARATION'S OWN PREFLIGHT REPORT, folded from the per-session scans.
 *
 * It is written for every preparation, including one that scanned nothing, because a reviewer
 * asking "was this corpus checked before a provider read it?" must not have to read an absent
 * field as an answer. `mode` is what was asked for and `records` is what was actually read, so
 * `off` and "scanned and clean" cannot be confused: the first reports zero records.
 */
function preflightReport(
  mode: PreflightMode,
  scans: readonly { readonly selector: string; readonly report: ScanReport }[],
): PreflightReport {
  const classes = new Map<string, number>();
  const sites: PreflightReport["sites"] = [];
  let records = 0;
  let redactions = 0;
  let sitesOmitted = 0;
  for (const scanned of scans) {
    records += scanned.report.records;
    redactions += scanned.report.redactions;
    for (const row of scanned.report.classes) {
      classes.set(row.class, (classes.get(row.class) ?? 0) + row.redactions);
    }
    for (const site of scanned.report.sites) {
      if (sites.length < MAX_RECEIPT_SITES) sites.push({ ...site, selector: scanned.selector });
      else sitesOmitted += 1;
    }
    sitesOmitted += scanned.report.sitesOmitted;
  }
  return {
    schema: PREFLIGHT_SCHEMA,
    detectors: PREFLIGHT_DETECTORS,
    mode,
    records,
    redactions,
    classes: [...classes.entries()]
      .map(([name, count]) => ({ class: name, redactions: count }))
      .sort((a, b) => (a.class < b.class ? -1 : a.class > b.class ? 1 : 0)),
    sites,
    sitesOmitted,
  };
}
