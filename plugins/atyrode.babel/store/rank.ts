/*
  THE ORDER OF THE FEED, as pure functions over a score, an age and a handful of votes
  (SPEC §8.7, ported from `internal/web/feed.go`).

  They are here rather than in the door because what "hot" means is a claim the deployment
  makes about its own corpus, and a client holding a second copy of it would drift the first
  time either changed. What a client names is the sort; what it receives is the order.

  Nothing in this module reads the store, the clock or the index. Every rule takes the two or
  three numbers it is about and returns one, which is what makes the tiering below assertable
  without a database: the tiers are measured — Reddit's 45,000-second decay, the twelve-hour
  rising window, the fixed epoch — and a measured constant that only exists inside a query is a
  constant nobody can check.
*/

import type { FeedSort, FeedWindow, PostKind } from "../contract.ts";

/**
 * The fixed origin the hot rank measures age from. A decay origin that moved — the
 * deployment's first record, say — would reorder the whole feed whenever the oldest record
 * changed.
 */
export const FEED_EPOCH_MS = Date.UTC(2026, 0, 1, 0, 0, 0, 0);

/** Seconds of age worth one order of magnitude of score: Reddit's 45,000, half a day. */
export const FEED_DECAY_SECONDS = 45_000;

/** The recent activity a rising rank counts. */
export const RISING_WINDOW_MS = 12 * 60 * 60 * 1000;

/** How long one built index serves before the next read rebuilds it. */
export const FEED_FRESHNESS_MS = 60_000;

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

/**
 * Each window's width in milliseconds; `all` is the whole corpus and is the only one that is
 * not a duration. It is a `Record` over the closed vocabulary rather than a switch so that a
 * window the contract admits and this table forgets is a type error.
 */
export const WINDOW_MS: Record<FeedWindow, number> = {
  hour: HOUR_MS,
  day: DAY_MS,
  week: 7 * DAY_MS,
  month: 30 * DAY_MS,
  year: 365 * DAY_MS,
  all: 0,
};

/**
 * The urgency ranks `next` groups by, before the kind decides and before the age does.
 *
 * Zero is "awaiting nothing" so that the field's zero value is the safe one: the comparator
 * reads urgency only for a post that awaits the operator, and a default of "blocked" would put
 * every unclassified record at the top of the list if that ever stopped being true.
 */
export const URGENCY = {
  /** Awaiting nobody. */
  none: 0,
  /** A question Babel has stopped on: it refused to guess, so nothing costs more to leave. */
  blocked: 1,
  /** A ruling the operator lifted: he has read it before, so it is the cheapest ruling here. */
  reopened: 2,
  /** A record nobody has ruled on — the review queue, which is most of what needs him. */
  unruled: 3,
  /** A question that blocks nothing; §4.8 keeps it in the inbox. */
  asked: 4,
} as const;

/**
 * §8.7's "a proposal before a finding before a candidate at equal urgency", with a question
 * after all three. The reason is what each kind is FOR rather than how much it matters: a
 * proposal is a remedy addressed to the operator, a finding is a pattern Babel consolidated and
 * is asking him to accept, and a candidate is something it is still developing on its own.
 */
export const KIND_WEIGHT: Record<PostKind, number> = {
  proposal: 0,
  finding: 1,
  hypothesis: 2,
  question: 3,
};

/**
 * The few facts of a post every rank and every tie-break reads.
 *
 * The four columns are reached through `post` rather than copied beside it, because the index
 * holds tens of thousands of entries and the wire shape already carries them: a flat copy would
 * be six more fields per row that two writers could disagree about.
 */
export interface Ranked {
  readonly post: {
    readonly id: string;
    readonly kind: PostKind;
    readonly score: number;
    readonly support: number;
    readonly oppose: number;
    readonly awaiting: boolean;
  };
  /** Milliseconds since the Unix epoch. */
  readonly createdAt: number;
  /** When each vote and comment landed, which is what the rising rank counts. */
  readonly activity: readonly number[];
  /** Which of `next`'s groups this post belongs to; read only when it awaits the operator. */
  readonly urgency: number;
}

/**
 * The signed log of the score plus age at a fixed decay.
 *
 * The sign is what makes it work on a corpus that can be voted down: a disputed record sinks
 * rather than sorting beside an unvoted one, and the logarithm is why the tenth vote moves a
 * post less than the second did.
 */
export function hotRank(score: number, createdAtMs: number): number {
  const magnitude = Math.log10(Math.max(Math.abs(score), 1));
  const sign = score > 0 ? 1 : score < 0 ? -1 : 0;
  const ageSeconds = (createdAtMs - FEED_EPOCH_MS) / 1000;
  return sign * magnitude + ageSeconds / FEED_DECAY_SECONDS;
}

/**
 * The score itself. It is a named rule rather than a field read so that every sort in this
 * module is one, and so the window that makes "top of the day" different from "top of all
 * time" is visibly not part of it.
 */
export function topRank(score: number): number {
  return score;
}

/**
 * Balance times magnitude, and zero for anything one-sided.
 *
 * The zero is the honest answer rather than a small number: §8.7 says controversial "needs both
 * support and opposition", and a record nine reviewers supported is not slightly controversial
 * — it is agreed on.
 */
export function controversialRank(support: number, oppose: number): number {
  if (support <= 0 || oppose <= 0) return 0;
  const smaller = Math.min(support, oppose);
  const larger = Math.max(support, oppose);
  return Math.pow(support + oppose, smaller / larger);
}

/**
 * Recent activity against age. Zero means nothing happened in the window, and the feed excludes
 * those rather than ranking them last: a rising list whose tail is every silent record in the
 * corpus is a list of every record in the corpus.
 */
export function risingRank(activity: readonly number[], createdAtMs: number, nowMs: number): number {
  const cutoff = nowMs - RISING_WINDOW_MS;
  let recent = 0;
  for (const at of activity) {
    if (at > cutoff) recent++;
  }
  if (recent === 0) return 0;
  const ageHours = Math.max((nowMs - createdAtMs) / HOUR_MS, 0);
  return recent / Math.pow(ageHours + 2, 1.5);
}

/** The rank one sort assigns one post; `new` and `next` are orders rather than scores. */
export function rankOf(sort: FeedSort, entry: Ranked, nowMs: number): number {
  switch (sort) {
    case "hot":
      return hotRank(entry.post.score, entry.createdAt);
    case "top":
      return topRank(entry.post.score);
    case "controversial":
      return controversialRank(entry.post.support, entry.post.oppose);
    case "rising":
      return risingRank(entry.activity, entry.createdAt, nowMs);
    case "new":
    case "next":
      return 0;
  }
}

/**
 * §8.5's reading order, which §8.7 puts on the sort bar as `next`: urgency first, then the
 * kind, then the oldest wait.
 *
 * Three keys and their order are the whole rule. Urgency, because a question Babel has stopped
 * on costs more to leave than a candidate it is still developing. The kind at equal urgency,
 * because a proposal is a remedy addressed to the operator and a candidate is not addressed to
 * him at all. The oldest first inside a kind, on the ordinary grounds that a queue nobody
 * drains from the bottom has a permanent bottom — the one place this order inverts every other
 * sort here.
 *
 * A post that awaits nothing sorts after every post that does, newest first, so `next` is a
 * complete order over the corpus rather than a filter wearing a sort's name.
 */
export function nextBefore(left: Ranked, right: Ranked): number {
  if (left.post.awaiting !== right.post.awaiting) return left.post.awaiting ? -1 : 1;
  if (!left.post.awaiting) {
    if (left.createdAt !== right.createdAt) return right.createdAt - left.createdAt;
    return compareId(left.post.id, right.post.id);
  }
  if (left.urgency !== right.urgency) return left.urgency - right.urgency;
  const leftWeight = KIND_WEIGHT[left.post.kind];
  const rightWeight = KIND_WEIGHT[right.post.kind];
  if (leftWeight !== rightWeight) return leftWeight - rightWeight;
  if (left.createdAt !== right.createdAt) return left.createdAt - right.createdAt;
  return compareId(left.post.id, right.post.id);
}

/** Lexicographic order over identifiers: the last tie-break of every sort in this module. */
function compareId(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

/**
 * Orders the eligible set in place. Ties resolve newer first everywhere and then by identifier,
 * so one corpus has one order rather than a different one per rebuild — except under `next`,
 * which is about a wait rather than a reception and therefore drains from the bottom.
 *
 * The ranks are computed once per post rather than once per comparison, which is the difference
 * between n and n log n calls to a `Math.pow`.
 */
export function sortFeed<T extends Ranked>(posts: T[], sort: FeedSort, nowMs: number): void {
  const count = posts.length;
  if (count < 2) return;
  const order = new Array<number>(count);
  for (let i = 0; i < count; i++) order[i] = i;
  if (sort === "next") {
    order.sort((a, b) => nextBefore(posts[a] as T, posts[b] as T));
  } else {
    const ranked = sort !== "new";
    const rank = new Float64Array(count);
    if (ranked) {
      for (let i = 0; i < count; i++) rank[i] = rankOf(sort, posts[i] as T, nowMs);
    }
    order.sort((a, b) => {
      if (ranked && rank[a] !== rank[b]) return (rank[b] as number) - (rank[a] as number);
      const left = posts[a] as T;
      const right = posts[b] as T;
      if (left.createdAt !== right.createdAt) return right.createdAt - left.createdAt;
      return compareId(left.post.id, right.post.id);
    });
  }
  const sorted = new Array<T>(count);
  for (let i = 0; i < count; i++) sorted[i] = posts[order[i] as number] as T;
  for (let i = 0; i < count; i++) posts[i] = sorted[i] as T;
}

/**
 * How long ago something happened, in one word.
 *
 * One word rather than "3 days" is what keeps §8.7's five-word budget spendable on the reason:
 * "never ruled on · waiting 3d" is five words and says both halves, where the same sentence
 * with the unit spelled out is six and says no more. A future instant reads as `now`: "waiting
 * -4m" is not a fact about anything.
 */
export function ageWord(elapsedMs: number): string {
  const minutes = elapsedMs / 60_000;
  if (minutes < 1) return "now";
  const hours = elapsedMs / HOUR_MS;
  if (hours < 1) return `${String(Math.trunc(minutes))}m`;
  if (hours < 24) return `${String(Math.trunc(hours))}h`;
  if (hours < 7 * 24) return `${String(Math.trunc(hours / 24))}d`;
  if (hours < 30 * 24) return `${String(Math.trunc(hours / (24 * 7)))}w`;
  if (hours < 365 * 24) return `${String(Math.trunc(hours / (24 * 30)))}mo`;
  return `${String(Math.trunc(hours / (24 * 365)))}y`;
}

/** Joins the two halves of a why with §8.6's separator, dropping an absent half. */
export function feedWhy(head: string, tail: string): string {
  if (head === "") return tail;
  if (tail === "") return head;
  return `${head} · ${tail}`;
}
