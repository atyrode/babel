/*
  THE catalog OPERATION (#453): what the fleet's archive holds, as one `sessions` row per session
  naming the newest CAPTURE that holds it — the snapshot, the path inside it, the host label, and
  the size and modification time restic recorded. It opens no transcript: `ls` reads tree
  metadata, which restic caches under its own cache directory, and no data blob is fetched.

  WHICH SNAPSHOTS. `snapshots` runs once per job and only the snapshots Babel reads as
  transcripts count (`babelSnapshot`): a tag is matched exactly, so the hub's own store, backed up
  into the same repository under `babel-store`, is never catalogued.

  IN WHAT ORDER. The machine keeps a MEMORY of the snapshots it has listed, one small file per
  repository in the managed cache; `full` sets it aside. Of the rest, the newest snapshot of each
  CHAIN — one label and one path set, which is what restic parents a backup by — is listed first,
  because that one snapshot is the chain's whole current view: a fresh hub learns every existing
  session in its first run. Older snapshots follow, newest first; they only add sessions later
  deleted from their machine. `maxSnapshots` bounds one run, and what it leaves is `pending`,
  which the next beat continues from.

  WHAT A ROW SAYS. Within one run only the newest capture of each session under each label is
  written; across runs the hub's ingest keeps the newest (`store/sessions.ts`). Instants are
  normalized to UTC (`captureInstant`), because restic spells them in the backing machine's own
  zone and the hub compares them as text. There is no `host` — a label is not a machine id — no
  content fact and no liveness: a capture never moves.

  The memory is committed only after the rows are written, and it records only snapshots whose
  listing completed. A job that dies in between lists those snapshots again, which is harmless:
  the hub's ingest is idempotent. The memory is convenience state, and losing it costs one run
  that lists everything again.
*/

import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { z } from "zod";
import {
  ARCHIVE_LABELS_REPORTED,
  SnapshotIdSchema,
  type CatalogInput,
  type Receipt,
  type SessionRow,
} from "../contract.ts";
import { babelOwnLog } from "./adapters/index.ts";
import { babelSnapshot, captureInstant, capturesOf } from "./archive-listing.ts";
import type { OutputSink } from "./output.ts";
import { ResticError, type Repo, type Snapshot } from "./restic.ts";

/** The one binding this operation reads from the environment: where its memory is kept, a
 *  fixed, reviewed path inside the managed cache the manifest declares writable. */
export const CATALOG_ENV = {
  cacheDir: "BABEL_CATALOG_CACHE_DIR",
} as const;

/** What a catalog asks of the repository: two reads, and the locator its memory is keyed on. */
export type CatalogRepo = Pick<Repo, "repository" | "snapshots" | "lsTo">;

/** The machine facts and job bindings this operation needs; the dispatcher owns them
 *  (machine/main.ts). */
export interface CatalogDeps {
  /** The repository this job's service binding opens. A failure to open it fails the run
   *  whole: there is nothing else to catalogue, and nothing local is read instead. */
  archive(): Promise<CatalogRepo>;
  /** Where the memory of listed snapshots is kept. Empty keeps none, so every snapshot is
   *  pending on every run, as under `full`. */
  cacheDir: string;
  /** The named-output lease's capacity, or null where it cannot be measured. */
  capacity(): Promise<NonNullable<Receipt["outputCapacity"]> | null>;
}

/** What one run counted. Every key is present, so a reader can tell "nothing was listed" from
 *  "this run did not look". */
interface CatalogCounts {
  /** `babel` snapshots the repository holds. */
  snapshots: number;
  /** Snapshots this run listed to the end. */
  listed: number;
  /** Snapshots not yet listed after this run: the next beat's work. */
  pending: number;
  /** Nodes the listings held, sessions or not: the listings' own cost. */
  entries: number;
  /** Rows written: one per session and label. */
  captures: number;
}

interface CatalogWork {
  readonly counts: CatalogCounts;
  /** Null when the archive was never read, which writes no `sessions` document at all. */
  readonly rows: readonly SessionRow[] | null;
  readonly archive: NonNullable<Receipt["archive"]> | null;
  /** The memory to commit once the rows are written, or null to leave it as it is. */
  readonly memory: { readonly path: string; readonly listed: readonly string[] } | null;
  readonly closure: Receipt["closure"];
  readonly reason: string;
}

/** One snapshot, with its time in the one spelling a row carries. */
interface Taken {
  readonly snapshot: Snapshot;
  readonly at: string;
}

/** The memory's whole document: the ids of the snapshots listed so far. */
const MemorySchema = z.strictObject({ listed: z.array(SnapshotIdSchema) });

/** How much of a failure a receipt's reason carries. */
const MAX_REASON = 1000;

const sha256 = (value: string): string =>
  new Bun.CryptoHasher("sha256").update(value).digest("hex");

export async function catalog(
  input: CatalogInput,
  out: OutputSink,
  deps: CatalogDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const capacity = await deps.capacity();
  const work = await look(input, deps);
  let closure = work.closure;
  let reason = work.reason;
  if (work.rows !== null) await out.write("sessions", work.rows);
  if (work.memory !== null) {
    try {
      await remember(work.memory.path, work.memory.listed);
    } catch (err) {
      // The rows stand; only the next run's starting point is lost, and it would list these
      // snapshots again for as long as the cache refuses writes — which the operator must see.
      closure = "failed";
      const cause = err instanceof Error ? err.message : String(err);
      reason = [reason, `the catalog memory could not be kept: ${cause}`]
        .filter((part) => part !== "")
        .join("; ");
    }
  }
  const receipt: Receipt = {
    runId,
    kind: "catalog",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure,
    counts: { ...work.counts },
    ...(work.archive === null ? {} : { archive: work.archive }),
    ...(capacity === null ? {} : { outputCapacity: capacity }),
    ...(reason === "" ? {} : { reason: reason.slice(0, MAX_REASON) }),
  };
  await out.receipt(receipt);
  return receipt;
}

async function look(input: CatalogInput, deps: CatalogDeps): Promise<CatalogWork> {
  const counts: CatalogCounts = { snapshots: 0, listed: 0, pending: 0, entries: 0, captures: 0 };
  const unread = (reason: string): CatalogWork => ({
    counts,
    rows: null,
    archive: null,
    memory: null,
    closure: "failed",
    reason,
  });

  let repo: CatalogRepo;
  try {
    repo = await deps.archive();
  } catch (err) {
    if (err instanceof ResticError) return unread(describe(err));
    throw err;
  }
  const taken: Taken[] = [];
  try {
    for (const snapshot of await repo.snapshots()) {
      const at = captureInstant(snapshot.time);
      if (babelSnapshot(snapshot) && at !== null) taken.push({ snapshot, at });
    }
  } catch (err) {
    if (!(err instanceof ResticError)) throw err;
    // The same rule `archive` and `verify` hold: a repository is created once, by hand, and a
    // reading never creates one.
    return unread(
      err.kind === "exit" && err.missingRepository
        ? `no repository at ${repo.repository}: a deployment's repository is created once, by hand`
        : describe(err),
    );
  }
  taken.sort(newestFirst);
  counts.snapshots = taken.length;

  const path = deps.cacheDir === "" ? null : join(deps.cacheDir, `${sha256(repo.repository)}.json`);
  const stored = path === null ? [] : await recall(path);
  const present = new Set(taken.map(({ snapshot }) => snapshot.id));
  // A remembered id the repository no longer lists as `babel` is dropped, so the memory only
  // ever names snapshots that exist.
  const remembered = input.full ? [] : stored.filter((id) => present.has(id));
  const known = new Set(remembered);

  const chains = new Set<string>();
  const heads: Taken[] = [];
  const older: Taken[] = [];
  for (const entry of taken) {
    const chain = JSON.stringify([entry.snapshot.host, [...entry.snapshot.paths].sort()]);
    const head = !chains.has(chain);
    chains.add(chain);
    if (known.has(entry.snapshot.id)) continue;
    (head ? heads : older).push(entry);
  }
  const pending = [...heads, ...older];

  const newest = new Map<string, { readonly row: SessionRow; readonly taken: Taken }>();
  const listed: string[] = [];
  let reason = "";
  for (const entry of pending.slice(0, input.maxSnapshots)) {
    const { snapshot, at } = entry;
    const found: SessionRow[] = [];
    try {
      await capturesOf(repo, snapshot, {
        entry: () => {
          counts.entries++;
        },
        capture: (session, node) => {
          found.push({
            selector: session.selector,
            harness: session.harness,
            source_id: session.sourceId,
            kind: babelOwnLog(node.path) ? "agent" : "operator",
            archive_label: snapshot.host,
            archive_path: node.path,
            snapshot_id: snapshot.id,
            archived_at: at,
            size: node.size,
            // A node restic could not time is dated by its snapshot, as Recall dates it.
            modified_at: captureInstant(node.modifiedAt) ?? at,
          });
        },
      });
    } catch (err) {
      if (!(err instanceof ResticError)) throw err;
      // An archive that stops answering mid-run keeps what was listed to the end; the rest,
      // this snapshot included, stays pending for the next beat.
      reason = `snapshot ${snapshot.id} could not be listed: ${describe(err)}`;
      break;
    }
    listed.push(snapshot.id);
    for (const row of found) {
      const key = JSON.stringify([row.archive_label, row.selector]);
      const previous = newest.get(key);
      if (previous === undefined || newer({ row, taken: entry }, previous)) {
        newest.set(key, { row, taken: entry });
      }
    }
  }

  const rows = [...newest.values()]
    .map(({ row }) => row)
    .sort((a, b) => compare(a.selector, b.selector) || compare(a.archive_label, b.archive_label));
  counts.listed = listed.length;
  counts.pending = pending.length - listed.length;
  counts.captures = rows.length;
  const next = [...remembered, ...listed].sort();
  return {
    counts,
    rows,
    archive: labelsOf(taken),
    memory:
      path === null || (listed.length === 0 && next.length === stored.length)
        ? null
        : { path, listed: next },
    closure: reason === "" ? "completed" : "failed",
    reason,
  };
}

/** Newest first, the id breaking a tie, as Recall orders snapshots. */
function newestFirst(a: Taken, b: Taken): number {
  return compare(b.at, a.at) || a.snapshot.id.localeCompare(b.snapshot.id);
}

/**
 * Whether a capture supersedes the one already kept for its session and label: a newer snapshot
 * wins, and within one snapshot the path Recall would pick, so both readers of the archive name
 * the same capture.
 */
function newer(
  candidate: { readonly row: SessionRow; readonly taken: Taken },
  kept: { readonly row: SessionRow; readonly taken: Taken },
): boolean {
  const order = newestFirst(candidate.taken, kept.taken);
  return order !== 0
    ? order < 0
    : candidate.row.archive_path.localeCompare(kept.row.archive_path) < 0;
}

/** Code-unit order, for strings whose spelling is fixed (instants, selectors, labels). */
function compare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Every label the archive holds, newest first, bounded for the receipt. `taken` is newest
 *  first, so a label's first sighting is its newest snapshot. */
function labelsOf(taken: readonly Taken[]): NonNullable<Receipt["archive"]> {
  const labels = new Map<string, { label: string; snapshots: number; newestAt: string }>();
  for (const { snapshot, at } of taken) {
    const seen = labels.get(snapshot.host);
    if (seen === undefined)
      labels.set(snapshot.host, { label: snapshot.host, snapshots: 1, newestAt: at });
    else seen.snapshots++;
  }
  const all = [...labels.values()];
  return {
    labels: all.slice(0, ARCHIVE_LABELS_REPORTED),
    omitted: Math.max(0, all.length - ARCHIVE_LABELS_REPORTED),
  };
}

/** The ids the memory holds, or none when it is absent or unreadable: it is rebuildable, and
 *  an unreadable memory costs one run that lists everything again. */
async function recall(path: string): Promise<readonly string[]> {
  try {
    const parsed = MemorySchema.safeParse(JSON.parse(await readFile(path, "utf8")));
    return parsed.success ? parsed.data.listed : [];
  } catch {
    return [];
  }
}

/** Replaces the memory whole, so a job killed while writing it leaves the previous one. */
async function remember(path: string, listed: readonly string[]): Promise<void> {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const staged = `${path}.${crypto.randomUUID()}.tmp`;
  try {
    await writeFile(staged, JSON.stringify({ listed }) + "\n", { mode: 0o600 });
    await rename(staged, path);
  } catch (err) {
    await rm(staged, { force: true });
    throw err;
  }
}

/** One restic failure as a receipt's reason: what failed and restic's own diagnosis, which is
 *  where the remedy is written. */
function describe(err: ResticError): string {
  return err.stderr === "" ? err.message : `${err.message}: ${err.stderr}`;
}
