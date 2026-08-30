---
id: decision-quality-operational-risk
version: 1
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec, public-research]
default: false
---

# Engineering decision quality and operational risk

> **Draft.** This lens ships for review and is not default-enabled. Its
> overlap with outcome integrity, code health, and security is not yet
> calibrated against a real corpus, and hindsight is the failure mode most
> likely to produce confident nonsense. Enable it explicitly, and expect its
> guidance to change with its version.

## Question

What was decided, on what information, with what alternatives considered — and
what did the decision cost when reality arrived?

Coding transcripts are one of the few places where a decision and its reasons
are recorded together, at the moment, before the outcome is known. That is
unusually good evidence: normally a decision's rationale is reconstructed
afterwards, contaminated by the result. A transcript preserves the assumption
as it was stated, the alternative that was mentioned and dropped, the
constraint that was assumed rather than checked, and the reversibility nobody
discussed.

The operational half is the consequence side: what happens when this is wrong.
Recovery, rollback, blast radius, data durability, and the ability to observe a
failure are decisions too, and they are usually made implicitly by not being
made.

## Inclusion, exclusion, and ambiguity

Include a candidate when a decision point is locatable and at least one of the
following is observable: the stated reasoning; an alternative raised; an
assumption that was not checked; a constraint that was assumed; a reversibility
or recovery property; or a consequence that later evidence shows.

Include decisions that were good and are worth keeping. A decision lens that
only revisits mistakes is a hindsight machine.

Include the absence of a decision where one was structurally required — a
migration with no rollback path, a destructive operation with no recovery, a
new dependency with no removal story, a schema change with no compatibility
consideration — but only when the requirement follows from the recorded
context rather than from a general principle.

Exclude hindsight as evidence. That the result was poor is not evidence that
the decision was poor, and this exclusion is the lens's central discipline. The
question is whether the reasoning used the information that was available *at
the time*, and availability must itself be evidenced.

Exclude judgements of competence, and exclude the challenger's temptation to
score participants. §5.4's rule applies with full force: criticism must be
grounded in evidence, consequences, missing checks, or concrete alternatives,
never in character, ability, emotion, or intent.

Exclude decisions whose alternatives cannot be stated concretely. "They should
have designed it differently" is not an observation.

Ambiguity is the norm. Constraints are frequently unrecorded — a deadline, a
platform limitation, an operator preference expressed in another channel — and
a decision that looks unjustified is often justified by something the corpus
does not contain. Reversibility is often unknown without the present snapshot.
Where information availability cannot be established, say so and stop; that is
a complete result.

## Sorting cues

- an assumption stated as fact and never checked, where checking was cheap;
- an alternative raised in one message and never addressed;
- a decision whose cost is irreversible — data shape, public interface,
  dependency, deletion — made in passing;
- an operation with no recorded recovery path;
- a constraint inferred rather than confirmed;
- a later session paying a cost that traces to the decision;
- a repeated decision made differently in comparable contexts;
- a dependency added to solve a problem the record shows already solved;
- a decision that a stated constraint contradicts;
- a migration or cutover without a described failure mode.

Strong cue: an alternative that was explicitly raised, explicitly dropped
without reasons, and later adopted anyway. Retrieval rank contributes nothing.

## Evidence and counter-evidence

Evidence, with locators:

- the decision as stated, quoted;
- the information demonstrably available at that time — earlier records, the
  repository state then, the tool output in hand;
- alternatives named in the record, and how they were disposed of;
- the assumption, and whether a cheap check existed for it;
- reversibility at the pinned snapshot: what would it now take to undo;
- consequences with independent evidence: a later failure, a rework episode, a
  measured cost — never a narrative;
- an experiment in a disposable clone measuring a property the decision
  assumed, scoped to the recorded snapshot and command environment;
- authorized public research about a chosen construct's known properties, as
  background only.

Counter-evidence to seek:

- constraints outside the corpus that would justify the choice;
- the alternative's own costs, which are frequently why it was dropped;
- evidence that the assumption was in fact checked elsewhere;
- the decision having been revisited deliberately later;
- the possibility that the consequence attributed to the decision has another
  cause;
- the possibility that the information the analysis is using was not available
  at the time — the single most important check in this lens.

## Temporal and present-reality checks

Two times must be kept separate and both must be stated: **decision time** and
**now**.

At decision time, the only admissible information is what the record shows was
available. Anything else is hindsight and invalidates the observation.

Now, at the pinned snapshot, the questions are different: does the decision
still bind, what would reversing it cost today, and has the constraint that
motivated it expired? A decision that was right then and is wrong now is a
maintenance finding, not a criticism, and saying so correctly is most of this
lens's value.

Use `historical` for a decision no longer in force, `still-applicable` where it
still binds, `resolved` where it was revisited, `regressed` where a corrected
decision was undone, `contradicted` where present evidence disproves the
assumption, and `unverifiable` where availability or consequence cannot be
established.

## Classifications and stopping conditions

- `unchecked-assumption` — stated as fact, cheaply checkable, unchecked;
- `alternative-dismissed-without-reasons` — raised, dropped, unexplained;
- `irreversibility-unconsidered` — a hard-to-undo choice made in passing;
- `recovery-path-absent` — an operation whose failure has no recorded recovery;
- `constraint-assumed` — a limit taken as given without confirmation;
- `consequence-observed` — a cost with independent evidence linking it to the
  decision;
- `decision-still-sound` — the positive class, kept deliberately;
- `expired-rationale` — right then, no longer applicable now;
- `insufficient-context-to-assess` — information availability cannot be
  established. Expect this often, and prefer it to a guess.

Stop when the decision, the information available at the time, and either an
alternative or a consequence are located. Stop as soon as the assessment would
need information the participants did not have. Stop before proposing a
redesign; the reviewable output is a specific reversible step or a check to
add, not an architecture.

## Cross-session synthesis keys

Group by: decision class; the assumption's subject; the entity or repository
affected; reversibility class; and whether the consequence was observed or
projected.

Recurrence is available to every lens, and here the valuable recurrence is a
*pattern of reasoning*: the same class of assumption left unchecked across
unrelated decisions, or the same alternative repeatedly dropped for the same
unstated reason. One decision is a decision; five sharing a shape is a
practice, and a practice is what a proposal can address. Count independent
sessions and independent decisions separately.

## Capability needs

- `corpus-search` is required, both for the decision's context and for its
  later consequences.
- `repo-read` at a pinned snapshot establishes present reversibility and
  whether the decision still binds.
- `sandbox-exec` can measure a property the decision assumed. Results are
  scoped to the recorded snapshot and command environment and are never a
  claim about the environment at decision time.
- `public-research` is optional background about a construct's documented
  properties. Brokered, with no private material in any request, and never
  evidence about this corpus.

## Known failure modes

- **Hindsight.** The dominant failure. Any reasoning of the form "we now know
  X, therefore the decision was poor" is invalid unless X was available then.
- **Scoring participants.** Explicitly forbidden; the observation addresses
  decisions, evidence, and consequences.
- **Free alternatives.** Proposing an alternative without its costs is not a
  comparison.
- **Attribution.** Linking a later failure to a decision requires evidence for
  the link, not adjacency.
- **Invented requirements.** Demanding a rollback plan for a trivially
  reversible change manufactures risk.
- **Overlap noise.** Much of what this lens finds is better reported by outcome
  integrity, code health, or security. Route it there rather than
  double-reporting; this overlap is why the lens is a draft.
- **Compliance as correctness.** A complete decision observation can be wrong.

## Examples

A useful observation: a record shows a choice to store a value in a shared
format, with the stated assumption that a single writer would exist; the
assumption was checkable at the time from code the session had open; a later
session shows a second writer being added and a corrective change following.
Evidence: the decision, the code available then, the second writer, the
correction. Counter-evidence sought: an out-of-band constraint — none in the
corpus; the alternative's cost — recorded and modest. Classification
`unchecked-assumption` plus `consequence-observed`, temporal status
`still-applicable`. The proposal is a check, not a redesign.

A useful positive observation: a decision to accept a slower implementation to
keep a boundary narrow, with the trade-off stated at the time and the boundary
still intact at the snapshot. Classification `decision-still-sound`; worth
recording because it is the kind of decision later work erodes without
noticing.

A candidate correctly rejected: a dependency choice criticized because a
better-regarded option exists today. The option's present standing is not
evidence about what was available or reasonable at decision time.
Classification `insufficient-context-to-assess`.

An error to avoid: treating a production incident as proof that the decision
preceding it was poor. The incident is evidence about consequences; the
decision's quality still depends on the information available when it was made.
