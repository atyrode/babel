import { createHash } from "node:crypto";

/*
  THE SHAPES A SETTLEMENT WRITES, AND THE ONE PLACE AN IDENTIFIER IS MINTED.

  A settled Code session becomes rows in the store's own tables, ingested through the same
  `INGEST` map a machine half's sealed output goes through (`server/conductor.ts`): one file key
  per table, each a list of rows whose keys are that table's own column names. Nothing between
  the answer and the table reinterprets a row, so there is exactly one row shape here and the
  column names are the schema's.

  {@link mintId} is the whole of how a run's answer becomes durable identifiers, and it is a
  DIGEST OF THE RUN AND THE MODEL'S OWN HANDLE rather than a counter. That is what makes a
  settlement idempotent: the second settlement of one run mints the identifiers the first one
  did, every insert is `INSERT OR IGNORE` keyed by that identifier, and a retry after a crash
  between two batches neither duplicates a record nor loses one. A counter would produce a
  second corpus of the same claims on every replay.
*/

/** What SQLite holds in one column of a row the ingest writes. */
export type Cell = string | number | null;
/** One row, keyed by the column names of the table it is bound for. */
export type Row = Record<string, Cell>;

/**
 * The durable identifier for one thing a run claimed, from the run and the handle the run gave
 * it. Stable across attempts, unique across runs, and in `RecordIdSchema`'s shape for the four
 * record families (`contract.ts`: a three-letter family and a hex tail).
 */
export function mintId(prefix: string, runId: string, ref: string): string {
  const digest = createHash("sha256").update(`${prefix}\u0000${runId}\u0000${ref}`).digest("hex");
  return `${prefix}_${digest.slice(0, 32)}`;
}

/** A title as the `records` column holds it: one line, bounded, never silently dropped. */
export function titleCell(title: string): string {
  const line = title.replace(/\s+/gu, " ").trim();
  return line.length <= 200 ? line : `${line.slice(0, 199)}…`;
}
