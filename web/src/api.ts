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

// ArchiveSessionRow is what GET /api/archive/sessions answers with: the four
// fields a snapshot's file listing actually carries, and nothing else. Reading
// a snapshot's listing downloads no transcript bytes, so title, workspace,
// modified time and continuation grade are not merely null there — they are
// unobserved, and this type cannot express them at all.
//
// No page in this build browses an archive's session listing. The type stays
// because the route stays served and the mock server answers it against the
// same shape the Go DTO sends; recovering a session out of a snapshot is
// `babel sessions fetch`'s job.
export interface ArchiveSessionRow {
  harness: string;
  source_id: string;
  selector: string;
  size: number;
  // Whether the session's files have already been fetched out of the snapshot,
  // and where they landed.
  fetched: boolean;
  fetched_path?: string;
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
// Catalog attribution, as the wire carries it.
//
// The catalog is one body of work and the interface reads it as one: no page
// renders which instance produced a record, offers it as a filter, or sorts by
// it. Exactly one attribution field survives here — `local_host`, which says
// whether the row arrived already carrying this instance's own derivations. A
// row the catalog merged has the record's own fields and none of the review
// history derived beside it, and a renderer has to show that absence rather
// than a decided-nothing.
//
// The machine's name and id travel on the wire beside it and are deliberately
// absent from these types. A field no page may render is a field a page
// eventually renders, and which computer produced a candidate answers no
// question a reader of it has.
//
// The one place a machine is a legitimate subject is the archive, where a
// snapshot is a backup *of* a machine. That vocabulary lives in ArchiveHost and
// nowhere near an analytical record.
// ---------------------------------------------------------------------------

// SyncNotice is the degraded marker a listing envelope carries when the shared
// catalog could not answer part of a read. The rows still render, which is why
// no page turns it into a banner: publication state is plumbing, and a record
// either renders or it does not.
export interface SyncNotice {
  sync_degraded?: boolean;
  sync_detail?: string;
}

export interface FleetMark {
  // Whether this instance holds the record itself rather than having read it
  // out of the shared catalog. It is never rendered as a place: it is read to
  // decide whether the derived state beside a row exists to be shown at all.
  local_host?: boolean;
  // The publication state the server reports, kept as the string it sends. No
  // page renders it — a record either shows or it does not, and where it has
  // replicated to is plumbing — so the interface holds no vocabulary of its
  // own for the values.
  sync?: string;
  committed_at?: string;
  unopened?: string;
}

// One machine the deployment has registered, and the only host vocabulary the
// browser still reads: the fleet diagnostic joins it to presence rows, whose
// subject is the machines themselves. `attributed: false` is the group whose
// origin instances registered before hosts were recorded — a name for it,
// rather than a set of rows with no name at all.
//
// It carries no per-machine record counts. A count of records per host is the
// corpus organised by machine, which is the one thing this vocabulary must not
// become.
export interface FleetHost {
  host: string;
  host_id: string;
  attributed: boolean;
}

export interface FleetHostsResponse {
  // False means no shared backend is configured, which is a fact about the
  // deployment rather than a failure. A backend that exists and did not answer
  // arrives as an APIError instead, and the two read differently on screen.
  configured: boolean;
  // The host id this instance registered, absent when it has registered none.
  local_host?: string;
  hosts: FleetHost[];
}

// ---------------------------------------------------------------------------
// Fleet presence (issue #118).
//
// The one read on this surface whose subject is neither durable nor local: what
// every machine in the deployment says it is running right now. Three rules from
// the issue live in these shapes rather than only in the page that renders them.
//
// A row is a claim, not an observation. `state` is what the run last said about
// itself, and it says nothing about now — so `freshness` and
// `heartbeat_age_seconds` travel beside it, and a renderer must use both. The
// classification alone cannot produce "last seen 4m ago", and the age alone
// cannot say which threshold was crossed.
//
// The classification is the server's. `freshness` is internal/presence's own
// word, and the thresholds it was decided by are on the envelope, so nothing
// here recomputes it: a page that compared `heartbeat_age_seconds` against a
// constant of its own would eventually contradict the badge beside it.
//
// A row identity is `id`, never `run_id`. One conductor cycle announces twice —
// the loop's own row and the run inside it, both under the same run id — and
// they are two facts, because the loop can be alive while the run it started is
// not. Keying a list on `run_id` would hide exactly that.
// ---------------------------------------------------------------------------

export type PresenceKind = "conductor" | "explore";

export type PresenceState = "running" | "finished" | "failed" | "cancelled";

// The four classifications, and none of them is a claim about a process.
// "lost" means nothing has been heard for a long time; it deliberately does not
// mean dead, and the interface never renders it as one.
export type PresenceFreshness = "fresh" | "stale" | "lost" | "finished";

export interface PresenceRow {
  id: string;
  // The machine's opaque host id, the same value the archive and the shared
  // catalog use. It is not a display name: presence stores no second copy of
  // host identity, so a label comes from joining the host vocabulary
  // GET /api/fleet/hosts already serves.
  host: string;
  local_host: boolean;
  kind: PresenceKind;
  run_id: string;
  // Absent when the run announced none. A conductor cycle that has not
  // resolved an assignment has no recipe, and rendering an empty string as a
  // value would invent one.
  recipe?: string;
  preparation_id?: string;
  authority: RunAuthority;
  state: PresenceState;
  started_at?: string;
  heartbeat_at?: string;
  // Absent exactly while the run is still running.
  finished_at?: string;
  heartbeat_age_seconds: number;
  freshness: PresenceFreshness;
  // The record the run committed when it finalized, absent until then.
  receipt_record_id?: string;
}

export interface PresenceResponse {
  // False whenever the rows are not what the fleet announced. `configured`
  // then separates the reasons: local mode has no presence table at all, and a
  // configured machine whose catalog could not be read has one it cannot see.
  // `unavailable` is the server's own sentence for whichever happened.
  available: boolean;
  configured: boolean;
  unavailable?: string;
  rows: PresenceRow[];
  // The number of rows whose own state is "running". A count of claims, never
  // of live processes, and the caption says so.
  running: number;
  // The thresholds the server classified by, so the page's prose and the
  // server's badge cannot disagree.
  stale_after_seconds: number;
  lost_after_seconds: number;
  // How far back the read reaches. Without it an empty list is ambiguous
  // between an idle deployment and a window that ended.
  retention_seconds: number;
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
  // The receipt's publication state, on FleetMark's terms: carried, never
  // rendered.
  sync: string;
  counts: RunCounts;
  // Why the run happened, as its receipt recorded it. Empty on a receipt
  // written before receipts carried one.
  authority: RunAuthority;
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

// A candidate as a listing shows it. A row the catalog merged carries the
// statement and no review_status and no observation count at all — both are
// derived beside the record and travel separately, and a zero rendered as a
// fact would say the candidate rests on no evidence.
export interface HypothesisSummary extends FleetMark {
  id: string;
  run_id: string;
  ancestor_id?: string;
  created_at: string;
  // Empty on a merged row this instance could not open: a candidate's status
  // lives in an append-only history beside the record that holds it, so a
  // record that would not open has none to report and the server sends none
  // rather than a plausible one.
  status: HypothesisStatus | "";
  statement: string;
  provisional_labels?: string[];
  observations: number;
  // Additive server field: the derived §4.7 review status beside the
  // exploration status. Optional so the mock stays minimal.
  review_status?: ReviewStatus;
}

// `total` is the enumerated count the server paged over, which is not the
// number of rows in `items` once merged rows follow them.
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

// A consolidation as a listing shows it, on HypothesisSummary's terms: a merged
// row carries the title and none of the counts derived beside the record.
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

// ProposalRow is one proposal as the listing shows it: the identifiers, the
// wording a reader scans by, and the two model gradings that decide whether a
// suggestion is worth opening. The server flattens them out of the stored
// payload, so a row costs no per-record request.
//
// A merged row carries only the title — the catalog's bounded summary line —
// and none of the rest, for FleetMark's reason.
export interface ProposalRow extends FleetMark {
  id: string;
  run_id: string;
  created_at: string;
  title: string;
  problem: string;
  outcome: string;
  impact: string;
  classification: string;
  review_status?: ReviewStatus;
}

export interface ProposalsResponse extends SyncNotice {
  items: ProposalRow[];
  total: number;
}

// TriageAdvice is what Babel said about a proposal before anybody ruled on it:
// where to read it in the pile, which records say the same thing, the case
// against acting on it, and the id of a proposal offered instead.
//
// None of it is a decision, and nothing here can become one. A rank is a
// reading order rather than a score, a cluster is an invitation to compare
// rather than a duplicate ruling, and the alternative is a second record in
// the queue rather than an edit of the first — the original keeps its wording
// and its place. The disposition stays the operator's, and the only surface
// that records one is the review page's own decide action.
export interface TriageAdvice {
  id: string;
  // The proposal the advice is about, which on an alternative's page is the
  // record it was offered instead of.
  proposal_id: string;
  alternative_id?: string;
  cluster: string[];
  run_id: string;
  recorded_at: string;
  rank: number;
  cohort: number;
  ranking?: string;
  counter_argument: string;
}

// ProposalDetail is one proposal whole: the row's own fields, the records it
// was written against, and the stored payload verbatim. `form` is #114's
// provenance — `consolidated` for a finding-backed artifact, `candidate` for a
// remedy resting only on the claim it addresses — and it is served rather than
// inferred, because a want rendered with a consolidation's authority is the
// failure the split exists to prevent.
export interface ProposalDetail extends ProposalRow {
  ancestor_id?: string;
  schema_version: number;
  finding_ids: string[];
  hypothesis_ids: string[];
  form: string;
  payload: ProposalPayload;
  // Absent when no pass has read this proposal. An untriaged record is not a
  // record with empty advice, so there is no block to render for one.
  triage?: TriageAdvice[];
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

// One review-inbox row. A merged row carries an empty status and zero counts
// because the append-only decision history is derived beside the record and
// does not travel with it; `local_host` is how a renderer tells the two apart
// and shows an absence rather than a decided-nothing claim.
export interface QueueItem extends FleetMark {
  subject: ReviewSubject;
  enrolled_at: string;
  status: ReviewStatus;
  decisions: number;
  last_decided_at?: string;
  refinements: number;
  // The subject's statement/title/claim, so the queue is readable.
  excerpt: string;
  // How many typed references (#113) leave this record and arrive at it.
  // Absent means not counted — this build has no reference graph, or the graph
  // could not answer for this row — which is a different claim from a counted
  // zero, so the chip is rendered only when the field is present.
  citations?: { cites: number; cited_by: number };
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

export function verifyArchive(deep: boolean): Promise<VerifyResult> {
  return request<VerifyResult>(`/api/archive/verify?${query({ deep: deep ? 1 : 0 })}`, {
    method: "POST",
  });
}

// fetchSession materializes one session's file closure out of a snapshot. The
// selector is the catalog's own, and the snapshot is optional: without one the
// newest snapshot holding the session is read.
export function fetchSession(
  selector: string,
  snapshot?: string,
): Promise<FetchResult> {
  const values: Record<string, string> = { selector };
  if (snapshot?.trim()) values.snapshot = snapshot.trim();
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

// getHypotheses reads the frontier. The read is deployment-wide: the catalog is
// one body of work, so there is no scope to ask about and no scope parameter to
// send.
export function getHypotheses(
  filter: { status?: string; limit?: number; offset?: number } = {},
): Promise<HypothesesResponse> {
  const values: Record<string, string | number> = {};
  if (filter.status) values.status = filter.status;
  if (filter.limit !== undefined) values.limit = filter.limit;
  if (filter.offset !== undefined) values.offset = filter.offset;
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<HypothesesResponse>(`/api/hypotheses${suffix}`);
}

export function getHypothesis(id: string): Promise<HypothesisDetail> {
  return request<HypothesisDetail>(`/api/hypothesis?${query({ id })}`);
}

export function getFindings(): Promise<FindingsResponse> {
  return request<FindingsResponse>("/api/findings");
}

export function getFinding(id: string): Promise<FindingDetail> {
  return request<FindingDetail>(`/api/finding?${query({ id })}`);
}

export function getProposals(
  filter: { limit?: number; offset?: number } = {},
): Promise<ProposalsResponse> {
  const values: Record<string, string | number> = {};
  if (filter.limit !== undefined) values.limit = filter.limit;
  if (filter.offset !== undefined) values.offset = filter.offset;
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<ProposalsResponse>(`/api/proposals${suffix}`);
}

// getProposal reads one proposal whole. The identifier travels in the path
// rather than a query, which is the shape the listing's rows link to.
export function getProposal(id: string): Promise<ProposalDetail> {
  return request<ProposalDetail>(`/api/proposals/${encodeURIComponent(id)}`);
}

export function getReviewQueue(
  filter: { type?: string; status?: string } = {},
): Promise<ReviewQueueResponse> {
  const values: Record<string, string> = {};
  if (filter.type) values.type = filter.type;
  if (filter.status) values.status = filter.status;
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<ReviewQueueResponse>(`/api/review/queue${suffix}`);
}

// ---------------------------------------------------------------------------
// The fleet read (issue #109 item 4).
//
// Only identifiers travel in these URLs -- record kinds, a page -- and never a
// word of a record. Record content in a query string would put one
// instance's analysis into another's browser history and into every request log
// between them, which is the channel the leak acceptance guards.
// ---------------------------------------------------------------------------

// getFleetHosts reads the deployment's host vocabulary: the labels the fleet
// diagnostic joins to presence rows. It takes no host of its own, because a
// vocabulary narrowed by the current selection could not name the machine the
// operator is trying to reach.
export function getFleetHosts(
  filter: { kinds?: string[]; pending?: boolean } = {},
): Promise<FleetHostsResponse> {
  const params = new URLSearchParams();
  for (const kind of filter.kinds ?? []) params.append("kind", kind);
  if (filter.pending) params.set("pending", "1");
  const suffix = params.size ? `?${params.toString()}` : "";
  return request<FleetHostsResponse>(`/api/fleet/hosts${suffix}`);
}

// getPresence reads what the deployment says it is running.
//
// It takes no filter at all, and the absence is the point rather than an
// omission. The whole answer is bounded already — internal/presence returns only
// rows inside its retention window, capped — and the question "what is happening
// on my fleet" has no narrowing that would not risk hiding the row the operator
// opened the page for.
export function getPresence(): Promise<PresenceResponse> {
  return request<PresenceResponse>("/api/fleet/presence");
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

// Issue #113's typed reference graph, as a record surface reads it.
//
// ReferenceEndpoint carries an identity and never a destination. The route that
// opens a record is derived from `kind` and `route_id ?? id` by this client's own
// route table, so nothing a record's text contains can become an href: an edge
// note is prose a model or an operator wrote, and a link built from it would make
// the citation graph an injection surface.
export interface ReferenceEndpoint {
  kind: string;
  id: string;
  // The identifier this app's page for the record is reached by, present only
  // when it differs from `id`. It exists for sessions: an edge records the
  // deployment-scoped durable key, and the session page routes on the local
  // selector.
  route_id?: string;
  // A short human identity for the record, when the server resolved one.
  // Untrusted content.
  label?: string;
  // inert marks an endpoint that must render as identified text rather than as
  // a link, and reason says why: a namespace with no page here, a service this
  // session did not wire, a record this instance does not hold, or a check that
  // could not be completed. The reason is rendered rather than replaced with a
  // generic message, on UnopenedNote's terms.
  inert?: boolean;
  reason?: string;
}

export interface ReferenceEdge {
  id: string;
  kind: string;
  // The far endpoint only: the cited record under `cites`, the citing one under
  // `cited_by`.
  other: ReferenceEndpoint;
  actor: { kind: string; id: string };
  note?: string;
  created_at: string;
}

export interface ReferenceKindCount {
  kind: string;
  count: number;
}

// One half of a record's citations. `counts` is over the whole direction while
// `edges` is the page cut from it, so a chip row does not shrink as a reader
// pages through the rows beneath it.
export interface ReferenceDirection {
  edges: ReferenceEdge[];
  counts: ReferenceKindCount[];
  total: number;
  limit: number;
  offset: number;
}

export interface RecordReferences {
  record: ReferenceEndpoint;
  // false on a build with no reference store. The section renders nothing at
  // all in that case: an absent feature is not a failed panel, which is why the
  // route answers 200 and says so rather than refusing.
  available: boolean;
  cites: ReferenceDirection;
  cited_by: ReferenceDirection;
}

export function getRecordRevisions(type: string, id: string): Promise<RevisionChain> {
  return request<RevisionChain>(`/api/record/revisions?${query({ type, id })}`);
}

export function getRecordDispositions(type: string, id: string): Promise<RecordDispositions> {
  return request<RecordDispositions>(`/api/record/dispositions?${query({ type, id })}`);
}

// getRecordLinks reads one record's citations, both directions in one request:
// they are one section of one page, and two calls would let it render half an
// answer. A session is named by its selector here, the identity every route and
// command already uses; the server derives the durable key an edge records.
export function getRecordLinks(type: string, id: string): Promise<RecordReferences> {
  return request<RecordReferences>(`/api/record/links?${query({ type, id })}`);
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

// ---------------------------------------------------------------------------
// Operator steering (issue #115).
//
// A complaint is steering pressure, never a ticket. These shapes carry no
// status, no closure marker, no assignee, and no priority — not as an
// omission but as the product rule: a complaint that acquired any of them
// would make Babel a work tracker, and GitHub already is one. "Was this
// addressed?" is answered by the `cited_by` direction of #113's reference
// graph and by nothing in these types.
// ---------------------------------------------------------------------------

// One head-of-chain row as GET /api/complaints lists it, newest first.
// `summary` is the bounded one-liner, never the full text: the operator's
// whole wording is read by opening the record, where it renders inside a
// quoted frame (§3).
export interface ComplaintSummary {
  id: string;
  root_id: string;
  // Absent for a chain's first wording, which supersedes nothing.
  supersedes?: string;
  sequence: number;
  by: string;
  summary: string;
  redacted: boolean;
  at: string;
  // Absent entirely on a build with no reference graph. Absent means nobody
  // counted, which is a different claim from a counted zero — the rule
  // CitationCount already renders by — so a listing shows an em dash there,
  // never a 0.
  citations?: { cites: number; cited_by: number };
}

export interface ComplaintsResponse {
  items: ComplaintSummary[];
  total: number;
}

// One wording of a complaint, whole. `text` is the operator's own verbatim
// bytes, newlines preserved: untrusted content, rendered as text inside a
// quoted frame, never markup and never a link destination.
export interface ComplaintWording {
  id: string;
  root_id: string;
  supersedes?: string;
  sequence: number;
  by: string;
  text: string;
  redacted: boolean;
  at: string;
  head: boolean;
}

// One entry of a chain's revision listing. It carries the bounded `summary`
// and never the full text: the full text of any wording is read by opening
// that wording's own id, where it is a whole record rather than a row.
export interface ComplaintRevision {
  id: string;
  supersedes?: string;
  sequence: number;
  by: string;
  summary: string;
  redacted: boolean;
  at: string;
  head: boolean;
}

export interface ComplaintDetail {
  complaint: ComplaintWording;
  head_id: string;
  // The whole chain, oldest first, unfiltered: amending appends, and an
  // earlier wording stays readable at its own identifier (§4.7).
  revisions: ComplaintRevision[];
}

// One record capture-time adjacency reports. It deliberately carries no
// score, rank, or relevance field: §5.4's rule is that retrieval rank never
// becomes evidence strength, and a number here would grade "compare these"
// into "these are the same". The list is a prompt to compare, never a claim
// of sameness.
export interface AdjacentOutput {
  kind: string;
  id: string;
  summary: string;
}

// What POST /api/complaint/tell answers. There is no acknowledgement, status,
// or "filed" field to read out of this, because capturing did none of that:
// the capture opened nothing, assigned nothing and scheduled nothing, and a
// field that implied otherwise would be the response promising work Babel
// never took on. The one sentence about what happens next is `steering`, the
// server's fixed wording, rendered verbatim rather than paraphrased.
export interface CaptureResult {
  complaint: ComplaintWording;
  // Always an array, never null, max 8 rows.
  adjacent: AdjacentOutput[];
  // Present only when the adjacency pass could not run; a pass that simply
  // matched nothing says nothing. Never an error's own text (§9).
  adjacency_note?: string;
  steering: string;
}

export function getComplaints(
  page: { limit?: number; offset?: number } = {},
): Promise<ComplaintsResponse> {
  const values: Record<string, string | number> = {};
  if (page.limit !== undefined) values.limit = page.limit;
  if (page.offset !== undefined) values.offset = page.offset;
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<ComplaintsResponse>(`/api/complaints${suffix}`);
}

// getComplaint accepts any wording's id and answers that wording with its
// whole chain, so a superseded wording is as openable as the head.
export function getComplaint(id: string): Promise<ComplaintDetail> {
  return request<ComplaintDetail>(`/api/complaint?${query({ id })}`);
}

// tellComplaint sends the operator's text exactly as typed: the record is the
// verbatim wording, and a client-side trim or rewrite would already be an
// edit. The route refuses unknown fields, so nothing else can ride along.
export function tellComplaint(text: string): Promise<CaptureResult> {
  return postJSON<CaptureResult>("/api/complaint/tell", { text });
}


// ---------------------------------------------------------------------------
// Focus policy (SPEC.md §4.8).
//
// What Babel is allowed to spend on a subject, and the operator's own
// statement of it. Three rules from the spec are visible in these shapes
// rather than only in the page that renders them.
//
// A policy value is not an allowance. `policy` is the analysis-policy fact the
// operator states; `allowance` is what the installed rule set version maps it
// to. They are two fields because §4.8's whole point is that no fact value
// implies an expenditure — the mapping is an explicit versioned artifact — so a
// client that rendered one as the other would be asserting a mapping the
// stored policy might not make.
//
// `means` always travels with an allowance. It is the server's sentence about
// what is actually withheld, and it is rendered verbatim: an operator choosing
// a policy and an operator reading a deferral have to be told the same thing,
// and a paraphrase here would drift from the one the ledger uses.
//
// Nothing is deleted. Reversing a policy is `supersedeFocusPolicy`, which
// writes a later revision and leaves the earlier one readable, so there is no
// delete call to make and no field whose absence means "removed".
// ---------------------------------------------------------------------------

export interface FocusCondition {
  predicate: string;
  equals: string;
}

// One rule of an installed version, in the version's own order: rules are
// first-match-wins, so a client that sorted them would be showing a policy
// that decides differently from the one stored.
export interface FocusRule {
  name: string;
  when: FocusCondition[];
  allows: string;
  because: string;
  means: string;
}

export interface FocusPolicy {
  version: number;
  default: string;
  default_means: string;
  note?: string;
  installed_at: string;
  rules: FocusRule[];
}

// One policy an operator may state, and what stating it would withhold under
// the installed version. `withholds` is false for the one choice that withholds
// nothing, which is what lets "lift this" render as what it is rather than as a
// fourth restriction.
export interface FocusChoice {
  policy: string;
  allowance: string;
  rule?: string;
  means: string;
  withholds: boolean;
  conditional?: boolean;
}

// The subject a rule is about, by the names the operator actually uses for it.
// The aliases travel because the canonical display name is frequently not the
// word he would have typed.
export interface FocusSubject {
  entity_id: string;
  kind: string;
  display_name: string;
  aliases: string[];
}

export interface FocusRuleInForce {
  subject: FocusSubject;
  allowance: string;
  means: string;
  withholds: boolean;
  rule?: string;
  because: string;
  policy: string;
  fact: FactView;
  contested: boolean;
  contested_fact_ids?: string[];
}

// A policy the operator stated that no installed version interprets. It is its
// own shape rather than a rule with an empty allowance, because it is a
// different state: the intent is recorded and nothing is withheld, which is
// exactly the "I said stop and nothing stopped" case the page has to explain.
export interface FocusStated {
  subject: FocusSubject;
  policy: string;
  fact: FactView;
}

export interface FocusResponse {
  installed: boolean;
  shipped_version: number;
  policy: FocusPolicy | null;
  choices: FocusChoice[];
  rules: FocusRuleInForce[];
  stated: FocusStated[];
  note: string;
}

// What a word the operator typed refers to. `resolved: false` is an answer
// rather than a failure — a name the ledger does not know, or one that means
// two entities — and `reason` is the server's sentence about which.
export interface FocusSubjectResponse {
  term: string;
  resolved: boolean;
  via?: string;
  subject: FocusSubject | null;
  rule: FocusRuleInForce | null;
  reason?: string;
  history: FactView[];
}

export interface FocusInstallResult {
  policy: FocusPolicy | null;
  applies: string;
}

export interface FocusWriteResult {
  fact: FactView;
  rule: FocusRuleInForce | null;
  dispute_id?: string;
  note?: string;
}

export function getFocus(): Promise<FocusResponse> {
  return request<FocusResponse>("/api/reality/focus");
}

// getFocusSubject resolves an operator's own word through the ledger's
// aliases. It takes a term rather than an identifier because that is the whole
// point: nobody reading a candidate about "the Minecraft mod" is holding a
// canonical entity id.
export function getFocusSubject(subject: string): Promise<FocusSubjectResponse> {
  return request<FocusSubjectResponse>(`/api/reality/focus/subject?${query({ subject })}`);
}

// installFocusPolicy stores the rule set version this build ships. It sends no
// rules, and the absence is the invariant: policy authored in a request body
// would make a past decision unexplainable from anything reviewable.
export function installFocusPolicy(): Promise<FocusInstallResult> {
  return request<FocusInstallResult>("/api/reality/focus/install", { method: "POST" });
}

export function assertFocusPolicy(
  subjectId: string,
  policy: string,
  note: string,
): Promise<FocusWriteResult> {
  return postJSON<FocusWriteResult>("/api/reality/focus/assert", { subjectId, policy, note });
}

// supersedeFocusPolicy is how the operator reverses himself. `priorFactId` is
// the fact the page showed as in force, and it is both the revision being
// replaced and the confirmation that the page was current: a policy that moved
// since is a 409 with an explanation rather than a write.
export function supersedeFocusPolicy(
  priorFactId: string,
  policy: string,
  note: string,
): Promise<FocusWriteResult> {
  return postJSON<FocusWriteResult>("/api/reality/focus/supersede", { priorFactId, policy, note });
}
