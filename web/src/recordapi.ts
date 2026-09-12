import { postJSON, request } from "./api";

// One request returns the whole record; the reader peels it.
//
// Before this endpoint a reader assembled a proposal from four pages: the
// record from /api/proposals/:id, its decisions from /api/review/history, its
// reception from /api/evaluation/detail, its citations from /api/record/links.
// Four round trips, four loading states, four ways to be half-rendered, and a
// reader who had to know which of them held the part he wanted. The peel is
// one GET: every depth of the record arrives together, so opening depth 4 is a
// disclosure and never a fetch, and a section either has content or is absent.
//
// Absence is load-bearing here and the types say so. Every optional member is
// omitted by the server rather than sent empty, so `undefined` means "this
// record has none" and there is no second, ambiguous shape — no `case: {}`, no
// zeroed counts — that the renderer would have to treat as absent too. That is
// why nearly every field below is optional and none of them is nullable: the
// wire never says null.

export type RecordKind = "proposal" | "finding" | "hypothesis" | "observation";

// The seven standings a record can hold, and the four tones the surface has
// for them. The tone is the server's judgement, not the renderer's: whether
// "superseded" reads as bad or merely neutral is a product decision, and it
// belongs where the standing is computed rather than in a switch per page.
export type StandingLabel =
  | "new"
  | "accepted"
  | "rejected"
  | "deferred"
  | "duplicate"
  | "reopened"
  | "refine-requested"
  | "superseded";

export type StandingTone = "neutral" | "good" | "bad" | "warn";

// An operator's reception and a run's assessment vote use different words on
// purpose: a person agreeing and a run voting support are not the same act,
// and §4.12 keeps them apart by attribution. Two enums rather than one shared
// union is what stops a renderer from quietly treating them as the same thing.
export type OperatorStance = "agree" | "disagree" | "unsure";
export type ModelStance = "support" | "oppose" | "unsure";

// The §4.12 assessment roles. `challenge` is here because the store holds
// challenge assessments; a renderer that switched on five of the six would
// show a reviewer with no role at all.
export type ModelRole =
  | "reception"
  | "evidence"
  | "challenge"
  | "comparison"
  | "outcome"
  | "relevance";

export interface RecordStanding {
  label: StandingLabel;
  tone: StandingTone;
}

// The one act the record wants. The whole object is absent when there is
// nothing to do, so a reader is never offered a button that does nothing —
// which is why there is no "none" verb to test for.
export interface RecordAction {
  verb: "rule" | "answer";
  label: string;
}

export interface CaseTarget {
  system: string;
  rationale?: string;
  confidence?: string;
}

// The case in the reader's terms. These are the record's own fields with the
// schema's names kept on the wire and nowhere else: `verification` is asked as
// "how you would know it worked" on the surface, because the person deciding
// needs the question and not the field.
export interface RecordCase {
  problem?: string;
  outcome?: string;
  impact?: string;
  scope?: string;
  classification?: string;
  uncertainty?: string;
  verification?: string[];
  risks?: string[];
  open_questions?: string[];
  prerequisites?: string[];
  targets?: CaseTarget[];
}

// What a record cites, and on which side.
//
// `kind` is load-bearing rather than decorative: §4.5 requires a proposal to
// state the material that conflicts with it, and an interface that rendered a
// conflicting excerpt the same way as a supporting one would invert the
// record's own argument.
//
// Two prose fields, in the order they matter. `excerpt` is the cited bytes —
// what a person actually said, recovered by the server from the session log at
// the locator and checked against the digest the citation carries — and
// `quote` is the citing record's note about them. The note used to be the only
// one, which meant the page showed Babel's paraphrase of a human sentence and
// kept the sentence itself behind a link.
//
// `excerpt` is absent, never empty, when this deployment cannot recover the
// bytes: the session is on another machine, the log was rotated, or what sits
// at that offset no longer hashes to what was cited. A blank pull-quote would
// read as somebody who said nothing.
export type EvidenceKind = "supporting" | "conflicting" | "evidence" | "counter-evidence";

// The three speakers an excerpt can have. A harness spells its roles its own
// way — `toolResult` in one, `tool` in another — and the server normalizes to
// these three or omits the field rather than guessing.
export type Speaker = "user" | "assistant" | "tool";

// `href` is computed by the server rather than assembled here: the route that
// a citation opens is the router's business, and a client that built the
// fragment itself would have to be edited every time that route changed. The
// locator parts travel too, for the debugger at depth 5 and for the line
// number a reader sees beside the quote.
export interface RecordEvidence {
  quote: string;
  kind?: EvidenceKind;
  excerpt?: string;
  speaker?: Speaker;
  session_title?: string;
  session_id?: string;
  path?: string;
  line?: number;
  event?: number;
  href?: string;
}

// Where a record came from: the first conversation it cites that this
// deployment holds, with that session's own title, workspace, date and cost.
//
// `cost_usd` and `turns` are nullable rather than optional because the server
// distinguishes two different absences and only one of them is "this record
// has no origin": a session Babel holds but whose harness recorded no usage
// has an origin with no cost, and rendering that as $0.00 would print a
// measurement nobody took.
export interface RecordOrigin {
  session_id: string;
  session_title?: string;
  workspace?: string;
  at?: string;
  cost_usd: number | null;
  turns: number | null;
  href?: string;
}

// One record on the far side of a relation. Which fields are filled depends on
// the relation, and the server documents that at each: a competing remedy
// carries its standing, a suspected duplicate carries the overlap where a
// heuristic measured one, a sibling carries its kind.
export interface RelatedRecord {
  id: string;
  kind?: RecordKind;
  title?: string;
  standing?: StandingLabel;
  overlap?: number;
}

// Every connection the record has that its own words do not state. Five
// relations rather than one list because a reader acts differently on each: a
// competing remedy is a choice, a suspected duplicate is a comparison, a
// supersession is a warning that he may be reading the wrong wording, and a
// sibling is the rest of one run's thought.
export interface RecordRelated {
  addressing?: RelatedRecord[];
  duplicates?: RelatedRecord[];
  supersedes?: RelatedRecord[];
  superseded_by?: RelatedRecord[];
  siblings?: RelatedRecord[];
}

export interface OperatorReception {
  stance: OperatorStance;
  reason?: string;
  at: string;
}

// The operator's earlier stances, newest first, holding only what he no
// longer says. §4.12 is append-only, so a changed mind is a second record
// rather than an edit, and this is how "he used to agree" stays readable
// instead of being silently replaced by what he says now.
export type OperatorHistory = OperatorReception[];

export interface ModelReception {
  actor: string;
  role: ModelRole;
  stance: ModelStance;
  rationale?: string;
  at: string;
}

export interface ReceptionDecision {
  disposition: "accept" | "reject" | "defer" | "duplicate" | "reopen";
  by: string;
  at: string;
  note?: string;
}

// The model reception tally, model-only. The operator's stance is never summed
// into it — an operator-authored feedback record carries no vote — so these
// numbers can render beside the reviewers without widening §4.12's boundary.
export interface ReceptionCounts {
  support: number;
  oppose: number;
  unsure: number;
}

// One role's answer to its own question, which is the only tally over Babel's
// reviewers that means anything. A role is what a reviewer was authorized to
// answer, so four supports across four roles are four answers to four
// questions; summing them is how a record with one satisfied evidence check
// came to read as broadly supported. Disagreement inside one role is the
// signal, and `contested` is exactly that.
export interface RoleReception {
  role: ModelRole;
  support: number;
  oppose: number;
  unsure: number;
  opposing_rationales?: string[];
}

export interface RecordReception {
  operator?: OperatorReception;
  history?: OperatorHistory;
  model?: ModelReception[];
  decisions?: ReceptionDecision[];
  counts?: ReceptionCounts;
  by_role?: RoleReception[];
  contested?: boolean;
}

export interface MachineryLink {
  kind: string;
  direction: "from" | "to";
  id: string;
  title?: string;
}

export interface MachineryReceipt {
  id: string;
  stage?: string;
  cost?: string;
  at?: string;
}

export interface MachineryRevision {
  id: string;
  at?: string;
}

// What one run spent, as its receipt recorded it. The duration is Babel's own
// clock over the whole run — preparation and storage included — because that
// is the number the receipt states and a narrower one derived here would
// disagree with it.
export interface RecordCost {
  usd: number | null;
  input_tokens?: number;
  output_tokens?: number;
  model?: string;
  duration_s?: number;
}

// Everything a person debugging Babel needs and a person reading a record does
// not. `host` is present only for a record this deployment resolved through the
// shared catalog rather than holding itself.
export interface RecordMachinery {
  revision?: string;
  digest?: string;
  schema?: number;
  created_at?: string;
  run_id?: string;
  policy_version?: string;
  host?: string;
  links?: MachineryLink[];
  receipts?: MachineryReceipt[];
  revisions?: MachineryRevision[];
  // What producing this record cost, out of the producing run's own receipt.
  // Absent for a record this machine did not produce: §9 seals the worker's
  // accounting before a receipt leaves its host, so only the producing
  // machine can be asked — and an absent block means nobody here can price
  // it, never that it was free. `usd` is null for the same reason at one
  // level down: an engine that never reported its own session accounting
  // leaves a receipt whose usage is zeros.
  cost?: RecordCost;
}

export interface RecordPeel {
  id: string;
  kind: RecordKind;
  title?: string;
  claim?: string;
  standing?: RecordStanding;
  action?: RecordAction;
  // Present only when the catalog could not be consulted, carrying the
  // sentence that says on what terms the record is being shown.
  notice?: string;
  case?: RecordCase;
  origin?: RecordOrigin;
  evidence?: RecordEvidence[];
  related?: RecordRelated;
  reception?: RecordReception;
  machinery?: RecordMachinery;
}

// getRecord reads one record whole. The id names its kind — the server refuses
// an id whose prefix it cannot open — so the client needs no kind parameter
// and a link to a record is just its id.
export function getRecord(id: string): Promise<RecordPeel> {
  return request<RecordPeel>(`/api/record/${encodeURIComponent(id)}`);
}

// The operator records no stance. §8.7: "Babel votes; the operator rules" —
// his acts on a record are the rulings and the question he asks about it, so
// the write that recorded a stance is gone from this client and from the
// server. What he recorded before it went stays readable in the reception
// block below, because §4.12 appends and nothing here deletes.

// The conversation under the post, and the moderator's log beside it.
//
// §8.7: "a reviewer's contribution prose, a refinement, the operator's reason
// in his own words, the answer to a question and the reason on a
// reconsideration are all comments, threaded by what they relate to". They
// are one endpoint rather than five because they are one thing to read: the
// records they come out of are §4.12's and §4.7's business, and a reader
// following a discussion should not have to know which store each line was
// written into.
//
// Rulings are not comments and do not arrive in the same list. Accept, reject,
// defer, duplicate and reopen are the append-only authority of §4.7, so they
// travel as `acts` — attributed and dated — and the renderer places them in
// the thread as the acts they are. Merging them into `comments` would let a
// decision read as an opinion.
// A question the operator asked is a comment with its own kind. It is a
// feedback record carrying the reason and a marker a later review of the
// record can find, which is what makes "Babel's next review must answer it"
// a property of the store rather than a promise in the interface.
export type CommentKind =
  | "contribution"
  | "refinement"
  | "reason"
  | "answer"
  | "reconsideration"
  | "question";

// Who wrote a line. `kind` is the §4.12 attribution boundary on the wire: a
// run authored what it wrote and the operator authored what he wrote, and the
// surface never renders one as the other. `href` reaches a run's own page and
// is empty for the operator, who has no page.
export interface CommentAuthor {
  kind: "run" | "operator";
  id: string;
  href: string;
}

// One line of the conversation. `role` is the question a reviewer was asked,
// empty for anyone who was not asked one; `related_id` is what this line
// replies to, empty at the top of the thread. Both are empty strings rather
// than omitted fields because this route sends a complete row — the peel's
// absent-means-absent rule is about a record's own sections, and a comment
// with no role is a comment whose role is nothing.
export interface Comment {
  id: string;
  kind: CommentKind;
  author: CommentAuthor;
  role: string;
  text: string;
  at: string;
  related_id: string;
  replies?: Comment[];
}

// One ruling, as the thread shows it. `reason` is the note the ruling carried,
// empty when it carried none.
export interface Act {
  id: string;
  act: "accept" | "reject" | "defer" | "duplicate" | "reopen";
  by: string;
  at: string;
  reason: string;
}

// `total` counts the comments, replies included, and never the acts: the
// heading over the thread says how much conversation there is, and a ruling is
// not part of the conversation.
export interface CommentThread {
  comments: Comment[];
  acts: Act[];
  total: number;
}

export interface CommentResult {
  comment: Comment;
}

export function getComments(id: string): Promise<CommentThread> {
  return request<CommentThread>(`/api/record/${encodeURIComponent(id)}/comments`);
}

// postComment records the operator's own words about a record he has read.
//
// It is a feedback record carrying a reason and no polarity: §8.7 gives the
// operator "a box the operator writes into that records a feedback record
// carrying a reason and no polarity", so writing here moves no score. Nothing
// he writes does: the score is Babel's reviewers' and he has no vote.
//
// `kind` is what the request vocabulary calls it — a comment or a question —
// and it is absent for a comment rather than sent as the default, because the
// route reads an absent kind as a comment and a client that spelled the
// default out would be the only place that knew it.
//
// The text is sent exactly as typed. The server escapes it and the surface
// renders it inside a quoted frame, which is what keeps an operator's own
// sentence from becoming markup.
export function postComment(
  id: string,
  text: string,
  kind: "comment" | "question" = "comment",
): Promise<CommentResult> {
  return postJSON<CommentResult>(
    `/api/record/${encodeURIComponent(id)}/comments`,
    kind === "question" ? { text, kind } : { text },
  );
}
