// Synthetic fixtures and routes for issue #219's evaluation surface.
//
// Everything here is generated preview data: no real transcript, credential,
// path, or analysis output appears. The awkward cases are deliberate, because
// they are the ones the interface has to render honestly before it can be
// trusted with a real corpus:
//
//   - a bare vote with no prose at all, beside a contribution with no vote;
//   - a record nobody has reviewed, which must not render as unopposed;
//   - a role with no evaluator, which must not render as a pass;
//   - a role that is supported but not yet required, which must not render as
//     overdue work;
//   - a verified outcome with a later contradiction, which must not collapse
//     into one success badge;
//   - two remedies for one problem, grouped and still separately decided;
//   - a revision that has been superseded since it was reviewed;
//   - a Reconsider item raised by changed evidence on a rejected record;
//   - enough rows to page, so the snapshot contract is observable;
//   - hostile content in every model-authored string.
//
// Mutations are stateful in memory, so the policy form and the three operator
// controls are exercisable end to end. MOCK_EVALUATION=empty presents the
// day-one state; =degraded presents a stale projection that still answers.

import type {
  EvaluationArtifact,
  EvaluationCoverage,
  EvaluationCoverageCounts,
  EvaluationItem,
  EvaluationPolicy,
  EvaluationRecord,
  EvaluationRoleCoverage,
  EvaluationSubject,
  EvidenceRef,
} from "../src/api";
import { HOSTILE_CONTROL, HOSTILE_HTML, HOSTILE_MARKDOWN, UNBROKEN_TOKEN } from "./phaseb";

const mode = Bun.env.MOCK_EVALUATION ?? "rich";
const empty = mode === "empty";
const degraded = mode === "degraded";

// The vocabularies the Go surface serves. They are stated here because the
// mock is the server's half of the contract: a fixture that derived them from
// the client's own types could never disagree with the client, which is the
// one disagreement a preview exists to surface.
const SORTS = ["recommended", "recent", "strengthened", "contested", "unreviewed", "overdue", "reconsider"];
const LANES = [
  "open", "accepted", "deferred", "rejected", "duplicate", "refine-requested",
  "implemented", "verified", "partial", "contradicted", "unverifiable", "reconsider",
];
const COVERAGE_STATES = ["unreviewed", "reviewed", "due", "unsupported", "blocked", "not_applicable"];
const COVERAGE_FILTERS = [...COVERAGE_STATES, "overdue"];
const KINDS = ["hypothesis", "observation", "finding", "proposal", "evaluation"];
const ROLES = ["reception", "evidence", "challenge", "comparison", "outcome", "relevance"];
const FEEDBACK_REASONS = ["not-now", "wrong-problem", "wrong-remedy"];
const OPERATOR_KINDS = ["criteria", "feedback", "reconsider_decision"];
// The two acts a reconsideration decision may state. The mock refuses
// anything else on that kind, and refuses the kind with no act at all,
// because that is what internal/evaluation does: a preview that accepted a
// decision with no polarity would let the interface ship a control the real
// service rejects.
const RECONSIDER_DECISIONS = ["reopen", "retain"];

// Which roles apply to which kind, mirroring internal/evaluation's
// RolesForKind. A bounded meta-review of an evaluation record carries
// reception and challenge only: nothing recursively demands an outcome
// verification of a review.
const ROLES_FOR_KIND: Record<string, string[]> = {
  hypothesis: ["reception", "evidence", "challenge", "relevance"],
  observation: ["reception", "evidence"],
  finding: ["reception", "evidence", "challenge", "relevance"],
  proposal: ["reception", "evidence", "challenge", "comparison", "outcome", "relevance"],
  evaluation: ["reception", "challenge"],
};

const SNAPSHOT = "snap-2026-09-11T09-00-00Z";

function digest(seed: string): string {
  return seed.repeat(64).slice(0, 64);
}

function evidence(path: string, line: number, seed: string, note: string): EvidenceRef {
  return { locator: { path, line, byte_offset: line * 64, digest: digest(seed) }, note };
}

function iso(daysAgo: number, hour = 9): string {
  const base = Date.UTC(2026, 8, 11, hour, 0, 0) - daysAgo * 86_400_000;
  return new Date(base).toISOString();
}

// role builds one role row. `reason` is required for the three states that
// assert something beyond "nobody has looked", and the fixture enforces that
// itself so a preview cannot show a bare "not applicable".
function role(
  name: string,
  state: string,
  extras: Partial<EvaluationRoleCoverage> = {},
): EvaluationRoleCoverage {
  const needsReason = state === "unsupported" || state === "blocked" || state === "not_applicable";
  if (needsReason && !extras.reason) {
    throw new Error(`synthetic role coverage ${name}/${state} has no reason`);
  }
  return { role: name, state, reviews: 0, ...extras };
}

// notRequired is the supported-but-not-activated row: never reviewed, not
// overdue, and carrying the sentence that says what would make it due. It is
// the row most likely to be misread as a backlog item, which is why the mock
// always has several.
function notRequired(name: string, activates: string): EvaluationRoleCoverage {
  return { role: name, state: "unreviewed", reviews: 0, reason: `not currently required: ${activates}` };
}

interface Fixture {
  item: EvaluationItem;
  history: EvaluationRecord[];
}

function artifact(input: {
  kind: string;
  id: string;
  title: string;
  rootID?: string;
  headID?: string;
  createdDaysAgo: number;
  reviewStatus: string;
  evidence?: EvidenceRef[];
  currentWork?: boolean;
  pain?: number;
  allowance?: string;
  unknown?: string[];
  // The operator's acceptance criteria, and the record they came from.
  // criteriaID is deliberately separable from criteria: an artifact whose
  // criteria are not linked to an operator record is the case the interface
  // must not paper over with a context version.
  criteria?: Array<{ id: string; description: string }>;
  criteriaID?: string;
}): EvaluationArtifact {
  const subject: EvaluationSubject = { kind: input.kind, id: input.id };
  return {
    subject,
    root_id: input.rootID ?? input.id,
    head_id: input.headID ?? input.id,
    run_id: `run-${input.id}`,
    created_at: iso(input.createdDaysAgo),
    title: input.title,
    body: { synthetic: true },
    review_status: input.reviewStatus,
    status: input.reviewStatus,
    related: null,
    evidence: input.evidence ?? null,
    criteria: input.criteria ?? null,
    criteria_id: input.criteriaID ?? "",
    context: {
      version: "ctx-7",
      priority: 2,
      current_work: input.currentWork ?? false,
      pain: input.pain ?? 0,
      blocked: false,
      allowance: input.allowance ?? "normal",
      reasons: input.currentWork
        ? ["the operator recorded this repository as current work on 2026-09-08"]
        : null,
      evidence: null,
      unknown: input.unknown ?? null,
    },
    context_version: "ctx-7",
  };
}

function provenance(runID: string, blinded: boolean) {
  return {
    run_id: runID,
    model: "synthetic-reviewer-1",
    profile: "review",
    recipe: "babel-evaluates-its-output",
    recipe_version: 2,
    blinded,
    context_version: "ctx-7",
    consulted: null,
  };
}

function assessmentRecord(input: {
  id: string;
  subject: EvaluationSubject;
  daysAgo: number;
  vote?: string;
  outcome?: string;
  criteriaID?: string;
}): EvaluationRecord {
  return {
    id: input.id,
    kind: "assessment",
    subject: input.subject,
    assignment_id: `asg-${input.id}`,
    supersedes_id: "",
    actor_kind: "run",
    actor_id: `run-${input.id}`,
    created_at: iso(input.daysAgo),
    provenance: provenance(`run-${input.id}`, true),
    assessment: {
      vote: input.vote ?? "",
      contributions: null,
      outcome: input.outcome ?? "",
      criteria_id: input.criteriaID ?? "",
      results: null,
      environment: "",
      as_of: iso(input.daysAgo),
      uncertainty: "",
      context_version: "ctx-7",
    },
    criteria: null,
    reason: "",
    context: null,
    policy: null,
    related_id: "",
  };
}

// --------------------------------------------------------------------------
// The named fixtures. Each exists for one rendering case named in the header.
// --------------------------------------------------------------------------

// A proposal with a bare support vote and nothing else. It is the case §4.12
// cares most about: the page must render a vote with no prose and invent none.
const bareVoteSubject: EvaluationSubject = { kind: "proposal", id: "pro_bare-vote" };
const bareVote: Fixture = {
  item: {
    artifact: artifact({
      kind: "proposal",
      id: "pro_bare-vote",
      title: "Retry the transcript describe pass with a bounded backoff",
      createdDaysAgo: 6,
      reviewStatus: "new",
      currentWork: true,
      pain: 3,
      evidence: [
        evidence(
          "/home/demo/.omp/agent/sessions/synthetic-project/describe-loop.jsonl",
          118,
          "a",
          "the same describe attempt repeats eleven times in one session",
        ),
      ],
      // Criteria the proposal states about itself, with no operator criteria
      // record behind them: criteriaID is deliberately absent. Nothing may
      // stand in for that identity, so the page has to say the target is
      // unsettled rather than show the context version and read as settled.
      criteria: [
        { id: "c1", description: "the describe pass stops repeating within one session" },
        { id: "c2", description: "no session becomes slower as a result" },
      ],
    }),
    reception: { support: 2, oppose: 0, unsure: 0, reviews: 2, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 2, last_reviewed: iso(2) }),
      notRequired("evidence", "a reviewer recording an uncertainty, or counter-evidence on the record"),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      notRequired("comparison", "a second remedy recorded against the same problem"),
      notRequired("outcome", "your acceptance of this proposal"),
      role("relevance", "reviewed", { reviews: 1, last_reviewed: iso(2) }),
    ],
    lane: "open",
    score: 0.91,
    reasons: [
      "the operator recorded this repository as current work, and the pain it names is the one recorded against it",
    ],
    objections: null,
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [
    assessmentRecord({ id: "evr_bare-1", subject: bareVoteSubject, daysAgo: 3, vote: "support" }),
    assessmentRecord({ id: "evr_bare-2", subject: bareVoteSubject, daysAgo: 2, vote: "support" }),
  ],
};

// A hypothesis nobody has reviewed at all, old enough that its initial review
// is overdue. It must be findable regardless of score and must not render as
// unopposed.
const neverReviewed: Fixture = {
  item: {
    artifact: artifact({
      kind: "hypothesis",
      id: "hyp_never-reviewed",
      title: "Sandbox teardown may leave a stale lock on interrupted runs",
      createdDaysAgo: 96,
      reviewStatus: "new",
      unknown: ["whether the lock is ever observed outside the synthetic corpus"],
    }),
    reception: { support: 0, oppose: 0, unsure: 0, reviews: 0, skips: 0 },
    coverage: "unreviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "unreviewed", { overdue: true }),
      notRequired("evidence", "a reviewer recording an uncertainty"),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "open",
    score: 0.4,
    reasons: ["nothing has read this in 96 days, and its reserved initial review is overdue"],
    objections: null,
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [],
};

// A finding whose evidence check cannot run: this build registers no evaluator
// for the role. It is a gap with a name, never a pass.
const unsupported: Fixture = {
  item: {
    artifact: artifact({
      kind: "finding",
      id: "fnd_no-evaluator",
      title: `Consolidated: verification is reported rather than performed ${HOSTILE_HTML}`,
      createdDaysAgo: 31,
      reviewStatus: "new",
    }),
    reception: { support: 1, oppose: 1, unsure: 2, reviews: 4, skips: 3 },
    coverage: "unsupported",
    coverage_reason:
      "no evidence evaluator is registered for findings in this build, so the check has not run and is not claimed to have passed",
    review_coverage: [
      role("reception", "reviewed", { reviews: 4, last_reviewed: iso(5) }),
      role("evidence", "unsupported", {
        reason: "no evidence evaluator is registered for findings in this build",
      }),
      role("challenge", "due", { reviews: 1, overdue: true, last_reviewed: iso(20) }),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "open",
    score: 0.66,
    reasons: ["reception is split four ways and nothing has resolved it"],
    objections: [`the cited runs may have been interrupted rather than dishonest ${HOSTILE_MARKDOWN}`],
    would_change: ["an evidence check that reads the cited lines rather than the claim about them"],
    group: "",
    reconsider: false,
  },
  history: [
    assessmentRecord({ id: "evr_nof-1", subject: { kind: "finding", id: "fnd_no-evaluator" }, daysAgo: 9, vote: "support" }),
    {
      ...assessmentRecord({ id: "evr_nof-2", subject: { kind: "finding", id: "fnd_no-evaluator" }, daysAgo: 8, vote: "oppose" }),
      assessment: {
        vote: "oppose",
        contributions: [
          {
            kind: "argument",
            text: `The cited sessions were cancelled by the operator, which this reads as a false report. ${HOSTILE_CONTROL}`,
            evidence: [
              evidence("/home/demo/.omp/agent/sessions/synthetic-project/cancelled.jsonl", 44, "b", "the run was cancelled here"),
            ],
            alternatives: null,
            would_change: "a cited session that ran to completion and still reported an unperformed check",
          },
        ],
        outcome: "",
        criteria_id: "",
        results: null,
        environment: "",
        as_of: iso(8),
        uncertainty: "whether cancellation is distinguishable from the pattern in the archived bytes",
        context_version: "ctx-7",
      },
    },
    {
      id: "evr_nof-skip",
      kind: "attempt",
      subject: { kind: "finding", id: "fnd_no-evaluator" },
      assignment_id: "asg_nof-3",
      supersedes_id: "",
      actor_kind: "run",
      actor_id: "run-skip",
      created_at: iso(7),
      provenance: provenance("run-skip", true),
      reason: "",
      related_id: "",
      attempt: {
        assignment_id: "asg_nof-3",
        state: "skipped",
        reason: "the cited session is not materialized on this host",
        cost: 0,
        // A skip that ran nothing genuinely cost nothing, and says so.
        unpriced: false,
        recorded_at: iso(7),
      },
    },
    // An attempt the provider reported no price for. The reservation is
    // charged instead, because the alternative — recording unmeasured work as
    // free — is a budget that can be spent without ever showing a cost. It is
    // the case the interface has to render as a conservative charge rather
    // than as an observed zero.
    {
      id: "evr_nof-unpriced",
      kind: "attempt",
      subject: { kind: "finding", id: "fnd_no-evaluator" },
      assignment_id: "asg_nof-4",
      supersedes_id: "",
      actor_kind: "run",
      actor_id: "run-unpriced",
      created_at: iso(6),
      provenance: provenance("run-unpriced", true),
      reason: "",
      related_id: "",
      attempt: {
        assignment_id: "asg_nof-4",
        state: "failed",
        reason: "the provider returned no usage for the completed call",
        cost: 0.05,
        unpriced: true,
        recorded_at: iso(6),
      },
    },
  ],
};

// Two remedies for one problem, grouped for reading and decided separately.
// The comparison prefers one of them in a named context and mints no votes.
const groupA: Fixture = {
  item: {
    artifact: artifact({
      kind: "proposal",
      id: "pro_group-cache",
      title: "Cache describe results per source digest",
      createdDaysAgo: 12,
      reviewStatus: "new",
      pain: 2,
    }),
    reception: { support: 3, oppose: 1, unsure: 0, reviews: 4, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 4, last_reviewed: iso(4) }),
      role("evidence", "reviewed", { reviews: 1, last_reviewed: iso(4) }),
      role("challenge", "due", { reviews: 1, last_reviewed: iso(11) }),
      role("comparison", "reviewed", { reviews: 1, last_reviewed: iso(3) }),
      notRequired("outcome", "your acceptance of this proposal"),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "open",
    score: 0.74,
    reasons: ["the cheaper of two remedies for the same repeated describe pass"],
    objections: ["a cache keyed on the digest goes stale when the adapter changes"],
    would_change: ["a measurement showing the adapter changes more often than the corpus"],
    group: "repeated describe pass",
    reconsider: false,
  },
  history: [
    {
      ...assessmentRecord({ id: "evr_cmp-1", subject: { kind: "proposal", id: "pro_group-cache" }, daysAgo: 3 }),
      assessment: {
        vote: "",
        contributions: [
          {
            kind: "comparison",
            text: "Against the other remedy for this problem, caching is cheaper to abandon.",
            evidence: null,
            alternatives: [
              { kind: "proposal", id: "pro_group-cache" },
              { kind: "proposal", id: "pro_group-skip" },
            ],
            preferred: { kind: "proposal", id: "pro_group-cache" },
            would_change: "a corpus that changes faster than the adapter",
          },
        ],
        outcome: "",
        criteria_id: "",
        results: null,
        environment: "",
        as_of: iso(3),
        uncertainty: "",
        context_version: "ctx-7",
      },
    },
  ],
};

const groupB: Fixture = {
  item: {
    artifact: artifact({
      kind: "proposal",
      id: "pro_group-skip",
      title: "Skip describing sessions whose digest is unchanged",
      createdDaysAgo: 10,
      reviewStatus: "deferred",
      pain: 2,
    }),
    reception: { support: 1, oppose: 0, unsure: 1, reviews: 2, skips: 0 },
    coverage: "due",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 2, last_reviewed: iso(6) }),
      role("evidence", "due", { reviews: 0, overdue: true }),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      role("comparison", "reviewed", { reviews: 1, last_reviewed: iso(3) }),
      notRequired("outcome", "your acceptance of this proposal"),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "deferred",
    score: 0.51,
    reasons: null,
    objections: ["deferred by the operator on 2026-09-02, with no reason recorded"],
    would_change: null,
    group: "repeated describe pass",
    reconsider: false,
  },
  history: [],
};

// An accepted proposal, verified against a criterion version, and then
// contradicted by later evidence. Both assessments survive.
const verifiedSubject: EvaluationSubject = { kind: "proposal", id: "pro_verified-then-contradicted" };
const verified: Fixture = {
  item: {
    artifact: artifact({
      kind: "proposal",
      id: "pro_verified-then-contradicted",
      title: "Bound the transcript reader's record budget",
      createdDaysAgo: 64,
      reviewStatus: "accepted",
      // Resolved: the criteria came from the operator's own record below, and
      // both outcome assessments name that version.
      criteria: [
        { id: "c1", description: "no session read exceeds the record budget" },
        { id: "c2", description: "no session becomes unreadable as a result" },
      ],
      criteriaID: "evr_criteria-1",
    }),
    reception: { support: 5, oppose: 0, unsure: 1, reviews: 6, skips: 0 },
    coverage: "due",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 6, last_reviewed: iso(40) }),
      role("evidence", "reviewed", { reviews: 2, last_reviewed: iso(38) }),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      notRequired("comparison", "a second remedy recorded against the same problem"),
      role("outcome", "due", { reviews: 2, overdue: true, last_reviewed: iso(5) }),
      role("relevance", "reviewed", { reviews: 1, last_reviewed: iso(30) }),
    ],
    lane: "contradicted",
    score: 0.58,
    reasons: ["an outcome was verified here and later contradicted; the dispute is unresolved"],
    objections: ["the contradiction was observed in a different environment than the verification"],
    would_change: ["a verification in the environment the contradiction was seen in"],
    group: "",
    reconsider: false,
  },
  history: [
    {
      id: "evr_criteria-1",
      kind: "criteria",
      subject: verifiedSubject,
      assignment_id: "",
      supersedes_id: "",
      actor_kind: "operator",
      actor_id: "operator",
      created_at: iso(50),
      provenance: provenance("", false),
      criteria: [
        { id: "c1", description: "no session read exceeds the record budget" },
        { id: "c2", description: "no session becomes unreadable as a result" },
      ],
      reason: "settled after acceptance, so it is a later decision rather than the original target",
      related_id: "",
    },
    {
      ...assessmentRecord({
        id: "evr_verified",
        subject: verifiedSubject,
        daysAgo: 20,
        outcome: "verified",
        criteriaID: "evr_criteria-1",
      }),
      assessment: {
        vote: "",
        contributions: null,
        outcome: "verified",
        criteria_id: "evr_criteria-1",
        results: [
          {
            criterion_id: "c1",
            satisfied: true,
            evidence: [evidence("/home/demo/synthetic/budget-check.log", 3, "c", "no read exceeded the budget across 40 sessions")],
            uncertainty: "",
          },
          {
            criterion_id: "c2",
            satisfied: true,
            evidence: null,
            uncertainty: "only the synthetic corpus was read",
          },
        ],
        environment: "synthetic corpus, 40 sessions, single host",
        as_of: iso(20),
        uncertainty: "",
        context_version: "ctx-7",
      },
    },
    {
      ...assessmentRecord({
        id: "evr_contradicted",
        subject: verifiedSubject,
        daysAgo: 5,
        outcome: "contradicted",
        criteriaID: "evr_criteria-1",
      }),
      assessment: {
        vote: "",
        contributions: null,
        outcome: "contradicted",
        criteria_id: "evr_criteria-1",
        results: [
          {
            criterion_id: "c2",
            satisfied: false,
            evidence: [evidence("/home/demo/synthetic/large-session.log", 9, "d", `one session became unreadable ${UNBROKEN_TOKEN.slice(0, 64)}`)],
            uncertainty: "observed on one host only",
          },
        ],
        environment: "a second host with a larger corpus",
        as_of: iso(5),
        uncertainty: "whether the host or the corpus size is the difference",
        context_version: "ctx-7",
      },
    },
  ],
};

// A rejected record whose evidence has since changed, raising exactly one
// Reconsider item. The rejection stands until the operator reopens it.
const reconsiderSubject: EvaluationSubject = { kind: "hypothesis", id: "hyp_reconsider" };
const reconsider: Fixture = {
  item: {
    artifact: artifact({
      kind: "hypothesis",
      id: "hyp_reconsider",
      title: "Publication may silently defer past a run's declared closure",
      createdDaysAgo: 120,
      reviewStatus: "rejected",
    }),
    reception: { support: 1, oppose: 3, unsure: 0, reviews: 4, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 4, last_reviewed: iso(70) }),
      role("evidence", "reviewed", { reviews: 2, last_reviewed: iso(70) }),
      role("challenge", "reviewed", { reviews: 1, last_reviewed: iso(69) }),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "reconsider",
    score: 0.62,
    reasons: ["new evidence contradicts the basis of the rejection recorded on 2026-06-20"],
    objections: ["the original rejection also cited a design decision the new evidence does not touch"],
    would_change: null,
    group: "",
    reconsider: true,
  },
  history: [
    assessmentRecord({ id: "evr_rec-1", subject: reconsiderSubject, daysAgo: 71, vote: "oppose" }),
    {
      id: "evr_rec-raised",
      kind: "reconsider",
      subject: reconsiderSubject,
      assignment_id: "",
      supersedes_id: "",
      actor_kind: "run",
      actor_id: "run-sweep",
      created_at: iso(2),
      provenance: provenance("run-sweep", false),
      reason: "a session recorded on 2026-09-09 shows the deferral this was rejected for not being able to happen",
      related_id: "",
    },
  ],
};

// A superseded revision: reviewed at n, revised since. The endorsement must
// not move to n+1.
const superseded: Fixture = {
  item: {
    artifact: artifact({
      kind: "proposal",
      id: "pro_superseded-r1",
      title: "Redact secret-shaped values before indexing",
      rootID: "pro_superseded-r1",
      headID: "pro_superseded-r2",
      createdDaysAgo: 22,
      reviewStatus: "new",
    }),
    reception: { support: 3, oppose: 0, unsure: 0, reviews: 3, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 3, last_reviewed: iso(18) }),
      notRequired("evidence", "a reviewer recording an uncertainty"),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      notRequired("comparison", "a second remedy recorded against the same problem"),
      notRequired("outcome", "your acceptance of this proposal"),
      notRequired("relevance", "the operator recording work or pain touching this subject"),
    ],
    lane: "open",
    score: 0.47,
    reasons: null,
    objections: null,
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [
    assessmentRecord({ id: "evr_sup-1", subject: { kind: "proposal", id: "pro_superseded-r1" }, daysAgo: 18, vote: "support" }),
  ],
};

// An observation whose source this host cannot open, so its evidence check is
// blocked rather than failed.
const blocked: Fixture = {
  item: {
    artifact: artifact({
      kind: "observation",
      id: "obs_blocked-source",
      title: "A retry loop appears in a session this host never fetched",
      createdDaysAgo: 44,
      reviewStatus: "new",
    }),
    reception: { support: 0, oppose: 0, unsure: 0, reviews: 0, skips: 2 },
    coverage: "blocked",
    coverage_reason:
      "the cited session is not materialized on this host, so nothing here can read what the claim rests on",
    review_coverage: [
      role("reception", "blocked", {
        reason: "the cited session is not materialized on this host",
      }),
      role("evidence", "blocked", {
        reason: "the cited session is not materialized on this host",
      }),
    ],
    lane: "open",
    score: 0.2,
    reasons: null,
    objections: null,
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [],
};

// A bounded meta-review subject: an evaluation record reviewed as output in
// its own right, with outcome verification not applicable by named policy.
const meta: Fixture = {
  item: {
    artifact: artifact({
      kind: "evaluation",
      id: "evl_meta-review",
      title: "Assessment evr_bare-1 offered a vote with no stated basis",
      createdDaysAgo: 4,
      reviewStatus: "new",
    }),
    reception: { support: 0, oppose: 1, unsure: 0, reviews: 1, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 1, last_reviewed: iso(1) }),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
    ],
    lane: "open",
    score: 0.3,
    reasons: null,
    objections: ["a bare vote is a valid review, so this objection may be about the policy rather than the record"],
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [],
};

// Filler, so paging and the snapshot contract are observable. Generated,
// never copied: each carries a different coverage state so a page of rows
// exercises the whole vocabulary rather than one row of it.
const fillerStates = ["unreviewed", "reviewed", "due", "reviewed", "unreviewed"];
const fillerLanes = ["open", "accepted", "implemented", "verified", "refine-requested", "duplicate"];

function filler(index: number): Fixture {
  const kind = index % 3 === 0 ? "hypothesis" : index % 3 === 1 ? "proposal" : "finding";
  const state = fillerStates[index % fillerStates.length];
  const reviews = state === "unreviewed" ? 0 : (index % 4) + 1;
  return {
    item: {
      artifact: artifact({
        kind,
        id: `${kind.slice(0, 3)}_filler-${String(index).padStart(2, "0")}`,
        title: `Synthetic ${kind} ${index}: a generated record so the listing pages`,
        createdDaysAgo: 8 + index,
        reviewStatus: "new",
      }),
      reception: {
        support: reviews > 0 ? (index % 3) : 0,
        oppose: reviews > 1 ? 1 : 0,
        unsure: 0,
        reviews,
        skips: 0,
      },
      coverage: state,
      coverage_reason: "",
      review_coverage: ROLES_FOR_KIND[kind].map((name, position) =>
        position === 0
          ? role(name, state, { reviews, overdue: state === "unreviewed" && index % 5 === 0 })
          : notRequired(name, "a reviewer recording an uncertainty"),
      ),
      lane: fillerLanes[index % fillerLanes.length],
      score: 0.5 - index / 200,
      reasons: null,
      objections: null,
      would_change: null,
      group: "",
      reconsider: false,
    },
    history: [],
  };
}

// The one fixture whose identifier is deliberately shared with ./phaseb: it
// is the hypothesis that file's record pages render, so the cross-link from a
// record's own page to its evaluation lands on a real page rather than on a
// 404. Two fixture files describing one record is exactly what a deployment
// looks like — the frontier holds the record, the projection holds what was
// said about it — and the shared id is what makes the navigation between them
// checkable in a browser.
const crossLinked: Fixture = {
  item: {
    artifact: artifact({
      kind: "hypothesis",
      id: "hyp_unverified-closures",
      title: "Verification may be reported rather than performed",
      createdDaysAgo: 15,
      reviewStatus: "new",
      currentWork: true,
      pain: 2,
    }),
    reception: { support: 2, oppose: 1, unsure: 0, reviews: 3, skips: 0 },
    coverage: "reviewed",
    coverage_reason: "",
    review_coverage: [
      role("reception", "reviewed", { reviews: 3, last_reviewed: iso(4) }),
      role("evidence", "due", { reviews: 1, last_reviewed: iso(12) }),
      notRequired("challenge", "support and opposition both recorded after the initial reviews"),
      role("relevance", "reviewed", { reviews: 1, last_reviewed: iso(4) }),
    ],
    lane: "open",
    score: 0.69,
    reasons: ["the operator recorded this repository as current work"],
    objections: null,
    would_change: null,
    group: "",
    reconsider: false,
  },
  history: [
    assessmentRecord({
      id: "evr_cross-1",
      subject: { kind: "hypothesis", id: "hyp_unverified-closures" },
      daysAgo: 4,
      vote: "support",
    }),
  ],
};

const named: Fixture[] = [
  bareVote, neverReviewed, unsupported, groupA, groupB, verified, reconsider, superseded, blocked,
  meta, crossLinked,
];

const fixtures: Fixture[] = empty
  ? []
  : [...named, ...Array.from({ length: 24 }, (_, index) => filler(index))];

// The subject index. A string-keyed lookup built once from the fixtures
// above, so a detail read and a listing row cannot describe different
// records.
const byID: Record<string, Fixture | undefined> = Object.fromEntries(
  fixtures.map((fixture) => [fixture.item.artifact.subject.id, fixture]),
);

// --------------------------------------------------------------------------
// Mutable operator state. The three operator controls and the policy form
// append here, so the flows are exercisable end to end in one browser.
// --------------------------------------------------------------------------

const operatorRecords: EvaluationRecord[] = [];
let operatorCounter = 0;

let policy: EvaluationPolicy = {
  version: "eval-policy-1",
  enabled: false,
  cadence_seconds: 3600,
  overdue_seconds: 604_800,
  initial_reviews: 2,
  cooldown_seconds: 172_800,
  coverage_share: 0.4,
  exploration_share: 0.1,
  discovery_share: 0.1,
  max_item_reviews: 6,
  per_cycle_cost: 0.5,
  daily_cost: 4,
  lease_seconds: 900,
  batch_size: 4,
};
let policyVersion = 1;

// Claimed work in flight. It is a fixture rather than a derivation of
// `enabled`, because the whole point of the status is that running is observed
// and not inferred: MOCK_EVALUATION=running previews the state a paused
// deployment cannot reach by saving a form.
const active = Bun.env.MOCK_EVALUATION === "running" ? 2 : 0;

function counts(items: EvaluationItem[]): EvaluationCoverageCounts {
  const result: EvaluationCoverageCounts = {
    unreviewed: 0, reviewed: 0, due: 0, unsupported: 0, blocked: 0, not_applicable: 0, overdue: 0,
  };
  for (const item of items) {
    const key = item.coverage as keyof EvaluationCoverageCounts;
    if (key in result) result[key] += 1;
    if ((item.review_coverage ?? []).some((row) => row.overdue)) result.overdue += 1;
  }
  return result;
}

function roleCounts(): Record<string, EvaluationCoverageCounts> {
  const byRole: Record<string, EvaluationCoverageCounts> = {};
  for (const name of ROLES) {
    byRole[name] = {
      unreviewed: 0, reviewed: 0, due: 0, unsupported: 0, blocked: 0, not_applicable: 0, overdue: 0,
    };
  }
  for (const fixture of fixtures) {
    for (const row of fixture.item.review_coverage ?? []) {
      const bucket = byRole[row.role];
      if (!bucket) continue;
      const key = row.state as keyof EvaluationCoverageCounts;
      if (key in bucket) bucket[key] += 1;
      if (row.overdue) bucket.overdue += 1;
    }
  }
  return byRole;
}

function coverage(): EvaluationCoverage {
  const flat = counts(fixtures.map((fixture) => fixture.item));
  return {
    ...flat,
    active,
    // A deployment with no completed sweep has no last check and no next
    // draw. The empty strings are the honest shape: the page says "unknown"
    // rather than printing an instant nobody observed.
    last_check: empty ? "" : iso(0, 6),
    next_draw: policy.enabled && !empty
      ? new Date(Date.parse(iso(0, 6)) + policy.cadence_seconds * 1000).toISOString()
      : "",
    updated_at: degraded ? iso(3) : iso(0, 8),
    reason: degraded
      ? "the fleet source could not open 3 records, so these counts are short by an unknown amount"
      : "",
    by_role: roleCounts(),
  };
}

// The orderings. Each is deterministic and each is a different question, so a
// preview can show that the sort control changes the answer rather than the
// label.
const ORDERINGS: Record<string, (a: EvaluationItem, b: EvaluationItem) => number> = {
  recommended: (a, b) => b.score - a.score,
  recent: (a, b) => b.artifact.created_at.localeCompare(a.artifact.created_at),
  // Substantive contribution first. A bare vote does not move an item up
  // this order, which is what makes the sort different from `recent`.
  strengthened: (a, b) => strength(b) - strength(a),
  contested: (a, b) => contention(b) - contention(a),
  unreviewed: (a, b) => a.reception.reviews - b.reception.reviews,
  overdue: (a, b) => a.artifact.created_at.localeCompare(b.artifact.created_at),
  reconsider: (a, b) => Number(b.reconsider) - Number(a.reconsider) || b.score - a.score,
};

function strength(item: EvaluationItem): number {
  const fixture = byID[item.artifact.subject.id];
  const substantive = (fixture?.history ?? []).filter(
    (record) =>
      (record.assessment?.contributions ?? []).length > 0 ||
      record.kind === "criteria" ||
      Boolean(record.assessment?.outcome),
  );
  if (substantive.length === 0) return 0;
  const newest = substantive.reduce((latest, record) =>
    record.created_at > latest.created_at ? record : latest);
  return Date.parse(newest.created_at);
}

function contention(item: EvaluationItem): number {
  const { support, oppose, unsure } = item.reception;
  return Math.min(support, oppose) * 2 + unsure;
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

function vocabulary() {
  return {
    sorts: SORTS,
    lanes: LANES,
    coverage: COVERAGE_FILTERS,
    kinds: KINDS,
    roles: ROLES,
    feedback_reasons: FEEDBACK_REASONS,
    operator_kinds: OPERATOR_KINDS,
    reconsider_decisions: RECONSIDER_DECISIONS,
  };
}

function selected(url: URL): EvaluationItem[] {
  const kind = url.searchParams.get("kind") ?? "";
  const lane = url.searchParams.get("lane") ?? "";
  const state = url.searchParams.get("coverage") ?? "";
  const wanted = url.searchParams.get("role") ?? "";
  return fixtures
    .map((fixture) => fixture.item)
    .filter((item) => {
      if (kind && item.artifact.subject.kind !== kind) return false;
      if (lane && item.lane !== lane) return false;
      if (wanted && !(item.review_coverage ?? []).some((row) => row.role === wanted)) return false;
      if (!state) return true;
      if (state === "overdue") return (item.review_coverage ?? []).some((row) => row.overdue);
      if (wanted) {
        const row = (item.review_coverage ?? []).find((entry) => entry.role === wanted);
        return row?.state === state;
      }
      return item.coverage === state;
    });
}

// An operator record is prepended to the subject's history so a write is
// visible on the page that made it, exactly as a re-read of the real service
// would show it.
function historyOf(id: string): EvaluationRecord[] {
  const own = operatorRecords.filter((record) => record.subject.id === id);
  return [...(byID[id]?.history ?? []), ...own].sort((a, b) =>
    a.created_at.localeCompare(b.created_at));
}

// ModelReception is what §4.12's projection says about one record, in the
// shape the record peel serves it: the run-authored votes with their
// rationales, the tally over them, and whether they disagree.
//
// The operator never appears here. His stance is a feedback record on the
// authority side of the boundary and is carried by the peel separately, so
// nothing in this function can add a person's opinion to a tally of runs'.
export interface ModelReception {
  model: Array<{ actor: string; role: string; stance: string; rationale?: string; at: string }>;
  counts?: { support: number; oppose: number; unsure: number };
  contested?: boolean;
}

export function receptionOf(id: string): ModelReception | null {
  const fixture = byID[id];
  if (!fixture) return null;
  const votes = historyOf(id).filter((record) => record.kind === "assessment" && record.assessment?.vote);
  // Every assessment fixture here is a reception vote: the role a run was
  // assigned is carried by the assignment, not by the record it wrote, so the
  // mock states the one role its fixtures actually hold rather than
  // distributing the coverage table's roles over records that never had them.
  const model = votes.map((record) => ({
    actor: record.actor_id,
    role: "reception",
    stance: record.assessment?.vote ?? "",
    ...(record.reason ? { rationale: record.reason } : {}),
    at: record.created_at,
  }));
  const tally = fixture.item.reception;
  if (model.length === 0 && (!tally || tally.reviews === 0)) return null;
  return {
    model,
    ...(tally ? { counts: { support: tally.support, oppose: tally.oppose, unsure: tally.unsure } } : {}),
    // Contested is derived rather than stored: support and opposition both
    // recorded is what the word means, and internal/evaluation computes it
    // the same way from the same two numbers.
    ...(tally && tally.support > 0 && tally.oppose > 0 ? { contested: true } : {}),
  };
}

export async function evaluationResponse(request: Request, url: URL): Promise<Response | null> {
  if (!url.pathname.startsWith("/api/evaluation/")) return null;

  // A launch that could not open the evaluation projection refuses every one
  // of these routes, and that gate lives in serve.ts's routeServices table
  // beside every other Phase B service rather than here: one table means a
  // route cannot be simulated as unwired under a name the real server never
  // prints. MOCK_UNWIRED=evaluation is what reaches it.

  if (request.method === "GET" && url.pathname === "/api/evaluation/list") {
    const limit = Number(url.searchParams.get("limit") ?? 50);
    const offset = Math.max(0, Number(url.searchParams.get("offset") ?? 0));
    if (!Number.isFinite(limit) || limit <= 0 || limit > 200) {
      return json({ error: "limit must be between 1 and 200" }, 400);
    }
    const sort = url.searchParams.get("sort") ?? "";
    if (sort && !SORTS.includes(sort)) {
      return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
    }
    const order = ORDERINGS[sort || "recommended"];
    // The whole eligible set is ordered before the page is cut, which is the
    // property §8.5 asks for and the one a mock that sliced first would
    // quietly fail to preview.
    const items = selected(url).sort(order);
    const asked = url.searchParams.get("snapshot") ?? "";
    const pruned = asked !== "" && asked !== SNAPSHOT;
    return json({
      items: items.slice(offset, offset + limit),
      total: items.length,
      snapshot: SNAPSHOT,
      updated_at: degraded ? iso(3) : iso(0, 8),
      stale: degraded || pruned,
      unavailable: pruned
        ? "the ordering you were paging through has been rebuilt since, so this page is cut from the current one"
        : degraded
          ? "3 records could not be opened from the shared catalog, so this ordering is short by an unknown amount"
          : "",
      coverage: coverage(),
      query: {
        kind: url.searchParams.get("kind") ?? "",
        lane: url.searchParams.get("lane") ?? "",
        sort,
        role: url.searchParams.get("role") ?? "",
        coverage: url.searchParams.get("coverage") ?? "",
        limit,
        offset,
        snapshot: SNAPSHOT,
      },
      vocabulary: vocabulary(),
    });
  }

  if (request.method === "GET" && url.pathname === "/api/evaluation/detail") {
    const id = url.searchParams.get("id") ?? "";
    const kind = url.searchParams.get("kind") ?? "";
    if (!kind) return json({ error: "kind is required" }, 400);
    if (!id) return json({ error: "id is required" }, 400);
    const fixture = byID[id];
    if (!fixture || fixture.item.artifact.subject.kind !== kind) {
      return json({ error: "no evaluation subject or record with that identifier" }, 404);
    }
    const group = fixture.item.group;
    return json({
      item: fixture.item,
      history: historyOf(id),
      assignments: fixture.history
        .filter((record) => record.assignment_id)
        .map((record) => ({
          id: record.assignment_id,
          subject: record.subject,
          run_id: record.provenance.run_id,
          role: "reception",
          policy_version: policy.version,
          context_version: "ctx-7",
          seed: 42,
          input_digest: digest("e"),
          created_at: record.created_at,
          expires_at: record.created_at,
          fence: 1,
          reserved_cost: 0.05,
          lane: "coverage",
        })),
      alternatives: group
        ? fixtures
            .map((other) => other.item)
            .filter((other) => other.group === group && other.artifact.subject.id !== id)
        : [],
      vocabulary: vocabulary(),
      // Only the kinds the review surface has a page for carry a decision
      // link, so an observation or an evaluation record offers none.
      decisions: ["hypothesis", "finding", "proposal"].includes(kind) ? { type: kind, id } : { type: "", id: "" },
    });
  }

  if (request.method === "GET" && url.pathname === "/api/evaluation/coverage") {
    return json({ coverage: coverage(), kinds: KINDS, roles: ROLES });
  }

  if (url.pathname === "/api/evaluation/policy") {
    if (request.method === "GET") return json(policyResult(null));
    if (request.method === "POST") {
      const body = (await request.json()) as EvaluationPolicy;
      if (body.daily_cost < 0 || body.per_cycle_cost < 0 || body.coverage_share < 0) {
        return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
      }
      policyVersion += 1;
      policy = { ...body, version: `eval-policy-${policyVersion}` };
      const record: EvaluationRecord = {
        id: `evr_policy-${policyVersion}`,
        kind: "policy",
        subject: { kind: "", id: "" },
        assignment_id: "",
        supersedes_id: "",
        actor_kind: "operator",
        actor_id: "operator",
        created_at: new Date().toISOString(),
        provenance: provenance("", false),
        reason: "",
        related_id: "",
        policy,
      };
      return json(policyResult(record));
    }
    return json({ error: "unsupported method" }, 400);
  }

  if (request.method === "POST" && url.pathname === "/api/evaluation/operator") {
    const body = (await request.json()) as {
      subject: EvaluationSubject;
      kind: string;
      reason: string;
      decision?: string;
      criteria: Array<{ id: string; description: string }>;
      related_id: string;
      operator?: string;
    };
    // The real route refuses unknown fields, and an operator field is the
    // one a caller would try: the author is the session's identity and never
    // a value in the body.
    if ("operator" in body) {
      return json({ error: "request body is not the JSON object this route accepts" }, 400);
    }
    if (!OPERATOR_KINDS.includes(body.kind)) {
      return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
    }
    if (!body.reason && body.kind !== "criteria") {
      return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
    }
    const decision = body.decision ?? "";
    // The store's own gate, mirrored: a reconsideration decision states one
    // of two acts and nothing else may. A missing act is refused rather than
    // defaulted, because the default would be a reopen nobody asked for, and
    // an act on a kind that performs none is refused too.
    if (body.kind === "reconsider_decision" && !RECONSIDER_DECISIONS.includes(decision)) {
      return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
    }
    if (body.kind !== "reconsider_decision" && decision !== "") {
      return json({ error: "a value in the request is outside what the evaluation service accepts" }, 400);
    }
    operatorCounter += 1;
    const record: EvaluationRecord = {
      id: `evr_operator-${operatorCounter}`,
      kind: body.kind,
      subject: body.subject,
      assignment_id: "",
      supersedes_id: "",
      actor_kind: "operator",
      actor_id: "operator",
      created_at: new Date().toISOString(),
      provenance: provenance("", false),
      criteria: body.criteria.length > 0 ? body.criteria : null,
      reason: body.reason,
      // The act as it was sent, stored beside the reason and never read out
      // of it. A history entry renders this field.
      decision,
      related_id: body.related_id,
    };
    operatorRecords.push(record);
    // A reopen is one act across two stores: the real service records the
    // decision and the reopened disposition in one transaction, and the
    // record then reads as undecided. The mock performs the same flip, so a
    // preview cannot show a reopen that left /review saying "rejected" — the
    // exact divergence this surface has to be checkable against.
    if (decision === "reopen") {
      const fixture = byID[body.subject.id];
      if (fixture) {
        fixture.item.artifact.review_status = "new";
        fixture.item.lane = "open";
      }
    }
    return json({ record, decided: decided(body.kind, decision) });
  }

  return json({ error: "unknown synthetic API endpoint" }, 404);
}

// The server's own sentences about what an operator write did.
//
// They are three because one would be false about the others: a criteria or
// feedback record decides nothing, a retain moves no disposition, and a reopen
// moves one — so the reopen sentence states the consequence rather than
// denying it.
function decided(kind: string, decision: string): string {
  if (kind !== "reconsider_decision") {
    return (
      "this is your own attributed statement about the record. It accepts, rejects, defers, " +
      "reopens and merges nothing: a disposition is recorded on the review surface that owns " +
      "the vocabulary, and reopening a decided record stays an explicit act there."
    );
  }
  if (decision === "retain") {
    return (
      "your decision to retain the earlier ruling is recorded, attributed to you and scoped to " +
      "the change it answers. Nothing was reopened and nothing was re-decided; the reconsider " +
      "item stays readable beside the decision that answered it."
    );
  }
  return (
    "your decision to reopen this is recorded, and the record was reopened with it: the decision " +
    "you reopened keeps its place in the history, and the review surface now reads this record as " +
    "undecided, so it can be decided again on its merits. The two are one act — had either " +
    "failed, neither would exist."
  );
}

const SAVING =
  "the policy is stored. It bounds what an authorized draw may spend on the schedule this " +
  "deployment already runs; saving it starts no run, launches no compute, and reviews nothing " +
  "by itself.";

function policyResult(record: EvaluationRecord | null) {
  const current = coverage();
  const status = degraded
    ? "unavailable"
    : !policy.enabled
      ? "paused"
      : current.active > 0
        ? "running"
        : "scheduled";
  const detail = {
    unavailable:
      "the coverage inventory is degraded, so what is running cannot be stated from it: " +
      current.reason,
    paused:
      "authorized evaluation work is paused. Nothing is drawn and nothing is spent; the overdue " +
      "and never-reviewed counts above keep accruing and stay visible.",
    running: `${current.active} evaluation assignments are claimed and still in flight.`,
    scheduled:
      "authorized evaluation work is enabled with nothing in flight: the next draw happens when " +
      "the schedule reaches it.",
  }[status];
  return {
    policy,
    coverage: current,
    status,
    detail,
    next_draw: policy.enabled ? current.next_draw : undefined,
    saving: SAVING,
    record,
  };
}
