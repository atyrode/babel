import { Database } from "bun:sqlite";
import { chmod, lstat, mkdir } from "node:fs/promises";
import { join } from "node:path";

import { MAX_MATERIAL_BYTES, termsQuery } from "../contract.ts";
import { SESSION_INDEX_SCHEMA } from "../store/schema.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { Observation, Reading, ReadingContext } from "./cache.ts";
import type { RecordSink } from "./output.ts";

export interface IndexedSession {
  readonly session: SessionRef;
  readonly seen: Observation;
}

export type IndexBuildResult = "indexed" | "reused" | "busy" | "changed";

/** No underlying SQLite or callback message is exposed: either can contain source text. */
export class SessionIndexError extends Error {
  constructor(readonly kind: "busy" | "unavailable") {
    super(kind === "busy" ? "Session content index is busy." : "Session content index is unavailable.");
    this.name = "SessionIndexError";
  }
}

export interface SessionIndex {
  holds(candidate: IndexedSession): boolean;
  build(
    candidate: IndexedSession,
    read: (sink: RecordSink) => Promise<{ reading: Reading; after: Observation }>,
  ): Promise<IndexBuildResult>;
  search(
    text: string,
    eligible: readonly IndexedSession[],
    limit: number,
    maxBytes: number,
  ): { selection: readonly SessionRef[]; matches: number; overBound: number };
  close(): void;
}

const VERSION = 1;
const BUSY_MS = 100;
const CHUNK_CHARS = 16 * 1024;
// Normalization already bounds source records. Leave room for JSON escaping and redaction;
// malformed/custom callbacks that exceed this bound refuse instead of publishing a truncation.
const MAX_RECORD_CHARS = 64 * 1024 * 1024;

function busy(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;
  if (error instanceof SessionIndexError) return error.kind === "busy";
  const code = "code" in error ? error.code : null;
  const errno = "errno" in error ? error.errno : null;
  return (
    (typeof code === "string" && (code.startsWith("SQLITE_BUSY") || code.startsWith("SQLITE_LOCKED"))) ||
    (typeof errno === "number" && ((errno & 255) === 5 || (errno & 255) === 6))
  );
}

function failure(error: unknown): SessionIndexError {
  return new SessionIndexError(busy(error) ? "busy" : "unavailable");
}

function observed(seen: Observation): boolean {
  return Number.isSafeInteger(seen.size) && seen.size >= 0 && Number.isFinite(seen.modifiedAt) && seen.modifiedAt > 0;
}

/** Walk scalars without allocating another flattened transcript or recursively using the stack. */
function* strings(value: unknown): Generator<string> {
  // Iterator frames bound traversal memory by nesting depth, not the number of fields.
  const stack: Iterator<unknown>[] = [[value][Symbol.iterator]()];
  while (stack.length !== 0) {
    const next = stack[stack.length - 1]!.next();
    if (next.done) { stack.pop(); continue; }
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

/**
 * Replay can split UTF-8 and JSON escapes anywhere. Buffer one bounded normalized record,
 * parse it once, and let SQLite (not a second Unicode tokenizer) index its scalar passages.
 * Raw (!-prefixed) records are searchable verbatim. No retained history is truncated.
 */
function passages(insert: (text: string) => void): RecordSink {
  const decoder = new TextDecoder();
  const parts: string[] = [];
  let length = 0;
  let tail = "";
  let broken = false;
  let closed = false;
  const record = (): void => {
    if (length === 0) return;
    if (tail !== "") parts.push(tail);
    const text = parts.length === 1 ? parts[0]! : parts.join("");
    parts.length = 0;
    tail = "";
    length = 0;
    if (text.startsWith("!")) insert(text.slice(1));
    else {
      const parsed: unknown = JSON.parse(text);
      let passage = "";
      for (const value of strings(parsed)) {
        if (value === "") continue;
        if (passage.length + value.length >= CHUNK_CHARS) {
          if (passage !== "") insert(passage);
          passage = "";
        }
        // Large scalars are already owned by this bounded record. Native FTS tokenizes them
        // without copying or splitting a word at an invented character boundary.
        if (value.length >= CHUNK_CHARS) insert(value);
        else passage += (passage === "" ? "" : "\n") + value;
      }
      if (passage !== "") insert(passage);
    }
  };
  const consume = (text: string): void => {
    let start = 0;
    while (start < text.length) {
      const newline = text.indexOf("\n", start);
      const end = newline < 0 ? text.length : newline;
      length += end - start;
      if (length > MAX_RECORD_CHARS) throw new SessionIndexError("unavailable");
      if (end > start) {
        tail += text.slice(start, end);
        if (tail.length >= CHUNK_CHARS) { parts.push(tail); tail = ""; }
      }
      if (newline < 0) return;
      record();
      start = newline + 1;
    }
  };
  return {
    write(record) {
      try {
        if (closed || broken) throw new SessionIndexError("unavailable");
        if (typeof record === "string") {
          consume(decoder.decode());
          for (let at = 0; at < record.length; at += CHUNK_CHARS) consume(record.slice(at, at + CHUNK_CHARS));
        } else {
          for (let at = 0; at < record.byteLength; at += CHUNK_CHARS) {
            consume(decoder.decode(record.subarray(at, at + CHUNK_CHARS), { stream: true }));
          }
        }
      } catch (error) { broken = true; throw failure(error); }
    },
    async close() {
      if (broken) throw new SessionIndexError("unavailable");
      if (closed) return;
      try {
        consume(decoder.decode());
        record();
        closed = true;
      } catch (error) { broken = true; throw failure(error); }
    },
  };
}

/** The only filesystem opened here is the managed cache; source reads belong to the callback. */
export async function sessionIndex(dir: string, context: ReadingContext): Promise<SessionIndex> {
  let db: Database | null = null;
  try {
    if (dir === "" || context.mode !== "redact") throw new SessionIndexError("unavailable");
    const privateDir = join(dir, "session-index-v1");
    await mkdir(privateDir, { recursive: true, mode: 0o700 });
    if (!(await lstat(privateDir)).isDirectory()) throw new SessionIndexError("unavailable");
    await chmod(privateDir, 0o700);
    const path = join(privateDir, "tokens.sqlite");
    const file = await lstat(path).catch((error: unknown) => {
      if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") return null;
      throw error;
    });
    if (file !== null && !file.isFile()) throw new SessionIndexError("unavailable");
    db = new Database(path, { create: true, strict: true });
    db.exec(`PRAGMA busy_timeout = ${BUSY_MS}; PRAGMA foreign_keys = ON; PRAGMA cache_size = -2048; PRAGMA temp_store = FILE`);
    db.exec("PRAGMA journal_mode = WAL");
    await chmod(path, 0o600);
    const version = (): number => db!.query<{ user_version: number }, []>("PRAGMA user_version").get()!.user_version;
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
    try { db?.close(); } catch { /* The original, sanitized failure is the useful one. */ }
    throw failure(error);
  }
}

function opened(db: Database, context: ReadingContext): SessionIndex {
  let closed = false;
  let active = false;
  const lookup = db.query<{ id: number }, [string, string, string, string, number, number, number, string, string]>(
    `SELECT id FROM session_sources WHERE selector = ? AND harness = ? AND source_id = ?
     AND path = ? AND size = ? AND modified_at = ? AND schema = ? AND detectors = ? AND mode = ?`,
  );
  const current = ({ session, seen }: IndexedSession): number | null => {
    if (!observed(seen)) return null;
    return lookup.get(session.selector, session.harness, session.sourceId, session.primaryPath,
      seen.size, seen.modifiedAt, context.schema, context.detectors, context.mode)?.id ?? null;
  };
  const ready = (): void => {
    if (closed) throw new SessionIndexError("unavailable");
    if (active) throw new SessionIndexError("busy");
  };
  const rollback = (): void => {
    if (closed || !db.inTransaction) return;
    try { db.exec("ROLLBACK"); } catch {
      closed = true;
      try { db.close(); } catch { /* Release a failed handle; never reuse a broken transaction. */ }
    }
  };
  return {
    holds(candidate) {
      try { ready(); return current(candidate) !== null; } catch (error) { throw failure(error); }
    },
    async build(candidate, read) {
      if (active) return "busy";
      ready();
      if (!observed(candidate.seen)) return "changed";
      // Snapshot caller-owned objects before awaiting any source read.
      const frozen = { session: { ...candidate.session }, seen: { ...candidate.seen } };
      active = true;
      try {
        db.exec("BEGIN IMMEDIATE");
        if (current(frozen) !== null) return "reused";
        const old = db.query<{ id: number }, [string]>("SELECT id FROM session_sources WHERE selector = ?").get(frozen.session.selector);
        if (old !== null) {
          db.query("DELETE FROM session_terms WHERE rowid IN (SELECT id FROM session_passages WHERE source = ?)").run(old.id);
          db.query("DELETE FROM session_passages WHERE source = ?").run(old.id);
          db.query("DELETE FROM session_sources WHERE id = ?").run(old.id);
        }
        const { session, seen } = frozen;
        const source = db.query(`INSERT INTO session_sources(selector, harness, source_id, path, size, modified_at, schema, detectors, mode)
          VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(session.selector, session.harness, session.sourceId,
          session.primaryPath, seen.size, seen.modifiedAt, context.schema, context.detectors, context.mode).lastInsertRowid;
        const passage = db.query("INSERT INTO session_passages(source) VALUES (?)");
        const terms = db.query("INSERT INTO session_terms(rowid, tokens) VALUES (?, ?)");
        const sink = passages((text) => {
          if (closed) throw new SessionIndexError("unavailable");
          const id = passage.run(source).lastInsertRowid;
          terms.run(id, text);
        });
        const { reading, after } = await read(sink);
        await sink.close();
        if (!observed(after) || after.size !== seen.size || after.modifiedAt !== seen.modifiedAt ||
            reading.bytes !== seen.size) return "changed";
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
      if (text.length > 512 || !Number.isInteger(limit) || limit < 1 || limit > 120 ||
          !Number.isSafeInteger(maxBytes) || maxBytes < 0) throw new SessionIndexError("unavailable");
      const query = termsQuery(text);
      if (query === "") return { selection: [], matches: 0, overBound: 0 };
      active = true;
      try {
        db.exec("BEGIN");
        // This read transaction fixes the identity checks and the search to one publication.
        const candidates = new Map<number, IndexedSession>();
        for (const candidate of eligible) {
          const id = current(candidate);
          if (id !== null && !candidates.has(id)) candidates.set(id, candidate);
        }
        const hits = db.query<{ source: number }, [string]>(
          `SELECT p.source FROM session_terms
           JOIN session_passages p ON p.id = session_terms.rowid
           JOIN session_sources s ON s.id = p.source
           WHERE session_terms MATCH ? ORDER BY session_terms.rank, s.selector`,
        );
        const matched = new Set<number>();
        const selection: SessionRef[] = [];
        let bytes = 0;
        let overBound = 0;
        const bound = Math.min(maxBytes, MAX_MATERIAL_BYTES);
        for (const hit of hits.iterate(query)) {
          const candidate = candidates.get(hit.source);
          if (candidate === undefined || matched.has(hit.source)) continue;
          matched.add(hit.source);
          if (selection.length >= limit) continue;
          if (candidate.seen.size > bound - bytes) { overBound += 1; continue; }
          bytes += candidate.seen.size;
          selection.push(candidate.session);
        }
        db.exec("COMMIT");
        return { selection, matches: matched.size, overBound };
      } catch (error) {
        throw failure(error);
      } finally {
        rollback();
        active = false;
      }
    },
    close() {
      if (closed) return;
      rollback();
      closed = true;
      try { db.close(); } catch (error) { throw failure(error); }
    },
  };
}
