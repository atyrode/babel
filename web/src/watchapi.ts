import { postJSON, request, type RunAuthority, type RunCounts } from "./api";

// ---------------------------------------------------------------------------
// Watch's own wire types (Contract W).
//
// Watch reads three things no other surface reads: what is running right now
// across the whole deployment, what the deployment produced and spent per day,
// and the half of a run receipt that never reached any page — the queries a run
// ran, the documents it fetched, the candidates it declined, what it cost.
//
// One rule decides every type in this file, and it is the rule the receipt
// itself follows: an unrecorded number is absent, never zero. A receipt written
// before resource accounting existed says nothing about CPU seconds, and a
// local receipt whose body was never opened says nothing about cost; rendering
// either as $0.00 or 0s would publish a measurement nobody made. So every
// optional figure is `number | null | undefined` here and is rendered as an
// em dash or omitted entirely at the point of use, and the server omits rather
// than zeroes. Read them with `x == null`, which catches both shapes.
//
// The counts that are not optional are the ones a day bucket genuinely knows:
// a day with no records published has zero records, which is a measurement.
// ---------------------------------------------------------------------------

// The four kinds of work a run can be. `prepare` never appears in a launch
// request — a corpus scope is fixed by the run that needs one — but it does
// appear in receipts, so a listing has to be able to name it.
export type RunKind = "explore" | "evaluate" | "conductor" | "prepare";

// The kinds this surface can start. Separate from RunKind because the launch
// route accepts three of the four, and a form offering `prepare` would be
// offering a run the route refuses.
export type LaunchKind = "explore" | "evaluate" | "conductor";

// The words a kind is shown as. Keyed loosely so an unrecorded or future kind
// falls through to what the wire said rather than to a wrong label: a receipt
// records why a run happened, not which subcommand typed it, so `kind` is a
// derivation the server declines to make where the authority does not name
// one, and both pages have to render that absence.
export const RUN_KIND_LABELS: Record<string, string> = {
  explore: "Exploration",
  evaluate: "Evaluation",
  conductor: "Conductor",
  prepare: "Preparation",
};

// How old the last word from a run is. It grades the evidence, never the
// health of a process: `lost` means nothing has been heard for a long time and
// deliberately does not mean dead. An absent value is a row that never
// announced — a child this server launched a moment ago — which is a third
// state and not a stale one.
export type LiveFreshness = "fresh" | "recent" | "stale" | "lost";

// One run in flight, deployment-wide.
//
// `pid` is this machine's own child process and is absent for every row this
// web server did not launch, which is also every row `stoppable` is false for:
// stopping a run on another machine is not something this surface can do, and
// a button that posted a pid from another host's process table would be
// stopping whatever happens to hold that number here.
export interface LiveRun {
  run_id: string;
  kind?: RunKind | string;
  started_at: string;
  // What the run last said it was doing: its recipe where it names one,
  // "launching" for a child that has not announced yet, else the announced
  // state. Server-worded; rendered verbatim.
  stage: string;
  spend_usd?: number | null;
  records?: number | null;
  stoppable: boolean;
  pid?: number | null;
  freshness?: LiveFreshness | string;
  heartbeat_age_s?: number | null;
  recipe?: string;
  authority?: RunAuthority;
  state?: string;
}

// The last automatic publication attempt (§9.1): publication is Babel's
// responsibility rather than the operator's memory, so Watch reports whether
// it is keeping up instead of offering a sync button.
export interface DrainState {
  last_at?: string;
  published?: number | null;
  sealed?: number | null;
  pending?: number | null;
}

export interface WatchLive {
  runs: LiveRun[];
  drain: DrainState;
}

// The four record kinds a day can produce. These are counts over a bucket the
// server enumerated, so zero is a fact and the fields are not optional.
export interface DayRecords {
  hypothesis: number;
  observation: number;
  finding: number;
  proposal: number;
}

export interface SeriesDay {
  // A calendar day, "2026-09-01".
  day: string;
  records: DayRecords;
  reviews: number;
  sessions: number;
  // Spend comes from local receipts, whose sealed bodies are plaintext on the
  // machine that wrote them. A day whose receipts this machine does not hold
  // is unknown rather than free.
  spend_usd?: number | null;
}

export interface WatchSeries {
  days: SeriesDay[];
}

// One recorded run as the listing shows it. The three figures beyond the
// receipt header are the ones an operator scanning for waste needs — what it
// cost, how long it took, how much it produced — and each is absent where the
// receipt cannot say, including `outputs`, which cannot distinguish "wrote
// nothing" from "the index cannot answer for this run".
export interface WatchRunRow {
  receipt_id: string;
  run_id: string;
  revision?: number | null;
  recorded_at: string;
  sync?: string;
  kind?: RunKind | string;
  authority?: RunAuthority;
  counts?: RunCounts;
  duration_s?: number | null;
  cost_usd?: number | null;
  outputs?: number | null;
}

export interface WatchRunsResponse {
  runs: WatchRunRow[];
}

export interface RunTiming {
  started_at?: string;
  finished_at?: string;
  duration_s?: number | null;
}

// What the run consumed on the machine. Every field is optional because the
// facility that counts them is optional: a run with no sandbox wrote no
// sandbox bytes, and a receipt from before the accounting existed counted
// nothing at all.
export interface RunResources {
  cpu_s?: number | null;
  max_rss_bytes?: number | null;
  sandbox_bytes_written?: number | null;
  tool_calls?: number | null;
}

export interface RunUsage {
  input_tokens?: number | null;
  output_tokens?: number | null;
  cost_usd?: number | null;
  model?: string;
  profile?: string;
}

// Where one retrieval hit came from. A locator is how an excerpt is recovered
// — file, line, and the digest of the bytes it was read from — and it is what
// makes a quoted note checkable rather than asserted.
export interface RetrievalLocator {
  path?: string;
  line?: number | null;
  offset?: number | null;
  digest?: string;
}

export interface RetrievalEvidence {
  locator?: RetrievalLocator;
  // Model-authored prose about the excerpt. Untrusted: rendered through the
  // shared sanitizing quote and never as markup.
  note?: string;
}

// `rank` is presentation order and nothing else. §5.4 forbids retrieval rank
// from becoming evidence strength, so this surface never numbers a result as a
// score or sorts by it.
export interface RetrievalResult {
  rank?: number | null;
  evidence?: RetrievalEvidence;
}

// One search the run ran. An empty `results` array is a recorded outcome — the
// query came back with nothing — which is a different fact from a step that
// recorded no results at all.
export interface RetrievalStep {
  index: number;
  tool: string;
  query: string;
  at: string;
  scope?: string;
  results?: RetrievalResult[] | null;
  // Frontier records a self-retrieval disclosed, by identifier: a durable
  // record is addressed by its id rather than by a file locator.
  records?: string[] | null;
}

// One public document a brokered fetch read. The content is deliberately
// absent from a receipt — the digest is what makes the citation checkable, and
// a receipt carrying fetched pages would put a copy of the public web in the
// operator's durable store.
export interface ResearchSource {
  url: string;
  retrieved_at?: string;
  media_type?: string;
  bytes?: number | null;
  truncated?: boolean;
  source_id?: string;
  digest?: string;
  redirects?: string[] | null;
}

// Something the run considered and did not publish, and why. Two dispositions
// rather than one flag: a deferred candidate can still be picked up, a
// rejected one was ruled out, and collapsing them would lose the difference.
export interface RunCandidate {
  id: string;
  reason: string;
  at?: string;
  disposition?: "deferred" | "rejected" | string;
  origin?: RetrievalEvidence[] | null;
}

export interface RunFailure {
  stage: string;
  code: string;
  message: string;
  at?: string;
}

// The versioned facilities and policies the run ran under (§7). A later review
// of what was disclosed is meaningless without knowing which rules were in
// force, which is why they are recorded per run rather than assumed.
export interface CapabilityVersions {
  sandbox?: string;
  tool?: string;
  repository?: string;
  public_research?: string;
}

export interface JobVersions {
  job?: number | null;
  prompt?: string;
  schema?: string;
}

export interface PolicyVersions {
  redaction?: string;
  disclosure?: string;
}

export interface RunVersions {
  capability?: CapabilityVersions;
  job?: JobVersions;
  policy?: PolicyVersions;
}

// One cookbook asset the run applied. A receipt records the whole applied set
// with versions rather than one titled recipe, and no title is stored, so the
// set is rendered as identifiers and versions rather than with a blank column
// where a title would go.
export interface CookbookAsset {
  id: string;
  kind?: string;
  version?: number | null;
}

// A durable record the run produced. `kind` is what makes it openable: one
// record has one page, reached by id.
export interface RunOutput {
  id: string;
  kind?: string;
  title?: string;
}

// One run receipt whole: the header that says why it ran and the body that
// says what it did. Every array may be absent, and absent means the receipt
// recorded none — which the page states rather than drawing an empty section.
export interface RunDetail {
  receipt_id: string;
  run_id: string;
  preparation_id?: string;
  revision?: number | null;
  supersedes?: string;
  recorded_at?: string;
  sync?: string;
  kind?: RunKind | string;
  authority?: RunAuthority;
  counts?: RunCounts;
  timing?: RunTiming;
  resources?: RunResources;
  usage?: RunUsage;
  retrieval?: RetrievalStep[] | null;
  research?: ResearchSource[] | null;
  candidates?: RunCandidate[] | null;
  failures?: RunFailure[] | null;
  versions?: RunVersions;
  cookbook?: CookbookAsset[] | null;
  outputs?: RunOutput[] | null;
}

// What a launch may ask for. Every field is one of the CLI's own flags, and
// only the ones the operator filled in are sent: an omitted field is the
// machine's configured default, where a zero would be an instruction.
//
// The union is deliberately flat while the route is per-kind: it refuses an
// argument the chosen kind has no flag for, which is stricter than this type
// can express and is where the check belongs. The page therefore offers each
// kind only its own fields.
export interface LaunchArgs {
  // conductor
  until?: string;
  concurrent?: number;
  evaluate?: number;
  consolidate?: number;
  once?: boolean;
  // explore
  preparation?: string;
  // Repeatable on the command line, so an array on the wire even when the
  // operator named one.
  recipe?: string[];
  develop?: number;
  // explore and evaluate
  retrievals?: number;
  fetches?: number;
  // evaluate: the record whose earlier statement this review supersedes.
  correct?: string;
  // explore and conductor
  challenge?: boolean;
  synthesize?: boolean;
}

export interface LaunchRequest {
  kind: LaunchKind;
  args: LaunchArgs;
}

// What the launch answers with. `run_id` is null when the child has not yet
// announced one — a run outlives the request that created it, so the surface
// tracks it through the live read rather than blocking until it names itself.
export interface LaunchResult {
  run_id?: string | null;
  pid?: number | null;
  detail?: string;
}

// Stopping is graceful by construction: the server writes the stop file or
// sends SIGTERM, and a run that is mid-cycle finishes what it committed.
// Nothing here can kill a process, so nothing here can lose a cycle's work.
//
// `method` says which of the two happened, because they stop at different
// moments: a stop file is honoured at the next cycle boundary, a signal at the
// next safe point inside one.
export interface StopResult {
  stopped?: boolean;
  pid?: number | null;
  method?: "stop-file" | "signal" | string;
  detail?: string;
}

// ---------------------------------------------------------------------------
// Rendering an absence.
//
// These three live beside the types rather than in a page because the rule
// they implement is the types' own: an unrecorded figure is absent, and both
// pages on this surface have to say so identically. A cost rendered as $0.00
// on one page and as "—" on the other would make the reader believe the two
// are different runs.
//
// The em dash is the whole vocabulary for "nobody recorded this". It is never
// "0", "unknown" or "n/a": a dash reads as a blank in a column of figures,
// which is what it is.
// ---------------------------------------------------------------------------

export const ABSENT = "—";

// dollars renders money at the precision the figure deserves. A run that cost
// four tenths of a cent is not "$0.00", which reads as free; and cents survive
// up to four figures, because a column mixing "$13" with "$3.28" cannot be
// scanned down the decimal point.
export function dollars(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return ABSENT;
  if (value === 0) return "$0";
  if (value < 0.01) return `$${value.toFixed(4)}`;
  if (value < 1000) return `$${value.toFixed(2)}`;
  return `$${Math.round(value).toLocaleString()}`;
}

// seconds renders a duration the way a receipt records it, to the precision a
// reader compares runs at: sub-minute in seconds, then minutes, then hours.
export function seconds(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value < 0) return ABSENT;
  if (value < 1) return `${(value * 1000).toFixed(0)}ms`;
  if (value < 60) return `${value < 10 ? value.toFixed(1) : value.toFixed(0)}s`;
  const minutes = Math.floor(value / 60);
  if (minutes < 60) return `${minutes}m ${String(Math.round(value % 60)).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

export function count(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return ABSENT;
  return value.toLocaleString();
}

export function getWatchLive(): Promise<WatchLive> {
  return request<WatchLive>("/api/watch/live");
}

export function getWatchSeries(days = 30): Promise<WatchSeries> {
  return request<WatchSeries>(`/api/watch/series?days=${encodeURIComponent(String(days))}`);
}

export function getWatchRuns(limit = 50): Promise<WatchRunsResponse> {
  return request<WatchRunsResponse>(`/api/watch/runs?limit=${encodeURIComponent(String(limit))}`);
}

// getWatchRun reads one receipt by run id rather than by receipt id, because
// that is the identity everything else on this surface carries: a live row, a
// record's provenance and a listing row all name the run, and an amended
// receipt has a new receipt id for the same run.
export function getWatchRun(runID: string): Promise<RunDetail> {
  return request<RunDetail>(`/api/watch/runs/${encodeURIComponent(runID)}`);
}

// launchRun starts this machine's own binary under the same ceilings, profile
// and grants the CLI enforces, attributed to the operator of this session. A
// deployment with no ceilings set is refused with the CLI's own sentence, which
// arrives as the message of an APIError and is rendered verbatim.
export function launchRun(input: LaunchRequest): Promise<LaunchResult> {
  return postJSON<LaunchResult>("/api/watch/launch", input);
}

export function stopRun(pid: number): Promise<StopResult> {
  return postJSON<StopResult>("/api/watch/stop", { pid });
}
