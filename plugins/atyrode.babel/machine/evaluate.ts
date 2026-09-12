/*
  THE `evaluate` OPERATION (plan §4): one drawn assignment — a record, the exact revision, a role,
  the lane it came from and the claim that reserved it — goes in; an assessment comes out, plus
  whatever that role's answer makes durable, plus a receipt. Ported from
  internal/explore/{review.go,reviewfiling.go,reviewbacklog.go}.

  What it reuses from an exploration is everything that makes one auditable: one supervised job, a
  generated result schema the engine validates before Babel sees a submission, a receipt recording
  the boundary, and the same refusal path when a submission does not hold.

  What it adds is procedural blinding, and the blinding is what Babel SERVED rather than an
  instruction about attention. An initial reception, evidence, outcome or relevance assessment is
  taken without the tallies, ranks and earlier evaluations the target has collected; this
  operation audits the projection about to be sent and REFUSES THE LAUNCH when it carries one,
  because the remedy for a leak is the projection that produced it and never a prompt asking the
  model to ignore what it read.

  A role's authority decides what its answer becomes. Reception votes; evidence and outcome record
  criteria; comparison prefers in a named context; filing says what a record is about; backlog
  says what becomes of a deferred candidate. The last two write Babel's own proposals: a topic
  change and a backlog act are records plus a `plans` row, applied by the operator's acceptance
  and by nothing else.
*/

import { randomUUID } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { z } from "zod";
import { OPERATIONS, ROLES, type Receipt } from "../contract.ts";
import { runEngineJob, type EngineOutcome } from "./engine/client.ts";
import {
  ENGINE_FAILURES,
  ProfileRefSchema,
  SANDBOXED_RUN,
  UNSANDBOXED,
  type EngineLimits,
} from "./engine/launch.ts";
import {
  blindedLeak,
  composeReviewPrompt,
  PARAM,
  REVIEW_BLINDING_POLICY_VERSION,
  REVIEW_JOB_VERSION,
  REVIEW_PROMPT_VERSION,
  type Recipe,
  type Source,
} from "./engine/prompts.ts";
import { buildReceipt } from "./engine/receipts.ts";
import {
  parseReviewResult,
  REVIEW_RESULT_SCHEMA,
  ResultRefusal,
  reviewJsonSchema,
  type ReviewResult,
  type Role,
} from "./engine/results.ts";
import {
  assessmentRow,
  EDGE,
  edgeRow,
  filingRow,
  mintRecordId,
  planRow,
  recordRow,
  STATUS,
  steeringReplyRow,
  type Authorship,
  type Row,
} from "./engine/rows.ts";
import type { OperationDeps } from "./explore.ts";
import type { OutputSink } from "./output.ts";

// ---------------------------------------------------------------------------- the input

const bounded = (max: number) => z.string().trim().min(1).max(max);

/**
 * One drawn review as the coordinator claimed it. The identity under review is the REVISION's own
 * and never a mutable chain head: a vote binds to the wording that was read.
 */
const AssignmentSchema = z.strictObject({
  id: bounded(200),
  recordId: bounded(200),
  revisionId: bounded(200),
  rootId: z.string().default(""),
  kind: bounded(40),
  role: z.enum(ROLES),
  lane: z.string().default(""),
  policyVersion: z.string().default(""),
  contextVersion: z.string().default(""),
  /** Whether this assessment is taken blind. Told to the worker, never hidden from it. */
  blinded: z.boolean().default(false),
  /** The assessment this one corrects, when it corrects one; a correction is never blinded. */
  corrects: z.string().default(""),
  fence: z.number().int().min(0).default(0),
  ordinal: z.number().int().min(0).default(0),
  seed: z.string().default(""),
  inputDigest: z.string().default(""),
  expiresAt: z.string().default(""),
});
export type Assignment = z.infer<typeof AssignmentSchema>;

const CapsSchema = z.strictObject({
  toolCalls: z.number().int().min(0).default(0),
  minutes: z.number().int().min(0).max(24 * 60).default(0),
  perRunUsd: z.number().min(0).default(0),
  idleMs: z.number().int().min(0).default(0),
  handshakeMs: z.number().int().min(0).default(0),
});

export const EvaluateInputSchema = z.strictObject({
  /** Empty for a scheduled beat: the machine mints one and the receipt carries it. */
  runId: z.string().default(""),
  machineId: bounded(120),
  engine: z.strictObject({
    binary: bounded(400),
    args: z.array(z.string()).default([]),
    cwd: z.string().default(""),
  }),
  profile: ProfileRefSchema,
  assignment: AssignmentSchema,
  /** The projection of the record under review, as the hub built it for this role. */
  target: z.unknown(),
  alternatives: z.array(z.unknown()).default([]),
  previous: z.array(z.unknown()).default([]),
  /** The filing pass's ledger material; absent for every other role. */
  ledger: z.unknown().optional(),
  /** The backlog pass's material; absent for every other role. */
  backlog: z.unknown().optional(),
  recipe: z.looseObject({
    id: bounded(120),
    version: z.number().int().min(0),
    title: z.string().default(""),
    body: z.string().min(1),
  }),
  sources: z
    .array(z.looseObject({ kind: z.string().default("session"), selector: bounded(500), digest: z.string().default("") }))
    .default([]),
  caps: CapsSchema.default({ toolCalls: 0, minutes: 0, perRunUsd: 0, idleMs: 0, handshakeMs: 0 }),
  /** Records this run authored, so a review cannot endorse its own work (§4.12's independence). */
  authored: z
    .strictObject({ target: z.boolean().default(false), subjects: z.array(z.string()).default([]) })
    .optional(),
  requireContainment: z.boolean().default(true),
});
export type EvaluateInput = z.infer<typeof EvaluateInputSchema>;

// ---------------------------------------------------------------------------- the operation

/**
 * Carries out one assignment and writes its output files. The receipt is written last and
 * returned; a refused launch and a refused submission both produce one, because the assignment's
 * reservation is reconciled from it either way.
 */
export async function evaluate(input: EvaluateInput, out: OutputSink, deps: OperationDeps = {}): Promise<Receipt> {
  const now = deps.now ?? (() => new Date());
  const runId = input.runId === "" ? `run_${randomUUID().replaceAll("-", "")}` : input.runId;
  const startedAt = now();
  const by: Authorship = { runId, at: startedAt.toISOString() };
  const role: Role = input.assignment.role;

  const assessments: Row[] = [];
  const records: Row[] = [];
  const edges: Row[] = [];
  const filings: Row[] = [];
  const plans: Row[] = [];
  const replies: Row[] = [];
  const jobs: EngineOutcome[] = [];
  const counts: Record<string, number> = {};

  let closure: Receipt["closure"] = "completed";
  let reason = "";

  // The blind is audited before anything is launched. A leak is not a degraded run: it is a run
  // that must not happen, and the failure names the key so the projection can be fixed.
  const leak = input.assignment.blinded
    ? blindedLeak({ target: input.target, alternatives: input.alternatives, previous: input.previous })
    : "";
  if (leak !== "") {
    closure = "failed";
    reason = `blinding: the read context of a blinded ${role} review would disclose ${leak}`;
  } else if (input.assignment.blinded && input.previous.length > 0) {
    closure = "failed";
    reason = `blinding: ${input.previous.length} prior evaluations were offered to a blinded ${role} review`;
  } else {
    const controller = new AbortController();
    const deadline =
      input.caps.minutes === 0 ? undefined : setTimeout(() => controller.abort(), input.caps.minutes * 60_000);
    try {
      const attempt = await runReview(input, runId, role, deps, controller);
      jobs.push(attempt.job);
      if (attempt.result === null) {
        closure = controller.signal.aborted ? "stopped" : "failed";
        reason = attempt.reason;
      } else {
        const written = record(input, attempt.result, role, by);
        assessments.push(...written.assessments);
        records.push(...written.records);
        edges.push(...written.edges);
        filings.push(...written.filings);
        plans.push(...written.plans);
        replies.push(...written.replies);
        counts.answer = 1;
        counts[`answer.${written.answer}`] = 1;
      }
    } finally {
      clearTimeout(deadline);
    }
  }

  counts.assessments = assessments.length;
  counts.records = records.length;
  counts.edges = edges.length;
  counts.filings = filings.length;
  counts.plans = plans.length;
  counts.steeringReplies = replies.length;

  await out.write("assessments", assessments);
  await out.write("records", records);
  await out.write("edges", edges);
  await out.write("filings", filings);
  await out.write("plans", plans);
  await out.write("steeringReplies", replies);

  const spent = jobs.reduce((total, job) => total + (job.usage?.costUsd ?? 0), 0);
  if (input.caps.perRunUsd > 0 && spent > input.caps.perRunUsd) {
    counts.ceilingExceeded = 1;
    if (reason === "") reason = `the review spent ${spent.toFixed(4)} against a ceiling of ${input.caps.perRunUsd}`;
  }
  const receipt = buildReceipt({
    runId,
    kind: OPERATIONS.evaluate,
    machineId: input.machineId,
    recipeId: input.recipe.id,
    role,
    preparation: {
      assignment: input.assignment.id,
      record: input.assignment.recordId,
      revision: input.assignment.revisionId,
      lane: input.assignment.lane,
      blinded: input.assignment.blinded,
      policy: input.assignment.policyVersion,
      context: input.assignment.contextVersion,
      fence: input.assignment.fence,
      ordinal: input.assignment.ordinal,
      seed: input.assignment.seed,
      inputDigest: input.assignment.inputDigest,
      blindingPolicy: REVIEW_BLINDING_POLICY_VERSION,
      // §7: the contract this assessment was taken under, for the re-run it is compared against.
      job: { version: REVIEW_JOB_VERSION, prompt: REVIEW_PROMPT_VERSION, schema: REVIEW_RESULT_SCHEMA },
      sessions: input.sources.map((source) => source.selector),
    },
    startedAt,
    finishedAt: now(),
    closure,
    ...(reason === "" ? {} : { reason }),
    counts,
    jobs,
  });
  await out.receipt(receipt);
  return receipt;
}

interface ReviewAttempt {
  job: EngineOutcome;
  result: ReviewResult | null;
  reason: string;
}

/** Launches, supervises and decodes the review's one job. */
async function runReview(
  input: EvaluateInput,
  runId: string,
  role: Role,
  deps: OperationDeps,
  controller: AbortController,
): Promise<ReviewAttempt> {
  const assignment = input.assignment;
  const params: Record<string, string> = {
    [PARAM.reviewRole]: role,
    [PARAM.reviewSubjectKind]: assignment.kind,
    [PARAM.reviewSubjectID]: assignment.revisionId,
    [PARAM.reviewAssignment]: assignment.id,
    [PARAM.reviewPolicy]: assignment.policyVersion,
    [PARAM.reviewContext]: assignment.contextVersion,
    [PARAM.reviewBlinded]: assignment.blinded ? "true" : "false",
  };
  // The lane is withheld from a blinded job: a lane reserved for never-reviewed work says
  // something about the target's prior evaluations, which is what the blind exists to withhold.
  if (!assignment.blinded) params[PARAM.reviewLane] = assignment.lane;

  const recipe: Recipe = {
    id: input.recipe.id,
    version: input.recipe.version,
    ...(input.recipe.title === "" ? {} : { title: input.recipe.title }),
    body: input.recipe.body,
  };
  const sources: Source[] = input.sources.map((source) => ({
    kind: source.kind,
    selector: source.selector,
    digest: source.digest,
  }));
  const tools = deps.tools ?? [];
  const prompt = composeReviewPrompt({
    role,
    recipe,
    target: input.target,
    alternatives: input.alternatives,
    previous: input.previous,
    ...(input.ledger === undefined ? {} : { ledger: input.ledger }),
    ...(input.backlog === undefined ? {} : { backlog: input.backlog }),
    sources,
    params,
    tools,
    blinded: assignment.blinded,
  });

  const self = {
    target: input.authored?.target ?? false,
    subjects: Object.fromEntries((input.authored?.subjects ?? []).map((id) => [id, true as const])),
  };
  const limits: Partial<EngineLimits> = {};
  if (input.caps.toolCalls > 0) limits.maxToolCalls = input.caps.toolCalls;
  if (input.caps.idleMs > 0) limits.idleMs = input.caps.idleMs;
  if (input.caps.handshakeMs > 0) limits.handshakeMs = input.caps.handshakeMs;

  const directory = await mkdtemp(join(deps.workDir ?? tmpdir(), "babel-engine-"));
  try {
    const outcome = await runEngineJob(
      {
        runId,
        profile: input.profile,
        prompt,
        submitSchema: reviewJsonSchema(role),
        tools,
        requirement: input.requireContainment ? SANDBOXED_RUN : UNSANDBOXED,
        accept: (payload) => {
          try {
            parseReviewResult(role, payload, self);
            return "";
          } catch (error) {
            return error instanceof ResultRefusal ? error.message : String(error);
          }
        },
      },
      {
        launch: {
          binary: input.engine.binary,
          args: input.engine.args,
          profile: input.profile,
          runtimeInfoPath: join(directory, "runtime.json"),
          ...(input.engine.cwd === "" ? {} : { cwd: input.engine.cwd }),
        },
        limits,
        ...(deps.broker === undefined ? {} : { broker: deps.broker }),
        ...(deps.now === undefined ? {} : { now: deps.now }),
        signal: controller.signal,
      },
    );
    if (outcome.closure === "failed" || outcome.result === null) {
      const refused = outcome.tools.filter((decision) => !decision.allowed && decision.tool.endsWith("submit_result"));
      const last = refused[refused.length - 1];
      // A refused submission is a recipe to review, not a boundary that broke, so it carries the
      // result-schema code and the reason the model was given.
      const reason =
        last !== undefined
          ? `${ENGINE_FAILURES.resultSchema}: ${last.reason}`
          : outcome.failure !== null
            ? `${outcome.failure.code}: ${outcome.failure.message}`
            : "the review submitted no assessment";
      return { job: outcome, result: null, reason };
    }
    return { job: outcome, result: parseReviewResult(role, outcome.result, self), reason: "" };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------------------- the rows

interface WrittenReview {
  assessments: Row[];
  records: Row[];
  edges: Row[];
  filings: Row[];
  plans: Row[];
  replies: Row[];
  /** Which answer this pass reached, for the receipt: a vote, a skip, a filing, a backlog act. */
  answer: string;
}

/**
 * Turns one accepted submission into the store's rows.
 *
 * Every role writes the assessment: it is the attributed, append-only record that one run judged
 * one exact revision in one role, and it carries the claim and the lane the draw came from so the
 * coordinator can reconcile its reservation from the row rather than from the receipt.
 *
 * The filing and backlog roles write what their answer makes durable beside it. A filing is a
 * `filings` row — including "about nothing in particular", which is an answer and therefore a row
 * with an empty entity and the reason in the rationale. A topic change or a backlog act is a
 * proposal record plus a `plans` row keyed on it: the operator reads the proposal, and accepting
 * it is what applies the plan.
 */
function record(input: EvaluateInput, result: ReviewResult, role: Role, by: Authorship): WrittenReview {
  const assignment = input.assignment;
  const written: WrittenReview = {
    assessments: [],
    records: [],
    edges: [],
    filings: [],
    plans: [],
    replies: [],
    answer: "assessment",
  };
  written.assessments.push(
    assessmentRow(
      {
        recordId: assignment.recordId,
        revisionId: assignment.revisionId,
        role,
        vote: result.vote === "" ? null : result.vote,
        lane: assignment.lane,
        claimId: assignment.id,
        payload: {
          vote: result.vote,
          contributions: result.contributions,
          outcome: result.outcome,
          results: result.results,
          environment: result.environment,
          asOf: result.asOf,
          uncertainty: result.uncertainty,
          skip: result.skip,
          filing: result.filing,
          topic: result.topic,
          noTopic: result.noTopic,
          noChange: result.noChange,
          consolidate: result.consolidate,
          supersede: result.supersede,
          retire: result.retire,
          promote: result.promote,
          keep: result.keep,
          blinded: assignment.blinded,
          corrects: assignment.corrects,
          policyVersion: assignment.policyVersion,
          contextVersion: assignment.contextVersion,
          recipe: { id: input.recipe.id, version: input.recipe.version },
        },
      },
      by,
    ),
  );
  if (result.skip !== "") {
    written.answer = "skip";
    return written;
  }
  if (result.vote !== "") written.answer = `vote.${result.vote}`;
  if (result.outcome !== "") written.answer = `outcome.${result.outcome}`;

  if (role === "filing") writeFiling(input, result, written, by);
  if (role === "backlog") writeBacklog(input, result, written, by);
  return written;
}

/** §4.13's filing pass: one of four answers, each with its own durable consequence. */
function writeFiling(input: EvaluateInput, result: ReviewResult, written: WrittenReview, by: Authorship): void {
  const assignment = input.assignment;
  const filing = result.filing;
  if (filing !== null) {
    // The entity is a name or alias the ledger already holds, resolved on ingestion: a name
    // nobody has created is refused there and the record stays honestly unfiled, which is what
    // makes a run raise a topic proposal instead of minting an identity (§4.8).
    written.filings.push(filingRow(assignment.recordId, filing.entity, filing.rationale, by));
    written.answer = "filing.filed";
    return;
  }
  if (result.noTopic !== null) {
    written.filings.push(filingRow(assignment.recordId, "", result.noTopic.reason, by));
    written.answer = "filing.no-topic";
    return;
  }
  const noChange = result.noChange;
  if (noChange !== null) {
    // An ask obeyed without judgement would be the operator editing the ledger through a run.
    // The reasoned no lands as a reply where he asked, and nothing about the ledger moved.
    written.replies.push(steeringReplyRow(noChange.ask_id, noChange.ask_id, noChange.reason, by));
    written.answer = "filing.no-change";
    return;
  }
  const topic = result.topic;
  if (topic === null) return;
  const ref = `topic/${topic.operation}/${topic.identity === "" ? topic.targets.join("+") : topic.identity}`;
  const proposalId = mintRecordId("proposal", by.runId, ref);
  const title =
    topic.operation === "create"
      ? `Create the topic ${topic.name}`
      : topic.operation === "split"
        ? `Split ${topic.targets[0] ?? ""} out into ${topic.name}`
        : topic.operation === "merge"
          ? `Merge ${topic.targets[0] ?? ""} into ${topic.targets[1] ?? ""}`
          : `Retire the topic ${topic.targets[0] ?? ""}`;
  written.records.push(
    recordRow(
      {
        id: proposalId,
        kind: "proposal",
        recipeId: input.recipe.id,
        recipeVersion: input.recipe.version,
        title,
        payload: {
          title,
          problem: topic.reasoning,
          outcome: title,
          impact: "moderate",
          classification: "private",
          topic,
          about: { kind: assignment.kind, id: assignment.recordId },
        },
      },
      by,
    ),
  );
  // The proposal addresses the record it would file: §4.13 publishes a topic change through
  // Babel's ordinary output path, and the edge is what makes the two readable together.
  written.edges.push(
    edgeRow(
      {
        kind: EDGE.addresses,
        fromKind: "proposal",
        fromId: proposalId,
        toKind: assignment.kind,
        toId: assignment.recordId,
      },
      by,
    ),
  );
  written.plans.push(
    planRow(
      {
        kind: "topic",
        subjectId: proposalId,
        operation: topic.operation,
        dedupeKey: topic.identity === "" ? topic.targets.join("+") : topic.identity,
        payload: { ...topic, record: assignment.recordId },
      },
      by,
    ),
  );
  written.answer = `filing.topic.${topic.operation}`;
}

/**
 * The status each backlog act would append if the operator accepts it. It is recorded in the plan
 * rather than written now: a candidate that is consolidated, superseded or retired keeps its
 * record, its observations and its history, and gains one appended status event saying a later
 * record speaks for it — when he says so.
 */
const PLANNED_STATUS: Record<string, string> = {
  consolidate: STATUS.promoted,
  supersede: STATUS.superseded,
  retire: STATUS.retired,
  promote: STATUS.promoted,
};

/** §4.13's backlog pass: five answers, four of them proposals the operator rules on. */
function writeBacklog(input: EvaluateInput, result: ReviewResult, written: WrittenReview, by: Authorship): void {
  const assignment = input.assignment;
  if (result.keep !== null) {
    // A backlog full of open questions nobody has had time for is a healthy backlog. The
    // assessment is the whole record of the decision; nothing moves.
    written.answer = "backlog.keep";
    return;
  }
  const consolidate = result.consolidate;
  const supersede = result.supersede;
  const retire = result.retire;
  const promote = result.promote;
  const operation =
    consolidate !== null ? "consolidate" : supersede !== null ? "supersede" : retire !== null ? "retire" : "promote";
  const payload =
    consolidate !== null
      ? { consolidate, candidate: assignment.recordId }
      : supersede !== null
        ? { supersede, candidate: assignment.recordId }
        : retire !== null
          ? { retire, candidate: assignment.recordId }
          : { promote, candidate: assignment.recordId };
  const title =
    consolidate !== null
      ? `Consolidate ${consolidate.hypotheses.length} candidates into ${consolidate.finding.title}`
      : supersede !== null
        ? `Supersede this candidate with ${supersede.by}`
        : retire !== null
          ? "Retire this candidate"
          : `Promote an observation to a fact about ${promote?.entity ?? ""}`;
  const problem =
    consolidate !== null
      ? consolidate.finding.why_it_matters
      : supersede !== null
        ? supersede.reason
        : retire !== null
          ? retire.reason
          : (promote?.reason ?? "");
  const proposalId = mintRecordId("proposal", by.runId, `backlog/${operation}/${assignment.recordId}`);
  written.records.push(
    recordRow(
      {
        id: proposalId,
        kind: "proposal",
        recipeId: input.recipe.id,
        recipeVersion: input.recipe.version,
        title,
        payload: { title, problem, outcome: title, impact: "moderate", classification: "private", backlog: payload },
      },
      by,
    ),
  );
  written.edges.push(
    edgeRow(
      {
        kind: EDGE.addresses,
        fromKind: "proposal",
        fromId: proposalId,
        toKind: assignment.kind,
        toId: assignment.recordId,
      },
      by,
    ),
  );
  written.plans.push(
    planRow(
      {
        kind: "backlog",
        subjectId: proposalId,
        operation,
        // The status the plan would append — `superseded`, `retired`, `promoted` — travels in the
        // payload and is NOT written here. §4.13's last paragraph makes every backlog act a
        // proposal the operator rules on: a status event written now would settle the candidate
        // before he read it, and nothing is deleted either way.
        payload: { ...payload, status: PLANNED_STATUS[operation] },
      },
      by,
    ),
  );
  written.answer = `backlog.${operation}`;
}
