import { afterAll, beforeAll, expect, test } from "bun:test";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { Receipt } from "../../contract.ts";
import type { OutputFile, OutputSink } from "../output.ts";
import { type SessionCatalogRow, ScanInputSchema, scan } from "../scan.ts";
import { writeClaudeSession, writeCodexRollout, writeCodexState, writeOmpSession } from "./fixtures.ts";

/*
  The `scan` operation over a whole synthetic machine: three harnesses, a real git checkout as
  one session's workspace, and one session nothing can read.
*/

let home = "";
let workspace = "";
const originalHome = process.env["HOME"];
const originalCodexHome = process.env["CODEX_HOME"];

interface Written {
  files: Partial<Record<OutputFile, readonly unknown[]>>;
  receipt: Receipt | null;
}

function memorySink(): { sink: OutputSink; written: Written } {
  const written: Written = { files: {}, receipt: null };
  return {
    written,
    sink: {
      write(file, rows) {
        written.files[file] = rows;
        return Promise.resolve();
      },
      receipt(receipt) {
        written.receipt = receipt;
        return Promise.resolve();
      },
    },
  };
}

function rowsOf(written: Written): SessionCatalogRow[] {
  return (written.files["sessions"] ?? []) as SessionCatalogRow[];
}

beforeAll(async () => {
  home = await mkdtemp(join(tmpdir(), "babel-scan-"));
  process.env["HOME"] = home;
  delete process.env["CODEX_HOME"];
  workspace = join(home, "checkout");
  await mkdir(workspace, { recursive: true });
  for (const args of [
    ["init", "--initial-branch=main"],
    ["remote", "add", "origin", "https://github.com/atyrode/babel.git"],
  ]) {
    const child = Bun.spawn(["git", ...args], {
      cwd: workspace,
      env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_SYSTEM: "/dev/null" },
      stdout: "ignore",
      stderr: "ignore",
    });
    await child.exited;
  }

  const ompRoot = join(home, ".omp", "agent", "sessions");
  await writeOmpSession(ompRoot, {
    project: "-checkout",
    stem: "2026-09-01T00-00-00-000Z_aaaa",
    title: "The scan's own session",
    cwd: workspace,
    turns: [{ toolCalls: 1, usage: { totalTokens: 1200, cost: 0.25 } }],
    toolErrors: 2,
  });
  await writeOmpSession(ompRoot, {
    project: "-elsewhere",
    stem: "2026-09-01T01-00-00-000Z_bbbb",
    title: "Work in a directory that is gone",
    cwd: join(home, "vanished"),
  });
  await writeCodexRollout(join(home, ".codex"), {
    date: ["2026", "09", "02"],
    name: "rollout-abc",
    cwd: workspace,
    delivered: "Explain the catalog's repository identity",
  });
  await writeCodexState(join(home, ".codex"), [1_756_000_000]);
  await writeClaudeSession(join(home, ".claude"), {
    project: "-checkout",
    session: "12121212-3434-4545-8656-767676767676",
    title: "A claude session",
    cwd: workspace,
  });
});

afterAll(async () => {
  if (originalHome === undefined) delete process.env["HOME"];
  else process.env["HOME"] = originalHome;
  if (originalCodexHome !== undefined) process.env["CODEX_HOME"] = originalCodexHome;
  await rm(home, { recursive: true, force: true });
});

test("a scan of this machine writes one catalog row per session", async () => {
  const { sink, written } = memorySink();
  const receipt = await scan(
    ScanInputSchema.parse({ machineId: "dev-01", runId: "run_fixed" }),
    sink,
  );
  const rows = rowsOf(written);

  expect(rows.map((row) => row.selector)).toEqual([
    "claude/-checkout/12121212-3434-4545-8656-767676767676",
    "codex/sessions/2026/09/02/rollout-abc.jsonl",
    "codex/state",
    "omp/-checkout/2026-09-01T00-00-00-000Z_aaaa",
    "omp/-elsewhere/2026-09-01T01-00-00-000Z_bbbb",
  ]);
  for (const row of rows) {
    expect(row.host).toBe("dev-01");
    expect(row.seen_at).toBe(receipt.startedAt);
    expect(row.content_digest).toMatch(/^sha256:[0-9a-f]{64}$/u);
    // The two columns `archive` owns are absent keys, not nulls: a scan that wrote nulls
    // would erase an archive's answer on the next beat.
    expect(Object.keys(row)).not.toContain("snapshot_id");
    expect(Object.keys(row)).not.toContain("archived_at");
  }

  const [, , , ompRow] = rows;
  expect(ompRow?.title).toBe("The scan's own session");
  expect(ompRow?.title_provenance).toBe("recorded");
  expect(ompRow?.workspace).toBe(workspace);
  expect(ompRow?.repository_identity).toBe(realpathSync(join(workspace, ".git")));
  expect(ompRow?.repository_remote).toBe("github.com/atyrode/babel");
  expect(ompRow?.repository_reason).toBeNull();
  expect(ompRow?.total_tokens).toBe(1200);
  expect(ompRow?.tool_errors).toBe(2);
  expect(ompRow?.cost_usd).toBeCloseTo(0.25, 9);

  // A session whose workspace this host no longer holds keeps its reason instead of an identity.
  const [, , , , gone] = rows;
  expect(gone?.repository_identity).toBeNull();
  expect(gone?.repository_reason).toBe("workspace absent on this host");

  // Codex's host state records no workspace at all, which is a third distinct reason.
  const state = rows.find((row) => row.selector === "codex/state");
  expect(state?.repository_reason).toBe("session records no workspace");
  expect(state?.total_tokens).toBeNull();
});

test("the receipt accounts for what the scan read", async () => {
  const { sink, written } = memorySink();
  const receipt = await scan(ScanInputSchema.parse({ machineId: "dev-01", runId: "run_fixed" }), sink);

  expect(written.receipt).toEqual(receipt);
  expect(receipt.kind).toBe("scan");
  expect(receipt.runId).toBe("run_fixed");
  expect(receipt.machineId).toBe("dev-01");
  expect(receipt.closure).toBe("completed");
  expect(receipt.counts["sessions"]).toBe(5);
  expect(receipt.counts["omp"]).toBe(2);
  expect(receipt.counts["codex"]).toBe(2);
  expect(receipt.counts["claude"]).toBe(1);
  expect(receipt.counts["titled"]).toBe(4);
  // Three sessions name the one checkout; the repository is counted once.
  expect(receipt.counts["repositories"]).toBe(1);
  expect(receipt.counts["with_repository"]).toBe(3);
  expect(receipt.counts["with_usage"]).toBe(1);
  expect(receipt.counts["skipped"]).toBe(0);
  expect(receipt.counts["bytes"]).toBeGreaterThan(0);
  expect(receipt.reason).toBeUndefined();
  expect(Date.parse(receipt.finishedAt)).toBeGreaterThanOrEqual(Date.parse(receipt.startedAt));
});

test("a scheduled beat carries no run id, so the machine mints one", async () => {
  const { sink } = memorySink();
  const receipt = await scan(ScanInputSchema.parse({ machineId: "dev-01" }), sink);
  expect(receipt.runId).toMatch(/^run_[0-9a-f]{32}$/u);
});

test("the harness filter and explicit roots narrow what is read", async () => {
  const { sink, written } = memorySink();
  await scan(
    ScanInputSchema.parse({
      machineId: "dev-01",
      runId: "run_narrow",
      harnesses: ["omp"],
      roots: [join(home, ".omp", "agent", "sessions")],
    }),
    sink,
  );
  expect(rowsOf(written).map((row) => row.harness)).toEqual(["omp", "omp"]);
});

test("a session that cannot be read costs one row, not the scan", async () => {
  const unreadable = join(
    home,
    ".omp",
    "agent",
    "sessions",
    "-elsewhere",
    "2026-09-01T01-00-00-000Z_bbbb.jsonl",
  );
  await chmod(unreadable, 0o000);
  try {
    const { sink, written } = memorySink();
    const receipt = await scan(
      ScanInputSchema.parse({ machineId: "dev-01", runId: "run_partial", harnesses: ["omp"] }),
      sink,
    );
    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["skipped"]).toBe(1);
    expect(receipt.counts["sessions"]).toBe(1);
    expect(receipt.reason).toContain("omp/-elsewhere/2026-09-01T01-00-00-000Z_bbbb");
    expect(rowsOf(written)).toHaveLength(1);
  } finally {
    await chmod(unreadable, 0o644);
  }
});

test("the input schema refuses what the store could not hold", () => {
  expect(() => ScanInputSchema.parse({})).toThrow();
  expect(() => ScanInputSchema.parse({ machineId: "" })).toThrow();
  expect(() => ScanInputSchema.parse({ machineId: "dev-01", harnesses: ["emacs"] })).toThrow();
  expect(() => ScanInputSchema.parse({ machineId: "dev-01", unexpected: true })).toThrow();
  const parsed = ScanInputSchema.parse({ machineId: "dev-01" });
  expect(parsed.roots).toEqual([]);
  expect(parsed.harnesses).toEqual([]);
});

test("a machine with no sessions writes an empty catalog and a receipt", async () => {
  const empty = await mkdtemp(join(tmpdir(), "babel-scan-empty-"));
  await writeFile(join(empty, "not-a-session"), "");
  const { sink, written } = memorySink();
  const receipt = await scan(
    ScanInputSchema.parse({ machineId: "dev-02", runId: "run_empty", roots: [empty] }),
    sink,
  );
  expect(rowsOf(written)).toEqual([]);
  expect(receipt.counts["sessions"]).toBe(0);
  await rm(empty, { recursive: true, force: true });
});
