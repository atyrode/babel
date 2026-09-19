/*
  THE verify OPERATION (#338): the archive's READING half.

  `archive` fills the repository and, until this operation existed, nothing read one back. An
  archive whose restore path lives only in an operator's head is an archive nobody has tested,
  so this operation runs restic's reading verbs and reports what they said:

    THE CHECK     the repository's structure always — its index, its trees, its pack headers —
                  and its stored bytes when the input asks for them (`--read-data`, or a subset
                  for a repository too large to read whole on a cadence). It is not optional:
                  the operation is called `verify`, and a job that skipped it would leave the
                  archive's integrity exactly where it was.

    THE PROOF     one CATALOGUED SESSION, restored from a named snapshot and proved BYTE-EXACT.
                  It is asked for by selector and never by path, and the path is resolved out of
                  the SNAPSHOT: the snapshot is listed, every file in it is offered to the same
                  adapters that catalogued it, and the entry whose selector matches is the one
                  restored. So a session whose log was deleted from the machine — the case an
                  archive exists for — is still restorable, which a path derived from the live
                  filesystem would not be.

                  Then: the archived bytes are dumped and digested without touching a disk, the
                  files are restored, and the restored file's digest is compared against BOTH
                  the dump and the catalogue's own `content_digest`. Two independent answers,
                  because a restore that agreed with the repository while disagreeing with what
                  was archived is the failure worth catching.

  NOTHING IS DELETED AND NOTHING CAN BE. `machine/restic.ts` admits a closed set of verbs and
  `forget`, `prune`, `repair` and `unlock` are not in it, so this operation cannot remove a
  snapshot, a pack or another process's lock however it is asked to.

  The repository and the secrets that open it arrive exactly as `archive`'s do — this job's own
  service binding, read through `resticConfig`, and nowhere else. One delivery, one set of
  refusals, one way a password reaches restic: its child's environment, never argv.

  A restore WRITES TO THE MACHINE and never to the repository. Into a directory under TMPDIR
  that is removed again when the input names no target, because the proof is the comparison and
  not the copy; into the named target, and kept, when it does.
*/

import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";
import { z } from "zod";
import type { Receipt } from "../contract.ts";
import { contentDigest, type SessionRef } from "./adapters/index.ts";
import type { OutputSink } from "./output.ts";
import { ResticError, openRepo, resticConfig, type ArchivedEntry, type Repo } from "./restic.ts";

/** The one binding this operation reads from the environment, for the reason
 *  `RESTIC_ENV.cacheDir` is read: a fixed, reviewed, non-secret path inside a location the
 *  manifest declares writable. Absent — a hand-run outside a job — leaves the system's own. */
export const VERIFY_ENV = {
  scratchDir: "BABEL_VERIFY_SCRATCH_DIR",
} as const;

/** restic's own subset spelling: `n/t` for one part in t, a percentage, or a size. */
const READ_DATA_SUBSET = /^([0-9]+\/[0-9]+|[0-9]+(\.[0-9]+)?%|[0-9]+[kKmMgGtT]?)$/;

/**
 * THE SESSION THIS RUN PROVES RECOVERABLE, as the hub's own catalog names one.
 *
 * `selector` and `digest` are `sessions.selector` and `sessions.content_digest`; `snapshotId` is
 * `sessions.snapshot_id` or whichever snapshot the operator wants read instead. Every field is
 * a row the hub already holds, so a caller asks with facts it has rather than facts it invents.
 */
const RestoreRequestSchema = z.strictObject({
  snapshotId: z
    .string()
    .trim()
    .regex(/^(latest|[0-9a-f]{8,64})$/),
  selector: z.string().trim().min(1).max(400),
  /** `sessions.content_digest`, or empty when the catalog holds none to compare against. */
  digest: z.union([z.literal(""), z.string().regex(/^sha256:[0-9a-f]{64}$/)]).default(""),
  /** Where the restored files are KEPT. Empty restores into a scratch directory this job
   *  removes again, which is what a verification wants: the comparison, not the copy. */
  target: z.string().trim().max(4096).default(""),
});

export const VerifyInputSchema = z.strictObject({
  /** The run this job is. Empty mints one (see archive.ts). */
  runId: z.string().trim().max(120).default(""),
  /** The machine's identity, recorded on the receipt as every operation's is. */
  machineId: z.string().trim().min(1).max(120),
  /**
   * How deep the check reads: `false` verifies the structure, `true` reads every data blob, a
   * string is restic's subset spelling. ONE field rather than a flag with a subset beside it,
   * because two fields can contradict each other and this one cannot.
   */
  readData: z
    .union([z.boolean(), z.string().trim().max(16).regex(READ_DATA_SUBSET)])
    .default(false),
  restore: RestoreRequestSchema.optional(),
});
export type VerifyInput = z.infer<typeof VerifyInputSchema>;

export interface VerifyDeps {
  /** The session one archived path is the primary log of, or null when no adapter claims it —
   *  the same function `archive` catalogues with, so a selector means the same thing on the
   *  way out of the repository as it did on the way in. */
  claim(path: string): SessionRef | null;
  /** Where the engine materialized the storage service binding for this job. */
  credentialFile: string;
  /** The directory an unnamed restore is written under and then removed from. A job's own
   *  TMPDIR is not a promise the sandbox makes, so the operation is GIVEN a writable place
   *  inside a location its manifest declares, exactly as restic is given its cache. */
  scratchDir: string;
}

/**
 * The largest session one job proves. The dumped bytes are held in memory to be digested and
 * the declared job is a gigabyte; the largest log this corpus has ever held is 240 MB, so this
 * covers it and still refuses by name, with the size, rather than dying on an allocation.
 */
const MAX_PROVEN_BYTES = 256 << 20;

/** How much of a failure list a receipt's reason carries. */
const MAX_REASON = 1000;

/** What one verification learned. Every key is present, so a reader can tell "nothing was
 *  restored" from "this run did not try". */
interface VerifyWork {
  readonly counts: Record<string, number>;
  readonly closure: Receipt["closure"];
  readonly reason: string;
}

export async function verify(
  input: VerifyInput,
  out: OutputSink,
  deps: VerifyDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const work = await look(input, deps);
  // No output document: a verification observes the archive and writes nothing into the store's
  // tables. The receipt is the whole of what it produced, and the `run` door (`doors/read.ts`)
  // serves it back verbatim.
  const receipt: Receipt = {
    runId,
    kind: "verify",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure: work.closure,
    counts: work.counts,
    ...(work.reason === "" ? {} : { reason: work.reason.slice(0, MAX_REASON) }),
  };
  await out.receipt(receipt);
  return receipt;
}

async function look(input: VerifyInput, deps: VerifyDeps): Promise<VerifyWork> {
  const counts: Record<string, number> = {
    checked: 0,
    checkErrors: 0,
    brokenPacks: 0,
    dataRead: 0,
    restored: 0,
    /** Whether the CATALOG's digest was part of the comparison, and not only the snapshot's
     *  own bytes. An imported row carries a digest this hub cannot read, and the door drops
     *  it rather than refusing to restore the session most likely to need it. */
    digestCompared: 0,
    archivedBytes: 0,
    restoredBytes: 0,
  };

  let config;
  try {
    config = await resticConfig({ credentialFile: deps.credentialFile, env: process.env });
  } catch (err) {
    if (err instanceof ResticError) return { counts, closure: "failed", reason: describe(err) };
    throw err;
  }

  const repo = openRepo(config);
  const failures: string[] = [];
  try {
    if (!(await repo.exists())) {
      // The same rule `archive` holds: a repository is created once, by hand, for the
      // deployment. A verification that created one would report a pristine empty archive.
      return {
        counts,
        closure: "failed",
        reason: `no repository at ${config.repository}: a deployment's repository is created once, by hand`,
      };
    }
    const check = await repo.check({ readData: input.readData });
    counts.checked = 1;
    counts.checkErrors = check.errorCount;
    counts.brokenPacks = check.brokenPacks.length;
    counts.dataRead = check.dataRead === "" ? 0 : 1;
    if (!check.ok) {
      failures.push(
        `the repository reports ${check.errorCount} ` +
          `${check.errorCount === 1 ? "error" : "errors"}: ${check.errors.join("; ")}`,
      );
    }

    if (input.restore !== undefined) {
      const proved = await prove(repo, deps, input.restore);
      counts.archivedBytes = proved.archivedBytes;
      counts.restoredBytes = proved.restoredBytes;
      if (proved.reason === "") {
        counts.restored = 1;
        counts.digestCompared = input.restore.digest === "" ? 0 : 1;
      } else {
        failures.push(proved.reason);
      }
    }
  } catch (err) {
    if (err instanceof ResticError) return { counts, closure: "failed", reason: describe(err) };
    throw err;
  }

  if (failures.length > 0) return { counts, closure: "failed", reason: failures.join("; ") };
  return { counts, closure: "completed", reason: "" };
}

/** What a restore proved, and the sentence naming what it did not. */
interface Proof {
  readonly archivedBytes: number;
  readonly restoredBytes: number;
  readonly reason: string;
}

/**
 * ONE SESSION, BROUGHT BACK AND COMPARED.
 *
 * The listing comes first, and not only to find the path: it is the cheap answer to the common
 * mistake — a snapshot that does not hold this session at all. Restoring into an empty
 * directory and then wondering why would cost a repository read and say nothing.
 */
async function prove(
  repo: Repo,
  deps: VerifyDeps,
  asked: z.infer<typeof RestoreRequestSchema>,
): Promise<Proof> {
  const empty = { archivedBytes: 0, restoredBytes: 0 };
  const listed = await repo.ls(asked.snapshotId);
  let file: ArchivedEntry | null = null;
  for (const entry of listed.entries) {
    if (entry.type !== "file") continue;
    if (deps.claim(entry.path)?.selector === asked.selector) {
      file = entry;
      break;
    }
  }
  if (file === null) {
    return {
      ...empty,
      reason:
        `snapshot ${asked.snapshotId} holds no session ${asked.selector}` +
        (listed.truncated ? ", and its listing was longer than one job reads" : ""),
    };
  }
  if (file.size > MAX_PROVEN_BYTES) {
    return {
      ...empty,
      reason:
        `${asked.selector} is ${file.size} bytes in ${asked.snapshotId} and one job proves up ` +
        `to ${MAX_PROVEN_BYTES}: restore it to a named target and compare it there`,
    };
  }

  const archived = await repo.dump(asked.snapshotId, file.path, { maxBytes: file.size });
  const archivedDigest = `sha256:${new Bun.CryptoHasher("sha256").update(archived).digest("hex")}`;
  // An unnamed target is the proof's own scratch space: the comparison is what this run
  // produces, so keeping the copy would leave a session's bytes in a sandbox for nothing.
  const minted = asked.target === "";
  let target = asked.target;
  if (minted) {
    // The declared location is `/home/job/.cache`; the subdirectory the manifest names inside
    // it is this operation's to create, and a hand-run's scratch path may not exist at all.
    mkdirSync(deps.scratchDir, { recursive: true });
    target = mkdtempSync(join(deps.scratchDir, "babel-verify-"));
  }
  try {
    await repo.restore(asked.snapshotId, { target, include: [file.path] });
    // restic recreates each file's recorded absolute path underneath the target.
    const restoredPath = join(target, file.path);
    if (!(await Bun.file(restoredPath).exists())) {
      return {
        archivedBytes: archived.byteLength,
        restoredBytes: 0,
        reason: `the restore of ${asked.selector} from ${asked.snapshotId} wrote no file`,
      };
    }
    const restored = await contentDigest(restoredPath);
    const held = { archivedBytes: archived.byteLength, restoredBytes: restored.size };
    if (restored.digest !== archivedDigest) {
      return {
        ...held,
        reason:
          `the restored ${asked.selector} is ${restored.digest} and the snapshot's own bytes ` +
          `are ${archivedDigest}`,
      };
    }
    if (asked.digest !== "" && restored.digest !== asked.digest) {
      return {
        ...held,
        reason: `${asked.selector} restores as ${restored.digest} and was catalogued as ${asked.digest}`,
      };
    }
    return { ...held, reason: "" };
  } finally {
    if (minted) rmSync(target, { recursive: true, force: true });
  }
}

/** One restic failure as a receipt's reason: what failed and restic's own diagnosis, which is
 *  where the remedy is written. */
function describe(err: ResticError): string {
  return err.stderr === "" ? err.message : `${err.message}: ${err.stderr}`;
}
