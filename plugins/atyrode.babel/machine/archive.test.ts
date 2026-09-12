/*
  The archive operation against a REAL restic repository — a temporary one, under the test's own
  directory, with synthetic session roots. restic is the thing being integrated with; a fake of
  its JSON protocol would only prove this file's idea of restic, and the two facts that matter
  (a snapshot per root, and a second backup finding its parent) are facts about restic.
*/

import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputFile, OutputSink } from "./output.ts";
import { ArchiveInputSchema, archive, type ArchiveDeps } from "./archive.ts";
import { BABEL_TAG, RESTIC_ENV, openRepo, resticConfigFromEnv } from "./restic.ts";

const MACHINE = "test-machine-01";
const RESTIC_TIMEOUT = 120_000;

let home = "";
let repository = "";
let ompRoot = "";
let codexRoot = "";

/** `<root>/<harness>/<id>.jsonl` is one session; anything else in a root is archived but names
 *  no catalog row, exactly as a blob store or an index inside a real root does. */
const deps: ArchiveDeps = {
  roots: async () => [ompRoot, codexRoot],
  claim: (path: string): SessionRef | null => {
    const match = /\/(omp|codex)\/([^/]+)\.jsonl$/.exec(path);
    if (match === null) return null;
    const [, harness, sourceId] = match;
    if (harness === undefined || sourceId === undefined) return null;
    return {
      harness: harness === "omp" ? "omp" : "codex",
      sourceId,
      selector: `${harness}/${sourceId}`,
      primaryPath: path,
    };
  },
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

interface SessionRow {
  readonly selector: string;
  readonly host: string;
  readonly harness: string;
  readonly source_id: string;
  readonly snapshot_id: string;
  readonly archived_at: string;
  readonly seen_at: string;
}

function rows(recorder: Recorder): readonly SessionRow[] {
  return (recorder.files["sessions"] ?? []) as readonly SessionRow[];
}

async function run(input: Partial<{ machineId: string; roots: string[] }> = {}): Promise<{
  receipt: Receipt;
  sessions: readonly SessionRow[];
}> {
  const recorder = new Recorder();
  const receipt = await archive(
    ArchiveInputSchema.parse({ machineId: MACHINE, ...input }),
    recorder,
    deps,
  );
  expect(recorder.written).toEqual(receipt);
  return { receipt, sessions: rows(recorder) };
}

beforeAll(async () => {
  home = mkdtempSync(join(tmpdir(), "babel-archive-"));
  repository = join(home, "repo");
  ompRoot = join(home, "roots", "omp");
  codexRoot = join(home, "roots", "codex");
  mkdirSync(ompRoot, { recursive: true });
  mkdirSync(codexRoot, { recursive: true });
  writeFileSync(join(ompRoot, "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n');
  writeFileSync(join(ompRoot, "d4e5f6.jsonl"), '{"type":"user","text":"second"}\n');
  writeFileSync(join(ompRoot, "blob-not-a-session"), "opaque\n");
  writeFileSync(join(codexRoot, "0192ab.jsonl"), '{"type":"message","text":"third"}\n');

  // The repository is created by hand, once, for the deployment: the operation under test
  // never creates one, so the test plays the operator.
  process.env[RESTIC_ENV.repository] = repository;
  process.env[RESTIC_ENV.password] = "babel-archive-test";
  process.env[RESTIC_ENV.cacheDir] = join(home, "cache");
  expect(await openRepo(resticConfigFromEnv(process.env)).init()).toBe(true);
}, RESTIC_TIMEOUT);

afterAll(() => {
  delete process.env[RESTIC_ENV.repository];
  delete process.env[RESTIC_ENV.password];
  delete process.env[RESTIC_ENV.cacheDir];
  delete process.env["RESTIC_REPOSITORY"];
  delete process.env["RESTIC_PASSWORD"];
  if (home !== "") rmSync(home, { recursive: true, force: true });
});

beforeEach(() => {
  process.env[RESTIC_ENV.repository] = repository;
  process.env[RESTIC_ENV.password] = "babel-archive-test";
});

test(
  "a backup snapshots each root on its own and catalogues every session it archived",
  async () => {
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("completed");
    expect(receipt.kind).toBe("archive");
    expect(receipt.machineId).toBe(MACHINE);
    expect(receipt.runId).toMatch(/^run_/);
    expect(receipt.counts["roots"]).toBe(2);
    expect(receipt.counts["snapshots"]).toBe(2);
    expect(receipt.counts["sessions"]).toBe(3);
    // The file no adapter claims is archived and simply names no row.
    expect(receipt.counts["unclaimed"]).toBe(1);
    expect(receipt.counts["filesNew"]).toBe(4);

    const snapshots = await openRepo(resticConfigFromEnv(process.env)).snapshots();
    expect(snapshots.length).toBe(2);
    const byRoot = new Map(snapshots.map((snapshot) => [snapshot.paths[0], snapshot]));
    expect([...byRoot.keys()].sort()).toEqual([codexRoot, ompRoot].sort());
    for (const snapshot of snapshots) {
      expect(snapshot.host).toBe(MACHINE);
      expect(snapshot.tags).toEqual([BABEL_TAG]);
      expect(snapshot.parentId).toBeNull();
    }

    // One snapshot id per root, and each session carries its own root's.
    const omp = byRoot.get(ompRoot);
    const codex = byRoot.get(codexRoot);
    expect(omp === undefined || codex === undefined).toBe(false);
    expect(omp?.id).not.toBe(codex?.id);
    const snapshotBySelector = Object.fromEntries(
      sessions.map((row) => [row.selector, row.snapshot_id]),
    );
    expect(snapshotBySelector).toEqual({
      "omp/a1b2c3": omp?.id ?? "",
      "omp/d4e5f6": omp?.id ?? "",
      "codex/0192ab": codex?.id ?? "",
    });
    for (const row of sessions) {
      expect(row.host).toBe(MACHINE);
      expect(`${row.harness}/${row.source_id}`).toBe(row.selector);
      // restic's own recorded time for that snapshot, so a catalog row and `restic snapshots`
      // never disagree about when the capture was taken.
      expect(row.archived_at).toBe(
        (row.harness === "omp" ? omp?.time : codex?.time) ?? "",
      );
    }
  },
  RESTIC_TIMEOUT,
);

test(
  "a second backup of unchanged roots is a parent-linked snapshot that adds no file",
  async () => {
    const before = await openRepo(resticConfigFromEnv(process.env)).snapshots();
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["snapshots"]).toBe(2);
    expect(receipt.counts["filesNew"]).toBe(0);
    expect(receipt.counts["filesChanged"]).toBe(0);
    expect(receipt.counts["filesUnmodified"]).toBe(4);
    // Both new snapshots found a parent: the roots were re-read against the last capture
    // instead of from scratch, which is what per-root snapshots protect.
    expect(receipt.counts["snapshotsParented"]).toBe(2);

    const after = await openRepo(resticConfigFromEnv(process.env)).snapshots();
    expect(after.length).toBe(before.length + 2);
    const minted = new Set(sessions.map((row) => row.snapshot_id));
    expect(minted.size).toBe(2);
    const parents = new Map(before.map((snapshot) => [snapshot.paths[0], snapshot.id]));
    for (const snapshot of after.filter((candidate) => minted.has(candidate.id))) {
      expect(snapshot.parentId).toBe(parents.get(snapshot.paths[0]) ?? "");
    }
  },
  RESTIC_TIMEOUT,
);

test(
  "a changed session is archived again and recatalogued under the new snapshot",
  async () => {
    writeFileSync(join(ompRoot, "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n{"type":"agent"}\n');
    const { receipt, sessions } = await run({ roots: [ompRoot] });

    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["roots"]).toBe(1);
    expect(receipt.counts["filesChanged"]).toBe(1);
    expect(receipt.counts["filesUnmodified"]).toBe(2);
    expect(sessions.map((row) => row.selector).sort()).toEqual(["omp/a1b2c3", "omp/d4e5f6"]);
  },
  RESTIC_TIMEOUT,
);

test(
  "the repository is the job's binding, never the ambient environment",
  async () => {
    // A machine whose shell exports its own restic coordinates must not change where Babel
    // archives, or under which password: the child's environment is built, not inherited.
    process.env["RESTIC_REPOSITORY"] = join(home, "ambient-repo");
    process.env["RESTIC_PASSWORD"] = "not-the-repository-password";
    const { receipt } = await run({ roots: [codexRoot] });

    expect(receipt.closure).toBe("completed");
    expect(readdirSync(home)).not.toContain("ambient-repo");
  },
  RESTIC_TIMEOUT,
);

test("a missing binding fails the run and names the binding", async () => {
  delete process.env[RESTIC_ENV.password];
  const { receipt, sessions } = await run();

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain(RESTIC_ENV.password);
  expect(sessions).toEqual([]);
  expect(receipt.counts["snapshots"]).toBe(0);
});

test(
  "a root that cannot be backed up loses its own snapshot, not the others",
  async () => {
    const missing = join(home, "roots", "was-here");
    const { receipt, sessions } = await run({ roots: [ompRoot, missing] });

    expect(receipt.counts["roots"]).toBe(2);
    expect(receipt.counts["snapshots"]).toBe(1);
    // The healthy root is archived and catalogued all the same.
    expect(sessions.map((row) => row.selector).sort()).toEqual(["omp/a1b2c3", "omp/d4e5f6"]);
    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).toContain(missing);
    // restic's own diagnosis reaches the operator, unwrapped from its --json error envelope.
    expect(receipt.reason).toContain("do not exist");
    expect(receipt.reason).not.toContain("message_type");
  },
  RESTIC_TIMEOUT,
);

test(
  "a password that does not open the repository says so, and is not read as a missing one",
  async () => {
    // The two failures need different remedies — fix the binding, or create the deployment's
    // repository — so restic's own diagnosis is what the receipt carries.
    process.env[RESTIC_ENV.password] = "not-the-repository-password";
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).not.toContain("no repository at");
    expect(receipt.reason?.toLowerCase()).toContain("password");
    expect(sessions).toEqual([]);
  },
  RESTIC_TIMEOUT,
);

test(
  "a repository that does not exist is a failure, never a repository this run created",
  async () => {
    const absent = join(home, "absent-repo");
    process.env[RESTIC_ENV.repository] = absent;
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).toContain(absent);
    expect(sessions).toEqual([]);
    expect(readdirSync(home)).not.toContain("absent-repo");
  },
  RESTIC_TIMEOUT,
);

test("a machine with no session root is skipped, not a successful backup", async () => {
  const recorder = new Recorder();
  const receipt = await archive(ArchiveInputSchema.parse({ machineId: MACHINE }), recorder, {
    roots: async () => [],
    claim: deps.claim,
  });

  expect(receipt.closure).toBe("skipped");
  expect(receipt.reason).toBe("no session root exists on this host");
  expect(receipt.counts["snapshots"]).toBe(0);
  // The declared output is still written, as an empty document rather than a missing file.
  expect(recorder.files["sessions"]).toEqual([]);
});
