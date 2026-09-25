import { existsSync } from "node:fs";
import { join } from "node:path";

import {
  NO_USAGE,
  type Adapter,
  homeDir,
  sessionRef,
  validSourceId,
  walkablePath,
} from "./identity.ts";

/*
  THE CODEX ADAPTER, ported from v0.4.0:internal/adapter/codex.

  Codex keeps one JSONL rollout log per session under "<root>/sessions/<yyyy>/<mm>/<dd>/",
  plus two host-level state files — "history.jsonl" and "session_index.jsonl" — and an
  "attachments/<id>/" tree referenced from message text. The host state is claimed as one
  dedicated session ("state") rather than duplicated into every rollout.

  ONE DEVIATION FROM THE GO ADAPTER, STATED BECAUSE IT IS DELIBERATE. The Go product read
  every "*.jsonl" anywhere under a configured root's "sessions" tree, which was safe because
  the root came from Codex-specific configuration. Here every path is offered to every
  adapter (so the operator configures roots, not roots-per-harness, and `archive` can claim any
  path restic reports), and OMP also keeps its logs under a directory named "sessions". So a
  rollout is recognized by the date partitioning Codex actually writes —
  "sessions/<yyyy>/<mm>/<dd>/<log>.jsonl" — which is exactly the evidence the Go
  cross-host identifier required before attributing a log to a Codex root, and no OMP session
  can satisfy it.
*/

const HARNESS = "codex";
const ROLLOUT_EXT = ".jsonl";
const HISTORY_FILE = "history.jsonl";
/** The identity of the single host-state session: history.jsonl, with the index beside it. */
const STATE_SOURCE_ID = "state";

/** $CODEX_HOME when the operator relocated the root, otherwise ~/.codex. */
function codexRoots(): string[] {
  const relocated = process.env["CODEX_HOME"]?.trim() ?? "";
  if (relocated !== "") return [relocated];
  const home = homeDir();
  return home === "" ? [] : [join(home, ".codex")];
}

export const codex: Adapter = {
  harness: HARNESS,

  /** Every Codex file worth capturing lives under the single Codex home root. */
  backupRoots() {
    return codexRoots();
  },

  claim(path, exists = existsSync, roots) {
    if (!walkablePath(path)) return null;
    const segments = path.split("/");
    if (segments[segments.length - 1] === HISTORY_FILE) {
      // A history file also needs its archived sibling sessions directory; a filename alone
      // is not evidence of Codex ownership.
      const root = segments.slice(0, -1).join("/");
      if (roots !== undefined && !roots.has(root || "/")) return null;
      return exists(join(root === "" ? "/" : root, "sessions"))
        ? sessionRef(HARNESS, STATE_SOURCE_ID, path)
        : null;
    }
    if (roots !== undefined && !roots.has(segments.slice(0, -5).join("/") || "/")) return null;
    const rollout = rolloutIdentity(segments);
    return rollout === null ? null : sessionRef(HARNESS, rollout, path);
  },

  // Codex writes no usage aggregate this adapter can sum; the Go tree read none either, and a
  // count produced from records whose shape Babel never observed would be a guess.
  usage: () => NO_USAGE,
};

/**
 * The root-relative identity of a rollout path, or null when it is not one. The identity is the
 * root-relative path itself, which is already in the source-id alphabet for every name Codex
 * generates. A path outside it degrades to a digest form, so an unusual on-disk name still gets
 * its transcript archived instead of being dropped or aliased onto another session.
 */
function rolloutIdentity(segments: readonly string[]): string | null {
  const file = segments[segments.length - 1];
  if (file === undefined || !file.endsWith(ROLLOUT_EXT)) return null;
  const day = segments[segments.length - 2];
  const month = segments[segments.length - 3];
  const year = segments[segments.length - 4];
  const sessions = segments[segments.length - 5];
  if (sessions !== "sessions" || year === undefined || month === undefined || day === undefined) {
    return null;
  }
  if (!digits(year, 4) || !digits(month, 2) || !digits(day, 2)) return null;
  const id = [sessions, year, month, day, file].join("/");
  if (validSourceId(id) && id !== STATE_SOURCE_ID) return id;
  return "path-" + new Bun.CryptoHasher("sha256").update(id).digest("hex");
}

function digits(value: string, count: number): boolean {
  if (value.length !== count) return false;
  for (let i = 0; i < count; i++) {
    const code = value.charCodeAt(i);
    if (code < 0x30 || code > 0x39) return false;
  }
  return true;
}
