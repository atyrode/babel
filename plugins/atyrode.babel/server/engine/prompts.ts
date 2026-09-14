import {
  MATERIAL_INDEX,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  type MaterialEntry,
} from "../../contract.ts";
import {
  exploreJsonSchema,
  parseExploreResult,
  REFUSALS,
  ResultRefusal,
  type Evidence,
  type ExploreResult,
  type Stage,
} from "../../machine/results.ts";

/*
  THE PROMPT: the whole of what the model is told, composed here from Babel-owned parts, and the
  reading of what comes back. Ported from #284's `machine/engine/prompts.ts`, which was in turn
  ported from `internal/explore/{prompt.go,instructions.go}`. Nothing about how to prompt lives
  in Code: Code owns the model, the thinking level and the account, and Babel owns the words.

  TWO THINGS ARE DIFFERENT FROM #284's VERSION, and both follow from Babel no longer running the
  session (#279).

  THERE ARE NO HOST TOOLS. #284 registered `babel_search`, `babel_fetch` and `babel_submit` on an
  omp pipe Babel held open; a Code session is omp's own job with omp's own tools, and Babel holds
  nothing. So the evidence is not served call by call — it is SEALED, as `prepare`'s own output,
  and bound into the session's sandbox as a read-only directory the model reads with the file
  tools it already has. The prompt's job is to say exactly what is in that directory.

  THE ANSWER IS THE SESSION'S FINAL MESSAGE. There is no submit tool to validate against a
  schema, so the contract is stated in the prompt and read back off the transcript:
  {@link ANSWER_FENCE} in the last message, the result schema for the stage printed above it,
  and {@link answerOf} is the one reader. Both ends live in this file on purpose — the Go tree's
  worst evaluation bug was three copies of one rule (`machine/results.ts` says the whole of it),
  and a prompt that promised one shape while the reader expected another is that bug again.

  ORDER IS LOAD-BEARING FOR COST, not for meaning. A provider can only serve a byte-identical
  prefix from its prompt cache, so the invariant part comes first — the recipe bodies, by far the
  largest block — then the answering protocol, then the stage's schema and instructions, then
  everything that varies per run: the parameters, the material's own index, and the prior records.
  For an exploration that order is also right on its merits: a recipe read after the material is
  a method chosen to fit a conclusion.

  Two sections are machine-readable on purpose. The `[babel-params]` block lists the run's
  parameters one per line, which is how a result can name the identifiers Babel minted for a
  brief, and the material section names each session by the selector Babel filed it under. Both
  are plain text a model reads and a fixture can parse; neither is a protocol.
*/

// ---------------------------------------------------------------------------- versions

/** The job and prompt this module composes, recorded in every receipt (§7). */
export const JOB_VERSION = 3;
export const PROMPT_VERSION = "babel.analysis-prompt/3";

// ---------------------------------------------------------------------------- the answer

/** The fence the answer is carried in, spelled once for the prompt and for the reader. */
export const ANSWER_FENCE = "```json";

/**
 * The fenced JSON block of a final message, or the sentence saying why there is none.
 *
 * The LAST block wins. A model that corrects itself writes a second one, and #284's submit tool
 * had the same rule for the same reason ("calling again replaces the earlier submission"); a
 * reader that took the first would persist the draft and discard the correction.
 */
export function answerOf(finalMessage: string): { json: string } | { refused: string } {
  const opens: number[] = [];
  let at = finalMessage.indexOf(ANSWER_FENCE);
  while (at !== -1) {
    opens.push(at);
    at = finalMessage.indexOf(ANSWER_FENCE, at + ANSWER_FENCE.length);
  }
  for (const open of opens.reverse()) {
    const from = open + ANSWER_FENCE.length;
    const close = finalMessage.indexOf("```", from);
    if (close === -1) continue;
    const body = finalMessage.slice(from, close).trim();
    if (body !== "") return { json: body };
  }
  return {
    refused:
      finalMessage.trim() === ""
        ? "the session ended with no final message at all"
        : `the final message carries no ${ANSWER_FENCE} block, so this run submitted no result`,
  };
}

/**
 * One exploration's answer, read off the session's final message and validated for its stage.
 *
 * It is one function rather than a parse and a validate at the call site because a caller that
 * did them separately would have two chances to report a `schema` refusal as a failure — and a
 * refused submission is SPEND, which only the code that knows it was a submission can say.
 */
export function readExploreAnswer(
  stage: Stage,
  finalMessage: string,
): { result: ExploreResult } | { refusal: ResultRefusal } {
  const answer = answerOf(finalMessage);
  if ("refused" in answer) {
    return { refusal: new ResultRefusal(REFUSALS.schema, answer.refused) };
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
    };
  }
  try {
    return { result: parseExploreResult(stage, payload) };
  } catch (error) {
    if (error instanceof ResultRefusal) return { refusal: error };
    throw error;
  }
}

/**
 * EVERY LOCATOR THIS RESULT CITES, CHECKED AGAINST WHAT BABEL ACTUALLY SERVED — which is the
 * sentence {@link INSTRUCTIONS_EVIDENCE} promises the model, kept here so the promise is true.
 *
 * #284 checked a locator against a served trace the host tools had recorded. There are no host
 * tools now, and there is something better: the material is an immutable selection with a source
 * digest per session, so a locator is admissible exactly when its `path` is one of the files the
 * material's index names and its `digest` is that entry's own source digest. A retyped digest, an
 * edited path or a citation of a session this run was never given is `unknown-reference` — the
 * claim is refused, its siblings are not, and nothing repairs it.
 *
 * The check is over the SELECTION rather than over the sealed bytes, and deliberately: the
 * selection is on the run row, so verifying costs no read of a 12 GB corpus — which is the whole
 * economy this lane was rebuilt for (post-mortem F1).
 */
export function unservedLocator(
  result: ExploreResult,
  sessions: readonly MaterialEntry[],
): string {
  const served = new Map<string, string>();
  for (const entry of sessions) {
    served.set(`${MATERIAL_SESSIONS}/${entry.file}`, entry.sourceDigest);
    served.set(entry.file, entry.sourceDigest);
    served.set(`${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${entry.file}`, entry.sourceDigest);
  }
  for (const evidence of citedEvidence(result)) {
    const digest = served.get(evidence.locator.path);
    if (digest === undefined) {
      return `${evidence.locator.path} is not a file this run was served`;
    }
    if (digest !== evidence.locator.digest) {
      return `${evidence.locator.path} was served at ${digest} and this claim cites ${evidence.locator.digest}`;
    }
  }
  return "";
}

/**
 * Every citation one exploration result carries, wherever the shape allows one: an observation's
 * claim and its counter-evidence, an objection's, a finding's counter-evidence (a finding rests on
 * observations and has no evidence field of its own), and the supporting and conflicting material
 * of both kinds of proposal.
 */
function citedEvidence(result: ExploreResult): readonly Evidence[] {
  const cited: Evidence[] = [];
  for (const candidate of result.candidates) {
    for (const observation of candidate.observations) {
      cited.push(...observation.claim.evidence, ...observation.claim.counter_evidence);
    }
    const remedy = candidate.remedy;
    if (remedy !== undefined) cited.push(...remedy.proposal.supporting, ...remedy.proposal.conflicting);
  }
  for (const objection of result.objections) {
    cited.push(...objection.claim.evidence, ...objection.claim.counter_evidence);
  }
  for (const consolidation of result.consolidations) {
    cited.push(...consolidation.finding.counter_evidence);
    const proposal = consolidation.proposal;
    if (proposal !== undefined) cited.push(...proposal.supporting, ...proposal.conflicting);
  }
  return cited;
}

// ---------------------------------------------------------------------------- parameters

const PARAMS_OPEN = "[babel-params]";
const PARAMS_CLOSE = "[end]";

/**
 * Job parameters Babel sets. A run reads them to know which stage it is running under and which
 * durable records its brief covers; identifiers are all they carry.
 */
export const PARAM = {
  stage: "babel.stage",
  runId: "babel.run",
  preparation: "babel.preparation",
  briefHypotheses: "babel.brief.hypotheses",
  briefObservations: "babel.brief.observations",
  briefObjections: "babel.brief.objections",
} as const;

/** One cookbook recipe as a prompt carries it: verbatim, with the provenance a claim must cite. */
export interface Recipe {
  readonly id: string;
  readonly version: number;
  readonly title?: string | undefined;
  readonly body: string;
}

/** One prior record the refine-first context offers, so a run refines rather than duplicates. */
export interface RelatedRecord {
  readonly kind: string;
  readonly id: string;
  readonly summary: string;
}

/**
 * WHAT THE PROMPT KNOWS ABOUT ONE SESSION BEFORE `prepare` HAS READ IT: the selector Babel filed
 * it under, and the file the material will hold it in (`materialFile` derives both sides' name
 * from the same ordered selectors).
 *
 * The digest, the record count and the size are deliberately NOT here. They come from the pass
 * over the log, which happens inside the `prepare` job that runs after the session is posted, so
 * a prompt stating them would be stating figures nobody had measured — and the digest is the one
 * field a citation must copy exactly. `index.json` is the authority and the prompt says so.
 */
export interface PromptSession {
  readonly selector: string;
  readonly file: string;
}

/** What one exploration prompt is composed from. */
export interface ExplorePromptInput {
  readonly stage: Stage;
  readonly recipes: readonly Recipe[];
  /** The sessions the material will hold, in the order `prepare` was given them. */
  readonly sessions: readonly PromptSession[];
  /** What the preparation is called, so a reader of a claim can find what it was served. */
  readonly preparationId: string;
  readonly params: Readonly<Record<string, string>>;
  readonly related?: { readonly framing: string; readonly records: readonly RelatedRecord[] } | undefined;
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

  parts.push("## How to answer\n\n", ANSWER_PROTOCOL);
  parts.push(
    `${ANSWER_FENCE}\n`,
    `${JSON.stringify(exploreJsonSchema(input.stage), null, 2)}\n`,
    "```\n\n",
  );

  parts.push(`## The ${input.stage} stage\n\n`, stageInstructions(input.stage), "\n");
  parts.push(
    "## Parameters\n\n",
    "Every parameter this run carries, one per line. Comma-separated values are lists.\n\n",
  );
  parts.push(paramsBlock(input.params));
  parts.push(materialSection(input));

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

function paramsBlock(params: Readonly<Record<string, string>>): string {
  const keys = Object.keys(params).sort();
  const lines = keys.map((key) => `${key} = ${params[key] ?? ""}`);
  return `${PARAMS_OPEN}\n${lines.join("\n")}${lines.length === 0 ? "" : "\n"}${PARAMS_CLOSE}\n\n`;
}

/**
 * THE MATERIAL, DESCRIBED EXACTLY AS IT IS ON DISK.
 *
 * The index is described AND its entries are listed, which is not a duplication: the list is what
 * lets a model plan its reading before opening a file, and the index is what it must read to get
 * the digest a citation needs. A run prepared over nothing says so — an empty section would read
 * as a corpus the model failed to find.
 */
function materialSection(input: ExplorePromptInput): string {
  const parts: string[] = ["## The material\n\n"];
  if (input.sessions.length === 0) {
    parts.push(
      `This run was prepared over no sessions, so \`${MATERIAL_ROOT}\` holds an index and nothing `,
      "else. Emit an empty result rather than citing anything.\n\n",
    );
    return parts.join("");
  }
  parts.push(
    `Everything this run may read is at \`${MATERIAL_ROOT}\`, mounted read-only. Read it with `,
    "your own file tools; there is nothing else to search, no network to fetch from, and no ",
    "other corpus on this machine that belongs to this run.\n\n",
  );
  parts.push(
    "```\n",
    `${MATERIAL_ROOT}/\n`,
    `  ${MATERIAL_INDEX}          the selection: {"schema", "preparationId", "preparedAt", "machineId",\n`,
    `                      "sessions": [{"selector", "harness", "sourceId", "captureDigest",\n`,
    `                                    "sourceDigest", "file", "records", "bytes"}]}\n`,
    `  ${MATERIAL_SESSIONS}/<file>   one file per session named by that entry's "file", one canonical\n`,
    `                      JSON record per line, in the order the harness wrote them\n`,
    "```\n\n",
  );
  parts.push(
    `Read \`${MATERIAL_INDEX}\` first: it is the only place the digest a citation needs is written, `,
    `and \`${MATERIAL_SESSIONS}/<file>\` is where that session's records are. The preparation is `,
    `\`${input.preparationId}\` and it is immutable — these are the exact bytes every later reader `,
    "of your claims will recover.\n\n",
  );
  for (const entry of input.sessions) {
    parts.push(`- ${entry.selector} — \`${MATERIAL_SESSIONS}/${entry.file}\`\n`);
  }
  parts.push("\n");
  return parts.join("");
}

/**
 * The prose half of the answering contract: where the answer goes, what replaces a submit tool's
 * validation, and what happens to a result Babel refuses. It is invariant text, which is why it
 * sits above the stage's own schema in the composed prompt.
 */
const ANSWER_PROTOCOL =
  `End your last message with one \`${ANSWER_FENCE}\` fenced block holding the complete result ` +
  "and nothing after the closing fence. The block is the answer: there is no tool to call and no " +
  "other channel, and prose outside it is read by a person and never by Babel. If you write more " +
  "than one such block, the last one is taken, so a correction replaces a draft by being written " +
  "after it.\n\n" +
  "The block must match the schema below exactly. Babel validates it when the session ends — the " +
  "shape, the refs within the result, the recipes cited, and every evidence locator against what " +
  "this run was served — and a refusal is recorded with its reason and costs the deployment the " +
  "run. There is no second chance after the session ends, so check the block before you finish.\n\n" +
  "Emit only what the material supports. An empty object is a valid result when there is nothing " +
  "to report, and it is a better answer than one padded to look productive.\n\n";

/**
 * The prose half of a stage's output contract: what the schema cannot say about how its fields are
 * filled. It is Babel's text because the rules it states are the ones Babel enforces —
 * provenance, authority, the development path — and it names parameter keys and paths, never a
 * value from the run.
 */
export function stageInstructions(stage: Stage): string {
  const blocks = [INSTRUCTIONS_COMMON];
  if (stage === "explore" || stage === "challenge") blocks.push(INSTRUCTIONS_EVIDENCE);
  blocks.push(STAGE_BLOCKS[stage]);
  if (stage === "explore" || stage === "synthesize") blocks.push(INSTRUCTIONS_PROPOSALS);
  blocks.push(INSTRUCTIONS_QUESTIONS, INSTRUCTIONS_DISPOSITIONS);
  return blocks.join("");
}

const INSTRUCTIONS_COMMON = `You are running one stage of a Babel exploration; the "${PARAM.stage}" job parameter names it.

Nothing is forced. Emit only what the material supports; an empty object is a valid result when there is nothing to report. Babel records what you emit and persists every candidate before anything else, so an item you are unsure of is better deferred with a reason than omitted or overstated.

Every "ref" is your own short label for an item (for example "c1", "o2", "con1"). Refs are unique across the whole result, not only within their list, and they are how later items in the same result name earlier ones. Durable identifiers Babel listed in the "${PARAM.briefHypotheses}", "${PARAM.briefObservations}" and "${PARAM.briefObjections}" parameters may be named wherever a ref may be.

Every "recipe" is one of the recipes above, copied as {"id", "version"} exactly. A claim citing a recipe this run did not select is refused.

Gradings are coarse on purpose: "confidence" and "impact" are "low", "moderate" or "high", and confidence is never a substitute for evidence. "novelty" and "priority" are numbers in [0, 1] used for ordering only; they never decide whether a candidate exists.

`;

const INSTRUCTIONS_EVIDENCE = `Evidence is a locator plus a note, and the locator must name bytes this run was served. Those bytes are in the material and nowhere else: "path" is the session's own file as \`${MATERIAL_INDEX}\` names it (\`${MATERIAL_SESSIONS}/<file>\`), "line" is the 1-based line of the record you read in that file, "byte_offset" is 0 unless you can state a real offset within it, and "digest" is that entry's "sourceDigest", copied from the index unchanged. The "note" says in one sentence what those bytes show.

Babel verifies every locator against the selection before persisting the claim. A path the index does not name, or a digest that is not the one the index records for it, makes that claim a recorded refusal; nothing repairs it, and the items beside it are unaffected. Do not cite anything outside the material — not a file elsewhere on the machine, not a document from the network, not a record you remember.

An observation's "claim" carries at least one evidence locator and states its counter-evidence position: either "counter_evidence" is a non-empty list of locators or "counter_evidence_absent" is true, never both and never neither. "temporal_status" is set only when you assessed whether the claim still holds now.

`;

const STAGE_BLOCKS: Record<Stage, string> = {
  explore: `This is the discovery and development stage. Emit each idea as a candidate in your own wording under "candidates", with the cues that provoked it and provisional labels where they help. Develop a candidate by attaching "observations", each a provenance-bearing claim against served evidence; leave "observations" empty for a candidate you surface but do not develop, and list it under "deferred" or "rejected" with your reason. A rejected candidate keeps its record; only its lifecycle changes.

Consolidate when observations in this result recur or reinforce each other: a "consolidations" entry names the observation refs (or brief observation identifiers) it rests on, states the pattern and why it matters, and gives its own counter-evidence position. Only locator-backed observations can be consolidated; a candidate is never evidence.

`,
  challenge: `This is the challenge stage. The "${PARAM.briefHypotheses}" and "${PARAM.briefObservations}" parameters list the candidates and the developed claims under review. Emit criticism under "objections", each naming the hypothesis it attacks by its identifier and resting on exactly one of the four grounds: "evidence" when served bytes contradict the claim, in which case the objection's claim cites them as evidence; "consequence", "missing-check" or "alternative" otherwise, in which case the claim's "evidence" is an empty list and Babel records the objection as a contradicting candidate rather than an observation. Never infer character, ability, emotion or intent.

You may add candidates of your own under "candidates" when the review suggests a hypothesis nobody stated. You cannot develop observations, suggest remedies, consolidate or schedule in this stage, and the schema offers no field for them.

`,
  synthesize: `This is the synthesis stage. The "${PARAM.briefObservations}" parameter lists the developed, locator-backed observations to consolidate and "${PARAM.briefObjections}" the recorded criticism; "${PARAM.briefHypotheses}" lists the candidates they belong to. Read them, read the material as needed, and prioritise consolidation over addition: a "consolidations" entry naming brief observation identifiers that recur or reinforce each other is the output this stage exists for, and a new candidate under "candidates" is secondary. Weigh objections when consolidating; a finding that ignores recorded criticism is not consolidated. A finding states the pattern, why it matters, the scope it was consolidated across, and its counter-evidence position.

You cannot develop observations or object in this stage, and the schema offers no field for them.

`,
};

const INSTRUCTIONS_PROPOSALS = `A proposal is a suggested change and is never required. Attach one to a consolidation as "proposal" when the finding justifies it, or to a candidate as "remedy" when the candidate says what should change as well as what is the case; a candidate that only states what is the case emits no remedy. A proposal names its problem, its outcome, its impact, its classification ("private", "redaction-required" or "public-safe"), and any risks, open questions, prerequisites and verification criteria. Its "supporting" and "conflicting" material are evidence citations under the same rule as every other locator. Babel renders proposals for an operator to review; nothing you propose is applied.

`;

const INSTRUCTIONS_QUESTIONS = `"questions" are the things the material cannot settle and a person can. Raise one when an answer would change what you conclude and no amount of further reading would produce it: which of two machines a service actually runs on, whether a convention the transcripts disagree about is still in force, what a repository is for. Each names its "subjects" — the machines, repositories, services or projects it is about, written the way the material names them — states its "prompt" and its "why_asked", and names under "hypothesis" the candidate it holds up, when it holds one up.

Babel resolves each subject against the operator's own record of their world. A subject that record has never heard of is a refused question, and refusing it is correct: nothing you write creates a machine or a repository, and a question about a thing nobody has declared has nobody to route it to. Ask about what the material names, not about what you would like to exist.

A question is a request that someone else settle something, so it is the one output here that carries no claim: it asserts nothing, decides nothing, and is answered only by the operator. Never answer one yourself, never treat an answer you imagine as evidence, and never raise one in place of a reading you could have done.
`;

const INSTRUCTIONS_DISPOSITIONS = `"dispositions" propose what an operator could do next with a record: "draft-issue" (which requires "workspace", the local checkout the issue is about), "propose-reality-fact", "store-memory", "ask-question" or "develop-further". They render as choices, never as actions, and are optional everywhere they appear.
`;
