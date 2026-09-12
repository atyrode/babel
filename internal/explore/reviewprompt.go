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
	ledger *TopicLedger, backlog *BacklogMaterial) (string, error) {
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
		// The two blocks below are rendered on their own rather than
		// inside this JSON, so the observations a pass may propose from
		// and the asks it has to answer cannot be read as more entities
		// that already exist. Same material, three headings, because the
		// difference between them is the whole authority boundary.
		known := *ledger
		known.Unbound, known.Asks = nil, nil
		encoded, err := json.MarshalIndent(known, "", "  ")
		if err != nil {
			return "", fmt.Errorf("explore: render the topic ledger: %w", err)
		}
		b.WriteString("```json\n")
		b.Write(encoded)
		b.WriteString("\n```\n\n")
		b.WriteString(renderUnbound(ledger.Unbound))
		b.WriteString(renderAsks(ledger.Asks))
	}

	if backlog != nil {
		block, err := renderBacklog(backlog)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
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

// renderUnbound renders what the scan observed and nothing names.
//
// It is evidence and the heading says so. §4.13's second reading removed the
// Go loop that turned each of these into a topic question, because a topic
// nothing judged is a topic Babel cannot explain; what reaches a pass is
// therefore the observation with its counts, and the proposal — if the record
// under review is about one of them — is the pass's own.
//
// The counts are rendered rather than the raw struct because they are what
// carries the judgement: sessions and checkouts say whether the identity is a
// project or a scratch clone, and the record count says how much filing the
// operator would be accepting.
func renderUnbound(observed []TopicObservation) string {
	if len(observed) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What the scan observed and nothing names\n\n")
	b.WriteString("Repository identities this host observed that no entity in the ledger binds. They are ")
	b.WriteString("evidence, not proposals: nothing here is a topic until you say the record under review is ")
	b.WriteString("about one and the operator accepts it. Propose one only when *this* record is about it — a ")
	b.WriteString("busy identity is not a reason to name it while reviewing something else.\n\n")
	for _, item := range observed {
		fmt.Fprintf(&b, "- %s (identity %s", item.Name, item.Identity)
		if item.Remote != "" {
			fmt.Fprintf(&b, ", remote %s", item.Remote)
		}
		fmt.Fprintf(&b, "): %d %s in %d %s",
			item.Sessions, plural(item.Sessions, "session", "sessions"),
			item.Checkouts, plural(item.Checkouts, "checkout", "checkouts"))
		if item.Records > 0 {
			// Rendered only when somebody counted. The count is a
			// corpus-wide derivation and not every caller holds the
			// corpus, so a printed zero would tell a pass that nothing
			// cites the identity when the truth is that nothing
			// counted.
			fmt.Fprintf(&b, ", %d %s cite it",
				item.Records, plural(item.Records, "record", "records"))
		}
		if item.Kind != "" {
			fmt.Fprintf(&b, "; observed as a %s", item.Kind)
		}
		b.WriteString("\n")
		for _, path := range item.Paths {
			fmt.Fprintf(&b, "  seen at: %s\n", path)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderAsks renders the operator's own asks about topics.
//
// They are his words and they are not parsed into an instruction, which is the
// difference §4.13 draws between Babel doing as it is told and Babel
// understanding what it was told. The id travels because answering one is a
// write against it: a proposal that names the ask, or a reasoned no that does.
func renderAsks(asks []TopicAsk) string {
	if len(asks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What the operator asked\n\n")
	b.WriteString("What the operator said about topics and nobody has answered yet, in his own words. Answer ")
	b.WriteString("every one that concerns this record's topics — with a `topic` proposal naming the ask, or ")
	b.WriteString("with `no_change` and the reason. An ask is answered rather than obeyed: if it is wrong, say ")
	b.WriteString("so and why.\n\n")
	for _, ask := range asks {
		fmt.Fprintf(&b, "- %s", ask.ID)
		if !ask.At.IsZero() {
			fmt.Fprintf(&b, " on %s", ask.At.UTC().Format("2006-01-02"))
		}
		if ask.Topic != "" {
			fmt.Fprintf(&b, " about %s", ask.Topic)
		}
		fmt.Fprintf(&b, ": %s\n", strings.TrimSpace(ask.Text))
	}
	b.WriteString("\n")
	return b.String()
}

// renderBacklog renders the deferred candidate a backlog pass was drawn for,
// with the evidence and the neighbours an act would name.
//
// Four headings rather than one JSON block, and the separation is the
// authority boundary again. The candidate and its observations are what this
// pass may settle; the siblings are candidates it may name but not act on
// beyond the act it proposes; and the entities are the ledger's, which this
// pass may promote a fact onto and may never create. Reading them as one list
// is how a pass comes to propose an act on something it was shown for context.
func renderBacklog(material *BacklogMaterial) (string, error) {
	var b strings.Builder
	b.WriteString("## The deferred candidate\n\n")
	b.WriteString("The hypothesis this pass was drawn for: a claim a run set down and nobody came back ")
	b.WriteString("to. It is not under review — nothing you say here is a vote — and the question is what ")
	b.WriteString("should become of it.\n\n")
	encoded, err := json.MarshalIndent(material.Candidate, "", "  ")
	if err != nil {
		return "", fmt.Errorf("explore: render the deferred candidate: %w", err)
	}
	b.WriteString("```json\n")
	b.Write(encoded)
	b.WriteString("\n```\n\n")

	b.WriteString("## Its observations\n\n")
	if len(material.Observations) == 0 {
		b.WriteString("Nothing was ever observed against this candidate. A claim with no evidence behind ")
		b.WriteString("it is the honest case for a retirement — or for keeping it, if the question it asks ")
		b.WriteString("is still worth asking.\n\n")
	} else {
		b.WriteString("Every claim developed against this candidate, oldest first, with the locators it ")
		b.WriteString("cites. This is the evidence: an observation has no standing of its own and takes ")
		b.WriteString("this candidate's fate, so what you propose decides what becomes of all of it.\n\n")
		encoded, err := json.MarshalIndent(material.Observations, "", "  ")
		if err != nil {
			return "", fmt.Errorf("explore: render the candidate's observations: %w", err)
		}
		b.WriteString("```json\n")
		b.Write(encoded)
		b.WriteString("\n```\n\n")
	}

	if len(material.Siblings) > 0 {
		b.WriteString("## Candidates beside it\n\n")
		b.WriteString("Other candidates filed under the same topics that share this one's key terms. They ")
		b.WriteString("are the only candidates a consolidation may fold or a supersession may name: an ")
		b.WriteString("identifier that is not listed here is refused as a malformed result.\n\n")
		for _, sibling := range material.Siblings {
			fmt.Fprintf(&b, "- %s (%s", sibling.ID, sibling.Status)
			if !sibling.DeferredAt.IsZero() {
				fmt.Fprintf(&b, ", deferred %s", sibling.DeferredAt.UTC().Format("2006-01-02"))
			}
			if sibling.Observations > 0 {
				fmt.Fprintf(&b, ", %d %s", sibling.Observations,
					plural(sibling.Observations, "observation", "observations"))
			}
			fmt.Fprintf(&b, "): %s\n", strings.TrimSpace(sibling.Statement))
			if len(sibling.Topics) > 0 {
				fmt.Fprintf(&b, "  filed under: %s\n", strings.Join(sibling.Topics, ", "))
			}
			if sibling.Note != "" {
				fmt.Fprintf(&b, "  set down because: %s\n", strings.TrimSpace(sibling.Note))
			}
		}
		b.WriteString("\n")
	}

	if len(material.Entities) > 0 {
		b.WriteString("## Entities a fact could be about\n\n")
		b.WriteString("The Reality Ledger's entities, by the names and aliases they answer to. A promotion ")
		b.WriteString("records a fact about one of these and never about a name you invent: only the ")
		b.WriteString("operator creates an entity, so an unresolvable name is refused rather than created.\n\n")
		for _, entity := range material.Entities {
			fmt.Fprintf(&b, "- %s (%s)", entity.Name, entity.Kind)
			if len(entity.Aliases) > 0 {
				fmt.Fprintf(&b, ", also: %s", strings.Join(entity.Aliases, ", "))
			}
			if entity.Binding != "" {
				fmt.Fprintf(&b, "; bound to %s", entity.Binding)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(material.Predicates) > 0 {
		b.WriteString("## What a fact may say\n\n")
		b.WriteString("The predicates the ledger admits, with the values each takes. The vocabulary is ")
		b.WriteString("closed: a fact outside it cannot be recorded, and a claim that does not fit one of ")
		b.WriteString("these is not a promotion.\n\n")
		for _, predicate := range material.Predicates {
			fmt.Fprintf(&b, "- `%s` (%s)", predicate.Name, predicate.Kind)
			if len(predicate.Values) > 0 {
				fmt.Fprintf(&b, ": one of %s", strings.Join(predicate.Values, ", "))
			}
			if predicate.Why != "" {
				fmt.Fprintf(&b, " — %s", predicate.Why)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// plural picks the noun form for a rendered count, so a prompt does not tell a
// model about "1 sessions" and invite it to read the number as approximate.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	if auth.backlog {
		// A backlog pass reads none of them either, and for the same
		// reason: what should become of a deferred candidate is not a
		// judgement about whether it is any good.
		b.WriteString(instructionsReviewBacklog)
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
		return "The question is what this record is about: which topic a reader would look for it under, " +
			"and whether the ledger's topics themselves should change for it to be findable. A topic is " +
			"a thing in the world with a name and a binding — a repository, a project, a machine, a " +
			"service, a concept — and never a folder, a directory or the workspace the work happened " +
			"in. You are not judging this record, and no part of your answer is a vote.\n"
	case evaluation.RoleBacklog:
		return "The question is what should become of this deferred candidate: whether it and others " +
			"say one thing a finding should say, whether a newer candidate already says it better, " +
			"whether it should be retired with a reason, whether one of its observations is a durable " +
			"fact about something the ledger names, or whether it is worth keeping exactly as it is. " +
			"You are not judging the candidate and no part of your answer is a vote, and nothing you " +
			"say changes anything: every act is a proposal the operator rules on.\n"
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
Answer with exactly one of four fields.

` + "`filing`" + ` files this record under a topic that already exists. Name the
entity by any name or alias it is listed under and say in ` + "`rationale`" + `
why this record is about it. This is the answer to prefer: a second topic for
something the ledger already names is a merge the operator has to do by hand.

` + "`topic`" + ` proposes a change to the ledger's topics. It is a proposal
for the operator and never a change: he reads it and accepting it is what
applies it. Set ` + "`operation`" + ` to exactly one of four:

- ` + "`create`" + ` — the record is about something no listed entity names.
  Give the ` + "`name`" + `, the ` + "`kind`" + `, an ` + "`identity`" + ` that
  is the same string for the same thing every time — a normalized remote, a
  common directory, a hostname, or a slug for a concept — and bind it to
  something real with ` + "`remote`" + `, ` + "`paths`" + ` or a one-sentence
  ` + "`definition`" + `. Name no targets.
- ` + "`split`" + ` — one listed topic names two things. Put that topic in
  ` + "`targets`" + `, and describe the part that would be separated out with
  the same name, kind, identity and binding a create carries. This record is
  what moves to the new part, so split only when this record belongs to the
  part you are describing.
- ` + "`merge`" + ` — two listed topics name one thing. Put both in
  ` + "`targets`" + `, the one that disappears first and the one that survives
  second, and carry no name, kind or identity: nothing is created.
- ` + "`retire`" + ` — one listed topic should never have existed. Put it in
  ` + "`targets`" + `, and remember that retiring it re-queues everything filed
  under it for triage, so the reason has to be that the topic is wrong rather
  than that it is quiet.

Every operation needs ` + "`reasoning`" + `: why the ledger should change, and
why the topics in ` + "`considered`" + ` were weighed and rejected. Every
` + "`targets`" + ` entry must be a topic listed above — an invented one is
refused as a malformed result and creates nothing.

An identity listed under "what the scan observed and nothing names" is evidence
for a ` + "`create`" + `, and only when *this* record is about it. The counts
say how much stands behind it; the fact that Babel saw a repository is not a
reason to name it while reviewing a record about something else.

An ask listed under "what the operator asked" is answered rather than obeyed.
If you agree with it, answer with the ` + "`topic`" + ` proposal it calls for
and set ` + "`ask_id`" + ` to that ask. If you judge it wrong — the topics it
names are one thing, the split it wants would cut across what the records
actually say — answer with ` + "`no_change`" + `, naming the ` + "`ask_id`" + `
and the ` + "`reason`" + `, which lands as a reply where he asked. Answer only
the asks that concern this record's topics.

` + "`no_topic`" + ` records that the record is about nothing in particular,
with the reason. Some outputs are about the process, about a passing question,
about nothing an operator would ever go looking for by name, and saying so is
the honest result. It is not a failure and it is not a skip: a skip means you
could not read the record, and this means you read it and it has no topic.

A name you use in ` + "`filing`" + ` that no listed entity answers to becomes a
create proposal rather than a filing, so guessing at a name costs the operator
a proposal to decline. Set ` + "`skip`" + ` only when the record itself is
unreadable from here.
`

const instructionsReviewBacklog = `
Answer with exactly one of five fields. Every one of the first four is a
proposal the operator rules on, published through Babel's ordinary chain and
applied by his acceptance and by nothing else. Nothing you say here settles
anything by itself, and nothing is ever deleted: a candidate that is
consolidated, superseded or retired keeps its record, its observations and its
history, and gains one appended status event saying a later record speaks for
it.

` + "`consolidate`" + ` says that this candidate and others beside it are
evidence for one thing, and that a finding should say it. Name every candidate
in ` + "`hypotheses`" + ` — the drawn candidate is folded whether or not you
list it — and write the ` + "`finding`" + ` with its ` + "`title`" + `,
the ` + "`pattern`" + ` the observations share, ` + "`why_it_matters`" + ` and
the ` + "`scope`" + ` it holds in. Prefer consolidating into what an existing
finding already says: if one of the candidates beside this one is already
consolidated by a finding that covers this evidence too, the honest act is to
say so in the pattern and fold this candidate into that claim rather than to
mint a second finding a reader would have to reconcile.

` + "`supersede`" + ` says a newer candidate states the same thing better. Set
` + "`by`" + ` to that candidate and say in ` + "`reason`" + ` what it says
better. Only a candidate listed beside this one qualifies, and only one that
genuinely says the *same* thing: a candidate that says something adjacent is
not a supersession, it is a second claim, and superseding with it would lose
the question this one was asking.

` + "`retire`" + ` says the candidate is not worth returning to, with a
` + "`reason`" + ` a reader could check. "The service it describes was
decommissioned and no observation was ever recorded against it" is checkable.
"Low value", "stale", "not interesting" and "superseded by later work" with no
candidate named are gradings, and a grading is not a reason. Nothing is stale
by a clock: age alone is never a retirement.

` + "`promote`" + ` says one of this candidate's observations is a durable fact
about something the ledger already names. Set ` + "`observation`" + ` to that
claim, ` + "`entity`" + ` to the entity by any name or alias it is listed
under, ` + "`predicate`" + ` and ` + "`value`" + ` to the fact in the ledger's
own closed vocabulary, and ` + "`reason`" + ` to why it is durable. Durable is
the whole test: a fact is what stays true until something changes it — where a
repository lives, what a service runs on, whether a project is dormant — and
not what was observed once in one session. An entity no listed name answers to
is refused rather than created, because only the operator creates one.

` + "`keep`" + ` says the candidate is worth keeping exactly as it is, with the
reason. It is a complete answer and frequently the right one: a backlog full of
open questions that nobody has had time for is a healthy backlog, and an act
invented to avoid answering ` + "`keep`" + ` costs the operator a ruling on
something that should not have moved.

Every identifier you use must be one you were shown. A candidate, an
observation or an entity the material does not hold is refused as a malformed
result and creates nothing. Set ` + "`skip`" + ` only when the material itself
is unreadable from here — being unable to choose an act is ` + "`keep`" + `
with the reason, not a skip.
`
