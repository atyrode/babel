/*
  THE EVALUATION COORDINATOR (§4.12, docs/evaluation-lifecycle.md §E4): which review of which
  record in which role is worth spending authorized attention on next, who is entitled to do
  it, and what it cost. It is the port of internal/evaluation's `policy.go`, `selection.go` and
  `coordination.go` onto the plugin's own tables, and it is one file because those three
  answered one question in three places: a draw that could not see the claims would hand the
  same work to two workers, and a claim that could not see the policy would spend against a
  ceiling nobody installed.

  What crossed unchanged:
  - the measured constants. Every number here is the number the Go policy carried, with the
    measurement that produced it, because a bound whose reason was lost is a magic number the
    next operator will round.
  - the order of the gates. A disabled policy refuses before any budget is consulted, the budget
    refuses before any candidate is built, and the candidate filter refuses before any
    randomness is drawn. Read the other way round, a disabled deployment would report "nothing
    eligible" and an exhausted one "no work", which are the two states a scheduler must never
    confuse.
  - the reservations. A share of every cycle goes to the oldest-due initial reviews, a protected
    share to what nothing recommends, a protected share to what nobody has read at all, and
    §4.13's two work lanes — filing and the backlog — draw candidate sets no review lane can
    touch or be displaced by.
  - the fence. A claim's authority is its fence and never its run id: a resumed worker can
    present the right run with a superseded fence and is still refused.

  What changed in the crossing, and why:
  - a gap is DATA. The Go tree returned formatted sentences; here a gap is `{recordId, role,
    reason, detail}`, so the pulse can count the reasons a cycle declined to spend without
    parsing prose, and the sentence is still there for the operator to read.
  - the spend ledger is the `claims` table itself, and a takeover ARCHIVES the epoch it
    supersedes as its own finished row (`outcome = 'abandoned'`, charged at what it reserved).
    That keeps Go's conservative accounting — an abandoned attempt may have burned its whole
    reservation before the machine died, so releasing it as zero would let a crash loop spend
    the day's allowance many times over — without a second table for reservations.
  - the operator's stance toward a topic is read where §4.13 puts it: the `lifecycle` and
    `analysis-policy` facts on the entities a record is FILED under. Excluded withholds the
    work and keeps the record; dormant damps its weight. Nothing here deletes anything.
  - the drawn subject is a record's head revision. There is no enrolment step in the rewrite,
    so every role its kind can carry is an obligation, rather than one a recipe activated.
*/

import { z } from "zod";
import type { GuestDatabase, GuestSqlParam, GuestSqlRow } from "@manifold/plugin-kit";
import { INTEREST_STATES, ROLES } from "../contract.ts";

/** The store handle this reads through; `BabelStore` satisfies it. */
export interface CoordinatorStore {
  readonly db: GuestDatabase;
}

export type Role = (typeof ROLES)[number];
export type InterestState = (typeof INTEREST_STATES)[number];

// ---------------------------------------------------------------------------- the lanes

/**
 * The allocation lanes one cycle's attention is divided into. This is NOT a record's lifecycle
 * vocabulary: it is which reservation paid for the work, so a cycle's accounting can say how
 * much went to arguing rather than to reviewing, and to naming rather than to judging.
 */
export const LANES = [
  "coverage",
  "weighted",
  "exploration",
  "discovery",
  "challenge",
  "filing",
  "backlog",
] as const;
export type Lane = (typeof LANES)[number];

// ---------------------------------------------------------------------------- the policy

/** The version stamped on the default policy; compared for equality and printed, never ordered. */
export const DEFAULT_POLICY_VERSION = "1";

/**
 * The default policy's settings, each with the reason its number is that number. They are named
 * rather than written inline so a test asserting a bound and the policy stating it cannot
 * disagree.
 */
const MEASURED = {
  /** How often a coverage check runs. Hourly: a check is a scan, not a model call, so an hour
   *  keeps "never reviewed" honest within one working session at no compute cost. */
  cadenceSeconds: 3600,
  /** How long an eligible record may go without its initial review before it is overdue.
   *  Fourteen days is the span over which the context behind a candidate is still recognisable
   *  to the operator who recorded it; past that the review is archaeology. */
  overdueSeconds: 14 * 24 * 3600,
  /** How many independent assessments a role needs before coverage stops calling the revision
   *  never-reviewed. Two, not one: a single assessment cannot show disagreement, and
   *  disagreement is the signal §4.12 says reception actually carries. */
  initialReviews: 2,
  /** How long a settled opinion rests. Seven days against a fourteen-day overdue bound means a
   *  stable-reception revision is asked at most twice per overdue window. */
  cooldownSeconds: 7 * 24 * 3600,
  /** Total assessments per record across all roles: two initial reviews plus one bounded
   *  challenge and comparison pair per side. It is what stops persistent disagreement from
   *  becoming an obligation to vote until consensus. */
  maxItemReviews: 6,
  /** How long a claim survives a worker that stops answering. Fifteen minutes is longer than any
   *  single review measured on this deployment (386s, 461s, 556s, 630s end to end) and short
   *  enough that a crashed worker's record is drawable again within one cadence period. */
  leaseSeconds: 900,
  /** One cycle's assignments, so a cadence tick can never turn the whole eligible set into
   *  concurrent work. */
  batchSize: 4,
  /** Half of one cycle for the oldest-due initial reviews. Half rather than all: a coverage
   *  share of one would make the weighted policy and the protected shares unreachable code. */
  coverageShare: 0.5,
  /** The random share that is not justified by weight: what lets a revision nothing recommends
   *  still be seen. Zero is refused. */
  explorationShare: 0.15,
  /** Attention reserved for records nobody has assessed in any role, so a deployment busy
   *  arguing about its favourite proposal still notices the observation nobody has read. Zero
   *  is refused. */
  discoveryShare: 0.1,
  /** §4.13's own draw kind: deciding what a record is about rather than what it is worth. Zero
   *  is a legitimate policy — an operator who files by hand spends nothing on it. */
  filingShare: 0.1,
  /** §4.13's second draw kind: the hypotheses a run deferred and nobody came back to. Zero is
   *  legitimate for the filing share's reason. */
  backlogShare: 0.1,
  /** The authorized spend, in the cost unit the receipts report. Deliberately small: raising it
   *  is an explicit operator act, and a default that could spend a day's allowance in one cycle
   *  would make the per-cycle bound decorative. */
  perCycleCost: 0.25,
  dailyCost: 2.0,
} as const;

/**
 * The floor a lease has to clear for the batch it is granted against, measured rather than
 * chosen: four review runs were lost here under a lease of 240s and a batch of 24, every one of
 * them because the claim expired during the preparation that precedes the worker. Those runs
 * took 386s to 630s, 16s to 26s of wall clock per record in the batch; twenty seconds is that
 * rounded down, and five minutes is the floor under any batch at all, because a batch of one
 * still has to cover the same preparation.
 */
const LEASE_SECONDS_PER_SUBJECT = 20;
const LEASE_FLOOR_SECONDS = 300;

export function leaseFloor(batchSize: number): number {
  return Math.max(LEASE_SECONDS_PER_SUBJECT * batchSize, LEASE_FLOOR_SECONDS);
}

/**
 * The policy as the `policies` row carries it. Every field defaults to the measured constant, so
 * a payload that predates a setting is completed rather than refused — and `enabled` defaults
 * to false, which is §14's activation gate expressed as a value: turning evaluation on is one
 * recorded operator decision and never a migration.
 */
export const PolicySchema = z.strictObject({
  version: z.string().trim().min(1).default(DEFAULT_POLICY_VERSION),
  enabled: z.boolean().default(false),
  cadenceSeconds: z.number().int().default(MEASURED.cadenceSeconds),
  overdueSeconds: z.number().int().default(MEASURED.overdueSeconds),
  initialReviews: z.number().int().default(MEASURED.initialReviews),
  cooldownSeconds: z.number().int().default(MEASURED.cooldownSeconds),
  coverageShare: z.number().default(MEASURED.coverageShare),
  explorationShare: z.number().default(MEASURED.explorationShare),
  discoveryShare: z.number().default(MEASURED.discoveryShare),
  filingShare: z.number().default(MEASURED.filingShare),
  backlogShare: z.number().default(MEASURED.backlogShare),
  maxItemReviews: z.number().int().default(MEASURED.maxItemReviews),
  perCycleCost: z.number().default(MEASURED.perCycleCost),
  dailyCost: z.number().default(MEASURED.dailyCost),
  leaseSeconds: z.number().int().default(MEASURED.leaseSeconds),
  batchSize: z.number().int().default(MEASURED.batchSize),
});
export type Policy = z.infer<typeof PolicySchema>;

/** The policy a deployment runs before an operator configures one. It is disabled. */
export const DEFAULT_POLICY: Policy = PolicySchema.parse({});

/**
 * Refuses a policy that cannot be honoured, and returns the sentence saying why — null for one
 * that can. Every refusal is a setting that would make some other part of the system lie:
 * an unversioned policy could never be replayed against; a zero exploration or discovery share
 * removes a protected allocation; shares over one would over-commit a cycle, so one lane's
 * reservation would silently come out of another's; a cap below the initial reviews leaves a
 * role permanently under-reviewed while reporting the record finished; a daily ceiling below
 * one cycle's makes the per-cycle bound decorative.
 */
export function validatePolicy(policy: Policy): string | null {
  if (policy.version.trim() === "") return "a policy has no version";
  if (policy.cadenceSeconds <= 0) return `cadence ${String(policy.cadenceSeconds)}s must be positive`;
  if (policy.overdueSeconds <= 0) {
    return `overdue threshold ${String(policy.overdueSeconds)}s must be positive`;
  }
  if (policy.initialReviews < 1) {
    return `initial reviews ${String(policy.initialReviews)} must be at least one`;
  }
  if (policy.cooldownSeconds < 0) return `cooldown ${String(policy.cooldownSeconds)}s is negative`;
  const shares: readonly (readonly [string, number, boolean])[] = [
    ["coverage share", policy.coverageShare, false],
    ["exploration share", policy.explorationShare, true],
    ["discovery share", policy.discoveryShare, true],
    ["filing share", policy.filingShare, false],
    ["backlog share", policy.backlogShare, false],
  ];
  for (const [name, value, protectedShare] of shares) {
    if (!(value >= 0 && value <= 1)) return `${name} ${String(value)} is outside [0,1]`;
    if (protectedShare && value <= 0) {
      return `${name} must stay positive; a zero share removes a protected allocation`;
    }
  }
  const total =
    policy.coverageShare +
    policy.explorationShare +
    policy.discoveryShare +
    policy.filingShare +
    policy.backlogShare;
  if (total > 1) return `reserved shares total ${String(total)} and over-commit one cycle`;
  if (policy.maxItemReviews < policy.initialReviews) {
    return `max item reviews ${String(policy.maxItemReviews)} is below initial reviews ${String(policy.initialReviews)}`;
  }
  if (policy.perCycleCost <= 0) {
    return `per-cycle cost ${String(policy.perCycleCost)} must be positive`;
  }
  if (policy.dailyCost < policy.perCycleCost) {
    return `daily cost ${String(policy.dailyCost)} is below the per-cycle cost ${String(policy.perCycleCost)}`;
  }
  if (policy.leaseSeconds <= 0) return `lease ${String(policy.leaseSeconds)}s must be positive`;
  if (policy.batchSize < 1) return `batch size ${String(policy.batchSize)} must be at least one`;
  return null;
}

/**
 * `validatePolicy` plus the rule only a policy being INSTALLED has to satisfy: its lease must
 * cover its batch. It is separate because the floor arrived after policies had been stored under
 * it — a deployment whose stored policy predates the floor keeps drawing (renewal carries those
 * reviews) and is refused only when it tries to install another one that would need renewal to
 * have worked at all. Refusing the stored one at draw time would stop every review here until
 * the operator noticed, which is the outage the floor exists to prevent.
 */
export function validateNewPolicy(policy: Policy): string | null {
  const refusal = validatePolicy(policy);
  if (refusal !== null) return refusal;
  const floor = leaseFloor(policy.batchSize);
  if (policy.leaseSeconds < floor) {
    return (
      `lease ${String(policy.leaseSeconds)}s cannot cover a batch of ${String(policy.batchSize)}: ` +
      `a lease must allow at least ${String(LEASE_SECONDS_PER_SUBJECT)}s per assignment and never ` +
      `less than ${String(LEASE_FLOOR_SECONDS)}s, so this batch needs ${String(floor)}s`
    );
  }
  return null;
}

export interface PolicyInForce {
  readonly policy: Policy;
  /** `stored` is the newest `policies` row; `default` is the disabled policy above. */
  readonly source: "stored" | "default";
  readonly version: string;
  readonly recordedAt: number | null;
}

// ---------------------------------------------------------------------------- what a draw answers

export const GAP_REASONS = [
  "excluded",
  "retired",
  "replaced",
  "capped",
  "claimed",
  "exhausted",
  "cooling",
  "settled",
  "unsupported",
  "empty",
] as const;
export type GapReason = (typeof GAP_REASONS)[number];

/**
 * One candidate this draw declined and why — data rather than a sentence, so a surface can count
 * the reasons a cycle did not spend. §E4 requires unsupported sources and repeatedly skipped
 * records to stay visible as gaps rather than receiving negative votes, and an exhausted draw
 * that could not say why leaves an operator unable to tell a satisfied deployment from a stuck
 * one. `recordId` is empty for a gap about a whole lane.
 */
export interface Gap {
  readonly recordId: string;
  readonly role: string;
  readonly reason: GapReason;
  readonly detail: string;
}

export const STOP_REASONS = [
  "invalid-policy",
  "disabled",
  "batch",
  "per-cycle",
  "daily",
  "no-candidates",
  "no-lane",
] as const;
export type StopReason = (typeof STOP_REASONS)[number];

/** Why a draw produced no assignment. It is recorded either way: "why is nothing being reviewed"
 *  is the question an operator asks when the answer is not visible. */
export interface Stop {
  readonly reason: StopReason;
  readonly detail: string;
}

/**
 * One unit of authorized work, before anything is reserved. The id is a digest of the record,
 * the role, the policy version and the sample ordinal — and deliberately NOT of the run that
 * drew it: two conductors that independently decide the same review is next derive the same id,
 * contend for one claim, and exactly one wins. An id containing the run would give each its own
 * assignment for one review and spend the shared allowance twice on one vote.
 */
export interface Assignment {
  readonly id: string;
  /** The exact revision drawn. */
  readonly recordId: string;
  readonly rootId: string;
  readonly kind: string;
  readonly role: Role;
  readonly lane: Lane;
  readonly policyVersion: string;
  readonly ordinal: number;
  /** The seed this draw ran under, as a decimal string; half of what makes it replayable. */
  readonly seed: string;
  /** The candidate set it ran against; the other half. */
  readonly inputDigest: string;
  readonly reservedCost: number;
  readonly drawnAt: number;
  /** The entities the record is filed under, whose stance decided it was drawable at all. */
  readonly topics: readonly string[];
}

export type DrawResult =
  | { readonly outcome: "assignment"; readonly assignment: Assignment; readonly gaps: readonly Gap[] }
  | { readonly outcome: "gap"; readonly gap: Stop; readonly gaps: readonly Gap[] };

export interface DrawRequest {
  /** Who is drawing. The per-cycle ceiling is measured against one run's whole UTC day. */
  readonly runId: string;
  readonly now?: number;
  readonly seed?: bigint;
}

// ---------------------------------------------------------------------------- claims

export interface Claim {
  readonly id: string;
  readonly recordId: string;
  readonly role: string;
  readonly lane: string;
  readonly policyVersion: string;
  readonly jobId: string | null;
  readonly runId: string;
  readonly fence: number;
  readonly reservedCost: number;
  readonly actualCost: number | null;
  readonly grantedAt: number;
  readonly expiresAt: number;
  readonly finishedAt: number | null;
  readonly outcome: string | null;
}

export const REFUSALS = [
  "conflict",
  "finished",
  "budget",
  "expired",
  "not-found",
  "taken-over",
  "invalid",
] as const;
export type RefusalReason = (typeof REFUSALS)[number];

export interface Refusal {
  readonly reason: RefusalReason;
  readonly detail: string;
}

export interface ClaimRequest {
  readonly assignment: Assignment;
  readonly runId: string;
  readonly jobId?: string;
  readonly now?: number;
}

export type ClaimResult =
  | { readonly outcome: "granted"; readonly claim: Claim }
  | { readonly outcome: "refused"; readonly refusal: Refusal };

export interface RenewRequest {
  readonly id: string;
  readonly runId: string;
  readonly fence: number;
  readonly now?: number;
}

export type RenewResult =
  | { readonly outcome: "renewed"; readonly expiresAt: number }
  | { readonly outcome: "refused"; readonly refusal: Refusal };

/** What a run reports when it is done: the receipt's cost and how the work ended. */
export const CLOSURES = ["completed", "failed", "skipped"] as const;
export type Closure = (typeof CLOSURES)[number];

export interface FinishRequest {
  readonly id: string;
  readonly runId: string;
  readonly fence: number;
  readonly cost: number;
  readonly outcome: Closure;
  readonly now?: number;
}

export type FinishResult =
  | {
      readonly outcome: "finished";
      readonly cost: number;
      readonly reserved: number;
      /** True when the spend passed its reservation. The full amount is charged to the day —
       *  so the next claim is judged against what was spent — and the identical retry reports
       *  the identical overrun; an overspend a caller could retry its way out of is not one. */
      readonly overrun: boolean;
    }
  | { readonly outcome: "refused"; readonly refusal: Refusal };

/** What the day already owes: reported cost where a claim settled, the full reservation where it
 *  did not. An expired lease says nothing about what it spent, so releasing it as zero would let
 *  one abandoned attempt authorize a second for free. */
export interface Spend {
  readonly day: string;
  readonly total: number;
  readonly byRun: Readonly<Record<string, number>>;
}

export interface Coordinator {
  policy(): Promise<PolicyInForce>;
  draw(request: DrawRequest): Promise<DrawResult>;
  claim(request: ClaimRequest): Promise<ClaimResult>;
  renew(request: RenewRequest): Promise<RenewResult>;
  finish(request: FinishRequest): Promise<FinishResult>;
  spend(now?: number): Promise<Spend>;
}

// ---------------------------------------------------------------------------- the tables it reads

/** The review roles each record kind can carry, in a stable order. An empty list means the kind
 *  is not review work at all. Observations and findings carry no `outcome`: they are claims about
 *  what is the case, and there is no promised change to verify. */
const ROLES_FOR_KIND: Record<string, readonly Role[]> = {
  hypothesis: ["reception", "evidence", "challenge", "relevance"],
  observation: ["reception", "evidence", "relevance"],
  finding: ["reception", "evidence", "challenge", "relevance"],
  proposal: ["reception", "evidence", "challenge", "comparison", "outcome", "relevance"],
};

/**
 * The operator's stance toward a topic, as §4.13's two facts spell it, plus the one state that
 * is not a degree of interest: a retired topic leaves the list rather than sitting at the bottom
 * of it.
 */
type Stance = InterestState | "retired";

/**
 * How much a topic's recorded stance multiplies the weight of work filed under it. It is the port
 * of §4.8's allowance: `excluded` and `retired` withhold the work outright (decided before any
 * weighting, so the zeroes here are never reached by a drawn candidate), and dormancy is a reason
 * to spend less rather than to forget. A record under no topic, or under one nobody has stated a
 * stance about, is weighted as it stands.
 */
const STANCE_WEIGHT: Record<Stance, number> = {
  working: 1.5,
  watching: 1,
  "not-now": 0.25,
  excluded: 0,
  retired: 0,
};

/** The stances that withhold work, and the gap each is reported as. §4.8 and §5.2 both refuse to
 *  delete a restricted subject, so this withholds expenditure and never existence. */
const WITHHOLDING: Record<string, GapReason | undefined> = {
  excluded: "excluded",
  retired: "retired",
};

/** How many times one record and role may be skipped or fail before it stops being drawn. Three:
 *  §E4 requires repeated skips to consume bounded attention and stay visible as a gap rather than
 *  receiving a negative vote. Without it the whole allowance would go to one unreadable record. */
const MAX_SETBACKS = 3;

/** The reception margin at which an opinion counts as settled: eighty percent one-sided, in
 *  either direction, because the reason to stop asking is the same — the next vote is
 *  predictable, so it buys no information. */
const SETTLED_MARGIN = 0.8;

/** What a changed input multiplies a candidate's weight by. Two, the largest single multiplier:
 *  attention is restored on a material change, and a factor that merely nudged would leave a
 *  reopened revision behind every lightly reviewed one. */
const MATERIAL_CHANGE_WEIGHT = 2;

/** Rows one scan reads per call. Well under the engine's own cap (10,000 rows and 4 MiB per call,
 *  `@manifold/plugin`'s `MAX_SQL_ROWS`), because a draw reads the whole eligible set and this
 *  deployment's frontier already holds several thousand records: a scan that grew into the cap
 *  would turn evaluation off with an opaque refusal rather than drawing less. */
const SCAN_PAGE = 2000;

interface Head {
  readonly id: string;
  readonly rootId: string;
  readonly kind: string;
  readonly createdAt: number;
}

interface RoleFacts {
  readonly reviews: number;
  readonly lastReviewed: number | null;
  readonly support: number;
  readonly oppose: number;
  readonly voted: number;
  readonly onOlder: number;
}

interface ClaimFacts {
  readonly total: number;
  readonly active: number;
  readonly setbacks: number;
  readonly completed: number;
}

interface Candidate {
  readonly head: Head;
  readonly role: Role;
  readonly lane: Lane | null;
  readonly dueAt: number;
  readonly weight: number;
  readonly ordinal: number;
  readonly initial: boolean;
  readonly untouched: boolean;
  readonly revisit: boolean;
  readonly filing: boolean;
  readonly backlog: boolean;
  /** When the candidate was set down, for the backlog lane's own order: the backlog is cleared
   *  as it accumulated, and a candidate written in January and deferred in June has been waiting
   *  since June. */
  readonly deferredAt: number;
  readonly topics: readonly string[];
}

// ---------------------------------------------------------------------------- row helpers

function text(value: GuestSqlParam | undefined): string {
  return typeof value === "string" ? value : "";
}

function maybeText(value: GuestSqlParam | undefined): string | null {
  return typeof value === "string" && value !== "" ? value : null;
}

function count(value: GuestSqlParam | undefined): number {
  if (typeof value === "number") return value;
  if (typeof value === "bigint") return Number(value);
  if (typeof value === "string") return Number(value);
  return 0;
}

function maybeCount(value: GuestSqlParam | undefined): number | null {
  return value === null || value === undefined ? null : count(value);
}

function at(value: GuestSqlParam | undefined): number {
  const parsed = Date.parse(text(value));
  return Number.isNaN(parsed) ? 0 : parsed;
}

function maybeAt(value: GuestSqlParam | undefined): number | null {
  const raw = maybeText(value);
  if (raw === null) return null;
  const parsed = Date.parse(raw);
  return Number.isNaN(parsed) ? null : parsed;
}

function iso(ms: number): string {
  return new Date(ms).toISOString();
}

/**
 * The UTC day a moment falls in and the half-open string window `granted_at` is compared against:
 * `[day, from, until)`. A day is UTC rather than local, because two machines in different zones
 * charging "today" against one allowance would otherwise disagree about when it resets, and the
 * window is a string range rather than `substr(granted_at, 1, 10)` so the index on `granted_at`
 * is the one that answers it.
 */
function dayWindow(ms: number): readonly [string, string, string] {
  const date = new Date(ms);
  const until = Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate() + 1);
  const day = date.toISOString().slice(0, 10);
  return [day, `${day}T00:00:00.000Z`, new Date(until).toISOString()];
}

// ---------------------------------------------------------------------------- determinism

const MASK64 = (1n << 64n) - 1n;

/** A 64-bit FNV-1a over the parts, separated by a byte no identifier carries. It gives the
 *  assignment its deployment-wide identity and the draw its input digest; neither is a secret. */
function digest(parts: readonly string[]): string {
  let hash = 0xcbf29ce484222325n;
  for (const byte of new TextEncoder().encode(parts.join("\u001f"))) {
    hash = ((hash ^ BigInt(byte)) * 0x100000001b3n) & MASK64;
  }
  return hash.toString(16).padStart(16, "0");
}

/**
 * The seeded stream every draw samples from: SplitMix64, which is deterministic, has no
 * degenerate seed and needs no state beyond one word. Randomness is recorded rather than hidden —
 * the seed and the input digest travel on the assignment, so a draw can be re-derived; what that
 * does not promise is that any individual draw preferred the highest-weight candidate, because a
 * weighted sample that always picked the maximum would not be a sample.
 */
class Stream {
  private state: bigint;

  constructor(seed: bigint) {
    this.state = seed & MASK64;
  }

  private next(): bigint {
    this.state = (this.state + 0x9e3779b97f4a7c15n) & MASK64;
    let z = this.state;
    z = ((z ^ (z >> 30n)) * 0xbf58476d1ce4e5b9n) & MASK64;
    z = ((z ^ (z >> 27n)) * 0x94d049bb133111ebn) & MASK64;
    return (z ^ (z >> 31n)) & MASK64;
  }

  float(): number {
    return Number(this.next() >> 11n) / 9007199254740992;
  }

  below(bound: number): number {
    const drawn = Math.floor(this.float() * bound);
    return drawn >= bound ? bound - 1 : drawn;
  }
}

// ---------------------------------------------------------------------------- the coordinator

export function coordinator(store: CoordinatorStore, now: () => number = Date.now): Coordinator {
  const db = store.db;

  async function policyInForce(): Promise<PolicyInForce> {
    const rows = await db.query(
      `SELECT version, payload, recorded_at FROM policies ORDER BY seq DESC LIMIT 1`,
    );
    const row = rows[0];
    if (row === undefined) {
      return {
        policy: DEFAULT_POLICY,
        source: "default",
        version: DEFAULT_POLICY.version,
        recordedAt: null,
      };
    }
    const version = text(row["version"]);
    let payload: unknown;
    try {
      payload = JSON.parse(text(row["payload"]));
    } catch (error) {
      throw new Error(`the policy row ${version} does not hold JSON: ${String(error)}`);
    }
    const parsed = PolicySchema.safeParse({
      ...(payload as Record<string, unknown>),
      version,
    });
    if (!parsed.success) {
      throw new Error(`the policy row ${version} is not a policy: ${parsed.error.message}`);
    }
    return {
      policy: parsed.data,
      source: "stored",
      version,
      recordedAt: maybeAt(row["recorded_at"]),
    };
  }

  /** What one review reserves: the per-cycle ceiling divided by the batch, so a full batch
   *  reserves exactly one cycle's allowance and no more. Reserving the whole ceiling per review
   *  would have concurrent workers each treat the shared allowance as theirs. */
  function reservedCost(policy: Policy): number {
    return policy.batchSize <= 0 ? policy.perCycleCost : policy.perCycleCost / policy.batchSize;
  }

  async function spendOn(moment: number): Promise<Spend> {
    const [day, from, until] = dayWindow(moment);
    const rows = await db.query(
      `SELECT run_id AS run, COALESCE(SUM(COALESCE(actual_cost, reserved_cost)), 0) AS charged
         FROM claims WHERE granted_at >= ? AND granted_at < ? GROUP BY run_id`,
      [from, until],
    );
    const byRun: Record<string, number> = {};
    let total = 0;
    for (const row of rows) {
      const charged = count(row["charged"]);
      total += charged;
      const run = text(row["run"]);
      byRun[run] = (byRun[run] ?? 0) + charged;
    }
    return { day, total, byRun };
  }

  async function openClaims(moment: number): Promise<number> {
    const rows = await db.query(
      `SELECT COUNT(*) AS open FROM claims WHERE finished_at IS NULL AND expires_at > ?`,
      [iso(moment)],
    );
    return count(rows[0]?.["open"]);
  }

  /**
   * The three independent bounds on one cycle's attention, in one place because every path that
   * claims work passes exactly these gates: the cycle batch, the per-cycle ceiling and the daily
   * ceiling. A second copy for any follow-up would be a second answer to "may this deployment
   * spend now", and the two would drift the first time a bound was tuned.
   */
  function admitSpend(
    policy: Policy,
    active: number,
    spentCycle: number,
    spentToday: number,
  ): Stop | null {
    const reserved = reservedCost(policy);
    if (active >= policy.batchSize) {
      return {
        reason: "batch",
        detail: `the cycle batch of ${String(policy.batchSize)} assignments is already claimed`,
      };
    }
    if (spentCycle + reserved > policy.perCycleCost) {
      return {
        reason: "per-cycle",
        detail:
          `per-cycle ceiling ${policy.perCycleCost.toFixed(4)} reached: ` +
          `${spentCycle.toFixed(4)} charged to this run today, ${reserved.toFixed(4)} reserved per review`,
      };
    }
    if (spentToday + reserved > policy.dailyCost) {
      return {
        reason: "daily",
        detail:
          `daily ceiling ${policy.dailyCost.toFixed(4)} reached: ` +
          `${spentToday.toFixed(4)} spent, ${reserved.toFixed(4)} reserved per review`,
      };
    }
    return null;
  }

  // -------------------------------------------------------------------------- candidate building

  /**
   * A scan read in pages. The engine bounds one call's rows (ADR 0034: a plugin pages past the
   * cap rather than the engine buffering), and a draw reads the WHOLE eligible set rather than a
   * page of it — the reserved lane picks the oldest-due across all of it — so the paging is here
   * and the candidate set is still complete. The order is the caller's own key, which makes the
   * pages disjoint.
   */
  async function scan(
    sql: string,
    order: string,
    params?: readonly GuestSqlParam[],
  ): Promise<readonly GuestSqlRow[]> {
    const out: GuestSqlRow[] = [];
    for (let offset = 0; ; offset += SCAN_PAGE) {
      const page = await db.query(
        `${sql} ORDER BY ${order} LIMIT ${String(SCAN_PAGE)} OFFSET ${String(offset)}`,
        params,
      );
      out.push(...page);
      if (page.length < SCAN_PAGE) return out;
    }
  }

  /**
   * The head of every record: the newest revision of each root. It is read through the
   * `records_by_root(root_id, seq)` index rather than as `NOT EXISTS (… supersedes_id = r.id)`,
   * which has no index behind it and would scan the whole table once per row — the head scan runs
   * on every draw, and a quadratic one over several thousand records is a cycle spent finding out
   * what work there is rather than doing it.
   */
  async function heads(): Promise<readonly Head[]> {
    const rows = await scan(
      `SELECT id, root_id, kind, created_at FROM (
         SELECT r.id AS id, r.root_id AS root_id, r.kind AS kind, r.created_at AS created_at,
                ROW_NUMBER() OVER (PARTITION BY r.root_id
                                   ORDER BY r.seq DESC, r.created_at DESC, r.id DESC) AS rn
           FROM records r
       ) WHERE rn = 1`,
      "root_id",
    );
    return rows.map((row) => ({
      id: text(row["id"]),
      rootId: text(row["root_id"]),
      kind: text(row["kind"]),
      createdAt: at(row["created_at"]),
    }));
  }

  async function statuses(): Promise<Map<string, { status: string; at: number }>> {
    const rows = await scan(
      `SELECT root, status, recorded_at FROM (
         SELECT r.root_id AS root, s.status AS status, s.recorded_at AS recorded_at,
                ROW_NUMBER() OVER (PARTITION BY r.root_id ORDER BY s.seq DESC, s.recorded_at DESC) AS rn
           FROM status_events s JOIN records r ON r.id = s.record_id
       ) WHERE rn = 1`,
      "root",
    );
    const out = new Map<string, { status: string; at: number }>();
    for (const row of rows) {
      out.set(text(row["root"]), { status: text(row["status"]), at: at(row["recorded_at"]) });
    }
    return out;
  }

  async function rulings(): Promise<Map<string, string>> {
    const rows = await scan(
      `SELECT root, disposition FROM (
         SELECT r.root_id AS root, d.disposition AS disposition,
                ROW_NUMBER() OVER (PARTITION BY r.root_id ORDER BY d.seq DESC, d.recorded_at DESC) AS rn
           FROM dispositions d JOIN records r ON r.id = d.record_id
       ) WHERE rn = 1`,
      "root",
    );
    const out = new Map<string, string>();
    for (const row of rows) out.set(text(row["root"]), text(row["disposition"]));
    return out;
  }

  /**
   * The live filings, as the rest of the hub reads them: the newest row per record and entity
   * decides, and a withdrawal kills it. A record is FILED when one of those is a non-heuristic
   * filing — including the recorded judgement that it is about nothing in particular, which took
   * it out of the backlog as surely as a topic would. A heuristic filing is one a run should
   * revisit, so it does not.
   */
  async function filings(): Promise<Map<string, { filed: boolean; topics: string[] }>> {
    const rows = await scan(
      `SELECT root, entity_id, heuristic FROM (
         SELECT r.root_id AS root, f.entity_id AS entity_id, f.heuristic AS heuristic,
                f.withdrawn AS withdrawn,
                ROW_NUMBER() OVER (PARTITION BY r.root_id, f.entity_id
                                   ORDER BY f.created_at DESC, f.id DESC) AS rn
           FROM filings f JOIN records r ON r.id = f.record_id
       ) WHERE rn = 1 AND withdrawn = 0`,
      "root, entity_id",
    );
    const out = new Map<string, { filed: boolean; topics: string[] }>();
    for (const row of rows) {
      const root = text(row["root"]);
      const entry = out.get(root) ?? { filed: false, topics: [] };
      if (count(row["heuristic"]) === 0) entry.filed = true;
      const entity = maybeText(row["entity_id"]);
      if (entity !== null && !entry.topics.includes(entity)) entry.topics.push(entity);
      out.set(root, entry);
    }
    return out;
  }

  /** The operator's stance per entity, read off the two facts §4.13 writes it as. A fact is in
   *  force when nothing supersedes it, its interval covers the moment, and its newest status is
   *  not one that took it out of force. */
  async function stances(moment: number): Promise<Map<string, Stance>> {
    const rows = await scan(
      `SELECT f.entity_id AS entity_id, f.predicate AS predicate, f.value AS value,
              f.valid_until AS valid_until, f.recorded_at AS recorded_at, f.id AS id,
              (SELECT st.status FROM fact_status st WHERE st.fact_id = f.id
                ORDER BY st.seq DESC LIMIT 1) AS status
         FROM facts f
        WHERE f.predicate IN ('lifecycle','analysis-policy')
          AND NOT EXISTS (SELECT 1 FROM facts later WHERE later.supersedes_id = f.id)`,
      "f.recorded_at, f.id",
    );
    const lifecycle = new Map<string, string>();
    const policy = new Map<string, string>();
    for (const row of rows) {
      const status = maybeText(row["status"]) ?? "active";
      if (status === "superseded" || status === "stale" || status === "proposed") continue;
      const until = maybeAt(row["valid_until"]);
      if (until !== null && until <= moment) continue;
      const entity = text(row["entity_id"]);
      const value = text(row["value"]);
      if (text(row["predicate"]) === "lifecycle") lifecycle.set(entity, value);
      else policy.set(entity, value);
    }
    const out = new Map<string, Stance>();
    for (const entity of new Set([...lifecycle.keys(), ...policy.keys()])) {
      // The analysis policy answers first: an excluded policy is the strongest thing the ledger
      // can say about expenditure, whatever the lifecycle says. Otherwise the lifecycle names the
      // stance — working, keep-an-eye and not-now differ in lifecycle and share their policy —
      // and an entity with neither fact is nobody having said, which is not a stance at all.
      if (policy.get(entity) === "excluded") {
        out.set(entity, "excluded");
        continue;
      }
      switch (lifecycle.get(entity)) {
        case "active":
          out.set(entity, "working");
          break;
        case "maintenance-only":
          out.set(entity, "watching");
          break;
        case "dormant":
          out.set(entity, "not-now");
          break;
        case "retired":
          out.set(entity, "retired");
          break;
        default:
          break;
      }
    }
    return out;
  }

  async function roleFacts(): Promise<Map<string, RoleFacts>> {
    const rows = await scan(
      `WITH heads AS (
         SELECT r.id AS id, r.root_id AS root_id FROM records r
          WHERE NOT EXISTS (SELECT 1 FROM records later WHERE later.supersedes_id = r.id)
       )
       SELECT h.root_id AS root, a.role AS role,
              COUNT(*) AS reviews,
              MAX(a.recorded_at) AS last_reviewed,
              SUM(CASE WHEN a.vote = 'support' THEN 1 ELSE 0 END) AS support,
              SUM(CASE WHEN a.vote = 'oppose' THEN 1 ELSE 0 END) AS oppose,
              SUM(CASE WHEN a.vote IS NOT NULL THEN 1 ELSE 0 END) AS voted,
              SUM(CASE WHEN a.revision_id <> h.id THEN 1 ELSE 0 END) AS on_older
         FROM assessments a
         JOIN records ar ON ar.id = a.record_id
         JOIN heads h ON h.root_id = ar.root_id
        WHERE NOT EXISTS (SELECT 1 FROM assessments later WHERE later.supersedes_id = a.id)
        GROUP BY h.root_id, a.role`,
      "root, role",
    );
    const out = new Map<string, RoleFacts>();
    for (const row of rows) {
      out.set(`${text(row["root"])}\u001f${text(row["role"])}`, {
        reviews: count(row["reviews"]),
        lastReviewed: maybeAt(row["last_reviewed"]),
        support: count(row["support"]),
        oppose: count(row["oppose"]),
        voted: count(row["voted"]),
        onOlder: count(row["on_older"]),
      });
    }
    return out;
  }

  async function claimFacts(moment: number): Promise<Map<string, ClaimFacts>> {
    const rows = await scan(
      `SELECT COALESCE(r.root_id, c.record_id) AS root, c.role AS role,
              SUM(CASE WHEN COALESCE(c.outcome, '') <> 'abandoned' THEN 1 ELSE 0 END) AS total,
              SUM(CASE WHEN c.finished_at IS NULL AND c.expires_at > ? THEN 1 ELSE 0 END) AS active,
              SUM(CASE WHEN COALESCE(c.outcome, '') IN ('skipped','failed') THEN 1 ELSE 0 END) AS setbacks,
              SUM(CASE WHEN COALESCE(c.outcome, '') = 'completed' THEN 1 ELSE 0 END) AS completed
         FROM claims c LEFT JOIN records r ON r.id = c.record_id
        GROUP BY COALESCE(r.root_id, c.record_id), c.role`,
      "root, role",
      [iso(moment)],
    );
    const out = new Map<string, ClaimFacts>();
    for (const row of rows) {
      out.set(`${text(row["root"])}\u001f${text(row["role"])}`, {
        total: count(row["total"]),
        active: count(row["active"]),
        setbacks: count(row["setbacks"]),
        completed: count(row["completed"]),
      });
    }
    return out;
  }

  const NO_CLAIMS: ClaimFacts = { total: 0, active: 0, setbacks: 0, completed: 0 };
  const NO_REVIEWS: RoleFacts = {
    reviews: 0,
    lastReviewed: null,
    support: 0,
    oppose: 0,
    voted: 0,
    onOlder: 0,
  };

  /**
   * Turns the store into drawable work, and explains every exclusion. The gaps are not
   * diagnostics for their own sake: an exhausted draw that could not say why leaves an operator
   * with a silent queue and no way to tell a satisfied deployment from a stuck one.
   */
  async function buildCandidates(
    policy: Policy,
    moment: number,
  ): Promise<{ candidates: Candidate[]; gaps: Gap[] }> {
    const [records, status, ruling, filed, stance, reviews, claims] = await Promise.all([
      heads(),
      statuses(),
      rulings(),
      filings(),
      stances(moment),
      roleFacts(),
      claimFacts(moment),
    ]);

    const candidates: Candidate[] = [];
    const gaps: Gap[] = [];
    let unfiledSeen = 0;
    let deferredSeen = 0;

    for (const head of records) {
      if ((ROLES_FOR_KIND[head.kind] ?? []).length === 0) continue;
      const filing = filed.get(head.rootId) ?? { filed: false, topics: [] };
      const topics = filing.topics;

      // The recorded restriction withholds the work and keeps the record: §4.8 and §5.2 both
      // refuse to delete a restricted subject, so this is a reported gap rather than a
      // disappearance, and rescinding the stance makes the same record drawable with nothing to
      // undo. The most restrictive topic decides; the most permissive would let one unrelated
      // entity unlock work on an excluded one.
      let withheld: { entity: string; reason: GapReason; stance: Stance } | null = null;
      let attention = 1;
      for (const entity of topics) {
        const state = stance.get(entity);
        if (state === undefined) continue;
        const refusal = WITHHOLDING[state];
        if (refusal !== undefined) withheld = { entity, reason: refusal, stance: state };
        attention = Math.min(attention, STANCE_WEIGHT[state]);
      }
      if (withheld !== null) {
        gaps.push({
          recordId: head.id,
          role: "",
          reason: withheld.reason,
          detail: `${withheld.entity} is ${withheld.stance}, so work filed under it is withheld`,
        });
        continue;
      }

      const roleClaims = (role: string): ClaimFacts =>
        claims.get(`${head.rootId}\u001f${role}`) ?? NO_CLAIMS;

      let assessments = 0;
      let materialChange = false;
      for (const role of ROLES_FOR_KIND[head.kind] ?? []) {
        const facts = reviews.get(`${head.rootId}\u001f${role}`) ?? NO_REVIEWS;
        assessments += facts.reviews;
        if (facts.onOlder > 0) materialChange = true;
      }
      const standing = ruling.get(head.rootId) ?? "";
      if (standing === "reopen" || standing === "refine") materialChange = true;

      // Filing is decided before the per-record review cap, and deliberately outside it: the cap
      // bounds how much judgement one record may collect, and where a record belongs is not a
      // judgement about it. A well-reviewed record nobody has filed is still invisible on a
      // surface organized by topic.
      if (!filing.filed) {
        unfiledSeen += 1;
        const held = roleClaims("filing");
        if (held.active > 0) {
          gaps.push({
            recordId: head.id,
            role: "filing",
            reason: "claimed",
            detail: "the filing is already claimed by a live worker",
          });
        } else if (held.completed > 0) {
          // A filing pass that filed the record or recorded no-topic took it out of this
          // backlog, so a completed filing on a record still listed here is the third outcome:
          // a topic proposal waiting on the operator. Drawing it again would pay to raise the
          // question the ledger already holds.
          gaps.push({
            recordId: head.id,
            role: "filing",
            reason: "settled",
            detail:
              "a filing pass already ran; the record stays unfiled until the topic it proposed is accepted",
          });
        } else if (held.setbacks >= MAX_SETBACKS) {
          gaps.push({
            recordId: head.id,
            role: "filing",
            reason: "exhausted",
            detail: `${String(held.setbacks)} filing skips or failures; bounded attention spent`,
          });
        } else {
          candidates.push({
            head,
            role: "filing",
            lane: "filing",
            // The record's own creation time: the filing backlog is cleared oldest first, so
            // the output that has been unfindable longest is named first.
            dueAt: head.createdAt,
            weight: 1,
            ordinal: held.total + 1,
            initial: false,
            untouched: false,
            revisit: false,
            filing: true,
            backlog: false,
            deferredAt: 0,
            topics,
          });
        }
      }

      const lifecycle = status.get(head.rootId);
      if (lifecycle !== undefined && lifecycle.status === "deferred") {
        deferredSeen += 1;
        const held = roleClaims("backlog");
        if (head.kind !== "hypothesis") {
          gaps.push({
            recordId: head.id,
            role: "backlog",
            reason: "unsupported",
            detail: `deferred, and only a hypothesis is worked from the backlog`,
          });
        } else if (held.active > 0) {
          gaps.push({
            recordId: head.id,
            role: "backlog",
            reason: "claimed",
            detail: "the backlog act is already claimed by a live worker",
          });
        } else if (held.completed > 0) {
          gaps.push({
            recordId: head.id,
            role: "backlog",
            reason: "settled",
            detail:
              "a backlog pass already ran; the candidate stays deferred until the act it proposed is accepted",
          });
        } else if (held.setbacks >= MAX_SETBACKS) {
          gaps.push({
            recordId: head.id,
            role: "backlog",
            reason: "exhausted",
            detail: `${String(held.setbacks)} backlog skips or failures; bounded attention spent`,
          });
        } else {
          candidates.push({
            head,
            role: "backlog",
            lane: "backlog",
            dueAt: head.createdAt,
            weight: 1,
            ordinal: held.total + 1,
            initial: false,
            untouched: false,
            revisit: false,
            filing: false,
            backlog: true,
            deferredAt: lifecycle.at,
            topics,
          });
        }
      }

      if (lifecycle !== undefined && (lifecycle.status === "superseded" || lifecycle.status === "retired")) {
        gaps.push({
          recordId: head.id,
          role: "",
          reason: "replaced",
          detail: `${lifecycle.status}, so no review of it is outstanding`,
        });
        continue;
      }
      if (assessments >= policy.maxItemReviews) {
        gaps.push({
          recordId: head.id,
          role: "",
          reason: "capped",
          detail: `the per-record cap of ${String(policy.maxItemReviews)} reviews is reached`,
        });
        continue;
      }

      for (const role of ROLES_FOR_KIND[head.kind] ?? []) {
        const facts = reviews.get(`${head.rootId}\u001f${role}`) ?? NO_REVIEWS;
        const held = roleClaims(role);
        // A revisit is not an outstanding obligation: reception on a well-reviewed open idea,
        // past its cooldown. Only the exploration share draws one, so it can never displace due
        // work — and its existence is why an empty backlog is not the same as nothing to sample.
        let revisit = false;
        if (facts.reviews >= policy.initialReviews && !materialChange) {
          if (role !== "reception" || standing === "accept" || standing === "reject" || standing === "duplicate") continue;
          revisit = true;
        }
        if (held.active > 0) {
          gaps.push({
            recordId: head.id,
            role,
            reason: "claimed",
            detail: "already claimed by a live worker",
          });
          continue;
        }
        if (held.setbacks >= MAX_SETBACKS) {
          gaps.push({
            recordId: head.id,
            role,
            reason: "exhausted",
            detail: `${String(held.setbacks)} skips or failures; bounded attention spent and reported as a gap rather than a negative vote`,
          });
          continue;
        }
        const resting = cooling(facts, role, policy, materialChange, moment);
        if (resting !== null) {
          gaps.push({ recordId: head.id, role, reason: "cooling", detail: resting });
          continue;
        }
        candidates.push({
          head,
          role,
          lane: null,
          dueAt: facts.lastReviewed ?? head.createdAt,
          weight: weigh(facts, role, policy, materialChange, attention, head, moment),
          ordinal: held.total + 1,
          initial: facts.reviews === 0,
          untouched: assessments === 0,
          revisit,
          filing: false,
          backlog: false,
          deferredAt: 0,
          topics,
        });
      }
    }

    // What the two work shares owe an operator who asks why they did not spend. An empty backlog
    // is the good outcome rather than a stuck one, and "nothing was drawn" and "nothing needed
    // drawing" are the two states a scheduler must never confuse.
    if (policy.filingShare > 0 && unfiledSeen === 0) {
      gaps.push({
        recordId: "",
        role: "filing",
        reason: "empty",
        detail: "every open record carries a filing, so the filing share has nothing to draw",
      });
    }
    if (policy.backlogShare > 0 && deferredSeen === 0) {
      gaps.push({
        recordId: "",
        role: "backlog",
        reason: "empty",
        detail: "nothing is deferred, so the backlog share has nothing to draw",
      });
    }

    sortCandidates(candidates);
    gaps.sort((a, b) =>
      a.recordId === b.recordId
        ? a.role === b.role
          ? a.reason.localeCompare(b.reason)
          : a.role.localeCompare(b.role)
        : a.recordId.localeCompare(b.recordId),
    );
    return { candidates, gaps };
  }

  /**
   * Whether a settled opinion is still resting. Two escapes, both deliberate: a material change
   * clears the cooldown, because a cooldown that survived one would freeze an opinion against a
   * situation that no longer holds; and a role with no reviews at all never cools, because there
   * is no opinion to have settled.
   */
  function cooling(
    facts: RoleFacts,
    role: Role,
    policy: Policy,
    materialChange: boolean,
    moment: number,
  ): string | null {
    if (facts.reviews === 0 || facts.lastReviewed === null) return null;
    if (materialChange) return null;
    const elapsed = (moment - facts.lastReviewed) / 1000;
    if (elapsed >= policy.cooldownSeconds) return null;
    if (role === "reception" && facts.reviews >= policy.initialReviews && facts.voted > 0) {
      const margin = Math.abs(facts.support - facts.oppose) / facts.voted;
      if (margin >= SETTLED_MARGIN) {
        return `reception is settled at ${(margin * 100).toFixed(0)}% one-sided; resting for another ${restFor(policy.cooldownSeconds - elapsed)}`;
      }
    }
    if (facts.reviews >= policy.initialReviews) {
      return `reassessed ${restFor(elapsed)} ago; resting for another ${restFor(policy.cooldownSeconds - elapsed)}`;
    }
    return null;
  }

  function restFor(seconds: number): string {
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${String(minutes)}m`;
    const hours = Math.floor(minutes / 60);
    return hours < 48 ? `${String(hours)}h` : `${String(Math.floor(hours / 24))}d`;
  }

  /**
   * A candidate's relative preference. Every factor is a multiplier and every multiplier is
   * positive, so no candidate can be weighted to zero and silently excluded — an exclusion is a
   * filter with a stated reason, never a weight that rounds away. Lightly reviewed work is
   * favoured; settled reception is damped at both extremes; a material change doubles it; the
   * operator's recorded stance on the topic raises or damps it; and age raises it up to the
   * overdue threshold, after which more age adds nothing, because past due is past due and the
   * reserved coverage lane is what actually clears it.
   */
  function weigh(
    facts: RoleFacts,
    role: Role,
    policy: Policy,
    materialChange: boolean,
    attention: number,
    head: Head,
    moment: number,
  ): number {
    let weight = 1 / (1 + facts.reviews);
    if (role === "reception" && facts.voted > 0) {
      weight *= 1 - 0.75 * (Math.abs(facts.support - facts.oppose) / facts.voted);
    }
    if (materialChange) weight *= MATERIAL_CHANGE_WEIGHT;
    weight *= attention;
    const reference = facts.lastReviewed ?? head.createdAt;
    const age = (moment - reference) / 1000;
    if (age > 0 && policy.overdueSeconds > 0) {
      weight *= 1 + Math.min(age / policy.overdueSeconds, 1);
    }
    // A weight damped close to zero by a settled margin stays drawable: a settled opinion is
    // resting rather than permanently closed.
    return Math.max(weight, 1e-6);
  }

  /** A total order for the samplers to walk, so one seed produces one draw on every instance:
   *  oldest due first, then kind, id and role. It is not a preference order. */
  function sortCandidates(candidates: Candidate[]): void {
    candidates.sort((a, b) => {
      if (a.dueAt !== b.dueAt) return a.dueAt - b.dueAt;
      if (a.head.kind !== b.head.kind) return a.head.kind.localeCompare(b.head.kind);
      if (a.head.id !== b.head.id) return a.head.id.localeCompare(b.head.id);
      return a.role.localeCompare(b.role);
    });
  }

  /** A candidate drawn to be acted on rather than judged: a record to be named, or a deferred
   *  candidate to be settled. One predicate, because every review lane owes both the same
   *  exclusion and two tests that had to be kept in step is how one comes to be forgotten. */
  function work(candidate: Candidate): boolean {
    return candidate.filing || candidate.backlog;
  }

  /**
   * The lane comes from one uniform draw against the policy's cumulative shares, which is what
   * makes the reservations actual reservations: over many cycles the coverage lane receives its
   * share whatever the weights say, and the exploration share is spent on a uniform pick no
   * weight can capture. Lanes fall through in a fixed order when the chosen one is empty —
   * an empty coverage lane means every initial review is done, which is a reason to spend the
   * share on weighted work and not a reason to idle. The two work lanes are last in every review
   * order and the review lanes last in theirs, so the three backlogs cover for each other only
   * when one of them is genuinely empty.
   */
  function sample(
    candidates: readonly Candidate[],
    policy: Policy,
    stream: Stream,
  ): { lane: Lane; chosen: Candidate } | null {
    const roll = stream.float();
    const coverageEdge = policy.coverageShare;
    const discoveryEdge = coverageEdge + policy.discoveryShare;
    const explorationEdge = discoveryEdge + policy.explorationShare;
    const filingEdge = explorationEdge + policy.filingShare;
    const backlogEdge = filingEdge + policy.backlogShare;

    let order: readonly Lane[] = [
      "weighted",
      "coverage",
      "discovery",
      "exploration",
      "filing",
      "backlog",
    ];
    if (roll < coverageEdge) {
      order = ["coverage", "discovery", "weighted", "exploration", "filing", "backlog"];
    } else if (roll < discoveryEdge) {
      order = ["discovery", "coverage", "weighted", "exploration", "filing", "backlog"];
    } else if (roll < explorationEdge) {
      order = ["exploration", "weighted", "coverage", "discovery", "filing", "backlog"];
    } else if (roll < filingEdge) {
      order = ["filing", "backlog", "weighted", "coverage", "discovery", "exploration"];
    } else if (roll < backlogEdge) {
      order = ["backlog", "filing", "weighted", "coverage", "discovery", "exploration"];
    }
    for (const lane of order) {
      const chosen = pick(candidates, lane, stream);
      if (chosen !== null) return { lane, chosen };
    }
    return null;
  }

  /**
   * One candidate from one lane. Coverage and discovery are deterministic — the oldest due and
   * the oldest untouched — because a reservation whose target was chosen at random would not
   * reliably clear the backlog it exists to clear; filing and the backlog are deterministic for
   * the same reason, oldest unfiled first and longest deferred first. Exploration is uniform.
   * Weighted is a cumulative-weight sample, the only lane where a higher weight means a higher
   * probability rather than a guarantee. Every review lane skips a work candidate and each work
   * lane draws nothing else: a record drawn to be named must never arrive at a reviewer.
   */
  function pick(candidates: readonly Candidate[], lane: Lane, stream: Stream): Candidate | null {
    let best: Candidate | null = null;
    switch (lane) {
      case "coverage":
        for (const candidate of candidates) {
          if (work(candidate) || candidate.revisit || !candidate.initial) continue;
          if (best === null || candidate.dueAt < best.dueAt) best = candidate;
        }
        return best;
      case "discovery":
        for (const candidate of candidates) {
          if (work(candidate) || candidate.revisit || !candidate.untouched) continue;
          if (best === null || candidate.dueAt < best.dueAt) best = candidate;
        }
        return best;
      case "filing":
        for (const candidate of candidates) {
          if (!candidate.filing) continue;
          if (best === null || candidate.dueAt < best.dueAt) best = candidate;
        }
        return best;
      case "backlog":
        // By when each was set down rather than by the record's age: the backlog is cleared in
        // the order it accumulated.
        for (const candidate of candidates) {
          if (!candidate.backlog) continue;
          if (best === null || candidate.deferredAt < best.deferredAt) best = candidate;
        }
        return best;
      case "exploration": {
        // The only lane that draws a revisit, and it draws uniformly over everything eligible:
        // an exploration share spent by weight would be the weighted lane under another name.
        const eligible = candidates.filter((candidate) => !work(candidate));
        if (eligible.length === 0) return null;
        return eligible[stream.below(eligible.length)] ?? null;
      }
      case "challenge":
        // Not a reservation of its own: a challenge is accounted to its lane once drawn.
        return null;
      case "weighted": {
        // Outstanding obligations only. A revisit is not backlog, so admitting it here would let
        // well-reviewed open ideas compete with due work for the paid share.
        const eligible = candidates.filter((candidate) => !work(candidate) && !candidate.revisit);
        let total = 0;
        for (const candidate of eligible) total += candidate.weight;
        if (total <= 0) return null;
        let target = stream.float() * total;
        for (const candidate of eligible) {
          target -= candidate.weight;
          if (target <= 0) return candidate;
        }
        return eligible[eligible.length - 1] ?? null;
      }
    }
  }

  // -------------------------------------------------------------------------- the draw

  async function draw(request: DrawRequest): Promise<DrawResult> {
    const moment = request.now ?? now();
    const inForce = await policyInForce();
    const policy = inForce.policy;

    const refusal = validatePolicy(policy);
    if (refusal !== null) {
      return { outcome: "gap", gap: { reason: "invalid-policy", detail: refusal }, gaps: [] };
    }
    if (!policy.enabled) {
      return {
        outcome: "gap",
        gap: {
          reason: "disabled",
          detail: `policy ${policy.version} has authorized evaluation disabled`,
        },
        gaps: [],
      };
    }

    const [active, spent] = await Promise.all([openClaims(moment), spendOn(moment)]);
    const overspent = admitSpend(policy, active, spent.byRun[request.runId] ?? 0, spent.total);
    if (overspent !== null) return { outcome: "gap", gap: overspent, gaps: [] };

    const { candidates, gaps } = await buildCandidates(policy, moment);
    if (candidates.length === 0) {
      return {
        outcome: "gap",
        gap: {
          reason: "no-candidates",
          detail:
            "no eligible review remains: every obligation is satisfied, in cooldown, capped, or withheld by a recorded stance",
        },
        gaps,
      };
    }

    const inputDigest = digest(
      candidates.map((candidate) => `${candidate.head.id}:${candidate.role}:${String(candidate.ordinal)}`),
    );
    const seed =
      request.seed ?? BigInt(`0x${digest([request.runId, String(moment), inputDigest])}`);
    const sampled = sample(candidates, policy, new Stream(seed));
    if (sampled === null) {
      return {
        outcome: "gap",
        gap: { reason: "no-lane", detail: "no lane could be satisfied from the eligible set" },
        gaps,
      };
    }

    // A challenge, a filing and a backlog act are accounted to their own lane whichever
    // reservation drew them, so a cycle can say how much went to arguing about disagreement, to
    // deciding what a record is about, and to working through what was deferred.
    const chosen = sampled.chosen;
    const lane: Lane =
      chosen.lane ?? (chosen.role === "challenge" ? "challenge" : sampled.lane);

    return {
      outcome: "assignment",
      assignment: {
        id: `asg_${digest([chosen.head.id, chosen.role, policy.version, String(chosen.ordinal)])}`,
        recordId: chosen.head.id,
        rootId: chosen.head.rootId,
        kind: chosen.head.kind,
        role: chosen.role,
        lane,
        policyVersion: policy.version,
        ordinal: chosen.ordinal,
        seed: seed.toString(),
        inputDigest,
        reservedCost: reservedCost(policy),
        drawnAt: moment,
        topics: chosen.topics,
      },
      gaps,
    };
  }

  // -------------------------------------------------------------------------- claims

  const CLAIM_COLUMNS = `id, record_id, role, lane, policy_version, job_id, run_id, fence,
    reserved_cost, actual_cost, granted_at, expires_at, finished_at, outcome`;

  function toClaim(row: GuestSqlRow): Claim {
    return {
      id: text(row["id"]),
      recordId: text(row["record_id"]),
      role: text(row["role"]),
      lane: text(row["lane"]),
      policyVersion: text(row["policy_version"]),
      jobId: maybeText(row["job_id"]),
      runId: text(row["run_id"]),
      fence: count(row["fence"]),
      reservedCost: count(row["reserved_cost"]),
      actualCost: maybeCount(row["actual_cost"]),
      grantedAt: at(row["granted_at"]),
      expiresAt: at(row["expires_at"]),
      finishedAt: maybeAt(row["finished_at"]),
      outcome: maybeText(row["outcome"]),
    };
  }

  async function readClaim(id: string): Promise<Claim | null> {
    const rows = await db.query(`SELECT ${CLAIM_COLUMNS} FROM claims WHERE id = ?`, [id]);
    const row = rows[0];
    return row === undefined ? null : toClaim(row);
  }

  /**
   * Grants one assignment against the ledger.
   *
   * The order matters. A live re-claim by the same run is the grant it already holds, returned
   * unchanged — a retry after a lost answer must not advance the fence or reserve a second time,
   * which would spend the day's allowance on a dropped packet. Only an expired authority may be
   * taken over, and a takeover archives the epoch it supersedes as its own finished row charged
   * at what it reserved, because an abandoned attempt may have burned the whole of it. The write
   * is one guarded batch: two workers racing for one assignment produce one winner and one
   * conflict, because the loser's guard no longer matches the fence it read.
   */
  async function claim(request: ClaimRequest): Promise<ClaimResult> {
    const moment = request.now ?? now();
    const assignment = request.assignment;
    if (request.runId === "") {
      return { outcome: "refused", refusal: { reason: "invalid", detail: "a claim names no run" } };
    }
    const policy = (await policyInForce()).policy;
    if (policy.leaseSeconds <= 0) {
      return {
        outcome: "refused",
        refusal: {
          reason: "invalid",
          detail: `policy ${policy.version} grants no lease, so a claim could never expire`,
        },
      };
    }
    const expires = moment + policy.leaseSeconds * 1000;
    const existing = await readClaim(assignment.id);

    if (existing !== null) {
      if (existing.recordId !== assignment.recordId || existing.role !== assignment.role) {
        return {
          outcome: "refused",
          refusal: {
            reason: "conflict",
            detail: `assignment ${assignment.id} already names ${existing.recordId} in the ${existing.role} role`,
          },
        };
      }
      if (existing.finishedAt !== null) {
        return {
          outcome: "refused",
          refusal: {
            reason: "finished",
            detail: `assignment ${assignment.id} was finished by run ${existing.runId} at fence ${String(existing.fence)}`,
          },
        };
      }
      if (existing.expiresAt > moment) {
        if (existing.runId === request.runId) return { outcome: "granted", claim: existing };
        return {
          outcome: "refused",
          refusal: {
            reason: "conflict",
            detail: `assignment ${assignment.id} is held by run ${existing.runId} until ${iso(existing.expiresAt)}`,
          },
        };
      }
    }

    // The day's charge is read before the fence advances, and it includes every reservation that
    // was never reconciled — expired or not.
    const spent = await spendOn(moment);
    if (spent.total + assignment.reservedCost > policy.dailyCost) {
      return {
        outcome: "refused",
        refusal: {
          reason: "budget",
          detail: `${spent.total.toFixed(4)} is already committed today and ${assignment.id} reserves ${assignment.reservedCost.toFixed(4)}, over the ${policy.dailyCost.toFixed(4)} daily allowance`,
        },
      };
    }
    const cycle = spent.byRun[request.runId] ?? 0;
    if (cycle + assignment.reservedCost > policy.perCycleCost) {
      return {
        outcome: "refused",
        refusal: {
          reason: "budget",
          detail: `run ${request.runId} has ${cycle.toFixed(4)} charged to ${spent.day} and ${assignment.id} reserves ${assignment.reservedCost.toFixed(4)}, over the ${policy.perCycleCost.toFixed(4)} a cycle may spend`,
        },
      };
    }

    const jobId: string | null = request.jobId ?? null;
    if (existing === null) {
      const rows = await db.batch([
        {
          sql: `INSERT INTO claims(${CLAIM_COLUMNS}) VALUES(?,?,?,?,?,?,?,1,?,NULL,?,?,NULL,NULL)
                ON CONFLICT(id) DO NOTHING RETURNING ${CLAIM_COLUMNS}`,
          params: [
            assignment.id,
            assignment.recordId,
            assignment.role,
            assignment.lane,
            assignment.policyVersion,
            jobId,
            request.runId,
            assignment.reservedCost,
            iso(moment),
            iso(expires),
          ],
        },
      ]);
      const row = rows[0]?.[0];
      if (row === undefined) {
        return {
          outcome: "refused",
          refusal: {
            reason: "conflict",
            detail: `assignment ${assignment.id} was claimed by another worker first`,
          },
        };
      }
      return { outcome: "granted", claim: toClaim(row) };
    }

    const rows = await db.batch([
      {
        // The superseded epoch stays charged, as its own finished row: an expired lease says
        // nothing about what it spent, so releasing it as zero would let a crash loop spend the
        // day's allowance many times over.
        sql: `INSERT INTO claims(${CLAIM_COLUMNS})
              SELECT c.id || '~' || CAST(c.fence AS TEXT), c.record_id, c.role, c.lane,
                     c.policy_version, c.job_id, c.run_id, c.fence, c.reserved_cost,
                     c.reserved_cost, c.granted_at, c.expires_at, ?, 'abandoned'
                FROM claims c
               WHERE c.id = ? AND c.fence = ? AND c.finished_at IS NULL
              ON CONFLICT(id) DO NOTHING`,
        params: [iso(moment), assignment.id, existing.fence],
      },
      {
        sql: `UPDATE claims SET run_id = ?, job_id = ?, lane = ?, policy_version = ?,
                 fence = fence + 1, reserved_cost = ?, actual_cost = NULL, granted_at = ?,
                 expires_at = ?, finished_at = NULL, outcome = NULL
               WHERE id = ? AND fence = ? AND finished_at IS NULL AND expires_at <= ?
               RETURNING ${CLAIM_COLUMNS}`,
        params: [
          request.runId,
          jobId,
          assignment.lane,
          assignment.policyVersion,
          assignment.reservedCost,
          iso(moment),
          iso(expires),
          assignment.id,
          existing.fence,
          iso(moment),
        ],
      },
    ]);
    const row = rows[1]?.[0];
    if (row === undefined) {
      return {
        outcome: "refused",
        refusal: {
          reason: "conflict",
          detail: `assignment ${assignment.id} moved past fence ${String(existing.fence)} before this takeover`,
        },
      };
    }
    return { outcome: "granted", claim: toClaim(row) };
  }

  /**
   * Extends a live claim's lease to one full policy lease from now.
   *
   * The refusals are the ones an authority check makes and deliberately not a finish's: an
   * extension is permission to start something NEW, so an expired claim is refused rather than
   * resurrected — the next claimer may have taken it already, and two live opinions on one
   * assignment is what the fence exists to prevent. The expiry never moves backwards: a renewal
   * is the holder keeping the authority it has, so a policy whose lease has since been shortened
   * governs the next claim rather than cutting short a window already granted.
   */
  async function renew(request: RenewRequest): Promise<RenewResult> {
    const moment = request.now ?? now();
    const policy = (await policyInForce()).policy;
    if (policy.leaseSeconds <= 0) {
      return {
        outcome: "refused",
        refusal: {
          reason: "invalid",
          detail: `policy ${policy.version} grants no lease, so a claim could never expire`,
        },
      };
    }
    const held = await readClaim(request.id);
    if (held === null) {
      return {
        outcome: "refused",
        refusal: { reason: "not-found", detail: `no assignment ${request.id}` },
      };
    }
    if (held.finishedAt !== null) {
      return {
        outcome: "refused",
        refusal: {
          reason: "finished",
          detail: `assignment ${request.id} was finished by run ${held.runId} at fence ${String(held.fence)}, so there is no lease left to extend`,
        },
      };
    }
    if (held.runId !== request.runId || held.fence !== request.fence) {
      return {
        outcome: "refused",
        refusal: {
          reason: "taken-over",
          detail: `assignment ${request.id} is held by run ${held.runId} at fence ${String(held.fence)}, not by ${request.runId} at fence ${String(request.fence)}`,
        },
      };
    }
    if (held.expiresAt <= moment) {
      return {
        outcome: "refused",
        refusal: {
          reason: "expired",
          detail: `the lease on assignment ${request.id} expired at ${iso(held.expiresAt)}`,
        },
      };
    }
    const extended = Math.max(moment + policy.leaseSeconds * 1000, held.expiresAt);
    if (extended === held.expiresAt) return { outcome: "renewed", expiresAt: held.expiresAt };
    const rows = await db.batch([
      {
        sql: `UPDATE claims SET expires_at = ?
               WHERE id = ? AND run_id = ? AND fence = ? AND finished_at IS NULL
               RETURNING expires_at`,
        params: [iso(extended), request.id, request.runId, request.fence],
      },
    ]);
    const row = rows[0]?.[0];
    if (row === undefined) {
      return {
        outcome: "refused",
        refusal: {
          reason: "taken-over",
          detail: `assignment ${request.id} moved before the renewal landed`,
        },
      };
    }
    return { outcome: "renewed", expiresAt: at(row["expires_at"]) };
  }

  /**
   * Reconciles the reservation with what was spent.
   *
   * Expiry does not refuse it and a takeover does. A holder whose lease lapsed while the model
   * was still working really did spend that money and really did produce that work, so refusing
   * the finish would both understate the allowance and discard a finished review; but once
   * another holder has taken the claim, this one's result belongs to a superseded epoch whose
   * spend is already charged under its own fence. A cost over the reservation is recorded in full
   * and reported as an overrun, and the identical retry reports the identical overrun.
   */
  async function finish(request: FinishRequest): Promise<FinishResult> {
    const moment = request.now ?? now();
    if (!Number.isFinite(request.cost) || request.cost < 0) {
      return {
        outcome: "refused",
        refusal: {
          reason: "invalid",
          detail: `a finish must report finite non-negative spend, got ${String(request.cost)}`,
        },
      };
    }
    const held = await readClaim(request.id);
    if (held === null) {
      return {
        outcome: "refused",
        refusal: { reason: "not-found", detail: `no assignment ${request.id}` },
      };
    }
    if (held.finishedAt !== null) {
      if (
        held.runId === request.runId &&
        held.fence === request.fence &&
        held.actualCost === request.cost
      ) {
        return {
          outcome: "finished",
          cost: request.cost,
          reserved: held.reservedCost,
          overrun: request.cost > held.reservedCost,
        };
      }
      return {
        outcome: "refused",
        refusal: {
          reason: "finished",
          detail: `assignment ${request.id} was finished by run ${held.runId} at fence ${String(held.fence)} for ${String(held.actualCost)}, and an identical receipt is the only one accepted twice`,
        },
      };
    }
    if (held.runId !== request.runId || held.fence !== request.fence) {
      return {
        outcome: "refused",
        refusal: {
          reason: "taken-over",
          detail: `assignment ${request.id} has been taken over by run ${held.runId} at fence ${String(held.fence)}, so the result from ${request.runId} at fence ${String(request.fence)} is refused`,
        },
      };
    }
    const rows = await db.batch([
      {
        sql: `UPDATE claims SET finished_at = ?, actual_cost = ?, outcome = ?
               WHERE id = ? AND run_id = ? AND fence = ? AND finished_at IS NULL
               RETURNING reserved_cost`,
        params: [iso(moment), request.cost, request.outcome, request.id, request.runId, request.fence],
      },
    ]);
    const row = rows[0]?.[0];
    if (row === undefined) {
      return {
        outcome: "refused",
        refusal: {
          reason: "taken-over",
          detail: `assignment ${request.id} moved before the finish landed`,
        },
      };
    }
    const reserved = count(row["reserved_cost"]);
    return {
      outcome: "finished",
      cost: request.cost,
      reserved,
      overrun: request.cost > reserved,
    };
  }

  return {
    policy: policyInForce,
    draw,
    claim,
    renew,
    finish,
    spend: async (moment?: number) => spendOn(moment ?? now()),
  };
}
