/*
  The result contracts: what each stage and each role may submit, and what is refused. The cases
  that matter are the authority ones — a field a stage or role has no authority for is absent from
  its generated schema AND refused when submitted anyway, which is the pair that makes the table
  enforcement rather than documentation.
*/

import { expect, test } from "bun:test";
import { ROLES, type MaterialEntry } from "../contract.ts";
import {
  acceptReviewResult,
  exploreJsonSchema,
  exploreSubmission,
  parseReviewResult,
  refusalCode,
  REFUSALS,
  REVIEW_SCOPE_RULE,
  ResultRefusal,
  reviewJsonSchema,
  type ExploreResult,
  type ExploreSubmission,
  type Role,
  type Stage,
} from "./results.ts";

const LOCATOR = { path: "omp/session-1.jsonl", line: 12, byte_offset: 480, digest: "sha256:abc" };
const EVIDENCE = { locator: LOCATOR, note: "the transcript says the router retries twice" };
const RECIPE = { id: "read-whats-new", version: 3 };

/** The one session every citation below is served from, at the digest {@link LOCATOR} names. */
const SERVED: readonly MaterialEntry[] = [
  {
    selector: "omp/s1",
    harness: "omp",
    sourceId: "s1",
    captureDigest: "sha256:capture",
    sourceDigest: "sha256:abc",
    file: "omp/session-1.jsonl",
    records: 1,
    bytes: 10,
  },
];

/** One submission against the material above: what it kept, what it refused, and never a throw. */
function submit(stage: Stage, payload: unknown): ExploreSubmission {
  return exploreSubmission(stage, payload, SERVED);
}

/** The subset a submission kept, or the failure that it kept none. */
function kept(submission: ExploreSubmission): ExploreResult {
  if (submission.result === null) throw new Error(`refused whole: ${submission.reason}`);
  return submission.result;
}

const CLAIM = {
  claim: "the router retries a failed publish twice and then drops it",
  confidence: "moderate",
  impact: "high",
  evidence: [EVIDENCE],
  counter_evidence_absent: true,
};

/** The properties a generated schema offers, which is what a model is allowed to fill. */
/**
 * The fields the ASSESSMENT form of a role's schema offers. The generated schema is a union of
 * the two answers a role may give — a skip with its reason, or an assessment — so a reader of
 * either form has to say which one it is asking about (#311).
 */
function properties(schema: unknown): string[] {
  const document = schema as {
    properties?: Record<string, unknown>;
    anyOf?: readonly { properties?: Record<string, unknown> }[];
  };
  if (document.properties !== undefined) return Object.keys(document.properties).sort();
  const assessment = (document.anyOf ?? []).find(
    (form) => Object.keys(form.properties ?? {}).length > 1,
  );
  return Object.keys(assessment?.properties ?? {}).sort();
}

/** The fields the SKIP form offers, which is one and its reason. */
function skipForm(schema: unknown): string[] {
  const document = schema as {
    anyOf?: readonly { properties?: Record<string, unknown>; required?: string[] }[];
  };
  const form = (document.anyOf ?? []).find(
    (candidate) => Object.keys(candidate.properties ?? {}).length === 1,
  );
  return Object.keys(form?.properties ?? {}).sort();
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
    "next_actions",
    "questions",
    "rejected",
  ]);
  // §5.4 gives the challenger no path to a finding and no business developing observations, and
  // no business directing the operator's work off the back of a criticism either.
  expect(properties(exploreJsonSchema("challenge"))).toEqual([
    "candidates",
    "objections",
    "questions",
  ]);
  // The synthesizer gathers no new evidence and objects to nothing.
  expect(properties(exploreJsonSchema("synthesize"))).toEqual([
    "candidates",
    "consolidations",
    "next_actions",
    "questions",
  ]);
});

test("a developed candidate with a remedy and a consolidation parses whole", () => {
  const submission = submit("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: {
          statement: "publishes are dropped silently",
          origin_cues: ["two retries"],
          priority: 0.6,
        },
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
        finding: {
          title: "silent drops",
          pattern: "retries exhaust and nothing is logged",
          counter_evidence_absent: true,
        },
      },
    ],
    deferred: [{ hypothesis: "c1", reason: "out of budget for this pass" }],
    questions: [
      {
        ref: "q1",
        subjects: ["dev-01"],
        hypothesis: "c1",
        prompt: "which host runs the publisher?",
        why_asked: "two hosts claim it",
      },
    ],
  });

  expect(submission.refused).toEqual([]);
  expect(submission.reason).toBe("");
  const result = kept(submission);
  expect(result.candidates).toHaveLength(1);
  expect(result.candidates[0]?.observations[0]?.claim.evidence[0]?.locator.digest).toBe(
    "sha256:abc",
  );
  expect(result.candidates[0]?.remedy?.ref).toBe("r1");
  expect(result.consolidations[0]?.observations).toEqual(["o1"]);
  expect(result.deferred[0]?.reason).toBe("out of budget for this pass");
  expect(result.questions[0]?.subjects).toEqual(["dev-01"]);
  // A stage that emitted no objections emitted none; the list is present and empty.
  expect(result.objections).toEqual([]);
});

test("an observation states the repository its evidence recorded, in one spelling", () => {
  /*
    The point of canonicalizing here rather than at the reader: the observed-versus-named
    decision downstream compares this remote against the catalog's, which the machine half
    already wrote canonically, and `git@github.com:atyrode/babel.git` compared as a string
    against `github.com/atyrode/babel` would report a repository Babel probed as one a
    transcript merely mentioned.
  */
  const result = kept(
    submit("explore", {
      candidates: [
        {
          ref: "c1",
          hypothesis: { statement: "the drain over-commits a cycle" },
          observations: [
            {
              ref: "o1",
              recipe: RECIPE,
              claim: {
                ...CLAIM,
                repository: {
                  remote: "git@github.com:atyrode/babel.git",
                  commit: "9C44AAF1AB3C4D5E6F7089ABCDEF0123456789AB",
                  reference: "https://github.com/atyrode/babel/pull/377",
                },
              },
            },
          ],
        },
      ],
    }),
  );

  expect(result.candidates[0]?.observations[0]?.claim.repository).toEqual({
    remote: "github.com/atyrode/babel",
    commit: "9c44aaf1ab3c4d5e6f7089abcdef0123456789ab",
    reference: "https://github.com/atyrode/babel/pull/377",
  });
});

test("a repository claim naming a directory, or a commit that is not one, is not a repository", () => {
  const stated = (repository: unknown): ExploreSubmission =>
    submit("explore", {
      candidates: [
        {
          ref: "c1",
          hypothesis: { statement: "the drain over-commits a cycle" },
          observations: [{ ref: "o1", recipe: RECIPE, claim: { ...CLAIM, repository } }],
        },
      ],
    });

  // A path remote names a directory on one machine, which is a locator and not a project; it
  // empties rather than refusing the answer, and the reader reads an empty remote as absent.
  expect(
    kept(stated({ remote: "/srv/git/thing" })).candidates[0]?.observations[0]?.claim.repository,
  ).toEqual({ remote: "", commit: "", reference: "" });
  // A commit is checkable on its face, so a value that cannot be one is refused rather than
  // stored: an unreadable sha would be shown to a reader as the position the run read. The
  // CANDIDATE still stands — the claim that misread its own provenance is the item at fault.
  const unreadable = stated({ remote: "github.com/atyrode/babel", commit: "HEAD~2" });
  expect(kept(unreadable).candidates[0]?.observations).toEqual([]);
  expect(unreadable.refused).toEqual([
    { item: "/candidates/0/observations/0", reason: expect.stringContaining(REFUSALS.schema) },
  ]);
  // And a bare issue number names a number in whatever project the reader assumes.
  expect(
    stated({ remote: "github.com/atyrode/babel", reference: "#312" }).refused[0]?.reason,
  ).toStartWith(`${REFUSALS.schema}:`);
});

test("a challenger that consolidates loses the consolidation and keeps its criticism", () => {
  /*
    Authority is enforced by ABSENCE where absence is what the model sees — `challenge` is never
    offered a `consolidations` field — and by refusal when a payload carries one anyway. What
    changed with #231 is the blast radius: the field it had no authority for costs itself, and
    the criticism beside it, which the stage did have authority for and which was paid for, is
    recorded rather than thrown away with it.
  */
  const submission = submit("challenge", {
    candidates: [],
    objections: [
      {
        ref: "j1",
        hypothesis: "hyp_00000001",
        grounds: "evidence",
        recipe: RECIPE,
        claim: CLAIM,
      },
    ],
    consolidations: [
      {
        ref: "con1",
        observations: ["o1"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
  });

  expect(kept(submission).objections[0]?.ref).toBe("j1");
  expect(kept(submission).consolidations).toEqual([]);
  expect(submission.refused).toEqual([
    {
      item: "/consolidations/0",
      reason: `${REFUSALS.authority}: a challenge result may not carry consolidations`,
    },
  ]);
});

test("an observation with no evidence, or with both counter-evidence answers, costs its own item", () => {
  const bare = submit("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: { statement: "s" },
        observations: [{ ref: "o1", recipe: RECIPE, claim: { ...CLAIM, evidence: [] } }],
      },
    ],
  });
  expect(bare.refused[0]?.item).toBe("/candidates/0/observations/0");
  expect(bare.refused[0]?.reason).toStartWith(`${REFUSALS.schema}:`);
  // THE HYPOTHESIS STANDS. It rests on nothing, so the claim that failed to develop it takes
  // only itself: #231's third cause was exactly one observation missing one obligation, and it
  // used to cost the whole run.
  expect(kept(bare).candidates[0]?.hypothesis.statement).toBe("s");
  expect(kept(bare).candidates[0]?.observations).toEqual([]);

  const both = submit("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: { statement: "s" },
        observations: [
          {
            ref: "o1",
            recipe: RECIPE,
            claim: { ...CLAIM, counter_evidence: [EVIDENCE], counter_evidence_absent: true },
          },
        ],
      },
    ],
  });
  expect(both.refused[0]?.reason).toContain("counter_evidence");
});

test("a consolidation resting on a candidate rather than an observation skips the development path", () => {
  const submission = submit("explore", {
    candidates: [{ ref: "c1", hypothesis: { statement: "s" }, observations: [] }],
    consolidations: [
      {
        ref: "con1",
        observations: ["c1"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
  });
  expect(submission.refused[0]?.item).toBe("/consolidations/0");
  expect(submission.refused[0]?.reason).toStartWith(`${REFUSALS.developmentPath}:`);
  expect(submission.refused[0]?.reason).toContain("hypothesis rather than an observation");
  expect(kept(submission).candidates).toHaveLength(1);
});

test("a consolidation resting on a name nobody emitted or listed is refused", () => {
  const submission = submit("explore", {
    candidates: [],
    consolidations: [
      {
        ref: "con1",
        observations: ["o-invented"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
  });
  // The only item it carried, so there is nothing left to keep and the submission stands refused.
  expect(submission.result).toBeNull();
  expect(submission.reason).toStartWith(`${REFUSALS.developmentPath}:`);
});

test("a consolidation may rest on a durable observation identifier from the brief", () => {
  const result = kept(
    submit("synthesize", {
      consolidations: [
        {
          ref: "con1",
          observations: ["obs_0123456789abcdef"],
          finding: { title: "t", pattern: "p", counter_evidence_absent: true },
        },
      ],
    }),
  );
  expect(result.consolidations[0]?.observations).toEqual(["obs_0123456789abcdef"]);
});

test("an objection on evidence grounds that cites none is refused", () => {
  const submission = submit("challenge", {
    objections: [
      {
        ref: "j1",
        hypothesis: "hyp_0123456789abcdef",
        grounds: "evidence",
        recipe: RECIPE,
        claim: { ...CLAIM, evidence: [] },
      },
    ],
  });
  expect(submission.result).toBeNull();
  expect(submission.reason).toStartWith(`${REFUSALS.support}:`);
});

test("an objection on alternative grounds carries no locator and is accepted", () => {
  const result = kept(
    submit("challenge", {
      objections: [
        {
          ref: "j1",
          hypothesis: "hyp_0123456789abcdef",
          grounds: "alternative",
          recipe: RECIPE,
          claim: {
            claim: "a queue would drop nothing",
            confidence: "low",
            impact: "moderate",
            evidence: [],
            counter_evidence_absent: true,
          },
        },
      ],
    }),
  );
  expect(result.objections[0]?.grounds).toBe("alternative");
  expect(result.objections[0]?.claim.evidence).toEqual([]);
});

test("a ref used twice costs the later item, because earlier ones were named by it", () => {
  const submission = submit("explore", {
    candidates: [
      { ref: "c1", hypothesis: { statement: "a" }, observations: [] },
      { ref: "c1", hypothesis: { statement: "b" }, observations: [] },
    ],
  });
  expect(submission.refused).toEqual([
    { item: "/candidates/1", reason: expect.stringContaining("used twice") },
  ]);
  // The FIRST declaration stands: every reference in the answer was written against it, and
  // minting both would have collapsed two claims into one row at the ingest.
  expect(kept(submission).candidates).toEqual([
    expect.objectContaining({ hypothesis: expect.objectContaining({ statement: "a" }) }),
  ]);
});

// ------------------------------------------------- a submission kept in part (#231, #311)

test("one unusable disposition costs itself and the paid records around it stand", () => {
  /*
    The three causes of #231, submitted together in one answer: a `draft-issue` naming no
    workspace (the disposition this machine cannot act on), an objection attacking an id nobody
    holds, and a citation of a file the run was never served. Each used to fail the run.
  */
  const submission = submit("explore", {
    candidates: [
      { ref: "c1", hypothesis: { statement: "the router drops publishes" } },
      {
        ref: "c2",
        hypothesis: { statement: "the catalog forgets sessions" },
        observations: [{ ref: "o1", recipe: RECIPE, claim: CLAIM }],
      },
      {
        ref: "c3",
        hypothesis: { statement: "the reaper reads a sealed session twice" },
        observations: [
          {
            ref: "o2",
            recipe: RECIPE,
            claim: {
              ...CLAIM,
              evidence: [{ locator: { ...LOCATOR, path: "omp/never-served.jsonl" }, note: "n" }],
            },
          },
        ],
      },
    ],
    consolidations: [
      {
        ref: "con1",
        observations: ["o1"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
    next_actions: [
      { record: "c1", kind: "draft-issue", summary: "file it", workspace: "" },
      { record: "c2", kind: "develop-further", summary: "keep reading" },
    ],
    questions: [
      { ref: "q1", subjects: ["dev-01"], prompt: "which host?", why_asked: "two claim it" },
    ],
  });

  const result = kept(submission);
  expect(result.candidates.map((candidate) => candidate.ref)).toEqual(["c1", "c2", "c3"]);
  expect(result.consolidations).toHaveLength(1);
  expect(result.next_actions.map((action) => action.kind)).toEqual(["develop-further"]);
  expect(result.questions).toHaveLength(1);
  // The two items at fault, named by position and by reason, and nothing else lost.
  expect(submission.refused).toEqual([
    {
      item: "/candidates/2/observations/0",
      reason: expect.stringContaining("omp/never-served.jsonl, which is not a file this run"),
    },
    {
      item: "/next_actions/0",
      reason: `${REFUSALS.support}: the draft-issue proposed on "c1" names no workspace, so the issue would be about no repository`,
    },
  ]);
});

test("a refused candidate takes what rested on it and nothing else", () => {
  /*
    What "kept" means for a set: the largest subset closed under §4.2's development path. The
    candidate's own claim is unreadable, so its observation cannot hang off a hypothesis that
    does not exist, and the finding consolidating that observation would rest on nothing. Both
    fall WITH IT and say so; the unrelated candidate beside them does not.
  */
  const submission = submit("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: { statement: "" },
        observations: [{ ref: "o1", recipe: RECIPE, claim: CLAIM }],
      },
      { ref: "c2", hypothesis: { statement: "the catalog forgets sessions" } },
      { ref: "c3", hypothesis: { statement: "a third reading" } },
      { ref: "c4", hypothesis: { statement: "a fourth reading" } },
    ],
    consolidations: [
      {
        ref: "con1",
        observations: ["o1"],
        finding: { title: "t", pattern: "p", counter_evidence_absent: true },
      },
    ],
  });

  expect(kept(submission).candidates.map((candidate) => candidate.ref)).toEqual(["c2", "c3", "c4"]);
  expect(kept(submission).consolidations).toEqual([]);
  expect(submission.refused).toEqual([
    { item: "/candidates/0", reason: expect.stringContaining(REFUSALS.schema) },
    {
      item: "/candidates/0/observations/0",
      reason: `${REFUSALS.developmentPath}: the candidate this observation develops was refused`,
    },
    {
      item: "/consolidations/0",
      reason: `${REFUSALS.developmentPath}: consolidation "con1" rests on "o1", which this submission refused`,
    },
  ]);
});

test("a submission that keeps less than the floor is refused whole, and says what it was", () => {
  /*
    The floor is the one judgement in this path (`SUBMISSION_KEPT_FLOOR`): below it the model
    demonstrably was not writing against this contract, and the items that happened to parse are
    then likely wrong in ways no schema sees. The receipt still gets every refusal — the run is
    spend either way, and the refusals are the measurement the spend bought.
  */
  const submission = submit("explore", {
    candidates: [
      { ref: "c1", hypothesis: { statement: "the one readable claim" } },
      { ref: "c2", hypothesis: { statement: "" } },
      { ref: "c3", hypothesis: { statement: "" } },
    ],
  });

  expect(submission.result).toBeNull();
  expect(submission.refused.map((item) => item.item)).toEqual(["/candidates/1", "/candidates/2"]);
  // The DEFECT leads the sentence, so `refusalCode` still counts the class the operator acts on.
  expect(refusalCode(submission.reason)).toBe(REFUSALS.schema);
  expect(submission.reason).toContain("2 of this submission's 3 items were refused");
  expect(submission.reason).toContain("50%");
});

test("exactly half kept clears the floor, because the comparison is inclusive", () => {
  const submission = submit("explore", {
    candidates: [
      { ref: "c1", hypothesis: { statement: "the readable claim" } },
      { ref: "c2", hypothesis: { statement: "" } },
    ],
  });
  expect(kept(submission).candidates.map((candidate) => candidate.ref)).toEqual(["c1"]);
});

test("a cascade counts against the floor, so a whole path resting on one bad claim is refused", () => {
  /*
    The floor is a share of the items SUBMITTED, and a cascade is an item this submission did
    not deliver whatever its own shape was. Two of these three items fall because the candidate
    they hang off is unreadable; counting only the item whose own schema failed would leave two
    of three kept and let the whole broken path through, which is the reward for having built
    everything on one bad claim that the floor exists to withhold.
  */
  const submission = submit("explore", {
    candidates: [
      {
        ref: "c1",
        hypothesis: { statement: "" },
        observations: [{ ref: "o1", recipe: RECIPE, claim: CLAIM }],
      },
      { ref: "c2", hypothesis: { statement: "the one readable claim" } },
    ],
  });

  expect(submission.result).toBeNull();
  expect(submission.refused.map((item) => item.item)).toEqual([
    "/candidates/0",
    "/candidates/0/observations/0",
  ]);
  expect(submission.reason).toContain("2 of this submission's 3 items were refused");
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
    challenge: {
      contributions: [{ kind: "objection", text: "the second observation contradicts the first" }],
    },
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
  expect(properties(reviewJsonSchema("reception"))).toEqual([
    "contributions",
    "skip",
    "uncertainty",
    "vote",
  ]);
  // Every role may decline, and declining is its own answer rather than a field beside a vote.
  for (const role of ROLES) expect(skipForm(reviewJsonSchema(role))).toEqual(["skip"]);
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
  expect(refusal(() => parseReviewResult("comparison", { vote: "support" })).refusal).toBe(
    REFUSALS.schema,
  );
  // A reception vote does not meet an evidence-check obligation.
  expect(refusal(() => parseReviewResult("reception", { results: [] })).refusal).toBe(
    REFUSALS.schema,
  );
  // Only the outcome role reports what happened.
  expect(refusal(() => parseReviewResult("evidence", { outcome: "verified" })).refusal).toBe(
    REFUSALS.schema,
  );
  // A filing pass is not reviewing the record.
  expect(
    refusal(() =>
      parseReviewResult("filing", { contributions: [{ kind: "comment", text: "and it is weak" }] }),
    ).refusal,
  ).toBe(REFUSALS.schema);
  // And no other role decides what a record is about.
  expect(
    refusal(() =>
      parseReviewResult("relevance", { filing: { entity: "babel", rationale: "because" } }),
    ).refusal,
  ).toBe(REFUSALS.schema);
});

test("a review that judged nothing is refused rather than consuming the assignment", () => {
  expect(refusal(() => parseReviewResult("reception", {})).refusal).toBe(REFUSALS.empty);
  expect(
    refusal(() => parseReviewResult("relevance", { uncertainty: "I could not tell" })).refusal,
  ).toBe(REFUSALS.empty);
});

test("a skip is a recorded gap, and the contract it is submitted under cannot spell one beside a vote", () => {
  const skipped = parseReviewResult("reception", { skip: "the evidence is unreachable from here" });
  expect(skipped.skip).toBe("the evidence is unreachable from here");
  expect(skipped.vote).toBe("");
  // REFUSED AT THE SCHEMA BOUNDARY, not by the rule further in: the shape a role is offered is a
  // skip OR an assessment, so a submission carrying both matches neither form (#311).
  const both = refusal(() =>
    parseReviewResult("reception", { skip: "cannot judge", vote: "oppose" }),
  );
  expect(both.refusal).toBe(REFUSALS.schema);
  expect(both.message).toContain("does not match its schema");
  // A model that echoes the field empty beside its assessment is submitting an assessment, which
  // has always been valid and stays valid — the change must not trade one refusal class for another.
  expect(parseReviewResult("reception", { skip: "", vote: "support" }).vote).toBe("support");
  // The validator is the ENFORCEMENT and not a belt, because the schema above is printed into the
  // prompt rather than constraining the model: a hand-written payload still gets the sentence.
  expect(
    refusal(() => acceptReviewResult("reception", { skip: "cannot judge", vote: "oppose" }))
      .message,
  ).toContain("skip cannot also state");
});

test("a refinement names the exact record part and the replacement it proposes", () => {
  expect(
    refusal(() =>
      parseReviewResult("challenge", {
        contributions: [
          { kind: "refinement", text: "the scope is ambiguous", would_change: "name the host" },
        ],
      }),
    ).message,
  ).toContain("names no part");
  expect(
    refusal(() =>
      parseReviewResult("challenge", {
        contributions: [
          { kind: "refinement", text: "the scope is ambiguous", target: { path: "/problem" } },
        ],
      }),
    ).message,
  ).toContain("both a reason and the change");
  const result = parseReviewResult("challenge", {
    contributions: [
      {
        kind: "refinement",
        text: "the scope is ambiguous",
        target: { path: "/payload/problem" },
        would_change: "name the affected host",
      },
    ],
  });
  expect(result.contributions[0]?.target?.path).toBe("/payload/problem");
});

/*
  F8, the drain of 2026-09-13's most expensive bug: the Go review contract required an environment
  on criterion results, the Go store refused any environment without an OUTCOME, and a
  results-only assessment counted as empty — so an evidence check, which is exactly a criterion
  result with no outcome, was paid for and then refused at submit. One validator states the rule
  once, in both directions.
*/

test("an evidence check states criterion results with an environment and no outcome", () => {
  const checked = parseReviewResult("evidence", {
    results: [{ criterion_id: "crit_1", satisfied: true, evidence: [EVIDENCE] }],
    environment: "dev-01",
    as_of: "2026-09-12T10:00:00Z",
  });
  expect(checked.outcome).toBe("");
  expect(checked.results).toHaveLength(1);
  expect(checked.environment).toBe("dev-01");
  // It judged something: criterion results are an assessment, not an empty one.
  expect(checked.vote).toBe("");
});

test("an environment with neither an outcome nor a criterion result scopes nothing", () => {
  const alone = refusal(() =>
    parseReviewResult("evidence", {
      contributions: [{ kind: "comment", text: "the criteria are not stated on the record" }],
      environment: "dev-01",
      as_of: "2026-09-12T10:00:00Z",
    }),
  );
  expect(alone.refusal).toBe(REFUSALS.schema);
  expect(alone.message).toContain("neither an outcome nor a criterion result");
});

test("the scope rule is the refusal's own wording", () => {
  const unscoped = refusal(() =>
    parseReviewResult("evidence", { results: [{ criterion_id: "crit_1", satisfied: false }] }),
  );
  expect(unscoped.message).toContain(REVIEW_SCOPE_RULE);
});

test("an observed outcome needs evidence, a scope, and an uncertainty when unverifiable", () => {
  const unsupported = refusal(() =>
    parseReviewResult("outcome", {
      outcome: "verified",
      environment: "staging",
      as_of: "2026-09-12T10:00:00Z",
    }),
  );
  expect(unsupported.refusal).toBe(REFUSALS.support);
  expect(unsupported.message).toContain("needs evidence");

  const unscoped = refusal(() =>
    parseReviewResult("outcome", {
      outcome: "implemented",
      contributions: [{ kind: "evidence", evidence: [EVIDENCE] }],
    }),
  );
  expect(unscoped.message).toContain("environment");

  const unverifiable = refusal(() =>
    parseReviewResult("outcome", {
      outcome: "unverifiable",
      environment: "staging",
      as_of: "2026-09-12T10:00:00Z",
    }),
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
  expect(refusal(() => parseReviewResult("reception", { vote: "support" }, self)).refusal).toBe(
    REFUSALS.selfBoost,
  );
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
          {
            kind: "comparison",
            text: "elsewhere",
            alternatives,
            preferred: { kind: "proposal", id: "pro_3333333333333333" },
          },
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

  const nothing = parseReviewResult("filing", {
    no_topic: { reason: "this is about the process itself" },
  });
  expect(nothing.noTopic?.reason).toContain("process");

  const answered = parseReviewResult("filing", {
    no_change: {
      ask_id: "str_0001",
      reason: "the two topics name one thing and splitting them would cut across the records",
    },
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
    topic: {
      operation: "retire",
      targets: ["ent_0000000000000003"],
      reasoning: "the topic was a directory, never a thing",
    },
  });
  expect(retire.topic?.operation).toBe("retire");

  // A merge of one topic, or a retirement of two, is not an act the ledger can perform.
  expect(
    refusal(() =>
      parseReviewResult("filing", {
        topic: { operation: "merge", targets: ["ent_1"], reasoning: "r" },
      }),
    ).message,
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
  const base = {
    operation: "create" as const,
    name: "babel",
    identity: "babel",
    reasoning: "nothing names it",
  };
  expect(
    refusal(() => parseReviewResult("filing", { topic: { ...base, kind: "folder" } })).refusal,
  ).toBe(REFUSALS.vocabulary);
  expect(
    refusal(() =>
      parseReviewResult("filing", { topic: { ...base, kind: "project", identity: "" } }),
    ).message,
  ).toContain("identity that deduplicates");
  expect(
    refusal(() => parseReviewResult("filing", { topic: { ...base, kind: "project" } })).message,
  ).toContain("topic with no binding is a folder");
});

test("the backlog role's five answers parse, and exactly one may be given", () => {
  const consolidated = parseReviewResult("backlog", {
    consolidate: {
      hypotheses: ["hyp_0123456789abcdef"],
      finding: {
        title: "silent drops",
        pattern: "retries exhaust",
        why_it_matters: "publishes are lost",
      },
    },
  });
  expect(consolidated.consolidate?.hypotheses).toHaveLength(1);

  expect(
    parseReviewResult("backlog", {
      supersede: { by: "hyp_beef", reason: "says it with the counts" },
    }).supersede?.by,
  ).toBe("hyp_beef");
  expect(
    parseReviewResult("backlog", {
      retire: { reason: "the service it describes was decommissioned" },
    }).retire?.reason,
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
  expect(
    parseReviewResult("backlog", { keep: { reason: "still worth asking" } }).keep?.reason,
  ).toContain("asking");

  expect(
    refusal(() => parseReviewResult("backlog", { retire: { reason: "a" }, keep: { reason: "b" } }))
      .message,
  ).toContain("not 2 of them");
  expect(refusal(() => parseReviewResult("backlog", {})).refusal).toBe(REFUSALS.empty);
  // A predicate outside the ledger's vocabulary is refused by the schema the model was handed.
  expect(
    refusal(() =>
      parseReviewResult("backlog", {
        promote: {
          observation: "obs_1",
          entity: "dev-01",
          predicate: "vibes",
          value: "good",
          reason: "r",
        },
      }),
    ).refusal,
  ).toBe(REFUSALS.schema);
});
