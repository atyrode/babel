import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  JOB_OUTPUT_FILES,
  MATERIAL_INDEX,
  MaterialIndexSchema,
  SessionRowSchema,
  type PrepareInput,
  type Receipt,
} from "../../contract.ts";
import { type Invocation, parseArgv } from "../main.ts";
import type { Snapshot } from "../restic.ts";
import { writeOmpSession } from "./fixtures.ts";
import { syntheticArchive, type SyntheticArchive } from "./restic-fixture.ts";

/*
  The machine half as a job actually runs it: argv in, output files and a receipt on disk, the
  receipt on stdout, a nonzero exit and a failed receipt when the operation throws. The archive
  is a synthetic one reached through the storage service binding a job is handed, so the
  dispatcher's own wiring — which binding, which lease, which cache — is what is exercised.
*/

const MAIN = join(import.meta.dir, "..", "main.ts");
const LABEL = "dev-01";
const SOURCE_ID = "-home-alex-babel/2026-09-06T12-00-00-000Z_cccc";
let fx: SyntheticArchive;
let session = "";
let snapshot: Snapshot;

interface Run {
  code: number;
  stdout: string;
  stderr: string;
  outputDir: string;
  materialDir: string;
}

async function runMachine(args: readonly string[], env: Record<string, string> = {}): Promise<Run> {
  const outputDir = await mkdtemp(join(tmpdir(), "babel-out-"));
  const materialDir = await mkdtemp(join(tmpdir(), "babel-material-"));
  const substitute = (arg: string): string =>
    arg === "%OUT%" ? outputDir : arg === "%MATERIAL%" ? materialDir : arg;
  const child = Bun.spawn(["bun", MAIN, ...args.map(substitute)], {
    env: {
      PATH: process.env["PATH"] ?? "",
      ...fx.env,
      CODEX_HOME: join(fx.home, "no-codex"),
      BABEL_RESTIC_BINDING: fx.credentialFile,
      ...env,
      BABEL_JOB_OUTPUT_DIR: substitute(env["BABEL_JOB_OUTPUT_DIR"] ?? ""),
    },
    stdout: "pipe",
    stderr: "pipe",
  });
  // Asynchronous, because the storage service answers from this very process.
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, stdout, stderr, outputDir, materialDir };
}

async function cleanUp(run: Run): Promise<void> {
  await rm(run.outputDir, { recursive: true, force: true });
  await rm(run.materialDir, { recursive: true, force: true });
}

beforeAll(async () => {
  fx = await syntheticArchive();
  session = await writeOmpSession(fx.sessionRoot("omp"), {
    project: "-home-alex-babel",
    stem: "2026-09-06T12-00-00-000Z_cccc",
    title: "Dispatching an operation",
    cwd: join(fx.home, "nowhere"),
    turns: [{ usage: { totalTokens: 42, cost: 0.01 } }],
  });
  snapshot = await fx.snapshot(LABEL, [fx.sessionRoot("omp")]);
}, 60_000);

afterAll(async () => {
  await fx.close();
});

test("a catalog run through argv writes its outputs and prints its receipt", async () => {
  const input = join(fx.home, "catalog-input.json");
  await Bun.write(input, JSON.stringify({ machineId: "machine-1", runId: "run_argv" }));

  const run = await runMachine(["catalog", "--input", input, "--out", "%OUT%"]);
  expect(run.stderr).toBe("");
  expect(run.code).toBe(0);

  const printed = JSON.parse(run.stdout) as Receipt;
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(printed).toEqual(receipt);
  expect(receipt.runId).toBe("run_argv");
  expect(receipt.kind).toBe("catalog");
  expect(receipt.closure).toBe("completed");
  expect(receipt.counts["captures"]).toBe(1);
  // The lease it wrote into is the one it measured.
  expect(receipt.outputCapacity?.bytes).toBeGreaterThan(0);

  const rows = (
    (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.sessions)).json()) as unknown[]
  ).map((row) => SessionRowSchema.parse(row));
  expect(rows.map((row) => [row.selector, row.archive_label, row.snapshot_id])).toEqual([
    [`omp/${SOURCE_ID}`, LABEL, snapshot.id],
  ]);
  // Nothing else was produced: an absent file means this operation produced none.
  expect(await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.records)).exists()).toBe(false);
  await cleanUp(run);
});

test("a prepare run through argv reads its capture from the archive and seals the material", async () => {
  // The hub names the capture; the dispatcher hands the preparation the job's storage binding
  // and its material lease, and nothing on this machine is read in its place.
  const input = join(fx.home, "prepare-input.json");
  const named: PrepareInput = {
    runId: "run_prepare",
    machineId: "machine-1",
    captures: [
      {
        snapshotId: snapshot.id,
        label: LABEL,
        sessions: [
          {
            harness: "omp",
            sourceId: SOURCE_ID,
            path: session,
            size: Bun.file(session).size,
            modifiedAt: Bun.file(session).lastModified,
          },
        ],
      },
    ],
    agentSessions: false,
    preflight: "redact",
  };
  await Bun.write(input, JSON.stringify(named));

  const run = await runMachine([
    "prepare",
    "--input",
    input,
    "--out",
    "%OUT%",
    "--material",
    "%MATERIAL%",
  ]);
  expect(run.stderr).toBe("");
  expect(run.code).toBe(0);
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.kind).toBe("prepare");
  expect(receipt.closure).toBe("completed");
  expect(receipt.counts["fetched"]).toBe(1);
  const index = MaterialIndexSchema.parse(
    await Bun.file(join(run.materialDir, MATERIAL_INDEX)).json(),
  );
  expect(index.sessions.map((entry) => entry.origin)).toEqual([
    { label: LABEL, snapshotId: snapshot.id, path: session },
  ]);
  await cleanUp(run);
});

test("the job bindings can arrive as environment variables instead of flags", async () => {
  const input = join(fx.home, "env-input.json");
  await Bun.write(input, JSON.stringify({ machineId: "dev-env", runId: "run_env" }));

  const run = await runMachine(["catalog"], {
    BABEL_JOB_INPUT: input,
    BABEL_JOB_OUTPUT_DIR: "%OUT%",
  });
  expect(run.code).toBe(0);
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.runId).toBe("run_env");
  expect(receipt.machineId).toBe("dev-env");
  await cleanUp(run);
});

test("an operation that throws still leaves a receipt, and exits nonzero", async () => {
  const input = join(fx.home, "bad-input.json");
  // No machineId: a receipt could not name the machine it came from, so the schema refuses it.
  await Bun.write(input, JSON.stringify({ runId: "run_broken" }));

  const run = await runMachine(["catalog", "--input", input, "--out", "%OUT%"]);
  expect(run.code).toBe(1);
  expect(run.stdout).toBe("");

  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.closure).toBe("failed");
  expect(receipt.runId).toBe("run_broken");
  expect(receipt.kind).toBe("catalog");
  expect(receipt.reason).toContain("machineId");
  expect(receipt.counts).toEqual({});
  expect(await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.sessions)).exists()).toBe(false);
  await cleanUp(run);
});

test("an input document that is not readable JSON fails as a run, with a receipt", async () => {
  const run = await runMachine([
    "catalog",
    "--input",
    join(fx.home, "no-such-input.json"),
    "--out",
    "%OUT%",
  ]);
  expect(run.code).toBe(1);
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.closure).toBe("failed");
  expect(receipt.kind).toBe("catalog");
  // Nothing named the run, so the machine half minted an id the hub can correlate.
  expect(receipt.runId).toMatch(/^run_[0-9a-f]{32}$/u);
  expect(receipt.machineId).toBe("");
  await cleanUp(run);
});

test("argv is read the same way under bun and as a compiled binary", () => {
  const expected: Invocation = {
    operation: "catalog",
    inputPath: "/in.json",
    outputDir: "/out",
    materialDir: "",
  };
  expect(parseArgv(["catalog", "--input", "/in.json", "--out", "/out"])).toEqual(expected);
  expect(parseArgv(["--input", "/in.json", "catalog", "--out", "/out"])).toEqual(expected);
});

test("the material lease is argv's, and only prepare's manifest declares one", () => {
  // `prepare` seals a second output the session's job binds read-only at `/inputs/material`
  // (#279); an invocation that names no `--material` prepares a selection and seals nothing,
  // which is what a hand-run on a machine does.
  expect(
    parseArgv(["prepare", "--input", "/in.json", "--out", "/out", "--material", "/mat"]),
  ).toEqual({
    operation: "prepare",
    inputPath: "/in.json",
    outputDir: "/out",
    materialDir: "/mat",
  });
  expect(() => parseArgv(["prepare", "--input", "/in.json", "--out", "/o", "--material"])).toThrow(
    "--material needs a path",
  );
});

test("argv that names no operation is a usage failure, not a default", () => {
  expect(() => parseArgv(["--input", "/in.json", "--out", "/out"])).toThrow("no operation named");
  expect(() => parseArgv(["catlog", "--input", "/in.json", "--out", "/out"])).toThrow(
    "unknown operation catlog",
  );
  // A retired operation is not a verb of this binary any more (#453).
  expect(() => parseArgv(["scan", "--input", "/in.json", "--out", "/out"])).toThrow(
    "unknown operation scan",
  );
  expect(() => parseArgv(["catalog", "extra", "--input", "/in.json", "--out", "/out"])).toThrow(
    "unexpected argument extra",
  );
  expect(() => parseArgv(["catalog", "--out", "/out", "--verbose"])).toThrow(
    "unknown flag --verbose",
  );
  expect(() => parseArgv(["catalog", "--input"])).toThrow("--input needs a path");
});
