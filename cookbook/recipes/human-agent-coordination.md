---
id: human-agent-coordination
version: 2
kind: lens
scope: [session, corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: true
---

# Human–agent coordination and avoidable rework

## Question

Where did work have to be redone because of how it was communicated rather
than because of what it required?

This is fruitful because the cost is large, recurring, and almost never
recorded as a problem. An instruction that could be read two ways produces a
plausible wrong result; a stated constraint that never reached the work
produces a correct-looking change that must be reverted; a handoff between
sessions loses the state that the next session then reconstructs. Each event
looks like ordinary work in the moment. Across a corpus, the same
coordination shape appears repeatedly, and the fix is usually a small, durable
change to instructions, defaults, or an interface — not more effort.

The lens is also the one most likely to be misused, so its boundary is part of
the question: it examines **observable artifacts of coordination**, and never
the people coordinating.

## Inclusion, exclusion, and ambiguity

**The hard boundary.** This lens does not diagnose emotion, ability,
personality, motivation, intent, or any mental state, for the operator or the
agent, and it does not use language that implies one. No observation may say
frustrated, annoyed, confused as a state, careless, impatient, struggling as a
condition, inexperienced, or overconfident. What may be recorded is what the
record shows: a message was repeated; a correction was issued three times; an
instruction admitted two readings; a stated constraint does not appear in the
resulting change; a request was rephrased; a session restarted the same task.
"Operator struggle" in this lens means **observable friction in the
interaction** — repetition, correction, restart, escalation, abandonment — and
nothing about a person.

Include: an instruction that admits more than one reasonable reading, with both
readings stated; a constraint stated in the session and absent from the work
that followed; a correction repeated more than once for the same class of
behaviour; a handoff — between sessions, agents, or machines — where the next
step re-established context that existed before; a mismatch between the
question asked and the answer given; rework whose cause is visible in the
exchange; and a place where a default, template, or standing instruction would
have removed the ambiguity.

Include successful coordination: a request that was precise enough to succeed
first time, a handoff that carried its state, a standing convention that was
followed without restatement. This is the material the effective-patterns lens
uses, and this lens is where it is observed.

Exclude ordinary iteration. Exploratory back-and-forth is how thinking works,
and calling it rework is both wrong and insulting to the record. Rework
requires *discarded* or *redone* work. Exclude disagreements about substance:
an operator who changed direction after seeing a result was not misunderstood.
Exclude anything about the quality of the code, which belongs to other lenses.
Exclude tone analysis entirely — the absence of politeness is not evidence of
anything.

Ambiguity: a repeated correction may be a changing requirement rather than an
ignored constraint, and the two are distinguishable only by what the operator's
words actually asked for. A lost handoff may be an intentional fresh start. A
rephrased request may be thinking out loud. When the record cannot separate
these, the observation states both readings; it does not choose the one that
makes a better finding.

Search the frontier before minting. Babel indexes its own hypotheses,
observations, findings, and the operator's recorded review answers; the job
document lists the prior records that looked related to this scope, and the same
search is available on demand through the corpus-search tool with
`"scope": "frontier"`. Everything it returns is a prior candidate idea and not
evidence — those records carry no locator of their own, and an operator may
already have rejected one — so treating any of them as established is the same
error as reading a confident summary as a verified claim. Where a prior record
already says what a candidate or observation of yours would say, emit against
it: refine its wording, develop another observation onto it, revive a candidate
that came to rest, or amend a finding, naming its record id, rather than
emitting a second copy of one idea for an operator to review twice. Where a
prior record is wrong, contradict it explicitly and with evidence; a
contradiction is a relationship worth recording and a duplicate is not. When the
job document marks the scope as drawn for serendipity, those records are
inspiration rather than a boundary, and following the corpus somewhere none of
them mention is the correct outcome — searching first still applies, because the
point was never to stay near prior work but to avoid restating it unknowingly.

## Sorting cues

- the same correction issued more than once, in one session or across sessions;
- an instruction containing an unqualified pronoun, an unbounded scope word, or
  two compatible readings, followed by work that took the other one;
- a constraint stated once early and violated later in the same session;
- a revert, an "actually", a "no, I meant", or a restatement immediately
  following a delivered result;
- a session that begins by re-establishing context another session already
  had;
- an answer that addresses a different question than the one asked;
- repeated requests for the same information;
- an escalation from description to example to explicit specification for one
  task;
- a task abandoned and restarted rather than continued.

Strongest cue: the same corrective instruction appearing in unrelated tasks,
which points at a missing standing convention rather than at any single
exchange. Retrieval rank is not a cue and never contributes to strength.

## Evidence and counter-evidence

Evidence, quoted with locators:

- the instruction, in the operator's exact words — paraphrase destroys the
  evidence, because the ambiguity is *in* the wording;
- the work that followed, and the specific way it diverged;
- the correction, and the number of independent times it was issued;
- the constraint and its absence from the result, both located;
- for a handoff, what state the earlier session had and what the later one
  reconstructed;
- repository state where a constraint's violation is visible in the code
  (`repo-read` at the pinned snapshot).

Counter-evidence to seek:

- a later message that changed the requirement, which makes the "ignored
  constraint" a superseded one;
- the constraint being satisfied in a way the analysis did not recognize;
- context available to the participants and absent from the corpus — another
  channel, a prior session, a document, a shared convention;
- the operator having accepted the divergent result, which reclassifies it from
  rework to a choice;
- the possibility that the second reading of an instruction is only ambiguous
  to a reader without the operator's context, which is a fact about the
  analysis rather than about the instruction.

An observation here must be constructible from quoted words alone. If it needs
an assumption about what someone felt or knew, it is out of bounds.

## Temporal and present-reality checks

Coordination evidence ages in a specific way: instructions, defaults, and
standing conventions change. A correction that recurred ten times last quarter
may have been made unnecessary by a rule added since, and proposing that rule
again is a visible waste of review. So check the present: does the standing
instruction, template, or default that would resolve this already exist at the
pinned snapshot? If it exists and the friction persists, that is a *stronger*
finding and a different one — the mechanism exists and did not take effect.

Use `historical` for friction a since-added convention resolves,
`still-applicable` where the ambiguity remains, `resolved` where the record
itself shows the convention landing and the friction stopping, and `regressed`
where friction returns after a convention was in place. Where the current
instruction set is not observable, say `unverifiable` rather than assuming.

## Classifications and stopping conditions

- `ambiguous-instruction` — two readings, both stated, one taken;
- `constraint-not-carried` — stated and absent from the result;
- `repeated-correction` — the same correction more than once, with a count;
- `weak-handoff` — context re-established that already existed;
- `question-answer-mismatch` — the answer addresses something else;
- `avoidable-rework` — work discarded, with the coordination cause located;
- `escalation-to-specification` — increasingly explicit restatement of one
  task;
- `effective-coordination` — the positive case, kept deliberately;
- `friction-without-identified-cause` — observable repetition whose cause the
  record does not show. This is the honest class for most candidates, and it
  must not be upgraded by guessing.

Stop when the two readings, or the constraint and its absence, are quoted with
locators and the smallest durable change is nameable — a standing instruction,
a default, a template, a checklist item, an interface change. Stop when
counter-evidence shows a changed requirement. Stop immediately if the next
step requires characterizing a participant; the candidate is finished, and what
remains is out of scope by construction.

## Cross-session synthesis keys

Group by: the correction's content, normalized; the ambiguous construct's
shape, not its subject; the constraint's identity; the handoff boundary
crossed; and the task type.

Recurrence belongs to every lens, and this lens is where it is most often the
entire point: a coordination finding is usually worthless at one occurrence and
valuable at five, because the proposal is a durable rule and a rule is only
worth its maintenance if the friction recurs. Count independent sessions, and
say so. Where a repeated correction spans different task types, the finding is
about a general default; where it clusters in one repository or subject, it is
about that context's instructions.

## Capability needs

- `corpus-search` is required and central: recurrence is the finding.
- `repo-read` at a pinned snapshot is used for two things: seeing whether a
  stated constraint is satisfied in the code, and seeing whether a standing
  instruction that would resolve the friction already exists. Without it,
  present-tense conclusions degrade to `unverifiable`.
- No execution capability is needed. This lens makes no claim that requires
  running anything, and it should not request one.

## Known failure modes

- **Diagnosing a person.** The defining failure of this lens. Emotion,
  ability, and mental state are out of bounds for the operator and the agent
  alike, in the observation text and in the classification. An observation
  that reads as a character assessment is invalid regardless of its evidence.
- **Iteration counted as rework.** Exploration is not waste.
- **Paraphrasing the instruction.** The ambiguity lives in the exact wording;
  a paraphrase manufactures or destroys it.
- **Hindsight ambiguity.** An instruction that is ambiguous only to a reader
  lacking the operator's context is not an ambiguous instruction.
- **Proposing an existing rule.** Cheap to avoid with a present-state check,
  and expensive in credibility.
- **Rule inflation.** Every friction becoming a new standing instruction
  produces an instruction set nobody can follow, which is itself a coordination
  cost. Weigh the proposed rule's permanence against the recurrence count.
- **Compliance as correctness.** Following every constraint in this lens does
  not make the interpretation right.
- **Re-minting what the frontier already holds.** One idea recorded four times
  across four runs, each copy carrying its own review history and nothing in any
  of them saying it is the fourth. Babel warns when a new candidate's statement
  closely overlaps an existing one, and records that warning on the candidate
  rather than dropping it — so a duplicate emitted here is a duplicate an
  operator has to read.

## Examples

A useful observation: across four unrelated sessions in the same repository,
the operator issued the same correction about where generated files belong;
each preceding instruction named the task without naming the location; the
pinned snapshot contains no written convention about it. Evidence: the four
corrections and the four preceding instructions, quoted with locators, plus the
absence of any convention document. Counter-evidence sought: an existing rule
elsewhere in the instruction set — searched, not found. Classification
`repeated-correction` with a count of four independent sessions, temporal
status `still-applicable`. Smallest durable change: one line in the
repository's standing conventions. Note that the observation says nothing about
anyone; it describes four instructions, four corrections, and one absent
document.

A useful ambiguity observation: an instruction said to update the client and
the tests; the work updated the client's tests but not the client's callers,
and the following message corrected it. Both readings are stated. Evidence:
the instruction, the change, the correction. Classification
`ambiguous-instruction`. The proposal is not "be clearer" — it is a concrete
default about what "and the tests" includes for this repository.

A useful positive observation: a session that opened with an explicit statement
of target, constraints, and acceptance criteria, produced a result accepted
without correction, and did so for a task class that elsewhere required
several rounds. Classification `effective-coordination`. It is worth keeping
because the enabling condition is transferable.

A candidate correctly rejected: three rephrasings of a design question, with no
work discarded and no correction issued. This is thinking, not friction. The
rejection is preserved with its reason.

An error to avoid, stated plainly: "the operator appeared frustrated and the
agent was overconfident" is not an observation. The valid version of the same
material is "the same instruction was restated three times, each time with more
specificity, and the intervening results each diverged in the same respect" —
with all four locators.
