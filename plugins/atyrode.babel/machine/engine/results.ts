/*
  THE RESULT CONTRACTS: what a stage of an exploration and what a review role may submit, as one
  declaration each. Ported from internal/explore/{result.go,schema.go,stages.go,review.go,
  reviewfiling.go,reviewbacklog.go}.

  The JSON Schema the submit tool is registered with is GENERATED from the zod schema below rather
  than written beside it, which is the whole point of the Go original: a hand-maintained schema is
  the second declaration a new payload field silently misses. The engine validates every call
  against it structurally before Babel sees one, and `parseExploreResult`/`parseReviewResult` then
  check what a schema cannot — the refs within a result, the closed vocabularies, the support an
  outcome claim needs, and the authority the stage or role actually has.

  Authority is enforced by ABSENCE, not by refusal after the fact. A stage's schema omits the
  fields it may not fill — a challenger is never offered a `consolidations` field it would be
  refused for filling — and because each schema is strict, a payload that carried one anyway is a
  refused submission with the reason, never a quietly dropped field.
*/

import { z } from "zod";
import { ROLES, VOTES } from "../../contract.ts";

// ---------------------------------------------------------------------------- versions

/**
 * The payload shape an exploration submits. A change to the shape is a new schema: a payload
 * interpreted under the wrong one would produce durable records nobody wrote.
 */
export const RESULT_SCHEMA = "babel.analysis-result/1";
/**
 * The payload shape a review submits. It is a different schema rather than a variant: an analysis
 * result becomes records and a review result becomes an assessment.
 */
export const REVIEW_RESULT_SCHEMA = "babel.evaluation-result/1";

// ---------------------------------------------------------------------------- refusals

/** Why a submission was refused. The distinctions are what an operator acts on differently. */
export const REFUSALS = {
  /** The payload is not this contract: a field of the wrong type, an unknown field. */
  schema: "schema",
  /** A stage or role emitted material it has no authority for. */
  authority: "authority",
  /** A value outside a closed vocabulary the engine's own schema forbade. */
  vocabulary: "vocabulary",
  /** A claim with nothing behind it: an outcome with no evidence, a satisfied criterion with none. */
  support: "support",
  /** A submission that judged or reported nothing. */
  empty: "empty",
  /** §4.2's mandatory path skipped: a consolidation whose observations do not exist. */
  developmentPath: "development-path",
  /** A reference resolving to nothing this result or the brief named. */
  unknownReference: "unknown-reference",
  /** A review promoting work its own run authored. */
  selfBoost: "self-boost",
} as const;
export type RefusalCode = (typeof REFUSALS)[keyof typeof REFUSALS];

/** One refused submission. The message is what the model reads back so it can correct itself. */
export class ResultRefusal extends Error {
  readonly refusal: RefusalCode;

  constructor(refusal: RefusalCode, message: string) {
    super(message);
    this.name = "ResultRefusal";
    this.refusal = refusal;
  }
}

// ---------------------------------------------------------------------------- shared payloads

/** Where cited bytes live. Path and digest identify them and prove they have not changed. */
export const LocatorSchema = z.strictObject({
  path: z.string().min(1),
  line: z.number().int().min(0).default(0),
  byte_offset: z.number().int().min(0).default(0),
  digest: z.string().min(1),
});

/**
 * One provenance-bearing citation: a locator that recovers the original bytes plus the note
 * explaining what those bytes show. §4.3 makes evidence inseparable from its locator, so there is
 * no shape here that carries a note alone.
 */
export const EvidenceSchema = z.strictObject({
  locator: LocatorSchema,
  note: z.string().default(""),
});
export type Evidence = z.infer<typeof EvidenceSchema>;

const GRADINGS = ["low", "moderate", "high"] as const;
const TEMPORAL_STATUSES = ["current", "past", "unknown"] as const;
const CLASSIFICATIONS = ["private", "redaction-required", "public-safe"] as const;
const RECIPE_REF = z.strictObject({ id: z.string().min(1), version: z.number().int().min(0) });

/** The candidate in the model's own wording (§5.2 keeps that wording through sorting). */
export const HypothesisPayloadSchema = z.strictObject({
  statement: z.string().min(1),
  origin_cues: z.array(z.string()).default([]),
  provisional_labels: z.array(z.string()).default([]),
  /** Ordering signals in [0,1]. §5.2 confines them to ordering: they never gate existence. */
  novelty: z.number().min(0).max(1).default(0),
  priority: z.number().min(0).max(1).default(0),
  notes: z.string().default(""),
});

/**
 * One §4.3 claim. Evidence is never empty, and exactly one of `counter_evidence` /
 * `counter_evidence_absent` is set so an empty list can never be mistaken for an unasked question.
 */
export const ObservationPayloadSchema = z
  .strictObject({
    claim: z.string().min(1),
    category: z.string().default(""),
    confidence: z.enum(GRADINGS),
    impact: z.enum(GRADINGS),
    /** Never empty: §4.3 makes an observation the locator-backed kind of claim. */
    evidence: z.array(EvidenceSchema).min(1),
    counter_evidence: z.array(EvidenceSchema).default([]),
    counter_evidence_absent: z.boolean().default(false),
    temporal_status: z.enum(TEMPORAL_STATUSES).optional(),
  })
  .refine((p) => p.counter_evidence.length > 0 !== p.counter_evidence_absent, {
    message: "state either counter_evidence or counter_evidence_absent, never both and never neither",
  });

/** One §4.4 consolidation: what recurs, why it matters, and the scope it was consolidated across. */
export const FindingPayloadSchema = z
  .strictObject({
    title: z.string().min(1),
    pattern: z.string().min(1),
    significance: z.string().default(""),
    scope: z.array(z.string()).default([]),
    recurrence: z.number().int().min(0).default(0),
    counter_evidence: z.array(EvidenceSchema).default([]),
    counter_evidence_absent: z.boolean().default(false),
    temporal_status: z.enum(TEMPORAL_STATUSES).optional(),
  })
  .refine((p) => p.counter_evidence.length > 0 !== p.counter_evidence_absent, {
    message: "a finding states its counter-evidence position",
  });

/** One §4.5 suggested change. Nothing proposed here is applied; the operator rules on it. */
export const ProposalPayloadSchema = z.strictObject({
  title: z.string().min(1),
  problem: z.string().min(1),
  outcome: z.string().min(1),
  applicability: z.string().default(""),
  temporal_status: z.enum(TEMPORAL_STATUSES).optional(),
  supporting: z.array(EvidenceSchema).default([]),
  conflicting: z.array(EvidenceSchema).default([]),
  uncertainty: z.string().default(""),
  impact: z.enum(GRADINGS),
  estimated_scope: z.string().default(""),
  risks: z.array(z.string()).default([]),
  open_questions: z.array(z.string()).default([]),
  prerequisites: z.array(z.string()).default([]),
  verification_criteria: z.array(z.string()).default([]),
  classification: z.enum(CLASSIFICATIONS),
});

/*
  #87's proposed actions ("dispositions": draft-issue, propose-reality-fact, store-memory,
  ask-question, develop-further) are deliberately NOT offered here. The rewrite's store has no
  table for them and `JOB_OUTPUT_FILES` no file, so a field for them would be one a run could fill
  and nothing could land — and the `dispositions` table in schema.ts is the operator's rulings,
  which a run may not write at all. Bringing them back is a contract addition (an output file and
  a table), not a schema field.
 */

// ---------------------------------------------------------------------------- the exploration

const ObservationSchema = z.strictObject({
  ref: z.string().min(1),
  recipe: RECIPE_REF,
  claim: ObservationPayloadSchema,
});
export type Observation = z.infer<typeof ObservationSchema>;

const RemedySchema = z.strictObject({ ref: z.string().min(1), proposal: ProposalPayloadSchema });
export type Remedy = z.infer<typeof RemedySchema>;

/** The four grounds §5.4 admits. An objection naming none is refused rather than stored. */
const GROUNDS = ["evidence", "consequence", "missing-check", "alternative"] as const;

const ObjectionSchema = z.strictObject({
  ref: z.string().min(1),
  hypothesis: z.string().min(1),
  grounds: z.enum(GROUNDS),
  recipe: RECIPE_REF,
  claim: ObservationPayloadSchema.or(
    // A criticism resting on a consequence, a missing check or an alternative carries no locator,
    // so §4.3 forbids it from being an observation: it becomes a contradicting candidate instead.
    // The claim shape is the same minus the evidence requirement.
    z.strictObject({
      claim: z.string().min(1),
      category: z.string().default(""),
      confidence: z.enum(GRADINGS),
      impact: z.enum(GRADINGS),
      evidence: z.array(EvidenceSchema).max(0),
      counter_evidence: z.array(EvidenceSchema).default([]),
      counter_evidence_absent: z.boolean().default(true),
      temporal_status: z.enum(TEMPORAL_STATUSES).optional(),
    }),
  ),
});
export type Objection = z.infer<typeof ObjectionSchema>;

const ConsolidationSchema = z.strictObject({
  ref: z.string().min(1),
  observations: z.array(z.string().min(1)).min(1),
  finding: FindingPayloadSchema,
  proposal: ProposalPayloadSchema.optional(),
});
export type Consolidation = z.infer<typeof ConsolidationSchema>;

const DisposalSchema = z.strictObject({ hypothesis: z.string().min(1), reason: z.string().min(1) });
export type Disposal = z.infer<typeof DisposalSchema>;

/**
 * One thing the corpus could not settle and a person can. It authorizes nothing — it is a request
 * that somebody else authorize something — which is why a run may raise one at all (§4.8).
 */
const QuestionDraftSchema = z.strictObject({
  ref: z.string().min(1),
  subjects: z.array(z.string().min(1)).min(1),
  predicates: z.array(z.string()).default([]),
  hypothesis: z.string().default(""),
  prompt: z.string().min(1),
  why_asked: z.string().min(1),
});
export type QuestionDraft = z.infer<typeof QuestionDraftSchema>;

/** The full candidate shape; a stage's schema keeps only the parts its authority admits. */
const candidateShape = {
  ref: z.string().min(1),
  hypothesis: HypothesisPayloadSchema,
  observations: z.array(ObservationSchema).default([]),
  remedy: RemedySchema.optional(),
};
const CandidateSchema = z.strictObject(candidateShape);
export type Candidate = z.infer<typeof CandidateSchema>;

/** The stages of one run. Each is a unit of authority as much as of sequencing (§5.4). */
export const STAGES = ["explore", "challenge", "synthesize"] as const;
export type Stage = (typeof STAGES)[number];

/** What a stage's result may contain. The table is the enforcement, not a comment about it. */
interface StageAuthority {
  observations: boolean;
  consolidate: boolean;
  remedies: boolean;
  objections: boolean;
  schedule: boolean;
}

const STAGE_AUTHORITY: Record<Stage, StageAuthority> = {
  explore: { observations: true, consolidate: true, remedies: true, objections: false, schedule: true },
  challenge: { observations: false, consolidate: false, remedies: false, objections: true, schedule: false },
  synthesize: { observations: false, consolidate: true, remedies: true, objections: false, schedule: false },
};

/** What one exploration submitted, normalized so an absent list reads as the empty one. */
export interface ExploreResult {
  candidates: Candidate[];
  consolidations: Consolidation[];
  objections: Objection[];
  deferred: Disposal[];
  rejected: Disposal[];
  questions: QuestionDraft[];
}

function exploreSchema(stage: Stage): z.ZodType {
  const authority = STAGE_AUTHORITY[stage];
  const candidate = z.strictObject({
    ref: candidateShape.ref,
    hypothesis: candidateShape.hypothesis,
    ...(authority.observations ? { observations: candidateShape.observations } : {}),
    ...(authority.remedies ? { remedy: candidateShape.remedy } : {}),
  });
  return z.strictObject({
    candidates: z.array(candidate).default([]),
    ...(authority.consolidate ? { consolidations: z.array(ConsolidationSchema).default([]) } : {}),
    ...(authority.objections ? { objections: z.array(ObjectionSchema).default([]) } : {}),
    ...(authority.schedule
      ? { deferred: z.array(DisposalSchema).default([]), rejected: z.array(DisposalSchema).default([]) }
      : {}),
    questions: z.array(QuestionDraftSchema).default([]),
  });
}

const exploreSchemas: Record<Stage, z.ZodType> = {
  explore: exploreSchema("explore"),
  challenge: exploreSchema("challenge"),
  synthesize: exploreSchema("synthesize"),
};

/** The JSON Schema the submit tool is registered with for one stage. */
export function exploreJsonSchema(stage: Stage): unknown {
  return z.toJSONSchema(exploreSchemas[stage], { io: "input", target: "draft-2020-12" });
}

/**
 * Decodes and structurally validates one exploration submission. It checks shape, provenance and
 * the development path within the result, and nothing about whether a claim is true.
 */
export function parseExploreResult(stage: Stage, payload: unknown): ExploreResult {
  const parsed = exploreSchemas[stage].safeParse(payload);
  if (!parsed.success) {
    throw new ResultRefusal(REFUSALS.schema, `the ${stage} result does not match its schema: ${issues(parsed.error)}`);
  }
  const shaped = ExploreResultShape.parse(parsed.data);
  const result: ExploreResult = {
    candidates: shaped.candidates,
    consolidations: shaped.consolidations,
    objections: shaped.objections,
    deferred: shaped.deferred,
    rejected: shaped.rejected,
    questions: shaped.questions,
  };

  const refs: Record<string, "hypothesis" | "observation" | "finding" | "proposal"> = {};
  for (const candidate of result.candidates) {
    if (refs[candidate.ref] !== undefined) {
      throw new ResultRefusal(REFUSALS.schema, `ref ${JSON.stringify(candidate.ref)} is used twice`);
    }
    refs[candidate.ref] = "hypothesis";
    for (const observation of candidate.observations) {
      if (refs[observation.ref] !== undefined) {
        throw new ResultRefusal(REFUSALS.schema, `ref ${JSON.stringify(observation.ref)} is used twice`);
      }
      refs[observation.ref] = "observation";
    }
    if (candidate.remedy !== undefined) {
      if (refs[candidate.remedy.ref] !== undefined) {
        throw new ResultRefusal(REFUSALS.schema, `ref ${JSON.stringify(candidate.remedy.ref)} is used twice`);
      }
      refs[candidate.remedy.ref] = "proposal";
    }
  }
  for (const consolidation of result.consolidations) {
    if (refs[consolidation.ref] !== undefined) {
      throw new ResultRefusal(REFUSALS.schema, `ref ${JSON.stringify(consolidation.ref)} is used twice`);
    }
    refs[consolidation.ref] = "finding";
  }
  // §4.2's path is mandatory: a consolidation rests on locator-backed observations that exist.
  // A name that is neither a ref from this result nor a durable identifier the brief listed is a
  // refusal, never a repair — the repair would be Babel inventing the evidence step.
  for (const consolidation of result.consolidations) {
    for (const name of consolidation.observations) {
      const within = refs[name];
      if (within === undefined) {
        if (!/^obs_[0-9a-f]{8,64}$/.test(name)) {
          throw new ResultRefusal(
            REFUSALS.developmentPath,
            `consolidation ${JSON.stringify(consolidation.ref)} rests on ${JSON.stringify(name)}, which is neither a ref in this result nor an observation identifier`,
          );
        }
        continue;
      }
      if (within !== "observation") {
        throw new ResultRefusal(
          REFUSALS.developmentPath,
          `consolidation ${JSON.stringify(consolidation.ref)} rests on ${JSON.stringify(name)}, which is a ${within} rather than an observation`,
        );
      }
    }
  }
  for (const objection of result.objections) {
    const target = refs[objection.hypothesis];
    if (target === undefined && !/^hyp_[0-9a-f]{8,64}$/.test(objection.hypothesis)) {
      throw new ResultRefusal(
        REFUSALS.unknownReference,
        `objection ${JSON.stringify(objection.ref)} attacks ${JSON.stringify(objection.hypothesis)}, which this result did not emit and no brief listed`,
      );
    }
    if (target !== undefined && target !== "hypothesis") {
      throw new ResultRefusal(
        REFUSALS.unknownReference,
        `objection ${JSON.stringify(objection.ref)} attacks a ${target}; §5.4 criticism names a hypothesis`,
      );
    }
    if (objection.grounds === "evidence" && objection.claim.evidence.length === 0) {
      throw new ResultRefusal(
        REFUSALS.support,
        `objection ${JSON.stringify(objection.ref)} rests on evidence and cites none`,
      );
    }
  }
  for (const disposal of [...result.deferred, ...result.rejected]) {
    if (refs[disposal.hypothesis] === undefined && !/^hyp_[0-9a-f]{8,64}$/.test(disposal.hypothesis)) {
      throw new ResultRefusal(
        REFUSALS.unknownReference,
        `${JSON.stringify(disposal.hypothesis)} was set down and is not a candidate this result or the brief named`,
      );
    }
  }
  for (const question of result.questions) {
    if (question.hypothesis !== "" && refs[question.hypothesis] === undefined && !/^hyp_/.test(question.hypothesis)) {
      throw new ResultRefusal(
        REFUSALS.unknownReference,
        `question ${JSON.stringify(question.ref)} blocks ${JSON.stringify(question.hypothesis)}, which is not a candidate it named`,
      );
    }
  }
  return result;
}

/** The normalizer: every list present, so a consumer never asks whether a stage could emit one. */
const ExploreResultShape = z.looseObject({
  candidates: z.array(CandidateSchema).default([]),
  consolidations: z.array(ConsolidationSchema).default([]),
  objections: z.array(ObjectionSchema).default([]),
  deferred: z.array(DisposalSchema).default([]),
  rejected: z.array(DisposalSchema).default([]),
  questions: z.array(QuestionDraftSchema).default([]),
});

// ---------------------------------------------------------------------------- the review

export type Role = (typeof ROLES)[number];

/** The observed-outcome vocabulary of §4.12's full lifecycle. */
const OUTCOMES = ["implemented", "verified", "partial", "contradicted", "unverifiable"] as const;

/** The contribution vocabulary: optional material beside a vote, or the whole of an assessment. */
const CONTRIBUTION_KINDS = [
  "comment",
  "argument",
  "objection",
  "evidence",
  "refinement",
  "comparison",
] as const;

const SubjectSchema = z.strictObject({ kind: z.string().min(1), id: z.string().min(1) });

const ContributionSchema = z.strictObject({
  kind: z.enum(CONTRIBUTION_KINDS),
  text: z.string().default(""),
  evidence: z.array(EvidenceSchema).default([]),
  alternatives: z.array(SubjectSchema).default([]),
  preferred: SubjectSchema.optional(),
  would_change: z.string().default(""),
});
export type Contribution = z.infer<typeof ContributionSchema>;

const CriterionResultSchema = z.strictObject({
  criterion_id: z.string().min(1),
  satisfied: z.boolean(),
  evidence: z.array(EvidenceSchema).default([]),
  uncertainty: z.string().default(""),
});
export type CriterionResult = z.infer<typeof CriterionResultSchema>;

/** The entity kinds §4.8 admits. A topic proposal creating something names one of them. */
const ENTITY_KINDS = [
  "environment",
  "machine",
  "organization",
  "project",
  "provider",
  "repository",
  "service",
  "subject",
] as const;

/** The predicates the ledger admits for a promoted fact. */
const FACT_PREDICATES = [
  "lifecycle",
  "ownership",
  "analysis-policy",
  "service-placement",
  "deployment-state",
  "local-path",
  "repository-remote",
] as const;

/** §4.13's four changes to a topic. They are one output kind with four operations. */
export const TOPIC_OPERATIONS = ["create", "split", "merge", "retire"] as const;
export type TopicOperation = (typeof TOPIC_OPERATIONS)[number];

/** How many existing topics each operation acts on. A merge of one is not an act the ledger has. */
const TOPIC_TARGETS: Record<TopicOperation, number> = { create: 0, split: 1, merge: 2, retire: 1 };

const FiledUnderSchema = z.strictObject({
  entity: z.string().min(1),
  /** Required: a link nobody can argue with is a link nobody can correct. */
  rationale: z.string().min(1),
});
export type FiledUnder = z.infer<typeof FiledUnderSchema>;

const TopicProposalSchema = z.strictObject({
  operation: z.enum(TOPIC_OPERATIONS),
  targets: z.array(z.string().min(1)).default([]),
  ask_id: z.string().default(""),
  name: z.string().default(""),
  kind: z.string().default(""),
  identity: z.string().default(""),
  aliases: z.array(z.string()).default([]),
  remote: z.string().default(""),
  paths: z.array(z.string()).default([]),
  definition: z.string().default(""),
  reasoning: z.string().min(1),
  considered: z.array(z.string()).default([]),
});
export type TopicProposal = z.infer<typeof TopicProposalSchema>;

const NoTopicSchema = z.strictObject({ reason: z.string().min(1) });
const NoChangeSchema = z.strictObject({ ask_id: z.string().min(1), reason: z.string().min(1) });
export type NoChange = z.infer<typeof NoChangeSchema>;

const FindingDraftSchema = z.strictObject({
  title: z.string().min(1),
  pattern: z.string().min(1),
  why_it_matters: z.string().min(1),
  scope: z.array(z.string()).default([]),
});

const BacklogConsolidationSchema = z.strictObject({
  hypotheses: z.array(z.string().min(1)).min(1),
  finding: FindingDraftSchema,
});
export type BacklogConsolidation = z.infer<typeof BacklogConsolidationSchema>;

const SupersessionSchema = z.strictObject({ by: z.string().min(1), reason: z.string().min(1) });
const RetirementSchema = z.strictObject({ reason: z.string().min(1) });
const PromotionSchema = z.strictObject({
  observation: z.string().min(1),
  entity: z.string().min(1),
  predicate: z.enum(FACT_PREDICATES),
  value: z.string().min(1),
  reason: z.string().min(1),
});
export type Promotion = z.infer<typeof PromotionSchema>;
const KeptSchema = z.strictObject({ reason: z.string().min(1) });

/** The full review shape; a role's schema keeps only what its authority admits. */
const reviewShape = {
  vote: z.enum(VOTES).optional(),
  contributions: z.array(ContributionSchema).default([]),
  outcome: z.enum(OUTCOMES).optional(),
  results: z.array(CriterionResultSchema).default([]),
  environment: z.string().default(""),
  as_of: z.string().default(""),
  uncertainty: z.string().default(""),
  skip: z.string().default(""),
  filing: FiledUnderSchema.optional(),
  topic: TopicProposalSchema.optional(),
  no_topic: NoTopicSchema.optional(),
  no_change: NoChangeSchema.optional(),
  consolidate: BacklogConsolidationSchema.optional(),
  supersede: SupersessionSchema.optional(),
  retire: RetirementSchema.optional(),
  promote: PromotionSchema.optional(),
  keep: KeptSchema.optional(),
};

/** What one role's result may contain. */
interface RoleAuthority {
  /** The reception vocabulary. Reception alone: a comparison minting a global vote would turn
   * "B is better here" into an endorsement of B everywhere. */
  vote: boolean;
  /** An observed-outcome claim, with evidence required behind it. */
  outcome: boolean;
  /** Per-criterion results: what an evidence check and an outcome verification produce. */
  criteria: boolean;
  /** Contributions naming alternatives and a preference. Comparison only. */
  alternatives: boolean;
  /** §4.13's filing pass: four answers and nothing else — not even a contribution. */
  filing: boolean;
  /** §4.13's backlog pass: five answers and nothing else. */
  backlog: boolean;
}

const ROLE_AUTHORITY: Record<Role, RoleAuthority> = {
  reception: { vote: true, outcome: false, criteria: false, alternatives: false, filing: false, backlog: false },
  evidence: { vote: false, outcome: false, criteria: true, alternatives: false, filing: false, backlog: false },
  challenge: { vote: false, outcome: false, criteria: false, alternatives: false, filing: false, backlog: false },
  comparison: { vote: false, outcome: false, criteria: false, alternatives: true, filing: false, backlog: false },
  outcome: { vote: false, outcome: true, criteria: true, alternatives: false, filing: false, backlog: false },
  relevance: { vote: false, outcome: false, criteria: false, alternatives: false, filing: false, backlog: false },
  filing: { vote: false, outcome: false, criteria: false, alternatives: false, filing: true, backlog: false },
  backlog: { vote: false, outcome: false, criteria: false, alternatives: false, filing: false, backlog: true },
};

/** One accepted review submission, normalized. Absence is a statement everywhere in it. */
export interface ReviewResult {
  vote: string;
  contributions: Contribution[];
  outcome: string;
  results: CriterionResult[];
  environment: string;
  asOf: string;
  uncertainty: string;
  skip: string;
  filing: FiledUnder | null;
  topic: TopicProposal | null;
  noTopic: { reason: string } | null;
  noChange: NoChange | null;
  consolidate: BacklogConsolidation | null;
  supersede: { by: string; reason: string } | null;
  retire: { reason: string } | null;
  promote: Promotion | null;
  keep: { reason: string } | null;
}

function reviewSchema(role: Role): z.ZodType {
  const authority = ROLE_AUTHORITY[role];
  const work = authority.filing || authority.backlog;
  return z.strictObject({
    ...(authority.vote ? { vote: reviewShape.vote } : {}),
    // The whole result of a filing or backlog pass is which of its answers it reached. A
    // contribution would invite it to review the record it was drawn to name or to settle.
    ...(work ? {} : { contributions: reviewShape.contributions }),
    ...(authority.outcome ? { outcome: reviewShape.outcome } : {}),
    ...(authority.criteria
      ? { results: reviewShape.results, environment: reviewShape.environment, as_of: reviewShape.as_of }
      : {}),
    uncertainty: reviewShape.uncertainty,
    skip: reviewShape.skip,
    ...(authority.filing
      ? {
          filing: reviewShape.filing,
          topic: reviewShape.topic,
          no_topic: reviewShape.no_topic,
          no_change: reviewShape.no_change,
        }
      : {}),
    ...(authority.backlog
      ? {
          consolidate: reviewShape.consolidate,
          supersede: reviewShape.supersede,
          retire: reviewShape.retire,
          promote: reviewShape.promote,
          keep: reviewShape.keep,
        }
      : {}),
  });
}

const reviewSchemas: Record<Role, z.ZodType> = {
  reception: reviewSchema("reception"),
  evidence: reviewSchema("evidence"),
  challenge: reviewSchema("challenge"),
  comparison: reviewSchema("comparison"),
  outcome: reviewSchema("outcome"),
  relevance: reviewSchema("relevance"),
  filing: reviewSchema("filing"),
  backlog: reviewSchema("backlog"),
};

/** The JSON Schema the submit tool is registered with for one role. */
export function reviewJsonSchema(role: Role): unknown {
  return z.toJSONSchema(reviewSchemas[role], { io: "input", target: "draft-2020-12" });
}

/** The normalizer for a review submission, on the exploration result's terms. */
const ReviewResultShape = z.looseObject({
  vote: z.string().default(""),
  contributions: z.array(ContributionSchema).default([]),
  outcome: z.string().default(""),
  results: z.array(CriterionResultSchema).default([]),
  environment: z.string().default(""),
  as_of: z.string().default(""),
  uncertainty: z.string().default(""),
  skip: z.string().default(""),
  filing: FiledUnderSchema.nullish(),
  topic: TopicProposalSchema.nullish(),
  no_topic: NoTopicSchema.nullish(),
  no_change: NoChangeSchema.nullish(),
  consolidate: BacklogConsolidationSchema.nullish(),
  supersede: SupersessionSchema.nullish(),
  retire: RetirementSchema.nullish(),
  promote: PromotionSchema.nullish(),
  keep: KeptSchema.nullish(),
});

/** What this run authored among the records it was handed: what the self-boost refusal reads. */
export interface ReviewSelf {
  /** The record under review came out of this run. */
  target: boolean;
  /** The alternative subject identities this run authored. */
  subjects: Readonly<Record<string, true>>;
}

/**
 * Decodes and validates one submission against the role's authority. It checks shape, vocabulary
 * and support, and nothing about whether the judgement is correct: Babel validates structure and
 * provenance, and a reception vote has no truth condition to check.
 */
export function parseReviewResult(role: Role, payload: unknown, self?: ReviewSelf): ReviewResult {
  const authority = ROLE_AUTHORITY[role];
  const parsed = reviewSchemas[role].safeParse(payload);
  if (!parsed.success) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `the ${role} result does not match its schema: ${issues(parsed.error)}`,
    );
  }
  const shaped = ReviewResultShape.parse(parsed.data);
  const result: ReviewResult = {
    vote: shaped.vote,
    contributions: shaped.contributions,
    outcome: shaped.outcome,
    results: shaped.results,
    environment: shaped.environment.trim(),
    asOf: shaped.as_of.trim(),
    uncertainty: shaped.uncertainty.trim(),
    skip: shaped.skip.trim(),
    filing: shaped.filing ?? null,
    topic: shaped.topic ?? null,
    noTopic: shaped.no_topic ?? null,
    noChange: shaped.no_change ?? null,
    consolidate: shaped.consolidate ?? null,
    supersede: shaped.supersede ?? null,
    retire: shaped.retire ?? null,
    promote: shaped.promote ?? null,
    keep: shaped.keep ?? null,
  };

  const filed = result.filing !== null || result.topic !== null || result.noTopic !== null || result.noChange !== null;
  const settled =
    result.consolidate !== null ||
    result.supersede !== null ||
    result.retire !== null ||
    result.promote !== null ||
    result.keep !== null;
  if (
    result.skip !== "" &&
    (result.vote !== "" ||
      result.outcome !== "" ||
      result.contributions.length > 0 ||
      result.results.length > 0 ||
      result.uncertainty !== "" ||
      filed ||
      settled)
  ) {
    throw new ResultRefusal(REFUSALS.schema, "a skip cannot also state an assessment");
  }

  if (authority.filing || authority.backlog) {
    if (result.skip !== "") return result;
    if (authority.filing) validateFiling(result);
    else validateBacklog(result);
    return result;
  }

  for (const [index, contribution] of result.contributions.entries()) {
    const compares = contribution.kind === "comparison";
    if (compares && !authority.alternatives) {
      throw new ResultRefusal(REFUSALS.authority, `the ${role} role may not compare alternatives`);
    }
    if (!compares && (contribution.alternatives.length > 0 || contribution.preferred !== undefined)) {
      throw new ResultRefusal(
        REFUSALS.schema,
        `contribution ${index + 1} is a ${contribution.kind} and may not name alternatives`,
      );
    }
    if (contribution.kind === "evidence" && contribution.evidence.length === 0) {
      throw new ResultRefusal(REFUSALS.support, `contribution ${index + 1} offers evidence and cites none`);
    }
    if (compares && contribution.alternatives.length < 2) {
      throw new ResultRefusal(
        REFUSALS.schema,
        `contribution ${index + 1} compares ${contribution.alternatives.length} alternatives; a comparison needs at least two`,
      );
    }
    const preferred = contribution.preferred;
    if (compares && preferred !== undefined && !contribution.alternatives.some((alt) => alt.id === preferred.id)) {
      throw new ResultRefusal(REFUSALS.schema, `contribution ${index + 1} prefers an alternative it did not compare`);
    }
    if (!compares && contribution.text === "" && contribution.evidence.length === 0) {
      throw new ResultRefusal(REFUSALS.empty, `contribution ${index + 1} carries neither text nor evidence`);
    }
    if (preferred !== undefined && self?.subjects[preferred.id] === true) {
      throw new ResultRefusal(
        REFUSALS.selfBoost,
        `this run authored ${preferred.id}; a review may not prefer its own alternative`,
      );
    }
  }

  // A run arguing against what it produced is the honest direction, so only endorsement is
  // refused: independence is the whole value of the judgement.
  if (self?.target === true && (result.vote === "support" || result.outcome === "implemented" || result.outcome === "verified")) {
    throw new ResultRefusal(REFUSALS.selfBoost, "this run authored the record under review");
  }
  if (result.outcome !== "" && result.outcome !== "unverifiable" && reviewEvidence(result).length === 0) {
    throw new ResultRefusal(REFUSALS.support, `an observed outcome of ${result.outcome} needs evidence`);
  }
  if ((result.outcome !== "" || result.results.length > 0) && (result.environment === "" || result.asOf === "")) {
    throw new ResultRefusal(
      REFUSALS.support,
      "criterion and outcome results need an explicit environment and as-of time",
    );
  }
  if (result.outcome === "unverifiable" && result.uncertainty === "") {
    throw new ResultRefusal(REFUSALS.support, "an unverifiable outcome must name what could not be checked");
  }
  for (const criterion of result.results) {
    if (criterion.satisfied && criterion.evidence.length === 0) {
      throw new ResultRefusal(
        REFUSALS.support,
        `criterion ${criterion.criterion_id} is reported satisfied with no evidence`,
      );
    }
  }
  if (
    result.vote === "" &&
    result.outcome === "" &&
    result.skip === "" &&
    result.contributions.length === 0 &&
    result.results.length === 0
  ) {
    throw new ResultRefusal(
      REFUSALS.empty,
      "the review submitted no vote, contribution, criterion result, outcome or skip reason",
    );
  }
  return result;
}

/** Every citation a submission carries: what an observed outcome is checked for support against. */
function reviewEvidence(result: ReviewResult): Evidence[] {
  const out: Evidence[] = [];
  for (const contribution of result.contributions) out.push(...contribution.evidence);
  for (const criterion of result.results) out.push(...criterion.evidence);
  return out;
}

/**
 * Checks one filing result against the four shapes, and nothing about whether the answer is
 * right. Exactly one answer, because the four are alternatives rather than fields: a pass that
 * filed a record and proposed a topic for it in the same breath has not decided what it is about.
 */
function validateFiling(result: ReviewResult): void {
  const answers = [result.filing, result.topic, result.noTopic, result.noChange].filter((a) => a !== null).length;
  if (answers === 0) {
    throw new ResultRefusal(
      REFUSALS.empty,
      "a filing states one of `filing`, `topic`, `no_topic` or `no_change`",
    );
  }
  if (answers > 1) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `a filing states one of \`filing\`, \`topic\`, \`no_topic\` or \`no_change\`, not ${answers} of them`,
    );
  }
  if (result.topic !== null) validateTopicProposal(result.topic);
}

/**
 * Refuses a topic proposal the operator could not act on. The binding requirement is the
 * load-bearing one: §4.13 admits a repository, a machine, a service or a concept as a topic and
 * requires each to be bound to something real, and a proposal with a name and no binding is a
 * folder — the one thing a topic is not.
 */
function validateTopicProposal(topic: TopicProposal): void {
  const wanted = TOPIC_TARGETS[topic.operation];
  if (topic.targets.length !== wanted) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `a ${topic.operation} names ${wanted} existing topics, not ${topic.targets.length}`,
    );
  }
  const creates = topic.operation === "create" || topic.operation === "split";
  if (!creates) {
    if (topic.name !== "" || topic.kind !== "" || topic.identity !== "") {
      throw new ResultRefusal(
        REFUSALS.schema,
        `a ${topic.operation} creates no entity, so it carries no name, kind or identity`,
      );
    }
    return;
  }
  if (topic.name === "") {
    throw new ResultRefusal(REFUSALS.schema, `a ${topic.operation} needs the name of the topic it would create`);
  }
  if (!ENTITY_KINDS.includes(topic.kind as (typeof ENTITY_KINDS)[number])) {
    throw new ResultRefusal(
      REFUSALS.vocabulary,
      `${JSON.stringify(topic.kind)} is not an entity kind the ledger admits (${ENTITY_KINDS.join(", ")})`,
    );
  }
  if (topic.identity === "") {
    throw new ResultRefusal(REFUSALS.schema, `a ${topic.operation} needs the identity that deduplicates it`);
  }
  if (topic.remote === "" && topic.paths.length === 0 && topic.definition === "") {
    throw new ResultRefusal(
      REFUSALS.schema,
      "a topic proposal binds to something real — a repository remote, a path, or a one-sentence definition — because a topic with no binding is a folder",
    );
  }
}

/** Checks one backlog result against the five shapes. Exactly one, on filing's reasoning. */
function validateBacklog(result: ReviewResult): void {
  const answers = [result.consolidate, result.supersede, result.retire, result.promote, result.keep].filter(
    (a) => a !== null,
  ).length;
  if (answers === 0) {
    throw new ResultRefusal(
      REFUSALS.empty,
      "a backlog pass states one of `consolidate`, `supersede`, `retire`, `promote` or `keep`",
    );
  }
  if (answers > 1) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `a backlog pass states one of its five answers, not ${answers} of them`,
    );
  }
}

/** Renders a zod failure as the sentence a model can act on. */
function issues(error: z.ZodError): string {
  return error.issues
    .map((issue) => {
      const at = issue.path.length === 0 ? "" : `${issue.path.join(".")}: `;
      return `${at}${issue.message}`;
    })
    .join("; ");
}
