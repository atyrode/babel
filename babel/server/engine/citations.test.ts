import { describe, expect, test } from "bun:test";
import {
  CITATION_OUTCOMES,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  type MaterialEntry,
} from "../../contract.ts";
import { parseExploreResult, type Evidence, type ExploreResult } from "../../machine/results.ts";
import {
  admitCitation,
  checkCitations,
  checkQuote,
  citationNotes,
  citationTally,
  materialLines,
  unservedCitation,
} from "./citations.ts";

/*
  THE TWO QUESTIONS A CITATION HAS TO ANSWER (#343, #348).

  The scope half is a security boundary: a claim may point only at bytes this run was served,
  and every shape that tries to leave the material has to be refused rather than resolved. The
  quote half is an accuracy check with a bias: it may never accuse an honest quote, which is
  what the normalisation exists for, and it must still catch an invention.
*/

const DIGEST = `sha256:${"a".repeat(64)}`;
const FILE = "0001-omp-s1.jsonl";

const SERVED: readonly MaterialEntry[] = [
  {
    selector: "omp/s1",
    harness: "omp",
    sourceId: "s1",
    captureDigest: `sha256:${"c".repeat(64)}`,
    sourceDigest: DIGEST,
    file: FILE,
    records: 3,
    bytes: 4096,
  },
];

/** A material file as `prepare` writes it: one canonical JSON record per line. */
const SESSION = materialLines(
  [
    JSON.stringify({ type: "message", text: "the router retries twice before it gives up" }),
    JSON.stringify({ type: "message", text: "we moved the queue to staging on Tuesday" }),
    JSON.stringify({
      type: "message",
      text: "the deploy role reads\nthe object store directly, which nobody documented",
    }),
    "",
  ].join("\n"),
);

/** One parsed answer citing one locator, the way a submission reaches the settlement. */
function cited(locator: Record<string, unknown>): ExploreResult {
  return parseExploreResult("explore", {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "the router's retry budget is wrong" },
        observations: [
          {
            ref: "o1",
            recipe: { id: "code-health", version: 3 },
            claim: {
              claim: "the transcript states the retry count",
              confidence: "high",
              impact: "moderate",
              evidence: [{ locator, note: "the retry sentence" }],
              counter_evidence_absent: true,
            },
          },
        ],
      },
    ],
  });
}

function onlyCheck(result: ExploreResult, lines: readonly string[] | null = SESSION) {
  const checks = checkCitations(result, SERVED, () => lines);
  const evidence = result.candidates[0]?.observations[0]?.claim.evidence[0] as Evidence;
  return checks.get(evidence);
}

// ---------------------------------------------------------------------- the scope (#343)

describe("a claim cites the corpus it was served, or it cites nothing", () => {
  test("the three spellings the prompt uses all name the same served entry", () => {
    expect(admitCitation(FILE, SERVED)?.file).toBe(FILE);
    expect(admitCitation(`${MATERIAL_SESSIONS}/${FILE}`, SERVED)?.file).toBe(FILE);
    expect(admitCitation(`${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${FILE}`, SERVED)?.file).toBe(FILE);
    expect(unservedCitation(cited({ path: FILE, line: 1, digest: DIGEST }), SERVED)).toBe("");
  });

  test("a traversal is refused even when it would resolve back inside the material", () => {
    // Every one of these names the served file under a resolver that normalises `..`, which is
    // exactly why none of them is admitted: the boundary is the index, not a resolver.
    for (const path of [
      `${MATERIAL_SESSIONS}/../${MATERIAL_SESSIONS}/${FILE}`,
      `${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/../${MATERIAL_SESSIONS}/${FILE}`,
      `../${MATERIAL_SESSIONS}/${FILE}`,
      `${MATERIAL_SESSIONS}/subdir/../${FILE}`,
    ]) {
      expect(admitCitation(path, SERVED)).toBeNull();
      expect(unservedCitation(cited({ path, line: 1, digest: DIGEST }), SERVED)).toContain(
        "not a file this run was served",
      );
    }
  });

  test("a path out of the material is refused whatever shape it arrives in", () => {
    for (const path of [
      "/etc/passwd",
      `${MATERIAL_ROOT}/../../etc/passwd`,
      `..%2f..%2fetc%2fpasswd`,
      `${MATERIAL_SESSIONS}\\${FILE}`,
      `/home/alex/.omp/sessions/${FILE}`,
      `other/${FILE}`,
      `${MATERIAL_SESSIONS}/0002-other.jsonl`,
      "https://example.invalid/doc",
    ]) {
      expect(admitCitation(path, SERVED)).toBeNull();
    }
  });

  test("a spelling that cannot change which file is named is admitted rather than wasted", () => {
    // A doubled slash and a `./` segment are how a model writes a path by accident. Refusing
    // them would discard a paid run's whole answer over punctuation.
    expect(admitCitation(`${MATERIAL_SESSIONS}//${FILE}`, SERVED)?.file).toBe(FILE);
    expect(admitCitation(`./${MATERIAL_SESSIONS}/${FILE}`, SERVED)?.file).toBe(FILE);
    expect(admitCitation(`${MATERIAL_ROOT}/./${MATERIAL_SESSIONS}/${FILE}`, SERVED)?.file).toBe(
      FILE,
    );
  });

  test("a retyped digest names both values, so the claim can be seen to be wrong", () => {
    const refusal = unservedCitation(
      cited({ path: FILE, line: 1, digest: `sha256:${"b".repeat(64)}` }),
      SERVED,
    );
    expect(refusal).toContain(DIGEST);
    expect(refusal).toContain("b".repeat(64));
  });
});

// ---------------------------------------------------------------------- the quote (#348)

describe("a quote is checked against the bytes at the line it cites", () => {
  test("a real quote at the line it names verifies", () => {
    expect(checkQuote("the router retries twice", SESSION, 1)).toEqual({
      outcome: CITATION_OUTCOMES.verified,
      detail: "",
    });
  });

  test("a fabricated quote is caught and says so of the session, not of the line", () => {
    const check = checkQuote("the router retries eleven times", SESSION, 1);
    expect(check.outcome).toBe(CITATION_OUTCOMES.absent);
    expect(check.detail).toContain("nowhere in the session");
  });

  test("a real quote at the wrong line is neither verified nor an invention", () => {
    // The 62/66/34 split the study measured is three outcomes, not two: this one is a claim
    // about bytes that exist whose locator does not reach them, and a reader has to be able to
    // tell it from a quote of something nobody wrote.
    const check = checkQuote("we moved the queue to staging", SESSION, 1);
    expect(check.outcome).toBe(CITATION_OUTCOMES.moved);
    expect(check.detail).toContain("line 2");
    expect(check.detail).toContain("not at line 1");
  });

  test("whitespace, line endings and composition are not content", () => {
    // Each of these is the same sentence as the record's, differently typed. A verdict that
    // called any of them fabricated would make the whole check worthless: the reader would
    // learn to ignore it.
    const rewrapped = "the router\n  retries   twice\tbefore it gives up";
    expect(checkQuote(rewrapped, SESSION, 1).outcome).toBe(CITATION_OUTCOMES.verified);
    const crlf = materialLines(`${JSON.stringify({ text: "a line of a Windows log" })}\r\n`);
    expect(checkQuote("a line of a Windows log", crlf, 1).outcome).toBe(CITATION_OUTCOMES.verified);
    // A record whose own text carries a newline: the model quotes what it read, and what it
    // read is the decoded prose rather than the `\n` escape the file holds.
    expect(checkQuote("the deploy role reads the object store directly", SESSION, 3).outcome).toBe(
      CITATION_OUTCOMES.verified,
    );
    // NFD from a copy that round-tripped through a tokenizer, against the composed record.
    const composed = materialLines(JSON.stringify({ text: "the café deployment is late" }));
    expect(checkQuote("the café deployment".normalize("NFD"), composed, 1).outcome).toBe(
      CITATION_OUTCOMES.verified,
    );
  });

  test("case is content and is not folded away", () => {
    expect(checkQuote("THE ROUTER RETRIES TWICE", SESSION, 1).outcome).toBe(
      CITATION_OUTCOMES.absent,
    );
  });

  test("a span too short to mean anything is unchecked rather than verified", () => {
    const check = checkQuote("the", SESSION, 1);
    expect(check.outcome).toBe(CITATION_OUTCOMES.unchecked);
    expect(check.detail).toContain("characters");
  });

  test("a line past the end of the session says so instead of accusing", () => {
    const check = checkQuote("the router retries twice", SESSION, 900);
    expect(check.outcome).toBe(CITATION_OUTCOMES.moved);
    expect(check.detail).toContain("line 1");
  });
});

describe("what the settlement writes beside each citation", () => {
  test("a citation that quoted nothing is unquoted, and no bytes are read for it", () => {
    let asked = 0;
    const result = cited({ path: FILE, line: 1, digest: DIGEST });
    const checks = checkCitations(result, SERVED, () => {
      asked += 1;
      return SESSION;
    });
    expect([...checks.values()]).toEqual([{ outcome: CITATION_OUTCOMES.unquoted, detail: "" }]);
    expect(asked).toBe(0);
  });

  test("bytes this hub cannot read leave the citation unchecked, never accused", () => {
    const check = onlyCheck(
      cited({ path: FILE, line: 1, digest: DIGEST, quote: "the router retries twice" }),
      null,
    );
    expect(check?.outcome).toBe(CITATION_OUTCOMES.unchecked);
    expect(check?.detail).toContain(FILE);
  });

  test("a citation naming no line is verified at the line it should have stated", () => {
    const check = onlyCheck(
      cited({ path: FILE, digest: DIGEST, quote: "we moved the queue to staging" }),
    );
    expect(check?.outcome).toBe(CITATION_OUTCOMES.verified);
    expect(check?.detail).toContain("line 2");
  });

  test("the bytes are found through the index entry, under any admitted spelling", () => {
    const check = onlyCheck(
      cited({
        path: `${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${FILE}`,
        line: 1,
        digest: DIGEST,
        quote: "the router retries twice",
      }),
    );
    expect(check?.outcome).toBe(CITATION_OUTCOMES.verified);
  });
});

describe("what a receipt and a cycle report say about it", () => {
  test("every outcome is counted, so nothing looked at and all clean differ", () => {
    const checks = checkCitations(
      cited({ path: FILE, line: 1, digest: DIGEST, quote: "the router retries twice" }),
      SERVED,
      () => SESSION,
    );
    expect(citationTally(checks)).toEqual({
      verified: 1,
      moved: 0,
      absent: 0,
      unquoted: 0,
      unchecked: 0,
    });
    expect(citationNotes(checks)).toEqual([]);
  });

  test("a report names the unverified quotes and says the records stand", () => {
    const checks = checkCitations(
      cited({ path: FILE, line: 1, digest: DIGEST, quote: "the router retries eleven times" }),
      SERVED,
      () => SESSION,
    );
    expect(citationNotes(checks)[0]).toContain("nowhere in the session named");
    expect(citationNotes(checks)[0]).toContain("which stand");
  });
});
