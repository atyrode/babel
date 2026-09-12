import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { JOB_OUTPUT_FILES, type Receipt } from "../../contract.ts";
import { type Invocation, parseArgv } from "../main.ts";
import type { SessionCatalogRow } from "../scan.ts";
import { writeOmpSession } from "./fixtures.ts";

/*
  The machine half as a job actually runs it: argv in, output files and a receipt on disk, the
  receipt on stdout, a nonzero exit and a failed receipt when the operation throws.
*/

const MAIN = join(import.meta.dir, "..", "main.ts");
let home = "";

interface Run {
  code: number;
  stdout: string;
  stderr: string;
  outputDir: string;
}

async function runMachine(args: readonly string[], env: Record<string, string> = {}): Promise<Run> {
  const outputDir = await mkdtemp(join(tmpdir(), "babel-out-"));
  const child = Bun.spawn(["bun", MAIN, ...args.map((arg) => (arg === "%OUT%" ? outputDir : arg))], {
    env: { ...process.env, HOME: home, CODEX_HOME: join(home, "no-codex"), ...env, BABEL_JOB_OUTPUT_DIR: env["BABEL_JOB_OUTPUT_DIR"] === "%OUT%" ? outputDir : (env["BABEL_JOB_OUTPUT_DIR"] ?? "") },
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, stdout, stderr, outputDir };
}

beforeAll(async () => {
  home = await mkdtemp(join(tmpdir(), "babel-main-"));
  await writeOmpSession(join(home, ".omp", "agent", "sessions"), {
    project: "-home-alex-babel",
    stem: "2026-09-06T12-00-00-000Z_cccc",
    title: "Dispatching an operation",
    cwd: join(home, "nowhere"),
    turns: [{ usage: { totalTokens: 42, cost: 0.01 } }],
  });
});

afterAll(async () => {
  await rm(home, { recursive: true, force: true });
});

test("a scan run through argv writes its outputs and prints its receipt", async () => {
  const input = join(home, "scan-input.json");
  await Bun.write(input, JSON.stringify({ machineId: "dev-01", runId: "run_argv" }));

  const run = await runMachine(["scan", "--input", input, "--out", "%OUT%"]);
  expect(run.stderr).toBe("");
  expect(run.code).toBe(0);

  const printed = JSON.parse(run.stdout) as Receipt;
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(printed).toEqual(receipt);
  expect(receipt.runId).toBe("run_argv");
  expect(receipt.kind).toBe("scan");
  expect(receipt.closure).toBe("completed");
  expect(receipt.counts["sessions"]).toBe(1);

  const rows = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.sessions)).json()) as SessionCatalogRow[];
  expect(rows).toHaveLength(1);
  expect(rows[0]?.selector).toBe("omp/-home-alex-babel/2026-09-06T12-00-00-000Z_cccc");
  expect(rows[0]?.host).toBe("dev-01");
  // Nothing else was produced: an absent file means this operation produced none.
  expect(await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.records)).exists()).toBe(false);
  await rm(run.outputDir, { recursive: true, force: true });
});

test("the job bindings can arrive as environment variables instead of flags", async () => {
  const input = join(home, "env-input.json");
  await Bun.write(input, JSON.stringify({ machineId: "dev-env", runId: "run_env" }));

  const run = await runMachine(["scan"], { BABEL_JOB_INPUT: input, BABEL_JOB_OUTPUT_DIR: "%OUT%" });
  expect(run.code).toBe(0);
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.runId).toBe("run_env");
  expect(receipt.machineId).toBe("dev-env");
  await rm(run.outputDir, { recursive: true, force: true });
});

test("an operation that throws still leaves a receipt, and exits nonzero", async () => {
  const input = join(home, "bad-input.json");
  // No machineId: a row could not name the machine it came from, so the schema refuses it.
  await Bun.write(input, JSON.stringify({ runId: "run_broken" }));

  const run = await runMachine(["scan", "--input", input, "--out", "%OUT%"]);
  expect(run.code).toBe(1);
  expect(run.stdout).toBe("");

  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.closure).toBe("failed");
  expect(receipt.runId).toBe("run_broken");
  expect(receipt.kind).toBe("scan");
  expect(receipt.reason).toContain("machineId");
  expect(receipt.counts).toEqual({});
  expect(await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.sessions)).exists()).toBe(false);
  await rm(run.outputDir, { recursive: true, force: true });
});

test("an input document that is not readable JSON fails as a run, with a receipt", async () => {
  const run = await runMachine(["scan", "--input", join(home, "no-such-input.json"), "--out", "%OUT%"]);
  expect(run.code).toBe(1);
  const receipt = (await Bun.file(join(run.outputDir, JOB_OUTPUT_FILES.receipt)).json()) as Receipt;
  expect(receipt.closure).toBe("failed");
  expect(receipt.kind).toBe("scan");
  // Nothing named the run, so the machine half minted an id the hub can correlate.
  expect(receipt.runId).toMatch(/^run_[0-9a-f]{32}$/u);
  expect(receipt.machineId).toBe("");
  await rm(run.outputDir, { recursive: true, force: true });
});

test("argv is read the same way under bun and as a compiled binary", () => {
  const expected: Invocation = { operation: "scan", inputPath: "/in.json", outputDir: "/out" };
  expect(parseArgv(["scan", "--input", "/in.json", "--out", "/out"])).toEqual(expected);
  expect(parseArgv(["--input", "/in.json", "scan", "--out", "/out"])).toEqual(expected);
});

test("argv that names no operation is a usage failure, not a default", () => {
  expect(() => parseArgv(["--input", "/in.json", "--out", "/out"])).toThrow("no operation named");
  expect(() => parseArgv(["sacn", "--input", "/in.json", "--out", "/out"])).toThrow("unknown operation sacn");
  expect(() => parseArgv(["scan", "extra", "--input", "/in.json", "--out", "/out"])).toThrow("unexpected argument extra");
  expect(() => parseArgv(["scan", "--out", "/out", "--verbose"])).toThrow("unknown flag --verbose");
  expect(() => parseArgv(["scan", "--input"])).toThrow("--input needs a path");
});
