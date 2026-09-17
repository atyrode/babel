import { createHash } from "node:crypto";
import { z } from "zod";
import { JOB_OUTPUT_FILES, ROLES } from "../../contract.ts";
import {
  parseReviewResult,
  REFUSALS,
  ResultRefusal,
  reviewJsonSchema,
  type ReviewResult,
} from "../../machine/results.ts";
import type { Assignment } from "../../store/coordinator.ts";
import { ANSWER_FENCE, answerOf, type Recipe } from "./prompts.ts";

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

export function readReviewAnswer(
  role: Role,
  finalMessage: string,
): { result: ReviewResult } | { refusal: ResultRefusal } {
  const answer = answerOf(finalMessage);
  if ("refused" in answer) return { refusal: new ResultRefusal(REFUSALS.schema, answer.refused) };
  let payload: unknown;
  try {
    payload = JSON.parse(answer.json);
  } catch (error) {
    return {
      refusal: new ResultRefusal(
        REFUSALS.schema,
        `the ${ANSWER_FENCE} block is not JSON: ${error instanceof Error ? error.message : String(error)}`,
      ),
    };
  }
  try {
    return { result: parseReviewResult(role, payload) };
  } catch (error) {
    if (error instanceof ResultRefusal) return { refusal: error };
    throw error;
  }
}

export function refinementPastDepth(result: ReviewResult, preparation: ReviewPreparation): string {
  if (
    preparation.refinementDepth >= preparation.maxRefinementDepth &&
    result.contributions.some((contribution) => contribution.kind === "refinement")
  ) {
    return (
      `this record is refinement generation ${String(preparation.refinementDepth)} of ` +
      `${String(preparation.maxRefinementDepth)}; another refinement would create an unbounded review obligation`
    );
  }
  return "";
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

/** A citation in an accepted review that was not already present in its blinded target. */
export function unservedReviewLocator(result: ReviewResult, target: unknown): string {
  const served: Locator[] = [];
  const cited: Locator[] = [];
  locators(target, served);
  locators(result, cited);
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
    if (typeof current !== "object" || current === null || !Object.hasOwn(current, key)) return false;
    current = (current as Record<string, unknown>)[key];
  }
  return true;
}

/** A granular comment/refinement target that is not part of the immutable record shown. */
export function unresolvedContributionTarget(result: ReviewResult, target: unknown): string {
  for (const [index, contribution] of result.contributions.entries()) {
    const path = contribution.target?.path;
    if (path !== undefined && !pointerExists(target, path)) {
      return `contribution ${String(index + 1)} targets ${JSON.stringify(path)}, which is not part of the record under review`;
    }
  }
  return "";
}

export type Cell = string | number | null;
export type ReviewRow = Record<string, Cell>;

function mintId(prefix: string, runId: string, ref: string): string {
  const digest = createHash("sha256").update(`${prefix}\u0000${runId}\u0000${ref}`).digest("hex");
  return `${prefix}_${digest.slice(0, 32)}`;
}

function recordRow(
  id: string,
  title: string,
  payload: unknown,
  preparation: ReviewPreparation,
  runId: string,
  at: string,
): ReviewRow {
  return {
    id,
    kind: "proposal",
    root_id: id,
    supersedes_id: null,
    seq: 0,
    parent_id: null,
    run_id: runId,
    recipe_id: preparation.recipe.id,
    recipe_version: preparation.recipe.version,
    actor_kind: "run",
    actor_id: runId,
    title: title.length <= 200 ? title : `${title.slice(0, 199)}…`,
    created_at: at,
    payload: JSON.stringify(payload),
  };
}

function edgeRow(
  fromId: string,
  preparation: ReviewPreparation,
  runId: string,
  at: string,
  kind = "addresses",
): ReviewRow {
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
    const label =
      path === ""
        ? "the record"
        : path
            .split("/")
            .filter((part) => part !== "")
            .at(-1)
            ?.replace(/~1/g, "/")
            .replace(/~0/g, "~") ?? "the record";
    const title = `Refine ${label} in ${preparation.recordId}`;
    const proposalId = mintId("pro", runId, `refinement|${String(index)}|${path}`);
    rows[JOB_OUTPUT_FILES.records]?.push(
      recordRow(
        proposalId,
        title,
        {
          title,
          problem: contribution.text,
          outcome: contribution.would_change,
          impact: "moderate",
          classification: "private",
          refinement: {
            targetRecordId: preparation.recordId,
            targetRevisionId: preparation.revisionId,
            depth: preparation.refinementDepth + 1,
            targetPath: path,
            reason: contribution.text,
            replacement: contribution.would_change,
            sourceRole: preparation.role,
          },
        },
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
        id: mintId("str", runId, `${result.noChange.ask_id}|${String(result.noChange.reason.length)}`),
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
        recordRow(
          proposalId,
          title,
          { title, problem: topic.reasoning, outcome: title, impact: "moderate", classification: "private", topic },
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
      recordRow(
        proposalId,
        title,
        { title, problem: title, outcome: title, impact: "moderate", classification: "private", backlog: payload },
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
