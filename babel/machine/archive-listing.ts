/*
  WHICH ARCHIVED FILES ARE SESSIONS, spelled once for every reader of the archive (#453).

  Recall and the catalog both read a snapshot by listing it and offering each file to the same
  adapters that name live sessions, so a selector means the same thing whichever of them found
  it. Two facts make that more than a loop over `ls`:

  - The roots are the SNAPSHOT's, never this machine's. An adapter's layout rule is anchored at
    a root (`~/.omp/agent/sessions`, `~/.codex`, `~/.claude`), and the root a capture was taken
    under is the path set restic recorded for it, whatever machine is reading it now.
  - Existence is the LISTING's, never this machine's filesystem. A Codex history file is a
    session only beside its `sessions/` directory, and restic lists a directory's members in
    name order, so `history.jsonl` precedes `sessions/`. A claim that asks whether a sibling
    exists is therefore decided once the whole listing is known, and not before.
*/

import { CaptureInstantSchema } from "../contract.ts";
import { claim, type SessionRef } from "./adapters/index.ts";
import { BABEL_TAG, type ArchivedEntry, type Repo, type Snapshot } from "./restic.ts";

/** The longest archived path a capture may name (`ArchivePathSchema`); a longer one is listed
 *  and counted, and names no session. */
const MAX_ARCHIVED_PATH = 4096;

/** The longest host label a snapshot may carry and still be read (`ArchiveLabelSchema`). */
const MAX_LABEL = 128;

/**
 * Whether a snapshot is one Babel reads as transcripts: tagged exactly `babel` (so a
 * `babel-store` backup of the hub's own store is never read as sessions), named by a full id,
 * timed, and attributed to a label of 1–128 characters.
 */
export function babelSnapshot(snapshot: Snapshot): boolean {
  return (
    snapshot.tags.includes(BABEL_TAG) &&
    /^[0-9a-f]{64}$/.test(snapshot.id) &&
    Number.isFinite(Date.parse(snapshot.time)) &&
    snapshot.host.length > 0 &&
    snapshot.host.length <= MAX_LABEL
  );
}

/**
 * An instant as restic spells it — the backing machine's own offset, nanoseconds — in the one
 * spelling a catalogued row carries ({@link CaptureInstantSchema}: UTC, three fractional
 * digits), or null for a value that is no instant at or after the epoch. The hub compares these
 * strings as text, so an offset left in place would order a capture by the zone it was taken in.
 */
export function captureInstant(time: string): string | null {
  const at = Date.parse(time);
  if (!Number.isFinite(at) || at <= 0) return null;
  const spelled = new Date(at).toISOString();
  return CaptureInstantSchema.safeParse(spelled).success ? spelled : null;
}

/** What {@link capturesOf} reports while it lists one snapshot. */
export interface ListingVisitor {
  /** Every node the listing holds, before any rule is applied: the listing's own cost. */
  entry(): void;
  /** One primary log an adapter claims, with the node restic listed for it. */
  capture(session: SessionRef, node: ArchivedEntry): void;
}

/**
 * Lists one snapshot and reports every primary log in it that an adapter claims.
 *
 * A claim is made against the snapshot's own roots and the listing's own existence. A claim
 * that asks about a sibling is deferred until the listing has ended, and decided then against
 * the directories it held. The same session may be reported more than once when two paths in
 * one snapshot claim it; which one stands is the caller's rule.
 */
export async function capturesOf(
  repo: Pick<Repo, "lsTo">,
  snapshot: Pick<Snapshot, "id" | "paths">,
  visit: ListingVisitor,
): Promise<void> {
  const roots = new Set(snapshot.paths.map((path) => path.replace(/\/+$/, "") || "/"));
  const directories = new Set<string>();
  const deferred: ArchivedEntry[] = [];
  await repo.lsTo(snapshot.id, (node) => {
    visit.entry();
    if (node.path.length > MAX_ARCHIVED_PATH) return;
    if (node.type === "dir") {
      directories.add(node.path);
      return;
    }
    if (node.type !== "file") return;
    let asked = false;
    const session = claim(
      node.path,
      () => {
        asked = true;
        return false;
      },
      roots,
    );
    if (asked) deferred.push(node);
    else if (session !== null) visit.capture(session, node);
  });
  for (const node of deferred) {
    const session = claim(node.path, (path) => directories.has(path), roots);
    if (session !== null) visit.capture(session, node);
  }
}
