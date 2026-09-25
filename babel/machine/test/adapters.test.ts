import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { claude, claim, codex, contentDigest, existingRoots, omp } from "../adapters/index.ts";
import {
  digestOf,
  writeClaudeSession,
  writeCodexRollout,
  writeCodexState,
  writeOmpArtifact,
  writeOmpSession,
} from "./fixtures.ts";

/*
  The adapters against synthetic trees for all three harnesses under one temporary HOME. The
  matrix at the top is the load-bearing one: three harnesses keep their logs in directories
  with overlapping names ("sessions" is OMP's and Codex's both), and an adapter that claimed a
  foreign log would file another harness's session under its own identity — a wrong row nobody
  downstream could detect.
*/

let home = "";
let ompRoot = "";
let codexRoot = "";
let claudeRoot = "";
const originalHome = process.env["HOME"];
const originalCodexHome = process.env["CODEX_HOME"];

beforeAll(async () => {
  home = await mkdtemp(join(tmpdir(), "babel-adapters-"));
  ompRoot = join(home, ".omp", "agent", "sessions");
  codexRoot = join(home, ".codex");
  claudeRoot = join(home, ".claude");
  process.env["HOME"] = home;
  delete process.env["CODEX_HOME"];

  await writeOmpSession(ompRoot, {
    project: "-home-alex-babel",
    stem: "2026-09-01T00-00-00-000Z_01a0",
    title: "Porting the adapters",
  });
  await writeOmpArtifact(
    ompRoot,
    "-home-alex-babel",
    "2026-09-01T00-00-00-000Z_01a0",
    "__advisor.jsonl",
  );
  await writeCodexRollout(codexRoot, {
    date: ["2026", "09", "02"],
    name: "rollout-2026-09-02T10-00-00-000Z-abc",
  });
  await writeCodexState(codexRoot, [1_756_000_000]);
  await writeClaudeSession(claudeRoot, {
    project: "-home-alex-code",
    session: "11111111-2222-4333-8444-555555555555",
  });
});

afterAll(async () => {
  if (originalHome === undefined) delete process.env["HOME"];
  else process.env["HOME"] = originalHome;
  if (originalCodexHome !== undefined) process.env["CODEX_HOME"] = originalCodexHome;
  await rm(home, { recursive: true, force: true });
});

describe("each adapter recognizes its own layout and refuses the others'", () => {
  test("a path is claimed by exactly one adapter", () => {
    const ompLog = join(ompRoot, "-home-alex-babel", "2026-09-01T00-00-00-000Z_01a0.jsonl");
    const codexLog = join(
      codexRoot,
      "sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl",
    );
    const codexHistory = join(codexRoot, "history.jsonl");
    const claudeLog = join(
      claudeRoot,
      "projects/-home-alex-code/11111111-2222-4333-8444-555555555555.jsonl",
    );

    expect(omp.claim(ompLog)?.selector).toBe("omp/-home-alex-babel/2026-09-01T00-00-00-000Z_01a0");
    expect(omp.claim(codexLog)).toBeNull();
    expect(omp.claim(claudeLog)).toBeNull();

    expect(codex.claim(codexLog)?.selector).toBe(
      "codex/sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl",
    );
    expect(codex.claim(codexHistory)?.selector).toBe("codex/state");
    expect(codex.claim(ompLog)).toBeNull();
    expect(codex.claim(claudeLog)).toBeNull();

    expect(claude.claim(claudeLog)?.selector).toBe(
      "claude/-home-alex-code/11111111-2222-4333-8444-555555555555",
    );
    expect(claude.claim(ompLog)).toBeNull();
    expect(claude.claim(codexLog)).toBeNull();

    // The registry answers with the one adapter that recognized it.
    expect(claim(codexLog)?.harness).toBe("codex");
    expect(claim(join(home, "notes", "scratch.jsonl"))).toBeNull();
  });

  test("a sibling artifact tree's own logs are not sessions", () => {
    const artifact = join(
      ompRoot,
      "-home-alex-babel/2026-09-01T00-00-00-000Z_01a0/__advisor.jsonl",
    );
    // Not OMP's, whose logs sit one directory below the root, and no other harness's either.
    expect(omp.claim(artifact)).toBeNull();
    expect(claim(artifact)).toBeNull();
  });

  test("history.jsonl outside a Codex root is nobody's session", () => {
    expect(codex.claim(join(home, "history.jsonl"))).toBeNull();
  });

  test("archive history claims use supplied listing existence instead of live siblings", () => {
    const liveHistory = join(codexRoot, "history.jsonl");
    expect(claim(liveHistory, () => false)).toBeNull();
    expect(codex.claim(liveHistory, () => false)).toBeNull();
    const archivedRoot = join(home, "archived-only-codex");
    const archivedHistory = join(archivedRoot, "history.jsonl");
    const archivedExists = (path: string): boolean => path === join(archivedRoot, "sessions");
    expect(claim(archivedHistory, archivedExists)?.selector).toBe("codex/state");
    expect(codex.claim(archivedHistory, archivedExists)?.selector).toBe("codex/state");
    expect(claim(archivedHistory)).toBeNull();
    expect(claim(liveHistory)?.selector).toBe("codex/state");
  });

  test("backup roots follow the machine's own home", async () => {
    expect(omp.backupRoots()).toEqual([
      ompRoot,
      join(home, ".omp", "agent", "blobs"),
      join(home, ".omp", "collab"),
    ]);
    expect(codex.backupRoots()).toEqual([codexRoot]);
    expect(claude.backupRoots()).toEqual([claudeRoot]);
    // Only the trees that exist here: blobs and collab were never created.
    expect(await existingRoots()).toEqual([claudeRoot, codexRoot, ompRoot].sort());
  });
});

describe("the bytes a session is identified by", () => {
  test("the digest is the file's own bytes and their count, across every chunk read", async () => {
    // Larger than one chunk of the file's stream, so the digest is folded over several.
    const path = join(home, "long.jsonl");
    await Bun.write(path, `${JSON.stringify({ type: "message", text: "x".repeat(2 << 20) })}\n`);
    const { digest, size } = await contentDigest(path);
    expect(digest).toBe(await digestOf(path));
    expect(size).toBe(Bun.file(path).size);
  });
});
