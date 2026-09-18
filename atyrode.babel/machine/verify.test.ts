/*
  The verify operation against a REAL restic repository (#338), through the same delivery a job
  gets: a loopback storage service on 127.0.0.1 that demands the capability and answers
  `GET /storage`, and a binding file holding {url, bearer}. `machine/archive.test.ts` fills the
  repository the same way; this file reads it back.

  WHAT IT IS FOR. A restore nobody has run is a claim, and the claim this operation makes is
  byte-exactness — so the rows below restore a catalogued session out of a named snapshot and
  compare the bytes, and one row deliberately lies about the catalogued digest to prove the
  comparison bites instead of being decorative.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RESTIC_SERVICE, type Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputFile, OutputSink } from "./output.ts";
import { BABEL_TAG, openRepo, type ResticConfig } from "./restic.ts";
import { VerifyInputSchema, verify, type VerifyDeps } from "./verify.ts";

/*
  A real restic and a real repository, so the row needs the binary — and a CI runner has none.
  Skipped with the reason on every row, exactly as `machine/archive.test.ts` skips.
*/
const RESTIC_ON_PATH = Bun.which("restic") !== null;
const withRepository = RESTIC_ON_PATH ? test : test.skip;
const whenRestic = (body: () => Promise<void> | void) => (RESTIC_ON_PATH ? body : () => undefined);

const RESTIC_TIMEOUT = 120_000;
const MACHINE = "test-machine-01";
const PASSWORD = "babel-verify-test";
const SELECTOR = "omp/a1b2c3";
/** The shape the engine mints: 32 random bytes, base64url. */
const BEARER = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");

/** A log with a NUL and a high byte: a comparison that passed through a text decoder would
 *  agree about a file this one disagrees about. */
const LOG = Buffer.from([
  ...Buffer.from('{"type":"user","text":"first"}\n'),
  0x00,
  0xff,
  ...Buffer.from(' tail"'),
]);

let home = "";
let repository = "";
let root = "";
let logPath = "";
let credentialFile = "";
let scratchDir = "";
let snapshotId = "";
let digest = "";
let service: Bun.Server<undefined> | null = null;

/** `<root>/omp/<id>.jsonl` is one session, as the omp adapter catalogues it. */
const claim = (path: string): SessionRef | null => {
  const match = /\/omp\/([^/]+)\.jsonl$/.exec(path);
  const sourceId = match?.[1];
  if (sourceId === undefined) return null;
  return { harness: "omp", sourceId, selector: `omp/${sourceId}`, primaryPath: path };
};

class Recorder implements OutputSink {
  readonly files: Record<string, readonly unknown[]> = {};
  written: Receipt | null = null;

  async write(file: OutputFile, rows: readonly unknown[]): Promise<void> {
    this.files[file] = rows;
  }

  async receipt(receipt: Receipt): Promise<void> {
    this.written = receipt;
  }
}

function deps(): VerifyDeps {
  return { claim, credentialFile, scratchDir };
}

async function run(input: Record<string, unknown>): Promise<{ receipt: Receipt; sink: Recorder }> {
  const sink = new Recorder();
  const receipt = await verify(
    VerifyInputSchema.parse({ machineId: MACHINE, ...input }),
    sink,
    deps(),
  );
  expect(sink.written).toEqual(receipt);
  // A verification writes no rows into any table: it observes the archive and reports.
  expect(Object.keys(sink.files)).toEqual([]);
  return { receipt, sink };
}

beforeAll(
  whenRestic(async () => {
    home = mkdtempSync(join(tmpdir(), "babel-verify-op-"));
    repository = join(home, "repo");
    root = join(home, "roots");
    scratchDir = join(home, "scratch");
    logPath = join(root, "omp", "a1b2c3.jsonl");
    mkdirSync(join(root, "omp"), { recursive: true });
    writeFileSync(logPath, LOG);
    digest = `sha256:${new Bun.CryptoHasher("sha256").update(LOG).digest("hex")}`;

    credentialFile = join(home, "restic-binding.json");
    service = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch(request) {
        if (request.headers.get("authorization") !== `Bearer ${BEARER}`) {
          return new Response("unauthorized", { status: 401 });
        }
        if (new URL(request.url).pathname !== RESTIC_SERVICE.path) {
          return new Response("unknown", { status: 404 });
        }
        return new Response(JSON.stringify({ repository, password: PASSWORD }), {
          headers: { "content-type": "application/json" },
        });
      },
    });
    writeFileSync(
      credentialFile,
      JSON.stringify({ url: `http://127.0.0.1:${service.port}`, bearer: BEARER }),
    );

    // The operator's own act: a repository is created once, by hand, and this operation never
    // creates one. The archive it reads is written here the way `archive` writes it.
    const config: ResticConfig = {
      repository,
      password: PASSWORD,
      binary: Bun.which("restic") ?? "/nonexistent/restic",
      cacheDir: join(home, "cache"),
      objectStore: null,
    };
    const repo = openRepo(config);
    expect(await repo.init()).toBe(true);
    const outcome = await repo.backup([root], { host: MACHINE, tags: [BABEL_TAG] });
    snapshotId = outcome.snapshotId;
  }),
  RESTIC_TIMEOUT,
);

afterAll(() => {
  service?.stop(true);
  if (home !== "") rmSync(home, { recursive: true, force: true });
});

withRepository(
  "a verification checks the repository and proves a catalogued session byte-exact",
  async () => {
    const { receipt } = await run({
      restore: { snapshotId, selector: SELECTOR, digest },
    });

    expect(receipt.kind).toBe("verify");
    expect(receipt.machineId).toBe(MACHINE);
    expect(receipt.runId).toMatch(/^run_/);
    expect(receipt.closure).toBe("completed");
    expect(receipt.reason).toBeUndefined();
    expect(receipt.counts).toEqual({
      checked: 1,
      checkErrors: 0,
      brokenPacks: 0,
      // The structural pass, which is what an unasked `readData` buys.
      dataRead: 0,
      restored: 1,
      // The catalogued digest was part of the comparison, and not only the snapshot's own
      // bytes: that is what makes this a proof about the archive rather than about restic.
      digestCompared: 1,
      archivedBytes: LOG.byteLength,
      restoredBytes: LOG.byteLength,
    });
    // The proof is the comparison, not the copy: an unnamed target leaves nothing behind.
    expect(readdirSync(scratchDir)).toEqual([]);
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "reading every stored byte is what the input asks for, and the receipt says which was read",
  async () => {
    const { receipt } = await run({ readData: true });
    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["dataRead"]).toBe(1);
    // Nothing was asked to be restored, which is not the same as a restore that failed.
    expect(receipt.counts["restored"]).toBe(0);
    expect(receipt.counts["archivedBytes"]).toBe(0);
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a catalogued digest the restored bytes disagree with fails the verification",
  async () => {
    const { receipt } = await run({
      restore: { snapshotId, selector: SELECTOR, digest: `sha256:${"0".repeat(64)}` },
    });
    expect(receipt.closure).toBe("failed");
    expect(receipt.counts["restored"]).toBe(0);
    // The restore itself worked — the bytes came back — and the catalog is what disagreed, so
    // the reason has to name both digests rather than saying the archive is broken.
    expect(receipt.counts["restoredBytes"]).toBe(LOG.byteLength);
    expect(receipt.reason).toContain(SELECTOR);
    expect(receipt.reason).toContain("was catalogued as");
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a session the snapshot does not hold is a failure that names the snapshot",
  async () => {
    const { receipt } = await run({
      restore: { snapshotId, selector: "omp/never-archived" },
    });
    expect(receipt.closure).toBe("failed");
    expect(receipt.counts["checked"]).toBe(1);
    expect(receipt.counts["restored"]).toBe(0);
    expect(receipt.reason).toContain("holds no session omp/never-archived");
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a named target keeps the restored file where the operator asked for it",
  async () => {
    const target = mkdtempSync(join(home, "kept-"));
    const { receipt } = await run({
      restore: { snapshotId, selector: SELECTOR, digest, target },
    });
    expect(receipt.closure).toBe("completed");
    expect(existsSync(join(target, logPath))).toBe(true);
    expect(readFileSync(join(target, logPath)).equals(LOG)).toBe(true);
  },
  RESTIC_TIMEOUT,
);
