import { mkdir } from "node:fs/promises";
import { join } from "node:path";

import { JOB_OUTPUT_FILES, type Receipt } from "../contract.ts";

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
