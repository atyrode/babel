/*
  THE `explore` OPERATION (plan §4): a preparation, the recipes selected for it and the run's caps
  go in; records, edges, status events, questions and a receipt come out as job output files, which
  the hub ingests into the store on completion. Ported from internal/explore's control plane.

  Four rules of the Go original survive here, and each is a property a reviewer can check.

  Every candidate is persisted before anything sorts it (§5.2). A budget chooses what is explored
  NOW, never which ideas are permitted, so a candidate past the budget is written and deferred
  with the run's own reason and the resumed run finds it on the frontier.

  The development path is mandatory (§4.2). A result that skips a step — a consolidation whose
  supporting observations do not exist — is refused while the model can still correct it, and the
  refusal is the run's recorded reason. It is never repaired: the repair would be Babel inventing
  the evidence step the worker skipped.

  A stage is a unit of authority (§5.4). The challenger and the synthesizer are separate jobs with
  their own prompt, their own result schema and their own receipt line, and a failed one leaves the
  earlier stage's records exactly as they were — a failed pass does not erase successful work.

  Nothing here publishes, applies or rules. Every output is a proposal for the operator, and the
  only thing this operation writes is files.
*/

import { randomUUID } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { z } from "zod";
import { type Receipt } from "../contract.ts";
import { runEngineJob, type EngineOutcome, type HostTool, type ToolBroker } from "./engine/client.ts";
import {
  ENGINE_FAILURES,
  ProfileRefSchema,
  SANDBOXED_RUN,
  UNSANDBOXED,
  type EngineLimits,
} from "./engine/launch.ts";
import {
  composeExplorePrompt,
  JOB_VERSION,
  PARAM,
  PROMPT_VERSION,
  type Recipe,
  type Source,
} from "./engine/prompts.ts";
import { buildReceipt } from "./engine/receipts.ts";
import {
  exploreJsonSchema,
  parseExploreResult,
  RESULT_SCHEMA,
  ResultRefusal,
  STAGES,
  type Evidence,
  type ExploreResult,
  type Stage,
} from "./engine/results.ts";
import {
  EDGE,
  edgeRow,
  mintRecordId,
  questionRow,
  recordRow,
  statusRow,
  STATUS,
  type Authorship,
  type Row,
} from "./engine/rows.ts";
import type { OutputSink } from "./output.ts";

// ---------------------------------------------------------------------------- the input

const bounded = (max: number) => z.string().trim().min(1).max(max);

/** One session the run was prepared over, as the `prepare` operation fixed it. */
const SelectionSchema = z.looseObject({
  harness: bounded(40),
  sourceId: bounded(400),
  /** `harness/sourceId`, which is what a `cites` edge names and what the search tool filters on. */
  selector: z.string().default(""),
  /** The capture digest, and the local path the capture was read from, when the caller has it. */
  digest: z.string().default(""),
  path: z.string().default(""),
  snapshot: z.string().default(""),
});

/** One cookbook recipe with the stages it declares participation in (§5.1). */
const RecipeSchema = z.looseObject({
  id: bounded(120),
  version: z.number().int().min(0),
  title: z.string().default(""),
  body: z.string().min(1),
  stages: z.array(z.enum(STAGES)).default(["explore"]),
});

/** What the operator's caps bound. They bound this pass, never what may exist. */
const CapsSchema = z.strictObject({
  /** Tool calls the run may make before every further one is refused. */
  toolCalls: z.number().int().min(0).default(0),
  /** How long the whole run may take. Zero is the engine's own idle bound and nothing more. */
  minutes: z.number().int().min(0).max(24 * 60).default(0),
  /** The ceiling the operator set. It is recorded and compared; the engine reports cost at the end. */
  perRunUsd: z.number().min(0).default(0),
  idleMs: z.number().int().min(0).default(0),
  handshakeMs: z.number().int().min(0).default(0),
});

export const ExploreInputSchema = z.strictObject({
  /** Empty for a scheduled beat, whose input is fixed at registration: the machine mints one. */
  runId: z.string().default(""),
  machineId: bounded(120),
  engine: z.strictObject({
    binary: bounded(400),
    args: z.array(z.string()).default([]),
    cwd: z.string().default(""),
  }),
  profile: ProfileRefSchema,
  preparation: z.strictObject({
    id: z.string().default(""),
    selection: z.array(SelectionSchema).default([]),
  }),
  recipes: z.array(RecipeSchema).min(1),
  /** The stages to run, in order. A stage no selected recipe declares is skipped and recorded. */
  stages: z.array(z.enum(STAGES)).min(1).default(["explore"]),
  caps: CapsSchema.default({ toolCalls: 0, minutes: 0, perRunUsd: 0, idleMs: 0, handshakeMs: 0 }),
  /** The refine-first context: prior records this run may refine rather than duplicate (#87). */
  related: z
    .strictObject({
      framing: z.string().default(""),
      records: z
        .array(z.strictObject({ kind: z.string(), id: z.string(), summary: z.string().default("") }))
        .default([]),
    })
    .optional(),
  /** Durable records a separate pass is asked to examine, so its result may name them back. */
  brief: z
    .strictObject({
      hypotheses: z.array(z.string()).default([]),
      observations: z.array(z.string()).default([]),
      objections: z.array(z.string()).default([]),
    })
    .optional(),
  /** False only for a run the operator deliberately relaxed; the strict default is the norm. */
  requireContainment: z.boolean().default(true),
});
export type ExploreInput = z.infer<typeof ExploreInputSchema>;

/** What an operation needs from the machine around it beyond its input. */
export interface OperationDeps {
  /** The evidence facilities this run grants, and what answers them. */
  tools?: readonly HostTool[];
  broker?: ToolBroker;
  /**
   * Whether this run was served the bytes a locator names. §4.3's provenance check: a plausible
   * fabrication reads exactly like provenance once stored, so a citation is verified against the
   * retrieval trace before the claim becomes durable. Absent means no facility served anything,
   * and the receipt records how many citations therefore went unverified rather than pretending
   * they were checked.
   */
  served?: (locator: Evidence["locator"]) => boolean;
  now?: () => Date;
  /** Where the per-launch runtime-info sidecar directory is made; the system temp dir by default. */
  workDir?: string;
}

// ---------------------------------------------------------------------------- the operation

/**
 * Runs one exploration and writes its output files. The receipt is written last and returned: it
 * is the record of what happened, which is needed most when the run failed.
 */
export async function explore(input: ExploreInput, out: OutputSink, deps: OperationDeps = {}): Promise<Receipt> {
  const now = deps.now ?? (() => new Date());
  const runId = input.runId === "" ? `run_${randomUUID().replaceAll("-", "")}` : input.runId;
  const startedAt = now();
  const by: Authorship = { runId, at: startedAt.toISOString() };

  const records: Row[] = [];
  const edges: Row[] = [];
  const statuses: Row[] = [];
  const questions: Row[] = [];
  const jobs: EngineOutcome[] = [];
  const counts: Record<string, number> = { hypotheses: 0, observations: 0, findings: 0, proposals: 0, unverifiedCitations: 0 };
  const brief = {
    hypotheses: [...(input.brief?.hypotheses ?? [])],
    observations: [...(input.brief?.observations ?? [])],
    objections: [...(input.brief?.objections ?? [])],
  };

  const sources: Source[] = input.preparation.selection.map((entry) => ({
    kind: "session",
    selector: entry.selector === "" ? `${entry.harness}/${entry.sourceId}` : entry.selector,
    digest: entry.digest,
    snapshot: entry.snapshot,
  }));
  const limits = limitsFrom(input.caps);
  const controller = new AbortController();
  const deadline =
    input.caps.minutes === 0 ? undefined : setTimeout(() => controller.abort(), input.caps.minutes * 60_000);

  let closure: Receipt["closure"] = "completed";
  let reason = "";
  try {
    for (const stage of input.stages) {
      const recipes = input.recipes.filter((recipe) => recipe.stages.includes(stage));
      if (recipes.length === 0) {
        // A stage no selected recipe declares is not a failure of the run: it is a stage the
        // cookbook did not ask for, and recording it is what makes that legible.
        counts[`skipped.${stage}`] = 1;
        continue;
      }
      const outcome = await runStage({ input, stage, recipes, sources, runId, limits, deps, brief, controller });
      jobs.push(outcome.job);
      if (outcome.result === null) {
        closure = controller.signal.aborted ? "stopped" : "failed";
        reason = outcome.reason;
        break;
      }
      const written = writeStage({
        stage,
        result: outcome.result,
        recipes,
        selection: input.preparation.selection,
        by,
        served: deps.served,
      });
      records.push(...written.records);
      edges.push(...written.edges);
      statuses.push(...written.statuses);
      questions.push(...written.questions);
      counts.hypotheses = (counts.hypotheses ?? 0) + written.counts.hypotheses;
      counts.observations = (counts.observations ?? 0) + written.counts.observations;
      counts.findings = (counts.findings ?? 0) + written.counts.findings;
      counts.proposals = (counts.proposals ?? 0) + written.counts.proposals;
      counts.unverifiedCitations = (counts.unverifiedCitations ?? 0) + written.counts.unverifiedCitations;
      brief.hypotheses.push(...written.emitted.hypotheses);
      brief.observations.push(...written.emitted.observations);
      brief.objections.push(...written.emitted.objections);
    }
  } finally {
    clearTimeout(deadline);
  }

  counts.records = records.length;
  counts.edges = edges.length;
  counts.statusEvents = statuses.length;
  counts.questions = questions.length;

  // The output files go out whatever the closure: a stage that produced records before a later
  // one failed produced them, and a failed run's receipt is exactly the record that is needed.
  await out.write("records", records);
  await out.write("edges", edges);
  await out.write("statusEvents", statuses);
  await out.write("questions", questions);

  const spent = jobs.reduce((total, job) => total + (job.usage?.costUsd ?? 0), 0);
  if (input.caps.perRunUsd > 0 && spent > input.caps.perRunUsd) {
    counts.ceilingExceeded = 1;
    if (reason === "") reason = `the run spent ${spent.toFixed(4)} against a ceiling of ${input.caps.perRunUsd}`;
  }
  const receipt = buildReceipt({
    runId,
    kind: "explore",
    machineId: input.machineId,
    ...(input.recipes[0] === undefined ? {} : { recipeId: input.recipes[0].id }),
    preparation: {
      id: input.preparation.id,
      sessions: sources.map((source) => source.selector),
      stages: input.stages,
      caps: input.caps,
      // §7: a later re-run is compared against the contract the earlier one applied, so the
      // job, prompt and result-schema versions travel with the scope rather than being inferred.
      job: { version: JOB_VERSION, prompt: PROMPT_VERSION, schema: RESULT_SCHEMA },
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

/** The transport and supervision bounds the operator's caps select. */
function limitsFrom(caps: z.infer<typeof CapsSchema>): Partial<EngineLimits> {
  const limits: Partial<EngineLimits> = {};
  if (caps.toolCalls > 0) limits.maxToolCalls = caps.toolCalls;
  if (caps.idleMs > 0) limits.idleMs = caps.idleMs;
  if (caps.handshakeMs > 0) limits.handshakeMs = caps.handshakeMs;
  return limits;
}

interface StageRun {
  job: EngineOutcome;
  result: ExploreResult | null;
  reason: string;
}

/** Launches, supervises and decodes one stage's job. */
async function runStage(args: {
  input: ExploreInput;
  stage: Stage;
  recipes: readonly z.infer<typeof RecipeSchema>[];
  sources: readonly Source[];
  runId: string;
  limits: Partial<EngineLimits>;
  deps: OperationDeps;
  brief: { hypotheses: string[]; observations: string[]; objections: string[] };
  controller: AbortController;
}): Promise<StageRun> {
  const { input, stage, recipes, sources, runId, deps } = args;
  const params: Record<string, string> = {
    [PARAM.stage]: stage,
    [PARAM.briefHypotheses]: args.brief.hypotheses.join(","),
    [PARAM.briefObservations]: args.brief.observations.join(","),
  };
  if (stage === "synthesize") params[PARAM.briefObjections] = args.brief.objections.join(",");

  const recipeText: Recipe[] = recipes.map((recipe) => ({
    id: recipe.id,
    version: recipe.version,
    ...(recipe.title === "" ? {} : { title: recipe.title }),
    body: recipe.body,
  }));
  const tools = deps.tools ?? [];
  const prompt = composeExplorePrompt({
    stage,
    recipes: recipeText,
    sources,
    params,
    tools,
    ...(input.related === undefined ? {} : { related: input.related }),
  });

  const directory = await mkdtemp(join(deps.workDir ?? tmpdir(), "babel-engine-"));
  try {
    const outcome = await runEngineJob(
      {
        runId: `${runId}/${stage}`,
        profile: input.profile,
        prompt,
        submitSchema: exploreJsonSchema(stage),
        tools,
        requirement: input.requireContainment ? SANDBOXED_RUN : UNSANDBOXED,
        // Accept is what can be decided while the model can still correct itself: the shape, the
        // refs within the result, the recipe provenance, and every citation against what this run
        // was served. A refusal is what the model reads back.
        accept: (payload) => {
          try {
            const result = parseExploreResult(stage, payload);
            checkRecipes(result, recipes, stage);
            checkCitations(result, deps.served);
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
        limits: args.limits,
        ...(deps.broker === undefined ? {} : { broker: deps.broker }),
        ...(deps.now === undefined ? {} : { now: deps.now }),
        signal: args.controller.signal,
      },
    );
    if (outcome.closure === "failed" || outcome.result === null) {
      return { job: outcome, result: null, reason: stageFailureReason(stage, outcome) };
    }
    // The accepted submission already parsed once, when `accept` admitted it; parsing the same
    // bytes again here is what keeps the receipt's payload and the written rows from diverging.
    return { job: outcome, result: parseExploreResult(stage, outcome.result), reason: "" };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

/**
 * Why a stage produced nothing.
 *
 * A refused submission is the interesting case and it gets its own code: the engine's turn ended,
 * it left cleanly, and Babel refused what it submitted, which is a recipe to review rather than a
 * boundary that broke. So the reason is the refusal the model was given under `result-schema`,
 * not the bare "none accepted" the transport reports.
 */
function stageFailureReason(stage: Stage, outcome: EngineOutcome): string {
  const refused = outcome.tools.filter((decision) => !decision.allowed && decision.tool.endsWith("submit_result"));
  const last = refused[refused.length - 1];
  if (last !== undefined) return `${ENGINE_FAILURES.resultSchema}: ${stage}: ${last.reason}`;
  if (outcome.failure !== null) return `${stage}: ${outcome.failure.code}: ${outcome.failure.message}`;
  return `${stage}: the stage submitted no result`;
}

/**
 * Refuses a result whose claims cite a recipe this stage did not select, so the model corrects
 * the provenance rather than losing the claim: §5.1 provenance the receipt cannot confirm is not
 * provenance.
 */
function checkRecipes(result: ExploreResult, recipes: readonly z.infer<typeof RecipeSchema>[], stage: Stage): void {
  const allowed: Record<string, true> = {};
  for (const recipe of recipes) allowed[`${recipe.id}@${recipe.version}`] = true;
  const cited: { ref: string; id: string; version: number }[] = [];
  for (const candidate of result.candidates) {
    for (const observation of candidate.observations) {
      cited.push({ ref: observation.ref, ...observation.recipe });
    }
  }
  for (const objection of result.objections) cited.push({ ref: objection.ref, ...objection.recipe });
  for (const claim of cited) {
    if (allowed[`${claim.id}@${claim.version}`] !== true) {
      throw new ResultRefusal(
        "unknown-reference",
        `${JSON.stringify(claim.ref)} cites ${claim.id}@${claim.version}, which the ${stage} stage did not select`,
      );
    }
  }
}

/** Every locator a result carries, with the item that cited it. */
function citations(result: ExploreResult): { ref: string; evidence: Evidence }[] {
  const out: { ref: string; evidence: Evidence }[] = [];
  for (const candidate of result.candidates) {
    for (const observation of candidate.observations) {
      for (const item of [...observation.claim.evidence, ...observation.claim.counter_evidence]) {
        out.push({ ref: observation.ref, evidence: item });
      }
    }
    const remedy = candidate.remedy;
    if (remedy !== undefined) {
      for (const item of [...remedy.proposal.supporting, ...remedy.proposal.conflicting]) {
        out.push({ ref: remedy.ref, evidence: item });
      }
    }
  }
  for (const objection of result.objections) {
    for (const item of [...objection.claim.evidence, ...objection.claim.counter_evidence]) {
      out.push({ ref: objection.ref, evidence: item });
    }
  }
  for (const consolidation of result.consolidations) {
    for (const item of consolidation.finding.counter_evidence) {
      out.push({ ref: consolidation.ref, evidence: item });
    }
    const proposal = consolidation.proposal;
    if (proposal !== undefined) {
      for (const item of [...proposal.supporting, ...proposal.conflicting]) {
        out.push({ ref: consolidation.ref, evidence: item });
      }
    }
  }
  return out;
}

/** Refuses a citation this run was never served, when a facility served anything at all. */
function checkCitations(result: ExploreResult, served: OperationDeps["served"]): void {
  if (served === undefined) return;
  for (const citation of citations(result)) {
    if (!served(citation.evidence.locator)) {
      throw new ResultRefusal(
        "support",
        `${JSON.stringify(citation.ref)} cites ${citation.evidence.locator.path} at ` +
          `${citation.evidence.locator.digest}, which this run was not served`,
      );
    }
  }
}

// ---------------------------------------------------------------------------- the rows

interface WrittenStage {
  records: Row[];
  edges: Row[];
  statuses: Row[];
  questions: Row[];
  emitted: { hypotheses: string[]; observations: string[]; objections: string[] };
  counts: { hypotheses: number; observations: number; findings: number; proposals: number; unverifiedCitations: number };
}

/**
 * Turns one stage's result into the store's own rows.
 *
 * Two decisions are the challenger's authority made mechanical (§5.4). A locator-backed objection
 * becomes a counter-observation hanging off the hypothesis it attacks, marked `contradicts`; an
 * objection resting on a consequence, a missing check or an alternative carries no locator, so
 * §4.3 forbids it from being an observation and it becomes a new candidate linked as contradicting
 * its target. Neither path can reach a finding.
 */
function writeStage(args: {
  stage: Stage;
  result: ExploreResult;
  recipes: readonly z.infer<typeof RecipeSchema>[];
  selection: readonly z.infer<typeof SelectionSchema>[];
  by: Authorship;
  served: OperationDeps["served"];
}): WrittenStage {
  const { result, by } = args;
  const written: WrittenStage = {
    records: [],
    edges: [],
    statuses: [],
    questions: [],
    emitted: { hypotheses: [], observations: [], objections: [] },
    counts: { hypotheses: 0, observations: 0, findings: 0, proposals: 0, unverifiedCitations: 0 },
  };
  /** ref or durable id → the durable id and kind it resolves to. */
  const minted: Record<string, { id: string; kind: string }> = {};
  /** How many statuses this run has appended to a record it created. */
  const seq: Record<string, number> = {};

  const sessionOf = sessionResolver(args.selection);
  const cite = (from: { kind: string; id: string }, evidence: readonly Evidence[], note: string): void => {
    for (const [index, item] of evidence.entries()) {
      written.edges.push(
        edgeRow(
          {
            kind: EDGE.cites,
            fromKind: from.kind,
            fromId: from.id,
            toKind: "session",
            toId: sessionOf(item.locator.path),
            position: index,
            note: item.note === "" ? note : item.note,
          },
          by,
        ),
      );
      if (args.served === undefined) written.counts.unverifiedCitations += 1;
    }
  };

  for (const candidate of result.candidates) {
    const id = mintRecordId("hypothesis", by.runId, candidate.ref);
    minted[candidate.ref] = { id, kind: "hypothesis" };
    written.records.push(
      recordRow({ id, kind: "hypothesis", title: candidate.hypothesis.statement, payload: candidate.hypothesis }, by),
    );
    seq[id] = 1;
    written.statuses.push(statusRow(id, 1, STATUS.untriaged, "", by));
    written.emitted.hypotheses.push(id);
    written.counts.hypotheses += 1;

    for (const observation of candidate.observations) {
      const obsId = mintRecordId("observation", by.runId, observation.ref);
      minted[observation.ref] = { id: obsId, kind: "observation" };
      written.records.push(
        recordRow(
          {
            id: obsId,
            kind: "observation",
            parentId: id,
            recipeId: observation.recipe.id,
            recipeVersion: observation.recipe.version,
            title: observation.claim.claim,
            payload: observation.claim,
          },
          by,
        ),
      );
      cite({ kind: "observation", id: obsId }, observation.claim.evidence, "cited by the claim");
      written.emitted.observations.push(obsId);
      written.counts.observations += 1;
    }

    const remedy = candidate.remedy;
    if (remedy !== undefined) {
      // #114: the value-claim half of a candidate. It rests on the claim beside it and on no
      // finding, so it is a candidate proposal `addresses`ing the hypothesis rather than §4.5's
      // consolidated artifact — the operator can accept the claim and reject the remedy.
      const proposalId = mintRecordId("proposal", by.runId, remedy.ref);
      minted[remedy.ref] = { id: proposalId, kind: "proposal" };
      written.records.push(
        recordRow({ id: proposalId, kind: "proposal", title: remedy.proposal.title, payload: remedy.proposal }, by),
      );
      written.edges.push(
        edgeRow(
          { kind: EDGE.addresses, fromKind: "proposal", fromId: proposalId, toKind: "hypothesis", toId: id },
          by,
        ),
      );
      cite({ kind: "proposal", id: proposalId }, remedy.proposal.supporting, "supporting material");
      written.counts.proposals += 1;
    }
  }

  for (const consolidation of result.consolidations) {
    const findingId = mintRecordId("finding", by.runId, consolidation.ref);
    minted[consolidation.ref] = { id: findingId, kind: "finding" };
    written.records.push(
      recordRow(
        { id: findingId, kind: "finding", title: consolidation.finding.title, payload: consolidation.finding },
        by,
      ),
    );
    written.counts.findings += 1;
    for (const [index, name] of consolidation.observations.entries()) {
      const target = minted[name]?.id ?? name;
      written.edges.push(
        edgeRow(
          {
            kind: EDGE.consolidates,
            fromKind: "finding",
            fromId: findingId,
            toKind: "observation",
            toId: target,
            position: index,
          },
          by,
        ),
      );
    }
    // §4.2's promotion: a candidate whose observations a finding consolidates has been carried
    // through the path, and the status history is where that is visible.
    for (const name of consolidation.observations) {
      const observation = result.candidates
        .flatMap((candidate) => candidate.observations.map((item) => ({ candidate, item })))
        .find((pair) => pair.item.ref === name);
      if (observation === undefined) continue;
      const hypothesisId = minted[observation.candidate.ref]?.id;
      if (hypothesisId === undefined) continue;
      const next = (seq[hypothesisId] ?? 0) + 1;
      if (next === 2) {
        seq[hypothesisId] = next;
        written.statuses.push(statusRow(hypothesisId, next, STATUS.promoted, "consolidated by a finding", by));
      }
    }
    const proposal = consolidation.proposal;
    if (proposal !== undefined) {
      const proposalId = mintRecordId("proposal", by.runId, `${consolidation.ref}/proposal`);
      written.records.push(
        recordRow({ id: proposalId, kind: "proposal", title: proposal.title, payload: proposal }, by),
      );
      written.edges.push(
        edgeRow(
          { kind: EDGE.derivedFrom, fromKind: "proposal", fromId: proposalId, toKind: "finding", toId: findingId },
          by,
        ),
      );
      cite({ kind: "proposal", id: proposalId }, proposal.supporting, "supporting material");
      written.counts.proposals += 1;
    }
  }

  for (const objection of result.objections) {
    const target = minted[objection.hypothesis]?.id ?? objection.hypothesis;
    if (objection.claim.evidence.length > 0) {
      const obsId = mintRecordId("observation", by.runId, objection.ref);
      minted[objection.ref] = { id: obsId, kind: "observation" };
      written.records.push(
        recordRow(
          {
            id: obsId,
            kind: "observation",
            parentId: target,
            recipeId: objection.recipe.id,
            recipeVersion: objection.recipe.version,
            title: objection.claim.claim,
            payload: { ...objection.claim, grounds: objection.grounds },
          },
          by,
        ),
      );
      cite({ kind: "observation", id: obsId }, objection.claim.evidence, "cited by the objection");
      written.edges.push(
        edgeRow(
          { kind: EDGE.contradicts, fromKind: "observation", fromId: obsId, toKind: "hypothesis", toId: target },
          by,
        ),
      );
      written.emitted.objections.push(obsId);
      written.counts.observations += 1;
      continue;
    }
    const id = mintRecordId("hypothesis", by.runId, objection.ref);
    minted[objection.ref] = { id, kind: "hypothesis" };
    written.records.push(
      recordRow(
        {
          id,
          kind: "hypothesis",
          recipeId: objection.recipe.id,
          recipeVersion: objection.recipe.version,
          title: objection.claim.claim,
          payload: {
            statement: objection.claim.claim,
            origin_cues: [`objection on ${objection.grounds} grounds`],
            provisional_labels: [],
            novelty: 0,
            priority: 0,
            notes: objection.claim.category,
          },
        },
        by,
      ),
    );
    seq[id] = 1;
    written.statuses.push(statusRow(id, 1, STATUS.untriaged, "", by));
    written.edges.push(
      edgeRow({ kind: EDGE.contradicts, fromKind: "hypothesis", fromId: id, toKind: "hypothesis", toId: target }, by),
    );
    written.emitted.objections.push(id);
    written.counts.hypotheses += 1;
  }

  for (const [status, disposals] of [
    [STATUS.deferred, result.deferred],
    [STATUS.rejected, result.rejected],
  ] as const) {
    for (const disposal of disposals) {
      const resolved = minted[disposal.hypothesis];
      const id = resolved?.id ?? disposal.hypothesis;
      // A record this run created knows its own sequence; one from the brief does not, and 0 is
      // how a row says "append after the newest" rather than colliding with a seq it never read.
      const next = resolved === undefined ? 0 : (seq[id] ?? 0) + 1;
      if (resolved !== undefined) seq[id] = next;
      written.statuses.push(statusRow(id, next, status, disposal.reason, by));
    }
  }

  for (const question of result.questions) {
    written.questions.push(
      questionRow(
        {
          ref: question.ref,
          subjects: question.subjects,
          predicates: question.predicates,
          blocks: question.hypothesis === "" ? "" : (minted[question.hypothesis]?.id ?? question.hypothesis),
          prompt: question.prompt,
          why: question.why_asked,
        },
        by,
      ),
    );
  }
  return written;
}

/**
 * Resolves a locator's path to the session selector a `cites` edge names. The preparation is what
 * knows the mapping; a path no selection entry claims is carried verbatim, because an edge to a
 * session the catalog has not seen is still the honest record of what was read.
 */
function sessionResolver(selection: readonly z.infer<typeof SelectionSchema>[]): (path: string) => string {
  const byPath: Record<string, string> = {};
  const selectors: string[] = [];
  for (const entry of selection) {
    const selector = entry.selector === "" ? `${entry.harness}/${entry.sourceId}` : entry.selector;
    selectors.push(selector);
    byPath[selector] = selector;
    if (entry.path !== "") byPath[entry.path] = selector;
    byPath[entry.sourceId] = selector;
  }
  return (path: string) => {
    const exact = byPath[path];
    if (exact !== undefined) return exact;
    const suffix = selectors.find((selector) => path.endsWith(selector));
    return suffix ?? path;
  };
}
