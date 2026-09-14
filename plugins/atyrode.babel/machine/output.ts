import { mkdir } from "node:fs/promises";
import { join } from "node:path";

import {
  JOB_OUTPUT_FILES,
  MATERIAL_INDEX,
  MATERIAL_SESSIONS,
  type MaterialIndex,
  type Receipt,
} from "../contract.ts";

/*
  WHERE A MACHINE OPERATION PUTS WHAT IT PRODUCED (plan §4).

  A job hands an operation one directory and the operation writes the documents of
  JOB_OUTPUT_FILES into it, flat, named exactly by the contract's own values. The hub then
  ingests each one as a `batch` of rows in the store's own shapes, so nothing between the
  machine and the table reinterprets a row.

  A file an operation had nothing to say about is not written. That is the one rule worth
  stating: an absent file means "this run produced none", never "this run produced zero", and
  an empty array written for every file would turn every scan into a claim about every table.
*/

/** The key of one job output document; the sink maps it to the contract's filename. */
export type OutputFile = keyof typeof JOB_OUTPUT_FILES;

export interface OutputSink {
  /** Write one job output document; rows are the store's own table-row shapes. */
  write(file: OutputFile, rows: readonly unknown[]): Promise<void>;
  /** The receipt (§7), written last, as one object rather than a list of one. */
  receipt(receipt: Receipt): Promise<void>;
}

/**
 * The sink a job gets: one directory, one file per document. The directory is created on the
 * first write rather than at construction, so an operation that fails before producing
 * anything leaves nothing behind but its receipt.
 */
export function directorySink(dir: string): OutputSink {
  let ensured: Promise<unknown> | null = null;
  const writeFile = async (file: OutputFile, body: unknown): Promise<void> => {
    ensured ??= mkdir(dir, { recursive: true });
    await ensured;
    await Bun.write(join(dir, JOB_OUTPUT_FILES[file]), JSON.stringify(body) + "\n");
  };
  return {
    write: (file, rows) => writeFile(file, rows),
    receipt: (receipt) => writeFile("receipt", receipt),
  };
}

/*
  THE SECOND SEALED OUTPUT: the material (#279).

  `prepare` writes TWO leases. `outputs` is the flat document directory every operation writes —
  rows the hub ingests into its own tables — and `material` is the evidence a run reads: an index
  naming the selection, and one file per session holding that session's normalized record stream.
  It is a second lease rather than a subdirectory of the first because it is bound into ANOTHER
  job's sandbox whole, read-only, at `/inputs/material`, and a binding names an output.

  The per-session file is the NORMALIZED stream — one canonical JSON record per line — and not a
  copy of the log. Two reasons, one economic and one about meaning: the normalization is already
  computed while the source digest is (`prepare.ts` reads each log once, and reading a 240 MB log
  twice per preparation is the only cost that matters), and the source digest a claim cites is a
  digest OF THAT STREAM. A model citing line 12 of the file it read is citing the bytes a later
  reader recovers by the same digest.
*/

/** Where one session's normalized record stream is written while its digests are computed. */
export interface RecordSink {
  /** One normalized record, newline-terminated, exactly as the source digest covered it. */
  write(record: string): void;
  /** How many records were written; the index records it so a prompt can say the size. */
  close(): Promise<number>;
}

export interface MaterialSink {
  /**
   * Opens the file for one session; `file` is the name the index will name it by. It is async
   * because the directory is made on the first one, and `write` must not be: it is called once
   * per record from inside the single pass over the log.
   */
  session(file: string): Promise<RecordSink>;
  /** The index, written last, as `index.json` at the material's root. */
  index(index: MaterialIndex): Promise<void>;
}

/**
 * The material's sink: one directory, an index at its root and a `sessions/` beside it.
 *
 * The directory is made on the first thing written into it rather than at construction, exactly
 * as `directorySink` makes its own: a preparation that selected nothing leaves an index saying
 * so, and an operation that failed before producing anything leaves the lease as it found it.
 */
export function materialSink(dir: string): MaterialSink {
  const sessions = join(dir, MATERIAL_SESSIONS);
  let ensured: Promise<unknown> | null = null;
  const ready = async (): Promise<void> => {
    ensured ??= mkdir(sessions, { recursive: true });
    await ensured;
  };
  return {
    session: async (file) => {
      await ready();
      const writer = Bun.file(join(sessions, file)).writer();
      let records = 0;
      return {
        write: (record) => {
          records += 1;
          writer.write(record);
        },
        close: async () => {
          await writer.end();
          return records;
        },
      };
    },
    index: async (index) => {
      await ready();
      await Bun.write(join(dir, MATERIAL_INDEX), JSON.stringify(index) + "\n");
    },
  };
}
