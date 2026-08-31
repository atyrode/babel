---
id: babel-improves-babel
version: 1
kind: meta
scope: [corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: false
---

# Babel improves Babel

> **Standing duty, off by default.** This recipe is never part of a default
> selection. It runs when the operator has authorized the product
> self-improvement duty — `babel conductor configure --babel-improves-babel` —
> or when it is named explicitly, and it is the recipe whose subject is Babel's
> own record of its own output. Every finding it produces is phrased for
> anyone; the operator-specific half of the same evaluation belongs to the
> personal-dimension recipe and must be routed there instead.

## Question

Which of Babel's own outputs earned their place, which were declined, and what
about how they were produced explains the difference?

Babel already records the answer and has never been asked. Every accept,
decline, refine request, duplicate marking, revive, and process-further nudge is
a durable, attributed, timestamped record; every output carries the recipe id
and version that produced it, the run receipt that names its evidence, and an
append-only revision chain showing each rewording and who asked for it. That is
an evaluation corpus, not telemetry, and it exists whether or not anyone reads
it. No new instrumentation is warranted here, and proposing one is a sign the
question was framed wrongly.

The second half of the question is what makes this more than a scoreboard. A
rate is not a finding. The finding is the mechanism: guidance that produced
unsupported claims, a stopping condition that fired too late, a synthesis key
that split one idea across four records, a retrieval step that missed the prior
record an output duplicated. A rate says where to look; only the mechanism can
be proposed against.

**Acceptance is evidence, never a target.** This is the binding constraint of
this recipe and the one that will be under pressure every time it runs. A rate
is a measurement of the pipeline, and the moment it becomes something to
maximize, the honest optimization is to say less that is surprising — the
instrument would improve its own numbers by narrowing what it is willing to
propose. No proposal from this recipe may recommend that a lens produce more
agreeable, safer, or more expected candidates, and a proposal whose predicted
effect is a higher acceptance rate through reduced diversity is a defect of this
recipe, not a finding.

## Inclusion, exclusion, and ambiguity

Include a candidate when it names a mechanism and locates the records that show
it. The locatable material is: disposition ledger entries with their ruling,
actor and timestamp; the derived review status of a record; the revision chain
of one output, with each revision's actor and stated reason; frontier statuses
and the transitions between them, including revivals out of a resting status;
typed links between records — `duplicate`, `same-concept`, `contradicts`,
`supersedes`; the recipe id and version each record was produced under, from the
run receipt; and the receipt's own counts of tool requests, denials, retrieval
steps, deferrals and rejections.

Include the process itself. "How improvement happens" is inside this recipe's
subject: a proposal accepted and never acted on, a refine request whose
revision was itself declined, a duplicate cluster nobody merged, a review
backlog that grew for a month. Those are findings about the loop rather than
about a lens, and they are the ones a person can act on cheapest.

Include declines with their reasons, in full. A decline with a stated reason is
the highest-value record in the store: it is the only place where the operator's
standard is written down in their own words against a specific output.

Exclude anything specific to this operator. A finding that only makes sense for
one person's corpus, one machine, one repository, or one working habit is a
personal-dimension finding, and the correct outcome is to say so and let the
personal recipe carry it. This split is not stylistic: the two dimensions reach
different dispositions and different audiences, and a product finding phrased as
an operator preference is a public proposal nobody outside this machine can
evaluate.

Exclude any claim about the operator. Their rulings are evidence about outputs,
not about them. No observation may characterize their judgement, attention,
consistency, or mood — a decline is a fact about a record.

Exclude an unanswered proposal counted as a decline. A disposition with an
empty ledger is unreviewed. Folding the two together manufactures a rejection
rate out of a review backlog, and the backlog is itself the more useful finding.

Exclude a rate whose denominator is too small to carry it, and exclude
comparisons between two recipes' rates as though the recipes had drawn the same
material. They did not: the conductor's ladder decides what gets analyzed, and a
lens that mostly ran on serendipity draws has been asked a harder question than
one that mostly ran on the operator's own invitations.

Ambiguity is the normal state of a rate here, and the reason this recipe is a
draft-quality instrument until its own findings have been reviewed a few times.
Acceptance depends on the output, the wording, the moment, the reviewer's
available attention, and the pile it arrived in. Where the record cannot
separate those, the observation states the candidate explanations and the
evidence for each, and stops.

Search the frontier before minting. Babel indexes its own hypotheses,
observations, findings, and recorded review answers; the job document lists the
prior records that looked related to this scope, and the same search is
available on demand with `"scope": "frontier"`. This recipe is unusually exposed
to duplicating itself, because it will meet the same acceptance pattern in the
same ledger every time it runs: where a prior record already says what a
candidate of yours would say, emit against it — refine its wording, develop
another observation onto it, revive a candidate that came to rest, or amend a
finding, naming its record id — rather than emitting a second copy for an
operator to review twice. Where a prior record is wrong, contradict it
explicitly and with evidence. Prior records carry no locator of their own and an
operator may already have declined one, so none of them is evidence.

## Sorting cues

- a disposition kind that is almost never accepted, with a decline reason
  repeated across independent records;
- a record accepted only after its third revision, where the revision reasons
  name the same missing thing each time;
- a decline reason that recurs verbatim or near-verbatim across recipes — a
  standard the guidance never states;
- a duplicate or `same-concept` cluster concentrated on one lens and one
  subject, which is a retrieval failure before it is a guidance failure;
- a candidate revived out of `rejected` and then accepted: the first ruling was
  about wording or timing, and the record shows which;
- a proposal accepted months ago whose repository state shows nothing changed;
- a `refine-requested` chain that ends in another decline;
- outputs whose receipts record denied tool requests, where the decline reason
  is thin evidence — a capability boundary showing up as a quality problem;
- a run whose deferred count dwarfs its developed records, repeatedly.

Weak cues: volume, record length, how recently something was produced, and the
order a listing happened to show. Retrieval rank contributes nothing.

## Evidence and counter-evidence

Evidence, quoted with locators, always:

- each disposition entry behind a rate: the record, the kind, the ruling, the
  actor, the time;
- the decline or refine reason verbatim — the wording is the evidence, and a
  paraphrase of a standard is not the standard;
- the revision chain, with each revision's stated reason and actor;
- the recipe id and version from the receipt of the run that produced the
  record, for every record counted;
- the frontier status history where a transition is part of the claim;
- for a proposal about code, the repository state at the pinned snapshot that
  shows the thing proposed does not already exist.

Counter-evidence to seek, and this recipe must seek it harder than a lens does,
because its subject is its own author:

- the denominator: how many records of this kind exist at all, and how many are
  still unreviewed;
- version drift: whether the records counted span two versions of the same
  recipe, in which case the rate describes neither;
- the draw: which rung scheduled the runs behind these records, and whether the
  comparison is between recipes or between the material they were handed;
- the reviewer's own stated reason contradicting the mechanism you inferred —
  that reason outranks the inference;
- a simpler explanation in the pile: a batch of records reviewed in one sitting
  shares a ruling for reasons that have nothing to do with the guidance;
- whether the defect is in the guidance at all, rather than in preparation,
  retrieval, the capability grant, or the worker boundary — proposing a recipe
  edit for a retrieval failure is the most common wrong answer available here;
- whether the change was already made: recipe versions increment, and a
  proposal against last version's wording is noise.

An observation whose counter-evidence section says "none sought" is not usable
here.

## Temporal and present-reality checks

Every claim this recipe makes is about a pipeline that is still moving, so three
checks precede any proposal:

1. Does the version still say this? Read the recipe version the records were
   produced under and the version shipping now. A defect fixed two versions ago
   is `resolved`, and reporting it wastes the review attention this recipe
   exists to conserve.
2. Does the code still look like this? Any proposal shaped as a change to Babel
   is checked against the repository at the pinned snapshot. A proposal for a
   surface that already exists is `resolved`; one whose mechanism the code no
   longer has is `historical`.
3. Is the record still unreviewed, or has it since been answered? A backlog
   finding drawn from a stale read is `unverifiable` at best.

Use `still-applicable`, `historical`, `resolved`, `regressed` (a defect the
record shows was fixed and has returned), `contradicted` (the reviewer's own
reason says otherwise), and `unverifiable`. Say which records the status was
established from.

## Classifications and stopping conditions

- `guidance-defect` — a named passage of a recipe, at a named version, that the
  records show produced a repeatable failure;
- `pipeline-defect` — the failure is in preparation, retrieval, scheduling,
  brokering, or storage rather than in guidance;
- `duplicate-pressure` — one idea recorded several times, with the retrieval
  step that should have found the first;
- `review-backlog` — outputs waiting, not declined, with the count and the age;
- `unactioned-acceptance` — accepted and nothing followed, with the repository
  or record state showing it;
- `standard-not-written-down` — a decline reason recurring across records that
  no recipe states as a requirement. Frequently the most valuable class here,
  and the one closest to a real proposal;
- `insufficient-denominator` — a pattern with too few independent records. The
  correct label for most candidates;
- `already-resolved` — the defect is fixed in a later version or in the current
  snapshot;
- `no-signal` — the rates differ and the record does not say why. An honest and
  frequent answer.

Stop when the mechanism is named, its records are located with their rulings and
reasons, and the present-state check has been done. Stop before proposing a
recipe-version change on fewer than three independent records unless a single
record carries an explicit operator statement of the standard. Stop when the
finding is better expressed as a question than as a proposal — an unclear
decline reason is precisely what a question to the operator is for, and asking
is cheaper and more accurate than inferring a standard.

Never edit the active cookbook. Every output of this recipe is a proposal
against a version, reviewed by a person; this recipe has no authority over the
guidance it is analyzing, and a run that behaves as though it does has violated
the boundary that makes self-analysis safe.

## Cross-session synthesis keys

Group by: recipe id and version; disposition kind; record kind; the decline
reason's subject, normalized; the rung that scheduled the run; and the
mechanism proposed against.

Recurrence is available to every recipe, and here it is the whole promotion
criterion: one declined record is an anecdote, and the same decline reason
across independent records, recipes and months is a standard. Count independent
records and independent reviewing sessions separately — five declines written in
one sitting are one act of review — and report both counts in the finding,
because a reviewer's decision about changing guidance depends on them.

Synthesis should cross-reference the mechanization audit's findings. The two
recipes read the same receipts for different reasons, and a quality defect whose
cause is a retrieval failure is that recipe's material rather than a guidance
change here.

## Capability needs

- `corpus-search` is required. Babel's own outputs are indexed beside the
  corpus, and every rate, cluster and recurrence claim in this recipe is a
  retrieval over that index. The `"scope": "frontier"` search is what makes the
  duplicate and prior-record obligations above possible.
- `repo-read` at a pinned snapshot is required before any proposal shaped as a
  change to Babel. Without it, every code-shaped proposal degrades to
  `unverifiable` and must be emitted as a question instead.
- No `sandbox-exec`: nothing here needs to run Babel to answer its question, and
  a self-modifying experiment is a different, explicitly operator-initiated
  activity.
- No `public-research`: the evidence is local and durable.

Dispositions this recipe emits: `draft-issue` for a proposal against the public
codebase, `ask-question` where the record cannot settle a standard,
`develop-further` where a pattern needs another pass. A `draft-issue` binds to a
repository verified from a local checkout's own git configuration, discovered
from the session workspaces this analysis read — Babel holds no repository
credential and publishes nothing, so a draft is rendered material an operator
publishes themselves. Where no checkout of the target repository is on this
machine, the anchor cannot be verified and the finding is emitted without a
draft: naming a repository nobody verified would be the hallucinated target the
anchor rule exists to prevent. `store-memory` and `propose-reality-fact` belong
to the personal dimension and are not this recipe's to emit.

## Known failure modes

- **Optimizing for acceptance.** The defining hazard. Every incentive in a
  self-evaluating instrument points at making its own numbers better, and the
  cheapest way to do that is to stop saying surprising things. Substrate,
  guidance clarity and retrieval are fair game; the diversity of what may be
  proposed is not.
- **A rate presented as a finding.** "This lens is accepted 40% of the time" is
  not actionable and not falsifiable. The finding is the mechanism.
- **Treating unreviewed as declined.** Manufactures a rejection rate out of a
  backlog and hides the backlog.
- **Averaging across recipe versions.** Two versions of one lens are two
  instruments, and a rate spanning both describes neither.
- **Proposing prose where the defect is plumbing.** A retrieval miss becomes a
  paragraph of guidance telling the model to search harder. The paragraph
  already exists; the retrieval is the problem.
- **Characterizing the reviewer.** Out of bounds. Rulings are evidence about
  records.
- **Claiming authority.** Nothing here is a fact about reality, and nothing here
  changes the cookbook. This recipe observes, asks and proposes.
- **Crossing the dimension line.** An operator-specific amendment emitted as a
  public draft issue is a proposal nobody else can evaluate and a leak of this
  operator's working context into a public artifact.
- **Re-minting what the frontier already holds.** This recipe will meet the same
  ledger on every draw, and a duplicate emitted here is a duplicate an operator
  has to read.
- **Compliance as correctness.** A fully evidenced acceptance observation can
  still be a coincidence of one busy week.

## Examples

A useful `standard-not-written-down` observation: eleven declined observations
across three lenses and five weeks carry decline reasons that all say some
version of "the claim is about intent, not about the record". The wording is
quoted for each; three of the eleven are the operator's own phrasing repeated
almost verbatim. Two of the three lenses state no such exclusion in their
inclusion section, and the third states it only for one class of claim.
Classification `standard-not-written-down`, eleven independent records over four
reviewing sessions, temporal status `still-applicable` against the current
versions. The disposition is a `draft-issue` anchored to the verified Babel
checkout, proposing the exclusion as guidance in the two lenses that lack it.

A useful `duplicate-pressure` observation: one candidate idea appears as four
hypotheses over six weeks, each from a different run, three of them under the
same lens; two were declined as duplicates and two are still unreviewed; the
receipts show each of the four runs performed a frontier search whose recorded
steps did not include the term the four records share. Classification
`duplicate-pressure`; the proposal is a retrieval change, not a guidance
change, and it names the mechanization audit as the recipe whose subject it is.

A candidate correctly rejected: one lens's five observations were all declined,
which looked like a guidance defect. The counter-evidence found that all five
were produced by two runs on the same day, both scheduled from serendipity
draws over a corpus slice with almost no material for that lens, and all five
declines were written in one sitting with the reason "nothing here to work
with". Classification `insufficient-denominator`, recorded with its reasoning,
because the negative result is what stops the same candidate being minted next
week.

An error to avoid: "the human-agent coordination lens underperforms; tighten its
inclusion criteria to raise acceptance". It grades a lens on agreeableness,
proposes narrowing what may be discovered, names no mechanism and no records,
and its predicted effect is a quieter instrument. The valid version names the
passage, the records, the reasons, and what a reader would expect to change
about the output rather than about the reviewer's response to it.
