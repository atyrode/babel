/*
  THE archive OPERATION (plan §4): `restic backup` of this machine's session roots, tagged
  `babel` and attributed to the machine's own identity, writing back the one fact the hub's
  catalog cannot learn any other way — which snapshot holds each session, and when.

  ONE SNAPSHOT PER ROOT, not one for all of them. restic picks a backup's parent by matching
  host and the snapshot's path set, so a machine that gains a harness would, with one combined
  snapshot, find no parent and re-read every byte of every root. Per-root snapshots keep each
  root's parent chain stable across that change, let one unreadable root fail without taking
  the others with it, and make restoring a single harness's sessions a restore of one snapshot.

  The repository and the secrets that open it come from the job's own service binding
  (`RESTIC_SERVICE`, read through `resticConfig`) and from nowhere else: this operation reads
  no credential file of the operator's, holds no environment secret, and creates no repository.
  A repository is created once, by hand, for the deployment — silent creation would turn a
  mistyped locator into a second, empty archive that grows happily while the real one appears
  to stop, and two concurrent creations corrupt.
*/

import { z } from "zod";
import type { Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputSink } from "./output.ts";
import { BABEL_TAG, ResticError, openRepo, resticConfig } from "./restic.ts";

export const ArchiveInputSchema = z.strictObject({
  /** The run this job is. Empty mints one: a scheduled beat's input is fixed at registration,
   *  so the hub cannot name a run per occurrence and the machine half names it instead. */
  runId: z.string().trim().max(120).default(""),
  /** The machine's identity, which is restic's `--host` and the catalog row's `host`. */
  machineId: z.string().trim().min(1).max(120),
  /** The roots to archive. Empty is every adapter's own root that exists on this host. */
  roots: z.array(z.string().trim().min(1).max(4096)).max(64).default([]),
});
export type ArchiveInput = z.infer<typeof ArchiveInputSchema>;

/** The machine facts and job bindings this operation needs; the adapters own the facts
 *  (machine/adapters) and the dispatcher owns the binding (machine/main.ts). */
export interface ArchiveDeps {
  /** Every adapter's backup root that exists on this host. */
  roots(): Promise<readonly string[]>;
  /** The session one archived path is the primary log of, or null when no adapter claims it. */
  claim(path: string): SessionRef | null;
  /** Where the engine materialized the storage service binding for this job. */
  credentialFile: string;
}

/**
 * One `sessions` row as the archive observed it.
 *
 * The columns are exactly what a backup learns and no more: an ingest that updates the named
 * columns only can therefore write this row for a selector `scan` also wrote, without either
 * clobbering the other's facts. Column names are the store's own (store/schema.ts).
 */
interface ArchivedSessionRow {
  readonly selector: string;
  readonly host: string;
  readonly harness: string;
  readonly source_id: string;
  readonly snapshot_id: string;
  readonly archived_at: string;
  readonly seen_at: string;
}

/** What a backup pass tallied. Every key is present, so a reader of the receipt's counts can
 *  tell "nothing was archived" from "this run did not look". */
type ArchiveTallies = {
  roots: number;
  snapshots: number;
  sessions: number;
  unclaimed: number;
  filesNew: number;
  filesChanged: number;
  filesUnmodified: number;
  bytesProcessed: number;
  dataAdded: number;
  rootsIncomplete: number;
  unreadable: number;
};

interface ArchiveWork {
  readonly rows: readonly ArchivedSessionRow[];
  readonly tallies: ArchiveTallies;
  /** How many of the new snapshots linked to a parent, or null when the listing that answers
   *  it could not be read: an unknown fact is not the fact "none of them linked". */
  readonly parented: number | null;
  readonly closure: Receipt["closure"];
  readonly reason: string;
}

/** How much of a failure list a receipt's reason carries. */
const MAX_REASON = 1000;

export async function archive(
  input: ArchiveInput,
  out: OutputSink,
  deps: ArchiveDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const work = await backUp(input, deps);
  // The rows go out before the receipt, always — including the empty document, which is what
  // an operation that declares an output owes its job even when it archived nothing.
  await out.write("sessions", work.rows);
  const receipt: Receipt = {
    runId,
    kind: "archive",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure: work.closure,
    counts: work.parented === null ? { ...work.tallies } : { ...work.tallies, snapshotsParented: work.parented },
    ...(work.reason === "" ? {} : { reason: work.reason.slice(0, MAX_REASON) }),
  };
  await out.receipt(receipt);
  return receipt;
}

async function backUp(input: ArchiveInput, deps: ArchiveDeps): Promise<ArchiveWork> {
  const counts: ArchiveTallies = {
    roots: 0,
    snapshots: 0,
    sessions: 0,
    unclaimed: 0,
    filesNew: 0,
    filesChanged: 0,
    filesUnmodified: 0,
    bytesProcessed: 0,
    dataAdded: 0,
    rootsIncomplete: 0,
    unreadable: 0,
  };
  const roots = input.roots.length > 0 ? [...input.roots].sort() : [...(await deps.roots())];
  if (roots.length === 0) {
    // A machine that runs no harness yet is not a failure, and must not look like a
    // successful backup either. Nothing is asked of the storage service for it: a machine with
    // nothing to archive must not be able to fail on the operator's policy.
    return {
      rows: [],
      tallies: counts,
      parented: null,
      closure: "skipped",
      reason: "no session root exists on this host",
    };
  }
  counts.roots = roots.length;

  let config;
  try {
    config = await resticConfig({ credentialFile: deps.credentialFile, env: process.env });
  } catch (err) {
    if (err instanceof ResticError) {
      return { rows: [], tallies: counts, parented: null, closure: "failed", reason: describe(err) };
    }
    throw err;
  }

  const repo = openRepo(config);
  try {
    if (!(await repo.exists())) {
      return {
        rows: [],
        tallies: counts,
        parented: null,
        closure: "failed",
        reason: `no repository at ${config.repository}: a deployment's repository is created once, by hand`,
      };
    }
  } catch (err) {
    if (err instanceof ResticError) {
      return { rows: [], tallies: counts, parented: null, closure: "failed", reason: describe(err) };
    }
    throw err;
  }

  const seenAt = new Date().toISOString();
  const rows = new Map<string, ArchivedSessionRow>();
  const minted: string[] = [];
  const failures: string[] = [];
  const incomplete: string[] = [];
  for (const root of roots) {
    let outcome;
    try {
      outcome = await repo.backup([root], { host: input.machineId, tags: [BABEL_TAG] });
    } catch (err) {
      if (err instanceof ResticError) {
        failures.push(`${root}: ${describe(err)}`);
        continue;
      }
      throw err;
    }
    counts.snapshots += 1;
    counts.filesNew += outcome.filesNew;
    counts.filesChanged += outcome.filesChanged;
    counts.filesUnmodified += outcome.filesUnmodified;
    counts.bytesProcessed += outcome.bytesProcessed;
    counts.dataAdded += outcome.dataAdded;
    counts.unreadable += outcome.unreadable.length;
    minted.push(outcome.snapshotId);
    if (outcome.incomplete) incomplete.push(root);
    for (const item of outcome.items) {
      const session = deps.claim(item.path);
      if (session === null) {
        // A root holds more than sessions — a blob store, an index, a lock — and a file no
        // adapter claims is archived all the same; it simply names no catalog row.
        counts.unclaimed += 1;
        continue;
      }
      rows.set(session.selector, {
        selector: session.selector,
        host: input.machineId,
        harness: session.harness,
        source_id: session.sourceId,
        snapshot_id: outcome.snapshotId,
        // restic's own recorded time for the snapshot, to the nanosecond, so a catalog row and
        // `restic snapshots` never disagree about when this capture was taken.
        archived_at: outcome.startedAt,
        seen_at: seenAt,
      });
    }
  }
  counts.sessions = rows.size;

  // Whether each new snapshot linked to a parent, which is the difference between an
  // incremental backup and restic having re-read the whole root. A listing that fails leaves
  // the count unknown rather than zero: the archive is already durable either way.
  let parented: number | null = null;
  try {
    const parents = new Map((await repo.snapshots()).map((snapshot) => [snapshot.id, snapshot.parentId]));
    parented = minted.filter((id) => (parents.get(id) ?? null) !== null).length;
  } catch (err) {
    if (!(err instanceof ResticError)) throw err;
  }

  if (failures.length > 0) {
    return { rows: [...rows.values()], tallies: counts, parented, closure: "failed", reason: failures.join("; ") };
  }
  if (incomplete.length > 0) {
    counts.rootsIncomplete = incomplete.length;
    // The snapshots are real and already recorded, but a source file Babel could not read is a
    // failure the operator has to see rather than a quieter, smaller archive.
    return {
      rows: [...rows.values()],
      tallies: counts,
      parented,
      closure: "failed",
      reason: `${counts.unreadable} unreadable ${counts.unreadable === 1 ? "path" : "paths"} under ${incomplete.join(", ")}`,
    };
  }
  return { rows: [...rows.values()], tallies: counts, parented, closure: "completed", reason: "" };
}

/** One restic failure as a receipt's reason: what failed and restic's own diagnosis, which is
 *  where the remedy is written. */
function describe(err: ResticError): string {
  return err.stderr === "" ? err.message : `${err.message}: ${err.stderr}`;
}
