import type { RecordSink } from "./output.ts";
import type { SecretScan } from "./preflight.ts";

export interface SessionDigests {
  readonly captureDigest: string;
  readonly sourceDigest: string;
  /** Every input byte, including blank lines and malformed records. */
  readonly bytes: number;
  /** Nonblank normalized records, also the line count of the sealed stream. */
  readonly records: number;
}

/** A record's size limit in UTF-16 code units. A surrogate pair crossing the limit starts
 *  the next piece intact, so opaque records never lose a decoded Unicode character. */
const MAX_RECORD_CHARS = 4 << 20;

/**
 * Normalized, newline-terminated records from bytes supplied by the caller. There is no source
 * lookup here: a live file and an explicitly selected archive feed the same decoder and rule.
 * Blank pieces are omitted; line numbers are the 1-based ordinals of the emitted records.
 *
 * Newlines and size boundaries compete in content order, never chunk order. In particular a
 * newline delivered with an oversized chunk cannot bypass the size limit. One code unit of
 * lookahead keeps a surrogate pair intact, including when its UTF-8 bytes span writes.
 */
export function recordReader(onRecord: (normalized: string, line: number) => void): {
  write(chunk: Uint8Array): void;
  finish(): void;
} {
  const decoder = new TextDecoder();
  let pending = "";
  let records = 0;
  const record = (line: string): void => {
    const normalized = normalize(line);
    if (normalized !== "") onRecord(normalized, ++records);
  };
  const decoded = (text: string): void => {
    pending += text;
    let start = 0;
    let newline = pending.indexOf("\n");
    while (start < pending.length) {
      if (newline >= 0 && newline - start <= MAX_RECORD_CHARS) {
        record(pending.slice(start, newline));
        start = newline + 1;
        newline = pending.indexOf("\n", start);
      } else if (pending.length - start > MAX_RECORD_CHARS) {
        let end = start + MAX_RECORD_CHARS;
        const last = pending.charCodeAt(end - 1);
        const next = pending.charCodeAt(end);
        if (last >= 0xd800 && last <= 0xdbff && next >= 0xdc00 && next <= 0xdfff) end -= 1;
        record(pending.slice(start, end));
        start = end;
      } else break;
    }
    if (start !== 0) pending = pending.slice(start);
  };
  return {
    write: (chunk) => decoded(decoder.decode(chunk, { stream: true })),
    finish: () => {
      decoded(decoder.decode());
      record(pending);
      pending = "";
    },
  };
}

/**
 * One pass captures every supplied byte and hashes exactly the normalized stream handed to the
 * sink. Scanning precedes both source hashing and sealing: a redacted reading describes the
 * bytes its consumer receives, not the original secrets. The caller owns the sink's lifetime.
 */
export function sessionDigester(seal?: RecordSink, scan?: SecretScan): {
  write(chunk: Uint8Array): void;
  finish(): SessionDigests;
} {
  const capture = new Bun.CryptoHasher("sha256");
  const source = new Bun.CryptoHasher("sha256");
  let bytes = 0;
  let records = 0;
  const reader = recordReader((normalized, line) => {
    records = line;
    const served = scan === undefined ? normalized : scan.redact(normalized, line);
    source.update(served);
    seal?.write(served);
  });
  return {
    write: (chunk) => {
      capture.update(chunk);
      bytes += chunk.byteLength;
      reader.write(chunk);
    },
    finish: () => {
      reader.finish();
      return {
        captureDigest: `sha256:${capture.digest("hex")}`,
        sourceDigest: `sha256:${source.digest("hex")}`,
        bytes,
        records,
      };
    },
  };
}

/** Canonical JSON ignores key order and whitespace; torn or corrupt lines remain evidence
 *  behind a marker canonical JSON can never start with. */
function normalize(line: string): string {
  if (line.trim() === "") return "";
  try {
    return `${canonical(JSON.parse(line))}\n`;
  } catch {
    return `!${line}\n`;
  }
}

function canonical(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  const keys = Object.keys(value).sort();
  const fields = keys.map(
    (key) => `${JSON.stringify(key)}:${canonical((value as Record<string, unknown>)[key])}`,
  );
  return `{${fields.join(",")}}`;
}
