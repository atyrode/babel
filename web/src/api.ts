export interface VersionInfo {
  version: string;
  commit: string;
  dirty: boolean;
  go: string;
  platform: string;
}

export interface StateInfo {
  configured: boolean;
  repository: string;
  host_id: string;
}

export interface SessionSummary {
  harness: string;
  source_id: string;
  selector: string;
  size: number;
  modified: string | null;
  title: string | null;
  // Where the title came from: "recorded" by the harness, "derived" by babel
  // from the session's own records, or "inferred" by a model. Null exactly
  // when title is. Three different kinds of claim render as the same short
  // line of text, so a surface that shows the title without this is showing
  // babel's arithmetic as if the harness had recorded it.
  title_provenance: string | null;
  workspace: string | null;
  continuation_grade: boolean;
}

export interface ScanState {
  running: boolean;
  described: number;
  total: number;
  failed: number;
  harness?: string;
  started_at?: string;
  finished_at?: string;
  error?: string;
}

export interface SessionsResponse {
  sessions: SessionSummary[];
  refreshed_at: string;
  scan: ScanState;
}

export interface CompletenessReason {
  field: string;
  reason: string;
}

export interface RepoInfo {
  remote?: string;
  commit?: string;
  branch?: string;
}

export interface Artifact {
  rel_path: string;
  source_path: string;
  size: number;
}

export interface Blob {
  digest: string;
  source_path: string;
  size: number;
}

export interface SessionDetail {
  harness: string;
  source_id: string;
  selector: string;
  primary_path: string;
  primary_size: number;
  described_at: string;
  hint?: string;
  title: string | null;
  title_provenance: string | null;
  workspace: string | null;
  created_at: string | null;
  modified_at: string | null;
  lifecycle: string | null;
  repo: RepoInfo | null;
  completeness?: CompletenessReason[];
  adapter_metadata_schema: number;
  adapter_metadata?: unknown;
  artifacts?: Artifact[];
  blobs?: Blob[];
  unresolved_blob_refs?: string[];
  continuation_grade: boolean;
}

export interface TranscriptEvent {
  index: number;
  role: string;
  kind: string;
  time: string | null;
  text: string;
}

export interface TranscriptResponse {
  total: number;
  events: TranscriptEvent[];
}

export interface ArchiveHost {
  host: string;
  snapshots: number;
  latest_time: string;
  latest_id: string;
  latest_short_id: string;
  tags?: string[];
}

export interface ArchiveStatus {
  repository: string;
  snapshots: number;
  hosts: ArchiveHost[];
}

// ArchiveSessionRow is deliberately not a SessionSummary and shares no field
// with it beyond the four a snapshot's file listing actually carries. Browsing
// another host's archive downloads no transcript bytes, so title, workspace,
// modified time, and continuation grade are not merely null there — they are
// unobserved, and this type cannot express them at all. A component holding
// one of these rows therefore cannot render an absent title as an empty cell;
// it has to say the snapshot listing does not carry one.
export interface ArchiveSessionRow {
  harness: string;
  source_id: string;
  selector: string;
  size: number;
  // Whether this machine already holds a fetched materialization of the
  // session, and where it landed.
  fetched: boolean;
  fetched_path?: string;
}

export interface ArchiveSessionsResponse {
  host: string;
  // The snapshot the request named; empty means that host's newest.
  snapshot: string;
  sessions: ArchiveSessionRow[];
}

export interface VerifyResult {
  repository: string;
  deep: boolean;
  ok: boolean;
  error?: string;
}

export interface FetchResult {
  selector: string;
  snapshot_id: string;
  snapshot_short_id: string;
  snapshot_time: string;
  target: string;
  files: number;
  bytes: number;
  included: string[];
  missing?: string[];
  already_present: boolean;
}

export interface LockResult {
  revoked: boolean;
  stopping: boolean;
}

// ---------------------------------------------------------------------------
// Phase B: analysis, frontier, review, Reality, and retrieval wire types.
//
// Envelope keys follow the shared Phase B contract exactly (including its
// camelCase statusHistory/contextId/duplicateOfId/questionId/planId/answerId);
// record fields are snake_case, and every `payload` object mirrors the Go
// service structs' own JSON tags, so the browser types cannot drift from what
// the services store. See local://phaseb-web-wire-contract.md.
// ---------------------------------------------------------------------------

export type HypothesisStatus =
  | "untriaged"
  | "queued"
  | "investigating"
  | "deferred"
  | "rejected"
  | "promoted";

export type Disposition = "accept" | "reject" | "defer" | "duplicate";

export type ReviewStatus =
  | "new"
  | "accepted"
  | "rejected"
  | "deferred"
  | "duplicate"
  | "refine-requested";

export type ReviewSubjectType = "hypothesis" | "finding" | "proposal";

// Model-supplied gradings are three-valued, never numeric: §10 warns that
// confidence never substitutes for evidence, and a decimal invites exactly
// that. The UI renders them as words, never as bars or percentages.
export type Grading = "low" | "moderate" | "high";

// ---------------------------------------------------------------------------
// Fleet attribution (issue #109 items 3 and 4).
//
// Every Phase B listing row carries these, and they mean exactly what the Go
// DTOs mean. Two of them exist because one string cannot say what it has to.
//
// `host` is empty for a record whose origin instance registered no host, and
// `host_attributed` is what distinguishes that from a host whose display name
// happens to be empty. The interface renders the absence as "unattributed" and
// never as the local machine: filing one machine's analysis under another's is
// the failure the pair exists to prevent.
//
// `sync` is one of exactly three values and they are three different facts.
// "local" means no remote row and no journal claim — nothing is going to carry
// this record anywhere — where "pending-sync" promises something will, and
// "unknown" means the shared catalog could not be reached so this machine did
// not find out. A renderer must never collapse any of them into another.
//
// "unknown" arrives with `sync_degraded` on the listing envelope, which is what
// makes it a state rather than a shrug: the rows still render, and the page says
// why their global state is not known.
// ---------------------------------------------------------------------------

export type SyncState = "committed" | "pending-sync" | "local" | "unknown";

// SyncNotice is the degraded marker a listing envelope carries when the shared
// catalog could not answer. The rows still render — this machine's durable store
// is local, and another machine's outage must not take away a local page — with
// every unresolved row's `sync` as "unknown" and this saying why.
export interface SyncNotice {
  sync_degraded?: boolean;
  // Operator-facing prose from the server. Rendered verbatim; the browser
  // claims nothing about the cause itself.
  sync_detail?: string;
}

export interface FleetMark {
  // Optional only because a mock or an older server may omit them; the current
  // server always sends them.
  host?: string;
  // The machine's identity, as opposed to `host`'s label. A client narrowing a
  // merged list matches on this and never on the display name: a display name
  // is a label for reading, two machines may carry the same one, and a filter
  // that matched labels would silently merge them.
  host_id?: string;
  host_attributed?: boolean;
  local_host?: boolean;
  sync?: SyncState | string;
  committed_at?: string;
  // Why this row has no content, when it has none: a key this machine does not
  // hold, a payload shape this build does not read. Rendered, never swallowed.
  unopened?: string;
}

// One fleet record as GET /api/fleet/records lists it.
export interface FleetRecord {
  record_id: string;
  run_id: string;
  kind: string;
  host: string;
  host_id: string;
  host_attributed: boolean;
  local_host: boolean;
  // The origin instance: the actor that generated the run and committed the
  // record. Always present, which is why it has no "attributed" companion.
  actor: string;
  sync: SyncState | string;
  committed_at?: string;
  summary?: string;
  unopened?: string;
}

// One machine in the host filter's vocabulary. `attributed: false` is the group
// of records with no host at all, offered as an option rather than hidden: a
// dropped group looks like records that do not exist.
export interface FleetHost {
  host: string;
  host_id: string;
  attributed: boolean;
  records: number;
  pending: number;
  newest_commit?: string;
}

export interface FleetRecordsResponse {
  // False means this machine has no shared backend, which is a fact about the
  // deployment rather than a failure. A backend that exists and did not answer
  // arrives as an APIError instead, and the two read differently on screen.
  configured: boolean;
  items: FleetRecord[];
  hosts: FleetHost[];
  pending: number;
}

export interface FleetHostsResponse {
  configured: boolean;
  // This machine's own host id, absent when it has registered none.
  local_host?: string;
  hosts: FleetHost[];
}

export interface FleetRecordFilter {
  hosts?: string[];
  kinds?: string[];
  pending?: boolean;
  limit?: number;
  offset?: number;
}

export interface RunCounts {
  tool_requests: number;
  tools_denied: number;
  retrieval: number;
  deferred: number;
  rejected: number;
  failures: number;
  // A non-zero redaction count is an audit signal: something on the far side
  // of the worker boundary tried to write a credential into the record.
  redactions: number;
}

export interface RunSummary {
  receipt_id: string;
  run_id: string;
  preparation_id: string;
  revision: number;
  recorded_at: string;
  sync: string; // "pending-sync" | "committed"
  counts: RunCounts;
  // Why the run happened, as its receipt recorded it. Empty on a receipt
  // written before receipts carried one.
  authority: RunAuthority;
  // Which machine produced the run, read from the shared catalog rather than
  // from the receipt (issue #109 item 4). Both are absent on a run the catalog
  // cannot attribute, and the receipt strip renders that as "unattributed"
  // rather than as this machine.
  host?: string;
  host_attributed?: boolean;
}

export interface RecipeSummary {
  id: string;
  version: number;
  kind: string; // "policy" | "lens" | "meta"
  title: string;
  default: boolean;
  scope: string[];
  stages: string[];
  capabilities: string[];
}

export interface WorkerAvailability {
  available: boolean;
  // Operator-facing prose from the server explaining why exploration cannot
  // start here. The browser renders it verbatim and claims nothing itself.
  detail: string;
}

export interface AnalysisState extends SyncNotice {
  configured: boolean;
  worker: WorkerAvailability;
  runs: RunSummary[];
  runs_total?: number;
  cookbook: RecipeSummary[];
}

export interface EvidenceLocator {
  path: string;
  line: number;
  byte_offset: number;
  digest: string;
}

export interface EvidenceRef {
  locator: EvidenceLocator;
  note?: string;
  // Best-effort catalog selector ("harness/source_id") when the server could
  // resolve the locator to a described session; absent otherwise.
  selector?: string;
}

// A candidate as a listing shows it. It extends FleetMark because a listing is
// now fleet-wide: with ?fleet=1 the rows after this machine's own belong to
// other hosts, and such a row carries no review_status and no observation count
// at all — both are the owning host's derivations, and a zero rendered as a fact
// would say a remote candidate rests on no evidence.
export interface HypothesisSummary extends FleetMark {
  id: string;
  run_id: string;
  ancestor_id?: string;
  created_at: string;
  status: HypothesisStatus;
  statement: string;
  provisional_labels?: string[];
  observations: number;
  // Additive server field: the derived §4.7 review status beside the
  // exploration status. Optional so the mock stays minimal.
  review_status?: ReviewStatus;
}

// `total` is this machine's frontier count and stays that way under ?fleet=1:
// the fleet rows are an attributed appendix, not more pages of the local
// frontier, so a heading reading "N in the frontier" keeps one meaning.
export interface HypothesesResponse extends SyncNotice {
  items: HypothesisSummary[];
  total: number;
}

export interface HypothesisPayload {
  statement: string;
  origin_cues?: string[];
  provisional_labels?: string[];
  // Sorting signals in [0,1]. §5.2 confines them to ordering; the UI shows
  // them as plain numbers labelled as such, never as a strength indicator.
  novelty: number;
  priority: number;
  notes?: string;
}

export interface Hypothesis {
  id: string;
  ancestor_id?: string;
  run_id: string;
  schema_version: number;
  created_at: string;
  status: HypothesisStatus;
  payload: HypothesisPayload;
}

export interface Actor {
  // "run" or "operator". The distinction is #87's whole attribution story:
  // a chain that cannot say whether a candidate was reworded by inference or
  // by its owner is a history nobody can audit.
  kind: string;
  id: string;
}

export interface StatusEvent {
  id: string;
  hypothesis_id: string;
  sequence: number;
  status: HypothesisStatus;
  run_id: string;
  actor: Actor;
  recorded_at: string;
  note?: string;
}

export interface ObservationPayload {
  claim: string;
  category?: string;
  confidence: Grading;
  impact: Grading;
  evidence: EvidenceRef[];
  // Exactly one of counter_evidence / counter_evidence_absent is set, so an
  // empty list can never be mistaken for an unasked question (§4.3).
  counter_evidence?: EvidenceRef[];
  counter_evidence_absent?: boolean;
  temporal_status?: string;
}

export interface Observation {
  id: string;
  ancestor_id?: string;
  hypothesis_id: string;
  run_id: string;
  recipe_id: string;
  recipe_version: number;
  schema_version: number;
  evidence_count: number;
  created_at: string;
  payload: ObservationPayload;
}

export interface LinkView {
  id: string;
  from_id: string;
  to_id: string;
  type: string;
  created_at: string;
  note?: string;
  // Statement excerpt of the far-end hypothesis, best-effort, so a link list
  // reads as prose rather than identifiers.
  other_statement?: string;
}

export interface LineageNode {
  kind: string;
  id: string;
}

export interface LineageEdge {
  id: string;
  relation: string;
  from: LineageNode;
  to: LineageNode;
  created_at: string;
  generation: number;
}

export interface Lineage {
  node: LineageNode;
  ancestors: LineageEdge[];
  descendants: LineageEdge[];
}

export interface HypothesisDetail {
  hypothesis: Hypothesis;
  statusHistory: StatusEvent[];
  observations: Observation[];
  links: LinkView[];
  lineage: Lineage;
}

// A consolidation as a listing shows it, on HypothesisSummary's terms: a row
// from another host carries the title and nothing this machine would have had to
// derive.
export interface FindingSummary extends FleetMark {
  id: string;
  run_id: string;
  created_at: string;
  title: string;
  observations: number;
  hypotheses: number;
  review_status: ReviewStatus;
}

export interface FindingsResponse extends SyncNotice {
  items: FindingSummary[];
  total: number;
}

export interface FindingPayload {
  title: string;
  pattern: string;
  significance?: string;
  scope?: string[];
  recurrence?: number;
  counter_evidence?: EvidenceRef[];
  counter_evidence_absent?: boolean;
  temporal_status?: string;
}

export interface Finding {
  id: string;
  ancestor_id?: string;
  run_id: string;
  schema_version: number;
  created_at: string;
  observation_ids: string[];
  hypothesis_ids: string[];
  payload: FindingPayload;
}

export interface ProposalTarget {
  system: string;
  confidence: Grading;
  rationale?: string;
}

export interface ProposalPayload {
  title: string;
  problem: string;
  outcome: string;
  applicability?: string;
  temporal_status?: string;
  supporting?: EvidenceRef[];
  conflicting?: EvidenceRef[];
  uncertainty?: string;
  impact: Grading;
  estimated_scope?: string;
  targets?: ProposalTarget[];
  risks?: string[];
  open_questions?: string[];
  prerequisites?: string[];
  verification_criteria?: string[];
  classification: string;
  destinations?: string[];
}

export interface Proposal {
  id: string;
  ancestor_id?: string;
  run_id: string;
  schema_version: number;
  created_at: string;
  finding_ids: string[];
  hypothesis_ids: string[];
  review_status: ReviewStatus;
  payload: ProposalPayload;
}

export interface FindingDetail {
  finding: Finding;
  observations: Observation[];
  proposals: Proposal[];
}

export interface ReviewSubject {
  type: ReviewSubjectType;
  id: string;
}

// One review-inbox row. A row from another host carries an empty status and
// zero counts because this machine holds none of that host's append-only review
// history; `local_host` is how a renderer tells the two apart and shows an
// absence rather than a decided-nothing claim.
export interface QueueItem extends FleetMark {
  subject: ReviewSubject;
  enrolled_at: string;
  status: ReviewStatus;
  decisions: number;
  last_decided_at?: string;
  refinements: number;
  // The subject's statement/title/claim, so the queue is readable.
  excerpt: string;
}

export interface ReviewQueueResponse extends SyncNotice {
  items: QueueItem[];
  total?: number;
}

export interface DecideRequest {
  subject: ReviewSubject;
  disposition: Disposition;
  contextId?: string;
  duplicateOfId?: string;
  note?: string;
}

export interface DecideResult {
  status: ReviewStatus;
  event: {
    id: string;
    sequence: number;
    disposition: Disposition;
    recorded_at: string;
  };
}

export interface ReviewContext {
  id: string;
  author: string;
  at: string;
  text: string;
}

export interface DecisionView {
  id: string;
  sequence: number;
  disposition: Disposition;
  reviewer_id: string;
  recorded_at: string;
  duplicate_of_id?: string;
  note?: string;
  context?: ReviewContext;
}

export interface RefinementView {
  request: {
    id: string;
    disposition_id: string;
    subject: ReviewSubject;
    created_at: string;
    guidance: string;
    scope?: string[];
  };
  // Absent until a refinement worker reported an outcome: an authorized
  // request with no outcome is a normal, visible state rather than a gap.
  outcome?: {
    id: string;
    mode: string;
    agent_id: string;
    recorded_at: string;
    revision?: ReviewSubject;
    memory_proposal_id?: string;
  };
}

export interface ReviewHistory {
  status: ReviewStatus;
  decisions: DecisionView[];
  refinements: RefinementView[];
}

export interface AnswerView {
  id: string;
  question_id: string;
  sequence: number;
  author: string;
  at: string;
  recorded_at: string;
  outcome: string; // "answered" | "unknown" | "declined"
  text: string;
}

export interface ActionView {
  id: string;
  position: number;
  kind: string;
  state: string;
  result_id?: string;
  applied_at?: string;
  // The reality.ActionPayload verbatim: rationale plus exactly one
  // kind-specific option field, rendered as structured JSON.
  payload: { rationale: string } & Record<string, unknown>;
}

export interface PlanView {
  id: string;
  question_id: string;
  answer_id: string;
  interpreter_version: number;
  created_at: string;
  state: string; // "proposed" | "accepted" | "rejected" | ...
  summary: string;
  actions: ActionView[];
}

export interface QuestionSummary {
  id: string;
  kind: string;
  class: string; // "blocking" | "maintenance" | "curiosity"
  state: string;
  sensitivity: string;
  created_at: string;
  prompt: string;
  why_asked: string;
  target_entity_ids: string[];
  target_predicates?: string[];
  // The §4.8 ranking with its per-factor terms, returned so the policy can
  // be argued with rather than presented as a bare number.
  score: number;
  terms: Record<string, number>;
  answers: AnswerView[];
  plans: PlanView[];
}

export interface RealityInbox {
  items: QuestionSummary[];
  total?: number;
}

export interface EntityView {
  id: string;
  kind: string;
  schema_version: number;
  created_at: string;
  role: string;
  canonical_id: string;
  display_name: string;
  notes?: string;
}

export interface AliasView {
  id: string;
  entity_id: string;
  kind: string;
  state: string;
  created_at: string;
  value: string;
  note?: string;
}

export interface RelationshipView {
  id: string;
  kind: string;
  state: string;
  created_at: string;
  from: { id: string; display_name: string };
  to: { id: string; display_name: string };
  note?: string;
}

export interface FactValueView {
  kind: string;
  enum?: string;
  text?: string;
  object_id?: string;
}

export interface FactView {
  id: string;
  subject_id: string;
  predicate: string;
  value: FactValueView;
  valid_from: string;
  valid_until?: string;
  observed_at: string;
  recorded_at: string;
  expires_at?: string;
  authority: { kind: string; id: string };
  confidence: Grading;
  sensitivity: string;
  status: string; // "proposed" | "active" | "superseded" | "disputed" | "stale"
  supersedes?: string;
  note?: string;
}

export interface EntityDetail {
  entity: EntityView;
  aliases: AliasView[];
  relationships: RelationshipView[];
  facts: FactView[];
}

export interface AnswerResult {
  answerId: string;
  state: string;
}

export interface PlanAcceptResult {
  applied: Array<{ kind: string; id: string }>;
  state: string;
}

// A hit deliberately carries no score, rank, or relevance field: §5.4's rule
// is that retrieval rank never becomes evidence strength, and the UI keeps it
// unrepresentable by never numbering or grading result rows.
export interface SearchHit {
  harness: string;
  adapter_schema: number;
  source_id: string;
  selector: string;
  index: number;
  kind: string;
  role?: string;
  tool?: string;
  outcome?: string;
  time?: string;
  paths?: string[];
  partial: boolean;
  text: string;
  locator: EvidenceLocator;
}

export interface SearchResponse {
  hits: SearchHit[];
}

// ---------------------------------------------------------------------------
// The dashboard's aggregate read (GET /api/overview).
//
// One document, one request, six independently degrading sections: a panel
// whose service could not be read carries `available: false` and the server's
// own note, and the rest of the page still renders. Nothing here is a new
// source of truth — every number is the owning page's number, so a panel and
// the page it links to cannot disagree.
// ---------------------------------------------------------------------------

export interface OverviewSection {
  available: boolean;
  unavailable?: string;
}

export interface OverviewArchiveHost {
  host: string;
  snapshots: number;
  latest_time: string;
  latest_short_id: string;
}

export interface OverviewArchive extends OverviewSection {
  configured: boolean;
  repository: string;
  host_id: string;
  snapshots: number;
  latest_time: string;
  hosts: OverviewArchiveHost[];
  hosts_total: number;
  // Null means unknown, never zero: a local deployment has no shared catalog
  // to be behind, and an unreachable one has not been read.
  uncatalogued: number | null;
  pending: number | null;
  catalog_reachable: boolean | null;
}

export interface OverviewHarness {
  harness: string;
  sessions: number;
  titled: number;
}

export interface OverviewCorpus extends OverviewSection {
  sessions: number;
  titled: number;
  harnesses: OverviewHarness[];
  recorded: number;
  derived: number;
  inferred: number;
  refreshed_at: string;
  scan: ScanState;
  pending: number;
}

export interface OverviewStatusCount {
  status: string;
  count: number;
}

export interface OverviewHypothesis {
  id: string;
  run_id: string;
  status: HypothesisStatus | string;
  created_at: string;
  statement: string;
}

export interface OverviewFrontier extends OverviewSection {
  hypotheses: number;
  statuses: OverviewStatusCount[];
  truncated: boolean;
  rows: OverviewHypothesis[];
}

export interface OverviewReviewRow {
  type: string;
  id: string;
  status: ReviewStatus | string;
  enrolled_at: string;
  excerpt: string;
}

export interface OverviewQuestionRow {
  id: string;
  state: string;
  class: string;
  score: number;
  prompt: string;
}

export interface OverviewQuestions extends OverviewSection {
  open: number;
  rows: OverviewQuestionRow[];
}

export interface OverviewReview extends OverviewSection {
  awaiting: number;
  rows: OverviewReviewRow[];
  questions: OverviewQuestions;
  dispositions: OverviewDispositions;
}

// The count of proposed next actions nobody has answered (#87). It is its own
// section because it comes from its own component of the durable file: a
// session can have a review log and be unable to say anything about actions.
export interface OverviewDispositions extends OverviewSection {
  pending: number;
}

// RunAuthority mirrors the authority a run receipt's header carries: an
// operator's command or invitation, a conductor policy, or a serendipity draw.
export interface RunAuthority {
  kind: string;
  ref: string;
}

export interface OverviewRecipe {
  id: string;
  version: number;
}

export interface OverviewRunRow {
  receipt_id: string;
  run_id: string;
  preparation_id: string;
  recorded_at: string;
  sync: string;
  retrievals: number;
  deferred: number;
  failures: number;
  redactions: number;
  hypotheses: number;
  recipes: OverviewRecipe[];
  // Why the run happened. Both fields are empty on a receipt recorded before
  // receipts carried an authority, which is an absence the surface states
  // rather than fills in.
  authority: RunAuthority;
}

export interface OverviewRuns extends OverviewSection {
  total: number;
  rows: OverviewRunRow[];
}

// The nullability is SessionRow's, kept: a session the catalog has not
// described yet has no title and no modification time, and a row that could
// not say so would render an unread session as an untitled one.
export interface OverviewActivityRow {
  harness: string;
  selector: string;
  title: string | null;
  title_provenance: string | null;
  modified: string | null;
}

export interface OverviewActivity extends OverviewSection {
  rows: OverviewActivityRow[];
}

export interface Overview {
  archive: OverviewArchive;
  corpus: OverviewCorpus;
  frontier: OverviewFrontier;
  review: OverviewReview;
  runs: OverviewRuns;
  activity: OverviewActivity;
}

// ---------------------------------------------------------------------------
// The §2.7 bootstrap exchange (decision 34, issue #72).
//
// The launch URL's fragment carries a one-time nonce, which the browser never
// sends to any server. It is read once, erased from the URL, and posted in a
// request body; the server answers with an `HttpOnly; SameSite=Strict` session
// cookie and kills the nonce. From then on the browser authenticates by cookie
// and this module holds no credential at all — there is no value here for an
// XSS hole or a compromised dependency to read out of the page, which is the
// whole reason the exchange exists.
//
// Nothing is stored. The old bootstrap copied the launch token into
// sessionStorage because every request had to attach it as a header; a cookie
// the page cannot read survives a reload without the page remembering
// anything, so a reload re-authenticates with a credential that was never
// reachable from script.
// ---------------------------------------------------------------------------

const BOOTSTRAP_PATH = "/api/bootstrap";

// takeNonce reads the launch nonce and erases the fragment in one step, so the
// value is used exactly once no matter how the page is later navigated.
//
// Honest accounting of the erasure: there are two independent mechanisms, and
// this replaceState is only one of them. "#nonce=…" matches no route, so it
// falls through to App.tsx's catch-all, which is <Navigate to="/"
// replace /> — a replacing redirect that drops the nonce-bearing entry on its
// own. Measured rather than assumed, by disabling each in turn and running the
// browser acceptance: scrub off with the redirect replacing passes; scrub on
// with the redirect pushing passes; with both disabled the history walk fails
// and names the retained "#nonce=" entry.
//
// Both are kept because either alone is a single point of failure, and the
// redirect's `replace` is easy to drop while editing routes. The test defends
// the property, not the mechanism: no reachable history entry retains the
// nonce. A retained entry is now a smaller exposure than it was — the nonce is
// spent and expires — but it is still a credential in a history list.
//
// The fragment is also route state, so this must read it before the router
// mounts. ES module evaluation guarantees that: main.tsx imports App, which
// imports this module, so this runs during import and therefore before
// createRoot().render(). Lazy-loading this module would let the router rewrite
// the fragment away from "#nonce=" first and silently break the bootstrap.
function takeNonce(): string {
  const hash = window.location.hash.replace(/^#/u, "");
  const supplied = new URLSearchParams(hash).get("nonce") ?? "";
  if (!supplied) return "";
  const url = new URL(window.location.href);
  window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}`);
  return supplied;
}

// refusal remembers why the exchange failed, so the first request's bare 401
// can be reported as the thing that actually happened — an expired or
// already-used launch link — instead of as an anonymous authorization failure
// the operator cannot act on.
let refusal = "";

// established is awaited by every request. It resolves rather than rejects on
// failure: a page whose exchange was refused must still render and report the
// refusal, which is what the ordinary unauthorized path already does.
//
// A load with no nonce in the fragment — every reload, and every navigation
// after the first — exchanges nothing and authenticates with the cookie it
// already holds.
const established: Promise<void> = (async () => {
  const nonce = takeNonce();
  if (!nonce) return;
  try {
    const response = await fetch(BOOTSTRAP_PATH, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ nonce }),
      cache: "no-store",
      // Explicit because the whole exchange depends on it: the response's
      // Set-Cookie must be kept, and every later request must carry it.
      credentials: "same-origin",
    });
    if (response.ok) return;
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = (await response.json()) as { error?: unknown };
      if (typeof body.error === "string" && body.error) message = body.error;
    } catch {
      // Keep the status fallback for a malformed or empty refusal.
    }
    refusal = message;
  } catch (error) {
    refusal = safeMessage(error);
  }
})();

// APIFailure is a failed request and the route that asked for it.
//
// The route is carried because a banner is a statement about the page the
// operator is looking at. A request outlives the page that made it -- a listing
// fetch is still in flight when a click navigates away -- and both halves of
// that go wrong without this: an error published before the navigation would
// otherwise survive into the new page, and one published after it would accuse
// a page that loaded perfectly. Attributing the failure to the route it was
// sent from makes both impossible rather than merely unlikely.
export interface APIFailure {
  message: string;
  // The application route, as the hash router names it: "/hypotheses". Captured
  // when the request was sent, never when it failed.
  route: string;
}

const errorListeners = new Set<(failure: APIFailure | null) => void>();
let currentError: APIFailure | null = null;

function safeMessage(value: unknown): string {
  const message = value instanceof Error ? value.message : String(value);
  return message.replace(/[\u0000-\u001f\u007f-\u009f]/gu, "").trim() || "Request failed";
}

function publishError(value: unknown, route: string): void {
  currentError = { message: safeMessage(value), route };
  for (const listener of errorListeners) listener(currentError);
}

export function subscribeAPIErrors(listener: (failure: APIFailure | null) => void): () => void {
  errorListeners.add(listener);
  listener(currentError);
  return () => errorListeners.delete(listener);
}

export function dismissAPIError(): void {
  currentError = null;
  for (const listener of errorListeners) listener(null);
}

export class APIError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "APIError";
    this.status = status;
  }
}

// Every request is bounded so a stalled server surfaces an error instead of an
// interface that spins forever.
const REQUEST_TIMEOUT_MS = 20_000;

// send is the one transport path every API call shares: the session cookie,
// cache bypass, bounded wait, error mapping, and error publication. Callers
// differ only in how they read a successful body.
//
// Every request waits for the bootstrap exchange. A request that overtook it
// would be sent before the session cookie existed and refused, which on a
// first load is every request the dashboard makes.
async function send<T>(
  path: string,
  init: RequestInit,
  read: (response: Response) => Promise<T>,
): Promise<T> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  // The route is read here, before the first await, so it is the route that
  // asked rather than whichever one the operator reached while waiting. The
  // search and nested-fragment parts are dropped so it compares equal to the
  // router's own pathname.
  const route = window.location.hash.replace(/^#/u, "").replace(/[?#].*$/u, "");
  try {
    await established;
    const response = await fetch(path, {
      ...init,
      cache: "no-store",
      credentials: "same-origin",
      signal: controller.signal,
    });
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const body = (await response.json()) as { error?: unknown };
        if (typeof body.error === "string" && body.error) message = body.error;
      } catch {
        // Keep the status fallback for malformed or empty error responses.
      }
      // A 401 after a refused exchange has one cause, and the server already
      // named it. Reporting "unauthorized" instead would hide an expired or
      // already-used launch link behind a message the operator cannot act on.
      if (response.status === 401 && refusal) message = refusal;
      throw new APIError(response.status, message);
    }
    return await read(response);
  } catch (error) {
    const failure = controller.signal.aborted
      ? new APIError(408, `${path} did not respond within ${REQUEST_TIMEOUT_MS / 1000}s`)
      : error;
    publishError(failure, route);
    throw failure;
  } finally {
    window.clearTimeout(timer);
  }
}

function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  return send(path, init, async (response) => (await response.json()) as T);
}

function postJSON<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

function query(values: Record<string, string | number>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) params.set(key, String(value));
  return params.toString();
}

export function getVersion(): Promise<VersionInfo> {
  return request<VersionInfo>("/api/version");
}

export function getState(): Promise<StateInfo> {
  return request<StateInfo>("/api/state");
}

export function getSessions(): Promise<SessionsResponse> {
  return request<SessionsResponse>("/api/sessions");
}

export function getScan(): Promise<ScanState> {
  return request<ScanState>("/api/scan");
}

export function refreshSessions(): Promise<ScanState> {
  return request<ScanState>("/api/sessions/refresh", { method: "POST" });
}

export function getSession(selector: string): Promise<SessionDetail> {
  return request<SessionDetail>(`/api/session?${query({ selector })}`);
}

export function getTranscript(
  selector: string,
  offset = 0,
  limit = 200,
): Promise<TranscriptResponse> {
  return request<TranscriptResponse>(
    `/api/transcript?${query({ selector, offset, limit })}`,
  );
}

export function getArchiveStatus(): Promise<ArchiveStatus> {
  return request<ArchiveStatus>("/api/archive/status");
}

// getArchiveSessions reads one host's archived session listing. It goes over
// the network to the repository, so it is slower than every other read on the
// Sessions page and is never polled.
export function getArchiveSessions(
  host: string,
  snapshot?: string,
): Promise<ArchiveSessionsResponse> {
  const values: Record<string, string> = { host };
  if (snapshot?.trim()) values.snapshot = snapshot.trim();
  return request<ArchiveSessionsResponse>(`/api/archive/sessions?${query(values)}`);
}

export function verifyArchive(deep: boolean): Promise<VerifyResult> {
  return request<VerifyResult>(`/api/archive/verify?${query({ deep: deep ? 1 : 0 })}`, {
    method: "POST",
  });
}

// fetchSession materializes one session's file closure locally. host is
// required for a session this machine never had: without it the selector is
// resolved against local source files, which by definition do not hold it.
export function fetchSession(
  selector: string,
  snapshot?: string,
  host?: string,
): Promise<FetchResult> {
  const values: Record<string, string> = { selector };
  if (snapshot?.trim()) values.snapshot = snapshot.trim();
  if (host?.trim()) values.host = host.trim();
  return request<FetchResult>(`/api/fetch?${query(values)}`, { method: "POST" });
}

// lockServer is one-way. The server revokes the session as it answers, so
// there is no second attempt to make and no way to re-read the confirmation:
// whatever this resolves or rejects with is the last thing this page learns.
export function lockServer(): Promise<LockResult> {
  return request<LockResult>("/api/lock", { method: "POST" });
}

// ---------------------------------------------------------------------------
// Phase B fetchers. Every mutation below goes through the same authenticated
// transport as every read: the browser renders what the services return and
// carries no authority the CLI would not have (§2.7, §14).
// ---------------------------------------------------------------------------

export function getAnalysisState(): Promise<AnalysisState> {
  return request<AnalysisState>("/api/analysis/state");
}

// getHypotheses reads this machine's frontier, and with `fleet` the other hosts'
// committed candidates after it. The flag is opt-in on the wire for the reason
// the server states: "what is on my frontier" and "what has the deployment
// produced" are two questions, and a list that silently became the second would
// make an operator's own backlog look like someone else's work.
export function getHypotheses(
  filter: { status?: string; limit?: number; offset?: number; fleet?: boolean } = {},
): Promise<HypothesesResponse> {
  const values: Record<string, string | number> = {};
  if (filter.status) values.status = filter.status;
  if (filter.limit !== undefined) values.limit = filter.limit;
  if (filter.offset !== undefined) values.offset = filter.offset;
  if (filter.fleet) values.fleet = "1";
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<HypothesesResponse>(`/api/hypotheses${suffix}`);
}

export function getHypothesis(id: string): Promise<HypothesisDetail> {
  return request<HypothesisDetail>(`/api/hypothesis?${query({ id })}`);
}

export function getFindings(filter: { fleet?: boolean } = {}): Promise<FindingsResponse> {
  return request<FindingsResponse>(`/api/findings${filter.fleet ? "?fleet=1" : ""}`);
}

export function getFinding(id: string): Promise<FindingDetail> {
  return request<FindingDetail>(`/api/finding?${query({ id })}`);
}

export function getReviewQueue(
  filter: { type?: string; status?: string; fleet?: boolean } = {},
): Promise<ReviewQueueResponse> {
  const values: Record<string, string> = {};
  if (filter.type) values.type = filter.type;
  if (filter.status) values.status = filter.status;
  if (filter.fleet) values.fleet = "1";
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<ReviewQueueResponse>(`/api/review/queue${suffix}`);
}

// ---------------------------------------------------------------------------
// The fleet read (issue #109 item 4).
//
// Only identifiers travel in these URLs -- host ids, record kinds, a page --
// and never a word of a record. Record content in a query string would put
// another machine's analysis into this machine's browser history and into every
// request log between them, which is the channel the leak acceptance guards.
// ---------------------------------------------------------------------------

export function getFleetRecords(filter: FleetRecordFilter = {}): Promise<FleetRecordsResponse> {
  const params = new URLSearchParams();
  // Repeatable, because the server's host and kind filters are any-of. A comma
  // list would make a host id containing a comma unaddressable.
  for (const host of filter.hosts ?? []) params.append("host", host);
  for (const kind of filter.kinds ?? []) params.append("kind", kind);
  if (filter.pending) params.set("pending", "1");
  if (filter.limit !== undefined) params.set("limit", String(filter.limit));
  if (filter.offset !== undefined) params.set("offset", String(filter.offset));
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<FleetRecordsResponse>(`/api/fleet/records${suffix}`);
}

// getFleetHosts reads the host filter's vocabulary. It takes no host of its
// own: a vocabulary narrowed by the current selection could not offer the
// machines the operator is trying to reach.
export function getFleetHosts(
  filter: { kinds?: string[]; pending?: boolean } = {},
): Promise<FleetHostsResponse> {
  const params = new URLSearchParams();
  for (const kind of filter.kinds ?? []) params.append("kind", kind);
  if (filter.pending) params.set("pending", "1");
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<FleetHostsResponse>(`/api/fleet/hosts${suffix}`);
}

export function decideReview(decision: DecideRequest): Promise<DecideResult> {
  return postJSON<DecideResult>("/api/review/decide", decision);
}

export function addReviewContext(text: string): Promise<{ id: string }> {
  return postJSON<{ id: string }>("/api/review/context", { text });
}

export function getReviewHistory(type: string, id: string): Promise<ReviewHistory> {
  return request<ReviewHistory>(`/api/review/history?${query({ type, id })}`);
}

// The export document is fetched rather than navigated to, so the record's
// content stays out of the URL, browser history, and server request logs.
export function getExportJSON(type: string, id: string): Promise<unknown> {
  return request<unknown>(`/api/export?${query({ type, id, format: "json" })}`);
}

export function getExportMarkdown(type: string, id: string): Promise<string> {
  return send(
    `/api/export?${query({ type, id, format: "markdown" })}`,
    {},
    (response) => response.text(),
  );
}

export function getRealityInbox(): Promise<RealityInbox> {
  return request<RealityInbox>("/api/reality/inbox");
}

export function getRealityEntity(id: string): Promise<EntityDetail> {
  return request<EntityDetail>(`/api/reality/entity?${query({ id })}`);
}

export function answerQuestion(
  questionId: string,
  text: string,
  outcome: string,
): Promise<AnswerResult> {
  return postJSON<AnswerResult>("/api/reality/answer", { questionId, text, outcome });
}

export function acceptPlan(planId: string): Promise<PlanAcceptResult> {
  return postJSON<PlanAcceptResult>("/api/reality/plan/accept", { planId });
}

export function searchCorpus(
  params: { q: string; harness?: string; kind?: string; limit?: number },
): Promise<SearchResponse> {
  const values: Record<string, string | number> = { q: params.q };
  if (params.harness) values.harness = params.harness;
  if (params.kind) values.kind = params.kind;
  if (params.limit !== undefined) values.limit = params.limit;
  return request<SearchResponse>(`/api/search?${query(values)}`);
}

// getOverview reads the dashboard's whole snapshot in one request. It takes no
// paging: a panel shows a fixed few rows and links to the page that lists the
// rest, so the window belongs to the server rather than to the caller.
export function getOverview(): Promise<Overview> {
  return request<Overview>("/api/overview");
}

// ---------------------------------------------------------------------------
// Record actions (issue #87)
//
// A record's revision chain, the next actions proposed against it, and the
// three things an operator may authorize from a record page. Two rules from the
// issue are visible in these shapes rather than only in the pages that render
// them.
//
// Accepting authorizes and publishes nothing. DecideDispositionResult carries a
// `published` sentence from the server for exactly that reason: a caller
// reading this module is the reader most likely to assume an accepted
// draft-issue was filed.
//
// Every mutation confirms the wording the operator was shown. `headId` is the
// chain head the page rendered against, and a head that moved since is a 409
// with an explanation rather than a write — so these three request types have
// no optional field and no default for it.

export interface Revision {
  id: string;
  record: RecordRef;
  root_id: string;
  supersedes_id?: string;
  sequence: number;
  actor: Actor;
  recorded_at: string;
  // Why this revision replaced the one before it. Absent on a chain's first
  // entry: an original supersedes nothing and has no reason to give.
  reason?: string;
  head: boolean;
}

export interface RecordRef {
  type: string;
  id: string;
}

export interface RevisionChain {
  record: RecordRef;
  head_id: string;
  revisions: Revision[];
}

export interface DispositionAnchor {
  workspace: string;
  remote: string;
  url: string;
  branch?: string;
}

export interface DispositionRuling {
  id: string;
  sequence: number;
  ruling: string;
  by: string;
  recorded_at: string;
  note?: string;
}

export interface ProposedAction {
  id: string;
  record: RecordRef;
  kind: string;
  status: string;
  proposed_by: Actor;
  ref?: string;
  created_at: string;
  summary: string;
  rationale?: string;
  anchor?: DispositionAnchor;
  ledger: DispositionRuling[];
  // The issue text a draft-issue renders to, absent for every other kind. It
  // is text and it is rendered as text: nothing here opens a link, and the
  // draft is filed by the operator or by nobody.
  draft?: string;
}

export interface RecordInvitation {
  id: string;
  record: RecordRef;
  by: string;
  created_at: string;
  consumed_by?: string;
  consumed_at?: string;
  open: boolean;
}

export interface RecordDispositions {
  record: RecordRef;
  head_id: string;
  dispositions: ProposedAction[];
  invitations: RecordInvitation[];
}

export interface DecideDispositionResult {
  entry: DispositionRuling;
  status: string;
  published: string;
}

export interface InviteResult {
  invitation: RecordInvitation;
  instruction: string;
}

export interface ReviveResult {
  record: RecordRef;
  event: StatusEvent;
}

export function getRecordRevisions(type: string, id: string): Promise<RevisionChain> {
  return request<RevisionChain>(`/api/record/revisions?${query({ type, id })}`);
}

export function getRecordDispositions(type: string, id: string): Promise<RecordDispositions> {
  return request<RecordDispositions>(`/api/record/dispositions?${query({ type, id })}`);
}

export function decideDisposition(
  dispositionId: string,
  ruling: "accepted" | "declined",
  headId: string,
  note = "",
): Promise<DecideDispositionResult> {
  return postJSON<DecideDispositionResult>("/api/record/disposition/decide", {
    dispositionId,
    ruling,
    note,
    headId,
  });
}

// inviteRecord takes no text, and the absence is the point rather than an
// oversight: #87's nudge says a record deserves attention and deliberately does
// not say what to do about it. The route refuses an unknown field, so a caller
// that added one here would get a 400 rather than a silently dropped
// instruction.
export function inviteRecord(record: RecordRef, headId: string): Promise<InviteResult> {
  return postJSON<InviteResult>("/api/record/invite", { record, headId });
}

export function reviveRecord(
  record: RecordRef,
  reason: string,
  headId: string,
  status = "",
): Promise<ReviveResult> {
  return postJSON<ReviveResult>("/api/record/revive", { record, reason, status, headId });
}
