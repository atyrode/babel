---
id: mechanization-audit
version: 1
kind: meta
scope: [corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: false
---

# Mechanization audit

> **Standing duty, off by default.** This recipe is never part of a default
> selection. It runs when the operator has authorized the product
> self-improvement duty — `babel conductor configure --babel-improves-babel` —
> or when it is named explicitly. Its findings are phrased for anyone and reach
> the public codebase as proposals; the operator-specific dimension is a
> different recipe and a different toggle.

## Question

Where did a run infer context that code could have served — and what would have
served it?

The principle is directional: the more of a run's work is mechanized, the more
of its spend is genuine thinking. A run that reconstructs the same context four
times, re-derives what a prior run already established, issues the same search
in three sessions, or exhausts its budget before it reaches a hypothesis has
spent the operator's money on retrieval that a retrieval surface should have
performed. The waste is not the tokens; the waste is that the thinking started
late or never started at all.

**The metric is mechanization, not frugality.** A cheaper run is not the goal
and is not evidence of improvement. The quantity this recipe reasons about is
the division of a run's budget between synthesis and context reconstruction, and
the proposal is always to move work from the second column into code — never to
shrink the first.

**Anti-convergence guardrail, binding on every output of this recipe.**
Efficiency pressure applies only to substrate: retrieval, tooling, context
assembly, the job document, the way evidence reaches a run. It never applies to
hypothesis content or to the diversity of what may be proposed. No recipe may be
graded on producing cheaper thoughts; the only thing gradeable here is how much
budget is wasted before thinking starts. A proposal whose effect would be fewer
candidates, narrower exploration, shorter reasoning, or fewer challenges is
outside this recipe's subject and is a defect of the audit, whatever it saves. A
finding that recommends thinking less has answered a different question than the
one asked.

## Inclusion, exclusion, and ambiguity

Include a candidate when the substrate cost and the mechanization that would
prevent it are both named and located. The evidence is already recorded and
needs no new instrumentation:

- run receipts: every evidence request with its decision, the denials, the
  retrieval trace with its steps, the counts of tool requests, tools denied,
  retrieval steps, deferrals and rejections, the failures, the timing;
- the worker boundary's own record inside the receipt: the profile reference,
  the capability grant, each tool request and how it was decided, the reported
  cost and the boundary's own failures;
- the usage the harnesses recorded for Babel's own exploration runs, which are
  ordinary archived sessions: assistant turns, how many carried usage at all,
  input, output and cache tokens, the harness's own priced cost, tool calls and
  tool errors;
- the preparation each run read, which says what corpus was in scope;
- the frontier: what the run produced, deferred and rejected, and what a prior
  run had already established about the same subject.

Include repeated retrieval across runs. The same query, or two queries that
differ only in wording, issued by independent runs over overlapping corpus, is
the clearest mechanizable pattern available: the second run paid to discover
what the first already found, and neither run's guidance was at fault.

Include budget-exhaustion points. A run whose deferred count is large and whose
developed records are few, with a receipt showing most of its tool requests
spent on retrieval, is a run that ran out of budget in the wrong column. Rank by
where the exhaustion happened, not by what it cost.

Include denials. A denied tool request is a run inferring around a boundary: it
either re-derived the context by other means or gave up on it, and both are
recorded. A denial pattern is either a missing brokered surface or a grant that
should never have been requested, and the receipt says which.

Include mechanizations that are not new tools. A pre-computation step in a
recipe's own preparation, a job-document field that carries what every run
searches for first, a memory registration that stops a fact being re-derived, an
adapter extraction that makes a metadata field available without a scan, an
index that makes an existing search cheap — these are usually better proposals
than a new tool, because they remove the request rather than serving it faster.

Exclude prompt tweaks as a first resort. "Tell the model to search before
inferring" is guidance that mostly already exists, and adding a sentence to a
recipe is the cheapest thing to propose and the least likely to work. A guidance
change is admissible only when the receipt shows the surface existed, was
reachable, was affordable, and was not used.

Exclude any proposal that reduces what may be thought. Fewer candidates, fewer
challenger passes, a lower deferral rate reached by exploring less, a tighter
inclusion rule to save tokens: all outside the subject, however well evidenced
the saving is.

Exclude cost comparisons between runs whose profiles, scopes, or recipes differ,
presented as efficiency. Two runs over different corpora are not two attempts at
one task, and a rate built from them measures the draw rather than the substrate.

Exclude any claim that treats an unpriced run as free. A run whose harness
recorded no usage, or whose profile reported no cost, is unmeasured — a zero
would be a measurement nobody made.

Ambiguity is the normal state of attribution here. A run's tokens are not
labelled synthesis or reconstruction, and the split has to be argued from what
the receipt shows the run was doing: retrieval steps, tool requests, and the
order they happened in. Where the record cannot separate the two, the
observation says so and reports the mechanizable incident rather than a ratio.
An incident with a named prevention is worth more than any ratio.

Search the frontier before minting. Babel indexes its own outputs; the job
document lists prior related records, and the same search is available with
`"scope": "frontier"`. This recipe will meet the same receipts on every draw, so
where a prior record already names the mechanization you would propose, emit
against it — refine it, develop another observation onto it, or revive it if it
came to rest, naming its record id — and where a prior proposal was declined,
read the reason before proposing it again. Proposing the same retrieval surface
every day is itself a failure to mechanize.

## Sorting cues

- the same search, or a near-identical one, in the retrieval traces of
  independent runs over overlapping corpus;
- a run whose recorded retrieval steps exceed its developed observations by a
  wide margin;
- repeated denials of one request kind across runs;
- a fact re-established in several runs that is already an authorized Reality
  fact or an accepted note — context that was held and not reached;
- runs whose deferred candidates outnumber their developed ones, repeatedly,
  with retrieval-heavy receipts;
- a metadata field several runs each recomputed from raw transcripts;
- tool errors clustered on one request kind: a surface that exists and does not
  work is a mechanization already paid for and not delivered;
- a job document that every run's first act was to supplement in the same way;
- two runs, same subject, months apart, neither aware of the other's record.

Weak cues: total cost, token count, run duration, and any per-run average.
Retrieval rank contributes nothing.

## Evidence and counter-evidence

Evidence, with locators, always:

- the receipts, named, with the specific requests, denials and retrieval steps
  the claim rests on;
- the recorded usage of the runs in question, with what it does and does not
  cover — how many turns carried usage at all, and whether the cost was priced;
- the repeated material itself: the two queries, the two records, the two
  derivations, quoted;
- the prior record or stored fact that already held the context, where the claim
  is that something was re-derived;
- for a proposal shaped as code, the repository state at the pinned snapshot
  showing the surface does not already exist, or exists and is unreachable from
  where the run needed it;
- the order of events: a receipt's timing and its retrieval trace are what
  distinguish "reconstructed context before starting" from "checked its work
  afterwards", and the second is not waste.

Counter-evidence to seek:

- the surface already exists: the most common wrong finding here is a proposal
  for something Babel has, reachable by a name the analysis did not search for;
- the repetition was warranted: two runs over different corpus slices
  legitimately issue the same query, because the answer is scoped to the slice;
- the cost was not where it looked: an unpriced or partially measured run makes
  any split a guess, and the receipt says how much of the usage was recorded;
- the run was cheap and the waste was elsewhere: a preparation that scanned a
  large corpus costs Babel time and no tokens at all;
- the mechanization would cost more than it saves: a new brokered surface has a
  grant, a version, a threat model and a maintenance cost, and a proposal that
  ignores them is not a proposal;
- the alternative explanation for a large deferral count: a rich corpus slice
  and a finite run is exactly the behaviour the frontier exists to support, and
  deferral is not failure.

An observation whose counter-evidence section says "none sought" is not usable
here.

## Temporal and present-reality checks

1. Does the surface exist now? Read the repository at the pinned snapshot before
   proposing anything shaped as code. A proposal for something that shipped
   since the receipts were written is `resolved`.
2. Were the receipts written under a build that still resembles this one? A
   denial pattern from before a capability existed is `historical`, and
   reporting it as a present gap wastes a review.
3. Is the repetition still happening? Recent receipts settle it. A pattern that
   stopped is `resolved` and worth recording as such — it says a mechanization
   worked.

Use `still-applicable`, `historical`, `resolved`, `regressed` (a mechanized path
that is being bypassed again), `contradicted` (the receipts show the surface was
used after all), and `unverifiable` (the usage was not recorded well enough to
support the claim).

## Classifications and stopping conditions

- `repeated-retrieval` — the same context found more than once by independent
  runs, with the retrieval steps quoted;
- `re-derived-held-context` — the run reconstructed something Babel already held
  as a fact, a note, or a prior record;
- `missing-retrieval-surface` — a request that had to be inferred around because
  no surface serves it, with the denial or the workaround in the receipt;
- `unreachable-existing-surface` — the surface exists and the run could not get
  to it: wrong scope, wrong stage, absent from the job document, or not granted;
- `precomputation-candidate` — context every run of a recipe assembles for
  itself that preparation could assemble once;
- `broken-mechanization` — an existing surface whose errors made runs fall back
  to inference. Highest value class: the cost was already paid;
- `budget-exhaustion-point` — where runs stop having budget, and what was
  consuming it, ranked by the incident rather than by the total;
- `warranted-repetition` — checked and correct as it stands. Worth recording;
- `unmeasured` — the usage does not support a split. Frequently the honest
  answer.

Stop when the incident is located in named receipts, the prevention is named,
and the present-state check has been done. Stop before proposing a new brokered
surface without stating its grant, its cost and what would enforce it — an
unbounded proposal for a new capability is a proposal to widen Babel's authority
in the name of efficiency, which is the one trade this recipe may never make.
Stop when the honest output is that the substrate is fine and the run was simply
doing a large job.

No output of this recipe changes anything. Every finding is a proposal against
the public codebase, reviewed by a person, and this recipe has no authority over
the retrieval, tooling, or guidance it is analyzing.

## Cross-session synthesis keys

Group by: the request kind or query subject; the recipe and version the run
applied; the surface proposed; the incident class; and whether the context was
held, holdable, or genuinely new.

Recurrence across runs is the definition of the subject here, not a bonus:
mechanization is worth building exactly where the same work happens more than
once. Count independent runs and independent subjects separately — one subject
searched five times by one run is a run that could not find something, while one
subject searched by five runs is a missing surface — and report both counts,
because the proposal differs completely between them.

Synthesis should cross-reference the quality recipe's findings: a duplicate
cluster it reports as a quality problem is frequently a retrieval failure this
recipe can name the prevention for, and the two should be emitted as one
proposal against the surface rather than as two records about the same
mechanism. A mechanization found here benefits every recipe, not only the
Babel-topic ones: better recollection and fewer redundant searches change the
substrate of every analysis Babel performs.

## Capability needs

- `corpus-search` is required. The receipts, the runs' own archived sessions and
  the frontier are all read through it, and every recurrence claim in this recipe
  is a retrieval over them.
- `repo-read` at a pinned snapshot is required before any proposal shaped as
  code. Without it the "does this already exist" check cannot be made, every
  such proposal degrades to `unverifiable`, and the recipe's most common failure
  mode is exactly the one that check prevents.
- No `sandbox-exec`: a mechanization proposal is reviewed and built by a person,
  and running Babel against itself to measure a saving is a separate,
  operator-initiated activity with its own containment.
- No `public-research`: every input is local and durable.

Dispositions this recipe emits: `draft-issue` for a proposal against the public
codebase, `ask-question` where the receipts cannot settle whether a surface was
reachable, and `develop-further` where an incident needs another pass. A
`draft-issue` binds to a repository verified from a local checkout's own git
configuration, discovered from the session workspaces this analysis read; where
no such checkout is on this machine the finding is emitted without a draft,
because naming an unverified repository would be exactly the invented target the
anchor rule prevents. This recipe emits no `store-memory` and no
`propose-reality-fact`: its findings are about the substrate everyone shares.

## Known failure modes

- **Grading thinking.** The failure this recipe is most likely to commit and the
  one the guardrail above exists to prevent. Substrate only: a proposal that
  makes exploration narrower has optimized the wrong column.
- **Frugality wearing this recipe's clothes.** A finding whose whole content is
  that a run was expensive. Expense is not waste; unmechanized retrieval is.
- **Proposing what exists.** The single most common wrong output, and the reason
  the snapshot check is mandatory rather than advisory.
- **Prompt tweaks first.** Cheap to propose, cheap to accept, and almost never
  the cause. Guidance is admissible only after the receipt shows the surface was
  available and unused.
- **Treating unpriced runs as free.** An unmeasured run supports no split, and a
  zero read as a measurement is worse than no number.
- **Deferral read as failure.** A finite run defers the rest of the frontier by
  design. A large deferred count is a finding only with a retrieval-heavy
  receipt beside it.
- **Widening authority to save budget.** A new capability, a broader grant, or a
  looser boundary proposed as an efficiency measure. Refused: the containment
  boundary is not a cost centre.
- **Ratios without incidents.** A percentage split of a run's budget that names
  no specific piece of re-derived context is not actionable.
- **Re-minting what the frontier already holds.** The same receipts every draw,
  the same proposal every day. An audit that cannot mechanize its own output has
  disproved its own thesis.
- **Compliance as correctness.** A fully evidenced mechanization proposal can
  still cost more to build and maintain than the inference it replaces, and the
  proposal has to say what it would cost.

## Examples

A useful `re-derived-held-context` observation: four runs across three weeks
each searched the corpus for the same project's deployment arrangement, and each
one's retrieval trace shows two to four steps before it found a session
mentioning it. An authorized Reality fact already records it, and no run's
receipt shows a request against the fact store. The observation quotes the four
traces, names the fact, and proposes the prevention: the job document carries
the authorized facts whose subject is in scope, so the context arrives instead of
being hunted. Classification `re-derived-held-context`, four independent runs,
one subject, temporal status `still-applicable` against the current snapshot,
which the observation shows lacks the field.

A useful `broken-mechanization` observation: one tool request kind accounts for a
run of recorded tool errors across nine runs, and in seven of them the following
requests show the run falling back to reading whole transcripts. The surface
exists, is granted, is reachable, and fails; the receipts name the error. The
proposal is a fix rather than a new surface, and the observation states the
saving in incidents prevented rather than in tokens, because the tokens depend on
what those runs would then have gone on to think about.

A useful `warranted-repetition` result: two runs issued the same query and looked
like duplicated retrieval. Their preparations show disjoint corpus slices, so
each answer was scoped to its own slice and neither could have used the other's.
Classification `warranted-repetition`, recorded with its reasoning so the next
draw does not re-mint it.

An error to avoid: "runs spend too much on the challenger stage; make it
optional to halve the cost of a cycle". It grades thinking, proposes removing a
falsification pass to save money, and names no substrate at all. The valid
version, if there is one, would name what the challenger had to reconstruct
before it could challenge, and propose serving that.
