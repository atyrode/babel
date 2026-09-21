import { Database } from "bun:sqlite";
import { chmod, lstat, mkdir } from "node:fs/promises";
import { join } from "node:path";
import { z } from "zod";

import { MAX_MATERIAL_BYTES, termsQuery, type SessionRecordPosition } from "../contract.ts";
import { SESSION_INDEX_SCHEMA } from "../store/schema.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { Observation, Reading, ReadingContext } from "./cache.ts";
import type { RecordSink } from "./output.ts";

export interface IndexedSession {
  readonly namespace?: string;
  readonly session: SessionRef;
  readonly seen: Observation;
}

export interface IndexedRecord {
  readonly candidate: IndexedSession;
  readonly position: SessionRecordPosition;
  readonly captureDigest: string;
  readonly sourceDigest: string;
}

export type IndexBuildResult = "indexed" | "reused" | "busy" | "changed";

/** No underlying SQLite or callback message is exposed: either can contain source text. */
export class SessionIndexError extends Error {
  constructor(readonly kind: "busy" | "unavailable") {
    super(
      kind === "busy" ? "Session content index is busy." : "Session content index is unavailable.",
    );
    this.name = "SessionIndexError";
  }
}

export interface SessionIndex {
  holds(candidate: IndexedSession): boolean;
  /** Canonical digests of the committed reading for this exact candidate, never caller hints. */
  digests(candidate: IndexedSession): Pick<Reading, "captureDigest" | "sourceDigest"> | null;
  build(
    candidate: IndexedSession,
    read: (sink: RecordSink) => Promise<{ reading: Reading; after: Observation }>,
    /** Replace a disagreeing committed entry only from this independently verified reading.
     *  Rechecked under the writer lock; omitted by ordinary preparation callers. */
    repair?: Pick<Reading, "captureDigest" | "sourceDigest">,
  ): Promise<IndexBuildResult>;
  search(
    text: string,
    eligible: readonly IndexedSession[],
    limit: number,
    maxBytes: number,
  ): { selection: readonly SessionRef[]; matches: number; overBound: number };
  searchRecords(
    text: string,
    eligible: readonly IndexedSession[],
    limit: number,
    window?: { since?: string; until?: string },
  ): { hits: readonly IndexedRecord[]; matches: number };
  close(): void;
}

const VERSION = 2;
const BUSY_MS = 100;
const CHUNK_CHARS = 16 * 1024;
// Normalization already bounds source records. Leave room for JSON escaping and redaction;
// malformed/custom callbacks that exceed this bound refuse instead of publishing a truncation.
const MAX_RECORD_CHARS = 64 * 1024 * 1024;
const RecordTimestampSchema = z.iso.datetime({ offset: true });

function busy(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;
  if (error instanceof SessionIndexError) return error.kind === "busy";
  const code = "code" in error ? error.code : null;
  const errno = "errno" in error ? error.errno : null;
  return (
    (typeof code === "string" &&
      (code.startsWith("SQLITE_BUSY") || code.startsWith("SQLITE_LOCKED"))) ||
    (typeof errno === "number" && ((errno & 255) === 5 || (errno & 255) === 6))
  );
}

function failure(error: unknown): SessionIndexError {
  return new SessionIndexError(busy(error) ? "busy" : "unavailable");
}

function observed(seen: Observation): boolean {
  return (
    Number.isSafeInteger(seen.size) &&
    seen.size >= 0 &&
    Number.isFinite(seen.modifiedAt) &&
    seen.modifiedAt > 0
  );
}

/** Walk scalars without allocating another flattened transcript or recursively using the stack. */
function* strings(value: unknown): Generator<string> {
  // Iterator frames bound traversal memory by nesting depth, not the number of fields.
  const stack: Iterator<unknown>[] = [[value][Symbol.iterator]()];
  while (stack.length !== 0) {
    const next = stack[stack.length - 1]!.next();
    if (next.done) {
      stack.pop();
      continue;
    }
    const item: unknown = next.value;
    if (typeof item === "string") yield item;
    else if (typeof item === "number") yield String(item);
    else if (Array.isArray(item)) stack.push(item.values());
    else if (typeof item === "object" && item !== null) stack.push(objectStrings(item));
  }
}

function* objectStrings(value: object): Generator<unknown> {
  for (const key in value) {
    if (!Object.hasOwn(value, key)) continue;
    yield key;
    yield (value as Record<string, unknown>)[key];
  }
}

function utcTime(value: string | number): string | null {
  const at = new Date(value);
  const year = at.getUTCFullYear();
  // The public locator carries a four-digit UTC ISO year, never an extended Date spelling.
  return year >= 0 && year <= 9999 ? at.toISOString() : null;
}

/** Only archived fields recognized by the harness adapters supply a record's time. */
function recordTime(parsed: unknown): string | null {
  if (typeof parsed !== "object" || parsed === null) return null;
  const fields = parsed as Record<string, unknown>;
  const iso = (value: unknown): string | null => {
    if (typeof value !== "string" || !RecordTimestampSchema.safeParse(value).success) return null;
    return utcTime(value);
  };
  const timestamp = iso(fields["timestamp"]);
  if (timestamp !== null) return timestamp;
  if (fields["type"] === "session_meta") {
    const payload = fields["payload"];
    if (typeof payload === "object" && payload !== null)
      return iso((payload as Record<string, unknown>)["timestamp"]);
  }
  // Codex's history.jsonl records seconds since the epoch rather than an ISO timestamp.
  const seconds = fields["ts"];
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) return null;
  return utcTime(seconds * 1000);
}

/**
 * Frame the already-normalized stream, never normalize it again. Hash incoming bytes, not
 * re-encoded JSON, and retain physical empty lines in the coordinates. Replay can split any
 * UTF-8 codepoint, string surrogate pair, JSON escape or record boundary.
 */
export function readNormalizedRecords(
  visit: (text: string, position: SessionRecordPosition, parsed: unknown) => void,
): RecordSink {
  const decoder = new TextDecoder("utf-8", { ignoreBOM: true, fatal: true });
  const encoder = new TextEncoder();
  const parts: string[] = [];
  let hash = new Bun.CryptoHasher("sha256");
  let length = 0;
  let tail = "";
  let surrogate = "";
  let line = 1;
  let byteOffset = 0;
  let byteLength = 0;
  let broken = false;
  let closed = false;
  const append = (text: string): void => {
    length += text.length;
    if (length > MAX_RECORD_CHARS) throw new SessionIndexError("unavailable");
    tail += text;
    if (tail.length >= CHUNK_CHARS) {
      parts.push(tail);
      tail = "";
    }
  };
  const record = (): void => {
    if (byteLength === 0) return;
    append(decoder.decode());
    if (tail !== "") parts.push(tail);
    const text = parts.length === 1 ? parts[0]! : parts.join("");
    let parsed: unknown;
    if (!text.startsWith("!")) {
      try {
        parsed = JSON.parse(text);
      } catch (error) {
        if (!(error instanceof SyntaxError)) throw error;
      }
    }
    visit(
      text,
      {
        line,
        byteOffset,
        byteLength,
        digest: `sha256:${hash.digest("hex")}`,
        time: recordTime(parsed),
      },
      parsed,
    );
    line += 1;
    byteOffset += byteLength;
    byteLength = 0;
    parts.length = 0;
    tail = "";
    length = 0;
    hash = new Bun.CryptoHasher("sha256");
  };
  const consume = (bytes: Uint8Array): void => {
    let start = 0;
    while (start < bytes.byteLength) {
      const newline = bytes.indexOf(10, start);
      const end = newline < 0 ? bytes.byteLength : newline;
      const next = newline < 0 ? end : end + 1;
      hash.update(bytes.subarray(start, next));
      byteLength += next - start;
      append(decoder.decode(bytes.subarray(start, end), { stream: true }));
      if (newline >= 0) record();
      start = next;
    }
  };
  const flushSurrogate = (): void => {
    if (surrogate === "") return;
    consume(encoder.encode(surrogate));
    surrogate = "";
  };
  return {
    write(chunk) {
      try {
        if (closed || broken) throw new SessionIndexError("unavailable");
        if (typeof chunk === "string") {
          let at = 0;
          if (surrogate !== "" && chunk !== "") {
            const first = chunk.charCodeAt(0);
            if (first >= 0xdc00 && first <= 0xdfff) {
              consume(encoder.encode(surrogate + chunk[0]!));
              surrogate = "";
              at = 1;
            } else flushSurrogate();
          }
          while (at < chunk.length) {
            let end = Math.min(at + CHUNK_CHARS, chunk.length);
            const last = chunk.charCodeAt(end - 1);
            if (last >= 0xd800 && last <= 0xdbff) end -= 1;
            if (end === at) {
              surrogate = chunk[at]!;
              break;
            }
            consume(encoder.encode(chunk.slice(at, end)));
            at = end;
          }
        } else if (chunk.byteLength !== 0) {
          flushSurrogate();
          for (let at = 0; at < chunk.byteLength; at += CHUNK_CHARS)
            consume(chunk.subarray(at, at + CHUNK_CHARS));
        }
      } catch (error) {
        broken = true;
        throw failure(error);
      }
    },
    async close() {
      if (broken) throw new SessionIndexError("unavailable");
      if (closed) return;
      try {
        flushSurrogate();
        record();
        closed = true;
      } catch (error) {
        broken = true;
        throw failure(error);
      }
    },
  };
}

/** SQLite owns tokenization; the record visitor already parsed JSON exactly once. */
function passages(text: string, parsed: unknown, insert: (text: string) => void): void {
  if (text.startsWith("!")) insert(text.slice(1));
  else if (parsed === undefined) {
    if (text !== "") insert(text);
  } else {
    let passage = "";
    for (const value of strings(parsed)) {
      if (value === "") continue;
      if (passage.length + value.length >= CHUNK_CHARS) {
        if (passage !== "") insert(passage);
        passage = "";
      }
      // Large scalars remain intact; invented boundaries would split searchable words.
      if (value.length >= CHUNK_CHARS) insert(value);
      else passage += (passage === "" ? "" : "\n") + value;
    }
    if (passage !== "") insert(passage);
  }
}

/** The only filesystem opened here is the managed cache; source reads belong to the callback. */
export async function sessionIndex(dir: string, context: ReadingContext): Promise<SessionIndex> {
  let db: Database | null = null;
  try {
    if (dir === "" || context.mode !== "redact") throw new SessionIndexError("unavailable");
    const privateDir = join(dir, `session-index-v${VERSION}`);
    await mkdir(privateDir, { recursive: true, mode: 0o700 });
    if (!(await lstat(privateDir)).isDirectory()) throw new SessionIndexError("unavailable");
    await chmod(privateDir, 0o700);
    const path = join(privateDir, "tokens.sqlite");
    const file = await lstat(path).catch((error: unknown) => {
      if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT")
        return null;
      throw error;
    });
    if (file !== null && !file.isFile()) throw new SessionIndexError("unavailable");
    db = new Database(path, { create: true, strict: true });
    db.exec(
      `PRAGMA busy_timeout = ${BUSY_MS}; PRAGMA foreign_keys = ON; PRAGMA cache_size = -2048; PRAGMA temp_store = FILE`,
    );
    db.exec("PRAGMA journal_mode = WAL");
    await chmod(path, 0o600);
    const version = (): number =>
      db!.query<{ user_version: number }, []>("PRAGMA user_version").get()!.user_version;
    if (version() === 0) {
      db.exec("BEGIN IMMEDIATE");
      // Another opener may have initialized the file between our first check and the lock.
      if (version() === 0) {
        for (const sql of SESSION_INDEX_SCHEMA) db.exec(sql);
        db.exec(`PRAGMA user_version = ${VERSION}`);
      }
      if (version() !== VERSION) throw new SessionIndexError("unavailable");
      db.exec("COMMIT");
    }
    if (version() !== VERSION) throw new SessionIndexError("unavailable");
    return opened(db, { ...context });
  } catch (error) {
    // Closing also rolls back an interrupted initialization. Never unlink a shared database.
    try {
      db?.close();
    } catch {
      /* The original, sanitized failure is the useful one. */
    }
    throw failure(error);
  }
}

function opened(db: Database, context: ReadingContext): SessionIndex {
  let closed = false;
  let active = false;
  const lookup = db.query<
    { id: number },
    [string, string, string, string, string, string, number, number, number, string, string]
  >(
    `SELECT id FROM session_sources WHERE namespace = ? AND selector = ? AND capture = ?
     AND harness = ? AND source_id = ? AND path = ? AND size = ? AND modified_at = ?
     AND schema = ? AND detectors = ? AND mode = ?`,
  );
  const digestLookup = db.query<Pick<Reading, "captureDigest" | "sourceDigest">, [number]>(
    "SELECT capture_digest AS captureDigest, source_digest AS sourceDigest FROM session_sources WHERE id = ?",
  );
  const current = ({ namespace, session, seen }: IndexedSession): number | null => {
    if (!observed(seen)) return null;
    return (
      lookup.get(
        namespace ?? "",
        session.selector,
        seen.capture ?? "",
        session.harness,
        session.sourceId,
        session.primaryPath,
        seen.size,
        seen.modifiedAt,
        context.schema,
        context.detectors,
        context.mode,
      )?.id ?? null
    );
  };
  const ready = (): void => {
    if (closed) throw new SessionIndexError("unavailable");
    if (active) throw new SessionIndexError("busy");
  };
  const rollback = (): void => {
    if (closed || !db.inTransaction) return;
    try {
      db.exec("ROLLBACK");
    } catch {
      closed = true;
      try {
        db.close();
      } catch {
        /* Release a failed handle; never reuse a broken transaction. */
      }
    }
  };
  // Both APIs use the same query, coverage snapshot and best-passage ordering. Only bounded
  // record hits load locator metadata; scanning the remaining matches retains integer IDs.
  const retrieve = <Result>(
    text: string,
    eligible: readonly IndexedSession[],
    limit: number,
    window: { since?: string; until?: string } | undefined,
    empty: Result,
    collect: (
      rows: Iterable<{ source: number; record: number }>,
      candidates: ReadonlyMap<number, IndexedSession>,
    ) => Result,
  ): Result => {
    ready();
    if (text.length > 512 || !Number.isInteger(limit) || limit < 1 || limit > 120)
      throw new SessionIndexError("unavailable");
    const bound = (value: string | undefined): string | null => {
      if (value === undefined) return null;
      const at = new Date(value);
      if (Number.isNaN(at.getTime())) throw new SessionIndexError("unavailable");
      return at.toISOString();
    };
    const since = bound(window?.since);
    const until = bound(window?.until);
    if (since !== null && until !== null && since > until)
      throw new SessionIndexError("unavailable");
    const query = termsQuery(text);
    if (query === "") return empty;
    active = true;
    try {
      db.exec("BEGIN");
      const candidates = new Map<number, IndexedSession>();
      for (const candidate of eligible) {
        const id = current(candidate);
        if (id === null) throw new SessionIndexError("unavailable");
        if (!candidates.has(id)) candidates.set(id, candidate);
      }
      const rows = db.query<
        { source: number; record: number },
        [string, string | null, string | null, string | null, string | null]
      >(
        `SELECT p.source, p.record FROM session_terms
         JOIN session_passages p ON p.id = session_terms.rowid
         JOIN session_sources s ON s.id = p.source
         JOIN session_records r ON r.id = p.record
         WHERE session_terms MATCH ? AND (? IS NULL OR r.time >= ?)
           AND (? IS NULL OR r.time <= ?)
         ORDER BY session_terms.rank, s.namespace, s.selector, r.line, p.id`,
      );
      const result = collect(rows.iterate(query, since, since, until, until), candidates);
      db.exec("COMMIT");
      return result;
    } catch (error) {
      throw failure(error);
    } finally {
      rollback();
      active = false;
    }
  };
  return {
    holds(candidate) {
      try {
        ready();
        return current(candidate) !== null;
      } catch (error) {
        throw failure(error);
      }
    },
    digests(candidate) {
      ready();
      active = true;
      try {
        // Keep identity and digests in one read snapshot: a concurrent replacement can reuse
        // a source row ID, but must never lend its digests to an older observation.
        db.exec("BEGIN");
        const source = current(candidate);
        if (source === null) return null;
        const held = digestLookup.get(source);
        if (
          held === null ||
          held.captureDigest.length !== 71 ||
          held.sourceDigest.length !== 71 ||
          !/^sha256:[0-9a-f]{64}$/.test(held.captureDigest) ||
          !/^sha256:[0-9a-f]{64}$/.test(held.sourceDigest)
        )
          return null;
        return held;
      } catch (error) {
        throw failure(error);
      } finally {
        rollback();
        active = false;
      }
    },
    async build(candidate, read, repair) {
      if (active) return "busy";
      ready();
      if (!observed(candidate.seen)) return "changed";
      // Snapshot caller-owned objects before awaiting any source read.
      const frozen = {
        namespace: candidate.namespace ?? "",
        session: { ...candidate.session },
        seen: { ...candidate.seen },
        repair: repair === undefined ? undefined : {
          captureDigest: repair.captureDigest,
          sourceDigest: repair.sourceDigest,
        },
      };
      active = true;
      try {
        db.exec("BEGIN IMMEDIATE");
        const existing = current(frozen);
        if (existing !== null) {
          if (frozen.repair === undefined) return "reused";
          const held = digestLookup.get(existing);
          if (
            held?.captureDigest === frozen.repair.captureDigest &&
            held.sourceDigest === frozen.repair.sourceDigest
          )
            return "reused";
        }
        const old = db
          .query<{ id: number }, [string, string]>(
            "SELECT id FROM session_sources WHERE namespace = ? AND selector = ?",
          )
          .get(frozen.namespace, frozen.session.selector);
        if (old !== null) {
          db.query(
            "DELETE FROM session_terms WHERE rowid IN (SELECT id FROM session_passages WHERE source = ?)",
          ).run(old.id);
          db.query("DELETE FROM session_passages WHERE source = ?").run(old.id);
          db.query("DELETE FROM session_records WHERE source = ?").run(old.id);
          db.query("DELETE FROM session_sources WHERE id = ?").run(old.id);
        }
        const { session, seen } = frozen;
        const source = db
          .query(
            `INSERT INTO session_sources(namespace, selector, capture, harness, source_id, path,
             size, modified_at, schema, detectors, mode, capture_digest, source_digest)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '')`,
          )
          .run(
            frozen.namespace,
            session.selector,
            seen.capture ?? "",
            session.harness,
            session.sourceId,
            session.primaryPath,
            seen.size,
            seen.modifiedAt,
            context.schema,
            context.detectors,
            context.mode,
          ).lastInsertRowid;
        const record = db.query(
          `INSERT INTO session_records(source, line, byte_offset, byte_length, digest, time)
           VALUES (?, ?, ?, ?, ?, ?)`,
        );
        const passage = db.query("INSERT INTO session_passages(source, record) VALUES (?, ?)");
        const terms = db.query("INSERT INTO session_terms(rowid, tokens) VALUES (?, ?)");
        const sink = readNormalizedRecords((text, position, parsed) => {
          if (closed) throw new SessionIndexError("unavailable");
          const id = record.run(
            source,
            position.line,
            position.byteOffset,
            position.byteLength,
            position.digest,
            position.time,
          ).lastInsertRowid;
          passages(text, parsed, (tokens) => {
            const row = passage.run(source, id).lastInsertRowid;
            terms.run(row, tokens);
          });
        });
        const { reading, after } = await read(sink);
        await sink.close();
        if (
          !observed(after) ||
          after.size !== seen.size ||
          after.modifiedAt !== seen.modifiedAt ||
          (after.capture ?? "") !== (seen.capture ?? "") ||
          reading.bytes !== seen.size ||
          (frozen.repair !== undefined &&
            (reading.captureDigest !== frozen.repair.captureDigest ||
              reading.sourceDigest !== frozen.repair.sourceDigest))
        )
          return "changed";
        db.query(
          "UPDATE session_sources SET capture_digest = ?, source_digest = ? WHERE id = ?",
        ).run(reading.captureDigest, reading.sourceDigest, source);
        db.exec("COMMIT");
        return "indexed";
      } catch (error) {
        if (busy(error)) return "busy";
        throw failure(error);
      } finally {
        rollback();
        active = false;
      }
    },
    search(text, eligible, limit, maxBytes) {
      ready();
      if (!Number.isSafeInteger(maxBytes) || maxBytes < 0)
        throw new SessionIndexError("unavailable");
      return retrieve(
        text,
        eligible,
        limit,
        undefined,
        { selection: [] as SessionRef[], matches: 0, overBound: 0 },
        (rows, candidates) => {
          const matched = new Set<number>();
          const selection: SessionRef[] = [];
          let bytes = 0;
          let overBound = 0;
          const bound = Math.min(maxBytes, MAX_MATERIAL_BYTES);
          for (const hit of rows) {
            const candidate = candidates.get(hit.source);
            if (candidate === undefined || matched.has(hit.source)) continue;
            matched.add(hit.source);
            if (selection.length >= limit) continue;
            if (candidate.seen.size > bound - bytes) {
              overBound += 1;
              continue;
            }
            bytes += candidate.seen.size;
            selection.push(candidate.session);
          }
          return { selection, matches: matched.size, overBound };
        },
      );
    },
    searchRecords(text, eligible, limit, window) {
      const empty = { hits: [] as IndexedRecord[], matches: 0 };
      return retrieve(text, eligible, limit, window, empty, (rows, candidates) => {
        const matched = new Set<number>();
        const hits: IndexedRecord[] = [];
        const metadata = db.query<
          SessionRecordPosition & { captureDigest: string; sourceDigest: string },
          [number]
        >(
          `SELECT r.line, r.byte_offset AS byteOffset, r.byte_length AS byteLength,
           r.digest, r.time, s.capture_digest AS captureDigest, s.source_digest AS sourceDigest
           FROM session_records r JOIN session_sources s ON s.id = r.source WHERE r.id = ?`,
        );
        for (const row of rows) {
          const candidate = candidates.get(row.source);
          if (candidate === undefined || matched.has(row.record)) continue;
          matched.add(row.record);
          if (hits.length >= limit) continue;
          const entry = metadata.get(row.record);
          if (entry === null) throw new SessionIndexError("unavailable");
          const { captureDigest, sourceDigest, ...position } = entry;
          hits.push({ candidate, position, captureDigest, sourceDigest });
        }
        return { hits, matches: matched.size };
      });
    },
    close() {
      if (closed) return;
      rollback();
      closed = true;
      try {
        db.close();
      } catch (error) {
        throw failure(error);
      }
    },
  };
}
