---
id: reusable-practice-capability-leverage
version: 2
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, public-research]
default: false
---

# Reusable practice and capability leverage

> **Draft.** This lens ships for review and is not default-enabled. Its
> overlap with the effective-patterns lens is real and not yet calibrated:
> patterns describe what worked, this lens proposes building something. The
> boundary and the cost discipline below are what the draft period is meant to
> test. Enable it explicitly, and expect its guidance to change with its
> version.

## Question

What was done by hand, repeatedly, that a tool, a script, a written procedure,
or a documented skill could do once — and what would that cost to build and to
keep?

A corpus of coding sessions is an unusually direct record of manual effort. It
shows the same sequence of commands being reconstructed, the same context being
assembled before work can start, the same check being performed from memory,
the same explanation being written again. Each instance is small. The recurring
total is not, and it is invisible from inside any single session.

The second half of the question is what stops this lens from generating a
backlog of half-useful tools. Every proposed capability has a build cost, a
maintenance cost, a failure mode, and a prerequisite; a proposal that names the
opportunity without them is not reviewable. The interesting output is often not
"build this" but "this recurring cost exists, and here is the smallest thing
that removes it" — frequently a written procedure rather than software.

## Inclusion, exclusion, and ambiguity

Include: a manual sequence repeated across independent sessions; context
assembly repeated before comparable tasks; a check performed from memory that a
script could perform; a procedure that succeeded and is written nowhere; a
capability whose absence the record shows being worked around; a piece of
knowledge transferred repeatedly by explanation; an existing tool that the
record shows was not discoverable at the moment it was needed.

Include the project's delivery pipeline as a capability in its own right
(standing emphasis, cookbook statement, 2026-09-02): a check performed by hand
or in review that CI could perform; a pipeline that does not run on the branch
or pull-request path where the work lands; a gate that exists only as a command
contributors are asked to run; a release, tag, or deployment step done by hand
repeatedly; a check the record shows drifting from what the project ships.
These are the same recurring manual costs as the rest of this list, with the
difference that the tool that removes them usually already exists and is
merely not wired in.

Include the prerequisites and the recurring costs, always. A proposal for a
capability includes what must exist first — data, access, a stable interface, a
convention — and what keeping it alive requires.

Include the option of doing nothing. A recurrence with a low total cost and a
high maintenance cost is a real finding whose conclusion is "leave it manual",
and recording it prevents the same proposal from being generated repeatedly.

Exclude speculative tooling. A capability nobody exercised manually is a
product idea, not an observation about this corpus, and it belongs to open
discovery rather than here.

Exclude anything whose only evidence is that a tool would be nice. The
evidence is recurrence of manual effort with locators.

Exclude a proposal that requires an authority Babel does not have. Nothing
here proposes an automation that would mutate a repository, publish, or act on
an external system as part of analysis; the output is a suggestion for the
operator's own systems, reviewed by the operator.

Ambiguity: repetition is not automatically waste. A sequence repeated three
times in one afternoon may be exploration; the same sequence across three
months is a procedure. Some manual work is deliberate — a human checkpoint
before something irreversible is a control, not an inefficiency, and proposing
its automation is a security and decision-quality regression. Where the record
cannot distinguish a control from a chore, the observation says so and does not
propose removing it.

## Sorting cues

- the same command sequence, in the same order, in independent sessions;
- the same context assembled before comparable tasks — the same files opened,
  the same state summarized;
- a check that the record shows being performed inconsistently, which is both
  the cost and the risk;
- an explanation given more than once to different audiences or in different
  sessions;
- a workaround for a missing capability, especially one described as temporary;
- a procedure that succeeded under time pressure and was never written down;
- an existing tool re-implemented ad hoc because it was not found;
- repeated setup that a template, default, or generator would remove;
- a manual step that consistently precedes an error.

Strong cue: the same manual sequence performed by different sessions in
different repositories, which suggests a general capability rather than a local
script. Retrieval rank contributes nothing.

## Evidence and counter-evidence

Evidence, with locators:

- each independent occurrence of the manual work, quoted or summarized with its
  locator;
- the sequence itself, precisely enough that a procedure could be written from
  it;
- the recurring cost, measured in what the record shows: number of steps,
  number of sessions, corrections caused by inconsistency;
- prerequisites, evidenced at the pinned snapshot: does the interface the tool
  would depend on exist and is it stable;
- whether the capability already exists — in the repository, in the toolchain,
  in a standing procedure — which is the most common reason a candidate here
  should be dropped;
- authorized public research into whether a well-maintained tool already does
  this, as background only.

Counter-evidence to seek:

- the capability already exists and was not found, which changes the finding
  from "build" to "make discoverable";
- the manual step is a deliberate control;
- the recurrence is one task repeated, not a general pattern;
- the sequence varies more than it appears, so automating it would encode the
  wrong variant;
- the prerequisite interface is unstable, which makes the maintenance cost
  dominate;
- the total manual cost is smaller than the build and maintenance cost.

The last two are the counter-evidence this lens most often skips, and they are
the ones that decide whether a proposal is worth an operator's attention.

## Temporal and present-reality checks

Check the present before proposing anything:

1. Does the capability exist now at the pinned snapshot? Recurrence observed
   over months is frequently already solved.
2. Do the prerequisites hold now — the interface, the data, the convention?
3. Is the manual work still happening in the most recent sessions, or did it
   stop? A cost that ended is `historical` and needs no tool.

Use `still-applicable` when recent sessions still show the work,
`historical` when it stopped, `resolved` when a capability now covers it,
`regressed` when a capability existed and stopped being used, and
`unverifiable` when present practice is not observable. A proposal to build
something whose subject stopped happening is the most visible waste this lens
can produce.

## Classifications and stopping conditions

- `repeated-manual-sequence` — independent occurrences, sequence stated;
- `undocumented-procedure` — worked, repeated, written nowhere;
- `missing-capability` — a workaround for something that does not exist;
- `discoverability-gap` — it exists and was not found. Cheapest to fix and
  frequently the true diagnosis;
- `knowledge-transfer-cost` — the same explanation more than once;
- `automation-with-unfavourable-cost` — real recurrence, and the honest
  conclusion is to leave it manual;
- `deliberate-manual-control` — repetition that must be preserved;
- `prerequisite-blocked` — worth building once something else exists, named.

Stop when the sequence, its independent occurrences, its prerequisites, and its
maintenance cost are all stated. Stop when the counter-evidence shows the
capability exists — and report the discoverability gap, which is a better
finding. Stop before designing the tool: the reviewable output is the
opportunity, its evidence, its prerequisites, and its costs, not an
implementation plan.

## Cross-session synthesis keys

Group by: the sequence's normalized shape; the task class; the repository or
entity; the capability class — script, template, procedure, check, generator,
interface; and the prerequisite it depends on.

Recurrence is a property every lens may use; here it is the measure of value,
because the benefit of a capability scales with occurrences while its cost does
not. Count independent sessions and independent contexts, and state both: the
same sequence in five sessions inside one repository suggests a local script,
while three sessions across three repositories suggests a general capability
with a broader maintenance obligation. A finding must say which conclusion its
counts support.

Cross-reference the effective-patterns lens: a pattern with named enabling
conditions plus a repeated manual sequence is the strongest input this lens
gets, because the practice is already validated and only its packaging is
missing.

## Capability needs

- `corpus-search` is required: recurrence is the evidence.
- `repo-read` at a pinned snapshot is required for two checks — whether the
  capability already exists, and whether its prerequisites hold. Without it,
  every proposal risks proposing what exists, and observations must say the
  check was impossible.
- `public-research` is optional background about existing tools. Brokered, no
  private material in any request, never evidence about this corpus.
- No execution capability. This lens does not need to run anything, and a
  candidate that requires an experiment belongs to the lens whose question the
  experiment answers.

## Known failure modes

- **Proposing what exists.** The most common and most credibility-damaging
  failure, and the reason the present-state check is mandatory.
- **Ignoring maintenance.** A tool with a recurring cost larger than the manual
  work it replaces is a net loss, and the proposal must carry the numbers it
  can support.
- **Automating a control.** Removing a deliberate human checkpoint is a
  regression in two other lenses' terms.
- **Encoding the wrong variant.** Sequences that look identical often differ in
  a step that matters; the evidence must show the invariant part.
- **Designing instead of observing.** An implementation plan is not an
  observation and cannot be reviewed as one.
- **Speculative capability.** No manual recurrence, no finding.
- **Overlap with effective patterns.** State the boundary explicitly in each
  observation: what worked is that lens, what should be built is this one.
- **Compliance as correctness.** Filling in every section does not make the
  opportunity real.

## Examples

A useful observation: six sessions across two repositories each begin by
assembling the same three pieces of state before a comparable task, in the same
order, with one session omitting a piece and producing a correction. Evidence:
the six openings with locators, the omission, the correction. Prerequisites:
the state is derivable from files present at the snapshot. Counter-evidence
sought: an existing helper — none in the snapshot; a documented procedure —
none. Costs: a short procedure document removes the omission risk; a generator
would additionally remove the assembly cost and would depend on a file layout
the record shows changing twice. Classification `repeated-manual-sequence` plus
`undocumented-procedure`, with the recommendation scoped to the document
because the generator's prerequisite is unstable.

A useful discoverability observation: three sessions implement an ad hoc
version of a check that exists at the snapshot, in a location the sessions
never open. Classification `discoverability-gap`. The proposal is a pointer,
not a tool.

A candidate correctly rejected: a five-step sequence performed twice in one
afternoon during exploration of a new interface, with no recurrence since.
Classification `insufficient recurrence`, recorded as
`automation-with-unfavourable-cost` with the reasoning preserved so the
candidate is not regenerated.

An error to avoid: proposing automation of a manual confirmation step that
precedes an irreversible operation. The repetition is real, and the step is the
control. The correct observation records it as
`deliberate-manual-control` and proposes nothing.
