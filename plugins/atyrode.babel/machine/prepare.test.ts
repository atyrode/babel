/*
  The preparation's identity and the two digests behind it, over real files on disk.

  What is being pinned is a contract later phases lean on: `explore --preparation <id>` names a
  corpus, so the same corpus must be the same id however it was discovered or when, and a
  corpus that changed by one byte must not be.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputFile, OutputSink } from "./output.ts";
import {
  PREPARATION_SCHEMA,
  PrepareInputSchema,
  type PreparationEntry,
  digests,
  newPreparation,
  prepare,
  type PrepareDeps,
} from "./prepare.ts";

const MACHINE = "test-machine-01";

let root = "";
let sessions: SessionRef[] = [];

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

const entry = (sourceId: string, capture: string, source: string): PreparationEntry => ({
  host: MACHINE,
  harness: "omp",
  sourceId,
  captureDigest: `sha256:${capture.repeat(64)}`,
  sourceDigest: `sha256:${source.repeat(64)}`,
});

function ref(harness: SessionRef["harness"], sourceId: string): SessionRef {
  return {
    harness,
    sourceId,
    selector: `${harness}/${sourceId}`,
    primaryPath: join(root, harness, `${sourceId}.jsonl`),
  };
}

function deps(over: readonly SessionRef[] = sessions): PrepareDeps {
  return { discover: async () => over, digests };
}

async function run(
  input: Partial<{ selectors: string[] }> = {},
  over: readonly SessionRef[] = sessions,
): Promise<{ receipt: Receipt; rows: readonly Record<string, unknown>[] }> {
  const recorder = new Recorder();
  const receipt = await prepare(
    PrepareInputSchema.parse({ machineId: MACHINE, ...input }),
    recorder,
    deps(over),
  );
  expect(recorder.written).toEqual(receipt);
  return {
    receipt,
    rows: (recorder.files["sessions"] ?? []) as readonly Record<string, unknown>[],
  };
}

beforeAll(() => {
  root = mkdtempSync(join(tmpdir(), "babel-prepare-"));
  mkdirSync(join(root, "omp"), { recursive: true });
  mkdirSync(join(root, "codex"), { recursive: true });
  writeFileSync(join(root, "omp", "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n');
  writeFileSync(join(root, "omp", "d4e5f6.jsonl"), '{"type":"user","text":"second"}\n');
  writeFileSync(join(root, "codex", "0192ab.jsonl"), '{"type":"message","text":"third"}\n');
  sessions = [ref("omp", "a1b2c3"), ref("omp", "d4e5f6"), ref("codex", "0192ab")];
});

afterAll(() => {
  if (root !== "") rmSync(root, { recursive: true, force: true });
});

test("a preparation id is the selection's content, not the moment it was fixed", () => {
  const selection = [entry("a1b2c3", "a", "b"), entry("d4e5f6", "c", "d")];
  const first = newPreparation("2026-09-12T10:00:00.000Z", selection);
  const later = newPreparation("2026-09-13T23:59:59.999Z", selection);

  expect(first.id).toMatch(/^prep-[0-9a-f]{64}$/);
  expect(later.id).toBe(first.id);
  expect(later.preparedAt).not.toBe(first.preparedAt);
  expect(first.schema).toBe(PREPARATION_SCHEMA);

  // Discovery order is not content: the record is canonically ordered before it is derived.
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
    newPreparation("2026-09-12T10:00:00.000Z", [entry("a1b2c3", "e", "b"), entry("d4e5f6", "c", "d")]).id,
    // the same bytes, normalized differently: our reading of the corpus changed
    newPreparation("2026-09-12T10:00:00.000Z", [entry("a1b2c3", "a", "e"), entry("d4e5f6", "c", "d")]).id,
    // a narrower scope over unchanged sessions
    newPreparation("2026-09-12T10:00:00.000Z", [entry("a1b2c3", "a", "b")]).id,
    // the same sessions, held by another machine
    newPreparation("2026-09-12T10:00:00.000Z", base.map((held) => ({ ...held, host: "other-machine" }))).id,
  ]);
  expect(ids.size).toBe(5);
});

test("a scope that names nothing, or one session twice, is refused", () => {
  expect(() => newPreparation("2026-09-12T10:00:00.000Z", [])).toThrow(/selection is empty/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [entry("a1b2c3", "a", "b"), entry("a1b2c3", "a", "b")]),
  ).toThrow(/twice/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [{ ...entry("a1b2c3", "a", "b"), sourceId: "" }]),
  ).toThrow(/names no session/);
  expect(() =>
    newPreparation("2026-09-12T10:00:00.000Z", [{ ...entry("a1b2c3", "a", "b"), captureDigest: "" }]),
  ).toThrow(/no capture digest/);
});

test("the bytes of a log and the records in it are digested apart", async () => {
  const path = join(root, "omp", "digest-me.jsonl");
  const session = ref("omp", "digest-me");
  writeFileSync(path, '{"type":"user","text":"hello"}\n{"type":"agent","text":"hi"}\n');
  const original = await digests(session);
  expect(original.captureDigest).toMatch(/^sha256:[0-9a-f]{64}$/);
  expect(original.sourceDigest).toMatch(/^sha256:[0-9a-f]{64}$/);
  expect(original.bytes).toBe(60);

  // The same records, spelled differently: a reflowed, reordered log is the same corpus.
  writeFileSync(path, '{ "text":"hello" , "type":"user"}\n{"text": "hi", "type":"agent"}\n');
  const respelled = await digests(session);
  expect(respelled.captureDigest).not.toBe(original.captureDigest);
  expect(respelled.sourceDigest).toBe(original.sourceDigest);
  expect(respelled.bytes).not.toBe(original.bytes);

  // A record that says something else is not the same corpus.
  writeFileSync(path, '{"type":"user","text":"hello"}\n{"type":"agent","text":"bye"}\n');
  const changed = await digests(session);
  expect(changed.sourceDigest).not.toBe(original.sourceDigest);

  // A torn final line is evidence that was seen, not evidence dropped.
  writeFileSync(path, '{"type":"user","text":"hello"}\n{"type":"agent","te');
  const torn = await digests(session);
  writeFileSync(path, '{"type":"user","text":"hello"}\n');
  const truncated = await digests(session);
  expect(torn.sourceDigest).not.toBe(truncated.sourceDigest);
  rmSync(path);
});

test("the operation fixes a scope over every session it discovered", async () => {
  const { receipt, rows } = await run();

  expect(receipt.kind).toBe("prepare");
  expect(receipt.closure).toBe("completed");
  expect(receipt.runId).toMatch(/^run_/);
  expect(receipt.counts["discovered"]).toBe(3);
  expect(receipt.counts["selected"]).toBe(3);
  expect(receipt.counts["bytes"]).toBeGreaterThan(0);

  const preparation = receipt.preparation as
    | { id: string; selection: readonly PreparationEntry[] }
    | undefined;
  expect(preparation?.id).toMatch(/^prep-[0-9a-f]{64}$/);
  expect(preparation?.selection.map((held) => `${held.harness}/${held.sourceId}`)).toEqual([
    "codex/0192ab",
    "omp/a1b2c3",
    "omp/d4e5f6",
  ]);
  for (const held of preparation?.selection ?? []) {
    expect(held.host).toBe(MACHINE);
    expect(held.captureDigest).toMatch(/^sha256:[0-9a-f]{64}$/);
  }

  // Every scoped session is registered, with the capture the scope fixed.
  expect(rows.length).toBe(3);
  expect(rows.map((row) => row["selector"]).sort()).toEqual([
    "codex/0192ab",
    "omp/a1b2c3",
    "omp/d4e5f6",
  ]);
  const first = rows.find((row) => row["selector"] === "omp/a1b2c3");
  expect(first?.["host"]).toBe(MACHINE);
  expect(first?.["harness"]).toBe("omp");
  expect(first?.["source_id"]).toBe("a1b2c3");
  expect(first?.["content_digest"]).toBe(
    preparation?.selection.find((held) => held.sourceId === "a1b2c3")?.captureDigest,
  );
  expect(first?.["size"]).toBe(31);
  expect(typeof first?.["seen_at"]).toBe("string");
});

test("re-preparing an unchanged corpus is the same scope; one changed session is not", async () => {
  const first = await run();
  const again = await run();
  expect(idOf(again.receipt)).toBe(idOf(first.receipt));

  const path = join(root, "omp", "d4e5f6.jsonl");
  writeFileSync(path, '{"type":"user","text":"second"}\n{"type":"agent","text":"more"}\n');
  const after = await run();
  expect(idOf(after.receipt)).not.toBe(idOf(first.receipt));

  writeFileSync(path, '{"type":"user","text":"second"}\n');
  const restored = await run();
  expect(idOf(restored.receipt)).toBe(idOf(first.receipt));
});

test("a selector is resolved by suffix, and an unmatched one refuses the scope", async () => {
  const chosen = await run({ selectors: ["a1b2c3", "codex/0192ab"] });
  expect(chosen.receipt.closure).toBe("completed");
  expect(chosen.receipt.counts["selected"]).toBe(2);
  expect(chosen.rows.map((row) => row["selector"]).sort()).toEqual(["codex/0192ab", "omp/a1b2c3"]);

  const missed = await run({ selectors: ["omp/nothing-here"] });
  expect(missed.receipt.closure).toBe("failed");
  expect(missed.receipt.reason).toContain("omp/nothing-here");
  expect(missed.receipt.preparation).toBeUndefined();
  expect(missed.rows).toEqual([]);
});

test("an ambiguous selector is refused with its candidates, never guessed", async () => {
  const twins = [ref("omp", "shared-tail"), ref("codex", "shared-tail")];
  writeFileSync(twins[0]?.primaryPath ?? "", '{"type":"user"}\n');
  writeFileSync(twins[1]?.primaryPath ?? "", '{"type":"user"}\n');
  const { receipt, rows } = await run({ selectors: ["shared-tail"] }, twins);

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("ambiguous");
  expect(receipt.reason).toContain("omp/shared-tail");
  expect(receipt.reason).toContain("codex/shared-tail");
  expect(rows).toEqual([]);
});

test("a machine with nothing to prepare is skipped, not an empty scope", async () => {
  const { receipt, rows } = await run({}, []);

  expect(receipt.closure).toBe("skipped");
  expect(receipt.reason).toBe("no session on this machine to prepare");
  expect(receipt.preparation).toBeUndefined();
  expect(rows).toEqual([]);
});

function idOf(receipt: Receipt): string {
  const preparation = receipt.preparation as { id?: string } | undefined;
  return preparation?.id ?? "";
}
