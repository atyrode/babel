import { z } from "zod";
import {
  NextActionSchema,
  RECORD_KINDS,
  RecordIdSchema,
  RecordKindSchema,
  RULINGS,
} from "../../contract.ts";

/*
  THE QUESTION BANK: WHAT JEV IS ASKED, AND WHAT EACH ANSWER IS ALLOWED TO DO.

  A reworded criterion silently changes every vote downstream, so the bank is not a constant in a
  bundle: it is one reviewable document per record kind under `bank/questions/`, each declaring
  its own version, each registered in `bank/versions.json`, and drift between the two is refused
  by `tools/seed-questions.ts` — the same mechanism, and deliberately the same shape, as the
  cookbook's recipes (`babel/tools/seed-recipes.ts`). An assessment cites `kind@version`
  the way a claim cites `recipe@version`, and a citation is only worth writing if the thing it
  names can be read back exactly.

  THIS MODULE IS THE VOCABULARY AND THE SHAPES: what a question, a vote, an advisory, a routing
  question and an exemplar are, and the tally that sums the first of those. `bank/bank.ts` is the
  runtime half and holds the seed the hub ships with; `bank/parse.ts` is the dev-time bridge from
  the documents to that seed. Nothing a hub runs reads a repository file, which is why the
  documents and the shipped artifact are two things.

  THERE ARE THREE BLOCKS BECAUSE THERE ARE THREE OUTPUTS, and which block a question sits in is
  the whole of what its answer may do. A thresholds row casts a VOTE, which moves where the
  record stands. A routing row produces a LABEL, which says where it is filed and whether it may
  be published. An advisory row produces a SUGGESTION — one proposed next action written beside
  the record through `babel.suggest`, which the operator accepts or declines and which moves
  nothing by itself. Collapsing the third into the first would make every question that can only
  propose also able to rank, and the first question to need the block is the one defending
  against a measured ranking bias (#336), so it would rank on exactly the axis it exists to stop
  ranking on.

  FOUR PROPERTIES ARE STRUCTURAL RATHER THAN CONVENTIONAL:

  1. A ROUTING QUESTION CANNOT BE TALLIED. `subject`, `classification` and `contains_instruction`
     say where a record goes and whether it may be published; they are not opinions about its
     merit. They live in `routing`, whose type has no `casts`, no `when` and no `observed` — so
     `tally()`, which takes `readonly Vote[]`, cannot be handed one. The parser closes the other
     direction: a thresholds row naming a routing question is refused, and so is a routing row
     naming anything else, so a routing question cannot become a vote by being written as one.

  2. NEITHER CAN AN ADVISORY, and for the same reason rather than a similar one: `Advisory` has
     no `voter` and no `casts`, so `tally()` will not take one either. The absence is the
     mechanism and `bank.test.ts` holds a compile-time assertion on it, because the way this
     would be lost is somebody adding a convenient `casts` field years from now and turning every
     suggestion in the bank into a vote without noticing.

  3. A THRESHOLD WITHOUT ITS OBSERVED DISTRIBUTION IS REFUSED, and an advisory's cut is a
     threshold. Not marked, not defaulted — refused, by `bank/parse.ts`, naming the row. A
     threshold copied from a vendor's documentation is somebody else's corpus, and a number
     nobody can trace back to this one is the same thing with the citation removed.

  4. A SIDE THAT FIRES ON ALMOST NOTHING OR ALMOST EVERYTHING IS NOT ADMITTED. The study's own
     methodology finding is that a question answering the same way for everything is broken
     rather than calibrated, and it caught three of its own that way. `admitted` is derived from
     the recorded distribution rather than declared, so the two cannot disagree, and `bank.ts`'s
     `votesFor()` and `advisoriesFor()` return the admitted rows alone.

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
 * ONE ADVISORY: a question, the line at which its answer becomes a SUGGESTION, and the next
 * action that suggestion proposes.
 *
 * It is the atom of the third block, and it is deliberately not a `Vote`. There is no `voter`
 * and no `casts`, so `tally()` cannot be handed one — the same structural argument `Routing`
 * rests on, made again because the failure it prevents is worse here: the first question to
 * need this block asks whether a record's confidence outruns its evidence, and the measured
 * reason to ask it is that tone already moves the record's standing by 30 times the assessor's
 * own noise floor. A question correcting for that bias which could itself move standing would
 * be a second helping of the bias with a different name on it.
 *
 * `suggests` is nullable rather than optional, and `none` is how a document writes it: a record
 * an advisory fires on and proposes nothing for is a measured answer, not a missing one.
 *
 * Several rows may name the same question. A `choice` question thresholds one row per option it
 * cares about, exactly as a two-sided voter writes one row per side.
 */
export const AdvisorySchema = z.strictObject({
  question: z.string().min(1),
  /** What firing proposes, in `babel.suggest`'s own vocabulary, or nothing at all. */
  suggests: NextActionSchema.nullable(),
  when: ConditionSchema,
  observed: DistributionSchema,
  /** Derived from `observed.fires`, never declared, by the same rule a vote's side is. */
  admitted: z.boolean(),
});
export type Advisory = z.infer<typeof AdvisorySchema>;

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
  /** The suggestion block, which may be empty: a kind nothing advises on advises nothing. */
  advisories: z.array(AdvisorySchema),
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

/**
 * Whether this advisory fires on these answers, and the ONLY spelling of that question outside
 * the tally. Every voter module asks it rather than re-reading `when.op` itself, because the
 * fourth hand-written copy of a four-armed operator is the one that is wrong, and a comparison
 * that is wrong here produces a confident suggestion about a record nobody judged.
 *
 * A question Jev did not answer does not fire. That is the same rule the tally holds and for
 * the same reason: an absent answer is not a judgement, and the part answers `null` for every
 * absence there is — no binding, no credit, an unreadable response.
 */
export function advises(
  advisory: Advisory,
  answers: Readonly<Record<string, number | string>>,
): boolean {
  return fires(advisory.when, answers[advisory.question]);
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

/**
 * WHETHER THIS ANSWER IS OF THE SHAPE THIS LINE CUTS ON, which is a different question from
 * whether it fires and the one a tally needs in order not to invent an abstention.
 *
 * `fires` answers `false` for an absent answer and for one of the wrong type, because a line that
 * cannot be applied has not been crossed. But "the voter was asked and did not object" and "the
 * voter was never in a position to say anything" are the two facts a position must keep apart:
 * a magnitude cut on a projection that returned the level as the word `"high"` is missing data,
 * and counting it as indifference is how a confident tally gets made out of a truncated reply.
 *
 * So: present, and a finite number where the condition compares magnitudes, or a string where it
 * compares names. Nothing else is readable, and `answersOf` has already dropped every leaf that
 * is neither.
 */
export function readable(when: Condition, answer: number | string | undefined): boolean {
  if (answer === undefined) return false;
  switch (when.op) {
    case "at-least":
    case "at-most":
      return typeof answer === "number";
    case "is":
    case "is-not":
      return typeof answer === "string";
  }
}

/**
 * THE DISTINCT VOTERS A SET OF ROWS SPEAKS FOR, in the order the rows are written.
 *
 * A voter is one opinion however many rows it writes: a two-sided one writes a row per side, and
 * a `choice` one writes a row per option it cares about. The denominator of a tally is therefore
 * the panel and never the row count, and this is the one place that reduction is spelled —
 * `bank.ts`'s `votersFor` is this over the admitted rows of one kind.
 */
export function panel(votes: readonly Vote[]): readonly Voter[] {
  const seen: Voter[] = [];
  for (const vote of votes) if (!seen.includes(vote.voter)) seen.push(vote.voter);
  return seen;
}
