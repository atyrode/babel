package explore

import (
	"strings"

	"github.com/atyrode/babel/internal/worker"
)

// stageInstructions is the prose half of a stage's output contract: what the
// schema cannot say about how its fields are filled. It is Babel's text
// because the rules it states are the ones Babel enforces at persistence —
// provenance, authority, the development path — and a worker that had to
// paraphrase them would be a second statement of them that could drift.
//
// The text is static. It names parameter keys and tool names, never a value
// from the run: the preamble it travels in carries nothing the worker has not
// yet earned, and the brief's identifiers ride in the job parameters.
func stageInstructions(stage Stage, auth authority) string {
	var b strings.Builder
	b.WriteString(instructionsCommon)
	if auth.observations || auth.objections {
		b.WriteString(instructionsEvidence)
	}
	switch stage {
	case StageExplore:
		b.WriteString(instructionsExplore)
	case StageChallenge:
		b.WriteString(instructionsChallenge)
	case StageSynthesize:
		b.WriteString(instructionsSynthesize)
	}
	if auth.remedies || auth.consolidate {
		b.WriteString(instructionsProposals)
	}
	b.WriteString(instructionsQuestions)
	b.WriteString(instructionsDispositions)
	return b.String()
}

const instructionsCommon = `You are running one stage of a Babel exploration; the "` + ParamStage + `" job parameter names it. Your result is one JSON document matching the supplied schema, and every submission is the complete result as it stands: resubmitting replaces the previous document rather than adding to it, so include everything you want kept each time.

Nothing is forced. Emit only what the material supports; an empty object is a valid result when there is nothing to report. Babel records what you emit and persists every candidate before anything else, so an item you are unsure of is better deferred with a reason than omitted or overstated.

Every "ref" is your own short label for an item (for example "c1", "o2", "con1"). Refs are unique across the whole result, not only within their list, and they are how later items in the same result name earlier ones. Durable identifiers Babel listed in the "` + ParamBriefHypotheses + `", "` + ParamBriefObservations + `" and "` + ParamBriefObjections + `" parameters may be named wherever a ref may be.

Every "recipe" is one of the recipes the job document lists, copied as {"id", "version"} exactly. A claim citing a recipe this job did not select is refused.

Gradings are coarse on purpose: "confidence" and "impact" are "low", "moderate" or "high", and confidence is never a substitute for evidence. "novelty" and "priority" are numbers in [0, 1] used for ordering only; they never decide whether a candidate exists.

`

const instructionsEvidence = `Evidence is a locator plus a note, and the locator must be one Babel served to this run, copied verbatim. For a corpus hit served by the "` + worker.ToolSearch + `" tool, copy the hit's "locator" object as it was served: "path", "line", "byte_offset" and "digest", every field unchanged. For a document served by the "` + worker.ToolFetch + `" tool, the locator is {"path": the document's source "url", "line": 0, "byte_offset": 0, "digest": the document's "digest"}, again copied exactly. A frontier search returns Babel's own prior records; they carry identifiers, not locators, and are never evidence. The "note" says in one sentence what those bytes show.

Babel verifies every locator against what it served before persisting the claim. A locator it did not serve — an edited path, a retyped digest, a line you did not receive — makes that claim a recorded refusal; nothing repairs it, and the items beside it are unaffected. Do not cite anything you have not been served in this run.

An observation's "claim" carries at least one evidence locator and states its counter-evidence position: either "counter_evidence" is a non-empty list of locators or "counter_evidence_absent" is true, never both and never neither. "temporal_status" is set only when you assessed whether the claim still holds now.

`

const instructionsExplore = `This is the discovery and development stage. Emit each idea as a candidate in your own wording under "candidates", with the cues that provoked it and provisional labels where they help. Develop a candidate by attaching "observations", each a provenance-bearing claim against served evidence; leave "observations" empty for a candidate you surface but do not develop, and list it under "deferred" or "rejected" with your reason. A rejected candidate keeps its record; only its lifecycle changes.

Consolidate when observations in this result recur or reinforce each other: a "consolidations" entry names the observation refs (or brief observation identifiers) it rests on, states the pattern and why it matters, and gives its own counter-evidence position. Only locator-backed observations can be consolidated; a candidate is never evidence.

`

const instructionsChallenge = `This is the challenge stage. The "` + ParamBriefHypotheses + `" and "` + ParamBriefObservations + `" parameters list the candidates and the developed claims under review. Emit criticism under "objections", each naming the hypothesis it attacks by its identifier and resting on exactly one of the four grounds: "evidence" when served bytes contradict the claim, in which case the objection's claim cites them as evidence; "consequence", "missing-check" or "alternative" otherwise, in which case the claim's "evidence" is an empty list and Babel records the objection as a contradicting candidate rather than an observation. Never infer character, ability, emotion or intent.

You may add candidates of your own under "candidates" when the review suggests a hypothesis nobody stated. You cannot develop observations, suggest remedies, consolidate or schedule in this stage, and the schema offers no field for them.

`

const instructionsSynthesize = `This is the synthesis stage. The "` + ParamBriefObservations + `" parameter lists the developed, locator-backed observations to consolidate and "` + ParamBriefObjections + `" the recorded criticism; "` + ParamBriefHypotheses + `" lists the candidates they belong to. Read them, search the corpus and the frontier as needed, and prioritise consolidation over addition: a "consolidations" entry naming brief observation identifiers (or observation identifiers a frontier search served to this run) that recur or reinforce each other is the output this stage exists for, and a new candidate under "candidates" is secondary. Weigh objections when consolidating; a finding that ignores recorded criticism is not consolidated. A finding states the pattern, why it matters, the scope it was consolidated across, and its counter-evidence position.

You cannot develop observations or object in this stage, and the schema offers no field for them.

`

const instructionsProposals = `A proposal is a suggested change and is never required. Attach one to a consolidation as "proposal" when the finding justifies it, or to a candidate as "remedy" when the candidate says what should change as well as what is the case; a candidate that only states what is the case emits no remedy. A proposal names its problem, its outcome, its impact, its classification ("private", "redaction-required" or "public-safe"), and any risks, open questions, prerequisites and verification criteria. Its "supporting" and "conflicting" material are evidence citations under the same rule as every other locator. Babel renders proposals for an operator to review; nothing you propose is applied.

`

const instructionsQuestions = `"questions" are the things the corpus cannot settle and a person can. Raise one when an answer would change what you conclude and no amount of further searching would produce it: which of two machines a service actually runs on, whether a convention the transcripts disagree about is still in force, what a repository is for. Each names its "subjects" — the machines, repositories, services or projects it is about, written the way the material names them — states its "prompt" and its "why_asked", and names under "hypothesis" the candidate it holds up, when it holds one up.

Babel resolves each subject against the operator's own record of their world. A subject that record has never heard of is a refused question, and refusing it is correct: nothing you write creates a machine or a repository, and a question about a thing nobody has declared has nobody to route it to. Ask about what the material names, not about what you would like to exist.

A question is a request that someone else settle something, so it is the one output here that carries no claim: it asserts nothing, decides nothing, and is answered only by the operator. Never answer one yourself, never treat an answer you imagine as evidence, and never raise one in place of a search you could have run.
`

const instructionsDispositions = `"dispositions" propose what an operator could do next with a record: "draft-issue" (which requires "workspace", the local checkout the issue is about), "propose-reality-fact", "store-memory", "ask-question" or "develop-further". They render as choices, never as actions, and are optional everywhere they appear.
`
