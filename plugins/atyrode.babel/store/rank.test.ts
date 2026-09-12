/*
  The feed's order, held to what a reader observes (ported from `internal/web/feed_test.go`).

  The ranks are pure functions over a score, an age and a handful of votes, so they are asserted
  directly: what "hot" means is a claim this deployment makes about its own order, and a test
  that could only reach it through a door would be asserting the door.
*/

import { describe, expect, test } from "bun:test";
import { FEED_SORTS, type FeedSort, type PostKind } from "../contract.ts";
import {
  ageWord,
  controversialRank,
  FEED_EPOCH_MS,
  feedWhy,
  hotRank,
  nextBefore,
  risingRank,
  sortFeed,
  URGENCY,
  WINDOW_MS,
  type Ranked,
} from "./rank.ts";

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;
const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);

interface Entry extends Ranked {
  post: { id: string; kind: PostKind; score: number; support: number; oppose: number; awaiting: boolean };
  createdAt: number;
  activity: number[];
  urgency: number;
}

function entry(
  id: string,
  createdAt: number,
  score: number,
  extra: Partial<{ kind: PostKind; support: number; oppose: number; awaiting: boolean; urgency: number; activity: number[] }> = {},
): Entry {
  return {
    post: {
      id,
      kind: extra.kind ?? "proposal",
      score,
      support: extra.support ?? Math.max(score, 0),
      oppose: extra.oppose ?? Math.max(-score, 0),
      awaiting: extra.awaiting ?? false,
    },
    createdAt,
    activity: extra.activity ?? [],
    urgency: extra.urgency ?? URGENCY.none,
  };
}

const ids = (posts: readonly Entry[]): string[] => posts.map((post) => post.post.id);

describe("hot", () => {
  // Both halves are needed because either alone is satisfiable by a degenerate rank: a feed
  // sorted by age alone passes the first, one sorted by score alone passes the second. Hot is
  // the claim that a young post with two votes can outrank an old one with ten.
  test("prefers the newer post at equal score and the higher score at equal age", () => {
    const old = FEED_EPOCH_MS + DAY;
    const recent = old + 12 * HOUR;

    expect(hotRank(5, recent)).toBeGreaterThan(hotRank(5, old));
    expect(hotRank(100, old)).toBeGreaterThan(hotRank(5, old));
  });

  // The decay is what makes it a feed rather than a leaderboard: half a day of youth is worth
  // one order of magnitude of score, so a post ten times better but a day older sinks.
  test("sinks a day-old post with ten times the score below a fresh one", () => {
    const old = FEED_EPOCH_MS + DAY;
    expect(hotRank(10, old)).toBeLessThan(hotRank(1, old + 2 * DAY));
  });

  // The sign is what makes it work on a corpus that can be voted down.
  test("puts a record voted down below one nobody voted on", () => {
    const old = FEED_EPOCH_MS + DAY;
    expect(hotRank(-3, old)).toBeLessThan(hotRank(0, old));
  });
});

// The score ordering is arithmetic nobody doubts; what "top of the day" means is a decision
// about which posts are eligible at all, and a window that included a post from twenty-five
// hours ago would make the period on the sort bar decorative.
describe("the window", () => {
  test("is a day wide for `day` and unbounded for `all`", () => {
    expect(WINDOW_MS.day).toBe(DAY);
    expect(WINDOW_MS.hour).toBe(HOUR);
    expect(WINDOW_MS.week).toBe(7 * DAY);
    expect(WINDOW_MS.month).toBe(30 * DAY);
    expect(WINDOW_MS.year).toBe(365 * DAY);
    expect(WINDOW_MS.all).toBe(0);
  });
});

describe("controversial", () => {
  // The zero is the half worth asserting. A record nine reviewers supported is not slightly
  // controversial; it is agreed on, and a rank that returned a small positive number for it
  // would put the deployment's most popular records at the bottom of a list nobody asked for.
  test("is zero for anything one-sided", () => {
    expect(controversialRank(9, 0)).toBe(0);
    expect(controversialRank(0, 9)).toBe(0);
    expect(controversialRank(0, 0)).toBe(0);
  });

  test("rewards balance, and magnitude inside it", () => {
    expect(controversialRank(5, 5)).toBeGreaterThan(controversialRank(9, 1));
    expect(controversialRank(10, 10)).toBeGreaterThan(controversialRank(1, 1));
  });
});

describe("rising", () => {
  // Excluding silence is what keeps the list short: a rising feed that ranked every quiet
  // record last would be a list of every record in the corpus with three interesting ones on
  // top.
  test("excludes silence and prefers the younger post", () => {
    const recent = [NOW - HOUR, NOW - 2 * HOUR];
    const stale = [NOW - 30 * HOUR];

    expect(risingRank([], NOW - HOUR, NOW)).toBe(0);
    expect(risingRank(stale, NOW - 2 * DAY, NOW)).toBe(0);
    expect(risingRank(recent, NOW - 3 * HOUR, NOW)).toBeGreaterThan(
      risingRank(recent, NOW - 72 * HOUR, NOW),
    );
  });
});

// Every sort here produces ties routinely — an unvoted corpus is all zeros on three of them —
// and a tie broken by insertion order would reshuffle the front page between two reads of the
// same deployment, which reads as records appearing and disappearing.
describe("every sort", () => {
  for (const sort of FEED_SORTS) {
    test(`${sort} breaks its ties newer first`, () => {
      const older = entry("pro_older", NOW - 2 * HOUR, 0, { activity: [NOW - 60_000] });
      const newer = entry("pro_newer", NOW - HOUR, 0, { activity: [NOW - 60_000] });
      const posts = [older, newer];
      sortFeed(posts, sort, NOW);
      expect(ids(posts)).toEqual(["pro_newer", "pro_older"]);
    });

    test(`${sort} breaks an exact tie by identifier`, () => {
      const first = entry("pro_aaa", NOW - HOUR, 0, { activity: [NOW - 60_000] });
      const second = entry("pro_bbb", NOW - HOUR, 0, { activity: [NOW - 60_000] });
      const posts = [second, first];
      sortFeed(posts, sort, NOW);
      expect(ids(posts)).toEqual(["pro_aaa", "pro_bbb"]);
    });
  }

  // `new` is the one sort that must ignore the score entirely: a rank leaking into it would
  // make "newest" mean "newest among the well-received", which is a different list.
  test("new ignores the score", () => {
    const loud = entry("pro_loud", NOW - 2 * HOUR, 99);
    const quiet = entry("pro_quiet", NOW - HOUR, 0);
    const posts = [loud, quiet];
    sortFeed(posts, "new", NOW);
    expect(ids(posts)).toEqual(["pro_quiet", "pro_loud"]);
  });

  test("top orders by the score alone", () => {
    const posts = [entry("pro_small", NOW - HOUR, 1), entry("pro_big", NOW - 2 * HOUR, 9)];
    sortFeed(posts, "top" as FeedSort, NOW);
    expect(ids(posts)).toEqual(["pro_big", "pro_small"]);
  });
});

// §8.5's reading order: urgency first, then the kind, then the longest wait. A post that awaits
// nothing sorts after every post that does, newest first, so that turning the queue filter off
// keeps the same list and adds to it.
describe("next", () => {
  test("orders by urgency, then kind, then the longest wait", () => {
    const blocked = entry("que_block", NOW - 60_000, 0, {
      kind: "question", awaiting: true, urgency: URGENCY.blocked,
    });
    const reopened = entry("fnd_reopened", NOW - HOUR, 0, {
      kind: "finding", awaiting: true, urgency: URGENCY.reopened,
    });
    const proposal = entry("pro_new", NOW - 2 * HOUR, 0, {
      kind: "proposal", awaiting: true, urgency: URGENCY.unruled,
    });
    const finding = entry("fnd_new", NOW - 3 * HOUR, 0, {
      kind: "finding", awaiting: true, urgency: URGENCY.unruled,
    });
    const candidate = entry("hyp_new", NOW - 4 * HOUR, 0, {
      kind: "hypothesis", awaiting: true, urgency: URGENCY.unruled,
    });
    const asked = entry("que_idle", NOW - 5 * HOUR, 0, {
      kind: "question", awaiting: true, urgency: URGENCY.asked,
    });
    const settled = entry("pro_done", NOW - 10 * HOUR, 4);

    const posts = [settled, asked, candidate, finding, proposal, reopened, blocked];
    sortFeed(posts, "next", NOW);
    expect(ids(posts)).toEqual([
      "que_block", "fnd_reopened", "pro_new", "fnd_new", "hyp_new", "que_idle", "pro_done",
    ]);
  });

  // The one place the order inverts every other sort: a queue nobody drains from the bottom has
  // a permanent bottom.
  test("drains the oldest wait first inside one kind and urgency", () => {
    const older = entry("pro_older", NOW - 5 * HOUR, 0, { awaiting: true, urgency: URGENCY.unruled });
    const newer = entry("pro_newer", NOW - HOUR, 0, { awaiting: true, urgency: URGENCY.unruled });
    expect(nextBefore(older, newer)).toBeLessThan(0);
    // …while a post awaiting nothing keeps the newest-first order of every other sort.
    const idleOld = entry("pro_idle_old", NOW - 5 * HOUR, 0);
    const idleNew = entry("pro_idle_new", NOW - HOUR, 0);
    expect(nextBefore(idleNew, idleOld)).toBeLessThan(0);
  });
});

// One word rather than "3 days" is what keeps §8.7's five-word budget spendable on the reason.
// The tiers are measured, so they are asserted at their own boundaries.
describe("the age word", () => {
  const cases: readonly [number, string][] = [
    [0, "now"],
    [59_000, "now"],
    [-4 * 60_000, "now"],
    [60_000, "1m"],
    [59 * 60_000, "59m"],
    [HOUR, "1h"],
    [23 * HOUR, "23h"],
    [DAY, "1d"],
    [6 * DAY, "6d"],
    [7 * DAY, "1w"],
    [29 * DAY, "4w"],
    [30 * DAY, "1mo"],
    [364 * DAY, "12mo"],
    [365 * DAY, "1y"],
    [800 * DAY, "2y"],
  ];
  for (const [elapsed, word] of cases) {
    test(`${String(elapsed)} ms reads as ${word}`, () => {
      expect(ageWord(elapsed)).toBe(word);
    });
  }
});

describe("the why", () => {
  test("joins both halves and drops an absent one rather than rendering a gap", () => {
    expect(feedWhy("never ruled on", "waiting 3d")).toBe("never ruled on · waiting 3d");
    expect(feedWhy("", "waiting 3d")).toBe("waiting 3d");
    expect(feedWhy("reopened", "")).toBe("reopened");
    expect(feedWhy("", "")).toBe("");
  });
});
