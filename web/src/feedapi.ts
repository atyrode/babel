import { request } from "./api";

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

// The five kinds of thing that are posts. `question` is here because §8.7
// makes a question Babel asks a post like any other; it is not a record kind
// the corpus stores under that name, which is why this union is the feed's
// own and not ./recordapi's RecordKind.
export type FeedKind = "hypothesis" | "observation" | "finding" | "proposal" | "question";

export const FEED_SORTS: FeedSort[] = [
  "next",
  "hot",
  "new",
  "top",
  "controversial",
  "rising",
];
export const FEED_WINDOWS: FeedWindow[] = ["hour", "day", "week", "month", "year", "all"];
export const FEED_KINDS: FeedKind[] = [
  "proposal",
  "finding",
  "hypothesis",
  "observation",
  "question",
];

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

export interface TopicRow {
  name: string;
  posts: number;
  latest_at: string;
  // What the topic's name is bound to, and how. §4.13: a topic is a name
  // bound to something real, with a reason, and "nothing in the surface may
  // assume a topic is a directory" — so the binding travels as an identity
  // with its own kind, is never rendered as a path in the reading path, and
  // is `null` for a name this deployment cannot bind.
  binding: TopicBinding | null;
  // Whether the filing was seeded from repository identity rather than
  // decided (§4.13). Every seeded filing says so, because the recipe that
  // will revisit it reads exactly this.
  heuristic: boolean;
}

export interface TopicBinding {
  kind: string;
  identity: string;
  paths: string[] | null;
}

export interface TopicsResponse {
  topics: TopicRow[] | null;
  // How many posts this deployment could not file under any topic. They are in
  // the feed rather than hidden (§8.7), and `topic=unfiled` selects exactly
  // them.
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
