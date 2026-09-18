import { describe, expect, test } from "bun:test";
import { ANSWER_FENCE } from "./prompts.ts";
import {
  MAX_REJECTED_SUBMISSION_BYTES,
  rejectedSubmission,
  reviewVerdict,
  type ReviewPreparation,
} from "./review.ts";

/*
  THE VERDICT ON ONE SEALED REVIEW (#305).

  Seven conductor-drawn reviews on a non-reasoning model spent 202k tokens and recorded two:
  five were discarded whole because ONE contribution broke a rule about itself. These tests hold
  the line the remedy has to stay behind — a refused contribution costs itself and nothing else,
  and it cannot become an accepted one on the way out.
*/

const RECORD = "rec_0000beef";

/** One assignment, at a refinement depth with room left so the bound is not what is under test. */
function preparation(over: Partial<ReviewPreparation> = {}): ReviewPreparation {
  return {
    assignmentId: "asg_1",
    recordId: RECORD,
    revisionId: RECORD,
    rootId: RECORD,
    kind: "hypothesis",
    role: "reception",
    lane: "coverage",
    policyVersion: "p1",
    fence: 1,
    ordinal: 0,
    seed: "seed",
    refinementDepth: 0,
    maxRefinementDepth: 2,
    inputDigest: "d".repeat(64),
    blinded: true,
    recipe: { id: "babel-triages-the-queue", version: 2 },
    ...over,
  };
}

/** The immutable record a review is shown, with one citable locator in it. */
const SERVED_DIGEST = "a".repeat(64);
const TARGET = {
  id: RECORD,
  kind: "hypothesis",
  payload: {
    statement: "the fleet catalog forgets archived sessions",
    evidence: [{ locator: { path: "sessions/0001-omp-s1.jsonl", digest: SERVED_DIGEST } }],
  },
};

function sealed(submission: unknown): string {
  return `Here is my assessment.\n\n${ANSWER_FENCE}\n${JSON.stringify(submission)}\n\`\`\``;
}

describe("a contribution the contract refuses costs itself and nothing else", () => {
  test("the surviving contributions and the vote are recorded, and the refusal is reported", () => {
    // The first of #305's own failures: an objection that names an alternative. It used to take
    // the vote and the second contribution with it.
    const verdict = reviewVerdict(
      preparation(),
      sealed({
        vote: "oppose",
        contributions: [
          {
            kind: "objection",
            text: "the statement does not survive the archived case",
            alternatives: [{ kind: "hypothesis", id: "hyp_other" }],
          },
          { kind: "comment", text: "the scope should name the harness" },
        ],
      }),
      TARGET,
    );

    expect(verdict.reason).toBe("");
    expect(verdict.result?.vote).toBe("oppose");
    expect(verdict.result?.contributions).toEqual([
      expect.objectContaining({ kind: "comment", text: "the scope should name the harness" }),
    ]);
    expect(verdict.refused).toEqual([
      { contribution: 1, reason: "schema: contribution 1 is an objection and may not name alternatives" },
    ]);
  });

  test("the refused contribution is dropped whole, never repaired into one that passes", () => {
    // THE LAUNDERING HAZARD. Stripping the `alternatives` off the objection and recording it
    // would turn a judgement the contract refused into one it accepted, in an altered form
    // nobody submitted. Nothing recorded may carry its text.
    const verdict = reviewVerdict(
      preparation(),
      sealed({
        vote: "support",
        contributions: [
          {
            kind: "objection",
            text: "this is the refused judgement",
            preferred: { kind: "hypothesis", id: "hyp_other" },
          },
          { kind: "comment", text: "this one stands on its own" },
        ],
      }),
      TARGET,
    );

    expect(verdict.result?.contributions).toHaveLength(1);
    expect(JSON.stringify(verdict.result)).not.toContain("this is the refused judgement");
    expect(JSON.stringify(verdict.result)).not.toContain("hyp_other");
    // The reason is the validator's own sentence and nothing besides it: no expected schema, no
    // example answer, nothing that says what the review should have concluded instead.
    expect(verdict.refused.map((refused) => refused.reason)).toEqual([
      "schema: contribution 1 is an objection and may not name alternatives",
    ]);
  });

  test("a citation the record never served refuses that contribution alone", () => {
    const verdict = reviewVerdict(
      preparation(),
      sealed({
        vote: "unsure",
        contributions: [
          {
            kind: "evidence",
            text: "the row is there",
            evidence: [{ locator: { path: "/payload/origin_cues/1", digest: "d5008f16" } }],
          },
          {
            kind: "evidence",
            text: "and so is this one",
            evidence: [{ locator: { path: "sessions/0001-omp-s1.jsonl", digest: SERVED_DIGEST } }],
          },
        ],
      }),
      TARGET,
    );

    expect(verdict.reason).toBe("");
    expect(verdict.result?.contributions).toHaveLength(1);
    expect(verdict.refused).toEqual([
      {
        contribution: 1,
        reason:
          "unknown-reference: /payload/origin_cues/1 at d5008f16 was not present in the record under review",
      },
    ]);
  });

  test("a target that is not an own field of the record is not a target", () => {
    // `toString` is reachable on every object and is part of no record. The same review against a
    // record that really carries the field is recorded.
    const refinement = {
      vote: "support",
      contributions: [
        {
          kind: "refinement",
          text: "replace an exact field",
          target: { path: "/payload/toString" },
          would_change: "replacement",
        },
      ],
    };

    expect(reviewVerdict(preparation(), sealed(refinement), TARGET).refused).toEqual([
      {
        contribution: 1,
        reason:
          'unknown-reference: contribution 1 targets "/payload/toString", which is not part of the record under review',
      },
    ]);
    const stored = reviewVerdict(preparation(), sealed(refinement), {
      ...TARGET,
      payload: { ...TARGET.payload, toString: "stored" },
    });
    expect(stored.refused).toEqual([]);
    expect(stored.result?.contributions).toHaveLength(1);
  });
});

describe("what is left has to stand on its own", () => {
  test("a review with nothing left fails, and names the refusal that took it", () => {
    // The review's whole material was one empty contribution: dropping it leaves a submission
    // that judges nothing, which the acceptance refuses as it always has. The reason still names
    // the DEFECT rather than its consequence, so a receipt stays comparable with #305's table,
    // and the refused contribution is reported beside it.
    const verdict = reviewVerdict(
      preparation(),
      sealed({ contributions: [{ kind: "comment" }, { kind: "comment", text: "" }] }),
      TARGET,
    );

    expect(verdict.result).toBeNull();
    expect(verdict.reason).toBe("empty: contribution 1 carries neither text nor evidence");
    expect(verdict.refused).toEqual([
      { contribution: 1, reason: "empty: contribution 1 carries neither text nor evidence" },
      { contribution: 2, reason: "empty: contribution 2 carries neither text nor evidence" },
    ]);
  });

  test("a claim whose only support was refused falls with it", () => {
    // An observed outcome resting on a citation the record never served. The contribution is
    // refused, and the outcome CANNOT be recorded without it: that is what stops a review from
    // keeping the judgement while losing the evidence for it.
    const verdict = reviewVerdict(
      preparation({ role: "outcome" }),
      sealed({
        outcome: "verified",
        environment: "dev-01",
        as_of: "2026-09-17T16:21:00Z",
        contributions: [
          {
            kind: "evidence",
            text: "the deployment reports it",
            evidence: [{ locator: { path: "sessions/invented.jsonl", digest: "b".repeat(64) } }],
          },
        ],
      }),
      TARGET,
    );

    expect(verdict.result).toBeNull();
    expect(verdict.reason).toBe("support: an observed outcome of verified needs evidence");
    expect(verdict.refused).toHaveLength(1);
  });

  test("a rule about the review as a whole is refused as a whole, with nothing kept", () => {
    // A skip that also states an assessment is not one contribution's fault and there is no
    // subset of it to keep. It now fails EARLIER — the shape a role is offered is a skip or an
    // assessment, so a submission carrying both matches neither form and never reaches the rule
    // that used to catch it (#311). Nothing is kept either way.
    const verdict = reviewVerdict(
      preparation(),
      sealed({
        skip: "the evidence is unreachable from here",
        vote: "oppose",
        contributions: [{ kind: "comment", text: "and it is weak anyway" }],
      }),
      TARGET,
    );

    expect(verdict.result).toBeNull();
    expect(verdict.reason).toContain("schema: the reception result does not match its schema");
    expect(verdict.refused).toEqual([]);
  });

  test("a refused review keeps what it submitted, and says how big one too large to keep was", () => {
    // THE MEASUREMENT #311 NEEDS. A reason string alone cannot answer "did that class of refusal
    // fall after the contract changed?" or "did the judgement change under refusal?", so the
    // answer the model actually sent travels with the verdict and lands on the receipt.
    const submission = {
      skip: "the evidence is unreachable from here",
      vote: "oppose",
      contributions: [{ kind: "comment", text: "and it is weak anyway" }],
    };
    const verdict = reviewVerdict(preparation(), sealed(submission), TARGET);
    expect(verdict.result).toBeNull();
    expect(verdict.submitted).toEqual(submission);
    expect(rejectedSubmission(verdict.submitted)).toEqual({
      bytes: JSON.stringify(submission).length,
      payload: submission,
    });

    // A session that answered nothing has no payload to keep, and no size to report either.
    expect(reviewVerdict(preparation(), "   ", TARGET).submitted).toBeNull();

    // Kept whole up to the bound, and reported as its size beyond it: a truncated answer is not
    // the answer anybody submitted.
    const oversized = { skip: "x".repeat(MAX_REJECTED_SUBMISSION_BYTES) };
    expect(rejectedSubmission(oversized)).toEqual({
      bytes: JSON.stringify(oversized).length,
      withheld: "too-large",
    });
  });

  test("a review with no answer at all is refused whole, with nothing to salvage", () => {
    const verdict = reviewVerdict(preparation(), "   ", TARGET);

    expect(verdict.result).toBeNull();
    expect(verdict.reason).toBe("schema: the session ended with no final message at all");
    expect(verdict.refused).toEqual([]);
  });
});
