/*
  What a capture says about itself, read from the stream a preparation seals (#453): the
  normalized, REDACTED records, handed to the fold exactly as `prepare` hands them — one encoded
  record at a time from the digester on a fetch, arbitrary chunks of the kept stream on a replay.
  The sessions are the synthetic harness layouts of `test/fixtures.ts`.
*/

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { Harness } from "../contract.ts";
import type { RecordSink } from "./output.ts";
import { secretScan } from "./preflight.ts";
import { captureFacts, type CaptureFacts } from "./session-facts.ts";
import { sessionDigester } from "./session-records.ts";
import {
  type CodexSpec,
  writeClaudeSession,
  writeCodexRollout,
  writeCodexState,
  writeOmpSession,
} from "./test/fixtures.ts";

let root = "";

beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "babel-session-facts-"));
});

afterAll(async () => {
  await rm(root, { recursive: true, force: true });
});

/** A credential in a format the scan matches, assembled so no literal of it is committed. */
const LEAKED_KEY = `${"AKIA"}IOSFODNN7SYNTH01`;

/**
 * The facts the pass states over one log's bytes, and the facts a replay of the stream it sealed
 * states, fed in chunks that split records and multibyte characters alike.
 */
async function read(
  harness: Harness,
  path: string,
): Promise<{
  readonly sealed: CaptureFacts;
  readonly replayed: CaptureFacts;
  readonly stream: string;
}> {
  const pass = captureFacts(harness);
  const chunks: Uint8Array[] = [];
  const kept: RecordSink = {
    write(record) {
      chunks.push(typeof record === "string" ? new TextEncoder().encode(record) : record.slice());
    },
    async close() {},
  };
  const digester = sessionDigester(
    {
      write(record) {
        pass.sink.write(record);
        kept.write(record);
      },
      async close() {},
    },
    secretScan(),
  );
  digester.write(new Uint8Array(await readFile(path)));
  digester.finish();
  const sealed = await pass.finish();

  const stream = Buffer.concat(chunks);
  const replay = captureFacts(harness);
  for (let at = 0; at < stream.byteLength; at += 7) replay.sink.write(stream.subarray(at, at + 7));
  return { sealed, replayed: await replay.finish(), stream: stream.toString("utf8") };
}

test("an omp capture states its recorded title, its workspace and the harness's own usage", async () => {
  const path = await writeOmpSession(join(root, "omp"), {
    project: "-home-alex-babel",
    stem: "2026-09-01T00-00-00-000Z_01a0",
    title: "Porting the adapters",
    sessionTitle: "the session record's own title",
    cwd: "/home/alex/babel",
    turns: [
      { toolCalls: 2, usage: { totalTokens: 8504, cost: 0.5 } },
      { toolCalls: 1, usage: { totalTokens: 8725, cost: 0.25 } },
      { toolCalls: 0 },
    ],
    toolErrors: 1,
  });
  const { sealed, replayed } = await read("omp", path);
  expect(sealed).toEqual({
    // The padded title record supersedes the session record's own title.
    title: "Porting the adapters",
    title_provenance: "recorded",
    workspace: "/home/alex/babel",
    cost_usd: 0.75,
    total_tokens: 17229,
    // Three assistant turns, one of which carried no usage block: the totals are a floor.
    turns: 3,
    tool_errors: 1,
  });
  // A kept reading replayed is the same capture, and states the same facts.
  expect(replayed).toEqual(sealed);
});

test("an omp log whose turns carry no usage block states no spend, not a free session", async () => {
  const path = await writeOmpSession(join(root, "quiet"), {
    project: "-home-alex-quiet",
    stem: "2026-09-01T00-00-00-000Z_05e0",
    title: "Written before OMP recorded usage",
    turns: [{ toolCalls: 1 }, { toolCalls: 0 }],
    toolErrors: 1,
  });
  const { sealed } = await read("omp", path);
  // No totals at all, not zeros beside a turn count; and no workspace, as no record names one.
  expect(sealed).toEqual({
    title: "Written before OMP recorded usage",
    title_provenance: "recorded",
  });
});

test("a torn tail costs only itself: the records before it still state their facts", async () => {
  const path = await writeOmpSession(join(root, "torn"), {
    project: "-home-alex-torn",
    stem: "2026-09-01T00-00-00-000Z_06f0",
    title: "Half a log",
    cwd: "/home/alex/torn",
    turns: [{ usage: { totalTokens: 10, cost: 0.5 } }],
  });
  // A harness mid-append when the capture was taken: a record cut short, with no newline.
  await Bun.write(
    path,
    (await Bun.file(path).text()) + '{"type":"message","message":{"role":"assist',
  );
  const { sealed, replayed } = await read("omp", path);
  expect(sealed).toEqual({
    title: "Half a log",
    title_provenance: "recorded",
    workspace: "/home/alex/torn",
    cost_usd: 0.5,
    total_tokens: 10,
    turns: 1,
    tool_errors: 0,
  });
  expect(replayed).toEqual(sealed);
});

test("a Claude Code capture states its recorded title; a Codex capture's is derived", async () => {
  const claude = await read(
    "claude",
    await writeClaudeSession(join(root, "claude"), {
      project: "-home-alex-code",
      session: "11111111-2222-4333-8444-555555555555",
      title: "Reviewing the broker's restart path",
      cwd: "/home/alex/code",
    }),
  );
  // No usage: the format records none this reader sums, and none is not zero.
  expect(claude.sealed).toEqual({
    title: "Reviewing the broker's restart path",
    title_provenance: "recorded",
    workspace: "/home/alex/code",
  });

  const codex = await read(
    "codex",
    await writeCodexRollout(join(root, "codex"), {
      date: ["2026", "09", "02"],
      name: "rollout-2026-09-02T10-00-00-000Z-abc",
      cwd: "/home/alex/manifold",
      delivered: "Port the session adapters to TypeScript, keeping the identities",
    }),
  );
  expect(codex.sealed).toEqual({
    title: "Port the session adapters to TypeScript, keeping the identities",
    title_provenance: "derived",
    workspace: "/home/alex/manifold",
  });
  expect(codex.replayed).toEqual(codex.sealed);
});

test("a Codex title is the thread's own request, never a harness template or a parent's replay", async () => {
  const title = async (spec: Omit<CodexSpec, "date">) =>
    (
      await read(
        "codex",
        await writeCodexRollout(join(root, "codex-rules"), { date: ["2026", "09", "05"], ...spec }),
      )
    ).sealed.title;
  // A built-in subagent role opens with a fixed harness template, which names nothing.
  expect(
    await title({
      name: "guardian",
      source: { subagent: { other: "guardian" } },
      delivered: "The following is the Codex agent history whose request action you are assessing",
    }),
  ).toBeUndefined();
  // A spawned thread may replay its parent: it is titled from its own job, or not at all.
  expect(
    await title({
      name: "spawn-named",
      source: {
        subagent: { thread_spawn: { agent_path: "/root/audit_dotfiles/pr49_safety_review" } },
      },
      delivered: "Here is the parent's whole conversation, replayed",
    }),
  ).toBe("Pr49 safety review");
  expect(
    await title({
      name: "spawn-anonymous",
      source: { subagent: { thread_spawn: {} } },
      delivered: "Here is the parent's whole conversation, replayed",
    }),
  ).toBeUndefined();
  // With no delivered turn, the model's input stream is read past injected context blocks.
  expect(
    await title({
      name: "fallback",
      responseItem: "Reconcile the two catalogs and report the difference",
    }),
  ).toBe("Reconcile the two catalogs and report the difference");
  expect(
    await title({
      name: "injected-only",
      responseItem:
        "<recommended_plugins>\nHere is a list of plugins that are available\n</recommended_plugins>",
    }),
  ).toBeUndefined();
  // The host state records every prompt on the host; it is no one thread's, and never titled.
  const state = await read(
    "codex",
    await writeCodexState(join(root, "codex-state"), [1_756_000_000]),
  );
  expect(state.sealed).toEqual({});
});

test("a title that held a credential reaches the row redacted", async () => {
  const path = await writeOmpSession(join(root, "leaky"), {
    project: "-home-alex-ops",
    stem: "2026-09-04T00-00-00-000Z_03c0",
    title: `rotate aws_access_key_id ${LEAKED_KEY} today`,
    cwd: "/home/alex/ops",
  });
  const { sealed, stream } = await read("omp", path);
  expect(stream).not.toContain(LEAKED_KEY);
  expect(sealed.title).not.toContain(LEAKED_KEY);
  expect(sealed.title).toContain("[[babel-redacted:aws-access-key-id@1:");
  expect(sealed.title_provenance).toBe("recorded");
});

test("a total no row could hold is left out, and the rest of the facts stand", async () => {
  const path = await writeOmpSession(join(root, "garbage"), {
    project: "-home-alex-odd",
    stem: "2026-09-05T00-00-00-000Z_04d0",
    title: "A log with odd arithmetic",
    turns: [{ usage: { totalTokens: 10, cost: -2 } }, { usage: { totalTokens: 0.5 } }],
  });
  const { sealed } = await read("omp", path);
  expect(sealed).toEqual({
    title: "A log with odd arithmetic",
    title_provenance: "recorded",
    turns: 2,
    tool_errors: 0,
  });
});
