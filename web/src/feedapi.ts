import { request } from "./api";
import type { OperatorStance } from "./recordapi";

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

export type FeedSort = "hot" | "new" | "top" | "controversial" | "rising";

// The windows `top` and `controversial` are computed over. Every other sort
// ignores the parameter, and the server echoes back what it applied, so the
// control can show what is in force rather than what was asked for.
export type FeedWindow = "hour" | "day" | "week" | "month" | "year" | "all";

// The five kinds of thing that are posts. `question` is here because §8.7
// makes a question Babel asks a post like any other; it is not a record kind
// the corpus stores under that name, which is why this union is the feed's
// own and not ./recordapi's RecordKind.
export type FeedKind = "hypothesis" | "observation" | "finding" | "proposal" | "question";

export const FEED_SORTS: FeedSort[] = ["hot", "new", "top", "controversial", "rising"];
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
  // One number and its parts. The score is support − oppose across Babel's
  // reviewers and the operator together (§8.7); the parts are what the
  // breakdown shows, so the number on the row and the gesture that explains
  // it are never computed from two different reads.
  score: number;
  support: number;
  oppose: number;
  unsure: number;
  you: OperatorStance | "";
  comments: number;
  last_activity_at: string;
  href: string;
}

export interface FeedResponse {
  posts: FeedPost[] | null;
  total: number;
  sort: FeedSort;
  t: FeedWindow;
  topic: string;
  kinds: string[] | null;
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
  if (query.kind && query.kind.length > 0) params.set("kind", query.kind.join(","));
  if (query.limit) params.set("limit", String(query.limit));
  if (query.offset) params.set("offset", String(query.offset));
  const search = params.toString();
  return request<FeedResponse>(`/api/feed${search ? `?${search}` : ""}`);
}

export function getTopics(): Promise<TopicsResponse> {
  return request<TopicsResponse>("/api/topics");
}
