/*
  The feed's order, held to what a reader observes (ported from `v0.4.0:internal/web/feed_test.go`).

  The ranks are pure functions over a score, an age and a handful of votes, so they are asserted
  directly: what "hot" means is a claim this deployment makes about its own order, and a test
  that could only reach it through a door would be asserting the door.
*/

import { describe, expect, test } from "bun:test";
import { FEED_SORTS, type FeedSort, type PostKind } from "../contract.ts";
import {
  controversialRank,
  FEED_EPOCH_MS,
  hotRank,
  nextBefore,
  risingRank,
  sortFeed,
  surfaceOf,
  URGENCY,
  WINDOW_MS,
  type Ranked,
  type RankedVote,
} from "./rank.ts";

const HOUR = 60 * 60 * 1000;
const DAY = 24 * HOUR;
const NOW = Date.UTC(2026, 8, 12, 12, 0, 0);

interface Entry extends Ranked {
  post: {
    id: string;
    kind: PostKind;
    surface: Ranked["post"]["surface"];
    attention: Ranked["post"]["attention"];
    score: number;
    awaiting: boolean;
    votes: RankedVote[];
  };
  createdAt: number;
  activity: number[];
  urgency: number;
}

/** `role: vote` per reviewer, so a case reads as the votes it is: `["reception: support"]`. */
function votes(said: readonly string[]): RankedVote[] {
  return said.map((one) => {
    const [role = "", vote = ""] = one.split(": ");
    return { role, vote };
  });
}

function entry(
  id: string,
  createdAt: number,
  score: number,
  extra: Partial<{
    kind: PostKind;
    votes: readonly string[];
    awaiting: boolean;
    urgency: number;
    activity: number[];
    surface: Ranked["post"]["surface"];
    attention: Ranked["post"]["attention"];
  }> = {},
): Entry {
  return {
    post: {
      id,
      kind: extra.kind ?? "proposal",
      surface:
        extra.surface ??
        surfaceOf(
          extra.kind ?? "proposal",
          extra.awaiting ? "new" : "accepted",
          extra.awaiting ?? false,
        ),
      attention: extra.attention ?? { at: new Date(createdAt).toISOString(), basis: "evidence" },
      score,
      awaiting: extra.awaiting ?? false,
      votes: votes(extra.votes ?? []),
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

// The order exists to find the records the reviewers argued over, and what counts as an
// argument is the same thing the row's badge and the peel's note call one: both sides inside
// ONE role. Getting that wrong does not degrade the list, it inverts it.
describe("controversial", () => {
  // The zero is the half worth asserting. A record nine reviewers supported is not slightly
  // controversial; it is agreed on, and a rank that returned a small positive number for it
  // would put the deployment's most popular records at the bottom of a list nobody asked for.
  test("is zero for anything no single role divided", () => {
    expect(controversialRank(votes(["reception: support", "evidence: support"]))).toBe(0);
    expect(controversialRank(votes(["reception: oppose", "evidence: oppose"]))).toBe(0);
    expect(controversialRank([])).toBe(0);
    // Neither side, twice over: a reviewer who declined to answer is not half of an argument.
    expect(controversialRank(votes(["reception: unsure", "reception: unsure"]))).toBe(0);
  });

  // THE CASE THIS ORDER GOT BACKWARDS. Support on whether a proposal matters beside opposition
  // on whether its evidence holds is two reviewers answering two questions; two runs answering
  // the SAME question in opposite directions is the argument. Summed into two columns the first
  // reads as perfectly balanced and outranks the second, so the list that exists to find
  // disagreement was led by a record the page itself labels `reviewed`.
  test("ranks a split inside one role above a mixture across two", () => {
    const split = votes(["reception: support", "reception: oppose", "evidence: support"]);
    const crossRole = votes(["reception: support", "evidence: oppose"]);
    expect(controversialRank(crossRole)).toBe(0);
    expect(controversialRank(split)).toBeGreaterThan(controversialRank(crossRole));
  });

  test("rewards balance, and magnitude inside it", () => {
    const even = votes([
      "reception: support",
      "reception: support",
      "reception: oppose",
      "reception: oppose",
    ]);
    const lopsided = votes([
      "reception: support",
      "reception: support",
      "reception: support",
      "reception: oppose",
    ]);
    const small = votes(["reception: support", "reception: oppose"]);
    expect(controversialRank(even)).toBeGreaterThan(controversialRank(lopsided));
    expect(controversialRank(even)).toBeGreaterThan(controversialRank(small));
  });

  // Two questions the reviewers each divided over is a broader argument than one, and the sum
  // is what says so. A rank that took the widest split alone would call them equal.
  test("counts every role that divided, and counts each one once", () => {
    const oneRole = votes(["reception: support", "reception: oppose"]);
    const twoRoles = votes([
      "reception: support",
      "reception: oppose",
      "evidence: support",
      "evidence: oppose",
    ]);
    expect(controversialRank(twoRoles)).toBeGreaterThan(controversialRank(oneRole));
    expect(controversialRank(twoRoles)).toBe(2 * controversialRank(oneRole));
  });

  // A grant whose role the index could not name is in the score and outside every split: a bare
  // vote credited to a role nobody authorized it for reads as a question that was answered.
  test("ignores a vote that names no role", () => {
    expect(controversialRank(votes([": support", ": oppose"]))).toBe(0);
    expect(controversialRank(votes(["reception: support", ": oppose"]))).toBe(0);
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

// The route is read off two columns and decides what the operator is shown at all, so the two
// standings that mean "an agent does this next" are asserted by name: getting either wrong
// moves work onto the desk, or off it, with nothing else changing.
describe("the surface", () => {
  test("is the desk only for what awaits him and is addressed to him", () => {
    expect(surfaceOf("proposal", "new", true)).toBe("desk");
    expect(surfaceOf("finding", "reopened", true)).toBe("desk");
    expect(surfaceOf("question", "open", true)).toBe("desk");
    // A candidate is a question Babel asked itself, and it waits on nobody.
    expect(surfaceOf("hypothesis", "new", true)).toBe("shelf");
  });

  test("is the agent queue for the two standings that name work a run does next", () => {
    expect(surfaceOf("proposal", "accepted", false)).toBe("queue");
    expect(surfaceOf("finding", "refine-requested", false)).toBe("queue");
    expect(surfaceOf("proposal", "rejected", false)).toBe("shelf");
    expect(surfaceOf("proposal", "deferred", false)).toBe("shelf");
    expect(surfaceOf("proposal", "duplicate", false)).toBe("shelf");
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

// Desk attention ages without changing a record's standing or the shelf's ordering.
describe("next", () => {
  test("keeps existing priority for similarly renewed desk items, ahead of other surfaces", () => {
    const blocked = entry("qst_block", NOW - 60_000, 0, {
      kind: "question",
      awaiting: true,
      urgency: URGENCY.blocked,
    });
    const reopened = entry("fnd_reopened", NOW - HOUR, 0, {
      kind: "finding",
      awaiting: true,
      urgency: URGENCY.reopened,
    });
    const proposal = entry("pro_new", NOW - 2 * HOUR, 0, {
      kind: "proposal",
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const finding = entry("fnd_new", NOW - 3 * HOUR, 0, {
      kind: "finding",
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const candidate = entry("hyp_new", NOW - 4 * HOUR, 0, {
      kind: "hypothesis",
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const asked = entry("qst_idle", NOW - 5 * HOUR, 0, {
      kind: "question",
      awaiting: true,
      urgency: URGENCY.asked,
    });
    const settled = entry("pro_done", NOW - 10 * HOUR, 4);

    const posts = [settled, asked, candidate, finding, proposal, reopened, blocked];
    sortFeed(posts, "next", NOW);
    expect(ids(posts)).toEqual([
      "qst_block",
      "fnd_reopened",
      "pro_new",
      "fnd_new",
      "qst_idle",
      "hyp_new",
      "pro_done",
    ]);
  });

  test("prefers recently renewed desk work without changing non-desk chronology", () => {
    const older = entry("pro_older", NOW - 5 * HOUR, 0, {
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const newer = entry("pro_newer", NOW - HOUR, 0, { awaiting: true, urgency: URGENCY.unruled });
    expect(nextBefore(newer, older, NOW)).toBeLessThan(0);
    // Non-desk chronology remains a stable browsing order, not a decaying weight.
    const idleOld = entry("pro_idle_old", NOW - 5 * HOUR, 0);
    const idleNew = entry("pro_idle_new", NOW - HOUR, 0);
    expect(nextBefore(idleNew, idleOld, NOW)).toBeLessThan(0);
  });

  test("stale high urgency can yield to a fresh lower-priority item without mixed-surface cycles", () => {
    const stale = entry("qst_stale", NOW - 180 * DAY, 0, {
      kind: "question",
      awaiting: true,
      urgency: URGENCY.blocked,
    });
    const fresh = entry("pro_fresh", NOW - HOUR, 0, {
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const shelf = entry("hyp_reopened", NOW - DAY, 0, {
      kind: "hypothesis",
      awaiting: true,
      urgency: URGENCY.reopened,
    });
    expect(nextBefore(fresh, stale, NOW)).toBeLessThan(0);
    expect(nextBefore(stale, shelf, NOW)).toBeLessThan(0);
    expect(nextBefore(fresh, shelf, NOW)).toBeLessThan(0);
    const posts = [stale, shelf, fresh];
    sortFeed(posts, "next", NOW);
    expect(ids(posts)).toEqual(["pro_fresh", "qst_stale", "hyp_reopened"]);
  });

  test("unknown history gets no freshness from a recent record creation date", () => {
    const unknown = entry("pro_unknown", NOW, 0, {
      awaiting: true,
      urgency: URGENCY.unruled,
      attention: { at: null, basis: null },
    });
    const dated = entry("pro_dated", NOW - 180 * DAY, 0, {
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const posts = [unknown, dated];
    sortFeed(posts, "next", NOW);
    expect(ids(posts)).toEqual(["pro_dated", "pro_unknown"]);
  });

  test("elapsed time and a later attention act do not decay or reorder the shelf", () => {
    const old = entry("hyp_old", NOW - 180 * DAY, 0, {
      kind: "hypothesis",
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const recent = entry("hyp_recent", NOW - HOUR, 0, {
      kind: "hypothesis",
      awaiting: true,
      urgency: URGENCY.unruled,
    });
    const posts = [recent, old];
    sortFeed(posts, "next", NOW);
    expect(ids(posts)).toEqual(["hyp_old", "hyp_recent"]);
    recent.post.attention = { at: new Date(NOW + 365 * DAY).toISOString(), basis: "operator" };
    sortFeed(posts, "next", NOW + 365 * DAY);
    expect(ids(posts)).toEqual(["hyp_old", "hyp_recent"]);
  });
});
