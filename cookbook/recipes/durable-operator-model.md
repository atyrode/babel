---
id: durable-operator-model
version: 1
kind: lens
scope: [session, corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search]
default: false
---

# Durable operator model

> **Draft.** This lens ships for review and is not default-enabled. It is the
> lens most likely to over-generalize from thin evidence and the one whose
> output feeds standing instructions, so a wrong finding here propagates into
> every future run. Enable it explicitly, and expect its guidance to change
> with its version.

## Question

Which of the operator's stated preferences, constraints, and conventions are
durable, and which were instructions for one moment?

The distinction is the entire lens. A corpus contains thousands of imperatives,
and almost all of them are local: do this now, in this file, for this task. A
few are standing: a convention about structure, a constraint about tools, a
requirement about how work is verified, a boundary about what may never be
done. Standing rules are worth writing into instructions, defaults, and
templates; local ones are worth nothing there and are actively harmful, because
an instruction set built from one-off imperatives becomes unfollowable and then
ignored.

Evidence for durability is observable: repetition across unrelated tasks,
restatement after a violation, generality of wording, and consistency over
time. Evidence against it is equally observable: contradiction, contextual
qualification, and explicit revision.

This lens is descriptive. It records what the record shows the operator asked
for, never what the operator is like.

## Inclusion, exclusion, and ambiguity

**The hard boundary.** No observation may characterize the operator — no
inference about personality, skill, mood, working style as a trait, or intent
beyond what was stated. The subject is instructions and their scope. "The
operator prefers X" is acceptable only as shorthand for "X was stated as a
requirement in N independent contexts, with these locators", and the
observation must carry that evidence.

Include: a convention stated in general terms and applied across unrelated
tasks; a constraint restated after being violated; an explicit "always" or
"never" with its scope; a standing verification requirement; a tool, format, or
structural preference with independent occurrences; a stated boundary about
what must not happen.

Include contradictions, and retain them. Two conflicting statements are not a
problem to resolve by picking the newer one: they may be context-dependent
rules whose contexts the analysis has not identified, and collapsing them
produces a false rule. A finding may report an unresolved conflict, which is
also the material a Reality Question is for.

Include scope. A convention that holds in one repository and not another is a
scoped rule, and recording it unscoped is a bug.

Exclude single occurrences as durable rules. One statement is a candidate with
a count of one, and it must be labelled as such.

Exclude anything the operator said about a specific artifact — a file name, a
value, a one-time exception.

Exclude authority. Nothing this lens produces is a fact about reality: facts
require attributed operator action or a configured trusted source. This lens
produces observations and, at most, proposed revisions or questions. It never
asserts.

Ambiguity: an imperative's scope is usually implicit. A statement inside a
narrow task may be general or local, and the wording alone rarely settles it.
The reliable discriminator is behaviour across contexts, which is why this lens
needs a corpus rather than a session. Where scope cannot be established, the
observation states the candidate scope and the evidence for each reading.

## Sorting cues

- the same requirement stated in unrelated tasks, repositories, or months;
- general wording — always, never, by default, in this project, from now on;
- restatement immediately after a violation;
- a requirement stated once and then enforced by correction repeatedly;
- an explicit meta-instruction: a rule about how to work rather than what to
  do;
- consistency between a stated rule and observable practice over time;
- an explicit revision of an earlier rule, which is strong evidence about both;
- a rule that the operator's own later instruction contradicts — strong signal,
  and its value is the conflict, not a resolution.

Weak cues: emphasis, repetition inside one session, and length. Retrieval rank
contributes nothing.

## Evidence and counter-evidence

Evidence, quoted with locators, always:

- each independent statement of the rule, with its session, time, and context;
- the wording, verbatim — durability is largely a property of how a rule was
  phrased;
- the contexts spanned: task type, repository, entity;
- observable adherence: work that followed the rule without it being restated;
- corrections that enforced it;
- any explicit statement of scope or exception.

Counter-evidence to seek:

- a later statement revising or reversing the rule;
- occurrences where the rule was not applied and nobody objected;
- contexts where the opposite was requested;
- the possibility that the repetition is an artifact of one task recurring
  rather than a general rule;
- the possibility that the rule is already written down, in which case the
  finding is that it exists and is or is not followed;
- selection effects from retrieval: searching for a phrase finds the sessions
  that use it and hides the ones that do not.

The last point deserves care. This lens is unusually exposed to confirmation:
having formed a candidate rule, the natural search finds its supporting
statements. Deliberately search for the negation.

## Temporal and present-reality checks

A durable rule is a claim about now, so recency and revision history matter
more than count. Three checks:

1. Is the most recent statement consistent with the rule? A rule with ten old
   occurrences and one recent reversal is reversed.
2. Is it already recorded — in a standing instruction, a convention document, a
   configuration? If so, the finding concerns adherence, not discovery.
3. Has its context expired? A constraint about a tool that is no longer used is
   `historical` regardless of how often it was stated.

Use `still-applicable`, `historical`, `resolved` (already written down),
`contradicted` (a later statement reverses it), and `unverifiable` (present
instruction set not observable). Weight recent evidence over old, and say what
the weighting was.

## Classifications and stopping conditions

- `standing-convention` — general, repeated across unrelated contexts;
- `scoped-convention` — durable within a named scope, with the scope stated;
- `hard-constraint` — an explicit never or must, with its wording;
- `verification-expectation` — a standing requirement about how work is
  checked;
- `local-instruction` — one moment, explicitly not durable. The correct label
  for most candidates;
- `unresolved-conflict` — two statements that disagree, both retained;
- `already-recorded` — the rule exists in writing; the question is adherence;
- `insufficient-evidence-for-durability` — a candidate with too few independent
  contexts.

Stop when the independent occurrences are counted with locators, the scope is
stated, and the negation has been searched for. Stop when the candidate is
better expressed as a question to the operator than as an inference — an
unresolved conflict about a standing rule is precisely what a Reality Question
exists to settle, and asking is cheaper and more accurate than inferring.
Stop before proposing an instruction-set change on fewer than three independent
contexts unless the statement is an explicit meta-instruction.

## Cross-session synthesis keys

Group by: the rule's subject, normalized; scope — global, entity, repository,
task class; the rule's modality — always, never, prefer, avoid; and the
evidence type — stated, enforced by correction, or observed in practice.

Recurrence is available to every lens, and here it is the definition of the
subject rather than a bonus: durability *is* recurrence across independent
contexts. Count independent contexts, not messages, and never count the same
session twice. Report the count in the finding, because a reviewer's decision
about writing a rule down depends on it.

## Capability needs

- `corpus-search` is required and is the only capability this lens needs. Its
  entire evidence base is what the operator wrote, and its most important
  search is the one that looks for the negation.
- No repository or execution capability: adherence claims about code belong to
  the lens that can evidence them. If a candidate needs a snapshot, it is a
  coordination or code-health candidate wearing this lens's clothes.

## Known failure modes

- **Characterizing the operator.** Out of bounds. The subject is instructions.
- **Over-generalizing.** A local imperative promoted to a standing rule
  produces an instruction that misfires in every future run, and this is the
  most expensive error available here.
- **Confirmation search.** Finding the supporting occurrences and stopping.
- **Collapsing contradictions.** Contradictions are evidence about scope;
  resolving them by recency destroys that evidence.
- **Confusing frequency with force.** A rule stated once as an absolute
  prohibition may bind harder than one stated ten times as a preference.
- **Claiming authority.** This lens observes and asks; it never asserts a fact,
  and only an attributed operator action can authorize one.
- **Compliance as correctness.** A well-evidenced rule can still be
  misinterpreted.

## Examples

A useful observation: across seven sessions spanning four repositories and
three months, the operator required that a specific class of change be verified
by running the affected surface rather than by adding a test; the wording is
general each time; two of the seven are corrections issued after a violation.
Evidence: seven quoted statements with locators, plus the two corrections.
Counter-evidence sought: the negation — sessions where a test alone was
accepted — one found, in a context the operator described as an exception, and
retained. Classification `standing-convention` with a stated exception, count
of seven independent contexts. Note what the observation does not say: nothing
about why, and nothing about the operator.

A useful conflict observation: two statements, four months apart, giving
opposite instructions about where a kind of file belongs, with no explicit
revision. Both retained; classification `unresolved-conflict`. The right output
is a question, not a rule.

A candidate correctly rejected: an emphatic instruction about a particular
file's contents, stated once. Classification `local-instruction`; recording it
as a convention would have added an unfollowable line to the instruction set.

An error to avoid: "the operator values thorough verification" as a finding.
It characterizes a person, is unfalsifiable, and yields no reviewable change.
The valid version names the class of change, the required check, the contexts,
the count, and the exception.
