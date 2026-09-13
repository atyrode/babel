import { homedir } from "node:os";

/*
  THE HARNESS SOURCE-ADAPTER PORT, ported from internal/adapter (SPEC.md §3).

  An adapter answers three questions about one harness's files and refuses the others':
  which sessions are here (discover), which session is this file the log of (claim), and what
  does this session's own transcript say about itself (describe). Everything an adapter reports
  is read from the live files in place and nothing is copied: durability is restic's job, so a
  description is a best-effort view of one instant, refreshed on every scan.

  Two rules of the Go port carry over unchanged because the product depends on them:

  - An absent value is explained, never synthesized. `absent` carries one reason per field the
    adapter could not observe, so "this session has no title" and "this reader found none" stay
    different claims. The store has no column for those reasons; they are the scan's own
    evidence and reach the operator through the receipt's counts.
  - A title's provenance travels with it. A harness that wrote the title into its own log is
    reporting a fact ("recorded"); a deterministic rule over the transcript is Babel's
    arithmetic ("derived"); a model summary is a guess that cost money ("inferred") and a scan
    never produces one.
*/

export const HARNESSES = ["omp", "codex", "claude"] as const;
export type Harness = (typeof HARNESSES)[number];

export const TITLE_PROVENANCES = ["recorded", "derived", "inferred"] as const;
export type TitleProvenance = (typeof TITLE_PROVENANCES)[number];

/** One discovered session: the identity the catalog files it under, and where its log is. */
export interface SessionRef {
  harness: Harness;
  sourceId: string;
  /** `${harness}/${sourceId}`: the selector every row, edge and fetch names a session by. */
  selector: string;
  primaryPath: string;
}

/**
 * What the harness itself recorded about this session's model spend, summed over its own
 * records. Nothing here is priced or estimated by Babel; a field is null when the harness
 * wrote no such number, and null is not zero.
 */
export interface SessionUsage {
  costUsd: number | null;
  totalTokens: number | null;
  turns: number | null;
  toolErrors: number | null;
}

/** One best-effort view of a session, read from its live files. */
export interface SessionFacts {
  ref: SessionRef;
  title: string | null;
  titleProvenance: TitleProvenance | null;
  workspace: string | null;
  /** ISO 8601, UTC. */
  createdAt: string | null;
  modifiedAt: string | null;
  /** The bytes read, and their canonical digest: "sha256:<64 lowercase hex>". */
  size: number;
  contentDigest: string;
  usage: SessionUsage | null;
  /** field → why this adapter could not observe it. */
  absent: Record<string, string>;
}

export interface Adapter {
  readonly harness: Harness;
  /** The version of this adapter's discovery and description behaviour. */
  readonly schema: number;
  /** Where this harness keeps its sessions on this machine, whether or not they exist. */
  defaultRoots(): string[];
  /** What a backup must capture to be able to restore a session, closure included. */
  backupRoots(): string[];
  /** The session this path is the primary log of, or null when it is not this harness's. */
  claim(path: string): SessionRef | null;
  discover(roots: readonly string[]): Promise<SessionRef[]>;
  describe(ref: SessionRef): Promise<SessionFacts>;
}

/** The one place the selector is spelled. */
export function sessionRef(harness: Harness, sourceId: string, primaryPath: string): SessionRef {
  return { harness, sourceId, selector: `${harness}/${sourceId}`, primaryPath };
}

const MAX_SOURCE_ID_BYTES = 512;
/** Bounds one segment so a two-segment id always fits MAX_SOURCE_ID_BYTES. */
const MAX_SEGMENT = 128;

/**
 * Source identities are one or more "/"-separated segments of [A-Za-z0-9._-], no empty, "."
 * or ".." segment, at most 512 bytes — the alphabet every consumer of a selector may assume.
 */
export function validSourceId(id: string): boolean {
  if (id.length === 0 || Buffer.byteLength(id) > MAX_SOURCE_ID_BYTES) return false;
  for (const segment of id.split("/")) {
    if (!validSegment(segment)) return false;
  }
  return true;
}

function validSegment(segment: string): boolean {
  if (segment === "" || segment === "." || segment === "..") return false;
  for (let i = 0; i < segment.length; i++) {
    if (!segmentCharOk(segment.charCodeAt(i))) return false;
  }
  return true;
}

function segmentCharOk(code: number): boolean {
  return (
    (code >= 0x61 && code <= 0x7a) || // a-z
    (code >= 0x41 && code <= 0x5a) || // A-Z
    (code >= 0x30 && code <= 0x39) || // 0-9
    code === 0x2e || // .
    code === 0x5f || // _
    code === 0x2d // -
  );
}

/**
 * Renders one on-disk name as a source-id segment. A name already in the alphabet is used
 * verbatim, which keeps ids readable and stable; anything else has its invalid bytes replaced
 * and a digest of the original appended, so two distinct names keep two distinct ids and the
 * same name keeps the same id across runs.
 */
export function idSegment(name: string): string {
  if (name.length <= MAX_SEGMENT && validSegment(name)) return name;
  const suffix = "-" + new Bun.CryptoHasher("sha256").update(name).digest("hex").slice(0, 8);
  const keep = MAX_SEGMENT - suffix.length;
  let out = "";
  for (let i = 0; i < name.length && out.length < keep; i++) {
    out += segmentCharOk(name.charCodeAt(i)) ? name[i] : "-";
  }
  return out + suffix;
}

/**
 * Reports whether a path is one a walk of this machine could have produced. A readdir never
 * yields an empty, "." or ".." component, so a path carrying one names a tree no adapter
 * reached, and its components are not a trustworthy identity — idSegment would happily
 * sanitize ".." into the alphabet.
 */
export function walkablePath(path: string): boolean {
  const segments = path.startsWith("/") ? path.slice(1).split("/") : path.split("/");
  for (const segment of segments) {
    if (segment === "" || segment === "." || segment === "..") return false;
  }
  return true;
}

/**
 * The home directory the roots hang off, as the process sees it NOW: $HOME when the job set
 * one, the passwd entry otherwise. The environment is read per call and never cached, because
 * a job's child is given its own HOME and a scan's roots belong to the machine it runs on —
 * `os.homedir()` alone answers from where the process started.
 */
export function homeDir(): string {
  const declared = process.env["HOME"]?.trim() ?? "";
  return declared !== "" ? declared : homedir().trim();
}
