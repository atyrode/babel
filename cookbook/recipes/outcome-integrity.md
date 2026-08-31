---
id: outcome-integrity
version: 2
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: true
---

# Outcome integrity and unresolved state

## Question

What was actually asked for, what was claimed, what changed, and what was
verified — and where do those four diverge?

This is fruitful because the four are recorded in different places and by
different authorities. The request is the operator's words. The claim is the
agent's summary. The change is a repository state. The verification is a
command result, and it is the only one of the four that was produced by
something other than a participant describing its own work. Where a summary
says "fixed and tested" and no test ran, the divergence is a fact about the
record rather than an inference about anyone's intent, and it is exactly the
kind of fact a transcript preserves and nobody re-reads.

Unresolved state is the second half of the question. Work is abandoned
mid-thread, deferred with an explicit "later", blocked on a decision nobody
returned to, or reopened after a fix regressed. Sessions end for reasons that
have nothing to do with completion, so a thread's last message is weak
evidence about its outcome and a strong pointer to where to look.

## Inclusion, exclusion, and ambiguity

Include a candidate when the record contains at least two of the four —
request, claim, change, verification — and they can be compared. A claim with
a corresponding change and no verification is includable. A claim alone is
includable only as a question about missing verification, never as a claim
that the work is incomplete.

Include genuinely resolved work. A lens that only reports failures produces a
biased corpus picture and makes the recurring, well-executed pattern
invisible; the effective-patterns lens depends on this lens not discarding
successes.

Exclude the operator changing their mind: a superseded request is not an
unfinished one. Exclude work whose scope the operator explicitly reduced —
that is a decision, and the decision-quality lens is where a decision belongs.
Exclude judgements about whether a solution was good; this lens is about
correspondence between record and reality, not quality. Exclude any
observation whose only support is that a summary sounds confident.

Ambiguity is common and must survive rather than be resolved by preference.
A verification may exist outside the transcript, on another machine, or in a
later session; absence of recorded verification is absence of evidence, and
the observation says so. Multi-part requests are partially satisfiable, and
"incomplete" needs the specific unsatisfied part with its locator. A retried
command that eventually passed is resolution, not failure, and the retries are
evidence about cost, not about outcome.

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

Cues that a candidate here is worth attention, in rough order of strength:

- a claimed verification with no recorded command, or a recorded command whose
  status contradicts the claim;
- a request whose specific acceptance criterion is never mentioned again;
- a test or check that was narrowed, skipped, or made conditional in the same
  work that claimed it passed;
- a fix that a later session revisits at the same location;
- an explicit deferral ("for now", "follow-up", "leaving the old path") with no
  later session touching it;
- a session whose final exchange is an unanswered question or an error;
- a change that touches fewer of the request's named targets than the request
  named.

Weak cues, useful only in combination: message volume, elapsed time, and
summary tone. Retrieval rank is not one of these cues at all; a result's
position in a search never contributes to how strong an observation is.

## Evidence and counter-evidence

Evidence to seek, each carrying the locator that recovers it:

- the request as the operator wrote it, quoted rather than paraphrased;
- the agent's completion claim, quoted, with its position in the thread;
- tool observations that show a file was written, a command ran, and what its
  exit status was;
- repository state at the pinned snapshot: does the described change exist,
  and does the described test exist and cover the described case;
- an experiment in a disposable clone that runs the check the record claims
  passed.

Counter-evidence to seek with equal effort:

- verification recorded elsewhere — a later session, a sibling artifact, a
  different tool result — before concluding it never happened;
- an intentional partial delivery the operator accepted;
- a later change that resolved the gap without referring to it;
- a request the operator withdrew;
- the possibility that the check the analysis ran differs from the check the
  record described, which makes the experiment's result about the analysis
  rather than about the work.

An observation with no counter-evidence section says explicitly that it looked
and found none. Silence is not a clean bill of health.

## Temporal and present-reality checks

Every candidate carries a time. Ask three questions before it becomes an
observation:

1. What was observable at the time of the session? A gap that existed then may
   have been closed since, and the transcript cannot know that.
2. What is observable now, at the pinned repository snapshot? Present state is
   the only evidence about the present, and it is evidence only for the
   snapshot and command environment the receipt records.
3. What is unverifiable? Machine state, credentials, and out-of-band checks are
   frequently unrecoverable, and the honest status is `unverifiable`.

Use `historical` for a gap closed since, `still-applicable` for one present at
the snapshot, `resolved` for one the record itself closes, `regressed` for a
closure that later reverted, `contradicted` when reality disagrees with the
claim, and `unverifiable` when the evidence needed cannot be reached. Never
infer the present from the confidence of a past conversation.

## Classifications and stopping conditions

Suggested classifications, to be used when they fit and ignored when they do
not:

- `claim-without-verification` — a completion claim with no recorded check;
- `verification-contradicts-claim` — a recorded check disagrees with the claim;
- `partial-delivery` — a specific named requirement is unmet;
- `silent-deferral` — work was postponed without being recorded as open;
- `regression` — a previously verified behaviour no longer holds;
- `resolved` — the record and present state agree that the work completed;
- `unresolvable-from-record` — the evidence needed is not in the corpus.

Stop investigating a candidate when: the divergence is established with
locators on both sides; or the evidence needed is identified as unreachable; or
three retrieval attempts across sessions and repository state have produced no
new evidence; or the candidate has become a question about a decision rather
than about correspondence, in which case hand it to the decision-quality lens
rather than continuing here. Stop before speculating about why a gap exists.

## Cross-session synthesis keys

Group observations by: the file or symbol the work touched; the stated
requirement, normalized; the check that was claimed; the repository or entity;
and the request-to-verification shape (claimed and verified, claimed and
unverified, verified and contradicted).

Recurrence is a property this lens reads, not its subject — every lens may
notice recurrence, and a repeated gap belongs to whichever lens explains it.
The same unverified claim shape appearing across unrelated work is materially
stronger than one instance, and the finding says how many independent
sessions support it. Two observations from the same session are one session's
worth of evidence.

## Capability needs

- `corpus-search` is required: comparing request, claim, and later revisits is
  a retrieval problem.
- `repo-read` at a pinned snapshot turns "claimed" into "present or absent".
  Without it, every present-tense conclusion degrades to `unverifiable` and
  the observation must say so.
- `sandbox-exec` is optional and is the only way to test whether a claimed
  check actually passes. Its results apply to the recorded snapshot and command
  environment only.

With none of the three granted beyond search, this lens still produces useful
observations about internal consistency of the record, and must present them
as exactly that.

## Known failure modes

- **Treating an unrecorded check as a missing check.** The most common error
  here, and the reason counter-evidence retrieval is mandatory.
- **Reading tone as evidence.** A hedged summary of finished work and a
  confident summary of unfinished work are both common.
- **Diagnosing the participants.** Attributing a gap to carelessness or
  overconfidence is out of bounds; the observation describes the record.
- **Snapshot blindness.** Concluding a fix is missing from a snapshot that
  predates it.
- **Counting instead of understanding.** Many small unverified claims are not
  automatically worse than one unverified claim about a security boundary.
- **Compliance as correctness.** Producing every field this lens asks for does
  not make the interpretation right; a well-formed observation can be wrong.
- **Re-minting what the frontier already holds.** One idea recorded four times
  across four runs, each copy carrying its own review history and nothing in any
  of them saying it is the fourth. Babel warns when a new candidate's statement
  closely overlaps an existing one, and records that warning on the candidate
  rather than dropping it — so a duplicate emitted here is a duplicate an
  operator has to read.

## Examples

A useful observation: the operator asked for a specific error path to be
covered by a test; the agent reported the path fixed and covered; the pinned
snapshot contains the fix and a test whose assertion covers only the success
path. Evidence: the request, the claim, the test body, all with locators.
Counter-evidence sought: a second test elsewhere covering the error path —
searched, not found. Classification `partial-delivery`, temporal status
`still-applicable`. This is worth review because the gap is specific,
currently true, and cheap to close.

A useful observation of resolution: a request, a claim, a recorded command
whose status was success, and a later session that touched the same code
without reopening the issue. Classification `resolved`. It is worth keeping
because it is the control case that stops the corpus from looking like a
history of failures, and because the pattern that produced it may be reusable.

A candidate correctly rejected: a summary saying "should be fine now" with no
verification, where the change is a comment edit. The divergence is real and
the consequence is nil; recording it would spend operator attention on noise.
Rejection is preserved with its reason, because a rejected candidate is still
part of the frontier.

An error to avoid: concluding from an ended session that work was abandoned.
Sessions end because the operator stopped, the context filled, or the machine
slept. The candidate is a place to look, not an outcome.
