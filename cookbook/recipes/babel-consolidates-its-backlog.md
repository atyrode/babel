---
id: babel-consolidates-its-backlog
version: 1
kind: meta
scope: [corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search]
default: false
---

# Babel consolidates its backlog

> **Authorized backlog work, off by default.** This recipe is never part of a
> default selection. It runs under the evaluation policy as its own draw kind
> beside coverage, exploration, discovery and filing (SPEC.md §4.12, §4.13), on
> hypotheses a run deferred and nobody came back to. Its subject is one deferred
> candidate, and the job document names which one. It records no judgement about
> that candidate: no vote, no evidence check, no outcome.

> **You may not settle anything.** Every act here is a *proposal* published
> through Babel's ordinary chain — observation, finding, proposal — that the
> operator rules on, and his acceptance is what applies it. Nothing is ever
> deleted: a candidate that is consolidated, superseded or retired keeps its
> record, its observations and its whole history, and gains one appended status
> event saying that a later record speaks for it.

## Question

Observations are evidence, not posts (§4.13). Every observation hangs off
exactly one hypothesis, findings consolidate observations, proposals address
findings — so an observation has no standing of its own and takes its
hypothesis's fate. The backlog this leaves is hypotheses that were deferred and
never revisited, and the question here is what should become of *this* one.

Five answers are available and exactly one of them is right for any candidate.

- **Consolidate** it with the candidates beside it, into a finding that says
  what their observations together show. This is the answer that makes a
  backlog smaller without losing anything.
- **Supersede** it with a newer candidate that says the same thing better.
- **Retire** it, with a reason a reader could check.
- **Promote** one of its observations to a fact about an entity the Reality
  Ledger already names.
- **Keep** it, with the reason. A backlog full of questions nobody has had time
  for is a healthy backlog, and this is frequently the right answer.

### Which act is right

`consolidate` is right when this candidate and at least one other say one
thing. Name every candidate you would fold, and write the finding: the
`pattern` their observations share, `why_it_matters`, and the `scope` the claim
holds in. Prefer folding into what an existing finding already says: if a
neighbour is already consolidated by a finding that covers this evidence too,
say so in the pattern rather than minting a second finding a reader would have
to reconcile with the first. A consolidation needs at least one observation
between the candidates it folds, because a finding with no observation behind
it is a claim with no provenance (§4.4).

`supersede` is right when a newer candidate states the *same* thing better —
more precisely, with the evidence this one lacked, or without the assumption
this one made. A candidate that says something adjacent is a second claim, not
a supersession, and superseding with it loses the question this one was asking.
Only a candidate you were shown qualifies.

`retire` is right when the candidate is not worth returning to, and the reason
has to be checkable: "the service it describes was decommissioned and no
observation was ever recorded against it" is checkable; "low value", "stale"
and "superseded by later work" with no candidate named are gradings. Nothing is
stale by a clock (§4.13): age alone is never a retirement.

`promote` is right when one of this candidate's observations is a durable fact
about something the ledger already names. Durable is the whole test: a fact is
what stays true until something changes it — where a repository lives, what a
service runs on, whether a project is dormant — and not what was observed once.
The predicate vocabulary is closed, the entity must be one you were shown, and
the operator's acceptance is what attributes the fact to him (§4.8).

`keep` is right whenever none of the other four is. It is a completed pass and
not a skip: a skip means you could not read the material, and this means you
read it and judged that nothing should happen to the candidate yet.

## Inclusion, exclusion, and ambiguity

**What you were shown.** The deferred candidate with the note the run gave when
it set it down, every observation hanging off it with the locators each cites,
the topics it is filed under, the candidates beside it that share its key terms,
the ledger's entities, and the predicates a fact may use. That is the whole of
what an act may name: an identifier that is not in front of you is refused as a
malformed result and creates nothing.

Include the deferral note. "Out of budget" and "contradicted by the second
observation" are different reasons to have stopped, and only the second is an
argument about the claim.

Include the evidence rather than the statement alone. A candidate whose
observations all point the same way is a consolidation waiting to happen; a
candidate with no observations at all has never been developed, and the honest
answers there are `keep` — if the question is still worth asking — or `retire`,
if it never was.

Exclude the neighbours you were shown for context. A candidate beside this one
is a neighbour to consider, not a claim that it is related: two candidates
share terms because the corpus is about one system, and only their evidence
says whether they are about one thing.

Exclude the fact that looks durable because it is written confidently. A
one-session observation stated as a general truth is still a one-session
observation, and promoting it would put a guess in the ledger under the
operator's own authority.

Ambiguity is normal and is answered with `keep`. Where you are torn between
consolidating and superseding, consolidate: a finding keeps both candidates'
evidence, and a supersession spends one of them.

## Sorting cues

Cues for which act, not a scoring function.

- several candidates whose observations cite the same sessions and reach the
  same conclusion — consolidate;
- a candidate whose statement a later one restates with evidence — supersede;
- a candidate about a system, a file or a workflow that demonstrably no longer
  exists — retire;
- an observation that states where something lives, what it runs on, or what
  the operator's stance toward it is — promote;
- a candidate nobody has had time for, whose question is still open — keep.

Weak cues: how long it has been deferred, how confident the statement sounds,
how many words it has, and how many neighbours happen to share a term with it.
Age is not a reason and volume is not a pattern.

## Evidence and counter-evidence

The reason for an act is an argument, so it rests on what you were shown: the
statement, the deferral note, the cited locators, the neighbours' own
observations. "These four candidates all observe the retry loop dropping its
backoff, each in a different harness" is a pattern. "These seem related" is a
guess with a sentence wrapped around it.

Counter-evidence to seek against your own answer:

- for a consolidation, whether the candidates actually say one thing or merely
  share vocabulary — read each one's observations before folding it;
- for a supersession, whether the newer candidate really covers the older one's
  question, or only part of it;
- for a retirement, whether the reason is checkable by somebody who was not
  here, and whether the candidate is merely unexplored rather than wrong;
- for a promotion, whether the claim would still be true next month, and
  whether the ledger already holds a fact that contradicts it;
- for `keep`, whether you are keeping it because it is open or because
  deciding was harder.

## Temporal and present-reality checks

1. Is the deferral still the current state? A candidate that was revived and
   deferred again is at its latest deferral, and the note that matters is the
   most recent one.
2. Is the neighbour still live? A candidate that is itself deferred can still
   supersede this one, but say so: two deferred candidates and one
   supersession leave one deferred candidate.
3. Is the fact still true? A promotion records what is true now and stays true
   until something changes it, and the ledger expires some predicates for
   exactly that reason.
4. Has the operator already answered this? A declined act on the same
   candidates is suppressed until the evidence materially grows, so proposing
   it again costs a decision nobody needed to make twice.

## Classifications and stopping conditions

The result's own fields are the classification: `consolidate` with the
candidates and the finding; `supersede` with `by` and the reason; `retire` with
the reason; `promote` with the observation, the entity, the predicate, the
value and the reason; or `keep` with the reason. There is no sixth answer.

Stop as soon as one act is right:

- Stop at one act. A pass that proposes two changes for one candidate has not
  decided what should become of it.
- Stop at `keep` rather than retiring a candidate you merely do not understand.
  A wrong retirement is worse than a deferred candidate: deferred is a visible
  backlog, and a retirement is an answer the operator has to un-make.
- Stop at `skip` only when the material itself is unreadable from here. Being
  unable to choose an act is `keep` with the reason, not a skip.

Never rule, never rank, never vote. Nothing here accepts, rejects, defers or
promotes anything by itself, and an act is not an opinion about whether the
candidate is any good.

## Cross-session synthesis keys

Group by: the act proposed; the topics the candidate is filed under; the
deferral note's reason; the predicate a promotion used; and the number of
candidates a consolidation folded.

Recurrence means something specific here. The same deferral note arriving
across many candidates is evidence about how Babel runs — a budget that stops
every run at the same place is a finding for `babel-improves-babel`, not a
backlog act. The same consolidation pattern arriving from several candidates is
the signal that a finding is overdue, and it accumulates on the proposal rather
than in any one pass.

An act the operator declines is the most valuable signal this recipe produces,
and it is not yours to record: the decline keeps his reason verbatim, suppresses
the same act until the evidence grows, and a later pass reads it the way the
filing recipe reads the declined topic reasons.

## Capability needs

- `corpus-search` is what a consolidation and a retirement rest on: the cited
  locators and the sessions around them are how you check that candidates
  saying similar things are about the same thing. An act needs no new citation
  — it cites what the candidates already cited — so the search is for your own
  certainty rather than for a field.
- No `repo-read`: what a candidate claims is established by its observations
  and their locators, never by materializing a checkout and looking.
- No `sandbox-exec`: nothing here runs anything.

## Known failure modes

- **Tidying.** Proposing an act because the backlog is large is the failure
  this recipe is most likely to produce. The backlog being large is not
  evidence about any candidate in it.
- **Consolidating vocabulary.** Four candidates that share three words and no
  evidence are four candidates, and a finding over them is a heading.
- **Retiring the unexplored.** A candidate nobody ever developed is not wrong;
  it is undeveloped, and retiring it converts "we never looked" into "we
  decided".
- **Promoting an observation into a fact the operator would not sign.** The
  ledger is his, the authority is his, and a fact he would not have written is
  one he has to contest afterwards.
- **Superseding to make room.** A supersession is a claim that two candidates
  say one thing; using it to drop one of two adjacent claims loses a question.


## Examples

*Consolidated.* Three deferred candidates, each observing that a different
harness drops the retry backoff after a reconnect, with five observations
between them:
`{"consolidate": {"hypotheses": ["hyp_41c8", "hyp_5d02", "hyp_77ae"],
"finding": {"title": "reconnects reset the retry backoff in every adapter",
"pattern": "all five observations show the backoff counter re-initialized on
the reconnect path rather than carried across it, in three adapters written by
different runs", "why_it_matters": "a failing endpoint is retried at full rate
after every reconnect, which is the load pattern the backoff exists to
prevent", "scope": ["babel"]}}}`.

*Superseded.* A candidate claiming the review lane starves the filing share,
and a later candidate saying the same thing with the draw's own accounting
behind it:
`{"supersede": {"by": "hyp_9b31", "reason": "hyp_9b31 says what this candidate
guessed at and shows it: it cites the draw records where the filing lane fell
through to weighted work, where this one reasoned from the share alone"}}`.

*Retired.* A candidate about a component that no longer exists:
`{"retire": {"reason": "the candidate is about the staging queue that
internal/index replaced in the same week it was written; no observation was
ever recorded against it, and the code path it names is not in the
repository"}}`.

*Promoted.* An observation recording where a repository is checked out on this
machine, under a candidate about workspace layout:
`{"promote": {"observation": "obs_2f19", "entity": "manifold", "predicate":
"local-path", "value": "/home/alex/src/manifold", "reason": "the observation
establishes where this machine keeps the checkout, which stays true until it
is moved; it is a fact about the repository rather than a claim about the
work"}}`.

*Kept.* A candidate nobody has developed, asking a question that is still
open:
`{"keep": {"reason": "the candidate asks whether reviewers agree more about
findings than about proposals, which nothing here answers and nothing
supersedes; it has no observations because nobody has run the comparison, not
because the question was settled"}}`.

*A candidate you were not shown.* Naming `hyp_0000` in a consolidation is
refused as a malformed result and creates nothing — not turned into anything
else. The candidates beside this one are the whole set an act may name, and an
identifier from somewhere else is a candidate this pass never read.