import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";

/*
  SYNTHETIC SESSION TREES.

  Each writer lays out exactly what one harness writes, in the records the adapters read, so a
  test can state a layout and a transcript rather than a pile of fixture files. The record
  shapes are the ones observed in the operator's live corpus (see the adapters' own notes);
  nothing here is a mock of an adapter, only of a harness's disk.
*/

async function writeLines(path: string, records: readonly unknown[]): Promise<string> {
  await mkdir(dirname(path), { recursive: true });
  await Bun.write(path, records.map((record) => JSON.stringify(record)).join("\n") + "\n");
  return path;
}

export interface OmpTurn {
  toolCalls?: number;
  usage?: { totalTokens: number; cost?: number };
}

export interface OmpSpec {
  project: string;
  stem: string;
  /** The padded title record OMP rewrites in place; omitted means the record is absent. */
  title?: string;
  /** The title inside the session record, which the title record supersedes. */
  sessionTitle?: string;
  cwd?: string;
  createdAt?: string;
  turns?: readonly OmpTurn[];
  toolErrors?: number;
  /** Extra raw lines appended verbatim — a torn tail, a garbage record. */
  trailing?: readonly string[];
}

/** `<root>/<project>/<stem>.jsonl`, the layout `~/.omp/agent/sessions` holds. */
export async function writeOmpSession(root: string, spec: OmpSpec): Promise<string> {
  const records: unknown[] = [];
  if (spec.title !== undefined) {
    records.push({ type: "title", v: 1, title: spec.title, updatedAt: "2026-09-01T00:00:00.000Z", pad: " ".repeat(32) });
  }
  records.push({
    type: "session",
    version: 3,
    id: `${spec.project}-${spec.stem}`,
    timestamp: spec.createdAt ?? "2026-09-01T00:00:00.000Z",
    ...(spec.cwd === undefined ? {} : { cwd: spec.cwd }),
    ...(spec.sessionTitle === undefined ? {} : { title: spec.sessionTitle }),
  });
  for (const [index, turn] of (spec.turns ?? []).entries()) {
    records.push({
      type: "message",
      id: `turn-${index}`,
      timestamp: "2026-09-01T00:01:00.000Z",
      message: {
        role: "assistant",
        model: "claude-haiku-4-5",
        provider: "anthropic",
        content: Array.from({ length: turn.toolCalls ?? 0 }, () => ({ type: "toolCall" })),
        ...(turn.usage === undefined
          ? {}
          : {
              usage: {
                input: 9,
                output: 41,
                cacheRead: 100,
                cacheWrite: 10,
                totalTokens: turn.usage.totalTokens,
                ...(turn.usage.cost === undefined ? {} : { cost: { total: turn.usage.cost } }),
              },
            }),
      },
    });
  }
  for (let i = 0; i < (spec.toolErrors ?? 0); i++) {
    records.push({ type: "message", id: `error-${i}`, message: { role: "toolResult", isError: true } });
  }
  const path = join(root, spec.project, `${spec.stem}.jsonl`);
  await writeLines(path, records);
  if (spec.trailing !== undefined && spec.trailing.length > 0) {
    const existing = await Bun.file(path).text();
    await Bun.write(path, existing + spec.trailing.join("\n") + "\n");
  }
  return path;
}

/** A session's sibling artifact tree: JSONL files one level deeper than a session log. */
export async function writeOmpArtifact(root: string, project: string, stem: string, name: string): Promise<string> {
  return writeLines(join(root, project, stem, name), [{ type: "message", message: { role: "assistant" } }]);
}

export interface CodexSpec {
  /** The date partition Codex writes: sessions/<yyyy>/<mm>/<dd>/<name>.jsonl. */
  date: [string, string, string];
  name: string;
  cwd?: string;
  timestamp?: string;
  /** `session_meta.source`: "vscode", { subagent: { other } } or { subagent: { thread_spawn } }. */
  source?: unknown;
  /** The turn delivered on the event channel. */
  delivered?: string;
  /** A `response_item` user record, used only when no delivered turn exists. */
  responseItem?: string;
}

export async function writeCodexRollout(root: string, spec: CodexSpec): Promise<string> {
  const [year, month, day] = spec.date;
  const timestamp = spec.timestamp ?? "2026-09-02T10:00:00.000Z";
  const records: unknown[] = [
    {
      timestamp,
      type: "session_meta",
      payload: {
        id: `thread-${spec.name}`,
        session_id: `session-${spec.name}`,
        timestamp,
        ...(spec.cwd === undefined ? {} : { cwd: spec.cwd }),
        ...(spec.source === undefined ? {} : { source: spec.source }),
        originator: "codex_cli_rs",
      },
    },
  ];
  if (spec.delivered !== undefined) {
    records.push({
      timestamp: "2026-09-02T10:01:00.000Z",
      type: "event_msg",
      payload: { type: "user_message", message: spec.delivered },
    });
  }
  if (spec.responseItem !== undefined) {
    records.push({
      timestamp: "2026-09-02T10:02:00.000Z",
      type: "response_item",
      payload: { type: "message", role: "user", content: [{ type: "input_text", text: spec.responseItem }] },
    });
  }
  return writeLines(join(root, "sessions", year, month, day, `${spec.name}.jsonl`), records);
}

/** `<root>/history.jsonl` plus the index beside it: Codex's host state, one session. */
export async function writeCodexState(root: string, seconds: readonly number[]): Promise<string> {
  await writeLines(join(root, "session_index.jsonl"), [{ thread: "t" }]);
  return writeLines(
    join(root, HISTORY_FILE),
    seconds.map((ts) => ({ session_id: "s", ts, text: "a prompt whose text is never read" })),
  );
}

const HISTORY_FILE = "history.jsonl";

export interface ClaudeSpec {
  project: string;
  session: string;
  title?: string;
  cwd?: string;
  secondCwd?: string;
  branch?: string;
  timestamps?: readonly string[];
}

/** `<root>/projects/<project>/<session>.jsonl`. */
export async function writeClaudeSession(root: string, spec: ClaudeSpec): Promise<string> {
  const stamps = spec.timestamps ?? ["2026-09-03T08:00:00.000Z", "2026-09-03T09:30:00.000Z"];
  const records: unknown[] = stamps.map((timestamp, index) => ({
    type: index === 0 ? "user" : "assistant",
    timestamp,
    sessionId: spec.session,
    version: "2.1.0",
    ...(index === 0 && spec.cwd !== undefined ? { cwd: spec.cwd } : {}),
    ...(index > 0 && spec.secondCwd !== undefined ? { cwd: spec.secondCwd } : {}),
    ...(spec.branch === undefined ? {} : { gitBranch: spec.branch }),
    ...(index > 0 && spec.title !== undefined ? { aiTitle: spec.title } : {}),
  }));
  return writeLines(join(root, "projects", spec.project, `${spec.session}.jsonl`), records);
}

/** The canonical digest of a file's bytes, computed independently of the adapters. */
export async function digestOf(path: string): Promise<string> {
  const hasher = new Bun.CryptoHasher("sha256");
  hasher.update(new Uint8Array(await Bun.file(path).arrayBuffer()));
  return "sha256:" + hasher.digest("hex");
}
