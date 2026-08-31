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

export interface AnalysisState {
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

export interface HypothesisSummary {
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

export interface HypothesesResponse {
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

export interface FindingSummary {
  id: string;
  run_id: string;
  created_at: string;
  title: string;
  observations: number;
  hypotheses: number;
  review_status: ReviewStatus;
}

export interface FindingsResponse {
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

export interface QueueItem {
  subject: ReviewSubject;
  enrolled_at: string;
  status: ReviewStatus;
  decisions: number;
  last_decided_at?: string;
  refinements: number;
  // The subject's statement/title/claim, so the queue is readable.
  excerpt: string;
}

export interface ReviewQueueResponse {
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

const TOKEN_KEY = "babel.web.token";

// The launch token arrives in the URL fragment, which the browser never sends
// to any server (SPEC.md §146). It is copied into session storage and the
// fragment is erased immediately, so a reload or a screenshot carries no
// credential. Every later request presents it as a bearer header.
//
// Honest accounting of that erasure: there are two independent mechanisms, and
// this replaceState is only one of them. "#token=…" matches no route, so it
// falls through to App.tsx's catch-all, which is <Navigate to="/"
// replace /> — a replacing redirect that drops the token-bearing entry on its
// own. Measured rather than assumed, by disabling each in turn and running the
// browser acceptance: scrub off with the redirect replacing passes; scrub on
// with the redirect pushing passes; with both disabled the history walk fails
// and names the retained "#token=" entry.
//
// Both are kept because either alone is a single point of failure for a
// credential, and the redirect's `replace` is easy to drop while editing
// routes. The test defends the property, not the mechanism: no reachable
// history entry retains the token.
//
// The fragment is also route state, so this must read it before the router
// mounts. ES module evaluation guarantees that: main.tsx imports App, which
// imports this module, so this function runs during import and therefore
// before createRoot().render(). Lazy-loading this module would let the router
// rewrite the fragment away from "#token=" first and silently break
// authentication. The nonce-to-cookie exchange in SPEC.md §146 would remove
// this coupling by using the fragment exactly once.
function bootstrapToken(): string {
  const hash = window.location.hash.replace(/^#/u, "");
  const supplied = new URLSearchParams(hash).get("token") ?? "";
  if (supplied) {
    try {
      window.sessionStorage.setItem(TOKEN_KEY, supplied);
    } catch {
      // A locked-down browser can disable session storage; the current request still works.
    }
    const url = new URL(window.location.href);
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}`,
    );
    return supplied;
  }
  try {
    return window.sessionStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

const token = bootstrapToken();
const errorListeners = new Set<(message: string | null) => void>();
let currentError: string | null = null;

function safeMessage(value: unknown): string {
  const message = value instanceof Error ? value.message : String(value);
  return message.replace(/[\u0000-\u001f\u007f-\u009f]/gu, "").trim() || "Request failed";
}

function publishError(value: unknown): void {
  currentError = safeMessage(value);
  for (const listener of errorListeners) listener(currentError);
}

export function subscribeAPIErrors(listener: (message: string | null) => void): () => void {
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

// send is the one transport path every API call shares: bearer token, cache
// bypass, bounded wait, error mapping, and error publication. Callers differ
// only in how they read a successful body.
async function send<T>(
  path: string,
  init: RequestInit,
  read: (response: Response) => Promise<T>,
): Promise<T> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const headers = new Headers(init.headers);
    headers.set("Authorization", `Bearer ${token}`);
    const response = await fetch(path, {
      ...init,
      headers,
      cache: "no-store",
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
      throw new APIError(response.status, message);
    }
    return await read(response);
  } catch (error) {
    const failure = controller.signal.aborted
      ? new APIError(408, `${path} did not respond within ${REQUEST_TIMEOUT_MS / 1000}s`)
      : error;
    publishError(failure);
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

// lockServer is one-way. The server revokes the launch token as it answers, so
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

export function getReviewQueue(
  filter: { type?: string; status?: string } = {},
): Promise<ReviewQueueResponse> {
  const values: Record<string, string> = {};
  if (filter.type) values.type = filter.type;
  if (filter.status) values.status = filter.status;
  const suffix = Object.keys(values).length ? `?${query(values)}` : "";
  return request<ReviewQueueResponse>(`/api/review/queue${suffix}`);
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
