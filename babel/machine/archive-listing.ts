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

  A snapshot never changes, so neither does its listing: only the rules that claim from it can.
  Recall reads the whole archive on every request, and a large archive's listings are its cost —
  minutes, repeated — so a listing may be REMEMBERED (`listingMemory`): the node count and the
  claims `capturesOf` reported, nothing a listing would not already say, kept beside Recall's
  index in its owner-private cache and keyed on the snapshot and the claim rules. It is raw: no
  policy, subject or request filter has touched it, so every one of them still applies on replay.
  It is convenience state; an absent, unreadable or foreign one costs one listing.
*/

import { chmod, mkdir, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { gunzipSync, gzipSync } from "node:zlib";
import { z } from "zod";
import { CaptureInstantSchema, HarnessSchema, SnapshotIdSchema } from "../contract.ts";
import { CLAIM_RULES, claim, sessionRef, type SessionRef } from "./adapters/index.ts";
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

/** What one listing of a snapshot reported through {@link capturesOf}. */
export interface SnapshotListing {
  /** Every node it held. */
  readonly entries: number;
  /** Every claim, in the order it was reported. */
  readonly captures: readonly { readonly session: SessionRef; readonly node: ArchivedEntry }[];
}

/** Snapshot listings remembered in one owner-private directory. */
export interface ListingMemory {
  /** The listing remembered for this snapshot under the current rules, or null when there is
   *  none, it cannot be read, or it was claimed under other rules. Never throws. */
  recall(snapshotId: string): Promise<SnapshotListing | null>;
  /** Remembers one completed listing in place of any before it. Never throws: a listing that
   *  cannot be kept is listed again next time. */
  keep(snapshotId: string, listing: SnapshotListing): Promise<void>;
}

/**
 * What a remembered listing is keyed on besides its snapshot: the rules that turned it into
 * captures. The first part is {@link capturesOf}'s own (the path bound, deferred sibling
 * claims) and this file's spelling of a memory — bump it whenever either changes — and the
 * second is the adapters' ({@link CLAIM_RULES}). Babel's release is deliberately not part of
 * it: a release that changes no rule would otherwise list the whole archive again.
 */
const LISTING_RULES = `babel.archive-listing/1+${CLAIM_RULES}`;

/** The largest remembered listing, as JSON text: some hundred thousand captures. A listing
 *  past it is not kept, and is listed again on every read. */
const MAX_REMEMBERED_BYTES = 32 * 1024 * 1024;

/** One remembered listing. A capture is `[harness, sourceId, path, size, modifiedAt]`: a
 *  claimed node is always a file whose path is its session's primary path, and the selector is
 *  spelled from the harness and the source id, so a replay rebuilds exactly what was claimed. */
const RememberedSchema = z.strictObject({
  version: z.literal(LISTING_RULES),
  snapshot: SnapshotIdSchema,
  entries: z.number().int().nonnegative(),
  captures: z.array(
    z.tuple([
      HarnessSchema,
      z.string().min(1),
      z.string().min(1).max(MAX_ARCHIVED_PATH),
      z.number().int().nonnegative(),
      z.string(),
    ]),
  ),
});

/** The memory kept in `directory`, one gzipped file per snapshot, written whole or not at all. */
export function listingMemory(directory: string): ListingMemory {
  const pathOf = (snapshotId: string): string | null =>
    SnapshotIdSchema.safeParse(snapshotId).success
      ? join(directory, `${snapshotId}.json.gz`)
      : null;
  return {
    async recall(snapshotId) {
      const path = pathOf(snapshotId);
      if (path === null) return null;
      try {
        const packed = await Bun.file(path)
          .slice(0, MAX_REMEMBERED_BYTES + 1)
          .arrayBuffer();
        if (packed.byteLength > MAX_REMEMBERED_BYTES) return null;
        const parsed = RememberedSchema.safeParse(
          JSON.parse(
            new TextDecoder("utf-8", { fatal: true }).decode(
              gunzipSync(packed, { maxOutputLength: MAX_REMEMBERED_BYTES }),
            ),
          ),
        );
        if (!parsed.success || parsed.data.snapshot !== snapshotId) return null;
        return {
          entries: parsed.data.entries,
          captures: parsed.data.captures.map(([harness, sourceId, path, size, modifiedAt]) => ({
            session: sessionRef(harness, sourceId, path),
            node: { path, type: "file", size, modifiedAt },
          })),
        };
      } catch {
        return null;
      }
    },
    async keep(snapshotId, listing) {
      const path = pathOf(snapshotId);
      if (path === null) return;
      const captures: z.infer<typeof RememberedSchema>["captures"] = [];
      for (const { session, node } of listing.captures) {
        const rebuilt = sessionRef(session.harness, session.sourceId, node.path);
        // A claim this spelling cannot replay exactly is listed every time instead.
        if (
          node.type !== "file" ||
          rebuilt.selector !== session.selector ||
          rebuilt.primaryPath !== session.primaryPath
        )
          return;
        captures.push([session.harness, session.sourceId, node.path, node.size, node.modifiedAt]);
      }
      const document = JSON.stringify({
        version: LISTING_RULES,
        snapshot: snapshotId,
        entries: listing.entries,
        captures,
      });
      if (Buffer.byteLength(document) > MAX_REMEMBERED_BYTES) return;
      const temporary = `${path}.${crypto.randomUUID()}`;
      try {
        await mkdir(directory, { recursive: true, mode: 0o700 });
        // mkdir's mode reaches only a directory it creates; an existing one is narrowed here.
        await chmod(directory, 0o700);
        // Created exclusively and owner-only: the umask can narrow this mode, never widen it.
        await writeFile(temporary, gzipSync(document), { mode: 0o600, flag: "wx" });
        await rename(temporary, path);
      } catch {
        // The memory is an optimization, never a condition of reading the archive.
      } finally {
        await rm(temporary, { force: true }).catch(() => undefined);
      }
    },
  };
}
