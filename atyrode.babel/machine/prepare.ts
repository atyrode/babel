/*
  THE prepare OPERATION (plan §4): selectors in, a preparation out — an immutable statement of
  one exploration's corpus scope, whose identity is DERIVED from the content of the selection
  rather than assigned.

  That is what makes `explore --preparation <id>` mean something: naming a preparation states
  which corpus a run read, instead of leaving it to whatever the machine happened to hold at
  the time. Two preparations over the same sessions at the same captures are the same id, so
  two runs over one scope look like two runs over one scope; one session's bytes changing is a
  different id, because it is a different corpus.

  DELIBERATE DIFFERENCE FROM THE GO PRODUCT (v0.4.0:internal/run/preparation.go): `preparedAt` is
  recorded but NOT hashed. Go's derivation included the instant, so re-preparing an unchanged
  corpus minted a second identity for the same scope — which contradicts the idempotence that
  same-scope-same-id is for. Here the id is a function of the selection alone.

  WHAT A DEFAULT SCOPE LEAVES OUT, and why the id above depends on it (#262). A session whose
  log was written in the last two minutes is still being appended: its bytes move under the run
  reading them, so a scope holding one has no stable identity at all — on 2026-09-13 every
  explore of the day reported "changed since the preparation was fixed", and the file doing it
  was Babel's own operator transcript. And Babel's own run transcripts are left out because an
  exploration's subject is the operator's work; a preset that studies Babel asks for them with
  `agentSessions`. Both rules apply to "scope this machine"; a selector that NAMES an excluded
  session is refused instead, because a scope that quietly shrank is worse than a refusal.

  Each entry carries both digests SPEC §7 requires of a selection: the CAPTURE digest over the
  primary log's bytes as they lie on disk, which is what a restore is checked against, and the
  SOURCE digest over the normalized record stream, which is what analysis reads. A harness that
  rewrites its log with different spacing moves the first and not the second, and that
  difference is what a later reviewer needs in order to tell "the corpus changed" from "our
  reading of it changed".

  What the normalization IS, in this wave: one canonical JSON record per line — object keys
  ordered, insignificant whitespace gone — and an explicit opaque marker for a line that is not
  a record, so nothing is ever dropped. What it is NOT, yet: v0.4.0:internal/event's classification of
  each record into §6.3's five evidence kinds, which belongs with the retrieval index that is
  its only consumer. When that lands it owns the source digest and bumps PREPARATION_SCHEMA.
*/

import { z } from "zod";
import {
  MATERIAL_SCHEMA,
  RUN_STAGES,
  materialFile,
  type MaterialEntry,
  type MaterialIndex,
  type Receipt,
} from "../contract.ts";
import { LIVE_GRACE_MS, babelOwnLog, type SessionRef } from "./adapters/index.ts";
import type { MaterialSink, OutputSink, RecordSink } from "./output.ts";
import { SILENT, type ProgressChannel } from "./progress.ts";

export const PrepareInputSchema = z.strictObject({
  /** The run this job is; empty mints one (see archive.ts). */
  runId: z.string().trim().max(120).default(""),
  /** The machine's identity, recorded as the host of every session it holds itself. */
  machineId: z.string().trim().min(1).max(120),
  /** `HARNESS/SOURCE-ID`, or any unambiguous suffix of one. Empty scopes every session this
   *  machine can see, which is what a scheduled preparation wants. */
  selectors: z.array(z.string().trim().min(1).max(400)).max(500).default([]),
  /**
   * Whether sessions of BABEL'S OWN runs may be in the scope (`kind` `agent`, scan.ts).
   *
   * False is the default because an exploration's subject is the operator's work, and a corpus
   * that quietly included Babel's own transcripts would have Babel reading itself by accident —
   * on 2026-09-13 that was a 35 MB harness log, still being appended, which invalidated every
   * preparation of the day. A preset that studies Babel on purpose (#270) asks for them, and
   * then this is what it asks with.
   *
   * There is no flag for a LIVE session, and there must not be: a preparation's identity is its
   * selection's content, so a file whose bytes are still moving is not a scope at all.
   */
  agentSessions: z.boolean().default(false),
});
export type PrepareInput = z.infer<typeof PrepareInputSchema>;

export interface SessionDigests {
  readonly captureDigest: string;
  readonly sourceDigest: string;
  readonly bytes: number;
  /**
   * How many normalized records the source stream held. It is counted here rather than by the
   * caller because it is the same pass: a second count would be a second read of the log, and
   * the prompt's "N records" is what tells a model how big a session is before it opens one.
   */
  readonly records: number;
}

/** The machine facts this operation needs, which the adapters own (machine/adapters). */
export interface PrepareDeps {
  /** Every session this machine can see, under the adapters' own roots. */
  discover(): Promise<readonly SessionRef[]>;
  /**
   * Both digests of one session's log, its size and its record count, from ONE pass — and, when
   * a sink is handed, that same pass writes the normalized stream into the material (#279). The
   * sink is a PARAMETER rather than a second verb because reading a 240 MB log twice per
   * preparation is the only cost this operation has ever had.
   */
  digests(ref: SessionRef, seal?: RecordSink | undefined): Promise<SessionDigests>;
  /**
   * When this session's primary log was last written, in epoch ms; 0 when nothing could be
   * observed. It is asked BEFORE the digests on purpose — one `stat` against a whole read —
   * because skipping a moving 240 MB log is the point.
   */
  modifiedAt(ref: SessionRef): Promise<number>;
  /**
   * Where the material is sealed: the second output lease (`machine/output.ts`). Null for a
   * hand-run that bound none, which prepares a selection and seals no evidence — the receipt
   * says which, so a run whose material nothing can read is never mistaken for one that has it.
   */
  material?: MaterialSink | null | undefined;
  /** Where this run says it is; a caller that hands none is not watched (`progress.ts`). */
  progress?: ProgressChannel | undefined;
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

/** One session inside a preparation, identified the way every session is: the machine
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
 * Both digests of one session's primary log, the bytes they covered, its record count — and,
 * when a sink is handed, the material's own copy of the normalized stream — from ONE pass.
 *
 * One pass because the corpus is dominated by a handful of very large logs, and reading them
 * twice per preparation would double the only cost that matters. The capture digest covers
 * every byte, including whatever the record splitter did not find a record in: it identifies
 * the file, not the part of it that parsed.
 *
 * THE SINK IS WRITTEN THE SAME BYTES THE SOURCE DIGEST COVERS, in the same order, which is the
 * whole reason a claim may cite a line of the material's file: the digest in the index is a
 * digest of exactly that file's contents, so a later reader recovers the bytes the model read
 * rather than the bytes the harness happened to hold when it was asked.
 */
export async function digests(ref: SessionRef, seal?: RecordSink | undefined): Promise<SessionDigests> {
  const capture = new Bun.CryptoHasher("sha256");
  const source = new Bun.CryptoHasher("sha256");
  const decoder = new TextDecoder();
  let bytes = 0;
  let records = 0;
  let pending = "";
  const record = (line: string): void => {
    const normalized = normalize(line);
    if (normalized === "") return;
    source.update(normalized);
    records += 1;
    seal?.write(normalized);
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
    records,
  };
}

/**
 * When a session's primary log was last written, in epoch ms; 0 when the file is gone or the
 * filesystem answered nothing. A log that cannot be stat-ed is not treated as live: it is the
 * digest pass that will fail over it, with the path in the refusal, and "unreadable" is a
 * better answer than "still being written".
 */
export async function modifiedAt(ref: SessionRef): Promise<number> {
  const at = Bun.file(ref.primaryPath).lastModified;
  return await Promise.resolve(Number.isFinite(at) && at > 0 ? at : 0);
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
  const counts = { discovered: 0, selected: 0, bytes: 0, records: 0, live: 0, agent: 0 };
  const rows: PreparedSessionRow[] = [];
  /** The material's own index, built as the loop seals each session's stream. */
  const sealed: MaterialEntry[] = [];
  let preparation: Preparation | null = null;
  let closure: Receipt["closure"] = "completed";
  let reason = "";
  const progress = deps.progress ?? SILENT;
  progress.report({ stage: RUN_STAGES.preparing, message: "discovering the sessions this machine holds" });

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
    const named = input.selectors.length > 0;
    const at = Date.now();
    // The loop already counts, so the fraction costs nothing and is the honest one: a digest
    // over a large log is where the minutes go, and "3/927" is what the operator wanted to see
    // on 2026-09-13 instead of silence.
    let examined = 0;
    for (const session of chosen.chosen) {
      examined += 1;
      progress.report({
        stage: RUN_STAGES.preparing,
        message: `${session.selector} (${String(examined)}/${String(chosen.chosen.length)})`,
        fraction: examined / chosen.chosen.length,
      });
      const left = await excluded(session, input, deps, at);
      if (left !== null) {
        if (named) {
          // A selector that names an excluded session is refused for the reason an unmatched
          // one is: what was asked for is not what would be prepared, and a scope that quietly
          // shrank makes the next run's coverage a mystery. Nothing is excluded silently here;
          // it is only when NO selector was given — "scope this machine" — that the two kinds
          // below are skipped and counted.
          closure = "failed";
          reason = left.reason;
          break;
        }
        if (left.kind === "live") counts.live++;
        else counts.agent++;
        continue;
      }
      // THE MATERIAL IS SEALED IN THE SAME PASS THE DIGESTS ARE TAKEN IN (#279). The file is
      // opened before the read and closed after it whatever the read did, so a scope refused
      // half way leaves no half-written stream a later reader could mistake for a session.
      const file = materialFile(sealed.length, session.selector);
      const seal = (await deps.material?.session(file)) ?? null;
      let measured;
      try {
        measured = await deps.digests(session, seal ?? undefined);
      } catch (err) {
        // The scope is refused whole. A preparation missing one of the sessions it was asked
        // for would be an immutable record of a corpus nobody chose.
        await seal?.close();
        closure = "failed";
        reason = `read ${session.selector}: ${err instanceof Error ? err.message : String(err)}`;
        break;
      }
      await seal?.close();
      counts.bytes += measured.bytes;
      counts.records += measured.records;
      selection.push({
        host: input.machineId,
        harness: session.harness,
        sourceId: session.sourceId,
        captureDigest: measured.captureDigest,
        sourceDigest: measured.sourceDigest,
      });
      sealed.push({
        selector: session.selector,
        harness: session.harness,
        sourceId: session.sourceId,
        captureDigest: measured.captureDigest,
        sourceDigest: measured.sourceDigest,
        file,
        records: measured.records,
        bytes: measured.bytes,
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
    if (closure === "completed" && selection.length === 0) {
      // Every session there was, excluded. It is `skipped` rather than failed for the same
      // reason a machine holding none is: there is nothing wrong here, there is nothing to do.
      closure = "skipped";
      reason =
        `no settled session of the operator's own on this machine to prepare: ` +
        `${String(counts.live)} still being written, ${String(counts.agent)} Babel's own runs'`;
    } else if (closure === "completed") {
      preparation = newPreparation(new Date().toISOString(), selection);
      counts.selected = selection.length;
    }
  }

  // Every scoped session is registered in the hub's catalog, because a preparation that named
  // sessions the hub holds no row for would be an identity nothing could later resolve.
  progress.report({ stage: RUN_STAGES.submitting, message: `${String(counts.selected)} sessions` });
  await out.write("sessions", closure === "completed" ? rows : []);
  /*
    THE INDEX IS WRITTEN LAST, TWICE, AND ONLY FOR A COMPLETED PREPARATION (#279).

    It is the material's own manifest: the preparation it belongs to, and the digest a citation
    must carry per session. It goes into the LEASE, where the session's sandbox binds it and the
    model reads it, and into the RECEIPT, where the hub ingests it and checks a submitted claim's
    locators against the selection they were served from — one document, two readers, and the
    alternative for the second was pulling a sealed archive of every session's records back
    through the hub to read the twenty lines at the front of it.

    A refused or skipped scope indexes NEITHER. A bound material whose index nobody chose the
    selection of is exactly the corpus-nobody-chose failure this operation refuses whole; the
    receipt's `reason` is where that is said, and a consumer with no index has nothing to read
    and says so.
  */
  const index: MaterialIndex | null =
    closure === "completed" && preparation !== null
      ? {
          schema: MATERIAL_SCHEMA,
          preparationId: preparation.id,
          preparedAt: preparation.preparedAt,
          machineId: input.machineId,
          sessions: sealed,
        }
      : null;
  if (index !== null && deps.material != null) await deps.material.index(index);
  const receipt: Receipt = {
    runId,
    kind: "prepare",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure,
    counts: { ...counts },
    ...(preparation === null ? {} : { preparation }),
    ...(index === null ? {} : { material: index }),
    ...(reason === "" ? {} : { reason }),
  };
  await out.receipt(receipt);
  return receipt;
}

/** Why a session is not in a default scope; null when it belongs in one. */
type Exclusion = { readonly kind: "live" | "agent"; readonly reason: string } | null;

/**
 * Whether this session may be in a scope, and the sentence saying why not.
 *
 * The two rules are the ones #262 names, in the order that costs least: Babel's own transcripts
 * are recognized from the path alone, and liveness costs one `stat` — so a 240 MB log that is
 * still being appended is excluded without being read, which is the whole point of asking here
 * rather than after the digests.
 *
 * A log written in the FUTURE is live. Clocks on one machine disagree by seconds, and a file
 * whose mtime is ahead of this process is the last thing to treat as settled.
 */
async function excluded(
  session: SessionRef,
  input: PrepareInput,
  deps: PrepareDeps,
  at: number,
): Promise<Exclusion> {
  if (!input.agentSessions && babelOwnLog(session.primaryPath)) {
    return {
      kind: "agent",
      reason:
        `${session.selector} is one of Babel's own runs' transcripts, which a preparation of ` +
        `the operator's work does not read`,
    };
  }
  const written = await deps.modifiedAt(session);
  if (written > 0 && at - written < LIVE_GRACE_MS) {
    const seconds = Math.max(0, Math.round((at - written) / 1000));
    return {
      kind: "live",
      reason:
        `${session.selector} was written ${String(seconds)}s ago and is still being appended; ` +
        `a preparation is its selection's content, so a moving file is not a scope`,
    };
  }
  return null;
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
