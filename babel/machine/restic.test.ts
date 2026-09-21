/*
  THE READING HALF of machine/restic.ts, against a REAL restic repository — a temporary one,
  under the test's own directory (#338). restic is the thing being integrated with, and the two
  claims that matter are claims about restic: that a check reports a damaged pack instead of
  passing, and that what comes back out of a snapshot is byte for byte what went in. A fake of
  the JSON protocol would only prove this file's idea of restic.

  The ALLOWLIST rows need no binary and are never skipped. They are the file's most important
  rows: `forget`, `prune`, `repair` and `unlock` staying absent is repository policy, and a
  closed set of verbs is what makes adding a fifth a deliberate act rather than an accident.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  BABEL_TAG,
  RESTIC_VERBS,
  ResticError,
  openRepo,
  resticArgv,
  type Repo,
  type ResticConfig,
} from "./restic.ts";

/*
  The repository rows drive a real restic, so they need the binary — and a CI runner has none.
  Skipped, with the reason on every row, rather than failing: `machine/archive.test.ts` skips
  the same way and a skipped row is reported as unverified, which is the truth of a machine
  without restic.
*/
const RESTIC_ON_PATH = Bun.which("restic") !== null;
const withRepository = RESTIC_ON_PATH ? test : test.skip;
/** The hooks run regardless of a skipped row, so they are gated by the same fact. */
const whenRestic = (body: () => Promise<void> | void) => (RESTIC_ON_PATH ? body : () => undefined);

const RESTIC_TIMEOUT = 120_000;
const PASSWORD = "babel-restic-test";
const HOST = "test-machine-01";

/** A log with a NUL, a high byte and no final newline: "byte-exact" has to mean bytes, and a
 *  copy that went through a text decoder or gained a newline would pass a weaker comparison. */
const LOG = Buffer.from([
  ...Buffer.from('{"type":"user","text":"first"}\n'),
  0x00,
  0xff,
  ...Buffer.from(' binary tail, no newline"'),
]);

let home = "";
let repository = "";
let sourcePath = "";
let snapshotId = "";
let repo: Repo;

function config(overrides: Partial<ResticConfig> = {}): ResticConfig {
  return {
    repository,
    password: PASSWORD,
    binary: Bun.which("restic") ?? "/nonexistent/restic",
    cacheDir: join(home, "cache"),
    objectStore: null,
    ...overrides,
  };
}

/** A synthetic child exercises pipe ownership, not restic's protocol. Its output is larger
 *  than either pipe, and it closes both before exiting so EOF is not mistaken for settlement. */
async function withDumpChild(
  body: (repo: Repo, exited: string, bytes: Buffer) => Promise<void>,
  exitCode = 0,
): Promise<void> {
  const root = mkdtempSync(join(tmpdir(), "babel-dump-child-"));
  const binary = join(root, "restic");
  const exited = join(root, "exited");
  const bytes = Buffer.alloc(2 << 20, LOG);
  writeFileSync(
    binary,
    `#!${process.execPath}
import { closeSync, writeFileSync } from "node:fs";
process.on("exit", (code) => writeFileSync(${JSON.stringify(exited)}, String(code)));
await Bun.write(Bun.stdout, Buffer.alloc(${bytes.byteLength}, Buffer.from(${JSON.stringify([...LOG])})));
closeSync(1);
await Bun.write(Bun.stderr, Buffer.alloc(2 << 20, "synthetic diagnostic\\n"));
closeSync(2);
process.exit(${exitCode});
`,
    { mode: 0o755 },
  );
  try {
    await body(
      openRepo(config({ binary, repository: join(root, "repo"), cacheDir: join(root, "cache") })),
      exited,
      bytes,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

beforeAll(
  whenRestic(async () => {
    home = mkdtempSync(join(tmpdir(), "babel-restic-"));
    repository = join(home, "repo");
    const root = join(home, "roots", "omp");
    mkdirSync(root, { recursive: true });
    sourcePath = join(root, "a1b2c3.jsonl");
    writeFileSync(sourcePath, LOG);

    repo = openRepo(config());
    expect(await repo.init()).toBe(true);
    const outcome = await repo.backup([root], { host: HOST, tags: [BABEL_TAG] });
    snapshotId = outcome.snapshotId;
  }),
  RESTIC_TIMEOUT,
);

afterAll(() => {
  if (home !== "") rmSync(home, { recursive: true, force: true });
});

test("the verbs restic may be asked for are a closed set, and no destructive one is in it", () => {
  // The list itself, because it is the policy: never-delete means these eight, and a ninth is a
  // reviewable line in a diff rather than something a caller can reach for.
  expect([...RESTIC_VERBS]).toEqual([
    "cat",
    "init",
    "backup",
    "snapshots",
    "check",
    "ls",
    "dump",
    "restore",
  ]);

  for (const verb of ["forget", "prune", "repair", "unlock", "rewrite", "copy", "migrate"]) {
    let refused: unknown;
    try {
      resticArgv(verb, ["--json"]);
    } catch (err) {
      refused = err;
    }
    expect(refused).toBeInstanceOf(ResticError);
    // `refused` and not `exit`: the argv was never built, so nothing was contacted and nothing
    // could have been removed even if restic had been standing there.
    expect((refused as ResticError).kind).toBe("refused");
    expect((refused as ResticError).message).toContain(verb);
  }

  expect(resticArgv("check", ["--read-data"])).toEqual(["check", "--read-data"]);
});

test("a snapshot or a path that could be read as a flag is refused before restic is reached", async () => {
  // The values reaching these verbs come from a door's caller, and both travel as POSITIONALS.
  const unreachable = openRepo(config({ binary: "/nonexistent/restic" }));
  for (const asked of ["--insecure-no-password", "; rm -rf /", "latest~1", ""]) {
    await expect(unreachable.ls(asked)).rejects.toMatchObject({ kind: "refused" });
  }
  await expect(unreachable.dump("latest", "relative/path.jsonl")).rejects.toMatchObject({
    kind: "refused",
  });
  await expect(unreachable.dump("latest", "/log\0.jsonl")).rejects.toMatchObject({
    kind: "refused",
  });
  await expect(unreachable.restore("latest", { target: "out" })).rejects.toMatchObject({
    kind: "refused",
  });
});

test("dumpTo forwards exact binary bytes in order and awaits its sink", async () => {
  await withDumpChild(async (streaming, exited, expected) => {
    let offset = 0;
    let pending = false;
    const result = await streaming.dumpTo(
      "latest",
      "/session.jsonl",
      async (chunk) => {
        expect(pending).toBe(false);
        pending = true;
        await new Promise<void>((resolve) => setImmediate(resolve));
        expect(
          Buffer.from(chunk).equals(expected.subarray(offset, offset + chunk.byteLength)),
        ).toBe(true);
        offset += chunk.byteLength;
        pending = false;
      },
      { maxBytes: expected.byteLength },
    );
    expect(result).toEqual({ bytes: expected.byteLength });
    expect(offset).toBe(expected.byteLength);
    expect(readFileSync(exited, "utf8")).toBe("0");
    expect(
      await streaming.dump("latest", "/session.jsonl", { maxBytes: expected.byteLength }),
    ).toEqual(new Uint8Array(expected));
  });
}, 15_000);

test("dumpTo refuses before forwarding an over-bound chunk and settles both pipes", async () => {
  await withDumpChild(async (streaming, exited) => {
    let forwarded = 0;
    await expect(
      streaming.dumpTo(
        "latest",
        "/session.jsonl",
        (chunk) => {
          forwarded += chunk.byteLength;
        },
        { maxBytes: 0 },
      ),
    ).rejects.toMatchObject({ kind: "refused" });
    expect(forwarded).toBe(0);
    expect(readFileSync(exited, "utf8")).toBe("0");
  });
}, 15_000);

test("dumpTo stops forwarding after a sink rejection and settles before preserving the error", async () => {
  await withDumpChild(async (streaming, exited) => {
    const failure = new Error("synthetic sink failure");
    let calls = 0;
    await expect(
      streaming.dumpTo("latest", "/session.jsonl", async () => {
        calls += 1;
        await new Promise<void>((resolve) => setImmediate(resolve));
        throw failure;
      }),
    ).rejects.toBe(failure);
    expect(calls).toBe(1);
    expect(readFileSync(exited, "utf8")).toBe("0");
  });
}, 15_000);

test("dumpTo preserves even an undefined synchronous sink failure after settling", async () => {
  await withDumpChild(async (streaming, exited) => {
    let rejected = false;
    try {
      await streaming.dumpTo("latest", "/session.jsonl", () => {
        throw undefined;
      });
    } catch (error) {
      rejected = true;
      expect(error).toBeUndefined();
    }
    expect(rejected).toBe(true);
    expect(readFileSync(exited, "utf8")).toBe("0");
  });
}, 15_000);

test("dump retains restic's exit error ahead of a size refusal", async () => {
  await withDumpChild(async (streaming, exited) => {
    await expect(streaming.dump("latest", "/session.jsonl", { maxBytes: 0 })).rejects.toMatchObject(
      { kind: "exit", code: 12 },
    );
    expect(readFileSync(exited, "utf8")).toBe("12");
  }, 12);
}, 15_000);

withRepository(
  "a fresh repository passes structurally and with every data blob read",
  async () => {
    const structural = await repo.check();
    expect(structural.ok).toBe(true);
    expect(structural.dataRead).toBe("");
    expect(structural.errorCount).toBe(0);
    expect(structural.brokenPacks).toEqual([]);

    const whole = await repo.check({ readData: true });
    expect(whole.ok).toBe(true);
    // The two assurances are not the same assurance, so the outcome says which was bought.
    expect(whole.dataRead).toBe("all");

    const subset = await repo.check({ readData: "50%" });
    expect(subset.ok).toBe(true);
    expect(subset.dataRead).toBe("50%");
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a session listed, dumped and restored comes back byte for byte",
  async () => {
    const listed = await repo.ls(snapshotId);
    expect(listed.truncated).toBe(false);
    const file = listed.entries.find((entry) => entry.path === sourcePath);
    expect(file).toMatchObject({ type: "file", size: LOG.byteLength });

    const dumped = await repo.dump(snapshotId, sourcePath, { maxBytes: LOG.byteLength });
    expect(Buffer.from(dumped).equals(LOG)).toBe(true);
    let streamed = 0;
    expect(
      await repo.dumpTo(
        snapshotId,
        sourcePath,
        (chunk) => {
          expect(
            Buffer.from(chunk).equals(LOG.subarray(streamed, streamed + chunk.byteLength)),
          ).toBe(true);
          streamed += chunk.byteLength;
        },
        { maxBytes: LOG.byteLength },
      ),
    ).toEqual({ bytes: LOG.byteLength });
    expect(streamed).toBe(LOG.byteLength);

    const target = mkdtempSync(join(home, "restored-"));
    const outcome = await repo.restore(snapshotId, { target, include: [sourcePath] });
    expect(outcome.target).toBe(target);
    expect(outcome.filesRestored).toBeGreaterThan(0);
    const restored = readFileSync(join(target, sourcePath));
    expect(restored.equals(LOG)).toBe(true);
    // `--include` restores that path and not the root's other members.
    expect(await readdir(join(target, home, "roots", "omp"))).toEqual(["a1b2c3.jsonl"]);
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a dump larger than the caller's bound is refused rather than held",
  async () => {
    await expect(repo.dump(snapshotId, sourcePath, { maxBytes: 4 })).rejects.toMatchObject({
      kind: "refused",
    });
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a damaged pack is reported with restic's own words instead of the check passing",
  async () => {
    // Its own repository: the damage is real, and the rows above must not read a broken archive.
    const separate = mkdtempSync(join(home, "damaged-"));
    const root = join(separate, "root");
    mkdirSync(root, { recursive: true });
    writeFileSync(join(root, "b2c3d4.jsonl"), LOG);
    const broken = openRepo(config({ repository: join(separate, "repo") }));
    expect(await broken.init()).toBe(true);
    await broken.backup([root], { host: HOST, tags: [BABEL_TAG] });

    // restic shards `data/` two hex characters deep and some shards are empty, so the pack is
    // whichever entry of the walk is a file.
    const packs = join(separate, "repo", "data");
    const walked = await readdir(packs, { recursive: true, withFileTypes: true });
    const pack = walked.find((entry) => entry.isFile());
    const path = join(pack?.parentPath ?? packs, pack?.name ?? "");
    // restic writes its packs read-only, which is itself a small piece of the archive's
    // integrity: damaging one has to be deliberate.
    chmodSync(path, 0o644);
    writeFileSync(path, Buffer.concat([readFileSync(path), Buffer.from("garbage")]));

    // restic 0.19 already catches a pack whose bytes disagree with the index in its STRUCTURAL
    // pass, so what is under test is the reporting rather than which pass found it: an outcome
    // that said `ok` about a repository restic refuses is the failure that matters.
    const structural = await broken.check();
    expect(structural.ok).toBe(false);
    expect(structural.errorCount).toBeGreaterThan(0);

    const found = await broken.check({ readData: true });
    expect(found.ok).toBe(false);
    expect(found.errorCount).toBeGreaterThan(0);
    expect(found.brokenPacks.length).toBeGreaterThan(0);
    // restic's own words, because they carry the remedy — and a caller that only saw a boolean
    // would have to guess whether a byte, a file or the index was wrong.
    expect(found.errors.join(" ")).toContain("unexpected file size");
  },
  RESTIC_TIMEOUT,
);

withRepository(
  "a session restores out of a repository whose local log has been deleted",
  async () => {
    // The case an archive exists for. The bytes come from the snapshot, so the file being gone
    // changes nothing about the restore — which is the whole claim a backup makes.
    const gone = mkdtempSync(join(home, "gone-"));
    const root = join(gone, "root");
    mkdirSync(root, { recursive: true });
    const log = join(root, "c3d4e5.jsonl");
    writeFileSync(log, LOG);
    const outcome = await repo.backup([root], { host: HOST, tags: [BABEL_TAG] });
    rmSync(log);

    const target = mkdtempSync(join(home, "revived-"));
    await repo.restore(outcome.snapshotId, { target, include: [log] });
    expect(readFileSync(join(target, log)).equals(LOG)).toBe(true);
  },
  RESTIC_TIMEOUT,
);

/** Large synthetic pipes isolate the ls visitor's backpressure and child-settlement contract. */
async function withListingChild(body: (repo: Repo, exited: string) => Promise<void>): Promise<void> {
  const root = mkdtempSync(join(tmpdir(), "babel-ls-child-"));
  const binary = join(root, "restic");
  const exited = join(root, "exited");
  writeFileSync(binary, `#!${process.execPath}
import { closeSync, writeFileSync } from "node:fs";
process.on("exit", () => writeFileSync(${JSON.stringify(exited)}, "settled"));
for (let i = 0; i < 20003; i++) {
  await Bun.write(Bun.stdout, JSON.stringify({struct_type:"node",path:"/synthetic/"+i,type:"file",size:i,mtime:"2026-09-01T00:00:00Z"})+"\\n");
}
closeSync(1);
await Bun.write(Bun.stderr, Buffer.alloc(2 << 20, "synthetic diagnostic\\n"));
closeSync(2);
`, { mode: 0o755 });
  try {
    await body(openRepo(config({ binary, repository: join(root, "repo") })), exited);
  } finally { rmSync(root, { recursive: true, force: true }); }
}

test("streamed listing awaits each visitor and visits past the retained ls bound", async () => {
  await withListingChild(async (repo, exited) => {
    let count = 0;
    let active = false;
    await repo.lsTo("a".repeat(64), async entry => {
      expect(active).toBe(false);
      active = true;
      await Promise.resolve();
      expect(entry.path).toBe(`/synthetic/${count}`);
      count++;
      active = false;
    });
    expect(count).toBe(20003);
    expect(readFileSync(exited, "utf8")).toBe("settled");
    const retained = await repo.ls("a".repeat(64));
    expect(retained.entries.length).toBe(20000);
    expect(retained.entries[19999]?.path).toBe("/synthetic/19999");
    expect(retained.truncated).toBe(true);
  });
}, 30_000);

test("a failed listing visitor stops forwarding but drains and settles both child pipes", async () => {
  await withListingChild(async (repo, exited) => {
    const failure = new Error("synthetic consumer refusal");
    let called = 0;
    await expect(repo.lsTo("a".repeat(64), async () => {
      called++;
      await Promise.resolve();
      throw failure;
    })).rejects.toBe(failure);
    expect(called).toBe(1);
    expect(readFileSync(exited, "utf8")).toBe("settled");
  });
}, 30_000);
