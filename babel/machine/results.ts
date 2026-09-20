/*
  THE RESULT CONTRACTS: what a stage of an exploration and what a review role may submit, as one
  declaration each. Ported from v0.4.0:internal/explore/{result.go,schema.go,stages.go,review.go,
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

  BOTH HALVES VALIDATE FROM HERE. The machine half runs `parseReviewResult` over what the model
  submitted; the store half runs `acceptReviewResult` over the normalized result an `assessments`
  row carries, through `store/acts.ts`. They are one function over one field table, because the
  Go tree's worst evaluation bug was three copies of one rule: the review contract required an
  environment on criterion results, the store refused any environment without an outcome, and a
  results-only assessment counted as empty — so an evidence review was paid for and then refused
  at submit (docs/postmortem-2026-09-13-drain.md, F8). The hub's review settlement asks the same
  table a third way — `contributionRefusal`, one contribution at a time, so a refused one can be
  dropped by name instead of taking the review with it (#305) — and it is the same rules, asked
  differently, for the same reason. Nothing here touches a process, a file or a database, which
  is what lets the hub's half import it.
*/

import { z } from "zod";
import type { MaterialEntry, ROLES, Stage } from "../contract.ts";
import {
  MAX_CITATION_QUOTE,
  NextActionSchema,
  ObjectionGroundSchema,
  SUBMISSION_KEPT_FLOOR,
  VOTES,
  normalizeRemote,
} from "../contract.ts";
// THE SCOPE HALF OF A CITATION IS SPELLED ONCE, in `server/engine/citations.ts`, and this file
// asks it rather than restating it (#422, #231). The module is pure — it reads no file, no
// database and no job, the bytes of the quote half arrive as a function its own caller supplies
// — so asking it here costs this contract nothing it did not already have, and the alternative
// is the second copy of a security boundary that #263 was opened about.
import { unservedCitation } from "../server/engine/citations.ts";

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

/**
 * A REFUSED SUBMISSION AS A RECEIPT CARRIES IT, and the same sentence read back.
 *
 * `evaluate` writes the refusal of the last submission into the receipt's `reason`, because an
 * operator reading a failed run needs to know a `schema` refusal from a `support` one — they are
 * different remedies. The hub then reads the code back off that sentence: a receipt whose reason
 * names one of these is PROOF THE MODEL ANSWERED, since only a submission can be refused, so the
 * conductor counts that run as spend rather than as a free failure (#265, post-mortem F16/F8).
 *
 * The two halves live here together so the format cannot drift apart: a writer that changed its
 * separator and a reader that did not would silently turn every paid refusal back into a
 * failure. The code is matched against the closed vocabulary above, which is what keeps an
 * engine failure written in the same shape (`launch: the binary is absent`) from reading as one.
 */
export function refusalReason(refusal: ResultRefusal): string {
  return `${refusal.refusal}: ${refusal.message}`;
}

export function refusalCode(reason: string): RefusalCode | null {
  const at = reason.indexOf(":");
  if (at <= 0) return null;
  const head = reason.slice(0, at);
  for (const code of Object.values(REFUSALS)) if (code === head) return code;
  return null;
}

// ---------------------------------------------------------------------------- shared payloads

/**
 * Where cited bytes live, and what they say. Path and digest identify them and prove they have
 * not changed; `quote` is the span of the record itself that supports the claim, and it is the
 * only field here anybody can check against the bytes rather than against an index (#348).
 *
 * It is OPTIONAL and it is not a refusal to omit it. A citation without a quote is recorded as
 * `unquoted` and stands: the corpus that exists was produced under a contract that never asked
 * for one, and a rule that refused every claim written before it would delete history rather
 * than improve it. What the field buys is that a quote, once written, is checkable — and a run
 * that stops writing them becomes visible in the receipt's own tally instead of invisible.
 */
export const LocatorSchema = z.strictObject({
  path: z.string().min(1),
  line: z.number().int().min(0).default(0),
  byte_offset: z.number().int().min(0).default(0),
  digest: z.string().min(1),
  quote: z.string().max(MAX_CITATION_QUOTE).default(""),
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

/**
 * THE REPOSITORY ONE CLAIM IS ABOUT, as the evidence recorded it (#183).
 *
 * 42.7% of this deployment's records cannot say which codebase they concern. Half of that is a
 * join nobody walked — Babel's own catalog knows the repository a cited session worked in — and
 * the other half is this: a conversation about a project the operator was not standing in
 * leaves no workspace to probe, and only the transcript says which project it was. A run is the
 * one reader of those bytes, so it is the one thing that can state them.
 *
 * NOTHING HERE IS A PROBE. The commit is what the evidence recorded — a `session_meta` git
 * block, a `git rev-parse` the conversation ran, a sha the operator pasted — and never what the
 * checkout happens to be at now: a run reads a repository as it was, and the current HEAD is
 * not evidence about the past. A field the transcript does not support is left empty; a guess
 * about a codebase is worse than a record that admits it does not know which one.
 *
 * It sits on the OBSERVATION and on no other kind, because §4.3 makes an observation the
 * locator-backed claim: a hypothesis is a guess, a finding consolidates observations and a
 * proposal addresses one of those, so all three inherit the repository through what they rest
 * on rather than restating it — and a restatement is a second place for it to be wrong.
 */
export const RepositoryClaimSchema = z.strictObject({
  /**
   * The repository as `host/owner/repo`. Any spelling git accepts is taken and canonicalized,
   * because the same repository is written four ways and four strings would read as four
   * projects; a remote naming a local path normalizes to nothing and reads as absent.
   */
  remote: z
    .string()
    .max(400)
    .default("")
    .transform((url) => normalizeRemote(url))
    .describe(
      "The repository this claim is about, as the cited evidence gives it — a remote URL or " +
        "host/owner/repo. Leave it out unless the transcript itself names the repository.",
    ),
  /** The commit the evidence recorded this repository at: 7 to 40 hex, or nothing. */
  commit: z
    .string()
    .trim()
    .max(40)
    .toLowerCase()
    .regex(/^(?:[0-9a-f]{7,40})?$/u, "a commit is 7 to 40 hexadecimal characters, or is omitted")
    .default("")
    .describe(
      "The commit the cited evidence records the repository at, 7 to 40 hexadecimal " +
        "characters. Only a sha the transcript itself states; never one inferred, and never " +
        "a checkout's present HEAD.",
    ),
  /**
   * The issue or pull request the cited evidence names, as a whole URL.
   *
   * A whole URL is required because it carries its own owner and repository: `#312` and
   * `Closes #4` name a number in whatever project the reader assumes, and a reference resolved
   * against the wrong repository is a worse answer than none. The reader shows it only where the
   * host, owner and repository match the record's own, so a link never travels to another
   * project on a model's word.
   */
  reference: z
    .string()
    .trim()
    .max(300)
    .regex(
      /^(?:https:\/\/[a-z0-9.-]+\/[^\s/]+\/[^\s/]+\/(?:issues|pull)\/[0-9]{1,9})?$/u,
      "an issue or pull request reference is its whole https URL, or is omitted",
    )
    .default("")
    .describe(
      "The issue or pull request the cited evidence names, as its whole https URL. A bare " +
        "number names nothing, so omit it unless the evidence gives the URL.",
    ),
});

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
    /**
     * Which codebase this claim is about, when the cited evidence says
     * ({@link RepositoryClaimSchema}). Optional because a great deal of the corpus is about
     * work that names no repository, and a required field would be answered by invention.
     */
    repository: RepositoryClaimSchema.optional(),
  })
  .refine((p) => p.counter_evidence.length > 0 !== p.counter_evidence_absent, {
    message:
      "state either counter_evidence or counter_evidence_absent, never both and never neither",
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

/**
 * ONE TYPED NEXT ACTION A RUN PROPOSES ON A RECORD (#340).
 *
 * This field was left out for a reason that has stopped being true. The store had no table for a
 * proposed action and `JOB_OUTPUT_FILES` no file, so a field here would have been one a run could
 * fill and nothing could land — worse than its absence, because the run would have spent tokens
 * on it. `next_actions` and `next_action_rulings` now exist (`store/schema.ts`) and
 * `JOB_OUTPUT_FILES.nextActions` carries the rows, so the field lands where it says it does.
 *
 * MEANWHILE THE PROMPT WAS ALREADY ASKING FOR IT. `server/engine/prompts.ts` instructed every
 * stage to propose "dispositions" from this very vocabulary, and these schemas are strict — so a
 * model that obeyed had its WHOLE answer refused for an unknown field, and the stage that obeyed
 * best lost the most. The instruction and the contract are one subject and are spelled against
 * each other here.
 *
 * `record` names what the action is about, the same way every other cross-reference in a result
 * does: a ref this result declared, or a durable identifier the brief listed. `kind` is
 * `contract.ts`'s closed vocabulary, which is what lets a proposal be rendered as a choice
 * rather than read as prose.
 *
 * `workspace` is the local checkout a `draft-issue` is about, and the two rules around it are the
 * retired product's (`v0.4.0:internal/disposition`: `ErrAnchorRequired`, and a payload refused for
 * carrying an anchor on any other kind). An issue draft naming no repository is a change proposed
 * to nothing, and a workspace on a `store-memory` is a field that kind has no authority for.
 */
const NextActionDraftSchema = z.strictObject({
  record: z.string().min(1),
  kind: NextActionSchema,
  summary: z.string().min(1),
  rationale: z.string().default(""),
  workspace: z.string().default(""),
});
export type NextActionDraft = z.infer<typeof NextActionDraftSchema>;

/** A record identifier the ledger already holds, as `RecordIdSchema` spells the four families. */
const DURABLE_RECORD = /^(hyp|obs|fnd|pro)_[0-9a-f]{8,64}$/u;

// ---------------------------------------------------------------------------- the exploration

const ObservationSchema = z.strictObject({
  ref: z.string().min(1),
  recipe: RECIPE_REF,
  claim: ObservationPayloadSchema,
});
export type Observation = z.infer<typeof ObservationSchema>;

const RemedySchema = z.strictObject({ ref: z.string().min(1), proposal: ProposalPayloadSchema });
export type Remedy = z.infer<typeof RemedySchema>;

const ObjectionSchema = z.strictObject({
  ref: z.string().min(1),
  hypothesis: z.string().min(1),
  grounds: ObjectionGroundSchema,
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

/** What a stage's result may contain. The table is the enforcement, not a comment about it. */
interface StageAuthority {
  observations: boolean;
  consolidate: boolean;
  remedies: boolean;
  objections: boolean;
  schedule: boolean;
  /**
   * Whether this stage may propose what to do next about a record. A challenger may not: its
   * authority is to criticize a claim, and directing the operator's work off the back of a
   * criticism is a second job nobody asked it to do.
   */
  nextActions: boolean;
}

const STAGE_AUTHORITY: Record<Stage, StageAuthority> = {
  explore: {
    observations: true,
    consolidate: true,
    remedies: true,
    objections: false,
    schedule: true,
    nextActions: true,
  },
  challenge: {
    observations: false,
    consolidate: false,
    remedies: false,
    objections: true,
    schedule: false,
    nextActions: false,
  },
  synthesize: {
    observations: false,
    consolidate: true,
    remedies: true,
    objections: false,
    schedule: false,
    nextActions: true,
  },
};

/** What one exploration submitted, normalized so an absent list reads as the empty one. */
export interface ExploreResult {
  candidates: Candidate[];
  consolidations: Consolidation[];
  objections: Objection[];
  deferred: Disposal[];
  rejected: Disposal[];
  questions: QuestionDraft[];
  next_actions: NextActionDraft[];
}

/**
 * THE SEVEN LISTS A SUBMISSION CARRIES, EACH WITH ITS ITEM SCHEMA AND ITS AUTHORITY.
 *
 * One table, read two ways. {@link exploreSchema} composes it into the strict per-stage document
 * the prompt prints, and {@link exploreSubmission} walks it item by item to decide what one
 * submission keeps — so the shape a model is shown and the shape a settlement enforces cannot
 * be two declarations that drift. `authority` names the {@link StageAuthority} field a list
 * answers to, and null is the two every stage has: a candidate is what an exploration is for,
 * and a question authorizes nothing (§4.8).
 */
const LISTS = {
  candidates: { schema: CandidateSchema, item: "candidate", authority: null },
  consolidations: { schema: ConsolidationSchema, item: "consolidation", authority: "consolidate" },
  objections: { schema: ObjectionSchema, item: "objection", authority: "objections" },
  deferred: { schema: DisposalSchema, item: "disposal", authority: "schedule" },
  rejected: { schema: DisposalSchema, item: "disposal", authority: "schedule" },
  next_actions: { schema: NextActionDraftSchema, item: "next-action", authority: "nextActions" },
  questions: { schema: QuestionDraftSchema, item: "question", authority: null },
} as const satisfies Record<
  string,
  { schema: z.ZodType; item: string; authority: keyof StageAuthority | null }
>;

/** One of the seven, as the pointer in a refusal spells it. */
type ExploreList = keyof typeof LISTS;
const EXPLORE_LISTS = Object.keys(LISTS) as readonly ExploreList[];

/** Whether this stage may fill this list at all. */
function admits(stage: Stage, list: ExploreList): boolean {
  const authority = LISTS[list].authority;
  return authority === null || STAGE_AUTHORITY[stage][authority];
}

/** The candidate shape a stage is offered: its claim, and what its authority admits beneath it. */
function candidateSchema(stage: Stage): z.ZodType {
  const authority = STAGE_AUTHORITY[stage];
  return z.strictObject({
    ref: candidateShape.ref,
    hypothesis: candidateShape.hypothesis,
    ...(authority.observations ? { observations: candidateShape.observations } : {}),
    ...(authority.remedies ? { remedy: candidateShape.remedy } : {}),
  });
}

function exploreSchema(stage: Stage): z.ZodType {
  const shape: Record<string, z.ZodType> = {};
  for (const list of EXPLORE_LISTS) {
    if (!admits(stage, list)) continue;
    shape[list] = z
      .array(list === "candidates" ? candidateSchema(stage) : LISTS[list].schema)
      .default([]);
  }
  return z.strictObject(shape);
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

/*
  A SUBMISSION IS PARTIAL, NOT ATOMIC (#231, #311).

  A run is one agent session over a large corpus, and by the time anything here reads its answer
  the tokens are gone. All-or-nothing persistence turned a partly-wrong answer into zero value at
  full price: on 2026-09-12 several runs finished their model work and lost ALL of it at
  persistence — one disposition naming a workspace the machine did not have, one objection
  attacking an id nobody held, one observation that forgot its counter-evidence position — and
  the conductor then parked reporting that the cycles had spent nothing (post-mortem F16, #231).
  Each of those is ONE unusable item. The items that validate are kept, the items that do not are
  refused BY NAME with the reason, and the run is spend either way.

  WHAT "KEPT" MEANS FOR A RESULT MEANT TO BE READ AS A SET. The items are not independent:
  §4.2's development path is the structure, so what is kept is the largest subset CLOSED UNDER
  THAT PATH. A refused item takes with it everything whose support ran through it — a candidate's
  observations and its remedy, a consolidation resting on an observation that fell, an objection
  attacking a candidate that fell — and nothing else. That answers the objection this file used
  to make ("half a development path is worse than none"): the half that is worse than none is the
  half that DANGLES, and a path-closed subset never dangles. A finding whose observations were
  refused is refused with them; a finding whose observations stood is a finding.

  THE ITEM IS THE RECORD OR THE ROW, which is why an observation inside a candidate is its own
  item and not part of one: a hypothesis rests on nothing, so it does not fall because one of the
  claims developing it did, and #231's third cause was exactly a single observation failing one
  schema obligation. A refusal names its item by JSON Pointer into the submitted document,
  because the receipt carries that document beside it (`rejectedSubmission`) and "which one" has
  to be findable in it.

  THE FLOOR IS {@link SUBMISSION_KEPT_FLOOR}: below it the submission is refused whole, and that
  constant carries the reasoning. Everything else here is a consequence rather than a judgement.

  ONE VALIDATOR, and {@link itemRefusal} is it. Every rule about an item is stated there once;
  `server/engine/records.ts` turns the kept items into rows and states none of them a second
  time, which is the whole reason it can no longer refuse anything. The two used to disagree in
  the open — a consolidation resting on a proposal was `development-path` here and
  `unknown-reference: no observation f1` there, for one submission, depending on which of the two
  saw it — and the Go tree's worst evaluation bug was one rule stated three times (F8).
*/

/** One item of a submission the contract refused, as a receipt records it (#231). */
export interface RefusedItem {
  /** JSON Pointer into the submitted document: `/candidates/0/observations/1`. */
  readonly item: string;
  /** `<code>: <sentence>` — the validator's own words, in the shape a `reason` already has. */
  readonly reason: string;
}

/** What one submission amounts to: what stands, what was dropped, and why nothing stood. */
export interface ExploreSubmission {
  /** The path-closed subset to record, or null when the submission is refused whole. */
  readonly result: ExploreResult | null;
  /** Every item dropped so the rest could stand, whether or not the rest did. */
  readonly refused: readonly RefusedItem[];
  /** `<code>: <sentence>` when nothing is recorded, and "" when something is. */
  readonly reason: string;
}

/** What a handle declared in one submission becomes: §4.2's path is checked over these. */
type RefKind = "hypothesis" | "observation" | "finding" | "proposal";

/**
 * One item with its shape already decided: the kind is what decides the rules it is held to,
 * and `refused` is an item whose own schema turned it down, which nothing can rest on.
 */
type Shaped =
  | { readonly kind: "candidate"; readonly value: z.infer<typeof CandidateEnvelope> }
  | { readonly kind: "observation"; readonly value: Observation }
  | { readonly kind: "remedy"; readonly value: Remedy }
  | { readonly kind: "consolidation"; readonly value: Consolidation }
  | { readonly kind: "objection"; readonly value: Objection }
  | { readonly kind: "disposal"; readonly value: Disposal }
  | { readonly kind: "question"; readonly value: QuestionDraft }
  | { readonly kind: "next-action"; readonly value: NextActionDraft }
  | { readonly kind: "refused" };

/** One item as the partition holds it: where it is, what it parsed to, and its fate. */
type Item = Shaped & {
  /** The JSON Pointer a refusal names it by. */
  readonly at: string;
  /** The list it goes back into when it stands. */
  readonly list: ExploreList;
  /** The candidate this observation or remedy hangs off; null for every top-level item. */
  readonly parent: Item | null;
  /** The handle it declares, or "" when it declares none. */
  readonly declares: string;
  /** The record that handle becomes, or null when it declares none. */
  readonly family: RefKind | null;
  /** `<code>: <sentence>` once refused, and "" while it stands. */
  reason: string;
};

/** What one item is checked against beyond itself. */
interface Scope {
  /** Every handle that still stands, by the record it becomes. */
  readonly refs: ReadonlyMap<string, RefKind>;
  /** Every handle whose own item was refused: what a dangling reference names. */
  readonly dropped: ReadonlySet<string>;
  /** The material this run was served: what {@link unservedCitation} admits a locator against. */
  readonly served: readonly MaterialEntry[];
}

/** A durable identifier of one family, as `RecordIdSchema` spells the four. */
const HYPOTHESIS_REF = /^hyp_[0-9a-f]{8,64}$/u;
const OBSERVATION_REF = /^obs_[0-9a-f]{8,64}$/u;

/**
 * The candidate as an envelope: its own claim strictly, its observations and its remedy unread.
 *
 * The two steps are what makes one bad observation cost one observation. A candidate parsed
 * whole fails whole, and #231's third cause was exactly that — `persist finding "con3":
 * counter-evidence must be listed or explicitly declared absent`, one obligation missed on one
 * item, and the whole paid run was gone.
 */
const CandidateEnvelope = z.strictObject({
  ref: candidateShape.ref,
  hypothesis: candidateShape.hypothesis,
  observations: z.array(z.unknown()).default([]),
  remedy: z.unknown().optional(),
});

/**
 * What hangs off a candidate, read without judging the candidate: the harvest that lets an
 * observation under an unreadable candidate still be an item with a name and a handle.
 */
const HangingOff = z.looseObject({
  observations: z.array(z.unknown()).default([]),
  remedy: z.unknown().optional(),
});

/**
 * The submission as seven lists of unread items: strict in its keys, and in nothing else.
 *
 * Every list is admitted here whatever the stage, and a list the stage has no authority for is
 * refused ITEM BY ITEM below under {@link REFUSALS.authority}. Authority is still enforced by
 * absence where absence is what the model sees — {@link exploreSchema} never offers a challenger
 * a `consolidations` field — but on the refusing side a field filled without authority is the
 * same accident as any other bad item, and taking the candidates down with it is the defect this
 * path exists to stop. An UNKNOWN key is refused whole: it is not an item, so there is nothing
 * to keep and nothing to count.
 */
const ExploreEnvelope = z.strictObject({
  candidates: z.array(z.unknown()).default([]),
  consolidations: z.array(z.unknown()).default([]),
  objections: z.array(z.unknown()).default([]),
  deferred: z.array(z.unknown()).default([]),
  rejected: z.array(z.unknown()).default([]),
  next_actions: z.array(z.unknown()).default([]),
  questions: z.array(z.unknown()).default([]),
});

/**
 * ONE EXPLORATION SUBMISSION, KEPT IN PART.
 *
 * The reference table is rebuilt after every round because a refusal removes the handles its own
 * item declared, and an item resting on one of those is refused in the next round: the loop is
 * the transitive closure of §4.2's path, and it settles because a reason is only ever set.
 */
export function exploreSubmission(
  stage: Stage,
  payload: unknown,
  sessions: readonly MaterialEntry[],
): ExploreSubmission {
  const envelope = ExploreEnvelope.safeParse(payload);
  if (!envelope.success) {
    return {
      result: null,
      refused: [],
      reason:
        `${REFUSALS.schema}: the ${stage} result does not match its schema: ` +
        issues(envelope.error),
    };
  }
  const items = shapeItems(stage, envelope.data);
  for (let settling = true; settling;) {
    settling = false;
    const refs = new Map<string, RefKind>();
    const dropped = new Set<string>();
    for (const item of items) {
      if (item.declares === "" || item.family === null) continue;
      if (item.reason !== "") {
        dropped.add(item.declares);
        continue;
      }
      if (refs.has(item.declares)) {
        // The LATER item loses: the first declaration is the one every reference in the answer
        // was written against, and minting both would have collapsed two claims into one row at
        // the ingest's `INSERT OR IGNORE` with nothing saying so.
        item.reason = `${REFUSALS.schema}: ref ${JSON.stringify(item.declares)} is used twice`;
        dropped.add(item.declares);
        settling = true;
        continue;
      }
      refs.set(item.declares, item.family);
    }
    const scope: Scope = { refs, dropped, served: sessions };
    for (const item of items) {
      if (item.reason !== "") continue;
      if (item.parent !== null && item.parent.reason !== "") {
        item.reason =
          `${REFUSALS.developmentPath}: the candidate this ${item.kind} ` +
          `${item.kind === "remedy" ? "addresses" : "develops"} was refused`;
        settling = true;
        continue;
      }
      const refusal = itemRefusal(item, scope);
      if (refusal === null) continue;
      item.reason = refusalReason(refusal);
      settling = true;
    }
  }
  const refused: RefusedItem[] = [];
  for (const item of items) {
    if (item.reason !== "") refused.push({ item: item.at, reason: item.reason });
  }
  const first = refused[0];
  if (first !== undefined && items.length - refused.length < items.length * SUBMISSION_KEPT_FLOOR) {
    return {
      result: null,
      refused,
      // THE DEFECT, NOT ITS CONSEQUENCE, leads the sentence: an operator reading a failed run
      // needs the rule that was broken, and `refusalCode` reads the class off the front of it.
      // The count follows, because "one item was wrong" and "this answer was not written against
      // this contract" are different findings and a receipt has to tell them apart.
      reason:
        `${first.reason} — and ${String(refused.length)} of this submission's ` +
        `${String(items.length)} items were refused, which keeps less than the ` +
        `${String(SUBMISSION_KEPT_FLOOR * 100)}% a submission must, so none of it was recorded`,
    };
  }
  return { result: keptResult(items), refused, reason: "" };
}

/**
 * Every item of one submission, shaped against its own schema and its stage's authority.
 *
 * Neither a shape refusal nor an authority refusal stops the walk: the point of the walk is that
 * the rest of the answer survives whatever one item did.
 */
function shapeItems(stage: Stage, envelope: Record<ExploreList, readonly unknown[]>): Item[] {
  const items: Item[] = [];
  for (const list of EXPLORE_LISTS) {
    const authority = LISTS[list].authority;
    const allowed = authority === null || STAGE_AUTHORITY[stage][authority];
    for (const [index, raw] of envelope[list].entries()) {
      const at = `/${list}/${String(index)}`;
      if (!allowed) {
        items.push(
          refusedItem(
            at,
            list,
            REFUSALS.authority,
            `${article(stage)} ${stage} result may not carry ${list}`,
          ),
        );
        continue;
      }
      if (list === "candidates") {
        items.push(...candidateItems(stage, at, raw));
        continue;
      }
      const parsed = LISTS[list].schema.safeParse(raw);
      if (!parsed.success) {
        items.push(
          refusedItem(
            at,
            list,
            REFUSALS.schema,
            `this ${LISTS[list].item} does not match its schema: ${issues(parsed.error)}`,
          ),
        );
        continue;
      }
      items.push({ at, list, parent: null, reason: "", ...shapedItem(list, parsed.data) });
    }
  }
  return items;
}

/**
 * One shaped top-level item: its kind, its value, and the handle it declares.
 *
 * A parsed list item is typed by the schema that admitted it, and the schema is chosen by the
 * list, so this is where the two are tied together — once, rather than at each of the seven
 * branches that would otherwise have to say which shape its own list holds.
 */
function shapedItem(
  list: Exclude<ExploreList, "candidates">,
  value: unknown,
): Shaped & { declares: string; family: RefKind | null } {
  switch (list) {
    case "consolidations": {
      const consolidation = value as Consolidation;
      return {
        kind: "consolidation",
        value: consolidation,
        declares: consolidation.ref,
        family: "finding",
      };
    }
    case "objections": {
      // An objection carrying locators becomes the observation §4.3 admits, and one carrying
      // none becomes a contradicting candidate. `records.ts` mints it on the same branch, so the
      // handle is registered under the family it will actually have — without that, an objection
      // and a candidate sharing a handle mint one row identifier and two claims collapse into
      // one at the ingest with nothing saying so.
      const objection = value as Objection;
      return {
        kind: "objection",
        value: objection,
        declares: objection.ref,
        family: objection.claim.evidence.length > 0 ? "observation" : "hypothesis",
      };
    }
    case "deferred":
    case "rejected":
      return { kind: "disposal", value: value as Disposal, declares: "", family: null };
    case "next_actions":
      return { kind: "next-action", value: value as NextActionDraft, declares: "", family: null };
    case "questions":
      return { kind: "question", value: value as QuestionDraft, declares: "", family: null };
  }
}

/** One candidate, its observations and its remedy: three kinds of item under one pointer. */
function candidateItems(stage: Stage, at: string, raw: unknown): Item[] {
  const parsed = CandidateEnvelope.safeParse(raw);
  const authority = STAGE_AUTHORITY[stage];
  // WHAT HANGS OFF IT IS READ EVEN WHEN THE CANDIDATE ITSELF IS NOT. A candidate whose own
  // claim is unreadable still submitted observations, and they have to become items or the
  // receipt cannot name them and a consolidation resting on one would be refused for a handle
  // "nobody emitted" rather than for the candidate that fell. This harvests; the strict parse
  // above is what judges.
  const hanging = HangingOff.safeParse(raw);
  const candidate: Item = parsed.success
    ? {
        at,
        list: "candidates",
        kind: "candidate",
        value: parsed.data,
        parent: null,
        declares: parsed.data.ref,
        family: "hypothesis",
        reason: "",
      }
    : refusedItem(
        at,
        "candidates",
        REFUSALS.schema,
        `this candidate does not match its schema: ${issues(parsed.error)}`,
      );
  const items: Item[] = [candidate];
  const submitted = hanging.success ? hanging.data : { observations: [], remedy: undefined };
  for (const [index, observed] of submitted.observations.entries()) {
    const where = `${at}/observations/${String(index)}`;
    if (!authority.observations) {
      items.push(
        refusedItem(
          where,
          "candidates",
          REFUSALS.authority,
          `${article(stage)} ${stage} candidate may not carry observations`,
        ),
      );
      continue;
    }
    const observation = ObservationSchema.safeParse(observed);
    if (!observation.success) {
      items.push(
        refusedItem(
          where,
          "candidates",
          REFUSALS.schema,
          `this observation does not match its schema: ${issues(observation.error)}`,
        ),
      );
      continue;
    }
    items.push({
      at: where,
      list: "candidates",
      kind: "observation",
      value: observation.data,
      parent: candidate,
      declares: observation.data.ref,
      family: "observation",
      reason: "",
    });
  }
  if (submitted.remedy === undefined) return items;
  const where = `${at}/remedy`;
  if (!authority.remedies) {
    items.push(
      refusedItem(
        where,
        "candidates",
        REFUSALS.authority,
        `${article(stage)} ${stage} candidate may not carry a remedy`,
      ),
    );
    return items;
  }
  const remedy = RemedySchema.safeParse(submitted.remedy);
  items.push(
    remedy.success
      ? {
          at: where,
          list: "candidates",
          kind: "remedy",
          value: remedy.data,
          parent: candidate,
          declares: remedy.data.ref,
          family: "proposal",
          reason: "",
        }
      : refusedItem(
          where,
          "candidates",
          REFUSALS.schema,
          `this remedy does not match its schema: ${issues(remedy.error)}`,
        ),
  );
  return items;
}

/** An item refused before it had a shape: it declares no handle, so nothing may rest on it. */
function refusedItem(at: string, list: ExploreList, code: RefusalCode, sentence: string): Item {
  return {
    at,
    list,
    kind: "refused",
    parent: null,
    declares: "",
    family: null,
    // The pointer is `item`'s job; the reason is the sentence, in the shape every other one has.
    reason: `${code}: ${sentence}`,
  };
}

/**
 * WHAT THE CONTRACT REFUSES ABOUT ONE ITEM, or null when it admits it.
 *
 * This is the one statement of every rule an item is held to: its references resolve, its
 * citations were served, and its authority covers what it asks for. It says nothing about
 * whether a claim is true. `server/engine/records.ts` turns the kept items into rows and
 * re-states none of this, which is what makes the two impossible to drift apart (#263).
 */
function itemRefusal(item: Item, scope: Scope): ResultRefusal | null {
  switch (item.kind) {
    // A candidate rests on nothing and cites nothing: the statement is the whole of it. A
    // `refused` item has already failed its own schema and nothing more is asked of it.
    case "candidate":
    case "refused":
      return null;
    case "observation":
      return unservedEvidence(`observation ${JSON.stringify(item.value.ref)}`, scope, [
        ...item.value.claim.evidence,
        ...item.value.claim.counter_evidence,
      ]);
    case "remedy":
      return unservedEvidence(`proposal ${JSON.stringify(item.value.ref)}`, scope, [
        ...item.value.proposal.supporting,
        ...item.value.proposal.conflicting,
      ]);
    case "consolidation": {
      const consolidation = item.value;
      const named = JSON.stringify(consolidation.ref);
      // §4.2's path is mandatory: a consolidation rests on locator-backed observations that
      // exist. A name that is neither a surviving ref of this result nor a durable identifier
      // the brief listed is a refusal, never a repair — the repair would be Babel inventing the
      // evidence step.
      for (const name of consolidation.observations) {
        const within = scope.refs.get(name);
        if (within === undefined) {
          if (scope.dropped.has(name)) {
            return new ResultRefusal(
              REFUSALS.developmentPath,
              `consolidation ${named} rests on ${JSON.stringify(name)}, which this submission refused`,
            );
          }
          if (!OBSERVATION_REF.test(name)) {
            return new ResultRefusal(
              REFUSALS.developmentPath,
              `consolidation ${named} rests on ${JSON.stringify(name)}, which is neither a ref in this result nor an observation identifier`,
            );
          }
          continue;
        }
        if (within !== "observation") {
          return new ResultRefusal(
            REFUSALS.developmentPath,
            `consolidation ${named} rests on ${JSON.stringify(name)}, which is a ${within} rather than an observation`,
          );
        }
      }
      const proposal = consolidation.proposal;
      return unservedEvidence(`finding ${named}`, scope, [
        ...consolidation.finding.counter_evidence,
        ...(proposal === undefined ? [] : [...proposal.supporting, ...proposal.conflicting]),
      ]);
    }
    case "objection": {
      const objection = item.value;
      const named = JSON.stringify(objection.ref);
      const attacked = JSON.stringify(objection.hypothesis);
      const target = scope.refs.get(objection.hypothesis);
      if (target === undefined) {
        if (scope.dropped.has(objection.hypothesis)) {
          return new ResultRefusal(
            REFUSALS.unknownReference,
            `objection ${named} attacks ${attacked}, which this submission refused`,
          );
        }
        if (!HYPOTHESIS_REF.test(objection.hypothesis)) {
          return new ResultRefusal(
            REFUSALS.unknownReference,
            `objection ${named} attacks ${attacked}, which this result did not emit and no brief listed`,
          );
        }
      } else if (target !== "hypothesis") {
        return new ResultRefusal(
          REFUSALS.unknownReference,
          `objection ${named} attacks a ${target}; §5.4 criticism names a hypothesis`,
        );
      }
      if (objection.grounds === "evidence" && objection.claim.evidence.length === 0) {
        return new ResultRefusal(
          REFUSALS.support,
          `objection ${named} rests on evidence and cites none`,
        );
      }
      return unservedEvidence(`objection ${named}`, scope, [
        ...objection.claim.evidence,
        ...objection.claim.counter_evidence,
      ]);
    }
    case "disposal": {
      const named = JSON.stringify(item.value.hypothesis);
      if (scope.refs.has(item.value.hypothesis) || HYPOTHESIS_REF.test(item.value.hypothesis)) {
        return null;
      }
      return new ResultRefusal(
        REFUSALS.unknownReference,
        scope.dropped.has(item.value.hypothesis)
          ? `${named} was set down and this submission refused it`
          : `${named} was set down and is not a candidate this result or the brief named`,
      );
    }
    case "question": {
      const question = item.value;
      if (question.hypothesis === "" || scope.refs.has(question.hypothesis)) return null;
      if (HYPOTHESIS_REF.test(question.hypothesis)) return null;
      return new ResultRefusal(
        REFUSALS.unknownReference,
        `question ${JSON.stringify(question.ref)} blocks ${JSON.stringify(question.hypothesis)}, ` +
          (scope.dropped.has(question.hypothesis)
            ? "which this submission refused"
            : "which is not a candidate it named"),
      );
    }
    case "next-action": {
      // A PROPOSED ACTION NAMES A RECORD THAT WILL EXIST. `next_actions.record_id` references
      // `records(id)`, so a proposal about a handle this result never declared could not be
      // inserted at all; refusing it says so in the model's own vocabulary. An observation is
      // admitted: §4.13 makes it evidence rather than a post, but it is a record, and "develop
      // this further" about one is a coherent thing to ask for.
      const action = item.value;
      const named = JSON.stringify(action.record);
      if (!scope.refs.has(action.record) && !DURABLE_RECORD.test(action.record)) {
        return new ResultRefusal(
          REFUSALS.unknownReference,
          scope.dropped.has(action.record)
            ? `a ${action.kind} is proposed on ${named}, which this submission refused`
            : `a ${action.kind} is proposed on ${named}, which this result did not emit and no brief listed`,
        );
      }
      // THE DISPOSITION NOBODY HERE CAN ACT ON (#231, cause 1). `draft-issue` is the one kind
      // bound to a checkout, and one naming no workspace is a change proposed to nothing. It
      // costs itself now: the records the run produced are not a suggestion's fault.
      if (action.kind === "draft-issue" && action.workspace === "") {
        return new ResultRefusal(
          REFUSALS.support,
          `the draft-issue proposed on ${named} names no workspace, so the issue would be about no repository`,
        );
      }
      if (action.kind !== "draft-issue" && action.workspace !== "") {
        return new ResultRefusal(
          REFUSALS.authority,
          `a ${action.kind} binds to no repository, and the one on ${named} names a workspace`,
        );
      }
      return null;
    }
  }
}

/**
 * WHAT ONE ITEM'S CITATIONS COST IT, or null when every one of them is admissible.
 *
 * The RULE is not here. `server/engine/citations.ts` is the single spelling of what a locator
 * has to be — the index entry it names, the source digest it was served at, and every shape
 * that tries to leave the material — and this asks it. What is here is the GRAIN: the question
 * is put to one observation, one proposal, one finding or one objection at a time, so a retyped
 * digest costs the claim that carries it and nothing beside it, which is the sentence
 * `INSTRUCTIONS_EVIDENCE` promises the model. The whole-result form this replaced refused every
 * sibling of the broken claim (#231), and the copy of the rule that used to sit beside it here
 * is the second declaration #263 was opened about.
 */
function unservedEvidence(
  what: string,
  scope: Scope,
  evidence: readonly Evidence[],
): ResultRefusal | null {
  const unserved = unservedCitation(evidence, scope.served);
  if (unserved === "") return null;
  return new ResultRefusal(REFUSALS.unknownReference, `${what} cites ${unserved}`);
}

/** The kept items back in their lists, every list present so no consumer has to ask. */
function keptResult(items: readonly Item[]): ExploreResult {
  const result: ExploreResult = {
    candidates: [],
    consolidations: [],
    objections: [],
    deferred: [],
    rejected: [],
    questions: [],
    next_actions: [],
  };
  const observations = new Map<Item, Observation[]>();
  const remedies = new Map<Item, Remedy>();
  for (const item of items) {
    if (item.reason !== "" || item.parent === null) continue;
    if (item.kind === "observation") {
      const kept = observations.get(item.parent);
      if (kept === undefined) observations.set(item.parent, [item.value]);
      else kept.push(item.value);
    } else if (item.kind === "remedy") remedies.set(item.parent, item.value);
  }
  for (const item of items) {
    if (item.reason !== "" || item.parent !== null) continue;
    switch (item.kind) {
      case "candidate": {
        const remedy = remedies.get(item);
        result.candidates.push({
          ref: item.value.ref,
          hypothesis: item.value.hypothesis,
          observations: observations.get(item) ?? [],
          ...(remedy === undefined ? {} : { remedy }),
        });
        break;
      }
      case "consolidation":
        result.consolidations.push(item.value);
        break;
      case "objection":
        result.objections.push(item.value);
        break;
      case "disposal":
        (item.list === "deferred" ? result.deferred : result.rejected).push(item.value);
        break;
      case "next-action":
        result.next_actions.push(item.value);
        break;
      case "question":
        result.questions.push(item.value);
        break;
      default:
        break;
    }
  }
  return result;
}

// ---------------------------------------------------------------------------- the review

export type Role = (typeof ROLES)[number];

/** The observed-outcome vocabulary of §4.12's full lifecycle. */
const OUTCOMES = ["implemented", "verified", "partial", "contradicted", "unverifiable"] as const;

/**
 * §4.12's scope rule, stated ONCE: the prompt tells the model this sentence, the refusal reads it
 * back, and `acceptReviewResult` enforces it. A second wording of it — in the prompt, in the
 * store, in the engine's schema — is a second rule, which is exactly what cost the drain of
 * 2026-09-13 its evidence-role reviews.
 */
export const REVIEW_SCOPE_RULE =
  "Any outcome or criterion result also needs `environment`, the setting observed, and `as_of`, " +
  "when it was observed: a result with no stated scope reads as a claim about every setting at " +
  "every time, and an environment or an as-of time with neither an outcome nor a criterion " +
  "result beside it scopes nothing.";

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

/**
 * The part of the immutable record a contribution addresses. An empty JSON Pointer addresses
 * the record as a whole; `/payload/problem` and `/payload/questions/0` address exact portions.
 * The record identity is implicit in the assessment, so a model cannot redirect a contribution
 * to material it was not asked to review.
 */
const ContributionTargetSchema = z.strictObject({
  path: z
    .string()
    .refine(
      (value) => value === "" || (value.startsWith("/") && !value.endsWith("/")),
      "a contribution target is an empty or absolute JSON Pointer",
    ),
});

const ContributionSchema = z.strictObject({
  kind: z.enum(CONTRIBUTION_KINDS),
  text: z.string().default(""),
  target: ContributionTargetSchema.optional(),
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
  reception: {
    vote: true,
    outcome: false,
    criteria: false,
    alternatives: false,
    filing: false,
    backlog: false,
  },
  evidence: {
    vote: false,
    outcome: false,
    criteria: true,
    alternatives: false,
    filing: false,
    backlog: false,
  },
  challenge: {
    vote: false,
    outcome: false,
    criteria: false,
    alternatives: false,
    filing: false,
    backlog: false,
  },
  comparison: {
    vote: false,
    outcome: false,
    criteria: false,
    alternatives: true,
    filing: false,
    backlog: false,
  },
  outcome: {
    vote: false,
    outcome: true,
    criteria: true,
    alternatives: false,
    filing: false,
    backlog: false,
  },
  relevance: {
    vote: false,
    outcome: false,
    criteria: false,
    alternatives: false,
    filing: false,
    backlog: false,
  },
  filing: {
    vote: false,
    outcome: false,
    criteria: false,
    alternatives: false,
    filing: true,
    backlog: false,
  },
  backlog: {
    vote: false,
    outcome: false,
    criteria: false,
    alternatives: false,
    filing: false,
    backlog: true,
  },
};

/**
 * One accepted review submission, normalized: the shape an `assessments` row carries and the
 * shape BOTH halves validate. Absence is a statement everywhere in it, so every field is present
 * and every answer is either the thing or `null` — a reader never asks whether a role could have
 * emitted one.
 *
 * It strips rather than refuses what it does not name, because the row's payload carries the
 * assignment's own facts beside the result — the policy and context versions, the recipe, whether
 * the review was taken blind — and those are the store's rather than the model's.
 */
const ReviewResultSchema = z.object({
  vote: z.enum(["", ...VOTES]).default(""),
  contributions: z.array(ContributionSchema).default([]),
  outcome: z.enum(["", ...OUTCOMES]).default(""),
  results: z.array(CriterionResultSchema).default([]),
  environment: z.string().trim().default(""),
  asOf: z.string().trim().default(""),
  uncertainty: z.string().trim().default(""),
  skip: z.string().trim().default(""),
  filing: FiledUnderSchema.nullable().default(null),
  topic: TopicProposalSchema.nullable().default(null),
  noTopic: NoTopicSchema.nullable().default(null),
  noChange: NoChangeSchema.nullable().default(null),
  consolidate: BacklogConsolidationSchema.nullable().default(null),
  supersede: SupersessionSchema.nullable().default(null),
  retire: RetirementSchema.nullable().default(null),
  promote: PromotionSchema.nullable().default(null),
  keep: KeptSchema.nullable().default(null),
});
export type ReviewResult = z.infer<typeof ReviewResultSchema>;

/**
 * THE CONTRACT OFFERS A SKIP OR AN ASSESSMENT, NEVER BOTH (#311).
 *
 * `a skip cannot also state an assessment` is a rule the recipe states in prose
 * (`cookbook/recipes/babel-triages-the-queue.md`: "Declining is not opposing") and
 * {@link acceptReviewResult} enforces — and five of one day's runs were discarded whole for
 * breaking it, having already paid for the inference. A flat object offering `skip` beside
 * `vote` invites exactly that, so what a role is shown is a union of the two answers it may
 * give: a skip with its reason, or an assessment.
 *
 * THE VALIDATOR IS STILL THE ENFORCEMENT, and this is documentation strength rather than a
 * guarantee: the generated schema is printed into the prompt (`server/engine/review.ts`), not
 * registered as a provider-constrained tool, so a model can still type both. What changes is
 * that the contract it reads cannot express the mistake, instead of forbidding it in prose two
 * files away.
 *
 * The assessment form accepts `skip: ""` because that is what a model echoing an empty field
 * submits today, and it has always been valid. Refusing it here would trade five refusals for a
 * new class of them.
 */
function reviewSchema(role: Role): z.ZodType {
  const authority = ROLE_AUTHORITY[role];
  const work = authority.filing || authority.backlog;
  const assessment = {
    ...(authority.vote ? { vote: reviewShape.vote } : {}),
    // The whole result of a filing or backlog pass is which of its answers it reached. A
    // contribution would invite it to review the record it was drawn to name or to settle.
    ...(work ? {} : { contributions: reviewShape.contributions }),
    ...(authority.outcome ? { outcome: reviewShape.outcome } : {}),
    ...(authority.criteria
      ? {
          results: reviewShape.results,
          environment: reviewShape.environment,
          as_of: reviewShape.as_of,
        }
      : {}),
    uncertainty: reviewShape.uncertainty,
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
  };
  return z.union([
    z.strictObject({ skip: z.string().trim().min(1) }),
    z.strictObject({ ...assessment, skip: z.literal("").optional() }),
  ]);
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

/**
 * ONE ROLE'S ANSWER CONTRACT, PRINTED INTO THE PROMPT — not registered anywhere.
 *
 * This said "the JSON Schema the submit tool is registered with", and there is no submit tool:
 * the only caller interpolates it into the answer fence (`server/engine/review.ts`) and the
 * answer is read back out of the final message. Two readers reasoned correctly from that
 * sentence and reached the false conclusion that the shape was enforced where it is generated,
 * an hour apart, so the sentence is the defect: nothing here constrains a model, and
 * `acceptReviewResult` is the only enforcement there is. Making the old sentence true — a submit
 * tool whose parameters are this schema — is #315.
 */
export function reviewJsonSchema(role: Role): unknown {
  return z.toJSONSchema(reviewSchemas[role], { io: "input", target: "draft-2020-12" });
}

/**
 * The submission's field names into the normalized ones, and the only place the two vocabularies
 * meet: a submission is snake_case because that is what the generated JSON Schema offers a model,
 * and the row is camelCase because that is what the store reads back. A field a role had no
 * authority for is absent from `parsed`, and `ReviewResultSchema` reads that absence as the empty
 * answer it is.
 */
function normalize(submitted: unknown): Record<string, unknown> {
  const data = (submitted ?? {}) as Record<string, unknown>;
  return {
    vote: data["vote"],
    contributions: data["contributions"],
    outcome: data["outcome"],
    results: data["results"],
    environment: data["environment"],
    asOf: data["as_of"],
    uncertainty: data["uncertainty"],
    skip: data["skip"],
    filing: data["filing"],
    topic: data["topic"],
    noTopic: data["no_topic"],
    noChange: data["no_change"],
    consolidate: data["consolidate"],
    supersede: data["supersede"],
    retire: data["retire"],
    promote: data["promote"],
    keep: data["keep"],
  };
}

/** What this run authored among the records it was handed: what the self-boost refusal reads. */
export interface ReviewSelf {
  /** The record under review came out of this run. */
  target: boolean;
  /** The alternative subject identities this run authored. */
  subjects: Readonly<Record<string, true>>;
}

/**
 * Decodes one submission the model made and accepts it. The role's own schema runs first — it is
 * the one the engine validated the call against, so a field outside the role's authority is
 * refused here with the reason rather than dropped — and what it admits goes through the same
 * acceptance the store runs.
 */
export function parseReviewResult(role: Role, payload: unknown, self?: ReviewSelf): ReviewResult {
  return acceptReviewResult(role, shapeReviewResult(role, payload), self);
}

/**
 * THE SAME SUBMISSION WITH ITS SHAPE CHECKED AND NO RULE APPLIED YET (#305).
 *
 * `parseReviewResult` is this step and then the acceptance, which is how it always read; the
 * step is named because a caller that wants to know WHICH contribution the rules refuse has to
 * be able to see the contributions, and the acceptance throws on the first offence. Everything
 * recorded still goes through {@link acceptReviewResult}: this returns a shape, never a verdict.
 */
export function shapeReviewResult(role: Role, payload: unknown): ReviewResult {
  const parsed = reviewSchemas[role].safeParse(payload);
  if (!parsed.success) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `the ${role} result does not match its schema: ${issues(parsed.error)}`,
    );
  }
  const shaped = ReviewResultSchema.safeParse(normalize(parsed.data));
  if (!shaped.success) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `the ${role} result does not match its schema: ${issues(shaped.error)}`,
    );
  }
  return shaped.data;
}

/**
 * THE ONE ACCEPTANCE: shape, vocabulary, authority, support and emptiness over a normalized
 * result, and nothing about whether the judgement is correct — Babel validates structure and
 * provenance, and a reception vote has no truth condition to check.
 *
 * The machine half reaches it through `parseReviewResult` with what the model submitted; the
 * store reaches it with the payload of an `assessments` row a machine half wrote. Both get the
 * same verdict under the same refusal code, which is the whole of F8's remedy: the producer's
 * contract and the store's acceptance cannot drift apart because there is one of them.
 *
 * `reviewSchema` OFFERS A SKIP OR AN ASSESSMENT AND THIS STILL CHECKS IT (#311). That is not
 * redundancy: the generated schema is printed into the prompt, never registered as a
 * provider-constrained output, so nothing stops a model typing both — the rule below is not a
 * belt beside a structural guarantee, it is the only enforcement there is. Deleting it because
 * the schema "already says so" would remove the check and keep the sentence.
 */
export function acceptReviewResult(role: Role, payload: unknown, self?: ReviewSelf): ReviewResult {
  const authority = ROLE_AUTHORITY[role];
  const accepted = ReviewResultSchema.safeParse(payload);
  if (!accepted.success) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `the ${role} result does not match its schema: ${issues(accepted.error)}`,
    );
  }
  const result = accepted.data;
  refuseBeyondAuthority(role, result);
  const filed =
    result.filing !== null ||
    result.topic !== null ||
    result.noTopic !== null ||
    result.noChange !== null;
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
    const refused = contributionRefusal(role, contribution, index, self);
    if (refused !== null) throw refused;
  }

  // A run arguing against what it produced is the honest direction, so only endorsement is
  // refused: independence is the whole value of the judgement.
  if (
    self?.target === true &&
    (result.vote === "support" || result.outcome === "implemented" || result.outcome === "verified")
  ) {
    throw new ResultRefusal(REFUSALS.selfBoost, "this run authored the record under review");
  }
  if (
    result.outcome !== "" &&
    result.outcome !== "unverifiable" &&
    reviewEvidence(result).length === 0
  ) {
    throw new ResultRefusal(
      REFUSALS.support,
      `an observed outcome of ${result.outcome} needs evidence`,
    );
  }
  const scoped = result.outcome !== "" || result.results.length > 0;
  if (scoped && (result.environment === "" || result.asOf === "")) {
    throw new ResultRefusal(
      REFUSALS.support,
      `this result states an outcome or a criterion result with no environment or as-of time. ${REVIEW_SCOPE_RULE}`,
    );
  }
  // The other direction, and F8's own bug: the Go store refused every environment that did not
  // come with an outcome, so an evidence check that reported criterion results — which is what
  // the role exists to do — was paid for and then refused. An environment belongs to a claim
  // about a setting, and a criterion result is one.
  if (!scoped && (result.environment !== "" || result.asOf !== "")) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `this result states an environment or an as-of time and neither an outcome nor a criterion result. ${REVIEW_SCOPE_RULE}`,
    );
  }
  if (result.outcome === "unverifiable" && result.uncertainty === "") {
    throw new ResultRefusal(
      REFUSALS.support,
      "an unverifiable outcome must name what could not be checked",
    );
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

/** The indefinite article a refusal sentence needs for the contribution kind it names. */
function article(kind: string): string {
  return /^[aeiou]/u.test(kind) ? "an" : "a";
}

/**
 * WHAT THE CONTRACT REFUSES ABOUT ONE CONTRIBUTION, or null when it admits it.
 *
 * These are the rules whose whole subject is a single contribution — the kind that may not name
 * alternatives, the evidence that cites none, the contribution carrying nothing. The acceptance
 * above throws the first of them and refuses the submission, which is the right answer for a
 * producer writing an `assessments` row: a row is one judgement and a defective one is not
 * written. It is the wrong answer for a REVIEW SESSION, which submits several contributions at
 * once and lost every good one to the first bad one — seven conductor-drawn reviews on
 * `openrouter/stealth/union-alpha` spent 202k tokens and recorded two (#305). So the rules are
 * stated here once and asked two ways: thrown as a submission's refusal, and asked per
 * contribution by `server/engine/review.ts` so the refused one can be dropped by name while the
 * rest of the review goes through the SAME acceptance it always did.
 *
 * A refusal here names no remedy and repairs nothing: the caller's only choices are to record
 * the contribution as it stands or to record it not at all.
 */
export function contributionRefusal(
  role: Role,
  contribution: Contribution,
  index: number,
  self?: ReviewSelf,
): ResultRefusal | null {
  const authority = ROLE_AUTHORITY[role];
  const compares = contribution.kind === "comparison";
  if (compares && !authority.alternatives) {
    return new ResultRefusal(REFUSALS.authority, `the ${role} role may not compare alternatives`);
  }
  if (!compares && (contribution.alternatives.length > 0 || contribution.preferred !== undefined)) {
    return new ResultRefusal(
      REFUSALS.schema,
      // "is a objection" read as a typo in a sentence whose whole job is to be read back to a
      // model and to a reviewer, so the article follows the word (#311).
      `contribution ${index + 1} is ${article(contribution.kind)} ${contribution.kind} and may not name alternatives`,
    );
  }
  if (contribution.kind === "evidence" && contribution.evidence.length === 0) {
    return new ResultRefusal(
      REFUSALS.support,
      `contribution ${index + 1} offers evidence and cites none`,
    );
  }
  if (compares && contribution.alternatives.length < 2) {
    return new ResultRefusal(
      REFUSALS.schema,
      `contribution ${index + 1} compares ${contribution.alternatives.length} alternatives; a comparison needs at least two`,
    );
  }
  const preferred = contribution.preferred;
  if (
    compares &&
    preferred !== undefined &&
    !contribution.alternatives.some((alt) => alt.id === preferred.id)
  ) {
    return new ResultRefusal(
      REFUSALS.schema,
      `contribution ${index + 1} prefers an alternative it did not compare`,
    );
  }
  if (!compares && contribution.text === "" && contribution.evidence.length === 0) {
    return new ResultRefusal(
      REFUSALS.empty,
      `contribution ${index + 1} carries neither text nor evidence`,
    );
  }
  if (contribution.kind === "refinement") {
    if (contribution.target === undefined) {
      return new ResultRefusal(
        REFUSALS.schema,
        `refinement ${index + 1} names no part of the record`,
      );
    }
    if (contribution.text.trim() === "" || contribution.would_change.trim() === "") {
      return new ResultRefusal(
        REFUSALS.empty,
        `refinement ${index + 1} needs both a reason and the change it proposes`,
      );
    }
  }
  if (preferred !== undefined && self?.subjects[preferred.id] === true) {
    return new ResultRefusal(
      REFUSALS.selfBoost,
      `this run authored ${preferred.id}; a review may not prefer its own alternative`,
    );
  }
  return null;
}

/**
 * The role's authority, read off the same table `reviewSchema` prunes a role's fields from.
 *
 * The machine half never reaches a refusal here: a field the role has no authority for is not in
 * the schema the engine validated the call against, so the submission was already refused. The
 * STORE does reach it, because it accepts a normalized result a machine half wrote — and a
 * producer that drifted from the contract is the one thing one validator exists to catch.
 */
function refuseBeyondAuthority(role: Role, result: ReviewResult): void {
  const authority = ROLE_AUTHORITY[role];
  const beyond: string[] = [];
  if (!authority.vote && result.vote !== "") beyond.push("a vote");
  if (!authority.outcome && result.outcome !== "") beyond.push("an observed outcome");
  if (
    !authority.criteria &&
    (result.results.length > 0 || result.environment !== "" || result.asOf !== "")
  ) {
    beyond.push("criterion results");
  }
  if ((authority.filing || authority.backlog) && result.contributions.length > 0)
    beyond.push("contributions");
  if (
    !authority.filing &&
    (result.filing !== null ||
      result.topic !== null ||
      result.noTopic !== null ||
      result.noChange !== null)
  ) {
    beyond.push("a filing answer");
  }
  if (
    !authority.backlog &&
    (result.consolidate !== null ||
      result.supersede !== null ||
      result.retire !== null ||
      result.promote !== null ||
      result.keep !== null)
  ) {
    beyond.push("a backlog answer");
  }
  if (beyond.length > 0) {
    throw new ResultRefusal(
      REFUSALS.schema,
      `the ${role} result does not match its schema: it states ${beyond.join(", ")}`,
    );
  }
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
  const answers = [result.filing, result.topic, result.noTopic, result.noChange].filter(
    (a) => a !== null,
  ).length;
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
    throw new ResultRefusal(
      REFUSALS.schema,
      `a ${topic.operation} needs the name of the topic it would create`,
    );
  }
  if (!ENTITY_KINDS.includes(topic.kind as (typeof ENTITY_KINDS)[number])) {
    throw new ResultRefusal(
      REFUSALS.vocabulary,
      `${JSON.stringify(topic.kind)} is not an entity kind the ledger admits (${ENTITY_KINDS.join(", ")})`,
    );
  }
  if (topic.identity === "") {
    throw new ResultRefusal(
      REFUSALS.schema,
      `a ${topic.operation} needs the identity that deduplicates it`,
    );
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
  const answers = [
    result.consolidate,
    result.supersede,
    result.retire,
    result.promote,
    result.keep,
  ].filter((a) => a !== null).length;
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
