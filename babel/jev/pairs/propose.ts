import type { SearchQuery, SearchResult } from "../../contract.ts";
import { chronological, pairKey, type PairRecord, type RecordPair } from "./pair.ts";

/*
  HOW A CANDIDATE PAIR IS PROPOSED (#357, #358), WHICH IS THE FIRST QUESTION THE SLICE HAD TO
  ANSWER.

  THE ARITHMETIC THAT RULES OUT THE OBVIOUS DESIGN. 6,038 imported records is 18,225,703 unordered
  pairs. At the study's own measured price of $4×10⁻⁶ a judgement that is not merely slow — it is
  seventy-odd dollars and eighteen million service calls for one pass over a corpus that grows
  every cycle, against 6,038 calls for the per-record screen. Enumeration is not a tuning choice
  here; it is the reason #357 and #358 record that nothing in Babel has ever looked for either
  relation.

  SO A PAIR IS RETRIEVED, NOT ENUMERATED, AND #413 IS WHAT MADE THAT POSSIBLE. Each record is used
  once as an ANCHOR: one search with the record's own prose as the query, and the neighbours that
  come back are its candidate counterparts. That is `anchors × neighbours` pairs rather than
  `n(n-1)/2` — at eight neighbours over the imported corpus, at most 48,304 candidates instead of
  18.2 million, a factor of 377 — and it is the same shape the study used, which judged 2,000
  candidate pairs drawn by lexical blocking rather than any pair it could form.

  IT IS BOUNDED TWICE AND BOTH BOUNDS ARE ON THE ANSWER. {@link PairProposal.bound} is the most
  pairs this proposal could have held and {@link PairProposal.truncated} says whether that ceiling
  actually cut, so a caller can tell a corpus with few neighbours from a proposal that stopped
  early — and a sweep can state the size of what it is about to pay for BEFORE paying, which is
  #360's standing requirement one level up. Work is bounded and not merely output: the loop stops
  searching when the ceiling is reached rather than proposing and then slicing.

  EVERY PAIR IS PROPOSED ONCE. Retrieval reaches a pair from both ends — `a` is among `b`'s
  neighbours exactly when `b` is among `a`'s — so the unordered key is what keeps one relation
  from being judged, and paid for, twice.

  WHAT HAPPENS ON A DEPLOYMENT THAT INSTALLED NO EMBEDDING SERVICE, stated rather than left to be
  discovered. The corpus index has two halves: FTS5, which is in the runtime and makes NO OUTBOUND
  CALL EVER, and a meaning half that needs a model the operator installs. With no policy the
  meaning half is simply absent, the door answers by keyword alone and this proposer still
  proposes — which is the right behaviour and not a degraded one, because keyword neighbours ARE
  the study's own lexical blocking. But it is a floor and #357 says so in as many words: a
  contradiction phrased in different words is invisible to a lexical pre-filter, so the rate
  measured that way undercounts. {@link PairProposal.meaning} and {@link PairProposal.absent}
  therefore travel on every proposal and carry the door's own sentence for which absence was met.
  A proposal that answered by keyword alone and did not say so would be a proposal reporting a
  floor as a measurement.

  THE PROPOSER DOES NOT SEARCH; IT IS HANDED A SEARCH. `atyrode.babel.jev` declares no capability
  at all (#370) — not `containers:write`, which `screen/pass.ts` argues at length, and not
  `containers:read` either, so it cannot call `babel.search` any more than it can call
  `babel.suggest`. The function is the caller's, exactly as `deliver` is, and it is typed in the
  DOOR's own schemas rather than in `store/corpus.ts`'s: what this consumes is a published answer,
  not the baseline's internals, and a part reaches the baseline only through its doors.

  THE QUERY IS A DERIVATION AND IT SAYS WHAT IT DROPS. `SearchQuerySchema` bounds a query at 512
  characters and REFUSES a longer one rather than truncating it, because "a search whose last
  third was silently dropped returns a confident answer to something nobody asked". That reasoning
  is about a person's question; an anchor query is derived from a record, so the derivation has to
  choose what it sends, and choosing is not the same as dropping. It sends the record's own title
  and the head of its text, whitespace collapsed, to the door's own limit. Two further narrowings
  are the index's rather than this file's and are worth knowing when reading a result: the keyword
  half reads the first 32 terms of any query, and the meaning half embeds at most 8 KB.
*/

/**
 * A SEARCH OVER THE CORPUS, as `babel.search` answers it. The caller supplies it; see the head
 * for why this part cannot hold one of its own.
 *
 * The door serializes an array while the in-process store exposes it as readonly. The consumer
 * mutates neither, so this view accepts both without copying an answer only to satisfy a mutable
 * inferred Zod array type.
 */
export type PairSearchAnswer = Omit<SearchResult, "hits"> & {
  readonly hits: readonly SearchResult["hits"][number][];
};
export type PairSearch = (query: SearchQuery) => Promise<PairSearchAnswer>;

/**
 * HOW MANY NEIGHBOURS ONE ANCHOR CONTRIBUTES.
 *
 * Eight, which is a retrieval depth rather than a calibration: it is how wide the net is cast
 * before anything is judged, and nothing about which pairs come back as related depends on it.
 * The study drew 2,000 candidate pairs over 6,038 records, a third of a pair per record; eight is
 * an order of magnitude more generous than that and still 377 times cheaper than every pair.
 */
export const NEIGHBOURS_PER_ANCHOR = 8;

/**
 * THE MOST PAIRS ONE PROPOSAL CARRIES, and it is the study's own sample size on purpose: 2,000
 * candidate pairs is the batch every measurement quoted for either relation was taken on, so a
 * first pass on a new deployment is directly comparable to it. It is a budget, not a line — a cut
 * decides what a relation IS and this decides how much is looked at.
 */
export const PAIRS_PROPOSED_CAP = 2000;

/** The door's own bound on a query, restated here because the derivation has to aim at it. */
export const ANCHOR_QUERY_CHARS = 512;

/** What a proposal produced, and its own account of how it was bounded and what answered. */
export interface PairProposal {
  /** The candidate pairs, each once, each ordered by {@link chronological}. */
  readonly pairs: readonly RecordPair[];
  /** The most pairs this proposal could have held: `min(records × neighbours, cap)`. */
  readonly bound: number;
  /** Anchors actually searched. Fewer than the records given means the bound cut or text was absent. */
  readonly anchors: number;
  /** Neighbours asked of each anchor. */
  readonly neighbours: number;
  /** Searches made, which is what a caller is billed for by whatever it gave us. */
  readonly searches: number;
  /** True when the bound refused a pair: more candidates exist than this proposal carries. */
  readonly truncated: boolean;
  /** Records with no title and no text, which cannot be queried with and were not searched. */
  readonly textless: number;
  /** Hits naming a record this batch does not hold. Not dropped silently: the caller may widen. */
  readonly unresolved: number;
  /** The weakest state the meaning half reported over every search made. */
  readonly meaning: SearchResult["meaning"];
  /** Which absence was met, in the door's own words; empty when the meaning half answered fully. */
  readonly absent: string;
  /** True when a sketch cut a candidate slice, so a nearer neighbour may lie outside it. */
  readonly approximate: boolean;
}

const MEANING_ORDER = { absent: 0, partial: 1, full: 2 } as const;

/**
 * The query one anchor becomes: its own words, collapsed, to the door's limit. See the head on
 * why a derivation may choose what it sends where a person's question may not be cut.
 */
export function anchorQuery(record: PairRecord): string {
  const prose = `${record.title} ${record.text}`.replace(/\s+/gu, " ").trim();
  if (prose.length <= ANCHOR_QUERY_CHARS) return prose;
  return prose.slice(0, ANCHOR_QUERY_CHARS).trimEnd();
}

/**
 * THE CANDIDATE PAIRS OF ONE BATCH, bounded, deduplicated, and honest about what answered.
 *
 * `records` is both the anchors and the pool: a hit naming a record outside it is counted in
 * `unresolved` rather than resolved by a read of this part's own, because a part with no
 * `containers:read` cannot read one and a proposer that quietly skipped them would understate the
 * corpus. A caller sweeping in batches widens the batch or accepts that cross-batch pairs are
 * proposed when the other end is the anchor.
 */
export async function proposePairs(
  records: readonly PairRecord[],
  search: PairSearch,
  options: {
    readonly neighbours?: number;
    readonly cap?: number;
  } = {},
): Promise<PairProposal> {
  const neighbours = options.neighbours ?? NEIGHBOURS_PER_ANCHOR;
  const cap = options.cap ?? PAIRS_PROPOSED_CAP;
  const bound = Math.min(records.length * neighbours, cap);
  const held = new Set<string>();
  const known = new Map(records.map((record) => [record.id, record]));
  const pairs: RecordPair[] = [];
  let anchors = 0;
  let searches = 0;
  let textless = 0;
  let unresolved = 0;
  let truncated = false;
  let absent = "";
  let approximate = false;
  let meaning: SearchResult["meaning"] = "full";
  for (const anchor of records) {
    if (truncated || pairs.length >= bound) {
      // The ceiling is reached and the records after this one were never searched. Saying so is
      // the whole point of the field: a sweep that could not tell a bounded pass from a complete
      // one would report a partial sweep as the corpus's answer.
      truncated = truncated || anchors + textless < records.length;
      break;
    }
    const query = anchorQuery(anchor);
    if (query === "") {
      textless += 1;
      continue;
    }
    anchors += 1;
    searches += 1;
    // One more than asked for, because the anchor is in the index and matches its own prose
    // better than anything else does.
    const answer = await search({ query, limit: neighbours + 1, kinds: [] });
    if (answer.meaningAbsent !== "" && absent === "") absent = answer.meaningAbsent;
    if (answer.approximate) approximate = true;
    if (MEANING_ORDER[answer.meaning] < MEANING_ORDER[meaning]) meaning = answer.meaning;
    for (const hit of answer.hits) {
      if (hit.id === anchor.id) continue;
      const other = known.get(hit.id);
      if (other === undefined) {
        unresolved += 1;
        continue;
      }
      const key = pairKey(anchor, other);
      if (held.has(key)) continue;
      if (pairs.length >= bound) {
        truncated = true;
        break;
      }
      held.add(key);
      pairs.push(chronological(anchor, other));
    }
  }
  return {
    pairs,
    bound,
    anchors,
    neighbours,
    searches,
    truncated,
    textless,
    unresolved,
    // Nothing was asked, so nothing answered. A proposal that reported `full` here would claim
    // the meaning half covered a corpus it was never consulted about.
    meaning: searches === 0 ? "absent" : meaning,
    absent:
      searches === 0
        ? "no search was made, so neither index answered and nothing was proposed"
        : absent,
    approximate,
  };
}
