import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { claude, claim, codex, contentDigest, discover, existingRoots, omp } from "../adapters/index.ts";
import { readRecords } from "../adapters/records.ts";
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
    sessionTitle: "the session record's own title",
    cwd: "/home/alex/babel",
    createdAt: "2026-09-01T00:00:00.000Z",
    turns: [
      { toolCalls: 2, usage: { totalTokens: 8504, cost: 0.00745375 } },
      { toolCalls: 1, usage: { totalTokens: 8725, cost: 0.00197855 } },
      { toolCalls: 0 },
    ],
    toolErrors: 1,
  });
  await writeOmpArtifact(ompRoot, "-home-alex-babel", "2026-09-01T00-00-00-000Z_01a0", "__advisor.jsonl");
  await writeCodexRollout(codexRoot, {
    date: ["2026", "09", "02"],
    name: "rollout-2026-09-02T10-00-00-000Z-abc",
    cwd: "/home/alex/manifold",
    delivered: "Port the session adapters to TypeScript, keeping the identities",
  });
  await writeCodexState(codexRoot, [1_756_000_000, 1_756_100_000]);
  await writeClaudeSession(claudeRoot, {
    project: "-home-alex-code",
    session: "11111111-2222-4333-8444-555555555555",
    title: "Reviewing the broker's restart path",
    cwd: "/home/alex/code",
    branch: "main",
  });
});

afterAll(async () => {
  if (originalHome === undefined) delete process.env["HOME"];
  else process.env["HOME"] = originalHome;
  if (originalCodexHome !== undefined) process.env["CODEX_HOME"] = originalCodexHome;
  await rm(home, { recursive: true, force: true });
});

describe("each adapter recognizes its own layout and refuses the others'", () => {
  test("discovery over every root finds only this harness's sessions", async () => {
    const roots = [ompRoot, codexRoot, claudeRoot];
    expect((await omp.discover(roots)).map((ref) => ref.selector)).toEqual([
      "omp/-home-alex-babel/2026-09-01T00-00-00-000Z_01a0",
    ]);
    expect((await codex.discover(roots)).map((ref) => ref.selector)).toEqual([
      "codex/sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl",
      "codex/state",
    ]);
    expect((await claude.discover(roots)).map((ref) => ref.selector)).toEqual([
      "claude/-home-alex-code/11111111-2222-4333-8444-555555555555",
    ]);
  });

  test("a path is claimed by exactly one adapter", () => {
    const ompLog = join(ompRoot, "-home-alex-babel", "2026-09-01T00-00-00-000Z_01a0.jsonl");
    const codexLog = join(codexRoot, "sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl");
    const codexHistory = join(codexRoot, "history.jsonl");
    const claudeLog = join(claudeRoot, "projects/-home-alex-code/11111111-2222-4333-8444-555555555555.jsonl");

    expect(omp.claim(ompLog)?.selector).toBe("omp/-home-alex-babel/2026-09-01T00-00-00-000Z_01a0");
    expect(omp.claim(codexLog)).toBeNull();
    expect(omp.claim(claudeLog)).toBeNull();

    expect(codex.claim(codexLog)?.selector).toBe(
      "codex/sessions/2026/09/02/rollout-2026-09-02T10-00-00-000Z-abc.jsonl",
    );
    expect(codex.claim(codexHistory)?.selector).toBe("codex/state");
    expect(codex.claim(ompLog)).toBeNull();
    expect(codex.claim(claudeLog)).toBeNull();

    expect(claude.claim(claudeLog)?.selector).toBe("claude/-home-alex-code/11111111-2222-4333-8444-555555555555");
    expect(claude.claim(ompLog)).toBeNull();
    expect(claude.claim(codexLog)).toBeNull();

    // The registry answers with the one adapter that recognized it.
    expect(claim(codexLog)?.harness).toBe("codex");
    expect(claim(join(home, "notes", "scratch.jsonl"))).toBeNull();
  });

  test("a sibling artifact tree's own logs are not sessions", async () => {
    const artifact = join(ompRoot, "-home-alex-babel/2026-09-01T00-00-00-000Z_01a0/__advisor.jsonl");
    expect(omp.claim(artifact)).toBeNull();
    const selectors = (await discover([ompRoot])).map((ref) => ref.selector);
    expect(selectors).not.toContain("omp/2026-09-01T00-00-00-000Z_01a0/__advisor");
    expect(selectors).toHaveLength(1);
  });

  test("history.jsonl outside a Codex root is nobody's session", () => {
    expect(codex.claim(join(home, "history.jsonl"))).toBeNull();
  });

  test("default and backup roots follow the machine's own home", async () => {
    expect(omp.defaultRoots()).toEqual([ompRoot]);
    expect(omp.backupRoots()).toEqual([
      ompRoot,
      join(home, ".omp", "agent", "blobs"),
      join(home, ".omp", "collab"),
    ]);
    expect(codex.defaultRoots()).toEqual([codexRoot]);
    expect(claude.defaultRoots()).toEqual([claudeRoot]);
    // Only the trees that exist here: blobs and collab were never created.
    expect(await existingRoots()).toEqual([claudeRoot, codexRoot, ompRoot].sort());
  });
});

describe("omp", () => {
  test("reports the recorded title, the workspace and the harness's own usage", async () => {
    const [ref] = await omp.discover([ompRoot]);
    expect(ref).toBeDefined();
    const facts = await omp.describe(ref!);
    // The padded title record is rewritten in place, so it supersedes the session record's.
    expect(facts.title).toBe("Porting the adapters");
    expect(facts.titleProvenance).toBe("recorded");
    expect(facts.workspace).toBe("/home/alex/babel");
    expect(facts.createdAt).toBe("2026-09-01T00:00:00.000Z");
    // The harness's own per-turn prices, summed and nothing more.
    expect(facts.usage?.costUsd).toBeCloseTo(0.0094323, 9);
    expect(facts.usage?.totalTokens).toBe(17229);
    expect(facts.usage?.turns).toBe(3);
    expect(facts.usage?.toolErrors).toBe(1);
    // Three assistant turns, two of them priced: the totals are a floor and say so.
    expect(facts.absent["usage_complete"]).toContain("2 of 3");
  });

  test("a log without usage blocks reports no spend, with the reason", async () => {
    await writeOmpSession(ompRoot, { project: "-tmp-quiet", stem: "quiet", title: "No turns at all" });
    const refs = await omp.discover([ompRoot]);
    const ref = refs.find((candidate) => candidate.sourceId.startsWith("-tmp-quiet/"));
    expect(ref).toBeDefined();
    const facts = await omp.describe(ref!);
    expect(facts.usage).toBeNull();
    expect(facts.absent["usage"]).toContain("no usage blocks");
    expect(facts.workspace).toBeNull();
    expect(facts.absent["workspace"]).toBe("the session record carries no cwd");
  });

  test("a torn tail degrades the description instead of failing it", async () => {
    await writeOmpSession(ompRoot, {
      project: "-tmp-torn",
      stem: "torn",
      title: "Half a log",
      cwd: "/tmp/torn",
      turns: [{ usage: { totalTokens: 10, cost: 0.5 } }],
      trailing: ['{"type":"message","message":{"role":"assist'],
    });
    const refs = await omp.discover([ompRoot]);
    const ref = refs.find((candidate) => candidate.sourceId.startsWith("-tmp-torn/"));
    const facts = await omp.describe(ref!);
    expect(facts.title).toBe("Half a log");
    expect(facts.usage?.totalTokens).toBe(10);
  });

  test("the digest is the file's own bytes, and stable across runs", async () => {
    const [ref] = await omp.discover([ompRoot]);
    const first = await omp.describe(ref!);
    const second = await omp.describe(ref!);
    expect(first.contentDigest).toBe(await digestOf(ref!.primaryPath));
    expect(second.contentDigest).toBe(first.contentDigest);
    expect(second.size).toBe(first.size);
    expect(first.contentDigest).toMatch(/^sha256:[0-9a-f]{64}$/u);
  });
});

describe("codex", () => {
  const rollout = async (spec: Parameters<typeof writeCodexRollout>[1]) => {
    const path = await writeCodexRollout(codexRoot, spec);
    const ref = codex.claim(path);
    expect(ref).not.toBeNull();
    return codex.describe(ref!);
  };

  test("titles a thread from the turn delivered on the event channel", async () => {
    const facts = await rollout({
      date: ["2026", "09", "04"],
      name: "delivered",
      cwd: "/home/alex/manifold",
      delivered: "Port the session adapters to TypeScript",
      responseItem: "<environment_context>\n  cwd: /home/alex/manifold\n</environment_context>",
    });
    expect(facts.title).toBe("Port the session adapters to TypeScript");
    // Codex records no title; this one is Babel's arithmetic over the log's own values.
    expect(facts.titleProvenance).toBe("derived");
    expect(facts.absent["title_basis"]).toBe("request");
    expect(facts.workspace).toBe("/home/alex/manifold");
  });

  test("falls back to the model's input stream, refusing injected context blocks", async () => {
    const injected = await rollout({
      date: ["2026", "09", "04"],
      name: "injected-only",
      responseItem: "<recommended_plugins>\nHere is a list of plugins that are available\n</recommended_plugins>",
    });
    expect(injected.title).toBeNull();
    expect(injected.absent["title"]).toContain("no delivered request record exposed titleable text");

    const real = await rollout({
      date: ["2026", "09", "04"],
      name: "fallback",
      responseItem: "Reconcile the two catalogs and report the difference",
    });
    expect(real.title).toBe("Reconcile the two catalogs and report the difference");
    expect(real.absent["title_basis"]).toBe("request_fallback");
  });

  test("a built-in subagent role has no caller request, and says so", async () => {
    const facts = await rollout({
      date: ["2026", "09", "05"],
      name: "guardian",
      source: { subagent: { other: "guardian" } },
      delivered: "The following is the Codex agent history whose request action you are assessing",
    });
    expect(facts.title).toBeNull();
    expect(facts.absent["title"]).toContain('built-in "guardian" role');
  });

  test("a spawned thread is titled from its own job, not its parent's replay", async () => {
    const named = await rollout({
      date: ["2026", "09", "05"],
      name: "spawn-named",
      source: { subagent: { thread_spawn: { agent_path: "/root/audit_dotfiles/pr49_safety_review" } } },
      delivered: "Here is the parent's whole conversation, replayed",
    });
    expect(named.title).toBe("Pr49 safety review");
    expect(named.absent["title_basis"]).toBe("agent_path");

    const anonymous = await rollout({
      date: ["2026", "09", "05"],
      name: "spawn-anonymous",
      source: { subagent: { thread_spawn: {} } },
      delivered: "Here is the parent's whole conversation, replayed",
    });
    expect(anonymous.title).toBeNull();
    expect(anonymous.absent["title"]).toContain("no agent path");
  });

  test("the host state is one session, dated by its own records and never titled", async () => {
    const ref = codex.claim(join(codexRoot, "history.jsonl"));
    expect(ref?.sourceId).toBe("state");
    const facts = await codex.describe(ref!);
    expect(facts.title).toBeNull();
    expect(facts.workspace).toBeNull();
    expect(facts.absent["title"]).toContain("per-host log");
    expect(facts.createdAt).toBe(new Date(1_756_000_000 * 1000).toISOString());
    expect(facts.modifiedAt).toBe(new Date(1_756_100_000 * 1000).toISOString());
    expect(facts.usage).toBeNull();
  });
});

describe("claude", () => {
  test("repeats the recorded ai-title and prefers the transcript's own cwd", async () => {
    const path = await writeClaudeSession(claudeRoot, {
      project: "-home-alex-manifold",
      session: "99999999-8888-4777-8666-555555555555",
      title: "Reading the plugin database ADR",
      cwd: "/home/alex/manifold",
      secondCwd: "/home/alex/manifold",
      branch: "feat/plugin-database",
    });
    const facts = await claude.describe(claude.claim(path)!);
    expect(facts.title).toBe("Reading the plugin database ADR");
    expect(facts.titleProvenance).toBe("recorded");
    expect(facts.workspace).toBe("/home/alex/manifold");
    expect(facts.createdAt).toBe("2026-09-03T08:00:00.000Z");
    expect(facts.modifiedAt).toBe("2026-09-03T09:30:00.000Z");
    expect(facts.usage).toBeNull();
    expect(facts.absent["usage"]).toContain("no per-turn usage");
  });

  test("without a recorded cwd the lossy project directory is reported as such", async () => {
    const path = await writeClaudeSession(claudeRoot, {
      project: "-home-alex-nix-dotfiles",
      session: "77777777-6666-4555-8444-333333333333",
    });
    const facts = await claude.describe(claude.claim(path)!);
    expect(facts.workspace).toBe("-home-alex-nix-dotfiles");
    expect(facts.absent["workspace"]).toContain("lossy");
    expect(facts.title).toBeNull();
    expect(facts.absent["title"]).toContain("no ai-title record");
  });

  test("two distinct cwds keep the first and admit the conflict", async () => {
    const path = await writeClaudeSession(claudeRoot, {
      project: "-home-alex-servers",
      session: "66666666-5555-4444-8333-222222222222",
      cwd: "/home/alex/servers",
      secondCwd: "/home/alex/servers/edge",
      title: "Moving the edge config",
    });
    const facts = await claude.describe(claude.claim(path)!);
    expect(facts.workspace).toBe("/home/alex/servers");
    expect(facts.absent["workspace"]).toContain("several distinct cwd values");
  });

  test("an unparseable record is counted, and the rest of the transcript still reads", async () => {
    const path = join(claudeRoot, "projects", "-home-alex-broken", "55555555-4444-4333-8222-111111111111.jsonl");
    await mkdir(dirname(path), { recursive: true });
    await Bun.write(
      path,
      '{"type":"user","timestamp":"2026-09-03T08:00:00.000Z","cwd":"/home/alex"}\n' +
        '{"type":"assistant","timestamp":"2026-09-\n' +
        '{"type":"assistant","timestamp":"2026-09-03T08:05:00.000Z","aiTitle":"Still readable"}\n',
    );
    const facts = await claude.describe(claude.claim(path)!);
    expect(facts.title).toBe("Still readable");
    expect(facts.absent["records"]).toBe("1 of this transcript's 3 records could not be read");
  });
});

describe("the bytes a session is identified by", () => {
  test("the registry's digest and an adapter's description agree", async () => {
    const [ref] = await omp.discover([ompRoot]);
    const described = await omp.describe(ref!);
    const direct = await contentDigest(ref!.primaryPath);
    // `archive` and `prepare` digest a path; `scan` digests while it parses. Two answers
    // about one file that disagreed would be two rows nobody could reconcile.
    expect(direct.digest).toBe(described.contentDigest);
    expect(direct.size).toBe(described.size);
  });

  test("a record too long to hold is counted, and still digested", async () => {
    const path = join(home, "long.jsonl");
    const long = JSON.stringify({ type: "message", text: "x".repeat(4096) });
    await Bun.write(path, `{"type":"session","cwd":"/w"}\n${long}\n{"type":"title","title":"after"}\n`);
    const seen: string[] = [];
    const stream = await readRecords(path, (record) => seen.push(record), 1024);
    // The oversized record is skipped for parsing and the reader resumes at the next one.
    expect(seen).toEqual(['{"type":"session","cwd":"/w"}', '{"type":"title","title":"after"}']);
    expect(stream.oversized).toBe(1);
    expect(stream.records).toBe(3);
    // The digest covers the file, not the part of it that parsed.
    expect(stream.digest).toBe(await digestOf(path));
    expect(stream.size).toBe(Bun.file(path).size);
  });

  test("a log whose last record has no newline is read to its end", async () => {
    const path = join(home, "unterminated.jsonl");
    await Bun.write(path, '{"type":"session","cwd":"/w"}\n{"type":"title","title":"still writing"}');
    const seen: string[] = [];
    const stream = await readRecords(path, (record) => seen.push(record));
    expect(seen).toHaveLength(2);
    expect(stream.digest).toBe(await digestOf(path));
  });
});
