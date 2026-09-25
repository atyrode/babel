import { join } from "node:path";

import {
  type Adapter,
  type SessionUsage,
  homeDir,
  idSegment,
  sessionRef,
  validSourceId,
  walkablePath,
} from "./identity.ts";

/*
  THE OMP ADAPTER, ported from v0.4.0:internal/adapter/omp.

  The layout it claims:

    <data root>/agent/sessions/<project>/<stem>.jsonl   primary log
    <data root>/agent/sessions/<project>/<stem>/...      sibling artifacts
    <data root>/agent/blobs/<64 hex>[.<ext>]             blob store

  A session log is a "*.jsonl" one directory below a sessions root. That single rule is what
  keeps a session's sibling artifact tree — a directory sharing the log's stem, whose own JSONL
  files sit one level deeper — from being mistaken for sessions of its own, and `claim` applies
  it to a path from this machine and a path from a snapshot's listing alike, so both get the
  same identity.

  The head of a log is a fixed-width padded {"type":"title"} record rewritten in place as the
  title changes, then a {"type":"session"} record carrying the id, the creation timestamp and
  the workspace cwd; those are Recall's metadata rule's to read (`machine/recall-records.ts`).
  OMP is the one harness of the three that writes a usage block into every assistant record, so
  it is the one whose spend Babel can sum without inferring anything (issue #89): the numbers
  below are OMP's own arithmetic over its own rate card, repeated.
*/

const HARNESS = "omp";
const SESSION_EXT = ".jsonl";

interface UsageTotals {
  assistantTurns: number;
  turnsWithUsage: number;
  turnsWithCost: number;
  costUsd: number;
  totalTokens: number;
  toolErrors: number;
}

export const omp: Adapter = {
  harness: HARNESS,

  /*
    Two trees beyond the sessions root. The content-addressed blob store, because referenced
    blobs live outside the session trees and a backup without them could never restore a
    session whole. And the collaboration transcripts, which sit beside "agent" and are written
    in the same record language: they are session history `claim` never names a session, so
    leaving them out of a backup would drop transcripts outright.
  */
  backupRoots() {
    const home = homeDir();
    if (home === "") return [];
    const agent = join(home, ".omp", "agent");
    return [join(agent, "sessions"), join(agent, "blobs"), join(home, ".omp", "collab")];
  },

  claim(path, _exists, roots) {
    if (!path.endsWith(SESSION_EXT) || !walkablePath(path)) return null;
    const segments = path.split("/");
    const n = segments.length;
    const project = segments[n - 2];
    const file = segments[n - 1];
    if (n < 3 || segments[n - 3] !== "sessions" || project === undefined || file === undefined) {
      return null;
    }
    if (roots !== undefined && !roots.has(segments.slice(0, -2).join("/") || "/")) return null;
    const id = idSegment(project) + "/" + idSegment(file.slice(0, -SESSION_EXT.length));
    return validSourceId(id) ? sessionRef(HARNESS, id, path) : null;
  },

  usage() {
    const totals: UsageTotals = {
      assistantTurns: 0,
      turnsWithUsage: 0,
      turnsWithCost: 0,
      costUsd: 0,
      totalTokens: 0,
      toolErrors: 0,
    };
    return {
      record: (fields) => addUsage(fields, totals),
      finish: () => usageOf(totals),
    };
  },
};

/** Folds one record into the totals: an assistant turn's usage block, or a failed tool result. */
function addUsage(fields: Record<string, unknown>, totals: UsageTotals): void {
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
 * The catalog's four numbers, or null when no assistant turn carried a usage block: a log
 * written before OMP recorded per-turn usage states no spend, never a session that cost nothing.
 */
function usageOf(totals: UsageTotals): SessionUsage | null {
  if (totals.turnsWithUsage === 0) return null;
  return {
    // A turn the harness never priced is not a turn priced at zero, so an unpriced log reports
    // no cost rather than a free session.
    costUsd: totals.turnsWithCost === 0 ? null : totals.costUsd,
    totalTokens: totals.totalTokens,
    turns: totals.assistantTurns,
    toolErrors: totals.toolErrors,
  };
}
