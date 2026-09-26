/*
  The preparation from the archive (#453), against a REAL restic repository
  (`machine/test/restic-fixture.ts`): synthetic session roots under the fixture's own home,
  snapshots taken under two host labels, the catalog run over them, and the hub's own input built
  from the catalog's rows exactly as the selection builds it — grouped by snapshot, `modifiedAt`
  read back from `modified_at`. What is pinned is what the hub and a model will read: which bytes
  were sealed and from which capture, what the rows say, what a second preparation does not do
  again, and each way a preparation refuses whole.

  The identity tests at the top are the preparation record's own and need no archive.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import { chmodSync, existsSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  CatalogInputSchema,
  MATERIAL_INDEX,
  MATERIAL_SESSIONS,
  MAX_MATERIAL_BYTES,
  MaterialIndexSchema,
  PREFLIGHT_SCHEMA,
  PrepareInputSchema,
  ReceiptSchema,
  SessionRowSchema,
  materialFile,
  type CaptureGroup,
  type MaterialIndex,
  type Receipt,
  type SessionRow,
} from "../contract.ts";
import { catalog } from "./catalog.ts";
import { materialSink, outputCapacity, type OutputFile, type OutputSink } from "./output.ts";
import { PREFLIGHT_DETECTORS } from "./preflight.ts";
import {
  PREPARATION_SCHEMA,
  newPreparation,
  prepare,
  resolveRedaction,
  type PreparationEntry,
  type PrepareDeps,
} from "./prepare.ts";
import { openRepo, resticConfig, type ResticConfig, type Snapshot } from "./restic.ts";
import { sessionDigester } from "./session-records.ts";
import {
  digestOf,
  writeClaudeSession,
  writeCodexRollout,
  writeOmpSession,
} from "./test/fixtures.ts";
import { syntheticArchive, type SyntheticArchive } from "./test/restic-fixture.ts";

const TIMEOUT = 120_000;
const MACHINE = "prepare-machine-01";
const DEV = "dev-01";
const WORKSTATION = "workstation-linux";

const OMP = "omp/-home-alex-babel/2026-09-01T00-00-00-000Z_01a0";
const LEAKY = "omp/-home-alex-ops/2026-09-04T00-00-00-000Z_03c0";
const OWN = "omp/-home-alex-babel/run_01.babel";
const ROLLOUT = "codex/sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl";
const CLAUDE = "claude/-home-alex-code/11111111-2222-4333-8444-555555555555";

/** A credential in a format the scan matches, assembled so no literal of it is committed. */
const LEAKED_KEY = `${"AKIA"}IOSFODNN7SYNTH01`;

// ------------------------------------------------------------------ the preparation's identity

const entry = (sourceId: string, capture: string, source: string): PreparationEntry => ({
  host: DEV,
  harness: "omp",
  sourceId,
  captureDigest: `sha256:${capture.repeat(64)}`,
  sourceDigest: `sha256:${source.repeat(64)}`,
});

test("a preparation id is the selection's content, not the moment it was fixed", () => {
  const selection = [entry("a1b2c3", "a", "b"), entry("d4e5f6", "c", "d")];
  const first = newPreparation("2026-09-12T10:00:00.000Z", selection);
  const later = newPreparation("2026-09-13T23:59:59.999Z", selection);

  expect(first.id).toMatch(/^prep-[0-9a-f]{64}$/);
  expect(later.id).toBe(first.id);
  expect(later.preparedAt).not.toBe(first.preparedAt);
  expect(first.schema).toBe(PREPARATION_SCHEMA);

  // The input's order is not content: the record is canonically ordered before it is derived.
  const reversed = newPreparation("2026-09-12T10:00:00.000Z", [...selection].reverse());
  expect(reversed.id).toBe(first.id);
  expect(reversed.selection.map((held) => held.sourceId)).toEqual(["a1b2c3", "d4e5f6"]);
});

test("a different capture, a different reading, or a different scope is a different id", () => {
  const base = [entry("a1b2c3", "a", "b"), entry("d4e5f6", "c", "d")];
  const id = newPreparation("2026-09-12T10:00:00.000Z", base).id;
  const ids = new Set([
    id,
    // one session's bytes changed
    newPreparation("2026-09-12T10:00:00.000Z", [
      entry("a1b2c3", "e", "b"),
      entry("d4e5f6", "c", "d"),
    ]).id,
    // the same bytes, normalized differently: our reading of the corpus changed
    newPreparation("2026-09-12T10:00:00.000Z", [
      entry("a1b2c3", "a", "e"),
      entry("d4e5f6", "c", "d"),
    ]).id,
    // a narrower scope over unchanged sessions
    newPreparation("2026-09-12T10:00:00.000Z", [entry("a1b2c3", "a", "b")]).id,
    // the same sessions, recorded by another machine
    newPreparation(
      "2026-09-12T10:00:00.000Z",
      base.map((held) => ({ ...held, host: WORKSTATION })),
    ).id,
  ]);
  expect(ids.size).toBe(5);
});

test("a scope that names nothing, or one session twice, is refused", () => {
  expect(() => newPreparation("2026-09-12T10:00:00.000Z", [])).toThrow(/selection is empty/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [
      entry("a1b2c3", "a", "b"),
      entry("a1b2c3", "a", "b"),
    ]),
  ).toThrow(/twice/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [{ ...entry("a1b2c3", "a", "b"), sourceId: "" }]),
  ).toThrow(/names no session/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [
      { ...entry("a1b2c3", "a", "b"), captureDigest: "" },
    ]),
  ).toThrow(/no capture digest/);
});

test("the bytes of a capture and the records in it are digested apart", () => {
  const digest = (text: string) => {
    const digester = sessionDigester();
    digester.write(new TextEncoder().encode(text));
    return digester.finish();
  };
  const original = digest('{"type":"user","text":"hello"}\n{"type":"agent","text":"hi"}\n');
  expect(original.bytes).toBe(60);
  // The same records, spelled differently: a reflowed, reordered log is the same corpus.
  const respelled = digest('{ "text":"hello" , "type":"user"}\n{"text": "hi", "type":"agent"}\n');
  expect(respelled.captureDigest).not.toBe(original.captureDigest);
  expect(respelled.sourceDigest).toBe(original.sourceDigest);
  // A record that says something else is not the same corpus.
  expect(
    digest('{"type":"user","text":"hello"}\n{"type":"agent","text":"bye"}\n').sourceDigest,
  ).not.toBe(original.sourceDigest);
  // A torn final line is evidence that was seen, not evidence dropped.
  expect(digest('{"type":"user","text":"hello"}\n{"type":"agent","te').sourceDigest).not.toBe(
    digest('{"type":"user","text":"hello"}\n').sourceDigest,
  );
});

// ---------------------------------------------------------------------------- the fleet

/*
  ONE SYNTHETIC FLEET for every archive test below: dev-01's omp and codex roots in one
  Go-shaped snapshot, the workstation's claude root in another, and the catalog's rows over
  both. Each test prepares a subset of those captures into directories of its own.
*/
let fx: SyntheticArchive;
let dev: Snapshot;
let workstation: Snapshot;
/** The catalog's rows, by selector. */
const catalogued = new Map<string, SessionRow>();
/** Each session's archived bytes' digest, taken before the live files were changed. */
const archived = new Map<string, string>();
/** Scratch directories the tests made, removed at the end. */
let scratch = "";

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

beforeAll(async () => {
  fx = await syntheticArchive();
  scratch = await mkdtemp(join(tmpdir(), "babel-prepare-"));
  const omp = fx.sessionRoot("omp");
  const codex = fx.sessionRoot("codex");
  const claude = fx.sessionRoot("claude");
  const paths: Record<string, string> = {
    [OMP]: await writeOmpSession(omp, {
      project: "-home-alex-babel",
      stem: "2026-09-01T00-00-00-000Z_01a0",
      title: "Porting the adapters",
      cwd: "/home/alex/babel",
      turns: [
        { toolCalls: 2, usage: { totalTokens: 8504, cost: 0.5 } },
        { toolCalls: 1, usage: { totalTokens: 8725, cost: 0.25 } },
      ],
      toolErrors: 1,
    }),
    [LEAKY]: await writeOmpSession(omp, {
      project: "-home-alex-ops",
      stem: "2026-09-04T00-00-00-000Z_03c0",
      title: "Rotating the deploy key",
      cwd: "/home/alex/ops",
      trailing: [
        JSON.stringify({
          type: "message",
          message: {
            role: "user",
            content: `deploy with aws_access_key_id ${LEAKED_KEY} and then stop`,
          },
        }),
      ],
    }),
    [OWN]: await writeOmpSession(omp, {
      project: "-home-alex-babel",
      stem: "run_01.babel",
      runId: "run_01",
    }),
    [ROLLOUT]: await writeCodexRollout(codex, {
      date: ["2026", "09", "02"],
      name: "rollout-2026-09-02T10-00-00-000Z-abc",
      cwd: "/home/alex/manifold",
      delivered: "Port the session adapters to TypeScript, keeping the identities",
    }),
    [CLAUDE]: await writeClaudeSession(claude, {
      project: "-home-alex-code",
      session: "11111111-2222-4333-8444-555555555555",
      title: "Reviewing the broker's restart path",
      cwd: "/home/alex/code",
    }),
  };
  for (const [selector, path] of Object.entries(paths))
    archived.set(selector, await digestOf(path));
  dev = await fx.snapshot(DEV, [omp, codex]);
  workstation = await fx.snapshot(WORKSTATION, [claude]);

  const recorder = new Recorder();
  await catalog(CatalogInputSchema.parse({ machineId: MACHINE }), recorder, {
    archive: async () => openRepo(await delivered()),
    cacheDir: "",
    capacity: async () => null,
  });
  for (const row of recorder.files.sessions ?? []) {
    const parsed = SessionRowSchema.parse(row);
    catalogued.set(parsed.selector, parsed);
  }
  expect([...catalogued.keys()].sort()).toEqual([CLAUDE, ROLLOUT, LEAKY, OMP, OWN].sort());

  // THE MACHINE'S OWN FILES ARE NOT WHAT IS READ. Every live log now says something else, and
  // the session roots are unreadable: a preparation that opened one would fail or seal the wrong
  // bytes, and every capture digest below is checked against the ARCHIVED bytes.
  for (const path of Object.values(paths)) writeFileSync(path, '{"type":"user","text":"live"}\n');
  for (const root of [omp, codex, claude]) chmodSync(root, 0o000);
}, TIMEOUT);

afterAll(async () => {
  for (const harness of ["omp", "codex", "claude"] as const) {
    chmodSync(fx.sessionRoot(harness), 0o700);
  }
  await fx.close();
  await rm(scratch, { recursive: true, force: true });
});

/** The storage document through the same delivery a job uses: the loopback service's binding. */
function delivered(): Promise<ResticConfig> {
  return resticConfig({ credentialFile: fx.credentialFile, env: fx.env });
}

/** The hub's input for these sessions, built from the catalog's rows as the selection builds
 *  it: grouped by snapshot, the modification time read back as epoch milliseconds. */
function offer(selectors: readonly string[]): CaptureGroup[] {
  const groups = new Map<string, CaptureGroup & { sessions: CaptureGroup["sessions"][number][] }>();
  for (const selector of selectors) {
    const row = catalogued.get(selector);
    if (row === undefined) throw new Error(`the catalog holds no ${selector}`);
    let group = groups.get(row.snapshot_id);
    if (group === undefined) {
      group = { snapshotId: row.snapshot_id, label: row.archive_label, sessions: [] };
      groups.set(row.snapshot_id, group);
    }
    group.sessions.push({
      harness: row.harness,
      sourceId: row.source_id,
      path: row.archive_path,
      size: row.size,
      modifiedAt: Date.parse(row.modified_at),
    });
  }
  return [...groups.values()];
}

/** A directory of this test's own under the suite's scratch. */
async function directory(name: string): Promise<string> {
  return await mkdtemp(join(scratch, `${name}-`));
}

/** restic behind a wrapper that logs every verb it is run with: the observable for "no fetch". */
async function counted(): Promise<{ readonly config: ResticConfig; calls(): Promise<string[]> }> {
  const dir = await directory("counted");
  const log = join(dir, "calls");
  const wrapper = join(dir, "restic");
  await writeFile(log, "");
  await writeFile(
    wrapper,
    `#!/bin/sh\nprintf '%s\\n' "$1" >> '${log}'\nexec '${fx.config.binary}' "$@"\n`,
    { mode: 0o700 },
  );
  return {
    config: { ...fx.config, binary: wrapper },
    calls: async () => (await readFile(log, "utf8")).split("\n").filter((line) => line !== ""),
  };
}

function deps(overrides: Partial<PrepareDeps> = {}): PrepareDeps {
  return {
    archive: async () => openRepo(await delivered()),
    repository: async () => (await delivered()).repository,
    capacity: () => outputCapacity(scratch),
    ...overrides,
  };
}

async function run(
  selectors: readonly string[],
  input: Partial<{
    machineId: string;
    agentSessions: boolean;
    preflight: "redact" | "refuse" | "off";
    captures: CaptureGroup[];
  }> = {},
  overrides: Partial<PrepareDeps> = {},
): Promise<{ receipt: Receipt; rows: readonly SessionRow[] }> {
  const recorder = new Recorder();
  const receipt = await prepare(
    PrepareInputSchema.parse({ machineId: MACHINE, captures: offer(selectors), ...input }),
    recorder,
    deps(overrides),
  );
  expect(recorder.written).toEqual(receipt);
  expect(ReceiptSchema.parse(receipt)).toEqual(receipt);
  // A read never takes a lock, so none is ever left behind.
  expect(await fx.locks()).toBe(0);
  return {
    receipt,
    rows: (recorder.files.sessions ?? []).map((row) => SessionRowSchema.parse(row)),
  };
}

function indexOf(material: string): MaterialIndex {
  return MaterialIndexSchema.parse(
    JSON.parse(readFileSync(join(material, MATERIAL_INDEX), "utf8")),
  );
}

function idOf(receipt: Receipt): string {
  return (receipt.preparation as { id?: string } | undefined)?.id ?? "";
}

const sha256 = (bytes: Uint8Array | string): string =>
  `sha256:${new Bun.CryptoHasher("sha256").update(bytes).digest("hex")}`;

// -------------------------------------------------------------------------------- reading

test(
  "captures from two labels are prepared into one material with origin set",
  async () => {
    const material = await directory("material");
    const { receipt } = await run([OMP, ROLLOUT, CLAUDE], {}, { material: materialSink(material) });

    expect(receipt.kind).toBe("prepare");
    expect(receipt.closure).toBe("completed");
    expect(receipt.reason).toBeUndefined();
    expect(receipt.counts).toMatchObject({ offered: 3, selected: 3, fetched: 3, reused: 0 });
    expect(receipt.counts["fetchedBytes"]).toBe(
      [OMP, ROLLOUT, CLAUDE].reduce((sum, selector) => sum + catalogued.get(selector)!.size, 0),
    );
    // The lease was measured, which is what the hub bounds the next material on this machine by.
    expect(receipt.outputCapacity?.bytes).toBeGreaterThan(0);

    const index = indexOf(material);
    expect(receipt.material).toEqual(index);
    expect(index.machineId).toBe(MACHINE);
    expect(index.sessions.map((held) => held.selector).sort()).toEqual(
      [CLAUDE, ROLLOUT, OMP].sort(),
    );
    for (const [ordinal, held] of index.sessions.entries()) {
      const row = catalogued.get(held.selector)!;
      // EVERY ENTRY NAMES THE CAPTURE ITS BYTES CAME FROM, whichever label recorded it.
      expect(held.origin).toEqual({
        label: row.archive_label,
        snapshotId: row.snapshot_id,
        path: row.archive_path,
      });
      expect(held.origin?.snapshotId).toBe(held.selector === CLAUDE ? workstation.id : dev.id);
      // The archived bytes, not the machine's own live file that says something else.
      expect(held.captureDigest).toBe(archived.get(held.selector)!);
      expect(held.file).toBe(materialFile(ordinal, held.selector));
      // One canonical record per line, and the source digest is over exactly those bytes.
      const body = readFileSync(join(material, MATERIAL_SESSIONS, held.file));
      expect(held.sourceDigest).toBe(sha256(body));
      const lines = body
        .toString("utf8")
        .split("\n")
        .filter((line) => line !== "");
      expect(lines).toHaveLength(held.records);
      for (const line of lines) expect(JSON.stringify(JSON.parse(line) as unknown)).toBe(line);
    }

    // THE SELECTION NAMES THE MACHINE THAT RECORDED EACH SESSION, not the one that prepared it,
    // so the same captures are the same preparation wherever they are prepared.
    const selection = (receipt.preparation as { selection: PreparationEntry[] }).selection;
    expect(selection.map((held) => held.host).sort()).toEqual([DEV, DEV, WORKSTATION]);
    const elsewhere = await run([OMP, ROLLOUT, CLAUDE], { machineId: "another-machine" });
    expect(elsewhere.receipt.closure).toBe("completed");
    expect(idOf(elsewhere.receipt)).toBe(idOf(receipt));
  },
  TIMEOUT,
);

test(
  "sessions.json rows parse with SessionRowSchema and carry archived_at from the snapshot",
  async () => {
    const { receipt, rows } = await run([OMP, ROLLOUT, CLAUDE]);
    expect(receipt.closure).toBe("completed");
    expect(rows.map((row) => row.selector).sort()).toEqual([CLAUDE, ROLLOUT, OMP].sort());
    for (const row of rows) {
      const listed = catalogued.get(row.selector)!;
      const snapshot = row.selector === CLAUDE ? workstation : dev;
      // The snapshot's own time, as restic stated it in the backing machine's zone, normalized.
      expect(snapshot.time).toContain("+05:30");
      expect(row.archived_at).toBe(new Date(Date.parse(snapshot.time)).toISOString());
      // THE ROW NAMES THE CAPTURE THE CATALOG NAMED, so the hub applies what it read.
      expect({
        archive_label: row.archive_label,
        archive_path: row.archive_path,
        snapshot_id: row.snapshot_id,
        archived_at: row.archived_at,
        size: row.size,
        modified_at: row.modified_at,
        kind: row.kind,
      }).toEqual({
        archive_label: listed.archive_label,
        archive_path: listed.archive_path,
        snapshot_id: listed.snapshot_id,
        archived_at: listed.archived_at,
        size: listed.size,
        modified_at: listed.modified_at,
        kind: "operator",
      });
      expect(row.content_digest).toBe(archived.get(row.selector)!);
    }
    // And what the capture says about itself, from the stream that was sealed.
    const bySelector = new Map(rows.map((row) => [row.selector, row]));
    expect(bySelector.get(OMP)).toMatchObject({
      title: "Porting the adapters",
      title_provenance: "recorded",
      workspace: "/home/alex/babel",
      cost_usd: 0.75,
      total_tokens: 17229,
      turns: 2,
      tool_errors: 1,
    });
    expect(bySelector.get(ROLLOUT)).toMatchObject({
      title: "Port the session adapters to TypeScript, keeping the identities",
      title_provenance: "derived",
      workspace: "/home/alex/manifold",
    });
    expect(bySelector.get(CLAUDE)).toMatchObject({
      title: "Reviewing the broker's restart path",
      title_provenance: "recorded",
      workspace: "/home/alex/code",
    });
  },
  TIMEOUT,
);

test(
  "a cache hit makes no restic call",
  async () => {
    const cacheDir = await directory("cache");
    const restic = await counted();
    const bindings = {
      archive: async () => openRepo(restic.config),
      repository: async () => fx.repository,
      cacheDir,
    };
    const one = await directory("material");
    const first = await run(
      [OMP, ROLLOUT, CLAUDE],
      {},
      { ...bindings, material: materialSink(one) },
    );
    expect(first.receipt.counts).toMatchObject({ fetched: 3, reused: 0 });
    // One snapshot lookup for both labels' snapshots, then one dump per capture.
    expect((await restic.calls()).sort()).toEqual(["dump", "dump", "dump", "snapshots"]);

    const two = await directory("material");
    const again = await run(
      [OMP, ROLLOUT, CLAUDE],
      {},
      { ...bindings, material: materialSink(two) },
    );
    // THE OBSERVABLE: no restic child was spawned the second time.
    expect(await restic.calls()).toHaveLength(4);
    expect(again.receipt.counts).toMatchObject({
      fetched: 0,
      fetchedBytes: 0,
      reused: 3,
      selected: 3,
    });
    expect(idOf(again.receipt)).toBe(idOf(first.receipt));
    expect(again.receipt.preflight).toEqual(first.receipt.preflight);
    // The rows are the same rows, snapshot times and facts included, off the kept readings.
    expect(again.rows).toEqual(first.rows);
    // AND THE MATERIAL IS THE SAME EVIDENCE, byte for byte, under the same names.
    expect(again.receipt.material?.sessions).toEqual(first.receipt.material?.sessions);
    for (const held of again.receipt.material?.sessions ?? []) {
      const body = readFileSync(join(two, MATERIAL_SESSIONS, held.file));
      expect(body).toEqual(readFileSync(join(one, MATERIAL_SESSIONS, held.file)));
      expect(sha256(body)).toBe(held.sourceDigest);
    }
  },
  TIMEOUT,
);

test(
  "a kept stream that does not digest to what it was kept as refuses the scope",
  async () => {
    const cacheDir = await directory("cache");
    const first = await run([ROLLOUT], {}, { cacheDir });
    expect(first.receipt.closure).toBe("completed");

    // One reading, corrupted in place at its own length — the one failure the observation
    // cannot see, because size and modification time are the capture's and not the cache's.
    const labels = join(cacheDir, "archive", readdirSync(join(cacheDir, "archive"))[0]!, "labels");
    const slot = join(labels, readdirSync(labels)[0]!);
    const stream = join(
      slot,
      readdirSync(slot).find((name) => name.endsWith(".records"))!,
    );
    writeFileSync(stream, "x".repeat(statSync(stream).size));

    const corrupt = await run([ROLLOUT], {}, { cacheDir });
    expect(corrupt.receipt.closure).toBe("failed");
    expect(corrupt.receipt.reason).toContain(ROLLOUT);
    expect(corrupt.receipt.reason).toContain("does not digest to what it was kept as");
    expect(corrupt.receipt.preparation).toBeUndefined();
    expect(corrupt.rows).toEqual([]);

    // The entry is gone, so the next preparation fetches the capture and is the scope the first
    // one was: a corrupt cache costs one refusal, never a machine that can no longer prepare.
    const after = await run([ROLLOUT], {}, { cacheDir });
    expect(after.receipt.counts).toMatchObject({ fetched: 1, reused: 0 });
    expect(idOf(after.receipt)).toBe(idOf(first.receipt));
  },
  TIMEOUT,
);

// ----------------------------------------------------------------------------- refusals

test(
  "a size mismatch refuses capture_changed",
  async () => {
    const [group] = offer([ROLLOUT]);
    const session = group!.sessions[0]!;
    for (const size of [session.size + 1, session.size - 1]) {
      const material = await directory("material");
      const { receipt, rows } = await run(
        [],
        { captures: [{ ...group!, sessions: [{ ...session, size }] }] },
        { material: materialSink(material) },
      );
      expect(receipt.closure).toBe("failed");
      expect(receipt.reason).toStartWith("capture_changed: ");
      expect(receipt.reason).toContain(ROLLOUT);
      expect(receipt.counts["fetched"]).toBe(1);
      expect(receipt.preparation).toBeUndefined();
      expect(receipt.material).toBeUndefined();
      expect(existsSync(join(material, MATERIAL_INDEX))).toBe(false);
      expect(rows).toEqual([]);
    }
  },
  TIMEOUT,
);

test(
  "a missing path refuses capture_missing",
  async () => {
    const [group] = offer([OMP]);
    const session = group!.sessions[0]!;
    const absent = session.path.replace("01a0.jsonl", "09z9.jsonl");
    const missingPath = await run([], {
      captures: [{ ...group!, sessions: [{ ...session, path: absent }] }],
    });
    expect(missingPath.receipt.closure).toBe("failed");
    expect(missingPath.receipt.reason).toBe(
      `capture_missing: snapshot ${dev.id} holds no file at ${absent}`,
    );
    expect(missingPath.rows).toEqual([]);

    // A snapshot the archive does not hold is refused before any capture of it is fetched.
    const unknown = "f".repeat(64);
    const missingSnapshot = await run([], { captures: [{ ...group!, snapshotId: unknown }] });
    expect(missingSnapshot.receipt.reason).toBe(
      `capture_missing: snapshot ${unknown} is not in the archive`,
    );
    expect(missingSnapshot.receipt.counts["fetched"]).toBe(0);
    // And so is one the archive holds under another label than the one it was named with.
    const relabelled = await run([], { captures: [{ ...group!, label: WORKSTATION }] });
    expect(relabelled.receipt.reason).toBe(
      `capture_missing: snapshot ${dev.id} was taken under the label ${DEV}, not ${WORKSTATION}`,
    );
  },
  TIMEOUT,
);

test(
  "an unreachable archive refuses archive_unavailable",
  async () => {
    // No binding at all: the job was never handed the storage service.
    const unbound = await run(
      [OMP],
      {},
      {
        archive: async () =>
          openRepo(
            await resticConfig({ credentialFile: join(scratch, "no-binding.json"), env: fx.env }),
          ),
      },
    );
    expect(unbound.receipt.closure).toBe("failed");
    expect(unbound.receipt.reason).toStartWith("archive_unavailable: ");
    expect(unbound.receipt.reason).toContain("bound no atyrode.babel.restic service");
    expect(unbound.rows).toEqual([]);
    expect(unbound.receipt.material).toBeUndefined();

    // A binding whose repository does not answer: restic's own diagnosis is the reason.
    const gone = await run(
      [OMP],
      {},
      {
        archive: async () => openRepo({ ...fx.config, repository: join(scratch, "gone") }),
      },
    );
    expect(gone.receipt.closure).toBe("failed");
    expect(gone.receipt.reason).toStartWith("archive_unavailable: ");
    expect(gone.receipt.reason).toContain("repository does not exist");
    expect(gone.receipt.counts["fetched"]).toBe(0);

    // With a cache to file readings in, the binding is asked for its locator first.
    const cached = await run(
      [OMP],
      {},
      {
        cacheDir: await directory("cache"),
        repository: async () =>
          (await resticConfig({ credentialFile: join(scratch, "no-binding.json"), env: fx.env }))
            .repository,
      },
    );
    expect(cached.receipt.reason).toStartWith("archive_unavailable: ");
  },
  TIMEOUT,
);

test(
  "capacity preflight refuses before any fetch",
  async () => {
    const restic = await counted();
    let opened = 0;
    const archive = async () => {
      opened++;
      return openRepo(restic.config);
    };
    const need = [OMP, ROLLOUT].reduce((sum, selector) => sum + catalogued.get(selector)!.size, 0);
    const full = await run(
      [OMP, ROLLOUT],
      {},
      {
        archive,
        capacity: async () => ({ bytes: 1 << 20, free: 4096 }),
      },
    );
    expect(full.receipt.closure).toBe("failed");
    // Both figures: what the material needs, and what the lease has free.
    expect(full.receipt.reason).toBe(
      `material_storage_insufficient: 2 captures need ${String(need + 512 * 5 + (1 << 20))} ` +
        `bytes of material and the material lease has 4096 free`,
    );
    expect(full.receipt.outputCapacity).toEqual({ bytes: 1 << 20, free: 4096 });

    // Past what one material may hold at all, whatever the lease has free.
    const [group] = offer([OMP]);
    const huge = await run(
      [],
      {
        captures: [{ ...group!, sessions: [{ ...group!.sessions[0]!, size: MAX_MATERIAL_BYTES }] }],
      },
      { archive },
    );
    expect(huge.receipt.reason).toStartWith("material_bound: ");

    for (const refused of [full, huge]) {
      expect(refused.receipt.counts["fetched"]).toBe(0);
      expect(refused.rows).toEqual([]);
    }
    // NOTHING WAS FETCHED, AND THE ARCHIVE WAS NEVER EVEN OPENED.
    expect(opened).toBe(0);
    expect(await restic.calls()).toEqual([]);
  },
  TIMEOUT,
);

test(
  "a babelOwnLog capture is refused unless agentSessions is set",
  async () => {
    expect(catalogued.get(OWN)?.kind).toBe("agent");
    const refused = await run([OMP, OWN]);
    expect(refused.receipt.closure).toBe("failed");
    expect(refused.receipt.reason).toBe(
      `${OWN} is one of Babel's own runs' transcripts, which a preparation of the operator's ` +
        `work does not read`,
    );
    expect(refused.receipt.counts["fetched"]).toBe(0);
    expect(refused.rows).toEqual([]);

    // A preset that studies Babel asks on purpose, and the same captures are a scope.
    const asked = await run([OMP, OWN], { agentSessions: true });
    expect(asked.receipt.closure).toBe("completed");
    expect(asked.receipt.counts["selected"]).toBe(2);
    expect(asked.rows.find((row) => row.selector === OWN)?.kind).toBe("agent");
  },
  TIMEOUT,
);

// ------------------------------------------------------------------------- the preflight

test(
  "secrets in an archived transcript are redacted in the material",
  async () => {
    const material = await directory("material");
    const cacheDir = await directory("cache");
    const { receipt, rows } = await run(
      [LEAKY],
      {},
      { material: materialSink(material), cacheDir },
    );

    expect(receipt.closure).toBe("completed");
    const held = indexOf(material).sessions[0]!;
    const body = readFileSync(join(material, MATERIAL_SESSIONS, held.file), "utf8");
    expect(body).not.toContain(LEAKED_KEY);
    expect(body).toContain("[[babel-redacted:aws-access-key-id@");
    // Still the digest of exactly these bytes, which every citation of this session relies on.
    expect(held.sourceDigest).toBe(sha256(body));

    // THE RECEIPT SAYS IT WAS SCANNED, BY WHAT, AND WHAT IT FOUND — by class, never by value.
    expect(receipt.preflight).toMatchObject({
      schema: PREFLIGHT_SCHEMA,
      detectors: PREFLIGHT_DETECTORS,
      mode: "redact",
      redactions: 1,
      classes: [{ class: "aws-access-key-id", redactions: 1 }],
    });
    expect(receipt.counts["redacted"]).toBe(1);

    // THE VALUE IS NOWHERE BABEL WROTE: not the receipt, not the rows, not the kept reading.
    expect(JSON.stringify(receipt)).not.toContain(LEAKED_KEY);
    expect(JSON.stringify(rows)).not.toContain(LEAKED_KEY);
    const kept = await readdir(cacheDir, { recursive: true, withFileTypes: true });
    expect(kept.filter((entry) => entry.isFile()).length).toBeGreaterThan(0);
    for (const entry of kept.filter((candidate) => candidate.isFile())) {
      const bytes = await readFile(join(entry.parentPath, entry.name));
      expect(bytes.includes(LEAKED_KEY)).toBe(false);
    }

    // The locator resolves against the capture's own bytes, fetched by a job holding the
    // archive binding, and names the digest of the bytes the preparation read.
    const site = receipt.preflight?.sites[0];
    if (site === undefined) throw new Error("expected the credential's locator");
    const origin = held.origin!;
    const resolved = await resolveRedaction(
      (async function* () {
        yield await fx.repo.dump(origin.snapshotId, origin.path);
      })(),
      site,
    );
    expect(resolved).toEqual({ value: LEAKED_KEY, captureDigest: held.captureDigest });
  },
  TIMEOUT,
);

test(
  "a refused preparation names what it found by class, seals nothing, and quotes no value",
  async () => {
    const material = await directory("material");
    const { receipt, rows } = await run(
      [LEAKY],
      { preflight: "refuse" },
      { material: materialSink(material) },
    );
    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).toBe(
      `secret preflight refused ${LEAKY}: aws-access-key-id (1); values are never named`,
    );
    expect(receipt.material).toBeUndefined();
    expect(rows).toEqual([]);
    // No index, so nothing is bound into a session's sandbox — and what the refused pass did
    // write into the lease is the redacted stream, not the capture.
    expect(existsSync(join(material, MATERIAL_INDEX))).toBe(false);
    expect(
      readFileSync(join(material, MATERIAL_SESSIONS, materialFile(0, LEAKY)), "utf8"),
    ).not.toContain(LEAKED_KEY);
    expect(receipt.preflight?.classes).toEqual([{ class: "aws-access-key-id", redactions: 1 }]);
  },
  TIMEOUT,
);

test(
  "an unscanned preparation says so, and a redacting one never reuses its stream",
  async () => {
    const cacheDir = await directory("cache");
    const raw = await directory("material");
    const unscanned = await run(
      [LEAKY],
      { preflight: "off" },
      { material: materialSink(raw), cacheDir },
    );
    expect(unscanned.receipt.closure).toBe("completed");
    // Zero records READ is what distinguishes this from a scan that found nothing.
    expect(unscanned.receipt.preflight).toMatchObject({ mode: "off", records: 0, redactions: 0 });
    const held = unscanned.receipt.material?.sessions[0];
    expect(readFileSync(join(raw, MATERIAL_SESSIONS, held!.file), "utf8")).toContain(LEAKED_KEY);

    // The kept stream is the stream that was SEALED, so under `off` it is the raw record:
    // serving it to a preparation that redacts would be the disclosure #339 exists to prevent.
    const scanned = await directory("material");
    const redacted = await run([LEAKY], {}, { material: materialSink(scanned), cacheDir });
    expect(redacted.receipt.counts).toMatchObject({ reused: 0, fetched: 1, redacted: 1 });
    const body = readFileSync(
      join(scanned, MATERIAL_SESSIONS, redacted.receipt.material!.sessions[0]!.file),
      "utf8",
    );
    expect(body).not.toContain(LEAKED_KEY);
  },
  TIMEOUT,
);

test(
  "a corpus with nothing to redact prepares to the same identity scanned or not",
  async () => {
    const scanned = await run([OMP, CLAUDE]);
    const unscanned = await run([OMP, CLAUDE], { preflight: "off" });
    expect(idOf(scanned.receipt)).toBe(idOf(unscanned.receipt));
    expect(scanned.receipt.preflight?.records).toBeGreaterThan(0);
    expect(scanned.receipt.preflight?.redactions).toBe(0);
    expect(unscanned.receipt.preflight?.records).toBe(0);
  },
  TIMEOUT,
);

test(
  "a preparation offered nothing is skipped, and seals no material",
  async () => {
    const material = await directory("material");
    const { receipt, rows } = await run([], {}, { material: materialSink(material) });
    expect(receipt.closure).toBe("skipped");
    expect(receipt.reason).toBe("no capture was offered to prepare");
    expect(receipt.preparation).toBeUndefined();
    expect(receipt.material).toBeUndefined();
    expect(rows).toEqual([]);
    expect(existsSync(join(material, MATERIAL_INDEX))).toBe(false);
  },
  TIMEOUT,
);
