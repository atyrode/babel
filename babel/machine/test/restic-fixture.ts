import { chmod, mkdir, mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RESTIC_SERVICE, type Harness } from "../../contract.ts";
import {
  BABEL_TAG,
  RESTIC_ENV,
  openRepo,
  type Repo,
  type ResticConfig,
  type Snapshot,
} from "../restic.ts";

/*
  A SYNTHETIC FLEET ARCHIVE, for every test that reads one (#453).

  A real restic repository in a temporary directory, because restic is the thing being
  integrated with and a fake of its JSON would only prove a test's idea of restic. Nothing here
  is a real transcript or a real repository:

  - The fixture's HOME is its own, mode 0700, and holds each harness's session root laid out as
    the harness lays it out, so the adapters claim what is archived from it. restic runs with
    that HOME, and a job on this synthetic machine sees it in `env`.
  - The repository password is generated per fixture and kept in a mode-0600 file inside that
    home. It reaches restic as RESTIC_PASSWORD in the child's environment and nowhere else.
  - The storage document reaches an operation exactly as it does on a machine: a loopback
    service that demands the job's capability and answers `GET /storage`, bound through the
    file `credentialFile` names (`resticConfig`).
  - Every snapshot is taken as a machine in UTC+05:30 takes it, so restic spells its times with
    an offset, as the fleet's collectors do, and a reader that forgets to normalize is caught.
  - Snapshots are taken through `Repo.backup`, the one write Babel keeps, under the label and
    tags a test names.
*/

/** A zone with a fixed, non-hour offset: a reader that drops or mangles it cannot pass. */
const BACKUP_ZONE = "Asia/Kolkata";

/** Where each harness keeps its sessions under a home, as its adapter's root rule reads it. */
const SESSION_ROOTS: Record<Harness, readonly string[]> = {
  omp: [".omp", "agent", "sessions"],
  codex: [".codex"],
  claude: [".claude"],
};

export interface SyntheticArchive {
  /** The fixture's own home, mode 0700: session roots, password, binding and caches. */
  readonly home: string;
  /** The repository's local path, which is also its locator. */
  readonly repository: string;
  /** The repository, opened directly with {@link SyntheticArchive.config}. */
  readonly repo: Repo;
  /** What `repo` was opened with, for a test that needs a handle of its own on the repository:
   *  a spy, or restic behind a wrapper of the test's. `binary` runs restic in this home and
   *  zone. */
  readonly config: ResticConfig;
  /** The service binding an operation reads with `resticConfig({ credentialFile, env })`. */
  readonly credentialFile: string;
  /** What a job on this synthetic machine sees: HOME, XDG_*, TMPDIR and restic's cache, all
   *  inside {@link SyntheticArchive.home}. */
  readonly env: Readonly<Record<string, string>>;
  /** `<home>/.omp/agent/sessions`, `<home>/.codex` or `<home>/.claude`, created empty. */
  sessionRoot(harness: Harness): string;
  /** Backs `roots` up as one snapshot under the host label `label`, tagged `babel` unless the
   *  test names other tags, and returns the snapshot as `restic snapshots` reports it. */
  snapshot(label: string, roots: readonly string[], tags?: readonly string[]): Promise<Snapshot>;
  /** How many lock files the repository holds. */
  locks(): Promise<number>;
  /** Stops the service and removes the home, repository included. */
  close(): Promise<void>;
}

/**
 * A fresh synthetic archive: a repository initialized under a new temporary home, its storage
 * service listening on loopback, and empty session roots. restic must be on PATH — 0.19.1, which
 * `bun run deps:tests` provisions in CI — and its absence fails the test by name rather than
 * skipping it.
 */
export async function syntheticArchive(): Promise<SyntheticArchive> {
  const restic = Bun.which("restic");
  if (restic === null) {
    throw new Error("archive tests require restic 0.19.1 on PATH (bun run deps:tests)");
  }
  const home = await mkdtemp(join(tmpdir(), "babel-archive-fixture-"));
  let service: Server | null = null;
  try {
    await chmod(home, 0o700);
    const repository = join(home, "repository");
    const scratch = join(home, "tmp");
    const resticCache = join(home, ".cache", "restic");
    const env: Record<string, string> = {
      HOME: home,
      XDG_CONFIG_HOME: join(home, ".config"),
      XDG_CACHE_HOME: join(home, ".cache"),
      XDG_DATA_HOME: join(home, ".local", "share"),
      XDG_STATE_HOME: join(home, ".local", "state"),
      TMPDIR: scratch,
      [RESTIC_ENV.cacheDir]: resticCache,
    };
    await mkdir(scratch);
    for (const segments of Object.values(SESSION_ROOTS)) {
      await mkdir(join(home, ...segments), { recursive: true });
    }

    // restic reads its zone and home from its own environment, which `Repo` keeps minimal and
    // inherited from this process; a wrapper is the one place both can be the machine's.
    const binary = join(home, "bin", "restic");
    await mkdir(join(home, "bin"));
    await writeFile(
      binary,
      `#!/bin/sh\nHOME=${shellQuoted(home)} TZ=${BACKUP_ZONE} exec ${shellQuoted(restic)} "$@"\n`,
      { mode: 0o700 },
    );

    const passwordFile = join(home, "password");
    const password = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");
    await writeFile(passwordFile, password, { mode: 0o600 });

    const config: ResticConfig = {
      repository,
      password,
      binary,
      cacheDir: resticCache,
      objectStore: null,
    };
    const repo = openRepo(config);
    await repo.init();

    // The engine mints the capability as 32 random bytes, base64url. The service is node's own
    // HTTP server rather than `Bun.serve`, because a panel test in the same process registers a
    // DOM whose `Response` replaces the global one, and `Bun.serve` answers nothing else: which
    // test file ran first must not decide whether the archive opens.
    const bearer = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");
    const listening = createServer((request, response) => {
      const answer = (status: number, body: string): void => {
        response.writeHead(status, {
          "content-type": status === 200 ? "application/json" : "text/plain",
        });
        response.end(body);
      };
      if (request.headers.authorization !== `Bearer ${bearer}`) {
        answer(401, "unauthorized");
        return;
      }
      if (new URL(request.url ?? "/", "http://127.0.0.1").pathname !== RESTIC_SERVICE.path) {
        answer(404, "unknown");
        return;
      }
      Bun.file(passwordFile)
        .text()
        .then(
          (password) => answer(200, JSON.stringify({ repository, password })),
          () => answer(500, "unreadable"),
        );
    });
    service = listening;
    const bound = Promise.withResolvers<void>();
    listening.once("error", bound.reject);
    listening.listen(0, "127.0.0.1", bound.resolve);
    await bound.promise;
    const address = listening.address();
    if (address === null || typeof address === "string") {
      throw new Error("the storage service has no port");
    }
    const credentialFile = join(home, "restic-binding.json");
    await writeFile(
      credentialFile,
      JSON.stringify({ url: `http://127.0.0.1:${String(address.port)}`, bearer }),
      { mode: 0o600 },
    );

    return {
      home,
      repository,
      repo,
      config,
      credentialFile,
      env,
      sessionRoot: (harness) => join(home, ...SESSION_ROOTS[harness]),
      async snapshot(label, roots, tags = [BABEL_TAG]) {
        const { snapshotId } = await repo.backup(roots, { host: label, tags });
        const taken = (await repo.snapshots()).find((snapshot) => snapshot.id === snapshotId);
        if (taken === undefined) throw new Error(`restic does not list snapshot ${snapshotId}`);
        return taken;
      },
      async locks() {
        return (await readdir(join(repository, "locks")).catch(() => [])).length;
      },
      async close() {
        await stopped(listening);
        await rm(home, { recursive: true, force: true });
      },
    };
  } catch (err) {
    if (service !== null) await stopped(service);
    await rm(home, { recursive: true, force: true });
    throw err;
  }
}

/** A path as one single-quoted shell word. */
function shellQuoted(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

/** Closes the service and every connection a client left open, and waits for both. */
function stopped(server: Server): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  server.closeAllConnections();
  server.close(() => resolve());
  return promise;
}
