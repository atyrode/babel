import { stat } from "node:fs/promises";

import type { Adapter, SessionRef } from "./identity.ts";
import { claude } from "./claude.ts";
import { codex } from "./codex.ts";
import { omp } from "./omp.ts";

/*
  THE ADAPTERS, AND THE THINGS EVERY OPERATION ASKS OF ALL THREE AT ONCE.

  One flat root list is offered to every adapter rather than a root list per harness: the
  operator configures where his sessions are, and each adapter's layout rule is what keeps a
  foreign tree out (see codex.ts for the one place that rule had to be sharpened for it).
  A root that does not exist costs a readdir that fails and nothing else.
*/

export type {
  Adapter,
  Harness,
  SessionFacts,
  SessionRef,
  SessionUsage,
  TitleProvenance,
} from "./identity.ts";
export { HARNESSES, TITLE_PROVENANCES, sessionRef, validSourceId } from "./identity.ts";
export { claude } from "./claude.ts";
export { codex } from "./codex.ts";
export { omp } from "./omp.ts";

/** Every adapter, in the order a path is offered to them. */
export const ADAPTERS: readonly Adapter[] = [omp, codex, claude];

/** The session this path is the primary log of, from the first adapter that recognizes it. */
export function claim(path: string): SessionRef | null {
  for (const adapter of ADAPTERS) {
    const ref = adapter.claim(path);
    if (ref !== null) return ref;
  }
  return null;
}

/**
 * Every session on this machine, selector-ordered. Roots default to each adapter's own; an
 * explicit list is offered to all of them, because a path is a path and the layout decides.
 */
export async function discover(roots?: readonly string[]): Promise<SessionRef[]> {
  const found: SessionRef[] = [];
  for (const adapter of ADAPTERS) {
    found.push(...(await adapter.discover(roots ?? adapter.defaultRoots())));
  }
  found.sort((a, b) => (a.selector < b.selector ? -1 : a.selector > b.selector ? 1 : 0));
  return found;
}

/** The backup roots that exist here: what a snapshot of this machine can actually cover. */
export async function existingRoots(): Promise<string[]> {
  const roots = new Set<string>();
  for (const adapter of ADAPTERS) {
    for (const root of adapter.backupRoots()) {
      const info = await stat(root).catch(() => null);
      if (info?.isDirectory() === true) roots.add(root);
    }
  }
  return [...roots].sort();
}

/**
 * The canonical digest of a file's live bytes and their count, in one pass and without
 * parsing anything: "sha256:<64 lowercase hex>", verifiable with plain sha256sum.
 */
export async function contentDigest(path: string): Promise<{ digest: string; size: number }> {
  const hasher = new Bun.CryptoHasher("sha256");
  let size = 0;
  for await (const chunk of Bun.file(path).stream()) {
    hasher.update(chunk);
    size += chunk.byteLength;
  }
  return { digest: "sha256:" + hasher.digest("hex"), size };
}
