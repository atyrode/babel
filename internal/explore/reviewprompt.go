package explore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/worker"
)

// A review's prompt is composed here from Babel-owned parts, on the same terms
// an exploration's is: the recipe body verbatim first, because it is the
// largest block and a provider can only serve a byte-identical prefix from its
// cache, then the answering protocol, then the role's instructions, its tools,
// its parameters, the approved sessions, and last the record under review.
//
// The target comes last rather than first, and that is not only cost. The
// material a reviewer must not be prejudiced about sits closest to its turn,
// after the method it is meant to apply — a recipe read after the record is a
// method chosen to fit a conclusion.
//
// Two things this composer will not do. It will not render a tally, a rank or
// an earlier evaluation into a blinded assessment's prompt: the projection it
// is handed has no field for one, and its body was audited before it got here.
// And it will not ask the model to ignore what it read. Blinding is what Babel
// served, not an instruction about attention.

// composeReviewPrompt renders one review's prompt.
func composeReviewPrompt(contract worker.OutputContract, recipe *cookbook.Recipe,
	target reviewTarget, alternatives []reviewTarget, previous []evaluation.Record,
	sources []worker.Source, params map[string]string, tools []worker.HostTool, blinded bool,
	ledger *TopicLedger) (string, error) {
	var b strings.Builder
	b.WriteString("# Babel evaluation\n\n")

	b.WriteString("## Recipe\n\n")
	b.WriteString("The cookbook recipe that states this review's method, verbatim.\n\n")
	fmt.Fprintf(&b, "### %s (version %d)\n\n", recipe.ID, recipe.Version)
	b.WriteString(strings.TrimSpace(recipe.Body))
	b.WriteString("\n\n")

	b.WriteString("## How to answer\n\n")
	b.WriteString("Read the record below, use the tools if they help, then call `" + worker.ToolSubmit + "` once with your ")
	b.WriteString("assessment. The arguments are validated against the result schema before Babel sees them, and Babel ")
	b.WriteString("then checks the role's authority, the closed vocabularies, and every evidence locator against what ")
	b.WriteString("this review was served; a refusal explains what to fix, and calling again replaces the earlier ")
	b.WriteString("submission. End your turn once the submission is accepted.\n\n")
	b.WriteString("A bare vote is a complete answer. You are not required to write prose, find new evidence, or ")
	b.WriteString("propose a refinement, and inventing any of them to fill the result would be worse than omitting ")
	b.WriteString("them. A contribution without a vote is equally valid. If you cannot judge this record at all — the ")
	b.WriteString("subject needs a check you cannot make, the evidence is unreachable from here — set `skip` and say ")
	b.WriteString("why. A skip is recorded as a gap, not as an opposing vote, so declining is never the same as ")
	b.WriteString("judging against it.\n\n")

	b.WriteString("## Your role\n\n")
	b.WriteString(contract.Instructions)
	b.WriteString("\n")

	if len(tools) > 0 {
		b.WriteString("## Tools\n\n")
		for _, tool := range tools {
			fmt.Fprintf(&b, "- `%s`: %s\n", tool.Name, tool.Description)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Parameters\n\n")
	b.WriteString("Every parameter this review carries, one per line.\n\n")
	b.WriteString(paramsOpen + "\n")
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s = %s\n", key, params[key])
	}
	b.WriteString(paramsClose + "\n\n")

	b.WriteString("## Sessions\n\n")
	if len(sources) == 0 {
		b.WriteString("This review was given no sessions to search.\n\n")
	} else {
		b.WriteString("The sessions this review may search, by the selector the search tool filters on.\n\n")
		for _, source := range sources {
			fmt.Fprintf(&b, "- %s\n", source.Selector)
		}
		b.WriteString("\n")
	}

	b.WriteString("## The record under review\n\n")
	if blinded {
		b.WriteString("This is an initial assessment and it is taken blind. Babel has not shown you how this record ")
		b.WriteString("has been received, how it was ranked, or what any earlier review of it said, and the tools ")
		b.WriteString("above cannot reach them either. Judge the record as it stands.\n\n")
	}
	encoded, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return "", fmt.Errorf("explore: render the review target: %w", err)
	}
	b.WriteString("```json\n")
	b.Write(encoded)
	b.WriteString("\n```\n\n")

	if len(alternatives) > 0 {
		b.WriteString("## Alternatives to compare\n\n")
		b.WriteString("Other records addressing the same problem. Comparing them does not merge them: each keeps its ")
		b.WriteString("own record, its own reception and its own decision, and preferring one here is a preference in ")
		b.WriteString("the context you name rather than a vote about either.\n\n")
		for _, alt := range alternatives {
			encoded, err := json.MarshalIndent(alt, "", "  ")
			if err != nil {
				return "", fmt.Errorf("explore: render a review alternative: %w", err)
			}
			b.WriteString("```json\n")
			b.Write(encoded)
			b.WriteString("\n```\n\n")
		}
	}

	if len(previous) > 0 {
		b.WriteString("## Earlier evaluations\n\n")
		b.WriteString("What earlier reviews recorded about this record. They are shown because this role's question is ")
		b.WriteString("about the disagreement itself; they are not a score to agree with, and an earlier reviewer ")
		b.WriteString("having voted is not evidence about the claim.\n\n")
		for _, record := range previous {
			b.WriteString(renderPriorEvaluation(record))
		}
		b.WriteString("\n")
	}

	if ledger != nil {
		b.WriteString("## What the ledger already names\n\n")
		b.WriteString("The topics that exist, why some were retired, why some proposals were declined, and the ")
		b.WriteString("repositories the sessions this record cites were in. Prefer one of these entities: a topic ")
		b.WriteString("nobody needed a second name for is the one an operator can act on. The retired and declined ")
		b.WriteString("reasons are why Babel got a topic wrong before, and repeating one of them is the failure this ")
		b.WriteString("material exists to prevent.\n\n")
		encoded, err := json.MarshalIndent(ledger, "", "  ")
		if err != nil {
			return "", fmt.Errorf("explore: render the topic ledger: %w", err)
		}
		b.WriteString("```json\n")
		b.Write(encoded)
		b.WriteString("\n```\n\n")
	}

	return b.String(), nil
}

// renderPriorEvaluation renders one earlier evaluation for the roles that may
// read them.
//
// It renders the statement and withholds the arithmetic: who said what, with
// what evidence and what they were unsure about, and deliberately no count of
// how many agreed. §4.12 keeps reception separate from evidence strength, and a
// "4 support, 1 oppose" line is exactly the number a later reviewer would
// mistake for corroboration.
func renderPriorEvaluation(record evaluation.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s by %s %s", record.Kind, record.ActorKind, record.ActorID)
	if !record.CreatedAt.IsZero() {
		fmt.Fprintf(&b, " on %s", record.CreatedAt.UTC().Format("2006-01-02"))
	}
	b.WriteString("\n")
	if record.Reason != "" {
		fmt.Fprintf(&b, "  reason: %s\n", record.Reason)
	}
	if record.Assessment == nil {
		return b.String()
	}
	if record.Assessment.Vote != "" {
		fmt.Fprintf(&b, "  vote: %s\n", record.Assessment.Vote)
	}
	if record.Assessment.Outcome != "" {
		fmt.Fprintf(&b, "  observed outcome: %s\n", record.Assessment.Outcome)
	}
	for _, contribution := range record.Assessment.Contributions {
		text := strings.TrimSpace(contribution.Text)
		if text == "" {
			text = "(no comment)"
		}
		fmt.Fprintf(&b, "  %s: %s\n", contribution.Kind, text)
		if contribution.WouldChange != "" {
			fmt.Fprintf(&b, "    would change: %s\n", contribution.WouldChange)
		}
	}
	if record.Assessment.Uncertainty != "" {
		fmt.Fprintf(&b, "  unsure about: %s\n", record.Assessment.Uncertainty)
	}
	return b.String()
}

// reviewInstructions composes one role's static instructions from the blocks
// its authority admits, the way stageInstructions does for an exploration
// stage: the schema says what a role may submit and this says what each field
// means, so a pruned field never arrives with instructions telling a model to
// fill it.
func reviewInstructions(role string, auth reviewAuthority) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are reviewing one record in the %s role.\n\n", role)
	b.WriteString(reviewRoleQuestion(role))
	b.WriteString("\n")
	if auth.filing {
		// A filing pass reads none of the common review instructions and
		// contributes nothing: the blocks below are about judging a
		// record, and this role is about naming what it is about.
		b.WriteString(instructionsReviewFiling)
		return b.String()
	}
	b.WriteString(instructionsReviewCommon)
	if auth.vote {
		b.WriteString(instructionsReviewVote)
	}
	if auth.criteria {
		b.WriteString(instructionsReviewCriteria)
	}
	if auth.outcome {
		b.WriteString(instructionsReviewOutcome)
	}
	if auth.alternatives {
		b.WriteString(instructionsReviewComparison)
	}
	b.WriteString(instructionsReviewContributions)
	return b.String()
}

// reviewRoleQuestion is the one question each role answers. They are separate
// questions on purpose: reception, evidence, relevance and observed outcome are
// four different things to know about a record, and a role that blurred two of
// them would produce an answer that means neither.
func reviewRoleQuestion(role string) string {
	switch role {
	case evaluation.RoleReception:
		return "The question is whether you support, oppose or are unsure about this record as it stands. " +
			"That is a judgement about the idea, not a measurement of its evidence, not a probability that " +
			"it is correct, and not a prediction of whether the operator will accept it.\n"
	case evaluation.RoleEvidence:
		return "The question is whether the evidence this record cites actually supports what it claims. " +
			"Check the locators. An evidence check is not a vote: report what the cited records do and do " +
			"not show, and leave the reception judgement to the reception role.\n"
	case evaluation.RoleChallenge:
		return "The question is what the strongest objection to this record is, given what earlier reviews " +
			"already said. Ground the objection in evidence, a consequence, a missing check or a concrete " +
			"alternative. You are diagnosing a disagreement, not voting until it resolves.\n"
	case evaluation.RoleComparison:
		return "The question is how this record compares with the alternatives addressing the same problem. " +
			"Say why now, what objection remains unresolved, and what would change the recommendation where " +
			"you know. Prefer one in a named context if you can; do not merge them and do not vote.\n"
	case evaluation.RoleOutcome:
		return "The question is what actually happened: whether this was implemented, whether the promised " +
			"outcome was observed, and against which criteria. A merge is not a deployment and a deployment " +
			"is not proof of the promised outcome. Missing, partial or conflicting evidence is not success.\n"
	case evaluation.RoleRelevance:
		return "The question is whether this record is relevant to the recorded work, pain and constraints " +
			"shown with it. Relevance is not quality and not reception: a correct finding about something " +
			"nobody is working on is correct and not relevant, and saying so is the useful answer.\n"
	case evaluation.RoleFiling:
		return "The question is what this record is about: which topic a reader would look for it under. " +
			"A topic is a thing in the world with a name and a binding — a repository, a project, a " +
			"machine, a service, a concept — and never a folder, a directory or the workspace the work " +
			"happened in. You are not judging this record, and no part of your answer is a vote.\n"
	default:
		return ""
	}
}

const instructionsReviewCommon = `
Judge the exact wording you were shown. The identity in the parameters names
one immutable revision of this record; if a newer revision exists, your
assessment is about the one here and Babel binds it to that revision rather
than moving it forward.

Cite only locators Babel served you. Every evidence item you submit is checked
against this review's own served trace, and a locator you did not receive is
refused whether or not its bytes exist.

Say what you are unsure about in ` + "`uncertainty`" + `. An unknown recorded is
worth more than a rationale invented to fill the field.
`

const instructionsReviewVote = `
Set ` + "`vote`" + ` to support, oppose or unsure. Omit it if you have nothing to
say about reception and are only contributing. Support means you think the
record should be acted on as it stands; oppose means you think it should not;
unsure means you have read it and cannot tell, which is a real answer and not a
missing one.
`

const instructionsReviewCriteria = `
Record one entry in ` + "`results`" + ` per criterion you checked, naming the
criterion id from the record above. Satisfied requires evidence: a criterion you
believe holds but cannot cite is unsatisfied with your uncertainty recorded, not
satisfied on your word. A criterion you did not check gets no entry.
`

const instructionsReviewOutcome = `
Set ` + "`outcome`" + ` only from evidence you cite: implemented, verified,
partial, contradicted or unverifiable. Verified means every criterion you listed
is satisfied and evidenced. Unverifiable is the honest answer when the checks
that would settle it are ones you cannot make from here, and it is strictly
better than a guess at verified; say in ` + "`uncertainty`" + ` what you could
not check. Any outcome or criterion result also needs ` + "`environment`" + `,
the setting you observed, and ` + "`as_of`" + `, when you observed it: a result
with no stated scope reads as a claim about every setting at every time.
`

const instructionsReviewComparison = `
A comparison contribution names at least two alternatives and may name one as
preferred. The preference holds in the context you state in the text and
nowhere else: it mints no vote for either record, merges nothing, and rules on
nothing. Preferring a record your own run authored is refused.
`

const instructionsReviewContributions = `
Contributions are optional. Each has a kind: comment, evidence, objection,
refinement or comparison. An evidence contribution must cite at least one
locator; a comparison must name its alternatives; every other kind carries text
or evidence. Use ` + "`would_change`" + ` to say what would change your mind
where you know it, and leave it empty where you do not.
`

const instructionsReviewFiling = `
Answer with exactly one of three fields.

` + "`filing`" + ` files this record under a topic that already exists. Name the
entity by any name or alias it is listed under and say in ` + "`rationale`" + `
why this record is about it. This is the answer to prefer: a second topic for
something the ledger already names is a merge the operator has to do by hand.

` + "`topic`" + ` proposes a topic nobody has created, when the record is about
something no listed entity names. It is a question for the operator and not a
creation: give the ` + "`name`" + `, the ` + "`kind`" + `, an
` + "`identity`" + ` that is the same string for the same thing every time — a
normalized remote, a common directory, a hostname, or a slug for a concept —
and bind it to something real with ` + "`remote`" + `, ` + "`paths`" + ` or a
one-sentence ` + "`definition`" + `. Say in ` + "`reasoning`" + ` why the topic
should exist and list in ` + "`considered`" + ` the existing topics you weighed
and rejected.

` + "`no_topic`" + ` records that the record is about nothing in particular,
with the reason. Some outputs are about the process, about a passing question,
about nothing an operator would ever go looking for by name, and saying so is
the honest result. It is not a failure and it is not a skip: a skip means you
could not read the record, and this means you read it and it has no topic.

A name you use in ` + "`filing`" + ` that no listed entity answers to becomes a
topic question rather than a filing, so guessing at a name costs the operator a
question to decline. Set ` + "`skip`" + ` only when the record itself is
unreadable from here.
`
