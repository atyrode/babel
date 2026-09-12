---
id: babel-files-its-output
version: 1
kind: meta
scope: [corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search]
default: false
---

# Babel files its output

> **Authorized filing, off by default.** This recipe is never part of a default
> selection. It runs under the evaluation policy as its own draw kind beside
> coverage, exploration and discovery (SPEC.md §4.12, §4.13), on records that
> are unfiled or filed only by a heuristic. Its subject is one record Babel
> produced, and the job document names which one. It records no judgement about
> that record: no vote, no evidence check, no outcome.

> **You may not create a topic.** A topic is a Reality Ledger entity, and
> entities are created by an attributed operator act (§4.8). You may file a
> record under an entity that already exists, or propose one and let the
> operator decide. A name you use that the ledger cannot resolve becomes a
> proposal, not a new thing.

## Question

The question is what this record is *about*: the topic a reader would look for
it under, months from now, when they remember the subject and not the sentence.

A topic is a thing in the world — a repository, a project, a machine, a
service, an organization, a concept the operator actually thinks in. It has a
name, and it has a binding to something real: a remote, a checkout, a hostname,
or a one-sentence definition somebody could disagree with. *A topic is not a
folder.* The directory a session was started in is where the work happened, not
what it was about; a path under `/tmp`, a generated worktree name, and a
scratch checkout are locators, and a locator is evidence about a topic and
never the topic itself.

Three answers are available and exactly one of them is right for any record.

- **File it** under a topic that already exists. This is the answer to prefer
  and the commonest correct one. Say why the record is about that topic.
- **Propose a topic** when the record is about something no existing entity
  names. It is a question for the operator: name it, say what kind of thing it
  is, bind it to something real, and say why the topics you considered were
  wrong.
- **No topic**, with the reason, when the record is about nothing in
  particular. Some outputs are about a passing question, about the mechanism,
  about a coincidence nobody will ever look up by name. Saying so is an honest
  result and a completed pass, not a failure and not a skip.

## Inclusion, exclusion, and ambiguity

**What you were shown.** The record as written, its claim, its case, the
evidence it cites, the topics the ledger already holds with their aliases and
bindings, the repositories the cited sessions were in, and the reasons earlier
topics were retired and earlier proposals declined. The last two are why Babel
got a topic wrong before, and proposing something they already rule out is the
failure that material exists to prevent.

Include the evidence, not only the summary. Which repository the cited sessions
were in is the one binding this deployment can observe without a model, and it
is usually right for a record about code. It is not automatically right: a
record about how the operator's agents interrupt each other, discovered while
working in one checkout, is not about that repository.

Include the aliases when matching. An entity listed as `manifold` with the
alias `github.com/atyrode/manifold` and the alias `~/src/manifold` is one
topic, and a record naming any of the three is about it. Name the entity by any
of them; Babel resolves the alias.

Exclude the workspace as a topic. `tmp`, `worktree-3`, `witty-sage-crab`,
`checkout`, `src`, `work` and every other directory name are locators. If the
only candidate name you can produce is a directory, the honest answers are the
repository that directory is a checkout of, or `no_topic`.

Exclude the process. "The analysis pipeline produced this without criteria" is
a topic about Babel's own mechanism, and unless the operator's ledger already
holds an entity for that mechanism, it is `no_topic` with the reason — or a
finding for `babel-improves-babel`, which is not this recipe's output.

Exclude the invented binding. If you cannot say what a proposed topic *is* —
which remote, which host, which one sentence of definition — you have a word
rather than a subject, and a word costs the operator a question to decline.

Ambiguity is normal and is answered by filing under more than one topic only
when the record genuinely belongs under each. Where you are torn between an
existing topic and a new one, the existing one wins: a duplicate topic is a
merge somebody has to perform by hand, while a record filed slightly too
broadly is still findable.

## Sorting cues

Cues for which topic, not a scoring function.

- the repository the cited sessions were in, when the record's claim is about
  code, a build, a test, or a change in that repository;
- the entity whose alias the record's own recorded subject names already match,
  which is the operator's own vocabulary and outranks your reading of the
  prose;
- the service or machine a record about an outage, a deployment or a
  configuration is about, which is frequently *not* the repository the work
  happened in;
- a concept the operator has already named — an entity of kind `subject` — when
  the record is about a practice rather than a thing: those exist precisely
  because the operator thinks in them;
- for a record that compares two things, the thing being decided about rather
  than the thing being compared against.

Weak cues: the record's title wording, the harness that produced it, the day it
was written, how long it is, and how confident it sounds. A record's own impact
or novelty grading says nothing about what it is about.

## Evidence and counter-evidence

The rationale for a filing is an argument, so it rests on what you were shown:
the claim, the cited locators, the repositories of the cited sessions, the
aliases the topic already answers to. "This is about `manifold` because every
session it cites was in that checkout and the claim is about its test suite" is
a rationale. "This seems related to manifold" is a guess with a sentence
wrapped around it.

For a proposal, the binding is the evidence:

- a repository is bound by its remote as `host/owner/repo`, and by the
  checkouts it was seen at — never by one worktree's generated name;
- a machine is bound by its hostname, a service by the host and name it runs
  under;
- a concept is bound by one sentence that says what it is, written so the
  operator can disagree with it. If the sentence is a restatement of the name,
  there is no concept yet.

Counter-evidence to seek against your own answer:

- for every proposal, the existing topic it duplicates — search the listed
  aliases for the words in your proposed name before proposing;
- for every proposal, the declined reasons: an operator who has already refused
  this topic refused it for a reason that has probably not changed;
- for every proposal, the retired reasons: a topic that was retired as "not a
  thing anybody works on" should not come back under a new name;
- for a repository filing, whether the record is about the repository or about
  something that merely happened inside it;
- for `no_topic`, whether one of the listed topics does in fact cover it —
  `no_topic` is honest for a record about nothing, and lazy for a record you
  did not match.

## Temporal and present-reality checks

1. Is the topic current? A topic retired for a stated reason is not a topic to
   file under, and proposing it again needs evidence that the reason no longer
   holds.
2. Is the binding still real? A remote you infer from a path you were not shown
   is a guess. Bind to what the material in front of you says.
3. Is the record the one you were given? Your filing binds to this record, and
   Babel does not move it to a later revision.
4. Has the operator already answered this? A declined proposal with the same
   identity is suppressed until the evidence materially grows, so re-proposing
   it costs a decision nobody needed to make twice.

## Classifications and stopping conditions

The result's own fields are the classification: `filing` with an entity and a
rationale, `topic` with a name, kind, identity, binding, reasoning and the
topics considered, or `no_topic` with a reason. There is no fourth answer.

The `identity` of a proposal is a dedup key and matters more than the name: the
normalized remote for a repository, the common directory every worktree shares
when there is no remote, the hostname for a machine, a lowercase hyphenated
slug for a concept. The same thing proposed from two records must arrive with
the same identity, or the operator gets the same question twice.

Stop as soon as one answer is right:

- Stop at the first existing topic that clearly covers the record. Do not
  enumerate the alternatives you did not need.
- Stop at one proposal. A pass that proposes three topics for one record has
  not decided what the record is about.
- Stop at `no_topic` rather than filing under a topic that is merely nearby.
  A wrong filing is worse than an unfiled record: unfiled is a visible backlog,
  and a wrong filing is a false answer on the topic's own page.
- Stop at `skip` only when the record itself is unreadable from here. Being
  unable to choose a topic is `no_topic` with the reason, not a skip.

Never rule, never rank, never vote. Nothing here accepts, rejects, defers or
retires anything, and a filing is not an opinion about whether the record is
any good.

## Cross-session synthesis keys

Group by: the topic filed under; the identity of a proposal; the repository of
the cited sessions; the kind of the record filed; and the reason a `no_topic`
was recorded.

Recurrence means something specific here. The same proposal identity arriving
from several records is the evidence that makes a topic worth creating — that
is the signal, and it accumulates on the question rather than in any one pass.
The same `no_topic` reason arriving from many records is evidence of something
else: either a class of output nobody will ever look up, which is a finding for
`babel-improves-babel` about what Babel produces, or a topic the operator has
not created yet and does not want.

A filing that the operator later moves is the most valuable signal this recipe
produces, and it is not yours to record: the re-filing supersedes yours in the
history, with the operator's reason beside it, and a later pass reads that
history the way it reads the retired and declined reasons.

## Capability needs

- `corpus-search` is what the rationale rests on: the record's cited locators
  and the sessions around them are how you check that the topic you are about
  to file under is the one the work was actually about. A filing needs no
  citation, so the search is for your own certainty rather than for a field.
- No `repo-read`: the repository a record is about is established by the
  observed identity of the sessions that produced it and by the ledger's
  aliases, never by materializing a checkout and looking.
- No `sandbox-exec`: nothing here runs anything.

## Known failure modes

- **Filing by directory.** The strongest and most common failure: the workspace
  name is right there in the evidence and it looks like an answer. It named the
  feed's worst topics — `tmp`, and one topic per generated worktree name —
  which is why §4.13 exists.
- **Proposing what exists.** A topic proposed under a synonym of a listed
  entity is a merge the operator has to perform. Read the aliases first.
- **Inventing a kind.** A proposal is `repository` because a remote was
  observed, not because the name looks like a project. Where you have a name
  and no observation, `subject` is the honest kind and the definition is what
  the operator judges.
- **`no_topic` as an escape.** Recording no topic because matching was work is
  the failure that makes this recipe useless: the backlog empties and nothing
  is findable. `no_topic` is for records that are genuinely about nothing in
  particular.
- **Filing everything under one big topic.** A topic that collects a third of
  the corpus is a folder that learned a name. If the honest answer is "the
  whole deployment", it is usually `no_topic`.
- **Writing a rationale that restates the title.** The rationale is why *this
  record* is about *that topic*, and a reader disagreeing with the filing needs
  something to disagree with.

## Examples

*Filed under an existing topic.* A finding about a flaky integration test,
citing four sessions all observed in `github.com/atyrode/manifold`, and the
ledger lists an entity `manifold` with that remote as an alias:
`{"filing": {"entity": "manifold", "rationale": "the claim is about
manifold's integration suite and every cited session was observed in that
repository"}}`.

*Filed under a concept, not a repository.* An observation about the operator
interrupting long agent runs, discovered while working in one checkout, where
the ledger holds a `subject` entity called "agent interruption cost":
`{"filing": {"entity": "agent interruption cost", "rationale": "the claim is
about when interruption is worth its restart cost; the checkout it was noticed
in is where the work happened, not what it is about"}}`.

*Proposed.* A hypothesis about a deployment failing on one host, where the
ledger holds no entity for that host:
`{"topic": {"name": "hearth", "kind": "machine", "identity": "hearth",
"remote": "", "paths": [], "definition": "the workstation Babel's conductor
runs on", "reasoning": "three cited sessions name this host as where the
deployment failed; the closest existing topics are the repository being
deployed, which is not what the claim is about, and the deployment service,
which the claim distinguishes from the host", "considered": ["babel",
"conductor"]}}`.

*No topic.* A record observing that two unrelated analyses were produced within
a minute of each other:
`{"no_topic": {"reason": "the observation is about the timing of two runs and
is not about any subject a reader would look up; neither analysis's topic is
what the record claims"}}`.

*A name the ledger does not know.* Filing under `"entity": "atlas"` when no
entity answers to `atlas` is not refused as an error: Babel turns it into a
topic question proposing `atlas` as an operator-defined subject, with your
rationale as the definition. That is the correct outcome when `atlas` really is
a thing, and a decision the operator has to decline when it is not — which is
why guessing at a name is not free.
