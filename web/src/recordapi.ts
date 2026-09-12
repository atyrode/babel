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
// There is one prose field, and it is the citing record's words about the
// cited bytes — which in practice carries the excerpt itself. The surface does
// not go and read the transcript for a second one: the cited sessions run to
// tens of megabytes and a record cites up to nine of them, so the bytes stay
// where they are and `href` is how a reader reaches them.
export type EvidenceKind = "supporting" | "conflicting" | "evidence" | "counter-evidence";

// `href` is computed by the server rather than assembled here: the route that
// a citation opens is the router's business, and a client that built the
// fragment itself would have to be edited every time that route changed. The
// locator parts travel too, for the debugger at depth 5 and for the line
// number a reader sees beside the quote.
export interface RecordEvidence {
  quote: string;
  kind?: EvidenceKind;
  session_id?: string;
  path?: string;
  line?: number;
  event?: number;
  href?: string;
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

export interface RecordReception {
  operator?: OperatorReception;
  history?: OperatorHistory;
  model?: ModelReception[];
  decisions?: ReceptionDecision[];
  counts?: ReceptionCounts;
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
  evidence?: RecordEvidence[];
  reception?: RecordReception;
  machinery?: RecordMachinery;
}

export interface ReceptionResult {
  stance: OperatorStance;
  at: string;
}

// getRecord reads one record whole. The id names its kind — the server refuses
// an id whose prefix it cannot open — so the client needs no kind parameter
// and a link to a record is just its id.
export function getRecord(id: string): Promise<RecordPeel> {
  return request<RecordPeel>(`/api/record/${encodeURIComponent(id)}`);
}

// putReception records the operator's own stance on a record he has read.
//
// It is an attributed operator reception and it decides nothing: the authority
// to rule stays with the disposition events, and §4.12's boundary is not
// widened by it. Posting the same record again replaces the operator's stance
// with the later one, which is what makes the control on the page reversible.
//
// The reason is sent only when the operator wrote one. An empty string would
// be stored as a reason he gave, and "he said nothing" is a different fact
// from "he said ''".
export function putReception(
  id: string,
  stance: OperatorStance,
  reason?: string,
): Promise<ReceptionResult> {
  const trimmed = reason?.trim();
  return postJSON<ReceptionResult>(
    `/api/record/${encodeURIComponent(id)}/reception`,
    trimmed ? { stance, reason: trimmed } : { stance },
  );
}
