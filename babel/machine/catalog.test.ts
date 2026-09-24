/*
  The catalog operation against a REAL restic repository (machine/test/restic-fixture.ts):
  synthetic session roots, snapshots taken through `Repo.backup` in a zone restic spells with an
  offset, and the storage document delivered through a loopback service exactly as a job's
  binding delivers it. What is under test is what the hub will ingest — which captures the rows
  name, in which order a bounded run reaches them, what the machine remembers between runs — and
  that no read leaves a lock in the repository, even when restic is killed halfway through.
*/

import { expect, test } from "bun:test";
import { appendFileSync, existsSync, readFileSync, rmSync, statSync, utimesSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  ARCHIVE_LABELS_REPORTED,
  CatalogInputSchema,
  ReceiptSchema,
  SessionRowSchema,
  type Receipt,
  type SessionRow,
} from "../contract.ts";
import { catalog, type CatalogDeps, type CatalogRepo } from "./catalog.ts";
import { outputCapacity, type OutputFile, type OutputSink } from "./output.ts";
import { openRepo, resticConfig, type Snapshot } from "./restic.ts";
import {
  writeClaudeSession,
  writeCodexRollout,
  writeCodexState,
  writeOmpSession,
} from "./test/fixtures.ts";
import { syntheticArchive, type SyntheticArchive } from "./test/restic-fixture.ts";

const TIMEOUT = 120_000;
const MACHINE = "catalog-machine-01";
const DEV = "dev-01";
const WORKSTATION = "workstation-linux";

const GROWING = "omp/-home-alex-babel/2026-09-01T00-00-00-000Z_01a0";
const GONE = "omp/-home-alex-babel/2026-09-01T01-00-00-000Z_02b0";
const OWN = "omp/-home-alex-babel/run_01.babel";
const ROLLOUT = "codex/sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl";
const HISTORY = "codex/state";
const CLAUDE = "claude/-home-alex-code/11111111-2222-4333-8444-555555555555";

class Recorder implements OutputSink {
  readonly files: Partial<Record<OutputFile, readonly unknown[]>> = {};
  written: Receipt | null = null;

  async write(file: OutputFile, rows: readonly unknown[]): Promise<void> {
    this.files[file] = rows;
  }

  async receipt(receipt: Receipt): Promise<void> {
    this.written = receipt;
  }
}

async function withArchive(body: (fx: SyntheticArchive) => Promise<void>): Promise<void> {
  const fx = await syntheticArchive();
  try {
    await body(fx);
  } finally {
    await fx.close();
  }
}

/** The bindings the dispatcher hands the operation: the service binding opened through the
 *  same delivery a job uses, the managed cache, and the output lease's own device. */
function deps(fx: SyntheticArchive, overrides: Partial<CatalogDeps> = {}): CatalogDeps {
  return {
    archive: async () =>
      openRepo(await resticConfig({ credentialFile: fx.credentialFile, env: fx.env })),
    cacheDir: join(fx.home, ".cache", "catalog"),
    capacity: () => outputCapacity(fx.home),
    ...overrides,
  };
}

async function run(
  fx: SyntheticArchive,
  input: Partial<{ full: boolean; maxSnapshots: number }> = {},
  overrides: Partial<CatalogDeps> = {},
): Promise<{ receipt: Receipt; rows: readonly SessionRow[] | undefined }> {
  const recorder = new Recorder();
  const receipt = await catalog(
    CatalogInputSchema.parse({ machineId: MACHINE, ...input }),
    recorder,
    deps(fx, overrides),
  );
  expect(recorder.written).toEqual(receipt);
  expect(ReceiptSchema.parse(receipt)).toEqual(receipt);
  // No read takes a lock, so none can be left behind.
  expect(await fx.locks()).toBe(0);
  const rows = recorder.files.sessions?.map((row) => SessionRowSchema.parse(row));
  return { receipt, rows };
}

/** The ids the machine remembers having listed, or null when it keeps no memory yet. */
function memory(fx: SyntheticArchive): readonly string[] | null {
  const hasher = new Bun.CryptoHasher("sha256").update(fx.repository).digest("hex");
  const path = join(fx.home, ".cache", "catalog", `${hasher}.json`);
  return existsSync(path)
    ? (JSON.parse(readFileSync(path, "utf8")) as { listed: string[] }).listed
    : null;
}

function stamp(path: string, at: string): void {
  utimesSync(path, new Date(at), new Date(at));
}

interface Fleet {
  /** dev-01's codex root alone, the oldest snapshot and the head of its chain. */
  readonly codex: Snapshot;
  /** dev-01's omp root, before the growing session grew and while the gone one existed. */
  readonly first: Snapshot;
  /** dev-01's omp root again: the head of that chain. */
  readonly second: Snapshot;
  /** A Go-shaped snapshot of every root at once under another label: its own chain. */
  readonly workstation: Snapshot;
  readonly paths: Readonly<Record<string, string>>;
  /** Each session's size as each snapshot holds it, keyed `<selector>@<snapshot id>`. */
  readonly sizes: ReadonlyMap<string, number>;
}

/**
 * Two labels and three chains, taken in this order: dev-01's codex root; dev-01's omp root
 * twice, with one session growing and another deleted in between; a `babel-store` backup of the
 * omp root after the session grew again; and one snapshot of every root under another label.
 * Newest first that is workstation, second, first, codex; the chain heads are workstation,
 * second and codex.
 */
async function fleet(fx: SyntheticArchive): Promise<Fleet> {
  const omp = fx.sessionRoot("omp");
  const codexRoot = fx.sessionRoot("codex");
  const claudeRoot = fx.sessionRoot("claude");
  const sizes = new Map<string, number>();
  const record = (snapshot: Snapshot, selectors: Record<string, string>): void => {
    for (const [selector, path] of Object.entries(selectors)) {
      sizes.set(`${selector}@${snapshot.id}`, statSync(path).size);
    }
  };

  const rollout = await writeCodexRollout(codexRoot, {
    date: ["2026", "09", "02"],
    name: "rollout-2026-09-02T10-00-00-000Z-abc",
  });
  const history = await writeCodexState(codexRoot, [1_788_000_000]);
  stamp(rollout, "2026-09-02T10:05:00.250Z");
  stamp(history, "2026-09-02T10:06:00.000Z");
  const codex = await fx.snapshot(DEV, [codexRoot]);
  record(codex, { [ROLLOUT]: rollout, [HISTORY]: history });

  const growing = await writeOmpSession(omp, {
    project: "-home-alex-babel",
    stem: "2026-09-01T00-00-00-000Z_01a0",
  });
  const gone = await writeOmpSession(omp, {
    project: "-home-alex-babel",
    stem: "2026-09-01T01-00-00-000Z_02b0",
  });
  const own = await writeOmpSession(omp, { project: "-home-alex-babel", stem: "run_01.babel" });
  stamp(growing, "2026-09-01T00:30:00.000Z");
  stamp(gone, "2026-09-01T01:30:00.000Z");
  stamp(own, "2026-09-01T02:30:00.000Z");
  const first = await fx.snapshot(DEV, [omp]);
  record(first, { [GROWING]: growing, [GONE]: gone, [OWN]: own });

  appendFileSync(growing, '{"type":"message","id":"later"}\n');
  stamp(growing, "2026-09-03T12:00:00.125Z");
  rmSync(gone);
  const second = await fx.snapshot(DEV, [omp]);
  record(second, { [GROWING]: growing, [OWN]: own });

  // The hub's own store is backed up into the same repository under `babel-store`. Were it read
  // as transcripts, dev-01's growing session would name this snapshot rather than `second`.
  appendFileSync(growing, '{"type":"message","id":"store-era"}\n');
  stamp(growing, "2026-09-04T12:00:00.000Z");
  await fx.snapshot(DEV, [omp], ["babel-store"]);

  const claude = await writeClaudeSession(claudeRoot, {
    project: "-home-alex-code",
    session: "11111111-2222-4333-8444-555555555555",
  });
  stamp(claude, "2026-09-03T09:30:00.000Z");
  const workstation = await fx.snapshot(WORKSTATION, [omp, codexRoot, claudeRoot]);
  record(workstation, {
    [GROWING]: growing,
    [OWN]: own,
    [ROLLOUT]: rollout,
    [HISTORY]: history,
    [CLAUDE]: claude,
  });

  return {
    codex,
    first,
    second,
    workstation,
    paths: {
      [GROWING]: growing,
      [GONE]: gone,
      [OWN]: own,
      [ROLLOUT]: rollout,
      [HISTORY]: history,
      [CLAUDE]: claude,
    },
    sizes,
  };
}

/** The row a capture of `selector` in `snapshot` must be. */
function expected(f: Fleet, selector: string, snapshot: Snapshot, modifiedAt: string): SessionRow {
  const [harness, ...rest] = selector.split("/");
  const path = f.paths[selector];
  const size = f.sizes.get(`${selector}@${snapshot.id}`);
  if (path === undefined || size === undefined) throw new Error(`no capture of ${selector}`);
  return SessionRowSchema.parse({
    selector,
    harness,
    source_id: rest.join("/"),
    kind: selector === OWN ? "agent" : "operator",
    archive_label: snapshot.host,
    archive_path: path,
    snapshot_id: snapshot.id,
    archived_at: new Date(Date.parse(snapshot.time)).toISOString(),
    size,
    modified_at: modifiedAt,
  });
}

test(
  "every session is catalogued under its newest capture per label, in UTC, and babel-store is never read",
  async () => {
    await withArchive(async (fx) => {
      const f = await fleet(fx);
      const { receipt, rows } = await run(fx);

      // restic spells both instants in the backing machine's zone; a row spells them in UTC.
      expect(f.second.time).toMatch(/\+05:30$/);
      const listed = await fx.repo.ls(f.second.id);
      const node = listed.entries.find((entry) => entry.path === f.paths[GROWING]);
      expect(node?.modifiedAt).toMatch(/^2026-09-03T17:30:00\.125[0-9]*\+05:30$/);

      expect(rows).toEqual([
        expected(f, CLAUDE, f.workstation, "2026-09-03T09:30:00.000Z"),
        expected(f, ROLLOUT, f.codex, "2026-09-02T10:05:00.250Z"),
        expected(f, ROLLOUT, f.workstation, "2026-09-02T10:05:00.250Z"),
        // The history file precedes its sibling `sessions/` in the listing, and is still Codex's.
        expected(f, HISTORY, f.codex, "2026-09-02T10:06:00.000Z"),
        expected(f, HISTORY, f.workstation, "2026-09-02T10:06:00.000Z"),
        // Grown between dev-01's two snapshots: the newer one is named, never the store's.
        expected(f, GROWING, f.second, "2026-09-03T12:00:00.125Z"),
        expected(f, GROWING, f.workstation, "2026-09-04T12:00:00.000Z"),
        // Deleted before dev-01's newest snapshot, and still catalogued from the older one.
        expected(f, GONE, f.first, "2026-09-01T01:30:00.000Z"),
        expected(f, OWN, f.second, "2026-09-01T02:30:00.000Z"),
        expected(f, OWN, f.workstation, "2026-09-01T02:30:00.000Z"),
      ]);

      expect(receipt).toMatchObject({
        kind: "catalog",
        machineId: MACHINE,
        closure: "completed",
        archive: {
          labels: [
            {
              label: WORKSTATION,
              snapshots: 1,
              newestAt: new Date(Date.parse(f.workstation.time)).toISOString(),
            },
            {
              label: DEV,
              snapshots: 3,
              newestAt: new Date(Date.parse(f.second.time)).toISOString(),
            },
          ],
          omitted: 0,
        },
      });
      expect(receipt.counts).toMatchObject({ snapshots: 4, listed: 4, pending: 0, captures: 10 });
      expect(receipt.counts["entries"]).toBeGreaterThan(10);
      const capacity = receipt.outputCapacity;
      expect(capacity?.bytes).toBeGreaterThan(0);
      expect(capacity?.free).toBeLessThanOrEqual(capacity?.bytes ?? 0);
    });
  },
  TIMEOUT,
);

test(
  "a bounded run lists chain heads first, and the memory carries the rest to the next run",
  async () => {
    await withArchive(async (fx) => {
      const f = await fleet(fx);

      // Newest first would reach dev-01's older omp snapshot before its codex head.
      const bounded = await run(fx, { maxSnapshots: 3 });
      expect(bounded.receipt.counts).toMatchObject({ snapshots: 4, listed: 3, pending: 1 });
      expect(bounded.rows?.map((row) => row.selector)).toContain(HISTORY);
      expect(bounded.rows?.map((row) => row.selector)).not.toContain(GONE);
      expect(memory(fx)).toEqual([f.workstation.id, f.second.id, f.codex.id].sort());

      const rest = await run(fx);
      expect(rest.receipt.counts).toMatchObject({ listed: 1, pending: 0, captures: 3 });
      expect(rest.rows?.map((row) => [row.selector, row.snapshot_id])).toEqual([
        [GROWING, f.first.id],
        [GONE, f.first.id],
        [OWN, f.first.id],
      ]);
      expect(memory(fx)).toEqual([f.workstation.id, f.second.id, f.first.id, f.codex.id].sort());

      const idle = await run(fx);
      expect(idle.receipt.counts).toMatchObject({ listed: 0, pending: 0, entries: 0, captures: 0 });
      expect(idle.rows).toEqual([]);

      // `full` sets the memory aside and starts it again from what it lists.
      const again = await run(fx, { full: true, maxSnapshots: 1 });
      expect(again.receipt.counts).toMatchObject({ listed: 1, pending: 3 });
      expect(new Set(again.rows?.map((row) => row.snapshot_id))).toEqual(
        new Set([f.workstation.id]),
      );
      expect(memory(fx)).toEqual([f.workstation.id]);
      const resumed = await run(fx);
      expect(resumed.receipt.counts).toMatchObject({ listed: 3, pending: 0 });
    });
  },
  TIMEOUT,
);

test(
  "restic killed mid-listing leaves no lock, and its snapshot stays pending",
  async () => {
    await withArchive(async (fx) => {
      await writeOmpSession(fx.sessionRoot("omp"), { project: "-home-alex-babel", stem: "one" });
      // Megabytes of listing, far past what a pipe holds: restic is still writing, and so still
      // running, while the first node is being read. None of these files is a session.
      const padding = join(fx.sessionRoot("omp"), "-home-alex-babel", "one", "padding");
      await mkdir(padding, { recursive: true });
      for (let index = 0; index < 3000; index++) {
        await writeFile(join(padding, `${String(index).padStart(4, "0")}-${"x".repeat(160)}`), "");
      }
      const only = await fx.snapshot(DEV, [fx.sessionRoot("omp")]);

      // restic behind a wrapper that names its pid; `exec` keeps it the pid restic runs as.
      const pidFile = join(fx.home, "restic.pid");
      const binary = join(fx.home, "bin", "restic-pid");
      await writeFile(
        binary,
        `#!/bin/sh\necho $$ > '${pidFile}'\nexec '${fx.config.binary}' "$@"\n`,
        {
          mode: 0o700,
        },
      );
      const repo = openRepo({ ...fx.config, binary });
      let locksWhileListing = -1;
      const killing: CatalogRepo = {
        repository: repo.repository,
        snapshots: () => repo.snapshots(),
        lsTo: (snapshotId, sink, paths) =>
          repo.lsTo(
            snapshotId,
            async (entry) => {
              if (locksWhileListing === -1) {
                locksWhileListing = await fx.locks();
                process.kill(Number(readFileSync(pidFile, "utf8").trim()), "SIGKILL");
              }
              await sink(entry);
            },
            paths,
          ),
      };

      const killed = await run(fx, {}, { archive: async () => killing });
      expect(locksWhileListing).toBe(0);
      expect(killed.receipt.closure).toBe("failed");
      expect(killed.receipt.reason).toContain(`snapshot ${only.id} could not be listed`);
      expect(killed.receipt.counts).toMatchObject({ listed: 0, pending: 1, captures: 0 });
      expect(killed.rows).toEqual([]);
      expect(memory(fx)).toBeNull();

      const next = await run(fx);
      expect(next.receipt.counts).toMatchObject({ listed: 1, pending: 0, captures: 1 });
    });
  },
  TIMEOUT,
);

test(
  "the memory advances only once the rows are written",
  async () => {
    await withArchive(async (fx) => {
      await writeOmpSession(fx.sessionRoot("omp"), { project: "-home-alex-babel", stem: "one" });
      await fx.snapshot(DEV, [fx.sessionRoot("omp")]);
      const refusing: OutputSink = {
        write: async () => {
          throw new Error("the output lease is full");
        },
        receipt: async () => undefined,
      };
      await expect(
        catalog(CatalogInputSchema.parse({ machineId: MACHINE }), refusing, deps(fx)),
      ).rejects.toThrow("the output lease is full");
      expect(memory(fx)).toBeNull();
      expect((await run(fx)).receipt.counts).toMatchObject({ listed: 1, captures: 1 });
    });
  },
  TIMEOUT,
);

test(
  "an archive the job cannot open fails the run whole and writes no rows",
  async () => {
    await withArchive(async (fx) => {
      await writeOmpSession(fx.sessionRoot("omp"), { project: "-home-alex-babel", stem: "one" });
      await fx.snapshot(DEV, [fx.sessionRoot("omp")]);
      const absent = join(fx.home, "no-binding.json");
      const { receipt, rows } = await run(
        fx,
        {},
        {
          archive: async () =>
            openRepo(await resticConfig({ credentialFile: absent, env: fx.env })),
        },
      );
      expect(receipt.closure).toBe("failed");
      expect(receipt.reason).toContain(absent);
      expect(receipt.archive).toBeUndefined();
      expect(rows).toBeUndefined();
      expect(memory(fx)).toBeNull();
    });
  },
  TIMEOUT,
);

test("the label report names the newest labels first and counts the rest", async () => {
  // Past the receipt's bound, restic is not what is under test: its snapshots are.
  const labels = ARCHIVE_LABELS_REPORTED + 2;
  const snapshots: Snapshot[] = Array.from({ length: labels }, (_, index) => ({
    id: index.toString(16).padStart(64, "0"),
    shortId: index.toString(16).padStart(8, "0"),
    time: new Date(Date.UTC(2026, 8, 1, 0, index)).toISOString().replace("Z", "+00:00"),
    parentId: null,
    host: `label-${String(index).padStart(3, "0")}`,
    paths: ["/home/synthetic/.codex"],
    tags: ["babel"],
  }));
  const recorder = new Recorder();
  const receipt = await catalog(
    CatalogInputSchema.parse({ machineId: MACHINE, maxSnapshots: 1 }),
    recorder,
    {
      archive: async () => ({
        repository: "/synthetic/repository",
        snapshots: async () => snapshots,
        lsTo: async () => undefined,
      }),
      cacheDir: "",
      capacity: async () => null,
    },
  );
  expect(ReceiptSchema.parse(receipt)).toEqual(receipt);
  expect(receipt.archive?.omitted).toBe(2);
  expect(receipt.archive?.labels).toHaveLength(ARCHIVE_LABELS_REPORTED);
  expect(receipt.archive?.labels[0]).toEqual({
    label: `label-${String(labels - 1).padStart(3, "0")}`,
    snapshots: 1,
    newestAt: new Date(Date.UTC(2026, 8, 1, 0, labels - 1)).toISOString(),
  });
  expect(receipt.archive?.labels.at(-1)?.label).toBe("label-002");
  expect(receipt.outputCapacity).toBeUndefined();
  expect(receipt.counts).toMatchObject({ snapshots: labels, listed: 1, pending: labels - 1 });
});
