import { mkdir, rename, rm, stat } from "node:fs/promises";
import { join } from "node:path";

import { z } from "zod";

import { PreflightModeSchema, type PreflightMode } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { RecordSink } from "./output.ts";
import { SECRET_CLASSES, type ScanReport, type SecretClass } from "./preflight.ts";

/*
  WHAT ONE PASS OVER A SESSION'S LOG PRODUCED, KEPT FOR THE NEXT PASS (#236).

  A preparation's whole cost is reading logs: one pass per selected session, normalizing every
  record, scanning it for credentials and hashing it twice. On 2026-09-12 twenty concurrent
  explorations over overlapping scopes paid that twenty times over the same files within seconds
  of each other — load 41 on twelve cores with no model call in flight, and an OOM that took the
  operator's editor with it. The digests do not depend on which run asked, so the second run
  should not have to ask the disk.

  THIS IS A CACHE OF A READING, NOT A POSITION. #337 refused a cursor for the corpus backfill
  because "the pending set is the gap itself", and a stored position is a second authority that
  can disagree with the rows. The same discipline is what makes a cache admissible here: an entry
  is not a claim about the corpus NOW. It is a claim about one observation — this path, this many
  bytes, this mtime, this immutable archive capture, under this normalization, this detector set
  and this preflight mode. The caller supplies the capture identity and the file metadata the
  archive recorded for it, and the cache opens no source to check them. A changed observation
  fails to match, which is a miss and needs no invalidation step to get right.

  WHY THE OBSERVATION IS ENOUGH. A capture never moves, but distinct immutable captures may share
  a path, size and mtime, so the capture identity is part of the match: a reading of one snapshot
  is never served for another.

  AND THE STREAM IS VERIFIED, NOT TRUSTED. The kept stream is replayed into the material and
  hashed as it goes, and the digest it actually produces is what the preparation records. A
  reused reading therefore cannot seal bytes it did not keep: the one thing a model reads is
  checked against the digest a citation will carry, every time, at the cost of a sequential read
  with no parsing and no scanning in it.

  ONE ENTRY PER SESSION in the caller-owned directory, so the cache is bounded by the corpus
  and not by how often it is read: the slot is named from the selector alone and a different
  observation overwrites its own entry. Archive hosts use separate directories. The whole of it
  is best effort: a failed cache write only means reading the source again next time.
*/

/** The capture's size and recorded modification time, bound to its immutable archive identity.
 *  `modifiedAt` is 0 when nothing could be observed, which is never a match. */
export interface Observation {
  readonly size: number;
  /** Epoch milliseconds, from the archive's metadata for the capture. */
  readonly modifiedAt: number;
  /** The immutable archive capture this observation is of. */
  readonly capture: string;
}

/** What one pass produced, as a later pass may reuse it. */
export interface Reading {
  readonly captureDigest: string;
  readonly sourceDigest: string;
  /** Bytes of the log the capture digest covered. */
  readonly bytes: number;
  readonly records: number;
  /** The secret scan's own report, or null when the stream was sealed unscanned (`off`). */
  readonly report: ScanReport | null;
}

/** A kept reading and the stream it belongs to, to be replayed and verified. */
export interface ReusedReading extends Reading {
  /** The kept normalized stream — see {@link ReadingCache.replay}, which is the only thing
   *  that may read it, because it is also what checks it. */
  readonly stream: string;
}

/** What an entry is a reading UNDER: change any of the three and every entry is a miss. */
export interface ReadingContext {
  /** The normalization behind the source digest (`prepare.ts`'s `PREPARATION_SCHEMA`). */
  readonly schema: number;
  /** The secret rule set the stream was written through (`PREFLIGHT_DETECTORS`). */
  readonly detectors: string;
  /** What that scan was asked to do. A stream kept unscanned is never served to a preparation
   *  that redacts, which is the whole reason the mode is part of the key. */
  readonly mode: PreflightMode;
}

/** The sink one pass writes its normalized stream through so a later pass can reuse it. */
export interface KeptStream {
  /** Written alongside the material's own sink, by the single pass over the log. */
  readonly sink: RecordSink;
  /** Commits the entry: this is the reading the stream that was just written belongs to. */
  commit(reading: Reading): Promise<void>;
  /** Abandons it. A scope refused half way leaves nothing a later pass could reuse. */
  abandon(): Promise<void>;
}

export interface ReadingCache {
  /** The kept reading for this observation, or null when nothing was kept for it. */
  reuse(session: SessionRef, seen: Observation): Promise<ReusedReading | null>;
  /**
   * Replays a kept stream into the material, and returns the source digest OF THE BYTES IT
   * ACTUALLY WROTE — which the caller compares against {@link Reading.sourceDigest} before
   * recording it as a session's reading.
   */
  replay(reused: ReusedReading, into: RecordSink): Promise<string>;
  /** Drops this session's entry, for the one caller that found it unusable. */
  forget(session: SessionRef): Promise<void>;
  /** Opens an entry for a session about to be read, or null when none can be written. */
  keep(session: SessionRef, seen: Observation): Promise<KeptStream | null>;
}

const SecretClassSchema = z.enum(
  SECRET_CLASSES.map((detector) => detector.name) as [SecretClass, ...SecretClass[]],
);

/** `preflight.ts`'s own `ScanReport`, as a document read back off this machine's disk. */
const ScanReportSchema = z.strictObject({
  records: z.number().int().nonnegative(),
  redactions: z.number().int().nonnegative(),
  classes: z.array(
    z.strictObject({ class: SecretClassSchema, redactions: z.number().int().positive() }),
  ),
  sites: z.array(
    z.strictObject({
      class: SecretClassSchema,
      line: z.number().int().positive(),
      offset: z.number().int().nonnegative(),
      length: z.number().int().positive(),
    }),
  ),
  sitesOmitted: z.number().int().nonnegative(),
});

/**
 * ONE ENTRY, AS IT LIES ON DISK. It is parsed rather than cast for the reason every document
 * crossing a process boundary is: an entry written by another schema, half-written, or truncated
 * by a full disk must be a miss, and a miss is the cheapest possible failure here.
 *
 * `streamBytes` is what makes the two files one entry: the stream is renamed into place BEFORE
 * the document that describes it, so a document whose stream is a different length is a torn
 * write and not a reading.
 */
const KeptReadingSchema = z.strictObject({
  schema: z.number().int(),
  detectors: z.string().min(1),
  mode: PreflightModeSchema,
  selector: z.string().min(1),
  path: z.string().min(1),
  capture: z.string(),
  size: z.number().int().nonnegative(),
  modifiedAt: z.number().positive(),
  captureDigest: z
    .string()
    .length(71)
    .regex(/^sha256:[0-9a-f]{64}$/),
  sourceDigest: z
    .string()
    .length(71)
    .regex(/^sha256:[0-9a-f]{64}$/),
  bytes: z.number().int().nonnegative(),
  records: z.number().int().nonnegative(),
  streamBytes: z.number().int().nonnegative(),
  report: ScanReportSchema.nullable(),
});

/**
 * The readings kept in one directory, under one context.
 *
 * The directory is created on the first entry written rather than at construction, as every
 * other sink in this half is: a preparation that reused everything, or one whose machine bound
 * no cache at all, leaves nothing behind.
 */
export function readingCache(dir: string, about: ReadingContext): ReadingCache {
  let ensured: Promise<unknown> | null = null;
  /** The slot this session's entry lives in: its selector, hashed so a source id cannot spell
   *  a path. One slot per session, whatever the log did, which is what bounds the cache. */
  const slotOf = (session: SessionRef): string =>
    join(dir, new Bun.CryptoHasher("sha256").update(session.selector).digest("hex"));

  return {
    reuse: async (session, seen) => {
      if (seen.modifiedAt <= 0) return null;
      const capture = seen.capture;
      const slot = slotOf(session);
      const text = await Bun.file(`${slot}.json`)
        .text()
        .catch(() => "");
      if (text === "") return null;
      let document: unknown;
      try {
        document = JSON.parse(text);
      } catch {
        return null;
      }
      const parsed = KeptReadingSchema.safeParse(document);
      if (!parsed.success) return null;
      const kept = parsed.data;
      // EVERY FIELD OF THE OBSERVATION, AND THE CONTEXT IT WAS READ UNDER. A mismatch in any
      // one of them is a miss: there is no partial reuse and no field this repairs.
      if (kept.schema !== about.schema) return null;
      if (kept.detectors !== about.detectors) return null;
      if (kept.mode !== about.mode) return null;
      if (kept.selector !== session.selector) return null;
      if (kept.path !== session.primaryPath) return null;
      if (kept.size !== seen.size) return null;
      if (kept.modifiedAt !== seen.modifiedAt) return null;
      if (kept.capture !== capture) return null;
      const stream = `${slot}.records`;
      const lying = await stat(stream).catch(() => null);
      if (lying === null || lying.size !== kept.streamBytes) return null;
      return {
        captureDigest: kept.captureDigest,
        sourceDigest: kept.sourceDigest,
        bytes: kept.bytes,
        records: kept.records,
        report: kept.report,
        stream,
      };
    },

    replay: async (reused, into) => {
      const source = new Bun.CryptoHasher("sha256");
      for await (const chunk of Bun.file(reused.stream).stream()) {
        source.update(chunk);
        into.write(chunk);
      }
      return `sha256:${source.digest("hex")}`;
    },

    forget: async (session) => {
      const slot = slotOf(session);
      // The document goes first: it is the commit marker, so an interrupted forget leaves an
      // orphan stream rather than an entry pointing at nothing.
      await rm(`${slot}.json`, { force: true }).catch(() => undefined);
      await rm(`${slot}.records`, { force: true }).catch(() => undefined);
    },

    keep: async (session, seen) => {
      if (seen.modifiedAt <= 0) return null;
      const capture = seen.capture;
      const slot = slotOf(session);
      const temporary = `${slot}.${crypto.randomUUID()}.records`;
      let writer: Bun.FileSink;
      try {
        ensured ??= mkdir(dir, { recursive: true });
        await ensured;
        writer = Bun.file(temporary).writer();
      } catch {
        return null;
      }
      /** Once a write has failed, nothing more is attempted and no entry is committed: the
       *  preparation itself is unaffected, and the next one reads the log. */
      let broken = false;
      const discard = async (): Promise<void> => {
        await rm(temporary, { force: true }).catch(() => undefined);
      };
      return {
        sink: {
          write: (record) => {
            if (broken) return;
            try {
              writer.write(record);
            } catch {
              broken = true;
            }
          },
          close: async () => {
            try {
              await writer.end();
            } catch {
              broken = true;
            }
          },
        },
        commit: async (reading) => {
          if (broken) {
            await discard();
            return;
          }
          const staged = `${slot}.${crypto.randomUUID()}.json`;
          try {
            const written = await stat(temporary);
            await rename(temporary, `${slot}.records`);
            const document = {
              ...about,
              selector: session.selector,
              path: session.primaryPath,
              capture,
              size: seen.size,
              modifiedAt: seen.modifiedAt,
              captureDigest: reading.captureDigest,
              sourceDigest: reading.sourceDigest,
              bytes: reading.bytes,
              records: reading.records,
              streamBytes: written.size,
              report: reading.report,
            };
            // The document is renamed last, so it is only ever seen beside a complete stream.
            await Bun.write(staged, JSON.stringify(document) + "\n");
            await rename(staged, `${slot}.json`);
          } catch {
            await discard();
          } finally {
            await rm(staged, { force: true }).catch(() => undefined);
          }
        },
        abandon: discard,
      };
    },
  };
}
