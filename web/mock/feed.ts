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
// and for the same reason. The hostile markup is the value half of the same
// arrangement: it is ./phaseb.ts's fixture rather than a second copy, because
// two strings that are supposed to be the same attack drift apart.
import type { FeedKind, FeedPost, FeedSort, FeedWindow } from "../src/feedapi";
import { HOSTILE_HTML } from "./phaseb";

// The four kinds, in the order the chips offer them. It is written out here
// rather than imported for the reason above; the wire contract is what the
// two files share, not a constant.
//
// An observation is not one of them, by operator decision (2026-09-12): it is
// evidence at depth 3 of the hypothesis that cites it rather than a row, so
// the fixture produces none and `?kind=observation` is refused as an unknown
// kind. ./phaseb.ts still holds observation records, and the record page still
// opens them — what changed is what the feed lists.
const FEED_KINDS: FeedKind[] = ["proposal", "finding", "hypothesis", "question"];

// The prefix each kind's identifiers carry. It is the corpus's own — an id
// names its kind, which is why /r/{id} needs no kind parameter — so a
// fixture that invented one would preview a link the real server refuses.
const ID_PREFIX: Record<FeedKind, string> = {
  proposal: "pro",
  finding: "fnd",
  hypothesis: "hyp",
  question: "qst",
};

// The repositories this synthetic deployment has evidence from. They are what
// a record's origin resolves to — the repository the work was in, never the
// directory it happened in — and they match the workspaces ./serve.ts's
// sessions carry so the preview tells one story.
//
// An origin is not a topic. §4.13 gives entity creation to an attributed
// operator act, so a record's origin becomes a *filing* only once the operator
// has accepted the topic that names it: `manifold` below is an origin nobody
// has accepted, which is why its records are unfiled and Babel has a proposal
// open about them.
const ORIGINS = ["atlas", "kepler", "babel", "scratch", "sandbox", "manifold"] as const;

// What each name is bound to, for the entities that exist. Four have remotes
// and are named by the repository the remote names; `scratch` has none and is
// bound by the common directory every worktree of it shares, which is what the
// surface must render as an identity and never as the topic itself. `sandbox`
// carries no binding at all — the shape a concept has, and the one a reader
// must not be shown a guess for.
const BINDINGS: Record<string, { kind: string; identity: string; remote?: string; paths: string[] }> = {
  atlas: {
    kind: "repository",
    identity: "example.invalid/synthetic/atlas",
    remote: "example.invalid/synthetic/atlas",
    paths: ["/home/demo/projects/atlas", "/home/demo/worktrees/atlas-imports", "/home/demo/worktrees/atlas-audit"],
  },
  kepler: {
    kind: "repository",
    identity: "example.invalid/synthetic/kepler",
    remote: "example.invalid/synthetic/kepler",
    paths: ["/home/demo/projects/kepler"],
  },
  babel: {
    kind: "repository",
    identity: "example.invalid/synthetic/babel",
    remote: "example.invalid/synthetic/babel",
    paths: ["/home/demo/projects/babel"],
  },
  scratch: {
    kind: "repository",
    identity: "/home/demo/scratch/.git",
    paths: ["/home/demo/scratch"],
  },
  manifold: {
    kind: "repository",
    identity: "example.invalid/synthetic/manifold",
    remote: "example.invalid/synthetic/manifold",
    paths: ["/home/demo/projects/manifold", "/home/demo/worktrees/manifold-sweep"],
  },
};

// The weight a kind carries inside one urgency band of `next`: a proposal is
// a remedy addressed to the operator, a finding is a conclusion Babel wants
// confirmed, a candidate is something it is still developing, and a question
// that blocks nothing is last. It is applied inside a band and never across
// one.
const KIND_WEIGHT: Record<FeedKind, number> = {
  proposal: 0,
  finding: 1,
  hypothesis: 2,
  question: 3,
};

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
    // The claim that names its own identifier: this is the second finding, so
    // it is the one REAL_IDS files under fnd_hostile-title, and the feed's
    // rendering of an untrusted line is then reachable in a browser without
    // opening the record that carries it.
    "A model's own output can carry markup: " + HOSTILE_HTML,
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
  question: ["qst_atlas-lifecycle", "qst_deploy-host", "qst_focus-policy", "qst_declined-vendor"],
};

// The standings a post can carry, by kind. They carry the vocabulary
// /api/record/{id} uses; a question has none of its own and reads as new
// until it is answered.
const STANDINGS: Record<FeedKind, string[]> = {
  proposal: ["new", "accepted", "rejected", "deferred", "reopened", "refine-requested"],
  finding: ["new", "accepted", "superseded", "reopened"],
  hypothesis: ["new", "rejected"],
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

// One post, with the fields the wire carries and the three the mock keeps to
// itself.
//
// `topics` is absent rather than stored, because a post's topics are not a
// property of the post: they are the accepted entities it is filed under, so
// the fixture holds the origins it came from and the wire carries whichever of
// them the operator has accepted (§4.13).
interface Fixture extends Omit<FeedPost, "topics"> {
  // Votes and comments recorded in the last twelve hours. It is what `rising`
  // is computed from and it is never sent: the server derives it from the
  // events it holds, and a client that could read it would be reading Babel's
  // bookkeeping rather than the feed.
  recent: number;
  // How stuck Babel is without an answer, as `next` groups it: 0 a question
  // blocking a run, 1 a decision something has changed about, 2 a record
  // enrolled for a ruling, 3 a question that blocks nothing. It is the
  // server's own grouping and is not on the wire — the row carries the
  // ordering's result, which is `why`, and never its arithmetic.
  urgency: number;
  // The repositories this record's evidence came from, resolved during the
  // scan. A filing is one of these that the operator has accepted as a topic;
  // the rest are evidence about a topic that does not exist yet.
  origins: string[];
}

// ---------------------------------------------------------------------------
// TOPICS (§4.13): what the operator has accepted, what Babel has proposed
// about it, and what is filed under neither.
//
// The state is mutable because every act on this surface is a write the reader
// has to be able to watch land: accepting a proposal files its records,
// merging two topics moves them, stating interest re-orders the rail. A
// fixture that answered the same three lists whatever was pressed would
// preview a surface that cannot be told from a broken one.
// ---------------------------------------------------------------------------

// The operator this preview attributes acts to. It is an opaque id, as the
// real one is: §4.12 resolves the author server-side and no client sends one.
const OPERATOR = "opr_demo";

// One topic the operator has accepted: a Reality Ledger entity with an id, a
// kind, a binding and his own recorded stance toward it.
interface AcceptedTopic {
  id: string;
  name: string;
  kind: string;
  binding: { kind: string; identity: string; remote?: string; paths: string[] } | null;
  interest: { state: string; reason: string; at: string; by: string };
}

// A stance nobody has recorded. It is the empty state rather than one of the
// four words, because "nobody has said anything" is a different answer from
// "not now" and the surface renders it as one.
const UNSTATED = { state: "", reason: "", at: "", by: "" };

function stated(state: string, reason: string, hoursAgo: number) {
  return {
    state,
    reason,
    at: new Date(bootedAt - hoursAgo * HOUR).toISOString(),
    by: OPERATOR,
  };
}

// The five topics this synthetic operator has accepted, one for each state the
// rail has to render: what he is working on, what he is keeping an eye on, one
// he has said nothing about, one he has parked and one he has excluded.
const accepted = new Map<string, AcceptedTopic>([
  ["atlas", {
    id: "ent_atlas",
    name: "atlas",
    kind: "repository",
    binding: BINDINGS.atlas,
    interest: stated("working", "The import pipeline is this month's work.", 26),
  }],
  ["kepler", {
    id: "ent_kepler",
    name: "kepler",
    kind: "repository",
    binding: BINDINGS.kepler,
    interest: stated("watching", "Keep filing it; spend nothing there until the cache work lands.", 70),
  }],
  ["babel", {
    id: "ent_babel",
    name: "babel",
    kind: "repository",
    binding: BINDINGS.babel,
    interest: { ...UNSTATED },
  }],
  ["scratch", {
    id: "ent_scratch",
    name: "scratch",
    kind: "repository",
    binding: BINDINGS.scratch,
    interest: stated("not-now", "Parked until the import work is done. Nothing here is wrong.", 200),
  }],
  ["sandbox", {
    id: "ent_sandbox",
    name: "sandbox",
    kind: "repository",
    binding: null,
    interest: stated("excluded", "Throwaway experiments. Not interested is a signal, not a deletion.", 400),
  }],
]);

// One topic change Babel has proposed, as the fixture holds it: the wire's
// fields plus the case its own record page shows and the age of the proposal.
//
// It is a proposal record and nothing else. The operator ruled that everything
// about a topic goes through Babel's normal chain, so accepting one is an
// ordinary ruling on an ordinary proposal — which is why these ids are `pro_`
// and why the rows in the feed are the same rows the rail links to.
interface TopicPlan {
  proposal_id: string;
  title: string;
  name: string;
  kind: string;
  operation: string;
  targets: Array<{ id: string; name: string }>;
  run_id: string;
  why: string;
  // The proposal's own case, for its record page.
  problem: string;
  outcome: string;
  verification: string[];
  ageHours: number;
}

const proposals: TopicPlan[] = [
  {
    proposal_id: "pro_topic-manifold",
    title: "Create the topic manifold and file the records that cite it",
    name: "manifold",
    kind: "repository",
    operation: "create",
    targets: [],
    run_id: "run_filing-04",
    why: "two checkouts of one repository, cited and unfiled",
    problem:
      "Records citing the manifold repository are unfiled, so the review lane draws them as "
      + "if nothing were known about what they are about.",
    outcome:
      "Create the entity for the repository the remote names and file the records that cite "
      + "either of its checkouts under it, each with the rationale that named it.",
    verification: [
      "The records citing manifold are filed under one entity",
      "The unfiled backlog falls by exactly that many",
    ],
    ageHours: 50,
  },
  {
    proposal_id: "pro_topic-merge-scratch",
    title: "Merge t/scratch into t/babel: both name the same checkout",
    name: "",
    kind: "",
    operation: "merge",
    targets: [
      { id: "ent_scratch", name: "scratch" },
      { id: "ent_babel", name: "babel" },
    ],
    run_id: "run_filing-04",
    why: "every record under scratch cites the babel checkout",
    problem:
      "Two entities carry filings for one repository, because the scratch checkout was seen "
      + "before its remote was observed.",
    outcome:
      "Fold the scratch identity into babel, so the filings under it resolve to one topic "
      + "without any of them being rewritten.",
    verification: ["One topic answers for both names", "No filing is edited"],
    ageHours: 26,
  },
  {
    proposal_id: "pro_topic-retire-sandbox",
    title: "Retire t/sandbox: it named a directory rather than a thing",
    name: "",
    kind: "",
    operation: "retire",
    targets: [{ id: "ent_sandbox", name: "sandbox" }],
    run_id: "run_filing-04",
    why: "a locator, not a subject; its filings belong in triage",
    problem:
      "The sandbox entity was created from a workspace name, and a workspace is where work "
      + "happened rather than what it was about.",
    outcome:
      "Record that the name should never have existed and return its filings to the triage "
      + "backlog. Nothing is deleted and the retirement is reversible.",
    verification: ["The topic stops being offered", "Its records read as unfiled"],
    ageHours: 8,
  },
];

// Which topic a plan is about: the name it would create, or the first entity
// it names for a merge, a split or a retirement.
function planSubject(plan: TopicPlan): string {
  return plan.name || (plan.targets[0]?.name ?? "");
}

// The proposals as posts. A topic change is an ordinary proposal, so it is an
// ordinary row: it awaits a ruling, it says why it is next, its author is the
// run that wrote it, and it is filed under the topic it is about the moment
// that topic exists.
function topicPlanPosts(): Fixture[] {
  return proposals.map((plan, index) => ({
    id: plan.proposal_id,
    kind: "proposal" as FeedKind,
    title: plan.title,
    standing: "new",
    created_at: new Date(bootedAt - plan.ageHours * HOUR).toISOString(),
    author: { run_id: plan.run_id, href: `/watch/runs/${plan.run_id}` },
    origins: [planSubject(plan)],
    score: 2 - index,
    support: 2,
    oppose: index,
    unsure: 0,
    comments: index,
    last_activity_at: new Date(bootedAt - plan.ageHours * HOUR).toISOString(),
    href: `/r/${encodeURIComponent(plan.proposal_id)}`,
    awaiting: true,
    why: `never ruled on · waiting ${elapsed(plan.ageHours)}`,
    recent: index,
    urgency: 2,
  }));
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

      // Origins. Two posts in nine resolve to none — the workspace this
      // deployment could not place — and two in nine cite evidence from two
      // repositories, which is what makes the "+n" on the row real once both
      // are accepted.
      const filing = random();
      const first = ORIGINS[Math.floor(random() * ORIGINS.length)];
      const second = ORIGINS[Math.floor(random() * ORIGINS.length)];
      const origins =
        filing < 0.22 ? [] : filing > 0.78 && second !== first ? [first, second] : [first];

      const comments = Math.floor(random() * (voted ? 15 : 4));
      // Recent activity, for `rising`. Only young posts have any: a year-old
      // record that collected a vote this morning is not rising, and the
      // formula's denominator says so, but the fixture should not pretend the
      // activity is there in the first place.
      const recent = ageHours < 48 ? Math.floor(random() * 9) : random() > 0.93 ? 1 : 0;

      const standings = STANDINGS[kind];
      // The two rows the `next` ordering is read against are pinned rather
      // than drawn: a reopened finding is more urgent than an untouched
      // proposal and must sort above it, and a fixture that left both to the
      // dice would demonstrate the ordering only on the days it happened to.
      const standing =
        kind === "finding" && index === 0
          ? "reopened"
          : kind === "proposal" && index === 0
            ? "new"
            : standings[Math.floor(random() * standings.length)];
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

      // What is waiting on the operator, and why.
      //
      // A record awaits a ruling while it is undecided — new, or reopened,
      // which is undecided again. A question awaits an answer while nobody
      // has given one, and this fixture's blocking ones are the questions
      // Babel has stopped rather than guessed on.
      const blocking = kind === "question" && random() > 0.45;
      const unanswered = kind === "question" && (blocking || random() > 0.5);
      const undecided = standing === "new" || standing === "reopened";
      const awaiting = kind === "question" ? unanswered : undecided;
      const urgency = blocking ? 0 : standing === "reopened" ? 1 : kind === "question" ? 3 : 2;
      // Why it is next, in the server's own words (internal/web/feed.go): a
      // stuck reason or a standing, then one token of age. Five words at
      // most, because that is what §8.7 gives a row.
      const waited = elapsed(ageHours);
      const why = !awaiting
        ? ""
        : kind === "question"
          ? `${blocking ? "blocks a run" : "curiosity"} · asked ${waited}`
          : `${standing === "reopened" ? "reopened" : "never ruled on"} · waiting ${waited}`;
      posts.push({
        id,
        kind,
        title,
        standing,
        created_at: createdAt.toISOString(),
        author: hasAuthor ? { run_id: runID, href: `/watch/runs/${runID}` } : null,
        origins,
        score: support - oppose,
        support,
        oppose,
        unsure,
        comments,
        last_activity_at: new Date(bootedAt - activityHours * HOUR).toISOString(),
        href,
        awaiting,
        why,
        recent,
        urgency,
      });
    });
  }
  posts.push(...topicPlanPosts());
  return posts;
}

// Day one has no posts, and the switch is ./phaseb.ts's own: a deployment
// whose frontier, queue and inbox are empty and whose front page is sixty
// posts deep is a deployment that does not exist, and the feed's own day-one
// sentence would then be unreachable in a browser.
const fixture = Bun.env.MOCK_PHASEB === "empty" ? [] : build();

// The age of something in one token, which is what fits in a five-word
// reason and what internal/web/feed.go's `why` carries: now, 7m, 4h, 3d, 2w,
// 5mo, 1y. `formatTime`'s "6 days ago" spends a third of the sentence on the
// tense.
function elapsed(ageHours: number): string {
  const spans: Array<[string, number]> = [
    ["y", 24 * 365],
    ["mo", 24 * 30],
    ["w", 24 * 7],
    ["d", 24],
    ["h", 1],
    ["m", 1 / 60],
  ];
  for (const [unit, span] of spans) {
    if (ageHours < span) continue;
    return `${Math.floor(ageHours / span)}${unit}`;
  }
  return "now";
}

// The post as the wire carries it: the fixture without the three fields the
// server keeps to itself, and with its filings in place of its origins.
//
// A post's topics are the accepted entities it is filed under, so an origin
// nobody has accepted contributes nothing — the record is unfiled, which is
// §4.13's honest state and the triage backlog. That projection is what makes
// accepting a proposal visible in every row it touches rather than only in the
// rail.
function onWire(post: Fixture): FeedPost {
  const { recent: _recent, urgency: _urgency, origins, ...wire } = post;
  return { ...wire, topics: origins.filter((name) => accepted.has(name)) };
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

// The five computed orderings, exactly as the contract states them. Each
// returns the number the sort is descending on; `new` is the creation time
// itself. `next` is not here: it is a grouping rather than a score, and a
// decimal would invite the reader to argue with a precision that does not
// exist.
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

// What a degraded shared catalog says, mirrored from internal/web/fleet.go's
// catalogUnreachable so the preview carries the server's own sentence. It
// names the catalog rather than the failure and no machine at all: which
// computer holds what is not a question this interface asks.
const CATALOG_UNREACHABLE =
  "the shared catalog could not be reached, so these records' global sync state and "
  + "host attribution are not known";

const catalogDegraded = (Bun.env.MOCK_FLEET ?? "") === "degraded";

function feed(url: URL): Response {
  const now = Date.now();
  const askedSort = url.searchParams.get("sort") ?? "";
  const sort = (["next", "hot", "new", "top", "controversial", "rising"] as FeedSort[]).includes(
    askedSort as FeedSort,
  )
    ? (askedSort as FeedSort)
    : "hot";
  const askedWindow = url.searchParams.get("t") ?? "";
  const t: FeedWindow = askedWindow in WINDOW_HOURS ? (askedWindow as FeedWindow) : "day";
  const topic = (url.searchParams.get("topic") ?? "").trim().toLowerCase();
  // A kind this deployment does not list is refused rather than answered with
  // an empty feed, which is internal/web/feed.go's rule and the reason
  // ?kind=observation now says so: an observation is evidence at depth 3 of
  // its hypothesis, and a reader who asked for a list of them is owed the
  // sentence rather than a page reading as though Babel had produced none.
  const kinds: FeedKind[] = [];
  for (const asked of (url.searchParams.get("kind") ?? "").split(",")) {
    const name = asked.trim();
    if (name === "") continue;
    if (!(FEED_KINDS as string[]).includes(name)) {
      return json({ error: `there is no record kind called ${JSON.stringify(name)}` }, 400);
    }
    if (!kinds.includes(name as FeedKind)) kinds.push(name as FeedKind);
  }
  const limit = Math.min(100, Math.max(1, Number(url.searchParams.get("limit") ?? 25) || 25));
  const offset = Math.max(0, Number(url.searchParams.get("offset") ?? 0) || 0);
  // "me" is the only value: the filter is on or it is off, and off is the
  // whole corpus rather than a wider selection.
  const needs = url.searchParams.get("needs") === "me" ? "me" : "";

  // The window applies to top and controversial and to nothing else, which
  // is the contract's rule and the reason the control is absent elsewhere.
  const scoped = sort === "top" || sort === "controversial";
  const horizon = scoped ? now - WINDOW_HOURS[t] * HOUR : Number.NEGATIVE_INFINITY;
  // The topic filter reads the filings rather than the origins, because that
  // is what a topic is: `unfiled` selects the posts no accepted entity
  // answers for, and a name selects the posts filed under it.
  const selected = fixture
    .map((post) => ({ post, wire: onWire(post) }))
    .filter(({ post, wire }) => {
      if (kinds.length > 0 && !kinds.includes(post.kind)) return false;
      if (needs === "me" && !post.awaiting) return false;
      if (topic === "unfiled" && wire.topics.length > 0) return false;
      if (topic && topic !== "unfiled" && !wire.topics.includes(topic)) return false;
      if (Date.parse(post.created_at) < horizon) return false;
      // A post nothing has happened to is not rising, and dividing zero by an
      // age would rank it above a post with one vote and a long life.
      if (sort === "rising" && post.recent === 0) return false;
      return true;
    });

  const ranked = selected
    .sort((left, right) => {
      // `next` is §8.5's order and is grouped rather than scored: what is
      // waiting comes first, most stuck first, and at equal urgency a
      // proposal outranks a finding outranks a candidate outranks a question,
      // oldest first. What is not waiting follows, newest first, because
      // nothing about it is a queue.
      if (sort === "next") {
        if (left.post.awaiting !== right.post.awaiting) return left.post.awaiting ? -1 : 1;
        if (left.post.awaiting) {
          if (left.post.urgency !== right.post.urgency) {
            return left.post.urgency - right.post.urgency;
          }
          const weight = KIND_WEIGHT[left.post.kind] - KIND_WEIGHT[right.post.kind];
          if (weight !== 0) return weight;
          return Date.parse(left.post.created_at) - Date.parse(right.post.created_at);
        }
        return Date.parse(right.post.created_at) - Date.parse(left.post.created_at);
      }
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
    needs,
    built_at: new Date(now).toISOString(),
    notice: catalogDegraded ? CATALOG_UNREACHABLE : "",
  });
}

// The three lists §4.13 answers as one: the topics the operator accepted, the
// changes Babel has proposed and nobody has ruled on, and how much is filed
// under neither.
//
// The counts are over the filings rather than over the origins, and the
// interest is the operator's own recorded stance — the two facts the rail
// orders itself by. The order is the server's: what he is working on, what he
// is keeping an eye on, what he has said nothing about, what he has parked,
// what he excluded, and inside each group the busiest first.
const INTEREST_RANK: Record<string, number> = {
  working: 0,
  watching: 1,
  "not-now": 3,
  excluded: 4,
};

function topics(): Response {
  const counts: Record<string, { posts: number; awaiting: number; latest: number }> = {};
  const origins: Record<string, number> = {};
  let unfiled = 0;
  for (const post of fixture) {
    const wire = onWire(post);
    for (const name of post.origins) origins[name] = (origins[name] ?? 0) + 1;
    if (wire.topics.length === 0) {
      unfiled += 1;
      continue;
    }
    const at = Date.parse(post.created_at);
    for (const name of wire.topics) {
      const row = counts[name] ?? { posts: 0, awaiting: 0, latest: 0 };
      row.posts += 1;
      if (post.awaiting) row.awaiting += 1;
      row.latest = Math.max(row.latest, at);
      counts[name] = row;
    }
  }
  const rows = [...accepted.values()]
    .map((topic) => {
      const row = counts[topic.name] ?? { posts: 0, awaiting: 0, latest: 0 };
      return {
        id: topic.id,
        name: topic.name,
        kind: topic.kind,
        binding: topic.binding,
        posts: row.posts,
        awaiting: row.awaiting,
        latest_at: row.latest > 0 ? new Date(row.latest).toISOString() : "",
        interest: topic.interest,
      };
    })
    .sort((left, right) => {
      const rank = (INTEREST_RANK[left.interest.state] ?? 2) - (INTEREST_RANK[right.interest.state] ?? 2);
      if (rank !== 0) return rank;
      return right.posts - left.posts || left.name.localeCompare(right.name);
    });
  const proposed = proposals.map((plan) => ({
    proposal_id: plan.proposal_id,
    title: plan.title,
    name: plan.name,
    kind: plan.kind,
    operation: plan.operation,
    targets: plan.targets,
    run_id: plan.run_id,
    why: plan.why,
    // What accepting it would touch: the records this deployment holds that
    // cite the repository it is about, which is what a filing run would file.
    posts: origins[planSubject(plan)] ?? 0,
  }));
  return json({ topics: rows, proposed, unfiled });
}

// The operator's stance toward one topic (§4.13's only direct act on a
// topic). The reason is kept verbatim and is optional for all four states,
// exactly as internal/web/topics_routes.go has it: a stance is often the whole
// statement, and refusing the act for want of prose would lose it.
async function stateInterest(request: Request, id: string): Promise<Response> {
  const body = (await request.json()) as { state?: unknown; reason?: unknown };
  const state = typeof body.state === "string" ? body.state : "";
  if (!["working", "watching", "not-now", "excluded"].includes(state)) {
    return json({
      error: `interest is one of working, watching, not-now and excluded; ${JSON.stringify(state)} is not one of them`,
    }, 400);
  }
  const topic = [...accepted.values()].find((entry) => entry.id === id || entry.name === id);
  if (!topic) {
    return json({ error: `no topic in this ledger answers to ${JSON.stringify(id)}` }, 404);
  }
  topic.interest = {
    state,
    reason: typeof body.reason === "string" ? body.reason : "",
    at: new Date().toISOString(),
    by: OPERATOR,
  };
  return json({ interest: topic.interest });
}

// Filing and unfiling one record (§4.13: filing is a link, append-only, with
// a rationale and an author). The mock moves the record's origins so the feed
// it re-reads says what the act did — the real server invalidates its index
// for the same reason.
async function fileRecord(request: Request, id: string, withdraw: boolean): Promise<Response> {
  const body = (await request.json()) as { entity?: unknown; rationale?: unknown; reason?: unknown };
  const words = typeof (withdraw ? body.reason : body.rationale) === "string"
    ? String(withdraw ? body.reason : body.rationale).trim()
    : "";
  if (words === "") {
    return json({
      error: withdraw
        ? "unfiling a record keeps the reason verbatim; this one gives none"
        : "filing a record under a topic says why it belongs there; this one says nothing",
    }, 400);
  }
  const named = typeof body.entity === "string" ? body.entity.trim() : "";
  if (named === "") return json({ error: "a filing names the topic it files under" }, 400);
  const topic = [...accepted.values()].find((entry) => entry.id === named || entry.name === named);
  if (!topic) {
    return json({
      error: `no topic in this ledger answers to ${JSON.stringify(named)}; a topic is an entity `
        + "somebody created, and filing does not create one",
    }, 404);
  }
  // A record the feed does not list is still filed: observations are evidence
  // rather than rows, and the act is about the record rather than about the
  // listing.
  const post = fixture.find((entry) => entry.id === id);
  if (post) {
    post.origins = withdraw
      ? post.origins.filter((name) => name !== topic.name)
      : [...post.origins.filter((name) => name !== topic.name), topic.name];
  }
  return json({
    filing: {
      id: `fil_${id}_${topic.id}`,
      record: id,
      record_kind: post?.kind ?? "finding",
      topic: topic.id,
      topic_name: topic.name,
      rationale: withdraw ? "" : words,
      author: "operator",
      author_id: OPERATOR,
      heuristic: false,
      withdrawn: withdraw,
      ...(withdraw ? { withdraw_reason: words } : {}),
      created_at: new Date().toISOString(),
    },
  }, withdraw ? 200 : 201);
}

// A ruling on a proposal that carries a topic plan.
//
// It is the ordinary review decision — the same route, the same wording, the
// same append — and the plan is applied server-side on an acceptance. The
// answer carries what the ledger did beside what the ruling did, because the
// two can part company: a ruling that stands over a ledger act that did not
// land is a state the operator has to be told about rather than shown as
// success.
async function ruleTopicPlan(request: Request): Promise<Response | null> {
  const body = (await request.clone().json()) as {
    subject?: { id?: unknown };
    disposition?: unknown;
    note?: unknown;
  };
  const id = typeof body.subject?.id === "string" ? body.subject.id : "";
  const index = proposals.findIndex((plan) => plan.proposal_id === id);
  if (index < 0) return null;
  const plan = proposals[index];
  const disposition = typeof body.disposition === "string" ? body.disposition : "";
  const note = typeof body.note === "string" ? body.note.trim() : "";
  if (disposition === "reject" && note === "") {
    return json({
      error: "declining a topic plan keeps the reason verbatim, and suppresses the same proposal "
        + "until something materially new turns up; this one gives none",
    }, 400);
  }
  if (!["accept", "reject", "defer"].includes(disposition)) {
    return json({ error: `there is no disposition called ${JSON.stringify(disposition)}` }, 400);
  }
  const outcome: Record<string, unknown> = {
    proposal_id: plan.proposal_id,
    operation: plan.operation,
    applied: false,
    declined: disposition === "reject",
  };
  if (disposition === "accept") Object.assign(outcome, applyPlan(plan));
  if (disposition !== "defer") RULED.push(...proposals.splice(index, 1));
  const post = fixture.find((entry) => entry.id === plan.proposal_id);
  if (post && disposition !== "defer") {
    post.standing = disposition === "accept" ? "accepted" : "rejected";
    post.awaiting = false;
    post.why = "";
  }
  return json({
    status: disposition === "accept" ? "accepted" : disposition === "reject" ? "rejected" : "deferred",
    event: {
      id: `dec_${plan.proposal_id}`,
      sequence: 1,
      disposition,
      recorded_at: new Date().toISOString(),
    },
    topic: outcome,
  });
}

// What accepting a plan does to the ledger. Creating adds the entity and files
// the records that cite it; merging folds one identity into another, so its
// filings resolve to the target without any of them being rewritten; retiring
// stops the topic speaking for itself and returns its filings to triage.
function applyPlan(plan: TopicPlan): Record<string, unknown> {
  const subject = planSubject(plan);
  if (plan.operation === "create") {
    if (accepted.has(plan.name)) {
      return { applied: false, error: `the ledger already holds a topic called "${plan.name}"` };
    }
    const id = `ent_${plan.name}`;
    accepted.set(plan.name, {
      id,
      name: plan.name,
      kind: plan.kind || "repository",
      binding: BINDINGS[plan.name] ?? null,
      interest: { ...UNSTATED },
    });
    return {
      applied: true,
      entity_id: id,
      filed: fixture.filter((post) => post.origins.includes(plan.name)).length,
    };
  }
  if (plan.operation === "merge") {
    const into = plan.targets[1]?.name ?? "";
    if (!accepted.has(subject) || !accepted.has(into)) {
      return { applied: false, error: "one of these two names is no longer a topic this ledger holds" };
    }
    let moved = 0;
    for (const post of fixture) {
      if (!post.origins.includes(subject)) continue;
      moved += 1;
      post.origins = [...post.origins.filter((name) => name !== subject && name !== into), into];
    }
    accepted.delete(subject);
    return { applied: true, entity_id: accepted.get(into)?.id, filed: moved };
  }
  if (plan.operation === "retire") {
    const topic = accepted.get(subject);
    if (!topic) return { applied: false, error: "that topic is no longer one this ledger holds" };
    accepted.delete(subject);
    return { applied: true, entity_id: topic.id, filed: 0 };
  }
  return { applied: false, error: `this deployment cannot apply a ${plan.operation} plan` };
}

// The record page for a topic plan. It is an ordinary proposal, so it peels
// like one: the claim, the case in prose, and the machinery. The fixture
// answers it here rather than in ./record.ts because the plan is this file's
// state — the same object the rail and the row read.
function planPeel(id: string): Response | null {
  const plan = proposals.find((entry) => entry.proposal_id === id)
    ?? RULED.find((entry) => entry.proposal_id === id);
  if (!plan) return null;
  const post = fixture.find((entry) => entry.id === id);
  return json({
    id,
    kind: "proposal",
    title: plan.title,
    claim: plan.title,
    standing: { label: post?.standing ?? "new", tone: post?.standing === "accepted" ? "good" : "neutral" },
    ...(post?.awaiting ? { action: { verb: "rule", label: "Rule on this topic change" } } : {}),
    case: {
      problem: plan.problem,
      outcome: plan.outcome,
      verification: plan.verification,
    },
    machinery: {
      digest: `sha256:${plan.proposal_id}`,
      schema: 1,
      created_at: post?.created_at ?? new Date(bootedAt).toISOString(),
      run_id: plan.run_id,
    },
  });
}

// The plans this session has ruled on. They leave `proposals` — a ruled
// proposal is not an open one — and their record pages stay readable, because
// a decision does not delete what it was about.
const RULED: TopicPlan[] = [];

export async function feedResponse(request: Request, url: URL): Promise<Response | null> {
  const path = url.pathname;
  const method = request.method;
  if (path === "/api/feed" && method === "GET") return feed(url);
  if (path === "/api/topics" && method === "GET") return topics();
  // §4.13's acts. Interest is the operator's own and lands here; a ruling on
  // a topic plan is an ordinary review decision, so it is answered here only
  // when the subject is one of this file's proposals and falls through to
  // ./phaseb.ts for every other record.
  if (method === "POST" && path.startsWith("/api/topics/") && path.endsWith("/interest")) {
    const id = decodeURIComponent(path.slice("/api/topics/".length, -"/interest".length));
    return stateInterest(request, id);
  }
  if (method === "POST" && path === "/api/review/decide") return ruleTopicPlan(request);
  if (method === "POST" && path.startsWith("/api/record/")) {
    const rest = path.slice("/api/record/".length);
    for (const [suffix, withdraw] of [["/file", false], ["/unfile", true]] as const) {
      if (!rest.endsWith(suffix)) continue;
      return fileRecord(request, decodeURIComponent(rest.slice(0, -suffix.length)), withdraw);
    }
  }
  if (method === "GET" && path.startsWith("/api/record/")) {
    return planPeel(decodeURIComponent(path.slice("/api/record/".length)));
  }
  // The retired acts are not paths this build serves: /api/topics/accept,
  // /decline, /{id}/retire, /merge and /split are gone with the direct
  // authority they carried, and the stance route the operator used to vote
  // with went with §8.7. An attempt on any of them falls through to the
  // unknown-route answer, which is what the real server now says.
  return null;
}
