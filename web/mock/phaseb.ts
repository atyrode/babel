// Phase B synthetic fixtures and routes for the Babel web mock.
//
// Everything here is generated preview data: no real transcript, credential,
// path, or analysis output appears. The awkward cases are deliberate — a
// rejected hypothesis, fifty observations, conflicting evidence, a plan
// awaiting acceptance, hostile HTML/Markdown/URL/terminal-control content,
// and unbroken kilocharacter tokens — because the interface has to render
// them inert and intact before it can be trusted with an archive (§3, §10).
//
// Mutations are stateful in-memory so the answer, decide, and accept flows
// are exercisable end to end. MOCK_PHASEB=empty presents the day-one state:
// an empty frontier, queue, and inbox.

import type {
  AnalysisState,
  AnswerView,
  DecideRequest,
  DecisionView,
  EntityDetail,
  EvidenceRef,
  FactView,
  FindingDetail,
  FindingSummary,
  HypothesisDetail,
  HypothesisStatus,
  HypothesisSummary,
  Observation,
  OverviewFrontier,
  OverviewQuestions,
  OverviewReview,
  OverviewRuns,
  PlanView,
  QuestionSummary,
  QueueItem,
  ReviewContext,
  ReviewStatus,
  RefinementView,
  SearchHit,
} from "../src/api";

const phasebMode = Bun.env.MOCK_PHASEB ?? "rich";
const empty = phasebMode === "empty";

// OVERVIEW_ROWS is internal/web's own panel bound: a dashboard panel shows a
// fixed few rows and links to the page that lists the rest.
export const OVERVIEW_ROWS = 5;

// Hostile content fixtures. If any of these ever executes, injects markup, or
// escapes its quoted frame, the UI has failed §2.7. The window flag is what
// the browser tests assert stays undefined. Exported so the tests assert the
// exact bytes the fixtures carry rather than a drifting copy.
export const HOSTILE_HTML =
  "<img src=x onerror=\"window.__babel_pwned=1\"><script>window.__babel_pwned=2</script>";
export const HOSTILE_MARKDOWN =
  "[harmless link](javascript:window.__babel_pwned=3) <a href=\"javascript:window.__babel_pwned=4\">anchor</a> **bold**";
export const HOSTILE_CONTROL = "\u001b]0;owned\u0007\u001b[2J\u009b31mred\u0000\u202Edetsurtnu";
export const HOSTILE_URL = "javascript:window.__babel_pwned=5";
// A thousand characters with no spaces: layout must wrap it, not widen.
export const UNBROKEN_TOKEN = "deadbeefcafef00d".repeat(63).slice(0, 1000);

function digest(seed: string): string {
  return seed.repeat(64).slice(0, 64);
}

function ev(
  path: string,
  line: number,
  seed: string,
  note?: string,
  selector?: string,
): EvidenceRef {
  return {
    locator: { path, line, byte_offset: line * 137, digest: digest(seed) },
    ...(note ? { note } : {}),
    ...(selector ? { selector } : {}),
  };
}

// ---------------------------------------------------------------------------
// Analysis state
// ---------------------------------------------------------------------------

const analysisState: AnalysisState = {
  configured: true,
  worker: {
    available: false,
    detail:
      "Exploration runs start from `babel explore` in a terminal, where each run's scope, " +
      "profile, and disclosure class are consented explicitly. No run can start anywhere on " +
      "this deployment yet: Code has not implemented its half of the worker protocol, so " +
      "there is no analysis worker to launch.",
  },
  runs: empty ? [] : [
    {
      receipt_id: "rcp_01synthetic0001",
      run_id: "run_discovery-07",
      preparation_id: "prep_corpus-2026-08",
      revision: 1,
      recorded_at: "2026-08-28T22:14:00Z",
      sync: "committed",
      counts: {
        tool_requests: 41,
        tools_denied: 3,
        retrieval: 17,
        deferred: 4,
        rejected: 1,
        failures: 0,
        redactions: 2,
      },
    },
    {
      receipt_id: "rcp_01synthetic0002",
      run_id: "run_challenge-08",
      preparation_id: "prep_corpus-2026-08",
      revision: 1,
      recorded_at: "2026-08-29T07:40:00Z",
      sync: "pending-sync",
      counts: {
        tool_requests: 12,
        tools_denied: 0,
        retrieval: 6,
        deferred: 0,
        rejected: 0,
        failures: 1,
        redactions: 0,
      },
    },
  ],
  cookbook: [
    { id: "outcome-integrity", version: 2, kind: "lens", title: "Outcome integrity and unresolved state", default: true, scope: ["session", "corpus"], stages: ["investigate", "challenge", "synthesize"], capabilities: ["corpus-search"] },
    { id: "security-privacy", version: 1, kind: "lens", title: "Security, privacy, and trust boundaries", default: true, scope: ["session", "corpus", "repository"], stages: ["investigate", "challenge"], capabilities: ["corpus-search", "repo-read"] },
    { id: "code-health", version: 1, kind: "lens", title: "Code health, maintainability, and comprehensibility", default: true, scope: ["repository"], stages: ["investigate", "synthesize"], capabilities: ["repo-read", "sandbox-exec"] },
    { id: "coordination", version: 1, kind: "lens", title: "Human–agent coordination and avoidable rework", default: true, scope: ["session"], stages: ["investigate"], capabilities: ["corpus-search"] },
    { id: "effective-patterns", version: 1, kind: "lens", title: "Effective patterns and enabling conditions", default: true, scope: ["session", "corpus"], stages: ["investigate", "synthesize"], capabilities: ["corpus-search"] },
    { id: "decision-quality", version: 1, kind: "lens", title: "Engineering decision quality and operational risk", default: false, scope: ["session", "repository"], stages: ["investigate"], capabilities: ["corpus-search", "repo-read"] },
    { id: "operator-model", version: 1, kind: "lens", title: "Durable operator model", default: false, scope: ["corpus"], stages: ["investigate", "synthesize"], capabilities: ["corpus-search"] },
    { id: "capability-leverage", version: 1, kind: "lens", title: "Reusable practice and capability leverage", default: false, scope: ["session", "corpus"], stages: ["investigate"], capabilities: ["corpus-search"] },
    { id: "shared-techniques", version: 3, kind: "policy", title: "Shared investigation techniques", default: true, scope: ["corpus"], stages: ["investigate", "challenge", "synthesize"], capabilities: ["corpus-search", "repo-read", "sandbox-exec", "public-research"] },
    { id: "cookbook-quality", version: 1, kind: "meta", title: "Cookbook quality meta-analysis", default: false, scope: ["corpus"], stages: ["synthesize"], capabilities: ["corpus-search"] },
  ],
};

// ---------------------------------------------------------------------------
// Frontier: hypotheses, observations, findings, proposals
// ---------------------------------------------------------------------------

function observation(
  id: string,
  hypothesisID: string,
  claim: string,
  extras: Partial<Observation["payload"]> = {},
  evidence?: EvidenceRef[],
): Observation {
  const items = evidence ?? [
    ev("/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", 42, "3c", "The agent claims completion without running the suite.", "omp/synthetic-charlie"),
  ];
  return {
    id,
    hypothesis_id: hypothesisID,
    run_id: "run_discovery-07",
    recipe_id: "outcome-integrity",
    recipe_version: 2,
    schema_version: 1,
    evidence_count: items.length,
    created_at: "2026-08-28T22:02:00Z",
    payload: {
      claim,
      category: "outcome-integrity",
      confidence: "moderate",
      impact: "moderate",
      evidence: items,
      counter_evidence_absent: true,
      ...extras,
    },
  };
}

const hypInvestigating: HypothesisDetail = {
  hypothesis: {
    id: "hyp_unverified-closures",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T21:55:00Z",
    status: "investigating",
    payload: {
      statement:
        "Sessions that end with an agent claiming success but no verification step show a " +
        "recurring pattern of silent regressions discovered one to three sessions later.",
      origin_cues: ["'done' with no test run", "later session reopens the same file"],
      provisional_labels: ["outcome-integrity", "verification"],
      novelty: 0.62,
      priority: 0.81,
      notes: "Synthetic fixture note: check whether the pattern survives the challenger.",
    },
  },
  statusHistory: [
    { id: "sev_001", hypothesis_id: "hyp_unverified-closures", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:55:00Z" },
    { id: "sev_002", hypothesis_id: "hyp_unverified-closures", sequence: 2, status: "queued", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:58:00Z" },
    { id: "sev_003", hypothesis_id: "hyp_unverified-closures", sequence: 3, status: "investigating", run_id: "run_discovery-07", recorded_at: "2026-08-28T22:00:00Z", note: "Selected within budget." },
  ],
  observations: [
    observation("obs_claim-no-verify", "hyp_unverified-closures",
      "In the synthetic charlie session the agent reported the fix complete; no test or build command appears afterwards.",
      {},
      [
        ev("/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", 42, "3c", "Completion claim with no subsequent verification event.", "omp/synthetic-charlie"),
        ev("/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", 57, "9a", "Session ends four events later."),
      ]),
    observation("obs_reopened", "hyp_unverified-closures",
      "The same module is reopened in the synthetic alpha session two days later with a failing import.",
      {
        confidence: "high",
        impact: "moderate",
        counter_evidence: [
          ev("/home/demo/.codex/sessions/synthetic-alpha.jsonl", 12, "5e", "The reopening session also changed requirements, so the regression is not cleanly attributable.", "codex/synthetic-alpha"),
        ],
        counter_evidence_absent: undefined,
        temporal_status: "still-applicable",
      },
      [
        ev("/home/demo/.codex/sessions/synthetic-alpha.jsonl", 8, "7b", "Import failure in the same module.", "codex/synthetic-alpha"),
      ]),
  ],
  links: [
    { id: "lnk_001", from_id: "hyp_unverified-closures", to_id: "hyp_hostile-content", type: "contradicts", created_at: "2026-08-28T22:05:00Z", other_statement: "Model suggests " + HOSTILE_HTML },
    { id: "lnk_002", from_id: "hyp_dense-token", to_id: "hyp_unverified-closures", type: "derived-from", created_at: "2026-08-28T22:06:00Z", other_statement: UNBROKEN_TOKEN.slice(0, 120) },
  ],
  lineage: {
    node: { kind: "hypothesis", id: "hyp_unverified-closures" },
    ancestors: [],
    descendants: [
      { id: "edg_001", relation: "refines", from: { kind: "hypothesis", id: "hyp_dense-token" }, to: { kind: "hypothesis", id: "hyp_unverified-closures" }, created_at: "2026-08-28T23:00:00Z", generation: 1 },
    ],
  },
};

const hypRejected: HypothesisDetail = {
  hypothesis: {
    id: "hyp_lens-overlap",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T21:50:00Z",
    status: "rejected",
    payload: {
      statement:
        "The coordination and decision-quality lenses overlap so heavily that one of them " +
        "should be retired.",
      origin_cues: ["duplicate candidates across lenses"],
      provisional_labels: ["meta"],
      novelty: 0.35,
      priority: 0.4,
    },
  },
  statusHistory: [
    { id: "sev_010", hypothesis_id: "hyp_lens-overlap", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:50:00Z" },
    { id: "sev_011", hypothesis_id: "hyp_lens-overlap", sequence: 2, status: "investigating", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:57:00Z" },
    { id: "sev_012", hypothesis_id: "hyp_lens-overlap", sequence: 3, status: "rejected", run_id: "run_discovery-07", recorded_at: "2026-08-28T23:10:00Z", note: "Rejected on review; kept, as everything is." },
  ],
  observations: [
    observation("obs_overlap", "hyp_lens-overlap",
      "Both lens drafts emitted near-identical candidates for the synthetic bravo session.",
      { confidence: "low", impact: "low" },
      [ev("/home/demo/.claude/projects/synthetic-bravo.jsonl", 3, "2d", "Two candidates with the same wording.", "claude-code/synthetic-bravo")]),
  ],
  links: [],
  lineage: {
    node: { kind: "hypothesis", id: "hyp_lens-overlap" },
    ancestors: [],
    descendants: [],
  },
};

const fiftyObservations = Array.from({ length: 50 }, (_, index) =>
  observation(
    `obs_fifty-${String(index + 1).padStart(2, "0")}`,
    "hyp_many-observations",
    `Synthetic recurring observation ${index + 1}: the same retry loop appears in a different generated session.`,
    { confidence: index % 3 === 0 ? "low" : "moderate", impact: "low" },
    [ev(`/home/demo/.omp/agent/sessions/synthetic-project/filler-${index % 7}.jsonl`, index + 5, String((index % 9) + 1), `Occurrence ${index + 1} of the generated pattern.`)],
  ));

const hypFifty: HypothesisDetail = {
  hypothesis: {
    id: "hyp_many-observations",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T22:20:00Z",
    status: "deferred",
    payload: {
      statement: "A retry loop without backoff recurs across most synthetic sessions that touch the import pipeline.",
      provisional_labels: ["code-health"],
      novelty: 0.5,
      priority: 0.66,
    },
  },
  statusHistory: [
    { id: "sev_020", hypothesis_id: "hyp_many-observations", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T22:20:00Z" },
    { id: "sev_021", hypothesis_id: "hyp_many-observations", sequence: 2, status: "deferred", run_id: "run_discovery-07", recorded_at: "2026-08-28T23:30:00Z", note: "Budget exhausted; the frontier keeps the remainder." },
  ],
  observations: fiftyObservations,
  links: [],
  lineage: { node: { kind: "hypothesis", id: "hyp_many-observations" }, ancestors: [], descendants: [] },
};

const hypHostile: HypothesisDetail = {
  hypothesis: {
    id: "hyp_hostile-content",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T22:30:00Z",
    status: "untriaged",
    payload: {
      statement: `Model suggests ${HOSTILE_HTML} and ${HOSTILE_MARKDOWN} while printing ${HOSTILE_CONTROL}.`,
      origin_cues: [HOSTILE_URL, "hostile fixture"],
      novelty: 0.9,
      priority: 0.1,
      notes: `Note body carrying a hostile URL ${HOSTILE_URL} that must render as text.`,
    },
  },
  statusHistory: [
    { id: "sev_030", hypothesis_id: "hyp_hostile-content", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T22:30:00Z" },
  ],
  observations: [
    observation("obs_hostile", "hyp_hostile-content",
      `Claim quoting hostile transcript bytes: ${HOSTILE_HTML} ${HOSTILE_CONTROL}`,
      { confidence: "low", impact: "high" },
      [ev("/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", 99, "6f", `Note with markup ${HOSTILE_MARKDOWN}`, "omp/synthetic-charlie")]),
  ],
  links: [
    { id: "lnk_030", from_id: "hyp_hostile-content", to_id: "hyp_unverified-closures", type: "contradicts", created_at: "2026-08-28T22:31:00Z", other_statement: hypInvestigating.hypothesis.payload.statement },
  ],
  lineage: { node: { kind: "hypothesis", id: "hyp_hostile-content" }, ancestors: [], descendants: [] },
};

const hypDense: HypothesisDetail = {
  hypothesis: {
    id: "hyp_dense-token",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T23:00:00Z",
    status: "queued",
    payload: {
      statement: UNBROKEN_TOKEN,
      novelty: 0.2,
      priority: 0.2,
    },
  },
  statusHistory: [
    { id: "sev_040", hypothesis_id: "hyp_dense-token", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T23:00:00Z" },
    { id: "sev_041", hypothesis_id: "hyp_dense-token", sequence: 2, status: "queued", run_id: "run_discovery-07", recorded_at: "2026-08-28T23:05:00Z" },
  ],
  observations: [],
  links: [],
  lineage: {
    node: { kind: "hypothesis", id: "hyp_dense-token" },
    ancestors: [
      { id: "edg_001", relation: "refines", from: { kind: "hypothesis", id: "hyp_dense-token" }, to: { kind: "hypothesis", id: "hyp_unverified-closures" }, created_at: "2026-08-28T23:00:00Z", generation: 1 },
    ],
    descendants: [],
  },
};

const hypPromoted: HypothesisDetail = {
  hypothesis: {
    id: "hyp_promoted-pattern",
    run_id: "run_discovery-07",
    schema_version: 1,
    created_at: "2026-08-28T21:40:00Z",
    status: "promoted",
    payload: {
      statement: "Sessions that state acceptance criteria up front close cleanly far more often in the synthetic corpus.",
      provisional_labels: ["effective-patterns"],
      novelty: 0.55,
      priority: 0.7,
    },
  },
  statusHistory: [
    { id: "sev_050", hypothesis_id: "hyp_promoted-pattern", sequence: 1, status: "untriaged", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:40:00Z" },
    { id: "sev_051", hypothesis_id: "hyp_promoted-pattern", sequence: 2, status: "investigating", run_id: "run_discovery-07", recorded_at: "2026-08-28T21:45:00Z" },
    { id: "sev_052", hypothesis_id: "hyp_promoted-pattern", sequence: 3, status: "promoted", run_id: "run_challenge-08", recorded_at: "2026-08-29T07:40:00Z", note: "Survived the challenger with one unresolved objection preserved." },
  ],
  observations: [
    observation("obs_criteria", "hyp_promoted-pattern",
      "Synthetic sessions with explicit acceptance criteria show verification commands before the closing claim.",
      { confidence: "high", impact: "moderate", counter_evidence: [
        ev("/home/demo/.claude/projects/synthetic-bravo.jsonl", 7, "4a", "One generated counter-example: criteria stated, still no verification.", "claude-code/synthetic-bravo"),
      ], counter_evidence_absent: undefined },
      [ev("/home/demo/.codex/sessions/synthetic-alpha.jsonl", 21, "8c", "Criteria stated, tests run, clean close.", "codex/synthetic-alpha")]),
  ],
  links: [],
  lineage: { node: { kind: "hypothesis", id: "hyp_promoted-pattern" }, ancestors: [], descendants: [{ id: "edg_060", relation: "responds-to", from: { kind: "finding", id: "fnd_conflicting-evidence" }, to: { kind: "hypothesis", id: "hyp_promoted-pattern" }, created_at: "2026-08-29T07:41:00Z", generation: 1 }] },
};

const hypotheses: Record<string, HypothesisDetail> = Object.fromEntries(
  [hypInvestigating, hypRejected, hypFifty, hypHostile, hypDense, hypPromoted]
    .map((detail) => [detail.hypothesis.id, detail]),
);

const findingConflict: FindingDetail = {
  finding: {
    id: "fnd_conflicting-evidence",
    run_id: "run_challenge-08",
    schema_version: 1,
    created_at: "2026-08-29T07:41:00Z",
    observation_ids: ["obs_criteria", "obs_claim-no-verify"],
    hypothesis_ids: ["hyp_promoted-pattern", "hyp_unverified-closures"],
    payload: {
      title: "Stated acceptance criteria correlate with verified closes — with real counter-evidence",
      pattern:
        "Across the synthetic corpus, sessions that begin with explicit acceptance criteria " +
        "end with a verification command before the closing claim far more often than those " +
        "that do not.",
      significance: "If it held on real data, it would justify a session-template experiment.",
      scope: ["synthetic-project", "atlas"],
      recurrence: 9,
      counter_evidence: [
        ev("/home/demo/.claude/projects/synthetic-bravo.jsonl", 7, "4a", "Criteria stated; no verification followed.", "claude-code/synthetic-bravo"),
        ev("/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", 61, "1e", "Verified close with no stated criteria — the inverse case.", "omp/synthetic-charlie"),
      ],
      temporal_status: "unverifiable",
    },
  },
  observations: [hypPromoted.observations[0], hypInvestigating.observations[0]],
  proposals: [
    {
      id: "prp_criteria-template",
      run_id: "run_challenge-08",
      schema_version: 1,
      created_at: "2026-08-29T07:42:00Z",
      finding_ids: ["fnd_conflicting-evidence"],
      hypothesis_ids: ["hyp_promoted-pattern"],
      review_status: "new",
      payload: {
        title: "Trial an acceptance-criteria preamble in agent session templates",
        problem: "Closes without verification recur in the synthetic corpus (see the finding's counter-evidence before believing this).",
        outcome: "A reviewable template change proposal; publishing or applying it happens outside Babel.",
        uncertainty: "The correlation may be confounded by task size; two direct counter-examples are attached.",
        impact: "moderate",
        estimated_scope: "one template file",
        supporting: [ev("/home/demo/.codex/sessions/synthetic-alpha.jsonl", 21, "8c", "Clean close with criteria.", "codex/synthetic-alpha")],
        conflicting: [ev("/home/demo/.claude/projects/synthetic-bravo.jsonl", 7, "4a", "Counter-example.", "claude-code/synthetic-bravo")],
        targets: [{ system: "synthetic/dotfiles", confidence: "low", rationale: "Suggestion only; the reviewer chooses the destination." }],
        risks: ["Template friction could outweigh the benefit."],
        open_questions: ["Does the pattern survive on real, non-synthetic sessions?"],
        classification: "private",
        destinations: ["operator-note"],
      },
    },
  ],
};

const findings: Record<string, FindingDetail> = {
  [findingConflict.finding.id]: findingConflict,
};

// ---------------------------------------------------------------------------
// Review: queue, dispositions, contexts, exports
// ---------------------------------------------------------------------------

interface ReviewRecordState {
  subject: { type: "hypothesis" | "finding" | "proposal"; id: string };
  excerpt: string;
  enrolled_at: string;
  decisions: DecisionView[];
  refinements: RefinementView[];
}

const reviewRecords: ReviewRecordState[] = empty ? [] : [
  {
    subject: { type: "hypothesis", id: "hyp_unverified-closures" },
    excerpt: hypInvestigating.hypothesis.payload.statement,
    enrolled_at: "2026-08-28T22:00:00Z",
    decisions: [],
    refinements: [],
  },
  {
    subject: { type: "hypothesis", id: "hyp_hostile-content" },
    excerpt: hypHostile.hypothesis.payload.statement,
    enrolled_at: "2026-08-28T22:30:00Z",
    decisions: [],
    refinements: [],
  },
  {
    subject: { type: "hypothesis", id: "hyp_lens-overlap" },
    excerpt: hypRejected.hypothesis.payload.statement,
    enrolled_at: "2026-08-28T22:00:00Z",
    decisions: [
      {
        id: "dsp_001",
        sequence: 1,
        disposition: "reject",
        reviewer_id: "operator",
        recorded_at: "2026-08-28T23:10:00Z",
        note: "The overlap is real but retiring a draft lens is premature.",
        context: {
          id: "ctx_001",
          author: "operator",
          at: "2026-08-28T23:09:00Z",
          text: "Re-examine after both drafts have run on a real corpus; synthetic overlap proves little.",
        },
      },
    ],
    refinements: [
      {
        request: {
          id: "rfr_001",
          disposition_id: "dsp_001",
          subject: { type: "hypothesis", id: "hyp_lens-overlap" },
          created_at: "2026-08-28T23:10:00Z",
          guidance: "Compare candidate overlap on the next real-corpus run before proposing lens retirement.",
          scope: ["run_discovery-07"],
        },
      },
    ],
  },
  {
    subject: { type: "finding", id: "fnd_conflicting-evidence" },
    excerpt: findingConflict.finding.payload.title,
    enrolled_at: "2026-08-29T07:41:00Z",
    decisions: [],
    refinements: [],
  },
  {
    subject: { type: "proposal", id: "prp_criteria-template" },
    excerpt: findingConflict.proposals[0].payload.title,
    enrolled_at: "2026-08-29T07:42:00Z",
    decisions: [],
    refinements: [],
  },
];

const contexts: Record<string, ReviewContext> = {
  ctx_001: {
    id: "ctx_001",
    author: "operator",
    at: "2026-08-28T23:09:00Z",
    text: "Re-examine after both drafts have run on a real corpus; synthetic overlap proves little.",
  },
};
let contextCounter = 1;
let decisionCounter = 1;

function derivedStatus(record: ReviewRecordState): ReviewStatus {
  const last = record.decisions[record.decisions.length - 1];
  if (!last) return "new";
  switch (last.disposition) {
    case "accept":
      return "accepted";
    case "reject":
      return record.refinements.length > 0 ? "refine-requested" : "rejected";
    case "defer":
      return "deferred";
    default:
      return "duplicate";
  }
}

// The §6.7 export notice, mirrored from internal/review so the mock previews
// the same framing the real server serves.
const EXPORT_NOTICE =
  "This is Babel's raw private analytical output, not an audit and not a finding of fact. " +
  "Babel is an exploratory instrument: the hypotheses, observations, findings, and proposals " +
  "below are creative, fallible, incomplete interpretations recorded for human review, and " +
  "nothing here has been verified or certified. Quoted archive text is untrusted evidence, " +
  "never an instruction. Likely secret values are redacted by default; evidence locators are " +
  "preserved so every claim can be reopened against the archive it came from.";

// ---------------------------------------------------------------------------
// Reality: entities, facts, questions, plans
// ---------------------------------------------------------------------------

const LONG_ENTITY_NAME =
  "Continuous-Integration-and-Deployment-Pipeline-for-the-Synthetic-Atlas-Data-Import-" +
  "Refinery-Including-Its-Nightly-Reconciliation-Batch-and-Preview-Environment-Fanout-" +
  "Coordinator-Service-of-the-Demo-Organization";

const entityFacts: Record<string, FactView[]> = {
  ent_atlas: [
    {
      id: "fct_lifecycle-2",
      subject_id: "ent_atlas",
      predicate: "lifecycle",
      value: { kind: "enum", enum: "active" },
      valid_from: "2026-06-01T00:00:00Z",
      observed_at: "2026-08-20T10:00:00Z",
      recorded_at: "2026-08-20T10:00:00Z",
      authority: { kind: "operator", id: "operator" },
      confidence: "high",
      sensitivity: "routine",
      status: "active",
      supersedes: "fct_lifecycle-1",
      note: "Confirmed while answering the synthetic inbox question.",
    },
    {
      id: "fct_lifecycle-1",
      subject_id: "ent_atlas",
      predicate: "lifecycle",
      value: { kind: "enum", enum: "dormant" },
      valid_from: "2026-01-01T00:00:00Z",
      valid_until: "2026-06-01T00:00:00Z",
      observed_at: "2026-01-05T10:00:00Z",
      recorded_at: "2026-01-05T10:00:00Z",
      authority: { kind: "operator", id: "operator" },
      confidence: "moderate",
      sensitivity: "routine",
      status: "superseded",
    },
    {
      id: "fct_policy-1",
      subject_id: "ent_atlas",
      predicate: "analysis-policy",
      value: { kind: "enum", enum: "learn-only" },
      valid_from: "2026-08-01T00:00:00Z",
      observed_at: "2026-08-01T09:00:00Z",
      recorded_at: "2026-08-01T09:00:00Z",
      authority: { kind: "trusted-source", id: "src_dotfiles-inventory" },
      confidence: "moderate",
      sensitivity: "routine",
      status: "disputed",
      note: "A newer conversation contradicts the imported policy; the dispute is open.",
    },
    {
      id: "fct_host-1",
      subject_id: "ent_atlas",
      predicate: "deployed-on",
      value: { kind: "entity", object_id: "ent_host" },
      valid_from: "2026-05-01T00:00:00Z",
      observed_at: "2026-05-01T09:00:00Z",
      recorded_at: "2026-05-01T09:00:00Z",
      expires_at: "2026-08-15T00:00:00Z",
      authority: { kind: "trusted-source", id: "src_dotfiles-inventory" },
      confidence: "moderate",
      sensitivity: "routine",
      status: "stale",
      note: "Volatile deployment fact past its refresh TTL; stale, not deleted.",
    },
  ],
  ent_longname: [
    {
      id: "fct_longname-path",
      subject_id: "ent_longname",
      predicate: "workspace-path",
      value: { kind: "text", text: `/home/demo/projects/${UNBROKEN_TOKEN.slice(0, 80)}` },
      valid_from: "2026-08-01T00:00:00Z",
      observed_at: "2026-08-01T09:00:00Z",
      recorded_at: "2026-08-01T09:00:00Z",
      authority: { kind: "operator", id: "operator" },
      confidence: "high",
      sensitivity: "routine",
      status: "proposed",
      note: "Proposed by an interpreter plan; asserts nothing until a plan applying it is accepted.",
    },
  ],
  ent_host: [],
};

const entities: Record<string, EntityDetail> = {
  ent_atlas: {
    entity: {
      id: "ent_atlas",
      kind: "project",
      schema_version: 1,
      created_at: "2026-05-01T09:00:00Z",
      role: "self",
      canonical_id: "ent_atlas",
      display_name: "Atlas import pipeline",
      notes: "Synthetic fixture project.",
    },
    aliases: [
      { id: "als_001", entity_id: "ent_atlas", kind: "name", state: "asserted", created_at: "2026-05-01T09:00:00Z", value: "atlas" },
      { id: "als_002", entity_id: "ent_atlas", kind: "path", state: "asserted", created_at: "2026-05-01T09:00:00Z", value: "/home/demo/projects/atlas" },
      { id: "als_003", entity_id: "ent_atlas", kind: "term", state: "asserted", created_at: "2026-06-10T09:00:00Z", value: HOSTILE_URL, note: "A URL-shaped alias must render as text, never as a link." },
      { id: "als_004", entity_id: "ent_atlas", kind: "name", state: "retracted", created_at: "2026-05-02T09:00:00Z", value: "the importer", note: "Mis-resolution, reversed by a split; history kept." },
    ],
    relationships: [
      { id: "rel_001", kind: "deployed-on", state: "asserted", created_at: "2026-05-01T09:00:00Z", from: { id: "ent_atlas", display_name: "Atlas import pipeline" }, to: { id: "ent_host", display_name: "demo-workstation" } },
      { id: "rel_002", kind: "contains", state: "asserted", created_at: "2026-08-01T09:00:00Z", from: { id: "ent_longname", display_name: LONG_ENTITY_NAME }, to: { id: "ent_atlas", display_name: "Atlas import pipeline" } },
    ],
    facts: entityFacts.ent_atlas,
  },
  ent_longname: {
    entity: {
      id: "ent_longname",
      kind: "service",
      schema_version: 1,
      created_at: "2026-08-01T09:00:00Z",
      role: "self",
      canonical_id: "ent_longname",
      display_name: LONG_ENTITY_NAME,
    },
    aliases: [
      { id: "als_010", entity_id: "ent_longname", kind: "term", state: "asserted", created_at: "2026-08-01T09:00:00Z", value: UNBROKEN_TOKEN.slice(0, 200), note: "Unbroken token alias for layout hardening." },
    ],
    relationships: [
      { id: "rel_002", kind: "contains", state: "asserted", created_at: "2026-08-01T09:00:00Z", from: { id: "ent_longname", display_name: LONG_ENTITY_NAME }, to: { id: "ent_atlas", display_name: "Atlas import pipeline" } },
    ],
    facts: entityFacts.ent_longname,
  },
  ent_host: {
    entity: {
      id: "ent_host",
      kind: "machine",
      schema_version: 1,
      created_at: "2026-05-01T09:00:00Z",
      role: "self",
      canonical_id: "ent_host",
      display_name: "demo-workstation",
    },
    aliases: [
      { id: "als_020", entity_id: "ent_host", kind: "name", state: "asserted", created_at: "2026-05-01T09:00:00Z", value: "demo-workstation" },
    ],
    relationships: [
      { id: "rel_001", kind: "deployed-on", state: "asserted", created_at: "2026-05-01T09:00:00Z", from: { id: "ent_atlas", display_name: "Atlas import pipeline" }, to: { id: "ent_host", display_name: "demo-workstation" } },
    ],
    facts: entityFacts.ent_host,
  },
};

const planProposed: PlanView = {
  id: "pln_focus-atlas",
  question_id: "qst_focus-policy",
  answer_id: "ans_focus-1",
  interpreter_version: 1,
  created_at: "2026-08-29T08:10:00Z",
  state: "proposed",
  summary:
    "The operator confirmed Atlas is active and wants normal analysis. Assert the lifecycle " +
    "fact, install the focus rule change, and retain one follow-up hypothesis about the " +
    "stale deployment fact.",
  actions: [
    {
      id: "act_001",
      position: 1,
      kind: "assert-fact",
      state: "pending-acceptance",
      payload: {
        rationale: "The answer states Atlas is under active development again.",
        fact: { subject_id: "ent_atlas", predicate: "analysis-policy", value: { kind: "enum", enum: "normal" } },
      },
    },
    {
      id: "act_002",
      position: 2,
      kind: "change-focus-policy",
      state: "pending-acceptance",
      payload: {
        rationale: "Normal policy for Atlas requires a focus-rule version installing the mapping.",
        focus_rules: { version: 3, rules: [{ predicate: "analysis-policy", equals: "normal", allowance: "full" }] },
      },
    },
    {
      id: "act_003",
      position: 3,
      kind: "create-hypothesis",
      state: "retained",
      result_id: "hyp_stale-deployment",
      payload: {
        rationale: "The stale deployed-on fact deserves its own investigation either way.",
        hypothesis: { statement: "The Atlas deployment host changed without an inventory refresh." },
      },
    },
    {
      id: "act_004",
      position: 4,
      kind: "ask-follow-up",
      state: "retained",
      result_id: "qst_deploy-host",
      payload: {
        rationale: "Only the operator can say where Atlas runs now.",
        follow_up: { prompt: "Where does Atlas run today?" },
      },
    },
  ],
};

const questions: QuestionSummary[] = empty ? [] : [
  {
    id: "qst_focus-policy",
    kind: "set-focus-policy",
    class: "blocking",
    state: "plan-ready",
    sensitivity: "routine",
    created_at: "2026-08-29T07:50:00Z",
    prompt: "Atlas shows fresh commits but its analysis policy still says learn-only. Should analysis spend on it normally?",
    why_asked: "Two queued hypotheses defer their repository stage on the learn-only policy.",
    target_entity_ids: ["ent_atlas"],
    target_predicates: ["analysis-policy"],
    score: 74,
    terms: { "affected-work": 30, "avoided-cost": 24, "dependencies": 10, "staleness": 6, "disclosure-impact": 4 },
    answers: [
      {
        id: "ans_focus-1",
        question_id: "qst_focus-policy",
        sequence: 1,
        author: "operator",
        at: "2026-08-29T08:05:00Z",
        recorded_at: "2026-08-29T08:05:00Z",
        outcome: "answered",
        text: "Yes — Atlas is active again since June, treat it normally.",
      },
    ],
    plans: [planProposed],
  },
  {
    id: "qst_hostile-answer",
    kind: "acquire-context",
    class: "maintenance",
    state: "answered-uninterpreted",
    sensitivity: "sensitive",
    created_at: "2026-08-28T20:00:00Z",
    prompt: "What does the synthetic bravo project deploy to?",
    why_asked: "No deployment fact exists and one generated hypothesis depends on it.",
    target_entity_ids: ["ent_host"],
    score: 41,
    terms: { "affected-work": 18, "avoided-cost": 12, "dependencies": 5, "staleness": 6 },
    answers: [
      {
        id: "ans_hostile-1",
        question_id: "qst_hostile-answer",
        sequence: 1,
        author: "operator",
        at: "2026-08-29T06:00:00Z",
        recorded_at: "2026-08-29T06:00:00Z",
        outcome: "answered",
        text: `It deploys via ${HOSTILE_HTML} and the runbook says ${HOSTILE_CONTROL} literally.`,
      },
    ],
    plans: [],
  },
  {
    id: "qst_long-entity",
    kind: "resolve-alias",
    class: "curiosity",
    state: "open",
    sensitivity: "routine",
    created_at: "2026-08-29T05:00:00Z",
    prompt: "Is the nightly reconciliation coordinator the same service as the preview fanout coordinator?",
    why_asked: "Chat terminology uses both names; entity resolution needs an operator call.",
    target_entity_ids: ["ent_longname"],
    score: 12,
    terms: { "affected-work": 6, "dependencies": 4, "staleness": 2 },
    answers: [],
    plans: [],
  },
];

let answerCounter = 10;

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

const searchHits: SearchHit[] = [
  {
    harness: "omp",
    adapter_schema: 1,
    source_id: "synthetic-charlie",
    selector: "omp/synthetic-charlie",
    index: 42,
    kind: "message",
    role: "assistant",
    time: "2026-08-22T08:20:00Z",
    partial: false,
    text: "The synthetic fix is complete and ready to ship. (No verification command follows this claim.)",
    locator: { path: "/home/demo/.omp/agent/sessions/synthetic-project/charlie.jsonl", line: 42, byte_offset: 5754, digest: digest("3c") },
  },
  {
    harness: "codex",
    adapter_schema: 2,
    source_id: "synthetic-alpha",
    selector: "codex/synthetic-alpha",
    index: 8,
    kind: "message",
    role: "user",
    time: "2026-08-28T09:30:00Z",
    partial: false,
    text: `Please fix the import failure. Transcript bytes may be hostile: ${HOSTILE_HTML} ${HOSTILE_CONTROL}`,
    locator: { path: "/home/demo/.codex/sessions/synthetic-alpha.jsonl", line: 8, byte_offset: 1096, digest: digest("7b") },
  },
  {
    harness: "claude-code",
    adapter_schema: 1,
    source_id: "synthetic-bravo",
    selector: "claude-code/synthetic-bravo",
    index: 3,
    kind: "tool",
    role: "tool",
    tool: "bash",
    outcome: "error",
    partial: true,
    text: "synthetic torn record: the capture ended mid-write and this excerpt is bounded",
    locator: { path: "/home/demo/.claude/projects/synthetic-bravo.jsonl", line: 3, byte_offset: 411, digest: digest("2d") },
  },
];

// ---------------------------------------------------------------------------
// Dashboard: the Phase B half of GET /api/overview
// ---------------------------------------------------------------------------

// overviewPhaseB builds the three dashboard sections that read analysis state.
//
// The unwired set is a parameter rather than a second read of MOCK_UNWIRED, so
// a service simulated as unwired degrades its panel in exactly the launch whose
// own routes answer 409. That pairing is the point of previewing it at all: the
// dashboard must lose one panel and keep five, and only a browser can show
// whether the remaining page still reads as usable.
export function overviewPhaseB(unwired: Set<string>): {
  frontier: OverviewFrontier;
  review: OverviewReview;
  runs: OverviewRuns;
} {
  const details = Object.values(hypotheses);
  const statuses: HypothesisStatus[] = [
    "untriaged", "queued", "investigating", "deferred", "rejected", "promoted",
  ];
  // Zeros included, in §4.2 order, exactly as internal/web serves them: the
  // panel describes the lifecycle rather than only its populated half.
  const lifecycle = statuses.map((status) => ({
    status,
    count: unwired.has("frontier") ? 0 : details.filter((d) => d.hypothesis.status === status).length,
  }));
  const frontier: OverviewFrontier = unwired.has("frontier")
    ? {
        available: false,
        unavailable: "The hypothesis frontier is not available in this session.",
        hypotheses: 0,
        statuses: lifecycle,
        truncated: false,
        rows: [],
      }
    : {
        available: true,
        hypotheses: details.length,
        statuses: lifecycle,
        truncated: false,
        rows: [...details]
          .sort((a, b) => b.hypothesis.created_at.localeCompare(a.hypothesis.created_at))
          .slice(0, OVERVIEW_ROWS)
          .map((detail) => ({
            id: detail.hypothesis.id,
            run_id: detail.hypothesis.run_id,
            status: detail.hypothesis.status,
            created_at: detail.hypothesis.created_at,
            statement: detail.hypothesis.payload.statement,
          })),
      };

  const inbox: OverviewQuestions = unwired.has("reality")
    ? { available: false, unavailable: "The reality ledger is not available in this session.", open: 0, rows: [] }
    : {
        available: true,
        open: questions.length,
        rows: questions.slice(0, OVERVIEW_ROWS).map((question) => ({
          id: question.id,
          state: question.state,
          class: question.class,
          score: question.score,
          prompt: question.prompt,
        })),
      };
  const awaiting = reviewRecords.filter((record) => derivedStatus(record) === "new");
  const review: OverviewReview = unwired.has("review")
    ? {
        available: false,
        unavailable: "The review service is not available in this session.",
        awaiting: 0,
        rows: [],
        questions: inbox,
      }
    : {
        available: true,
        awaiting: awaiting.length,
        rows: awaiting.slice(0, OVERVIEW_ROWS).map((record) => ({
          type: record.subject.type,
          id: record.subject.id,
          status: derivedStatus(record),
          enrolled_at: record.enrolled_at,
          excerpt: record.excerpt,
        })),
        questions: inbox,
      };

  // The receipts are the same listing /api/analysis/state serves, joined to
  // the frontier for the two things a receipt's header cannot carry: how many
  // candidates the run produced, and which recipe its observations recorded.
  const runs: OverviewRuns = unwired.has("frontier")
    ? { available: false, unavailable: "Run receipts are not available in this session.", total: 0, rows: [] }
    : {
        available: true,
        total: analysisState.runs.length,
        rows: analysisState.runs.slice(0, OVERVIEW_ROWS).map((receipt) => {
          const seen = new Map<string, { id: string; version: number }>();
          for (const detail of details) {
            for (const observed of detail.observations) {
              if (observed.run_id !== receipt.run_id) continue;
              seen.set(`${observed.recipe_id} v${observed.recipe_version}`, {
                id: observed.recipe_id,
                version: observed.recipe_version,
              });
            }
          }
          return {
            receipt_id: receipt.receipt_id,
            run_id: receipt.run_id,
            preparation_id: receipt.preparation_id,
            recorded_at: receipt.recorded_at,
            sync: receipt.sync,
            retrievals: receipt.counts.retrieval,
            deferred: receipt.counts.deferred,
            failures: receipt.counts.failures,
            redactions: receipt.counts.redactions,
            hypotheses: details.filter((d) => d.hypothesis.run_id === receipt.run_id).length,
            recipes: [...seen.values()],
          };
        }),
      };
  return { frontier, review, runs };
}

// ---------------------------------------------------------------------------
// Route handling
// ---------------------------------------------------------------------------

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

function paged<T>(url: URL, items: T[]): { slice: T[]; total: number } {
  const limit = Math.min(200, Math.max(1, Number(url.searchParams.get("limit") ?? 50)));
  const offset = Math.max(0, Number(url.searchParams.get("offset") ?? 0));
  return { slice: items.slice(offset, offset + limit), total: items.length };
}

export async function phasebResponse(request: Request, url: URL): Promise<Response | null> {
  const { method } = request;
  const path = url.pathname;

  if (method === "GET" && path === "/api/analysis/state") return json(analysisState);

  if (method === "GET" && path === "/api/hypotheses") {
    const status = url.searchParams.get("status") ?? "";
    const all = (empty ? [] : Object.values(hypotheses))
      .filter((detail) => !status || detail.hypothesis.status === status)
      .map((detail): HypothesisSummary => ({
        id: detail.hypothesis.id,
        run_id: detail.hypothesis.run_id,
        created_at: detail.hypothesis.created_at,
        status: detail.hypothesis.status as HypothesisStatus,
        statement: detail.hypothesis.payload.statement,
        provisional_labels: detail.hypothesis.payload.provisional_labels,
        observations: detail.observations.length,
      }));
    const { slice, total } = paged(url, all);
    return json({ items: slice, total });
  }

  if (method === "GET" && path === "/api/hypothesis") {
    const id = url.searchParams.get("id") ?? "";
    const detail = hypotheses[id];
    return detail ? json(detail) : json({ error: `synthetic hypothesis not found: ${id}` }, 404);
  }

  if (method === "GET" && path === "/api/findings") {
    const all = (empty ? [] : Object.values(findings))
      .map((detail): FindingSummary => ({
        id: detail.finding.id,
        run_id: detail.finding.run_id,
        created_at: detail.finding.created_at,
        title: detail.finding.payload.title,
        observations: detail.finding.observation_ids.length,
        hypotheses: detail.finding.hypothesis_ids.length,
        review_status: (() => {
          const record = reviewRecords.find((candidate) => candidate.subject.id === detail.finding.id);
          return record ? derivedStatus(record) : "new";
        })(),
      }));
    const { slice, total } = paged(url, all);
    return json({ items: slice, total });
  }

  if (method === "GET" && path === "/api/finding") {
    const id = url.searchParams.get("id") ?? "";
    const detail = findings[id];
    return detail ? json(detail) : json({ error: `synthetic finding not found: ${id}` }, 404);
  }

  if (method === "GET" && path === "/api/review/queue") {
    const type = url.searchParams.get("type") ?? "";
    const status = url.searchParams.get("status") ?? "";
    const all = reviewRecords
      .filter((record) => !type || record.subject.type === type)
      .filter((record) => {
        if (status === "all") return true;
        if (!status) return record.decisions.length === 0;
        return derivedStatus(record) === status;
      })
      .map((record): QueueItem => ({
        subject: record.subject,
        enrolled_at: record.enrolled_at,
        status: derivedStatus(record),
        decisions: record.decisions.length,
        last_decided_at: record.decisions[record.decisions.length - 1]?.recorded_at,
        refinements: record.refinements.length,
        excerpt: record.excerpt,
      }));
    const { slice, total } = paged(url, all);
    return json({ items: slice, total });
  }

  if (method === "POST" && path === "/api/review/context") {
    {
      const body = (await request.json()) as { text?: string };
      if (!body.text?.trim()) return json({ error: "context requires text" }, 400);
      contextCounter += 1;
      const id = `ctx_${String(contextCounter).padStart(3, "0")}`;
      contexts[id] = { id, author: "operator", at: new Date().toISOString(), text: body.text };
      return json({ id });
    }
  }

  if (method === "POST" && path === "/api/review/decide") {
    {
      const body = (await request.json()) as DecideRequest;
      const record = reviewRecords.find(
        (candidate) =>
          candidate.subject.type === body.subject?.type && candidate.subject.id === body.subject?.id,
      );
      if (!record) return json({ error: `synthetic record not found: ${body.subject?.id}` }, 404);
      if (!["accept", "reject", "defer", "duplicate"].includes(body.disposition)) {
        return json({ error: `unknown disposition: ${body.disposition}` }, 400);
      }
      if (body.disposition === "duplicate" && !body.duplicateOfId) {
        return json({ error: "duplicate disposition names no original" }, 400);
      }
      decisionCounter += 1;
      const event: DecisionView = {
        id: `dsp_${String(decisionCounter).padStart(3, "0")}`,
        sequence: record.decisions.length + 1,
        disposition: body.disposition,
        reviewer_id: "operator",
        recorded_at: new Date().toISOString(),
        duplicate_of_id: body.duplicateOfId,
        note: body.note,
        context: body.contextId ? contexts[body.contextId] : undefined,
      };
      record.decisions.push(event);
      return json({
        status: derivedStatus(record),
        event: {
          id: event.id,
          sequence: event.sequence,
          disposition: event.disposition,
          recorded_at: event.recorded_at,
        },
      });
    }
  }

  if (method === "GET" && path === "/api/review/history") {
    const type = url.searchParams.get("type") ?? "";
    const id = url.searchParams.get("id") ?? "";
    const record = reviewRecords.find(
      (candidate) => candidate.subject.type === type && candidate.subject.id === id,
    );
    if (!record) return json({ error: `synthetic record not found: ${id}` }, 404);
    return json({
      status: derivedStatus(record),
      decisions: record.decisions,
      refinements: record.refinements,
    });
  }

  if (method === "GET" && path === "/api/export") {
    const type = url.searchParams.get("type") ?? "";
    const id = url.searchParams.get("id") ?? "";
    const format = url.searchParams.get("format") ?? "json";
    const record =
      type === "hypothesis" ? hypotheses[id]?.hypothesis
        : type === "finding" ? findings[id]?.finding
          : type === "proposal" ? findingConflict.proposals.find((proposal) => proposal.id === id)
            : undefined;
    if (!record) return json({ error: `synthetic export target not found: ${type} ${id}` }, 404);
    if (format === "markdown") {
      const markdown =
        `> ${EXPORT_NOTICE}\n\n# ${type} ${id}\n\nSynthetic markdown export. Hostile markup must render as text: ${HOSTILE_MARKDOWN}\n`;
      return new Response(markdown, {
        headers: {
          "Content-Type": "text/markdown; charset=utf-8",
          "Cache-Control": "no-store",
          "X-Content-Type-Options": "nosniff",
        },
      });
    }
    return json({
      schema: 1,
      kind: type,
      id,
      exported_at: new Date().toISOString(),
      notice: EXPORT_NOTICE,
      redaction: { policy: "internal/preflight", redactions: 0 },
      [type]: record,
    });
  }

  if (method === "GET" && path === "/api/reality/inbox") {
    const ranked = [...questions].sort((left, right) => right.score - left.score);
    const { slice, total } = paged(url, ranked);
    return json({ items: slice, total });
  }

  if (method === "GET" && path === "/api/reality/entity") {
    const id = url.searchParams.get("id") ?? "";
    const detail = entities[id];
    return detail ? json(detail) : json({ error: `synthetic entity not found: ${id}` }, 404);
  }

  if (method === "POST" && path === "/api/reality/answer") {
    {
      const body = (await request.json()) as { questionId?: string; text?: string; outcome?: string };
      const question = questions.find((candidate) => candidate.id === body.questionId);
      if (!question) return json({ error: `synthetic question not found: ${body.questionId}` }, 404);
      const outcome = body.outcome ?? "answered";
      if (!["answered", "unknown", "declined"].includes(outcome)) {
        return json({ error: `unknown outcome: ${outcome}` }, 400);
      }
      if (outcome === "answered" && !body.text?.trim()) {
        return json({ error: "an answered outcome requires text" }, 400);
      }
      answerCounter += 1;
      const timestamp = new Date().toISOString();
      const answer: AnswerView = {
        id: `ans_${String(answerCounter).padStart(3, "0")}`,
        question_id: question.id,
        sequence: question.answers.length + 1,
        author: "operator",
        at: timestamp,
        recorded_at: timestamp,
        outcome,
        text: body.text ?? "",
      };
      question.answers.push(answer);
      question.state =
        outcome === "answered" ? "answered-uninterpreted"
          : outcome === "unknown" ? "answered"
            : "declined";
      return json({ answerId: answer.id, state: question.state });
    }
  }

  if (method === "POST" && path === "/api/reality/plan/accept") {
    {
      const body = (await request.json()) as { planId?: string };
      const question = questions.find((candidate) =>
        candidate.plans.some((plan) => plan.id === body.planId));
      const plan = question?.plans.find((candidate) => candidate.id === body.planId);
      if (!question || !plan) return json({ error: `synthetic plan not found: ${body.planId}` }, 404);
      if (plan.state !== "proposed") {
        return json({ error: `plan is ${plan.state}; only a proposed plan can be accepted` }, 409);
      }
      const timestamp = new Date().toISOString();
      plan.state = "accepted";
      question.state = "answered";
      const applied: Array<{ kind: string; id: string }> = [];
      for (const action of plan.actions) {
        if (action.state !== "pending-acceptance") continue;
        action.state = "applied";
        action.applied_at = timestamp;
        if (action.kind === "assert-fact") {
          action.result_id = "fct_policy-2";
          applied.push({ kind: "fact", id: "fct_policy-2" });
          {
            entityFacts.ent_atlas.unshift({
              id: "fct_policy-2",
              subject_id: "ent_atlas",
              predicate: "analysis-policy",
              value: { kind: "enum", enum: "normal" },
              valid_from: timestamp,
              observed_at: timestamp,
              recorded_at: timestamp,
              authority: { kind: "operator", id: "operator" },
              confidence: "high",
              sensitivity: "routine",
              status: "active",
              supersedes: "fct_policy-1",
              note: "Applied by accepting the synthetic interpreter plan.",
            });
          }
        } else if (action.kind === "change-focus-policy") {
          action.result_id = "focus_v3";
          applied.push({ kind: "focus", id: "3" });
        }
      }
      return json({ applied, state: question.state });
    }
  }

  if (method === "GET" && path === "/api/search") {
    const q = (url.searchParams.get("q") ?? "").toLocaleLowerCase();
    const harness = url.searchParams.get("harness") ?? "";
    const kind = url.searchParams.get("kind") ?? "";
    if (!q) return json({ error: "search requires q" }, 400);
    const terms = q.split(/\s+/u).filter(Boolean);
    const hits = searchHits
      .filter((hit) => !harness || hit.harness === harness)
      .filter((hit) => !kind || hit.kind === kind)
      .filter((hit) => terms.every((term) => hit.text.toLocaleLowerCase().includes(term)));
    return json({ hits });
  }

  return null;
}
