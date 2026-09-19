import { z } from "zod";
import {
  RECORD_KINDS,
  RecordIdSchema,
  RecordKindSchema,
  RULINGS,
} from "../../atyrode.babel/contract.ts";

/*
  THE QUESTION BANK: WHAT JEV IS ASKED, AND AT WHAT LINE AN ANSWER BECOMES A VOTE.

  A reworded criterion silently changes every vote downstream, so the bank is not a constant in a
  bundle: it is one reviewable document per record kind under `bank/questions/`, each declaring
  its own version, each registered in `bank/versions.json`, and drift between the two is refused
  by `tools/seed-questions.ts` — the same mechanism, and deliberately the same shape, as the
  cookbook's recipes (`atyrode.babel/tools/seed-recipes.ts`). An assessment cites `kind@version`
  the way a claim cites `recipe@version`, and a citation is only worth writing if the thing it
  names can be read back exactly.

  THIS MODULE IS THE VOCABULARY AND THE SHAPES: what a question, a vote, a routing question and
  an exemplar are, and the tally that sums the first of those. `bank/bank.ts` is the runtime half
  and holds the seed the hub ships with; `bank/parse.ts` is the dev-time bridge from the
  documents to that seed. Nothing a hub runs reads a repository file, which is why the documents
  and the shipped artifact are two things.

  THREE PROPERTIES ARE STRUCTURAL RATHER THAN CONVENTIONAL:

  1. A ROUTING QUESTION CANNOT BE TALLIED. `subject`, `classification` and `contains_instruction`
     say where a record goes and whether it may be published; they are not opinions about its
     merit. They live in `routing`, whose type has no `casts`, no `when` and no `observed` — so
     `tally()`, which takes `readonly Vote[]`, cannot be handed one. The parser closes the other
     direction: a thresholds row naming a routing question is refused, and so is a routing row
     naming anything else, so a routing question cannot become a vote by being written as one.

  2. A THRESHOLD WITHOUT ITS OBSERVED DISTRIBUTION IS REFUSED. Not marked, not defaulted —
     refused, by `bank/parse.ts`, naming the row. A threshold copied from a vendor's
     documentation is somebody else's corpus, and a number nobody can trace back to this one is
     the same thing with the citation removed.

  3. A SIDE THAT FIRES ON ALMOST NOTHING OR ALMOST EVERYTHING IS NOT ADMITTED. The study's own
     methodology finding is that a question answering the same way for everything is broken
     rather than calibrated, and it caught three of its own that way. `admitted` is derived from
     the recorded distribution rather than declared, so the two cannot disagree, and
     `bank.ts`'s `votesFor()` returns the admitted sides alone.

  Every threshold and every distribution here was fitted to one deployment's imported Go-era
  corpus of 6,038 records, at one date, under one recipe and model set. That is calibration, not
  behaviour: it belongs in data, per operator, and re-fitting it against what the plugin's own
  intake produces is the reason it is kept in a document. Read `docs/jev-case-study-audit.md` §0
  before quoting a number here as if it described Babel in general.
*/

/**
 * The thirteen voters, spelled as the study's own front page spells them. A voter is a question
 * plus the line at which its answer becomes an opinion; the two-sided ones cast on both ends of
 * their own distribution and the one-sided ones only back or only object.
 */
export const VOTERS = [
  "worth-of-attention",
  "concreteness",
  "contradicts-intent",
  "actionability",
  "evidence",
  "recurrence",
  "friction-lens",
  "freshness",
  "rigour",
  "scope",
  "editorial",
  "novelty",
  "trustworthiness",
] as const;
export const VoterSchema = z.enum(VOTERS);
export type Voter = z.infer<typeof VoterSchema>;

/**
 * The three questions asked of every record and tallied by nobody. They decide where a record is
 * filed, whether it may leave the machine, and whether it is addressing its own judge — none of
 * which is a view on whether the record is any good.
 */
export const ROUTING_QUESTIONS = ["subject", "classification", "contains_instruction"] as const;
export const RoutingQuestionSchema = z.enum(ROUTING_QUESTIONS);
export type RoutingQuestionId = z.infer<typeof RoutingQuestionSchema>;

/** A side of a voter firing on less of its kind than this answers the same way for everything. */
export const ADMISSION_FLOOR_PCT = 2;
/** So does one firing on more than this. Both bounds are the degeneracy rule, not a preference. */
export const ADMISSION_CEILING_PCT = 95;

/**
 * What kind of answer a question asks for, in the assessor's own vocabulary: a `score` over
 * described levels, a `choice` among named ones, or a `noul` — a bounded degree of yes.
 */
export const QuestionTypeSchema = z.enum(["score", "choice", "noul"]);

/**
 * ONE QUESTION, VERBATIM. `asks` and `criteria` are the text that goes out, and they are here
 * rather than in code because rewording either changes every answer downstream: the version in
 * the frontmatter is what an assessment cites, and a criterion edited without the bump would
 * make two incomparable sweeps look like one.
 */
export const QuestionSchema = z.strictObject({
  id: z.string().min(1),
  type: QuestionTypeSchema,
  asks: z.string().min(1),
  /** The described levels or named choices, in order. A `noul` has none and asks for a degree. */
  criteria: z.array(z.string().min(1)),
});
export type Question = z.infer<typeof QuestionSchema>;

/** How an answer becomes a vote. Four forms, because the questions have two shapes. */
export const ConditionSchema = z.discriminatedUnion("op", [
  z.strictObject({ op: z.literal("at-least"), value: z.number() }),
  z.strictObject({ op: z.literal("at-most"), value: z.number() }),
  z.strictObject({ op: z.literal("is"), value: z.string() }),
  z.strictObject({ op: z.literal("is-not"), value: z.string() }),
]);
export type Condition = z.infer<typeof ConditionSchema>;

/**
 * THE OBSERVED DISTRIBUTION THAT JUSTIFIED A THRESHOLD, for this record kind alone.
 *
 * `fires` is the share of that kind on which this side of the voter actually fired — the one
 * number that says whether the line is a line or a formality. `mean` and `sd` are `null` for a
 * question answered in categories rather than magnitudes, and the parser refuses a numeric
 * threshold that leaves them null.
 */
export const DistributionSchema = z.strictObject({
  n: z.number().int().positive(),
  fires: z.number().min(0).max(100),
  mean: z.number().nullable(),
  sd: z.number().min(0).nullable(),
});
export type Distribution = z.infer<typeof DistributionSchema>;

/** One side of one voter: the atom a tally sums. */
export const VoteSchema = z.strictObject({
  voter: VoterSchema,
  /** The question it thresholds, which `questions` defines and a policy renders. */
  question: z.string().min(1),
  casts: z.enum(["up", "down"]),
  when: ConditionSchema,
  observed: DistributionSchema,
  /** Derived from `observed.fires`, never declared: a number cannot disagree with itself. */
  admitted: z.boolean(),
});
export type Vote = z.infer<typeof VoteSchema>;

/**
 * A routing question's job. It carries no threshold and no direction BY TYPE, which is the whole
 * of why nothing can tally it: there is no field for a tally to read.
 */
export const RoutingSchema = z.strictObject({
  question: RoutingQuestionSchema,
  routes: z.string().min(1),
});
export type Routing = z.infer<typeof RoutingSchema>;

/**
 * How an exemplar came to be one. Jev takes no conversational turns, so a few-shot example has
 * to live in the question definition — and an example is only worth carrying if the document
 * says whose judgement it records.
 *
 * `standing` is the panel's own tally and no ruling at all. It exists so an unruled record
 * cannot be passed off as the operator's taste: the four ruled provenances require a word from
 * `RULINGS`, and `standing` requires the tally instead.
 */
export const PROVENANCES = [
  "standing",
  "accepted",
  "rejected",
  "upvoted-rejected",
  "downvoted-accepted",
] as const;
export const ProvenanceSchema = z.enum(PROVENANCES);
export type Provenance = z.infer<typeof ProvenanceSchema>;

export const ExemplarSchema = z.strictObject({
  record: RecordIdSchema,
  provenance: ProvenanceSchema,
  /** The operator's own word on the record, or `null` when he has given none. */
  ruling: z.enum(RULINGS).nullable(),
  /** What the panel made of it, or `null` when the exemplar is a ruling rather than a standing. */
  tally: z.number().int().nullable(),
  /** The record's claim, verbatim. An edited exemplar teaches the edit. */
  text: z.string().min(1),
  /** Why this one is in the bank, in the document's own voice. */
  why: z.string().min(1),
});
export type Exemplar = z.infer<typeof ExemplarSchema>;

export const BankDocumentSchema = z.strictObject({
  kind: RecordKindSchema,
  version: z.number().int().positive(),
  title: z.string().min(1),
  questions: z.array(QuestionSchema).min(1),
  votes: z.array(VoteSchema).min(1),
  routing: z.array(RoutingSchema).length(ROUTING_QUESTIONS.length),
  exemplars: z.array(ExemplarSchema).min(1),
});
export type BankDocument = z.infer<typeof BankDocumentSchema>;

export const BankSchema = z.strictObject({
  about: z.string().min(1),
  /** The bank's own version, which an assessment cites beside the document's. */
  version: z.number().int().positive(),
  documents: z.array(BankDocumentSchema).length(RECORD_KINDS.length),
});
export type Bank = z.infer<typeof BankSchema>;

/**
 * Whether a distribution admits its own threshold. It is a named function because it is the one
 * place the degeneracy band is spelled, and the parser derives every `admitted` flag with it.
 */
export function admits(observed: Distribution): boolean {
  return observed.fires >= ADMISSION_FLOOR_PCT && observed.fires <= ADMISSION_CEILING_PCT;
}

function fires(when: Condition, answer: number | string | undefined): boolean {
  if (answer === undefined) return false;
  switch (when.op) {
    case "at-least":
      return typeof answer === "number" && answer >= when.value;
    case "at-most":
      return typeof answer === "number" && answer <= when.value;
    case "is":
      return answer === when.value;
    case "is-not":
      return answer !== when.value;
  }
}

export interface TallyResult {
  readonly up: number;
  readonly down: number;
  readonly tally: number;
  readonly backed: readonly Voter[];
  readonly objected: readonly Voter[];
}

/**
 * THE TALLY: a sum of independent opinions, with no weights anywhere.
 *
 * It takes votes rather than a bank because the caller has to have chosen which sides it is
 * summing, and it takes `readonly Vote[]` rather than anything wider because that is what keeps
 * a routing question out — `Routing` has no `when` and no `casts`, so it does not typecheck
 * here. A question nobody answered abstains rather than counting against the record: an absent
 * answer is not a negative opinion, and treating it as one would let a truncated response vote.
 */
export function tally(
  votes: readonly Vote[],
  answers: Readonly<Record<string, number | string>>,
): TallyResult {
  const backed: Voter[] = [];
  const objected: Voter[] = [];
  for (const vote of votes) {
    if (!fires(vote.when, answers[vote.question])) continue;
    (vote.casts === "up" ? backed : objected).push(vote.voter);
  }
  return {
    up: backed.length,
    down: objected.length,
    tally: backed.length - objected.length,
    backed,
    objected,
  };
}
