import { z } from "zod";
import { JOB_OUTPUT_FILES, REFINEMENT_KEY, ROLES, type Refinement } from "../../contract.ts";
import {
  acceptReviewResult,
  contributionRefusal,
  REFUSALS,
  ResultRefusal,
  refusalReason,
  reviewJsonSchema,
  shapeReviewResult,
  type Contribution,
  type ReviewResult,
} from "../../machine/results.ts";
import type { Assignment } from "../../store/coordinator.ts";
import { ANSWER_FENCE, answerOf, type Recipe } from "./prompts.ts";
import { mintId, recordRow, type Row } from "./rows.ts";

type Role = (typeof ROLES)[number];
/** The Code-session review contract recorded on every review receipt. */
export const REVIEW_JOB_VERSION = 2;
export const REVIEW_PROMPT_VERSION = "babel.evaluation-prompt/2";
export const REVIEW_BLINDING_POLICY_VERSION = "babel.evaluation-blinding/1";

const PARAMS_OPEN = "[babel-params]";
const PARAMS_CLOSE = "[end]";

export const REVIEW_PARAM = {
  role: "babel.review.role",
  subjectKind: "babel.review.subject.kind",
  subjectId: "babel.review.subject.id",
  assignment: "babel.review.assignment",
  policy: "babel.review.policy",
  blinded: "babel.review.blinded",
} as const;

export interface ReviewProjection {
  readonly target: Readonly<Record<string, unknown>>;
  readonly sources: readonly Readonly<Record<string, unknown>>[];
}

export interface ReviewRoute {
  readonly machineId: string;
  readonly profile: { readonly containerId: string; readonly expectedRevision: number };
  readonly recipes: Readonly<Record<Role, Recipe | undefined>>;
}

export interface ReviewPreparation {
  readonly assignmentId: string;
  readonly recordId: string;
  readonly revisionId: string;
  readonly rootId: string;
  readonly kind: string;
  readonly role: Role;
  readonly lane: string;
  readonly policyVersion: string;
  readonly fence: number;
  readonly ordinal: number;
  readonly seed: string;
  readonly refinementDepth: number;
  readonly maxRefinementDepth: number;
  readonly inputDigest: string;
  readonly blinded: boolean;
  readonly recipe: { readonly id: string; readonly version: number };
}

const ReviewPreparationSchema = z.strictObject({
  assignmentId: z.string().min(1),
  recordId: z.string().min(1),
  revisionId: z.string().min(1),
  rootId: z.string(),
  kind: z.string().min(1),
  role: z.enum(ROLES),
  lane: z.string(),
  policyVersion: z.string(),
  fence: z.number().int().min(0),
  ordinal: z.number().int().min(0),
  seed: z.string(),
  inputDigest: z.string(),
  refinementDepth: z.number().int().min(0),
  maxRefinementDepth: z.number().int().min(0),
  blinded: z.boolean(),
  recipe: z.strictObject({ id: z.string().min(1), version: z.number().int().min(0) }),
});

export function reviewPreparation(value: unknown): ReviewPreparation | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const parsed = ReviewPreparationSchema.safeParse((value as Record<string, unknown>)["review"]);
  return parsed.success ? parsed.data : null;
}

/** The key a blinded projection leaks through, or an empty string when it carries none. */
const BLINDED_KEYS: Readonly<Record<string, true>> = {
  reception: true,
  tally: true,
  votes: true,
  vote: true,
  rank: true,
  cohort: true,
  novelty: true,
  priority: true,
  assessment: true,
  assessments: true,
  evaluations: true,
  support_count: true,
  oppose_count: true,
  unsure_count: true,
};

/**
 * The same contract as {@link blindedLeak}, applied rather than asserted.
 *
 * A projection is built from a record's own payload, and a record Babel wrote in an earlier
 * era carries the very keys §4.12 withholds from a reviewer — `novelty` on every imported
 * hypothesis. Leaving them in and refusing the dispatch made the leak check the FILTER: the
 * conductor drew, refused itself, and no review of an imported record could ever be
 * dispatched. Stripping here keeps `blindedLeak` as what it should be, the post-condition.
 */
export function blinded(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(blinded);
  if (typeof value !== "object" || value === null) return value;
  const out: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value)) {
    if (BLINDED_KEYS[key] === true) continue;
    out[key] = blinded(item);
  }
  return out;
}

export function blindedLeak(value: unknown, path = ""): string {
  if (Array.isArray(value)) {
    for (const [index, item] of value.entries()) {
      const leak = blindedLeak(item, `${path}[${String(index)}]`);
      if (leak !== "") return leak;
    }
    return "";
  }
  if (typeof value !== "object" || value === null) return "";
  const entries = Object.entries(value).sort(([left], [right]) => left.localeCompare(right));
  for (const [key, item] of entries) {
    if (BLINDED_KEYS[key] === true) return path === "" ? key : `${path}.${key}`;
    const leak = blindedLeak(item, path === "" ? key : `${path}.${key}`);
    if (leak !== "") return leak;
  }
  return "";
}

function paramsBlock(params: Readonly<Record<string, string>>): string {
  const lines = Object.keys(params)
    .sort()
    .map((key) => `${key} = ${params[key] ?? ""}`);
  return `${PARAMS_OPEN}\n${lines.join("\n")}${lines.length === 0 ? "" : "\n"}${PARAMS_CLOSE}\n\n`;
}

const ROLE_QUESTION: Record<Role, string> = {
  reception:
    "Decide whether you support, oppose or are unsure about this record as it stands. This is a judgement about the idea, not an evidence score or an outcome prediction.",
  evidence:
    "Check whether the evidence already carried by this record supports its claim. Do not turn an evidence check into a reception vote.",
  challenge:
    "State the strongest concrete objection: evidence, a consequence, a missing check or an alternative. Diagnose disagreement; do not manufacture one.",
  comparison:
    "Compare only alternatives the prompt supplies. A contextual preference is not a vote and does not merge records.",
  outcome:
    "Report what actually happened against the record's criteria. A merge is not a deployment, and a deployment is not verification.",
  relevance:
    "Assess whether this record is relevant to the work and constraints shown with it. Relevance is not quality or reception.",
  filing:
    "Decide what this record is about. Prefer an existing topic; otherwise answer no_topic, no_change, or propose one explicit topic operation.",
  backlog:
    "Decide what should become of this deferred candidate: consolidate, supersede, retire, promote, or keep. This is a proposal, never an immediate mutation.",
};

const ROLE_RULES: Record<Role, string> = {
  reception:
    "Set vote to support, oppose or unsure. A bare vote is complete. Contributions are optional. A comment may address the whole record or an exact JSON Pointer; a refinement must name the pointer and the replacement it proposes.",
  evidence:
    "Record one results entry per criterion checked. A satisfied criterion needs evidence already present in the target; unchecked criteria get no entry. Name the environment and as_of whenever results are present.",
  challenge:
    "Use a contribution for a concrete objection, comment, or refinement. A refinement may target the whole record or an exact JSON Pointer such as `/payload/problem`. If the material cannot support one, use skip rather than inventing it.",
  comparison:
    "A comparison contribution names at least two supplied alternatives and may prefer one of them. If no alternatives are supplied, use skip.",
  outcome:
    "Set outcome only from evidence already present in the target. Verified means every reported criterion is satisfied and evidenced. Use unverifiable with uncertainty when the checks cannot be made.",
  relevance:
    "Use a contribution to state the relevance or its absence, or to propose a targeted refinement. Use skip when the shown record provides no basis for either.",
  filing:
    "Answer with exactly one of filing, topic, no_topic or no_change. A topic change is only a proposal for the operator.",
  backlog:
    "Answer with exactly one of consolidate, supersede, retire, promote or keep. Every identifier must be one the prompt supplied.",
};

export function composeReviewPrompt(input: {
  readonly assignment: Assignment;
  readonly preparation: ReviewPreparation;
  readonly recipe: Recipe;
  readonly projection: ReviewProjection;
}): string {
  const { assignment, preparation, recipe, projection } = input;
  const params: Record<string, string> = {
    [REVIEW_PARAM.role]: assignment.role,
    [REVIEW_PARAM.subjectKind]: assignment.kind,
    [REVIEW_PARAM.subjectId]: assignment.recordId,
    [REVIEW_PARAM.assignment]: assignment.id,
    [REVIEW_PARAM.policy]: assignment.policyVersion,
    [REVIEW_PARAM.blinded]: "true",
  };
  return [
    "# Babel evaluation\n\n",
    "## Recipe\n\n",
    `### ${recipe.title ?? recipe.id} (id ${recipe.id}, version ${String(recipe.version)})\n\n`,
    `${recipe.body.trim()}\n\n`,
    "## How to answer\n\n",
    "Return exactly one final JSON document in the last fenced block shown below. The document must match this role's schema. A bare answer is valid; do not invent prose, evidence, criteria, alternatives or work merely to fill fields. If the shown material cannot support this role's judgement, set `skip` to the reason.\n\n",
    `${ANSWER_FENCE}\n${JSON.stringify(reviewJsonSchema(assignment.role), null, 2)}\n\`\`\`\n\n`,
    "## Your role\n\n",
    `${ROLE_QUESTION[assignment.role]}\n\n${ROLE_RULES[assignment.role]}\n\n`,
    preparation.refinementDepth < preparation.maxRefinementDepth
      ? "Cite only evidence locators already present in the record below, copied exactly. Comments may set `target.path` to an empty JSON Pointer for the whole record or to an exact path such as `/payload/problem`, `/payload/questions/0`, or `/payload/unsolved/0`. A `refinement` must set that target and put its proposed replacement in `would_change`; Babel stores it as a new proposal, so independent reviewers can support, oppose, challenge, or refine that proposal in turn. The record is an immutable revision; judge this wording and do not move the answer to a newer revision.\n\n"
      : `Cite only evidence locators already present in the record below, copied exactly. Comments may target any exact JSON Pointer, but this record is already refinement generation ${String(preparation.refinementDepth)} of ${String(preparation.maxRefinementDepth)}; do not create another refinement. The record is an immutable revision; judge this wording and do not move the answer to a newer revision.\n\n`,
    paramsBlock(params),
    "## The record under review\n\n",
    "This initial assessment is blind. Babel has withheld tallies, ranks and earlier evaluations. Judge only what follows.\n\n",
    `\`\`\`json\n${JSON.stringify(projection.target, null, 2)}\n\`\`\`\n\n`,
    projection.sources.length === 0
      ? ""
      : `## Cited sessions\n\n${JSON.stringify(projection.sources, null, 2)}\n\n`,
    `Review contract: job ${String(REVIEW_JOB_VERSION)}, prompt ${REVIEW_PROMPT_VERSION}, blinding ${REVIEW_BLINDING_POLICY_VERSION}. Assignment fence ${String(preparation.fence)}.\n`,
  ].join("");
}

/**
 * What the sealed answer was: the accepted result, or the refusal AND the shape it was refused
 * in. The shape is there so a caller can ask which contribution the rules refuse — it is null
 * when the refusal is the fence, the JSON or the shape itself, because a submission nobody
 * could parse has no contributions to salvage. `submitted` is the document the model sent,
 * whatever became of it, so a refusal can be recorded beside the answer that earned it (#311).
 */
type ReviewAnswer =
  | { readonly result: ReviewResult; readonly submitted: unknown }
  | {
      readonly refusal: ResultRefusal;
      readonly shaped: ReviewResult | null;
      readonly submitted: unknown;
    };

function readReviewAnswer(role: Role, finalMessage: string): ReviewAnswer {
  const answer = answerOf(finalMessage);
  if ("refused" in answer) {
    return {
      refusal: new ResultRefusal(REFUSALS.schema, answer.refused),
      shaped: null,
      submitted: null,
    };
  }
  let payload: unknown;
  try {
    payload = JSON.parse(answer.json);
  } catch (error) {
    return {
      refusal: new ResultRefusal(
        REFUSALS.schema,
        `the ${ANSWER_FENCE} block is not JSON: ${error instanceof Error ? error.message : String(error)}`,
      ),
      shaped: null,
      submitted: null,
    };
  }
  let shaped: ReviewResult;
  try {
    shaped = shapeReviewResult(role, payload);
  } catch (error) {
    if (error instanceof ResultRefusal) return { refusal: error, shaped: null, submitted: payload };
    throw error;
  }
  try {
    return { result: acceptReviewResult(role, shaped), submitted: payload };
  } catch (error) {
    if (error instanceof ResultRefusal) return { refusal: error, shaped, submitted: payload };
    throw error;
  }
}

/**
 * How much of a refused submission a receipt keeps. A review answer is bounded by what a model
 * writes and not by a column, so it is kept whole up to this and reported as its size beyond it:
 * a truncated answer is not the answer anybody submitted, and the size is still evidence (#311).
 */
export const MAX_REJECTED_SUBMISSION_BYTES = 32768;

/** What a receipt records of a submission that was refused: the answer, or how big it was. */
export function rejectedSubmission(payload: unknown): {
  bytes: number;
  payload?: unknown;
  withheld?: "too-large";
} {
  const bytes = new TextEncoder().encode(JSON.stringify(payload) ?? "").length;
  return bytes > MAX_REJECTED_SUBMISSION_BYTES
    ? { bytes, withheld: "too-large" }
    : { bytes, payload };
}

/** One contribution the contract refused, as the receipt records it. */
export interface RefusedContribution {
  /** 1-based, which is how the validator's own sentences number contributions. */
  readonly contribution: number;
  /** `<code>: <sentence>` — the validator's own words, in the shape a `reason` already has. */
  readonly reason: string;
}

/** What a sealed review amounts to: what is recorded, what was refused, and why nothing was. */
export interface ReviewVerdict {
  /** The result to record, or null when the review is refused whole. */
  readonly result: ReviewResult | null;
  /** The contributions dropped so the rest could stand, whether or not the rest did. */
  readonly refused: readonly RefusedContribution[];
  /** `<code>: <sentence>` when nothing is recorded, and "" when something is. */
  readonly reason: string;
  /**
   * THE DOCUMENT THE MODEL SUBMITTED, or null when it submitted none this could parse.
   *
   * A review refused whole used to leave a reason string and nothing else, so "did the
   * judgement change under a shape refusal?" and "did that class of refusal actually fall?"
   * were questions the store could not answer at all (#311). The payload is carried here and
   * recorded on the run's receipt when the review is refused; it is the model's own answer,
   * unedited, which is the only form in which it is evidence.
   */
  readonly submitted: unknown;
}

/**
 * THE WHOLE VERDICT ON ONE SEALED REVIEW (#305).
 *
 * Seven conductor-drawn reviews on `openrouter/stealth/union-alpha` spent 202k tokens and
 * recorded two: five were discarded whole for one refused contribution apiece, having already
 * paid for the good ones beside it. The preferred remedy — hand the validator's sentence back to
 * the same session and let it correct itself — is not available to this plugin: Code publishes
 * `runSession`, `readSession` and `cancelSession`, whose input is a prompt and whose answer is a
 * sealed transcript, and omp's `resumeSession` prepares an INTERACTIVE TERMINAL rather than a
 * governed one-shot (and belongs to a plugin Babel's manifest does not declare an edge to). A
 * session that has sealed cannot be spoken to again, so there is no turn to spend.
 *
 * What is available is to stop charging the good contributions for the bad one. A rule whose
 * whole subject is ONE contribution refuses that contribution by name; the rest of the review
 * then goes through the SAME acceptance it always did, as a whole, and is recorded only if it
 * stands on its own.
 *
 * NOTHING IS LAUNDERED THROUGH THIS PATH, and that is the property to keep when reading it:
 *
 * - A refused contribution is DROPPED, never edited. There is no branch that strips the
 *   `alternatives` off an objection that may not name them, or supplies the text an empty
 *   contribution lacks; a judgement the contract refused is not recorded in an altered form
 *   that would pass.
 * - Only contributions are droppable. The vote, the outcome, the skip, the filing and backlog
 *   answers, the refinement-depth bound and the scope rule are statements about the review as a
 *   whole, and a review that gets one of those wrong still fails whole — there is no subset of a
 *   vote to keep.
 * - The survivors are re-accepted TOGETHER by {@link acceptReviewResult}, so a claim that leaned
 *   on what was dropped falls with it: an observed outcome whose only evidence was in the
 *   refused contribution is refused for want of support, and a review with nothing left is
 *   refused as empty. Losing a contribution can never make a review easier to record.
 * - Every row still passes the store's own acceptance at ingest (`store/acts.ts`), which is this
 *   same function over the payload that was written.
 */
export function reviewVerdict(
  preparation: ReviewPreparation,
  finalMessage: string,
  target: unknown,
): ReviewVerdict {
  const role = preparation.role;
  const answer = readReviewAnswer(role, finalMessage);
  let submitted: ResultRefusal | null = null;
  let shaped: ReviewResult;
  if ("refusal" in answer) {
    if (answer.shaped === null) {
      return {
        result: null,
        refused: [],
        reason: refusalReason(answer.refusal),
        submitted: answer.submitted,
      };
    }
    submitted = answer.refusal;
    shaped = answer.shaped;
  } else {
    shaped = answer.result;
  }

  const served: Locator[] = [];
  locators(target, served);
  const kept: Contribution[] = [];
  const refused: RefusedContribution[] = [];
  for (const [index, contribution] of shaped.contributions.entries()) {
    const reason = contributionReason(role, contribution, index, target, served);
    if (reason === "") kept.push(contribution);
    else refused.push({ contribution: index + 1, reason });
  }

  if (refused.length === 0) {
    // Nothing here is one contribution's fault. A refusal the acceptance raised stands exactly
    // as it did before this path existed, which is what keeps the receipts comparable.
    if (submitted !== null)
      return {
        result: null,
        refused,
        reason: refusalReason(submitted),
        submitted: answer.submitted,
      };
    const whole = wholeReviewReason(shaped, preparation, served);
    return whole === ""
      ? { result: shaped, refused, reason: "", submitted: answer.submitted }
      : { result: null, refused, reason: whole, submitted: answer.submitted };
  }

  let stands: ReviewResult;
  try {
    stands = acceptReviewResult(role, { ...shaped, contributions: kept });
  } catch (error) {
    if (!(error instanceof ResultRefusal)) throw error;
    // The reason describes the review as a whole: the refusal the SUBMISSION earned if it earned
    // one — the same sentence today's receipt carries, so a failed review still reports the
    // defect rather than its consequence — and otherwise what the survivors failed on. Which
    // contributions were refused is `refused`, and that is reported either way.
    return {
      result: null,
      refused,
      reason: refusalReason(submitted ?? error),
      submitted: answer.submitted,
    };
  }
  const whole = wholeReviewReason(stands, preparation, served);
  if (whole === "") return { result: stands, refused, reason: "", submitted: answer.submitted };
  return {
    result: null,
    refused,
    reason: submitted === null ? whole : refusalReason(submitted),
    submitted: answer.submitted,
  };
}

/** A refusal of one contribution, in the validator's own sentence and nothing besides it. */
function contributionReason(
  role: Role,
  contribution: Contribution,
  index: number,
  target: unknown,
  served: readonly Locator[],
): string {
  const refused = contributionRefusal(role, contribution, index);
  if (refused !== null) return refusalReason(refused);
  const unserved = unservedReviewLocator(contribution, served);
  if (unserved !== "") return `${REFUSALS.unknownReference}: ${unserved}`;
  // A REFINEMENT NAMES AN EXACT POINTER OR IT IS NOT A REFINEMENT (§4.7). The contribution
  // vocabulary lets an empty pointer address the record as a whole, which is right for a comment
  // and impossible for a refinement: the operator's acceptance of one replaces the wording UNDER
  // that pointer, so an empty one names nothing to replace and the proposal could only ever be
  // refused at the moment he accepted it. Refusing the contribution costs it and nothing else,
  // and the receipt says why rather than leaving him a proposal that does nothing.
  if (contribution.kind === "refinement" && contribution.target?.path === "") {
    return (
      `${REFUSALS.schema}: refinement ${String(index + 1)} names the record as a whole; a ` +
      "refinement names the exact JSON Pointer whose wording it would replace"
    );
  }
  const path = contribution.target?.path;
  if (path !== undefined && !pointerExists(target, path)) {
    return (
      `${REFUSALS.unknownReference}: contribution ${String(index + 1)} targets ` +
      `${JSON.stringify(path)}, which is not part of the record under review`
    );
  }
  return "";
}

/**
 * What refuses the review as a whole once its contributions are settled: the depth bound this
 * assignment carries, and a citation nowhere in the contributions — a criterion result's own
 * evidence — that the record never served.
 */
function wholeReviewReason(
  result: ReviewResult,
  preparation: ReviewPreparation,
  served: readonly Locator[],
): string {
  if (
    preparation.refinementDepth >= preparation.maxRefinementDepth &&
    result.contributions.some((contribution) => contribution.kind === "refinement")
  ) {
    return (
      `${REFUSALS.authority}: this record is refinement generation ` +
      `${String(preparation.refinementDepth)} of ${String(preparation.maxRefinementDepth)}; ` +
      "another refinement would create an unbounded review obligation"
    );
  }
  const unserved = unservedReviewLocator(result, served);
  return unserved === "" ? "" : `${REFUSALS.unknownReference}: ${unserved}`;
}

interface Locator {
  readonly path: string;
  readonly digest: string;
}

function locators(value: unknown, out: Locator[]): void {
  if (Array.isArray(value)) {
    for (const item of value) locators(item, out);
    return;
  }
  if (typeof value !== "object" || value === null) return;
  const row = value as Record<string, unknown>;
  if (typeof row["path"] === "string" && typeof row["digest"] === "string") {
    out.push({ path: row["path"], digest: row["digest"] });
  }
  for (const item of Object.values(row)) locators(item, out);
}

/** A citation in what a review submitted that was not already present in its blinded target. */
function unservedReviewLocator(submitted: unknown, served: readonly Locator[]): string {
  const cited: Locator[] = [];
  locators(submitted, cited);
  for (const locator of cited) {
    if (!served.some((entry) => entry.path === locator.path && entry.digest === locator.digest)) {
      return `${locator.path} at ${locator.digest} was not present in the record under review`;
    }
  }
  return "";
}

function pointerExists(value: unknown, pointer: string): boolean {
  if (pointer === "") return true;
  let current: unknown = value;
  for (const encoded of pointer.slice(1).split("/")) {
    const key = encoded.replace(/~1/g, "/").replace(/~0/g, "~");
    if (Array.isArray(current)) {
      if (!/^(0|[1-9]\d*)$/.test(key)) return false;
      const index = Number(key);
      if (index >= current.length) return false;
      current = current[index];
      continue;
    }
    if (typeof current !== "object" || current === null || !Object.hasOwn(current, key))
      return false;
    current = (current as Record<string, unknown>)[key];
  }
  return true;
}

/** The row shape every table here is written in, as {@link Row} spells it. */
export type ReviewRow = Row;

/** A review mints no record its own proposal could rest on, so its settlement holds none. */
const NO_OWN_RECORDS: ReadonlySet<string> = new Set();

/**
 * A record this hub minted in this settlement and a review's proposal have to mean the same
 * thing by the same keys, so both go through {@link recordRow}: it is what folds in whether the
 * proposal rests on one run, and the determination is as of creation because a record is
 * immutable by trigger and nothing later adds a support to a row that already exists.
 *
 * `supports` is required for the same reason it is required there — a review lane added later
 * has to say what its proposal rests on rather than silently producing a record whose
 * corroboration nobody determined. A review mints no record its own proposal could rest on, so
 * the settlement's own set is empty and every support it names is an earlier run's.
 */
function proposalRow(
  id: string,
  title: string,
  payload: Record<string, unknown>,
  supports: readonly string[],
  preparation: ReviewPreparation,
  runId: string,
  at: string,
): Row {
  return recordRow({
    id,
    kind: "proposal",
    runId,
    at,
    title,
    payload,
    supports,
    ownRecords: NO_OWN_RECORDS,
    recipe: preparation.recipe,
  });
}

function edgeRow(
  fromId: string,
  preparation: ReviewPreparation,
  runId: string,
  at: string,
  kind = "addresses",
): Row {
  return {
    id: mintId("edg", runId, `${kind}|${fromId}|${preparation.recordId}`),
    kind,
    from_kind: "proposal",
    from_id: fromId,
    to_kind: preparation.kind,
    to_id: preparation.recordId,
    position: 0,
    note: null,
    actor_kind: "run",
    actor_id: runId,
    created_at: at,
  };
}

export function reviewRows(
  preparation: ReviewPreparation,
  result: ReviewResult,
  runId: string,
  at: string,
): Readonly<Record<string, readonly ReviewRow[]>> {
  const rows: Record<string, ReviewRow[]> = {
    [JOB_OUTPUT_FILES.assessments]: [],
    [JOB_OUTPUT_FILES.records]: [],
    [JOB_OUTPUT_FILES.edges]: [],
    [JOB_OUTPUT_FILES.filings]: [],
    [JOB_OUTPUT_FILES.plans]: [],
    [JOB_OUTPUT_FILES.steeringReplies]: [],
  };
  rows[JOB_OUTPUT_FILES.assessments]?.push({
    id: mintId("asm", runId, `${preparation.revisionId}|${preparation.role}`),
    record_id: preparation.recordId,
    revision_id: preparation.revisionId,
    run_id: runId,
    role: preparation.role,
    vote: result.vote === "" ? null : result.vote,
    lane: preparation.lane,
    claim_id: preparation.assignmentId,
    supersedes_id: null,
    payload: JSON.stringify({
      ...result,
      blinded: preparation.blinded,
      policyVersion: preparation.policyVersion,
      recipe: preparation.recipe,
    }),
    recorded_at: at,
  });
  if (result.skip !== "") return rows;

  for (const [index, contribution] of result.contributions.entries()) {
    if (contribution.kind !== "refinement") continue;
    const path = contribution.target?.path ?? "";
    // The pointer's last element, as a reader sees it: `/payload/problem` is "problem". An empty
    // pointer cannot reach here — `contributionReason` refuses a refinement that names no exact
    // part — and the fallback is what makes that a compile-time certainty rather than a cast.
    const label =
      path
        .split("/")
        .filter((part) => part !== "")
        .at(-1)
        ?.replace(/~1/g, "/")
        .replace(/~0/g, "~") ?? "the record";
    const title = `Refine ${label} in ${preparation.recordId}`;
    const refinement: Refinement = {
      targetRecordId: preparation.recordId,
      targetRevisionId: preparation.revisionId,
      depth: preparation.refinementDepth + 1,
      targetPath: path,
      reason: contribution.text,
      replacement: contribution.would_change,
      sourceRole: preparation.role,
    };
    const proposalId = mintId("pro", runId, `refinement|${String(index)}|${path}`);
    rows[JOB_OUTPUT_FILES.records]?.push(
      proposalRow(
        proposalId,
        title,
        {
          title,
          problem: contribution.text,
          outcome: contribution.would_change,
          impact: "moderate",
          classification: "private",
          // Spelled under the key the operator's acceptance reads it back from, and typed
          // against the same schema that parses it there (`contract.ts`): a refinement Babel
          // writes and a refinement Babel applies are one shape or the operator reviews a
          // proposal whose acceptance can do nothing.
          [REFINEMENT_KEY]: refinement,
        },
        // A refinement REFINES the record it rewrites; it does not rest on it. The count this
        // marks is over `consolidates` and `addresses` alone, so this proposal supports nothing.
        [],
        preparation,
        runId,
        at,
      ),
    );
    rows[JOB_OUTPUT_FILES.edges]?.push(edgeRow(proposalId, preparation, runId, at, "refines"));
  }

  if (preparation.role === "filing") {
    if (result.filing !== null) {
      rows[JOB_OUTPUT_FILES.filings]?.push({
        id: mintId("fil", runId, `${preparation.recordId}|${result.filing.entity}`),
        record_id: preparation.recordId,
        entity_id: result.filing.entity,
        rationale: result.filing.rationale,
        author_kind: "run",
        author_id: runId,
        heuristic: 0,
        withdrawn: 0,
        supersedes_id: null,
        created_at: at,
      });
    } else if (result.noTopic !== null) {
      rows[JOB_OUTPUT_FILES.filings]?.push({
        id: mintId("fil", runId, `${preparation.recordId}|`),
        record_id: preparation.recordId,
        entity_id: "",
        rationale: result.noTopic.reason,
        author_kind: "run",
        author_id: runId,
        heuristic: 0,
        withdrawn: 0,
        supersedes_id: null,
        created_at: at,
      });
    } else if (result.noChange !== null) {
      rows[JOB_OUTPUT_FILES.steeringReplies]?.push({
        id: mintId(
          "str",
          runId,
          `${result.noChange.ask_id}|${String(result.noChange.reason.length)}`,
        ),
        root_id: result.noChange.ask_id,
        reply_to_id: result.noChange.ask_id,
        seq: 0,
        actor_kind: "run",
        actor_id: runId,
        target_kind: null,
        target_id: null,
        text: result.noChange.reason,
        recorded_at: at,
      });
    } else if (result.topic !== null) {
      const topic = result.topic;
      const ref = `topic/${topic.operation}/${topic.identity === "" ? topic.targets.join("+") : topic.identity}`;
      const proposalId = mintId("pro", runId, ref);
      const title =
        topic.operation === "create"
          ? `Create the topic ${topic.name}`
          : topic.operation === "split"
            ? `Split ${topic.targets[0] ?? ""} out into ${topic.name}`
            : topic.operation === "merge"
              ? `Merge ${topic.targets[0] ?? ""} into ${topic.targets[1] ?? ""}`
              : `Retire the topic ${topic.targets[0] ?? ""}`;
      rows[JOB_OUTPUT_FILES.records]?.push(
        proposalRow(
          proposalId,
          title,
          {
            title,
            problem: topic.reasoning,
            outcome: title,
            impact: "moderate",
            classification: "private",
            topic,
          },
          // It ADDRESSES the record it was drawn on, which is one support and one run: the
          // reviewed record is always an earlier run's.
          [preparation.recordId],
          preparation,
          runId,
          at,
        ),
      );
      rows[JOB_OUTPUT_FILES.edges]?.push(edgeRow(proposalId, preparation, runId, at));
      rows[JOB_OUTPUT_FILES.plans]?.push({
        id: mintId("pln", runId, `topic|${proposalId}|${topic.operation}`),
        kind: "topic",
        subject_kind: "proposal",
        subject_id: proposalId,
        operation: topic.operation,
        dedupe_key: topic.identity === "" ? topic.targets.join("+") : topic.identity,
        payload: JSON.stringify({ ...topic, record: preparation.recordId }),
        proposed_by_kind: "run",
        proposed_by_id: runId,
        state: "open",
        ruled_by: null,
        ruled_at: null,
        ruling_reason: null,
        result: null,
        created_at: at,
      });
    }
  }

  if (preparation.role === "backlog" && result.keep === null) {
    const operation =
      result.consolidate !== null
        ? "consolidate"
        : result.supersede !== null
          ? "supersede"
          : result.retire !== null
            ? "retire"
            : "promote";
    const payload =
      result.consolidate !== null
        ? { consolidate: result.consolidate, candidate: preparation.recordId }
        : result.supersede !== null
          ? { supersede: result.supersede, candidate: preparation.recordId }
          : result.retire !== null
            ? { retire: result.retire, candidate: preparation.recordId }
            : { promote: result.promote, candidate: preparation.recordId };
    const title =
      result.consolidate !== null
        ? `Consolidate ${String(result.consolidate.hypotheses.length)} candidates into ${result.consolidate.finding.title}`
        : result.supersede !== null
          ? `Supersede this candidate with ${result.supersede.by}`
          : result.retire !== null
            ? "Retire this candidate"
            : `Promote an observation to a fact about ${result.promote?.entity ?? ""}`;
    const proposalId = mintId("pro", runId, `backlog/${operation}/${preparation.recordId}`);
    rows[JOB_OUTPUT_FILES.records]?.push(
      proposalRow(
        proposalId,
        title,
        {
          title,
          problem: title,
          outcome: title,
          impact: "moderate",
          classification: "private",
          backlog: payload,
        },
        // The same one support: it ADDRESSES the candidate it was drawn on.
        [preparation.recordId],
        preparation,
        runId,
        at,
      ),
    );
    rows[JOB_OUTPUT_FILES.edges]?.push(edgeRow(proposalId, preparation, runId, at));
    const status: Readonly<Record<string, string>> = {
      consolidate: "promoted",
      supersede: "superseded",
      retire: "retired",
      promote: "promoted",
    };
    rows[JOB_OUTPUT_FILES.plans]?.push({
      id: mintId("pln", runId, `backlog|${proposalId}|${operation}`),
      kind: "backlog",
      subject_kind: "proposal",
      subject_id: proposalId,
      operation,
      dedupe_key: null,
      payload: JSON.stringify({ ...payload, status: status[operation] ?? "promoted" }),
      proposed_by_kind: "run",
      proposed_by_id: runId,
      state: "open",
      ruled_by: null,
      ruled_at: null,
      ruling_reason: null,
      result: null,
      created_at: at,
    });
  }
  return rows;
}
