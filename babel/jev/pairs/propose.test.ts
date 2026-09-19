import { expect, test } from "bun:test";
import type { SearchQuery } from "../../contract.ts";
import { pairKey, type PairRecord } from "./pair.ts";
import { ANCHOR_QUERY_CHARS, anchorQuery, proposePairs, type PairSearch } from "./propose.ts";

/*
  HOW A CANDIDATE PAIR IS PROPOSED, against the door-shaped answer the part is allowed to read.

  Four properties, each of which a plausible change breaks silently:

  - WITH NO EMBEDDING POLICY THE PROPOSER STILL PROPOSES AND SAYS WHICH ABSENCE IT MET. This file
    pins the pair consumer's side of that contract; `babel/store/corpus.test.ts` pins the producer
    against the real index, including an invocation count of zero. Importing the baseline here is
    forbidden on purpose: a part reaches it through the door, not as a library.
  - THE BOUND IS OBSERVABLE AND IT BOUNDS THE WORK. A proposal that capped its output after
    searching everything would report the same `pairs` and the same `bound` while paying for the
    whole corpus, so the search count is asserted and not only the pair count.
  - A PAIR IS PROPOSED ONCE. Retrieval reaches every pair from both ends; without the unordered
    key each relation would be judged, and paid for, twice.
  - A QUERY STAYS INSIDE THE DOOR'S OWN BOUND. `SearchQuerySchema` refuses a query over 512
    characters rather than truncating it, so a derivation that overran would not degrade — every
    anchor with a long record would be refused outright.
*/

const NOW = Date.UTC(2026, 8, 19, 12, 0, 0);

/** A record the proposer may anchor on, with no store behind it. */
function pairRecord(id: string, minute: number, text: string): PairRecord {
  return {
    id,
    revision: 0,
    kind: "finding",
    title: `record ${id}`,
    text,
    writtenAt: new Date(NOW + minute * 60_000).toISOString(),
  };
}

/** A search that answers with the ids it is told to, counting what it was asked. */
function fakeSearch(answer: (query: SearchQuery) => readonly string[]): {
  readonly search: PairSearch;
  readonly queries: SearchQuery[];
} {
  const queries: SearchQuery[] = [];
  return {
    queries,
    search: async (query) => {
      queries.push(query);
      return {
        hits: answer(query)
          .slice(0, query.limit)
          .map((id) => ({
            id,
            title: id,
            via: "keyword" as const,
            score: 1,
            keyword: -1,
            meaning: null,
          })),
        coverage: { records: 0, keyworded: 0, embedded: 0, empty: 0, stale: 0, model: "" },
        meaning: "absent" as const,
        meaningAbsent: "no embedding service is installed, so this answer is by keyword alone",
        scanned: 0,
        rescored: 0,
        approximate: false,
      };
    },
  };
}

test("with no embedding policy installed, a proposal is made by keyword alone and says which absence it met", async () => {
  const pool = [
    pairRecord("fnd_00000001", 0, "the fan never drains and the window is not spent"),
    pairRecord("fnd_00000002", 5, "the drain stalls and the window is never spent"),
    pairRecord("obs_00000003", 10, "no restic archive has had its restore path exercised"),
  ];
  const { search } = fakeSearch(() => pool.map((record) => record.id));
  const proposal = await proposePairs(pool, search, { neighbours: 4 });
  expect(proposal.meaning).toBe("absent");
  expect(proposal.absent).toContain("no embedding service is installed");
  expect(proposal.searches).toBe(3);
  expect(proposal.pairs.map((candidate) => pairKey(candidate.a, candidate.b))).toContain(
    pairKey(pool[0] as PairRecord, pool[1] as PairRecord),
  );
});

test("a proposal states the most pairs it could hold, and whether that bound cut the work short", async () => {
  const pool = [0, 1, 2, 3].map((index) =>
    pairRecord(`fnd_0000000${String(index)}`, index, "drain window spend"),
  );
  const everyone = (): readonly string[] => pool.map((record) => record.id);

  const whole = fakeSearch(everyone);
  const complete = await proposePairs(pool, whole.search, { neighbours: 4 });
  expect(complete.bound).toBe(16);
  expect(complete.truncated).toBe(false);
  expect(complete.pairs).toHaveLength(6);
  expect(complete.searches).toBe(4);

  const capped = fakeSearch(everyone);
  const cut = await proposePairs(pool, capped.search, { neighbours: 4, cap: 2 });
  expect(cut.bound).toBe(2);
  expect(cut.truncated).toBe(true);
  expect(cut.pairs).toHaveLength(2);
  // The bound bounds the WORK: one anchor filled it, so the other three were never searched.
  expect(cut.searches).toBe(1);
  expect(capped.queries).toHaveLength(1);
});

test("a pair is proposed once however many of its ends the retrieval reaches, oldest record first", async () => {
  const pool = [
    pairRecord("fnd_a", 9, "drain"),
    pairRecord("fnd_b", 3, "drain"),
    pairRecord("fnd_c", 6, "drain"),
  ];
  const { search } = fakeSearch(() => pool.map((record) => record.id));
  const proposal = await proposePairs(pool, search, { neighbours: 4 });
  const keys = proposal.pairs.map((pair) => pairKey(pair.a, pair.b));
  expect(new Set(keys).size).toBe(keys.length);
  expect(keys).toHaveLength(3);
  for (const pair of proposal.pairs) {
    expect(pair.a.writtenAt < pair.b.writtenAt).toBe(true);
  }
});

test("a hit naming a record this batch does not hold is counted rather than dropped", async () => {
  const pool = [pairRecord("fnd_a", 0, "drain"), pairRecord("fnd_b", 1, "drain")];
  const { search } = fakeSearch(() => ["fnd_a", "fnd_b", "fnd_elsewhere"]);
  const proposal = await proposePairs(pool, search, { neighbours: 4 });
  expect(proposal.pairs).toHaveLength(1);
  expect(proposal.unresolved).toBe(2);
});

test("an anchor query stays inside the bound the search door refuses beyond", () => {
  const long = pairRecord(
    "fnd_long",
    0,
    "the drain stalls at zero and the window is not spent. ".repeat(40),
  );
  const query = anchorQuery(long);
  expect(long.text.length).toBeGreaterThan(ANCHOR_QUERY_CHARS);
  expect(query.length).toBeLessThanOrEqual(ANCHOR_QUERY_CHARS);
  expect(query.length).toBeGreaterThan(0);
});
