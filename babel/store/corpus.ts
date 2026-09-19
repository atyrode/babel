import { createHash } from "node:crypto";
import { MAX_SQL_BATCH_STATEMENTS } from "@manifold/plugin";
import type { PluginDatabase, SqlStatement } from "@manifold/plugin";
import { nameableRecordSql, recordTextSql } from "./schema.ts";

/*
  THE CORPUS INDEX (#337): what the records say, in two indexes, and the one door that reads them.

  WHAT WAS WRONG. A preparation selected sessions by recency or by topic and the feed filtered
  structured columns; nothing indexed what a record SAID, so nothing could retrieve against it.
  The measurement is in `docs/jev-case-study-audit.md`: a correctly matched record scores 2.043
  out of 3 on real work, the mechanism that picked one scored 0.606, and picking at random scored
  0.680. Babel's content was good and its retrieval lost to a coin, because there was no
  retrieval.

  TWO INDEXES, AND THE SPLIT IS NOT AN OPTIMIZATION. `record_terms` answers "these words", and it
  is the baseline's own: FTS5 is in the runtime, it costs no dependency, no model, no account and
  NO OUTBOUND CALL EVER, and it works on a deployment that installs nothing. `record_vectors`
  answers "this meaning", and it needs a model, which Babel does not have and will not hold a
  credential for — so it needs the operator to install one, and until he does it is simply
  absent. A search says which of the two answered, every time.

  WHY THE MEANING HALF IS SHAPED LIKE THIS, and it was the host's budget that shaped it:

    - `load_extension` is refused to a plugin (manifold `packages/plugin/src/database.ts`), so
      there is no vector extension below the SQL boundary. Similarity is computed here.
    - one SQL call may return 4 MiB (`MAX_SQL_RESULT_BYTES`). The imported corpus is 6,038
      records; at 768 float32 dimensions that is 18.5 MB, so the full vectors CANNOT all cross
      the boundary in a query. A brute-force scan is not slow here, it is impossible.

  So every vector is stored twice: once as float32, and once as ONE SIGN BIT PER DIMENSION. The
  bits are 96 bytes a record — 580 KB for the whole corpus, one query — and Hamming distance over
  them orders candidates. The float vectors of the best {@link PROBE_DEPTH} are then read back
  and scored exactly.

  WHAT THE SKETCH LOSES, stated rather than buried. A sign-bit sketch keeps the quadrant and
  throws away the magnitude of every coordinate, so its ordering is a correlate of the angle and
  not the angle. At a depth of 256 over 6,038 records the prefilter carries 4.2% of the corpus
  into the exact stage, and a true nearest neighbour that the sketch ranked 257th is missed. That
  is why a result says `approximate` whenever the prefilter actually cut — a caller can tell
  "nothing in the corpus matched" (`scanned` is 0) from "this was drawn from a sketch"
  (`scanned` exceeds `rescored`) — and why a search that must not miss reads the keyword half,
  which is exhaustive.

  A VECTOR CARRIES THE MODEL THAT MADE IT and a search reads only the rows matching the model it
  embedded its own query with. Two models' vectors are not comparable — the same text in two
  embedding spaces has no defined angle between its images — and the failure mode is not an error
  but a confident wrong ordering that nothing downstream would notice. Rows under another model
  are therefore not compared and not deleted: they are PENDING WORK, which is exactly what
  changing the model should mean, and the backfill re-embeds them.

  NOTHING HERE IS BACKED UP AND NOTHING HERE IS AUTHORITATIVE. Every row of both indexes is
  derived from `records`, and {@link rebuildTerms} plus {@link pendingVectors} are the whole of
  the reconstruction on a machine that has never seen this file.
*/

/** The one handle this module reads and writes through. */
export interface CorpusStore {
  readonly db: PluginDatabase;
}

/**
 * One embedding, and the model that produced it.
 *
 * `model` is not decoration: it is written into every row the vector reaches and is the key a
 * later search matches on. An embedder that cannot say what model answered cannot be used here,
 * because what it produced could never be told from what the next model produces.
 */
export interface Embedding {
  readonly model: string;
  readonly values: readonly number[];
}

/**
 * How text becomes a vector, or `null` because it cannot right now.
 *
 * ONE BRANCH, on `askJev`'s rule: an absent service, a disabled one, a revoked credential, an
 * exhausted account and an unreachable origin are one answer, because a caller whose behaviour
 * differed between them would be a caller whose behaviour changes when a card expires. `null`
 * writes no row, so the record stays pending and the next tick asks again.
 */
export type Embedder = (text: string) => Promise<Embedding | null>;

/**
 * The most text one embedding call carries, in bytes, measured as the host measures a service
 * input. It is `JEV_CALL_CAP_BYTES`' discipline at the same number and for the same reason: a
 * record several times the largest anyone has written still fits, and a pasted session does not.
 *
 * Over-long text is TRUNCATED here rather than refused, which is the opposite of the judgement
 * path's choice and right for the opposite reason: a judgement of a record with its middle
 * removed is a judgement of something the operator never sees, while an embedding of a record's
 * first 8 KB is a usable approximate position for a record whose first 8 KB is nearly all of it.
 */
export const EMBED_TEXT_CAP_BYTES = 8192;

/** How many sketch candidates are rescored exactly. See the header on what the depth costs. */
export const PROBE_DEPTH = 256;

/** How many records one backfill pass embeds. One service call each; see `server/drain.ts`. */
export const BACKFILL_BATCH = 24;

/** How many rows one rebuild statement writes, under the batch's own five-second deadline. */
const REBUILD_CHUNK = 2000;

/** The reason column's one value: a permanent, local fact, never a service that was down. */
const NO_TEXT = "this record carries no text to embed";

// ---------------------------------------------------------------------------- the keyword index

/**
 * Rebuilds the keyword index from `records` alone, and answers how many rows it wrote.
 *
 * THIS IS THE ONE PASS THE ISSUE ASKS FOR. `record_terms_follow` keeps the index current for
 * every record written after this shape existed; this is for the 6,038 that arrived before it,
 * and for a machine restoring a corpus from an import with no index of its own. It deletes
 * first, so it is idempotent and so a crashed pass costs a repeat rather than a duplicate: an
 * FTS5 table has no unique constraint to lean on, and an anti-join against an UNINDEXED column
 * is a full scan per record.
 *
 * The write is chunked by rowid because a batch has a five-second deadline and one statement
 * inside it cannot be interrupted. The text is `recordTextSql`, the same expression the trigger
 * writes, so a rebuilt row and a followed row cannot disagree.
 */
export async function rebuildTerms(store: CorpusStore): Promise<number> {
  await store.db.run("DELETE FROM record_terms");
  let after = 0;
  let written = 0;
  for (;;) {
    const chunk = await store.db.query<{ id: string; rowid: number }>(
      `SELECT rowid AS rowid, id AS id FROM records WHERE rowid > ? ORDER BY rowid LIMIT ?`,
      [after, REBUILD_CHUNK],
    );
    if (chunk.length === 0) break;
    const last = chunk[chunk.length - 1];
    if (last === undefined) break;
    await store.db.run(
      `INSERT INTO record_terms(record_id, title, body)
       SELECT r.id, r.title, ${recordTextSql("r.")}
         FROM records r WHERE r.rowid > ? AND r.rowid <= ?`,
      [after, last.rowid],
    );
    written += chunk.length;
    after = last.rowid;
  }
  return written;
}

/** How many records exist and how many the keyword index holds. */
async function termCounts(store: CorpusStore): Promise<{ records: number; terms: number }> {
  const rows = await store.db.query<{ records: number; terms: number }>(
    `SELECT (SELECT COUNT(*) FROM records) AS records,
            (SELECT COUNT(*) FROM record_terms) AS terms`,
  );
  const row = rows[0];
  return { records: Number(row?.records ?? 0), terms: Number(row?.terms ?? 0) };
}

/**
 * Rebuilds the keyword index when it holds fewer rows than there are records, and answers what
 * it did.
 *
 * The counts are the whole test, and they are cheap. A store that reached this shape by addition
 * has every record and no term; one whose rebuild was interrupted has some of each; one that has
 * been current since the trigger arrived has neither gap and this costs two counts. It cannot
 * detect a row whose TEXT is stale — that would need the expression evaluated per record — and
 * changing `recordTextSql` is therefore a deliberate `DELETE FROM record_terms` away from a
 * rebuild, which is a decision rather than a drift.
 */
export async function ensureTerms(store: CorpusStore): Promise<number> {
  const counted = await termCounts(store);
  if (counted.terms >= counted.records) return 0;
  return await rebuildTerms(store);
}

/**
 * The FTS5 query one line of operator prose becomes.
 *
 * EVERY TERM IS QUOTED AND THE TERMS ARE OR-ED. Quoting is what makes the input text rather than
 * syntax: `NEAR`, `*`, `-` and a stray double quote are FTS5 operators, and a search box that
 * raised `fts5: syntax error` at a hyphen would be a search box nobody uses twice.
 *
 * OR rather than FTS5's implicit AND, because bm25 already does the work AND would do badly: it
 * sums a per-term contribution weighted by how rare the term is, so a record matching four terms
 * of five outranks one matching two, while AND answers NOTHING for a five-word question. A
 * corpus whose measured problem is that retrieval loses to chance cannot afford a zero-recall
 * default.
 */
export function termsQuery(query: string): string {
  const terms = query
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((term) => term.length > 1)
    .slice(0, 32);
  return terms.map((term) => `"${term}"`).join(" OR ");
}

/** One record the keyword index matched, and bm25's score for it; lower is better. */
interface TermHit {
  readonly id: string;
  readonly rank: number;
}

async function keywordHits(
  store: CorpusStore,
  query: string,
  limit: number,
): Promise<readonly TermHit[]> {
  const match = termsQuery(query);
  if (match === "") return [];
  const rows = await store.db.query<{ record_id: string; rank: number }>(
    `SELECT record_id, bm25(record_terms) AS rank
       FROM record_terms WHERE record_terms MATCH ? AND ${nameableRecordSql("record_id")}
       ORDER BY rank LIMIT ?`,
    [match, limit],
  );
  return rows.map((row) => ({ id: String(row.record_id), rank: Number(row.rank) }));
}

// ---------------------------------------------------------------------------- the meaning index

/** Bits set in each byte value, so a Hamming distance is a table walk rather than a loop. */
const POPCOUNT = new Uint8Array(256);
for (let value = 0; value < 256; value += 1) {
  POPCOUNT[value] = (value & 1) + (POPCOUNT[value >> 1] ?? 0);
}

/**
 * The unit-length float32 bytes of a vector, so an exact score is a dot product.
 *
 * Normalizing at WRITE time rather than at read time is what keeps the rescore a single pass over
 * the candidate bytes: cosine of two unit vectors is their dot product, and the alternative is
 * recomputing two magnitudes per candidate per query for a value that never changes. A zero
 * vector — which a model should never return and a broken projection might — normalizes to
 * itself and scores 0 against everything, which is the honest answer for a position that carries
 * no direction.
 */
function unitBytes(values: readonly number[]): Uint8Array {
  const floats = new Float32Array(values.length);
  let sum = 0;
  for (let i = 0; i < values.length; i += 1) {
    const value = values[i] ?? 0;
    sum += value * value;
  }
  const norm = Math.sqrt(sum);
  for (let i = 0; i < values.length; i += 1) {
    floats[i] = norm === 0 ? 0 : (values[i] ?? 0) / norm;
  }
  return new Uint8Array(floats.buffer);
}

/** One sign bit per dimension, most significant bit first, as the prefilter reads them. */
function probeBits(values: readonly number[]): Uint8Array {
  const bits = new Uint8Array(Math.ceil(values.length / 8));
  for (let i = 0; i < values.length; i += 1) {
    if ((values[i] ?? 0) <= 0) continue;
    const at = i >> 3;
    bits[at] = (bits[at] ?? 0) | (0x80 >> (i & 7));
  }
  return bits;
}

function hamming(a: Uint8Array, b: Uint8Array): number {
  const width = Math.min(a.length, b.length);
  let distance = 0;
  for (let i = 0; i < width; i += 1) {
    distance += POPCOUNT[((a[i] ?? 0) ^ (b[i] ?? 0)) & 0xff] ?? 0;
  }
  return distance;
}

function dot(a: Uint8Array, b: Float32Array): number {
  // The stored bytes are a Float32Array's own buffer; a misaligned or short blob is a row this
  // build did not write, and scoring it as 0 is better than reading past it.
  if (a.byteLength !== b.length * 4 || a.byteOffset % 4 !== 0) return 0;
  const floats = new Float32Array(a.buffer, a.byteOffset, b.length);
  let total = 0;
  for (let i = 0; i < b.length; i += 1) total += (floats[i] ?? 0) * (b[i] ?? 0);
  return total;
}

/** The text an embedding is computed from: the record's own prose, capped, and nothing else. */
export function embedText(title: string, body: string): string {
  const joined = `${title}\n${body}`.trim();
  const bytes = new TextEncoder().encode(joined);
  if (bytes.length <= EMBED_TEXT_CAP_BYTES) return joined;
  return new TextDecoder().decode(bytes.subarray(0, EMBED_TEXT_CAP_BYTES));
}

/** One record the backfill has not embedded under the model in force, and its text. */
export interface PendingRecord {
  readonly id: string;
  readonly text: string;
}

/**
 * Records with no vector under this model, oldest first, bounded.
 *
 * RESUMPTION IS THIS QUERY AND THERE IS NO CURSOR. A backfill that stopped — a tick that ended, a
 * process that died, a hub restarted — leaves the rows it wrote and the rows it did not, and the
 * next pass asks the same question and gets the remainder. A stored position would be a second
 * authority that could disagree with the rows themselves, and the failure it would cause is the
 * quiet one: a corpus reported as embedded that is not.
 *
 * The join carries the model, so a row written under a model the operator has since changed is
 * pending rather than present. That is the re-embed, and it needs no second mechanism.
 */
export async function pendingVectors(
  store: CorpusStore,
  model: string,
  limit: number,
): Promise<readonly PendingRecord[]> {
  const rows = await store.db.query<{ record_id: string; title: string; body: string }>(
    `SELECT t.record_id AS record_id, t.title AS title, t.body AS body
       FROM record_terms t
       LEFT JOIN record_vectors v ON v.record_id = t.record_id AND v.model = ?
      WHERE v.record_id IS NULL
      ORDER BY t.rowid LIMIT ?`,
    [model, limit],
  );
  return rows.map((row) => ({
    id: String(row.record_id),
    text: embedText(String(row.title ?? ""), String(row.body ?? "")),
  }));
}

/**
 * Writes one record's vector, replacing whatever model's vector was there.
 *
 * `INSERT OR REPLACE` rather than an insert that fails, because the row is DERIVED: a second
 * answer for the same record under a new model supersedes nothing — it recomputes something.
 * That is also why neither table here has the append-only triggers every table that records an
 * ACT carries.
 */
function vectorStatement(
  id: string,
  model: string,
  values: readonly number[],
  text: string,
  at: string,
): SqlStatement {
  const digest = createHash("sha256").update(text).digest("hex");
  return {
    sql: `INSERT OR REPLACE INTO record_vectors
            (record_id, model, dims, probe, vector, digest, reason, embedded_at)
          VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
    params: [id, model, values.length, probeBits(values), unitBytes(values), digest, at],
  };
}

/** The row a record with nothing to embed gets, so no later pass offers it again. */
function emptyStatement(id: string, model: string, at: string): SqlStatement {
  return {
    sql: `INSERT OR REPLACE INTO record_vectors
            (record_id, model, dims, probe, vector, digest, reason, embedded_at)
          VALUES (?, ?, 0, X'', X'', '', ?, ?)`,
    params: [id, model, NO_TEXT, at],
  };
}

/** What one backfill pass did. */
export interface BackfillReport {
  /** Records whose vector was written this pass. */
  readonly embedded: number;
  /** Records recorded as having nothing to embed. */
  readonly empty: number;
  /** Records offered to the embedder that it could not answer for; they stay pending. */
  readonly unanswered: number;
  /** Records still without a vector under this model after this pass. */
  readonly remaining: number;
  /** The model the pass wrote under, or empty when nothing could be embedded at all. */
  readonly model: string;
}

/**
 * Embeds up to `limit` records that have no vector under the embedder's own model.
 *
 * THE MODEL IS THE EMBEDDER'S TO NAME, and it is learned by asking rather than read off a
 * configuration: a configuration says what the operator asked to route to and an answer says what
 * answered, and the whole purpose of recording the model is that those two can disagree.
 *
 * A PASS THAT HAS NOTHING TO DO MAKES NO CALL. The model of the newest row is what the pending set
 * is computed against first, so a fully covered corpus costs two queries and reaches no origin —
 * a drain that ticks for two hours must not make one paid call per tick for ever, which is exactly
 * the standing cost the decision weighed. The consequence is deliberate and stated: a model
 * changed while the corpus is fully covered is not noticed until SOMETHING is pending, which the
 * next record written makes true. Until then a search reports every row as `stale` and embeds
 * nothing, which is the visible form of "your model changed"; forcing the re-embed of a static
 * corpus is `DELETE FROM record_vectors`, an operator's act on `session_titles`' own rule.
 *
 * A MODEL THAT CHANGED SINCE THE LAST PASS IS PICKED UP ON THIS ONE. The first pending record's
 * own answer names the model in force; if that is not the model the pending set was read under,
 * the set is read again under the new name and the pass proceeds there. Nothing is written under
 * the old one.
 *
 * BOUNDED AND RESUMABLE. The bound is `limit`; the resumption is {@link pendingVectors}. Rows are
 * written in ONE batch at the end, because a batch is the transaction — a pass either records
 * what it paid for or records none of it, and a crash between two service calls costs at most
 * this pass's calls rather than leaving a half-written slice nothing can account for.
 */
export async function backfillVectors(
  store: CorpusStore,
  embed: Embedder,
  at: string,
  limit: number = BACKFILL_BATCH,
): Promise<BackfillReport> {
  // One write per record and one batch per pass, so the pass can be no wider than a batch is
  // long. A caller asking for more would have its work silently rolled back by the engine's own
  // bound rather than split, and a pass that wrote nothing while reporting what it paid for is
  // the one failure the single transaction exists to prevent.
  const bound = Math.max(1, Math.min(limit, MAX_SQL_BATCH_STATEMENTS));
  const newest = await store.db.query<{ model: string }>(
    `SELECT model FROM record_vectors ORDER BY embedded_at DESC, record_id DESC LIMIT 1`,
  );
  const known = String(newest[0]?.model ?? "");
  let model = known;
  let pending = await pendingVectors(store, known, bound);
  if (pending.length === 0) {
    return { embedded: 0, empty: 0, unanswered: 0, remaining: 0, model: known };
  }
  const writes: SqlStatement[] = [];
  let unanswered = 0;
  let empty = 0;
  // The model is not known yet when nothing has ever been embedded, and may have changed since
  // the newest row. Either way the first record with text is what answers the question, and its
  // answer is the call this pass owed that record anyway.
  const sounding = pending.find((record) => record.text !== "");
  if (sounding !== undefined) {
    const answer = await embed(sounding.text);
    if (answer === null) {
      return { embedded: 0, empty: 0, unanswered: pending.length, remaining: 0, model: "" };
    }
    if (answer.model !== known) {
      model = answer.model;
      pending = await pendingVectors(store, model, bound);
    }
    writes.push(vectorStatement(sounding.id, model, answer.values, sounding.text, at));
  }
  for (const record of pending) {
    if (record.id === sounding?.id) continue;
    if (record.text === "") {
      // A record with no text is a permanent local fact, but the row still has to name a model,
      // because the pending query is per model and a row under `''` would be invisible to every
      // one of them. So it waits for a pass in which some model is known — which is any pass
      // where one record has text — rather than costing a call of its own to learn a name it
      // does not use. A corpus of nothing but blank records therefore makes no call at all.
      if (model === "") {
        unanswered += 1;
        continue;
      }
      writes.push(emptyStatement(record.id, model, at));
      empty += 1;
      continue;
    }
    const answer = await embed(record.text);
    if (answer === null) {
      unanswered += 1;
      continue;
    }
    // A model that changed mid-pass writes nothing under the name the pass opened with: mixing
    // two models' vectors under one label is the failure this refuses to make, and the record
    // stays pending for the pass that reads the new name from its first answer.
    if (answer.model !== model) {
      unanswered += 1;
      continue;
    }
    writes.push(vectorStatement(record.id, model, answer.values, record.text, at));
  }
  if (writes.length > 0) await store.db.batch(writes);
  const left = await store.db.query<{ n: number }>(
    `SELECT COUNT(*) AS n FROM record_terms t
       LEFT JOIN record_vectors v ON v.record_id = t.record_id AND v.model = ?
      WHERE v.record_id IS NULL`,
    [model],
  );
  return {
    embedded: writes.length - empty,
    empty,
    unanswered,
    remaining: Number(left[0]?.n ?? 0),
    model,
  };
}

// ---------------------------------------------------------------------------- searching

/** How much of the corpus each index holds, as a search reports it. */
export interface CorpusCoverage {
  readonly records: number;
  /** Records the keyword index holds. */
  readonly keyworded: number;
  /** Records with a usable vector under the model this search used. */
  readonly embedded: number;
  /** Records recorded as having no text to embed; they can never be more than this. */
  readonly empty: number;
  /** Records whose only vector was made by a different model, and are therefore pending. */
  readonly stale: number;
  /**
   * Records whose stored id is not one {@link isRecordId} admits (#426). They are left out of
   * every answer because no caller could ask for one, and they are counted so an answer that
   * is short says why rather than looking complete.
   */
  readonly unnameable: number;
  readonly model: string;
}

/** Whether the meaning half answered, and how completely. */
export type MeaningState = "absent" | "partial" | "full";

/** One record a search matched. */
export interface CorpusHit {
  readonly id: string;
  readonly title: string;
  /** Which index found it. */
  readonly via: "keyword" | "meaning" | "both";
  /** The fused rank score; higher is better. Comparable only within one answer. */
  readonly score: number;
  /** bm25's own score, when the keyword index matched it; lower is better. */
  readonly keyword: number | null;
  /** Cosine similarity against the query, when the meaning index matched it. */
  readonly meaning: number | null;
}

export interface CorpusSearch {
  readonly hits: readonly CorpusHit[];
  readonly coverage: CorpusCoverage;
  readonly meaning: MeaningState;
  /** Why the meaning half did not answer; empty when it did. */
  readonly meaningAbsent: string;
  /** Vectors the prefilter compared. 0 means there was nothing to compare, not a near miss. */
  readonly scanned: number;
  /** Vectors scored exactly. Fewer than `scanned` means the answer came through the sketch. */
  readonly rescored: number;
  /** True when the prefilter cut: a better match may exist outside the slice that was scored. */
  readonly approximate: boolean;
}

/** What a search was asked for. */
export interface CorpusQuery {
  readonly query: string;
  readonly limit: number;
  /** Record kinds to answer with; empty means every kind. */
  readonly kinds: readonly string[];
}

async function coverageOf(store: CorpusStore, model: string): Promise<CorpusCoverage> {
  const counted = await termCounts(store);
  const rows = await store.db.query<{
    embedded: number;
    empty: number;
    stale: number;
    unnameable: number;
  }>(
    `SELECT COALESCE(SUM(CASE WHEN model = ? AND dims > 0 THEN 1 ELSE 0 END), 0) AS embedded,
            COALESCE(SUM(CASE WHEN model = ? AND dims = 0 THEN 1 ELSE 0 END), 0) AS empty,
            COALESCE(SUM(CASE WHEN model <> ? THEN 1 ELSE 0 END), 0) AS stale,
            (SELECT COUNT(*) FROM records r WHERE NOT (${nameableRecordSql("r.id")})) AS unnameable
       FROM record_vectors`,
    [model, model, model],
  );
  const row = rows[0];
  return {
    records: counted.records,
    keyworded: counted.terms,
    embedded: Number(row?.embedded ?? 0),
    empty: Number(row?.empty ?? 0),
    stale: Number(row?.stale ?? 0),
    unnameable: Number(row?.unnameable ?? 0),
    model,
  };
}

/** One record the meaning index matched, and its cosine similarity. */
interface MeaningHits {
  readonly hits: ReadonlyMap<string, number>;
  readonly order: readonly string[];
  readonly scanned: number;
  readonly rescored: number;
}

/**
 * The prefilter and the exact stage, in two queries.
 *
 * The first reads every sketch under this model — 96 bytes a record, the only read here whose
 * size grows with the corpus, and the reason the sketch exists. The second reads back the float
 * vectors of the best {@link PROBE_DEPTH} by Hamming distance and scores them by dot product,
 * which is cosine because both sides are unit length.
 */
async function meaningHits(
  store: CorpusStore,
  query: Embedding,
  limit: number,
): Promise<MeaningHits> {
  const wanted = probeBits(query.values);
  const sketches = await store.db.query<{ record_id: string; probe: Uint8Array }>(
    `SELECT record_id, probe FROM record_vectors WHERE model = ? AND dims = ?
       AND ${nameableRecordSql("record_id")}`,
    [query.model, query.values.length],
  );
  if (sketches.length === 0) {
    return { hits: new Map(), order: [], scanned: 0, rescored: 0 };
  }
  const ranked = sketches
    .map((row) => ({
      id: String(row.record_id),
      distance: hamming(wanted, row.probe instanceof Uint8Array ? row.probe : new Uint8Array()),
    }))
    .sort((a, b) => a.distance - b.distance)
    .slice(0, PROBE_DEPTH);
  const ids = ranked.map((entry) => entry.id);
  const unit = new Float32Array(query.values.length);
  {
    let sum = 0;
    for (const value of query.values) sum += value * value;
    const norm = Math.sqrt(sum);
    for (let i = 0; i < query.values.length; i += 1) {
      unit[i] = norm === 0 ? 0 : (query.values[i] ?? 0) / norm;
    }
  }
  const holes = ids.map(() => "?").join(",");
  const vectors = await store.db.query<{ record_id: string; vector: Uint8Array }>(
    `SELECT record_id, vector FROM record_vectors WHERE record_id IN (${holes})`,
    ids,
  );
  const scored = vectors
    .map((row) => ({
      id: String(row.record_id),
      score: dot(row.vector instanceof Uint8Array ? row.vector : new Uint8Array(), unit),
    }))
    .sort((a, b) => b.score - a.score)
    .slice(0, limit);
  return {
    hits: new Map(scored.map((entry) => [entry.id, entry.score])),
    order: scored.map((entry) => entry.id),
    scanned: sketches.length,
    rescored: ranked.length,
  };
}

/**
 * The fusion constant of reciprocal rank fusion, at its usual 60.
 *
 * RANKS ARE FUSED RATHER THAN SCORES because the two scores are not commensurable: bm25 is an
 * unbounded negative number whose scale depends on the corpus's own term statistics, and cosine
 * is bounded in [-1, 1]. Normalizing either into the other's range would invent a conversion
 * nobody can defend; a record's POSITION in each list is the one thing both agree on. The
 * constant damps the top of each list so one index's first place cannot by itself decide the
 * answer.
 */
const RRF_K = 60;

/**
 * Searches the corpus by words and by meaning, and says which half answered.
 *
 * A DEPLOYMENT THAT INSTALLED NO EMBEDDING SERVICE GETS THE KEYWORD HALF AND MAKES NO OUTBOUND
 * CALL. `embed` is `null` there and no branch below reaches a service; the answer is complete,
 * exhaustive over the words it was given, and `meaning` says `absent` with the reason. That is
 * the property worth keeping: Babel without a policy installed behaves exactly as Babel did
 * before this existed.
 */
export async function searchCorpus(
  store: CorpusStore,
  embed: Embedder | null,
  query: CorpusQuery,
): Promise<CorpusSearch> {
  const width = Math.max(query.limit * 4, 32);
  const keyword = await keywordHits(store, query.query, width);
  let meaning: MeaningHits = { hits: new Map(), order: [], scanned: 0, rescored: 0 };
  let model = "";
  let absent = "no embedding service is installed, so this answer is by keyword alone";
  if (embed !== null) {
    const embedded = await embed(query.query);
    if (embedded === null) {
      absent = "the embedding service did not answer, so this answer is by keyword alone";
    } else {
      model = embedded.model;
      meaning = await meaningHits(store, embedded, width);
      absent =
        meaning.scanned === 0
          ? `no record has been embedded by ${model} yet, so this answer is by keyword alone`
          : "";
    }
  }
  const coverage = await coverageOf(store, model);
  const scores = new Map<string, number>();
  // Both retrieval lanes exclude unaddressable ids before their candidate bounds, so even a
  // whole leading slice of legacy damage cannot crowd valid records out of the fused answer.
  const add = (id: string, rank: number): void => {
    scores.set(id, (scores.get(id) ?? 0) + 1 / (RRF_K + rank + 1));
  };
  keyword.forEach((hit, rank) => {
    add(hit.id, rank);
  });
  meaning.order.forEach((id, rank) => {
    add(id, rank);
  });
  const bm25 = new Map(keyword.map((hit) => [hit.id, hit.rank]));
  const ids = [...scores.keys()];
  if (ids.length === 0) {
    return {
      hits: [],
      coverage,
      meaning: meaningState(coverage, meaning.scanned),
      meaningAbsent: absent,
      scanned: meaning.scanned,
      rescored: meaning.rescored,
      approximate: false,
    };
  }
  // The title and the kind come from `records` rather than from the index: the index holds a copy
  // of the text for matching, and a reader must be shown the record's own row.
  const holes = ids.map(() => "?").join(",");
  const kinds =
    query.kinds.length === 0 ? "" : ` AND kind IN (${query.kinds.map(() => "?").join(",")})`;
  const rows = await store.db.query<{ id: string; title: string }>(
    `SELECT id, title FROM records WHERE id IN (${holes})${kinds}`,
    [...ids, ...query.kinds],
  );
  const hits: CorpusHit[] = [];
  for (const row of rows) {
    const id = String(row.id);
    const key = bm25.has(id);
    const mean = meaning.hits.has(id);
    hits.push({
      id,
      title: String(row.title ?? ""),
      via: key && mean ? "both" : mean ? "meaning" : "keyword",
      score: scores.get(id) ?? 0,
      keyword: key ? (bm25.get(id) ?? null) : null,
      meaning: mean ? (meaning.hits.get(id) ?? null) : null,
    });
  }
  hits.sort((a, b) => b.score - a.score || a.id.localeCompare(b.id));
  return {
    hits: hits.slice(0, query.limit),
    coverage,
    meaning: meaningState(coverage, meaning.scanned),
    meaningAbsent: absent,
    scanned: meaning.scanned,
    rescored: meaning.rescored,
    // The prefilter cut only when it had more to compare than it carried forward. Saying so on
    // an answer it did not cut would make "approximate" mean "a vector was involved".
    approximate: meaning.scanned > meaning.rescored,
  };
}

/**
 * Whether the meaning half covered the corpus, covered part of it, or was not there.
 *
 * PARTIAL IS THE ORDINARY STATE AND IT IS USABLE. A backfill that has embedded a third of the
 * corpus answers over that third and says `partial`; it does not refuse, and it does not pretend.
 * A record with no text is counted as covered because it never can be embedded and a corpus of
 * them would otherwise read as permanently partial.
 */
function meaningState(coverage: CorpusCoverage, scanned: number): MeaningState {
  if (scanned === 0) return "absent";
  return coverage.embedded + coverage.empty >= coverage.records ? "full" : "partial";
}
