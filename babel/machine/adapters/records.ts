/*
  READING ONE SESSION LOG, ONCE.

  Every harness writes its sessions as newline-delimited JSON, and a scan wants three things
  from one: the digest of the bytes on disk, their count, and the records themselves. The Go
  tree read the head for metadata and then the whole file again for usage (omp/omp.go,
  omp/usage.go); here the digest already forces a full read, so the metadata, the usage and the
  hash come out of the same pass and a 300 MB transcript is read exactly once.

  Nothing throws over a malformed log. A torn tail, a garbage line, a record too large to hold:
  each is counted and skipped, because restic's snapshots are crash-consistent per file rather
  than transactional across files and a live harness appending while this reads must degrade a
  description instead of failing a scan. The raw bytes are archived either way.
*/

const NEWLINE = 0x0a;

/** How much of one record is held in memory. Bigger records are counted, not assembled. */
export const MAX_RECORD_BYTES = 32 << 20;

export interface ReadRecords {
  /** Canonical "sha256:<64 lowercase hex>" over every byte read, verifiable with sha256sum. */
  digest: string;
  /** Bytes read, which is what was hashed: never a second stat that could disagree. */
  size: number;
  /** Non-blank lines seen, including the ones too long to assemble. */
  records: number;
  /** Lines past MAX_RECORD_BYTES, delivered to no one. */
  oversized: number;
}

/**
 * Streams `path`, hands every non-blank record to `onRecord`, and returns the digest and
 * counts of the bytes it read. A record's text is valid only for the duration of the call.
 */
export async function readRecords(
  path: string,
  onRecord: (record: string) => void,
  maxRecordBytes: number = MAX_RECORD_BYTES,
): Promise<ReadRecords> {
  const hasher = new Bun.CryptoHasher("sha256");
  const decoder = new TextDecoder();
  let size = 0;
  let records = 0;
  let oversized = 0;

  // `held` is the head of a record split across chunks; `dropping` is the tail of one that
  // outgrew the bound and is discarded to its newline, so reading resumes at the next record
  // instead of mistaking the remainder of an outsized line for records of its own.
  let held: Uint8Array[] = [];
  let heldBytes = 0;
  let dropping = false;

  const deliver = (tail: Uint8Array): void => {
    let text: string;
    if (heldBytes === 0) {
      text = decoder.decode(tail);
    } else {
      const whole = new Uint8Array(heldBytes + tail.byteLength);
      let at = 0;
      for (const part of held) {
        whole.set(part, at);
        at += part.byteLength;
      }
      whole.set(tail, at);
      text = decoder.decode(whole);
      held = [];
      heldBytes = 0;
    }
    if (text.trim() === "") return;
    records++;
    onRecord(text);
  };

  for await (const chunk of Bun.file(path).stream()) {
    hasher.update(chunk);
    size += chunk.byteLength;
    let start = 0;
    for (;;) {
      const newline = chunk.indexOf(NEWLINE, start);
      if (newline < 0) break;
      const tail = chunk.subarray(start, newline);
      start = newline + 1;
      if (dropping) {
        dropping = false;
        continue;
      }
      // The bound is on the whole record, whether it arrived in one chunk or ten.
      if (heldBytes + tail.byteLength > maxRecordBytes) {
        records++;
        oversized++;
        held = [];
        heldBytes = 0;
        continue;
      }
      deliver(tail);
    }
    const rest = chunk.subarray(start);
    if (dropping || rest.byteLength === 0) continue;
    if (heldBytes + rest.byteLength > maxRecordBytes) {
      records++;
      oversized++;
      dropping = true;
      held = [];
      heldBytes = 0;
      continue;
    }
    // The tail is copied rather than referenced: a stream may reuse its chunk's buffer, and a
    // record assembled from a recycled one would be a quiet corruption.
    held.push(new Uint8Array(rest));
    heldBytes += rest.byteLength;
  }
  // A log whose last record carries no newline is a log a harness is still writing.
  if (!dropping && heldBytes > 0) deliver(new Uint8Array(0));

  return { digest: "sha256:" + hasher.digest("hex"), size, records, oversized };
}
