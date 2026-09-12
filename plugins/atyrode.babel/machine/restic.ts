/*
  THE RESTIC INVOCATION of the machine half. restic owns Babel's archival storage — the
  session logs of every enrolled machine, deduplicated into one repository — and Babel never
  reimplements a repository format. This is a thin, contract-bearing shell over the binary:

  - The repository password NEVER reaches argv and is never logged. It arrives in the child's
    environment as RESTIC_PASSWORD, from the job's own binding (see `RESTIC_ENV`), so a
    password cannot leak through a process listing and the plugin reads no credential file of
    its own. The engine hands the job its bindings; nothing here consults the operator's disk.
  - Every child gets a MINIMAL environment: the repository coordinates, the object-store
    credential when the repository has one, and the three variables a subprocess legitimately
    needs (HOME, PATH, TMPDIR) when the parent has them. Behaviour therefore does not drift
    with whatever ambient RESTIC_* variables the machine's shell happens to carry.
  - Nothing here deletes anything: no forget, no prune, no repair. Never-delete is policy.

  Snapshots are crash-consistent per file, not transactional across files: a backup taken
  while a session log is being appended to may capture a torn final line. Readers tolerate
  that; the next snapshot supersedes it.
*/

/** The job bindings this module reads. The engine provides them; the plugin reads no file. */
export const RESTIC_ENV = {
  /** The repository locator, restic's RESTIC_REPOSITORY. Required. */
  repository: "BABEL_RESTIC_REPOSITORY",
  /** The repository password. Required, secret, never argv, never a file the plugin reads. */
  password: "BABEL_RESTIC_PASSWORD",
  /** restic's cache directory. Optional; absent lets restic use its own default under HOME. */
  cacheDir: "BABEL_RESTIC_CACHE_DIR",
  /** The restic executable, when the job pins one. Optional; absent resolves `restic` on PATH. */
  binary: "BABEL_RESTIC_BINARY",
  /** An object-store repository's credential. restic offers no file reference for these. */
  accessKeyId: "BABEL_RESTIC_ACCESS_KEY_ID",
  secretAccessKey: "BABEL_RESTIC_SECRET_ACCESS_KEY",
} as const;

/** The tag every snapshot Babel writes carries, which is what tells them from anything else
 *  sharing the repository. */
export const BABEL_TAG = "babel";

/** restic exit statuses this module interprets; anything else is surfaced verbatim. */
const EXIT_INCOMPLETE = 3; // the snapshot was created, some source files were unreadable
const EXIT_NO_SUCH_REPO = 10;

/** One line of restic's --json stream, bounded. A status line names the files being read, so
 *  it can be long; past this a line is not a message worth parsing. */
const MAX_JSON_LINE = 1 << 20;

/** How much of the child's stderr an error carries. */
const STDERR_TAIL = 4 << 10;

export interface ObjectStoreCredential {
  readonly accessKeyId: string;
  readonly secretAccessKey: string;
}

/** One repository and how to talk to it. */
export interface ResticConfig {
  readonly repository: string;
  readonly password: string;
  /** The executable, resolved through PATH on first use. */
  readonly binary: string;
  /** RESTIC_CACHE_DIR, or null to leave restic its own default. */
  readonly cacheDir: string | null;
  readonly objectStore: ObjectStoreCredential | null;
}

/**
 * What went wrong, in the one shape callers match on.
 *
 * `kind` separates the three problems that need different remedies: a binding the job did not
 * provide, an executable that cannot be run (the repository was never contacted and nothing
 * was written), and a restic invocation that failed. `stderr` is a bounded tail of restic's
 * own diagnostics, rendered as one line — restic never prints the password, and its messages
 * carry the remedy, so they are surfaced rather than summarized.
 */
export class ResticError extends Error {
  readonly kind: "binding" | "binary" | "exit";
  readonly code: number;
  readonly stderr: string;

  constructor(kind: "binding" | "binary" | "exit", message: string, code = -1, stderr = "") {
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
  backup(paths: readonly string[], attribution: { host: string; tags: readonly string[] }): Promise<BackupOutcome>;
  /** Every snapshot the repository holds, restic's own order (newest last). */
  snapshots(): Promise<readonly Snapshot[]>;
}

/**
 * The config the job's bindings state, or a `binding` error naming the one that is missing.
 *
 * The cache directory is the one value with a fallback, because restic needs somewhere to put
 * an index cache and a confined job may have no HOME: an explicit binding wins, a HOME leaves
 * restic its own default, and neither leaves a directory under TMPDIR.
 */
export function resticConfigFromEnv(env: Readonly<Record<string, string | undefined>>): ResticConfig {
  const repository = env[RESTIC_ENV.repository]?.trim() ?? "";
  if (repository === "") {
    throw new ResticError("binding", `the job provided no ${RESTIC_ENV.repository}`);
  }
  const password = env[RESTIC_ENV.password] ?? "";
  if (password === "") {
    throw new ResticError("binding", `the job provided no ${RESTIC_ENV.password}`);
  }
  const accessKeyId = env[RESTIC_ENV.accessKeyId] ?? "";
  const secretAccessKey = env[RESTIC_ENV.secretAccessKey] ?? "";
  const bound = env[RESTIC_ENV.cacheDir]?.trim() ?? "";
  const tmpdir = env["TMPDIR"]?.trim() ?? "/tmp";
  return {
    repository,
    password,
    binary: env[RESTIC_ENV.binary]?.trim() || "restic",
    cacheDir: bound !== "" ? bound : env["HOME"] ? null : `${tmpdir}/babel-restic`,
    objectStore: accessKeyId !== "" ? { accessKeyId, secretAccessKey } : null,
  };
}

export function openRepo(config: ResticConfig): Repo {
  return new ResticRepo(config);
}

class ResticRepo implements Repo {
  readonly #config: ResticConfig;
  #binPath: string | null = null;

  constructor(config: ResticConfig) {
    this.#config = config;
  }

  get repository(): string {
    return this.#config.repository;
  }

  async exists(): Promise<boolean> {
    try {
      await this.#run("open repository", ["cat", "config"]);
      return true;
    } catch (err) {
      if (err instanceof ResticError && err.kind === "exit" && err.missingRepository) return false;
      throw err;
    }
  }

  async init(): Promise<boolean> {
    if (await this.exists()) return false;
    await this.#run("init repository", ["init", "--json"]);
    return true;
  }

  async backup(
    paths: readonly string[],
    attribution: { host: string; tags: readonly string[] },
  ): Promise<BackupOutcome> {
    if (paths.length === 0) throw new ResticError("exit", "restic backup: no paths");
    const args = ["backup", "--json", "--verbose", "--host", attribution.host];
    for (const tag of attribution.tags) args.push("--tag", tag);
    // `--` keeps a path that starts with "-" from being read as a flag.
    args.push("--", ...paths);

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
    return { ...summary, items: fromStdout.items, incomplete: code === EXIT_INCOMPLETE, unreadable };
  }

  async snapshots(): Promise<readonly Snapshot[]> {
    const stdout = await this.#run("list snapshots", ["snapshots", "--json"]);
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
      throw new ResticError("exit", `restic ${operation} failed (exit ${code})`, code, tail.toString());
    }
    return stdout;
  }

  #spawn(args: readonly string[]): Bun.Subprocess<"ignore", "pipe", "pipe"> {
    const env = this.#env();
    if (this.#binPath === null) {
      const resolved = Bun.which(this.#config.binary, { PATH: env["PATH"] ?? "" });
      if (resolved === null) {
        throw new ResticError("binary", `restic: ${this.#config.binary} is not on PATH`);
      }
      this.#binPath = resolved;
    }
    return Bun.spawn([this.#binPath, ...args], { env, stdin: "ignore", stdout: "pipe", stderr: "pipe" });
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
async function consume(stream: ReadableStream<Uint8Array>, tail: Tail | null): Promise<StreamReport> {
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
