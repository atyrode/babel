import { postJSON, request } from "./api";

// The feed's two reads: the front page and the topics beside it.
//
// It is a separate client from ./recordapi for the reason that file is
// separate from ./api — one endpoint family, one set of types, declared where
// the page that consumes them can be read against them. The record client
// answers "this record, whole"; this one answers "what is there, in what
// order", which is a different question with a different shape.
//
// Absence is on the wire here rather than in the types: the server sends the
// envelope with empty arrays rather than omitting them, because a feed with no
// posts is an answer and not a missing field. `posts` and `topics` are still
// typed nullable, because a projection that could not be read is a null the
// renderer must treat as "unknown" and never as "none" (§8.5).

// `next` is §8.5's order over what awaits the operator: urgency first, then a
// proposal before a finding before a candidate, then the longest wait. It is
// the server's, like the other five — internal/web/feed.go states and tests
// it — and it leads the bar because it is the order the operator arrives in.
export type FeedSort = "next" | "hot" | "new" | "top" | "controversial" | "rising";

// The windows `top` and `controversial` are computed over. Every other sort
// ignores the parameter, and the server echoes back what it applied, so the
// control can show what is in force rather than what was asked for.
export type FeedWindow = "hour" | "day" | "week" | "month" | "year" | "all";

// The four kinds of thing that are posts. `question` is here because §8.7
// makes a question Babel asks a post like any other; it is not a record kind
// the corpus stores under that name, which is why this union is the feed's
// own and not ./recordapi's RecordKind.
//
// An observation is not here, by operator decision (2026-09-12): it is
// evidence at depth 3 of the hypothesis that cites it, never a row of its
// own. The corpus still stores observations and still files them; the feed
// simply does not list them, and `?kind=observation` is refused as the
// unknown kind it now is rather than answered with an empty feed.
export type FeedKind = "hypothesis" | "finding" | "proposal" | "question";

export const FEED_SORTS: FeedSort[] = [
  "next",
  "hot",
  "new",
  "top",
  "controversial",
  "rising",
];
export const FEED_WINDOWS: FeedWindow[] = ["hour", "day", "week", "month", "year", "all"];
export const FEED_KINDS: FeedKind[] = ["proposal", "finding", "hypothesis", "question"];

// Whether a sort reads the window at all. The control only appears for the two
// that do, because a period selector beside "new" is a control that does
// nothing and says nothing about doing nothing.
export function windowed(sort: FeedSort): boolean {
  return sort === "top" || sort === "controversial";
}

// The run that wrote the post. A run is the author of what it wrote (§8.7) and
// its name reaches its own page; a record this deployment cannot attribute
// carries `null` rather than an empty author, so absence is one shape.
export interface FeedAuthor {
  run_id: string;
  href: string;
}

// One reviewer's vote on one exact revision, as the row draws it: which
// question the run was answering (§4.12's role) and what it answered.
//
// The dots on a row are these, not the totals: four supports across four roles
// are four answers to four different questions, and a row that drew four
// identical dots would be summing them in the reader's eye. A build whose feed
// does not carry them draws the totals instead — same colours, no role shape —
// because a score with no parts at all is the figure §8.5 refuses.
export interface FeedVote {
  role: string;
  vote: string;
}

export interface FeedPost {
  id: string;
  kind: FeedKind;
  title: string;
  // The same standing vocabulary /api/record/{id} uses. It is a bare string
  // rather than ./recordapi's union because the feed must render a standing
  // this build has no word for as itself rather than dropping it.
  standing: string;
  created_at: string;
  author: FeedAuthor | null;
  topics: string[];
  // One number and its parts, over Babel's reviewers and nobody else (§8.7):
  // the score is their support minus their opposition, and the operator's
  // acts are the rulings rather than a vote beside it. A record no reviewer
  // has assessed has no score, which the row says with an em dash: the parts
  // being nought is how a reader tells "unreviewed" from "unopposed".
  score: number;
  support: number;
  oppose: number;
  unsure: number;
  comments: number;
  last_activity_at: string;
  href: string;
  // Whether this post is waiting on the operator: a record whose review
  // standing awaits a ruling, or a question whose state awaits an answer.
  awaiting: boolean;
  // Why it is next, in five words at most, from the fields the post's own
  // store returned — a stuck reason, an age, a tier. Empty whenever
  // `awaiting` is false, because a reason to act now on something nobody is
  // waiting for is a sentence the server would have to invent.
  why: string;
  // Whether Babel's reviewers disagree about it: support on one side and
  // opposition on the other, which is the one thing a summed score cannot
  // say. Absent on a build that does not compute it, and then unmarked.
  contested?: boolean;
  // Whether a reviewer is assessing it right now. It is the only fact on a
  // row that is about this moment rather than about the record, so it is the
  // only mark on a row that moves.
  reviewing?: boolean;
  // The individual assessments behind the score, newest first. Absent on a
  // build that serves only the totals.
  votes?: FeedVote[] | null;
}

export interface FeedResponse {
  posts: FeedPost[] | null;
  total: number;
  sort: FeedSort;
  t: FeedWindow;
  topic: string;
  kinds: string[] | null;
  // What the server applied of the operator's filter: "me" when the feed was
  // narrowed to what awaits him, empty when it was not. It is echoed for the
  // same reason the sort and the window are — the control shows what is in
  // force rather than what was asked for.
  needs: string;
  built_at: string;
  // Present and non-empty only when the deployment is serving the feed on
  // terms the reader has to know about — a projection that could not be
  // rebuilt, a store that answered partially.
  notice: string;
}

// One topic as the rail, the index and the topic's own page read it (§4.13).
//
// A topic is a Reality Ledger entity and nothing else: it has a global id, a
// kind, and a binding to something real, so the same repository seen from two
// deployments is one topic rather than two names. That is why `id` is here
// beside `name` — the acts are performed on the entity, and the name is what
// the reader reads.
export interface TopicRow {
  id: string;
  name: string;
  // "repository" today; a machine, a service or a concept just as
  // legitimately, so the kind travels rather than being inferred from the
  // shape of the binding.
  kind: string;
  // What the topic's name is bound to, and how. §4.13: a topic is a name
  // bound to something real, with a reason, and *a topic is not a folder* —
  // so the binding travels as an identity with its own kind, is never
  // rendered as a path in the reading path, and is `null` for an entity this
  // deployment holds no binding facts about.
  binding: TopicBinding | null;
  posts: number;
  // How many of those posts are waiting on the operator. A topic with forty
  // posts and nothing waiting is a different thing to open from one with
  // three that all need a ruling.
  awaiting: number;
  latest_at: string;
  // Where the operator stands toward it, as §4.8 facts on the entity. An
  // empty state is "nobody has said anything", which is a different answer
  // from "not now" and is rendered as one.
  interest: TopicInterest;
}

// §4.13's stance: working on it, keeping an eye, not now, excluded — with the
// reason kept verbatim and the act attributed. `state` is a bare string
// rather than the union below because a state this build has no word for must
// render as itself rather than as one of the four.
export interface TopicInterest {
  state: string;
  reason: string;
  at: string;
  by: string;
}

// The four words the interest control offers. They are the ledger's own
// vocabulary (internal/reality's InterestStates), and the server refuses
// anything outside it.
export type InterestState = "working" | "watching" | "not-now" | "excluded";

export const INTEREST_STATES: InterestState[] = ["working", "watching", "not-now", "excluded"];

// What each stance is called where the operator states it, and what stating it
// does. §4.13 spells the four in the operator's own terms — *keep an eye*
// means Babel keeps filing into the topic and spends nothing there — and the
// sentence is what makes the difference between "not now" and "excluded"
// legible without a manual.
export const INTEREST_LABEL: Record<InterestState, string> = {
  working: "Working on it",
  watching: "Keep an eye",
  "not-now": "Not now",
  excluded: "Excluded",
};

export const INTEREST_MEANS: Record<InterestState, string> = {
  working: "Babel files into it and analysis may spend on it.",
  watching: "Babel keeps filing into it and spends nothing there.",
  "not-now": "Parked: the review lane draws elsewhere. Nothing is deleted.",
  excluded: "Left out of analysis. Not interested is a signal, not a deletion.",
};

export interface TopicBinding {
  kind: string;
  // The remote as host/owner/repo when the repository has one, and the common
  // directory otherwise — which is why it is never printed as a row: an
  // identity that may be a path belongs in a title or a fold.
  identity: string;
  // The remote alone, absent for a repository nobody has recorded one for.
  // It is beside the identity rather than derived from it because a reader
  // looking at a topic bound only by a checkout is owed the absence.
  remote?: string;
  paths: string[] | null;
}

// One topic change Babel has proposed and nobody has ruled on (§4.13).
//
// It is an ordinary proposal in the corpus — a record with an id, a page and
// the same four rulings — rather than a topic-shaped act of its own: the
// operator ruled that everything about a topic goes through Babel's normal
// chain, so the rail's rows are shortcuts to that record and never a second
// authority over it.
export interface TopicProposal {
  // The proposal record's own id, which is where the row links and what the
  // ruling names.
  proposal_id: string;
  // The proposal's own headline, which is what the record page shows.
  title: string;
  // The topic it would create, and that topic's kind. Both are empty for a
  // merge and a retirement, which name only topics that already exist.
  name: string;
  kind: string;
  // What it proposes doing: "create", "split", "merge" or "retire". A word
  // this build does not know renders as itself rather than as one of them.
  operation: string;
  // The topics the operation names — the target of a merge, the parent of a
  // split, the topic retired — as entities, because the row reads the name
  // and links the id.
  targets: TopicTarget[] | null;
  // The run that wrote it. §8.7 makes a run the author of what it wrote, so
  // the row carries the byline; it is empty for a proposal this deployment
  // cannot attribute.
  run_id: string;
  // How many of the records the proposal names this deployment holds, which
  // is what accepting it would file.
  posts: number;
  // The plan's own one-line reasoning — "32 sessions in 3 checkouts cite it"
  // — rather than this surface's paraphrase of it.
  why: string;
}

export interface TopicTarget {
  id: string;
  name: string;
}

// What each operation is called where the operator reads it, and the shape of
// the sentence the row builds from the proposal's own fields. The vocabulary
// is the server's; an operation this build has no word for is shown as the
// word the server sent.
export const OPERATION_LABEL: Record<string, string> = {
  create: "New topic",
  split: "Split",
  merge: "Merge",
  retire: "Retire",
};

export interface TopicsResponse {
  topics: TopicRow[] | null;
  // What Babel has proposed and the operator has not ruled on. A separate
  // list from the topics rather than a flag on one, because a proposal is not
  // a topic: nothing is filed under it, and until it is accepted the records
  // it names are unfiled.
  proposed: TopicProposal[] | null;
  // How many posts nothing has filed. They are in the feed rather than hidden
  // (§8.7), `topic=unfiled` selects exactly them, and *unfiled* is an honest
  // state and the triage backlog rather than a bin.
  unfiled: number;
}

export interface FeedQuery {
  sort?: FeedSort | "";
  t?: FeedWindow | "";
  topic?: string;
  kind?: string[];
  // "me" narrows the feed to the posts awaiting the operator. Absent is
  // everything, which is the whole corpus and not a wider filter.
  needs?: string;
  limit?: number;
  offset?: number;
}

// The topic value that selects the posts with no topic. It is the server's
// reserved word, not a name a workspace can take.
export const UNFILED = "unfiled";

export function getFeed(query: FeedQuery = {}): Promise<FeedResponse> {
  const params = new URLSearchParams();
  if (query.sort) params.set("sort", query.sort);
  if (query.t) params.set("t", query.t);
  if (query.topic) params.set("topic", query.topic);
  if (query.needs) params.set("needs", query.needs);
  if (query.kind && query.kind.length > 0) params.set("kind", query.kind.join(","));
  if (query.limit) params.set("limit", String(query.limit));
  if (query.offset) params.set("offset", String(query.offset));
  const search = params.toString();
  return request<FeedResponse>(`/api/feed${search ? `?${search}` : ""}`);
}

export function getTopics(): Promise<TopicsResponse> {
  return request<TopicsResponse>("/api/topics");
}

// The operator's stance toward one topic, recorded as attributed §4.8 facts.
//
// The reason is sent as typed and is optional for every one of the four: the
// server requires none — a stance is often the whole statement — so a client
// that refused the act for want of prose would lose a lawful stance to gain
// nothing.
export function setTopicInterest(
  id: string,
  state: InterestState,
  reason: string,
): Promise<{ interest: TopicInterest }> {
  return postJSON<{ interest: TopicInterest }>(
    `/api/topics/${encodeURIComponent(id)}/interest`,
    { state, reason },
  );
}

// One filing as the record page reads it back: which record, which topic, in
// whose words, and whether it is the seeder's guess or a person's judgement.
export interface RecordFiling {
  id: string;
  record: string;
  record_kind: string;
  topic: string;
  topic_name?: string;
  rationale: string;
  author: string;
  author_id?: string;
  heuristic: boolean;
  withdrawn: boolean;
  withdraw_reason?: string;
  created_at: string;
}

// Filing is a link: an `about` edge from the record to the entity, carrying a
// rationale and its author (§4.13). The topic is named rather than created —
// a name the ledger does not know is refused, because entities are created by
// an attributed operator act and never as a side effect of filing.
export function fileRecord(
  id: string,
  entity: string,
  rationale: string,
): Promise<{ filing: RecordFiling }> {
  return postJSON<{ filing: RecordFiling }>(
    `/api/record/${encodeURIComponent(id)}/file`,
    { entity, rationale },
  );
}

// Unfiling withdraws the link and keeps the reason verbatim. It deletes
// nothing: the filing stays readable as a withdrawn row, which is what makes
// "where this record was filed and why" a history rather than a current value.
export function unfileRecord(
  id: string,
  entity: string,
  reason: string,
): Promise<{ filing: RecordFiling }> {
  return postJSON<{ filing: RecordFiling }>(
    `/api/record/${encodeURIComponent(id)}/unfile`,
    { entity, reason },
  );
}
