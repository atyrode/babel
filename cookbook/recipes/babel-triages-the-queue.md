---
id: babel-triages-the-queue
version: 2
kind: meta
scope: [corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search]
default: false
---

# Babel triages the queue

> **Authorized review, off by default.** This recipe is never part of a default
> selection. It runs when the operator has authorized review work —
> `babel conductor configure --babel-triages-the-queue` — and a share of cycles
> has been allocated to it, or when `babel evaluate` is run by hand. Its subject
> is one record revision Babel itself produced, and the job document names which
> one and in which role. Everything it produces is an attributed assessment
> recorded beside that record. It records no disposition, and the surface it
> writes through cannot express one.

> **Version 2 replaces the proposal-triage contract.** Version 1 ranked unruled
> proposals within a cohort and required a counter-argument for every piece of
> advice. That contract is superseded by SPEC §4.12: reception, evidence
> checking, personal relevance and observed outcomes are four different
> questions, they apply to every kind of reviewable output rather than to
> proposals alone, and a bare vote with no prose is a complete answer. Cohort
> rank is gone. Historical v1 advice remains readable as advice and is never a
> reception vote, an exposure, or an outcome prediction; do not treat a v1 rank
> found in the record as a prior evaluation.

## Question

The job document names a record revision and a role. The question is the role's
question about that revision, and nothing else:

- **reception** — do you support this, oppose it, or are you unsure? That is a
  judgement about the idea as stated. It is not a measurement of evidence
  strength, not independent corroboration, and not a probability that the claim
  is true. Those are different objectives and they have their own roles.
- **evidence** — does the cited evidence support what the record claims? Follow
  the locators. Report what the cited material does and does not show, per
  criterion where criteria exist.
- **challenge** — given the disagreement already recorded, what is the
  strongest objection? Ground it in evidence, a consequence, a missing check or
  a concrete alternative. This role is a bounded diagnosis of a disagreement,
  not an obligation to argue until one side wins.
- **comparison** — how does this record compare with the alternatives offered
  beside it for the same problem? Say why now, what objection is unresolved,
  and what would change the recommendation where you know it.
- **outcome** — what actually happened? Whether it was implemented, whether the
  promised outcome was observed, against which criteria, in which environment,
  at what date, with what uncertainty.
- **relevance** — is this relevant to the recorded work, pain and constraints
  shown with it? Relevance is not quality: a correct finding about something
  nobody is working on is correct and not relevant, and saying so is useful.

Answer the role you were given. A reception vote does not discharge an evidence
obligation and an evidence check does not imply a vote; the coverage inventory
tracks them separately and an answer in the wrong field is refused rather than
credited to the role that asked.

**A bare answer is a complete answer.** This is the binding constraint of this
recipe and the one under pressure every time it runs. A support, opposition or
uncertainty vote with no comment is a valid reception review. A contribution
with no vote is valid. Nothing here requires prose, new evidence, a
counter-argument or an alternative, and inventing any of them to fill the result
makes the record worse: a manufactured objection is noise wearing the most
authoritative shape this recipe can produce, and a rationale invented for a bare
vote is a fabricated reason attached to an honest judgement. Where the useful
answer is one word, give one word.

**Assessment, never disposition.** The recipe may vote, contribute, check
evidence, compare and report an observed outcome. It may not accept, reject,
defer, merge or shelve, and it may not do any of those in effect by other means:
an opposing vote is not a rejection, a comparison is not a merge, and an
`unverifiable` outcome is not a refusal. The store enforces this — the
submission has no field that can express a disposition — but the guidance has to
want what the type system enforces, because a recipe straining against its own
boundary produces assessments written to be obeyed.

## Inclusion, exclusion, and ambiguity

**What you were and were not shown.** For reception, evidence, outcome and
relevance, this is an initial blind assessment. Babel has not shown you how the
record has been received, how any earlier pass ranked it, or what any prior
review of it said, and the search tool you were offered cannot reach Babel's
own output at all — the surface selector is absent from its arguments and the
request is refused if you construct one. That is procedural: it is a statement
about what Babel served, not a claim about what you remember. Judge the record
as it stands, and do not describe a reception you were not shown.

For challenge and comparison the earlier evaluations are shown, because the
disagreement is the question. They are statements, not a score: read who said
what, on what evidence, and what they were unsure about. How many agreed is
deliberately not reported and is not evidence about the claim.

The record's own body, its evidence and its criteria are shown in every role.
For a reception vote the operator's recorded disposition is withheld as well, on
the same reasoning as the tally: a recorded ruling is the loudest prior
judgement there is, and reception is the one question that has to be answered
independently of it. Every other role is *about* the lifecycle state and
receives it.

Include the record as written. The identity in the parameters names one
immutable revision, and your assessment binds to that revision rather than to
whatever the chain head becomes later. If the wording looks superseded, say so
in your uncertainty; do not assess a draft you were not given.

Include the corpus. Every claim in the record is backed by locators into
sessions, and the search tool serves exactly those sessions. For an evidence or
outcome role this is the work: open what was cited, read around it, and look for
what would contradict the claim rather than only for what repeats it.

Include the record's own counter-material. Conflicting evidence, stated risks,
open questions and declared uncertainty are the argument the author already
half-wrote. Surfacing it is not an accusation; it is reading the record
properly, and it is the most defensible objection available because every word
of it is the record's own.

Exclude anything you cannot cite. A locator Babel did not serve you is refused
whether or not its bytes exist, and an evidence contribution with no locator is
refused as a shape error. If the material you would need is not reachable from
here, that is a skip with the reason, or an `unverifiable` outcome, not a
judgement made anyway.

Exclude the mechanism. "Records of this kind keep arriving without criteria" is
a defect in the pipeline and is `babel-improves-babel`'s subject. Said here it
would be buried in an assessment about one record.

Exclude any claim about the operator. Their prior rulings are evidence about
records; a decline reason is a fact about an output rather than about the person
who wrote it.

Exclude your own run's work. If the record under review, or an alternative
offered beside it, came out of this same run, you may not support it, prefer it,
or report it implemented or verified. Opposing or being unsure about your own
run's output is accepted — a run arguing against what it produced is the honest
direction, and the refusal exists to stop the other one.

Ambiguity is the normal state of a vote. Reception depends on what the reader
values, and the record settles none of that. Where your own judgement is
genuinely balanced, `unsure` is the accurate answer and is more useful than a
confident vote with a hedged comment attached.

## Sorting cues

These are what makes one answer rather than another more defensible. They are
cues, not a scoring function, and no arithmetic over them produces a vote.

- a claim whose cited evidence, read at the locator, says something narrower
  than the claim — the commonest real defect and the one the evidence role
  exists for;
- a record whose own conflicting material or stated risk answers its outcome,
  which is an objection already written and not yet read;
- a proposal whose prerequisites name something the corpus shows does not
  exist, which is a reason to wait rather than a reason to oppose;
- a finding that rests on observations whose locators recover different bytes
  than the claim quotes;
- an outcome claim where a merge exists and a deployment does not, or a
  deployment exists and the promised effect is unmeasured — both are `partial`
  at best and neither is `verified`;
- a hypothesis stated so broadly that no observation could contradict it, which
  is a reception judgement about the statement rather than about its subject;
- alternatives that differ in what they rest on rather than in what they
  propose, where the comparison is about how cheaply each can be believed;
- a record answering an objection a prior review raised, which is the loop
  working — arguing against it with the objection it already addressed teaches
  the pipeline not to revise.

Weak cues: how long the record is, how confidently it is worded, its own
confidence or impact grading, and how recently it was written. Retrieval rank
contributes nothing. A record's own impact claim is the least reliable field it
carries, because it is the field its author had most reason to inflate. A
historical v1 triage rank, if you encounter one in the corpus, is advice
somebody wrote once and is not a prior evaluation.

## Evidence and counter-evidence

Evidence, quoted with locators, wherever you make a claim about the world:

- for an evidence contribution, the material you read and what it shows,
  quoted at the sentence that decides it;
- for a criterion result, the material that satisfies it — a criterion you
  believe holds but cannot cite is unsatisfied with your uncertainty recorded,
  never satisfied on your word;
- for an observed outcome, what shows the implementation and what shows the
  effect. They are two claims and one does not stand for the other;
- for an objection, whichever of these carries it: the record's own conflicting
  material, quoted; the cited locator that says less than the claim; the
  prerequisite the corpus does not support;
- for a comparison, what each alternative rests on, which is never widened
  beyond what its own record rests on.

A bare vote needs no evidence, and that is not an exemption. A vote is a
judgement about the record rather than an assertion about the world, so there is
nothing for a locator to prove. What needs evidence is every statement of fact —
and an observed outcome is a statement of fact, which is why an unevidenced
outcome is refused before it is stored.

Counter-evidence to seek, and seek it against your own answer rather than
against the record:

- for every objection, the record's answer to it — frequently the record
  already addresses it in a field you did not read;
- for every "the evidence does not support this", whether you opened the right
  locator and whether the claim is narrower than you read it as;
- for an `already-known` or `already-done` objection, whether what exists is the
  thing claimed or something adjacent to it;
- for an outcome, the contrary observation: a change that shipped and was
  reverted, an effect measured before the change, a criterion satisfied in one
  environment and not the one claimed;
- for a comparison preference, the context in which the other alternative wins,
  which is what makes the preference a preference rather than a ruling.

## Temporal and present-reality checks

An assessment is written about a record that may be moving, and it is read later
than it is written, so three checks precede it:

1. Is this the revision you were given? Your vote binds to the wording read,
   and Babel will not move it to a later revision. If a descendant exists, that
   is a fact for your uncertainty, not a reason to assess the descendant.
2. Is the context still current? The recorded work, pain and constraints shown
   with the record are as of a stated version. A relevance answer is about that
   version, and the same votes may yield a different recommendation when the
   recorded work changes — which is Babel's arithmetic, not yours to anticipate.
3. Does the world still look like this? Any statement shaped as "this already
   happened" is checked against a locator, and one that cannot be is not made.
   An unverifiable "this may already be done" is worse than silence.

For an outcome, name the environment and the date the observation is about.
`implemented`, `verified`, `partial`, `contradicted` and `unverifiable` are the
whole vocabulary: `verified` requires every criterion you listed to be satisfied
and evidenced, and `unverifiable` is the honest answer when the checks that
would settle it are ones you cannot make from here.

## Classifications and stopping conditions

The result's own fields are the classification. The contribution kinds are
`comment`, `evidence`, `objection`, `refinement` and `comparison`; the vote
vocabulary is `support`, `oppose` and `unsure`; the outcome vocabulary is the
five above. There is no sixth value to reach for, and a judgement that does not
fit one of them is a comment saying so.

Stop when the role's question is answered. That is frequently one field:

- Stop at the vote when you have a reception judgement and nothing to add. No
  comment is required and none should be invented.
- Stop at `no objection found` — expressed as a comment saying you looked and
  found nothing — when a hostile reading came up empty. It is an honest and
  valuable answer: it tells a later reader the obvious objection was checked,
  and a pass that never returns it has stopped discriminating.
- Stop before offering a refinement that merely rewords. A second record costs
  a second reading, and the bar is that the operator would act on the
  refinement where they would not act on the original.
- Stop at a skip when the subject needs a check you cannot make or the evidence
  is unreachable from here. Say which. A skip is recorded as a gap, consumes
  bounded attention, and never becomes an opposing vote.
- Stop at `unverifiable` rather than guessing at an outcome.

Never rule. Nothing in this recipe accepts, rejects, defers or merges, and
nothing it writes causes one of those to be recorded.

## Cross-session synthesis keys

Group by: the record's kind and the role assessed; the material it rests on —
the finding, or the claim a remedy addresses; the criterion version an outcome
was judged against; the recipe and version that produced the record; and the
recipe and version that produced the assessment.

Recurrence means something different here than in a lens, and reading it as the
same thing is the error to avoid. Repeated assessments of one record are not
stronger evidence about it — they are independent judgements, legitimately from
the same model, and none of them claims independence from the others. What does
promote is an objection that lands: one this recipe raised which the operator's
own decline reason later repeated in their own words is evidence that the
objection was a standard nobody had written down. That is a finding for
`babel-improves-babel`, phrased as a mechanism, not for this recipe to keep.

Persistent disagreement is bounded on purpose. A record with support and
opposition recorded gets a challenge assessment, once, and then stops
collecting votes: the useful output there is the diagnosis of what the two sides
disagree about, not a larger tally.

## Capability needs

- `corpus-search` is required and does all of the work. The record's cited
  locators, the material around them, and whatever would contradict the claim
  are all retrievals over the sessions this review was given. In a blind role
  the search reaches the corpus only: the surface selector is not in the tool's
  arguments and a constructed one is refused, because Babel's own prior output
  is exactly what an initial assessment may not read.
- No `repo-read`: a review reads the archive of what happened, and a
  repository-shaped claim is checked against the sessions that recorded the
  work. A pinned snapshot is not brokered to a review in this build, so an
  objection shaped as "the code already does this" is made from a locator or
  not made at all.
- No `sandbox-exec`: nothing here runs anything. A record's verification
  criteria are a suggestion for a person, and executing them would be acting on
  the record this recipe exists to assess.
- No `public-research`: the material is entirely local, and an objection drawn
  from the open web would be an argument the operator cannot check against
  their own record.

Dispositions this recipe emits: none, and that is the recipe rather than an
omission. Its output is one assessment — a vote, contributions, criterion
results, an observed outcome, or a skip — recorded against the revision it was
shown. It emits no `draft-issue`, no `ask-question`, no `store-memory`, no
`propose-reality-fact` and no `develop-further`: every one of those is a record
that would join the queue this recipe was asked to assess. A question you
genuinely need answered goes in the uncertainty, where the operator is already
reading.

## Known failure modes

- **Prose as proof of work.** The defining hazard of version 2, and the exact
  inverse of version 1's. A bare vote looks like a thin answer, so the
  temptation is to attach a paragraph that restates the record and concludes
  nothing. That paragraph is cost with no content. Vote and stop.
- **Manufacturing objections.** Every record can be argued against by someone
  determined enough. An objection nobody would act on is noise, and a pass that
  never finds a record acceptable has stopped discriminating.
- **Voting the evidence.** "Support because it is well evidenced" conflates two
  objectives §4.12 separates. Evidence strength is the evidence role's answer;
  reception is whether the idea should be acted on.
- **Describing a reception you were not shown.** In a blind role there is no
  tally in front of you. A sentence about how this has been received is
  invented, and it is invented in the one place the record is least able to
  correct.
- **Claiming an outcome from a merge.** A merge is not a deployment and a
  deployment is not proof of the promised effect. Two claims, two pieces of
  evidence, and `partial` where only one holds.
- **Verifying against a replacement.** Criteria resolved after acceptance are a
  later decision and stay attributed as one. Judging a record against criteria
  you would rather it had met is rewriting the target.
- **Turning a skip into a vote.** Declining is not opposing. A subject you
  cannot assess gets a skip with its reason and stays a visible gap.
- **Preferring your own.** A comparison that prefers the alternative this run
  just wrote is advocacy, and it is refused.
- **Compliance as correctness.** A fully evidenced assessment can still be the
  wrong reading, and the operator ignoring it is a legitimate outcome rather
  than a failure to be designed around.

## Examples

A complete reception review: a proposal asks for a handoff template to carry a
constraints section, resting on a finding about constraints restated after a
change. The record is clear, the finding is evidenced, and there is nothing to
add. The submission is `vote: support` and nothing else. That is the whole
answer, and a paragraph explaining it would have been a paragraph nobody needs.

A contribution with no vote: the same proposal, read by an evidence role. Two of
its three supporting observations quote the locators they cite accurately; the
third cites a session where the constraint was stated up front and dropped
anyway, which is closer to counter-evidence than support. The submission carries
one `evidence` contribution with that locator quoted and the discrepancy stated,
one criterion result marked unsatisfied with its uncertainty, and no vote —
because whether the proposal should be acted on is not this role's question.

An honest uncertainty: a hypothesis claims a class of failures comes from a
missing retrieval step. The corpus shows two instances that fit and one that
does not, and the record does not say which population it means. The submission
is `vote: unsure` with a short comment naming the ambiguity and
`uncertainty` recording that the scope of the claim decides the answer. No
objection is manufactured and no support is implied.

A skip: an outcome role is assigned to a proposal whose promised effect is a
change in provider latency. Nothing in the archive records latency, and the
review cannot measure it. The submission is `skip` with that reason. The
assignment is journalled as skipped, the reservation is released, the record
receives no vote, and the gap stays visible as a gap — which is the correct
outcome and the one an unverifiable guess would have destroyed.

An error to avoid: "oppose — three similar proposals are waiting and the backlog
is long." It reads the length of the queue as evidence about one record, it
recommends a disposition in the guise of a vote, and its subject is the pile
rather than the merit of anything in it. The valid version is a reception vote
on the record's own terms and — if the backlog itself is the problem — a
mechanism finding for the recipe whose subject that is.
