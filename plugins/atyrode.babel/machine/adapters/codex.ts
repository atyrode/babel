import { readdir, stat } from "node:fs/promises";
import { existsSync } from "node:fs";
import { join } from "node:path";

import {
  type Adapter,
  type SessionFacts,
  type SessionRef,
  homeDir,
  sessionRef,
  validSourceId,
  walkablePath,
} from "./identity.ts";
import { readRecords } from "./records.ts";
import {
  MAX_REQUEST_CANDIDATES,
  NO_THREAD_SOURCE,
  type ThreadSource,
  type TitleBasis,
  decodeThreadSource,
  deriveTitle,
  injectedBlock,
  messageText,
} from "./codex-title.ts";

/*
  THE CODEX ADAPTER, ported from internal/adapter/codex.

  Codex keeps one JSONL rollout log per session under "<root>/sessions/<yyyy>/<mm>/<dd>/",
  plus two host-level state files — "history.jsonl" and "session_index.jsonl" — and an
  "attachments/<id>/" tree referenced from message text. The host state is described as one
  dedicated session ("state") rather than duplicated into every rollout.

  ONE DEVIATION FROM THE GO DISCOVERY, STATED BECAUSE IT IS DELIBERATE. `babel scan` read
  every "*.jsonl" anywhere under a configured root's "sessions" tree, which was safe because
  the root came from Codex-specific configuration. Here one flat root list is offered to every
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
export const STATE_SOURCE_ID = "state";

/** $CODEX_HOME when the operator relocated the root, otherwise ~/.codex. */
function codexRoots(): string[] {
  const relocated = process.env["CODEX_HOME"]?.trim() ?? "";
  if (relocated !== "") return [relocated];
  const home = homeDir();
  return home === "" ? [] : [join(home, ".codex")];
}

interface RolloutScan {
  cwd: string;
  lastTurnCwd: string;
  first: string;
  last: string;
  malformed: number;
  source: ThreadSource;
  request: string;
  requestFallback: string;
  fallbackTried: number;
}

export const codex: Adapter = {
  harness: HARNESS,
  schema: 1,

  /** $CODEX_HOME when the operator relocated it, otherwise ~/.codex. */
  defaultRoots() {
    return codexRoots();
  },

  /** Every Codex file worth capturing lives under the single Codex home root. */
  backupRoots() {
    return codexRoots();
  },

  claim(path) {
    if (!walkablePath(path)) return null;
    const segments = path.split("/");
    if (segments[segments.length - 1] === HISTORY_FILE) {
      // The host state is only Codex's when a "sessions" tree sits beside it: a listing cannot
      // say which ancestor was a root, and "history.jsonl" is not a name only Codex uses.
      const root = segments.slice(0, -1).join("/");
      return existsSync(join(root === "" ? "/" : root, "sessions"))
        ? sessionRef(HARNESS, STATE_SOURCE_ID, path)
        : null;
    }
    const rollout = rolloutIdentity(segments);
    return rollout === null ? null : sessionRef(HARNESS, rollout, path);
  },

  async discover(roots) {
    const found: SessionRef[] = [];
    const seen = new Set<string>();
    for (const root of roots) {
      const sessions = join(root, "sessions");
      const history = join(root, HISTORY_FILE);
      const historyInfo = await stat(history).catch(() => null);
      const sessionsInfo = await stat(sessions).catch(() => null);
      if (!sessionsInfo?.isDirectory()) continue;
      if (historyInfo?.isFile() && !seen.has(STATE_SOURCE_ID)) {
        seen.add(STATE_SOURCE_ID);
        found.push(sessionRef(HARNESS, STATE_SOURCE_ID, history));
      }
      // Codex partitions rollouts by date, three levels below the sessions tree.
      for (const year of await readdir(sessions, { withFileTypes: true }).catch(() => [])) {
        if (!year.isDirectory() || !digits(year.name, 4)) continue;
        for (const month of await readdir(join(sessions, year.name), { withFileTypes: true }).catch(() => [])) {
          if (!month.isDirectory() || !digits(month.name, 2)) continue;
          for (const day of await readdir(join(sessions, year.name, month.name), { withFileTypes: true }).catch(() => [])) {
            if (!day.isDirectory() || !digits(day.name, 2)) continue;
            const dayDir = join(sessions, year.name, month.name, day.name);
            for (const entry of await readdir(dayDir, { withFileTypes: true }).catch(() => [])) {
              if (!entry.isFile() || !entry.name.endsWith(ROLLOUT_EXT)) continue;
              const id = rolloutSourceId(["sessions", year.name, month.name, day.name, entry.name]);
              if (id === null || seen.has(id)) continue;
              seen.add(id);
              found.push(sessionRef(HARNESS, id, join(dayDir, entry.name)));
            }
          }
        }
      }
    }
    found.sort((a, b) => (a.sourceId < b.sourceId ? -1 : a.sourceId > b.sourceId ? 1 : 0));
    return found;
  },

  async describe(ref) {
    const info = await stat(ref.primaryPath);
    const absent: Record<string, string> = {};
    const state = ref.sourceId === STATE_SOURCE_ID;
    const scan: RolloutScan = {
      cwd: "",
      lastTurnCwd: "",
      first: "",
      last: "",
      malformed: 0,
      source: NO_THREAD_SOURCE,
      request: "",
      requestFallback: "",
      fallbackTried: 0,
    };

    const stream = await readRecords(ref.primaryPath, (record) => {
      if (state) {
        observeHistory(record, scan);
        return;
      }
      observeRollout(record, scan);
    });

    let title = "";
    let basis: TitleBasis | null = null;
    if (state) {
      absent["title"] =
        "codex host state is a per-host log, not a session with a request to name it";
      absent["workspace"] = "codex host state is not workspace-scoped";
    } else {
      const derived = deriveTitle(scan);
      title = derived.title;
      basis = derived.basis;
      if (title === "") {
        absent["title"] = "codex session logs record no title, and none could be derived: " + derived.reason;
      }
      if (scan.cwd === "" && scan.lastTurnCwd === "") {
        absent["workspace"] = "no session_meta or turn_context record exposed a working directory";
      }
    }
    if (scan.first === "") absent["created_at"] = "no record exposed a parsable timestamp";
    const unreadable = scan.malformed + stream.oversized;
    if (unreadable > 0) {
      absent["records"] = `${unreadable} of this log's ${stream.records} records could not be read`;
    }
    // Codex writes no usage aggregate this adapter can sum; the Go tree read none either, and
    // a count produced from records whose shape Babel never observed would be a guess.
    absent["usage"] = "codex records no per-turn usage block this adapter reads";
    if (basis !== null) absent["title_basis"] = basis;

    const workspace = state ? "" : scan.cwd !== "" ? scan.cwd : scan.lastTurnCwd;
    return {
      ref,
      title: title === "" ? null : title,
      // Deterministic, offline, from the log's own values: Babel's arithmetic, never Codex's
      // record of a title it does not keep.
      titleProvenance: title === "" ? null : "derived",
      workspace: workspace === "" ? null : workspace,
      createdAt: scan.first === "" ? null : scan.first,
      // A rollout's own records date it; the file's mtime is the fallback.
      modifiedAt: scan.last !== "" ? scan.last : new Date(info.mtimeMs).toISOString(),
      size: stream.size,
      contentDigest: stream.digest,
      usage: null,
      absent,
    } satisfies SessionFacts;
  },
};

/** The root-relative identity of a rollout path, or null when it is not one. */
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
  return rolloutSourceId([sessions, year, month, day, file]);
}

/**
 * The identity of a rollout is its root-relative path, which is already in the source-id
 * alphabet for every name Codex generates. A path outside it degrades to a digest form, so an
 * unusual on-disk name still gets its transcript archived instead of being dropped or aliased
 * onto another session.
 */
function rolloutSourceId(relative: readonly string[]): string | null {
  const id = relative.join("/");
  if (validSourceId(id) && id !== STATE_SOURCE_ID) return id;
  const digest = new Bun.CryptoHasher("sha256").update(id).digest("hex");
  return "path-" + digest;
}

function digits(value: string, count: number): boolean {
  if (value.length !== count) return false;
  for (let i = 0; i < count; i++) {
    const code = value.charCodeAt(i);
    if (code < 0x30 || code > 0x39) return false;
  }
  return true;
}

/** Extracts session metadata, the title evidence and the observed span from one rollout record. */
function observeRollout(record: string, scan: RolloutScan): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(record);
  } catch {
    scan.malformed++;
    return;
  }
  if (typeof parsed !== "object" || parsed === null) return;
  const fields = parsed as Record<string, unknown>;
  const timestamp = fields["timestamp"];
  if (typeof timestamp === "string") observeTime(scan, timestamp);
  const payload = fields["payload"];
  const body = typeof payload === "object" && payload !== null ? (payload as Record<string, unknown>) : null;

  switch (fields["type"]) {
    case "session_meta": {
      if (body === null) {
        scan.malformed++;
        return;
      }
      if (scan.cwd === "" && typeof body["cwd"] === "string") scan.cwd = body["cwd"];
      if (scan.source === NO_THREAD_SOURCE) scan.source = decodeThreadSource(body["source"]);
      if (typeof body["timestamp"] === "string") observeTime(scan, body["timestamp"]);
      return;
    }
    case "turn_context": {
      if (body !== null && typeof body["cwd"] === "string" && body["cwd"] !== "") {
        scan.lastTurnCwd = body["cwd"];
      }
      return;
    }
    case "event_msg": {
      // The front end's channel: an injected context block never appears here, which is what
      // makes this the primary title rule rather than a filter over the model's input stream.
      if (scan.request !== "" || body === null || body["type"] !== "user_message") return;
      scan.request = messageText(body);
      return;
    }
    case "response_item": {
      // The model's input stream, consulted only when the log emits no delivered turn; here
      // the injected-block guard does the work the channel would otherwise do.
      if (scan.requestFallback !== "" || scan.fallbackTried >= MAX_REQUEST_CANDIDATES) return;
      if (body === null || body["type"] !== "message" || body["role"] !== "user") return;
      const text = messageText(body);
      scan.fallbackTried++;
      if (text.trim() === "" || injectedBlock(text)) return;
      scan.requestFallback = text;
      return;
    }
    default:
      return;
  }
}

/**
 * The observable span of the host history log. Only the timestamp is interpreted: the recorded
 * prompt text is named for archival and never read.
 */
function observeHistory(record: string, scan: RolloutScan): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(record);
  } catch {
    scan.malformed++;
    return;
  }
  if (typeof parsed !== "object" || parsed === null) return;
  const seconds = (parsed as Record<string, unknown>)["ts"];
  if (typeof seconds !== "number" || seconds <= 0) return;
  observeTime(scan, new Date(seconds * 1000).toISOString());
}

function observeTime(scan: RolloutScan, value: string): void {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return;
  const iso = at.toISOString();
  if (scan.first === "" || iso < scan.first) scan.first = iso;
  if (scan.last === "" || iso > scan.last) scan.last = iso;
}
