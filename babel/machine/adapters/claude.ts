import { join } from "node:path";

import { NO_USAGE, type Adapter, homeDir, sessionRef, walkablePath } from "./identity.ts";

/*
  THE CLAUDE CODE ADAPTER, ported from v0.4.0:internal/adapter/claude.

  Claude Code keeps one JSONL transcript per session at
  "<root>/projects/<project-dir>/<session-uuid>.jsonl". The project directory name is a lossy
  encoding of the workspace path — separators and several punctuation characters all collapse
  to "-" — so it names the session's identity and never its workspace, which is read from the
  transcript's own `cwd` (`machine/recall-records.ts`).

  The format is undocumented and unstable. Claude Code records no per-turn usage this adapter
  reads, so a session's spend is absent rather than zero.
*/

const HARNESS = "claude";
const SESSION_EXT = ".jsonl";
const PROJECTS_DIR = "projects";
/** Claude's own identity bound: longer names keep a digest of the original. */
const MAX_SEGMENT = 200;

export const claude: Adapter = {
  harness: HARNESS,

  /** Every Claude Code file worth capturing lives under the single Claude home root. */
  backupRoots() {
    const home = homeDir();
    return home === "" ? [] : [join(home, ".claude")];
  },

  claim(path, _exists, roots) {
    if (!path.endsWith(SESSION_EXT) || !walkablePath(path)) return null;
    const segments = path.split("/");
    const file = segments[segments.length - 1];
    const project = segments[segments.length - 2];
    if (
      segments[segments.length - 3] !== PROJECTS_DIR ||
      project === undefined ||
      file === undefined
    ) {
      return null;
    }
    if (roots !== undefined && !roots.has(segments.slice(0, -3).join("/") || "/")) return null;
    const session = file.slice(0, -SESSION_EXT.length);
    if (session === "") return null;
    return sessionRef(HARNESS, sanitizeSegment(project) + "/" + sanitizeSegment(session), path);
  },

  // Claude Code's on-disk format records no per-turn usage this adapter reads.
  usage: () => NO_USAGE,
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
