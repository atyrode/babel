/*
  THE RESTIC INVOCATION of the machine half. restic owns Babel's archival storage — the
  session logs of every enrolled machine, deduplicated into one repository — and Babel never
  reimplements a repository format. This is a thin, contract-bearing shell over the binary:

  - The repository and the secrets that open it arrive through the job's own SERVICE BINDING
    (`RESTIC_SERVICE` in contract.ts) and from nowhere else. The engine materializes that
    binding into `RESTIC_CREDENTIAL_FILE` as the loopback endpoint of this job's service proxy
    plus a capability minted for this job alone, and this module asks the service once for the
    storage document. Nothing here reads the operator's disk, and no ambient variable can
    redirect an archive: an operation's `environment` is fixed reviewed values in a committed
    manifest, which is neither where a password goes nor where a deployment's locator can go.
  - The password NEVER reaches argv and is never logged. It reaches restic as RESTIC_PASSWORD
    in the CHILD's environment only, so a process listing cannot carry it and nothing this
    process spawns later inherits it.
  - Every child gets a MINIMAL environment: the repository coordinates, the object-store
    credential when the repository has one, and the three variables a subprocess legitimately
    needs (HOME, PATH, TMPDIR) when the parent has them. Behaviour therefore does not drift
    with whatever ambient RESTIC_* variables the machine's shell happens to carry.
  - restic itself is taken from where the owner bound it (`RUNTIME_TOOL_BIN/restic`) first and
    from PATH second — the same rule as git in machine/repository.ts, because inside a job
    sandbox there is no PATH and outside one nothing is bound.
  - THE VERBS ARE A CLOSED SET ({@link RESTIC_VERBS}) and every invocation is built by
    {@link resticArgv}, which admits a verb or throws. `forget`, `prune`, `repair` and
    `unlock` are absent from that set and from this file: never-delete is policy, and an
    allowlist is what makes adding one a deliberate act rather than an accident. `unlock`
    belongs to the same rule for a subtler reason — it removes another process's claim on the
    repository, and a stale lock is the operator's to clear with restic in his own hand.
  - The repository is READ as well as written: `check` proves it, `ls` says what a snapshot
    holds, `dump` streams one archived file without touching a disk, and `restore` writes the
    files back. An archive whose restore path has never been run is an archive nobody has
    tested.

  Snapshots are crash-consistent per file, not transactional across files: a backup taken
  while a session log is being appended to may capture a torn final line. Readers tolerate
  that; the next snapshot supersedes it.
*/

import { z } from "zod";
import { RESTIC_SERVICE, RUNTIME_TOOL_BIN } from "../contract.ts";

/** The one binding this module reads from the environment, because it is the one that is
 *  neither secret nor deployment-specific: where restic keeps its index cache. */
export const RESTIC_ENV = {
  /** restic's cache directory. Optional; absent lets restic use its own default under HOME. */
  cacheDir: "BABEL_RESTIC_CACHE_DIR",
} as const;

/**
 * The endpoint the engine materialized for the operation's service binding. Both values belong
 * to one job: the URL is a loopback listener the owner opened for it and the bearer a
 * capability it minted for it, so neither is a credential of the operator's and neither
 * outlives the run. A document that names anything but loopback is refused rather than
 * followed — the only writer of this file is the engine.
 */
const ServiceEndpointSchema = z.strictObject({
  url: z.string().regex(/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/),
  bearer: z.string().regex(/^[A-Za-z0-9_-]{32,128}$/),
});
type ServiceEndpoint = z.infer<typeof ServiceEndpointSchema>;

/**
 * WHAT THE SERVICE ANSWERS WITH: the repository this deployment archives into and the secrets
 * that open it, in ONE document. An object-store credential is refused in halves and required
 * for an `s3:` locator: the storage document carries the object-store credential inline beside
 * the repository's, and a half-installed policy that failed at the first
 * backup would be found at the worst possible moment, and a locator reviewed apart from its
 * credential is two facts that can disagree.
 */
export const ResticStorageSchema = z
  .strictObject({
    repository: z.string().trim().min(1).max(4096),
    password: z.string().min(1).max(16384),
    accessKeyId: z.string().trim().max(4096).default(""),
    secretAccessKey: z.string().max(4096).default(""),
  })
  .refine((storage) => (storage.accessKeyId === "") === (storage.secretAccessKey === ""), {
    message: "an object-store credential is two values or none",
  })
  .refine((storage) => !storage.repository.startsWith("s3:") || storage.accessKeyId !== "", {
    message: "an s3: repository needs an object-store credential",
  });
export type ResticStorage = z.infer<typeof ResticStorageSchema>;

/** How long the storage document may take to arrive. The service is a loopback listener the
 *  owner opened for this job; a request still unanswered after this is a policy that is not
 *  installed, not a slow one. */
const STORAGE_TIMEOUT_MS = 15_000;

/** A storage document is four short strings. Past this it is not one, whatever it is. */
const MAX_STORAGE_BYTES = 64 << 10;

/** The tag every snapshot Babel writes carries, which is what tells them from anything else
 *  sharing the repository. */
export const BABEL_TAG = "babel";

/** restic exit statuses this module interprets; anything else is surfaced verbatim. */
const EXIT_INCOMPLETE = 3; // the snapshot was created, some source files were unreadable
const EXIT_NO_SUCH_REPO = 10;

/** `check` exits 1 when it FOUND something. The repository answered and its errors ARE the
 *  answer, so that status is an outcome rather than a failed invocation; every other nonzero
 *  status (10 no repository, 12 wrong password) is the invocation failing. */
const EXIT_ERRORS_FOUND = 1;

/** One line of restic's --json stream, bounded. A status line names the files being read, so
 *  it can be long; past this a line is not a message worth parsing. */
const MAX_JSON_LINE = 1 << 20;

/** How much of the child's stderr an error carries. */
const STDERR_TAIL = 4 << 10;

/** How many of `check`'s own error messages an outcome carries. A repository reporting more
 *  than this is broken in a way the first few messages already name. */
const MAX_CHECK_ERRORS = 64;

/** How many entries one listing holds. A snapshot of a session root is thousands of files, and
 *  a caller that needs more than this wants `restore`, not a listing in memory. */
const MAX_LISTED_ENTRIES = 20_000;

/** What `dump` returns without being told otherwise. The caller that knows the file's
 *  catalogued size raises it; the default keeps one unexpected file from becoming the job's
 *  whole memory. */
const MAX_DUMP_BYTES = 64 << 20;

/**
 * THE VERBS THIS MODULE MAY RUN, as a closed set rather than an open passthrough.
 *
 * `forget`, `prune`, `repair`, `unlock` and `rewrite` are not here. Never-delete is policy, so
 * the boundary is drawn where a verb is ADMITTED rather than where one is called: there is no
 * `run(verb, args)` on {@link Repo} for a caller to reach past this list with, and a verb added
 * to it is a reviewable line in a diff.
 *
 * `cat` is here because `exists()` asks the repository for its config, which is the cheapest
 * question that distinguishes "no repository" from "wrong password".
 */
export const RESTIC_VERBS = [
  "cat",
  "init",
  "backup",
  "snapshots",
  "check",
  "ls",
  "dump",
  "restore",
] as const;

/** A snapshot as restic names one: a full or short id, or `latest`. It is checked because it
 *  travels into argv as a POSITIONAL, where a value beginning with `-` would be read as a flag
 *  — and the values reaching this module come from a door's caller. */
const SNAPSHOT_ID = /^(latest|[0-9a-f]{8,64})$/;

/**
 * One invocation's argv, and the ONE place a verb is admitted.
 *
 * Every child of this module is spawned from this, so a verb outside {@link RESTIC_VERBS}
 * cannot be reached by any path — including a future one whose author never read the policy.
 * The refusal happens before restic exists: nothing is contacted and nothing is written.
 */
export function resticArgv(verb: string, flags: readonly string[] = []): readonly string[] {
  if (!(RESTIC_VERBS as readonly string[]).includes(verb)) {
    throw new ResticError("refused", `restic ${verb} is not a verb Babel runs`);
  }
  return [verb, ...flags];
}

/** A snapshot id fit to pass as a positional, or the refusal naming what was asked. */
function snapshotArgument(snapshotId: string): string {
  if (!SNAPSHOT_ID.test(snapshotId)) {
    throw new ResticError("refused", `${snapshotId} is not a snapshot id`);
  }
  return snapshotId;
}

/** A path inside a snapshot, which restic records absolute. A relative one would match nothing
 *  and a NUL cannot cross execve, so both are refused with the value named rather than being
 *  passed on to fail as something else. */
function pathArgument(path: string): string {
  if (!path.startsWith("/") || path.includes("\0")) {
    throw new ResticError("refused", `${path} is not an absolute path inside a snapshot`);
  }
  return path;
}

export interface ObjectStoreCredential {
  readonly accessKeyId: string;
  readonly secretAccessKey: string;
}

/** One repository and how to talk to it, as the storage document and the machine state it. */
export interface ResticConfig {
  readonly repository: string;
  readonly password: string;
  /** The executable, already resolved: a bound tool or a PATH entry, never a bare name. */
  readonly binary: string;
  /** RESTIC_CACHE_DIR, or null to leave restic its own default. */
  readonly cacheDir: string | null;
  readonly objectStore: ObjectStoreCredential | null;
}

/** Which half of the delivery failed; {@link ResticError} carries one. */
export type ResticFailure = "binding" | "service" | "binary" | "refused" | "exit";

/**
 * What went wrong, in the one shape callers match on.
 *
 * `kind` separates the five problems that need different remedies: a service binding the job
 * did not carry, a bound service that would not answer with a storage document (the operator's
 * policy), an executable that cannot be run, an invocation this module refuses to make, and a
 * restic invocation that failed. The first four all mean the repository was never contacted and
 * nothing was written — `refused` most emphatically, since it is this file declining to build
 * the argv at all (a verb outside {@link RESTIC_VERBS}, an argument that is not a snapshot or a
 * path). `stderr` is a bounded tail of restic's own diagnostics, rendered as one line — restic
 * never prints the password, and its messages carry the remedy, so they are surfaced rather
 * than summarized.
 */
export class ResticError extends Error {
  readonly kind: ResticFailure;
  readonly code: number;
  readonly stderr: string;

  constructor(kind: ResticFailure, message: string, code = -1, stderr = "") {
    super(message);
    this.name = "ResticError";
    this.kind = kind;
    this.code = code;
    this.stderr = stderr;
  }

  /** Whether the failure says the repository does not exist. Creating one is an explicit
   *  operator act, never a side effect of backing up, so this is a distinct condition. */
  get missingRepository(): boolean {
    return (
      this.code === EXIT_NO_SUCH_REPO ||
      /repository .*does not exist|unable to open config file/i.test(this.stderr)
    );
  }
}

/** What restic did to one path, in restic's own spelling: its stream says `modified` where its
 *  summary counts `files_changed`, and the stream's word is the one kept here. */
export type BackupAction = "new" | "modified" | "unchanged";

export interface BackupItem {
  readonly path: string;
  readonly action: BackupAction;
}

/** The outcome of one backup, from restic's own summary. */
export interface BackupOutcome {
  readonly snapshotId: string;
  /** restic's `backup_start`, which is also the snapshot's own recorded time: kept verbatim so
   *  a catalog row and `restic snapshots` agree to the nanosecond. */
  readonly startedAt: string;
  readonly finishedAt: string;
  readonly filesNew: number;
  readonly filesChanged: number;
  readonly filesUnmodified: number;
  readonly filesProcessed: number;
  readonly bytesProcessed: number;
  /** Repository growth in bytes before compression: far below `bytesProcessed` whenever
   *  deduplication found existing chunks, which is the normal case for append-only logs. */
  readonly dataAdded: number;
  /** Every file the snapshot holds, directories excluded. */
  readonly items: readonly BackupItem[];
  /** The snapshot was created but some sources could not be read (restic exits 3). */
  readonly incomplete: boolean;
  /** The paths restic reported an error for, one per unreadable item. */
  readonly unreadable: readonly string[];
}

export interface Snapshot {
  readonly id: string;
  readonly shortId: string;
  readonly time: string;
  /** The snapshot this one was taken against, or null when restic found none and rescanned
   *  every byte. */
  readonly parentId: string | null;
  readonly host: string;
  readonly paths: readonly string[];
  readonly tags: readonly string[];
}

/** How deep a verification reads. Structure is always checked; this says what else. */
export interface CheckOptions {
  /**
   * `false` checks the index, the trees and the pack headers — everything but the stored bytes.
   * `true` reads every data blob and verifies its hash (restic's `--read-data`). A string is
   * restic's own subset spelling, `n/t`, `x%` or a size with a `k`/`m`/`g`/`t` suffix, for a
   * deployment whose repository is too large to read whole on a cadence.
   */
  readonly readData?: boolean | string;
}

/**
 * What a verification found. A structural pass that reports nothing and a `--read-data` pass
 * that reports nothing are different assurances, so `dataRead` says which one was bought.
 *
 * `ok === false` is a REPOSITORY with errors, not a failed call: restic answered, and what it
 * found is the answer. A repository that could not be opened at all throws instead.
 */
export interface CheckOutcome {
  readonly ok: boolean;
  /** "" for a structural check, "all" for every data blob, or the subset restic was given. */
  readonly dataRead: string;
  /** How many errors restic counted, which can exceed the messages kept beside it. */
  readonly errorCount: number;
  /** restic's own error messages, bounded; the remedy is written in them. */
  readonly errors: readonly string[];
  /** The packs restic named as damaged, from its summary. */
  readonly brokenPacks: readonly string[];
}

/** One node of a snapshot, as `ls` reports it. */
export interface ArchivedEntry {
  /** The absolute path the backup recorded, which is the path `dump` and `restore` name. */
  readonly path: string;
  readonly type: "file" | "dir" | "symlink" | "other";
  /** Bytes, for a file; 0 for everything else, which restic reports no size for. */
  readonly size: number;
  readonly modifiedAt: string;
}

export interface Listing {
  readonly entries: readonly ArchivedEntry[];
  /** The snapshot holds more entries than one listing carries, and these are the first of
   *  them. A caller that needs the rest wants `restore`, not a bigger listing. */
  readonly truncated: boolean;
}

export interface DumpOptions {
  /** The most bytes retained or forwarded. A file past it is refused, but its rejected
   *  remainder is drained: this bound does not limit remote transport cost. */
  readonly maxBytes?: number;
}

export interface RestoreOptions {
  /** Where the files are written. restic recreates each one's absolute path UNDER this
   *  directory, so a snapshot of `/home/x/log` restores to `<target>/home/x/log`. */
  readonly target: string;
  /** The paths of the snapshot to write; empty restores all of it. */
  readonly include?: readonly string[];
}

/** What a restore wrote, from restic's own summary. Both counts are null when this restic
 *  printed no summary: the files on disk are the outcome either way, and an unknown number is
 *  not the number zero. */
export interface RestoreOutcome {
  readonly target: string;
  readonly filesRestored: number | null;
  readonly bytesRestored: number | null;
}

/** An opened handle on one repository. It holds no connection: each operation is one child
 *  process, and the handle is safe to use concurrently. */
export interface Repo {
  readonly repository: string;
  /** Whether the repository exists and the password opens it. */
  exists(): Promise<boolean>;
  /** Creates the repository, reporting whether this call created it. NOT safe to run
   *  concurrently against the same absent repository: two inits racing leave two valid keys
   *  and one config, which restic then has to be repaired out of by hand. */
  init(): Promise<boolean>;
  /** Snapshots `paths` under `host` with `tags`. Paths are recorded as given, so they should
   *  be absolute. */
  backup(
    paths: readonly string[],
    attribution: { host: string; tags: readonly string[] },
  ): Promise<BackupOutcome>;
  /** Every snapshot the repository holds, restic's own order (newest last). */
  snapshots(): Promise<readonly Snapshot[]>;
  /**
   * Verifies the repository and reports what it found. Structure always; the stored bytes when
   * {@link CheckOptions.readData} asks for them.
   *
   * restic takes an EXCLUSIVE lock for this, so a check and a backup of the same repository do
   * not overlap — one of them waits and then fails. A lock left behind by a killed process is
   * cleared with restic's `unlock`, which Babel does not run: removing another process's claim
   * is the operator's act.
   */
  check(options?: CheckOptions): Promise<CheckOutcome>;
  /** What one snapshot holds, entirely or under the given absolute paths. */
  ls(snapshotId: string, paths?: readonly string[]): Promise<Listing>;
  /** One archived file's bytes, straight out of the snapshot: nothing is written to a disk, so
   *  a session can be proved recoverable without a target directory or a cleanup. */
  dump(snapshotId: string, path: string, options?: DumpOptions): Promise<Uint8Array>;
  /** Streams one archived file without retaining it. Each sink call settles before the next;
   *  a failed sink or exceeded bound stops forwarding, then drains and settles the child.
   *  Forwarded bytes are provisional until this operation succeeds. */
  dumpTo(
    snapshotId: string,
    path: string,
    sink: (chunk: Uint8Array) => void | Promise<void>,
    options?: DumpOptions,
  ): Promise<{ bytes: number }>;
  /** Writes a snapshot's files back, under `target`. It writes to the machine and never to the
   *  repository: a restore adds and removes nothing there. */
  restore(snapshotId: string, options: RestoreOptions): Promise<RestoreOutcome>;
}

/**
 * The config this job's service binding states, or the one error that says which half of the
 * delivery is missing: the binding itself, the operator's policy behind it, or restic.
 *
 * The cache directory is the one value that comes from the environment and the one with a
 * fallback, because restic needs somewhere to put an index cache and a confined job may have
 * no HOME: an explicit binding wins, a HOME leaves restic its own default, and neither leaves
 * a directory under TMPDIR.
 */
export async function resticConfig(options: {
  readonly credentialFile: string;
  readonly env: Readonly<Record<string, string | undefined>>;
}): Promise<ResticConfig> {
  const storage = await readStorage(await readEndpoint(options.credentialFile));
  const bound = options.env[RESTIC_ENV.cacheDir]?.trim() ?? "";
  const tmpdir = options.env["TMPDIR"]?.trim() ?? "/tmp";
  return {
    repository: storage.repository,
    password: storage.password,
    binary: await resticBinary(),
    cacheDir: bound !== "" ? bound : options.env["HOME"] ? null : `${tmpdir}/babel-restic`,
    objectStore:
      storage.accessKeyId === ""
        ? null
        : { accessKeyId: storage.accessKeyId, secretAccessKey: storage.secretAccessKey },
  };
}

/** The service binding as the engine wrote it, or a `binding` error: a job that reached this
 *  operation without one was launched against a manifest that does not declare it. */
async function readEndpoint(path: string): Promise<ServiceEndpoint> {
  let document: unknown;
  try {
    document = await Bun.file(path).json();
  } catch {
    throw new ResticError(
      "binding",
      `the job bound no ${RESTIC_SERVICE.serviceId} service at ${path}`,
    );
  }
  const parsed = ServiceEndpointSchema.safeParse(document);
  if (!parsed.success) {
    throw new ResticError("binding", `${path} is not a ${RESTIC_SERVICE.serviceId} binding`);
  }
  return parsed.data;
}

/**
 * One request to the bound service for the storage document.
 *
 * Every failure is the operator's policy rather than the archive's: a service that does not
 * answer, one that refuses the route, or one whose answer is not a storage document. The
 * reason names the field that was wrong and never a value, because a receipt is durable and a
 * value here is a secret.
 */
async function readStorage(endpoint: ServiceEndpoint): Promise<ResticStorage> {
  const service = RESTIC_SERVICE.serviceId;
  let response: Response;
  try {
    response = await fetch(`${endpoint.url}${RESTIC_SERVICE.path}`, {
      headers: { authorization: `Bearer ${endpoint.bearer}` },
      signal: AbortSignal.timeout(STORAGE_TIMEOUT_MS),
      redirect: "error",
    });
  } catch (cause) {
    const reason = cause instanceof Error ? cause.message : String(cause);
    throw new ResticError("service", `${service} did not answer ${RESTIC_SERVICE.path}: ${reason}`);
  }
  if (!response.ok) {
    throw new ResticError(
      "service",
      `${service} refused ${RESTIC_SERVICE.path} (HTTP ${response.status})`,
      response.status,
    );
  }
  const body = await response.text();
  if (body.length > MAX_STORAGE_BYTES) {
    throw new ResticError("service", `${service} answered ${body.length} bytes, not a document`);
  }
  let document: unknown;
  try {
    document = JSON.parse(body);
  } catch {
    throw new ResticError("service", `${service} answered no JSON document`);
  }
  const parsed = ResticStorageSchema.safeParse(document);
  if (!parsed.success) {
    const issues = parsed.error.issues
      .map((issue) => `${issue.path.join(".") || "document"}: ${issue.message}`)
      .join("; ");
    throw new ResticError("service", `${service} answered no storage document (${issues})`);
  }
  return parsed.data;
}

/** restic where the owner bound it, and on PATH otherwise: inside a job the sandbox has no
 *  PATH and the tool is at its bound path; outside one — a hand-run, the tests — nothing is
 *  bound and PATH is the answer. The same rule as git in machine/repository.ts. */
async function resticBinary(): Promise<string> {
  const bound = `${RUNTIME_TOOL_BIN}/restic`;
  if (await Bun.file(bound).exists()) return bound;
  const found = Bun.which("restic");
  if (found === null) {
    throw new ResticError("binary", `restic is neither bound at ${bound} nor on PATH`);
  }
  return found;
}

export function openRepo(config: ResticConfig): Repo {
  return new ResticRepo(config);
}

class ResticRepo implements Repo {
  readonly #config: ResticConfig;

  constructor(config: ResticConfig) {
    this.#config = config;
  }

  get repository(): string {
    return this.#config.repository;
  }

  async exists(): Promise<boolean> {
    try {
      await this.#run("open repository", resticArgv("cat", ["config"]));
      return true;
    } catch (err) {
      if (err instanceof ResticError && err.kind === "exit" && err.missingRepository) return false;
      throw err;
    }
  }

  async init(): Promise<boolean> {
    if (await this.exists()) return false;
    await this.#run("init repository", resticArgv("init", ["--json"]));
    return true;
  }

  async backup(
    paths: readonly string[],
    attribution: { host: string; tags: readonly string[] },
  ): Promise<BackupOutcome> {
    if (paths.length === 0) throw new ResticError("exit", "restic backup: no paths");
    const flags = ["--json", "--verbose", "--host", attribution.host];
    for (const tag of attribution.tags) flags.push("--tag", tag);
    // `--` keeps a path that starts with "-" from being read as a flag.
    flags.push("--", ...paths);
    const args = resticArgv("backup", flags);

    const child = this.#spawn(args);
    const tail = new Tail();
    // restic splits the protocol across both streams: the summary and the per-item verbose
    // lines arrive on stdout while per-item errors and the final exit_error land on stderr,
    // so both are parsed, concurrently — a child blocked on a full pipe never finishes.
    const [fromStdout, fromStderr] = await Promise.all([
      consume(child.stdout, null),
      consume(child.stderr, tail),
    ]);
    const code = await child.exited;

    const summary = fromStdout.summary ?? fromStderr.summary;
    const unreadable = [...fromStdout.unreadable, ...fromStderr.unreadable];
    if (code !== 0 && code !== EXIT_INCOMPLETE) {
      throw new ResticError("exit", `restic backup failed (exit ${code})`, code, tail.toString());
    }
    if (!summary) {
      // Per-file read failures are non-fatal in restic, which finishes the snapshot and exits
      // 3; no summary at all means no snapshot was created, which is a failure whatever the
      // exit code said.
      throw new ResticError("exit", "restic backup produced no summary", code, tail.toString());
    }
    return {
      ...summary,
      items: fromStdout.items,
      incomplete: code === EXIT_INCOMPLETE,
      unreadable,
    };
  }

  async snapshots(): Promise<readonly Snapshot[]> {
    const stdout = await this.#run("list snapshots", resticArgv("snapshots", ["--json"]));
    const parsed: unknown = JSON.parse(stdout.trim() === "" ? "[]" : stdout);
    if (!Array.isArray(parsed)) return [];
    const snapshots: Snapshot[] = [];
    for (const row of parsed) {
      if (!isRow(row)) continue;
      const id = text(row, "id");
      if (id === "") continue;
      const parentId = text(row, "parent");
      snapshots.push({
        id,
        shortId: text(row, "short_id") || id.slice(0, 8),
        time: text(row, "time"),
        parentId: parentId === "" ? null : parentId,
        host: text(row, "hostname"),
        paths: strings(row, "paths"),
        tags: strings(row, "tags"),
      });
    }
    return snapshots;
  }

  async check(options: CheckOptions = {}): Promise<CheckOutcome> {
    const readData = options.readData ?? false;
    // A fresh cache is the stronger question: `--with-cache` would let a corrupted pack that
    // is already cached answer for itself, which is the one thing a verification must not
    // allow. restic makes its own temporary cache under RESTIC_CACHE_DIR and removes it.
    const depth =
      readData === false
        ? []
        : readData === true
          ? ["--read-data"]
          : ["--read-data-subset", readData];
    const child = this.#spawn(resticArgv("check", ["--json", ...depth]));
    const tail = new Tail();
    const errors: string[] = [];
    let reported = 0;
    let brokenPacks: readonly string[] = [];
    const keep = (line: string): void => {
      if (line.length === 0 || line.charCodeAt(0) !== 0x7b /* { */) return;
      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch {
        return;
      }
      if (!isRow(parsed)) return;
      switch (text(parsed, "message_type")) {
        case "error": {
          reported += 1;
          if (errors.length < MAX_CHECK_ERRORS) errors.push(text(parsed, "message").trim());
          break;
        }
        case "summary":
          // `num_errors` is restic's own count, which survives the bound on the messages above.
          reported = Math.max(reported, count(parsed, "num_errors"));
          brokenPacks = strings(parsed, "broken_packs");
          break;
        default:
          break;
      }
    };
    await Promise.all([
      (async () => {
        for await (const line of readLines(child.stdout)) keep(line);
      })(),
      (async () => {
        for await (const line of readLines(child.stderr)) {
          tail.push(line);
          keep(line);
        }
      })(),
    ]);
    const code = await child.exited;
    if (code !== 0 && code !== EXIT_ERRORS_FOUND) {
      // 10 and 12 are a repository that was never read: no config, or a password that does not
      // open it. Those are not a verdict on the archive's integrity and must not read as one.
      throw new ResticError("exit", `restic check failed (exit ${code})`, code, tail.toString());
    }
    const account = errors.length > 0 || code === 0 ? errors : [tail.toString()];
    return {
      ok: code === 0 && reported === 0,
      dataRead: readData === false ? "" : readData === true ? "all" : readData,
      // A nonzero exit with nothing parsed still has to say something: restic's own tail is
      // the only account of it there is, and it counts as one error.
      errorCount: reported > 0 ? reported : account.length,
      errors: account,
      brokenPacks,
    };
  }

  async ls(snapshotId: string, paths: readonly string[] = []): Promise<Listing> {
    const within = paths.map(pathArgument);
    const args = resticArgv("ls", ["--json", "--", snapshotArgument(snapshotId), ...within]);
    const child = this.#spawn(args);
    const tail = new Tail();
    const entries: ArchivedEntry[] = [];
    let truncated = false;
    await Promise.all([
      (async () => {
        // The stream is read to its end even once the bound is reached: a child left with a
        // full pipe never exits, and the alternative — killing it — turns a complete listing
        // into an exit status nobody can tell from a failure.
        for await (const line of readLines(child.stdout)) {
          const node = parseNode(line);
          if (node === null) continue;
          if (entries.length >= MAX_LISTED_ENTRIES) {
            truncated = true;
            continue;
          }
          entries.push(node);
        }
      })(),
      (async () => {
        for await (const line of readLines(child.stderr)) tail.push(line);
      })(),
    ]);
    const code = await child.exited;
    if (code !== 0) {
      throw new ResticError("exit", `restic ls failed (exit ${code})`, code, tail.toString());
    }
    return { entries, truncated };
  }

  async dump(snapshotId: string, path: string, options: DumpOptions = {}): Promise<Uint8Array> {
    const chunks: Uint8Array[] = [];
    const { bytes } = await this.dumpTo(
      snapshotId,
      path,
      (chunk) => {
        chunks.push(chunk);
      },
      options,
    );
    const held = new Uint8Array(bytes);
    let at = 0;
    for (const chunk of chunks) {
      held.set(chunk, at);
      at += chunk.byteLength;
    }
    return held;
  }

  async dumpTo(
    snapshotId: string,
    path: string,
    sink: (chunk: Uint8Array) => void | Promise<void>,
    options: DumpOptions = {},
  ): Promise<{ bytes: number }> {
    const bound = options.maxBytes ?? MAX_DUMP_BYTES;
    const args = resticArgv("dump", ["--", snapshotArgument(snapshotId), pathArgument(path)]);
    const child = this.#spawn(args);
    const tail = new Tail();
    let bytes = 0;
    let over = false;
    let sinkFailed = false;
    let sinkError: unknown;
    // Only pipe failures require termination. A bound or sink refusal still drains both
    // streams, preserving restic's own outcome and leaving no child blocked on its output.
    const settleStream = async (consume: () => Promise<void>): Promise<void> => {
      try {
        await consume();
      } catch (error) {
        child.kill();
        throw error;
      }
    };
    const settled = await Promise.allSettled([
      settleStream(async () => {
        for await (const chunk of child.stdout as unknown as AsyncIterable<Uint8Array>) {
          bytes += chunk.byteLength;
          if (bytes > bound) {
            over = true;
            continue;
          }
          if (!over && !sinkFailed) {
            try {
              await sink(chunk);
            } catch (error) {
              sinkFailed = true;
              sinkError = error;
            }
          }
        }
      }),
      settleStream(async () => {
        for await (const line of readLines(child.stderr)) tail.push(line);
      }),
      child.exited,
    ]);
    for (const result of settled) {
      if (result.status === "rejected") throw result.reason;
    }
    if (sinkFailed) throw sinkError;
    const code = await child.exited;
    if (code !== 0) {
      throw new ResticError("exit", `restic dump failed (exit ${code})`, code, tail.toString());
    }
    if (over) {
      throw new ResticError(
        "refused",
        `${path} is ${bytes} bytes in ${snapshotId}, past the ${bound} this dump holds`,
      );
    }
    return { bytes };
  }

  async restore(snapshotId: string, options: RestoreOptions): Promise<RestoreOutcome> {
    const target = pathArgument(options.target);
    const flags = ["--json", "--target", target];
    for (const path of options.include ?? []) flags.push("--include", pathArgument(path));
    flags.push("--", snapshotArgument(snapshotId));
    const stdout = await this.#run("restore", resticArgv("restore", flags));
    let filesRestored: number | null = null;
    let bytesRestored: number | null = null;
    for (const line of stdout.split("\n")) {
      if (line.length === 0 || line.charCodeAt(0) !== 0x7b /* { */) continue;
      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch {
        continue;
      }
      if (!isRow(parsed) || text(parsed, "message_type") !== "summary") continue;
      filesRestored = count(parsed, "files_restored");
      bytesRestored = count(parsed, "bytes_restored");
    }
    return { target, filesRestored, bytesRestored };
  }

  /** One invocation whose whole output is small enough to buffer. */
  async #run(operation: string, args: readonly string[]): Promise<string> {
    const child = this.#spawn(args);
    const tail = new Tail();
    const [stdout] = await Promise.all([
      new Response(child.stdout).text(),
      (async () => {
        for await (const line of readLines(child.stderr)) tail.push(line);
      })(),
    ]);
    const code = await child.exited;
    if (code !== 0) {
      throw new ResticError(
        "exit",
        `restic ${operation} failed (exit ${code})`,
        code,
        tail.toString(),
      );
    }
    return stdout;
  }

  #spawn(args: readonly string[]): Bun.Subprocess<"ignore", "pipe", "pipe"> {
    const binary = this.#config.binary;
    try {
      return Bun.spawn([binary, ...args], {
        env: this.#env(),
        stdin: "ignore",
        stdout: "pipe",
        stderr: "pipe",
      });
    } catch (cause) {
      // The repository was never contacted, so this is its own kind: a tool the owner bound
      // to something that cannot be executed here is not a failed backup.
      const reason = cause instanceof Error ? cause.message : String(cause);
      throw new ResticError("binary", `restic: ${binary} cannot be run: ${reason}`);
    }
  }

  /** The child's whole environment: the repository's coordinates and nothing inherited. */
  #env(): Record<string, string> {
    const config = this.#config;
    const env: Record<string, string> = {
      RESTIC_REPOSITORY: config.repository,
      RESTIC_PASSWORD: config.password,
    };
    if (config.cacheDir !== null) env["RESTIC_CACHE_DIR"] = config.cacheDir;
    if (config.objectStore !== null) {
      env["AWS_ACCESS_KEY_ID"] = config.objectStore.accessKeyId;
      env["AWS_SECRET_ACCESS_KEY"] = config.objectStore.secretAccessKey;
    }
    for (const name of ["HOME", "PATH", "TMPDIR"]) {
      const value = process.env[name];
      if (value !== undefined) env[name] = value;
    }
    return env;
  }
}

/** What one of restic's two backup streams carried. */
interface StreamReport {
  summary: Omit<BackupOutcome, "items" | "incomplete" | "unreadable"> | null;
  items: BackupItem[];
  unreadable: string[];
}

/**
 * Parses one ndjson stream of restic's backup protocol, mirroring every line into `tail` when
 * one is given so a fatal non-JSON message still reaches the caller's error.
 *
 * Unparseable lines are skipped rather than fatal: a torn line, a message type this reader
 * does not know, or a wrapper's own noise must not fail a backup restic considers successful.
 */
async function consume(
  stream: ReadableStream<Uint8Array>,
  tail: Tail | null,
): Promise<StreamReport> {
  const report: StreamReport = { summary: null, items: [], unreadable: [] };
  for await (const line of readLines(stream)) {
    if (tail !== null) tail.push(line);
    if (line.length === 0 || line.charCodeAt(0) !== 0x7b /* { */) continue;
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      continue;
    }
    if (!isRow(parsed)) continue;
    switch (text(parsed, "message_type")) {
      case "summary":
        report.summary = {
          snapshotId: text(parsed, "snapshot_id"),
          startedAt: text(parsed, "backup_start"),
          finishedAt: text(parsed, "backup_end"),
          filesNew: count(parsed, "files_new"),
          filesChanged: count(parsed, "files_changed"),
          filesUnmodified: count(parsed, "files_unmodified"),
          filesProcessed: count(parsed, "total_files_processed"),
          bytesProcessed: count(parsed, "total_bytes_processed"),
          dataAdded: count(parsed, "data_added"),
        };
        break;
      case "verbose_status": {
        const action = text(parsed, "action");
        const path = text(parsed, "item");
        // A directory's item ends in a separator and the scan messages name nothing; only
        // files are evidence a session was archived.
        if (path === "" || path.endsWith("/")) break;
        if (action === "new" || action === "modified" || action === "unchanged") {
          report.items.push({ path, action });
        }
        break;
      }
      case "error": {
        const item = text(parsed, "item");
        if (item !== "") report.unreadable.push(item);
        break;
      }
      default:
        break;
    }
  }
  return report;
}

/**
 * The lines of a byte stream, decoded as UTF-8.
 *
 * A line longer than MAX_JSON_LINE is cut there and its remainder is read as further lines:
 * both fail to parse and are skipped, which bounds the memory one runaway message can cost
 * without letting it abort the scan.
 */
async function* readLines(stream: ReadableStream<Uint8Array>): AsyncGenerator<string> {
  const decoder = new TextDecoder();
  let buffer = "";
  for await (const chunk of stream as unknown as AsyncIterable<Uint8Array>) {
    buffer += decoder.decode(chunk, { stream: true });
    let start = 0;
    for (let nl = buffer.indexOf("\n"); nl >= 0; nl = buffer.indexOf("\n", start)) {
      yield buffer.slice(start, nl);
      start = nl + 1;
    }
    buffer = start === 0 ? buffer : buffer.slice(start);
    while (buffer.length > MAX_JSON_LINE) {
      yield buffer.slice(0, MAX_JSON_LINE);
      buffer = buffer.slice(MAX_JSON_LINE);
    }
  }
  buffer += decoder.decode();
  if (buffer.length > 0) yield buffer;
}

/** The last STDERR_TAIL characters written to it, rendered as one line so it composes with a
 *  wrapped error. Truncation is marked with a leading ellipsis. */
class Tail {
  #text = "";
  #truncated = false;

  push(line: string): void {
    const next = this.#text === "" ? line : `${this.#text}; ${line}`;
    if (next.length > STDERR_TAIL) {
      this.#text = next.slice(next.length - STDERR_TAIL);
      this.#truncated = true;
      return;
    }
    this.#text = next;
  }

  toString(): string {
    // restic reports a fatal error as prose normally, but as one JSON object when --json is in
    // effect. Only the framing is removed: the message carries the remedy and is never trimmed.
    const text = this.#text.replace(
      /\{"message_type":"exit_error".*?"message":"((?:[^"\\]|\\.)*)"\}/g,
      (_match, message: string) => {
        try {
          return JSON.parse(`"${message}"`) as string;
        } catch {
          return message;
        }
      },
    );
    return this.#truncated ? `...${text}` : text;
  }
}

function isRow(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * One node of `restic ls --json`, or null for anything else on that stream.
 *
 * The stream opens with the SNAPSHOT itself (`struct_type: "snapshot"`) and then carries one
 * object per entry, so the type is what separates the listing from its header. A kind restic
 * reports that is neither a file nor a directory nor a link is kept as `other` rather than
 * dropped: a device node or a socket inside a session root is still evidence of what the
 * snapshot holds.
 */
function parseNode(line: string): ArchivedEntry | null {
  if (line.length === 0 || line.charCodeAt(0) !== 0x7b /* { */) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch {
    return null;
  }
  if (!isRow(parsed) || text(parsed, "struct_type") !== "node") return null;
  const path = text(parsed, "path");
  if (path === "") return null;
  const kind = text(parsed, "type");
  return {
    path,
    type: kind === "file" || kind === "dir" || kind === "symlink" ? kind : "other",
    size: count(parsed, "size"),
    modifiedAt: text(parsed, "mtime"),
  };
}

function text(row: Record<string, unknown>, key: string): string {
  const value = row[key];
  return typeof value === "string" ? value : "";
}

function count(row: Record<string, unknown>, key: string): number {
  const value = row[key];
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function strings(row: Record<string, unknown>, key: string): readonly string[] {
  const value = row[key];
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}
