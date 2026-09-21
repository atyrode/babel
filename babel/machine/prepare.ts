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

  WHAT A SECOND PREPARATION OVER AN UNCHANGED SCOPE DOES NOT DO AGAIN (#236). Reading a log is
  this operation's whole cost, and the answer does not depend on which run asked: twenty
  explorations over overlapping scopes on 2026-09-12 read and hashed the same files twenty
  times, at load 41 with no model call in flight. So the reading is KEPT on the machine, keyed
  on the observation it was derived from — path, size, mtime, normalization, detector set,
  preflight mode — and a later pass that observes the same triple replays it instead of reading
  the log (`machine/cache.ts` holds the whole of the reasoning, and why an entry is a claim
  about an observation rather than a position). The two rules above are what make that safe: a
  file that could still be moving is never in a scope, so an entry is only ever about a log that
  settled minutes ago.

  Normalization remains one canonical JSON record per line — object keys ordered, insignificant
  whitespace gone — with an explicit opaque marker for a line that is not a record, so nothing
  is dropped. The lexical session index reads this same redacted stream; it does not introduce
  v0.4.0:internal/event's evidence-kind classification or change the source digest schema.

  THE MATERIAL IS SCANNED BEFORE IT IS SEALED (#339, SPEC §6.4). `machine/preflight.ts` replaces
  every likely-credential span with a marker naming its class and the locator of the original,
  and the pass is the same one: the redacted record is what the source digest covers and what the
  sink receives, so a scan cannot disagree with the bytes a session was given. A preparation may
  also refuse the whole scope over what was found, by class and never by value, and the receipt
  carries the result either way — an absent report says nothing was scanned, never that a corpus
  was clean. The only way back to a redacted value is `resolveRedaction`, which needs the log
  this machine holds, so what crosses to the hub is a locator and a class.
*/

import { stat } from "node:fs/promises";

import { z } from "zod";
import {
  MATERIAL_SCHEMA,
  MATERIAL_RETRIEVAL,
  MaterialRetrievalSchema,
  MAX_MATERIAL_BYTES,
  PREFLIGHT_SCHEMA,
  RECALL_MAX_HITS,
  RECALL_MAX_RESULT_BYTES,
  PreflightModeSchema,
  RUN_STAGES,
  SessionContentQuerySchema,
  materialFile,
  termsQuery,
  type MaterialEntry,
  type MaterialIndex,
  type MaterialRetrieval,
  type PreflightMode,
  type PreflightReport,
  type Receipt,
  type SessionContentQuery,
  type SessionRetrieval,
} from "../contract.ts";
import { LIVE_GRACE_MS, babelOwnLog, type SessionRef } from "./adapters/index.ts";
import { readingCache, type Observation, type ReadingContext } from "./cache.ts";
import { teeRecords, type MaterialSink, type OutputSink, type RecordSink } from "./output.ts";
import {
  PREFLIGHT_DETECTORS,
  refusalMessage,
  secretScan,
  type ScanReport,
  type SecretScan,
} from "./preflight.ts";
import { SILENT, type ProgressChannel } from "./progress.ts";
import { recallRecordReader } from "./recall-records.ts";
import { recordReader, sessionDigester, type SessionDigests } from "./session-records.ts";
import {
  sessionIndex,
  SessionIndexError,
  type IndexedSession,
  type IndexedRecord,
  type SessionIndex,
} from "./session-index.ts";

/**
 * The one binding this operation reads from the environment, for the reason `VERIFY_ENV`'s is a
 * fixed, reviewed, non-secret path inside a location the manifest declares writable — here the
 * managed `atyrode.babel.cache`, shared by every `prepare` job on the machine, which is what
 * makes one reading serve twenty concurrent explorations (#236).
 *
 * Absent — a hand-run outside a job, the suites — keeps nothing: a cache a hand-run invented
 * under the system's temporary directory would be a surprise rather than a saving.
 */
export const PREPARE_ENV = {
  cacheDir: "BABEL_PREPARE_CACHE_DIR",
} as const;

export const PrepareInputSchema = z.strictObject({
  /** The run this job is; empty mints one (see archive.ts). */
  runId: z.string().trim().max(120).default(""),
  /** The machine's identity, recorded as the host of every session it holds itself. */
  machineId: z.string().trim().min(1).max(120),
  /** `HARNESS/SOURCE-ID`, or any unambiguous suffix of one. Empty scopes every session this
   *  machine can see, which is what a scheduled preparation wants. */
  selectors: z.array(z.string().trim().min(1).max(400)).max(500).default([]),
  /** Literal lexical selection over settled, redacted session content, instead of selectors. */
  query: SessionContentQuerySchema.optional(),
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
  /**
   * WHAT THIS PREPARATION DOES ABOUT A LIKELY SECRET IN THE MATERIAL (#339).
   *
   * `redact`, and nothing asks for anything else: no door sets this field, so the default is
   * what every posted preparation gets, and the default is a decision rather than a fallback.
   * Refusing a whole scope over one credential would lose the transcript in order to protect the
   * operator from his own paste — the session that carries a stale key is usually the session
   * worth reading — so the span is replaced with a marker naming its class, the rest of the
   * record stays evidence, and the locator back to the original stays on this machine.
   *
   * `refuse` is for a scope that must not risk a disclosure at all, and `off` seals the raw
   * stream. Both are reachable only from a job input written by hand, and both are recorded on
   * the receipt, so an unscanned preparation is never mistaken for a clean one.
   */
  preflight: PreflightModeSchema.default("redact"),
});
export type PrepareInput = z.infer<typeof PrepareInputSchema>;

/** The machine facts this operation needs, which the adapters own (machine/adapters). */
export interface PrepareDeps {
  /** Every session this machine can see, under the adapters' own roots. */
  discover(): Promise<readonly SessionRef[]>;
  /**
   * Both digests of one session's log, its size and its record count, from ONE pass — and, when
   * a sink is handed, that same pass writes the normalized stream into the material (#279); when
   * a scan is handed, it is what the stream is written THROUGH (#339). Both are PARAMETERS
   * rather than second verbs because reading a 240 MB log twice per preparation is the only cost
   * this operation has ever had, and a scan that read the log a second time would have paid it.
   */
  digests(
    ref: SessionRef,
    seal?: RecordSink | undefined,
    scan?: SecretScan | undefined,
  ): Promise<SessionDigests>;
  /**
   * How large this session's log is and when it was last written; `modifiedAt` 0 when nothing
   * could be observed. It is asked BEFORE the digests on purpose — one `stat` against a whole
   * read — because skipping a moving 240 MB log is the point, and because the same `stat` is
   * what says whether a kept reading of that log is still a reading OF IT (#236).
   */
  observe(ref: SessionRef): Promise<Observation>;
  /**
   * Where the material is sealed: the second output lease (`machine/output.ts`). Null for a
   * hand-run that bound none, which prepares a selection and seals no evidence — the receipt
   * says which, so a run whose material nothing can read is never mistaken for one that has it.
   */
  material?: MaterialSink | null | undefined;
  /** Where this run says it is; a caller that hands none is not watched (`progress.ts`). */
  progress?: ProgressChannel | undefined;
  /**
   * Where a reading is kept between preparations (`machine/cache.ts`, #236). Empty for an
   * invocation that was given no such directory — a hand-run, the tests that pass none — which
   * reads every log it selects, every time, and says so as `counts.reused` 0.
   */
  cacheDir?: string | undefined;
}

/** The version of the preparation record's shape AND of the normalization behind its source
 *  digest. It participates in the derivation, so a record written by a later schema can never
 *  collide with one written by this schema even if every other field matches. */
export const PREPARATION_SCHEMA = 3;

/** Separates this hash from every other use of SHA-256 in Babel: without a domain, a digest
 *  over some other structure that happened to serialize identically would be a valid
 *  preparation id. The `v3` is PREPARATION_SCHEMA's own, moved with it. */
const PREPARATION_DOMAIN = "babel/preparation/v3";

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

/**
 * Fixes a corpus scope and derives its identity.
 *
 * An empty selection is refused: an exploration over nothing is a mistake, and accepting it
 * would make a broken selection indistinguishable from a deliberate one. So is the same
 * session twice, which would let a scope claim a weight it does not have.
 */
export function newPreparation(
  preparedAt: string,
  selection: readonly PreparationEntry[],
): Preparation {
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
 *
 * WHICH IS WHY THE SCAN IS HERE AND NOT AFTER (#339). A redaction changes the record, so it has
 * to happen before the source digest covers it and before the sink receives it; a scan bolted on
 * afterwards would have produced a material whose index describes different bytes. The redacted
 * stream is therefore what the source digest is OF, and a corpus holding a credential digests
 * differently from the same corpus prepared raw — which is correct, and is the difference
 * between "the corpus changed" and "our reading of it changed" the two digests exist to state.
 */
export async function digests(
  ref: SessionRef,
  seal?: RecordSink | undefined,
  scan?: SecretScan | undefined,
): Promise<SessionDigests> {
  const reader = sessionDigester(seal, scan);
  for await (const chunk of Bun.file(ref.primaryPath).stream()) reader.write(chunk);
  return reader.finish();
}

/** What a redaction's locator recovers, and the digest saying it was recovered from the same
 *  bytes the preparation read. */
export interface ResolvedRedaction {
  /** The value that was redacted. It exists only in this process, on this machine. */
  readonly value: string;
  /** The log's capture digest as it stands now. A reader compares it against the material
   *  index's entry for this session: equal means the offsets still address what they addressed,
   *  different means the log moved and this is a different corpus. */
  readonly captureDigest: string;
}

/**
 * WHAT A REDACTION'S LOCATOR RESOLVES AGAINST, and why it can only be resolved here (#339).
 *
 * A marker in the material names a class, a record and a range and carries no value; this is the
 * only way back to the bytes, and it needs the session's own log — which lives on the machine
 * that prepared it and nowhere else. So a receipt, a run row and a refusal can all say exactly
 * what was found and where without any of them carrying a credential: the hub holds locators and
 * this function holds the door, and the door is on the machine.
 *
 * The record is re-normalized rather than read out of the material, because the material holds
 * the redacted stream: the value is gone from it by design. Null when this log holds no such
 * record, or when the range is not inside it — both of which mean the log is no longer the one
 * the preparation read, and the capture digest above is how a caller confirms that.
 */
export async function resolveRedaction(
  ref: SessionRef,
  site: { readonly line: number; readonly offset: number; readonly length: number },
): Promise<ResolvedRedaction | null> {
  const capture = new Bun.CryptoHasher("sha256");
  let found = "";
  const reader = recordReader((normalized, line) => {
    if (line !== site.line) return;
    found = normalized.endsWith("\n") ? normalized.slice(0, -1) : normalized;
  });
  for await (const chunk of Bun.file(ref.primaryPath).stream()) {
    capture.update(chunk);
    reader.write(chunk);
  }
  reader.finish();
  const captureDigest = `sha256:${capture.digest("hex")}`;
  const end = site.offset + site.length;
  if (found === "" || end > found.length) return null;
  return { value: found.slice(site.offset, end), captureDigest };
}

/**
 * How large a session's primary log is and when it was last written, in epoch ms; zeroes when
 * the file is gone or the filesystem answered nothing.
 *
 * A log that cannot be stat-ed is not treated as live: it is the digest pass that will fail over
 * it, with the path in the refusal, and "unreadable" is a better answer than "still being
 * written". Nor is it a log a reading may be kept of, for the same reason — an observation of
 * nothing matches nothing.
 */
export async function observe(ref: SessionRef): Promise<Observation> {
  const info = await stat(ref.primaryPath).catch(() => null);
  if (info === null) return { size: 0, modifiedAt: 0 };
  return { size: info.size, modifiedAt: Math.trunc(info.mtimeMs) };
}

export async function prepare(
  input: PrepareInput,
  out: OutputSink,
  deps: PrepareDeps,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId === "" ? `run_${crypto.randomUUID()}` : input.runId;
  const counts = {
    discovered: 0,
    selected: 0,
    bytes: 0,
    records: 0,
    live: 0,
    agent: 0,
    /** Spans the secret preflight replaced, over every session in the scope (#339). */
    redacted: 0,
    /**
     * Sessions served from a reading this machine already had (#236). It is on the receipt
     * because it is the only place the saving is visible: two preparations over one scope cost
     * the same wall-clock to an operator watching them, and this is what says the second one
     * did not read the corpus again.
     */
    reused: 0,
  };
  const rows: PreparedSessionRow[] = [];
  /** What each session's scan found, folded into the receipt's report at the end. */
  const scans: { readonly selector: string; readonly report: ScanReport }[] = [];
  /** The material's own index, built as the loop seals each session's stream. */
  const sealed: MaterialEntry[] = [];
  const retrievalHits: MaterialRetrieval["hits"] = [];
  let preparation: Preparation | null = null;
  let closure: Receipt["closure"] = "completed";
  let reason = "";
  const retrieval: SessionRetrieval | undefined =
    input.query === undefined
      ? undefined
      : {
          query: {
            digest: `sha256:${new Bun.CryptoHasher("sha256").update(termsQuery(input.query.text)).digest("hex")}`,
            limit: input.query.limit,
          },
          status: "complete",
          eligible: 0,
          indexed: 0,
          reused: 0,
          unavailable: 0,
          matches: null,
          overBound: 0,
        };
  const progress = deps.progress ?? SILENT;
  progress.report({
    stage: RUN_STAGES.preparing,
    message: "discovering the sessions this machine holds",
  });
  /**
   * The readings this machine keeps. Constructed here rather than handed in because the context
   * an entry is valid under is this input's — the preflight mode is part of it — and a cache
   * keyed on someone else's mode would serve an unscanned stream to a preparation that redacts.
   */
  const cache =
    (deps.cacheDir ?? "") === ""
      ? null
      : readingCache(deps.cacheDir ?? "", {
          schema: PREPARATION_SCHEMA,
          detectors: PREFLIGHT_DETECTORS,
          mode: input.preflight,
        });

  const discovered = await deps.discover();
  counts.discovered = discovered.length;
  const queried =
    input.query === undefined || retrieval === undefined
      ? null
      : await contentSelection(discovered, input, input.query, deps, retrieval, counts);
  const chosen = queried ?? choose(discovered, input.selectors);
  if (chosen.failure !== "") {
    // A selector that matches nothing, or matches two sessions, is a rejected invocation
    // rather than a silently smaller scope: a preparation records what was meant to be
    // explored, and a scope that quietly shrank would make the next run's coverage a mystery.
    closure = "failed";
    reason = chosen.failure;
  } else if (chosen.chosen.length === 0) {
    closure = "skipped";
    reason =
      retrieval === undefined
        ? "no session on this machine to prepare"
        : "no eligible session matches the content query within the material bounds";
  } else {
    const seenAt = new Date().toISOString();
    const selection: PreparationEntry[] = [];
    const named = input.selectors.length > 0 || queried !== null;
    const at = Date.now();
    // The loop already counts, so the fraction costs nothing and is the honest one: a digest
    // over a large log is where the minutes go, and "3/927" is what the operator wanted to see
    // on 2026-09-13 instead of silence.
    let examined = 0;
    try {
      for (const session of chosen.chosen) {
        examined += 1;
        progress.report({
          stage: RUN_STAGES.preparing,
          message: `${session.selector} (${String(examined)}/${String(chosen.chosen.length)})`,
          fraction: examined / chosen.chosen.length,
        });
        const seen = await deps.observe(session);
        if (
          queried !== null &&
          !sameObservation(queried.observations.get(session.primaryPath), seen)
        ) {
          closure = "failed";
          reason =
            "content selection refused: a selected session changed or disappeared before sealing";
          if (retrieval !== undefined) {
            retrieval.status = "unavailable";
            retrieval.unavailable++;
          }
          break;
        }
        const left = excluded(session, input, seen, at);
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
        const reused = cache === null ? null : await cache.reuse(session, seen);
        const record = queried?.hits.get(session.selector);
        const excerpt =
          record === undefined
            ? null
            : recallRecordReader({
                harness: session.harness,
                anchor: record.position,
              });
        let measured: SessionDigests;
        let found: ScanReport | null;
        if (cache !== null && reused !== null) {
          /*
          A READING THIS MACHINE ALREADY HAD (#236). The log is not opened at all: the kept
          stream is replayed into the material and HASHED as it goes, and what it hashes to is
          what the selection records. So the digest a citation carries is always a digest of the
          bytes that were actually sealed, and the cache's only remembered claim is the capture
          digest of a file whose size and mtime it just re-observed.

          A stream that does not hash to what it was kept as refuses the scope rather than
          falling back to a read. The entry is dropped, so the next preparation reads the log and
          succeeds — and the alternative, sealing bytes nothing verified into a material a model
          reads, is the one outcome worth failing a run over.
        */
          const seal = teeRecords(
            (await deps.material?.session(file)) ?? null,
            excerpt?.sink ?? null,
          );
          let digested = reused.sourceDigest;
          try {
            if (seal !== null) digested = await cache.replay(reused, seal);
          } finally {
            await seal?.close();
          }
          if (digested !== reused.sourceDigest) {
            await cache.forget(session);
            closure = "failed";
            reason =
              `the reading kept for ${session.selector} does not digest to what it was kept ` +
              `as; it has been dropped, and the next preparation reads the log`;
            if (retrieval !== undefined) {
              retrieval.status = "unavailable";
              retrieval.unavailable++;
            }
            break;
          }
          measured = {
            captureDigest: reused.captureDigest,
            sourceDigest: reused.sourceDigest,
            bytes: reused.bytes,
            records: reused.records,
          };
          found = reused.report;
          counts.reused++;
        } else {
          const seal = teeRecords(
            (await deps.material?.session(file)) ?? null,
            excerpt?.sink ?? null,
          );
          // The reading is kept in the SAME pass, off the same bytes, for the reason the scan is
          // in it: a second pass to fill a cache would have paid the cost the cache exists to
          // avoid.
          const kept = cache === null ? null : await cache.keep(session, seen);
          // NOTHING IS SEALED UNSCANNED (#339). The scan is what the stream is written THROUGH, so
          // the redaction happens before the sink and before the source digest — see `digests`.
          const scan = input.preflight === "off" ? null : secretScan();
          // BOTH SINKS ARE CLOSED BEFORE EITHER IS MEASURED. `digests` writes and never closes,
          // and a kept stream whose writer had not flushed would be committed at whatever length
          // happened to have reached the disk — a reading of a truncation.
          const into = teeRecords(seal, kept?.sink ?? null);
          try {
            measured = await deps.digests(session, into ?? undefined, scan ?? undefined);
          } catch (err) {
            // The scope is refused whole. A preparation missing one of the sessions it was asked
            // for would be an immutable record of a corpus nobody chose.
            await into?.close();
            await kept?.abandon();
            closure = "failed";
            reason =
              retrieval === undefined
                ? `read ${session.selector}: ${err instanceof Error ? err.message : String(err)}`
                : "content selection refused: a selected session could not be read";
            if (retrieval !== undefined) {
              retrieval.status = "unavailable";
              retrieval.unavailable++;
            }
            break;
          }
          await into?.close();
          found = scan === null ? null : scan.report();
          await kept?.commit({ ...measured, report: found });
        }
        if (queried !== null && !sameObservation(seen, await deps.observe(session))) {
          closure = "failed";
          reason = "content selection refused: a selected session changed while sealing";
          if (retrieval !== undefined) {
            retrieval.status = "unavailable";
            retrieval.unavailable++;
          }
          break;
        }
        if (found !== null) {
          scans.push({ selector: session.selector, report: found });
          counts.redacted += found.redactions;
          if (input.preflight === "refuse" && found.redactions > 0) {
            // The scope is refused whole, by classes and never by value. What this session already
            // wrote into the lease is the REDACTED stream, and no index is written for a failed
            // preparation, so the material binds nothing and holds no secret either way.
            closure = "failed";
            reason = refusalMessage(session.selector, found);
            break;
          }
        }
        if (excerpt !== null && record !== undefined) {
          const read = await excerpt.finish();
          if (
            !read.anchorMatches ||
            measured.sourceDigest !== record.sourceDigest ||
            measured.captureDigest !== record.captureDigest
          )
            throw new Error("content selection evidence changed before sealing");
          retrievalHits.push({
            selector: session.selector,
            harness: session.harness,
            file,
            captureDigest: measured.captureDigest,
            sourceDigest: measured.sourceDigest,
            record: record.position,
            excerpt: read.excerpt,
          });
        }
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
    } catch (error) {
      if (retrieval === undefined) throw error;
      closure = "failed";
      reason = "content selection refused: a selected session or its reading is unavailable";
      retrieval.status = "unavailable";
      retrieval.unavailable++;
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
          ...(queried !== null && input.preflight !== "off" && deps.material != null
            ? { retrievalFile: MATERIAL_RETRIEVAL }
            : {}),
        }
      : null;
  if (
    index?.retrievalFile !== undefined &&
    deps.material != null &&
    queried !== null &&
    retrieval !== undefined
  ) {
    const body = MaterialRetrievalSchema.parse({
      schema: "babel.material-retrieval/1",
      queryDigest: retrieval.query.digest,
      matches: queried.recordMatches,
      omitted: queried.recordMatches - retrievalHits.length,
      hits: retrievalHits,
    });
    let encoded = JSON.stringify(body);
    while (Buffer.byteLength(encoded) > RECALL_MAX_RESULT_BYTES && body.hits.length > 0) {
      body.hits.pop();
      body.omitted++;
      encoded = JSON.stringify(body);
    }
    const sidecar = await deps.material.session(MATERIAL_RETRIEVAL);
    try {
      sidecar.write(encoded);
    } finally {
      await sidecar.close();
    }
  }
  if (index !== null && deps.material != null) await deps.material.index(index);
  const preflight = preflightReport(input.preflight, scans);
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
    preflight,
    ...(retrieval === undefined ? {} : { retrieval }),
    ...(reason === "" ? {} : { reason }),
  };
  await out.receipt(receipt);
  return receipt;
}

/** Observation equality is deliberately stricter than size: an unknown mtime proves nothing. */
function sameObservation(before: Observation | undefined, after: Observation): boolean {
  return (
    before !== undefined &&
    before.modifiedAt > 0 &&
    before.modifiedAt === after.modifiedAt &&
    before.size === after.size
  );
}

/**
 * Pays for lexical coverage before choosing material. Only the ordinary redacted reading is
 * indexed; the final selection still passes through prepare's existing sealing and preflight.
 * Failure is whole-scope, never a partial search presented as complete coverage.
 */
async function contentSelection(
  discovered: readonly SessionRef[],
  input: PrepareInput,
  query: SessionContentQuery,
  deps: PrepareDeps,
  retrieval: SessionRetrieval,
  counts: { live: number; agent: number },
): Promise<{
  readonly chosen: readonly SessionRef[];
  readonly failure: string;
  readonly observations: ReadonlyMap<string, Observation>;
  readonly hits: ReadonlyMap<string, IndexedRecord>;
  readonly recordMatches: number;
}> {
  const observations = new Map<string, Observation>();
  const hits = new Map<string, IndexedRecord>();
  const refuse = (reason: string) => ({
    chosen: [],
    failure: reason,
    observations,
    hits,
    recordMatches: 0,
  });
  if (input.selectors.length > 0) {
    retrieval.status = "unavailable";
    return refuse("content selection refused: a query cannot be combined with explicit selectors");
  }
  if ((deps.cacheDir ?? "") === "") {
    retrieval.status = "unavailable";
    return refuse("content selection refused: a managed preparation cache is required");
  }
  const context: ReadingContext = {
    schema: PREPARATION_SCHEMA,
    detectors: PREFLIGHT_DETECTORS,
    mode: "redact",
  };
  const cache = readingCache(deps.cacheDir ?? "", context);
  const eligible: IndexedSession[] = [];
  let index: SessionIndex | null = null;
  try {
    const at = Date.now();
    for (const session of discovered) {
      const seen = await deps.observe(session);
      const left = excluded(session, input, seen, at);
      if (left !== null) {
        if (left.kind === "live") counts.live++;
        else counts.agent++;
        continue;
      }
      eligible.push({ session, seen });
      observations.set(session.primaryPath, seen);
    }
    retrieval.eligible = eligible.length;
    index = await sessionIndex(deps.cacheDir ?? "", context);
    for (const candidate of eligible) {
      if (index.holds(candidate)) {
        retrieval.reused++;
        continue;
      }
      const { session, seen } = candidate;
      const result = await index.build(candidate, async (sink) => {
        // The SQLite builder may have waited for a lock since discovery. Exclude before even
        // replaying a kept reading, not merely after the expensive read has already happened.
        const before = await deps.observe(session);
        if (
          !sameObservation(seen, before) ||
          excluded(session, input, before, Date.now()) !== null
        ) {
          throw new Error("eligible session changed before indexing");
        }
        const reused = await cache.reuse(session, seen);
        if (reused !== null) {
          // Finish hashing even if corrupt bytes fail the index parser early. Otherwise the
          // invalid reading survives every refused query; an index failure alone must not
          // discard a verified reading.
          let indexFailure: SessionIndexError | null = null;
          const digest = await cache.replay(reused, {
            ...sink,
            write(record) {
              if (indexFailure !== null) return;
              try {
                sink.write(record);
              } catch (error) {
                indexFailure =
                  error instanceof SessionIndexError ? error : new SessionIndexError("unavailable");
              }
            },
          });
          if (digest !== reused.sourceDigest) {
            await cache.forget(session);
            throw new Error("redacted reading failed verification");
          }
          if (indexFailure !== null) throw indexFailure;
          await sink.close();
          return { reading: reused, after: await deps.observe(session) };
        }
        const kept = await cache.keep(session, seen);
        if (kept === null) throw new Error("redacted reading cannot be kept");
        const scan = secretScan();
        const into = teeRecords(sink, kept.sink);
        let closed = false;
        try {
          const measured = await deps.digests(session, into ?? undefined, scan);
          await into?.close();
          closed = true;
          const reading = { ...measured, report: scan.report() };
          const after = await deps.observe(session);
          if (sameObservation(seen, after) && measured.bytes === seen.size) {
            await kept.commit(reading);
            if ((await cache.reuse(session, seen)) === null) {
              throw new Error("redacted reading could not be kept");
            }
          } else {
            await kept.abandon();
          }
          return { reading, after };
        } catch (error) {
          try {
            if (!closed) await into?.close();
          } finally {
            await kept.abandon();
          }
          throw error;
        }
      });
      if (result === "busy" || result === "changed") {
        retrieval.status = result === "busy" ? "busy" : "unavailable";
        retrieval.unavailable = eligible.length - retrieval.indexed - retrieval.reused;
        return refuse(
          result === "busy"
            ? "content selection refused: the session index is busy"
            : "content selection refused: an eligible session changed while indexing",
        );
      }
      if (result === "indexed") retrieval.indexed++;
      else retrieval.reused++;
    }
    // A covered session can change while another is read. Do not run a knowingly partial or
    // stale query; checking every eligible observation is cheap beside opening every log.
    for (const { session, seen } of eligible) {
      const after = await deps.observe(session);
      if (!sameObservation(seen, after) || excluded(session, input, after, Date.now()) !== null) {
        retrieval.status = "unavailable";
        retrieval.unavailable++;
        return refuse("content selection refused: an eligible session changed before selection");
      }
    }
    const found = index.search(query.text, eligible, query.limit, MAX_MATERIAL_BYTES);
    retrieval.matches = found.matches;
    retrieval.overBound = found.overBound;
    let recordMatches = 0;
    if (input.preflight !== "off") {
      const selected = new Set(found.selection.map((session) => session.primaryPath));
      const records = index.searchRecords(
        query.text,
        eligible.filter((candidate) => selected.has(candidate.session.primaryPath)),
        RECALL_MAX_HITS,
      );
      recordMatches = records.matches;
      for (const record of records.hits)
        if (!hits.has(record.candidate.session.selector))
          hits.set(record.candidate.session.selector, record);
    }
    return { chosen: found.selection, failure: "", observations, hits, recordMatches };
  } catch (error) {
    retrieval.status = error instanceof SessionIndexError ? error.kind : "unavailable";
    retrieval.unavailable = Math.max(1, eligible.length - retrieval.indexed - retrieval.reused);
    return refuse(
      retrieval.status === "busy"
        ? "content selection refused: the session index is busy"
        : "content selection refused: eligible session content or its index is unavailable",
    );
  } finally {
    index?.close();
  }
}

/** How many sites one receipt carries. The class counts above them are complete, and every
 *  locator is in the material's own markers, so this bounds a document rather than losing
 *  evidence: a corpus of a thousand leaky sessions must not write a receipt nobody can read. */
const MAX_RECEIPT_SITES = 64;

/**
 * THE PREPARATION'S OWN PREFLIGHT REPORT, folded from the per-session scans.
 *
 * It is written for every preparation, including one that scanned nothing, because a reviewer
 * asking "was this corpus checked before a provider read it?" must not have to read an absent
 * field as an answer. `mode` is what was asked for and `records` is what was actually read, so
 * `off` and "scanned and clean" cannot be confused: the first reports zero records.
 */
function preflightReport(
  mode: PreflightMode,
  scans: readonly { readonly selector: string; readonly report: ScanReport }[],
): PreflightReport {
  const classes = new Map<string, number>();
  const sites: PreflightReport["sites"] = [];
  let records = 0;
  let redactions = 0;
  let sitesOmitted = 0;
  for (const scanned of scans) {
    records += scanned.report.records;
    redactions += scanned.report.redactions;
    for (const row of scanned.report.classes) {
      classes.set(row.class, (classes.get(row.class) ?? 0) + row.redactions);
    }
    for (const site of scanned.report.sites) {
      if (sites.length < MAX_RECEIPT_SITES) sites.push({ ...site, selector: scanned.selector });
      else sitesOmitted += 1;
    }
    sitesOmitted += scanned.report.sitesOmitted;
  }
  return {
    schema: PREFLIGHT_SCHEMA,
    detectors: PREFLIGHT_DETECTORS,
    mode,
    records,
    redactions,
    classes: [...classes.entries()]
      .map(([name, count]) => ({ class: name, redactions: count }))
      .sort((a, b) => (a.class < b.class ? -1 : a.class > b.class ? 1 : 0)),
    sites,
    sitesOmitted,
  };
}

/** Why a session is not in a default scope; null when it belongs in one. */
type Exclusion = { readonly kind: "live" | "agent"; readonly reason: string } | null;

/**
 * Whether this session may be in a scope, and the sentence saying why not.
 *
 * The two rules are the ones #262 names, in the order that costs least: Babel's own transcripts
 * are recognized from the path alone, and liveness is read off the one `stat` the loop already
 * took — so a 240 MB log that is still being appended is excluded without being read, which is
 * the whole point of asking here rather than after the digests.
 *
 * A log written in the FUTURE is live. Clocks on one machine disagree by seconds, and a file
 * whose mtime is ahead of this process is the last thing to treat as settled.
 */
function excluded(
  session: SessionRef,
  input: PrepareInput,
  seen: Observation,
  at: number,
): Exclusion {
  if (!input.agentSessions && babelOwnLog(session.primaryPath)) {
    return {
      kind: "agent",
      reason:
        `${session.selector} is one of Babel's own runs' transcripts, which a preparation of ` +
        `the operator's work does not read`,
    };
  }
  if (seen.modifiedAt > 0 && at - seen.modifiedAt < LIVE_GRACE_MS) {
    const seconds = Math.max(0, Math.round((at - seen.modifiedAt) / 1000));
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
