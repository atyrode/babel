---
id: effective-patterns
version: 2
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: true
---

# Effective patterns and enabling conditions

## Question

What worked, why did it work here, and what would have to be true elsewhere for
it to work again?

Almost every analytical instrument points at failure, and the corpus therefore
records success without anyone extracting it. That asymmetry is expensive in a
specific way: a procedure that reliably produced good outcomes is rediscovered
by accident, applied in a context where its preconditions do not hold, or lost
when the person who used it stops.

The second half of the question is what keeps this lens honest. A pattern
without its enabling conditions is a superstition. The conditions are usually
observable — a test suite that was fast enough to run per change, a
specification that already named the constraint, a small blast radius, an
interface that was already isolated — and naming them is what distinguishes
transferable practice from a story about one good day.

## Inclusion, exclusion, and ambiguity

Include a candidate when three things are locatable: an approach, an outcome
that the record or the snapshot shows was good, and at least one condition that
plausibly enabled it. Good outcome means something better than "the agent said
it worked": a verification that passed, a change that later sessions did not
revisit, a first-time-correct delivery of a task class that usually iterates,
an ambiguity resolved before work started, a failure caught early and cheaply.

Include patterns of any kind: an ordering of steps, a way of stating a request,
a decision to write a test first, a decision *not* to abstract, a preflight
check, a rollback plan, a way of splitting a task, a habit of quoting the
constraint back before starting.

Include the limits. A pattern's known counterexample is part of the pattern,
and a finding that omits the case where the same approach did poorly is
misleading rather than incomplete.

Exclude success attributed to the outcome alone — post-hoc pattern-fitting is
the standing hazard here, and a "pattern" that is only visible because the
result was good is not evidence. Exclude single occurrences presented as
practice: one success is a candidate, not a pattern, and the observation says
which it is. Exclude anything requiring a claim about a participant's skill.
Exclude generic engineering advice that the corpus did not produce.

Ambiguity: attribution is genuinely hard. When several things differed between
a successful episode and an unsuccessful one, the record usually cannot isolate
the cause, and the honest observation lists the candidate conditions rather
than selecting one. Success may also be the task's, not the approach's — easy
work goes well. The strongest available evidence is a contrast: the same task
class, handled two ways, with different outcomes.

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

- a task class that usually iterates, delivered correctly the first time;
- an explicit statement of acceptance criteria before work started;
- verification that ran before a claim was made rather than after a question;
- a change that later sessions never revisited, in code they did touch;
- a constraint restated by the agent before acting on it;
- a decision to reduce scope that the record shows preserved the outcome;
- a check that caught a problem while it was still cheap;
- a rollback or containment step that limited a failure's cost;
- a procedure repeated across unrelated tasks with consistent results;
- an explicit "this worked well" from the operator — weak alone, useful as a
  pointer.

A contrast pair — same task class, different approach, different outcome — is
worth more than any number of successes on their own. Retrieval rank
contributes nothing.

## Evidence and counter-evidence

Evidence, with locators:

- the approach as it happened, in order, quoted where the wording matters;
- the outcome, evidenced by verification, by absence of later revisiting, or by
  repository state at the pinned snapshot;
- the conditions present: what already existed, what was already known, how
  large the change was, what the feedback loop cost;
- the comparison case: the same task class handled differently, with its
  outcome;
- recurrence: independent sessions where the approach appears, and what
  happened each time.

Counter-evidence to seek, and this lens must seek it harder than the others,
because confirmation is so easy here:

- occurrences where the same approach produced a poor outcome;
- an alternative explanation for the good outcome — a simpler task, an existing
  test suite, a well-isolated interface, prior work in the same area;
- later reversal: work that looked good and was undone;
- conditions that are not transferable — a specific person's context, a
  one-off state of the repository, a constraint that no longer holds;
- selection effects: successful sessions may be shorter, more likely to be
  complete in the archive, and easier to search.

An observation whose counter-evidence section says "none sought" is not usable
here.

## Temporal and present-reality checks

A pattern is a claim about the future, so its conditions must be checked
against the present, not only the past. Three checks:

1. Do the enabling conditions still hold at the pinned snapshot? A pattern that
   depended on a fast test suite is not transferable to a repository where the
   suite is now slow.
2. Has the pattern already been adopted? If it is standing practice, the
   finding is that it works, not that it should start.
3. Has the environment changed underneath it? Tooling, interfaces, and
   conventions move, and a pattern whose mechanism no longer exists is
   `historical`.

Use `still-applicable` when the conditions hold now, `historical` when they do
not, `resolved` when the practice is already adopted, `regressed` when an
adopted practice lapsed, and `unverifiable` when the conditions cannot be
observed. Never present a past success as a present recommendation without
this check.

## Classifications and stopping conditions

- `repeatable-practice` — several independent occurrences, conditions named;
- `single-success-candidate` — one occurrence, worth watching, explicitly not
  yet practice;
- `enabling-condition` — a condition that several successes share, which is
  often more valuable than any single procedure;
- `contrast-supported-pattern` — the strongest class: same task class, two
  approaches, different outcomes;
- `pattern-with-known-limit` — includes the counterexample and its boundary;
- `lapsed-practice` — worked, was adopted, and is no longer followed;
- `already-standard` — works and is already the default; no proposal needed
  beyond preserving it;
- `unattributable-success` — a good outcome whose cause the record does not
  isolate. Frequently the correct answer.

Stop when the pattern, its outcome evidence, its conditions, and its known
limit are all located. Stop when counter-evidence has produced a comparably
good alternative explanation — and record that, because "we cannot tell what
made this work" is a useful result. Stop before generalizing beyond the
contexts the evidence covers; the scope of a pattern is part of the finding,
and an unbounded one is a slogan.

## Cross-session synthesis keys

Group by: task class; the procedure's shape, normalized; the enabling condition
class; the repository or entity; and the outcome evidence type.

Recurrence is available to every lens; here it is the promotion criterion.
Count independent sessions and independent task classes separately: five
successes in one task class support a narrow pattern, while three across
unrelated classes support a general one. Say which. A pattern supported by
occurrences that all share one enabling condition is a finding about the
condition at least as much as about the procedure.

Synthesis should also cross-reference the coordination and outcome-integrity
lenses' positive observations, which are produced in the course of looking for
their own failures and are exactly the raw material this lens consolidates.

## Capability needs

- `corpus-search` is required: a pattern is a recurrence claim, and recurrence
  is retrieval.
- `repo-read` at a pinned snapshot is needed for two checks — whether the good
  outcome survived, and whether the enabling conditions still hold. Without
  it, every transferability claim degrades to `unverifiable`.
- No execution capability is required. If a candidate needs an experiment to
  establish that something worked, the outcome evidence is too weak for this
  lens and the candidate belongs to the lens whose question it answers.

## Known failure modes

- **Post-hoc pattern-fitting.** Constructing a procedure out of a successful
  session's incidental details. The defining hazard of this lens, and the
  reason enabling conditions and contrast cases are mandatory.
- **Survivorship.** Successes are easier to find, shorter, and better
  preserved; treating their frequency as evidence of a pattern's reliability
  overstates it.
- **Pattern without preconditions.** Produces advice that fails in the next
  context and discredits the whole cookbook.
- **Crediting people.** Out of bounds; the observation describes procedures and
  conditions.
- **Proposing what is already standard.** Fix with a present-state check.
- **Cargo culting a name.** Recording that a practice has a fashionable label
  is not evidence that it caused anything in this corpus.
- **Compliance as correctness.** A fully populated pattern observation can
  still be a coincidence.
- **Re-minting what the frontier already holds.** One idea recorded four times
  across four runs, each copy carrying its own review history and nothing in any
  of them saying it is the fourth. Babel warns when a new candidate's statement
  closely overlaps an existing one, and records that warning on the candidate
  rather than dropping it — so a duplicate emitted here is a duplicate an
  operator has to read.

## Examples

A useful contrast-supported observation: for a task class that elsewhere
required several corrections — changing a shared interface — two sessions
stated the full list of call sites before editing, and both delivered without a
correction; three sessions that began editing immediately each required a
follow-up for a missed call site. Evidence: the five sessions' opening moves
and outcomes, with locators, plus repository state showing the call sites.
Conditions named: the call sites were enumerable statically, and the repository
was small enough for that enumeration to be cheap. Known limit: one session
where enumeration missed a generated caller. Classification
`contrast-supported-pattern`, temporal status `still-applicable`.

A useful enabling-condition observation: four unrelated successful episodes
share one condition — the acceptance criterion was written down before work
started — and the procedures otherwise differ. Classification
`enabling-condition`. This is more valuable than any of the four procedures,
and it is only visible across sessions.

A candidate correctly rejected: a session that went smoothly and happened to
begin with a summary of the codebase, presented as evidence that summaries
cause smooth sessions. No contrast case, no condition analysis, and the task
was a one-line change. Classification would have been
`unattributable-success`; the rejection is preserved with its reason.

An error to avoid: reporting "small, verified changes work well" as a finding.
It is unbounded, unfalsifiable from this corpus, and not actionable. The
version worth reviewing names the task class, the size threshold the evidence
actually supports, the verification that was available, and the case where it
did not hold.
