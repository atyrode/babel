/*
  The result contracts: what each stage and each role may submit, and what is refused. The cases
  that matter are the authority ones — a field a stage or role has no authority for is absent from
  its generated schema AND refused when submitted anyway, which is the pair that makes the table
  enforcement rather than documentation.
*/

import { expect, test } from "bun:test";
import { ROLES } from "../../contract.ts";
import {
  exploreJsonSchema,
  parseExploreResult,
  parseReviewResult,
  REFUSALS,
  ResultRefusal,
  reviewJsonSchema,
  type Role,
} from "./results.ts";

const LOCATOR = { path: "omp/session-1.jsonl", line: 12, byte_offset: 480, digest: "sha256:abc" };
const EVIDENCE = { locator: LOCATOR, note: "the transcript says the router retries twice" };
const RECIPE = { id: "read-whats-new", version: 3 };

const CLAIM = {
  claim: "the router retries a failed publish twice and then drops it",
  confidence: "moderate",
  impact: "high",
  evidence: [EVIDENCE],
  counter_evidence_absent: true,
};

/** The properties a generated schema offers, which is what a model is allowed to fill. */
function properties(schema: unknown): string[] {
  const document = schema as { properties?: Record<string, unknown> };
  return Object.keys(document.properties ?? {}).sort();
}

function refusal(run: () => unknown): ResultRefusal {
  try {
    run();
  } catch (error) {
    if (error instanceof ResultRefusal) return error;
    throw error;
  }
  throw new Error("the submission was accepted and should have been refused");
}

// ---------------------------------------------------------------------------- the exploration

test("a stage is offered exactly the fields its authority admits", () => {
  expect(properties(exploreJsonSchema("explore"))).toEqual([
    "candidates",
    "consolidations",
    "deferred",
    "questions",
    "rejected",
  ]);
  // §5.4 gives the challenger no path to a finding and no business developing observations.
  expect(properties(exploreJsonSchema("challenge"))).toEqual(["candidates", "objections", "questions"]);
  // The synthesizer gathers no new evidence and objects to nothing.
  expect(properties(exploreJsonSchema("synthesize"))).toEqual(["candidates", "consolidations", "questions"]);
});

test("a developed candidate with a remedy and a consolidation parses whole", () => {
  const result = parseExploreResult("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: { statement: "publishes are dropped silently", origin_cues: ["two retries"], priority: 0.6 },
        observations: [{ ref: "o1", recipe: RECIPE, claim: CLAIM }],
        remedy: {
          ref: "r1",
          proposal: {
            title: "log the dropped publish",
            problem: "a dropped publish is invisible",
            outcome: "the drop is logged with the reason",
            impact: "moderate",
            classification: "private",
          },
        },
      },
    ],
    consolidations: [
      {
        ref: "con1",
        observations: ["o1"],
        finding: { title: "silent drops", pattern: "retries exhaust and nothing is logged", counter_evidence_absent: true },
      },
    ],
    deferred: [{ hypothesis: "c1", reason: "out of budget for this pass" }],
    questions: [
      { ref: "q1", subjects: ["dev-01"], hypothesis: "c1", prompt: "which host runs the publisher?", why_asked: "two hosts claim it" },
    ],
  });

  expect(result.candidates).toHaveLength(1);
  expect(result.candidates[0]?.observations[0]?.claim.evidence[0]?.locator.digest).toBe("sha256:abc");
  expect(result.candidates[0]?.remedy?.ref).toBe("r1");
  expect(result.consolidations[0]?.observations).toEqual(["o1"]);
  expect(result.deferred[0]?.reason).toBe("out of budget for this pass");
  expect(result.questions[0]?.subjects).toEqual(["dev-01"]);
  // A stage that emitted no objections emitted none; the list is present and empty.
  expect(result.objections).toEqual([]);
});

test("a challenger that consolidates is refused, not trimmed", () => {
  const error = refusal(() =>
    parseExploreResult("challenge", {
      candidates: [],
      objections: [{ ref: "j1", hypothesis: "hyp_00000001", grounds: "evidence", recipe: RECIPE, claim: CLAIM }],
      consolidations: [{ ref: "con1", observations: ["o1"], finding: { title: "t", pattern: "p", counter_evidence_absent: true } }],
    }),
  );
  expect(error.refusal).toBe(REFUSALS.schema);
  expect(error.message).toContain("consolidations");
});

test("an observation with no evidence, or with both counter-evidence answers, is refused", () => {
  const bare = refusal(() =>
    parseExploreResult("explore", {
      candidates: [
        {
          ref: "c1",
          hypothesis: { statement: "s" },
          observations: [{ ref: "o1", recipe: RECIPE, claim: { ...CLAIM, evidence: [] } }],
        },
      ],
    }),
  );
  expect(bare.refusal).toBe(REFUSALS.schema);

  const both = refusal(() =>
    parseExploreResult("explore", {
      candidates: [
        {
          ref: "c1",
          hypothesis: { statement: "s" },
          observations: [
            { ref: "o1", recipe: RECIPE, claim: { ...CLAIM, counter_evidence: [EVIDENCE], counter_evidence_absent: true } },
          ],
        },
      ],
    }),
  );
  expect(both.message).toContain("counter_evidence");
});

test("a consolidation resting on a candidate rather than an observation skips the development path", () => {
  const error = refusal(() =>
    parseExploreResult("explore", {
      candidates: [{ ref: "c1", hypothesis: { statement: "s" }, observations: [] }],
      consolidations: [
        { ref: "con1", observations: ["c1"], finding: { title: "t", pattern: "p", counter_evidence_absent: true } },
      ],
    }),
  );
  expect(error.refusal).toBe(REFUSALS.developmentPath);
  expect(error.message).toContain("hypothesis rather than an observation");
});

test("a consolidation resting on a name nobody emitted or listed is refused", () => {
  const error = refusal(() =>
    parseExploreResult("explore", {
      candidates: [],
      consolidations: [
        { ref: "con1", observations: ["o-invented"], finding: { title: "t", pattern: "p", counter_evidence_absent: true } },
      ],
    }),
  );
  expect(error.refusal).toBe(REFUSALS.developmentPath);
});

test("a consolidation may rest on a durable observation identifier from the brief", () => {
  const result = parseExploreResult("synthesize", {
    consolidations: [
      {
        ref: "con1",
        observations: ["obs_0123456789abcdef"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
  });
  expect(result.consolidations[0]?.observations).toEqual(["obs_0123456789abcdef"]);
});

test("an objection on evidence grounds that cites none is refused", () => {
  const error = refusal(() =>
    parseExploreResult("challenge", {
      objections: [
        {
          ref: "j1",
          hypothesis: "hyp_0123456789abcdef",
          grounds: "evidence",
          recipe: RECIPE,
          claim: { ...CLAIM, evidence: [] },
        },
      ],
    }),
  );
  expect(error.refusal).toBe(REFUSALS.support);
});

test("an objection on alternative grounds carries no locator and is accepted", () => {
  const result = parseExploreResult("challenge", {
    objections: [
      {
        ref: "j1",
        hypothesis: "hyp_0123456789abcdef",
        grounds: "alternative",
        recipe: RECIPE,
        claim: { claim: "a queue would drop nothing", confidence: "low", impact: "moderate", evidence: [], counter_evidence_absent: true },
      },
    ],
  });
  expect(result.objections[0]?.grounds).toBe("alternative");
  expect(result.objections[0]?.claim.evidence).toEqual([]);
});

test("a ref used twice is refused, because later items name earlier ones by it", () => {
  const error = refusal(() =>
    parseExploreResult("explore", {
      candidates: [
        { ref: "c1", hypothesis: { statement: "a" }, observations: [] },
        { ref: "c1", hypothesis: { statement: "b" }, observations: [] },
      ],
    }),
  );
  expect(error.message).toContain("used twice");
});

// ---------------------------------------------------------------------------- the review

test("every role has a generated schema and a result it can submit", () => {
  const submission: Record<Role, unknown> = {
    reception: { vote: "support" },
    evidence: {
      results: [{ criterion_id: "crit_1", satisfied: true, evidence: [EVIDENCE] }],
      environment: "dev-01",
      as_of: "2026-09-12T10:00:00Z",
    },
    challenge: { contributions: [{ kind: "objection", text: "the second observation contradicts the first" }] },
    comparison: {
      contributions: [
        {
          kind: "comparison",
          text: "cheaper for the indexing path",
          alternatives: [
            { kind: "proposal", id: "pro_1111111111111111" },
            { kind: "proposal", id: "pro_2222222222222222" },
          ],
          preferred: { kind: "proposal", id: "pro_2222222222222222" },
        },
      ],
    },
    outcome: {
      outcome: "verified",
      results: [{ criterion_id: "crit_1", satisfied: true, evidence: [EVIDENCE] }],
      contributions: [{ kind: "evidence", evidence: [EVIDENCE] }],
      environment: "staging",
      as_of: "2026-09-12T10:00:00Z",
    },
    relevance: { contributions: [{ kind: "comment", text: "nobody is working on the publisher" }] },
    filing: { filing: { entity: "babel", rationale: "every cited session is a checkout of it" } },
    backlog: { keep: { reason: "the question it asks is still open and still worth asking" } },
  };
  for (const role of ROLES) {
    expect(properties(reviewJsonSchema(role)).length).toBeGreaterThan(0);
    const result = parseReviewResult(role, submission[role]);
    expect(result.skip).toBe("");
  }
});

test("a role is offered exactly the fields its authority admits", () => {
  expect(properties(reviewJsonSchema("reception"))).toEqual(["contributions", "skip", "uncertainty", "vote"]);
  expect(properties(reviewJsonSchema("evidence"))).toEqual([
    "as_of",
    "contributions",
    "environment",
    "results",
    "skip",
    "uncertainty",
  ]);
  // A filing or backlog pass records what it did and contributes nothing to the review.
  expect(properties(reviewJsonSchema("filing"))).toEqual([
    "filing",
    "no_change",
    "no_topic",
    "skip",
    "topic",
    "uncertainty",
  ]);
  expect(properties(reviewJsonSchema("backlog"))).toEqual([
    "consolidate",
    "keep",
    "promote",
    "retire",
    "skip",
    "supersede",
    "uncertainty",
  ]);
});

test("a role may not say what another role says", () => {
  // A comparison that could mint a global vote would turn "B is better here" into an endorsement.
  expect(refusal(() => parseReviewResult("comparison", { vote: "support" })).refusal).toBe(REFUSALS.schema);
  // A reception vote does not meet an evidence-check obligation.
  expect(refusal(() => parseReviewResult("reception", { results: [] })).refusal).toBe(REFUSALS.schema);
  // Only the outcome role reports what happened.
  expect(refusal(() => parseReviewResult("evidence", { outcome: "verified" })).refusal).toBe(REFUSALS.schema);
  // A filing pass is not reviewing the record.
  expect(
    refusal(() => parseReviewResult("filing", { contributions: [{ kind: "comment", text: "and it is weak" }] })).refusal,
  ).toBe(REFUSALS.schema);
  // And no other role decides what a record is about.
  expect(
    refusal(() => parseReviewResult("relevance", { filing: { entity: "babel", rationale: "because" } })).refusal,
  ).toBe(REFUSALS.schema);
});

test("a review that judged nothing is refused rather than consuming the assignment", () => {
  expect(refusal(() => parseReviewResult("reception", {})).refusal).toBe(REFUSALS.empty);
  expect(refusal(() => parseReviewResult("relevance", { uncertainty: "I could not tell" })).refusal).toBe(
    REFUSALS.empty,
  );
});

test("a skip is a recorded gap and cannot also state an assessment", () => {
  const skipped = parseReviewResult("reception", { skip: "the evidence is unreachable from here" });
  expect(skipped.skip).toBe("the evidence is unreachable from here");
  expect(skipped.vote).toBe("");
  expect(refusal(() => parseReviewResult("reception", { skip: "cannot judge", vote: "oppose" })).message).toContain(
    "skip cannot also state",
  );
});

test("an observed outcome needs evidence, a scope, and an uncertainty when unverifiable", () => {
  const unsupported = refusal(() =>
    parseReviewResult("outcome", { outcome: "verified", environment: "staging", as_of: "2026-09-12T10:00:00Z" }),
  );
  expect(unsupported.refusal).toBe(REFUSALS.support);
  expect(unsupported.message).toContain("needs evidence");

  const unscoped = refusal(() =>
    parseReviewResult("outcome", { outcome: "implemented", contributions: [{ kind: "evidence", evidence: [EVIDENCE] }] }),
  );
  expect(unscoped.message).toContain("environment");

  const unverifiable = refusal(() =>
    parseReviewResult("outcome", { outcome: "unverifiable", environment: "staging", as_of: "2026-09-12T10:00:00Z" }),
  );
  expect(unverifiable.message).toContain("could not be checked");

  // Unverifiable is the honest answer and needs no evidence, only the unknown recorded.
  const honest = parseReviewResult("outcome", {
    outcome: "unverifiable",
    environment: "staging",
    as_of: "2026-09-12T10:00:00Z",
    uncertainty: "the production window is not mine to open",
  });
  expect(honest.outcome).toBe("unverifiable");
});

test("a satisfied criterion with no evidence is the manufactured result §4.12 forbids", () => {
  const error = refusal(() =>
    parseReviewResult("evidence", {
      results: [{ criterion_id: "crit_1", satisfied: true }],
      environment: "dev-01",
      as_of: "2026-09-12T10:00:00Z",
    }),
  );
  expect(error.refusal).toBe(REFUSALS.support);
});

test("a review may not promote work its own run authored, but may argue against it", () => {
  const self = { target: true, subjects: { pro_2222222222222222: true as const } };
  expect(refusal(() => parseReviewResult("reception", { vote: "support" }, self)).refusal).toBe(REFUSALS.selfBoost);
  expect(parseReviewResult("reception", { vote: "oppose" }, self).vote).toBe("oppose");
  expect(
    refusal(() =>
      parseReviewResult(
        "comparison",
        {
          contributions: [
            {
              kind: "comparison",
              text: "mine is better",
              alternatives: [
                { kind: "proposal", id: "pro_1111111111111111" },
                { kind: "proposal", id: "pro_2222222222222222" },
              ],
              preferred: { kind: "proposal", id: "pro_2222222222222222" },
            },
          ],
        },
        self,
      ),
    ).refusal,
  ).toBe(REFUSALS.selfBoost);
});

test("a comparison needs two alternatives and may only prefer one it compared", () => {
  const alternatives = [
    { kind: "proposal", id: "pro_1111111111111111" },
    { kind: "proposal", id: "pro_2222222222222222" },
  ];
  expect(
    refusal(() =>
      parseReviewResult("comparison", {
        contributions: [{ kind: "comparison", text: "alone", alternatives: [alternatives[0]] }],
      }),
    ).message,
  ).toContain("at least two");
  expect(
    refusal(() =>
      parseReviewResult("comparison", {
        contributions: [
          { kind: "comparison", text: "elsewhere", alternatives, preferred: { kind: "proposal", id: "pro_3333333333333333" } },
        ],
      }),
    ).message,
  ).toContain("did not compare");
});

// ---------------------------------------------------------------------------- §4.13's two passes

test("the filing role's four answers parse, and exactly one may be given", () => {
  const filed = parseReviewResult("filing", {
    filing: { entity: "babel", rationale: "every cited session is a checkout of it" },
  });
  expect(filed.filing?.entity).toBe("babel");

  const nothing = parseReviewResult("filing", { no_topic: { reason: "this is about the process itself" } });
  expect(nothing.noTopic?.reason).toContain("process");

  const answered = parseReviewResult("filing", {
    no_change: { ask_id: "str_0001", reason: "the two topics name one thing and splitting them would cut across the records" },
  });
  expect(answered.noChange?.ask_id).toBe("str_0001");

  const proposed = parseReviewResult("filing", {
    topic: {
      operation: "create",
      name: "manifold",
      kind: "repository",
      identity: "github.com/atyrode/manifold",
      remote: "git@github.com:atyrode/manifold.git",
      reasoning: "the record is about a repository no listed entity names",
    },
  });
  expect(proposed.topic?.operation).toBe("create");

  expect(
    refusal(() =>
      parseReviewResult("filing", {
        filing: { entity: "babel", rationale: "r" },
        no_topic: { reason: "about nothing" },
      }),
    ).message,
  ).toContain("not 2 of them");
  expect(refusal(() => parseReviewResult("filing", {})).refusal).toBe(REFUSALS.empty);
});

test("each of §4.13's four topic operations parses with its own target arithmetic", () => {
  const create = parseReviewResult("filing", {
    topic: {
      operation: "create",
      name: "babel",
      kind: "project",
      identity: "babel",
      definition: "the analysis system this record is about",
      reasoning: "nothing listed names it",
    },
  });
  expect(create.topic?.targets).toEqual([]);

  const split = parseReviewResult("filing", {
    topic: {
      operation: "split",
      targets: ["ent_0000000000000001"],
      name: "babel-web",
      kind: "repository",
      identity: "github.com/atyrode/babel-web",
      remote: "git@github.com:atyrode/babel-web.git",
      reasoning: "the topic names two things and this record belongs to the second",
    },
  });
  expect(split.topic?.targets).toHaveLength(1);

  const merge = parseReviewResult("filing", {
    topic: {
      operation: "merge",
      targets: ["ent_0000000000000001", "ent_0000000000000002"],
      reasoning: "both names answer to one repository",
    },
  });
  expect(merge.topic?.operation).toBe("merge");

  const retire = parseReviewResult("filing", {
    topic: { operation: "retire", targets: ["ent_0000000000000003"], reasoning: "the topic was a directory, never a thing" },
  });
  expect(retire.topic?.operation).toBe("retire");

  // A merge of one topic, or a retirement of two, is not an act the ledger can perform.
  expect(
    refusal(() => parseReviewResult("filing", { topic: { operation: "merge", targets: ["ent_1"], reasoning: "r" } }))
      .message,
  ).toContain("names 2 existing topics, not 1");
  // A merge creates nothing, so a name on one is a claim it cannot make.
  expect(
    refusal(() =>
      parseReviewResult("filing", {
        topic: { operation: "merge", targets: ["ent_1", "ent_2"], name: "new", reasoning: "r" },
      }),
    ).message,
  ).toContain("creates no entity");
});

test("a created topic needs an admitted kind, a dedup identity and a binding to something real", () => {
  const base = { operation: "create" as const, name: "babel", identity: "babel", reasoning: "nothing names it" };
  expect(refusal(() => parseReviewResult("filing", { topic: { ...base, kind: "folder" } })).refusal).toBe(
    REFUSALS.vocabulary,
  );
  expect(
    refusal(() => parseReviewResult("filing", { topic: { ...base, kind: "project", identity: "" } })).message,
  ).toContain("identity that deduplicates");
  expect(refusal(() => parseReviewResult("filing", { topic: { ...base, kind: "project" } })).message).toContain(
    "topic with no binding is a folder",
  );
});

test("the backlog role's five answers parse, and exactly one may be given", () => {
  const consolidated = parseReviewResult("backlog", {
    consolidate: {
      hypotheses: ["hyp_0123456789abcdef"],
      finding: { title: "silent drops", pattern: "retries exhaust", why_it_matters: "publishes are lost" },
    },
  });
  expect(consolidated.consolidate?.hypotheses).toHaveLength(1);

  expect(parseReviewResult("backlog", { supersede: { by: "hyp_beef", reason: "says it with the counts" } }).supersede?.by).toBe(
    "hyp_beef",
  );
  expect(
    parseReviewResult("backlog", { retire: { reason: "the service it describes was decommissioned" } }).retire?.reason,
  ).toContain("decommissioned");
  const promoted = parseReviewResult("backlog", {
    promote: {
      observation: "obs_0123456789abcdef",
      entity: "dev-01",
      predicate: "service-placement",
      value: "publisher",
      reason: "it stays true until the service moves",
    },
  });
  expect(promoted.promote?.predicate).toBe("service-placement");
  expect(parseReviewResult("backlog", { keep: { reason: "still worth asking" } }).keep?.reason).toContain("asking");

  expect(
    refusal(() =>
      parseReviewResult("backlog", { retire: { reason: "a" }, keep: { reason: "b" } }),
    ).message,
  ).toContain("not 2 of them");
  expect(refusal(() => parseReviewResult("backlog", {})).refusal).toBe(REFUSALS.empty);
  // A predicate outside the ledger's vocabulary is refused by the schema the model was handed.
  expect(
    refusal(() =>
      parseReviewResult("backlog", {
        promote: { observation: "obs_1", entity: "dev-01", predicate: "vibes", value: "good", reason: "r" },
      }),
    ).refusal,
  ).toBe(REFUSALS.schema);
});
