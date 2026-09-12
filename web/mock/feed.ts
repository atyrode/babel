// The feed, synthetic.
//
// GET /api/feed and GET /api/topics, answered from one fixture of sixty posts
// so the front page can be walked in a browser before internal/web serves it.
// The sorts are implemented here the way the contract states them — and the
// way internal/web/feed.go tests them — because a mock whose ordering is
// "whatever the fixture array holds" previews a feed that cannot be told from
// a broken one: the whole point of five sorts is that they visibly disagree.
//
// The fixture is deterministic. Ages, votes, comments and recent activity come
// from one seeded generator rather than from Math.random, so two screenshots
// of the same sort are the same picture and a reordering is a change in the
// code rather than in the dice. Only the clock moves: ages are computed from
// the moment this module loads, which is what makes hour/day/week/month/year
// windows genuinely different windows.
//
// It also answers POST /api/record/{id}/reception, but only for the ids this
// fixture invented. A vote on a post whose record ./record.ts holds is that
// file's business and falls through to it; a vote on a synthetic feed post has
// nowhere else to land, and an arrow that 404s is not a preview of the arrow.

// Types only. ./serve.ts runs under Bun with no DOM, and ../src/feedapi pulls
// in ../src/api, which reads `window` at import time to take the launch
// nonce: every other mock in this directory imports its shapes the same way
// and for the same reason.
import type { FeedKind, FeedPost, FeedSort, FeedWindow } from "../src/feedapi";

// The five kinds, in the order the chips offer them. It is written out here
// rather than imported for the reason above; the wire contract is what the
// two files share, not a constant.
const FEED_KINDS: FeedKind[] = ["proposal", "finding", "hypothesis", "observation", "question"];

// The prefix each kind's identifiers carry. It is the corpus's own — an id
// names its kind, which is why /r/{id} needs no kind parameter — so a
// fixture that invented one would preview a link the real server refuses.
const ID_PREFIX: Record<FeedKind, string> = {
  proposal: "pro",
  finding: "fnd",
  hypothesis: "hyp",
  observation: "obs",
  question: "qst",
};

// The four topics this synthetic deployment has evidence from, plus the posts
// with none. They are workspace basenames because that is what a topic is
// today (§8.7), and they match the workspaces ./serve.ts's sessions carry so
// the preview tells one story.
const TOPICS = ["atlas", "kepler", "babel", "scratch"] as const;

// One line of claim per post, twelve per kind. They are written out rather
// than generated because a feed is read as prose: sixty rows of "Synthetic
// post 41" previews the layout and hides the thing the layout is for.
const CLAIMS: Record<FeedKind, string[]> = {
  proposal: [
    "Renew the evaluation lease while a review batch is still running",
    "Cap the describe pass at one read per transcript",
    "Store the catalog digest beside the snapshot it was taken from",
    "Split archive verify into a fast check and a deep one",
    "Refuse a run whose ceiling was saved after it started",
    "Keep the operator's reason verbatim on every reconsideration",
    "Page the sessions listing instead of rendering the whole catalog",
    "Give every reviewer role its own coverage figure",
    "Drop the per-kind listing pages and filter one list instead",
    "Retry a refused write-back once, under the lease it actually holds",
    "Name the ordering basis wherever a list claims to be ranked",
    "Stop summing the operator's stance into the model tally",
  ],
  finding: [
    "Every review run this month outlived its lease and lost its write-back",
    "The sessions listing renders 15,311 pixels tall on a 1440px screen",
    "Describe re-reads each transcript once per scan, not once per change",
    "Four of fifty-five evaluation claims finished, and all four failed",
    "The catalog's lag is reported from the snapshot rather than from the store",
    "A twenty-four record batch takes about nine minutes under this policy",
    "Half the runs on the presence endpoint have no fresh heartbeat",
    "The record page made four requests to assemble one proposal",
    "Deep verify costs eleven minutes on a repository of this size",
    "Two hosts describe the same workspace under different names",
    "The evaluation store holds twenty operator stances and no reviewer votes",
    "Spend is unknown for every day whose receipts carry no cost",
  ],
  hypothesis: [
    "Lease expiry, not model latency, is what loses the review work",
    "The scan is bounded by file reads rather than by model calls",
    "Most of the catalog is Babel talking to itself",
    "A shared repository lock is what serializes two hosts' pushes",
    "The queue's depth is dominated by candidates nobody asked for",
    "Topic resolution fails when a workspace is reached through a symlink",
    "The frontier stalls when every candidate needs the same evidence",
    "Readers stop reading a listing at about twenty rows",
    "Duplicate proposals come from one pain rather than from one run",
    "A stale banner is what makes the surface feel haunted",
    "Cost per finding falls as the corpus index warms",
    "A record with no topic is a record whose session was never archived",
  ],
  observation: [
    "Assignment claim refused: the lease on assignment eval-7 had expired",
    "A stale lock left by an interrupted push was removed before the retry",
    "The describe pass read 1,204 transcripts in forty-one seconds",
    "Run run_atlas-07 stopped after its ceiling was reached",
    "Two snapshots share a parent and differ by one session",
    "The policy version changed in the middle of a batch",
    "A reviewer skipped the record and recorded why",
    "The catalog answered from cache for thirty-eight of forty reads",
    "One workspace resolves to two basenames across hosts",
    "The presence endpoint listed thirty-three rows and sixteen heartbeats",
    "A write-back arrived after its assignment had been reassigned",
    "Archive verify completed with no errors on either host",
  ],
  question: [
    "Which machine should hold the archive while the hub is down?",
    "Is a 240-second lease long enough for any real review batch?",
    "Should a skipped review count against coverage?",
    "What counts as the same project across two machines?",
    "May Babel start an evaluation run without being asked?",
    "Which of these two remedies do you want first?",
    "Is the atlas workspace still the one you care about?",
    "Should deferred proposals leave the queue entirely?",
    "How long should a finding wait before it is reconsidered?",
    "Do you want spend reported per run or per day?",
    "Is this a duplicate, or a different pain in the same words?",
    "Should a reopened decision notify the reviewers who voted?",
  ],
};

// Ids this preview shares with ./phaseb.ts's records, so the first rows of the
// feed open onto a record page that actually holds something. The rest are the
// fixture's own and open onto the "no record with that identifier" answer,
// which is the real server's sentence for an id it does not hold.
const REAL_IDS: Record<FeedKind, string[]> = {
  proposal: ["pro_criteria-template", "pro_stdin-credential"],
  finding: ["fnd_conflicting-evidence", "fnd_hostile-title", "fnd_absent-on-this-host"],
  hypothesis: ["hyp_unverified-closures", "hyp_dense-token", "hyp_lens-overlap", "hyp_promoted-pattern"],
  observation: ["obs_claim-no-verify", "obs_reopened", "obs_overlap", "obs_criteria"],
  question: ["qst_atlas-lifecycle", "qst_deploy-host", "qst_focus-policy", "qst_declined-vendor"],
};

// The standings a post can carry, by kind. An observation is never ruled on,
// so it is always "new"; the rest carry the vocabulary /api/record/{id} uses.
const STANDINGS: Record<FeedKind, string[]> = {
  proposal: ["new", "accepted", "rejected", "deferred", "reopened", "refine-requested"],
  finding: ["new", "accepted", "superseded"],
  hypothesis: ["new", "rejected"],
  observation: ["new"],
  question: ["new"],
};

const RUNS = [
  "run_discovery-07",
  "run_challenge-08",
  "run_build-11",
  "run_consolidate-03",
  "run_review-19",
];

const HOUR = 3_600_000;

// The fixture's clock. One value for the whole process so a post's age does
// not change between the feed read and the topics read.
const bootedAt = Date.now();

// A seeded generator, so the fixture is one fixture. The constants are the
// usual 32-bit LCG; nothing about them is meaningful beyond reproducibility.
function seeded(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state = (Math.imul(state, 1_664_525) + 1_013_904_223) >>> 0;
    return state / 0x1_0000_0000;
  };
}

// One post, with the fields the wire carries and the two the mock ranks with.
interface Fixture extends FeedPost {
  // Votes and comments recorded in the last twelve hours. It is what `rising`
  // is computed from and it is never sent: the server derives it from the
  // events it holds, and a client that could read it would be reading Babel's
  // bookkeeping rather than the feed.
  recent: number;
}

function build(): Fixture[] {
  const random = seeded(0x0babe1);
  const posts: Fixture[] = [];
  for (const kind of FEED_KINDS) {
    const claims = CLAIMS[kind];
    const real = REAL_IDS[kind];
    claims.forEach((title, index) => {
      // Ages are log-uniform from eighteen minutes to four hundred days, so
      // every window — hour, day, week, month, year, all — selects a
      // genuinely different set and "top this week" is visibly not "top of
      // all time". A linear spread would put almost every post in the same
      // window and make five of the six periods the same picture.
      const ageHours = 0.3 * Math.pow(32_000, random());
      const createdAt = new Date(bootedAt - ageHours * HOUR);

      // Votes. A third of the corpus has none at all, which is the honest
      // shape of a deployment whose reviewers have not run: those rows must
      // render as an absent score rather than as a zero.
      //
      // How many a post has depends on how long it has been readable, which
      // is what makes the windows mean something: "top this hour" and "top
      // of all time" led by the same row would be two controls with one
      // answer.
      const voted = random() > 0.32;
      const reach = 2 + Math.log10(ageHours + 1) * 3.2;
      const support = voted ? Math.floor(random() * reach) : 0;
      const oppose = voted ? Math.floor(random() * reach * 0.7) : 0;
      const unsure = voted ? Math.floor(random() * 3) : 0;

      // Topics. Two posts in nine have none — the origin this deployment
      // could not resolve — and two in nine cite evidence from two
      // workspaces, which is what makes the "+n" on the row real.
      const filing = random();
      const first = TOPICS[Math.floor(random() * TOPICS.length)];
      const second = TOPICS[Math.floor(random() * TOPICS.length)];
      const topics =
        filing < 0.22 ? [] : filing > 0.78 && second !== first ? [first, second] : [first];

      const comments = Math.floor(random() * (voted ? 15 : 4));
      // Recent activity, for `rising`. Only young posts have any: a year-old
      // record that collected a vote this morning is not rising, and the
      // formula's denominator says so, but the fixture should not pretend the
      // activity is there in the first place.
      const recent = ageHours < 48 ? Math.floor(random() * 9) : random() > 0.93 ? 1 : 0;

      const standings = STANDINGS[kind];
      const standing = standings[Math.floor(random() * standings.length)];
      const hasAuthor = kind === "question" ? random() > 0.5 : random() > 0.12;
      const runID = RUNS[Math.floor(random() * RUNS.length)];
      const id = index < real.length ? real[index] : `${ID_PREFIX[kind]}_feed-${index}`;
      const href =
        kind === "question"
          ? `/ask/questions/${encodeURIComponent(id)}`
          : `/r/${encodeURIComponent(id)}`;
      // The last thing that happened to the post: its newest comment or vote,
      // and its own creation when nothing has.
      const activityHours = recent > 0 ? Math.min(ageHours, random() * 12) : ageHours;
      posts.push({
        id,
        kind,
        title,
        standing,
        created_at: createdAt.toISOString(),
        author: hasAuthor ? { run_id: runID, href: `/watch/runs/${runID}` } : null,
        topics,
        score: support - oppose,
        support,
        oppose,
        unsure,
        you: "",
        comments,
        last_activity_at: new Date(bootedAt - activityHours * HOUR).toISOString(),
        href,
        recent,
      });
    });
  }
  return posts;
}

const fixture = build();

// The operator's own stance, in memory, keyed by post id. It is the same
// append-nothing receipt ./record.ts keeps for the record page: the preview
// has to be able to vote, read the number move, and vote again.
const stances: Record<string, "agree" | "disagree" | "unsure" | undefined> = {};

// The post as the wire carries it: the fixture's reviewer votes with the
// operator's own stance folded in, because §8.7's score is one number over
// both and `you` is what keeps them attributable.
function onWire(post: Fixture): FeedPost {
  const you = stances[post.id] ?? "";
  const support = post.support + (you === "agree" ? 1 : 0);
  const oppose = post.oppose + (you === "disagree" ? 1 : 0);
  const unsure = post.unsure + (you === "unsure" ? 1 : 0);
  const { recent: _recent, ...wire } = post;
  return { ...wire, support, oppose, unsure, score: support - oppose, you };
}

const WINDOW_HOURS: Record<FeedWindow, number> = {
  hour: 1,
  day: 24,
  week: 24 * 7,
  month: 24 * 30,
  year: 24 * 365,
  all: Number.POSITIVE_INFINITY,
};

// The epoch hot counts age from. It is the contract's fixed date and not the
// corpus's first record: a decay measured from the oldest post would reorder
// the whole feed the day that post was deleted.
const HOT_EPOCH = Date.parse("2026-01-01T00:00:00Z");

// The five orderings, exactly as the contract states them. Each returns the
// number the sort is descending on; `new` is the creation time itself.
function rank(post: FeedPost, sort: FeedSort, recent: number, now: number): number {
  const created = Date.parse(post.created_at);
  switch (sort) {
    case "new":
      return created;
    case "top":
      return post.score;
    case "controversial": {
      if (post.support <= 0 || post.oppose <= 0) return 0;
      const magnitude = post.support + post.oppose;
      const balance = Math.min(post.support, post.oppose) / Math.max(post.support, post.oppose);
      return Math.pow(magnitude, balance);
    }
    case "rising": {
      const ageHours = (now - created) / HOUR;
      return recent / Math.pow(ageHours + 2, 1.5);
    }
    case "hot":
    default: {
      const sign = Math.sign(post.score);
      const magnitude = Math.log10(Math.max(Math.abs(post.score), 1));
      return sign * magnitude + (created - HOT_EPOCH) / 45_000_000;
    }
  }
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

function feed(url: URL): Response {
  const now = Date.now();
  const askedSort = url.searchParams.get("sort") ?? "";
  const sort = (["hot", "new", "top", "controversial", "rising"] as FeedSort[]).includes(
    askedSort as FeedSort,
  )
    ? (askedSort as FeedSort)
    : "hot";
  const askedWindow = url.searchParams.get("t") ?? "";
  const t: FeedWindow = askedWindow in WINDOW_HOURS ? (askedWindow as FeedWindow) : "day";
  const topic = (url.searchParams.get("topic") ?? "").trim().toLowerCase();
  const kinds = (url.searchParams.get("kind") ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name): name is FeedKind => (FEED_KINDS as string[]).includes(name));
  const limit = Math.min(100, Math.max(1, Number(url.searchParams.get("limit") ?? 25) || 25));
  const offset = Math.max(0, Number(url.searchParams.get("offset") ?? 0) || 0);

  // The window applies to top and controversial and to nothing else, which
  // is the contract's rule and the reason the control is absent elsewhere.
  const scoped = sort === "top" || sort === "controversial";
  const horizon = scoped ? now - WINDOW_HOURS[t] * HOUR : Number.NEGATIVE_INFINITY;
  const selected = fixture.filter((post) => {
    if (kinds.length > 0 && !kinds.includes(post.kind)) return false;
    if (topic === "unfiled" && post.topics.length > 0) return false;
    if (topic && topic !== "unfiled" && !post.topics.includes(topic)) return false;
    if (Date.parse(post.created_at) < horizon) return false;
    // A post nothing has happened to is not rising, and dividing zero by an
    // age would rank it above a post with one vote and a long life.
    if (sort === "rising" && post.recent === 0) return false;
    return true;
  });

  const ranked = selected
    .map((post) => ({ post, wire: onWire(post) }))
    .sort((left, right) => {
      const difference =
        rank(right.wire, sort, right.post.recent, now) -
        rank(left.wire, sort, left.post.recent, now);
      if (difference !== 0) return difference;
      // Ties are broken by the newer post, in every sort. A stable order the
      // reader can page through is worth more than the order the fixture
      // happened to be built in.
      return Date.parse(right.wire.created_at) - Date.parse(left.wire.created_at);
    });

  return json({
    posts: ranked.slice(offset, offset + limit).map((entry) => entry.wire),
    total: ranked.length,
    sort,
    t,
    topic,
    kinds,
    built_at: new Date(now).toISOString(),
    notice: "",
  });
}

function topics(): Response {
  const counts = new Map<string, { posts: number; latest: number }>();
  let unfiled = 0;
  for (const post of fixture) {
    if (post.topics.length === 0) {
      unfiled += 1;
      continue;
    }
    const at = Date.parse(post.created_at);
    for (const name of post.topics) {
      const row = counts.get(name) ?? { posts: 0, latest: 0 };
      row.posts += 1;
      row.latest = Math.max(row.latest, at);
      counts.set(name, row);
    }
  }
  const rows = [...counts.entries()]
    .map(([name, row]) => ({
      name,
      posts: row.posts,
      latest_at: new Date(row.latest).toISOString(),
    }))
    .sort((left, right) => right.posts - left.posts || left.name.localeCompare(right.name));
  return json({ topics: rows, unfiled });
}

export async function feedResponse(request: Request, url: URL): Promise<Response | null> {
  const path = url.pathname;
  if (path === "/api/feed" && request.method === "GET") return feed(url);
  if (path === "/api/topics" && request.method === "GET") return topics();

  // The stance, for the posts this fixture invented. Anything else — a record
  // ./phaseb.ts holds — is left to ./record.ts, which keeps the operator's
  // stance for the record page and must stay the one place that does.
  if (path.startsWith("/api/record/") && path.endsWith("/reception")) {
    const id = decodeURIComponent(
      path.slice("/api/record/".length, path.length - "/reception".length),
    );
    if (!fixture.some((post) => post.id === id && post.id.includes("_feed-"))) return null;
    if (request.method !== "POST") return json({ error: "method not allowed" }, 405);
    const body = (await request.json().catch(() => ({}))) as { stance?: unknown };
    const stance = typeof body.stance === "string" ? body.stance : "";
    if (stance !== "agree" && stance !== "disagree" && stance !== "unsure") {
      return json(
        { error: "a value in the request is outside what the evaluation service accepts" },
        400,
      );
    }
    stances[id] = stance;
    return json({ stance, at: new Date().toISOString() });
  }
  return null;
}
