import { readdir, stat } from "node:fs/promises";
import { join } from "node:path";

import {
  type Adapter,
  type SessionFacts,
  type SessionRef,
  homeDir,
  idSegment,
  sessionRef,
  validSourceId,
  walkablePath,
} from "./identity.ts";
import { readRecords } from "./records.ts";

/*
  THE OMP ADAPTER, ported from internal/adapter/omp.

  The layout it reads:

    <data root>/agent/sessions/<project>/<stem>.jsonl   primary log
    <data root>/agent/sessions/<project>/<stem>/...      sibling artifacts
    <data root>/agent/blobs/<64 hex>[.<ext>]             blob store

  A session log is a "*.jsonl" one directory below a root. That single rule is what keeps a
  session's sibling artifact tree — a directory sharing the log's stem, whose own JSONL files
  sit one level deeper — from being mistaken for sessions of its own, and it is the rule both
  discovery and `claim` apply, so a file recognized here and a file recognized from a
  snapshot's listing get the same identity.

  The head of a log is a fixed-width padded {"type":"title"} record rewritten in place as the
  title changes, then a {"type":"session"} record carrying the id, the creation timestamp and
  the workspace cwd. OMP is the one harness of the three that writes a usage block into every
  assistant record, so it is the one whose spend Babel can sum without inferring anything
  (issue #89): the numbers below are OMP's own arithmetic over its own rate card, repeated.
*/

const HARNESS = "omp";
const SESSION_EXT = ".jsonl";
/** The head records a title and a session record are allowed to appear in. */
const HEADER_RECORDS = 8;

interface Header {
  title: string;
  cwd: string;
  createdAt: string;
  done: boolean;
}

interface UsageTotals {
  assistantTurns: number;
  turnsWithUsage: number;
  turnsWithCost: number;
  unreadable: number;
  costUsd: number;
  totalTokens: number;
  toolErrors: number;
}

export const omp: Adapter = {
  harness: HARNESS,
  schema: 1,

  defaultRoots() {
    const home = homeDir();
    return home === "" ? [] : [join(home, ".omp", "agent", "sessions")];
  },

  /*
    Two trees beyond the sessions root. The content-addressed blob store, because referenced
    blobs live outside the session trees and a backup without them could never restore a
    session whole. And the collaboration transcripts, which sit beside "agent" and are written
    in the same record language: they are session history that discovery cannot address, so
    leaving them out of a backup would drop transcripts outright.
  */
  backupRoots() {
    const home = homeDir();
    if (home === "") return [];
    const agent = join(home, ".omp", "agent");
    return [join(agent, "sessions"), join(agent, "blobs"), join(home, ".omp", "collab")];
  },

  claim(path) {
    if (!path.endsWith(SESSION_EXT) || !walkablePath(path)) return null;
    const segments = path.split("/");
    const n = segments.length;
    const project = segments[n - 2];
    const file = segments[n - 1];
    if (n < 3 || segments[n - 3] !== "sessions" || project === undefined || file === undefined) {
      return null;
    }
    const id = idSegment(project) + "/" + idSegment(file.slice(0, -SESSION_EXT.length));
    return validSourceId(id) ? sessionRef(HARNESS, id, path) : null;
  },

  async discover(roots) {
    const found: SessionRef[] = [];
    const seen = new Set<string>();
    for (const root of roots) {
      const projects = await readdir(root, { withFileTypes: true }).catch(() => []);
      for (const project of projects) {
        if (!project.isDirectory()) continue;
        const projectDir = join(root, project.name);
        const entries = await readdir(projectDir, { withFileTypes: true }).catch(() => []);
        for (const entry of entries) {
          if (!entry.isFile() || !entry.name.endsWith(SESSION_EXT)) continue;
          const stem = entry.name.slice(0, -SESSION_EXT.length);
          const id = idSegment(project.name) + "/" + idSegment(stem);
          if (!validSourceId(id) || seen.has(id)) continue;
          seen.add(id);
          found.push(sessionRef(HARNESS, id, join(projectDir, entry.name)));
        }
      }
    }
    found.sort((a, b) => (a.sourceId < b.sourceId ? -1 : a.sourceId > b.sourceId ? 1 : 0));
    return found;
  },

  async describe(ref) {
    const info = await stat(ref.primaryPath);
    const header: Header = { title: "", cwd: "", createdAt: "", done: false };
    const totals: UsageTotals = {
      assistantTurns: 0,
      turnsWithUsage: 0,
      turnsWithCost: 0,
      unreadable: 0,
      costUsd: 0,
      totalTokens: 0,
      toolErrors: 0,
    };
    let seen = 0;

    const stream = await readRecords(ref.primaryPath, (record) => {
      seen++;
      if (!header.done && seen <= HEADER_RECORDS) readHeader(record, header, totals);
      // A record is only re-read for usage when its text names one of the two roles that
      // carry any: a log is overwhelmingly reasoning and tool traffic, and decoding all of it
      // to find the two would make describing a session cost several times what it costs.
      if (record.includes('"assistant"') || record.includes('"toolResult"')) {
        addUsage(record, totals);
      }
    });

    const absent: Record<string, string> = {};
    if (header.title === "") {
      absent["title"] = "the session log carries no non-empty title record";
    }
    if (header.cwd === "") absent["workspace"] = "the session record carries no cwd";
    if (header.createdAt === "") {
      absent["created_at"] = "the session record carries no parsable timestamp";
    }
    const usage = finishUsage(totals, stream.oversized, absent);

    return {
      ref,
      // OMP writes this title into the log itself. That its own tiny model composed the text
      // does not make it Babel's inference: the value arrived with the session.
      title: header.title === "" ? null : header.title,
      titleProvenance: header.title === "" ? null : "recorded",
      workspace: header.cwd === "" ? null : header.cwd,
      createdAt: header.createdAt === "" ? null : header.createdAt,
      modifiedAt: new Date(info.mtimeMs).toISOString(),
      size: stream.size,
      contentDigest: stream.digest,
      usage,
      absent,
    } satisfies SessionFacts;
  },
};

/**
 * Reads the head records. Every absent value stays absent: a log whose head is truncated or
 * malformed yields a header whose empty fields become explicit reasons.
 */
function readHeader(record: string, header: Header, totals: UsageTotals): void {
  let head: unknown;
  try {
    head = JSON.parse(record);
  } catch {
    totals.unreadable++;
    return;
  }
  if (typeof head !== "object" || head === null) return;
  const fields = head as Record<string, unknown>;
  switch (fields["type"]) {
    case "title":
      // The padded title record is rewritten in place as the title changes, so it supersedes
      // the session record's title.
      if (header.title === "" && typeof fields["title"] === "string") {
        header.title = fields["title"];
      }
      return;
    case "session": {
      if (header.title === "" && typeof fields["title"] === "string") {
        header.title = fields["title"];
      }
      if (typeof fields["cwd"] === "string") header.cwd = fields["cwd"];
      const timestamp = fields["timestamp"];
      if (typeof timestamp === "string") {
        const at = new Date(timestamp);
        if (!Number.isNaN(at.getTime())) header.createdAt = at.toISOString();
      }
      header.done = true;
      return;
    }
    default:
      return;
  }
}

/** Folds one record into the totals; a record that is not JSON is counted and skipped. */
function addUsage(record: string, totals: UsageTotals): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(record);
  } catch {
    totals.unreadable++;
    return;
  }
  if (typeof parsed !== "object" || parsed === null) return;
  const fields = parsed as Record<string, unknown>;
  if (fields["type"] !== "message") return;
  const message = fields["message"];
  if (typeof message !== "object" || message === null) return;
  const body = message as Record<string, unknown>;

  if (body["role"] === "toolResult") {
    if (body["isError"] === true) totals.toolErrors++;
    return;
  }
  if (body["role"] !== "assistant") return;
  totals.assistantTurns++;
  const usage = body["usage"];
  if (typeof usage !== "object" || usage === null) return;
  const turn = usage as Record<string, unknown>;
  totals.turnsWithUsage++;
  if (typeof turn["totalTokens"] === "number") totals.totalTokens += turn["totalTokens"];
  const cost = turn["cost"];
  if (typeof cost === "object" && cost !== null) {
    const total = (cost as Record<string, unknown>)["total"];
    if (typeof total === "number") {
      totals.turnsWithCost++;
      totals.costUsd += total;
    }
  }
}

/**
 * States the totals as the catalog's four numbers, or says why there are none. "OMP recorded
 * nothing" and "this reader summed nothing" are different claims, and only the adapter can
 * tell them apart, so the absence carries which one it was.
 */
function finishUsage(
  totals: UsageTotals,
  oversized: number,
  absent: Record<string, string>,
): SessionFacts["usage"] {
  if (totals.turnsWithUsage === 0) {
    const unreadable = totals.unreadable + oversized;
    absent["usage"] =
      unreadable > 0
        ? `no assistant record carried a readable usage block, and ${unreadable} record(s) of this log could not be parsed at all`
        : "the session log's assistant records carry no usage blocks, which is what a transcript written before OMP recorded per-turn usage looks like";
    return null;
  }
  if (totals.turnsWithUsage < totals.assistantTurns) {
    absent["usage_complete"] =
      `${totals.turnsWithUsage} of ${totals.assistantTurns} assistant turns carried a usage block, so the totals are a floor`;
  }
  return {
    // A turn the harness never priced is not a turn priced at zero, so an unpriced log reports
    // no cost rather than a free session.
    costUsd: totals.turnsWithCost === 0 ? null : totals.costUsd,
    totalTokens: totals.totalTokens,
    turns: totals.assistantTurns,
    toolErrors: totals.toolErrors,
  };
}
