import { readdir, stat } from "node:fs/promises";
import { join } from "node:path";

import {
  type Adapter,
  type SessionFacts,
  type SessionRef,
  homeDir,
  sessionRef,
  walkablePath,
} from "./identity.ts";
import { readRecords } from "./records.ts";

/*
  THE CLAUDE CODE ADAPTER, ported from internal/adapter/claude.

  Claude Code keeps one JSONL transcript per session at
  "<root>/projects/<project-dir>/<session-uuid>.jsonl". The project directory name is a lossy
  encoding of the workspace path — separators and several punctuation characters all collapse
  to "-" — so the absolute workspace cannot be recovered from it, which is why a transcript's
  own `cwd` is preferred and the directory name is reported as the fallback it is.

  The format is undocumented and unstable, so title, workspace, timestamps and repository
  state are all allowed to be unavailable and every absence carries its reason. Claude Code
  records no per-turn usage this adapter reads, so a session's spend is absent rather than
  zero.
*/

const HARNESS = "claude";
const SESSION_EXT = ".jsonl";
const PROJECTS_DIR = "projects";
/** Claude's own identity bound: longer names keep a digest of the original. */
const MAX_SEGMENT = 200;

/** The one Claude Code home root, as this machine's home names it. */
function claudeRoots(): string[] {
  const home = homeDir();
  return home === "" ? [] : [join(home, ".claude")];
}

interface TranscriptScan {
  title: string;
  cwd: string;
  cwdConflict: boolean;
  first: string;
  last: string;
  malformed: number;
}

export const claude: Adapter = {
  harness: HARNESS,
  schema: 1,

  defaultRoots() {
    return claudeRoots();
  },

  /** Every Claude Code file worth capturing lives under the single Claude home root. */
  backupRoots() {
    return claudeRoots();
  },

  claim(path) {
    if (!path.endsWith(SESSION_EXT) || !walkablePath(path)) return null;
    const segments = path.split("/");
    const file = segments[segments.length - 1];
    const project = segments[segments.length - 2];
    if (segments[segments.length - 3] !== PROJECTS_DIR || project === undefined || file === undefined) {
      return null;
    }
    const session = file.slice(0, -SESSION_EXT.length);
    if (session === "") return null;
    return sessionRef(HARNESS, sanitizeSegment(project) + "/" + sanitizeSegment(session), path);
  },

  async discover(roots) {
    const found: SessionRef[] = [];
    const seen = new Set<string>();
    for (const root of roots) {
      const projectsPath = join(root, PROJECTS_DIR);
      for (const project of await readdir(projectsPath, { withFileTypes: true }).catch(() => [])) {
        if (!project.isDirectory()) continue;
        const projectDir = join(projectsPath, project.name);
        for (const entry of await readdir(projectDir, { withFileTypes: true }).catch(() => [])) {
          if (entry.isDirectory() || !entry.name.endsWith(SESSION_EXT)) continue;
          const session = entry.name.slice(0, -SESSION_EXT.length);
          if (session === "") continue;
          const id = sanitizeSegment(project.name) + "/" + sanitizeSegment(session);
          if (seen.has(id)) continue;
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
    const scan: TranscriptScan = {
      title: "",
      cwd: "",
      cwdConflict: false,
      first: "",
      last: "",
      malformed: 0,
    };
    const stream = await readRecords(ref.primaryPath, (record) => observe(record, scan));

    const absent: Record<string, string> = {};
    if (scan.title === "") {
      absent["title"] =
        "the transcript contains no ai-title record and the Claude Code format exposes no other session title";
    }
    let workspace = scan.cwd;
    if (workspace !== "" && scan.cwdConflict) {
      absent["workspace"] =
        "the transcript recorded several distinct cwd values; the first observed value was used";
    }
    if (workspace === "") {
      // The project directory name is all that is left, and it is an encoding rather than a
      // path: it is reported so a reader can tell which sessions belong together, with the
      // reason stating that no absolute workspace can be recovered from it.
      const project = ref.primaryPath.split("/").at(-2) ?? "";
      workspace = project;
      absent["workspace"] =
        project === ""
          ? "the transcript recorded no cwd and the session has no project directory name to fall back on"
          : "no transcript record carried a cwd, so the value is the Claude Code project directory name; that encoding is lossy and the absolute workspace path cannot be recovered from it";
    }
    if (scan.first === "") {
      absent["created_at"] = "the transcript exposes no parseable record timestamp";
    }
    const unreadable = scan.malformed + stream.oversized;
    if (unreadable > 0) {
      absent["records"] = `${unreadable} of this transcript's ${stream.records} records could not be read`;
    }
    absent["usage"] = "the Claude Code on-disk format records no per-turn usage this adapter reads";

    return {
      ref,
      // The ai-title record is in the transcript Claude Code wrote, so Babel is repeating a
      // recorded value rather than deriving one.
      title: scan.title === "" ? null : scan.title,
      titleProvenance: scan.title === "" ? null : "recorded",
      workspace: workspace === "" ? null : workspace,
      createdAt: scan.first === "" ? null : scan.first,
      modifiedAt: scan.last !== "" ? scan.last : new Date(info.mtimeMs).toISOString(),
      size: stream.size,
      contentDigest: stream.digest,
      usage: null,
      absent,
    } satisfies SessionFacts;
  },
};

/**
 * Maps one on-disk name onto a single valid source-id segment: characters outside
 * [A-Za-z0-9._-] become "-", and degenerate or over-long names keep a digest of the original
 * so two distinct names stay distinct and one name keeps one identity across runs.
 */
function sanitizeSegment(name: string): string {
  let out = "";
  for (let i = 0; i < name.length; i++) {
    const code = name.charCodeAt(i);
    const ok =
      (code >= 0x61 && code <= 0x7a) ||
      (code >= 0x41 && code <= 0x5a) ||
      (code >= 0x30 && code <= 0x39) ||
      code === 0x2e ||
      code === 0x5f ||
      code === 0x2d;
    out += ok ? name[i] : "-";
  }
  if (out === "" || out === "." || out === "..") return "x-" + nameDigest(name);
  if (out.length > MAX_SEGMENT) {
    const digest = nameDigest(name);
    return out.slice(0, MAX_SEGMENT - digest.length - 1) + "-" + digest;
  }
  return out;
}

function nameDigest(name: string): string {
  return new Bun.CryptoHasher("sha256").update(name).digest("hex").slice(0, 12);
}

/** Folds one transcript record into the scan; an unparseable line is counted and skipped. */
function observe(record: string, scan: TranscriptScan): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(record);
  } catch {
    scan.malformed++;
    return;
  }
  if (typeof parsed !== "object" || parsed === null) return;
  const fields = parsed as Record<string, unknown>;
  const title = fields["aiTitle"];
  if (typeof title === "string" && title !== "") scan.title = title;
  const cwd = fields["cwd"];
  if (typeof cwd === "string" && cwd !== "") {
    if (scan.cwd === "") scan.cwd = cwd;
    else if (scan.cwd !== cwd) scan.cwdConflict = true;
  }
  const timestamp = fields["timestamp"];
  if (typeof timestamp !== "string" || timestamp === "") return;
  const at = new Date(timestamp);
  if (Number.isNaN(at.getTime())) return;
  const iso = at.toISOString();
  if (scan.first === "" || iso < scan.first) scan.first = iso;
  if (scan.last === "" || iso > scan.last) scan.last = iso;
}
