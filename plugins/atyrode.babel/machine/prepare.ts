/*
  THE prepare OPERATION (plan §4): selectors in, a preparation out — an immutable statement of
  one exploration's corpus scope, whose identity is DERIVED from the content of the selection
  rather than assigned.

  That is what makes `explore --preparation <id>` mean something: naming a preparation states
  which corpus a run read, instead of leaving it to whatever the machine happened to hold at
  the time. Two preparations over the same sessions at the same captures are the same id, so
  two runs over one scope look like two runs over one scope; one session's bytes changing is a
  different id, because it is a different corpus.

  DELIBERATE DIFFERENCE FROM THE GO PRODUCT (internal/run/preparation.go): `preparedAt` is
  recorded but NOT hashed. Go's derivation included the instant, so re-preparing an unchanged
  corpus minted a second identity for the same scope — which contradicts the idempotence that
  same-scope-same-id is for. Here the id is a function of the selection alone.

  Each entry carries both digests SPEC §7 requires of a selection: the CAPTURE digest over the
  primary log's bytes as they lie on disk, which is what a restore is checked against, and the
  SOURCE digest over the normalized record stream, which is what analysis reads. A harness that
  rewrites its log with different spacing moves the first and not the second, and that
  difference is what a later reviewer needs in order to tell "the corpus changed" from "our
  reading of it changed".

  What the normalization IS, in this wave: one canonical JSON record per line — object keys
  ordered, insignificant whitespace gone — and an explicit opaque marker for a line that is not
  a record, so nothing is ever dropped. What it is NOT, yet: internal/event's classification of
  each record into §6.3's five evidence kinds, which belongs with the retrieval index that is
  its only consumer. When that lands it owns the source digest and bumps PREPARATION_SCHEMA.
*/

import { z } from "zod";
import type { Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputSink } from "./output.ts";

export const PrepareInputSchema = z.strictObject({
  /** The run this job is; empty mints one (see archive.ts). */
  runId: z.string().trim().max(120).default(""),
  /** The machine's identity, recorded as the host of every session it holds itself. */
  machineId: z.string().trim().min(1).max(120),
  /** `HARNESS/SOURCE-ID`, or any unambiguous suffix of one. Empty scopes every session this
   *  machine can see, which is what a scheduled preparation wants. */
  selectors: z.array(z.string().trim().min(1).max(400)).max(500).default([]),
});
export type PrepareInput = z.infer<typeof PrepareInputSchema>;

export interface SessionDigests {
  readonly captureDigest: string;
  readonly sourceDigest: string;
  readonly bytes: number;
}

/** The machine facts this operation needs, which the adapters own (machine/adapters). */
export interface PrepareDeps {
  /** Every session this machine can see, under the adapters' own roots. */
  discover(): Promise<readonly SessionRef[]>;
  digests(ref: SessionRef): Promise<SessionDigests>;
}

/** The version of the preparation record's shape AND of the normalization behind its source
 *  digest. It participates in the derivation, so a record written by a later schema can never
 *  collide with one written by this schema even if every other field matches. */
export const PREPARATION_SCHEMA = 2;

/** Separates this hash from every other use of SHA-256 in Babel: without a domain, a digest
 *  over some other structure that happened to serialize identically would be a valid
 *  preparation id. The `v2` is PREPARATION_SCHEMA's own, moved with it. */
const PREPARATION_DOMAIN = "babel/preparation/v2";

/** Marks a preparation id as one, so a mistyped identifier fails as the wrong kind of id
 *  rather than as a missing row. */
const PREPARATION_PREFIX = "prep-";

/** One session inside a preparation, identified as decision 9 identifies sessions: the machine
 *  that holds it, the harness, and the adapter-defined source identity. */
export type PreparationEntry = {
  readonly host: string;
  readonly harness: string;
  readonly sourceId: string;
  readonly captureDigest: string;
  readonly sourceDigest: string;
};

export type Preparation = {
  readonly id: string;
  readonly schema: number;
  /** When the scope was fixed. Recorded, never hashed — see the header. */
  readonly preparedAt: string;
  /** Canonically ordered, so neither discovery order nor a caller's later mutation can change
   *  what the record means. */
  readonly selection: readonly PreparationEntry[];
};

/** One `sessions` row as a preparation observed it: the identity, the capture it fixed, and
 *  the size that capture was. Nothing else — `scan` owns the rest of the row. */
interface PreparedSessionRow {
  readonly selector: string;
  readonly host: string;
  readonly harness: string;
  readonly source_id: string;
  readonly content_digest: string;
  readonly size: number;
  readonly seen_at: string;
}

/** A record longer than this is hashed in bounded pieces, split at a content-determined
 *  offset so the digest never depends on how the file happened to arrive in chunks. */
const MAX_RECORD_CHARS = 4 << 20;

/**
 * Fixes a corpus scope and derives its identity.
 *
 * An empty selection is refused: an exploration over nothing is a mistake, and accepting it
 * would make a broken selection indistinguishable from a deliberate one. So is the same
 * session twice, which would let a scope claim a weight it does not have.
 */
export function newPreparation(preparedAt: string, selection: readonly PreparationEntry[]): Preparation {
  if (selection.length === 0) throw new Error("preparation: the selection is empty");
  const canonical = [...selection].sort((a, b) => {
    if (a.host !== b.host) return a.host < b.host ? -1 : 1;
    if (a.harness !== b.harness) return a.harness < b.harness ? -1 : 1;
    if (a.sourceId === b.sourceId) return 0;
    return a.sourceId < b.sourceId ? -1 : 1;
  });
  for (const [index, entry] of canonical.entries()) {
    if (entry.host === "" || entry.harness === "" || entry.sourceId === "") {
      throw new Error(`preparation: selection entry ${index} names no session`);
    }
    if (entry.captureDigest === "") {
      throw new Error(`preparation: selection entry ${index} has no capture digest`);
    }
    const previous = canonical[index - 1];
    if (
      previous !== undefined &&
      previous.host === entry.host &&
      previous.harness === entry.harness &&
      previous.sourceId === entry.sourceId
    ) {
      throw new Error(`preparation: the selection holds ${entry.harness}/${entry.sourceId} twice`);
    }
  }
  return { id: derive(canonical), schema: PREPARATION_SCHEMA, preparedAt, selection: canonical };
}

/**
 * The id, as `prep-<64 lowercase hex>`.
 *
 * The hashed encoding is injective, which is what makes the id a function of the content
 * rather than of its punctuation:
 *
 *   PREPARATION_DOMAIN || u32(schema) || u32(entries)
 *     then per entry, in canonical order:
 *       lp(host) || lp(harness) || lp(sourceId) || lp(captureDigest) || lp(sourceDigest)
 *
 * where lp(s) is a four-byte big-endian length followed by s's UTF-8 bytes and u32 is four big
 * endian bytes. Every variable-length field is length-prefixed and every repetition is
 * count-prefixed, so no two distinct selections encode to the same bytes — plain concatenation
 * would let a host of "a" with a harness of "bc" collide with a host of "ab" and a harness
 * of "c".
 */
function derive(selection: readonly PreparationEntry[]): string {
  const hasher = new Bun.CryptoHasher("sha256");
  hasher.update(PREPARATION_DOMAIN);
  writeU32(hasher, PREPARATION_SCHEMA);
  writeU32(hasher, selection.length);
  for (const entry of selection) {
    writeLP(hasher, entry.host);
    writeLP(hasher, entry.harness);
    writeLP(hasher, entry.sourceId);
    writeLP(hasher, entry.captureDigest);
    writeLP(hasher, entry.sourceDigest);
  }
  return PREPARATION_PREFIX + hasher.digest("hex");
}

/** One reusable four-byte frame for the length prefixes: `update` copies synchronously and
 *  nothing awaits between the write and the read, so one buffer serves every derivation. */
const U32_BYTES = new Uint8Array(4);
const U32 = new DataView(U32_BYTES.buffer);

function writeU32(hasher: Bun.CryptoHasher, value: number): void {
  U32.setUint32(0, value, false);
  hasher.update(U32_BYTES);
}

function writeLP(hasher: Bun.CryptoHasher, value: string): void {
  const bytes = new TextEncoder().encode(value);
  writeU32(hasher, bytes.length);
  hasher.update(bytes);
}

/**
 * Both digests of one session's primary log, and the bytes they covered, from ONE pass.
 *
 * One pass because the corpus is dominated by a handful of very large logs, and reading them
 * twice per preparation would double the only cost that matters. The capture digest covers
 * every byte, including whatever the record splitter did not find a record in: it identifies
 * the file, not the part of it that parsed.
 */
export async function digests(ref: SessionRef): Promise<SessionDigests> {
  const capture = new Bun.CryptoHasher("sha256");
  const source = new Bun.CryptoHasher("sha256");
  const decoder = new TextDecoder();
  let bytes = 0;
  let pending = "";
  const record = (line: string): void => {
    const normalized = normalize(line);
    if (normalized !== "") source.update(normalized);
  };
  for await (const chunk of Bun.file(ref.primaryPath).stream()) {
    capture.update(chunk);
    bytes += chunk.byteLength;
    pending += decoder.decode(chunk, { stream: true });
    let start = 0;
    for (let nl = pending.indexOf("\n"); nl >= 0; nl = pending.indexOf("\n", start)) {
      record(pending.slice(start, nl));
      start = nl + 1;
    }
    pending = start === 0 ? pending : pending.slice(start);
    while (pending.length > MAX_RECORD_CHARS) {
      record(pending.slice(0, MAX_RECORD_CHARS));
      pending = pending.slice(MAX_RECORD_CHARS);
    }
  }
  pending += decoder.decode();
  record(pending);
  return {
    captureDigest: `sha256:${capture.digest("hex")}`,
    sourceDigest: `sha256:${source.digest("hex")}`,
    bytes,
  };
}

/**
 * One line of a primary log as the normalized stream states it.
 *
 * A record becomes canonical JSON, so a harness that reorders its keys or reflows its
 * whitespace is the same corpus. A line that is not a record is retained verbatim behind a
 * marker canonical JSON can never start with: a torn or corrupt line is evidence that this
 * exact line was seen, and dropping it would make degradation silent.
 */
function normalize(line: string): string {
  if (line.trim() === "") return "";
  try {
    return `${canonical(JSON.parse(line))}\n`;
  } catch {
    return `!${line}\n`;
  }
}

function canonical(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  const keys = Object.keys(value).sort();
  const fields = keys.map((key) => `${JSON.stringify(key)}:${canonical((value as Record<string, unknown>)[key])}`);
  return `{${fields.join(",")}}`;
}

export async function prepare(
  input: PrepareInput,
  out: OutputSink,
  deps: PrepareDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const counts = { discovered: 0, selected: 0, bytes: 0 };
  const rows: PreparedSessionRow[] = [];
  let preparation: Preparation | null = null;
  let closure: Receipt["closure"] = "completed";
  let reason = "";

  const discovered = await deps.discover();
  counts.discovered = discovered.length;
  const chosen = choose(discovered, input.selectors);
  if (chosen.failure !== "") {
    // A selector that matches nothing, or matches two sessions, is a rejected invocation
    // rather than a silently smaller scope: a preparation records what was meant to be
    // explored, and a scope that quietly shrank would make the next run's coverage a mystery.
    closure = "failed";
    reason = chosen.failure;
  } else if (chosen.chosen.length === 0) {
    closure = "skipped";
    reason = "no session on this machine to prepare";
  } else {
    const seenAt = new Date().toISOString();
    const selection: PreparationEntry[] = [];
    for (const session of chosen.chosen) {
      let measured;
      try {
        measured = await deps.digests(session);
      } catch (err) {
        // The scope is refused whole. A preparation missing one of the sessions it was asked
        // for would be an immutable record of a corpus nobody chose.
        closure = "failed";
        reason = `read ${session.selector}: ${err instanceof Error ? err.message : String(err)}`;
        break;
      }
      counts.bytes += measured.bytes;
      selection.push({
        host: input.machineId,
        harness: session.harness,
        sourceId: session.sourceId,
        captureDigest: measured.captureDigest,
        sourceDigest: measured.sourceDigest,
      });
      rows.push({
        selector: session.selector,
        host: input.machineId,
        harness: session.harness,
        source_id: session.sourceId,
        content_digest: measured.captureDigest,
        size: measured.bytes,
        seen_at: seenAt,
      });
    }
    if (closure === "completed") {
      preparation = newPreparation(new Date().toISOString(), selection);
      counts.selected = selection.length;
    }
  }

  // Every scoped session is registered in the hub's catalog, because a preparation that named
  // sessions the hub holds no row for would be an identity nothing could later resolve.
  await out.write("sessions", closure === "completed" ? rows : []);
  const receipt: Receipt = {
    runId,
    kind: "prepare",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure,
    counts: { ...counts },
    ...(preparation === null ? {} : { preparation }),
    ...(reason === "" ? {} : { reason }),
  };
  await out.receipt(receipt);
  return receipt;
}

/**
 * The sessions the selectors name, or the first refusal.
 *
 * Matching is tiered — the exact canonical selector first, then a segment-aligned suffix, then
 * any suffix — so a fully qualified selector can never be shadowed by a longer source id that
 * happens to end the same way. Ambiguity is a rejected invocation, not a guess, and the
 * candidates are listed so the operator can qualify what they meant.
 */
function choose(
  sessions: readonly SessionRef[],
  selectors: readonly string[],
): { readonly chosen: readonly SessionRef[]; readonly failure: string } {
  if (selectors.length === 0) return { chosen: sessions, failure: "" };
  const chosen: SessionRef[] = [];
  const taken = new Set<string>();
  for (const selector of selectors) {
    const exact: SessionRef[] = [];
    const aligned: SessionRef[] = [];
    const loose: SessionRef[] = [];
    for (const session of sessions) {
      if (session.selector === selector) exact.push(session);
      else if (session.selector.endsWith(`/${selector}`)) aligned.push(session);
      else if (session.selector.endsWith(selector)) loose.push(session);
    }
    const tier = exact.length > 0 ? exact : aligned.length > 0 ? aligned : loose;
    const [session] = tier;
    if (session === undefined) {
      return { chosen: [], failure: `no session on this machine matches the selector ${selector}` };
    }
    if (tier.length > 1) {
      return {
        chosen: [],
        failure: `the selector ${selector} is ambiguous, it matches ${tier.length} sessions: ${tier
          .map((candidate) => candidate.selector)
          .join(" ")}`,
      };
    }
    if (taken.has(session.selector)) continue;
    taken.add(session.selector);
    chosen.push(session);
  }
  return { chosen, failure: "" };
}
