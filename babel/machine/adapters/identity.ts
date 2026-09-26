import { homedir } from "node:os";
import { join } from "node:path";
import {
  HARNESSES,
  TITLE_PROVENANCES,
  type Harness,
  type TitleProvenance,
} from "../../contract.ts";

/*
  THE HARNESS SOURCE-ADAPTER PORT, ported from v0.4.0:internal/adapter (SPEC.md §3).

  An adapter answers three questions about one harness and refuses the others' files: which
  session a path is the primary log of (`claim`), what a backup must capture to restore that
  harness's sessions whole (`backupRoots`), and what the harness's own records say it spent
  (`usage`, a fold over parsed records). It opens no session log: `claim` is asked of the
  paths a restic listing names, and the usage fold is fed an archived capture's normalized,
  redacted stream (`machine/session-facts.ts`). A session's title and workspace are not an
  adapter's question: Recall's metadata rule reads them from that same stream
  (`machine/recall-records.ts`), so a catalog row and a Recall answer state one reading of one
  capture.

  Two rules of the Go port carry over because the product depends on them:

  - An absent value is never synthesized. A total the harness did not write is null, and null
    is not zero: a harness whose records carry no usage folds to no totals (`NO_USAGE`), never
    to a free session.
  - A title's provenance travels with it. A harness that wrote the title into its own log is
    reporting a fact ("recorded"); a deterministic rule over the transcript is Babel's
    arithmetic ("derived", `codex-title.ts`); a model summary is a guess that cost money
    ("inferred") and no machine row carries one — the contract's session row refuses it.
*/

export { HARNESSES, TITLE_PROVENANCES, type Harness, type TitleProvenance };

/** One claimed session: the identity the catalog files it under, and where its log is. */
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

/**
 * THE HARNESS'S OWN USAGE, folded one parsed record at a time. It reads records rather than a
 * file, so it sums a capture's normalized, redacted stream (`machine/session-facts.ts`, #453):
 * numbers survive redaction, and a total is the same total whichever stream carried it.
 */
export interface UsageFold {
  record(fields: Record<string, unknown>): void;
  /** The totals, or null when the harness recorded no usage this fold reads. */
  finish(): SessionUsage | null;
}

/** The fold of a harness that records no usage an adapter reads: every total is absent. */
export const NO_USAGE: UsageFold = {
  record() {},
  finish: () => null,
};

export interface Adapter {
  readonly harness: Harness;
  /** What a backup must capture to be able to restore a session, closure included. */
  backupRoots(): string[];
  /** The session this path is the primary log of. Archive callers supply listing existence
   *  and recorded roots; omitted arguments retain the live filesystem behavior. */
  claim(
    path: string,
    exists?: (path: string) => boolean,
    roots?: ReadonlySet<string>,
  ): SessionRef | null;
  /** A fresh fold of this harness's own usage over parsed records. */
  usage(): UsageFold;
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
 * a job's child is given its own HOME and an archive's roots belong to the machine it runs on —
 * `os.homedir()` alone answers from where the process started.
 */
export function homeDir(): string {
  const declared = process.env["HOME"]?.trim() ?? "";
  return declared !== "" ? declared : homedir().trim();
}

/**
 * Babel's own analysis-session root, `<data dir>/babel/analysis` — where a run's transcript is
 * written, one directory per run and one "*.babel.jsonl" per supervised job inside it.
 *
 * The rule is the data directory's own: XDG_DATA_HOME when set, else ~/.local/share, read per
 * call for the reason `homeDir` is (a job's child is given its own environment).
 */
export function babelAnalysisRoot(): string {
  const declared = process.env["XDG_DATA_HOME"]?.trim() ?? "";
  const base = declared !== "" ? declared : join(homeDir(), ".local", "share");
  return join(base, "babel", "analysis");
}

/** The extension a Babel run's own transcript is named with; part of the identity, not decor. */
const BABEL_SESSION_EXT = ".babel.jsonl";

/**
 * Whether this primary log is one of BABEL'S OWN runs, by layout alone.
 *
 * Two tests, because either alone is reachable without the other. Under the analysis root a log
 * is Babel's whatever it is called; and the compound extension identifies one anywhere, which
 * matters because a run's transcript that has been moved, restored out of a snapshot or archived
 * under a root the operator named explicitly is the same session — and because OMP's own layout
 * is this one ("*.jsonl" a directory below a root), so a plain extension under an explicit root
 * would let the OMP adapter claim Babel's transcripts as OMP's.
 *
 * It is a PATH rule: it costs no read, so `catalog` and `prepare` can honour it from a listing
 * before a single byte of the capture is fetched.
 */
export function babelOwnLog(path: string): boolean {
  if (path.endsWith(BABEL_SESSION_EXT)) return true;
  const root = babelAnalysisRoot();
  return path.startsWith(root.endsWith("/") ? root : `${root}/`);
}
