import { describe, expect, test } from "bun:test";
import {
  MATERIAL_INDEX,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  materialFile,
  type MaterialEntry,
} from "../../contract.ts";
import { REFUSALS, parseExploreResult, type ExploreResult } from "../../machine/results.ts";
import {
  ANSWER_FENCE,
  PROMPT_VERSION,
  answerOf,
  composeExplorePrompt,
  readExploreAnswer,
  unservedLocator,
} from "./prompts.ts";

/*
  THE PROMPT AND THE ANSWER, held to the promises the prompt makes.

  Babel runs no session and holds no tools in one, so the whole of the answering contract is
  prose plus a fenced block, and the whole of provenance is a locator checked against the
  material's index. Both are promises the model is given in writing; these tests are what keeps
  them true.
*/

const DIGEST = "a".repeat(64);
const FILE = "0001-omp-s1.jsonl";

/** One session as the material's index carries it. */
const SERVED: readonly MaterialEntry[] = [
  {
    selector: "omp/s1",
    harness: "omp",
    sourceId: "s1",
    captureDigest: "c".repeat(64),
    sourceDigest: DIGEST,
    file: FILE,
    records: 12,
    bytes: 4096,
  },
];

/** A valid explore result citing one locator, parsed the way the hub parses a submission. */
function cited(path: string, digest: string): ExploreResult {
  return parseExploreResult("explore", {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "the catalog forgets archived sessions" },
        observations: [
          {
            ref: "o1",
            recipe: { id: "code-health", version: 3 },
            claim: {
              claim: "the rescan dropped the snapshot",
              confidence: "high",
              impact: "moderate",
              evidence: [{ locator: { path, line: 12, byte_offset: 0, digest }, note: "the row" }],
              counter_evidence_absent: true,
            },
          },
        ],
      },
    ],
  });
}

describe("the answer is the last fenced block of the final message", () => {
  test("a correction written after a draft is the one that is taken", () => {
    const message = `${ANSWER_FENCE}\n{"first":true}\n\`\`\`\n\nOn reflection:\n\n${ANSWER_FENCE}\n{"second":true}\n\`\`\``;
    const answer = answerOf(message);

    expect(answer).toEqual({ json: '{"second":true}' });
  });

  test("a message with no block, and a message with none at all, say which happened", () => {
    expect(answerOf("I could not find anything worth reporting.")).toEqual({
      refused:
        `the final message carries no ${ANSWER_FENCE} block, so this run submitted no result`,
    });
    expect(answerOf("   ")).toEqual({ refused: "the session ended with no final message at all" });
  });

  test("an unterminated fence is not a block: the answer was never closed", () => {
    const answer = answerOf(`${ANSWER_FENCE}\n{"half":`);
    expect("refused" in answer).toBe(true);
  });
});

describe("reading one exploration's answer", () => {
  test("a valid submission comes back as the result, parsed for its stage", () => {
    const message = `Done.\n\n${ANSWER_FENCE}\n${JSON.stringify({ candidates: [], questions: [] })}\n\`\`\``;
    const read = readExploreAnswer("explore", message);

    expect("result" in read).toBe(true);
    if (!("result" in read)) return;
    expect(read.result.candidates).toEqual([]);
  });

  test("a block that is not JSON is a schema refusal, which is spend and not a crash", () => {
    const read = readExploreAnswer("explore", `${ANSWER_FENCE}\n{ not json\n\`\`\``);

    expect("refusal" in read).toBe(true);
    if (!("refusal" in read)) return;
    // A REFUSAL, NOT A THROW: only the code that knows this was a submission can say that the
    // deployment paid for it, and that is what settles the claim at the run's cost.
    expect(read.refusal.refusal).toBe(REFUSALS.schema);
    expect(read.refusal.message).toContain(ANSWER_FENCE);
  });

  test("a shape the stage has no authority for is refused by the stage's own schema", () => {
    // `objections` belong to the challenge stage; an explore that emitted one exceeded its
    // authority, and the stage schema is the enforcement rather than a comment about it.
    const payload = { candidates: [], objections: [{ ref: "x" }] };
    const read = readExploreAnswer("explore", `${ANSWER_FENCE}\n${JSON.stringify(payload)}\n\`\`\``);

    expect("refusal" in read).toBe(true);
    if (!("refusal" in read)) return;
    expect(read.refusal.refusal).toBe(REFUSALS.schema);
  });
});

describe("a locator is admissible exactly when the material served those bytes", () => {
  test("the index's own file and its own digest are served, under either spelling of the path", () => {
    expect(unservedLocator(cited(`${MATERIAL_SESSIONS}/${FILE}`, DIGEST), SERVED)).toBe("");
    expect(unservedLocator(cited(FILE, DIGEST), SERVED)).toBe("");
    expect(
      unservedLocator(cited(`${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${FILE}`, DIGEST), SERVED),
    ).toBe("");
  });

  test("a file the index does not name is not a file this run was served", () => {
    const unserved = unservedLocator(cited(`${MATERIAL_SESSIONS}/0002-other.jsonl`, DIGEST), SERVED);
    expect(unserved).toContain("0002-other.jsonl");
    expect(unserved).toContain("not a file this run was served");
  });

  test("a retyped digest names the two values, so the claim can be seen to be wrong", () => {
    const unserved = unservedLocator(
      cited(`${MATERIAL_SESSIONS}/${FILE}`, "b".repeat(64)),
      SERVED,
    );
    expect(unserved).toContain(DIGEST);
    expect(unserved).toContain("b".repeat(64));
  });
});

test("the material section describes the layout the machine half actually writes", () => {
  const prompt = composeExplorePrompt({
    stage: "explore",
    recipes: [{ id: "code-health", version: 3, title: "Code health", body: "look for trouble" }],
    sessions: [{ selector: "omp/s1", file: materialFile(0, "omp/s1") }],
    preparationId: "job_1_material",
    params: { stage: "explore" },
  });

  // THE LAYOUT IS FIXED IN THE CONTRACT AND STATED HERE: the machine half writes it and this
  // prompt describes it, and a prompt pointing at a path that is not there is a run that reads
  // nothing and answers out of the prompt alone.
  expect(prompt).toContain(`${MATERIAL_ROOT}/`);
  expect(prompt).toContain(MATERIAL_INDEX);
  expect(prompt).toContain(`${MATERIAL_SESSIONS}/<file>`);
  expect(prompt).toContain("one canonical");
  // The file the index will hold this session under is derived by the SAME function both
  // halves use, so the prompt and the sealed directory agree without a round trip.
  expect(prompt).toContain(`- omp/s1 — \`${MATERIAL_SESSIONS}/0001-omp-s1.jsonl\``);
  expect(prompt).toContain("job_1_material");
  // The evidence rule the locator check above enforces, said to the model in writing.
  expect(prompt).toContain('"digest" is that entry\'s "sourceDigest"');
  // And no tool block: Babel runs no session, so there is nothing to call.
  expect(prompt).not.toContain("## Tools");
  expect(PROMPT_VERSION).toBe("babel.analysis-prompt/3");
});

test("a run prepared over nothing says so rather than describing an empty corpus", () => {
  const prompt = composeExplorePrompt({
    stage: "explore",
    recipes: [{ id: "code-health", version: 3, body: "look for trouble" }],
    sessions: [],
    preparationId: "job_2_material",
    params: { stage: "explore" },
  });

  expect(prompt).toContain("prepared over no sessions");
  expect(prompt).toContain("Emit an empty result rather than citing anything");
});
