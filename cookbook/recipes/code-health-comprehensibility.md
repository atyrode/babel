---
id: code-health-comprehensibility
version: 2
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: true
---

# Code health, maintainability, documentation, and comprehensibility

## Question

What in this code is harder to understand, change, or trust than it needs to
be — and what does the conversation record reveal about that which the code
alone does not?

Static inspection of a snapshot finds duplication, dead code, and missing
tests. It cannot find the thing this lens is for: the places where a human or
an agent had to reconstruct knowledge that was never written down. A
transcript records that reconstruction directly. Someone asked what a function
was for. Someone guessed a data flow wrong and corrected it two messages
later. Someone re-derived an invariant that lives only in a maintainer's head.
Someone wrote a workaround because the real mechanism was undiscoverable.

Each of those is evidence of a missing comprehension layer, located at a
specific symbol, and evidence of its cost, measured in the work it took. That
combination — a location plus a demonstrated cost — is what makes this lens
worth running over conversations rather than over code.

## Inclusion, exclusion, and ambiguity

Include: code no longer reachable, or reachable only by tests; complexity that
the record shows caused a wrong first attempt; an abstraction whose name or
shape misleads about what it does; an invariant enforced by convention with
nothing to check it; a missing test for a case the record shows mattered;
documentation absent where the record shows it was needed; documentation
present and contradicted by the code; and a comprehension layer that would
have prevented recorded confusion — a diagram, a doc comment explaining why, a
worked example, a named concept.

Include the inverse when the record supports it: a place where an existing
comment, test, or name demonstrably prevented a mistake. That is health, and
losing it in a rewrite is a real risk worth recording.

Exclude style. Formatting, naming preference, and layout are not health
unless the record shows a concrete cost. Exclude "add tests" as a general
proposition; include a specific untested case with the evidence that it
matters. Exclude wholesale rewrites: a proposal whose scope is "restructure
this package" is not reviewable, and the lens should instead name the smallest
change that removes the demonstrated cost. Exclude judgements about the
author.

Ambiguity: complexity is often necessary, and the record rarely says which.
Where an apparently convoluted mechanism has a reason, look for it before
proposing simplification — a constraint stated once in an old session is
common, and this lens must not propose removing a mechanism whose purpose it
failed to retrieve. Where documentation is absent, absence may be correct for
code whose readers all have the context; the evidence for a missing layer is
recorded confusion, not an empty doc comment. Dead code may be an in-progress
migration or a supported public surface.

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

- a question asked about the same symbol, mechanism, or file in more than one
  session;
- a wrong first attempt followed by a correction that reveals a non-obvious
  invariant;
- a workaround introduced next to a mechanism that already solved the problem;
- an abstraction that participants describe differently than its name implies;
- code changed repeatedly without its behaviour changing;
- a test that was narrowed, skipped, or asserted on something incidental;
- a comment or document contradicted by the code beside it;
- a concept named consistently in conversation and nowhere in the code;
- unreachable code, especially where a migration's other half landed;
- a repeated onboarding cost: the same explanation given more than once.

Two independent sessions hitting the same comprehension gap is the single
strongest cue this lens has. Retrieval rank is not a cue.

## Evidence and counter-evidence

Evidence, with locators on both sides — record and snapshot:

- the recorded question, wrong attempt, or correction, quoted;
- the symbol, file, and line at the pinned snapshot;
- reachability at the snapshot: callers, exported surface, test-only use;
- test presence and what a test actually asserts, not what its name says;
- history where available: how often a location changed, and whether behaviour
  changed with it;
- an experiment in a disposable clone — remove the suspected dead code, run
  the build and tests, and record what happened. This turns "appears unused"
  into an observation about the recorded snapshot and command environment.

Counter-evidence to seek:

- a reason for the complexity, stated in an older session or a commit message;
- a caller the analysis did not see: reflection, generated code, build tags, a
  different platform, an external consumer;
- a test that covers the case elsewhere;
- documentation that exists outside the searched scope;
- an in-flight migration that explains the apparent duplication;
- the possibility that the recorded confusion was about something adjacent.

## Temporal and present-reality checks

The record's complaint and the snapshot's code are separated by time, and this
lens is meaningless without checking both. A comprehension gap that a later
doc comment closed is `resolved`, and reporting it wastes review. A gap still
present at the snapshot is `still-applicable`, and the recorded confusion is
evidence of its cost.

`regressed` is worth watching for here: an explanatory comment deleted in a
refactor, a test removed with the code it covered, a name changed back to the
misleading one.

Experiments establish behaviour in the recorded snapshot and command
environment only. "Removing this compiles and passes" is a true statement
about that environment and not a claim about every consumer, platform, or build
configuration. Where the analysis cannot see all consumers, the status is
`unverifiable` and the observation says which consumers it could not check.

## Classifications and stopping conditions

- `unreachable-code` — no caller at the snapshot, with the experiment that
  supports it;
- `missing-test-for-recorded-case` — a specific case, with the record showing
  it mattered;
- `missing-comprehension-layer` — recorded confusion at a located mechanism;
- `misleading-abstraction` — name or shape contradicts behaviour;
- `unenforced-invariant` — a rule held by convention only;
- `documentation-contradicts-code` — both located;
- `repeated-explanation-cost` — the same knowledge transferred more than once;
- `healthy-pattern-at-risk` — an existing layer that demonstrably works;
- `complexity-without-recorded-cost` — the weakest class, and the honest place
  for "this looks hard to read".

Stop when the smallest change that removes the demonstrated cost can be named,
with locators for the cost and the location. Stop when counter-evidence
retrieval has produced a reason for the construct. Stop before proposing a
restructuring whose blast radius exceeds the evidence. Stop when the candidate
has become a decision question ("was this architecture right?") and hand it to
the decision-quality lens.

## Cross-session synthesis keys

Group by: file, symbol, and package at the snapshot; the concept the confusion
was about, normalized to a name; the mechanism class; and the kind of missing
layer.

Recurrence is a property every lens may use, and here it is the difference
between an annoyance and a finding: one person confused once is weak, the same
gap costing two independent sessions is strong, and a gap that recurs after
being closed is stronger still. Count independent sessions, not messages.
Sessions from the same day about the same task are one episode.

## Capability needs

- `corpus-search` is required, and this lens leans on it hardest: the cost side
  of every observation is retrieval over conversations.
- `repo-read` at a pinned snapshot is required for any present-tense claim.
  Without it, observations are historical only and must say so.
- `sandbox-exec` is what turns reachability and test-coverage guesses into
  results. Every such result is scoped to the recorded snapshot and command
  environment.

## Known failure modes

- **Style as health.** The most common way this lens produces noise.
- **Proposing a rewrite.** Unreviewable, and the evidence never supports it.
- **Deleting a mechanism whose reason was not retrieved.** The one failure here
  that can break working software; counter-evidence retrieval is not optional.
- **Confusing test presence with coverage.** A named test that asserts nothing
  relevant is worse than no test, because it hides the gap.
- **Reading confusion as an ability judgement.** The observation is that a
  mechanism was undiscoverable, never that a participant failed.
- **Snapshot-only conclusions.** Static findings a plain analyzer could produce
  add little; the corpus is the differentiator.
- **Compliance as correctness.** Every section filled in does not make the
  proposed change right.
- **Re-minting what the frontier already holds.** One idea recorded four times
  across four runs, each copy carrying its own review history and nothing in any
  of them saying it is the fourth. Babel warns when a new candidate's statement
  closely overlaps an existing one, and records that warning on the candidate
  rather than dropping it — so a duplicate emitted here is a duplicate an
  operator has to read.

## Examples

A useful observation: two unrelated sessions each ask why a particular
conversion happens twice; in both, the answer is an invariant about ordering
that appears in no comment; the pinned snapshot shows the code unchanged and
undocumented. Evidence: both questions with locators, the code, the absence of
any comment or test asserting the ordering. Counter-evidence sought: an older
session explaining it — found, and it explains the reason, which is exactly the
material the missing layer should contain. Classification
`missing-comprehension-layer` plus `unenforced-invariant`; smallest change: a
doc comment stating the invariant and a test asserting the ordering.

A useful dead-code observation: a migration's old path with no callers at the
snapshot; the record shows the migration completing; an experiment removing it
builds and passes. Classification `unreachable-code`, with the experiment's
environment recorded and the caveat that external consumers were not
observable.

A candidate correctly rejected: a long function flagged as complex, where the
record contains no confusion about it, no wrong attempt near it, and the
snapshot shows it well covered by tests. Classification would have been
`complexity-without-recorded-cost`; the rejection is preserved with its
reason.

An error to avoid: proposing removal of an apparently unused exported symbol
without checking build-tagged or platform-specific callers, then presenting the
sandbox's successful build as proof. The experiment tested one configuration,
and the observation must say so.
