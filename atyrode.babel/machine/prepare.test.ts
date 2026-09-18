/*
  The preparation's identity and the two digests behind it, over real files on disk.

  What is being pinned is a contract later phases lean on: `explore --preparation <id>` names a
  corpus, so the same corpus must be the same id however it was discovered or when, and a
  corpus that changed by one byte must not be.

  Every fixture is written and then BACKDATED (`settle`), because a log written a moment ago is
  one a preparation refuses to read at all (#262): the identity under test is a settled corpus's,
  and a moving file has none. The exclusion itself is the subject of the last three tests.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import {
  appendFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  MATERIAL_INDEX,
  MATERIAL_SCHEMA,
  MATERIAL_SESSIONS,
  MaterialIndexSchema,
  materialFile,
  type Receipt,
} from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import { materialSink, type OutputFile, type OutputSink } from "./output.ts";
import {
  PREPARATION_SCHEMA,
  PrepareInputSchema,
  type PreparationEntry,
  digests,
  modifiedAt,
  newPreparation,
  prepare,
  type PrepareDeps,
} from "./prepare.ts";

/** An hour ago: past `LIVE_GRACE_MS`, so this file is a settled session and not a moving one. */
function settle(path: string, contents?: string): string {
  if (contents !== undefined) writeFileSync(path, contents);
  const at = new Date(Date.now() - 60 * 60 * 1000);
  utimesSync(path, at, at);
  return path;
}

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
  return { discover: async () => over, digests, modifiedAt };
}

async function run(
  input: Partial<{ selectors: string[]; agentSessions: boolean }> = {},
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
  settle(join(root, "omp", "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n');
  settle(join(root, "omp", "d4e5f6.jsonl"), '{"type":"user","text":"second"}\n');
  settle(join(root, "codex", "0192ab.jsonl"), '{"type":"message","text":"third"}\n');
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
  settle(path, '{"type":"user","text":"second"}\n{"type":"agent","text":"more"}\n');
  const after = await run();
  expect(idOf(after.receipt)).not.toBe(idOf(first.receipt));

  settle(path, '{"type":"user","text":"second"}\n');
  const restored = await run();
  expect(idOf(restored.receipt)).toBe(idOf(first.receipt));
});

test("a session being appended to is left out, so the scope's id does not move with it", async () => {
  // The 2026-09-13 failure, reproduced: one session appended between preparations while the
  // rest of the corpus sits still. Ten preparations, one id — and the moving file in none of
  // them, which is why there is one id rather than ten.
  const moving = join(root, "omp", "still-writing.jsonl");
  writeFileSync(moving, '{"type":"user","text":"0"}\n');
  const over = [...sessions, ref("omp", "still-writing")];

  const ids = new Set<string>();
  for (let turn = 0; turn < 10; turn++) {
    const { receipt } = await run({}, over);
    ids.add(idOf(receipt));
    expect(receipt.counts["live"]).toBe(1);
    expect(receipt.counts["selected"]).toBe(3);
    appendFileSync(moving, `{"type":"agent","text":"${String(turn)}"}\n`);
  }

  expect(ids.size).toBe(1);
  expect([...ids][0]).toBe(idOf((await run()).receipt));
  rmSync(moving);
});

test("a scope holds none of Babel's own run transcripts unless it asks for them", async () => {
  const own = settle(join(root, "omp", "run-abc.babel.jsonl"), '{"type":"session","runId":"run-abc"}\n');
  const over = [...sessions, ref("omp", "run-abc.babel")];

  const ordinary = await run({}, over);
  expect(ordinary.receipt.counts["agent"]).toBe(1);
  expect(ordinary.receipt.counts["selected"]).toBe(3);
  expect(ordinary.rows.map((row) => row["selector"])).not.toContain("omp/run-abc.babel");

  // #270's preset, asking on purpose: the same corpus plus Babel's own.
  const studied = await run({ agentSessions: true }, over);
  expect(studied.receipt.counts["agent"]).toBe(0);
  expect(studied.receipt.counts["selected"]).toBe(4);
  expect(studied.rows.map((row) => row["selector"])).toContain("omp/run-abc.babel");
  expect(idOf(studied.receipt)).not.toBe(idOf(ordinary.receipt));
  rmSync(own);
});

test("a selector that names an excluded session is refused, never quietly dropped", async () => {
  const moving = ref("omp", "still-writing");
  writeFileSync(moving.primaryPath, '{"type":"user","text":"0"}\n');
  const own = ref("omp", "run-abc.babel");
  settle(own.primaryPath, '{"type":"session","runId":"run-abc"}\n');
  const over = [...sessions, moving, own];

  const live = await run({ selectors: ["omp/still-writing"] }, over);
  expect(live.receipt.closure).toBe("failed");
  expect(live.receipt.reason).toContain("omp/still-writing");
  expect(live.receipt.reason).toContain("still being appended");
  expect(live.receipt.preparation).toBeUndefined();
  expect(live.rows).toEqual([]);

  const babel = await run({ selectors: ["omp/run-abc.babel"] }, over);
  expect(babel.receipt.closure).toBe("failed");
  expect(babel.receipt.reason).toContain("Babel's own runs' transcripts");
  expect(babel.receipt.preparation).toBeUndefined();

  // Asked for on purpose, the same selector is a scope.
  const asked = await run({ selectors: ["omp/run-abc.babel"], agentSessions: true }, over);
  expect(asked.receipt.closure).toBe("completed");
  expect(asked.receipt.counts["selected"]).toBe(1);

  rmSync(moving.primaryPath);
  rmSync(own.primaryPath);
});

test("a corpus of nothing but moving and own sessions is skipped, and says which", async () => {
  const moving = ref("omp", "still-writing");
  writeFileSync(moving.primaryPath, '{"type":"user","text":"0"}\n');
  const own = ref("omp", "run-abc.babel");
  settle(own.primaryPath, '{"type":"session","runId":"run-abc"}\n');

  const { receipt, rows } = await run({}, [moving, own]);
  expect(receipt.closure).toBe("skipped");
  expect(receipt.reason).toContain("1 still being written");
  expect(receipt.reason).toContain("1 Babel's own runs'");
  expect(receipt.preparation).toBeUndefined();
  expect(rows).toEqual([]);

  rmSync(moving.primaryPath);
  rmSync(own.primaryPath);
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

/*
  THE SEALED MATERIAL (#279).

  A Code session reads `/inputs/material`, and every later reader of a claim recovers its bytes
  from there. The layout is fixed in `contract.ts` because two things far apart depend on it
  being the same: this half writes it and `server/engine/prompts.ts` describes it. What the
  tests below pin is the three promises the prompt makes about it — an index at the root, one
  file per session named the way the index names it, one canonical JSON record per line — and
  the one that makes a citation checkable: the digest in the index is taken over exactly the
  bytes of that file.
*/
test("the material is an index plus one canonical record stream per session", async () => {
  const dir = mkdtempSync(join(tmpdir(), "babel-material-"));
  try {
    const recorder = new Recorder();
    const receipt = await prepare(
      PrepareInputSchema.parse({ machineId: MACHINE }),
      recorder,
      { ...deps(), material: materialSink(dir) },
    );

    const index = MaterialIndexSchema.parse(
      JSON.parse(readFileSync(join(dir, MATERIAL_INDEX), "utf8")),
    );
    expect(index.schema).toBe(MATERIAL_SCHEMA);
    expect(index.preparationId).toBe(idOf(receipt));
    expect(index.machineId).toBe(MACHINE);
    // The receipt carries the SAME document, so the hub can verify a citation from one row
    // rather than pulling the sealed archive back to read the front of it.
    expect(receipt.material).toEqual(index);

    // The ordinal goes in front of the name so two selectors differing only in a replaced
    // character cannot become one file — a material where one session silently overwrote
    // another is worse than no material at all.
    expect(index.sessions.map((held) => held.file)).toEqual(
      index.sessions.map((held, ordinal) => materialFile(ordinal, held.selector)),
    );

    for (const held of index.sessions) {
      const body = readFileSync(join(dir, MATERIAL_SESSIONS, held.file), "utf8");
      const lines = body.split("\n").filter((line) => line !== "");
      // ONE CANONICAL RECORD PER LINE, in the order the harness wrote them: re-serialised,
      // so a reflowed log and a tidy one are byte-identical here and cite the same digest.
      expect(lines).toHaveLength(held.records);
      for (const line of lines) {
        expect(JSON.stringify(JSON.parse(line) as unknown)).toBe(line);
      }
      // AND THE DIGEST IS OVER EXACTLY THOSE BYTES. This is the whole reason a claim may cite
      // a line of this file: the index's `sourceDigest` is what the model copies into its
      // locator, and it must be a digest of what the model actually read.
      const hashed = new Bun.CryptoHasher("sha256").update(body).digest("hex");
      expect(held.sourceDigest).toBe(`sha256:${hashed}`);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a preparation that selected nothing seals no material rather than an empty one", async () => {
  const dir = mkdtempSync(join(tmpdir(), "babel-material-"));
  try {
    const receipt = await prepare(
      PrepareInputSchema.parse({ machineId: MACHINE }),
      new Recorder(),
      { ...deps([]), material: materialSink(dir) },
    );

    expect(receipt.closure).toBe("skipped");
    expect(receipt.material).toBeUndefined();
    // The lease is left exactly as it was found: a bound material nobody chose the contents of
    // is a directory a session would read and call evidence.
    expect(existsSync(join(dir, MATERIAL_INDEX))).toBe(false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

function idOf(receipt: Receipt): string {
  const preparation = receipt.preparation as { id?: string } | undefined;
  return preparation?.id ?? "";
}
