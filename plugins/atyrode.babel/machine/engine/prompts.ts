/*
  THE PROMPT: the whole of what the model is told, composed here from Babel-owned parts. Ported
  from internal/explore/{prompt.go,instructions.go,reviewprompt.go}. Nothing about how to prompt
  lives in Code; Code forwards the engine and the engine reads this.

  Order is load-bearing for cost, not for meaning. A provider can only serve a byte-identical
  prefix from its prompt cache, so the invariant part comes first — the recipe bodies, by far the
  largest block — then the answering protocol, and everything that varies after it: the stage's or
  role's own instructions, its tools, the parameters naming this run, the sessions it was prepared
  over, and last the material it is judging. For a review that order is also the right one on its
  merits: a recipe read after the record is a method chosen to fit a conclusion.

  Two sections are machine-readable on purpose. The `[babel-params]` block lists the run's
  parameters one per line, which is how a result can name the identifiers Babel minted for a
  brief, and the sources section names each session by the selector the search tool filters on.
  Both are plain text a model reads and a fixture can parse; neither is a protocol.

  What this composer will not do: render a tally, a rank or an earlier evaluation into a blinded
  assessment's prompt, and ask the model to ignore what it read. Blinding is what Babel served,
  not an instruction about attention — `blindedLeak` audits the bytes about to be sent and the
  caller refuses the launch rather than adding a sentence about forgetting.
*/

import { TOOL_FETCH, TOOL_SEARCH, TOOL_SUBMIT, type HostTool } from "./client.ts";
import type { Role, Stage } from "./results.ts";

// ---------------------------------------------------------------------------- versions

/** The job and prompt this module composes, recorded in every receipt (§7). */
export const JOB_VERSION = 2;
export const PROMPT_VERSION = "babel.analysis-prompt/2";
export const REVIEW_JOB_VERSION = 1;
export const REVIEW_PROMPT_VERSION = "babel.evaluation-prompt/1";
/** Which blinding rules were applied: what the projection withholds and which parameters are held. */
export const REVIEW_BLINDING_POLICY_VERSION = "babel.evaluation-blinding/1";

// ---------------------------------------------------------------------------- parameters

const PARAMS_OPEN = "[babel-params]";
const PARAMS_CLOSE = "[end]";

/**
 * Job parameters Babel sets. A worker reads them to know which stage or role it is running under
 * and which durable records its brief covers; identifiers are all they carry.
 */
export const PARAM = {
  stage: "babel.stage",
  briefHypotheses: "babel.brief.hypotheses",
  briefObservations: "babel.brief.observations",
  briefObjections: "babel.brief.objections",
  reviewRole: "babel.review.role",
  reviewSubjectKind: "babel.review.subject.kind",
  reviewSubjectID: "babel.review.subject.id",
  reviewAssignment: "babel.review.assignment",
  reviewPolicy: "babel.review.policy",
  reviewContext: "babel.review.context",
  reviewBlinded: "babel.review.blinded",
  /** Withheld from a blinded job: a lane reserved for never-reviewed work says something about
   * the target's prior evaluations, which is exactly what a blinded assessment may not be told. */
  reviewLane: "babel.review.lane",
} as const;

/** One cookbook recipe as a prompt carries it: verbatim, with the provenance a claim must cite. */
export interface Recipe {
  id: string;
  version: number;
  title?: string;
  body: string;
}

/** One approved input the run may read. */
export interface Source {
  kind: string;
  selector: string;
  digest?: string;
  snapshot?: string;
}

/** One prior record the refine-first context offers, so a run refines rather than duplicates. */
export interface RelatedRecord {
  kind: string;
  id: string;
  summary: string;
}

function paramsBlock(params: Readonly<Record<string, string>>): string {
  const keys = Object.keys(params).sort();
  const lines = keys.map((key) => `${key} = ${params[key] ?? ""}`);
  return `${PARAMS_OPEN}\n${lines.join("\n")}${lines.length === 0 ? "" : "\n"}${PARAMS_CLOSE}\n\n`;
}

function toolsBlock(tools: readonly HostTool[]): string {
  if (tools.length === 0) return "";
  const lines = tools.map((tool) => `- \`${tool.name}\`: ${tool.description}`);
  return `## Tools\n\n${lines.join("\n")}\n\n`;
}

// ---------------------------------------------------------------------------- exploration

/** What one exploration prompt is composed from. */
export interface ExplorePromptInput {
  stage: Stage;
  recipes: readonly Recipe[];
  sources: readonly Source[];
  params: Readonly<Record<string, string>>;
  tools: readonly HostTool[];
  related?: { framing: string; records: readonly RelatedRecord[] };
}

/** Renders one stage's prompt. */
export function composeExplorePrompt(input: ExplorePromptInput): string {
  const parts: string[] = ["# Babel analysis\n\n"];

  parts.push(
    "## Recipes\n\n",
    "The cookbook recipes selected for this stage, verbatim. Cite one by its id and version in every claim.\n\n",
  );
  for (const recipe of input.recipes) {
    parts.push(`### ${recipe.title ?? recipe.id} (id ${recipe.id}, version ${recipe.version})\n\n`);
    parts.push(`${recipe.body.trim()}\n\n`);
  }

  parts.push(
    "## How to answer\n\n",
    `Work with the tools below, then call \`${TOOL_SUBMIT}\` with the complete result. Its arguments are `,
    "validated against the result schema before Babel sees them, and Babel then checks the refs within the ",
    "result, the recipes cited, and every evidence locator against what this run was served; a refusal ",
    "explains what to fix, and calling again replaces the earlier submission. End your turn once the ",
    "submission is accepted. Do not write the result as prose.\n\n",
  );

  parts.push(`## The ${input.stage} stage\n\n`, stageInstructions(input.stage), "\n");
  parts.push(toolsBlock(input.tools));
  parts.push("## Parameters\n\n", "Every parameter this run carries, one per line. Comma-separated values are lists.\n\n");
  parts.push(paramsBlock(input.params));

  parts.push("## Sources\n\n");
  if (input.sources.length === 0) {
    parts.push("This run was prepared over no sessions.\n\n");
  } else {
    parts.push(
      "The sessions this run was prepared over, as `harness/source_id` with the capture digest. The search ",
      "tool reads these and nothing else.\n\n",
    );
    for (const source of input.sources) {
      parts.push(`- ${source.kind} ${source.selector}${source.digest ? ` (${source.digest})` : ""}\n`);
    }
    parts.push("\n");
  }

  const related = input.related;
  if (related !== undefined && related.records.length > 0) {
    parts.push("## Prior records\n\n", `${related.framing}\n\n`);
    for (const record of related.records) {
      parts.push(`- ${record.kind} ${record.id}: ${record.summary}\n`);
    }
    parts.push("\n");
  }
  return parts.join("");
}

/**
 * The prose half of a stage's output contract: what the schema cannot say about how its fields are
 * filled. It is Babel's text because the rules it states are the ones Babel enforces —
 * provenance, authority, the development path — and a worker paraphrasing them would be a second
 * statement that could drift. It names parameter keys and tool names, never a value from the run.
 */
export function stageInstructions(stage: Stage): string {
  const blocks = [INSTRUCTIONS_COMMON];
  if (stage === "explore" || stage === "challenge") blocks.push(INSTRUCTIONS_EVIDENCE);
  blocks.push(STAGE_BLOCKS[stage]);
  if (stage === "explore" || stage === "synthesize") blocks.push(INSTRUCTIONS_PROPOSALS);
  blocks.push(INSTRUCTIONS_QUESTIONS, INSTRUCTIONS_DISPOSITIONS);
  return blocks.join("");
}

const INSTRUCTIONS_COMMON = `You are running one stage of a Babel exploration; the "${PARAM.stage}" job parameter names it. Your result is one JSON document matching the supplied schema, and every submission is the complete result as it stands: resubmitting replaces the previous document rather than adding to it, so include everything you want kept each time.

Nothing is forced. Emit only what the material supports; an empty object is a valid result when there is nothing to report. Babel records what you emit and persists every candidate before anything else, so an item you are unsure of is better deferred with a reason than omitted or overstated.

Every "ref" is your own short label for an item (for example "c1", "o2", "con1"). Refs are unique across the whole result, not only within their list, and they are how later items in the same result name earlier ones. Durable identifiers Babel listed in the "${PARAM.briefHypotheses}", "${PARAM.briefObservations}" and "${PARAM.briefObjections}" parameters may be named wherever a ref may be.

Every "recipe" is one of the recipes the job document lists, copied as {"id", "version"} exactly. A claim citing a recipe this job did not select is refused.

Gradings are coarse on purpose: "confidence" and "impact" are "low", "moderate" or "high", and confidence is never a substitute for evidence. "novelty" and "priority" are numbers in [0, 1] used for ordering only; they never decide whether a candidate exists.

`;

const INSTRUCTIONS_EVIDENCE = `Evidence is a locator plus a note, and the locator must be one Babel served to this run, copied verbatim. For a corpus hit served by the "${TOOL_SEARCH}" tool, copy the hit's "locator" object as it was served: "path", "line", "byte_offset" and "digest", every field unchanged. For a document served by the "${TOOL_FETCH}" tool, the locator is {"path": the document's source "url", "line": 0, "byte_offset": 0, "digest": the document's "digest"}, again copied exactly. A frontier search returns Babel's own prior records; they carry identifiers, not locators, and are never evidence. The "note" says in one sentence what those bytes show.

Babel verifies every locator against what it served before persisting the claim. A locator it did not serve — an edited path, a retyped digest, a line you did not receive — makes that claim a recorded refusal; nothing repairs it, and the items beside it are unaffected. Do not cite anything you have not been served in this run.

An observation's "claim" carries at least one evidence locator and states its counter-evidence position: either "counter_evidence" is a non-empty list of locators or "counter_evidence_absent" is true, never both and never neither. "temporal_status" is set only when you assessed whether the claim still holds now.

`;

const STAGE_BLOCKS: Record<Stage, string> = {
  explore: `This is the discovery and development stage. Emit each idea as a candidate in your own wording under "candidates", with the cues that provoked it and provisional labels where they help. Develop a candidate by attaching "observations", each a provenance-bearing claim against served evidence; leave "observations" empty for a candidate you surface but do not develop, and list it under "deferred" or "rejected" with your reason. A rejected candidate keeps its record; only its lifecycle changes.

Consolidate when observations in this result recur or reinforce each other: a "consolidations" entry names the observation refs (or brief observation identifiers) it rests on, states the pattern and why it matters, and gives its own counter-evidence position. Only locator-backed observations can be consolidated; a candidate is never evidence.

`,
  challenge: `This is the challenge stage. The "${PARAM.briefHypotheses}" and "${PARAM.briefObservations}" parameters list the candidates and the developed claims under review. Emit criticism under "objections", each naming the hypothesis it attacks by its identifier and resting on exactly one of the four grounds: "evidence" when served bytes contradict the claim, in which case the objection's claim cites them as evidence; "consequence", "missing-check" or "alternative" otherwise, in which case the claim's "evidence" is an empty list and Babel records the objection as a contradicting candidate rather than an observation. Never infer character, ability, emotion or intent.

You may add candidates of your own under "candidates" when the review suggests a hypothesis nobody stated. You cannot develop observations, suggest remedies, consolidate or schedule in this stage, and the schema offers no field for them.

`,
  synthesize: `This is the synthesis stage. The "${PARAM.briefObservations}" parameter lists the developed, locator-backed observations to consolidate and "${PARAM.briefObjections}" the recorded criticism; "${PARAM.briefHypotheses}" lists the candidates they belong to. Read them, search the corpus and the frontier as needed, and prioritise consolidation over addition: a "consolidations" entry naming brief observation identifiers that recur or reinforce each other is the output this stage exists for, and a new candidate under "candidates" is secondary. Weigh objections when consolidating; a finding that ignores recorded criticism is not consolidated. A finding states the pattern, why it matters, the scope it was consolidated across, and its counter-evidence position.

You cannot develop observations or object in this stage, and the schema offers no field for them.

`,
};

const INSTRUCTIONS_PROPOSALS = `A proposal is a suggested change and is never required. Attach one to a consolidation as "proposal" when the finding justifies it, or to a candidate as "remedy" when the candidate says what should change as well as what is the case; a candidate that only states what is the case emits no remedy. A proposal names its problem, its outcome, its impact, its classification ("private", "redaction-required" or "public-safe"), and any risks, open questions, prerequisites and verification criteria. Its "supporting" and "conflicting" material are evidence citations under the same rule as every other locator. Babel renders proposals for an operator to review; nothing you propose is applied.

`;

const INSTRUCTIONS_QUESTIONS = `"questions" are the things the corpus cannot settle and a person can. Raise one when an answer would change what you conclude and no amount of further searching would produce it: which of two machines a service actually runs on, whether a convention the transcripts disagree about is still in force, what a repository is for. Each names its "subjects" — the machines, repositories, services or projects it is about, written the way the material names them — states its "prompt" and its "why_asked", and names under "hypothesis" the candidate it holds up, when it holds one up.

Babel resolves each subject against the operator's own record of their world. A subject that record has never heard of is a refused question, and refusing it is correct: nothing you write creates a machine or a repository, and a question about a thing nobody has declared has nobody to route it to. Ask about what the material names, not about what you would like to exist.

A question is a request that someone else settle something, so it is the one output here that carries no claim: it asserts nothing, decides nothing, and is answered only by the operator. Never answer one yourself, never treat an answer you imagine as evidence, and never raise one in place of a search you could have run.
`;

const INSTRUCTIONS_DISPOSITIONS = `"dispositions" propose what an operator could do next with a record: "draft-issue" (which requires "workspace", the local checkout the issue is about), "propose-reality-fact", "store-memory", "ask-question" or "develop-further". They render as choices, never as actions, and are optional everywhere they appear.
`;

// ---------------------------------------------------------------------------- evaluation

/** What one review prompt is composed from. */
export interface ReviewPromptInput {
  role: Role;
  recipe: Recipe;
  /** The projection of the record under review. A blinded one has no field for what is withheld. */
  target: unknown;
  alternatives?: readonly unknown[];
  previous?: readonly unknown[];
  /** The filing pass's ledger material: what already exists, and what the operator asked. */
  ledger?: unknown;
  /** The backlog pass's material: the deferred candidate, its observations, its siblings. */
  backlog?: unknown;
  sources: readonly Source[];
  params: Readonly<Record<string, string>>;
  tools: readonly HostTool[];
  blinded: boolean;
}

/** Renders one review's prompt. */
export function composeReviewPrompt(input: ReviewPromptInput): string {
  const parts: string[] = ["# Babel evaluation\n\n"];

  parts.push("## Recipe\n\n", "The cookbook recipe that states this review's method, verbatim.\n\n");
  parts.push(`### ${input.recipe.id} (version ${input.recipe.version})\n\n`, `${input.recipe.body.trim()}\n\n`);

  parts.push(
    "## How to answer\n\n",
    `Read the record below, use the tools if they help, then call \`${TOOL_SUBMIT}\` once with your assessment. `,
    "The arguments are validated against the result schema before Babel sees them, and Babel then checks the ",
    "role's authority, the closed vocabularies, and every evidence locator against what this review was ",
    "served; a refusal explains what to fix, and calling again replaces the earlier submission. End your turn ",
    "once the submission is accepted.\n\n",
    "A bare vote is a complete answer. You are not required to write prose, find new evidence, or propose a ",
    "refinement, and inventing any of them to fill the result would be worse than omitting them. A ",
    "contribution without a vote is equally valid. If you cannot judge this record at all — the subject needs ",
    "a check you cannot make, the evidence is unreachable from here — set `skip` and say why. A skip is ",
    "recorded as a gap, not as an opposing vote, so declining is never the same as judging against it.\n\n",
  );

  parts.push("## Your role\n\n", roleInstructions(input.role), "\n");
  parts.push(toolsBlock(input.tools));
  parts.push("## Parameters\n\n", "Every parameter this review carries, one per line.\n\n", paramsBlock(input.params));

  parts.push("## Sessions\n\n");
  if (input.sources.length === 0) {
    parts.push("This review was given no sessions to search.\n\n");
  } else {
    parts.push("The sessions this review may search, by the selector the search tool filters on.\n\n");
    for (const source of input.sources) parts.push(`- ${source.selector}\n`);
    parts.push("\n");
  }

  parts.push("## The record under review\n\n");
  if (input.blinded) {
    parts.push(
      "This is an initial assessment and it is taken blind. Babel has not shown you how this record has been ",
      "received, how it was ranked, or what any earlier review of it said, and the tools above cannot reach ",
      "them either. Judge the record as it stands.\n\n",
    );
  }
  parts.push("```json\n", JSON.stringify(input.target, null, 2), "\n```\n\n");

  const alternatives = input.alternatives ?? [];
  if (alternatives.length > 0) {
    parts.push(
      "## Alternatives to compare\n\n",
      "Other records addressing the same problem. Comparing them does not merge them: each keeps its own ",
      "record, its own reception and its own decision, and preferring one here is a preference in the ",
      "context you name rather than a vote about either.\n\n",
    );
    for (const alternative of alternatives) {
      parts.push("```json\n", JSON.stringify(alternative, null, 2), "\n```\n\n");
    }
  }

  const previous = input.previous ?? [];
  if (previous.length > 0) {
    parts.push(
      "## Earlier evaluations\n\n",
      "What earlier reviews recorded about this record. They are shown because this role's question is about ",
      "the disagreement itself; they are not a score to agree with, and an earlier reviewer having voted is ",
      "not evidence about the claim.\n\n",
      "```json\n",
      JSON.stringify(previous, null, 2),
      "\n```\n\n",
    );
  }

  if (input.ledger !== undefined) {
    parts.push(
      "## What the ledger already names\n\n",
      "The topics that exist, why some were retired, why some proposals were declined, the repositories the ",
      "sessions this record cites were in, the identities the scan observed that nothing names, and what the ",
      "operator asked and nobody has answered. Prefer an entity that already exists: a topic nobody needed a ",
      "second name for is the one an operator can act on. The retired and declined reasons are why Babel got ",
      "a topic wrong before, and repeating one of them is the failure this material exists to prevent. An ",
      "unbound identity is evidence for a create and only when *this* record is about it; an ask is answered ",
      "rather than obeyed.\n\n",
      "```json\n",
      JSON.stringify(input.ledger, null, 2),
      "\n```\n\n",
    );
  }

  if (input.backlog !== undefined) {
    parts.push(
      "## The deferred candidate\n\n",
      "The candidate this pass was drawn for, its own observations with what stands behind them, the topics ",
      "it is filed under, the siblings an act may name, and the entities and predicates a promotion may use. ",
      "You may name no candidate, observation or entity that is not here.\n\n",
      "```json\n",
      JSON.stringify(input.backlog, null, 2),
      "\n```\n\n",
    );
  }
  return parts.join("");
}

/**
 * One role's static instructions, composed from the blocks its authority admits: the schema says
 * what a role may submit and this says what each field means, so a pruned field never arrives
 * with instructions telling a model to fill it.
 */
export function roleInstructions(role: Role): string {
  const parts = [`You are reviewing one record in the ${role} role.\n\n`, ROLE_QUESTION[role], "\n"];
  if (role === "filing") return `${parts.join("")}${INSTRUCTIONS_FILING}`;
  if (role === "backlog") return `${parts.join("")}${INSTRUCTIONS_BACKLOG}`;
  parts.push(INSTRUCTIONS_REVIEW_COMMON);
  if (role === "reception") parts.push(INSTRUCTIONS_REVIEW_VOTE);
  if (role === "evidence" || role === "outcome") parts.push(INSTRUCTIONS_REVIEW_CRITERIA);
  if (role === "outcome") parts.push(INSTRUCTIONS_REVIEW_OUTCOME);
  if (role === "comparison") parts.push(INSTRUCTIONS_REVIEW_COMPARISON);
  parts.push(INSTRUCTIONS_REVIEW_CONTRIBUTIONS);
  return parts.join("");
}

/**
 * The one question each role answers. They are separate questions on purpose: reception,
 * evidence, relevance and observed outcome are four different things to know about a record, and
 * a role that blurred two of them would produce an answer that means neither.
 */
const ROLE_QUESTION: Record<Role, string> = {
  reception:
    "The question is whether you support, oppose or are unsure about this record as it stands. That is a " +
    "judgement about the idea, not a measurement of its evidence, not a probability that it is correct, and " +
    "not a prediction of whether the operator will accept it.\n",
  evidence:
    "The question is whether the evidence this record cites actually supports what it claims. Check the " +
    "locators. An evidence check is not a vote: report what the cited records do and do not show, and leave " +
    "the reception judgement to the reception role.\n",
  challenge:
    "The question is what the strongest objection to this record is, given what earlier reviews already " +
    "said. Ground the objection in evidence, a consequence, a missing check or a concrete alternative. You " +
    "are diagnosing a disagreement, not voting until it resolves.\n",
  comparison:
    "The question is how this record compares with the alternatives addressing the same problem. Say why " +
    "now, what objection remains unresolved, and what would change the recommendation where you know. " +
    "Prefer one in a named context if you can; do not merge them and do not vote.\n",
  outcome:
    "The question is what actually happened: whether this was implemented, whether the promised outcome was " +
    "observed, and against which criteria. A merge is not a deployment and a deployment is not proof of the " +
    "promised outcome. Missing, partial or conflicting evidence is not success.\n",
  relevance:
    "The question is whether this record is relevant to the recorded work, pain and constraints shown with " +
    "it. Relevance is not quality and not reception: a correct finding about something nobody is working on " +
    "is correct and not relevant, and saying so is the useful answer.\n",
  filing:
    "The question is what this record is about: which topic a reader would look for it under, and whether " +
    "the ledger's topics themselves should change for it to be findable. A topic is a thing in the world " +
    "with a name and a binding — a repository, a project, a machine, a service, a concept — and never a " +
    "folder, a directory or the workspace the work happened in. You are not judging this record, and no " +
    "part of your answer is a vote.\n",
  backlog:
    "The question is what should become of this deferred candidate: whether it and others say one thing a " +
    "finding should say, whether a newer candidate already says it better, whether it should be retired " +
    "with a reason, whether one of its observations is a durable fact about something the ledger names, or " +
    "whether it is worth keeping exactly as it is. You are not judging the candidate and no part of your " +
    "answer is a vote, and nothing you say changes anything: every act is a proposal the operator rules " +
    "on.\n",
};

const INSTRUCTIONS_REVIEW_COMMON = `
Judge the exact wording you were shown. The identity in the parameters names
one immutable revision of this record; if a newer revision exists, your
assessment is about the one here and Babel binds it to that revision rather
than moving it forward.

Cite only locators Babel served you. Every evidence item you submit is checked
against this review's own served trace, and a locator you did not receive is
refused whether or not its bytes exist.

Say what you are unsure about in \`uncertainty\`. An unknown recorded is worth
more than a rationale invented to fill the field.
`;

const INSTRUCTIONS_REVIEW_VOTE = `
Set \`vote\` to support, oppose or unsure. Omit it if you have nothing to say
about reception and are only contributing. Support means you think the record
should be acted on as it stands; oppose means you think it should not; unsure
means you have read it and cannot tell, which is a real answer and not a
missing one.
`;

const INSTRUCTIONS_REVIEW_CRITERIA = `
Record one entry in \`results\` per criterion you checked, naming the criterion
id from the record above. Satisfied requires evidence: a criterion you believe
holds but cannot cite is unsatisfied with your uncertainty recorded, not
satisfied on your word. A criterion you did not check gets no entry.
`;

const INSTRUCTIONS_REVIEW_OUTCOME = `
Set \`outcome\` only from evidence you cite: implemented, verified, partial,
contradicted or unverifiable. Verified means every criterion you listed is
satisfied and evidenced. Unverifiable is the honest answer when the checks that
would settle it are ones you cannot make from here, and it is strictly better
than a guess at verified; say in \`uncertainty\` what you could not check. Any
outcome or criterion result also needs \`environment\`, the setting you
observed, and \`as_of\`, when you observed it: a result with no stated scope
reads as a claim about every setting at every time.
`;

const INSTRUCTIONS_REVIEW_COMPARISON = `
A comparison contribution names at least two alternatives and may name one as
preferred. The preference holds in the context you state in the text and
nowhere else: it mints no vote for either record, merges nothing, and rules on
nothing. Preferring a record your own run authored is refused.
`;

const INSTRUCTIONS_REVIEW_CONTRIBUTIONS = `
Contributions are optional. Each has a kind: comment, argument, objection,
evidence, refinement or comparison. An evidence contribution must cite at least
one locator; a comparison must name its alternatives; every other kind carries
text or evidence. Use \`would_change\` to say what would change your mind where
you know it, and leave it empty where you do not.
`;

const INSTRUCTIONS_FILING = `
Answer with exactly one of four fields.

\`filing\` files this record under a topic that already exists. Name the entity
by any name or alias it is listed under and say in \`rationale\` why this record
is about it. This is the answer to prefer: a second topic for something the
ledger already names is a merge the operator has to do by hand.

\`topic\` proposes a change to the ledger's topics. It is a proposal for the
operator and never a change: he reads it and accepting it is what applies it.
Set \`operation\` to exactly one of four:

- \`create\` — the record is about something no listed entity names. Give the
  \`name\`, the \`kind\`, an \`identity\` that is the same string for the same
  thing every time — a normalized remote, a common directory, a hostname, or a
  slug for a concept — and bind it to something real with \`remote\`, \`paths\`
  or a one-sentence \`definition\`. Name no targets.
- \`split\` — one listed topic names two things. Put that topic in \`targets\`,
  and describe the part that would be separated out with the same name, kind,
  identity and binding a create carries. This record is what moves to the new
  part, so split only when this record belongs to the part you are describing.
- \`merge\` — two listed topics name one thing. Put both in \`targets\`, the one
  that disappears first and the one that survives second, and carry no name,
  kind or identity: nothing is created.
- \`retire\` — one listed topic should never have existed. Put it in
  \`targets\`, and remember that retiring it re-queues everything filed under it
  for triage, so the reason has to be that the topic is wrong rather than that
  it is quiet.

Every operation needs \`reasoning\`: why the ledger should change, and why the
topics in \`considered\` were weighed and rejected. Every \`targets\` entry must
be a topic listed above — an invented one is refused as a malformed result and
creates nothing.

An identity listed under the unbound identities is evidence for a \`create\`,
and only when *this* record is about it. The counts say how much stands behind
it; the fact that Babel saw a repository is not a reason to name it while
reviewing a record about something else.

An ask listed under the operator's asks is answered rather than obeyed. If you
agree with it, answer with the \`topic\` proposal it calls for and set
\`ask_id\` to that ask. If you judge it wrong — the topics it names are one
thing, the split it wants would cut across what the records actually say —
answer with \`no_change\`, naming the \`ask_id\` and the \`reason\`, which lands
as a reply where he asked. Answer only the asks that concern this record's
topics.

\`no_topic\` records that the record is about nothing in particular, with the
reason. Some outputs are about the process, about a passing question, about
nothing an operator would ever go looking for by name, and saying so is the
honest result. It is not a failure and it is not a skip: a skip means you could
not read the record, and this means you read it and it has no topic.

A name you use in \`filing\` that no listed entity answers to becomes a create
proposal rather than a filing, so guessing at a name costs the operator a
proposal to decline. Set \`skip\` only when the record itself is unreadable
from here.
`;

const INSTRUCTIONS_BACKLOG = `
Answer with exactly one of five fields. Every one of the first four is a
proposal the operator rules on, published through Babel's ordinary chain and
applied by his acceptance and by nothing else. Nothing you say here settles
anything by itself, and nothing is ever deleted: a candidate that is
consolidated, superseded or retired keeps its record, its observations and its
history, and gains one appended status event saying a later record speaks for
it.

\`consolidate\` says that this candidate and others beside it are evidence for
one thing, and that a finding should say it. Name every candidate in
\`hypotheses\` — the drawn candidate is folded whether or not you list it — and
write the \`finding\` with its \`title\`, the \`pattern\` the observations
share, \`why_it_matters\` and the \`scope\` it holds in. Prefer consolidating
into what an existing finding already says rather than minting a second finding
a reader would have to reconcile.

\`supersede\` says a newer candidate states the same thing better. Set \`by\` to
that candidate and say in \`reason\` what it says better. Only a candidate
listed beside this one qualifies, and only one that genuinely says the *same*
thing: a candidate that says something adjacent is a second claim, and
superseding with it would lose the question this one was asking.

\`retire\` says the candidate is not worth returning to, with a \`reason\` a
reader could check. "The service it describes was decommissioned and no
observation was ever recorded against it" is checkable. "Low value", "stale",
"not interesting" and "superseded by later work" with no candidate named are
gradings, and a grading is not a reason. Nothing is stale by a clock: age alone
is never a retirement.

\`promote\` says one of this candidate's observations is a durable fact about
something the ledger already names. Set \`observation\` to that claim,
\`entity\` to the entity by any name or alias it is listed under, \`predicate\`
and \`value\` to the fact in the ledger's own closed vocabulary, and \`reason\`
to why it is durable. Durable is the whole test: a fact is what stays true
until something changes it — where a repository lives, what a service runs on,
whether a project is dormant — and not what was observed once in one session.
An entity no listed name answers to is refused rather than created, because
only the operator creates one.

\`keep\` says the candidate is worth keeping exactly as it is, with the reason.
It is a complete answer and frequently the right one: a backlog full of open
questions nobody has had time for is a healthy backlog, and an act invented to
avoid answering \`keep\` costs the operator a ruling on something that should
not have moved.

Every identifier you use must be one you were shown. A candidate, an
observation or an entity the material does not hold is refused as a malformed
result and creates nothing. Set \`skip\` only when the material itself is
unreadable from here — being unable to choose an act is \`keep\` with the
reason, not a skip.
`;

// ---------------------------------------------------------------------------- the blind

/**
 * Keys whose presence in a blinded projection is a leak. §4.12 blinds an initial reception,
 * evidence, outcome or relevance assessment from the tallies, ranks and earlier evaluations its
 * target has collected — not because a model can be made to forget, but because Babel can be held
 * to what it served. The audit is over the bytes about to be sent, which is the only place that
 * can see it: a projection whose body carried a tally is a refused launch, and the remedy is the
 * projection rather than a prompt asking the model to ignore what it read.
 */
export const REVIEW_BLINDED_KEYS: Record<string, true> = {
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
 * The key a blinded projection leaks through, or "" when it carries none. Keys are checked in
 * sorted order so the key a refusal names does not change between two identical payloads.
 */
export function blindedLeak(value: unknown, path = ""): string {
  if (Array.isArray(value)) {
    for (const [index, item] of value.entries()) {
      const leak = blindedLeak(item, `${path}[${index}]`);
      if (leak !== "") return leak;
    }
    return "";
  }
  if (typeof value !== "object" || value === null) return "";
  const entries = Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  for (const [key, item] of entries) {
    if (REVIEW_BLINDED_KEYS[key] === true) return path === "" ? key : `${path}.${key}`;
    const leak = blindedLeak(item, path === "" ? key : `${path}.${key}`);
    if (leak !== "") return leak;
  }
  return "";
}
